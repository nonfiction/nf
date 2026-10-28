package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/passwords"
)

func TestManagedEnvCredentialsAcrossSnapshotAndPrefixChanges(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, prefix := range []string{"wp_", "wpmc_"} {
			name := prefix + "derived"
			if explicit {
				name = prefix + "explicit"
			}
			t.Run(name, func(t *testing.T) {
				t.Setenv("NF_CONFIG_HOME", t.TempDir())
				t.Setenv("NF_STATE_HOME", t.TempDir())
				t.Setenv("NF_DATA_HOME", t.TempDir())
				t.Setenv("NF_PASSWORD_SALT", "credential-regression-salt")
				if err := saveGlobalConfig(map[string]string{"default_wp_user": "owner", "default_wp_email": "owner@example.test"}); err != nil {
					t.Fatal(err)
				}
				cfg := envConfig{ProjectSlug: "client", PasswordVersion: "3", EnvDir: t.TempDir(), WordpressService: "wordpress", ThemeSlug: "twentytwentyfive"}
				wantDB := passwords.DerivePassword("client:v3", "mysql", "credential-regression-salt")
				wantAdmin := passwords.DerivePassword("client:v3", "wp-admin", "credential-regression-salt")
				wantUser, wantEmail := "owner", "owner@example.test"
				if explicit {
					cfg.DBUser, cfg.DBPassword = "custom-db-user", "custom-db-password"
					cfg.AdminUser, cfg.AdminPassword, cfg.AdminEmail = "custom-admin", "custom-admin-password", "custom@example.test"
					wantDB, wantAdmin = cfg.DBPassword, cfg.AdminPassword
					wantUser, wantEmail = cfg.AdminUser, cfg.AdminEmail
				}
				// Existing healthy containers still use their original credentials even
				// when an unhydrated config rewrites the generated files on disk.
				if err := os.WriteFile(filepath.Join(cfg.EnvDir, ".env"), []byte("WP_TABLE_PREFIX=wp_\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				fixture := filepath.Join(t.TempDir(), "nf-env-credentials-fixture")
				if err := os.WriteFile(fixture, []byte("#!/bin/sh\nset -eu\nprintf '%s\\n' \"$*\" >> commands.log\nif [ \"$1\" = up ]; then cp .env recreated.env; fi\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				cfg.Compose = fixture
				oldOutput := runCommandSpecOutputSilentFn
				runCommandSpecOutputSilentFn = func(spec execSpec) (string, error) { return prefix, nil }
				t.Cleanup(func() { runCommandSpecOutputSilentFn = oldOutput })

				assertCredentials := func(path, wantPrefix string) {
					t.Helper()
					values, err := config.ReadEnvFile(path)
					if err != nil {
						t.Fatal(err)
					}
					for key, want := range map[string]string{"DB_PASSWORD": wantDB, "ADMIN_PASSWORD": wantAdmin, "ADMIN_USER": wantUser, "ADMIN_EMAIL": wantEmail, "WP_TABLE_PREFIX": wantPrefix, "DB_USER": firstNonEmpty(cfg.DBUser, "client")} {
						if values[key] != want {
							t.Errorf("%s: %s does not match expected credential/prefix", filepath.Base(path), key)
						}
					}
				}
				if err := ensureEnvReadyForSnapshot(cfg); err != nil {
					t.Fatal(err)
				}
				assertCredentials(filepath.Join(cfg.EnvDir, ".env"), "wp_")
				commands, err := os.ReadFile(filepath.Join(cfg.EnvDir, "commands.log"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Count(string(commands), "\n") != 2 || !strings.Contains(string(commands), "core is-installed") || !strings.Contains(string(commands), "theme is-active") {
					t.Fatalf("expected healthy-container early return; commands = %s", commands)
				}
				if err := applyLocalWordPressTablePrefix(cfg, prefix); err != nil {
					t.Fatal(err)
				}
				if prefix != "wp_" {
					assertCredentials(filepath.Join(cfg.EnvDir, "recreated.env"), prefix)
				} else if _, err := os.Stat(filepath.Join(cfg.EnvDir, "recreated.env")); !os.IsNotExist(err) {
					t.Fatal("unchanged prefix recreated WordPress")
				}
				// Repeat readiness and ordinary commands after the transfer. All
				// command execution uses the fixture, including down and logs.
				if err := ensureEnvReadyForSnapshot(cfg); err != nil {
					t.Fatal(err)
				}
				for _, command := range []string{"shell", "wp", "logs", "down"} {
					if err := (envCommandRunner{name: command, cfg: cfg}).Execute("", []string{"db", "check"}); err != nil {
						t.Fatal(err)
					}
					assertCredentials(filepath.Join(cfg.EnvDir, ".env"), prefix)
				}
			})
		}
	}
}

func TestEnsureManagedEnvDoesNotRequireBootstrapEmail(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	t.Setenv("NF_STATE_HOME", t.TempDir())
	t.Setenv("NF_DATA_HOME", t.TempDir())
	t.Setenv("NF_PASSWORD_SALT", "credential-regression-salt")
	cfg := envConfig{ProjectSlug: "client", EnvDir: t.TempDir()}
	if err := ensureManagedEnv(cfg); err != nil {
		t.Fatalf("existing env commands must not require bootstrap email: %v", err)
	}
	if _, err := envConfigWithAdminCredentials(cfg); err == nil {
		t.Fatal("WordPress bootstrap must still require configured admin email")
	}
}

func TestEnsureManagedEnvCredentialFailureLeavesFilesUntouched(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	t.Setenv("NF_STATE_HOME", t.TempDir())
	t.Setenv("NF_DATA_HOME", t.TempDir())
	t.Setenv("NF_PASSWORD_SALT", "")
	t.Setenv("NF_SECRET_SALT", "")
	cfg := envConfig{ProjectSlug: "client", EnvDir: t.TempDir()}
	envPath := filepath.Join(cfg.EnvDir, ".env")
	original := "DB_PASSWORD=existing-password\nWP_TABLE_PREFIX=wpmc_\n"
	if err := os.WriteFile(envPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureManagedEnv(cfg); err == nil {
		t.Fatal("expected missing password salt error")
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatal("credential failure overwrote generated environment")
	}
}

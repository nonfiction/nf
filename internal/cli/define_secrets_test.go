package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/project"
)

func TestDefineSecretStoreRoundTripDoesNotExposePlaintext(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	t.Setenv("NF_PASSWORD_SALT", "nf_test-secret-store-salt")
	root := t.TempDir()
	ref := "wpdef_0123456789abcdef0123456789abcdef"
	metadata := &projectMetadata{WordPress: project.WordPress{Defines: []any{
		map[string]any{"name": "CLIENT_API_KEY", "secret": ref},
	}}}
	store, err := newDefineSecretStore()
	if err != nil {
		t.Fatalf("newDefineSecretStore() error = %v", err)
	}
	store.Secrets[ref] = "sensitive-value-for-test"
	if err := writeDefineSecretStore(root, store); err != nil {
		t.Fatalf("writeDefineSecretStore() error = %v", err)
	}
	ciphertext, err := os.ReadFile(defineSecretStorePath(root))
	if err != nil {
		t.Fatalf("ReadFile(nf.age) error = %v", err)
	}
	if strings.Contains(string(ciphertext), "sensitive-value-for-test") {
		t.Fatal("nf.age contains plaintext")
	}
	if !strings.Contains(string(ciphertext), "BEGIN AGE ENCRYPTED FILE") {
		t.Fatal("nf.age is not ASCII armored")
	}
	loaded, err := loadDefineSecretStore(root, metadata, false)
	if err != nil {
		t.Fatalf("loadDefineSecretStore() error = %v", err)
	}
	if got := loaded.Secrets[ref]; got != "sensitive-value-for-test" {
		t.Fatalf("loaded secret = %q", got)
	}
	t.Setenv("NF_PASSWORD_SALT", "nf_test-wrong-secret-store-salt")
	if _, err := loadDefineSecretStore(root, metadata, false); err == nil || !strings.Contains(err.Error(), "current NF_PASSWORD_SALT") {
		t.Fatalf("wrong salt error = %v", err)
	}
}

func TestRunDefineListDoesNotDecryptOrExposeSecretMetadata(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	t.Setenv("NF_PASSWORD_SALT", "")
	ref := "wpdef_0123456789abcdef0123456789abcdef"
	setupTestNFProjectWithMetadata(t, map[string]any{
		"version": 2,
		"project": map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{
			"themes":  []any{"twentytwentyfive"},
			"defines": []any{map[string]any{"name": "CLIENT_API_KEY", "secret": ref}},
		},
	})
	output := captureStdout(t, func() {
		if got := Run([]string{"define", "list"}); got != 0 {
			t.Fatalf("Run(define list) = %d", got)
		}
	})
	if !strings.Contains(output, "CLIENT_API_KEY") || !strings.Contains(output, "encrypted secret") {
		t.Fatalf("define list output = %q", output)
	}
	if strings.Contains(output, ref) {
		t.Fatalf("define list exposed opaque secret reference: %q", output)
	}
}

func TestWriteDefineSecretStoreRejectsOversizedPlaintextWithoutReplacingStore(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	t.Setenv("NF_PASSWORD_SALT", "nf_test-secret-store-size-salt")
	root := t.TempDir()
	ref := "wpdef_0123456789abcdef0123456789abcdef"
	store, err := newDefineSecretStore()
	if err != nil {
		t.Fatal(err)
	}
	store.Secrets[ref] = "original"
	if err := writeDefineSecretStore(root, store); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(defineSecretStorePath(root))
	if err != nil {
		t.Fatal(err)
	}
	store.Secrets[ref] = strings.Repeat("x", defineSecretStoreMaxJSONSize)
	if err := writeDefineSecretStore(root, store); err == nil || !strings.Contains(err.Error(), "plaintext size limit") {
		t.Fatalf("oversized write error = %v", err)
	}
	after, err := os.ReadFile(defineSecretStorePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("oversized write replaced the existing encrypted store")
	}
}

func TestRunDefineMigrateEnvPreservesMappingsAndDeletesSource(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	t.Setenv("NF_PASSWORD_SALT", "nf_test-migrate-env-salt")
	root := setupTestNFProjectWithMetadata(t, map[string]any{
		"version": 2,
		"project": map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{
			"themes": []any{"twentytwentyfive"},
			"defines": []any{
				map[string]any{"name": "PLUGIN_KEY", "values": map[string]any{
					"local": map[string]any{"env": "PLUGIN_KEY_LOCAL"},
					"live":  map[string]any{"env": "PLUGIN_KEY_LIVE"},
				}},
				map[string]any{"name": "MAPS_KEY", "values": map[string]any{
					"local":   map[string]any{"value": ""},
					"default": map[string]any{"env": "MAPS_KEY"},
				}},
			},
		},
	})
	envContents := "# project secrets\nPLUGIN_KEY_LOCAL=local-secret\nPLUGIN_KEY_LIVE='live-secret'\nMAPS_KEY=maps-secret\nEXTRA_SECRET=extra-secret\n"
	envPath := filepath.Join(root, ".env")
	if err := os.WriteFile(envPath, []byte(envContents), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	beforeManifest, err := os.ReadFile(filepath.Join(root, "nf.json"))
	if err != nil {
		t.Fatalf("ReadFile(nf.json) error = %v", err)
	}
	dryRunOutput := captureStdout(t, func() {
		if got := Run([]string{"define", "migrate-env", "--dry-run"}); got != 0 {
			t.Fatalf("Run(define migrate-env --dry-run) = %d", got)
		}
	})
	for _, secret := range []string{"local-secret", "live-secret", "maps-secret", "extra-secret"} {
		if strings.Contains(dryRunOutput, secret) {
			t.Fatalf("dry-run output leaked %q", secret)
		}
	}
	if _, err := os.Stat(defineSecretStorePath(root)); !os.IsNotExist(err) {
		t.Fatalf("nf.age exists after dry-run: %v", err)
	}
	afterDryRun, _ := os.ReadFile(filepath.Join(root, "nf.json"))
	if string(afterDryRun) != string(beforeManifest) {
		t.Fatal("dry-run changed nf.json")
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"define", "migrate-env", "--delete-source"}); got != 0 {
			t.Fatalf("Run(define migrate-env --delete-source) = %d", got)
		}
	})
	for _, secret := range []string{"local-secret", "live-secret", "maps-secret", "extra-secret"} {
		if strings.Contains(output, secret) {
			t.Fatalf("migration output leaked %q", secret)
		}
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf(".env still exists after migration: %v", err)
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		t.Fatalf("loadProjectMetadataOrError() error = %v", err)
	}
	store, err := loadDefineSecretStore(root, metadata, false)
	if err != nil {
		t.Fatalf("loadDefineSecretStore() error = %v", err)
	}
	valuesByLocation := map[string]string{}
	for _, raw := range metadata.WordPress.Defines {
		item := raw.(map[string]any)
		name := recordValueString(item["name"])
		if ref := recordValueString(item["secret"]); ref != "" {
			valuesByLocation[name+":all"] = store.Secrets[ref]
		}
		values, _ := item["values"].(map[string]any)
		for selector, rawSpec := range values {
			spec := rawSpec.(map[string]any)
			if _, hasEnv := spec["env"]; hasEnv {
				t.Fatalf("%s:%s retained env source", name, selector)
			}
			if ref := recordValueString(spec["secret"]); ref != "" {
				valuesByLocation[name+":"+selector] = store.Secrets[ref]
			}
		}
	}
	want := map[string]string{
		"PLUGIN_KEY:local": "local-secret",
		"PLUGIN_KEY:live":  "live-secret",
		"MAPS_KEY:default": "maps-secret",
		"EXTRA_SECRET:all": "extra-secret",
	}
	for location, value := range want {
		if got := valuesByLocation[location]; got != value {
			t.Fatalf("%s value = %q, want %q", location, got, value)
		}
	}
	maps := findConfiguredDefineForTest(t, metadata, "MAPS_KEY")
	if got := maps["values"].(map[string]any)["local"].(map[string]any)["value"]; got != "" {
		t.Fatalf("MAPS_KEY local literal = %#v, want empty string", got)
	}
}

func TestRunDefineMigrateEnvRejectsMalformedInputWithoutWrites(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	t.Setenv("NF_PASSWORD_SALT", "nf_test-migrate-env-salt")
	root := setupTestNFProjectWithMetadata(t, map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}},
	})
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("GOOD=value\nBROKEN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(root, "nf.json"))
	stderr := captureStderr(t, func() {
		if got := Run([]string{"define", "migrate-env", "--delete-source"}); got != 1 {
			t.Fatalf("Run(define migrate-env) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, ".env line 2") || strings.Contains(stderr, "value") {
		t.Fatalf("migration stderr = %q", stderr)
	}
	after, _ := os.ReadFile(filepath.Join(root, "nf.json"))
	if string(after) != string(before) {
		t.Fatal("malformed migration changed nf.json")
	}
	if _, err := os.Stat(defineSecretStorePath(root)); !os.IsNotExist(err) {
		t.Fatalf("malformed migration wrote nf.age: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".env")); err != nil {
		t.Fatalf("malformed migration removed .env: %v", err)
	}
}

func TestRunDefineRekeySupportsRecipientTransition(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	oldSalt := "nf_test-old-rekey-salt"
	newSalt := "nf_test-new-rekey-salt"
	t.Setenv("NF_PASSWORD_SALT", oldSalt)
	root := setupTestNFProjectWithMetadata(t, map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}},
	})
	oldStdin := defineSecretStdin
	t.Cleanup(func() { defineSecretStdin = oldStdin })
	defineSecretStdin = strings.NewReader("first-secret\n")
	if got := Run([]string{"define", "add", "FIRST_SECRET", "--secret-stdin"}); got != 0 {
		t.Fatalf("Run(define add FIRST_SECRET) = %d", got)
	}
	_, newRecipient, err := passwords.DeriveAgeIdentity(newSalt)
	if err != nil {
		t.Fatalf("DeriveAgeIdentity(new salt) error = %v", err)
	}
	if got := Run([]string{"define", "rekey", "--add-recipient", newRecipient}); got != 0 {
		t.Fatalf("Run(define rekey --add-recipient) = %d", got)
	}

	t.Setenv("NF_PASSWORD_SALT", newSalt)
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := loadDefineSecretStore(root, metadata, false)
	if err != nil {
		t.Fatalf("new recipient could not decrypt transitioned store: %v", err)
	}
	if len(store.Recipients) != 2 {
		t.Fatalf("transition recipients = %d, want 2", len(store.Recipients))
	}
	defineSecretStdin = strings.NewReader("second-secret\n")
	if got := Run([]string{"define", "add", "SECOND_SECRET", "--secret-stdin"}); got != 0 {
		t.Fatalf("Run(define add SECOND_SECRET) = %d", got)
	}
	metadata, _ = loadProjectMetadataOrError(root)
	store, err = loadDefineSecretStore(root, metadata, false)
	if err != nil || len(store.Recipients) != 2 {
		t.Fatalf("ordinary edit did not preserve recipients: %#v, %v", store, err)
	}
	if got := Run([]string{"define", "rekey"}); got != 0 {
		t.Fatalf("Run(define rekey) = %d", got)
	}
	t.Setenv("NF_PASSWORD_SALT", oldSalt)
	if _, err := loadDefineSecretStore(root, metadata, false); err == nil {
		t.Fatal("old recipient still decrypts store after pruning")
	}
	t.Setenv("NF_PASSWORD_SALT", newSalt)
	if _, err := loadDefineSecretStore(root, metadata, false); err != nil {
		t.Fatalf("new recipient could not decrypt pruned store: %v", err)
	}
}

func findConfiguredDefineForTest(t *testing.T, metadata *projectMetadata, name string) map[string]any {
	t.Helper()
	for _, raw := range metadata.WordPress.Defines {
		item := raw.(map[string]any)
		if recordValueString(item["name"]) == name {
			return item
		}
	}
	t.Fatalf("define %s not found", name)
	return nil
}

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/project"
)

func nterchangePackageFixture(t *testing.T) (string, *projectMetadata) {
	t.Helper()
	base := nterchangeTestHomes(t)
	repo := filepath.Join(base, "source with spaces")
	packageWrite(t, repo, "nf.json", `{"version":2,"project":{"slug":"client","password_version":0},"nterchange":{"media":["public_html/upload","public_html/videos"]}}`)
	packageWrite(t, repo, "composer.json", `{"name":"example/client","require":{"example/runtime":"1.0.0"},"autoload":{"classmap":["app/"]}}`)
	packageWrite(t, repo, "composer.lock", `{"content-hash":"fixture-mocked","packages":[{"name":"example/runtime","version":"1.0.0","type":"library","dist":{"type":"zip","url":"https://example.invalid/runtime.zip","reference":"1111111111111111111111111111111111111111"}}]}`)
	for name, text := range map[string]string{
		"app/site.php": "<?php class Site {}", "conf/conf.php": "<?php // environment only", "public_html/index.php": "<?php // selected version",
		"public_html/.htaccess": "Require all granted", "public_html/assets/built.js": "reviewed asset", "lib/site.php": "<?php // site library",
		"vendor/legacy/library.php": "<?php // tracked legacy dependency",
	} {
		packageWrite(t, repo, name, text)
	}
	packageGit(t, repo, "init", "-q")
	metadata, err := project.Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	return repo, metadata
}

func packageGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=NF fixture", "-c", "user.email=fixture@example.invalid", "-c", "core.hooksPath=/dev/null"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

func packageCommit(t *testing.T, repo string) string {
	t.Helper()
	packageGit(t, repo, "add", "-A")
	packageGit(t, repo, "commit", "-qm", "Disposable packaging fixture")
	return packageGit(t, repo, "rev-parse", "HEAD")
}

func packageWrite(t *testing.T, repo, name, text string) {
	t.Helper()
	p := filepath.Join(repo, name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

// This mock proves orchestration, not Composer resolution, lock freshness or platform support.
func packageComposerMock(t *testing.T, tail string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> ` + shellQuoteArg(log) + `
case " $* " in *" --no-interaction "*) ;; *) exit 31;; esac
case " $* " in *" --no-plugins "*) ;; *) exit 32;; esac
case " $* " in *" --no-scripts "*) ;; *) exit 33;; esac
[ -z "${COMPOSER_AUTH-}${COMPOSER-}${DB_PASSWORD-}" ] || exit 34
[ "$HOME" = "$TMPDIR" ] || exit 35
case "$1" in
validate) exit 0;;
check-platform-reqs) exit 0;;
install)
 case " $* " in *" --no-dev "*) ;; *) exit 36;; esac
 [ ! -e vendor/composer/installed.json ] || exit 37
 [ ! -e vendor/nonfiction/nterchange/dirty.php ] || exit 38
 mkdir -p vendor/composer vendor/nonfiction/nterchange/nterchange vendor/nonfiction/nterchange/var
 cp composer.lock vendor/composer/installed.json
 printf '<?php // generated production autoload' > vendor/autoload.php
 printf '<?php // locked runtime' > vendor/nonfiction/nterchange/nterchange/index.php
 printf 'DEPENDENCY-SECRET-SENTINEL' > vendor/nonfiction/nterchange/.env
 printf 'DEPENDENCY-RUNTIME-SENTINEL' > vendor/nonfiction/nterchange/var/session
 ` + tail + `
 ;;
*) exit 39;;
esac
`
	path := filepath.Join(dir, "composer")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("COMPOSER_AUTH", "not-to-be-inherited")
	t.Setenv("COMPOSER", "/not/to/be/used.json")
	t.Setenv("DB_PASSWORD", "not-to-be-inherited")
	return log
}

func TestNterchangePackageSelectedCommitExclusionsAndIntegrity(t *testing.T) {
	repo, metadata := nterchangePackageFixture(t)
	excluded := []string{
		".env", ".env.production", "auth.json", "compose.yaml", "docker/runtime", "var/sessions/a", "scratch/dump.sql", "tests/fixture.php", "README.md", "package.json", "node_modules/a.js",
		"public_html/upload/image.jpg", "public_html/videos/movie.mp4", "public_html/.env", "app/private.env", "conf/auth.json", "conf/server.key", "conf/cert.pem", "conf/credentials.json", "app/.git/config", "app/sess_abc", "app/cache/generated.php", "public_html/a.sql.gz", "public_html/state.sqlite3", "lib/debug.log", "vendor/legacy/.env",
		"vendor/autoload.php", "vendor/composer/installed.json", "vendor/nonfiction/nterchange/dirty.php",
	}
	for _, name := range excluded {
		packageWrite(t, repo, name, "EXCLUDED-SENTINEL:"+name)
	}
	if err := os.Symlink("../vendor/nonfiction/nterchange/nterchange", filepath.Join(repo, "public_html/nterchange")); err != nil {
		t.Fatal(err)
	}
	ref := packageCommit(t, repo)
	packageWrite(t, repo, "app/site.php", "DIRTY-CONCURRENT-SENTINEL")
	packageGit(t, repo, "add", "app/site.php")
	packageWrite(t, repo, "public_html/untracked.php", "UNTRACKED-CONCURRENT-SENTINEL")
	log := packageComposerMock(t, "")
	t.Chdir(repo)
	var result string
	out := captureStdout(t, func() {
		if code := Run([]string{"env", "package", "--ref=" + ref}); code != 0 {
			t.Fatalf("package dispatch = %d", code)
		}
	})
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Package: ") {
			result = strings.TrimPrefix(line, "Package: ")
		}
	}
	if result == "" {
		t.Fatalf("missing result: %s", out)
	}
	data, err := os.ReadFile(filepath.Join(result, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest nterchangePackageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Commit != ref || manifest.Project != metadata.Project.Slug || len(manifest.Dependencies) != 1 || len(manifest.Media) != 2 {
		t.Fatalf("wrong provenance: %+v", manifest)
	}
	code := filepath.Join(result, "code")
	seen := map[string]bool{}
	for _, file := range manifest.Files {
		seen[file.Path] = true
		var data []byte
		if file.Target != "" {
			target, err := os.Readlink(filepath.Join(code, file.Path))
			if err != nil || target != file.Target {
				t.Fatalf("link integrity %s: %v", file.Path, err)
			}
			data = []byte(target)
		} else {
			data, err = os.ReadFile(filepath.Join(code, file.Path))
			if err != nil {
				t.Fatal(err)
			}
		}
		sum := sha256.Sum256(data)
		if file.SHA256 != hex.EncodeToString(sum[:]) || file.Size != int64(len(data)) || strings.Contains(string(data), "SENTINEL") {
			t.Fatalf("bad package integrity or leaked data: %s", file.Path)
		}
	}
	for _, name := range []string{"app/site.php", "conf/conf.php", "public_html/index.php", "public_html/assets/built.js", "public_html/.htaccess", "lib/site.php", "vendor/legacy/library.php", "vendor/autoload.php", "vendor/composer/installed.json", "vendor/nonfiction/nterchange/nterchange/index.php", "public_html/nterchange"} {
		if !seen[name] {
			t.Errorf("missing code/dependency: %s", name)
		}
	}
	for _, name := range excluded {
		if name == "vendor/autoload.php" || name == "vendor/composer/installed.json" {
			continue // Replaced by the mocked production install, never copied from source.
		}
		if _, err := os.Lstat(filepath.Join(code, name)); !os.IsNotExist(err) {
			t.Errorf("leaked excluded file %s: %v", name, err)
		}
	}
	for _, home := range []string{config.ConfigHome(), config.StateHome()} {
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Errorf("wrote config/state: %s %v", home, err)
		}
	}
	info, err := os.Stat(result)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("package privacy: %v %v", info, err)
	}
	entries, err := os.ReadDir(result)
	if err != nil || len(entries) != 2 {
		t.Fatalf("build cache/config leaked: %v %v", entries, err)
	}
	calls, err := os.ReadFile(log)
	if err != nil || strings.Count(string(calls), "\n") != 3 {
		t.Fatalf("Composer calls: %s %v", calls, err)
	}
	for _, name := range excluded {
		before, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil || string(before) != "EXCLUDED-SENTINEL:"+name {
			t.Errorf("source changed: %s %v", name, err)
		}
	}
}

func TestNterchangePackageRejectsInvalidInputs(t *testing.T) {
	for _, test := range []struct {
		name, file, contents, want string
	}{
		{"missing lock", "composer.lock", "", "requires a non-empty regular composer.lock"},
		{"invalid lock", "composer.lock", "null", "invalid composer.lock"},
		{"invalid composer", "composer.json", "{", "invalid composer.json"},
		{"escape config", "composer.json", `{"config":{"vendor-dir":"../../source/vendor"}}`, "unsupported Composer config"},
		{"path repository", "composer.json", `{"repositories":[{"type":"path","url":"../../source"}]}`, "custom Composer repositories"},
		{"missing bootstrap", "public_html/index.php", "", "requires a non-empty regular public_html/index.php"},
		{"wrong project", "nf.json", `{"version":2,"project":{"slug":"other","password_version":0},"nterchange":{}}`, "must match"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, metadata := nterchangePackageFixture(t)
			packageWrite(t, repo, test.file, test.contents)
			ref := packageCommit(t, repo)
			log := packageComposerMock(t, "exit 99")
			if _, err := packageNterchange(repo, metadata, ref); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatal("invalid input reached Composer")
			}
			assertNoNterchangePackages(t)
		})
	}
}

func TestNterchangePackageFailureCleanup(t *testing.T) {
	for _, tail := range []string{"exit 42", "echo changed >> composer.lock", "rm vendor/autoload.php", `echo '{"packages":[],"dev":false}' > vendor/composer/installed.json`} {
		t.Run(tail, func(t *testing.T) {
			repo, metadata := nterchangePackageFixture(t)
			ref := packageCommit(t, repo)
			packageComposerMock(t, tail)
			if _, err := packageNterchange(repo, metadata, ref); err == nil {
				t.Fatal("accepted failed/tampered install")
			}
			assertNoNterchangePackages(t)
			if got := packageGit(t, repo, "status", "--porcelain"); got != "" {
				t.Fatalf("failure mutated source: %s", got)
			}
		})
	}
}

func assertNoNterchangePackages(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(config.DataHome(), "nterchange", "packages"))
	if err != nil && !os.IsNotExist(err) || len(entries) != 0 {
		t.Fatalf("failed package retained build tree: %v %v", entries, err)
	}
}

func TestNterchangePackageStorageAndSymlinkGuards(t *testing.T) {
	for _, kind := range []string{"data in source", "source in data", "config as data", "state as data", "symlink data", "symlink package parent", "source link"} {
		t.Run(kind, func(t *testing.T) {
			repo, metadata := nterchangePackageFixture(t)
			if kind == "source link" {
				if err := os.Symlink("../../.env", filepath.Join(repo, "public_html/leak.php")); err != nil {
					t.Fatal(err)
				}
			}
			ref := packageCommit(t, repo)
			switch kind {
			case "data in source":
				t.Setenv("NF_DATA_HOME", filepath.Join(repo, "output"))
			case "source in data":
				t.Setenv("NF_DATA_HOME", filepath.Dir(repo))
			case "config as data":
				t.Setenv("NF_DATA_HOME", config.ConfigHome())
			case "state as data":
				t.Setenv("NF_DATA_HOME", config.StateHome())
			case "symlink data":
				link := filepath.Join(filepath.Dir(repo), "alias")
				if err := os.Symlink(repo, link); err != nil {
					t.Fatal(err)
				}
				t.Setenv("NF_DATA_HOME", filepath.Join(link, "new-output"))
			case "symlink package parent":
				if err := os.MkdirAll(config.DataHome(), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(repo, filepath.Join(config.DataHome(), "nterchange")); err != nil {
					t.Fatal(err)
				}
			}
			log := packageComposerMock(t, "")
			if _, err := packageNterchange(repo, metadata, ref); err == nil {
				t.Fatalf("accepted %s", kind)
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatal("unsafe storage/source reached Composer")
			}
			if got := packageGit(t, repo, "status", "--porcelain"); got != "" {
				t.Fatalf("unsafe operation changed source: %s", got)
			}
		})
	}
}

func TestNterchangePackageRequiresExplicitFullCommit(t *testing.T) {
	repo, metadata := nterchangePackageFixture(t)
	ref := packageCommit(t, repo)
	log := packageComposerMock(t, "")
	for _, args := range [][]string{nil, {"--ref", "HEAD"}, {"--ref", ref[:12]}, {"--ref", strings.Repeat("f", len(ref))}, {"--ref", ref, "--execute"}, {"--ref", ref, "review"}} {
		if code := cmdNterchangePackage(repo, metadata, args); code == 0 {
			t.Fatalf("accepted invalid selection/options %v", args)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("bad selection reached Composer")
	}
}

func TestNterchangePackageRejectsUnsafeLockedDependency(t *testing.T) {
	repo, _ := nterchangePackageFixture(t)
	for _, test := range []struct{ kind, source, url, ref string }{
		{"composer-plugin", "", "https://example.invalid/lib.zip", strings.Repeat("a", 40)},
		{"library", "", "file:///tmp/lib.zip", strings.Repeat("a", 40)},
		{"library", "", "https://user:password@example.invalid/lib.zip", strings.Repeat("a", 40)},
		{"library", "", "https://example.invalid/lib.zip", "main"},
		{"library", `,"source":{"type":"git","url":"/tmp/concurrent","reference":"` + strings.Repeat("a", 40) + `"}`, "https://example.invalid/lib.zip", strings.Repeat("a", 40)},
	} {
		lock := fmt.Sprintf(`{"content-hash":"test","packages":[{"name":"example/lib","version":"1.0.0","type":%q,"dist":{"type":"zip","url":%q,"reference":%q}%s}]}`, test.kind, test.url, test.ref, test.source)
		if _, err := validateNterchangeComposer(repo, []byte(lock)); err == nil {
			t.Fatalf("accepted unsafe dependency: %s", lock)
		}
	}
}

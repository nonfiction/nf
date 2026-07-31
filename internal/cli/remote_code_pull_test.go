package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWordPressOrgCodeAvailableWithClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("request[slug]") == "akismet" {
			_, _ = w.Write([]byte(`{"slug":"akismet"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	available, err := wordpressOrgCodeAvailableWithClient(server.Client(), server.URL, wordpressCodePlugin, "akismet")
	if err != nil || !available {
		t.Fatalf("public lookup = %v, %v", available, err)
	}
	available, err = wordpressOrgCodeAvailableWithClient(server.Client(), server.URL, wordpressCodeTheme, "private-theme")
	if err != nil || available {
		t.Fatalf("private lookup = %v, %v", available, err)
	}
}

func TestParseRemoteWordPressCodeInventory(t *testing.T) {
	items := parseRemoteWordPressCodeInventory("name,status\nprivate-pro,active\nakismet,inactive\nbad/name,inactive\nakismet,inactive\n")
	if len(items) != 2 || items[0].Slug != "akismet" || items[0].Active || items[1].Slug != "private-pro" || !items[1].Active {
		t.Fatalf("parseRemoteWordPressCodeInventory() = %#v", items)
	}
	active, err := remoteActiveTheme(items)
	if err != nil || active != "private-pro" {
		t.Fatalf("remoteActiveTheme() = %q, %v", active, err)
	}
}

func TestRemoteWordPressCodeScriptsUseProviderAwareCommands(t *testing.T) {
	target := envRemoteSyncTarget{WordPressPath: "/var/www/sites/client/public", WPCommand: "sudo -u www-data wp", SudoFileOps: true}
	inventory := remoteWordPressCodeInventoryScript(target, wordpressCodePlugin)
	for _, want := range []string{"sudo -u www-data wp", "plugin list --fields=name,status --format=csv"} {
		if !strings.Contains(inventory, want) {
			t.Fatalf("inventory script missing %q:\n%s", want, inventory)
		}
	}
	archive := remoteWordPressCodeArchiveScript(target, wordpressCodeTheme, "client-theme", "/tmp/pull")
	for _, want := range []string{"wp-content/themes/client-theme", "find \"$source\" -type l", "sudo tar", "sudo chmod 644"} {
		if !strings.Contains(archive, want) {
			t.Fatalf("archive script missing %q:\n%s", want, archive)
		}
	}
}

func TestExtractPulledCodeArchiveRejectsUnsafeEntries(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "bad.tar.gz")
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "../outside.php", Mode: 0o644, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, data.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractPulledCodeArchive(archive, t.TempDir(), "private-pro"); err == nil {
		t.Fatal("extractPulledCodeArchive() error = nil, want unsafe path error")
	}
}

func TestOverlayPulledCodePreservesLocalOnlyFiles(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	for _, dir := range []string{source, filepath.Join(destination, "node_modules")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "plugin.php"), []byte("remote"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "plugin.php"), []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := overlayPulledCode(source, destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "plugin.php"))
	if err != nil || string(data) != "remote" {
		t.Fatalf("overlaid plugin.php = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(destination, "package.json")); err != nil {
		t.Fatalf("local-only package.json was not preserved: %v", err)
	}
}

func TestGitWorktreeCleanIncludesUntrackedFiles(t *testing.T) {
	root := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	if err := gitWorktreeClean(root); err != nil {
		t.Fatalf("empty worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := gitWorktreeClean(root); err == nil {
		t.Fatal("gitWorktreeClean() error = nil with untracked file")
	}
}

func TestConfigureAdoptedRepoThemeMovesThemeFirst(t *testing.T) {
	metadata := &projectMetadata{}
	metadata.WordPress.Themes = []any{"twentytwentyfive", orderedObject{Pairs: []orderedPair{{Key: "slug", Value: "legacy-private"}, {Key: "source", Value: "cache"}, {Key: "note", Value: "adopted"}}}}
	if err := configureAdoptedRepoTheme(metadata, "legacy-private"); err != nil {
		t.Fatal(err)
	}
	themes, err := loadWordPressThemeSpecs(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if len(themes) != 2 || themes[0].Slug != "legacy-private" || !themeSourceIsRepo(themes[0]) || themes[0].Path != "theme" || themes[0].Note != "adopted" {
		t.Fatalf("adopted themes = %#v", themes)
	}
}

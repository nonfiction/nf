package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
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

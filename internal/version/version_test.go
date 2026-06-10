package version

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultVersionComesFromVersionFile(t *testing.T) {
	if got := DefaultVersion(); got == "" {
		t.Fatal("DefaultVersion() is empty")
	}
	if got := DefaultVersion(); got != Version {
		t.Fatalf("Version = %q, want embedded default %q", Version, got)
	}
}

func TestReleaseDateFromVersion(t *testing.T) {
	if got := releaseDateFromVersion("2030.04.05.2"); got != "2030-04-05" {
		t.Fatalf("releaseDateFromVersion() = %q, want 2030-04-05", got)
	}
	if got := releaseDateFromVersion("dev"); got != "" {
		t.Fatalf("releaseDateFromVersion(dev) = %q, want empty", got)
	}
}

func TestFillFromVCSSettingsUsesCommitAndReleaseDate(t *testing.T) {
	oldVersion, oldCommit, oldDate := Version, Commit, Date
	Version = "2030.04.05.2"
	Commit = "unknown"
	Date = "unknown"
	t.Cleanup(func() {
		Version = oldVersion
		Commit = oldCommit
		Date = oldDate
	})

	fillFromVCSSettings(map[string]string{
		"vcs.revision": "abcdef1234567890",
		"vcs.modified": "true",
		"vcs.time":     "2026-06-10T01:02:03Z",
	})

	if Commit != "abcdef1-dirty" {
		t.Fatalf("Commit = %q, want abcdef1-dirty", Commit)
	}
	if Date != "2030-04-05" {
		t.Fatalf("Date = %q, want release date 2030-04-05", Date)
	}
}

func TestLooksLikeNFRepo(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/nonfiction/nf\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod) error = %v", err)
	}
	if !looksLikeNFRepo(root) {
		t.Fatal("looksLikeNFRepo() = false, want true")
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "go.mod"), []byte("module example.com/other\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(other go.mod) error = %v", err)
	}
	if looksLikeNFRepo(other) {
		t.Fatal("looksLikeNFRepo(other) = true, want false")
	}
}

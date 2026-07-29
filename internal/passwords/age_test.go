package passwords

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
)

func TestDeriveAgeIdentityIsDeterministic(t *testing.T) {
	identity, recipient, err := DeriveAgeIdentity("nf_test-salt-with-high-entropy")
	if err != nil {
		t.Fatalf("DeriveAgeIdentity() error = %v", err)
	}
	parsed, err := age.ParseX25519Identity(identity)
	if err != nil {
		t.Fatalf("ParseX25519Identity() error = %v", err)
	}
	if got := parsed.Recipient().String(); got != recipient {
		t.Fatalf("recipient = %q, want %q", got, recipient)
	}
	if want := "age1rtcjdvc673zc7ah3aej9gn20xdjrz8lmw06gk8w3vmthkj9fdegscr2273"; recipient != want {
		t.Fatalf("recipient = %q, want known vector %q", recipient, want)
	}

	identityAgain, recipientAgain, err := DeriveAgeIdentity("nf_test-salt-with-high-entropy")
	if err != nil {
		t.Fatalf("DeriveAgeIdentity() second error = %v", err)
	}
	if identityAgain != identity || recipientAgain != recipient {
		t.Fatal("DeriveAgeIdentity() returned different values for the same salt")
	}

	otherIdentity, otherRecipient, err := DeriveAgeIdentity("nf_other-test-salt-with-high-entropy")
	if err != nil {
		t.Fatalf("DeriveAgeIdentity() other error = %v", err)
	}
	if otherIdentity == identity || otherRecipient == recipient {
		t.Fatal("DeriveAgeIdentity() returned the same values for different salts")
	}
}

func TestDeriveAgeIdentityRejectsEmptySalt(t *testing.T) {
	if _, _, err := DeriveAgeIdentity(""); err == nil {
		t.Fatal("DeriveAgeIdentity() error = nil, want error")
	}
}

func TestEnsureAgeIdentityWritesSecurelyAndDoesNotRewriteMatchingFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	path := filepath.Join(dir, "age-identity.txt")
	recipient, err := EnsureAgeIdentity(path, "nf_test-salt-with-high-entropy")
	if err != nil {
		t.Fatalf("EnsureAgeIdentity() error = %v", err)
	}
	if recipient == "" {
		t.Fatal("EnsureAgeIdentity() recipient is empty")
	}
	assertFileMode(t, dir, 0o700)
	assertFileMode(t, path, 0o600)

	oldTime := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	if _, err := EnsureAgeIdentity(path, "nf_test-salt-with-high-entropy"); err != nil {
		t.Fatalf("EnsureAgeIdentity() second error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if !info.ModTime().Equal(oldTime) {
		t.Fatalf("identity mtime = %v, want %v", info.ModTime(), oldTime)
	}
	assertFileMode(t, path, 0o600)
}

func TestEnsureAgeIdentityReplacesStaleContent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	path := filepath.Join(dir, "age-identity.txt")
	if err := os.WriteFile(path, []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := EnsureAgeIdentity(path, "nf_test-salt-with-high-entropy"); err != nil {
		t.Fatalf("EnsureAgeIdentity() error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	identity, _, err := DeriveAgeIdentity("nf_test-salt-with-high-entropy")
	if err != nil {
		t.Fatalf("DeriveAgeIdentity() error = %v", err)
	}
	if got, want := string(contents), identity+"\n"; got != want {
		t.Fatalf("identity contents = %q, want %q", got, want)
	}
	assertFileMode(t, dir, 0o700)
	assertFileMode(t, path, 0o600)
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %s = %04o, want %04o", path, got, want)
	}
}

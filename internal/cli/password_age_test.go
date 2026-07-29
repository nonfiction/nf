package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/nonfiction/nf/internal/config"
)

func TestRunPasswordAgeCommands(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_PASSWORD_SALT", "nf_test-salt-with-high-entropy")
	t.Setenv("NF_SECRET_SALT", "")

	identityOutput := captureStdout(t, func() {
		if got := runPassword([]string{"age-identity"}); got != 0 {
			t.Fatalf("runPassword(age-identity) = %d, want 0", got)
		}
	})
	identityPath := strings.TrimSpace(identityOutput)
	if want := config.AgeIdentityFile(); identityPath != want {
		t.Fatalf("age-identity output = %q, want %q", identityPath, want)
	}
	contents, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	identity, err := age.ParseX25519Identity(strings.TrimSpace(string(contents)))
	if err != nil {
		t.Fatalf("ParseX25519Identity() error = %v", err)
	}

	recipientOutput := captureStdout(t, func() {
		if got := runPassword([]string{"age-recipient"}); got != 0 {
			t.Fatalf("runPassword(age-recipient) = %d, want 0", got)
		}
	})
	if got, want := strings.TrimSpace(recipientOutput), identity.Recipient().String(); got != want {
		t.Fatalf("age-recipient output = %q, want %q", got, want)
	}
	if info, err := os.Stat(filepath.Join(configHome, "age-identity.txt")); err != nil {
		t.Fatalf("Stat() error = %v", err)
	} else if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("age identity mode = %04o, want 0600", got)
	}
}

func TestRunPasswordAgeCommandsValidateArgumentsAndSalt(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	t.Setenv("NF_PASSWORD_SALT", "")
	t.Setenv("NF_SECRET_SALT", "")

	stderr := captureStderr(t, func() {
		if got := runPassword([]string{"age-identity", "extra"}); got != 1 {
			t.Fatalf("runPassword(age-identity extra) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "password age-identity takes no arguments") {
		t.Fatalf("argument stderr = %q", stderr)
	}

	stderr = captureStderr(t, func() {
		if got := runPassword([]string{"age-recipient"}); got != 1 {
			t.Fatalf("runPassword(age-recipient) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "NF_PASSWORD_SALT is not set") {
		t.Fatalf("missing salt stderr = %q", stderr)
	}
}

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadAndUpdateEnvFile(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	path := EnvFile()
	input := "# keep me\nEXISTING=keep\nBLANK=\nexport QUOTED=\"value\"\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	values, err := ReadEnvFile(path)
	if err != nil {
		t.Fatalf("ReadEnvFile() error = %v", err)
	}
	if got, want := values["EXISTING"], "keep"; got != want {
		t.Fatalf("ReadEnvFile() EXISTING = %q, want %q", got, want)
	}
	if got, want := values["BLANK"], ""; got != want {
		t.Fatalf("ReadEnvFile() BLANK = %q, want %q", got, want)
	}
	if got, want := values["QUOTED"], "value"; got != want {
		t.Fatalf("ReadEnvFile() QUOTED = %q, want %q", got, want)
	}

	written, err := UpdateEnvFile(path, map[string]string{"BLANK": "filled", "NEW": "value"})
	if err != nil {
		t.Fatalf("UpdateEnvFile() error = %v", err)
	}
	if got, want := strings.Join(written, ","), "BLANK,NEW"; got != want {
		t.Fatalf("UpdateEnvFile() written = %q, want %q", got, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	want := "# keep me\nEXISTING=keep\nBLANK=filled\nexport QUOTED=\"value\"\nNEW=value\n"
	if got := string(data); got != want {
		t.Fatalf("updated file =\n%s\nwant=\n%s", got, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("file mode = %v, want %v", got, want)
	}
	if got, want := mustStatDir(t, filepath.Dir(path)).Mode().Perm(), os.FileMode(0o700); got != want {
		t.Fatalf("dir mode = %v, want %v", got, want)
	}
}

func mustStatDir(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s) error = %v", path, err)
	}
	return info
}

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNterchangeMixedEngineReadLockLifecycle(t *testing.T) {
	old := nterchangeOutputFn
	t.Cleanup(func() { nterchangeOutputFn = old })
	root := t.TempDir()
	record := filepath.Join(root, "queries")
	t.Setenv("NF_LOCK_TEST_RECORD", record)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	fake := "#!/bin/sh\nIFS= read -r lock\nprintf '%s\\n' \"$lock\" > \"$NF_LOCK_TEST_RECORD\"\nprintf 'nf_locked\\n'\nIFS= read -r unlock\nprintf '%s\\n' \"$unlock\" >> \"$NF_LOCK_TEST_RECORD\"\nprintf 'nf_unlocked\\n'\n"
	if err := os.WriteFile(filepath.Join(root, "docker"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	nterchangeOutputFn = func(_ string, args []string) ([]byte, error) {
		if !strings.Contains(strings.Join(args, " "), "SELECT HEX(TABLE_NAME)") {
			t.Fatalf("unsafe table inventory: %v", args)
		}
		return []byte("7573657273\n6f6464607461626c65\n"), nil
	}
	facts := nterchangeRuntimeFacts{DBID: "fixture", Database: "fixture", SQL: nterchangeDatabaseFacts{Engines: []string{"InnoDB\t1", "MyISAM\t1"}}}
	release, err := lockNterchangeMyISAM(root, facts, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(record)
	if err != nil || !strings.Contains(string(data), "LOCK TABLES `fixture`.`users` READ,`fixture`.`odd``table` READ;") || strings.Contains(string(data), "LOCAL") || strings.Contains(string(data), "UNLOCK") {
		t.Fatalf("lock not held through export: %s, %v", data, err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(record)
	if !strings.Contains(string(data), "UNLOCK TABLES;") {
		t.Fatal("lock not released")
	}
	if err := os.WriteFile(filepath.Join(root, "docker"), []byte("#!/bin/sh\nIFS= read -r lock\nprintf 'nf_locked\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	release, err = lockNterchangeMyISAM(root, facts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err == nil {
		t.Fatal("lost lock session accepted as a consistent export")
	}
	nterchangeOutputFn = func(string, []string) ([]byte, error) { return []byte("not-hex"), nil }
	if _, err := lockNterchangeMyISAM(root, facts, nil); err == nil {
		t.Fatal("invalid table inventory accepted")
	}
}

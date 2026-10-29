package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNterchangeDumpConsistencyAndCredentialBoundaries(t *testing.T) {
	for _, engine := range []string{"InnoDB\t2", "MyISAM\t1"} {
		facts := nterchangeRuntimeFacts{DBID: "db-id", Database: "fixture", SQL: nterchangeDatabaseFacts{Engines: []string{engine}}}
		for _, remote := range []bool{true, false} {
			args := strings.Join(nterchangeSQLArgs(facts, true, remote), " ")
			if !strings.Contains(args, "--skip-lock-tables") || strings.Contains(engine, "InnoDB") != strings.Contains(args, "--single-transaction") {
				t.Fatalf("inconsistent dump strategy: %s", args)
			}
			for _, forbidden := range []string{"--all-databases", "--databases", "--lock-all-tables", "--password=", "--events=true", "--routines=true"} {
				if strings.Contains(args, forbidden) {
					t.Fatalf("unsafe dump: %s", args)
				}
			}
			if remote != strings.Contains(args, "--defaults-extra-file=") {
				t.Fatalf("incorrect credential boundary: %s", args)
			}
		}
	}
}

func TestNterchangeMediaRejectsLinksAndSpecialFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ordinary"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkNterchangeMedia(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ordinary", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := checkNterchangeMedia(root); err == nil {
		t.Fatal("media symlink accepted")
	}
}

func TestNterchangeImportUsesRestrictedClientAndRestoresSchemaDefaults(t *testing.T) {
	oldOutput, oldStream := nterchangeOutputFn, nterchangeStreamFn
	t.Cleanup(func() { nterchangeOutputFn, nterchangeStreamFn = oldOutput, oldStream })
	root := t.TempDir()
	file := filepath.Join(root, "database.sql")
	if err := os.WriteFile(file, []byte("CREATE TABLE example (id INT);"), 0600); err != nil {
		t.Fatal(err)
	}
	nterchangeOutputFn = func(_ string, args []string) ([]byte, error) {
		if !strings.Contains(strings.Join(args, " "), "REPLACE(TABLE_NAME,'`','``')") {
			t.Fatalf("unsafe table identifier query: %v", args)
		}
		return []byte("DROP TABLE `old_table`;\n"), nil
	}
	calls := 0
	nterchangeStreamFn = func(_ string, args []string, input io.Reader, output io.Writer) error {
		calls++
		command := strings.Join(args, " ")
		if !strings.Contains(command, "--binary-mode --local-infile=0") || strings.Contains(command, "--user=root") || !strings.HasSuffix(command, "fixture") {
			t.Fatalf("unsafe import client: %s", command)
		}
		body, err := io.ReadAll(input)
		if err != nil || !strings.Contains(string(body), "DROP TABLE `old_table`;") || !strings.Contains(string(body), "ALTER DATABASE `fixture` CHARACTER SET latin1 COLLATE latin1_swedish_ci;") || !strings.HasSuffix(string(body), "CREATE TABLE example (id INT);") {
			t.Fatalf("incorrect import: %s, %v", body, err)
		}
		return nil
	}
	facts := nterchangeRuntimeFacts{DBID: "local-db", Database: "fixture"}
	if err := nterchangeImport(root, file, facts, nterchangeDatabaseFacts{Charset: "latin1", Collation: "latin1_swedish_ci"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("import not run")
	}
	if err := nterchangeImport(root, file, facts, nterchangeDatabaseFacts{Charset: "latin1; DROP DATABASE other", Collation: "latin1_swedish_ci"}); err == nil || calls != 1 {
		t.Fatal("unsafe charset reached import")
	}
}

func TestNterchangeTransferRejectsSourceAliasAndBroadPrivileges(t *testing.T) {
	nterchangeTestHomes(t)
	old := nterchangeOutputFn
	t.Cleanup(func() { nterchangeOutputFn = old })
	local := nterchangeRuntimeFacts{DBID: "local", Database: "fixture"}
	if err := executeNterchangePull(t.TempDir(), "client", nterchangePullTarget{}, local, local); err == nil {
		t.Fatal("same source/destination database accepted")
	}
	for _, grants := range []string{"", "GRANT ALL PRIVILEGES ON *.* TO `root`@`%`", "GRANT ALL PRIVILEGES ON `other`.* TO `fixture`@`%`", "GRANT ALL PRIVILEGES ON *.* TO ` ON `fixture`.* TO `@`%`"} {
		nterchangeOutputFn = func(string, []string) ([]byte, error) { return []byte(grants), nil }
		if err := executeNterchangePull(t.TempDir(), "client", nterchangePullTarget{}, local, nterchangeRuntimeFacts{DBID: "source"}); err == nil {
			t.Fatalf("unsafe privileges accepted: %q", grants)
		}
	}
}

func TestNterchangeTransferSQLRejectsUnsupportedObjects(t *testing.T) {
	old := nterchangeOutputFn
	t.Cleanup(func() { nterchangeOutputFn = old })
	for _, result := range []string{"1\n", "", "0\n"} {
		nterchangeOutputFn = func(string, []string) ([]byte, error) { return []byte(result), nil }
		err := checkNterchangeTransferSQL("", nterchangeRuntimeFacts{Database: "fixture"}, nil)
		if (err == nil) != (result == "0\n") {
			t.Fatalf("stored object guard: %q, %v", result, err)
		}
	}
	nterchangeOutputFn = func(string, []string) ([]byte, error) { return nil, errors.New("denied") }
	if err := checkNterchangeTransferSQL("", nterchangeRuntimeFacts{Database: "fixture"}, nil); err == nil {
		t.Fatal("failed object inspection accepted")
	}
	if err := checkNterchangeTransferSQL("", nterchangeRuntimeFacts{SQL: nterchangeDatabaseFacts{Engines: []string{"VIEW\t1"}}}, nil); err == nil {
		t.Fatal("view accepted")
	}
}

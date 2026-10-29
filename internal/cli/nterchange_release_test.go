package cli

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNterchangeReleasePackageVerification(t *testing.T) {
	for _, problem := range []string{"valid", "tampered", "unlisted", "missing", "mode", "escape-link", "identity", "lock"} {
		t.Run(problem, func(t *testing.T) {
			repo, metadata := nterchangePackageFixture(t)
			packageComposerMock(t, "")
			location, err := packageNterchange(repo, metadata, packageCommit(t, repo))
			if err != nil {
				t.Fatal(err)
			}
			code := filepath.Join(location, "code")
			switch problem {
			case "tampered":
				packageWrite(t, code, "app/site.php", "tampered")
			case "unlisted":
				packageWrite(t, code, "public_html/upload/sentinel", "persistent data")
			case "missing":
				os.Remove(filepath.Join(code, "conf/conf.php"))
			case "mode":
				os.Chmod(filepath.Join(code, "app/site.php"), 0777)
			case "escape-link":
				os.Remove(filepath.Join(code, "app/site.php"))
				os.Symlink("/etc/passwd", filepath.Join(code, "app/site.php"))
			case "identity":
				metadata.Project.Slug = "other"
			case "lock":
				packageWrite(t, code, "composer.lock", "{}")
			}
			manifest, err := verifyNterchangePackage(location, metadata)
			if problem != "valid" {
				if err == nil {
					t.Fatal("invalid package accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var stream bytes.Buffer
			if err := writeNterchangeReleaseInput(&stream, nterchangeReleaseRequest{Operation: "deploy", Manifest: &manifest}, location); err != nil {
				t.Fatal(err)
			}
			header, _, ok := bytes.Cut(stream.Bytes(), []byte{'\n'})
			if !ok || !json.Valid(header) || !bytes.Contains(stream.Bytes(), []byte("app/site.php")) {
				t.Fatal("invalid upload protocol")
			}
			packageWrite(t, code, "app/site.php", "changed after validation")
			if err := writeNterchangeReleaseInput(&bytes.Buffer{}, nterchangeReleaseRequest{Operation: "deploy", Manifest: &manifest}, location); err == nil {
				t.Fatal("upload missed changed file")
			}
		})
	}
}

func TestNterchangeReleaseArchiveRoundTrip(t *testing.T) {
	repo, metadata := nterchangePackageFixture(t)
	packageComposerMock(t, `mkdir -p vendor/nonfiction/nterchange/compat/pear/Log
printf '<?php // PEAR logging library' > vendor/nonfiction/nterchange/compat/pear/Log/file.php`)
	location, err := packageNterchange(repo, metadata, packageCommit(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := verifyNterchangePackage(location, metadata)
	if err != nil {
		t.Fatal(err)
	}
	var stream bytes.Buffer
	if err := writeNterchangeReleaseInput(&stream, nterchangeReleaseRequest{Operation: "deploy", Manifest: &manifest}, location); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "nterchange_release.py"), []byte(nterchangeReleaseScript), 0600); err != nil {
		t.Fatal(err)
	}
	// Real Go archive writer and Python receiver, without SSH, Docker or application boot.
	cmd := exec.Command("python3", "-B", "-c", `import json, sys
from pathlib import Path
import nterchange_release as worker
request = json.loads(sys.stdin.buffer.readline())
directory = Path(sys.argv[1])
worker.receive_package(sys.stdin.buffer, directory, request['manifest'])
worker.verify_code(directory / 'code', request['manifest'], mountpoints=True)
assert (directory / 'code/vendor/nonfiction/nterchange/compat/pear/Log/file.php').is_file()
`, t.TempDir())
	cmd.Dir = directory
	cmd.Stdin = &stream
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("archive round trip: %v\n%s", err, output)
	}
}

func TestNterchangeReleasePathPolicy(t *testing.T) {
	nterchangeTestHomes(t)
	cases := []string{"nf.json", "composer.json", "composer.lock", "public_html/.htaccess", "app/site.php", "app/Session/handler.php", "lib/backup/helper.php", "vendor/nonfiction/nterchange/compat/pear/Log/file.php",
		"app", "var/session", "public_html/upload/media.jpg", "public_html/videos/media.mp4", "../escape", "/absolute", "app/../escape", "app/.env", "app/.git/config", "app/sess_private", "app/dump.sql", "app/dump.sql.gz", "app/dump.sql.custom", "app/settings.env", "app/settings.env.local", "app/private.pem", "app/private.key", "app/private.p12", "app/private.pfx", "app/data.sqlite", "app/data.sqlite3", "app/data.db", "app/runtime.log", "app/source.bak"}
	for _, directory := range []string{"var", "cache", "caches", "tmp", "temp", "logs", "sessions", "scratch", "backups", "node_modules"} {
		cases = append(cases, "vendor/library/"+directory+"/sentinel.php")
	}
	for _, name := range []string{"auth.json", "credentials.json", "credentials.yml", "credentials.yaml", "credentials.ini", "secrets.json", "secrets.yml", "secrets.yaml", "secrets.ini", "id_rsa", "id_ed25519"} {
		cases = append(cases, "conf/"+name)
	}
	media := []string{"public_html/upload", "public_html/videos"}
	accepted := map[string]bool{}
	for _, name := range cases {
		accepted[name] = nterchangePackagePath(name, media, true)
	}
	data, err := json.Marshal(accepted)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "nterchange_release.py"), []byte(nterchangeReleaseScript), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "-B", "-c", `import json, sys
import nterchange_release as worker
for name, expected in json.load(sys.stdin).items():
    try:
        worker.package_path(name, ['public_html/upload', 'public_html/videos'])
        accepted = True
    except RuntimeError:
        accepted = False
    assert accepted == expected, 'Producer/receiver path policy differs: ' + name
`)
	cmd.Dir = directory
	cmd.Stdin = bytes.NewReader(data)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("path policy: %v\n%s", err, output)
	}
}

func TestNterchangeReleasePlansAndGuards(t *testing.T) {
	root, _, _ := nterchangePullTestProject(t)
	old := nterchangeReleaseRunFn
	nterchangeReleaseRunFn = func(sshTarget, *os.File) error { t.Fatal("plan or rejected args connected"); return nil }
	t.Cleanup(func() { nterchangeReleaseRunFn = old })
	out := captureStdout(t, func() {
		if code := Run([]string{"env", "rollback", "review", "--dry-run"}); code != 0 {
			t.Fatalf("plan = %d", code)
		}
	})
	if !strings.Contains(out, "Plan only") || !strings.Contains(out, "Env rollback plan:") || !strings.Contains(out, "client.test-host") {
		t.Fatal(out)
	}
	for _, args := range [][]string{{"deploy", "review"}, {"deploy", "review", "--package=/missing"}, {"rollback", "missing"}, {"rollback", "review", "--yes"}, {"rollback", "review", "--execute", "--dry-run"}, {"rollback", "review", "--package=/bad"}, {"rollback", "review", "--unexpected"}} {
		if code := Run(append([]string{"env"}, args...)); code == 0 {
			t.Fatalf("invalid args accepted: %v", args)
		}
	}
	if _, err := os.Stat(os.Getenv("NF_DATA_HOME")); !os.IsNotExist(err) {
		t.Fatalf("plan wrote data: %v", err)
	}
	link := filepath.Join(t.TempDir(), "checkout-link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	for _, location := range []string{root, os.Getenv("NF_CONFIG_HOME"), os.Getenv("NF_STATE_HOME"), link} {
		t.Run("storage-"+filepath.Base(location), func(t *testing.T) {
			t.Setenv("NF_DATA_HOME", location)
			if code := Run([]string{"env", "rollback", "review", "--execute", "--yes"}); code == 0 {
				t.Fatal("overlapping transfer storage accepted")
			}
		})
	}
}

//go:embed nterchange_release_test.py
var nterchangeReleaseWorkerTests string

func TestNterchangeReleaseWorker(t *testing.T) {
	nterchangeTestHomes(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("release safety tests require python3:", err)
	}
	directory := t.TempDir()
	for name, source := range map[string]string{"nterchange_release.py": nterchangeReleaseScript, "nterchange_release_test.py": nterchangeReleaseWorkerTests} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(python, "-B", "nterchange_release_test.py")
	cmd.Dir = directory
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("worker safety tests: %v\n%s", err, output)
	}
	t.Log(string(output))
}

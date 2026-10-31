package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/project"
)

func TestNterchangeOutputEnvDetails(t *testing.T) {
	root := nterchangeLocalTestProject(t)
	nterchangeEnvDocker(t, nterchangeEnvTestConfig)
	metadata, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	metadata.Remotes["zeta"] = "client.other-host:review"
	if err := project.Save(root, metadata); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(config.ProjectFile(root))
	if err != nil {
		t.Fatal(err)
	}
	oldSSH, oldLocal := runSSHOutputFn, nterchangeOutputFn
	runSSHOutputFn = func([]string) ([]byte, error) { t.Fatal("env details connected"); return nil, nil }
	nterchangeOutputFn = func(string, []string) ([]byte, error) { t.Fatal("env details inspected runtime"); return nil, nil }
	t.Cleanup(func() { runSSHOutputFn, nterchangeOutputFn = oldSSH, oldLocal })
	out := captureStdout(t, func() {
		if code := Run([]string{"env", "show"}); code != 0 {
			t.Fatalf("show = %d", code)
		}
	})
	_, _, dbPort := envDerivedPorts("nterchange-client-local")
	want := fmt.Sprintf(`> docker compose -f '/checkout/compose.yaml' config --format json
client:local
────────────
Site          client
Env           local
Application   nterchange
Path          /checkout
Compose       client-local
File          /checkout/compose.yaml
Services      app, db, db-ui, gateway

Database
  DB URL        http://127.0.0.1:%d/?db=client&mysql=db&username=client

Email
  Mailpit URL   http://127.0.0.1:18093

nterchange
  Site URL      http://127.0.0.1:18083

Runtime
  Var           /checkout/var

Media (excluded from code deployment)
  Path          /checkout/public_html/upload
  Path          /checkout/public_html/videos

Remotes
  review        client.test-host:review
  zeta          client.other-host:review
`, dbPort)
	if got := strings.ReplaceAll(out, root, "/checkout"); got != want {
		t.Fatalf("env show:\n%s\nwant:\n%s", got, want)
	}
	for _, secret := range []string{"never-print-this", "private-db-value", "private-root-value"} {
		if strings.Contains(out, secret) {
			t.Fatal("env show exposed a credential")
		}
	}
	after, err := os.ReadFile(config.ProjectFile(root))
	if err != nil || string(before) != string(after) {
		t.Fatal("env show changed metadata")
	}
	if _, err := os.Stat(config.DataHome()); !os.IsNotExist(err) {
		t.Fatalf("env show wrote data: %v", err)
	}
	metadata.Remotes = nil
	if err := project.Save(root, metadata); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() { Run([]string{"env", "show"}) })
	if strings.Contains(out, "\nRemotes") {
		t.Fatal("empty section rendered")
	}
}

func TestNterchangeOutputPullUsesSharedPreflight(t *testing.T) {
	root, site, _ := nterchangePullTestProject(t)
	oldSSH, oldLocal := runSSHOutputFn, nterchangeOutputFn
	runSSHOutputFn = func([]string) ([]byte, error) { t.Fatal("plan connected"); return nil, nil }
	nterchangeOutputFn = func(string, []string) ([]byte, error) { t.Fatal("plan inspected runtime"); return nil, nil }
	t.Cleanup(func() { runSSHOutputFn, nterchangeOutputFn = oldSSH, oldLocal })
	want := captureStdout(t, func() {
		printEnvRemoteSyncPreflight("pull", envConfig{ProjectSlug: "client", EnvDir: root}, envRemoteSyncTarget{
			RemoteName: "review", SiteID: "client.test-host", Env: site.Env, Provider: "ssh", TargetLabel: "target", TargetRef: "test-host",
			URL: site.URL, SSHHost: "192.0.2.1", SSHUser: "nonfiction", SSHPort: "22",
		}, "dry-run")
	})
	out := captureStdout(t, func() {
		if code := Run([]string{"env", "pull", "review", "--dry-run"}); code != 0 {
			t.Fatalf("pull plan = %d", code)
		}
	})
	if !strings.HasPrefix(out, want+"\nData:\n") {
		t.Fatalf("pull does not share WordPress preflight layout:\n%s\nwant prefix:\n%s", out, want)
	}
	for _, value := range []string{filepath.Join(root, "compose.yaml"), "public_html/videos", "credentials", "Plan only. No SSH, Docker"} {
		if !strings.Contains(out, value) {
			t.Fatalf("missing pull detail %q: %s", value, out)
		}
	}
}

func TestNterchangeOutputReleaseUsesPlanRows(t *testing.T) {
	nterchangePullTestProject(t)
	old := nterchangeReleaseRunFn
	nterchangeReleaseRunFn = func(sshTarget, *os.File) error { t.Fatal("rollback plan connected"); return nil }
	t.Cleanup(func() { nterchangeReleaseRunFn = old })
	out := captureStdout(t, func() {
		if code := Run([]string{"env", "rollback", "review", "--dry-run"}); code != 0 {
			t.Fatalf("rollback plan = %d", code)
		}
	})
	want := "Env rollback plan:\n" + strings.Join(detailRowLines([]detailRow{
		{label: "remote:", value: "review"},
		{label: "site:", value: "client.test-host"},
		{label: "env:", value: "review"},
		{label: "provider:", value: "ssh"},
		{label: "target:", value: "test-host"},
		{label: "url:", value: "https://client.example.invalid"},
		{label: "path:", value: "/srv/test/sites/client"},
		{label: "compose:", value: "/srv/test/compose.yaml"},
		{label: "app service:", value: "client"},
		{label: "preserve:", value: "database, credentials, media, var, base Compose and original source tree"},
		{label: "mode:", value: "dry-run"},
	}, 2), "\n") + "\n"
	if !strings.HasPrefix(out, want) || !strings.Contains(out, "Plan only. No SSH, writes, health requests or container changes.") {
		t.Fatalf("release plan:\n%s\nwant prefix:\n%s", out, want)
	}
}

func TestNterchangeOutputPackageSummaryAndCommands(t *testing.T) {
	repo, _ := nterchangePackageFixture(t)
	ref := packageCommit(t, repo)
	packageComposerMock(t, "")
	t.Chdir(repo)
	out := captureStdout(t, func() {
		if code := Run([]string{"env", "package", "--ref", ref}); code != 0 {
			t.Fatalf("package = %d", code)
		}
	})
	for _, expected := range []string{"> composer validate ", "> composer install ", "> composer check-platform-reqs ", "Package created.\n", "No activation performed."} {
		if !strings.Contains(out, expected) {
			t.Fatalf("missing %q: %s", expected, out)
		}
	}
	if strings.Contains(out, "> (staging only)") {
		t.Fatal("nonstandard command preview")
	}
	entries, err := os.ReadDir(filepath.Join(config.DataHome(), "nterchange", "packages"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("package entries: %v %v", entries, err)
	}
	location := filepath.Join(config.DataHome(), "nterchange", "packages", entries[0].Name())
	want := strings.Join(detailRowLines([]detailRow{
		{label: "Project", value: "client"}, {label: "Commit", value: ref}, {label: "Path", value: location}, {label: "Manifest", value: filepath.Join(location, "manifest.json")},
	}, 2), "\n")
	if !strings.Contains(out, want) {
		t.Fatalf("summary does not use detail rows:\n%s\nwant:\n%s", out, want)
	}
}

func TestNterchangeOutputSharedListsAndDetails(t *testing.T) {
	root, _, _ := nterchangePullTestProject(t)
	wordpress := filepath.Join(t.TempDir(), "wordpress")
	if err := os.MkdirAll(filepath.Join(wordpress, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	packageWrite(t, wordpress, "nf.json", `{"version":2,"project":{"slug":"client","password_version":0},"wordpress":{"themes":["client"]},"remotes":{"review":"client.test-host:review"}}`)
	oldSSH := runSSHOutputFn
	runSSHOutputFn = func([]string) ([]byte, error) { t.Fatal("cached list/show connected"); return nil, nil }
	t.Cleanup(func() { runSSHOutputFn = oldSSH })
	for _, args := range [][]string{
		{"target", "list"}, {"target", "show", "test-host"}, {"target", "show", "test-host", "--json"},
		{"site", "list"}, {"site", "list", "--envs"}, {"site", "show", "client.test-host:review"}, {"site", "show", "client.test-host:review", "--json"},
		{"remote", "list"}, {"remote", "show", "review"},
	} {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			t.Chdir(root)
			nt := captureStdout(t, func() {
				if code := Run(args); code != 0 {
					t.Fatalf("nterchange %v = %d", args, code)
				}
			})
			t.Chdir(wordpress)
			wp := captureStdout(t, func() {
				if code := Run(args); code != 0 {
					t.Fatalf("WordPress %v = %d", args, code)
				}
			})
			if nt == "" || nt != wp {
				t.Fatalf("different shared output for %v:\nnterchange:\n%s\nWordPress:\n%s", args, nt, wp)
			}
		})
	}
}

func TestNterchangeOutputHelpUsesGroupHeading(t *testing.T) {
	nterchangePullTestProject(t)
	out := captureStdout(t, func() {
		if code := Run([]string{"env", "help"}); code != 0 {
			t.Fatalf("help = %d", code)
		}
	})
	if !strings.HasPrefix(out, "env\n\nCommands:\n") {
		t.Fatalf("nonstandard group heading: %s", out)
	}
	for _, command := range []string{"up", "down", "show", "logs", "shell, sh", "pull <remote>", "package --ref", "deploy <remote>", "rollback <remote>", "--rebuild", "--check"} {
		if !strings.Contains(out, command) {
			t.Fatalf("help omitted %q: %s", command, out)
		}
	}
	if !strings.Contains(out, "Logs and shell are local only.") || !strings.Contains(out, "Reset, push, snapshots and password are not available.") {
		t.Fatal("capability limits omitted")
	}
}

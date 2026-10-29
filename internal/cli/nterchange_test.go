package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/project"
	"github.com/nonfiction/nf/internal/state"
)

func nterchangeTestHomes(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, child := range map[string]string{"NF_CONFIG_HOME": "config", "NF_STATE_HOME": "state", "NF_DATA_HOME": "data"} {
		t.Setenv(name, filepath.Join(root, child))
	}
	t.Setenv("NF_PASSWORD_SALT", "")
	t.Setenv("NF_SECRET_SALT", "")
	return root
}

func registerTestSSHTarget(t *testing.T) sshTarget {
	t.Helper()
	if code := Run([]string{"target", "add", "ssh", "test-host", "--host", "192.0.2.1"}); code != 0 {
		t.Fatalf("register SSH target = %d", code)
	}
	targets, err := loadSSHTargets()
	if err != nil {
		t.Fatal(err)
	}
	return targets[0]
}

func testSSHSite() sshSiteEnv {
	return sshSiteEnv{Name: "client", Env: "review", Kind: "nterchange", URL: "https://client.example.invalid", Path: "/srv/test/sites/client", ComposeFile: "/srv/test/compose.yaml", Service: "client", Database: "client", Media: []string{"public_html/upload", "public_html/videos"}}
}

func TestNterchangeInitInspectAndWordPressGuards(t *testing.T) {
	root := nterchangeTestHomes(t)
	repo := filepath.Join(root, "client")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "compose.yaml"), []byte("services: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	if code := Run([]string{"init", "--type", "nterchange"}); code != 0 {
		t.Fatalf("init = %d", code)
	}
	manifest, err := project.Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Nterchange == nil || manifest.Project.Slug != "client" {
		t.Fatalf("bad manifest: %#v", manifest)
	}
	before, err := os.ReadFile(config.ProjectFile(repo))
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if code := Run([]string{"env", "show"}); code != 0 {
			t.Fatalf("show = %d", code)
		}
	})
	if !strings.Contains(out, "Application   nterchange") || !strings.Contains(out, filepath.Join(repo, "public_html/upload")) {
		t.Fatalf("bad show: %s", out)
	}
	for _, argv := range [][]string{{"env", "up"}, {"env", "reset"}, {"env", "push", "review", "--execute", "--yes"}, {"env", "pull", "review", "--execute", "--yes"}, {"theme", "deploy", "review"}, {"plugin", "update"}, {"define", "set", "A", "B"}, {"alias", "add", "x"}, {"init", "--force"}} {
		out := captureStderr(t, func() {
			if code := Run(argv); code == 0 {
				t.Fatalf("unsafe/unsupported command succeeded: %v", argv)
			}
		})
		if !strings.Contains(out, "nterchange") {
			t.Fatalf("missing nterchange guard for %v: %s", argv, out)
		}
	}
	after, err := os.ReadFile(config.ProjectFile(repo))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("guarded commands changed manifest")
	}
	if _, err := os.Stat(config.DataHome()); !os.IsNotExist(err) {
		t.Fatalf("inspection/guards created managed environment: %v", err)
	}
	out = captureStdout(t, func() { Run([]string{"help"}) })
	if strings.Contains(out, "WordPress themes") || !strings.Contains(out, "nterchange Compose") {
		t.Fatalf("wrong application help: %s", out)
	}
}

func TestSSHTargetRegistrationIsDurablePrivateAndConnectionFree(t *testing.T) {
	root := nterchangeTestHomes(t)
	old := runSSHOutputFn
	runSSHOutputFn = func(args []string) ([]byte, error) {
		t.Fatalf("registration made a connection: %v", args)
		return nil, nil
	}
	t.Cleanup(func() { runSSHOutputFn = old })
	key := filepath.Join(root, "key with spaces")
	hosts := filepath.Join(root, "known_hosts")
	for _, file := range []string{key, hosts} {
		if err := os.WriteFile(file, []byte("private-test-marker"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if code := Run([]string{"target", "add", "ssh", "test-host", "--host", "192.0.2.1", "--identity-file", key, "--known-hosts-file", hosts}); code != 0 {
		t.Fatalf("register = %d", code)
	}
	file := filepath.Join(config.ConfigHome(), "ssh-targets.json")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-test-marker") {
		t.Fatal("copied key contents into config")
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config mode = %o", info.Mode().Perm())
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []any{map[string]any{"name": "cloud-host"}}}}); err != nil {
		t.Fatal(err)
	}
	targets, err := cachedTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || matchingTargetByName(targets, "test-host") == nil {
		t.Fatalf("provider inventory lost SSH target: %v", targets)
	}
	if err := state.SaveStateRecords("providers", nil); err != nil {
		t.Fatal(err)
	}
	targets, err = cachedTargets()
	if err != nil || len(targets) != 1 {
		t.Fatalf("SSH target depended on provider cache: %v %v", targets, err)
	}
	if code := Run([]string{"target", "add", "ssh", "test-host", "--host", "192.0.2.2"}); code == 0 {
		t.Fatal("silently replaced existing target")
	}
	after, err := os.ReadFile(file)
	if err != nil || string(after) != string(data) {
		t.Fatal("duplicate registration changed config")
	}
	registered, err := loadSSHTargets()
	if err != nil {
		t.Fatal(err)
	}
	args := sshTargetArgs(registered[0])
	joined := strings.Join(args, "\n")
	if !strings.Contains(joined, "StrictHostKeyChecking=yes") || !strings.Contains(joined, "UserKnownHostsFile="+hosts) || !strings.Contains(joined, "BatchMode=yes") || !strings.Contains(joined, key) || strings.Contains(joined, "/dev/null") {
		t.Fatalf("unsafe SSH arguments: %v", args)
	}
	if _, err := appendSSHTargets([]map[string]any{{"provider": "linode", "name": "TEST-HOST"}}); err == nil {
		t.Fatal("accepted cloud/registered name collision")
	}
}

func TestSSHDiscoveryFeedsExistingRemoteModelAndPreservesCacheOnFailure(t *testing.T) {
	root := nterchangeTestHomes(t)
	target := registerTestSSHTarget(t)
	site := testSSHSite()
	payload, err := json.Marshal([]sshSiteEnv{site})
	if err != nil {
		t.Fatal(err)
	}
	old := runSSHOutputFn
	runSSHOutputFn = func(args []string) ([]byte, error) {
		if !reflect.DeepEqual(args[len(args)-4:], []string{"head", "-c", "1048577", "/var/lib/nf/sites.json"}) {
			t.Fatalf("unexpected remote operation: %v", args)
		}
		return payload, nil
	}
	t.Cleanup(func() { runSSHOutputFn = old })
	result, err := refreshRemoteTargetSites([]map[string]any{target.record()})
	if err != nil || result.Refreshed != 1 || result.Discovered != 1 {
		t.Fatalf("refresh: %+v %v", result, err)
	}
	sites, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 || recordValueString(sites[0]["env_id"]) != "client.test-host:review" || recordValueString(sites[0]["kind"]) != "nterchange" {
		t.Fatalf("bad site records: %v", sites)
	}
	before, err := os.ReadFile(state.StatePath("sites"))
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "client")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(repo, &project.Manifest{Version: 2, Project: project.Project{Slug: "client"}, Nterchange: &project.Nterchange{}}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	if code := Run([]string{"remote", "add", "review", "client.test-host:review"}); code != 0 {
		t.Fatalf("remote add = %d", code)
	}
	metadata, err := loadProjectMetadataOrError(repo)
	if err != nil || metadata.Remotes["review"] != "client.test-host:review" {
		t.Fatalf("remote manifest: %#v %v", metadata, err)
	}
	out := captureStdout(t, func() {
		if code := Run([]string{"remote", "show", "review"}); code != 0 {
			t.Fatalf("remote show = %d", code)
		}
	})
	if !strings.Contains(out, "client.test-host") {
		t.Fatalf("remote model not readable: %s", out)
	}
	for _, invalid := range []string{"null", "[{}]", string(payload) + " {}", strings.Replace(string(payload), "\"kind\":\"nterchange\"", "\"kind\":\"wordpress\"", 1), strings.Replace(string(payload), "\"name\":\"client\"", "\"name\":\"client\",\"password\":\"do-not-print\"", 1), string(payload[:len(payload)-1]) + "," + string(payload[1:]), strings.Repeat("x", 1048577)} {
		payload = []byte(invalid)
		result, err = refreshRemoteTargetSites([]map[string]any{target.record()})
		if err != nil || result.Refreshed != 0 || len(result.Warnings) != 1 {
			t.Fatalf("invalid inventory changed cache: %+v %v", result, err)
		}
		if strings.Contains(fmt.Sprint(result.Warnings), "do-not-print") {
			t.Fatal("inventory error exposed secret value")
		}
		after, err := os.ReadFile(state.StatePath("sites"))
		if err != nil || string(before) != string(after) {
			t.Fatal("failed refresh replaced cached sites")
		}
	}
}

func TestSSHTargetedRefreshPreservesOtherProvidersAndStaleSites(t *testing.T) {
	nterchangeTestHomes(t)
	registerTestSSHTarget(t)
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "kinsta", "targets": []any{map[string]any{"name": "cloud-host"}}}}); err != nil {
		t.Fatal(err)
	}
	original := []map[string]any{
		{"provider": "kinsta", "target": "cloud-host", "env_id": "other:live"},
		{"provider": "linode", "target": "removed-host", "env_id": "stale:live"},
		{"provider": "ssh", "target": "test-host", "env_id": "outdated:review"},
	}
	if err := state.SaveStateRecords("sites", original); err != nil {
		t.Fatal(err)
	}
	old := runSSHOutputFn
	calls := 0
	runSSHOutputFn = func(args []string) ([]byte, error) {
		calls++
		return json.Marshal([]sshSiteEnv{testSSHSite()})
	}
	t.Cleanup(func() { runSSHOutputFn = old })
	if code := Run([]string{"site", "refresh", "test-host"}); code != 0 {
		t.Fatalf("targeted refresh = %d", code)
	}
	if calls != 1 {
		t.Fatalf("SSH calls = %d", calls)
	}
	sites, err := state.LoadStateRecords("sites")
	if err != nil || len(sites) != 3 || !reflect.DeepEqual(sites[:2], original[:2]) || recordValueString(sites[2]["env_id"]) != "client.test-host:review" {
		t.Fatalf("targeted refresh changed unrelated cache: %v %v", sites, err)
	}
	before, err := os.ReadFile(state.StatePath("sites"))
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"site", "refresh", "missing"}, {"site", "refresh", ""}, {"site", "refresh", " "}, {"site", "refresh", "--bad"}, {"site", "refresh", "test-host", "extra"}} {
		if code := Run(args); code == 0 {
			t.Fatalf("invalid refresh succeeded: %v", args)
		}
	}
	after, err := os.ReadFile(state.StatePath("sites"))
	if err != nil || string(before) != string(after) || calls != 1 {
		t.Fatal("invalid refresh changed cache or connected")
	}
}

func TestSSHMetadataRejectsUnsafeValues(t *testing.T) {
	for _, mutate := range []func(*sshSiteEnv){
		func(s *sshSiteEnv) { s.Path = "/srv/test/../secrets" },
		func(s *sshSiteEnv) { s.Service = "app; shell" },
		func(s *sshSiteEnv) { s.Service = "--all" },
		func(s *sshSiteEnv) { s.Database = " client " },
		func(s *sshSiteEnv) { s.URL = "https://user:private@example.invalid" },
		func(s *sshSiteEnv) { s.Media = []string{"public_html/.env"} },
		func(s *sshSiteEnv) { s.ComposeFile = "/" },
	} {
		site := testSSHSite()
		mutate(&site)
		if err := site.validate(); err == nil {
			t.Fatalf("accepted unsafe metadata: %#v", site)
		}
	}
	for _, host := range []string{"-oProxyCommand=bad", "host; shell", "user@host", "host\nvalue"} {
		if err := (sshTarget{Name: "test", Host: host, User: "nonfiction", Port: 22}).validate(); err == nil {
			t.Errorf("accepted SSH host %q", host)
		}
	}
	for _, port := range []int{0, -1, 65536} {
		if err := (sshTarget{Name: "test", Host: "host", User: "nonfiction", Port: port}).validate(); err == nil {
			t.Errorf("accepted port %d", port)
		}
	}
}

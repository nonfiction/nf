package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nonfiction/nf/internal/project"
	"github.com/nonfiction/nf/internal/state"
)

func nterchangePullTestProject(t *testing.T) (string, sshSiteEnv, []byte) {
	t.Helper()
	home := nterchangeTestHomes(t)
	target := registerTestSSHTarget(t)
	site := testSSHSite()
	inventory, err := json.Marshal([]sshSiteEnv{site})
	if err != nil {
		t.Fatal(err)
	}
	old := runSSHOutputFn
	runSSHOutputFn = func([]string) ([]byte, error) { return inventory, nil }
	records, err := discoverSSHTargetSites(target.record())
	runSSHOutputFn = old
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SaveStateRecords("sites", records); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "repo with spaces")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(root, &project.Manifest{Version: 2, Project: project.Project{Slug: "client"}, Nterchange: &project.Nterchange{Media: site.Media}, Remotes: map[string]string{"review": "client.test-host:review"}}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	return root, site, inventory
}

func nterchangeTestInspection(root, service string, remote bool) map[string]any {
	mount := func(kind, source, destination string, rw bool) map[string]any {
		return map[string]any{"Type": kind, "Source": source, "Destination": destination, "RW": rw}
	}
	mounts := []map[string]any{
		mount("bind", root, "/var/www/site", false),
		mount("bind", filepath.Join(root, "var"), "/var/www/site/var", true),
		mount("bind", filepath.Join(root, "public_html/upload"), "/var/www/site/public_html/upload", true),
		mount("bind", "/srv/secrets/client.env", "/var/www/site/.env", false),
	}
	if !remote {
		mounts[3]["Source"] = filepath.Join(root, ".env")
	}
	env := []string{"DB_NAME=fixture", "DB_HOST=db", "DB_USER=fixture", "DB_PASSWORD=private-marker", "PUBLIC_SITE=http://127.0.0.1:18080"}
	if remote {
		env = nil
	}
	if service == "db" {
		kind := "volume"
		if remote {
			kind = "bind"
		}
		mounts = []map[string]any{mount(kind, "/var/lib/docker/test-data", "/var/lib/mysql", true), mount("bind", "/srv/secrets/client.cnf", "/run/secrets/mysql-client.cnf", false)}
		env = []string{"MYSQL_DATABASE=fixture", "MYSQL_USER=fixture", "MYSQL_PASSWORD=private-marker"}
	}
	return map[string]any{
		"Id": service + "-id", "State": map[string]any{"Status": "running", "Health": map[string]any{"Status": "healthy"}},
		"Config": map[string]any{"Env": env}, "Mounts": mounts,
		"NetworkSettings": map[string]any{"Networks": map[string]any{"runtime": map[string]any{"NetworkID": "internal-net"}}},
	}
}

func TestNterchangePullPlanAndExecutionGuardsNeverConnect(t *testing.T) {
	_, _, _ = nterchangePullTestProject(t)
	oldSSH, oldLocal := runSSHOutputFn, nterchangeOutputFn
	runSSHOutputFn = func([]string) ([]byte, error) { t.Fatal("plan/guard connected over SSH"); return nil, nil }
	nterchangeOutputFn = func(string, []string) ([]byte, error) { t.Fatal("plan/guard inspected local runtime"); return nil, nil }
	t.Cleanup(func() { runSSHOutputFn, nterchangeOutputFn = oldSSH, oldLocal })
	for _, args := range [][]string{{"review"}, {"review", "--dry-run"}, {"review", "--non-interactive"}} {
		out := captureStdout(t, func() {
			if code := Run(append([]string{"env", "pull"}, args...)); code != 0 {
				t.Fatalf("plan %v = %d", args, code)
			}
		})
		if !strings.Contains(out, "client.test-host:review -> local client") || !strings.Contains(out, "Plan only") || strings.Contains(out, "private-marker") {
			t.Fatalf("bad plan: %s", out)
		}
	}
	for _, args := range [][]string{{}, {"missing", "--dry-run"}, {"review", "--execute", "--non-interactive"}, {"review", "--yes"}, {"review", "--execute", "--check"}, {"review", "--execute", "--dry-run"}, {"review", "--check", "--dry-run"}, {"review", "--unknown"}} {
		if code := Run(append([]string{"env", "pull"}, args...)); code == 0 {
			t.Fatalf("unsafe plan succeeded: %v", args)
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("NF_DATA_HOME"))); !os.IsNotExist(err) {
		t.Fatalf("plan created data storage: %v", err)
	}
}

func TestNterchangePullCheckReadsMetadataWithoutMutations(t *testing.T) {
	root, site, inventory := nterchangePullTestProject(t)
	managedRoot := filepath.Join(filepath.Dir(site.Path), ".nf-releases", "client-review", "release-fixture", "code")
	ownershipResult, ownershipFailure := managedRoot, false
	oldSSH, oldLocal := runSSHOutputFn, nterchangeOutputFn
	t.Cleanup(func() { runSSHOutputFn, nterchangeOutputFn = oldSSH, oldLocal })
	localCalls, remoteCalls := 0, 0
	answer := func(args []string, remote bool) ([]byte, error) {
		joined := strings.Join(args, " ")
		for _, forbidden := range []string{"mysqldump", "rsync", " stop ", " pause ", " restart ", " up ", " down ", "DROP ", "ALTER ", "INSERT ", "LOCK TABLES", "UPDATE "} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("preflight attempted mutation: %s", joined)
			}
		}
		location := root
		if remote {
			location = site.Path
		}
		switch {
		case strings.Contains(joined, " ps -q "):
			return []byte(args[len(args)-1] + "-id\n"), nil
		case len(args) > 2 && args[0] == "docker" && args[1] == "inspect":
			service := strings.TrimSuffix(args[2], "-id")
			inspection := nterchangeTestInspection(location, service, remote)
			if remote && service == site.Service {
				inspection["Mounts"].([]map[string]any)[0]["Source"] = managedRoot
			}
			return json.Marshal([]any{inspection})
		case strings.HasPrefix(joined, "realpath "):
			return []byte(args[len(args)-1] + "\n"), nil
		case strings.HasPrefix(joined, "docker network inspect "):
			return []byte(`[{"Internal":true}]`), nil
		case strings.HasPrefix(joined, "docker exec "):
			return []byte("latin1\tlatin1_swedish_ci\nInnoDB\t39\nMyISAM\t1\nOFF\n"), nil
		case strings.HasPrefix(joined, "python3 "):
			if args[len(args)-1] != "/srv/secrets/client.env" {
				t.Fatalf("read wrong environment file: %v", args)
			}
			return []byte("db\n" + site.Database + "\n"), nil
		}
		return nil, fmt.Errorf("unexpected inspection: %v", args)
	}
	nterchangeOutputFn = func(_ string, args []string) ([]byte, error) { localCalls++; return answer(args, false) }
	runSSHOutputFn = func(args []string) ([]byte, error) {
		remoteCalls++
		if args[len(args)-1] == "/var/lib/nf/sites.json" {
			return inventory, nil
		}
		// Match the adapter's command without invoking a shell in this mock.
		command := args[len(args)-1]
		if !strings.HasPrefix(command, "sudo -n ") {
			t.Fatalf("missing noninteractive sudo: %v", args)
		}
		command = strings.TrimPrefix(command, "sudo -n ")
		if strings.Contains(command, "--inspect-root") {
			if ownershipFailure {
				return nil, fmt.Errorf("pending release recovery")
			}
			return json.Marshal(map[string]any{"root": ownershipResult, "managed": true})
		}
		if strings.HasPrefix(command, "python3 ") {
			return answer([]string{"python3", "-c", "script", "/srv/secrets/client.env"}, true)
		}
		if strings.HasPrefix(command, "docker exec ") {
			return answer([]string{"docker", "exec", "db-id", "mysql"}, true)
		}
		return answer(strings.Fields(command), true)
	}
	out := captureStdout(t, func() {
		if code := Run([]string{"env", "pull", "review", "--check"}); code != 0 {
			t.Fatalf("preflight = %d", code)
		}
	})
	if localCalls == 0 || remoteCalls == 0 || !strings.Contains(out, "Read-only preflight passed") || !strings.Contains(out, "MyISAM") || strings.Contains(out, "private-marker") {
		t.Fatalf("missing/unsafe inspection: local=%d remote=%d %s", localCalls, remoteCalls, out)
	}
	ownershipFailure = true
	if code := Run([]string{"env", "pull", "review", "--check"}); code == 0 {
		t.Fatal("failed ownership verifier passed preflight")
	}
	ownershipFailure, ownershipResult = false, "/unrecognized-root"
	if code := Run([]string{"env", "pull", "review", "--check"}); code == 0 {
		t.Fatal("mismatched managed root passed preflight")
	}
	ownershipResult = managedRoot
	// A successful check does not rewrite even the disposable inventory cache.
	before, err := os.ReadFile(state.StatePath("sites"))
	if err != nil {
		t.Fatal(err)
	}
	inventory = []byte(strings.Replace(string(inventory), site.Database+`","media`, `different","media`, 1))
	if code := Run([]string{"env", "pull", "review", "--check"}); code == 0 {
		t.Fatal("stale remote inventory passed preflight")
	}
	after, err := os.ReadFile(state.StatePath("sites"))
	if err != nil || string(after) != string(before) {
		t.Fatal("preflight changed cached inventory")
	}
}

func TestNterchangeRuntimeRejectsUnsafeLocalDestinations(t *testing.T) {
	nterchangeTestHomes(t)
	old := nterchangeOutputFn
	t.Cleanup(func() { nterchangeOutputFn = old })
	for _, failure := range []string{"stopped", "unhealthy", "paused", "wrong-root", "writable-source", "wrong-upload", "ephemeral-db", "external-network", "nonlocal-url", "wrong-database", "wrong-password", "events-on", "symlink-media"} {
		t.Run(failure, func(t *testing.T) {
			nterchangeOutputFn = func(_ string, args []string) ([]byte, error) {
				joined := strings.Join(args, " ")
				switch {
				case strings.Contains(joined, " ps -q "):
					return []byte(args[len(args)-1] + "-id"), nil
				case args[0] == "docker" && args[1] == "inspect":
					service := strings.TrimSuffix(args[2], "-id")
					c := nterchangeTestInspection("/test", service, false)
					if service == "app" {
						state := c["State"].(map[string]any)
						switch failure {
						case "stopped":
							state["Status"] = "exited"
						case "unhealthy":
							state["Health"] = map[string]any{"Status": "unhealthy"}
						case "paused":
							state["Paused"] = true
						case "wrong-root":
							c["Mounts"].([]map[string]any)[0]["Source"] = "/other"
						case "writable-source":
							c["Mounts"].([]map[string]any)[0]["RW"] = true
						case "wrong-upload":
							c["Mounts"].([]map[string]any)[2]["Source"] = "/other/upload"
						case "nonlocal-url":
							c["Config"].(map[string]any)["Env"] = []string{"DB_NAME=fixture", "DB_HOST=db", "DB_USER=fixture", "DB_PASSWORD=private-marker", "PUBLIC_SITE=https://public.example.com"}
						case "wrong-database":
							c["Config"].(map[string]any)["Env"] = []string{"DB_NAME=production", "DB_HOST=db"}
						case "wrong-password":
							c["Config"].(map[string]any)["Env"] = []string{"DB_NAME=fixture", "DB_HOST=db", "DB_USER=fixture", "DB_PASSWORD=wrong"}
						}
					} else if failure == "ephemeral-db" {
						c["Mounts"] = nil
					}
					return json.Marshal([]any{c})
				case args[0] == "realpath":
					if failure == "symlink-media" {
						return []byte("/outside/upload"), nil
					}
					return []byte("/test/public_html/upload"), nil
				case strings.HasPrefix(joined, "docker network inspect "):
					return json.Marshal([]any{map[string]any{"Internal": failure != "external-network"}})
				case strings.HasPrefix(joined, "docker exec "):
					if failure == "events-on" {
						return []byte("utf8mb4\tutf8mb4_0900_ai_ci\nON"), nil
					}
					return []byte("utf8mb4\tutf8mb4_0900_ai_ci\nOFF"), nil
				}
				return nil, fmt.Errorf("unexpected call: %v", args)
			}
			if _, err := inspectNterchangeRuntime("/test", "/test/compose.yaml", "app", []string{"public_html/upload"}, "", nil); err == nil {
				t.Fatal("unsafe destination passed preflight")
			}
		})
	}
}

func TestNterchangeRuntimeReadsLocalMountedDotenv(t *testing.T) {
	nterchangeTestHomes(t)
	root := t.TempDir()
	envFile := filepath.Join(t.TempDir(), "mounted.env")
	body := "DB_SERVER=db\nDB_DATABASE=fixture\nDB_SERVER_USERNAME=fixture\nDB_SERVER_PASSWORD=private-marker\nPUBLIC_URL=http://127.0.0.1:18080\n"
	old := nterchangeOutputFn
	t.Cleanup(func() { nterchangeOutputFn = old })
	for _, failure := range []string{"", "wrong-password", "public-url", "container-override"} {
		t.Run(failure, func(t *testing.T) {
			contents := body
			if failure == "wrong-password" {
				contents = strings.Replace(contents, "private-marker", "wrong", 1)
			} else if failure == "public-url" {
				contents = strings.Replace(contents, "127.0.0.1:18080", "public.example.com", 1)
			}
			if err := os.WriteFile(envFile, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			nterchangeOutputFn = func(_ string, args []string) ([]byte, error) {
				switch {
				case strings.Contains(strings.Join(args, " "), " ps -q "):
					return []byte(args[len(args)-1] + "-id"), nil
				case args[0] == "docker" && args[1] == "inspect":
					service := strings.TrimSuffix(args[2], "-id")
					c := nterchangeTestInspection(root, service, false)
					if service == "app" {
						c["Config"].(map[string]any)["Env"] = []string{}
						if failure == "container-override" {
							c["Config"].(map[string]any)["Env"] = []string{"DB_SERVER=external.example.com"}
						}
						c["Mounts"].([]map[string]any)[3]["Source"] = envFile
					}
					return json.Marshal([]any{c})
				case args[0] == "realpath":
					return []byte(args[len(args)-1]), nil
				case args[0] == "docker" && args[1] == "network":
					return []byte(`[{"Internal":true}]`), nil
				case args[0] == "docker" && args[1] == "exec":
					return []byte("latin1\tlatin1_swedish_ci\nInnoDB\t1\nOFF"), nil
				}
				return nil, fmt.Errorf("unexpected command: %v", args)
			}
			_, err := inspectNterchangeRuntime(root, filepath.Join(root, "compose.yaml"), "app", []string{"public_html/upload"}, "", nil)
			if (err == nil) != (failure == "") {
				t.Fatalf("dotenv preflight %q: %v", failure, err)
			}
		})
	}
}

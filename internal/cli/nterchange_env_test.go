package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nonfiction/nf/internal/config"
)

const nterchangeEnvTestConfig = `{"name":"client-local","services":{"app":{"networks":{"runtime":null},"environment":{"PASSWORD":"never-print-this"}},"db":{"networks":{"runtime":null},"environment":{"MYSQL_DATABASE":"client","MYSQL_USER":"client","MYSQL_PASSWORD":"private-db-value","MYSQL_ROOT_PASSWORD":"private-root-value"}},"gateway":{"ports":[{"host_ip":"127.0.0.1","published":"18093","target":8025,"protocol":"tcp"},{"host_ip":"127.0.0.1","published":"18083","target":8080,"protocol":"tcp"}]}},"networks":{"runtime":{"internal":true}}}`

func nterchangeLocalTestProject(t *testing.T) string {
	t.Helper()
	root, _, _ := nterchangePullTestProject(t)
	packageWrite(t, root, "compose.yaml", "services: {}\n")
	return root
}

func nterchangeEnvDocker(t *testing.T, cfg string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "commands")
	t.Setenv("NF_TEST_COMPOSE_CONFIG", cfg)
	t.Setenv("NF_TEST_DOCKER_LOG", log)
	t.Setenv("NF_TEST_DOCKER_FAIL", "")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$NF_TEST_DOCKER_LOG"
shift
while [ "$1" = -f ]; do shift 2; done
if [ "$1" = config ]; then
  printf '%s\n' "$NF_TEST_COMPOSE_CONFIG"
  if [ "$NF_TEST_DOCKER_FAIL" = config ]; then
    printf 'never-print-this\n' >&2
    exit 9
  fi
  exit 0
fi
if [ "$1" = "$NF_TEST_DOCKER_FAIL" ]; then exit 9; fi
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestNterchangeLocalLifecycle(t *testing.T) {
	root := nterchangeLocalTestProject(t)
	log := nterchangeEnvDocker(t, nterchangeEnvTestConfig)
	// Execute from a nested directory, without changing recipe or site data.
	packageWrite(t, root, "var/session", "session-sentinel")
	packageWrite(t, root, "public_html/upload/media", "media-sentinel")
	packageWrite(t, root, ".env", "PASSWORD=settings-sentinel")
	files := []string{"nf.json", "compose.yaml", ".env", "var/session", "public_html/upload/media"}
	before := map[string]string{}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		before[file] = string(data)
	}
	t.Chdir(filepath.Join(root, "var"))
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"up"}, []string{"pull --policy missing db-ui", "up -d --no-build --pull never"}},
		{[]string{"up", "--rebuild"}, []string{"build app", "pull --policy missing db-ui", "up -d --no-build --pull never"}},
		{[]string{"down"}, []string{"down"}},
		{[]string{"logs"}, []string{"logs -f app"}},
		{[]string{"shell"}, []string{"exec app sh"}},
		{[]string{"sh"}, []string{"exec app sh"}},
	} {
		t.Run(strings.Join(tc.args, "-"), func(t *testing.T) {
			os.Remove(log)
			out := captureStdout(t, func() {
				if code := Run(append([]string{"env"}, tc.args...)); code != 0 {
					t.Fatalf("%v = %d", tc.args, code)
				}
			})
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			prefix := "compose -f " + filepath.Join(root, "compose.yaml") + " "
			want := []string{prefix + "config --format json"}
			commandArgs := nterchangeComposeArgs(root)
			if tc.args[0] == "up" || tc.args[0] == "down" {
				override := filepath.Join(config.DataHome(), "nterchange", "envs", "client-local", "db-ui.compose.json")
				prefix += "-f " + override + " "
				commandArgs = append(commandArgs, "-f", override)
			}
			for _, cmd := range tc.want {
				want = append(want, prefix+cmd)
				preview := append(append([]string{}, commandArgs...), strings.Fields(cmd)...)
				if !strings.Contains(out, "> "+renderCommandArgs(preview)) {
					t.Fatalf("missing preview: %s", out)
				}
			}
			if got := strings.Split(strings.TrimSpace(string(data)), "\n"); !reflect.DeepEqual(got, want) {
				t.Fatalf("commands = %v, want %v", got, want)
			}
			if tc.args[0] == "up" && (!strings.Contains(out, "Env started.") || !strings.Contains(out, "http://127.0.0.1:18083")) {
				t.Fatalf("missing start summary: %s", out)
			}
			if tc.args[0] == "down" && (!strings.Contains(out, "Env stopped.") || strings.Contains(out, "\nDatabase\n")) {
				t.Fatalf("wrong stop summary: %s", out)
			}
		})
	}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil || string(data) != before[file] {
			t.Fatalf("changed %s: %v", file, err)
		}
	}
}

func TestNterchangeLocalRejectsArgumentsBeforeDocker(t *testing.T) {
	nterchangeLocalTestProject(t)
	log := nterchangeEnvDocker(t, nterchangeEnvTestConfig)
	for _, args := range [][]string{
		{"up", "--yes"}, {"up", "--rebuild", "--unknown"}, {"down", "-v"}, {"down", "--remove-orphans"},
		{"show", "extra"}, {"logs", "live"}, {"shell", "live"}, {"sh", "--", "rm"},
		{"reset"}, {"push", "live"}, {"password"}, {"snapshot"}, {"wp", "--", "core", "version"},
	} {
		captureStderr(t, func() {
			if code := Run(append([]string{"env"}, args...)); code == 0 {
				t.Fatalf("accepted %v", args)
			}
		})
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("invalid/unsupported invocation reached Docker: %v", err)
	}
}

func TestNterchangeLocalFailuresStopExecution(t *testing.T) {
	for _, fail := range []string{"config", "build", "pull", "up", "down", "logs", "exec"} {
		t.Run(fail, func(t *testing.T) {
			nterchangeLocalTestProject(t)
			log := nterchangeEnvDocker(t, nterchangeEnvTestConfig)
			t.Setenv("NF_TEST_DOCKER_FAIL", fail)
			args := []string{"env", fail}
			switch fail {
			case "config":
				args = []string{"env", "show"}
			case "build":
				args = []string{"env", "up", "--rebuild"}
			case "pull":
				args = []string{"env", "up"}
			case "exec":
				args = []string{"env", "shell"}
			}
			var stderr string
			out := captureStdout(t, func() {
				stderr = captureStderr(t, func() {
					if code := Run(args); code != 1 {
						t.Fatalf("%v = %d", args, code)
					}
				})
			})
			if strings.Contains(out+stderr, "never-print-this") || strings.Contains(out, "Env started.") || strings.Contains(out, "Env stopped.") {
				t.Fatalf("secret or false success: %s %s", out, stderr)
			}
			data, _ := os.ReadFile(log)
			if (fail == "build" || fail == "pull") && strings.Contains(string(data), " up ") {
				t.Fatal("started after failed build")
			}
		})
	}
}

func TestNterchangeLocalStartSafety(t *testing.T) {
	for _, tc := range []struct{ name, config string }{
		{"invalid-json", "secret-invalid-json"},
		{"missing-service", `{"name":"client","services":{"app":{}}}`},
		{"external-network", strings.Replace(nterchangeEnvTestConfig, `"internal":true`, `"internal":false`, 1)},
		{"default-network", strings.Replace(nterchangeEnvTestConfig, `"app":{"networks":{"runtime":null},`, `"app":{`, 1)},
		{"public-port", strings.Replace(nterchangeEnvTestConfig, `127.0.0.1`, `0.0.0.0`, 1)},
		{"unspecified-port", strings.Replace(nterchangeEnvTestConfig, `"host_ip":"127.0.0.1",`, ``, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nterchangeLocalTestProject(t)
			log := nterchangeEnvDocker(t, tc.config)
			captureStdout(t, func() {
				captureStderr(t, func() {
					if code := Run([]string{"env", "up", "--rebuild"}); code != 1 {
						t.Fatalf("unsafe start = %d", code)
					}
				})
			})
			data, _ := os.ReadFile(log)
			if strings.Count(string(data), "\n") != 1 {
				t.Fatalf("unsafe config reached mutation: %s", data)
			}
		})
	}
}

func TestNterchangeLocalPortRendering(t *testing.T) {
	var cfg nterchangeComposeConfig
	if err := json.Unmarshal([]byte(nterchangeEnvTestConfig), &cfg); err != nil {
		t.Fatal(err)
	}
	want := []detailRow{{label: "Mailpit URL", value: "http://127.0.0.1:18093"}, {label: "Site URL", value: "http://127.0.0.1:18083"}}
	if got := cfg.portRows(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ports = %v", got)
	}
	if cfg.Services["gateway"].Ports[0].Target != 8025 {
		t.Fatal("render changed config")
	}
	ipv6 := strings.ReplaceAll(nterchangeEnvTestConfig, "127.0.0.1", "::1")
	if err := json.Unmarshal([]byte(ipv6), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.validateLocalStart(); err != nil || cfg.portRows()[0].value != "http://[::1]:18093" {
		t.Fatalf("IPv6: %v %v", cfg.portRows(), err)
	}
	gateway := cfg.Services["gateway"]
	gateway.Ports = append(gateway.Ports, nterchangeComposePort{HostIP: "127.0.0.1", Published: "19000", Target: 9000, Protocol: "tcp"})
	cfg.Services["gateway"] = gateway
	if row := cfg.portRows()[2]; row.label != "gateway:9000" || row.value != "127.0.0.1:19000" {
		t.Fatalf("unclassified port = %v", row)
	}
}

func TestNterchangeDatabaseUIOverride(t *testing.T) {
	var cfg nterchangeComposeConfig
	if err := json.Unmarshal([]byte(nterchangeEnvTestConfig), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Services["db"].Networks["runtime"] = map[string]any{"aliases": []string{"db"}, "ipv4_address": "10.0.0.5"}
	effective, path, data, err := cfg.withDatabaseUI()
	if err != nil || !strings.HasSuffix(path, "/nterchange/envs/client-local/db-ui.compose.json") {
		t.Fatalf("override: %s %v", path, err)
	}
	var overlay struct {
		Services map[string]nterchangeComposeService
		Networks map[string]any
	}
	if err := json.Unmarshal(data, &overlay); err != nil {
		t.Fatal(err)
	}
	ui := overlay.Services["db-ui"]
	if len(overlay.Services) != 1 || len(overlay.Networks) != 1 || len(ui.Networks) != 2 || ui.Networks["runtime"] != nil || ui.Image != defaultDockerWordpressImage {
		t.Fatalf("unsafe override: %s", data)
	}
	if ui.Ports[0].HostIP != "127.0.0.1" || ui.Ports[0].Target != 80 || !strings.Contains(strings.Join(ui.Command, " "), localAdminNeoURL) {
		t.Fatalf("UI configuration: %v", ui)
	}
	for _, unwanted := range []string{"private-db-value", "private-root-value", "never-print-this", "10.0.0.5", "aliases", "volumes", "environment"} {
		if strings.Contains(string(data), unwanted) {
			t.Fatalf("override includes %q", unwanted)
		}
	}
	if _, changed := cfg.Services["db-ui"]; changed {
		t.Fatal("mutated the base services")
	}
	if err := effective.validateLocalStart(); err != nil {
		t.Fatal(err)
	}
	// An explicitly configured UI is kept as-is, without an nf overlay or pull.
	existing, path, data, err := effective.withDatabaseUI()
	if err != nil || path != "" || data != nil || !reflect.DeepEqual(existing, effective) {
		t.Fatal("replaced an existing database UI")
	}
	env := effective.Services["db"].Environment
	env["MYSQL_USER"], env["MYSQL_DATABASE"] = "client+user", "client db"
	if got := effective.portRows()[0].value; !strings.HasSuffix(got, "/?db=client+db&mysql=db&username=client%2Buser") {
		t.Fatalf("URL encoding: %s", got)
	}
}

func TestNterchangeExistingDatabaseUI(t *testing.T) {
	nterchangeLocalTestProject(t)
	var cfg nterchangeComposeConfig
	if err := json.Unmarshal([]byte(nterchangeEnvTestConfig), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Services["db-ui"] = nterchangeComposeService{Image: "existing-ui"}
	data, _ := json.Marshal(cfg)
	log := nterchangeEnvDocker(t, string(data))
	captureStdout(t, func() {
		if code := Run([]string{"env", "up"}); code != 0 {
			t.Fatalf("up = %d", code)
		}
	})
	commands, _ := os.ReadFile(log)
	if strings.Contains(string(commands), " pull ") || strings.Count(string(commands), " -f ") != 2 {
		t.Fatalf("replaced existing UI: %s", commands)
	}
	if _, err := os.Stat(config.DataHome()); !os.IsNotExist(err) {
		t.Fatalf("existing UI wrote managed data: %v", err)
	}
}

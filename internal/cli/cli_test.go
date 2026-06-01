package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nonfiction/nf/internal/config"
)

func TestSlugToTitle(t *testing.T) {
	tests := map[string]string{
		"demo":             "Demo",
		"demo-site":        "Demo Site",
		"demo_site":        "Demo Site",
		"demo--site":       "Demo Site",
		"demo_site-public": "Demo Site Public",
		"already-Titled":   "Already Titled",
		"":                 "",
		"__demo__site__":   "Demo Site",
	}

	for input, want := range tests {
		if got := slugToTitle(input); got != want {
			t.Fatalf("slugToTitle(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRecordValueStringFormatsNumericIDs(t *testing.T) {
	if got := recordValueString(float64(98222343)); got != "98222343" {
		t.Fatalf("recordValueString(float64 id) = %q, want decimal id", got)
	}
	if got := recordValueString(json.Number("98223448")); got != "98223448" {
		t.Fatalf("recordValueString(json.Number id) = %q, want decimal id", got)
	}
	if got := recordValueString("9.8223448e+07"); got != "98223448" {
		t.Fatalf("recordValueString(scientific string id) = %q, want decimal id", got)
	}
}

func TestIsLinodeNotFoundError(t *testing.T) {
	for _, message := range []string{
		"Request failed: 404\n[{\"field\": \"\", \"reason\": \"Not found\"}]",
		"not found",
	} {
		if !isLinodeNotFoundError(fmt.Errorf("%s", message)) {
			t.Fatalf("isLinodeNotFoundError(%q) = false, want true", message)
		}
	}
	if isLinodeNotFoundError(fmt.Errorf("Request failed: 401")) {
		t.Fatalf("isLinodeNotFoundError(401) = true, want false")
	}
}

func TestRecordPickerHelpers(t *testing.T) {
	server := map[string]any{"id": float64(98222343), "name": "test1", "provider": "linode", "hostname": "test1.nfweb.dev"}
	if got := recordPickerValue("server", server); got != "test1" {
		t.Fatalf("recordPickerValue(server) = %q, want test1", got)
	}
	if got := recordPickerLabel("server", server); !strings.Contains(got, "id 98222343") || !strings.Contains(got, "test1.nfweb.dev") {
		t.Fatalf("recordPickerLabel(server) = %q, want id and hostname", got)
	}

	site := map[string]any{"hostname": "example.com", "server_name": "test1", "status": "active"}
	if got := recordPickerValue("site", site); got != "example.com" {
		t.Fatalf("recordPickerValue(site) = %q, want example.com", got)
	}
	if got := recordPickerLabel("site", site); !strings.Contains(got, "example.com") || !strings.Contains(got, "server test1") {
		t.Fatalf("recordPickerLabel(site) = %q, want hostname and server", got)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("io.Copy() error = %v", err)
	}
	return buf.String()
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()

	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("io.Copy() error = %v", err)
	}
	return buf.String()
}

func TestRunHelpShowsTopLevelCommandsOutsideGit(t *testing.T) {
	workdir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() { _ = runHelp() })
	for _, wanted := range []string{"\n  init          initialize project metadata\n", "\n  provider      manage provider integrations\n", "\n  target        list and show deployable targets\n", "\n  site          refresh, list, and show remote sites/envs\n", "\n  remote        manage repo deploy remotes\n", "\n  theme         package artifacts and run theme tasks\n", "\n  env           manage the local development env\n", "\n  config        manage global config\n", "\n  password      derive passwords\n", "\n  help          show help\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runHelp() output missing %q:\n%s", wanted, output)
		}
	}
	for _, unwanted := range []string{"\n  repo          ", "\n  instance      ", "\n  server        ", "Shortcuts:", "nf up", "nf shell", "snapshot create", "\n  commands\n", "\n  run <name>\n", "\n  build\n"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("runHelp() output unexpectedly contained %q:\n%s", unwanted, output)
		}
	}
}

func TestRunHelpShowsTopLevelCommandsInsideGit(t *testing.T) {
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() { _ = runHelp() })
	for _, wanted := range []string{"\n  init          initialize project metadata\n", "\n  theme         package artifacts and run theme tasks\n", "\n  env           manage the local development env\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runHelp() output missing %q:\n%s", wanted, output)
		}
	}
	for _, unwanted := range []string{"\n  instance      ", "\n  server        ", "Shortcuts:", "nf up", "nf shell", "snapshot create", "\n  commands\n", "\n  run <name>\n", "\n  build\n"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("runHelp() output unexpectedly contained %q:\n%s", unwanted, output)
		}
	}
}

func TestRunInitHelpShowsFlags(t *testing.T) {
	output := captureStdout(t, func() { _ = runInitHelp() })
	for _, want := range []string{"init\n\nUsage:\n", "nf init [flags]", "--project-slug string", "--project-name string", "--theme-slug string", "--theme-source string", "--type string", "--force"} {
		if !strings.Contains(output, want) {
			t.Fatalf("runInitHelp() output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "repo") {
		t.Fatalf("runInitHelp() output unexpectedly mentioned repo:\n%s", output)
	}
}

func TestRunProviderHelpShowsCommands(t *testing.T) {
	output := captureStdout(t, func() { _ = runProviderHelp() })
	for _, wanted := range []string{"provider\n\nCommands:\n", "\n  list                 list provider integrations\n", "\n  show <provider>      show provider config status\n", "\n  check <provider>     preflight provider config\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runProviderHelp() output missing %q:\n%s", wanted, output)
		}
	}
}

func TestRunProviderListShowsProviders(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("DNSIMPLE_TOKEN", "")
	t.Setenv("DNSIMPLE_ACCOUNT_ID", "")
	t.Setenv("KINSTA_API_KEY", "")
	t.Setenv("LINODE_TOKEN", "")
	t.Setenv("LINODE_CLI_TOKEN", "")

	output := captureStdout(t, func() {
		if got := Run([]string{"provider", "list"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, want := range []string{"provider", "status", "missing", "dnsimple", "DNSIMPLE_TOKEN", "kinsta", "KINSTA_API_KEY", "linode", "LINODE_TOKEN or LINODE_CLI_TOKEN"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, output)
		}
	}
}

func TestRunProviderShowMasksConfiguredValues(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("DNSIMPLE_TOKEN", "dnsimple-token-secret")
	t.Setenv("DNSIMPLE_ACCOUNT_ID", "")

	output := captureStdout(t, func() {
		if got := Run([]string{"provider", "show", "dnsimple"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, want := range []string{"Provider: dnsimple", "Status: configured", "DNSIMPLE_TOKEN: dns***********", "DNSIMPLE_ACCOUNT_ID: 14 (default)"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "dnsimple-token-secret") {
		t.Fatalf("Run() output leaked secret:\n%s", output)
	}
}

func TestRunProviderCheckPreflightsWithoutRemoteCall(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("LINODE_TOKEN", "")
	t.Setenv("LINODE_CLI_TOKEN", "linode-token-secret")

	output := captureStdout(t, func() {
		if got := Run([]string{"provider", "check", "linode"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, want := range []string{"Provider linode preflight passed.", "No remote API call was made."} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, output)
		}
	}
}

func TestRunProviderCheckFailsWhenRequiredConfigMissing(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("KINSTA_API_KEY", "")

	output := captureStdout(t, func() {
		if got := Run([]string{"provider", "check", "kinsta"}); got != 1 {
			t.Fatalf("Run() = %d, want 1", got)
		}
	})
	for _, want := range []string{"Provider kinsta preflight failed.", "Missing: KINSTA_API_KEY", "No remote API call was made."} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, output)
		}
	}
}

func TestRunTargetListAndShowUseStateTargets(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	servers := map[string]any{"servers": map[string]any{"app1-linode": map[string]any{"id": 98222343, "name": "app1-linode", "provider": "linode", "hostname": "app1.nfweb.dev", "status": "active", "ssh": map[string]any{"user": "nonfiction", "host": "app1.nfweb.dev"}}}}
	data, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "servers.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	listOutput := captureStdout(t, func() {
		if got := Run([]string{"target", "list"}); got != 0 {
			t.Fatalf("Run(target list) = %d, want 0", got)
		}
	})
	for _, want := range []string{"target", "app1-linode", "linode", "app1.nfweb.dev", "active"} {
		if !strings.Contains(listOutput, want) {
			t.Fatalf("target list output missing %q:\n%s", want, listOutput)
		}
	}

	showOutput := captureStdout(t, func() {
		if got := Run([]string{"target", "show", "app1-linode"}); got != 0 {
			t.Fatalf("Run(target show) = %d, want 0", got)
		}
	})
	for _, want := range []string{`"name": "app1-linode"`, `"provider": "linode"`, `"hostname": "app1.nfweb.dev"`} {
		if !strings.Contains(showOutput, want) {
			t.Fatalf("target show output missing %q:\n%s", want, showOutput)
		}
	}
}

func TestRunSiteRefreshReportsStateCachePaths(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "list", "--refresh"}); got != 0 {
			t.Fatalf("Run(site list --refresh) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Provider refresh is not implemented yet; using local state cache.", filepath.Join(stateDir, "sites.json"), filepath.Join(stateDir, "servers.json"), "No sites found."} {
		if !strings.Contains(output, want) {
			t.Fatalf("site list --refresh output missing %q:\n%s", want, output)
		}
	}
}

func TestRunSiteEnvListAndShowUseCachedSites(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	sites := map[string]any{"sites": map[string]any{
		"live-client-kinsta":    map[string]any{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "branch": "main", "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-live"}},
		"staging-client-kinsta": map[string]any{"provider": "kinsta", "site_id": "client-kinsta", "env": "staging", "url": "https://staging.example.com/", "branch": "develop", "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-staging"}},
	}}
	data, err := json.MarshalIndent(sites, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "sites.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(sites) error = %v", err)
	}

	listOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "env", "list", "client-kinsta"}); got != 0 {
			t.Fatalf("Run(site env list) = %d, want 0", got)
		}
	})
	for _, want := range []string{"env", "site", "live", "staging", "client-kinsta", "https://www.example.com/", "develop"} {
		if !strings.Contains(listOutput, want) {
			t.Fatalf("site env list output missing %q:\n%s", want, listOutput)
		}
	}

	showOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "env", "show", "client-kinsta", "staging"}); got != 0 {
			t.Fatalf("Run(site env show) = %d, want 0", got)
		}
	})
	for _, want := range []string{`"requested_site": "client-kinsta"`, `"requested_env": "staging"`, `"resolved_site": "client-kinsta"`, `"resolved_env": "staging"`, `"kinsta_environment_id": "kenv-staging"`} {
		if !strings.Contains(showOutput, want) {
			t.Fatalf("site env show output missing %q:\n%s", want, showOutput)
		}
	}
}

func TestRunSiteEnvShellAndWpPreflightWithoutRunningRemoteCommands(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	sites := map[string]any{"sites": map[string]any{"live-client-kinsta": map[string]any{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-live"}}}}
	data, err := json.MarshalIndent(sites, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(sites) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "sites.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(sites) error = %v", err)
	}

	shellStderr := captureStderr(t, func() {
		stdout := captureStdout(t, func() {
			if got := Run([]string{"site", "env", "shell", "client-kinsta", "live"}); got != 1 {
				t.Fatalf("Run(site env shell) = %d, want 1 while remote shell is unimplemented", got)
			}
		})
		for _, want := range []string{"Site env shell preflight:", "site:     client-kinsta", "env:      live", "provider: kinsta", "url:      https://www.example.com/"} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("site env shell stdout missing %q:\n%s", want, stdout)
			}
		}
	})
	if !strings.Contains(shellStderr, "Remote site env shell is not implemented yet; no command was run.") {
		t.Fatalf("site env shell stderr = %q", shellStderr)
	}

	wpStderr := captureStderr(t, func() {
		stdout := captureStdout(t, func() {
			if got := Run([]string{"site", "env", "wp", "client-kinsta", "live", "--", "plugin", "list"}); got != 1 {
				t.Fatalf("Run(site env wp) = %d, want 1 while remote wp is unimplemented", got)
			}
		})
		for _, want := range []string{"Site env wp preflight:", "site:     client-kinsta", "env:      live", "provider: kinsta", "wp args:  plugin list"} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("site env wp stdout missing %q:\n%s", want, stdout)
			}
		}
	})
	if !strings.Contains(wpStderr, "Remote site env wp is not implemented yet; no command was run.") {
		t.Fatalf("site env wp stderr = %q", wpStderr)
	}
}

func TestRunEnvPushPreflightsRepoRemoteWithoutSyncing(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_DATA_HOME", t.TempDir())
	sites := map[string]any{"sites": map[string]any{"live-client-kinsta": map[string]any{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-live"}}}}
	data, err := json.MarshalIndent(sites, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(sites) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "sites.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(sites) error = %v", err)
	}

	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.nf) error = %v", err)
	}
	project := map[string]any{
		"schema":    1,
		"project":   map[string]any{"slug": "client", "name": "Client"},
		"wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"},
		"env":       map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
		"deploy":    map[string]any{"remotes": map[string]any{"production": map[string]any{"site_id": "client-kinsta", "env": "live"}}},
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".nf", "project.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(project) error = %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	stderr := captureStderr(t, func() {
		stdout := captureStdout(t, func() {
			if got := Run([]string{"env", "push", "production"}); got != 1 {
				t.Fatalf("Run(env push) = %d, want 1 while sync is unimplemented", got)
			}
		})
		for _, want := range []string{"Env push preflight:", "local project: client", "remote:        production", "site:          client-kinsta", "env:           live", "provider:      kinsta", "url:           https://www.example.com/"} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("env push stdout missing %q:\n%s", want, stdout)
			}
		}
	})
	if !strings.Contains(stderr, "Remote env sync is not implemented yet; no data was changed.") {
		t.Fatalf("env push stderr = %q", stderr)
	}
}

func TestRunRemoteAddListRemoveWritesProjectMetadata(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.nf) error = %v", err)
	}
	project := map[string]any{"schema": 1, "project": map[string]any{"slug": "client"}, "deploy": map[string]any{"targets": map[string]any{}}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	projectPath := filepath.Join(repoRoot, ".nf", "project.json")
	if err := os.WriteFile(projectPath, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(project) error = %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	addOutput := captureStdout(t, func() {
		if got := Run([]string{"remote", "add", "production", "client-app1-linode", "live"}); got != 0 {
			t.Fatalf("Run(remote add) = %d, want 0", got)
		}
	})
	if !strings.Contains(addOutput, "Added remote production -> client-app1-linode live") {
		t.Fatalf("remote add output = %q", addOutput)
	}

	listOutput := captureStdout(t, func() {
		if got := Run([]string{"remote", "list"}); got != 0 {
			t.Fatalf("Run(remote list) = %d, want 0", got)
		}
	})
	for _, want := range []string{"name", "site", "env", "production", "client-app1-linode", "live"} {
		if !strings.Contains(listOutput, want) {
			t.Fatalf("remote list output missing %q:\n%s", want, listOutput)
		}
	}

	projectData, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("ReadFile(project) error = %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(projectData, &metadata); err != nil {
		t.Fatalf("Unmarshal(project) error = %v", err)
	}
	remote, ok := mapMapAtPath(metadata, "deploy", "remotes", "production")["site_id"].(string)
	if !ok || remote != "client-app1-linode" {
		t.Fatalf("deploy.remotes.production.site_id = %#v, want client-app1-linode", mapMapAtPath(metadata, "deploy", "remotes", "production"))
	}

	removeOutput := captureStdout(t, func() {
		if got := Run([]string{"remote", "remove", "production"}); got != 0 {
			t.Fatalf("Run(remote remove) = %d, want 0", got)
		}
	})
	if !strings.Contains(removeOutput, "Removed remote production") {
		t.Fatalf("remote remove output = %q", removeOutput)
	}
	listAfterRemove := captureStdout(t, func() {
		if got := Run([]string{"remote", "list"}); got != 0 {
			t.Fatalf("Run(remote list after remove) = %d, want 0", got)
		}
	})
	if !strings.Contains(listAfterRemove, "No remotes found.") {
		t.Fatalf("remote list after remove output = %q", listAfterRemove)
	}
}

func TestRunEnvHelpShowsCommandsWithoutShortcuts(t *testing.T) {
	output := captureStdout(t, func() { _ = runEnvHelp() })
	for _, wanted := range []string{"env\n\nCommands:\n", "\n  show                show local env paths, ports, and URLs\n", "\n  up                  start the local env\n", "\n  down                stop the local env\n", "\n  shell               open a shell in the local env\n", "\n  logs                tail WordPress logs\n", "\n  reset               destroy and recreate the local env\n", "\n  wp -- <args>        run wp-cli in the local env\n", "\n  push <remote>       preflight a remote env push\n", "\n  pull <remote>       preflight a remote env pull\n", "\n  snapshot            manage/list env snapshots\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runEnvHelp() output missing %q:\n%s", wanted, output)
		}
	}
	for _, unwanted := range []string{"Shortcuts:", "nf env snapshots", "snapshot create", "snapshot restore", "instance"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("runEnvHelp() output unexpectedly contained %q:\n%s", unwanted, output)
		}
	}
}

func TestRunEnvSnapshotHelpShowsDedicatedCommands(t *testing.T) {
	output := captureStdout(t, func() { _ = runEnvSnapshot([]string{"help"}) })
	for _, want := range []string{"env snapshot\n\nCommands:\n", "\n  add [name]          create an env snapshot\n", "\n  list                list env snapshots\n", "\n  use [name]          restore an env snapshot\n", "\n  remove [name]       delete an env snapshot\n"} {
		if !strings.Contains(output, want) {
			t.Fatalf("runEnvSnapshot(help) output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "alias") || strings.Contains(output, "instance") || strings.Contains(output, "snapshot create") || strings.Contains(output, "snapshot restore") {
		t.Fatalf("runEnvSnapshot(help) output unexpectedly mentioned removed alias:\n%s", output)
	}
}

func TestRunEnvSnapshotAddSkipsComposeUpWhenReady(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll(theme) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "theme", "style.css"), []byte("/* demo */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(style.css) error = %v", err)
	}
	project := map[string]any{"schema": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"}, "env": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".nf", "project.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	snapshotDir := filepath.Join(config.DataHome(), "snapshots", "client", "demo-snapshot")
	dockerDir := t.TempDir()
	dockerScript := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() {
		if got := Run([]string{"env", "snapshot", "add", "demo-snapshot"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, want := range []string{"Snapshot created.", "project: client", "name: demo-snapshot", "> docker compose run --rm cli wp core is-installed --allow-root", "> docker compose run --rm cli wp theme is-active theme --allow-root"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "> docker compose up -d") {
		t.Fatalf("Run() output unexpectedly included compose up:\n%s", output)
	}
	if strings.Contains(output, "> docker compose run --rm cli sh -lc") {
		t.Fatalf("Run() output unexpectedly exposed snapshot shell script preview:\n%s", output)
	}
	if _, err := os.Stat(filepath.Join(snapshotDir, "snapshot.json")); err != nil {
		t.Fatalf("snapshot metadata missing: %v", err)
	}
}

func TestRunEnvSnapshotListShowsSnapshots(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	project := map[string]any{"schema": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"}, "env": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".nf", "project.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll(theme) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "theme", "style.css"), []byte("/* demo */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(style.css) error = %v", err)
	}
	snapshotDir := filepath.Join(config.DataHome(), "snapshots", "client", "2026-05-28-093012")
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(snapshotDir) error = %v", err)
	}
	meta := envSnapshotMetadata{
		Schema:         envSnapshotSchema,
		Name:           "2026-05-28-093012",
		ProjectSlug:    "client",
		CreatedAt:      "2026-05-28T09:30:12Z",
		EnvPath:        filepath.Join(config.DataHome(), "envs", "client"),
		ComposeProject: "nf_client_env",
		WordpressURL:   "http://localhost:18432",
		Contents:       envSnapshotContents{Database: "database.sql.gz", WpContent: "wp-content.tar.gz", WpContentPaths: envSnapshotContentPaths()},
	}
	metaJSON, err := envSnapshotMetadataJSON(meta)
	if err != nil {
		t.Fatalf("envSnapshotMetadataJSON() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "snapshot.json"), []byte(metaJSON), 0o644); err != nil {
		t.Fatalf("WriteFile(snapshot.json) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "database.sql.gz"), []byte("db"), 0o644); err != nil {
		t.Fatalf("WriteFile(database.sql.gz) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "wp-content.tar.gz"), []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile(wp-content.tar.gz) error = %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() {
		if got := Run([]string{"env", "snapshot", "list"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, want := range []string{"2026-05-28-093012", "2026-05-28 09:30:12", "2 B", "7 B", snapshotDir} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, output)
		}
	}
}

func TestRunEnvSnapshotUseSkipsComposeUpWhenReady(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll(theme) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "theme", "style.css"), []byte("/* demo */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(style.css) error = %v", err)
	}
	project := map[string]any{"schema": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"}, "env": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".nf", "project.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(project.json) error = %v", err)
	}
	sourceSnapshotDir := filepath.Join(config.DataHome(), "snapshots", "client", "restore-source")
	if err := os.MkdirAll(sourceSnapshotDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(sourceSnapshotDir) error = %v", err)
	}
	sourceMeta := envSnapshotMetadata{
		Schema:         envSnapshotSchema,
		Name:           "restore-source",
		ProjectSlug:    "client",
		CreatedAt:      "2026-05-28T09:30:12Z",
		EnvPath:        filepath.Join(config.DataHome(), "envs", "client"),
		ComposeProject: "nf_client_env",
		WordpressURL:   "http://localhost:18432",
		Contents:       envSnapshotContents{Database: "database.sql.gz", WpContent: "wp-content.tar.gz", WpContentPaths: envSnapshotContentPaths()},
	}
	sourceMetaJSON, err := envSnapshotMetadataJSON(sourceMeta)
	if err != nil {
		t.Fatalf("envSnapshotMetadataJSON() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceSnapshotDir, "snapshot.json"), []byte(sourceMetaJSON), 0o644); err != nil {
		t.Fatalf("WriteFile(snapshot.json) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceSnapshotDir, "database.sql.gz"), []byte("db"), 0o644); err != nil {
		t.Fatalf("WriteFile(database.sql.gz) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceSnapshotDir, "wp-content.tar.gz"), []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile(wp-content.tar.gz) error = %v", err)
	}
	dockerDir := t.TempDir()
	logPath := filepath.Join(dockerDir, "docker-args.txt")
	dockerScript := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$DOCKER_LOG\"\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	t.Setenv("DOCKER_LOG", logPath)
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldIsInteractive := envSnapshotIsInteractive
	oldConfirm := envSnapshotConfirm
	envSnapshotIsInteractive = func() bool { return true }
	envSnapshotConfirm = func(prompt string, defaultYes bool) (bool, error) { return true, nil }
	t.Cleanup(func() {
		envSnapshotIsInteractive = oldIsInteractive
		envSnapshotConfirm = oldConfirm
	})
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() {
		if got := Run([]string{"env", "snapshot", "use", "restore-source"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, want := range []string{"Snapshot restored.", "name: restore-source", "Safety snapshot:", "> docker compose run --rm cli wp core is-installed --allow-root", "> docker compose run --rm cli wp theme is-active theme --allow-root"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "> docker compose up -d") {
		t.Fatalf("Run() output unexpectedly included compose up:\n%s", output)
	}
	if strings.Contains(output, "> docker compose run --rm cli sh -lc") {
		t.Fatalf("Run() output unexpectedly exposed snapshot shell script preview:\n%s", output)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(docker log) error = %v", err)
	}
	logText := string(logData)
	if strings.Index(logText, "wp db export") == -1 || strings.Index(logText, "wp db import") == -1 || strings.Index(logText, "wp db export") > strings.Index(logText, "wp db import") {
		t.Fatalf("restore command order looks wrong:\n%s", logText)
	}
}

func TestRunEnvSnapshotRemoveRemovesSnapshotAfterConfirmation(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	project := map[string]any{"schema": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"}, "env": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".nf", "project.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(project.json) error = %v", err)
	}
	snapshotDir := filepath.Join(config.DataHome(), "snapshots", "client", "delete-me")
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(snapshotDir) error = %v", err)
	}
	meta := envSnapshotMetadata{Schema: envSnapshotSchema, Name: "delete-me", ProjectSlug: "client", CreatedAt: "2026-05-28T09:30:12Z", EnvPath: filepath.Join(config.DataHome(), "envs", "client"), ComposeProject: "nf_client_env", WordpressURL: "http://localhost:18432", Contents: envSnapshotContents{Database: "database.sql.gz", WpContent: "wp-content.tar.gz", WpContentPaths: envSnapshotContentPaths()}}
	metaJSON, err := envSnapshotMetadataJSON(meta)
	if err != nil {
		t.Fatalf("envSnapshotMetadataJSON() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "snapshot.json"), []byte(metaJSON), 0o644); err != nil {
		t.Fatalf("WriteFile(snapshot.json) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "database.sql.gz"), []byte("db"), 0o644); err != nil {
		t.Fatalf("WriteFile(database.sql.gz) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "wp-content.tar.gz"), []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile(wp-content.tar.gz) error = %v", err)
	}
	oldIsInteractive := envSnapshotIsInteractive
	oldConfirm := envSnapshotConfirm
	envSnapshotIsInteractive = func() bool { return true }
	envSnapshotConfirm = func(prompt string, defaultYes bool) (bool, error) { return true, nil }
	t.Cleanup(func() {
		envSnapshotIsInteractive = oldIsInteractive
		envSnapshotConfirm = oldConfirm
	})
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() {
		if got := Run([]string{"env", "snapshot", "remove", "delete-me"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Deleted env snapshot.") || !strings.Contains(output, "name: delete-me") || !strings.Contains(output, snapshotDir) {
		t.Fatalf("Run() output = %q, want delete confirmation", output)
	}
	if _, err := os.Stat(snapshotDir); !os.IsNotExist(err) {
		t.Fatalf("snapshot dir still exists: %v", err)
	}
}

func TestRunThemeHelpShowsThemeCommandsInsideGit(t *testing.T) {
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	project := map[string]any{"schema": 1, "project": map[string]any{"slug": "client"}, "tasks": map[string]any{"build": map[string]any{"description": "Build the theme assets", "run": "npm run build"}}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".nf", "project.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() { _ = runThemeHelp() })
	for _, wanted := range []string{"\n  tasks               list configured theme tasks\n", "\n  package [--dry-run] [--source] [--output]   package theme artifacts\n", "\nTheme tasks:\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runThemeHelp() output missing %q:\n%s", wanted, output)
		}
	}
}

func TestRunThemeHelpShowsCommandsOnlyOutsideGit(t *testing.T) {
	workdir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() { _ = runThemeHelp() })
	for _, want := range []string{"\n  tasks               list configured theme tasks\n", "\n  package [--dry-run] [--source] [--output]   package theme artifacts\n"} {
		if !strings.Contains(output, want) {
			t.Fatalf("runThemeHelp() output missing %q:\n%s", want, output)
		}
	}
	for _, unwanted := range []string{"\n  init\n", "\n  run <name>\n", "Theme tasks:"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("runThemeHelp() output unexpectedly contained %q:\n%s", unwanted, output)
		}
	}
}

func TestRunSiteShowResolvesAliasAndIncludesServerSummary(t *testing.T) {
	configHome := t.TempDir()
	stateDir := filepath.Join(configHome, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	servers := map[string]any{
		"servers": map[string]any{
			"app1": map[string]any{"id": 98222343, "name": "app1", "provider": "linode", "hostname": "app1.nfweb.dev", "ssh": map[string]any{"user": "nonfiction", "host": "app1.nfweb.dev"}},
		},
	}
	serverData, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "servers.json"), append(serverData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	sites := map[string]any{
		"sites": map[string]any{
			"client-app1-production": map[string]any{"provider": "linode", "server": "app1", "hostname": "client.app1.nfweb.dev", "url": "https://client.app1.nfweb.dev/", "branch": "main", "environment": "production"},
		},
	}
	siteData, err := json.MarshalIndent(sites, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "sites.json"), append(siteData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	project := map[string]any{
		"schema":    1,
		"project":   map[string]any{"slug": "client", "name": "Client", "type": "wordpress-theme"},
		"wordpress": map[string]any{"deploy_unit": "theme", "theme_slug": "theme", "theme_path": "theme"},
		"build":     map[string]any{"steps": []any{"composer install", "npm run build"}},
		"artifact":  map[string]any{"include": []any{"vendor/", "assets/dist/"}, "exclude": []any{"node_modules/", ".git/"}},
		"deploy":    map[string]any{"targets": map[string]any{"app1": "client-app1-production", "production": "client-app1-production", "staging": "client-app1-staging"}},
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".nf", "project.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	oldConfigHome := os.Getenv("NF_CONFIG_HOME")
	oldStateHome := os.Getenv("NF_STATE_HOME")
	if err := os.Setenv("NF_CONFIG_HOME", configHome); err != nil {
		t.Fatalf("Setenv() error = %v", err)
	}
	if err := os.Setenv("NF_STATE_HOME", stateDir); err != nil {
		t.Fatalf("Setenv() error = %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Setenv("NF_CONFIG_HOME", oldConfigHome)
		_ = os.Setenv("NF_STATE_HOME", oldStateHome)
		_ = os.Chdir(oldwd)
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "show", "app1"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, wanted := range []string{`"requested_target": "app1"`, `"resolved_target": "client-app1-production"`, `"resolved_server_summary": "app1 / id 98222343 / linode / ssh nonfiction@app1.nfweb.dev"`, `"url": "https://client.app1.nfweb.dev/"`} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("Run() output missing %q:\n%s", wanted, output)
		}
	}
}

func TestRunSiteShowUsesDirectTargetWithoutAlias(t *testing.T) {
	configHome := t.TempDir()
	stateDir := filepath.Join(configHome, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	servers := map[string]any{"servers": map[string]any{"app1": map[string]any{"id": 98222343, "name": "app1", "provider": "linode", "ssh": map[string]any{"user": "nonfiction", "host": "app1.nfweb.dev"}}}}
	serverData, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "servers.json"), append(serverData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	sites := map[string]any{"sites": map[string]any{"client-app1-production": map[string]any{"provider": "linode", "server": "app1", "hostname": "client.app1.nfweb.dev", "branch": "main"}}}
	siteData, err := json.MarshalIndent(sites, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "sites.json"), append(siteData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	project := map[string]any{"schema": 1, "project": map[string]any{"slug": "client"}, "deploy": map[string]any{"targets": map[string]any{"app1": "client-app1-production"}}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".nf", "project.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	oldConfigHome := os.Getenv("NF_CONFIG_HOME")
	oldStateHome := os.Getenv("NF_STATE_HOME")
	if err := os.Setenv("NF_CONFIG_HOME", configHome); err != nil {
		t.Fatalf("Setenv() error = %v", err)
	}
	if err := os.Setenv("NF_STATE_HOME", stateDir); err != nil {
		t.Fatalf("Setenv() error = %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Setenv("NF_CONFIG_HOME", oldConfigHome)
		_ = os.Setenv("NF_STATE_HOME", oldStateHome)
		_ = os.Chdir(oldwd)
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "show", "client-app1-production"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, wanted := range []string{`"requested_target": "client-app1-production"`, `"resolved_target": "client-app1-production"`, `"server": "app1"`} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("Run() output missing %q:\n%s", wanted, output)
		}
	}
}

func TestRunSiteShowResolvesRepoRemoteAlias(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	sites := map[string]any{"sites": map[string]any{"client-kinsta": map[string]any{"provider": "kinsta", "url": "https://www.example.com/", "environment": "live", "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv123"}}}}
	data, err := json.MarshalIndent(sites, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "sites.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(sites) error = %v", err)
	}

	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.nf) error = %v", err)
	}
	project := map[string]any{"schema": 1, "project": map[string]any{"slug": "client"}, "deploy": map[string]any{"remotes": map[string]any{"production": map[string]any{"site_id": "client-kinsta", "env": "live"}}}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".nf", "project.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(project) error = %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "show", "production"}); got != 0 {
			t.Fatalf("Run(site show production) = %d, want 0", got)
		}
	})
	for _, want := range []string{`"requested_target": "production"`, `"resolved_target": "client-kinsta"`, `"kinsta_site_id": "ksite123"`, `"kinsta_environment_id": "kenv123"`} {
		if !strings.Contains(output, want) {
			t.Fatalf("site show remote alias output missing %q:\n%s", want, output)
		}
	}
}

func TestRunInitWritesPortableMetadataShape(t *testing.T) {
	workdir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	if got := Run([]string{"init", "--project-slug", "client", "--force"}); got != 0 {
		t.Fatalf("Run() = %d, want 0", got)
	}
	data, err := os.ReadFile(filepath.Join(workdir, ".nf", "project.json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if metadata["schema"] != float64(1) {
		t.Fatalf("schema = %v, want 1", metadata["schema"])
	}
	if project, ok := metadata["project"].(map[string]any); !ok || project["slug"] != "client" {
		t.Fatalf("project block = %#v, want slug client", metadata["project"])
	}
	if wordpress, ok := metadata["wordpress"].(map[string]any); !ok || wordpress["theme_path"] != "theme" || wordpress["theme_slug"] != "theme" {
		t.Fatalf("wordpress block = %#v, want theme_path theme and theme_slug theme", metadata["wordpress"])
	}
	if env, ok := metadata["env"].(map[string]any); !ok {
		t.Fatalf("env block = %#v, want env config", metadata["env"])
	} else {
		for key, want := range map[string]string{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"} {
			if got := env[key]; got != want {
				t.Fatalf("env.%s = %#v, want %q", key, got, want)
			}
		}
		if _, exists := env["ports"]; exists {
			t.Fatalf("env.ports unexpectedly present: %#v", env["ports"])
		}
		if _, exists := env["path"]; exists {
			t.Fatalf("env.path unexpectedly present: %#v", env)
		}
	}
	if build, ok := metadata["build"].(map[string]any); !ok {
		t.Fatalf("build block = %#v, want steps list", metadata["build"])
	} else if steps, ok := build["steps"].([]any); !ok || len(steps) != 2 {
		t.Fatalf("build.steps = %#v, want two steps", build["steps"])
	}
	if artifact, ok := metadata["artifact"].(map[string]any); !ok || artifact["path"] != "dist/client-v{version}.zip" {
		t.Fatalf("artifact block = %#v, want dist/client-v{version}.zip", metadata["artifact"])
	} else if include, ok := artifact["include"].([]any); !ok || len(include) != 2 {
		t.Fatalf("artifact.include = %#v, want include paths", artifact["include"])
	} else if exclude, ok := artifact["exclude"].([]any); !ok || len(exclude) != 2 {
		t.Fatalf("artifact.exclude = %#v, want exclude paths", artifact["exclude"])
	}
	if deploy, ok := metadata["deploy"].(map[string]any); !ok {
		t.Fatalf("deploy block = %#v, want targets map", metadata["deploy"])
	} else if targets, ok := deploy["targets"].(map[string]any); !ok || len(targets) != 0 {
		t.Fatalf("deploy.targets = %#v, want empty map", deploy["targets"])
	}
	if tasks, ok := metadata["tasks"].(map[string]any); !ok {
		t.Fatalf("tasks block = %#v, want task map", metadata["tasks"])
	} else {
		for _, want := range []string{"composer", "npm", "build", "watch", "test"} {
			if tasks[want] == nil {
				t.Fatalf("tasks block missing %q: %#v", want, tasks)
			}
		}
		if len(tasks) != 5 {
			t.Fatalf("tasks block len = %d, want 5", len(tasks))
		}
	}
	for _, legacy := range []string{"project_slug", "project_name", "theme_slug", "theme_source", "default_provider"} {
		if _, ok := metadata[legacy]; ok {
			t.Fatalf("legacy field %q unexpectedly present: %#v", legacy, metadata[legacy])
		}
	}
	if project, ok := metadata["project"].(map[string]any); !ok || project["type"] != "wordpress-theme" {
		t.Fatalf("project block = %#v, want type wordpress-theme", metadata["project"])
	}
	if wordpress, ok := metadata["wordpress"].(map[string]any); !ok || wordpress["deploy_unit"] != "theme" {
		t.Fatalf("wordpress block = %#v, want deploy_unit theme", metadata["wordpress"])
	}
	if build, ok := metadata["build"].(map[string]any); ok {
		if _, exists := build["commands"]; exists {
			t.Fatalf("build.commands unexpectedly present: %#v", metadata["build"])
		}
		if _, exists := build["source"]; exists {
			t.Fatalf("build.source unexpectedly present: %#v", metadata["build"])
		}
	}
	if _, err := os.Stat(filepath.Join(workdir, "instance")); !os.IsNotExist(err) {
		t.Fatalf("instance scaffold unexpectedly created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "env")); !os.IsNotExist(err) {
		t.Fatalf("env scaffold unexpectedly created: %v", err)
	}
}

func TestRunInitDefaultsProjectSlugFromGitRoot(t *testing.T) {
	repoRoot := filepath.Join(t.TempDir(), "client-site")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	workdir := filepath.Join(repoRoot, "nested")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	if got := Run([]string{"init"}); got != 0 {
		t.Fatalf("Run() = %d, want 0", got)
	}
	data, err := os.ReadFile(filepath.Join(workdir, ".nf", "project.json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if project, ok := metadata["project"].(map[string]any); !ok || project["slug"] != "client-site" {
		t.Fatalf("project block = %#v, want slug client-site", metadata["project"])
	} else if project["name"] != "Client Site" {
		t.Fatalf("project block = %#v, want name Client Site", metadata["project"])
	}
}

func TestRunInitWithoutProjectSlugOutsideGitFails(t *testing.T) {
	workdir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStderr(t, func() {
		if got := Run([]string{"init"}); got != 1 {
			t.Fatalf("Run() = %d, want 1", got)
		}
	})
	if !strings.Contains(output, "init requires a .git repository above the current directory when --project-slug is not set") {
		t.Fatalf("Run() stderr = %q, want missing-git-root error", output)
	}
}

func TestRunInitHonorsExplicitThemeSlug(t *testing.T) {
	workdir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	if got := Run([]string{"init", "--project-slug", "client", "--theme-slug", "custom-theme", "--force"}); got != 0 {
		t.Fatalf("Run() = %d, want 0", got)
	}
	data, err := os.ReadFile(filepath.Join(workdir, ".nf", "project.json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if wordpress, ok := metadata["wordpress"].(map[string]any); !ok || wordpress["theme_slug"] != "custom-theme" || wordpress["theme_path"] != "theme" {
		t.Fatalf("wordpress block = %#v, want explicit theme_slug custom-theme and theme_path theme", metadata["wordpress"])
	}
}

func TestRunInitWithoutForceRejectsExistingProjectJson(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	projectPath := filepath.Join(workdir, ".nf", "project.json")
	if err := os.WriteFile(projectPath, []byte("{\n}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStderr(t, func() {
		if got := Run([]string{"init", "--project-slug", "client"}); got != 1 {
			t.Fatalf("Run() = %d, want 1", got)
		}
	})
	if !strings.Contains(output, projectPath+" already exists; use --force to overwrite.") {
		t.Fatalf("Run() stderr = %q, want existing-file warning", output)
	}
	data, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "{\n}\n" {
		t.Fatalf("project.json changed unexpectedly: %q", string(data))
	}
}

func TestRunInitRejectsUnsupportedProjectType(t *testing.T) {
	workdir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStderr(t, func() {
		if got := Run([]string{"init", "--project-slug", "client", "--type", "wordpress-plugin"}); got != 1 {
			t.Fatalf("Run() = %d, want 1", got)
		}
	})
	if !strings.Contains(output, "unsupported init type \"wordpress-plugin\"; only wordpress-theme is supported") {
		t.Fatalf("Run() stderr = %q, want unsupported type error", output)
	}
}

func TestRenderEnvComposeUsesMetadataDefaults(t *testing.T) {
	root := t.TempDir()
	metadata := map[string]any{
		"project":   map[string]any{"slug": "client"},
		"wordpress": map[string]any{"theme_path": "theme-src"},
		"env":       map[string]any{"compose": "docker compose", "wordpress_service": "wp-app", "cli_service": "wp-cli", "theme_mount_slug": "theme-slot", "uploads_path": "uploads"},
	}
	cfg, ok := loadEnvConfig(root, metadata)
	if !ok {
		t.Fatalf("loadEnvConfig() = false, want true")
	}
	compose := renderEnvCompose(cfg)
	for _, want := range []string{"wp-app:", "wp-cli:", "condition: service_healthy", "depends_on:\n      wp-app:", "working_dir: /var/www/html", filepath.Join(root, "theme-src") + ":/var/www/html/wp-content/themes/theme-slot", config.SnapshotProjectDir("client") + ":/env-snapshots"} {
		if !strings.Contains(compose, want) {
			t.Fatalf("renderEnvCompose() missing %q:\n%s", want, compose)
		}
	}
	if strings.Contains(compose, "\t") {
		t.Fatalf("renderEnvCompose() contains a tab character:\n%s", compose)
	}
}

func TestEnvSnapshotHelpersValidateNamesAndRenderMetadata(t *testing.T) {
	if got, want := defaultEnvSnapshotName(time.Date(2026, 5, 28, 9, 30, 12, 0, time.UTC)), "2026-05-28-093012"; got != want {
		t.Fatalf("defaultEnvSnapshotName() = %q, want %q", got, want)
	}
	if got, want := defaultPreRestoreSnapshotName(time.Date(2026, 5, 28, 9, 30, 12, 0, time.UTC)), "2026-05-28-093012-pre-restore"; got != want {
		t.Fatalf("defaultPreRestoreSnapshotName() = %q, want %q", got, want)
	}
	for input, want := range map[string]string{"demo snapshot": "demo-snapshot", "  demo   snapshot  ": "demo-snapshot", "snapshot-1": "snapshot-1"} {
		got, err := envSnapshotNormalizedName(input)
		if err != nil {
			t.Fatalf("envSnapshotNormalizedName(%q) error = %v", input, err)
		}
		if got != want {
			t.Fatalf("envSnapshotNormalizedName(%q) = %q, want %q", input, got, want)
		}
	}
	for _, input := range []string{"", "../snapshot", "/tmp/snapshot", "snapshot/name", "snapshot\\name", "snapshot..name", "snapshot.name", "snapshot?name"} {
		if got, err := envSnapshotNormalizedName(input); err == nil {
			t.Fatalf("envSnapshotNormalizedName(%q) = %q, want error", input, got)
		}
	}
	meta := envSnapshotMetadata{
		Schema:         envSnapshotSchema,
		Name:           "2026-05-28-093012",
		ProjectSlug:    "client",
		CreatedAt:      "2026-05-28T09:30:12Z",
		EnvPath:        "/data/nf/envs/client",
		ComposeProject: "nf_client_env",
		WordpressURL:   "http://localhost:18432",
		Contents:       envSnapshotContents{Database: "database.sql.gz", WpContent: "wp-content.tar.gz", WpContentPaths: envSnapshotContentPaths()},
	}
	gotJSON, err := envSnapshotMetadataJSON(meta)
	if err != nil {
		t.Fatalf("envSnapshotMetadataJSON() error = %v", err)
	}
	wantJSON := "{\n  \"schema\": 1,\n  \"name\": \"2026-05-28-093012\",\n  \"project_slug\": \"client\",\n  \"created_at\": \"2026-05-28T09:30:12Z\",\n  \"env_path\": \"/data/nf/envs/client\",\n  \"compose_project\": \"nf_client_env\",\n  \"wordpress_url\": \"http://localhost:18432\",\n  \"contents\": {\n    \"database\": \"database.sql.gz\",\n    \"wp_content\": \"wp-content.tar.gz\",\n    \"wp_content_paths\": [\n      \"wp-content/uploads\",\n      \"wp-content/plugins\",\n      \"wp-content/mu-plugins\",\n      \"wp-content/languages\"\n    ]\n  }\n}\n"
	if gotJSON != wantJSON {
		t.Fatalf("envSnapshotMetadataJSON() =\n%s\nwant=\n%s", gotJSON, wantJSON)
	}
}

func TestRunEnvUpAutoInitializesProjectMetadata(t *testing.T) {
	repoRoot := filepath.Join(t.TempDir(), "client-site")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "work"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	dockerDir := t.TempDir()
	logPath := filepath.Join(dockerDir, "docker-args.txt")
	dockerScript := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$DOCKER_LOG\"\ncase \"$*\" in\n  *\"wp core is-installed\"*) exit 1 ;;\nesac\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	t.Setenv("DOCKER_LOG", logPath)
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(filepath.Join(repoRoot, "work")); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	projectPath := filepath.Join(repoRoot, ".nf", "project.json")
	output := captureStdout(t, func() {
		if got := Run([]string{"env", "up"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, want := range []string{
		"Wrote " + projectPath,
		"> docker compose up -d",
		"> docker compose run --rm cli wp core is-installed --allow-root",
		"> docker compose run --rm cli sh -lc",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, output)
		}
	}
	data, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("ReadFile(project.json) error = %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	project, _ := metadata["project"].(map[string]any)
	if got, want := project["slug"], "client-site"; got != want {
		t.Fatalf("project.slug = %v, want %v", got, want)
	}
}

func TestLoadEnvConfigUsesEnvBlock(t *testing.T) {
	root := t.TempDir()
	metadata := map[string]any{
		"project":   map[string]any{"slug": "client", "name": "Client"},
		"wordpress": map[string]any{"theme_path": "theme-src", "theme_slug": "theme"},
		"env":       map[string]any{"compose": "env compose", "wordpress_service": "env-wp", "cli_service": "env-cli", "theme_mount_slug": "env-theme", "uploads_path": "env-uploads"},
	}
	cfg, ok := loadEnvConfig(root, metadata)
	if !ok {
		t.Fatalf("loadEnvConfig() = false, want true")
	}
	if got, want := cfg.Compose, "env compose"; got != want {
		t.Fatalf("Compose = %q, want %q", got, want)
	}
	if got, want := cfg.WordpressService, "env-wp"; got != want {
		t.Fatalf("WordpressService = %q, want %q", got, want)
	}
	if got, want := cfg.CliService, "env-cli"; got != want {
		t.Fatalf("CliService = %q, want %q", got, want)
	}
	if got, want := cfg.ThemeMountSlug, "env-theme"; got != want {
		t.Fatalf("ThemeMountSlug = %q, want %q", got, want)
	}
	if got, want := cfg.UploadsPath, "env-uploads"; got != want {
		t.Fatalf("UploadsPath = %q, want %q", got, want)
	}
}

func TestRunThemeTasksUsesCompactDescriptions(t *testing.T) {
	repoRoot := filepath.Join(t.TempDir(), "client-site")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	if got := Run([]string{"init", "--force"}); got != 0 {
		t.Fatalf("Run(init) = %d, want 0", got)
	}
	output := captureStdout(t, func() {
		if got := Run([]string{"theme", "tasks"}); got != 0 {
			t.Fatalf("Run(tasks) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Theme tasks:", "Update theme Composer dependencies", "Build the theme assets", "Watch theme assets during development", "Run the theme test suite"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run(tasks) output missing %q:\n%s", want, output)
		}
	}
	for _, unwanted := range []string{"start the managed instance", "run wp-cli passthrough"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("Run(tasks) output unexpectedly contained %q:\n%s", unwanted, output)
		}
	}
	if strings.Contains(output, "name  description  run") || strings.Contains(output, "\n  run ") {
		t.Fatalf("Run(tasks) output still looks wide:\n%s", output)
	}
}

func TestRunThemeTaskPreservesPassthroughSeparator(t *testing.T) {
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll(theme) error = %v", err)
	}
	project := map[string]any{
		"schema":    1,
		"project":   map[string]any{"slug": "client", "name": "Client", "type": "wordpress-theme"},
		"wordpress": map[string]any{"deploy_unit": "theme", "theme_slug": "theme", "theme_path": "theme"},
		"env":       map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
		"tasks": map[string]any{
			"capture": map[string]any{"description": "Capture passthrough args", "run": []any{"sh", "-c", "printf '%s\n' \"$@\" > \"$CAPTURE_FILE\"", "sh"}},
		},
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".nf", "project.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	capturePath := filepath.Join(t.TempDir(), "capture.txt")
	t.Setenv("CAPTURE_FILE", capturePath)

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	if got := Run([]string{"theme", "capture", "--", "--watch", "--color"}); got != 0 {
		t.Fatalf("Run() = %d, want 0", got)
	}
	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := strings.Split(strings.TrimSpace(string(data)), "\n"), []string{"--watch", "--color"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("captured args = %#v, want %#v", got, want)
	}
}

func TestEnvComposeProjectName(t *testing.T) {
	for input, want := range map[string]string{
		"client":        "nf_client_env",
		" Client Site ": "nf_client_site_env",
		"":              "nf_project_env",
	} {
		if got := envComposeProjectName(input); got != want {
			t.Fatalf("envComposeProjectName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestEnvDerivedPortsUseCleanedSlug(t *testing.T) {
	wpA, mailpitA := envDerivedPorts(" Client Site ")
	wpB, mailpitB := envDerivedPorts("client_site")
	if wpA != wpB || mailpitA != mailpitB {
		t.Fatalf("envDerivedPorts() = (%d, %d) and (%d, %d), want matching ports", wpA, mailpitA, wpB, mailpitB)
	}
	if mailpitA != wpA+1 {
		t.Fatalf("envDerivedPorts() mailpit = %d, want wordpress+1 (%d)", mailpitA, wpA+1)
	}
	if wpA < 18000 || mailpitA > 21999 {
		t.Fatalf("envDerivedPorts() = (%d, %d), want ports in 18000-21999 block", wpA, mailpitA)
	}
}

func TestRenderEnvFileUsesComposeProjectName(t *testing.T) {
	wpPort, mailpitPort := envDerivedPorts("client")
	cfg := envConfig{ProjectSlug: "client", ProjectName: "Client", WordpressPort: wpPort, MailpitPort: mailpitPort}
	want := fmt.Sprintf("COMPOSE_PROJECT_NAME=nf_client_env\nWP_PORT=%d\nMAILPIT_PORT=%d\nDB_NAME=client\nDB_USER=client\nDB_PASSWORD=wordpress\nDB_ROOT_PASSWORD=root\nWP_URL=http://localhost:%d\nWP_TITLE=Client\nADMIN_USER=admin\nADMIN_PASSWORD=admin\nADMIN_EMAIL=web@nonfiction.ca\n", wpPort, mailpitPort, wpPort)
	if got := renderEnvFile(cfg); got != want {
		t.Fatalf("renderEnvFile() = %q, want %q", got, want)
	}
}

func TestRenderEnvInfoUsesEffectivePorts(t *testing.T) {
	cfg := envConfig{ProjectSlug: "client", ProjectName: "Client", EnvDir: filepath.Join("/data", "envs", "client"), WordpressPort: 18432, MailpitPort: 18433}
	want := "Env:\n  project: client\n  path: /data/envs/client\n  compose project: nf_client_env\n  WordPress: http://localhost:18432\n  Mailpit:   http://localhost:18433"
	if got := renderEnvInfo(cfg, true); got != want {
		t.Fatalf("renderEnvInfo(full) = %q, want %q", got, want)
	}
	want = "Env:\n  project: client\n  path: /data/envs/client\n  compose project: nf_client_env"
	if got := renderEnvInfo(cfg, false); got != want {
		t.Fatalf("renderEnvInfo(short) = %q, want %q", got, want)
	}
}

func TestLoadEnvConfigAppliesPortOverrides(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	metadata := map[string]any{
		"project":   map[string]any{"slug": "client", "name": "Client"},
		"wordpress": map[string]any{"theme_slug": "theme", "theme_path": "theme"},
		"env": map[string]any{
			"compose":           "docker compose",
			"wordpress_service": "wordpress",
			"cli_service":       "cli",
			"theme_mount_slug":  "theme",
			"uploads_path":      "uploads",
			"ports": map[string]any{
				"wordpress": 19111,
				"mailpit":   19112,
			},
		},
	}
	cfg, ok := loadEnvConfig(root, metadata)
	if !ok {
		t.Fatalf("loadEnvConfig() = false, want true")
	}
	if cfg.WordpressPort != 19111 || cfg.MailpitPort != 19112 {
		t.Fatalf("effective ports = (%d, %d), want overrides (19111, 19112)", cfg.WordpressPort, cfg.MailpitPort)
	}
}

func TestLoadEnvConfigFallsBackPerPortIndependently(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	derivedWordpress, derivedMailpit := envDerivedPorts("client")
	for _, tc := range []struct {
		name          string
		envPorts      map[string]any
		wantWordpress int
		wantMailpit   int
	}{
		{name: "wordpress override only", envPorts: map[string]any{"wordpress": 19111, "mailpit": 0}, wantWordpress: 19111, wantMailpit: derivedMailpit},
		{name: "mailpit override only", envPorts: map[string]any{"wordpress": 0, "mailpit": 19112}, wantWordpress: derivedWordpress, wantMailpit: 19112},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata := map[string]any{
				"project":   map[string]any{"slug": "client", "name": "Client"},
				"wordpress": map[string]any{"theme_slug": "theme", "theme_path": "theme"},
				"env": map[string]any{
					"compose":           "docker compose",
					"wordpress_service": "wordpress",
					"cli_service":       "cli",
					"theme_mount_slug":  "theme",
					"uploads_path":      "uploads",
					"ports":             tc.envPorts,
				},
			}
			cfg, ok := loadEnvConfig(root, metadata)
			if !ok {
				t.Fatalf("loadEnvConfig() = false, want true")
			}
			if cfg.WordpressPort != tc.wantWordpress || cfg.MailpitPort != tc.wantMailpit {
				t.Fatalf("effective ports = (%d, %d), want (%d, %d)", cfg.WordpressPort, cfg.MailpitPort, tc.wantWordpress, tc.wantMailpit)
			}
		})
	}
}

func openAdjacentPortPair(t *testing.T) (int, net.Listener, net.Listener) {
	t.Helper()
	for i := 0; i < 20; i++ {
		first, err := net.Listen("tcp", ":0")
		if err != nil {
			t.Fatalf("net.Listen() error = %v", err)
		}
		port := first.Addr().(*net.TCPAddr).Port
		second, err := net.Listen("tcp", fmt.Sprintf(":%d", port+1))
		if err == nil {
			return port, first, second
		}
		_ = first.Close()
	}
	t.Fatal("could not reserve two adjacent ports")
	return 0, nil, nil
}

func TestPreflightEnvPortsDetectsSingleCollision(t *testing.T) {
	wpPort, mailpitPort := envDerivedPorts("client")
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", wpPort))
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	cfg := envConfig{ProjectSlug: "client", EnvDir: filepath.Join("/data", "envs", "client"), WordpressPort: wpPort, MailpitPort: mailpitPort}
	err = preflightEnvPorts(cfg)
	if err == nil {
		t.Fatal("preflightEnvPorts() error = nil, want collision")
	}
	message := err.Error()
	for _, want := range []string{fmt.Sprintf("Port %d is already in use.", wpPort), fmt.Sprintf("WordPress: http://localhost:%d", wpPort), fmt.Sprintf("Mailpit:   http://localhost:%d", mailpitPort)} {
		if !strings.Contains(message, want) {
			t.Fatalf("preflightEnvPorts() error = %q, want %q", message, want)
		}
	}
}

func TestPreflightEnvPortsDetectsBothCollisions(t *testing.T) {
	wpPort, first, second := openAdjacentPortPair(t)
	t.Cleanup(func() {
		_ = first.Close()
		_ = second.Close()
	})

	cfg := envConfig{ProjectSlug: "client", EnvDir: filepath.Join("/data", "envs", "client"), WordpressPort: wpPort, MailpitPort: wpPort + 1}
	err := preflightEnvPorts(cfg)
	if err == nil {
		t.Fatal("preflightEnvPorts() error = nil, want collision")
	}
	message := err.Error()
	for _, want := range []string{fmt.Sprintf("Ports %d and %d are already in use.", wpPort, wpPort+1), fmt.Sprintf("WordPress: http://localhost:%d", wpPort), fmt.Sprintf("Mailpit:   http://localhost:%d", wpPort+1)} {
		if !strings.Contains(message, want) {
			t.Fatalf("preflightEnvPorts() error = %q, want %q", message, want)
		}
	}
}

func TestEnvCommandHelpersBuildExpectedArgs(t *testing.T) {
	cfg := envConfig{
		ProjectSlug:      "client",
		ProjectName:      "Client",
		RepoRoot:         "/repo",
		ThemePath:        "/repo/theme",
		EnvDir:           filepath.Join("/data", "envs", "client"),
		Compose:          "docker compose",
		WordpressService: "wordpress",
		CliService:       "cli",
		ThemeMountSlug:   "theme",
		UploadsPath:      "uploads",
		ThemeSlug:        "client",
	}

	if got, want := envComposeArgs(cfg, "up", "-d"), []string{"docker", "compose", "up", "-d"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("envComposeArgs() = %#v, want %#v", got, want)
	}
	if got, want := envWpArgs(cfg, "plugin", "list"), []string{"docker", "compose", "run", "--rm", "cli", "wp", "plugin", "list", "--allow-root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("envWpArgs() = %#v, want %#v", got, want)
	}
	if got, want := envShellArgs(cfg), []string{"docker", "compose", "exec", "wordpress", "sh"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("envShellArgs() = %#v, want %#v", got, want)
	}
	if got, want := envWpThemeIsActiveArgs(cfg, ""), []string{"docker", "compose", "run", "--rm", "cli", "wp", "theme", "is-active", "theme", "--allow-root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("envWpThemeIsActiveArgs() = %#v, want %#v", got, want)
	}
	hostPath, containerPath := envThemeArchivePaths(cfg, "/tmp/theme.zip")
	if hostPath != filepath.Join(cfg.EnvDir, "uploads", "theme.zip") || containerPath != "/env/uploads/theme.zip" {
		t.Fatalf("envThemeArchivePaths() = (%q, %q), want host and container upload paths", hostPath, containerPath)
	}
	if got, want := envCommandDir(cfg), cfg.EnvDir; got != want {
		t.Fatalf("envCommandDir() = %q, want %q", got, want)
	}
	if got, want := envWpThemeActivateArgs(cfg, ""), []string{"docker", "compose", "run", "--rm", "cli", "wp", "theme", "activate", "theme", "--allow-root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("envWpThemeActivateArgs() = %#v, want %#v", got, want)
	}
	if got, want := envWpThemeActivateArgs(cfg, "custom-slug"), []string{"docker", "compose", "run", "--rm", "cli", "wp", "theme", "activate", "custom-slug", "--allow-root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("envWpThemeActivateArgs(explicit) = %#v, want %#v", got, want)
	}
	installArgs := envWpCoreInstallArgs(cfg)
	joined := strings.Join(installArgs, " ")
	for _, wanted := range []string{"docker compose run --rm cli sh -lc", "WP_URL", "WP_TITLE", "ADMIN_USER", "ADMIN_PASSWORD", "ADMIN_EMAIL", "wp theme activate theme --allow-root"} {
		if !strings.Contains(joined, wanted) {
			t.Fatalf("envWpCoreInstallArgs() missing %q in %#v", wanted, installArgs)
		}
	}
	if strings.Contains(joined, "wp core is-installed") {
		t.Fatalf("envWpCoreInstallArgs() unexpectedly probes install state: %#v", installArgs)
	}
	if got, want := envRepoPath("/repo", "dist/theme.zip"), filepath.Join("/repo", "dist", "theme.zip"); got != want {
		t.Fatalf("envRepoPath() = %q, want %q", got, want)
	}
	if got, want := envRepoPath("/repo", "/tmp/theme.zip"), "/tmp/theme.zip"; got != want {
		t.Fatalf("envRepoPath() = %q, want %q", got, want)
	}
	if got, want := (envCommandRunner{name: "up", cfg: cfg}).Render(), "docker compose up -d; install WordPress if missing and ensure the mounted theme is active"; got != want {
		t.Fatalf("up Render() = %q, want %q", got, want)
	}
	if got, want := (envCommandRunner{name: "reset", cfg: cfg}).Render(), "docker compose down -v --remove-orphans; nuke env data and recreate it with docker compose up -d, install WordPress if missing, and ensure the mounted theme is active"; got != want {
		t.Fatalf("reset Render() = %q, want %q", got, want)
	}
	if got, want := (envCommandRunner{name: "shell", cfg: cfg}).Render(), "docker compose exec wordpress sh"; got != want {
		t.Fatalf("shell Render() = %q, want %q", got, want)
	}
}

func TestEnsureManagedEnvWritesManagedFiles(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "theme", "style.css"), []byte("/* demo */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	metadata := map[string]any{
		"project":   map[string]any{"slug": "client", "name": "Client"},
		"wordpress": map[string]any{"theme_slug": "theme", "theme_path": "theme"},
		"env":       map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
	}
	cfg, ok := loadEnvConfig(root, metadata)
	if !ok {
		t.Fatalf("loadEnvConfig() = false, want true")
	}
	if got, want := cfg.EnvDir, config.EnvDir("client"); got != want {
		t.Fatalf("EnvDir = %q, want %q", got, want)
	}
	wpPort, mailpitPort := envDerivedPorts("client")
	if err := ensureManagedEnv(cfg); err != nil {
		t.Fatalf("ensureManagedInstance() error = %v", err)
	}
	checks := map[string][]string{
		filepath.Join(cfg.EnvDir, "docker-compose.yml"):                   {filepath.Join(root, "theme") + ":/var/www/html/wp-content/themes/theme", "mailpit", "wordpress:cli-php8.4"},
		filepath.Join(cfg.EnvDir, ".env"):                                 {"COMPOSE_PROJECT_NAME=nf_client_env", fmt.Sprintf("WP_PORT=%d", wpPort), fmt.Sprintf("MAILPIT_PORT=%d", mailpitPort), fmt.Sprintf("WP_URL=http://localhost:%d", wpPort), "WP_TITLE=Client"},
		filepath.Join(cfg.EnvDir, "php", "uploads.ini"):                   {"upload_max_filesize=128M", "max_execution_time=120"},
		filepath.Join(cfg.EnvDir, "wordpress", "Dockerfile"):              {"FROM wordpress:7.0-php8.4-apache", "COPY wordpress/wordpress-rewrites.conf"},
		filepath.Join(cfg.EnvDir, "wordpress", "wordpress-rewrites.conf"): {"RewriteRule . /index.php [L]"},
	}
	for path, wants := range checks {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", path, err)
		}
		text := string(data)
		for _, want := range wants {
			if !strings.Contains(text, want) {
				t.Fatalf("%s missing %q:\n%s", path, want, text)
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(cfg.EnvDir, "uploads", ".gitkeep")); err != nil {
		t.Fatalf("ReadFile(.gitkeep) error = %v", err)
	} else if len(data) != 0 {
		t.Fatalf("uploads/.gitkeep = %q, want empty file", string(data))
	}
}

func TestRunEnvUpPrintsUnderlyingCommands(t *testing.T) {
	repoRoot := filepath.Join(t.TempDir(), "client-site")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "theme", "style.css"), []byte("/* demo */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	project := map[string]any{
		"schema":    1,
		"project":   map[string]any{"slug": "client-site", "name": "Client Site", "type": "wordpress-theme"},
		"wordpress": map[string]any{"deploy_unit": "theme", "theme_slug": "theme", "theme_path": "theme"},
		"env":       map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".nf", "project.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	dockerDir := t.TempDir()
	logPath := filepath.Join(dockerDir, "docker-args.txt")
	dockerScript := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$DOCKER_LOG\"\ncase \"$*\" in\n  *\"wp core is-installed\"*) exit 1 ;;\nesac\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	t.Setenv("DOCKER_LOG", logPath)
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() {
		if got := Run([]string{"env", "up"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, want := range []string{
		"> docker compose up -d",
		"> docker compose run --rm cli wp core is-installed --allow-root",
		"> docker compose run --rm cli sh -lc",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output = %q, want %q", output, want)
		}
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("docker log missing: %v", err)
	}
}

func TestRunEnvUpActivatesThemeWhenAlreadyInstalled(t *testing.T) {
	repoRoot := filepath.Join(t.TempDir(), "client-site")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "theme", "style.css"), []byte("/* demo */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	project := map[string]any{
		"schema":    1,
		"project":   map[string]any{"slug": "client-site", "name": "Client Site", "type": "wordpress-theme"},
		"wordpress": map[string]any{"deploy_unit": "theme", "theme_slug": "theme", "theme_path": "theme"},
		"env":       map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".nf", "project.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	dockerDir := t.TempDir()
	logPath := filepath.Join(dockerDir, "docker-args.txt")
	dockerScript := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$DOCKER_LOG\"\ncase \"$*\" in\n  *\"wp theme is-active\"*) exit 1 ;;\n  *\"wp core is-installed\"*) exit 0 ;;\nesac\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	t.Setenv("DOCKER_LOG", logPath)
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() {
		if got := Run([]string{"env", "up"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, want := range []string{
		"> docker compose up -d",
		"> docker compose run --rm cli wp core is-installed --allow-root",
		"> docker compose run --rm cli wp theme is-active theme --allow-root",
		"> docker compose run --rm cli wp theme activate theme --allow-root",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output = %q, want %q", output, want)
		}
	}
	if strings.Contains(output, "wp core install") {
		t.Fatalf("Run() output unexpectedly installed WordPress: %q", output)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("docker log missing: %v", err)
	}
}

func TestRunEnvResetPrintsUnderlyingCommands(t *testing.T) {
	repoRoot := filepath.Join(t.TempDir(), "client-site")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "theme", "style.css"), []byte("/* demo */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	project := map[string]any{
		"schema":    1,
		"project":   map[string]any{"slug": "client-site", "name": "Client Site", "type": "wordpress-theme"},
		"wordpress": map[string]any{"deploy_unit": "theme", "theme_slug": "theme", "theme_path": "theme"},
		"env":       map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".nf", "project.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	dockerDir := t.TempDir()
	logPath := filepath.Join(dockerDir, "docker-args.txt")
	dockerScript := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$DOCKER_LOG\"\ncase \"$*\" in\n  *\"wp core is-installed\"*) exit 1 ;;\nesac\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	t.Setenv("DOCKER_LOG", logPath)
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() {
		if got := Run([]string{"env", "reset"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, want := range []string{
		"> docker compose down -v --remove-orphans",
		"> docker compose up -d",
		"> docker compose run --rm cli wp core is-installed --allow-root",
		"> docker compose run --rm cli sh -lc",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output = %q, want %q", output, want)
		}
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("docker log missing: %v", err)
	}
}

func TestRunThemePackageUsesThemeStyleVersionWhenPresent(t *testing.T) {
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "theme", "style.css"), []byte("/*\nTheme Name: Demo\nVersion: 2.0.0\n*/\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "theme", "package.json"), []byte("{\n  \"version\": \"1.2.3\"\n}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	project := map[string]any{
		"schema":       1,
		"project":      map[string]any{"slug": "client", "name": "Client", "type": "wordpress-theme"},
		"wordpress":    map[string]any{"deploy_unit": "theme", "theme_slug": "theme", "theme_path": "theme"},
		"build":        map[string]any{"steps": []any{"composer install", "npm run build"}},
		"artifact":     map[string]any{"path": "release/client-v{version}.zip", "include": []any{"vendor/", "assets/dist/"}, "exclude": []any{"node_modules/", ".git/"}},
		"deploy":       map[string]any{"targets": map[string]any{}},
		"project_slug": "legacy-project",
		"project_name": "Legacy Project",
		"theme_slug":   "legacy-theme",
		"theme_source": "legacy-theme",
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".nf", "project.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() {
		if got := Run([]string{"theme", "package", "--dry-run"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Would package "+filepath.Join(workdir, "theme")+" -> "+filepath.Join(workdir, "release", "client-v2.0.0.zip")) {
		t.Fatalf("Run() output = %q, want style.css version to win over package.json", output)
	}
	for _, unwanted := range []string{"legacy-theme", "legacy-project"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("Run() output unexpectedly contained %q: %s", unwanted, output)
		}
	}
}

func TestRunThemePackageFallsBackToPackageVersionWhenStyleVersionMissing(t *testing.T) {
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "theme", "package.json"), []byte("{\n  \"version\": \"1.2.3\"\n}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	project := map[string]any{
		"schema":    1,
		"project":   map[string]any{"slug": "client", "name": "Client", "type": "wordpress-theme"},
		"wordpress": map[string]any{"deploy_unit": "theme", "theme_slug": "theme", "theme_path": "theme"},
		"artifact":  map[string]any{"path": "dist/client-v{version}.zip", "include": []any{"vendor/", "assets/dist/"}, "exclude": []any{"node_modules/", ".git/"}},
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".nf", "project.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() {
		if got := Run([]string{"theme", "package", "--dry-run"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "client-v1.2.3.zip") {
		t.Fatalf("Run() output = %q, want package.json fallback version", output)
	}
	if strings.Contains(output, "theme version not found") {
		t.Fatalf("Run() output = %q, did not expect missing version error", output)
	}
}

func TestRunThemePackageFailsWhenThemeVersionMissingFromStyleAndPackage(t *testing.T) {
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "theme", "style.css"), []byte("/* demo */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	project := map[string]any{
		"schema":    1,
		"project":   map[string]any{"slug": "client", "name": "Client", "type": "wordpress-theme"},
		"wordpress": map[string]any{"deploy_unit": "theme", "theme_slug": "theme", "theme_path": "theme"},
		"artifact":  map[string]any{"path": "dist/client-v{version}.zip", "include": []any{"vendor/", "assets/dist/"}, "exclude": []any{"node_modules/", ".git/"}},
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".nf", "project.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStderr(t, func() {
		if got := Run([]string{"theme", "package", "--dry-run"}); got != 1 {
			t.Fatalf("Run() = %d, want 1", got)
		}
	})
	if !strings.Contains(output, "theme version not found") {
		t.Fatalf("Run() stderr = %q, want missing version error", output)
	}
	for _, want := range []string{"style.css", "package.json"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() stderr = %q, want %q in error", output, want)
		}
	}
}

func TestRunRejectsThemeTasksOutsideGit(t *testing.T) {
	workdir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStderr(t, func() {
		if got := Run([]string{"theme", "tasks"}); got != 1 {
			t.Fatalf("Run() = %d, want 1", got)
		}
	})
	if !strings.Contains(output, ".git repository") {
		t.Fatalf("Run() stderr = %q, want .git repository message", output)
	}
}

func TestRunRemovedTopLevelEnvShortcutFails(t *testing.T) {
	workdir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStderr(t, func() {
		if got := Run([]string{"up"}); got != 1 {
			t.Fatalf("Run(up) = %d, want 1", got)
		}
	})
	if !strings.Contains(output, "unsupported command: up") {
		t.Fatalf("Run(up) stderr = %q, want unsupported command", output)
	}
}

func TestRunRemovedTopLevelShellShortcutFails(t *testing.T) {
	workdir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStderr(t, func() {
		if got := Run([]string{"shell"}); got != 1 {
			t.Fatalf("Run(shell) = %d, want 1", got)
		}
	})
	if !strings.Contains(output, "unsupported command: shell") {
		t.Fatalf("Run(shell) stderr = %q, want unsupported command", output)
	}
}

func TestRunEnvShowPrintsEnvInfo(t *testing.T) {
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	project := map[string]any{"schema": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "env": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".nf", "project.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() {
		if got := Run([]string{"env", "show"}); got != 0 {
			t.Fatalf("Run(env show) = %d, want 0", got)
		}
	})
	wpPort, mailpitPort := envDerivedPorts("client")
	for _, want := range []string{"Env:\n", "  project: client\n", "  compose project: nf_client_env\n", fmt.Sprintf("  WordPress: http://localhost:%d\n", wpPort), fmt.Sprintf("  Mailpit:   http://localhost:%d", mailpitPort)} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run(env show) output missing %q:\n%s", want, output)
		}
	}
}

func TestRunEnvShellExecutesWordpressShell(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)

	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "theme", "style.css"), []byte("/* demo */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	project := map[string]any{"schema": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"}, "env": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".nf", "project.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	binDir := t.TempDir()
	capturePath := filepath.Join(t.TempDir(), "docker-args.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAPTURE_FILE\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("CAPTURE_FILE", capturePath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() {
		if got := Run([]string{"env", "shell"}); got != 0 {
			t.Fatalf("Run(env shell) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "> docker compose exec wordpress sh") {
		t.Fatalf("Run(shell) stdout = %q, want compose exec preview", output)
	}
	args, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := strings.Split(strings.TrimSpace(string(args)), "\n"), []string{"compose", "exec", "wordpress", "sh"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("docker args = %#v, want %#v", got, want)
	}
}

func TestRunRejectsRemovedTopLevelCompatibilityRoutes(t *testing.T) {
	for _, argv := range [][]string{
		{"provision-server"},
		{"project", "help"},
		{"repo"},
		{"repo", "help"},
		{"repo", "init"},
		{"repo", "tasks"},
		{"repo", "package"},
		{"commands"},
		{"run", "build"},
		{"list", "servers"},
		{"show", "server", "app1"},
		{"delete", "server", "app1"},
		{"build"},
	} {
		argv := argv
		_ = captureStderr(t, func() {
			if got := Run(argv); got != 1 {
				t.Fatalf("Run(%v) = %d, want 1", argv, got)
			}
		})
	}
}

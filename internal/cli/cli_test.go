package cli

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/linode/linodego"
	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/state"
	"github.com/nonfiction/nf/internal/ui"
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
	server := map[string]any{"id": float64(98222343), "name": "test1", "provider": "linode", "hostname": "test1.nonfiction.dev"}
	if got := recordPickerValue("server", server); got != "test1" {
		t.Fatalf("recordPickerValue(server) = %q, want test1", got)
	}
	if got := recordPickerLabel("server", server); !strings.Contains(got, "id 98222343") || !strings.Contains(got, "test1.nonfiction.dev") {
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
	for _, wanted := range []string{"\n  init        initialize project metadata\n", "\n  provider    manage provider integrations\n", "\n  target      manage deployable targets\n", "\n  site        manage remote sites and envs\n", "\n  config      manage global config\n", "\n  password    derive passwords\n", "\n  completion  print shell completion scripts\n", "\n  help        show help\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runHelp() output missing %q:\n%s", wanted, output)
		}
	}
	for _, unwanted := range []string{"\n  remote  ", "\n  theme   ", "\n  env     ", "\n  repo  ", "\n  instance  ", "\n  server  ", "Shortcuts:", "nf up", "nf shell", "snapshot create", "\n  commands\n", "\n  run <name>\n", "\n  build\n"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("runHelp() output unexpectedly contained %q:\n%s", unwanted, output)
		}
	}
}

func TestRunHelpHidesProjectCommandsInsideGitWithoutNFDir(t *testing.T) {
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
	for _, wanted := range []string{"\n  init        initialize project metadata\n", "\n  provider    manage provider integrations\n", "\n  config      manage global config\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runHelp() output missing %q:\n%s", wanted, output)
		}
	}
	for _, unwanted := range []string{"\n  remote  ", "\n  theme   ", "\n  env     ", "\n  instance  ", "\n  server  ", "Shortcuts:", "nf up", "nf shell", "snapshot create", "\n  commands\n", "\n  run <name>\n", "\n  build\n"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("runHelp() output unexpectedly contained %q:\n%s", unwanted, output)
		}
	}
}

func TestRunHelpShowsProjectCommandsInsideNFProject(t *testing.T) {
	workdir := t.TempDir()
	for _, dir := range []string{".git"} {
		if err := os.Mkdir(filepath.Join(workdir, dir), 0o755); err != nil {
			t.Fatalf("Mkdir(%s) error = %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(workdir, "nf.json"), []byte("{\n  \"version\": 1\n}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
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
	for _, wanted := range []string{"\n  remote      manage repo remotes\n", "\n  env         manage the local development env\n", "\n  theme       package files and run theme tasks\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runHelp() output missing %q:\n%s", wanted, output)
		}
	}
}

func TestRunInitHelpShowsFlags(t *testing.T) {
	output := captureStdout(t, func() { _ = runInitHelp() })
	for _, want := range []string{"init\n\nUsage:\n", "nf init [flags]", "--project-slug string", "--theme-slug string", "--theme-source string", "--type string", "--force"} {
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
	for _, wanted := range []string{"provider\n\nCommands:\n", "\n  list, ls                   list provider integrations\n", "\n  check [provider] [--json]  run provider healthcheck\n", "\n  show [provider] [--json]   show cached provider metadata\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runProviderHelp() output missing %q:\n%s", wanted, output)
		}
	}
}

func TestRunTargetHelpShowsRefresh(t *testing.T) {
	output := captureStdout(t, func() { _ = runTargetHelp() })
	for _, wanted := range []string{"target\n\nCommands:\n", "\n  list, ls                   list deployable targets\n", "\n  refresh                    refresh targets from providers\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runTargetHelp() output missing %q:\n%s", wanted, output)
		}
	}
}

func TestRunCompletionPrintsBashAndZshScripts(t *testing.T) {
	bashOutput := captureStdout(t, func() {
		if got := Run([]string{"completion", "bash"}); got != 0 {
			t.Fatalf("Run(completion bash) = %d, want 0", got)
		}
	})
	for _, want := range []string{"# bash completion for nf", "complete -F _nf_completion nf", "command -v nf", "__complete --"} {
		if !strings.Contains(bashOutput, want) {
			t.Fatalf("bash completion output missing %q:\n%s", want, bashOutput)
		}
	}

	zshOutput := captureStdout(t, func() {
		if got := Run([]string{"completion", "zsh"}); got != 0 {
			t.Fatalf("Run(completion zsh) = %d, want 0", got)
		}
	})
	for _, want := range []string{"#compdef nf", "compctl -d nf", "compdef _nf nf", "command -v nf", "__complete --", "compadd -Q -U -S ' '"} {
		if !strings.Contains(zshOutput, want) {
			t.Fatalf("zsh completion output missing %q:\n%s", want, zshOutput)
		}
	}
}

func TestRunCompleteSuggestsStaticAndCachedValues(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("providers", []map[string]any{{
		"provider": "linode",
		"targets":  []map[string]any{{"id": "123", "name": "app1-linode", "provider": "linode", "hostname": "app1.example.com"}},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{
		"_state_key": "client-app1-linode",
		"site_id":    "client-app1-linode",
		"site":       "client",
		"env":        "live",
		"target":     "app1-linode",
	}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	rootOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "pr"}); got != 0 {
			t.Fatalf("Run(__complete root) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(rootOutput) != "provider" {
		t.Fatalf("root completion = %q, want provider", rootOutput)
	}

	providerOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "provider", "show", ""}); got != 0 {
			t.Fatalf("Run(__complete provider show) = %d, want 0", got)
		}
	})
	for _, want := range []string{"dnsimple\n", "kinsta\n", "linode\n"} {
		if !strings.Contains(providerOutput, want) {
			t.Fatalf("provider completion missing %q:\n%s", want, providerOutput)
		}
	}
	if strings.Contains(providerOutput, "--json") {
		t.Fatalf("provider completion included --json:\n%s", providerOutput)
	}

	targetOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "target", "show", "app"}); got != 0 {
			t.Fatalf("Run(__complete target show) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(targetOutput) != "app1-linode" {
		t.Fatalf("target completion = %q, want app1-linode only", targetOutput)
	}

	siteOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "show", "client"}); got != 0 {
			t.Fatalf("Run(__complete site show) = %d, want 0", got)
		}
	})
	if !strings.Contains(siteOutput, "client-app1-linode\n") {
		t.Fatalf("site completion missing cached site:\n%s", siteOutput)
	}
	if !strings.Contains(siteOutput, "client-app1-linode:live\n") {
		t.Fatalf("site completion missing cached env:\n%s", siteOutput)
	}
	if strings.Contains(siteOutput, "--json") || strings.Contains(siteOutput, "app1-linode.nonfiction.dev") {
		t.Fatalf("site completion included flags or aliases:\n%s", siteOutput)
	}

	siteShellOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "shell", "client"}); got != 0 {
			t.Fatalf("Run(__complete site shell) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(siteShellOutput) != "client-app1-linode:live" {
		t.Fatalf("site shell completion = %q, want env id only", siteShellOutput)
	}

	sitePasswordOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "password", "client"}); got != 0 {
			t.Fatalf("Run(__complete site password) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(sitePasswordOutput) != "client-app1-linode" {
		t.Fatalf("site password completion = %q, want site id only", sitePasswordOutput)
	}

	remoteAddOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "remote", "add", "production", "client"}); got != 0 {
			t.Fatalf("Run(__complete remote add) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(remoteAddOutput) != "client-app1-linode:live" {
		t.Fatalf("remote add completion = %q, want env id only", remoteAddOutput)
	}

	siteSnapshotOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "snapshot", "client"}); got != 0 {
			t.Fatalf("Run(__complete site snapshot) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(siteSnapshotOutput) != "client-app1-linode:live" {
		t.Fatalf("site snapshot completion = %q, want env id only", siteSnapshotOutput)
	}
	siteSnapshotListOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "snapshot", "l"}); got != 0 {
			t.Fatalf("Run(__complete site snapshot list) = %d, want 0", got)
		}
	})
	if !strings.Contains(siteSnapshotListOutput, "list\n") {
		t.Fatalf("site snapshot list completion = %q, want list", siteSnapshotListOutput)
	}
	dataHome := t.TempDir()
	t.Setenv("NF_DATA_HOME", dataHome)
	writeTestRemoteSnapshot(t, "client-kinsta.live-2026-06-04-120000", "client-kinsta:live", "2026-06-04T12:00:00Z", 10, 20)
	siteSnapshotRemoveOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "snapshot", "remove", "client"}); got != 0 {
			t.Fatalf("Run(__complete site snapshot remove) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(siteSnapshotRemoveOutput) != "client-kinsta.live-2026-06-04-120000" {
		t.Fatalf("site snapshot remove completion = %q, want snapshot name", siteSnapshotRemoveOutput)
	}
	envSnapshotImportOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "env", "snapshot", "import", "client"}); got != 0 {
			t.Fatalf("Run(__complete env snapshot import) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(envSnapshotImportOutput) != "client-kinsta.live-2026-06-04-120000" {
		t.Fatalf("env snapshot import completion = %q, want remote snapshot name", envSnapshotImportOutput)
	}
	siteSnapshotPruneOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "snapshot", "prune", "--"}); got != 0 {
			t.Fatalf("Run(__complete site snapshot prune) = %d, want 0", got)
		}
	})
	for _, want := range []string{"--keep\n", "--dry-run\n", "--yes\n"} {
		if !strings.Contains(siteSnapshotPruneOutput, want) {
			t.Fatalf("site snapshot prune completion missing %q:\n%s", want, siteSnapshotPruneOutput)
		}
	}

	snapshotOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "env", "snapshot", "pr"}); got != 0 {
			t.Fatalf("Run(__complete env snapshot) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(snapshotOutput) != "prune" {
		t.Fatalf("env snapshot completion = %q, want prune", snapshotOutput)
	}

	pruneOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "env", "snapshot", "prune", "--"}); got != 0 {
			t.Fatalf("Run(__complete env snapshot prune) = %d, want 0", got)
		}
	})
	for _, want := range []string{"--keep\n", "--dry-run\n", "--yes\n"} {
		if !strings.Contains(pruneOutput, want) {
			t.Fatalf("env snapshot prune completion missing %q:\n%s", want, pruneOutput)
		}
	}

	useOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "env", "snapshot", "use", "--"}); got != 0 {
			t.Fatalf("Run(__complete env snapshot use --) = %d, want 0", got)
		}
	})
	for _, want := range []string{"--yes\n", "--remote\n", "--name\n"} {
		if !strings.Contains(useOutput, want) {
			t.Fatalf("env snapshot use completion missing %q:\n%s", want, useOutput)
		}
	}
	useRemoteOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "env", "snapshot", "use", "client"}); got != 0 {
			t.Fatalf("Run(__complete env snapshot use client) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(useRemoteOutput) != "client-kinsta.live-2026-06-04-120000" {
		t.Fatalf("env snapshot use remote completion = %q, want remote snapshot name", useRemoteOutput)
	}
}

func TestRunCompleteSuggestsProjectValues(t *testing.T) {
	workdir := t.TempDir()
	for _, dir := range []string{".git"} {
		if err := os.Mkdir(filepath.Join(workdir, dir), 0o755); err != nil {
			t.Fatalf("Mkdir(%s) error = %v", dir, err)
		}
	}
	project := map[string]any{
		"version":   1,
		"project":   map[string]any{"slug": "client"},
		"wordpress": map[string]any{"plugins": []any{"stream"}},
		"remotes":   map[string]any{"production": "client-app1-linode:live"},
		"tasks":     map[string]any{"build": map[string]any{"description": "Build assets", "run": "npm run build"}},
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(project) error = %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	remoteOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "remote", "show", "pro"}); got != 0 {
			t.Fatalf("Run(__complete remote show) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(remoteOutput) != "production" {
		t.Fatalf("remote completion = %q, want production", remoteOutput)
	}

	themeOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "theme", "b"}); got != 0 {
			t.Fatalf("Run(__complete theme) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(themeOutput) != "build" {
		t.Fatalf("theme completion = %q, want build", themeOutput)
	}

	themeCommandOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "theme", "d"}); got != 0 {
			t.Fatalf("Run(__complete theme d) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(themeCommandOutput) != "deploy" {
		t.Fatalf("theme command completion = %q, want deploy", themeCommandOutput)
	}

	themeDeployOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "theme", "deploy", "pro"}); got != 0 {
			t.Fatalf("Run(__complete theme deploy pro) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(themeDeployOutput) != "production" {
		t.Fatalf("theme deploy completion = %q, want production", themeDeployOutput)
	}

	themeRollbackOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "theme", "rollback", "pro"}); got != 0 {
			t.Fatalf("Run(__complete theme rollback pro) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(themeRollbackOutput) != "production" {
		t.Fatalf("theme rollback completion = %q, want production", themeRollbackOutput)
	}

	themeDeployAllOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "theme", "deploy", ""}); got != 0 {
			t.Fatalf("Run(__complete theme deploy) = %d, want 0", got)
		}
	})
	if got, want := strings.TrimSpace(themeDeployAllOutput), "production"; got != want {
		t.Fatalf("theme deploy completion order = %q, want %q", got, want)
	}

	envPluginsOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "env", "pl"}); got != 0 {
			t.Fatalf("Run(__complete env pl) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(envPluginsOutput) != "plugins" {
		t.Fatalf("env plugins completion = %q, want plugins", envPluginsOutput)
	}

	envPluginsCommandOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "env", "plugins", ""}); got != 0 {
			t.Fatalf("Run(__complete env plugins) = %d, want 0", got)
		}
	})
	for _, want := range []string{"list\n", "ls\n", "add\n", "remove\n", "rm\n", "status\n", "diff\n", "install\n", "help\n"} {
		if !strings.Contains(envPluginsCommandOutput, want) {
			t.Fatalf("env plugins command completion missing %q:\n%s", want, envPluginsCommandOutput)
		}
	}

	envPluginsAddOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "env", "plugins", "add", ""}); got != 0 {
			t.Fatalf("Run(__complete env plugins add) = %d, want 0", got)
		}
	})
	for _, want := range []string{"--source\n", "--no-activate\n", "--no-auto-update\n"} {
		if !strings.Contains(envPluginsAddOutput, want) {
			t.Fatalf("env plugins add completion missing %q:\n%s", want, envPluginsAddOutput)
		}
	}

	envPluginsRemoveOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "env", "plugins", "remove", ""}); got != 0 {
			t.Fatalf("Run(__complete env plugins remove) = %d, want 0", got)
		}
	})
	if !strings.Contains(envPluginsRemoveOutput, "stream\n") {
		t.Fatalf("env plugins remove completion missing stream:\n%s", envPluginsRemoveOutput)
	}

	envPluginsStatusOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "env", "plugins", "status", ""}); got != 0 {
			t.Fatalf("Run(__complete env plugins status) = %d, want 0", got)
		}
	})
	if !strings.Contains(envPluginsStatusOutput, "production\n") {
		t.Fatalf("env plugins status completion missing production:\n%s", envPluginsStatusOutput)
	}

	envPluginsDiffOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "env", "plugins", "diff", ""}); got != 0 {
			t.Fatalf("Run(__complete env plugins diff) = %d, want 0", got)
		}
	})
	if !strings.Contains(envPluginsDiffOutput, "production\n") {
		t.Fatalf("env plugins diff completion missing production:\n%s", envPluginsDiffOutput)
	}

	envPluginsInstallOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "env", "plugins", "install", ""}); got != 0 {
			t.Fatalf("Run(__complete env plugins install) = %d, want 0", got)
		}
	})
	for _, want := range []string{"production\n", "--dry-run\n", "--yes\n"} {
		if !strings.Contains(envPluginsInstallOutput, want) {
			t.Fatalf("env plugins install completion missing %q:\n%s", want, envPluginsInstallOutput)
		}
	}
}

func TestRunProviderListShowsProviders(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("DNSIMPLE_TOKEN", "")
	t.Setenv("KINSTA_API_KEY", "")
	t.Setenv("LINODE_TOKEN", "")
	t.Setenv("LINODE_CLI_TOKEN", "")

	output := captureStdout(t, func() {
		if got := Run([]string{"provider", "list"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, want := range []string{"provider", "status", "missing", "dnsimple", "base_domain", "DNSIMPLE_TOKEN", "kinsta", "KINSTA_API_KEY", "linode", "LINODE_TOKEN or LINODE_CLI_TOKEN"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, output)
		}
	}
}

func TestInitGlobalConfigPromptsForMissingSettings(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)

	oldPromptString := configPromptString
	oldIsInteractive := configIsInteractive
	t.Cleanup(func() {
		configPromptString = oldPromptString
		configIsInteractive = oldIsInteractive
	})

	answers := map[string]string{
		"Base domain: ":                "nonfiction.dev",
		"Default WordPress email: ":    "web@nonfiction.ca",
		"Default WordPress user: ":     "admin",
		"Kinsta default PHP version: ": "8.3",
		"Linode default region: ":      "ca-central",
		"Linode default SSH user: ":    "nonfiction",
		"Linode default type: ":        "g6-standard-1",
	}
	configIsInteractive = func() bool { return true }
	configPromptString = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		value, ok := answers[prompt]
		if !ok {
			t.Fatalf("unexpected prompt %q", prompt)
		}
		return value, nil
	}

	if err := initGlobalConfig(configInitSettings(), false); err != nil {
		t.Fatalf("initGlobalConfig() error = %v", err)
	}
	values, err := loadGlobalConfig()
	if err != nil {
		t.Fatalf("loadGlobalConfig() error = %v", err)
	}
	for key, want := range map[string]string{
		"base_domain":           "nonfiction.dev",
		"default_wp_email":      "web@nonfiction.ca",
		"default_wp_user":       "admin",
		"kinsta_default_php":    "8.3",
		"linode_default_region": "ca-central",
		"linode_default_user":   "nonfiction",
		"linode_default_type":   "g6-standard-1",
	} {
		if got := values[key]; got != want {
			t.Fatalf("config[%s] = %q, want %q", key, got, want)
		}
	}
}

func TestInitGlobalConfigPreservesExistingSettings(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	if err := saveGlobalConfig(map[string]string{"base_domain": "example.com"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}

	oldPromptString := configPromptString
	oldIsInteractive := configIsInteractive
	t.Cleanup(func() {
		configPromptString = oldPromptString
		configIsInteractive = oldIsInteractive
	})
	configIsInteractive = func() bool { return true }
	configPromptString = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		if prompt == "Base domain: " {
			t.Fatalf("base_domain should not be prompted when already set")
		}
		return "value", nil
	}

	if err := initGlobalConfig(configInitSettings(), false); err != nil {
		t.Fatalf("initGlobalConfig() error = %v", err)
	}
	values, err := loadGlobalConfig()
	if err != nil {
		t.Fatalf("loadGlobalConfig() error = %v", err)
	}
	if got := values["base_domain"]; got != "example.com" {
		t.Fatalf("base_domain = %q, want existing value", got)
	}
}

func TestRunConfigSetKinstaDefaultPHP(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)

	output := captureStdout(t, func() {
		if got := Run([]string{"config", "set-kinsta-default-php", "8.3"}); got != 0 {
			t.Fatalf("Run(config set-kinsta-default-php) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Set kinsta_default_php") {
		t.Fatalf("output = %q, want set message", output)
	}
	values, err := loadGlobalConfig()
	if err != nil {
		t.Fatalf("loadGlobalConfig() error = %v", err)
	}
	if got := values["kinsta_default_php"]; got != "8.3" {
		t.Fatalf("kinsta_default_php = %q, want 8.3", got)
	}
}

func TestRunProviderShowReadsCachedMetadata(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("DNSIMPLE_TOKEN", "dnsimple-token-secret")
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{
		"provider":      "dnsimple",
		"account_id":    "14",
		"account_email": "hello@example.com",
		"targets":       []map[string]any{},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"provider", "show", "dnsimple"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, want := range []string{"Provider: dnsimple", "Status: configured", filepath.Join(stateDir, "providers.json"), "Account ID: 14", "Account email: hello@example.com", "Targets: 0"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, output)
		}
	}
	for _, unwanted := range []string{"dnsimple-token-secret", "DNSIMPLE_TOKEN", `"provider": "dnsimple"`} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("Run() output included %q:\n%s", unwanted, output)
		}
	}
}

func TestRunProviderShowJSONReadsCachedMetadata(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("providers", []map[string]any{{
		"provider":   "linode",
		"username":   "nf-user",
		"restricted": false,
		"targets":    []map[string]any{},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"provider", "show", "linode", "--json"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	if !strings.Contains(output, `"provider": "linode"`) || !strings.Contains(output, `"username": "nf-user"`) {
		t.Fatalf("Run() JSON output missing cached fields:\n%s", output)
	}
	if strings.Contains(output, "Provider: linode") || strings.Contains(output, "Cache:") {
		t.Fatalf("Run() JSON output included human text:\n%s", output)
	}
}

func TestRunProviderShowWithoutProviderPromptsPicker(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("providers", []map[string]any{{
		"provider": "kinsta",
		"company":  "company-123",
		"status":   "active",
		"targets":  []map[string]any{{"name": "kinsta", "status": "active"}},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	oldSelect := providerSelectFn
	var selectTitle string
	var selectOptions []ui.SelectOption
	providerSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectTitle = title
		selectOptions = append([]ui.SelectOption(nil), options...)
		return "kinsta", nil
	}
	t.Cleanup(func() { providerSelectFn = oldSelect })

	output := captureStdout(t, func() {
		if got := Run([]string{"provider", "show"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	if selectTitle != "Choose a provider to show" {
		t.Fatalf("select title = %q", selectTitle)
	}
	if len(selectOptions) != 3 || selectOptions[0] != (ui.SelectOption{Value: "dnsimple", Label: "dnsimple"}) || selectOptions[2] != (ui.SelectOption{Value: "linode", Label: "linode"}) {
		t.Fatalf("select options = %#v", selectOptions)
	}
	for _, want := range []string{"Provider: kinsta", "Company ID: company-123", "Provider status: active", "Targets: 1", "kinsta (active)"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, output)
		}
	}
}

func TestRunProviderShowRequiresCachedMetadata(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)

	stderr := captureStderr(t, func() {
		if got := Run([]string{"provider", "show", "linode"}); got != 1 {
			t.Fatalf("Run() = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, `No cached provider metadata matched "linode"`) || !strings.Contains(stderr, "Run nf provider check linode") {
		t.Fatalf("Run() stderr = %q", stderr)
	}
}

func TestProviderValueLabelMasksConfiguredValues(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	t.Setenv("DNSIMPLE_TOKEN", "dnsimple-token-secret")
	status, ok := providerConfigStatusByName("dnsimple")
	if !ok {
		t.Fatal("providerConfigStatusByName(dnsimple) missing")
	}
	var secretGroup providerConfigKey
	for _, group := range status.Keys {
		if group.Secret {
			secretGroup = group
			break
		}
	}
	if len(secretGroup.Keys) == 0 {
		t.Fatal("dnsimple provider has no secret config group")
	}
	got := providerValueLabel(status, secretGroup)
	if got != "dns***********" {
		t.Fatalf("providerValueLabel() = %q, want masked secret", got)
	}
	if strings.Contains(got, "dnsimple-token-secret") {
		t.Fatalf("providerValueLabel() leaked secret: %s", got)
	}
}

func TestRunProviderCheckRunsHealthcheckAndSavesMetadata(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("LINODE_TOKEN", "")
	t.Setenv("LINODE_CLI_TOKEN", "linode-token-secret")
	oldCheck := providerCheckLinodeFn
	providerCheckLinodeFn = func() (providerHealthResult, error) {
		return providerHealthResult{
			Provider: "linode",
			Details:  map[string]string{"username": "nf-user", "restricted": "false"},
			Record: map[string]any{
				"provider":   "linode",
				"username":   "nf-user",
				"restricted": false,
				"targets": []map[string]any{{
					"id":       "98222343",
					"name":     "app1-linode",
					"provider": "linode",
				}},
			},
		}, nil
	}
	t.Cleanup(func() { providerCheckLinodeFn = oldCheck })

	output := captureStdout(t, func() {
		if got := Run([]string{"provider", "check", "linode"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, want := range []string{"Provider linode healthcheck passed.", "username: nf-user", "restricted: false", "Saved provider metadata"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, output)
		}
	}
	records, err := state.LoadStateRecords("providers")
	if err != nil {
		t.Fatalf("LoadStateRecords(providers) error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("providers records = %d, want 1", len(records))
	}
	if got := records[0]["username"]; got != "nf-user" {
		t.Fatalf("provider username = %q, want nf-user", got)
	}
	targets, ok := records[0]["targets"].([]any)
	if !ok || len(targets) != 1 {
		t.Fatalf("provider targets = %#v, want one target", records[0]["targets"])
	}
}

func TestRunProviderCheckWithoutProviderPromptsPickerAndJSON(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("KINSTA_API_KEY", "kinsta-token-secret")
	oldSelect := providerSelectFn
	oldCheck := providerCheckKinstaFn
	var selectTitle string
	providerSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectTitle = title
		return "kinsta", nil
	}
	providerCheckKinstaFn = func() (providerHealthResult, error) {
		return providerHealthResult{
			Provider: "kinsta",
			Details:  map[string]string{"status": "active"},
			Record: map[string]any{
				"provider": "kinsta",
				"company":  "company-123",
				"status":   "active",
				"targets":  []map[string]any{{"name": "kinsta", "status": "active"}},
			},
		}, nil
	}
	t.Cleanup(func() {
		providerSelectFn = oldSelect
		providerCheckKinstaFn = oldCheck
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"provider", "check", "--json"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	if selectTitle != "Choose a provider to check" {
		t.Fatalf("select title = %q", selectTitle)
	}
	for _, want := range []string{`"provider": "kinsta"`, `"company": "company-123"`, `"checked_at":`, `"targets":`} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() JSON output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "healthcheck passed") || strings.Contains(output, "Saved provider metadata") {
		t.Fatalf("Run() JSON output included human text:\n%s", output)
	}
}

func TestLinodeInstanceTargetRecordDerivesHostnameAndSSH(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev", "linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	instance := linodego.Instance{
		ID:     98222343,
		Label:  "app1-linode",
		Region: "ca-central",
		Status: linodego.InstanceRunning,
		IPv4:   []*net.IP{ptrTo(net.ParseIP("198.51.100.10"))},
		Tags:   []string{"nf"},
	}

	record := linodeInstanceTargetRecord(instance)
	if got, want := recordValueString(record["hostname"]), "app1-linode.nonfiction.dev"; got != want {
		t.Fatalf("hostname = %q, want %q", got, want)
	}
	if got, want := serverSSHHost(record), "app1-linode.nonfiction.dev"; got != want {
		t.Fatalf("serverSSHHost() = %q, want %q", got, want)
	}
	if got, want := serverSSHUser(record), "nonfiction"; got != want {
		t.Fatalf("serverSSHUser() = %q, want %q", got, want)
	}
	if got, want := recordValueString(record["ipv4"]), "198.51.100.10"; got != want {
		t.Fatalf("ipv4 = %q, want %q", got, want)
	}
	if got, want := recordValueString(record["status"]), "running"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
}

func ptrTo[T any](value T) *T {
	return &value
}

func TestCheckKinstaProviderSetsTargetStatusFromAPIValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/validate" {
			t.Fatalf("request path = %q, want /validate", r.URL.Path)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer kinsta-token-secret"; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
		_, _ = io.WriteString(w, `{"name":"nf","company":"company-123","status":"active"}`)
	}))
	t.Cleanup(server.Close)
	t.Setenv("KINSTA_BASE_URL", server.URL)
	t.Setenv("KINSTA_API_KEY", "kinsta-token-secret")

	result, err := checkKinstaProvider()
	if err != nil {
		t.Fatalf("checkKinstaProvider() error = %v", err)
	}
	targets := targetMaps(result.Record["targets"])
	if len(targets) != 1 {
		t.Fatalf("targets len = %d, want 1", len(targets))
	}
	if got, want := recordValueString(targets[0]["status"]), "active"; got != want {
		t.Fatalf("target status = %q, want %q", got, want)
	}
}

func TestCheckProvidersAfterConfigInitPopulatesTargets(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	t.Setenv("DNSIMPLE_TOKEN", "dnsimple-token-secret")
	t.Setenv("KINSTA_API_KEY", "kinsta-token-secret")
	t.Setenv("LINODE_TOKEN", "linode-token-secret")

	oldDNSimple := providerCheckDNSimpleFn
	oldKinsta := providerCheckKinstaFn
	oldLinode := providerCheckLinodeFn
	t.Cleanup(func() {
		providerCheckDNSimpleFn = oldDNSimple
		providerCheckKinstaFn = oldKinsta
		providerCheckLinodeFn = oldLinode
	})
	providerCheckDNSimpleFn = func() (providerHealthResult, error) {
		return providerHealthResult{Provider: "dnsimple", Details: map[string]string{"zone_active": "true"}, Record: map[string]any{"provider": "dnsimple", "targets": []map[string]any{}}}, nil
	}
	providerCheckKinstaFn = func() (providerHealthResult, error) {
		return providerHealthResult{Provider: "kinsta", Details: map[string]string{"status": "active"}, Record: map[string]any{"provider": "kinsta", "targets": []map[string]any{{"id": "kinsta", "name": "kinsta", "provider": "kinsta"}}}}, nil
	}
	providerCheckLinodeFn = func() (providerHealthResult, error) {
		return providerHealthResult{Provider: "linode", Details: map[string]string{"targets": "1"}, Record: map[string]any{"provider": "linode", "targets": []map[string]any{{"id": "98222343", "name": "app1-linode", "provider": "linode", "status": "running"}}}}, nil
	}

	output := captureStdout(t, func() {
		if err := checkProvidersAfterConfigInit(); err != nil {
			t.Fatalf("checkProvidersAfterConfigInit() error = %v", err)
		}
	})
	for _, want := range []string{"Checking providers...", "Provider dnsimple healthcheck passed.", "Provider kinsta healthcheck passed.", "Provider linode healthcheck passed."} {
		if !strings.Contains(output, want) {
			t.Fatalf("checkProvidersAfterConfigInit() output missing %q:\n%s", want, output)
		}
	}
	targets, err := cachedTargets()
	if err != nil {
		t.Fatalf("cachedTargets() error = %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("cachedTargets() len = %d, want 2: %#v", len(targets), targets)
	}
	for _, want := range []string{"kinsta", "app1-linode"} {
		found := false
		for _, target := range targets {
			if recordValueString(target["name"]) == want || recordValueString(target["id"]) == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("cachedTargets() missing %q: %#v", want, targets)
		}
	}
}

func TestRunTargetRefreshUpdatesProviderTargets(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("KINSTA_API_KEY", "kinsta-token-secret")
	t.Setenv("LINODE_TOKEN", "linode-token-secret")

	oldKinsta := providerCheckKinstaFn
	oldLinode := providerCheckLinodeFn
	t.Cleanup(func() {
		providerCheckKinstaFn = oldKinsta
		providerCheckLinodeFn = oldLinode
	})
	providerCheckKinstaFn = func() (providerHealthResult, error) {
		return providerHealthResult{Provider: "kinsta", Record: map[string]any{"provider": "kinsta", "targets": []map[string]any{{"id": "kinsta", "name": "kinsta", "provider": "kinsta", "status": "active"}}}}, nil
	}
	providerCheckLinodeFn = func() (providerHealthResult, error) {
		return providerHealthResult{Provider: "linode", Record: map[string]any{"provider": "linode", "targets": []map[string]any{{"id": "98222344", "name": "app2-linode", "provider": "linode", "status": "running"}}}}, nil
	}
	providers := []map[string]any{{
		"provider": "linode",
		"targets": []map[string]any{{
			"id":       "98222343",
			"name":     "app1-linode",
			"provider": "linode",
			"status":   "running",
		}},
	}}
	data, err := json.MarshalIndent(providers, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "providers.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"target", "refresh"}); got != 0 {
			t.Fatalf("Run(target refresh) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Target refresh updates target metadata from configured providers.", "Provider kinsta refreshed. Targets: 1", "Provider linode refreshed. Targets: 1", "Refreshed providers: 2", "Targets: 2"} {
		if !strings.Contains(output, want) {
			t.Fatalf("target refresh output missing %q:\n%s", want, output)
		}
	}
	targets, err := cachedTargets()
	if err != nil {
		t.Fatalf("cachedTargets() error = %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("cachedTargets() len = %d, want 2: %#v", len(targets), targets)
	}
	for _, unwanted := range []string{"app1-linode", "98222343"} {
		for _, target := range targets {
			if recordValueString(target["name"]) == unwanted || recordValueString(target["id"]) == unwanted {
				t.Fatalf("cachedTargets() included stale target %q: %#v", unwanted, targets)
			}
		}
	}
	for _, want := range []string{"kinsta", "app2-linode"} {
		found := false
		for _, target := range targets {
			if recordValueString(target["name"]) == want || recordValueString(target["id"]) == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("cachedTargets() missing %q: %#v", want, targets)
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

func TestRunProviderCheckReportsHealthcheckFailure(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	t.Setenv("DNSIMPLE_TOKEN", "dnsimple-token-secret")
	oldCheck := providerCheckDNSimpleFn
	providerCheckDNSimpleFn = func() (providerHealthResult, error) {
		return providerHealthResult{}, fmt.Errorf("dnsimple unavailable")
	}
	t.Cleanup(func() { providerCheckDNSimpleFn = oldCheck })

	stderr := captureStderr(t, func() {
		stdout := captureStdout(t, func() {
			if got := Run([]string{"provider", "check", "dnsimple"}); got != 1 {
				t.Fatalf("Run() = %d, want 1", got)
			}
		})
		if !strings.Contains(stdout, "Provider dnsimple healthcheck failed.") {
			t.Fatalf("Run() stdout missing healthcheck failure:\n%s", stdout)
		}
	})
	if !strings.Contains(stderr, "dnsimple unavailable") {
		t.Fatalf("Run() stderr = %q", stderr)
	}
}

func TestCheckDNSimpleProviderValidatesManagedDomain(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	requests := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		switch r.URL.Path {
		case "/v2/whoami":
			_, _ = io.WriteString(w, `{"data":{"account":{"id":14,"email":"hello@example.com","name":"Example"}}}`)
		case "/v2/14/zones/nonfiction.dev":
			_, _ = io.WriteString(w, `{"data":{"id":123,"account_id":14,"name":"nonfiction.dev","active":true}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("DNSIMPLE_BASE_URL", server.URL)
	t.Setenv("DNSIMPLE_TOKEN", "dnsimple-token-secret")

	result, err := checkDNSimpleProvider()
	if err != nil {
		t.Fatalf("checkDNSimpleProvider() error = %v", err)
	}
	if result.Record["managed_domain"] != "nonfiction.dev" || result.Record["zone_id"] != "123" {
		t.Fatalf("checkDNSimpleProvider() record = %#v", result.Record)
	}
	if got := strings.Join(requests, ","); got != "/v2/whoami,/v2/14/zones/nonfiction.dev" {
		t.Fatalf("requests = %q", got)
	}
	values, err := loadGlobalConfig()
	if err != nil {
		t.Fatalf("loadGlobalConfig() error = %v", err)
	}
	if got, want := values["dnsimple_account_id"], "14"; got != want {
		t.Fatalf("dnsimple_account_id = %q, want %q", got, want)
	}
}

func TestRunTargetListAndShowUseStateTargets(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	oldKinsta := providerCheckKinstaFn
	providerCheckKinstaFn = func() (providerHealthResult, error) {
		return providerHealthResult{Provider: "kinsta", Record: map[string]any{"provider": "kinsta", "targets": []map[string]any{{"id": "kinsta", "provider": "kinsta", "status": "active"}}}}, nil
	}
	t.Cleanup(func() { providerCheckKinstaFn = oldKinsta })
	providers := []map[string]any{
		{
			"provider":   "kinsta",
			"company_id": "company-123",
			"targets": []map[string]any{{
				"id":         "kinsta",
				"name":       "kinsta",
				"provider":   "kinsta",
				"company_id": "company-123",
				"status":     "active",
			}},
		},
		{
			"provider": "linode",
			"username": "nf-test",
			"targets": []map[string]any{{
				"id":       98222343,
				"name":     "app1-linode",
				"provider": "linode",
				"ipv4":     "203.0.113.10",
				"status":   "active",
			}},
		},
	}
	data, err := json.MarshalIndent(providers, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "providers.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	listOutput := captureStdout(t, func() {
		if got := Run([]string{"target", "list"}); got != 0 {
			t.Fatalf("Run(target list) = %d, want 0", got)
		}
	})
	for _, want := range []string{"target", "kinsta", "app1-linode", "linode", "203.0.113.10", "active"} {
		if !strings.Contains(listOutput, want) {
			t.Fatalf("target list output missing %q:\n%s", want, listOutput)
		}
	}
	if strings.Contains(listOutput, "ssh host") {
		t.Fatalf("target list output included removed ssh host column:\n%s", listOutput)
	}

	showOutput := captureStdout(t, func() {
		if got := Run([]string{"target", "show", "app1-linode"}); got != 0 {
			t.Fatalf("Run(target show) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Target: app1-linode", "Provider: linode", "Hostname: 203.0.113.10", "Status: active", "Cached status: active"} {
		if !strings.Contains(showOutput, want) {
			t.Fatalf("target show output missing %q:\n%s", want, showOutput)
		}
	}
	jsonOutput := captureStdout(t, func() {
		if got := Run([]string{"target", "show", "app1-linode", "--json"}); got != 0 {
			t.Fatalf("Run(target show --json) = %d, want 0", got)
		}
	})
	for _, want := range []string{`"name": "app1-linode"`, `"provider": "linode"`, `"ipv4": "203.0.113.10"`} {
		if !strings.Contains(jsonOutput, want) {
			t.Fatalf("target show --json output missing %q:\n%s", want, jsonOutput)
		}
	}
}

func TestRunTargetListShowsLiveLinodeSSHStatus(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	providers := []map[string]any{{
		"provider": "linode",
		"targets": []map[string]any{{
			"name":     "app1-linode",
			"provider": "linode",
			"hostname": "app1-linode.nonfiction.dev",
			"status":   "running",
		}},
	}}
	data, err := json.MarshalIndent(providers, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "providers.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	oldSSH := targetSSHReachableFn
	targetSSHReachableFn = func(record map[string]any) bool {
		return recordValueString(record["name"]) == "app1-linode"
	}
	t.Cleanup(func() { targetSSHReachableFn = oldSSH })

	output := captureStdout(t, func() {
		if got := Run([]string{"target", "list"}); got != 0 {
			t.Fatalf("Run(target list) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "reachable") || strings.Contains(output, "running") {
		t.Fatalf("target list output = %q, want live reachable status", output)
	}
}

func TestRunTargetShowWithoutTargetPromptsPicker(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	providers := []map[string]any{{
		"provider": "linode",
		"targets": []map[string]any{{
			"id":       "98222343",
			"name":     "app1-linode",
			"provider": "linode",
			"hostname": "app1-linode.nonfiction.dev",
		}},
	}}
	data, err := json.MarshalIndent(providers, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "providers.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	oldSelect := targetSelectFn
	oldSSH := targetSSHReachableFn
	var selectTitle string
	var selectOptions []ui.SelectOption
	targetSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectTitle = title
		selectOptions = append([]ui.SelectOption(nil), options...)
		return "app1-linode", nil
	}
	targetSSHReachableFn = func(record map[string]any) bool { return true }
	t.Cleanup(func() {
		targetSelectFn = oldSelect
		targetSSHReachableFn = oldSSH
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"target", "show"}); got != 0 {
			t.Fatalf("Run(target show) = %d, want 0", got)
		}
	})
	if selectTitle != "Choose a target to show" {
		t.Fatalf("select title = %q", selectTitle)
	}
	if len(selectOptions) != 1 || selectOptions[0] != (ui.SelectOption{Value: "app1-linode", Label: "app1-linode"}) {
		t.Fatalf("select options = %#v", selectOptions)
	}
	for _, want := range []string{"Target: app1-linode", "Provider: linode", "Hostname: app1-linode.nonfiction.dev", "ID: 98222343", "Status: reachable"} {
		if !strings.Contains(output, want) {
			t.Fatalf("target show output missing %q:\n%s", want, output)
		}
	}
}

func TestRunTargetListShowsReachableForProvisionedLinode(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	providers := []map[string]any{{
		"provider": "linode",
		"targets": []map[string]any{{
			"name":     "app1-linode",
			"provider": "linode",
			"hostname": "app1-linode.nonfiction.dev",
			"status":   "provisioned",
		}},
	}}
	data, err := json.MarshalIndent(providers, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "providers.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	oldSSH := targetSSHReachableFn
	targetSSHReachableFn = func(record map[string]any) bool { return true }
	t.Cleanup(func() { targetSSHReachableFn = oldSSH })

	output := captureStdout(t, func() {
		if got := Run([]string{"target", "list"}); got != 0 {
			t.Fatalf("Run(target list) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "reachable") || strings.Contains(output, "provisioned") {
		t.Fatalf("target list output = %q, want dynamic reachable status", output)
	}
}

func TestProviderTargetRecordsBackfillsKinstaStatus(t *testing.T) {
	providers := []map[string]any{{
		"provider": "kinsta",
		"status":   "active",
		"targets":  []map[string]any{{"id": "kinsta", "name": "kinsta"}},
	}}
	targets := providerTargetRecords(providers)
	if len(targets) != 1 {
		t.Fatalf("providerTargetRecords() len = %d, want 1", len(targets))
	}
	if got, want := recordValueString(targets[0]["status"]), "active"; got != want {
		t.Fatalf("target status = %q, want %q", got, want)
	}
}

func TestRunTargetListReconcilesCompletedLinodeHandoff(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Fatalf("health path = %q, want /healthz", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"server":"app2-linode","hostname":"app2-linode.nonfiction.dev","status":"ready"}`)
	}))
	t.Cleanup(server.Close)
	providers := []map[string]any{{
		"provider": "linode",
		"targets": []map[string]any{{
			"name":       "app2-linode",
			"provider":   "linode",
			"hostname":   "app2-linode.nonfiction.dev",
			"health_url": server.URL,
			"status":     "provisioning",
			"phase":      "dns_configured",
		}},
	}}
	data, err := json.MarshalIndent(providers, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "providers.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(providers.json) error = %v", err)
	}
	oldSSH := targetSSHReachableFn
	targetSSHReachableFn = func(record map[string]any) bool { return false }
	t.Cleanup(func() { targetSSHReachableFn = oldSSH })

	output := captureStdout(t, func() {
		if got := Run([]string{"target", "list"}); got != 0 {
			t.Fatalf("Run(target list) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "app2-linode") || !strings.Contains(output, "ssh unavailable") || strings.Contains(output, "provisioning") {
		t.Fatalf("target list output = %q, want dynamic ssh unavailable status", output)
	}
	records, err := state.LoadStateRecords("providers")
	if err != nil {
		t.Fatalf("LoadStateRecords(providers) error = %v", err)
	}
	targets := targetMaps(records[0]["targets"])
	if got, want := recordValueString(targets[0]["status"]), "provisioned"; got != want {
		t.Fatalf("saved status = %q, want %q", got, want)
	}
	if got, want := recordValueString(targets[0]["phase"]), "complete"; got != want {
		t.Fatalf("saved phase = %q, want %q", got, want)
	}
}

func TestRunTargetAddLinodeDryRunUsesTargetNameAndConfigDefaults(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	configData := map[string]string{
		"base_domain":           "nonfiction.dev",
		"dnsimple_account_id":   "14",
		"linode_default_region": "us-east",
		"linode_default_type":   "g6-standard-2",
		"linode_default_image":  "linode/ubuntu24.04",
		"linode_default_user":   "nonfiction",
	}
	data, err := json.Marshal(configData)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile(config.json) error = %v", err)
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"target", "add", "linode", "app1", "--dry-run", "--non-interactive", "--region", "ca-central", "--type", "g6-standard-1", "--image", "linode/ubuntu24.04", "--user", "nonfiction", "--keys", "all"}); got != 0 {
			t.Fatalf("Run(target add linode) = %d, want 0", got)
		}
	})
	for _, want := range []string{"app1-linode", "hostname: app1-linode.nonfiction.dev", "wildcard hostname: *.app1-linode.nonfiction.dev", "region: ca-central", "type: g6-standard-1", "image: linode/ubuntu24.04", "ssh user: nonfiction", "authorized keys: all Linode profile keys", "state: not checked"} {
		if !strings.Contains(output, want) {
			t.Fatalf("target add output missing %q:\n%s", want, output)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "providers.json")); !os.IsNotExist(err) {
		t.Fatalf("providers.json unexpectedly exists after dry-run: %v", err)
	}
}

func TestRunTargetAddLinodeRejectsWaitConflict(t *testing.T) {
	stderr := captureStderr(t, func() {
		if got := Run([]string{"target", "add", "linode", "app1", "--wait", "--no-wait"}); got != 1 {
			t.Fatalf("Run(target add linode --wait --no-wait) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "Choose either --wait or --no-wait, not both.") {
		t.Fatalf("Run() stderr = %q, want wait conflict", stderr)
	}
}

func TestRunTargetRemoveLinodeDeletesRemoteDNSAndState(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("DNSIMPLE_TOKEN", "token")
	providers := []map[string]any{{
		"provider": "linode",
		"targets": []map[string]any{{
			"id":       "98222343",
			"name":     "app1-linode",
			"provider": "linode",
			"hostname": "app1-linode.nonfiction.dev",
			"dns": map[string]any{
				"provider":   "dnsimple",
				"account_id": "14",
				"zone":       "nonfiction.dev",
				"hostname_record": map[string]any{
					"name": "app1-linode",
				},
				"wildcard_record": map[string]any{
					"name": "*.app1-linode",
				},
			},
		}, {
			"id":       "98222344",
			"name":     "app2-linode",
			"provider": "linode",
		}},
	}}
	if err := state.SaveStateRecords("providers", providers); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	deletedLinodes := []string{}
	oldLinodeDelete := runLinodeDeleteFn
	runLinodeDeleteFn = func(id string) error {
		deletedLinodes = append(deletedLinodes, id)
		return nil
	}
	deletedDNS := []string{}
	oldDNSDelete := deleteDNSRecordFn
	deleteDNSRecordFn = func(token, accountID, zone, name string) error {
		deletedDNS = append(deletedDNS, token+"|"+accountID+"|"+zone+"|"+name)
		return nil
	}
	deletedTXT := []string{}
	oldTXTDelete := deleteDNSTXTRecordFn
	deleteDNSTXTRecordFn = func(token, accountID, zone, name string) error {
		deletedTXT = append(deletedTXT, token+"|"+accountID+"|"+zone+"|"+name)
		return nil
	}
	t.Cleanup(func() {
		runLinodeDeleteFn = oldLinodeDelete
		deleteDNSRecordFn = oldDNSDelete
		deleteDNSTXTRecordFn = oldTXTDelete
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"target", "remove", "app1-linode", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(target remove) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Remove target plan:", "Linode API delete instance 98222343", "delete dnsimple app1-linode.nonfiction.dev", "delete dnsimple *.app1-linode.nonfiction.dev", "delete dnsimple TXT _acme-challenge.app1-linode.nonfiction.dev", "mode: execute"} {
		if !strings.Contains(output, want) {
			t.Fatalf("target remove output missing %q:\n%s", want, output)
		}
	}
	if got, want := strings.Join(deletedLinodes, ","), "98222343"; got != want {
		t.Fatalf("deleted linodes = %q, want %q", got, want)
	}
	if got, want := strings.Join(deletedDNS, ","), "token|14|nonfiction.dev|app1-linode,token|14|nonfiction.dev|*.app1-linode"; got != want {
		t.Fatalf("deleted DNS = %q, want %q", got, want)
	}
	if got, want := strings.Join(deletedTXT, ","), "token|14|nonfiction.dev|_acme-challenge.app1-linode"; got != want {
		t.Fatalf("deleted TXT = %q, want %q", got, want)
	}
	records, err := state.LoadStateRecords("providers")
	if err != nil {
		t.Fatalf("LoadStateRecords(providers) error = %v", err)
	}
	targets := targetMaps(records[0]["targets"])
	if len(targets) != 1 || recordValueString(targets[0]["name"]) != "app2-linode" {
		t.Fatalf("provider targets = %#v, want only app2-linode", targets)
	}
}

func TestRunTargetRemoveLinodeInfersDNSRecordsWhenCachedDNSNamesMissing(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("DNSIMPLE_TOKEN", "token")
	providers := []map[string]any{{
		"provider": "linode",
		"targets": []map[string]any{{
			"id":       "98222343",
			"name":     "app1-linode",
			"provider": "linode",
			"hostname": "app1-linode.nonfiction.dev",
			"dns": map[string]any{
				"provider":   "dnsimple",
				"account_id": "14",
				"zone":       "nonfiction.dev",
			},
		}},
	}}
	if err := state.SaveStateRecords("providers", providers); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	oldLinodeDelete := runLinodeDeleteFn
	runLinodeDeleteFn = func(id string) error { return nil }
	deletedDNS := []string{}
	oldDNSDelete := deleteDNSRecordFn
	deleteDNSRecordFn = func(token, accountID, zone, name string) error {
		deletedDNS = append(deletedDNS, token+"|"+accountID+"|"+zone+"|"+name)
		return nil
	}
	deletedTXT := []string{}
	oldTXTDelete := deleteDNSTXTRecordFn
	deleteDNSTXTRecordFn = func(token, accountID, zone, name string) error {
		deletedTXT = append(deletedTXT, token+"|"+accountID+"|"+zone+"|"+name)
		return nil
	}
	t.Cleanup(func() {
		runLinodeDeleteFn = oldLinodeDelete
		deleteDNSRecordFn = oldDNSDelete
		deleteDNSTXTRecordFn = oldTXTDelete
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"target", "remove", "app1-linode", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(target remove) = %d, want 0", got)
		}
	})
	for _, want := range []string{"delete dnsimple app1-linode.nonfiction.dev", "delete dnsimple *.app1-linode.nonfiction.dev", "delete dnsimple TXT _acme-challenge.app1-linode.nonfiction.dev"} {
		if !strings.Contains(output, want) {
			t.Fatalf("target remove output missing %q:\n%s", want, output)
		}
	}
	if got, want := strings.Join(deletedDNS, ","), "token|14|nonfiction.dev|app1-linode,token|14|nonfiction.dev|*.app1-linode"; got != want {
		t.Fatalf("deleted DNS = %q, want %q", got, want)
	}
	if got, want := strings.Join(deletedTXT, ","), "token|14|nonfiction.dev|_acme-challenge.app1-linode"; got != want {
		t.Fatalf("deleted TXT = %q, want %q", got, want)
	}
}

func TestRunTargetRemoveFailsWhenDNSimpleListing404s(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("DNSIMPLE_TOKEN", "token")
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev", "dnsimple_account_id": "14"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	providers := []map[string]any{{
		"provider": "linode",
		"targets": []map[string]any{{
			"id":       "98222343",
			"name":     "app1-linode",
			"provider": "linode",
			"hostname": "app1-linode.nonfiction.dev",
		}, {
			"id":       "98222344",
			"name":     "app2-linode",
			"provider": "linode",
		}},
	}}
	if err := state.SaveStateRecords("providers", providers); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	deletedLinodes := []string{}
	oldLinodeDelete := runLinodeDeleteFn
	runLinodeDeleteFn = func(id string) error {
		deletedLinodes = append(deletedLinodes, id)
		return nil
	}
	oldDNSDelete := deleteDNSRecordFn
	deleteDNSRecordFn = func(token, accountID, zone, name string) error {
		return fmt.Errorf("Listing DNSimple A records for zone %s: GET https://api.dnsimple.com/v2/zones/%s/records?type=A: 404 Not Found", zone, zone)
	}
	oldTXTDelete := deleteDNSTXTRecordFn
	deleteDNSTXTRecordFn = func(token, accountID, zone, name string) error {
		return fmt.Errorf("Listing DNSimple TXT records for zone %s: GET https://api.dnsimple.com/v2/zones/%s/records?type=TXT: 404 Not Found", zone, zone)
	}
	t.Cleanup(func() {
		runLinodeDeleteFn = oldLinodeDelete
		deleteDNSRecordFn = oldDNSDelete
		deleteDNSTXTRecordFn = oldTXTDelete
	})

	output := captureStderr(t, func() {
		if got := Run([]string{"target", "remove", "app1-linode", "--execute", "--yes", "--non-interactive"}); got != 1 {
			t.Fatalf("Run(target remove) = %d, want 1", got)
		}
	})
	if strings.Contains(output, "already absent") {
		t.Fatalf("target remove output = %q, should not treat list failure as already absent", output)
	}
	if !strings.Contains(output, "Listing DNSimple A records for zone nonfiction.dev") {
		t.Fatalf("target remove output = %q, want listing error", output)
	}
	if got, want := strings.Join(deletedLinodes, ","), "98222343"; got != want {
		t.Fatalf("deleted linodes = %q, want %q", got, want)
	}
	records, err := state.LoadStateRecords("providers")
	if err != nil {
		t.Fatalf("LoadStateRecords(providers) error = %v", err)
	}
	targets := targetMaps(records[0]["targets"])
	if len(targets) != 2 {
		t.Fatalf("provider targets = %#v, want unchanged targets after DNS failure", targets)
	}
}

func TestRunTargetRemoveRequiresDNSimpleAccountIDBeforeDeletingLinode(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("DNSIMPLE_TOKEN", "token")
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	providers := []map[string]any{{
		"provider": "linode",
		"targets": []map[string]any{{
			"id":       "98222343",
			"name":     "app1-linode",
			"provider": "linode",
			"hostname": "app1-linode.nonfiction.dev",
		}},
	}}
	if err := state.SaveStateRecords("providers", providers); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	deletedLinodes := []string{}
	oldLinodeDelete := runLinodeDeleteFn
	runLinodeDeleteFn = func(id string) error {
		deletedLinodes = append(deletedLinodes, id)
		return nil
	}
	t.Cleanup(func() { runLinodeDeleteFn = oldLinodeDelete })

	output := captureStderr(t, func() {
		if got := Run([]string{"target", "remove", "app1-linode", "--execute", "--yes", "--non-interactive"}); got != 1 {
			t.Fatalf("Run(target remove) = %d, want 1", got)
		}
	})
	if !strings.Contains(output, "Expected dnsimple_account_id") {
		t.Fatalf("target remove output = %q, want missing account id error", output)
	}
	if len(deletedLinodes) != 0 {
		t.Fatalf("deleted linodes = %v, want none", deletedLinodes)
	}
}

func TestRunTargetRemoveWithoutTargetPromptsPicker(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	providers := []map[string]any{{
		"provider": "linode",
		"targets": []map[string]any{{
			"id":       "98222343",
			"name":     "app1-linode",
			"provider": "linode",
			"hostname": "app1-linode.nonfiction.dev",
		}},
	}}
	if err := state.SaveStateRecords("providers", providers); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	oldSelect := targetSelectFn
	var selectTitle string
	var selectOptions []ui.SelectOption
	targetSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectTitle = title
		selectOptions = append([]ui.SelectOption(nil), options...)
		return "app1-linode", nil
	}
	t.Cleanup(func() { targetSelectFn = oldSelect })

	output := captureStdout(t, func() {
		if got := Run([]string{"target", "remove", "--dry-run"}); got != 0 {
			t.Fatalf("Run(target remove --dry-run) = %d, want 0", got)
		}
	})
	if selectTitle != "Choose a target to remove" {
		t.Fatalf("select title = %q", selectTitle)
	}
	if len(selectOptions) != 1 || selectOptions[0] != (ui.SelectOption{Value: "app1-linode", Label: "app1-linode"}) {
		t.Fatalf("select options = %#v", selectOptions)
	}
	for _, want := range []string{"Remove target plan:", "target: app1-linode", "mode: dry-run"} {
		if !strings.Contains(output, want) {
			t.Fatalf("target remove output missing %q:\n%s", want, output)
		}
	}
}

func TestRunTargetRemoveRejectsKinsta(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "kinsta", "targets": []map[string]any{{"id": "kinsta", "name": "kinsta"}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	stderr := captureStderr(t, func() {
		if got := Run([]string{"target", "remove", "kinsta", "--dry-run", "--non-interactive"}); got != 1 {
			t.Fatalf("Run(target remove kinsta) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "Kinsta target cannot be removed.") {
		t.Fatalf("Run() stderr = %q, want kinsta rejection", stderr)
	}
}

func TestRunTargetRemoveLinodeRemovesRelatedSitesFromCache(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"id": "98222343", "name": "app1-linode", "provider": "linode"}, {"id": "98222344", "name": "app2-linode", "provider": "linode"}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"site_id": "client", "name": "client", "provider": "linode", "env": "live", "target": "app1-linode", "hostname": "client.app1-linode.nonfiction.dev"},
		{"site_id": "client", "name": "client", "provider": "linode", "env": "staging", "target": "app1-linode", "hostname": "client-staging.app1-linode.nonfiction.dev"},
		{"site_id": "other", "name": "other", "provider": "linode", "env": "live", "target": "app2-linode", "hostname": "other.app2-linode.nonfiction.dev"},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	deletedLinodes := []string{}
	oldLinodeDelete := runLinodeDeleteFn
	runLinodeDeleteFn = func(id string) error {
		deletedLinodes = append(deletedLinodes, id)
		return nil
	}
	t.Cleanup(func() { runLinodeDeleteFn = oldLinodeDelete })

	output := captureStdout(t, func() {
		if got := Run([]string{"target", "remove", "app1-linode", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(target remove app1-linode) = %d, want 0", got)
		}
	})
	for _, want := range []string{"related sites: client", "site cache action: remove 1 site(s) from local cache", "mode: execute"} {
		if !strings.Contains(output, want) {
			t.Fatalf("target remove output missing %q:\n%s", want, output)
		}
	}
	if got, want := strings.Join(deletedLinodes, ","), "98222343"; got != want {
		t.Fatalf("deleted linodes = %q, want %q", got, want)
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(records) != 1 || siteRecordID(records[0]) != "other" {
		t.Fatalf("site cache after target remove = %#v, want only other", records)
	}
}

func TestRunTargetListFallsBackToLegacyServersCache(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	servers := map[string]any{"servers": map[string]any{"app1-linode": map[string]any{"id": 98222343, "name": "app1-linode", "provider": "linode", "hostname": "app1.nonfiction.dev", "status": "active"}}}
	data, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "servers.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	oldSSH := targetSSHReachableFn
	targetSSHReachableFn = func(record map[string]any) bool { return false }
	t.Cleanup(func() { targetSSHReachableFn = oldSSH })

	output := captureStdout(t, func() {
		if got := Run([]string{"target", "list"}); got != 0 {
			t.Fatalf("Run(target list) = %d, want 0", got)
		}
	})
	for _, want := range []string{"target", "app1-linode", "linode", "app1.nonfiction.dev", "ssh unavailable"} {
		if !strings.Contains(output, want) {
			t.Fatalf("target list output missing %q:\n%s", want, output)
		}
	}
}

func TestRunTargetListTreatsProvidersCacheAsAuthoritative(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	providers := []map[string]any{{"provider": "dnsimple", "account_id": "14", "targets": []map[string]any{}}}
	providerData, err := json.MarshalIndent(providers, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(providers) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "providers.json"), append(providerData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(providers) error = %v", err)
	}
	servers := map[string]any{"servers": map[string]any{"app1-linode": map[string]any{"name": "app1-linode", "provider": "linode"}}}
	serverData, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(servers) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "servers.json"), append(serverData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(servers) error = %v", err)
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"target", "list"}); got != 0 {
			t.Fatalf("Run(target list) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "No targets found.") || strings.Contains(output, "app1-linode") {
		t.Fatalf("target list output = %q, want providers cache to win", output)
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
	for _, want := range []string{"Site refresh discovers sites from cached targets.", filepath.Join(stateDir, "sites.json"), filepath.Join(stateDir, "providers.json"), "No cached targets found.", "No sites found."} {
		if !strings.Contains(output, want) {
			t.Fatalf("site list --refresh output missing %q:\n%s", want, output)
		}
	}
}

func TestRunSiteRefreshReportsCachedTargets(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	providers := []map[string]any{{
		"provider": "other",
		"targets":  []map[string]any{{"name": "other", "provider": "other"}},
	}}
	data, err := json.MarshalIndent(providers, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(providers) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "providers.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(providers) error = %v", err)
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "refresh"}); got != 0 {
			t.Fatalf("Run(site refresh) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Site refresh discovers sites from cached targets.", "Targets: 1", "other (other)", "Skipped targets: 1", "No remote targets were refreshed; no site cache was changed."} {
		if !strings.Contains(output, want) {
			t.Fatalf("site refresh output missing %q:\n%s", want, output)
		}
	}
}

func TestRunSiteRefreshDiscoversLinodeRemoteSites(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "old-app1-linode", "env": "live", "target": "app1-linode"}, {"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "target": "kinsta"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	var sshArgs []string
	oldRunSSHOutput := runSSHOutputFn
	runSSHOutputFn = func(args []string) ([]byte, error) {
		sshArgs = append([]string(nil), args...)
		if len(args) > 0 && args[len(args)-1] == "/var/lib/nf/target.json" {
			return []byte(`{"php_version":"8.3"}`), nil
		}
		return []byte(`[{"site_id":"client-app1-linode","name":"client","env":"live","url":"https://client.app1-linode.nonfiction.dev/"},{"site_id":"client-app1-linode","name":"client","env":"staging","target":"app1-linode","url":"https://client-staging.app1-linode.nonfiction.dev/"}]`), nil
	}
	t.Cleanup(func() { runSSHOutputFn = oldRunSSHOutput })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "refresh"}); got != 0 {
			t.Fatalf("Run(site refresh) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Refreshed targets: 1", "Discovered remote site envs: 2", filepath.Join(stateDir, "sites.json")} {
		if !strings.Contains(output, want) {
			t.Fatalf("site refresh output missing %q:\n%s", want, output)
		}
	}
	joinedArgs := strings.Join(sshArgs, " ")
	for _, want := range []string{"ssh", "nonfiction@app1-linode.nonfiction.dev", "cat", "/var/lib/nf/sites.json"} {
		if !strings.Contains(joinedArgs, want) {
			t.Fatalf("ssh args missing %q: %#v", want, sshArgs)
		}
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("site records len = %d, want 3: %#v", len(records), records)
	}
	if siteRecordID(records[0]) == "old-app1-linode" || siteRecordID(records[1]) == "old-app1-linode" || siteRecordID(records[2]) == "old-app1-linode" {
		t.Fatalf("old app1 record was not replaced: %#v", records)
	}
	if got := recordValueString(records[1]["provider"]); got != "linode" {
		t.Fatalf("normalized provider = %q, want linode in %#v", got, records[1])
	}
	if got := siteProviderTarget(records[1]); got != "app1-linode" {
		t.Fatalf("normalized target = %q, want app1-linode in %#v", got, records[1])
	}
	if got := sitePHPVersion(records[1]); got != "8.3" {
		t.Fatalf("normalized php version = %q, want 8.3 in %#v", got, records[1])
	}
}

func TestRunSiteRefreshPrunesSitesForRemovedTargets(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app2-linode", "provider": "linode", "hostname": "app2-linode.nonfiction.dev", "ssh": map[string]any{"user": "nonfiction", "host": "app2-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "linode", "site_id": "foobar", "name": "foobar", "env": "live", "target": "app1-linode"},
		{"provider": "linode", "site_id": "foobar", "name": "foobar", "env": "staging", "target": "app1-linode"},
		{"provider": "linode", "site_id": "happytents.app2-linode", "name": "happytents", "env": "live", "target": "app2-linode"},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	oldRunSSHOutput := runSSHOutputFn
	runSSHOutputFn = func(args []string) ([]byte, error) {
		return []byte(`[{
			"site_id":"happytents.app2-linode",
			"name":"happytents",
			"env":"live",
			"target":"app2-linode",
			"url":"https://happytents.app2-linode.nonfiction.dev/"
		},{
			"site_id":"happytents.app2-linode",
			"name":"happytents",
			"env":"staging",
			"target":"app2-linode",
			"url":"https://happytents-staging.app2-linode.nonfiction.dev/"
		}]`), nil
	}
	t.Cleanup(func() { runSSHOutputFn = oldRunSSHOutput })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "refresh"}); got != 0 {
			t.Fatalf("Run(site refresh) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Targets: 1", "app2-linode (linode)", "Refreshed targets: 1", "Discovered remote site envs: 2"} {
		if !strings.Contains(output, want) {
			t.Fatalf("site refresh output missing %q:\n%s", want, output)
		}
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("site records len = %d, want 2: %#v", len(records), records)
	}
	for _, record := range records {
		if got := siteProviderTarget(record); got != "app2-linode" {
			t.Fatalf("site refresh kept site for removed target %q: %#v", got, records)
		}
		if siteRecordID(record) == "foobar" {
			t.Fatalf("site refresh kept removed target site: %#v", records)
		}
	}
}

func TestRunSiteRefreshDiscoversKinstaRemoteSites(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("KINSTA_API_KEY", "")
	if err := os.WriteFile(config.EnvFile(), []byte("KINSTA_API_KEY=kinsta-token\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "kinsta", "targets": []map[string]any{{"name": "kinsta", "provider": "kinsta", "company_id": "company-123"}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "old.kinsta", "env": "live", "target": "kinsta"}, {"provider": "linode", "site_id": "client.app1-linode", "env": "live", "target": "app1-linode"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer kinsta-token"; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites":
			_ = json.NewEncoder(w).Encode(map[string]any{"company": map[string]any{"sites": []map[string]any{{"id": "ksite123", "name": "client", "display_name": "Client"}}}})
		case "GET /sites/ksite123/environments":
			_ = json.NewEncoder(w).Encode(map[string]any{"site": map[string]any{"environments": []map[string]any{
				{"id": "kenv-live", "name": "client", "display_name": "Client", "web_root": "/www/client_123/public", "container_info": map[string]any{"php_engine_version": "php8.3"}, "primaryDomain": map[string]any{"id": "kdom-live", "name": "client.kinsta.nonfiction.dev"}},
				{"id": "kenv-staging", "name": "client-staging", "display_name": "Client Staging", "web_root": "/www/clientstaging_456/public", "container_info": map[string]any{"php_engine_version": "php8.3"}, "primaryDomain": map[string]any{"id": "kdom-staging", "name": "client-staging.kinsta.nonfiction.dev"}},
			}}})
		case "GET /sites/ksite123/environments/kenv-live/ssh/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client_123", "ssh_command": "ssh client_123@203.0.113.10 -p 12345"})
		case "GET /sites/ksite123/environments/kenv-staging/ssh/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"host": "203.0.113.11", "port": "12346", "user": "clientstaging_456", "ssh_command": "ssh clientstaging_456@203.0.113.11 -p 12346"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KINSTA_BASE_URL", server.URL)

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "refresh"}); got != 0 {
			t.Fatalf("Run(site refresh) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Refreshed targets: 1", "Discovered remote site envs: 2", filepath.Join(stateDir, "sites.json")} {
		if !strings.Contains(output, want) {
			t.Fatalf("site refresh output missing %q:\n%s", want, output)
		}
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("site records len = %d, want 2: %#v", len(records), records)
	}
	for _, want := range []struct{ env, path, database, sshHost, sshPort, sshUser string }{
		{"live", "/www/client_123/public", "client_123", "203.0.113.10", "12345", "client_123"},
		{"staging", "/www/clientstaging_456/public", "clientstaging_456", "203.0.113.11", "12346", "clientstaging_456"},
	} {
		var record map[string]any
		for _, candidate := range records {
			if siteRecordID(candidate) == "client.kinsta" && siteEnvName(candidate) == want.env {
				record = candidate
				break
			}
		}
		if record == nil {
			t.Fatalf("missing %s kinsta record in %#v", want.env, records)
		}
		if got := recordValueString(record["path"]); got != want.path {
			t.Fatalf("%s path = %q, want %q", want.env, got, want.path)
		}
		if got := recordValueString(record["database"]); got != want.database {
			t.Fatalf("%s database = %q, want %q", want.env, got, want.database)
		}
		if got := sitePHPVersion(record); got != "8.3" {
			t.Fatalf("%s php version = %q, want 8.3", want.env, got)
		}
		if got := mapStringAtPath(record, "ssh", "host"); got != want.sshHost {
			t.Fatalf("%s ssh host = %q, want %q", want.env, got, want.sshHost)
		}
		if got := mapStringAtPath(record, "ssh", "port"); got != want.sshPort {
			t.Fatalf("%s ssh port = %q, want %q", want.env, got, want.sshPort)
		}
		if got := mapStringAtPath(record, "ssh", "user"); got != want.sshUser {
			t.Fatalf("%s ssh user = %q, want %q", want.env, got, want.sshUser)
		}
	}
}

func TestRunSiteAddLinodeDryRunPlansLiveAndStaging(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev", "default_wp_email": "web@nonfiction.ca", "default_wp_user": "admin", "linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "ssh_user": "nonfiction"}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	oldRunSSH := runSSHScriptFn
	runSSHScriptFn = func(user, host, script string) error {
		t.Fatalf("runSSHScriptFn called during dry-run")
		return nil
	}
	t.Cleanup(func() { runSSHScriptFn = oldRunSSH })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "add", "app1-linode", "foobar", "--dry-run", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site add) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Add site plan:", "target: app1-linode", "site: foobar", "site id: foobar.app1-linode", "admin email: web@nonfiction.ca", "admin password: derived from foobar", "path: /var/www/sites/foobar/public", "database: foobar", "vhost: foobar.app1-linode.nonfiction.dev", "path: /var/www/sites/foobar_staging/public", "database: foobar_staging", "vhost: foobar-staging.app1-linode.nonfiction.dev", "remote state: /var/lib/nf/sites.json", "mode: dry-run"} {
		if !strings.Contains(output, want) {
			t.Fatalf("site add dry-run output missing %q:\n%s", want, output)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sites.json")); !os.IsNotExist(err) {
		t.Fatalf("sites.json unexpectedly exists after dry-run: %v", err)
	}
}

func TestRunSiteAddLinodeExecuteRunsSSHAndCachesEnvs(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev", "default_wp_email": "web@nonfiction.ca", "default_wp_user": "admin", "linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	var sshUser, sshHost, sshScript string
	oldRunSSH := runSSHScriptFn
	runSSHScriptFn = func(user, host, script string) error {
		sshUser, sshHost, sshScript = user, host, script
		return nil
	}
	t.Cleanup(func() { runSSHScriptFn = oldRunSSH })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "add", "app1-linode", "foobar", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site add execute) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Site added.") || !strings.Contains(output, "mode: execute") {
		t.Fatalf("site add execute output = %q, want success", output)
	}
	if sshUser != "nonfiction" || sshHost != "app1-linode.nonfiction.dev" {
		t.Fatalf("ssh target = %s@%s, want nonfiction@app1-linode.nonfiction.dev", sshUser, sshHost)
	}
	for _, want := range []string{"/var/www/sites/foobar/public", "/var/www/sites/foobar_staging/public", "CREATE DATABASE IF NOT EXISTS", "wp core install", "foobar.app1-linode.nonfiction.dev", "foobar-staging.app1-linode.nonfiction.dev", "/var/lib/nf/sites.json"} {
		if !strings.Contains(sshScript, want) {
			t.Fatalf("ssh script missing %q:\n%s", want, sshScript)
		}
	}
	sites, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(sites) != 2 {
		t.Fatalf("sites len = %d, want 2: %#v", len(sites), sites)
	}
	for _, want := range []struct{ env, path, db, host string }{
		{"live", "/var/www/sites/foobar/public", "foobar", "foobar.app1-linode.nonfiction.dev"},
		{"staging", "/var/www/sites/foobar_staging/public", "foobar_staging", "foobar-staging.app1-linode.nonfiction.dev"},
	} {
		var record map[string]any
		for _, candidate := range sites {
			if recordValueString(candidate["env"]) == want.env {
				record = candidate
				break
			}
		}
		if record == nil {
			t.Fatalf("missing %s record in %#v", want.env, sites)
		}
		if got := recordValueString(record["path"]); got != want.path {
			t.Fatalf("%s path = %q, want %q", want.env, got, want.path)
		}
		if got := recordValueString(record["database"]); got != want.db {
			t.Fatalf("%s database = %q, want %q", want.env, got, want.db)
		}
		if got := recordValueString(record["hostname"]); got != want.host {
			t.Fatalf("%s hostname = %q, want %q", want.env, got, want.host)
		}
		if got := recordValueString(record["target"]); got != "app1-linode" {
			t.Fatalf("%s target = %q, want app1-linode", want.env, got)
		}
		if got := recordValueString(record["site_id"]); got != "foobar.app1-linode" {
			t.Fatalf("%s site_id = %q, want foobar.app1-linode", want.env, got)
		}
		wantEnvID := "foobar.app1-linode:" + want.env
		if got := recordValueString(record["env_id"]); got != wantEnvID {
			t.Fatalf("%s env_id = %q, want %q", want.env, got, wantEnvID)
		}
		if got := recordValueString(record["name"]); got != "foobar" {
			t.Fatalf("%s name = %q, want foobar", want.env, got)
		}
		if got := recordValueString(record["target_name"]); got != "" {
			t.Fatalf("%s target_name = %q, want empty", want.env, got)
		}
		if got := mapStringAtPath(record, "ssh", "host"); got != "app1-linode.nonfiction.dev" {
			t.Fatalf("%s ssh.host = %q, want app1-linode.nonfiction.dev", want.env, got)
		}
		if got := mapStringAtPath(record, "ssh", "user"); got != "nonfiction" {
			t.Fatalf("%s ssh.user = %q, want nonfiction", want.env, got)
		}
		if _, ok := record["linode"]; ok {
			t.Fatalf("%s linode should be omitted when empty", want.env)
		}
		for _, key := range []string{"environment", "server", "server_name", "server_hostname"} {
			if _, ok := record[key]; ok {
				t.Fatalf("%s %s should be omitted from normalized site data", want.env, key)
			}
		}
	}
	if !strings.Contains(sshScript, "--arg site_id foobar.app1-linode") {
		t.Fatalf("ssh script missing canonical site id:\n%s", sshScript)
	}
	for _, want := range []string{"create_env live /var/www/sites/foobar/public foobar foobar.app1-linode.nonfiction.dev https://foobar.app1-linode.nonfiction.dev Foobar foobar.app1-linode:live foobar.app1-linode.live", "create_env staging /var/www/sites/foobar_staging/public foobar_staging foobar-staging.app1-linode.nonfiction.dev https://foobar-staging.app1-linode.nonfiction.dev 'Foobar Staging' foobar.app1-linode:staging foobar.app1-linode.staging"} {
		if !strings.Contains(sshScript, want) {
			t.Fatalf("ssh script missing env id command %q:\n%s", want, sshScript)
		}
	}
	for _, want := range []string{"nf-site-$file_slug", "$file_slug.access.log", "$file_slug.error.log"} {
		if !strings.Contains(sshScript, want) {
			t.Fatalf("ssh script missing file slug usage %q:\n%s", want, sshScript)
		}
	}
	listOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "list"}); got != 0 {
			t.Fatalf("Run(site list) = %d, want 0", got)
		}
	})
	for _, want := range []string{"site id", "name", "target", "envs", "foobar.app1-linode", "foobar", "app1-linode", "live,staging"} {
		if !strings.Contains(listOutput, want) {
			t.Fatalf("site list output missing %q:\n%s", want, listOutput)
		}
	}
	for _, notWant := range []string{"provider", "foobar-live", "live url", "staging url", "https://foobar.app1-linode.nonfiction.dev", "https://foobar-staging.app1-linode.nonfiction.dev"} {
		if strings.Contains(listOutput, notWant) {
			t.Fatalf("site list output contains %q:\n%s", notWant, listOutput)
		}
	}
	showOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "show", "foobar.app1-linode"}); got != 0 {
			t.Fatalf("Run(site show) = %d, want 0", got)
		}
	})
	for _, want := range []string{"foobar.app1-linode", "Site       foobar.app1-linode", "Name       foobar", "Provider   linode", "Target     app1-linode", "Environments:", "env", "php", "url", "live", "staging", "foobar.app1-linode.nonfiction.dev", "foobar-staging.app1-linode.nonfiction.dev"} {
		if !strings.Contains(showOutput, want) {
			t.Fatalf("site show output missing %q:\n%s", want, showOutput)
		}
	}
	jsonOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "show", "foobar.app1-linode", "--json"}); got != 0 {
			t.Fatalf("Run(site show --json) = %d, want 0", got)
		}
	})
	for _, want := range []string{`"site_id": "foobar.app1-linode"`, `"env_id": "foobar.app1-linode:live"`, `"env_id": "foobar.app1-linode:staging"`, `"name": "foobar"`, `"target": "app1-linode"`, `"envs":`, `"env": "live"`, `"env": "staging"`} {
		if !strings.Contains(jsonOutput, want) {
			t.Fatalf("site show --json output missing %q:\n%s", want, jsonOutput)
		}
	}
}

func TestRunSiteAddKinstaDryRunPlansLiveAndStaging(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev", "default_wp_email": "web@nonfiction.ca", "default_wp_user": "admin", "dnsimple_account_id": "14", "kinsta_default_region": "us-central1"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "kinsta", "targets": []map[string]any{{"name": "kinsta", "provider": "kinsta", "company_id": "company-123", "status": "active"}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	oldProvision := kinstaProvisionSiteFn
	kinstaProvisionSiteFn = func(plan kinstaSiteAddPlan) (kinstaProvisionResult, error) {
		t.Fatalf("kinstaProvisionSiteFn called during dry-run")
		return kinstaProvisionResult{}, nil
	}
	t.Cleanup(func() { kinstaProvisionSiteFn = oldProvision })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "add", "kinsta", "foobar", "--dry-run", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site add kinsta) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Add site plan:", "target: kinsta", "provider: kinsta", "company id: company-123", "site: foobar", "site id: foobar.kinsta", "region: us-central1", "php: 8.3", "admin email: web@nonfiction.ca", "admin password: derived from foobar", "domain: foobar.kinsta.nonfiction.dev", "domain: foobar-staging.kinsta.nonfiction.dev", "dns: dnsimple zone nonfiction.dev account 14", "mode: dry-run"} {
		if !strings.Contains(output, want) {
			t.Fatalf("site add kinsta dry-run output missing %q:\n%s", want, output)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sites.json")); !os.IsNotExist(err) {
		t.Fatalf("sites.json unexpectedly exists after dry-run: %v", err)
	}
}

func TestRunSiteAddKinstaExecuteCachesEnvs(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev", "default_wp_email": "web@nonfiction.ca", "default_wp_user": "admin", "dnsimple_account_id": "14"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "kinsta", "targets": []map[string]any{{"name": "kinsta", "provider": "kinsta", "company_id": "company-123", "status": "active"}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	var capturedPlan kinstaSiteAddPlan
	oldProvision := kinstaProvisionSiteFn
	kinstaProvisionSiteFn = func(plan kinstaSiteAddPlan) (kinstaProvisionResult, error) {
		capturedPlan = plan
		return kinstaProvisionResult{SiteID: "ksite123", Envs: []kinstaSiteAddEnvPlan{
			{Env: "live", Domain: "foobar.kinsta.nonfiction.dev", URL: "https://foobar.kinsta.nonfiction.dev", Branch: "main", EnvID: "kenv-live", DomainID: "kdom-live", Path: "/www/foobar/public", Database: "foobar", SSHHost: "203.0.113.10", SSHPort: "12345", SSHUser: "foobar", SSHCmd: "ssh foobar@203.0.113.10 -p 12345"},
			{Env: "staging", Domain: "foobar-staging.kinsta.nonfiction.dev", URL: "https://foobar-staging.kinsta.nonfiction.dev", Branch: "develop", EnvID: "kenv-staging", DomainID: "kdom-staging", Path: "/www/foobarstaging/public", Database: "foobarstaging", SSHHost: "203.0.113.11", SSHPort: "12346", SSHUser: "foobarstaging", SSHCmd: "ssh foobarstaging@203.0.113.11 -p 12346"},
		}}, nil
	}
	t.Cleanup(func() { kinstaProvisionSiteFn = oldProvision })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "add", "kinsta", "foobar", "--region", "ca-toronto-1", "--php", "8.2", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site add kinsta execute) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Site added.") || !strings.Contains(output, "mode: execute") {
		t.Fatalf("site add kinsta execute output = %q, want success", output)
	}
	if capturedPlan.Region != "ca-toronto-1" || capturedPlan.SiteID != "foobar.kinsta" || capturedPlan.PHPVersion != "8.2" {
		t.Fatalf("captured plan = %#v, want region and site id", capturedPlan)
	}
	sites, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(sites) != 2 {
		t.Fatalf("sites len = %d, want 2: %#v", len(sites), sites)
	}
	for _, want := range []struct{ env, domain, branch, envID, domainID, path, database, sshHost, sshPort, sshUser string }{
		{"live", "foobar.kinsta.nonfiction.dev", "main", "kenv-live", "kdom-live", "/www/foobar/public", "foobar", "203.0.113.10", "12345", "foobar"},
		{"staging", "foobar-staging.kinsta.nonfiction.dev", "develop", "kenv-staging", "kdom-staging", "/www/foobarstaging/public", "foobarstaging", "203.0.113.11", "12346", "foobarstaging"},
	} {
		var record map[string]any
		for _, candidate := range sites {
			if recordValueString(candidate["env"]) == want.env {
				record = candidate
				break
			}
		}
		if record == nil {
			t.Fatalf("missing %s record in %#v", want.env, sites)
		}
		if got := recordValueString(record["site_id"]); got != "foobar.kinsta" {
			t.Fatalf("%s site_id = %q, want foobar.kinsta", want.env, got)
		}
		if got := recordValueString(record["env_id"]); got != "foobar.kinsta:"+want.env {
			t.Fatalf("%s env_id = %q, want foobar.kinsta:%s", want.env, got, want.env)
		}
		if got := recordValueString(record["target"]); got != "kinsta" {
			t.Fatalf("%s target = %q, want kinsta", want.env, got)
		}
		if got := recordValueString(record["hostname"]); got != want.domain {
			t.Fatalf("%s hostname = %q, want %q", want.env, got, want.domain)
		}
		if got := mapStringAtPath(record, "kinsta", "branch"); got != want.branch {
			t.Fatalf("%s kinsta.branch = %q, want %q", want.env, got, want.branch)
		}
		if _, ok := record["branch"]; ok {
			t.Fatalf("%s branch should be omitted from top-level normalized site data", want.env)
		}
		if got := recordValueString(record["php_version"]); got != "8.2" {
			t.Fatalf("%s php_version = %q, want 8.2", want.env, got)
		}
		if got := recordValueString(record["path"]); got != want.path {
			t.Fatalf("%s path = %q, want %q", want.env, got, want.path)
		}
		if got := recordValueString(record["database"]); got != want.database {
			t.Fatalf("%s database = %q, want %q", want.env, got, want.database)
		}
		if got := mapStringAtPath(record, "ssh", "host"); got != want.sshHost {
			t.Fatalf("%s ssh host = %q, want %q", want.env, got, want.sshHost)
		}
		if got := mapStringAtPath(record, "ssh", "port"); got != want.sshPort {
			t.Fatalf("%s ssh port = %q, want %q", want.env, got, want.sshPort)
		}
		if got := mapStringAtPath(record, "ssh", "user"); got != want.sshUser {
			t.Fatalf("%s ssh user = %q, want %q", want.env, got, want.sshUser)
		}
		if got := mapStringAtPath(record, "kinsta", "site_id"); got != "ksite123" {
			t.Fatalf("%s kinsta site_id = %q, want ksite123", want.env, got)
		}
		if got := mapStringAtPath(record, "kinsta", "environment_id"); got != want.envID {
			t.Fatalf("%s kinsta environment_id = %q, want %q", want.env, got, want.envID)
		}
		if got := mapStringAtPath(record, "kinsta", "domain_id"); got != want.domainID {
			t.Fatalf("%s kinsta domain_id = %q, want %q", want.env, got, want.domainID)
		}
		for _, key := range []string{"company_id", "php_version", "path", "database", "ssh"} {
			if _, ok := mapMapAtPath(record, "kinsta")[key]; ok {
				t.Fatalf("%s kinsta.%s should be omitted from normalized provider data", want.env, key)
			}
		}
	}
	showOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "show", "foobar.kinsta"}); got != 0 {
			t.Fatalf("Run(site show kinsta) = %d, want 0", got)
		}
	})
	for _, want := range []string{"foobar.kinsta", "Site       foobar.kinsta", "Name       foobar", "Provider   kinsta", "Target     kinsta", "Environments:", "env", "php", "url", "live     8.2", "staging  8.2", "https://foobar.kinsta.nonfiction.dev", "https://foobar-staging.kinsta.nonfiction.dev"} {
		if !strings.Contains(showOutput, want) {
			t.Fatalf("site show kinsta output missing %q:\n%s", want, showOutput)
		}
	}
	for _, notWant := range []string{"path", "database", "ssh", "/www/foobar/public", "foobar@203.0.113.10:12345", "/www/foobarstaging/public", "foobarstaging@203.0.113.11:12346"} {
		if strings.Contains(showOutput, notWant) {
			t.Fatalf("site show kinsta output contains %q:\n%s", notWant, showOutput)
		}
	}
}

func TestProvisionKinstaSiteCreatesStagingDomainsDNSAndPrimaryDomains(t *testing.T) {
	t.Setenv("KINSTA_API_KEY", "kinsta-token")
	t.Setenv("DNSIMPLE_TOKEN", "dnsimple-token")
	createdSite := false
	createdStaging := false
	modifiedPHP := map[string]bool{}
	domains := map[string]bool{}
	changedPrimary := map[string]bool{}
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if got, want := r.Header.Get("Authorization"), "Bearer kinsta-token"; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites":
			if createdSite {
				_ = json.NewEncoder(w).Encode(map[string]any{"company": map[string]any{"sites": []map[string]any{{"id": "ksite123", "name": "foobar", "display_name": "foobar"}}}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"company": map[string]any{"sites": []map[string]any{}}})
		case "POST /sites":
			createdSite = true
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-create-site"})
		case "GET /operations/op-create-site", "GET /operations/op-clone-staging", "GET /operations/op-modify-live-php", "GET /operations/op-add-live-domain", "GET /operations/op-add-staging-domain", "GET /operations/op-primary-live", "GET /operations/op-primary-staging":
			_ = json.NewEncoder(w).Encode(map[string]any{"operation": map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/operations/"), "status": "complete"}})
		case "GET /sites/ksite123/environments":
			livePHP := "8.2"
			if modifiedPHP["kenv-live"] {
				livePHP = "8.3"
			}
			envs := []map[string]any{{"id": "kenv-live", "name": "foobar", "display_name": "foobar", "php_version": livePHP, "web_root": "/"}}
			if createdStaging {
				envs = append(envs, map[string]any{"id": "kenv-staging", "name": "foobar-staging", "display_name": "foobar-staging", "php_version": livePHP, "web_root": "/"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"site": map[string]any{"environments": envs}})
		case "PUT /sites/tools/modify-php-version":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("modify php decode error = %v", err)
			}
			if payload["environment_id"] != "kenv-live" || payload["php_version"] != "8.3" || payload["is_opt_out_from_automatic_php_update"] != false {
				t.Fatalf("modify php payload = %#v", payload)
			}
			modifiedPHP["kenv-live"] = true
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-modify-live-php"})
		case "GET /sites/ksite123/environments/kenv-live/ssh/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"host": "203.0.113.10", "port": "12345", "user": "foobar", "ssh_command": "ssh foobar@203.0.113.10 -p 12345"})
		case "GET /sites/ksite123/environments/kenv-staging/ssh/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"host": "203.0.113.11", "port": "12346", "user": "foobarstaging", "ssh_command": "ssh foobarstaging@203.0.113.11 -p 12346"})
		case "POST /sites/ksite123/environments/clone":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("clone environment decode error = %v", err)
			}
			if payload["display_name"] != "Staging" || payload["source_env_id"] != "kenv-live" || payload["is_premium"] != false {
				t.Fatalf("clone environment payload = %#v", payload)
			}
			createdStaging = true
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-clone-staging"})
		case "GET /sites/environments/kenv-live/domains":
			if domains["live"] {
				_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{{"id": "kdom-live", "name": "foobar.kinsta.nonfiction.dev", "is_primary": changedPrimary["live"]}}}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{}}})
		case "POST /sites/environments/kenv-live/domains":
			domains["live"] = true
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-add-live-domain"})
		case "GET /sites/environments/kenv-staging/domains":
			if domains["staging"] {
				_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{{"id": "kdom-staging", "name": "foobar-staging.kinsta.nonfiction.dev", "is_primary": changedPrimary["staging"]}}}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{}}})
		case "POST /sites/environments/kenv-staging/domains":
			domains["staging"] = true
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-add-staging-domain"})
		case "GET /sites/environments/domains/kdom-live/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"verification_records": []map[string]any{{"name": "_acme-challenge.foobar.kinsta.nonfiction.dev", "type": "TXT", "content": "live-token"}}, "pointing_records": []map[string]any{{"name": "foobar.kinsta.nonfiction.dev", "type": "A", "content": "203.0.113.10", "ttl": 300}, {"name": "www", "type": "CNAME", "content": "foobar.kinsta.nonfiction.dev"}}}})
		case "GET /sites/environments/domains/kdom-staging/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"verification_records": []map[string]any{{"name": "_acme-challenge.foobar-staging.kinsta.nonfiction.dev", "type": "TXT", "content": "staging-token"}}, "pointing_records": []map[string]any{{"name": "foobar-staging.kinsta.nonfiction.dev", "type": "CNAME", "content": "hosting.kinsta.cloud", "ttl": 300}}}})
		case "PUT /sites/environments/kenv-live/change-primary-domain":
			changedPrimary["live"] = true
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-primary-live"})
		case "PUT /sites/environments/kenv-staging/change-primary-domain":
			changedPrimary["staging"] = true
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-primary-staging"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KINSTA_BASE_URL", server.URL)

	type dnsCall struct{ name, recordType, content string }
	dnsCalls := []dnsCall{}
	oldUpsert := upsertDNSRecordFn
	upsertDNSRecordFn = func(token, accountID, zone, name, recordType, content string, ttl int) error {
		if token != "dnsimple-token" || accountID != "14" || zone != "nonfiction.dev" || ttl != 300 {
			t.Fatalf("DNS upsert args = %q %q %q ttl %d", token, accountID, zone, ttl)
		}
		dnsCalls = append(dnsCalls, dnsCall{name: name, recordType: recordType, content: content})
		return nil
	}
	t.Cleanup(func() { upsertDNSRecordFn = oldUpsert })

	result, err := provisionKinstaSite(kinstaSiteAddPlan{
		CompanyID:     "company-123",
		Site:          "foobar",
		SiteID:        "foobar.kinsta",
		Region:        "ca-toronto-1",
		AdminUser:     "admin",
		AdminEmail:    "web@nonfiction.ca",
		AdminPassword: "derived-password",
		PHPVersion:    "8.3",
		DNSZone:       "nonfiction.dev",
		DNSAccountID:  "14",
		Envs: []kinstaSiteAddEnvPlan{
			{Env: "live", Domain: "foobar.kinsta.nonfiction.dev", URL: "https://foobar.kinsta.nonfiction.dev", Branch: "main"},
			{Env: "staging", Domain: "foobar-staging.kinsta.nonfiction.dev", URL: "https://foobar-staging.kinsta.nonfiction.dev", Branch: "develop"},
		},
	})
	if err != nil {
		t.Fatalf("provisionKinstaSite() error = %v", err)
	}
	if result.CompanyID != "company-123" || result.SiteID != "ksite123" || len(result.Envs) != 2 {
		t.Fatalf("result = %#v, want company/site/envs", result)
	}
	for _, want := range []struct{ env, path, database, sshHost, sshPort, sshUser string }{
		{"live", "/www/foobar/public", "foobar", "203.0.113.10", "12345", "foobar"},
		{"staging", "/www/foobarstaging/public", "foobarstaging", "203.0.113.11", "12346", "foobarstaging"},
	} {
		found := false
		for _, got := range result.Envs {
			if got.Env != want.env {
				continue
			}
			found = true
			if got.Path != want.path || got.Database != want.database || got.SSHHost != want.sshHost || got.SSHPort != want.sshPort || got.SSHUser != want.sshUser {
				t.Fatalf("%s env metadata = %#v, want path/db/ssh %#v", want.env, got, want)
			}
		}
		if !found {
			t.Fatalf("missing result env %s in %#v", want.env, result.Envs)
		}
	}
	if !createdSite || !createdStaging || !modifiedPHP["kenv-live"] || !changedPrimary["live"] || !changedPrimary["staging"] {
		t.Fatalf("flow flags: createdSite=%v createdStaging=%v modifiedPHP=%#v changedPrimary=%#v", createdSite, createdStaging, modifiedPHP, changedPrimary)
	}
	for _, want := range []dnsCall{
		{"_acme-challenge.foobar.kinsta", "TXT", "live-token"},
		{"foobar.kinsta", "A", "203.0.113.10"},
		{"_acme-challenge.foobar-staging.kinsta", "TXT", "staging-token"},
		{"foobar-staging.kinsta", "CNAME", "hosting.kinsta.cloud"},
	} {
		found := false
		for _, got := range dnsCalls {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing DNS call %#v in %#v", want, dnsCalls)
		}
	}
	for _, got := range dnsCalls {
		if got.name == "www" {
			t.Fatalf("unexpected apex www DNS call in %#v", dnsCalls)
		}
	}
	for _, want := range []string{"POST /sites", "PUT /sites/tools/modify-php-version", "POST /sites/ksite123/environments/clone", "POST /sites/environments/kenv-live/domains", "POST /sites/environments/kenv-staging/domains", "GET /sites/ksite123/environments/kenv-live/ssh/config", "GET /sites/ksite123/environments/kenv-staging/ssh/config", "PUT /sites/environments/kenv-live/change-primary-domain", "PUT /sites/environments/kenv-staging/change-primary-domain"} {
		found := false
		for _, got := range requests {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing request %q in %#v", want, requests)
		}
	}
}

func TestRunSiteRemoveLinodeDryRunPlansEnvDeletion(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "linode", "site_id": "foobar.app1-linode", "env_id": "foobar.app1-linode:live", "name": "foobar", "env": "live", "target": "app1-linode", "path": "/var/www/sites/foobar/public", "database": "foobar", "hostname": "foobar.app1-linode.nonfiction.dev"},
		{"provider": "linode", "site_id": "foobar.app1-linode", "env_id": "foobar.app1-linode:staging", "name": "foobar", "env": "staging", "target": "app1-linode", "path": "/var/www/sites/foobar_staging/public", "database": "foobar_staging", "hostname": "foobar-staging.app1-linode.nonfiction.dev"},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	oldRunSSH := runSSHScriptFn
	runSSHScriptFn = func(user, host, script string) error {
		t.Fatalf("runSSHScriptFn called during dry-run")
		return nil
	}
	t.Cleanup(func() { runSSHScriptFn = oldRunSSH })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "remove", "foobar", "--dry-run", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site remove dry-run) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Remove site plan:", "site id: foobar.app1-linode", "target: app1-linode", "dns actions: none", "env live:", "env id: foobar.app1-linode:live", "delete path: /var/www/sites/foobar/public", "drop database: foobar", "env staging:", "env id: foobar.app1-linode:staging", "delete path: /var/www/sites/foobar_staging/public", "drop database: foobar_staging", "mode: dry-run"} {
		if !strings.Contains(output, want) {
			t.Fatalf("site remove dry-run output missing %q:\n%s", want, output)
		}
	}
}

func TestRunSiteRemoveLinodeAllowsLegacySiteRootPath(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "foobar", "env_id": "foobar", "name": "foobar", "env": "live", "target": "app1-linode", "path": "/var/www/sites/foobar", "database": "foobar", "hostname": "foobar.app1-linode.nonfiction.dev"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	oldRunSSH := runSSHScriptFn
	runSSHScriptFn = func(user, host, script string) error {
		t.Fatalf("runSSHScriptFn called during dry-run")
		return nil
	}
	t.Cleanup(func() { runSSHScriptFn = oldRunSSH })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "remove", "foobar", "--dry-run", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site remove dry-run) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Remove site plan:", "site id: foobar", "target: app1-linode", "delete path: /var/www/sites/foobar", "drop database: foobar", "mode: dry-run"} {
		if !strings.Contains(output, want) {
			t.Fatalf("site remove legacy path output missing %q:\n%s", want, output)
		}
	}
}

func TestRunSiteRemoveLinodeExecuteRunsSSHAndRemovesCache(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "linode", "site_id": "foobar.app1-linode", "env_id": "foobar.app1-linode:live", "name": "foobar", "env": "live", "target": "app1-linode", "path": "/var/www/sites/foobar/public", "database": "foobar", "hostname": "foobar.app1-linode.nonfiction.dev"},
		{"provider": "linode", "site_id": "foobar.app1-linode", "env_id": "foobar.app1-linode:staging", "name": "foobar", "env": "staging", "target": "app1-linode", "path": "/var/www/sites/foobar_staging/public", "database": "foobar_staging", "hostname": "foobar-staging.app1-linode.nonfiction.dev"},
		{"provider": "linode", "site_id": "other.app1-linode", "env_id": "other.app1-linode", "name": "other", "env": "live", "target": "app1-linode", "path": "/var/www/sites/other/public", "database": "other"},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	var sshUser, sshHost, sshScript string
	oldRunSSH := runSSHScriptFn
	runSSHScriptFn = func(user, host, script string) error {
		sshUser, sshHost, sshScript = user, host, script
		return nil
	}
	t.Cleanup(func() { runSSHScriptFn = oldRunSSH })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "remove", "foobar.app1-linode", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site remove execute) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Site removed.") || !strings.Contains(output, "mode: execute") {
		t.Fatalf("site remove execute output = %q, want success", output)
	}
	if sshUser != "nonfiction" || sshHost != "app1-linode.nonfiction.dev" {
		t.Fatalf("ssh target = %s@%s, want nonfiction@app1-linode.nonfiction.dev", sshUser, sshHost)
	}
	for _, want := range []string{"rm -rf -- \"$site_path\"", "DROP DATABASE IF EXISTS \\`$db_name\\`;", "DROP USER IF EXISTS '$db_name'@'localhost';", "remove_env foobar.app1-linode:live foobar.app1-linode.live /var/www/sites/foobar/public foobar", "remove_env foobar.app1-linode:staging foobar.app1-linode.staging /var/www/sites/foobar_staging/public foobar_staging", "nf-site-$file_slug", "nf-site-$env_id", "$file_slug.access.log", "$env_id.access.log", "jq --arg site_id foobar.app1-linode", "nginx -t", "systemctl reload nginx"} {
		if !strings.Contains(sshScript, want) {
			t.Fatalf("ssh script missing %q:\n%s", want, sshScript)
		}
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(records) != 1 || siteRecordID(records[0]) != "other.app1-linode" {
		t.Fatalf("site cache after remove = %#v, want only other.app1-linode", records)
	}
}

func TestRunSiteRemoveKinstaDryRunPlansEnvAndSiteDeletion(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev", "dnsimple_account_id": "14"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "kinsta", "targets": []map[string]any{{"name": "kinsta", "provider": "kinsta", "company_id": "company-123"}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "kinsta", "site_id": "foobar.kinsta", "name": "foobar", "env": "live", "target": "kinsta", "hostname": "foobar.kinsta.nonfiction.dev", "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-live", "domain_id": "kdom-live"}},
		{"provider": "kinsta", "site_id": "foobar.kinsta", "name": "foobar", "env": "staging", "target": "kinsta", "hostname": "foobar-staging.kinsta.nonfiction.dev", "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-staging", "domain_id": "kdom-staging"}},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	oldRemove := kinstaRemoveSiteFn
	kinstaRemoveSiteFn = func(plan siteRemovePlan) error {
		t.Fatalf("kinstaRemoveSiteFn called during dry-run")
		return nil
	}
	t.Cleanup(func() { kinstaRemoveSiteFn = oldRemove })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "remove", "foobar.kinsta", "--dry-run", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site remove kinsta dry-run) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Remove site plan:", "site id: foobar.kinsta", "target: kinsta", "provider: kinsta", "kinsta site id: ksite123", "dns: dnsimple zone nonfiction.dev account 14", "dns delete: A foobar.kinsta.nonfiction.dev", "dns delete: TXT _acme-challenge.foobar.kinsta.nonfiction.dev", "dns delete: A foobar-staging.kinsta.nonfiction.dev", "dns delete: TXT _acme-challenge.foobar-staging.kinsta.nonfiction.dev", "env live:", "kinsta environment id: kenv-live", "env staging:", "kinsta environment id: kenv-staging", "remote actions: delete Kinsta environments, delete Kinsta site", "mode: dry-run"} {
		if !strings.Contains(output, want) {
			t.Fatalf("site remove kinsta dry-run output missing %q:\n%s", want, output)
		}
	}
}

func TestRunSiteRemoveKinstaExecuteDeletesRemoteAndCache(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("KINSTA_API_KEY", "kinsta-token")
	t.Setenv("DNSIMPLE_TOKEN", "dns-token")
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev", "dnsimple_account_id": "14"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "kinsta", "site_id": "foobar.kinsta", "name": "foobar", "env": "live", "target": "kinsta", "hostname": "foobar.kinsta.nonfiction.dev", "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-live", "domain_id": "kdom-live"}},
		{"provider": "kinsta", "site_id": "foobar.kinsta", "name": "foobar", "env": "staging", "target": "kinsta", "hostname": "foobar-staging.kinsta.nonfiction.dev", "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-staging", "domain_id": "kdom-staging"}},
		{"provider": "kinsta", "site_id": "other.kinsta", "name": "other", "env": "live", "target": "kinsta", "kinsta": map[string]any{"site_id": "ksite-other", "environment_id": "kenv-other"}},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if got, want := r.Header.Get("Authorization"), "Bearer kinsta-token"; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/environments/domains/kdom-live/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"pointing_records": []map[string]any{{"name": "foobar.kinsta.nonfiction.dev", "type": "A", "content": "203.0.113.10"}}, "verification_records": []map[string]any{{"name": "_acme-challenge.foobar.kinsta.nonfiction.dev", "type": "TXT", "content": "token"}, {"name": "_kinsta.foobar.kinsta.nonfiction.dev", "type": "TXT", "content": "token"}}}})
		case "GET /sites/environments/domains/kdom-staging/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"pointing_records": []map[string]any{{"name": "foobar-staging.kinsta.nonfiction.dev", "type": "A", "content": "203.0.113.11"}}, "verification_records": []map[string]any{{"name": "_acme-challenge.foobar-staging.kinsta.nonfiction.dev", "type": "TXT", "content": "token"}}}})
		case "DELETE /sites/environments/kenv-live":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-delete-live", "status": 202})
		case "DELETE /sites/environments/kenv-staging":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-delete-staging", "status": 202})
		case "DELETE /sites/ksite123":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-delete-site", "status": 202})
		case "GET /operations/op-delete-live", "GET /operations/op-delete-staging", "GET /operations/op-delete-site":
			_ = json.NewEncoder(w).Encode(map[string]any{"operation": map[string]any{"status": "complete"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KINSTA_BASE_URL", server.URL)
	type dnsCall struct{ kind, token, accountID, zone, name string }
	dnsCalls := []dnsCall{}
	oldDNSDelete := deleteDNSRecordFn
	deleteDNSRecordFn = func(token, accountID, zone, name string) error {
		dnsCalls = append(dnsCalls, dnsCall{kind: "A", token: token, accountID: accountID, zone: zone, name: name})
		return nil
	}
	oldTXTDelete := deleteDNSTXTRecordFn
	deleteDNSTXTRecordFn = func(token, accountID, zone, name string) error {
		dnsCalls = append(dnsCalls, dnsCall{kind: "TXT", token: token, accountID: accountID, zone: zone, name: name})
		return nil
	}
	t.Cleanup(func() {
		deleteDNSRecordFn = oldDNSDelete
		deleteDNSTXTRecordFn = oldTXTDelete
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "remove", "foobar.kinsta", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site remove kinsta execute) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Site removed.") || !strings.Contains(output, "mode: execute") {
		t.Fatalf("site remove kinsta execute output = %q, want success", output)
	}
	for _, want := range []dnsCall{
		{kind: "A", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "foobar.kinsta"},
		{kind: "TXT", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "_acme-challenge.foobar.kinsta"},
		{kind: "TXT", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "_kinsta.foobar.kinsta"},
		{kind: "A", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "foobar-staging.kinsta"},
		{kind: "TXT", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "_acme-challenge.foobar-staging.kinsta"},
	} {
		found := false
		for _, got := range dnsCalls {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing DNS delete %#v in %#v", want, dnsCalls)
		}
	}
	for _, want := range []string{"GET /sites/environments/domains/kdom-live/verification-records", "GET /sites/environments/domains/kdom-staging/verification-records", "DELETE /sites/environments/kenv-live", "GET /operations/op-delete-live", "DELETE /sites/environments/kenv-staging", "GET /operations/op-delete-staging", "DELETE /sites/ksite123", "GET /operations/op-delete-site"} {
		found := false
		for _, got := range requests {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing Kinsta request %q in %#v", want, requests)
		}
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(records) != 1 || siteRecordID(records[0]) != "other.kinsta" {
		t.Fatalf("site cache after kinsta remove = %#v, want only other.kinsta", records)
	}
}

func TestRunSiteRemoveWithoutArgUsesPicker(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "foobar.app1-linode", "env_id": "foobar.app1-linode", "name": "foobar", "env": "live", "target": "app1-linode", "path": "/var/www/sites/foobar/public", "database": "foobar"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	var prompt string
	var options []ui.SelectOption
	oldSelect := siteSelectFn
	siteSelectFn = func(p string, opts []ui.SelectOption) (string, error) {
		prompt = p
		options = append([]ui.SelectOption(nil), opts...)
		return "foobar.app1-linode", nil
	}
	oldRunSSH := runSSHScriptFn
	runSSHScriptFn = func(user, host, script string) error { return nil }
	t.Cleanup(func() {
		siteSelectFn = oldSelect
		runSSHScriptFn = oldRunSSH
	})

	if got := Run([]string{"site", "remove", "--execute", "--yes"}); got != 0 {
		t.Fatalf("Run(site remove picker) = %d, want 0", got)
	}
	if prompt != "Choose a site to remove" {
		t.Fatalf("picker prompt = %q, want Choose a site to remove", prompt)
	}
	if len(options) != 1 || options[0] != (ui.SelectOption{Value: "foobar.app1-linode", Label: "foobar.app1-linode"}) {
		t.Fatalf("picker options = %#v, want foobar.app1-linode", options)
	}
}

func TestRunSiteRemoveRejectsEnvRef(t *testing.T) {
	stderr := captureStderr(t, func() {
		if got := Run([]string{"site", "remove", "foobar.app1-linode:staging", "--dry-run"}); got != 1 {
			t.Fatalf("Run(site remove env) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, `Cannot remove one env; remove site "foobar.app1-linode" to delete live and staging.`) {
		t.Fatalf("site remove env stderr = %q", stderr)
	}
}

func TestRunSiteListEnvsAndShowEnvUseCachedSites(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	sites := map[string]any{"sites": map[string]any{
		"live-client-kinsta":    map[string]any{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "php_version": "8.3", "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-live", "branch": "main"}},
		"staging-client-kinsta": map[string]any{"provider": "kinsta", "site_id": "client-kinsta", "env": "staging", "url": "https://staging.example.com/", "php_version": "8.3", "ssh": map[string]any{"host": "203.0.113.11", "port": "12346", "user": "clientstaging", "command": "ssh clientstaging@203.0.113.11 -p 12346"}, "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-staging", "branch": "develop"}},
	}}
	data, err := json.MarshalIndent(sites, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "sites.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(sites) error = %v", err)
	}

	listOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "list", "--envs", "client-kinsta"}); got != 0 {
			t.Fatalf("Run(site list --envs) = %d, want 0", got)
		}
	})
	for _, want := range []string{"env id", "site", "env", "php", "url", "client-kinsta:live", "client-kinsta:staging", "live", "staging", "client-kinsta", "8.3", "https://www.example.com/", "https://staging.example.com/"} {
		if !strings.Contains(listOutput, want) {
			t.Fatalf("site list --envs output missing %q:\n%s", want, listOutput)
		}
	}
	for _, notWant := range []string{"target", "provider", "branch", "develop"} {
		if strings.Contains(listOutput, notWant) {
			t.Fatalf("site list --envs output contains %q:\n%s", notWant, listOutput)
		}
	}

	showOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "show", "client-kinsta:staging"}); got != 0 {
			t.Fatalf("Run(site show env) = %d, want 0", got)
		}
	})
	for _, want := range []string{"client-kinsta:staging", "Site       client-kinsta", "Env        staging", "Provider   kinsta", "Target     kinsta", "URL        https://staging.example.com/", "PHP        8.3", "Provider IDs", "Kinsta env    kenv-staging", "Access", "SSH command   ssh clientstaging@203.0.113.11 -p 12346", "Branch     develop"} {
		if !strings.Contains(showOutput, want) {
			t.Fatalf("site show env output missing %q:\n%s", want, showOutput)
		}
	}
	for _, notWant := range []string{"SSH host", "SSH port", "SSH user", "SSH address"} {
		if strings.Contains(showOutput, notWant) {
			t.Fatalf("site show env output contains %q:\n%s", notWant, showOutput)
		}
	}
	jsonOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "show", "client-kinsta:staging", "--json"}); got != 0 {
			t.Fatalf("Run(site show env --json) = %d, want 0", got)
		}
	})
	for _, want := range []string{`"requested_site": "client-kinsta"`, `"requested_env": "staging"`, `"resolved_site": "client-kinsta"`, `"resolved_env": "staging"`, `"php_version": "8.3"`, `"ssh_host": "203.0.113.11"`, `"ssh_port": "12346"`, `"ssh_user": "clientstaging"`, `"kinsta_environment_id": "kenv-staging"`} {
		if !strings.Contains(jsonOutput, want) {
			t.Fatalf("site show env --json output missing %q:\n%s", want, jsonOutput)
		}
	}
}

func TestRunSiteShowEnvLinodeUsesCachedTargets(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app2-linode", "provider": "linode", "hostname": "app2-linode.nonfiction.dev", "php": map[string]any{"version": "8.3"}, "ssh": map[string]any{"user": "nonfiction", "host": "app2-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "linode", "site_id": "happytents.app2-linode", "name": "happytents", "env": "live", "target": "app2-linode", "url": "https://happytents.app2-linode.nonfiction.dev", "hostname": "happytents.app2-linode.nonfiction.dev", "php_version": "8.3"},
		{"provider": "linode", "site_id": "happytents.app2-linode", "name": "happytents", "env": "staging", "target": "app2-linode", "url": "https://happytents-staging.app2-linode.nonfiction.dev", "hostname": "happytents-staging.app2-linode.nonfiction.dev", "php_version": "8.3"},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	showOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "show", "happytents.app2-linode:staging"}); got != 0 {
			t.Fatalf("Run(site show env) = %d, want 0", got)
		}
	})
	adminPassword := passwords.DerivePassword("happytents", "wp-admin", "test-salt")
	for _, want := range []string{"happytents.app2-linode:staging", "Site       happytents.app2-linode", "Env        staging", "Provider   linode", "Target     app2-linode", "URL        https://happytents-staging.app2-linode.nonfiction.dev", "PHP        8.3", "SSH command   ssh nonfiction@app2-linode.nonfiction.dev", "Admin user    admin", "Admin pass    " + adminPassword} {
		if !strings.Contains(showOutput, want) {
			t.Fatalf("site show env output missing %q:\n%s", want, showOutput)
		}
	}
	for _, notWant := range []string{"Hostname:", "Target summary:", "SSH host", "SSH port", "SSH user", "SSH address"} {
		if strings.Contains(showOutput, notWant) {
			t.Fatalf("site show env output contains %q:\n%s", notWant, showOutput)
		}
	}
	jsonOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "show", "happytents.app2-linode:staging", "--json"}); got != 0 {
			t.Fatalf("Run(site show env --json) = %d, want 0", got)
		}
	})
	for _, want := range []string{`"resolved_site": "happytents.app2-linode"`, `"resolved_env": "staging"`, `"resolved_target": "app2-linode"`, `"php_version": "8.3"`, `"resolved_admin_user": "admin"`, `"resolved_admin_password": "` + adminPassword + `"`, `"resolved_target_summary": "app2-linode / linode / ssh nonfiction@app2-linode.nonfiction.dev"`} {
		if !strings.Contains(jsonOutput, want) {
			t.Fatalf("site show env --json output missing %q:\n%s", want, jsonOutput)
		}
	}
}

func TestRunSitePasswordPrintsAdminPasswordOnly(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "linode", "site_id": "happytents.app2-linode", "name": "happytents", "env": "live", "target": "app2-linode", "url": "https://happytents.app2-linode.nonfiction.dev"},
		{"provider": "linode", "site_id": "happytents.app2-linode", "name": "happytents", "env": "staging", "target": "app2-linode", "url": "https://happytents-staging.app2-linode.nonfiction.dev"},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	want := passwords.DerivePassword("happytents", "wp-admin", "test-salt") + "\n"
	output := captureStdout(t, func() {
		if got := Run([]string{"site", "password", "happytents.app2-linode"}); got != 0 {
			t.Fatalf("Run(site password) = %d, want 0", got)
		}
	})
	if output != want {
		t.Fatalf("site password output = %q, want %q", output, want)
	}
}

func TestRunSitePasswordRejectsEnvRef(t *testing.T) {
	stderr := captureStderr(t, func() {
		if got := Run([]string{"site", "password", "happytents.app2-linode:staging"}); got != 1 {
			t.Fatalf("Run(site password env) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, `site password takes a site, not an env; use "happytents.app2-linode".`) {
		t.Fatalf("site password env stderr = %q", stderr)
	}
}

func TestRunSitePasswordWithoutSitePromptsPicker(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "client.app1-linode", "name": "client", "env": "live", "target": "app1-linode"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	oldSelect := siteSelectFn
	var selectTitle string
	var selectOptions []ui.SelectOption
	siteSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectTitle = title
		selectOptions = append([]ui.SelectOption(nil), options...)
		return "client.app1-linode", nil
	}
	t.Cleanup(func() { siteSelectFn = oldSelect })

	want := passwords.DerivePassword("client", "wp-admin", "test-salt") + "\n"
	output := captureStdout(t, func() {
		if got := Run([]string{"site", "password"}); got != 0 {
			t.Fatalf("Run(site password) = %d, want 0", got)
		}
	})
	if selectTitle != "Choose a site to show password for" {
		t.Fatalf("select title = %q", selectTitle)
	}
	if len(selectOptions) != 1 || selectOptions[0] != (ui.SelectOption{Value: "client.app1-linode", Label: "client.app1-linode"}) {
		t.Fatalf("select options = %#v", selectOptions)
	}
	if output != want {
		t.Fatalf("site password output = %q, want %q", output, want)
	}
}

func TestRunSiteListEnvsWithoutSiteListsAll(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	sites := map[string]any{"sites": map[string]any{
		"live-client-kinsta":    map[string]any{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/"},
		"staging-client-kinsta": map[string]any{"provider": "kinsta", "site_id": "client-kinsta", "env": "staging", "url": "https://staging.example.com/"},
	}}
	data, err := json.MarshalIndent(sites, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "sites.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(sites) error = %v", err)
	}
	output := captureStdout(t, func() {
		if got := Run([]string{"site", "list", "--envs"}); got != 0 {
			t.Fatalf("Run(site list --envs) = %d, want 0", got)
		}
	})
	for _, want := range []string{"env id", "site", "env", "php", "url", "client-kinsta:live", "client-kinsta:staging", "live", "staging", "client-kinsta", "https://www.example.com/", "https://staging.example.com/"} {
		if !strings.Contains(output, want) {
			t.Fatalf("site list --envs output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "target") {
		t.Fatalf("site list --envs output contains target column:\n%s", output)
	}
}

func TestRunSiteShellSiteRefPromptsEnvPicker(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}},
		{"provider": "kinsta", "site_id": "client-kinsta", "env": "staging", "url": "https://staging.example.com/", "path": "/www/clientstaging/public", "ssh": map[string]any{"host": "203.0.113.11", "port": "12346", "user": "clientstaging"}},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	oldSelect := siteSelectFn
	oldInteractive := siteIsInteractiveFn
	oldRunSSHCommand := runSSHCommandFn
	var selectTitle string
	var selectOptions []ui.SelectOption
	siteSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectTitle = title
		selectOptions = append([]ui.SelectOption(nil), options...)
		return "client-kinsta:staging", nil
	}
	siteIsInteractiveFn = func() bool { return true }
	runSSHCommandFn = func(args []string) error { return nil }
	t.Cleanup(func() {
		siteSelectFn = oldSelect
		siteIsInteractiveFn = oldInteractive
		runSSHCommandFn = oldRunSSHCommand
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "shell", "client-kinsta"}); got != 0 {
			t.Fatalf("Run(site shell site ref) = %d, want 0", got)
		}
	})
	if selectTitle != "Choose a remote env to shell" {
		t.Fatalf("select title = %q", selectTitle)
	}
	if len(selectOptions) != 2 || selectOptions[0].Value != "client-kinsta:live" || selectOptions[1].Value != "client-kinsta:staging" {
		t.Fatalf("select options = %#v", selectOptions)
	}
	for _, want := range []string{"Site shell preflight:", "site:     client-kinsta", "env:      staging", "url:      https://staging.example.com/", "> ssh -t -p 12346 clientstaging@203.0.113.11"} {
		if !strings.Contains(output, want) {
			t.Fatalf("site shell output missing %q:\n%s", want, output)
		}
	}
}

func TestRunSiteShellAndWpRunSSHForKinsta(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	sites := map[string]any{"sites": map[string]any{
		"live-client-kinsta":    map[string]any{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "path": "/www/client/public/www/client_123/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client", "command": "ssh client@203.0.113.10 -p 12345"}, "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-live"}},
		"staging-client-kinsta": map[string]any{"provider": "kinsta", "site_id": "client-kinsta", "env": "staging", "url": "https://staging.example.com/", "path": "/www/clientstaging/public", "ssh": map[string]any{"host": "203.0.113.11", "port": "12346", "user": "clientstaging"}, "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-staging"}},
	}}
	data, err := json.MarshalIndent(sites, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(sites) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "sites.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(sites) error = %v", err)
	}

	var commands [][]string
	oldRunSSHCommand := runSSHCommandFn
	runSSHCommandFn = func(args []string) error {
		commands = append(commands, append([]string(nil), args...))
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSHCommand })

	shellOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "shell", "client-kinsta:live"}); got != 0 {
			t.Fatalf("Run(site shell) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Site shell preflight:", "site:     client-kinsta", "env:      live", "provider: kinsta", "url:      https://www.example.com/", "> ssh -t -p 12345 client@203.0.113.10", "cd /www/client_123/public"} {
		if !strings.Contains(shellOutput, want) {
			t.Fatalf("site shell stdout missing %q:\n%s", want, shellOutput)
		}
	}

	wpOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "wp", "client-kinsta:staging", "--", "plugin", "list"}); got != 0 {
			t.Fatalf("Run(site wp) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Site wp preflight:", "site:     client-kinsta", "env:      staging", "provider: kinsta", "wp args:  plugin list", "> ssh -p 12346 clientstaging@203.0.113.11", "wp --path=/www/clientstaging/public plugin list"} {
		if !strings.Contains(wpOutput, want) {
			t.Fatalf("site wp stdout missing %q:\n%s", want, wpOutput)
		}
	}
	if len(commands) != 2 {
		t.Fatalf("commands len = %d, want 2: %#v", len(commands), commands)
	}
}

func TestRunSiteShellWithoutEnvPromptsPicker(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	oldSelect := siteSelectFn
	oldInteractive := siteIsInteractiveFn
	var selectTitle string
	var selectOptions []ui.SelectOption
	siteSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectTitle = title
		selectOptions = append([]ui.SelectOption(nil), options...)
		return "client-kinsta:live", nil
	}
	siteIsInteractiveFn = func() bool { return true }
	t.Cleanup(func() {
		siteSelectFn = oldSelect
		siteIsInteractiveFn = oldInteractive
	})

	oldRunSSHCommand := runSSHCommandFn
	runSSHCommandFn = func(args []string) error { return nil }
	t.Cleanup(func() { runSSHCommandFn = oldRunSSHCommand })
	stdout := captureStdout(t, func() {
		if got := Run([]string{"site", "shell"}); got != 0 {
			t.Fatalf("Run(site shell) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Site shell preflight:", "site:     client-kinsta", "env:      live", "provider: kinsta", "url:      https://www.example.com/", "> ssh -t -p 12345 client@203.0.113.10"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("site shell stdout missing %q:\n%s", want, stdout)
		}
	}

	if selectTitle != "Choose a remote env to shell" {
		t.Fatalf("select title = %q", selectTitle)
	}
	if len(selectOptions) != 1 || selectOptions[0].Value != "client-kinsta:live" {
		t.Fatalf("select options = %#v", selectOptions)
	}
}

func TestRunSiteWpWithoutArgsPrintsError(t *testing.T) {
	for _, argv := range [][]string{
		{"site", "wp"},
		{"site", "wp", "--staging", "plugin", "list"},
	} {
		stderr := captureStderr(t, func() {
			if got := Run(argv); got != 1 {
				t.Fatalf("Run(%v) = %d, want 1", argv, got)
			}
		})
		if !strings.Contains(stderr, "site wp requires an env ref and wp-cli command") {
			t.Fatalf("Run(%v) stderr = %q", argv, stderr)
		}
	}
}

func TestRunSiteShellAndWpRunSSHForLinode(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "linode", "site_id": "foobar.app1-linode", "env_id": "foobar.app1-linode:live", "name": "foobar", "env": "live", "target": "app1-linode", "hostname": "foobar.app1-linode.nonfiction.dev", "url": "https://foobar.app1-linode.nonfiction.dev", "path": "/var/www/sites/foobar/public", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev", "port": "22"}},
		{"provider": "linode", "site_id": "foobar.app1-linode", "env_id": "foobar.app1-linode:staging", "name": "foobar", "env": "staging", "target": "app1-linode", "hostname": "foobar-staging.app1-linode.nonfiction.dev", "url": "https://foobar-staging.app1-linode.nonfiction.dev", "path": "/var/www/sites/foobar_staging/public", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev", "port": "22"}},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	var commands [][]string
	oldRunSSHCommand := runSSHCommandFn
	runSSHCommandFn = func(args []string) error {
		commands = append(commands, append([]string(nil), args...))
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSHCommand })

	shellOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "shell", "foobar.app1-linode:live"}); got != 0 {
			t.Fatalf("Run(site shell) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Site shell preflight:", "env:      live", "target:   app1-linode", "> ssh -t -p 22 nonfiction@app1-linode.nonfiction.dev", "cd /var/www/sites/foobar/public"} {
		if !strings.Contains(shellOutput, want) {
			t.Fatalf("site shell output missing %q:\n%s", want, shellOutput)
		}
	}
	if len(commands) != 1 {
		t.Fatalf("shell command = %#v", commands)
	}
	shellCommand := strings.Join(commands[0], " ")
	for _, want := range []string{"ssh -t -p 22 nonfiction@app1-linode.nonfiction.dev", "cd /var/www/sites/foobar/public", "exec ${SHELL:-/bin/bash} -i"} {
		if !strings.Contains(shellCommand, want) {
			t.Fatalf("shell command missing %q: %#v", want, commands[0])
		}
	}

	wpOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "wp", "foobar.app1-linode:staging", "plugin", "list"}); got != 0 {
			t.Fatalf("Run(site wp) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Site wp preflight:", "env:      staging", "wp args:  plugin list", "> ssh -p 22 nonfiction@app1-linode.nonfiction.dev"} {
		if !strings.Contains(wpOutput, want) {
			t.Fatalf("site wp output missing %q:\n%s", want, wpOutput)
		}
	}
	if len(commands) != 2 {
		t.Fatalf("commands len = %d, want 2: %#v", len(commands), commands)
	}
	wpCommand := strings.Join(commands[1], " ")
	for _, want := range []string{"ssh -p 22 nonfiction@app1-linode.nonfiction.dev", "cd /var/www/sites/foobar_staging/public", "sudo -u www-data wp --path=/var/www/sites/foobar_staging/public plugin list"} {
		if !strings.Contains(wpCommand, want) {
			t.Fatalf("wp command missing %q: %#v", want, commands[1])
		}
	}
}

func TestRunEnvPushPreflightsRepoRemoteWithoutSyncing(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_DATA_HOME", t.TempDir())
	sites := map[string]any{"sites": map[string]any{"live-client-kinsta": map[string]any{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "user": "client", "port": "12345"}, "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-live"}}}}
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
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), []byte("{\n  \"version\": 1\n}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	project := map[string]any{
		"version":   1,
		"project":   map[string]any{"slug": "client", "name": "Client"},
		"wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"},
		"env":       map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
		"remotes":   map[string]any{"production": "client-kinsta:live"},
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
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
			if got := Run([]string{"env", "push", "production", "--dry-run"}); got != 0 {
				t.Fatalf("Run(env push) = %d, want 0 for preflight", got)
			}
		})
		for _, want := range []string{"Env push preflight:", "local project: client", "remote:        production", "site:          client-kinsta", "env:           live", "provider:      kinsta", "url:           https://www.example.com/", "environment ssh: client@203.0.113.10"} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("env push stdout missing %q:\n%s", want, stdout)
			}
		}
		if strings.Contains(stdout, "target record:") || strings.Contains(stdout, "target:        kinsta") {
			t.Fatalf("env push stdout used target wording for Kinsta:\n%s", stdout)
		}
	})
	if stderr != "" {
		t.Fatalf("env push stderr = %q", stderr)
	}
}

func TestRunEnvPullPreflightResolvesLinodeTargetFromProvidersCache(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_DATA_HOME", t.TempDir())
	providers := []map[string]any{{
		"provider": "linode",
		"targets": []map[string]any{{
			"name":     "app4-linode",
			"provider": "linode",
			"hostname": "app4-linode.nonfiction.dev",
			"ssh":      map[string]any{"user": "nonfiction", "host": "app4-linode.nonfiction.dev", "port": "22"},
		}},
	}}
	if err := state.SaveStateRecords("providers", providers); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	sites := []map[string]any{{
		"provider": "linode",
		"site_id":  "foobar.app4-linode",
		"env":      "live",
		"target":   "app4-linode",
		"hostname": "foobar.app4-linode.nonfiction.dev",
		"url":      "https://foobar.app4-linode.nonfiction.dev",
		"path":     "/var/www/sites/foobar_live/public",
	}}
	if err := state.SaveStateRecords("sites", sites); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	repoRoot := t.TempDir()
	for _, dir := range []string{".git"} {
		if err := os.MkdirAll(filepath.Join(repoRoot, dir), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	project := map[string]any{
		"version":   1,
		"project":   map[string]any{"slug": "foobar", "name": "FooBar"},
		"wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"},
		"env":       map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
		"remotes":   map[string]any{"live": "foobar.app4-linode:live"},
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
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
			if got := Run([]string{"env", "pull", "live", "--dry-run"}); got != 0 {
				t.Fatalf("Run(env pull) = %d, want 0 for preflight", got)
			}
		})
		for _, want := range []string{"Env pull preflight:", "local project: foobar", "remote:        live", "site:          foobar.app4-linode", "provider:      linode", "target:        app4-linode", "target record: app4-linode", "mode:          dry-run", "No data was changed."} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("env pull stdout missing %q:\n%s", want, stdout)
			}
		}
	})
	if stderr != "" {
		t.Fatalf("env pull stderr = %q", stderr)
	}

	oldRemoteSelect := remoteSelectFn
	selectedTitle := ""
	remoteSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectedTitle = title
		if len(options) != 1 || options[0].Value != "live" {
			t.Fatalf("remote picker options = %#v, want live", options)
		}
		return "live", nil
	}
	t.Cleanup(func() { remoteSelectFn = oldRemoteSelect })
	stdout := captureStdout(t, func() {
		if got := Run([]string{"env", "pull", "--dry-run"}); got != 0 {
			t.Fatalf("Run(env pull --dry-run) = %d, want 0", got)
		}
	})
	if selectedTitle != "Choose a remote to pull" || !strings.Contains(stdout, "remote:        live") {
		t.Fatalf("env pull picker title/output = %q /\n%s", selectedTitle, stdout)
	}
}

func TestRunThemeDeployDryRunPlansPackagedReleaseToConfiguredRemote(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	repoRoot := t.TempDir()
	for _, dir := range []string{".git", "build/theme"} {
		if err := os.MkdirAll(filepath.Join(repoRoot, dir), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "build", "theme", "style.css"), []byte("/*\nTheme Name: Theme\nVersion: 1.2.3\n*/\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(style.css) error = %v", err)
	}
	project := map[string]any{
		"version":   1,
		"project":   map[string]any{"slug": "client", "name": "Client"},
		"wordpress": map[string]any{"theme_path": "build/theme", "theme_slug": "theme"},
		"remotes":   map[string]any{"production": "client-kinsta:live"},
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
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

	oldRunRsync := runRsyncCommandFn
	runRsyncCommandFn = func(args []string) error {
		t.Fatalf("runRsyncCommandFn called during dry-run: %#v", args)
		return nil
	}
	t.Cleanup(func() { runRsyncCommandFn = oldRunRsync })
	oldRunSSH := runSSHCommandFn
	runSSHCommandFn = func(args []string) error {
		t.Fatalf("runSSHCommandFn called during dry-run: %#v", args)
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })

	stdout := captureStdout(t, func() {
		if got := Run([]string{"theme", "deploy", "production", "--dry-run"}); got != 0 {
			t.Fatalf("Run(theme deploy --dry-run) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Theme deploy plan:", "remote:      production", "site:        client-kinsta", "env:         live", "provider:    kinsta", "source:      " + filepath.Join(repoRoot, "build", "theme"), "artifact:    " + filepath.Join(repoRoot, "dist", "client-v1.2.3.zip"), "release id:  v1.2.3-", "release dir: /www/client/public/wp-content/themes/.nf-releases/theme/v1.2.3-", "active dir:  /www/client/public/wp-content/themes/theme", "keep:        last 5 releases", "mode:        dry-run", "Would package " + filepath.Join(repoRoot, "build", "theme") + " -> " + filepath.Join(repoRoot, "dist", "client-v1.2.3.zip"), "> ssh -p 12345 client@203.0.113.10 'mkdir -p /www/client/public/wp-content/themes/.nf-releases/theme/_uploads'", "> rsync -az -e 'ssh -p 12345' " + filepath.Join(repoRoot, "dist", "client-v1.2.3.zip") + " client@203.0.113.10:/www/client/public/wp-content/themes/.nf-releases/theme/_uploads/client-v1.2.3.zip", "remote script: extract release, switch active theme, activate, record metadata, prune old releases", "> ssh -p 12345 client@203.0.113.10 'sh -s -- nf-theme-deploy-release'", "No remote files were changed."} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("theme deploy stdout missing %q:\n%s", want, stdout)
		}
	}
	for _, unwanted := range []string{"unzip -q", "GLOB_ONLYDIR", "wp --path=/www/client/public theme activate"} {
		if strings.Contains(stdout, unwanted) {
			t.Fatalf("theme deploy stdout should not print remote script fragment %q:\n%s", unwanted, stdout)
		}
	}
}

func TestRunThemeDeployDryRunPlansLinodePackagedRelease(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev", "port": "22"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "client.app1-linode", "env": "staging", "target": "app1-linode", "url": "https://client-staging.app1-linode.nonfiction.dev", "path": "/var/www/sites/client_staging/public", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev", "port": "22"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	repoRoot := t.TempDir()
	for _, dir := range []string{".git", "theme"} {
		if err := os.MkdirAll(filepath.Join(repoRoot, dir), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "theme", "style.css"), []byte("/*\nTheme Name: Theme\nVersion: 2.0.0\n*/\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(style.css) error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "client-theme"}, "remotes": map[string]any{"staging": "client.app1-linode:staging"}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
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

	oldRunRsync := runRsyncCommandFn
	runRsyncCommandFn = func(args []string) error { t.Fatalf("runRsyncCommandFn called during dry-run: %#v", args); return nil }
	t.Cleanup(func() { runRsyncCommandFn = oldRunRsync })
	oldRunSSH := runSSHCommandFn
	runSSHCommandFn = func(args []string) error { t.Fatalf("runSSHCommandFn called during dry-run: %#v", args); return nil }
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })

	stdout := captureStdout(t, func() {
		if got := Run([]string{"theme", "deploy", "staging", "--dry-run"}); got != 0 {
			t.Fatalf("Run(theme deploy --dry-run) = %d, want 0", got)
		}
	})
	for _, want := range []string{"provider:    linode", "artifact:    " + filepath.Join(repoRoot, "dist", "client-v2.0.0.zip"), "release dir: /var/www/sites/client_staging/public/wp-content/themes/.nf-releases/client-theme/v2.0.0-", "active dir:  /var/www/sites/client_staging/public/wp-content/themes/client-theme", "remote script: extract release, switch active theme, activate, record metadata, prune old releases", "> ssh -p 22 nonfiction@app1-linode.nonfiction.dev 'sh -s -- nf-theme-deploy-release'"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("theme deploy linode stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestRunThemeDeployWithoutRemotePromptsPicker(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	repoRoot := t.TempDir()
	for _, dir := range []string{".git", "theme"} {
		if err := os.MkdirAll(filepath.Join(repoRoot, dir), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "theme", "style.css"), []byte("/*\nTheme Name: Theme\nVersion: 1.0.0\n*/\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(style.css) error = %v", err)
	}
	project := map[string]any{
		"version":   1,
		"project":   map[string]any{"slug": "client"},
		"wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"},
		"remotes":   map[string]any{"production": "client-kinsta:live"},
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
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

	oldSelect := remoteSelectFn
	var selectTitle string
	var selectOptions []ui.SelectOption
	remoteSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectTitle = title
		selectOptions = append([]ui.SelectOption(nil), options...)
		return "production", nil
	}
	t.Cleanup(func() { remoteSelectFn = oldSelect })
	oldRunRsync := runRsyncCommandFn
	runRsyncCommandFn = func(args []string) error { return nil }
	t.Cleanup(func() { runRsyncCommandFn = oldRunRsync })
	oldRunSSH := runSSHCommandFn
	runSSHCommandFn = func(args []string) error { return nil }
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })
	oldRunSSHStdin := runSSHStdinCommandFn
	runSSHStdinCommandFn = func(args []string, script string) error { return nil }
	t.Cleanup(func() { runSSHStdinCommandFn = oldRunSSHStdin })

	stdout := captureStdout(t, func() {
		if got := Run([]string{"theme", "deploy"}); got != 0 {
			t.Fatalf("Run(theme deploy) = %d, want 0", got)
		}
	})
	if selectTitle != "Choose a remote to deploy theme to" {
		t.Fatalf("select title = %q", selectTitle)
	}
	if len(selectOptions) != 1 || selectOptions[0] != (ui.SelectOption{Value: "production", Label: "production -> client-kinsta:live"}) {
		t.Fatalf("select options = %#v", selectOptions)
	}
	if !strings.Contains(stdout, "remote:      production") || !strings.Contains(stdout, "Theme release deployed.") {
		t.Fatalf("theme deploy picker stdout = %q", stdout)
	}
}

func TestRunThemeRollbackDryRunPlansPreviousKinstaRelease(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	repoRoot := t.TempDir()
	for _, dir := range []string{".git", "theme"} {
		if err := os.MkdirAll(filepath.Join(repoRoot, dir), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"}, "remotes": map[string]any{"production": "client-kinsta:live"}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
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
	oldRunSSH := runSSHCommandFn
	runSSHCommandFn = func(args []string) error { t.Fatalf("runSSHCommandFn called during dry-run: %#v", args); return nil }
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })

	stdout := captureStdout(t, func() {
		if got := Run([]string{"theme", "rollback", "production", "--dry-run"}); got != 0 {
			t.Fatalf("Run(theme rollback --dry-run) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Theme rollback plan:", "remote:      production", "provider:    kinsta", "releases:    /www/client/public/wp-content/themes/.nf-releases/theme/releases.json", "release dir: /www/client/public/wp-content/themes/.nf-releases/theme/<previous-release>", "active dir:  /www/client/public/wp-content/themes/theme", "mode:        dry-run", "remote script: select previous release, switch active theme, activate, record rollback", "> ssh -p 12345 client@203.0.113.10 'sh -s -- nf-theme-rollback-release'", "No remote files were changed."} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("theme rollback stdout missing %q:\n%s", want, stdout)
		}
	}
	for _, unwanted := range []string{"target_release=$(php -r", "wp --path=/www/client/public theme activate"} {
		if strings.Contains(stdout, unwanted) {
			t.Fatalf("theme rollback stdout should not print remote script fragment %q:\n%s", unwanted, stdout)
		}
	}
}

func TestRunThemeRollbackExecuteRunsLinodeRollbackScript(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev", "port": "22"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "client.app1-linode", "env": "live", "target": "app1-linode", "path": "/var/www/sites/client/public", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev", "port": "22"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	repoRoot := t.TempDir()
	for _, dir := range []string{".git", "theme"} {
		if err := os.MkdirAll(filepath.Join(repoRoot, dir), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "client-theme"}, "remotes": map[string]any{"production": "client.app1-linode:live"}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
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
	oldRunSSHStdin := runSSHStdinCommandFn
	var sshCommands [][]string
	var sshScripts []string
	runSSHStdinCommandFn = func(args []string, script string) error {
		sshCommands = append(sshCommands, append([]string(nil), args...))
		sshScripts = append(sshScripts, script)
		return nil
	}
	t.Cleanup(func() { runSSHStdinCommandFn = oldRunSSHStdin })

	stdout := captureStdout(t, func() {
		if got := Run([]string{"theme", "rollback", "production"}); got != 0 {
			t.Fatalf("Run(theme rollback) = %d, want 0", got)
		}
	})
	if !strings.Contains(stdout, "Theme release rolled back.") {
		t.Fatalf("theme rollback stdout = %q", stdout)
	}
	if len(sshCommands) != 1 {
		t.Fatalf("ssh commands len = %d, want 1: %#v", len(sshCommands), sshCommands)
	}
	if sshCommands[0][len(sshCommands[0])-1] != "sh -s -- nf-theme-rollback-release" {
		t.Fatalf("rollback ssh command = %#v", sshCommands[0])
	}
	script := sshScripts[0]
	for _, want := range []string{"release_base=/var/www/sites/client/public/wp-content/themes/.nf-releases/client-theme", "metadata_file=/var/www/sites/client/public/wp-content/themes/.nf-releases/client-theme/releases.json", "target_release=$(php -r", "cp -a \"$release_dir\" \"$active_tmp\"", "sudo -u www-data wp --path=/var/www/sites/client/public theme activate client-theme --allow-root", `"action"=>"rollback"`} {
		if !strings.Contains(script, want) {
			t.Fatalf("rollback ssh script missing %q:\n%s", want, script)
		}
	}
}

func TestRunSiteSnapshotDownloadsRemoteEnvSnapshot(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_DATA_HOME", t.TempDir())
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	outputDir := filepath.Join(t.TempDir(), "client-snapshot")
	oldRunSSH := runSSHCommandFn
	var sshCommands [][]string
	runSSHCommandFn = func(args []string) error {
		sshCommands = append(sshCommands, append([]string(nil), args...))
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })
	oldRunRsync := runRsyncCommandFn
	var rsyncCommands [][]string
	runRsyncCommandFn = func(args []string) error {
		rsyncCommands = append(rsyncCommands, append([]string(nil), args...))
		return nil
	}
	t.Cleanup(func() { runRsyncCommandFn = oldRunRsync })

	stdout := captureStdout(t, func() {
		if got := Run([]string{"site", "snapshot", "client-kinsta:live", "--output", outputDir}); got != 0 {
			t.Fatalf("Run(site snapshot) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Site snapshot plan:", "env:           client-kinsta:live", "provider:      kinsta", "environment ssh: client@203.0.113.10", "output:        " + outputDir, "Site snapshot created.", "source: remote", "database: database.sql.gz", "wp-content: wp-content.tar.gz"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("site snapshot stdout missing %q:\n%s", want, stdout)
		}
	}
	if len(sshCommands) != 2 {
		t.Fatalf("ssh commands len = %d, want snapshot and cleanup: %#v", len(sshCommands), sshCommands)
	}
	if !strings.Contains(sshCommands[0][len(sshCommands[0])-1], "wp --path=/www/client/public db export") {
		t.Fatalf("snapshot ssh command = %#v", sshCommands[0])
	}
	if len(rsyncCommands) != 1 {
		t.Fatalf("rsync commands len = %d, want 1: %#v", len(rsyncCommands), rsyncCommands)
	}
	if got, want := rsyncCommands[0][3], "ssh -p 12345"; got != want {
		t.Fatalf("rsync ssh option = %q, want %q", got, want)
	}
	if got, want := rsyncCommands[0][len(rsyncCommands[0])-1], outputDir+string(filepath.Separator); got != want {
		t.Fatalf("rsync output = %q, want %q", got, want)
	}
	metadataPath := filepath.Join(outputDir, "snapshot.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("ReadFile(snapshot.json) error = %v", err)
	}
	for _, want := range []string{`"source": "remote"`, `"env_id": "client-kinsta:live"`, `"database": "database.sql.gz"`, `"wp_content": "wp-content.tar.gz"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("snapshot metadata missing %q:\n%s", want, data)
		}
	}
}

func TestRunSiteSnapshotWithoutEnvPromptsPicker(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	oldSelect := siteSelectFn
	var selectTitle string
	siteSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectTitle = title
		return "client-kinsta:live", nil
	}
	t.Cleanup(func() { siteSelectFn = oldSelect })
	oldRunSSH := runSSHCommandFn
	runSSHCommandFn = func(args []string) error { return nil }
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })
	oldRunRsync := runRsyncCommandFn
	runRsyncCommandFn = func(args []string) error { return nil }
	t.Cleanup(func() { runRsyncCommandFn = oldRunRsync })

	stdout := captureStdout(t, func() {
		if got := Run([]string{"site", "snapshot", "--dry-run"}); got != 0 {
			t.Fatalf("Run(site snapshot --dry-run) = %d, want 0", got)
		}
	})
	if selectTitle != "Choose a remote env to snapshot" {
		t.Fatalf("select title = %q", selectTitle)
	}
	if !strings.Contains(stdout, "mode:          dry-run") || !strings.Contains(stdout, "No data was changed") {
		t.Fatalf("site snapshot dry-run output = %q", stdout)
	}
}

func TestRunSiteSnapshotListShowsRemoteSnapshots(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("NF_DATA_HOME", dataHome)
	writeTestRemoteSnapshot(t, "client-kinsta.live-2026-06-04-120000", "client-kinsta:live", "2026-06-04T12:00:00Z", 120, 240)
	writeTestRemoteSnapshot(t, "client-kinsta.staging-2026-06-04-110000", "client-kinsta:staging", "2026-06-04T11:00:00Z", 60, 0)

	stdout := captureStdout(t, func() {
		if got := Run([]string{"site", "snapshot", "list"}); got != 0 {
			t.Fatalf("Run(site snapshot list) = %d, want 0", got)
		}
	})
	for _, want := range []string{"name", "created", "env", "database", "wp-content", "path", "client-kinsta.live-2026-06-04-120000", "client-kinsta:live", "120 B", "240 B", "client-kinsta.staging-2026-06-04-110000", "client-kinsta:staging", "60 B", "0 B"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("site snapshot list output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Index(stdout, "client-kinsta.live-2026-06-04-120000") > strings.Index(stdout, "client-kinsta.staging-2026-06-04-110000") {
		t.Fatalf("site snapshot list not sorted newest first:\n%s", stdout)
	}
}

func TestRunSiteSnapshotListEmpty(t *testing.T) {
	t.Setenv("NF_DATA_HOME", t.TempDir())
	stdout := captureStdout(t, func() {
		if got := Run([]string{"site", "snapshot", "ls"}); got != 0 {
			t.Fatalf("Run(site snapshot ls) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(stdout) != "No remote snapshots found." {
		t.Fatalf("site snapshot ls output = %q", stdout)
	}
}

func TestRunSiteSnapshotRemoveDeletesRemoteSnapshotWithYes(t *testing.T) {
	t.Setenv("NF_DATA_HOME", t.TempDir())
	name := "client-kinsta.live-2026-06-04-120000"
	writeTestRemoteSnapshot(t, name, "client-kinsta:live", "2026-06-04T12:00:00Z", 120, 240)
	path := config.RemoteSnapshotDir(name)

	stdout := captureStdout(t, func() {
		if got := Run([]string{"site", "snapshot", "remove", name, "--yes"}); got != 0 {
			t.Fatalf("Run(site snapshot remove --yes) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Deleted remote snapshot.", "name: " + name, "env: client-kinsta:live", "path: " + path} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("site snapshot remove output missing %q:\n%s", want, stdout)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("remote snapshot still exists: %v", err)
	}
}

func TestRunSiteSnapshotRemoveRequiresYesWhenNonInteractive(t *testing.T) {
	t.Setenv("NF_DATA_HOME", t.TempDir())
	name := "client-kinsta.live-2026-06-04-120000"
	writeTestRemoteSnapshot(t, name, "client-kinsta:live", "2026-06-04T12:00:00Z", 120, 240)
	path := config.RemoteSnapshotDir(name)
	oldInteractive := envSnapshotIsInteractive
	envSnapshotIsInteractive = func() bool { return false }
	t.Cleanup(func() { envSnapshotIsInteractive = oldInteractive })

	stderr := captureStderr(t, func() {
		if got := Run([]string{"site", "snapshot", "rm", name}); got != 1 {
			t.Fatalf("Run(site snapshot rm) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "site snapshot remove requires --yes when stdin is not interactive") {
		t.Fatalf("site snapshot rm stderr = %q", stderr)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("remote snapshot was removed without --yes: %v", err)
	}
}

func TestRunSiteSnapshotPruneDryRunKeepsNewestPerEnv(t *testing.T) {
	t.Setenv("NF_DATA_HOME", t.TempDir())
	writeTestRemoteSnapshot(t, "client-kinsta.live-2026-06-04-120000", "client-kinsta:live", "2026-06-04T12:00:00Z", 120, 0)
	writeTestRemoteSnapshot(t, "client-kinsta.live-2026-06-04-110000", "client-kinsta:live", "2026-06-04T11:00:00Z", 110, 0)
	writeTestRemoteSnapshot(t, "client-kinsta.live-2026-06-04-100000", "client-kinsta:live", "2026-06-04T10:00:00Z", 100, 0)
	writeTestRemoteSnapshot(t, "client-kinsta.staging-2026-06-04-120000", "client-kinsta:staging", "2026-06-04T12:00:00Z", 220, 0)
	writeTestRemoteSnapshot(t, "client-kinsta.staging-2026-06-04-110000", "client-kinsta:staging", "2026-06-04T11:00:00Z", 210, 0)

	stdout := captureStdout(t, func() {
		if got := Run([]string{"site", "snapshot", "prune", "--keep", "1", "--dry-run"}); got != 0 {
			t.Fatalf("Run(site snapshot prune --dry-run) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Remote snapshot prune plan:", "keep newest per env: 1", "delete snapshots:    3", "reclaim about:       420 B", "client-kinsta.live-2026-06-04-110000", "client-kinsta.live-2026-06-04-100000", "client-kinsta.staging-2026-06-04-110000", "No remote snapshots were deleted"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("site snapshot prune output missing %q:\n%s", want, stdout)
		}
	}
	for _, notWant := range []string{"client-kinsta.live-2026-06-04-120000", "client-kinsta.staging-2026-06-04-120000"} {
		if strings.Contains(stdout, notWant) {
			t.Fatalf("site snapshot prune output contains kept snapshot %q:\n%s", notWant, stdout)
		}
	}
	if _, err := os.Stat(config.RemoteSnapshotDir("client-kinsta.live-2026-06-04-100000")); err != nil {
		t.Fatalf("dry-run deleted snapshot: %v", err)
	}
}

func TestRunSiteSnapshotPruneDeletesOldSnapshotsWithYes(t *testing.T) {
	t.Setenv("NF_DATA_HOME", t.TempDir())
	writeTestRemoteSnapshot(t, "client-kinsta.live-2026-06-04-120000", "client-kinsta:live", "2026-06-04T12:00:00Z", 120, 0)
	writeTestRemoteSnapshot(t, "client-kinsta.live-2026-06-04-110000", "client-kinsta:live", "2026-06-04T11:00:00Z", 110, 0)
	writeTestRemoteSnapshot(t, "client-kinsta.staging-2026-06-04-120000", "client-kinsta:staging", "2026-06-04T12:00:00Z", 220, 0)
	writeTestRemoteSnapshot(t, "client-kinsta.staging-2026-06-04-110000", "client-kinsta:staging", "2026-06-04T11:00:00Z", 210, 0)

	stdout := captureStdout(t, func() {
		if got := Run([]string{"site", "snapshot", "prune", "--keep=1", "--yes"}); got != 0 {
			t.Fatalf("Run(site snapshot prune --yes) = %d, want 0", got)
		}
	})
	if !strings.Contains(stdout, "Deleted 2 remote snapshots") {
		t.Fatalf("site snapshot prune output = %q", stdout)
	}
	for _, deleted := range []string{"client-kinsta.live-2026-06-04-110000", "client-kinsta.staging-2026-06-04-110000"} {
		if _, err := os.Stat(config.RemoteSnapshotDir(deleted)); !os.IsNotExist(err) {
			t.Fatalf("remote snapshot %q still exists: %v", deleted, err)
		}
	}
	for _, kept := range []string{"client-kinsta.live-2026-06-04-120000", "client-kinsta.staging-2026-06-04-120000"} {
		if _, err := os.Stat(config.RemoteSnapshotDir(kept)); err != nil {
			t.Fatalf("remote snapshot %q missing: %v", kept, err)
		}
	}
}

func writeTestRemoteSnapshot(t *testing.T, name, envID, createdAt string, dbSize, contentSize int) {
	t.Helper()
	dir := config.RemoteSnapshotDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(remote snapshot) error = %v", err)
	}
	siteID, env, ok := splitSiteEnvRef(envID)
	if !ok {
		t.Fatalf("invalid test envID %q", envID)
	}
	meta := remoteSnapshotMetadata{Schema: 1, Source: "remote", EnvID: envID, SiteID: siteID, Env: env, Provider: "kinsta", CreatedAt: createdAt, Path: dir, Contents: envSnapshotContents{Database: "database.sql.gz", WpContent: "wp-content.tar.gz", WpContentPaths: envSnapshotContentPaths()}}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(remote snapshot) error = %v", err)
	}
	if err := os.WriteFile(remoteSnapshotMetadataPath(dir), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(snapshot.json) error = %v", err)
	}
	if err := os.WriteFile(remoteSnapshotDatabaseArchive(dir), bytes.Repeat([]byte("d"), dbSize), 0o644); err != nil {
		t.Fatalf("WriteFile(database.sql.gz) error = %v", err)
	}
	if err := os.WriteFile(remoteSnapshotWpContentArchive(dir), bytes.Repeat([]byte("w"), contentSize), 0o644); err != nil {
		t.Fatalf("WriteFile(wp-content.tar.gz) error = %v", err)
	}
}

func TestRunRemoteAddListRemoveWritesProjectMetadata(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	sites := map[string]any{"sites": map[string]any{"live-client-app1-linode": map[string]any{"provider": "linode", "site_id": "client-app1-linode", "env": "live", "url": "https://client.app1.nonfiction.dev/", "server": "app1-linode"}}}
	stateData, err := json.MarshalIndent(sites, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(sites) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "sites.json"), append(stateData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(sites) error = %v", err)
	}
	servers := map[string]any{"servers": map[string]any{"app1-linode": map[string]any{"name": "app1-linode", "provider": "linode", "hostname": "app1.nonfiction.dev"}}}
	serverData, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(servers) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "servers.json"), append(serverData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(servers) error = %v", err)
	}

	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client"}, "remotes": map[string]any{}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	projectPath := filepath.Join(repoRoot, "nf.json")
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
		if got := Run([]string{"remote", "add", "production", "client-app1-linode:live"}); got != 0 {
			t.Fatalf("Run(remote add) = %d, want 0", got)
		}
	})
	if !strings.Contains(addOutput, "Added remote production -> client-app1-linode:live") {
		t.Fatalf("remote add output = %q", addOutput)
	}

	listOutput := captureStdout(t, func() {
		if got := Run([]string{"remote", "list"}); got != 0 {
			t.Fatalf("Run(remote list) = %d, want 0", got)
		}
	})
	for _, want := range []string{"remote", "env", "production", "client-app1-linode:live"} {
		if !strings.Contains(listOutput, want) {
			t.Fatalf("remote list output missing %q:\n%s", want, listOutput)
		}
	}
	for _, notWant := range []string{"name", "site\n", "client-app1-linode  live"} {
		if strings.Contains(listOutput, notWant) {
			t.Fatalf("remote list output contains %q:\n%s", notWant, listOutput)
		}
	}

	showOutput := captureStdout(t, func() {
		if got := Run([]string{"remote", "show", "production"}); got != 0 {
			t.Fatalf("Run(remote show) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Remote: production", "Env: client-app1-linode:live", "Provider: linode", "Target: app1-linode", "URL: https://client.app1.nonfiction.dev/"} {
		if !strings.Contains(showOutput, want) {
			t.Fatalf("remote show output missing %q:\n%s", want, showOutput)
		}
	}
	if strings.Contains(showOutput, "Site: client-app1-linode") {
		t.Fatalf("remote show output contains separate site field:\n%s", showOutput)
	}

	oldRemoteSelect := remoteSelectFn
	showPickerCalled := false
	remoteSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		showPickerCalled = true
		return "", fmt.Errorf("unexpected picker")
	}
	showOnlyOutput := captureStdout(t, func() {
		if got := Run([]string{"remote", "show"}); got != 0 {
			t.Fatalf("Run(remote show without arg) = %d, want 0", got)
		}
	})
	if showPickerCalled {
		t.Fatalf("remote show without arg opened picker with one remote")
	}
	if !strings.Contains(showOnlyOutput, "Env: client-app1-linode:live") {
		t.Fatalf("remote show without arg output = %q", showOnlyOutput)
	}
	remoteSelectFn = oldRemoteSelect

	projectData, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("ReadFile(project) error = %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(projectData, &metadata); err != nil {
		t.Fatalf("Unmarshal(project) error = %v", err)
	}
	remote, ok := metadata["remotes"].(map[string]any)["production"].(string)
	if !ok || remote != "client-app1-linode:live" {
		t.Fatalf("remotes.production = %#v, want client-app1-linode:live", metadata["remotes"])
	}

	var removeSelectTitle string
	var removeSelectOptions []ui.SelectOption
	remoteSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		removeSelectTitle = title
		removeSelectOptions = append([]ui.SelectOption(nil), options...)
		return "production", nil
	}
	t.Cleanup(func() { remoteSelectFn = oldRemoteSelect })
	removeOutput := captureStdout(t, func() {
		if got := Run([]string{"remote", "remove"}); got != 0 {
			t.Fatalf("Run(remote remove) = %d, want 0", got)
		}
	})
	if removeSelectTitle != "Choose a remote to remove" {
		t.Fatalf("remote remove select title = %q", removeSelectTitle)
	}
	if len(removeSelectOptions) != 1 || removeSelectOptions[0] != (ui.SelectOption{Value: "production", Label: "production -> client-app1-linode:live"}) {
		t.Fatalf("remote remove select options = %#v", removeSelectOptions)
	}
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

func TestRunRemoteAddRequiresCachedSiteEnv(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client"}, "remotes": map[string]any{}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	projectPath := filepath.Join(repoRoot, "nf.json")
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

	stderr := captureStderr(t, func() {
		if got := Run([]string{"remote", "add", "production", "client-kinsta:live"}); got != 1 {
			t.Fatalf("Run(remote add) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "No cached remote env matched site \"client-kinsta\" env \"live\"") {
		t.Fatalf("remote add stderr = %q", stderr)
	}
	projectData, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("ReadFile(project) error = %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(projectData, &metadata); err != nil {
		t.Fatalf("Unmarshal(project) error = %v", err)
	}
	if len(mapMapAtPath(metadata, "remotes")) != 0 {
		t.Fatalf("remote add wrote metadata despite missing cache: %#v", mapMapAtPath(metadata, "remotes"))
	}
}

func TestRunRemoteAddRequiresEnvRef(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client"}, "remotes": map[string]any{}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
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
		if got := Run([]string{"remote", "add", "production", "client-kinsta"}); got != 1 {
			t.Fatalf("Run(remote add site) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "remote add requires an env ref like site.target:env") {
		t.Fatalf("remote add stderr = %q", stderr)
	}
}

func TestRunRemoteAddWithoutEnvPromptsEnvPicker(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/"},
		{"provider": "kinsta", "site_id": "client-kinsta", "env": "staging", "url": "https://staging.example.com/"},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	repoRoot, projectPath := writeTestProject(t)
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	oldSelect := siteSelectFn
	var selectTitle string
	var selectOptions []ui.SelectOption
	siteSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectTitle = title
		selectOptions = append([]ui.SelectOption(nil), options...)
		return "client-kinsta:staging", nil
	}
	t.Cleanup(func() { siteSelectFn = oldSelect })

	output := captureStdout(t, func() {
		if got := Run([]string{"remote", "add", "staging"}); got != 0 {
			t.Fatalf("Run(remote add name) = %d, want 0", got)
		}
	})
	if selectTitle != "Choose a remote env" {
		t.Fatalf("select title = %q", selectTitle)
	}
	if len(selectOptions) != 2 || selectOptions[0] != (ui.SelectOption{Value: "client-kinsta:live", Label: "client-kinsta:live"}) || selectOptions[1] != (ui.SelectOption{Value: "client-kinsta:staging", Label: "client-kinsta:staging"}) {
		t.Fatalf("select options = %#v", selectOptions)
	}
	if !strings.Contains(output, "Added remote staging -> client-kinsta:staging") {
		t.Fatalf("remote add output = %q", output)
	}
	assertProjectRemote(t, projectPath, "staging", "client-kinsta", "staging")
}

func TestRunRemoteAddWithoutNamePromptsNameAndEnvPicker(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	repoRoot, projectPath := writeTestProject(t)
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	oldPrompt := remotePromptString
	oldSelect := siteSelectFn
	var promptTitle string
	remotePromptString = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		promptTitle = prompt
		if defaultValue != "" || allowBlank {
			t.Fatalf("remote prompt default/allowBlank = %q/%v, want empty/false", defaultValue, allowBlank)
		}
		return "production", nil
	}
	var selectTitle string
	siteSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectTitle = title
		return "client-kinsta:live", nil
	}
	t.Cleanup(func() {
		remotePromptString = oldPrompt
		siteSelectFn = oldSelect
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"remote", "add"}); got != 0 {
			t.Fatalf("Run(remote add) = %d, want 0", got)
		}
	})
	if promptTitle != "Remote name" {
		t.Fatalf("prompt title = %q", promptTitle)
	}
	if selectTitle != "Choose a remote env" {
		t.Fatalf("select title = %q", selectTitle)
	}
	if !strings.Contains(output, "Added remote production -> client-kinsta:live") {
		t.Fatalf("remote add output = %q", output)
	}
	assertProjectRemote(t, projectPath, "production", "client-kinsta", "live")
}

func writeTestProject(t *testing.T) (string, string) {
	t.Helper()
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client"}, "remotes": map[string]any{}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	projectPath := filepath.Join(repoRoot, "nf.json")
	if err := os.WriteFile(projectPath, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(project) error = %v", err)
	}
	return repoRoot, projectPath
}

func assertProjectRemote(t *testing.T, projectPath, remoteName, wantSiteID, wantEnv string) {
	t.Helper()
	projectData, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("ReadFile(project) error = %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(projectData, &metadata); err != nil {
		t.Fatalf("Unmarshal(project) error = %v", err)
	}
	remote := recordValueString(metadata["remotes"].(map[string]any)[remoteName])
	if got, want := remote, canonicalEnvID(wantSiteID, wantEnv); got != want {
		t.Fatalf("remotes.%s = %q, want %q", remoteName, got, want)
	}
}

func TestRunEnvHelpShowsCommandsWithoutShortcuts(t *testing.T) {
	output := captureStdout(t, func() { _ = runEnvHelp() })
	for _, wanted := range []string{"env\n\nCommands:\n", "show", "show paths, ports, and URLs", "password", "show admin password only", "up", "start the local env", "down", "stop the local env", "logs", "tail WordPress logs", "shell", "open a shell in the local env", "reset", "destroy and recreate the local env", "wp -- <args>", "run wp-cli in the local env", "plugins", "manage configured WordPress plugins", "push [remote] [--dry-run] [--execute] [--yes]", "pull [remote] [--dry-run] [--execute] [--yes]", "snapshot", "manage env snapshots"} {
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
	for _, want := range []string{"env snapshot\n\nCommands:\n", "list, ls", "list env snapshots", "add [name]", "create an env snapshot", "import [remote] [--name name]", "import a remote snapshot", "use [name] [--remote remote] [--name name] [--yes]", "restore an env snapshot", "remove, rm [name]", "delete an env snapshot", "prune [--keep N] [--dry-run] [--yes]"} {
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
	if err := os.MkdirAll(filepath.Join(repoRoot, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll(theme) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "theme", "style.css"), []byte("/* demo */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(style.css) error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"}, "env": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	snapshotDir := filepath.Join(config.DataHome(), "snapshots", "local", "client", "demo-snapshot")
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
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"}, "env": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll(theme) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "theme", "style.css"), []byte("/* demo */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(style.css) error = %v", err)
	}
	snapshotDir := filepath.Join(config.DataHome(), "snapshots", "local", "client", "2026-05-28-093012")
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

func TestRunEnvSnapshotImportCopiesRemoteSnapshot(t *testing.T) {
	remoteName := "client-kinsta.live-2026-06-04-120000"
	repoRoot, _ := writeTestEnvProject(t)
	writeTestRemoteSnapshot(t, remoteName, "client-kinsta:live", "2026-06-04T12:00:00Z", 2, 7)
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	stdout := captureStdout(t, func() {
		if got := Run([]string{"env", "snapshot", "import", remoteName, "--name", "imported-live"}); got != 0 {
			t.Fatalf("Run(env snapshot import) = %d, want 0", got)
		}
	})
	localDir := filepath.Join(config.DataHome(), "snapshots", "local", "client", "imported-live")
	for _, want := range []string{"Remote snapshot imported.", "project: client", "name: imported-live", "remote: " + remoteName, "env: client-kinsta:live", "path: " + localDir, "Next: nf env snapshot use imported-live"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("env snapshot import output missing %q:\n%s", want, stdout)
		}
	}
	dbData, err := os.ReadFile(filepath.Join(localDir, "database.sql.gz"))
	if err != nil {
		t.Fatalf("ReadFile(imported database) error = %v", err)
	}
	if string(dbData) != "dd" {
		t.Fatalf("imported database = %q, want copied data", dbData)
	}
	contentData, err := os.ReadFile(filepath.Join(localDir, "wp-content.tar.gz"))
	if err != nil {
		t.Fatalf("ReadFile(imported wp-content) error = %v", err)
	}
	if string(contentData) != "wwwwwww" {
		t.Fatalf("imported wp-content = %q, want copied data", contentData)
	}
	metaData, err := os.ReadFile(filepath.Join(localDir, "snapshot.json"))
	if err != nil {
		t.Fatalf("ReadFile(imported snapshot.json) error = %v", err)
	}
	for _, want := range []string{`"name": "imported-live"`, `"project_slug": "client"`, `"database": "database.sql.gz"`, `"wp_content": "wp-content.tar.gz"`} {
		if !strings.Contains(string(metaData), want) {
			t.Fatalf("imported metadata missing %q:\n%s", want, metaData)
		}
	}
}

func TestRunEnvSnapshotImportDefaultNameSanitizesRemoteSnapshotName(t *testing.T) {
	remoteName := "client-kinsta.live-2026-06-04-120000"
	repoRoot, _ := writeTestEnvProject(t)
	writeTestRemoteSnapshot(t, remoteName, "client-kinsta:live", "2026-06-04T12:00:00Z", 2, 7)
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	stdout := captureStdout(t, func() {
		if got := Run([]string{"env", "snapshot", "import", remoteName}); got != 0 {
			t.Fatalf("Run(env snapshot import) = %d, want 0", got)
		}
	})
	wantName := "remote-client-kinsta-live-2026-06-04-120000"
	if !strings.Contains(stdout, "name: "+wantName) {
		t.Fatalf("env snapshot import output = %q, want default name %q", stdout, wantName)
	}
	if _, err := os.Stat(filepath.Join(config.DataHome(), "snapshots", "local", "client", wantName, "snapshot.json")); err != nil {
		t.Fatalf("default-named imported snapshot missing: %v", err)
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
	if err := os.MkdirAll(filepath.Join(repoRoot, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll(theme) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "theme", "style.css"), []byte("/* demo */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(style.css) error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"}, "env": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	sourceSnapshotDir := filepath.Join(config.DataHome(), "snapshots", "local", "client", "restore-source")
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

func TestRunEnvSnapshotUseYesSkipsInteractiveConfirmation(t *testing.T) {
	repoRoot, cfg := writeTestEnvProject(t)
	writeTestEnvSnapshot(t, cfg, "restore-source", "2026-05-28T09:30:12Z", 2, 7)
	dockerDir := t.TempDir()
	logPath := filepath.Join(dockerDir, "docker-args.txt")
	dockerScript := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$DOCKER_LOG\"\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	t.Setenv("DOCKER_LOG", logPath)
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldConfirm := envSnapshotConfirm
	envSnapshotConfirm = func(prompt string, defaultYes bool) (bool, error) {
		t.Fatalf("envSnapshotConfirm called with %q", prompt)
		return false, nil
	}
	t.Cleanup(func() { envSnapshotConfirm = oldConfirm })
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() {
		if got := Run([]string{"env", "snapshot", "use", "restore-source", "--yes"}); got != 0 {
			t.Fatalf("Run(env snapshot use --yes) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Snapshot restored.", "name: restore-source", "Safety snapshot:"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run(env snapshot use --yes) output missing %q:\n%s", want, output)
		}
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

func TestRunEnvSnapshotUseRemoteImportsThenRestores(t *testing.T) {
	repoRoot, cfg := writeTestEnvProject(t)
	remoteName := "client-kinsta.live-2026-06-04-120000"
	writeTestRemoteSnapshot(t, remoteName, "client-kinsta:live", "2026-06-04T12:00:00Z", 2, 7)
	dockerDir := t.TempDir()
	logPath := filepath.Join(dockerDir, "docker-args.txt")
	dockerScript := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$DOCKER_LOG\"\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	t.Setenv("DOCKER_LOG", logPath)
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldConfirm := envSnapshotConfirm
	envSnapshotConfirm = func(prompt string, defaultYes bool) (bool, error) {
		t.Fatalf("envSnapshotConfirm called with %q", prompt)
		return false, nil
	}
	t.Cleanup(func() { envSnapshotConfirm = oldConfirm })
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	stdout := captureStdout(t, func() {
		if got := Run([]string{"env", "snapshot", "use", "--remote", remoteName, "--name", "live-copy", "--yes"}); got != 0 {
			t.Fatalf("Run(env snapshot use --remote --yes) = %d, want 0", got)
		}
	})
	localDir := envSnapshotDir(cfg, "live-copy")
	for _, want := range []string{"Remote snapshot imported.", "name: live-copy", "remote: " + remoteName, "env: client-kinsta:live", "Snapshot restored.", "Safety snapshot:"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("env snapshot use --remote output missing %q:\n%s", want, stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(localDir, "snapshot.json")); err != nil {
		t.Fatalf("imported local snapshot missing: %v", err)
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
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"}, "env": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	snapshotDir := filepath.Join(config.DataHome(), "snapshots", "local", "client", "delete-me")
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

func TestExactLineFilterWriterSuppressesOnlyKnownDBWarning(t *testing.T) {
	var out bytes.Buffer
	filter := newExactLineFilterWriter(&out, wpCLIPasswordlessLoginWarning)
	input := "before\n" + wpCLIPasswordlessLoginWarning + "\nWARNING: keep this\nafter"
	if _, err := filter.Write([]byte(input)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := filter.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	got := out.String()
	if strings.Contains(got, wpCLIPasswordlessLoginWarning) {
		t.Fatalf("filtered output still contains warning: %q", got)
	}
	for _, want := range []string{"before\n", "WARNING: keep this\n", "after"} {
		if !strings.Contains(got, want) {
			t.Fatalf("filtered output missing %q: %q", want, got)
		}
	}
}

func TestRunEnvSnapshotPruneDryRunShowsOldAutoSnapshots(t *testing.T) {
	repoRoot, cfg := writeTestEnvProject(t)
	writeTestEnvSnapshot(t, cfg, "known-good", "2026-06-04T12:00:00Z", 10, 10)
	writeTestEnvSnapshot(t, cfg, "pull-live-2026-06-04-100000", "2026-06-04T10:00:00Z", 100, 0)
	writeTestEnvSnapshot(t, cfg, "pull-live-2026-06-04-110000", "2026-06-04T11:00:00Z", 200, 0)
	writeTestEnvSnapshot(t, cfg, "2026-06-04-113000-pre-restore", "2026-06-04T11:30:00Z", 300, 0)
	writeTestEnvSnapshot(t, cfg, "push-live-2026-06-04-120000", "2026-06-04T12:00:00Z", 400, 0)
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() {
		if got := Run([]string{"env", "snapshot", "prune", "--keep", "2", "--dry-run"}); got != 0 {
			t.Fatalf("Run(prune --dry-run) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Env snapshot prune plan:", "keep newest auto snapshots: 2", "delete snapshots:            2", "name", "created", "database", "wp-content", "path", "200 B", "0 B", "pull-live-2026-06-04-110000", "pull-live-2026-06-04-100000", "No snapshots were deleted"} {
		if !strings.Contains(output, want) {
			t.Fatalf("prune dry-run output missing %q:\n%s", want, output)
		}
	}
	for _, notWant := range []string{"known-good", "push-live-2026-06-04-120000"} {
		if strings.Contains(output, notWant) {
			t.Fatalf("prune dry-run output contains kept snapshot %q:\n%s", notWant, output)
		}
	}
	if _, err := os.Stat(envSnapshotDir(cfg, "pull-live-2026-06-04-100000")); err != nil {
		t.Fatalf("dry-run deleted old snapshot: %v", err)
	}
}

func TestRunEnvSnapshotPruneDeletesOldAutoSnapshotsWithYes(t *testing.T) {
	repoRoot, cfg := writeTestEnvProject(t)
	writeTestEnvSnapshot(t, cfg, "known-good", "2026-06-04T12:00:00Z", 10, 10)
	writeTestEnvSnapshot(t, cfg, "pull-live-2026-06-04-100000", "2026-06-04T10:00:00Z", 100, 0)
	writeTestEnvSnapshot(t, cfg, "pull-live-2026-06-04-110000", "2026-06-04T11:00:00Z", 200, 0)
	writeTestEnvSnapshot(t, cfg, "2026-06-04-113000-pre-restore", "2026-06-04T11:30:00Z", 300, 0)
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() {
		if got := Run([]string{"env", "snapshot", "prune", "--keep=1", "--yes"}); got != 0 {
			t.Fatalf("Run(prune --yes) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Deleted 2 env snapshots") {
		t.Fatalf("prune output = %q", output)
	}
	for _, deleted := range []string{"pull-live-2026-06-04-110000", "pull-live-2026-06-04-100000"} {
		if _, err := os.Stat(envSnapshotDir(cfg, deleted)); !os.IsNotExist(err) {
			t.Fatalf("snapshot %q still exists: %v", deleted, err)
		}
	}
	for _, kept := range []string{"known-good", "2026-06-04-113000-pre-restore"} {
		if _, err := os.Stat(envSnapshotDir(cfg, kept)); err != nil {
			t.Fatalf("snapshot %q missing: %v", kept, err)
		}
	}
}

func writeTestEnvProject(t *testing.T) (string, envConfig) {
	t.Helper()
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll(theme) error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"}, "env": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	cfg := envConfig{ProjectSlug: "client", RepoRoot: repoRoot, ThemePath: "theme", EnvDir: config.EnvDir("client"), WordpressPort: 18432, MailpitPort: 18433, Compose: "docker compose", WordpressService: "wordpress", CliService: "cli", ThemeMountSlug: "theme", UploadsPath: "uploads", ThemeSlug: "theme"}
	return repoRoot, cfg
}

func writeTestEnvSnapshot(t *testing.T, cfg envConfig, name, createdAt string, dbSize, contentSize int) {
	t.Helper()
	dir := envSnapshotDir(cfg, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(snapshot) error = %v", err)
	}
	meta := envSnapshotMetadata{Schema: envSnapshotSchema, Name: name, ProjectSlug: cfg.ProjectSlug, CreatedAt: createdAt, EnvPath: cfg.EnvDir, ComposeProject: envComposeProjectName(cfg.ProjectSlug), WordpressURL: envSnapshotWordPressURL(cfg), Contents: envSnapshotContents{Database: "database.sql.gz", WpContent: "wp-content.tar.gz", WpContentPaths: envSnapshotContentPaths()}}
	metaJSON, err := envSnapshotMetadataJSON(meta)
	if err != nil {
		t.Fatalf("envSnapshotMetadataJSON() error = %v", err)
	}
	if err := os.WriteFile(envSnapshotMetadataPath(cfg, name), []byte(metaJSON), 0o644); err != nil {
		t.Fatalf("WriteFile(snapshot.json) error = %v", err)
	}
	if err := os.WriteFile(envSnapshotHostDatabaseArchive(cfg, name), bytes.Repeat([]byte("d"), dbSize), 0o644); err != nil {
		t.Fatalf("WriteFile(database.sql.gz) error = %v", err)
	}
	if err := os.WriteFile(envSnapshotHostWpContentArchive(cfg, name), bytes.Repeat([]byte("w"), contentSize), 0o644); err != nil {
		t.Fatalf("WriteFile(wp-content.tar.gz) error = %v", err)
	}
}

func TestRunThemeHelpShowsThemeCommandsInsideGit(t *testing.T) {
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client"}, "tasks": map[string]any{"build": map[string]any{"description": "Build the theme assets", "run": "npm run build"}}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
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
	for _, wanted := range []string{"\n  tasks                                      list configured theme tasks\n", "\n  package [--dry-run] [--source] [--output]  package theme files\n", "\n  deploy <remote> [--dry-run]                deploy a packaged theme release\n", "\n  rollback <remote> [--dry-run]              roll back to the previous theme release\n", "\nTheme tasks:\n"} {
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
	for _, want := range []string{"\n  tasks                                      list configured theme tasks\n", "\n  package [--dry-run] [--source] [--output]  package theme files\n", "\n  deploy <remote> [--dry-run]                deploy a packaged theme release\n", "\n  rollback <remote> [--dry-run]              roll back to the previous theme release\n"} {
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
			"app1": map[string]any{"id": 98222343, "name": "app1", "provider": "linode", "hostname": "app1.nonfiction.dev", "ssh": map[string]any{"user": "nonfiction", "host": "app1.nonfiction.dev"}},
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
			"client-app1-production": map[string]any{"provider": "linode", "server": "app1", "hostname": "client.app1.nonfiction.dev", "url": "https://client.app1.nonfiction.dev/", "branch": "main", "environment": "production"},
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
		"version":   1,
		"project":   map[string]any{"slug": "client", "name": "Client", "type": "wordpress-theme"},
		"wordpress": map[string]any{"deploy_unit": "theme", "theme_slug": "theme", "theme_path": "theme"},
		"artifact":  map[string]any{"path": "dist/client-v{version}.zip"},
		"remotes":   map[string]any{},
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
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
		if got := Run([]string{"site", "show", "client-app1-production", "--json"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, wanted := range []string{`"requested_target": "client-app1-production"`, `"resolved_target": "client-app1-production"`, `"resolved_target_summary": "app1 / id 98222343 / linode / ssh nonfiction@app1.nonfiction.dev"`, `"url": "https://client.app1.nonfiction.dev/"`} {
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
	servers := map[string]any{"servers": map[string]any{"app1": map[string]any{"id": 98222343, "name": "app1", "provider": "linode", "ssh": map[string]any{"user": "nonfiction", "host": "app1.nonfiction.dev"}}}}
	serverData, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "servers.json"), append(serverData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	sites := map[string]any{"sites": map[string]any{"client-app1-production": map[string]any{"provider": "linode", "server": "app1", "hostname": "client.app1.nonfiction.dev", "branch": "main"}}}
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
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client"}, "remotes": map[string]any{}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
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
		if got := Run([]string{"site", "show", "client-app1-production", "--json"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, wanted := range []string{`"requested_target": "client-app1-production"`, `"resolved_target": "client-app1-production"`, `"server": "app1"`} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("Run() output missing %q:\n%s", wanted, output)
		}
	}
}

func TestRunSiteShowWithoutSitePromptsPicker(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	sites := map[string]any{"sites": map[string]any{
		"foobar-live":    map[string]any{"provider": "linode", "site_id": "foobar-app1-linode", "name": "foobar", "target": "app1-linode", "env": "live", "url": "https://foobar.app1-linode.nonfiction.dev/"},
		"foobar-staging": map[string]any{"provider": "linode", "site_id": "foobar-app1-linode", "name": "foobar", "target": "app1-linode", "env": "staging", "url": "https://foobar-staging.app1-linode.nonfiction.dev/"},
	}}
	data, err := json.MarshalIndent(sites, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "sites.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(sites) error = %v", err)
	}
	oldSelect := siteSelectFn
	var selectTitle string
	var selectOptions []ui.SelectOption
	siteSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectTitle = title
		selectOptions = append([]ui.SelectOption(nil), options...)
		return "foobar-app1-linode", nil
	}
	t.Cleanup(func() { siteSelectFn = oldSelect })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "show"}); got != 0 {
			t.Fatalf("Run(site show) = %d, want 0", got)
		}
	})
	if selectTitle != "Choose a site to show" {
		t.Fatalf("select title = %q", selectTitle)
	}
	if len(selectOptions) != 1 || selectOptions[0] != (ui.SelectOption{Value: "foobar-app1-linode", Label: "foobar-app1-linode"}) {
		t.Fatalf("select options = %#v", selectOptions)
	}
	for _, want := range []string{"foobar-app1-linode", "Site       foobar-app1-linode", "Name       foobar", "Provider   linode", "Target     app1-linode", "Environments:", "env", "php", "url", "live", "staging"} {
		if !strings.Contains(output, want) {
			t.Fatalf("site show output missing %q:\n%s", want, output)
		}
	}
	jsonOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "show", "--json"}); got != 0 {
			t.Fatalf("Run(site show --json) = %d, want 0", got)
		}
	})
	for _, want := range []string{`"site_id": "foobar-app1-linode"`, `"name": "foobar"`, `"env": "live"`, `"env": "staging"`} {
		if !strings.Contains(jsonOutput, want) {
			t.Fatalf("site show --json output missing %q:\n%s", want, jsonOutput)
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
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client"}, "remotes": map[string]any{"production": "client-kinsta:live"}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
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
		if got := Run([]string{"site", "show", "production", "--json"}); got != 0 {
			t.Fatalf("Run(site show production) = %d, want 0", got)
		}
	})
	for _, want := range []string{`"requested_site": "client-kinsta"`, `"requested_env": "live"`, `"resolved_site": "client-kinsta"`, `"resolved_env": "live"`, `"resolved_target": "kinsta"`, `"kinsta_site_id": "ksite123"`, `"kinsta_environment_id": "kenv123"`} {
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
	data, err := os.ReadFile(filepath.Join(workdir, "nf.json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if metadata["version"] != float64(1) {
		t.Fatalf("version = %v, want 1", metadata["version"])
	}
	if got := strings.Index(string(data), "\"version\""); got < 0 || got > strings.Index(string(data), "\"project\"") {
		t.Fatalf("nf.json top-level order = %s, want version before project", data)
	}
	if project, ok := metadata["project"].(map[string]any); !ok || project["slug"] != "client" {
		t.Fatalf("project block = %#v, want slug client", metadata["project"])
	} else if _, exists := project["name"]; exists {
		t.Fatalf("project block = %#v, did not want name", metadata["project"])
	}
	if wordpress, ok := metadata["wordpress"].(map[string]any); !ok || wordpress["theme_path"] != "theme" || wordpress["theme_slug"] != "client" {
		t.Fatalf("wordpress block = %#v, want theme_path theme and theme_slug client", metadata["wordpress"])
	} else if plugins, ok := wordpress["plugins"].([]any); !ok || len(plugins) != 0 {
		t.Fatalf("wordpress.plugins = %#v, want empty list", wordpress["plugins"])
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
	if artifact, ok := metadata["artifact"].(map[string]any); !ok || artifact["path"] != "dist/client-v{version}.zip" {
		t.Fatalf("artifact block = %#v, want dist/client-v{version}.zip", metadata["artifact"])
	}
	if remotes, ok := metadata["remotes"].(map[string]any); !ok || len(remotes) != 0 {
		t.Fatalf("remotes = %#v, want empty map", metadata["remotes"])
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
	for _, dropped := range []string{"build", "deploy"} {
		if _, exists := metadata[dropped]; exists {
			t.Fatalf("%s unexpectedly present: %#v", dropped, metadata[dropped])
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
	data, err := os.ReadFile(filepath.Join(repoRoot, "nf.json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if project, ok := metadata["project"].(map[string]any); !ok || project["slug"] != "client-site" {
		t.Fatalf("project block = %#v, want slug client-site", metadata["project"])
	} else if _, exists := project["name"]; exists {
		t.Fatalf("project block = %#v, did not want name", metadata["project"])
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
	data, err := os.ReadFile(filepath.Join(workdir, "nf.json"))
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
	projectPath := filepath.Join(workdir, "nf.json")
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
		t.Fatalf("nf.json changed unexpectedly: %q", string(data))
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
	for _, want := range []string{"wp-app:", "wp-cli:", "condition: service_healthy", "depends_on:\n      wp-app:", "working_dir: /var/www/html", "HOME: /tmp", "WP_CLI_CACHE_DIR: /tmp/wp-cli-cache", filepath.Join(root, "theme-src") + ":/var/www/html/wp-content/themes/theme-slot", config.SnapshotProjectDir("client") + ":/env-snapshots"} {
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
	writeTestWPDefaults(t, "test-salt")
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

	projectPath := filepath.Join(repoRoot, "nf.json")
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
		t.Fatalf("ReadFile(nf.json) error = %v", err)
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
	if err := os.MkdirAll(filepath.Join(workdir, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll(theme) error = %v", err)
	}
	project := map[string]any{
		"version":   1,
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
	if err := os.WriteFile(filepath.Join(workdir, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
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

func writeTestWPDefaults(t *testing.T, salt string) {
	t.Helper()
	t.Setenv("NF_PASSWORD_SALT", salt)
	if err := saveGlobalConfig(map[string]string{"default_wp_email": "web@nonfiction.ca", "default_wp_user": "admin"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
}

func TestRenderEnvFileUsesComposeProjectName(t *testing.T) {
	wpPort, mailpitPort := envDerivedPorts("client")
	cfg := envConfig{ProjectSlug: "client", WordpressPort: wpPort, MailpitPort: mailpitPort}
	want := fmt.Sprintf("COMPOSE_PROJECT_NAME=nf_client_env\nWP_PORT=%d\nMAILPIT_PORT=%d\nDB_NAME=client\nDB_USER=client\nDB_PASSWORD=wordpress\nDB_ROOT_PASSWORD=root\nWP_URL=http://localhost:%d\nWP_TITLE=Client\nADMIN_USER=admin\nADMIN_PASSWORD=admin\nADMIN_EMAIL=web@nonfiction.ca\n", wpPort, mailpitPort, wpPort)
	if got := renderEnvFile(cfg); got != want {
		t.Fatalf("renderEnvFile() = %q, want %q", got, want)
	}
}

func TestEnvConfigWithAdminCredentialsUsesGlobalDefaultsAndProjectSlug(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := saveGlobalConfig(map[string]string{"default_wp_email": "web@nonfiction.ca", "default_wp_user": "owner"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	cfg, err := envConfigWithAdminCredentials(envConfig{ProjectSlug: "foobar"})
	if err != nil {
		t.Fatalf("envConfigWithAdminCredentials() error = %v", err)
	}
	if got, want := cfg.AdminUser, "owner"; got != want {
		t.Fatalf("AdminUser = %q, want %q", got, want)
	}
	if got, want := cfg.AdminEmail, "web@nonfiction.ca"; got != want {
		t.Fatalf("AdminEmail = %q, want %q", got, want)
	}
	if got, want := cfg.AdminPassword, passwords.DerivePassword("foobar", "wp-admin", "test-salt"); got != want {
		t.Fatalf("AdminPassword = %q, want %q", got, want)
	}
}

func TestRunEnvPasswordPrintsCurrentProjectAdminPassword(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version":   1,
		"project":   map[string]any{"slug": "foobar", "name": "FooBar"},
		"wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"},
		"env":       map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
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
		if got := Run([]string{"env", "password"}); got != 0 {
			t.Fatalf("Run(env password) = %d, want 0", got)
		}
	})
	want := passwords.DerivePassword("foobar", "wp-admin", "test-salt") + "\n"
	if output != want {
		t.Fatalf("Run(env password) output = %q, want %q", output, want)
	}
}

func TestRunEnvPasswordRejectsArgs(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), []byte("{\n  \"version\": 1\n}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
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
		if got := Run([]string{"env", "password", "foobar"}); got != 1 {
			t.Fatalf("Run(env password arg) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "env password takes no arguments") {
		t.Fatalf("stderr = %q, want no-args error", stderr)
	}
}

func TestRunEnvPluginsListReadsWordPressPlugins(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version": 1,
		"project": map[string]any{"slug": "client", "name": "Client"},
		"wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme", "plugins": []any{
			"stream",
			map[string]any{"slug": "acf-pro", "source": "$NF_PLUGIN_ACF_PRO_ZIP", "activate": true},
			map[string]any{"slug": "query-monitor", "activate": false},
		}},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
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
		if got := Run([]string{"env", "plugins", "list"}); got != 0 {
			t.Fatalf("Run(env plugins list) = %d, want 0", got)
		}
	})
	for _, want := range []string{"plugin", "source", "activate", "auto-update", "acf-pro", "$NF_PLUGIN_ACF_PRO_ZIP", "query-monitor", "no", "stream", "wordpress.org", "yes"} {
		if !strings.Contains(output, want) {
			t.Fatalf("env plugins list output missing %q:\n%s", want, output)
		}
	}
}

func TestRunEnvPluginsAddUpdatesProjectMetadata(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version":   1,
		"project":   map[string]any{"slug": "client", "name": "Client"},
		"wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme", "plugins": []any{"stream"}},
		"env":       map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
		"remotes":   map[string]any{},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	projectPath := filepath.Join(repoRoot, "nf.json")
	if err := os.WriteFile(projectPath, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
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
		if got := Run([]string{"env", "plugins", "add", "acf-pro", "--source", "$NF_PLUGIN_ACF_PRO_ZIP", "--no-auto-update"}); got != 0 {
			t.Fatalf("Run(env plugins add) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Added WordPress plugin acf-pro to nf.json.") {
		t.Fatalf("env plugins add output unexpected:\n%s", output)
	}
	updated, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("ReadFile(nf.json) error = %v", err)
	}
	text := string(updated)
	for _, want := range []string{`"version": 1`, `"wordpress":`, `"plugins": [`, `"stream"`, `"slug": "acf-pro"`, `"source": "$NF_PLUGIN_ACF_PRO_ZIP"`, `"auto_update": false`} {
		if !strings.Contains(text, want) {
			t.Fatalf("nf.json missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, `"activate": true`) || strings.Contains(text, `"auto_update": true`) {
		t.Fatalf("nf.json wrote noisy true defaults:\n%s", text)
	}
}

func TestRunEnvPluginsAddCreatesWordPressPlugins(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), []byte("{\n  \"version\": 1,\n  \"project\": {\n    \"slug\": \"client\"\n  }\n}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	if got := Run([]string{"env", "plugins", "add", "stream"}); got != 0 {
		t.Fatalf("Run(env plugins add) = %d, want 0", got)
	}
	metadata, err := loadProjectMetadataOrError(repoRoot)
	if err != nil {
		t.Fatalf("loadProjectMetadataOrError() error = %v", err)
	}
	plugins := mapMapAtPath(metadata, "wordpress")["plugins"].([]any)
	if len(plugins) != 1 || plugins[0] != "stream" {
		t.Fatalf("wordpress.plugins = %#v, want [stream]", plugins)
	}
}

func TestRunEnvPluginsAddRejectsDuplicate(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client"}, "wordpress": map[string]any{"plugins": []any{"stream"}}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
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
		if got := Run([]string{"env", "plugins", "add", "stream"}); got != 1 {
			t.Fatalf("Run(env plugins add duplicate) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, `nf.json wordpress.plugins already contains "stream"`) {
		t.Fatalf("duplicate stderr unexpected:\n%s", stderr)
	}
}

func TestRunEnvPluginsRemoveUpdatesProjectMetadata(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client"}, "wordpress": map[string]any{"plugins": []any{"stream", map[string]any{"slug": "acf-pro", "source": "$NF_PLUGIN_ACF_PRO_ZIP"}}}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	projectPath := filepath.Join(repoRoot, "nf.json")
	if err := os.WriteFile(projectPath, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
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
		if got := Run([]string{"env", "plugins", "rm", "stream"}); got != 0 {
			t.Fatalf("Run(env plugins rm) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Removed WordPress plugin stream from nf.json.") {
		t.Fatalf("env plugins rm output unexpected:\n%s", output)
	}
	updated, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("ReadFile(nf.json) error = %v", err)
	}
	text := string(updated)
	if strings.Contains(text, `"stream"`) {
		t.Fatalf("nf.json still contains removed plugin:\n%s", text)
	}
	for _, want := range []string{`"slug": "acf-pro"`, `"source": "$NF_PLUGIN_ACF_PRO_ZIP"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("nf.json missing %q:\n%s", want, text)
		}
	}
}

func TestRunEnvPluginsRemoveRejectsMissing(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client"}, "wordpress": map[string]any{"plugins": []any{"stream"}}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
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
		if got := Run([]string{"env", "plugins", "remove", "missing"}); got != 1 {
			t.Fatalf("Run(env plugins remove missing) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, `nf.json wordpress.plugins does not contain "missing"`) {
		t.Fatalf("missing stderr unexpected:\n%s", stderr)
	}
}

func TestRunEnvPluginsInstallInstallsMissingAndActivatesInstalled(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version": 1,
		"project": map[string]any{"slug": "client", "name": "Client"},
		"wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme", "plugins": []any{
			"stream",
			map[string]any{"slug": "acf-pro", "source": "$NF_PLUGIN_ACF_PRO_ZIP", "activate": true},
		}},
		"env": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	dockerDir := t.TempDir()
	logPath := filepath.Join(dockerDir, "docker-args.txt")
	dockerScript := []byte("#!/bin/sh\nprintf '%s\n' \"$@\" >> \"$DOCKER_LOG\"\ncase \"$*\" in\n  *\"sh -lc\"*) exit 0 ;;\n  *\"wp core is-installed\"*) exit 0 ;;\n  *\"wp theme is-active\"*) exit 0 ;;\nesac\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	t.Setenv("DOCKER_LOG", logPath)
	t.Setenv("NF_PLUGIN_ACF_PRO_ZIP", "https://plugins.example.test/acf-pro.zip")
	t.Setenv("NF_DATA_HOME", t.TempDir())
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
		if got := Run([]string{"env", "plugins", "install"}); got != 0 {
			t.Fatalf("Run(env plugins install) = %d, want 0", got)
		}
	})
	for _, want := range []string{
		"> docker compose run --rm cli wp core is-installed --allow-root",
		"> docker compose run --rm cli wp theme is-active theme --allow-root",
		"> docker compose run --rm cli '<wp plugin bootstrap script>'",
		"WordPress plugins installed.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("env plugins install output missing %q:\n%s", want, output)
		}
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(log) error = %v", err)
	}
	logText := string(logData)
	for _, want := range []string{"wp plugin install stream --activate --allow-root", "wp plugin auto-updates enable stream --allow-root", "wp plugin is-active acf-pro --allow-root", "wp plugin auto-updates enable acf-pro --allow-root", "https://plugins.example.test/acf-pro.zip"} {
		if !strings.Contains(logText, want) {
			t.Fatalf("plugin install script missing %q:\n%s", want, logText)
		}
	}
}

func TestRunEnvPluginsInstallSkipsSatisfiedPlugins(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version":   1,
		"project":   map[string]any{"slug": "client", "name": "Client"},
		"wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme", "plugins": []any{"stream"}},
		"env":       map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	dockerDir := t.TempDir()
	logPath := filepath.Join(dockerDir, "docker-args.txt")
	dockerScript := []byte("#!/bin/sh\nprintf '%s\n' \"$@\" >> \"$DOCKER_LOG\"\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	t.Setenv("DOCKER_LOG", logPath)
	t.Setenv("NF_DATA_HOME", t.TempDir())
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
		if got := Run([]string{"env", "plugins", "install"}); got != 0 {
			t.Fatalf("Run(env plugins install) = %d, want 0", got)
		}
	})
	for _, want := range []string{
		"> docker compose run --rm cli '<wp plugin bootstrap script>'",
		"WordPress plugins installed.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("env plugins install output missing %q:\n%s", want, output)
		}
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(log) error = %v", err)
	}
	logText := string(logData)
	for _, want := range []string{"wp plugin is-installed stream --allow-root", "wp plugin is-active stream --allow-root", "wp plugin auto-updates status stream --enabled-only --field=name --allow-root", "wp plugin auto-updates enable stream --allow-root"} {
		if !strings.Contains(logText, want) {
			t.Fatalf("plugin install script missing %q:\n%s", want, logText)
		}
	}
}

func TestRunEnvPluginsStatusShowsLocalConfiguredState(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version":   1,
		"project":   map[string]any{"slug": "client", "name": "Client"},
		"wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme", "plugins": []any{"stream", map[string]any{"slug": "acf-pro", "source": "private/acf-pro.zip"}}},
		"env":       map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	dockerDir := t.TempDir()
	logPath := filepath.Join(dockerDir, "docker.log")
	dockerScript := []byte("#!/bin/sh\nprintf 'call\\n' >> \"$DOCKER_LOG\"\ncase \"$*\" in\n  *core*is-installed*) printf 'stream\\tyes\\tyes\\tyes\\nacf-pro\\tno\\tno\\tno\\n'; exit 0 ;;\nesac\nexit 1\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	dataDir := t.TempDir()
	t.Setenv("NF_DATA_HOME", dataDir)
	t.Setenv("DOCKER_LOG", logPath)
	if err := os.MkdirAll(filepath.Join(dataDir, "envs", "client"), 0o755); err != nil {
		t.Fatalf("MkdirAll(env dir) error = %v", err)
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
		if got := Run([]string{"env", "plugins", "status"}); got != 0 {
			t.Fatalf("Run(env plugins status) = %d, want 0", got)
		}
	})
	for _, want := range []string{"plugin", "source", "installed", "active", "auto-update", "stream", "wordpress.org", "yes", "acf-pro", "private/acf-pro.zip", "no"} {
		if !strings.Contains(output, want) {
			logData, _ := os.ReadFile(logPath)
			t.Logf("docker log:\n%s", logData)
			t.Fatalf("env plugins status output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "> docker") {
		t.Fatalf("env plugins status printed command previews unexpectedly:\n%s", output)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(docker log) error = %v", err)
	}
	if calls := strings.Count(strings.TrimSpace(string(logData)), "\n") + 1; calls != 1 {
		t.Fatalf("env plugins status made %d docker calls, want 1:\n%s", calls, logData)
	}
}

func TestRunEnvPluginsDiffShowsLocalDrift(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version": 1,
		"project": map[string]any{"slug": "client", "name": "Client"},
		"wordpress": map[string]any{"plugins": []any{
			"stream",
			"wp-crontrol",
			map[string]any{"slug": "acf-pro", "source": "private/acf-pro.zip"},
		}},
		"env": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	dockerDir := t.TempDir()
	dockerScript := []byte("#!/bin/sh\ncase \"$*\" in\n  *core*is-installed*) printf 'stream\\tyes\\tyes\\tyes\\nwp-crontrol\\tyes\\tyes\\tno\\nacf-pro\\tno\\tno\\tno\\nakismet\\tyes\\tno\\tno\\textra\\nimsanity\\tyes\\tyes\\tyes\\textra\\n'; exit 0 ;;\nesac\nexit 1\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	dataDir := t.TempDir()
	t.Setenv("NF_DATA_HOME", dataDir)
	if err := os.MkdirAll(filepath.Join(dataDir, "envs", "client"), 0o755); err != nil {
		t.Fatalf("MkdirAll(env dir) error = %v", err)
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

	var got int
	output := captureStdout(t, func() {
		got = Run([]string{"env", "plugins", "diff"})
	})
	if got != 2 {
		t.Fatalf("Run(env plugins diff) = %d, want 2", got)
	}
	for _, want := range []string{"Plugin diff:", "plugin", "change", "stream", "ok", "wp-crontrol", "enable auto-update", "acf-pro", "source unavailable locally", "akismet", "extra (inactive, auto-update off)", "imsanity", "extra (active, auto-update on)"} {
		if !strings.Contains(output, want) {
			t.Fatalf("env plugins diff output missing %q:\n%s", want, output)
		}
	}
}

func TestRunEnvPluginsDiffReturnsZeroWhenLocalSatisfied(t *testing.T) {
	statuses := []wordpressPluginStatus{{Plugin: wordpressPluginSpec{Slug: "stream", Source: "wordpress.org", Activate: true, AutoUpdate: true}, Installed: true, Active: true, AutoUpdate: true}}
	output := captureStdout(t, func() {
		if got := printWordPressPluginDiff("Plugin diff:", nil, statuses); got != 0 {
			t.Fatalf("printWordPressPluginDiff() = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "ok") {
		t.Fatalf("plugin diff output missing ok:\n%s", output)
	}
}

func TestRunEnvPluginsStatusRemoteShowsConfiguredState(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client"}, "wordpress": map[string]any{"plugins": []any{"stream", map[string]any{"slug": "acf-pro", "source": "private/acf-pro.zip"}}}, "remotes": map[string]any{"production": "client-kinsta:live"}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	oldRunSSHOutput := runSSHOutputFn
	var sshArgs []string
	runSSHOutputFn = func(args []string) ([]byte, error) {
		sshArgs = append([]string(nil), args...)
		return []byte("stream\tyes\tyes\tyes\nacf-pro\tno\tno\tno\n"), nil
	}
	t.Cleanup(func() { runSSHOutputFn = oldRunSSHOutput })

	output := captureStdout(t, func() {
		if got := Run([]string{"env", "plugins", "status", "production"}); got != 0 {
			t.Fatalf("Run(env plugins status remote) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Plugin status:", "remote:   production", "site:     client-kinsta", "env:      live", "provider: kinsta", "stream", "wordpress.org", "yes", "acf-pro", "private/acf-pro.zip", "no"} {
		if !strings.Contains(output, want) {
			t.Fatalf("env plugins remote status output missing %q:\n%s", want, output)
		}
	}
	if len(sshArgs) != 5 || sshArgs[0] != "ssh" || sshArgs[3] != "client@203.0.113.10" {
		t.Fatalf("ssh args = %#v", sshArgs)
	}
	script := sshArgs[len(sshArgs)-1]
	for _, want := range []string{"wp_cmd plugin is-installed stream", "wp_cmd plugin is-active stream", "wp_cmd plugin auto-updates status stream --enabled-only --field=name", "printf '%s\\t%s\\t%s\\t%s\\n' stream"} {
		if !strings.Contains(script, want) {
			t.Fatalf("remote plugin status script missing %q:\n%s", want, script)
		}
	}
}

func TestRunEnvPluginsDiffRemoteShowsDrift(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client"}, "wordpress": map[string]any{"plugins": []any{"stream", "wp-crontrol"}}, "remotes": map[string]any{"production": "client-kinsta:live"}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	oldRunSSHOutput := runSSHOutputFn
	runSSHOutputFn = func(args []string) ([]byte, error) {
		return []byte("stream\tyes\tyes\tyes\nwp-crontrol\tno\tno\tno\nimsanity\tyes\tyes\tyes\textra\n"), nil
	}
	t.Cleanup(func() { runSSHOutputFn = oldRunSSHOutput })

	var got int
	output := captureStdout(t, func() {
		got = Run([]string{"env", "plugins", "diff", "production"})
	})
	if got != 2 {
		t.Fatalf("Run(env plugins diff remote) = %d, want 2", got)
	}
	for _, want := range []string{"Plugin diff:", "remote:   production", "site:     client-kinsta", "env:      live", "provider: kinsta", "stream", "ok", "wp-crontrol", "install, activate, enable auto-update", "imsanity", "extra (active, auto-update on)"} {
		if !strings.Contains(output, want) {
			t.Fatalf("env plugins remote diff output missing %q:\n%s", want, output)
		}
	}
}

func TestRunEnvPluginsInstallRemoteDryRunPrintsPlan(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client"}, "wordpress": map[string]any{"plugins": []any{"stream", map[string]any{"slug": "query-monitor", "activate": false, "auto_update": false}, map[string]any{"slug": "acf-pro", "source": "private/acf-pro.zip"}}}, "remotes": map[string]any{"production": "client-kinsta:live"}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	oldRunSSH := runSSHCommandFn
	runSSHCommandFn = func(args []string) error { t.Fatalf("runSSHCommandFn called during dry-run: %#v", args); return nil }
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })

	stdout := captureStdout(t, func() {
		if got := Run([]string{"env", "plugins", "install", "production", "--dry-run"}); got != 0 {
			t.Fatalf("Run(env plugins install remote --dry-run) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Plugin install plan:", "remote:        production", "site:          client-kinsta", "env:           live", "provider:      kinsta", "url:           https://www.example.com/", "environment ssh: client@203.0.113.10", "mode:          dry-run", "uploads:       1 local plugin zip(s)", "stream", "query-monitor", "acf-pro", "private/acf-pro.zip", "Local plugin sources will be uploaded before install:", "acf-pro -> /tmp/nf-plugins-client-kinsta-live-", "No remote plugins were changed."} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("remote plugin dry-run stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestRunEnvPluginsInstallRemoteUploadsLocalZipSource(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(repoRoot, "private"), 0o755); err != nil {
		t.Fatalf("Mkdir(private) error = %v", err)
	}
	localZip := filepath.Join(repoRoot, "private", "acf-pro.zip")
	if err := os.WriteFile(localZip, []byte("zip"), 0o644); err != nil {
		t.Fatalf("WriteFile(local zip) error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client"}, "wordpress": map[string]any{"plugins": []any{map[string]any{"slug": "acf-pro", "source": "private/acf-pro.zip"}}}, "remotes": map[string]any{"production": "client-kinsta:live"}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	oldRunSSH := runSSHCommandFn
	var sshCommands [][]string
	runSSHCommandFn = func(args []string) error {
		sshCommands = append(sshCommands, append([]string(nil), args...))
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })
	oldRunRsync := runRsyncCommandFn
	var rsyncCommands [][]string
	runRsyncCommandFn = func(args []string) error {
		rsyncCommands = append(rsyncCommands, append([]string(nil), args...))
		return nil
	}
	t.Cleanup(func() { runRsyncCommandFn = oldRunRsync })

	stdout := captureStdout(t, func() {
		if got := Run([]string{"env", "plugins", "install", "production", "--yes"}); got != 0 {
			t.Fatalf("Run(env plugins install remote local zip --yes) = %d, want 0", got)
		}
	})
	for _, want := range []string{"uploads:       1 local plugin zip(s)", "Local plugin sources will be uploaded before install:", "acf-pro -> /tmp/nf-plugins-client-kinsta-live-", "> rsync -az -e 'ssh -p 12345' " + localZip + " client@203.0.113.10:/tmp/nf-plugins-client-kinsta-live-", "Remote WordPress plugins installed."} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("remote plugin local zip stdout missing %q:\n%s", want, stdout)
		}
	}
	if len(rsyncCommands) != 1 {
		t.Fatalf("rsync commands len = %d, want 1: %#v", len(rsyncCommands), rsyncCommands)
	}
	if got, want := rsyncCommands[0][4], localZip; got != want {
		t.Fatalf("rsync source = %q, want %q", got, want)
	}
	if !strings.Contains(rsyncCommands[0][5], "client@203.0.113.10:/tmp/nf-plugins-client-kinsta-live-") || !strings.HasSuffix(rsyncCommands[0][5], "/acf-pro.zip") {
		t.Fatalf("rsync destination = %q", rsyncCommands[0][5])
	}
	if len(sshCommands) != 3 {
		t.Fatalf("ssh commands len = %d, want mkdir/install/cleanup: %#v", len(sshCommands), sshCommands)
	}
	if !strings.Contains(sshCommands[0][len(sshCommands[0])-1], "mkdir -p /tmp/nf-plugins-client-kinsta-live-") {
		t.Fatalf("mkdir ssh command = %#v", sshCommands[0])
	}
	script := sshCommands[1][len(sshCommands[1])-1]
	for _, want := range []string{"wp_cmd plugin is-installed acf-pro", "wp_cmd plugin install /tmp/nf-plugins-client-kinsta-live-", "/acf-pro.zip --activate", "wp_cmd plugin auto-updates enable acf-pro"} {
		if !strings.Contains(script, want) {
			t.Fatalf("remote plugin local zip script missing %q:\n%s", want, script)
		}
	}
	if !strings.Contains(sshCommands[2][len(sshCommands[2])-1], "rm -rf /tmp/nf-plugins-client-kinsta-live-") {
		t.Fatalf("cleanup ssh command = %#v", sshCommands[2])
	}
}

func TestRunEnvPluginsInstallRemoteExecutesBootstrapScriptWithYes(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	t.Setenv("NF_PLUGIN_STREAM_ZIP", "https://plugins.example.test/stream.zip")
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev", "port": "22"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "client.app1-linode", "env": "live", "target": "app1-linode", "url": "https://client.app1.nonfiction.dev/", "path": "/var/www/sites/client/public", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev", "port": "22"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client"}, "wordpress": map[string]any{"plugins": []any{map[string]any{"slug": "stream", "source": "$NF_PLUGIN_STREAM_ZIP"}}}, "remotes": map[string]any{"production": "client.app1-linode:live"}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	oldRunSSH := runSSHCommandFn
	var sshCommands [][]string
	runSSHCommandFn = func(args []string) error {
		sshCommands = append(sshCommands, append([]string(nil), args...))
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })

	stdout := captureStdout(t, func() {
		if got := Run([]string{"env", "plugins", "install", "production", "--yes"}); got != 0 {
			t.Fatalf("Run(env plugins install remote --yes) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Plugin install plan:", "provider:      linode", "> ssh -p 22 nonfiction@app1-linode.nonfiction.dev '<wp plugin bootstrap script>'", "Remote WordPress plugins installed."} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("remote plugin install stdout missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "https://plugins.example.test/stream.zip") {
		t.Fatalf("remote plugin install stdout leaked expanded source URL:\n%s", stdout)
	}
	if len(sshCommands) != 1 {
		t.Fatalf("ssh commands len = %d, want 1: %#v", len(sshCommands), sshCommands)
	}
	if got, want := sshCommands[0][:4], []string{"ssh", "-p", "22", "nonfiction@app1-linode.nonfiction.dev"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh command prefix = %#v, want %#v", got, want)
	}
	script := sshCommands[0][len(sshCommands[0])-1]
	for _, want := range []string{"set -eu", "cd /var/www/sites/client/public", "wp_cmd() { sudo -u www-data wp --path=/var/www/sites/client/public \"$@\"; }", "wp_cmd plugin is-installed stream", "wp_cmd plugin install https://plugins.example.test/stream.zip --activate", "wp_cmd plugin auto-updates status stream --enabled-only --field=name | grep -qx stream", "wp_cmd plugin auto-updates enable stream"} {
		if !strings.Contains(script, want) {
			t.Fatalf("remote plugin script missing %q:\n%s", want, script)
		}
	}
}

func TestRunEnvPluginsInstallRemotePromptsBeforeExecution(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client"}, "wordpress": map[string]any{"plugins": []any{"stream"}}, "remotes": map[string]any{"production": "client-kinsta:live"}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	oldConfirm := envRemoteSyncConfirm
	var message string
	envRemoteSyncConfirm = func(prompt string, def bool) (bool, error) {
		message = prompt
		return false, nil
	}
	t.Cleanup(func() { envRemoteSyncConfirm = oldConfirm })
	oldRunSSH := runSSHCommandFn
	runSSHCommandFn = func(args []string) error {
		t.Fatalf("runSSHCommandFn called after denied confirmation: %#v", args)
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })

	stderr := captureStderr(t, func() {
		if got := Run([]string{"env", "plugins", "install", "production"}); got != 1 {
			t.Fatalf("Run(env plugins install remote denied) = %d, want 1", got)
		}
	})
	if !strings.Contains(message, "Install configured WordPress plugins on client-kinsta:live (production)?") {
		t.Fatalf("confirm message = %q", message)
	}
	if !strings.Contains(stderr, "Aborted.") {
		t.Fatalf("stderr = %q, want abort", stderr)
	}
}

func TestRenderEnvInfoUsesEffectivePorts(t *testing.T) {
	cfg := envConfig{ProjectSlug: "client", EnvDir: filepath.Join("/data", "envs", "client"), WordpressPort: 18432, MailpitPort: 18433}
	want := "client:local\n────────────\nSite       client\nEnv        local\nURL        http://localhost:18432\nPath       /data/envs/client\nPHP        8.3\nDatabase   client\nCompose    nf_client_env\nMailpit    http://localhost:18433"
	if got := renderEnvInfo(cfg, true); got != want {
		t.Fatalf("renderEnvInfo(full) = %q, want %q", got, want)
	}
	want = "client:local\n────────────\nSite       client\nEnv        local\nPath       /data/envs/client\nPHP        8.3\nDatabase   client\nCompose    nf_client_env"
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

func TestPreflightEnvPortsAllowsExistingManagedEnv(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_DATA_HOME", configHome)
	wpPort, mailpitPort := envDerivedPorts("client")
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", wpPort))
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	cfg := envConfig{ProjectSlug: "client", EnvDir: config.EnvDir("client"), WordpressPort: wpPort, MailpitPort: mailpitPort}
	if err := os.MkdirAll(cfg.EnvDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.EnvDir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(compose) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.EnvDir, ".env"), []byte("COMPOSE_PROJECT_NAME=nf_client_env\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}

	if err := preflightEnvPorts(cfg); err != nil {
		t.Fatalf("preflightEnvPorts() error = %v, want existing managed env allowed", err)
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
	if got, want := envShellArgs(cfg), []string{"docker", "compose", "exec", "wordpress", "bash"}; !reflect.DeepEqual(got, want) {
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
	if got, want := (envCommandRunner{name: "shell", cfg: cfg}).Render(), "docker compose exec wordpress bash"; got != want {
		t.Fatalf("shell Render() = %q, want %q", got, want)
	}
}

func TestEnsureManagedEnvWritesManagedFiles(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	writeTestWPDefaults(t, "test-salt")
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
	credentialCfg, err := envConfigWithAdminCredentials(cfg)
	if err != nil {
		t.Fatalf("envConfigWithAdminCredentials() error = %v", err)
	}
	if err := ensureManagedEnv(credentialCfg); err != nil {
		t.Fatalf("ensureManagedInstance() error = %v", err)
	}
	adminPassword := passwords.DerivePassword("client", "wp-admin", "test-salt")
	checks := map[string][]string{
		filepath.Join(cfg.EnvDir, "docker-compose.yml"):                   {filepath.Join(root, "theme") + ":/var/www/html/wp-content/themes/theme", "mailpit", "wordpress:cli-php8.4"},
		filepath.Join(cfg.EnvDir, ".env"):                                 {"COMPOSE_PROJECT_NAME=nf_client_env", fmt.Sprintf("WP_PORT=%d", wpPort), fmt.Sprintf("MAILPIT_PORT=%d", mailpitPort), fmt.Sprintf("WP_URL=http://localhost:%d", wpPort), "WP_TITLE=Client", "ADMIN_USER=admin", "ADMIN_PASSWORD=" + adminPassword, "ADMIN_EMAIL=web@nonfiction.ca"},
		filepath.Join(cfg.EnvDir, "php", "uploads.ini"):                   {"upload_max_filesize=128M", "max_execution_time=120"},
		filepath.Join(cfg.EnvDir, "wordpress", "Dockerfile"):              {"FROM wordpress:php8.3-apache", "COPY wordpress/wordpress-rewrites.conf"},
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
	if err := os.MkdirAll(filepath.Join(repoRoot, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "theme", "style.css"), []byte("/* demo */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	project := map[string]any{
		"version":   1,
		"project":   map[string]any{"slug": "client-site", "name": "Client Site", "type": "wordpress-theme"},
		"wordpress": map[string]any{"deploy_unit": "theme", "theme_slug": "theme", "theme_path": "theme"},
		"env":       map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
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
	writeTestWPDefaults(t, "test-salt")
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
	if err := os.MkdirAll(filepath.Join(repoRoot, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "theme", "style.css"), []byte("/* demo */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	project := map[string]any{
		"version":   1,
		"project":   map[string]any{"slug": "client-site", "name": "Client Site", "type": "wordpress-theme"},
		"wordpress": map[string]any{"deploy_unit": "theme", "theme_slug": "theme", "theme_path": "theme"},
		"env":       map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
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
	writeTestWPDefaults(t, "test-salt")
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

func TestRunEnvUpBootstrapsMissingThemeDependencies(t *testing.T) {
	repoRoot := filepath.Join(t.TempDir(), "client-site")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.git) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll(theme) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "theme", "composer.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(composer.json) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "theme", "package.json"), []byte(`{"scripts":{"build":"vite build"}}`), 0o644); err != nil {
		t.Fatalf("WriteFile(package.json) error = %v", err)
	}
	project := map[string]any{
		"version":   1,
		"project":   map[string]any{"slug": "client-site", "name": "Client Site", "type": "wordpress-theme"},
		"wordpress": map[string]any{"deploy_unit": "theme", "theme_slug": "theme", "theme_path": "theme"},
		"env":       map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
		"tasks": map[string]any{
			"composer": map[string]any{"description": "Install Composer dependencies", "run": "mkdir -p theme/vendor && touch theme/vendor/autoload.php"},
			"npm":      map[string]any{"description": "Install npm dependencies", "run": "mkdir -p theme/node_modules && touch theme/node_modules/.installed"},
			"build":    map[string]any{"description": "Build theme assets", "run": "mkdir -p theme/dist && touch theme/dist/manifest.json"},
		},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}

	dockerDir := t.TempDir()
	dockerScript := []byte("#!/bin/sh\ncase \"$*\" in\n  *\"wp core is-installed\"*) exit 0 ;;\n  *\"wp theme is-active\"*) exit 0 ;;\nesac\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	writeTestWPDefaults(t, "test-salt")
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
			t.Fatalf("Run(env up) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Theme bootstrap: running nf theme composer", "Theme bootstrap: running nf theme npm", "Theme bootstrap: running nf theme build"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run(env up) output missing %q:\n%s", want, output)
		}
	}
	for _, path := range []string{filepath.Join(repoRoot, "theme", "vendor", "autoload.php"), filepath.Join(repoRoot, "theme", "node_modules", ".installed"), filepath.Join(repoRoot, "theme", "dist", "manifest.json")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected bootstrap output %s: %v", path, err)
		}
	}
}

func TestRunEnvResetPrintsUnderlyingCommands(t *testing.T) {
	repoRoot := filepath.Join(t.TempDir(), "client-site")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "theme", "style.css"), []byte("/* demo */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	project := map[string]any{
		"version":   1,
		"project":   map[string]any{"slug": "client-site", "name": "Client Site", "type": "wordpress-theme"},
		"wordpress": map[string]any{"deploy_unit": "theme", "theme_slug": "theme", "theme_path": "theme"},
		"env":       map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
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
	writeTestWPDefaults(t, "test-salt")
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
		"Safety snapshot:",
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
	records, err := loadEnvSnapshots(envConfig{ProjectSlug: "client-site"})
	if err != nil {
		t.Fatalf("loadEnvSnapshots() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("loadEnvSnapshots() returned %d records, want 1", len(records))
	}
	if name := records[0].Metadata.Name; !strings.HasSuffix(name, "-pre-restore") {
		t.Fatalf("reset safety snapshot name = %q, want pre-restore suffix", name)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(log) error = %v", err)
	}
	logText := string(logData)
	if strings.Index(logText, "wp db export") == -1 || strings.Index(logText, "down\n-v\n--remove-orphans") == -1 || strings.Index(logText, "wp db export") > strings.Index(logText, "down\n-v\n--remove-orphans") {
		t.Fatalf("reset did not create safety snapshot before down -v:\n%s", logText)
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
	project := map[string]any{
		"version":   1,
		"project":   map[string]any{"slug": "client", "name": "Client", "type": "wordpress-theme"},
		"wordpress": map[string]any{"deploy_unit": "theme", "theme_slug": "theme", "theme_path": "theme"},
		"artifact":  map[string]any{"path": "release/client-v{version}.zip"},

		"project_slug": "legacy-project",
		"project_name": "Legacy Project",
		"theme_slug":   "legacy-theme",
		"theme_source": "legacy-theme",
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
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

func TestRunThemePackageUsesThemeSlugAsArchiveRoot(t *testing.T) {
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, "theme", "assets"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "theme", "style.css"), []byte("/*\nTheme Name: Demo\nVersion: 2.0.0\n*/\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "theme", "assets", "main.css"), []byte("body{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	project := map[string]any{
		"version":   1,
		"project":   map[string]any{"slug": "client", "name": "Client", "type": "wordpress-theme"},
		"wordpress": map[string]any{"deploy_unit": "theme", "theme_slug": "client", "theme_path": "theme"},
		"artifact":  map[string]any{"path": "dist/client-v{version}.zip"},
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
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

	if got := Run([]string{"theme", "package"}); got != 0 {
		t.Fatalf("Run() = %d, want 0", got)
	}
	zr, err := zip.OpenReader(filepath.Join(workdir, "dist", "client-v2.0.0.zip"))
	if err != nil {
		t.Fatalf("OpenReader() error = %v", err)
	}
	defer zr.Close()
	names := map[string]bool{}
	for _, file := range zr.File {
		names[file.Name] = true
	}
	for _, want := range []string{"client/style.css", "client/assets/main.css"} {
		if !names[want] {
			t.Fatalf("zip entries = %#v, missing %q", names, want)
		}
	}
	if names["theme/style.css"] {
		t.Fatalf("zip entries = %#v, should not use source directory name as archive root", names)
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
	project := map[string]any{
		"version":   1,
		"project":   map[string]any{"slug": "client", "name": "Client", "type": "wordpress-theme"},
		"wordpress": map[string]any{"deploy_unit": "theme", "theme_slug": "theme", "theme_path": "theme"},
		"artifact":  map[string]any{"path": "dist/client-v{version}.zip"},
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
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
	project := map[string]any{
		"version":   1,
		"project":   map[string]any{"slug": "client", "name": "Client", "type": "wordpress-theme"},
		"wordpress": map[string]any{"deploy_unit": "theme", "theme_slug": "theme", "theme_path": "theme"},
		"artifact":  map[string]any{"path": "dist/client-v{version}.zip"},
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
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
	if !strings.Contains(output, "nf project") {
		t.Fatalf("Run() stderr = %q, want nf project message", output)
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
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	writeTestWPDefaults(t, "test-salt")
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "env": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
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
	adminPassword := passwords.DerivePassword("client", "wp-admin", "test-salt")
	for _, want := range []string{"client:local\n", "Site       client\n", "Env        local\n", "Compose    nf_client_env\n", fmt.Sprintf("URL        http://localhost:%d\n", wpPort), fmt.Sprintf("Mailpit    http://localhost:%d", mailpitPort), "Access\n", "  Admin user   admin\n", "  Admin pass   " + adminPassword} {
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
	if err := os.MkdirAll(filepath.Join(workdir, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "theme", "style.css"), []byte("/* demo */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	project := map[string]any{"version": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"}, "env": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
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
	if !strings.Contains(output, "> docker compose exec wordpress bash") {
		t.Fatalf("Run(shell) stdout = %q, want compose exec preview", output)
	}
	args, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := strings.Split(strings.TrimSpace(string(args)), "\n"), []string{"compose", "exec", "wordpress", "bash"}; !reflect.DeepEqual(got, want) {
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

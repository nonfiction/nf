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
	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/state"
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
	for _, wanted := range []string{"\n  init          initialize project metadata\n", "\n  theme         package artifacts and run theme tasks\n", "\n  instance      manage the local WordPress instance\n", "\n  site          list, show, deploy/sync remote sites\n", "\n  server        provision, list, show, delete infrastructure hosts\n", "\n  config        init local config\n", "\n  password      derive passwords\n", "\n  help          show help\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runHelp() output missing %q:\n%s", wanted, output)
		}
	}
	for _, unwanted := range []string{"\n  repo          ", "Shortcuts:", "nf up", "nf shell", "snapshot create", "\n  commands\n", "\n  run <name>\n", "\n  build\n"} {
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
	for _, wanted := range []string{"\n  init          initialize project metadata\n", "\n  theme         package artifacts and run theme tasks\n", "\n  instance      manage the local WordPress instance\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runHelp() output missing %q:\n%s", wanted, output)
		}
	}
	for _, unwanted := range []string{"Shortcuts:", "nf up", "nf shell", "snapshot create", "\n  commands\n", "\n  run <name>\n", "\n  build\n"} {
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

func TestRunServerHelpShowsProvisionFlags(t *testing.T) {
	output := captureStdout(t, func() { _ = runServerHelp() })
	for _, want := range []string{"server\n\nCommands:\n", "\n  provision [flags]   provision an infrastructure host\n", "\n  list                list servers\n", "\n  show <id-or-name>   show a server\n", "\n  root-password <id-or-name>   derive the Linode root password for a server\n"} {
		if !strings.Contains(output, want) {
			t.Fatalf("runServerHelp() output missing %q:\n%s", want, output)
		}
	}
}

func TestRunServerRootPasswordUsesHostnameAndSalt(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_SECRET_SALT", "secret-salt")
	records := []map[string]any{{"provider": "linode", "name": "app1", "hostname": "app1.nfweb.dev", "label": "app1"}}
	if err := state.SaveStateRecords("servers", records); err != nil {
		t.Fatalf("SaveStateRecords() error = %v", err)
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"server", "root-password", "app1"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	wantPassword := passwords.DerivePassword("app1.nfweb.dev", "linode-root", "secret-salt")
	want := "Root password for app1.nfweb.dev:\n\n" + wantPassword + "\n"
	if output != want {
		t.Fatalf("Run() output = %q, want %q", output, want)
	}
}

func TestRunServerRootPasswordMissingSaltFailsClearly(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_SECRET_SALT", "")
	records := []map[string]any{{"provider": "linode", "name": "app1", "hostname": "app1.nfweb.dev", "label": "app1"}}
	if err := state.SaveStateRecords("servers", records); err != nil {
		t.Fatalf("SaveStateRecords() error = %v", err)
	}

	output := captureStderr(t, func() {
		if got := Run([]string{"server", "root-password", "app1"}); got != 1 {
			t.Fatalf("Run() = %d, want 1", got)
		}
	})
	for _, want := range []string{"NF_SECRET_SALT is not set", "nf config init"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() stderr missing %q:\n%s", want, output)
		}
	}
}

func TestRunServerRootPasswordMissingServerFailsClearly(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_SECRET_SALT", "secret-salt")
	if err := state.SaveStateRecords("servers", []map[string]any{}); err != nil {
		t.Fatalf("SaveStateRecords() error = %v", err)
	}

	output := captureStderr(t, func() {
		if got := Run([]string{"server", "root-password", "missing"}); got != 1 {
			t.Fatalf("Run() = %d, want 1", got)
		}
	})
	if !strings.Contains(output, "No server matched \"missing\"") {
		t.Fatalf("Run() stderr = %q, want missing server error", output)
	}
}

func TestRunServerProvisionDryRunShowsDnsZoneFlag(t *testing.T) {
	output := captureStdout(t, func() {
		if got := Run([]string{"server", "provision", "--non-interactive", "--dry-run", "--dns-zone", "example.test", "--ubuntu-version", "24.04"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, want := range []string{"Server provision dry-run plan", "Host", "  provider: linode", "Access", "  ssh user: nonfiction", "  auth: SSH keys only", "  sudo: passwordless", "  key source: linode-profile", "  authorized keys: all Linode profile keys", "  root password: derived from hostname + purpose linode-root", "  root stored in state: no", "  root reveal: nf server root-password app1", "Ubuntu firewall", "  ufw default: deny incoming", "  ufw outbound: allow", "  allow: 22/tcp, 80/tcp, 443/tcp", "Linode firewall", "  provider: linode", "  mode: managed", "  managed label: nf-web", "  inbound: 22/tcp, 80/tcp, 443/tcp", "  inbound policy: DROP", "  outbound policy: ACCEPT", "PHP baseline", "  timezone: UTC", "  swap: 2G", "  zone: example.test (explicit)", "  stack: Ubuntu 24.04 LTS / PHP 8.3", "  ubuntu: 24.04 LTS", "  image: linode/ubuntu24.04", "  php version: 8.3", "  php service: php8.3-fpm", "  php socket: /run/php/php8.3-fpm.sock", "  package source: ubuntu-native", "  packages: php8.3-fpm, php8.3-cli", "Server health URL: https://app1.nfweb.dev", "Paths", "  marker: /etc/nf/server.json", "  motd: /etc/update-motd.d/99-nf", "  sites root: /var/www/sites", "  shared root: /var/www/shared", "  nginx site logs: /var/log/nginx/sites", "Mode", "  dry-run: true", "  hostname A: app1.nfweb.dev -> <created after server IP is known>", "  wildcard A: *.app1.nfweb.dev -> <created after server IP is known>"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, output)
		}
	}
	for _, unwanted := range []string{"PHP-FPM socket:", "php-fpm socket:", "--php-version", "--php-fpm-socket"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("Run() output unexpectedly contained %q:\n%s", unwanted, output)
		}
	}
}

func TestRunServerProvisionHelpOmitsPhpVersionAndSocketFlags(t *testing.T) {
	output := captureStderr(t, func() {
		if got := Run([]string{"server", "provision", "--help"}); got != 1 {
			t.Fatalf("Run() = %d, want 1", got)
		}
	})
	for _, want := range []string{"-firewall string", "-firewall-id string"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() help output missing %q:\n%s", want, output)
		}
	}
	for _, unwanted := range []string{"--php-version", "--php-fpm-socket"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("Run() help output unexpectedly contained %q:\n%s", unwanted, output)
		}
	}
}

func TestRunServerProvisionRejectsExecuteAndDryRunTogether(t *testing.T) {
	output := captureStderr(t, func() {
		if got := Run([]string{"server", "provision", "--non-interactive", "--execute", "--dry-run"}); got != 1 {
			t.Fatalf("Run() = %d, want 1", got)
		}
	})
	if !strings.Contains(output, "Choose either --execute or --dry-run, not both.") {
		t.Fatalf("Run() stderr = %q, want execute/dry-run conflict", output)
	}
}

func TestRunInstanceHelpShowsCommandsAndShortcuts(t *testing.T) {
	output := captureStdout(t, func() { _ = runInstanceHelp() })
	for _, wanted := range []string{"instance\n\nCommands:\n", "\n  up                  start the local instance\n", "\n  down                stop the local instance\n", "\n  shell               open a shell in the local instance\n", "\n  logs                tail WordPress logs\n", "\n  reset               destroy and recreate the local instance\n", "\n  wp -- <args>        run wp-cli in the local instance\n", "\n  info                show local instance paths, ports, and URLs\n", "\n  snapshot            manage/list instance snapshots\n", "\nShortcuts:\n", "\n  nf info             shortcut for nf instance info\n", "\n  nf up               shortcut for nf instance up\n", "\n  nf shell            shortcut for nf instance shell\n", "\n  nf wp -- <args>     shortcut for nf instance wp -- <args>\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runInstanceHelp() output missing %q:\n%s", wanted, output)
		}
	}
	for _, unwanted := range []string{"nf instance snapshots", "snapshot create", "snapshot list", "snapshot restore", "snapshot delete"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("runInstanceHelp() output unexpectedly contained %q:\n%s", unwanted, output)
		}
	}
	if strings.Contains(output, "theme tasks") {
		t.Fatalf("runInstanceHelp() output unexpectedly mentioned theme tasks:\n%s", output)
	}
}

func TestRunInstanceSnapshotHelpShowsDedicatedCommands(t *testing.T) {
	output := captureStdout(t, func() { _ = runInstanceSnapshot([]string{"help"}) })
	for _, want := range []string{"instance snapshot\n\nCommands:\n", "\n  create [name]       create an instance snapshot\n", "\n  list                list instance snapshots\n", "\n  ls                  alias for list\n", "\n  restore [name]      restore an instance snapshot\n", "\n  delete [name]       delete an instance snapshot\n"} {
		if !strings.Contains(output, want) {
			t.Fatalf("runInstanceSnapshot(help) output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "snapshots           alias for list") || strings.Contains(output, "nf instance snapshots") || strings.Contains(output, "shortcut for snapshot list") {
		t.Fatalf("runInstanceSnapshot(help) output unexpectedly mentioned removed alias:\n%s", output)
	}
}

func TestRunInstanceSnapshotLsRoutesToList(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	project := map[string]any{"schema": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"}, "instance": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".nf", "project.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
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
		if got := Run([]string{"instance", "snapshot", "ls"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "No instance snapshots found.") {
		t.Fatalf("Run() output = %q, want empty snapshot message", output)
	}
}

func TestRunInstanceSnapshotsAliasRoutesToList(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	project := map[string]any{"schema": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"}, "instance": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".nf", "project.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
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
		got = Run([]string{"instance", "snapshots"})
	})
	if got != 0 {
		t.Fatalf("Run() = %d, want 0", got)
	}
	if !strings.Contains(output, "No instance snapshots found.") {
		t.Fatalf("Run() output = %q, want empty snapshot message", output)
	}
}

func TestRunInstanceSnapshotCreateSkipsComposeUpWhenReady(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
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
	project := map[string]any{"schema": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"}, "instance": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".nf", "project.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	snapshotDir := filepath.Join(configHome, "snapshots", "client", "demo-snapshot")
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
		if got := Run([]string{"instance", "snapshot", "create", "demo-snapshot"}); got != 0 {
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

func TestRunInstanceSnapshotListShowsSnapshots(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	project := map[string]any{"schema": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"}, "instance": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
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
	snapshotDir := filepath.Join(configHome, "snapshots", "client", "2026-05-28-093012")
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(snapshotDir) error = %v", err)
	}
	meta := instanceSnapshotMetadata{
		Schema:         instanceSnapshotSchema,
		Name:           "2026-05-28-093012",
		ProjectSlug:    "client",
		CreatedAt:      "2026-05-28T09:30:12Z",
		InstancePath:   filepath.Join(configHome, "instances", "client"),
		ComposeProject: "nf_client_instance",
		WordpressURL:   "http://localhost:18432",
		Contents:       instanceSnapshotContents{Database: "database.sql.gz", WpContent: "wp-content.tar.gz", WpContentPaths: instanceSnapshotContentPaths()},
	}
	metaJSON, err := instanceSnapshotMetadataJSON(meta)
	if err != nil {
		t.Fatalf("instanceSnapshotMetadataJSON() error = %v", err)
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
		if got := Run([]string{"instance", "snapshot", "list"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, want := range []string{"2026-05-28-093012", "2026-05-28 09:30:12", "2 B", "7 B", snapshotDir} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, output)
		}
	}
}

func TestRunInstanceSnapshotRestoreSkipsComposeUpWhenReady(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
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
	project := map[string]any{"schema": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"}, "instance": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".nf", "project.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(project.json) error = %v", err)
	}
	sourceSnapshotDir := filepath.Join(configHome, "snapshots", "client", "restore-source")
	if err := os.MkdirAll(sourceSnapshotDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(sourceSnapshotDir) error = %v", err)
	}
	sourceMeta := instanceSnapshotMetadata{
		Schema:         instanceSnapshotSchema,
		Name:           "restore-source",
		ProjectSlug:    "client",
		CreatedAt:      "2026-05-28T09:30:12Z",
		InstancePath:   filepath.Join(configHome, "instances", "client"),
		ComposeProject: "nf_client_instance",
		WordpressURL:   "http://localhost:18432",
		Contents:       instanceSnapshotContents{Database: "database.sql.gz", WpContent: "wp-content.tar.gz", WpContentPaths: instanceSnapshotContentPaths()},
	}
	sourceMetaJSON, err := instanceSnapshotMetadataJSON(sourceMeta)
	if err != nil {
		t.Fatalf("instanceSnapshotMetadataJSON() error = %v", err)
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
	oldIsInteractive := instanceSnapshotIsInteractive
	oldConfirm := instanceSnapshotConfirm
	instanceSnapshotIsInteractive = func() bool { return true }
	instanceSnapshotConfirm = func(prompt string, defaultYes bool) (bool, error) { return true, nil }
	t.Cleanup(func() {
		instanceSnapshotIsInteractive = oldIsInteractive
		instanceSnapshotConfirm = oldConfirm
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
		if got := Run([]string{"instance", "snapshot", "restore", "restore-source"}); got != 0 {
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

func TestRunInstanceSnapshotDeleteRemovesSnapshotAfterConfirmation(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	project := map[string]any{"schema": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"}, "instance": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".nf", "project.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(project.json) error = %v", err)
	}
	snapshotDir := filepath.Join(configHome, "snapshots", "client", "delete-me")
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(snapshotDir) error = %v", err)
	}
	meta := instanceSnapshotMetadata{Schema: instanceSnapshotSchema, Name: "delete-me", ProjectSlug: "client", CreatedAt: "2026-05-28T09:30:12Z", InstancePath: filepath.Join(configHome, "instances", "client"), ComposeProject: "nf_client_instance", WordpressURL: "http://localhost:18432", Contents: instanceSnapshotContents{Database: "database.sql.gz", WpContent: "wp-content.tar.gz", WpContentPaths: instanceSnapshotContentPaths()}}
	metaJSON, err := instanceSnapshotMetadataJSON(meta)
	if err != nil {
		t.Fatalf("instanceSnapshotMetadataJSON() error = %v", err)
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
	oldIsInteractive := instanceSnapshotIsInteractive
	oldConfirm := instanceSnapshotConfirm
	instanceSnapshotIsInteractive = func() bool { return true }
	instanceSnapshotConfirm = func(prompt string, defaultYes bool) (bool, error) { return true, nil }
	t.Cleanup(func() {
		instanceSnapshotIsInteractive = oldIsInteractive
		instanceSnapshotConfirm = oldConfirm
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
		if got := Run([]string{"instance", "snapshot", "delete", "delete-me"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Deleted instance snapshot.") || !strings.Contains(output, "name: delete-me") || !strings.Contains(output, snapshotDir) {
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

func TestRunServerDeleteAcceptsFlagsAfterIdentifier(t *testing.T) {
	configHome := t.TempDir()
	stateDir := filepath.Join(configHome, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	servers := []map[string]any{{"id": 98223448, "name": "test2", "provider": "linode"}}
	data, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "servers.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "sites.json"), []byte("[]\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	oldConfigHome := os.Getenv("NF_CONFIG_HOME")
	if err := os.Setenv("NF_CONFIG_HOME", configHome); err != nil {
		t.Fatalf("Setenv() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Setenv("NF_CONFIG_HOME", oldConfigHome)
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"server", "delete", "test2", "--non-interactive"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Delete server plan:") || !strings.Contains(output, "mode: dry-run") || !strings.Contains(output, "linode-cli linodes delete 98223448 --json") {
		t.Fatalf("Run() output = %q, want dry-run plan", output)
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
	if err := os.Setenv("NF_CONFIG_HOME", configHome); err != nil {
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
	if err := os.Setenv("NF_CONFIG_HOME", configHome); err != nil {
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
	if instance, ok := metadata["instance"].(map[string]any); !ok {
		t.Fatalf("instance block = %#v, want instance config", metadata["instance"])
	} else {
		for key, want := range map[string]string{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"} {
			if got := instance[key]; got != want {
				t.Fatalf("instance.%s = %#v, want %q", key, got, want)
			}
		}
		if _, exists := instance["ports"]; exists {
			t.Fatalf("instance.ports unexpectedly present: %#v", instance["ports"])
		}
		if _, exists := instance["path"]; exists {
			t.Fatalf("instance.path unexpectedly present: %#v", instance)
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

func TestRenderInstanceComposeUsesMetadataDefaults(t *testing.T) {
	root := t.TempDir()
	metadata := map[string]any{
		"project":   map[string]any{"slug": "client"},
		"wordpress": map[string]any{"theme_path": "theme-src"},
		"instance":  map[string]any{"compose": "docker compose", "wordpress_service": "wp-app", "cli_service": "wp-cli", "theme_mount_slug": "theme-slot", "uploads_path": "uploads"},
	}
	cfg, ok := loadInstanceConfig(root, metadata)
	if !ok {
		t.Fatalf("loadInstanceConfig() = false, want true")
	}
	compose := renderInstanceCompose(cfg)
	for _, want := range []string{"wp-app:", "wp-cli:", "condition: service_healthy", "depends_on:\n      wp-app:", "working_dir: /var/www/html", filepath.Join(root, "theme-src") + ":/var/www/html/wp-content/themes/theme-slot", config.SnapshotProjectDir("client") + ":/instance-snapshots"} {
		if !strings.Contains(compose, want) {
			t.Fatalf("renderInstanceCompose() missing %q:\n%s", want, compose)
		}
	}
	if strings.Contains(compose, "\t") {
		t.Fatalf("renderInstanceCompose() contains a tab character:\n%s", compose)
	}
}

func TestInstanceSnapshotHelpersValidateNamesAndRenderMetadata(t *testing.T) {
	if got, want := defaultInstanceSnapshotName(time.Date(2026, 5, 28, 9, 30, 12, 0, time.UTC)), "2026-05-28-093012"; got != want {
		t.Fatalf("defaultInstanceSnapshotName() = %q, want %q", got, want)
	}
	if got, want := defaultPreRestoreSnapshotName(time.Date(2026, 5, 28, 9, 30, 12, 0, time.UTC)), "2026-05-28-093012-pre-restore"; got != want {
		t.Fatalf("defaultPreRestoreSnapshotName() = %q, want %q", got, want)
	}
	for input, want := range map[string]string{"demo snapshot": "demo-snapshot", "  demo   snapshot  ": "demo-snapshot", "snapshot-1": "snapshot-1"} {
		got, err := instanceSnapshotNormalizedName(input)
		if err != nil {
			t.Fatalf("instanceSnapshotNormalizedName(%q) error = %v", input, err)
		}
		if got != want {
			t.Fatalf("instanceSnapshotNormalizedName(%q) = %q, want %q", input, got, want)
		}
	}
	for _, input := range []string{"", "../snapshot", "/tmp/snapshot", "snapshot/name", "snapshot\\name", "snapshot..name", "snapshot.name", "snapshot?name"} {
		if got, err := instanceSnapshotNormalizedName(input); err == nil {
			t.Fatalf("instanceSnapshotNormalizedName(%q) = %q, want error", input, got)
		}
	}
	meta := instanceSnapshotMetadata{
		Schema:         instanceSnapshotSchema,
		Name:           "2026-05-28-093012",
		ProjectSlug:    "client",
		CreatedAt:      "2026-05-28T09:30:12Z",
		InstancePath:   "/config/nf/instances/client",
		ComposeProject: "nf_client_instance",
		WordpressURL:   "http://localhost:18432",
		Contents:       instanceSnapshotContents{Database: "database.sql.gz", WpContent: "wp-content.tar.gz", WpContentPaths: instanceSnapshotContentPaths()},
	}
	gotJSON, err := instanceSnapshotMetadataJSON(meta)
	if err != nil {
		t.Fatalf("instanceSnapshotMetadataJSON() error = %v", err)
	}
	wantJSON := "{\n  \"schema\": 1,\n  \"name\": \"2026-05-28-093012\",\n  \"project_slug\": \"client\",\n  \"created_at\": \"2026-05-28T09:30:12Z\",\n  \"instance_path\": \"/config/nf/instances/client\",\n  \"compose_project\": \"nf_client_instance\",\n  \"wordpress_url\": \"http://localhost:18432\",\n  \"contents\": {\n    \"database\": \"database.sql.gz\",\n    \"wp_content\": \"wp-content.tar.gz\",\n    \"wp_content_paths\": [\n      \"wp-content/uploads\",\n      \"wp-content/plugins\",\n      \"wp-content/mu-plugins\",\n      \"wp-content/languages\"\n    ]\n  }\n}\n"
	if gotJSON != wantJSON {
		t.Fatalf("instanceSnapshotMetadataJSON() =\n%s\nwant=\n%s", gotJSON, wantJSON)
	}
}

func TestRunInstanceUpAutoInitializesProjectMetadata(t *testing.T) {
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
		if got := Run([]string{"instance", "up"}); got != 0 {
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

func TestLoadInstanceConfigUsesInstanceBlock(t *testing.T) {
	root := t.TempDir()
	metadata := map[string]any{
		"project":   map[string]any{"slug": "client", "name": "Client"},
		"wordpress": map[string]any{"theme_path": "theme-src", "theme_slug": "theme"},
		"instance":  map[string]any{"compose": "instance compose", "wordpress_service": "instance-wp", "cli_service": "instance-cli", "theme_mount_slug": "instance-theme", "uploads_path": "instance-uploads"},
	}
	cfg, ok := loadInstanceConfig(root, metadata)
	if !ok {
		t.Fatalf("loadInstanceConfig() = false, want true")
	}
	if got, want := cfg.Compose, "instance compose"; got != want {
		t.Fatalf("Compose = %q, want %q", got, want)
	}
	if got, want := cfg.WordpressService, "instance-wp"; got != want {
		t.Fatalf("WordpressService = %q, want %q", got, want)
	}
	if got, want := cfg.CliService, "instance-cli"; got != want {
		t.Fatalf("CliService = %q, want %q", got, want)
	}
	if got, want := cfg.ThemeMountSlug, "instance-theme"; got != want {
		t.Fatalf("ThemeMountSlug = %q, want %q", got, want)
	}
	if got, want := cfg.UploadsPath, "instance-uploads"; got != want {
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
		"instance":  map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
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

func TestInstanceComposeProjectName(t *testing.T) {
	for input, want := range map[string]string{
		"client":        "nf_client_instance",
		" Client Site ": "nf_client_site_instance",
		"":              "nf_project_instance",
	} {
		if got := instanceComposeProjectName(input); got != want {
			t.Fatalf("instanceComposeProjectName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestInstanceDerivedPortsUseCleanedSlug(t *testing.T) {
	wpA, mailpitA := instanceDerivedPorts(" Client Site ")
	wpB, mailpitB := instanceDerivedPorts("client_site")
	if wpA != wpB || mailpitA != mailpitB {
		t.Fatalf("instanceDerivedPorts() = (%d, %d) and (%d, %d), want matching ports", wpA, mailpitA, wpB, mailpitB)
	}
	if mailpitA != wpA+1 {
		t.Fatalf("instanceDerivedPorts() mailpit = %d, want wordpress+1 (%d)", mailpitA, wpA+1)
	}
	if wpA < 18000 || mailpitA > 21999 {
		t.Fatalf("instanceDerivedPorts() = (%d, %d), want ports in 18000-21999 block", wpA, mailpitA)
	}
}

func TestRenderInstanceEnvUsesComposeProjectName(t *testing.T) {
	wpPort, mailpitPort := instanceDerivedPorts("client")
	cfg := instanceConfig{ProjectSlug: "client", ProjectName: "Client", WordpressPort: wpPort, MailpitPort: mailpitPort}
	want := fmt.Sprintf("COMPOSE_PROJECT_NAME=nf_client_instance\nWP_PORT=%d\nMAILPIT_PORT=%d\nDB_NAME=client\nDB_USER=client\nDB_PASSWORD=wordpress\nDB_ROOT_PASSWORD=root\nWP_URL=http://localhost:%d\nWP_TITLE=Client\nADMIN_USER=admin\nADMIN_PASSWORD=admin\nADMIN_EMAIL=web@nonfiction.ca\n", wpPort, mailpitPort, wpPort)
	if got := renderInstanceEnv(cfg); got != want {
		t.Fatalf("renderInstanceEnv() = %q, want %q", got, want)
	}
}

func TestRenderInstanceInfoUsesEffectivePorts(t *testing.T) {
	cfg := instanceConfig{ProjectSlug: "client", ProjectName: "Client", InstanceDir: filepath.Join("/config", "instances", "client"), WordpressPort: 18432, MailpitPort: 18433}
	want := "Instance:\n  project: client\n  path: /config/instances/client\n  compose project: nf_client_instance\n  WordPress: http://localhost:18432\n  Mailpit:   http://localhost:18433"
	if got := renderInstanceInfo(cfg, true); got != want {
		t.Fatalf("renderInstanceInfo(full) = %q, want %q", got, want)
	}
	want = "Instance:\n  project: client\n  path: /config/instances/client\n  compose project: nf_client_instance"
	if got := renderInstanceInfo(cfg, false); got != want {
		t.Fatalf("renderInstanceInfo(short) = %q, want %q", got, want)
	}
}

func TestLoadInstanceConfigAppliesPortOverrides(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	metadata := map[string]any{
		"project":   map[string]any{"slug": "client", "name": "Client"},
		"wordpress": map[string]any{"theme_slug": "theme", "theme_path": "theme"},
		"instance": map[string]any{
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
	cfg, ok := loadInstanceConfig(root, metadata)
	if !ok {
		t.Fatalf("loadInstanceConfig() = false, want true")
	}
	if cfg.WordpressPort != 19111 || cfg.MailpitPort != 19112 {
		t.Fatalf("effective ports = (%d, %d), want overrides (19111, 19112)", cfg.WordpressPort, cfg.MailpitPort)
	}
}

func TestLoadInstanceConfigFallsBackPerPortIndependently(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	derivedWordpress, derivedMailpit := instanceDerivedPorts("client")
	for _, tc := range []struct {
		name          string
		instancePorts map[string]any
		wantWordpress int
		wantMailpit   int
	}{
		{name: "wordpress override only", instancePorts: map[string]any{"wordpress": 19111, "mailpit": 0}, wantWordpress: 19111, wantMailpit: derivedMailpit},
		{name: "mailpit override only", instancePorts: map[string]any{"wordpress": 0, "mailpit": 19112}, wantWordpress: derivedWordpress, wantMailpit: 19112},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata := map[string]any{
				"project":   map[string]any{"slug": "client", "name": "Client"},
				"wordpress": map[string]any{"theme_slug": "theme", "theme_path": "theme"},
				"instance": map[string]any{
					"compose":           "docker compose",
					"wordpress_service": "wordpress",
					"cli_service":       "cli",
					"theme_mount_slug":  "theme",
					"uploads_path":      "uploads",
					"ports":             tc.instancePorts,
				},
			}
			cfg, ok := loadInstanceConfig(root, metadata)
			if !ok {
				t.Fatalf("loadInstanceConfig() = false, want true")
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

func TestPreflightInstancePortsDetectsSingleCollision(t *testing.T) {
	wpPort, mailpitPort := instanceDerivedPorts("client")
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", wpPort))
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	cfg := instanceConfig{ProjectSlug: "client", InstanceDir: filepath.Join("/config", "instances", "client"), WordpressPort: wpPort, MailpitPort: mailpitPort}
	err = preflightInstancePorts(cfg)
	if err == nil {
		t.Fatal("preflightInstancePorts() error = nil, want collision")
	}
	message := err.Error()
	for _, want := range []string{fmt.Sprintf("Port %d is already in use.", wpPort), fmt.Sprintf("WordPress: http://localhost:%d", wpPort), fmt.Sprintf("Mailpit:   http://localhost:%d", mailpitPort)} {
		if !strings.Contains(message, want) {
			t.Fatalf("preflightInstancePorts() error = %q, want %q", message, want)
		}
	}
}

func TestPreflightInstancePortsDetectsBothCollisions(t *testing.T) {
	wpPort, first, second := openAdjacentPortPair(t)
	t.Cleanup(func() {
		_ = first.Close()
		_ = second.Close()
	})

	cfg := instanceConfig{ProjectSlug: "client", InstanceDir: filepath.Join("/config", "instances", "client"), WordpressPort: wpPort, MailpitPort: wpPort + 1}
	err := preflightInstancePorts(cfg)
	if err == nil {
		t.Fatal("preflightInstancePorts() error = nil, want collision")
	}
	message := err.Error()
	for _, want := range []string{fmt.Sprintf("Ports %d and %d are already in use.", wpPort, wpPort+1), fmt.Sprintf("WordPress: http://localhost:%d", wpPort), fmt.Sprintf("Mailpit:   http://localhost:%d", wpPort+1)} {
		if !strings.Contains(message, want) {
			t.Fatalf("preflightInstancePorts() error = %q, want %q", message, want)
		}
	}
}

func TestInstanceCommandHelpersBuildExpectedArgs(t *testing.T) {
	cfg := instanceConfig{
		ProjectSlug:      "client",
		ProjectName:      "Client",
		RepoRoot:         "/repo",
		ThemePath:        "/repo/theme",
		InstanceDir:      filepath.Join("/config", "instances", "client"),
		Compose:          "docker compose",
		WordpressService: "wordpress",
		CliService:       "cli",
		ThemeMountSlug:   "theme",
		UploadsPath:      "uploads",
		ThemeSlug:        "client",
	}

	if got, want := instanceComposeArgs(cfg, "up", "-d"), []string{"docker", "compose", "up", "-d"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("instanceComposeArgs() = %#v, want %#v", got, want)
	}
	if got, want := instanceWpArgs(cfg, "plugin", "list"), []string{"docker", "compose", "run", "--rm", "cli", "wp", "plugin", "list", "--allow-root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("instanceWpArgs() = %#v, want %#v", got, want)
	}
	if got, want := instanceShellArgs(cfg), []string{"docker", "compose", "exec", "wordpress", "sh"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("instanceShellArgs() = %#v, want %#v", got, want)
	}
	if got, want := instanceWpThemeIsActiveArgs(cfg, ""), []string{"docker", "compose", "run", "--rm", "cli", "wp", "theme", "is-active", "theme", "--allow-root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("instanceWpThemeIsActiveArgs() = %#v, want %#v", got, want)
	}
	hostPath, containerPath := instanceThemeArchivePaths(cfg, "/tmp/theme.zip")
	if hostPath != filepath.Join(cfg.InstanceDir, "uploads", "theme.zip") || containerPath != "/instance/uploads/theme.zip" {
		t.Fatalf("instanceThemeArchivePaths() = (%q, %q), want host and container upload paths", hostPath, containerPath)
	}
	if got, want := instanceCommandDir(cfg), cfg.InstanceDir; got != want {
		t.Fatalf("instanceCommandDir() = %q, want %q", got, want)
	}
	if got, want := instanceWpThemeActivateArgs(cfg, ""), []string{"docker", "compose", "run", "--rm", "cli", "wp", "theme", "activate", "theme", "--allow-root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("instanceWpThemeActivateArgs() = %#v, want %#v", got, want)
	}
	if got, want := instanceWpThemeActivateArgs(cfg, "custom-slug"), []string{"docker", "compose", "run", "--rm", "cli", "wp", "theme", "activate", "custom-slug", "--allow-root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("instanceWpThemeActivateArgs(explicit) = %#v, want %#v", got, want)
	}
	installArgs := instanceWpCoreInstallArgs(cfg)
	joined := strings.Join(installArgs, " ")
	for _, wanted := range []string{"docker compose run --rm cli sh -lc", "WP_URL", "WP_TITLE", "ADMIN_USER", "ADMIN_PASSWORD", "ADMIN_EMAIL", "wp theme activate theme --allow-root"} {
		if !strings.Contains(joined, wanted) {
			t.Fatalf("instanceWpCoreInstallArgs() missing %q in %#v", wanted, installArgs)
		}
	}
	if strings.Contains(joined, "wp core is-installed") {
		t.Fatalf("instanceWpCoreInstallArgs() unexpectedly probes install state: %#v", installArgs)
	}
	if got, want := instanceRepoPath("/repo", "dist/theme.zip"), filepath.Join("/repo", "dist", "theme.zip"); got != want {
		t.Fatalf("instanceRepoPath() = %q, want %q", got, want)
	}
	if got, want := instanceRepoPath("/repo", "/tmp/theme.zip"), "/tmp/theme.zip"; got != want {
		t.Fatalf("instanceRepoPath() = %q, want %q", got, want)
	}
	if got, want := (instanceCommandRunner{name: "up", cfg: cfg}).Render(), "docker compose up -d; install WordPress if missing and ensure the mounted theme is active"; got != want {
		t.Fatalf("up Render() = %q, want %q", got, want)
	}
	if got, want := (instanceCommandRunner{name: "reset", cfg: cfg}).Render(), "docker compose down -v --remove-orphans; nuke instance data and recreate it with docker compose up -d, install WordPress if missing, and ensure the mounted theme is active"; got != want {
		t.Fatalf("reset Render() = %q, want %q", got, want)
	}
	if got, want := (instanceCommandRunner{name: "shell", cfg: cfg}).Render(), "docker compose exec wordpress sh"; got != want {
		t.Fatalf("shell Render() = %q, want %q", got, want)
	}
}

func TestEnsureManagedInstanceWritesManagedFiles(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
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
		"instance":  map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
	}
	cfg, ok := loadInstanceConfig(root, metadata)
	if !ok {
		t.Fatalf("loadInstanceConfig() = false, want true")
	}
	if got, want := cfg.InstanceDir, config.InstanceDir("client"); got != want {
		t.Fatalf("InstanceDir = %q, want %q", got, want)
	}
	wpPort, mailpitPort := instanceDerivedPorts("client")
	if err := ensureManagedInstance(cfg); err != nil {
		t.Fatalf("ensureManagedInstance() error = %v", err)
	}
	checks := map[string][]string{
		filepath.Join(cfg.InstanceDir, "docker-compose.yml"):                   {filepath.Join(root, "theme") + ":/var/www/html/wp-content/themes/theme", "mailpit", "wordpress:cli-php8.4"},
		filepath.Join(cfg.InstanceDir, ".env"):                                 {"COMPOSE_PROJECT_NAME=nf_client_instance", fmt.Sprintf("WP_PORT=%d", wpPort), fmt.Sprintf("MAILPIT_PORT=%d", mailpitPort), fmt.Sprintf("WP_URL=http://localhost:%d", wpPort), "WP_TITLE=Client"},
		filepath.Join(cfg.InstanceDir, "php", "uploads.ini"):                   {"upload_max_filesize=128M", "max_execution_time=120"},
		filepath.Join(cfg.InstanceDir, "wordpress", "Dockerfile"):              {"FROM wordpress:7.0-php8.4-apache", "COPY wordpress/wordpress-rewrites.conf"},
		filepath.Join(cfg.InstanceDir, "wordpress", "wordpress-rewrites.conf"): {"RewriteRule . /index.php [L]"},
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
	if data, err := os.ReadFile(filepath.Join(cfg.InstanceDir, "uploads", ".gitkeep")); err != nil {
		t.Fatalf("ReadFile(.gitkeep) error = %v", err)
	} else if len(data) != 0 {
		t.Fatalf("uploads/.gitkeep = %q, want empty file", string(data))
	}
}

func TestRunInstanceUpPrintsUnderlyingCommands(t *testing.T) {
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
		"instance":  map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
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
		if got := Run([]string{"instance", "up"}); got != 0 {
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

func TestRunInstanceUpActivatesThemeWhenAlreadyInstalled(t *testing.T) {
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
		"instance":  map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
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
		if got := Run([]string{"instance", "up"}); got != 0 {
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

func TestRunInstanceResetPrintsUnderlyingCommands(t *testing.T) {
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
		"instance":  map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
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
		if got := Run([]string{"instance", "reset"}); got != 0 {
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

func TestRunDeleteServerWithoutIDRequiresIDInNonInteractiveMode(t *testing.T) {
	output := captureStderr(t, func() {
		if got := Run([]string{"server", "delete", "--non-interactive"}); got != 1 {
			t.Fatalf("Run() = %d, want 1", got)
		}
	})
	if !strings.Contains(output, "server delete requires an id or name in non-interactive mode") {
		t.Fatalf("Run() stderr = %q, want non-interactive id requirement", output)
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

func TestRunInstanceShortcutRoutesToInstanceCommand(t *testing.T) {
	workdir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	shortcut := captureStderr(t, func() {
		if got := Run([]string{"up"}); got != 1 {
			t.Fatalf("Run(up) = %d, want 1", got)
		}
	})
	canonical := captureStderr(t, func() {
		if got := Run([]string{"instance", "up"}); got != 1 {
			t.Fatalf("Run(instance up) = %d, want 1", got)
		}
	})
	if shortcut != canonical {
		t.Fatalf("shortcut and canonical output differed:\nshortcut: %q\ncanonical: %q", shortcut, canonical)
	}
	if !strings.Contains(shortcut, "instance up requires a .git repository") {
		t.Fatalf("Run(up) stderr = %q, want instance project context message", shortcut)
	}
}

func TestRunShellShortcutRoutesToInstanceShell(t *testing.T) {
	workdir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	shortcut := captureStderr(t, func() {
		if got := Run([]string{"shell"}); got != 1 {
			t.Fatalf("Run(shell) = %d, want 1", got)
		}
	})
	canonical := captureStderr(t, func() {
		if got := Run([]string{"instance", "shell"}); got != 1 {
			t.Fatalf("Run(instance shell) = %d, want 1", got)
		}
	})
	if shortcut != canonical {
		t.Fatalf("shortcut and canonical output differed:\nshortcut: %q\ncanonical: %q", shortcut, canonical)
	}
	if !strings.Contains(shortcut, "instance shell requires a .git repository") {
		t.Fatalf("Run(shell) stderr = %q, want instance project context message", shortcut)
	}
}

func TestRunInfoShortcutRoutesToInstanceInfo(t *testing.T) {
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	project := map[string]any{"schema": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "instance": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
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

	shortcut := captureStdout(t, func() {
		if got := Run([]string{"info"}); got != 0 {
			t.Fatalf("Run(info) = %d, want 0", got)
		}
	})
	canonical := captureStdout(t, func() {
		if got := Run([]string{"instance", "info"}); got != 0 {
			t.Fatalf("Run(instance info) = %d, want 0", got)
		}
	})
	if shortcut != canonical {
		t.Fatalf("shortcut and canonical output differed:\nshortcut: %q\ncanonical: %q", shortcut, canonical)
	}
	wpPort, mailpitPort := instanceDerivedPorts("client")
	for _, want := range []string{"Instance:\n", "  project: client\n", "  compose project: nf_client_instance\n", fmt.Sprintf("  WordPress: http://localhost:%d\n", wpPort), fmt.Sprintf("  Mailpit:   http://localhost:%d", mailpitPort)} {
		if !strings.Contains(shortcut, want) {
			t.Fatalf("Run(info) output missing %q:\n%s", want, shortcut)
		}
	}
}

func TestRunShellShortcutExecutesWordpressShell(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)

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
	project := map[string]any{"schema": 1, "project": map[string]any{"slug": "client", "name": "Client"}, "wordpress": map[string]any{"theme_path": "theme", "theme_slug": "theme"}, "instance": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"}}
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
		if got := Run([]string{"shell"}); got != 0 {
			t.Fatalf("Run(shell) = %d, want 0", got)
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

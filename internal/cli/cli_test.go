package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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

func TestRunHelpShowsRepoMetadataOutsideGit(t *testing.T) {
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
	for _, wanted := range []string{"\n  server        provision, list, show, delete servers\n", "\n  site          list, show, future install/delete/deploy/sync\n", "\n  repo          init repo metadata and manage workbench runtime\n", "\n  config        init local config\n", "\n  password      derive passwords\n", "\n  help          show help\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runHelp() output missing %q:\n%s", wanted, output)
		}
	}
	for _, unwanted := range []string{"\n  theme         package theme artifacts\n", "\n  commands\n", "\n  run <name>\n", "\n  build\n"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("runHelp() output unexpectedly contained %q:\n%s", unwanted, output)
		}
	}
}

func TestRunHelpShowsRepoCommandsInsideGit(t *testing.T) {
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
	for _, wanted := range []string{"\n  repo          init and repo-local commands\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runHelp() output missing %q:\n%s", wanted, output)
		}
	}
	for _, unwanted := range []string{"\n  theme         package theme artifacts\n", "\n  commands\n", "\n  run <name>\n", "\n  build\n"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("runHelp() output unexpectedly contained %q:\n%s", unwanted, output)
		}
	}
}

func TestRunRepoHelpShowsRepoLocalCommandsInsideGit(t *testing.T) {
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

	output := captureStdout(t, func() { _ = runRepoHelp() })
	for _, wanted := range []string{"\n  init                create .nf/project.json\n", "\n  commands            list configured local repo commands\n", "\n  run <name>          run a configured local repo command\n", "\n  package [--dry-run] [--source] [--output]   package theme artifacts\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runRepoHelp() output missing %q:\n%s", wanted, output)
		}
	}
}

func TestRunRepoHelpShowsInitOnlyOutsideGit(t *testing.T) {
	workdir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() { _ = runRepoHelp() })
	if !strings.Contains(output, "\n  init                create .nf/project.json\n") {
		t.Fatalf("runRepoHelp() output missing init:\n%s", output)
	}
	for _, unwanted := range []string{"\n  commands\n", "\n  run <name>\n", "\n  package [--dry-run] [--source] [--output]\n", "repo-local commands:"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("runRepoHelp() output unexpectedly contained %q:\n%s", unwanted, output)
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
			"sanjel-app1-production": map[string]any{"provider": "linode", "server": "app1", "hostname": "sanjel.app1.nfweb.dev", "url": "https://sanjel.app1.nfweb.dev/", "branch": "main", "environment": "production"},
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
		"project":   map[string]any{"slug": "sanjel", "name": "Sanjel", "type": "wordpress-theme"},
		"wordpress": map[string]any{"deploy_unit": "theme", "theme_slug": "sanjel", "theme_path": "theme"},
		"build":     map[string]any{"commands": []any{"composer install", "npm run build"}},
		"artifact":  map[string]any{"include": []any{"vendor/", "assets/dist/"}, "exclude": []any{"node_modules/", ".git/"}},
		"deploy":    map[string]any{"aliases": map[string]any{"app1": "sanjel-app1-production", "production": "sanjel-app1-production", "staging": "sanjel-app1-staging"}},
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
	for _, wanted := range []string{`"requested_target": "app1"`, `"resolved_target": "sanjel-app1-production"`, `"resolved_server_summary": "app1 / id 98222343 / linode / ssh nonfiction@app1.nfweb.dev"`, `"url": "https://sanjel.app1.nfweb.dev/"`} {
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
	sites := map[string]any{"sites": map[string]any{"sanjel-app1-production": map[string]any{"provider": "linode", "server": "app1", "hostname": "sanjel.app1.nfweb.dev", "branch": "main"}}}
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
	project := map[string]any{"schema": 1, "project": map[string]any{"slug": "sanjel"}, "deploy": map[string]any{"aliases": map[string]any{"app1": "sanjel-app1-production"}}}
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
		if got := Run([]string{"site", "show", "sanjel-app1-production"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, wanted := range []string{`"requested_target": "sanjel-app1-production"`, `"resolved_target": "sanjel-app1-production"`, `"server": "app1"`} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("Run() output missing %q:\n%s", wanted, output)
		}
	}
}

func TestRunRepoInitWritesPortableMetadataShape(t *testing.T) {
	workdir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	if got := Run([]string{"repo", "init", "--project-slug", "sanjel", "--force"}); got != 0 {
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
	if project, ok := metadata["project"].(map[string]any); !ok || project["slug"] != "sanjel" {
		t.Fatalf("project block = %#v, want slug sanjel", metadata["project"])
	}
	if wordpress, ok := metadata["wordpress"].(map[string]any); !ok || wordpress["theme_path"] != "theme" {
		t.Fatalf("wordpress block = %#v, want theme_path theme", metadata["wordpress"])
	}
	if workbench, ok := metadata["workbench"].(map[string]any); !ok {
		t.Fatalf("workbench block = %#v, want workbench config", metadata["workbench"])
	} else {
		for key, want := range map[string]string{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"} {
			if got := workbench[key]; got != want {
				t.Fatalf("workbench.%s = %#v, want %q", key, got, want)
			}
		}
		if _, exists := workbench["path"]; exists {
			t.Fatalf("workbench.path unexpectedly present: %#v", workbench)
		}
	}
	if build, ok := metadata["build"].(map[string]any); !ok {
		t.Fatalf("build block = %#v, want commands list", metadata["build"])
	} else if commands, ok := build["commands"].([]any); !ok || len(commands) != 2 {
		t.Fatalf("build.commands = %#v, want two commands", build["commands"])
	}
	if artifact, ok := metadata["artifact"].(map[string]any); !ok || artifact["path"] != "dist/sanjel.zip" {
		t.Fatalf("artifact block = %#v, want dist/sanjel.zip", metadata["artifact"])
	} else if include, ok := artifact["include"].([]any); !ok || len(include) != 2 {
		t.Fatalf("artifact.include = %#v, want include paths", artifact["include"])
	} else if exclude, ok := artifact["exclude"].([]any); !ok || len(exclude) != 2 {
		t.Fatalf("artifact.exclude = %#v, want exclude paths", artifact["exclude"])
	}
	if deploy, ok := metadata["deploy"].(map[string]any); !ok {
		t.Fatalf("deploy block = %#v, want aliases map", metadata["deploy"])
	} else if aliases, ok := deploy["aliases"].(map[string]any); !ok || len(aliases) != 0 {
		t.Fatalf("deploy.aliases = %#v, want empty map", deploy["aliases"])
	}
	if commands, ok := metadata["commands"].(map[string]any); !ok {
		t.Fatalf("commands block = %#v, want command map", metadata["commands"])
	} else {
		for _, want := range []string{"composer", "npm", "build", "watch", "test"} {
			if commands[want] == nil {
				t.Fatalf("commands block missing %q: %#v", want, commands)
			}
		}
		for _, unwanted := range []string{"setup", "up", "down", "restart", "logs", "reset", "fresh", "wp", "install-theme", "activate-theme"} {
			if _, ok := commands[unwanted]; ok {
				t.Fatalf("commands block unexpectedly contained %q: %#v", unwanted, commands)
			}
		}
		if len(commands) != 5 {
			t.Fatalf("commands block len = %d, want 5", len(commands))
		}
	}
	for _, legacy := range []string{"project_slug", "project_name", "theme_slug", "theme_source", "local_workbench_url", "default_provider"} {
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
		if _, exists := build["source"]; exists {
			t.Fatalf("build.source unexpectedly present: %#v", metadata["build"])
		}
	}
	if _, err := os.Stat(filepath.Join(workdir, "workbench")); !os.IsNotExist(err) {
		t.Fatalf("workbench scaffold unexpectedly created: %v", err)
	}
}

func TestRunRepoInitWithoutForceRejectsExistingProjectJson(t *testing.T) {
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
		if got := Run([]string{"repo", "init", "--project-slug", "sanjel"}); got != 1 {
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

func TestRenderWorkbenchComposeUsesMetadataDefaults(t *testing.T) {
	root := t.TempDir()
	metadata := map[string]any{
		"project":   map[string]any{"slug": "sanjel"},
		"wordpress": map[string]any{"theme_path": "theme-src"},
		"workbench": map[string]any{"compose": "docker compose", "wordpress_service": "wp-app", "cli_service": "wp-cli", "theme_mount_slug": "theme-slot", "uploads_path": "uploads"},
	}
	cfg, ok := loadWorkbenchConfig(root, metadata)
	if !ok {
		t.Fatalf("loadWorkbenchConfig() = false, want true")
	}
	compose := renderWorkbenchCompose(cfg)
	for _, want := range []string{"wp-app:", "wp-cli:", "condition: service_healthy", "depends_on:\n      wp-app:", "working_dir: /var/www/html", filepath.Join(root, "theme-src") + ":/var/www/html/wp-content/themes/theme-slot"} {
		if !strings.Contains(compose, want) {
			t.Fatalf("renderWorkbenchCompose() missing %q:\n%s", want, compose)
		}
	}
}

func TestWorkbenchCommandHelpersBuildExpectedArgs(t *testing.T) {
	cfg := workbenchConfig{
		ProjectSlug:      "sanjel",
		ProjectName:      "Sanjel",
		RepoRoot:         "/repo",
		ThemePath:        "/repo/theme",
		ManagedDir:       filepath.Join("/config", "workbenches", "sanjel"),
		Compose:          "docker compose",
		WordpressService: "wordpress",
		CliService:       "cli",
		ThemeMountSlug:   "theme",
		UploadsPath:      "uploads",
		ThemeSlug:        "sanjel",
	}

	if got, want := workbenchComposeArgs(cfg, "up", "-d"), []string{"docker", "compose", "up", "-d"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("workbenchComposeArgs() = %#v, want %#v", got, want)
	}
	if got, want := workbenchWpArgs(cfg, "plugin", "list"), []string{"docker", "compose", "run", "--rm", "cli", "wp", "plugin", "list", "--allow-root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("workbenchWpArgs() = %#v, want %#v", got, want)
	}
	hostPath, containerPath := workbenchThemeArchivePaths(cfg, "/tmp/theme.zip")
	if hostPath != filepath.Join(cfg.ManagedDir, "uploads", "theme.zip") || containerPath != "/workbench/uploads/theme.zip" {
		t.Fatalf("workbenchThemeArchivePaths() = (%q, %q), want host and container upload paths", hostPath, containerPath)
	}
	if got, want := workbenchCommandDir(cfg), cfg.ManagedDir; got != want {
		t.Fatalf("workbenchCommandDir() = %q, want %q", got, want)
	}
	if got, want := workbenchWpThemeActivateArgs(cfg, ""), []string{"docker", "compose", "run", "--rm", "cli", "wp", "theme", "activate", "sanjel", "--allow-root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("workbenchWpThemeActivateArgs() = %#v, want %#v", got, want)
	}
	installArgs := workbenchWpCoreInstallArgs(cfg)
	joined := strings.Join(installArgs, " ")
	for _, wanted := range []string{"docker compose run --rm cli sh -lc", "WP_URL", "WP_TITLE", "ADMIN_USER", "ADMIN_PASSWORD", "ADMIN_EMAIL", "wp theme activate sanjel --allow-root"} {
		if !strings.Contains(joined, wanted) {
			t.Fatalf("workbenchWpCoreInstallArgs() missing %q in %#v", wanted, installArgs)
		}
	}
	if strings.Contains(joined, "wp core is-installed") {
		t.Fatalf("workbenchWpCoreInstallArgs() unexpectedly probes install state: %#v", installArgs)
	}
	if got, want := workbenchRepoPath("/repo", "dist/theme.zip"), filepath.Join("/repo", "dist", "theme.zip"); got != want {
		t.Fatalf("workbenchRepoPath() = %q, want %q", got, want)
	}
	if got, want := workbenchRepoPath("/repo", "/tmp/theme.zip"), "/tmp/theme.zip"; got != want {
		t.Fatalf("workbenchRepoPath() = %q, want %q", got, want)
	}
	if got, want := (workbenchCommandRunner{name: "setup", cfg: cfg}).Render(), "docker compose up -d; wp core install and activate sanjel if needed"; got != want {
		t.Fatalf("setup Render() = %q, want %q", got, want)
	}
	if got, want := (workbenchCommandRunner{name: "fresh", cfg: cfg}).Render(), "docker compose down -v --remove-orphans && docker compose up -d; wp core install and activate sanjel if needed"; got != want {
		t.Fatalf("fresh Render() = %q, want %q", got, want)
	}
}

func TestEnsureManagedWorkbenchRuntimeWritesManagedFiles(t *testing.T) {
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
		"project":   map[string]any{"slug": "sanjel", "name": "Sanjel"},
		"wordpress": map[string]any{"theme_slug": "sanjel", "theme_path": "theme"},
		"workbench": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
	}
	cfg, ok := loadWorkbenchConfig(root, metadata)
	if !ok {
		t.Fatalf("loadWorkbenchConfig() = false, want true")
	}
	if got, want := cfg.ManagedDir, config.WorkbenchDir("sanjel"); got != want {
		t.Fatalf("ManagedDir = %q, want %q", got, want)
	}
	if err := ensureManagedWorkbenchRuntime(cfg); err != nil {
		t.Fatalf("ensureManagedWorkbenchRuntime() error = %v", err)
	}
	checks := map[string][]string{
		filepath.Join(cfg.ManagedDir, "docker-compose.yml"):                   {filepath.Join(root, "theme") + ":/var/www/html/wp-content/themes/theme", "mailpit", "wordpress:cli-php8.4"},
		filepath.Join(cfg.ManagedDir, ".env"):                                 {"COMPOSE_PROJECT_NAME=sanjel_workbench", "WP_TITLE=Sanjel"},
		filepath.Join(cfg.ManagedDir, "php", "uploads.ini"):                   {"upload_max_filesize=128M", "max_execution_time=120"},
		filepath.Join(cfg.ManagedDir, "wordpress", "Dockerfile"):              {"FROM wordpress:7.0-php8.4-apache", "COPY wordpress/wordpress-rewrites.conf"},
		filepath.Join(cfg.ManagedDir, "wordpress", "wordpress-rewrites.conf"): {"RewriteRule . /index.php [L]"},
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
	if data, err := os.ReadFile(filepath.Join(cfg.ManagedDir, "uploads", ".gitkeep")); err != nil {
		t.Fatalf("ReadFile(.gitkeep) error = %v", err)
	} else if len(data) != 0 {
		t.Fatalf("uploads/.gitkeep = %q, want empty file", string(data))
	}
}

func TestRunRepoSetupCanBeOverriddenByExplicitCommand(t *testing.T) {
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, ".nf"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	project := map[string]any{
		"schema":    1,
		"project":   map[string]any{"slug": "sanjel", "name": "Sanjel", "type": "wordpress-theme"},
		"wordpress": map[string]any{"deploy_unit": "theme", "theme_slug": "sanjel", "theme_path": "theme"},
		"workbench": map[string]any{"compose": "docker compose", "wordpress_service": "wordpress", "cli_service": "cli", "theme_mount_slug": "theme", "uploads_path": "uploads"},
		"commands":  map[string]any{"setup": map[string]any{"description": "Custom setup", "run": []any{"sh", "-lc", "printf custom > override.txt"}}},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".nf", "project.json"), append(data, '\n'), 0o644); err != nil {
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

	if got := Run([]string{"repo", "setup"}); got != 0 {
		t.Fatalf("Run() = %d, want 0", got)
	}
	if _, err := os.Stat(filepath.Join(workdir, "override.txt")); err != nil {
		t.Fatalf("override.txt not created: %v", err)
	}
}

func TestRunRepoPackageUsesNestedMetadataShape(t *testing.T) {
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
		"schema":       1,
		"project":      map[string]any{"slug": "sanjel", "name": "Sanjel", "type": "wordpress-theme"},
		"wordpress":    map[string]any{"deploy_unit": "theme", "theme_slug": "sanjel", "theme_path": "theme"},
		"build":        map[string]any{"commands": []any{"composer install", "npm run build"}},
		"artifact":     map[string]any{"path": "release/sanjel.zip", "include": []any{"vendor/", "assets/dist/"}, "exclude": []any{"node_modules/", ".git/"}},
		"deploy":       map[string]any{"aliases": map[string]any{}},
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
		if got := Run([]string{"repo", "package", "--dry-run"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Would package "+filepath.Join(workdir, "theme")+" -> "+filepath.Join(workdir, "release", "sanjel.zip")) {
		t.Fatalf("Run() output = %q, want nested theme_path/theme_slug and artifact.path", output)
	}
	for _, unwanted := range []string{"legacy-theme", "legacy-project"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("Run() output unexpectedly contained %q: %s", unwanted, output)
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

func TestRunRejectsRepoCommandsOutsideGit(t *testing.T) {
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
		if got := Run([]string{"repo", "commands"}); got != 1 {
			t.Fatalf("Run() = %d, want 1", got)
		}
	})
	if !strings.Contains(output, ".git repository") {
		t.Fatalf("Run() stderr = %q, want .git repository message", output)
	}
}

func TestRunRejectsRemovedTopLevelCompatibilityRoutes(t *testing.T) {
	for _, argv := range [][]string{
		{"provision-server"},
		{"project", "help"},
		{"theme", "package"},
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

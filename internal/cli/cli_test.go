package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/linode/linodego"
	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/kinsta"
	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/project"
	"github.com/nonfiction/nf/internal/state"
	"github.com/nonfiction/nf/internal/target/provision"
	"github.com/nonfiction/nf/internal/ui"
	"github.com/nonfiction/nf/internal/version"
)

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "nf-cli-test-project-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)
	oldwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	if err := os.Chdir(root); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.Chdir(oldwd)
	os.Exit(code)
}

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

func assertContainsInOrder(t *testing.T, output string, values []string) {
	t.Helper()
	offset := 0
	for _, value := range values {
		index := strings.Index(output[offset:], value)
		if index < 0 {
			t.Fatalf("output missing %q after offset %d:\n%s", value, offset, output)
		}
		offset += index + len(value)
	}
}

func stubEnvRemoteDiskOutput(t *testing.T, outputs ...string) *[]string {
	t.Helper()
	oldRunSSHOutput := runSSHOutputFn
	scripts := []string{}
	runSSHOutputFn = func(args []string) ([]byte, error) {
		scripts = append(scripts, args[len(args)-1])
		if len(outputs) == 0 {
			t.Fatalf("unexpected runSSHOutputFn call: %#v", args)
			return []byte("0\n"), nil
		}
		output := outputs[0]
		outputs = outputs[1:]
		return []byte(output), nil
	}
	t.Cleanup(func() { runSSHOutputFn = oldRunSSHOutput })
	return &scripts
}

func stubLocalAvailableDisk(t *testing.T, available int64) {
	t.Helper()
	oldLocalAvailableDiskBytes := localAvailableDiskBytesFn
	localAvailableDiskBytesFn = func(string) (int64, error) { return available, nil }
	t.Cleanup(func() { localAvailableDiskBytesFn = oldLocalAvailableDiskBytes })
}

func stubLocalAvailableDiskOutputs(t *testing.T, outputs ...int64) {
	t.Helper()
	oldLocalAvailableDiskBytes := localAvailableDiskBytesFn
	localAvailableDiskBytesFn = func(string) (int64, error) {
		if len(outputs) == 0 {
			t.Fatalf("unexpected localAvailableDiskBytesFn call")
			return 0, nil
		}
		output := outputs[0]
		outputs = outputs[1:]
		return output, nil
	}
	t.Cleanup(func() { localAvailableDiskBytesFn = oldLocalAvailableDiskBytes })
}

func stubLocalWordPressTransferEstimate(t *testing.T, estimate int64) {
	t.Helper()
	oldLocalWordPressTransferEstimateBytes := localWordPressTransferEstimateBytesFn
	localWordPressTransferEstimateBytesFn = func(envConfig) (int64, error) { return estimate, nil }
	t.Cleanup(func() { localWordPressTransferEstimateBytesFn = oldLocalWordPressTransferEstimateBytes })
}

func stubLocalSnapshotExpandedSize(t *testing.T, expandedSize int64) {
	t.Helper()
	oldLocalSnapshotExpandedSizeBytes := localSnapshotExpandedSizeBytesFn
	localSnapshotExpandedSizeBytesFn = func(envConfig, string) (int64, error) { return expandedSize, nil }
	t.Cleanup(func() { localSnapshotExpandedSizeBytesFn = oldLocalSnapshotExpandedSizeBytes })
}

func stubLocalPushTransferSizes(t *testing.T, archiveSize, expandedSize int64) {
	t.Helper()
	oldEstimateSize := localPushTransferEstimateBytesFn
	oldArchiveSize := localPushTransferArchiveSizeBytesFn
	oldExpandedSize := localPushTransferExpandedSizeBytesFn
	localPushTransferEstimateBytesFn = func(envConfig) (int64, error) { return addEnvTransferBytes(archiveSize, expandedSize), nil }
	localPushTransferArchiveSizeBytesFn = func(envConfig, string) (int64, error) { return archiveSize, nil }
	localPushTransferExpandedSizeBytesFn = func(envConfig, string) (int64, error) { return expandedSize, nil }
	t.Cleanup(func() {
		localPushTransferEstimateBytesFn = oldEstimateSize
		localPushTransferArchiveSizeBytesFn = oldArchiveSize
		localPushTransferExpandedSizeBytesFn = oldExpandedSize
	})
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
	assertContainsInOrder(t, output, []string{
		"nf\n\nCommands:\n",
		"  provider    manage provider integrations\n",
		"  target      manage deployable targets\n",
		"  site        manage remote sites and envs\n",
		"  password    manage and derive passwords\n",
		"\n  init        initialize project metadata\n",
		"  config      manage global config\n",
		"  completion  print shell completion scripts\n",
		"  refresh     refresh all provider, target, and site caches\n",
		"  version     show nf version\n",
		"  help        show help\n",
	})
	for _, wanted := range []string{"\n  init        initialize project metadata\n", "\n  provider    manage provider integrations\n", "\n  target      manage deployable targets\n", "\n  site        manage remote sites and envs\n", "\n  refresh     refresh all provider, target, and site caches\n", "\n  config      manage global config\n", "\n  password    manage and derive passwords\n", "\n  completion  print shell completion scripts\n", "\n  version     show nf version\n", "\n  help        show help\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runHelp() output missing %q:\n%s", wanted, output)
		}
	}
	for _, unwanted := range []string{"\n  remote  ", "\n  theme   ", "\n  env     ", "\n  alias   ", "\n  public  ", "\n  repo  ", "\n  instance  ", "\n  server  ", "Shortcuts:", "nf up", "nf shell", "snapshot create", "\n  commands\n", "\n  run <name>\n", "\n  build\n"} {
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
	assertContainsInOrder(t, output, []string{
		"  provider    manage provider integrations\n",
		"  target      manage deployable targets\n",
		"  site        manage remote sites and envs\n",
		"  password    manage and derive passwords\n",
		"\n  init        initialize project metadata\n",
		"  config      manage global config\n",
	})
	for _, wanted := range []string{"\n  init        initialize project metadata\n", "\n  provider    manage provider integrations\n", "\n  config      manage global config\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runHelp() output missing %q:\n%s", wanted, output)
		}
	}
	for _, unwanted := range []string{"\n  remote  ", "\n  theme   ", "\n  env     ", "\n  alias   ", "\n  public  ", "\n  instance  ", "\n  server  ", "Shortcuts:", "nf up", "nf shell", "snapshot create", "\n  commands\n", "\n  run <name>\n", "\n  build\n"} {
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
	if err := os.WriteFile(filepath.Join(workdir, "nf.json"), []byte("{\n  \"version\": 2,\n  \"project\": {\"slug\": \"client\", \"password_version\": 0},\n  \"wordpress\": {\"themes\": [\"twentytwentyfive\"]}\n}\n"), 0o644); err != nil {
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
	assertContainsInOrder(t, output, []string{
		"  provider    manage provider integrations\n",
		"  target      manage deployable targets\n",
		"  site        manage remote sites and envs\n",
		"  domain      manage remote env domains\n",
		"  password    manage and derive passwords\n",
		"\n  env         manage the local development env\n",
		"  theme       manage configured WordPress themes\n",
		"  plugin      manage configured WordPress plugins\n",
		"  alias       manage root-level WordPress content aliases\n",
		"  remote      manage repo remotes\n",
		"\n  init        initialize project metadata\n",
		"  config      manage global config\n",
	})
	for _, wanted := range []string{"\n  env         manage the local development env\n", "\n  theme       manage configured WordPress themes\n", "\n  plugin      manage configured WordPress plugins\n", "\n  alias       manage root-level WordPress content aliases\n", "\n  remote      manage repo remotes\n", "\n  password    manage and derive passwords\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runHelp() output missing %q:\n%s", wanted, output)
		}
	}
}

func TestRunVersionShowsBuildMetadata(t *testing.T) {
	oldVersion, oldCommit := version.Version, version.Commit
	wantVersion := version.DefaultVersion()
	version.Version = wantVersion
	version.Commit = "abc1234"
	t.Cleanup(func() {
		version.Version = oldVersion
		version.Commit = oldCommit
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"version"}); got != 0 {
			t.Fatalf("Run(version) = %d, want 0", got)
		}
	})
	for _, want := range []string{"version: " + wantVersion + "\n", "commit:  abc1234\n"} {
		if !strings.Contains(output, want) {
			t.Fatalf("version output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "date:") {
		t.Fatalf("version output unexpectedly included date:\n%s", output)
	}

	shortOutput := captureStdout(t, func() {
		if got := Run([]string{"version", "--short"}); got != 0 {
			t.Fatalf("Run(version --short) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(shortOutput) != wantVersion {
		t.Fatalf("version --short = %q, want %s", shortOutput, wantVersion)
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
	assertContainsInOrder(t, output, []string{"list, ls", "show [provider]", "check [provider]", "\nShow/Check Options:\n", "--json"})
	for _, wanted := range []string{"provider\n\nCommands:\n", "\n  list, ls", "list provider integrations\n", "\n  show [provider]", "show cached provider metadata\n", "\n  check [provider]", "check provider access and refresh cached metadata\n", "\nShow/Check Options:\n", "\n  --json  print JSON output\n"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runProviderHelp() output missing %q:\n%s", wanted, output)
		}
	}
}

func TestRunTargetHelpShowsRefresh(t *testing.T) {
	output := captureStdout(t, func() { _ = runTargetHelp() })
	assertContainsInOrder(t, output, []string{"list, ls", "show [target]", "password [target]", "refresh", "\n\n  add linode <name>", "remove, rm [target]", "\nShow Options:\n", "--json", "\nPassword Options:\n", "--root", "--db", "\nAdd Options:\n", "--region <region>", "--ubuntu-version <version>", "--all-linode-ssh-keys", "\nMutation Options:\n", "--dry-run"})
	for _, wanted := range []string{"target\n\nCommands:\n", "list, ls", "list deployable targets", "password [target]", "refresh", "refresh targets from providers", "Show Options:", "Password Options:", "Add Options:", "Mutation Options:"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runTargetHelp() output missing %q:\n%s", wanted, output)
		}
	}
}

func TestGroupedHelpScreensUseIntendedOrder(t *testing.T) {
	tests := []struct {
		name   string
		render func() string
		values []string
	}{
		{
			name:   "remote",
			render: func() string { return captureStdout(t, func() { _ = runRemoteHelp() }) },
			values: []string{"list, ls", "show [name]", "\n\n  add [name] [env]", "remove, rm [name]"},
		},
		{
			name:   "site",
			render: func() string { return captureStdout(t, func() { _ = runSiteHelp() }) },
			values: []string{"list, ls [--envs [site]]", "show [site|env]", "refresh", "cache [site|env]", "repair [site|env]", "\n\n  shell, sh [env]", "wp <env> -- <args>", "password [site|env]", "\n\n  snapshot [env]", "snapshot list, ls", "snapshot remove, rm [name]", "export [env]", "basicauth <action> [site|env]", "\n\n  add <target> <site>", "staging <action> [site]", "remove, rm [site]", "\nList Options:\n", "--envs", "--refresh", "\nShow Options:\n", "--json", "\nPassword Options:\n", "--basicauth", "\nSnapshot/Export Options:\n", "--output <path>", "\nAdd Options:\n", "--with-staging", "--password-version <version>", "\nMutation Options:\n", "--dry-run"},
		},
		{
			name:   "config",
			render: func() string { return captureStdout(t, func() { _ = runConfigHelp() }) },
			values: []string{"show", "get [key]", "set [key] [value]", "unset <key>", "keys", "edit", "init", "\nInit Options:\n", "--non-interactive", "\nExamples:\n", "nf config show", "nf config keys", "nf config get", "pick a key", "nf config set", "pick a key and value", "nf config get kinsta.php", "nf config set kinsta.php 8.3", "nf config unset kinsta.region"},
		},
		{
			name:   "password",
			render: func() string { return captureStdout(t, func() { _ = runPasswordHelp() }) },
			values: []string{"derive [scope] [value...]", "\n\n  show-salt", "set-salt <salt>", "\n\n  age-identity", "age-recipient", "\nOptions:\n", "--password-version <N>"},
		},
		{
			name:   "site add",
			render: func() string { return captureStdout(t, func() { _ = runSiteAdd([]string{"help"}) }) },
			values: []string{"<target> <site> [flags]", "\n\n  --with-staging", "--password-version <version>", "--kinsta-slug <slug>", "--region <region>", "--php <version>", "\n\n  --dry-run", "--execute", "--yes", "--non-interactive"},
		},
		{
			name:   "site staging",
			render: func() string { return captureStdout(t, func() { _ = runSiteStagingHelp() }) },
			values: []string{"status [site]", "\n\n  add [site]", "remove, rm [site]"},
		},
		{
			name:   "site basicauth",
			render: func() string { return captureStdout(t, func() { _ = runSiteBasicAuthHelp() }) },
			values: []string{"status [env]", "password [site]", "\n\n  enable [env]", "disable [env]"},
		},
		{
			name:   "plugin",
			render: func() string { return captureStdout(t, func() { _ = runPlugin([]string{"help"}) }) },
			values: []string{"list, ls", "status [remote]", "diff [remote]", "\n\n  install [remote]", "pull [plugin] [remote]", "\n\n  add <plugin>", "remove, rm <plugin>", "\nAdd Options:\n", "--source <source>", "--manual", "--note <note>", "--no-auto-update", "\nInstall Options:\n", "--dry-run", "--yes", "\nCache Commands:\n", "cache add <plugin> <zip>", "cache pull [plugin] [remote]", "cache remove, cache rm <plugin>"},
		},
		{
			name:   "theme",
			render: func() string { return captureStdout(t, func() { _ = runTheme([]string{"help"}) }) },
			values: []string{"list, ls", "status [remote]", "diff [remote]", "\n\n  install [remote]", "pull [remote]", "\n\n  add <theme>", "activate <theme>", "remove, rm <theme>", "\nCache Commands:\n", "cache add <theme> <zip>", "cache pull [theme] [remote]", "cache remove, cache rm <theme>"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertContainsInOrder(t, tt.render(), tt.values)
		})
	}
}

func TestHelpFlagsAndNestedTopicsShowGroupHelp(t *testing.T) {
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	projectData := []byte("{\n  \"version\": 2,\n  \"project\": {\"slug\": \"client\", \"password_version\": 0},\n  \"wordpress\": {\"themes\": [\"twentytwentyfive\"]}\n}\n")
	if err := os.WriteFile(filepath.Join(workdir, "nf.json"), projectData, 0o644); err != nil {
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

	tests := []struct {
		args []string
		want string
	}{
		{[]string{"site", "--help"}, "site\n\nCommands:\n"},
		{[]string{"env", "-h"}, "env\n\nCommands:\n"},
		{[]string{"plugin", "--help"}, "plugin\n\nCommands:\n"},
		{[]string{"plugin", "cache"}, "plugin cache\n\nCommands:\n"},
		{[]string{"theme", "cache", "--help"}, "theme cache\n\nCommands:\n"},
		{[]string{"env", "snapshot", "-h"}, "env snapshot\n\nCommands:\n"},
		{[]string{"site", "snapshot", "--help"}, "site snapshot\n\nCommands:\n"},
		{[]string{"help", "site", "snapshot"}, "site snapshot\n\nCommands:\n"},
		{[]string{"help", "site", "staging"}, "site staging\n\nCommands:\n"},
		{[]string{"help", "site", "basicauth"}, "site basicauth\n\nCommands:\n"},
		{[]string{"help", "plugin", "cache"}, "plugin cache\n\nCommands:\n"},
		{[]string{"help", "env", "snapshot"}, "env snapshot\n\nCommands:\n"},
		{[]string{"help", "env", "import"}, "env import\n\n"},
		{[]string{"help", "theme", "cache"}, "theme cache\n\nCommands:\n"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, "_"), func(t *testing.T) {
			output := captureStdout(t, func() {
				if got := Run(tt.args); got != 0 {
					t.Fatalf("Run(%v) = %d, want 0", tt.args, got)
				}
			})
			if !strings.Contains(output, tt.want) {
				t.Fatalf("Run(%v) output missing %q:\n%s", tt.args, tt.want, output)
			}
		})
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
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("providers", []map[string]any{{
		"provider": "linode",
		"targets":  []map[string]any{{"id": "123", "name": "app1-linode", "provider": "linode", "hostname": "app1.example.com"}},
	}, {
		"provider": "kinsta",
		"targets":  []map[string]any{{"id": "kinsta", "name": "kinsta", "provider": "kinsta"}},
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
	versionOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "ver"}); got != 0 {
			t.Fatalf("Run(__complete ver) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(versionOutput) != "version" {
		t.Fatalf("version completion = %q, want version", versionOutput)
	}
	refreshOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "ref"}); got != 0 {
			t.Fatalf("Run(__complete ref) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(refreshOutput) != "refresh" {
		t.Fatalf("refresh completion = %q, want refresh", refreshOutput)
	}
	versionFlagOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "version", "--"}); got != 0 {
			t.Fatalf("Run(__complete version --) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(versionFlagOutput) != "--short" {
		t.Fatalf("version flag completion = %q, want --short", versionFlagOutput)
	}
	passwordScopeOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "password", "derive", ""}); got != 0 {
			t.Fatalf("Run(__complete password derive) = %d, want 0", got)
		}
	})
	for _, want := range []string{"wp-admin\n", "mysql\n", "basic-auth\n", "linode-root\n", "db-admin\n", "--password-version\n"} {
		if !strings.Contains(passwordScopeOutput, want) {
			t.Fatalf("password derive scope completion missing %q:\n%s", want, passwordScopeOutput)
		}
	}
	for _, unwanted := range []string{"adminer\n", "adminer-console\n", "db\n"} {
		if strings.Contains(passwordScopeOutput, unwanted) {
			t.Fatalf("password derive scope completion unexpectedly contains %q:\n%s", unwanted, passwordScopeOutput)
		}
	}
	passwordProjectIdentityOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "password", "derive", "wp-admin", "cl"}); got != 0 {
			t.Fatalf("Run(__complete password derive wp-admin cl) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(passwordProjectIdentityOutput) != "client" {
		t.Fatalf("password derive project identity completion = %q, want client", passwordProjectIdentityOutput)
	}
	passwordTargetIdentityOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "password", "derive", "db-admin", "app"}); got != 0 {
			t.Fatalf("Run(__complete password derive db-admin app) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(passwordTargetIdentityOutput) != "app1.example.com" {
		t.Fatalf("password derive target identity completion = %q, want app1.example.com", passwordTargetIdentityOutput)
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
	if !strings.Contains(providerOutput, "--json\n") {
		t.Fatalf("provider completion missing --json:\n%s", providerOutput)
	}

	targetOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "target", "show", "app"}); got != 0 {
			t.Fatalf("Run(__complete target show) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(targetOutput) != "app1-linode" {
		t.Fatalf("target completion = %q, want app1-linode only", targetOutput)
	}
	targetAddFlagOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "target", "add", "linode", "app1", "--"}); got != 0 {
			t.Fatalf("Run(__complete target add linode --) = %d, want 0", got)
		}
	})
	if !strings.Contains(targetAddFlagOutput, "--db-user\n") {
		t.Fatalf("target add flag completion missing --db-user:\n%s", targetAddFlagOutput)
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
	siteCacheOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "cache", "client"}); got != 0 {
			t.Fatalf("Run(__complete site cache) = %d, want 0", got)
		}
	})
	if !strings.Contains(siteCacheOutput, "client-app1-linode\n") || !strings.Contains(siteCacheOutput, "client-app1-linode:live\n") {
		t.Fatalf("site cache completion = %q, want site and env ids", siteCacheOutput)
	}
	siteRepairOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "repair", "client"}); got != 0 {
			t.Fatalf("Run(__complete site repair) = %d, want 0", got)
		}
	})
	for _, want := range []string{"client-app1-linode\n", "client-app1-linode:live\n"} {
		if !strings.Contains(siteRepairOutput, want) {
			t.Fatalf("site repair completion missing %q:\n%s", want, siteRepairOutput)
		}
	}
	siteRepairFlagOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "repair", "client-app1-linode", "--"}); got != 0 {
			t.Fatalf("Run(__complete site repair --) = %d, want 0", got)
		}
	})
	for _, want := range []string{"--project-slug\n", "--dry-run\n", "--execute\n", "--yes\n", "--non-interactive\n"} {
		if !strings.Contains(siteRepairFlagOutput, want) {
			t.Fatalf("site repair flag completion missing %q:\n%s", want, siteRepairFlagOutput)
		}
	}
	siteShOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "sh", "client"}); got != 0 {
			t.Fatalf("Run(__complete site sh) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(siteShOutput) != "client-app1-linode:live" {
		t.Fatalf("site sh completion = %q, want env id only", siteShOutput)
	}
	setupTestNFProjectWithMetadata(t, map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"remotes":   map[string]any{"production": "client-app1-linode:live"},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "defines": []any{map[string]any{"name": "OTGS_INSTALLER_SITE_KEY_WPML", "env": "CLIENT_WPML_SITE_KEY"}}},
	})
	defineRootOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "de"}); got != 0 {
			t.Fatalf("Run(__complete de) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(defineRootOutput) != "define" {
		t.Fatalf("define root completion = %q, want define", defineRootOutput)
	}
	defineCommandOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "define", ""}); got != 0 {
			t.Fatalf("Run(__complete define) = %d, want 0", got)
		}
	})
	for _, want := range []string{"list\n", "get\n", "status\n", "sync\n", "set\n", "remove\n", "rm\n", "migrate-env\n", "rekey\n"} {
		if !strings.Contains(defineCommandOutput, want) {
			t.Fatalf("define completion missing %q:\n%s", want, defineCommandOutput)
		}
	}
	if strings.Contains(defineCommandOutput, "add\n") {
		t.Fatalf("define completion retained removed add command:\n%s", defineCommandOutput)
	}
	defineSyncOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "define", "sync", "pro"}); got != 0 {
			t.Fatalf("Run(__complete define sync pro) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(defineSyncOutput) != "production" {
		t.Fatalf("define sync completion = %q, want production", defineSyncOutput)
	}
	defineRemoveOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "define", "remove", "OT"}); got != 0 {
			t.Fatalf("Run(__complete define remove OT) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(defineRemoveOutput) != "OTGS_INSTALLER_SITE_KEY_WPML" {
		t.Fatalf("define remove completion = %q, want configured define", defineRemoveOutput)
	}
	defineGetOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "define", "get", "OT"}); got != 0 {
			t.Fatalf("Run(__complete define get OT) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(defineGetOutput) != "OTGS_INSTALLER_SITE_KEY_WPML" {
		t.Fatalf("define get completion = %q, want configured define", defineGetOutput)
	}
	defineSetOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "define", "set", "OT"}); got != 0 {
			t.Fatalf("Run(__complete define set OT) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(defineSetOutput) != "OTGS_INSTALLER_SITE_KEY_WPML" {
		t.Fatalf("define set completion = %q, want configured define", defineSetOutput)
	}
	defineForOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "define", "set", "SOME_PLUGIN_CONSTANT", "true", "--for", ""}); got != 0 {
			t.Fatalf("Run(__complete define set --for) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(defineForOutput) != "local\nproduction" {
		t.Fatalf("define --for completion = %q, want local and production", defineForOutput)
	}
	for _, notWant := range []string{"default\n", "live\n", "staging\n"} {
		if strings.Contains(defineForOutput, notWant) {
			t.Fatalf("define --for completion included generic selector %q:\n%s", notWant, defineForOutput)
		}
	}
	defineForEqualsOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "define", "set", "SOME_PLUGIN_CONSTANT", "true", "--for=lo"}); got != 0 {
			t.Fatalf("Run(__complete define set --for=lo) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(defineForEqualsOutput) != "--for=local" {
		t.Fatalf("define --for= completion = %q, want --for=local", defineForEqualsOutput)
	}

	sitePasswordOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "password", "client"}); got != 0 {
			t.Fatalf("Run(__complete site password) = %d, want 0", got)
		}
	})
	if !strings.Contains(sitePasswordOutput, "client-app1-linode\n") || !strings.Contains(sitePasswordOutput, "client-app1-linode:live\n") {
		t.Fatalf("site password completion = %q, want site and env ids", sitePasswordOutput)
	}

	siteBasicAuthOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "b"}); got != 0 {
			t.Fatalf("Run(__complete site b) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(siteBasicAuthOutput) != "basicauth" {
		t.Fatalf("site basicauth command completion = %q, want basicauth", siteBasicAuthOutput)
	}

	siteStagingOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "st"}); got != 0 {
			t.Fatalf("Run(__complete site st) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(siteStagingOutput) != "staging" {
		t.Fatalf("site staging command completion = %q, want staging", siteStagingOutput)
	}

	domainOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "do"}); got != 0 {
			t.Fatalf("Run(__complete do) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(domainOutput) != "domain" {
		t.Fatalf("domain command completion = %q, want domain", domainOutput)
	}

	removedSiteDomainOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "do"}); got != 0 {
			t.Fatalf("Run(__complete site do) = %d, want 0", got)
		}
	})
	if strings.Contains(removedSiteDomainOutput, "domain") {
		t.Fatalf("site completion still contains domain:\n%s", removedSiteDomainOutput)
	}

	domainActionsOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "domain", ""}); got != 0 {
			t.Fatalf("Run(__complete domain actions) = %d, want 0", got)
		}
	})
	for _, want := range []string{"list\n", "add\n", "check\n", "primary\n", "remove\n", "help\n"} {
		if !strings.Contains(domainActionsOutput, want) {
			t.Fatalf("domain action completion missing %q:\n%s", want, domainActionsOutput)
		}
	}
	if strings.Contains(domainActionsOutput, "prepare\n") {
		t.Fatalf("domain action completion still contains prepare:\n%s", domainActionsOutput)
	}

	domainEnvOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "domain", "add", "client"}); got != 0 {
			t.Fatalf("Run(__complete domain add client) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(domainEnvOutput) != "client-app1-linode:live" {
		t.Fatalf("domain env completion = %q, want env id only", domainEnvOutput)
	}

	domainPrimaryFlagOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "domain", "primary", "client-app1-linode:live", "www.client.com", "--"}); got != 0 {
			t.Fatalf("Run(__complete domain primary --) = %d, want 0", got)
		}
	})
	for _, want := range []string{"--proxy\n", "--no-proxy\n", "--search-replace\n", "--no-search-replace\n", "--force\n", "--wait-timeout\n", "--wait-interval\n", "--dry-run\n", "--execute\n", "--yes\n", "--non-interactive\n"} {
		if !strings.Contains(domainPrimaryFlagOutput, want) {
			t.Fatalf("domain primary flag completion missing %q:\n%s", want, domainPrimaryFlagOutput)
		}
	}
	for _, unwanted := range []string{"--alias\n", "--canonical\n", "--primary\n", "--setup\n", "--wait\n"} {
		if strings.Contains(domainPrimaryFlagOutput, unwanted) {
			t.Fatalf("domain primary flag completion unexpectedly contains %q:\n%s", unwanted, domainPrimaryFlagOutput)
		}
	}

	domainAddFlagOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "domain", "add", "client-app1-linode:live", "www.client.com", "--"}); got != 0 {
			t.Fatalf("Run(__complete domain add --) = %d, want 0", got)
		}
	})
	for _, want := range []string{"--proxy\n", "--no-proxy\n", "--dry-run\n", "--execute\n", "--yes\n", "--non-interactive\n"} {
		if !strings.Contains(domainAddFlagOutput, want) {
			t.Fatalf("domain add flag completion missing %q:\n%s", want, domainAddFlagOutput)
		}
	}
	for _, unwanted := range []string{"--alias\n", "--canonical\n", "--primary\n", "--no-primary\n", "--setup\n", "--search-replace\n", "--no-search-replace\n", "--wait-timeout\n", "--wait-interval\n"} {
		if strings.Contains(domainAddFlagOutput, unwanted) {
			t.Fatalf("domain add flag completion unexpectedly contains %q:\n%s", unwanted, domainAddFlagOutput)
		}
	}

	domainCheckFlagOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "domain", "check", "client-app1-linode:live", "www.client.com", "--"}); got != 0 {
			t.Fatalf("Run(__complete domain check --) = %d, want 0", got)
		}
	})
	for _, want := range []string{"--proxy\n", "--no-proxy\n", "--non-interactive\n"} {
		if !strings.Contains(domainCheckFlagOutput, want) {
			t.Fatalf("domain check flag completion missing %q:\n%s", want, domainCheckFlagOutput)
		}
	}
	for _, unwanted := range []string{"--alias\n", "--canonical\n", "--execute\n", "--yes\n", "--dry-run\n", "--setup\n", "--search-replace\n"} {
		if strings.Contains(domainCheckFlagOutput, unwanted) {
			t.Fatalf("domain check flag completion unexpectedly contains %q:\n%s", unwanted, domainCheckFlagOutput)
		}
	}

	domainProxyValueOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "domain", "add", "client-app1-linode:live", "www.client.com", "--proxy", ""}); got != 0 {
			t.Fatalf("Run(__complete domain add --proxy) = %d, want 0", got)
		}
	})
	for _, want := range []string{"cloudflare\n"} {
		if !strings.Contains(domainProxyValueOutput, want) {
			t.Fatalf("domain proxy value completion missing %q:\n%s", want, domainProxyValueOutput)
		}
	}

	siteStagingActionsOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "staging", ""}); got != 0 {
			t.Fatalf("Run(__complete site staging actions) = %d, want 0", got)
		}
	})
	for _, want := range []string{"status\n", "add\n", "remove\n", "rm\n", "help\n"} {
		if !strings.Contains(siteStagingActionsOutput, want) {
			t.Fatalf("site staging action completion missing %q:\n%s", want, siteStagingActionsOutput)
		}
	}

	siteStagingSiteOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "staging", "add", "client"}); got != 0 {
			t.Fatalf("Run(__complete site staging add client) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(siteStagingSiteOutput) != "client-app1-linode" {
		t.Fatalf("site staging add site completion = %q, want site id only", siteStagingSiteOutput)
	}

	siteStagingStatusSiteOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "staging", "status", "client"}); got != 0 {
			t.Fatalf("Run(__complete site staging status client) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(siteStagingStatusSiteOutput) != "client-app1-linode" {
		t.Fatalf("site staging status site completion = %q, want site id only", siteStagingStatusSiteOutput)
	}

	siteStagingStatusEmptySiteOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "staging", "status", ""}); got != 0 {
			t.Fatalf("Run(__complete site staging status empty) = %d, want 0", got)
		}
	})
	if !strings.Contains(siteStagingStatusEmptySiteOutput, "client-app1-linode\n") {
		t.Fatalf("site staging status empty site completion missing site:\n%s", siteStagingStatusEmptySiteOutput)
	}

	siteStagingMissingSiteFlagOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "staging", "add", "--"}); got != 0 {
			t.Fatalf("Run(__complete site staging add missing-site --) = %d, want 0", got)
		}
	})
	for _, want := range []string{"--dry-run\n", "--execute\n", "--yes\n", "--non-interactive\n"} {
		if !strings.Contains(siteStagingMissingSiteFlagOutput, want) {
			t.Fatalf("site staging missing-site flag completion missing %q:\n%s", want, siteStagingMissingSiteFlagOutput)
		}
	}

	siteStagingFlagOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "staging", "add", "client-app1-linode", "--"}); got != 0 {
			t.Fatalf("Run(__complete site staging add --) = %d, want 0", got)
		}
	})
	for _, want := range []string{"--dry-run\n", "--execute\n", "--yes\n", "--non-interactive\n"} {
		if !strings.Contains(siteStagingFlagOutput, want) {
			t.Fatalf("site staging flag completion missing %q:\n%s", want, siteStagingFlagOutput)
		}
	}

	siteBasicAuthActionsOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "basicauth", ""}); got != 0 {
			t.Fatalf("Run(__complete site basicauth actions) = %d, want 0", got)
		}
	})
	for _, want := range []string{"status\n", "enable\n", "disable\n", "password\n", "help\n"} {
		if !strings.Contains(siteBasicAuthActionsOutput, want) {
			t.Fatalf("site basicauth action completion missing %q:\n%s", want, siteBasicAuthActionsOutput)
		}
	}

	siteBasicAuthEnvOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "basicauth", "status", "client"}); got != 0 {
			t.Fatalf("Run(__complete site basicauth status client) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(siteBasicAuthEnvOutput) != "client-app1-linode:live" {
		t.Fatalf("site basicauth env completion = %q, want env id only", siteBasicAuthEnvOutput)
	}

	siteBasicAuthFlagOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "basicauth", "enable", "client-app1-linode:live", "--"}); got != 0 {
			t.Fatalf("Run(__complete site basicauth enable --) = %d, want 0", got)
		}
	})
	for _, want := range []string{"--dry-run\n", "--execute\n", "--yes\n", "--non-interactive\n"} {
		if !strings.Contains(siteBasicAuthFlagOutput, want) {
			t.Fatalf("site basicauth flag completion missing %q:\n%s", want, siteBasicAuthFlagOutput)
		}
	}

	siteBasicAuthPasswordOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "basicauth", "password", "client"}); got != 0 {
			t.Fatalf("Run(__complete site basicauth password) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(siteBasicAuthPasswordOutput) != "client-app1-linode" {
		t.Fatalf("site basicauth password completion = %q, want site id only", siteBasicAuthPasswordOutput)
	}

	configOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "config", "se"}); got != 0 {
			t.Fatalf("Run(__complete config se) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(configOutput) != "set" {
		t.Fatalf("config completion = %q, want set", configOutput)
	}
	configDBOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "config", "get", "database."}); got != 0 {
			t.Fatalf("Run(__complete config get database.) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(configDBOutput) != "database.user" {
		t.Fatalf("config key completion = %q, want database.user", configDBOutput)
	}
	configSetKeyOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "config", "set", ""}); got != 0 {
			t.Fatalf("Run(__complete config set) = %d, want 0", got)
		}
	})
	for _, want := range []string{"core.base-domain\n", "kinsta.php\n", "linode.user\n"} {
		if !strings.Contains(configSetKeyOutput, want) {
			t.Fatalf("config set key completion missing %q:\n%s", want, configSetKeyOutput)
		}
	}
	configSetValueOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "config", "set", "kinsta.php", ""}); got != 0 {
			t.Fatalf("Run(__complete config set kinsta.php) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(configSetValueOutput) != "8.3" {
		t.Fatalf("config set value completion = %q, want 8.3", configSetValueOutput)
	}
	envUpFlagOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "env", "up", "--"}); got != 0 {
			t.Fatalf("Run(__complete env up --) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(envUpFlagOutput) != "--rebuild" {
		t.Fatalf("env up completion = %q, want --rebuild", envUpFlagOutput)
	}
	envResetFlagOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "env", "reset", "--"}); got != 0 {
			t.Fatalf("Run(__complete env reset --) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(envResetFlagOutput) != "--rebuild" {
		t.Fatalf("env reset completion = %q, want --rebuild", envResetFlagOutput)
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
	siteExportOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "site", "export", "client"}); got != 0 {
			t.Fatalf("Run(__complete site export) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(siteExportOutput) != "client-app1-linode:live" {
		t.Fatalf("site export completion = %q, want env id only", siteExportOutput)
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
	envImportOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "env", "import", "--"}); got != 0 {
			t.Fatalf("Run(__complete env import) = %d, want 0", got)
		}
	})
	for _, want := range []string{"--db\n", "--source-url\n", "--table-prefix\n", "--name\n", "--dry-run\n", "--yes\n"} {
		if !strings.Contains(envImportOutput, want) {
			t.Fatalf("env import completion missing %q:\n%s", want, envImportOutput)
		}
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
	dataHome := t.TempDir()
	t.Setenv("NF_DATA_HOME", dataHome)
	for _, dir := range []string{filepath.Join(dataHome, "plugins", "cached-pro"), filepath.Join(dataHome, "themes", "paid-parent")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	for _, dir := range []string{".git"} {
		if err := os.Mkdir(filepath.Join(workdir, dir), 0o755); err != nil {
			t.Fatalf("Mkdir(%s) error = %v", dir, err)
		}
	}
	project := map[string]any{
		"version": 2,
		"project": map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{
			"themes": []any{map[string]any{
				"slug":   "client",
				"source": "repo",
				"path":   "theme",
				"tasks":  map[string]any{"build": map[string]any{"description": "Build assets", "run": "npm run build"}},
			}},
			"plugins": []any{"stream"},
			"aliases": map[string]any{"files": "wp-content/uploads/public/files"},
		},
		"remotes": map[string]any{"production": "client-app1-linode:live"},
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

	envCommandOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "env", "s"}); got != 0 {
			t.Fatalf("Run(__complete env s) = %d, want 0", got)
		}
	})
	for _, want := range []string{"show\n", "shell\n", "sh\n", "snapshot\n"} {
		if !strings.Contains(envCommandOutput, want) {
			t.Fatalf("env command completion missing %q:\n%s", want, envCommandOutput)
		}
	}
	if strings.Contains(envCommandOutput, "ssh") {
		t.Fatalf("env command completion included removed ssh alias:\n%s", envCommandOutput)
	}

	envShellOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "env", "shell", ""}); got != 0 {
			t.Fatalf("Run(__complete env shell) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(envShellOutput) != "production" {
		t.Fatalf("env shell completion = %q, want production", envShellOutput)
	}

	envLogsOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "env", "logs", ""}); got != 0 {
			t.Fatalf("Run(__complete env logs) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(envLogsOutput) != "production" {
		t.Fatalf("env logs completion = %q, want production", envLogsOutput)
	}

	envPasswordOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "env", "password", ""}); got != 0 {
			t.Fatalf("Run(__complete env password) = %d, want 0", got)
		}
	})
	for _, want := range []string{"production\n", "--wp\n", "--db\n", "--basicauth\n"} {
		if !strings.Contains(envPasswordOutput, want) {
			t.Fatalf("env password completion missing %q:\n%s", want, envPasswordOutput)
		}
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
	for _, want := range []string{"deploy\n", "diff\n"} {
		if !strings.Contains(themeCommandOutput, want) {
			t.Fatalf("theme command completion missing %q:\n%s", want, themeCommandOutput)
		}
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
	if got, want := strings.TrimSpace(themeDeployAllOutput), "production\n--dry-run\n--restart"; got != want {
		t.Fatalf("theme deploy completion order = %q, want %q", got, want)
	}

	themePullOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "theme", "pull", ""}); got != 0 {
			t.Fatalf("Run(__complete theme pull) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(themePullOutput) != "production" {
		t.Fatalf("theme pull completion = %q, want production", themePullOutput)
	}

	themeCachePullOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "theme", "cache", "pull", ""}); got != 0 {
			t.Fatalf("Run(__complete theme cache pull) = %d, want 0", got)
		}
	})
	for _, want := range []string{"client\n", "paid-parent\n"} {
		if !strings.Contains(themeCachePullOutput, want) {
			t.Fatalf("theme cache pull completion missing %q:\n%s", want, themeCachePullOutput)
		}
	}
	themeCacheRemoteOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "theme", "cache", "pull", "paid-parent", ""}); got != 0 {
			t.Fatalf("Run(__complete theme cache pull remote) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(themeCacheRemoteOutput) != "production" {
		t.Fatalf("theme cache pull remote completion = %q, want production", themeCacheRemoteOutput)
	}

	pluginOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "pl"}); got != 0 {
			t.Fatalf("Run(__complete pl) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(pluginOutput) != "plugin" {
		t.Fatalf("plugin completion = %q, want plugin", pluginOutput)
	}

	pluginCommandOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "plugin", ""}); got != 0 {
			t.Fatalf("Run(__complete plugin) = %d, want 0", got)
		}
	})
	for _, want := range []string{"list\n", "ls\n", "add\n", "remove\n", "rm\n", "status\n", "diff\n", "install\n", "cache\n", "help\n"} {
		if !strings.Contains(pluginCommandOutput, want) {
			t.Fatalf("plugin command completion missing %q:\n%s", want, pluginCommandOutput)
		}
	}

	pluginAddOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "plugin", "add", ""}); got != 0 {
			t.Fatalf("Run(__complete plugin add) = %d, want 0", got)
		}
	})
	for _, want := range []string{"--source\n", "--manual\n", "--note\n", "--no-activate\n", "--no-auto-update\n"} {
		if !strings.Contains(pluginAddOutput, want) {
			t.Fatalf("plugin add completion missing %q:\n%s", want, pluginAddOutput)
		}
	}

	pluginRemoveOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "plugin", "remove", ""}); got != 0 {
			t.Fatalf("Run(__complete plugin remove) = %d, want 0", got)
		}
	})
	if !strings.Contains(pluginRemoveOutput, "stream\n") {
		t.Fatalf("plugin remove completion missing stream:\n%s", pluginRemoveOutput)
	}

	pluginStatusOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "plugin", "status", ""}); got != 0 {
			t.Fatalf("Run(__complete plugin status) = %d, want 0", got)
		}
	})
	if !strings.Contains(pluginStatusOutput, "production\n") {
		t.Fatalf("plugin status completion missing production:\n%s", pluginStatusOutput)
	}

	pluginDiffOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "plugin", "diff", ""}); got != 0 {
			t.Fatalf("Run(__complete plugin diff) = %d, want 0", got)
		}
	})
	if !strings.Contains(pluginDiffOutput, "production\n") {
		t.Fatalf("plugin diff completion missing production:\n%s", pluginDiffOutput)
	}

	pluginInstallOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "plugin", "install", ""}); got != 0 {
			t.Fatalf("Run(__complete plugin install) = %d, want 0", got)
		}
	})
	for _, want := range []string{"production\n", "--dry-run\n", "--yes\n"} {
		if !strings.Contains(pluginInstallOutput, want) {
			t.Fatalf("plugin install completion missing %q:\n%s", want, pluginInstallOutput)
		}
	}

	pluginPullOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "plugin", "pull", ""}); got != 0 {
			t.Fatalf("Run(__complete plugin pull) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(pluginPullOutput) != "stream" {
		t.Fatalf("plugin pull completion = %q, want stream", pluginPullOutput)
	}
	pluginPullRemoteOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "plugin", "pull", "stream", ""}); got != 0 {
			t.Fatalf("Run(__complete plugin pull remote) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(pluginPullRemoteOutput) != "production" {
		t.Fatalf("plugin pull remote completion = %q, want production", pluginPullRemoteOutput)
	}
	pluginCachePullOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "plugin", "cache", "pull", ""}); got != 0 {
			t.Fatalf("Run(__complete plugin cache pull) = %d, want 0", got)
		}
	})
	for _, want := range []string{"cached-pro\n", "stream\n"} {
		if !strings.Contains(pluginCachePullOutput, want) {
			t.Fatalf("plugin cache pull completion missing %q:\n%s", want, pluginCachePullOutput)
		}
	}
	pluginCacheRemoteOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "plugin", "cache", "pull", "cached-pro", ""}); got != 0 {
			t.Fatalf("Run(__complete plugin cache pull remote) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(pluginCacheRemoteOutput) != "production" {
		t.Fatalf("plugin cache pull remote completion = %q, want production", pluginCacheRemoteOutput)
	}

	aliasOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "al"}); got != 0 {
			t.Fatalf("Run(__complete al) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(aliasOutput) != "alias" {
		t.Fatalf("alias completion = %q, want alias", aliasOutput)
	}

	aliasCommandOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "alias", ""}); got != 0 {
			t.Fatalf("Run(__complete alias) = %d, want 0", got)
		}
	})
	for _, want := range []string{"list\n", "ls\n", "status\n", "sync\n", "add\n", "remove\n", "rm\n", "help\n"} {
		if !strings.Contains(aliasCommandOutput, want) {
			t.Fatalf("alias command completion missing %q:\n%s", want, aliasCommandOutput)
		}
	}

	aliasStatusOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "alias", "status", ""}); got != 0 {
			t.Fatalf("Run(__complete alias status) = %d, want 0", got)
		}
	})
	if !strings.Contains(aliasStatusOutput, "production\n") {
		t.Fatalf("alias status completion missing production:\n%s", aliasStatusOutput)
	}

	aliasRemoveOutput := captureStdout(t, func() {
		if got := Run([]string{"__complete", "--", "alias", "remove", ""}); got != 0 {
			t.Fatalf("Run(__complete alias remove) = %d, want 0", got)
		}
	})
	if strings.TrimSpace(aliasRemoveOutput) != "files" {
		t.Fatalf("alias remove completion = %q, want files", aliasRemoveOutput)
	}
}

func TestRunPasswordDeriveUsesScopeValueOrderAndPasswordVersion(t *testing.T) {
	t.Setenv("NF_PASSWORD_SALT", "test-salt")

	output := captureStdout(t, func() {
		if got := Run([]string{"password", "derive", "wp-admin", "client", "--password-version", "2"}); got != 0 {
			t.Fatalf("Run(password derive) = %d, want 0", got)
		}
	})
	want := passwords.DerivePassword("client:v2", "wp-admin", "test-salt") + "\n"
	if output != want {
		t.Fatalf("password derive output = %q, want %q", output, want)
	}
}

func TestRunPasswordDeriveUsesMatchingProjectPasswordVersion(t *testing.T) {
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	metadata := &project.Manifest{
		Version:   project.ManifestVersion,
		Project:   project.Project{Slug: "client", PasswordVersion: 3},
		WordPress: project.WordPress{Themes: []any{"twentytwentyfive"}},
	}
	if err := project.Save(repoRoot, metadata); err != nil {
		t.Fatalf("project.Save() error = %v", err)
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
		if got := Run([]string{"password", "derive", "mysql", "client"}); got != 0 {
			t.Fatalf("Run(password derive) = %d, want 0", got)
		}
	})
	want := passwords.DerivePassword("client:v3", "mysql", "test-salt") + "\n"
	if output != want {
		t.Fatalf("password derive output = %q, want %q", output, want)
	}
}

func TestRunPasswordDerivePromptsWhenArgumentsAreMissing(t *testing.T) {
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	oldSelect := passwordDeriveSelectFn
	oldPrompt := passwordDerivePromptString
	t.Cleanup(func() {
		passwordDeriveSelectFn = oldSelect
		passwordDerivePromptString = oldPrompt
	})
	var selectTitle string
	var selectOptions []ui.SelectOption
	passwordDeriveSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectTitle = title
		selectOptions = append([]ui.SelectOption(nil), options...)
		return "db-admin", nil
	}
	var prompts []string
	passwordDerivePromptString = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		prompts = append(prompts, prompt)
		return "app1.example.com", nil
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"password", "derive"}); got != 0 {
			t.Fatalf("Run(password derive) = %d, want 0", got)
		}
	})
	if selectTitle != "Choose a password scope" {
		t.Fatalf("select title = %q, want password scope picker", selectTitle)
	}
	values := make([]string, 0, len(selectOptions))
	for _, option := range selectOptions {
		values = append(values, option.Value)
	}
	if !reflect.DeepEqual(values, []string{"wp-admin", "mysql", "basic-auth", "linode-root", "db-admin"}) {
		t.Fatalf("select option values = %#v", values)
	}
	if !reflect.DeepEqual(prompts, []string{"Target hostname (example: linode1.nonfiction.dev)"}) {
		t.Fatalf("prompts = %#v, want identity prompt only", prompts)
	}
	want := passwords.DerivePassword("app1.example.com", "db-admin", "test-salt") + "\n"
	if output != want {
		t.Fatalf("password derive output = %q, want %q", output, want)
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
		"Default WordPress user: ":     defaultWordPressAdminUser,
		"Basic auth default user: ":    "nonfiction",
		"Database default user: ":      "admin",
		"Kinsta default PHP version: ": "8.3",
		"Linode default region: ":      "ca-central",
		"Linode default SSH user: ":    "nonfiction",
		"Linode default type: ":        "g6-standard-1",
	}
	configIsInteractive = func() bool { return true }
	configPromptString = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		if prompt == "Default WordPress user: " && defaultValue != defaultWordPressAdminUser {
			t.Fatalf("Default WordPress user default = %q, want %q", defaultValue, defaultWordPressAdminUser)
		}
		if prompt == "Database default user: " && defaultValue != defaultDatabaseUser {
			t.Fatalf("Database default user default = %q, want %q", defaultValue, defaultDatabaseUser)
		}
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
		"base_domain":            "nonfiction.dev",
		"default_wp_email":       "web@nonfiction.ca",
		"default_wp_user":        defaultWordPressAdminUser,
		"basicauth_default_user": "nonfiction",
		"db_default_user":        defaultDatabaseUser,
		"kinsta_default_php":     "8.3",
		"linode_default_region":  "ca-central",
		"linode_default_user":    "nonfiction",
		"linode_default_type":    "g6-standard-1",
	} {
		if got := values[key]; got != want {
			t.Fatalf("config[%s] = %q, want %q", key, got, want)
		}
	}
}

func TestInitGlobalConfigValidatesDBDefaultUser(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)

	oldPromptString := configPromptString
	oldIsInteractive := configIsInteractive
	t.Cleanup(func() {
		configPromptString = oldPromptString
		configIsInteractive = oldIsInteractive
	})

	answers := map[string]string{
		"Base domain: ":             "nonfiction.dev",
		"Default WordPress email: ": "web@nonfiction.ca",
		"Default WordPress user: ":  "admin",
		"Basic auth default user: ": "nonfiction",
		"Database default user: ":   "bad user",
	}
	configIsInteractive = func() bool { return true }
	configPromptString = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		if value, ok := answers[prompt]; ok {
			return value, nil
		}
		return defaultValue, nil
	}

	err := initGlobalConfig(configInitSettings(), false)
	if err == nil || !strings.Contains(err.Error(), "db_default_user") {
		t.Fatalf("initGlobalConfig() error = %v, want db_default_user validation", err)
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
		if got := Run([]string{"config", "set", "kinsta.php", "8.3"}); got != 0 {
			t.Fatalf("Run(config set kinsta.php) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Set kinsta.php = 8.3") || !strings.Contains(output, "Path "+config.ConfigFile()) {
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

func TestRunConfigGetKinstaDefaultPHP(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	if err := saveGlobalConfig(map[string]string{"kinsta_default_php": "8.2"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"config", "get", "kinsta.php"}); got != 0 {
			t.Fatalf("Run(config get kinsta.php) = %d, want 0", got)
		}
	})
	if got, want := strings.TrimSpace(output), "8.2"; got != want {
		t.Fatalf("config get kinsta.php = %q, want %q", got, want)
	}
}

func TestRunConfigGetPromptsForKeyWhenMissing(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	if err := saveGlobalConfig(map[string]string{"kinsta_default_php": "8.2"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}

	oldSelect := configSelectFn
	oldInteractive := configIsInteractive
	t.Cleanup(func() {
		configSelectFn = oldSelect
		configIsInteractive = oldInteractive
	})
	configIsInteractive = func() bool { return true }
	configSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		if title != "Choose a config key" {
			t.Fatalf("select title = %q, want Choose a config key", title)
		}
		if !slices.ContainsFunc(options, func(option ui.SelectOption) bool {
			return option.Value == "kinsta.php" && strings.Contains(option.Label, "default Kinsta PHP version")
		}) {
			t.Fatalf("select options missing kinsta.php: %#v", options)
		}
		return "kinsta.php", nil
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"config", "get"}); got != 0 {
			t.Fatalf("Run(config get) = %d, want 0", got)
		}
	})
	if got, want := strings.TrimSpace(output), "8.2"; got != want {
		t.Fatalf("config get picker output = %q, want %q", got, want)
	}
}

func TestRunConfigGetWithoutKeyRequiresInteractive(t *testing.T) {
	oldInteractive := configIsInteractive
	t.Cleanup(func() { configIsInteractive = oldInteractive })
	configIsInteractive = func() bool { return false }

	stderr := captureStderr(t, func() {
		if got := Run([]string{"config", "get"}); got == 0 {
			t.Fatalf("Run(config get) = 0, want failure")
		}
	})
	if !strings.Contains(stderr, "config get requires a key") {
		t.Fatalf("stderr = %q, want interactive error", stderr)
	}
}

func TestRunConfigSetPromptsForKeyAndValueWhenMissing(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	if err := saveGlobalConfig(map[string]string{"kinsta_default_php": "8.2"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}

	oldSelect := configSelectFn
	oldPromptString := configPromptString
	oldInteractive := configIsInteractive
	t.Cleanup(func() {
		configSelectFn = oldSelect
		configPromptString = oldPromptString
		configIsInteractive = oldInteractive
	})
	configIsInteractive = func() bool { return true }
	configSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		if title != "Choose a config key" {
			t.Fatalf("select title = %q, want Choose a config key", title)
		}
		return "kinsta.php", nil
	}
	configPromptString = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		if prompt != "Value for kinsta.php" {
			t.Fatalf("prompt = %q, want Value for kinsta.php", prompt)
		}
		if defaultValue != "8.2" {
			t.Fatalf("prompt default = %q, want 8.2", defaultValue)
		}
		if allowBlank {
			t.Fatalf("allowBlank = true, want false")
		}
		return "8.3", nil
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"config", "set"}); got != 0 {
			t.Fatalf("Run(config set) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Set kinsta.php = 8.3") {
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

func TestRunConfigSetPromptsForValueWhenKeyProvided(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)

	oldPromptString := configPromptString
	oldInteractive := configIsInteractive
	t.Cleanup(func() {
		configPromptString = oldPromptString
		configIsInteractive = oldInteractive
	})
	configIsInteractive = func() bool { return true }
	configPromptString = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		if prompt != "Value for kinsta.php" {
			t.Fatalf("prompt = %q, want Value for kinsta.php", prompt)
		}
		if defaultValue != "8.3" {
			t.Fatalf("prompt default = %q, want default 8.3", defaultValue)
		}
		return "8.4", nil
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"config", "set", "kinsta.php"}); got != 0 {
			t.Fatalf("Run(config set kinsta.php) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Set kinsta.php = 8.4") {
		t.Fatalf("output = %q, want set message", output)
	}
}

func TestRunConfigSetWithoutArgsRequiresInteractive(t *testing.T) {
	oldInteractive := configIsInteractive
	t.Cleanup(func() { configIsInteractive = oldInteractive })
	configIsInteractive = func() bool { return false }

	stderr := captureStderr(t, func() {
		if got := Run([]string{"config", "set"}); got == 0 {
			t.Fatalf("Run(config set) = 0, want failure")
		}
	})
	if !strings.Contains(stderr, "config set requires a key and value") {
		t.Fatalf("stderr = %q, want interactive error", stderr)
	}
}

func TestRunConfigUnsetKinstaRegion(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	if err := saveGlobalConfig(map[string]string{"kinsta_default_region": "us-central1", "kinsta_default_php": "8.3"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"config", "unset", "kinsta.region"}); got != 0 {
			t.Fatalf("Run(config unset kinsta.region) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Unset kinsta.region") || !strings.Contains(output, "Path "+config.ConfigFile()) {
		t.Fatalf("output = %q, want unset message", output)
	}
	values, err := loadGlobalConfig()
	if err != nil {
		t.Fatalf("loadGlobalConfig() error = %v", err)
	}
	if _, ok := values["kinsta_default_region"]; ok {
		t.Fatalf("kinsta_default_region still present: %#v", values)
	}
	if got := values["kinsta_default_php"]; got != "8.3" {
		t.Fatalf("kinsta_default_php = %q, want preserved value", got)
	}
}

func TestRunConfigKeysListsExpectedKeys(t *testing.T) {
	output := captureStdout(t, func() {
		if got := Run([]string{"config", "keys"}); got != 0 {
			t.Fatalf("Run(config keys) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, output, []string{
		"Config keys\n",
		"Core\n",
		"core.base-domain",
		"core.password-salt",
		"WordPress\n",
		"wordpress.admin-email",
		"Database\n",
		"database.user",
		"Docker\n",
		"docker.images.wordpress",
		"DNSimple\n",
		"dnsimple.account-id",
		"Kinsta\n",
		"kinsta.php",
		"Linode\n",
		"linode.user",
	})
}

func TestRunConfigUnknownKeyFailsClearly(t *testing.T) {
	stderr := captureStderr(t, func() {
		if got := Run([]string{"config", "get", "kinsta.foo"}); got == 0 {
			t.Fatalf("Run(config get kinsta.foo) = 0, want failure")
		}
	})
	if !strings.Contains(stderr, "Unknown config key: kinsta.foo") || !strings.Contains(stderr, "nf config keys") {
		t.Fatalf("stderr = %q, want unknown-key guidance", stderr)
	}
}

func TestRunConfigSetBasicAuthDefaultUser(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)

	output := captureStdout(t, func() {
		if got := Run([]string{"config", "set-basicauth-default-user", "preview"}); got != 0 {
			t.Fatalf("Run(config set-basicauth-default-user) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Deprecated. Use:") || !strings.Contains(output, "nf config set wordpress.basic-auth-user preview") || !strings.Contains(output, "Set wordpress.basic-auth-user = preview") {
		t.Fatalf("output = %q, want deprecated set message", output)
	}
	values, err := loadGlobalConfig()
	if err != nil {
		t.Fatalf("loadGlobalConfig() error = %v", err)
	}
	if got := values["basicauth_default_user"]; got != "preview" {
		t.Fatalf("basicauth_default_user = %q, want preview", got)
	}
}

func TestRunConfigSetDBDefaultUser(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)

	output := captureStdout(t, func() {
		if got := Run([]string{"config", "set", "database.user", "dbadmin"}); got != 0 {
			t.Fatalf("Run(config set database.user) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Set database.user = dbadmin") {
		t.Fatalf("output = %q, want set message", output)
	}
	values, err := loadGlobalConfig()
	if err != nil {
		t.Fatalf("loadGlobalConfig() error = %v", err)
	}
	if got := values["db_default_user"]; got != "dbadmin" {
		t.Fatalf("db_default_user = %q, want dbadmin", got)
	}
}

func TestRunConfigShowGroupsSettings(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := saveGlobalConfig(map[string]string{
		"base_domain":            "nonfiction.dev",
		"default_wp_email":       "web@nonfiction.ca",
		"default_wp_user":        "admin",
		"basicauth_default_user": "nonfiction",
		"db_default_user":        "dbadmin",
		"dnsimple_account_id":    "14",
		"kinsta_default_php":     "8.3",
		"linode_default_region":  "ca-central",
		"linode_default_type":    "g6-standard-1",
		"linode_default_user":    "nonfiction",
	}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"config", "show"}); got != 0 {
			t.Fatalf("Run(config show) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, output, []string{
		"config\n",
		"──────\n",
		"Path   " + config.ConfigFile() + "\n",
		"Core\n",
		"  Base domain     nonfiction.dev\n",
		"  Password salt   set\n",
		"WordPress\n",
		"  Admin email       web@nonfiction.ca\n",
		"  Admin user        admin\n",
		"  Basic auth user   nonfiction\n",
		"Database\n",
		"  User   dbadmin\n",
		"DNSimple\n",
		"  Account ID   14\n",
		"Kinsta\n",
		"  Region   unset\n",
		"  PHP      8.3\n",
		"Linode\n",
		"  Region   ca-central\n",
		"  Type     g6-standard-1\n",
		"  Image    unset\n",
		"  User     nonfiction",
	})
	for _, notWant := range []string{"Default WP Email", "Default WP User", "Basic Auth Default User", "Password Salt:"} {
		if strings.Contains(output, notWant) {
			t.Fatalf("config show output contains %q:\n%s", notWant, output)
		}
	}
}

func TestRunConfigShowMarksFallbackDefaults(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_PASSWORD_SALT", "")
	if err := saveGlobalConfig(map[string]string{
		"base_domain":      "nonfiction.dev",
		"default_wp_email": "web@nonfiction.ca",
	}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"config", "show"}); got != 0 {
			t.Fatalf("Run(config show) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, output, []string{
		"Core\n",
		"  Base domain     nonfiction.dev\n",
		"  Password salt   unset\n",
		"WordPress\n",
		"  Admin email       web@nonfiction.ca\n",
		"  Admin user        nonfiction (default)\n",
		"  Basic auth user   nonfiction (default)\n",
		"Database\n",
		"  User   admin (default)\n",
		"DNSimple\n",
		"  Account ID   unset\n",
		"Kinsta\n",
		"  Region   unset\n",
		"  PHP      8.3 (default)\n",
		"Linode\n",
		"  Region   ca-central (default)\n",
		"  Type     g6-standard-1 (default)\n",
		"  Image    unset\n",
		"  User     nonfiction (default)",
	})
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
	for _, want := range []string{"Provider: dnsimple", "Status: configured", filepath.Join(stateDir, "providers.json"), "Account ID: 14", "Targets: 0"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, output)
		}
	}
	for _, unwanted := range []string{"Account email", "hello@example.com", "dnsimple-token-secret", "DNSIMPLE_TOKEN", `"provider": "dnsimple"`} {
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
	for _, want := range []string{"Provider linode health check passed.", "username: nf-user", "restricted: false", "Saved provider metadata"} {
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

func TestRunProviderCheckDNSimpleSuppressesAccountEmail(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("DNSIMPLE_TOKEN", "dnsimple-token-secret")
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	oldCheck := providerCheckDNSimpleFn
	providerCheckDNSimpleFn = func() (providerHealthResult, error) {
		return providerHealthResult{
			Provider: "dnsimple",
			Details:  map[string]string{"account_email": "hello@example.com", "account_id": "14", "managed_domain": "nonfiction.dev"},
			Record: map[string]any{
				"provider":       "dnsimple",
				"account_email":  "hello@example.com",
				"account_id":     "14",
				"managed_domain": "nonfiction.dev",
				"targets":        []map[string]any{},
			},
		}, nil
	}
	t.Cleanup(func() { providerCheckDNSimpleFn = oldCheck })

	output := captureStdout(t, func() {
		if got := Run([]string{"provider", "check", "dnsimple"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, want := range []string{"Provider dnsimple health check passed.", "account_id: 14", "managed_domain: nonfiction.dev", "Saved provider metadata"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, output)
		}
	}
	for _, unwanted := range []string{"account_email", "hello@example.com"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("Run() output included %q:\n%s", unwanted, output)
		}
	}
	records, err := state.LoadStateRecords("providers")
	if err != nil {
		t.Fatalf("LoadStateRecords(providers) error = %v", err)
	}
	if len(records) != 1 || recordValueString(records[0]["account_email"]) != "hello@example.com" {
		t.Fatalf("stored provider records = %#v, want account_email preserved", records)
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
	if strings.Contains(output, "health check passed") || strings.Contains(output, "Saved provider metadata") {
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
	for _, want := range []string{"Checking providers...", "Provider dnsimple health check passed.", "Provider kinsta health check passed.", "Provider linode health check passed."} {
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

func TestRunRefreshUpdatesProvidersAndSites(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
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
	oldRunSSHOutput := runSSHOutputFn
	t.Cleanup(func() {
		providerCheckDNSimpleFn = oldDNSimple
		providerCheckKinstaFn = oldKinsta
		providerCheckLinodeFn = oldLinode
		runSSHOutputFn = oldRunSSHOutput
	})
	providerCheckDNSimpleFn = func() (providerHealthResult, error) {
		return providerHealthResult{Provider: "dnsimple", Details: map[string]string{"managed_domain": "nonfiction.dev"}, Record: map[string]any{"provider": "dnsimple", "targets": []map[string]any{}}}, nil
	}
	providerCheckKinstaFn = func() (providerHealthResult, error) {
		return providerHealthResult{Provider: "kinsta", Details: map[string]string{"status": "active"}, Record: map[string]any{"provider": "kinsta", "targets": []map[string]any{}}}, nil
	}
	providerCheckLinodeFn = func() (providerHealthResult, error) {
		return providerHealthResult{Provider: "linode", Details: map[string]string{"targets": "1"}, Record: map[string]any{"provider": "linode", "targets": []map[string]any{{"id": "98222343", "name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}, nil
	}
	runSSHOutputFn = func(args []string) ([]byte, error) {
		if len(args) > 0 && args[len(args)-1] == "/var/lib/nf/target.json" {
			return []byte(`{"php_version":"8.3"}`), nil
		}
		return []byte(`[{"site_id":"client.app1-linode","name":"client","env":"live","target":"app1-linode","url":"https://client.app1-linode.nonfiction.dev/"}]`), nil
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"refresh"}); got != 0 {
			t.Fatalf("Run(refresh) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, output, []string{"Refresh updates all provider, target, and site caches.", "Provider dnsimple health check passed.", "Provider kinsta health check passed.", "Provider linode health check passed.", "Refreshing sites...", "Refreshed targets: 1", "Discovered remote site envs: 1", "Refresh complete."})
	providers, err := state.LoadStateRecords("providers")
	if err != nil {
		t.Fatalf("LoadStateRecords(providers) error = %v", err)
	}
	if len(providers) != 3 {
		t.Fatalf("provider records len = %d, want 3: %#v", len(providers), providers)
	}
	sites, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(sites) != 1 || siteRecordID(sites[0]) != "client.app1-linode" {
		t.Fatalf("site records = %#v, want discovered client site", sites)
	}
}

func TestRunRefreshBestEffortAfterProviderFailure(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
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
	oldRunSSHOutput := runSSHOutputFn
	t.Cleanup(func() {
		providerCheckDNSimpleFn = oldDNSimple
		providerCheckKinstaFn = oldKinsta
		providerCheckLinodeFn = oldLinode
		runSSHOutputFn = oldRunSSHOutput
	})
	providerCheckDNSimpleFn = func() (providerHealthResult, error) {
		return providerHealthResult{}, fmt.Errorf("dnsimple unavailable")
	}
	providerCheckKinstaFn = func() (providerHealthResult, error) {
		return providerHealthResult{Provider: "kinsta", Details: map[string]string{"status": "active"}, Record: map[string]any{"provider": "kinsta", "targets": []map[string]any{}}}, nil
	}
	providerCheckLinodeFn = func() (providerHealthResult, error) {
		return providerHealthResult{Provider: "linode", Details: map[string]string{"targets": "1"}, Record: map[string]any{"provider": "linode", "targets": []map[string]any{{"id": "98222343", "name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}, nil
	}
	runSSHOutputFn = func(args []string) ([]byte, error) {
		if len(args) > 0 && args[len(args)-1] == "/var/lib/nf/target.json" {
			return []byte(`{"php_version":"8.3"}`), nil
		}
		return []byte(`[{"site_id":"client.app1-linode","name":"client","env":"live","target":"app1-linode","url":"https://client.app1-linode.nonfiction.dev/"}]`), nil
	}

	var output string
	stderr := captureStderr(t, func() {
		output = captureStdout(t, func() {
			if got := Run([]string{"refresh"}); got != 1 {
				t.Fatalf("Run(refresh) = %d, want 1", got)
			}
		})
	})
	assertContainsInOrder(t, output, []string{"Provider dnsimple health check failed.", "Provider kinsta health check passed.", "Provider linode health check passed.", "Refreshing sites...", "Refreshed targets: 1", "Discovered remote site envs: 1"})
	for _, want := range []string{"dnsimple unavailable", "refresh failed for: provider dnsimple"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr)
		}
	}
	sites, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(sites) != 1 || siteRecordID(sites[0]) != "client.app1-linode" {
		t.Fatalf("site records = %#v, want best-effort discovered site", sites)
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

func TestRunTargetRefreshPreservesLinodeTargetMetadata(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("KINSTA_API_KEY", "")
	t.Setenv("LINODE_TOKEN", "linode-token-secret")
	t.Setenv("LINODE_CLI_TOKEN", "")

	oldLinode := providerCheckLinodeFn
	t.Cleanup(func() { providerCheckLinodeFn = oldLinode })
	providerCheckLinodeFn = func() (providerHealthResult, error) {
		return providerHealthResult{Provider: "linode", Record: map[string]any{"provider": "linode", "targets": []map[string]any{{
			"id":       "98222343",
			"name":     "app1-linode",
			"provider": "linode",
			"status":   "running",
			"region":   "ca-central",
			"ipv4":     "203.0.113.10",
			"ssh":      map[string]any{"host": "app1-linode.nonfiction.dev"},
		}}}}, nil
	}
	providers := []map[string]any{{
		"provider": "linode",
		"targets": []map[string]any{{
			"provider_id": "98222343",
			"name":        "app1-linode",
			"provider":    "linode",
			"hostname":    "app1-linode.nonfiction.dev",
			"status":      "provisioned",
			"phase":       "complete",
			"target_path": "/var/lib/nf/target.json",
			"sites_path":  "/var/lib/nf/sites.json",
			"ssh":         map[string]any{"host": "app1-linode.nonfiction.dev", "user": "custom", "port": "2222"},
			"db":          map[string]any{"url": "https://dbadmin.app1-linode.nonfiction.dev/", "user": "dbadmin"},
			"credentials": map[string]any{"db": map[string]any{"identity": "app1-linode.nonfiction.dev", "purpose": "db-admin", "stored": false}},
		}},
	}}
	if err := state.SaveStateRecords("providers", providers); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"target", "refresh"}); got != 0 {
			t.Fatalf("Run(target refresh) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Provider linode refreshed. Targets: 1") {
		t.Fatalf("target refresh output missing linode refresh:\n%s", output)
	}
	targets, err := cachedTargets()
	if err != nil {
		t.Fatalf("cachedTargets() error = %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("cachedTargets() len = %d, want 1: %#v", len(targets), targets)
	}
	target := targets[0]
	for key, want := range map[string]string{
		"id":          "98222343",
		"name":        "app1-linode",
		"status":      "provisioned",
		"phase":       "complete",
		"target_path": "/var/lib/nf/target.json",
		"sites_path":  "/var/lib/nf/sites.json",
		"region":      "ca-central",
		"ipv4":        "203.0.113.10",
	} {
		if got := recordValueString(target[key]); got != want {
			t.Fatalf("target[%s] = %q, want %q: %#v", key, got, want, target)
		}
	}
	if got := targetDBURL(target); got != "https://dbadmin.app1-linode.nonfiction.dev/" {
		t.Fatalf("targetDBURL() = %q, want cached database URL", got)
	}
	if got := targetDBUser(target); got != "dbadmin" {
		t.Fatalf("targetDBUser() = %q, want cached database user", got)
	}
	if got := mapStringAtPath(target, "credentials", "db", "purpose"); got != "db-admin" {
		t.Fatalf("credentials.db.purpose = %q, want db-admin", got)
	}
	if got := mapStringAtPath(target, "ssh", "user"); got != "custom" {
		t.Fatalf("ssh.user = %q, want custom", got)
	}
	if got := mapStringAtPath(target, "ssh", "port"); got != "2222" {
		t.Fatalf("ssh.port = %q, want 2222", got)
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
		if !strings.Contains(stdout, "Provider dnsimple health check failed.") {
			t.Fatalf("Run() stdout missing health check failure:\n%s", stdout)
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
	assertContainsInOrder(t, showOutput, []string{
		"app1-linode\n",
		"───────────\n",
		"Provider        linode\n",
		"Hostname        203.0.113.10\n",
		"ID              98222343\n",
		"Status          active\n",
		"Cached status   active",
	})
	for _, notWant := range []string{"Target:", "SSH:"} {
		if strings.Contains(showOutput, notWant) {
			t.Fatalf("target show output contains %q:\n%s", notWant, showOutput)
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
	assertContainsInOrder(t, output, []string{
		"app1-linode\n",
		"───────────\n",
		"Provider   linode\n",
		"Hostname   app1-linode.nonfiction.dev\n",
		"ID         98222343\n",
		"Status     reachable\n",
		"Access\n",
		"  SSH   ssh app1-linode.nonfiction.dev",
	})
	for _, notWant := range []string{"Target:", "SSH:"} {
		if strings.Contains(output, notWant) {
			t.Fatalf("target show output contains %q:\n%s", notWant, output)
		}
	}
}

func TestRunTargetShowDisplaysCachedAdminerURL(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{
		"name":     "app1-linode",
		"provider": "linode",
		"hostname": "app1-linode.nonfiction.dev",
		"ssh":      map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"},
		"adminer": map[string]any{
			"url":  "https://adminer.app1-linode.nonfiction.dev/",
			"user": "adminer",
			"auth": map[string]any{"password": map[string]any{"identity": "app1-linode.nonfiction.dev", "purpose": "adminer", "stored": false}},
		},
	}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	oldSSH := targetSSHReachableFn
	targetSSHReachableFn = func(record map[string]any) bool { return true }
	t.Cleanup(func() { targetSSHReachableFn = oldSSH })

	output := captureStdout(t, func() {
		if got := Run([]string{"target", "show", "app1-linode"}); got != 0 {
			t.Fatalf("Run(target show) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, output, []string{
		"Access\n",
		"  SSH        ssh nonfiction@app1-linode.nonfiction.dev\n",
		"  Database   https://adminer.app1-linode.nonfiction.dev/",
		"   - User   adminer\n",
		"   - Pass   " + passwords.DerivePassword("app1-linode.nonfiction.dev", "adminer", "test-salt"),
	})
}

func TestRunTargetShowReadsRemoteAdminerMetadataWhenCacheIsMissing(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{
		"name":     "app1-linode",
		"provider": "linode",
		"hostname": "app1-linode.nonfiction.dev",
		"ssh":      map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"},
	}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	var sshArgs []string
	oldSSH := targetSSHReachableFn
	oldSSHOutput := runSSHOutputFn
	targetSSHReachableFn = func(record map[string]any) bool { return true }
	runSSHOutputFn = func(args []string) ([]byte, error) {
		sshArgs = append([]string(nil), args...)
		return []byte(`{
  "hostname": "app1-linode.nonfiction.dev",
  "adminer": {
    "tool": "AdminNeo",
    "version": "5.4.1",
    "hostname": "adminer.app1-linode.nonfiction.dev",
    "url": "https://adminer.app1-linode.nonfiction.dev/",
    "user": "adminer",
    "auth": {"password": {"identity": "app1-linode.nonfiction.dev", "purpose": "adminer-console", "stored": false}}
  }
}`), nil
	}
	t.Cleanup(func() {
		targetSSHReachableFn = oldSSH
		runSSHOutputFn = oldSSHOutput
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"target", "show", "app1-linode"}); got != 0 {
			t.Fatalf("Run(target show) = %d, want 0", got)
		}
	})
	password := passwords.DerivePassword("app1-linode.nonfiction.dev", "adminer-console", "test-salt")
	assertContainsInOrder(t, output, []string{
		"Access\n",
		"  SSH        ssh nonfiction@app1-linode.nonfiction.dev\n",
		"  Database   https://adminer.app1-linode.nonfiction.dev/",
		"   - User   adminer\n",
		"   - Pass   " + password,
	})
	if got := strings.Join(sshArgs, " "); !strings.Contains(got, "nonfiction@app1-linode.nonfiction.dev") || !strings.Contains(got, "/var/lib/nf/target.json") {
		t.Fatalf("ssh args = %#v, want target.json read", sshArgs)
	}
}

func TestRunTargetAdminerCommandIsRemoved(t *testing.T) {
	stderr := captureStderr(t, func() {
		if got := Run([]string{"target", "adminer", "show", "app1-linode"}); got != 1 {
			t.Fatalf("Run(removed target subcommand) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "unsupported target command") {
		t.Fatalf("stderr = %q, want unsupported target command", stderr)
	}
}

func TestRunTargetPasswordPrintsRootPasswordByDefault(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev"}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"target", "password", "app1-linode"}); got != 0 {
			t.Fatalf("Run(target password) = %d, want 0", got)
		}
	})
	want := passwords.DerivePassword("app1-linode.nonfiction.dev", "linode-root", "test-salt") + "\n"
	if output != want {
		t.Fatalf("target password output = %q, want %q", output, want)
	}
}

func TestRunTargetPasswordPrintsDBPassword(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{
		"name":     "app1-linode",
		"provider": "linode",
		"hostname": "app1-linode.nonfiction.dev",
		"ssh":      map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"},
	}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	oldSSHOutput := runSSHOutputFn
	runSSHOutputFn = func(args []string) ([]byte, error) {
		return []byte(`{"hostname":"app1-linode.nonfiction.dev","adminer":{"auth":{"password":{"identity":"app1-linode.nonfiction.dev","purpose":"adminer-console"}}}}`), nil
	}
	t.Cleanup(func() { runSSHOutputFn = oldSSHOutput })

	output := captureStdout(t, func() {
		if got := Run([]string{"target", "password", "app1-linode", "--db"}); got != 0 {
			t.Fatalf("Run(target password --db) = %d, want 0", got)
		}
	})
	want := passwords.DerivePassword("app1-linode.nonfiction.dev", "adminer-console", "test-salt") + "\n"
	if output != want {
		t.Fatalf("target password --db output = %q, want %q", output, want)
	}
}

func TestRunTargetPasswordRejectsNonLinode(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "kinsta", "targets": []map[string]any{{"name": "kinsta", "provider": "kinsta"}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}

	stderr := captureStderr(t, func() {
		if got := Run([]string{"target", "password", "kinsta"}); got != 1 {
			t.Fatalf("Run(target password kinsta) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "target passwords are only available on linode targets") {
		t.Fatalf("stderr = %q", stderr)
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
		if got := Run([]string{"target", "add", "linode", "app1", "--dry-run", "--non-interactive", "--region", "ca-central", "--type", "g6-standard-1", "--image", "linode/ubuntu24.04", "--db-user", "dbadmin", "--user", "nonfiction", "--keys", "all"}); got != 0 {
			t.Fatalf("Run(target add linode) = %d, want 0", got)
		}
	})
	for _, want := range []string{"app1", "hostname: app1.nonfiction.dev", "wildcard hostname: *.app1.nonfiction.dev", "url: https://dbadmin.app1.nonfiction.dev/", "region: ca-central", "type: g6-standard-1", "image: linode/ubuntu24.04", "user: dbadmin", "ssh user: nonfiction", "authorized keys: all Linode profile keys", "state: not checked"} {
		if !strings.Contains(output, want) {
			t.Fatalf("target add output missing %q:\n%s", want, output)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "providers.json")); !os.IsNotExist(err) {
		t.Fatalf("providers.json unexpectedly exists after dry-run: %v", err)
	}
}

func TestRunTargetAddLinodeRejectsReservedTargetName(t *testing.T) {
	stderr := captureStderr(t, func() {
		if got := Run([]string{"target", "add", "linode", "kinsta", "--dry-run", "--non-interactive"}); got != 1 {
			t.Fatalf("Run(target add linode kinsta) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, `Target name "kinsta" is reserved`) {
		t.Fatalf("Run() stderr = %q, want reserved target name error", stderr)
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
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "php": map[string]any{"version": "8.3", "service": "php8.3-fpm"}, "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
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
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
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
			_ = json.NewEncoder(w).Encode(map[string]any{"company": map[string]any{"sites": []map[string]any{{"id": "ksite123", "name": "clienthost", "display_name": "Client Host"}}}})
		case "GET /sites/ksite123/environments":
			_ = json.NewEncoder(w).Encode(map[string]any{"site": map[string]any{"environments": []map[string]any{
				{"id": "kenv-live", "name": "client", "display_name": "Client", "web_root": "/www/client_123/public", "container_info": map[string]any{"php_engine_version": "php8.3"}, "primaryDomain": map[string]any{"id": "kdom-live", "name": "client.kinsta.nonfiction.dev"}},
				{"id": "kenv-staging", "name": "client-staging", "display_name": "Client Staging", "web_root": "/www/clientstaging_456/public", "container_info": map[string]any{"php_engine_version": "php8.3"}, "primaryDomain": map[string]any{"id": "kdom-staging", "name": "client-staging.kinsta.nonfiction.dev"}},
			}}})
		case "GET /sites/environments/kenv-live/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{
				{"id": "kdom-live", "name": "client.kinsta.nonfiction.dev", "is_primary": false},
				{"id": "kdom-generated", "name": "client.kinsta.cloud", "is_primary": false},
				{"id": "kdom-generated-wildcard", "name": "*.client.kinsta.cloud", "is_primary": false},
				{"id": "kdom-www", "name": "www.client.com", "is_primary": true, "status": "verified"},
				{"id": "kdom-apex", "name": "client.com", "is_primary": false},
			}}})
		case "GET /sites/environments/kenv-staging/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{
				{"id": "kdom-staging", "name": "client-staging.kinsta.nonfiction.dev", "is_primary": false},
				{"id": "kdom-staging-generated", "name": "client-staging.kinsta.cloud", "is_primary": false},
				{"id": "kdom-staging-generated-wildcard", "name": "*.client-staging.kinsta.cloud", "is_primary": false},
				{"id": "kdom-stage-public", "name": "staging.client.com", "is_primary": true, "status": "pending_dns"},
			}}})
		case "GET /sites/ksite123/environments/kenv-live/ssh/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"host": "203.0.113.10", "port": "12345"})
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
	for _, want := range []struct {
		env, path, database, sshHost, sshPort, sshUser, primary string
		externalDomains                                         map[string]string
		internalDomains                                         []string
	}{
		{"live", "/www/client_123/public", "clienthost", "203.0.113.10", "12345", "clienthost", "www.client.com", map[string]string{"www.client.com": "verified", "client.com": "pending"}, []string{"client.kinsta.nonfiction.dev", "client.kinsta.cloud"}},
		{"staging", "/www/clientstaging_456/public", "clientstaging_456", "203.0.113.11", "12346", "clientstaging_456", "staging.client.com", map[string]string{"staging.client.com": "pending"}, []string{"client-staging.kinsta.nonfiction.dev", "client-staging.kinsta.cloud"}},
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
		if got := recordValueString(record["project_slug"]); got != "client" {
			t.Fatalf("%s project_slug = %q, want client", want.env, got)
		}
		if got := recordValueString(record["name"]); got != "client" {
			t.Fatalf("%s name = %q, want client", want.env, got)
		}
		if got := mapStringAtPath(record, "kinsta", "slug"); got != "clienthost" {
			t.Fatalf("%s kinsta.slug = %q, want clienthost", want.env, got)
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
		if got := recordValueString(record["primary_domain"]); got != want.primary {
			t.Fatalf("%s primary_domain = %q, want %q", want.env, got, want.primary)
		}
		if _, ok := record["domain_state"]; ok {
			t.Fatalf("%s domain_state should not be written in new cache model: %#v", want.env, record)
		}
		for domain := range want.externalDomains {
			if !recordHasSiteDomain(record, domain) {
				t.Fatalf("%s cached domains missing %q in %#v", want.env, domain, record["domains"])
			}
		}
		for _, domain := range want.internalDomains {
			if !recordHasSiteDomain(record, domain) {
				t.Fatalf("%s cached domains missing %q in %#v", want.env, domain, record["domains"])
			}
		}
		for _, entry := range siteDomainEntryValues(record["domains"]) {
			domain := siteDomainEntryMap(entry)
			name := recordValueString(domain["name"])
			if strings.HasPrefix(name, "*.") {
				t.Fatalf("%s cached domains include wildcard %q in %#v", want.env, name, record["domains"])
			}
			role := "secondary"
			if name == want.primary {
				role = "primary"
			}
			management := "external"
			status := want.externalDomains[name]
			if strings.Contains(name, ".kinsta.") {
				management = "internal"
				status = "active"
			}
			if recordValueString(domain["role"]) != role || recordValueString(domain["management"]) != management || recordValueString(domain["status"]) != status {
				t.Fatalf("%s domain entry = %#v, want %s %s %s", want.env, domain, role, management, status)
			}
		}
		listOutput := captureStdout(t, func() {
			if got := Run([]string{"domain", "list", canonicalEnvID("client.kinsta", want.env)}); got != 0 {
				t.Fatalf("Run(domain list %s) = %d, want 0", want.env, got)
			}
		})
		for _, internal := range want.internalDomains {
			if !strings.Contains(listOutput, internal) || !strings.Contains(listOutput, "internal") {
				t.Fatalf("%s domain list output missing internal domain %q:\n%s", want.env, internal, listOutput)
			}
		}
		if strings.Contains(listOutput, "*.") {
			t.Fatalf("%s domain list output contains wildcard:\n%s", want.env, listOutput)
		}
	}
}

func TestRunSiteAddLinodeDryRunPlansLiveOnlyByDefault(t *testing.T) {
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
	for _, want := range []string{"Add site plan:", "target: app1-linode", "site: foobar", "site id: foobar.app1-linode", "admin email: web@nonfiction.ca", "admin password: derived from foobar", "env live:", "path: /var/www/sites/foobar/public", "database: foobar", "vhost: foobar.app1-linode.nonfiction.dev", "remote state: /var/lib/nf/sites.json", "mode: dry-run"} {
		if !strings.Contains(output, want) {
			t.Fatalf("site add dry-run output missing %q:\n%s", want, output)
		}
	}
	for _, notWant := range []string{"env staging:", "foobar_staging", "foobar-staging.app1-linode.nonfiction.dev"} {
		if strings.Contains(output, notWant) {
			t.Fatalf("site add dry-run output contains %q:\n%s", notWant, output)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sites.json")); !os.IsNotExist(err) {
		t.Fatalf("sites.json unexpectedly exists after dry-run: %v", err)
	}
}

func TestRunSiteAddWithoutArgumentsPromptsForTargetSiteAndStaging(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev", "default_wp_email": "web@nonfiction.ca", "default_wp_user": "admin", "linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "php": map[string]any{"version": "8.3", "service": "php8.3-fpm"}, "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}

	oldSelect := siteAddSelectFn
	oldPrompt := siteAddPromptStringFn
	oldConfirm := siteAddConfirmFn
	oldInteractive := siteIsInteractiveFn
	oldRunSSH := runSSHScriptFn
	t.Cleanup(func() {
		siteAddSelectFn = oldSelect
		siteAddPromptStringFn = oldPrompt
		siteAddConfirmFn = oldConfirm
		siteIsInteractiveFn = oldInteractive
		runSSHScriptFn = oldRunSSH
	})

	siteIsInteractiveFn = func() bool { return true }
	var targetOptions []ui.SelectOption
	var stagingOptions []ui.SelectOption
	siteAddSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		switch title {
		case "Choose a target":
			targetOptions = append([]ui.SelectOption(nil), options...)
			return "app1-linode", nil
		case "Create a staging environment too":
			stagingOptions = append([]ui.SelectOption(nil), options...)
			return "yes", nil
		default:
			t.Fatalf("unexpected select title %q", title)
			return "", nil
		}
	}
	var prompts []string
	siteAddPromptStringFn = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		prompts = append(prompts, prompt)
		if prompt != "Site name" {
			t.Fatalf("unexpected prompt %q", prompt)
		}
		return "foobar", nil
	}
	var confirmMessage string
	siteAddConfirmFn = func(prompt string, defaultYes bool) (bool, error) {
		confirmMessage = prompt
		if defaultYes {
			t.Fatalf("site add confirm defaultYes = true, want false")
		}
		return true, nil
	}
	var sshUser, sshHost, sshScript string
	runSSHScriptFn = func(user, host, script string) error {
		sshUser, sshHost, sshScript = user, host, script
		return nil
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "add"}); got != 0 {
			t.Fatalf("Run(site add) = %d, want 0", got)
		}
	})
	if len(targetOptions) != 1 || targetOptions[0] != (ui.SelectOption{Value: "app1-linode", Label: "app1-linode (linode)"}) {
		t.Fatalf("target options = %#v", targetOptions)
	}
	if len(stagingOptions) != 2 || stagingOptions[0].Value != "yes" || stagingOptions[1] != (ui.SelectOption{Value: "no", Label: "No", Default: true}) {
		t.Fatalf("staging options = %#v", stagingOptions)
	}
	if !reflect.DeepEqual(prompts, []string{"Site name"}) {
		t.Fatalf("prompts = %#v", prompts)
	}
	if confirmMessage != "Add site \"foobar\" with live and staging envs on target \"app1-linode\"?" {
		t.Fatalf("confirm message = %q", confirmMessage)
	}
	if sshUser != "nonfiction" || sshHost != "app1-linode.nonfiction.dev" || !strings.Contains(sshScript, "foobar-staging.app1-linode.nonfiction.dev") {
		t.Fatalf("ssh call = user %q host %q script contains staging=%v", sshUser, sshHost, strings.Contains(sshScript, "foobar-staging.app1-linode.nonfiction.dev"))
	}
	for _, want := range []string{"Add site plan:", "target: app1-linode", "site: foobar", "env staging:", "mode: execute", "Site added."} {
		if !strings.Contains(output, want) {
			t.Fatalf("site add output missing %q:\n%s", want, output)
		}
	}
	sites, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(sites) != 2 {
		t.Fatalf("sites len = %d, want 2: %#v", len(sites), sites)
	}
}

func TestRunSiteAddPromptsUntilSiteNameIsValidAndAvailable(t *testing.T) {
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
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "taken.app1-linode", "name": "taken", "env": "live", "target": "app1-linode"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	oldSelect := siteAddSelectFn
	oldPrompt := siteAddPromptStringFn
	oldInteractive := siteIsInteractiveFn
	oldRunSSH := runSSHScriptFn
	t.Cleanup(func() {
		siteAddSelectFn = oldSelect
		siteAddPromptStringFn = oldPrompt
		siteIsInteractiveFn = oldInteractive
		runSSHScriptFn = oldRunSSH
	})
	siteIsInteractiveFn = func() bool { return true }
	siteAddSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		switch title {
		case "Choose a target":
			return "app1-linode", nil
		case "Create a staging environment too":
			return "no", nil
		default:
			t.Fatalf("unexpected select title %q", title)
			return "", nil
		}
	}
	answers := []string{"Client", "taken", "fresh"}
	siteAddPromptStringFn = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		if prompt != "Site name" {
			t.Fatalf("unexpected prompt %q", prompt)
		}
		if len(answers) == 0 {
			t.Fatal("site prompt called too many times")
		}
		answer := answers[0]
		answers = answers[1:]
		return answer, nil
	}
	runSSHScriptFn = func(user, host, script string) error {
		t.Fatalf("runSSHScriptFn called during dry-run")
		return nil
	}

	var output string
	stderr := captureStderr(t, func() {
		output = captureStdout(t, func() {
			if got := Run([]string{"site", "add", "--dry-run"}); got != 0 {
				t.Fatalf("Run(site add --dry-run) = %d, want 0", got)
			}
		})
	})
	for _, want := range []string{"invalid site slug \"Client\"", "Site \"taken.app1-linode\" already exists in local site cache."} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr)
		}
	}
	for _, want := range []string{"site: fresh", "site id: fresh.app1-linode", "mode: dry-run"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	if len(answers) != 0 {
		t.Fatalf("unused prompt answers: %#v", answers)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sites.json")); err != nil {
		t.Fatalf("sites.json should still exist with fixture: %v", err)
	}
}

func TestRunSiteAddKinstaPromptsUntilKinstaSlugIsValidAndAvailable(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	t.Setenv("KINSTA_API_KEY", "kinsta-token")
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev", "default_wp_email": "web@nonfiction.ca", "default_wp_user": "admin", "dnsimple_account_id": "14", "kinsta_default_region": "ca-toronto-1"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "kinsta", "targets": []map[string]any{{"name": "kinsta", "provider": "kinsta", "company_id": "company-123", "status": "active"}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites":
			_ = json.NewEncoder(w).Encode(map[string]any{"company": map[string]any{"sites": []map[string]any{}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KINSTA_BASE_URL", server.URL)

	oldLookup := kinstaLookupHost
	oldProvision := kinstaProvisionSiteFn
	oldSelect := siteAddSelectFn
	oldPrompt := siteAddPromptStringFn
	oldInteractive := siteIsInteractiveFn
	t.Cleanup(func() {
		kinstaLookupHost = oldLookup
		kinstaProvisionSiteFn = oldProvision
		siteAddSelectFn = oldSelect
		siteAddPromptStringFn = oldPrompt
		siteIsInteractiveFn = oldInteractive
	})
	kinstaLookupHost = func(host string) ([]string, error) {
		switch host {
		case "nonfiction.kinsta.cloud":
			return []string{"203.0.113.10"}, nil
		case "nonfiction2.kinsta.cloud":
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		default:
			t.Fatalf("unexpected Kinsta lookup host %q", host)
			return nil, nil
		}
	}
	kinstaProvisionSiteFn = func(plan kinstaSiteAddPlan) (kinstaProvisionResult, error) {
		t.Fatalf("kinstaProvisionSiteFn called during dry-run")
		return kinstaProvisionResult{}, nil
	}
	siteIsInteractiveFn = func() bool { return true }
	siteAddSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		switch title {
		case "Choose a target":
			return "kinsta", nil
		case "Create a staging environment too":
			return "no", nil
		default:
			t.Fatalf("unexpected select title %q", title)
			return "", nil
		}
	}
	kinstaSlugAnswers := []string{"bad slug", "nonfiction2"}
	siteAddPromptStringFn = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		switch prompt {
		case "Site name":
			return "nonfiction", nil
		case "Kinsta slug":
			if defaultValue != "nonfiction" {
				t.Fatalf("Kinsta slug default = %q, want nonfiction", defaultValue)
			}
			if len(kinstaSlugAnswers) == 0 {
				t.Fatal("Kinsta slug prompt called too many times")
			}
			answer := kinstaSlugAnswers[0]
			kinstaSlugAnswers = kinstaSlugAnswers[1:]
			return answer, nil
		default:
			t.Fatalf("unexpected prompt %q", prompt)
			return "", nil
		}
	}

	var output string
	stderr := captureStderr(t, func() {
		output = captureStdout(t, func() {
			if got := Run([]string{"site", "add", "--dry-run"}); got != 0 {
				t.Fatalf("Run(site add --dry-run) = %d, want 0", got)
			}
		})
	})
	for _, want := range []string{"Kinsta slug \"nonfiction\" appears unavailable", "invalid Kinsta slug \"bad slug\""} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr)
		}
	}
	for _, want := range []string{"site: nonfiction", "kinsta slug: nonfiction2", "site id: nonfiction.kinsta", "mode: dry-run"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	if len(kinstaSlugAnswers) != 0 {
		t.Fatalf("unused Kinsta slug prompt answers: %#v", kinstaSlugAnswers)
	}
}

func TestRunSiteAddNonInteractiveRequiresTargetAndSite(t *testing.T) {
	oldSelect := siteAddSelectFn
	oldPrompt := siteAddPromptStringFn
	t.Cleanup(func() {
		siteAddSelectFn = oldSelect
		siteAddPromptStringFn = oldPrompt
	})
	siteAddSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		t.Fatalf("siteAddSelectFn called in non-interactive mode")
		return "", nil
	}
	siteAddPromptStringFn = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		t.Fatalf("siteAddPromptStringFn called in non-interactive mode")
		return "", nil
	}

	stderr := captureStderr(t, func() {
		if got := Run([]string{"site", "add", "--non-interactive"}); got != 1 {
			t.Fatalf("Run(site add --non-interactive) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "site add requires target and site in non-interactive mode") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestBuildSiteAddPlanUsesMatchingProjectPasswordVersion(t *testing.T) {
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
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "foobar", "password_version": 5}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}}}
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

	plan, err := buildSiteAddPlan(siteAddArgs{target: "app1-linode", site: "foobar", withStaging: true})
	if err != nil {
		t.Fatalf("buildSiteAddPlan() error = %v", err)
	}
	if got, want := plan.PasswordVersion, "5"; got != want {
		t.Fatalf("PasswordVersion = %q, want %q", got, want)
	}
	if got, want := plan.AdminPassword, passwords.DerivePassword("foobar:v5", "wp-admin", "test-salt"); got != want {
		t.Fatalf("AdminPassword = %q, want %q", got, want)
	}
	if got, want := plan.DBPassword, passwords.DerivePassword("foobar:v5", "mysql", "test-salt"); got != want {
		t.Fatalf("DBPassword = %q, want %q", got, want)
	}
	records := siteAddRecords(plan)
	if len(records) != 2 {
		t.Fatalf("siteAddRecords len = %d, want 2", len(records))
	}
	for _, record := range records {
		if _, ok := record["password_version"]; ok {
			t.Fatalf("siteAddRecords wrote password_version into provider state: %#v", record)
		}
	}
}

func TestBuildSiteAddPlanUsesExplicitPasswordVersion(t *testing.T) {
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

	plan, err := buildSiteAddPlan(siteAddArgs{target: "app1-linode", site: "foobar", passwordVersion: "7"})
	if err != nil {
		t.Fatalf("buildSiteAddPlan() error = %v", err)
	}
	if got, want := plan.PasswordVersion, "7"; got != want {
		t.Fatalf("PasswordVersion = %q, want %q", got, want)
	}
	if got, want := plan.AdminPassword, passwords.DerivePassword("foobar:v7", "wp-admin", "test-salt"); got != want {
		t.Fatalf("AdminPassword = %q, want %q", got, want)
	}
	if got, want := plan.DBPassword, passwords.DerivePassword("foobar:v7", "mysql", "test-salt"); got != want {
		t.Fatalf("DBPassword = %q, want %q", got, want)
	}
	if records := siteAddRecords(plan); len(records) != 1 {
		t.Fatalf("siteAddRecords len = %d, want 1", len(records))
	} else if _, ok := records[0]["password_version"]; ok {
		t.Fatalf("siteAddRecords wrote password_version into provider state: %#v", records[0])
	}
}

func TestBuildSiteAddPlanExplicitPasswordVersionOverridesProject(t *testing.T) {
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
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "foobar", "password_version": 5}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}}}
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

	plan, err := buildSiteAddPlan(siteAddArgs{target: "app1-linode", site: "foobar", passwordVersion: "2"})
	if err != nil {
		t.Fatalf("buildSiteAddPlan() error = %v", err)
	}
	if got, want := plan.PasswordVersion, "2"; got != want {
		t.Fatalf("PasswordVersion = %q, want %q", got, want)
	}
	if got, want := plan.AdminPassword, passwords.DerivePassword("foobar:v2", "wp-admin", "test-salt"); got != want {
		t.Fatalf("AdminPassword = %q, want %q", got, want)
	}

	plan, err = buildSiteAddPlan(siteAddArgs{target: "app1-linode", site: "foobar", passwordVersion: "0", passwordVersionSet: true})
	if err != nil {
		t.Fatalf("buildSiteAddPlan(version 0) error = %v", err)
	}
	if got, want := plan.PasswordVersion, ""; got != want {
		t.Fatalf("PasswordVersion = %q, want %q", got, want)
	}
	if got, want := plan.AdminPassword, passwords.DerivePassword("foobar", "wp-admin", "test-salt"); got != want {
		t.Fatalf("AdminPassword = %q, want %q", got, want)
	}
}

func TestValidateSiteAddSlug(t *testing.T) {
	for _, input := range []string{"client", "nonfiction", "site001", "a", "a123"} {
		if err := validateSiteAddSlug(input); err != nil {
			t.Fatalf("validateSiteAddSlug(%q) error = %v, want nil", input, err)
		}
	}

	invalid := []string{"Client", "client-name", "client_name", "1client", "client.com", "client name", "", "a" + strings.Repeat("1", 32)}
	for _, input := range invalid {
		err := validateSiteAddSlug(input)
		if err == nil {
			t.Fatalf("validateSiteAddSlug(%q) = nil, want error", input)
		}
		message := err.Error()
		for _, want := range []string{"invalid site slug", "must start with a lowercase ASCII letter", "only lowercase ASCII letters and digits", "1-32 characters", "Valid examples: client, nonfiction, site001", "Invalid examples: client-name, client_name, 1client, Client, client.com"} {
			if !strings.Contains(message, want) {
				t.Fatalf("validateSiteAddSlug(%q) error missing %q:\n%s", input, want, message)
			}
		}
	}
}

func TestValidateKinstaSlug(t *testing.T) {
	for _, input := range []string{"abcde", "client", "client-1", "a1234", strings.Repeat("a", 32)} {
		if err := validateKinstaSlug(input); err != nil {
			t.Fatalf("validateKinstaSlug(%q) error = %v, want nil", input, err)
		}
	}

	invalid := []string{"abcd", "Client", "client_name", "1client", "client.com", "client name", "abcde-", "", "a" + strings.Repeat("1", 32)}
	for _, input := range invalid {
		err := validateKinstaSlug(input)
		if err == nil {
			t.Fatalf("validateKinstaSlug(%q) = nil, want error", input)
		}
		message := err.Error()
		for _, want := range []string{"invalid Kinsta slug", "must start with a lowercase ASCII letter", "end with a lowercase ASCII letter or digit", "5-32 characters"} {
			if !strings.Contains(message, want) {
				t.Fatalf("validateKinstaSlug(%q) error missing %q:\n%s", input, want, message)
			}
		}
	}
}

func TestRunSiteAddRejectsInvalidSlugBeforeLookup(t *testing.T) {
	stderr := captureStderr(t, func() {
		if got := runSiteAdd([]string{"missing-target", "Client", "--execute", "--yes", "--non-interactive"}); got != 1 {
			t.Fatalf("runSiteAdd(invalid slug) = %d, want 1", got)
		}
	})
	for _, want := range []string{"invalid site slug \"Client\"", "Valid examples: client, nonfiction, site001", "Invalid examples: client-name, client_name, 1client, Client, client.com"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("runSiteAdd stderr missing %q:\n%s", want, stderr)
		}
	}
	for _, notWant := range []string{"No target matched", "Expected base_domain", "Expected default_wp_email"} {
		if strings.Contains(stderr, notWant) {
			t.Fatalf("runSiteAdd stderr contains %q, so validation did not stop early:\n%s", notWant, stderr)
		}
	}
}

func TestRunSiteAddKinstaRejectsShortProviderSlugBeforeConfigLookup(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "kinsta", "targets": []map[string]any{{"name": "kinsta", "provider": "kinsta", "company_id": "company-123", "status": "active"}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}

	stderr := captureStderr(t, func() {
		if got := runSiteAdd([]string{"kinsta", "abcd", "--dry-run", "--non-interactive"}); got != 1 {
			t.Fatalf("runSiteAdd(short Kinsta slug) = %d, want 1", got)
		}
	})
	for _, want := range []string{"invalid Kinsta slug \"abcd\"", "5-32 characters"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("runSiteAdd stderr missing %q:\n%s", want, stderr)
		}
	}
	for _, notWant := range []string{"Expected base_domain", "Expected default_wp_email", "Expected KINSTA_API_KEY"} {
		if strings.Contains(stderr, notWant) {
			t.Fatalf("runSiteAdd stderr contains %q, so Kinsta slug validation did not stop early:\n%s", notWant, stderr)
		}
	}
}

func TestRunSiteAddRejectsInvalidPasswordVersionBeforeLookup(t *testing.T) {
	for _, argv := range [][]string{
		{"missing-target", "foobar", "--password-version"},
		{"missing-target", "foobar", "--password-version="},
		{"missing-target", "foobar", "--password-version", "-1"},
		{"missing-target", "foobar", "--password-version=1.2"},
		{"missing-target", "foobar", "--password-version", "abc"},
	} {
		stderr := captureStderr(t, func() {
			if got := runSiteAdd(argv); got != 1 {
				t.Fatalf("runSiteAdd(%v) = %d, want 1", argv, got)
			}
		})
		if !strings.Contains(stderr, "--password-version requires a value") && !strings.Contains(stderr, "must be an unsigned integer") {
			t.Fatalf("runSiteAdd(%v) stderr missing password-version error:\n%s", argv, stderr)
		}
		for _, notWant := range []string{"No target matched", "Expected base_domain", "Expected default_wp_email"} {
			if strings.Contains(stderr, notWant) {
				t.Fatalf("runSiteAdd(%v) stderr contains %q, so validation did not stop early:\n%s", argv, notWant, stderr)
			}
		}
	}
}

func TestRunSiteAddPasswordVersionFlagNormalizesZeroPaddedValue(t *testing.T) {
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
		if got := Run([]string{"site", "add", "app1-linode", "foobar", "--password-version=002", "--dry-run", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site add) = %d, want 0", got)
		}
	})
	for _, want := range []string{"password version: 2", "admin password: derived from foobar", "mode: dry-run"} {
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
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "php": map[string]any{"version": "8.3", "service": "php8.3-fpm"}, "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
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
		if got := Run([]string{"site", "add", "app1-linode", "foobar", "--with-staging", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site add execute) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Site added.") || !strings.Contains(output, "mode: execute") {
		t.Fatalf("site add execute output = %q, want success", output)
	}
	if !strings.Contains(output, "php: 8.3") {
		t.Fatalf("site add execute output missing PHP version:\n%s", output)
	}
	if strings.Contains(output, "map[") {
		t.Fatalf("site add execute output contains raw PHP map:\n%s", output)
	}
	if sshUser != "nonfiction" || sshHost != "app1-linode.nonfiction.dev" {
		t.Fatalf("ssh target = %s@%s, want nonfiction@app1-linode.nonfiction.dev", sshUser, sshHost)
	}
	for _, want := range []string{"/var/www/sites/foobar/public", "/var/www/sites/foobar_staging/public", "CREATE DATABASE IF NOT EXISTS", "wp core install", "foobar.app1-linode.nonfiction.dev", "foobar-staging.app1-linode.nonfiction.dev", "/var/lib/nf/sites.json", "/etc/nginx/conf.d/nf-server-names-hash.conf", "server_names_hash_bucket_size 128;", "server_names_hash_max_size 4096;", ".db.user // .db.database.user // .adminer.user // .adminer.database.user", "if [ -n \"$db_access_user\" ] && [ \"$db_access_user\" = \"$db_name\" ]; then", "SELECT COUNT(*) FROM mysql.user WHERE User='$db_access_user' AND Host='localhost';", "GRANT ALL PRIVILEGES ON \\`$db_name\\`.* TO '$db_access_user'@'localhost';"} {
		if !strings.Contains(sshScript, want) {
			t.Fatalf("ssh script missing %q:\n%s", want, sshScript)
		}
	}
	if strings.Index(sshScript, "if [ -n \"$db_access_user\" ] && [ \"$db_access_user\" = \"$db_name\" ]; then") > strings.Index(sshScript, "CREATE USER IF NOT EXISTS '$db_name'@'localhost'") {
		t.Fatalf("database access user collision guard must run before creating the site DB user:\n%s", sshScript)
	}
	if strings.Contains(sshScript, "password_version") {
		t.Fatalf("ssh script wrote password_version into target state:\n%s", sshScript)
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
		if _, ok := record["password_version"]; ok {
			t.Fatalf("%s cached record wrote password_version: %#v", want.env, record)
		}
		if got := sitePHPVersion(record); got != "8.3" {
			t.Fatalf("%s php version = %q, want 8.3 in %#v", want.env, got, record)
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
	for _, want := range []string{"nf-site-$file_slug", "$file_slug.access.log", "$file_slug.error.log", "nf_linode_write_cache_snippets", "cache_zone=$(nf_linode_ensure_cache_config \"$site_path\")", "wp-content/mu-plugins/nf-linode-cache.php", "include /etc/nginx/snippets/nf-fastcgi-cache-bypass.conf;", "fastcgi_cache $cache_zone;", "include /etc/nginx/snippets/nf-fastcgi-cache.conf;"} {
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
	siteRecords, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	for _, record := range siteRecords {
		record["php"] = map[string]any{"version": "8.3", "service": "php8.3-fpm"}
		if recordValueString(record["env"]) == "live" {
			record["domains"] = []map[string]any{{"name": "www.foobar.com", "role": "primary", "management": "external", "status": "active"}, {"name": "foobar.com", "role": "secondary", "management": "external", "status": "active"}}
			record["primary_domain"] = "www.foobar.com"
		}
	}
	if err := state.SaveStateRecords("sites", siteRecords); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	showOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "show", "foobar.app1-linode"}); got != 0 {
			t.Fatalf("Run(site show) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, showOutput, []string{
		"foobar.app1-linode\n",
		"──────────────────\n",
		"Name       foobar\n",
		"Provider   linode\n",
		"Target     app1-linode\n",
		"Environments\n",
		"env      php  url",
		"live     8.3  https://foobar.app1-linode.nonfiction.dev          www.foobar.com, foobar.com\n",
		"staging  8.3  https://foobar-staging.app1-linode.nonfiction.dev",
	})
	for _, notWant := range []string{"Site       foobar.app1-linode", "Environments:", "map["} {
		if strings.Contains(showOutput, notWant) {
			t.Fatalf("site show output contains %q:\n%s", notWant, showOutput)
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

func TestRunSiteAddKinstaDryRunPlansLiveOnlyByDefault(t *testing.T) {
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
	for _, want := range []string{"Add site plan:", "target: kinsta", "provider: kinsta", "company id: company-123", "site: foobar", "site id: foobar.kinsta", "region: us-central1", "php: 8.3", "admin email: web@nonfiction.ca", "admin password: derived from foobar", "env live:", "domain: foobar.kinsta.nonfiction.dev", "dns: dnsimple zone nonfiction.dev account 14", "mode: dry-run"} {
		if !strings.Contains(output, want) {
			t.Fatalf("site add kinsta dry-run output missing %q:\n%s", want, output)
		}
	}
	for _, notWant := range []string{"env staging:", "foobar-staging.kinsta.nonfiction.dev"} {
		if strings.Contains(output, notWant) {
			t.Fatalf("site add kinsta dry-run output contains %q:\n%s", notWant, output)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sites.json")); !os.IsNotExist(err) {
		t.Fatalf("sites.json unexpectedly exists after dry-run: %v", err)
	}
}

func TestBuildKinstaSiteAddPlanUsesConfiguredBaseDomain(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := saveGlobalConfig(map[string]string{"base_domain": "example.test", "default_wp_email": "web@example.test", "dnsimple_account_id": "14"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "kinsta", "targets": []map[string]any{{"name": "kinsta", "provider": "kinsta", "company_id": "company-123", "status": "active"}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}

	plan, err := buildKinstaSiteAddPlan(siteAddArgs{target: "kinsta", site: "client", withStaging: true})
	if err != nil {
		t.Fatalf("buildKinstaSiteAddPlan() error = %v", err)
	}
	if len(plan.Envs) != 2 {
		t.Fatalf("plan.Envs len = %d, want 2", len(plan.Envs))
	}
	wants := map[string]string{"live": "client.kinsta.example.test", "staging": "client-staging.kinsta.example.test"}
	for _, env := range plan.Envs {
		if got := env.Domain; got != wants[env.Env] {
			t.Fatalf("%s domain = %q, want %q", env.Env, got, wants[env.Env])
		}
		if got := env.URL; got != "https://"+wants[env.Env] {
			t.Fatalf("%s URL = %q, want %q", env.Env, got, "https://"+wants[env.Env])
		}
	}
}

func TestBuildKinstaSiteAddPlanAllowsShortProjectSlugWithExplicitProviderSlug(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev", "default_wp_email": "web@nonfiction.ca", "dnsimple_account_id": "14"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "kinsta", "targets": []map[string]any{{"name": "kinsta", "provider": "kinsta", "company_id": "company-123", "status": "active"}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}

	plan, err := buildKinstaSiteAddPlan(siteAddArgs{target: "kinsta", site: "abcd", kinstaSlug: "abcde"})
	if err != nil {
		t.Fatalf("buildKinstaSiteAddPlan() error = %v", err)
	}
	if plan.Site != "abcd" || plan.SiteID != "abcd.kinsta" || plan.KinstaSlug != "abcde" {
		t.Fatalf("plan = %#v, want short project slug with explicit Kinsta slug", plan)
	}
	if len(plan.Envs) != 1 || plan.Envs[0].Domain != "abcd.kinsta.nonfiction.dev" {
		t.Fatalf("plan.Envs = %#v, want project slug domain", plan.Envs)
	}
}

func TestBuildKinstaSiteAddPlanUsesExplicitPasswordVersion(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev", "default_wp_email": "web@nonfiction.ca", "dnsimple_account_id": "14"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "kinsta", "targets": []map[string]any{{"name": "kinsta", "provider": "kinsta", "company_id": "company-123", "status": "active"}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}

	plan, err := buildKinstaSiteAddPlan(siteAddArgs{target: "kinsta", site: "client", passwordVersion: "3"})
	if err != nil {
		t.Fatalf("buildKinstaSiteAddPlan() error = %v", err)
	}
	if got, want := plan.PasswordVersion, "3"; got != want {
		t.Fatalf("PasswordVersion = %q, want %q", got, want)
	}
	if got, want := plan.AdminPassword, passwords.DerivePassword("client:v3", "wp-admin", "test-salt"); got != want {
		t.Fatalf("AdminPassword = %q, want %q", got, want)
	}
}

func TestEnsureKinstaSiteRejectsResolvableKinstaCloudSlugBeforeCreate(t *testing.T) {
	createCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites":
			_ = json.NewEncoder(w).Encode(map[string]any{"company": map[string]any{"sites": []map[string]any{}}})
		case "POST /sites":
			createCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-create-site", "status": 202})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	lookupCalls := 0
	oldLookup := kinstaLookupHost
	kinstaLookupHost = func(host string) ([]string, error) {
		lookupCalls++
		if host != "nonfiction.kinsta.cloud" {
			t.Fatalf("lookup host = %q, want nonfiction.kinsta.cloud", host)
		}
		return []string{"203.0.113.10"}, nil
	}
	t.Cleanup(func() { kinstaLookupHost = oldLookup })

	client := kinsta.NewClient(server.URL, "kinsta-token")
	_, err := ensureKinstaSite(context.Background(), client, kinstaSiteAddPlan{Site: "nonfiction", Region: "ca-toronto-1"}, "company-123")
	if err == nil {
		t.Fatal("ensureKinstaSite() error = nil, want unavailable slug error")
	}
	for _, want := range []string{"Kinsta slug \"nonfiction\" appears unavailable", "no matching Kinsta site was found in this account", "nonfiction.kinsta.cloud resolves", "Choose another slug"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ensureKinstaSite() error missing %q:\n%s", want, err)
		}
	}
	if lookupCalls != 1 || createCalls != 0 {
		t.Fatalf("lookupCalls=%d createCalls=%d, want 1 and 0", lookupCalls, createCalls)
	}
}

func TestEnsureKinstaSiteAllowsNXDOMAINKinstaCloudSlugBeforeCreate(t *testing.T) {
	siteListCalls := 0
	createCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites":
			siteListCalls++
			sites := []map[string]any{}
			if siteListCalls > 1 {
				sites = []map[string]any{{"id": "ksite123", "name": "client", "display_name": "client"}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"company": map[string]any{"sites": sites}})
		case "POST /sites":
			createCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-create-site", "status": 202})
		case "GET /operations/op-create-site":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Successfully finished request"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	lookupCalls := 0
	oldLookup := kinstaLookupHost
	kinstaLookupHost = func(host string) ([]string, error) {
		lookupCalls++
		if host != "client.kinsta.cloud" {
			t.Fatalf("lookup host = %q, want client.kinsta.cloud", host)
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	t.Cleanup(func() { kinstaLookupHost = oldLookup })

	client := kinsta.NewClient(server.URL, "kinsta-token")
	site, err := ensureKinstaSite(context.Background(), client, kinstaSiteAddPlan{Site: "client", Region: "ca-toronto-1"}, "company-123")
	if err != nil {
		t.Fatalf("ensureKinstaSite() error = %v", err)
	}
	if site.ID != "ksite123" || site.Name != "client" {
		t.Fatalf("ensureKinstaSite() = %#v, want ksite123 client", site)
	}
	if lookupCalls != 1 || createCalls != 1 || siteListCalls != 2 {
		t.Fatalf("lookupCalls=%d createCalls=%d siteListCalls=%d, want 1, 1, 2", lookupCalls, createCalls, siteListCalls)
	}
}

func TestEnsureKinstaSiteSkipsKinstaCloudPreflightForExistingExactSlug(t *testing.T) {
	createCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites":
			_ = json.NewEncoder(w).Encode(map[string]any{"company": map[string]any{"sites": []map[string]any{{"id": "ksite123", "name": "client", "display_name": "client"}}}})
		case "POST /sites":
			createCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-create-site", "status": 202})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	lookupCalls := 0
	oldLookup := kinstaLookupHost
	kinstaLookupHost = func(host string) ([]string, error) {
		lookupCalls++
		return []string{"203.0.113.10"}, nil
	}
	t.Cleanup(func() { kinstaLookupHost = oldLookup })

	client := kinsta.NewClient(server.URL, "kinsta-token")
	site, err := ensureKinstaSite(context.Background(), client, kinstaSiteAddPlan{Site: "client", Region: "ca-toronto-1"}, "company-123")
	if err != nil {
		t.Fatalf("ensureKinstaSite() error = %v", err)
	}
	if site.ID != "ksite123" || site.Name != "client" {
		t.Fatalf("ensureKinstaSite() = %#v, want ksite123 client", site)
	}
	if lookupCalls != 0 || createCalls != 0 {
		t.Fatalf("lookupCalls=%d createCalls=%d, want 0 and 0", lookupCalls, createCalls)
	}
}

func TestEnsureKinstaSiteRejectsAssignedSlugMismatch(t *testing.T) {
	siteListCalls := 0
	createCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites":
			siteListCalls++
			sites := []map[string]any{}
			if siteListCalls > 1 {
				sites = []map[string]any{{"id": "ksite123", "name": "sanjelv", "display_name": "sanjel"}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"company": map[string]any{"sites": sites}})
		case "POST /sites":
			createCalls++
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("create site decode error = %v", err)
			}
			if payload["display_name"] != "sanjel" {
				t.Fatalf("create site display_name = %#v, want sanjel", payload["display_name"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-create-site", "status": 202})
		case "GET /operations/op-create-site":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Successfully finished request"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	oldLookup := kinstaLookupHost
	kinstaLookupHost = func(host string) ([]string, error) {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	t.Cleanup(func() { kinstaLookupHost = oldLookup })

	client := kinsta.NewClient(server.URL, "kinsta-token")
	_, err := ensureKinstaSite(context.Background(), client, kinstaSiteAddPlan{Site: "sanjel", Region: "ca-toronto-1"}, "company-123")
	if err == nil {
		t.Fatal("ensureKinstaSite() error = nil, want slug mismatch error")
	}
	for _, want := range []string{"Kinsta returned site slug \"sanjelv\" instead of requested \"sanjel\"", "ksite123", "cannot safely cache", "Delete the mismatched Kinsta site"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ensureKinstaSite() error missing %q:\n%s", want, err)
		}
	}
	if siteListCalls != 2 || createCalls != 1 {
		t.Fatalf("siteListCalls=%d createCalls=%d, want 2 and 1", siteListCalls, createCalls)
	}
}

func TestEnsureKinstaSiteRejectsExistingDisplayNameSlugMismatch(t *testing.T) {
	createCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites":
			_ = json.NewEncoder(w).Encode(map[string]any{"company": map[string]any{"sites": []map[string]any{{"id": "ksite123", "name": "sanjelv", "display_name": "sanjel"}}}})
		case "POST /sites":
			createCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-create-site", "status": 202})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	client := kinsta.NewClient(server.URL, "kinsta-token")
	_, err := ensureKinstaSite(context.Background(), client, kinstaSiteAddPlan{Site: "sanjel", Region: "ca-toronto-1"}, "company-123")
	if err == nil {
		t.Fatal("ensureKinstaSite() error = nil, want slug mismatch error")
	}
	if !strings.Contains(err.Error(), "Kinsta returned site slug \"sanjelv\" instead of requested \"sanjel\"") {
		t.Fatalf("ensureKinstaSite() error = %v", err)
	}
	if createCalls != 0 {
		t.Fatalf("createCalls = %d, want 0", createCalls)
	}
}

func TestEnsureKinstaSiteUsesProviderSlugWhenItDiffersFromProjectSlug(t *testing.T) {
	createCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites":
			_ = json.NewEncoder(w).Encode(map[string]any{"company": map[string]any{"sites": []map[string]any{{"id": "ksite123", "name": "sanjelv", "display_name": "sanjelv"}}}})
		case "POST /sites":
			createCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-create-site", "status": 202})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	client := kinsta.NewClient(server.URL, "kinsta-token")
	site, err := ensureKinstaSite(context.Background(), client, kinstaSiteAddPlan{Site: "sanjel", KinstaSlug: "sanjelv", Region: "ca-toronto-1"}, "company-123")
	if err != nil {
		t.Fatalf("ensureKinstaSite() error = %v", err)
	}
	if site.ID != "ksite123" || site.Name != "sanjelv" {
		t.Fatalf("ensureKinstaSite() = %#v, want ksite123 sanjelv", site)
	}
	if createCalls != 0 {
		t.Fatalf("createCalls = %d, want 0", createCalls)
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
		if got := Run([]string{"site", "add", "kinsta", "foobar", "--kinsta-slug", "foobarinc", "--with-staging", "--region", "ca-toronto-1", "--php", "8.2", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site add kinsta execute) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Site added.") || !strings.Contains(output, "mode: execute") {
		t.Fatalf("site add kinsta execute output = %q, want success", output)
	}
	if capturedPlan.Region != "ca-toronto-1" || capturedPlan.SiteID != "foobar.kinsta" || capturedPlan.KinstaSlug != "foobarinc" || capturedPlan.PHPVersion != "8.2" {
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
		if got := recordValueString(record["project_slug"]); got != "foobar" {
			t.Fatalf("%s project_slug = %q, want foobar", want.env, got)
		}
		if got := recordValueString(record["env_id"]); got != "foobar.kinsta:"+want.env {
			t.Fatalf("%s env_id = %q, want foobar.kinsta:%s", want.env, got, want.env)
		}
		if got := recordValueString(record["target"]); got != "kinsta" {
			t.Fatalf("%s target = %q, want kinsta", want.env, got)
		}
		if _, ok := record["password_version"]; ok {
			t.Fatalf("%s cached record wrote password_version: %#v", want.env, record)
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
		if got := mapStringAtPath(record, "kinsta", "slug"); got != "foobarinc" {
			t.Fatalf("%s kinsta slug = %q, want foobarinc", want.env, got)
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
	assertContainsInOrder(t, showOutput, []string{
		"foobar.kinsta\n",
		"─────────────\n",
		"Name       foobar\n",
		"Provider   kinsta\n",
		"Target     kinsta\n",
		"Environments\n",
		"env      php  url",
		"live     8.2  https://foobar.kinsta.nonfiction.dev\n",
		"staging  8.2  https://foobar-staging.kinsta.nonfiction.dev",
	})
	for _, notWant := range []string{"Site       foobar.kinsta", "Environments:", "path", "database", "ssh", "/www/foobar/public", "foobar@203.0.113.10:12345", "/www/foobarstaging/public", "foobarstaging@203.0.113.11:12346"} {
		if strings.Contains(showOutput, notWant) {
			t.Fatalf("site show kinsta output contains %q:\n%s", notWant, showOutput)
		}
	}
}

func TestRunSiteAddKinstaExecuteResumesCachedSite(t *testing.T) {
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
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "kinsta", "site_id": "foobar.kinsta", "name": "foobar", "env": "live", "target": "kinsta", "hostname": "foobar.kinsta.nonfiction.dev", "kinsta": map[string]any{"site_id": "old-site", "environment_id": "old-live", "domain_id": "old-domain"}},
		{"provider": "kinsta", "site_id": "foobar.kinsta", "name": "foobar", "env": "staging", "target": "kinsta", "hostname": "foobar-staging.kinsta.nonfiction.dev", "kinsta": map[string]any{"site_id": "old-site", "environment_id": "old-staging", "domain_id": "old-domain-staging"}},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	provisionCalls := 0
	oldProvision := kinstaProvisionSiteFn
	kinstaProvisionSiteFn = func(plan kinstaSiteAddPlan) (kinstaProvisionResult, error) {
		provisionCalls++
		if plan.SiteID != "foobar.kinsta" || len(plan.Envs) != 2 {
			t.Fatalf("resume plan = %#v, want foobar.kinsta with live+staging", plan)
		}
		return kinstaProvisionResult{SiteID: "new-site", Envs: []kinstaSiteAddEnvPlan{
			{Env: "live", Domain: "foobar.kinsta.nonfiction.dev", URL: "https://foobar.kinsta.nonfiction.dev", Branch: "main", EnvID: "new-live", DomainID: "new-domain", Path: "/www/foobar/public", Database: "foobar", SSHHost: "203.0.113.10", SSHPort: "12345", SSHUser: "foobar", SSHCmd: "ssh foobar@203.0.113.10 -p 12345"},
			{Env: "staging", Domain: "foobar-staging.kinsta.nonfiction.dev", URL: "https://foobar-staging.kinsta.nonfiction.dev", Branch: "develop", EnvID: "new-staging", DomainID: "new-domain-staging", Path: "/www/foobarstaging/public", Database: "foobarstaging", SSHHost: "203.0.113.11", SSHPort: "12346", SSHUser: "foobarstaging", SSHCmd: "ssh foobarstaging@203.0.113.11 -p 12346"},
		}}, nil
	}
	t.Cleanup(func() { kinstaProvisionSiteFn = oldProvision })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "add", "kinsta", "foobar", "--with-staging", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site add kinsta resume) = %d, want 0", got)
		}
	})
	if provisionCalls != 1 {
		t.Fatalf("provision calls = %d, want 1", provisionCalls)
	}
	for _, want := range []string{"Resume Kinsta site add plan:", "Site added."} {
		if !strings.Contains(output, want) {
			t.Fatalf("resume output missing %q:\n%s", want, output)
		}
	}
	sites, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(sites) != 2 {
		t.Fatalf("sites len = %d, want 2: %#v", len(sites), sites)
	}
	for _, record := range sites {
		if got := mapStringAtPath(record, "kinsta", "site_id"); got != "new-site" {
			t.Fatalf("kinsta site_id = %q, want new-site in %#v", got, sites)
		}
	}
}

func TestProvisionKinstaSiteAddWaitsForPointingRecordsBeforePrimary(t *testing.T) {
	oldInterval := kinstaDomainRecordsWaitInterval
	kinstaDomainRecordsWaitInterval = time.Millisecond
	t.Cleanup(func() { kinstaDomainRecordsWaitInterval = oldInterval })

	domainListCalls := 0
	domainRecordsCalls := 0
	validationCalls := 0
	confirmCalls := 0
	primaryCalls := 0
	primaryBeforePointing := false
	confirmBeforeValidation := false
	var addDomainPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/environments/kenv-live/domains":
			domainListCalls++
			domains := []map[string]any{}
			if domainListCalls > 1 {
				domains = []map[string]any{{"id": "kdom-live", "name": "foobar.kinsta.nonfiction.dev", "is_primary": false}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": domains}})
		case "POST /sites/environments/kenv-live/domains":
			if err := json.NewDecoder(r.Body).Decode(&addDomainPayload); err != nil {
				t.Fatalf("add domain decode error = %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-add-domain", "status": 202})
		case "GET /operations/op-add-domain":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Successfully finished request"})
		case "GET /sites/environments/domains/kdom-live/verification-records":
			domainRecordsCalls++
			siteDomain := map[string]any{"verification_records": []map[string]any{{"name": "k-verification.foobar.kinsta.nonfiction.dev", "type": "TXT", "content": "verify-token"}}}
			if domainRecordsCalls > 1 {
				siteDomain["verification_records"] = []map[string]any{{"name": "_cf-custom-hostname.foobar.kinsta.nonfiction.dev", "type": "TXT", "content": "cf-token"}, {"name": "_acme-challenge.foobar.kinsta.nonfiction.dev", "type": "CNAME", "content": "foobar.kinsta.nonfiction.dev.kinstavalidation.app"}}
			}
			if domainRecordsCalls > 3 {
				siteDomain["pointing_records"] = []map[string]any{{"name": "foobar.kinsta.nonfiction.dev", "type": "A", "content": "203.0.113.10", "ttl": 300}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": siteDomain})
		case "POST /":
			var payload struct {
				OperationName string         `json:"operationName"`
				Variables     map[string]any `json:"variables"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("graphql decode error = %v", err)
			}
			switch payload.OperationName {
			case "ValidateVerificationRecordsOfSiteDomains":
				validationCalls++
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"validateVerificationRecordsOfSiteDomains": []map[string]any{{"idSiteDomain": "kdom-live", "isValid": true, "records": []map[string]any{{"name": "k-verification.foobar.kinsta.nonfiction.dev", "value": "verify-token", "type": "TXT", "isDetected": true}}}}}})
			case "ConfirmCloudflareVerification":
				confirmCalls++
				if validationCalls == 0 {
					confirmBeforeValidation = true
				}
				if payload.Variables["idEnvironment"] != "kenv-live" || payload.Variables["idSiteDomain"] != "kdom-live" || payload.Variables["isConfirmed"] != true {
					t.Fatalf("confirm variables = %#v", payload.Variables)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"idAction": 123}})
			case "Action":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"action": map[string]any{"id": 123, "error": nil, "isDone": false}, "action_liveKeys": []string{"key"}}})
			default:
				t.Fatalf("unexpected graphql operation %q", payload.OperationName)
			}
		case "PUT /sites/environments/kenv-live/change-primary-domain":
			primaryCalls++
			if domainRecordsCalls < 4 {
				primaryBeforePointing = true
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-primary-domain", "status": 202})
		case "GET /operations/op-primary-domain":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Successfully finished request"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	oldUpsert := upsertDNSRecordFn
	upserts := []string{}
	upsertDNSRecordFn = func(token, accountID, zone, name, recordType, content string, ttl int) error {
		upserts = append(upserts, strings.Join([]string{token, accountID, zone, name, recordType, content}, " "))
		return nil
	}
	t.Cleanup(func() { upsertDNSRecordFn = oldUpsert })
	stubDNSTypedDeletes(t)

	client := kinsta.NewClient(server.URL, "kinsta-token", kinsta.WithGraphQLURL(server.URL))
	plan := kinstaSiteAddPlan{
		Site:         "foobar",
		SiteID:       "foobar.kinsta",
		TargetName:   "kinsta",
		PHPVersion:   "8.3",
		DNSZone:      "nonfiction.dev",
		DNSAccountID: "14",
		Envs: []kinstaSiteAddEnvPlan{{
			Env:    "live",
			Domain: "foobar.kinsta.nonfiction.dev",
			URL:    "https://foobar.kinsta.nonfiction.dev",
		}},
	}

	result, err := provisionKinstaSelectedEnvs(context.Background(), client, "dns-token", plan, "company-123", "ksite123", kinsta.Environment{ID: "kenv-live", Name: "live", PrimaryDomain: kinsta.Domain{Name: "foobar.kinsta.cloud"}})
	if err != nil {
		t.Fatalf("provisionKinstaSelectedEnvs() error = %v", err)
	}
	if len(result.Envs) != 1 || result.Envs[0].DomainID != "kdom-live" {
		t.Fatalf("result envs = %#v, want kdom-live", result.Envs)
	}
	if primaryCalls != 1 {
		t.Fatalf("change primary calls = %d, want 1", primaryCalls)
	}
	if validationCalls != 1 || confirmCalls != 1 || confirmBeforeValidation {
		t.Fatalf("verification calls validation=%d confirm=%d confirmBeforeValidation=%v, want validate then confirm", validationCalls, confirmCalls, confirmBeforeValidation)
	}
	if primaryBeforePointing {
		t.Fatal("change primary was called before pointing records were returned")
	}
	if addDomainPayload["setup_type"] != "avoid_downtime" || addDomainPayload["is_wildcardless"] != true || addDomainPayload["domain_name"] != "foobar.kinsta.nonfiction.dev" {
		t.Fatalf("add domain payload = %#v, want wildcardless avoid_downtime domain", addDomainPayload)
	}
	for _, want := range []string{
		"k-verification.foobar.kinsta TXT verify-token",
		"_cf-custom-hostname.foobar.kinsta TXT cf-token",
		"_acme-challenge.foobar.kinsta CNAME foobar.kinsta.nonfiction.dev.kinstavalidation.app",
		"foobar.kinsta A 203.0.113.10",
	} {
		found := false
		for _, got := range upserts {
			if strings.Contains(got, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing DNS upsert %q in %#v", want, upserts)
		}
	}
	if got := countKinstaDNSUpserts(upserts, "_cf-custom-hostname.foobar.kinsta TXT cf-token"); got != 1 {
		t.Fatalf("_cf-custom-hostname upserts = %d, want 1 in %#v", got, upserts)
	}
}

func countKinstaDNSUpserts(upserts []string, needle string) int {
	count := 0
	for _, got := range upserts {
		if strings.Contains(got, needle) {
			count++
		}
	}
	return count
}

func stubDNSTypedDeletes(t *testing.T) *[]string {
	t.Helper()
	oldDelete := deleteDNSTypedRecordFn
	deletes := []string{}
	deleteDNSTypedRecordFn = func(token, accountID, zone, name, recordType string) error {
		deletes = append(deletes, strings.Join([]string{token, accountID, zone, name, recordType}, " "))
		return nil
	}
	t.Cleanup(func() { deleteDNSTypedRecordFn = oldDelete })
	return &deletes
}

func stubKinstaLookupHost(t *testing.T, addresses ...string) {
	t.Helper()
	oldLookup := kinstaLookupHost
	kinstaLookupHost = func(host string) ([]string, error) {
		return addresses, nil
	}
	t.Cleanup(func() { kinstaLookupHost = oldLookup })
}

func dnsCallContains(calls []string, want string) bool {
	for _, got := range calls {
		if strings.Contains(got, want) {
			return true
		}
	}
	return false
}

func TestProvisionKinstaSiteAddSkipsVerificationForExistingPrimaryDomainWithNoRecords(t *testing.T) {
	domainRecordsCalls := 0
	graphqlCalls := 0
	primaryCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/environments/kenv-live/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{{"id": "kdom-live", "name": "foobar.kinsta.nonfiction.dev"}}}})
		case "GET /sites/environments/domains/kdom-live/verification-records":
			domainRecordsCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"verification_records": []map[string]any{}, "pointing_records": []map[string]any{}}})
		case "POST /":
			graphqlCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
		case "PUT /sites/environments/kenv-live/change-primary-domain":
			primaryCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-primary-domain", "status": 202})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	oldUpsert := upsertDNSRecordFn
	upsertCalls := 0
	upsertDNSRecordFn = func(token, accountID, zone, name, recordType, content string, ttl int) error {
		upsertCalls++
		return nil
	}
	t.Cleanup(func() { upsertDNSRecordFn = oldUpsert })

	client := kinsta.NewClient(server.URL, "kinsta-token", kinsta.WithGraphQLURL(server.URL))
	plan := kinstaSiteAddPlan{
		Site:         "foobar",
		SiteID:       "foobar.kinsta",
		TargetName:   "kinsta",
		PHPVersion:   "8.3",
		DNSZone:      "nonfiction.dev",
		DNSAccountID: "14",
		Envs: []kinstaSiteAddEnvPlan{{
			Env:    "live",
			Domain: "foobar.kinsta.nonfiction.dev",
			URL:    "https://foobar.kinsta.nonfiction.dev",
		}},
	}
	liveEnv := kinsta.Environment{ID: "kenv-live", Name: "live", PrimaryDomain: kinsta.Domain{ID: "kdom-live", Name: "foobar.kinsta.nonfiction.dev"}}

	result, err := provisionKinstaSelectedEnvs(context.Background(), client, "dns-token", plan, "company-123", "ksite123", liveEnv)
	if err != nil {
		t.Fatalf("provisionKinstaSelectedEnvs() error = %v", err)
	}
	if len(result.Envs) != 1 || result.Envs[0].DomainID != "kdom-live" {
		t.Fatalf("result envs = %#v, want kdom-live", result.Envs)
	}
	if domainRecordsCalls != 1 {
		t.Fatalf("domain records calls = %d, want 1", domainRecordsCalls)
	}
	if graphqlCalls != 0 || primaryCalls != 0 || upsertCalls != 0 {
		t.Fatalf("graphql=%d primary=%d upsert=%d, want no verification, primary change, or DNS upsert", graphqlCalls, primaryCalls, upsertCalls)
	}
}

func TestProvisionKinstaSiteAddPreservesExistingPublicPrimaryDomain(t *testing.T) {
	primaryCalls := 0
	domainRecordsCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/environments/kenv-live/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{
				{"id": "kdom-live", "name": "foobar.kinsta.nonfiction.dev", "is_primary": false},
				{"id": "kdom-public", "name": "www.foobar.com", "is_primary": true},
			}}})
		case "GET /sites/environments/domains/kdom-live/verification-records":
			domainRecordsCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"pointing_records": []map[string]any{{"name": "foobar.kinsta.nonfiction.dev", "type": "A", "content": "203.0.113.10", "ttl": 300}}}})
		case "PUT /sites/environments/kenv-live/change-primary-domain":
			primaryCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-primary-domain", "status": 202})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	oldUpsert := upsertDNSRecordFn
	upsertDNSRecordFn = func(token, accountID, zone, name, recordType, content string, ttl int) error { return nil }
	t.Cleanup(func() { upsertDNSRecordFn = oldUpsert })
	stubDNSTypedDeletes(t)

	client := kinsta.NewClient(server.URL, "kinsta-token", kinsta.WithGraphQLURL(server.URL))
	plan := kinstaSiteAddPlan{
		Site:         "foobar",
		SiteID:       "foobar.kinsta",
		TargetName:   "kinsta",
		PHPVersion:   "8.3",
		DNSZone:      "nonfiction.dev",
		DNSAccountID: "14",
		Envs: []kinstaSiteAddEnvPlan{{
			Env:    "live",
			Domain: "foobar.kinsta.nonfiction.dev",
			URL:    "https://foobar.kinsta.nonfiction.dev",
		}},
	}
	liveEnv := kinsta.Environment{ID: "kenv-live", Name: "live", PrimaryDomain: kinsta.Domain{ID: "kdom-public", Name: "www.foobar.com"}}

	result, err := provisionKinstaSelectedEnvs(context.Background(), client, "dns-token", plan, "company-123", "ksite123", liveEnv)
	if err != nil {
		t.Fatalf("provisionKinstaSelectedEnvs() error = %v", err)
	}
	if len(result.Envs) != 1 || result.Envs[0].DomainID != "kdom-live" {
		t.Fatalf("result envs = %#v, want kdom-live", result.Envs)
	}
	if domainRecordsCalls != 1 {
		t.Fatalf("domain records calls = %d, want 1", domainRecordsCalls)
	}
	if primaryCalls != 0 {
		t.Fatalf("change primary calls = %d, want 0", primaryCalls)
	}
}

func TestProvisionKinstaSiteAddContinuesWhenVerificationActionIsNotVisible(t *testing.T) {
	oldInterval := kinstaDomainRecordsWaitInterval
	kinstaDomainRecordsWaitInterval = time.Millisecond
	t.Cleanup(func() { kinstaDomainRecordsWaitInterval = oldInterval })

	domainListCalls := 0
	domainRecordsCalls := 0
	confirmCalls := 0
	primaryCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/environments/kenv-live/domains":
			domainListCalls++
			domains := []map[string]any{}
			if domainListCalls > 1 {
				domains = []map[string]any{{"id": "kdom-live", "name": "foobar.kinsta.nonfiction.dev", "is_primary": false}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": domains}})
		case "POST /sites/environments/kenv-live/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-add-domain", "status": 202})
		case "GET /operations/op-add-domain":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Successfully finished request"})
		case "GET /sites/environments/domains/kdom-live/verification-records":
			domainRecordsCalls++
			siteDomain := map[string]any{"verification_records": []map[string]any{{"name": "k-verification.foobar.kinsta.nonfiction.dev", "type": "TXT", "content": "verify-token"}}}
			if domainRecordsCalls > 1 {
				siteDomain["verification_records"] = []map[string]any{{"name": "_cf-custom-hostname.foobar.kinsta.nonfiction.dev", "type": "TXT", "content": "cf-token"}, {"name": "_acme-challenge.foobar.kinsta.nonfiction.dev", "type": "CNAME", "content": "foobar.kinsta.nonfiction.dev.kinstavalidation.app"}}
			}
			if domainRecordsCalls > 2 {
				siteDomain["pointing_records"] = []map[string]any{{"name": "foobar.kinsta.nonfiction.dev", "type": "A", "content": "203.0.113.10", "ttl": 300}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": siteDomain})
		case "POST /":
			var payload struct {
				OperationName string `json:"operationName"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("graphql decode error = %v", err)
			}
			switch payload.OperationName {
			case "ValidateVerificationRecordsOfSiteDomains":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"validateVerificationRecordsOfSiteDomains": []map[string]any{{"idSiteDomain": "kdom-live", "isValid": true}}}})
			case "ConfirmCloudflareVerification":
				confirmCalls++
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"idAction": 123}})
			case "Action":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"action": nil, "action_liveKeys": []string{"key"}}})
			default:
				t.Fatalf("unexpected graphql operation %q", payload.OperationName)
			}
		case "PUT /sites/environments/kenv-live/change-primary-domain":
			primaryCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-primary-domain", "status": 202})
		case "GET /operations/op-primary-domain":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Successfully finished request"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	oldUpsert := upsertDNSRecordFn
	upserts := []string{}
	upsertDNSRecordFn = func(token, accountID, zone, name, recordType, content string, ttl int) error {
		upserts = append(upserts, strings.Join([]string{name, recordType, content}, " "))
		return nil
	}
	t.Cleanup(func() { upsertDNSRecordFn = oldUpsert })
	stubDNSTypedDeletes(t)

	client := kinsta.NewClient(server.URL, "kinsta-token", kinsta.WithGraphQLURL(server.URL))
	plan := kinstaSiteAddPlan{
		Site:         "foobar",
		SiteID:       "foobar.kinsta",
		TargetName:   "kinsta",
		PHPVersion:   "8.3",
		DNSZone:      "nonfiction.dev",
		DNSAccountID: "14",
		Envs: []kinstaSiteAddEnvPlan{{
			Env:    "live",
			Domain: "foobar.kinsta.nonfiction.dev",
			URL:    "https://foobar.kinsta.nonfiction.dev",
		}},
	}

	if _, err := provisionKinstaSelectedEnvs(context.Background(), client, "dns-token", plan, "company-123", "ksite123", kinsta.Environment{ID: "kenv-live", Name: "live", PrimaryDomain: kinsta.Domain{Name: "foobar.kinsta.cloud"}}); err != nil {
		t.Fatalf("provisionKinstaSelectedEnvs() error = %v", err)
	}
	if confirmCalls != 1 || primaryCalls != 1 {
		t.Fatalf("confirm=%d primary=%d, want one each", confirmCalls, primaryCalls)
	}
	for _, want := range []string{
		"k-verification.foobar.kinsta TXT verify-token",
		"_cf-custom-hostname.foobar.kinsta TXT cf-token",
		"_acme-challenge.foobar.kinsta CNAME foobar.kinsta.nonfiction.dev.kinstavalidation.app",
		"foobar.kinsta A 203.0.113.10",
	} {
		found := false
		for _, got := range upserts {
			if strings.Contains(got, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing DNS upsert %q in %#v", want, upserts)
		}
	}
}

func TestProvisionKinstaSiteAddUsesGeneratedDNSFallbackWhenPointingRecordsAreMissing(t *testing.T) {
	oldTimeout := kinstaDomainPhantomWaitTimeout
	oldRecordsTimeout := kinstaDomainRecordsWaitTimeout
	oldInterval := kinstaDomainRecordsWaitInterval
	kinstaDomainPhantomWaitTimeout = 3 * time.Millisecond
	kinstaDomainRecordsWaitTimeout = 10 * time.Millisecond
	kinstaDomainRecordsWaitInterval = time.Millisecond
	t.Cleanup(func() {
		kinstaDomainPhantomWaitTimeout = oldTimeout
		kinstaDomainRecordsWaitTimeout = oldRecordsTimeout
		kinstaDomainRecordsWaitInterval = oldInterval
	})

	domainListCalls := 0
	domainRecordsCalls := 0
	primaryCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/environments/kenv-live/domains":
			domainListCalls++
			domains := []map[string]any{}
			if domainListCalls > 1 {
				domains = []map[string]any{{"id": "kdom-live", "name": "foobar.kinsta.nonfiction.dev", "is_primary": false}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": domains}})
		case "POST /sites/environments/kenv-live/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-add-domain", "status": 202})
		case "GET /operations/op-add-domain":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Successfully finished request"})
		case "GET /sites/environments/domains/kdom-live/verification-records":
			domainRecordsCalls++
			records := []map[string]any{{"name": "k-verification.foobar.kinsta.nonfiction.dev", "type": "TXT", "content": "verify-token"}}
			if domainRecordsCalls > 1 {
				records = []map[string]any{{"name": "_acme-challenge.foobar.kinsta.nonfiction.dev", "type": "CNAME", "content": "foobar.kinsta.nonfiction.dev.kinstavalidation.app"}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"verification_records": records}})
		case "POST /":
			var payload struct {
				OperationName string `json:"operationName"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("graphql decode error = %v", err)
			}
			switch payload.OperationName {
			case "ValidateVerificationRecordsOfSiteDomains":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"validateVerificationRecordsOfSiteDomains": []map[string]any{{"idSiteDomain": "kdom-live", "isValid": true}}}})
			case "ConfirmCloudflareVerification":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"idAction": 123}})
			case "Action":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"action": nil, "action_liveKeys": []string{"key"}}})
			default:
				t.Fatalf("unexpected graphql operation %q", payload.OperationName)
			}
		case "PUT /sites/environments/kenv-live/change-primary-domain":
			primaryCalls++
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "domain is not ready"})
		case "GET /operations/op-primary-domain":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Successfully finished request"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	oldUpsert := upsertDNSRecordFn
	upserts := []string{}
	upsertDNSRecordFn = func(token, accountID, zone, name, recordType, content string, ttl int) error {
		upserts = append(upserts, strings.Join([]string{name, recordType, content}, " "))
		return nil
	}
	t.Cleanup(func() { upsertDNSRecordFn = oldUpsert })
	dnsDeletes := stubDNSTypedDeletes(t)
	stubKinstaLookupHost(t, "203.0.113.10")
	stubKinstaLookupHost(t, "203.0.113.10")

	client := kinsta.NewClient(server.URL, "kinsta-token", kinsta.WithGraphQLURL(server.URL))
	plan := kinstaSiteAddPlan{
		Site:         "foobar",
		SiteID:       "foobar.kinsta",
		TargetName:   "kinsta",
		BaseDomain:   "nonfiction.dev",
		PHPVersion:   "8.3",
		DNSZone:      "nonfiction.dev",
		DNSAccountID: "14",
		Envs: []kinstaSiteAddEnvPlan{{
			Env:    "live",
			Domain: "foobar.kinsta.nonfiction.dev",
			URL:    "https://foobar.kinsta.nonfiction.dev",
		}},
	}
	liveEnv := kinsta.Environment{ID: "kenv-live", Name: "live", PrimaryDomain: kinsta.Domain{Name: "foobar.kinsta.cloud"}}

	_, err := provisionKinstaSelectedEnvs(context.Background(), client, "dns-token", plan, "company-123", "ksite123", liveEnv)
	if err == nil {
		t.Fatal("provisionKinstaSelectedEnvs() error = nil, want manual verification error")
	}
	if msg := err.Error(); !strings.Contains(msg, "Kinsta did not return authoritative pointing records") || !strings.Contains(msg, "Open MyKinsta") || !strings.Contains(msg, "command is resumable") {
		t.Fatalf("provisionKinstaSelectedEnvs() error = %q, want manual verification guidance", msg)
	}
	if primaryCalls == 0 {
		t.Fatalf("primary calls = %d, want fallback primary retry before manual verification", primaryCalls)
	}
	for _, want := range []string{
		"_acme-challenge.foobar.kinsta CNAME foobar.kinsta.nonfiction.dev.kinstavalidation.app",
		"foobar.kinsta A 203.0.113.10",
	} {
		if !dnsCallContains(upserts, want) {
			t.Fatalf("missing DNS upsert %q in %#v", want, upserts)
		}
	}
	if got := countKinstaDNSUpserts(upserts, "_acme-challenge.foobar.kinsta CNAME foobar.kinsta.nonfiction.dev.kinstavalidation.app"); got != 1 {
		t.Fatalf("_acme-challenge upserts = %d, want 1 in %#v", got, upserts)
	}
	for _, want := range []string{"foobar.kinsta CNAME"} {
		if !dnsCallContains(*dnsDeletes, want) {
			t.Fatalf("missing conflicting DNS delete %q in %#v", want, *dnsDeletes)
		}
	}
}

func TestProvisionKinstaSiteAddFallbackCanPromotePrimaryAndContinueStaging(t *testing.T) {
	oldTimeout := kinstaDomainPhantomWaitTimeout
	oldInterval := kinstaDomainRecordsWaitInterval
	kinstaDomainPhantomWaitTimeout = 20 * time.Millisecond
	kinstaDomainRecordsWaitInterval = time.Millisecond
	t.Cleanup(func() {
		kinstaDomainPhantomWaitTimeout = oldTimeout
		kinstaDomainRecordsWaitInterval = oldInterval
	})

	liveDomainListCalls := 0
	stagingDomainListCalls := 0
	liveDomainRecordsCalls := 0
	livePrimaryCalls := 0
	stagingPrimaryCalls := 0
	environmentCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/environments/kenv-live/domains":
			liveDomainListCalls++
			domains := []map[string]any{}
			if liveDomainListCalls > 1 {
				domain := map[string]any{"id": "kdom-live", "name": "foobar.kinsta.nonfiction.dev", "is_primary": false}
				if livePrimaryCalls > 0 {
					domain["is_primary"] = true
				}
				domains = append(domains,
					map[string]any{"id": "kdom-generated-live", "name": "foobar.kinsta.cloud", "is_primary": livePrimaryCalls == 0},
					domain,
				)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": domains}})
		case "POST /sites/environments/kenv-live/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-add-live-domain", "status": 202})
		case "GET /operations/op-add-live-domain":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Successfully finished request"})
		case "GET /sites/environments/domains/kdom-live/verification-records":
			liveDomainRecordsCalls++
			records := []map[string]any{{"name": "k-verification.foobar.kinsta.nonfiction.dev", "type": "TXT", "content": "verify-token"}}
			if liveDomainRecordsCalls > 1 {
				records = []map[string]any{{"name": "_acme-challenge.foobar.kinsta.nonfiction.dev", "type": "CNAME", "content": "foobar.kinsta.nonfiction.dev.kinstavalidation.app"}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"verification_records": records}})
		case "PUT /sites/environments/kenv-live/change-primary-domain":
			livePrimaryCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-primary-live", "status": 202})
		case "GET /operations/op-primary-live":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Successfully finished request"})
		case "GET /sites/ksite123/environments":
			environmentCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"site": map[string]any{"environments": []map[string]any{
				{"id": "kenv-live", "name": "live", "php_version": "8.3", "primaryDomain": map[string]any{"id": "kdom-generated-live", "name": "foobar.kinsta.cloud"}},
				{"id": "kenv-staging", "name": "staging", "php_version": "8.3", "web_root": "/public", "primaryDomain": map[string]any{"id": "kdom-generated-staging", "name": "foobar-staging.kinsta.cloud"}},
			}}})
		case "GET /sites/environments/kenv-staging/domains":
			stagingDomainListCalls++
			domains := []map[string]any{
				{"id": "kdom-generated-staging", "name": "foobar-staging.kinsta.cloud", "is_primary": stagingPrimaryCalls == 0},
				{"id": "kdom-staging", "name": "foobar-staging.kinsta.nonfiction.dev", "is_primary": stagingPrimaryCalls > 0},
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": domains}})
		case "GET /sites/environments/domains/kdom-staging/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"pointing_records": []map[string]any{{"name": "foobar-staging.kinsta.nonfiction.dev", "type": "A", "content": "203.0.113.20", "ttl": 300}}}})
		case "PUT /sites/environments/kenv-staging/change-primary-domain":
			stagingPrimaryCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-primary-staging", "status": 202})
		case "GET /operations/op-primary-staging":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Successfully finished request"})
		case "POST /":
			var payload struct {
				OperationName string `json:"operationName"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("graphql decode error = %v", err)
			}
			switch payload.OperationName {
			case "ValidateVerificationRecordsOfSiteDomains":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"validateVerificationRecordsOfSiteDomains": []map[string]any{{"idSiteDomain": "kdom-live", "isValid": true}}}})
			case "ConfirmCloudflareVerification":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"idAction": 123}})
			case "Action":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"action": nil, "action_liveKeys": []string{"key"}}})
			default:
				t.Fatalf("unexpected graphql operation %q", payload.OperationName)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	oldUpsert := upsertDNSRecordFn
	upserts := []string{}
	upsertDNSRecordFn = func(token, accountID, zone, name, recordType, content string, ttl int) error {
		upserts = append(upserts, strings.Join([]string{name, recordType, content}, " "))
		return nil
	}
	t.Cleanup(func() { upsertDNSRecordFn = oldUpsert })
	stubDNSTypedDeletes(t)
	stubKinstaLookupHost(t, "203.0.113.10")

	client := kinsta.NewClient(server.URL, "kinsta-token", kinsta.WithGraphQLURL(server.URL))
	plan := kinstaSiteAddPlan{
		Site:         "foobar",
		SiteID:       "foobar.kinsta",
		TargetName:   "kinsta",
		BaseDomain:   "nonfiction.dev",
		PHPVersion:   "8.3",
		DNSZone:      "nonfiction.dev",
		DNSAccountID: "14",
		KinstaSlug:   "foobar",
		Envs: []kinstaSiteAddEnvPlan{
			{Env: "live", Domain: "foobar.kinsta.nonfiction.dev", URL: "https://foobar.kinsta.nonfiction.dev"},
			{Env: "staging", Domain: "foobar-staging.kinsta.nonfiction.dev", URL: "https://foobar-staging.kinsta.nonfiction.dev"},
		},
	}
	liveEnv := kinsta.Environment{ID: "kenv-live", Name: "live", PrimaryDomain: kinsta.Domain{Name: "foobar.kinsta.cloud"}}

	result, err := provisionKinstaSelectedEnvs(context.Background(), client, "dns-token", plan, "company-123", "ksite123", liveEnv)
	if err != nil {
		t.Fatalf("provisionKinstaSelectedEnvs() error = %v", err)
	}
	if livePrimaryCalls != 1 || stagingPrimaryCalls != 1 {
		t.Fatalf("primary calls live=%d staging=%d, want one each", livePrimaryCalls, stagingPrimaryCalls)
	}
	if environmentCalls == 0 {
		t.Fatal("staging environment was not checked after live fallback recovery")
	}
	if len(result.Envs) != 2 {
		t.Fatalf("result envs len = %d, want 2: %#v", len(result.Envs), result.Envs)
	}
	if result.Envs[0].Env != "live" || result.Envs[0].DomainID != "kdom-live" || result.Envs[1].Env != "staging" || result.Envs[1].DomainID != "kdom-staging" {
		t.Fatalf("result envs = %#v, want live and staging domains", result.Envs)
	}
	for _, want := range []string{
		"_acme-challenge.foobar.kinsta CNAME foobar.kinsta.nonfiction.dev.kinstavalidation.app",
		"foobar.kinsta A 203.0.113.10",
		"foobar-staging.kinsta A 203.0.113.20",
	} {
		if !dnsCallContains(upserts, want) {
			t.Fatalf("missing DNS upsert %q in %#v", want, upserts)
		}
	}
}

func TestKinstaFallbackPrimaryTimeoutDoesNotExposeListContextDeadline(t *testing.T) {
	oldTimeout := kinstaDomainRecordsWaitTimeout
	oldInterval := kinstaDomainRecordsWaitInterval
	kinstaDomainRecordsWaitTimeout = 5 * time.Millisecond
	kinstaDomainRecordsWaitInterval = time.Millisecond
	t.Cleanup(func() {
		kinstaDomainRecordsWaitTimeout = oldTimeout
		kinstaDomainRecordsWaitInterval = oldInterval
	})

	primaryCalls := 0
	domainListCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "PUT /sites/environments/kenv-live/change-primary-domain":
			primaryCalls++
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "domain is not ready"})
		case "GET /sites/environments/kenv-live/domains":
			domainListCalls++
			time.Sleep(50 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{{"id": "kdom-live", "name": "foobar.kinsta.nonfiction.dev"}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	client := kinsta.NewClient(server.URL, "kinsta-token")
	_, _, err := waitKinstaDomainReadyAfterFallback(context.Background(), client, kinsta.Environment{ID: "kenv-live", PrimaryDomain: kinsta.Domain{Name: "foobar.kinsta.cloud"}}, "foobar.kinsta.nonfiction.dev", kinsta.Domain{ID: "kdom-live", Name: "foobar.kinsta.nonfiction.dev"})
	if err == nil {
		t.Fatal("waitKinstaDomainReadyAfterFallback() error = nil, want timeout")
	}
	msg := err.Error()
	if !strings.Contains(msg, "timed out waiting for Kinsta to accept foobar.kinsta.nonfiction.dev as primary after fallback DNS") {
		t.Fatalf("waitKinstaDomainReadyAfterFallback() error = %q, want fallback timeout", msg)
	}
	if strings.Contains(msg, "Get \"") || strings.Contains(msg, "context deadline exceeded") {
		t.Fatalf("waitKinstaDomainReadyAfterFallback() error = %q, want no raw list context deadline", msg)
	}
	if primaryCalls == 0 || domainListCalls == 0 {
		t.Fatalf("primaryCalls=%d domainListCalls=%d, want both", primaryCalls, domainListCalls)
	}
}

func TestProvisionKinstaSiteAddUsesGeneratedDNSFallbackWithOnlyInitialVerificationRecord(t *testing.T) {
	oldTimeout := kinstaDomainPhantomWaitTimeout
	oldRecordsTimeout := kinstaDomainRecordsWaitTimeout
	oldInterval := kinstaDomainRecordsWaitInterval
	kinstaDomainPhantomWaitTimeout = 3 * time.Millisecond
	kinstaDomainRecordsWaitTimeout = 10 * time.Millisecond
	kinstaDomainRecordsWaitInterval = time.Millisecond
	t.Cleanup(func() {
		kinstaDomainPhantomWaitTimeout = oldTimeout
		kinstaDomainRecordsWaitTimeout = oldRecordsTimeout
		kinstaDomainRecordsWaitInterval = oldInterval
	})

	domainListCalls := 0
	primaryCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/environments/kenv-live/domains":
			domainListCalls++
			domains := []map[string]any{}
			if domainListCalls > 1 {
				domains = []map[string]any{{"id": "kdom-live", "name": "foobar.kinsta.nonfiction.dev", "is_primary": false}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": domains}})
		case "POST /sites/environments/kenv-live/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-add-domain", "status": 202})
		case "GET /operations/op-add-domain":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Successfully finished request"})
		case "GET /sites/environments/domains/kdom-live/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"verification_records": []map[string]any{{"name": "k-verification.foobar.kinsta.nonfiction.dev", "type": "TXT", "content": "verify-token"}}}})
		case "POST /":
			var payload struct {
				OperationName string `json:"operationName"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("graphql decode error = %v", err)
			}
			switch payload.OperationName {
			case "ValidateVerificationRecordsOfSiteDomains":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"validateVerificationRecordsOfSiteDomains": []map[string]any{{"idSiteDomain": "kdom-live", "isValid": true}}}})
			case "ConfirmCloudflareVerification":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"idAction": 123}})
			case "Action":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"action": nil, "action_liveKeys": []string{"key"}}})
			default:
				t.Fatalf("unexpected graphql operation %q", payload.OperationName)
			}
		case "PUT /sites/environments/kenv-live/change-primary-domain":
			primaryCalls++
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "domain is not ready"})
		case "GET /operations/op-primary-domain":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Successfully finished request"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	oldUpsert := upsertDNSRecordFn
	upserts := []string{}
	upsertDNSRecordFn = func(token, accountID, zone, name, recordType, content string, ttl int) error {
		upserts = append(upserts, strings.Join([]string{name, recordType, content}, " "))
		return nil
	}
	t.Cleanup(func() { upsertDNSRecordFn = oldUpsert })
	dnsDeletes := stubDNSTypedDeletes(t)
	stubKinstaLookupHost(t, "203.0.113.10")

	client := kinsta.NewClient(server.URL, "kinsta-token", kinsta.WithGraphQLURL(server.URL))
	plan := kinstaSiteAddPlan{
		Site:         "foobar",
		SiteID:       "foobar.kinsta",
		TargetName:   "kinsta",
		BaseDomain:   "nonfiction.dev",
		PHPVersion:   "8.3",
		DNSZone:      "nonfiction.dev",
		DNSAccountID: "14",
		Envs: []kinstaSiteAddEnvPlan{{
			Env:    "live",
			Domain: "foobar.kinsta.nonfiction.dev",
			URL:    "https://foobar.kinsta.nonfiction.dev",
		}},
	}
	liveEnv := kinsta.Environment{ID: "kenv-live", Name: "live", PrimaryDomain: kinsta.Domain{Name: "foobar.kinsta.cloud"}}

	_, err := provisionKinstaSelectedEnvs(context.Background(), client, "dns-token", plan, "company-123", "ksite123", liveEnv)
	if err == nil {
		t.Fatal("provisionKinstaSelectedEnvs() error = nil, want manual verification error")
	}
	if msg := err.Error(); !strings.Contains(msg, "Kinsta did not return authoritative pointing records") || !strings.Contains(msg, "Open MyKinsta") || !strings.Contains(msg, "command is resumable") {
		t.Fatalf("provisionKinstaSelectedEnvs() error = %q, want manual verification guidance", msg)
	}
	if primaryCalls == 0 {
		t.Fatalf("primary calls = %d, want fallback primary retry before manual verification", primaryCalls)
	}
	for _, want := range []string{
		"_acme-challenge.foobar.kinsta CNAME foobar.kinsta.nonfiction.dev.kinstavalidation.app",
		"foobar.kinsta A 203.0.113.10",
	} {
		if !dnsCallContains(upserts, want) {
			t.Fatalf("missing DNS upsert %q in %#v", want, upserts)
		}
	}
	foundFallback := false
	for _, got := range upserts {
		if strings.Contains(got, "foobar.kinsta A 203.0.113.10") {
			foundFallback = true
		}
	}
	if !foundFallback {
		t.Fatalf("missing generated DNS fallback in %#v", upserts)
	}
	for _, want := range []string{"foobar.kinsta CNAME"} {
		if !dnsCallContains(*dnsDeletes, want) {
			t.Fatalf("missing conflicting DNS delete %q in %#v", want, *dnsDeletes)
		}
	}
}

func TestProvisionKinstaSiteAddFallsBackWhenKinstaVerificationDetectionLags(t *testing.T) {
	oldTimeout := kinstaDomainPhantomWaitTimeout
	oldRecordsTimeout := kinstaDomainRecordsWaitTimeout
	oldInterval := kinstaDomainRecordsWaitInterval
	kinstaDomainPhantomWaitTimeout = 3 * time.Millisecond
	kinstaDomainRecordsWaitTimeout = 10 * time.Millisecond
	kinstaDomainRecordsWaitInterval = time.Millisecond
	t.Cleanup(func() {
		kinstaDomainPhantomWaitTimeout = oldTimeout
		kinstaDomainRecordsWaitTimeout = oldRecordsTimeout
		kinstaDomainRecordsWaitInterval = oldInterval
	})

	domainListCalls := 0
	primaryCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/environments/kenv-live/domains":
			domainListCalls++
			domains := []map[string]any{}
			if domainListCalls > 1 {
				domains = []map[string]any{{"id": "kdom-live", "name": "foobar.kinsta.nonfiction.dev", "is_primary": false}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": domains}})
		case "POST /sites/environments/kenv-live/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-add-domain", "status": 202})
		case "GET /operations/op-add-domain":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Successfully finished request"})
		case "GET /sites/environments/domains/kdom-live/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"verification_records": []map[string]any{{"name": "_acme-challenge.foobar.kinsta.nonfiction.dev", "type": "CNAME", "content": "foobar.kinsta.nonfiction.dev.kinstavalidation.app"}}}})
		case "POST /":
			var payload struct {
				OperationName string `json:"operationName"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("graphql decode error = %v", err)
			}
			switch payload.OperationName {
			case "ValidateVerificationRecordsOfSiteDomains":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"validateVerificationRecordsOfSiteDomains": []map[string]any{{
					"idSiteDomain": "kdom-live",
					"isValid":      false,
					"records": []map[string]any{
						{"name": "_cf-custom-hostname.foobar.kinsta.nonfiction.dev", "value": "cf-token", "type": "TXT", "isDetected": false},
						{"name": "_acme-challenge.foobar.kinsta.nonfiction.dev", "value": "foobar.kinsta.nonfiction.dev.kinstavalidation.app", "type": "CNAME", "isDetected": true},
					},
				}}}})
			default:
				t.Fatalf("unexpected graphql operation %q", payload.OperationName)
			}
		case "PUT /sites/environments/kenv-live/change-primary-domain":
			primaryCalls++
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "domain is not ready"})
		case "GET /operations/op-primary-domain":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Successfully finished request"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	oldUpsert := upsertDNSRecordFn
	upserts := []string{}
	upsertDNSRecordFn = func(token, accountID, zone, name, recordType, content string, ttl int) error {
		upserts = append(upserts, strings.Join([]string{name, recordType, content}, " "))
		return nil
	}
	t.Cleanup(func() { upsertDNSRecordFn = oldUpsert })
	stubDNSTypedDeletes(t)
	stubKinstaLookupHost(t, "203.0.113.10")

	client := kinsta.NewClient(server.URL, "kinsta-token", kinsta.WithGraphQLURL(server.URL))
	plan := kinstaSiteAddPlan{
		Site:         "foobar",
		SiteID:       "foobar.kinsta",
		TargetName:   "kinsta",
		BaseDomain:   "nonfiction.dev",
		PHPVersion:   "8.3",
		DNSZone:      "nonfiction.dev",
		DNSAccountID: "14",
		Envs: []kinstaSiteAddEnvPlan{{
			Env:    "live",
			Domain: "foobar.kinsta.nonfiction.dev",
			URL:    "https://foobar.kinsta.nonfiction.dev",
		}},
	}
	liveEnv := kinsta.Environment{ID: "kenv-live", Name: "live", PrimaryDomain: kinsta.Domain{Name: "foobar.kinsta.cloud"}}

	_, err := provisionKinstaSelectedEnvs(context.Background(), client, "dns-token", plan, "company-123", "ksite123", liveEnv)
	if err == nil {
		t.Fatal("provisionKinstaSelectedEnvs() error = nil, want manual verification error")
	}
	if msg := err.Error(); !strings.Contains(msg, "Kinsta did not return authoritative pointing records") || !strings.Contains(msg, "Open MyKinsta") || !strings.Contains(msg, "command is resumable") {
		t.Fatalf("provisionKinstaSelectedEnvs() error = %q, want manual verification guidance", msg)
	}
	if primaryCalls == 0 {
		t.Fatalf("primary calls = %d, want fallback primary retry before manual verification", primaryCalls)
	}
	for _, want := range []string{
		"_cf-custom-hostname.foobar.kinsta TXT cf-token",
		"_acme-challenge.foobar.kinsta CNAME foobar.kinsta.nonfiction.dev.kinstavalidation.app",
		"foobar.kinsta A 203.0.113.10",
	} {
		if !dnsCallContains(upserts, want) {
			t.Fatalf("missing DNS upsert %q in %#v", want, upserts)
		}
	}
}

func TestProvisionKinstaSiteAddTimesOutWithoutChangingPrimaryWhenPointingRecordsAreMissing(t *testing.T) {
	domainListCalls := 0
	validationCalls := 0
	confirmCalls := 0
	primaryCalls := 0
	oldTimeout := kinstaDomainRecordsWaitTimeout
	oldInterval := kinstaDomainRecordsWaitInterval
	kinstaDomainRecordsWaitTimeout = 3 * time.Millisecond
	kinstaDomainRecordsWaitInterval = time.Millisecond
	t.Cleanup(func() {
		kinstaDomainRecordsWaitTimeout = oldTimeout
		kinstaDomainRecordsWaitInterval = oldInterval
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/environments/kenv-live/domains":
			domainListCalls++
			domains := []map[string]any{}
			if domainListCalls > 1 {
				domains = []map[string]any{{"id": "kdom-live", "name": "foobar.kinsta.nonfiction.dev", "is_primary": false}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": domains}})
		case "POST /sites/environments/kenv-live/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-add-domain", "status": 202})
		case "GET /operations/op-add-domain":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Successfully finished request"})
		case "GET /sites/environments/domains/kdom-live/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"verification_records": []map[string]any{{"name": "k-verification.foobar.kinsta.nonfiction.dev", "type": "TXT", "content": "verify-token"}}}})
		case "POST /":
			var payload struct {
				OperationName string `json:"operationName"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("graphql decode error = %v", err)
			}
			switch payload.OperationName {
			case "ValidateVerificationRecordsOfSiteDomains":
				validationCalls++
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"validateVerificationRecordsOfSiteDomains": []map[string]any{{"idSiteDomain": "kdom-live", "isValid": true}}}})
			case "ConfirmCloudflareVerification":
				confirmCalls++
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"idAction": 123}})
			case "Action":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"action": map[string]any{"id": 123, "error": nil, "isDone": false}, "action_liveKeys": []string{"key"}}})
			default:
				t.Fatalf("unexpected graphql operation %q", payload.OperationName)
			}
		case "PUT /sites/environments/kenv-live/change-primary-domain":
			primaryCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-primary-domain", "status": 202})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	oldUpsert := upsertDNSRecordFn
	upserts := []string{}
	upsertDNSRecordFn = func(token, accountID, zone, name, recordType, content string, ttl int) error {
		upserts = append(upserts, strings.Join([]string{token, accountID, zone, name, recordType, content}, " "))
		return nil
	}
	t.Cleanup(func() { upsertDNSRecordFn = oldUpsert })

	client := kinsta.NewClient(server.URL, "kinsta-token", kinsta.WithGraphQLURL(server.URL))
	plan := kinstaSiteAddPlan{
		Site:         "foobar",
		SiteID:       "foobar.kinsta",
		TargetName:   "kinsta",
		PHPVersion:   "8.3",
		DNSZone:      "nonfiction.dev",
		DNSAccountID: "14",
		Envs: []kinstaSiteAddEnvPlan{{
			Env:    "live",
			Domain: "foobar.kinsta.nonfiction.dev",
			URL:    "https://foobar.kinsta.nonfiction.dev",
		}},
	}

	_, err := provisionKinstaSelectedEnvs(context.Background(), client, "dns-token", plan, "company-123", "ksite123", kinsta.Environment{ID: "kenv-live", Name: "live"})
	if err == nil {
		t.Fatal("provisionKinstaSelectedEnvs() error = nil, want pointing record timeout")
	}
	if msg := err.Error(); !strings.Contains(msg, "timed out waiting for Kinsta pointing DNS records") || !strings.Contains(msg, "foobar.kinsta.nonfiction.dev") {
		t.Fatalf("provisionKinstaSelectedEnvs() error = %q, want timeout guidance", msg)
	}
	if primaryCalls != 0 {
		t.Fatalf("change primary calls = %d, want 0", primaryCalls)
	}
	if validationCalls == 0 || confirmCalls == 0 {
		t.Fatalf("verification calls validation=%d confirm=%d, want both before pointing timeout", validationCalls, confirmCalls)
	}
	if len(upserts) == 0 || !strings.Contains(upserts[0], "k-verification.foobar.kinsta TXT verify-token") {
		t.Fatalf("DNS upserts = %#v, want verification TXT written", upserts)
	}
}

func TestRunDomainKinstaAddExecutePrintsDNSAndCachesDomains(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{
		"provider": "kinsta",
		"site_id":  "client.kinsta",
		"env_id":   "client.kinsta:live",
		"name":     "client",
		"env":      "live",
		"target":   "kinsta",
		"hostname": "client.kinsta.nonfiction.dev",
		"url":      "https://client.kinsta.nonfiction.dev",
		"path":     "/www/client/public",
		"ssh":      map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"},
		"kinsta":   map[string]any{"site_id": "ksite123", "environment_id": "kenv-live", "domain_id": "kdom-internal"},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	var capturedPlan siteDomainPlan
	prepareCalls := 0
	oldPrepare := kinstaPrepareDomainFn
	oldPrimary := kinstaPrimaryDomainFn
	kinstaPrepareDomainFn = func(plan siteDomainPlan) (siteDomainProviderResult, error) {
		prepareCalls++
		capturedPlan = plan
		return siteDomainProviderResult{Domains: []siteDomainProviderDomain{
			{
				Name:     "www.client.com",
				Role:     "secondary",
				DomainID: "kdom-www",
				Records:  kinsta.DomainRecords{Pointing: []kinsta.DNSRecord{{Type: "CNAME", Name: "www.client.com", Content: "hosting.kinsta.cloud", TTL: 300}}},
			},
			{Name: "client.com", Role: "secondary", DomainID: "kdom-apex", Records: kinsta.DomainRecords{Pointing: []kinsta.DNSRecord{{Type: "A", Name: "client.com", Content: "203.0.113.20"}}}},
		}}, nil
	}
	kinstaPrimaryDomainFn = func(plan siteDomainPlan) (siteDomainProviderResult, error) {
		t.Fatalf("kinstaPrimaryDomainFn should not be called by domain add")
		return siteDomainProviderResult{}, nil
	}
	t.Cleanup(func() {
		kinstaPrepareDomainFn = oldPrepare
		kinstaPrimaryDomainFn = oldPrimary
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "add", "client.kinsta:live", "www.client.com", "client.com", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(domain add kinsta) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Add domain plan:", "provider:  kinsta", "fallback:  https://client.kinsta.nonfiction.dev", "secondary: www.client.com, client.com", "public DNS: no DNS records will be changed by nf", "Kinsta DNS records for client DNS:", "www.client.com (secondary):", "routing (point public DNS at Kinsta):", "CNAME  www.client.com  hosting.kinsta.cloud  TTL 300", "client.com (secondary):", "A  client.com  203.0.113.20", "Domain added."} {
		if !strings.Contains(output, want) {
			t.Fatalf("domain kinsta output missing %q:\n%s", want, output)
		}
	}
	for _, notWant := range []string{"Primary domain plan:", "kinsta setup:", "Waiting for client DNS records to resolve.", "Waiting for public domain checks.", "Launching primary domain now...", "Changing Kinsta primary domain", "Domain launched as primary."} {
		if strings.Contains(output, notWant) {
			t.Fatalf("domain add output unexpectedly contains %q:\n%s", notWant, output)
		}
	}
	if prepareCalls != 1 {
		t.Fatalf("prepareCalls=%d, want one", prepareCalls)
	}
	if capturedPlan.KinstaEnvID != "kenv-live" || capturedPlan.Canonical != "www.client.com" || capturedPlan.Primary || !reflect.DeepEqual(capturedPlan.Aliases, []string{"client.com"}) {
		t.Fatalf("captured plan = %#v, want kinsta env and secondary add", capturedPlan)
	}

	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("site records len = %d, want 1: %#v", len(records), records)
	}
	record := records[0]
	if got := recordValueString(record["hostname"]); got != "client.kinsta.nonfiction.dev" {
		t.Fatalf("hostname = %q, want unchanged internal hostname", got)
	}
	if got := recordValueString(record["url"]); got != "https://client.kinsta.nonfiction.dev" {
		t.Fatalf("url = %q, want unchanged internal url", got)
	}
	if got := recordValueString(record["internal_hostname"]); got != "client.kinsta.nonfiction.dev" {
		t.Fatalf("internal_hostname = %q, want client.kinsta.nonfiction.dev", got)
	}
	if got := recordValueString(record["internal_url"]); got != "https://client.kinsta.nonfiction.dev" {
		t.Fatalf("internal_url = %q, want internal url", got)
	}
	if got := recordValueString(record["primary_domain"]); got != "" {
		t.Fatalf("primary_domain = %q, want unset", got)
	}
	if _, ok := record["domain_state"]; ok {
		t.Fatalf("domain_state should not be written in new cache model: %#v", record)
	}
	domains, ok := record["domains"].([]any)
	if !ok || len(domains) != 2 {
		t.Fatalf("domains = %#v, want two domain entries", record["domains"])
	}
	www, _ := domains[0].(map[string]any)
	apex, _ := domains[1].(map[string]any)
	if recordValueString(www["name"]) != "www.client.com" || recordValueString(www["role"]) != "secondary" || recordValueString(www["management"]) != "external" || recordValueString(www["status"]) != "pending" || recordValueString(www["domain_id"]) != "kdom-www" {
		t.Fatalf("www domain = %#v", www)
	}
	if recordValueString(apex["name"]) != "client.com" || recordValueString(apex["role"]) != "secondary" || recordValueString(apex["management"]) != "external" || recordValueString(apex["status"]) != "pending" || recordValueString(apex["domain_id"]) != "kdom-apex" {
		t.Fatalf("apex domain = %#v", apex)
	}
	if got := mapStringAtPath(record, "kinsta", "domain_id"); got != "kdom-internal" {
		t.Fatalf("kinsta.domain_id = %q, want unchanged internal domain id", got)
	}
}

func TestRunDomainKinstaAddExistingDashboardDomainDoesNotRequireCachedSSHUser(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("KINSTA_API_KEY", "test-token")
	if err := state.SaveStateRecords("sites", []map[string]any{{
		"provider": "kinsta",
		"site_id":  "client.kinsta",
		"env_id":   "client.kinsta:live",
		"name":     "client",
		"env":      "live",
		"target":   "kinsta",
		"hostname": "client.kinsta.nonfiction.dev",
		"url":      "https://client.kinsta.nonfiction.dev",
		"path":     "/www/client/public",
		"ssh":      map[string]any{"host": "203.0.113.10", "port": "12345"},
		"kinsta":   map[string]any{"site_id": "ksite123", "environment_id": "kenv-live", "domain_id": "kdom-internal"},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	addCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer test-token"; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/environments/kenv-live/domains":
			verified := true
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{
				{"id": "kdom-internal", "name": "client.kinsta.nonfiction.dev", "is_primary": true},
				{"id": "kdom-dashboard", "name": "arpisnorth.com", "is_primary": false, "status": "verified", "is_verified": verified, "is_pointing": verified},
			}}})
		case "POST /sites/environments/kenv-live/domains":
			addCalls++
			http.Error(w, "domain should already exist", http.StatusInternalServerError)
		case "GET /sites/environments/domains/kdom-dashboard/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{
				"verification_records": []map[string]any{{"name": "_cf-custom-hostname.arpisnorth.com", "type": "TXT", "content": "verify-token"}},
				"pointing_records":     []map[string]any{{"name": "arpisnorth.com", "type": "A", "content": "203.0.113.20", "ttl": 300}},
			}})
		default:
			t.Fatalf("unexpected Kinsta request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KINSTA_BASE_URL", server.URL)

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "add", "client.kinsta:live", "arpisnorth.com", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(domain add existing Kinsta domain) = %d, want 0", got)
		}
	})
	if addCalls != 0 {
		t.Fatalf("Kinsta AddDomain calls = %d, want 0", addCalls)
	}
	for _, want := range []string{"Add domain plan:", "provider:  kinsta", "secondary: arpisnorth.com", "Kinsta DNS records for client DNS:", "A  arpisnorth.com  203.0.113.20  TTL 300", "Domain added."} {
		if !strings.Contains(output, want) {
			t.Fatalf("domain add existing Kinsta output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "Adding Kinsta domain arpisnorth.com") {
		t.Fatalf("domain add should not try to create an already attached Kinsta domain:\n%s", output)
	}

	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	domains := siteDomainEntryValues(records[0]["domains"])
	if len(domains) != 1 {
		t.Fatalf("domains = %#v, want one cached dashboard domain", records[0]["domains"])
	}
	domain := siteDomainEntryMap(domains[0])
	if recordValueString(domain["name"]) != "arpisnorth.com" || recordValueString(domain["role"]) != "secondary" || recordValueString(domain["management"]) != "external" || recordValueString(domain["status"]) != "verified" || recordValueString(domain["domain_id"]) != "kdom-dashboard" {
		t.Fatalf("cached domain = %#v, want verified dashboard domain", domain)
	}
}

func TestRunDomainKinstaAddWaitsBrieflyForRoutingRecords(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("KINSTA_API_KEY", "test-token")
	if err := state.SaveStateRecords("sites", []map[string]any{{
		"provider": "kinsta",
		"site_id":  "client.kinsta",
		"env_id":   "client.kinsta:live",
		"name":     "client",
		"env":      "live",
		"target":   "kinsta",
		"hostname": "client.kinsta.nonfiction.dev",
		"url":      "https://client.kinsta.nonfiction.dev",
		"path":     "/www/client/public",
		"ssh":      map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"},
		"kinsta":   map[string]any{"site_id": "ksite123", "environment_id": "kenv-live", "domain_id": "kdom-internal"},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	oldTimeout := kinstaDomainAddPointingWaitTimeout
	oldInterval := kinstaDomainAddPointingWaitInterval
	kinstaDomainAddPointingWaitTimeout = 50 * time.Millisecond
	kinstaDomainAddPointingWaitInterval = time.Millisecond
	t.Cleanup(func() {
		kinstaDomainAddPointingWaitTimeout = oldTimeout
		kinstaDomainAddPointingWaitInterval = oldInterval
	})

	recordCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/environments/kenv-live/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{{"id": "kdom-internal", "name": "client.kinsta.nonfiction.dev", "is_primary": true}, {"id": "kdom-generated", "name": "client.hosting.kinsta.cloud"}, {"id": "kdom-www", "name": "www.client.com"}}}})
		case "GET /sites/environments/domains/kdom-www/verification-records":
			recordCalls++
			siteDomain := map[string]any{"verification_records": []map[string]any{{"name": "_cf-custom-hostname.www.client.com", "type": "TXT", "content": "verify-token"}}}
			if recordCalls > 1 {
				siteDomain["pointing_records"] = []map[string]any{{"name": "www.client.com", "type": "CNAME", "content": "client.hosting.kinsta.cloud"}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": siteDomain})
		default:
			t.Fatalf("unexpected Kinsta request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KINSTA_BASE_URL", server.URL)

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "add", "client.kinsta:live", "www.client.com", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(domain add kinsta) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Waiting briefly for Kinsta routing records for www.client.com...", "Kinsta returned routing records for www.client.com.", "Kinsta DNS records for client DNS:", "verification (prove ownership and TLS validation):", "TXT  _cf-custom-hostname.www.client.com  verify-token", "routing (point public DNS at Kinsta):", "CNAME  www.client.com  client.hosting.kinsta.cloud", "Domain added."} {
		if !strings.Contains(output, want) {
			t.Fatalf("domain kinsta output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "https://my.kinsta.com/sites/domains/") {
		t.Fatalf("domain add should not print MyKinsta fallback URL when routing records are known:\n%s", output)
	}
	if recordCalls < 2 {
		t.Fatalf("DomainRecords calls = %d, want delayed pointing poll", recordCalls)
	}
}

func TestRunDomainKinstaAddShowsMyKinstaURLWhenRoutingRecordsMissing(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("KINSTA_API_KEY", "test-token")
	if err := state.SaveStateRecords("sites", []map[string]any{{
		"provider": "kinsta",
		"site_id":  "client.kinsta",
		"env_id":   "client.kinsta:live",
		"name":     "client",
		"env":      "live",
		"target":   "kinsta",
		"hostname": "client.kinsta.nonfiction.dev",
		"url":      "https://client.kinsta.nonfiction.dev",
		"path":     "/www/client/public",
		"ssh":      map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"},
		"kinsta":   map[string]any{"site_id": "ksite123", "environment_id": "kenv-live", "domain_id": "kdom-internal"},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	oldTimeout := kinstaDomainAddPointingWaitTimeout
	oldInterval := kinstaDomainAddPointingWaitInterval
	kinstaDomainAddPointingWaitTimeout = 2 * time.Millisecond
	kinstaDomainAddPointingWaitInterval = time.Millisecond
	t.Cleanup(func() {
		kinstaDomainAddPointingWaitTimeout = oldTimeout
		kinstaDomainAddPointingWaitInterval = oldInterval
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/environments/kenv-live/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{{"id": "kdom-internal", "name": "client.kinsta.nonfiction.dev", "is_primary": true}, {"id": "kdom-www", "name": "www.client.com"}}}})
		case "GET /sites/environments/domains/kdom-www/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"verification_records": []map[string]any{{"name": "_cf-custom-hostname.www.client.com", "type": "TXT", "content": "verify-token"}}}})
		default:
			t.Fatalf("unexpected Kinsta request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KINSTA_BASE_URL", server.URL)

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "add", "client.kinsta:live", "www.client.com", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(domain add kinsta) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Kinsta has not returned routing records for www.client.com yet. Open https://my.kinsta.com/sites/domains/ and follow Kinsta's domain DNS instructions for this site.", "Kinsta DNS records for client DNS:", "verification (prove ownership and TLS validation):", "TXT  _cf-custom-hostname.www.client.com  verify-token", "Domain added."} {
		if !strings.Contains(output, want) {
			t.Fatalf("domain kinsta output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "routing (point public DNS at Kinsta):") {
		t.Fatalf("domain add should not print routing records when none are known:\n%s", output)
	}
}

func TestRunDomainKinstaAddSecondaryAllowsInternalPrimary(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{
		"provider": "kinsta",
		"site_id":  "client.kinsta",
		"env_id":   "client.kinsta:live",
		"name":     "client",
		"env":      "live",
		"target":   "kinsta",
		"hostname": "client.kinsta.nonfiction.dev",
		"url":      "https://client.kinsta.nonfiction.dev",
		"path":     "/www/client/public",
		"ssh":      map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"},
		"kinsta":   map[string]any{"site_id": "ksite123", "environment_id": "kenv-live", "domain_id": "kdom-internal"},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	var capturedPlan siteDomainPlan
	oldPrepare := kinstaPrepareDomainFn
	oldPrimary := kinstaPrimaryDomainFn
	kinstaPrepareDomainFn = func(plan siteDomainPlan) (siteDomainProviderResult, error) {
		capturedPlan = plan
		return siteDomainProviderResult{Domains: []siteDomainProviderDomain{{Name: "oldwebsite.com", Role: "secondary", DomainID: "kdom-old"}}}, nil
	}
	kinstaPrimaryDomainFn = func(plan siteDomainPlan) (siteDomainProviderResult, error) {
		t.Fatalf("kinstaPrimaryDomainFn should not be called for secondary add")
		return siteDomainProviderResult{}, nil
	}
	t.Cleanup(func() {
		kinstaPrepareDomainFn = oldPrepare
		kinstaPrimaryDomainFn = oldPrimary
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "add", "client.kinsta:live", "oldwebsite.com", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(domain add secondary kinsta) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Add domain plan:", "secondary: oldwebsite.com", "redirects: https://client.kinsta.nonfiction.dev", "Domain added."} {
		if !strings.Contains(output, want) {
			t.Fatalf("domain add secondary output missing %q:\n%s", want, output)
		}
	}
	if capturedPlan.Primary || capturedPlan.Canonical != "oldwebsite.com" || capturedPlan.RedirectTarget != "client.kinsta.nonfiction.dev" {
		t.Fatalf("captured plan = %#v, want secondary oldwebsite.com redirecting to internal primary", capturedPlan)
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	record := records[0]
	if got := recordValueString(record["hostname"]); got != "client.kinsta.nonfiction.dev" {
		t.Fatalf("hostname = %q, want unchanged internal hostname", got)
	}
	if got := recordValueString(record["url"]); got != "https://client.kinsta.nonfiction.dev" {
		t.Fatalf("url = %q, want unchanged internal url", got)
	}
	if got := recordValueString(record["primary_domain"]); got != "" {
		t.Fatalf("primary_domain = %q, want unset", got)
	}
	domains := siteDomainEntryValues(record["domains"])
	if len(domains) != 1 {
		t.Fatalf("domains = %#v, want one secondary external domain", record["domains"])
	}
	domain := siteDomainEntryMap(domains[0])
	if recordValueString(domain["name"]) != "oldwebsite.com" || recordValueString(domain["role"]) != "secondary" || recordValueString(domain["management"]) != "external" || recordValueString(domain["domain_id"]) != "kdom-old" {
		t.Fatalf("domain entry = %#v, want oldwebsite.com secondary external", domain)
	}
}

func TestRunSiteDomainListShowsCachedDomainsAndFilters(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{
		{
			"provider":   "linode",
			"site_id":    "client.app1-linode",
			"env_id":     "client.app1-linode:live",
			"name":       "client",
			"env":        "live",
			"target":     "app1-linode",
			"hostname":   "client.app1-linode.nonfiction.dev",
			"url":        "https://client.app1-linode.nonfiction.dev",
			"proxy_mode": "cloudflare-strict",
			"domains":    []map[string]any{{"name": "www.client.com", "role": "primary", "management": "external", "status": "pending", "proxy_mode": "cloudflare"}, {"name": "client.com", "role": "secondary", "management": "external", "status": "pending"}},
		},
		{
			"provider":          "linode",
			"site_id":           "client.app1-linode",
			"env_id":            "client.app1-linode:staging",
			"name":              "client",
			"env":               "staging",
			"target":            "app1-linode",
			"hostname":          "staging.client.com",
			"url":               "https://staging.client.com",
			"internal_hostname": "client-staging.app1-linode.nonfiction.dev",
			"internal_url":      "https://client-staging.app1-linode.nonfiction.dev",
			"domains":           []map[string]any{{"name": "staging.client.com", "role": "primary", "management": "external", "status": "active"}},
		},
		{
			"provider":          "kinsta",
			"site_id":           "other.kinsta",
			"env_id":            "other.kinsta:live",
			"name":              "other",
			"env":               "live",
			"target":            "kinsta",
			"hostname":          "www.other.com",
			"url":               "https://www.other.com",
			"primary_domain":    "www.other.com",
			"internal_hostname": "other.kinsta.nonfiction.dev",
			"internal_url":      "https://other.kinsta.nonfiction.dev",
		},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "list"}); got != 0 {
			t.Fatalf("Run(domain list) = %d, want 0", got)
		}
	})
	for _, want := range []string{
		"domain",
		"env",
		"role",
		"management",
		"status",
		"provider",
		"proxy",
		"client.app1-linode.nonfiction.dev",
		"client-staging.app1-linode.nonfiction.dev",
		"other.kinsta.nonfiction.dev",
		"www.client.com",
		"client.com",
		"staging.client.com",
		"www.other.com",
		"client.app1-linode:live",
		"client.app1-linode:staging",
		"other.kinsta:live",
		"primary",
		"secondary",
		"internal",
		"external",
		"active",
		"pending",
		"linode",
		"kinsta",
		"cloudflare",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("domain list output missing %q:\n%s", want, output)
		}
	}
	if firstLine, _, _ := strings.Cut(output, "\n"); strings.Contains(firstLine, "site") {
		t.Fatalf("domain list header contains old site column:\n%s", output)
	}
	if firstLine, _, _ := strings.Cut(output, "\n"); strings.Contains(firstLine, "url") {
		t.Fatalf("domain list header contains url column:\n%s", output)
	}
	for _, notWant := range []string{"https://client.app1-linode.nonfiction.dev", "https://staging.client.com", "https://www.other.com"} {
		if strings.Contains(output, notWant) {
			t.Fatalf("domain list output contains URL %q:\n%s", notWant, output)
		}
	}
	for _, notWant := range []string{"type", "state", "canonical", "redirect", "default", "managed", "prepared"} {
		if strings.Contains(output, notWant) {
			t.Fatalf("domain list output contains old vocabulary %q:\n%s", notWant, output)
		}
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "client.app1-linode.nonfiction.dev") && strings.Contains(line, "cloudflare") {
			t.Fatalf("internal fallback domain should not inherit proxy mode:\n%s", output)
		}
		if strings.Contains(line, "www.client.com") && !strings.Contains(line, "cloudflare") {
			t.Fatalf("external domain should show its own proxy mode:\n%s", output)
		}
		if strings.Contains(line, "client.com") && !strings.Contains(line, "www.client.com") && strings.Contains(line, "cloudflare") {
			t.Fatalf("external domain should not inherit stale env proxy mode:\n%s", output)
		}
	}

	siteOutput := captureStdout(t, func() {
		if got := Run([]string{"domain", "list", "client.app1-linode"}); got != 0 {
			t.Fatalf("Run(domain list site) = %d, want 0", got)
		}
	})
	for _, want := range []string{"client.app1-linode.nonfiction.dev", "www.client.com", "client.com", "client-staging.app1-linode.nonfiction.dev", "staging.client.com"} {
		if !strings.Contains(siteOutput, want) {
			t.Fatalf("domain list site output missing %q:\n%s", want, siteOutput)
		}
	}
	if strings.Contains(siteOutput, "www.other.com") {
		t.Fatalf("domain list site output contains other site:\n%s", siteOutput)
	}

	envOutput := captureStdout(t, func() {
		if got := Run([]string{"domain", "list", "client.app1-linode:live"}); got != 0 {
			t.Fatalf("Run(domain list env) = %d, want 0", got)
		}
	})
	for _, want := range []string{"client.app1-linode.nonfiction.dev", "www.client.com", "client.com"} {
		if !strings.Contains(envOutput, want) {
			t.Fatalf("domain list env output missing %q:\n%s", want, envOutput)
		}
	}
	for _, notWant := range []string{"staging.client.com", "www.other.com"} {
		if strings.Contains(envOutput, notWant) {
			t.Fatalf("domain list env output contains %q:\n%s", notWant, envOutput)
		}
	}

	setupTestNFProject(t, map[string]any{"production": "client.app1-linode:live"})
	projectOutput := captureStdout(t, func() {
		if got := Run([]string{"domain", "list"}); got != 0 {
			t.Fatalf("Run(project domain list) = %d, want 0", got)
		}
	})
	for _, want := range []string{"client.app1-linode.nonfiction.dev", "www.client.com", "client.com", "client.app1-linode:live"} {
		if !strings.Contains(projectOutput, want) {
			t.Fatalf("project domain list output missing %q:\n%s", want, projectOutput)
		}
	}
	for _, notWant := range []string{"client-staging.app1-linode.nonfiction.dev", "staging.client.com", "other.kinsta.nonfiction.dev", "www.other.com", "client.app1-linode:staging", "other.kinsta:live"} {
		if strings.Contains(projectOutput, notWant) {
			t.Fatalf("project domain list output contains %q:\n%s", notWant, projectOutput)
		}
	}
}

func TestSiteDomainListDomainsPrefersExplicitPrimaryDomain(t *testing.T) {
	record := map[string]any{
		"provider":          "kinsta",
		"hostname":          "www.example.com",
		"url":               "https://www.example.com",
		"primary_domain":    "www.example.com",
		"internal_hostname": "client.kinsta.nonfiction.dev",
		"domains": []map[string]any{
			{"name": "client.kinsta.nonfiction.dev", "role": "primary", "management": "internal", "status": "active"},
			{"name": "www.example.com", "role": "secondary", "management": "external", "status": "pending"},
		},
	}

	domains := siteDomainListDomains(record)
	primary := []string{}
	for _, domain := range domains {
		if domain.role == "primary" {
			primary = append(primary, domain.name)
		}
	}
	if len(primary) != 1 || primary[0] != "www.example.com" {
		t.Fatalf("primary domains = %v, want only www.example.com", primary)
	}
}

func TestBuildSiteDomainPlanPreservesCachedProxyModesDuringPrimaryPromotion(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "public_ipv4": "203.0.113.10", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{
		"provider":          "linode",
		"site_id":           "client.app1-linode",
		"env_id":            "client.app1-linode:live",
		"name":              "client",
		"env":               "live",
		"target":            "app1-linode",
		"path":              "/var/www/sites/client/public",
		"database":          "client",
		"hostname":          "www.client.com",
		"url":               "https://www.client.com",
		"primary_domain":    "www.client.com",
		"internal_hostname": "client.app1-linode.nonfiction.dev",
		"internal_url":      "https://client.app1-linode.nonfiction.dev",
		"proxy_mode":        "cloudflare",
		"domains": []map[string]any{
			{"name": "www.client.com", "role": "primary", "management": "external", "status": "active", "proxy_mode": "159.203.49.164"},
			{"name": "client.com", "role": "secondary", "management": "external", "status": "active"},
			{"name": "cdn.client.com", "role": "secondary", "management": "external", "status": "active", "proxy_mode": "cloudflare"},
		},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	plan, err := buildSiteDomainPlan("client.app1-linode", "live", "primary", siteDomainOptions{domains: []string{"client.com"}, proxySet: true})
	if err != nil {
		t.Fatalf("buildSiteDomainPlan() error = %v", err)
	}
	entries := siteDomainCacheEntries(plan, siteDomainProviderResult{})
	byName := map[string]map[string]any{}
	for _, entry := range entries {
		byName[recordValueString(entry["name"])] = entry
	}
	if got := recordValueString(byName["client.com"]["proxy_mode"]); got != "" {
		t.Fatalf("client.com proxy_mode = %q, want direct", got)
	}
	if got := recordValueString(byName["www.client.com"]["proxy_mode"]); got != "159.203.49.164" {
		t.Fatalf("www.client.com proxy_mode = %q, want proxy IP", got)
	}
	if got := recordValueString(byName["cdn.client.com"]["proxy_mode"]); got != "cloudflare" {
		t.Fatalf("cdn.client.com proxy_mode = %q, want cloudflare", got)
	}
}

func TestBuildSiteDomainPlanPrimaryRequiresCachedExternalDomain(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "client.app1-linode", "env_id": "client.app1-linode:live", "name": "client", "env": "live", "target": "app1-linode", "path": "/var/www/sites/client/public", "database": "client", "hostname": "client.app1-linode.nonfiction.dev", "url": "https://client.app1-linode.nonfiction.dev"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	_, err := buildSiteDomainPlan("client.app1-linode", "live", "primary", siteDomainOptions{domains: []string{"www.client.com"}, proxySet: true})
	if err == nil || !strings.Contains(err.Error(), "www.client.com is not a cached external domain for client.app1-linode:live") || !strings.Contains(err.Error(), "Run nf domain add client.app1-linode:live www.client.com first") {
		t.Fatalf("buildSiteDomainPlan() error = %v, want cached-domain guidance", err)
	}
}

func TestRunDomainLinodePrimaryConfiguresVhostsAndCachesPrimary(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "public_ipv4": "203.0.113.10", "public_ipv6": "2001:db8::10", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "client.app1-linode", "env_id": "client.app1-linode:live", "name": "client", "env": "live", "target": "app1-linode", "path": "/var/www/sites/client/public", "database": "client", "hostname": "client.app1-linode.nonfiction.dev", "url": "https://client.app1-linode.nonfiction.dev", "php_version": "8.3", "domains": []map[string]any{{"name": "www.client.com", "role": "secondary", "management": "external", "status": "pending"}, {"name": "client.com", "role": "secondary", "management": "external", "status": "pending"}}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	plan, err := buildSiteDomainPlan("client.app1-linode", "live", "primary", siteDomainOptions{domains: []string{"www.client.com"}, searchReplace: true})
	if err != nil {
		t.Fatalf("buildSiteDomainPlan() error = %v", err)
	}
	script := renderLinodeDomainBindingScript(plan)
	for _, want := range []string{"/usr/local/bin/nf-refresh-domain-www-client-com", "/usr/local/bin/nf-refresh-domain-client-com", "/etc/nginx/sites-available/nf-site-domain-www-client-com", "/etc/nginx/sites-available/nf-site-domain-client-com", "server_name $domain;", "return 301 https://$domain\\$request_uri;", "add_header Cache-Control \"no-store\" always;", "return 302 https://$redirect_target\\$request_uri;", "getent ahosts \"$domain\"", "flock -n -E 75 /run/nf-certbot.lock", "Certbot is already running; timer will retry.", "certbot certonly --non-interactive --agree-tos --webroot", "nf-domain-www-client-com-tls.timer", "nf-domain-client-com-tls.timer", "option update home https://www.client.com", "option update siteurl https://www.client.com", "search-replace \"$old_url\" https://www.client.com --all-tables --skip-columns=guid", "del(.domain_state, .proxy_mode)", ".hostname = $canonical | .url = $url | .primary_domain = $canonical", "nf_linode_write_cache_snippets", "cache_zone=$(nf_linode_ensure_cache_config \"$site_path\")", "wp-content/mu-plugins/nf-linode-cache.php", "fastcgi_cache $cache_zone;", "client.com", "www.client.com"} {
		if !strings.Contains(script, want) {
			t.Fatalf("linode domain script missing %q:\n%s", want, script)
		}
	}

	var sshArgs []string
	oldRunSSH := runSSHCommandFn
	runSSHCommandFn = func(args []string) error {
		sshArgs = append([]string{}, args...)
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "primary", "client.app1-linode:live", "www.client.com", "--search-replace", "--force", "--no-proxy", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(domain primary linode) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Primary domain plan:", "provider:  linode", "target:    app1-linode", "fallback:  https://client.app1-linode.nonfiction.dev", "primary:   www.client.com", "secondary: client.com", "public DNS: no DNS records will be changed by nf", "A     www.client.com -> 203.0.113.10", "AAAA  client.com -> 2001:db8::10", "search-replace: true", "TLS: HTTP-01 certbot retry timer", "Domain launched as primary."} {
		if !strings.Contains(output, want) {
			t.Fatalf("domain linode output missing %q:\n%s", want, output)
		}
	}
	joinedArgs := strings.Join(sshArgs, " ")
	for _, want := range []string{"ssh", "-p 22", "nonfiction@app1-linode.nonfiction.dev", "sudo bash -c", "nf-refresh-domain-www-client-com", "nf-issue-domain-cert-www-client-com", "nf-refresh-domain-client-com"} {
		if !strings.Contains(joinedArgs, want) {
			t.Fatalf("ssh args missing %q:\n%#v", want, sshArgs)
		}
	}

	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("site records len = %d, want 1: %#v", len(records), records)
	}
	record := records[0]
	if got := recordValueString(record["hostname"]); got != "www.client.com" {
		t.Fatalf("hostname = %q, want primary public hostname", got)
	}
	if got := recordValueString(record["url"]); got != "https://www.client.com" {
		t.Fatalf("url = %q, want primary public URL", got)
	}
	if got := recordValueString(record["primary_domain"]); got != "www.client.com" {
		t.Fatalf("primary_domain = %q, want www.client.com", got)
	}
	if got := recordValueString(record["internal_hostname"]); got != "client.app1-linode.nonfiction.dev" {
		t.Fatalf("internal_hostname = %q, want internal fallback hostname", got)
	}
	if got := recordValueString(record["internal_url"]); got != "https://client.app1-linode.nonfiction.dev" {
		t.Fatalf("internal_url = %q, want internal fallback URL", got)
	}
	if _, ok := record["domain_state"]; ok {
		t.Fatalf("domain_state should not be written in new cache model: %#v", record)
	}
	domains, ok := record["domains"].([]any)
	if !ok || len(domains) != 2 {
		t.Fatalf("domains = %#v, want two domain entries", record["domains"])
	}
	byName := map[string]map[string]any{}
	for _, entry := range domains {
		domain := siteDomainEntryMap(entry)
		byName[recordValueString(domain["name"])] = domain
	}
	primary := byName["www.client.com"]
	secondary := byName["client.com"]
	if recordValueString(primary["role"]) != "primary" || recordValueString(primary["management"]) != "external" || recordValueString(primary["status"]) != "active" {
		t.Fatalf("primary domain = %#v", primary)
	}
	if recordValueString(secondary["role"]) != "secondary" || recordValueString(secondary["management"]) != "external" || recordValueString(secondary["status"]) != "pending" {
		t.Fatalf("secondary domain = %#v", secondary)
	}
}

func TestRunSiteDomainLinodePrepareRejectsCloudflareFull(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "public_ipv4": "203.0.113.10", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "client.app1-linode", "env_id": "client.app1-linode:live", "name": "client", "env": "live", "target": "app1-linode", "path": "/var/www/sites/client/public", "database": "client", "hostname": "client.app1-linode.nonfiction.dev", "url": "https://client.app1-linode.nonfiction.dev", "php_version": "8.3"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	_, err := buildSiteDomainPlan("client.app1-linode", "live", "add", siteDomainOptions{domains: []string{"www.client.com"}, proxyMode: "cloudflare-full"})
	if err == nil || !strings.Contains(err.Error(), "--proxy must be cloudflare or an IP address") {
		t.Fatalf("buildSiteDomainPlan() error = %v, want cloudflare-full rejected", err)
	}
}

func TestLinodeDomainCacheJQFilterUpdatesPrimary(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not available")
	}
	cmd := exec.Command("jq",
		"--arg", "site_id", "client.app1-linode",
		"--arg", "env", "live",
		"--arg", "canonical", "www.client.com",
		"--arg", "url", "https://www.client.com",
		"--arg", "internal_hostname", "client.app1-linode.nonfiction.dev",
		"--arg", "internal_url", "https://client.app1-linode.nonfiction.dev",
		"--arg", "proxy_mode", "159.203.49.164",
		"--arg", "primary", "1",
		"--argjson", "names", `["www.client.com"]`,
		"--argjson", "domains", `[{"name":"www.client.com","role":"primary","management":"external","status":"pending","proxy_mode":"159.203.49.164"}]`,
		linodeDomainCacheUpdateJQFilter(),
	)
	cmd.Stdin = strings.NewReader(`[{"site_id":"client.app1-linode","env":"live","hostname":"old.client.com","url":"https://old.client.com","domains":[{"name":"old.client.com","role":"primary","management":"external","status":"active"}],"domain_state":"pending"}]`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jq update filter failed: %v\n%s", err, out)
	}
	var records []map[string]any
	if err := json.Unmarshal(out, &records); err != nil {
		t.Fatalf("jq output decode error = %v: %s", err, out)
	}
	if got := recordValueString(records[0]["hostname"]); got != "www.client.com" {
		t.Fatalf("hostname = %q, want www.client.com", got)
	}
	if got := recordValueString(records[0]["internal_hostname"]); got != "client.app1-linode.nonfiction.dev" {
		t.Fatalf("internal_hostname = %q, want fallback hostname", got)
	}
	if _, ok := records[0]["proxy_mode"]; ok {
		t.Fatalf("top-level proxy_mode should be deleted: %#v", records[0])
	}
	if _, ok := records[0]["domain_state"]; ok {
		t.Fatalf("domain_state should be deleted: %#v", records[0])
	}
	domains, ok := records[0]["domains"].([]any)
	if !ok || len(domains) != 2 {
		t.Fatalf("domains = %#v, want old plus new domain", records[0]["domains"])
	}
	oldDomain := domains[0].(map[string]any)
	newDomain := domains[1].(map[string]any)
	if recordValueString(oldDomain["role"]) != "secondary" {
		t.Fatalf("old domain role = %#v, want demoted secondary", oldDomain)
	}
	if recordValueString(newDomain["name"]) != "www.client.com" || recordValueString(newDomain["role"]) != "primary" {
		t.Fatalf("new domain = %#v, want primary www.client.com", newDomain)
	}
	if got := recordValueString(newDomain["proxy_mode"]); got != "159.203.49.164" {
		t.Fatalf("new domain proxy_mode = %q, want proxy IP", got)
	}
}

func TestRunSiteDomainLinodePrimaryLaunchesAfterChecksPass(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "public_ipv4": "203.0.113.10", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "client.app1-linode", "env_id": "client.app1-linode:live", "name": "client", "env": "live", "target": "app1-linode", "path": "/var/www/sites/client/public", "database": "client", "hostname": "client.app1-linode.nonfiction.dev", "url": "https://client.app1-linode.nonfiction.dev", "php_version": "8.3", "domains": []map[string]any{{"name": "www.client.com", "role": "secondary", "management": "external", "status": "pending"}}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	checks := 0
	launches := 0
	oldRunSSHOutput := runSSHOutputFn
	oldRunSSH := runSSHCommandFn
	oldLookupHost := siteDomainLookupHostFn
	oldHTTP := siteDomainHTTPStatusFn
	oldHTTPS := siteDomainHTTPSStatusFn
	oldTLS := siteDomainTLSStatusFn
	runSSHOutputFn = func(args []string) ([]byte, error) {
		checks++
		if checks == 1 {
			return []byte("vhost=missing\nenabled=missing\ntimer=missing\nservice=failed\ncert=missing\n"), nil
		}
		return []byte("vhost=present\nenabled=present\ntimer=active\nservice=inactive\ncert=ready\n"), nil
	}
	runSSHCommandFn = func(args []string) error {
		launches++
		return nil
	}
	siteDomainLookupHostFn = func(host string) ([]string, error) { return []string{"203.0.113.10"}, nil }
	siteDomainHTTPStatusFn = func(domain string) siteDomainHTTPCheckResult { return siteDomainHTTPCheckResult{StatusCode: 200} }
	siteDomainHTTPSStatusFn = func(domain string) siteDomainHTTPCheckResult { return siteDomainHTTPCheckResult{StatusCode: 200} }
	siteDomainTLSStatusFn = func(domain string) siteDomainTLSCheckResult {
		return siteDomainTLSCheckResult{OK: true, NotAfter: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), Issuer: "Let's Encrypt"}
	}
	t.Cleanup(func() {
		runSSHOutputFn = oldRunSSHOutput
		runSSHCommandFn = oldRunSSH
		siteDomainLookupHostFn = oldLookupHost
		siteDomainHTTPStatusFn = oldHTTP
		siteDomainHTTPSStatusFn = oldHTTPS
		siteDomainTLSStatusFn = oldTLS
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "primary", "client.app1-linode:live", "www.client.com", "--no-proxy", "--no-search-replace", "--wait-interval", "1ns", "--wait-timeout", "1s", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(domain primary linode) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, output, []string{"Primary domain plan:", "Waiting for public domain checks.", "Approval already captured; launch will run automatically when checks pass.", "Overall: pending", "Next check in", "Rechecking public domain readiness...", "Overall: ready", "Checks ready.", "Launching primary domain now...", "Domain launched as primary."})
	if checks < 2 {
		t.Fatalf("readiness checks = %d, want at least 2", checks)
	}
	if launches != 1 {
		t.Fatalf("launches = %d, want 1", launches)
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if got := recordValueString(records[0]["hostname"]); got != "www.client.com" {
		t.Fatalf("hostname = %q, want www.client.com", got)
	}
}

func TestSiteDomainPrimaryReadinessPlanChecksOnlyRequestedPrimary(t *testing.T) {
	plan := siteDomainPlan{
		Primary:   true,
		Canonical: "client.kinsta.nonfiction.dev",
		Aliases:   []string{"client.com", "www.client.com"},
		Domains:   []string{"client.kinsta.nonfiction.dev", "client.com", "www.client.com"},
	}

	readinessPlan := siteDomainPrimaryReadinessPlan(plan)
	if got := readinessPlan.allDomains(); !reflect.DeepEqual(got, []string{"client.kinsta.nonfiction.dev"}) {
		t.Fatalf("readiness domains = %#v, want requested primary only", got)
	}
	if !reflect.DeepEqual(plan.Aliases, []string{"client.com", "www.client.com"}) {
		t.Fatalf("original plan aliases changed = %#v", plan.Aliases)
	}
}

func TestRunSiteDomainKinstaPrimaryWaitsForRequestedRoutingChain(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("KINSTA_API_KEY", "test-token")
	if err := state.SaveStateRecords("sites", []map[string]any{{
		"provider":          "kinsta",
		"site_id":           "client.kinsta",
		"env_id":            "client.kinsta:live",
		"name":              "client",
		"env":               "live",
		"target":            "kinsta",
		"hostname":          "client.kinsta.nonfiction.dev",
		"url":               "https://client.kinsta.nonfiction.dev",
		"internal_hostname": "client.kinsta.nonfiction.dev",
		"internal_url":      "https://client.kinsta.nonfiction.dev",
		"domains": []map[string]any{
			{"name": "client.kinsta.nonfiction.dev", "role": "primary", "management": "internal", "status": "active", "domain_id": "kdom-internal"},
			{"name": "client.com", "role": "secondary", "management": "external", "status": "pending", "domain_id": "kdom-apex"},
			{"name": "www.client.com", "role": "secondary", "management": "external", "status": "active", "domain_id": "kdom-www"},
			{"name": "client.kinsta.cloud", "role": "secondary", "management": "internal", "status": "active", "domain_id": "kdom-generated"},
		},
		"kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-live", "domain_id": "kdom-internal"},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/environments/kenv-live/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{
				{"id": "kdom-internal", "name": "client.kinsta.nonfiction.dev", "is_primary": true, "is_verified": true},
				{"id": "kdom-apex", "name": "client.com", "is_verified": true},
				{"id": "kdom-www", "name": "www.client.com", "is_verified": true},
				{"id": "kdom-generated", "name": "client.kinsta.cloud", "is_verified": true},
			}}})
		case "GET /sites/environments/domains/kdom-www/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{}})
		case "GET /sites/environments/domains/kdom-apex/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"pointing_records": []map[string]any{
				{"name": "client.com", "type": "A", "content": "162.159.135.42"},
				{"name": "www", "type": "CNAME", "content": "client.com"},
			}}})
		default:
			t.Fatalf("unexpected Kinsta request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KINSTA_BASE_URL", server.URL)

	oldPrimary := kinstaPrimaryDomainFn
	oldLookupHost := siteDomainLookupHostFn
	oldLookupCNAME := siteDomainLookupCNAMEFn
	oldHTTP := siteDomainHTTPStatusFn
	oldHTTPS := siteDomainHTTPSStatusFn
	oldTLS := siteDomainTLSStatusFn
	launches := 0
	kinstaPrimaryDomainFn = func(plan siteDomainPlan) (siteDomainProviderResult, error) {
		launches++
		return siteDomainProviderResult{}, nil
	}
	siteDomainLookupHostFn = func(host string) ([]string, error) {
		switch host {
		case "client.com", "www.client.com":
			return []string{"159.203.49.164"}, nil
		default:
			return nil, fmt.Errorf("unexpected host lookup %s", host)
		}
	}
	siteDomainLookupCNAMEFn = func(host string) (string, error) {
		if host == "www.client.com" {
			return "client.com.", nil
		}
		return "", fmt.Errorf("unexpected CNAME lookup %s", host)
	}
	siteDomainHTTPStatusFn = func(domain string) siteDomainHTTPCheckResult {
		return siteDomainHTTPCheckResult{StatusCode: 301, Location: "https://www.client.com/"}
	}
	siteDomainHTTPSStatusFn = func(domain string) siteDomainHTTPCheckResult { return siteDomainHTTPCheckResult{StatusCode: 200} }
	siteDomainTLSStatusFn = func(domain string) siteDomainTLSCheckResult {
		return siteDomainTLSCheckResult{OK: true, NotAfter: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), Issuer: "Let's Encrypt"}
	}
	t.Cleanup(func() {
		kinstaPrimaryDomainFn = oldPrimary
		siteDomainLookupHostFn = oldLookupHost
		siteDomainLookupCNAMEFn = oldLookupCNAME
		siteDomainHTTPStatusFn = oldHTTP
		siteDomainHTTPSStatusFn = oldHTTPS
		siteDomainTLSStatusFn = oldTLS
	})

	var output string
	stderr := captureStderr(t, func() {
		output = captureStdout(t, func() {
			if got := Run([]string{"domain", "primary", "client.kinsta:live", "www.client.com", "--no-search-replace", "--wait-interval", "1ns", "--wait-timeout", "1ns", "--execute", "--yes", "--non-interactive"}); got != 1 {
				t.Fatalf("Run(domain primary Kinsta stale DNS) = %d, want 1", got)
			}
		})
	})
	if launches != 0 {
		t.Fatalf("Kinsta primary launches = %d, want 0", launches)
	}
	for _, want := range []string{"[ok] CNAME", "name: www.client.com", "[pending] A", "name: client.com", "expected: 162.159.135.42", "got 159.203.49.164", "Overall: pending"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Kinsta stale DNS output missing %q:\n%s", want, output)
		}
	}
	if !strings.Contains(stderr, "Timed out waiting for public domain checks; primary was not changed.") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestKinstaRoutingExpectationsUseOnlyRequestedBareDomain(t *testing.T) {
	records := kinsta.DomainRecords{Pointing: []kinsta.DNSRecord{
		{Name: "client.com", Type: "A", Content: "162.159.135.42"},
		{Name: "www", Type: "CNAME", Content: "client.com"},
	}}
	expectations, complete := kinstaRoutingExpectations("client.com", []kinstaOwnedDomainRecords{{Domain: "client.com", Records: records}})
	if !complete {
		t.Fatal("bare-domain routing expectations are incomplete")
	}
	want := []siteDomainExpectedDNSRecord{{Domain: "client.com", Type: "A", Name: "client.com", Value: "162.159.135.42"}}
	if !reflect.DeepEqual(expectations, want) {
		t.Fatalf("bare-domain expectations = %#v, want %#v", expectations, want)
	}
}

func TestKinstaRoutingRecordNameHandlesRelativeSelectedAndChildNames(t *testing.T) {
	for _, test := range []struct {
		owner string
		name  string
		want  string
	}{
		{owner: "www.client.com", name: "www", want: "www.client.com"},
		{owner: "client.com", name: "www", want: "www.client.com"},
		{owner: "client.com", name: "@", want: "client.com"},
		{owner: "client.com", name: "client.com", want: "client.com"},
	} {
		if got := kinstaRoutingRecordName(test.owner, test.name); got != test.want {
			t.Errorf("kinstaRoutingRecordName(%q, %q) = %q, want %q", test.owner, test.name, got, test.want)
		}
	}
}

func TestKinstaInternalRoutingExpectationUsesGeneratedDomainAddresses(t *testing.T) {
	oldLookupHost := siteDomainLookupHostFn
	siteDomainLookupHostFn = func(host string) ([]string, error) {
		if host != "client.kinsta.cloud" {
			return nil, fmt.Errorf("unexpected host lookup %s", host)
		}
		return []string{"162.159.134.42", "162.159.135.42"}, nil
	}
	t.Cleanup(func() { siteDomainLookupHostFn = oldLookupHost })

	expectation, ok := kinstaInternalRoutingExpectation("client.kinsta.nonfiction.dev", []kinsta.Domain{{Name: "client.kinsta.cloud"}})
	if !ok {
		t.Fatal("internal Kinsta routing expectation was not derived")
	}
	want := siteDomainExpectedDNSRecord{Domain: "client.kinsta.nonfiction.dev", Type: "A", Name: "client.kinsta.nonfiction.dev", Values: []string{"162.159.134.42", "162.159.135.42"}}
	if !reflect.DeepEqual(expectation, want) {
		t.Fatalf("internal routing expectation = %#v, want %#v", expectation, want)
	}

	siteDomainLookupHostFn = func(host string) ([]string, error) {
		return []string{"162.159.135.42"}, nil
	}
	if result := checkSiteDomainDNSRecord(expectation); !result.OK {
		t.Fatalf("internal DNS result = %#v, want any generated Kinsta address to match", result)
	}
}

func TestRunSiteDomainLinodePrimaryTimeoutDoesNotLaunch(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "public_ipv4": "203.0.113.10", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "client.app1-linode", "env_id": "client.app1-linode:live", "name": "client", "env": "live", "target": "app1-linode", "path": "/var/www/sites/client/public", "database": "client", "hostname": "client.app1-linode.nonfiction.dev", "url": "https://client.app1-linode.nonfiction.dev", "php_version": "8.3", "domains": []map[string]any{{"name": "www.client.com", "role": "secondary", "management": "external", "status": "pending"}}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	oldRunSSHOutput := runSSHOutputFn
	oldRunSSH := runSSHCommandFn
	oldLookupHost := siteDomainLookupHostFn
	oldHTTP := siteDomainHTTPStatusFn
	oldHTTPS := siteDomainHTTPSStatusFn
	oldTLS := siteDomainTLSStatusFn
	runSSHOutputFn = func(args []string) ([]byte, error) {
		return []byte("vhost=missing\nenabled=missing\ntimer=missing\nservice=failed\ncert=missing\n"), nil
	}
	runSSHCommandFn = func(args []string) error {
		t.Fatalf("runSSHCommandFn should not be called before readiness passes")
		return nil
	}
	siteDomainLookupHostFn = func(host string) ([]string, error) { return []string{"203.0.113.10"}, nil }
	siteDomainHTTPStatusFn = func(domain string) siteDomainHTTPCheckResult { return siteDomainHTTPCheckResult{StatusCode: 200} }
	siteDomainHTTPSStatusFn = func(domain string) siteDomainHTTPCheckResult { return siteDomainHTTPCheckResult{StatusCode: 200} }
	siteDomainTLSStatusFn = func(domain string) siteDomainTLSCheckResult {
		return siteDomainTLSCheckResult{OK: true, NotAfter: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), Issuer: "Let's Encrypt"}
	}
	t.Cleanup(func() {
		runSSHOutputFn = oldRunSSHOutput
		runSSHCommandFn = oldRunSSH
		siteDomainLookupHostFn = oldLookupHost
		siteDomainHTTPStatusFn = oldHTTP
		siteDomainHTTPSStatusFn = oldHTTPS
		siteDomainTLSStatusFn = oldTLS
	})

	stderr := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			if got := Run([]string{"domain", "primary", "client.app1-linode:live", "www.client.com", "--no-proxy", "--no-search-replace", "--wait-interval", "1ns", "--wait-timeout", "1ns", "--execute", "--yes", "--non-interactive"}); got != 1 {
				t.Fatalf("Run(domain primary timeout) = %d, want 1", got)
			}
		})
	})
	if !strings.Contains(stderr, "Timed out waiting for public domain checks; primary was not changed.") {
		t.Fatalf("timeout stderr = %q", stderr)
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if got := recordValueString(records[0]["hostname"]); got != "client.app1-linode.nonfiction.dev" {
		t.Fatalf("hostname = %q, want unchanged internal hostname", got)
	}
}

func TestRunDomainLinodeAddCloudflareKeepsLetsEncrypt(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "public_ipv4": "203.0.113.10", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "client.app1-linode", "env_id": "client.app1-linode:live", "name": "client", "env": "live", "target": "app1-linode", "path": "/var/www/sites/client/public", "database": "client", "hostname": "client.app1-linode.nonfiction.dev", "url": "https://client.app1-linode.nonfiction.dev", "php_version": "8.3"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	plan, err := buildSiteDomainPlan("client.app1-linode", "live", "add", siteDomainOptions{domains: []string{"www.client.com"}, proxyMode: "cloudflare"})
	if err != nil {
		t.Fatalf("buildSiteDomainPlan() error = %v", err)
	}
	if plan.ProxyMode != "cloudflare" {
		t.Fatalf("ProxyMode = %q, want cloudflare", plan.ProxyMode)
	}
	script := renderLinodeDomainBindingScript(plan)
	for _, want := range []string{"--arg proxy_mode cloudflare", "expected_ips=()", "cloudflare_ranges=(173.245.48.0/20", "domain_resolves_to_cloudflare", "cloudflare_http_challenge_reachable", "not in Cloudflare IP ranges; timer will retry.", "ACME HTTP challenge path is not reachable through Cloudflare yet; timer will retry.", "certbot certonly --non-interactive --agree-tos --webroot", "nf-domain-www-client-com-tls.timer", "del(.domain_state, .proxy_mode)"} {
		if !strings.Contains(script, want) {
			t.Fatalf("cloudflare linode script missing %q:\n%s", want, script)
		}
	}
	guardIndex := strings.Index(script, "if [ ${#cloudflare_ranges[@]} -gt 0 ]; then")
	certbotIndex := strings.Index(script, "args=(certbot certonly")
	if guardIndex < 0 || certbotIndex < 0 || guardIndex > certbotIndex {
		t.Fatalf("cloudflare readiness guard must run before certbot:\n%s", script)
	}
	for _, notWant := range []string{"systemctl disable --now nf-domain-www-client-com-tls.timer"} {
		if strings.Contains(script, notWant) {
			t.Fatalf("cloudflare linode script unexpectedly contains %q:\n%s", notWant, script)
		}
	}

	oldRunSSH := runSSHCommandFn
	runSSHCommandFn = func(args []string) error { return nil }
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })
	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "add", "client.app1-linode:live", "www.client.com", "--proxy", "cloudflare", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(domain add linode cloudflare) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Add domain plan:", "secondary: www.client.com", "redirects: https://client.app1-linode.nonfiction.dev", "proxy:     cloudflare", "Cloudflare proxied A     www.client.com -> 203.0.113.10", "Cloudflare SSL/TLS mode: Full (strict)", "TLS: Cloudflare uses Full (strict) with a public Let's Encrypt origin cert", "Domain added."} {
		if !strings.Contains(output, want) {
			t.Fatalf("domain cloudflare output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "Domain launched as primary.") {
		t.Fatalf("domain add cloudflare should not launch primary:\n%s", output)
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if _, ok := records[0]["proxy_mode"]; ok {
		t.Fatalf("top-level proxy_mode should not be cached: %#v", records[0])
	}
	domains := siteDomainEntryValues(records[0]["domains"])
	if len(domains) != 1 {
		t.Fatalf("domains = %#v, want one cached domain", records[0]["domains"])
	}
	domain := siteDomainEntryMap(domains[0])
	if got := recordValueString(domain["proxy_mode"]); got != "cloudflare" {
		t.Fatalf("domain proxy_mode = %q, want cloudflare", got)
	}
}

func TestRunDomainLinodeAddProxyIPUsesWildcardOrigin(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "public_ipv4": "203.0.113.10", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "client.app1-linode", "env_id": "client.app1-linode:live", "name": "client", "env": "live", "target": "app1-linode", "path": "/var/www/sites/client/public", "database": "client", "hostname": "client.app1-linode.nonfiction.dev", "url": "https://client.app1-linode.nonfiction.dev", "php_version": "8.3"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	plan, err := buildSiteDomainPlan("client.app1-linode", "live", "add", siteDomainOptions{domains: []string{"www.client.com"}, proxyMode: "159.203.49.164"})
	if err != nil {
		t.Fatalf("buildSiteDomainPlan() error = %v", err)
	}
	if plan.ProxyMode != "159.203.49.164" {
		t.Fatalf("ProxyMode = %q, want proxy IP", plan.ProxyMode)
	}
	script := renderLinodeDomainBindingScript(plan)
	for _, want := range []string{"origin_wildcard_tls=1", "wildcard_cert_snippet=\"/etc/nginx/snippets/nf-wildcard-cert.conf\"", "printf '    include %s;\\n' \"$wildcard_cert_snippet\"", "systemctl disable --now nf-domain-www-client-com-tls.timer", "rm -f /etc/systemd/system/nf-domain-www-client-com-tls.service", "--arg proxy_mode 159.203.49.164", "if ! jq --arg site_id", "(.domains =", "del(.domain_state, .proxy_mode)"} {
		if !strings.Contains(script, want) {
			t.Fatalf("proxy IP linode script missing %q:\n%s", want, script)
		}
	}
	for _, notWant := range []string{"certbot certonly --non-interactive --agree-tos --webroot", "domain_resolves_to_cloudflare", "systemctl enable --now nf-domain-www-client-com-tls.timer"} {
		if strings.Contains(script, notWant) {
			t.Fatalf("proxy IP linode script unexpectedly contains %q:\n%s", notWant, script)
		}
	}

	oldRunSSH := runSSHCommandFn
	runSSHCommandFn = func(args []string) error { return nil }
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })
	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "add", "client.app1-linode:live", "www.client.com", "--proxy", "159.203.49.164", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(domain add linode proxy IP) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Add domain plan:", "secondary: www.client.com", "redirects: https://client.app1-linode.nonfiction.dev", "proxy:     159.203.49.164", "A     www.client.com -> 159.203.49.164", "reverse proxy origin:", "upstream: https://app1-linode.nonfiction.dev", "Host header: preserve the requested public domain", "TLS: reverse proxy terminates public HTTPS; Linode origin uses the target wildcard certificate", "Domain added."} {
		if !strings.Contains(output, want) {
			t.Fatalf("domain proxy IP output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "Domain launched as primary.") {
		t.Fatalf("domain add proxy IP should not launch primary:\n%s", output)
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if _, ok := records[0]["proxy_mode"]; ok {
		t.Fatalf("top-level proxy_mode should not be cached: %#v", records[0])
	}
	domains := siteDomainEntryValues(records[0]["domains"])
	if len(domains) != 1 {
		t.Fatalf("domains = %#v, want one cached domain", records[0]["domains"])
	}
	domain := siteDomainEntryMap(domains[0])
	if got := recordValueString(domain["proxy_mode"]); got != "159.203.49.164" {
		t.Fatalf("domain proxy_mode = %q, want proxy IP", got)
	}
}

func TestRunSiteDomainLinodeRemoveExecuteCleansPublicBindingAndResetsCache(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "public_ipv4": "203.0.113.10", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{
		"provider":          "linode",
		"site_id":           "client.app1-linode",
		"env_id":            "client.app1-linode:live",
		"name":              "client",
		"env":               "live",
		"target":            "app1-linode",
		"path":              "/var/www/sites/client/public",
		"database":          "client",
		"hostname":          "www.client.com",
		"url":               "https://www.client.com",
		"primary_domain":    "www.client.com",
		"internal_hostname": "client.app1-linode.nonfiction.dev",
		"internal_url":      "https://client.app1-linode.nonfiction.dev",
		"proxy_mode":        "cloudflare",
		"domains":           []map[string]any{{"name": "www.client.com", "role": "primary", "management": "external", "status": "active"}, {"name": "client.com", "role": "secondary", "management": "external", "status": "active"}},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	plan, err := buildSiteDomainPlan("client.app1-linode", "live", "remove", siteDomainOptions{domains: []string{"client.com"}, deleteCert: true})
	if err != nil {
		t.Fatalf("buildSiteDomainPlan() error = %v", err)
	}
	script := renderLinodeDomainBindingRemoveScript(plan, true)
	for _, want := range []string{"rm -f /etc/nginx/sites-enabled/nf-site-domain-client-com", "systemctl disable --now nf-domain-client-com-tls.timer", "certbot delete --cert-name client.com --non-interactive", "if ! jq --arg site_id", "--arg reset_primary 0", "--argjson remove_domains", "del(.domain_state, .proxy_mode)"} {
		if !strings.Contains(script, want) {
			t.Fatalf("linode remove script missing %q:\n%s", want, script)
		}
	}
	for _, notWant := range []string{"nf-site-domain-www-client-com", "certbot delete --cert-name www.client.com", "--arg reset_primary 1"} {
		if strings.Contains(script, notWant) {
			t.Fatalf("linode remove script unexpectedly contains %q:\n%s", notWant, script)
		}
	}

	var sshArgs []string
	oldRunSSH := runSSHCommandFn
	runSSHCommandFn = func(args []string) error {
		sshArgs = append([]string{}, args...)
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })
	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "remove", "client.app1-linode:live", "client.com", "--no-proxy", "--delete-cert", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(domain remove linode) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Remove domain plan:", "provider:  linode", "domains:   client.com", "provider: remove nf-managed domain vhosts, scripts, timers, and cached metadata", "TLS: delete the Let's Encrypt certificate lineage", "Domain removed."} {
		if !strings.Contains(output, want) {
			t.Fatalf("domain linode remove output missing %q:\n%s", want, output)
		}
	}
	joinedArgs := strings.Join(sshArgs, " ")
	for _, want := range []string{"ssh", "nonfiction@app1-linode.nonfiction.dev", "nf-site-domain-client-com", "certbot delete --cert-name client.com"} {
		if !strings.Contains(joinedArgs, want) {
			t.Fatalf("ssh args missing %q:\n%#v", want, sshArgs)
		}
	}
	for _, notWant := range []string{"nf-site-domain-www-client-com", "certbot delete --cert-name www.client.com"} {
		if strings.Contains(joinedArgs, notWant) {
			t.Fatalf("ssh args unexpectedly contain %q:\n%#v", notWant, sshArgs)
		}
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	record := records[0]
	if got := recordValueString(record["hostname"]); got != "www.client.com" {
		t.Fatalf("hostname = %q, want primary domain", got)
	}
	if got := recordValueString(record["url"]); got != "https://www.client.com" {
		t.Fatalf("url = %q, want primary URL", got)
	}
	if got := recordValueString(record["primary_domain"]); got != "www.client.com" {
		t.Fatalf("primary_domain = %q, want www.client.com", got)
	}
	for _, key := range []string{"domain_state", "proxy_mode"} {
		if _, ok := record[key]; ok {
			t.Fatalf("%s still present after remove: %#v", key, record)
		}
	}
	domains := siteDomainEntryValues(record["domains"])
	if len(domains) != 1 || recordValueString(siteDomainEntryMap(domains[0])["name"]) != "www.client.com" {
		t.Fatalf("domains after remove = %#v, want only primary", record["domains"])
	}
}

func TestRunSiteDomainRemoveRejectsDefaultHostname(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{
		"provider":          "linode",
		"site_id":           "client.app1-linode",
		"env_id":            "client.app1-linode:live",
		"name":              "client",
		"env":               "live",
		"target":            "app1-linode",
		"hostname":          "www.client.com",
		"url":               "https://www.client.com",
		"primary_domain":    "www.client.com",
		"internal_hostname": "client.app1-linode.nonfiction.dev",
		"internal_url":      "https://client.app1-linode.nonfiction.dev",
		"domains":           []map[string]any{{"name": "www.client.com", "role": "primary", "management": "external", "status": "active"}},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if !siteDomainIsDefaultHostname(records[0], "client.app1-linode.nonfiction.dev") {
		t.Fatalf("siteDomainIsDefaultHostname() = false, want true for internal hostname in %#v", records[0])
	}
	_, err = buildSiteDomainPlan("client.app1-linode", "live", "remove", siteDomainOptions{domains: []string{"www.client.com"}})
	if err == nil || !strings.Contains(err.Error(), "www.client.com is not a cached external secondary domain for client.app1-linode:live") {
		t.Fatalf("buildSiteDomainPlan(primary remove) error = %v, want primary-domain rejection", err)
	}
	_, err = buildSiteDomainPlan("client.app1-linode", "live", "remove", siteDomainOptions{domains: []string{"client.app1-linode.nonfiction.dev"}})
	if err == nil || !strings.Contains(err.Error(), "nf-managed default domain") {
		t.Fatalf("buildSiteDomainPlan(default remove) error = %v, want default-domain rejection", err)
	}
	oldRunSSH := runSSHCommandFn
	runSSHCommandFn = func(args []string) error {
		t.Fatalf("runSSHCommandFn should not be called for default hostname removal")
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })

	stderr := captureStderr(t, func() {
		if got := Run([]string{"domain", "remove", "client.app1-linode:live", "client.app1-linode.nonfiction.dev", "--no-proxy", "--execute", "--yes", "--non-interactive"}); got != 1 {
			t.Fatalf("Run(domain remove default hostname) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "client.app1-linode.nonfiction.dev is an nf-managed default domain for client.app1-linode:live and cannot be removed with domain remove.") {
		t.Fatalf("default removal stderr = %q", stderr)
	}
}

func TestRunSiteDomainKinstaRemoveDeletesNonPrimaryAndRefusesPrimary(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("KINSTA_API_KEY", "test-token")
	if err := state.SaveStateRecords("sites", []map[string]any{{
		"provider":       "kinsta",
		"site_id":        "client.kinsta",
		"env_id":         "client.kinsta:live",
		"name":           "client",
		"env":            "live",
		"target":         "kinsta",
		"hostname":       "www.client.com",
		"url":            "https://www.client.com",
		"primary_domain": "www.client.com",
		"path":           "/www/client/public",
		"ssh":            map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"},
		"kinsta":         map[string]any{"site_id": "ksite123", "environment_id": "kenv-live", "domain_id": "kdom-www"},
		"domains":        []map[string]any{{"name": "www.client.com", "role": "primary", "management": "external", "status": "active", "domain_id": "kdom-www"}, {"name": "old.client.com", "role": "secondary", "management": "external", "status": "active", "domain_id": "kdom-old"}},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/environments/kenv-live/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{{"id": "kdom-www", "name": "www.client.com", "is_primary": true}, {"id": "kdom-old", "name": "old.client.com"}}}})
		case "DELETE /sites/environments/kenv-live/domains":
			deleteCalls++
			var payload map[string][]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("delete domain decode error = %v", err)
			}
			if !reflect.DeepEqual(payload["domain_ids"], []string{"kdom-old"}) {
				t.Fatalf("delete payload = %#v", payload)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-delete-domain", "status": 202})
		case "GET /operations/op-delete-domain":
			_ = json.NewEncoder(w).Encode(map[string]any{"operation": map[string]any{"status": "complete"}})
		default:
			t.Fatalf("unexpected Kinsta request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KINSTA_BASE_URL", server.URL)

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "remove", "client.kinsta:live", "old.client.com", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(domain remove kinsta old) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Removing Kinsta domain old.client.com") || !strings.Contains(output, "Domain removed.") {
		t.Fatalf("domain kinsta remove output = %q, want success", output)
	}
	if deleteCalls != 1 {
		t.Fatalf("deleteCalls = %d, want 1", deleteCalls)
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	domains, ok := records[0]["domains"].([]any)
	if !ok || len(domains) != 1 || recordValueString(domains[0].(map[string]any)["name"]) != "www.client.com" {
		t.Fatalf("domains after Kinsta remove = %#v, want only primary", records[0]["domains"])
	}

	stderr := captureStderr(t, func() {
		if got := Run([]string{"domain", "remove", "client.kinsta:live", "www.client.com", "--execute", "--yes", "--non-interactive"}); got != 1 {
			t.Fatalf("Run(domain remove kinsta primary) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "www.client.com is not a cached external secondary domain for client.kinsta:live") {
		t.Fatalf("primary remove stderr = %q", stderr)
	}
}

func TestRunSiteDomainPrepareWarnsWhenDomainCachedElsewhere(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "public_ipv4": "203.0.113.10", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}, {"name": "app4-linode", "provider": "linode", "hostname": "app4-linode.nonfiction.dev", "public_ipv4": "203.0.113.40", "ssh": map[string]any{"user": "nonfiction", "host": "app4-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "linode", "site_id": "client.app1-linode", "env_id": "client.app1-linode:live", "name": "client", "env": "live", "target": "app1-linode", "path": "/var/www/sites/client/public", "database": "client", "hostname": "www.client.com", "url": "https://www.client.com", "proxy_mode": "cloudflare", "domains": []map[string]any{{"name": "www.client.com", "role": "primary", "management": "external", "status": "active", "proxy_mode": "cloudflare"}}},
		{"provider": "linode", "site_id": "client.app4-linode", "env_id": "client.app4-linode:live", "name": "client", "env": "live", "target": "app4-linode", "path": "/var/www/sites/client/public", "database": "client", "hostname": "client.app4-linode.nonfiction.dev", "url": "https://client.app4-linode.nonfiction.dev"},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "add", "client.app4-linode:live", "www.client.com", "--proxy", "cloudflare", "--dry-run", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(domain add duplicate dry-run) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Warning: www.client.com is also cached on client.app1-linode:live", "nf domain remove client.app1-linode:live www.client.com --proxy cloudflare", "Add domain plan:"} {
		if !strings.Contains(output, want) {
			t.Fatalf("duplicate domain warning output missing %q:\n%s", want, output)
		}
	}
}

func TestRunSiteDomainKinstaCheckReportsReady(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("KINSTA_API_KEY", "test-token")
	if err := state.SaveStateRecords("sites", []map[string]any{{
		"provider": "kinsta",
		"site_id":  "client.kinsta",
		"env_id":   "client.kinsta:live",
		"name":     "client",
		"env":      "live",
		"target":   "kinsta",
		"hostname": "client.kinsta.nonfiction.dev",
		"url":      "https://client.kinsta.nonfiction.dev",
		"path":     "/www/client/public",
		"ssh":      map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"},
		"kinsta":   map[string]any{"site_id": "ksite123", "environment_id": "kenv-live", "domain_id": "kdom-internal"},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/environments/kenv-live/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{{"id": "kdom-www", "name": "www.client.com", "is_primary": true, "is_verified": true, "is_pointing": true}, {"id": "kdom-apex", "name": "client.com", "is_verified": true, "is_pointing": true}}}})
		case "GET /sites/environments/domains/kdom-www/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"verification_records": []map[string]any{{"name": "_kinsta.www.client.com", "type": "TXT", "content": "verify-token"}}, "pointing_records": []map[string]any{{"name": "www.client.com", "type": "CNAME", "content": "hosting.kinsta.cloud"}}}})
		case "GET /sites/environments/domains/kdom-apex/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"pointing_records": []map[string]any{{"name": "client.com", "type": "A", "content": "203.0.113.20"}}}})
		default:
			t.Fatalf("unexpected Kinsta request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KINSTA_BASE_URL", server.URL)

	oldLookupHost := siteDomainLookupHostFn
	oldLookupTXT := siteDomainLookupTXTFn
	oldLookupCNAME := siteDomainLookupCNAMEFn
	oldHTTP := siteDomainHTTPStatusFn
	oldHTTPS := siteDomainHTTPSStatusFn
	oldTLS := siteDomainTLSStatusFn
	siteDomainLookupHostFn = func(host string) ([]string, error) {
		if host == "client.com" {
			return []string{"203.0.113.20"}, nil
		}
		return nil, fmt.Errorf("unexpected host lookup %s", host)
	}
	siteDomainLookupTXTFn = func(host string) ([]string, error) {
		if host == "_kinsta.www.client.com" {
			return []string{"verify-token"}, nil
		}
		return nil, fmt.Errorf("unexpected TXT lookup %s", host)
	}
	siteDomainLookupCNAMEFn = func(host string) (string, error) {
		if host == "www.client.com" {
			return "hosting.kinsta.cloud.", nil
		}
		return "", fmt.Errorf("unexpected CNAME lookup %s", host)
	}
	siteDomainHTTPStatusFn = func(domain string) siteDomainHTTPCheckResult {
		if domain == "client.com" {
			return siteDomainHTTPCheckResult{StatusCode: 301, Location: "https://www.client.com/"}
		}
		return siteDomainHTTPCheckResult{StatusCode: 200}
	}
	siteDomainHTTPSStatusFn = func(domain string) siteDomainHTTPCheckResult {
		return siteDomainHTTPCheckResult{StatusCode: 200}
	}
	siteDomainTLSStatusFn = func(domain string) siteDomainTLSCheckResult {
		return siteDomainTLSCheckResult{OK: true, NotAfter: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), Issuer: "Let's Encrypt"}
	}
	t.Cleanup(func() {
		siteDomainLookupHostFn = oldLookupHost
		siteDomainLookupTXTFn = oldLookupTXT
		siteDomainLookupCNAMEFn = oldLookupCNAME
		siteDomainHTTPStatusFn = oldHTTP
		siteDomainHTTPSStatusFn = oldHTTPS
		siteDomainTLSStatusFn = oldTLS
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "check", "client.kinsta:live", "www.client.com", "client.com", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(domain check kinsta) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, output, []string{"Public domain check", "provider: kinsta", "fallback: https://client.kinsta.nonfiction.dev", "domains: www.client.com, client.com", "Kinsta", "[ok] www.client.com", "role: primary", "status: present", "verification: verified", "primary: yes", "routing: pointed", "[ok] client.com", "role: secondary", "DNS", "[ok] TXT", "name: _kinsta.www.client.com", "expected: verify-token", "[ok] CNAME", "name: www.client.com", "expected: hosting.kinsta.cloud", "[ok] A", "name: client.com", "expected: 203.0.113.20", "HTTP", "url: http://client.com/", "status: 301", "location: https://www.client.com/", "HTTPS", "url: https://www.client.com/", "expires: 2026-12-31", "issuer: Let's Encrypt", "domain is primary and public checks passed", "Overall: ready"})
}

func TestRunSiteDomainKinstaCheckUsesPublicDNSWhenProviderRecordsAreMissing(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("KINSTA_API_KEY", "test-token")
	if err := state.SaveStateRecords("sites", []map[string]any{{
		"provider": "kinsta",
		"site_id":  "sanjel.kinsta",
		"env_id":   "sanjel.kinsta:live",
		"name":     "sanjel",
		"env":      "live",
		"target":   "kinsta",
		"hostname": "sanjel.kinsta.nonfiction.dev",
		"url":      "https://sanjel.kinsta.nonfiction.dev",
		"path":     "/www/sanjel/public",
		"ssh":      map[string]any{"host": "203.0.113.10", "port": "12345", "user": "sanjel"},
		"kinsta":   map[string]any{"site_id": "ksite123", "environment_id": "kenv-live", "domain_id": "kdom-internal"},
		"domains":  []map[string]any{{"name": "sanjelenergyservices.nonserver.com", "role": "secondary", "management": "external", "status": "active", "domain_id": "kdom-energy"}},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/environments/kenv-live/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{{"id": "kdom-energy", "name": "sanjelenergyservices.nonserver.com", "is_verified": true}}}})
		case "GET /sites/environments/domains/kdom-energy/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{}})
		default:
			t.Fatalf("unexpected Kinsta request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KINSTA_BASE_URL", server.URL)

	oldLookupHost := siteDomainLookupHostFn
	oldHTTP := siteDomainHTTPStatusFn
	oldHTTPS := siteDomainHTTPSStatusFn
	oldTLS := siteDomainTLSStatusFn
	siteDomainLookupHostFn = func(host string) ([]string, error) {
		if host == "sanjelenergyservices.nonserver.com" {
			return []string{"104.18.1.2", "104.18.2.2"}, nil
		}
		return nil, fmt.Errorf("unexpected host lookup %s", host)
	}
	siteDomainHTTPStatusFn = func(domain string) siteDomainHTTPCheckResult {
		return siteDomainHTTPCheckResult{StatusCode: 301, Location: "https://sanjel.kinsta.nonfiction.dev/"}
	}
	siteDomainHTTPSStatusFn = func(domain string) siteDomainHTTPCheckResult {
		return siteDomainHTTPCheckResult{StatusCode: 301, Location: "https://sanjel.kinsta.nonfiction.dev/"}
	}
	siteDomainTLSStatusFn = func(domain string) siteDomainTLSCheckResult {
		return siteDomainTLSCheckResult{OK: true, NotAfter: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), Issuer: "WE1"}
	}
	t.Cleanup(func() {
		siteDomainLookupHostFn = oldLookupHost
		siteDomainHTTPStatusFn = oldHTTP
		siteDomainHTTPSStatusFn = oldHTTPS
		siteDomainTLSStatusFn = oldTLS
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "check", "sanjel.kinsta:live", "sanjelenergyservices.nonserver.com", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(domain check kinsta missing provider records) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, output, []string{"Public domain check", "provider: kinsta", "domain: sanjelenergyservices.nonserver.com", "Kinsta", "[ok] sanjelenergyservices.nonserver.com", "role: secondary", "status: present", "verification: verified", "routing: check public DNS below", "DNS", "[unchecked] provider DNS records", "result: unavailable from provider", "fallback: checking public DNS resolution", "[ok] public DNS", "domain: sanjelenergyservices.nonserver.com", "resolves to: 104.18.1.2, 104.18.2.2", "HTTP", "url: http://sanjelenergyservices.nonserver.com/", "status: 301", "location: https://sanjel.kinsta.nonfiction.dev/", "HTTPS", "url: https://sanjelenergyservices.nonserver.com/", "expires: 2026-09-20", "issuer: WE1", "status: 301", "domain is ready as a secondary redirect", "optional primary promotion: nf domain primary sanjel.kinsta:live", "sanjelenergyservices.nonserver.com", "Overall: ready"})
}

func TestRunSiteDomainKinstaCheckMissingProviderRecordsStillRequiresPublicDNS(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("KINSTA_API_KEY", "test-token")
	if err := state.SaveStateRecords("sites", []map[string]any{{
		"provider": "kinsta",
		"site_id":  "sanjel.kinsta",
		"env_id":   "sanjel.kinsta:live",
		"name":     "sanjel",
		"env":      "live",
		"target":   "kinsta",
		"hostname": "sanjel.kinsta.nonfiction.dev",
		"url":      "https://sanjel.kinsta.nonfiction.dev",
		"path":     "/www/sanjel/public",
		"ssh":      map[string]any{"host": "203.0.113.10", "port": "12345", "user": "sanjel"},
		"kinsta":   map[string]any{"site_id": "ksite123", "environment_id": "kenv-live", "domain_id": "kdom-internal"},
		"domains":  []map[string]any{{"name": "missing.nonserver.com", "role": "secondary", "management": "external", "status": "pending", "domain_id": "kdom-missing"}},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/environments/kenv-live/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{{"id": "kdom-missing", "name": "missing.nonserver.com", "is_verified": true}}}})
		case "GET /sites/environments/domains/kdom-missing/verification-records":
			http.Error(w, "upstream timeout", http.StatusGatewayTimeout)
		default:
			t.Fatalf("unexpected Kinsta request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KINSTA_BASE_URL", server.URL)

	oldLookupHost := siteDomainLookupHostFn
	oldHTTP := siteDomainHTTPStatusFn
	oldHTTPS := siteDomainHTTPSStatusFn
	oldTLS := siteDomainTLSStatusFn
	siteDomainLookupHostFn = func(host string) ([]string, error) {
		return nil, fmt.Errorf("lookup %s: no such host", host)
	}
	siteDomainHTTPStatusFn = func(domain string) siteDomainHTTPCheckResult {
		return siteDomainHTTPCheckResult{Error: fmt.Sprintf("lookup %s: no such host", domain)}
	}
	siteDomainHTTPSStatusFn = func(domain string) siteDomainHTTPCheckResult {
		return siteDomainHTTPCheckResult{Error: fmt.Sprintf("lookup %s: no such host", domain)}
	}
	siteDomainTLSStatusFn = func(domain string) siteDomainTLSCheckResult {
		return siteDomainTLSCheckResult{Error: fmt.Sprintf("lookup %s: no such host", domain)}
	}
	t.Cleanup(func() {
		siteDomainLookupHostFn = oldLookupHost
		siteDomainHTTPStatusFn = oldHTTP
		siteDomainHTTPSStatusFn = oldHTTPS
		siteDomainTLSStatusFn = oldTLS
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "check", "sanjel.kinsta:live", "missing.nonserver.com", "--non-interactive"}); got != 2 {
			t.Fatalf("Run(domain check kinsta missing public DNS) = %d, want 2", got)
		}
	})
	assertContainsInOrder(t, output, []string{"Public domain check", "provider: kinsta", "domain: missing.nonserver.com", "Kinsta", "[ok] missing.nonserver.com", "provider DNS records: unavailable", "detail: Kinsta GET /sites/environments/domains/kdom-missing/verification-records", "returned 504 Gateway Timeout: upstream timeout", "DNS", "[unchecked] provider DNS records", "fallback: checking public DNS resolution", "[pending] public DNS", "domain: missing.nonserver.com", "result: lookup failed", "detail: lookup missing.nonserver.com: no such host", "HTTP", "url: http://missing.nonserver.com/", "result: lookup failed", "HTTPS", "url: https://missing.nonserver.com/", "result: lookup failed", "point public DNS records at Kinsta, then run nf domain check again", "Overall: pending"})
}

func TestRunSiteDomainKinstaCheckReportsVerifiedNotPointed(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("KINSTA_API_KEY", "test-token")
	if err := state.SaveStateRecords("sites", []map[string]any{{
		"provider": "kinsta",
		"site_id":  "sanjel.kinsta",
		"env_id":   "sanjel.kinsta:live",
		"name":     "sanjel",
		"env":      "live",
		"target":   "kinsta",
		"hostname": "sanjel.kinsta.nonfiction.dev",
		"url":      "https://sanjel.kinsta.nonfiction.dev",
		"path":     "/www/sanjel/public",
		"ssh":      map[string]any{"host": "203.0.113.10", "port": "12345", "user": "sanjel"},
		"kinsta":   map[string]any{"site_id": "ksite123", "environment_id": "kenv-live", "domain_id": "kdom-internal"},
		"domains":  []map[string]any{{"name": "sanjelcanada.nonserver.com", "role": "secondary", "management": "external", "status": "pending", "domain_id": "kdom-canada"}},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/environments/kenv-live/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{{"id": "kdom-canada", "name": "sanjelcanada.nonserver.com"}}}})
		case "GET /sites/environments/domains/kdom-canada/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"pointing_records": []map[string]any{{"name": "sanjelcanada.nonserver.com", "type": "A", "content": "162.159.134.42"}, {"name": "www", "type": "CNAME", "content": "sanjelcanada.nonserver.com"}}}})
		case "POST /":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"validateVerificationRecordsOfSiteDomains": []map[string]any{{"idSiteDomain": "kdom-canada", "isValid": true}}}})
		default:
			t.Fatalf("unexpected Kinsta request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KINSTA_BASE_URL", server.URL)
	t.Setenv("KINSTA_GRAPHQL_URL", server.URL)

	oldLookupHost := siteDomainLookupHostFn
	oldLookupCNAME := siteDomainLookupCNAMEFn
	oldHTTP := siteDomainHTTPStatusFn
	oldHTTPS := siteDomainHTTPSStatusFn
	oldTLS := siteDomainTLSStatusFn
	siteDomainLookupHostFn = func(host string) ([]string, error) {
		return nil, fmt.Errorf("lookup %s: no such host", host)
	}
	siteDomainLookupCNAMEFn = func(host string) (string, error) {
		return "", fmt.Errorf("lookup %s: no such host", host)
	}
	siteDomainHTTPStatusFn = func(domain string) siteDomainHTTPCheckResult {
		return siteDomainHTTPCheckResult{Error: fmt.Sprintf("lookup %s: no such host", domain)}
	}
	siteDomainHTTPSStatusFn = func(domain string) siteDomainHTTPCheckResult {
		return siteDomainHTTPCheckResult{Error: fmt.Sprintf("lookup %s: no such host", domain)}
	}
	siteDomainTLSStatusFn = func(domain string) siteDomainTLSCheckResult {
		return siteDomainTLSCheckResult{Error: fmt.Sprintf("lookup %s: no such host", domain)}
	}
	t.Cleanup(func() {
		siteDomainLookupHostFn = oldLookupHost
		siteDomainLookupCNAMEFn = oldLookupCNAME
		siteDomainHTTPStatusFn = oldHTTP
		siteDomainHTTPSStatusFn = oldHTTPS
		siteDomainTLSStatusFn = oldTLS
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "check", "sanjel.kinsta:live", "sanjelcanada.nonserver.com", "--non-interactive"}); got != 2 {
			t.Fatalf("Run(domain check kinsta verified not pointed) = %d, want 2", got)
		}
	})
	assertContainsInOrder(t, output, []string{"Public domain check", "provider: kinsta", "domain: sanjelcanada.nonserver.com", "Kinsta", "[ok] sanjelcanada.nonserver.com", "verification: verified", "routing: check public DNS below", "DNS", "[pending] A", "name: sanjelcanada.nonserver.com", "expected: 162.159.134.42", "result: lookup failed", "[pending] CNAME", "name: www", "expected: sanjelcanada.nonserver.com", "result: lookup failed", "HTTP", "url: http://sanjelcanada.nonserver.com/", "result: lookup failed", "HTTPS", "url: https://sanjelcanada.nonserver.com/", "result: lookup failed", "point public DNS records at Kinsta, then run nf domain check again", "Overall: pending"})
}

func TestRunSiteDomainLinodeCheckReportsPending(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "public_ipv4": "203.0.113.10", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "client.app1-linode", "env_id": "client.app1-linode:live", "name": "client", "env": "live", "target": "app1-linode", "path": "/var/www/sites/client/public", "database": "client", "hostname": "client.app1-linode.nonfiction.dev", "url": "https://client.app1-linode.nonfiction.dev", "php_version": "8.3"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	var sshArgs []string
	oldRunSSHOutput := runSSHOutputFn
	oldLookupHost := siteDomainLookupHostFn
	oldHTTP := siteDomainHTTPStatusFn
	oldTLS := siteDomainTLSStatusFn
	runSSHOutputFn = func(args []string) ([]byte, error) {
		sshArgs = append([]string{}, args...)
		return []byte("vhost=present\nenabled=present\ntimer=active\nservice=failed\ncert=missing\n"), nil
	}
	siteDomainLookupHostFn = func(host string) ([]string, error) {
		return []string{"198.51.100.9"}, nil
	}
	siteDomainHTTPStatusFn = func(domain string) siteDomainHTTPCheckResult {
		if domain == "www.client.com" {
			return siteDomainHTTPCheckResult{StatusCode: 301, Location: "https://client.app1-linode.nonfiction.dev/"}
		}
		return siteDomainHTTPCheckResult{StatusCode: 200}
	}
	siteDomainTLSStatusFn = func(domain string) siteDomainTLSCheckResult {
		return siteDomainTLSCheckResult{Error: "certificate is not ready"}
	}
	t.Cleanup(func() {
		runSSHOutputFn = oldRunSSHOutput
		siteDomainLookupHostFn = oldLookupHost
		siteDomainHTTPStatusFn = oldHTTP
		siteDomainTLSStatusFn = oldTLS
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "check", "client.app1-linode:live", "www.client.com", "client.com", "--no-proxy", "--non-interactive"}); got != 2 {
			t.Fatalf("Run(domain check linode) = %d, want 2", got)
		}
	})
	assertContainsInOrder(t, output, []string{"Public domain check", "provider: linode", "target: app1-linode", "domains: www.client.com, client.com", "Linode target", "[ok] nginx vhost", "status: present", "[ok] certbot timer", "status: active", "[pending] certificate", "status: missing", "DNS", "[pending] A", "name: www.client.com", "expected: 203.0.113.10", "detail: got 198.51.100.9", "HTTP", "url: http://www.client.com/", "status: 301", "location: https://client.app1-linode.nonfiction.dev/", "HTTPS", "url: https://www.client.com/", "result: TLS failed", "detail: certificate is not ready", "wait for pending checks", "Overall: pending"})
	joinedArgs := strings.Join(sshArgs, " ")
	for _, want := range []string{"ssh", "nonfiction@app1-linode.nonfiction.dev", "nf-site-domain-www-client-com", "nf-site-domain-client-com", "nf-domain-www-client-com-tls.timer", "nf-domain-client-com-tls.timer"} {
		if !strings.Contains(joinedArgs, want) {
			t.Fatalf("ssh args missing %q:\n%#v", want, sshArgs)
		}
	}
}

func TestRunSiteDomainLinodeCheckProxyIPUsesProxyDNS(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "public_ipv4": "203.0.113.10", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "client.app1-linode", "env_id": "client.app1-linode:live", "name": "client", "env": "live", "target": "app1-linode", "path": "/var/www/sites/client/public", "database": "client", "hostname": "client.app1-linode.nonfiction.dev", "url": "https://client.app1-linode.nonfiction.dev", "php_version": "8.3", "proxy_mode": "159.203.49.164"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	oldRunSSHOutput := runSSHOutputFn
	oldLookupHost := siteDomainLookupHostFn
	oldHTTP := siteDomainHTTPStatusFn
	oldHTTPS := siteDomainHTTPSStatusFn
	oldTLS := siteDomainTLSStatusFn
	oldOriginTLS := siteDomainOriginTLSFn
	runSSHOutputFn = func(args []string) ([]byte, error) {
		return []byte("vhost=present\nenabled=present\ntimer=missing\nservice=missing\ncert=missing\n"), nil
	}
	siteDomainLookupHostFn = func(host string) ([]string, error) {
		return []string{"159.203.49.164"}, nil
	}
	siteDomainHTTPStatusFn = func(domain string) siteDomainHTTPCheckResult {
		return siteDomainHTTPCheckResult{StatusCode: 200}
	}
	siteDomainHTTPSStatusFn = func(domain string) siteDomainHTTPCheckResult {
		return siteDomainHTTPCheckResult{StatusCode: 200}
	}
	siteDomainTLSStatusFn = func(domain string) siteDomainTLSCheckResult {
		return siteDomainTLSCheckResult{OK: true, NotAfter: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), Issuer: "Let's Encrypt"}
	}
	siteDomainOriginTLSFn = func(domain, origin string) siteDomainTLSCheckResult {
		t.Fatalf("origin TLS should not be checked for reverse-proxy IP mode")
		return siteDomainTLSCheckResult{}
	}
	t.Cleanup(func() {
		runSSHOutputFn = oldRunSSHOutput
		siteDomainLookupHostFn = oldLookupHost
		siteDomainHTTPStatusFn = oldHTTP
		siteDomainHTTPSStatusFn = oldHTTPS
		siteDomainTLSStatusFn = oldTLS
		siteDomainOriginTLSFn = oldOriginTLS
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "check", "client.app1-linode:live", "www.client.com", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(domain check linode proxy IP) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, output, []string{"Public domain check", "proxy: 159.203.49.164", "Linode target", "[unchecked] proxy mode", "mode: reverse proxy 159.203.49.164", "[ok] nginx vhost", "[unchecked] certbot timer", "status: missing", "[unchecked] certificate", "status: missing", "DNS", "[ok] A", "name: www.client.com", "expected: 159.203.49.164", "result: matches expected", "HTTPS", "url: https://www.client.com/", "expires: 2026-12-31", "issuer: Let's Encrypt", "domain checks passed", "Overall: ready"})
	if strings.Contains(output, "Origin HTTPS") {
		t.Fatalf("proxy IP check should not run origin TLS checks:\n%s", output)
	}
}

func TestRunSiteDomainLinodeCheckCachedSecondaryIsReady(t *testing.T) {
	setupLinodeDomainPromptFixture(t, map[string]any{
		"hostname":          "www.client.com",
		"url":               "https://www.client.com",
		"primary_domain":    "www.client.com",
		"internal_hostname": "client.app1-linode.nonfiction.dev",
		"internal_url":      "https://client.app1-linode.nonfiction.dev",
		"domains": []map[string]any{
			{"name": "www.client.com", "role": "primary", "management": "external", "status": "active"},
			{"name": "client.com", "role": "secondary", "management": "external", "status": "active"},
		},
	})

	oldRunSSHOutput := runSSHOutputFn
	oldLookupHost := siteDomainLookupHostFn
	oldHTTP := siteDomainHTTPStatusFn
	oldHTTPS := siteDomainHTTPSStatusFn
	oldTLS := siteDomainTLSStatusFn
	runSSHOutputFn = func(args []string) ([]byte, error) {
		return []byte("vhost=present\nenabled=present\ntimer=active\nservice=inactive\ncert=ready\n"), nil
	}
	siteDomainLookupHostFn = func(host string) ([]string, error) { return []string{"203.0.113.10"}, nil }
	siteDomainHTTPStatusFn = func(domain string) siteDomainHTTPCheckResult {
		return siteDomainHTTPCheckResult{StatusCode: 302, Location: "https://www.client.com/"}
	}
	siteDomainHTTPSStatusFn = func(domain string) siteDomainHTTPCheckResult { return siteDomainHTTPCheckResult{StatusCode: 200} }
	siteDomainTLSStatusFn = func(domain string) siteDomainTLSCheckResult {
		return siteDomainTLSCheckResult{OK: true, NotAfter: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), Issuer: "Let's Encrypt"}
	}
	t.Cleanup(func() {
		runSSHOutputFn = oldRunSSHOutput
		siteDomainLookupHostFn = oldLookupHost
		siteDomainHTTPStatusFn = oldHTTP
		siteDomainHTTPSStatusFn = oldHTTPS
		siteDomainTLSStatusFn = oldTLS
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "check", "client.app1-linode:live", "client.com", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(domain check cached secondary) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, output, []string{"Public domain check", "domain: client.com", "DNS", "[ok] A", "name: client.com", "expected: 203.0.113.10", "HTTP", "url: http://client.com/", "status: 302", "location: https://www.client.com/", "HTTPS", "url: https://client.com/", "expires: 2026-12-31", "issuer: Let's Encrypt", "domain is ready as a secondary redirect", "optional primary promotion: nf domain primary client.app1-linode:live client.com", "Overall: ready"})
	if strings.Contains(output, "ready for primary") {
		t.Fatalf("cached secondary check should not imply primary is required:\n%s", output)
	}
}

func TestRunSiteDomainLinodeCheckCachedSecondaryRedirectingToInternalPrimaryIsReady(t *testing.T) {
	setupLinodeDomainPromptFixture(t, map[string]any{
		"hostname":          "client.app1-linode.nonfiction.dev",
		"url":               "https://client.app1-linode.nonfiction.dev",
		"internal_hostname": "client.app1-linode.nonfiction.dev",
		"internal_url":      "https://client.app1-linode.nonfiction.dev",
		"domains": []map[string]any{
			{"name": "client.com", "role": "secondary", "management": "external", "status": "active"},
		},
	})

	oldRunSSHOutput := runSSHOutputFn
	oldLookupHost := siteDomainLookupHostFn
	oldHTTP := siteDomainHTTPStatusFn
	oldHTTPS := siteDomainHTTPSStatusFn
	oldTLS := siteDomainTLSStatusFn
	runSSHOutputFn = func(args []string) ([]byte, error) {
		return []byte("vhost=present\nenabled=present\ntimer=active\nservice=inactive\ncert=ready\n"), nil
	}
	siteDomainLookupHostFn = func(host string) ([]string, error) { return []string{"203.0.113.10"}, nil }
	siteDomainHTTPStatusFn = func(domain string) siteDomainHTTPCheckResult {
		return siteDomainHTTPCheckResult{StatusCode: 302, Location: "https://client.app1-linode.nonfiction.dev/"}
	}
	siteDomainHTTPSStatusFn = func(domain string) siteDomainHTTPCheckResult {
		return siteDomainHTTPCheckResult{StatusCode: 302, Location: "https://client.app1-linode.nonfiction.dev/"}
	}
	siteDomainTLSStatusFn = func(domain string) siteDomainTLSCheckResult {
		return siteDomainTLSCheckResult{OK: true, NotAfter: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), Issuer: "Let's Encrypt"}
	}
	t.Cleanup(func() {
		runSSHOutputFn = oldRunSSHOutput
		siteDomainLookupHostFn = oldLookupHost
		siteDomainHTTPStatusFn = oldHTTP
		siteDomainHTTPSStatusFn = oldHTTPS
		siteDomainTLSStatusFn = oldTLS
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "check", "client.app1-linode:live", "client.com", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(domain check cached secondary internal primary) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, output, []string{"Public domain check", "current: https://client.app1-linode.nonfiction.dev", "fallback: https://client.app1-linode.nonfiction.dev", "domain: client.com", "DNS", "[ok] A", "name: client.com", "HTTP", "url: http://client.com/", "status: 302", "location: https://client.app1-linode.nonfiction.dev/", "HTTPS", "url: https://client.com/", "expires: 2026-12-31", "issuer: Let's Encrypt", "status: 302", "domain is ready as a secondary redirect", "optional primary promotion: nf domain primary client.app1-linode:live client.com", "Overall: ready"})
	for _, unwanted := range []string{"redirects to internal hostname", "Overall: pending"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("cached secondary internal-primary check should not contain %q:\n%s", unwanted, output)
		}
	}
}

func TestRunSiteDomainLinodeCheckRejectsHTTPSInternalRedirect(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "public_ipv4": "203.0.113.10", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{
		"provider":          "linode",
		"site_id":           "client.app1-linode",
		"env_id":            "client.app1-linode:live",
		"name":              "client",
		"env":               "live",
		"target":            "app1-linode",
		"path":              "/var/www/sites/client/public",
		"database":          "client",
		"hostname":          "www.client.com",
		"url":               "https://www.client.com",
		"primary_domain":    "www.client.com",
		"internal_hostname": "client.app1-linode.nonfiction.dev",
		"internal_url":      "https://client.app1-linode.nonfiction.dev",
		"php_version":       "8.3",
		"proxy_mode":        "159.203.49.164",
		"domains":           []map[string]any{{"name": "www.client.com", "role": "primary", "management": "external", "status": "active", "proxy_mode": "159.203.49.164"}},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	oldRunSSHOutput := runSSHOutputFn
	oldLookupHost := siteDomainLookupHostFn
	oldHTTP := siteDomainHTTPStatusFn
	oldHTTPS := siteDomainHTTPSStatusFn
	oldTLS := siteDomainTLSStatusFn
	runSSHOutputFn = func(args []string) ([]byte, error) {
		return []byte("vhost=present\nenabled=present\ntimer=not-required\nservice=not-required\ncert=wildcard-origin\n"), nil
	}
	siteDomainLookupHostFn = func(host string) ([]string, error) {
		return []string{"159.203.49.164"}, nil
	}
	siteDomainHTTPStatusFn = func(domain string) siteDomainHTTPCheckResult {
		return siteDomainHTTPCheckResult{StatusCode: 200}
	}
	siteDomainHTTPSStatusFn = func(domain string) siteDomainHTTPCheckResult {
		return siteDomainHTTPCheckResult{StatusCode: 302, Location: "https://client.app1-linode.nonfiction.dev/"}
	}
	siteDomainTLSStatusFn = func(domain string) siteDomainTLSCheckResult {
		return siteDomainTLSCheckResult{OK: true, NotAfter: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), Issuer: "Let's Encrypt"}
	}
	t.Cleanup(func() {
		runSSHOutputFn = oldRunSSHOutput
		siteDomainLookupHostFn = oldLookupHost
		siteDomainHTTPStatusFn = oldHTTP
		siteDomainHTTPSStatusFn = oldHTTPS
		siteDomainTLSStatusFn = oldTLS
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "check", "client.app1-linode:live", "www.client.com", "--non-interactive"}); got != 2 {
			t.Fatalf("Run(domain check linode proxy IP internal redirect) = %d, want 2", got)
		}
	})
	for _, want := range []string{"[pending] HTTPS", "url: https://www.client.com/", "result: redirects to internal hostname", "internal hostname: client.app1-linode.nonfiction.dev", "Overall: pending"} {
		if !strings.Contains(output, want) {
			t.Fatalf("domain check output missing %q:\n%s", want, output)
		}
	}
}

func setupLinodeDomainPromptFixture(t *testing.T, extra map[string]any) {
	t.Helper()
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "public_ipv4": "203.0.113.10", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	record := map[string]any{"provider": "linode", "site_id": "client.app1-linode", "env_id": "client.app1-linode:live", "name": "client", "env": "live", "target": "app1-linode", "path": "/var/www/sites/client/public", "database": "client", "hostname": "client.app1-linode.nonfiction.dev", "url": "https://client.app1-linode.nonfiction.dev", "php_version": "8.3"}
	for key, value := range extra {
		record[key] = value
	}
	if err := state.SaveStateRecords("sites", []map[string]any{record}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
}

func setupTestNFProject(t *testing.T, remotes map[string]any) string {
	t.Helper()
	return setupTestNFProjectWithMetadata(t, map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}},
		"remotes":   remotes,
	})
}

func setupTestNFProjectWithMetadata(t *testing.T, project map[string]any) string {
	t.Helper()
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
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
		t.Fatalf("Chdir(project) error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	return repoRoot
}

func withInteractiveDomainPrompts(t *testing.T) func() {
	t.Helper()
	oldInteractive := siteIsInteractiveFn
	oldSiteSelect := siteSelectFn
	oldDomainSelect := siteDomainSelectFn
	oldDomainMultiSelect := siteDomainMultiSelectFn
	oldDomainMultiSelectNone := siteDomainMultiSelectNoneFn
	oldDomainPrompt := siteDomainPromptStringFn
	siteIsInteractiveFn = func() bool { return true }
	return func() {
		siteIsInteractiveFn = oldInteractive
		siteSelectFn = oldSiteSelect
		siteDomainSelectFn = oldDomainSelect
		siteDomainMultiSelectFn = oldDomainMultiSelect
		siteDomainMultiSelectNoneFn = oldDomainMultiSelectNone
		siteDomainPromptStringFn = oldDomainPrompt
	}
}

func TestRunSiteDomainPrimaryWithoutArgsPromptsEnvDomainProxyAndSearch(t *testing.T) {
	setupLinodeDomainPromptFixture(t, map[string]any{"domains": []map[string]any{{"name": "www.client.com", "role": "secondary", "management": "external", "status": "pending"}}})
	t.Cleanup(withInteractiveDomainPrompts(t))

	var envTitle string
	var envOptions []ui.SelectOption
	siteSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		envTitle = title
		envOptions = append([]ui.SelectOption(nil), options...)
		return "client.app1-linode:live", nil
	}
	selectCalls := []string{}
	siteDomainSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectCalls = append(selectCalls, title)
		switch title {
		case "Choose a domain to make primary":
			return "www.client.com", nil
		case "Choose Linode proxy mode":
			return "direct", nil
		case "Database search-replace":
			return "yes", nil
		default:
			t.Fatalf("unexpected select %q with options %#v", title, options)
			return "", nil
		}
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "primary", "--dry-run"}); got != 0 {
			t.Fatalf("Run(domain primary picker) = %d, want 0", got)
		}
	})
	if envTitle != "Choose an env or remote for domain primary" {
		t.Fatalf("env picker title = %q", envTitle)
	}
	if len(envOptions) != 1 || envOptions[0].Value != "client.app1-linode:live" {
		t.Fatalf("env picker options = %#v", envOptions)
	}
	for _, want := range []string{"Choose a domain to make primary", "Choose Linode proxy mode", "Database search-replace"} {
		if !slices.Contains(selectCalls, want) {
			t.Fatalf("select calls = %#v, missing %q", selectCalls, want)
		}
	}
	for _, want := range []string{"Primary domain plan:", "primary:   www.client.com", "proxy:     none", "search-replace: true", "mode:      dry-run"} {
		if !strings.Contains(output, want) {
			t.Fatalf("domain primary picker output missing %q:\n%s", want, output)
		}
	}
}

func TestRunSiteDomainPrimaryKinstaPickerIncludesIdentity(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{
		"provider":          "kinsta",
		"site_id":           "client.kinsta",
		"env_id":            "client.kinsta:live",
		"name":              "client",
		"env":               "live",
		"target":            "kinsta",
		"hostname":          "www.example.com",
		"url":               "https://www.example.com",
		"primary_domain":    "www.example.com",
		"internal_hostname": "client.kinsta.nonfiction.dev",
		"internal_url":      "https://client.kinsta.nonfiction.dev",
		"domains": []map[string]any{
			{"name": "client.kinsta.nonfiction.dev", "role": "secondary", "management": "internal", "status": "active"},
			{"name": "www.example.com", "role": "primary", "management": "external", "status": "active"},
			{"name": "client.kinsta.cloud", "role": "secondary", "management": "internal", "status": "active"},
		},
		"kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-live", "domain_id": "public-domain"},
	}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	t.Cleanup(withInteractiveDomainPrompts(t))

	var primaryOptions []ui.SelectOption
	siteDomainSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		switch title {
		case "Choose a domain to make primary":
			primaryOptions = append([]ui.SelectOption(nil), options...)
			return "client.kinsta.nonfiction.dev", nil
		case "Database search-replace":
			return "no", nil
		default:
			t.Fatalf("unexpected select %q", title)
			return "", nil
		}
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "primary", "client.kinsta:live", "--dry-run"}); got != 0 {
			t.Fatalf("Run(domain primary Kinsta identity picker) = %d, want 0", got)
		}
	})
	values := []string{}
	for _, option := range primaryOptions {
		values = append(values, option.Value)
	}
	if !reflect.DeepEqual(values, []string{"client.kinsta.nonfiction.dev", "www.example.com"}) {
		t.Fatalf("primary picker values = %#v", values)
	}
	for _, want := range []string{"Primary domain plan:", "primary:   client.kinsta.nonfiction.dev", "provider:  kinsta", "mode:      dry-run"} {
		if !strings.Contains(output, want) {
			t.Fatalf("domain primary Kinsta identity output missing %q:\n%s", want, output)
		}
	}
}

func TestRunSiteDomainPrimaryInProjectPromptsOnlyConfiguredRemotes(t *testing.T) {
	setupLinodeDomainPromptFixture(t, map[string]any{"domains": []map[string]any{{"name": "www.client.com", "role": "secondary", "management": "external", "status": "pending"}}})
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	records = append(records, map[string]any{"provider": "kinsta", "site_id": "other.kinsta", "env_id": "other.kinsta:live", "name": "other", "env": "live", "target": "kinsta", "hostname": "www.other.com", "url": "https://www.other.com"})
	if err := state.SaveStateRecords("sites", records); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	setupTestNFProject(t, map[string]any{"test": "client.app1-linode:live"})
	t.Cleanup(withInteractiveDomainPrompts(t))

	var envOptions []ui.SelectOption
	siteSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		if title != "Choose an env or remote for domain primary" {
			t.Fatalf("env picker title = %q", title)
		}
		envOptions = append([]ui.SelectOption(nil), options...)
		return "test", nil
	}
	siteDomainSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		switch title {
		case "Choose a domain to make primary":
			return "www.client.com", nil
		case "Choose Linode proxy mode":
			return "direct", nil
		case "Database search-replace":
			return "no", nil
		default:
			t.Fatalf("unexpected select %q", title)
			return "", nil
		}
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "primary", "--dry-run"}); got != 0 {
			t.Fatalf("Run(project domain primary picker) = %d, want 0", got)
		}
	})
	if len(envOptions) != 1 {
		t.Fatalf("env picker options = %#v, want one project remote", envOptions)
	}
	if envOptions[0].Value != "test" || envOptions[0].Label != "test (client.app1-linode:live)" {
		t.Fatalf("env picker option = %#v, want test without remote prefix", envOptions[0])
	}
	if strings.Contains(envOptions[0].Label, "remote ") {
		t.Fatalf("env picker label contains remote prefix: %#v", envOptions[0])
	}
	for _, want := range []string{"Primary domain plan:", "env:       client.app1-linode:live", "primary:   www.client.com", "proxy:     none", "search-replace: false"} {
		if !strings.Contains(output, want) {
			t.Fatalf("project domain primary output missing %q:\n%s", want, output)
		}
	}
}

func TestRunSiteDomainAddWithoutArgsPromptsDomainAndProxy(t *testing.T) {
	setupLinodeDomainPromptFixture(t, nil)
	t.Cleanup(withInteractiveDomainPrompts(t))

	siteSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		if title != "Choose an env or remote for domain add" {
			t.Fatalf("env picker title = %q", title)
		}
		return "client.app1-linode:live", nil
	}
	siteDomainPromptStringFn = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		switch prompt {
		case "Domain(s) to add":
			return "www.client.com, client.com", nil
		case "Reverse proxy public IP":
			return "159.203.49.164", nil
		default:
			t.Fatalf("unexpected prompt %q", prompt)
			return "", nil
		}
	}
	siteDomainSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		switch title {
		case "Choose Linode proxy mode":
			return "ip", nil
		default:
			t.Fatalf("unexpected select %q", title)
			return "", nil
		}
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "add", "--dry-run"}); got != 0 {
			t.Fatalf("Run(domain add picker) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Add domain plan:", "secondary: www.client.com, client.com", "redirects: https://client.app1-linode.nonfiction.dev", "proxy:     159.203.49.164", "A     www.client.com -> 159.203.49.164", "mode:      dry-run"} {
		if !strings.Contains(output, want) {
			t.Fatalf("domain add picker output missing %q:\n%s", want, output)
		}
	}
	for _, notWant := range []string{"Primary domain plan:", "search-replace:"} {
		if strings.Contains(output, notWant) {
			t.Fatalf("domain add picker output unexpectedly contains %q:\n%s", notWant, output)
		}
	}
}

func TestRunSiteDomainRemoveWithoutArgsPromptsEnvDomainsAndProxy(t *testing.T) {
	setupLinodeDomainPromptFixture(t, map[string]any{"domains": []map[string]any{{"name": "www.client.com", "role": "primary", "management": "external", "status": "active"}, {"name": "client.com", "role": "secondary", "management": "external", "status": "pending"}}})
	t.Cleanup(withInteractiveDomainPrompts(t))

	siteSelectFn = func(title string, options []ui.SelectOption) (string, error) { return "client.app1-linode:live", nil }
	siteDomainMultiSelectFn = func(title string, options []ui.SelectOption) ([]string, error) {
		t.Fatalf("unexpected default multi-select %q with options %#v", title, options)
		return nil, nil
	}
	siteDomainMultiSelectNoneFn = func(title string, options []ui.SelectOption) ([]string, error) {
		if title != "Choose domains to remove" {
			t.Fatalf("multi-select title = %q", title)
		}
		if len(options) != 1 || options[0].Value != "client.com" || options[0].Label != "client.com (secondary)" {
			t.Fatalf("remove domain options = %#v, want only secondary client.com", options)
		}
		for _, option := range options {
			if option.Default {
				t.Fatalf("remove domain option was preselected: %#v", option)
			}
		}
		return []string{"client.com"}, nil
	}
	siteDomainSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		if title != "Choose Linode proxy mode" {
			t.Fatalf("select title = %q", title)
		}
		if len(options) == 0 || options[0].Label != "Direct (no proxy)" {
			t.Fatalf("proxy mode options = %#v, want Direct (no proxy)", options)
		}
		return "direct", nil
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "remove", "--dry-run"}); got != 0 {
			t.Fatalf("Run(domain remove picker) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Remove domain plan:", "domains:   client.com", "proxy:     none", "mode:      dry-run"} {
		if !strings.Contains(output, want) {
			t.Fatalf("domain remove picker output missing %q:\n%s", want, output)
		}
	}
}

func TestRunSiteDomainCheckWithoutArgsPromptsEnvDomainAndUsesCachedProxy(t *testing.T) {
	setupLinodeDomainPromptFixture(t, map[string]any{"hostname": "www.client.com", "url": "https://www.client.com", "primary_domain": "www.client.com", "internal_hostname": "client.app1-linode.nonfiction.dev", "internal_url": "https://client.app1-linode.nonfiction.dev", "domains": []map[string]any{{"name": "www.client.com", "role": "primary", "management": "external", "status": "active", "proxy_mode": ""}, {"name": "assets.client.com", "role": "secondary", "management": "external", "status": "active", "proxy_mode": "cloudflare"}}})
	t.Cleanup(withInteractiveDomainPrompts(t))

	siteSelectFn = func(title string, options []ui.SelectOption) (string, error) { return "client.app1-linode:live", nil }
	siteDomainMultiSelectFn = func(title string, options []ui.SelectOption) ([]string, error) {
		t.Fatalf("unexpected multi-select %q with options %#v", title, options)
		return nil, nil
	}
	selectCalls := []string{}
	siteDomainSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectCalls = append(selectCalls, title)
		switch title {
		case "Choose a domain to check":
			if len(options) != 2 || options[0].Value != "www.client.com" || options[1].Value != "assets.client.com" {
				t.Fatalf("domain picker options = %#v", options)
			}
			return "www.client.com", nil
		default:
			t.Fatalf("unexpected select %q with options %#v", title, options)
			return "", nil
		}
	}
	oldRunSSHOutput := runSSHOutputFn
	oldLookupHost := siteDomainLookupHostFn
	oldHTTP := siteDomainHTTPStatusFn
	oldHTTPS := siteDomainHTTPSStatusFn
	oldTLS := siteDomainTLSStatusFn
	runSSHOutputFn = func(args []string) ([]byte, error) {
		return []byte("vhost=present\nenabled=present\ntimer=active\nservice=inactive\ncert=ready\n"), nil
	}
	siteDomainLookupHostFn = func(host string) ([]string, error) { return []string{"203.0.113.10"}, nil }
	siteDomainHTTPStatusFn = func(domain string) siteDomainHTTPCheckResult { return siteDomainHTTPCheckResult{StatusCode: 200} }
	siteDomainHTTPSStatusFn = func(domain string) siteDomainHTTPCheckResult { return siteDomainHTTPCheckResult{StatusCode: 200} }
	siteDomainTLSStatusFn = func(domain string) siteDomainTLSCheckResult {
		return siteDomainTLSCheckResult{OK: true, NotAfter: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), Issuer: "Let's Encrypt"}
	}
	t.Cleanup(func() {
		runSSHOutputFn = oldRunSSHOutput
		siteDomainLookupHostFn = oldLookupHost
		siteDomainHTTPStatusFn = oldHTTP
		siteDomainHTTPSStatusFn = oldHTTPS
		siteDomainTLSStatusFn = oldTLS
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "check"}); got != 0 {
			t.Fatalf("Run(domain check picker) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Public domain check", "domain: www.client.com", "proxy: none", "Overall: ready"} {
		if !strings.Contains(output, want) {
			t.Fatalf("domain check picker output missing %q:\n%s", want, output)
		}
	}
	for _, want := range []string{"Choose a domain to check"} {
		if !slices.Contains(selectCalls, want) {
			t.Fatalf("select calls = %#v, missing %q", selectCalls, want)
		}
	}
	if slices.Contains(selectCalls, "Choose Linode proxy mode") {
		t.Fatalf("domain check should use cached proxy mode without prompting; select calls = %#v", selectCalls)
	}
}

func TestResolveSiteDomainProxyDecisionCheckUsesCachedProxyWithoutPrompt(t *testing.T) {
	oldInteractive := siteIsInteractiveFn
	oldSelect := siteDomainSelectFn
	siteIsInteractiveFn = func() bool { return true }
	siteDomainSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		t.Fatalf("unexpected proxy prompt %q with options %#v", title, options)
		return "", nil
	}
	t.Cleanup(func() {
		siteIsInteractiveFn = oldInteractive
		siteDomainSelectFn = oldSelect
	})

	record := map[string]any{"domains": []map[string]any{
		{"name": "www.client.com", "role": "primary", "management": "external", "proxy_mode": ""},
		{"name": "assets.client.com", "role": "secondary", "management": "external", "proxy_mode": "cloudflare"},
	}}
	for _, tt := range []struct {
		name string
		want string
	}{
		{name: "www.client.com", want: ""},
		{name: "assets.client.com", want: "cloudflare"},
	} {
		opts, err := resolveSiteDomainProxyDecision("check", siteDomainOptions{domains: []string{tt.name}}, record)
		if err != nil {
			t.Fatalf("resolveSiteDomainProxyDecision(%q) error = %v", tt.name, err)
		}
		if !opts.proxySet || opts.proxyMode != tt.want {
			t.Fatalf("resolveSiteDomainProxyDecision(%q) proxySet=%t proxyMode=%q, want true %q", tt.name, opts.proxySet, opts.proxyMode, tt.want)
		}
	}
}

func TestResolveSiteDomainProxyDecisionCheckHonorsExplicitProxyOverride(t *testing.T) {
	oldSelect := siteDomainSelectFn
	siteDomainSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		t.Fatalf("unexpected proxy prompt %q with options %#v", title, options)
		return "", nil
	}
	t.Cleanup(func() { siteDomainSelectFn = oldSelect })

	record := map[string]any{"domains": []map[string]any{{"name": "www.client.com", "role": "primary", "management": "external", "proxy_mode": "cloudflare"}}}
	opts, err := resolveSiteDomainProxyDecision("check", siteDomainOptions{domains: []string{"www.client.com"}, proxySet: true, proxyMode: ""}, record)
	if err != nil {
		t.Fatalf("resolveSiteDomainProxyDecision(explicit no-proxy) error = %v", err)
	}
	if !opts.proxySet || opts.proxyMode != "" {
		t.Fatalf("explicit no-proxy override proxySet=%t proxyMode=%q, want true empty", opts.proxySet, opts.proxyMode)
	}
}

func TestResolveSiteDomainProxyDecisionCheckPromptsWhenCacheUnknown(t *testing.T) {
	oldInteractive := siteIsInteractiveFn
	oldSelect := siteDomainSelectFn
	siteIsInteractiveFn = func() bool { return true }
	var promptTitle string
	siteDomainSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		promptTitle = title
		if len(options) == 0 || options[0].Label != "Direct (no proxy)" {
			t.Fatalf("proxy prompt options = %#v, want Direct (no proxy) first", options)
		}
		return "cloudflare", nil
	}
	t.Cleanup(func() {
		siteIsInteractiveFn = oldInteractive
		siteDomainSelectFn = oldSelect
	})

	record := map[string]any{"domains": []map[string]any{{"name": "other.client.com", "role": "secondary", "management": "external", "proxy_mode": ""}}}
	opts, err := resolveSiteDomainProxyDecision("check", siteDomainOptions{domains: []string{"www.client.com"}}, record)
	if err != nil {
		t.Fatalf("resolveSiteDomainProxyDecision(unknown cache) error = %v", err)
	}
	if promptTitle != "Choose Linode proxy mode" {
		t.Fatalf("proxy prompt title = %q", promptTitle)
	}
	if !opts.proxySet || opts.proxyMode != "cloudflare" {
		t.Fatalf("unknown cache proxySet=%t proxyMode=%q, want true cloudflare", opts.proxySet, opts.proxyMode)
	}
}

func TestRunSiteDomainNonInteractiveRequiresExplicitDecisions(t *testing.T) {
	setupLinodeDomainPromptFixture(t, nil)
	stderr := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			if got := Run([]string{"domain", "add", "client.app1-linode:live", "www.client.com", "--dry-run", "--non-interactive"}); got != 1 {
				t.Fatalf("Run(domain add missing proxy decision) = %d, want 1", got)
			}
		})
	})
	if !strings.Contains(stderr, "domain add requires --proxy or --no-proxy in non-interactive mode") {
		t.Fatalf("missing proxy decision stderr = %q", stderr)
	}

	setupLinodeDomainPromptFixture(t, map[string]any{"domains": []map[string]any{{"name": "www.client.com", "role": "secondary", "management": "external", "status": "pending"}}})
	stderr = captureStderr(t, func() {
		_ = captureStdout(t, func() {
			if got := Run([]string{"domain", "primary", "client.app1-linode:live", "www.client.com", "--no-proxy", "--dry-run", "--non-interactive"}); got != 1 {
				t.Fatalf("Run(domain primary missing search decision) = %d, want 1", got)
			}
		})
	})
	if !strings.Contains(stderr, "domain primary requires --search-replace or --no-search-replace in non-interactive mode") {
		t.Fatalf("missing search decision stderr = %q", stderr)
	}
}

func TestRunSiteDomainAddRejectsPrimaryAndSearchFlags(t *testing.T) {
	setupLinodeDomainPromptFixture(t, nil)
	stderr := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			if got := Run([]string{"domain", "add", "client.app1-linode:live", "www.client.com", "--primary", "--no-proxy", "--dry-run", "--non-interactive"}); got != 1 {
				t.Fatalf("Run(domain add --primary) = %d, want 1", got)
			}
		})
	})
	if !strings.Contains(stderr, "unknown domain flag: --primary") {
		t.Fatalf("primary flag stderr = %q", stderr)
	}

	stderr = captureStderr(t, func() {
		_ = captureStdout(t, func() {
			if got := Run([]string{"domain", "add", "client.app1-linode:live", "www.client.com", "--no-search-replace", "--no-proxy", "--dry-run", "--non-interactive"}); got != 1 {
				t.Fatalf("Run(domain add --no-search-replace) = %d, want 1", got)
			}
		})
	})
	if !strings.Contains(stderr, "--search-replace and --no-search-replace only apply to domain primary") {
		t.Fatalf("search flag stderr = %q", stderr)
	}

	stderr = captureStderr(t, func() {
		_ = captureStdout(t, func() {
			if got := Run([]string{"domain", "add", "client.app1-linode:live", "www.client.com", "--setup", "quick", "--no-proxy", "--dry-run", "--non-interactive"}); got != 1 {
				t.Fatalf("Run(domain add --setup) = %d, want 1", got)
			}
		})
	})
	if !strings.Contains(stderr, "--setup is no longer supported; Kinsta domain setup always uses avoid-downtime") {
		t.Fatalf("setup flag stderr = %q", stderr)
	}
}

func testCloudflareIPRangeSet() siteDomainIPRangeSet {
	return siteDomainIPRangeSet{Prefixes: []netip.Prefix{netip.MustParsePrefix("104.16.0.0/13"), netip.MustParsePrefix("2606:4700::/32")}, Source: "test"}
}

func TestRunSiteDomainLinodeCheckCloudflareRejectsNonCloudflareDNS(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "public_ipv4": "203.0.113.10", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "client.app1-linode", "env_id": "client.app1-linode:live", "name": "client", "env": "live", "target": "app1-linode", "path": "/var/www/sites/client/public", "database": "client", "hostname": "client.app1-linode.nonfiction.dev", "url": "https://client.app1-linode.nonfiction.dev", "php_version": "8.3", "proxy_mode": "cloudflare"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	oldRunSSHOutput := runSSHOutputFn
	oldLookupHost := siteDomainLookupHostFn
	oldHTTP := siteDomainHTTPStatusFn
	oldHTTPS := siteDomainHTTPSStatusFn
	oldTLS := siteDomainTLSStatusFn
	oldOriginTLS := siteDomainOriginTLSFn
	oldCloudflareRanges := siteDomainCloudflareIPRangesFn
	runSSHOutputFn = func(args []string) ([]byte, error) {
		return []byte("vhost=present\nenabled=present\ntimer=active\nservice=inactive\ncert=ready\n"), nil
	}
	siteDomainLookupHostFn = func(host string) ([]string, error) {
		return []string{"198.51.100.9"}, nil
	}
	siteDomainCloudflareIPRangesFn = testCloudflareIPRangeSet
	siteDomainHTTPStatusFn = func(domain string) siteDomainHTTPCheckResult {
		return siteDomainHTTPCheckResult{StatusCode: 200}
	}
	siteDomainHTTPSStatusFn = func(domain string) siteDomainHTTPCheckResult {
		return siteDomainHTTPCheckResult{StatusCode: 200}
	}
	siteDomainTLSStatusFn = func(domain string) siteDomainTLSCheckResult {
		return siteDomainTLSCheckResult{OK: true, NotAfter: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), Issuer: "Cloudflare Inc ECC"}
	}
	siteDomainOriginTLSFn = func(domain, origin string) siteDomainTLSCheckResult {
		return siteDomainTLSCheckResult{OK: true, NotAfter: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC), Issuer: "Let's Encrypt"}
	}
	t.Cleanup(func() {
		runSSHOutputFn = oldRunSSHOutput
		siteDomainLookupHostFn = oldLookupHost
		siteDomainHTTPStatusFn = oldHTTP
		siteDomainHTTPSStatusFn = oldHTTPS
		siteDomainTLSStatusFn = oldTLS
		siteDomainOriginTLSFn = oldOriginTLS
		siteDomainCloudflareIPRangesFn = oldCloudflareRanges
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "check", "client.app1-linode:live", "www.client.com", "--non-interactive"}); got != 2 {
			t.Fatalf("Run(domain check linode cloudflare) = %d, want 2", got)
		}
	})
	assertContainsInOrder(t, output, []string{"Public domain check", "proxy: cloudflare", "Linode target", "[unchecked] proxy mode", "mode: cloudflare", "[ok] certbot timer", "status: active", "[ok] certificate", "status: ready", "DNS", "[pending] Cloudflare DNS", "domain: www.client.com", "resolves to: 198.51.100.9", "result: non-Cloudflare address found", "outside ranges: 198.51.100.9", "HTTPS", "url: https://www.client.com/", "expires: 2026-12-31", "issuer: Cloudflare Inc ECC", "Origin HTTPS", "origin: 203.0.113.10", "issuer: Let's Encrypt", "wait for pending checks", "Overall: pending"})
}

func TestRunSiteDomainLinodeCheckCloudflareChecksOriginTLS(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "public_ipv4": "203.0.113.10", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "client.app1-linode", "env_id": "client.app1-linode:live", "name": "client", "env": "live", "target": "app1-linode", "path": "/var/www/sites/client/public", "database": "client", "hostname": "client.app1-linode.nonfiction.dev", "url": "https://client.app1-linode.nonfiction.dev", "php_version": "8.3", "proxy_mode": "cloudflare"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	oldRunSSHOutput := runSSHOutputFn
	oldLookupHost := siteDomainLookupHostFn
	oldHTTP := siteDomainHTTPStatusFn
	oldHTTPS := siteDomainHTTPSStatusFn
	oldTLS := siteDomainTLSStatusFn
	oldOriginTLS := siteDomainOriginTLSFn
	oldCloudflareRanges := siteDomainCloudflareIPRangesFn
	runSSHOutputFn = func(args []string) ([]byte, error) {
		return []byte("vhost=present\nenabled=present\ntimer=active\nservice=inactive\ncert=ready\n"), nil
	}
	siteDomainLookupHostFn = func(host string) ([]string, error) {
		return []string{"104.16.1.1", "104.16.2.2"}, nil
	}
	siteDomainCloudflareIPRangesFn = testCloudflareIPRangeSet
	siteDomainHTTPStatusFn = func(domain string) siteDomainHTTPCheckResult {
		return siteDomainHTTPCheckResult{StatusCode: 200}
	}
	siteDomainHTTPSStatusFn = func(domain string) siteDomainHTTPCheckResult {
		return siteDomainHTTPCheckResult{StatusCode: 200}
	}
	siteDomainTLSStatusFn = func(domain string) siteDomainTLSCheckResult {
		return siteDomainTLSCheckResult{OK: true, NotAfter: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), Issuer: "Cloudflare Inc ECC"}
	}
	siteDomainOriginTLSFn = func(domain, origin string) siteDomainTLSCheckResult {
		if domain != "www.client.com" || origin != "203.0.113.10" {
			t.Fatalf("siteDomainOriginTLSFn(%q, %q), want www.client.com, 203.0.113.10", domain, origin)
		}
		return siteDomainTLSCheckResult{OK: true, NotAfter: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC), Issuer: "Let's Encrypt"}
	}
	t.Cleanup(func() {
		runSSHOutputFn = oldRunSSHOutput
		siteDomainLookupHostFn = oldLookupHost
		siteDomainHTTPStatusFn = oldHTTP
		siteDomainHTTPSStatusFn = oldHTTPS
		siteDomainTLSStatusFn = oldTLS
		siteDomainOriginTLSFn = oldOriginTLS
		siteDomainCloudflareIPRangesFn = oldCloudflareRanges
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"domain", "check", "client.app1-linode:live", "www.client.com", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(domain check linode cloudflare) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, output, []string{"Public domain check", "proxy: cloudflare", "Linode target", "[unchecked] proxy mode", "mode: cloudflare", "[ok] certbot timer", "status: active", "[ok] certificate", "status: ready", "DNS", "[ok] Cloudflare DNS", "domain: www.client.com", "resolves to: 104.16.1.1, 104.16.2.2", "origin check: skipped for cloudflare", "HTTPS", "url: https://www.client.com/", "expires: 2026-12-31", "issuer: Cloudflare Inc ECC", "Origin HTTPS", "[ok] origin HTTPS", "domain: www.client.com", "origin: 203.0.113.10", "expires: 2026-09-12", "issuer: Let's Encrypt", "domain checks passed", "Overall: ready"})
}

func TestIsSameHTTPSRedirect(t *testing.T) {
	for _, tt := range []struct {
		domain   string
		location string
		want     bool
	}{
		{domain: "sanjel.jons.ca", location: "https://sanjel.jons.ca/", want: true},
		{domain: "sanjel.jons.ca", location: "https://sanjel.jons.ca", want: true},
		{domain: "sanjel.jons.ca", location: "/", want: true},
		{domain: "sanjel.jons.ca", location: "https://www.sanjel.jons.ca/", want: false},
		{domain: "sanjel.jons.ca", location: "", want: false},
	} {
		if got := isSameHTTPSRedirect(tt.domain, tt.location); got != tt.want {
			t.Fatalf("isSameHTTPSRedirect(%q, %q) = %t, want %t", tt.domain, tt.location, got, tt.want)
		}
	}
}

func TestRunSiteStagingAddLinodeExecuteCreatesOnlyStaging(t *testing.T) {
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
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "foobar.app1-linode", "env_id": "foobar.app1-linode:live", "name": "foobar", "env": "live", "target": "app1-linode", "path": "/var/www/sites/foobar/public", "database": "foobar", "hostname": "foobar.app1-linode.nonfiction.dev", "php_version": "8.3"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	showOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "show", "foobar.app1-linode"}); got != 0 {
			t.Fatalf("Run(site show) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, showOutput, []string{
		"foobar.app1-linode\n",
		"Name       foobar\n",
		"Provider   linode\n",
		"Target     app1-linode\n",
		"Staging   not created\n",
		"Next      nf site staging add foobar.app1-linode\n",
		"Environments\n",
		"live  8.3  foobar.app1-linode.nonfiction.dev",
	})
	for _, notWant := range []string{"Site       foobar.app1-linode", "Staging: not created", "Create staging:", "Environments:"} {
		if strings.Contains(showOutput, notWant) {
			t.Fatalf("site show output contains %q:\n%s", notWant, showOutput)
		}
	}
	var sshScript string
	oldRunSSH := runSSHScriptFn
	runSSHScriptFn = func(user, host, script string) error {
		if user != "nonfiction" || host != "app1-linode.nonfiction.dev" {
			t.Fatalf("ssh target = %s@%s, want nonfiction@app1-linode.nonfiction.dev", user, host)
		}
		sshScript = script
		return nil
	}
	t.Cleanup(func() { runSSHScriptFn = oldRunSSH })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "staging", "add", "foobar.app1-linode", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site staging add linode) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Add staging env plan:", "site id: foobar.app1-linode", "provider: linode", "env staging:", "mode: execute", "Staging env added."} {
		if !strings.Contains(output, want) {
			t.Fatalf("site staging add output missing %q:\n%s", want, output)
		}
	}
	for _, want := range []string{"create_env staging /var/www/sites/foobar_staging/public foobar_staging", "foobar-staging.app1-linode.nonfiction.dev", "foobar.app1-linode:staging"} {
		if !strings.Contains(sshScript, want) {
			t.Fatalf("staging add ssh script missing %q:\n%s", want, sshScript)
		}
	}
	if strings.Contains(sshScript, "create_env live ") {
		t.Fatalf("staging add ssh script created live env:\n%s", sshScript)
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(records) != 2 || !siteEnvRecordExists(records, "foobar.app1-linode", "live") || !siteEnvRecordExists(records, "foobar.app1-linode", "staging") {
		t.Fatalf("site records after staging add = %#v, want live and staging", records)
	}
}

func TestRunSiteStagingRemoveLinodeExecuteRemovesOnlyStaging(t *testing.T) {
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
	var sshScript string
	oldRunSSH := runSSHScriptFn
	runSSHScriptFn = func(user, host, script string) error {
		sshScript = script
		return nil
	}
	t.Cleanup(func() { runSSHScriptFn = oldRunSSH })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "staging", "rm", "foobar.app1-linode", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site staging rm linode) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Remove staging env plan:", "env staging:", "mode: execute", "Staging env removed."} {
		if !strings.Contains(output, want) {
			t.Fatalf("site staging remove output missing %q:\n%s", want, output)
		}
	}
	for _, want := range []string{"remove_env foobar.app1-linode:staging", "--arg env staging", "map(select(.site_id != $site_id or .env != $env))"} {
		if !strings.Contains(sshScript, want) {
			t.Fatalf("staging remove ssh script missing %q:\n%s", want, sshScript)
		}
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(records) != 1 || !siteEnvRecordExists(records, "foobar.app1-linode", "live") || siteEnvRecordExists(records, "foobar.app1-linode", "staging") {
		t.Fatalf("site records after staging remove = %#v, want only live", records)
	}
}

func TestRunSiteStagingAddKinstaExecuteCachesStaging(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev", "dnsimple_account_id": "14"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "foobar.kinsta", "env_id": "foobar.kinsta:live", "name": "foobar", "env": "live", "target": "kinsta", "hostname": "foobar.kinsta.nonfiction.dev", "php_version": "8.2", "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-live", "domain_id": "kdom-live"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	var capturedPlan kinstaSiteAddPlan
	oldProvision := kinstaProvisionStagingFn
	kinstaProvisionStagingFn = func(plan kinstaSiteAddPlan) (kinstaProvisionResult, error) {
		capturedPlan = plan
		return kinstaProvisionResult{SiteID: "ksite123", Envs: []kinstaSiteAddEnvPlan{{Env: "staging", Domain: "foobar-staging.kinsta.nonfiction.dev", URL: "https://foobar-staging.kinsta.nonfiction.dev", Branch: "develop", EnvID: "kenv-staging", DomainID: "kdom-staging", Path: "/www/foobarstaging/public", Database: "foobarstaging", SSHHost: "203.0.113.11", SSHPort: "12346", SSHUser: "foobarstaging", SSHCmd: "ssh foobarstaging@203.0.113.11 -p 12346"}}}, nil
	}
	t.Cleanup(func() { kinstaProvisionStagingFn = oldProvision })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "staging", "add", "foobar.kinsta", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site staging add kinsta) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Add staging env plan:", "provider: kinsta", "kinsta site id: ksite123", "domain: foobar-staging.kinsta.nonfiction.dev", "mode: execute", "Staging env added."} {
		if !strings.Contains(output, want) {
			t.Fatalf("site staging add kinsta output missing %q:\n%s", want, output)
		}
	}
	if capturedPlan.KinstaSiteID != "ksite123" || capturedPlan.SiteID != "foobar.kinsta" || len(capturedPlan.Envs) != 1 || capturedPlan.Envs[0].Env != "staging" {
		t.Fatalf("captured plan = %#v, want staging-only kinsta plan", capturedPlan)
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(records) != 2 || !siteEnvRecordExists(records, "foobar.kinsta", "live") || !siteEnvRecordExists(records, "foobar.kinsta", "staging") {
		t.Fatalf("site records after kinsta staging add = %#v, want live and staging", records)
	}
}

func TestRunSiteStagingAddKinstaExistingStagingResumes(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev", "dnsimple_account_id": "14"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "kinsta", "site_id": "foobar.kinsta", "env_id": "foobar.kinsta:live", "name": "foobar", "env": "live", "target": "kinsta", "hostname": "foobar.kinsta.nonfiction.dev", "php_version": "8.2", "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-live", "domain_id": "kdom-live"}},
		{"provider": "kinsta", "site_id": "foobar.kinsta", "env_id": "foobar.kinsta:staging", "name": "foobar", "env": "staging", "target": "kinsta", "hostname": "foobar-staging.kinsta.nonfiction.dev", "status": "active", "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-staging-old", "domain_id": "kdom-staging-old"}},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	called := false
	oldProvision := kinstaProvisionStagingFn
	kinstaProvisionStagingFn = func(plan kinstaSiteAddPlan) (kinstaProvisionResult, error) {
		called = true
		return kinstaProvisionResult{SiteID: "ksite123", Envs: []kinstaSiteAddEnvPlan{{Env: "staging", Domain: "foobar-staging.kinsta.nonfiction.dev", URL: "https://foobar-staging.kinsta.nonfiction.dev", Branch: "develop", EnvID: "kenv-staging", DomainID: "kdom-staging", Path: "/www/foobarstaging/public", Database: "foobarstaging", SSHHost: "203.0.113.11", SSHPort: "12346", SSHUser: "foobarstaging", SSHCmd: "ssh foobarstaging@203.0.113.11 -p 12346"}}}, nil
	}
	t.Cleanup(func() { kinstaProvisionStagingFn = oldProvision })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "staging", "add", "foobar.kinsta", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site staging add kinsta existing) = %d, want 0", got)
		}
	})
	if !called {
		t.Fatal("kinstaProvisionStagingFn was not called")
	}
	if !strings.Contains(output, "Staging env added.") {
		t.Fatalf("output = %q, want success", output)
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(records) != 2 || !siteEnvRecordExists(records, "foobar.kinsta", "live") || !siteEnvRecordExists(records, "foobar.kinsta", "staging") {
		t.Fatalf("site records after kinsta staging resume = %#v, want live and staging", records)
	}
	staging := findSiteRecordByEnv(records, "staging")
	if got := siteKinstaID(staging, "domain_id"); got != "kdom-staging" {
		t.Fatalf("staging domain_id = %q, want refreshed domain id", got)
	}
}

func TestRunSiteStagingRemoveKinstaDryRunPlansOnlyStagingDeletion(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev", "dnsimple_account_id": "14"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
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
		if got := Run([]string{"site", "staging", "remove", "foobar.kinsta", "--dry-run", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site staging remove kinsta dry-run) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Remove staging env plan:", "site id: foobar.kinsta", "provider: kinsta", "env staging:", "kinsta environment id: kenv-staging", "dns delete: A foobar-staging.kinsta.nonfiction.dev (inferred)", "dns delete: CNAME foobar-staging.kinsta.nonfiction.dev (inferred)", "dns delete: TXT _acme-challenge.foobar-staging.kinsta.nonfiction.dev (inferred)", "remote actions: delete Kinsta staging environment", "mode: dry-run"} {
		if !strings.Contains(output, want) {
			t.Fatalf("site staging remove kinsta output missing %q:\n%s", want, output)
		}
	}
	for _, notWant := range []string{"kenv-live", "delete Kinsta site", "foobar.kinsta.nonfiction.dev"} {
		if strings.Contains(output, notWant) {
			t.Fatalf("site staging remove kinsta output contains %q:\n%s", notWant, output)
		}
	}
}

func TestRunSiteStagingRemoveKinstaExecuteDeletesTypedDNSAndKeepsLive(t *testing.T) {
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
		case "GET /sites/environments/domains/kdom-staging/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"pointing_records": []map[string]any{{"name": "foobar-staging.kinsta.nonfiction.dev", "type": "CNAME", "content": "hosting.kinsta.cloud"}, {"name": "verify.foobar-staging.kinsta.nonfiction.dev", "type": "CNAME", "content": "verify.kinsta.cloud"}}, "verification_records": []map[string]any{{"name": "_acme-challenge.foobar-staging.kinsta.nonfiction.dev", "type": "TXT", "content": "token"}}}})
		case "DELETE /sites/environments/kenv-staging":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-delete-staging", "status": 202})
		case "GET /operations/op-delete-staging":
			_ = json.NewEncoder(w).Encode(map[string]any{"operation": map[string]any{"status": "complete"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KINSTA_BASE_URL", server.URL)
	type dnsCall struct{ recordType, token, accountID, zone, name string }
	dnsCalls := []dnsCall{}
	oldTypedDelete := deleteDNSTypedRecordFn
	deleteDNSTypedRecordFn = func(token, accountID, zone, name, recordType string) error {
		dnsCalls = append(dnsCalls, dnsCall{recordType: recordType, token: token, accountID: accountID, zone: zone, name: name})
		return nil
	}
	t.Cleanup(func() { deleteDNSTypedRecordFn = oldTypedDelete })
	oldListRecords := listDNSTypedRecordsFn
	listDNSTypedRecordsFn = func(token, accountID, zone, recordType string) ([]provision.DNSRecord, error) {
		switch recordType {
		case "TXT":
			return []provision.DNSRecord{
				{Name: "k-verification-staging.foobar-staging.kinsta", Type: "TXT", Content: "verify-token"},
				{Name: "_cf-custom-hostname.foobar-staging.kinsta", Type: "TXT", Content: "cf-token"},
				{Name: "k-verification-live.foobar.kinsta", Type: "TXT", Content: "live-token"},
			}, nil
		case "CNAME":
			return []provision.DNSRecord{{Name: "_acme-challenge.foobar-staging.kinsta", Type: "CNAME", Content: "foobar-staging.kinsta.nonfiction.dev.kinstavalidation.app"}}, nil
		default:
			return nil, nil
		}
	}
	t.Cleanup(func() { listDNSTypedRecordsFn = oldListRecords })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "staging", "remove", "foobar.kinsta", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site staging remove kinsta execute) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Staging env removed.") || !strings.Contains(output, "mode: execute") {
		t.Fatalf("site staging remove kinsta execute output = %q, want success", output)
	}
	for _, want := range []dnsCall{
		{recordType: "A", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "foobar-staging.kinsta"},
		{recordType: "CNAME", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "foobar-staging.kinsta"},
		{recordType: "CNAME", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "_acme-challenge.foobar-staging.kinsta"},
		{recordType: "CNAME", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "verify.foobar-staging.kinsta"},
		{recordType: "TXT", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "_acme-challenge.foobar-staging.kinsta"},
		{recordType: "TXT", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "_cf-custom-hostname.foobar-staging.kinsta"},
		{recordType: "TXT", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "k-verification-staging.foobar-staging.kinsta"},
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
	for _, got := range dnsCalls {
		if strings.Contains(got.name, "foobar.kinsta") && !strings.Contains(got.name, "foobar-staging.kinsta") {
			t.Fatalf("deleted live DNS during staging remove: %#v", dnsCalls)
		}
	}
	for _, notWant := range []string{"GET /sites/environments/domains/kdom-live/verification-records", "DELETE /sites/environments/kenv-live", "DELETE /sites/ksite123"} {
		for _, got := range requests {
			if got == notWant {
				t.Fatalf("unexpected Kinsta request %q in %#v", notWant, requests)
			}
		}
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(records) != 1 || !siteEnvRecordExists(records, "foobar.kinsta", "live") || siteEnvRecordExists(records, "foobar.kinsta", "staging") {
		t.Fatalf("site cache after kinsta staging remove = %#v, want only live", records)
	}
}

func TestRunSiteStagingStatusShowsMissingStaging(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "foobar.app1-linode", "name": "foobar", "env": "live", "target": "app1-linode", "hostname": "foobar.app1-linode.nonfiction.dev"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	output := captureStdout(t, func() {
		if got := Run([]string{"site", "staging", "status", "foobar.app1-linode"}); got != 0 {
			t.Fatalf("Run(site staging status) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Site staging status:", "site id: foobar.app1-linode", "live: active", "staging: not created", "create staging: nf site staging add foobar.app1-linode"} {
		if !strings.Contains(output, want) {
			t.Fatalf("site staging status output missing %q:\n%s", want, output)
		}
	}
}

func TestRunSiteStagingStatusWithoutSiteUsesPicker(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "foobar.app1-linode", "name": "foobar", "env": "live", "target": "app1-linode", "hostname": "foobar.app1-linode.nonfiction.dev"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	oldSelect := siteSelectFn
	var selectTitle string
	var selectOptions []ui.SelectOption
	siteSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectTitle = title
		selectOptions = append([]ui.SelectOption(nil), options...)
		return "foobar.app1-linode", nil
	}
	t.Cleanup(func() { siteSelectFn = oldSelect })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "staging", "status"}); got != 0 {
			t.Fatalf("Run(site staging status picker) = %d, want 0", got)
		}
	})
	if selectTitle != "Choose a site to show staging status for" {
		t.Fatalf("select title = %q", selectTitle)
	}
	if len(selectOptions) != 1 || selectOptions[0] != (ui.SelectOption{Value: "foobar.app1-linode", Label: "foobar.app1-linode"}) {
		t.Fatalf("select options = %#v", selectOptions)
	}
	if !strings.Contains(output, "site id: foobar.app1-linode") {
		t.Fatalf("site staging status picker output = %q", output)
	}
}

func TestRunSiteStagingAddWithoutSiteUsesPicker(t *testing.T) {
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
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "foobar.app1-linode", "name": "foobar", "env": "live", "target": "app1-linode", "hostname": "foobar.app1-linode.nonfiction.dev", "path": "/var/www/sites/foobar/public", "database": "foobar"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	oldSelect := siteSelectFn
	var selectTitle string
	siteSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectTitle = title
		return "foobar.app1-linode", nil
	}
	t.Cleanup(func() { siteSelectFn = oldSelect })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "staging", "add", "--dry-run"}); got != 0 {
			t.Fatalf("Run(site staging add picker) = %d, want 0", got)
		}
	})
	if selectTitle != "Choose a site to add staging to" {
		t.Fatalf("select title = %q", selectTitle)
	}
	for _, want := range []string{"Add staging env plan:", "site id: foobar.app1-linode", "mode: dry-run"} {
		if !strings.Contains(output, want) {
			t.Fatalf("site staging add picker output missing %q:\n%s", want, output)
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
			envs := []map[string]any{{"id": "kenv-live", "name": "foobar", "display_name": "foobar", "php_version": livePHP, "web_root": "/", "primaryDomain": map[string]any{"name": "foobar.kinsta.cloud"}}}
			if createdStaging {
				envs = append(envs, map[string]any{"id": "kenv-staging", "name": "foobar-staging", "display_name": "foobar-staging", "php_version": livePHP, "web_root": "/", "primaryDomain": map[string]any{"name": "foobar-staging.kinsta.cloud"}})
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
		case "GET /company/company-123/activity-logs":
			_ = json.NewEncoder(w).Encode(map[string]any{"company": map[string]any{"activity_logs": map[string]any{"items": []map[string]any{}}}})
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
	stubDNSTypedDeletes(t)

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

func TestEnsureKinstaStagingEnvironmentRetriesWhenLiveEnvironmentIsBusy(t *testing.T) {
	oldTimeout := kinstaEnvironmentWaitTimeout
	oldInterval := kinstaEnvironmentWaitInterval
	kinstaEnvironmentWaitTimeout = 200 * time.Millisecond
	kinstaEnvironmentWaitInterval = time.Millisecond
	t.Cleanup(func() {
		kinstaEnvironmentWaitTimeout = oldTimeout
		kinstaEnvironmentWaitInterval = oldInterval
	})

	cloneCalls := 0
	envCallsAfterSecondClone := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer kinsta-token"; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/ksite123/environments":
			if cloneCalls >= 2 {
				envCallsAfterSecondClone++
				if envCallsAfterSecondClone == 1 {
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(map[string]any{"status": 500, "message": "Server Error"})
					return
				}
			}
			envs := []map[string]any{{"id": "kenv-live", "name": "live", "display_name": "Live", "php_version": "8.3"}}
			if cloneCalls >= 2 {
				envs = append(envs, map[string]any{"id": "kenv-staging", "name": "staging", "display_name": "Staging", "php_version": "8.3", "is_blocked": envCallsAfterSecondClone == 2})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"site": map[string]any{"environments": envs}})
		case "GET /company/company-123/activity-logs":
			items := []map[string]any{{"id": 10, "type": "changePrimaryDomain", "is_done": true, "has_failed": false}}
			if cloneCalls == 1 {
				items = append([]map[string]any{{"id": 11, "type": "addEnvironment", "is_done": true, "has_failed": true, "descriptions": []string{"Add \"Staging\" environment (cloned from \"Live\")"}, "public_error": "The \"Live\" environment is blocked by another process. Please try again a bit later."}}, items...)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"company": map[string]any{"activity_logs": map[string]any{"items": items}}})
		case "POST /sites/ksite123/environments/clone":
			cloneCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": fmt.Sprintf("op-clone-%d", cloneCalls)})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	client := kinsta.NewClient(server.URL, "kinsta-token")
	staging, err := ensureKinstaStagingEnvironment(context.Background(), client, "company-123", "ksite123", kinsta.Environment{ID: "kenv-live", Name: "live", PHPVersion: "8.3"}, "8.3")
	if err != nil {
		t.Fatalf("ensureKinstaStagingEnvironment() error = %v", err)
	}
	if staging.ID != "kenv-staging" {
		t.Fatalf("staging.ID = %q, want kenv-staging", staging.ID)
	}
	if cloneCalls != 2 {
		t.Fatalf("clone calls = %d, want retry after busy failure", cloneCalls)
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
	for _, want := range []string{"rm -rf -- \"$site_path\"", ".db.user // .db.database.user // .adminer.user // .adminer.database.user", "REVOKE ALL PRIVILEGES ON \\`$db_name\\`.* FROM '$db_access_user'@'localhost';", "DROP DATABASE IF EXISTS \\`$db_name\\`;", "DROP USER IF EXISTS '$db_name'@'localhost';", "remove_env foobar.app1-linode:live foobar.app1-linode.live /var/www/sites/foobar/public foobar", "remove_env foobar.app1-linode:staging foobar.app1-linode.staging /var/www/sites/foobar_staging/public foobar_staging", "nf-site-$file_slug", "nf-site-$env_id", "$file_slug.access.log", "$env_id.access.log", "jq --arg site_id foobar.app1-linode", "nginx -t", "systemctl reload nginx"} {
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
	for _, want := range []string{"Remove site plan:", "site id: foobar.kinsta", "target: kinsta", "provider: kinsta", "kinsta site id: ksite123", "dns: dnsimple zone nonfiction.dev account 14", "dns delete: A foobar.kinsta.nonfiction.dev (inferred)", "dns delete: CNAME foobar.kinsta.nonfiction.dev (inferred)", "dns delete: TXT _acme-challenge.foobar.kinsta.nonfiction.dev (inferred)", "dns delete: A foobar-staging.kinsta.nonfiction.dev (inferred)", "dns delete: CNAME foobar-staging.kinsta.nonfiction.dev (inferred)", "dns delete: TXT _acme-challenge.foobar-staging.kinsta.nonfiction.dev (inferred)", "env live:", "kinsta environment id: kenv-live", "env staging:", "kinsta environment id: kenv-staging", "remote actions: delete Kinsta environments, delete Kinsta site", "mode: dry-run"} {
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
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"pointing_records": []map[string]any{{"name": "foobar-staging.kinsta.nonfiction.dev", "type": "CNAME", "content": "hosting.kinsta.cloud"}, {"name": "verify.foobar-staging.kinsta.nonfiction.dev", "type": "CNAME", "content": "verify.kinsta.cloud"}}, "verification_records": []map[string]any{{"name": "_acme-challenge.foobar-staging.kinsta.nonfiction.dev", "type": "TXT", "content": "token"}}}})
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
	oldTypedDelete := deleteDNSTypedRecordFn
	deleteDNSTypedRecordFn = func(token, accountID, zone, name, recordType string) error {
		dnsCalls = append(dnsCalls, dnsCall{kind: recordType, token: token, accountID: accountID, zone: zone, name: name})
		return nil
	}
	t.Cleanup(func() { deleteDNSTypedRecordFn = oldTypedDelete })
	oldListRecords := listDNSTypedRecordsFn
	listDNSTypedRecordsFn = func(token, accountID, zone, recordType string) ([]provision.DNSRecord, error) {
		switch recordType {
		case "TXT":
			return []provision.DNSRecord{
				{Name: "k-verification-live.foobar.kinsta", Type: "TXT", Content: "live-token"},
				{Name: "_cf-custom-hostname.foobar.kinsta", Type: "TXT", Content: "cf-token"},
				{Name: "k-verification-staging.foobar-staging.kinsta", Type: "TXT", Content: "staging-token"},
				{Name: "_cf-custom-hostname.foobar-staging.kinsta", Type: "TXT", Content: "staging-cf-token"},
				{Name: "k-verification-other.other.kinsta", Type: "TXT", Content: "other-token"},
			}, nil
		case "CNAME":
			return []provision.DNSRecord{
				{Name: "_acme-challenge.foobar.kinsta", Type: "CNAME", Content: "foobar.kinsta.nonfiction.dev.kinstavalidation.app"},
				{Name: "_acme-challenge.foobar-staging.kinsta", Type: "CNAME", Content: "foobar-staging.kinsta.nonfiction.dev.kinstavalidation.app"},
			}, nil
		default:
			return nil, nil
		}
	}
	t.Cleanup(func() { listDNSTypedRecordsFn = oldListRecords })

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
		{kind: "AAAA", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "foobar.kinsta"},
		{kind: "CNAME", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "foobar.kinsta"},
		{kind: "CNAME", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "_acme-challenge.foobar.kinsta"},
		{kind: "TXT", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "_acme-challenge.foobar.kinsta"},
		{kind: "TXT", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "_cf-custom-hostname.foobar.kinsta"},
		{kind: "TXT", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "k-verification-live.foobar.kinsta"},
		{kind: "TXT", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "_kinsta.foobar.kinsta"},
		{kind: "A", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "foobar-staging.kinsta"},
		{kind: "AAAA", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "foobar-staging.kinsta"},
		{kind: "CNAME", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "foobar-staging.kinsta"},
		{kind: "CNAME", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "_acme-challenge.foobar-staging.kinsta"},
		{kind: "CNAME", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "verify.foobar-staging.kinsta"},
		{kind: "TXT", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "_acme-challenge.foobar-staging.kinsta"},
		{kind: "TXT", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "_cf-custom-hostname.foobar-staging.kinsta"},
		{kind: "TXT", token: "dns-token", accountID: "14", zone: "nonfiction.dev", name: "k-verification-staging.foobar-staging.kinsta"},
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
	for _, got := range dnsCalls {
		if got.name == "k-verification-other.other.kinsta" {
			t.Fatalf("deleted unrelated Kinsta verification record: %#v", dnsCalls)
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
	if !strings.Contains(stderr, `Use nf site staging remove foobar.app1-linode to delete staging.`) {
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
	for _, want := range []string{"happytents.app2-linode:staging", "Site       happytents.app2-linode", "Env        staging", "Provider   linode", "Target     app2-linode", "URL        https://happytents-staging.app2-linode.nonfiction.dev", "PHP        8.3", "SSH command   ssh nonfiction@app2-linode.nonfiction.dev", "Admin user    nonfiction", "Admin pass    " + adminPassword} {
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
	for _, want := range []string{`"resolved_site": "happytents.app2-linode"`, `"resolved_env": "staging"`, `"resolved_target": "app2-linode"`, `"php_version": "8.3"`, `"resolved_admin_user": "nonfiction"`, `"resolved_admin_password": "` + adminPassword + `"`, `"resolved_target_summary": "app2-linode / linode / ssh nonfiction@app2-linode.nonfiction.dev"`} {
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

func TestRunSitePasswordPrintsRequestedDerivedPassword(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "happytents.app2-linode", "name": "happytents", "env": "live", "target": "app2-linode"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	tests := []struct {
		flag    string
		purpose string
	}{
		{"--wp", "wp-admin"},
		{"--db", "mysql"},
		{"--basicauth", "basic-auth"},
	}
	for _, tt := range tests {
		output := captureStdout(t, func() {
			if got := Run([]string{"site", "password", "happytents.app2-linode", tt.flag}); got != 0 {
				t.Fatalf("Run(site password %s) = %d, want 0", tt.flag, got)
			}
		})
		want := passwords.DerivePassword("happytents", tt.purpose, "test-salt") + "\n"
		if output != want {
			t.Fatalf("site password %s output = %q, want %q", tt.flag, output, want)
		}
	}
}

func TestRunSitePasswordDBAcceptsEnvRef(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "linode", "site_id": "happytents.app2-linode", "name": "happytents", "env": "live", "target": "app2-linode"},
		{"provider": "linode", "site_id": "happytents.app2-linode", "name": "happytents", "env": "staging", "target": "app2-linode"},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "password", "happytents.app2-linode:staging", "--db"}); got != 0 {
			t.Fatalf("Run(site password env --db) = %d, want 0", got)
		}
	})
	want := passwords.DerivePassword("happytents", "mysql", "test-salt") + "\n"
	if output != want {
		t.Fatalf("site password env --db output = %q, want %q", output, want)
	}
}

func TestRunSitePasswordKinstaDBReadsRemoteConfig(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("KINSTA_API_KEY", "kinsta-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer kinsta-token"; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/ksite123/environments/kenv-staging/ssh/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"host": "1.2.3.4", "port": "2222", "user": "client"})
		case "GET /sites/environments/kenv-staging/ssh/password":
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"id": "kenv-staging", "sftp_password": "sftp-pass"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KINSTA_BASE_URL", server.URL)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client.kinsta", "name": "client", "env": "staging", "target": "kinsta", "path": "/www/client_123/public", "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-staging"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	oldRunSSHOutput := runSSHOutputFn
	var sshArgs []string
	runSSHOutputFn = func(args []string) ([]byte, error) {
		sshArgs = append([]string(nil), args...)
		return []byte("db-secret\n"), nil
	}
	t.Cleanup(func() { runSSHOutputFn = oldRunSSHOutput })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "password", "client.kinsta:staging", "--db"}); got != 0 {
			t.Fatalf("Run(site password kinsta --db) = %d, want 0", got)
		}
	})
	if output != "db-secret\n" {
		t.Fatalf("output = %q, want db-secret", output)
	}
	joined := strings.Join(sshArgs, " ")
	for _, want := range []string{"ssh", "-p 2222", "client@1.2.3.4", "cd /www/client_123/public", "wp --path=/www/client_123/public config get DB_PASSWORD --type=constant"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("ssh args missing %q: %#v", want, sshArgs)
		}
	}
}

func TestRunSitePasswordRejectsMultiplePasswordFlags(t *testing.T) {
	stderr := captureStderr(t, func() {
		if got := Run([]string{"site", "password", "client", "--wp", "--db"}); got != 1 {
			t.Fatalf("Run(site password multiple flags) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "site password accepts only one password flag") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestRunSitePasswordUsesMatchingProjectPasswordVersion(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "happytents", "password_version": 4}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}}}
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
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "linode", "site_id": "happytents.app2-linode", "name": "happytents", "env": "live", "target": "app2-linode"},
		{"provider": "linode", "site_id": "happytents.app2-linode", "name": "happytents", "env": "staging", "target": "app2-linode"},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	want := passwords.DerivePassword("happytents:v4", "wp-admin", "test-salt") + "\n"
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

func TestRunSiteBasicAuthPasswordUsesMatchingProjectPasswordVersion(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "happytents", "password_version": 4}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}}}
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
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "linode", "site_id": "happytents.app2-linode", "name": "happytents", "env": "live", "target": "app2-linode"},
		{"provider": "linode", "site_id": "happytents.app2-linode", "name": "happytents", "env": "staging", "target": "app2-linode"},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	want := passwords.DerivePassword("happytents:v4", "basic-auth", "test-salt") + "\n"
	output := captureStdout(t, func() {
		if got := Run([]string{"site", "basicauth", "password", "happytents.app2-linode"}); got != 0 {
			t.Fatalf("Run(site basicauth password) = %d, want 0", got)
		}
	})
	if output != want {
		t.Fatalf("site basicauth password output = %q, want %q", output, want)
	}
}

func TestRunSiteBasicAuthLinodeEnableRunsSSHWithDerivedHash(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := saveGlobalConfig(map[string]string{"basicauth_default_user": "preview", "linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "foobar", "password_version": 5}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}}}
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
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "foobar.app1-linode", "env_id": "foobar.app1-linode:staging", "name": "foobar", "env": "staging", "target": "app1-linode", "hostname": "foobar-staging.app1-linode.nonfiction.dev", "url": "https://foobar-staging.app1-linode.nonfiction.dev", "path": "/var/www/sites/foobar_staging/public", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev", "port": "22"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	var command []string
	oldRunSSHCommand := runSSHCommandFn
	runSSHCommandFn = func(args []string) error {
		command = append([]string(nil), args...)
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSHCommand })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "basicauth", "enable", "foobar.app1-linode:staging", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site basicauth enable) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Site basic-auth plan:", "env:      foobar.app1-linode:staging", "provider: linode", "action:   enable", "user:     preview", "password: derived from foobar", "Basic auth enabled."} {
		if !strings.Contains(output, want) {
			t.Fatalf("site basicauth output missing %q:\n%s", want, output)
		}
	}
	joined := strings.Join(command, " ")
	derived := passwords.DerivePassword("foobar:v5", "basic-auth", "test-salt")
	for _, want := range []string{"ssh -p 22 nonfiction@app1-linode.nonfiction.dev", "sudo bash -c", "file_slugs=(foobar.app1-linode.staging foobar-staging.app1-linode)", "/etc/nginx/sites-available/nf-site-$file_slug", "/etc/nginx/snippets/nf-basic-auth-$selected_file_slug.conf", "auth_basic", "awk -v inc=", "install -o root -g www-data -m 0640", "preview", basicAuthSHA(derived), "nginx -t", "systemctl reload nginx"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("ssh command missing %q: %#v", want, command)
		}
	}
	if strings.Contains(joined, "awk -v include=") {
		t.Fatalf("ssh command used reserved gawk variable name: %#v", command)
	}
	if strings.Contains(joined, derived) {
		t.Fatalf("ssh command included raw basic-auth password: %#v", command)
	}
}

func TestRunSiteBasicAuthKinstaStatusUnsupported(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"basicauth_default_user": "preview"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "staging", "url": "https://staging.example.com/", "path": "/www/clientstaging/public", "ssh": map[string]any{"host": "203.0.113.11", "port": "12346", "user": "clientstaging"}, "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-staging"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	stdout := captureStdout(t, func() {
		if got := Run([]string{"site", "basicauth", "status", "client-kinsta:staging"}); got != 1 {
			t.Fatalf("Run(site basicauth status) = %d, want 1", got)
		}
	})
	for _, want := range []string{"Site basic-auth status:", "provider: kinsta", "user:     preview", "status:   unsupported", kinstaBasicAuthUnsupportedMessage()} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("site basicauth kinsta status missing %q:\n%s", want, stdout)
		}
	}
}

func TestRunSiteBasicAuthActionsUseEnvPicker(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"basicauth_default_user": "preview", "linode_default_user": "nonfiction"}); err != nil {
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
	oldSelect := siteSelectFn
	oldRunSSHOutput := runSSHOutputFn
	var selectTitle string
	var selectOptions []ui.SelectOption
	siteSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectTitle = title
		selectOptions = append([]ui.SelectOption(nil), options...)
		return "foobar.app1-linode:staging", nil
	}
	runSSHOutputFn = func(args []string) ([]byte, error) {
		return []byte("  status:   disabled\n"), nil
	}
	t.Cleanup(func() {
		siteSelectFn = oldSelect
		runSSHOutputFn = oldRunSSHOutput
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "basicauth", "status"}); got != 0 {
			t.Fatalf("Run(site basicauth status) = %d, want 0", got)
		}
	})
	if selectTitle != "Choose a remote env to basic-auth status" {
		t.Fatalf("select title = %q", selectTitle)
	}
	if len(selectOptions) != 2 || selectOptions[0].Value != "foobar.app1-linode:live" || selectOptions[1].Value != "foobar.app1-linode:staging" {
		t.Fatalf("select options = %#v", selectOptions)
	}
	if !strings.Contains(output, "env:      foobar.app1-linode:staging") || !strings.Contains(output, "status:   disabled") {
		t.Fatalf("site basicauth status output = %q, want picked staging status", output)
	}

	selectTitle = ""
	selectOptions = nil
	output = captureStdout(t, func() {
		if got := Run([]string{"site", "basicauth", "status", "foobar.app1-linode"}); got != 0 {
			t.Fatalf("Run(site basicauth status site) = %d, want 0", got)
		}
	})
	if selectTitle != "Choose a remote env to basic-auth status" {
		t.Fatalf("site-filtered select title = %q", selectTitle)
	}
	if len(selectOptions) != 2 || selectOptions[0].Value != "foobar.app1-linode:live" || selectOptions[1].Value != "foobar.app1-linode:staging" {
		t.Fatalf("site-filtered select options = %#v", selectOptions)
	}
	if !strings.Contains(output, "env:      foobar.app1-linode:staging") {
		t.Fatalf("site basicauth site-filtered status output = %q, want picked staging", output)
	}
}

func TestRunSiteBasicAuthEnableDisablePickerAndNonInteractiveErrors(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := saveGlobalConfig(map[string]string{"basicauth_default_user": "preview", "linode_default_user": "nonfiction"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("providers", []map[string]any{{"provider": "linode", "targets": []map[string]any{{"name": "app1-linode", "provider": "linode", "hostname": "app1-linode.nonfiction.dev", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev"}}}}}); err != nil {
		t.Fatalf("SaveStateRecords(providers) error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "foobar.app1-linode", "env_id": "foobar.app1-linode:staging", "name": "foobar", "env": "staging", "target": "app1-linode", "hostname": "foobar-staging.app1-linode.nonfiction.dev", "url": "https://foobar-staging.app1-linode.nonfiction.dev", "path": "/var/www/sites/foobar_staging/public", "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev", "port": "22"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	oldSelect := siteSelectFn
	var selectTitles []string
	siteSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectTitles = append(selectTitles, title)
		return "foobar.app1-linode:staging", nil
	}
	t.Cleanup(func() { siteSelectFn = oldSelect })

	for _, action := range []string{"enable", "disable"} {
		output := captureStdout(t, func() {
			if got := Run([]string{"site", "basicauth", action, "--dry-run"}); got != 0 {
				t.Fatalf("Run(site basicauth %s --dry-run) = %d, want 0", action, got)
			}
		})
		if !strings.Contains(output, "env:      foobar.app1-linode:staging") || !strings.Contains(output, "mode:     dry-run") {
			t.Fatalf("site basicauth %s output = %q, want picked dry-run plan", action, output)
		}
	}
	if got, want := selectTitles, []string{"Choose a remote env to basic-auth enable", "Choose a remote env to basic-auth disable"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("select titles = %#v, want %#v", got, want)
	}

	stderr := captureStderr(t, func() {
		if got := Run([]string{"site", "basicauth", "disable", "--execute", "--yes", "--non-interactive"}); got != 1 {
			t.Fatalf("Run(site basicauth disable non-interactive no ref) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "site basicauth disable requires an explicit env ref in non-interactive mode") {
		t.Fatalf("non-interactive stderr = %q, want explicit env ref error", stderr)
	}
}

func TestRenderLinodeBasicAuthDisableAndStatusCheckAllCandidateVhosts(t *testing.T) {
	plan := siteBasicAuthPlan{Action: "disable", EnvID: "foobar.app1-linode:staging", FileSlugs: []string{"foobar.app1-linode.staging", "foobar-staging.app1-linode"}}
	disableScript := renderLinodeBasicAuthScript(plan)
	for _, want := range []string{"vhosts=()", "snippets=()", "htpasswds=()", "for vhost in \"${vhosts[@]}\"", "for snippet in \"${snippets[@]}\"", "rm -f \"${snippets[@]}\" \"${htpasswds[@]}\"", "nginx -t", "systemctl reload nginx"} {
		if !strings.Contains(disableScript, want) {
			t.Fatalf("disable script missing %q:\n%s", want, disableScript)
		}
	}
	for _, notWant := range []string{"selected_file_slug", "break; fi"} {
		if strings.Contains(disableScript, notWant) {
			t.Fatalf("disable script contained obsolete first-match behavior %q:\n%s", notWant, disableScript)
		}
	}

	statusScript := renderLinodeBasicAuthStatusScript(plan)
	for _, want := range []string{"vhosts=()", "snippets=()", "for vhost in \"${vhosts[@]}\"", "for snippet in \"${snippets[@]}\"", "grep -Fxq \"    include $snippet;\" \"$vhost\""} {
		if !strings.Contains(statusScript, want) {
			t.Fatalf("status script missing %q:\n%s", want, statusScript)
		}
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
		if got := Run([]string{"site", "sh", "client-kinsta:live"}); got != 0 {
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

func TestRunSiteCacheClearsKinstaSiteCache(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("KINSTA_API_KEY", "kinsta-token")
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "path": "/www/client/public", "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-live"}},
		{"provider": "kinsta", "site_id": "client-kinsta", "env": "staging", "url": "https://staging.example.com/", "path": "/www/clientstaging/public", "kinsta": map[string]any{"site_id": "ksite123", "environment_id": "kenv-staging"}},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	var clearedEnv string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer kinsta-token"; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sites/tools/clear-cache":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("Decode(clear-cache) error = %v", err)
			}
			clearedEnv = recordValueString(payload["environment_id"])
			if clearedEnv != "kenv-live" {
				t.Fatalf("environment_id = %q, want kenv-live", clearedEnv)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "cache:clear-kenv-live", "status": 202})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/operations/cache:clear-kenv-live"):
			_ = json.NewEncoder(w).Encode(map[string]any{"operation": map[string]any{"id": "cache:clear-kenv-live", "status": "complete"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KINSTA_BASE_URL", server.URL)

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "cache", "client-kinsta"}); got != 0 {
			t.Fatalf("Run(site cache) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, output, []string{"Site cache preflight:", "site:     client-kinsta", "env:      live", "provider: kinsta", "kinsta env: kenv-live", "action:   clear Kinsta site cache", "Kinsta operation: cache:clear-kenv-live", "Site cache cleared."})
	if clearedEnv != "kenv-live" {
		t.Fatalf("cleared env = %q, want kenv-live", clearedEnv)
	}
}

func TestRunSiteCacheKinstaMissingEnvironmentID(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/"}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	stderr := captureStderr(t, func() {
		if got := Run([]string{"site", "cache", "client-kinsta"}); got != 1 {
			t.Fatalf("Run(site cache missing env id) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "Kinsta site env \"client-kinsta:live\" is missing environment_id. Run nf site refresh.") {
		t.Fatalf("stderr = %q, want missing environment_id guidance", stderr)
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
		if !strings.Contains(stderr, "site wp requires an env ref and WP-CLI command") {
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

func TestRunSiteCachePurgesLinodeCacheAndFlushesWP(t *testing.T) {
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

	liveOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "cache", "foobar.app1-linode"}); got != 0 {
			t.Fatalf("Run(site cache live) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, liveOutput, []string{"Site cache preflight:", "site:     foobar.app1-linode", "env:      live", "provider: linode", "target:   app1-linode", "action:   purge nginx page cache and flush WordPress object cache", "cache:    /var/cache/nginx/nf/sites/foobar", "> ssh -p 22 nonfiction@app1-linode.nonfiction.dev", "Site cache cleared."})

	stagingOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "cache", "foobar.app1-linode:staging"}); got != 0 {
			t.Fatalf("Run(site cache staging) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, stagingOutput, []string{"env:      staging", "cache:    /var/cache/nginx/nf/sites/foobar_staging", "Site cache cleared."})
	if len(commands) != 2 {
		t.Fatalf("commands len = %d, want 2: %#v", len(commands), commands)
	}
	liveCommand := strings.Join(commands[0], " ")
	for _, want := range []string{"ssh -p 22 nonfiction@app1-linode.nonfiction.dev", "cache_dir=/var/cache/nginx/nf/sites/foobar", "sudo find \"$cache_dir\" -mindepth 1 -maxdepth 1 -exec rm -rf {} +", "cd /var/www/sites/foobar/public", "sudo -u www-data wp --path=/var/www/sites/foobar/public cache flush"} {
		if !strings.Contains(liveCommand, want) {
			t.Fatalf("live cache command missing %q: %#v", want, commands[0])
		}
	}
	stagingCommand := strings.Join(commands[1], " ")
	for _, want := range []string{"cache_dir=/var/cache/nginx/nf/sites/foobar_staging", "cd /var/www/sites/foobar_staging/public", "sudo -u www-data wp --path=/var/www/sites/foobar_staging/public cache flush"} {
		if !strings.Contains(stagingCommand, want) {
			t.Fatalf("staging cache command missing %q: %#v", want, commands[1])
		}
	}
	for _, command := range commands {
		if strings.Contains(strings.Join(command, " "), "rewrite flush") {
			t.Fatalf("cache-only command unexpectedly flushed rewrite rules: %#v", command)
		}
	}
}

func TestRunSiteRepairKinstaPlansAndExecutesMUPluginRepair(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "path": "/www/client/public", "domains": []map[string]any{{"name": "www.example.com", "role": "primary", "management": "external", "status": "active"}, {"name": "client.kinsta.nonfiction.dev", "role": "secondary", "management": "internal", "status": "active"}}, "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}, "kinsta": map[string]any{"site_id": "ksite123", "slug": "client", "environment_id": "kenv-live", "domain_id": "public-domain"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	var commands [][]string
	oldRunSSHCommand := runSSHCommandFn
	runSSHCommandFn = func(args []string) error {
		commands = append(commands, append([]string(nil), args...))
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSHCommand })

	dryRunOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "repair", "client-kinsta", "--dry-run"}); got != 0 {
			t.Fatalf("Run(site repair dry-run) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, dryRunOutput, []string{"Site repair plan:", "env:      client-kinsta:live", "provider: kinsta", "path:     /www/client/public", "mode:     dry-run", "remove local-only wp-content/mu-plugins/nf-mailpit.php", "restore Kinsta's required MU plugin", "ensure KINSTAMU_WHITELABEL is enabled in wp-config.php"})
	if len(commands) != 0 {
		t.Fatalf("dry-run executed SSH commands: %#v", commands)
	}
	oldConfirm := siteAddConfirmFn
	var confirmMessage string
	siteAddConfirmFn = func(message string, defaultYes bool) (bool, error) {
		confirmMessage = message
		return true, nil
	}
	t.Cleanup(func() { siteAddConfirmFn = oldConfirm })

	executeOutput := captureStdout(t, func() {
		if got := Run([]string{"site", "repair", "client-kinsta"}); got != 0 {
			t.Fatalf("Run(site repair execute) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, executeOutput, []string{"mode:     execute", "> ssh -p 12345 client@203.0.113.10 '<site repair script>'", "Site repaired."})
	if confirmMessage != `Repair provider platform state for "client-kinsta:live"?` {
		t.Fatalf("confirm message = %q", confirmMessage)
	}
	if len(commands) != 1 {
		t.Fatalf("commands len = %d, want 1: %#v", len(commands), commands)
	}
	command := strings.Join(commands[0], " ")
	for _, want := range []string{"ssh -p 12345 client@203.0.113.10", "site_path=/www/client/public", "rm -f \"$mailpit_file\"", kinstaMUPluginsZipURL, "kinsta-mu-plugins.php", "cp -R \"$tmp/extract/kinsta-mu-plugins\" \"$plugin_dir\"", "KINSTAMU_WHITELABEL", "NF_WP_CONFIG_FILE", "tempnam($dir, '.nf-wp-config-')", "rename($tmp, $configFile)"} {
		if !strings.Contains(command, want) {
			t.Fatalf("kinsta repair command missing %q: %#v", want, commands[0])
		}
	}
	for _, unwanted := range []string{".bak", "backup"} {
		if strings.Contains(command, unwanted) {
			t.Fatalf("kinsta repair command contains persistent backup marker %q: %#v", unwanted, commands[0])
		}
	}
}

func TestRunSiteRepairKinstaMissingIdentityPromptsWithoutExecuting(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev", "dnsimple_account_id": "acct123"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "provider-site.kinsta", "name": "provider-site", "project_slug": "provider-site", "env": "live", "hostname": "www.example.com", "url": "https://www.example.com/", "path": "/www/provider/public", "domains": []map[string]any{{"name": "www.example.com", "role": "primary", "management": "external", "status": "active"}, {"name": "provider-site.kinsta.cloud", "role": "secondary", "management": "internal", "status": "active"}}, "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "provider"}, "kinsta": map[string]any{"site_id": "ksite123", "slug": "provider-site", "environment_id": "kenv-live", "domain_id": "public-domain"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	oldInteractive := siteIsInteractiveFn
	oldPrompt := siteAddPromptStringFn
	oldRepair := kinstaRepairIdentityFn
	siteIsInteractiveFn = func() bool { return true }
	var prompt, promptDefault string
	siteAddPromptStringFn = func(label, defaultValue string, allowBlank bool) (string, error) {
		prompt, promptDefault = label, defaultValue
		return "client", nil
	}
	kinstaRepairIdentityFn = func(siteRepairPlan) (kinstaSiteAddEnvPlan, error) {
		t.Fatal("dry-run called Kinsta identity repair")
		return kinstaSiteAddEnvPlan{}, nil
	}
	t.Cleanup(func() {
		siteIsInteractiveFn = oldInteractive
		siteAddPromptStringFn = oldPrompt
		kinstaRepairIdentityFn = oldRepair
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "repair", "provider-site.kinsta", "--dry-run"}); got != 0 {
			t.Fatalf("Run(site repair dry-run) = %d, want 0", got)
		}
	})
	if prompt != "Project slug" || promptDefault != "provider-site" {
		t.Fatalf("project prompt = %q default %q", prompt, promptDefault)
	}
	assertContainsInOrder(t, output, []string{"project:  client", "Kinsta slug: provider-site", "identity: client.kinsta.nonfiction.dev", "mode:     dry-run", "add or verify Kinsta identity domain client.kinsta.nonfiction.dev"})
}

func TestRunSiteRepairKinstaReconcilesIdentityAndRekeysCache(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev", "dnsimple_account_id": "acct123"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "kinsta", "site_id": "provider-site.kinsta", "env_id": "provider-site.kinsta:live", "name": "provider-site", "project_slug": "provider-site", "env": "live", "hostname": "www.example.com", "url": "https://www.example.com/", "primary_domain": "www.example.com", "path": "/www/provider/public", "domains": []map[string]any{{"name": "www.example.com", "role": "primary", "management": "external", "status": "active"}, {"name": "provider-site.kinsta.cloud", "role": "secondary", "management": "internal", "status": "active"}}, "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "provider"}, "kinsta": map[string]any{"site_id": "ksite123", "slug": "provider-site", "environment_id": "kenv-live", "domain_id": "public-domain"}},
		{"provider": "kinsta", "site_id": "provider-site.kinsta", "env_id": "provider-site.kinsta:staging", "name": "provider-site", "project_slug": "provider-site", "env": "staging", "hostname": "provider-site-staging.kinsta.cloud", "url": "https://provider-site-staging.kinsta.cloud/", "path": "/www/provider_staging/public", "domains": []map[string]any{{"name": "provider-site-staging.kinsta.cloud", "role": "primary", "management": "internal", "status": "active"}, {"name": "client-staging.kinsta.nonfiction.dev", "role": "secondary", "management": "internal", "status": "active"}}, "ssh": map[string]any{"host": "203.0.113.11", "port": "12346", "user": "provider"}, "kinsta": map[string]any{"site_id": "ksite123", "slug": "provider-site", "environment_id": "kenv-staging", "domain_id": "staging-primary"}},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	oldRepair := kinstaRepairIdentityFn
	oldSSH := runSSHCommandFn
	var repairPlan siteRepairPlan
	kinstaRepairIdentityFn = func(plan siteRepairPlan) (kinstaSiteAddEnvPlan, error) {
		repairPlan = plan
		return kinstaSiteAddEnvPlan{DomainID: "identity-domain", DomainEntries: []map[string]any{{"name": "www.example.com", "role": "primary", "management": "external", "status": "active", "domain_id": "public-domain"}, {"name": "client.kinsta.nonfiction.dev", "role": "secondary", "management": "internal", "status": "active", "domain_id": "identity-domain"}}}, nil
	}
	sshCalls := 0
	runSSHCommandFn = func([]string) error { sshCalls++; return nil }
	t.Cleanup(func() {
		kinstaRepairIdentityFn = oldRepair
		runSSHCommandFn = oldSSH
	})

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "repair", "provider-site.kinsta", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site repair execute) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, output, []string{"Kinsta slug: provider-site", "identity: client.kinsta.nonfiction.dev", "mode:     execute", "re-key local Kinsta cache as client.kinsta:live", "Site repaired."})
	if repairPlan.ProjectSlug != "client" || repairPlan.KinstaSiteID != "ksite123" || repairPlan.KinstaEnvID != "kenv-live" || !repairPlan.ReconcileIdentity {
		t.Fatalf("repair plan = %#v", repairPlan)
	}
	if sshCalls != 1 {
		t.Fatalf("SSH calls = %d, want 1", sshCalls)
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("site records = %#v", records)
	}
	for _, record := range records {
		env := siteEnvName(record)
		if got := firstRecordString(record, "site_id"); got != "client.kinsta" {
			t.Fatalf("%s site_id = %q", env, got)
		}
		if got := firstRecordString(record, "project_slug"); got != "client" {
			t.Fatalf("%s project_slug = %q", env, got)
		}
		if got, want := firstRecordString(record, "env_id"), canonicalEnvID("client.kinsta", env); got != want {
			t.Fatalf("%s env_id = %q, want %q", env, got, want)
		}
		if env == "live" {
			if got := firstRecordString(record, "hostname"); got != "www.example.com" {
				t.Fatalf("live hostname = %q", got)
			}
			if got := firstRecordString(record, "internal_hostname"); got != "client.kinsta.nonfiction.dev" {
				t.Fatalf("live internal hostname = %q", got)
			}
			if got := siteKinstaID(record, "domain_id"); got != "public-domain" {
				t.Fatalf("live primary domain id = %q, want preserved public-domain", got)
			}
		}
	}
}

func TestRepairKinstaIdentityPreservesExternalPrimary(t *testing.T) {
	domainListCalls := 0
	primaryCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /sites/ksite123/environments":
			_ = json.NewEncoder(w).Encode(map[string]any{"site": map[string]any{"environments": []map[string]any{{"id": "kenv-live", "name": "live", "primaryDomain": map[string]any{"id": "public-domain", "name": "www.example.com"}}}}})
		case "GET /sites/environments/kenv-live/domains":
			domainListCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{{"id": "generated-domain", "name": "provider-site.kinsta.cloud", "is_primary": false}, {"id": "public-domain", "name": "www.example.com", "is_primary": false}, {"id": "identity-domain", "name": "client.kinsta.nonfiction.dev", "is_primary": false}}}})
		case "GET /sites/environments/domains/identity-domain/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"pointing_records": []map[string]any{{"name": "client.kinsta.nonfiction.dev", "type": "A", "content": "203.0.113.10", "ttl": 300}}}})
		case "PUT /sites/environments/kenv-live/change-primary-domain":
			primaryCalls++
			http.Error(w, "must not change external primary", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KINSTA_API_KEY", "kinsta-token")
	t.Setenv("DNSIMPLE_TOKEN", "dns-token")
	t.Setenv("KINSTA_BASE_URL", server.URL)
	oldUpsert := upsertDNSRecordFn
	upsertDNSRecordFn = func(token, accountID, zone, name, recordType, content string, ttl int) error { return nil }
	t.Cleanup(func() { upsertDNSRecordFn = oldUpsert })
	stubDNSTypedDeletes(t)

	result, err := repairKinstaIdentity(siteRepairPlan{ProjectSlug: "client", CanonicalSiteID: "client.kinsta", Env: "live", KinstaSiteID: "ksite123", KinstaSlug: "provider-site", KinstaEnvID: "kenv-live", IdentityDomain: "client.kinsta.nonfiction.dev", BaseDomain: "nonfiction.dev", DNSZone: "nonfiction.dev", DNSAccountID: "acct123"})
	if err != nil {
		t.Fatalf("repairKinstaIdentity() error = %v", err)
	}
	if domainListCalls < 3 {
		t.Fatalf("domain list calls = %d, want at least 3", domainListCalls)
	}
	if primaryCalls != 0 {
		t.Fatalf("primary domain calls = %d, want 0", primaryCalls)
	}
	if result.DomainID != "identity-domain" {
		t.Fatalf("identity domain id = %q", result.DomainID)
	}
	if len(result.DomainEntries) != 3 || firstRecordString(result.DomainEntries[0], "role") != "secondary" || firstRecordString(result.DomainEntries[1], "name") != "www.example.com" || firstRecordString(result.DomainEntries[1], "role") != "primary" || firstRecordString(result.DomainEntries[2], "role") != "secondary" {
		t.Fatalf("domain entries = %#v", result.DomainEntries)
	}
}

func TestRunSiteRepairKinstaRejectsConflictingCanonicalCacheClaim(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev", "dnsimple_account_id": "acct123"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "kinsta", "site_id": "provider-site.kinsta", "env": "live", "url": "https://www.example.com", "path": "/www/provider/public", "ssh": map[string]any{"host": "203.0.113.10", "user": "provider"}, "kinsta": map[string]any{"site_id": "ksite123", "slug": "provider-site", "environment_id": "kenv-live"}},
		{"provider": "kinsta", "site_id": "client.kinsta", "project_slug": "client", "env": "live", "kinsta": map[string]any{"site_id": "other-site", "environment_id": "other-live"}},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	stderr := captureStderr(t, func() {
		if got := Run([]string{"site", "repair", "provider-site.kinsta", "--project-slug", "client", "--dry-run", "--non-interactive"}); got != 1 {
			t.Fatalf("Run(site repair conflict) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, `another Kinsta site (other-site) already claims canonical site "client.kinsta"`) {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestKinstaRemoteResolutionFallsBackToSiteSlugNotPath(t *testing.T) {
	record := map[string]any{
		"provider": "kinsta",
		"site_id":  "arpisnorth.kinsta",
		"env":      "live",
		"url":      "https://www.example.com/",
		"path":     "/www/arpisnorth_433/public",
		"database": "arpisnorth_433",
		"ssh":      map[string]any{"host": "203.0.113.10", "port": "12345"},
		"kinsta":   map[string]any{"slug": "arpisnorth"},
	}
	target, err := envRemoteSyncTargetFromSiteRecord(record, "repair", "arpisnorth.kinsta", "live")
	if err != nil {
		t.Fatalf("envRemoteSyncTargetFromSiteRecord() error = %v", err)
	}
	if got, want := target.SSHUser, "arpisnorth"; got != want {
		t.Fatalf("target SSHUser = %q, want %q", got, want)
	}
	args, err := kinstaSiteEnvSSHArgs(record, "wp", []string{"cache", "flush"})
	if err != nil {
		t.Fatalf("kinstaSiteEnvSSHArgs() error = %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "arpisnorth@203.0.113.10") {
		t.Fatalf("ssh args = %q, want Kinsta slug user", joined)
	}
	if strings.Contains(joined, "arpisnorth_433@") {
		t.Fatalf("ssh args = %q, must not derive user from webroot path", joined)
	}
}

func TestRunSiteRepairLinodeRewritesInternalVhostWithCache(t *testing.T) {
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
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "linode", "site_id": "foobar.app1-linode", "env_id": "foobar.app1-linode:live", "name": "foobar", "env": "live", "target": "app1-linode", "hostname": "foobar.app1-linode.nonfiction.dev", "url": "https://foobar.app1-linode.nonfiction.dev", "path": "/var/www/sites/foobar/public", "php_version": "8.3", "domains": []map[string]any{{"name": "www.foobar.com", "role": "primary", "management": "external", "status": "active"}}, "ssh": map[string]any{"user": "nonfiction", "host": "app1-linode.nonfiction.dev", "port": "22"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	var commands [][]string
	oldRunSSHCommand := runSSHCommandFn
	runSSHCommandFn = func(args []string) error {
		commands = append(commands, append([]string(nil), args...))
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSHCommand })

	output := captureStdout(t, func() {
		if got := Run([]string{"site", "repair", "foobar.app1-linode", "--execute", "--yes", "--non-interactive"}); got != 0 {
			t.Fatalf("Run(site repair linode) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, output, []string{"Site repair plan:", "env:      foobar.app1-linode:live", "provider: linode", "target:   app1-linode", "mode:     execute", "install or refresh the nf Linode cache MU plugin", "warning:  cached external domain vhosts are not rewritten", "> ssh -p 22 nonfiction@app1-linode.nonfiction.dev '<sudo site repair script>'", "Site repaired."})
	if len(commands) != 1 {
		t.Fatalf("commands len = %d, want 1: %#v", len(commands), commands)
	}
	command := strings.Join(commands[0], " ")
	for _, want := range []string{"sudo bash -c", "site_path=/var/www/sites/foobar/public", "host_name=foobar.app1-linode.nonfiction.dev", "file_slugs=(foobar.app1-linode.live foobar.app1-linode)", "nf_linode_write_cache_snippets", "cache_zone=$(nf_linode_ensure_cache_config \"$site_path\")", "nf_linode_install_cache_mu_plugin \"$site_path\"", "rm -f \"$site_path/wp-content/mu-plugins/nf-mailpit.php\"", "Plugin Name: nf Server Cache", "Server Cache", "server-tools", "dashicons-cloud", ", 3.1);", "Clear All Caches", "Clear Site Cache", "Clear Object Cache", "NF_LINODE_CACHE_AUTOPURGE_OPTION", "transition_post_status", "nf_linode_cache_clear_object", "top-secondary", "basic_auth_snippet=\"/etc/nginx/snippets/nf-basic-auth-$selected_file_slug.conf\"", "if [ -f \"$basic_auth_snippet\" ]; then printf", "include /etc/nginx/snippets/nf-fastcgi-cache-bypass.conf;", "fastcgi_cache $cache_zone;", "include /etc/nginx/snippets/nf-fastcgi-cache.conf;", "nginx -t", "systemctl reload nginx", "systemctl reload \"php${php_version}-fpm\" || systemctl restart \"php${php_version}-fpm\""} {
		if !strings.Contains(command, want) {
			t.Fatalf("linode repair command missing %q: %#v", want, commands[0])
		}
	}
	if strings.Contains(command, "systemctl reload \"php${php_version}-fpm\" || systemctl restart \"php${php_version}-fpm\" || true") {
		t.Fatalf("linode repair command masks php-fpm restart failures: %#v", commands[0])
	}
	stderr := captureStderr(t, func() {
		if got := Run([]string{"site", "repair", "foobar.app1-linode", "--project-slug", "foobar", "--dry-run"}); got != 1 {
			t.Fatalf("Run(site repair linode project slug) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "--project-slug is only valid when repairing a Kinsta env") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestLoadWordPressConfigDefinesResolvesValuesWithoutLeakingSecrets(t *testing.T) {
	t.Setenv("CLIENT_WPML_SITE_KEY", "wpml-secret-key")
	t.Setenv("CLIENT_SHARED_LICENSE", "shared-secret")
	metadata := &project.Manifest{
		WordPress: project.WordPress{
			Defines: []any{
				map[string]any{
					"name": "SHARED_LICENSE_KEY",
					"env":  "CLIENT_SHARED_LICENSE",
				},
				map[string]any{
					"name":  "TOP_LEVEL_FALLBACK",
					"value": "shared-value",
					"values": map[string]any{
						"production": map[string]any{"value": "production-value"},
					},
				},
				map[string]any{
					"name": "WP_ENVIRONMENT_TYPE",
					"values": map[string]any{
						"local":      map[string]any{"value": "local"},
						"production": map[string]any{"value": "production"},
						"default":    map[string]any{"value": "staging"},
					},
				},
				map[string]any{
					"name": "OTGS_INSTALLER_SITE_KEY_WPML",
					"values": map[string]any{
						"production": map[string]any{"env": "CLIENT_WPML_SITE_KEY"},
						"default":    map[string]any{"value": "fallback-key"},
					},
				},
			},
		},
	}

	remoteDefines, err := loadWordPressConfigDefines("", metadata, wpConfigDefineSelector{RemoteName: "production", EnvID: "client-kinsta:live", Env: "live"})
	if err != nil {
		t.Fatalf("loadWordPressConfigDefines(remote) error = %v", err)
	}
	if got, want := len(remoteDefines), 4; got != want {
		t.Fatalf("remote defines len = %d, want %d: %#v", got, want, remoteDefines)
	}
	byName := map[string]wpConfigDefine{}
	for _, define := range remoteDefines {
		byName[define.Name] = define
	}
	if got, want := byName["OTGS_INSTALLER_SITE_KEY_WPML"].PHPValue, "'wpml-secret-key'"; got != want {
		t.Fatalf("OTGS PHPValue = %q, want %q", got, want)
	}
	if got, want := byName["OTGS_INSTALLER_SITE_KEY_WPML"].Source, "legacy env CLIENT_WPML_SITE_KEY"; got != want {
		t.Fatalf("OTGS Source = %q, want %q", got, want)
	}
	if got, want := byName["SHARED_LICENSE_KEY"].PHPValue, "'shared-secret'"; got != want {
		t.Fatalf("SHARED_LICENSE_KEY PHPValue = %q, want %q", got, want)
	}
	if got, want := byName["TOP_LEVEL_FALLBACK"].PHPValue, "'production-value'"; got != want {
		t.Fatalf("TOP_LEVEL_FALLBACK PHPValue = %q, want %q", got, want)
	}
	if got, want := byName["WP_ENVIRONMENT_TYPE"].PHPValue, "'production'"; got != want {
		t.Fatalf("WP_ENVIRONMENT_TYPE PHPValue = %q, want %q", got, want)
	}
	listOutput := captureStdout(t, func() { _ = cmdDefineList(metadata) })
	for _, want := range []string{"OTGS_INSTALLER_SITE_KEY_WPML", "legacy env CLIENT_WPML_SITE_KEY", "SHARED_LICENSE_KEY", "legacy env CLIENT_SHARED_LICENSE"} {
		if !strings.Contains(listOutput, want) {
			t.Fatalf("define list output missing %q:\n%s", want, listOutput)
		}
	}
	for _, secret := range []string{"wpml-secret-key", "shared-secret"} {
		if strings.Contains(listOutput, secret) {
			t.Fatalf("define list leaked secret %q:\n%s", secret, listOutput)
		}
	}

	localDefines, err := loadWordPressConfigDefines("", metadata, wpConfigDefineSelector{Local: true})
	if err != nil {
		t.Fatalf("loadWordPressConfigDefines(local) error = %v", err)
	}
	localByName := map[string]wpConfigDefine{}
	for _, define := range localDefines {
		localByName[define.Name] = define
	}
	if got, want := localByName["OTGS_INSTALLER_SITE_KEY_WPML"].PHPValue, "'fallback-key'"; got != want {
		t.Fatalf("local OTGS PHPValue = %q, want %q", got, want)
	}
	if got, want := localByName["TOP_LEVEL_FALLBACK"].PHPValue, "'shared-value'"; got != want {
		t.Fatalf("local TOP_LEVEL_FALLBACK PHPValue = %q, want %q", got, want)
	}
	if got, want := localByName["WP_ENVIRONMENT_TYPE"].PHPValue, "'local'"; got != want {
		t.Fatalf("local WP_ENVIRONMENT_TYPE PHPValue = %q, want %q", got, want)
	}

	missingEnvMetadata := &project.Manifest{WordPress: project.WordPress{Defines: []any{map[string]any{"name": "OTGS_INSTALLER_SITE_KEY_WPML", "env": "MISSING_WPML_SITE_KEY"}}}}
	_, err = loadWordPressConfigDefines("", missingEnvMetadata, wpConfigDefineSelector{Local: true})
	if err == nil || !strings.Contains(err.Error(), "Expected MISSING_WPML_SITE_KEY") {
		t.Fatalf("missing env error = %v, want Expected MISSING_WPML_SITE_KEY", err)
	}
	reservedMetadata := &project.Manifest{WordPress: project.WordPress{Defines: []any{map[string]any{"name": "KINSTAMU_WHITELABEL", "value": true}}}}
	_, err = loadWordPressConfigDefines("", reservedMetadata, wpConfigDefineSelector{Local: true})
	if err == nil || !strings.Contains(err.Error(), "KINSTAMU_WHITELABEL is provider-owned") {
		t.Fatalf("reserved define load error = %v, want provider-owned error", err)
	}
}

func TestRenderWPConfigDefineScriptIsAtomicAndDoesNotCreateBackups(t *testing.T) {
	script := renderWPConfigDefineScript("/www/client/public", []wpConfigDefine{
		kinstaWhitelabelWPConfigDefine(),
		{Name: "OTGS_INSTALLER_SITE_KEY_WPML", PHPValue: "'wpml-secret-key'", Source: "env CLIENT_WPML_SITE_KEY", Mode: wpConfigDefineModeForce},
	})
	for _, want := range []string{"site_path=/www/client/public", "KINSTAMU_WHITELABEL", "replace_false", "OTGS_INSTALLER_SITE_KEY_WPML", wpConfigProjectBlockBegin, wpConfigProjectBlockEnd, "nf_wp_config_strip_legacy_managed_defines", "/* That's all, stop editing! Happy publishing. */", "wp-settings.php", "preg_match_all", "Refusing to manage duplicate wp-config define", "already exists outside the nf-managed block", "tempnam($dir, '.nf-wp-config-')", "rename($tmp, $configFile)", "fileperms($configFile)", "fileowner($configFile)", "filegroup($configFile)"} {
		if !strings.Contains(script, want) {
			t.Fatalf("wp-config script missing %q:\n%s", want, script)
		}
	}
	providerScript := renderWPConfigProviderDefineScript("/www/client/public", []wpConfigDefine{kinstaWhitelabelWPConfigDefine()})
	for _, want := range []string{wpConfigProviderBlockBegin, wpConfigProviderBlockEnd, "KINSTAMU_WHITELABEL", "provider"} {
		if !strings.Contains(providerScript, want) {
			t.Fatalf("wp-config provider script missing %q:\n%s", want, providerScript)
		}
	}
	for _, unwanted := range []string{".bak", "backup"} {
		if strings.Contains(script, unwanted) {
			t.Fatalf("wp-config script contains persistent backup marker %q:\n%s", unwanted, script)
		}
	}
	statusScript := renderWPConfigDefineStatusScript("/www/client/public", []wpConfigDefine{{Name: "OTGS_INSTALLER_SITE_KEY_WPML", PHPValue: "'wpml-secret-key'", Source: "env CLIENT_WPML_SITE_KEY"}})
	for _, want := range []string{"preg_match_all", "duplicate"} {
		if !strings.Contains(statusScript, want) {
			t.Fatalf("wp-config status script missing %q:\n%s", want, statusScript)
		}
	}
	if got := defineStatusExitCode("OTGS_INSTALLER_SITE_KEY_WPML\tenv CLIENT_WPML_SITE_KEY\tduplicate\n"); got != 1 {
		t.Fatalf("defineStatusExitCode(duplicate) = %d, want 1", got)
	}
}

func TestRenderWPConfigDefineScriptMigratesManagedBlocks(t *testing.T) {
	if _, err := exec.LookPath("php"); err != nil {
		t.Skip("php not available")
	}
	sitePath := t.TempDir()
	configPath := filepath.Join(sitePath, "wp-config.php")
	initial := `<?php
define('MANUAL_KEEP', 'yes');

/* nf-managed wp-config defines */
define('AGENCY_NAME', 'old agency');

/* nf-managed wp-config defines */
define('REMOVED_DEFINE', 'remove me');

/* nf-managed wp-config defines: begin */
define('OLD_BLOCK_DEFINE', 'remove me too');
/* nf-managed wp-config defines: end */

/* nf-managed provider wp-config defines: begin */
define('KINSTAMU_WHITELABEL', true);
/* nf-managed provider wp-config defines: end */

/* That's all, stop editing! Happy publishing. */
require_once ABSPATH . 'wp-settings.php';
`
	if err := os.WriteFile(configPath, []byte(initial), 0644); err != nil {
		t.Fatalf("WriteFile(wp-config.php) error = %v", err)
	}
	runWPConfigScriptForTest(t, renderWPConfigDefineScript(sitePath, []wpConfigDefine{{Name: "AGENCY_NAME", PHPValue: "'new agency'", Source: "literal value", Mode: wpConfigDefineModeForce}}))
	updatedBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(wp-config.php) error = %v", err)
	}
	updated := string(updatedBytes)
	for _, want := range []string{wpConfigProjectBlockBegin, "define('AGENCY_NAME', 'new agency');", wpConfigProjectBlockEnd, "define('MANUAL_KEEP', 'yes');", wpConfigProviderBlockBegin, "define('KINSTAMU_WHITELABEL', true);", wpConfigProviderBlockEnd, "wp-settings.php"} {
		if !strings.Contains(updated, want) {
			t.Fatalf("updated wp-config missing %q:\n%s", want, updated)
		}
	}
	for _, notWant := range []string{"REMOVED_DEFINE", "OLD_BLOCK_DEFINE", "old agency", "/* nf-managed wp-config defines */"} {
		if strings.Contains(updated, notWant) {
			t.Fatalf("updated wp-config contains %q:\n%s", notWant, updated)
		}
	}

	runWPConfigScriptForTest(t, renderWPConfigDefineScript(sitePath, nil))
	prunedBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(wp-config.php pruned) error = %v", err)
	}
	pruned := string(prunedBytes)
	for _, notWant := range []string{wpConfigProjectBlockBegin, "AGENCY_NAME", wpConfigProjectBlockEnd} {
		if strings.Contains(pruned, notWant) {
			t.Fatalf("pruned wp-config contains %q:\n%s", notWant, pruned)
		}
	}
	for _, want := range []string{"define('MANUAL_KEEP', 'yes');", wpConfigProviderBlockBegin, "define('KINSTAMU_WHITELABEL', true);", wpConfigProviderBlockEnd} {
		if !strings.Contains(pruned, want) {
			t.Fatalf("pruned wp-config missing %q:\n%s", want, pruned)
		}
	}
}

func runWPConfigScriptForTest(t *testing.T, script string) {
	t.Helper()
	cmd := exec.Command("sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wp-config script failed: %v\n%s\nscript:\n%s", err, output, script)
	}
}

func TestRunDefineStatusRemoteHidesSecret(t *testing.T) {
	t.Setenv("NF_STATE_HOME", t.TempDir())
	t.Setenv("CLIENT_WPML_SITE_KEY", "wpml-secret-key")
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	setupTestNFProjectWithMetadata(t, map[string]any{
		"version": 2,
		"project": map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{
			"themes":  []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}},
			"defines": []any{map[string]any{"name": "OTGS_INSTALLER_SITE_KEY_WPML", "env": "CLIENT_WPML_SITE_KEY"}},
		},
		"remotes": map[string]any{"production": "client-kinsta:live"},
	})
	var capturedArgs []string
	var capturedScript string
	oldRunSSHStdinOutput := runSSHStdinOutputFn
	runSSHStdinOutputFn = func(args []string, script string) ([]byte, error) {
		capturedArgs = append([]string(nil), args...)
		capturedScript = script
		return []byte("OTGS_INSTALLER_SITE_KEY_WPML\tenv CLIENT_WPML_SITE_KEY\tmissing\n"), nil
	}
	t.Cleanup(func() { runSSHStdinOutputFn = oldRunSSHStdinOutput })

	output := captureStdout(t, func() {
		if got := Run([]string{"define", "status", "production"}); got != 0 {
			t.Fatalf("Run(define status production) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, output, []string{"Define status:", "remote:   production", "provider: kinsta", "path:     /www/client/public/wp-config.php", "OTGS_INSTALLER_SITE_KEY_WPML", "env CLIENT_WPML_SITE_KEY", "missing"})
	if strings.Contains(output, "wpml-secret-key") {
		t.Fatalf("define status leaked secret:\n%s", output)
	}
	if got, want := strings.Join(capturedArgs, " "), "ssh -p 12345 client@203.0.113.10 bash -s"; got != want {
		t.Fatalf("captured args = %q, want %q", got, want)
	}
	for _, want := range []string{"site_path=/www/client/public", "OTGS_INSTALLER_SITE_KEY_WPML", "'wpml-secret-key'"} {
		if !strings.Contains(capturedScript, want) {
			t.Fatalf("captured status script missing %q:\n%s", want, capturedScript)
		}
	}
}

func TestRunDefineSyncRemoteUsesStdinScript(t *testing.T) {
	t.Setenv("NF_STATE_HOME", t.TempDir())
	t.Setenv("CLIENT_WPML_SITE_KEY", "wpml-secret-key")
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	setupTestNFProjectWithMetadata(t, map[string]any{
		"version": 2,
		"project": map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{
			"themes":  []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}},
			"defines": []any{map[string]any{"name": "OTGS_INSTALLER_SITE_KEY_WPML", "env": "CLIENT_WPML_SITE_KEY"}},
		},
		"remotes": map[string]any{"production": "client-kinsta:live"},
	})
	var capturedArgs []string
	var capturedScript string
	oldRunSSHStdin := runSSHStdinCommandFn
	runSSHStdinCommandFn = func(args []string, script string) error {
		capturedArgs = append([]string(nil), args...)
		capturedScript = script
		return nil
	}
	t.Cleanup(func() { runSSHStdinCommandFn = oldRunSSHStdin })

	output := captureStdout(t, func() {
		if got := Run([]string{"define", "sync", "production"}); got != 0 {
			t.Fatalf("Run(define sync production) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, output, []string{"Define sync:", "remote:   production", "provider: kinsta", "> ssh -p 12345 client@203.0.113.10 '<sync defines>'", "Defines synced."})
	if strings.Contains(output, "wpml-secret-key") {
		t.Fatalf("define sync output leaked secret:\n%s", output)
	}
	if got, want := strings.Join(capturedArgs, " "), "ssh -p 12345 client@203.0.113.10 bash -s"; got != want {
		t.Fatalf("captured args = %q, want %q", got, want)
	}
	for _, want := range []string{"site_path=/www/client/public", "OTGS_INSTALLER_SITE_KEY_WPML", "'wpml-secret-key'", "tempnam($dir, '.nf-wp-config-')", "rename($tmp, $configFile)"} {
		if !strings.Contains(capturedScript, want) {
			t.Fatalf("captured script missing %q:\n%s", want, capturedScript)
		}
	}
}

func TestRunDefineSetRemoveWritesSharedAndSelectorValues(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	t.Setenv("NF_PASSWORD_SALT", "nf_test-define-secret-salt")
	root := setupTestNFProjectWithMetadata(t, map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}},
	})
	stderr := captureStderr(t, func() {
		if got := Run([]string{"define", "set", "KINSTAMU_WHITELABEL", "true"}); got != 1 {
			t.Fatalf("Run(define set KINSTAMU_WHITELABEL) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "define KINSTAMU_WHITELABEL is provider-owned") {
		t.Fatalf("reserved define stderr = %q", stderr)
	}

	if output := captureStdout(t, func() {
		if got := Run([]string{"define", "set", "SOME_PLUGIN_CONSTANT", "true"}); got != 0 {
			t.Fatalf("Run(define set literal) = %d, want 0", got)
		}
	}); !strings.Contains(output, "Set define SOME_PLUGIN_CONSTANT.") {
		t.Fatalf("define set literal output = %q", output)
	}
	oldStdin := defineSecretStdin
	t.Cleanup(func() { defineSecretStdin = oldStdin })
	defineSecretStdin = strings.NewReader("shared-secret\n")
	if got := Run([]string{"define", "set", "OTGS_INSTALLER_SITE_KEY_WPML", "--secret-stdin"}); got != 0 {
		t.Fatalf("Run(define set secret) = %d, want 0", got)
	}
	defineSecretStdin = strings.NewReader("production-secret\n")
	if got := Run([]string{"define", "set", "OTGS_INSTALLER_SITE_KEY_WPML", "--secret-stdin", "--for", "production"}); got != 0 {
		t.Fatalf("Run(define set secret for production) = %d, want 0", got)
	}

	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		t.Fatalf("loadProjectMetadataOrError() error = %v", err)
	}
	defines := metadata.WordPress.Defines
	items := map[string]map[string]any{}
	for _, raw := range defines {
		item := raw.(map[string]any)
		items[recordValueString(item["name"])] = item
	}
	if got, want := items["SOME_PLUGIN_CONSTANT"]["value"], true; got != want {
		t.Fatalf("SOME_PLUGIN_CONSTANT value = %#v, want %#v", got, want)
	}
	wpml := items["OTGS_INSTALLER_SITE_KEY_WPML"]
	if _, ok := wpml["secret"]; ok {
		t.Fatalf("WPML define kept top-level secret after selector promotion: %#v", wpml)
	}
	values := wpml["values"].(map[string]any)
	defaultRef := recordValueString(values["default"].(map[string]any)["secret"])
	productionRef := recordValueString(values["production"].(map[string]any)["secret"])
	if !defineSecretRefPattern.MatchString(defaultRef) {
		t.Fatalf("WPML default secret ref = %q", defaultRef)
	}
	if !defineSecretRefPattern.MatchString(productionRef) || productionRef == defaultRef {
		t.Fatalf("WPML production secret ref = %q, default = %q", productionRef, defaultRef)
	}

	if got := Run([]string{"define", "rm", "OTGS_INSTALLER_SITE_KEY_WPML", "--for", "production"}); got != 0 {
		t.Fatalf("Run(define rm --for production) = %d, want 0", got)
	}
	metadata, err = loadProjectMetadataOrError(root)
	if err != nil {
		t.Fatalf("loadProjectMetadataOrError() after rm error = %v", err)
	}
	defines = metadata.WordPress.Defines
	items = map[string]map[string]any{}
	for _, raw := range defines {
		item := raw.(map[string]any)
		items[recordValueString(item["name"])] = item
	}
	values = items["OTGS_INSTALLER_SITE_KEY_WPML"]["values"].(map[string]any)
	if _, ok := values["production"]; ok {
		t.Fatalf("WPML production selector still present after remove: %#v", values)
	}
	if got := recordValueString(values["default"].(map[string]any)["secret"]); got != defaultRef {
		t.Fatalf("WPML default secret after remove = %q, want %q", got, defaultRef)
	}
	store, err := loadDefineSecretStore(root, metadata, false)
	if err != nil {
		t.Fatalf("loadDefineSecretStore() after selector remove error = %v", err)
	}
	if _, ok := store.Secrets[productionRef]; ok {
		t.Fatal("removed selector secret remains in nf.age")
	}
	if got := store.Secrets[defaultRef]; got != "shared-secret" {
		t.Fatalf("default encrypted value = %q", got)
	}
	if got := Run([]string{"define", "rm", "OTGS_INSTALLER_SITE_KEY_WPML"}); got != 0 {
		t.Fatalf("Run(define rm shared secret) = %d, want 0", got)
	}
	if _, err := os.Stat(defineSecretStorePath(root)); !os.IsNotExist(err) {
		t.Fatalf("nf.age remains after removing last secret define: %v", err)
	}
}

func TestRunDefineSetInteractiveWizardWritesSecretSelector(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	t.Setenv("NF_PASSWORD_SALT", "nf_test-define-secret-salt")
	root := setupTestNFProjectWithMetadata(t, map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}},
		"remotes":   map[string]any{"production": "client-kinsta:live"},
	})
	oldInteractive := siteIsInteractiveFn
	oldPrompt := definePromptStringFn
	oldPromptSecret := definePromptSecretFn
	oldSelect := defineSelectFn
	oldConfirm := defineConfirmFn
	t.Cleanup(func() {
		siteIsInteractiveFn = oldInteractive
		definePromptStringFn = oldPrompt
		definePromptSecretFn = oldPromptSecret
		defineSelectFn = oldSelect
		defineConfirmFn = oldConfirm
	})
	siteIsInteractiveFn = func() bool { return true }
	prompts := []string{}
	selectTitles := []string{}
	var defineOptions []ui.SelectOption
	var sourceOptions []ui.SelectOption
	var selectorOptions []ui.SelectOption
	confirmPrompt := ""
	definePromptStringFn = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		prompts = append(prompts, prompt)
		switch prompt {
		case "Define name (usually ALL_CAPS)":
			return "SomePluginKey", nil
		default:
			t.Fatalf("unexpected prompt %q", prompt)
			return "", nil
		}
	}
	definePromptSecretFn = func(prompt, defaultValue string) (string, error) {
		if prompt != "Encrypted define value" {
			t.Fatalf("unexpected secret prompt %q", prompt)
		}
		if defaultValue != "" {
			t.Fatalf("secret default = %q, want empty", defaultValue)
		}
		return "wizard-secret", nil
	}
	defineSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		selectTitles = append(selectTitles, title)
		switch title {
		case "Choose a define to set":
			defineOptions = append([]ui.SelectOption(nil), options...)
			return defineSetNewDefine, nil
		case "Choose value source":
			sourceOptions = append([]ui.SelectOption(nil), options...)
			return "secret", nil
		case "Choose where this define applies":
			selectorOptions = append([]ui.SelectOption(nil), options...)
			return "production", nil
		default:
			t.Fatalf("unexpected select %q", title)
			return "", nil
		}
	}
	defineConfirmFn = func(prompt string, defaultYes bool) (bool, error) {
		confirmPrompt = prompt
		if defaultYes {
			t.Fatalf("define uppercase confirmation defaultYes = true, want false")
		}
		return true, nil
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"define", "set"}); got != 0 {
			t.Fatalf("Run(define set wizard) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Set define SomePluginKey for production.") {
		t.Fatalf("define set wizard output = %q", output)
	}
	if !slices.Equal(prompts, []string{"Define name (usually ALL_CAPS)"}) {
		t.Fatalf("prompts = %#v", prompts)
	}
	if !slices.Equal(selectTitles, []string{"Choose a define to set", "Choose where this define applies", "Choose value source"}) {
		t.Fatalf("select titles = %#v", selectTitles)
	}
	if !strings.Contains(confirmPrompt, "ALL_CAPS") || !strings.Contains(confirmPrompt, "SomePluginKey") {
		t.Fatalf("uppercase confirmation prompt = %q", confirmPrompt)
	}
	if len(sourceOptions) != 2 || sourceOptions[0].Value != "literal" || sourceOptions[1].Value != "secret" {
		t.Fatalf("source options = %#v, want literal/secret", sourceOptions)
	}
	if !slices.Equal(defineOptions, []ui.SelectOption{{Value: defineSetNewDefine, Label: "Add a new define..."}}) {
		t.Fatalf("define options = %#v", defineOptions)
	}
	if !slices.ContainsFunc(selectorOptions, func(option ui.SelectOption) bool { return option.Value == "__all__" && option.Default }) {
		t.Fatalf("selector options missing shared default: %#v", selectorOptions)
	}
	if !slices.ContainsFunc(selectorOptions, func(option ui.SelectOption) bool { return option.Value == "local" }) {
		t.Fatalf("selector options missing local: %#v", selectorOptions)
	}
	if !slices.ContainsFunc(selectorOptions, func(option ui.SelectOption) bool {
		return option.Value == "production" && option.Label == "production (client-kinsta:live)"
	}) {
		t.Fatalf("selector options missing production remote: %#v", selectorOptions)
	}
	for _, value := range []string{"default", "live", "staging"} {
		if slices.ContainsFunc(selectorOptions, func(option ui.SelectOption) bool { return option.Value == value }) {
			t.Fatalf("selector options included generic selector %q: %#v", value, selectorOptions)
		}
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		t.Fatalf("loadProjectMetadataOrError() error = %v", err)
	}
	defines := metadata.WordPress.Defines
	item := defines[0].(map[string]any)
	if got, want := recordValueString(item["name"]), "SomePluginKey"; got != want {
		t.Fatalf("wizard define name = %q, want %q", got, want)
	}
	values := item["values"].(map[string]any)
	if got := recordValueString(values["production"].(map[string]any)["secret"]); !defineSecretRefPattern.MatchString(got) {
		t.Fatalf("wizard production secret ref = %q", got)
	}
}

func TestRunDefineSetInteractivePromptsForSecretAndSelector(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	t.Setenv("NF_PASSWORD_SALT", "nf_test-define-secret-salt")
	root := setupTestNFProjectWithMetadata(t, map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}},
		"remotes":   map[string]any{"production": "client-kinsta:live"},
	})
	oldInteractive := siteIsInteractiveFn
	oldPrompt := definePromptStringFn
	oldPromptSecret := definePromptSecretFn
	oldSelect := defineSelectFn
	oldConfirm := defineConfirmFn
	oldStdin := defineSecretStdin
	t.Cleanup(func() {
		siteIsInteractiveFn = oldInteractive
		definePromptStringFn = oldPrompt
		definePromptSecretFn = oldPromptSecret
		defineSelectFn = oldSelect
		defineConfirmFn = oldConfirm
		defineSecretStdin = oldStdin
	})
	siteIsInteractiveFn = func() bool { return true }
	definePromptSecretFn = func(prompt, defaultValue string) (string, error) {
		return "shared-secret", nil
	}
	defineSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		if title != "Choose where this define applies" {
			t.Fatalf("unexpected select %q", title)
		}
		return "production", nil
	}
	defineConfirmFn = func(prompt string, defaultYes bool) (bool, error) {
		t.Fatalf("unexpected confirm %q", prompt)
		return false, nil
	}

	if got := Run([]string{"define", "set", "SHARED_KEY", "--secret", "--for"}); got != 0 {
		t.Fatalf("Run(define set --secret --for) = %d, want 0", got)
	}
	if got := Run([]string{"define", "set", "SCOPED_FLAG", "true", "--for"}); got != 0 {
		t.Fatalf("Run(define set --for) = %d, want 0", got)
	}
	defineSecretStdin = strings.NewReader("stdin-secret\n")
	if got := Run([]string{"define", "set", "STDIN_KEY", "--secret-stdin", "--for"}); got != 0 {
		t.Fatalf("Run(define set --secret-stdin --for) = %d, want 0", got)
	}

	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		t.Fatalf("loadProjectMetadataOrError() error = %v", err)
	}
	defines := metadata.WordPress.Defines
	items := map[string]map[string]any{}
	for _, raw := range defines {
		item := raw.(map[string]any)
		items[recordValueString(item["name"])] = item
	}
	sharedValues := items["SHARED_KEY"]["values"].(map[string]any)
	if got := recordValueString(sharedValues["production"].(map[string]any)["secret"]); !defineSecretRefPattern.MatchString(got) {
		t.Fatalf("SHARED_KEY secret ref = %q", got)
	}
	values := items["SCOPED_FLAG"]["values"].(map[string]any)
	if got, want := values["production"].(map[string]any)["value"], true; got != want {
		t.Fatalf("SCOPED_FLAG production value = %#v, want %#v", got, want)
	}
	stdinValues := items["STDIN_KEY"]["values"].(map[string]any)
	stdinRef := recordValueString(stdinValues["production"].(map[string]any)["secret"])
	store, err := loadDefineSecretStore(root, metadata, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Secrets[stdinRef]; got != "stdin-secret" {
		t.Fatalf("STDIN_KEY encrypted value = %q", got)
	}
}

func TestRunDefineSetMissingArgsRequiresInteractiveTerminal(t *testing.T) {
	setupTestNFProjectWithMetadata(t, map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}},
	})
	oldInteractive := siteIsInteractiveFn
	siteIsInteractiveFn = func() bool { return false }
	t.Cleanup(func() { siteIsInteractiveFn = oldInteractive })

	stderr := captureStderr(t, func() {
		if got := Run([]string{"define", "set"}); got != 1 {
			t.Fatalf("Run(define set non-interactive) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "define set requires a name and value, or a name with --secret") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestRunDefineGetPrintsRawValuesAndRequiresExactSelector(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	t.Setenv("NF_PASSWORD_SALT", "nf_test-define-get-salt")
	t.Setenv("LEGACY_DEFINE_VALUE", "legacy-value")
	setupTestNFProjectWithMetadata(t, map[string]any{
		"version": 2,
		"project": map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "defines": []any{
			map[string]any{"name": "LITERAL_VALUE", "value": "plain-value"},
			map[string]any{"name": "BOOL_VALUE", "value": true},
			map[string]any{"name": "NUMBER_VALUE", "value": 42.5},
			map[string]any{"name": "LEGACY_VALUE", "env": "LEGACY_DEFINE_VALUE"},
			map[string]any{"name": "SCOPED_VALUE", "values": map[string]any{
				"default": map[string]any{"value": "default-value"},
				"local":   map[string]any{"value": "local-value"},
			}},
		}},
	})
	oldStdin := defineSecretStdin
	oldInteractive := siteIsInteractiveFn
	oldSelect := defineSelectFn
	t.Cleanup(func() {
		defineSecretStdin = oldStdin
		siteIsInteractiveFn = oldInteractive
		defineSelectFn = oldSelect
	})
	defineSecretStdin = strings.NewReader("encrypted-value\n")
	if got := Run([]string{"define", "set", "SECRET_VALUE", "--secret-stdin"}); got != 0 {
		t.Fatalf("Run(define set SECRET_VALUE) = %d", got)
	}
	siteIsInteractiveFn = func() bool { return false }

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"define", "get", "LITERAL_VALUE"}, "plain-value\n"},
		{[]string{"define", "get", "BOOL_VALUE"}, "true\n"},
		{[]string{"define", "get", "NUMBER_VALUE"}, "42.5\n"},
		{[]string{"define", "get", "LEGACY_VALUE"}, "legacy-value\n"},
		{[]string{"define", "get", "SECRET_VALUE"}, "encrypted-value\n"},
		{[]string{"define", "get", "SCOPED_VALUE", "--for", "local"}, "local-value\n"},
		{[]string{"define", "get", "SCOPED_VALUE", "--for=local"}, "local-value\n"},
	} {
		output := captureStdout(t, func() {
			if got := Run(tc.args); got != 0 {
				t.Fatalf("Run(%v) = %d", tc.args, got)
			}
		})
		if output != tc.want {
			t.Fatalf("Run(%v) output = %q, want %q", tc.args, output, tc.want)
		}
	}

	stderr := captureStderr(t, func() {
		if got := Run([]string{"define", "get", "SCOPED_VALUE"}); got != 1 {
			t.Fatalf("Run(define get SCOPED_VALUE) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "requires --for") {
		t.Fatalf("missing selector stderr = %q", stderr)
	}
	stderr = captureStderr(t, func() {
		if got := Run([]string{"define", "get", "SCOPED_VALUE", "--for", "staging"}); got != 1 {
			t.Fatalf("Run(define get SCOPED_VALUE --for staging) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "not configured for staging") || strings.Contains(stderr, "default-value") {
		t.Fatalf("unknown selector stderr = %q", stderr)
	}
	stderr = captureStderr(t, func() {
		if got := Run([]string{"define", "get", "LITERAL_VALUE", "--for", "local"}); got != 1 {
			t.Fatalf("Run(define get shared --for local) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "uses a shared value") {
		t.Fatalf("shared selector stderr = %q", stderr)
	}

	siteIsInteractiveFn = func() bool { return true }
	defineSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		switch title {
		case "Choose a define to get":
			want := []ui.SelectOption{
				{Value: "BOOL_VALUE", Label: "BOOL_VALUE"},
				{Value: "LEGACY_VALUE", Label: "LEGACY_VALUE"},
				{Value: "LITERAL_VALUE", Label: "LITERAL_VALUE"},
				{Value: "NUMBER_VALUE", Label: "NUMBER_VALUE"},
				{Value: "SCOPED_VALUE", Label: "SCOPED_VALUE"},
				{Value: "SECRET_VALUE", Label: "SECRET_VALUE"},
			}
			if !slices.Equal(options, want) {
				t.Fatalf("define options = %#v, want %#v", options, want)
			}
			return "SCOPED_VALUE", nil
		case "Choose a selector for SCOPED_VALUE":
			if !slices.Equal(options, []ui.SelectOption{{Value: "default", Label: "default (shared default)"}, {Value: "local", Label: "local"}}) {
				t.Fatalf("selector options = %#v", options)
			}
			return "default", nil
		default:
			t.Fatalf("unexpected select %q", title)
			return "", nil
		}
	}
	output := captureStdout(t, func() {
		if got := Run([]string{"define", "get"}); got != 0 {
			t.Fatalf("interactive Run(define get) = %d", got)
		}
	})
	if output != "default-value\n" {
		t.Fatalf("interactive get output = %q", output)
	}

	stderr = captureStderr(t, func() {
		if got := Run([]string{"define", "add", "OLD_COMMAND", "value"}); got != 1 {
			t.Fatalf("Run(define add) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "unsupported define command") {
		t.Fatalf("define add stderr = %q", stderr)
	}
}

func TestRunDefineGetWithoutArgsReportsNoConfiguredDefines(t *testing.T) {
	setupTestNFProjectWithMetadata(t, map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}},
	})
	oldInteractive := siteIsInteractiveFn
	oldSelect := defineSelectFn
	t.Cleanup(func() {
		siteIsInteractiveFn = oldInteractive
		defineSelectFn = oldSelect
	})
	siteIsInteractiveFn = func() bool { return true }
	defineSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		t.Fatalf("unexpected picker %q with options %#v", title, options)
		return "", nil
	}
	stderr := captureStderr(t, func() {
		if got := Run([]string{"define", "get"}); got != 1 {
			t.Fatalf("Run(define get) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "no defines are configured in nf.json") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestRunDefineSetInteractivePrepopulatesExistingValues(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	t.Setenv("NF_PASSWORD_SALT", "nf_test-define-edit-salt")
	root := setupTestNFProjectWithMetadata(t, map[string]any{
		"version": 2,
		"project": map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "defines": []any{
			map[string]any{"name": "AGENCY_NAME", "value": "old-name"},
			map[string]any{"name": "FEATURE_ENABLED", "value": true},
			map[string]any{"name": "SAMPLE_RATIO", "value": 1.5},
			map[string]any{"name": "SCOPED_VALUE", "values": map[string]any{
				"default": map[string]any{"value": "shared-value"},
				"local":   map[string]any{"value": "local-old"},
			}},
		}},
	})
	oldStdin := defineSecretStdin
	oldInteractive := siteIsInteractiveFn
	oldPrompt := definePromptStringFn
	oldSecretPrompt := definePromptSecretFn
	oldSelect := defineSelectFn
	t.Cleanup(func() {
		defineSecretStdin = oldStdin
		siteIsInteractiveFn = oldInteractive
		definePromptStringFn = oldPrompt
		definePromptSecretFn = oldSecretPrompt
		defineSelectFn = oldSelect
	})
	defineSecretStdin = strings.NewReader("secret-old\n")
	if got := Run([]string{"define", "set", "SECRET_VALUE", "--secret-stdin"}); got != 0 {
		t.Fatalf("Run(define set SECRET_VALUE) = %d", got)
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		t.Fatal(err)
	}
	secretRef := recordValueString(configuredDefineItem(metadata, "SECRET_VALUE")["secret"])

	siteIsInteractiveFn = func() bool { return true }
	definePromptStringFn = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		if prompt != "Define value" || !allowBlank {
			t.Fatalf("unexpected value prompt %q allowBlank=%t", prompt, allowBlank)
		}
		switch defaultValue {
		case "old-name":
			return "new-name", nil
		case "true":
			return "false", nil
		case "1.5":
			return "2.5", nil
		case "local-old":
			return "local-new", nil
		default:
			t.Fatalf("unexpected default value %q", defaultValue)
			return "", nil
		}
	}
	definePromptSecretFn = func(prompt, defaultValue string) (string, error) {
		if prompt != "Encrypted define value" || defaultValue != "secret-old" {
			t.Fatalf("secret prompt = %q default = %q", prompt, defaultValue)
		}
		return "secret-new", nil
	}
	defineSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		switch title {
		case "Choose a define to set":
			want := []ui.SelectOption{
				{Value: "AGENCY_NAME", Label: "AGENCY_NAME"},
				{Value: "FEATURE_ENABLED", Label: "FEATURE_ENABLED"},
				{Value: "SAMPLE_RATIO", Label: "SAMPLE_RATIO"},
				{Value: "SCOPED_VALUE", Label: "SCOPED_VALUE"},
				{Value: "SECRET_VALUE", Label: "SECRET_VALUE"},
				{Value: defineSetNewDefine, Label: "Add a new define..."},
			}
			if !slices.Equal(options, want) {
				t.Fatalf("define options = %#v, want %#v", options, want)
			}
			return "AGENCY_NAME", nil
		case "Choose a selector for SCOPED_VALUE":
			return "local", nil
		default:
			t.Fatalf("unexpected selector %q", title)
			return "", nil
		}
	}

	if got := Run([]string{"define", "set"}); got != 0 {
		t.Fatalf("Run(define set picker) = %d", got)
	}
	for _, name := range []string{"FEATURE_ENABLED", "SAMPLE_RATIO", "SECRET_VALUE", "SCOPED_VALUE"} {
		if got := Run([]string{"define", "set", name}); got != 0 {
			t.Fatalf("Run(define set %s) = %d", name, got)
		}
	}
	metadata, err = loadProjectMetadataOrError(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := configuredDefineItem(metadata, "AGENCY_NAME")["value"]; got != "new-name" {
		t.Fatalf("AGENCY_NAME = %#v", got)
	}
	if got := configuredDefineItem(metadata, "FEATURE_ENABLED")["value"]; got != false {
		t.Fatalf("FEATURE_ENABLED = %#v", got)
	}
	if got := configuredDefineItem(metadata, "SAMPLE_RATIO")["value"]; got != float64(2.5) {
		t.Fatalf("SAMPLE_RATIO = %#v", got)
	}
	scopedValues := configuredDefineItem(metadata, "SCOPED_VALUE")["values"].(map[string]any)
	if got := scopedValues["local"].(map[string]any)["value"]; got != "local-new" {
		t.Fatalf("SCOPED_VALUE local = %#v", got)
	}
	if got := scopedValues["default"].(map[string]any)["value"]; got != "shared-value" {
		t.Fatalf("SCOPED_VALUE default = %#v", got)
	}
	if got := recordValueString(configuredDefineItem(metadata, "SECRET_VALUE")["secret"]); got != secretRef {
		t.Fatalf("SECRET_VALUE ref = %q, want %q", got, secretRef)
	}
	store, err := loadDefineSecretStore(root, metadata, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Secrets[secretRef]; got != "secret-new" {
		t.Fatalf("SECRET_VALUE = %q", got)
	}
}

func TestRunDefineRemoveWithoutArgUsesPicker(t *testing.T) {
	root := setupTestNFProjectWithMetadata(t, map[string]any{
		"version": 2,
		"project": map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "defines": []any{
			map[string]any{"name": "AGENCY_NAME", "value": "nonfiction studios"},
			map[string]any{"name": "OTGS_INSTALLER_SITE_KEY_WPML", "env": "CLIENT_WPML_SITE_KEY"},
		}},
	})
	oldInteractive := siteIsInteractiveFn
	oldSelect := defineSelectFn
	t.Cleanup(func() {
		siteIsInteractiveFn = oldInteractive
		defineSelectFn = oldSelect
	})
	siteIsInteractiveFn = func() bool { return true }
	var selectOptions []ui.SelectOption
	defineSelectFn = func(title string, options []ui.SelectOption) (string, error) {
		if title != "Choose a define to remove" {
			t.Fatalf("unexpected select %q", title)
		}
		selectOptions = append([]ui.SelectOption(nil), options...)
		return "AGENCY_NAME", nil
	}

	output := captureStdout(t, func() {
		if got := Run([]string{"define", "rm"}); got != 0 {
			t.Fatalf("Run(define rm picker) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Removed define AGENCY_NAME.") {
		t.Fatalf("define rm picker output = %q", output)
	}
	if !slices.Equal(selectOptions, []ui.SelectOption{{Value: "AGENCY_NAME", Label: "AGENCY_NAME"}, {Value: "OTGS_INSTALLER_SITE_KEY_WPML", Label: "OTGS_INSTALLER_SITE_KEY_WPML"}}) {
		t.Fatalf("define remove picker options = %#v", selectOptions)
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		t.Fatalf("loadProjectMetadataOrError() error = %v", err)
	}
	defines := metadata.WordPress.Defines
	if len(defines) != 1 {
		t.Fatalf("defines after picker remove = %#v, want one define", defines)
	}
	item := defines[0].(map[string]any)
	if got, want := recordValueString(item["name"]), "OTGS_INSTALLER_SITE_KEY_WPML"; got != want {
		t.Fatalf("remaining define = %q, want %q", got, want)
	}
}

func TestRunDefineRemoveWithoutArgRequiresInteractiveTerminal(t *testing.T) {
	setupTestNFProjectWithMetadata(t, map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "defines": []any{map[string]any{"name": "AGENCY_NAME", "value": "nonfiction studios"}}},
	})
	oldInteractive := siteIsInteractiveFn
	siteIsInteractiveFn = func() bool { return false }
	t.Cleanup(func() { siteIsInteractiveFn = oldInteractive })

	stderr := captureStderr(t, func() {
		if got := Run([]string{"define", "remove"}); got != 1 {
			t.Fatalf("Run(define remove non-interactive) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "define remove requires exactly one name") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestRunSiteRepairNonInteractiveRequiresRefAndExecuteYes(t *testing.T) {
	oldInteractive := siteIsInteractiveFn
	siteIsInteractiveFn = func() bool { return false }
	t.Cleanup(func() { siteIsInteractiveFn = oldInteractive })

	stderr := captureStderr(t, func() {
		if got := Run([]string{"site", "repair", "--non-interactive"}); got != 1 {
			t.Fatalf("Run(site repair non-interactive no ref) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "site repair requires a site or env ref like site.target or site.target:staging") {
		t.Fatalf("stderr = %q, want explicit ref error", stderr)
	}

	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configDir)
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := saveGlobalConfig(map[string]string{"base_domain": "nonfiction.dev", "dnsimple_account_id": "acct123"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}, "kinsta": map[string]any{"site_id": "ksite123", "slug": "client", "environment_id": "kenv-live"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	stderr = captureStderr(t, func() {
		if got := Run([]string{"site", "repair", "client-kinsta", "--execute", "--non-interactive"}); got != 1 {
			t.Fatalf("Run(site repair non-interactive execute without yes) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "Remote execution requires both --execute and --yes in non-interactive mode.") {
		t.Fatalf("stderr = %q, want execute yes error", stderr)
	}

	stderr = captureStderr(t, func() {
		if got := Run([]string{"site", "repair", "client-kinsta", "--dry-run", "--non-interactive"}); got != 1 {
			t.Fatalf("Run(site repair non-interactive missing project slug) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "has no nf identity domain; pass --project-slug <slug> in non-interactive mode") {
		t.Fatalf("stderr = %q, want project slug error", stderr)
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
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}},
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
		for _, want := range []string{"Env push preflight:", "client local env  ──▶  production remote", "FROM local:", "project: client", "TO remote:", "remote:   production", "site:     client-kinsta", "env:      live", "provider: kinsta", "url:      https://www.example.com/", "ssh:      client@203.0.113.10:12345", "mode:     dry-run", "No data was changed. Re-run with --execute to replace the remote database and mutable wp-content."} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("env push stdout missing %q:\n%s", want, stdout)
			}
		}
		if strings.Contains(stdout, "target record:") || strings.Contains(stdout, "target:        kinsta") || strings.Contains(stdout, "environment ssh") {
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
		"version":   2,
		"project":   map[string]any{"slug": "foobar", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}},
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
		for _, want := range []string{"Env pull preflight:", "foobar local env  ◀──  live remote", "TO local:", "project: foobar", "FROM remote:", "remote:   live", "site:     foobar.app4-linode", "provider: linode", "target:   app4-linode", "ssh:      nonfiction@app4-linode.nonfiction.dev", "target record: app4-linode", "mode:     dry-run", "No data was changed. Re-run with --execute to replace the local database and mutable wp-content."} {
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
	if selectedTitle != "Choose a remote to pull from" || !strings.Contains(stdout, "live remote") {
		t.Fatalf("env pull picker title/output = %q /\n%s", selectedTitle, stdout)
	}
}

func TestProjectRemoteSelectTitleUsesDirectionalPushPullWording(t *testing.T) {
	if got := projectRemoteSelectTitle("push"); got != "Choose a remote to push to" {
		t.Fatalf("projectRemoteSelectTitle(push) = %q", got)
	}
	if got := projectRemoteSelectTitle("pull"); got != "Choose a remote to pull from" {
		t.Fatalf("projectRemoteSelectTitle(pull) = %q", got)
	}
	if got := projectRemoteSelectTitle("remove"); got != "Choose a remote to remove" {
		t.Fatalf("projectRemoteSelectTitle(remove) = %q", got)
	}
}

func TestRunThemeDeployDryRunPlansPackagedReleaseToConfiguredRemote(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "path": "/www/client/public", "kinsta": map[string]any{"environment_id": "kenv-live"}, "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
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
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "build/theme"}}},
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
	for _, want := range []string{"Theme deploy plan:", "remote:      production", "site:        client-kinsta", "env:         live", "provider:    kinsta", "source:      " + filepath.Join(repoRoot, "build", "theme"), "artifact:    " + filepath.Join(repoRoot, "dist", "client-v1.2.3.zip"), "release id:  v1.2.3-", "release dir: /www/client/public/wp-content/themes/.nf-releases/theme/v1.2.3-", "active dir:  /www/client/public/wp-content/themes/theme", "keep:        last 5 releases", "mode:        dry-run", "Would package " + filepath.Join(repoRoot, "build", "theme") + " -> " + filepath.Join(repoRoot, "dist", "client-v1.2.3.zip"), "> ssh -p 12345 client@203.0.113.10 'mkdir -p /www/client/public/wp-content/themes/.nf-releases/theme/_uploads'", "> rsync -az -e 'ssh -p 12345' " + filepath.Join(repoRoot, "dist", "client-v1.2.3.zip") + " client@203.0.113.10:/www/client/public/wp-content/themes/.nf-releases/theme/_uploads/client-v1.2.3.zip", "remote script: extract release, switch active theme, refresh runtime mtimes, activate, record metadata, prune old releases", "> ssh -p 12345 client@203.0.113.10 'sh -s -- nf-theme-deploy-release'", "post-deploy: clear Kinsta site cache", "No remote files were changed."} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("theme deploy stdout missing %q:\n%s", want, stdout)
		}
	}
	for _, unwanted := range []string{"unzip -q", "GLOB_ONLYDIR", "wp --path=/www/client/public theme activate"} {
		if strings.Contains(stdout, unwanted) {
			t.Fatalf("theme deploy stdout should not print remote script fragment %q:\n%s", unwanted, stdout)
		}
	}
	if strings.Contains(stdout, "restart Kinsta PHP") {
		t.Fatalf("theme deploy without --restart planned a PHP restart:\n%s", stdout)
	}
	stdout = captureStdout(t, func() {
		if got := Run([]string{"theme", "deploy", "production", "--dry-run", "--restart"}); got != 0 {
			t.Fatalf("Run(theme deploy --dry-run --restart) = %d, want 0", got)
		}
	})
	if !strings.Contains(stdout, "post-deploy: restart Kinsta PHP and clear site cache") {
		t.Fatalf("theme deploy --restart plan missing PHP restart:\n%s", stdout)
	}
}

func TestRunThemeRuntimeMaintenanceRestartsKinstaPHPBeforeClearingCache(t *testing.T) {
	events := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer kinsta-token"; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /sites/tools/restart-php":
			events = append(events, "restart")
			assertKinstaEnvironmentPayload(t, r, "kenv-live")
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-restart"})
		case "GET /operations/op-restart":
			events = append(events, "wait-restart")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Restarting PHP successfully finished."})
		case "POST /sites/tools/clear-cache":
			events = append(events, "cache")
			assertKinstaEnvironmentPayload(t, r, "kenv-live")
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-cache"})
		case "GET /operations/op-cache":
			events = append(events, "wait-cache")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Cache clearing successfully finished."})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KINSTA_API_KEY", "kinsta-token")
	t.Setenv("KINSTA_BASE_URL", server.URL)

	stdout := captureStdout(t, func() {
		if err := runThemeRuntimeMaintenance(themeDeployTarget{Provider: "kinsta", SiteID: "client-kinsta", Env: "live", KinstaEnvID: "kenv-live"}, false); err != nil {
			t.Fatalf("runThemeRuntimeMaintenance() error = %v", err)
		}
	})
	if got, want := strings.Join(events, ","), "cache,wait-cache"; got != want {
		t.Fatalf("runtime maintenance events = %q, want %q", got, want)
	}
	if strings.Contains(stdout, "Kinsta PHP restarted.") || !strings.Contains(stdout, "Kinsta site cache cleared.") {
		t.Fatalf("runtime maintenance without restart stdout = %q", stdout)
	}
	events = nil
	stdout = captureStdout(t, func() {
		if err := runThemeRuntimeMaintenance(themeDeployTarget{Provider: "kinsta", SiteID: "client-kinsta", Env: "live", KinstaEnvID: "kenv-live"}, true); err != nil {
			t.Fatalf("runThemeRuntimeMaintenance(restart) error = %v", err)
		}
	})
	if got, want := strings.Join(events, ","), "restart,wait-restart,cache,wait-cache"; got != want {
		t.Fatalf("runtime maintenance restart events = %q, want %q", got, want)
	}
	for _, want := range []string{"Kinsta PHP restarted.", "Kinsta site cache cleared."} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("runtime maintenance restart stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestValidateThemeRuntimeMaintenancePreflightsKinsta(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	t.Setenv("KINSTA_API_KEY", "")
	target := themeDeployTarget{Provider: "kinsta", SiteID: "client-kinsta", Env: "live", KinstaEnvID: "kenv-live"}
	if err := validateThemeRuntimeMaintenance(target, true); err != nil {
		t.Fatalf("validateThemeRuntimeMaintenance(dry-run) error = %v", err)
	}
	if err := validateThemeRuntimeMaintenance(target, false); err == nil || !strings.Contains(err.Error(), "KINSTA_API_KEY") {
		t.Fatalf("validateThemeRuntimeMaintenance() error = %v, want missing token guidance", err)
	}
	target.KinstaEnvID = ""
	if err := validateThemeRuntimeMaintenance(target, true); err == nil || !strings.Contains(err.Error(), "nf site refresh") {
		t.Fatalf("validateThemeRuntimeMaintenance(missing env id) error = %v, want refresh guidance", err)
	}
}

func assertKinstaEnvironmentPayload(t *testing.T, r *http.Request, environmentID string) {
	t.Helper()
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode(payload) error = %v", err)
	}
	if payload["environment_id"] != environmentID {
		t.Fatalf("environment payload = %#v, want %q", payload, environmentID)
	}
}

func TestAliasSpecsLoadShortFormTargets(t *testing.T) {
	metadata := &project.Manifest{WordPress: project.WordPress{Aliases: map[string]string{
		"content":           "wp-content",
		"files":             "wp-content/uploads/public/files",
		"annual-report.pdf": "wp-content/uploads/2026/annual-report.pdf",
	}}}
	specs, err := loadAliasSpecs(metadata)
	if err != nil {
		t.Fatalf("loadAliasSpecs() error = %v", err)
	}
	if got, want := len(specs), 3; got != want {
		t.Fatalf("len(specs) = %d, want %d", got, want)
	}
	if specs[0].Alias != "annual-report.pdf" || specs[0].Target != "wp-content/uploads/2026/annual-report.pdf" {
		t.Fatalf("specs[0] = %#v, want sorted annual report alias", specs[0])
	}
}

func TestRunAliasAddRemoveUpdatesProjectMetadata(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	projectPath := filepath.Join(repoRoot, "nf.json")
	if err := os.WriteFile(projectPath, []byte("{\n  \"version\": 2,\n  \"project\": {\n    \"slug\": \"client\",\n    \"password_version\": 0\n  },\n  \"wordpress\": {\n    \"themes\": [\"twentytwentyfive\"]\n  }\n}\n"), 0o644); err != nil {
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

	stdout := captureStdout(t, func() {
		if got := Run([]string{"alias", "add", "files", "wp-content/uploads/public/files"}); got != 0 {
			t.Fatalf("Run(alias add) = %d, want 0", got)
		}
	})
	if !strings.Contains(stdout, "Added alias /files -> wp-content/uploads/public/files to nf.json.") {
		t.Fatalf("alias add output = %q", stdout)
	}
	data, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("ReadFile(nf.json) error = %v", err)
	}
	if text := string(data); !strings.Contains(text, `"aliases": {`) || !strings.Contains(text, `"files": "wp-content/uploads/public/files"`) {
		t.Fatalf("nf.json missing alias:\n%s", text)
	}

	stdout = captureStdout(t, func() {
		if got := Run([]string{"alias", "remove", "files"}); got != 0 {
			t.Fatalf("Run(alias remove) = %d, want 0", got)
		}
	})
	if !strings.Contains(stdout, "Removed alias /files from nf.json.") {
		t.Fatalf("alias remove output = %q", stdout)
	}
	metadata, err := loadProjectMetadataOrError(repoRoot)
	if err != nil {
		t.Fatalf("loadProjectMetadataOrError() error = %v", err)
	}
	aliases := metadata.WordPress.Aliases
	if len(aliases) != 0 {
		t.Fatalf("aliases = %#v, want empty object", aliases)
	}
}

func TestAliasValidationRejectsUnsafeNamesAndTargets(t *testing.T) {
	for _, name := range []string{"", "/files", "foo/bar", "foo\\bar", ".", "..", "bad..name", "wp-admin", "wp-content", "index.php", "uploads", "bad name"} {
		if _, _, err := normalizeAliasName(name); err == nil {
			t.Fatalf("normalizeAliasName(%q) succeeded, want error", name)
		}
	}
	for _, name := range []string{"files", "esg-report-2025", "annual-report.pdf"} {
		if got, _, err := normalizeAliasName(name); err != nil || got != name {
			t.Fatalf("normalizeAliasName(%q) = %q, %v; want success", name, got, err)
		}
	}
	for _, target := range []string{"", "/wp-content/uploads/file.pdf", "../secret", "wp-content/../wp-config.php", "wp-config.php", "uploads/file.pdf", "wp-content\\uploads\\file.pdf"} {
		if _, err := normalizeAliasTarget(target); err == nil {
			t.Fatalf("normalizeAliasTarget(%q) succeeded, want error", target)
		}
	}
	for _, target := range []string{"wp-content", "wp-content/uploads/public/files", "wp-content/themes/client/assets/report.pdf"} {
		if got, err := normalizeAliasTarget(target); err != nil || got != target {
			t.Fatalf("normalizeAliasTarget(%q) = %q, %v; want success", target, got, err)
		}
	}
	if _, warning, err := normalizeAliasName("feed"); err != nil || !strings.Contains(warning, "WordPress feed routes") {
		t.Fatalf("normalizeAliasName(feed) warning = %q, err = %v", warning, err)
	}
}

func TestAliasStatusScriptReportsSymlinkStates(t *testing.T) {
	docroot := t.TempDir()
	for _, dir := range []string{"wp-content/uploads/public/files", "wp-content/uploads/public/report", "wp-content/uploads/public/good", "wp-content/uploads/public/old"} {
		if err := os.MkdirAll(filepath.Join(docroot, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	if err := os.Symlink("wp-content/uploads/public/files", filepath.Join(docroot, "files")); err != nil {
		t.Fatalf("Symlink(files) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(docroot, "file-conflict"), []byte("real"), 0o644); err != nil {
		t.Fatalf("WriteFile(file-conflict) error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(docroot, "dir-conflict"), 0o755); err != nil {
		t.Fatalf("Mkdir(dir-conflict) error = %v", err)
	}
	if err := os.Symlink("wp-content/uploads/public/old", filepath.Join(docroot, "wrong")); err != nil {
		t.Fatalf("Symlink(wrong) error = %v", err)
	}
	if err := os.Symlink("wp-content/uploads/public/old", filepath.Join(docroot, "stale")); err != nil {
		t.Fatalf("Symlink(stale) error = %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(docroot, "wp-content", "uploads", "escape")); err != nil {
		t.Fatalf("Symlink(escape) error = %v", err)
	}
	specs := []aliasSpec{
		{Alias: "dir-conflict", Target: "wp-content/uploads/public/good"},
		{Alias: "escaped", Target: "wp-content/uploads/escape"},
		{Alias: "file-conflict", Target: "wp-content/uploads/public/good"},
		{Alias: "files", Target: "wp-content/uploads/public/files"},
		{Alias: "missing", Target: "wp-content/uploads/public/missing"},
		{Alias: "report", Target: "wp-content/uploads/public/report"},
		{Alias: "wrong", Target: "wp-content/uploads/public/files"},
	}
	output, err := runAliasTestScript(aliasStatusScript(docroot, specs))
	if err != nil {
		t.Fatalf("aliasStatusScript() error = %v\n%s", err, output)
	}
	for _, want := range []string{
		"/files\twp-content/uploads/public/files\tOK",
		"/report\twp-content/uploads/public/report\tMissing symlink",
		"/missing\twp-content/uploads/public/missing\tMissing target",
		"/file-conflict\twp-content/uploads/public/good\tConflict: real file exists",
		"/dir-conflict\twp-content/uploads/public/good\tConflict: real directory exists",
		"/wrong\twp-content/uploads/public/old\tWrong symlink target",
		"/escaped\twp-content/uploads/escape\tTarget outside wp-content",
		"/stale\twp-content/uploads/public/old\tStale symlink",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("status output missing %q:\n%s", want, output)
		}
	}
}

func TestAliasSyncScriptCreatesUpdatesPrunesAndPreservesContent(t *testing.T) {
	docroot := t.TempDir()
	for _, dir := range []string{"wp-content/uploads/public/files", "wp-content/uploads/public/good", "wp-content/uploads/public/old"} {
		if err := os.MkdirAll(filepath.Join(docroot, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	if err := os.Symlink("wp-content/uploads/public/old", filepath.Join(docroot, "wrong")); err != nil {
		t.Fatalf("Symlink(wrong) error = %v", err)
	}
	if err := os.Symlink("wp-content/uploads/public/old", filepath.Join(docroot, "stale")); err != nil {
		t.Fatalf("Symlink(stale) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(docroot, "conflict"), []byte("real"), 0o644); err != nil {
		t.Fatalf("WriteFile(conflict) error = %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(docroot, "wp-content", "uploads", "escape")); err != nil {
		t.Fatalf("Symlink(escape) error = %v", err)
	}
	specs := []aliasSpec{
		{Alias: "conflict", Target: "wp-content/uploads/public/good"},
		{Alias: "escaped", Target: "wp-content/uploads/escape"},
		{Alias: "files", Target: "wp-content/uploads/public/files"},
		{Alias: "missing", Target: "wp-content/uploads/public/missing"},
		{Alias: "wrong", Target: "wp-content/uploads/public/files"},
	}
	output, err := runAliasTestScript(aliasSyncScript(docroot, specs))
	if err == nil {
		t.Fatalf("aliasSyncScript() succeeded, want non-zero for conflicts\n%s", output)
	}
	for _, want := range []string{
		"Created /files -> wp-content/uploads/public/files",
		"Updated /wrong -> wp-content/uploads/public/files",
		"Pruned /stale",
		"Skipped /missing: Missing target: wp-content/uploads/public/missing",
		"Skipped /escaped: Target outside wp-content: wp-content/uploads/escape",
		"Conflict /conflict: real file exists at web root",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("sync output missing %q:\n%s", want, output)
		}
	}
	assertSymlinkTarget(t, filepath.Join(docroot, "files"), "wp-content/uploads/public/files")
	assertSymlinkTarget(t, filepath.Join(docroot, "wrong"), "wp-content/uploads/public/files")
	if _, err := os.Lstat(filepath.Join(docroot, "stale")); !os.IsNotExist(err) {
		t.Fatalf("stale symlink still exists, err = %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(docroot, "conflict")); err != nil || string(data) != "real" {
		t.Fatalf("conflict file = %q, %v; want unchanged", data, err)
	}
	if _, err := os.Lstat(filepath.Join(docroot, "missing")); !os.IsNotExist(err) {
		t.Fatalf("missing target alias was created, err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(docroot, "escaped")); !os.IsNotExist(err) {
		t.Fatalf("escaped target alias was created, err = %v", err)
	}
}

func TestRunAliasRemoteStatusAndSyncUseFinalRemoteArg(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "aliases": map[string]any{"files": "wp-content/uploads/public/files"}}, "remotes": map[string]any{"production": "client-kinsta:live"}}
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

	var scripts []string
	oldRunSSHOutput := runSSHOutputFn
	runSSHOutputFn = func(args []string) ([]byte, error) {
		if len(args) != 5 || args[0] != "ssh" || args[2] != "12345" || args[3] != "client@203.0.113.10" {
			t.Fatalf("runSSHOutputFn args = %#v, want kinsta ssh args", args)
		}
		scripts = append(scripts, args[4])
		if strings.Contains(args[4], "\nsync_alias ") {
			return []byte("OK /files -> wp-content/uploads/public/files\n"), nil
		}
		return []byte("/files\twp-content/uploads/public/files\tOK\n"), nil
	}
	t.Cleanup(func() { runSSHOutputFn = oldRunSSHOutput })

	statusOutput := captureStdout(t, func() {
		if got := Run([]string{"alias", "status", "production"}); got != 0 {
			t.Fatalf("Run(alias status production) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Alias status:", "remote:   production", "site:     client-kinsta", "env:      live", "/files", "OK"} {
		if !strings.Contains(statusOutput, want) {
			t.Fatalf("alias status output missing %q:\n%s", want, statusOutput)
		}
	}
	syncOutput := captureStdout(t, func() {
		if got := Run([]string{"alias", "sync", "production"}); got != 0 {
			t.Fatalf("Run(alias sync production) = %d, want 0", got)
		}
	})
	if !strings.Contains(syncOutput, "OK /files -> wp-content/uploads/public/files") {
		t.Fatalf("alias sync output = %q", syncOutput)
	}
	if len(scripts) != 2 {
		t.Fatalf("ran %d remote scripts, want 2", len(scripts))
	}
	for _, script := range scripts {
		for _, want := range []string{"docroot=/www/client/public", "files", "wp-content/uploads/public/files"} {
			if !strings.Contains(script, want) {
				t.Fatalf("remote script missing %q:\n%s", want, script)
			}
		}
	}
}

func runAliasTestScript(script string) (string, error) {
	cmd := exec.Command("sh", "-lc", script)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func assertSymlinkTarget(t *testing.T, linkPath, want string) {
	t.Helper()
	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("Readlink(%s) error = %v", linkPath, err)
	}
	if got != want {
		t.Fatalf("Readlink(%s) = %q, want %q", linkPath, got, want)
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
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{map[string]any{"slug": "client-theme", "source": "repo", "path": "theme"}}}, "remotes": map[string]any{"staging": "client.app1-linode:staging"}}
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
	for _, want := range []string{"provider:    linode", "artifact:    " + filepath.Join(repoRoot, "dist", "client-v2.0.0.zip"), "release dir: /var/www/sites/client_staging/public/wp-content/themes/.nf-releases/client-theme/v2.0.0-", "active dir:  /var/www/sites/client_staging/public/wp-content/themes/client-theme", "remote script: extract release, switch active theme, refresh runtime mtimes, activate, record metadata, prune old releases", "> ssh -p 22 nonfiction@app1-linode.nonfiction.dev 'sh -s -- nf-theme-deploy-release'", "post-deploy: regenerate WordPress rewrite rules", "> ssh -p 22 nonfiction@app1-linode.nonfiction.dev 'sudo -u www-data wp --path=/var/www/sites/client_staging/public rewrite flush'"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("theme deploy linode stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestRunThemeDeployExecuteRefreshesRuntimeMtimesBeforeActivation(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(repoRoot, "theme", "style.css"), []byte("/*\nTheme Name: Theme\nVersion: 2.0.0\n*/\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(style.css) error = %v", err)
	}
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{map[string]any{"slug": "client-theme", "source": "repo", "path": "theme"}}}, "remotes": map[string]any{"production": "client.app1-linode:live"}}
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
	var events []string
	oldRunSSH := runSSHCommandFn
	var maintenanceCommands [][]string
	runSSHCommandFn = func(args []string) error {
		maintenanceCommands = append(maintenanceCommands, append([]string(nil), args...))
		if strings.Contains(args[len(args)-1], "rewrite flush") {
			events = append(events, "rewrite")
		}
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })
	oldRunRsync := runRsyncCommandFn
	runRsyncCommandFn = func(args []string) error { return nil }
	t.Cleanup(func() { runRsyncCommandFn = oldRunRsync })
	oldRunSSHStdin := runSSHStdinCommandFn
	var sshCommands [][]string
	var sshScripts []string
	runSSHStdinCommandFn = func(args []string, script string) error {
		sshCommands = append(sshCommands, append([]string(nil), args...))
		sshScripts = append(sshScripts, script)
		events = append(events, "release")
		return nil
	}
	t.Cleanup(func() { runSSHStdinCommandFn = oldRunSSHStdin })

	stdout := captureStdout(t, func() {
		if got := Run([]string{"theme", "deploy", "production"}); got != 0 {
			t.Fatalf("Run(theme deploy) = %d, want 0", got)
		}
	})
	if !strings.Contains(stdout, "Theme release deployed.") {
		t.Fatalf("theme deploy stdout = %q", stdout)
	}
	if len(sshCommands) != 1 {
		t.Fatalf("ssh stdin commands len = %d, want 1: %#v", len(sshCommands), sshCommands)
	}
	if sshCommands[0][len(sshCommands[0])-1] != "sh -s -- nf-theme-deploy-release" {
		t.Fatalf("deploy ssh command = %#v", sshCommands[0])
	}
	script := sshScripts[0]
	mtimeRefresh := `find "$active_dir" -type f \( -name '*.php' -o -name '*.twig' -o -name '*.json' -o -name '*.css' -o -name '*.js' -o -name '*.mjs' -o -name '*.map' \) -exec touch {} +`
	for _, want := range []string{mtimeRefresh, `php -r 'if (function_exists("opcache_reset")) { @opcache_reset(); }'`, "sudo -u www-data wp --path=/var/www/sites/client/public theme activate client-theme --allow-root", "sudo -n systemctl reload php8.3-fpm"} {
		if !strings.Contains(script, want) {
			t.Fatalf("deploy ssh script missing %q:\n%s", want, script)
		}
	}
	switchIdx := strings.Index(script, `if mv "$active_tmp" "$active_dir"; then`)
	touchIdx := strings.Index(script, mtimeRefresh)
	activateIdx := strings.Index(script, "sudo -u www-data wp --path=/var/www/sites/client/public theme activate client-theme --allow-root")
	if switchIdx == -1 || touchIdx == -1 || activateIdx == -1 || !(switchIdx < touchIdx && touchIdx < activateIdx) {
		t.Fatalf("deploy script order switch=%d touch=%d activate=%d:\n%s", switchIdx, touchIdx, activateIdx, script)
	}
	if got := strings.Join(events, ","); got != "release,rewrite" {
		t.Fatalf("deploy maintenance events = %q, want release,rewrite", got)
	}
	if len(maintenanceCommands) != 2 || !strings.Contains(maintenanceCommands[1][len(maintenanceCommands[1])-1], "sudo -u www-data wp --path=/var/www/sites/client/public rewrite flush") {
		t.Fatalf("deploy rewrite command = %#v", maintenanceCommands)
	}
	if !strings.Contains(stdout, "post-deploy: regenerate WordPress rewrite rules") || !strings.Contains(stdout, rewriteRulesRegeneratedMessage) {
		t.Fatalf("theme deploy stdout missing rewrite maintenance output:\n%s", stdout)
	}
	releaseCount := len(sshScripts)
	runSSHCommandFn = func(args []string) error {
		if strings.Contains(args[len(args)-1], "rewrite flush") {
			return fmt.Errorf("rewrite command failed")
		}
		return nil
	}
	var failureStdout string
	failureStderr := captureStderr(t, func() {
		failureStdout = captureStdout(t, func() {
			if got := Run([]string{"theme", "deploy", "production"}); got != 1 {
				t.Fatalf("Run(theme deploy) with rewrite failure = %d, want 1", got)
			}
		})
	})
	if len(sshScripts) != releaseCount+1 {
		t.Fatalf("deploy release script count after rewrite failure = %d, want %d", len(sshScripts), releaseCount+1)
	}
	if !strings.Contains(failureStderr, "failed to flush WordPress rewrite rules on live: rewrite command failed") {
		t.Fatalf("theme deploy rewrite failure stderr = %q", failureStderr)
	}
	if strings.Contains(failureStdout, "Theme release deployed.") || strings.Contains(failureStdout, rewriteRulesRegeneratedMessage) {
		t.Fatalf("theme deploy printed success after rewrite failure:\n%s", failureStdout)
	}
}

func TestRunThemeDeployWithoutRemotePromptsPicker(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("KINSTA_API_KEY", "kinsta-token")
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "path": "/www/client/public", "kinsta": map[string]any{"environment_id": "kenv-live"}, "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
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
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}},
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
	oldRuntimeMaintenance := runThemeRuntimeMaintenanceFn
	maintenanceCalls := 0
	restartRequests := []bool{}
	runThemeRuntimeMaintenanceFn = func(target themeDeployTarget, restartPHP bool) error {
		maintenanceCalls++
		restartRequests = append(restartRequests, restartPHP)
		if target.Provider != "kinsta" || target.KinstaEnvID != "kenv-live" {
			t.Fatalf("runtime maintenance target = %#v", target)
		}
		return nil
	}
	t.Cleanup(func() { runThemeRuntimeMaintenanceFn = oldRuntimeMaintenance })

	stdout := captureStdout(t, func() {
		if got := Run([]string{"theme", "deploy"}); got != 0 {
			t.Fatalf("Run(theme deploy) = %d, want 0", got)
		}
	})
	if selectTitle != "Choose a remote to deploy theme to" {
		t.Fatalf("select title = %q", selectTitle)
	}
	if len(selectOptions) != 1 || selectOptions[0] != (ui.SelectOption{Value: "production", Label: "production (client-kinsta:live)"}) {
		t.Fatalf("select options = %#v", selectOptions)
	}
	if !strings.Contains(stdout, "remote:      production") || !strings.Contains(stdout, "Theme release deployed.") {
		t.Fatalf("theme deploy picker stdout = %q", stdout)
	}
	if maintenanceCalls != 1 {
		t.Fatalf("theme deploy runtime maintenance calls = %d, want 1", maintenanceCalls)
	}
	if len(restartRequests) != 1 || restartRequests[0] {
		t.Fatalf("theme deploy restart requests = %#v, want false", restartRequests)
	}
	stdout = captureStdout(t, func() {
		if got := Run([]string{"theme", "deploy", "production", "--restart"}); got != 0 {
			t.Fatalf("Run(theme deploy --restart) = %d, want 0", got)
		}
	})
	if maintenanceCalls != 2 || len(restartRequests) != 2 || !restartRequests[1] {
		t.Fatalf("theme deploy restart requests = %#v, maintenance calls = %d", restartRequests, maintenanceCalls)
	}
}

func TestRunThemeRollbackPlansAndExecutesKinstaMaintenance(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "path": "/www/client/public", "kinsta": map[string]any{"environment_id": "kenv-live"}, "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	repoRoot := t.TempDir()
	for _, dir := range []string{".git", "theme"} {
		if err := os.MkdirAll(filepath.Join(repoRoot, dir), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}}, "remotes": map[string]any{"production": "client-kinsta:live"}}
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
	for _, want := range []string{"Theme rollback plan:", "remote:      production", "provider:    kinsta", "releases:    /www/client/public/wp-content/themes/.nf-releases/theme/releases.json", "release dir: /www/client/public/wp-content/themes/.nf-releases/theme/<previous-release>", "active dir:  /www/client/public/wp-content/themes/theme", "mode:        dry-run", "remote script: select previous release, switch active theme, refresh runtime mtimes, activate, record rollback", "> ssh -p 12345 client@203.0.113.10 'sh -s -- nf-theme-rollback-release'", "post-rollback: regenerate WordPress rewrite rules", "> ssh -p 12345 client@203.0.113.10 'wp --path=/www/client/public rewrite flush'", "post-rollback: restart Kinsta PHP and clear site cache", "No remote files were changed."} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("theme rollback stdout missing %q:\n%s", want, stdout)
		}
	}
	for _, unwanted := range []string{"target_release=$(php -r", "wp --path=/www/client/public theme activate", "--hard"} {
		if strings.Contains(stdout, unwanted) {
			t.Fatalf("theme rollback stdout should not print remote script fragment %q:\n%s", unwanted, stdout)
		}
	}

	t.Setenv("KINSTA_API_KEY", "kinsta-token")
	runSSHCommandFn = func(args []string) error { return nil }
	oldRunSSHStdin := runSSHStdinCommandFn
	runSSHStdinCommandFn = func(args []string, script string) error { return nil }
	t.Cleanup(func() { runSSHStdinCommandFn = oldRunSSHStdin })
	oldRuntimeMaintenance := runThemeRuntimeMaintenanceFn
	maintenanceCalls := 0
	runThemeRuntimeMaintenanceFn = func(target themeDeployTarget, restartPHP bool) error {
		maintenanceCalls++
		if !restartPHP {
			t.Fatal("theme rollback did not request PHP restart")
		}
		if target.Provider != "kinsta" || target.KinstaEnvID != "kenv-live" {
			t.Fatalf("runtime maintenance target = %#v", target)
		}
		return nil
	}
	t.Cleanup(func() { runThemeRuntimeMaintenanceFn = oldRuntimeMaintenance })
	stdout = captureStdout(t, func() {
		if got := Run([]string{"theme", "rollback", "production"}); got != 0 {
			t.Fatalf("Run(theme rollback) = %d, want 0", got)
		}
	})
	if maintenanceCalls != 1 || !strings.Contains(stdout, "Theme release rolled back.") {
		t.Fatalf("theme rollback maintenance calls = %d, stdout = %q", maintenanceCalls, stdout)
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
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{map[string]any{"slug": "client-theme", "source": "repo", "path": "theme"}}}, "remotes": map[string]any{"production": "client.app1-linode:live"}}
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
	var events []string
	runSSHStdinCommandFn = func(args []string, script string) error {
		sshCommands = append(sshCommands, append([]string(nil), args...))
		sshScripts = append(sshScripts, script)
		events = append(events, "rollback")
		return nil
	}
	t.Cleanup(func() { runSSHStdinCommandFn = oldRunSSHStdin })
	oldRunSSH := runSSHCommandFn
	var maintenanceCommands [][]string
	runSSHCommandFn = func(args []string) error {
		maintenanceCommands = append(maintenanceCommands, append([]string(nil), args...))
		events = append(events, "rewrite")
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })

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
	mtimeRefresh := `find "$active_dir" -type f \( -name '*.php' -o -name '*.twig' -o -name '*.json' -o -name '*.css' -o -name '*.js' -o -name '*.mjs' -o -name '*.map' \) -exec touch {} +`
	for _, want := range []string{"release_base=/var/www/sites/client/public/wp-content/themes/.nf-releases/client-theme", "metadata_file=/var/www/sites/client/public/wp-content/themes/.nf-releases/client-theme/releases.json", "target_release=$(php -r", "cp -a \"$release_dir\" \"$active_tmp\"", mtimeRefresh, `php -r 'if (function_exists("opcache_reset")) { @opcache_reset(); }'`, "sudo -u www-data wp --path=/var/www/sites/client/public theme activate client-theme --allow-root", "sudo -n systemctl reload php8.3-fpm", `"action"=>"rollback"`} {
		if !strings.Contains(script, want) {
			t.Fatalf("rollback ssh script missing %q:\n%s", want, script)
		}
	}
	switchIdx := strings.Index(script, `if mv "$active_tmp" "$active_dir"; then`)
	touchIdx := strings.Index(script, mtimeRefresh)
	activateIdx := strings.Index(script, "sudo -u www-data wp --path=/var/www/sites/client/public theme activate client-theme --allow-root")
	if switchIdx == -1 || touchIdx == -1 || activateIdx == -1 || !(switchIdx < touchIdx && touchIdx < activateIdx) {
		t.Fatalf("rollback script order switch=%d touch=%d activate=%d:\n%s", switchIdx, touchIdx, activateIdx, script)
	}
	if got := strings.Join(events, ","); got != "rollback,rewrite" {
		t.Fatalf("rollback maintenance events = %q, want rollback,rewrite", got)
	}
	if len(maintenanceCommands) != 1 || !strings.Contains(maintenanceCommands[0][len(maintenanceCommands[0])-1], "sudo -u www-data wp --path=/var/www/sites/client/public rewrite flush") {
		t.Fatalf("rollback rewrite command = %#v", maintenanceCommands)
	}
	if !strings.Contains(stdout, "post-rollback: regenerate WordPress rewrite rules") || !strings.Contains(stdout, rewriteRulesRegeneratedMessage) {
		t.Fatalf("theme rollback stdout missing rewrite maintenance output:\n%s", stdout)
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

func TestRunSiteExportCreatesFullHandoffExport(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_DATA_HOME", t.TempDir())
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	outputDir := filepath.Join(t.TempDir(), "client-export")
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
		if strings.Contains(args[len(args)-2], tablePrefixFilename) {
			return os.WriteFile(filepath.Join(outputDir, tablePrefixFilename), []byte("wpmc_\n"), 0o644)
		}
		return nil
	}
	t.Cleanup(func() { runRsyncCommandFn = oldRunRsync })

	stdout := captureStdout(t, func() {
		if got := Run([]string{"site", "export", "client-kinsta:live", "--output", outputDir}); got != 0 {
			t.Fatalf("Run(site export) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Site export plan:", "env:           client-kinsta:live", "provider:      kinsta", "environment ssh: client@203.0.113.10", "includes:      full WordPress filesystem, database.sql.gz", "Site export created.", "files: files/", "database: database.sql.gz"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("site export stdout missing %q:\n%s", want, stdout)
		}
	}
	if len(sshCommands) != 2 {
		t.Fatalf("ssh commands len = %d, want export and cleanup: %#v", len(sshCommands), sshCommands)
	}
	if !strings.Contains(sshCommands[0][len(sshCommands[0])-1], "wp --path=/www/client/public db export") {
		t.Fatalf("export ssh command = %#v", sshCommands[0])
	}
	if len(rsyncCommands) != 3 {
		t.Fatalf("rsync commands len = %d, want database, prefix, and files downloads: %#v", len(rsyncCommands), rsyncCommands)
	}
	if got, want := rsyncCommands[0][3], "ssh -p 12345"; got != want {
		t.Fatalf("database rsync ssh option = %q, want %q", got, want)
	}
	if got, want := rsyncCommands[2][len(rsyncCommands[2])-1], siteExportFilesDir(outputDir)+string(filepath.Separator); got != want {
		t.Fatalf("files rsync output = %q, want %q", got, want)
	}
	data, err := os.ReadFile(siteExportManifestPath(outputDir))
	if err != nil {
		t.Fatalf("ReadFile(manifest.json) error = %v", err)
	}
	for _, want := range []string{`"source": "remote-site-export"`, `"env_id": "client-kinsta:live"`, `"files": "files"`, `"database": "database.sql.gz"`, `"table_prefix": "wpmc_"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("export manifest missing %q:\n%s", want, data)
		}
	}
	if _, err := os.Stat(filepath.Join(outputDir, "README.txt")); err != nil {
		t.Fatalf("README.txt missing: %v", err)
	}
}

func TestRunSiteExportDryRunDoesNotCallRemoteCommands(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	oldRunSSH := runSSHCommandFn
	runSSHCommandFn = func(args []string) error { t.Fatalf("runSSHCommandFn called during dry-run: %#v", args); return nil }
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })
	oldRunRsync := runRsyncCommandFn
	runRsyncCommandFn = func(args []string) error { t.Fatalf("runRsyncCommandFn called during dry-run: %#v", args); return nil }
	t.Cleanup(func() { runRsyncCommandFn = oldRunRsync })

	stdout := captureStdout(t, func() {
		if got := Run([]string{"site", "export", "client-kinsta:live", "--dry-run"}); got != 0 {
			t.Fatalf("Run(site export --dry-run) = %d, want 0", got)
		}
	})
	if !strings.Contains(stdout, "mode:          dry-run") || !strings.Contains(stdout, "No data was changed") {
		t.Fatalf("site export dry-run output = %q", stdout)
	}
}

func TestRemoteSiteExportScriptAllowsLinodeWPUserToWriteDatabase(t *testing.T) {
	target := envRemoteSyncTarget{WordPressPath: "/var/www/sites/client/public", WPCommand: "sudo -u www-data wp", SudoFileOps: true}
	script := remoteSiteExportScript(target, "/tmp/nf-export-client")
	for _, want := range []string{
		"chmod 777 /tmp/nf-export-client",
		"sudo -u www-data wp --path=/var/www/sites/client/public db export /tmp/nf-export-client/database.sql",
		"sudo gzip -f /tmp/nf-export-client/database.sql",
		"sudo chmod 644 /tmp/nf-export-client/database.sql.gz",
		"sudo tar -C /var/www/sites/client/public -czf /tmp/nf-export-client/files.tar.gz .",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("Linode site export script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "chmod 755 /tmp/nf-export-client") {
		t.Fatalf("Linode site export script should not make temp dir read-only to www-data:\n%s", script)
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
	meta := remoteSnapshotMetadata{Schema: 1, Source: "remote", EnvID: envID, SiteID: siteID, Env: env, Provider: "kinsta", URL: "https://" + strings.ReplaceAll(envID, ":", ".") + ".example.test", CreatedAt: createdAt, Path: dir, Contents: envSnapshotContents{Database: "database.sql.gz", WpContent: "wp-content.tar.gz", WpContentPaths: envSnapshotContentPaths()}}
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
	servers := map[string]any{"servers": map[string]any{"app1-linode": map[string]any{"name": "app1-linode", "provider": "linode", "provider_id": "98589908", "hostname": "app1.nonfiction.dev", "ssh_user": "nonfiction"}}}
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
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}}, "remotes": map[string]any{}}
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
	assertContainsInOrder(t, showOutput, []string{
		"production\n",
		"──────────\n",
		"Env         client-app1-linode:live\n",
		"Provider    linode\n",
		"Target      app1-linode\n",
		"Target ID   98589908\n",
		"Access\n",
		"  URL   https://client.app1.nonfiction.dev/\n",
		"  SSH   ssh nonfiction@app1.nonfiction.dev",
	})
	for _, notWant := range []string{"Remote:", "Site:", "Target record:"} {
		if strings.Contains(showOutput, notWant) {
			t.Fatalf("remote show output contains %q:\n%s", notWant, showOutput)
		}
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
	if !strings.Contains(showOnlyOutput, "Env         client-app1-linode:live") {
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
	if len(removeSelectOptions) != 1 || removeSelectOptions[0] != (ui.SelectOption{Value: "production", Label: "production (client-app1-linode:live)"}) {
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
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}}, "remotes": map[string]any{}}
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
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}}, "remotes": map[string]any{}}
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
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}}, "remotes": map[string]any{}}
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
	assertContainsInOrder(t, output, []string{"up", "down", "show", "password [remote]", "logs [remote]", "shell, sh [remote]", "wp -- <args>", "snapshot", "\n\n  pull [remote]", "push [remote]", "import <source>", "\n\n  reset", "\nUp/Reset Options:\n", "--rebuild", "\nPassword Options:\n", "--wp", "\nImport Options:\n", "--source-url <url>", "\nSync Options:\n", "--dry-run", "--execute", "--yes"})
	for _, wanted := range []string{"env\n\nCommands:\n", "show", "show paths, ports, and URLs", "password [remote]", "print only a local or remote env password", "up", "start the local env", "down", "stop the local env", "logs [remote]", "tail local or remote WordPress logs", "shell", "open a local or remote shell", "reset", "destroy and recreate the local env", "wp -- <args>", "run WP-CLI in the local env", "push [remote]", "pull [remote]", "snapshot", "manage env snapshots", "Up/Reset Options:", "Password Options:", "Import Options:", "Sync Options:"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("runEnvHelp() output missing %q:\n%s", wanted, output)
		}
	}
	for _, unwanted := range []string{"Shortcuts:", "nf env snapshots", "snapshot create", "snapshot restore", "wp-config", "plugin", "instance"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("runEnvHelp() output unexpectedly contained %q:\n%s", unwanted, output)
		}
	}
}

func TestRunDefineHelpShowsCommands(t *testing.T) {
	output := captureStdout(t, func() { _ = runDefineHelp() })
	assertContainsInOrder(t, output, []string{"define", "list, ls", "get [name]", "status [remote]", "sync [remote]", "set", "set <name> <value>", "set <name> --secret", "set <name> --secret-stdin", "remove, rm", "remove, rm <name>", "migrate-env", "rekey", "\nOptions:\n", "--for <selector>", "--dry-run", "--delete-source", "--add-recipient <age1...>"})
	if strings.Contains(output, "--env") || !strings.Contains(output, "encrypted define value") {
		t.Fatalf("define help did not describe encrypted authoring:\n%s", output)
	}
	if strings.Contains(output, "\n  add ") || strings.Contains(output, "\n  add\n") {
		t.Fatalf("define help retained removed add command:\n%s", output)
	}
}

func TestRunEnvImportWithoutArgsShowsHelp(t *testing.T) {
	output := captureStdout(t, func() {
		if got := Run([]string{"env", "import"}); got != 0 {
			t.Fatalf("Run(env import) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, output, []string{"env import", "\nUsage:\n", "nf env import <source>", "\nSource layout:\n", "source/", "database.sql.gz", "wp-content/", "uploads/", "plugins/", "mu-plugins/", "languages/", "\nUploads-only layout:\n", "source/", "database.sql.gz", "uploads/", "\nnf site export layout:\n", "manifest.json", "files/", "Imported paths", "database, wp-content/uploads, plugins, languages", "skips target-specific wp-content/mu-plugins", "\nOptions:\n", "--db <path>", "--source-url <url>", "--dry-run", "--yes"})
	if strings.Contains(output, "requires exactly one source path") {
		t.Fatalf("env import help showed old error:\n%s", output)
	}
}

func TestCreateWpContentImportArchiveSkipsMuPlugins(t *testing.T) {
	source := filepath.Join(t.TempDir(), "files")
	for _, dir := range []string{
		filepath.Join(source, "wp-content", "uploads"),
		filepath.Join(source, "wp-content", "plugins", "classic-editor"),
		filepath.Join(source, "wp-content", "mu-plugins"),
		filepath.Join(source, "wp-content", "languages"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	files := map[string]string{
		filepath.Join(source, "wp-content", "uploads", "image.jpg"):                    "image",
		filepath.Join(source, "wp-content", "plugins", "classic-editor", "plugin.php"): "<?php\n",
		filepath.Join(source, "wp-content", "mu-plugins", "kinsta-mu-plugins.php"):     "<?php\n",
		filepath.Join(source, "wp-content", "mu-plugins", "nf-mailpit.php"):            "<?php\n",
		filepath.Join(source, "wp-content", "languages", "admin-en_CA.mo"):             "language",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}
	archivePath := filepath.Join(t.TempDir(), "wp-content.tar.gz")
	if err := createWpContentImportArchive(source, archivePath); err != nil {
		t.Fatalf("createWpContentImportArchive() error = %v", err)
	}
	extractDir := t.TempDir()
	if err := extractTarGzArchive(archivePath, extractDir); err != nil {
		t.Fatalf("extractTarGzArchive() error = %v", err)
	}
	for _, want := range []string{
		filepath.Join(extractDir, "wp-content", "uploads", "image.jpg"),
		filepath.Join(extractDir, "wp-content", "plugins", "classic-editor", "plugin.php"),
		filepath.Join(extractDir, "wp-content", "languages", "admin-en_CA.mo"),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Fatalf("expected archive member %s: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(extractDir, "wp-content", "mu-plugins")); !os.IsNotExist(err) {
		t.Fatalf("mu-plugins should not be included in env import archive: %v", err)
	}
}

func TestRunEnvPluginIsRemoved(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), []byte("{\n  \"version\": 2,\n  \"project\": {\"slug\": \"client\", \"password_version\": 0},\n  \"wordpress\": {\"themes\": [\"twentytwentyfive\"]}\n}\n"), 0o644); err != nil {
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
		if got := Run([]string{"env", "plugin", "list"}); got != 1 {
			t.Fatalf("Run(env plugin list) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "unsupported env command") {
		t.Fatalf("stderr = %q, want unsupported env command", stderr)
	}
}

func TestRunEnvSnapshotHelpShowsDedicatedCommands(t *testing.T) {
	output := captureStdout(t, func() { _ = runEnvSnapshot([]string{"help"}) })
	assertContainsInOrder(t, output, []string{"list, ls", "add [name]", "import [remote] [--name name]", "\n\n  use [name] [--remote remote] [--name name] [--yes]", "\n\n  remove, rm [name]", "prune [--keep N] [--dry-run] [--yes]"})
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
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}},
	}
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
	stubLocalWordPressTransferEstimate(t, 1024)

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
	for _, want := range []string{"Snapshot created.", "project: client", "name: demo-snapshot", "> docker compose exec --user nonfiction wordpress wp core is-installed", "> docker compose exec --user nonfiction wordpress wp theme is-active theme"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "> docker compose up -d") {
		t.Fatalf("Run() output unexpectedly included compose up:\n%s", output)
	}
	if strings.Contains(output, "> docker compose exec --user nonfiction wordpress sh -lc") {
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
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}}}
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

func TestFormatEnvSnapshotSizeUsesCorrectUnits(t *testing.T) {
	tests := []struct {
		size int64
		want string
	}{
		{size: -1, want: "-"},
		{size: 2, want: "2 B"},
		{size: 65 * 1024, want: "65 KiB"},
		{size: 2 * 1024 * 1024, want: "2.0 MiB"},
		{size: 19 * 1024 * 1024 / 10, want: "1.9 MiB"},
	}
	for _, tt := range tests {
		if got := formatEnvSnapshotSize(tt.size); got != tt.want {
			t.Fatalf("formatEnvSnapshotSize(%d) = %q, want %q", tt.size, got, tt.want)
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
	for _, want := range []string{`"name": "imported-live"`, `"project_slug": "client"`, `"wordpress_url": "https://client-kinsta.live.example.test"`, `"database": "database.sql.gz"`, `"wp_content": "wp-content.tar.gz"`} {
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
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}}}
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
		WordpressURL:   "https://source.example.test",
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
	stubLocalWordPressTransferEstimate(t, 1024)
	stubLocalSnapshotExpandedSize(t, 1024)
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
	for _, want := range []string{"Snapshot restored.", "name: restore-source", "Safety snapshot:", "> docker compose exec --user nonfiction wordpress wp core is-installed", "> docker compose exec --user nonfiction wordpress wp theme is-active theme"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "> docker compose up -d") {
		t.Fatalf("Run() output unexpectedly included compose up:\n%s", output)
	}
	if strings.Contains(output, "> docker compose exec --user nonfiction wordpress sh -lc") {
		t.Fatalf("Run() output unexpectedly exposed snapshot shell script preview:\n%s", output)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(docker log) error = %v", err)
	}
	logText := string(logData)
	assertContainsInOrder(t, logText, []string{"wp db export", "wp db import", "--user\nroot", "chown -R nonfiction:www-data", "chmod -R u+rwX,g+rwX,o-rwx", "wp\n--skip-themes\n--skip-plugins\noption\nupdate\nhome\nhttp://localhost:18440", "wp\n--skip-themes\n--skip-plugins\noption\nupdate\nsiteurl\nhttp://localhost:18440", "wp\nsearch-replace\nhttps://source.example.test\nhttp://localhost:18440\n--all-tables-with-prefix\n--skip-columns=guid", "wp\ntheme\nactivate\ntheme", "wp\nrewrite\nflush", "wp\ncache\nflush"})
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
	stubLocalWordPressTransferEstimate(t, 1024)
	stubLocalSnapshotExpandedSize(t, 1024)
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
	assertContainsInOrder(t, logText, []string{"wp db export", "wp db import", "--user\nroot", "chown -R nonfiction:www-data", "wp\n--skip-themes\n--skip-plugins\noption\nupdate\nhome\nhttp://localhost:18440", "wp\n--skip-themes\n--skip-plugins\noption\nupdate\nsiteurl\nhttp://localhost:18440", "wp\nsearch-replace\nhttp://localhost:18432\nhttp://localhost:18440\n--all-tables-with-prefix\n--skip-columns=guid", "wp\ntheme\nactivate\ntheme", "wp\nrewrite\nflush", "wp\ncache\nflush"})
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
	stubLocalWordPressTransferEstimate(t, 1024)
	stubLocalSnapshotExpandedSize(t, 1024)
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
	assertContainsInOrder(t, logText, []string{"wp db export", "wp db import", "--user\nroot", "chown -R nonfiction:www-data", "wp\n--skip-themes\n--skip-plugins\noption\nupdate\nhome\nhttp://localhost:18440", "wp\n--skip-themes\n--skip-plugins\noption\nupdate\nsiteurl\nhttp://localhost:18440", "wp\nsearch-replace\nhttps://client-kinsta.live.example.test\nhttp://localhost:18440\n--all-tables-with-prefix\n--skip-columns=guid", "wp\ntheme\nactivate\ntheme", "wp\nrewrite\nflush", "wp\ncache\nflush"})
}

func TestRunEnvImportRestoresSiteExportIntoLocalEnv(t *testing.T) {
	repoRoot, cfg := writeTestEnvProject(t)
	exportDir := filepath.Join(t.TempDir(), "client-export")
	for _, dir := range []string{
		filepath.Join(exportDir, "files", "wp-content", "uploads"),
		filepath.Join(exportDir, "files", "wp-content", "plugins", "demo"),
		filepath.Join(exportDir, "files", "wp-content", "themes", "remote-theme"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(exportDir, "files", "wp-content", "uploads", "image.jpg"), []byte("image"), 0o644); err != nil {
		t.Fatalf("WriteFile(upload) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(exportDir, "files", "wp-content", "plugins", "demo", "demo.php"), []byte("<?php\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(plugin) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(exportDir, "files", "wp-content", "themes", "remote-theme", "style.css"), []byte("/* theme */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(theme) error = %v", err)
	}
	if err := os.WriteFile(siteExportDatabasePath(exportDir), []byte("db"), 0o644); err != nil {
		t.Fatalf("WriteFile(database.sql.gz) error = %v", err)
	}
	manifest := siteExportManifest{Schema: siteExportSchema, Source: "remote-site-export", EnvID: "client-kinsta:live", SiteID: "client-kinsta", Env: "live", Provider: "kinsta", URL: "https://www.example.com/", CreatedAt: "2026-06-12T12:00:00Z", Files: "files", Database: "database.sql.gz"}
	if err := writeSiteExportManifest(exportDir, manifest); err != nil {
		t.Fatalf("writeSiteExportManifest() error = %v", err)
	}
	dockerDir := t.TempDir()
	logPath := filepath.Join(dockerDir, "docker-args.txt")
	dockerScript := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$DOCKER_LOG\"\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	t.Setenv("DOCKER_LOG", logPath)
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	stubLocalWordPressTransferEstimate(t, 1024)
	stubLocalSnapshotExpandedSize(t, 1024)
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
		if got := Run([]string{"env", "import", exportDir, "--name", "client-handoff", "--yes"}); got != 0 {
			t.Fatalf("Run(env import) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Env import plan:", "source type:   nf site export", "snapshot:      client-handoff", rewriteRulesRegeneratedMessage, "WordPress import restored.", "Imported snapshot:", "Safety snapshot:"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("env import stdout missing %q:\n%s", want, stdout)
		}
	}
	importedDir := envSnapshotDir(cfg, "client-handoff")
	if _, err := os.Stat(envSnapshotMetadataPath(cfg, "client-handoff")); err != nil {
		t.Fatalf("imported snapshot metadata missing: %v", err)
	}
	extracted := filepath.Join(t.TempDir(), "extracted")
	if err := extractTarGzArchive(envSnapshotHostWpContentArchive(cfg, "client-handoff"), extracted); err != nil {
		t.Fatalf("extractTarGzArchive(imported wp-content) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(extracted, "wp-content", "uploads", "image.jpg")); err != nil {
		t.Fatalf("imported uploads missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(extracted, "wp-content", "plugins", "demo", "demo.php")); err != nil {
		t.Fatalf("imported plugins missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(extracted, "wp-content", "themes", "remote-theme", "style.css")); !os.IsNotExist(err) {
		t.Fatalf("remote theme should not be imported into env snapshot: %v", err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(docker log) error = %v", err)
	}
	logText := string(logData)
	assertContainsInOrder(t, logText, []string{"wp db export", "wp db import", "--user\nroot", "chown -R nonfiction:www-data", "wp\n--skip-themes\n--skip-plugins\noption\nupdate\nhome\nhttp://localhost:18440", "wp\n--skip-themes\n--skip-plugins\noption\nupdate\nsiteurl\nhttp://localhost:18440", "wp\nsearch-replace\nhttps://www.example.com\nhttp://localhost:18440\n--all-tables-with-prefix\n--skip-columns=guid", "wp\ntheme\nactivate\ntheme", "wp\nrewrite\nflush", "wp\ncache\nflush"})
	if !strings.Contains(stdout, "path: "+importedDir) {
		t.Fatalf("env import stdout missing imported path %s:\n%s", importedDir, stdout)
	}
}

func TestEnvFinalizeLocalRestoreShortCircuitsRewriteMaintenance(t *testing.T) {
	for _, test := range []struct {
		name           string
		failCommand    string
		wantRewrite    bool
		wantError      string
		forbiddenAfter string
	}{
		{name: "search replace failure", failCommand: "search-replace", wantRewrite: false, forbiddenAfter: "rewrite flush"},
		{name: "theme activation failure", failCommand: "theme activate", wantRewrite: false, forbiddenAfter: "rewrite flush"},
		{name: "rewrite failure", failCommand: "rewrite flush", wantRewrite: true, wantError: "failed to flush WordPress rewrite rules in the local environment", forbiddenAfter: "cache flush"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dockerDir := t.TempDir()
			logPath := filepath.Join(dockerDir, "docker.log")
			dockerScript := []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$DOCKER_LOG\"\ncase \"$*\" in *\"$FAIL_COMMAND\"*) exit 23 ;; esac\nexit 0\n")
			if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
				t.Fatalf("WriteFile(docker) error = %v", err)
			}
			t.Setenv("DOCKER_LOG", logPath)
			t.Setenv("FAIL_COMMAND", test.failCommand)
			t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			cfg := envConfig{EnvDir: t.TempDir(), WordpressPort: 18432, ThemeSlug: "client"}
			var finalizeErr error
			stdout := captureStdout(t, func() {
				finalizeErr = envFinalizeLocalRestore(cfg, "https://www.example.com")
			})
			if finalizeErr == nil {
				t.Fatal("envFinalizeLocalRestore() error = nil, want failure")
			}
			if test.wantError != "" && !strings.Contains(finalizeErr.Error(), test.wantError) {
				t.Fatalf("envFinalizeLocalRestore() error = %q, want %q", finalizeErr, test.wantError)
			}
			logData, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("ReadFile(docker log) error = %v", err)
			}
			logText := string(logData)
			if got := strings.Contains(logText, "rewrite flush"); got != test.wantRewrite {
				t.Fatalf("rewrite flush present = %t, want %t:\n%s", got, test.wantRewrite, logText)
			}
			if strings.Contains(logText, test.forbiddenAfter) {
				t.Fatalf("command %q ran after failure:\n%s", test.forbiddenAfter, logText)
			}
			if strings.Contains(stdout, rewriteRulesRegeneratedMessage) {
				t.Fatalf("rewrite success output printed after failure:\n%s", stdout)
			}
		})
	}
}

func TestFlushRemoteRewriteRulesPropagatesContextualFailure(t *testing.T) {
	oldRunSSH := runSSHCommandFn
	var command []string
	runSSHCommandFn = func(args []string) error {
		command = append([]string(nil), args...)
		return fmt.Errorf("remote command failed")
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })
	target := envRemoteSyncTarget{Env: "staging", SSHUser: "client", SSHHost: "203.0.113.10", SSHPort: "12345", WordPressPath: "/www/client/public", WPCommand: "wp"}
	stdout := captureStdout(t, func() {
		err := flushRemoteRewriteRules(target)
		if err == nil || !strings.Contains(err.Error(), "failed to flush WordPress rewrite rules on staging: remote command failed") {
			t.Fatalf("flushRemoteRewriteRules() error = %v", err)
		}
	})
	if got := strings.Join(command, " "); !strings.Contains(got, "wp --path=/www/client/public rewrite flush") || strings.Contains(got, "--hard") {
		t.Fatalf("remote rewrite command = %q", got)
	}
	if strings.Contains(stdout, rewriteRulesRegeneratedMessage) {
		t.Fatalf("rewrite success output printed after remote failure:\n%s", stdout)
	}
}

func TestRunEnvImportDryRunDoesNotCreateSnapshot(t *testing.T) {
	repoRoot, cfg := writeTestEnvProject(t)
	sourceDir := filepath.Join(t.TempDir(), "wordpress")
	if err := os.MkdirAll(filepath.Join(sourceDir, "wp-content", "uploads"), 0o755); err != nil {
		t.Fatalf("MkdirAll(source) error = %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "database.sql")
	if err := os.WriteFile(dbPath, []byte("db"), 0o644); err != nil {
		t.Fatalf("WriteFile(database.sql) error = %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	stdout := captureStdout(t, func() {
		if got := Run([]string{"env", "import", sourceDir, "--db", dbPath, "--name", "dry-run-import", "--dry-run"}); got != 0 {
			t.Fatalf("Run(env import --dry-run) = %d, want 0", got)
		}
	})
	if !strings.Contains(stdout, "mode:          dry-run") || !strings.Contains(stdout, "No data was changed") {
		t.Fatalf("env import dry-run output = %q", stdout)
	}
	if _, err := os.Stat(envSnapshotDir(cfg, "dry-run-import")); !os.IsNotExist(err) {
		t.Fatalf("dry-run import created snapshot: %v", err)
	}
}

func TestResolveEnvImportSourceDetectsAnySQLDump(t *testing.T) {
	sourceDir := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(filepath.Join(sourceDir, "uploads"), 0o755); err != nil {
		t.Fatalf("MkdirAll(uploads) error = %v", err)
	}
	dbPath := filepath.Join(sourceDir, "client-live-2026-06-26.sql.gz")
	if err := os.WriteFile(dbPath, []byte("db"), 0o644); err != nil {
		t.Fatalf("WriteFile(sql.gz) error = %v", err)
	}
	source, err := resolveEnvImportSource(envImportOptions{source: sourceDir})
	if err != nil {
		t.Fatalf("resolveEnvImportSource() error = %v", err)
	}
	if source.Database != dbPath {
		t.Fatalf("Database = %q, want %q", source.Database, dbPath)
	}
}

func TestResolveEnvImportSourcePrefersDatabaseSQLDump(t *testing.T) {
	sourceDir := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(filepath.Join(sourceDir, "uploads"), 0o755); err != nil {
		t.Fatalf("MkdirAll(uploads) error = %v", err)
	}
	for _, name := range []string{"client-live.sql.gz", "database.sql", "database.sql.gz"} {
		if err := os.WriteFile(filepath.Join(sourceDir, name), []byte(name), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	want := filepath.Join(sourceDir, "database.sql.gz")
	source, err := resolveEnvImportSource(envImportOptions{source: sourceDir})
	if err != nil {
		t.Fatalf("resolveEnvImportSource() error = %v", err)
	}
	if source.Database != want {
		t.Fatalf("Database = %q, want %q", source.Database, want)
	}
}

func TestCreateEnvImportSnapshotSupportsTopLevelUploads(t *testing.T) {
	_, cfg := writeTestEnvProject(t)
	sourceDir := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(filepath.Join(sourceDir, "uploads", "2026"), 0o755); err != nil {
		t.Fatalf("MkdirAll(uploads) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "uploads", "2026", "image.jpg"), []byte("image"), 0o644); err != nil {
		t.Fatalf("WriteFile(upload) error = %v", err)
	}
	dbPath := filepath.Join(sourceDir, "database.sql.gz")
	if err := os.WriteFile(dbPath, []byte("db"), 0o644); err != nil {
		t.Fatalf("WriteFile(database.sql.gz) error = %v", err)
	}
	source := envImportSource{InputPath: sourceDir, FilesPath: sourceDir, Database: dbPath}
	if err := createEnvImportSnapshot(cfg, "top-level-uploads", source, time.Now()); err != nil {
		t.Fatalf("createEnvImportSnapshot() error = %v", err)
	}
	extracted := filepath.Join(t.TempDir(), "extracted")
	if err := extractTarGzArchive(envSnapshotHostWpContentArchive(cfg, "top-level-uploads"), extracted); err != nil {
		t.Fatalf("extractTarGzArchive() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(extracted, "wp-content", "uploads", "2026", "image.jpg")); err != nil {
		t.Fatalf("top-level uploads were not imported as wp-content/uploads: %v", err)
	}
}

func TestCreateEnvImportSnapshotSupportsUploadsDirectorySource(t *testing.T) {
	_, cfg := writeTestEnvProject(t)
	uploadsDir := filepath.Join(t.TempDir(), "uploads")
	if err := os.MkdirAll(filepath.Join(uploadsDir, "2026"), 0o755); err != nil {
		t.Fatalf("MkdirAll(uploads) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(uploadsDir, "2026", "image.jpg"), []byte("image"), 0o644); err != nil {
		t.Fatalf("WriteFile(upload) error = %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "database.sql")
	if err := os.WriteFile(dbPath, []byte("db"), 0o644); err != nil {
		t.Fatalf("WriteFile(database.sql) error = %v", err)
	}
	source := envImportSource{InputPath: uploadsDir, FilesPath: uploadsDir, Database: dbPath}
	if err := createEnvImportSnapshot(cfg, "uploads-dir", source, time.Now()); err != nil {
		t.Fatalf("createEnvImportSnapshot() error = %v", err)
	}
	extracted := filepath.Join(t.TempDir(), "extracted")
	if err := extractTarGzArchive(envSnapshotHostWpContentArchive(cfg, "uploads-dir"), extracted); err != nil {
		t.Fatalf("extractTarGzArchive() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(extracted, "wp-content", "uploads", "2026", "image.jpg")); err != nil {
		t.Fatalf("uploads directory source was not imported as wp-content/uploads: %v", err)
	}
}

func TestEnvSnapshotCreateArchivesChecksLocalSpaceBeforeDocker(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	dockerDir := t.TempDir()
	markerPath := filepath.Join(t.TempDir(), "docker-called")
	dockerScript := []byte("#!/bin/sh\ntouch \"$DOCKER_CALLED\"\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	t.Setenv("DOCKER_CALLED", markerPath)
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := envConfig{ProjectSlug: "client", EnvDir: config.EnvDir("client"), Compose: "docker compose", WordpressService: "wordpress", DockerUser: "nonfiction"}
	stubLocalWordPressTransferEstimate(t, 1073741824)
	stubLocalAvailableDisk(t, 1)

	err := envSnapshotCreateArchives(cfg, "too-large")
	if err == nil || !strings.Contains(err.Error(), "not enough disk space for local snapshot workspace") {
		t.Fatalf("envSnapshotCreateArchives() error = %v, want local disk-space error", err)
	}
	if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
		t.Fatalf("docker command ran before disk check: %v", statErr)
	}
}

func TestExecuteEnvPullFinalizesLocalDestinationThemeAndCache(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	dockerDir := t.TempDir()
	logPath := filepath.Join(dockerDir, "docker-args.txt")
	dockerScript := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$DOCKER_LOG\"\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	t.Setenv("DOCKER_LOG", logPath)
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := envConfig{ProjectSlug: "client", EnvDir: config.EnvDir("client"), Compose: "docker compose", WordpressService: "wordpress", DockerUser: "nonfiction", ThemeMountSlug: "local-theme", ThemeSlug: "remote-theme", Themes: []wordpressThemeSpec{{Slug: "local-theme", Source: wordpressThemeRepoSource, Path: "theme"}}, UploadsPath: "uploads", WordpressPort: 18432}
	target := envRemoteSyncTarget{RemoteName: "live", Provider: "linode", SiteID: "client.app1-linode", Env: "live", URL: "https://client.app1-linode.nonfiction.dev/", SSHUser: "nonfiction", SSHHost: "app1-linode.nonfiction.dev", SSHPort: "22", WordPressPath: "/var/www/sites/client/public", WPCommand: "sudo -u www-data wp", SudoFileOps: true}
	oldRunSSH := runSSHCommandFn
	runSSHCommandFn = func(args []string) error { return nil }
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })
	oldRunRsync := runRsyncCommandFn
	var rsyncCommands [][]string
	runRsyncCommandFn = func(args []string) error {
		rsyncCommands = append(rsyncCommands, append([]string(nil), args...))
		return nil
	}
	t.Cleanup(func() { runRsyncCommandFn = oldRunRsync })
	stubEnvRemoteDiskOutput(t, "1024\n", "1073741824\n", "2048\n")
	stubLocalAvailableDisk(t, 1073741824)
	stubLocalWordPressTransferEstimate(t, 1024)
	stubLocalSnapshotExpandedSize(t, 1024)

	stdout := captureStdout(t, func() {
		if got := executeEnvPull(cfg, target); got != 0 {
			t.Fatalf("executeEnvPull() = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, stdout, []string{"Preparing local safety snapshot before remote pull...", "Checking available disk space for local snapshot workspace...", "Estimating remote database and non-upload wp-content size for pull...", "Checking available disk space for remote export workspace...", "Preparing remote database and non-upload wp-content export for pull...", "Measuring remote pull export...", "Checking available disk space for local pull snapshot...", "Preparing to rsync remote database and non-upload wp-content to local...", "Restoring pulled database and non-upload wp-content into local env...", "Preparing to rsync remote uploads to local...", "Finalizing local WordPress URL, theme, rewrite rules, and cache...", "Rewrite rules regenerated.", "Recording complete pulled snapshot locally..."})
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(docker log) error = %v", err)
	}
	logText := string(logData)
	assertContainsInOrder(t, logText, []string{"wp db export", "wp db import", "--user\nroot", "chown -R nonfiction:www-data", "wp\n--skip-themes\n--skip-plugins\noption\nupdate\nhome\nhttp://localhost:18432", "wp\n--skip-themes\n--skip-plugins\noption\nupdate\nsiteurl\nhttp://localhost:18432", "wp\nsearch-replace\nhttps://client.app1-linode.nonfiction.dev\nhttp://localhost:18432\n--all-tables-with-prefix\n--skip-columns=guid", "wp\ntheme\nactivate\nlocal-theme", "wp\nrewrite\nflush", "wp\ncache\nflush"})
	if strings.Contains(logText, "wp\ntheme\nactivate\nremote-theme") {
		t.Fatalf("pull activated remote theme slug locally:\n%s", logText)
	}
	if len(rsyncCommands) != 2 {
		t.Fatalf("pull rsync commands len = %d, want archive and uploads: %#v", len(rsyncCommands), rsyncCommands)
	}
	if !slices.Contains(rsyncCommands[0], "--progress") || !slices.Contains(rsyncCommands[1], "--progress") || !slices.Contains(rsyncCommands[1], "--delete") {
		t.Fatalf("pull rsync args missing progress/delete: %#v", rsyncCommands)
	}
	if !strings.Contains(rsyncCommands[1][len(rsyncCommands[1])-2], "/wp-content/uploads/") || rsyncCommands[1][len(rsyncCommands[1])-1] != cfg.managedUploadsDir()+string(filepath.Separator) {
		t.Fatalf("pull uploads rsync args = %#v", rsyncCommands[1])
	}
}

func TestExecuteEnvPullSkipsMissingLocalDestinationTheme(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	dockerDir := t.TempDir()
	logPath := filepath.Join(dockerDir, "docker-args.txt")
	dockerScript := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$DOCKER_LOG\"\ncase \"$*\" in\n  *'theme is-installed local-theme'*) exit 1 ;;\nesac\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	t.Setenv("DOCKER_LOG", logPath)
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := envConfig{ProjectSlug: "client", EnvDir: config.EnvDir("client"), Compose: "docker compose", WordpressService: "wordpress", DockerUser: "nonfiction", ThemeMountSlug: "local-theme", ThemeSlug: "remote-theme", Themes: []wordpressThemeSpec{{Slug: "local-theme", Source: wordpressThemeRepoSource, Path: "theme"}}, UploadsPath: "uploads", WordpressPort: 18432}
	target := envRemoteSyncTarget{RemoteName: "live", Provider: "linode", SiteID: "client.app1-linode", Env: "live", URL: "https://client.app1-linode.nonfiction.dev/", SSHUser: "nonfiction", SSHHost: "app1-linode.nonfiction.dev", SSHPort: "22", WordPressPath: "/var/www/sites/client/public", WPCommand: "sudo -u www-data wp", SudoFileOps: true}
	oldRunSSH := runSSHCommandFn
	runSSHCommandFn = func(args []string) error { return nil }
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })
	oldRunRsync := runRsyncCommandFn
	runRsyncCommandFn = func(args []string) error { return nil }
	t.Cleanup(func() { runRsyncCommandFn = oldRunRsync })
	stubEnvRemoteDiskOutput(t, "1024\n", "1073741824\n", "2048\n")
	stubLocalAvailableDisk(t, 1073741824)
	stubLocalWordPressTransferEstimate(t, 1024)
	stubLocalSnapshotExpandedSize(t, 1024)

	stderr := captureStderr(t, func() {
		captureStdout(t, func() {
			if got := executeEnvPull(cfg, target); got != 0 {
				t.Fatalf("executeEnvPull() = %d, want 0", got)
			}
		})
	})
	if !strings.Contains(stderr, `Warning: theme "local-theme" is not installed locally; skipping theme activation.`) {
		t.Fatalf("stderr missing missing-theme warning:\n%s", stderr)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(docker log) error = %v", err)
	}
	logText := string(logData)
	assertContainsInOrder(t, logText, []string{"wp db import", "--user\nroot", "chown -R nonfiction:www-data", "wp\n--skip-themes\n--skip-plugins\noption\nupdate\nhome\nhttp://localhost:18432", "wp\n--skip-themes\n--skip-plugins\noption\nupdate\nsiteurl\nhttp://localhost:18432", "wp\nsearch-replace\nhttps://client.app1-linode.nonfiction.dev\nhttp://localhost:18432\n--all-tables-with-prefix\n--skip-columns=guid", "wp\ntheme\nis-installed\nlocal-theme", "wp\nrewrite\nflush", "wp\ncache\nflush"})
	if strings.Contains(logText, "wp\ntheme\nactivate\nlocal-theme") {
		t.Fatalf("pull activated missing local theme:\n%s", logText)
	}
}

func TestNormalizeWordPressDestinationURLRejectsMalformedURL(t *testing.T) {
	if _, err := normalizeWordPressDestinationURL("https:///wp", true); err == nil {
		t.Fatalf("normalizeWordPressDestinationURL(https:///wp) error = nil, want error")
	}
	got, err := normalizeWordPressDestinationURL("client.example.test/", true)
	if err != nil {
		t.Fatalf("normalizeWordPressDestinationURL(valid) error = %v", err)
	}
	if got != "https://client.example.test" {
		t.Fatalf("normalizeWordPressDestinationURL(valid) = %q, want https://client.example.test", got)
	}
}

func TestNormalizeWordPressTablePrefix(t *testing.T) {
	for _, value := range []string{"wp_", "wpmc_", "client2_"} {
		if got, err := normalizeWordPressTablePrefix(value); err != nil || got != value {
			t.Fatalf("normalizeWordPressTablePrefix(%q) = %q, %v", value, got, err)
		}
	}
	for _, value := range []string{"", "wp-prefix", "wp prefix", "wp;drop"} {
		if _, err := normalizeWordPressTablePrefix(value); err == nil {
			t.Fatalf("normalizeWordPressTablePrefix(%q) error = nil", value)
		}
	}
}

func TestParseEnvImportTablePrefix(t *testing.T) {
	opts, err := parseEnvImportArgs([]string{"source", "--table-prefix=wpmc_"})
	if err != nil {
		t.Fatalf("parseEnvImportArgs() error = %v", err)
	}
	if opts.tablePrefix != "wpmc_" {
		t.Fatalf("parseEnvImportArgs() tablePrefix = %q", opts.tablePrefix)
	}
	if _, err := parseEnvImportArgs([]string{"source", "--table-prefix=wp-prefix"}); err == nil {
		t.Fatal("parseEnvImportArgs(invalid prefix) error = nil")
	}
}

func TestApplyLocalWordPressTablePrefixRecreatesWordPress(t *testing.T) {
	envDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(envDir, ".env"), []byte("COMPOSE_PROJECT_NAME=nf_client_env\nWP_TABLE_PREFIX=wp_\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	dockerDir := t.TempDir()
	logPath := filepath.Join(dockerDir, "docker.log")
	dockerScript := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$DOCKER_LOG\"\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	t.Setenv("DOCKER_LOG", logPath)
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldRunOutput := runCommandSpecOutputSilentFn
	runCommandSpecOutputSilentFn = func(spec execSpec) (string, error) {
		return "wpmc_\n", nil
	}
	t.Cleanup(func() { runCommandSpecOutputSilentFn = oldRunOutput })
	cfg := envConfig{EnvDir: envDir, Compose: "docker compose", WordpressService: "wordpress", DockerUser: "nonfiction"}

	if err := applyLocalWordPressTablePrefix(cfg, "wpmc_"); err != nil {
		t.Fatalf("applyLocalWordPressTablePrefix() error = %v", err)
	}
	envData, err := os.ReadFile(filepath.Join(envDir, ".env"))
	if err != nil {
		t.Fatalf("ReadFile(.env) error = %v", err)
	}
	if !strings.Contains(string(envData), "WP_TABLE_PREFIX=wpmc_\n") {
		t.Fatalf("managed .env did not persist table prefix:\n%s", envData)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(docker.log) error = %v", err)
	}
	assertContainsInOrder(t, string(logData), []string{"up\n-d\n--no-deps\n--force-recreate\nwordpress", "exec\n--user\nnonfiction\nwordpress\nsh\n-lc"})
}

func TestDatabaseExportScriptsCaptureWordPressTablePrefix(t *testing.T) {
	cfg := envConfig{}
	target := envRemoteSyncTarget{WordPressPath: "/www/client/public", WPCommand: "wp"}
	for name, script := range map[string]string{
		"local snapshot": envSnapshotCreateScript(cfg, "snapshot"),
		"local push":     envPushTransferCreateScript(cfg, "push"),
		"remote":         remoteExportScript(target, "/tmp/export"),
		"site export":    remoteSiteExportScript(target, "/tmp/export"),
	} {
		if !strings.Contains(script, "config get table_prefix") || !strings.Contains(script, tablePrefixFilename) {
			t.Fatalf("%s script does not capture table prefix:\n%s", name, script)
		}
		if strings.Contains(script, "%!") {
			t.Fatalf("%s script contains fmt error:\n%s", name, script)
		}
	}
}

func TestEnsureManagedEnvPreservesTablePrefixState(t *testing.T) {
	cfg := envConfig{ProjectSlug: "client", EnvDir: t.TempDir(), WordpressPort: 18432, MailpitPort: 18433, AdminerPort: 18434, DBUser: "client", DBPassword: "db-pass", AdminUser: "admin", AdminPassword: "admin-pass", AdminEmail: "admin@example.test"}
	if err := os.WriteFile(filepath.Join(cfg.EnvDir, ".env"), []byte("WP_TABLE_PREFIX=wpmc_\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	if err := ensureManagedEnv(cfg); err != nil {
		t.Fatalf("ensureManagedEnv() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.EnvDir, ".env"))
	if err != nil {
		t.Fatalf("ReadFile(.env) error = %v", err)
	}
	if !strings.Contains(string(data), "WP_TABLE_PREFIX=wpmc_\n") {
		t.Fatalf("ensureManagedEnv() reset table prefix state:\n%s", data)
	}
}

func TestExecuteEnvPushFinalizesRemoteDestinationThemeAndCache(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	dockerDir := t.TempDir()
	dockerScript := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := envConfig{ProjectSlug: "client", EnvDir: config.EnvDir("client"), Compose: "docker compose", WordpressService: "wordpress", DockerUser: "nonfiction", ThemeMountSlug: "local-theme", ThemeSlug: "remote-theme", UploadsPath: "uploads", WordpressPort: 18432}
	if err := os.MkdirAll(cfg.managedUploadsDir(), 0o755); err != nil {
		t.Fatalf("MkdirAll(uploads) error = %v", err)
	}
	target := envRemoteSyncTarget{RemoteName: "live", Provider: "linode", SiteID: "client.app1-linode", Env: "live", URL: "https://client.app1-linode.nonfiction.dev/", SSHUser: "nonfiction", SSHHost: "app1-linode.nonfiction.dev", SSHPort: "22", WordPressPath: "/var/www/sites/client/public", WPCommand: "sudo -u www-data wp", SudoFileOps: true}
	oldRunRsync := runRsyncCommandFn
	var rsyncCommands [][]string
	runRsyncCommandFn = func(args []string) error {
		rsyncCommands = append(rsyncCommands, append([]string(nil), args...))
		return nil
	}
	t.Cleanup(func() { runRsyncCommandFn = oldRunRsync })
	oldRunSSH := runSSHCommandFn
	var sshCommands [][]string
	runSSHCommandFn = func(args []string) error {
		sshCommands = append(sshCommands, append([]string(nil), args...))
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })
	stubEnvRemoteDiskOutput(t, "1024\n", "1073741824\n", "1073741824\n", "1073741824\n")
	stubLocalAvailableDisk(t, 1073741824)
	stubLocalWordPressTransferEstimate(t, 1024)
	stubLocalPushTransferSizes(t, 1024, 1024)

	stdout := captureStdout(t, func() {
		if got := executeEnvPush(cfg, target); got != 0 {
			t.Fatalf("executeEnvPush() = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, stdout, []string{"Preparing local database and non-upload wp-content transfer for remote push...", "Checking available disk space for local push transfer workspace...", "Measuring local push transfer payload...", "Estimating current remote database and uploads size for backup...", "Checking available disk space for remote push backup workspace...", "Creating remote backup before push...", "Checking available disk space for remote push workspace...", "Preparing remote push workspace...", "Preparing to rsync local database archive to remote...", "Preparing to rsync local non-upload wp-content archive to remote...", "Checking available disk space for remote push import workspace...", "Importing pushed database and non-upload wp-content on remote...", "Preparing to rsync local uploads to remote...", "Finalizing remote WordPress URL, theme, rewrite rules, and cache...", "Env pushed.", "Remote backup: cleaned up after successful push to avoid production disk growth"})
	if strings.Contains(stdout, "Local snapshot:") {
		t.Fatalf("push stdout advertised a retained local snapshot:\n%s", stdout)
	}
	if matches, err := filepath.Glob(filepath.Join(envSnapshotProjectDir(cfg), "push-live-*")); err != nil || len(matches) != 0 {
		t.Fatalf("push left local transfer workspace matches = %#v, err = %v", matches, err)
	}
	if len(sshCommands) != 7 {
		t.Fatalf("ssh commands len = %d, want prefix-check/export/mkdir/import/finalize/cleanup/cleanup: %#v", len(sshCommands), sshCommands)
	}
	if !strings.Contains(sshCommands[0][len(sshCommands[0])-1], "config get table_prefix") {
		t.Fatalf("first remote command did not check table prefix: %#v", sshCommands[0])
	}
	importScript := sshCommands[3][len(sshCommands[3])-1]
	finalizeScript := sshCommands[4][len(sshCommands[4])-1]
	if !strings.Contains(importScript, "db import") {
		t.Fatalf("remote import script missing db import:\n%s", importScript)
	}
	assertContainsInOrder(t, finalizeScript, []string{"--skip-themes --skip-plugins option update home https://client.app1-linode.nonfiction.dev", "--skip-themes --skip-plugins option update siteurl https://client.app1-linode.nonfiction.dev", "search-replace http://localhost:18432 https://client.app1-linode.nonfiction.dev --all-tables-with-prefix --skip-columns=guid", "theme is-installed remote-theme", "theme activate remote-theme", "rewrite flush", "Rewrite rules regenerated.", "cache flush"})
	if !strings.Contains(finalizeScript, "skipping theme activation") {
		t.Fatalf("remote finalize script missing missing-theme warning:\n%s", finalizeScript)
	}
	if strings.Contains(finalizeScript, "theme activate local-theme") {
		t.Fatalf("push activated local theme mount slug remotely:\n%s", finalizeScript)
	}
	if len(rsyncCommands) != 3 {
		t.Fatalf("push rsync commands len = %d, want database/archive/uploads: %#v", len(rsyncCommands), rsyncCommands)
	}
	if !slices.Contains(rsyncCommands[0], "--progress") || !slices.Contains(rsyncCommands[1], "--progress") || !slices.Contains(rsyncCommands[2], "--progress") || !slices.Contains(rsyncCommands[2], "--delete") {
		t.Fatalf("push rsync args missing progress/delete: %#v", rsyncCommands)
	}
	if !strings.HasSuffix(rsyncCommands[0][len(rsyncCommands[0])-1], "/database.sql.gz") || !strings.HasSuffix(rsyncCommands[1][len(rsyncCommands[1])-1], "/wp-content.tar.gz") {
		t.Fatalf("push archive rsync args = %#v", rsyncCommands[:2])
	}
	if rsyncCommands[2][len(rsyncCommands[2])-2] != cfg.managedUploadsDir()+string(filepath.Separator) || !strings.Contains(rsyncCommands[2][len(rsyncCommands[2])-1], "/wp-content/uploads/") || !slices.Contains(rsyncCommands[2], "--rsync-path=sudo rsync") || !slices.Contains(rsyncCommands[2], "--chown=www-data:www-data") {
		t.Fatalf("push uploads rsync args = %#v", rsyncCommands[2])
	}
	if !strings.Contains(sshCommands[5][len(sshCommands[5])-1], "rm -rf -- /tmp/nf-push-") || !strings.Contains(sshCommands[6][len(sshCommands[6])-1], "rm -rf -- /tmp/nf-backup-") {
		t.Fatalf("push cleanup commands = %#v", sshCommands[5:])
	}
}

func TestExecuteEnvPushRejectsTablePrefixMismatchBeforeBackup(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	dockerDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := envConfig{ProjectSlug: "client", EnvDir: config.EnvDir("client"), Compose: "docker compose", WordpressService: "wordpress", DockerUser: "nonfiction", UploadsPath: "uploads", WordpressPort: 18432}
	if err := os.MkdirAll(cfg.managedUploadsDir(), 0o755); err != nil {
		t.Fatalf("MkdirAll(uploads) error = %v", err)
	}
	target := envRemoteSyncTarget{RemoteName: "production", Provider: "kinsta", SiteID: "client-kinsta", Env: "live", URL: "https://www.example.com/", SSHUser: "client", SSHHost: "203.0.113.10", SSHPort: "12345", WordPressPath: "/www/client/public", WPCommand: "wp"}
	stubEnvRemoteDiskOutput(t, "1024\n", "1073741824\n")
	stubLocalAvailableDisk(t, 1073741824)
	stubLocalWordPressTransferEstimate(t, 1024)
	stubLocalPushTransferSizes(t, 1024, 1024)
	oldRunSSH := runSSHCommandFn
	var sshCommands [][]string
	runSSHCommandFn = func(args []string) error {
		sshCommands = append(sshCommands, append([]string(nil), args...))
		return fmt.Errorf("table prefix mismatch")
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })
	oldRunRsync := runRsyncCommandFn
	runRsyncCommandFn = func(args []string) error {
		t.Fatalf("runRsyncCommandFn called after prefix mismatch: %#v", args)
		return nil
	}
	t.Cleanup(func() { runRsyncCommandFn = oldRunRsync })

	stderr := captureStderr(t, func() {
		captureStdout(t, func() {
			if got := executeEnvPush(cfg, target); got != 1 {
				t.Fatalf("executeEnvPush() = %d, want 1", got)
			}
		})
	})
	if !strings.Contains(stderr, "table prefix mismatch") {
		t.Fatalf("stderr missing prefix mismatch:\n%s", stderr)
	}
	if len(sshCommands) != 1 || !strings.Contains(sshCommands[0][len(sshCommands[0])-1], "config get table_prefix") || strings.Contains(sshCommands[0][len(sshCommands[0])-1], "db export") {
		t.Fatalf("ssh commands = %#v, want prefix check only", sshCommands)
	}
}

func TestExecuteEnvPushRetainsRemoteBackupAfterRemoteChangeFailure(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	dockerDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := envConfig{ProjectSlug: "client", EnvDir: config.EnvDir("client"), Compose: "docker compose", WordpressService: "wordpress", DockerUser: "nonfiction", ThemeSlug: "remote-theme", UploadsPath: "uploads", WordpressPort: 18432}
	if err := os.MkdirAll(cfg.managedUploadsDir(), 0o755); err != nil {
		t.Fatalf("MkdirAll(uploads) error = %v", err)
	}
	target := envRemoteSyncTarget{RemoteName: "production", Provider: "linode", SiteID: "client.app1-linode", Env: "live", URL: "https://client.app1-linode.nonfiction.dev/", SSHUser: "nonfiction", SSHHost: "app1-linode.nonfiction.dev", SSHPort: "22", WordPressPath: "/var/www/sites/client/public", WPCommand: "sudo -u www-data wp", SudoFileOps: true}
	stubEnvRemoteDiskOutput(t, "1024\n", "1073741824\n", "1073741824\n", "1073741824\n")
	stubLocalAvailableDisk(t, 1073741824)
	stubLocalWordPressTransferEstimate(t, 1024)
	stubLocalPushTransferSizes(t, 1024, 1024)
	oldRunRsync := runRsyncCommandFn
	runRsyncCommandFn = func(args []string) error { return nil }
	t.Cleanup(func() { runRsyncCommandFn = oldRunRsync })
	oldRunSSH := runSSHCommandFn
	var sshCommands [][]string
	runSSHCommandFn = func(args []string) error {
		sshCommands = append(sshCommands, append([]string(nil), args...))
		if strings.Contains(args[len(args)-1], "rewrite flush") {
			return fmt.Errorf("rewrite flush failed")
		}
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })

	stderr := captureStderr(t, func() {
		captureStdout(t, func() {
			if got := executeEnvPush(cfg, target); got != 1 {
				t.Fatalf("executeEnvPush() = %d, want 1", got)
			}
		})
	})
	assertContainsInOrder(t, stderr, []string{"rewrite flush failed", "Remote backup retained for recovery: /tmp/nf-backup-", "Remove it after recovery to free production disk space."})
	if len(sshCommands) != 6 || !strings.Contains(sshCommands[5][len(sshCommands[5])-1], "rm -rf -- /tmp/nf-push-") {
		t.Fatalf("ssh commands = %#v, want prefix-check/backup/export/import/finalize plus push cleanup", sshCommands)
	}
	for _, command := range sshCommands {
		if strings.Contains(command[len(command)-1], "rm -rf -- /tmp/nf-backup-") {
			t.Fatalf("remote backup was cleaned up after failed push: %#v", sshCommands)
		}
	}
}

func TestExecuteEnvPushChecksRemoteSpaceBeforeRsync(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	dockerDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := envConfig{ProjectSlug: "client", EnvDir: config.EnvDir("client"), Compose: "docker compose", WordpressService: "wordpress", DockerUser: "nonfiction", UploadsPath: "uploads", WordpressPort: 18432}
	if err := os.MkdirAll(cfg.managedUploadsDir(), 0o755); err != nil {
		t.Fatalf("MkdirAll(uploads) error = %v", err)
	}
	target := envRemoteSyncTarget{RemoteName: "production", Provider: "kinsta", SiteID: "client-kinsta", Env: "live", URL: "https://www.example.com/", SSHUser: "client", SSHHost: "203.0.113.10", SSHPort: "12345", WordPressPath: "/www/client/public", WPCommand: "wp"}
	scripts := stubEnvRemoteDiskOutput(t, "0\n", "1\n")
	stubLocalAvailableDisk(t, 1073741824)
	stubLocalWordPressTransferEstimate(t, 1024)
	stubLocalPushTransferSizes(t, 1024, 1024)
	oldRunSSH := runSSHCommandFn
	runSSHCommandFn = func(args []string) error {
		t.Fatalf("runSSHCommandFn called after failed disk check: %#v", args)
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })
	oldRunRsync := runRsyncCommandFn
	runRsyncCommandFn = func(args []string) error {
		t.Fatalf("runRsyncCommandFn called after failed disk check: %#v", args)
		return nil
	}
	t.Cleanup(func() { runRsyncCommandFn = oldRunRsync })

	stderr := captureStderr(t, func() {
		captureStdout(t, func() {
			if got := executeEnvPush(cfg, target); got != 1 {
				t.Fatalf("executeEnvPush() = %d, want 1", got)
			}
		})
	})
	if !strings.Contains(stderr, "not enough disk space for remote push backup workspace at /tmp") {
		t.Fatalf("stderr missing remote disk error:\n%s", stderr)
	}
	if len(*scripts) != 2 || !strings.Contains((*scripts)[0], "information_schema.tables") || !strings.Contains((*scripts)[1], "df -Pk /tmp") {
		t.Fatalf("remote disk check scripts = %#v", *scripts)
	}
}

func TestExecuteEnvPushChecksPostBackupSpaceBeforeRsync(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	dockerDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := envConfig{ProjectSlug: "client", EnvDir: config.EnvDir("client"), Compose: "docker compose", WordpressService: "wordpress", DockerUser: "nonfiction", UploadsPath: "uploads", WordpressPort: 18432}
	if err := os.MkdirAll(cfg.managedUploadsDir(), 0o755); err != nil {
		t.Fatalf("MkdirAll(uploads) error = %v", err)
	}
	target := envRemoteSyncTarget{RemoteName: "production", Provider: "linode", SiteID: "client.app1-linode", Env: "live", URL: "https://client.app1-linode.nonfiction.dev/", SSHUser: "nonfiction", SSHHost: "app1-linode.nonfiction.dev", SSHPort: "22", WordPressPath: "/var/www/sites/client/public", WPCommand: "sudo -u www-data wp", SudoFileOps: true}
	stubEnvRemoteDiskOutput(t, "1024\n", "1073741824\n", "1\n")
	stubLocalAvailableDisk(t, 1073741824)
	stubLocalWordPressTransferEstimate(t, 1024)
	stubLocalPushTransferSizes(t, 1024, 1024)
	oldRunSSH := runSSHCommandFn
	var sshCommands [][]string
	runSSHCommandFn = func(args []string) error {
		sshCommands = append(sshCommands, append([]string(nil), args...))
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })
	oldRunRsync := runRsyncCommandFn
	runRsyncCommandFn = func(args []string) error {
		t.Fatalf("runRsyncCommandFn called after failed post-backup disk check: %#v", args)
		return nil
	}
	t.Cleanup(func() { runRsyncCommandFn = oldRunRsync })

	stderr := captureStderr(t, func() {
		captureStdout(t, func() {
			if got := executeEnvPush(cfg, target); got != 1 {
				t.Fatalf("executeEnvPush() = %d, want 1", got)
			}
		})
	})
	if !strings.Contains(stderr, "not enough disk space for remote push workspace at /tmp") {
		t.Fatalf("stderr missing remote push workspace error:\n%s", stderr)
	}
	if len(sshCommands) != 3 || !strings.Contains(sshCommands[0][len(sshCommands[0])-1], "config get table_prefix") || !strings.Contains(sshCommands[1][len(sshCommands[1])-1], "db export") || !strings.Contains(sshCommands[1][len(sshCommands[1])-1], "/tmp/nf-backup-") || !strings.Contains(sshCommands[2][len(sshCommands[2])-1], "rm -rf -- /tmp/nf-backup-") {
		t.Fatalf("ssh commands = %#v, want prefix check, backup export, then backup cleanup", sshCommands)
	}
}

func TestExecuteEnvPullChecksLocalSpaceBeforeRsync(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	dockerDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := envConfig{ProjectSlug: "client", EnvDir: config.EnvDir("client"), Compose: "docker compose", WordpressService: "wordpress", DockerUser: "nonfiction", UploadsPath: "uploads", WordpressPort: 18432}
	target := envRemoteSyncTarget{RemoteName: "production", Provider: "linode", SiteID: "client.app1-linode", Env: "live", URL: "https://client.app1-linode.nonfiction.dev/", SSHUser: "nonfiction", SSHHost: "app1-linode.nonfiction.dev", SSHPort: "22", WordPressPath: "/var/www/sites/client/public", WPCommand: "sudo -u www-data wp", SudoFileOps: true}
	stubEnvRemoteDiskOutput(t, "0\n", "1073741824\n", "1073741824\n")
	stubLocalAvailableDiskOutputs(t, 1073741824, 1)
	stubLocalWordPressTransferEstimate(t, 1024)
	stubLocalSnapshotExpandedSize(t, 1024)
	oldRunSSH := runSSHCommandFn
	var sshCommands [][]string
	runSSHCommandFn = func(args []string) error {
		sshCommands = append(sshCommands, append([]string(nil), args...))
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })
	oldRunRsync := runRsyncCommandFn
	runRsyncCommandFn = func(args []string) error {
		t.Fatalf("runRsyncCommandFn called after failed local disk check: %#v", args)
		return nil
	}
	t.Cleanup(func() { runRsyncCommandFn = oldRunRsync })

	stderr := captureStderr(t, func() {
		captureStdout(t, func() {
			if got := executeEnvPull(cfg, target); got != 1 {
				t.Fatalf("executeEnvPull() = %d, want 1", got)
			}
		})
	})
	if !strings.Contains(stderr, "not enough disk space for local pull snapshot") {
		t.Fatalf("stderr missing local disk error:\n%s", stderr)
	}
	if len(sshCommands) != 2 || !strings.Contains(sshCommands[0][len(sshCommands[0])-1], "db export") || !strings.Contains(sshCommands[1][len(sshCommands[1])-1], "rm -rf -- /tmp/nf-pull-") {
		t.Fatalf("ssh commands before local disk failure = %#v", sshCommands)
	}
}

func TestExecuteEnvPullCleansRemoteTempAfterExportFailure(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	dockerDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := envConfig{ProjectSlug: "client", EnvDir: config.EnvDir("client"), Compose: "docker compose", WordpressService: "wordpress", DockerUser: "nonfiction", UploadsPath: "uploads", WordpressPort: 18432}
	target := envRemoteSyncTarget{RemoteName: "production", Provider: "linode", SiteID: "client.app1-linode", Env: "live", URL: "https://client.app1-linode.nonfiction.dev/", SSHUser: "nonfiction", SSHHost: "app1-linode.nonfiction.dev", SSHPort: "22", WordPressPath: "/var/www/sites/client/public", WPCommand: "sudo -u www-data wp", SudoFileOps: true}
	stubEnvRemoteDiskOutput(t, "0\n", "1073741824\n")
	stubLocalAvailableDisk(t, 1073741824)
	stubLocalWordPressTransferEstimate(t, 1024)
	stubLocalSnapshotExpandedSize(t, 1024)
	oldRunSSH := runSSHCommandFn
	var sshCommands [][]string
	runSSHCommandFn = func(args []string) error {
		sshCommands = append(sshCommands, append([]string(nil), args...))
		if len(sshCommands) == 1 {
			return fmt.Errorf("export failed")
		}
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSH })
	oldRunRsync := runRsyncCommandFn
	runRsyncCommandFn = func(args []string) error {
		t.Fatalf("runRsyncCommandFn called after failed export: %#v", args)
		return nil
	}
	t.Cleanup(func() { runRsyncCommandFn = oldRunRsync })

	stderr := captureStderr(t, func() {
		captureStdout(t, func() {
			if got := executeEnvPull(cfg, target); got != 1 {
				t.Fatalf("executeEnvPull() = %d, want 1", got)
			}
		})
	})
	if !strings.Contains(stderr, "export failed") {
		t.Fatalf("stderr missing export error:\n%s", stderr)
	}
	if len(sshCommands) != 2 || !strings.Contains(sshCommands[0][len(sshCommands[0])-1], "db export") || !strings.Contains(sshCommands[1][len(sshCommands[1])-1], "rm -rf -- /tmp/nf-pull-") {
		t.Fatalf("ssh commands = %#v, want export then cleanup", sshCommands)
	}
}

func TestRemoteImportScriptPreservesRepoPluginDirs(t *testing.T) {
	cfg := envConfig{RepoPluginMounts: []envPluginMount{{Slug: "agency-credit", Host: "/repo/plugins/agency-credit"}}}
	target := envRemoteSyncTarget{WordPressPath: "/var/www/sites/client/public", WPCommand: "sudo -u www-data wp", SudoFileOps: true}
	script := remoteImportScript(cfg, target, "/tmp/nf-push-client", "wp_")
	for _, want := range []string{"repo_plugins=agency-credit", "case \" $repo_plugins \" in *\" $base \"*) continue ;; esac", "sudo rm -rf \"$entry\"", "sudo tar --exclude=wp-content/plugins/agency-credit --exclude=wp-content/mu-plugins --exclude='wp-content/mu-plugins/*' -tzf /tmp/nf-push-client/wp-content.tar.gz >/dev/null", "sudo tar --exclude=wp-content/plugins/agency-credit --exclude=wp-content/mu-plugins --exclude='wp-content/mu-plugins/*' -xzf /tmp/nf-push-client/wp-content.tar.gz -C /var/www/sites/client/public", "sudo chown -R www-data:www-data /var/www/sites/client/public/wp-content/plugins /var/www/sites/client/public/wp-content/languages"} {
		if !strings.Contains(script, want) {
			t.Fatalf("remote import script missing %q:\n%s", want, script)
		}
	}
	for _, unwanted := range []string{"wp-content-extract", "copy_dir_contents", "clear_dir_contents /var/www/sites/client/public/wp-content/uploads", "clear_dir_contents /var/www/sites/client/public/wp-content/mu-plugins", "chown -R www-data:www-data /var/www/sites/client/public/wp-content/uploads", "chown -R www-data:www-data /var/www/sites/client/public/wp-content/mu-plugins", "rm -rf /var/www/sites/client/public/wp-content/uploads /var/www/sites/client/public/wp-content/plugins", "rm -rf /var/www/sites/client/public/wp-content/plugins", "tar --no-overwrite-dir"} {
		if strings.Contains(script, unwanted) {
			t.Fatalf("remote import script should not remove repo plugin mount point %q:\n%s", unwanted, script)
		}
	}
}

func TestEnsureDiskSpaceShowsBytesWhenRoundedValuesMatch(t *testing.T) {
	required := int64(20*1024*1024*1024 + 1)
	available := int64(20 * 1024 * 1024 * 1024)
	err := ensureDiskSpace("remote push workspace", "/tmp", required, available)
	if err == nil {
		t.Fatalf("ensureDiskSpace() error = nil, want insufficient-space error")
	}
	for _, want := range []string{"need 20 GiB (21474836481 bytes)", "available 20 GiB (21474836480 bytes)"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q:\n%v", want, err)
		}
	}
}

func TestEnvRemoteSyncConfirmationMessageShowsLocalLeftAndRemoteName(t *testing.T) {
	cfg := envConfig{ProjectSlug: "themestarter"}
	target := envRemoteSyncTarget{RemoteName: "test", SiteID: "themestarter.app3-linode", Env: "live"}
	wantPush := "Push local env themestarter ──▶ remote test / themestarter.app3-linode:live?\nThis will replace the remote database and mutable wp-content with your local env data."
	if got := envRemoteSyncConfirmationMessage("push", cfg, target); got != wantPush {
		t.Fatalf("push confirmation = %q, want %q", got, wantPush)
	}
	wantPull := "Pull local env themestarter ◀── remote test / themestarter.app3-linode:live?\nThis will replace your local database and mutable wp-content with remote data."
	if got := envRemoteSyncConfirmationMessage("pull", cfg, target); got != wantPull {
		t.Fatalf("pull confirmation = %q, want %q", got, wantPull)
	}
	if strings.Contains(envRemoteSyncConfirmationMessage("push", cfg, target), "sync") || strings.Contains(envRemoteSyncConfirmationMessage("pull", cfg, target), "sync") {
		t.Fatalf("confirmation messages should not use sync wording")
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
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}}}
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
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	cfg := envConfig{ProjectSlug: "client", RepoRoot: repoRoot, ThemePath: "theme", EnvDir: config.EnvDir("client"), WordpressPort: 18432, MailpitPort: 18433, Compose: "docker compose", WordpressService: "wordpress", ThemeMountSlug: "theme", UploadsPath: "uploads", ThemeSlug: "theme"}
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
	project := map[string]any{
		"version": 2,
		"project": map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{
			"slug":   "client",
			"source": "repo",
			"path":   "theme",
			"tasks":  map[string]any{"build": map[string]any{"description": "Build the theme assets", "run": "npm run build"}},
		}}},
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

	output := captureStdout(t, func() { _ = runThemeHelp() })
	assertContainsInOrder(t, output, []string{"tasks", "package", "\n\n  deploy <remote>", "rollback <remote>", "\nPackage Options:\n", "--source <path>", "\nDeploy Options:\n", "--dry-run", "--restart", "\nTheme tasks:\n", "build"})
	for _, wanted := range []string{"\n  tasks", "list configured theme tasks\n", "\n  package", "package a clean theme artifact\n", "\n  deploy <remote>", "deploy a packaged theme release\n", "\n  rollback <remote>", "roll back to the previous theme release\n", "\nPackage Options:\n", "\nDeploy Options:\n", "\nTheme tasks:\n"} {
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
	assertContainsInOrder(t, output, []string{"tasks", "package", "\n\n  deploy <remote>", "rollback <remote>", "\nPackage Options:\n", "--source <path>", "\nDeploy Options:\n", "--dry-run", "--restart"})
	for _, want := range []string{"\n  tasks", "list configured theme tasks\n", "\n  package", "package a clean theme artifact\n", "\n  deploy <remote>", "deploy a packaged theme release\n", "\n  rollback <remote>", "roll back to the previous theme release\n", "\nPackage Options:\n", "\nDeploy Options:\n"} {
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
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme", "package": map[string]any{"output": "dist/client-v{version}.zip"}}}},
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
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}}, "remotes": map[string]any{}}
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
	assertContainsInOrder(t, output, []string{
		"foobar-app1-linode\n",
		"──────────────────\n",
		"Name       foobar\n",
		"Provider   linode\n",
		"Target     app1-linode\n",
		"Environments\n",
		"env      php  url",
		"live",
		"staging",
	})
	for _, notWant := range []string{"Site       foobar-app1-linode", "Environments:"} {
		if strings.Contains(output, notWant) {
			t.Fatalf("site show output contains %q:\n%s", notWant, output)
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
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}}, "remotes": map[string]any{"production": "client-kinsta:live"}}
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
	if metadata["version"] != float64(2) {
		t.Fatalf("version = %v, want 2", metadata["version"])
	}
	if got := strings.Index(string(data), "\"version\""); got < 0 || got > strings.Index(string(data), "\"project\"") {
		t.Fatalf("nf.json top-level order = %s, want version before project", data)
	}
	if project, ok := metadata["project"].(map[string]any); !ok || project["slug"] != "client" {
		t.Fatalf("project block = %#v, want slug client", metadata["project"])
	} else if project["password_version"] != float64(0) {
		t.Fatalf("project.password_version = %#v, want 0", project["password_version"])
	} else if _, exists := project["name"]; exists {
		t.Fatalf("project block = %#v, did not want name", metadata["project"])
	}
	if wordpress, ok := metadata["wordpress"].(map[string]any); !ok {
		t.Fatalf("wordpress block = %#v, want wordpress config", metadata["wordpress"])
	} else if themes, ok := wordpress["themes"].([]any); !ok || len(themes) != 4 {
		t.Fatalf("wordpress.themes = %#v, want repo theme plus bundled WordPress themes", wordpress["themes"])
	} else if theme, ok := themes[0].(map[string]any); !ok || theme["slug"] != "client" || theme["source"] != "repo" || theme["path"] != "theme" {
		t.Fatalf("wordpress.themes[0] = %#v, want client repo theme at theme", themes[0])
	} else if themes[1] != "twentytwentyfive" || themes[2] != "twentytwentyfour" || themes[3] != "twentytwentythree" {
		t.Fatalf("wordpress.themes bundled defaults = %#v, want WordPress default themes", themes[1:])
	} else if _, exists := wordpress["theme_path"]; exists {
		t.Fatalf("wordpress.theme_path unexpectedly present: %#v", wordpress)
	} else if _, exists := wordpress["theme_slug"]; exists {
		t.Fatalf("wordpress.theme_slug unexpectedly present: %#v", wordpress)
	} else if _, exists := wordpress["plugins"]; exists {
		t.Fatalf("wordpress.plugins unexpectedly present: %#v", wordpress["plugins"])
	}
	if _, exists := metadata["local"]; exists {
		t.Fatalf("local unexpectedly present: %#v", metadata["local"])
	}
	theme := metadata["wordpress"].(map[string]any)["themes"].([]any)[0].(map[string]any)
	if packageConfig, ok := theme["package"].(map[string]any); !ok || packageConfig["output"] != "dist/client-v{version}.zip" {
		t.Fatalf("repo theme package = %#v, want dist/client-v{version}.zip", theme["package"])
	}
	if _, exists := metadata["remotes"]; exists {
		t.Fatalf("remotes unexpectedly present: %#v", metadata["remotes"])
	}
	if tasks, ok := theme["tasks"].(map[string]any); !ok {
		t.Fatalf("repo theme tasks = %#v, want task map", theme["tasks"])
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
	for _, dropped := range []string{"artifact", "tasks", "aliases", "env", "build", "deploy"} {
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

	if got := Run([]string{"init", "--project-slug", "client", "--theme-slug", "custom-theme", "--theme-source", "custom/theme", "--force"}); got != 0 {
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
	if wordpress, ok := metadata["wordpress"].(map[string]any); !ok {
		t.Fatalf("wordpress block = %#v, want wordpress config", metadata["wordpress"])
	} else if themes, ok := wordpress["themes"].([]any); !ok || len(themes) != 4 {
		t.Fatalf("wordpress.themes = %#v, want repo theme plus bundled WordPress themes", wordpress["themes"])
	} else if theme, ok := themes[0].(map[string]any); !ok || theme["slug"] != "custom-theme" || theme["source"] != "repo" || theme["path"] != "custom/theme" {
		t.Fatalf("wordpress.themes[0] = %#v, want explicit custom-theme repo theme at custom/theme", themes[0])
	} else if tasks, ok := theme["tasks"].(map[string]any); !ok || !strings.Contains(recordValueString(tasks["build"].(map[string]any)["run"]), "npm --prefix custom/theme run build") || !strings.Contains(recordValueString(tasks["composer"].(map[string]any)["run"]), "composer --working-dir=custom/theme") {
		t.Fatalf("wordpress.themes[0].tasks = %#v, want custom/theme command paths", theme["tasks"])
	} else if themes[1] != "twentytwentyfive" || themes[2] != "twentytwentyfour" || themes[3] != "twentytwentythree" {
		t.Fatalf("wordpress.themes bundled defaults = %#v, want WordPress default themes", themes[1:])
	}
}

func TestProjectInitThemeListSkipsBundledThemeDuplicate(t *testing.T) {
	themes := projectInitThemeList("client", "twentytwentyfive", "theme")
	if len(themes) != 3 {
		t.Fatalf("len(projectInitThemeList) = %d, want 3", len(themes))
	}
	if theme, ok := themes[0].(orderedObject); !ok || len(theme.Pairs) != 5 || theme.Pairs[0].Value != "twentytwentyfive" || theme.Pairs[1].Value != wordpressThemeRepoSource || theme.Pairs[2].Value != "theme" {
		t.Fatalf("projectInitThemeList()[0] = %#v, want repo twentytwentyfive", themes[0])
	}
	if themes[1] != "twentytwentyfour" || themes[2] != "twentytwentythree" {
		t.Fatalf("projectInitThemeList bundled themes = %#v, want remaining bundled defaults", themes[1:])
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
	if err := os.MkdirAll(filepath.Join(root, "theme-src"), 0o755); err != nil {
		t.Fatalf("MkdirAll(theme-src) error = %v", err)
	}
	metadata := &project.Manifest{
		Project: project.Project{Slug: "client"},
		WordPress: project.WordPress{Themes: []any{
			map[string]any{"slug": "client-theme", "source": "repo", "path": "theme-src"},
		}},
		Local: &project.Local{Compose: "docker compose", WordPressService: "wp-app", UploadsPath: "uploads"},
	}
	cfg, ok := loadEnvConfig(root, metadata)
	if !ok {
		t.Fatalf("loadEnvConfig() = false, want true")
	}
	compose := renderEnvCompose(cfg)
	for _, want := range []string{"wp-app:", "condition: service_healthy", "HOME: /home/nonfiction", "WP_CLI_CACHE_DIR: /tmp/wp-cli-cache", filepath.Join(root, "theme-src") + ":/var/www/html/wp-content/themes/client-theme", "./uploads:/var/www/html/wp-content/uploads", "./.nf-transfer:/env/uploads", config.SnapshotProjectDir("client") + ":/env-snapshots"} {
		if !strings.Contains(compose, want) {
			t.Fatalf("renderEnvCompose() missing %q:\n%s", want, compose)
		}
	}
	if strings.Contains(compose, "\t") {
		t.Fatalf("renderEnvCompose() contains a tab character:\n%s", compose)
	}
}

func TestRenderEnvComposeMountsConfiguredRepoPlugins(t *testing.T) {
	root := t.TempDir()
	for _, slug := range []string{"client-plugin", "client-tools"} {
		if err := os.MkdirAll(filepath.Join(root, "plugins", slug), 0o755); err != nil {
			t.Fatalf("MkdirAll(plugin) error = %v", err)
		}
	}
	metadata := &project.Manifest{
		Project: project.Project{Slug: "client"},
		WordPress: project.WordPress{Themes: []any{"twentytwentyfive"}, Plugins: []any{
			map[string]any{"slug": "client-plugin", "source": "repo"},
			map[string]any{"slug": "client-tools", "source": "repo"},
			map[string]any{"slug": "missing-plugin", "source": "repo"},
			"stream",
		}},
		Local: &project.Local{Compose: "docker compose", WordPressService: "wp-app", UploadsPath: "uploads"},
	}
	cfg, ok := loadEnvConfig(root, metadata)
	if !ok {
		t.Fatalf("loadEnvConfig() = false, want true")
	}
	compose := renderEnvCompose(cfg)
	for _, want := range []string{
		filepath.Join(root, "plugins", "client-plugin") + ":/var/www/html/wp-content/plugins/client-plugin",
		filepath.Join(root, "plugins", "client-tools") + ":/var/www/html/wp-content/plugins/client-tools",
	} {
		if !strings.Contains(compose, want) {
			t.Fatalf("renderEnvCompose() missing %q:\n%s", want, compose)
		}
	}
	if strings.Contains(compose, "missing-plugin:/var/www/html") || strings.Contains(compose, "stream:/var/www/html") {
		t.Fatalf("renderEnvCompose() mounted a non-existent or non-repo plugin:\n%s", compose)
	}
}

func TestEnsureManagedEnvUsesConfiguredDockerImages(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	if err := saveGlobalConfig(map[string]string{
		"docker_db_image":        "mariadb:11.4",
		"docker_wordpress_image": "wordpress:php8.3-custom-apache",
		"docker_user":            "developer",
	}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}

	root := t.TempDir()
	metadata := &project.Manifest{
		Project:   project.Project{Slug: "client"},
		WordPress: project.WordPress{Themes: []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}},
		Local:     &project.Local{Compose: "docker compose", WordPressService: "wordpress", UploadsPath: "uploads"},
	}
	cfg, ok := loadEnvConfig(root, metadata)
	if !ok {
		t.Fatalf("loadEnvConfig() = false, want true")
	}
	cfg.AdminUser = "admin"
	cfg.AdminEmail = "web@example.com"
	cfg.AdminPassword = "wp-pass"
	cfg.DBUser = "client"
	cfg.DBPassword = "db-pass"
	cfg, err := envConfigWithAdminCredentials(cfg)
	if err != nil {
		t.Fatalf("envConfigWithAdminCredentials() error = %v", err)
	}
	if err := ensureManagedEnv(cfg); err != nil {
		t.Fatalf("ensureManagedEnv() error = %v", err)
	}

	composeData, err := os.ReadFile(filepath.Join(cfg.EnvDir, "docker-compose.yml"))
	if err != nil {
		t.Fatalf("ReadFile(docker-compose.yml) error = %v", err)
	}
	compose := string(composeData)
	for _, want := range []string{"image: mariadb:11.4", "image: wordpress:php8.3-custom-apache"} {
		if !strings.Contains(compose, want) {
			t.Fatalf("docker-compose.yml missing %q:\n%s", want, compose)
		}
	}
	dockerfileData, err := os.ReadFile(filepath.Join(cfg.EnvDir, "wordpress", "Dockerfile"))
	if err != nil {
		t.Fatalf("ReadFile(Dockerfile) error = %v", err)
	}
	if !strings.Contains(string(dockerfileData), "FROM wordpress:php8.3-custom-apache") {
		t.Fatalf("Dockerfile missing configured FROM:\n%s", string(dockerfileData))
	}
	for _, want := range []string{"apt-get install -y --no-install-recommends", "iputils-ping", "dnsutils", "mariadb-client", "nano", "vim", "wp-cli.phar", "/usr/local/bin/wp", "useradd --create-home --shell /bin/bash --groups www-data developer", "export APACHE_RUN_USER=developer", "export APACHE_RUN_GROUP=www-data", "umask 0002", "chown -R developer:www-data"} {
		if !strings.Contains(string(dockerfileData), want) {
			t.Fatalf("Dockerfile missing %q:\n%s", want, string(dockerfileData))
		}
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
	wantJSON := "{\n  \"schema\": 1,\n  \"name\": \"2026-05-28-093012\",\n  \"project_slug\": \"client\",\n  \"created_at\": \"2026-05-28T09:30:12Z\",\n  \"env_path\": \"/data/nf/envs/client\",\n  \"compose_project\": \"nf_client_env\",\n  \"wordpress_url\": \"http://localhost:18432\",\n  \"contents\": {\n    \"database\": \"database.sql.gz\",\n    \"wp_content\": \"wp-content.tar.gz\",\n    \"wp_content_paths\": [\n      \"wp-content/uploads\",\n      \"wp-content/plugins\",\n      \"wp-content/languages\"\n    ]\n  }\n}\n"
	if gotJSON != wantJSON {
		t.Fatalf("envSnapshotMetadataJSON() =\n%s\nwant=\n%s", gotJSON, wantJSON)
	}
}

func TestRunEnvUpAutoInitializesProjectMetadata(t *testing.T) {
	repoRoot := filepath.Join(t.TempDir(), "client-site")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll(theme) error = %v", err)
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
		"> docker compose exec --user nonfiction wordpress wp core is-installed",
		"> docker compose exec --user nonfiction wordpress '<fix WordPress content permissions>'",
		"> docker compose exec --user nonfiction wordpress sh -lc",
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
	if err := os.MkdirAll(filepath.Join(root, "theme-src"), 0o755); err != nil {
		t.Fatalf("MkdirAll(theme-src) error = %v", err)
	}
	metadata := &project.Manifest{
		Project: project.Project{Slug: "client"},
		WordPress: project.WordPress{Themes: []any{
			map[string]any{"slug": "theme", "source": "repo", "path": "theme-src"},
		}},
		Local: &project.Local{Compose: "env compose", WordPressService: "env-wp", UploadsPath: "env-uploads"},
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
	if got, want := cfg.ThemeMountSlug, "theme"; got != want {
		t.Fatalf("ThemeMountSlug = %q, want %q", got, want)
	}
	if got, want := cfg.ThemePath, filepath.Join(root, "theme-src"); got != want {
		t.Fatalf("ThemePath = %q, want %q", got, want)
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
		"version": 2,
		"project": map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{
			"slug":   "theme",
			"source": "repo",
			"path":   "theme",
			"tasks": map[string]any{
				"capture": map[string]any{"description": "Capture passthrough args", "run": []any{"sh", "-c", "printf '%s\n' \"$@\" > \"$CAPTURE_FILE\"", "sh"}},
			},
		}}},
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
	wpA, mailpitA, adminerA := envDerivedPorts(" Client Site ")
	wpB, mailpitB, adminerB := envDerivedPorts("client_site")
	if wpA != wpB || mailpitA != mailpitB || adminerA != adminerB {
		t.Fatalf("envDerivedPorts() = (%d, %d, %d) and (%d, %d, %d), want matching ports", wpA, mailpitA, adminerA, wpB, mailpitB, adminerB)
	}
	if mailpitA != wpA+1 {
		t.Fatalf("envDerivedPorts() mailpit = %d, want wordpress+1 (%d)", mailpitA, wpA+1)
	}
	if adminerA != wpA+2 {
		t.Fatalf("envDerivedPorts() adminer = %d, want wordpress+2 (%d)", adminerA, wpA+2)
	}
	if wpA < 18000 || adminerA > 21999 {
		t.Fatalf("envDerivedPorts() = (%d, %d, %d), want ports in 18000-21999 block", wpA, mailpitA, adminerA)
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
	wpPort, mailpitPort, adminerPort := envDerivedPorts("client")
	cfg := envConfig{ProjectSlug: "client", WordpressPort: wpPort, MailpitPort: mailpitPort, AdminerPort: adminerPort, DBUser: "client", DBPassword: "db-pass"}
	want := fmt.Sprintf("COMPOSE_PROJECT_NAME=nf_client_env\nWP_PORT=%d\nMAILPIT_PORT=%d\nDB_UI_PORT=%d\nDB_NAME=client\nDB_USER=client\nDB_PASSWORD=db-pass\nDB_ROOT_PASSWORD=root\nWP_TABLE_PREFIX=wp_\nWP_URL=http://localhost:%d\nWP_TITLE=Client\nADMIN_USER=nonfiction\nADMIN_PASSWORD=admin\nADMIN_EMAIL=web@nonfiction.ca\n", wpPort, mailpitPort, adminerPort, wpPort)
	if got := renderEnvFile(cfg); got != want {
		t.Fatalf("renderEnvFile() = %q, want %q", got, want)
	}
}

func TestRenderEnvFileQuotesTitleWithSpaces(t *testing.T) {
	wpPort, mailpitPort, adminerPort := envDerivedPorts("theme-starter")
	cfg := envConfig{ProjectSlug: "theme-starter", WordpressPort: wpPort, MailpitPort: mailpitPort, AdminerPort: adminerPort}
	if got := renderEnvFile(cfg); !strings.Contains(got, "WP_TITLE='Theme Starter'\n") {
		t.Fatalf("renderEnvFile() missing shell-quoted title:\n%s", got)
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

func TestEnvConfigWithAdminCredentialsUsesPasswordVersion(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	if err := saveGlobalConfig(map[string]string{"default_wp_email": "web@nonfiction.ca"}); err != nil {
		t.Fatalf("saveGlobalConfig() error = %v", err)
	}
	cfg, err := envConfigWithAdminCredentials(envConfig{ProjectSlug: "foobar", PasswordVersion: "2"})
	if err != nil {
		t.Fatalf("envConfigWithAdminCredentials() error = %v", err)
	}
	if got, want := cfg.AdminPassword, passwords.DerivePassword("foobar:v2", "wp-admin", "test-salt"); got != want {
		t.Fatalf("AdminPassword = %q, want %q", got, want)
	}
	if got, want := cfg.AdminUser, defaultWordPressAdminUser; got != want {
		t.Fatalf("AdminUser = %q, want %q", got, want)
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
		"version":   2,
		"project":   map[string]any{"slug": "foobar", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}},
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

func TestRunEnvPasswordUsesProjectPasswordVersion(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "foobar", "password_version": 3},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}},
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
	want := passwords.DerivePassword("foobar:v3", "wp-admin", "test-salt") + "\n"
	if output != want {
		t.Fatalf("Run(env password) output = %q, want %q", output, want)
	}
}

func TestRunEnvPasswordPrintsRequestedPassword(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "foobar", "password_version": 3},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}},
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

	tests := []struct {
		flag    string
		purpose string
	}{
		{"--wp", "wp-admin"},
		{"--db", "mysql"},
		{"--basicauth", "basic-auth"},
	}
	for _, tt := range tests {
		output := captureStdout(t, func() {
			if got := Run([]string{"env", "password", tt.flag}); got != 0 {
				t.Fatalf("Run(env password %s) = %d, want 0", tt.flag, got)
			}
		})
		want := passwords.DerivePassword("foobar:v3", tt.purpose, "test-salt") + "\n"
		if output != want {
			t.Fatalf("env password %s output = %q, want %q", tt.flag, output, want)
		}
	}
}

func TestRunEnvPasswordPrintsRequestedRemotePassword(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "local", "password_version": 9},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}},
		"remotes":   map[string]any{"production": "client-app1-linode:staging"},
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
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "linode", "site_id": "client-app1-linode", "name": "client", "env": "live", "target": "app1-linode"},
		{"provider": "linode", "site_id": "client-app1-linode", "name": "client", "env": "staging", "target": "app1-linode"},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	tests := []struct {
		flag    string
		purpose string
	}{
		{"--wp", "wp-admin"},
		{"--db", "mysql"},
		{"--basicauth", "basic-auth"},
	}
	for _, tt := range tests {
		output := captureStdout(t, func() {
			if got := Run([]string{"env", "password", "production", tt.flag}); got != 0 {
				t.Fatalf("Run(env password production %s) = %d, want 0", tt.flag, got)
			}
		})
		want := passwords.DerivePassword("client", tt.purpose, "test-salt") + "\n"
		if output != want {
			t.Fatalf("env password production %s output = %q, want %q", tt.flag, output, want)
		}
	}
}

func TestRunEnvPasswordRejectsMultipleRemotes(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), []byte("{\n  \"version\": 2,\n  \"project\": {\"slug\": \"client\", \"password_version\": 0},\n  \"wordpress\": {\"themes\": [\"twentytwentyfive\"]}\n}\n"), 0o644); err != nil {
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
		if got := Run([]string{"env", "password", "production", "staging"}); got != 1 {
			t.Fatalf("Run(env password multiple remotes) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "env password takes at most one remote") {
		t.Fatalf("stderr = %q, want at-most-one error", stderr)
	}
}

func TestRunEnvThemesListReadsWordPressThemes(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version": 2,
		"project": map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{
			"twentytwentyfive",
			map[string]any{"slug": "acf-theme", "source": "cache", "auto_update": true, "note": "Paid theme"},
			map[string]any{"slug": "client", "source": "repo", "path": "theme", "note": "Child theme"},
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
		if got := Run([]string{"theme", "list"}); got != 0 {
			t.Fatalf("Run(theme list) = %d, want 0", got)
		}
	})
	for _, want := range []string{"theme", "source", "active", "auto-update", "twentytwentyfive", "wordpress.org", "acf-theme", "cache", "Paid theme", "client", "repo", "theme", "Child theme"} {
		if !strings.Contains(output, want) {
			t.Fatalf("theme list output missing %q:\n%s", want, output)
		}
	}
}

func TestLoadWordPressThemeSpecsAllowsNoRepoTheme(t *testing.T) {
	themes, err := loadWordPressThemeSpecs(&project.Manifest{
		WordPress: project.WordPress{Themes: []any{
			"twentytwentyfive",
			map[string]any{"slug": "paid-parent", "source": "cache", "auto_update": true},
		}},
	})
	if err != nil {
		t.Fatalf("loadWordPressThemeSpecs() error = %v", err)
	}
	if len(themes) != 2 {
		t.Fatalf("len(themes) = %d, want 2", len(themes))
	}
	if got := activeWordPressThemeSlug(themes); got != "twentytwentyfive" {
		t.Fatalf("activeWordPressThemeSlug() = %q, want twentytwentyfive", got)
	}
	if _, ok := repoWordPressThemeSpec(themes); ok {
		t.Fatalf("repoWordPressThemeSpec() found a repo theme, want none")
	}
}

func TestRunEnvThemesListRejectsEmptyThemes(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{}},
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

	stderr := captureStderr(t, func() {
		if got := Run([]string{"theme", "list"}); got != 1 {
			t.Fatalf("Run(theme list) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "nf.json wordpress.themes must include at least one theme") {
		t.Fatalf("stderr = %q, want empty themes error", stderr)
	}
}

func TestRunEnvThemesAddUpdatesProjectMetadata(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(repoRoot, "theme"), 0o755); err != nil {
		t.Fatalf("Mkdir(theme) error = %v", err)
	}
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}},
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
		if got := Run([]string{"theme", "add", "client", "--source", "repo"}); got != 0 {
			t.Fatalf("Run(theme add --source repo) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Added WordPress theme client to nf.json.") {
		t.Fatalf("theme add output unexpected:\n%s", output)
	}
	updated, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("ReadFile(nf.json) error = %v", err)
	}
	text := string(updated)
	for _, want := range []string{`"themes": [`, `"twentytwentyfive"`, `"slug": "client"`, `"source": "repo"`, `"path": "theme"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("nf.json missing %q:\n%s", want, text)
		}
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "themes")); !os.IsNotExist(err) {
		t.Fatalf("theme add should not scaffold a themes directory, stat err = %v", err)
	}
}

func TestProjectManifestRejectsOldAndInvalidSchemas(t *testing.T) {
	tests := map[string]string{
		"v1":                   `{"version":1,"project":{"slug":"client","password_version":0},"wordpress":{"themes":["twentytwentyfive"]}}`,
		"missing version":      `{"project":{"slug":"client","password_version":0},"wordpress":{"themes":["twentytwentyfive"]}}`,
		"non-2 version":        `{"version":3,"project":{"slug":"client","password_version":0},"wordpress":{"themes":["twentytwentyfive"]}}`,
		"legacy top-level env": `{"version":2,"project":{"slug":"client","password_version":0},"wordpress":{"themes":["twentytwentyfive"]},"env":{}}`,
		"legacy theme fields":  `{"version":2,"project":{"slug":"client","password_version":0},"wordpress":{"themes":["twentytwentyfive"],"theme_slug":"client"}}`,
		"empty themes":         `{"version":2,"project":{"slug":"client","password_version":0},"wordpress":{"themes":[]}}`,
	}
	for name, fixture := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(config.ProjectFile(root), []byte(fixture+"\n"), 0o644); err != nil {
				t.Fatalf("WriteFile(nf.json) error = %v", err)
			}
			if _, err := project.Load(root); err == nil {
				t.Fatal("project.Load() error = nil, want schema rejection")
			}
		})
	}
}

func TestRunEnvThemesAddRejectsSecondRepoTheme(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(repoRoot, "other-theme"), 0o755); err != nil {
		t.Fatalf("Mkdir(other-theme) error = %v", err)
	}
	project := map[string]any{
		"version": 2,
		"project": map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{
			map[string]any{"slug": "client", "source": "repo", "path": "theme"},
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

	stderr := captureStderr(t, func() {
		if got := Run([]string{"theme", "add", "other-theme", "--source", "repo", "--path", "other-theme"}); got != 1 {
			t.Fatalf("Run(theme add second repo) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "only one repo theme is allowed") {
		t.Fatalf("stderr = %q, want one-repo error", stderr)
	}
}

func TestRunEnvThemesActivateMovesThemeToTop(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version": 2,
		"project": map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{
			map[string]any{"slug": "client", "source": "repo", "path": "theme"},
			"twentytwentyfive",
			map[string]any{"slug": "paid-parent", "source": "cache", "note": "Paid parent"},
		}},
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
		if got := Run([]string{"theme", "activate", "paid-parent"}); got != 0 {
			t.Fatalf("Run(theme activate) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Made WordPress theme paid-parent first in wordpress.themes and configured it as active.") {
		t.Fatalf("theme activate output unexpected:\n%s", output)
	}
	updated, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("ReadFile(nf.json) error = %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(updated, &metadata); err != nil {
		t.Fatalf("Unmarshal(updated) error = %v", err)
	}
	wordpress := metadata["wordpress"].(map[string]any)
	themes := wordpress["themes"].([]any)
	first := themes[0].(map[string]any)
	if first["slug"] != "paid-parent" || first["source"] != "cache" || first["note"] != "Paid parent" {
		t.Fatalf("themes[0] = %#v, want paid-parent cache object", themes[0])
	}
	second := themes[1].(map[string]any)
	if second["slug"] != "client" || second["source"] != "repo" || second["path"] != "theme" {
		t.Fatalf("themes[1] = %#v, want original repo object", themes[1])
	}
	if themes[2] != "twentytwentyfive" {
		t.Fatalf("themes[2] = %#v, want original string theme", themes[2])
	}
}

func TestRunEnvThemesRemoveRejectsLastTheme(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}},
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

	stderr := captureStderr(t, func() {
		if got := Run([]string{"theme", "remove", "twentytwentyfive"}); got != 1 {
			t.Fatalf("Run(theme remove last) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "cannot remove the last configured WordPress theme") {
		t.Fatalf("stderr = %q, want last-theme error", stderr)
	}
	updated, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("ReadFile(nf.json) error = %v", err)
	}
	if text := string(updated); !strings.Contains(text, `"twentytwentyfive"`) {
		t.Fatalf("theme remove should leave nf.json unchanged:\n%s", text)
	}
}

func TestLocalThemeInstallScriptInstallsAndActivatesFirstTheme(t *testing.T) {
	themes := []wordpressThemeSpec{
		{Slug: "client", Source: wordpressThemeRepoSource, Path: "theme"},
		{Slug: "twentytwentyfive", Source: "wordpress.org", AutoUpdate: true},
	}
	script := localThemeInstallScript(themes, map[string]string{"client": localRepoThemeInstallSourceMark}, activeWordPressThemeSlug(themes))
	for _, want := range []string{"wp theme is-installed client", "wp theme install twentytwentyfive", "wp theme auto-updates enable twentytwentyfive", "wp theme activate client", "wp rewrite flush", rewriteRulesRegeneratedMessage} {
		if !strings.Contains(script, want) {
			t.Fatalf("theme install script missing %q:\n%s", want, script)
		}
	}
	assertContainsInOrder(t, script, []string{"wp theme activate client", "wp rewrite flush", rewriteRulesRegeneratedMessage})
	if strings.Contains(script, "auto-updates enable client") {
		t.Fatalf("repo theme should not get auto-updates enabled:\n%s", script)
	}
	if strings.Contains(script, "--hard") {
		t.Fatalf("theme install rewrite flush must not use --hard:\n%s", script)
	}
}

func TestRemoteThemeInstallScriptFlushesAfterActivation(t *testing.T) {
	target := envRemoteSyncTarget{Env: "staging", WordPressPath: "/www/client/public", WPCommand: "wp"}
	themes := []remoteThemeInstallSpec{{Theme: wordpressThemeSpec{Slug: "client", Source: "wordpress.org"}, InstallSource: "client"}}
	script := remoteThemeInstallScript(target, themes, "client")
	assertContainsInOrder(t, script, []string{"wp_cmd theme activate client", "wp_cmd rewrite flush", rewriteRulesRegeneratedMessage})
	if !strings.Contains(script, "Failed to flush WordPress rewrite rules on staging") {
		t.Fatalf("remote theme install script missing contextual rewrite error:\n%s", script)
	}
	if strings.Contains(script, "--hard") {
		t.Fatalf("remote theme install rewrite flush must not use --hard:\n%s", script)
	}
}

func TestWordPressThemeDiffsReportsExtraThemes(t *testing.T) {
	themes := []wordpressThemeSpec{{Slug: "client", Source: wordpressThemeRepoSource, Path: "theme"}}
	statuses := parseWordPressThemeDiffStatusOutput(themes, "client\tyes\tyes\tno\ntwentytwentytwo\tyes\tno\tyes\textra\n")
	diffs, drift := wordpressThemeDiffs(statuses, t.TempDir())
	if !drift {
		t.Fatalf("wordpressThemeDiffs drift = false, want true")
	}
	if len(diffs) != 2 {
		t.Fatalf("len(diffs) = %d, want 2: %#v", len(diffs), diffs)
	}
	if diffs[0].Change != "ok" {
		t.Fatalf("configured theme diff = %#v, want ok", diffs[0])
	}
	if diffs[1].Theme.Slug != "twentytwentytwo" || diffs[1].Change != "extra (inactive, auto-update on)" || !diffs[1].Drift {
		t.Fatalf("extra theme diff = %#v, want extra drift", diffs[1])
	}
}

func TestRunEnvPluginsListReadsWordPressPlugins(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version": 2,
		"project": map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{
			"stream",
			map[string]any{"slug": "acf-pro", "source": "$NF_PLUGIN_ACF_PRO_ZIP", "activate": true},
			map[string]any{"slug": "query-monitor", "activate": false},
			map[string]any{"slug": "sitepress-multilingual-cms", "install": false, "note": "Install manually from wpml.org account"},
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
		if got := Run([]string{"plugin", "list"}); got != 0 {
			t.Fatalf("Run(plugin list) = %d, want 0", got)
		}
	})
	for _, want := range []string{"plugin", "source", "install", "activate", "auto-update", "note", "acf-pro", "$NF_PLUGIN_ACF_PRO_ZIP", "query-monitor", "no", "stream", "wordpress.org", "yes", "sitepress-multilingual-cms", "-", "Install manually from wpml.org account"} {
		if !strings.Contains(output, want) {
			t.Fatalf("plugin list output missing %q:\n%s", want, output)
		}
	}
	manualLine := ""
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "sitepress-multilingual-cms") {
			manualLine = line
			break
		}
	}
	if manualLine == "" {
		t.Fatalf("plugin list output missing manual plugin line:\n%s", output)
	}
	if strings.Contains(manualLine, "wordpress.org") {
		t.Fatalf("manual plugin source should not default to wordpress.org:\n%s", output)
	}
}

func TestRunEnvPluginsAddUpdatesProjectMetadata(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{"stream"}},
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
		if got := Run([]string{"plugin", "add", "acf-pro", "--source", "$NF_PLUGIN_ACF_PRO_ZIP", "--no-auto-update"}); got != 0 {
			t.Fatalf("Run(plugin add) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Added WordPress plugin acf-pro to nf.json.") {
		t.Fatalf("plugin add output unexpected:\n%s", output)
	}
	updated, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("ReadFile(nf.json) error = %v", err)
	}
	text := string(updated)
	for _, want := range []string{`"version": 2`, `"wordpress":`, `"plugins": [`, `"stream"`, `"slug": "acf-pro"`, `"source": "$NF_PLUGIN_ACF_PRO_ZIP"`, `"auto_update": false`} {
		if !strings.Contains(text, want) {
			t.Fatalf("nf.json missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, `"activate": true`) || strings.Contains(text, `"auto_update": true`) {
		t.Fatalf("nf.json wrote noisy true defaults:\n%s", text)
	}
}

func TestRunPluginsAddRepoScaffoldsMissingPlugin(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), []byte("{\n  \"version\": 2,\n  \"project\": {\"slug\": \"client\", \"password_version\": 0},\n  \"wordpress\": {\"themes\": [\"twentytwentyfive\"]}\n}\n"), 0o644); err != nil {
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

	if got := Run([]string{"plugin", "add", "client-plugin", "--source", "repo"}); got != 0 {
		t.Fatalf("Run(plugin add --source repo) = %d, want 0", got)
	}
	pluginFile := filepath.Join(repoRoot, "plugins", "client-plugin", "client-plugin.php")
	data, err := os.ReadFile(pluginFile)
	if err != nil {
		t.Fatalf("ReadFile(scaffolded plugin) error = %v", err)
	}
	text := string(data)
	for _, want := range []string{"Plugin Name: Client Plugin", "Version: 0.1.0", "if (!defined('ABSPATH'))"} {
		if !strings.Contains(text, want) {
			t.Fatalf("scaffolded plugin missing %q:\n%s", want, text)
		}
	}
	metadata, err := loadProjectMetadataOrError(repoRoot)
	if err != nil {
		t.Fatalf("loadProjectMetadataOrError() error = %v", err)
	}
	plugins := metadata.WordPress.Plugins
	plugin, ok := plugins[0].(map[string]any)
	if !ok || plugin["slug"] != "client-plugin" || plugin["source"] != "repo" {
		t.Fatalf("wordpress.plugins[0] = %#v, want repo plugin object", plugins[0])
	}
}

func TestRunPluginsAddRepoLeavesExistingPluginDirectoryUntouched(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	pluginDir := filepath.Join(repoRoot, "plugins", "client-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(pluginDir) error = %v", err)
	}
	existingFile := filepath.Join(pluginDir, "custom.php")
	if err := os.WriteFile(existingFile, []byte("existing"), 0o644); err != nil {
		t.Fatalf("WriteFile(existing plugin) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), []byte("{\n  \"version\": 2,\n  \"project\": {\"slug\": \"client\", \"password_version\": 0},\n  \"wordpress\": {\"themes\": [\"twentytwentyfive\"]}\n}\n"), 0o644); err != nil {
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

	if got := Run([]string{"plugin", "add", "client-plugin", "--source", "repo"}); got != 0 {
		t.Fatalf("Run(plugin add --source repo) = %d, want 0", got)
	}
	if _, err := os.Stat(filepath.Join(pluginDir, "client-plugin.php")); !os.IsNotExist(err) {
		t.Fatalf("existing plugin directory should not be scaffolded, stat err = %v", err)
	}
	data, err := os.ReadFile(existingFile)
	if err != nil {
		t.Fatalf("ReadFile(existing plugin) error = %v", err)
	}
	if string(data) != "existing" {
		t.Fatalf("existing plugin file was modified: %q", data)
	}
}

func TestRunEnvPluginsAddCreatesWordPressPlugins(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), []byte("{\n  \"version\": 2,\n  \"project\": {\"slug\": \"client\", \"password_version\": 0},\n  \"wordpress\": {\"themes\": [\"twentytwentyfive\"]}\n}\n"), 0o644); err != nil {
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

	if got := Run([]string{"plugin", "add", "stream"}); got != 0 {
		t.Fatalf("Run(plugin add) = %d, want 0", got)
	}
	metadata, err := loadProjectMetadataOrError(repoRoot)
	if err != nil {
		t.Fatalf("loadProjectMetadataOrError() error = %v", err)
	}
	plugins := metadata.WordPress.Plugins
	if len(plugins) != 1 || plugins[0] != "stream" {
		t.Fatalf("wordpress.plugins = %#v, want [stream]", plugins)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "plugins")); !os.IsNotExist(err) {
		t.Fatalf("non-repo plugin add should not scaffold plugins directory, stat err = %v", err)
	}
}

func TestRunEnvPluginsAddManualPluginUpdatesProjectMetadata(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), []byte("{\n  \"version\": 2,\n  \"project\": {\"slug\": \"client\", \"password_version\": 0},\n  \"wordpress\": {\"themes\": [\"twentytwentyfive\"]}\n}\n"), 0o644); err != nil {
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

	if got := Run([]string{"plugin", "add", "sitepress-multilingual-cms", "--manual", "--note", "Install manually from wpml.org account"}); got != 0 {
		t.Fatalf("Run(plugin add --manual) = %d, want 0", got)
	}
	metadata, err := loadProjectMetadataOrError(repoRoot)
	if err != nil {
		t.Fatalf("loadProjectMetadataOrError() error = %v", err)
	}
	plugins := metadata.WordPress.Plugins
	if len(plugins) != 1 {
		t.Fatalf("wordpress.plugins length = %d, want 1", len(plugins))
	}
	plugin, ok := plugins[0].(map[string]any)
	if !ok {
		t.Fatalf("wordpress.plugins[0] = %#v, want object", plugins[0])
	}
	for key, want := range map[string]any{"slug": "sitepress-multilingual-cms", "install": false, "note": "Install manually from wpml.org account"} {
		if got := plugin[key]; got != want {
			t.Fatalf("wordpress.plugins[0].%s = %#v, want %#v", key, got, want)
		}
	}
}

func TestRunEnvPluginsAddRejectsDuplicate(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{"stream"}}}
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
		if got := Run([]string{"plugin", "add", "stream"}); got != 1 {
			t.Fatalf("Run(plugin add duplicate) = %d, want 1", got)
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
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{"stream", map[string]any{"slug": "acf-pro", "source": "$NF_PLUGIN_ACF_PRO_ZIP"}}}}
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
		if got := Run([]string{"plugin", "rm", "stream"}); got != 0 {
			t.Fatalf("Run(plugin rm) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Removed WordPress plugin stream from nf.json.") {
		t.Fatalf("plugin rm output unexpected:\n%s", output)
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
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{"stream"}}}
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
		if got := Run([]string{"plugin", "remove", "missing"}); got != 1 {
			t.Fatalf("Run(plugin remove missing) = %d, want 1", got)
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
		"version": 2,
		"project": map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}, "plugins": []any{
			"stream",
			map[string]any{"slug": "acf-pro", "source": "$NF_PLUGIN_ACF_PRO_ZIP", "activate": true},
			map[string]any{"slug": "sitepress-multilingual-cms", "install": false, "note": "Install manually from wpml.org account"},
		}},
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
		if got := Run([]string{"plugin", "install"}); got != 0 {
			t.Fatalf("Run(plugin install) = %d, want 0", got)
		}
	})
	for _, want := range []string{
		"> docker compose exec --user nonfiction wordpress wp core is-installed",
		"> docker compose exec --user nonfiction wordpress wp theme is-active theme",
		"> docker compose exec --user nonfiction wordpress '<wp plugin bootstrap script>'",
		"WordPress plugins installed.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("plugin install output missing %q:\n%s", want, output)
		}
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(log) error = %v", err)
	}
	logText := string(logData)
	for _, want := range []string{"wp plugin install stream --activate", "wp plugin auto-updates enable stream", "wp plugin is-active acf-pro", "wp plugin auto-updates enable acf-pro", "https://plugins.example.test/acf-pro.zip"} {
		if !strings.Contains(logText, want) {
			t.Fatalf("plugin install script missing %q:\n%s", want, logText)
		}
	}
	if strings.Contains(logText, "sitepress-multilingual-cms") {
		t.Fatalf("plugin install script should skip manual plugin:\n%s", logText)
	}
}

func TestRunEnvPluginsInstallUsesMountedRepoPluginSource(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	pluginDir := filepath.Join(repoRoot, "plugins", "client-plugin")
	if err := os.MkdirAll(filepath.Join(pluginDir, "includes"), 0o755); err != nil {
		t.Fatalf("MkdirAll(plugin) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "client-plugin.php"), []byte("<?php\n/* Plugin Name: Client Plugin */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(plugin main) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "includes", "feature.php"), []byte("<?php\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(plugin include) error = %v", err)
	}
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{map[string]any{"slug": "client-plugin", "source": "repo"}}},
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
	dataDir := t.TempDir()
	t.Setenv("DOCKER_LOG", logPath)
	t.Setenv("NF_DATA_HOME", dataDir)
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
		if got := Run([]string{"plugin", "install"}); got != 0 {
			t.Fatalf("Run(plugin install repo source) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "WordPress plugins installed.") {
		t.Fatalf("plugin install output missing success:\n%s", output)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(log) error = %v", err)
	}
	logText := string(logData)
	for _, want := range []string{"wp plugin is-installed client-plugin", "wp plugin activate client-plugin", "wp plugin auto-updates enable client-plugin"} {
		if !strings.Contains(logText, want) {
			t.Fatalf("repo plugin install script missing %q:\n%s", want, logText)
		}
	}
	if strings.Contains(logText, "wp plugin install") || strings.Contains(logText, ".nf-plugin-zips") {
		t.Fatalf("repo plugin should be activated from mount, not zip-installed:\n%s", logText)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "envs", "client", "uploads", ".nf-plugin-cache")); !os.IsNotExist(err) {
		t.Fatalf("temporary plugin cache directory was not cleaned up: %v", err)
	}
}

func TestRunEnvPluginsCacheAddListShow(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{}},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	sourceDir := filepath.Join(t.TempDir(), "acf-pro")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(source plugin) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "acf-pro.php"), []byte("<?php\n/* Plugin Name: ACF Pro */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(plugin) error = %v", err)
	}
	sourceZip := filepath.Join(t.TempDir(), "acf-pro.zip")
	if _, err := packagePluginSource(sourceDir, sourceZip, "acf-pro"); err != nil {
		t.Fatalf("packagePluginSource() error = %v", err)
	}
	dataDir := t.TempDir()
	t.Setenv("NF_DATA_HOME", dataDir)
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	addOutput := captureStdout(t, func() {
		if got := Run([]string{"plugin", "cache", "add", "acf-pro", sourceZip}); got != 0 {
			t.Fatalf("Run(cache add) = %d, want 0", got)
		}
	})
	cacheZip := filepath.Join(dataDir, "plugins", "acf-pro", "acf-pro.zip")
	if !strings.Contains(addOutput, "Cached WordPress plugin acf-pro at "+cacheZip) {
		t.Fatalf("cache add output missing cache path:\n%s", addOutput)
	}
	if _, err := os.Stat(cacheZip); err != nil {
		t.Fatalf("cached zip missing: %v", err)
	}
	listOutput := captureStdout(t, func() {
		if got := Run([]string{"plugin", "cache", "list"}); got != 0 {
			t.Fatalf("Run(cache list) = %d, want 0", got)
		}
	})
	for _, want := range []string{"plugin", "zip", "acf-pro", cacheZip} {
		if !strings.Contains(listOutput, want) {
			t.Fatalf("cache list output missing %q:\n%s", want, listOutput)
		}
	}
	showOutput := captureStdout(t, func() {
		if got := Run([]string{"plugin", "cache", "show", "acf-pro"}); got != 0 {
			t.Fatalf("Run(cache show) = %d, want 0", got)
		}
	})
	for _, want := range []string{"plugin: acf-pro", "status: available", cacheZip} {
		if !strings.Contains(showOutput, want) {
			t.Fatalf("cache show output missing %q:\n%s", want, showOutput)
		}
	}
	removeOutput := captureStdout(t, func() {
		if got := Run([]string{"plugin", "cache", "remove", "acf-pro"}); got != 0 {
			t.Fatalf("Run(cache remove) = %d, want 0", got)
		}
	})
	cacheDir := filepath.Join(dataDir, "plugins", "acf-pro")
	if !strings.Contains(removeOutput, "Removed WordPress plugin cache acf-pro from "+cacheDir) {
		t.Fatalf("cache remove output missing cache dir:\n%s", removeOutput)
	}
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Fatalf("cache dir still exists after remove: %v", err)
	}
}

func TestRunEnvPluginsCacheRemoveAliasDeletesCachedPlugin(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}}}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	dataDir := t.TempDir()
	t.Setenv("NF_DATA_HOME", dataDir)
	cacheDir := filepath.Join(dataDir, "plugins", "acf-pro")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(cacheDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "acf-pro.zip"), []byte("zip"), 0o644); err != nil {
		t.Fatalf("WriteFile(cache zip) error = %v", err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	if got := Run([]string{"plugin", "cache", "rm", "acf-pro"}); got != 0 {
		t.Fatalf("Run(cache rm) = %d, want 0", got)
	}
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Fatalf("cache dir still exists after rm alias: %v", err)
	}
}

func TestRunEnvThemesCacheRemoveDeletesCachedTheme(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	projectData := []byte("{\n  \"version\": 2,\n  \"project\": {\"slug\": \"client\", \"password_version\": 0},\n  \"wordpress\": {\"themes\": [\"twentytwentyfive\"]}\n}\n")
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), projectData, 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	t.Setenv("NF_DATA_HOME", t.TempDir())
	cacheDir := config.ThemeCacheThemeDir("paid-parent")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(cacheDir) error = %v", err)
	}
	if err := os.WriteFile(config.ThemeCacheZip("paid-parent"), []byte("zip"), 0o644); err != nil {
		t.Fatalf("WriteFile(cache zip) error = %v", err)
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
		if got := Run([]string{"theme", "cache", "rm", "paid-parent"}); got != 0 {
			t.Fatalf("Run(theme cache rm) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "Removed WordPress theme cache paid-parent from "+cacheDir) {
		t.Fatalf("theme cache remove output missing cache dir:\n%s", output)
	}
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Fatalf("theme cache dir still exists after remove: %v", err)
	}
}

func TestRunEnvPluginsCacheSaveArchivesInstalledPlugin(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{}},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	cacheSource := t.TempDir()
	installedPlugin := filepath.Join(cacheSource, "sitepress-multilingual-cms")
	if err := os.MkdirAll(filepath.Join(installedPlugin, "classes"), 0o755); err != nil {
		t.Fatalf("MkdirAll(installed plugin) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(installedPlugin, "sitepress.php"), []byte("<?php\n/* Plugin Name: WPML */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(plugin main) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(installedPlugin, "classes", "loader.php"), []byte("<?php\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(plugin include) error = %v", err)
	}
	dockerDir := t.TempDir()
	logPath := filepath.Join(dockerDir, "docker-args.txt")
	dockerScript := []byte("#!/bin/sh\nprintf '%s\n' \"$@\" >> \"$DOCKER_LOG\"\ncase \"$*\" in\n  *\".nf-plugin-cache-save\"*) mkdir -p \"$NF_DATA_HOME/envs/client/.nf-transfer/.nf-plugin-cache-save\"; tar -C \"$CACHE_SOURCE\" -czf \"$NF_DATA_HOME/envs/client/.nf-transfer/.nf-plugin-cache-save/sitepress-multilingual-cms.tar.gz\" sitepress-multilingual-cms ;;\nesac\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	dataDir := t.TempDir()
	t.Setenv("CACHE_SOURCE", cacheSource)
	t.Setenv("DOCKER_LOG", logPath)
	t.Setenv("NF_DATA_HOME", dataDir)
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
		if got := Run([]string{"plugin", "cache", "save", "sitepress-multilingual-cms"}); got != 0 {
			t.Fatalf("Run(cache save) = %d, want 0", got)
		}
	})
	cacheZip := filepath.Join(dataDir, "plugins", "sitepress-multilingual-cms", "sitepress-multilingual-cms.zip")
	if !strings.Contains(output, "Cached WordPress plugin sitepress-multilingual-cms at "+cacheZip) {
		t.Fatalf("cache save output missing cache path:\n%s", output)
	}
	names := readZipNames(t, cacheZip)
	for _, want := range []string{"sitepress-multilingual-cms/sitepress.php", "sitepress-multilingual-cms/classes/loader.php"} {
		if !names[want] {
			t.Fatalf("cached zip missing %q; names=%v", want, names)
		}
	}
}

func TestRunEnvPluginsInstallUsesCachePluginSource(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{map[string]any{"slug": "acf-pro", "source": "cache", "auto_update": false}}},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	dataDir := t.TempDir()
	t.Setenv("NF_DATA_HOME", dataDir)
	sourceDir := filepath.Join(t.TempDir(), "acf-pro")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(source plugin) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "acf-pro.php"), []byte("<?php\n/*\nPlugin Name: ACF Pro\nVersion: 2.8.1\n*/\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(plugin) error = %v", err)
	}
	if _, err := packagePluginSource(sourceDir, config.PluginCacheZip("acf-pro"), "acf-pro"); err != nil {
		t.Fatalf("packagePluginSource(cache) error = %v", err)
	}
	dockerDir := t.TempDir()
	logPath := filepath.Join(dockerDir, "docker-args.txt")
	dockerScript := []byte("#!/bin/sh\nprintf '%s\n' \"$@\" >> \"$DOCKER_LOG\"\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
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

	if got := Run([]string{"plugin", "install"}); got != 0 {
		t.Fatalf("Run(plugin install cache source) = %d, want 0", got)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(log) error = %v", err)
	}
	logText := string(logData)
	for _, want := range []string{"wp plugin install /env/uploads/.nf-plugin-cache/acf-pro.zip --activate", "wp plugin is-installed acf-pro", "wp plugin get acf-pro --field=version", "NF_CACHED_PLUGIN_VERSION=2.8.1", "version_compare", "wp plugin install /env/uploads/.nf-plugin-cache/acf-pro.zip --force --activate"} {
		if !strings.Contains(logText, want) {
			t.Fatalf("cache plugin install script missing %q:\n%s", want, logText)
		}
	}
	if strings.Contains(logText, "wp plugin auto-updates enable acf-pro") {
		t.Fatalf("cache plugin with auto_update false should not enable auto-updates:\n%s", logText)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "envs", "client", "uploads", ".nf-plugin-cache")); !os.IsNotExist(err) {
		t.Fatalf("temporary plugin cache directory was not cleaned up: %v", err)
	}
}

func TestRunEnvPluginsInstallSkipsUnavailableLocalSources(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version": 2,
		"project": map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{
			map[string]any{"slug": "gravityformsstripe", "source": "cache"},
			map[string]any{"slug": "client-plugin", "source": "repo"},
			"stream",
		}},
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
	dockerScript := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$DOCKER_LOG\"\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), dockerScript, 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	dataDir := t.TempDir()
	t.Setenv("DOCKER_LOG", logPath)
	t.Setenv("NF_DATA_HOME", dataDir)
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	stderr := captureStderr(t, func() {
		if got := Run([]string{"plugin", "install"}); got != 0 {
			t.Fatalf("Run(plugin install unavailable sources) = %d, want 0", got)
		}
	})
	missingZip := filepath.Join(dataDir, "plugins", "gravityformsstripe", "gravityformsstripe.zip")
	for _, want := range []string{
		"Skipping WordPress plugin gravityformsstripe: cache does not exist: " + missingZip,
		"Skipping WordPress plugin client-plugin: repo source directory does not exist: " + filepath.Join(repoRoot, "plugins", "client-plugin"),
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("plugin install stderr missing %q:\n%s", want, stderr)
		}
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(log) error = %v", err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "wp plugin install stream --activate") {
		t.Fatalf("plugin install script did not install available plugin:\n%s", logText)
	}
	if strings.Contains(logText, "gravityformsstripe") || strings.Contains(logText, "client-plugin") {
		t.Fatalf("plugin install script included an unavailable plugin:\n%s", logText)
	}
}

func TestRemotePluginInstallSpecsUploadsCacheSource(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("NF_DATA_HOME", dataDir)
	sourceDir := filepath.Join(t.TempDir(), "acf-pro")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(source plugin) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "acf-pro.php"), []byte("<?php\n/**\n * Plugin Name: ACF Pro\n * Version: 2.8.1\n */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(plugin) error = %v", err)
	}
	cacheZip := config.PluginCacheZip("acf-pro")
	if _, err := packagePluginSource(sourceDir, cacheZip, "acf-pro"); err != nil {
		t.Fatalf("packagePluginSource(cache) error = %v", err)
	}
	plugins := []wordpressPluginSpec{{Slug: "acf-pro", Source: "cache", Install: true, Activate: true}}
	remotePlugins, uploads, err := remotePluginInstallSpecs(t.TempDir(), plugins, "/tmp/nf-plugins-client", true, nil)
	if err != nil {
		t.Fatalf("remotePluginInstallSpecs() error = %v", err)
	}
	if len(remotePlugins) != 1 || remotePlugins[0].InstallSource != "/tmp/nf-plugins-client/acf-pro.zip" || remotePlugins[0].SourceVersion != "2.8.1" {
		t.Fatalf("remotePlugins = %#v, want cache remote install source", remotePlugins)
	}
	if len(uploads) != 1 || uploads[0].LocalPath != cacheZip || uploads[0].RemotePath != "/tmp/nf-plugins-client/acf-pro.zip" {
		t.Fatalf("uploads = %#v, want cache zip upload", uploads)
	}
	script := remotePluginInstallScript(envRemoteSyncTarget{WordPressPath: "/www/client/public", WPCommand: "wp"}, remotePlugins)
	for _, want := range []string{"wp_cmd plugin is-installed acf-pro", "wp_cmd plugin get acf-pro --field=version", "[ -n \"$installed_version\" ]", "NF_CACHED_PLUGIN_VERSION=2.8.1", "version_compare", "wp_cmd plugin install /tmp/nf-plugins-client/acf-pro.zip --force --activate"} {
		if !strings.Contains(script, want) {
			t.Fatalf("remote cache plugin script missing %q:\n%s", want, script)
		}
	}
}

func TestPluginZipVersionRequiresVersionedPluginHeader(t *testing.T) {
	sourceDir := filepath.Join(t.TempDir(), "client-plugin")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(source plugin) error = %v", err)
	}
	pluginFile := filepath.Join(sourceDir, "client-plugin.php")
	if err := os.WriteFile(pluginFile, []byte("<?php\n/**\n * Plugin Name: Client Plugin\n * Version: 3.2.1-beta.2\n */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(plugin) error = %v", err)
	}
	zipPath := filepath.Join(t.TempDir(), "client-plugin.zip")
	if _, err := packagePluginSource(sourceDir, zipPath, "client-plugin"); err != nil {
		t.Fatalf("packagePluginSource() error = %v", err)
	}
	version, err := pluginZipVersion(zipPath, "client-plugin")
	if err != nil {
		t.Fatalf("pluginZipVersion() error = %v", err)
	}
	if version != "3.2.1-beta.2" {
		t.Fatalf("pluginZipVersion() = %q, want 3.2.1-beta.2", version)
	}

	if err := os.WriteFile(pluginFile, []byte("<?php\n/* Plugin Name: Client Plugin */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(unversioned plugin) error = %v", err)
	}
	if _, err := packagePluginSource(sourceDir, zipPath, "client-plugin"); err != nil {
		t.Fatalf("packagePluginSource(unversioned) error = %v", err)
	}
	if _, err := pluginZipVersion(zipPath, "client-plugin"); err == nil || !strings.Contains(err.Error(), "does not declare a Version header") {
		t.Fatalf("pluginZipVersion(unversioned) error = %v, want missing Version header", err)
	}
}

func TestEnvSnapshotScriptsExcludeRepoPluginMounts(t *testing.T) {
	cfg := envConfig{RepoPluginMounts: []envPluginMount{{Slug: "client-plugin", Host: "/repo/plugins/client-plugin"}}}
	createScript := envSnapshotCreateScript(cfg, "snapshot")
	if !strings.Contains(createScript, "--exclude=wp-content/plugins/client-plugin") {
		t.Fatalf("snapshot create script missing repo plugin exclude:\n%s", createScript)
	}
	if !strings.Contains(createScript, "--exclude=wp-content/mu-plugins") {
		t.Fatalf("snapshot create script missing mu-plugin exclude:\n%s", createScript)
	}
	if strings.Contains(createScript, "for dir in wp-content/uploads wp-content/plugins wp-content/mu-plugins") {
		t.Fatalf("snapshot create script should not archive mu-plugins as mutable content:\n%s", createScript)
	}
	restoreScript := envSnapshotRestoreScript(cfg, "snapshot")
	for _, want := range []string{"clear_dir_contents /var/www/html/wp-content/uploads", "find \"$dir\" -mindepth 1 -maxdepth 1 -exec rm -rf {} +", "repo_plugins=client-plugin", "case \" $repo_plugins \" in *\" $base \"*) continue", "tar --exclude=wp-content/plugins/client-plugin", "--exclude=wp-content/mu-plugins", "-C \"$extract_dir\"", "copy_dir_contents \"$extract_dir/wp-content/plugins\" /var/www/html/wp-content/plugins", "--exclude=wp-content/plugins/client-plugin"} {
		if !strings.Contains(restoreScript, want) {
			t.Fatalf("snapshot restore script missing %q:\n%s", want, restoreScript)
		}
	}
	for _, unwanted := range []string{"rm -rf /var/www/html/wp-content/uploads", "rm -rf /var/www/html/wp-content/uploads /var/www/html/wp-content/plugins", "clear_dir_contents /var/www/html/wp-content/mu-plugins", "copy_dir_contents \"$extract_dir/wp-content/mu-plugins\""} {
		if strings.Contains(restoreScript, unwanted) {
			t.Fatalf("snapshot restore script should not contain %q:\n%s", unwanted, restoreScript)
		}
	}
	if strings.Contains(restoreScript, "tar --no-overwrite-dir") {
		t.Fatalf("snapshot restore should extract into temp dir instead of relying on tar directory overwrite behavior:\n%s", restoreScript)
	}
}

func TestRunEnvPluginsInstallSkipsSatisfiedPlugins(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{"stream"}},
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
		if got := Run([]string{"plugin", "install"}); got != 0 {
			t.Fatalf("Run(plugin install) = %d, want 0", got)
		}
	})
	for _, want := range []string{
		"> docker compose exec --user nonfiction wordpress '<wp plugin bootstrap script>'",
		"WordPress plugins installed.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("plugin install output missing %q:\n%s", want, output)
		}
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(log) error = %v", err)
	}
	logText := string(logData)
	for _, want := range []string{"wp plugin is-installed stream", "wp plugin is-active stream", "wp plugin auto-updates status stream --enabled-only --field=name", "wp plugin auto-updates enable stream"} {
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
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{"stream", map[string]any{"slug": "acf-pro", "source": "private/acf-pro.zip"}}},
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
		if got := Run([]string{"plugin", "status"}); got != 0 {
			t.Fatalf("Run(plugin status) = %d, want 0", got)
		}
	})
	for _, want := range []string{"plugin", "source", "install", "installed", "active", "auto-update", "note", "stream", "wordpress.org", "yes", "acf-pro", "private/acf-pro.zip", "no"} {
		if !strings.Contains(output, want) {
			logData, _ := os.ReadFile(logPath)
			t.Logf("docker log:\n%s", logData)
			t.Fatalf("plugin status output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "> docker") {
		t.Fatalf("plugin status printed command previews unexpectedly:\n%s", output)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(docker log) error = %v", err)
	}
	if calls := strings.Count(strings.TrimSpace(string(logData)), "\n") + 1; calls != 1 {
		t.Fatalf("plugin status made %d docker calls, want 1:\n%s", calls, logData)
	}
}

func TestRunEnvPluginsDiffShowsLocalDrift(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version": 2,
		"project": map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{
			"stream",
			"wp-crontrol",
			map[string]any{"slug": "acf-pro", "source": "private/acf-pro.zip"},
			map[string]any{"slug": "sitepress-multilingual-cms", "install": false, "note": "Install manually from wpml.org account"},
		}},
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "nf.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	dockerDir := t.TempDir()
	dockerScript := []byte("#!/bin/sh\ncase \"$*\" in\n  *core*is-installed*) printf 'stream\\tyes\\tyes\\tyes\\nwp-crontrol\\tyes\\tyes\\tno\\nacf-pro\\tno\\tno\\tno\\nsitepress-multilingual-cms\\tno\\tno\\tno\\nakismet\\tyes\\tno\\tno\\textra\\nimsanity\\tyes\\tyes\\tyes\\textra\\n'; exit 0 ;;\nesac\nexit 1\n")
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
		got = Run([]string{"plugin", "diff"})
	})
	if got != 2 {
		t.Fatalf("Run(plugin diff) = %d, want 2", got)
	}
	for _, want := range []string{"Plugin diff:", "plugin", "change", "stream", "ok", "wp-crontrol", "enable auto-update", "acf-pro", "source unavailable locally", "sitepress-multilingual-cms", "manual install required", "akismet", "extra (inactive, auto-update off)", "imsanity", "extra (active, auto-update on)"} {
		if !strings.Contains(output, want) {
			t.Fatalf("plugin diff output missing %q:\n%s", want, output)
		}
	}
}

func TestRunEnvPluginsDiffReturnsZeroWhenLocalSatisfied(t *testing.T) {
	statuses := []wordpressPluginStatus{{Plugin: wordpressPluginSpec{Slug: "stream", Source: "wordpress.org", Install: true, Activate: true, AutoUpdate: true}, Installed: true, Active: true, AutoUpdate: true}}
	output := captureStdout(t, func() {
		if got := printWordPressPluginDiff("Plugin diff:", nil, statuses, ""); got != 0 {
			t.Fatalf("printWordPressPluginDiff() = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "ok") {
		t.Fatalf("plugin diff output missing ok:\n%s", output)
	}
}

func TestRunEnvPluginsDiffReportsMissingRepoSource(t *testing.T) {
	repoRoot := t.TempDir()
	statuses := []wordpressPluginStatus{{Plugin: wordpressPluginSpec{Slug: "client-plugin", Source: "repo", Install: true, Activate: true, AutoUpdate: true}, Installed: false, Active: false, AutoUpdate: false}}
	output := captureStdout(t, func() {
		if got := printWordPressPluginDiff("Plugin diff:", nil, statuses, repoRoot); got != 2 {
			t.Fatalf("printWordPressPluginDiff() = %d, want 2", got)
		}
	})
	for _, want := range []string{"client-plugin", "source unavailable locally"} {
		if !strings.Contains(output, want) {
			t.Fatalf("plugin diff output missing %q:\n%s", want, output)
		}
	}
}

func TestWordPressPluginDiffIgnoresMustUseAndDropInExtras(t *testing.T) {
	plugins := []wordpressPluginSpec{{Slug: "stream", Source: "wordpress.org", Install: true, Activate: true, AutoUpdate: true}}
	fakeBin := t.TempDir()
	wpScript := `#!/bin/sh
case "$*" in
  "core is-installed") exit 0 ;;
  "plugin is-installed stream") exit 0 ;;
  "plugin is-active stream") exit 0 ;;
  "plugin auto-updates status stream --enabled-only --field=name") printf 'stream\n'; exit 0 ;;
  "plugin list --fields=name,status --format=csv") printf 'name,status\nstream,active\nakismet,inactive\nhello,inactive\nnf-mailpit,must-use\ndb.php,dropin\n'; exit 0 ;;
  "plugin is-active akismet") exit 1 ;;
  "plugin is-active hello") exit 1 ;;
  "plugin auto-updates status akismet --enabled-only --field=name") exit 0 ;;
  "plugin auto-updates status hello --enabled-only --field=name") exit 0 ;;
esac
printf 'unexpected wp args: %s\n' "$*" >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(fakeBin, "wp"), []byte(wpScript), 0o755); err != nil {
		t.Fatalf("WriteFile(wp) error = %v", err)
	}
	cmd := exec.Command("sh", "-lc", localPluginStatusScript(plugins))
	cmd.Env = append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("localPluginStatusScript failed: %v\n%s", err, output)
	}
	statuses := parseWordPressPluginDiffStatusOutput(plugins, string(output))
	diffOutput := captureStdout(t, func() {
		if got := printWordPressPluginDiff("Plugin diff:", nil, statuses, ""); got != 2 {
			t.Fatalf("printWordPressPluginDiff() = %d, want 2", got)
		}
	})
	for _, want := range []string{"akismet", "hello", "extra (inactive, auto-update off)"} {
		if !strings.Contains(diffOutput, want) {
			t.Fatalf("plugin diff output missing %q:\n%s", want, diffOutput)
		}
	}
	for _, notWant := range []string{"nf-mailpit", "db.php"} {
		if strings.Contains(diffOutput, notWant) {
			t.Fatalf("plugin diff output should ignore %q:\n%s", notWant, diffOutput)
		}
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
	repoPluginDir := filepath.Join(repoRoot, "plugins", "client-plugin")
	writeRepoPluginTestPayload(t, repoPluginDir)
	repoFingerprint, err := pluginSourceFingerprint(repoPluginDir)
	if err != nil {
		t.Fatalf("pluginSourceFingerprint() error = %v", err)
	}
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{"stream", map[string]any{"slug": "client-plugin", "source": "repo"}, map[string]any{"slug": "acf-pro", "source": "private/acf-pro.zip"}}}, "remotes": map[string]any{"production": "client-kinsta:live"}}
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
		return []byte("stream\tyes\tyes\tyes\nclient-plugin\tyes\tyes\tyes\trepo:" + repoFingerprint + "\nacf-pro\tno\tno\tno\n"), nil
	}
	t.Cleanup(func() { runSSHOutputFn = oldRunSSHOutput })

	output := captureStdout(t, func() {
		if got := Run([]string{"plugin", "status", "production"}); got != 0 {
			t.Fatalf("Run(plugin status remote) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Plugin status:", "remote:   production", "site:     client-kinsta", "env:      live", "provider: kinsta", "stream", "wordpress.org", "yes", "client-plugin", "repo", "current", "acf-pro", "private/acf-pro.zip", "no"} {
		if !strings.Contains(output, want) {
			t.Fatalf("plugin remote status output missing %q:\n%s", want, output)
		}
	}
	if len(sshArgs) != 5 || sshArgs[0] != "ssh" || sshArgs[3] != "client@203.0.113.10" {
		t.Fatalf("ssh args = %#v", sshArgs)
	}
	script := sshArgs[len(sshArgs)-1]
	for _, want := range []string{"wp_cmd plugin is-installed stream", "wp_cmd plugin is-active stream", "wp_cmd plugin auto-updates status stream --enabled-only --field=name", "printf '%s\\t%s\\t%s\\t%s\\n' stream", "wp_cmd eval", "WP_PLUGIN_DIR", "repo:%s"} {
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
	writeRepoPluginTestPayload(t, filepath.Join(repoRoot, "plugins", "client-plugin"))
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{"stream", map[string]any{"slug": "client-plugin", "source": "repo"}, "wp-crontrol"}}, "remotes": map[string]any{"production": "client-kinsta:live"}}
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
		return []byte("stream\tyes\tyes\tyes\nclient-plugin\tyes\tno\tno\trepo:" + strings.Repeat("0", 64) + "\nwp-crontrol\tno\tno\tno\nimsanity\tyes\tyes\tyes\textra\n"), nil
	}
	t.Cleanup(func() { runSSHOutputFn = oldRunSSHOutput })

	var got int
	output := captureStdout(t, func() {
		got = Run([]string{"plugin", "diff", "production"})
	})
	if got != 2 {
		t.Fatalf("Run(plugin diff remote) = %d, want 2", got)
	}
	for _, want := range []string{"Plugin diff:", "remote:   production", "site:     client-kinsta", "env:      live", "provider: kinsta", "stream", "ok", "client-plugin", "refresh repo source, activate, enable auto-update", "wp-crontrol", "install, activate, enable auto-update", "imsanity", "extra (active, auto-update on)"} {
		if !strings.Contains(output, want) {
			t.Fatalf("plugin remote diff output missing %q:\n%s", want, output)
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
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{"stream", map[string]any{"slug": "query-monitor", "activate": false, "auto_update": false}, map[string]any{"slug": "acf-pro", "source": "private/acf-pro.zip"}}}, "remotes": map[string]any{"production": "client-kinsta:live"}}
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
		if got := Run([]string{"plugin", "install", "production", "--dry-run"}); got != 0 {
			t.Fatalf("Run(plugin install remote --dry-run) = %d, want 0", got)
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
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{map[string]any{"slug": "acf-pro", "source": "private/acf-pro.zip"}}}, "remotes": map[string]any{"production": "client-kinsta:live"}}
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
		if got := Run([]string{"plugin", "install", "production", "--yes"}); got != 0 {
			t.Fatalf("Run(plugin install remote local zip --yes) = %d, want 0", got)
		}
	})
	for _, want := range []string{"uploads:       1 local plugin zip(s)", "Local plugin sources will be uploaded before install:", "acf-pro -> /tmp/nf-plugins-client-kinsta-live-", "Remote WordPress plugins installed."} {
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

func TestRunEnvPluginsInstallRemotePackagesRepoPluginSource(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("NF_STATE_HOME", stateDir)
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	pluginDir := filepath.Join(repoRoot, "plugins", "client-plugin")
	if err := os.MkdirAll(filepath.Join(pluginDir, "includes"), 0o755); err != nil {
		t.Fatalf("MkdirAll(plugin) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "client-plugin.php"), []byte("<?php\n/* Plugin Name: Client Plugin */\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(plugin main) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "includes", "feature.php"), []byte("<?php\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(plugin include) error = %v", err)
	}
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{map[string]any{"slug": "client-plugin", "source": "repo"}}}, "remotes": map[string]any{"production": "client-kinsta:live"}}
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
	var uploadedZip string
	runRsyncCommandFn = func(args []string) error {
		rsyncCommands = append(rsyncCommands, append([]string(nil), args...))
		uploadedZip = args[4]
		names := readZipNames(t, uploadedZip)
		for _, want := range []string{"client-plugin/client-plugin.php", "client-plugin/includes/feature.php"} {
			if !names[want] {
				t.Fatalf("repo plugin zip missing %q: %#v", want, names)
			}
		}
		return nil
	}
	t.Cleanup(func() { runRsyncCommandFn = oldRunRsync })

	stdout := captureStdout(t, func() {
		if got := Run([]string{"plugin", "install", "production", "--yes"}); got != 0 {
			t.Fatalf("Run(plugin install remote repo --yes) = %d, want 0", got)
		}
	})
	for _, want := range []string{"uploads:       1 local plugin zip(s)", "client-plugin -> /tmp/nf-plugins-client-kinsta-live-", "Remote WordPress plugins installed."} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("remote repo plugin stdout missing %q:\n%s", want, stdout)
		}
	}
	if len(rsyncCommands) != 1 {
		t.Fatalf("rsync commands len = %d, want 1: %#v", len(rsyncCommands), rsyncCommands)
	}
	if !strings.HasSuffix(rsyncCommands[0][4], "client-plugin.zip") {
		t.Fatalf("rsync source = %q, want generated client-plugin.zip", rsyncCommands[0][4])
	}
	if !strings.Contains(rsyncCommands[0][5], "client@203.0.113.10:/tmp/nf-plugins-client-kinsta-live-") || !strings.HasSuffix(rsyncCommands[0][5], "/client-plugin.zip") {
		t.Fatalf("rsync destination = %q", rsyncCommands[0][5])
	}
	if len(sshCommands) != 3 {
		t.Fatalf("ssh commands len = %d, want mkdir/install/cleanup: %#v", len(sshCommands), sshCommands)
	}
	script := sshCommands[1][len(sshCommands[1])-1]
	for _, want := range []string{"wp_cmd plugin install /tmp/nf-plugins-client-kinsta-live-", "/client-plugin.zip --force --activate", "wp_cmd plugin auto-updates enable client-plugin"} {
		if !strings.Contains(script, want) {
			t.Fatalf("remote repo plugin script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "wp_cmd plugin is-installed client-plugin") {
		t.Fatalf("remote repo plugin should refresh even when already installed:\n%s", script)
	}
	if uploadedZip == "" {
		t.Fatal("runRsyncCommandFn did not capture generated zip path")
	}
	if _, err := os.Stat(uploadedZip); !os.IsNotExist(err) {
		t.Fatalf("temporary repo plugin zip was not cleaned up: %v", err)
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
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{map[string]any{"slug": "stream", "source": "$NF_PLUGIN_STREAM_ZIP"}}}, "remotes": map[string]any{"production": "client.app1-linode:live"}}
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
		if got := Run([]string{"plugin", "install", "production", "--yes"}); got != 0 {
			t.Fatalf("Run(plugin install remote --yes) = %d, want 0", got)
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
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}, "plugins": []any{"stream"}}, "remotes": map[string]any{"production": "client-kinsta:live"}}
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
		if got := Run([]string{"plugin", "install", "production"}); got != 1 {
			t.Fatalf("Run(plugin install remote denied) = %d, want 1", got)
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
	cfg := envConfig{ProjectSlug: "client", EnvDir: filepath.Join("/data", "envs", "client"), WordpressPort: 18432, MailpitPort: 18433, AdminerPort: 18434, DBUser: "client", DBPassword: "db-pass", AdminUser: "admin", AdminPassword: "wp-pass"}
	want := "client:local\n────────────\nSite      client\nEnv       local\nPath      /data/envs/client\nPHP       8.3\nCompose   nf_client_env\n\nDatabase\n  DB URL        http://localhost:18434/?mysql=db&username=client&db=client\n  DB user       client\n  DB pass       db-pass\n\nEmail\n  Mailpit URL   http://localhost:18433\n\nWordPress\n  Site URL      http://localhost:18432\n  Admin URL     http://localhost:18432/wp-login.php\n  WP user       admin\n  WP pass       wp-pass"
	if got := renderEnvInfo(cfg, true); got != want {
		t.Fatalf("renderEnvInfo(full) = %q, want %q", got, want)
	}
	want = "client:local\n────────────\nSite      client\nEnv       local\nPath      /data/envs/client\nPHP       8.3\nCompose   nf_client_env"
	if got := renderEnvInfo(cfg, false); got != want {
		t.Fatalf("renderEnvInfo(short) = %q, want %q", got, want)
	}
	want = "client:local\n────────────\nSite      client\nEnv       local\nPath      /data/envs/client\nPHP       8.3\nCompose   nf_client_env\n\nDatabase\n  DB URL        http://localhost:18434/?mysql=db&username=client&db=client\n  DB user       client\n  DB pass       db-pass\n\nEmail\n  Mailpit URL   http://localhost:18433\n\nWordPress\n  Site URL      http://localhost:18432\n  Admin URL     http://localhost:18432/wp-login.php\n  WP user       admin\n  WP pass       wp-pass\n\nRemotes\n  Live URL      https://client.example.com\n  Staging URL   https://staging.client.example.com"
	remoteRows := []detailRow{{label: "Live URL", value: "https://client.example.com"}, {label: "Staging URL", value: "https://staging.client.example.com"}}
	if got := renderEnvInfo(cfg, true, remoteRows...); got != want {
		t.Fatalf("renderEnvInfo(full with remotes) = %q, want %q", got, want)
	}
}

func TestLocalEnvDBURLPrefillsConnectionWithoutPassword(t *testing.T) {
	cfg := envConfig{ProjectSlug: "client-site", AdminerPort: 18434, DBUser: "client user"}
	want := "http://localhost:18434/?mysql=db&username=client+user&db=client-site"
	if got := localEnvDBURL(cfg); got != want {
		t.Fatalf("localEnvDBURL() = %q, want %q", got, want)
	}
}

func TestLoadEnvConfigAppliesPortOverrides(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	metadata := &project.Manifest{
		Project:   project.Project{Slug: "client"},
		WordPress: project.WordPress{Themes: []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}},
		Local: &project.Local{
			Compose:          "docker compose",
			WordPressService: "wordpress",
			UploadsPath:      "uploads",
			Ports:            &project.Ports{WordPress: 19111, Mailpit: 19112},
		},
	}
	cfg, ok := loadEnvConfig(root, metadata)
	if !ok {
		t.Fatalf("loadEnvConfig() = false, want true")
	}
	if cfg.WordpressPort != 19111 || cfg.MailpitPort != 19112 || cfg.AdminerPort == 0 {
		t.Fatalf("effective ports = (%d, %d, %d), want overrides (19111, 19112, derived)", cfg.WordpressPort, cfg.MailpitPort, cfg.AdminerPort)
	}
}

func TestLoadEnvConfigFallsBackPerPortIndependently(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	derivedWordpress, derivedMailpit, _ := envDerivedPorts("client")
	for _, tc := range []struct {
		name          string
		ports         project.Ports
		wantWordpress int
		wantMailpit   int
	}{
		{name: "wordpress override only", ports: project.Ports{WordPress: 19111}, wantWordpress: 19111, wantMailpit: derivedMailpit},
		{name: "mailpit override only", ports: project.Ports{Mailpit: 19112}, wantWordpress: derivedWordpress, wantMailpit: 19112},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata := &project.Manifest{
				Project:   project.Project{Slug: "client"},
				WordPress: project.WordPress{Themes: []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}},
				Local: &project.Local{
					Compose:          "docker compose",
					WordPressService: "wordpress",
					UploadsPath:      "uploads",
					Ports:            &tc.ports,
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
	wpPort, mailpitPort, adminerPort := envDerivedPorts("client")
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", wpPort))
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	cfg := envConfig{ProjectSlug: "client", EnvDir: filepath.Join("/data", "envs", "client"), WordpressPort: wpPort, MailpitPort: mailpitPort, AdminerPort: adminerPort}
	err = preflightEnvPorts(cfg)
	if err == nil {
		t.Fatal("preflightEnvPorts() error = nil, want collision")
	}
	message := err.Error()
	for _, want := range []string{fmt.Sprintf("Port %d is already in use.", wpPort), fmt.Sprintf("WordPress: http://localhost:%d", wpPort), fmt.Sprintf("Mailpit:   http://localhost:%d", mailpitPort), fmt.Sprintf("Database:  http://localhost:%d", adminerPort)} {
		if !strings.Contains(message, want) {
			t.Fatalf("preflightEnvPorts() error = %q, want %q", message, want)
		}
	}
}

func TestPreflightEnvPortsAllowsExistingManagedEnv(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_DATA_HOME", configHome)
	wpPort, mailpitPort, adminerPort := envDerivedPorts("client")
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", wpPort))
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	cfg := envConfig{ProjectSlug: "client", EnvDir: config.EnvDir("client"), WordpressPort: wpPort, MailpitPort: mailpitPort, AdminerPort: adminerPort}
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

	cfg := envConfig{ProjectSlug: "client", EnvDir: filepath.Join("/data", "envs", "client"), WordpressPort: wpPort, MailpitPort: wpPort + 1, AdminerPort: wpPort + 2}
	err := preflightEnvPorts(cfg)
	if err == nil {
		t.Fatal("preflightEnvPorts() error = nil, want collision")
	}
	message := err.Error()
	for _, want := range []string{fmt.Sprintf("Ports %d and %d are already in use.", wpPort, wpPort+1), fmt.Sprintf("WordPress: http://localhost:%d", wpPort), fmt.Sprintf("Mailpit:   http://localhost:%d", wpPort+1), fmt.Sprintf("Database:  http://localhost:%d", wpPort+2)} {
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
		ThemeMountSlug:   "theme",
		UploadsPath:      "uploads",
		ThemeSlug:        "client",
		Themes:           []wordpressThemeSpec{{Slug: "client", Source: wordpressThemeRepoSource, Path: "theme"}},
	}

	if got, want := envComposeArgs(cfg, "up", "-d"), []string{"docker", "compose", "up", "-d"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("envComposeArgs() = %#v, want %#v", got, want)
	}
	if got, want := envWpArgs(cfg, "plugin", "list"), []string{"docker", "compose", "exec", "--user", "nonfiction", "wordpress", "wp", "plugin", "list"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("envWpArgs() = %#v, want %#v", got, want)
	}
	if got, want := envWpRewriteFlushArgs(cfg), []string{"docker", "compose", "exec", "--user", "nonfiction", "wordpress", "wp", "rewrite", "flush"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("envWpRewriteFlushArgs() = %#v, want %#v", got, want)
	}
	remoteTarget := envRemoteSyncTarget{SSHUser: "client", SSHHost: "203.0.113.10", SSHPort: "12345", WordPressPath: "/www/client/public", WPCommand: "wp"}
	if got, want := remoteWPSSHArgs(remoteTarget, wpRewriteFlushArgs()...), []string{"ssh", "-p", "12345", "client@203.0.113.10", "wp --path=/www/client/public rewrite flush"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("remoteWPSSHArgs(rewrite flush) = %#v, want %#v", got, want)
	}
	if strings.Contains(strings.Join(envWpRewriteFlushArgs(cfg), " ")+" "+strings.Join(remoteWPSSHArgs(remoteTarget, wpRewriteFlushArgs()...), " "), "--hard") {
		t.Fatal("rewrite flush helpers unexpectedly use --hard")
	}
	if got, want := envShellArgs(cfg), []string{"docker", "compose", "exec", "--user", "nonfiction", "wordpress", "bash"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("envShellArgs() = %#v, want %#v", got, want)
	}
	if got, want := envWpThemeIsActiveArgs(cfg, ""), []string{"docker", "compose", "exec", "--user", "nonfiction", "wordpress", "wp", "theme", "is-active", "client"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("envWpThemeIsActiveArgs() = %#v, want %#v", got, want)
	}
	hostPath, containerPath := envThemeArchivePaths(cfg, "/tmp/theme.zip")
	if hostPath != filepath.Join(cfg.EnvDir, ".nf-transfer", "theme.zip") || containerPath != "/env/uploads/theme.zip" {
		t.Fatalf("envThemeArchivePaths() = (%q, %q), want host and container upload paths", hostPath, containerPath)
	}
	if got, want := envCommandDir(cfg), cfg.EnvDir; got != want {
		t.Fatalf("envCommandDir() = %q, want %q", got, want)
	}
	if got, want := envWpThemeActivateArgs(cfg, ""), []string{"docker", "compose", "exec", "--user", "nonfiction", "wordpress", "wp", "theme", "activate", "client"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("envWpThemeActivateArgs() = %#v, want %#v", got, want)
	}
	if got, want := envWpThemeActivateArgs(cfg, "custom-slug"), []string{"docker", "compose", "exec", "--user", "nonfiction", "wordpress", "wp", "theme", "activate", "custom-slug"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("envWpThemeActivateArgs(explicit) = %#v, want %#v", got, want)
	}
	readyArgs := envWpBootstrapReadyArgs(cfg)
	readyJoined := strings.Join(readyArgs, " ")
	for _, wanted := range []string{"docker compose exec --user nonfiction wordpress sh -lc", "wp-config.php", "wp-settings.php", "WordPress files are not ready yet."} {
		if !strings.Contains(readyJoined, wanted) {
			t.Fatalf("envWpBootstrapReadyArgs() missing %q in %#v", wanted, readyArgs)
		}
	}
	if got, want := envWpBootstrapPreviewArgs(cfg, "wait for WordPress files"), []string{"docker", "compose", "exec", "--user", "nonfiction", "wordpress", "<wait for WordPress files>"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("envWpBootstrapPreviewArgs() = %#v, want %#v", got, want)
	}
	installArgs := envWpCoreInstallArgs(cfg)
	joined := strings.Join(installArgs, " ")
	for _, wanted := range []string{"docker compose exec --user nonfiction wordpress sh -lc", "WP_URL", "WP_TITLE", "ADMIN_USER", "ADMIN_PASSWORD", "ADMIN_EMAIL", "wp core install"} {
		if !strings.Contains(joined, wanted) {
			t.Fatalf("envWpCoreInstallArgs() missing %q in %#v", wanted, installArgs)
		}
	}
	if strings.Contains(joined, "wp theme activate") {
		t.Fatalf("envWpCoreInstallArgs() unexpectedly activates a theme: %#v", installArgs)
	}
	if strings.Contains(joined, "wp core is-installed") {
		t.Fatalf("envWpCoreInstallArgs() unexpectedly probes install state: %#v", installArgs)
	}
	mailpitArgs := envWpMailpitSMTPArgs(cfg)
	mailpitJoined := strings.Join(mailpitArgs, " ")
	for _, wanted := range []string{"docker compose exec --user nonfiction wordpress sh -lc", "wp-content/mu-plugins/nf-mailpit.php", "phpmailer_init", "mailpit", "1025"} {
		if !strings.Contains(mailpitJoined, wanted) {
			t.Fatalf("envWpMailpitSMTPArgs() missing %q in %#v", wanted, mailpitArgs)
		}
	}
	if got, want := envRepoPath("/repo", "dist/theme.zip"), filepath.Join("/repo", "dist", "theme.zip"); got != want {
		t.Fatalf("envRepoPath() = %q, want %q", got, want)
	}
	if got, want := envRepoPath("/repo", "/tmp/theme.zip"), "/tmp/theme.zip"; got != want {
		t.Fatalf("envRepoPath() = %q, want %q", got, want)
	}
	if got, want := (envCommandRunner{name: "up", cfg: cfg}).Render(), "docker compose up -d; configure Mailpit SMTP; install WordPress if missing and ensure configured themes are installed and active"; got != want {
		t.Fatalf("up Render() = %q, want %q", got, want)
	}
	if got, want := (envCommandRunner{name: "up", cfg: cfg, rebuild: true}).Render(), "docker compose build; docker compose up -d; configure Mailpit SMTP; install WordPress if missing and ensure configured themes are installed and active"; got != want {
		t.Fatalf("up --rebuild Render() = %q, want %q", got, want)
	}
	if got, want := (envCommandRunner{name: "reset", cfg: cfg}).Render(), "docker compose down -v --remove-orphans; nuke env data and recreate it with docker compose up -d, configure Mailpit SMTP, install WordPress if missing, and ensure configured themes are installed and active"; got != want {
		t.Fatalf("reset Render() = %q, want %q", got, want)
	}
	if got, want := (envCommandRunner{name: "reset", cfg: cfg, rebuild: true}).Render(), "docker compose down -v --remove-orphans; nuke env data and recreate it with docker compose build, docker compose up -d, configure Mailpit SMTP, install WordPress if missing, and ensure configured themes are installed and active"; got != want {
		t.Fatalf("reset --rebuild Render() = %q, want %q", got, want)
	}
	if got, want := (envCommandRunner{name: "shell", cfg: cfg}).Render(), "docker compose exec --user nonfiction wordpress bash"; got != want {
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
	metadata := &project.Manifest{
		Project:   project.Project{Slug: "client"},
		WordPress: project.WordPress{Themes: []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}},
		Local:     &project.Local{Compose: "docker compose", WordPressService: "wordpress", UploadsPath: "uploads"},
	}
	cfg, ok := loadEnvConfig(root, metadata)
	if !ok {
		t.Fatalf("loadEnvConfig() = false, want true")
	}
	if got, want := cfg.EnvDir, config.EnvDir("client"); got != want {
		t.Fatalf("EnvDir = %q, want %q", got, want)
	}
	wpPort, mailpitPort, adminerPort := envDerivedPorts("client")
	credentialCfg, err := envConfigWithAdminCredentials(cfg)
	if err != nil {
		t.Fatalf("envConfigWithAdminCredentials() error = %v", err)
	}
	if err := ensureManagedEnv(credentialCfg); err != nil {
		t.Fatalf("ensureManagedInstance() error = %v", err)
	}
	adminPassword := passwords.DerivePassword("client", "wp-admin", "test-salt")
	dbPassword := passwords.DerivePassword("client", "mysql", "test-salt")
	checks := map[string][]string{
		filepath.Join(cfg.EnvDir, "docker-compose.yml"):                   {filepath.Join(root, "theme") + ":/var/www/html/wp-content/themes/theme", "mailpit", "db-ui:", "wordpress:php8.3-apache", "https://www.adminneo.org/files/5.4.1/mysql_en_default/adminneo-5.4.1.php", "${DB_UI_PORT}:80", "HOME: /home/nonfiction", "WP_CLI_CACHE_DIR: /tmp/wp-cli-cache", "./php/uploads.ini:/usr/local/etc/php/conf.d/uploads.ini:ro", "./uploads:/var/www/html/wp-content/uploads", "./.nf-transfer:/env/uploads", ":/env-snapshots"},
		filepath.Join(cfg.EnvDir, ".env"):                                 {"COMPOSE_PROJECT_NAME=nf_client_env", fmt.Sprintf("WP_PORT=%d", wpPort), fmt.Sprintf("MAILPIT_PORT=%d", mailpitPort), fmt.Sprintf("DB_UI_PORT=%d", adminerPort), fmt.Sprintf("WP_URL=http://localhost:%d", wpPort), "DB_USER=client", "DB_PASSWORD=" + dbPassword, "WP_TITLE=Client", "ADMIN_USER=admin", "ADMIN_PASSWORD=" + adminPassword, "ADMIN_EMAIL=web@nonfiction.ca"},
		filepath.Join(cfg.EnvDir, "php", "uploads.ini"):                   {"upload_max_filesize=1024M", "post_max_size=1024M", "max_execution_time=120"},
		filepath.Join(cfg.EnvDir, "wordpress", "Dockerfile"):              {"FROM wordpress:php8.3-apache", "apt-get install -y --no-install-recommends", "iputils-ping", "dnsutils", "mariadb-client", "nano", "vim", "wp-cli.phar", "/usr/local/bin/wp", "useradd --create-home --shell /bin/bash --groups www-data nonfiction", "export APACHE_RUN_USER=nonfiction", "export APACHE_RUN_GROUP=www-data", "umask 0002", "chown -R nonfiction:www-data", "COPY wordpress/wordpress-rewrites.conf"},
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
	if data, err := os.ReadFile(filepath.Join(cfg.EnvDir, ".nf-transfer", ".gitkeep")); err != nil {
		t.Fatalf("ReadFile(.nf-transfer/.gitkeep) error = %v", err)
	} else if len(data) != 0 {
		t.Fatalf(".nf-transfer/.gitkeep = %q, want empty file", string(data))
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
		"version":   2,
		"project":   map[string]any{"slug": "client-site", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}},
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
		if got := Run([]string{"env", "up", "--rebuild"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, want := range []string{
		"> docker compose build",
		"> docker compose up -d",
		"> docker compose exec --user nonfiction wordpress '<wait for WordPress files>'",
		"> docker compose exec --user nonfiction wordpress '<configure Mailpit SMTP>'",
		"> docker compose exec --user nonfiction wordpress wp core is-installed",
		"> docker compose exec --user nonfiction wordpress sh -lc",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output = %q, want %q", output, want)
		}
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("docker log missing: %v", err)
	}
	linkPath := filepath.Join(repoRoot, "uploads")
	linkTarget, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("Readlink(uploads) error = %v", err)
	}
	if want := filepath.Join(config.EnvDir("client-site"), "uploads"); linkTarget != want {
		t.Fatalf("uploads symlink = %q, want %q", linkTarget, want)
	}
}

func TestRunEnvDownRemovesManagedUploadsSymlink(t *testing.T) {
	repoRoot := filepath.Join(t.TempDir(), "client-site")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll(theme) error = %v", err)
	}
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client-site", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}},
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
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), []byte("#!/bin/sh\nprintf '%s\n' \"$@\" >> \"$DOCKER_LOG\"\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(docker) error = %v", err)
	}
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	t.Setenv("DOCKER_LOG", logPath)
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	target := filepath.Join(config.EnvDir("client-site"), "uploads")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("MkdirAll(target) error = %v", err)
	}
	linkPath := filepath.Join(repoRoot, "uploads")
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	if got := Run([]string{"env", "down"}); got != 0 {
		t.Fatalf("Run() = %d, want 0", got)
	}
	if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
		t.Fatalf("uploads symlink still exists or stat failed: %v", err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(log) error = %v", err)
	}
	if !strings.Contains(string(logData), "compose\ndown") {
		t.Fatalf("docker log missing compose down:\n%s", string(logData))
	}
}

func TestEnsureProjectUploadsSymlinkRejectsExistingPath(t *testing.T) {
	root := t.TempDir()
	cfg := envConfig{EnvDir: t.TempDir(), UploadsPath: "uploads"}
	if err := os.Mkdir(filepath.Join(root, "uploads"), 0o755); err != nil {
		t.Fatalf("Mkdir(uploads) error = %v", err)
	}
	err := ensureProjectUploadsSymlink(root, cfg)
	if err == nil {
		t.Fatal("ensureProjectUploadsSymlink() error = nil, want conflict")
	}
	if !strings.Contains(err.Error(), "refusing to replace existing project uploads path") {
		t.Fatalf("ensureProjectUploadsSymlink() error = %q, want conflict message", err)
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
		"version":   2,
		"project":   map[string]any{"slug": "client-site", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}},
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
	dockerScript := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$DOCKER_LOG\"\ncase \"$6 $7 $8\" in\n  \"wp theme is-active\") exit 1 ;;\n  \"wp core is-installed\") exit 0 ;;\nesac\nexit 0\n")
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
		"> docker compose exec --user nonfiction wordpress '<wait for WordPress files>'",
		"> docker compose exec --user nonfiction wordpress '<configure Mailpit SMTP>'",
		"> docker compose exec --user nonfiction wordpress wp core is-installed",
		"> docker compose exec --user nonfiction wordpress '<wp theme bootstrap script>'",
		"> docker compose exec --user nonfiction wordpress wp theme is-active theme",
		"> docker compose exec --user nonfiction wordpress wp theme activate theme",
		"> docker compose exec --user nonfiction wordpress wp rewrite flush",
		rewriteRulesRegeneratedMessage,
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
		"version": 2,
		"project": map[string]any{"slug": "client-site", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{
			"slug":   "theme",
			"source": "repo",
			"path":   "theme",
			"tasks": map[string]any{
				"composer": map[string]any{"description": "Install Composer dependencies", "run": "mkdir -p theme/vendor && touch theme/vendor/autoload.php"},
				"npm":      map[string]any{"description": "Install npm dependencies", "run": "mkdir -p theme/node_modules && touch theme/node_modules/.installed"},
				"build":    map[string]any{"description": "Build theme assets", "run": "mkdir -p theme/dist && touch theme/dist/manifest.json"},
			},
		}}},
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
		"version":   2,
		"project":   map[string]any{"slug": "client-site", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}},
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
	stubLocalWordPressTransferEstimate(t, 1024)

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() {
		if got := Run([]string{"env", "reset", "--rebuild"}); got != 0 {
			t.Fatalf("Run() = %d, want 0", got)
		}
	})
	for _, want := range []string{
		"Safety snapshot:",
		"> docker compose down -v --remove-orphans",
		"> docker compose build",
		"> docker compose up -d",
		"> docker compose exec --user nonfiction wordpress '<wait for WordPress files>'",
		"> docker compose exec --user nonfiction wordpress '<configure Mailpit SMTP>'",
		"> docker compose exec --user nonfiction wordpress wp core is-installed",
		"> docker compose exec --user nonfiction wordpress sh -lc",
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
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme", "package": map[string]any{"output": "release/client-v{version}.zip"}}}},
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
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "client", "source": "repo", "path": "theme", "package": map[string]any{"output": "dist/client-v{version}.zip"}}}},
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

func TestRunThemePackageStagesProductionComposerDependencies(t *testing.T) {
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	for _, dir := range []string{
		filepath.Join(workdir, "theme", "app"),
		filepath.Join(workdir, "theme", "config"),
		filepath.Join(workdir, "theme", "src"),
		filepath.Join(workdir, "theme", "dist"),
		filepath.Join(workdir, "theme", "vendor", "bin"),
		filepath.Join(workdir, "theme", "vendor", "friendsofphp", "php-cs-fixer"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	files := map[string]string{
		filepath.Join(workdir, "theme", "style.css"):                                         "/*\nTheme Name: Demo\nVersion: 2.0.0\n*/\n",
		filepath.Join(workdir, "theme", "screenshot.png"):                                    "png",
		filepath.Join(workdir, "theme", "index.php"):                                         "<?php\n",
		filepath.Join(workdir, "theme", "app", "setup.php"):                                  "<?php\n",
		filepath.Join(workdir, "theme", "config", "theme.php"):                               "<?php\n",
		filepath.Join(workdir, "theme", "src", "Runtime.php"):                                "<?php\n",
		filepath.Join(workdir, "theme", "dist", "manifest.json"):                             "{}\n",
		filepath.Join(workdir, "theme", "composer.json"):                                     `{"require":{"acme/runtime":"1.0.0"},"require-dev":{"friendsofphp/php-cs-fixer":"3.0.0"}}`,
		filepath.Join(workdir, "theme", "composer.lock"):                                     "{}\n",
		filepath.Join(workdir, "theme", "package.json"):                                      `{"scripts":{"build":"vite build"}}`,
		filepath.Join(workdir, "theme", ".php-cs-fixer.php"):                                 "<?php\n",
		filepath.Join(workdir, "theme", "phpcs.xml"):                                         "<ruleset />\n",
		filepath.Join(workdir, "theme", "vite.config.js"):                                    "export default {}\n",
		filepath.Join(workdir, "theme", "vendor", "bin", "phpcs"):                            "#!/bin/sh\n",
		filepath.Join(workdir, "theme", "vendor", "friendsofphp", "php-cs-fixer", "dev.php"): "<?php\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "client", "source": "repo", "path": "theme", "package": map[string]any{"output": "dist/client-v{version}.zip"}}}},
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}

	composerArgsPath := filepath.Join(t.TempDir(), "composer-args.txt")
	binDir := t.TempDir()
	composerScript := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$COMPOSER_ARGS_FILE\"\ncase \" $* \" in *\" --no-dev \"*) ;; *) exit 7 ;; esac\nif [ -d vendor ]; then exit 8; fi\nmkdir -p vendor/acme/runtime\nprintf '<?php\\n' > vendor/autoload.php\nprintf '<?php\\n' > vendor/acme/runtime/runtime.php\n"
	if err := os.WriteFile(filepath.Join(binDir, "composer"), []byte(composerScript), 0o755); err != nil {
		t.Fatalf("WriteFile(composer) error = %v", err)
	}
	t.Setenv("COMPOSER_ARGS_FILE", composerArgsPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

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
	composerArgs, err := os.ReadFile(composerArgsPath)
	if err != nil {
		t.Fatalf("ReadFile(composer args) error = %v", err)
	}
	if !strings.Contains(string(composerArgs), "install\n") || !strings.Contains(string(composerArgs), "--no-dev\n") {
		t.Fatalf("composer args = %q, want install --no-dev", composerArgs)
	}
	if _, err := os.Stat(filepath.Join(workdir, "theme", "vendor", "friendsofphp", "php-cs-fixer", "dev.php")); err != nil {
		t.Fatalf("source vendor was mutated or removed: %v", err)
	}

	names := readZipNames(t, filepath.Join(workdir, "dist", "client-v2.0.0.zip"))
	for _, want := range []string{
		"client/style.css",
		"client/screenshot.png",
		"client/index.php",
		"client/app/setup.php",
		"client/config/theme.php",
		"client/src/Runtime.php",
		"client/dist/manifest.json",
		"client/vendor/autoload.php",
		"client/vendor/acme/runtime/runtime.php",
	} {
		if !names[want] {
			t.Fatalf("zip entries missing %q: %#v", want, names)
		}
	}
	for _, unwanted := range []string{
		"client/vendor/bin/phpcs",
		"client/vendor/friendsofphp/php-cs-fixer/dev.php",
		"client/composer.json",
		"client/composer.lock",
		"client/package.json",
		"client/.php-cs-fixer.php",
		"client/phpcs.xml",
		"client/vite.config.js",
	} {
		if names[unwanted] {
			t.Fatalf("zip entries unexpectedly contained %q: %#v", unwanted, names)
		}
	}
}

func TestRunThemePackageFailsWhenBuildOutputMissing(t *testing.T) {
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, "theme"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "theme", "style.css"), []byte("/*\nTheme Name: Demo\nVersion: 2.0.0\n*/\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(style.css) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "theme", "package.json"), []byte(`{"scripts":{"build":"vite build"}}`), 0o644); err != nil {
		t.Fatalf("WriteFile(package.json) error = %v", err)
	}
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "client", "source": "repo", "path": "theme", "package": map[string]any{"output": "dist/client-v{version}.zip"}}}},
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
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

	output := captureStderr(t, func() {
		if got := Run([]string{"theme", "package", "--dry-run"}); got != 1 {
			t.Fatalf("Run() = %d, want 1", got)
		}
	})
	for _, want := range []string{"theme build output missing", "nf theme build"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() stderr = %q, want %q", output, want)
		}
	}
}

func readZipNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader(%s) error = %v", path, err)
	}
	defer zr.Close()
	names := map[string]bool{}
	for _, file := range zr.File {
		names[file.Name] = true
	}
	return names
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
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme", "package": map[string]any{"output": "dist/client-v{version}.zip"}}}},
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
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme", "package": map[string]any{"output": "dist/client-v{version}.zip"}}}},
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
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{"twentytwentyfive"}}, "local": map[string]any{"admin_user": "cached-owner"}}
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
	oldRunCommandSpecOutputSilent := runCommandSpecOutputSilentFn
	var userLookupArgs []string
	runCommandSpecOutputSilentFn = func(spec execSpec) (string, error) {
		userLookupArgs = append([]string(nil), spec.Args...)
		return "owner\n", nil
	}
	t.Cleanup(func() { runCommandSpecOutputSilentFn = oldRunCommandSpecOutputSilent })

	output := captureStdout(t, func() {
		if got := Run([]string{"env", "show"}); got != 0 {
			t.Fatalf("Run(env show) = %d, want 0", got)
		}
	})
	wpPort, mailpitPort, adminerPort := envDerivedPorts("client")
	adminPassword := passwords.DerivePassword("client", "wp-admin", "test-salt")
	dbPassword := passwords.DerivePassword("client", "mysql", "test-salt")
	assertContainsInOrder(t, output, []string{
		"client:local\n",
		"Site      client\n",
		"Env       local\n",
		"Compose   nf_client_env\n",
		"Database\n",
		fmt.Sprintf("  DB URL        http://localhost:%d/?mysql=db&username=client&db=client\n", adminerPort),
		"  DB user       client\n",
		"  DB pass       " + dbPassword,
		"Email\n",
		fmt.Sprintf("  Mailpit URL   http://localhost:%d", mailpitPort),
		"WordPress\n",
		fmt.Sprintf("  Site URL      http://localhost:%d\n", wpPort),
		fmt.Sprintf("  Admin URL     http://localhost:%d/wp-login.php\n", wpPort),
		"  WP user       owner\n",
		"  WP pass       " + adminPassword,
	})
	if got, want := strings.Join(userLookupArgs, " "), "docker compose exec --user nonfiction wordpress wp user get 1 --field=user_login"; got != want {
		t.Fatalf("env show user lookup args = %q, want %q", got, want)
	}
	if strings.Contains(output, "Remotes\n") {
		t.Fatalf("Run(env show) output = %q, did not expect remotes section without nf.json remotes", output)
	}
}

func TestRunEnvShowPrintsConfiguredRemoteURLs(t *testing.T) {
	configHome := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_DATA_HOME", configHome)
	writeTestWPDefaults(t, "test-salt")
	if err := state.SaveStateRecords("sites", []map[string]any{
		{"provider": "linode", "site_id": "client-app1-linode", "env": "staging", "url": "https://client-staging.app1-linode.nonfiction.dev"},
		{"provider": "linode", "site_id": "client-app1-linode", "env": "live", "url": "https://client.app1-linode.nonfiction.dev"},
	}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{"twentytwentyfive"}},
		"local":     map[string]any{"admin_user": "cached-owner"},
		"remotes": map[string]any{
			"staging": "client-app1-linode:staging",
			"live":    "client-app1-linode:live",
		},
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
	oldRunCommandSpecOutputSilent := runCommandSpecOutputSilentFn
	runCommandSpecOutputSilentFn = func(spec execSpec) (string, error) { return "", fmt.Errorf("env down") }
	t.Cleanup(func() { runCommandSpecOutputSilentFn = oldRunCommandSpecOutputSilent })

	output := captureStdout(t, func() {
		if got := Run([]string{"env", "show"}); got != 0 {
			t.Fatalf("Run(env show) = %d, want 0", got)
		}
	})
	assertContainsInOrder(t, output, []string{
		"WordPress\n",
		"  WP user       cached-owner\n",
		"Remotes\n",
		"  Live URL      https://client.app1-linode.nonfiction.dev\n",
		"  Staging URL   https://client-staging.app1-linode.nonfiction.dev",
	})
}

func TestRunEnvShellRemoteUsesConfiguredRemote(t *testing.T) {
	configHome := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_DATA_HOME", configHome)
	writeTestWPDefaults(t, "test-salt")
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}},
		"remotes":   map[string]any{"production": "client-kinsta:live"},
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	oldRunSSHCommand := runSSHCommandFn
	var sshArgs []string
	runSSHCommandFn = func(args []string) error {
		sshArgs = append([]string(nil), args...)
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSHCommand })
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() {
		if got := Run([]string{"env", "shell", "production"}); got != 0 {
			t.Fatalf("Run(env shell production) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Site shell preflight:", "site:     client-kinsta", "env:      live", "provider: kinsta", "> ssh -t -p 12345 client@203.0.113.10", "cd /www/client/public"} {
		if !strings.Contains(output, want) {
			t.Fatalf("env shell remote output missing %q:\n%s", want, output)
		}
	}
	if len(sshArgs) != 6 || sshArgs[0] != "ssh" || sshArgs[4] != "client@203.0.113.10" {
		t.Fatalf("ssh args = %#v, want kinsta shell args", sshArgs)
	}

	stderr := captureStderr(t, func() {
		if got := Run([]string{"env", "ssh"}); got != 1 {
			t.Fatalf("Run(env ssh) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "unsupported env command") {
		t.Fatalf("Run(env ssh) stderr = %q, want unsupported env command", stderr)
	}
}

func TestRunEnvLogsRemoteUsesConfiguredRemote(t *testing.T) {
	configHome := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", stateDir)
	t.Setenv("NF_DATA_HOME", configHome)
	writeTestWPDefaults(t, "test-salt")
	if err := state.SaveStateRecords("sites", []map[string]any{{"provider": "kinsta", "site_id": "client-kinsta", "env": "live", "url": "https://www.example.com/", "path": "/www/client/public", "ssh": map[string]any{"host": "203.0.113.10", "port": "12345", "user": "client"}}}); err != nil {
		t.Fatalf("SaveStateRecords(sites) error = %v", err)
	}

	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{
		"version":   2,
		"project":   map[string]any{"slug": "client", "password_version": 0},
		"wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}},
		"remotes":   map[string]any{"production": "client-kinsta:live"},
	}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(nf.json) error = %v", err)
	}
	oldRunSSHCommand := runSSHCommandFn
	var sshArgs []string
	runSSHCommandFn = func(args []string) error {
		sshArgs = append([]string(nil), args...)
		return nil
	}
	t.Cleanup(func() { runSSHCommandFn = oldRunSSHCommand })
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	output := captureStdout(t, func() {
		if got := Run([]string{"env", "logs", "production"}); got != 0 {
			t.Fatalf("Run(env logs production) = %d, want 0", got)
		}
	})
	for _, want := range []string{"Site logs preflight:", "site:     client-kinsta", "env:      live", "provider: kinsta", "> ssh -p 12345 client@203.0.113.10", "cd /www/client/public", "touch wp-content/debug.log", "tail -f wp-content/debug.log"} {
		if !strings.Contains(output, want) {
			t.Fatalf("env logs remote output missing %q:\n%s", want, output)
		}
	}
	if got, want := sshArgs, []string{"ssh", "-p", "12345", "client@203.0.113.10", "cd /www/client/public && mkdir -p wp-content && touch wp-content/debug.log && tail -f wp-content/debug.log"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh args = %#v, want %#v", got, want)
	}

	stderr := captureStderr(t, func() {
		if got := Run([]string{"env", "logs", "missing"}); got != 1 {
			t.Fatalf("Run(env logs missing) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "No configured remote matched \"missing\".") {
		t.Fatalf("Run(env logs missing) stderr = %q, want missing remote", stderr)
	}
	stderr = captureStderr(t, func() {
		if got := Run([]string{"env", "logs", "production", "extra"}); got != 1 {
			t.Fatalf("Run(env logs production extra) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "env logs takes at most one remote") {
		t.Fatalf("Run(env logs extra) stderr = %q, want too many args", stderr)
	}
	stderr = captureStderr(t, func() {
		if got := Run([]string{"env", "logs", "--bad"}); got != 1 {
			t.Fatalf("Run(env logs --bad) = %d, want 1", got)
		}
	})
	if !strings.Contains(stderr, "unknown env logs flag: --bad") {
		t.Fatalf("Run(env logs --bad) stderr = %q, want unknown flag", stderr)
	}
}

func TestRunEnvLogsExecutesLocalComposeLogs(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")

	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir(.git) error = %v", err)
	}
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}}}
	projectData, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "nf.json"), append(projectData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	binDir := t.TempDir()
	capturePath := filepath.Join(t.TempDir(), "docker-args.txt")
	script := "#!/bin/sh\nprintf '%s\n' \"$@\" > \"$CAPTURE_FILE\"\n"
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
		if got := Run([]string{"env", "logs"}); got != 0 {
			t.Fatalf("Run(env logs) = %d, want 0", got)
		}
	})
	if !strings.Contains(output, "> docker compose logs -f wordpress") {
		t.Fatalf("Run(env logs) stdout = %q, want compose logs preview", output)
	}
	args, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := strings.Split(strings.TrimSpace(string(args)), "\n"), []string{"compose", "logs", "-f", "wordpress"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("docker args = %#v, want %#v", got, want)
	}
}

func TestRunEnvShellExecutesWordpressShell(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_DATA_HOME", configHome)
	t.Setenv("NF_PASSWORD_SALT", "test-salt")

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
	project := map[string]any{"version": 2, "project": map[string]any{"slug": "client", "password_version": 0}, "wordpress": map[string]any{"themes": []any{map[string]any{"slug": "theme", "source": "repo", "path": "theme"}}}}
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
	if !strings.Contains(output, "> docker compose exec --user nonfiction wordpress bash") {
		t.Fatalf("Run(shell) stdout = %q, want compose exec preview", output)
	}
	args, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := strings.Split(strings.TrimSpace(string(args)), "\n"), []string{"compose", "exec", "--user", "nonfiction", "wordpress", "bash"}; !reflect.DeepEqual(got, want) {
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

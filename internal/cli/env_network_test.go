package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvUpNetworkRecovery(t *testing.T) {
	const missingNetwork = "Error response from daemon: failed to set up container networking: network deadbeef not found"
	for _, tt := range []struct {
		name      string
		failure   string
		failRetry bool
		failBuild bool
		rebuild   bool
		wantRetry bool
		wantErr   bool
	}{
		{name: "healthy"},
		{name: "stale network", failure: missingNetwork, wantRetry: true},
		{name: "older daemon", failure: "Error response from daemon: network deadbeef not found", wantRetry: true},
		{name: "rebuild", failure: missingNetwork, rebuild: true, wantRetry: true},
		{name: "retry fails", failure: missingNetwork, failRetry: true, wantRetry: true, wantErr: true},
		{name: "port conflict", failure: "Error response from daemon: address already in use", wantErr: true},
		{name: "address pool exhausted", failure: "Error response from daemon: all predefined address pools have been fully subnetted", wantErr: true},
		{name: "missing image", failure: "Error response from daemon: image network not found", wantErr: true},
		{name: "application output", failure: "application: network deadbeef not found", wantErr: true},
		{name: "successful warning", failure: missingNetwork},
		{name: "build fails", failure: missingNetwork, rebuild: true, failBuild: true, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NF_CONFIG_HOME", t.TempDir())
			t.Setenv("NF_STATE_HOME", t.TempDir())
			t.Setenv("NF_DATA_HOME", t.TempDir())
			t.Setenv("NF_NETWORK_FAILURE", tt.failure)
			failureStatus := "1"
			if tt.name == "successful warning" {
				failureStatus = "0"
			}
			t.Setenv("NF_NETWORK_FAILURE_STATUS", failureStatus)
			retryStatus, buildStatus := "0", "0"
			if tt.failRetry {
				retryStatus = "1"
			}
			if tt.failBuild {
				buildStatus = "1"
			}
			t.Setenv("NF_NETWORK_RETRY_STATUS", retryStatus)
			t.Setenv("NF_NETWORK_BUILD_STATUS", buildStatus)
			dir := t.TempDir()
			compose := filepath.Join(dir, "compose")
			script := `#!/bin/sh
set -eu
printf '%s' "$*" | tr '\n' ' ' >> commands.log
printf '\n' >> commands.log
case "$*" in
  build) exit "$NF_NETWORK_BUILD_STATUS" ;;
  'up -d')
    if [ -n "$NF_NETWORK_FAILURE" ]; then
      printf '%s\n' "$NF_NETWORK_FAILURE" >&2
      exit "$NF_NETWORK_FAILURE_STATUS"
    fi ;;
  'up -d --force-recreate')
    printf '%s\n' "$NF_NETWORK_FAILURE" >&2
    exit "$NF_NETWORK_RETRY_STATUS" ;;
esac
`
			if err := os.WriteFile(compose, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			runner := envCommandRunner{cfg: envConfig{Compose: compose, EnvDir: dir, WordpressService: "wordpress", ThemeSlug: "twentytwentyfive"}, rebuild: tt.rebuild}
			var runErr error
			var output string
			stderr := captureStderr(t, func() {
				output = captureStdout(t, func() { runErr = runner.ensureUpInstalledActive(dir) })
			})
			if (runErr != nil) != tt.wantErr {
				t.Fatalf("startup error = %v, wantErr %v", runErr, tt.wantErr)
			}
			if strings.Contains(stderr, "preserving volumes and bind mounts") != tt.wantRetry {
				t.Fatalf("recovery explanation does not match retry: %s", stderr)
			}
			if tt.failRetry && !strings.Contains(runErr.Error(), "after recreating containers") {
				t.Fatalf("missing recovery error context: %v", runErr)
			}
			if tt.failure != "" && !tt.failBuild && !strings.Contains(stderr, tt.failure) {
				t.Fatalf("original Docker error was not displayed: %s", stderr)
			}
			data, err := os.ReadFile(filepath.Join(dir, "commands.log"))
			if err != nil {
				t.Fatal(err)
			}
			commands := strings.Split(strings.TrimSpace(string(data)), "\n")
			wantStart := []string{"up -d"}
			if tt.wantRetry {
				wantStart = append(wantStart, "up -d --force-recreate")
				if !strings.Contains(output, "> "+compose+" up -d --force-recreate") {
					t.Fatalf("missing recovery preview: %s", output)
				}
			}
			if tt.rebuild {
				wantStart = append([]string{"build"}, wantStart...)
			}
			if tt.failBuild {
				wantStart = []string{"build"}
			}
			if len(commands) < len(wantStart) || strings.Join(commands[:len(wantStart)], "\n") != strings.Join(wantStart, "\n") {
				t.Fatalf("commands = %q, want startup prefix %q", commands, wantStart)
			}
			if tt.wantErr && len(commands) != len(wantStart) {
				t.Fatalf("continued after failed startup: %q", commands)
			}
			for _, command := range commands[len(wantStart):] {
				if !strings.HasPrefix(command, "exec ") {
					t.Fatalf("unexpected recovery command: %s", command)
				}
			}
			if !tt.wantErr && (len(commands) == len(wantStart) || !strings.Contains(string(data), "core is-installed")) {
				t.Fatalf("did not continue WordPress bootstrap: %s", data)
			}
		})
	}
}

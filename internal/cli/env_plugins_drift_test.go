package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRepoPluginFingerprintDetectsPayloadDrift(t *testing.T) {
	tests := []struct {
		name        string
		change      func(t *testing.T, sourceDir, installedDir string)
		wantMatches bool
	}{
		{
			name: "matching with different metadata",
			change: func(t *testing.T, _, installedDir string) {
				file := filepath.Join(installedDir, "includes", "feature.php")
				if err := os.Chmod(file, 0o600); err != nil {
					t.Fatalf("Chmod(remote file) error = %v", err)
				}
				stamp := time.Unix(1_700_000_000, 0)
				if err := os.Chtimes(file, stamp, stamp); err != nil {
					t.Fatalf("Chtimes(remote file) error = %v", err)
				}
			},
			wantMatches: true,
		},
		{
			name: "changed file contents",
			change: func(t *testing.T, _, installedDir string) {
				writeRepoPluginTestFile(t, installedDir, "includes/feature.php", "<?php\nreturn 'remote';\n")
			},
		},
		{
			name: "file added locally",
			change: func(t *testing.T, sourceDir, _ string) {
				writeRepoPluginTestFile(t, sourceDir, "includes/local.php", "<?php\n")
			},
		},
		{
			name: "file missing remotely",
			change: func(t *testing.T, _, installedDir string) {
				if err := os.Remove(filepath.Join(installedDir, "includes", "feature.php")); err != nil {
					t.Fatalf("Remove(remote file) error = %v", err)
				}
			},
		},
		{
			name: "extra file remotely",
			change: func(t *testing.T, _, installedDir string) {
				writeRepoPluginTestFile(t, installedDir, "includes/remote.php", "<?php\n")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sourceDir := filepath.Join(t.TempDir(), "client-plugin")
			installedDir := filepath.Join(t.TempDir(), "client-plugin")
			writeRepoPluginTestPayload(t, sourceDir)
			writeRepoPluginTestPayload(t, installedDir)
			if test.change != nil {
				test.change(t, sourceDir, installedDir)
			}

			localFingerprint, err := pluginSourceFingerprint(sourceDir)
			if err != nil {
				t.Fatalf("pluginSourceFingerprint(local) error = %v", err)
			}
			installedFingerprint, err := pluginSourceFingerprint(installedDir)
			if err != nil {
				t.Fatalf("pluginSourceFingerprint(installed) error = %v", err)
			}
			if got := localFingerprint == installedFingerprint; got != test.wantMatches {
				t.Fatalf("fingerprints match = %t, want %t", got, test.wantMatches)
			}
		})
	}
}

func TestRepoPluginFingerprintMatchesPackagePayload(t *testing.T) {
	sourceDir := filepath.Join(t.TempDir(), "client-plugin")
	writeRepoPluginTestPayload(t, sourceDir)
	writeRepoPluginTestFile(t, sourceDir, ".git/config", "not deployed\n")

	fingerprint, err := pluginSourceFingerprint(sourceDir)
	if err != nil {
		t.Fatalf("pluginSourceFingerprint() error = %v", err)
	}
	if fingerprint == "" {
		t.Fatal("pluginSourceFingerprint() returned an empty fingerprint")
	}
	zipPath := filepath.Join(t.TempDir(), "client-plugin.zip")
	count, err := packagePluginSource(sourceDir, zipPath, "client-plugin")
	if err != nil {
		t.Fatalf("packagePluginSource() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("packagePluginSource() file count = %d, want 2", count)
	}
	names := readZipNames(t, zipPath)
	if names["client-plugin/.git/config"] {
		t.Fatalf("packagePluginSource() included .git file: %#v", names)
	}
}

func TestRemoteRepoPluginFingerprintMatchesLocalAlgorithm(t *testing.T) {
	if _, err := exec.LookPath("php"); err != nil {
		t.Skip("php is not available")
	}
	pluginDir := filepath.Join(t.TempDir(), "client-plugin")
	writeRepoPluginTestPayload(t, pluginDir)
	want, err := pluginSourceFingerprint(pluginDir)
	if err != nil {
		t.Fatalf("pluginSourceFingerprint() error = %v", err)
	}
	code := `define("WP_PLUGIN_DIR",` + strconv.Quote(filepath.Dir(pluginDir)) + `);` + remoteRepoPluginFingerprintPHP(filepath.Base(pluginDir))
	cmd := exec.Command("php", "-r", code)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("remote fingerprint PHP failed: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != want {
		t.Fatalf("remote fingerprint = %q, want %q", got, want)
	}
	if err := os.Symlink("client-plugin.php", filepath.Join(pluginDir, "remote-link.php")); err != nil {
		t.Fatalf("Symlink(remote file) error = %v", err)
	}
	output, err = exec.Command("php", "-r", code).CombinedOutput()
	if err != nil {
		t.Fatalf("remote fingerprint PHP with symlink failed: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got == want {
		t.Fatalf("remote fingerprint with extra symlink = %q, want drift from %q", got, want)
	}
}

func TestRepoPluginCodeStatusAndDiff(t *testing.T) {
	repoRoot := t.TempDir()
	plugin := wordpressPluginSpec{Slug: "client-plugin", Source: "repo", Install: true, Activate: true, AutoUpdate: true}
	sourceDir := filepath.Join(repoRoot, "plugins", plugin.Slug)
	writeRepoPluginTestPayload(t, sourceDir)
	localFingerprint, err := pluginSourceFingerprint(sourceDir)
	if err != nil {
		t.Fatalf("pluginSourceFingerprint() error = %v", err)
	}

	tests := []struct {
		name         string
		status       wordpressPluginStatus
		removeSource bool
		wantCode     string
		wantChange   string
		wantDrift    bool
	}{
		{
			name:       "matching repo source and installed plugin",
			status:     wordpressPluginStatus{Plugin: plugin, Installed: true, Active: true, AutoUpdate: true, RemoteFingerprint: localFingerprint},
			wantCode:   repoPluginCodeCurrent,
			wantChange: "ok",
		},
		{
			name:       "changed repo source",
			status:     wordpressPluginStatus{Plugin: plugin, Installed: true, Active: true, AutoUpdate: true, RemoteFingerprint: strings.Repeat("0", 64)},
			wantCode:   repoPluginCodeDrifted,
			wantChange: "refresh repo source",
			wantDrift:  true,
		},
		{
			name:       "missing installed plugin",
			status:     wordpressPluginStatus{Plugin: plugin},
			wantCode:   repoPluginCodeUnavailable,
			wantChange: "install, activate, enable auto-update",
			wantDrift:  true,
		},
		{
			name:         "missing repo source",
			status:       wordpressPluginStatus{Plugin: plugin, Installed: true, Active: true, AutoUpdate: true, RemoteFingerprint: localFingerprint},
			removeSource: true,
			wantCode:     repoPluginCodeUnavailable,
			wantChange:   "source unavailable locally",
			wantDrift:    true,
		},
		{
			name:       "source and activation drift",
			status:     wordpressPluginStatus{Plugin: plugin, Installed: true, RemoteFingerprint: strings.Repeat("f", 64)},
			wantCode:   repoPluginCodeDrifted,
			wantChange: "refresh repo source, activate, enable auto-update",
			wantDrift:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.removeSource {
				if err := os.Rename(sourceDir, sourceDir+"-missing"); err != nil {
					t.Fatalf("Rename(source) error = %v", err)
				}
				t.Cleanup(func() { _ = os.Rename(sourceDir+"-missing", sourceDir) })
			}
			statuses := []wordpressPluginStatus{test.status}
			applyRepoPluginCodeStatus(statuses, repoRoot, true)
			if got := statuses[0].Code; got != test.wantCode {
				t.Fatalf("code = %q, want %q", got, test.wantCode)
			}
			if table := formatWordPressPluginStatusTable(statuses); !strings.Contains(table, test.wantCode) {
				t.Fatalf("status table missing code %q:\n%s", test.wantCode, table)
			}
			diffs, drift := wordpressPluginDiffs(statuses, repoRoot)
			if drift != test.wantDrift {
				t.Fatalf("drift = %t, want %t", drift, test.wantDrift)
			}
			if got := diffs[0].Change; got != test.wantChange {
				t.Fatalf("change = %q, want %q", got, test.wantChange)
			}
		})
	}
}

func TestLocalRepoPluginCodeStatusUsesBindMountedSource(t *testing.T) {
	repoRoot := t.TempDir()
	plugin := wordpressPluginSpec{Slug: "client-plugin", Source: "repo", Install: true, Activate: true, AutoUpdate: true}
	sourceDir := filepath.Join(repoRoot, "plugins", plugin.Slug)
	writeRepoPluginTestPayload(t, sourceDir)
	writeRepoPluginTestFile(t, sourceDir, ".git/config", "local only\n")
	statuses := []wordpressPluginStatus{{Plugin: plugin, Installed: true, Active: true, AutoUpdate: true, RemoteFingerprint: strings.Repeat("0", 64)}}

	applyRepoPluginCodeStatus(statuses, repoRoot, false)

	if got := statuses[0].Code; got != repoPluginCodeCurrent {
		t.Fatalf("local repo plugin code = %q, want %q", got, repoPluginCodeCurrent)
	}
}

func TestRepoPluginStatusHandlesMultiplePluginsWithoutChangingNonRepoBehavior(t *testing.T) {
	repoRoot := t.TempDir()
	repoOne := wordpressPluginSpec{Slug: "client-plugin", Source: "repo", Install: true, Activate: true, AutoUpdate: true}
	repoTwo := wordpressPluginSpec{Slug: "forms-plugin", Source: "repo", Install: true, Activate: true, AutoUpdate: true}
	stream := wordpressPluginSpec{Slug: "stream", Source: "wordpress.org", Install: true, Activate: true, AutoUpdate: true}
	writeRepoPluginTestPayload(t, filepath.Join(repoRoot, "plugins", repoOne.Slug))
	writeRepoPluginTestPayload(t, filepath.Join(repoRoot, "plugins", repoTwo.Slug))
	fingerprintOne, err := pluginSourceFingerprint(filepath.Join(repoRoot, "plugins", repoOne.Slug))
	if err != nil {
		t.Fatalf("pluginSourceFingerprint(%s) error = %v", repoOne.Slug, err)
	}
	statuses := parseRemotePluginStatusOutput([]wordpressPluginSpec{repoOne, stream, repoTwo}, strings.Join([]string{
		repoOne.Slug + "\tyes\tyes\tyes\trepo:" + fingerprintOne,
		"stream\tyes\tyes\tyes",
		repoTwo.Slug + "\tyes\tyes\tyes\trepo:" + strings.Repeat("0", 64),
	}, "\n"))
	applyRepoPluginCodeStatus(statuses, repoRoot, true)

	if statuses[0].Code != repoPluginCodeCurrent || statuses[1].Code != "" || statuses[2].Code != repoPluginCodeDrifted {
		t.Fatalf("statuses = %#v", statuses)
	}
	diffs, drift := wordpressPluginDiffs(statuses, repoRoot)
	if !drift || diffs[0].Change != "ok" || diffs[1].Change != "ok" || diffs[2].Change != "refresh repo source" {
		t.Fatalf("diffs = %#v, drift = %t", diffs, drift)
	}

	script := remotePluginStatusScript(envRemoteSyncTarget{WordPressPath: "/www/client/public", WPCommand: "wp"}, []wordpressPluginSpec{repoOne, stream, repoTwo})
	if got := strings.Count(script, "wp_cmd eval"); got != 2 {
		t.Fatalf("remote status script fingerprint calls = %d, want 2:\n%s", got, script)
	}
	for _, want := range []string{"repo:%s", "printf '%s\\t%s\\t%s\\t%s\\n' stream"} {
		if !strings.Contains(script, want) {
			t.Fatalf("remote status script missing %q:\n%s", want, script)
		}
	}
}

func writeRepoPluginTestPayload(t *testing.T, pluginDir string) {
	t.Helper()
	writeRepoPluginTestFile(t, pluginDir, "client-plugin.php", "<?php\n/* Plugin Name: Client Plugin */\n")
	writeRepoPluginTestFile(t, pluginDir, "includes/feature.php", "<?php\nreturn 'feature';\n")
}

func writeRepoPluginTestFile(t *testing.T, pluginDir, name, contents string) {
	t.Helper()
	file := filepath.Join(pluginDir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", name, err)
	}
	if err := os.WriteFile(file, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", name, err)
	}
}

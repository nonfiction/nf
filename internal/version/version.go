package version

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
)

//go:embed VERSION
var defaultVersion string

var (
	Version = DefaultVersion()
	Commit  = "unknown"
	Date    = "unknown"
)

func init() {
	fillFromBuildInfo()
	fillFromGitRepo()
}

func DefaultVersion() string {
	return strings.TrimSpace(defaultVersion)
}

func fillFromBuildInfo() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	settings := map[string]string{}
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	fillFromVCSSettings(settings)
}

func fillFromVCSSettings(settings map[string]string) {
	if Commit == "unknown" {
		if revision := settings["vcs.revision"]; revision != "" {
			Commit = shortCommit(revision)
			if settings["vcs.modified"] == "true" {
				Commit += "-dirty"
			}
		}
	}
	if Date == "unknown" {
		if releaseDate := releaseDateFromVersion(Version); releaseDate != "" {
			Date = releaseDate
		}
	}
}

func fillFromGitRepo() {
	if Commit != "unknown" {
		return
	}
	root, err := gitOutput("rev-parse", "--show-toplevel")
	if err != nil || !looksLikeNFRepo(root) {
		return
	}
	revision, err := gitOutput("-C", root, "rev-parse", "--short", "HEAD")
	if err != nil || revision == "" {
		return
	}
	Commit = revision
	status, err := gitOutput("-C", root, "status", "--porcelain")
	if err == nil && status != "" {
		Commit += "-dirty"
	}
}

func gitOutput(args ...string) (string, error) {
	output, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func looksLikeNFRepo(root string) bool {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "module github.com/nonfiction/nf")
}

func shortCommit(revision string) string {
	if len(revision) <= 7 {
		return revision
	}
	return revision[:7]
}

func releaseDateFromVersion(value string) string {
	parts := strings.Split(value, ".")
	if len(parts) < 3 || len(parts[0]) != 4 || len(parts[1]) != 2 || len(parts[2]) != 2 {
		return ""
	}
	return parts[0] + "-" + parts[1] + "-" + parts[2]
}

func Summary() string {
	return fmt.Sprintf("nf %s", Version)
}

func Details() []string {
	return []string{
		"version: " + Version,
		"commit:  " + Commit,
		"date:    " + Date,
	}
}

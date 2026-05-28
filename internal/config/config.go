package config

import (
	"os"
	"path/filepath"
	"strings"
)

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

func expandHome(value string) string {
	switch value {
	case "~":
		return homeDir()
	case "":
		return value
	}
	if len(value) >= 2 && value[:2] == "~/" {
		return filepath.Join(homeDir(), value[2:])
	}
	return value
}

func ConfigHome() string {
	if override := os.Getenv("NF_CONFIG_HOME"); override != "" {
		return filepath.Clean(expandHome(override))
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(expandHome(xdg), "nf")
	}
	return filepath.Join(homeDir(), ".config", "nf")
}

func StateDir() string { return filepath.Join(ConfigHome(), "state") }

func EnvFile() string { return filepath.Join(ConfigHome(), ".env") }

func WorkbenchesDir() string { return filepath.Join(ConfigHome(), "workbenches") }

func WorkbenchDir(projectSlug string) string {
	projectSlug = strings.TrimSpace(projectSlug)
	if projectSlug == "" {
		projectSlug = "project"
	}
	return filepath.Join(WorkbenchesDir(), projectSlug)
}

func ProjectFile(root string) string {
	if root == "" {
		root = "."
	}
	return filepath.Join(root, ".nf", "project.json")
}

func DiscoverProjectRoot(start string) (string, bool) {
	if start == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", false
		}
		start = cwd
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	if info, err := os.Stat(abs); err == nil && !info.IsDir() {
		abs = filepath.Dir(abs)
	}
	for {
		if _, err := os.Stat(ProjectFile(abs)); err == nil {
			return abs, true
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", false
		}
		abs = parent
	}
}

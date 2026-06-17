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

func StateHome() string {
	if override := os.Getenv("NF_STATE_HOME"); override != "" {
		return filepath.Clean(expandHome(override))
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(expandHome(xdg), "nf")
	}
	return filepath.Join(homeDir(), ".local", "state", "nf")
}

func DataHome() string {
	if override := os.Getenv("NF_DATA_HOME"); override != "" {
		return filepath.Clean(expandHome(override))
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(expandHome(xdg), "nf")
	}
	return filepath.Join(homeDir(), ".local", "share", "nf")
}

func ConfigFile() string { return filepath.Join(ConfigHome(), "config.json") }

func StateDir() string { return StateHome() }

func EnvFile() string { return filepath.Join(ConfigHome(), ".env") }

func EnvsDir() string { return filepath.Join(DataHome(), "envs") }

func SnapshotsDir() string { return filepath.Join(DataHome(), "snapshots") }

func ExportsDir() string { return filepath.Join(DataHome(), "exports") }

func PluginCacheDir() string { return filepath.Join(DataHome(), "plugins") }

func PluginCachePluginDir(slug string) string {
	return filepath.Join(PluginCacheDir(), strings.TrimSpace(slug))
}

func PluginCacheZip(slug string) string {
	slug = strings.TrimSpace(slug)
	return filepath.Join(PluginCachePluginDir(slug), slug+".zip")
}

func LocalSnapshotsDir() string { return filepath.Join(SnapshotsDir(), "local") }

func RemoteSnapshotsDir() string { return filepath.Join(SnapshotsDir(), "remote") }

func EnvDir(projectSlug string) string {
	projectSlug = strings.TrimSpace(projectSlug)
	if projectSlug == "" {
		projectSlug = "project"
	}
	return filepath.Join(EnvsDir(), projectSlug)
}

func SnapshotProjectDir(projectSlug string) string {
	projectSlug = strings.TrimSpace(projectSlug)
	if projectSlug == "" {
		projectSlug = "project"
	}
	return filepath.Join(LocalSnapshotsDir(), projectSlug)
}

func SnapshotDir(projectSlug, snapshotName string) string {
	return filepath.Join(SnapshotProjectDir(projectSlug), snapshotName)
}

func RemoteSnapshotDir(snapshotName string) string {
	snapshotName = strings.TrimSpace(snapshotName)
	if snapshotName == "" {
		snapshotName = "snapshot"
	}
	return filepath.Join(RemoteSnapshotsDir(), snapshotName)
}

func ExportDir(exportName string) string {
	exportName = strings.TrimSpace(exportName)
	if exportName == "" {
		exportName = "export"
	}
	return filepath.Join(ExportsDir(), exportName)
}

func ProjectFile(root string) string {
	if root == "" {
		root = "."
	}
	return filepath.Join(root, "nf.json")
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

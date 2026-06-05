package cli

// Local WordPress plugin bootstrap commands backed by wordpress.plugins in nf.json.

import (
	"fmt"
	"os"
	"strings"
)

type wordpressPluginSpec struct {
	Slug       string
	Source     string
	Activate   bool
	AutoUpdate bool
}

func loadWordPressPluginSpecs(metadata map[string]any) ([]wordpressPluginSpec, error) {
	wordpress := mapMapAtPath(metadata, "wordpress")
	if wordpress == nil {
		return nil, nil
	}
	value, ok := wordpress["plugins"]
	if !ok {
		return nil, nil
	}
	raw, ok := value.([]any)
	if !ok {
		return nil, ProjectError{Msg: "nf.json wordpress.plugins must be an array"}
	}
	plugins := make([]wordpressPluginSpec, 0, len(raw))
	seen := map[string]struct{}{}
	for i, item := range raw {
		plugin, err := parseWordPressPluginSpec(i, item)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[plugin.Slug]; ok {
			return nil, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.plugins contains duplicate slug %q", plugin.Slug)}
		}
		seen[plugin.Slug] = struct{}{}
		plugins = append(plugins, plugin)
	}
	return plugins, nil
}

func parseWordPressPluginSpec(index int, value any) (wordpressPluginSpec, error) {
	switch typed := value.(type) {
	case string:
		slug := strings.TrimSpace(typed)
		if slug == "" {
			return wordpressPluginSpec{}, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.plugins[%d] must not be empty", index)}
		}
		return wordpressPluginSpec{Slug: slug, Source: "wordpress.org", Activate: true, AutoUpdate: true}, nil
	case map[string]any:
		slug := strings.TrimSpace(recordValueString(typed["slug"]))
		if slug == "" {
			return wordpressPluginSpec{}, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.plugins[%d].slug is required", index)}
		}
		activate := true
		if value, ok := typed["activate"]; ok {
			boolValue, ok := value.(bool)
			if !ok {
				return wordpressPluginSpec{}, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.plugins[%d].activate must be true or false", index)}
			}
			activate = boolValue
		}
		autoUpdate := true
		if value, ok := typed["auto_update"]; ok {
			boolValue, ok := value.(bool)
			if !ok {
				return wordpressPluginSpec{}, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.plugins[%d].auto_update must be true or false", index)}
			}
			autoUpdate = boolValue
		}
		source := strings.TrimSpace(recordValueString(typed["source"]))
		if source == "" {
			source = "wordpress.org"
		}
		return wordpressPluginSpec{Slug: slug, Source: source, Activate: activate, AutoUpdate: autoUpdate}, nil
	default:
		return wordpressPluginSpec{}, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.plugins[%d] must be a string or object", index)}
	}
}

func cmdEnvPluginsList(metadata map[string]any) int {
	plugins, err := loadWordPressPluginSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(plugins) == 0 {
		fmt.Println("No WordPress plugins configured.")
		return 0
	}
	rows := [][]string{{"plugin", "source", "activate", "auto-update"}}
	for _, plugin := range plugins {
		activate := "no"
		if plugin.Activate {
			activate = "yes"
		}
		autoUpdate := "no"
		if plugin.AutoUpdate {
			autoUpdate = "yes"
		}
		rows = append(rows, []string{plugin.Slug, plugin.Source, activate, autoUpdate})
	}
	fmt.Println(formatTable(rows))
	return 0
}

func cmdEnvPluginsInstall(cfg envConfig, metadata map[string]any) int {
	plugins, err := loadWordPressPluginSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(plugins) == 0 {
		fmt.Println("No WordPress plugins configured.")
		return 0
	}
	if err := ensureEnvReadyForSnapshot(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	runner := envPluginInstaller{cfg: cfg}
	if err := runner.Install(plugins); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("WordPress plugins installed.")
	return 0
}

type envPluginInstaller struct {
	cfg envConfig
}

func (i envPluginInstaller) Install(plugins []wordpressPluginSpec) error {
	envDir := localEnvDir(i.cfg)
	for _, plugin := range plugins {
		installed := i.pluginInstalled(envDir, plugin.Slug)
		if !installed {
			args := []string{"plugin", "install", pluginInstallSource(plugin)}
			if plugin.Activate {
				args = append(args, "--activate")
			}
			if err := runCommandSpec(execSpec{Dir: envDir, Args: envWpArgs(i.cfg, args...)}); err != nil {
				return err
			}
		} else if plugin.Activate && !i.pluginActive(envDir, plugin.Slug) {
			if err := runCommandSpec(execSpec{Dir: envDir, Args: envWpArgs(i.cfg, "plugin", "activate", plugin.Slug)}); err != nil {
				return err
			}
		}
		if plugin.AutoUpdate && !i.pluginAutoUpdatesEnabled(envDir, plugin.Slug) {
			if err := runCommandSpec(execSpec{Dir: envDir, Args: envWpArgs(i.cfg, "plugin", "auto-updates", "enable", plugin.Slug)}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (i envPluginInstaller) pluginInstalled(envDir, slug string) bool {
	return runCommandSpecQuiet(execSpec{Dir: envDir, Args: envWpArgs(i.cfg, "plugin", "is-installed", slug)}) == nil
}

func (i envPluginInstaller) pluginActive(envDir, slug string) bool {
	return runCommandSpecQuiet(execSpec{Dir: envDir, Args: envWpArgs(i.cfg, "plugin", "is-active", slug)}) == nil
}

func (i envPluginInstaller) pluginAutoUpdatesEnabled(envDir, slug string) bool {
	output, err := runCommandSpecOutputQuiet(execSpec{Dir: envDir, Args: envWpArgs(i.cfg, "plugin", "auto-updates", "status", slug, "--enabled-only", "--field=name")})
	if err != nil {
		return false
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == slug {
			return true
		}
	}
	return false
}

func pluginInstallSource(plugin wordpressPluginSpec) string {
	source := strings.TrimSpace(os.ExpandEnv(plugin.Source))
	if source == "" || source == "wordpress.org" {
		return plugin.Slug
	}
	return source
}

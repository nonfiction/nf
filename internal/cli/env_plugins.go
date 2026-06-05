package cli

// WordPress plugin bootstrap commands backed by wordpress.plugins in nf.json.

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
	fmt.Println(formatWordPressPluginTable(plugins))
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

type envPluginInstallOptions struct {
	RemoteName string
	DryRun     bool
	Yes        bool
}

func cmdEnvPluginsInstallWithOptions(root string, metadata map[string]any, opts envPluginInstallOptions) int {
	if strings.TrimSpace(opts.RemoteName) == "" {
		if opts.DryRun {
			fmt.Fprintln(os.Stderr, "env plugins install --dry-run requires a remote")
			return 1
		}
		cfg, ok := loadEnvConfig(root, metadata)
		if !ok {
			fmt.Fprintln(os.Stderr, "Missing env metadata in nf.json. Run nf env up first.")
			return 1
		}
		return cmdEnvPluginsInstall(cfg, metadata)
	}
	return cmdEnvPluginsInstallRemote(metadata, opts)
}

func cmdEnvPluginsInstallRemote(metadata map[string]any, opts envPluginInstallOptions) int {
	plugins, err := loadWordPressPluginSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(plugins) == 0 {
		fmt.Println("No WordPress plugins configured.")
		return 0
	}
	target, err := resolveEnvRemoteSyncTarget("plugins install", opts.RemoteName, metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	remoteTmp := remotePluginInstallTempDir(target)
	remotePlugins, uploads, err := remotePluginInstallSpecs(plugins, remoteTmp, !opts.DryRun)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Plugin install plan:")
	fmt.Printf("  remote:        %s\n", target.RemoteName)
	fmt.Printf("  site:          %s\n", target.SiteID)
	fmt.Printf("  env:           %s\n", target.Env)
	fmt.Printf("  provider:      %s\n", target.Provider)
	if target.TargetLabel != "" && target.TargetRef != "" {
		fmt.Printf("  %s:        %s\n", target.TargetLabel, target.TargetRef)
	}
	if target.URL != "" {
		fmt.Printf("  url:           %s\n", target.URL)
	}
	if target.AccessSummary != "" {
		fmt.Printf("  %s: %s\n", target.AccessLabel, target.AccessSummary)
	}
	mode := "execute"
	if opts.DryRun {
		mode = "dry-run"
	}
	fmt.Printf("  mode:          %s\n", mode)
	if len(uploads) > 0 {
		fmt.Printf("  uploads:       %d local plugin zip(s)\n", len(uploads))
	}
	fmt.Println()
	fmt.Println(formatWordPressPluginTable(plugins))
	if len(uploads) > 0 {
		fmt.Println()
		fmt.Println("Local plugin sources will be uploaded before install:")
		for _, upload := range uploads {
			fmt.Printf("  %s -> %s\n", upload.Plugin.Slug, upload.RemotePath)
		}
	}
	if opts.DryRun {
		fmt.Println("No remote plugins were changed.")
		return 0
	}
	if !opts.Yes {
		message := fmt.Sprintf("Install configured WordPress plugins on %s:%s (%s)?", target.SiteID, target.Env, target.RemoteName)
		confirmed, err := envRemoteSyncConfirm(message, false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	if len(uploads) > 0 {
		mkdirArgs := remoteSSHArgs(target, "rm -rf "+shellQuoteArg(remoteTmp)+" && mkdir -p "+shellQuoteArg(remoteTmp))
		printCommandArgs(mkdirArgs)
		if err := runSSHCommandFn(mkdirArgs); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		for _, upload := range uploads {
			uploadArgs := remotePluginUploadArgs(target, upload)
			printCommandArgs(uploadArgs)
			if err := runRsyncCommandFn(uploadArgs); err != nil {
				fmt.Fprintln(os.Stderr, err)
				_ = runSSHCommandFn(remoteSSHArgs(target, "rm -rf "+shellQuoteArg(remoteTmp)))
				return 1
			}
		}
	}
	script := remotePluginInstallScript(target, remotePlugins)
	printRemotePluginInstallCommand(target)
	if err := runSSHCommandFn(remoteSSHArgs(target, script)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if len(uploads) > 0 {
			_ = runSSHCommandFn(remoteSSHArgs(target, "rm -rf "+shellQuoteArg(remoteTmp)))
		}
		return 1
	}
	if len(uploads) > 0 {
		_ = runSSHCommandFn(remoteSSHArgs(target, "rm -rf "+shellQuoteArg(remoteTmp)))
	}
	fmt.Println("Remote WordPress plugins installed.")
	return 0
}

type remotePluginInstallSpec struct {
	Plugin        wordpressPluginSpec
	InstallSource string
}

type remotePluginUpload struct {
	Plugin     wordpressPluginSpec
	LocalPath  string
	RemotePath string
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

func remotePluginInstallSpecs(plugins []wordpressPluginSpec, remoteTmp string, requireFiles bool) ([]remotePluginInstallSpec, []remotePluginUpload, error) {
	remotePlugins := make([]remotePluginInstallSpec, 0, len(plugins))
	uploads := []remotePluginUpload{}
	for _, plugin := range plugins {
		installSource := pluginInstallSource(plugin)
		if remotePluginSourceLooksLocal(plugin, installSource) {
			localPath, err := filepath.Abs(installSource)
			if err != nil {
				return nil, nil, err
			}
			if requireFiles {
				info, err := os.Stat(localPath)
				if err != nil {
					return nil, nil, ProjectError{Msg: fmt.Sprintf("remote plugin source for %s does not exist: %s", plugin.Slug, localPath)}
				}
				if info.IsDir() {
					return nil, nil, ProjectError{Msg: fmt.Sprintf("remote plugin source for %s must be a zip file, got directory: %s", plugin.Slug, localPath)}
				}
			}
			remotePath := path.Join(remoteTmp, filepath.Base(localPath))
			upload := remotePluginUpload{Plugin: plugin, LocalPath: localPath, RemotePath: remotePath}
			uploads = append(uploads, upload)
			remotePlugins = append(remotePlugins, remotePluginInstallSpec{Plugin: plugin, InstallSource: remotePath})
			continue
		}
		remotePlugins = append(remotePlugins, remotePluginInstallSpec{Plugin: plugin, InstallSource: installSource})
	}
	return remotePlugins, uploads, nil
}

func remotePluginSourceLooksLocal(plugin wordpressPluginSpec, installSource string) bool {
	if installSource == "" || installSource == plugin.Slug || installSource == "wordpress.org" {
		return false
	}
	lower := strings.ToLower(installSource)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.Contains(lower, "://") {
		return false
	}
	return filepath.IsAbs(installSource) || strings.HasPrefix(installSource, ".") || strings.ContainsAny(installSource, `/\\`) || strings.HasSuffix(lower, ".zip")
}

func remotePluginInstallTempDir(target envRemoteSyncTarget) string {
	return path.Join("/tmp", "nf-plugins-"+cleanEnvSlug(target.SiteID+"-"+target.Env)+"-"+strconv.FormatInt(time.Now().Unix(), 10))
}

func remotePluginUploadArgs(target envRemoteSyncTarget, upload remotePluginUpload) []string {
	return []string{"rsync", "-az", "-e", "ssh -p " + target.SSHPort, upload.LocalPath, target.SSHUser + "@" + target.SSHHost + ":" + upload.RemotePath}
}

func formatWordPressPluginTable(plugins []wordpressPluginSpec) string {
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
	return formatTable(rows)
}

func remotePluginInstallScript(target envRemoteSyncTarget, plugins []remotePluginInstallSpec) string {
	var builder strings.Builder
	builder.WriteString("set -eu\n")
	builder.WriteString("cd ")
	builder.WriteString(shellQuoteArg(target.WordPressPath))
	builder.WriteString("\nwp_cmd() { ")
	builder.WriteString(target.WPCommand)
	builder.WriteString(" --path=")
	builder.WriteString(shellQuoteArg(target.WordPressPath))
	builder.WriteString(" \"$@\"; }\n")
	for _, remotePlugin := range plugins {
		plugin := remotePlugin.Plugin
		slug := shellQuoteArg(plugin.Slug)
		source := shellQuoteArg(remotePlugin.InstallSource)
		builder.WriteString("if ! wp_cmd plugin is-installed ")
		builder.WriteString(slug)
		builder.WriteString("; then\n")
		builder.WriteString("  wp_cmd plugin install ")
		builder.WriteString(source)
		if plugin.Activate {
			builder.WriteString(" --activate")
		}
		builder.WriteString("\n")
		if plugin.Activate {
			builder.WriteString("elif ! wp_cmd plugin is-active ")
			builder.WriteString(slug)
			builder.WriteString("; then\n")
			builder.WriteString("  wp_cmd plugin activate ")
			builder.WriteString(slug)
			builder.WriteString("\n")
		}
		builder.WriteString("fi\n")
		if plugin.AutoUpdate {
			builder.WriteString("if ! wp_cmd plugin auto-updates status ")
			builder.WriteString(slug)
			builder.WriteString(" --enabled-only --field=name | grep -qx ")
			builder.WriteString(slug)
			builder.WriteString("; then\n")
			builder.WriteString("  wp_cmd plugin auto-updates enable ")
			builder.WriteString(slug)
			builder.WriteString("\n")
			builder.WriteString("fi\n")
		}
	}
	return builder.String()
}

func printRemotePluginInstallCommand(target envRemoteSyncTarget) {
	args := []string{"ssh", "-p", target.SSHPort, target.SSHUser + "@" + target.SSHHost, "<wp plugin bootstrap script>"}
	printCommandArgs(args)
}

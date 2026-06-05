package cli

// WordPress plugin bootstrap commands backed by wordpress.plugins in nf.json.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
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

type envPluginAddOptions struct {
	Slug          string
	Source        string
	Activate      bool
	AutoUpdate    bool
	HasActivate   bool
	HasAutoUpdate bool
}

func cmdEnvPluginsAdd(root string, metadata map[string]any, opts envPluginAddOptions) int {
	if _, err := loadWordPressPluginSpecs(metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	plugins, err := projectWordPressPlugins(metadata, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, item := range plugins {
		plugin, err := parseWordPressPluginSpec(0, item)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if plugin.Slug == opts.Slug {
			fmt.Fprintf(os.Stderr, "nf.json wordpress.plugins already contains %q\n", opts.Slug)
			return 1
		}
	}
	plugins = append(plugins, wordpressPluginAddValue(opts))
	wordpress := metadata["wordpress"].(map[string]any)
	wordpress["plugins"] = plugins
	if err := saveProjectMetadata(root, metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Added WordPress plugin %s to nf.json.\n", opts.Slug)
	return 0
}

func cmdEnvPluginsRemove(root string, metadata map[string]any, slug string) int {
	if _, err := loadWordPressPluginSpecs(metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	plugins, err := projectWordPressPlugins(metadata, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(plugins) == 0 {
		fmt.Fprintf(os.Stderr, "nf.json wordpress.plugins does not contain %q\n", slug)
		return 1
	}
	kept := make([]any, 0, len(plugins))
	removed := false
	for _, item := range plugins {
		plugin, err := parseWordPressPluginSpec(0, item)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if plugin.Slug == slug {
			removed = true
			continue
		}
		kept = append(kept, item)
	}
	if !removed {
		fmt.Fprintf(os.Stderr, "nf.json wordpress.plugins does not contain %q\n", slug)
		return 1
	}
	wordpress := metadata["wordpress"].(map[string]any)
	wordpress["plugins"] = kept
	if err := saveProjectMetadata(root, metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Removed WordPress plugin %s from nf.json.\n", slug)
	return 0
}

func projectWordPressPlugins(metadata map[string]any, create bool) ([]any, error) {
	wordpress, ok := metadata["wordpress"].(map[string]any)
	if !ok || wordpress == nil {
		if !create {
			return nil, nil
		}
		wordpress = map[string]any{}
		metadata["wordpress"] = wordpress
	}
	value, ok := wordpress["plugins"]
	if !ok {
		if !create {
			return nil, nil
		}
		plugins := []any{}
		wordpress["plugins"] = plugins
		return plugins, nil
	}
	plugins, ok := value.([]any)
	if !ok {
		return nil, ProjectError{Msg: "nf.json wordpress.plugins must be an array"}
	}
	return plugins, nil
}

func wordpressPluginAddValue(opts envPluginAddOptions) any {
	if strings.TrimSpace(opts.Source) == "" && !opts.HasActivate && !opts.HasAutoUpdate {
		return opts.Slug
	}
	pairs := []orderedPair{{Key: "slug", Value: opts.Slug}}
	if strings.TrimSpace(opts.Source) != "" {
		pairs = append(pairs, orderedPair{Key: "source", Value: opts.Source})
	}
	if opts.HasActivate && !opts.Activate {
		pairs = append(pairs, orderedPair{Key: "activate", Value: false})
	}
	if opts.HasAutoUpdate && !opts.AutoUpdate {
		pairs = append(pairs, orderedPair{Key: "auto_update", Value: false})
	}
	return orderedObject{Pairs: pairs}
}

func cmdEnvPluginsStatusWithOptions(root string, metadata map[string]any, remoteName string) int {
	if strings.TrimSpace(remoteName) == "" {
		cfg, ok := loadEnvConfig(root, metadata)
		if !ok {
			fmt.Fprintln(os.Stderr, "Missing env metadata in nf.json. Run nf env up first.")
			return 1
		}
		return cmdEnvPluginsStatusLocal(cfg, metadata)
	}
	return cmdEnvPluginsStatusRemote(metadata, remoteName)
}

func cmdEnvPluginsDiffWithOptions(root string, metadata map[string]any, remoteName string) int {
	if strings.TrimSpace(remoteName) == "" {
		cfg, ok := loadEnvConfig(root, metadata)
		if !ok {
			fmt.Fprintln(os.Stderr, "Missing env metadata in nf.json. Run nf env up first.")
			return 1
		}
		return cmdEnvPluginsDiffLocal(cfg, metadata)
	}
	return cmdEnvPluginsDiffRemote(metadata, remoteName)
}

func cmdEnvPluginsStatusLocal(cfg envConfig, metadata map[string]any) int {
	plugins, err := loadWordPressPluginSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(plugins) == 0 {
		fmt.Println("No WordPress plugins configured.")
		return 0
	}
	checker := envPluginStatusChecker{cfg: cfg}
	statuses, ready, err := checker.statuses(plugins, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !ready {
		fmt.Fprintln(os.Stderr, "Local env is not ready. Run nf env up first.")
		return 1
	}
	fmt.Println(formatWordPressPluginStatusTable(statuses))
	return 0
}

func cmdEnvPluginsDiffLocal(cfg envConfig, metadata map[string]any) int {
	plugins, err := loadWordPressPluginSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(plugins) == 0 {
		fmt.Println("No WordPress plugins configured.")
		return 0
	}
	checker := envPluginStatusChecker{cfg: cfg}
	statuses, ready, err := checker.statuses(plugins, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !ready {
		fmt.Fprintln(os.Stderr, "Local env is not ready. Run nf env up first.")
		return 1
	}
	return printWordPressPluginDiff("Plugin diff:", nil, statuses)
}

func cmdEnvPluginsStatusRemote(metadata map[string]any, remoteName string) int {
	plugins, err := loadWordPressPluginSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(plugins) == 0 {
		fmt.Println("No WordPress plugins configured.")
		return 0
	}
	target, err := resolveEnvRemoteSyncTarget("plugins status", remoteName, metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	output, err := runSSHOutputFn(remoteSSHArgs(target, remotePluginStatusScript(target, plugins)))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	statuses := parseRemotePluginStatusOutput(plugins, string(output))
	fmt.Println("Plugin status:")
	fmt.Printf("  remote:   %s\n", target.RemoteName)
	fmt.Printf("  site:     %s\n", target.SiteID)
	fmt.Printf("  env:      %s\n", target.Env)
	fmt.Printf("  provider: %s\n", target.Provider)
	fmt.Println()
	fmt.Println(formatWordPressPluginStatusTable(statuses))
	return 0
}

func cmdEnvPluginsDiffRemote(metadata map[string]any, remoteName string) int {
	plugins, err := loadWordPressPluginSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(plugins) == 0 {
		fmt.Println("No WordPress plugins configured.")
		return 0
	}
	target, err := resolveEnvRemoteSyncTarget("plugins diff", remoteName, metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	output, err := runSSHOutputFn(remoteSSHArgs(target, remotePluginStatusScript(target, plugins)))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	statuses := parseWordPressPluginDiffStatusOutput(plugins, string(output))
	return printWordPressPluginDiff("Plugin diff:", &target, statuses)
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

type wordpressPluginStatus struct {
	Plugin     wordpressPluginSpec
	Installed  bool
	Active     bool
	AutoUpdate bool
	Extra      bool
}

type wordpressPluginDiff struct {
	Plugin wordpressPluginSpec
	Change string
	Drift  bool
}

type envPluginInstaller struct {
	cfg envConfig
}

func (i envPluginInstaller) Install(plugins []wordpressPluginSpec) error {
	envDir := localEnvDir(i.cfg)
	args := envCliArgs(i.cfg, "sh", "-lc", localPluginInstallScript(plugins))
	preview := envCliArgs(i.cfg, "<wp plugin bootstrap script>")
	return runCommandSpecWithPreview(execSpec{Dir: envDir, Args: args}, preview)
}

type envPluginStatusChecker struct {
	cfg envConfig
}

func (c envPluginStatusChecker) statuses(plugins []wordpressPluginSpec, includeExtras bool) ([]wordpressPluginStatus, bool, error) {
	output, err := runCommandSpecOutputSilent(execSpec{Dir: localEnvDir(c.cfg), Args: envCliArgs(c.cfg, "sh", "-lc", localPluginStatusScript(plugins))})
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(output) == "__NF_NOT_READY__" {
		return nil, false, nil
	}
	if includeExtras {
		return parseWordPressPluginDiffStatusOutput(plugins, output), true, nil
	}
	return parseRemotePluginStatusOutput(plugins, output), true, nil
}

func runCommandSpecOutputSilent(spec execSpec) (string, error) {
	if len(spec.Args) == 0 {
		return "", fmt.Errorf("unsupported repo command type")
	}
	cmd := exec.Command(spec.Args[0], spec.Args[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = append(os.Environ(), "COMPOSE_PROGRESS=quiet")
	stderr := new(bytes.Buffer)
	cmd.Stderr = stderr
	cmd.Stdin = os.Stdin
	output, err := cmd.Output()
	if err != nil && stderr.Len() > 0 {
		err = fmt.Errorf("%w\n%s", err, strings.TrimSpace(stderr.String()))
	}
	return string(output), err
}

func runCommandSpecWithPreview(spec execSpec, preview []string) error {
	if len(spec.Args) == 0 {
		return fmt.Errorf("unsupported repo command type")
	}
	printCommandArgs(preview)
	cmd := exec.Command(spec.Args[0], spec.Args[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = append(os.Environ(), "COMPOSE_PROGRESS=quiet")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
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

func formatWordPressPluginStatusTable(statuses []wordpressPluginStatus) string {
	rows := [][]string{{"plugin", "source", "installed", "active", "auto-update"}}
	for _, status := range statuses {
		rows = append(rows, []string{status.Plugin.Slug, status.Plugin.Source, yesNo(status.Installed), yesNo(status.Active), yesNo(status.AutoUpdate)})
	}
	return formatTable(rows)
}

func formatWordPressPluginDiffTable(diffs []wordpressPluginDiff) string {
	rows := [][]string{{"plugin", "change"}}
	for _, diff := range diffs {
		rows = append(rows, []string{diff.Plugin.Slug, diff.Change})
	}
	return formatTable(rows)
}

func wordpressPluginDiffs(statuses []wordpressPluginStatus) ([]wordpressPluginDiff, bool) {
	diffs := make([]wordpressPluginDiff, 0, len(statuses))
	drift := false
	for _, status := range statuses {
		changes := []string{}
		if status.Extra {
			changes = append(changes, extraPluginDiffChange(status))
		} else if !status.Installed {
			if pluginLocalSourceMissing(status.Plugin) {
				changes = append(changes, "source unavailable locally")
			} else {
				changes = append(changes, "install")
				if status.Plugin.Activate {
					changes = append(changes, "activate")
				}
				if status.Plugin.AutoUpdate {
					changes = append(changes, "enable auto-update")
				}
			}
		} else {
			if status.Plugin.Activate && !status.Active {
				changes = append(changes, "activate")
			}
			if status.Plugin.AutoUpdate && !status.AutoUpdate {
				changes = append(changes, "enable auto-update")
			}
		}
		change := "ok"
		if len(changes) > 0 {
			change = strings.Join(changes, ", ")
			drift = true
		}
		diffs = append(diffs, wordpressPluginDiff{Plugin: status.Plugin, Change: change, Drift: len(changes) > 0})
	}
	return diffs, drift
}

func extraPluginDiffChange(status wordpressPluginStatus) string {
	active := "inactive"
	if status.Active {
		active = "active"
	}
	autoUpdate := "auto-update off"
	if status.AutoUpdate {
		autoUpdate = "auto-update on"
	}
	return "extra (" + active + ", " + autoUpdate + ")"
}

func pluginLocalSourceMissing(plugin wordpressPluginSpec) bool {
	installSource := pluginInstallSource(plugin)
	if !remotePluginSourceLooksLocal(plugin, installSource) {
		return false
	}
	localPath, err := filepath.Abs(installSource)
	if err != nil {
		return true
	}
	info, err := os.Stat(localPath)
	return err != nil || info.IsDir()
}

func printWordPressPluginDiff(title string, target *envRemoteSyncTarget, statuses []wordpressPluginStatus) int {
	diffs, drift := wordpressPluginDiffs(statuses)
	fmt.Println(title)
	if target != nil {
		fmt.Printf("  remote:   %s\n", target.RemoteName)
		fmt.Printf("  site:     %s\n", target.SiteID)
		fmt.Printf("  env:      %s\n", target.Env)
		fmt.Printf("  provider: %s\n", target.Provider)
	}
	fmt.Println()
	fmt.Println(formatWordPressPluginDiffTable(diffs))
	if drift {
		return 2
	}
	return 0
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
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

func localPluginStatusScript(plugins []wordpressPluginSpec) string {
	var builder strings.Builder
	builder.WriteString("set -eu\n")
	builder.WriteString("if ! wp core is-installed --allow-root >/dev/null 2>&1; then printf '__NF_NOT_READY__\\n'; exit 0; fi\n")
	for _, plugin := range plugins {
		slug := shellQuoteArg(plugin.Slug)
		builder.WriteString("installed=no active=no auto_update=no\n")
		builder.WriteString("if wp plugin is-installed ")
		builder.WriteString(slug)
		builder.WriteString(" --allow-root >/dev/null 2>&1; then\n")
		builder.WriteString("  installed=yes\n")
		builder.WriteString("  if wp plugin is-active ")
		builder.WriteString(slug)
		builder.WriteString(" --allow-root >/dev/null 2>&1; then active=yes; fi\n")
		builder.WriteString("  if wp plugin auto-updates status ")
		builder.WriteString(slug)
		builder.WriteString(" --enabled-only --field=name --allow-root 2>/dev/null | grep -qx ")
		builder.WriteString(slug)
		builder.WriteString("; then auto_update=yes; fi\n")
		builder.WriteString("fi\n")
		builder.WriteString("printf '%s\\t%s\\t%s\\t%s\\n' ")
		builder.WriteString(slug)
		builder.WriteString(" \"$installed\" \"$active\" \"$auto_update\"\n")
	}
	writeExtraPluginStatusScript(&builder, plugins, "wp", true)
	return builder.String()
}

func localPluginInstallScript(plugins []wordpressPluginSpec) string {
	var builder strings.Builder
	builder.WriteString("set -eu\n")
	for _, plugin := range plugins {
		slug := shellQuoteArg(plugin.Slug)
		source := shellQuoteArg(pluginInstallSource(plugin))
		builder.WriteString("if ! wp plugin is-installed ")
		builder.WriteString(slug)
		builder.WriteString(" --allow-root; then\n")
		builder.WriteString("  wp plugin install ")
		builder.WriteString(source)
		if plugin.Activate {
			builder.WriteString(" --activate")
		}
		builder.WriteString(" --allow-root\n")
		if plugin.Activate {
			builder.WriteString("elif ! wp plugin is-active ")
			builder.WriteString(slug)
			builder.WriteString(" --allow-root; then\n")
			builder.WriteString("  wp plugin activate ")
			builder.WriteString(slug)
			builder.WriteString(" --allow-root\n")
		}
		builder.WriteString("fi\n")
		if plugin.AutoUpdate {
			builder.WriteString("if ! wp plugin auto-updates status ")
			builder.WriteString(slug)
			builder.WriteString(" --enabled-only --field=name --allow-root 2>/dev/null | grep -qx ")
			builder.WriteString(slug)
			builder.WriteString("; then\n")
			builder.WriteString("  wp plugin auto-updates enable ")
			builder.WriteString(slug)
			builder.WriteString(" --allow-root\n")
			builder.WriteString("fi\n")
		}
	}
	return builder.String()
}

func remotePluginStatusScript(target envRemoteSyncTarget, plugins []wordpressPluginSpec) string {
	var builder strings.Builder
	builder.WriteString("set -eu\n")
	builder.WriteString("cd ")
	builder.WriteString(shellQuoteArg(target.WordPressPath))
	builder.WriteString("\nwp_cmd() { ")
	builder.WriteString(target.WPCommand)
	builder.WriteString(" --path=")
	builder.WriteString(shellQuoteArg(target.WordPressPath))
	builder.WriteString(" \"$@\"; }\n")
	for _, plugin := range plugins {
		slug := shellQuoteArg(plugin.Slug)
		builder.WriteString("installed=no active=no auto_update=no\n")
		builder.WriteString("if wp_cmd plugin is-installed ")
		builder.WriteString(slug)
		builder.WriteString(" >/dev/null 2>&1; then\n")
		builder.WriteString("  installed=yes\n")
		builder.WriteString("  if wp_cmd plugin is-active ")
		builder.WriteString(slug)
		builder.WriteString(" >/dev/null 2>&1; then active=yes; fi\n")
		builder.WriteString("  if wp_cmd plugin auto-updates status ")
		builder.WriteString(slug)
		builder.WriteString(" --enabled-only --field=name 2>/dev/null | grep -qx ")
		builder.WriteString(slug)
		builder.WriteString("; then auto_update=yes; fi\n")
		builder.WriteString("fi\n")
		builder.WriteString("printf '%s\\t%s\\t%s\\t%s\\n' ")
		builder.WriteString(slug)
		builder.WriteString(" \"$installed\" \"$active\" \"$auto_update\"\n")
	}
	writeExtraPluginStatusScript(&builder, plugins, "wp_cmd", false)
	return builder.String()
}

func writeExtraPluginStatusScript(builder *strings.Builder, plugins []wordpressPluginSpec, wpCommand string, allowRoot bool) {
	configured := strings.Builder{}
	for _, plugin := range plugins {
		configured.WriteString(" ")
		configured.WriteString(plugin.Slug)
		configured.WriteString(" ")
	}
	builder.WriteString("configured_plugins=")
	builder.WriteString(shellQuoteArg(configured.String()))
	builder.WriteString("\n")
	builder.WriteString(wpCommand)
	builder.WriteString(" plugin list --field=name")
	if allowRoot {
		builder.WriteString(" --allow-root")
	}
	builder.WriteString(" 2>/dev/null | while IFS= read -r slug; do\n")
	builder.WriteString("  case \"$configured_plugins\" in *\" $slug \"*) continue ;; esac\n")
	builder.WriteString("  active=no auto_update=no\n")
	builder.WriteString("  if ")
	builder.WriteString(wpCommand)
	builder.WriteString(" plugin is-active \"$slug\"")
	if allowRoot {
		builder.WriteString(" --allow-root")
	}
	builder.WriteString(" >/dev/null 2>&1; then active=yes; fi\n")
	builder.WriteString("  if ")
	builder.WriteString(wpCommand)
	builder.WriteString(" plugin auto-updates status \"$slug\" --enabled-only --field=name")
	if allowRoot {
		builder.WriteString(" --allow-root")
	}
	builder.WriteString(" 2>/dev/null | grep -qx \"$slug\"; then auto_update=yes; fi\n")
	builder.WriteString("  printf '%s\\t%s\\t%s\\t%s\\t%s\\n' \"$slug\" yes \"$active\" \"$auto_update\" extra\n")
	builder.WriteString("done\n")
}

func parseRemotePluginStatusOutput(plugins []wordpressPluginSpec, output string) []wordpressPluginStatus {
	bySlug := map[string]wordpressPluginStatus{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) != 4 {
			continue
		}
		bySlug[fields[0]] = wordpressPluginStatus{Installed: fields[1] == "yes", Active: fields[2] == "yes", AutoUpdate: fields[3] == "yes"}
	}
	statuses := make([]wordpressPluginStatus, 0, len(plugins))
	for _, plugin := range plugins {
		status := bySlug[plugin.Slug]
		status.Plugin = plugin
		statuses = append(statuses, status)
	}
	return statuses
}

func parseWordPressPluginDiffStatusOutput(plugins []wordpressPluginSpec, output string) []wordpressPluginStatus {
	statuses := parseRemotePluginStatusOutput(plugins, output)
	configured := map[string]struct{}{}
	for _, plugin := range plugins {
		configured[plugin.Slug] = struct{}{}
	}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) != 5 || fields[4] != "extra" {
			continue
		}
		if _, ok := configured[fields[0]]; ok {
			continue
		}
		statuses = append(statuses, wordpressPluginStatus{
			Plugin:     wordpressPluginSpec{Slug: fields[0], Source: "installed"},
			Installed:  fields[1] == "yes",
			Active:     fields[2] == "yes",
			AutoUpdate: fields[3] == "yes",
			Extra:      true,
		})
	}
	return statuses
}

func printRemotePluginInstallCommand(target envRemoteSyncTarget) {
	args := []string{"ssh", "-p", target.SSHPort, target.SSHUser + "@" + target.SSHHost, "<wp plugin bootstrap script>"}
	printCommandArgs(args)
}

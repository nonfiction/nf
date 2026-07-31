package cli

// WordPress plugin bootstrap commands backed by wordpress.plugins in nf.json.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/project"
)

const (
	wordpressPluginRepoSource        = "repo"
	wordpressPluginCacheSource       = "cache"
	localRepoPluginInstallSourceMark = "__NF_REPO_PLUGIN_MOUNT__"
	repoPluginCodeCurrent            = "current"
	repoPluginCodeDrifted            = "drifted"
	repoPluginCodeUnavailable        = "unavailable"
)

type wordpressPluginSpec struct {
	Slug       string
	Source     string
	Install    bool
	Activate   bool
	AutoUpdate bool
	Note       string
}

func loadWordPressPluginSpecs(metadata *projectMetadata) ([]wordpressPluginSpec, error) {
	raw := metadata.WordPress.Plugins
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
		if err := validatePluginSlug(slug); err != nil {
			return wordpressPluginSpec{}, err
		}
		return wordpressPluginSpec{Slug: slug, Source: "wordpress.org", Install: true, Activate: true, AutoUpdate: true}, nil
	case map[string]any:
		location := fmt.Sprintf("nf.json wordpress.plugins[%d]", index)
		if err := validateProjectObjectFields(location, typed, "slug", "source", "install", "activate", "auto_update", "note"); err != nil {
			return wordpressPluginSpec{}, err
		}
		slug, err := projectObjectStringField(location, typed, "slug", true)
		if err != nil {
			return wordpressPluginSpec{}, err
		}
		if err := validatePluginSlug(slug); err != nil {
			return wordpressPluginSpec{}, err
		}
		activate := true
		install := true
		if value, ok := typed["install"]; ok {
			boolValue, ok := value.(bool)
			if !ok {
				return wordpressPluginSpec{}, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.plugins[%d].install must be true or false", index)}
			}
			install = boolValue
		}
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
		source, err := projectObjectStringField(location, typed, "source", false)
		if err != nil {
			return wordpressPluginSpec{}, err
		}
		if source == "" && install {
			source = "wordpress.org"
		}
		note, err := projectObjectStringField(location, typed, "note", false)
		if err != nil {
			return wordpressPluginSpec{}, err
		}
		return wordpressPluginSpec{Slug: slug, Source: source, Install: install, Activate: activate, AutoUpdate: autoUpdate, Note: note}, nil
	default:
		return wordpressPluginSpec{}, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.plugins[%d] must be a string or object", index)}
	}
}

func cmdEnvPluginsList(metadata *projectMetadata) int {
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
	Install       bool
	Activate      bool
	AutoUpdate    bool
	Note          string
	HasInstall    bool
	HasActivate   bool
	HasAutoUpdate bool
}

func cmdEnvPluginsAdd(root string, metadata *projectMetadata, opts envPluginAddOptions) int {
	if err := validatePluginSlug(opts.Slug); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
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
	if strings.EqualFold(strings.TrimSpace(opts.Source), wordpressPluginRepoSource) {
		if err := scaffoldRepoPlugin(root, opts.Slug); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	plugins = append(plugins, wordpressPluginAddValue(opts))
	metadata.WordPress.Plugins = plugins
	if err := saveProjectMetadata(root, metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Added WordPress plugin %s to nf.json.\n", opts.Slug)
	return 0
}

func cmdEnvPluginsRemove(root string, metadata *projectMetadata, slug string) int {
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
	metadata.WordPress.Plugins = kept
	if err := saveProjectMetadata(root, metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Removed WordPress plugin %s from nf.json.\n", slug)
	return 0
}

func scaffoldRepoPlugin(root, slug string) error {
	if err := validatePluginSlug(slug); err != nil {
		return err
	}
	pluginDir := filepath.Join(root, "plugins", slug)
	if info, err := os.Stat(pluginDir); err == nil {
		if !info.IsDir() {
			return ProjectError{Msg: fmt.Sprintf("repo plugin path exists but is not a directory: %s", pluginDir)}
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		return err
	}
	pluginFile := filepath.Join(pluginDir, slug+".php")
	contents := fmt.Sprintf(`<?php
/**
 * Plugin Name: %s
 * Description: Project plugin for %s.
 * Version: 0.1.0
 */

if (!defined('ABSPATH')) {
    exit;
}
`, slugToTitle(slug), slugToTitle(slug))
	return os.WriteFile(pluginFile, []byte(contents), 0o644)
}

type envPluginCacheOptions struct {
	Command string
	Slug    string
	Source  string
}

func cmdEnvPluginsCache(cfg envConfig, opts envPluginCacheOptions) int {
	switch opts.Command {
	case "add":
		return cmdEnvPluginsCacheAdd(opts.Slug, opts.Source)
	case "save":
		return cmdEnvPluginsCacheSave(cfg, opts.Slug)
	case "list":
		return cmdEnvPluginsCacheList()
	case "show":
		return cmdEnvPluginsCacheShow(opts.Slug)
	case "remove":
		return cmdEnvPluginsCacheRemove(opts.Slug)
	default:
		fmt.Fprintln(os.Stderr, "unsupported plugin cache command")
		return 1
	}
}

func cmdEnvPluginsCacheAdd(slug, sourcePath string) int {
	if err := validatePluginSlug(slug); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	sourcePath = os.ExpandEnv(strings.TrimSpace(sourcePath))
	info, err := os.Stat(sourcePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if info.IsDir() {
		fmt.Fprintf(os.Stderr, "plugin cache source for %s must be a zip file, got directory: %s\n", slug, sourcePath)
		return 1
	}
	destination := config.PluginCacheZip(slug)
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := copyFile(sourcePath, destination); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Cached WordPress plugin %s at %s\n", slug, destination)
	return 0
}

func cmdEnvPluginsCacheSave(cfg envConfig, slug string) int {
	if err := validatePluginSlug(slug); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := ensureEnvReadyForSnapshot(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	hostDir := filepath.Join(cfg.managedTransferDir(), ".nf-plugin-cache-save")
	if err := os.RemoveAll(hostDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(hostDir) }()
	containerArchive := path.Join(cfg.uploadsContainerPath(), ".nf-plugin-cache-save", slug+".tar.gz")
	hostArchive := filepath.Join(hostDir, slug+".tar.gz")
	script := "set -eu\nmkdir -p " + shellQuoteArg(path.Dir(containerArchive)) + "\ntar -C /var/www/html/wp-content/plugins -czf " + shellQuoteArg(containerArchive) + " " + shellQuoteArg(slug) + "\n"
	args := envWordpressRootExecArgs(cfg, "sh", "-lc", script)
	preview := envWordpressRootExecArgs(cfg, "<save plugin cache archive>")
	if err := runCommandSpecWithPreview(execSpec{Dir: localEnvDir(cfg), Args: args}, preview); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	tmpDir, err := os.MkdirTemp("", "nf-plugin-cache-save-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	if err := extractPluginTarGz(hostArchive, tmpDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	destination := config.PluginCacheZip(slug)
	if _, err := packagePluginSource(filepath.Join(tmpDir, slug), destination, slug); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Cached WordPress plugin %s at %s\n", slug, destination)
	return 0
}

func cmdEnvPluginsCacheList() int {
	entries, err := os.ReadDir(config.PluginCacheDir())
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No cached WordPress plugins.")
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	rows := [][]string{{"plugin", "zip"}}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		slug := entry.Name()
		zipPath := config.PluginCacheZip(slug)
		if _, err := os.Stat(zipPath); err == nil {
			rows = append(rows, []string{slug, zipPath})
		}
	}
	if len(rows) == 1 {
		fmt.Println("No cached WordPress plugins.")
		return 0
	}
	fmt.Println(formatTable(rows))
	return 0
}

func cmdEnvPluginsCacheShow(slug string) int {
	if err := validatePluginSlug(slug); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	zipPath := config.PluginCacheZip(slug)
	status := "missing"
	if _, err := os.Stat(zipPath); err == nil {
		status = "available"
	} else if !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Plugin cache:\n  plugin: %s\n  status: %s\n  zip:    %s\n", slug, status, zipPath)
	return 0
}

func cmdEnvPluginsCacheRemove(slug string) int {
	if err := validatePluginSlug(slug); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	pluginDir := config.PluginCachePluginDir(slug)
	if _, err := os.Stat(pluginDir); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "plugin cache for %s does not exist: %s\n", slug, pluginDir)
			return 1
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.RemoveAll(pluginDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Removed WordPress plugin cache %s from %s\n", slug, pluginDir)
	return 0
}

func projectWordPressPlugins(metadata *projectMetadata, create bool) ([]any, error) {
	if metadata.WordPress.Plugins == nil && create {
		metadata.WordPress.Plugins = []any{}
	}
	return metadata.WordPress.Plugins, nil
}

func wordpressPluginAddValue(opts envPluginAddOptions) any {
	if strings.TrimSpace(opts.Source) == "" && strings.TrimSpace(opts.Note) == "" && !opts.HasInstall && !opts.HasActivate && !opts.HasAutoUpdate {
		return opts.Slug
	}
	pairs := []orderedPair{{Key: "slug", Value: opts.Slug}}
	if strings.TrimSpace(opts.Source) != "" {
		pairs = append(pairs, orderedPair{Key: "source", Value: opts.Source})
	}
	if opts.HasInstall && !opts.Install {
		pairs = append(pairs, orderedPair{Key: "install", Value: false})
	}
	if strings.TrimSpace(opts.Note) != "" {
		pairs = append(pairs, orderedPair{Key: "note", Value: opts.Note})
	}
	if opts.HasActivate && !opts.Activate {
		pairs = append(pairs, orderedPair{Key: "activate", Value: false})
	}
	if opts.HasAutoUpdate && !opts.AutoUpdate {
		pairs = append(pairs, orderedPair{Key: "auto_update", Value: false})
	}
	return orderedObject{Pairs: pairs}
}

func cmdEnvPluginsStatusWithOptions(root string, metadata *projectMetadata, remoteName string) int {
	if strings.TrimSpace(remoteName) == "" {
		cfg, ok := loadEnvConfig(root, metadata)
		if !ok {
			fmt.Fprintln(os.Stderr, "Invalid local project metadata in nf.json.")
			return 1
		}
		return cmdEnvPluginsStatusLocal(cfg, metadata)
	}
	return cmdEnvPluginsStatusRemote(root, metadata, remoteName)
}

func cmdEnvPluginsDiffWithOptions(root string, metadata *projectMetadata, remoteName string) int {
	if strings.TrimSpace(remoteName) == "" {
		cfg, ok := loadEnvConfig(root, metadata)
		if !ok {
			fmt.Fprintln(os.Stderr, "Invalid local project metadata in nf.json.")
			return 1
		}
		return cmdEnvPluginsDiffLocal(cfg, metadata)
	}
	return cmdEnvPluginsDiffRemote(root, metadata, remoteName)
}

func cmdEnvPluginsStatusLocal(cfg envConfig, metadata *projectMetadata) int {
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

func cmdEnvPluginsDiffLocal(cfg envConfig, metadata *projectMetadata) int {
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
	return printWordPressPluginDiff("Plugin diff:", nil, statuses, cfg.RepoRoot)
}

func cmdEnvPluginsStatusRemote(root string, metadata *projectMetadata, remoteName string) int {
	plugins, err := loadWordPressPluginSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(plugins) == 0 {
		fmt.Println("No WordPress plugins configured.")
		return 0
	}
	target, err := resolveEnvRemoteSyncTarget("plugin status", remoteName, metadata)
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
	applyRepoPluginCodeStatus(statuses, root, true)
	fmt.Println("Plugin status:")
	fmt.Printf("  remote:   %s\n", target.RemoteName)
	fmt.Printf("  site:     %s\n", target.SiteID)
	fmt.Printf("  env:      %s\n", target.Env)
	fmt.Printf("  provider: %s\n", target.Provider)
	fmt.Println()
	fmt.Println(formatWordPressPluginStatusTable(statuses))
	return 0
}

func cmdEnvPluginsDiffRemote(root string, metadata *projectMetadata, remoteName string) int {
	plugins, err := loadWordPressPluginSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(plugins) == 0 {
		fmt.Println("No WordPress plugins configured.")
		return 0
	}
	target, err := resolveEnvRemoteSyncTarget("plugin diff", remoteName, metadata)
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
	applyRepoPluginCodeStatus(statuses, root, true)
	return printWordPressPluginDiff("Plugin diff:", &target, statuses, root)
}

func cmdEnvPluginsInstall(cfg envConfig, metadata *projectMetadata) int {
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
	localSources, cleanup, err := prepareLocalPluginInstallSources(cfg, plugins)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, plugin := range plugins {
		if source := localSources[plugin.Slug]; source.SkipReason != "" {
			fmt.Fprintf(os.Stderr, "Skipping WordPress plugin %s: %s\n", plugin.Slug, source.SkipReason)
		}
	}
	runner := envPluginInstaller{cfg: cfg}
	if err := runner.Install(plugins, localSources); err != nil {
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

func cmdEnvPluginsInstallWithOptions(root string, metadata *projectMetadata, opts envPluginInstallOptions) int {
	if strings.TrimSpace(opts.RemoteName) == "" {
		if opts.DryRun {
			fmt.Fprintln(os.Stderr, "plugin install --dry-run requires a remote")
			return 1
		}
		cfg, ok := loadEnvConfig(root, metadata)
		if !ok {
			fmt.Fprintln(os.Stderr, "Invalid local project metadata in nf.json.")
			return 1
		}
		return cmdEnvPluginsInstall(cfg, metadata)
	}
	return cmdEnvPluginsInstallRemote(root, metadata, opts)
}

func cmdEnvPluginsInstallRemote(root string, metadata *projectMetadata, opts envPluginInstallOptions) int {
	plugins, err := loadWordPressPluginSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(plugins) == 0 {
		fmt.Println("No WordPress plugins configured.")
		return 0
	}
	target, err := resolveEnvRemoteSyncTarget("plugin install", opts.RemoteName, metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	remoteTmp := remotePluginInstallTempDir(target)
	repoZips, cleanup, err := prepareRemoteRepoPluginZips(root, plugins, opts.DryRun)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	remotePlugins, uploads, err := remotePluginInstallSpecs(root, plugins, remoteTmp, !opts.DryRun, repoZips)
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
	SourceVersion string
}

type preparedPluginInstallSource struct {
	Path       string
	Version    string
	SkipReason string
}

type remotePluginUpload struct {
	Plugin     wordpressPluginSpec
	LocalPath  string
	RemotePath string
}

type wordpressPluginStatus struct {
	Plugin            wordpressPluginSpec
	Installed         bool
	Active            bool
	AutoUpdate        bool
	Extra             bool
	Code              string
	CodeReason        string
	RemoteFingerprint string
}

type wordpressPluginDiff struct {
	Plugin wordpressPluginSpec
	Change string
	Drift  bool
}

type envPluginInstaller struct {
	cfg envConfig
}

func (i envPluginInstaller) Install(plugins []wordpressPluginSpec, installSources map[string]preparedPluginInstallSource) error {
	envDir := localEnvDir(i.cfg)
	args := envWordpressExecArgs(i.cfg, "sh", "-lc", localPluginInstallScript(plugins, installSources))
	preview := envWordpressExecArgs(i.cfg, "<wp plugin bootstrap script>")
	return runCommandSpecWithPreview(execSpec{Dir: envDir, Args: args}, preview)
}

type envPluginStatusChecker struct {
	cfg envConfig
}

func (c envPluginStatusChecker) statuses(plugins []wordpressPluginSpec, includeExtras bool) ([]wordpressPluginStatus, bool, error) {
	output, err := runCommandSpecOutputSilentFn(execSpec{Dir: localEnvDir(c.cfg), Args: envWordpressExecArgs(c.cfg, "sh", "-lc", localPluginStatusScript(plugins))})
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(output) == "__NF_NOT_READY__" {
		return nil, false, nil
	}
	var statuses []wordpressPluginStatus
	if includeExtras {
		statuses = parseWordPressPluginDiffStatusOutput(plugins, output)
	} else {
		statuses = parseRemotePluginStatusOutput(plugins, output)
	}
	applyRepoPluginCodeStatus(statuses, c.cfg.RepoRoot, false)
	return statuses, true, nil
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

func pluginSourceIsRepo(plugin wordpressPluginSpec) bool {
	return strings.EqualFold(strings.TrimSpace(plugin.Source), wordpressPluginRepoSource)
}

func pluginSourceIsCache(plugin wordpressPluginSpec) bool {
	return strings.EqualFold(strings.TrimSpace(plugin.Source), wordpressPluginCacheSource)
}

func validatePluginSlug(slug string) error {
	slug = strings.TrimSpace(slug)
	if err := project.ValidateName(slug); err != nil {
		return ProjectError{Msg: fmt.Sprintf("plugin slug %q must be one safe directory name", slug)}
	}
	return nil
}

func repoPluginSourceDir(root string, plugin wordpressPluginSpec) (string, error) {
	slug := strings.TrimSpace(plugin.Slug)
	if err := validatePluginSlug(slug); err != nil {
		return "", err
	}
	return filepath.Join(root, "plugins", slug), nil
}

func repoPluginMountsFromMetadata(root string, metadata *projectMetadata) []envPluginMount {
	plugins, err := loadWordPressPluginSpecs(metadata)
	if err != nil {
		return nil
	}
	mounts := []envPluginMount{}
	for _, plugin := range plugins {
		if !pluginSourceIsRepo(plugin) {
			continue
		}
		sourceDir, err := repoPluginSourceDir(root, plugin)
		if err != nil {
			continue
		}
		if info, err := os.Stat(sourceDir); err != nil || !info.IsDir() {
			continue
		}
		mounts = append(mounts, envPluginMount{Slug: plugin.Slug, Host: sourceDir})
	}
	return mounts
}

func envRepoPluginSlugList(cfg envConfig) string {
	slugs := make([]string, 0, len(cfg.RepoPluginMounts))
	for _, mount := range cfg.RepoPluginMounts {
		if strings.TrimSpace(mount.Slug) != "" {
			slugs = append(slugs, mount.Slug)
		}
	}
	sort.Strings(slugs)
	return strings.Join(slugs, " ")
}

func envRepoPluginTarExcludeArgs(cfg envConfig) string {
	slugs := strings.Fields(envRepoPluginSlugList(cfg))
	if len(slugs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(slugs))
	for _, slug := range slugs {
		parts = append(parts, "--exclude="+shellQuoteArg("wp-content/plugins/"+slug))
	}
	return strings.Join(parts, " ")
}

func envTargetOwnedMuPluginTarExcludeArgs() string {
	return "--exclude=" + shellQuoteArg("wp-content/mu-plugins") + " --exclude=" + shellQuoteArg("wp-content/mu-plugins/*")
}

func envMutableWpContentTarExcludeArgs(cfg envConfig) string {
	parts := []string{envRepoPluginTarExcludeArgs(cfg), envTargetOwnedMuPluginTarExcludeArgs()}
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, " ")
}

func prepareLocalPluginInstallSources(cfg envConfig, plugins []wordpressPluginSpec) (map[string]preparedPluginInstallSource, func(), error) {
	sources := map[string]preparedPluginInstallSource{}
	if !hasLocalPreparedPluginSource(plugins) {
		return sources, nil, nil
	}
	outputDir := filepath.Join(cfg.managedTransferDir(), ".nf-plugin-cache")
	if err := os.RemoveAll(outputDir); err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(outputDir) }
	for _, plugin := range plugins {
		if !plugin.Install {
			continue
		}
		if pluginSourceIsRepo(plugin) {
			sourceDir, err := repoPluginSourceDir(cfg.RepoRoot, plugin)
			if err != nil {
				cleanup()
				return nil, nil, err
			}
			info, err := os.Stat(sourceDir)
			if os.IsNotExist(err) {
				sources[plugin.Slug] = preparedPluginInstallSource{SkipReason: "repo source directory does not exist: " + sourceDir}
				continue
			}
			if err != nil {
				cleanup()
				return nil, nil, err
			}
			if !info.IsDir() {
				cleanup()
				return nil, nil, ProjectError{Msg: fmt.Sprintf("repo plugin source directory does not exist: %s", sourceDir)}
			}
			sources[plugin.Slug] = preparedPluginInstallSource{Path: localRepoPluginInstallSourceMark}
			continue
		}
		if pluginSourceIsCache(plugin) {
			cacheZip := config.PluginCacheZip(plugin.Slug)
			info, err := os.Stat(cacheZip)
			if os.IsNotExist(err) {
				sources[plugin.Slug] = preparedPluginInstallSource{SkipReason: "cache does not exist: " + cacheZip}
				continue
			}
			if err != nil {
				cleanup()
				return nil, nil, err
			}
			if info.IsDir() {
				cleanup()
				return nil, nil, ProjectError{Msg: fmt.Sprintf("plugin cache for %s must be a zip file, got directory: %s", plugin.Slug, cacheZip)}
			}
			version, err := pluginZipVersion(cacheZip, plugin.Slug)
			if err != nil {
				cleanup()
				return nil, nil, err
			}
			zipPath := filepath.Join(outputDir, plugin.Slug+".zip")
			if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
				cleanup()
				return nil, nil, err
			}
			if err := copyFile(cacheZip, zipPath); err != nil {
				cleanup()
				return nil, nil, err
			}
			sources[plugin.Slug] = preparedPluginInstallSource{
				Path:    path.Join(cfg.uploadsContainerPath(), ".nf-plugin-cache", plugin.Slug+".zip"),
				Version: version,
			}
		}
	}
	return sources, cleanup, nil
}

func prepareRemoteRepoPluginZips(root string, plugins []wordpressPluginSpec, dryRun bool) (map[string]string, func(), error) {
	zips := map[string]string{}
	if !hasInstallableRepoPlugin(plugins) {
		return zips, nil, nil
	}
	if dryRun {
		for _, plugin := range plugins {
			if !plugin.Install || !pluginSourceIsRepo(plugin) {
				continue
			}
			sourceDir, err := repoPluginSourceDir(root, plugin)
			if err != nil {
				return nil, nil, err
			}
			zips[plugin.Slug] = sourceDir
		}
		return zips, nil, nil
	}
	outputDir, err := os.MkdirTemp("", "nf-plugin-zips-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(outputDir) }
	for _, plugin := range plugins {
		if !plugin.Install || !pluginSourceIsRepo(plugin) {
			continue
		}
		sourceDir, err := repoPluginSourceDir(root, plugin)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		zipPath := filepath.Join(outputDir, plugin.Slug+".zip")
		if _, err := packagePluginSource(sourceDir, zipPath, plugin.Slug); err != nil {
			cleanup()
			return nil, nil, err
		}
		zips[plugin.Slug] = zipPath
	}
	return zips, cleanup, nil
}

func hasInstallableRepoPlugin(plugins []wordpressPluginSpec) bool {
	for _, plugin := range plugins {
		if plugin.Install && pluginSourceIsRepo(plugin) {
			return true
		}
	}
	return false
}

func hasLocalPreparedPluginSource(plugins []wordpressPluginSpec) bool {
	for _, plugin := range plugins {
		if plugin.Install && (pluginSourceIsRepo(plugin) || pluginSourceIsCache(plugin)) {
			return true
		}
	}
	return false
}

func extractPluginTarGz(sourcePath, destinationDir string) error {
	input, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer input.Close()
	gz, err := gzip.NewReader(input)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(header.Name)
		if name == "." || filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(filepath.Separator)) || name == ".." {
			return ProjectError{Msg: fmt.Sprintf("plugin cache archive contains unsafe path: %s", header.Name)}
		}
		target := filepath.Join(destinationDir, name)
		if header.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !header.FileInfo().Mode().IsRegular() {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		output, err := os.Create(target)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, reader)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
}

func packagePluginSource(sourceDir, outputPath, archiveRoot string) (int, error) {
	if err := project.ValidateName(archiveRoot); err != nil {
		return 0, ProjectError{Msg: fmt.Sprintf("plugin archive root %q must be one safe directory name", archiveRoot)}
	}
	files, err := pluginSourceFiles(sourceDir)
	if err != nil {
		return 0, err
	}
	if err := writePluginZip(sourceDir, outputPath, archiveRoot, files); err != nil {
		return 0, err
	}
	return len(files), nil
}

func pluginSourceFiles(sourceDir string) ([]string, error) {
	info, err := os.Stat(sourceDir)
	if err != nil || !info.IsDir() {
		return nil, ProjectError{Msg: fmt.Sprintf("repo plugin source directory does not exist: %s", sourceDir)}
	}
	files := []string{}
	err = filepath.WalkDir(sourceDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func pluginSourceFingerprint(sourceDir string) (string, error) {
	files, err := pluginSourceFiles(sourceDir)
	if err != nil {
		return "", err
	}
	fingerprint := sha256.New()
	for _, file := range files {
		rel, err := filepath.Rel(sourceDir, file)
		if err != nil {
			return "", err
		}
		input, err := os.Open(file)
		if err != nil {
			return "", err
		}
		contentHash := sha256.New()
		_, copyErr := io.Copy(contentHash, input)
		closeErr := input.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		_, _ = io.WriteString(fingerprint, filepath.ToSlash(rel))
		_, _ = fingerprint.Write([]byte{0})
		_, _ = fingerprint.Write(contentHash.Sum(nil))
	}
	return hex.EncodeToString(fingerprint.Sum(nil)), nil
}

func writePluginZip(sourceDir, outputPath, archiveRoot string, files []string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	out, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	for _, file := range files {
		rel, err := filepath.Rel(sourceDir, file)
		if err != nil {
			_ = zw.Close()
			_ = out.Close()
			return err
		}
		writer, err := zw.Create(filepath.ToSlash(filepath.Join(archiveRoot, rel)))
		if err != nil {
			_ = zw.Close()
			_ = out.Close()
			return err
		}
		data, err := os.ReadFile(file)
		if err != nil {
			_ = zw.Close()
			_ = out.Close()
			return err
		}
		if _, err := writer.Write(data); err != nil {
			_ = zw.Close()
			_ = out.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func pluginZipVersion(zipPath, slug string) (string, error) {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", ProjectError{Msg: fmt.Sprintf("plugin cache for %s is not a readable zip: %s", slug, zipPath)}
	}
	defer reader.Close()

	prefix := slug + "/"
	for _, file := range reader.File {
		name := path.Clean(file.Name)
		if file.FileInfo().IsDir() || path.Dir(name) != slug || !strings.HasPrefix(name, prefix) || !strings.EqualFold(path.Ext(name), ".php") {
			continue
		}
		contents, err := file.Open()
		if err != nil {
			return "", err
		}
		header, readErr := io.ReadAll(io.LimitReader(contents, 8192))
		closeErr := contents.Close()
		if readErr != nil {
			return "", readErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		if pluginHeaderValue(header, "Plugin Name") == "" {
			continue
		}
		if version := pluginHeaderValue(header, "Version"); version != "" {
			return version, nil
		}
	}
	return "", ProjectError{Msg: fmt.Sprintf("plugin cache for %s does not declare a Version header: %s", slug, zipPath)}
}

func pluginHeaderValue(header []byte, field string) string {
	for _, line := range strings.Split(string(header), "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "/*#@"))
		key, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), field) {
			continue
		}
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "*/"))
	}
	return ""
}

func remotePluginInstallSpecs(root string, plugins []wordpressPluginSpec, remoteTmp string, requireFiles bool, repoZips map[string]string) ([]remotePluginInstallSpec, []remotePluginUpload, error) {
	remotePlugins := make([]remotePluginInstallSpec, 0, len(plugins))
	uploads := []remotePluginUpload{}
	for _, plugin := range plugins {
		if !plugin.Install {
			continue
		}
		if pluginSourceIsRepo(plugin) {
			localPath := repoZips[plugin.Slug]
			if localPath == "" {
				var err error
				localPath, err = repoPluginSourceDir(root, plugin)
				if err != nil {
					return nil, nil, err
				}
			}
			remotePath := path.Join(remoteTmp, plugin.Slug+".zip")
			uploads = append(uploads, remotePluginUpload{Plugin: plugin, LocalPath: localPath, RemotePath: remotePath})
			remotePlugins = append(remotePlugins, remotePluginInstallSpec{Plugin: plugin, InstallSource: remotePath})
			continue
		}
		if pluginSourceIsCache(plugin) {
			localPath := config.PluginCacheZip(plugin.Slug)
			version := ""
			if requireFiles {
				info, err := os.Stat(localPath)
				if err != nil {
					return nil, nil, ProjectError{Msg: fmt.Sprintf("plugin cache for %s does not exist: %s", plugin.Slug, localPath)}
				}
				if info.IsDir() {
					return nil, nil, ProjectError{Msg: fmt.Sprintf("plugin cache for %s must be a zip file, got directory: %s", plugin.Slug, localPath)}
				}
				version, err = pluginZipVersion(localPath, plugin.Slug)
				if err != nil {
					return nil, nil, err
				}
			}
			remotePath := path.Join(remoteTmp, plugin.Slug+".zip")
			uploads = append(uploads, remotePluginUpload{Plugin: plugin, LocalPath: localPath, RemotePath: remotePath})
			remotePlugins = append(remotePlugins, remotePluginInstallSpec{Plugin: plugin, InstallSource: remotePath, SourceVersion: version})
			continue
		}
		installSource := pluginInstallSource(plugin)
		if remotePluginSourceLooksLocal(plugin, installSource) {
			localPath, err := pluginLocalSourcePath(root, installSource)
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

func pluginLocalSourcePath(root, installSource string) (string, error) {
	if filepath.IsAbs(installSource) || strings.TrimSpace(root) == "" {
		return filepath.Abs(installSource)
	}
	return filepath.Abs(filepath.Join(root, installSource))
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
	rows := [][]string{{"plugin", "source", "install", "activate", "auto-update", "note"}}
	for _, plugin := range plugins {
		activate := "no"
		if plugin.Activate {
			activate = "yes"
		}
		install := "no"
		if plugin.Install {
			install = "yes"
		}
		autoUpdate := "no"
		if plugin.AutoUpdate {
			autoUpdate = "yes"
		}
		if !plugin.Install {
			activate = "-"
			autoUpdate = "-"
		}
		source := plugin.Source
		if source == "" {
			source = "-"
		}
		rows = append(rows, []string{plugin.Slug, source, install, activate, autoUpdate, plugin.Note})
	}
	return formatTable(rows)
}

func formatWordPressPluginStatusTable(statuses []wordpressPluginStatus) string {
	rows := [][]string{{"plugin", "source", "install", "installed", "active", "auto-update", "code", "note"}}
	for _, status := range statuses {
		code := "-"
		if pluginSourceIsRepo(status.Plugin) {
			code = status.Code
		}
		rows = append(rows, []string{status.Plugin.Slug, status.Plugin.Source, yesNo(status.Plugin.Install), yesNo(status.Installed), yesNo(status.Active), yesNo(status.AutoUpdate), code, status.Plugin.Note})
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

func wordpressPluginDiffs(statuses []wordpressPluginStatus, root string) ([]wordpressPluginDiff, bool) {
	diffs := make([]wordpressPluginDiff, 0, len(statuses))
	drift := false
	for _, status := range statuses {
		changes := []string{}
		if status.Extra {
			changes = append(changes, extraPluginDiffChange(status))
		} else if !status.Plugin.Install {
			if !status.Installed {
				changes = append(changes, "manual install required")
			}
		} else if !status.Installed {
			if pluginLocalSourceMissing(status.Plugin, root) {
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
			if pluginSourceIsRepo(status.Plugin) {
				switch status.Code {
				case repoPluginCodeDrifted:
					changes = append(changes, "refresh repo source")
				case repoPluginCodeUnavailable:
					if status.CodeReason == "local-source" {
						changes = append(changes, "source unavailable locally")
					} else {
						changes = append(changes, "repo code unavailable remotely")
					}
				}
			}
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

func pluginLocalSourceMissing(plugin wordpressPluginSpec, root string) bool {
	if pluginSourceIsRepo(plugin) {
		sourceDir, err := repoPluginSourceDir(root, plugin)
		if err != nil {
			return true
		}
		info, err := os.Stat(sourceDir)
		return err != nil || !info.IsDir()
	}
	if pluginSourceIsCache(plugin) {
		info, err := os.Stat(config.PluginCacheZip(plugin.Slug))
		return err != nil || info.IsDir()
	}
	installSource := pluginInstallSource(plugin)
	if !remotePluginSourceLooksLocal(plugin, installSource) {
		return false
	}
	localPath, err := pluginLocalSourcePath(root, installSource)
	if err != nil {
		return true
	}
	info, err := os.Stat(localPath)
	return err != nil || info.IsDir()
}

func printWordPressPluginDiff(title string, target *envRemoteSyncTarget, statuses []wordpressPluginStatus, root string) int {
	diffs, drift := wordpressPluginDiffs(statuses, root)
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
		writePluginInstallScript(&builder, "wp_cmd", plugin, remotePlugin.InstallSource, remotePlugin.SourceVersion, pluginSourceIsRepo(plugin))
	}
	return builder.String()
}

func writePluginInstallScript(builder *strings.Builder, wpCommand string, plugin wordpressPluginSpec, installSource, sourceVersion string, refresh bool) {
	slug := shellQuoteArg(plugin.Slug)
	source := shellQuoteArg(installSource)
	writeInstall := func(force bool, indent string) {
		builder.WriteString(indent)
		builder.WriteString(wpCommand)
		builder.WriteString(" plugin install ")
		builder.WriteString(source)
		if force {
			builder.WriteString(" --force")
		}
		if plugin.Activate {
			builder.WriteString(" --activate")
		}
		builder.WriteString("\n")
	}

	if refresh {
		writeInstall(true, "")
	} else {
		builder.WriteString("if ! ")
		builder.WriteString(wpCommand)
		builder.WriteString(" plugin is-installed ")
		builder.WriteString(slug)
		builder.WriteString("; then\n")
		writeInstall(false, "  ")
		if sourceVersion != "" {
			builder.WriteString("else\n")
			builder.WriteString("  installed_version=$(")
			builder.WriteString(wpCommand)
			builder.WriteString(" plugin get ")
			builder.WriteString(slug)
			builder.WriteString(" --field=version 2>/dev/null || true)\n")
			builder.WriteString("  if [ -n \"$installed_version\" ] && NF_CACHED_PLUGIN_VERSION=")
			builder.WriteString(shellQuoteArg(sourceVersion))
			builder.WriteString(" NF_INSTALLED_PLUGIN_VERSION=\"$installed_version\" ")
			builder.WriteString(wpCommand)
			builder.WriteString(" eval 'exit(version_compare((string) getenv(\"NF_CACHED_PLUGIN_VERSION\"), (string) getenv(\"NF_INSTALLED_PLUGIN_VERSION\"), \">\") ? 0 : 1);' --skip-plugins --skip-themes; then\n")
			writeInstall(true, "    ")
			if plugin.Activate {
				builder.WriteString("  elif ! ")
				builder.WriteString(wpCommand)
				builder.WriteString(" plugin is-active ")
				builder.WriteString(slug)
				builder.WriteString("; then\n")
				builder.WriteString("    ")
				builder.WriteString(wpCommand)
				builder.WriteString(" plugin activate ")
				builder.WriteString(slug)
				builder.WriteString("\n")
			}
			builder.WriteString("  fi\n")
		} else if plugin.Activate {
			builder.WriteString("elif ! ")
			builder.WriteString(wpCommand)
			builder.WriteString(" plugin is-active ")
			builder.WriteString(slug)
			builder.WriteString("; then\n")
			builder.WriteString("  ")
			builder.WriteString(wpCommand)
			builder.WriteString(" plugin activate ")
			builder.WriteString(slug)
			builder.WriteString("\n")
		}
		builder.WriteString("fi\n")
	}

	if plugin.AutoUpdate {
		builder.WriteString("if ! ")
		builder.WriteString(wpCommand)
		builder.WriteString(" plugin auto-updates status ")
		builder.WriteString(slug)
		builder.WriteString(" --enabled-only --field=name | grep -qx ")
		builder.WriteString(slug)
		builder.WriteString("; then\n")
		builder.WriteString("  ")
		builder.WriteString(wpCommand)
		builder.WriteString(" plugin auto-updates enable ")
		builder.WriteString(slug)
		builder.WriteString("\n")
		builder.WriteString("fi\n")
	}
}

func localPluginStatusScript(plugins []wordpressPluginSpec) string {
	var builder strings.Builder
	builder.WriteString("set -eu\n")
	builder.WriteString("if ! wp core is-installed >/dev/null 2>&1; then printf '__NF_NOT_READY__\\n'; exit 0; fi\n")
	for _, plugin := range plugins {
		slug := shellQuoteArg(plugin.Slug)
		builder.WriteString("installed=no active=no auto_update=no\n")
		builder.WriteString("if wp plugin is-installed ")
		builder.WriteString(slug)
		builder.WriteString(" >/dev/null 2>&1; then\n")
		builder.WriteString("  installed=yes\n")
		builder.WriteString("  if wp plugin is-active ")
		builder.WriteString(slug)
		builder.WriteString(" >/dev/null 2>&1; then active=yes; fi\n")
		builder.WriteString("  if wp plugin auto-updates status ")
		builder.WriteString(slug)
		builder.WriteString(" --enabled-only --field=name 2>/dev/null | grep -qx ")
		builder.WriteString(slug)
		builder.WriteString("; then auto_update=yes; fi\n")
		builder.WriteString("fi\n")
		builder.WriteString("printf '%s\\t%s\\t%s\\t%s\\n' ")
		builder.WriteString(slug)
		builder.WriteString(" \"$installed\" \"$active\" \"$auto_update\"\n")
	}
	writeExtraPluginStatusScript(&builder, plugins, "wp", false)
	return builder.String()
}

func localPluginInstallScript(plugins []wordpressPluginSpec, installSources map[string]preparedPluginInstallSource) string {
	var builder strings.Builder
	builder.WriteString("set -eu\n")
	for _, plugin := range plugins {
		if !plugin.Install {
			continue
		}
		slug := shellQuoteArg(plugin.Slug)
		installSource := preparedPluginInstallSource{Path: pluginInstallSource(plugin)}
		if override, ok := installSources[plugin.Slug]; ok {
			installSource = override
		}
		if installSource.SkipReason != "" {
			continue
		}
		if installSource.Path == localRepoPluginInstallSourceMark {
			builder.WriteString("if ! wp plugin is-installed ")
			builder.WriteString(slug)
			builder.WriteString("; then\n")
			builder.WriteString("  printf 'Repo plugin %s is not available in the local WordPress env. Run nf env up to refresh mounts.\\n' ")
			builder.WriteString(slug)
			builder.WriteString(" >&2\n")
			builder.WriteString("  exit 1\n")
			if plugin.Activate {
				builder.WriteString("elif ! wp plugin is-active ")
				builder.WriteString(slug)
				builder.WriteString("; then\n")
				builder.WriteString("  wp plugin activate ")
				builder.WriteString(slug)
				builder.WriteString("\n")
			}
			builder.WriteString("fi\n")
			if plugin.AutoUpdate {
				builder.WriteString("if ! wp plugin auto-updates status ")
				builder.WriteString(slug)
				builder.WriteString(" --enabled-only --field=name 2>/dev/null | grep -qx ")
				builder.WriteString(slug)
				builder.WriteString("; then\n")
				builder.WriteString("  wp plugin auto-updates enable ")
				builder.WriteString(slug)
				builder.WriteString("\n")
				builder.WriteString("fi\n")
			}
			continue
		}
		writePluginInstallScript(&builder, "wp", plugin, installSource.Path, installSource.Version, false)
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
		if pluginSourceIsRepo(plugin) {
			builder.WriteString("repo_fingerprint=unavailable\n")
			builder.WriteString("if [ \"$installed\" = yes ]; then\n")
			builder.WriteString("  repo_fingerprint=$(wp_cmd eval ")
			builder.WriteString(shellQuoteArg(remoteRepoPluginFingerprintPHP(plugin.Slug)))
			builder.WriteString(" --skip-plugins --skip-themes 2>/dev/null || true)\n")
			builder.WriteString("  if [ ${#repo_fingerprint} -ne 64 ]; then repo_fingerprint=unavailable; fi\n")
			builder.WriteString("fi\n")
			builder.WriteString("printf '%s\\t%s\\t%s\\t%s\\trepo:%s\\n' ")
			builder.WriteString(slug)
			builder.WriteString(" \"$installed\" \"$active\" \"$auto_update\" \"$repo_fingerprint\"\n")
		} else {
			builder.WriteString("printf '%s\\t%s\\t%s\\t%s\\n' ")
			builder.WriteString(slug)
			builder.WriteString(" \"$installed\" \"$active\" \"$auto_update\"\n")
		}
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
	builder.WriteString(" plugin list --fields=name,status --format=csv")
	if allowRoot {
		builder.WriteString(" --allow-root")
	}
	builder.WriteString(" 2>/dev/null | while IFS=, read -r slug status _; do\n")
	builder.WriteString("  [ \"$slug\" = \"name\" ] && continue\n")
	builder.WriteString("  [ \"$status\" = \"must-use\" ] && continue\n")
	builder.WriteString("  [ \"$status\" = \"dropin\" ] && continue\n")
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
		if len(fields) != 4 && len(fields) != 5 {
			continue
		}
		status := wordpressPluginStatus{Installed: fields[1] == "yes", Active: fields[2] == "yes", AutoUpdate: fields[3] == "yes"}
		if len(fields) == 5 && strings.HasPrefix(fields[4], "repo:") {
			status.RemoteFingerprint = strings.TrimPrefix(fields[4], "repo:")
		}
		bySlug[fields[0]] = status
	}
	statuses := make([]wordpressPluginStatus, 0, len(plugins))
	for _, plugin := range plugins {
		status := bySlug[plugin.Slug]
		status.Plugin = plugin
		statuses = append(statuses, status)
	}
	return statuses
}

func remoteRepoPluginFingerprintPHP(slug string) string {
	return `$root=WP_PLUGIN_DIR.` + strconv.Quote("/"+slug) + `;try{if(!is_dir($root)){exit(1);}$files=[];$iterator=new RecursiveIteratorIterator(new RecursiveDirectoryIterator($root,FilesystemIterator::SKIP_DOTS));foreach($iterator as $file){$full=$file->getPathname();$rel=str_replace(DIRECTORY_SEPARATOR,"/",substr($full,strlen(rtrim($root,DIRECTORY_SEPARATOR))+1));if($file->isLink()){$files[$rel]=[null,hash("sha256","symlink\0".readlink($full),true)];continue;}if(!$file->isFile()){continue;}$files[$rel]=[$full,null];}ksort($files,SORT_STRING);$hash=hash_init("sha256");foreach($files as $rel=>$entry){$content=$entry[1]??hash_file("sha256",$entry[0],true);if($content===false){exit(1);}hash_update($hash,$rel."\0".$content);}echo hash_final($hash);}catch(Throwable $error){exit(1);}`
}

func applyRepoPluginCodeStatus(statuses []wordpressPluginStatus, root string, remote bool) {
	for i := range statuses {
		status := &statuses[i]
		if status.Extra || !pluginSourceIsRepo(status.Plugin) {
			continue
		}
		sourceDir, err := repoPluginSourceDir(root, status.Plugin)
		if err != nil {
			status.Code = repoPluginCodeUnavailable
			status.CodeReason = "local-source"
			continue
		}
		localFingerprint, err := pluginSourceFingerprint(sourceDir)
		if err != nil {
			status.Code = repoPluginCodeUnavailable
			status.CodeReason = "local-source"
			continue
		}
		if !status.Installed {
			status.Code = repoPluginCodeUnavailable
			status.CodeReason = "not-installed"
			continue
		}
		if !remote {
			status.Code = repoPluginCodeCurrent
			continue
		}
		decoded, err := hex.DecodeString(status.RemoteFingerprint)
		if err != nil || len(decoded) != sha256.Size {
			status.Code = repoPluginCodeUnavailable
			status.CodeReason = "remote-fingerprint"
			continue
		}
		if status.RemoteFingerprint == localFingerprint {
			status.Code = repoPluginCodeCurrent
		} else {
			status.Code = repoPluginCodeDrifted
		}
	}
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

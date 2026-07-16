package cli

// WordPress theme bootstrap commands backed by wordpress.themes in nf.json.

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
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
	wordpressThemeRepoSource        = "repo"
	wordpressThemeCacheSource       = "cache"
	localRepoThemeInstallSourceMark = "__NF_REPO_THEME_MOUNT__"
)

type wordpressThemeSpec struct {
	Slug       string
	Source     string
	Path       string
	AutoUpdate bool
	Note       string
}

type envThemeAddOptions struct {
	Slug       string
	Source     string
	Path       string
	AutoUpdate bool
	Note       string
}

type envThemeCacheOptions struct {
	Command string
	Slug    string
	Source  string
}

type envThemeInstallOptions struct {
	RemoteName string
	DryRun     bool
	Yes        bool
}

type remoteThemeInstallSpec struct {
	Theme         wordpressThemeSpec
	InstallSource string
}

type remoteThemeUpload struct {
	Theme      wordpressThemeSpec
	LocalPath  string
	RemotePath string
}

type wordpressThemeStatus struct {
	Theme      wordpressThemeSpec
	Installed  bool
	Active     bool
	AutoUpdate bool
	Extra      bool
}

type wordpressThemeDiff struct {
	Theme  wordpressThemeSpec
	Change string
	Drift  bool
}

type envThemeInstaller struct {
	cfg envConfig
}

type envThemeStatusChecker struct {
	cfg envConfig
}

func loadWordPressThemeSpecs(metadata *projectMetadata) ([]wordpressThemeSpec, error) {
	return loadWordPressThemeSpecsWithOptions(metadata, false)
}

func loadWordPressThemeSpecsAllowEmpty(metadata *projectMetadata) ([]wordpressThemeSpec, error) {
	return loadWordPressThemeSpecsWithOptions(metadata, true)
}

func loadWordPressThemeSpecsWithOptions(metadata *projectMetadata, allowEmpty bool) ([]wordpressThemeSpec, error) {
	raw := metadata.WordPress.Themes
	if len(raw) == 0 && !allowEmpty {
		return nil, ProjectError{Msg: "nf.json wordpress.themes must include at least one theme"}
	}
	themes := make([]wordpressThemeSpec, 0, len(raw))
	seen := map[string]struct{}{}
	repoCount := 0
	for i, item := range raw {
		theme, err := parseWordPressThemeSpec(i, item)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[theme.Slug]; ok {
			return nil, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.themes contains duplicate slug %q", theme.Slug)}
		}
		seen[theme.Slug] = struct{}{}
		if themeSourceIsRepo(theme) {
			repoCount++
			if repoCount > 1 {
				return nil, ProjectError{Msg: "nf.json wordpress.themes may contain at most one repo theme"}
			}
		}
		themes = append(themes, theme)
	}
	return themes, nil
}

func parseWordPressThemeSpec(index int, value any) (wordpressThemeSpec, error) {
	switch typed := value.(type) {
	case orderedObject:
		return parseWordPressThemeSpec(index, orderedObjectMap(typed))
	case string:
		slug := strings.TrimSpace(typed)
		if slug == "" {
			return wordpressThemeSpec{}, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.themes[%d] must not be empty", index)}
		}
		if err := validateThemeSlug(slug); err != nil {
			return wordpressThemeSpec{}, err
		}
		return wordpressThemeSpec{Slug: slug, Source: "wordpress.org"}, nil
	case map[string]any:
		location := fmt.Sprintf("nf.json wordpress.themes[%d]", index)
		if err := validateProjectObjectFields(location, typed, "slug", "source", "path", "auto_update", "note", "package", "tasks"); err != nil {
			return wordpressThemeSpec{}, err
		}
		slug, err := projectObjectStringField(location, typed, "slug", true)
		if err != nil {
			return wordpressThemeSpec{}, err
		}
		if err := validateThemeSlug(slug); err != nil {
			return wordpressThemeSpec{}, err
		}
		source, err := projectObjectStringField(location, typed, "source", false)
		if err != nil {
			return wordpressThemeSpec{}, err
		}
		if source == "" {
			source = "wordpress.org"
		}
		source = normalizeThemeSource(source)
		sourcePath, err := projectObjectStringField(location, typed, "path", false)
		if err != nil {
			return wordpressThemeSpec{}, err
		}
		autoUpdate := false
		if value, ok := typed["auto_update"]; ok {
			boolValue, ok := value.(bool)
			if !ok {
				return wordpressThemeSpec{}, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.themes[%d].auto_update must be true or false", index)}
			}
			autoUpdate = boolValue
		}
		note, err := projectObjectStringField(location, typed, "note", false)
		if err != nil {
			return wordpressThemeSpec{}, err
		}
		theme := wordpressThemeSpec{Slug: slug, Source: source, Path: sourcePath, AutoUpdate: autoUpdate, Note: note}
		if themeSourceIsRepo(theme) {
			if theme.Path == "" {
				theme.Path = "theme"
			}
			if err := project.ValidateRelativePath(theme.Path); err != nil {
				return wordpressThemeSpec{}, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.themes[%d].path: %s", index, err)}
			}
			theme.AutoUpdate = false
		} else {
			if theme.Path != "" {
				return wordpressThemeSpec{}, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.themes[%d].path is only supported for repo themes", index)}
			}
			if _, ok := typed["package"]; ok {
				return wordpressThemeSpec{}, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.themes[%d].package is only supported for repo themes", index)}
			}
			if _, ok := typed["tasks"]; ok {
				return wordpressThemeSpec{}, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.themes[%d].tasks is only supported for repo themes", index)}
			}
		}
		return theme, nil
	default:
		return wordpressThemeSpec{}, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.themes[%d] must be a string or object", index)}
	}
}

func normalizeThemeSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "", "wordpress.org":
		return "wordpress.org"
	case wordpressThemeRepoSource:
		return wordpressThemeRepoSource
	case wordpressThemeCacheSource:
		return wordpressThemeCacheSource
	default:
		return strings.TrimSpace(source)
	}
}

func cmdEnvThemesList(metadata *projectMetadata) int {
	themes, err := loadWordPressThemeSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(themes) == 0 {
		fmt.Println("No WordPress themes configured.")
		return 0
	}
	fmt.Println(formatWordPressThemeTable(themes))
	return 0
}

func cmdEnvThemesAdd(root string, metadata *projectMetadata, opts envThemeAddOptions) int {
	if err := validateThemeSlug(opts.Slug); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	existing, err := loadWordPressThemeSpecsAllowEmpty(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	opts.Source = normalizeThemeSource(firstNonEmpty(opts.Source, "wordpress.org"))
	newTheme := wordpressThemeSpec{Slug: opts.Slug, Source: opts.Source, Path: strings.TrimSpace(opts.Path), AutoUpdate: opts.AutoUpdate, Note: strings.TrimSpace(opts.Note)}
	if themeSourceIsRepo(newTheme) {
		if newTheme.Path == "" {
			newTheme.Path = "theme"
		}
		newTheme.AutoUpdate = false
		for _, theme := range existing {
			if themeSourceIsRepo(theme) {
				fmt.Fprintf(os.Stderr, "nf.json wordpress.themes already contains repo theme %q; only one repo theme is allowed\n", theme.Slug)
				return 1
			}
		}
		sourceDir, err := repoThemeSourceDir(root, newTheme)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if info, err := os.Stat(sourceDir); err != nil || !info.IsDir() {
			fmt.Fprintf(os.Stderr, "repo theme source directory does not exist: %s\n", sourceDir)
			return 1
		}
	} else if newTheme.Path != "" {
		fmt.Fprintln(os.Stderr, "theme add --path is only supported with --source repo")
		return 1
	}
	for _, theme := range existing {
		if theme.Slug == opts.Slug {
			fmt.Fprintf(os.Stderr, "nf.json wordpress.themes already contains %q\n", opts.Slug)
			return 1
		}
	}
	themes, err := projectWordPressThemes(metadata, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	themes = append(themes, wordpressThemeAddValue(newTheme))
	metadata.WordPress.Themes = themes
	if err := saveProjectMetadata(root, metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Added WordPress theme %s to nf.json.\n", opts.Slug)
	return 0
}

func cmdEnvThemesRemove(root string, metadata *projectMetadata, slug string) int {
	if _, err := loadWordPressThemeSpecs(metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	themes, err := projectWordPressThemes(metadata, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(themes) == 0 {
		fmt.Fprintf(os.Stderr, "nf.json wordpress.themes does not contain %q\n", slug)
		return 1
	}
	kept := make([]any, 0, len(themes))
	removed := false
	for _, item := range themes {
		theme, err := parseWordPressThemeSpec(0, item)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if theme.Slug == slug {
			removed = true
			continue
		}
		kept = append(kept, item)
	}
	if !removed {
		fmt.Fprintf(os.Stderr, "nf.json wordpress.themes does not contain %q\n", slug)
		return 1
	}
	if len(kept) == 0 {
		fmt.Fprintln(os.Stderr, "cannot remove the last configured WordPress theme; nf.json wordpress.themes must include at least one theme")
		return 1
	}
	metadata.WordPress.Themes = kept
	if err := saveProjectMetadata(root, metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Removed WordPress theme %s from nf.json.\n", slug)
	return 0
}

func cmdEnvThemesActivate(root string, metadata *projectMetadata, slug string) int {
	if err := validateThemeSlug(slug); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if _, err := loadWordPressThemeSpecsAllowEmpty(metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	themes, err := projectWordPressThemes(metadata, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(themes) == 0 {
		fmt.Fprintln(os.Stderr, "nf.json wordpress.themes must include at least one theme")
		return 1
	}
	var activated any
	kept := make([]any, 0, len(themes)-1)
	for _, item := range themes {
		theme, err := parseWordPressThemeSpec(0, item)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if theme.Slug == slug {
			activated = item
			continue
		}
		kept = append(kept, item)
	}
	if activated == nil {
		fmt.Fprintf(os.Stderr, "nf.json wordpress.themes does not contain %q\n", slug)
		return 1
	}
	reordered := append([]any{activated}, kept...)
	metadata.WordPress.Themes = reordered
	if err := saveProjectMetadata(root, metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Moved WordPress theme %s to the top of wordpress.themes.\n", slug)
	return 0
}

func cmdEnvThemesCache(cfg envConfig, opts envThemeCacheOptions) int {
	switch opts.Command {
	case "add":
		return cmdEnvThemesCacheAdd(opts.Slug, opts.Source)
	case "save":
		return cmdEnvThemesCacheSave(cfg, opts.Slug)
	case "list":
		return cmdEnvThemesCacheList()
	case "show":
		return cmdEnvThemesCacheShow(opts.Slug)
	default:
		fmt.Fprintln(os.Stderr, "unsupported theme cache command")
		return 1
	}
}

func cmdEnvThemesCacheAdd(slug, sourcePath string) int {
	if err := validateThemeSlug(slug); err != nil {
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
		fmt.Fprintf(os.Stderr, "theme cache source for %s must be a zip file, got directory: %s\n", slug, sourcePath)
		return 1
	}
	destination := config.ThemeCacheZip(slug)
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := copyFile(sourcePath, destination); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Cached WordPress theme %s at %s\n", slug, destination)
	return 0
}

func cmdEnvThemesCacheSave(cfg envConfig, slug string) int {
	if err := validateThemeSlug(slug); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := ensureEnvReadyForSnapshot(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	hostDir := filepath.Join(cfg.managedTransferDir(), ".nf-theme-cache-save")
	if err := os.RemoveAll(hostDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(hostDir) }()
	containerArchive := path.Join(cfg.uploadsContainerPath(), ".nf-theme-cache-save", slug+".tar.gz")
	hostArchive := filepath.Join(hostDir, slug+".tar.gz")
	script := "set -eu\nmkdir -p " + shellQuoteArg(path.Dir(containerArchive)) + "\ntar -C /var/www/html/wp-content/themes -czf " + shellQuoteArg(containerArchive) + " " + shellQuoteArg(slug) + "\n"
	args := envWordpressRootExecArgs(cfg, "sh", "-lc", script)
	preview := envWordpressRootExecArgs(cfg, "<save theme cache archive>")
	if err := runCommandSpecWithPreview(execSpec{Dir: localEnvDir(cfg), Args: args}, preview); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	tmpDir, err := os.MkdirTemp("", "nf-theme-cache-save-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	if err := extractThemeTarGz(hostArchive, tmpDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	destination := config.ThemeCacheZip(slug)
	if _, err := packageThemeCacheSource(filepath.Join(tmpDir, slug), destination, slug); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Cached WordPress theme %s at %s\n", slug, destination)
	return 0
}

func cmdEnvThemesCacheList() int {
	entries, err := os.ReadDir(config.ThemeCacheDir())
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No cached WordPress themes.")
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	rows := [][]string{{"theme", "zip"}}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		slug := entry.Name()
		zipPath := config.ThemeCacheZip(slug)
		if _, err := os.Stat(zipPath); err == nil {
			rows = append(rows, []string{slug, zipPath})
		}
	}
	if len(rows) == 1 {
		fmt.Println("No cached WordPress themes.")
		return 0
	}
	fmt.Println(formatTable(rows))
	return 0
}

func cmdEnvThemesCacheShow(slug string) int {
	if err := validateThemeSlug(slug); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	zipPath := config.ThemeCacheZip(slug)
	status := "missing"
	if _, err := os.Stat(zipPath); err == nil {
		status = "available"
	} else if !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Theme cache:\n  theme:  %s\n  status: %s\n  zip:    %s\n", slug, status, zipPath)
	return 0
}

func projectWordPressThemes(metadata *projectMetadata, create bool) ([]any, error) {
	if metadata.WordPress.Themes == nil && create {
		metadata.WordPress.Themes = []any{}
	}
	return metadata.WordPress.Themes, nil
}

func wordpressThemeAddValue(theme wordpressThemeSpec) any {
	if (theme.Source == "" || themeSourceIsWordPressOrg(theme)) && strings.TrimSpace(theme.Note) == "" && strings.TrimSpace(theme.Path) == "" && !theme.AutoUpdate {
		return theme.Slug
	}
	pairs := []orderedPair{{Key: "slug", Value: theme.Slug}}
	if strings.TrimSpace(theme.Source) != "" {
		pairs = append(pairs, orderedPair{Key: "source", Value: theme.Source})
	}
	if strings.TrimSpace(theme.Path) != "" {
		pairs = append(pairs, orderedPair{Key: "path", Value: theme.Path})
	}
	if strings.TrimSpace(theme.Note) != "" {
		pairs = append(pairs, orderedPair{Key: "note", Value: theme.Note})
	}
	if theme.AutoUpdate {
		pairs = append(pairs, orderedPair{Key: "auto_update", Value: true})
	}
	return orderedObject{Pairs: pairs}
}

func cmdEnvThemesStatusWithOptions(root string, metadata *projectMetadata, remoteName string) int {
	if strings.TrimSpace(remoteName) == "" {
		cfg, ok := loadEnvConfig(root, metadata)
		if !ok {
			fmt.Fprintln(os.Stderr, "Invalid local project metadata in nf.json.")
			return 1
		}
		return cmdEnvThemesStatusLocal(cfg, metadata)
	}
	return cmdEnvThemesStatusRemote(metadata, remoteName)
}

func cmdEnvThemesDiffWithOptions(root string, metadata *projectMetadata, remoteName string) int {
	if strings.TrimSpace(remoteName) == "" {
		cfg, ok := loadEnvConfig(root, metadata)
		if !ok {
			fmt.Fprintln(os.Stderr, "Invalid local project metadata in nf.json.")
			return 1
		}
		return cmdEnvThemesDiffLocal(cfg, metadata)
	}
	return cmdEnvThemesDiffRemote(root, metadata, remoteName)
}

func cmdEnvThemesStatusLocal(cfg envConfig, metadata *projectMetadata) int {
	themes, err := loadWordPressThemeSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(themes) == 0 {
		fmt.Println("No WordPress themes configured.")
		return 0
	}
	checker := envThemeStatusChecker{cfg: cfg}
	statuses, ready, err := checker.statuses(themes, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !ready {
		fmt.Fprintln(os.Stderr, "Local env is not ready. Run nf env up first.")
		return 1
	}
	fmt.Println(formatWordPressThemeStatusTable(statuses))
	return 0
}

func cmdEnvThemesDiffLocal(cfg envConfig, metadata *projectMetadata) int {
	themes, err := loadWordPressThemeSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(themes) == 0 {
		fmt.Println("No WordPress themes configured.")
		return 0
	}
	checker := envThemeStatusChecker{cfg: cfg}
	statuses, ready, err := checker.statuses(themes, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !ready {
		fmt.Fprintln(os.Stderr, "Local env is not ready. Run nf env up first.")
		return 1
	}
	return printWordPressThemeDiff("Theme diff:", nil, statuses, cfg.RepoRoot)
}

func cmdEnvThemesStatusRemote(metadata *projectMetadata, remoteName string) int {
	themes, err := loadWordPressThemeSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(themes) == 0 {
		fmt.Println("No WordPress themes configured.")
		return 0
	}
	target, err := resolveEnvRemoteSyncTarget("theme status", remoteName, metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	output, err := runSSHOutputFn(remoteSSHArgs(target, remoteThemeStatusScript(target, themes)))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	statuses := parseRemoteThemeStatusOutput(themes, string(output))
	fmt.Println("Theme status:")
	fmt.Printf("  remote:   %s\n", target.RemoteName)
	fmt.Printf("  site:     %s\n", target.SiteID)
	fmt.Printf("  env:      %s\n", target.Env)
	fmt.Printf("  provider: %s\n", target.Provider)
	fmt.Println()
	fmt.Println(formatWordPressThemeStatusTable(statuses))
	return 0
}

func cmdEnvThemesDiffRemote(root string, metadata *projectMetadata, remoteName string) int {
	themes, err := loadWordPressThemeSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(themes) == 0 {
		fmt.Println("No WordPress themes configured.")
		return 0
	}
	target, err := resolveEnvRemoteSyncTarget("theme diff", remoteName, metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	output, err := runSSHOutputFn(remoteSSHArgs(target, remoteThemeStatusScript(target, themes)))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	statuses := parseWordPressThemeDiffStatusOutput(themes, string(output))
	return printWordPressThemeDiff("Theme diff:", &target, statuses, root)
}

func cmdEnvThemesInstallWithOptions(root string, metadata *projectMetadata, opts envThemeInstallOptions) int {
	if strings.TrimSpace(opts.RemoteName) == "" {
		if opts.DryRun {
			fmt.Fprintln(os.Stderr, "theme install --dry-run requires a remote")
			return 1
		}
		cfg, ok := loadEnvConfig(root, metadata)
		if !ok {
			fmt.Fprintln(os.Stderr, "Invalid local project metadata in nf.json.")
			return 1
		}
		return cmdEnvThemesInstall(cfg, metadata)
	}
	return cmdEnvThemesInstallRemote(root, metadata, opts)
}

func cmdEnvThemesInstall(cfg envConfig, metadata *projectMetadata) int {
	themes, err := loadWordPressThemeSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(themes) == 0 {
		fmt.Println("No WordPress themes configured.")
		return 0
	}
	cfg.Themes = themes
	if err := ensureEnvReadyForSnapshot(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := ensureEnvThemesInstalledActive(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("WordPress themes installed.")
	return 0
}

func cmdEnvThemesInstallRemote(root string, metadata *projectMetadata, opts envThemeInstallOptions) int {
	themes, err := loadWordPressThemeSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(themes) == 0 {
		fmt.Println("No WordPress themes configured.")
		return 0
	}
	target, err := resolveEnvRemoteSyncTarget("theme install", opts.RemoteName, metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	remoteTmp := remoteThemeInstallTempDir(target)
	repoZips, cleanup, err := prepareRemoteRepoThemeZips(root, themes, opts.DryRun)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	remoteThemes, uploads, err := remoteThemeInstallSpecs(root, themes, remoteTmp, !opts.DryRun, repoZips)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Theme install plan:")
	fmt.Printf("  remote:        %s\n", target.RemoteName)
	fmt.Printf("  site:          %s\n", target.SiteID)
	fmt.Printf("  env:           %s\n", target.Env)
	fmt.Printf("  provider:      %s\n", target.Provider)
	if target.URL != "" {
		fmt.Printf("  url:           %s\n", target.URL)
	}
	mode := "execute"
	if opts.DryRun {
		mode = "dry-run"
	}
	fmt.Printf("  mode:          %s\n", mode)
	if len(uploads) > 0 {
		fmt.Printf("  uploads:       %d local theme zip(s)\n", len(uploads))
	}
	fmt.Println()
	fmt.Println(formatWordPressThemeTable(themes))
	if len(uploads) > 0 {
		fmt.Println()
		fmt.Println("Local theme sources will be uploaded before install:")
		for _, upload := range uploads {
			fmt.Printf("  %s -> %s\n", upload.Theme.Slug, upload.RemotePath)
		}
	}
	if opts.DryRun {
		fmt.Println("No remote themes were changed.")
		return 0
	}
	if !opts.Yes {
		message := fmt.Sprintf("Install configured WordPress themes on %s:%s (%s)?", target.SiteID, target.Env, target.RemoteName)
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
		if err := uploadRemoteThemeSources(target, remoteTmp, uploads); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	script := remoteThemeInstallScript(target, remoteThemes, activeWordPressThemeSlug(themes))
	printRemoteThemeInstallCommand(target)
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
	fmt.Println("Remote WordPress themes installed.")
	return 0
}

func ensureEnvThemesInstalledActive(cfg envConfig) error {
	if len(cfg.Themes) == 0 {
		return nil
	}
	localSources, cleanup, err := prepareLocalThemeInstallSources(cfg, cfg.Themes)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return err
	}
	runner := envThemeInstaller{cfg: cfg}
	return runner.Install(cfg.Themes, localSources, activeWordPressThemeSlug(cfg.Themes))
}

func (i envThemeInstaller) Install(themes []wordpressThemeSpec, installSources map[string]string, activeSlug string) error {
	envDir := localEnvDir(i.cfg)
	args := envWordpressExecArgs(i.cfg, "sh", "-lc", localThemeInstallScript(themes, installSources, activeSlug))
	preview := envWordpressExecArgs(i.cfg, "<wp theme bootstrap script>")
	return runCommandSpecWithPreview(execSpec{Dir: envDir, Args: args}, preview)
}

func (c envThemeStatusChecker) statuses(themes []wordpressThemeSpec, includeExtras bool) ([]wordpressThemeStatus, bool, error) {
	output, err := runCommandSpecOutputSilentFn(execSpec{Dir: localEnvDir(c.cfg), Args: envWordpressExecArgs(c.cfg, "sh", "-lc", localThemeStatusScript(themes))})
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(output) == "__NF_NOT_READY__" {
		return nil, false, nil
	}
	if includeExtras {
		return parseWordPressThemeDiffStatusOutput(themes, output), true, nil
	}
	return parseRemoteThemeStatusOutput(themes, output), true, nil
}

func activeWordPressThemeSlug(themes []wordpressThemeSpec) string {
	if len(themes) == 0 {
		return ""
	}
	return strings.TrimSpace(themes[0].Slug)
}

func activeEnvThemeSlug(cfg envConfig) string {
	if active := activeWordPressThemeSlug(cfg.Themes); active != "" {
		return active
	}
	return firstNonEmpty(cfg.ThemeSlug, cfg.ProjectSlug, cfg.ThemeMountSlug, "theme")
}

func repoWordPressThemeSpec(themes []wordpressThemeSpec) (wordpressThemeSpec, bool) {
	for _, theme := range themes {
		if themeSourceIsRepo(theme) {
			return theme, true
		}
	}
	return wordpressThemeSpec{}, false
}

func projectRepoTheme(root string, metadata *projectMetadata, commandName string, requireSource bool) (wordpressThemeSpec, string, []wordpressThemeSpec, error) {
	themes, err := loadWordPressThemeSpecs(metadata)
	if err != nil {
		return wordpressThemeSpec{}, "", nil, err
	}
	repoTheme, ok := repoWordPressThemeSpec(themes)
	if !ok {
		return wordpressThemeSpec{}, "", themes, ProjectError{Msg: fmt.Sprintf("%s requires one wordpress.themes entry with source %q", commandName, wordpressThemeRepoSource)}
	}
	sourceDir, err := repoThemeSourceDir(root, repoTheme)
	if err != nil {
		return wordpressThemeSpec{}, "", themes, err
	}
	if requireSource {
		info, err := os.Stat(sourceDir)
		if err != nil || !info.IsDir() {
			return wordpressThemeSpec{}, "", themes, ProjectError{Msg: fmt.Sprintf("Theme source directory does not exist: %s", sourceDir)}
		}
	}
	return repoTheme, sourceDir, themes, nil
}

func themeInstallSource(theme wordpressThemeSpec) string {
	source := strings.TrimSpace(os.ExpandEnv(theme.Source))
	if source == "" || strings.EqualFold(source, "wordpress.org") {
		return theme.Slug
	}
	return source
}

func themeSourceIsRepo(theme wordpressThemeSpec) bool {
	return strings.EqualFold(strings.TrimSpace(theme.Source), wordpressThemeRepoSource)
}

func themeSourceIsCache(theme wordpressThemeSpec) bool {
	return strings.EqualFold(strings.TrimSpace(theme.Source), wordpressThemeCacheSource)
}

func themeSourceIsWordPressOrg(theme wordpressThemeSpec) bool {
	source := strings.TrimSpace(theme.Source)
	return source == "" || strings.EqualFold(source, "wordpress.org")
}

func themeAutoUpdateManaged(theme wordpressThemeSpec) bool {
	return theme.AutoUpdate && !themeSourceIsRepo(theme)
}

func validateThemeSlug(slug string) error {
	slug = strings.TrimSpace(slug)
	if err := project.ValidateName(slug); err != nil {
		return ProjectError{Msg: fmt.Sprintf("theme slug %q must be one safe directory name", slug)}
	}
	return nil
}

func repoThemeSourceDir(root string, theme wordpressThemeSpec) (string, error) {
	if err := validateThemeSlug(theme.Slug); err != nil {
		return "", err
	}
	sourcePath := firstNonEmpty(theme.Path, "theme")
	if err := project.ValidateRelativePath(sourcePath); err != nil {
		return "", ProjectError{Msg: fmt.Sprintf("repo theme path %q: %s", sourcePath, err)}
	}
	if strings.TrimSpace(root) == "" {
		return "", ProjectError{Msg: "repo theme path requires a project root"}
	}
	return filepath.Join(root, sourcePath), nil
}

func repoThemeMountsFromMetadata(root string, metadata *projectMetadata) []envThemeMount {
	themes, err := loadWordPressThemeSpecs(metadata)
	if err != nil {
		return nil
	}
	mounts := []envThemeMount{}
	for _, theme := range themes {
		if !themeSourceIsRepo(theme) {
			continue
		}
		sourceDir, err := repoThemeSourceDir(root, theme)
		if err != nil {
			continue
		}
		if info, err := os.Stat(sourceDir); err != nil || !info.IsDir() {
			continue
		}
		mounts = append(mounts, envThemeMount{Slug: theme.Slug, Host: sourceDir})
	}
	return mounts
}

func prepareLocalThemeInstallSources(cfg envConfig, themes []wordpressThemeSpec) (map[string]string, func(), error) {
	sources := map[string]string{}
	if !hasLocalPreparedThemeSource(themes) {
		return sources, nil, nil
	}
	outputDir := filepath.Join(cfg.managedTransferDir(), ".nf-theme-cache")
	if err := os.RemoveAll(outputDir); err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(outputDir) }
	for _, theme := range themes {
		if themeSourceIsRepo(theme) {
			sourceDir, err := repoThemeSourceDir(cfg.RepoRoot, theme)
			if err != nil {
				cleanup()
				return nil, nil, err
			}
			info, err := os.Stat(sourceDir)
			if err != nil || !info.IsDir() {
				cleanup()
				return nil, nil, ProjectError{Msg: fmt.Sprintf("repo theme source directory does not exist: %s", sourceDir)}
			}
			sources[theme.Slug] = localRepoThemeInstallSourceMark
			continue
		}
		installSource := themeInstallSource(theme)
		if themeSourceIsCache(theme) {
			installSource = config.ThemeCacheZip(theme.Slug)
		}
		if !themeSourceIsCache(theme) && !remoteThemeSourceLooksLocal(theme, installSource) {
			continue
		}
		localPath, err := themeLocalSourcePath(cfg.RepoRoot, installSource)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		info, err := os.Stat(localPath)
		if err != nil {
			cleanup()
			if themeSourceIsCache(theme) {
				return nil, nil, ProjectError{Msg: fmt.Sprintf("theme cache for %s does not exist: %s", theme.Slug, localPath)}
			}
			return nil, nil, ProjectError{Msg: fmt.Sprintf("theme source for %s does not exist: %s", theme.Slug, localPath)}
		}
		if info.IsDir() {
			cleanup()
			return nil, nil, ProjectError{Msg: fmt.Sprintf("theme source for %s must be a zip file, got directory: %s", theme.Slug, localPath)}
		}
		zipName := filepath.Base(localPath)
		if themeSourceIsCache(theme) {
			zipName = theme.Slug + ".zip"
		}
		zipPath := filepath.Join(outputDir, zipName)
		if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
			cleanup()
			return nil, nil, err
		}
		if err := copyFile(localPath, zipPath); err != nil {
			cleanup()
			return nil, nil, err
		}
		sources[theme.Slug] = path.Join(cfg.uploadsContainerPath(), ".nf-theme-cache", zipName)
	}
	return sources, cleanup, nil
}

func hasLocalPreparedThemeSource(themes []wordpressThemeSpec) bool {
	for _, theme := range themes {
		installSource := themeInstallSource(theme)
		if themeSourceIsRepo(theme) || themeSourceIsCache(theme) || remoteThemeSourceLooksLocal(theme, installSource) {
			return true
		}
	}
	return false
}

func prepareRemoteRepoThemeZips(root string, themes []wordpressThemeSpec, dryRun bool) (map[string]string, func(), error) {
	zips := map[string]string{}
	if !hasRepoTheme(themes) {
		return zips, nil, nil
	}
	if dryRun {
		for _, theme := range themes {
			if !themeSourceIsRepo(theme) {
				continue
			}
			sourceDir, err := repoThemeSourceDir(root, theme)
			if err != nil {
				return nil, nil, err
			}
			zips[theme.Slug] = sourceDir
		}
		return zips, nil, nil
	}
	outputDir, err := os.MkdirTemp("", "nf-theme-zips-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(outputDir) }
	for _, theme := range themes {
		if !themeSourceIsRepo(theme) {
			continue
		}
		sourceDir, err := repoThemeSourceDir(root, theme)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		zipPath := filepath.Join(outputDir, theme.Slug+".zip")
		if _, err := packageThemeCacheSource(sourceDir, zipPath, theme.Slug); err != nil {
			cleanup()
			return nil, nil, err
		}
		zips[theme.Slug] = zipPath
	}
	return zips, cleanup, nil
}

func remoteThemeInstallSpecs(root string, themes []wordpressThemeSpec, remoteTmp string, requireFiles bool, repoZips map[string]string) ([]remoteThemeInstallSpec, []remoteThemeUpload, error) {
	remoteThemes := make([]remoteThemeInstallSpec, 0, len(themes))
	uploads := []remoteThemeUpload{}
	for _, theme := range themes {
		if themeSourceIsRepo(theme) {
			localPath := repoZips[theme.Slug]
			if localPath == "" {
				var err error
				localPath, err = repoThemeSourceDir(root, theme)
				if err != nil {
					return nil, nil, err
				}
			}
			remotePath := path.Join(remoteTmp, theme.Slug+".zip")
			uploads = append(uploads, remoteThemeUpload{Theme: theme, LocalPath: localPath, RemotePath: remotePath})
			remoteThemes = append(remoteThemes, remoteThemeInstallSpec{Theme: theme, InstallSource: remotePath})
			continue
		}
		if themeSourceIsCache(theme) {
			localPath := config.ThemeCacheZip(theme.Slug)
			if requireFiles {
				info, err := os.Stat(localPath)
				if err != nil {
					return nil, nil, ProjectError{Msg: fmt.Sprintf("theme cache for %s does not exist: %s", theme.Slug, localPath)}
				}
				if info.IsDir() {
					return nil, nil, ProjectError{Msg: fmt.Sprintf("theme cache for %s must be a zip file, got directory: %s", theme.Slug, localPath)}
				}
			}
			remotePath := path.Join(remoteTmp, theme.Slug+".zip")
			uploads = append(uploads, remoteThemeUpload{Theme: theme, LocalPath: localPath, RemotePath: remotePath})
			remoteThemes = append(remoteThemes, remoteThemeInstallSpec{Theme: theme, InstallSource: remotePath})
			continue
		}
		installSource := themeInstallSource(theme)
		if remoteThemeSourceLooksLocal(theme, installSource) {
			localPath, err := themeLocalSourcePath(root, installSource)
			if err != nil {
				return nil, nil, err
			}
			if requireFiles {
				info, err := os.Stat(localPath)
				if err != nil {
					return nil, nil, ProjectError{Msg: fmt.Sprintf("remote theme source for %s does not exist: %s", theme.Slug, localPath)}
				}
				if info.IsDir() {
					return nil, nil, ProjectError{Msg: fmt.Sprintf("remote theme source for %s must be a zip file, got directory: %s", theme.Slug, localPath)}
				}
			}
			remotePath := path.Join(remoteTmp, filepath.Base(localPath))
			uploads = append(uploads, remoteThemeUpload{Theme: theme, LocalPath: localPath, RemotePath: remotePath})
			remoteThemes = append(remoteThemes, remoteThemeInstallSpec{Theme: theme, InstallSource: remotePath})
			continue
		}
		remoteThemes = append(remoteThemes, remoteThemeInstallSpec{Theme: theme, InstallSource: installSource})
	}
	return remoteThemes, uploads, nil
}

func filterNonRepoThemes(themes []wordpressThemeSpec) []wordpressThemeSpec {
	filtered := make([]wordpressThemeSpec, 0, len(themes))
	for _, theme := range themes {
		if !themeSourceIsRepo(theme) {
			filtered = append(filtered, theme)
		}
	}
	return filtered
}

func hasRepoTheme(themes []wordpressThemeSpec) bool {
	for _, theme := range themes {
		if themeSourceIsRepo(theme) {
			return true
		}
	}
	return false
}

func themeLocalSourcePath(root, installSource string) (string, error) {
	if filepath.IsAbs(installSource) || strings.TrimSpace(root) == "" {
		return filepath.Abs(installSource)
	}
	return filepath.Abs(filepath.Join(root, installSource))
}

func remoteThemeSourceLooksLocal(theme wordpressThemeSpec, installSource string) bool {
	if installSource == "" || installSource == theme.Slug || strings.EqualFold(installSource, "wordpress.org") {
		return false
	}
	lower := strings.ToLower(installSource)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.Contains(lower, "://") {
		return false
	}
	return filepath.IsAbs(installSource) || strings.HasPrefix(installSource, ".") || strings.ContainsAny(installSource, `/\`) || strings.HasSuffix(lower, ".zip")
}

func remoteThemeInstallTempDir(target envRemoteSyncTarget) string {
	return path.Join("/tmp", "nf-themes-"+cleanEnvSlug(target.SiteID+"-"+target.Env)+"-"+strconv.FormatInt(time.Now().Unix(), 10))
}

func remoteThemeUploadArgs(target envRemoteSyncTarget, upload remoteThemeUpload) []string {
	return []string{"rsync", "-az", "-e", "ssh -p " + target.SSHPort, upload.LocalPath, target.SSHUser + "@" + target.SSHHost + ":" + upload.RemotePath}
}

func uploadRemoteThemeSources(target envRemoteSyncTarget, remoteTmp string, uploads []remoteThemeUpload) error {
	mkdirArgs := remoteSSHArgs(target, "rm -rf "+shellQuoteArg(remoteTmp)+" && mkdir -p "+shellQuoteArg(remoteTmp))
	printCommandArgs(mkdirArgs)
	if err := runSSHCommandFn(mkdirArgs); err != nil {
		return err
	}
	for _, upload := range uploads {
		uploadArgs := remoteThemeUploadArgs(target, upload)
		printCommandArgs(uploadArgs)
		if err := runRsyncCommandFn(uploadArgs); err != nil {
			_ = runSSHCommandFn(remoteSSHArgs(target, "rm -rf "+shellQuoteArg(remoteTmp)))
			return err
		}
	}
	return nil
}

func formatWordPressThemeTable(themes []wordpressThemeSpec) string {
	rows := [][]string{{"theme", "source", "active", "auto-update", "path", "note"}}
	activeSlug := activeWordPressThemeSlug(themes)
	for _, theme := range themes {
		source := firstNonEmpty(theme.Source, "wordpress.org")
		autoUpdate := "no"
		if themeSourceIsRepo(theme) {
			autoUpdate = "-"
		} else if theme.AutoUpdate {
			autoUpdate = "yes"
		}
		rows = append(rows, []string{theme.Slug, source, yesNo(theme.Slug == activeSlug), autoUpdate, theme.Path, theme.Note})
	}
	return formatTable(rows)
}

func formatWordPressThemeStatusTable(statuses []wordpressThemeStatus) string {
	rows := [][]string{{"theme", "source", "expected-active", "installed", "active", "auto-update", "path", "note"}}
	activeSlug := ""
	if len(statuses) > 0 {
		activeSlug = statuses[0].Theme.Slug
	}
	for _, status := range statuses {
		rows = append(rows, []string{status.Theme.Slug, firstNonEmpty(status.Theme.Source, "wordpress.org"), yesNo(status.Theme.Slug == activeSlug), yesNo(status.Installed), yesNo(status.Active), yesNo(status.AutoUpdate), status.Theme.Path, status.Theme.Note})
	}
	return formatTable(rows)
}

func formatWordPressThemeDiffTable(diffs []wordpressThemeDiff) string {
	rows := [][]string{{"theme", "change"}}
	for _, diff := range diffs {
		rows = append(rows, []string{diff.Theme.Slug, diff.Change})
	}
	return formatTable(rows)
}

func wordpressThemeDiffs(statuses []wordpressThemeStatus, root string) ([]wordpressThemeDiff, bool) {
	diffs := make([]wordpressThemeDiff, 0, len(statuses))
	drift := false
	for i, status := range statuses {
		changes := []string{}
		expectedActive := i == 0
		if status.Extra {
			changes = append(changes, extraThemeDiffChange(status))
			diffs = append(diffs, wordpressThemeDiff{Theme: status.Theme, Change: strings.Join(changes, ", "), Drift: true})
			drift = true
			continue
		}
		if !status.Installed {
			if themeLocalSourceMissing(status.Theme, root) {
				changes = append(changes, "source unavailable locally")
			} else {
				changes = append(changes, "install")
				if expectedActive {
					changes = append(changes, "activate")
				}
				if themeAutoUpdateManaged(status.Theme) {
					changes = append(changes, "enable auto-update")
				}
			}
		} else {
			if expectedActive && !status.Active {
				changes = append(changes, "activate")
			}
			if themeAutoUpdateManaged(status.Theme) && !status.AutoUpdate {
				changes = append(changes, "enable auto-update")
			}
		}
		change := "ok"
		if len(changes) > 0 {
			change = strings.Join(changes, ", ")
			drift = true
		}
		diffs = append(diffs, wordpressThemeDiff{Theme: status.Theme, Change: change, Drift: len(changes) > 0})
	}
	return diffs, drift
}

func extraThemeDiffChange(status wordpressThemeStatus) string {
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

func themeLocalSourceMissing(theme wordpressThemeSpec, root string) bool {
	if themeSourceIsRepo(theme) {
		sourceDir, err := repoThemeSourceDir(root, theme)
		if err != nil {
			return true
		}
		info, err := os.Stat(sourceDir)
		return err != nil || !info.IsDir()
	}
	if themeSourceIsCache(theme) {
		info, err := os.Stat(config.ThemeCacheZip(theme.Slug))
		return err != nil || info.IsDir()
	}
	installSource := themeInstallSource(theme)
	if !remoteThemeSourceLooksLocal(theme, installSource) {
		return false
	}
	localPath, err := themeLocalSourcePath(root, installSource)
	if err != nil {
		return true
	}
	info, err := os.Stat(localPath)
	return err != nil || info.IsDir()
}

func printWordPressThemeDiff(title string, target *envRemoteSyncTarget, statuses []wordpressThemeStatus, root string) int {
	diffs, drift := wordpressThemeDiffs(statuses, root)
	fmt.Println(title)
	if target != nil {
		fmt.Printf("  remote:   %s\n", target.RemoteName)
		fmt.Printf("  site:     %s\n", target.SiteID)
		fmt.Printf("  env:      %s\n", target.Env)
		fmt.Printf("  provider: %s\n", target.Provider)
	}
	fmt.Println()
	fmt.Println(formatWordPressThemeDiffTable(diffs))
	if drift {
		return 2
	}
	return 0
}

func localThemeInstallScript(themes []wordpressThemeSpec, installSources map[string]string, activeSlug string) string {
	var builder strings.Builder
	builder.WriteString("set -eu\n")
	for _, theme := range themes {
		slug := shellQuoteArg(theme.Slug)
		installSource := themeInstallSource(theme)
		if override := installSources[theme.Slug]; override != "" {
			installSource = override
		}
		if installSource == localRepoThemeInstallSourceMark {
			builder.WriteString("if ! wp theme is-installed ")
			builder.WriteString(slug)
			builder.WriteString("; then\n")
			builder.WriteString("  printf 'Repo theme %s is not available in the local WordPress env. Run nf env up to refresh mounts.\\n' ")
			builder.WriteString(slug)
			builder.WriteString(" >&2\n")
			builder.WriteString("  exit 1\n")
			builder.WriteString("fi\n")
			continue
		}
		source := shellQuoteArg(installSource)
		builder.WriteString("if ! wp theme is-installed ")
		builder.WriteString(slug)
		builder.WriteString("; then\n")
		builder.WriteString("  wp theme install ")
		builder.WriteString(source)
		builder.WriteString("\n")
		builder.WriteString("fi\n")
		if themeAutoUpdateManaged(theme) {
			writeThemeAutoUpdateScript(&builder, "wp", slug, false)
		}
	}
	if strings.TrimSpace(activeSlug) != "" {
		slug := shellQuoteArg(activeSlug)
		builder.WriteString("if ! wp theme is-active ")
		builder.WriteString(slug)
		builder.WriteString("; then\n")
		builder.WriteString("  wp theme activate ")
		builder.WriteString(slug)
		builder.WriteString("\n")
		builder.WriteString("  ")
		builder.WriteString(rewriteFlushShellStep(wpRewriteFlushCommand("wp"), "Failed to flush WordPress rewrite rules in the local environment"))
		builder.WriteString("\n")
		builder.WriteString("fi\n")
	}
	return builder.String()
}

func remoteThemeInstallScript(target envRemoteSyncTarget, themes []remoteThemeInstallSpec, activeSlug string) string {
	var builder strings.Builder
	builder.WriteString("set -eu\n")
	builder.WriteString("cd ")
	builder.WriteString(shellQuoteArg(target.WordPressPath))
	builder.WriteString("\nwp_cmd() { ")
	builder.WriteString(target.WPCommand)
	builder.WriteString(" --path=")
	builder.WriteString(shellQuoteArg(target.WordPressPath))
	builder.WriteString(" \"$@\"; }\n")
	for _, remoteTheme := range themes {
		theme := remoteTheme.Theme
		slug := shellQuoteArg(theme.Slug)
		source := shellQuoteArg(remoteTheme.InstallSource)
		builder.WriteString("if ! wp_cmd theme is-installed ")
		builder.WriteString(slug)
		builder.WriteString("; then\n")
		builder.WriteString("  wp_cmd theme install ")
		builder.WriteString(source)
		builder.WriteString("\n")
		builder.WriteString("fi\n")
		if themeAutoUpdateManaged(theme) {
			writeThemeAutoUpdateScript(&builder, "wp_cmd", slug, false)
		}
	}
	if strings.TrimSpace(activeSlug) != "" {
		slug := shellQuoteArg(activeSlug)
		builder.WriteString("if ! wp_cmd theme is-active ")
		builder.WriteString(slug)
		builder.WriteString("; then\n")
		builder.WriteString("  wp_cmd theme activate ")
		builder.WriteString(slug)
		builder.WriteString("\n")
		builder.WriteString("  ")
		builder.WriteString(rewriteFlushShellStep(wpRewriteFlushCommand("wp_cmd"), fmt.Sprintf("Failed to flush WordPress rewrite rules on %s", target.Env)))
		builder.WriteString("\n")
		builder.WriteString("fi\n")
	}
	return builder.String()
}

func writeThemeAutoUpdateScript(builder *strings.Builder, wpCommand, slug string, allowRoot bool) {
	builder.WriteString("if ! ")
	builder.WriteString(wpCommand)
	builder.WriteString(" theme auto-updates status ")
	builder.WriteString(slug)
	builder.WriteString(" --enabled-only --field=name")
	if allowRoot {
		builder.WriteString(" --allow-root")
	}
	builder.WriteString(" 2>/dev/null | grep -qx ")
	builder.WriteString(slug)
	builder.WriteString("; then\n")
	builder.WriteString("  ")
	builder.WriteString(wpCommand)
	builder.WriteString(" theme auto-updates enable ")
	builder.WriteString(slug)
	if allowRoot {
		builder.WriteString(" --allow-root")
	}
	builder.WriteString("\n")
	builder.WriteString("fi\n")
}

func localThemeStatusScript(themes []wordpressThemeSpec) string {
	var builder strings.Builder
	builder.WriteString("set -eu\n")
	builder.WriteString("if ! wp core is-installed >/dev/null 2>&1; then printf '__NF_NOT_READY__\\n'; exit 0; fi\n")
	writeThemeStatusScript(&builder, themes, "wp")
	return builder.String()
}

func remoteThemeStatusScript(target envRemoteSyncTarget, themes []wordpressThemeSpec) string {
	var builder strings.Builder
	builder.WriteString("set -eu\n")
	builder.WriteString("cd ")
	builder.WriteString(shellQuoteArg(target.WordPressPath))
	builder.WriteString("\nwp_cmd() { ")
	builder.WriteString(target.WPCommand)
	builder.WriteString(" --path=")
	builder.WriteString(shellQuoteArg(target.WordPressPath))
	builder.WriteString(" \"$@\"; }\n")
	writeThemeStatusScript(&builder, themes, "wp_cmd")
	return builder.String()
}

func writeThemeStatusScript(builder *strings.Builder, themes []wordpressThemeSpec, wpCommand string) {
	for _, theme := range themes {
		slug := shellQuoteArg(theme.Slug)
		builder.WriteString("installed=no active=no auto_update=no\n")
		builder.WriteString("if ")
		builder.WriteString(wpCommand)
		builder.WriteString(" theme is-installed ")
		builder.WriteString(slug)
		builder.WriteString(" >/dev/null 2>&1; then\n")
		builder.WriteString("  installed=yes\n")
		builder.WriteString("  if ")
		builder.WriteString(wpCommand)
		builder.WriteString(" theme is-active ")
		builder.WriteString(slug)
		builder.WriteString(" >/dev/null 2>&1; then active=yes; fi\n")
		builder.WriteString("  if ")
		builder.WriteString(wpCommand)
		builder.WriteString(" theme auto-updates status ")
		builder.WriteString(slug)
		builder.WriteString(" --enabled-only --field=name 2>/dev/null | grep -qx ")
		builder.WriteString(slug)
		builder.WriteString("; then auto_update=yes; fi\n")
		builder.WriteString("fi\n")
		builder.WriteString("printf '%s\\t%s\\t%s\\t%s\\n' ")
		builder.WriteString(slug)
		builder.WriteString(" \"$installed\" \"$active\" \"$auto_update\"\n")
	}
	writeExtraThemeStatusScript(builder, themes, wpCommand)
}

func writeExtraThemeStatusScript(builder *strings.Builder, themes []wordpressThemeSpec, wpCommand string) {
	configured := strings.Builder{}
	for _, theme := range themes {
		configured.WriteString(" ")
		configured.WriteString(theme.Slug)
		configured.WriteString(" ")
	}
	builder.WriteString("configured_themes=")
	builder.WriteString(shellQuoteArg(configured.String()))
	builder.WriteString("\n")
	builder.WriteString(wpCommand)
	builder.WriteString(" theme list --fields=name,status --format=csv 2>/dev/null | while IFS=, read -r slug status _; do\n")
	builder.WriteString("  [ \"$slug\" = \"name\" ] && continue\n")
	builder.WriteString("  case \"$configured_themes\" in *\" $slug \"*) continue ;; esac\n")
	builder.WriteString("  active=no auto_update=no\n")
	builder.WriteString("  if [ \"$status\" = \"active\" ]; then active=yes; fi\n")
	builder.WriteString("  if ")
	builder.WriteString(wpCommand)
	builder.WriteString(" theme auto-updates status \"$slug\" --enabled-only --field=name 2>/dev/null | grep -qx \"$slug\"; then auto_update=yes; fi\n")
	builder.WriteString("  printf '%s\\t%s\\t%s\\t%s\\t%s\\n' \"$slug\" yes \"$active\" \"$auto_update\" extra\n")
	builder.WriteString("done\n")
}

func parseRemoteThemeStatusOutput(themes []wordpressThemeSpec, output string) []wordpressThemeStatus {
	bySlug := map[string]wordpressThemeStatus{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) != 4 {
			continue
		}
		bySlug[fields[0]] = wordpressThemeStatus{Installed: fields[1] == "yes", Active: fields[2] == "yes", AutoUpdate: fields[3] == "yes"}
	}
	statuses := make([]wordpressThemeStatus, 0, len(themes))
	for _, theme := range themes {
		status := bySlug[theme.Slug]
		status.Theme = theme
		statuses = append(statuses, status)
	}
	return statuses
}

func parseWordPressThemeDiffStatusOutput(themes []wordpressThemeSpec, output string) []wordpressThemeStatus {
	statuses := parseRemoteThemeStatusOutput(themes, output)
	configured := map[string]struct{}{}
	for _, theme := range themes {
		configured[theme.Slug] = struct{}{}
	}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) != 5 || fields[4] != "extra" {
			continue
		}
		if _, ok := configured[fields[0]]; ok {
			continue
		}
		statuses = append(statuses, wordpressThemeStatus{
			Theme:      wordpressThemeSpec{Slug: fields[0], Source: "installed"},
			Installed:  fields[1] == "yes",
			Active:     fields[2] == "yes",
			AutoUpdate: fields[3] == "yes",
			Extra:      true,
		})
	}
	return statuses
}

func extractThemeTarGz(sourcePath, destinationDir string) error {
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
			return ProjectError{Msg: fmt.Sprintf("theme cache archive contains unsafe path: %s", header.Name)}
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

func packageThemeCacheSource(sourceDir, outputPath, archiveRoot string) (int, error) {
	info, err := os.Stat(sourceDir)
	if err != nil || !info.IsDir() {
		return 0, ProjectError{Msg: fmt.Sprintf("theme source directory does not exist: %s", sourceDir)}
	}
	if err := project.ValidateName(archiveRoot); err != nil {
		return 0, ProjectError{Msg: fmt.Sprintf("theme archive root %q must be one safe directory name", archiveRoot)}
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
		return 0, err
	}
	sort.Strings(files)
	if err := writePluginZip(sourceDir, outputPath, archiveRoot, files); err != nil {
		return 0, err
	}
	return len(files), nil
}

func printRemoteThemeInstallCommand(target envRemoteSyncTarget) {
	args := []string{"ssh", "-p", target.SSHPort, target.SSHUser + "@" + target.SSHHost, "<wp theme bootstrap script>"}
	printCommandArgs(args)
}

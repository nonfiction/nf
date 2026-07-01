package cli

// Local env command runner and snapshot archive helpers.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type envCommandRunner struct {
	name     string
	cfg      envConfig
	metadata map[string]any
	rebuild  bool
}

func (c envCommandRunner) ensureUpInstalledActive(envDir string) error {
	if c.rebuild {
		if err := runCommandSpec(execSpec{Dir: envDir, Args: envComposeArgs(c.cfg, "build")}); err != nil {
			return err
		}
	}
	if err := runCommandSpec(execSpec{Dir: envDir, Args: envComposeArgs(c.cfg, "up", "-d")}); err != nil {
		return err
	}
	if err := runCommandSpecWithPreview(execSpec{Dir: envDir, Args: envWpBootstrapReadyArgs(c.cfg)}, envWpBootstrapPreviewArgs(c.cfg, "wait for WordPress files")); err != nil {
		return err
	}
	if err := runCommandSpecWithPreview(execSpec{Dir: envDir, Args: envWpContentPermissionsArgs(c.cfg)}, envWpBootstrapPreviewArgs(c.cfg, "fix WordPress content permissions")); err != nil {
		return err
	}
	if err := runCommandSpecWithPreview(execSpec{Dir: envDir, Args: envWpMailpitSMTPArgs(c.cfg)}, envWpBootstrapPreviewArgs(c.cfg, "configure Mailpit SMTP")); err != nil {
		return err
	}
	if len(c.metadata) > 0 {
		if err := ensureLocalWPConfigDefines(c.cfg, c.metadata); err != nil {
			return err
		}
	}
	if err := runCommandSpecQuiet(execSpec{Dir: envDir, Args: envWpProbeArgs(c.cfg, "core", "is-installed")}); err != nil {
		if err := runCommandSpec(execSpec{Dir: envDir, Args: envWpCoreInstallArgs(c.cfg)}); err != nil {
			return err
		}
	}
	if err := ensureEnvThemesInstalledActive(c.cfg); err != nil {
		return err
	}
	if err := runCommandSpecQuiet(execSpec{Dir: envDir, Args: envWpThemeIsActiveArgs(c.cfg, "")}); err != nil {
		if err := runCommandSpec(execSpec{Dir: envDir, Args: envWpThemeActivateArgs(c.cfg, "")}); err != nil {
			return err
		}
		return flushLocalRewriteRules(c.cfg)
	}
	return nil
}

func (c envCommandRunner) envReadyForSnapshot(envDir string) bool {
	if err := runCommandSpecQuiet(execSpec{Dir: envDir, Args: envWpProbeArgs(c.cfg, "core", "is-installed")}); err != nil {
		return false
	}
	if err := runCommandSpecQuiet(execSpec{Dir: envDir, Args: envWpThemeIsActiveArgs(c.cfg, "")}); err != nil {
		return false
	}
	return true
}

func (c envCommandRunner) Execute(root string, extraArgs []string) error {
	if c.name == "up" || c.name == "reset" {
		cfg, err := envConfigWithAdminCredentials(c.cfg)
		if err != nil {
			return err
		}
		c.cfg = cfg
	} else if c.name == "shell" || c.name == "wp" {
		if c.name == "shell" {
			cfg, err := envConfigWithDockerConfig(c.cfg)
			if err != nil {
				return err
			}
			c.cfg = cfg
		}
		cfg, err := envConfigWithDBCredentials(c.cfg)
		if err != nil {
			return err
		}
		c.cfg = cfg
	}
	if err := ensureManagedEnv(c.cfg); err != nil {
		return err
	}
	envDir := localEnvDir(c.cfg)
	switch c.name {
	case "up":
		if err := ensureProjectUploadsSymlink(root, c.cfg); err != nil {
			return err
		}
		if err := bootstrapThemeForEnv(root, c.cfg); err != nil {
			return err
		}
		return c.ensureUpInstalledActive(envDir)
	case "down":
		if err := runCommandSpec(execSpec{Dir: envDir, Args: envComposeArgs(c.cfg, "down")}); err != nil {
			return err
		}
		return removeProjectUploadsSymlink(root, c.cfg)
	case "logs":
		return runCommandSpec(execSpec{Dir: envDir, Args: envComposeArgs(c.cfg, "logs", "-f", c.cfg.WordpressService)})
	case "reset":
		safetyName, err := createPreRestoreSnapshot(c.cfg)
		if err != nil {
			return err
		}
		fmt.Printf("Safety snapshot: %s\n", safetyName)
		if err := runCommandSpec(execSpec{Dir: envDir, Args: envComposeArgs(c.cfg, "down", "-v", "--remove-orphans")}); err != nil {
			return err
		}
		if err := bootstrapThemeForEnv(root, c.cfg); err != nil {
			return err
		}
		if err := ensureProjectUploadsSymlink(root, c.cfg); err != nil {
			return err
		}
		return c.ensureUpInstalledActive(envDir)
	case "shell":
		return runCommandSpec(execSpec{Dir: envDir, Args: envShellArgs(c.cfg)})
	case "wp":
		return runCommandSpec(execSpec{Dir: envDir, Args: envWpArgs(c.cfg, extraArgs...)})
	default:
		return fmt.Errorf("unsupported repo command type")
	}
}

func (c envCommandRunner) Render() string {
	switch c.name {
	case "up":
		if c.rebuild {
			return "docker compose build; docker compose up -d; configure Mailpit SMTP; install WordPress if missing and ensure configured themes are installed and active"
		}
		return "docker compose up -d; configure Mailpit SMTP; install WordPress if missing and ensure configured themes are installed and active"
	case "down":
		return "docker compose down"
	case "logs":
		return "docker compose logs -f " + c.cfg.WordpressService
	case "reset":
		if c.rebuild {
			return "docker compose down -v --remove-orphans; nuke env data and recreate it with docker compose build, docker compose up -d, configure Mailpit SMTP, install WordPress if missing, and ensure configured themes are installed and active"
		}
		return "docker compose down -v --remove-orphans; nuke env data and recreate it with docker compose up -d, configure Mailpit SMTP, install WordPress if missing, and ensure configured themes are installed and active"
	case "shell":
		return "docker compose exec --user " + firstNonEmpty(c.cfg.DockerUser, defaultDockerUser) + " " + firstNonEmpty(c.cfg.WordpressService, "wordpress") + " bash"
	case "wp":
		return "docker compose exec --user " + firstNonEmpty(c.cfg.DockerUser, defaultDockerUser) + " " + firstNonEmpty(c.cfg.WordpressService, "wordpress") + " wp ..."
	default:
		return c.name
	}
}

func ensureEnvReadyForSnapshot(cfg envConfig) error {
	if err := ensureManagedEnv(cfg); err != nil {
		return err
	}
	runner := envCommandRunner{name: "up", cfg: cfg}
	if runner.envReadyForSnapshot(localEnvDir(cfg)) {
		return nil
	}
	credentialCfg, err := envConfigWithAdminCredentials(cfg)
	if err != nil {
		return err
	}
	if err := ensureManagedEnv(credentialCfg); err != nil {
		return err
	}
	runner.cfg = credentialCfg
	return runner.ensureUpInstalledActive(localEnvDir(cfg))
}

func createPreRestoreSnapshot(cfg envConfig) (string, error) {
	if err := ensureEnvReadyForSnapshot(cfg); err != nil {
		return "", err
	}
	safetyName := defaultPreRestoreSnapshotName(time.Now())
	if envSnapshotExists(cfg, safetyName) {
		return "", fmt.Errorf("env snapshot %q already exists", safetyName)
	}
	if err := envSnapshotCreateArchives(cfg, safetyName); err != nil {
		return "", err
	}
	meta, err := envSnapshotMetadataJSON(newEnvSnapshotMetadata(cfg, safetyName, time.Now()))
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(envSnapshotMetadataPath(cfg, safetyName), []byte(meta), 0o644); err != nil {
		return "", err
	}
	return safetyName, nil
}

func envSnapshotCreateScript(cfg envConfig, name string) string {
	containerDir := envSnapshotContainerDir(name)
	wpContentArchive := envSnapshotContainerWpContentArchive(name)
	excludes := envMutableWpContentTarExcludeArgs(cfg)
	return fmt.Sprintf(`set -eu
mkdir -p "%s"
wp db export "%s/database.sql"
gzip -f "%s/database.sql"
dirs=""
for dir in wp-content/uploads wp-content/plugins wp-content/languages; do
  if [ -e "/var/www/html/$dir" ]; then
    dirs="$dirs $dir"
  fi
done
if [ -n "$dirs" ]; then
  # shellcheck disable=SC2086
  tar -C /var/www/html %s -czf "%s" $dirs
else
  tar -C /var/www/html -czf "%s" --files-from /dev/null
fi
`, containerDir, containerDir, containerDir, excludes, wpContentArchive, wpContentArchive)
}

func envSnapshotCreateWpContentTransferArchiveScript(cfg envConfig, name string) string {
	containerDir := envSnapshotContainerDir(name)
	archive := envSnapshotContainerWpContentTransferArchive(name)
	excludes := envMutableWpContentTarExcludeArgs(cfg)
	return fmt.Sprintf(`set -eu
mkdir -p "%s"
dirs=""
for dir in wp-content/plugins wp-content/languages; do
  if [ -e "/var/www/html/$dir" ]; then
    dirs="$dirs $dir"
  fi
done
if [ -n "$dirs" ]; then
  # shellcheck disable=SC2086
  tar -C /var/www/html %s -czf "%s" $dirs
else
  tar -C /var/www/html -czf "%s" --files-from /dev/null
fi
`, containerDir, excludes, archive, archive)
}

func envPushTransferCreateScript(cfg envConfig, name string) string {
	containerDir := envSnapshotContainerDir(name)
	wpContentArchiveScript := envSnapshotCreateWpContentTransferArchiveScript(cfg, name)
	return fmt.Sprintf(`set -eu
mkdir -p "%s"
wp db export "%s/database.sql"
gzip -f "%s/database.sql"
%s`, containerDir, containerDir, containerDir, wpContentArchiveScript)
}

func envSnapshotRestoreScript(cfg envConfig, name string) string {
	return envSnapshotRestoreScriptWithUploads(cfg, name, true)
}

func envSnapshotRestoreScriptWithUploads(cfg envConfig, name string, includeUploads bool) string {
	databaseArchive := envSnapshotContainerDatabaseArchive(name)
	wpContentArchive := envSnapshotContainerWpContentArchive(name)
	repoPlugins := shellQuoteArg(envRepoPluginSlugList(cfg))
	excludes := envMutableWpContentTarExcludeArgs(cfg)
	tmpDir := path.Join(envSnapshotContainerDir(name), ".restore-tmp")
	uploadsClear := ""
	uploadsCopy := ""
	if includeUploads {
		uploadsClear = "  clear_dir_contents /var/www/html/wp-content/uploads\n"
		uploadsCopy = "  copy_dir_contents \"$extract_dir/wp-content/uploads\" /var/www/html/wp-content/uploads\n"
	}
	return fmt.Sprintf(`set -eu
tmpdir=%s
rm -rf "$tmpdir"
mkdir -p "$tmpdir"
trap 'rm -rf "$tmpdir"' EXIT
gzip -cd "%s" > "$tmpdir/database.sql"
wp db import "$tmpdir/database.sql"
if [ -f "%s" ]; then
  extract_dir="$tmpdir/wp-content-extract"
  mkdir -p "$extract_dir"
  tar %s -xzf "%s" -C "$extract_dir"
  clear_dir_contents() {
    dir="$1"
    if [ -e "$dir" ] && [ ! -d "$dir" ]; then
      rm -f "$dir"
    fi
    mkdir -p "$dir"
    find "$dir" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
  }
  copy_dir_contents() {
    source="$1"
    dest="$2"
    [ -d "$source" ] || return 0
    find "$source" -mindepth 1 -maxdepth 1 -exec cp -a {} "$dest"/ \;
  }
%s
  clear_dir_contents /var/www/html/wp-content/languages
  mkdir -p /var/www/html/wp-content/plugins
  repo_plugins=%s
  for entry in /var/www/html/wp-content/plugins/* /var/www/html/wp-content/plugins/.[!.]* /var/www/html/wp-content/plugins/..?*; do
    [ -e "$entry" ] || continue
    base="${entry##*/}"
    case " $repo_plugins " in *" $base "*) continue ;; esac
    rm -rf "$entry"
  done
%s
  copy_dir_contents "$extract_dir/wp-content/languages" /var/www/html/wp-content/languages
  copy_dir_contents "$extract_dir/wp-content/plugins" /var/www/html/wp-content/plugins
fi
`, shellQuoteArg(tmpDir), databaseArchive, wpContentArchive, excludes, wpContentArchive, uploadsClear, repoPlugins, uploadsCopy)
}

func envSnapshotComposeArgs(cfg envConfig, args ...string) []string {
	return append(envWordpressExecArgs(cfg, "sh", "-lc"), args...)
}

type exactLineFilterWriter struct {
	dst     io.Writer
	filters map[string]bool
	buf     strings.Builder
}

func newExactLineFilterWriter(dst io.Writer, filters ...string) *exactLineFilterWriter {
	set := make(map[string]bool, len(filters))
	for _, filter := range filters {
		set[filter] = true
	}
	return &exactLineFilterWriter{dst: dst, filters: set}
}

func (w *exactLineFilterWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			if err := w.writeLine(w.buf.String(), true); err != nil {
				return 0, err
			}
			w.buf.Reset()
			continue
		}
		w.buf.WriteByte(b)
	}
	return len(p), nil
}

func (w *exactLineFilterWriter) Flush() error {
	if w.buf.Len() == 0 {
		return nil
	}
	line := w.buf.String()
	w.buf.Reset()
	return w.writeLine(line, false)
}

func (w *exactLineFilterWriter) writeLine(line string, newline bool) error {
	trimmed := strings.TrimRight(line, "\r")
	if w.filters[trimmed] {
		return nil
	}
	if newline {
		_, err := fmt.Fprintln(w.dst, line)
		return err
	}
	_, err := fmt.Fprint(w.dst, line)
	return err
}

func runCommandSpecNoPreview(spec execSpec) error {
	if len(spec.Args) == 0 {
		return fmt.Errorf("unsupported repo command type")
	}
	cmd := exec.Command(spec.Args[0], spec.Args[1:]...)
	cmd.Dir = spec.Dir
	cmd.Stdout = os.Stdout
	stderr := newExactLineFilterWriter(os.Stderr, wpCLIPasswordlessLoginWarning)
	cmd.Stderr = stderr
	cmd.Stdin = os.Stdin
	err := cmd.Run()
	if flushErr := stderr.Flush(); flushErr != nil && err == nil {
		err = flushErr
	}
	return err
}

func envSnapshotCreateArchives(cfg envConfig, name string) error {
	if err := os.MkdirAll(envSnapshotDir(cfg, name), 0o755); err != nil {
		return err
	}
	if err := os.Chmod(envSnapshotDir(cfg, name), 0o777); err != nil {
		return err
	}
	if _, err := ensureLocalSnapshotCreateDiskSpace(cfg); err != nil {
		return err
	}
	if err := runCommandSpecNoPreview(execSpec{Dir: localEnvDir(cfg), Args: envSnapshotComposeArgs(cfg, envSnapshotCreateScript(cfg, name))}); err != nil {
		return err
	}
	return nil
}

func envPushTransferCreateArchives(cfg envConfig, name string) error {
	if err := os.MkdirAll(envSnapshotDir(cfg, name), 0o755); err != nil {
		return err
	}
	if err := os.Chmod(envSnapshotDir(cfg, name), 0o777); err != nil {
		return err
	}
	if _, err := ensureLocalPushTransferCreateDiskSpace(cfg); err != nil {
		return err
	}
	return runCommandSpecNoPreview(execSpec{Dir: localEnvDir(cfg), Args: envSnapshotComposeArgs(cfg, envPushTransferCreateScript(cfg, name))})
}

func envSnapshotRestoreArchives(cfg envConfig, name string) error {
	return envSnapshotRestoreArchivesWithUploads(cfg, name, true)
}

func envSnapshotRestoreArchivesWithUploads(cfg envConfig, name string, includeUploads bool) error {
	if err := ensureLocalSnapshotRestoreDiskSpace(cfg, name); err != nil {
		return err
	}
	if err := runCommandSpecNoPreview(execSpec{Dir: localEnvDir(cfg), Args: envSnapshotComposeArgs(cfg, envSnapshotRestoreScriptWithUploads(cfg, name, includeUploads))}); err != nil {
		return err
	}
	return nil
}

func normalizeWordPressURL(value string, assumeHTTPS bool) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if assumeHTTPS && !strings.Contains(value, "://") {
		value = "https://" + value
	}
	return strings.TrimRight(value, "/")
}

func normalizeWordPressDestinationURL(value string, assumeHTTPS bool) (string, error) {
	destinationURL := normalizeWordPressURL(value, assumeHTTPS)
	parsed, err := url.Parse(destinationURL)
	if destinationURL == "" || err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("invalid destination WordPress URL %q", strings.TrimSpace(value))
	}
	return destinationURL, nil
}

func envLocalWordPressURL(cfg envConfig) string {
	return envSnapshotWordPressURL(cfg)
}

func envWpSearchReplaceArgs(cfg envConfig, sourceURL, destinationURL string) []string {
	return envWpArgs(cfg, "search-replace", sourceURL, destinationURL, "--all-tables-with-prefix", "--skip-columns=guid")
}

func envWpOptionUpdateArgs(cfg envConfig, optionName, value string) []string {
	return envWpArgs(cfg, "--skip-themes", "--skip-plugins", "option", "update", optionName, value)
}

func envFinalizeLocalRestore(cfg envConfig, sourceURL string) error {
	if err := runCommandSpecWithPreview(execSpec{Dir: localEnvDir(cfg), Args: envWpContentPermissionsArgs(cfg)}, envWpBootstrapPreviewArgs(cfg, "fix WordPress content permissions")); err != nil {
		return err
	}
	sourceURL = normalizeWordPressURL(sourceURL, false)
	destinationURL, err := normalizeWordPressDestinationURL(envLocalWordPressURL(cfg), false)
	if err != nil {
		return err
	}
	if err := runCommandSpec(execSpec{Dir: localEnvDir(cfg), Args: envWpOptionUpdateArgs(cfg, "home", destinationURL)}); err != nil {
		return err
	}
	if err := runCommandSpec(execSpec{Dir: localEnvDir(cfg), Args: envWpOptionUpdateArgs(cfg, "siteurl", destinationURL)}); err != nil {
		return err
	}
	if sourceURL != "" && destinationURL != "" && sourceURL != destinationURL {
		if err := runCommandSpec(execSpec{Dir: localEnvDir(cfg), Args: envWpSearchReplaceArgs(cfg, sourceURL, destinationURL)}); err != nil {
			return err
		}
	}
	themeSlug := activeEnvThemeSlug(cfg)
	if err := runCommandSpecQuiet(execSpec{Dir: localEnvDir(cfg), Args: envWpThemeIsInstalledArgs(cfg, themeSlug)}); err == nil {
		if err := runCommandSpec(execSpec{Dir: localEnvDir(cfg), Args: envWpThemeActivateArgs(cfg, themeSlug)}); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(os.Stderr, "Warning: theme %q is not installed locally; skipping theme activation.\n", themeSlug)
	}
	if err := flushLocalRewriteRules(cfg); err != nil {
		return err
	}
	return runCommandSpec(execSpec{Dir: localEnvDir(cfg), Args: envWpArgs(cfg, "cache", "flush")})
}

func envSnapshotMetadataFromFile(path string) (envSnapshotMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return envSnapshotMetadata{}, err
	}
	var meta envSnapshotMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return envSnapshotMetadata{}, err
	}
	return meta, nil
}

func envSnapshotArchiveSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return info.Size()
}

func envSnapshotCreatedAt(meta envSnapshotMetadata) time.Time {
	if meta.CreatedAt == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339, meta.CreatedAt); err == nil {
		return parsed
	}
	return time.Time{}
}

func loadEnvSnapshots(cfg envConfig) ([]envSnapshotRecord, error) {
	dir := envSnapshotProjectDir(cfg)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	records := make([]envSnapshotRecord, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		meta, err := envSnapshotMetadataFromFile(envSnapshotMetadataPath(cfg, name))
		if err != nil {
			continue
		}
		record := envSnapshotRecord{
			Metadata:         meta,
			Directory:        envSnapshotDir(cfg, name),
			DatabaseArchive:  envSnapshotHostDatabaseArchive(cfg, name),
			WpContentArchive: envSnapshotHostWpContentArchive(cfg, name),
			DatabaseSize:     envSnapshotArchiveSize(envSnapshotHostDatabaseArchive(cfg, name)),
			WpContentSize:    envSnapshotArchiveSize(envSnapshotHostWpContentArchive(cfg, name)),
			CreatedAt:        envSnapshotCreatedAt(meta),
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		left := records[i]
		right := records[j]
		if !left.CreatedAt.IsZero() && !right.CreatedAt.IsZero() && !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.After(right.CreatedAt)
		}
		if left.Metadata.CreatedAt != right.Metadata.CreatedAt {
			return left.Metadata.CreatedAt > right.Metadata.CreatedAt
		}
		return left.Metadata.Name < right.Metadata.Name
	})
	return records, nil
}

func formatEnvSnapshotTime(value string) string {
	if value == "" {
		return "-"
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC().Format("2006-01-02 15:04:05")
	}
	return value
}

func formatEnvSnapshotSize(size int64) string {
	if size < 0 {
		return "-"
	}
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	value := float64(size)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if value >= 10 {
		return fmt.Sprintf("%.0f %s", value, units[unit])
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}

func envSnapshotRows(records []envSnapshotRecord) [][]string {
	rows := [][]string{{"name", "created", "database", "wp-content", "path"}}
	for _, record := range records {
		rows = append(rows, []string{
			firstNonEmpty(record.Metadata.Name, filepath.Base(record.Directory)),
			formatEnvSnapshotTime(record.Metadata.CreatedAt),
			formatEnvSnapshotSize(record.DatabaseSize),
			formatEnvSnapshotSize(record.WpContentSize),
			record.Directory,
		})
	}
	return rows
}

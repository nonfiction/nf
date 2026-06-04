package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dnsimple/dnsimple-go/v9/dnsimple"
	"github.com/linode/linodego"
	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/kinsta"
	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/state"
	"github.com/nonfiction/nf/internal/target"
	"github.com/nonfiction/nf/internal/target/provision"
	"github.com/nonfiction/nf/internal/theme"
	"github.com/nonfiction/nf/internal/ui"
)

type ProjectError struct{ Msg string }

func (e ProjectError) Error() string { return e.Msg }

var (
	runLinodeDeleteFn       = runLinodeDelete
	deleteDNSRecordFn       = provision.DeleteDNSimpleARecord
	deleteDNSTXTRecordFn    = provision.DeleteDNSimpleTXTRecord
	upsertDNSRecordFn       = provision.UpsertDNSimpleRecord
	providerCheckDNSimpleFn = checkDNSimpleProvider
	providerCheckKinstaFn   = checkKinstaProvider
	providerCheckLinodeFn   = checkLinodeProvider
	kinstaProvisionSiteFn   = provisionKinstaSite
	kinstaRemoveSiteFn      = removeKinstaSite
	targetSSHReachableFn    = targetSSHReachable
	runSSHScriptFn          = runSSHScript
	runSSHCommandFn         = runSSHCommand
	runSSHOutputFn          = runSSHOutput
	runRsyncCommandFn       = runRsyncCommand
	targetSelectFn          = ui.Select
	providerSelectFn        = ui.Select
	siteSelectFn            = ui.Select
	remoteSelectFn          = ui.Select
	remotePromptString      = ui.PromptString
	siteIsInteractiveFn     = envwizard.IsInteractiveTerminal
)

type repoCommandRunner interface {
	Execute(root string, extraArgs []string) error
	Render() string
}

type shellCommandRunner string

func (c shellCommandRunner) Execute(root string, extraArgs []string) error {
	printShellCommand(string(c), extraArgs)
	cmd := exec.Command("sh", append([]string{"-lc", string(c), "sh"}, extraArgs...)...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func (c shellCommandRunner) Render() string { return string(c) }

type argvCommandRunner []string

func (c argvCommandRunner) Execute(root string, extraArgs []string) error {
	if len(c) == 0 {
		return fmt.Errorf("unsupported repo command type")
	}
	printCommandArgs(append(append([]string{}, c...), extraArgs...))
	cmd := exec.Command(c[0], append(c[1:], extraArgs...)...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func (c argvCommandRunner) Render() string { return strings.Join(c, " ") }

type envConfig struct {
	ProjectSlug      string
	ProjectName      string
	RepoRoot         string
	ThemePath        string
	EnvDir           string
	WordpressPort    int
	MailpitPort      int
	Compose          string
	WordpressService string
	CliService       string
	ThemeMountSlug   string
	UploadsPath      string
	ThemeSlug        string
	AdminUser        string
	AdminEmail       string
	AdminPassword    string
}

type envSnapshotContents struct {
	Database       string   `json:"database"`
	WpContent      string   `json:"wp_content"`
	WpContentPaths []string `json:"wp_content_paths"`
}

type envSnapshotMetadata struct {
	Schema         int                 `json:"schema"`
	Name           string              `json:"name"`
	ProjectSlug    string              `json:"project_slug"`
	CreatedAt      string              `json:"created_at"`
	EnvPath        string              `json:"env_path"`
	ComposeProject string              `json:"compose_project"`
	WordpressURL   string              `json:"wordpress_url"`
	Contents       envSnapshotContents `json:"contents"`
}

type envSnapshotRecord struct {
	Metadata         envSnapshotMetadata
	Directory        string
	DatabaseArchive  string
	WpContentArchive string
	DatabaseSize     int64
	WpContentSize    int64
	CreatedAt        time.Time
}

type envSnapshotPruneOptions struct {
	keep   int
	dryRun bool
	yes    bool
}

type envRemoteSyncOptions struct {
	dryRun         bool
	execute        bool
	yes            bool
	nonInteractive bool
}

type envRemoteSyncTarget struct {
	Provider      string
	RemoteName    string
	SiteID        string
	Env           string
	URL           string
	TargetLabel   string
	TargetRef     string
	AccessLabel   string
	AccessSummary string
	SSHUser       string
	SSHHost       string
	SSHPort       string
	WordPressPath string
	WPCommand     string
	SudoFileOps   bool
}

type siteSnapshotOptions struct {
	output string
	dryRun bool
}

const envSnapshotSchema = 1

const wpCLIPasswordlessLoginWarning = "WARNING: option --ssl-verify-server-cert is disabled, because of an insecure passwordless login."

var (
	envSnapshotPromptString  = ui.PromptString
	envSnapshotConfirm       = ui.Confirm
	envSnapshotSelect        = ui.Select
	envSnapshotIsInteractive = envSnapshotInteractive
	envRemoteSyncConfirm     = ui.Confirm
	configPromptString       = ui.PromptString
	configIsInteractive      = envwizard.IsInteractiveTerminal
)

func defaultEnvSnapshotName(now time.Time) string {
	return now.Format("2006-01-02-150405")
}

func defaultPreRestoreSnapshotName(now time.Time) string {
	return defaultEnvSnapshotName(now) + "-pre-restore"
}

func envSnapshotProjectDir(cfg envConfig) string {
	return config.SnapshotProjectDir(cfg.ProjectSlug)
}

func remoteSnapshotDir(envID string, now time.Time) string {
	return config.RemoteSnapshotDir(envIDFileSlug(envID) + "-" + defaultEnvSnapshotName(now))
}

func envSnapshotDir(cfg envConfig, name string) string {
	return config.SnapshotDir(cfg.ProjectSlug, name)
}

func envSnapshotContainerDir(name string) string {
	return path.Join("/env-snapshots", name)
}

func envSnapshotContainerDatabaseArchive(name string) string {
	return path.Join(envSnapshotContainerDir(name), "database.sql.gz")
}

func envSnapshotContainerWpContentArchive(name string) string {
	return path.Join(envSnapshotContainerDir(name), "wp-content.tar.gz")
}

func envSnapshotHostDatabaseArchive(cfg envConfig, name string) string {
	return filepath.Join(envSnapshotDir(cfg, name), "database.sql.gz")
}

func envSnapshotHostWpContentArchive(cfg envConfig, name string) string {
	return filepath.Join(envSnapshotDir(cfg, name), "wp-content.tar.gz")
}

func envSnapshotMetadataPath(cfg envConfig, name string) string {
	return filepath.Join(envSnapshotDir(cfg, name), "snapshot.json")
}

func envSnapshotComposeMount(cfg envConfig) string {
	return config.SnapshotProjectDir(cfg.ProjectSlug)
}

func envSnapshotContentPaths() []string {
	return []string{"wp-content/uploads", "wp-content/plugins", "wp-content/mu-plugins", "wp-content/languages"}
}

func newEnvSnapshotMetadata(cfg envConfig, name string, createdAt time.Time) envSnapshotMetadata {
	envDir := localEnvDir(cfg)
	return envSnapshotMetadata{
		Schema:         envSnapshotSchema,
		Name:           name,
		ProjectSlug:    cfg.ProjectSlug,
		CreatedAt:      createdAt.Format(time.RFC3339),
		EnvPath:        envDir,
		ComposeProject: envComposeProjectName(cfg.ProjectSlug),
		WordpressURL:   envSnapshotWordPressURL(cfg),
		Contents: envSnapshotContents{
			Database:       "database.sql.gz",
			WpContent:      "wp-content.tar.gz",
			WpContentPaths: envSnapshotContentPaths(),
		},
	}
}

func envSnapshotWordPressURL(cfg envConfig) string {
	return fmt.Sprintf("http://localhost:%d", cfg.WordpressPort)
}

func envSnapshotNormalizedName(input string) (string, error) {
	name := strings.TrimSpace(input)
	if name == "" {
		return "", ProjectError{Msg: "env snapshot name cannot be empty"}
	}
	name = strings.Join(strings.Fields(name), "-")
	if name == "" {
		return "", ProjectError{Msg: "env snapshot name cannot be empty"}
	}
	if filepath.IsAbs(name) {
		return "", ProjectError{Msg: fmt.Sprintf("env snapshot name %q must not be absolute", input)}
	}
	if strings.ContainsAny(name, "/\\") {
		return "", ProjectError{Msg: fmt.Sprintf("env snapshot name %q must not contain path separators", input)}
	}
	if strings.Contains(name, "..") {
		return "", ProjectError{Msg: fmt.Sprintf("env snapshot name %q must not contain path traversal", input)}
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return "", ProjectError{Msg: fmt.Sprintf("env snapshot name %q contains unsafe characters", input)}
	}
	return name, nil
}

func envSnapshotExists(cfg envConfig, name string) bool {
	_, err := os.Stat(envSnapshotDir(cfg, name))
	return err == nil
}

func envSnapshotInteractive() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func envSnapshotMetadataJSON(meta envSnapshotMetadata) (string, error) {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return "", err
	}
	return string(append(data, '\n')), nil
}

func (c envConfig) managedUploadsDir() string {
	return filepath.Join(localEnvDir(c), firstNonEmpty(c.UploadsPath, "uploads"))
}

func (c envConfig) uploadsContainerPath() string {
	return path.Join("/", "env", firstNonEmpty(c.UploadsPath, "uploads"))
}

func localEnvDir(cfg envConfig) string {
	return cfg.EnvDir
}

func envPortBlockStart(projectSlug string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(cleanEnvSlug(projectSlug)))
	return 18000 + int(h.Sum32()%1000)*4
}

func envDerivedPorts(projectSlug string) (int, int) {
	base := envPortBlockStart(projectSlug)
	return base, base + 1
}

func cleanEnvSlug(projectSlug string) string {
	cleaned := strings.ToLower(strings.TrimSpace(projectSlug))
	var b strings.Builder
	b.Grow(len(cleaned) + len("nf__env"))
	for _, r := range cleaned {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('_')
		default:
			b.WriteByte('_')
		}
	}
	slug := strings.Trim(b.String(), "_-")
	if slug == "" {
		slug = "project"
	}
	return slug
}

type envCommandRunner struct {
	name string
	cfg  envConfig
}

func (c envCommandRunner) ensureUpInstalledActive(envDir string) error {
	if err := runCommandSpec(execSpec{Dir: envDir, Args: envComposeArgs(c.cfg, "up", "-d")}); err != nil {
		return err
	}
	if err := runCommandSpecQuiet(execSpec{Dir: envDir, Args: envWpProbeArgs(c.cfg, "core", "is-installed")}); err != nil {
		if err := runCommandSpec(execSpec{Dir: envDir, Args: envWpCoreInstallArgs(c.cfg)}); err != nil {
			return err
		}
		return nil
	}
	if err := runCommandSpecQuiet(execSpec{Dir: envDir, Args: envWpThemeIsActiveArgs(c.cfg, "")}); err != nil {
		return runCommandSpec(execSpec{Dir: envDir, Args: envWpThemeActivateArgs(c.cfg, "")})
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
	}
	if err := ensureManagedEnv(c.cfg); err != nil {
		return err
	}
	envDir := localEnvDir(c.cfg)
	switch c.name {
	case "up":
		if err := bootstrapThemeForEnv(root, c.cfg); err != nil {
			return err
		}
		return c.ensureUpInstalledActive(envDir)
	case "down":
		return runCommandSpec(execSpec{Dir: envDir, Args: envComposeArgs(c.cfg, "down")})
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
		return "docker compose up -d; install WordPress if missing and ensure the mounted theme is active"
	case "down":
		return "docker compose down"
	case "logs":
		return "docker compose logs -f " + c.cfg.WordpressService
	case "reset":
		return "docker compose down -v --remove-orphans; nuke env data and recreate it with docker compose up -d, install WordPress if missing, and ensure the mounted theme is active"
	case "shell":
		return "docker compose exec " + firstNonEmpty(c.cfg.WordpressService, "wordpress") + " bash"
	case "wp":
		return "docker compose run --rm " + c.cfg.CliService + " wp ... --allow-root"
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

func envSnapshotCreateScript(name string) string {
	containerDir := envSnapshotContainerDir(name)
	wpContentArchive := envSnapshotContainerWpContentArchive(name)
	return fmt.Sprintf(`set -eu
mkdir -p "%s"
wp db export "%s/database.sql" --allow-root
gzip -f "%s/database.sql"
dirs=""
for dir in wp-content/uploads wp-content/plugins wp-content/mu-plugins wp-content/languages; do
  if [ -e "/var/www/html/$dir" ]; then
    dirs="$dirs $dir"
  fi
done
if [ -n "$dirs" ]; then
  # shellcheck disable=SC2086
  tar -C /var/www/html -czf "%s" $dirs
else
  tar -C /var/www/html -czf "%s" --files-from /dev/null
fi
`, containerDir, containerDir, containerDir, wpContentArchive, wpContentArchive)
}

func envSnapshotRestoreScript(name string) string {
	databaseArchive := envSnapshotContainerDatabaseArchive(name)
	wpContentArchive := envSnapshotContainerWpContentArchive(name)
	return fmt.Sprintf(`set -eu
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
gzip -cd "%s" > "$tmpdir/database.sql"
wp db import "$tmpdir/database.sql" --allow-root
if [ -f "%s" ]; then
  rm -rf /var/www/html/wp-content/uploads /var/www/html/wp-content/plugins /var/www/html/wp-content/mu-plugins /var/www/html/wp-content/languages
  tar -xzf "%s" -C /var/www/html
fi
`, databaseArchive, wpContentArchive, wpContentArchive)
}

func envSnapshotComposeArgs(cfg envConfig, args ...string) []string {
	return append(envComposeArgs(cfg, "run", "--rm", firstNonEmpty(cfg.CliService, "cli"), "sh", "-lc"), args...)
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
	if err := runCommandSpecNoPreview(execSpec{Dir: localEnvDir(cfg), Args: envSnapshotComposeArgs(cfg, envSnapshotCreateScript(name))}); err != nil {
		return err
	}
	return nil
}

func envSnapshotRestoreArchives(cfg envConfig, name string) error {
	if err := runCommandSpecNoPreview(execSpec{Dir: localEnvDir(cfg), Args: envSnapshotComposeArgs(cfg, envSnapshotRestoreScript(name))}); err != nil {
		return err
	}
	return nil
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
	units := []string{"KiB", "MiB", "GiB", "TiB"}
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

func chooseEnvSnapshot(records []envSnapshotRecord, action string) (envSnapshotRecord, error) {
	options := make([]ui.SelectOption, 0, len(records))
	for _, record := range records {
		name := firstNonEmpty(record.Metadata.Name, filepath.Base(record.Directory))
		if name == "" {
			continue
		}
		label := fmt.Sprintf("%s / %s / %s / %s", name, formatEnvSnapshotTime(record.Metadata.CreatedAt), formatEnvSnapshotSize(record.DatabaseSize), formatEnvSnapshotSize(record.WpContentSize))
		options = append(options, ui.SelectOption{Label: label, Value: name})
	}
	if len(options) == 0 {
		return envSnapshotRecord{}, fmt.Errorf("No env snapshots found.")
	}
	selected, err := envSnapshotSelect(fmt.Sprintf("Choose an env snapshot to %s", action), options)
	if err != nil {
		return envSnapshotRecord{}, err
	}
	for _, record := range records {
		if firstNonEmpty(record.Metadata.Name, filepath.Base(record.Directory)) == selected {
			return record, nil
		}
	}
	return envSnapshotRecord{}, fmt.Errorf("env snapshot %q was not found", selected)
}

func cmdEnvSnapshotCreate(cfg envConfig, name string, nonInteractive bool) int {
	if strings.TrimSpace(name) == "" {
		if nonInteractive || !envSnapshotIsInteractive() {
			fmt.Fprintln(os.Stderr, "env snapshot add requires a name when stdin is not interactive")
			return 1
		}
		defaultName := defaultEnvSnapshotName(time.Now())
		prompted, err := envSnapshotPromptString("Snapshot name", defaultName, false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		name = prompted
	}
	normalized, err := envSnapshotNormalizedName(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if envSnapshotExists(cfg, normalized) {
		fmt.Fprintf(os.Stderr, "env snapshot %q already exists.\n", normalized)
		return 1
	}
	if err := ensureEnvReadyForSnapshot(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := envSnapshotCreateArchives(cfg, normalized); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	meta := newEnvSnapshotMetadata(cfg, normalized, time.Now())
	jsonText, err := envSnapshotMetadataJSON(meta)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(envSnapshotMetadataPath(cfg, normalized), []byte(jsonText), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Snapshot created.\n\nSnapshot:\n  project: %s\n  name: %s\n  path: %s\n  database: database.sql.gz\n  wp-content: wp-content.tar.gz\n", cfg.ProjectSlug, normalized, envSnapshotDir(cfg, normalized))
	return 0
}

func cmdEnvSnapshotList(cfg envConfig) int {
	records, err := loadEnvSnapshots(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(records) == 0 {
		fmt.Println("No env snapshots found.")
		return 0
	}
	fmt.Println(formatTable(envSnapshotRows(records)))
	return 0
}

func cmdEnvSnapshotDelete(cfg envConfig, name string, nonInteractive bool) int {
	records, err := loadEnvSnapshots(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	selectedName := strings.TrimSpace(name)
	if selectedName == "" {
		if nonInteractive || !envSnapshotIsInteractive() {
			fmt.Fprintln(os.Stderr, "env snapshot remove requires a name when stdin is not interactive")
			return 1
		}
		record, err := chooseEnvSnapshot(records, "delete")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		selectedName = firstNonEmpty(record.Metadata.Name, filepath.Base(record.Directory))
	}
	normalized, err := envSnapshotNormalizedName(selectedName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	path := envSnapshotDir(cfg, normalized)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "No env snapshot matched %q.\n", normalized)
			return 1
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if nonInteractive || !envSnapshotIsInteractive() {
		fmt.Fprintln(os.Stderr, "env snapshot remove requires an interactive terminal for confirmation")
		return 1
	}
	confirmed, err := envSnapshotConfirm(fmt.Sprintf("Delete env snapshot %q? This removes %s.", normalized, path), false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !confirmed {
		fmt.Fprintln(os.Stderr, "Aborted.")
		return 1
	}
	if err := os.RemoveAll(path); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Deleted env snapshot.\n\nDeleted:\n  name: %s\n  path: %s\n", normalized, path)
	return 0
}

func envSnapshotAutoPruneCandidate(name string) bool {
	return strings.HasPrefix(name, "pull-") || strings.HasPrefix(name, "push-") || strings.HasSuffix(name, "-pre-restore")
}

func envSnapshotTotalSize(record envSnapshotRecord) int64 {
	total := int64(0)
	if record.DatabaseSize > 0 {
		total += record.DatabaseSize
	}
	if record.WpContentSize > 0 {
		total += record.WpContentSize
	}
	return total
}

func envSnapshotPruneRows(records []envSnapshotRecord) [][]string {
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

func envSnapshotPrunePlan(records []envSnapshotRecord, keep int) []envSnapshotRecord {
	if keep < 0 {
		keep = 0
	}
	candidates := make([]envSnapshotRecord, 0)
	for _, record := range records {
		name := firstNonEmpty(record.Metadata.Name, filepath.Base(record.Directory))
		if envSnapshotAutoPruneCandidate(name) {
			candidates = append(candidates, record)
		}
	}
	if len(candidates) <= keep {
		return nil
	}
	return candidates[keep:]
}

func cmdEnvSnapshotPrune(cfg envConfig, opts envSnapshotPruneOptions) int {
	if opts.keep < 0 {
		fmt.Fprintln(os.Stderr, "env snapshot prune --keep must be 0 or greater")
		return 1
	}
	records, err := loadEnvSnapshots(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	prune := envSnapshotPrunePlan(records, opts.keep)
	if len(prune) == 0 {
		fmt.Printf("No env snapshots to prune. Keeping %d newest auto snapshots.\n", opts.keep)
		return 0
	}
	total := int64(0)
	for _, record := range prune {
		total += envSnapshotTotalSize(record)
	}
	fmt.Printf("Env snapshot prune plan:\n  keep newest auto snapshots: %d\n  delete snapshots:            %d\n  reclaim about:               %s\n\n", opts.keep, len(prune), formatEnvSnapshotSize(total))
	fmt.Println(formatTable(envSnapshotPruneRows(prune)))
	if opts.dryRun {
		fmt.Println("\nNo snapshots were deleted. Re-run without --dry-run to prune.")
		return 0
	}
	if !opts.yes {
		if !envSnapshotIsInteractive() {
			fmt.Fprintln(os.Stderr, "env snapshot prune requires --yes when stdin is not interactive")
			return 1
		}
		confirmed, err := envSnapshotConfirm(fmt.Sprintf("Delete %d auto env snapshots? This removes %s.", len(prune), formatEnvSnapshotSize(total)), false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	for _, record := range prune {
		if err := os.RemoveAll(record.Directory); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	fmt.Printf("\nDeleted %d env snapshots. Reclaimed about %s.\n", len(prune), formatEnvSnapshotSize(total))
	return 0
}

func parseEnvSnapshotPruneArgs(args []string) (envSnapshotPruneOptions, error) {
	opts := envSnapshotPruneOptions{keep: 3}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--dry-run":
			opts.dryRun = true
		case arg == "--yes":
			opts.yes = true
		case arg == "--keep":
			if i+1 >= len(args) {
				return opts, ProjectError{Msg: "env snapshot prune --keep requires a number"}
			}
			i++
			keep, err := strconv.Atoi(args[i])
			if err != nil || keep < 0 {
				return opts, ProjectError{Msg: "env snapshot prune --keep must be 0 or greater"}
			}
			opts.keep = keep
		case strings.HasPrefix(arg, "--keep="):
			keepText := strings.TrimPrefix(arg, "--keep=")
			keep, err := strconv.Atoi(keepText)
			if err != nil || keep < 0 {
				return opts, ProjectError{Msg: "env snapshot prune --keep must be 0 or greater"}
			}
			opts.keep = keep
		default:
			return opts, ProjectError{Msg: fmt.Sprintf("unsupported env snapshot prune option %q", arg)}
		}
	}
	return opts, nil
}

func cmdEnvSnapshotRestore(cfg envConfig, name string, nonInteractive bool) int {
	records, err := loadEnvSnapshots(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	selectedName := strings.TrimSpace(name)
	if selectedName == "" {
		if nonInteractive || !envSnapshotIsInteractive() {
			fmt.Fprintln(os.Stderr, "env snapshot use requires a name when stdin is not interactive")
			return 1
		}
		record, err := chooseEnvSnapshot(records, "restore")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		selectedName = firstNonEmpty(record.Metadata.Name, filepath.Base(record.Directory))
	}
	normalized, err := envSnapshotNormalizedName(selectedName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	path := envSnapshotDir(cfg, normalized)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "No env snapshot matched %q.\n", normalized)
			return 1
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if nonInteractive || !envSnapshotIsInteractive() {
		fmt.Fprintln(os.Stderr, "env snapshot use requires an interactive terminal for confirmation")
		return 1
	}
	confirmed, err := envSnapshotConfirm(fmt.Sprintf("Restore env snapshot %q? This will overwrite the current local env database and mutable wp-content.", normalized), false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !confirmed {
		fmt.Fprintln(os.Stderr, "Aborted.")
		return 1
	}
	if err := ensureEnvReadyForSnapshot(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	safetyName := defaultPreRestoreSnapshotName(time.Now())
	if envSnapshotExists(cfg, safetyName) {
		fmt.Fprintf(os.Stderr, "env snapshot %q already exists.\n", safetyName)
		return 1
	}
	if err := envSnapshotCreateArchives(cfg, safetyName); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	safetyMeta := newEnvSnapshotMetadata(cfg, safetyName, time.Now())
	jsonText, err := envSnapshotMetadataJSON(safetyMeta)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(envSnapshotMetadataPath(cfg, safetyName), []byte(jsonText), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := envSnapshotRestoreArchives(cfg, normalized); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Snapshot restored.\n\nRestored:\n  project: %s\n  name: %s\n\nSafety snapshot:\n  name: %s\n  path: %s\n", cfg.ProjectSlug, normalized, safetyName, envSnapshotDir(cfg, safetyName))
	return 0
}

type execSpec struct {
	Dir  string
	Args []string
}

func runCommandSpec(spec execSpec) error {
	if len(spec.Args) == 0 {
		return fmt.Errorf("unsupported repo command type")
	}
	printCommandArgs(spec.Args)
	cmd := exec.Command(spec.Args[0], spec.Args[1:]...)
	cmd.Dir = spec.Dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func runCommandSpecQuiet(spec execSpec) error {
	if len(spec.Args) == 0 {
		return fmt.Errorf("unsupported repo command type")
	}
	printCommandArgs(spec.Args)
	cmd := exec.Command(spec.Args[0], spec.Args[1:]...)
	cmd.Dir = spec.Dir
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func runRsyncCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("unsupported rsync command")
	}
	printCommandArgs(args)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func printShellCommand(command string, extraArgs []string) {
	if len(extraArgs) == 0 {
		fmt.Printf("> %s\n", command)
		return
	}
	fmt.Printf("> %s -- %s\n", command, renderCommandArgs(extraArgs))
}

func printCommandArgs(args []string) {
	fmt.Printf("> %s\n", renderCommandArgs(args))
}

func renderCommandArgs(args []string) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, shellQuoteArg(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuoteArg(arg string) string {
	if arg == "" {
		return "''"
	}
	if !strings.ContainsAny(arg, " \t\n\r'\"$`\\!&|;<>(){}[]*?~") {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
}

func envCommandDir(cfg envConfig) string {
	return localEnvDir(cfg)
}

func ensureManagedEnv(cfg envConfig) error {
	envDir := localEnvDir(cfg)
	if strings.TrimSpace(envDir) == "" {
		return fmt.Errorf("missing managed env directory")
	}
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(envSnapshotProjectDir(cfg), 0o755); err != nil {
		return err
	}
	if err := os.Chmod(envSnapshotProjectDir(cfg), 0o777); err != nil {
		return err
	}
	files := map[string]string{
		filepath.Join(envDir, "docker-compose.yml"):                                  renderEnvCompose(cfg),
		filepath.Join(envDir, ".env"):                                                renderEnvFile(cfg),
		filepath.Join(envDir, "php", "uploads.ini"):                                  renderEnvUploadsINI(),
		filepath.Join(envDir, "wordpress", "Dockerfile"):                             renderEnvDockerfile(),
		filepath.Join(envDir, "wordpress", "wordpress-rewrites.conf"):                renderEnvRewritesConf(),
		filepath.Join(envDir, firstNonEmpty(cfg.UploadsPath, "uploads"), ".gitkeep"): "",
	}
	for path, contents := range files {
		if err := writeManagedFile(path, contents, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func envConfigWithAdminCredentials(cfg envConfig) (envConfig, error) {
	if cfg.AdminUser != "" && cfg.AdminEmail != "" && cfg.AdminPassword != "" {
		return cfg, nil
	}
	values, err := loadGlobalConfig()
	if err != nil {
		return cfg, err
	}
	adminEmail := firstNonEmpty(cfg.AdminEmail, values["default_wp_email"])
	if adminEmail == "" {
		return cfg, ProjectError{Msg: fmt.Sprintf("Expected default_wp_email in %s. Set it with nf config set-default-wp-email <email>.", config.ConfigFile())}
	}
	adminUser := firstNonEmpty(cfg.AdminUser, values["default_wp_user"], "admin")
	adminPassword, err := envAdminPassword(cfg)
	if err != nil {
		return cfg, err
	}
	cfg.AdminUser = adminUser
	cfg.AdminEmail = adminEmail
	cfg.AdminPassword = adminPassword
	return cfg, nil
}

func envAdminPassword(cfg envConfig) (string, error) {
	if cfg.AdminPassword != "" {
		return cfg.AdminPassword, nil
	}
	salt, err := passwords.SecretSalt()
	if err != nil {
		return "", err
	}
	return passwords.DerivePassword(cfg.ProjectSlug, "wp-admin", salt), nil
}

func writeManagedFile(path, contents string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) == contents {
			return os.Chmod(path, mode)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		return err
	}
	return nil
}

func envPortInUse(port int) bool {
	if port <= 0 || port > 65535 {
		return true
	}
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return true
	}
	_ = listener.Close()
	return false
}

func envPortsInUse(cfg envConfig) []int {
	occupied := make([]int, 0, 2)
	for _, port := range []int{cfg.WordpressPort, cfg.MailpitPort} {
		if envPortInUse(port) {
			occupied = append(occupied, port)
		}
	}
	return occupied
}

func envPortCollisionMessage(cfg envConfig, occupied []int) string {
	if len(occupied) == 0 {
		return ""
	}
	ports := append([]int(nil), occupied...)
	sort.Ints(ports)
	projectLabel := firstNonEmpty(cfg.ProjectSlug, "project")
	block := fmt.Sprintf("The %s env wants:\n  WordPress: http://localhost:%d\n  Mailpit:   http://localhost:%d\n\nSet env.ports.wordpress and env.ports.mailpit in .nf/project.json to override.", projectLabel, cfg.WordpressPort, cfg.MailpitPort)
	if len(ports) == 1 {
		return fmt.Sprintf("Port %d is already in use.\n\n%s", ports[0], block)
	}
	parts := make([]string, 0, len(ports))
	for _, port := range ports {
		parts = append(parts, strconv.Itoa(port))
	}
	return fmt.Sprintf("Ports %s are already in use.\n\n%s", strings.Join(parts, " and "), block)
}

func preflightEnvPorts(cfg envConfig) error {
	if envManagedComposeExists(cfg) {
		return nil
	}
	if occupied := envPortsInUse(cfg); len(occupied) > 0 {
		return fmt.Errorf("%s", envPortCollisionMessage(cfg, occupied))
	}
	return nil
}

func envManagedComposeExists(cfg envConfig) bool {
	envDir := localEnvDir(cfg)
	if strings.TrimSpace(envDir) == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(envDir, "docker-compose.yml")); err != nil {
		return false
	}
	values, err := config.ReadEnvFile(filepath.Join(envDir, ".env"))
	if err != nil {
		return false
	}
	return values["COMPOSE_PROJECT_NAME"] == envComposeProjectName(cfg.ProjectSlug)
}

func envComposeArgs(cfg envConfig, args ...string) []string {
	fields := strings.Fields(firstNonEmpty(cfg.Compose, "docker compose"))
	return append(fields, args...)
}

func envCliArgs(cfg envConfig, args ...string) []string {
	return append(envComposeArgs(cfg, "run", "--rm", firstNonEmpty(cfg.CliService, "cli")), args...)
}

func envWpArgs(cfg envConfig, args ...string) []string {
	return append(envCliArgs(cfg, "wp"), append(args, "--allow-root")...)
}

func envShellArgs(cfg envConfig) []string {
	return envComposeArgs(cfg, "exec", firstNonEmpty(cfg.WordpressService, "wordpress"), "bash")
}

func envWpProbeArgs(cfg envConfig, args ...string) []string {
	return envWpArgs(cfg, args...)
}

func envWpThemeIsActiveArgs(cfg envConfig, slug string) []string {
	return envWpArgs(cfg, "theme", "is-active", firstNonEmpty(slug, cfg.ThemeMountSlug, cfg.ThemeSlug, "theme"))
}

func envWpCoreInstallArgs(cfg envConfig) []string {
	slug := firstNonEmpty(cfg.ThemeMountSlug, cfg.ThemeSlug, "theme")
	return append(envComposeArgs(cfg, "run", "--rm", firstNonEmpty(cfg.CliService, "cli"), "sh", "-lc"), `wp core install --url="$WP_URL" --title="$WP_TITLE" --admin_user="$ADMIN_USER" --admin_password="$ADMIN_PASSWORD" --admin_email="$ADMIN_EMAIL" --skip-email --allow-root && wp theme activate `+slug+` --allow-root`)
}

func envWpThemeActivateArgs(cfg envConfig, slug string) []string {
	return envWpArgs(cfg, "theme", "activate", firstNonEmpty(slug, cfg.ThemeMountSlug, cfg.ThemeSlug, "theme"))
}

func envThemeArchivePaths(cfg envConfig, sourcePath string) (string, string) {
	base := filepath.Base(sourcePath)
	host := filepath.Join(envCommandDir(cfg), firstNonEmpty(cfg.UploadsPath, "uploads"), base)
	container := path.Join("/", "env", firstNonEmpty(cfg.UploadsPath, "uploads"), base)
	return host, container
}

func envRepoPath(root, sourcePath string) string {
	if filepath.IsAbs(sourcePath) {
		return sourcePath
	}
	return filepath.Join(root, sourcePath)
}

func copyFile(sourcePath, destinationPath string) error {
	input, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		return err
	}
	output, err := os.Create(destinationPath)
	if err != nil {
		return err
	}
	defer func() { _ = output.Close() }()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	return nil
}

func configInitRequirements() []envwizard.Requirement {
	return []envwizard.Requirement{
		{Keys: []string{"DNSIMPLE_TOKEN"}, Prompt: "DNSimple token: ", Secret: true, WriteKey: "DNSIMPLE_TOKEN", Required: true},
		{Keys: []string{"KINSTA_API_KEY"}, Prompt: "Kinsta API key: ", Secret: true, WriteKey: "KINSTA_API_KEY", Required: true},
		{Keys: []string{"LINODE_TOKEN", "LINODE_CLI_TOKEN"}, Prompt: "LINODE_TOKEN (Linode API token): ", Secret: true, WriteKey: "LINODE_TOKEN", Required: true},
		{Keys: []string{"NF_PASSWORD_SALT", "NF_SECRET_SALT"}, Prompt: "NF_PASSWORD_SALT (used for derived passwords): ", Secret: true, WriteKey: "NF_PASSWORD_SALT", Required: true},
	}
}

type configInitSetting struct {
	Key      string
	Prompt   string
	Default  string
	Required bool
}

func configInitSettings() []configInitSetting {
	return []configInitSetting{
		{Key: "base_domain", Prompt: "Base domain: ", Required: true},
		{Key: "default_wp_email", Prompt: "Default WordPress email: ", Required: true},
		{Key: "default_wp_user", Prompt: "Default WordPress user: ", Default: "admin", Required: true},
		{Key: "kinsta_default_php", Prompt: "Kinsta default PHP version: ", Default: "8.3", Required: true},
		{Key: "linode_default_region", Prompt: "Linode default region: ", Default: "ca-central", Required: true},
		{Key: "linode_default_user", Prompt: "Linode default SSH user: ", Default: "nonfiction", Required: true},
		{Key: "linode_default_type", Prompt: "Linode default type: ", Default: "g6-standard-1", Required: true},
	}
}

func passwordRequirements() []envwizard.Requirement {
	return []envwizard.Requirement{
		{Keys: []string{"NF_PASSWORD_SALT", "NF_SECRET_SALT"}, Prompt: "NF_PASSWORD_SALT (used for derived passwords): ", Secret: true, WriteKey: "NF_PASSWORD_SALT", Required: true},
	}
}

func slugToTitle(value string) string {
	parts := strings.Split(strings.ReplaceAll(value, "_", "-"), "-")
	titles := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		titles = append(titles, strings.ToUpper(part[:1])+strings.ToLower(part[1:]))
	}
	return strings.Join(titles, " ")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func maskSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	prefix := 3
	if len(value) < prefix {
		prefix = len(value)
	}
	return value[:prefix] + strings.Repeat("*", 11)
}

func currentGitRoot() (string, bool) {
	wd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	return discoverGitRoot(wd)
}

func currentGitRootBase() (string, error) {
	root, ok := currentGitRoot()
	if !ok {
		return "", ProjectError{Msg: "init requires a .git repository above the current directory when --project-slug is not set"}
	}
	base := filepath.Base(filepath.Clean(root))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "", ProjectError{Msg: fmt.Sprintf("init could not derive a project slug from git root %q; pass --project-slug", root)}
	}
	return base, nil
}

func discoverGitRoot(start string) (string, bool) {
	if strings.TrimSpace(start) == "" {
		return "", false
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	if info, err := os.Stat(abs); err == nil && !info.IsDir() {
		abs = filepath.Dir(abs)
	}
	for {
		if info, err := os.Stat(filepath.Join(abs, ".git")); err == nil {
			if info.IsDir() || !info.IsDir() {
				return abs, true
			}
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", false
		}
		abs = parent
	}
}

func projectContextAvailable() bool {
	_, ok := currentNFProjectRoot()
	return ok
}

func requireProjectContext(command string) error {
	if _, ok := currentNFProjectRoot(); !ok {
		return ProjectError{Msg: fmt.Sprintf("%s requires an nf project with .nf next to .git", command)}
	}
	return nil
}

func currentNFProjectRoot() (string, bool) {
	root, ok := currentGitRoot()
	if !ok {
		return "", false
	}
	info, err := os.Stat(filepath.Join(root, ".nf"))
	if err != nil || !info.IsDir() {
		return "", false
	}
	return root, true
}

func formatTable(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	lines := make([]string, 0, len(rows)*2)
	for i, row := range rows {
		parts := make([]string, len(row))
		for j, cell := range row {
			parts[j] = cell + strings.Repeat(" ", widths[j]-len(cell))
		}
		lines = append(lines, strings.TrimRight(strings.Join(parts, "  "), " "))
		if i == 0 {
			headers := make([]string, len(widths))
			for j, width := range widths {
				headers[j] = strings.Repeat("-", width)
			}
			lines = append(lines, strings.TrimRight(strings.Join(headers, "  "), " "))
		}
	}
	return strings.Join(lines, "\n")
}

func renderCommandRun(run any) string {
	switch typed := run.(type) {
	case string:
		return typed
	case []string:
		return strings.Join(typed, " ")
	case repoCommandRunner:
		return typed.Render()
	default:
		return fmt.Sprint(run)
	}
}

func recordValueString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		value := strings.TrimSpace(typed)
		if strings.ContainsAny(value, "eE") {
			if parsed, err := strconv.ParseFloat(value, 64); err == nil && parsed == float64(int64(parsed)) {
				return strconv.FormatInt(int64(parsed), 10)
			}
		}
		return value
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		f := float64(typed)
		if f == float64(int64(f)) {
			return strconv.FormatInt(int64(f), 10)
		}
		return strconv.FormatFloat(f, 'f', -1, 32)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case json.Number:
		return typed.String()
	default:
		value := strings.TrimSpace(fmt.Sprint(typed))
		if value == "<nil>" {
			return ""
		}
		return value
	}
}

func recordStringValues(record map[string]any, keys ...string) []string {
	values := make([]string, 0, len(keys))
	seen := map[string]struct{}{}
	for _, key := range keys {
		value := recordValueString(record[key])
		if value == "" || value == "<nil>" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values
}

func recordMatchesAnyValue(record map[string]any, keys, values []string) bool {
	if len(values) == 0 {
		return false
	}
	needle := map[string]struct{}{}
	for _, value := range values {
		needle[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
	}
	for _, key := range keys {
		value := strings.ToLower(recordValueString(record[key]))
		if value == "" || value == "<nil>" {
			continue
		}
		if _, ok := needle[value]; ok {
			return true
		}
	}
	return false
}

func serverIdentityValues(server map[string]any) []string {
	return recordStringValues(server, "id", "linode_id", "_state_key", "name", "slug", "hostname", "label")
}

func siteMatchesServer(site, server map[string]any) bool {
	return recordMatchesAnyValue(site, []string{"server_id", "server", "server_name", "server_hostname", "server_label", "hostname", "label"}, serverIdentityValues(server))
}

func siteMatchesTarget(site, target map[string]any) bool {
	return recordMatchesAnyValue(site, []string{"target", "target_name", "server_id", "server", "server_name", "server_hostname", "server_label", "hostname", "label"}, serverIdentityValues(target))
}

func serverMatchesRecord(record, server map[string]any) bool {
	return recordMatchesAnyValue(record, []string{"id", "linode_id", "_state_key", "name", "slug", "hostname", "label"}, serverIdentityValues(server))
}

func linodeTokenEnv() (string, error) {
	if token := envwizard.Value("LINODE_TOKEN"); token != "" {
		return token, nil
	}
	if token := envwizard.Value("LINODE_CLI_TOKEN"); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("Expected LINODE_TOKEN in the environment or %s. LINODE_CLI_TOKEN is also accepted for convenience.", config.EnvFile())
}

func runLinodeDelete(id string) error {
	token, err := linodeTokenEnv()
	if err != nil {
		return err
	}
	linodeID, err := strconv.Atoi(strings.TrimSpace(id))
	if err != nil {
		return fmt.Errorf("invalid Linode id %q", id)
	}
	client := provision.NewLinodeClient(token)
	if err := client.DeleteInstance(context.Background(), linodeID); err != nil {
		return fmt.Errorf("deleting Linode: %w", err)
	}
	return nil
}

func isLinodeNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "request failed: 404") || strings.Contains(message, "not found")
}

type serverDNSDeleteTarget struct {
	provider   string
	accountID  string
	zone       string
	name       string
	recordType string
}

func serverDNSDeleteTargets(server map[string]any) []serverDNSDeleteTarget {
	dns, _ := server["dns"].(map[string]any)
	if dns == nil {
		return inferredServerDNSDeleteTargets(server)
	}
	provider := strings.ToLower(strings.TrimSpace(firstRecordString(dns, "provider")))
	zone := firstRecordString(dns, "zone")
	if provider == "" || zone == "" {
		return inferredServerDNSDeleteTargets(server)
	}
	accountID := firstRecordString(dns, "account_id")
	if provider == "dnsimple" && accountID == "" {
		accountID = dnsimpleAccountIDValue()
	}
	seen := map[string]struct{}{}
	targets := make([]serverDNSDeleteTarget, 0, 2)
	for _, name := range []string{
		mapStringAtPath(server, "dns", "hostname_record", "name"),
		mapStringAtPath(server, "dns", "wildcard_record", "name"),
		firstRecordString(dns, "hostname_name"),
		firstRecordString(dns, "wildcard_name"),
	} {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := "A:" + name
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, serverDNSDeleteTarget{provider: provider, accountID: accountID, zone: zone, name: name, recordType: "A"})
	}
	if provider == "dnsimple" {
		for _, target := range inferredServerDNSDeleteTargetsForZone(server, zone, accountID) {
			key := target.recordType + ":" + target.name
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			targets = append(targets, target)
		}
		for _, target := range inferredServerACMETXTDeleteTargetsForZone(server, zone, accountID) {
			key := target.recordType + ":" + target.name
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			targets = append(targets, target)
		}
	}
	return targets
}

func inferredServerDNSDeleteTargets(server map[string]any) []serverDNSDeleteTarget {
	zone := baseDomainValue()
	accountID := dnsimpleAccountIDValue()
	targets := inferredServerDNSDeleteTargetsForZone(server, zone, accountID)
	targets = append(targets, inferredServerACMETXTDeleteTargetsForZone(server, zone, accountID)...)
	return targets
}

func inferredServerDNSDeleteTargetsForZone(server map[string]any, zone, accountID string) []serverDNSDeleteTarget {
	hostname := firstRecordString(server, "hostname", "host")
	if hostname == "" || zone == "" {
		return nil
	}
	hostname = strings.TrimSuffix(strings.TrimSpace(hostname), ".")
	zone = strings.TrimSuffix(strings.TrimSpace(zone), ".")
	suffix := "." + zone
	if hostname == zone || !strings.HasSuffix(hostname, suffix) {
		return nil
	}
	name := strings.TrimSuffix(hostname, suffix)
	if name == "" {
		return nil
	}
	return []serverDNSDeleteTarget{
		{provider: "dnsimple", accountID: accountID, zone: zone, name: name, recordType: "A"},
		{provider: "dnsimple", accountID: accountID, zone: zone, name: "*." + name, recordType: "A"},
	}
}

func inferredServerACMETXTDeleteTargetsForZone(server map[string]any, zone, accountID string) []serverDNSDeleteTarget {
	hostname := firstRecordString(server, "hostname", "host")
	if hostname == "" || zone == "" {
		return nil
	}
	hostname = strings.TrimSuffix(strings.TrimSpace(hostname), ".")
	zone = strings.TrimSuffix(strings.TrimSpace(zone), ".")
	suffix := "." + zone
	if hostname == zone || !strings.HasSuffix(hostname, suffix) {
		return nil
	}
	name := strings.TrimSuffix(hostname, suffix)
	if name == "" {
		return nil
	}
	return []serverDNSDeleteTarget{{provider: "dnsimple", accountID: accountID, zone: zone, name: "_acme-challenge." + name, recordType: "TXT"}}
}

func provisionDNSRecordFQDN(target serverDNSDeleteTarget) string {
	name := strings.TrimSpace(target.name)
	zone := strings.TrimSpace(target.zone)
	switch {
	case name == "":
		return zone
	case zone == "":
		return name
	default:
		return name + "." + zone
	}
}

func isDNSimpleRecordAlreadyAbsentError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "dnsimple") && strings.Contains(message, "404") && strings.Contains(message, "not found")
}

func deleteServerDNSRecord(target serverDNSDeleteTarget) error {
	switch target.provider {
	case "", "none":
		return nil
	case "dnsimple":
		token := envwizard.Value("DNSIMPLE_TOKEN")
		if strings.TrimSpace(token) == "" {
			return fmt.Errorf("Expected DNSIMPLE_TOKEN in the environment or %s.", config.EnvFile())
		}
		deleteFn := deleteDNSRecordFn
		if strings.EqualFold(target.recordType, "TXT") {
			deleteFn = deleteDNSTXTRecordFn
		}
		if err := deleteFn(token, target.accountID, target.zone, target.name); err != nil {
			if isDNSimpleRecordAlreadyAbsentError(err) {
				fmt.Printf("DNSimple record %s already absent (%v)\n", provisionDNSRecordFQDN(target), err)
				return nil
			}
			return err
		}
		return nil
	default:
		return fmt.Errorf("unsupported DNS provider %q for server deletion", target.provider)
	}
}

func defaultProjectTasks() map[string]map[string]any {
	return map[string]map[string]any{
		"composer": map[string]any{"description": "Update theme Composer dependencies", "run": "composer --working-dir=theme update && composer --working-dir=theme dump-autoload -o"},
		"npm":      map[string]any{"description": "Refresh theme development dependencies", "run": "npm --prefix theme update --save-dev"},
		"build":    map[string]any{"description": "Build the theme assets", "run": "npm --prefix theme run build"},
		"watch":    map[string]any{"description": "Watch theme assets during development", "run": "npm --prefix theme start"},
		"test":     map[string]any{"description": "Run the theme test suite", "run": "composer --working-dir=theme test"},
	}
}

func parseProjectTask(name string, value any) (string, string, repoCommandRunner, error) {
	switch typed := value.(type) {
	case string:
		return name, typed, shellCommandRunner(typed), nil
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			s, ok := item.(string)
			if !ok {
				return "", "", nil, ProjectError{Msg: fmt.Sprintf(".nf/project.json tasks.%s.run must be a string or array of strings", name)}
			}
			parts = append(parts, s)
		}
		return name, strings.Join(parts, " "), argvCommandRunner(parts), nil
	case map[string]any:
		desc, _ := typed["description"].(string)
		if strings.TrimSpace(desc) == "" {
			return "", "", nil, ProjectError{Msg: fmt.Sprintf(".nf/project.json tasks.%s must include a description string", name)}
		}
		run := typed["run"]
		switch rr := run.(type) {
		case string:
			return name, desc, shellCommandRunner(rr), nil
		case []any:
			parts := make([]string, 0, len(rr))
			for _, item := range rr {
				s, ok := item.(string)
				if !ok {
					return "", "", nil, ProjectError{Msg: fmt.Sprintf(".nf/project.json tasks.%s.run must be a string or array of strings", name)}
				}
				parts = append(parts, s)
			}
			return name, desc, argvCommandRunner(parts), nil
		default:
			return "", "", nil, ProjectError{Msg: fmt.Sprintf(".nf/project.json tasks.%s.run must be a string or array of strings", name)}
		}
	default:
		return "", "", nil, ProjectError{Msg: fmt.Sprintf(".nf/project.json tasks.%s must be a string, array, or object", name)}
	}
}

func loadProjectMetadataOrError(root string) (map[string]any, error) {
	if root == "" {
		return map[string]any{}, nil
	}
	return theme.LoadProjectMetadata(root)
}

func saveProjectMetadata(root string, metadata map[string]any) error {
	projectPath := config.ProjectFile(root)
	if err := os.MkdirAll(filepath.Dir(projectPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(projectPath, []byte(projectInitJSON(metadata)), 0o644)
}

func projectRemotes(metadata map[string]any, create bool) (map[string]any, error) {
	deploy := mapMapAtPath(metadata, "deploy")
	if deploy == nil {
		if !create {
			return nil, nil
		}
		deploy = map[string]any{}
		metadata["deploy"] = deploy
	}
	value, ok := deploy["remotes"]
	if !ok {
		if !create {
			return nil, nil
		}
		remotes := map[string]any{}
		deploy["remotes"] = remotes
		return remotes, nil
	}
	remotes, ok := value.(map[string]any)
	if !ok || remotes == nil {
		return nil, ProjectError{Msg: ".nf/project.json deploy.remotes must be an object"}
	}
	return remotes, nil
}

type projectCommand struct {
	Description string
	Run         repoCommandRunner
}

func loadProjectTasks(root string) (map[string]projectCommand, error) {
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		return nil, err
	}
	parsed := map[string]projectCommand{}
	tasks, ok := metadata["tasks"].(map[string]any)
	if !ok || tasks == nil {
		return parsed, nil
	}
	for name, value := range tasks {
		_, desc, run, err := parseProjectTask(name, value)
		if err != nil {
			return nil, err
		}
		parsed[name] = projectCommand{Description: desc, Run: run}
	}
	return parsed, nil
}

func bootstrapThemeForEnv(root string, cfg envConfig) error {
	steps := themeBootstrapStepsNeeded(cfg.ThemePath)
	if len(steps) == 0 {
		return nil
	}
	tasks, err := loadProjectTasks(root)
	if err != nil {
		return err
	}
	for _, step := range steps {
		task, ok := tasks[step]
		if !ok {
			return ProjectError{Msg: fmt.Sprintf("theme bootstrap needs task %q in .nf/project.json", step)}
		}
		fmt.Printf("Theme bootstrap: running nf theme %s\n", step)
		if err := task.Run.Execute(root, nil); err != nil {
			return err
		}
	}
	return nil
}

func themeBootstrapStepsNeeded(themePath string) []string {
	themePath = strings.TrimSpace(themePath)
	if themePath == "" || !dirExists(themePath) {
		return nil
	}
	steps := []string{}
	if fileExists(filepath.Join(themePath, "composer.json")) && !fileExists(filepath.Join(themePath, "vendor", "autoload.php")) {
		steps = append(steps, "composer")
	}
	if fileExists(filepath.Join(themePath, "package.json")) && !dirExists(filepath.Join(themePath, "node_modules")) {
		steps = append(steps, "npm")
	}
	if themePackageHasBuildScript(filepath.Join(themePath, "package.json")) && !themeBuildOutputExists(themePath) {
		steps = append(steps, "build")
	}
	return steps
}

func themePackageHasBuildScript(packagePath string) bool {
	data, err := os.ReadFile(packagePath)
	if err != nil {
		return false
	}
	var payload struct {
		Scripts map[string]any `json:"scripts"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return false
	}
	return strings.TrimSpace(recordValueString(payload.Scripts["build"])) != ""
}

func themeBuildOutputExists(themePath string) bool {
	for _, rel := range []string{"dist", filepath.Join("assets", "dist")} {
		if dirHasFiles(filepath.Join(themePath, rel)) {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func dirHasFiles(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if dirHasFiles(filepath.Join(path, entry.Name())) {
				return true
			}
			continue
		}
		if entry.Name() != ".gitkeep" {
			return true
		}
	}
	return false
}

func formatProjectTaskLines(tasks map[string]projectCommand) []string {
	keys := make([]string, 0, len(tasks))
	for name := range tasks {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	width := 0
	for _, name := range keys {
		if len(name) > width {
			width = len(name)
		}
	}
	lines := make([]string, 0, len(keys))
	for _, name := range keys {
		task := tasks[name]
		lines = append(lines, fmt.Sprintf("%-*s -- %s", width, name, task.Description))
	}
	return lines
}

func loadEnvConfig(root string, metadata map[string]any) (envConfig, bool) {
	raw, ok := metadata["env"].(map[string]any)
	if !ok || raw == nil {
		return envConfig{}, false
	}
	projectSlug := firstNonEmpty(mapStringAtPath(metadata, "project", "slug"), "project")
	projectName := firstNonEmpty(mapStringAtPath(metadata, "project", "name"), slugToTitle(projectSlug))
	themePath := firstNonEmpty(mapStringAtPath(metadata, "wordpress", "theme_path"), "theme")
	if !filepath.IsAbs(themePath) {
		themePath = filepath.Join(root, themePath)
	}
	wordpress := mapMapAtPath(metadata, "wordpress")
	return envConfig{
		ProjectSlug:      projectSlug,
		ProjectName:      projectName,
		RepoRoot:         root,
		ThemePath:        themePath,
		EnvDir:           config.EnvDir(projectSlug),
		WordpressPort:    firstEnvPort(raw, "wordpress", projectSlug),
		MailpitPort:      firstEnvPort(raw, "mailpit", projectSlug),
		Compose:          firstNonEmpty(mapStringAtPath(raw, "compose"), "docker compose"),
		WordpressService: firstNonEmpty(mapStringAtPath(raw, "wordpress_service"), "wordpress"),
		CliService:       firstNonEmpty(mapStringAtPath(raw, "cli_service"), "cli"),
		ThemeMountSlug:   firstNonEmpty(mapStringAtPath(raw, "theme_mount_slug"), "theme"),
		UploadsPath:      firstNonEmpty(mapStringAtPath(raw, "uploads_path"), "uploads"),
		ThemeSlug:        firstNonEmpty(recordValueString(wordpress["theme_slug"]), "theme"),
	}, true
}

func firstEnvPort(raw map[string]any, name, projectSlug string) int {
	derivedWordpress, derivedMailpit := envDerivedPorts(projectSlug)
	derived := derivedWordpress
	if name == "mailpit" {
		derived = derivedMailpit
	}
	ports := mapMapAtPath(raw, "ports")
	if ports == nil {
		return derived
	}
	value, ok := ports[name]
	if !ok {
		return derived
	}
	port, parsed := parseEnvPort(value)
	if !parsed || port == 0 {
		return derived
	}
	return port
}

func parseEnvPort(value any) (int, bool) {
	switch typed := value.(type) {
	case nil:
		return 0, false
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		if n, err := typed.Int64(); err == nil {
			return int(n), true
		}
		return 0, false
	case string:
		if strings.TrimSpace(typed) == "" {
			return 0, false
		}
		n, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func defaultEnvCommands(cfg envConfig) map[string]projectCommand {
	return map[string]projectCommand{
		"up":    {Description: "Start the managed env, install WordPress if missing, and ensure the mounted theme is active", Run: envCommandRunner{name: "up", cfg: cfg}},
		"down":  {Description: "Stop the managed env", Run: envCommandRunner{name: "down", cfg: cfg}},
		"logs":  {Description: "Tail WordPress logs", Run: envCommandRunner{name: "logs", cfg: cfg}},
		"reset": {Description: "Destroy and recreate the local env", Run: envCommandRunner{name: "reset", cfg: cfg}},
		"shell": {Description: "Open a shell in the WordPress container", Run: envCommandRunner{name: "shell", cfg: cfg}},
		"wp":    {Description: "Run wp-cli passthrough", Run: envCommandRunner{name: "wp", cfg: cfg}},
	}
}

func discoverProjectRootOrError() (string, error) {
	if root, ok := config.DiscoverProjectRoot(""); ok {
		return root, nil
	}
	return "", ProjectError{Msg: "No project metadata found above the current directory. Add .nf/project.json with env metadata or tasks.<name>."}
}

func cmdThemeTasks() int {
	if err := requireProjectContext("theme tasks"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root, err := discoverProjectRootOrError()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	tasks, err := loadProjectTasks(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(tasks) == 0 {
		fmt.Fprintln(os.Stderr, "No local theme tasks configured. Add .nf/project.json tasks.<name>.")
		return 1
	}
	fmt.Println("Theme tasks:")
	for _, line := range formatProjectTaskLines(tasks) {
		fmt.Printf("  %s\n", line)
	}
	return 0
}

func cmdDeleteServer(needle string, dryRun, execute, yes, nonInteractive bool) int {
	servers, err := state.LoadStateRecords("servers")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	server := state.MatchingRecord(servers, needle)
	if server == nil {
		fmt.Fprintf(os.Stderr, "No server matched %q.\n", needle)
		return 1
	}
	provider := strings.ToLower(strings.TrimSpace(fmt.Sprint(server["provider"])))
	relatedSites, err := state.LoadStateRecords("sites")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	matchedSites := make([]map[string]any, 0)
	for _, site := range relatedSites {
		if siteMatchesServer(site, server) {
			matchedSites = append(matchedSites, site)
		}
	}
	remoteID := firstRecordString(server, "linode_id", "id", "_state_key")
	dnsDeletes := serverDNSDeleteTargets(server)
	if !execute && (dryRun || nonInteractive) {
		dryRun = true
	}
	if execute && dryRun {
		fmt.Fprintln(os.Stderr, "Choose either --execute or --dry-run, not both.")
		return 1
	}
	if nonInteractive && execute && !yes {
		fmt.Fprintln(os.Stderr, "Remote execution requires both --execute and --yes in non-interactive mode.")
		return 1
	}
	willExecute := execute || (!dryRun && !nonInteractive)
	if willExecute && provider != "linode" {
		fmt.Fprintf(os.Stderr, "Unsupported provider %q. Only linode is available for server deletion.\n", provider)
		return 1
	}
	if willExecute && provider == "linode" && strings.TrimSpace(remoteID) == "" {
		fmt.Fprintln(os.Stderr, "Selected server is missing a Linode id.")
		return 1
	}
	mode := "dry-run"
	if willExecute {
		mode = "execute"
	}
	serverLabel := serverSummary(server)
	if serverLabel == "" {
		serverLabel = needle
	}
	fmt.Println("Delete server plan:")
	fmt.Printf("  server: %s\n", serverLabel)
	fmt.Printf("  provider: %s\n", provider)
	if remoteID != "" {
		if provider == "linode" {
			fmt.Printf("  remote action: Linode API delete instance %s\n", remoteID)
		} else {
			fmt.Printf("  remote action: unavailable for provider %q\n", provider)
		}
	}
	if len(dnsDeletes) == 0 {
		fmt.Println("  dns actions: none")
	} else {
		for _, target := range dnsDeletes {
			recordType := ""
			if !strings.EqualFold(target.recordType, "A") && strings.TrimSpace(target.recordType) != "" {
				recordType = " " + strings.ToUpper(strings.TrimSpace(target.recordType))
			}
			fmt.Printf("  dns action: delete %s%s %s\n", target.provider, recordType, provisionDNSRecordFQDN(target))
		}
	}
	if len(matchedSites) == 0 {
		fmt.Println("  related sites: none")
	} else {
		names := make([]string, 0, len(matchedSites))
		for _, site := range matchedSites {
			if summary := siteSummary(site); summary != "" {
				names = append(names, summary)
			}
		}
		if len(names) == 0 {
			fmt.Printf("  related sites: %d\n", len(matchedSites))
		} else {
			fmt.Printf("  related sites: %d (%s)\n", len(matchedSites), strings.Join(names, ", "))
		}
	}
	fmt.Printf("  mode: %s\n", mode)
	if !willExecute {
		return 0
	}
	if !yes {
		confirmed, err := ui.Confirm(fmt.Sprintf("Delete server %q and matching sites from remote infrastructure and shared state?", needle), false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	if err := runLinodeDeleteFn(remoteID); err != nil {
		if isLinodeNotFoundError(err) {
			fmt.Fprintln(os.Stderr, "Remote Linode was not found; removing stale local state.")
		} else {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	for _, target := range dnsDeletes {
		if err := deleteServerDNSRecord(target); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if _, err := state.DeleteStateRecords("servers", func(record map[string]any) bool { return serverMatchesRecord(record, server) }); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if _, err := state.DeleteStateRecords("sites", func(record map[string]any) bool { return siteMatchesServer(record, server) }); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func cmdRemoveTarget(needle string, dryRun, execute, yes, nonInteractive bool) int {
	providers, err := state.LoadStateRecords("providers")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	target, providerName := findProviderTarget(providers, needle)
	if target == nil {
		fmt.Fprintf(os.Stderr, "No target matched %q.\n", needle)
		return 1
	}
	if providerName == "" {
		providerName = strings.ToLower(strings.TrimSpace(recordValueString(target["provider"])))
	}
	if providerName == "kinsta" {
		fmt.Fprintln(os.Stderr, "Kinsta target cannot be removed.")
		return 1
	}
	if providerName != "linode" {
		fmt.Fprintf(os.Stderr, "Unsupported provider %q. Only linode targets can be removed.\n", providerName)
		return 1
	}
	sites, err := state.LoadStateRecords("sites")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	matchedSites := make([]map[string]any, 0)
	for _, site := range sites {
		if siteMatchesTarget(site, target) {
			matchedSites = append(matchedSites, site)
		}
	}
	relatedSiteNames := make([]string, 0, len(matchedSites))
	seenRelatedSites := map[string]bool{}
	for _, site := range matchedSites {
		summary := siteEnvDisplaySite(site)
		if summary == "" {
			summary = siteSummary(site)
		}
		if summary != "" && !seenRelatedSites[summary] {
			seenRelatedSites[summary] = true
			relatedSiteNames = append(relatedSiteNames, summary)
		}
	}
	remoteID := firstRecordString(target, "linode_id", "id", "provider_id", "_state_key")
	dnsDeletes := serverDNSDeleteTargets(target)
	if !execute && (dryRun || nonInteractive) {
		dryRun = true
	}
	if execute && dryRun {
		fmt.Fprintln(os.Stderr, "Choose either --execute or --dry-run, not both.")
		return 1
	}
	if nonInteractive && execute && !yes {
		fmt.Fprintln(os.Stderr, "Remote execution requires both --execute and --yes in non-interactive mode.")
		return 1
	}
	willExecute := execute || (!dryRun && !nonInteractive)
	if willExecute && strings.TrimSpace(remoteID) == "" {
		fmt.Fprintln(os.Stderr, "Selected target is missing a Linode id.")
		return 1
	}
	mode := "dry-run"
	if willExecute {
		mode = "execute"
	}
	targetLabel := serverSummary(target)
	if targetLabel == "" {
		targetLabel = needle
	}
	fmt.Println("Remove target plan:")
	fmt.Printf("  target: %s\n", targetLabel)
	fmt.Printf("  provider: %s\n", providerName)
	if remoteID != "" {
		fmt.Printf("  remote action: Linode API delete instance %s\n", remoteID)
	}
	if len(dnsDeletes) == 0 {
		fmt.Println("  dns actions: none")
	} else {
		for _, target := range dnsDeletes {
			recordType := ""
			if !strings.EqualFold(target.recordType, "A") && strings.TrimSpace(target.recordType) != "" {
				recordType = " " + strings.ToUpper(strings.TrimSpace(target.recordType))
			}
			fmt.Printf("  dns action: delete %s%s %s\n", target.provider, recordType, provisionDNSRecordFQDN(target))
		}
	}
	if len(relatedSiteNames) == 0 {
		fmt.Println("  related sites: none")
	} else {
		fmt.Printf("  related sites: %s\n", strings.Join(relatedSiteNames, ", "))
		fmt.Printf("  site cache action: remove %d site(s) from local cache\n", len(relatedSiteNames))
	}
	fmt.Printf("  mode: %s\n", mode)
	if !willExecute {
		return 0
	}
	if !yes {
		message := fmt.Sprintf("Remove target %q, delete its Linode, and delete its DNS records?", needle)
		if len(relatedSiteNames) > 0 {
			message = fmt.Sprintf("Remove target %q, delete its Linode, delete its DNS records, and remove %d related site(s) from local cache?", needle, len(relatedSiteNames))
		}
		confirmed, err := ui.Confirm(message, false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	if err := runLinodeDeleteFn(remoteID); err != nil {
		if isLinodeNotFoundError(err) {
			fmt.Fprintln(os.Stderr, "Remote Linode was not found; removing stale local state.")
		} else {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	for _, target := range dnsDeletes {
		if err := deleteServerDNSRecord(target); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if !removeProviderTarget(providers, target) {
		fmt.Fprintf(os.Stderr, "No target matched %q.\n", needle)
		return 1
	}
	if err := state.SaveStateRecords("providers", providers); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(matchedSites) > 0 {
		if _, err := state.DeleteStateRecords("sites", func(record map[string]any) bool { return siteMatchesTarget(record, target) }); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return 0
}

func findProviderTarget(providers []map[string]any, needle string) (map[string]any, string) {
	for _, provider := range providers {
		providerName := strings.ToLower(strings.TrimSpace(recordValueString(provider["provider"])))
		if providerName == "" {
			providerName = strings.ToLower(strings.TrimSpace(recordValueString(provider["_state_key"])))
		}
		for _, target := range targetMaps(provider["targets"]) {
			candidate := cloneRecord(target)
			if recordValueString(candidate["provider"]) == "" && providerName != "" {
				candidate["provider"] = providerName
			}
			if state.MatchingRecord([]map[string]any{candidate}, needle) != nil {
				return target, providerName
			}
		}
	}
	return nil, ""
}

func removeProviderTarget(providers []map[string]any, target map[string]any) bool {
	for _, provider := range providers {
		targets := targetMaps(provider["targets"])
		kept := make([]map[string]any, 0, len(targets))
		removed := false
		for _, candidate := range targets {
			if !removed && serverMatchesRecord(candidate, target) {
				removed = true
				continue
			}
			kept = append(kept, candidate)
		}
		if removed {
			provider["targets"] = kept
			return true
		}
	}
	return false
}

func recordPickerValue(kind string, record map[string]any) string {
	switch kind {
	case "server":
		return firstRecordString(record, "name", "slug", "hostname", "label", "linode_id", "id", "_state_key")
	case "site":
		return firstRecordString(record, "hostname", "name", "slug", "label", "id", "_state_key")
	default:
		return firstRecordString(record, "name", "slug", "hostname", "label", "id", "_state_key")
	}
}

func recordPickerLabel(kind string, record map[string]any) string {
	switch kind {
	case "server":
		label := serverSummary(record)
		if hostname := firstRecordString(record, "hostname"); hostname != "" && !strings.Contains(label, hostname) {
			label += " / " + hostname
		}
		return label
	case "site":
		parts := []string{}
		if name := siteSummary(record); name != "" {
			parts = append(parts, name)
		}
		if server := firstRecordString(record, "server_name", "server", "server_hostname", "server_label"); server != "" {
			parts = append(parts, "server "+server)
		}
		if status := recordValueString(record["status"]); status != "" {
			parts = append(parts, status)
		}
		return strings.Join(parts, " / ")
	default:
		return firstRecordString(record, "name", "slug", "hostname", "label", "id", "_state_key")
	}
}

func chooseRecord(kind, action string) (string, error) {
	stateKind := kind + "s"
	records, err := state.LoadStateRecords(stateKind)
	if err != nil {
		return "", err
	}
	if len(records) == 0 {
		return "", fmt.Errorf("No %ss found.", kind)
	}
	options := make([]ui.SelectOption, 0, len(records))
	for _, record := range records {
		value := recordPickerValue(kind, record)
		if value == "" {
			continue
		}
		label := recordPickerLabel(kind, record)
		if label == "" {
			label = value
		}
		options = append(options, ui.SelectOption{Label: label, Value: value})
	}
	if len(options) == 0 {
		return "", fmt.Errorf("No selectable %ss found.", kind)
	}
	return ui.Select(fmt.Sprintf("Choose a %s to %s", kind, action), options)
}

func chooseServerForDelete() (string, error) {
	return chooseRecord("server", "delete")
}

func cmdList(kind string) int {
	bundle, err := state.LoadStateBundle()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	switch kind {
	case "servers":
		return cmdListServers(bundle.Servers)
	case "sites":
		return cmdListSites(bundle.Sites, bundle.Servers)
	default:
		fmt.Fprintln(os.Stderr, "unsupported list kind")
		return 1
	}
}

func firstRecordValue(record map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := record[key]; ok && recordValueString(value) != "" {
			return value
		}
	}
	return ""
}

func firstRecordString(record map[string]any, keys ...string) string {
	return recordValueString(firstRecordValue(record, keys...))
}

func mapValueAtPath(value any, keys ...string) any {
	current := value
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		next, ok := m[key]
		if !ok {
			return nil
		}
		current = next
	}
	return current
}

func mapStringAtPath(value any, keys ...string) string {
	return recordValueString(mapValueAtPath(value, keys...))
}

func mapMapAtPath(value any, keys ...string) map[string]any {
	nested, _ := mapValueAtPath(value, keys...).(map[string]any)
	return nested
}

func cloneRecord(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func serverSSHHost(server map[string]any) string {
	if sshHost := mapStringAtPath(server, "linode", "ssh", "host"); sshHost != "" {
		return sshHost
	}
	sshHost := mapStringAtPath(server, "ssh", "host")
	if sshHost != "" {
		return sshHost
	}
	return firstRecordString(server, "ssh_host", "hostname")
}

func serverSSHUser(server map[string]any) string {
	sshUser := mapStringAtPath(server, "ssh", "user")
	if sshUser != "" {
		return sshUser
	}
	return firstRecordString(server, "ssh_user", "ssh_username")
}

func serverSummary(server map[string]any) string {
	name := firstRecordString(server, "name", "slug", "_state_key", "hostname", "label")
	id := firstRecordString(server, "provider_id", "id", "linode_id")
	provider := recordValueString(server["provider"])
	sshHost := serverSSHHost(server)
	parts := make([]string, 0, 4)
	if name != "" {
		parts = append(parts, name)
	}
	if id != "" {
		parts = append(parts, "id "+id)
	}
	if provider != "" && provider != "<nil>" {
		parts = append(parts, provider)
	}
	if sshHost != "" {
		if sshUser := serverSSHUser(server); sshUser != "" {
			parts = append(parts, "ssh "+sshUser+"@"+sshHost)
		} else {
			parts = append(parts, "ssh "+sshHost)
		}
	}
	return strings.Join(parts, " / ")
}

func siteSummary(site map[string]any) string {
	return firstRecordString(site, "_state_key", "hostname", "name", "slug", "label", "server_name")
}

func siteTargetName(site map[string]any) string {
	return firstRecordString(site, "_state_key", "target_name", "target", "hostname", "name", "slug", "label")
}

func siteProviderTarget(site map[string]any) string {
	if target := firstRecordString(site, "target", "server", "server_name", "server_id", "server_hostname", "server_label"); target != "" {
		return target
	}
	return recordValueString(site["provider"])
}

func siteServerReference(site map[string]any) string {
	return firstRecordString(site, "server", "server_id", "server_name", "server_hostname", "server_label")
}

func siteKinstaID(site map[string]any, key string) string {
	if value := mapStringAtPath(site, "kinsta", key); value != "" {
		return value
	}
	return firstRecordString(site, "kinsta_"+key)
}

func sitePHPVersion(site map[string]any) string {
	return firstNonEmpty(firstRecordString(site, "php_version", "php"), mapStringAtPath(site, "kinsta", "php_version"), mapStringAtPath(site, "php", "version"))
}

func targetPHPVersion(target map[string]any) string {
	return firstNonEmpty(firstRecordString(target, "php_version", "php"), mapStringAtPath(target, "php", "version"))
}

func normalizedRecordString(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func siteEnvName(site map[string]any) string {
	return firstRecordString(site, "env", "environment", "environment_name", "environment_slug")
}

func siteEnvSiteID(site map[string]any) string {
	return firstRecordString(site, "site_id", "site", "site_name", "project", "project_slug", "wordpress_site")
}

func siteRecordName(site map[string]any) string {
	return firstRecordString(site, "name", "site_name", "project", "project_slug", "wordpress_site")
}

func siteCanonicalID(name, target string) string {
	name = strings.TrimSpace(name)
	target = strings.TrimSpace(target)
	if name == "" || target == "" {
		return name
	}
	return name + "." + target
}

func canonicalEnvID(siteID, env string) string {
	siteID = strings.TrimSpace(siteID)
	env = strings.TrimSpace(env)
	if siteID == "" || env == "" {
		return siteID
	}
	return siteID + ":" + env
}

func splitSiteEnvRef(ref string) (siteID, env string, ok bool) {
	left, right, found := strings.Cut(strings.TrimSpace(ref), ":")
	if !found || strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return "", "", false
	}
	return strings.TrimSpace(left), strings.TrimSpace(right), true
}

func envIDFileSlug(envID string) string {
	if siteID, env, ok := splitSiteEnvRef(envID); ok {
		return siteID + "." + env
	}
	return strings.TrimSpace(envID)
}

func normalizeSiteEnvRequest(siteID, env string) (string, string) {
	if parsedSiteID, parsedEnv, ok := splitSiteEnvRef(siteID); ok {
		return parsedSiteID, parsedEnv
	}
	env = strings.TrimSpace(env)
	if env == "" {
		env = "live"
	}
	return strings.TrimSpace(siteID), env
}

func siteRecordEnvID(site map[string]any) string {
	if envID := firstRecordString(site, "env_id"); envID != "" {
		return envID
	}
	return canonicalEnvID(siteRecordID(site), siteEnvName(site))
}

func siteRecordID(site map[string]any) string {
	if id := siteEnvSiteID(site); id != "" {
		return id
	}
	if id := siteCanonicalID(siteRecordName(site), siteProviderTarget(site)); id != "" {
		return id
	}
	return firstRecordString(site, "_state_key")
}

func siteEnvMatchesSite(site map[string]any, siteID string) bool {
	if parsedSiteID, _, ok := splitSiteEnvRef(siteID); ok {
		siteID = parsedSiteID
	}
	needle := normalizedRecordString(siteID)
	if needle == "" {
		return true
	}
	for _, candidate := range []string{siteRecordID(site), siteEnvSiteID(site), siteRecordName(site), siteRecordEnvID(site), siteTargetName(site), siteSummary(site), firstRecordString(site, "hostname", "url", "site_url", "home_url")} {
		if normalizedRecordString(candidate) == needle {
			return true
		}
	}
	return false
}

func siteEnvMatchesEnv(site map[string]any, env string) bool {
	if _, parsedEnv, ok := splitSiteEnvRef(env); ok {
		env = parsedEnv
	}
	needle := normalizedRecordString(env)
	if needle == "" {
		return true
	}
	if normalizedRecordString(siteEnvName(site)) == needle {
		return true
	}
	if _, parsedEnv, ok := splitSiteEnvRef(siteRecordEnvID(site)); ok && normalizedRecordString(parsedEnv) == needle {
		return true
	}
	stateKey := normalizedRecordString(siteTargetName(site))
	return strings.HasPrefix(stateKey, needle+"-") || strings.HasSuffix(stateKey, "-"+needle)
}

func siteEnvDisplaySite(site map[string]any) string {
	if siteID := siteRecordID(site); siteID != "" {
		return siteID
	}
	return siteTargetName(site)
}

func siteListEnvOrder(env string) int {
	switch env {
	case "live":
		return 0
	case "staging":
		return 1
	default:
		return 2
	}
}

func sortedSiteListEnvs(envs map[string]bool) []string {
	names := make([]string, 0, len(envs))
	for env := range envs {
		if env != "" {
			names = append(names, env)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		left, right := siteListEnvOrder(names[i]), siteListEnvOrder(names[j])
		if left != right {
			return left < right
		}
		return names[i] < names[j]
	})
	return names
}

func enrichSiteOutput(out map[string]any, record map[string]any, servers []map[string]any) error {
	provider := strings.ToLower(strings.TrimSpace(recordValueString(record["provider"])))
	if provider == "linode" {
		targetRef := siteProviderTarget(record)
		targets, err := cachedTargets()
		if err != nil {
			return err
		}
		candidates := append([]map[string]any{}, servers...)
		candidates = append(candidates, targets...)
		target := state.MatchingRecord(candidates, targetRef)
		if target == nil {
			return ProjectError{Msg: fmt.Sprintf("Linode site %q references target %q, but no cached target matched. Run nf provider check linode.", siteSummary(record), targetRef)}
		}
		if err := validateTargetRecord(target); err != nil {
			return err
		}
		out["resolved_target_summary"] = serverSummary(target)
		out["resolved_target_record"] = target
	}
	if provider == "kinsta" {
		if value := siteKinstaID(record, "site_id"); value != "" {
			out["kinsta_site_id"] = value
		}
		if value := siteKinstaID(record, "environment_id"); value != "" {
			out["kinsta_environment_id"] = value
		}
	}
	return nil
}

func projectDeployTargetAlias(metadata map[string]any, targetAlias string) (string, bool, error) {
	deploy := mapMapAtPath(metadata, "deploy")
	if deploy == nil {
		return "", false, nil
	}
	targets := mapMapAtPath(deploy, "targets")
	if targets == nil {
		return "", false, nil
	}
	value, ok := targets[targetAlias]
	if !ok {
		return "", false, nil
	}
	resolved, ok := value.(string)
	if !ok || strings.TrimSpace(resolved) == "" {
		return "", false, ProjectError{Msg: fmt.Sprintf(".nf/project.json deploy.targets.%s must be a string target alias", targetAlias)}
	}
	return strings.TrimSpace(resolved), true, nil
}

func projectRemoteAlias(metadata map[string]any, name string) (string, string, bool, error) {
	deploy := mapMapAtPath(metadata, "deploy")
	if deploy == nil {
		return "", "", false, nil
	}
	remotes := mapMapAtPath(deploy, "remotes")
	if remotes == nil {
		return "", "", false, nil
	}
	value, ok := remotes[name]
	if !ok {
		return "", "", false, nil
	}
	remote, ok := value.(map[string]any)
	if !ok || remote == nil {
		return "", "", false, ProjectError{Msg: fmt.Sprintf(".nf/project.json deploy.remotes.%s must be an object", name)}
	}
	siteID := strings.TrimSpace(recordValueString(remote["site_id"]))
	env := strings.TrimSpace(recordValueString(remote["env"]))
	if siteID == "" || env == "" {
		return "", "", false, ProjectError{Msg: fmt.Sprintf(".nf/project.json deploy.remotes.%s must include site_id and env", name)}
	}
	return siteID, env, true, nil
}

func resolveSiteTarget(requested string) (string, map[string]any, bool, bool, error) {
	resolved := strings.TrimSpace(requested)
	if resolved == "" {
		return "", nil, false, false, ProjectError{Msg: "site show requires a target or target alias"}
	}
	root, ok := currentGitRoot()
	if !ok {
		return resolved, nil, false, false, nil
	}
	projectFile := config.ProjectFile(root)
	projectFileExists := false
	if _, err := os.Stat(projectFile); err == nil {
		projectFileExists = true
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		return "", nil, false, false, err
	}
	if targetAlias, targetAliasFound, err := projectDeployTargetAlias(metadata, resolved); err != nil {
		return "", nil, false, false, err
	} else if targetAliasFound {
		return targetAlias, metadata, projectFileExists, true, nil
	}
	if remoteSiteID, remoteEnv, remoteFound, err := projectRemoteAlias(metadata, resolved); err != nil {
		return "", nil, false, false, err
	} else if remoteFound {
		return canonicalEnvID(remoteSiteID, remoteEnv), metadata, projectFileExists, true, nil
	}
	return resolved, metadata, projectFileExists, false, nil
}

func validateServerRecord(server map[string]any) error {
	if strings.TrimSpace(recordValueString(server["provider"])) == "" {
		return ProjectError{Msg: fmt.Sprintf("Server %q is missing provider.", serverSummary(server))}
	}
	return nil
}

func validateTargetRecord(target map[string]any) error {
	if strings.TrimSpace(recordValueString(target["provider"])) == "" {
		return ProjectError{Msg: fmt.Sprintf("Target %q is missing provider.", serverSummary(target))}
	}
	return nil
}

func cachedTargets() ([]map[string]any, error) {
	providers, err := state.LoadStateRecords("providers")
	if err != nil {
		return nil, err
	}
	if reconcileProviderTargetHandoffs(providers) {
		if err := state.SaveStateRecords("providers", providers); err != nil {
			return nil, err
		}
	}
	targets := providerTargetRecords(providers)
	if len(providers) > 0 {
		return targets, nil
	}
	return state.LoadStateRecords("servers")
}

func reconcileProviderTargetHandoffs(providers []map[string]any) bool {
	updated := false
	for _, provider := range providers {
		providerName := strings.ToLower(strings.TrimSpace(recordValueString(provider["provider"])))
		if providerName == "" {
			providerName = strings.ToLower(strings.TrimSpace(recordValueString(provider["_state_key"])))
		}
		if providerName != "linode" {
			continue
		}
		for _, target := range targetMaps(provider["targets"]) {
			if reconcileLinodeTargetHandoff(target) {
				updated = true
			}
		}
	}
	return updated
}

func reconcileLinodeTargetHandoff(target map[string]any) bool {
	status := strings.ToLower(strings.TrimSpace(recordValueString(target["status"])))
	phase := strings.ToLower(strings.TrimSpace(recordValueString(target["phase"])))
	if status != "provisioning" {
		return false
	}
	switch phase {
	case "dns_configured", "tls_configured":
	default:
		return false
	}
	healthURL := targetHealthURL(target)
	if healthURL == "" || !targetHealthReady(healthURL, target) {
		return false
	}
	now := time.Now().UTC().Format(time.RFC3339)
	target["status"] = "provisioned"
	target["phase"] = "complete"
	target["updated_at"] = now
	return true
}

func targetHealthURL(target map[string]any) string {
	healthURL := strings.TrimSpace(recordValueString(target["health_url"]))
	if healthURL == "" {
		hostname := firstRecordString(target, "hostname", "host")
		if hostname == "" {
			return ""
		}
		healthURL = "https://" + hostname
	}
	healthURL = strings.TrimRight(healthURL, "/")
	if strings.HasSuffix(healthURL, "/healthz") {
		return healthURL
	}
	return healthURL + "/healthz"
}

func targetHealthReady(healthURL string, target map[string]any) bool {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(healthURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return false
	}
	text := strings.ToLower(string(body))
	if !strings.Contains(text, "ready") {
		return false
	}
	name := strings.ToLower(firstRecordString(target, "target_name", "name", "label"))
	hostname := strings.ToLower(firstRecordString(target, "hostname", "host"))
	return (name != "" && strings.Contains(text, name)) || (hostname != "" && strings.Contains(text, hostname))
}

func providerTargetRecords(providers []map[string]any) []map[string]any {
	targets := make([]map[string]any, 0)
	for _, provider := range providers {
		providerName := recordValueString(provider["provider"])
		if providerName == "" {
			providerName = recordValueString(provider["_state_key"])
		}
		for _, target := range targetMaps(provider["targets"]) {
			record := cloneRecord(target)
			if recordValueString(record["provider"]) == "" && providerName != "" {
				record["provider"] = providerName
			}
			if strings.EqualFold(providerName, "kinsta") && recordValueString(record["status"]) == "" {
				if status := recordValueString(provider["status"]); status != "" {
					record["status"] = status
				}
			}
			targets = append(targets, record)
		}
	}
	return targets
}

func targetMaps(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if record, ok := item.(map[string]any); ok {
				out = append(out, record)
			}
		}
		return out
	default:
		return nil
	}
}

func validateSiteRecord(site map[string]any) error {
	provider := strings.ToLower(strings.TrimSpace(recordValueString(site["provider"])))
	if provider == "" {
		return ProjectError{Msg: fmt.Sprintf("Site %q is missing provider.", siteSummary(site))}
	}
	if provider == "linode" && siteProviderTarget(site) == "" {
		return ProjectError{Msg: fmt.Sprintf("Linode site %q is missing a target reference.", siteSummary(site))}
	}
	return nil
}

func cmdListServers(records []map[string]any) int {
	if len(records) == 0 {
		fmt.Println("No servers found.")
		return 0
	}
	return cmdListTargets(records)
}

func cmdListTargets(records []map[string]any) int {
	if len(records) == 0 {
		fmt.Println("No targets found.")
		return 0
	}
	rows := [][]string{{"target", "provider", "hostname", "status"}}
	for _, record := range records {
		rows = append(rows, []string{
			firstRecordString(record, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id"),
			recordValueString(record["provider"]),
			firstRecordString(record, "hostname", "host", "public_ipv4", "ipv4", "ip"),
			targetLiveStatus(record),
		})
	}
	fmt.Println(formatTable(rows))
	return 0
}

func targetLiveStatus(record map[string]any) string {
	switch strings.ToLower(strings.TrimSpace(recordValueString(record["provider"]))) {
	case "kinsta":
		return kinstaTargetLiveStatus(record)
	case "linode":
		return linodeTargetLiveStatus(record)
	default:
		return recordValueString(record["status"])
	}
}

func kinstaTargetLiveStatus(record map[string]any) string {
	result, err := providerCheckKinstaFn()
	if err != nil {
		return "unreachable"
	}
	for _, target := range targetMaps(result.Record["targets"]) {
		if recordValueString(target["id"]) == "kinsta" || strings.EqualFold(recordValueString(target["provider"]), "kinsta") {
			if status := recordValueString(target["status"]); status != "" {
				return status
			}
		}
	}
	if status := recordValueString(result.Record["status"]); status != "" {
		return status
	}
	return firstNonEmpty(recordValueString(record["status"]), "active")
}

func linodeTargetLiveStatus(record map[string]any) string {
	if targetSSHReachableFn(record) {
		return "reachable"
	}
	if serverSSHHost(record) != "" {
		return "ssh unavailable"
	}
	return recordValueString(record["status"])
}

func targetSSHReachable(record map[string]any) bool {
	host := serverSSHHost(record)
	if host == "" {
		return false
	}
	user := serverSSHUser(record)
	destination := host
	if user != "" {
		destination = user + "@" + host
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=3", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", destination, "true")
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

type siteAddArgs struct {
	target         string
	site           string
	region         string
	phpVersion     string
	execute        bool
	dryRun         bool
	yes            bool
	nonInteractive bool
}

type siteEnvPlan struct {
	Env      string
	Path     string
	Database string
	Hostname string
	URL      string
	Title    string
}

type siteAddPlan struct {
	Target        map[string]any
	TargetName    string
	SSHUser       string
	SSHHost       string
	Site          string
	SiteID        string
	BaseDomain    string
	PHPVersion    string
	AdminUser     string
	AdminEmail    string
	AdminPassword string
	DBPassword    string
	Envs          []siteEnvPlan
}

type kinstaSiteAddEnvPlan struct {
	Env      string
	Domain   string
	URL      string
	Title    string
	Branch   string
	EnvID    string
	DomainID string
	Path     string
	Database string
	SSHHost  string
	SSHPort  string
	SSHUser  string
	SSHCmd   string
}

type kinstaSiteAddPlan struct {
	Target        map[string]any
	TargetName    string
	CompanyID     string
	Site          string
	SiteID        string
	BaseDomain    string
	Region        string
	PHPVersion    string
	AdminUser     string
	AdminEmail    string
	AdminPassword string
	DNSZone       string
	DNSAccountID  string
	Envs          []kinstaSiteAddEnvPlan
}

type kinstaProvisionResult struct {
	CompanyID string
	SiteID    string
	Envs      []kinstaSiteAddEnvPlan
}

type siteRemoveEnvPlan struct {
	Env      string
	EnvID    string
	DomainID string
	Path     string
	Database string
	Hostname string
}

type siteRemovePlan struct {
	SiteID       string
	Name         string
	Provider     string
	Target       map[string]any
	TargetName   string
	KinstaSiteID string
	DNSZone      string
	DNSAccountID string
	DNSNames     []string
	SSHUser      string
	SSHHost      string
	Envs         []siteRemoveEnvPlan
}

func cleanSiteSlug(input string) (string, error) {
	slug := strings.ToLower(strings.TrimSpace(input))
	if slug == "" {
		return "", ProjectError{Msg: "site name cannot be empty"}
	}
	if strings.HasPrefix(slug, "-") || strings.HasSuffix(slug, "-") {
		return "", ProjectError{Msg: fmt.Sprintf("site name %q must not start or end with '-'", input)}
	}
	for _, r := range slug {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			continue
		}
		return "", ProjectError{Msg: fmt.Sprintf("site name %q must use only lowercase letters, numbers, and '-'", input)}
	}
	return slug, nil
}

func siteDBName(site, env string) string {
	name := strings.ReplaceAll(site, "-", "_")
	if env == "staging" {
		return name + "_staging"
	}
	return name
}

func siteEnvPath(site, env string) string {
	if env == "staging" {
		return path.Join("/var/www/sites", site+"_staging", "public")
	}
	return path.Join("/var/www/sites", site, "public")
}

func siteEnvHostname(site, targetName, baseDomain, env string) string {
	label := site
	if env == "staging" {
		label += "-staging"
	}
	return label + "." + targetName + "." + baseDomain
}

func linodeSiteID(site, targetName string) string {
	return site + "." + targetName
}

func linodeEnvID(site, targetName, env string) string {
	return canonicalEnvID(linodeSiteID(site, targetName), env)
}

func sshRecord(user, host, port, command string) map[string]any {
	ssh := map[string]any{}
	if host != "" {
		ssh["host"] = host
	}
	if port != "" {
		ssh["port"] = port
	}
	if user != "" {
		ssh["user"] = user
	}
	if command != "" {
		ssh["command"] = command
	}
	return ssh
}

func sshCommand(user, host, port string) string {
	if host == "" {
		return ""
	}
	destination := host
	if user != "" {
		destination = user + "@" + host
	}
	if port != "" {
		return "ssh " + destination + " -p " + port
	}
	return "ssh " + destination
}

func siteEnvTitle(site, env string) string {
	title := slugToTitle(site)
	if env == "staging" {
		return title + " Staging"
	}
	return title
}

func buildSiteAddPlan(args siteAddArgs) (siteAddPlan, error) {
	siteSlug, err := cleanSiteSlug(args.site)
	if err != nil {
		return siteAddPlan{}, err
	}
	values, err := loadGlobalConfig()
	if err != nil {
		return siteAddPlan{}, err
	}
	baseDomain := strings.TrimSuffix(strings.TrimSpace(values["base_domain"]), ".")
	if baseDomain == "" {
		return siteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Expected base_domain in %s. Set it with nf config set-base-domain <domain>.", config.ConfigFile())}
	}
	adminEmail := strings.TrimSpace(values["default_wp_email"])
	if adminEmail == "" {
		return siteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Expected default_wp_email in %s. Set it with nf config set-default-wp-email <email>.", config.ConfigFile())}
	}
	adminUser := firstNonEmpty(values["default_wp_user"], "admin")
	targets, err := cachedTargets()
	if err != nil {
		return siteAddPlan{}, err
	}
	target := state.MatchingRecord(targets, args.target)
	if target == nil {
		return siteAddPlan{}, ProjectError{Msg: fmt.Sprintf("No target matched %q.", args.target)}
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(target["provider"])))
	if provider != "linode" {
		return siteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Unsupported provider %q. Only linode site add is available.", provider)}
	}
	targetName := firstRecordString(target, "target_name", "name", "slug", "label", "_state_key")
	if targetName == "" {
		return siteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Target %q is missing a name.", args.target)}
	}
	sshHost := serverSSHHost(target)
	if sshHost == "" {
		return siteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Target %q is missing an SSH host.", targetName)}
	}
	sshUser := firstNonEmpty(serverSSHUser(target), values["linode_default_user"])
	if sshUser == "" {
		return siteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Target %q is missing an SSH user. Set linode_default_user with nf config set-linode-default-user <user>.", targetName)}
	}
	salt, err := passwords.SecretSalt()
	if err != nil {
		return siteAddPlan{}, err
	}
	plan := siteAddPlan{
		Target:        target,
		TargetName:    targetName,
		SSHUser:       sshUser,
		SSHHost:       sshHost,
		Site:          siteSlug,
		SiteID:        linodeSiteID(siteSlug, targetName),
		BaseDomain:    baseDomain,
		PHPVersion:    targetPHPVersion(target),
		AdminUser:     adminUser,
		AdminEmail:    adminEmail,
		AdminPassword: passwords.DerivePassword(siteSlug, "wp-admin", salt),
		DBPassword:    passwords.DerivePassword(siteSlug, "mysql", salt),
	}
	for _, env := range []string{"live", "staging"} {
		hostname := siteEnvHostname(siteSlug, targetName, baseDomain, env)
		plan.Envs = append(plan.Envs, siteEnvPlan{
			Env:      env,
			Path:     siteEnvPath(siteSlug, env),
			Database: siteDBName(siteSlug, env),
			Hostname: hostname,
			URL:      "https://" + hostname,
			Title:    siteEnvTitle(siteSlug, env),
		})
	}
	return plan, nil
}

func kinstaSiteID(site string) string {
	return site + ".kinsta"
}

func kinstaSiteDomain(site, baseDomain, env string) string {
	label := site
	if env == "staging" {
		label += "-staging"
	}
	return label + ".kinsta." + baseDomain
}

func buildKinstaSiteAddPlan(args siteAddArgs) (kinstaSiteAddPlan, error) {
	siteSlug, err := cleanSiteSlug(args.site)
	if err != nil {
		return kinstaSiteAddPlan{}, err
	}
	values, err := loadGlobalConfig()
	if err != nil {
		return kinstaSiteAddPlan{}, err
	}
	baseDomain := strings.TrimSuffix(strings.TrimSpace(values["base_domain"]), ".")
	if baseDomain == "" {
		return kinstaSiteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Expected base_domain in %s. Set it with nf config set-base-domain <domain>.", config.ConfigFile())}
	}
	adminEmail := strings.TrimSpace(values["default_wp_email"])
	if adminEmail == "" {
		return kinstaSiteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Expected default_wp_email in %s. Set it with nf config set-default-wp-email <email>.", config.ConfigFile())}
	}
	adminUser := firstNonEmpty(values["default_wp_user"], "admin")
	region := firstNonEmpty(args.region, values["kinsta_default_region"], "ca-toronto-1")
	phpVersion := firstNonEmpty(args.phpVersion, values["kinsta_default_php"], "8.3")
	targets, err := cachedTargets()
	if err != nil {
		return kinstaSiteAddPlan{}, err
	}
	target := state.MatchingRecord(targets, args.target)
	if target == nil {
		return kinstaSiteAddPlan{}, ProjectError{Msg: fmt.Sprintf("No target matched %q.", args.target)}
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(target["provider"])))
	if provider != "kinsta" {
		return kinstaSiteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Unsupported provider %q. Only kinsta site add is available.", provider)}
	}
	targetName := firstRecordString(target, "target_name", "name", "slug", "label", "_state_key")
	if targetName == "" {
		targetName = "kinsta"
	}
	companyID := firstRecordString(target, "company_id", "company")
	dnsAccountID := firstNonEmpty(values["dnsimple_account_id"], dnsimpleAccountIDValue())
	if dnsAccountID == "" {
		return kinstaSiteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Expected dnsimple_account_id in %s. Run nf provider check dnsimple.", config.ConfigFile())}
	}
	salt, err := passwords.SecretSalt()
	if err != nil {
		return kinstaSiteAddPlan{}, err
	}
	plan := kinstaSiteAddPlan{
		Target:        target,
		TargetName:    targetName,
		CompanyID:     companyID,
		Site:          siteSlug,
		SiteID:        kinstaSiteID(siteSlug),
		BaseDomain:    baseDomain,
		Region:        region,
		PHPVersion:    phpVersion,
		AdminUser:     adminUser,
		AdminEmail:    adminEmail,
		AdminPassword: passwords.DerivePassword(siteSlug, "wp-admin", salt),
		DNSZone:       baseDomain,
		DNSAccountID:  dnsAccountID,
	}
	for _, env := range []string{"live", "staging"} {
		domain := kinstaSiteDomain(siteSlug, baseDomain, env)
		branch := "main"
		if env == "staging" {
			branch = "develop"
		}
		plan.Envs = append(plan.Envs, kinstaSiteAddEnvPlan{Env: env, Domain: domain, URL: "https://" + domain, Title: siteEnvTitle(siteSlug, env), Branch: branch})
	}
	return plan, nil
}

func siteAddRecord(plan siteAddPlan, env siteEnvPlan) map[string]any {
	envID := canonicalEnvID(plan.SiteID, env.Env)
	sshPort := firstNonEmpty(mapStringAtPath(plan.Target, "ssh", "port"), firstRecordString(plan.Target, "ssh_port"), "22")
	return map[string]any{
		"provider":    "linode",
		"env_id":      envID,
		"site_id":     plan.SiteID,
		"name":        plan.Site,
		"env":         env.Env,
		"target":      plan.TargetName,
		"hostname":    env.Hostname,
		"url":         env.URL,
		"path":        env.Path,
		"database":    env.Database,
		"php_version": plan.PHPVersion,
		"status":      "active",
		"ssh":         sshRecord(plan.SSHUser, plan.SSHHost, sshPort, sshCommand(plan.SSHUser, plan.SSHHost, sshPort)),
	}
}

func siteAddRecords(plan siteAddPlan) []map[string]any {
	records := make([]map[string]any, 0, len(plan.Envs))
	for _, env := range plan.Envs {
		records = append(records, siteAddRecord(plan, env))
	}
	return records
}

func appendSiteAddRecords(plan siteAddPlan) error {
	existing, err := state.LoadStateRecords("sites")
	if err != nil {
		return err
	}
	if err := ensureSiteNotCached(existing, plan.SiteID); err != nil {
		return err
	}
	existing = append(existing, siteAddRecords(plan)...)
	return state.SaveStateRecords("sites", existing)
}

func kinstaSiteAddRecord(plan kinstaSiteAddPlan, env kinstaSiteAddEnvPlan, result kinstaProvisionResult) map[string]any {
	return map[string]any{
		"provider":    "kinsta",
		"env_id":      canonicalEnvID(plan.SiteID, env.Env),
		"site_id":     plan.SiteID,
		"name":        plan.Site,
		"env":         env.Env,
		"target":      plan.TargetName,
		"hostname":    env.Domain,
		"url":         env.URL,
		"path":        env.Path,
		"database":    env.Database,
		"php_version": plan.PHPVersion,
		"status":      "active",
		"ssh":         sshRecord(env.SSHUser, env.SSHHost, env.SSHPort, env.SSHCmd),
		"kinsta": map[string]any{
			"site_id":        result.SiteID,
			"environment_id": env.EnvID,
			"domain_id":      env.DomainID,
			"branch":         env.Branch,
		},
	}
}

func upsertKinstaSiteAddRecords(plan kinstaSiteAddPlan, result kinstaProvisionResult) error {
	existing, err := state.LoadStateRecords("sites")
	if err != nil {
		return err
	}
	kept := make([]map[string]any, 0, len(existing)+len(result.Envs))
	for _, record := range existing {
		if siteEnvMatchesSite(record, plan.SiteID) {
			continue
		}
		kept = append(kept, record)
	}
	for _, env := range result.Envs {
		kept = append(kept, kinstaSiteAddRecord(plan, env, result))
	}
	return state.SaveStateRecords("sites", kept)
}

func printKinstaSiteAddPlan(plan kinstaSiteAddPlan, mode string) {
	fmt.Println("Add site plan:")
	fmt.Printf("  target: %s\n", plan.TargetName)
	fmt.Printf("  provider: kinsta\n")
	if plan.CompanyID != "" {
		fmt.Printf("  company id: %s\n", plan.CompanyID)
	}
	fmt.Printf("  site: %s\n", plan.Site)
	fmt.Printf("  site id: %s\n", plan.SiteID)
	fmt.Printf("  region: %s\n", plan.Region)
	fmt.Printf("  php: %s\n", plan.PHPVersion)
	fmt.Printf("  admin user: %s\n", plan.AdminUser)
	fmt.Printf("  admin email: %s\n", plan.AdminEmail)
	fmt.Printf("  admin password: derived from %s\n", plan.Site)
	for _, env := range plan.Envs {
		fmt.Printf("  env %s:\n", env.Env)
		fmt.Printf("    domain: %s\n", env.Domain)
		fmt.Printf("    url: %s\n", env.URL)
	}
	fmt.Printf("  dns: dnsimple zone %s account %s\n", plan.DNSZone, plan.DNSAccountID)
	fmt.Printf("  local state: %s\n", state.StatePath("sites"))
	fmt.Printf("  mode: %s\n", mode)
}

func cmdKinstaSiteAdd(args siteAddArgs) int {
	if args.execute && args.dryRun {
		fmt.Fprintln(os.Stderr, "Choose either --execute or --dry-run, not both.")
		return 1
	}
	if !args.execute && (args.dryRun || args.nonInteractive) {
		args.dryRun = true
	}
	if args.nonInteractive && args.execute && !args.yes {
		fmt.Fprintln(os.Stderr, "Remote execution requires both --execute and --yes in non-interactive mode.")
		return 1
	}
	plan, err := buildKinstaSiteAddPlan(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	willExecute := args.execute || (!args.dryRun && !args.nonInteractive)
	mode := "dry-run"
	if willExecute {
		mode = "execute"
	}
	printKinstaSiteAddPlan(plan, mode)
	if !willExecute {
		return 0
	}
	if !args.yes {
		confirmed, err := ui.Confirm(fmt.Sprintf("Add site %q with live and staging envs on target %q?", plan.Site, plan.TargetName), false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	result, err := kinstaProvisionSiteFn(plan)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, "Kinsta site add is resumable; rerun the same command after fixing the error.")
		return 1
	}
	if err := upsertKinstaSiteAddRecords(plan, result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Site added.")
	return 0
}

func provisionKinstaSite(plan kinstaSiteAddPlan) (kinstaProvisionResult, error) {
	token := envwizard.Value("KINSTA_API_KEY")
	if token == "" {
		return kinstaProvisionResult{}, fmt.Errorf("Expected KINSTA_API_KEY in the environment or %s.", config.EnvFile())
	}
	dnsToken := envwizard.Value("DNSIMPLE_TOKEN")
	if dnsToken == "" {
		return kinstaProvisionResult{}, fmt.Errorf("Expected DNSIMPLE_TOKEN in the environment or %s.", config.EnvFile())
	}
	client := kinsta.NewClient(firstNonEmpty(envwizard.Value("KINSTA_BASE_URL"), "https://api.kinsta.com/v2"), token)
	ctx := context.Background()
	companyID := plan.CompanyID
	if companyID == "" {
		validate, err := client.Validate(ctx)
		if err != nil {
			return kinstaProvisionResult{}, err
		}
		companyID = strings.TrimSpace(validate.Company)
		if companyID == "" {
			return kinstaProvisionResult{}, fmt.Errorf("Kinsta /v2/validate did not return a company uuid")
		}
		plan.CompanyID = companyID
	}
	kinstaSite, err := ensureKinstaSite(ctx, client, plan, companyID)
	if err != nil {
		return kinstaProvisionResult{}, err
	}
	liveEnv, stagingEnv, err := ensureKinstaEnvironments(ctx, client, kinstaSite.ID, plan.PHPVersion)
	if err != nil {
		return kinstaProvisionResult{}, err
	}
	result := kinstaProvisionResult{CompanyID: companyID, SiteID: kinstaSite.ID, Envs: make([]kinstaSiteAddEnvPlan, 0, len(plan.Envs))}
	for _, env := range plan.Envs {
		remoteEnv := liveEnv
		if env.Env == "staging" {
			remoteEnv = stagingEnv
		}
		domain, err := ensureKinstaDomain(ctx, client, remoteEnv.ID, env.Domain)
		if err != nil {
			return kinstaProvisionResult{}, err
		}
		records, err := client.DomainRecords(ctx, domain.ID)
		if err != nil {
			return kinstaProvisionResult{}, err
		}
		if err := upsertKinstaDNSRecords(dnsToken, plan.DNSAccountID, plan.DNSZone, env.Domain, records); err != nil {
			return kinstaProvisionResult{}, err
		}
		if !domain.IsPrimary {
			opID, err := client.ChangePrimaryDomain(ctx, remoteEnv.ID, domain.ID, true)
			if err != nil {
				return kinstaProvisionResult{}, err
			}
			if err := waitKinstaOperation(ctx, client, opID); err != nil {
				return kinstaProvisionResult{}, err
			}
		}
		env.EnvID = remoteEnv.ID
		env.DomainID = domain.ID
		if cfg, err := client.SFTPConfig(ctx, result.SiteID, remoteEnv.ID); err == nil {
			env.SSHHost = cfg.Host
			env.SSHPort = firstNonEmpty(cfg.Port, "22")
			env.SSHUser = cfg.User
			env.SSHCmd = cfg.SSHCommand
			env.Path = kinstaEnvPath(cfg.User, remoteEnv.WebRoot)
			env.Database = cfg.User
		} else {
			env.SSHHost = remoteEnv.SSHConnection.SSHIP.ExternalIP
			env.SSHPort = firstNonEmpty(remoteEnv.SSHConnection.SSHPort, "22")
			env.Path = kinstaEnvPath(plan.Site, remoteEnv.WebRoot)
			env.Database = plan.Site
		}
		result.Envs = append(result.Envs, env)
	}
	return result, nil
}

func kinstaEnvPath(user, webRoot string) string {
	user = strings.TrimSpace(user)
	if user == "" {
		return ""
	}
	root := path.Join("/www", user, "public")
	webRoot = strings.TrimSpace(webRoot)
	if webRoot == "" || webRoot == "/" {
		return root
	}
	if strings.HasPrefix(webRoot, "/www/") {
		return path.Clean(webRoot)
	}
	return path.Join(root, webRoot)
}

func ensureKinstaSite(ctx context.Context, client *kinsta.Client, plan kinstaSiteAddPlan, companyID string) (kinsta.Site, error) {
	sites, err := client.ListSites(ctx, companyID)
	if err != nil {
		return kinsta.Site{}, err
	}
	if site, ok := kinsta.FindSite(sites, plan.Site); ok {
		return site, nil
	}
	fmt.Printf("Creating Kinsta site %s in %s...\n", plan.Site, plan.Region)
	opID, err := client.CreateSite(ctx, kinsta.CreateSiteRequest{
		Company:              companyID,
		DisplayName:          plan.Site,
		Region:               plan.Region,
		InstallMode:          "new",
		AdminEmail:           plan.AdminEmail,
		AdminPassword:        plan.AdminPassword,
		AdminUser:            plan.AdminUser,
		SiteTitle:            plan.Site,
		WPLanguage:           "en_US",
		IsSubdomainMultisite: false,
		IsMultisite:          false,
		WooCommerce:          false,
		WordPressSEO:         false,
	})
	if err != nil {
		return kinsta.Site{}, err
	}
	if err := waitKinstaOperation(ctx, client, opID); err != nil {
		return kinsta.Site{}, err
	}
	sites, err = client.ListSites(ctx, companyID)
	if err != nil {
		return kinsta.Site{}, err
	}
	if site, ok := kinsta.FindSite(sites, plan.Site); ok {
		return site, nil
	}
	return kinsta.Site{}, fmt.Errorf("Kinsta site %q was created but was not found in site list", plan.Site)
}

func ensureKinstaEnvironments(ctx context.Context, client *kinsta.Client, siteID, phpVersion string) (kinsta.Environment, kinsta.Environment, error) {
	envs, err := waitKinstaEnvironments(ctx, client, siteID, func(envs []kinsta.Environment) (kinsta.Environment, bool) {
		return findKinstaLiveEnvironment(envs)
	})
	if err != nil {
		return kinsta.Environment{}, kinsta.Environment{}, err
	}
	live, ok := findKinstaLiveEnvironment(envs)
	if !ok {
		return kinsta.Environment{}, kinsta.Environment{}, fmt.Errorf("Kinsta site %s is missing live environment; found: %s", siteID, kinstaEnvironmentSummary(envs))
	}
	if err := ensureKinstaEnvironmentPHP(ctx, client, live, phpVersion); err != nil {
		return kinsta.Environment{}, kinsta.Environment{}, err
	}
	envs, err = waitKinstaEnvironments(ctx, client, siteID, func(envs []kinsta.Environment) (kinsta.Environment, bool) {
		return findKinstaLiveEnvironment(envs)
	})
	if err != nil {
		return kinsta.Environment{}, kinsta.Environment{}, err
	}
	live, ok = findKinstaLiveEnvironment(envs)
	if !ok {
		return kinsta.Environment{}, kinsta.Environment{}, fmt.Errorf("Kinsta site %s is missing live environment; found: %s", siteID, kinstaEnvironmentSummary(envs))
	}
	staging, ok := findKinstaStagingEnvironment(envs, live)
	if ok {
		if err := ensureKinstaEnvironmentPHP(ctx, client, staging, phpVersion); err != nil {
			return kinsta.Environment{}, kinsta.Environment{}, err
		}
		return live, staging, nil
	}
	fmt.Println("Creating Kinsta staging environment...")
	opID, err := client.CloneEnvironment(ctx, siteID, kinsta.CloneEnvironmentRequest{DisplayName: "Staging", IsPremium: false, SourceEnvID: live.ID})
	if err != nil {
		return kinsta.Environment{}, kinsta.Environment{}, err
	}
	if err := waitKinstaOperation(ctx, client, opID); err != nil {
		return kinsta.Environment{}, kinsta.Environment{}, err
	}
	envs, err = waitKinstaEnvironments(ctx, client, siteID, func(envs []kinsta.Environment) (kinsta.Environment, bool) {
		return findKinstaStagingEnvironment(envs, live)
	})
	if err != nil {
		return kinsta.Environment{}, kinsta.Environment{}, err
	}
	staging, ok = findKinstaStagingEnvironment(envs, live)
	if !ok {
		return kinsta.Environment{}, kinsta.Environment{}, fmt.Errorf("Kinsta staging environment was created but was not found in environment list; found: %s", kinstaEnvironmentSummary(envs))
	}
	if err := ensureKinstaEnvironmentPHP(ctx, client, staging, phpVersion); err != nil {
		return kinsta.Environment{}, kinsta.Environment{}, err
	}
	return live, staging, nil
}

func ensureKinstaEnvironmentPHP(ctx context.Context, client *kinsta.Client, env kinsta.Environment, phpVersion string) error {
	phpVersion = strings.TrimSpace(phpVersion)
	if phpVersion == "" || env.ID == "" || env.CurrentPHPVersion() == phpVersion {
		return nil
	}
	fmt.Printf("Setting Kinsta PHP %s on environment %s...\n", phpVersion, firstNonEmpty(env.Name, env.DisplayName, env.ID))
	opID, err := client.ModifyPHPVersion(ctx, kinsta.ModifyPHPVersionRequest{EnvironmentID: env.ID, PHPVersion: phpVersion, IsOptOutFromAutomaticPHPUpdate: false})
	if err != nil {
		return err
	}
	return waitKinstaOperation(ctx, client, opID)
}

func waitKinstaEnvironments(ctx context.Context, client *kinsta.Client, siteID string, ready func([]kinsta.Environment) (kinsta.Environment, bool)) ([]kinsta.Environment, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var last []kinsta.Environment
	for {
		envs, err := client.ListEnvironments(ctx, siteID)
		if err != nil {
			return nil, err
		}
		last = envs
		if _, ok := ready(envs); ok {
			return envs, nil
		}
		select {
		case <-ctx.Done():
			return last, fmt.Errorf("timed out waiting for Kinsta environments for site %s; found: %s", siteID, kinstaEnvironmentSummary(last))
		case <-ticker.C:
		}
	}
}

func findKinstaLiveEnvironment(envs []kinsta.Environment) (kinsta.Environment, bool) {
	if live, ok := kinsta.FindEnvironment(envs, "live"); ok {
		return live, true
	}
	if len(envs) == 1 {
		return envs[0], true
	}
	return kinsta.Environment{}, false
}

func findKinstaStagingEnvironment(envs []kinsta.Environment, live kinsta.Environment) (kinsta.Environment, bool) {
	if staging, ok := kinsta.FindEnvironment(envs, "staging"); ok {
		return staging, true
	}
	if len(envs) == 2 {
		for _, env := range envs {
			if env.ID != live.ID {
				return env, true
			}
		}
	}
	return kinsta.Environment{}, false
}

func kinstaEnvironmentSummary(envs []kinsta.Environment) string {
	if len(envs) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(envs))
	for _, env := range envs {
		parts = append(parts, fmt.Sprintf("id=%s name=%q display_name=%q", env.ID, env.Name, env.DisplayName))
	}
	return strings.Join(parts, "; ")
}

func ensureKinstaDomain(ctx context.Context, client *kinsta.Client, envID, domainName string) (kinsta.Domain, error) {
	domains, err := client.ListDomains(ctx, envID)
	if err != nil {
		return kinsta.Domain{}, err
	}
	if domain, ok := kinsta.FindDomain(domains, domainName); ok {
		return domain, nil
	}
	fmt.Printf("Adding Kinsta domain %s...\n", domainName)
	opID, err := client.AddDomain(ctx, envID, kinsta.AddDomainRequest{DomainName: domainName, IsWildcardless: false, AddWithWWWSubdomain: false, SetupType: "quick"})
	if err != nil {
		return kinsta.Domain{}, err
	}
	if err := waitKinstaOperation(ctx, client, opID); err != nil {
		return kinsta.Domain{}, err
	}
	domains, err = client.ListDomains(ctx, envID)
	if err != nil {
		return kinsta.Domain{}, err
	}
	if domain, ok := kinsta.FindDomain(domains, domainName); ok {
		return domain, nil
	}
	return kinsta.Domain{}, fmt.Errorf("Kinsta domain %q was added but was not found in domain list", domainName)
}

func waitKinstaOperation(parent context.Context, client *kinsta.Client, opID string) error {
	ctx, cancel := context.WithTimeout(parent, 30*time.Minute)
	defer cancel()
	return client.WaitOperation(ctx, opID, 5*time.Second)
}

func upsertKinstaDNSRecords(token, accountID, zone, domain string, records kinsta.DomainRecords) error {
	all := append([]kinsta.DNSRecord{}, records.Verification...)
	all = append(all, records.Pointing...)
	for _, record := range all {
		fqdn := record.RecordName()
		if !kinstaDNSRecordBelongsToDomain(fqdn, domain) {
			continue
		}
		name := dnsimpleRelativeName(fqdn, zone)
		recordType := strings.ToUpper(record.RecordTypeName())
		content := record.RecordContent()
		if fqdn == "" || recordType == "" || content == "" {
			continue
		}
		ttl := record.TTL
		if ttl <= 0 {
			ttl = 300
		}
		if err := upsertDNSRecordFn(token, accountID, zone, name, recordType, content, ttl); err != nil {
			return err
		}
	}
	return nil
}

func kinstaDNSRecordBelongsToDomain(recordName, domain string) bool {
	recordName = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(recordName), "."))
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if recordName == "" || domain == "" {
		return false
	}
	return recordName == domain || strings.HasSuffix(recordName, "."+domain)
}

func dnsimpleRelativeName(fqdn, zone string) string {
	fqdn = strings.TrimSuffix(strings.TrimSpace(fqdn), ".")
	zone = strings.TrimSuffix(strings.TrimSpace(zone), ".")
	if zone == "" || fqdn == zone {
		return ""
	}
	suffix := "." + zone
	if strings.HasSuffix(strings.ToLower(fqdn), strings.ToLower(suffix)) {
		return strings.TrimSuffix(fqdn, suffix)
	}
	return fqdn
}

func dnsimpleFQDNForRelativeName(name, zone string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	zone = strings.TrimSuffix(strings.TrimSpace(zone), ".")
	if name == "" {
		return zone
	}
	if zone == "" || strings.HasSuffix(strings.ToLower(name), "."+strings.ToLower(zone)) || strings.EqualFold(name, zone) {
		return name
	}
	return name + "." + zone
}

func dnsimpleTLSChallengeName(name string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	if name == "" {
		return "_acme-challenge"
	}
	return "_acme-challenge." + name
}

func ensureSiteNotCached(records []map[string]any, site string) error {
	for _, record := range records {
		if siteEnvMatchesSite(record, site) {
			return ProjectError{Msg: fmt.Sprintf("Site %q already exists in local site cache.", site)}
		}
	}
	return nil
}

func runSSHScript(user, host, script string) error {
	destination := host
	if strings.TrimSpace(user) != "" {
		destination = user + "@" + host
	}
	cmd := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", destination, "sudo", "bash", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runSSHCommand(args []string) error {
	cmd := exec.Command(args[0], args[1:]...)
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

func runSSHOutput(args []string) ([]byte, error) {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

func renderSiteAddScript(plan siteAddPlan) string {
	q := shellQuoteArg
	phpVersion := firstNonEmpty(plan.PHPVersion, "8.3")
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	b.WriteString("export DEBIAN_FRONTEND=noninteractive\n")
	b.WriteString("if ! command -v wp >/dev/null 2>&1; then\n")
	b.WriteString("  curl -fsSL https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar -o /usr/local/bin/wp\n")
	b.WriteString("  chmod 0755 /usr/local/bin/wp\n")
	b.WriteString("fi\n")
	b.WriteString("install -d -o www-data -g www-data -m 2775 /var/www/sites /var/log/nginx/sites /var/lib/nf\n")
	b.WriteString("touch /var/lib/nf/sites.json\n")
	b.WriteString("if ! jq empty /var/lib/nf/sites.json >/dev/null 2>&1; then printf '[]\\n' >/var/lib/nf/sites.json; fi\n")
	b.WriteString("target_php_version=$(jq -r '.php_version // .php.version // \"\"' /var/lib/nf/target.json 2>/dev/null || true)\n")
	b.WriteString("if [ -z \"$target_php_version\" ]; then target_php_version=")
	b.WriteString(q(phpVersion))
	b.WriteString("; fi\n")
	b.WriteString("create_env() {\n")
	b.WriteString("  env_name=$1 site_path=$2 db_name=$3 host_name=$4 site_url=$5 site_title=$6 state_target=$7 file_slug=$8\n")
	b.WriteString("  install -d -o www-data -g www-data -m 2775 \"$site_path\"\n")
	b.WriteString("  mariadb -uroot <<SQL\n")
	b.WriteString("CREATE DATABASE IF NOT EXISTS \\`$db_name\\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n")
	b.WriteString("CREATE USER IF NOT EXISTS '$db_name'@'localhost' IDENTIFIED BY '")
	b.WriteString(plan.DBPassword)
	b.WriteString("';\n")
	b.WriteString("ALTER USER '$db_name'@'localhost' IDENTIFIED BY '")
	b.WriteString(plan.DBPassword)
	b.WriteString("';\n")
	b.WriteString("GRANT ALL PRIVILEGES ON \\`$db_name\\`.* TO '$db_name'@'localhost';\n")
	b.WriteString("FLUSH PRIVILEGES;\n")
	b.WriteString("SQL\n")
	b.WriteString("  if [ ! -f \"$site_path/wp-load.php\" ]; then sudo -u www-data wp core download --path=\"$site_path\" --allow-root; fi\n")
	b.WriteString("  sudo -u www-data wp config create --path=\"$site_path\" --dbname=\"$db_name\" --dbuser=\"$db_name\" --dbpass=")
	b.WriteString(q(plan.DBPassword))
	b.WriteString(" --dbhost=localhost --skip-check --force --allow-root\n")
	b.WriteString("  if ! sudo -u www-data wp core is-installed --path=\"$site_path\" --allow-root >/dev/null 2>&1; then\n")
	b.WriteString("    sudo -u www-data wp core install --path=\"$site_path\" --url=\"$site_url\" --title=\"$site_title\" --admin_user=")
	b.WriteString(q(plan.AdminUser))
	b.WriteString(" --admin_password=")
	b.WriteString(q(plan.AdminPassword))
	b.WriteString(" --admin_email=")
	b.WriteString(q(plan.AdminEmail))
	b.WriteString(" --skip-email --allow-root\n")
	b.WriteString("  fi\n")
	b.WriteString("  cat >/etc/nginx/sites-available/nf-site-$file_slug <<EOF\n")
	b.WriteString("server {\n    listen 80;\n    listen [::]:80;\n    server_name $host_name;\n    return 301 https://$host_name\\$request_uri;\n}\n\n")
	b.WriteString("server {\n    listen 443 ssl http2;\n    listen [::]:443 ssl http2;\n    server_name $host_name;\n    include /etc/nginx/snippets/nf-wildcard-cert.conf;\n    include /etc/nginx/snippets/nf-security-headers.conf;\n    root $site_path;\n    access_log /var/log/nginx/sites/$file_slug.access.log;\n    error_log /var/log/nginx/sites/$file_slug.error.log;\n    include /etc/nginx/snippets/nf-wordpress.conf;\n    include /etc/nginx/snippets/nf-static-assets.conf;\n    location ~ \\.php$ { include /etc/nginx/snippets/nf-fastcgi-php.conf; fastcgi_pass unix:/run/php/php${target_php_version}-fpm.sock; }\n}\n")
	b.WriteString("EOF\n")
	b.WriteString("  ln -sf /etc/nginx/sites-available/nf-site-$file_slug /etc/nginx/sites-enabled/nf-site-$file_slug\n")
	b.WriteString("  tmp=$(mktemp)\n")
	b.WriteString("  jq --arg provider linode --arg site_id ")
	b.WriteString(q(plan.SiteID))
	b.WriteString(" --arg name ")
	b.WriteString(q(plan.Site))
	b.WriteString(" --arg env_id \"$state_target\" --arg env \"$env_name\" --arg target ")
	b.WriteString(q(plan.TargetName))
	b.WriteString(" --arg ssh_user ")
	b.WriteString(q(plan.SSHUser))
	b.WriteString(" --arg ssh_host ")
	b.WriteString(q(plan.SSHHost))
	b.WriteString(" --arg ssh_port ")
	b.WriteString(q(firstNonEmpty(mapStringAtPath(plan.Target, "ssh", "port"), firstRecordString(plan.Target, "ssh_port"), "22")))
	b.WriteString(" --arg hostname \"$host_name\" --arg url \"$site_url\" --arg path \"$site_path\" --arg database \"$db_name\" --arg php_version \"$target_php_version\" '\n")
	b.WriteString("    map(select(.site_id != $site_id or .env != $env)) + [{provider:$provider,env_id:$env_id,site_id:$site_id,name:$name,env:$env,target:$target,hostname:$hostname,url:$url,path:$path,database:$database,php_version:$php_version,status:\"active\",ssh:{host:$ssh_host,port:$ssh_port,user:$ssh_user,command:(\"ssh \" + $ssh_user + \"@\" + $ssh_host + \" -p \" + $ssh_port)}}]\n")
	b.WriteString("  ' /var/lib/nf/sites.json >\"$tmp\" && install -o ")
	b.WriteString(q(plan.SSHUser))
	b.WriteString(" -g www-data -m 0664 \"$tmp\" /var/lib/nf/sites.json && rm -f \"$tmp\"\n")
	b.WriteString("}\n")
	for _, env := range plan.Envs {
		stateTarget := linodeEnvID(plan.Site, plan.TargetName, env.Env)
		b.WriteString("create_env ")
		b.WriteString(q(env.Env))
		b.WriteByte(' ')
		b.WriteString(q(env.Path))
		b.WriteByte(' ')
		b.WriteString(q(env.Database))
		b.WriteByte(' ')
		b.WriteString(q(env.Hostname))
		b.WriteByte(' ')
		b.WriteString(q(env.URL))
		b.WriteByte(' ')
		b.WriteString(q(env.Title))
		b.WriteByte(' ')
		b.WriteString(q(stateTarget))
		b.WriteByte(' ')
		b.WriteString(q(envIDFileSlug(stateTarget)))
		b.WriteByte('\n')
	}
	b.WriteString("nginx -t\n")
	b.WriteString("systemctl reload nginx\n")
	b.WriteString("systemctl reload php${target_php_version}-fpm || systemctl restart php${target_php_version}-fpm\n")
	return b.String()
}

func printSiteAddPlan(plan siteAddPlan, mode string) {
	fmt.Println("Add site plan:")
	fmt.Printf("  target: %s\n", plan.TargetName)
	fmt.Printf("  provider: linode\n")
	fmt.Printf("  ssh: %s@%s\n", plan.SSHUser, plan.SSHHost)
	fmt.Printf("  site: %s\n", plan.Site)
	fmt.Printf("  site id: %s\n", plan.SiteID)
	fmt.Printf("  admin user: %s\n", plan.AdminUser)
	fmt.Printf("  admin email: %s\n", plan.AdminEmail)
	fmt.Printf("  admin password: derived from %s\n", plan.Site)
	for _, env := range plan.Envs {
		fmt.Printf("  env %s:\n", env.Env)
		fmt.Printf("    path: %s\n", env.Path)
		fmt.Printf("    database: %s\n", env.Database)
		if plan.PHPVersion != "" {
			fmt.Printf("    php: %s\n", plan.PHPVersion)
		}
		fmt.Printf("    vhost: %s\n", env.Hostname)
		fmt.Printf("    url: %s\n", env.URL)
	}
	fmt.Printf("  remote state: /var/lib/nf/sites.json\n")
	fmt.Printf("  local state: %s\n", state.StatePath("sites"))
	fmt.Printf("  mode: %s\n", mode)
}

func cmdSiteAdd(args siteAddArgs) int {
	provider, err := siteAddTargetProvider(args.target)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if provider == "kinsta" {
		return cmdKinstaSiteAdd(args)
	}
	if strings.TrimSpace(args.phpVersion) != "" {
		fmt.Fprintln(os.Stderr, "--php only applies to kinsta targets")
		return 1
	}
	if args.execute && args.dryRun {
		fmt.Fprintln(os.Stderr, "Choose either --execute or --dry-run, not both.")
		return 1
	}
	if !args.execute && (args.dryRun || args.nonInteractive) {
		args.dryRun = true
	}
	if args.nonInteractive && args.execute && !args.yes {
		fmt.Fprintln(os.Stderr, "Remote execution requires both --execute and --yes in non-interactive mode.")
		return 1
	}
	plan, err := buildSiteAddPlan(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	existing, err := state.LoadStateRecords("sites")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := ensureSiteNotCached(existing, plan.SiteID); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	willExecute := args.execute || (!args.dryRun && !args.nonInteractive)
	mode := "dry-run"
	if willExecute {
		mode = "execute"
	}
	printSiteAddPlan(plan, mode)
	if !willExecute {
		return 0
	}
	if !args.yes {
		confirmed, err := ui.Confirm(fmt.Sprintf("Add site %q with live and staging envs on target %q?", plan.Site, plan.TargetName), false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	if err := runSSHScriptFn(plan.SSHUser, plan.SSHHost, renderSiteAddScript(plan)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := appendSiteAddRecords(plan); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Site added.")
	return 0
}

func siteAddTargetProvider(targetRef string) (string, error) {
	targets, err := cachedTargets()
	if err != nil {
		return "", err
	}
	target := state.MatchingRecord(targets, targetRef)
	if target == nil {
		return "", ProjectError{Msg: fmt.Sprintf("No target matched %q.", targetRef)}
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(target["provider"])))
	if provider == "" {
		return "", ProjectError{Msg: fmt.Sprintf("Target %q is missing provider.", targetRef)}
	}
	return provider, nil
}

func buildSiteRemovePlan(siteID string) (siteRemovePlan, error) {
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		return siteRemovePlan{}, err
	}
	matches, resolvedSiteID, err := siteRecordsMatchingSite(records, siteID)
	if err != nil {
		return siteRemovePlan{}, err
	}
	if len(matches) == 0 {
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("No site matched %q.", siteID)}
	}
	first := matches[0]
	provider := strings.ToLower(strings.TrimSpace(recordValueString(first["provider"])))
	if provider == "kinsta" {
		return buildKinstaSiteRemovePlan(matches, resolvedSiteID)
	}
	if provider != "linode" {
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("Unsupported provider %q. Only linode and kinsta site remove are available.", provider)}
	}
	targetName := siteProviderTarget(first)
	if targetName == "" {
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("Site %q is missing a target.", siteID)}
	}
	targets, err := cachedTargets()
	if err != nil {
		return siteRemovePlan{}, err
	}
	target := state.MatchingRecord(targets, targetName)
	if target == nil {
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("No target matched site target %q.", targetName)}
	}
	sshHost := serverSSHHost(target)
	if sshHost == "" {
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("Target %q is missing an SSH host.", targetName)}
	}
	values, err := loadGlobalConfig()
	if err != nil {
		return siteRemovePlan{}, err
	}
	sshUser := firstNonEmpty(serverSSHUser(target), values["linode_default_user"])
	if sshUser == "" {
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("Target %q is missing an SSH user. Set linode_default_user with nf config set-linode-default-user <user>.", targetName)}
	}
	plan := siteRemovePlan{
		SiteID:     resolvedSiteID,
		Name:       siteRecordName(first),
		Provider:   provider,
		Target:     target,
		TargetName: targetName,
		SSHUser:    sshUser,
		SSHHost:    sshHost,
		Envs:       make([]siteRemoveEnvPlan, 0, len(matches)),
	}
	for _, record := range matches {
		env := siteRemoveEnvPlan{
			Env:      siteEnvName(record),
			EnvID:    firstRecordString(record, "env_id"),
			Path:     firstRecordString(record, "path"),
			Database: firstRecordString(record, "database", "db_name"),
			Hostname: firstRecordString(record, "hostname", "url"),
		}
		if env.EnvID == "" {
			env.EnvID = linodeEnvID(siteRecordName(record), targetName, env.Env)
		}
		if err := validateSiteRemoveEnv(env); err != nil {
			return siteRemovePlan{}, err
		}
		plan.Envs = append(plan.Envs, env)
	}
	sort.SliceStable(plan.Envs, func(i, j int) bool {
		left, right := siteListEnvOrder(plan.Envs[i].Env), siteListEnvOrder(plan.Envs[j].Env)
		if left != right {
			return left < right
		}
		return plan.Envs[i].Env < plan.Envs[j].Env
	})
	return plan, nil
}

func buildKinstaSiteRemovePlan(matches []map[string]any, resolvedSiteID string) (siteRemovePlan, error) {
	first := matches[0]
	values, err := loadGlobalConfig()
	if err != nil {
		return siteRemovePlan{}, err
	}
	dnsZone := strings.TrimSuffix(strings.TrimSpace(values["base_domain"]), ".")
	if dnsZone == "" {
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("Expected base_domain in %s. Set it with nf config set-base-domain <domain>.", config.ConfigFile())}
	}
	dnsAccountID := firstNonEmpty(values["dnsimple_account_id"], dnsimpleAccountIDValue())
	if dnsAccountID == "" {
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("Expected dnsimple_account_id in %s. Run nf provider check dnsimple.", config.ConfigFile())}
	}
	targetName := firstNonEmpty(siteProviderTarget(first), "kinsta")
	kinstaSiteID := ""
	plan := siteRemovePlan{
		SiteID:       resolvedSiteID,
		Name:         siteRecordName(first),
		Provider:     "kinsta",
		TargetName:   targetName,
		DNSZone:      dnsZone,
		DNSAccountID: dnsAccountID,
		Envs:         make([]siteRemoveEnvPlan, 0, len(matches)),
	}
	if targets, err := cachedTargets(); err == nil {
		if target := state.MatchingRecord(targets, targetName); target != nil {
			plan.Target = target
		}
	}
	for _, record := range matches {
		if kinstaSiteID == "" {
			kinstaSiteID = siteKinstaID(record, "site_id")
		}
		hostname := kinstaRemoveEnvHostname(record)
		env := siteRemoveEnvPlan{
			Env:      siteEnvName(record),
			EnvID:    firstNonEmpty(siteKinstaID(record, "environment_id"), firstRecordString(record, "env_id")),
			DomainID: siteKinstaID(record, "domain_id"),
			Path:     firstNonEmpty(siteKinstaID(record, "path"), firstRecordString(record, "path")),
			Database: firstNonEmpty(siteKinstaID(record, "database"), firstRecordString(record, "database", "db_name")),
			Hostname: hostname,
		}
		if strings.TrimSpace(env.Env) == "" {
			return siteRemovePlan{}, ProjectError{Msg: "Selected Kinsta site has an env with no name."}
		}
		if strings.TrimSpace(env.EnvID) == "" {
			return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("Selected Kinsta site env %q has no environment_id.", env.Env)}
		}
		plan.Envs = append(plan.Envs, env)
		if hostname != "" {
			plan.DNSNames = append(plan.DNSNames, dnsimpleRelativeName(hostname, dnsZone))
		}
	}
	if strings.TrimSpace(kinstaSiteID) == "" {
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("Selected Kinsta site %q has no Kinsta site_id. Run nf site refresh and try again.", resolvedSiteID)}
	}
	plan.KinstaSiteID = kinstaSiteID
	plan.DNSNames = uniqueNonEmptyStrings(plan.DNSNames)
	sort.SliceStable(plan.Envs, func(i, j int) bool {
		left, right := siteListEnvOrder(plan.Envs[i].Env), siteListEnvOrder(plan.Envs[j].Env)
		if left != right {
			return left < right
		}
		return plan.Envs[i].Env < plan.Envs[j].Env
	})
	return plan, nil
}

func kinstaRemoveEnvHostname(record map[string]any) string {
	for _, value := range []string{firstRecordString(record, "hostname"), firstRecordString(record, "url", "site_url", "home_url")} {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if strings.Contains(value, "://") {
			parsed, err := url.Parse(value)
			if err == nil && strings.TrimSpace(parsed.Hostname()) != "" {
				return strings.TrimSpace(parsed.Hostname())
			}
		}
		return strings.TrimSuffix(value, "/")
	}
	return ""
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func sortedMapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func validateSiteRemoveEnv(env siteRemoveEnvPlan) error {
	if strings.TrimSpace(env.Env) == "" {
		return ProjectError{Msg: "Selected site has an env with no name."}
	}
	if strings.TrimSpace(env.EnvID) == "" {
		return ProjectError{Msg: fmt.Sprintf("Selected site env %q has no env_id.", env.Env)}
	}
	if !safeDatabaseName(env.Database) {
		return ProjectError{Msg: fmt.Sprintf("Selected site env %q has unsafe database name %q.", env.Env, env.Database)}
	}
	if !safeSitePath(env.Path) {
		return ProjectError{Msg: fmt.Sprintf("Selected site env %q has unsafe path %q.", env.Env, env.Path)}
	}
	return nil
}

func safeDatabaseName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func safeSitePath(sitePath string) bool {
	cleaned := path.Clean(sitePath)
	if strings.Contains(cleaned, "..") || !strings.HasPrefix(cleaned, "/var/www/sites/") {
		return false
	}
	if strings.HasSuffix(cleaned, "/public") {
		return true
	}
	rel := strings.TrimPrefix(cleaned, "/var/www/sites/")
	return rel != "" && !strings.Contains(rel, "/")
}

func printSiteRemovePlan(plan siteRemovePlan, mode string) {
	fmt.Println("Remove site plan:")
	fmt.Printf("  site id: %s\n", plan.SiteID)
	if plan.Name != "" {
		fmt.Printf("  site: %s\n", plan.Name)
	}
	fmt.Printf("  target: %s\n", plan.TargetName)
	fmt.Printf("  provider: %s\n", plan.Provider)
	if plan.Provider == "kinsta" {
		fmt.Printf("  kinsta site id: %s\n", plan.KinstaSiteID)
		fmt.Printf("  dns: dnsimple zone %s account %s\n", plan.DNSZone, plan.DNSAccountID)
		for _, name := range plan.DNSNames {
			fmt.Printf("  dns delete: A %s\n", dnsimpleFQDNForRelativeName(name, plan.DNSZone))
			fmt.Printf("  dns delete: TXT %s\n", dnsimpleFQDNForRelativeName(dnsimpleTLSChallengeName(name), plan.DNSZone))
		}
		for _, env := range plan.Envs {
			fmt.Printf("  env %s:\n", env.Env)
			fmt.Printf("    kinsta environment id: %s\n", env.EnvID)
			if env.DomainID != "" {
				fmt.Printf("    kinsta domain id: %s\n", env.DomainID)
			}
			if env.Hostname != "" {
				fmt.Printf("    domain: %s\n", env.Hostname)
			}
		}
		fmt.Printf("  remote actions: delete Kinsta environments, delete Kinsta site\n")
		fmt.Printf("  local state: %s\n", state.StatePath("sites"))
		fmt.Printf("  mode: %s\n", mode)
		return
	}
	fmt.Printf("  ssh: %s@%s\n", plan.SSHUser, plan.SSHHost)
	fmt.Println("  dns actions: none")
	for _, env := range plan.Envs {
		fmt.Printf("  env %s:\n", env.Env)
		fmt.Printf("    env id: %s\n", env.EnvID)
		fmt.Printf("    delete path: %s\n", env.Path)
		fmt.Printf("    drop database: %s\n", env.Database)
		if env.Hostname != "" {
			fmt.Printf("    vhost: %s\n", env.Hostname)
		}
	}
	fmt.Printf("  remote state: /var/lib/nf/sites.json\n")
	fmt.Printf("  local state: %s\n", state.StatePath("sites"))
	fmt.Printf("  mode: %s\n", mode)
}

func renderSiteRemoveScript(plan siteRemovePlan) string {
	q := shellQuoteArg
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	b.WriteString("remove_env() {\n")
	b.WriteString("  env_id=$1 file_slug=$2 site_path=$3 db_name=$4\n")
	b.WriteString("  rm -f /etc/nginx/sites-enabled/nf-site-$file_slug /etc/nginx/sites-available/nf-site-$file_slug\n")
	b.WriteString("  rm -f /var/log/nginx/sites/$file_slug.access.log /var/log/nginx/sites/$file_slug.error.log\n")
	b.WriteString("  if [ \"$file_slug\" != \"$env_id\" ]; then\n")
	b.WriteString("    rm -f /etc/nginx/sites-enabled/nf-site-$env_id /etc/nginx/sites-available/nf-site-$env_id\n")
	b.WriteString("    rm -f /var/log/nginx/sites/$env_id.access.log /var/log/nginx/sites/$env_id.error.log\n")
	b.WriteString("  fi\n")
	b.WriteString("  rm -rf -- \"$site_path\"\n")
	b.WriteString("  parent=$(dirname \"$site_path\")\n")
	b.WriteString("  if [ \"$parent\" != /var/www/sites ]; then rmdir --ignore-fail-on-non-empty -- \"$parent\" 2>/dev/null || true; fi\n")
	b.WriteString("  mariadb -uroot <<SQL\n")
	b.WriteString("DROP DATABASE IF EXISTS \\`$db_name\\`;\n")
	b.WriteString("DROP USER IF EXISTS '$db_name'@'localhost';\n")
	b.WriteString("FLUSH PRIVILEGES;\n")
	b.WriteString("SQL\n")
	b.WriteString("}\n")
	for _, env := range plan.Envs {
		b.WriteString("remove_env ")
		b.WriteString(q(env.EnvID))
		b.WriteByte(' ')
		b.WriteString(q(envIDFileSlug(env.EnvID)))
		b.WriteByte(' ')
		b.WriteString(q(env.Path))
		b.WriteByte(' ')
		b.WriteString(q(env.Database))
		b.WriteByte('\n')
	}
	b.WriteString("if [ -f /var/lib/nf/sites.json ]; then\n")
	b.WriteString("  tmp=$(mktemp)\n")
	b.WriteString("  jq --arg site_id ")
	b.WriteString(q(plan.SiteID))
	b.WriteString(" 'map(select(.site_id != $site_id))' /var/lib/nf/sites.json >\"$tmp\" && install -o ")
	b.WriteString(q(plan.SSHUser))
	b.WriteString(" -g www-data -m 0664 \"$tmp\" /var/lib/nf/sites.json && rm -f \"$tmp\"\n")
	b.WriteString("fi\n")
	b.WriteString("nginx -t\n")
	b.WriteString("systemctl reload nginx\n")
	b.WriteString("systemctl reload php8.3-fpm || systemctl restart php8.3-fpm\n")
	return b.String()
}

func removeSiteFromLocalCache(siteID string) error {
	_, err := state.DeleteStateRecords("sites", func(record map[string]any) bool {
		return normalizedRecordString(siteRecordID(record)) == normalizedRecordString(siteID)
	})
	return err
}

func removeKinstaSite(plan siteRemovePlan) error {
	token := envwizard.Value("KINSTA_API_KEY")
	if token == "" {
		return fmt.Errorf("Expected KINSTA_API_KEY in the environment or %s.", config.EnvFile())
	}
	dnsToken := envwizard.Value("DNSIMPLE_TOKEN")
	if dnsToken == "" {
		return fmt.Errorf("Expected DNSIMPLE_TOKEN in the environment or %s.", config.EnvFile())
	}
	client := kinsta.NewClient(firstNonEmpty(envwizard.Value("KINSTA_BASE_URL"), "https://api.kinsta.com/v2"), token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	aRecords := map[string]bool{}
	txtRecords := map[string]bool{}
	for _, name := range plan.DNSNames {
		aRecords[name] = true
		txtRecords[dnsimpleTLSChallengeName(name)] = true
	}
	for _, env := range plan.Envs {
		if env.DomainID == "" {
			continue
		}
		records, err := client.DomainRecords(ctx, env.DomainID)
		if err != nil {
			return err
		}
		for _, record := range append(append([]kinsta.DNSRecord{}, records.Pointing...), records.Verification...) {
			fqdn := record.RecordName()
			if !kinstaDNSRecordBelongsToDomain(fqdn, env.Hostname) {
				continue
			}
			name := dnsimpleRelativeName(fqdn, plan.DNSZone)
			switch strings.ToUpper(record.RecordTypeName()) {
			case "A":
				aRecords[name] = true
			case "TXT":
				txtRecords[name] = true
			}
		}
	}
	for _, name := range sortedMapKeys(aRecords) {
		fmt.Printf("Deleting DNS A %s...\n", dnsimpleFQDNForRelativeName(name, plan.DNSZone))
		if err := deleteDNSRecordFn(dnsToken, plan.DNSAccountID, plan.DNSZone, name); err != nil {
			return err
		}
	}
	for _, txtName := range sortedMapKeys(txtRecords) {
		fmt.Printf("Deleting DNS TXT %s...\n", dnsimpleFQDNForRelativeName(txtName, plan.DNSZone))
		if err := deleteDNSTXTRecordFn(dnsToken, plan.DNSAccountID, plan.DNSZone, txtName); err != nil {
			return err
		}
	}
	for _, env := range plan.Envs {
		fmt.Printf("Deleting Kinsta environment %s (%s)...\n", env.Env, env.EnvID)
		opID, err := client.DeleteEnvironment(ctx, env.EnvID)
		if err != nil {
			return err
		}
		if err := waitKinstaOperation(ctx, client, opID); err != nil {
			return err
		}
	}
	fmt.Printf("Deleting Kinsta site %s...\n", plan.KinstaSiteID)
	opID, err := client.DeleteSite(ctx, plan.KinstaSiteID)
	if err != nil {
		return err
	}
	return waitKinstaOperation(ctx, client, opID)
}

func cmdSiteRemove(siteID string, dryRun, execute, yes, nonInteractive bool) int {
	if execute && dryRun {
		fmt.Fprintln(os.Stderr, "Choose either --execute or --dry-run, not both.")
		return 1
	}
	if nonInteractive && execute && !yes {
		fmt.Fprintln(os.Stderr, "Remote execution requires both --execute and --yes in non-interactive mode.")
		return 1
	}
	if !execute && (dryRun || nonInteractive) {
		dryRun = true
	}
	plan, err := buildSiteRemovePlan(siteID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	willExecute := execute || (!dryRun && !nonInteractive)
	mode := "dry-run"
	if willExecute {
		mode = "execute"
	}
	printSiteRemovePlan(plan, mode)
	if !willExecute {
		return 0
	}
	if !yes {
		message := fmt.Sprintf("Remove site %q from target %q and delete its databases and files?", plan.SiteID, plan.TargetName)
		if plan.Provider == "kinsta" {
			message = fmt.Sprintf("Remove Kinsta site %q, delete all cached Kinsta environments, and free it from the Kinsta account?", plan.SiteID)
		}
		confirmed, err := ui.Confirm(message, false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	if plan.Provider == "kinsta" {
		if err := kinstaRemoveSiteFn(plan); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	} else {
		if err := runSSHScriptFn(plan.SSHUser, plan.SSHHost, renderSiteRemoveScript(plan)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if err := removeSiteFromLocalCache(plan.SiteID); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Site removed.")
	return 0
}

func cmdListSites(records, servers []map[string]any) int {
	if len(records) == 0 {
		fmt.Println("No sites found.")
		return 0
	}
	type siteListRow struct {
		SiteID     string
		Name       string
		Target     string
		Envs       map[string]bool
		FirstIndex int
	}
	grouped := map[string]*siteListRow{}
	for _, record := range records {
		siteID := siteEnvDisplaySite(record)
		if siteID == "" {
			siteID = siteSummary(record)
		}
		if siteID == "" {
			continue
		}
		row := grouped[siteID]
		if row == nil {
			row = &siteListRow{SiteID: siteID, Name: siteRecordName(record), Target: siteProviderTarget(record), Envs: map[string]bool{}, FirstIndex: len(grouped)}
			grouped[siteID] = row
		}
		if row.Name == "" {
			row.Name = siteRecordName(record)
		}
		if row.Target == "" {
			row.Target = siteProviderTarget(record)
		}
		env := siteEnvName(record)
		row.Envs[env] = true
	}
	if len(grouped) == 0 {
		fmt.Println("No sites found.")
		return 0
	}
	rowsBySite := make([]*siteListRow, 0, len(grouped))
	for _, row := range grouped {
		rowsBySite = append(rowsBySite, row)
	}
	sort.Slice(rowsBySite, func(i, j int) bool { return rowsBySite[i].FirstIndex < rowsBySite[j].FirstIndex })
	rows := [][]string{{"site id", "name", "target", "envs"}}
	for _, row := range rowsBySite {
		rows = append(rows, []string{
			row.SiteID,
			row.Name,
			row.Target,
			strings.Join(sortedSiteListEnvs(row.Envs), ","),
		})
	}
	fmt.Println(formatTable(rows))
	return 0
}

func cmdListSiteEnvs(siteID string) int {
	bundle, err := state.LoadStateBundle()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	rows := [][]string{{"env id", "site", "env", "php", "url"}}
	for _, record := range bundle.Sites {
		if !siteEnvMatchesSite(record, siteID) {
			continue
		}
		rows = append(rows, []string{
			siteRecordEnvID(record),
			siteEnvDisplaySite(record),
			siteEnvName(record),
			sitePHPVersion(record),
			firstRecordString(record, "url", "site_url", "home_url", "hostname"),
		})
	}
	if len(rows) == 1 {
		if strings.TrimSpace(siteID) != "" {
			fmt.Printf("No remote envs found for %q.\n", siteID)
		} else {
			fmt.Println("No remote envs found.")
		}
		return 0
	}
	fmt.Println(formatTable(rows))
	return 0
}

func cmdShowSiteRef(needle string, jsonOutput bool) int {
	if siteID, env, ok := splitSiteEnvRef(needle); ok {
		return cmdShowSiteEnv(siteID, env, jsonOutput)
	}
	return cmdShowSite(needle, jsonOutput)
}

func cmdShowSiteEnv(siteID, env string, jsonOutput bool) int {
	siteID, env = normalizeSiteEnvRequest(siteID, env)
	if siteID == "" || env == "" {
		fmt.Fprintln(os.Stderr, "site show requires a site or env ref")
		return 1
	}
	bundle, err := state.LoadStateBundle()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var record map[string]any
	for _, candidate := range bundle.Sites {
		if siteEnvMatchesSite(candidate, siteID) && siteEnvMatchesEnv(candidate, env) {
			record = candidate
			break
		}
	}
	if record == nil {
		fmt.Fprintf(os.Stderr, "No remote env matched site %q env %q.\n", siteID, env)
		return 1
	}
	if err := validateSiteRecord(record); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	out := siteEnvDetailsOutput(siteID, env, record)
	if err := enrichSiteOutput(out, record, bundle.Servers); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := enrichSiteAdminCredentials(out, record); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !jsonOutput {
		printSiteEnvDetails(out)
		return 0
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(data))
	return 0
}

func siteEnvDetailsOutput(siteID, env string, record map[string]any) map[string]any {
	out := cloneRecord(record)
	out["requested_site"] = siteID
	out["requested_env"] = env
	out["resolved_site"] = siteEnvDisplaySite(record)
	out["resolved_env"] = siteEnvName(record)
	out["resolved_target"] = siteProviderTarget(record)
	if phpVersion := sitePHPVersion(record); phpVersion != "" {
		out["php_version"] = phpVersion
	}
	if host := firstNonEmpty(mapStringAtPath(record, "ssh", "host"), mapStringAtPath(record, "kinsta", "ssh", "host")); host != "" {
		out["ssh_host"] = host
	}
	if port := firstNonEmpty(mapStringAtPath(record, "ssh", "port"), mapStringAtPath(record, "kinsta", "ssh", "port")); port != "" {
		out["ssh_port"] = port
	}
	if user := firstNonEmpty(mapStringAtPath(record, "ssh", "user"), mapStringAtPath(record, "kinsta", "ssh", "user")); user != "" {
		out["ssh_user"] = user
	}
	if command := firstNonEmpty(mapStringAtPath(record, "ssh", "command"), mapStringAtPath(record, "kinsta", "ssh", "command")); command != "" {
		out["ssh_command"] = command
	}
	return out
}

func printSiteEnvDetails(out map[string]any) {
	site := recordValueString(out["resolved_site"])
	env := recordValueString(out["resolved_env"])
	title := site
	if env != "" {
		title += ":" + env
	}
	if title != "" {
		fmt.Println(title)
		fmt.Println(strings.Repeat("─", len(title)))
	}
	printDetailRows([]detailRow{
		{label: "Site", value: site},
		{label: "Env", value: env},
		{label: "Provider", value: recordValueString(out["provider"])},
		{label: "Target", value: recordValueString(out["resolved_target"])},
		{label: "URL", value: firstRecordString(out, "url", "site_url", "home_url")},
		{label: "Path", value: firstRecordString(out, "path", "root", "document_root")},
		{label: "Branch", value: firstNonEmpty(firstRecordString(out, "branch"), mapStringAtPath(out, "kinsta", "branch"))},
		{label: "PHP", value: firstRecordString(out, "php_version")},
		{label: "Database", value: firstRecordString(out, "database", "db_name")},
	})
	requestedSite := recordValueString(out["requested_site"])
	if requestedSite != "" && site != "" && requestedSite != site {
		printDetailRows([]detailRow{{label: "Requested site", value: requestedSite}})
	}
	requestedEnv := recordValueString(out["requested_env"])
	if requestedEnv != "" && env != "" && requestedEnv != env {
		printDetailRows([]detailRow{{label: "Requested env", value: requestedEnv}})
	}
	providerRows := []detailRow{
		{label: "Kinsta site", value: firstRecordString(out, "kinsta_site_id")},
		{label: "Kinsta env", value: firstRecordString(out, "kinsta_environment_id")},
	}
	if hasDetailRows(providerRows) {
		fmt.Println()
		fmt.Println("Provider IDs")
		printIndentedDetailRows(providerRows, 2)
	}
	ssh := siteEnvSSHInfo(out)
	accessRows := []detailRow{
		{label: "SSH command", value: ssh.command()},
		{label: "Admin user", value: firstRecordString(out, "resolved_admin_user", "admin_user", "admin_username", "wp_admin_user", "wordpress_admin_user")},
		{label: "Admin pass", value: firstRecordString(out, "resolved_admin_password", "admin_password", "wp_admin_password", "wordpress_admin_password")},
	}
	if hasDetailRows(accessRows) {
		fmt.Println()
		fmt.Println("Access")
		printIndentedDetailRows(accessRows, 2)
	}
}

type detailRow struct {
	label string
	value string
}

func hasDetailRows(rows []detailRow) bool {
	for _, row := range rows {
		if strings.TrimSpace(row.value) != "" {
			return true
		}
	}
	return false
}

func printDetailRows(rows []detailRow) {
	printIndentedDetailRows(rows, 0)
}

func printIndentedDetailRows(rows []detailRow, indent int) {
	for _, line := range detailRowLines(rows, indent) {
		fmt.Println(line)
	}
}

func detailRowLines(rows []detailRow, indent int) []string {
	width := 0
	for _, row := range rows {
		if strings.TrimSpace(row.value) == "" {
			continue
		}
		if len(row.label) > width {
			width = len(row.label)
		}
	}
	if width == 0 {
		return nil
	}
	prefix := strings.Repeat(" ", indent)
	lines := []string{}
	for _, row := range rows {
		if strings.TrimSpace(row.value) == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s%-*s   %s", prefix, width, row.label, row.value))
	}
	return lines
}

type siteEnvSSHInfoValue struct {
	host       string
	port       string
	user       string
	sshCommand string
}

func siteEnvSSHInfo(record map[string]any) siteEnvSSHInfoValue {
	info := siteEnvSSHInfoValue{
		host:       firstRecordString(record, "ssh_host"),
		port:       firstRecordString(record, "ssh_port"),
		user:       firstRecordString(record, "ssh_user", "ssh_username"),
		sshCommand: firstRecordString(record, "ssh_command"),
	}
	if info.host == "" {
		info.host = firstNonEmpty(mapStringAtPath(record, "ssh", "host"), mapStringAtPath(record, "kinsta", "ssh", "host"))
	}
	if info.port == "" {
		info.port = firstNonEmpty(mapStringAtPath(record, "ssh", "port"), mapStringAtPath(record, "kinsta", "ssh", "port"))
	}
	if info.user == "" {
		info.user = firstNonEmpty(mapStringAtPath(record, "ssh", "user"), mapStringAtPath(record, "kinsta", "ssh", "user"))
	}
	if info.sshCommand == "" {
		info.sshCommand = firstNonEmpty(mapStringAtPath(record, "ssh", "command"), mapStringAtPath(record, "kinsta", "ssh", "command"))
	}
	target := mapMapAtPath(record, "resolved_target_record")
	if target != nil {
		if info.host == "" {
			info.host = serverSSHHost(target)
		}
		if info.port == "" {
			info.port = firstNonEmpty(mapStringAtPath(target, "ssh", "port"), firstRecordString(target, "ssh_port"))
		}
		if info.user == "" {
			info.user = serverSSHUser(target)
		}
	}
	return info
}

func (info siteEnvSSHInfoValue) command() string {
	if info.sshCommand != "" {
		return info.sshCommand
	}
	if info.host == "" {
		return ""
	}
	destination := info.host
	if info.user != "" {
		destination = info.user + "@" + destination
	}
	if info.port != "" {
		return "ssh " + destination + " -p " + info.port
	}
	return "ssh " + destination
}

func enrichSiteAdminCredentials(out, record map[string]any) error {
	if user := firstRecordString(record, "admin_user", "admin_username", "wp_admin_user", "wordpress_admin_user"); user != "" {
		out["resolved_admin_user"] = user
	} else {
		values, err := loadGlobalConfig()
		if err != nil {
			return err
		}
		out["resolved_admin_user"] = firstNonEmpty(values["default_wp_user"], "admin")
	}

	password, err := siteAdminPassword(record)
	if err != nil {
		if _, ok := err.(passwords.PasswordError); ok {
			return nil
		}
		return err
	}
	if password != "" {
		out["resolved_admin_password"] = password
	}
	return nil
}

func siteAdminPassword(record map[string]any) (string, error) {
	if password := firstRecordString(record, "admin_password", "wp_admin_password", "wordpress_admin_password"); password != "" {
		return password, nil
	}
	slug := sitePasswordSlug(record)
	if slug == "" {
		return "", nil
	}
	salt, err := passwords.SecretSalt()
	if err != nil {
		return "", err
	}
	return passwords.DerivePassword(slug, "wp-admin", salt), nil
}

func sitePasswordSlug(record map[string]any) string {
	if slug := firstRecordString(record, "password_scope", "admin_password_scope", "name", "site_name", "project", "project_slug", "wordpress_site"); slug != "" {
		return slug
	}
	siteID := siteEnvSiteID(record)
	target := siteProviderTarget(record)
	for _, suffix := range []string{"." + target, "-" + target} {
		if target != "" && strings.HasSuffix(siteID, suffix) {
			return strings.TrimSuffix(siteID, suffix)
		}
	}
	return siteID
}

func cachedSiteEnv(siteID, env string) (map[string]any, []map[string]any, error) {
	siteID, env = normalizeSiteEnvRequest(siteID, env)
	bundle, err := state.LoadStateBundle()
	if err != nil {
		return nil, nil, err
	}
	for _, candidate := range bundle.Sites {
		if siteEnvMatchesSite(candidate, siteID) && siteEnvMatchesEnv(candidate, env) {
			return candidate, bundle.Servers, nil
		}
	}
	return nil, bundle.Servers, nil
}

func cachedSiteTarget(targetRef string) (map[string]any, error) {
	targets, err := cachedTargets()
	if err != nil {
		return nil, err
	}
	return state.MatchingRecord(targets, targetRef), nil
}

type themeDeployTarget struct {
	Provider       string
	RemoteName     string
	SiteID         string
	Env            string
	URL            string
	SSHUser        string
	SSHHost        string
	SSHPort        string
	WordPressPath  string
	RemoteThemeDir string
	WPCommand      string
}

func cmdThemeDeploy(remoteName string, dryRun bool) int {
	remoteName = strings.TrimSpace(remoteName)
	if remoteName == "" {
		fmt.Fprintln(os.Stderr, "theme deploy requires a non-empty remote")
		return 1
	}
	root, err := discoverProjectRootOrError()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	themeSource := firstNonEmpty(mapStringAtPath(metadata, "wordpress", "theme_path"), "theme")
	if !filepath.IsAbs(themeSource) {
		themeSource = filepath.Join(root, themeSource)
	}
	info, err := os.Stat(themeSource)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "Theme source directory does not exist: %s\n", themeSource)
		return 1
	}
	themeSlug := firstNonEmpty(mapStringAtPath(metadata, "wordpress", "theme_slug"), filepath.Base(themeSource), "theme")
	if err := validateThemeDeploySlug(themeSlug); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	target, err := resolveThemeDeployTarget(remoteName, themeSlug, metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	fmt.Println("Theme deploy plan:")
	fmt.Printf("  remote:      %s\n", target.RemoteName)
	fmt.Printf("  site:        %s\n", target.SiteID)
	fmt.Printf("  env:         %s\n", target.Env)
	fmt.Printf("  provider:    %s\n", target.Provider)
	if target.URL != "" {
		fmt.Printf("  url:         %s\n", target.URL)
	}
	fmt.Printf("  source:      %s\n", themeSource)
	fmt.Printf("  destination: %s@%s:%s\n", target.SSHUser, target.SSHHost, target.RemoteThemeDir)
	if dryRun {
		fmt.Println("  mode:        dry-run")
	}

	sshArgs := themeDeployMkdirArgs(target)
	printCommandArgs(sshArgs)
	if !dryRun {
		if err := runSSHCommandFn(sshArgs); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	rsyncArgs := themeDeployRsyncArgs(themeSource, target, dryRun)
	if err := runRsyncCommandFn(rsyncArgs); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	activateArgs := themeDeployActivateArgs(target, themeSlug)
	printCommandArgs(activateArgs)
	if !dryRun {
		if err := runSSHCommandFn(activateArgs); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if dryRun {
		fmt.Println("No remote files were changed.")
	} else {
		fmt.Println("Theme deployed.")
	}
	return 0
}

func resolveThemeDeployTarget(remoteName, themeSlug string, metadata map[string]any) (themeDeployTarget, error) {
	siteID, remoteEnv, ok, err := projectRemoteAlias(metadata, remoteName)
	if err != nil {
		return themeDeployTarget{}, err
	}
	if !ok {
		return themeDeployTarget{}, ProjectError{Msg: fmt.Sprintf("No remote named %q in .nf/project.json deploy.remotes.", remoteName)}
	}
	record, _, err := cachedSiteEnv(siteID, remoteEnv)
	if err != nil {
		return themeDeployTarget{}, err
	}
	if record == nil {
		return themeDeployTarget{}, ProjectError{Msg: fmt.Sprintf("No cached remote env matched site %q env %q. Run nf site refresh after target cache is current, or update the local state cache.", siteID, remoteEnv)}
	}
	if err := validateSiteRecord(record); err != nil {
		return themeDeployTarget{}, err
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(record["provider"])))
	target := themeDeployTarget{Provider: provider, RemoteName: remoteName, SiteID: siteID, Env: remoteEnv, URL: firstRecordString(record, "url", "site_url", "home_url", "hostname")}
	switch provider {
	case "kinsta":
		target.SSHHost = firstNonEmpty(mapStringAtPath(record, "ssh", "host"), mapStringAtPath(record, "kinsta", "ssh", "host"), firstRecordString(record, "ssh_host"))
		target.SSHUser = firstNonEmpty(mapStringAtPath(record, "ssh", "user"), mapStringAtPath(record, "kinsta", "ssh", "user"), firstRecordString(record, "ssh_user", "ssh_username"))
		target.SSHPort = firstNonEmpty(mapStringAtPath(record, "ssh", "port"), mapStringAtPath(record, "kinsta", "ssh", "port"), firstRecordString(record, "ssh_port"), "22")
		target.WordPressPath = normalizeKinstaCachedPath(firstRecordString(record, "path"))
		target.WPCommand = "wp"
	case "linode":
		sshUser, sshHost, sshPort, wpPath, err := linodeThemeDeploySSHInfo(record)
		if err != nil {
			return themeDeployTarget{}, err
		}
		target.SSHUser = sshUser
		target.SSHHost = sshHost
		target.SSHPort = sshPort
		target.WordPressPath = wpPath
		target.WPCommand = "sudo -u www-data wp"
	default:
		return themeDeployTarget{}, ProjectError{Msg: fmt.Sprintf("Theme deploy is not implemented for provider %q; no files were changed.", provider)}
	}
	if target.SSHHost == "" {
		return themeDeployTarget{}, ProjectError{Msg: fmt.Sprintf("Site env %q is missing SSH host. Run nf site refresh.", siteSummary(record))}
	}
	if target.SSHUser == "" {
		return themeDeployTarget{}, ProjectError{Msg: fmt.Sprintf("Site env %q is missing SSH user. Run nf site refresh.", siteSummary(record))}
	}
	if target.WordPressPath == "" {
		return themeDeployTarget{}, ProjectError{Msg: fmt.Sprintf("Site env %q is missing path. Run nf site refresh.", siteSummary(record))}
	}
	target.RemoteThemeDir = path.Join(target.WordPressPath, "wp-content", "themes", themeSlug)
	return target, nil
}

func linodeThemeDeploySSHInfo(record map[string]any) (user, host, port, wpPath string, err error) {
	targetRef := siteProviderTarget(record)
	targets, err := cachedTargets()
	if err != nil {
		return "", "", "", "", err
	}
	target := state.MatchingRecord(targets, targetRef)
	if target == nil {
		return "", "", "", "", ProjectError{Msg: fmt.Sprintf("Linode site %q references target %q, but no cached target matched. Run nf provider check linode.", siteSummary(record), targetRef)}
	}
	values, err := loadGlobalConfig()
	if err != nil {
		return "", "", "", "", err
	}
	user = firstNonEmpty(mapStringAtPath(record, "ssh", "user"), firstRecordString(record, "ssh_user", "ssh_username"), serverSSHUser(target), values["linode_default_user"])
	if user == "" {
		return "", "", "", "", ProjectError{Msg: fmt.Sprintf("Target %q is missing an SSH user. Set linode_default_user with nf config set-linode-default-user <user>.", targetRef)}
	}
	host = firstNonEmpty(mapStringAtPath(record, "ssh", "host"), firstRecordString(record, "ssh_host"), serverSSHHost(target), firstRecordString(record, "hostname"))
	port = firstNonEmpty(mapStringAtPath(record, "ssh", "port"), firstRecordString(record, "ssh_port"), mapStringAtPath(target, "ssh", "port"), firstRecordString(target, "ssh_port"), "22")
	wpPath = firstRecordString(record, "path")
	return user, host, port, wpPath, nil
}

func themeDeployMkdirArgs(target themeDeployTarget) []string {
	return []string{"ssh", "-p", target.SSHPort, target.SSHUser + "@" + target.SSHHost, "mkdir -p " + shellQuoteArg(target.RemoteThemeDir)}
}

func themeDeployRsyncArgs(sourceDir string, target themeDeployTarget, dryRun bool) []string {
	args := []string{"rsync", "-az", "--delete"}
	if dryRun {
		args = append(args, "--dry-run")
	}
	args = append(args, "-e", "ssh -p "+target.SSHPort)
	return append(args, filepath.Clean(sourceDir)+string(filepath.Separator), target.SSHUser+"@"+target.SSHHost+":"+target.RemoteThemeDir+"/")
}

func themeDeployActivateArgs(target themeDeployTarget, themeSlug string) []string {
	remoteCommand := target.WPCommand + " --path=" + shellQuoteArg(target.WordPressPath) + " theme activate " + shellQuoteArg(themeSlug) + " --allow-root"
	return []string{"ssh", "-p", target.SSHPort, target.SSHUser + "@" + target.SSHHost, remoteCommand}
}

func resolveEnvRemoteSyncTarget(action, remoteName string, metadata map[string]any) (envRemoteSyncTarget, error) {
	remoteName = strings.TrimSpace(remoteName)
	if remoteName == "" {
		return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("env %s requires a non-empty remote", action)}
	}
	siteID, remoteEnv, ok, err := projectRemoteAlias(metadata, remoteName)
	if err != nil {
		return envRemoteSyncTarget{}, err
	}
	if !ok {
		return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("No remote named %q in .nf/project.json deploy.remotes.", remoteName)}
	}
	record, _, err := cachedSiteEnv(siteID, remoteEnv)
	if err != nil {
		return envRemoteSyncTarget{}, err
	}
	if record == nil {
		return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("No cached remote env matched site %q env %q. Run nf site refresh after target cache is current, or update the local state cache.", siteID, remoteEnv)}
	}
	target, err := envRemoteSyncTargetFromSiteRecord(record, remoteName, siteID, remoteEnv)
	if err != nil {
		return envRemoteSyncTarget{}, err
	}
	return target, nil
}

func envRemoteSyncTargetFromSiteRecord(record map[string]any, remoteName, siteID, remoteEnv string) (envRemoteSyncTarget, error) {
	if err := validateSiteRecord(record); err != nil {
		return envRemoteSyncTarget{}, err
	}
	if siteID == "" {
		siteID = siteRecordID(record)
	}
	if remoteEnv == "" {
		remoteEnv = siteEnvName(record)
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(record["provider"])))
	target := envRemoteSyncTarget{Provider: provider, RemoteName: remoteName, SiteID: siteID, Env: remoteEnv, URL: firstRecordString(record, "url", "site_url", "home_url", "hostname"), TargetLabel: "target", TargetRef: siteProviderTarget(record), AccessLabel: "target record"}
	switch provider {
	case "kinsta":
		target.TargetLabel = ""
		target.TargetRef = ""
		target.AccessLabel = "environment ssh"
		target.SSHHost = firstNonEmpty(mapStringAtPath(record, "ssh", "host"), mapStringAtPath(record, "kinsta", "ssh", "host"), firstRecordString(record, "ssh_host"))
		if target.SSHHost == "" {
			return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("Kinsta env %q is missing SSH host in the site cache. Run nf site refresh.", siteSummary(record))}
		}
		target.SSHUser = firstNonEmpty(mapStringAtPath(record, "ssh", "user"), mapStringAtPath(record, "kinsta", "ssh", "user"), firstRecordString(record, "ssh_user", "ssh_username"))
		if target.SSHUser == "" {
			return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("Kinsta env %q is missing SSH user in the site cache. Run nf site refresh.", siteSummary(record))}
		}
		target.SSHPort = firstNonEmpty(mapStringAtPath(record, "ssh", "port"), mapStringAtPath(record, "kinsta", "ssh", "port"), firstRecordString(record, "ssh_port"), "22")
		target.WordPressPath = normalizeKinstaCachedPath(firstRecordString(record, "path"))
		if target.WordPressPath == "" {
			return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("Kinsta env %q is missing path in the site cache. Run nf site refresh.", siteSummary(record))}
		}
		target.AccessSummary = fmt.Sprintf("%s@%s", target.SSHUser, target.SSHHost)
		target.WPCommand = "wp"
	case "linode":
		resolved, err := cachedSiteTarget(target.TargetRef)
		if err != nil {
			return envRemoteSyncTarget{}, err
		}
		if resolved == nil {
			return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("Linode site %q references target %q, but no cached target matched. Run nf provider check linode.", siteSummary(record), target.TargetRef)}
		}
		target.AccessSummary = serverSummary(resolved)
		target.SSHUser = firstNonEmpty(mapStringAtPath(record, "ssh", "user"), firstRecordString(record, "ssh_user", "ssh_username"), serverSSHUser(resolved))
		if target.SSHUser == "" {
			values, err := loadGlobalConfig()
			if err != nil {
				return envRemoteSyncTarget{}, err
			}
			target.SSHUser = values["linode_default_user"]
		}
		if target.SSHUser == "" {
			return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("Target %q is missing an SSH user. Set linode_default_user with nf config set-linode-default-user <user>.", target.TargetRef)}
		}
		target.SSHHost = firstNonEmpty(mapStringAtPath(record, "ssh", "host"), firstRecordString(record, "ssh_host"), serverSSHHost(resolved), firstRecordString(record, "hostname"))
		if target.SSHHost == "" {
			return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("Site env %q is missing hostname.", siteSummary(record))}
		}
		target.SSHPort = firstNonEmpty(mapStringAtPath(record, "ssh", "port"), firstRecordString(record, "ssh_port"), mapStringAtPath(resolved, "ssh", "port"), firstRecordString(resolved, "ssh_port"), "22")
		target.WordPressPath = firstRecordString(record, "path")
		if target.WordPressPath == "" {
			return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("Site env %q is missing path.", siteSummary(record))}
		}
		target.WPCommand = "sudo -u www-data wp"
		target.SudoFileOps = true
	default:
		return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("Remote env sync is not implemented for provider %q; no data was changed.", provider)}
	}
	return target, nil
}

func cmdEnvRemoteSyncPlan(action, remoteName string, cfg envConfig, metadata map[string]any, opts envRemoteSyncOptions) int {
	if opts.execute && opts.dryRun {
		fmt.Fprintln(os.Stderr, "Choose either --execute or --dry-run, not both.")
		return 1
	}
	if strings.TrimSpace(remoteName) == "" {
		if opts.nonInteractive {
			fmt.Fprintf(os.Stderr, "env %s requires a remote in non-interactive mode\n", action)
			return 1
		}
		selected, err := chooseProjectRemote(action)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		remoteName = selected
	}
	target, err := resolveEnvRemoteSyncTarget(action, remoteName, metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Env %s preflight:\n", action)
	fmt.Printf("  local project: %s\n", cfg.ProjectSlug)
	fmt.Printf("  local env:     %s\n", localEnvDir(cfg))
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
	willExecute := opts.execute || (!opts.dryRun && !opts.nonInteractive)
	mode := "execute"
	if !willExecute {
		mode = "dry-run"
	} else if opts.execute {
		mode = "execute"
	}
	fmt.Printf("  mode:          %s\n", mode)
	if !willExecute {
		fmt.Println("No data was changed. Re-run with --execute to sync database and mutable wp-content.")
		return 0
	}
	if target.Provider != "linode" && target.Provider != "kinsta" {
		fmt.Fprintf(os.Stderr, "Remote env sync execution is not implemented for provider %q; no data was changed.\n", target.Provider)
		return 1
	}
	if opts.nonInteractive && !opts.yes {
		fmt.Fprintln(os.Stderr, "Remote execution requires both --execute and --yes in non-interactive mode.")
		return 1
	}
	if !opts.yes {
		displayAction := action
		if displayAction != "" {
			displayAction = strings.ToUpper(displayAction[:1]) + displayAction[1:]
		}
		message := fmt.Sprintf("%s %s:%s %s local env %s? This syncs the database and mutable wp-content.", displayAction, target.SiteID, target.Env, map[string]string{"pull": "into", "push": "from"}[action], cfg.ProjectSlug)
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
	if action == "pull" {
		return executeEnvPull(cfg, target)
	}
	return executeEnvPush(cfg, target)
}

func remoteSyncTempDir(cfg envConfig, target envRemoteSyncTarget, action string) string {
	return path.Join("/tmp", "nf-"+action+"-"+cleanEnvSlug(cfg.ProjectSlug)+"-"+strconv.FormatInt(time.Now().Unix(), 10))
}

func remoteFileOpPrefix(target envRemoteSyncTarget) string {
	if target.SudoFileOps {
		return "sudo "
	}
	return ""
}

func remoteExportScript(target envRemoteSyncTarget, remoteTmp string) string {
	fileOp := remoteFileOpPrefix(target)
	return fmt.Sprintf(`set -eu
rm -rf %s
mkdir -p %s
chmod 777 %s
cd %s
%s --path=%s db export %s/database.sql
%sgzip -f %s/database.sql
%schmod 644 %s/database.sql.gz
dirs=""
for dir in wp-content/uploads wp-content/plugins wp-content/mu-plugins wp-content/languages; do
  if [ -e %s/$dir ]; then dirs="$dirs $dir"; fi
done
if [ -n "$dirs" ]; then %star -C %s -czf %s/wp-content.tar.gz $dirs; else %star -C %s -czf %s/wp-content.tar.gz --files-from /dev/null; fi
%schmod 644 %s/wp-content.tar.gz
`, shellQuoteArg(remoteTmp), shellQuoteArg(remoteTmp), shellQuoteArg(remoteTmp), shellQuoteArg(target.WordPressPath), target.WPCommand, shellQuoteArg(target.WordPressPath), shellQuoteArg(remoteTmp), fileOp, shellQuoteArg(remoteTmp), fileOp, shellQuoteArg(remoteTmp), shellQuoteArg(target.WordPressPath), fileOp, shellQuoteArg(target.WordPressPath), shellQuoteArg(remoteTmp), fileOp, shellQuoteArg(target.WordPressPath), shellQuoteArg(remoteTmp), fileOp, shellQuoteArg(remoteTmp))
}

func remoteImportScript(target envRemoteSyncTarget, remoteTmp string) string {
	fileOp := remoteFileOpPrefix(target)
	chown := ""
	if target.SudoFileOps {
		chown = fmt.Sprintf("sudo chown -R www-data:www-data %s/wp-content/uploads %s/wp-content/plugins %s/wp-content/mu-plugins %s/wp-content/languages 2>/dev/null || true\n", shellQuoteArg(target.WordPressPath), shellQuoteArg(target.WordPressPath), shellQuoteArg(target.WordPressPath), shellQuoteArg(target.WordPressPath))
	}
	return fmt.Sprintf(`set -eu
tmp_sql=%s/database.sql
gzip -cd %s/database.sql.gz > "$tmp_sql"
%s --path=%s db import "$tmp_sql"
%srm -rf %s/wp-content/uploads %s/wp-content/plugins %s/wp-content/mu-plugins %s/wp-content/languages
%star -xzf %s/wp-content.tar.gz -C %s
%s`, shellQuoteArg(remoteTmp), shellQuoteArg(remoteTmp), target.WPCommand, shellQuoteArg(target.WordPressPath), fileOp, shellQuoteArg(target.WordPressPath), shellQuoteArg(target.WordPressPath), shellQuoteArg(target.WordPressPath), shellQuoteArg(target.WordPressPath), fileOp, shellQuoteArg(remoteTmp), shellQuoteArg(target.WordPressPath), chown)
}

func remoteSSHArgs(target envRemoteSyncTarget, script string) []string {
	return []string{"ssh", "-p", target.SSHPort, target.SSHUser + "@" + target.SSHHost, script}
}

func remoteSnapshotMetadataJSON(target envRemoteSyncTarget, envID, outputDir string, now time.Time) string {
	data, _ := json.MarshalIndent(map[string]any{
		"schema":     1,
		"source":     "remote",
		"env_id":     envID,
		"site_id":    target.SiteID,
		"env":        target.Env,
		"provider":   target.Provider,
		"target":     target.TargetRef,
		"url":        target.URL,
		"created_at": now.Format(time.RFC3339),
		"path":       outputDir,
		"contents": map[string]any{
			"database":         "database.sql.gz",
			"wp_content":       "wp-content.tar.gz",
			"wp_content_paths": envSnapshotContentPaths(),
		},
	}, "", "  ")
	return string(append(data, '\n'))
}

func cmdSiteSnapshot(envRef string, opts siteSnapshotOptions) int {
	if strings.TrimSpace(envRef) == "" {
		selected, err := chooseSiteEnv("snapshot", "")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		envRef = selected
	}
	siteID, env, ok := splitSiteEnvRef(envRef)
	if !ok {
		fmt.Fprintln(os.Stderr, "site snapshot requires an env ref like site.target:env")
		return 1
	}
	record, _, err := cachedSiteEnv(siteID, env)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if record == nil {
		fmt.Fprintf(os.Stderr, "No cached remote env matched %q.\n", canonicalEnvID(siteID, env))
		return 1
	}
	target, err := envRemoteSyncTargetFromSiteRecord(record, canonicalEnvID(siteID, env), siteID, env)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if target.Provider != "linode" && target.Provider != "kinsta" {
		fmt.Fprintf(os.Stderr, "site snapshot is not implemented for provider %q; no data was changed.\n", target.Provider)
		return 1
	}
	envID := canonicalEnvID(siteID, env)
	outputDir := strings.TrimSpace(opts.output)
	now := time.Now()
	if outputDir == "" {
		outputDir = remoteSnapshotDir(envID, now)
	}
	absOutputDir, err := filepath.Abs(outputDir)
	if err == nil {
		outputDir = absOutputDir
	}
	remoteTmp := path.Join("/tmp", "nf-snapshot-"+cleanEnvSlug(envIDFileSlug(envID))+"-"+strconv.FormatInt(now.Unix(), 10))
	fmt.Println("Site snapshot plan:")
	fmt.Printf("  env:           %s\n", envID)
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
	fmt.Printf("  output:        %s\n", outputDir)
	if opts.dryRun {
		fmt.Println("  mode:          dry-run")
		fmt.Println("No data was changed. Re-run without --dry-run to create a remote snapshot.")
		return 0
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := runSSHCommandFn(remoteSSHArgs(target, remoteExportScript(target, remoteTmp))); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := runRsyncCommandFn([]string{"rsync", "-az", "-e", "ssh -p " + target.SSHPort, target.SSHUser + "@" + target.SSHHost + ":" + remoteTmp + "/", outputDir + string(filepath.Separator)}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(outputDir, "snapshot.json"), []byte(remoteSnapshotMetadataJSON(target, envID, outputDir, now)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = runSSHCommandFn(remoteSSHArgs(target, "rm -rf "+shellQuoteArg(remoteTmp)))
	fmt.Printf("Site snapshot created.\n\nSnapshot:\n  source: remote\n  env: %s\n  path: %s\n  database: database.sql.gz\n  wp-content: wp-content.tar.gz\n", envID, outputDir)
	return 0
}

func executeEnvPull(cfg envConfig, target envRemoteSyncTarget) int {
	if err := ensureEnvReadyForSnapshot(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	safetyName := defaultPreRestoreSnapshotName(time.Now())
	if envSnapshotExists(cfg, safetyName) {
		fmt.Fprintf(os.Stderr, "env snapshot %q already exists.\n", safetyName)
		return 1
	}
	if err := envSnapshotCreateArchives(cfg, safetyName); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	meta, _ := envSnapshotMetadataJSON(newEnvSnapshotMetadata(cfg, safetyName, time.Now()))
	if err := os.WriteFile(envSnapshotMetadataPath(cfg, safetyName), []byte(meta), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	name := "pull-" + target.RemoteName + "-" + defaultEnvSnapshotName(time.Now())
	if err := os.MkdirAll(envSnapshotDir(cfg, name), 0o777); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	remoteTmp := remoteSyncTempDir(cfg, target, "pull")
	if err := runSSHCommandFn(remoteSSHArgs(target, remoteExportScript(target, remoteTmp))); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := runRsyncCommandFn([]string{"rsync", "-az", "-e", "ssh -p " + target.SSHPort, target.SSHUser + "@" + target.SSHHost + ":" + remoteTmp + "/", envSnapshotDir(cfg, name) + string(filepath.Separator)}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	meta, _ = envSnapshotMetadataJSON(newEnvSnapshotMetadata(cfg, name, time.Now()))
	if err := os.WriteFile(envSnapshotMetadataPath(cfg, name), []byte(meta), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := envSnapshotRestoreArchives(cfg, name); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Env pulled.\n\nRestored snapshot: %s\nSafety snapshot: %s\n", name, safetyName)
	return 0
}

func executeEnvPush(cfg envConfig, target envRemoteSyncTarget) int {
	if err := ensureEnvReadyForSnapshot(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	name := "push-" + target.RemoteName + "-" + defaultEnvSnapshotName(time.Now())
	if err := envSnapshotCreateArchives(cfg, name); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	meta, _ := envSnapshotMetadataJSON(newEnvSnapshotMetadata(cfg, name, time.Now()))
	if err := os.WriteFile(envSnapshotMetadataPath(cfg, name), []byte(meta), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	remoteTmp := remoteSyncTempDir(cfg, target, "push")
	if err := runSSHCommandFn(remoteSSHArgs(target, "rm -rf "+shellQuoteArg(remoteTmp)+" && mkdir -p "+shellQuoteArg(remoteTmp))); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := runRsyncCommandFn([]string{"rsync", "-az", "-e", "ssh -p " + target.SSHPort, envSnapshotDir(cfg, name) + string(filepath.Separator), target.SSHUser + "@" + target.SSHHost + ":" + remoteTmp + "/"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	backupTmp := remoteSyncTempDir(cfg, target, "backup")
	if err := runSSHCommandFn(remoteSSHArgs(target, remoteExportScript(target, backupTmp))); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := runSSHCommandFn(remoteSSHArgs(target, remoteImportScript(target, remoteTmp))); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Env pushed.\n\nLocal snapshot: %s\nRemote backup: %s\n", name, backupTmp)
	return 0
}

func cmdSiteRemoteCommandPlan(action, envRef string, args []string) int {
	siteID, env, ok := splitSiteEnvRef(envRef)
	if siteID == "" || env == "" {
		fmt.Fprintf(os.Stderr, "site %s requires an env ref like site.target:env\n", action)
		return 1
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "site %s requires an env ref like site.target:env\n", action)
		return 1
	}
	if action == "wp" {
		args = normalizePassthroughArgs(args)
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, "site wp requires an env ref and wp-cli command")
			return 1
		}
	}
	record, _, err := cachedSiteEnv(siteID, env)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if record == nil {
		fmt.Fprintf(os.Stderr, "No cached remote env matched %q.\n", canonicalEnvID(siteID, env))
		return 1
	}
	if err := validateSiteRecord(record); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(record["provider"])))
	fmt.Printf("Site %s preflight:\n", action)
	fmt.Printf("  site:     %s\n", siteID)
	fmt.Printf("  env:      %s\n", env)
	fmt.Printf("  provider: %s\n", provider)
	fmt.Printf("  target:   %s\n", siteProviderTarget(record))
	if url := firstRecordString(record, "url", "site_url", "home_url", "hostname"); url != "" {
		fmt.Printf("  url:      %s\n", url)
	}
	if provider != "linode" && provider != "kinsta" {
		if action == "wp" {
			fmt.Printf("  wp args:  %s\n", strings.Join(args, " "))
		}
		fmt.Fprintf(os.Stderr, "Remote site env %s is not implemented for provider %q; no command was run.\n", action, provider)
		return 1
	}
	var sshArgs []string
	if provider == "kinsta" {
		sshArgs, err = kinstaSiteEnvSSHArgs(record, action, args)
	} else {
		sshArgs, err = linodeSiteEnvSSHArgs(record, action, args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if action == "wp" {
		fmt.Printf("  wp args:  %s\n", strings.Join(args, " "))
	}
	printCommandArgs(sshArgs)
	if err := runSSHCommandFn(sshArgs); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func kinstaSiteEnvSSHArgs(record map[string]any, action string, wpArgs []string) ([]string, error) {
	host := firstNonEmpty(mapStringAtPath(record, "ssh", "host"), mapStringAtPath(record, "kinsta", "ssh", "host"), firstRecordString(record, "ssh_host"))
	if host == "" {
		return nil, ProjectError{Msg: fmt.Sprintf("Kinsta site env %q is missing SSH host. Run nf site refresh.", siteSummary(record))}
	}
	user := firstNonEmpty(mapStringAtPath(record, "ssh", "user"), mapStringAtPath(record, "kinsta", "ssh", "user"), firstRecordString(record, "ssh_user", "ssh_username"))
	if user == "" {
		return nil, ProjectError{Msg: fmt.Sprintf("Kinsta site env %q is missing SSH user. Run nf site refresh.", siteSummary(record))}
	}
	port := firstNonEmpty(mapStringAtPath(record, "ssh", "port"), mapStringAtPath(record, "kinsta", "ssh", "port"), firstRecordString(record, "ssh_port"), "22")
	path := firstRecordString(record, "path")
	if path == "" {
		return nil, ProjectError{Msg: fmt.Sprintf("Kinsta site env %q is missing path. Run nf site refresh.", siteSummary(record))}
	}
	path = normalizeKinstaCachedPath(path)
	destination := user + "@" + host
	if action != "wp" {
		remoteCommand := "cd " + shellQuoteArg(path) + " && exec ${SHELL:-/bin/bash} -i"
		return []string{"ssh", "-t", "-p", port, destination, remoteCommand}, nil
	}
	sshArgs := []string{"ssh", "-p", port, destination}
	remoteCommand := "cd " + shellQuoteArg(path) + " && wp --path=" + shellQuoteArg(path)
	if normalized := normalizePassthroughArgs(wpArgs); len(normalized) > 0 {
		remoteCommand += " " + renderCommandArgs(normalized)
	}
	return append(sshArgs, remoteCommand), nil
}

func normalizeKinstaCachedPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	const duplicatedPublicRoot = "/public/www/"
	if index := strings.Index(value, duplicatedPublicRoot); index >= 0 {
		return path.Clean("/www/" + value[index+len(duplicatedPublicRoot):])
	}
	return path.Clean(value)
}

func linodeSiteEnvSSHArgs(record map[string]any, action string, wpArgs []string) ([]string, error) {
	targetRef := siteProviderTarget(record)
	targets, err := cachedTargets()
	if err != nil {
		return nil, err
	}
	target := state.MatchingRecord(targets, targetRef)
	if target == nil {
		return nil, ProjectError{Msg: fmt.Sprintf("Linode site %q references target %q, but no cached target matched. Run nf provider check linode.", siteSummary(record), targetRef)}
	}
	values, err := loadGlobalConfig()
	if err != nil {
		return nil, err
	}
	user := firstNonEmpty(mapStringAtPath(record, "ssh", "user"), firstRecordString(record, "ssh_user", "ssh_username"), serverSSHUser(target), values["linode_default_user"])
	if user == "" {
		return nil, ProjectError{Msg: fmt.Sprintf("Target %q is missing an SSH user. Set linode_default_user with nf config set-linode-default-user <user>.", targetRef)}
	}
	host := firstNonEmpty(mapStringAtPath(record, "ssh", "host"), firstRecordString(record, "ssh_host"), serverSSHHost(target), firstRecordString(record, "hostname"))
	if host == "" {
		return nil, ProjectError{Msg: fmt.Sprintf("Site env %q is missing hostname.", siteSummary(record))}
	}
	port := firstNonEmpty(mapStringAtPath(record, "ssh", "port"), firstRecordString(record, "ssh_port"), mapStringAtPath(target, "ssh", "port"), firstRecordString(target, "ssh_port"), "22")
	destination := user + "@" + host
	path := firstRecordString(record, "path")
	if path == "" {
		return nil, ProjectError{Msg: fmt.Sprintf("Site env %q is missing path.", siteSummary(record))}
	}
	if action != "wp" {
		remoteCommand := "cd " + shellQuoteArg(path) + " && exec ${SHELL:-/bin/bash} -i"
		return []string{"ssh", "-t", "-p", port, destination, remoteCommand}, nil
	}
	sshArgs := []string{"ssh", "-p", port, destination}
	remoteCommand := "cd " + shellQuoteArg(path) + " && sudo -u www-data wp --path=" + shellQuoteArg(path)
	if normalized := normalizePassthroughArgs(wpArgs); len(normalized) > 0 {
		remoteCommand += " " + renderCommandArgs(normalized)
	}
	return append(sshArgs, remoteCommand), nil
}

func cmdShowServer(needle string) int {
	bundle, err := state.LoadStateBundle()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	record := state.MatchingRecord(bundle.Servers, needle)
	if record == nil {
		fmt.Fprintf(os.Stderr, "No server matched %q.\n", needle)
		return 1
	}
	if err := validateServerRecord(record); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	data, _ := json.MarshalIndent(record, "", "  ")
	fmt.Println(string(data))
	return 0
}

func cmdShowTarget(needle string, jsonOutput bool) int {
	targets, err := cachedTargets()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	record := state.MatchingRecord(targets, needle)
	if record == nil {
		fmt.Fprintf(os.Stderr, "No target matched %q.\n", needle)
		return 1
	}
	if err := validateTargetRecord(record); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !jsonOutput {
		printTargetDetails(record)
		return 0
	}
	data, _ := json.MarshalIndent(record, "", "  ")
	fmt.Println(string(data))
	return 0
}

func printTargetDetails(record map[string]any) {
	name := firstRecordString(record, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id")
	provider := recordValueString(record["provider"])
	fmt.Printf("Target: %s\n", name)
	fmt.Printf("Provider: %s\n", provider)
	if hostname := firstRecordString(record, "hostname", "host", "public_ipv4", "ipv4", "ip"); hostname != "" {
		fmt.Printf("Hostname: %s\n", hostname)
	}
	if id := firstRecordString(record, "id", "provider_id", "linode_id"); id != "" {
		fmt.Printf("ID: %s\n", id)
	}
	if status := targetLiveStatus(record); status != "" {
		fmt.Printf("Status: %s\n", status)
	}
	if cachedStatus := recordValueString(record["status"]); cachedStatus != "" {
		fmt.Printf("Cached status: %s\n", cachedStatus)
	}
	if region := firstRecordString(record, "region"); region != "" {
		fmt.Printf("Region: %s\n", region)
	}
	if targetType := firstRecordString(record, "type", "linode_type"); targetType != "" {
		fmt.Printf("Type: %s\n", targetType)
	}
	if image := firstRecordString(record, "image"); image != "" {
		fmt.Printf("Image: %s\n", image)
	}
	if sshHost := serverSSHHost(record); sshHost != "" {
		ssh := sshHost
		if sshUser := serverSSHUser(record); sshUser != "" {
			ssh = sshUser + "@" + ssh
		}
		if port := firstNonEmpty(mapStringAtPath(record, "ssh", "port"), firstRecordString(record, "ssh_port")); port != "" && port != "22" {
			ssh += ":" + port
		}
		fmt.Printf("SSH: %s\n", ssh)
	}
}

func cmdSiteRefresh() int {
	if err := os.MkdirAll(config.StateDir(), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	targets, err := cachedTargets()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Site refresh discovers sites from cached targets.")
	fmt.Printf("Sites cache: %s\n", state.StatePath("sites"))
	fmt.Printf("Targets cache: %s\n", state.StatePath("providers"))
	if len(targets) == 0 {
		fmt.Println("No cached targets found. Run nf provider check <provider> to refresh target metadata.")
		return 0
	}
	fmt.Printf("Targets: %d\n", len(targets))
	for _, target := range targets {
		fmt.Printf("  %s (%s)\n", firstRecordString(target, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id"), recordValueString(target["provider"]))
	}
	result, err := refreshRemoteTargetSites(targets)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if result.Skipped > 0 {
		fmt.Printf("Skipped targets: %d\n", result.Skipped)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
	}
	if result.Refreshed == 0 {
		if result.Pruned == 0 {
			fmt.Println("No remote targets were refreshed; no site cache was changed.")
		}
		if len(result.Warnings) > 0 {
			return 1
		}
		if result.Pruned == 0 {
			return 0
		}
	} else {
		fmt.Printf("Refreshed targets: %d\n", result.Refreshed)
		fmt.Printf("Discovered remote site envs: %d\n", result.Discovered)
	}
	if result.Pruned > 0 {
		fmt.Printf("Pruned stale site envs: %d\n", result.Pruned)
	}
	fmt.Printf("Saved site cache: %s\n", state.StatePath("sites"))
	if len(result.Warnings) > 0 {
		return 1
	}
	return 0
}

type siteRefreshResult struct {
	Refreshed  int
	Skipped    int
	Discovered int
	Pruned     int
	Warnings   []string
}

func refreshRemoteTargetSites(targets []map[string]any) (siteRefreshResult, error) {
	result := siteRefreshResult{}
	currentTargets := currentTargetNames(targets)
	refreshedTargets := map[string]bool{}
	discovered := []map[string]any{}
	for _, target := range targets {
		provider := strings.ToLower(strings.TrimSpace(recordValueString(target["provider"])))
		if provider != "linode" && provider != "kinsta" {
			result.Skipped++
			continue
		}
		targetName := firstRecordString(target, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id")
		if provider == "kinsta" {
			remote, err := discoverKinstaTargetSites(target)
			if err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("target %q: %v", targetName, err))
				continue
			}
			result.Refreshed++
			refreshedTargets[normalizedRecordString(targetName)] = true
			discovered = append(discovered, remote...)
			continue
		}
		sshHost := serverSSHHost(target)
		if sshHost == "" {
			result.Warnings = append(result.Warnings, fmt.Sprintf("target %q has no SSH host", targetName))
			continue
		}
		remote, err := discoverLinodeTargetSites(target)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("target %q: %v", targetName, err))
			continue
		}
		result.Refreshed++
		refreshedTargets[normalizedRecordString(targetName)] = true
		for _, record := range remote {
			normalizeRemoteSiteRecord(record, target)
			discovered = append(discovered, record)
		}
	}
	existing, err := state.LoadStateRecords("sites")
	if err != nil {
		return result, err
	}
	merged := make([]map[string]any, 0, len(existing)+len(discovered))
	for _, record := range existing {
		if staleSiteTarget(record, currentTargets) {
			result.Pruned++
			continue
		}
		if refreshedTargets[normalizedRecordString(siteProviderTarget(record))] {
			continue
		}
		if refreshedTargets[normalizedRecordString(siteServerReference(record))] {
			continue
		}
		merged = append(merged, record)
	}
	if result.Refreshed == 0 && result.Pruned == 0 {
		return result, nil
	}
	merged = append(merged, discovered...)
	if err := state.SaveStateRecords("sites", merged); err != nil {
		return result, err
	}
	result.Discovered = len(discovered)
	return result, nil
}

func currentTargetNames(targets []map[string]any) map[string]bool {
	names := map[string]bool{}
	for _, target := range targets {
		for _, key := range []string{"_state_key", "target_name", "target", "name", "slug", "hostname", "host", "label", "id"} {
			if value := normalizedRecordString(recordValueString(target[key])); value != "" {
				names[value] = true
			}
		}
	}
	return names
}

func staleSiteTarget(site map[string]any, currentTargets map[string]bool) bool {
	provider := strings.ToLower(strings.TrimSpace(recordValueString(site["provider"])))
	if provider != "linode" {
		return false
	}
	target := normalizedRecordString(siteProviderTarget(site))
	if target == "" {
		return false
	}
	return !currentTargets[target]
}

func discoverKinstaTargetSites(target map[string]any) ([]map[string]any, error) {
	token := envwizard.Value("KINSTA_API_KEY")
	if token == "" {
		return nil, fmt.Errorf("Expected KINSTA_API_KEY in the environment or %s.", config.EnvFile())
	}
	targetName := firstNonEmpty(firstRecordString(target, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id"), "kinsta")
	companyID := firstNonEmpty(firstRecordString(target, "company_id", "company"), mapStringAtPath(target, "kinsta", "company_id"))
	client := kinsta.NewClient(os.Getenv("KINSTA_BASE_URL"), token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if companyID == "" {
		validate, err := client.Validate(ctx)
		if err != nil {
			return nil, err
		}
		companyID = strings.TrimSpace(validate.Company)
	}
	sites, err := client.ListSites(ctx, companyID)
	if err != nil {
		return nil, err
	}
	records := []map[string]any{}
	for _, site := range sites {
		siteName := firstNonEmpty(site.Name, site.DisplayName, site.ID)
		if siteName == "" || site.ID == "" {
			continue
		}
		siteID := kinstaSiteID(siteName)
		envs, err := client.ListEnvironments(ctx, site.ID)
		if err != nil {
			return nil, fmt.Errorf("site %s environments: %w", siteName, err)
		}
		for i, env := range envs {
			envName := kinstaCacheEnvName(env, i)
			phpVersion := env.CurrentPHPVersion()
			domain := kinstaEnvPrimaryDomain(env)
			if domain.ID == "" || domainName(domain) == "" {
				domains, err := client.ListDomains(ctx, env.ID)
				if err == nil {
					domain = preferredKinstaDomain(domains)
				}
			}
			cfg, _ := client.SFTPConfig(ctx, site.ID, env.ID)
			pathValue := kinstaEnvPath(firstNonEmpty(cfg.User, siteName), env.WebRoot)
			database := firstNonEmpty(cfg.User, siteName)
			host := firstNonEmpty(cfg.Host, env.SSHConnection.SSHIP.ExternalIP)
			port := firstNonEmpty(cfg.Port, env.SSHConnection.SSHPort, "22")
			user := cfg.User
			domainValue := domainName(domain)
			records = append(records, map[string]any{
				"provider":    "kinsta",
				"env_id":      canonicalEnvID(siteID, envName),
				"site_id":     siteID,
				"name":        siteName,
				"env":         envName,
				"target":      targetName,
				"hostname":    domainValue,
				"url":         kinstaURL(domainValue),
				"path":        pathValue,
				"database":    database,
				"php_version": phpVersion,
				"status":      "active",
				"ssh":         sshRecord(user, host, port, cfg.SSHCommand),
				"kinsta": map[string]any{
					"site_id":        site.ID,
					"environment_id": env.ID,
					"domain_id":      domain.ID,
					"branch":         kinstaEnvBranch(envName),
				},
			})
		}
	}
	return records, nil
}

func kinstaCacheEnvName(env kinsta.Environment, index int) string {
	value := strings.ToLower(strings.TrimSpace(firstNonEmpty(env.Name, env.DisplayName)))
	if strings.Contains(value, "stag") {
		return "staging"
	}
	if value == "live" || index == 0 {
		return "live"
	}
	return value
}

func kinstaEnvPrimaryDomain(env kinsta.Environment) kinsta.Domain {
	if env.PrimaryDomain.ID != "" || domainName(env.PrimaryDomain) != "" {
		return env.PrimaryDomain
	}
	return preferredKinstaDomain(env.Domains)
}

func preferredKinstaDomain(domains []kinsta.Domain) kinsta.Domain {
	for _, domain := range domains {
		if domain.IsPrimary || strings.EqualFold(strings.TrimSpace(domain.Type), "live") {
			return domain
		}
	}
	if len(domains) > 0 {
		return domains[0]
	}
	return kinsta.Domain{}
}

func domainName(domain kinsta.Domain) string {
	return firstNonEmpty(domain.Name, domain.Domain, domain.DomainName)
}

func kinstaURL(domain string) string {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return ""
	}
	return "https://" + domain
}

func kinstaEnvBranch(env string) string {
	if env == "staging" {
		return "develop"
	}
	return "main"
}

func discoverLinodeTargetSites(target map[string]any) ([]map[string]any, error) {
	remoteTarget := map[string]any{}
	if data, err := readLinodeTargetFile(target); err == nil {
		remoteTarget = data
	}
	mergedTarget := cloneRecord(target)
	for key, value := range remoteTarget {
		if recordValueString(mergedTarget[key]) == "" {
			mergedTarget[key] = value
		}
	}
	sshHost := serverSSHHost(target)
	sshUser := serverSSHUser(target)
	args := []string{"ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null"}
	if port := firstNonEmpty(mapStringAtPath(target, "ssh", "port"), firstRecordString(target, "ssh_port")); port != "" {
		args = append(args, "-p", port)
	}
	destination := sshHost
	if sshUser != "" {
		destination = sshUser + "@" + sshHost
	}
	args = append(args, destination, "cat", "/var/lib/nf/sites.json")
	data, err := runSSHOutputFn(args)
	if err != nil {
		return nil, err
	}
	records, err := parseRemoteSiteRecords(data)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		normalizeRemoteSiteRecord(record, mergedTarget)
	}
	return records, nil
}

func readLinodeTargetFile(target map[string]any) (map[string]any, error) {
	sshHost := serverSSHHost(target)
	sshUser := serverSSHUser(target)
	args := []string{"ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null"}
	if port := firstNonEmpty(mapStringAtPath(target, "ssh", "port"), firstRecordString(target, "ssh_port")); port != "" {
		args = append(args, "-p", port)
	}
	destination := sshHost
	if sshUser != "" {
		destination = sshUser + "@" + sshHost
	}
	args = append(args, destination, "cat", "/var/lib/nf/target.json")
	data, err := runSSHOutputFn(args)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	record := map[string]any{}
	if err := dec.Decode(&record); err != nil {
		return nil, fmt.Errorf("parse /var/lib/nf/target.json: %w", err)
	}
	return record, nil
}

func parseRemoteSiteRecords(data []byte) ([]map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	var payload any
	if err := dec.Decode(&payload); err != nil {
		return nil, fmt.Errorf("parse /var/lib/nf/sites.json: %w", err)
	}
	switch typed := payload.(type) {
	case []any:
		records := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			record, ok := item.(map[string]any)
			if !ok {
				return nil, ProjectError{Msg: "/var/lib/nf/sites.json must contain site objects"}
			}
			records = append(records, record)
		}
		return records, nil
	case map[string]any:
		if list, ok := typed["sites"].([]any); ok {
			records := make([]map[string]any, 0, len(list))
			for _, item := range list {
				record, ok := item.(map[string]any)
				if !ok {
					return nil, ProjectError{Msg: "/var/lib/nf/sites.json sites must contain site objects"}
				}
				records = append(records, record)
			}
			return records, nil
		}
	}
	return nil, ProjectError{Msg: "Unsupported JSON shape in /var/lib/nf/sites.json"}
}

func normalizeRemoteSiteRecord(record, target map[string]any) {
	targetName := firstRecordString(target, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id")
	if targetName != "" {
		if firstRecordString(record, "target", "server", "server_name", "server_id", "server_hostname", "server_label") == "" {
			record["target"] = targetName
		}
	}
	if recordValueString(record["provider"]) == "" {
		record["provider"] = "linode"
	}
	if firstRecordString(record, "site_id") == "" {
		if siteID := siteCanonicalID(siteRecordName(record), siteProviderTarget(record)); siteID != "" {
			record["site_id"] = siteID
		}
	}
	if envID := canonicalEnvID(siteRecordID(record), siteEnvName(record)); envID != "" {
		record["env_id"] = envID
	}
	if sitePHPVersion(record) == "" {
		if phpVersion := targetPHPVersion(target); phpVersion != "" {
			record["php_version"] = phpVersion
		}
	}
	if mapStringAtPath(record, "ssh", "host") == "" {
		sshHost := serverSSHHost(target)
		sshUser := serverSSHUser(target)
		sshPort := firstNonEmpty(mapStringAtPath(target, "ssh", "port"), firstRecordString(target, "ssh_port"), "22")
		if sshHost != "" || sshUser != "" || sshPort != "" {
			record["ssh"] = sshRecord(sshUser, sshHost, sshPort, sshCommand(sshUser, sshHost, sshPort))
		}
	}
	if strings.EqualFold(recordValueString(record["provider"]), "linode") {
		if linode := mapMapAtPath(record, "linode"); linode != nil {
			delete(linode, "target_hostname")
			if len(linode) == 0 {
				delete(record, "linode")
			}
		}
		delete(record, "server")
		delete(record, "server_name")
		delete(record, "server_hostname")
		delete(record, "environment")
	}
}

func cmdServerRootPassword(needle string) int {
	servers, err := state.LoadStateRecords("servers")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	record := state.MatchingRecord(servers, needle)
	if record == nil {
		fmt.Fprintf(os.Stderr, "No server matched %q.\n", needle)
		return 1
	}
	hostname := firstRecordString(record, "hostname")
	if hostname == "" {
		fmt.Fprintf(os.Stderr, "Server %q is missing hostname.\n", needle)
		return 1
	}
	salt, err := passwords.SecretSalt()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	password := passwords.DerivePassword(hostname, "linode-root", salt)
	fmt.Printf("Root password for %s:\n\n%s\n", hostname, password)
	return 0
}

func cmdShowSite(needle string, jsonOutput bool) int {
	resolved, _, projectFileExists, targetAliasUsed, err := resolveSiteTarget(needle)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if siteID, env, ok := splitSiteEnvRef(resolved); ok {
		return cmdShowSiteEnv(siteID, env, jsonOutput)
	}
	bundle, err := state.LoadStateBundle()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	records := siteRecordsByID(bundle.Sites, resolved)
	if len(records) == 0 {
		if targetAliasUsed {
			fmt.Fprintf(os.Stderr, "deploy.targets.%s resolves to %q, but no site target matched that name.\n", needle, resolved)
			return 1
		}
		if projectFileExists {
			fmt.Fprintf(os.Stderr, "No site matched %q. Add deploy.targets.%s in .nf/project.json or create a site target with that name.\n", needle, needle)
			return 1
		}
		fmt.Fprintf(os.Stderr, "No site matched %q.\n", needle)
		return 1
	}
	out := siteDetailsOutput(needle, resolved, records, bundle.Servers)
	if !jsonOutput {
		printSiteDetails(out)
		return 0
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(data))
	return 0
}

func cmdSitePassword(needle string) int {
	resolved, _, projectFileExists, targetAliasUsed, err := resolveSiteTarget(needle)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if siteID, _, ok := splitSiteEnvRef(resolved); ok {
		resolved = siteID
	}
	bundle, err := state.LoadStateBundle()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	records := siteRecordsByID(bundle.Sites, resolved)
	if len(records) == 0 {
		if targetAliasUsed {
			fmt.Fprintf(os.Stderr, "deploy.targets.%s resolves to %q, but no site target matched that name.\n", needle, resolved)
			return 1
		}
		if projectFileExists {
			fmt.Fprintf(os.Stderr, "No site matched %q. Add deploy.targets.%s in .nf/project.json or create a site target with that name.\n", needle, needle)
			return 1
		}
		fmt.Fprintf(os.Stderr, "No site matched %q.\n", needle)
		return 1
	}
	password, err := siteAdminPassword(preferredPasswordSiteRecord(records))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if password == "" {
		fmt.Fprintf(os.Stderr, "Site %q has no derivable admin password.\n", resolved)
		return 1
	}
	fmt.Println(password)
	return 0
}

func cmdEnvPassword(cfg envConfig) int {
	password, err := envAdminPassword(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(password)
	return 0
}

func preferredPasswordSiteRecord(records []map[string]any) map[string]any {
	for _, record := range records {
		if normalizedRecordString(siteEnvName(record)) == "live" {
			return record
		}
	}
	return records[0]
}

func printSiteDetails(out map[string]any) {
	siteID := recordValueString(out["site_id"])
	if siteID != "" {
		fmt.Println(siteID)
		fmt.Println(strings.Repeat("─", len(siteID)))
	}
	printDetailRows([]detailRow{
		{label: "Site", value: siteID},
		{label: "Name", value: recordValueString(out["name"])},
		{label: "Provider", value: recordValueString(out["provider"])},
		{label: "Target", value: recordValueString(out["target"])},
	})
	requested := recordValueString(out["requested_site"])
	resolved := recordValueString(out["resolved_site"])
	if requested != "" && resolved != "" && requested != resolved {
		printDetailRows([]detailRow{
			{label: "Requested", value: requested},
			{label: "Resolved", value: resolved},
		})
	}
	envs, _ := out["envs"].([]map[string]any)
	if len(envs) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("Environments:")
	rows := [][]string{{"env", "php", "url"}}
	for _, env := range envs {
		rows = append(rows, []string{
			siteEnvName(env),
			sitePHPVersion(env),
			firstRecordString(env, "url", "site_url", "home_url", "hostname"),
		})
	}
	fmt.Println(formatTable(rows))
}

func siteEnvSSHDisplay(record map[string]any) string {
	host := firstNonEmpty(mapStringAtPath(record, "ssh", "host"), mapStringAtPath(record, "kinsta", "ssh", "host"), firstRecordString(record, "ssh_host"))
	if host == "" {
		return ""
	}
	value := host
	if user := firstNonEmpty(mapStringAtPath(record, "ssh", "user"), mapStringAtPath(record, "kinsta", "ssh", "user"), firstRecordString(record, "ssh_user", "ssh_username")); user != "" {
		value = user + "@" + value
	}
	if port := firstNonEmpty(mapStringAtPath(record, "ssh", "port"), mapStringAtPath(record, "kinsta", "ssh", "port"), firstRecordString(record, "ssh_port")); port != "" && port != "22" {
		value += ":" + port
	}
	return value
}

func siteRecordsByID(records []map[string]any, siteID string) []map[string]any {
	needle := normalizedRecordString(siteID)
	if needle == "" {
		return nil
	}
	matches := []map[string]any{}
	for _, record := range records {
		if normalizedRecordString(siteRecordID(record)) == needle {
			matches = append(matches, record)
		}
	}
	return matches
}

func siteRecordsMatchingSite(records []map[string]any, needle string) ([]map[string]any, string, error) {
	normalized := normalizedRecordString(needle)
	if normalized == "" {
		return nil, "", nil
	}
	matchedIDs := []string{}
	seen := map[string]bool{}
	for _, record := range records {
		id := siteRecordID(record)
		if id == "" {
			continue
		}
		candidates := []string{id, siteEnvSiteID(record), siteRecordName(record), siteTargetName(record), firstRecordString(record, "hostname", "url", "site_url", "home_url")}
		for _, candidate := range candidates {
			if normalizedRecordString(candidate) != normalized {
				continue
			}
			if !seen[id] {
				seen[id] = true
				matchedIDs = append(matchedIDs, id)
			}
			break
		}
	}
	if len(matchedIDs) == 0 {
		return nil, "", nil
	}
	if len(matchedIDs) > 1 {
		sort.Strings(matchedIDs)
		return nil, "", ProjectError{Msg: fmt.Sprintf("Site %q matched multiple sites: %s.", needle, strings.Join(matchedIDs, ", "))}
	}
	return siteRecordsByID(records, matchedIDs[0]), matchedIDs[0], nil
}

func siteDetailsOutput(requested, resolved string, records []map[string]any, servers []map[string]any) map[string]any {
	first := records[0]
	out := map[string]any{
		"requested_site":   requested,
		"resolved_site":    resolved,
		"requested_target": requested,
		"resolved_target":  resolved,
		"site_id":          siteRecordID(first),
		"name":             siteRecordName(first),
		"target":           siteProviderTarget(first),
		"provider":         recordValueString(first["provider"]),
	}
	envs := make([]map[string]any, 0, len(records))
	for _, record := range records {
		env := cloneRecord(record)
		env["resolved_site"] = siteEnvDisplaySite(record)
		env["resolved_env"] = siteEnvName(record)
		env["resolved_target"] = siteProviderTarget(record)
		if err := validateSiteRecord(record); err == nil {
			_ = enrichSiteOutput(env, record, servers)
		}
		envs = append(envs, env)
	}
	sort.SliceStable(envs, func(i, j int) bool {
		left := siteEnvName(envs[i])
		right := siteEnvName(envs[j])
		li, ri := siteListEnvOrder(left), siteListEnvOrder(right)
		if li != ri {
			return li < ri
		}
		return left < right
	})
	out["envs"] = envs
	return out
}

func chooseSiteForShow() (string, error) {
	return chooseSite("show")
}

func chooseSiteForRemove() (string, error) {
	return chooseSite("remove")
}

func chooseSiteForPassword() (string, error) {
	return chooseSite("show password for")
}

func chooseSite(action string) (string, error) {
	bundle, err := state.LoadStateBundle()
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	options := []ui.SelectOption{}
	for _, record := range bundle.Sites {
		siteID := siteRecordID(record)
		if siteID == "" || seen[siteID] {
			continue
		}
		seen[siteID] = true
		options = append(options, ui.SelectOption{Value: siteID, Label: siteID})
	}
	if len(options) == 0 {
		return "", ProjectError{Msg: "No selectable sites found."}
	}
	return siteSelectFn(fmt.Sprintf("Choose a site to %s", action), options)
}

func chooseSiteEnv(action, siteID string) (string, error) {
	return selectSiteEnv(fmt.Sprintf("Choose a remote env to %s", action), siteID)
}

func selectSiteEnv(title, siteID string) (string, error) {
	bundle, err := state.LoadStateBundle()
	if err != nil {
		return "", err
	}
	options := []ui.SelectOption{}
	seen := map[string]bool{}
	for _, record := range bundle.Sites {
		if !siteEnvMatchesSite(record, siteID) {
			continue
		}
		envID := siteRecordEnvID(record)
		if envID == "" || seen[envID] {
			continue
		}
		seen[envID] = true
		options = append(options, ui.SelectOption{Value: envID, Label: envID})
	}
	if len(options) == 0 {
		if strings.TrimSpace(siteID) != "" {
			return "", ProjectError{Msg: fmt.Sprintf("No selectable envs found for %q.", siteID)}
		}
		return "", ProjectError{Msg: "No selectable envs found."}
	}
	sort.SliceStable(options, func(i, j int) bool {
		leftSite, leftEnv, _ := splitSiteEnvRef(options[i].Value)
		rightSite, rightEnv, _ := splitSiteEnvRef(options[j].Value)
		if leftSite != rightSite {
			return leftSite < rightSite
		}
		li, ri := siteListEnvOrder(leftEnv), siteListEnvOrder(rightEnv)
		if li != ri {
			return li < ri
		}
		return leftEnv < rightEnv
	})
	return siteSelectFn(title, options)
}

func parseSiteShowArgs(argv []string) (string, bool, error) {
	needle := ""
	jsonOutput := false
	for _, arg := range argv {
		switch arg {
		case "--json":
			jsonOutput = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, fmt.Errorf("unknown site show flag: %s", arg)
			}
			if needle != "" {
				return "", false, fmt.Errorf("site show takes at most one site")
			}
			needle = arg
		}
	}
	return needle, jsonOutput, nil
}

func parseSiteListArgs(argv []string) (bool, string, error) {
	envs := false
	siteID := ""
	for _, arg := range argv {
		switch arg {
		case "--envs":
			envs = true
		default:
			if strings.HasPrefix(arg, "-") {
				return false, "", fmt.Errorf("unknown site list flag: %s", arg)
			}
			if siteID != "" {
				return false, "", fmt.Errorf("site list --envs takes at most one site")
			}
			siteID = arg
		}
	}
	if siteID != "" && !envs {
		return false, "", fmt.Errorf("site list takes no arguments unless --envs is used")
	}
	return envs, siteID, nil
}

func resolveSiteCommandEnvRef(action, ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		if !siteIsInteractiveFn() {
			fmt.Fprintf(os.Stderr, "site %s requires an env ref like site.target:env\n", action)
			return "", false
		}
		selected, err := chooseSiteEnv(action, "")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return "", false
		}
		return selected, true
	}
	if _, _, ok := splitSiteEnvRef(ref); ok {
		return ref, true
	}
	if !siteIsInteractiveFn() {
		fmt.Fprintf(os.Stderr, "site %s requires an env ref like %s:live or %s:staging\n", action, ref, ref)
		return "", false
	}
	selected, err := chooseSiteEnv(action, ref)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return "", false
	}
	return selected, true
}

func parseSiteShellArgs(argv []string) (string, bool) {
	if len(argv) > 1 {
		fmt.Fprintln(os.Stderr, "site shell takes at most one env ref")
		return "", false
	}
	ref := ""
	if len(argv) == 1 {
		ref = argv[0]
		if strings.HasPrefix(ref, "-") {
			fmt.Fprintf(os.Stderr, "unknown site shell flag: %s\n", ref)
			return "", false
		}
	}
	return resolveSiteCommandEnvRef("shell", ref)
}

func parseSiteWPArgs(argv []string) (string, []string, bool) {
	if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
		fmt.Fprintln(os.Stderr, "site wp requires an env ref and wp-cli command")
		return "", nil, false
	}
	envRef, ok := resolveSiteCommandEnvRef("wp", argv[0])
	if !ok {
		return "", nil, false
	}
	command := normalizePassthroughArgs(argv[1:])
	if len(command) == 0 {
		fmt.Fprintln(os.Stderr, "site wp requires an env ref and wp-cli command")
		return "", nil, false
	}
	return envRef, command, true
}

func chooseTargetForShow() (string, error) {
	return chooseTarget("show")
}

func chooseTargetForRemove() (string, error) {
	return chooseTarget("remove")
}

func chooseTarget(action string) (string, error) {
	targets, err := cachedTargets()
	if err != nil {
		return "", err
	}
	if len(targets) == 0 {
		return "", ProjectError{Msg: "No targets found."}
	}
	options := make([]ui.SelectOption, 0, len(targets))
	for _, target := range targets {
		value := firstRecordString(target, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id")
		if value == "" {
			continue
		}
		options = append(options, ui.SelectOption{Label: value, Value: value})
	}
	if len(options) == 0 {
		return "", ProjectError{Msg: "No selectable targets found."}
	}
	return targetSelectFn(fmt.Sprintf("Choose a target to %s", action), options)
}

func parseTargetShowArgs(argv []string) (string, bool, error) {
	needle := ""
	jsonOutput := false
	for _, arg := range argv {
		switch arg {
		case "--json":
			jsonOutput = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, fmt.Errorf("unknown target show flag: %s", arg)
			}
			if needle != "" {
				return "", false, fmt.Errorf("target show takes at most one target")
			}
			needle = arg
		}
	}
	return needle, jsonOutput, nil
}

func cmdProjectInit(args projectInitArgs) int {
	root := projectInitRoot()
	if err := writeProjectInit(root, args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func projectInitRoot() string {
	if root, ok := currentGitRoot(); ok {
		return root
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func writeProjectInit(root string, args projectInitArgs) error {
	metadata := projectInitMetadata(args)
	projectPath := config.ProjectFile(root)
	if !args.force {
		if _, err := os.Stat(projectPath); err == nil {
			return ProjectError{Msg: fmt.Sprintf("%s already exists; use --force to overwrite.", projectPath)}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(projectPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(projectPath, []byte(projectInitJSON(metadata)), 0o644); err != nil {
		return err
	}
	fmt.Printf("Wrote %s\n", projectPath)
	return nil
}

func ensureEnvProjectMetadata() error {
	root, ok := currentGitRoot()
	if !ok {
		return ProjectError{Msg: "env up requires a .git repository above the current directory"}
	}
	projectPath := config.ProjectFile(root)
	if _, err := os.Stat(projectPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	slug, err := currentGitRootBase()
	if err != nil {
		return err
	}
	return writeProjectInit(root, projectInitArgs{projectSlug: slug, projectType: "wordpress-theme"})
}

func projectInitMetadata(args projectInitArgs) map[string]any {
	themePath := firstNonEmpty(args.themeSource, "theme")
	themePathArg := shellQuoteArg(themePath)
	themeSlug := firstNonEmpty(args.themeSlug, "theme")
	projectName := firstNonEmpty(args.projectName, slugToTitle(args.projectSlug))
	projectSlug := args.projectSlug
	metadata := map[string]any{
		"schema": 1,
		"project": map[string]any{
			"slug": projectSlug,
			"name": projectName,
			"type": firstNonEmpty(args.projectType, "wordpress-theme"),
		},
		"wordpress": map[string]any{
			"deploy_unit": "theme",
			"theme_slug":  themeSlug,
			"theme_path":  themePath,
		},
		"env": map[string]any{
			"compose":           "docker compose",
			"wordpress_service": "wordpress",
			"cli_service":       "cli",
			"theme_mount_slug":  "theme",
			"uploads_path":      "uploads",
		},
		"build": map[string]any{
			"steps": []any{"composer --working-dir=" + themePathArg + " install", "npm --prefix " + themePathArg + " run build"},
		},
		"artifact": map[string]any{
			"path":    filepath.ToSlash(filepath.Join("dist", projectSlug+"-v{version}.zip")),
			"include": []any{"vendor/", "assets/dist/"},
			"exclude": []any{"node_modules/", ".git/"},
		},
		"deploy": map[string]any{
			"targets": map[string]any{},
		},
		"tasks": defaultProjectTasks(),
	}
	return metadata
}

type projectInitArgs struct {
	projectSlug string
	projectName string
	themeSlug   string
	themeSource string
	projectType string
	force       bool
}

func projectInitJSON(metadata map[string]any) string {
	data, _ := json.MarshalIndent(metadata, "", "  ")
	return string(append(data, '\n'))
}

func renderEnvCompose(cfg envConfig) string {
	themeMountSlug := firstNonEmpty(cfg.ThemeMountSlug, "theme")
	wordpressService := firstNonEmpty(cfg.WordpressService, "wordpress")
	cliService := firstNonEmpty(cfg.CliService, "cli")
	themePath := cfg.ThemePath
	uploadsPath := firstNonEmpty(cfg.UploadsPath, "uploads")
	return fmt.Sprintf(`services:
  db:
    image: mariadb:11
    command: --character-set-server=utf8mb4 --collation-server=utf8mb4_unicode_ci
    environment:
      MARIADB_DATABASE: ${DB_NAME}
      MARIADB_USER: ${DB_USER}
      MARIADB_PASSWORD: ${DB_PASSWORD}
      MARIADB_ROOT_PASSWORD: ${DB_ROOT_PASSWORD}
    healthcheck:
      test: ["CMD", "healthcheck.sh", "--connect", "--innodb_initialized"]
      interval: 10s
      timeout: 5s
      retries: 10
    volumes:
      - db_data:/var/lib/mysql

  %s:
    build:
      context: .
      dockerfile: wordpress/Dockerfile
    depends_on:
      db:
        condition: service_healthy
    ports:
      - "${WP_PORT}:80"
    environment:
      WORDPRESS_DB_HOST: db:3306
      WORDPRESS_DB_NAME: ${DB_NAME}
      WORDPRESS_DB_USER: ${DB_USER}
      WORDPRESS_DB_PASSWORD: ${DB_PASSWORD}
      WP_URL: ${WP_URL}
      WP_TITLE: ${WP_TITLE}
      ADMIN_USER: ${ADMIN_USER}
      ADMIN_PASSWORD: ${ADMIN_PASSWORD}
      ADMIN_EMAIL: ${ADMIN_EMAIL}
      WORDPRESS_CONFIG_EXTRA: |
        define('WP_HOME', getenv('WP_URL'));
        define('WP_SITEURL', getenv('WP_URL'));
        define('FS_METHOD', 'direct');
        if ( ! defined('WP_DEBUG') ) define('WP_DEBUG', true);
        if ( ! defined('WP_DEBUG_LOG') ) define('WP_DEBUG_LOG', true);
        if ( ! defined('WP_DEBUG_DISPLAY') ) define('WP_DEBUG_DISPLAY', false);
    volumes:
      - wp_data:/var/www/html
      - %s:/var/www/html/wp-content/themes/%s
      - ./php/uploads.ini:/usr/local/etc/php/conf.d/uploads.ini:ro

  %s:
    image: wordpress:cli-php8.4
    depends_on:
      %s:
        condition: service_started
    working_dir: /var/www/html
    user: "33:33"
    environment:
      WORDPRESS_DB_HOST: db:3306
      WORDPRESS_DB_NAME: ${DB_NAME}
      WORDPRESS_DB_USER: ${DB_USER}
      WORDPRESS_DB_PASSWORD: ${DB_PASSWORD}
      WP_URL: ${WP_URL}
      WP_TITLE: ${WP_TITLE}
      ADMIN_USER: ${ADMIN_USER}
      ADMIN_PASSWORD: ${ADMIN_PASSWORD}
      ADMIN_EMAIL: ${ADMIN_EMAIL}
    volumes:
      - wp_data:/var/www/html
      - %s:/var/www/html/wp-content/themes/%s
      - ./%s:%s
      - %s:/env-snapshots

  mailpit:
    image: axllent/mailpit
    ports:
      - "${MAILPIT_PORT}:8025"

volumes:
  db_data:
  wp_data:
`, wordpressService, themePath, themeMountSlug, cliService, wordpressService, themePath, themeMountSlug, uploadsPath, path.Join("/", "env", uploadsPath), envSnapshotComposeMount(cfg))
}

func renderEnvFile(cfg envConfig) string {
	wpTitle := firstNonEmpty(cfg.ProjectName, slugToTitle(cfg.ProjectSlug))
	adminUser := firstNonEmpty(cfg.AdminUser, "admin")
	adminPassword := firstNonEmpty(cfg.AdminPassword, "admin")
	adminEmail := firstNonEmpty(cfg.AdminEmail, "web@nonfiction.ca")
	return fmt.Sprintf(`COMPOSE_PROJECT_NAME=%s
WP_PORT=%d
MAILPIT_PORT=%d
DB_NAME=%s
DB_USER=%s
DB_PASSWORD=wordpress
DB_ROOT_PASSWORD=root
WP_URL=http://localhost:%d
WP_TITLE=%s
ADMIN_USER=%s
ADMIN_PASSWORD=%s
ADMIN_EMAIL=%s
`, envComposeProjectName(cfg.ProjectSlug), cfg.WordpressPort, cfg.MailpitPort, cfg.ProjectSlug, cfg.ProjectSlug, cfg.WordpressPort, wpTitle, adminUser, adminPassword, adminEmail)
}

func envComposeProjectName(projectSlug string) string {
	return "nf_" + cleanEnvSlug(projectSlug) + "_env"
}

func renderEnvInfo(cfg envConfig, includeURLs bool) string {
	title := cfg.ProjectSlug + ":local"
	lines := []string{title, strings.Repeat("─", len(title))}
	rows := []detailRow{
		{label: "Site", value: cfg.ProjectSlug},
		{label: "Env", value: "local"},
	}
	if includeURLs {
		rows = append(rows, detailRow{label: "URL", value: fmt.Sprintf("http://localhost:%d", cfg.WordpressPort)})
	}
	rows = append(rows,
		detailRow{label: "Path", value: localEnvDir(cfg)},
		detailRow{label: "PHP", value: localEnvPHPVersion()},
		detailRow{label: "Database", value: cfg.ProjectSlug},
		detailRow{label: "Compose", value: envComposeProjectName(cfg.ProjectSlug)},
	)
	if includeURLs {
		rows = append(rows, detailRow{label: "Mailpit", value: fmt.Sprintf("http://localhost:%d", cfg.MailpitPort)})
	}
	lines = append(lines, detailRowLines(rows, 0)...)
	accessRows := []detailRow{
		{label: "Admin user", value: cfg.AdminUser},
		{label: "Admin pass", value: cfg.AdminPassword},
	}
	if includeURLs && hasDetailRows(accessRows) {
		lines = append(lines, "", "Access")
		lines = append(lines, detailRowLines(accessRows, 2)...)
	}
	return strings.Join(lines, "\n")
}

func localEnvPHPVersion() string { return "8.3" }

func renderEnvUploadsINI() string {
	return "file_uploads=On\nmemory_limit=256M\nupload_max_filesize=128M\npost_max_size=128M\nmax_execution_time=120\nmax_input_time=120\n"
}

func renderEnvDockerfile() string {
	return `FROM wordpress:php8.3-apache

RUN a2enmod rewrite \
  && sed -ri 's/AllowOverride None/AllowOverride All/g' /etc/apache2/apache2.conf

COPY wordpress/wordpress-rewrites.conf /etc/apache2/conf-enabled/wordpress-rewrites.conf
`
}

func renderEnvRewritesConf() string {
	return `<Directory /var/www/html>
  Options FollowSymLinks
  AllowOverride All
  Require all granted

  RewriteEngine On
  RewriteBase /
  RewriteRule ^index\.php$ - [L]
  RewriteCond %{REQUEST_FILENAME} !-f
  RewriteCond %{REQUEST_FILENAME} !-d
  RewriteRule . /index.php [L]
</Directory>
`
}

func cmdPasswordDerive(slug, purpose string, nonInteractive bool) int {
	if err := envwizard.Ensure(passwordRequirements(), nonInteractive); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	salt, err := passwords.SecretSalt()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(passwords.DerivePassword(slug, purpose, salt))
	return 0
}

func cmdThemePackage(source, output string, dryRun bool) int {
	return cmdPackage("theme package", source, output, dryRun)
}

func parseThemeDeployArgs(args []string) (remote string, dryRun bool, ok bool) {
	for _, arg := range args {
		switch arg {
		case "--dry-run":
			dryRun = true
		case "--":
			return "", false, false
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, false
			}
			if remote != "" {
				return "", false, false
			}
			remote = arg
		}
	}
	return remote, dryRun, true
}

func chooseProjectRemote(action string) (string, error) {
	options, err := projectRemoteSelectOptions(action)
	if err != nil {
		return "", err
	}
	return remoteSelectFn("Choose a remote to "+action, options)
}

func chooseProjectRemoteOrOnly(action string) (string, error) {
	options, err := projectRemoteSelectOptions(action)
	if err != nil {
		return "", err
	}
	if len(options) == 1 {
		return options[0].Value, nil
	}
	return remoteSelectFn("Choose a remote to "+action, options)
}

func projectRemoteSelectOptions(action string) ([]ui.SelectOption, error) {
	root, ok := currentGitRoot()
	if !ok {
		return nil, ProjectError{Msg: fmt.Sprintf("%s requires a .git repository above the current directory", action)}
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		return nil, err
	}
	remotes, err := projectRemotes(metadata, false)
	if err != nil {
		return nil, err
	}
	if len(remotes) == 0 {
		return nil, ProjectError{Msg: "No remotes found. Add one with nf remote add <name> <site.target:env>."}
	}
	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	options := make([]ui.SelectOption, 0, len(names))
	for _, name := range names {
		remote, _ := remotes[name].(map[string]any)
		label := name
		if remote != nil {
			if siteID := strings.TrimSpace(recordValueString(remote["site_id"])); siteID != "" {
				if env := strings.TrimSpace(recordValueString(remote["env"])); env != "" {
					label += " -> " + siteID + ":" + env
				} else {
					label += " -> " + siteID
				}
			}
		}
		options = append(options, ui.SelectOption{Value: name, Label: label})
	}
	return options, nil
}

func validateThemeDeploySlug(slug string) error {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return ProjectError{Msg: "wordpress.theme_slug cannot be empty"}
	}
	if filepath.IsAbs(slug) || strings.ContainsAny(slug, "/\\") || strings.Contains(slug, "..") {
		return ProjectError{Msg: fmt.Sprintf("wordpress.theme_slug %q must be one safe directory name", slug)}
	}
	return nil
}

func cmdPackage(commandName, source, output string, dryRun bool) int {
	root, _ := config.DiscoverProjectRoot("")
	if root == "" {
		root = "."
	}
	metadata, err := theme.LoadProjectMetadata(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if source == "" {
		source = firstNonEmpty(mapStringAtPath(metadata, "wordpress", "theme_path"), "theme")
	}
	sourceDir := source
	if !filepath.IsAbs(sourceDir) {
		sourceDir = filepath.Join(root, sourceDir)
	}
	projectSlug := firstNonEmpty(mapStringAtPath(metadata, "project", "slug"), "project")
	versionedOutput := output
	if versionedOutput == "" {
		versionedOutput = firstNonEmpty(mapStringAtPath(metadata, "artifact", "path"), filepath.ToSlash(filepath.Join("dist", projectSlug+"-v{version}.zip")))
	}
	versionedOutput, err = resolveVersionedArtifactPath(sourceDir, versionedOutput)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	output = versionedOutput
	if !filepath.IsAbs(output) {
		if strings.HasSuffix(strings.ToLower(output), ".zip") {
			output = filepath.Join(root, output)
		} else {
			output = filepath.Join(root, output, projectSlug+".zip")
		}
	}
	result, err := theme.PackageTheme(sourceDir, output, dryRun)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if result.DryRun {
		fmt.Printf("Would package %s -> %s (%d files)\n", result.SourceDir, result.OutputPath, result.FileCount)
	} else {
		fmt.Printf("Wrote %s (%d files)\n", result.OutputPath, result.FileCount)
	}
	return 0
}

func resolveVersionedArtifactPath(sourceDir, template string) (string, error) {
	if !strings.Contains(template, "{version}") {
		return template, nil
	}
	version, err := readThemeVersion(sourceDir)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(template, "{version}", version), nil
}

func readThemeVersion(sourceDir string) (string, error) {
	stylePath := filepath.Join(sourceDir, "style.css")
	if version, found, err := readThemeStyleVersion(stylePath); err != nil {
		return "", err
	} else if found {
		return version, nil
	}

	packagePath := filepath.Join(sourceDir, "package.json")
	if version, found, err := readThemePackageVersion(packagePath); err != nil {
		return "", err
	} else if found {
		return version, nil
	}

	return "", fmt.Errorf("theme version not found: no Version header in %s and no version field in %s", stylePath, packagePath)
}

func readThemeStyleVersion(stylePath string) (string, bool, error) {
	data, err := os.ReadFile(stylePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "*"))
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "version") {
			continue
		}
		version := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "*/"))
		if version != "" {
			return version, true, nil
		}
	}
	return "", false, nil
}

func readThemePackageVersion(packagePath string) (string, bool, error) {
	data, err := os.ReadFile(packagePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", false, err
	}
	version := strings.TrimSpace(payload.Version)
	if version == "" {
		return "", false, nil
	}
	return version, true, nil
}

func normalizePassthroughArgs(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}

func buildParser() *flag.FlagSet { return flag.NewFlagSet("nf", flag.ContinueOnError) }

type helpLine struct {
	Command     string
	Description string
}

func cliCommandAlias(name string) string {
	switch name {
	case "ls":
		return "list"
	case "rm":
		return "remove"
	case "ssh":
		return "shell"
	default:
		return name
	}
}

func printGroupHelp(title string, lines []helpLine) {
	fmt.Println(title)
	fmt.Println("\nCommands:")
	width := 0
	for _, line := range lines {
		if len(line.Command) > width {
			width = len(line.Command)
		}
	}
	for _, line := range lines {
		if line.Command == "" && line.Description == "" {
			fmt.Println()
			continue
		}
		if line.Description == "" {
			fmt.Printf("  %s\n", line.Command)
			continue
		}
		fmt.Printf("  %-*s  %s\n", width, line.Command, line.Description)
	}
}

func runServerHelp() int {
	printGroupHelp("server", []helpLine{
		{"provision [flags]", "provision an infrastructure host"},
		{"list, ls", "list servers"},
		{"show <id-or-name>", "show a server"},
		{"root-password <id-or-name>", "derive the Linode root password"},
		{"delete <id-or-name> [flags]", "delete a server"},
	})
	return 0
}

func runProviderHelp() int {
	printGroupHelp("provider", []helpLine{
		{"list, ls", "list provider integrations"},
		{"check [provider] [--json]", "run provider healthcheck"},
		{"show [provider] [--json]", "show cached provider metadata"},
	})
	return 0
}

func runTargetHelp() int {
	printGroupHelp("target", []helpLine{
		{"list, ls", "list deployable targets"},
		{"show <target>", "show a deployable target"},
		{"refresh", "refresh targets from providers"},
		{"add linode <name> [flags]", "create a Linode target"},
		{"remove, rm <target>", "remove an empty Linode target"},
	})
	return 0
}

func runRemoteHelp() int {
	printGroupHelp("remote", []helpLine{
		{"list, ls", "list repo remotes"},
		{"show <name>", "show a repo remote"},
		{"add [name] [env]", "add a repo remote"},
		{"remove, rm <name>", "remove a repo remote"},
	})
	return 0
}

func runSiteHelp() int {
	printGroupHelp("site", []helpLine{
		{"list, ls [--envs]", "list sites or remote envs"},
		{"show [site|env] [--json]", "show a site or remote env"},
		{"shell, ssh <env>", "shell into a remote env"},
		{"wp <env> -- <args>", "run wp-cli against a remote env"},
		{"snapshot [env] [--output path] [--dry-run]", "download remote database and mutable wp-content"},
		{"password [site]", "show admin password only"},
		{"refresh", "refresh local site cache"},
		{"add <target> <site> [flags]", "create live and staging envs"},
		{"remove, rm [site] [flags]", "remove a site and both envs"},
	})
	return 0
}

func runConfigHelp() int {
	printGroupHelp("config", []helpLine{
		{"init", "initialize local config"},
		{"show", "show global config"},
		{"set-base-domain <domain>", "set provider base domain"},
		{"set-default-wp-email <email>", "set default WordPress email"},
		{"set-default-wp-user <user>", "set default WordPress user"},
		{"set-kinsta-default-region <region>", "set default Kinsta region"},
		{"set-kinsta-default-php <version>", "set default Kinsta PHP version"},
		{"set-linode-default-region <region>", "set default Linode region"},
		{"set-linode-default-type <type>", "set default Linode type"},
		{"set-linode-default-image <image>", "set default Linode image"},
		{"set-linode-default-user <user>", "set default Linode SSH user"},
	})
	return 0
}

func runPasswordHelp() int {
	printGroupHelp("password", []helpLine{
		{"show-salt", "show the masked password salt"},
		{"set-salt <salt>", "save the shared password salt"},
		{"derive <scope> [args...]", "derive a password"},
	})
	return 0
}

func runCompletionHelp() int {
	printGroupHelp("completion", []helpLine{
		{"bash", "print bash completion script"},
		{"zsh", "print zsh completion script"},
	})
	return 0
}

func runCompletion(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runCompletionHelp()
	}
	if len(argv) != 1 {
		fmt.Fprintln(os.Stderr, "completion takes exactly one shell: bash or zsh")
		return 1
	}
	switch argv[0] {
	case "bash":
		fmt.Print(bashCompletionScript())
		return 0
	case "zsh":
		fmt.Print(zshCompletionScript())
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unsupported completion shell %q; use bash or zsh\n", argv[0])
		return 1
	}
}

func bashCompletionScript() string {
	return `# bash completion for nf
_nf_completion() {
  local cur nf_command
  cur="${COMP_WORDS[COMP_CWORD]}"
  nf_command="$(command -v nf)"
  COMPREPLY=( $(compgen -W "$("$nf_command" __complete -- "${COMP_WORDS[@]:1:$COMP_CWORD}")" -- "$cur") )
}
complete -F _nf_completion nf
`
}

func zshCompletionScript() string {
	return `#compdef nf
# zsh completion for nf
_nf() {
  local -a args completions
  local i nf_command
  args=()
  for (( i = 2; i <= CURRENT; i++ )); do
    args+=("${words[i]}")
  done
  if [[ -n "$NF_COMPLETION_DEBUG" ]]; then
    print -ru2 -- "nf completion debug: CURRENT=$CURRENT PREFIX=$PREFIX SUFFIX=$SUFFIX words=(${words[*]}) args=(${args[*]})"
  fi
  nf_command="$(command -v nf)"
  completions=( ${(f)"$("$nf_command" __complete -- "${args[@]}")"} )
  compadd -Q -U -S ' ' -- "${completions[@]}"
}
compctl -d nf 2>/dev/null || true
compdef _nf nf
`
}

func runComplete(argv []string) int {
	if len(argv) > 0 && argv[0] == "--" {
		argv = argv[1:]
	}
	for _, value := range completeCandidates(argv) {
		fmt.Println(value)
	}
	return 0
}

func completeCandidates(argv []string) []string {
	prefix := ""
	args := append([]string{}, argv...)
	if len(args) > 0 {
		prefix = args[len(args)-1]
		args = args[:len(args)-1]
	}
	candidates := completeContextCandidates(args)
	return filterCompletionCandidates(candidates, prefix)
}

func completeContextCandidates(args []string) []string {
	if len(args) == 0 {
		return rootCompletionCandidates()
	}
	switch args[0] {
	case "help":
		return rootCompletionCandidates()
	case "completion":
		return []string{"bash", "zsh"}
	case "provider":
		return providerCompletionCandidates(args[1:])
	case "target":
		return targetCompletionCandidates(args[1:])
	case "site":
		return siteCompletionCandidates(args[1:])
	case "config":
		return []string{"init", "show", "set-base-domain", "set-default-wp-email", "set-default-wp-user", "set-kinsta-default-region", "set-kinsta-default-php", "set-linode-default-region", "set-linode-default-type", "set-linode-default-image", "set-linode-default-user", "help"}
	case "password":
		return []string{"show-salt", "set-salt", "derive", "help"}
	case "remote":
		return remoteCompletionCandidates(args[1:])
	case "env":
		return envCompletionCandidates(args[1:])
	case "theme":
		return themeCompletionCandidates(args[1:])
	default:
		return nil
	}
}

func rootCompletionCandidates() []string {
	candidates := []string{"init", "provider", "target", "site", "config", "password", "completion", "help"}
	if projectContextAvailable() {
		candidates = append(candidates, "remote", "env", "theme")
	}
	sort.Strings(candidates)
	return candidates
}

func providerCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"list", "ls", "check", "show", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "check", "show":
		return []string{"dnsimple", "kinsta", "linode"}
	default:
		return nil
	}
}

func targetCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"list", "ls", "show", "refresh", "add", "remove", "rm", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "add":
		if len(args) == 1 {
			return []string{"linode"}
		}
		return targetAddFlagCandidates()
	case "show":
		return cachedTargetCompletionNames()
	case "remove":
		return cachedTargetCompletionNames()
	default:
		return nil
	}
}

func targetAddFlagCandidates() []string {
	return []string{"--region", "--type", "--image", "--ssh-user", "--execute", "--yes", "--non-interactive", "--dry-run"}
}

func siteCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"list", "ls", "show", "shell", "ssh", "wp", "snapshot", "password", "refresh", "add", "remove", "rm", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "list":
		for _, arg := range args[1:] {
			if arg == "--envs" {
				return cachedSiteCompletionNames()
			}
		}
		return []string{"--envs"}
	case "show":
		return cachedSiteAndEnvCompletionNames()
	case "shell", "wp", "snapshot":
		return cachedSiteEnvCompletionNames()
	case "password":
		return cachedSiteCompletionNames()
	case "remove":
		return cachedSiteCompletionNames()
	case "add":
		if len(args) == 1 {
			return cachedTargetCompletionNames()
		}
		return []string{"--region", "--php", "--dry-run", "--execute", "--yes", "--non-interactive"}
	default:
		return nil
	}
}

func remoteCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"list", "ls", "show", "add", "remove", "rm", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "show", "remove":
		return projectRemoteCompletionNames()
	case "add":
		if len(args) == 2 {
			return cachedSiteEnvCompletionNames()
		}
	}
	return nil
}

func envCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"show", "password", "up", "down", "logs", "shell", "ssh", "wp", "snapshot", "pull", "push", "reset", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "pull", "push":
		return projectRemoteCompletionNames()
	case "snapshot":
		return envSnapshotCompletionCandidates(args[1:])
	default:
		return nil
	}
}

func envSnapshotCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"list", "ls", "add", "use", "remove", "rm", "prune", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "use", "remove":
		return envSnapshotCompletionNames()
	case "prune":
		return []string{"--keep", "--dry-run", "--yes"}
	default:
		return nil
	}
}

func themeCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		candidates := []string{"tasks", "package", "deploy", "help"}
		candidates = append(candidates, projectTaskCompletionNames()...)
		return uniqueSortedStrings(candidates)
	}
	switch args[0] {
	case "package":
		return []string{"--dry-run", "--source", "--output"}
	case "deploy":
		return projectRemoteCompletionNames()
	default:
		return nil
	}
}

func cachedTargetCompletionNames() []string {
	targets, err := completionCachedTargets()
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(targets))
	for _, target := range targets {
		values = append(values, firstRecordString(target, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id"))
	}
	return uniqueSortedStrings(values)
}

func completionCachedTargets() ([]map[string]any, error) {
	providers, err := state.LoadStateRecords("providers")
	if err != nil {
		return nil, err
	}
	if len(providers) > 0 {
		return providerTargetRecords(providers), nil
	}
	return state.LoadStateRecords("servers")
}

func cachedSiteCompletionNames() []string {
	sites, err := state.LoadStateRecords("sites")
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(sites))
	for _, site := range sites {
		values = append(values, siteRecordID(site))
	}
	return uniqueSortedStrings(values)
}

func cachedSiteEnvCompletionNames() []string {
	sites, err := state.LoadStateRecords("sites")
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(sites))
	for _, site := range sites {
		values = append(values, siteRecordEnvID(site))
	}
	return uniqueSortedStrings(values)
}

func cachedSiteAndEnvCompletionNames() []string {
	values := append(cachedSiteCompletionNames(), cachedSiteEnvCompletionNames()...)
	return uniqueSortedStrings(values)
}

func projectRemoteCompletionNames() []string {
	root, ok := currentNFProjectRoot()
	if !ok {
		return nil
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		return nil
	}
	remotes, err := projectRemotes(metadata, false)
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(remotes))
	for name := range remotes {
		values = append(values, name)
	}
	return uniqueSortedStrings(values)
}

func projectTaskCompletionNames() []string {
	root, ok := currentNFProjectRoot()
	if !ok {
		return nil
	}
	tasks, err := loadProjectTasks(root)
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(tasks))
	for name := range tasks {
		values = append(values, name)
	}
	return uniqueSortedStrings(values)
}

func envSnapshotCompletionNames() []string {
	root, ok := currentNFProjectRoot()
	if !ok {
		return nil
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		return nil
	}
	cfg, ok := loadEnvConfig(root, metadata)
	if !ok {
		return nil
	}
	records, err := loadEnvSnapshots(cfg)
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(records))
	for _, record := range records {
		values = append(values, firstNonEmpty(record.Metadata.Name, filepath.Base(record.Directory)))
	}
	return uniqueSortedStrings(values)
}

func filterCompletionCandidates(candidates []string, prefix string) []string {
	values := make([]string, 0, len(candidates))
	for _, candidate := range uniqueStrings(candidates) {
		if strings.HasPrefix(candidate, prefix) {
			values = append(values, candidate)
		}
	}
	return values
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	unique := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || strings.ContainsAny(value, " \t\n\r") {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func uniqueSortedStrings(values []string) []string {
	unique := uniqueStrings(values)
	sort.Strings(unique)
	return unique
}

func runInitHelp() int {
	fmt.Println("init")
	fmt.Println("\nUsage:")
	fmt.Println("  nf init [flags]")
	fmt.Println("\nFlags:")
	for _, line := range []string{
		"--project-slug string   project slug (defaults to the current git root name)",
		"--project-name string   project name",
		"--theme-slug string     mounted theme slug",
		"--theme-source string   theme source directory",
		"--type string           project type (default wordpress-theme)",
		"--force                 overwrite .nf/project.json",
	} {
		fmt.Printf("  %s\n", line)
	}
	return 0
}

func runEnvHelp() int {
	printGroupHelp("env", []helpLine{
		{"show", "show paths, ports, and URLs"},
		{"password", "show admin password only"},
		{"up", "start the local env"},
		{"down", "stop the local env"},
		{"logs", "tail WordPress logs"},
		{"shell, ssh", "open a shell in the local env"},
		{"wp -- <args>", "run wp-cli in the local env"},
		{"snapshot", "manage env snapshots"},
		{"pull [remote] [--dry-run] [--execute] [--yes]", "pull database and mutable wp-content from a remote env"},
		{"push [remote] [--dry-run] [--execute] [--yes]", "push database and mutable wp-content to a remote env"},
		{"reset", "destroy and recreate the local env"},
	})
	return 0
}

func runThemeHelp() int {
	lines := []helpLine{
		{"tasks", "list configured theme tasks"},
		{"package [--dry-run] [--source] [--output]", "package theme files"},
		{"deploy <remote> [--dry-run]", "rsync theme files to a repo remote"},
	}
	printGroupHelp("theme", lines)
	if projectContextAvailable() {
		if root, ok := currentGitRoot(); ok {
			if tasks, err := loadProjectTasks(root); err == nil && len(tasks) > 0 {
				fmt.Println("\nTheme tasks:")
				for _, line := range formatProjectTaskLines(tasks) {
					fmt.Printf("  %s\n", line)
				}
			}
		}
	}
	return 0
}

func parseEnvRemoteSyncArgs(action string, args []string) (string, envRemoteSyncOptions, bool) {
	var opts envRemoteSyncOptions
	positionals := make([]string, 0, 1)
	for _, arg := range args {
		switch arg {
		case "--dry-run":
			opts.dryRun = true
		case "--execute":
			opts.execute = true
		case "--yes":
			opts.yes = true
		case "--non-interactive":
			opts.nonInteractive = true
		default:
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(os.Stderr, "unknown env %s flag: %s\n", action, arg)
				return "", opts, false
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 1 {
		fmt.Fprintf(os.Stderr, "env %s takes at most one remote\n", action)
		return "", opts, false
	}
	if len(positionals) == 0 {
		return "", opts, true
	}
	return positionals[0], opts, true
}

func runInit(argv []string) int {
	if len(argv) > 0 && (argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h") {
		return runInitHelp()
	}
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	projectSlug := fs.String("project-slug", "", "")
	projectName := fs.String("project-name", "", "")
	themeSlug := fs.String("theme-slug", "", "")
	themeSource := fs.String("theme-source", "", "")
	projectType := fs.String("type", "wordpress-theme", "")
	force := fs.Bool("force", false, "")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(argv); err != nil {
		return 1
	}
	if strings.TrimSpace(*projectType) != "wordpress-theme" {
		fmt.Fprintf(os.Stderr, "unsupported init type %q; only wordpress-theme is supported\n", *projectType)
		return 1
	}
	if strings.TrimSpace(*projectSlug) == "" {
		derivedSlug, err := currentGitRootBase()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		projectSlug = &derivedSlug
	}
	return cmdProjectInit(projectInitArgs{projectSlug: *projectSlug, projectName: *projectName, themeSlug: *themeSlug, themeSource: *themeSource, projectType: *projectType, force: *force})
}

func runTheme(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runThemeHelp()
	}
	switch argv[0] {
	case "tasks":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "theme tasks takes no arguments")
			return 1
		}
		if err := requireProjectContext("theme tasks"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return cmdThemeTasks()
	case "package":
		if err := requireProjectContext("theme package"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fs := flag.NewFlagSet("theme package", flag.ContinueOnError)
		source := fs.String("source", "", "")
		output := fs.String("output", "", "")
		dryRun := fs.Bool("dry-run", false, "")
		fs.SetOutput(os.Stderr)
		if err := fs.Parse(argv[1:]); err != nil {
			return 1
		}
		return cmdThemePackage(*source, *output, *dryRun)
	case "deploy":
		if err := requireProjectContext("theme deploy"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		remote, dryRun, ok := parseThemeDeployArgs(argv[1:])
		if !ok {
			fmt.Fprintln(os.Stderr, "theme deploy takes exactly one remote")
			return 1
		}
		if strings.TrimSpace(remote) == "" {
			selected, err := chooseProjectRemote("deploy theme to")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			remote = selected
		}
		return cmdThemeDeploy(remote, dryRun)
	default:
		if err := requireProjectContext("theme " + argv[0]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		root, err := discoverProjectRootOrError()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		tasks, err := loadProjectTasks(root)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if task, ok := tasks[argv[0]]; ok {
			extraArgs := normalizePassthroughArgs(argv[1:])
			if err := task.Run.Execute(root, extraArgs); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			return 0
		}
		fmt.Fprintln(os.Stderr, "unsupported theme command")
		return 1
	}
}

func runEnv(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" {
		return runEnvHelp()
	}
	name := argv[0]
	name = cliCommandAlias(name)
	switch name {
	case "show", "password", "up", "down", "logs", "reset", "shell", "wp", "push", "pull", "snapshot":
	default:
		fmt.Fprintln(os.Stderr, "unsupported env command")
		return 1
	}
	if name == "snapshot" {
		return runEnvSnapshot(argv[1:])
	}
	if name == "show" && len(argv) != 1 {
		fmt.Fprintln(os.Stderr, "env show takes no arguments")
		return 1
	}
	if name == "password" && len(argv) != 1 {
		fmt.Fprintln(os.Stderr, "env password takes no arguments")
		return 1
	}
	if name == "shell" && len(argv) != 1 {
		fmt.Fprintln(os.Stderr, "env shell takes no arguments")
		return 1
	}
	if name == "push" || name == "pull" {
		remoteName, opts, ok := parseEnvRemoteSyncArgs(name, argv[1:])
		if !ok {
			return 1
		}
		argv = []string{name, remoteName}
		if opts.dryRun {
			argv = append(argv, "--dry-run")
		}
		if opts.execute {
			argv = append(argv, "--execute")
		}
		if opts.yes {
			argv = append(argv, "--yes")
		}
		if opts.nonInteractive {
			argv = append(argv, "--non-interactive")
		}
	}
	if name == "up" {
		if err := ensureEnvProjectMetadata(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if err := requireProjectContext("env " + name); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root, err := discoverProjectRootOrError()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cfg, ok := loadEnvConfig(root, metadata)
	if !ok {
		fmt.Fprintln(os.Stderr, "Missing env metadata in .nf/project.json. Run nf env up first.")
		return 1
	}
	if name == "show" {
		credentialCfg, err := envConfigWithAdminCredentials(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(renderEnvInfo(credentialCfg, true))
		return 0
	}
	if name == "password" {
		return cmdEnvPassword(cfg)
	}
	if name == "push" || name == "pull" {
		remoteName, opts, ok := parseEnvRemoteSyncArgs(name, argv[1:])
		if !ok {
			return 1
		}
		return cmdEnvRemoteSyncPlan(name, remoteName, cfg, metadata, opts)
	}
	if name == "up" {
		if err := preflightEnvPorts(cfg); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	extraArgs := argv[1:]
	if name == "wp" {
		extraArgs = normalizePassthroughArgs(extraArgs)
	}
	if err := (envCommandRunner{name: name, cfg: cfg}).Execute(root, extraArgs); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	switch name {
	case "up":
		fmt.Println("Env started.")
		fmt.Println()
		credentialCfg, err := envConfigWithAdminCredentials(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(renderEnvInfo(credentialCfg, true))
	case "reset":
		fmt.Println("Env reset.")
		fmt.Println()
		credentialCfg, err := envConfigWithAdminCredentials(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(renderEnvInfo(credentialCfg, true))
	case "down":
		fmt.Println("Env stopped.")
		fmt.Println()
		fmt.Println(renderEnvInfo(cfg, false))
	}
	return 0
}

func runEnvSnapshot(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" {
		printGroupHelp("env snapshot", []helpLine{
			{"list, ls", "list env snapshots"},
			{"add [name]", "create an env snapshot"},
			{"use [name]", "restore an env snapshot"},
			{"remove, rm [name]", "delete an env snapshot"},
			{"prune [--keep N] [--dry-run] [--yes]", "delete old auto snapshots"},
		})
		return 0
	}
	cmd := cliCommandAlias(argv[0])
	args := argv[1:]
	switch cmd {
	case "list":
		if len(args) != 0 {
			fmt.Fprintln(os.Stderr, "env snapshot list takes no arguments")
			return 1
		}
	case "add", "use", "remove":
		if len(args) > 1 {
			fmt.Fprintln(os.Stderr, "env snapshot command takes at most one name")
			return 1
		}
	case "prune":
	default:
		fmt.Fprintln(os.Stderr, "unsupported env snapshot command")
		return 1
	}
	if err := requireProjectContext("env snapshot " + cmd); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root, err := discoverProjectRootOrError()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cfg, ok := loadEnvConfig(root, metadata)
	if !ok {
		fmt.Fprintln(os.Stderr, "Missing env metadata in .nf/project.json. Run nf env up first.")
		return 1
	}
	switch cmd {
	case "list":
		return cmdEnvSnapshotList(cfg)
	case "add":
		name := ""
		if len(args) == 1 {
			name = args[0]
		}
		return cmdEnvSnapshotCreate(cfg, name, false)
	case "use":
		name := ""
		if len(args) == 1 {
			name = args[0]
		}
		return cmdEnvSnapshotRestore(cfg, name, false)
	case "remove":
		name := ""
		if len(args) == 1 {
			name = args[0]
		}
		return cmdEnvSnapshotDelete(cfg, name, false)
	case "prune":
		opts, err := parseEnvSnapshotPruneArgs(args)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return cmdEnvSnapshotPrune(cfg, opts)
	default:
		return 1
	}
}

func runHelp() int {
	lines := []helpLine{
		{"init", "initialize project metadata"},
		{"provider", "manage provider integrations"},
		{"target", "manage deployable targets"},
		{"site", "manage remote sites and envs"},
	}
	if projectContextAvailable() {
		lines = append(lines,
			helpLine{"remote", "manage repo remotes"},
			helpLine{"env", "manage the local development env"},
			helpLine{"theme", "package files and run theme tasks"},
		)
	}
	lines = append(lines,
		helpLine{"config", "manage global config"},
		helpLine{"password", "derive passwords"},
		helpLine{"completion", "print shell completion scripts"},
		helpLine{"help", "show help"},
	)
	printGroupHelp("nf", lines)
	return 0
}

func projectOnlyCommand(name string) bool {
	switch name {
	case "remote", "theme", "env":
		return true
	default:
		return false
	}
}

func rejectOutsideProject(command string) bool {
	if !projectOnlyCommand(command) {
		return false
	}
	if err := requireProjectContext(command); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return true
	}
	return false
}

type deleteServerOptions struct {
	dryRun         bool
	execute        bool
	yes            bool
	nonInteractive bool
}

func parseDeleteServerArgs(argv []string) (string, deleteServerOptions, error) {
	var opts deleteServerOptions
	positionals := make([]string, 0, 1)
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch arg {
		case "--":
			positionals = append(positionals, argv[i+1:]...)
			i = len(argv)
		case "--non-interactive":
			opts.nonInteractive = true
		case "--execute":
			opts.execute = true
		case "--yes":
			opts.yes = true
		case "--dry-run":
			opts.dryRun = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", opts, fmt.Errorf("unsupported flag %s", arg)
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 1 {
		return "", opts, fmt.Errorf("server delete takes at most one id or name")
	}
	if len(positionals) == 0 {
		return "", opts, nil
	}
	return positionals[0], opts, nil
}

func parseRemoveTargetArgs(argv []string) (string, deleteServerOptions, error) {
	needle, opts, err := parseDeleteServerArgs(argv)
	if err != nil {
		return "", opts, fmt.Errorf("%s", strings.Replace(err.Error(), "server delete", "target remove", 1))
	}
	return needle, opts, nil
}

func parseRemoveSiteArgs(argv []string) (string, deleteServerOptions, error) {
	needle, opts, err := parseDeleteServerArgs(argv)
	if err != nil {
		return "", opts, fmt.Errorf("%s", strings.Replace(err.Error(), "server delete", "site remove", 1))
	}
	return needle, opts, nil
}

func parseSiteSnapshotArgs(argv []string) (string, siteSnapshotOptions, error) {
	var opts siteSnapshotOptions
	positionals := make([]string, 0, 1)
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch arg {
		case "--dry-run":
			opts.dryRun = true
		case "--output":
			if i+1 >= len(argv) || strings.TrimSpace(argv[i+1]) == "" {
				return "", opts, fmt.Errorf("site snapshot --output requires a path")
			}
			i++
			opts.output = argv[i]
		default:
			if strings.HasPrefix(arg, "--output=") {
				opts.output = strings.TrimPrefix(arg, "--output=")
				if strings.TrimSpace(opts.output) == "" {
					return "", opts, fmt.Errorf("site snapshot --output requires a path")
				}
				continue
			}
			if strings.HasPrefix(arg, "-") {
				return "", opts, fmt.Errorf("unsupported flag %s", arg)
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 1 {
		return "", opts, fmt.Errorf("site snapshot takes at most one env ref")
	}
	if len(positionals) == 0 {
		return "", opts, nil
	}
	return positionals[0], opts, nil
}

func Run(argv []string) int {
	if len(argv) == 0 {
		return runHelp()
	}
	switch argv[0] {
	case "--help", "-h", "help":
		if len(argv) == 1 {
			return runHelp()
		}
		return runTopicHelp(argv[1:])
	case "__complete":
		return runComplete(argv[1:])
	case "completion":
		return runCompletion(argv[1:])
	case "provider":
		return runProvider(argv[1:])
	case "target":
		return runTarget(argv[1:])
	case "site":
		return runSite(argv[1:])
	case "remote":
		if rejectOutsideProject(argv[0]) {
			return 1
		}
		return runRemote(argv[1:])
	case "env":
		if !envUpCommand(argv[1:]) && rejectOutsideProject(argv[0]) {
			return 1
		}
		return runEnv(argv[1:])
	case "init":
		return runInit(argv[1:])
	case "theme":
		if rejectOutsideProject(argv[0]) {
			return 1
		}
		return runTheme(argv[1:])
	case "config":
		return runConfig(argv[1:])
	case "password":
		return runPassword(argv[1:])
	default:
		fmt.Fprintf(os.Stderr, "unsupported command: %s\n", argv[0])
		return 1
	}
}

func envUpCommand(argv []string) bool {
	return len(argv) > 0 && argv[0] == "up"
}

func runTopicHelp(argv []string) int {
	if len(argv) == 0 {
		return runHelp()
	}
	switch argv[0] {
	case "provider":
		return runProviderHelp()
	case "target":
		return runTargetHelp()
	case "site":
		return runSiteHelp()
	case "remote":
		if rejectOutsideProject(argv[0]) {
			return 1
		}
		return runRemoteHelp()
	case "env":
		if rejectOutsideProject(argv[0]) {
			return 1
		}
		return runEnvHelp()
	case "init":
		return runInitHelp()
	case "theme":
		if rejectOutsideProject(argv[0]) {
			return 1
		}
		return runThemeHelp()
	case "config":
		return runConfigHelp()
	case "password":
		return runPasswordHelp()
	case "completion":
		return runCompletionHelp()
	default:
		return runHelp()
	}
}

func runPassword(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runPasswordHelp()
	}
	switch argv[0] {
	case "set-salt":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "password set-salt takes exactly one salt")
			return 1
		}
		if _, err := config.SetEnvFile(config.EnvFile(), map[string]string{"NF_PASSWORD_SALT": argv[1]}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("Password Salt: Set")
		return 0
	case "show-salt":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "password show-salt takes no arguments")
			return 1
		}
		salt, err := passwords.SecretSalt()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("Password Salt: %s\n", maskSecret(salt))
		return 0
	case "derive":
		if len(argv) < 3 {
			fmt.Fprintln(os.Stderr, "password derive requires a scope and at least one value")
			return 1
		}
		return cmdPasswordDerive(argv[1], strings.Join(argv[2:], ":"), false)
	default:
		fmt.Fprintln(os.Stderr, "unsupported password command")
		return 1
	}
}

func runConfig(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runConfigHelp()
	}
	switch argv[0] {
	case "init":
		fs := flag.NewFlagSet("config init", flag.ContinueOnError)
		nonInteractive := fs.Bool("non-interactive", false, "")
		fs.SetOutput(os.Stderr)
		if err := fs.Parse(argv[1:]); err != nil {
			if err == flag.ErrHelp {
				return 0
			}
			return 1
		}
		return cmdConfigInit(*nonInteractive)
	case "set-base-domain":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-base-domain takes exactly one domain")
			return 1
		}
		return cmdConfigSet("base_domain", argv[1])
	case "set-default-wp-email":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-default-wp-email takes exactly one email")
			return 1
		}
		return cmdConfigSet("default_wp_email", argv[1])
	case "set-default-wp-user":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-default-wp-user takes exactly one user")
			return 1
		}
		return cmdConfigSet("default_wp_user", argv[1])
	case "set-kinsta-default-region":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-kinsta-default-region takes exactly one region")
			return 1
		}
		return cmdConfigSet("kinsta_default_region", argv[1])
	case "set-kinsta-default-php":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-kinsta-default-php takes exactly one version")
			return 1
		}
		return cmdConfigSet("kinsta_default_php", argv[1])
	case "set-linode-default-region":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-linode-default-region takes exactly one region")
			return 1
		}
		return cmdConfigSet("linode_default_region", argv[1])
	case "set-linode-default-type":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-linode-default-type takes exactly one type")
			return 1
		}
		return cmdConfigSet("linode_default_type", argv[1])
	case "set-linode-default-image":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-linode-default-image takes exactly one image")
			return 1
		}
		return cmdConfigSet("linode_default_image", argv[1])
	case "set-linode-default-user":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-linode-default-user takes exactly one user")
			return 1
		}
		return cmdConfigSet("linode_default_user", argv[1])
	case "show":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "config show takes no arguments")
			return 1
		}
		return cmdConfigShow()
	default:
		fmt.Fprintln(os.Stderr, "unsupported config command")
		return 1
	}
}

func loadGlobalConfig() (map[string]string, error) {
	path := config.ConfigFile()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	values := map[string]string{}
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func saveGlobalConfig(values map[string]string) error {
	path := config.ConfigFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func cmdConfigInit(nonInteractive bool) int {
	if err := envwizard.Init(configInitRequirements(), nonInteractive); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := initGlobalConfig(configInitSettings(), nonInteractive); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := checkProvidersAfterConfigInit(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func checkProvidersAfterConfigInit() error {
	fmt.Println("Checking providers...")
	failed := []string{}
	for _, status := range providerConfigStatuses() {
		if cmdProviderCheck(status.Name, false) != 0 {
			failed = append(failed, status.Name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("provider checks failed: %s", strings.Join(failed, ", "))
	}
	return nil
}

func cmdTargetRefresh() int {
	fmt.Println("Target refresh updates target metadata from configured providers.")
	refreshed := []string{}
	skipped := []string{}
	failed := []string{}
	totalTargets := 0
	for _, status := range providerConfigStatuses() {
		if !providerHasTargets(status.Name) {
			continue
		}
		if len(status.Missing) > 0 {
			skipped = append(skipped, fmt.Sprintf("%s (missing %s)", status.Name, providerMissingLabel(status)))
			continue
		}
		result, err := runProviderHealthcheck(status.Name)
		if err != nil {
			failed = append(failed, status.Name)
			fmt.Fprintf(os.Stderr, "%s: %v\n", status.Name, err)
			continue
		}
		if result.Provider == "" {
			result.Provider = status.Name
		}
		if err := saveProviderHealthRecord(result); err != nil {
			failed = append(failed, status.Name)
			fmt.Fprintf(os.Stderr, "%s: %v\n", status.Name, err)
			continue
		}
		count := len(targetMaps(result.Record["targets"]))
		totalTargets += count
		refreshed = append(refreshed, status.Name)
		fmt.Printf("Provider %s refreshed. Targets: %d\n", status.Name, count)
	}
	if len(skipped) > 0 {
		fmt.Printf("Skipped providers: %d\n", len(skipped))
		for _, line := range skipped {
			fmt.Printf("  - %s\n", line)
		}
	}
	if len(refreshed) > 0 {
		fmt.Printf("Refreshed providers: %d\n", len(refreshed))
		fmt.Printf("Targets: %d\n", totalTargets)
		fmt.Printf("Saved provider metadata to %s.\n", state.StatePath("providers"))
	}
	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "target refresh failed for providers: %s\n", strings.Join(failed, ", "))
		return 1
	}
	if len(refreshed) == 0 {
		fmt.Println("No target providers were refreshed.")
		return 1
	}
	return 0
}

func providerHasTargets(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "kinsta", "linode":
		return true
	default:
		return false
	}
}

func initGlobalConfig(settings []configInitSetting, nonInteractive bool) error {
	values, err := loadGlobalConfig()
	if err != nil {
		return err
	}
	updates := map[string]string{}
	for _, setting := range settings {
		if strings.TrimSpace(values[setting.Key]) != "" {
			continue
		}
		if nonInteractive || !configIsInteractive() {
			if strings.TrimSpace(setting.Default) != "" {
				updates[setting.Key] = strings.TrimSpace(setting.Default)
				continue
			}
			if setting.Required {
				return fmt.Errorf("Missing %s. It is not set in %s. Run `nf config init` interactively to populate it.", setting.Key, config.ConfigFile())
			}
			continue
		}
		value, err := configPromptString(setting.Prompt, setting.Default, false)
		if err != nil {
			return err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			value = strings.TrimSpace(setting.Default)
		}
		if value == "" && setting.Required {
			return fmt.Errorf("%s is required", setting.Key)
		}
		if value != "" {
			updates[setting.Key] = value
		}
	}
	if len(updates) == 0 {
		return nil
	}
	for key, value := range updates {
		values[key] = value
	}
	if err := saveGlobalConfig(values); err != nil {
		return err
	}
	fmt.Printf("Updated %s\n", config.ConfigFile())
	return nil
}

func cmdConfigSet(key, value string) int {
	values, err := loadGlobalConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	values[key] = strings.TrimSpace(value)
	if err := saveGlobalConfig(values); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Set %s\n", key)
	return 0
}

func cmdConfigShow() int {
	values, err := loadGlobalConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	saltStatus := "Unset"
	if _, err := passwords.SecretSalt(); err == nil {
		saltStatus = "Set"
	}
	fmt.Printf("Default WP Email: %s\n", values["default_wp_email"])
	fmt.Printf("Default WP User: %s\n", values["default_wp_user"])
	fmt.Printf("Base Domain: %s\n", values["base_domain"])
	fmt.Printf("DNSimple Account ID: %s\n", values["dnsimple_account_id"])
	fmt.Printf("Kinsta Default Region: %s\n", values["kinsta_default_region"])
	fmt.Printf("Kinsta Default PHP: %s\n", firstNonEmpty(values["kinsta_default_php"], "8.3"))
	fmt.Printf("Linode Default Region: %s\n", values["linode_default_region"])
	fmt.Printf("Linode Default Type: %s\n", values["linode_default_type"])
	fmt.Printf("Linode Default Image: %s\n", values["linode_default_image"])
	fmt.Printf("Linode Default User: %s\n", values["linode_default_user"])
	fmt.Printf("Password Salt: %s\n", saltStatus)
	return 0
}

func cmdRemoteAdd(name, envRef string) int {
	name = strings.TrimSpace(name)
	siteID, env, ok := splitSiteEnvRef(strings.TrimSpace(envRef))
	if name == "" {
		fmt.Fprintln(os.Stderr, "remote add requires a non-empty name")
		return 1
	}
	if !ok || siteID == "" || env == "" {
		fmt.Fprintln(os.Stderr, "remote add requires an env ref like site.target:env")
		return 1
	}
	root, ok := currentGitRoot()
	if !ok {
		fmt.Fprintln(os.Stderr, "remote add requires a .git repository above the current directory")
		return 1
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	remotes, err := projectRemotes(metadata, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	record, _, err := cachedSiteEnv(siteID, env)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if record == nil {
		fmt.Fprintf(os.Stderr, "No cached remote env matched site %q env %q. Run nf site refresh after target cache is current, or update the local state cache.\n", siteID, env)
		return 1
	}
	if err := validateSiteRecord(record); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	remotes[name] = map[string]any{"site_id": siteID, "env": env}
	if err := saveProjectMetadata(root, metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Added remote %s -> %s\n", name, canonicalEnvID(siteID, env))
	return 0
}

func cmdRemoteRemove(name string) int {
	name = strings.TrimSpace(name)
	if name == "" {
		fmt.Fprintln(os.Stderr, "remote remove requires a non-empty name")
		return 1
	}
	root, ok := currentGitRoot()
	if !ok {
		fmt.Fprintln(os.Stderr, "remote remove requires a .git repository above the current directory")
		return 1
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	remotes, err := projectRemotes(metadata, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if remotes == nil {
		fmt.Fprintf(os.Stderr, "No remote named %q.\n", name)
		return 1
	}
	if _, ok := remotes[name]; !ok {
		fmt.Fprintf(os.Stderr, "No remote named %q.\n", name)
		return 1
	}
	delete(remotes, name)
	if err := saveProjectMetadata(root, metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Removed remote %s\n", name)
	return 0
}

func cmdRemoteShow(name string) int {
	name = strings.TrimSpace(name)
	if name == "" {
		fmt.Fprintln(os.Stderr, "remote show requires a non-empty name")
		return 1
	}
	root, ok := currentGitRoot()
	if !ok {
		fmt.Fprintln(os.Stderr, "remote show requires a .git repository above the current directory")
		return 1
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	siteID, remoteEnv, ok, err := projectRemoteAlias(metadata, name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "No remote named %q in .nf/project.json deploy.remotes.\n", name)
		return 1
	}
	envID := canonicalEnvID(siteID, remoteEnv)
	fmt.Printf("Remote: %s\n", name)
	fmt.Printf("Env: %s\n", envID)
	record, _, err := cachedSiteEnv(siteID, remoteEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if record == nil {
		fmt.Println("Cache: no matching cached remote env")
		return 0
	}
	if err := validateSiteRecord(record); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(record["provider"])))
	fmt.Printf("Provider: %s\n", provider)
	target := siteTargetName(record)
	if provider == "linode" && siteServerReference(record) != "" {
		target = siteServerReference(record)
	}
	fmt.Printf("Target: %s\n", target)
	if url := firstRecordString(record, "url", "site_url", "home_url", "hostname"); url != "" {
		fmt.Printf("URL: %s\n", url)
	}
	if provider == "linode" {
		targetRef := siteProviderTarget(record)
		target, err := cachedSiteTarget(targetRef)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if target == nil {
			fmt.Fprintf(os.Stderr, "Linode site %q references target %q, but no cached target matched. Run nf provider check linode.\n", siteSummary(record), targetRef)
			return 1
		}
		fmt.Printf("Target record: %s\n", serverSummary(target))
	}
	return 0
}

func cmdRemoteList() int {
	root, ok := currentGitRoot()
	if !ok {
		fmt.Fprintln(os.Stderr, "remote list requires a .git repository above the current directory")
		return 1
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	remotes, err := projectRemotes(metadata, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(remotes) == 0 {
		fmt.Println("No remotes found.")
		return 0
	}
	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := [][]string{{"remote", "env"}}
	for _, name := range names {
		remote, ok := remotes[name].(map[string]any)
		if !ok || remote == nil {
			fmt.Fprintf(os.Stderr, ".nf/project.json deploy.remotes.%s must be an object\n", name)
			return 1
		}
		siteID := strings.TrimSpace(recordValueString(remote["site_id"]))
		env := strings.TrimSpace(recordValueString(remote["env"]))
		if siteID == "" || env == "" {
			fmt.Fprintf(os.Stderr, ".nf/project.json deploy.remotes.%s must include site_id and env\n", name)
			return 1
		}
		rows = append(rows, []string{name, canonicalEnvID(siteID, env)})
	}
	fmt.Println(formatTable(rows))
	return 0
}

func runProvider(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runProviderHelp()
	}
	argv[0] = cliCommandAlias(argv[0])
	switch argv[0] {
	case "list":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "provider list takes no arguments")
			return 1
		}
		return cmdProviderList()
	case "show":
		name, jsonOutput, err := parseProviderActionArgs(argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if name == "" {
			selected, err := chooseProvider("show")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			name = selected
		}
		return cmdProviderShow(name, jsonOutput)
	case "check":
		name, jsonOutput, err := parseProviderActionArgs(argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if name == "" {
			selected, err := chooseProvider("check")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			name = selected
		}
		return cmdProviderCheck(name, jsonOutput)
	default:
		fmt.Fprintln(os.Stderr, "unsupported provider command")
		return 1
	}
}

func parseProviderActionArgs(argv []string) (string, bool, error) {
	var name string
	jsonOutput := false
	for _, arg := range argv {
		switch arg {
		case "--json":
			jsonOutput = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, fmt.Errorf("unsupported flag %s", arg)
			}
			if name != "" {
				return "", false, fmt.Errorf("provider command takes at most one provider")
			}
			name = arg
		}
	}
	return name, jsonOutput, nil
}

func chooseProvider(action string) (string, error) {
	statuses := providerConfigStatuses()
	options := make([]ui.SelectOption, 0, len(statuses))
	for _, status := range statuses {
		options = append(options, ui.SelectOption{Value: status.Name, Label: status.Name})
	}
	if len(options) == 0 {
		return "", ProjectError{Msg: "No selectable providers found."}
	}
	return providerSelectFn(fmt.Sprintf("Choose a provider to %s", action), options)
}

type providerConfigKey struct {
	Keys     []string
	Required bool
	Default  string
	Secret   bool
}

type providerConfigStatus struct {
	Name    string
	Keys    []providerConfigKey
	Missing []string
	Values  map[string]string
}

type providerHealthResult struct {
	Provider string
	Details  map[string]string
	Record   map[string]any
}

func providerConfigStatuses() []providerConfigStatus {
	return []providerConfigStatus{
		providerConfigStatusFor("dnsimple", []providerConfigKey{
			{Keys: []string{"base_domain"}, Required: true},
			{Keys: []string{"DNSIMPLE_TOKEN"}, Required: true, Secret: true},
			{Keys: []string{"dnsimple_account_id"}},
		}),
		providerConfigStatusFor("kinsta", []providerConfigKey{
			{Keys: []string{"KINSTA_API_KEY"}, Required: true, Secret: true},
		}),
		providerConfigStatusFor("linode", []providerConfigKey{
			{Keys: []string{"LINODE_TOKEN", "LINODE_CLI_TOKEN"}, Required: true, Secret: true},
		}),
	}
}

func providerConfigStatusFor(name string, keys []providerConfigKey) providerConfigStatus {
	status := providerConfigStatus{Name: name, Keys: keys, Values: map[string]string{}}
	for _, group := range keys {
		value := ""
		for _, key := range group.Keys {
			if v := providerConfigValue(key); v != "" {
				value = v
				status.Values[key] = v
				break
			}
		}
		if value == "" && group.Default != "" {
			value = group.Default
			status.Values[group.Keys[0]] = group.Default
		}
		if value == "" && group.Required {
			status.Missing = append(status.Missing, strings.Join(group.Keys, " or "))
		}
	}
	return status
}

func providerConfigValue(key string) string {
	switch key {
	case "base_domain":
		return baseDomainValue()
	case "dnsimple_account_id":
		return dnsimpleAccountIDValue()
	default:
		return envwizard.Value(key)
	}
}

func providerConfigStatusByName(name string) (providerConfigStatus, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, status := range providerConfigStatuses() {
		if status.Name == name {
			return status, true
		}
	}
	return providerConfigStatus{}, false
}

func providerStatusLabel(status providerConfigStatus) string {
	if len(status.Missing) == 0 {
		return "configured"
	}
	return "missing"
}

func providerMissingLabel(status providerConfigStatus) string {
	if len(status.Missing) == 0 {
		return "-"
	}
	return strings.Join(status.Missing, ", ")
}

func providerValueLabel(status providerConfigStatus, group providerConfigKey) string {
	for _, key := range group.Keys {
		if value := strings.TrimSpace(status.Values[key]); value != "" {
			if group.Default != "" && value == group.Default && envwizard.Value(key) == "" {
				return value + " (default)"
			}
			if !group.Secret {
				return value
			}
			return maskSecret(value)
		}
	}
	return "unset"
}

func cmdProviderList() int {
	rows := [][]string{{"provider", "status", "missing"}}
	for _, status := range providerConfigStatuses() {
		rows = append(rows, []string{status.Name, providerStatusLabel(status), providerMissingLabel(status)})
	}
	fmt.Println(formatTable(rows))
	return 0
}

func cmdProviderShow(name string, jsonOutput bool) int {
	status, ok := providerConfigStatusByName(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "unsupported provider %q\n", name)
		return 1
	}
	records, err := state.LoadStateRecords("providers")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	record := providerRecordByName(records, status.Name)
	if record == nil {
		fmt.Fprintf(os.Stderr, "No cached provider metadata matched %q. Run nf provider check %s.\n", name, status.Name)
		return 1
	}
	if jsonOutput {
		data, _ := json.MarshalIndent(record, "", "  ")
		fmt.Println(string(data))
		return 0
	}
	printProviderDetails(status, record)
	return 0
}

func printProviderDetails(status providerConfigStatus, record map[string]any) {
	fmt.Printf("Provider: %s\n", status.Name)
	fmt.Printf("Status: %s\n", providerStatusLabel(status))
	if len(status.Missing) > 0 {
		fmt.Printf("Missing: %s\n", providerMissingLabel(status))
	}
	fmt.Printf("Cache: %s\n", state.StatePath("providers"))
	if checkedAt := recordValueString(record["checked_at"]); checkedAt != "" {
		fmt.Printf("Checked at: %s\n", checkedAt)
	}
	for _, field := range []struct {
		Label string
		Keys  []string
	}{
		{Label: "Account ID", Keys: []string{"account_id"}},
		{Label: "Account email", Keys: []string{"account_email", "email"}},
		{Label: "Username", Keys: []string{"username", "user"}},
		{Label: "Company ID", Keys: []string{"company", "company_id"}},
		{Label: "Provider status", Keys: []string{"status"}},
	} {
		if value := firstRecordString(record, field.Keys...); value != "" {
			fmt.Printf("%s: %s\n", field.Label, value)
		}
	}
	targets := targetMaps(record["targets"])
	fmt.Printf("Targets: %d\n", len(targets))
	for _, target := range targets {
		name := firstRecordString(target, "name", "label", "id")
		if name == "" {
			continue
		}
		status := firstRecordString(target, "status", "phase")
		if status != "" {
			fmt.Printf("  - %s (%s)\n", name, status)
			continue
		}
		fmt.Printf("  - %s\n", name)
	}
}

func providerRecordByName(records []map[string]any, name string) map[string]any {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, record := range records {
		for _, key := range []string{"provider", "_state_key", "name", "id"} {
			if strings.ToLower(strings.TrimSpace(recordValueString(record[key]))) == name {
				return record
			}
		}
	}
	return nil
}

func cmdProviderCheck(name string, jsonOutput bool) int {
	status, ok := providerConfigStatusByName(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "unsupported provider %q\n", name)
		return 1
	}
	if len(status.Missing) > 0 {
		fmt.Printf("Provider %s preflight failed.\n", status.Name)
		fmt.Printf("Missing: %s\n", providerMissingLabel(status))
		fmt.Printf("Set values in the environment or %s.\n", config.EnvFile())
		fmt.Println("No remote API call was made.")
		return 1
	}
	result, err := runProviderHealthcheck(status.Name)
	if err != nil {
		fmt.Printf("Provider %s healthcheck failed.\n", status.Name)
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if result.Record == nil {
		result.Record = map[string]any{}
	}
	if err := saveProviderHealthRecord(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if jsonOutput {
		data, _ := json.MarshalIndent(result.Record, "", "  ")
		fmt.Println(string(data))
		return 0
	}
	fmt.Printf("Provider %s healthcheck passed.\n", status.Name)
	for _, line := range providerHealthDetailLines(result.Details) {
		fmt.Println(line)
	}
	fmt.Printf("Saved provider metadata to %s.\n", state.StatePath("providers"))
	return 0
}

func runProviderHealthcheck(provider string) (providerHealthResult, error) {
	switch provider {
	case "dnsimple":
		return providerCheckDNSimpleFn()
	case "kinsta":
		return providerCheckKinstaFn()
	case "linode":
		return providerCheckLinodeFn()
	default:
		return providerHealthResult{}, fmt.Errorf("unsupported provider %q", provider)
	}
}

func providerHealthDetailLines(details map[string]string) []string {
	keys := make([]string, 0, len(details))
	for key := range details {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s: %s", key, details[key]))
	}
	return lines
}

func saveProviderHealthRecord(result providerHealthResult) error {
	record := result.Record
	if record == nil {
		record = map[string]any{}
	}
	record["provider"] = result.Provider
	record["checked_at"] = time.Now().UTC().Format(time.RFC3339)
	if _, ok := record["targets"]; !ok {
		record["targets"] = []map[string]any{}
	}
	records, err := state.LoadStateRecords("providers")
	if err != nil {
		return err
	}
	replaced := false
	for i, existing := range records {
		if strings.EqualFold(recordValueString(existing["provider"]), result.Provider) || strings.EqualFold(recordValueString(existing["_state_key"]), result.Provider) {
			records[i] = record
			replaced = true
			break
		}
	}
	if !replaced {
		records = append(records, record)
	}
	return state.SaveStateRecords("providers", records)
}

func providerHealthContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

func checkDNSimpleProvider() (providerHealthResult, error) {
	token := envwizard.Value("DNSIMPLE_TOKEN")
	if token == "" {
		return providerHealthResult{}, fmt.Errorf("Expected DNSIMPLE_TOKEN in the environment or %s.", config.EnvFile())
	}
	managedDomain := baseDomainValue()
	if managedDomain == "" {
		return providerHealthResult{}, fmt.Errorf("Expected base_domain in %s. Set it with nf config set-base-domain <domain>.", config.ConfigFile())
	}
	ctx, cancel := providerHealthContext()
	defer cancel()
	client := dnsimple.NewClient(dnsimple.StaticTokenHTTPClient(ctx, token))
	if baseURL := envwizard.Value("DNSIMPLE_BASE_URL"); baseURL != "" {
		client.BaseURL = baseURL
	}
	resp, err := client.Identity.Whoami(ctx)
	if err != nil {
		return providerHealthResult{}, err
	}
	if resp == nil || resp.Data == nil || resp.Data.Account == nil || resp.Data.Account.ID == 0 {
		return providerHealthResult{}, fmt.Errorf("DNSimple /v2/whoami did not return an account id")
	}
	accountID := strconv.FormatInt(resp.Data.Account.ID, 10)
	zoneResp, err := client.Zones.GetZone(ctx, accountID, managedDomain)
	if err != nil {
		var apiErr *dnsimple.ErrorResponse
		if errors.As(err, &apiErr) && apiErr.HTTPResponse != nil && apiErr.HTTPResponse.StatusCode == http.StatusNotFound {
			return providerHealthResult{}, fmt.Errorf("DNSimple zone %s was not found for account %s. Check base_domain and DNSIMPLE_TOKEN", managedDomain, accountID)
		}
		return providerHealthResult{}, fmt.Errorf("Checking DNSimple zone %s for account %s: %v", managedDomain, accountID, err)
	}
	if zoneResp == nil || zoneResp.Data == nil || strings.TrimSpace(zoneResp.Data.Name) == "" {
		return providerHealthResult{}, fmt.Errorf("DNSimple zone %s did not return zone metadata", managedDomain)
	}
	details := map[string]string{"account_id": accountID, "managed_domain": managedDomain}
	if resp.Data.Account.Email != "" {
		details["account_email"] = resp.Data.Account.Email
	}
	if resp.Data.Account.Name != "" {
		details["account_name"] = resp.Data.Account.Name
	}
	details["zone_id"] = strconv.FormatInt(zoneResp.Data.ID, 10)
	record := map[string]any{
		"provider":       "dnsimple",
		"account_id":     accountID,
		"managed_domain": managedDomain,
		"zone_id":        strconv.FormatInt(zoneResp.Data.ID, 10),
		"zone_active":    zoneResp.Data.Active,
		"targets":        []map[string]any{},
	}
	if resp.Data.Account.Email != "" {
		record["account_email"] = resp.Data.Account.Email
	}
	if resp.Data.Account.Name != "" {
		record["account_name"] = resp.Data.Account.Name
	}
	if values, err := loadGlobalConfig(); err == nil {
		values["dnsimple_account_id"] = accountID
		if err := saveGlobalConfig(values); err != nil {
			return providerHealthResult{}, err
		}
	} else {
		return providerHealthResult{}, err
	}
	return providerHealthResult{Provider: "dnsimple", Details: details, Record: record}, nil
}

func baseDomainValue() string {
	values, err := loadGlobalConfig()
	if err == nil {
		if value := strings.TrimSpace(values["base_domain"]); value != "" {
			return value
		}
	}
	return firstNonEmpty(envwizard.Value("NF_SERVER_DOMAIN"), envwizard.Value("DNSIMPLE_ZONE_NAME"))
}

func dnsimpleAccountIDValue() string {
	values, err := loadGlobalConfig()
	if err == nil {
		return strings.TrimSpace(values["dnsimple_account_id"])
	}
	return ""
}

type kinstaValidateResponse struct {
	Name      string  `json:"name"`
	ExpiresAt *string `json:"expires_at"`
	Company   string  `json:"company"`
	Status    string  `json:"status"`
}

func checkKinstaProvider() (providerHealthResult, error) {
	token := envwizard.Value("KINSTA_API_KEY")
	if token == "" {
		return providerHealthResult{}, fmt.Errorf("Expected KINSTA_API_KEY in the environment or %s.", config.EnvFile())
	}
	ctx, cancel := providerHealthContext()
	defer cancel()
	baseURL := firstNonEmpty(envwizard.Value("KINSTA_BASE_URL"), "https://api.kinsta.com/v2")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/validate", nil)
	if err != nil {
		return providerHealthResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return providerHealthResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return providerHealthResult{}, fmt.Errorf("Kinsta /v2/validate returned %s", resp.Status)
	}
	var payload kinstaValidateResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return providerHealthResult{}, err
	}
	company := strings.TrimSpace(payload.Company)
	if company == "" {
		return providerHealthResult{}, fmt.Errorf("Kinsta /v2/validate did not return a company uuid")
	}
	details := map[string]string{"company_id": company}
	if payload.Name != "" {
		details["api_key_name"] = payload.Name
	}
	if payload.Status != "" {
		details["status"] = payload.Status
	}
	if payload.ExpiresAt != nil && strings.TrimSpace(*payload.ExpiresAt) != "" {
		details["expires_at"] = strings.TrimSpace(*payload.ExpiresAt)
	}
	targetStatus := firstNonEmpty(strings.TrimSpace(payload.Status), "active")
	record := map[string]any{
		"provider":   "kinsta",
		"company_id": company,
		"targets": []map[string]any{{
			"id":         "kinsta",
			"name":       "kinsta",
			"provider":   "kinsta",
			"company_id": company,
			"status":     targetStatus,
		}},
	}
	if payload.Name != "" {
		record["api_key_name"] = payload.Name
	}
	if payload.Status != "" {
		record["status"] = payload.Status
	}
	if payload.ExpiresAt != nil && strings.TrimSpace(*payload.ExpiresAt) != "" {
		record["expires_at"] = strings.TrimSpace(*payload.ExpiresAt)
	}
	return providerHealthResult{Provider: "kinsta", Details: details, Record: record}, nil
}

func checkLinodeProvider() (providerHealthResult, error) {
	token, err := linodeTokenEnv()
	if err != nil {
		return providerHealthResult{}, err
	}
	ctx, cancel := providerHealthContext()
	defer cancel()
	client := linodego.NewClient(nil)
	client.SetToken(token)
	profile, err := client.GetProfile(ctx)
	if err != nil {
		return providerHealthResult{}, err
	}
	if profile == nil || strings.TrimSpace(profile.Username) == "" {
		return providerHealthResult{}, fmt.Errorf("Linode profile endpoint did not return a username")
	}
	details := map[string]string{"username": profile.Username}
	if profile.Email != "" {
		details["email"] = profile.Email
	}
	details["restricted"] = strconv.FormatBool(profile.Restricted)
	instances, err := client.ListInstances(ctx, nil)
	if err != nil {
		return providerHealthResult{}, err
	}
	targets := make([]map[string]any, 0)
	for _, instance := range instances {
		if !linodeInstanceHasTag(instance, "nf") {
			continue
		}
		targets = append(targets, linodeInstanceTargetRecord(instance))
	}
	details["targets"] = strconv.Itoa(len(targets))
	record := map[string]any{
		"provider":   "linode",
		"username":   profile.Username,
		"restricted": profile.Restricted,
		"targets":    targets,
	}
	if profile.Email != "" {
		record["email"] = profile.Email
	}
	return providerHealthResult{Provider: "linode", Details: details, Record: record}, nil
}

func linodeInstanceHasTag(instance linodego.Instance, tag string) bool {
	for _, candidate := range instance.Tags {
		if strings.EqualFold(strings.TrimSpace(candidate), tag) {
			return true
		}
	}
	return false
}

func linodeInstanceTargetRecord(instance linodego.Instance) map[string]any {
	record := map[string]any{
		"id":       strconv.Itoa(instance.ID),
		"name":     instance.Label,
		"provider": "linode",
		"region":   instance.Region,
		"status":   string(instance.Status),
		"tags":     instance.Tags,
	}
	if len(instance.IPv4) > 0 && instance.IPv4[0] != nil {
		record["ipv4"] = instance.IPv4[0].String()
	}
	if hostname := linodeInstanceHostname(instance.Label); hostname != "" {
		record["hostname"] = hostname
		ssh := map[string]any{"host": hostname}
		if user := linodeDefaultSSHUser(); user != "" {
			ssh["user"] = user
		}
		record["ssh"] = ssh
	}
	return record
}

func linodeInstanceHostname(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	if strings.Contains(label, ".") {
		return strings.TrimSuffix(label, ".")
	}
	domain := baseDomainValue()
	if domain == "" {
		return ""
	}
	return label + "." + domain
}

func linodeDefaultSSHUser() string {
	values, err := loadGlobalConfig()
	if err == nil {
		if value := strings.TrimSpace(values["linode_default_user"]); value != "" {
			return value
		}
	}
	return "nonfiction"
}

func runTarget(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runTargetHelp()
	}
	argv[0] = cliCommandAlias(argv[0])
	switch argv[0] {
	case "refresh":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "target refresh takes no arguments")
			return 1
		}
		return cmdTargetRefresh()
	case "list":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "target list takes no arguments")
			return 1
		}
		targets, err := cachedTargets()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return cmdListTargets(targets)
	case "show":
		needle, jsonOutput, err := parseTargetShowArgs(argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if needle == "" {
			selected, err := chooseTargetForShow()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			needle = selected
		}
		return cmdShowTarget(needle, jsonOutput)
	case "add":
		return runTargetAdd(argv[1:])
	case "remove":
		needle, opts, err := parseRemoveTargetArgs(argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if needle == "" {
			if opts.nonInteractive {
				fmt.Fprintln(os.Stderr, "target remove requires a target in non-interactive mode")
				return 1
			}
			selected, err := chooseTargetForRemove()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			needle = selected
		}
		return cmdRemoveTarget(needle, opts.dryRun, opts.execute, opts.yes, opts.nonInteractive)
	default:
		fmt.Fprintln(os.Stderr, "unsupported target command")
		return 1
	}
}

func runTargetAdd(argv []string) int {
	return target.RunAdd(argv, os.Stderr)
}

func runRemote(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runRemoteHelp()
	}
	argv[0] = cliCommandAlias(argv[0])
	if err := requireProjectContext("remote " + argv[0]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	switch argv[0] {
	case "add":
		if len(argv) > 3 {
			fmt.Fprintln(os.Stderr, "remote add takes at most name and env ref")
			return 1
		}
		name := ""
		envRef := ""
		if len(argv) >= 2 {
			name = argv[1]
		} else {
			prompted, err := remotePromptString("Remote name", "", false)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			name = strings.TrimSpace(prompted)
		}
		if len(argv) == 3 {
			envRef = argv[2]
		} else {
			selected, err := selectSiteEnv("Choose a remote env", "")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			envRef = selected
		}
		return cmdRemoteAdd(name, envRef)
	case "remove":
		if len(argv) > 2 {
			fmt.Fprintln(os.Stderr, "remote remove takes at most one name")
			return 1
		}
		name := ""
		if len(argv) == 2 {
			name = argv[1]
		} else {
			selected, err := chooseProjectRemote("remove")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			name = selected
		}
		return cmdRemoteRemove(name)
	case "show":
		if len(argv) > 2 {
			fmt.Fprintln(os.Stderr, "remote show takes at most one name")
			return 1
		}
		name := ""
		if len(argv) == 2 {
			name = argv[1]
		} else {
			selected, err := chooseProjectRemoteOrOnly("show")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			name = selected
		}
		return cmdRemoteShow(name)
	case "list":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "remote list takes no arguments")
			return 1
		}
		return cmdRemoteList()
	default:
		fmt.Fprintln(os.Stderr, "unsupported remote command")
		return 1
	}
}

func runServer(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" {
		return runServerHelp()
	}
	argv[0] = cliCommandAlias(argv[0])
	switch argv[0] {
	case "provision":
		return runProvision(argv[1:])
	case "list":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "server list takes no arguments")
			return 1
		}
		return cmdList("servers")
	case "show":
		if len(argv) > 2 {
			fmt.Fprintln(os.Stderr, "server show takes exactly one identifier")
			return 1
		}
		needle := ""
		if len(argv) == 2 {
			needle = argv[1]
		} else {
			selected, err := chooseRecord("server", "show")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			needle = selected
		}
		return cmdShowServer(needle)
	case "root-password":
		if len(argv) != 2 {
			fmt.Fprintln(os.Stderr, "server root-password takes exactly one identifier")
			return 1
		}
		return cmdServerRootPassword(argv[1])
	case "delete":
		needle, opts, err := parseDeleteServerArgs(argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if needle == "" {
			if opts.nonInteractive {
				fmt.Fprintln(os.Stderr, "server delete requires an id or name in non-interactive mode")
				return 1
			}
			selected, err := chooseServerForDelete()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			needle = selected
		}
		return cmdDeleteServer(needle, opts.dryRun, opts.execute, opts.yes, opts.nonInteractive)
	default:
		fmt.Fprintln(os.Stderr, "unsupported server command")
		return 1
	}
}

func runSite(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" {
		return runSiteHelp()
	}
	argv[0] = cliCommandAlias(argv[0])
	switch argv[0] {
	case "add":
		return runSiteAdd(argv[1:])
	case "refresh":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "site refresh takes no arguments")
			return 1
		}
		return cmdSiteRefresh()
	case "list":
		refresh := false
		listArgs := []string{}
		for _, arg := range argv[1:] {
			if arg == "--refresh" {
				refresh = true
				continue
			}
			listArgs = append(listArgs, arg)
		}
		listEnvs, siteID, err := parseSiteListArgs(listArgs)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if refresh {
			if code := cmdSiteRefresh(); code != 0 {
				return code
			}
		}
		if listEnvs {
			return cmdListSiteEnvs(siteID)
		}
		return cmdList("sites")
	case "show":
		needle, jsonOutput, err := parseSiteShowArgs(argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if needle == "" {
			selected, err := chooseSiteForShow()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			needle = selected
		}
		return cmdShowSiteRef(needle, jsonOutput)
	case "shell":
		envRef, ok := parseSiteShellArgs(argv[1:])
		if !ok {
			return 1
		}
		return cmdSiteRemoteCommandPlan("shell", envRef, nil)
	case "wp":
		envRef, command, ok := parseSiteWPArgs(argv[1:])
		if !ok {
			return 1
		}
		return cmdSiteRemoteCommandPlan("wp", envRef, command)
	case "snapshot":
		envRef, opts, err := parseSiteSnapshotArgs(argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return cmdSiteSnapshot(envRef, opts)
	case "password":
		if len(argv) > 2 {
			fmt.Fprintln(os.Stderr, "site password takes at most one site")
			return 1
		}
		needle := ""
		if len(argv) == 2 {
			needle = argv[1]
		}
		if needle == "" {
			selected, err := chooseSiteForPassword()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			needle = selected
		}
		if siteID, _, ok := splitSiteEnvRef(needle); ok {
			fmt.Fprintf(os.Stderr, "site password takes a site, not an env; use %q.\n", siteID)
			return 1
		}
		return cmdSitePassword(needle)
	case "remove":
		needle, opts, err := parseRemoveSiteArgs(argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if needle == "" {
			if opts.nonInteractive {
				fmt.Fprintln(os.Stderr, "site remove requires a site in non-interactive mode")
				return 1
			}
			selected, err := chooseSiteForRemove()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			needle = selected
		}
		if siteID, _, ok := splitSiteEnvRef(needle); ok {
			fmt.Fprintf(os.Stderr, "Cannot remove one env; remove site %q to delete live and staging.\n", siteID)
			return 1
		}
		return cmdSiteRemove(needle, opts.dryRun, opts.execute, opts.yes, opts.nonInteractive)
	default:
		fmt.Fprintln(os.Stderr, "unsupported site command")
		return 1
	}
}

func runSiteAdd(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		printGroupHelp("site add", []helpLine{
			{"<target> <site> [flags]", "create live and staging envs"},
			{"--region <region>", "Kinsta region override"},
			{"--php <version>", "Kinsta PHP version override"},
			{"--dry-run", "show the plan only"},
			{"--execute", "execute the plan"},
			{"--yes", "confirm execution"},
			{"--non-interactive", "fail instead of prompting"},
		})
		return 0
	}
	args := siteAddArgs{}
	positionals := []string{}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch arg {
		case "--execute":
			args.execute = true
		case "--yes":
			args.yes = true
		case "--non-interactive":
			args.nonInteractive = true
		case "--dry-run":
			args.dryRun = true
		case "--region":
			if i+1 >= len(argv) || strings.TrimSpace(argv[i+1]) == "" {
				fmt.Fprintln(os.Stderr, "--region requires a value")
				return 1
			}
			i++
			args.region = argv[i]
		case "--php":
			if i+1 >= len(argv) || strings.TrimSpace(argv[i+1]) == "" {
				fmt.Fprintln(os.Stderr, "--php requires a value")
				return 1
			}
			i++
			args.phpVersion = argv[i]
		default:
			if strings.HasPrefix(arg, "--region=") {
				args.region = strings.TrimPrefix(arg, "--region=")
				if strings.TrimSpace(args.region) == "" {
					fmt.Fprintln(os.Stderr, "--region requires a value")
					return 1
				}
				continue
			}
			if strings.HasPrefix(arg, "--php=") {
				args.phpVersion = strings.TrimPrefix(arg, "--php=")
				if strings.TrimSpace(args.phpVersion) == "" {
					fmt.Fprintln(os.Stderr, "--php requires a value")
					return 1
				}
				continue
			}
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(os.Stderr, "unknown site add flag: %s\n", arg)
				return 1
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 2 {
		fmt.Fprintln(os.Stderr, "site add takes exactly target and site")
		return 1
	}
	args.target = positionals[0]
	args.site = positionals[1]
	return cmdSiteAdd(args)
}

func runProvision(argv []string) int {
	fs := flag.NewFlagSet("server provision", flag.ContinueOnError)
	args := provision.Args{}
	fs.StringVar(&args.Provider, "provider", "", "server provider (linode)")
	fs.StringVar(&args.DnsProvider, "dns-provider", "", "DNS provider (dnsimple)")
	fs.StringVar(&args.UbuntuVersion, "ubuntu-version", "", "Ubuntu LTS version to use (26.04, 24.04, 22.04, 20.04)")
	fs.StringVar(&args.Firewall, "firewall", "", "Linode cloud firewall mode (managed or none)")
	fs.StringVar(&args.FirewallID, "firewall-id", "", "existing Linode cloud firewall id")
	fs.StringVar(&args.Name, "name", "", "server name")
	fs.StringVar(&args.Region, "region", "", "Linode region")
	fs.StringVar(&args.Type, "type", "", "Linode type")
	fs.StringVar(&args.Image, "image", "", "advanced Linode image override")
	fs.StringVar(&args.SshUser, "ssh-user", "", "deployment SSH user")
	fs.StringVar(&args.SshKeySource, "ssh-key-source", "", "SSH key source (linode-profile or file)")
	fs.StringVar(&args.SshKeyLabel, "ssh-key-label", "", "filter Linode profile SSH keys by label")
	fs.StringVar(&args.SshKeyID, "ssh-key-id", "", "filter Linode profile SSH keys by id")
	fs.BoolVar(&args.AllLinodeSshKeys, "all-linode-ssh-keys", false, "use all Linode profile SSH keys")
	fs.StringVar(&args.SshPublicKeyFile, "ssh-public-key-file", "", "SSH public key file fallback for --ssh-key-source file")
	fs.StringVar(&args.DnsimpleAccountID, "dnsimple-account-id", "", "DNSimple account ID")
	fs.BoolVar(&args.Wait, "wait", false, "wait for SSH, TLS, and health checks")
	fs.BoolVar(&args.NoWait, "no-wait", false, "skip SSH, TLS, and health checks")
	fs.DurationVar(&args.SshTimeout, "ssh-timeout", 5*time.Minute, "timeout for waiting on SSH port 22")
	fs.DurationVar(&args.CloudInitTimeout, "cloud-init-timeout", 10*time.Minute, "timeout for cloud-init and TLS setup")
	fs.DurationVar(&args.TLSTimeout, "tls-timeout", 5*time.Minute, "timeout budget for TLS setup")
	fs.DurationVar(&args.HealthTimeout, "health-timeout", 2*time.Minute, "timeout for HTTPS health checks")
	fs.StringVar(&args.WriteCloudInit, "write-cloud-init", "", "write cloud-init preview to a file")
	fs.BoolVar(&args.NonInteractive, "non-interactive", false, "")
	fs.BoolVar(&args.ShowCloudInit, "show-cloud-init", false, "show cloud-init preview in the terminal")
	fs.BoolVar(&args.Execute, "execute", false, "execute remote provisioning")
	fs.BoolVar(&args.Yes, "yes", false, "confirm execution in non-interactive mode")
	fs.BoolVar(&args.DryRun, "dry-run", false, "show the plan without executing")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(argv); err != nil {
		return 1
	}
	if args.Provider != "" && args.Provider != "linode" {
		fmt.Fprintln(os.Stderr, "Only --provider linode is supported in this slice.")
		return 1
	}
	if args.DnsProvider != "" && args.DnsProvider != "dnsimple" {
		fmt.Fprintln(os.Stderr, "Only --dns-provider dnsimple is supported in this slice.")
		return 1
	}
	if args.Execute && args.DryRun {
		fmt.Fprintln(os.Stderr, "Choose either --execute or --dry-run, not both.")
		return 1
	}
	if args.Wait && args.NoWait {
		fmt.Fprintln(os.Stderr, "Choose either --wait or --no-wait, not both.")
		return 1
	}
	if !args.Execute {
		args.DryRun = true
	}
	if args.Execute && !args.NoWait {
		args.Wait = true
	}
	if args.NonInteractive && args.Execute && !args.Yes {
		fmt.Fprintln(os.Stderr, "Remote execution requires both --execute and --yes in non-interactive mode.")
		return 1
	}
	plan, err := provision.BuildPlan(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_, err = provision.ProvisionServer(plan)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

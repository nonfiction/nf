package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/provision"
	"github.com/nonfiction/nf/internal/state"
	"github.com/nonfiction/nf/internal/theme"
	"github.com/nonfiction/nf/internal/ui"
)

type ProjectError struct{ Msg string }

func (e ProjectError) Error() string { return e.Msg }

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

type instanceConfig struct {
	ProjectSlug      string
	ProjectName      string
	RepoRoot         string
	ThemePath        string
	InstanceDir      string
	WordpressPort    int
	MailpitPort      int
	Compose          string
	WordpressService string
	CliService       string
	ThemeMountSlug   string
	UploadsPath      string
	ThemeSlug        string
}

type instanceSnapshotContents struct {
	Database       string   `json:"database"`
	WpContent      string   `json:"wp_content"`
	WpContentPaths []string `json:"wp_content_paths"`
}

type instanceSnapshotMetadata struct {
	Schema         int                      `json:"schema"`
	Name           string                   `json:"name"`
	ProjectSlug    string                   `json:"project_slug"`
	CreatedAt      string                   `json:"created_at"`
	InstancePath   string                   `json:"instance_path"`
	ComposeProject string                   `json:"compose_project"`
	WordpressURL   string                   `json:"wordpress_url"`
	Contents       instanceSnapshotContents `json:"contents"`
}

type instanceSnapshotRecord struct {
	Metadata         instanceSnapshotMetadata
	Directory        string
	DatabaseArchive  string
	WpContentArchive string
	DatabaseSize     int64
	WpContentSize    int64
	CreatedAt        time.Time
}

const instanceSnapshotSchema = 1

var (
	instanceSnapshotPromptString  = ui.PromptString
	instanceSnapshotConfirm       = ui.Confirm
	instanceSnapshotSelect        = ui.Select
	instanceSnapshotIsInteractive = instanceSnapshotInteractive
)

func defaultInstanceSnapshotName(now time.Time) string {
	return now.Format("2006-01-02-150405")
}

func defaultPreRestoreSnapshotName(now time.Time) string {
	return defaultInstanceSnapshotName(now) + "-pre-restore"
}

func instanceSnapshotProjectDir(cfg instanceConfig) string {
	return config.SnapshotProjectDir(cfg.ProjectSlug)
}

func instanceSnapshotDir(cfg instanceConfig, name string) string {
	return config.SnapshotDir(cfg.ProjectSlug, name)
}

func instanceSnapshotContainerDir(name string) string {
	return path.Join("/instance-snapshots", name)
}

func instanceSnapshotContainerDatabaseArchive(name string) string {
	return path.Join(instanceSnapshotContainerDir(name), "database.sql.gz")
}

func instanceSnapshotContainerWpContentArchive(name string) string {
	return path.Join(instanceSnapshotContainerDir(name), "wp-content.tar.gz")
}

func instanceSnapshotHostDatabaseArchive(cfg instanceConfig, name string) string {
	return filepath.Join(instanceSnapshotDir(cfg, name), "database.sql.gz")
}

func instanceSnapshotHostWpContentArchive(cfg instanceConfig, name string) string {
	return filepath.Join(instanceSnapshotDir(cfg, name), "wp-content.tar.gz")
}

func instanceSnapshotMetadataPath(cfg instanceConfig, name string) string {
	return filepath.Join(instanceSnapshotDir(cfg, name), "snapshot.json")
}

func instanceSnapshotComposeMount(cfg instanceConfig) string {
	return config.SnapshotProjectDir(cfg.ProjectSlug)
}

func instanceSnapshotContentPaths() []string {
	return []string{"wp-content/uploads", "wp-content/plugins", "wp-content/mu-plugins", "wp-content/languages"}
}

func newInstanceSnapshotMetadata(cfg instanceConfig, name string, createdAt time.Time) instanceSnapshotMetadata {
	return instanceSnapshotMetadata{
		Schema:         instanceSnapshotSchema,
		Name:           name,
		ProjectSlug:    cfg.ProjectSlug,
		CreatedAt:      createdAt.Format(time.RFC3339),
		InstancePath:   cfg.InstanceDir,
		ComposeProject: instanceComposeProjectName(cfg.ProjectSlug),
		WordpressURL:   instanceSnapshotWordPressURL(cfg),
		Contents: instanceSnapshotContents{
			Database:       "database.sql.gz",
			WpContent:      "wp-content.tar.gz",
			WpContentPaths: instanceSnapshotContentPaths(),
		},
	}
}

func instanceSnapshotWordPressURL(cfg instanceConfig) string {
	return fmt.Sprintf("http://localhost:%d", cfg.WordpressPort)
}

func instanceSnapshotNormalizedName(input string) (string, error) {
	name := strings.TrimSpace(input)
	if name == "" {
		return "", ProjectError{Msg: "instance snapshot name cannot be empty"}
	}
	name = strings.Join(strings.Fields(name), "-")
	if name == "" {
		return "", ProjectError{Msg: "instance snapshot name cannot be empty"}
	}
	if filepath.IsAbs(name) {
		return "", ProjectError{Msg: fmt.Sprintf("instance snapshot name %q must not be absolute", input)}
	}
	if strings.ContainsAny(name, "/\\") {
		return "", ProjectError{Msg: fmt.Sprintf("instance snapshot name %q must not contain path separators", input)}
	}
	if strings.Contains(name, "..") {
		return "", ProjectError{Msg: fmt.Sprintf("instance snapshot name %q must not contain path traversal", input)}
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return "", ProjectError{Msg: fmt.Sprintf("instance snapshot name %q contains unsafe characters", input)}
	}
	return name, nil
}

func instanceSnapshotExists(cfg instanceConfig, name string) bool {
	_, err := os.Stat(instanceSnapshotDir(cfg, name))
	return err == nil
}

func instanceSnapshotInteractive() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func instanceSnapshotMetadataJSON(meta instanceSnapshotMetadata) (string, error) {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return "", err
	}
	return string(append(data, '\n')), nil
}

func (c instanceConfig) managedUploadsDir() string {
	return filepath.Join(c.InstanceDir, firstNonEmpty(c.UploadsPath, "uploads"))
}

func (c instanceConfig) uploadsContainerPath() string {
	return path.Join("/", "instance", firstNonEmpty(c.UploadsPath, "uploads"))
}

func instancePortBlockStart(projectSlug string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(cleanInstanceSlug(projectSlug)))
	return 18000 + int(h.Sum32()%1000)*4
}

func instanceDerivedPorts(projectSlug string) (int, int) {
	base := instancePortBlockStart(projectSlug)
	return base, base + 1
}

func cleanInstanceSlug(projectSlug string) string {
	cleaned := strings.ToLower(strings.TrimSpace(projectSlug))
	var b strings.Builder
	b.Grow(len(cleaned) + len("nf__instance"))
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

type instanceCommandRunner struct {
	name string
	cfg  instanceConfig
}

func (c instanceCommandRunner) ensureUpInstalledActive(instanceDir string) error {
	if err := runCommandSpec(execSpec{Dir: instanceDir, Args: instanceComposeArgs(c.cfg, "up", "-d")}); err != nil {
		return err
	}
	if err := runCommandSpecQuiet(execSpec{Dir: instanceDir, Args: instanceWpProbeArgs(c.cfg, "core", "is-installed")}); err != nil {
		if err := runCommandSpec(execSpec{Dir: instanceDir, Args: instanceWpCoreInstallArgs(c.cfg)}); err != nil {
			return err
		}
		return nil
	}
	if err := runCommandSpecQuiet(execSpec{Dir: instanceDir, Args: instanceWpThemeIsActiveArgs(c.cfg, "")}); err != nil {
		return runCommandSpec(execSpec{Dir: instanceDir, Args: instanceWpThemeActivateArgs(c.cfg, "")})
	}
	return nil
}

func (c instanceCommandRunner) instanceReadyForSnapshot(instanceDir string) bool {
	if err := runCommandSpecQuiet(execSpec{Dir: instanceDir, Args: instanceWpProbeArgs(c.cfg, "core", "is-installed")}); err != nil {
		return false
	}
	if err := runCommandSpecQuiet(execSpec{Dir: instanceDir, Args: instanceWpThemeIsActiveArgs(c.cfg, "")}); err != nil {
		return false
	}
	return true
}

func (c instanceCommandRunner) Execute(root string, extraArgs []string) error {
	if err := ensureManagedInstance(c.cfg); err != nil {
		return err
	}
	instanceDir := c.cfg.InstanceDir
	switch c.name {
	case "up":
		return c.ensureUpInstalledActive(instanceDir)
	case "down":
		return runCommandSpec(execSpec{Dir: instanceDir, Args: instanceComposeArgs(c.cfg, "down")})
	case "logs":
		return runCommandSpec(execSpec{Dir: instanceDir, Args: instanceComposeArgs(c.cfg, "logs", "-f", c.cfg.WordpressService)})
	case "reset":
		if err := runCommandSpec(execSpec{Dir: instanceDir, Args: instanceComposeArgs(c.cfg, "down", "-v", "--remove-orphans")}); err != nil {
			return err
		}
		return c.ensureUpInstalledActive(instanceDir)
	case "shell":
		return runCommandSpec(execSpec{Dir: instanceDir, Args: instanceShellArgs(c.cfg)})
	case "wp":
		return runCommandSpec(execSpec{Dir: instanceDir, Args: instanceWpArgs(c.cfg, extraArgs...)})
	default:
		return fmt.Errorf("unsupported repo command type")
	}
}

func (c instanceCommandRunner) Render() string {
	switch c.name {
	case "up":
		return "docker compose up -d; install WordPress if missing and ensure the mounted theme is active"
	case "down":
		return "docker compose down"
	case "logs":
		return "docker compose logs -f " + c.cfg.WordpressService
	case "reset":
		return "docker compose down -v --remove-orphans; nuke instance data and recreate it with docker compose up -d, install WordPress if missing, and ensure the mounted theme is active"
	case "shell":
		return "docker compose exec " + firstNonEmpty(c.cfg.WordpressService, "wordpress") + " sh"
	case "wp":
		return "docker compose run --rm " + c.cfg.CliService + " wp ... --allow-root"
	default:
		return c.name
	}
}

func ensureInstanceReadyForSnapshot(cfg instanceConfig) error {
	if err := ensureManagedInstance(cfg); err != nil {
		return err
	}
	runner := instanceCommandRunner{name: "up", cfg: cfg}
	if runner.instanceReadyForSnapshot(cfg.InstanceDir) {
		return nil
	}
	return runner.ensureUpInstalledActive(cfg.InstanceDir)
}

func instanceSnapshotCreateScript(name string) string {
	containerDir := instanceSnapshotContainerDir(name)
	wpContentArchive := instanceSnapshotContainerWpContentArchive(name)
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

func instanceSnapshotRestoreScript(name string) string {
	databaseArchive := instanceSnapshotContainerDatabaseArchive(name)
	wpContentArchive := instanceSnapshotContainerWpContentArchive(name)
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

func instanceSnapshotComposeArgs(cfg instanceConfig, args ...string) []string {
	return append(instanceComposeArgs(cfg, "run", "--rm", firstNonEmpty(cfg.CliService, "cli"), "sh", "-lc"), args...)
}

func runCommandSpecNoPreview(spec execSpec) error {
	if len(spec.Args) == 0 {
		return fmt.Errorf("unsupported repo command type")
	}
	cmd := exec.Command(spec.Args[0], spec.Args[1:]...)
	cmd.Dir = spec.Dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func instanceSnapshotCreateArchives(cfg instanceConfig, name string) error {
	if err := os.MkdirAll(instanceSnapshotDir(cfg, name), 0o755); err != nil {
		return err
	}
	if err := os.Chmod(instanceSnapshotDir(cfg, name), 0o777); err != nil {
		return err
	}
	if err := runCommandSpecNoPreview(execSpec{Dir: cfg.InstanceDir, Args: instanceSnapshotComposeArgs(cfg, instanceSnapshotCreateScript(name))}); err != nil {
		return err
	}
	return nil
}

func instanceSnapshotRestoreArchives(cfg instanceConfig, name string) error {
	if err := runCommandSpecNoPreview(execSpec{Dir: cfg.InstanceDir, Args: instanceSnapshotComposeArgs(cfg, instanceSnapshotRestoreScript(name))}); err != nil {
		return err
	}
	return nil
}

func instanceSnapshotMetadataFromFile(path string) (instanceSnapshotMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return instanceSnapshotMetadata{}, err
	}
	var meta instanceSnapshotMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return instanceSnapshotMetadata{}, err
	}
	return meta, nil
}

func instanceSnapshotArchiveSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return info.Size()
}

func instanceSnapshotCreatedAt(meta instanceSnapshotMetadata) time.Time {
	if meta.CreatedAt == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339, meta.CreatedAt); err == nil {
		return parsed
	}
	return time.Time{}
}

func loadInstanceSnapshots(cfg instanceConfig) ([]instanceSnapshotRecord, error) {
	dir := instanceSnapshotProjectDir(cfg)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	records := make([]instanceSnapshotRecord, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		meta, err := instanceSnapshotMetadataFromFile(instanceSnapshotMetadataPath(cfg, name))
		if err != nil {
			continue
		}
		record := instanceSnapshotRecord{
			Metadata:         meta,
			Directory:        instanceSnapshotDir(cfg, name),
			DatabaseArchive:  instanceSnapshotHostDatabaseArchive(cfg, name),
			WpContentArchive: instanceSnapshotHostWpContentArchive(cfg, name),
			DatabaseSize:     instanceSnapshotArchiveSize(instanceSnapshotHostDatabaseArchive(cfg, name)),
			WpContentSize:    instanceSnapshotArchiveSize(instanceSnapshotHostWpContentArchive(cfg, name)),
			CreatedAt:        instanceSnapshotCreatedAt(meta),
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

func formatInstanceSnapshotTime(value string) string {
	if value == "" {
		return "-"
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC().Format("2006-01-02 15:04:05")
	}
	return value
}

func formatInstanceSnapshotSize(size int64) string {
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

func instanceSnapshotRows(records []instanceSnapshotRecord) [][]string {
	rows := [][]string{{"name", "created", "database", "wp-content", "path"}}
	for _, record := range records {
		rows = append(rows, []string{
			firstNonEmpty(record.Metadata.Name, filepath.Base(record.Directory)),
			formatInstanceSnapshotTime(record.Metadata.CreatedAt),
			formatInstanceSnapshotSize(record.DatabaseSize),
			formatInstanceSnapshotSize(record.WpContentSize),
			record.Directory,
		})
	}
	return rows
}

func chooseInstanceSnapshot(records []instanceSnapshotRecord, action string) (instanceSnapshotRecord, error) {
	options := make([]ui.SelectOption, 0, len(records))
	for _, record := range records {
		name := firstNonEmpty(record.Metadata.Name, filepath.Base(record.Directory))
		if name == "" {
			continue
		}
		label := fmt.Sprintf("%s / %s / %s / %s", name, formatInstanceSnapshotTime(record.Metadata.CreatedAt), formatInstanceSnapshotSize(record.DatabaseSize), formatInstanceSnapshotSize(record.WpContentSize))
		options = append(options, ui.SelectOption{Label: label, Value: name})
	}
	if len(options) == 0 {
		return instanceSnapshotRecord{}, fmt.Errorf("No instance snapshots found.")
	}
	selected, err := instanceSnapshotSelect(fmt.Sprintf("Choose an instance snapshot to %s", action), options)
	if err != nil {
		return instanceSnapshotRecord{}, err
	}
	for _, record := range records {
		if firstNonEmpty(record.Metadata.Name, filepath.Base(record.Directory)) == selected {
			return record, nil
		}
	}
	return instanceSnapshotRecord{}, fmt.Errorf("instance snapshot %q was not found", selected)
}

func cmdInstanceSnapshotCreate(cfg instanceConfig, name string, nonInteractive bool) int {
	if strings.TrimSpace(name) == "" {
		if nonInteractive || !instanceSnapshotIsInteractive() {
			fmt.Fprintln(os.Stderr, "instance snapshot create requires a name when stdin is not interactive")
			return 1
		}
		defaultName := defaultInstanceSnapshotName(time.Now())
		prompted, err := instanceSnapshotPromptString("Snapshot name", defaultName, false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		name = prompted
	}
	normalized, err := instanceSnapshotNormalizedName(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if instanceSnapshotExists(cfg, normalized) {
		fmt.Fprintf(os.Stderr, "instance snapshot %q already exists.\n", normalized)
		return 1
	}
	if err := ensureInstanceReadyForSnapshot(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := instanceSnapshotCreateArchives(cfg, normalized); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	meta := newInstanceSnapshotMetadata(cfg, normalized, time.Now())
	jsonText, err := instanceSnapshotMetadataJSON(meta)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(instanceSnapshotMetadataPath(cfg, normalized), []byte(jsonText), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Snapshot created.\n\nSnapshot:\n  project: %s\n  name: %s\n  path: %s\n  database: database.sql.gz\n  wp-content: wp-content.tar.gz\n", cfg.ProjectSlug, normalized, instanceSnapshotDir(cfg, normalized))
	return 0
}

func cmdInstanceSnapshotList(cfg instanceConfig) int {
	records, err := loadInstanceSnapshots(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(records) == 0 {
		fmt.Println("No instance snapshots found.")
		return 0
	}
	fmt.Println(formatTable(instanceSnapshotRows(records)))
	return 0
}

func cmdInstanceSnapshotDelete(cfg instanceConfig, name string, nonInteractive bool) int {
	records, err := loadInstanceSnapshots(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	selectedName := strings.TrimSpace(name)
	if selectedName == "" {
		if nonInteractive || !instanceSnapshotIsInteractive() {
			fmt.Fprintln(os.Stderr, "instance snapshot delete requires a name when stdin is not interactive")
			return 1
		}
		record, err := chooseInstanceSnapshot(records, "delete")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		selectedName = firstNonEmpty(record.Metadata.Name, filepath.Base(record.Directory))
	}
	normalized, err := instanceSnapshotNormalizedName(selectedName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	path := instanceSnapshotDir(cfg, normalized)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "No instance snapshot matched %q.\n", normalized)
			return 1
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if nonInteractive || !instanceSnapshotIsInteractive() {
		fmt.Fprintln(os.Stderr, "instance snapshot delete requires an interactive terminal for confirmation")
		return 1
	}
	confirmed, err := instanceSnapshotConfirm(fmt.Sprintf("Delete instance snapshot %q? This removes %s.", normalized, path), false)
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
	fmt.Printf("Deleted instance snapshot.\n\nDeleted:\n  name: %s\n  path: %s\n", normalized, path)
	return 0
}

func cmdInstanceSnapshotRestore(cfg instanceConfig, name string, nonInteractive bool) int {
	records, err := loadInstanceSnapshots(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	selectedName := strings.TrimSpace(name)
	if selectedName == "" {
		if nonInteractive || !instanceSnapshotIsInteractive() {
			fmt.Fprintln(os.Stderr, "instance snapshot restore requires a name when stdin is not interactive")
			return 1
		}
		record, err := chooseInstanceSnapshot(records, "restore")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		selectedName = firstNonEmpty(record.Metadata.Name, filepath.Base(record.Directory))
	}
	normalized, err := instanceSnapshotNormalizedName(selectedName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	path := instanceSnapshotDir(cfg, normalized)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "No instance snapshot matched %q.\n", normalized)
			return 1
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if nonInteractive || !instanceSnapshotIsInteractive() {
		fmt.Fprintln(os.Stderr, "instance snapshot restore requires an interactive terminal for confirmation")
		return 1
	}
	confirmed, err := instanceSnapshotConfirm(fmt.Sprintf("Restore instance snapshot %q? This will overwrite the current instance database and mutable wp-content.", normalized), false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !confirmed {
		fmt.Fprintln(os.Stderr, "Aborted.")
		return 1
	}
	if err := ensureInstanceReadyForSnapshot(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	safetyName := defaultPreRestoreSnapshotName(time.Now())
	if instanceSnapshotExists(cfg, safetyName) {
		fmt.Fprintf(os.Stderr, "instance snapshot %q already exists.\n", safetyName)
		return 1
	}
	if err := instanceSnapshotCreateArchives(cfg, safetyName); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	safetyMeta := newInstanceSnapshotMetadata(cfg, safetyName, time.Now())
	jsonText, err := instanceSnapshotMetadataJSON(safetyMeta)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(instanceSnapshotMetadataPath(cfg, safetyName), []byte(jsonText), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := instanceSnapshotRestoreArchives(cfg, normalized); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Snapshot restored.\n\nRestored:\n  project: %s\n  name: %s\n\nSafety snapshot:\n  name: %s\n  path: %s\n", cfg.ProjectSlug, normalized, safetyName, instanceSnapshotDir(cfg, safetyName))
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

func instanceCommandDir(cfg instanceConfig) string {
	return cfg.InstanceDir
}

func ensureManagedInstance(cfg instanceConfig) error {
	if strings.TrimSpace(cfg.InstanceDir) == "" {
		return fmt.Errorf("missing managed instance directory")
	}
	if err := os.MkdirAll(cfg.InstanceDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(instanceSnapshotProjectDir(cfg), 0o755); err != nil {
		return err
	}
	if err := os.Chmod(instanceSnapshotProjectDir(cfg), 0o777); err != nil {
		return err
	}
	files := map[string]string{
		filepath.Join(cfg.InstanceDir, "docker-compose.yml"):                                  renderInstanceCompose(cfg),
		filepath.Join(cfg.InstanceDir, ".env"):                                                renderInstanceEnv(cfg),
		filepath.Join(cfg.InstanceDir, "php", "uploads.ini"):                                  renderInstanceUploadsINI(),
		filepath.Join(cfg.InstanceDir, "wordpress", "Dockerfile"):                             renderInstanceDockerfile(),
		filepath.Join(cfg.InstanceDir, "wordpress", "wordpress-rewrites.conf"):                renderInstanceRewritesConf(),
		filepath.Join(cfg.InstanceDir, firstNonEmpty(cfg.UploadsPath, "uploads"), ".gitkeep"): "",
	}
	for path, contents := range files {
		if err := writeManagedFile(path, contents, 0o644); err != nil {
			return err
		}
	}
	return nil
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

func instancePortInUse(port int) bool {
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

func instancePortsInUse(cfg instanceConfig) []int {
	occupied := make([]int, 0, 2)
	for _, port := range []int{cfg.WordpressPort, cfg.MailpitPort} {
		if instancePortInUse(port) {
			occupied = append(occupied, port)
		}
	}
	return occupied
}

func instancePortCollisionMessage(cfg instanceConfig, occupied []int) string {
	if len(occupied) == 0 {
		return ""
	}
	ports := append([]int(nil), occupied...)
	sort.Ints(ports)
	projectLabel := firstNonEmpty(cfg.ProjectSlug, "project")
	block := fmt.Sprintf("The %s instance wants:\n  WordPress: http://localhost:%d\n  Mailpit:   http://localhost:%d\n\nSet instance.ports.wordpress and instance.ports.mailpit in .nf/project.json to override.", projectLabel, cfg.WordpressPort, cfg.MailpitPort)
	if len(ports) == 1 {
		return fmt.Sprintf("Port %d is already in use.\n\n%s", ports[0], block)
	}
	parts := make([]string, 0, len(ports))
	for _, port := range ports {
		parts = append(parts, strconv.Itoa(port))
	}
	return fmt.Sprintf("Ports %s are already in use.\n\n%s", strings.Join(parts, " and "), block)
}

func preflightInstancePorts(cfg instanceConfig) error {
	if occupied := instancePortsInUse(cfg); len(occupied) > 0 {
		return fmt.Errorf("%s", instancePortCollisionMessage(cfg, occupied))
	}
	return nil
}

func instanceComposeArgs(cfg instanceConfig, args ...string) []string {
	fields := strings.Fields(firstNonEmpty(cfg.Compose, "docker compose"))
	return append(fields, args...)
}

func instanceCliArgs(cfg instanceConfig, args ...string) []string {
	return append(instanceComposeArgs(cfg, "run", "--rm", firstNonEmpty(cfg.CliService, "cli")), args...)
}

func instanceWpArgs(cfg instanceConfig, args ...string) []string {
	return append(instanceCliArgs(cfg, "wp"), append(args, "--allow-root")...)
}

func instanceShellArgs(cfg instanceConfig) []string {
	return instanceComposeArgs(cfg, "exec", firstNonEmpty(cfg.WordpressService, "wordpress"), "sh")
}

func instanceWpProbeArgs(cfg instanceConfig, args ...string) []string {
	return instanceWpArgs(cfg, args...)
}

func instanceWpThemeIsActiveArgs(cfg instanceConfig, slug string) []string {
	return instanceWpArgs(cfg, "theme", "is-active", firstNonEmpty(slug, cfg.ThemeMountSlug, cfg.ThemeSlug, "theme"))
}

func instanceWpCoreInstallArgs(cfg instanceConfig) []string {
	slug := firstNonEmpty(cfg.ThemeMountSlug, cfg.ThemeSlug, "theme")
	return append(instanceComposeArgs(cfg, "run", "--rm", firstNonEmpty(cfg.CliService, "cli"), "sh", "-lc"), `wp core install --url="$WP_URL" --title="$WP_TITLE" --admin_user="$ADMIN_USER" --admin_password="$ADMIN_PASSWORD" --admin_email="$ADMIN_EMAIL" --skip-email --allow-root && wp theme activate `+slug+` --allow-root`)
}

func instanceWpThemeActivateArgs(cfg instanceConfig, slug string) []string {
	return instanceWpArgs(cfg, "theme", "activate", firstNonEmpty(slug, cfg.ThemeMountSlug, cfg.ThemeSlug, "theme"))
}

func instanceThemeArchivePaths(cfg instanceConfig, sourcePath string) (string, string) {
	base := filepath.Base(sourcePath)
	host := filepath.Join(instanceCommandDir(cfg), firstNonEmpty(cfg.UploadsPath, "uploads"), base)
	container := path.Join("/", "instance", firstNonEmpty(cfg.UploadsPath, "uploads"), base)
	return host, container
}

func instanceRepoPath(root, sourcePath string) string {
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
		{Keys: []string{"NF_SERVER_DOMAIN"}, Prompt: "NF_SERVER_DOMAIN (server domain): ", Default: "nfweb.dev", WriteKey: "NF_SERVER_DOMAIN"},
		{Keys: []string{"DNSIMPLE_ACCOUNT_ID"}, Prompt: "DNSIMPLE_ACCOUNT_ID (DNSimple account id): ", Default: "14", WriteKey: "DNSIMPLE_ACCOUNT_ID"},
		{Keys: []string{"DNSIMPLE_TOKEN"}, Prompt: "DNSimple token: ", Secret: true, WriteKey: "DNSIMPLE_TOKEN", Required: true},
		{Keys: []string{"LINODE_TOKEN", "LINODE_CLI_TOKEN"}, Prompt: "LINODE_TOKEN (Linode API token): ", Secret: true, WriteKey: "LINODE_TOKEN", Required: true},
		{Keys: []string{"NF_SECRET_SALT"}, Prompt: "NF_SECRET_SALT (used for derived passwords): ", Secret: true, WriteKey: "NF_SECRET_SALT", Required: true},
	}
}

func passwordRequirements() []envwizard.Requirement {
	return []envwizard.Requirement{
		{Keys: []string{"NF_SECRET_SALT"}, Prompt: "NF_SECRET_SALT (used for derived passwords): ", Secret: true, WriteKey: "NF_SECRET_SALT", Required: true},
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
	_, ok := currentGitRoot()
	return ok
}

func requireProjectContext(command string) error {
	if _, ok := currentGitRoot(); !ok {
		return ProjectError{Msg: fmt.Sprintf("%s requires a .git repository above the current directory", command)}
	}
	return nil
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

func loadInstanceConfig(root string, metadata map[string]any) (instanceConfig, bool) {
	raw, ok := metadata["instance"].(map[string]any)
	if !ok || raw == nil {
		return instanceConfig{}, false
	}
	projectSlug := firstNonEmpty(mapStringAtPath(metadata, "project", "slug"), "project")
	projectName := firstNonEmpty(mapStringAtPath(metadata, "project", "name"), slugToTitle(projectSlug))
	themePath := firstNonEmpty(mapStringAtPath(metadata, "wordpress", "theme_path"), "theme")
	if !filepath.IsAbs(themePath) {
		themePath = filepath.Join(root, themePath)
	}
	wordpress := mapMapAtPath(metadata, "wordpress")
	return instanceConfig{
		ProjectSlug:      projectSlug,
		ProjectName:      projectName,
		RepoRoot:         root,
		ThemePath:        themePath,
		InstanceDir:      config.InstanceDir(projectSlug),
		WordpressPort:    firstInstancePort(raw, "wordpress", projectSlug),
		MailpitPort:      firstInstancePort(raw, "mailpit", projectSlug),
		Compose:          firstNonEmpty(mapStringAtPath(raw, "compose"), "docker compose"),
		WordpressService: firstNonEmpty(mapStringAtPath(raw, "wordpress_service"), "wordpress"),
		CliService:       firstNonEmpty(mapStringAtPath(raw, "cli_service"), "cli"),
		ThemeMountSlug:   firstNonEmpty(mapStringAtPath(raw, "theme_mount_slug"), "theme"),
		UploadsPath:      firstNonEmpty(mapStringAtPath(raw, "uploads_path"), "uploads"),
		ThemeSlug:        firstNonEmpty(recordValueString(wordpress["theme_slug"]), "theme"),
	}, true
}

func firstInstancePort(raw map[string]any, name, projectSlug string) int {
	derivedWordpress, derivedMailpit := instanceDerivedPorts(projectSlug)
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
	port, parsed := parseInstancePort(value)
	if !parsed || port == 0 {
		return derived
	}
	return port
}

func parseInstancePort(value any) (int, bool) {
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

func defaultInstanceCommands(cfg instanceConfig) map[string]projectCommand {
	return map[string]projectCommand{
		"up":    {Description: "Start the managed instance, install WordPress if missing, and ensure the mounted theme is active", Run: instanceCommandRunner{name: "up", cfg: cfg}},
		"down":  {Description: "Stop the managed instance", Run: instanceCommandRunner{name: "down", cfg: cfg}},
		"logs":  {Description: "Tail WordPress logs", Run: instanceCommandRunner{name: "logs", cfg: cfg}},
		"reset": {Description: "Destroy and recreate the local instance", Run: instanceCommandRunner{name: "reset", cfg: cfg}},
		"shell": {Description: "Open a shell in the WordPress container", Run: instanceCommandRunner{name: "shell", cfg: cfg}},
		"wp":    {Description: "Run wp-cli passthrough", Run: instanceCommandRunner{name: "wp", cfg: cfg}},
	}
}

func discoverProjectRootOrError() (string, error) {
	if root, ok := config.DiscoverProjectRoot(""); ok {
		return root, nil
	}
	return "", ProjectError{Msg: "No project metadata found above the current directory. Add .nf/project.json with instance metadata or tasks.<name>."}
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
	if err := runLinodeDelete(remoteID); err != nil {
		if isLinodeNotFoundError(err) {
			fmt.Fprintln(os.Stderr, "Remote Linode was not found; removing stale local state.")
		} else {
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
	return firstRecordString(server, "ssh_host")
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

func siteServerReference(site map[string]any) string {
	return firstRecordString(site, "server", "server_id", "server_name", "server_hostname", "server_label")
}

func siteServerSummary(site, server map[string]any) string {
	if server != nil {
		return serverSummary(server)
	}
	return siteServerReference(site)
}

func siteKinstaID(site map[string]any, key string) string {
	if value := mapStringAtPath(site, "kinsta", key); value != "" {
		return value
	}
	return firstRecordString(site, "kinsta_"+key, key)
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
	return resolved, metadata, projectFileExists, false, nil
}

func validateServerRecord(server map[string]any) error {
	if strings.TrimSpace(recordValueString(server["provider"])) == "" {
		return ProjectError{Msg: fmt.Sprintf("Server %q is missing provider.", serverSummary(server))}
	}
	return nil
}

func validateSiteRecord(site map[string]any) error {
	provider := strings.ToLower(strings.TrimSpace(recordValueString(site["provider"])))
	if provider == "" {
		return ProjectError{Msg: fmt.Sprintf("Site %q is missing provider.", siteSummary(site))}
	}
	if provider == "linode" && siteServerReference(site) == "" {
		return ProjectError{Msg: fmt.Sprintf("Linode site %q is missing a server reference.", siteSummary(site))}
	}
	return nil
}

func cmdListServers(records []map[string]any) int {
	if len(records) == 0 {
		fmt.Println("No servers found.")
		return 0
	}
	rows := [][]string{{"target", "provider", "hostname", "ssh host", "status"}}
	for _, record := range records {
		rows = append(rows, []string{
			firstRecordString(record, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id"),
			recordValueString(record["provider"]),
			firstRecordString(record, "hostname", "host", "public_ipv4", "ip"),
			serverSSHHost(record),
			recordValueString(record["status"]),
		})
	}
	fmt.Println(formatTable(rows))
	return 0
}

func cmdListSites(records, servers []map[string]any) int {
	if len(records) == 0 {
		fmt.Println("No sites found.")
		return 0
	}
	rows := [][]string{{"target", "provider", "environment", "url", "branch", "server"}}
	for _, record := range records {
		rows = append(rows, []string{
			siteTargetName(record),
			recordValueString(record["provider"]),
			firstRecordString(record, "environment", "environment_name", "env"),
			firstRecordString(record, "url", "site_url", "home_url", "hostname"),
			firstRecordString(record, "branch", "git_branch"),
			siteServerSummary(record, state.MatchingRecord(servers, siteServerReference(record))),
		})
	}
	fmt.Println(formatTable(rows))
	return 0
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

func cmdShowSite(needle string) int {
	resolved, _, projectFileExists, targetAliasUsed, err := resolveSiteTarget(needle)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	bundle, err := state.LoadStateBundle()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	record := state.MatchingRecord(bundle.Sites, resolved)
	if record == nil {
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
	if err := validateSiteRecord(record); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(record["provider"])))
	out := cloneRecord(record)
	out["requested_target"] = needle
	out["resolved_target"] = resolved
	if provider == "linode" {
		serverRef := siteServerReference(record)
		server := state.MatchingRecord(bundle.Servers, serverRef)
		if server == nil {
			fmt.Fprintf(os.Stderr, "Linode site %q references server %q, but no server matched that target.\n", siteSummary(record), serverRef)
			return 1
		}
		if err := validateServerRecord(server); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		out["resolved_server_summary"] = serverSummary(server)
		out["resolved_server"] = server
	}
	if provider == "kinsta" {
		if value := siteKinstaID(record, "company_id"); value != "" {
			out["kinsta_company_id"] = value
		}
		if value := siteKinstaID(record, "site_id"); value != "" {
			out["kinsta_site_id"] = value
		}
		if value := siteKinstaID(record, "environment_id"); value != "" {
			out["kinsta_environment_id"] = value
		}
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(data))
	return 0
}

func cmdProjectInit(args projectInitArgs) int {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := writeProjectInit(root, args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
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

func ensureInstanceProjectMetadata() error {
	root, ok := currentGitRoot()
	if !ok {
		return ProjectError{Msg: "instance up requires a .git repository above the current directory"}
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
		"instance": map[string]any{
			"compose":           "docker compose",
			"wordpress_service": "wordpress",
			"cli_service":       "cli",
			"theme_mount_slug":  "theme",
			"uploads_path":      "uploads",
		},
		"build": map[string]any{
			"steps": []any{"composer install", "npm run build"},
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

func renderInstanceCompose(cfg instanceConfig) string {
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
      - %s:/instance-snapshots

  mailpit:
    image: axllent/mailpit
    ports:
      - "${MAILPIT_PORT}:8025"

volumes:
  db_data:
  wp_data:
`, wordpressService, themePath, themeMountSlug, cliService, wordpressService, themePath, themeMountSlug, uploadsPath, path.Join("/", "instance", uploadsPath), instanceSnapshotComposeMount(cfg))
}

func renderInstanceEnv(cfg instanceConfig) string {
	wpTitle := firstNonEmpty(cfg.ProjectName, slugToTitle(cfg.ProjectSlug))
	return fmt.Sprintf(`COMPOSE_PROJECT_NAME=%s
WP_PORT=%d
MAILPIT_PORT=%d
DB_NAME=%s
DB_USER=%s
DB_PASSWORD=wordpress
DB_ROOT_PASSWORD=root
WP_URL=http://localhost:%d
WP_TITLE=%s
ADMIN_USER=admin
ADMIN_PASSWORD=admin
ADMIN_EMAIL=web@nonfiction.ca
`, instanceComposeProjectName(cfg.ProjectSlug), cfg.WordpressPort, cfg.MailpitPort, cfg.ProjectSlug, cfg.ProjectSlug, cfg.WordpressPort, wpTitle)
}

func instanceComposeProjectName(projectSlug string) string {
	return "nf_" + cleanInstanceSlug(projectSlug) + "_instance"
}

func renderInstanceInfo(cfg instanceConfig, includeURLs bool) string {
	lines := []string{
		"Instance:",
		"  project: " + cfg.ProjectSlug,
		"  path: " + cfg.InstanceDir,
		"  compose project: " + instanceComposeProjectName(cfg.ProjectSlug),
	}
	if includeURLs {
		lines = append(lines,
			fmt.Sprintf("  WordPress: http://localhost:%d", cfg.WordpressPort),
			fmt.Sprintf("  Mailpit:   http://localhost:%d", cfg.MailpitPort),
		)
	}
	return strings.Join(lines, "\n")
}

func renderInstanceUploadsINI() string {
	return "file_uploads=On\nmemory_limit=256M\nupload_max_filesize=128M\npost_max_size=128M\nmax_execution_time=120\nmax_input_time=120\n"
}

func renderInstanceDockerfile() string {
	return `FROM wordpress:7.0-php8.4-apache

RUN a2enmod rewrite \
  && sed -ri 's/AllowOverride None/AllowOverride All/g' /etc/apache2/apache2.conf

COPY wordpress/wordpress-rewrites.conf /etc/apache2/conf-enabled/wordpress-rewrites.conf
`
}

func renderInstanceRewritesConf() string {
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

func printGroupHelp(title string, lines []string) {
	fmt.Println(title)
	fmt.Println("\nCommands:")
	for _, line := range lines {
		fmt.Printf("  %s\n", line)
	}
}

func runServerHelp() int {
	printGroupHelp("server", []string{
		"provision [flags]   provision an infrastructure host",
		"list                list servers",
		"show <id-or-name>   show a server",
		"root-password <id-or-name>   derive the Linode root password for a server",
		"delete [flags] <id-or-name>   delete a server (flags may also follow the id)",
	})
	return 0
}

func runSiteHelp() int {
	printGroupHelp("site", []string{
		"list                list sites",
		"show <id-or-name>   show a site",
		"install             not implemented yet",
		"delete              not implemented yet",
		"deploy              not implemented yet",
		"push                not implemented yet",
		"pull                not implemented yet",
	})
	return 0
}

func runConfigHelp() int {
	printGroupHelp("config", []string{
		"init                initialize local config",
	})
	return 0
}

func runPasswordHelp() int {
	printGroupHelp("password", []string{
		"derive [--non-interactive] <project-slug> <purpose>   derive a password",
	})
	return 0
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

func runInstanceHelp() int {
	printGroupHelp("instance", []string{
		"up                  start the local instance",
		"down                stop the local instance",
		"shell               open a shell in the local instance",
		"logs                tail WordPress logs",
		"reset               destroy and recreate the local instance",
		"wp -- <args>        run wp-cli in the local instance",
		"info                show local instance paths, ports, and URLs",
		"snapshot            manage/list instance snapshots",
	})
	fmt.Println("\nShortcuts:")
	for _, line := range []string{
		"nf up               shortcut for nf instance up",
		"nf down             shortcut for nf instance down",
		"nf logs             shortcut for nf instance logs",
		"nf reset            shortcut for nf instance reset",
		"nf shell            shortcut for nf instance shell",
		"nf wp -- <args>     shortcut for nf instance wp -- <args>",
		"nf info             shortcut for nf instance info",
	} {
		fmt.Printf("  %s\n", line)
	}
	return 0
}

func runThemeHelp() int {
	lines := []string{
		"tasks               list configured theme tasks",
		"package [--dry-run] [--source] [--output]   package theme artifacts",
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

func runInstance(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" {
		return runInstanceHelp()
	}
	name := argv[0]
	switch name {
	case "info", "up", "down", "logs", "reset", "shell", "wp", "snapshot", "snapshots":
	default:
		fmt.Fprintln(os.Stderr, "unsupported instance command")
		return 1
	}
	if name == "snapshots" {
		return runInstanceSnapshot([]string{"list"})
	}
	if name == "snapshot" {
		return runInstanceSnapshot(argv[1:])
	}
	if name == "info" && len(argv) != 1 {
		fmt.Fprintln(os.Stderr, "instance info takes no arguments")
		return 1
	}
	if name == "shell" && len(argv) != 1 {
		fmt.Fprintln(os.Stderr, "instance shell takes no arguments")
		return 1
	}
	if err := requireProjectContext("instance " + name); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if name == "up" {
		if err := ensureInstanceProjectMetadata(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
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
	cfg, ok := loadInstanceConfig(root, metadata)
	if !ok {
		fmt.Fprintln(os.Stderr, "Missing instance metadata in .nf/project.json. Run nf instance up first.")
		return 1
	}
	if name == "info" {
		fmt.Println(renderInstanceInfo(cfg, true))
		return 0
	}
	if name == "up" {
		if err := preflightInstancePorts(cfg); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	extraArgs := argv[1:]
	if name == "wp" {
		extraArgs = normalizePassthroughArgs(extraArgs)
	}
	if err := (instanceCommandRunner{name: name, cfg: cfg}).Execute(root, extraArgs); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	switch name {
	case "up":
		fmt.Println("Instance started.")
		fmt.Println()
		fmt.Println(renderInstanceInfo(cfg, true))
	case "reset":
		fmt.Println("Instance reset.")
		fmt.Println()
		fmt.Println(renderInstanceInfo(cfg, true))
	case "down":
		fmt.Println("Instance stopped.")
		fmt.Println()
		fmt.Println(renderInstanceInfo(cfg, false))
	}
	return 0
}

func runInstanceSnapshot(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" {
		printGroupHelp("instance snapshot", []string{
			"create [name]       create an instance snapshot",
			"list                list instance snapshots",
			"ls                  alias for list",
			"restore [name]      restore an instance snapshot",
			"delete [name]       delete an instance snapshot",
		})
		return 0
	}
	cmd := argv[0]
	args := argv[1:]
	switch cmd {
	case "list", "ls":
		if len(args) != 0 {
			fmt.Fprintln(os.Stderr, "instance snapshot list takes no arguments")
			return 1
		}
	case "create", "restore", "delete":
		if len(args) > 1 {
			fmt.Fprintln(os.Stderr, "instance snapshot command takes at most one name")
			return 1
		}
	default:
		fmt.Fprintln(os.Stderr, "unsupported instance snapshot command")
		return 1
	}
	if err := requireProjectContext("instance snapshot " + cmd); err != nil {
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
	cfg, ok := loadInstanceConfig(root, metadata)
	if !ok {
		fmt.Fprintln(os.Stderr, "Missing instance metadata in .nf/project.json. Run nf instance up first.")
		return 1
	}
	switch cmd {
	case "list", "ls":
		return cmdInstanceSnapshotList(cfg)
	case "create":
		name := ""
		if len(args) == 1 {
			name = args[0]
		}
		return cmdInstanceSnapshotCreate(cfg, name, false)
	case "restore":
		name := ""
		if len(args) == 1 {
			name = args[0]
		}
		return cmdInstanceSnapshotRestore(cfg, name, false)
	case "delete":
		name := ""
		if len(args) == 1 {
			name = args[0]
		}
		return cmdInstanceSnapshotDelete(cfg, name, false)
	default:
		return 1
	}
}

func runHelp() int {
	fmt.Println("nf")
	fmt.Println("\nCommands:")
	fmt.Println("  init          initialize project metadata")
	fmt.Println("  theme         package artifacts and run theme tasks")
	fmt.Println("  instance      manage the local WordPress instance")
	fmt.Println("  site          list, show, deploy/sync remote sites")
	fmt.Println("  server        provision, list, show, delete infrastructure hosts")
	fmt.Println("  config        init local config")
	fmt.Println("  password      derive passwords")
	fmt.Println("  help          show help")
	return 0
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
	case "server":
		return runServer(argv[1:])
	case "site":
		return runSite(argv[1:])
	case "instance":
		return runInstance(argv[1:])
	case "init":
		return runInit(argv[1:])
	case "theme":
		return runTheme(argv[1:])
	case "config":
		return runConfig(argv[1:])
	case "password":
		return runPassword(argv[1:])
	case "info":
		return runInstance([]string{"info"})
	case "up", "down", "logs", "reset", "shell", "wp":
		return runInstance(argv)
	default:
		fmt.Fprintf(os.Stderr, "unsupported command: %s\n", argv[0])
		return 1
	}
}

func runTopicHelp(argv []string) int {
	if len(argv) == 0 {
		return runHelp()
	}
	switch argv[0] {
	case "server":
		return runServerHelp()
	case "site":
		return runSiteHelp()
	case "init":
		return runInitHelp()
	case "theme":
		return runThemeHelp()
	case "instance":
		return runInstanceHelp()
	case "config":
		return runConfigHelp()
	case "password":
		return runPasswordHelp()
	default:
		return runHelp()
	}
}

func runPassword(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" {
		return runPasswordHelp()
	}
	if argv[0] != "derive" {
		fmt.Fprintln(os.Stderr, "unsupported password command")
		return 1
	}
	fs := flag.NewFlagSet("password derive", flag.ContinueOnError)
	nonInteractive := fs.Bool("non-interactive", false, "")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(argv[1:]); err != nil {
		return 1
	}
	args := fs.Args()
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "password derive requires a project slug and purpose")
		return 1
	}
	return cmdPasswordDerive(args[0], args[1], *nonInteractive)
}

func runConfig(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runConfigHelp()
	}
	if argv[0] != "init" {
		fmt.Fprintln(os.Stderr, "unsupported config command")
		return 1
	}
	fs := flag.NewFlagSet("config init", flag.ContinueOnError)
	nonInteractive := fs.Bool("non-interactive", false, "")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(argv[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}
	if err := envwizard.Init(configInitRequirements(), *nonInteractive); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func runServer(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" {
		return runServerHelp()
	}
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
	switch argv[0] {
	case "list":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "site list takes no arguments")
			return 1
		}
		return cmdList("sites")
	case "show":
		if len(argv) > 2 {
			fmt.Fprintln(os.Stderr, "site show takes exactly one identifier")
			return 1
		}
		needle := ""
		if len(argv) == 2 {
			needle = argv[1]
		} else {
			selected, err := chooseRecord("site", "show")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			needle = selected
		}
		return cmdShowSite(needle)
	case "install", "delete", "deploy", "push", "pull":
		fmt.Fprintf(os.Stderr, "site %s is not implemented yet\n", argv[0])
		return 1
	default:
		fmt.Fprintln(os.Stderr, "unsupported site command")
		return 1
	}
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

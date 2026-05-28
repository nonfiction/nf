package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

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

type workbenchConfig struct {
	ProjectSlug      string
	ProjectName      string
	RepoRoot         string
	ThemePath        string
	ManagedDir       string
	Compose          string
	WordpressService string
	CliService       string
	ThemeMountSlug   string
	UploadsPath      string
	ThemeSlug        string
}

func (c workbenchConfig) managedUploadsDir() string {
	return filepath.Join(c.ManagedDir, firstNonEmpty(c.UploadsPath, "uploads"))
}

func (c workbenchConfig) uploadsContainerPath() string {
	return path.Join("/", "workbench", firstNonEmpty(c.UploadsPath, "uploads"))
}

type workbenchCommandRunner struct {
	name string
	cfg  workbenchConfig
}

func (c workbenchCommandRunner) Execute(root string, extraArgs []string) error {
	if err := ensureManagedWorkbenchRuntime(c.cfg); err != nil {
		return err
	}
	workbenchDir := c.cfg.ManagedDir
	switch c.name {
	case "up":
		return runCommandSpec(execSpec{Dir: workbenchDir, Args: workbenchComposeArgs(c.cfg, "up", "-d")})
	case "down":
		return runCommandSpec(execSpec{Dir: workbenchDir, Args: workbenchComposeArgs(c.cfg, "down")})
	case "restart":
		if err := runCommandSpec(execSpec{Dir: workbenchDir, Args: workbenchComposeArgs(c.cfg, "down")}); err != nil {
			return err
		}
		return runCommandSpec(execSpec{Dir: workbenchDir, Args: workbenchComposeArgs(c.cfg, "up", "-d")})
	case "logs":
		return runCommandSpec(execSpec{Dir: workbenchDir, Args: workbenchComposeArgs(c.cfg, "logs", "-f", c.cfg.WordpressService)})
	case "reset":
		return runCommandSpec(execSpec{Dir: workbenchDir, Args: workbenchComposeArgs(c.cfg, "down", "-v", "--remove-orphans")})
	case "setup":
		if err := runCommandSpec(execSpec{Dir: workbenchDir, Args: workbenchComposeArgs(c.cfg, "up", "-d")}); err != nil {
			return err
		}
		if err := runCommandSpecQuiet(execSpec{Dir: workbenchDir, Args: workbenchWpProbeArgs(c.cfg, "core", "is-installed")}); err != nil {
			if err := runCommandSpec(execSpec{Dir: workbenchDir, Args: workbenchWpCoreInstallArgs(c.cfg)}); err != nil {
				return err
			}
		}
		return nil
	case "fresh":
		if err := runCommandSpec(execSpec{Dir: workbenchDir, Args: workbenchComposeArgs(c.cfg, "down", "-v", "--remove-orphans")}); err != nil {
			return err
		}
		if err := runCommandSpec(execSpec{Dir: workbenchDir, Args: workbenchComposeArgs(c.cfg, "up", "-d")}); err != nil {
			return err
		}
		if err := runCommandSpecQuiet(execSpec{Dir: workbenchDir, Args: workbenchWpProbeArgs(c.cfg, "core", "is-installed")}); err != nil {
			if err := runCommandSpec(execSpec{Dir: workbenchDir, Args: workbenchWpCoreInstallArgs(c.cfg)}); err != nil {
				return err
			}
		}
		return nil
	case "wp":
		return runCommandSpec(execSpec{Dir: workbenchDir, Args: workbenchWpArgs(c.cfg, extraArgs...)})
	case "install-theme":
		if len(extraArgs) == 0 || strings.TrimSpace(extraArgs[0]) == "" {
			return fmt.Errorf("install-theme requires a zip path")
		}
		zipPath := workbenchRepoPath(root, extraArgs[0])
		hostPath, containerPath := workbenchThemeArchivePaths(c.cfg, zipPath)
		if err := os.MkdirAll(c.cfg.managedUploadsDir(), 0o755); err != nil {
			return err
		}
		if err := copyFile(zipPath, hostPath); err != nil {
			return err
		}
		return runCommandSpec(execSpec{Dir: workbenchDir, Args: workbenchWpArgs(c.cfg, "theme", "install", containerPath)})
	case "activate-theme":
		slug := ""
		if len(extraArgs) > 0 && strings.TrimSpace(extraArgs[0]) != "" {
			slug = strings.TrimSpace(extraArgs[0])
		}
		return runCommandSpec(execSpec{Dir: workbenchDir, Args: workbenchWpThemeActivateArgs(c.cfg, slug)})
	default:
		return fmt.Errorf("unsupported repo command type")
	}
}

func (c workbenchCommandRunner) Render() string {
	switch c.name {
	case "up":
		return "docker compose up -d"
	case "down":
		return "docker compose down"
	case "restart":
		return "docker compose down && docker compose up -d"
	case "logs":
		return "docker compose logs -f " + c.cfg.WordpressService
	case "reset":
		return "docker compose down -v --remove-orphans"
	case "setup":
		return "docker compose up -d; wp core install and activate " + firstNonEmpty(c.cfg.ThemeMountSlug, c.cfg.ThemeSlug, "theme") + " if needed"
	case "fresh":
		return "docker compose down -v --remove-orphans && docker compose up -d; wp core install and activate " + firstNonEmpty(c.cfg.ThemeMountSlug, c.cfg.ThemeSlug, "theme") + " if needed"
	case "wp":
		return "docker compose run --rm " + c.cfg.CliService + " wp ... --allow-root"
	case "install-theme":
		return "copy theme zip into nf-managed workbench uploads and run wp theme install"
	case "activate-theme":
		return "docker compose run --rm " + c.cfg.CliService + " wp theme activate <slug> --allow-root"
	default:
		return c.name
	}
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

func workbenchCommandDir(cfg workbenchConfig) string {
	return cfg.ManagedDir
}

func ensureManagedWorkbenchRuntime(cfg workbenchConfig) error {
	if strings.TrimSpace(cfg.ManagedDir) == "" {
		return fmt.Errorf("missing managed workbench directory")
	}
	if err := os.MkdirAll(cfg.ManagedDir, 0o755); err != nil {
		return err
	}
	files := map[string]string{
		filepath.Join(cfg.ManagedDir, "docker-compose.yml"):                                  renderWorkbenchCompose(cfg),
		filepath.Join(cfg.ManagedDir, ".env"):                                                renderWorkbenchEnv(cfg),
		filepath.Join(cfg.ManagedDir, "php", "uploads.ini"):                                  renderWorkbenchUploadsINI(),
		filepath.Join(cfg.ManagedDir, "wordpress", "Dockerfile"):                             renderWorkbenchDockerfile(),
		filepath.Join(cfg.ManagedDir, "wordpress", "wordpress-rewrites.conf"):                renderWorkbenchRewritesConf(),
		filepath.Join(cfg.ManagedDir, firstNonEmpty(cfg.UploadsPath, "uploads"), ".gitkeep"): "",
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

func workbenchComposeArgs(cfg workbenchConfig, args ...string) []string {
	fields := strings.Fields(firstNonEmpty(cfg.Compose, "docker compose"))
	return append(fields, args...)
}

func workbenchCliArgs(cfg workbenchConfig, args ...string) []string {
	return append(workbenchComposeArgs(cfg, "run", "--rm", firstNonEmpty(cfg.CliService, "cli")), args...)
}

func workbenchWpArgs(cfg workbenchConfig, args ...string) []string {
	return append(workbenchCliArgs(cfg, "wp"), append(args, "--allow-root")...)
}

func workbenchWpProbeArgs(cfg workbenchConfig, args ...string) []string {
	return workbenchWpArgs(cfg, args...)
}

func workbenchWpCoreInstallArgs(cfg workbenchConfig) []string {
	slug := firstNonEmpty(cfg.ThemeMountSlug, cfg.ThemeSlug, "theme")
	return append(workbenchComposeArgs(cfg, "run", "--rm", firstNonEmpty(cfg.CliService, "cli"), "sh", "-lc"), `wp core install --url="$WP_URL" --title="$WP_TITLE" --admin_user="$ADMIN_USER" --admin_password="$ADMIN_PASSWORD" --admin_email="$ADMIN_EMAIL" --skip-email --allow-root && wp theme activate `+slug+` --allow-root`)
}

func workbenchWpThemeActivateArgs(cfg workbenchConfig, slug string) []string {
	return workbenchWpArgs(cfg, "theme", "activate", firstNonEmpty(slug, cfg.ThemeMountSlug, cfg.ThemeSlug, "theme"))
}

func workbenchThemeArchivePaths(cfg workbenchConfig, sourcePath string) (string, string) {
	base := filepath.Base(sourcePath)
	host := filepath.Join(workbenchCommandDir(cfg), firstNonEmpty(cfg.UploadsPath, "uploads"), base)
	container := path.Join("/", "workbench", firstNonEmpty(cfg.UploadsPath, "uploads"), base)
	return host, container
}

func workbenchRepoPath(root, sourcePath string) string {
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
		{Keys: []string{"NF_SECRET_SALT"}, Prompt: "NF_SECRET_SALT (used for derived passwords): ", Secret: true, WriteKey: "NF_SECRET_SALT", Required: true},
		{Keys: []string{"DNSIMPLE_TOKEN"}, Prompt: "DNSimple token: ", Secret: true, WriteKey: "DNSIMPLE_TOKEN", Required: true},
		{Keys: []string{"LINODE_CLI_TOKEN", "LINODE_TOKEN"}, Prompt: "Linode token: ", Secret: true, WriteKey: "LINODE_CLI_TOKEN", Required: true},
		{Keys: []string{"DNSIMPLE_ACCOUNT_ID"}, Prompt: "DNSimple account id: ", Default: "14", WriteKey: "DNSIMPLE_ACCOUNT_ID"},
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
		return "", ProjectError{Msg: "repo init requires a .git repository above the current directory when --project-slug is not set"}
	}
	base := filepath.Base(filepath.Clean(root))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "", ProjectError{Msg: fmt.Sprintf("repo init could not derive a project slug from git root %q; pass --project-slug", root)}
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
	if token := envwizard.Value("LINODE_CLI_TOKEN"); token != "" {
		return token, nil
	}
	if token := envwizard.Value("LINODE_TOKEN"); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("Expected LINODE_CLI_TOKEN or LINODE_TOKEN in the environment or %s.", config.EnvFile())
}

func runLinodeDelete(id string) error {
	token, err := linodeTokenEnv()
	if err != nil {
		return err
	}
	cmd := exec.Command("linode-cli", "linodes", "delete", id, "--json")
	cmd.Env = append(os.Environ(), "LINODE_CLI_TOKEN="+token, "LINODE_TOKEN="+token)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		details := strings.TrimSpace(stderr.String())
		if details == "" {
			details = strings.TrimSpace(stdout.String())
		}
		if details == "" {
			details = "linode-cli failed"
		}
		return fmt.Errorf("%s", details)
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

func defaultProjectCommands() map[string]map[string]any {
	return map[string]map[string]any{
		"composer": map[string]any{"description": "Update theme Composer dependencies", "run": "composer --working-dir=theme update && composer --working-dir=theme dump-autoload -o"},
		"npm":      map[string]any{"description": "Refresh theme development dependencies", "run": "npm --prefix theme update --save-dev"},
		"build":    map[string]any{"description": "Build the theme assets", "run": "npm --prefix theme run build"},
		"watch":    map[string]any{"description": "Watch theme assets during development", "run": "npm --prefix theme start"},
		"test":     map[string]any{"description": "Run the theme test suite", "run": "composer --working-dir=theme test"},
	}
}

func parseProjectCommand(name string, value any) (string, string, repoCommandRunner, error) {
	switch typed := value.(type) {
	case string:
		return name, typed, shellCommandRunner(typed), nil
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			s, ok := item.(string)
			if !ok {
				return "", "", nil, ProjectError{Msg: fmt.Sprintf(".nf/project.json commands.%s.run must be a string or array of strings", name)}
			}
			parts = append(parts, s)
		}
		return name, strings.Join(parts, " "), argvCommandRunner(parts), nil
	case map[string]any:
		desc, _ := typed["description"].(string)
		if strings.TrimSpace(desc) == "" {
			return "", "", nil, ProjectError{Msg: fmt.Sprintf(".nf/project.json commands.%s must include a description string", name)}
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
					return "", "", nil, ProjectError{Msg: fmt.Sprintf(".nf/project.json commands.%s.run must be a string or array of strings", name)}
				}
				parts = append(parts, s)
			}
			return name, desc, argvCommandRunner(parts), nil
		default:
			return "", "", nil, ProjectError{Msg: fmt.Sprintf(".nf/project.json commands.%s.run must be a string or array of strings", name)}
		}
	default:
		return "", "", nil, ProjectError{Msg: fmt.Sprintf(".nf/project.json commands.%s must be a string, array, or object", name)}
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

func loadProjectCommands(root string) (map[string]projectCommand, error) {
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		return nil, err
	}
	parsed := map[string]projectCommand{}
	if cfg, ok := loadWorkbenchConfig(root, metadata); ok {
		for name, command := range defaultWorkbenchCommands(cfg) {
			parsed[name] = command
		}
	}
	commands, ok := metadata["commands"].(map[string]any)
	if !ok || commands == nil {
		return parsed, nil
	}
	for name, value := range commands {
		_, desc, run, err := parseProjectCommand(name, value)
		if err != nil {
			return nil, err
		}
		parsed[name] = projectCommand{Description: desc, Run: run}
	}
	return parsed, nil
}

func formatProjectCommandLines(commands map[string]projectCommand) []string {
	keys := make([]string, 0, len(commands))
	for name := range commands {
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
		command := commands[name]
		lines = append(lines, fmt.Sprintf("%-*s -- %s", width, name, command.Description))
	}
	return lines
}

func loadWorkbenchConfig(root string, metadata map[string]any) (workbenchConfig, bool) {
	raw, ok := metadata["workbench"].(map[string]any)
	if !ok || raw == nil {
		return workbenchConfig{}, false
	}
	projectSlug := firstNonEmpty(mapStringAtPath(metadata, "project", "slug"), "project")
	projectName := firstNonEmpty(mapStringAtPath(metadata, "project", "name"), slugToTitle(projectSlug))
	themePath := firstNonEmpty(mapStringAtPath(metadata, "wordpress", "theme_path"), "theme")
	if !filepath.IsAbs(themePath) {
		themePath = filepath.Join(root, themePath)
	}
	wordpress := mapMapAtPath(metadata, "wordpress")
	return workbenchConfig{
		ProjectSlug:      projectSlug,
		ProjectName:      projectName,
		RepoRoot:         root,
		ThemePath:        themePath,
		ManagedDir:       config.WorkbenchDir(projectSlug),
		Compose:          firstNonEmpty(mapStringAtPath(raw, "compose"), "docker compose"),
		WordpressService: firstNonEmpty(mapStringAtPath(raw, "wordpress_service"), "wordpress"),
		CliService:       firstNonEmpty(mapStringAtPath(raw, "cli_service"), "cli"),
		ThemeMountSlug:   firstNonEmpty(mapStringAtPath(raw, "theme_mount_slug"), "theme"),
		UploadsPath:      firstNonEmpty(mapStringAtPath(raw, "uploads_path"), "uploads"),
		ThemeSlug:        firstNonEmpty(recordValueString(wordpress["theme_slug"]), "theme"),
	}, true
}

func defaultWorkbenchCommands(cfg workbenchConfig) map[string]projectCommand {
	return map[string]projectCommand{
		"setup":          {Description: "Set up the local workbench", Run: workbenchCommandRunner{name: "setup", cfg: cfg}},
		"up":             {Description: "Start the local workbench", Run: workbenchCommandRunner{name: "up", cfg: cfg}},
		"down":           {Description: "Stop the local workbench", Run: workbenchCommandRunner{name: "down", cfg: cfg}},
		"restart":        {Description: "Restart the local workbench", Run: workbenchCommandRunner{name: "restart", cfg: cfg}},
		"logs":           {Description: "Show local workbench logs", Run: workbenchCommandRunner{name: "logs", cfg: cfg}},
		"reset":          {Description: "Reset the local workbench", Run: workbenchCommandRunner{name: "reset", cfg: cfg}},
		"fresh":          {Description: "Rebuild the local workbench from scratch", Run: workbenchCommandRunner{name: "fresh", cfg: cfg}},
		"wp":             {Description: "Run wp-cli through the local workbench", Run: workbenchCommandRunner{name: "wp", cfg: cfg}},
		"install-theme":  {Description: "Install the theme in the local workbench", Run: workbenchCommandRunner{name: "install-theme", cfg: cfg}},
		"activate-theme": {Description: "Activate the theme in the local workbench", Run: workbenchCommandRunner{name: "activate-theme", cfg: cfg}},
	}
}

func discoverProjectRootOrError() (string, error) {
	if root, ok := config.DiscoverProjectRoot(""); ok {
		return root, nil
	}
	return "", ProjectError{Msg: "No repo metadata found above the current directory. Add .nf/project.json with workbench metadata or commands.<name>."}
}

func cmdProjectCommands() int {
	if err := requireProjectContext("repo commands"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root, err := discoverProjectRootOrError()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	commands, err := loadProjectCommands(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(commands) == 0 {
		fmt.Fprintln(os.Stderr, "No local repo commands configured. Add .nf/project.json workbench metadata or commands.<name>.")
		return 1
	}
	fmt.Println("repo-local commands:")
	for _, line := range formatProjectCommandLines(commands) {
		fmt.Printf("  %s\n", line)
	}
	return 0
}

func cmdProjectRun(name string, extraArgs []string) int {
	if err := requireProjectContext("repo run"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root, err := discoverProjectRootOrError()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	commands, err := loadProjectCommands(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	command, ok := commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "No configured local repo command named %q. Add .nf/project.json workbench metadata or commands.%s.\n", name, name)
		return 1
	}
	if err := command.Run.Execute(root, normalizePassthroughArgs(extraArgs)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
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
			fmt.Printf("  remote action: linode-cli linodes delete %s --json\n", remoteID)
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
	id := firstRecordString(server, "id", "linode_id")
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

func projectDeployAlias(metadata map[string]any, alias string) (string, bool, error) {
	deploy := mapMapAtPath(metadata, "deploy")
	if deploy == nil {
		return "", false, nil
	}
	aliases := mapMapAtPath(deploy, "aliases")
	if aliases == nil {
		return "", false, nil
	}
	value, ok := aliases[alias]
	if !ok {
		return "", false, nil
	}
	resolved, ok := value.(string)
	if !ok || strings.TrimSpace(resolved) == "" {
		return "", false, ProjectError{Msg: fmt.Sprintf(".nf/project.json deploy.aliases.%s must be a string target name", alias)}
	}
	return strings.TrimSpace(resolved), true, nil
}

func resolveSiteTarget(requested string) (string, map[string]any, bool, bool, error) {
	resolved := strings.TrimSpace(requested)
	if resolved == "" {
		return "", nil, false, false, ProjectError{Msg: "site show requires a target or alias"}
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
	if aliasTarget, aliasFound, err := projectDeployAlias(metadata, resolved); err != nil {
		return "", nil, false, false, err
	} else if aliasFound {
		return aliasTarget, metadata, projectFileExists, true, nil
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

func cmdShowSite(needle string) int {
	resolved, _, projectFileExists, aliasUsed, err := resolveSiteTarget(needle)
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
		if aliasUsed {
			fmt.Fprintf(os.Stderr, "deploy.aliases.%s resolves to %q, but no site target matched that name.\n", needle, resolved)
			return 1
		}
		if projectFileExists {
			fmt.Fprintf(os.Stderr, "No site matched %q. Add deploy.aliases.%s in .nf/project.json or create a site target with that name.\n", needle, needle)
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
	metadata := repoInitMetadata(args)
	projectPath := filepath.Join(root, ".nf", "project.json")
	if !args.force {
		if _, err := os.Stat(projectPath); err == nil {
			fmt.Fprintf(os.Stderr, "%s already exists; use --force to overwrite.\n", projectPath)
			return 1
		} else if !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if err := os.MkdirAll(filepath.Dir(projectPath), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(projectPath, []byte(repoInitJSON(metadata)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Wrote %s\n", projectPath)
	return 0
}

func repoInitMetadata(args projectInitArgs) map[string]any {
	themePath := firstNonEmpty(args.themeSource, "theme")
	themeSlug := firstNonEmpty(args.themeSlug, "theme")
	projectName := firstNonEmpty(args.projectName, slugToTitle(args.projectSlug))
	projectSlug := args.projectSlug
	metadata := map[string]any{
		"schema": 1,
		"project": map[string]any{
			"slug": projectSlug,
			"name": projectName,
			"type": "wordpress-theme",
		},
		"wordpress": map[string]any{
			"deploy_unit": "theme",
			"theme_slug":  themeSlug,
			"theme_path":  themePath,
		},
		"workbench": map[string]any{
			"compose":           "docker compose",
			"wordpress_service": "wordpress",
			"cli_service":       "cli",
			"theme_mount_slug":  "theme",
			"uploads_path":      "uploads",
		},
		"build": map[string]any{
			"commands": []any{"composer install", "npm run build"},
		},
		"artifact": map[string]any{
			"path":    filepath.ToSlash(filepath.Join("dist", projectSlug+"-v{version}.zip")),
			"include": []any{"vendor/", "assets/dist/"},
			"exclude": []any{"node_modules/", ".git/"},
		},
		"deploy": map[string]any{
			"aliases": map[string]any{},
		},
		"commands": defaultProjectCommands(),
	}
	return metadata
}

type projectInitArgs struct {
	projectSlug string
	projectName string
	themeSlug   string
	themeSource string
	force       bool
}

func repoInitJSON(metadata map[string]any) string {
	data, _ := json.MarshalIndent(metadata, "", "  ")
	return string(append(data, '\n'))
}

func renderWorkbenchCompose(cfg workbenchConfig) string {
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

  mailpit:
    image: axllent/mailpit
    ports:
      - "${MAILPIT_PORT}:8025"

volumes:
  db_data:
  wp_data:
`, wordpressService, themePath, themeMountSlug, cliService, wordpressService, themePath, themeMountSlug, uploadsPath, path.Join("/", "workbench", uploadsPath))
}

func renderWorkbenchEnv(cfg workbenchConfig) string {
	wpTitle := firstNonEmpty(cfg.ProjectName, slugToTitle(cfg.ProjectSlug))
	return fmt.Sprintf(`COMPOSE_PROJECT_NAME=%s
WP_PORT=18080
MAILPIT_PORT=8026
DB_NAME=%s
DB_USER=%s
DB_PASSWORD=wordpress
DB_ROOT_PASSWORD=root
WP_URL=http://localhost:18080
WP_TITLE=%s
ADMIN_USER=admin
ADMIN_PASSWORD=admin
ADMIN_EMAIL=web@nonfiction.ca
`, workbenchComposeProjectName(cfg.ProjectSlug), cfg.ProjectSlug, cfg.ProjectSlug, wpTitle)
}

func workbenchComposeProjectName(projectSlug string) string {
	cleaned := strings.ToLower(strings.TrimSpace(projectSlug))
	var b strings.Builder
	b.Grow(len(cleaned) + len("nf__workbench"))
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
	return "nf_" + slug + "_workbench"
}

func renderWorkbenchUploadsINI() string {
	return "file_uploads=On\nmemory_limit=256M\nupload_max_filesize=128M\npost_max_size=128M\nmax_execution_time=120\nmax_input_time=120\n"
}

func renderWorkbenchDockerfile() string {
	return `FROM wordpress:7.0-php8.4-apache

RUN a2enmod rewrite \
  && sed -ri 's/AllowOverride None/AllowOverride All/g' /etc/apache2/apache2.conf

COPY wordpress/wordpress-rewrites.conf /etc/apache2/conf-enabled/wordpress-rewrites.conf
`
}

func renderWorkbenchRewritesConf() string {
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

func cmdRepoPackage(source, output string, dryRun bool) int {
	return cmdPackage("repo package", source, output, dryRun)
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
		"provision [flags]   provision a new server",
		"list                list servers",
		"show <id-or-name>   show a server",
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

func runRepoHelp() int {
	lines := []string{
		"init                create .nf/project.json",
	}
	if projectContextAvailable() {
		lines = append(lines,
			"commands            list configured local repo commands",
			"run <name>          run a configured local repo command",
			"package [--dry-run] [--source] [--output]   package theme artifacts",
		)
		if root, ok := currentGitRoot(); ok {
			if commands, err := loadProjectCommands(root); err == nil && len(commands) > 0 {
				lines = append(lines, "repo-local commands:")
				lines = append(lines, formatProjectCommandLines(commands)...)
			}
		}
	}
	printGroupHelp("repo", lines)
	return 0
}

func runHelp() int {
	fmt.Println("nf")
	fmt.Println("\nCommands:")
	fmt.Println("  server        provision, list, show, delete servers")
	fmt.Println("  site          list, show, future install/delete/deploy/sync")
	repoLine := "  repo          init repo metadata and manage workbench runtime"
	if projectContextAvailable() {
		repoLine = "  repo          init and repo-local commands"
	}
	fmt.Println(repoLine)
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
	case "repo":
		return runRepo(argv[1:])
	case "config":
		return runConfig(argv[1:])
	case "password":
		return runPassword(argv[1:])
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
	case "repo":
		return runRepoHelp()
	case "config":
		return runConfigHelp()
	case "password":
		return runPasswordHelp()
	default:
		return runHelp()
	}
}

func runRepo(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" {
		return runRepoHelp()
	}
	if argv[0] == "init" {
		fs := flag.NewFlagSet("repo init", flag.ContinueOnError)
		projectSlug := fs.String("project-slug", "", "")
		projectName := fs.String("project-name", "", "")
		themeSlug := fs.String("theme-slug", "", "")
		themeSource := fs.String("theme-source", "", "")
		force := fs.Bool("force", false, "")
		fs.SetOutput(os.Stderr)
		if err := fs.Parse(argv[1:]); err != nil {
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
		return cmdProjectInit(projectInitArgs{projectSlug: *projectSlug, projectName: *projectName, themeSlug: *themeSlug, themeSource: *themeSource, force: *force})
	}
	if argv[0] == "package" {
		if err := requireProjectContext("repo package"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fs := flag.NewFlagSet("repo package", flag.ContinueOnError)
		source := fs.String("source", "", "")
		output := fs.String("output", "", "")
		dryRun := fs.Bool("dry-run", false, "")
		fs.SetOutput(os.Stderr)
		if err := fs.Parse(argv[1:]); err != nil {
			return 1
		}
		return cmdRepoPackage(*source, *output, *dryRun)
	}
	if argv[0] == "commands" {
		return cmdProjectCommands()
	}
	if argv[0] == "run" {
		if len(argv) < 2 {
			fmt.Fprintln(os.Stderr, "repo run requires a command name")
			return 1
		}
		return cmdProjectRun(argv[1], argv[2:])
	}
	if err := requireProjectContext(argv[0]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root, err := discoverProjectRootOrError()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	commands, err := loadProjectCommands(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if command, ok := commands[argv[0]]; ok {
		if err := command.Run.Execute(root, normalizePassthroughArgs(argv[1:])); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, "unsupported repo command")
	return 1
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
	if len(argv) == 0 || argv[0] == "help" {
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
	fs.StringVar(&args.Provider, "provider", "", "")
	fs.StringVar(&args.ProjectSlug, "project-slug", "", "")
	fs.StringVar(&args.ServerName, "server-name", "", "")
	fs.StringVar(&args.SiteDomain, "site-domain", "", "")
	fs.StringVar(&args.Label, "label", "", "")
	fs.StringVar(&args.Region, "region", "", "")
	fs.StringVar(&args.Type, "type", "", "")
	fs.StringVar(&args.Image, "image", "", "")
	fs.StringVar(&args.SshUser, "ssh-user", "", "")
	fs.StringVar(&args.SshPublicKeyFile, "ssh-public-key-file", "", "")
	fs.StringVar(&args.RemoteWpPath, "remote-wp-path", "", "")
	fs.StringVar(&args.PhpFpmSocket, "php-fpm-socket", "", "")
	fs.StringVar(&args.DbName, "db-name", "", "")
	fs.StringVar(&args.DbUser, "db-user", "", "")
	fs.StringVar(&args.WpAdminUser, "wp-admin-user", "", "")
	fs.StringVar(&args.WpAdminEmail, "wp-admin-email", "", "")
	fs.StringVar(&args.SiteTitle, "site-title", "", "")
	fs.StringVar(&args.DnsZone, "dns-zone", "", "")
	fs.StringVar(&args.DnsimpleAccountID, "dnsimple-account-id", "", "")
	fs.StringVar(&args.WriteCloudInit, "write-cloud-init", "", "")
	fs.BoolVar(&args.NonInteractive, "non-interactive", false, "")
	fs.BoolVar(&args.ShowCloudInit, "show-cloud-init", false, "")
	fs.BoolVar(&args.Execute, "execute", false, "")
	fs.BoolVar(&args.Yes, "yes", false, "")
	fs.BoolVar(&args.DryRun, "dry-run", false, "")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(argv); err != nil {
		return 1
	}
	if args.Provider != "" && args.Provider != "linode" {
		fmt.Fprintln(os.Stderr, "Only --provider linode is supported in this slice.")
		return 1
	}
	if args.Execute && args.DryRun {
		fmt.Fprintln(os.Stderr, "Choose either --execute or --dry-run, not both.")
		return 1
	}
	if !args.Execute {
		args.DryRun = true
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

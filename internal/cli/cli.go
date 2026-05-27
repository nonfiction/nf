package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/provision"
	"github.com/nonfiction/nf/internal/state"
	"github.com/nonfiction/nf/internal/theme"
)

type ProjectError struct{ Msg string }

func (e ProjectError) Error() string { return e.Msg }

var localProjectCommands = []string{"composer", "npm", "build", "watch", "test", "setup", "up", "down", "restart", "logs", "reset", "fresh", "wp", "install-theme", "activate-theme"}

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
	default:
		return fmt.Sprint(run)
	}
}

func defaultProjectCommands() map[string]map[string]any {
	return map[string]map[string]any{
		"composer":       map[string]any{"description": "Update theme Composer dependencies", "run": "composer --working-dir=theme update && composer --working-dir=theme dump-autoload -o"},
		"npm":            map[string]any{"description": "Refresh theme development dependencies", "run": "npm --prefix theme update --save-dev"},
		"build":          map[string]any{"description": "Build the theme assets", "run": "npm --prefix theme run build"},
		"watch":          map[string]any{"description": "Watch theme assets during development", "run": "npm --prefix theme start"},
		"test":           map[string]any{"description": "Run the theme test suite", "run": "composer --working-dir=theme test"},
		"setup":          map[string]any{"description": "Set up the local workbench", "run": "cd workbench && docker compose up -d && docker compose run --rm cli sh -lc 'wp core is-installed --allow-root || wp core install --url=\"$WP_URL\" --title=\"$WP_TITLE\" --admin_user=\"$ADMIN_USER\" --admin_password=\"$ADMIN_PASSWORD\" --admin_email=\"$ADMIN_EMAIL\" --skip-email --allow-root'"},
		"up":             map[string]any{"description": "Start the local workbench", "run": "cd workbench && docker compose up -d"},
		"down":           map[string]any{"description": "Stop the local workbench", "run": "cd workbench && docker compose down"},
		"restart":        map[string]any{"description": "Restart the local workbench", "run": "cd workbench && docker compose down && docker compose up -d"},
		"logs":           map[string]any{"description": "Show local workbench logs", "run": "cd workbench && docker compose logs -f wordpress"},
		"reset":          map[string]any{"description": "Reset the local workbench", "run": "cd workbench && docker compose down -v --remove-orphans"},
		"fresh":          map[string]any{"description": "Rebuild the local workbench from scratch", "run": "cd workbench && docker compose down -v --remove-orphans && docker compose up -d && docker compose run --rm cli sh -lc 'wp core is-installed --allow-root || wp core install --url=\"$WP_URL\" --title=\"$WP_TITLE\" --admin_user=\"$ADMIN_USER\" --admin_password=\"$ADMIN_PASSWORD\" --admin_email=\"$ADMIN_EMAIL\" --skip-email --allow-root'"},
		"wp":             map[string]any{"description": "Run wp-cli through the local workbench", "run": "cd workbench && docker compose run --rm cli sh -lc 'wp \"$@\" --allow-root' sh \"$@\""},
		"install-theme":  map[string]any{"description": "Install the theme in the local workbench", "run": "cd workbench && docker compose run --rm cli sh -lc 'theme_zip=\"${1:?theme zip path required}\"; wp theme install \"$theme_zip\" --activate --allow-root' sh \"$@\""},
		"activate-theme": map[string]any{"description": "Activate the theme in the local workbench", "run": "cd workbench && docker compose run --rm cli sh -lc 'theme_slug=\"${1:?theme slug required}\"; wp theme activate \"$theme_slug\" --allow-root' sh \"$@\""},
	}
}

func parseProjectCommand(name string, value any) (string, string, any, error) {
	switch typed := value.(type) {
	case string:
		return name, typed, typed, nil
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			s, ok := item.(string)
			if !ok {
				return "", "", nil, ProjectError{Msg: fmt.Sprintf(".nf/project.json commands.%s.run must be a string or array of strings", name)}
			}
			parts = append(parts, s)
		}
		return name, strings.Join(parts, " "), parts, nil
	case map[string]any:
		desc, _ := typed["description"].(string)
		if strings.TrimSpace(desc) == "" {
			return "", "", nil, ProjectError{Msg: fmt.Sprintf(".nf/project.json commands.%s must include a description string", name)}
		}
		run := typed["run"]
		switch rr := run.(type) {
		case string:
			return name, desc, rr, nil
		case []any:
			parts := make([]string, 0, len(rr))
			for _, item := range rr {
				s, ok := item.(string)
				if !ok {
					return "", "", nil, ProjectError{Msg: fmt.Sprintf(".nf/project.json commands.%s.run must be a string or array of strings", name)}
				}
				parts = append(parts, s)
			}
			return name, desc, parts, nil
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

func loadProjectCommands(root string) (map[string]struct {
	Description string
	Run         any
}, error) {
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		return nil, err
	}
	commands, ok := metadata["commands"].(map[string]any)
	if !ok || commands == nil {
		return map[string]struct {
			Description string
			Run         any
		}{}, nil
	}
	parsed := map[string]struct {
		Description string
		Run         any
	}{}
	for name, value := range commands {
		_, desc, run, err := parseProjectCommand(name, value)
		if err != nil {
			return nil, err
		}
		parsed[name] = struct {
			Description string
			Run         any
		}{Description: desc, Run: run}
	}
	return parsed, nil
}

func discoverProjectRootOrError() (string, error) {
	if root, ok := config.DiscoverProjectRoot(""); ok {
		return root, nil
	}
	return "", ProjectError{Msg: "No .nf/project.json found above the current directory. Add one with commands.<name>."}
}

func executeProjectCommand(root string, run any, extraArgs []string) error {
	switch typed := run.(type) {
	case string:
		cmd := exec.Command("sh", append([]string{"-lc", typed, "sh"}, extraArgs...)...)
		cmd.Dir = root
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		return cmd.Run()
	case []string:
		cmd := exec.Command(typed[0], append(typed[1:], extraArgs...)...)
		cmd.Dir = root
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		return cmd.Run()
	default:
		return fmt.Errorf("unsupported project command type")
	}
}

func cmdProjectCommands() int {
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
		fmt.Fprintln(os.Stderr, "No local project commands configured. Add .nf/project.json commands.<name>.")
		return 1
	}
	rows := [][]string{{"name", "description", "run"}}
	keys := make([]string, 0, len(commands))
	for name := range commands {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		command := commands[name]
		rows = append(rows, []string{name, command.Description, renderCommandRun(command.Run)})
	}
	fmt.Println(formatTable(rows))
	return 0
}

func cmdProjectRun(name string, extraArgs []string) int {
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
		fmt.Fprintf(os.Stderr, "No configured local project command named %q. Add .nf/project.json commands.%s.\n", name, name)
		return 1
	}
	if err := executeProjectCommand(root, command.Run, normalizePassthroughArgs(extraArgs)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func cmdProjectAlias(name string, extraArgs []string) int { return cmdProjectRun(name, extraArgs) }

func cmdList(kind string) int {
	bundle, err := state.LoadStateBundle()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var records []map[string]any
	switch kind {
	case "servers":
		records = bundle.Servers
	case "sites":
		records = bundle.Sites
	default:
		fmt.Fprintln(os.Stderr, "unsupported list kind")
		return 1
	}
	if len(records) == 0 {
		fmt.Printf("No %s found.\n", kind)
		return 0
	}
	rows := [][]string{{"id", "name", "provider", "hostname", "status"}}
	for _, record := range records {
		rows = append(rows, []string{
			fmt.Sprint(firstRecordValue(record, "id", "_state_key")),
			fmt.Sprint(firstRecordValue(record, "name", "slug")),
			fmt.Sprint(record["provider"]),
			fmt.Sprint(firstRecordValue(record, "hostname", "site_url")),
			fmt.Sprint(record["status"]),
		})
	}
	fmt.Println(formatTable(rows))
	return 0
}

func firstRecordValue(record map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := record[key]; ok && fmt.Sprint(value) != "<nil>" && fmt.Sprint(value) != "" {
			return value
		}
	}
	return ""
}

func cmdShow(kind, needle string) int {
	bundle, err := state.LoadStateBundle()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var records []map[string]any
	switch kind {
	case "server":
		records = bundle.Servers
	case "site":
		records = bundle.Sites
	default:
		fmt.Fprintln(os.Stderr, "unsupported show kind")
		return 1
	}
	record := state.MatchingRecord(records, needle)
	if record == nil {
		fmt.Fprintf(os.Stderr, "No %s matched %q.\n", kind, needle)
		return 1
	}
	data, _ := json.MarshalIndent(record, "", "  ")
	fmt.Println(string(data))
	return 0
}

func cmdProjectInit(args projectInitArgs) int {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	metadata := map[string]any{
		"project_slug":        args.projectSlug,
		"project_name":        firstNonEmpty(args.projectName, slugToTitle(args.projectSlug)),
		"theme_slug":          firstNonEmpty(args.themeSlug, args.projectSlug),
		"theme_source":        firstNonEmpty(args.themeSource, "theme"),
		"local_workbench_url": firstNonEmpty(args.localWorkbenchURL, "http://localhost:18181"),
		"default_provider":    firstNonEmpty(args.defaultProvider, "linode"),
		"commands":            defaultProjectCommands(),
	}
	projectPath := filepath.Join(root, ".nf", "project.json")
	if _, err := os.Stat(projectPath); err == nil && !args.force {
		fmt.Fprintf(os.Stderr, "%s already exists; use --force to overwrite.\n", projectPath)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(projectPath), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	data, _ := json.MarshalIndent(metadata, "", "  ")
	if err := os.WriteFile(projectPath, append(data, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Wrote %s\n", projectPath)
	return 0
}

type projectInitArgs struct {
	projectSlug       string
	projectName       string
	themeSlug         string
	themeSource       string
	localWorkbenchURL string
	defaultProvider   string
	force             bool
}

func cmdPasswordDerive(slug, purpose string) int {
	salt, err := passwords.SecretSalt()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(passwords.DerivePassword(slug, purpose, salt))
	return 0
}

func cmdThemePackage(source, output string, dryRun bool) int {
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
		if v, ok := metadata["theme_source"].(string); ok && strings.TrimSpace(v) != "" {
			source = v
		} else {
			source = "theme"
		}
	}
	sourceDir := source
	if !filepath.IsAbs(sourceDir) {
		sourceDir = filepath.Join(root, sourceDir)
	}
	themeSlug := filepath.Base(sourceDir)
	if v, ok := metadata["theme_slug"].(string); ok && strings.TrimSpace(v) != "" {
		themeSlug = v
	}
	if output == "" {
		output = filepath.Join(root, "dist", themeSlug+".zip")
	} else if !filepath.IsAbs(output) {
		if strings.HasSuffix(strings.ToLower(output), ".zip") {
			output = filepath.Join(root, output)
		} else {
			output = filepath.Join(root, output, themeSlug+".zip")
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

func normalizePassthroughArgs(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}

func buildParser() *flag.FlagSet { return flag.NewFlagSet("nf", flag.ContinueOnError) }

func Run(argv []string) int {
	if len(argv) == 0 {
		return runHelp()
	}
	switch argv[0] {
	case "--help", "-h", "help":
		return runHelp()
	case "provision-server":
		return runProvision(argv[1:])
	case "commands":
		return cmdProjectCommands()
	case "run":
		if len(argv) < 2 {
			fmt.Fprintln(os.Stderr, "run requires a command name")
			return 1
		}
		return cmdProjectRun(argv[1], argv[2:])
	case "list":
		if len(argv) < 2 {
			fmt.Fprintln(os.Stderr, "list requires servers or sites")
			return 1
		}
		return cmdList(argv[1])
	case "show":
		if len(argv) < 3 {
			fmt.Fprintln(os.Stderr, "show requires server/site and an identifier")
			return 1
		}
		return cmdShow(argv[1], argv[2])
	case "project":
		return runProject(argv[1:])
	case "password":
		return runPassword(argv[1:])
	case "theme":
		return runTheme(argv[1:])
	default:
		for _, name := range localProjectCommands {
			if argv[0] == name {
				return cmdProjectAlias(name, argv[1:])
			}
		}
		fmt.Fprintf(os.Stderr, "unsupported command: %s\n", argv[0])
		return 1
	}
}

func runHelp() int {
	fmt.Println("nf")
	fmt.Println("\nCommands:")
	fmt.Println("  provision-server")
	fmt.Println("  commands")
	fmt.Println("  run <name>")
	fmt.Println("  list servers|sites")
	fmt.Println("  show server|site <id-or-name>")
	fmt.Println("  project init")
	fmt.Println("  password derive <project-slug> <purpose>")
	fmt.Println("  theme package")
	for _, name := range localProjectCommands {
		fmt.Printf("  %s\n", name)
	}
	return 0
}

func runProject(argv []string) int {
	if len(argv) == 0 || argv[0] != "init" {
		fmt.Fprintln(os.Stderr, "unsupported project command")
		return 1
	}
	fs := flag.NewFlagSet("project init", flag.ContinueOnError)
	projectSlug := fs.String("project-slug", "", "")
	projectName := fs.String("project-name", "", "")
	themeSlug := fs.String("theme-slug", "", "")
	themeSource := fs.String("theme-source", "", "")
	localWorkbenchURL := fs.String("local-workbench-url", "http://localhost:18181", "")
	defaultProvider := fs.String("default-provider", "linode", "")
	force := fs.Bool("force", false, "")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(argv[1:]); err != nil {
		return 1
	}
	if strings.TrimSpace(*projectSlug) == "" {
		fmt.Fprintln(os.Stderr, "--project-slug is required")
		return 1
	}
	return cmdProjectInit(projectInitArgs{*projectSlug, *projectName, *themeSlug, *themeSource, *localWorkbenchURL, *defaultProvider, *force})
}

func runPassword(argv []string) int {
	if len(argv) < 3 || argv[0] != "derive" {
		fmt.Fprintln(os.Stderr, "unsupported password command")
		return 1
	}
	return cmdPasswordDerive(argv[1], argv[2])
}

func runTheme(argv []string) int {
	if len(argv) == 0 || argv[0] != "package" {
		fmt.Fprintln(os.Stderr, "unsupported theme command")
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
}

func runProvision(argv []string) int {
	fs := flag.NewFlagSet("provision-server", flag.ContinueOnError)
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

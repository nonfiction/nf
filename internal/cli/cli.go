package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
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

func currentGitRoot() (string, bool) {
	wd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	return discoverGitRoot(wd)
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

func serverSummary(server map[string]any) string {
	name := firstRecordString(server, "name", "slug", "_state_key", "hostname", "label")
	id := firstRecordString(server, "id", "linode_id")
	provider := recordValueString(server["provider"])
	parts := make([]string, 0, 3)
	if name != "" {
		parts = append(parts, name)
	}
	if id != "" {
		parts = append(parts, "id "+id)
	}
	if provider != "" && provider != "<nil>" {
		parts = append(parts, provider)
	}
	return strings.Join(parts, " / ")
}

func siteSummary(site map[string]any) string {
	return firstRecordString(site, "hostname", "name", "slug", "label", "server_name", "_state_key")
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
	return "", ProjectError{Msg: "No repo metadata found above the current directory. Add .nf/project.json with commands.<name>."}
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
		return fmt.Errorf("unsupported repo command type")
	}
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
		fmt.Fprintln(os.Stderr, "No local repo commands configured. Add .nf/project.json commands.<name>.")
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
		fmt.Fprintf(os.Stderr, "No configured local repo command named %q. Add .nf/project.json commands.%s.\n", name, name)
		return 1
	}
	if err := executeProjectCommand(root, command.Run, normalizePassthroughArgs(extraArgs)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func cmdProjectAlias(name string, extraArgs []string) int { return cmdProjectRun(name, extraArgs) }

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
			firstRecordString(record, "id", "_state_key"),
			firstRecordString(record, "name", "slug"),
			recordValueString(record["provider"]),
			firstRecordString(record, "hostname", "site_url"),
			recordValueString(record["status"]),
		})
	}
	fmt.Println(formatTable(rows))
	return 0
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
				keys := make([]string, 0, len(commands))
				for name := range commands {
					keys = append(keys, name)
				}
				sort.Strings(keys)
				for _, name := range keys {
					lines = append(lines, fmt.Sprintf("%s - %s", name, commands[name].Description))
				}
			}
			lines = append(lines,
				"build               repo alias",
				"watch               repo alias",
				"test                repo alias",
				"setup               repo alias",
				"up                  repo alias",
				"down                repo alias",
				"restart             repo alias",
				"logs                repo alias",
				"reset               repo alias",
				"fresh               repo alias",
				"wp                  repo alias",
				"install-theme       repo alias",
				"activate-theme      repo alias",
			)
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
	repoLine := "  repo          init repo metadata"
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
	for _, name := range localProjectCommands {
		if argv[0] == name {
			if err := requireProjectContext(name); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			return cmdProjectAlias(name, argv[1:])
		}
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
		return cmdShow("server", needle)
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
		return cmdShow("site", needle)
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

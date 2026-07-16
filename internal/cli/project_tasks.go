package cli

// Project metadata, task loading, and default local env commands.
//
// Project-local commands read nf.json only from a git worktree and keep
// generated files/secrets outside that metadata file.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/project"
)

type projectMetadata = project.Manifest

func defaultProjectTasks(themePath string) map[string]any {
	themePath = firstNonEmpty(strings.TrimSpace(themePath), "theme")
	composerPath := shellQuoteArg(themePath)
	npmPath := shellQuoteArg(themePath)
	return map[string]any{
		"composer": map[string]any{"description": "Update theme Composer dependencies", "run": "composer --working-dir=" + composerPath + " update && composer --working-dir=" + composerPath + " dump-autoload -o"},
		"npm":      map[string]any{"description": "Refresh theme development dependencies", "run": "npm --prefix " + npmPath + " update --save-dev"},
		"build":    map[string]any{"description": "Build the theme assets", "run": "npm --prefix " + npmPath + " run build"},
		"watch":    map[string]any{"description": "Watch theme assets during development", "run": "npm --prefix " + npmPath + " start"},
		"test":     map[string]any{"description": "Run the theme test suite", "run": "composer --working-dir=" + composerPath + " test"},
	}
}

func parseProjectTask(location, name string, value any) (string, string, repoCommandRunner, error) {
	taskLocation := location + "." + name
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return "", "", nil, ProjectError{Msg: taskLocation + " must not be empty"}
		}
		return name, typed, shellCommandRunner(typed), nil
	case []any:
		if len(typed) == 0 {
			return "", "", nil, ProjectError{Msg: taskLocation + " must not be an empty array"}
		}
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			s, ok := item.(string)
			if !ok {
				return "", "", nil, ProjectError{Msg: taskLocation + " must be an array of strings"}
			}
			parts = append(parts, s)
		}
		return name, strings.Join(parts, " "), argvCommandRunner(parts), nil
	case map[string]any:
		if err := validateProjectObjectFields(taskLocation, typed, "description", "run"); err != nil {
			return "", "", nil, err
		}
		desc := ""
		if rawDescription, ok := typed["description"]; ok {
			var descriptionOK bool
			desc, descriptionOK = rawDescription.(string)
			if !descriptionOK || strings.TrimSpace(desc) == "" {
				return "", "", nil, ProjectError{Msg: taskLocation + ".description must be a non-empty string"}
			}
		}
		run := typed["run"]
		switch rr := run.(type) {
		case string:
			if strings.TrimSpace(rr) == "" {
				return "", "", nil, ProjectError{Msg: taskLocation + ".run must not be empty"}
			}
			if desc == "" {
				desc = rr
			}
			return name, desc, shellCommandRunner(rr), nil
		case []any:
			if len(rr) == 0 {
				return "", "", nil, ProjectError{Msg: taskLocation + ".run must not be an empty array"}
			}
			parts := make([]string, 0, len(rr))
			for _, item := range rr {
				s, ok := item.(string)
				if !ok {
					return "", "", nil, ProjectError{Msg: taskLocation + ".run must be a string or array of strings"}
				}
				parts = append(parts, s)
			}
			if desc == "" {
				desc = strings.Join(parts, " ")
			}
			return name, desc, argvCommandRunner(parts), nil
		default:
			return "", "", nil, ProjectError{Msg: taskLocation + ".run must be a string or array of strings"}
		}
	default:
		return "", "", nil, ProjectError{Msg: taskLocation + " must be a string, array, or object"}
	}
}

func validateProjectObjectFields(location string, object map[string]any, allowed ...string) error {
	allowedFields := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedFields[field] = struct{}{}
	}
	unknown := make([]string, 0)
	for field := range object {
		if _, ok := allowedFields[field]; !ok {
			unknown = append(unknown, field)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return ProjectError{Msg: fmt.Sprintf("%s contains unknown field %q", location, unknown[0])}
}

func projectObjectStringField(location string, object map[string]any, field string, required bool) (string, error) {
	raw, exists := object[field]
	if !exists {
		if required {
			return "", ProjectError{Msg: location + "." + field + " is required"}
		}
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", ProjectError{Msg: location + "." + field + " must be a string"}
	}
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", ProjectError{Msg: location + "." + field + " is required"}
	}
	return value, nil
}

func loadProjectMetadataOrError(root string) (*projectMetadata, error) {
	if root == "" {
		return &projectMetadata{}, nil
	}
	metadata, err := project.Load(root)
	if err != nil {
		return nil, err
	}
	if err := validateProjectMetadata(metadata); err != nil {
		return nil, err
	}
	return metadata, nil
}

func validateProjectMetadata(metadata *projectMetadata) error {
	if _, err := loadWordPressThemeSpecs(metadata); err != nil {
		return err
	}
	if _, err := loadWordPressPluginSpecs(metadata); err != nil {
		return err
	}
	if err := validateConfiguredDefineMetadata(metadata); err != nil {
		return err
	}
	if _, err := loadAliasSpecs(metadata); err != nil {
		return err
	}
	if _, err := loadProjectTasksFromMetadata(metadata); err != nil {
		return err
	}
	if _, err := repoThemePackageOutput(metadata); err != nil {
		return err
	}
	for name := range metadata.Remotes {
		if _, _, _, err := projectRemoteAlias(metadata, name); err != nil {
			return err
		}
	}
	return nil
}

func saveProjectMetadata(root string, metadata *projectMetadata) error {
	return project.Save(root, metadata)
}

func projectRemotes(metadata *projectMetadata, create bool) (project.RemoteRefs, error) {
	if metadata.Remotes == nil {
		if !create {
			return nil, nil
		}
		metadata.Remotes = project.RemoteRefs{}
	}
	return metadata.Remotes, nil
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
	return loadProjectTasksFromMetadata(metadata)
}

func loadProjectTasksFromMetadata(metadata *projectMetadata) (map[string]projectCommand, error) {
	parsed := map[string]projectCommand{}
	tasks, themeIndex, err := projectRepoThemeTasks(metadata)
	if err != nil {
		return nil, err
	}
	if tasks == nil {
		return parsed, nil
	}
	for name, value := range tasks {
		location := fmt.Sprintf("nf.json wordpress.themes[%d].tasks", themeIndex)
		if themeTaskNameReserved(name) {
			return nil, ProjectError{Msg: fmt.Sprintf("%s.%s conflicts with the built-in nf theme command %q", location, name, name)}
		}
		_, desc, run, err := parseProjectTask(location, name, value)
		if err != nil {
			return nil, err
		}
		parsed[name] = projectCommand{Description: desc, Run: run}
	}
	return parsed, nil
}

func themeTaskNameReserved(name string) bool {
	switch name {
	case "list", "ls", "status", "diff", "install", "add", "activate", "remove", "rm", "cache", "tasks", "package", "deploy", "rollback", "help", "--help", "-h":
		return true
	default:
		return false
	}
}

func projectRepoThemeTasks(metadata *projectMetadata) (map[string]any, int, error) {
	themeObject, themeIndex, err := projectRepoThemeObject(metadata)
	if err != nil || themeObject == nil {
		return nil, themeIndex, err
	}
	value, ok := themeObject["tasks"]
	if !ok {
		return nil, themeIndex, nil
	}
	tasks, ok := value.(map[string]any)
	if !ok || tasks == nil {
		return nil, 0, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.themes[%d].tasks must be an object", themeIndex)}
	}
	return tasks, themeIndex, nil
}

func projectRepoThemeObject(metadata *projectMetadata) (map[string]any, int, error) {
	for i, raw := range metadata.WordPress.Themes {
		themeSpec, err := parseWordPressThemeSpec(i, raw)
		if err != nil {
			return nil, 0, err
		}
		if !themeSourceIsRepo(themeSpec) {
			continue
		}
		var themeObject map[string]any
		switch typed := raw.(type) {
		case map[string]any:
			themeObject = typed
		case orderedObject:
			themeObject = orderedObjectMap(typed)
		default:
			return nil, 0, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.themes[%d] repo theme must be an object", i)}
		}
		return themeObject, i, nil
	}
	return nil, 0, nil
}

func repoThemePackageOutput(metadata *projectMetadata) (string, error) {
	themeObject, themeIndex, err := projectRepoThemeObject(metadata)
	if err != nil || themeObject == nil {
		return "", err
	}
	raw, ok := themeObject["package"]
	if !ok {
		return "", nil
	}
	packageConfig, ok := raw.(map[string]any)
	if !ok || packageConfig == nil {
		return "", ProjectError{Msg: fmt.Sprintf("nf.json wordpress.themes[%d].package must be an object", themeIndex)}
	}
	location := fmt.Sprintf("nf.json wordpress.themes[%d].package", themeIndex)
	if err := validateProjectObjectFields(location, packageConfig, "output"); err != nil {
		return "", err
	}
	rawOutput, exists := packageConfig["output"]
	if !exists {
		return "", nil
	}
	output, ok := rawOutput.(string)
	if !ok || strings.TrimSpace(output) == "" {
		return "", ProjectError{Msg: fmt.Sprintf("nf.json wordpress.themes[%d].package.output must be a non-empty string", themeIndex)}
	}
	return strings.TrimSpace(output), nil
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
			return ProjectError{Msg: fmt.Sprintf("theme bootstrap needs task %q in nf.json", step)}
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

func loadEnvConfig(root string, metadata *projectMetadata) (envConfig, bool) {
	projectSlug := firstNonEmpty(metadata.Project.Slug, "project")
	themes, err := loadWordPressThemeSpecs(metadata)
	if err != nil {
		return envConfig{}, false
	}
	activeThemeSlug := activeWordPressThemeSlug(themes)
	themePath := ""
	if repoTheme, ok := repoWordPressThemeSpec(themes); ok {
		if sourceDir, err := repoThemeSourceDir(root, repoTheme); err == nil {
			themePath = sourceDir
		}
	}
	themeMountSlug := activeThemeSlug
	themeSlug := activeThemeSlug
	local := project.Local{}
	if metadata.Local != nil {
		local = *metadata.Local
	}
	ports := project.Ports{}
	if local.Ports != nil {
		ports = *local.Ports
	}
	return envConfig{
		ProjectSlug:      projectSlug,
		PasswordVersion:  projectPasswordVersion(metadata),
		RepoRoot:         root,
		ThemePath:        themePath,
		EnvDir:           config.EnvDir(projectSlug),
		WordpressPort:    firstEnvPort(ports.WordPress, "wordpress", projectSlug),
		MailpitPort:      firstEnvPort(ports.Mailpit, "mailpit", projectSlug),
		AdminerPort:      firstEnvPort(ports.DB, "db", projectSlug),
		Compose:          firstNonEmpty(local.Compose, "docker compose"),
		WordpressService: firstNonEmpty(local.WordPressService, "wordpress"),
		ThemeMountSlug:   themeMountSlug,
		UploadsPath:      firstNonEmpty(local.UploadsPath, "uploads"),
		ThemeSlug:        themeSlug,
		AdminUser:        local.AdminUser,
		Themes:           themes,
		RepoThemeMounts:  repoThemeMountsFromMetadata(root, metadata),
		RepoPluginMounts: repoPluginMountsFromMetadata(root, metadata),
	}, true
}

func firstEnvPort(port int, name, projectSlug string) int {
	derivedWordpress, derivedMailpit, derivedDB := envDerivedPorts(projectSlug)
	derived := derivedWordpress
	switch name {
	case "mailpit":
		derived = derivedMailpit
	case "db":
		derived = derivedDB
	}
	if port == 0 {
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
		"up":    {Description: "Start the managed env, install WordPress if missing, and ensure configured themes are active", Run: envCommandRunner{name: "up", cfg: cfg}},
		"down":  {Description: "Stop the managed env", Run: envCommandRunner{name: "down", cfg: cfg}},
		"logs":  {Description: "Tail WordPress logs", Run: envCommandRunner{name: "logs", cfg: cfg}},
		"reset": {Description: "Destroy and recreate the local env", Run: envCommandRunner{name: "reset", cfg: cfg}},
		"shell": {Description: "Open a shell in the WordPress container", Run: envCommandRunner{name: "shell", cfg: cfg}},
		"wp":    {Description: "Run WP-CLI passthrough", Run: envCommandRunner{name: "wp", cfg: cfg}},
	}
}

func discoverProjectRootOrError() (string, error) {
	if root, ok := config.DiscoverProjectRoot(""); ok {
		return root, nil
	}
	return "", ProjectError{Msg: "No project metadata found above the current directory. Add a version 2 nf.json next to .git."}
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
		fmt.Fprintln(os.Stderr, "No local theme tasks configured. Add tasks to the repo theme in nf.json wordpress.themes.")
		return 1
	}
	fmt.Println("Theme tasks:")
	for _, line := range formatProjectTaskLines(tasks) {
		fmt.Printf("  %s\n", line)
	}
	return 0
}

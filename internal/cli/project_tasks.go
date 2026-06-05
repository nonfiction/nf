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
	"github.com/nonfiction/nf/internal/theme"
)

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
				return "", "", nil, ProjectError{Msg: fmt.Sprintf("nf.json tasks.%s.run must be a string or array of strings", name)}
			}
			parts = append(parts, s)
		}
		return name, strings.Join(parts, " "), argvCommandRunner(parts), nil
	case map[string]any:
		desc, _ := typed["description"].(string)
		if strings.TrimSpace(desc) == "" {
			return "", "", nil, ProjectError{Msg: fmt.Sprintf("nf.json tasks.%s must include a description string", name)}
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
					return "", "", nil, ProjectError{Msg: fmt.Sprintf("nf.json tasks.%s.run must be a string or array of strings", name)}
				}
				parts = append(parts, s)
			}
			return name, desc, argvCommandRunner(parts), nil
		default:
			return "", "", nil, ProjectError{Msg: fmt.Sprintf("nf.json tasks.%s.run must be a string or array of strings", name)}
		}
	default:
		return "", "", nil, ProjectError{Msg: fmt.Sprintf("nf.json tasks.%s must be a string, array, or object", name)}
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
	value, ok := metadata["remotes"]
	if !ok {
		if !create {
			return nil, nil
		}
		remotes := map[string]any{}
		metadata["remotes"] = remotes
		return remotes, nil
	}
	remotes, ok := value.(map[string]any)
	if !ok || remotes == nil {
		return nil, ProjectError{Msg: "nf.json remotes must be an object"}
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

func loadEnvConfig(root string, metadata map[string]any) (envConfig, bool) {
	raw, ok := metadata["env"].(map[string]any)
	if !ok || raw == nil {
		return envConfig{}, false
	}
	projectSlug := firstNonEmpty(mapStringAtPath(metadata, "project", "slug"), "project")
	themePath := firstNonEmpty(mapStringAtPath(metadata, "wordpress", "theme_path"), "theme")
	if !filepath.IsAbs(themePath) {
		themePath = filepath.Join(root, themePath)
	}
	wordpress := mapMapAtPath(metadata, "wordpress")
	return envConfig{
		ProjectSlug:      projectSlug,
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
		ThemeSlug:        firstNonEmpty(recordValueString(wordpress["theme_slug"]), projectSlug, "theme"),
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
	return "", ProjectError{Msg: "No project metadata found above the current directory. Add nf.json with env metadata or tasks.<name>."}
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
		fmt.Fprintln(os.Stderr, "No local theme tasks configured. Add nf.json tasks.<name>.")
		return 1
	}
	fmt.Println("Theme tasks:")
	for _, line := range formatProjectTaskLines(tasks) {
		fmt.Printf("  %s\n", line)
	}
	return 0
}

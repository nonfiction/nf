package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/project"
)

func currentNterchangeProject() (string, *projectMetadata, bool) {
	root, ok := currentNFProjectRoot()
	if !ok {
		return "", nil, false
	}
	metadata, err := project.Load(root)
	if err != nil || metadata.Nterchange == nil {
		return "", nil, false
	}
	return root, metadata, true
}

func cmdInitNterchange(slug string) int {
	root, ok := currentGitRoot()
	if !ok {
		fmt.Fprintln(os.Stderr, "nterchange init requires an existing Git repository")
		return 1
	}
	if _, err := os.Lstat(config.ProjectFile(root)); !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "nf.json already exists or cannot be inspected; it was not changed")
		return 1
	}
	if !fileExists(filepath.Join(root, "compose.yaml")) {
		fmt.Fprintln(os.Stderr, "nterchange init requires the site's existing compose.yaml")
		return 1
	}
	metadata := &projectMetadata{Version: project.ManifestVersion, Project: project.Project{Slug: slug}, Nterchange: &project.Nterchange{}}
	if err := saveProjectMetadata(root, metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Created nterchange nf.json for %s. Existing Compose files and data were not changed.\n", slug)
	return 0
}

func maybeRunNterchangeEnv(argv []string) (int, bool) {
	root, metadata, ok := currentNterchangeProject()
	if !ok {
		return 0, false
	}
	if err := validateProjectMetadata(metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1, true
	}
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		printCommandHelp("env", []helpLine{
			{"up", "start the local env"},
			{"down", "stop the local env"},
			{"show", "show paths, ports, and URLs"},
			{"logs", "tail local application logs"},
			{"shell, sh", "open a local application shell"},
			{},
			{"pull <remote>", "pull SQL and media into the local env"},
			{},
			{"package --ref <full-commit-id>", "stage reviewed code and locked production dependencies"},
			{"deploy <remote> --package <directory> [--execute --yes]", "activate a verified package with app-only health recovery"},
			{"rollback <remote> [--execute --yes]", "restore the previous code release, leaving data untouched"},
		}, helpSection{"Up Options", []helpLine{
			{"--rebuild", "rebuild the local application image"},
		}}, helpSection{"Pull Options", []helpLine{
			{"--dry-run", "show the pull plan without connections"},
			{"--check", "verify source and local runtime read-only"},
			{"--execute", "replace local SQL/media with a safety copy"},
			{"--yes", "skip replacement confirmation"},
			{"--non-interactive", "do not prompt"},
		}})
		fmt.Println("\nDeploy/rollback default to connection-free plans. Logs and shell are local only.")
		fmt.Println("Up uses existing site images and adds local AdminNeo; --rebuild builds app first. Reset, push, snapshots and password are not available.")
		return 0, true
	}
	if argv[0] == "deploy" || argv[0] == "rollback" {
		return cmdNterchangeRelease(root, metadata, argv[0], argv[1:]), true
	}
	if argv[0] == "package" {
		return cmdNterchangePackage(root, metadata, argv[1:]), true
	}
	if argv[0] == "pull" {
		return cmdNterchangePull(root, metadata, argv[1:]), true
	}
	switch name := cliCommandAlias(argv[0]); name {
	case "show", "up", "down", "logs", "shell":
		return cmdNterchangeLocalEnv(root, metadata, name, argv[1:]), true
	default:
		fmt.Fprintln(os.Stderr, "This nterchange env operation is not implemented; no state was changed. Use nf env help for available commands.")
		return 1, true
	}
}

func renderNterchangeEnvInfo(root string, metadata *projectMetadata, cfg nterchangeComposeConfig, includeDetails bool) string {
	title := metadata.Project.Slug + ":local"
	lines := []string{title, strings.Repeat("─", len(title))}
	lines = append(lines, detailRowLines([]detailRow{
		{label: "Site", value: metadata.Project.Slug},
		{label: "Env", value: "local"},
		{label: "Application", value: "nterchange"},
		{label: "Path", value: root},
		{label: "Compose", value: cfg.Name},
		{label: "File", value: filepath.Join(root, "compose.yaml")},
		{label: "Services", value: strings.Join(cfg.serviceNames(), ", ")},
	}, 0)...)
	if !includeDetails {
		return strings.Join(lines, "\n")
	}
	var dbRows, emailRows, siteRows, portRows []detailRow
	for _, row := range cfg.portRows() {
		switch row.label {
		case "DB URL":
			dbRows = append(dbRows, row)
		case "Mailpit URL":
			emailRows = append(emailRows, row)
		case "Site URL":
			siteRows = append(siteRows, row)
		default:
			portRows = append(portRows, row)
		}
	}
	runtimeRows := []detailRow{{label: "Var", value: filepath.Join(root, "var")}}
	mediaRows := []detailRow{}
	for _, media := range metadata.Nterchange.MediaPaths() {
		mediaRows = append(mediaRows, detailRow{label: "Path", value: filepath.Join(root, media)})
	}
	names := make([]string, 0, len(metadata.Remotes))
	for name := range metadata.Remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	remoteRows := []detailRow{}
	for _, name := range names {
		remoteRows = append(remoteRows, detailRow{label: name, value: metadata.Remotes[name]})
	}
	width := detailRowsWidth(dbRows, emailRows, siteRows, portRows, runtimeRows, mediaRows, remoteRows)
	for _, section := range []struct {
		title string
		rows  []detailRow
	}{
		{"Database", dbRows}, {"Email", emailRows}, {"nterchange", siteRows}, {"Ports", portRows},
	} {
		if hasDetailRows(section.rows) {
			lines = append(lines, "", section.title)
			lines = append(lines, detailRowLinesWithWidth(section.rows, 2, width)...)
		}
	}
	lines = append(lines, "", "Runtime")
	lines = append(lines, detailRowLinesWithWidth(runtimeRows, 2, width)...)
	lines = append(lines, "", "Media (excluded from code deployment)")
	lines = append(lines, detailRowLinesWithWidth(mediaRows, 2, width)...)
	if hasDetailRows(remoteRows) {
		lines = append(lines, "", "Remotes")
		lines = append(lines, detailRowLinesWithWidth(remoteRows, 2, width)...)
	}
	return strings.Join(lines, "\n")
}

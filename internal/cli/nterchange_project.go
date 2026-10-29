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
		printGroupHelp("env", []helpLine{
			{"show", "inspect existing local Compose paths and repo remotes"},
			{"package --ref <full-commit-id>", "stage reviewed code and locked production dependencies"},
			{"deploy <remote> --package <directory> [--execute --yes]", "activate a verified package with app-only health recovery"},
			{"rollback <remote> [--execute --yes]", "restore the previous code release, leaving data untouched"},
			{"pull <remote> --dry-run", "preview a data pull without connections"},
			{"pull <remote> --check", "verify source and local runtime read-only"},
			{"pull <remote> --execute [--yes]", "replace local SQL/media with a safety copy"},
		})
		fmt.Println("\nDeploy/rollback default to connection-free plans. Local container lifecycle is not available yet.")
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
	if len(argv) != 1 || argv[0] != "show" {
		fmt.Fprintln(os.Stderr, "This nterchange env operation is not implemented; no state was changed. Use nf env show for the current configuration.")
		return 1, true
	}
	fmt.Println(renderNterchangeEnvInfo(root, metadata))
	return 0, true
}

func renderNterchangeEnvInfo(root string, metadata *projectMetadata) string {
	title := metadata.Project.Slug + ":local"
	lines := []string{title, strings.Repeat("─", len(title))}
	lines = append(lines, detailRowLines([]detailRow{
		{label: "Site", value: metadata.Project.Slug},
		{label: "Env", value: "local"},
		{label: "Application", value: "nterchange"},
		{label: "Path", value: root},
		{label: "Compose", value: filepath.Join(root, "compose.yaml")},
		{label: "Services", value: "app, db"},
	}, 0)...)
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
	width := detailRowsWidth(runtimeRows, mediaRows, remoteRows)
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

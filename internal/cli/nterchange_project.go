package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

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
		printGroupHelp("env (nterchange)", []helpLine{
			{"show", "inspect existing local Compose paths and repo remotes"},
			{"pull <remote> --dry-run", "preview a data pull without connections"},
			{"pull <remote> --check", "verify source and local runtime read-only"},
			{"pull <remote> --execute [--yes]", "replace local SQL/media with a safety copy"},
		})
		fmt.Println("\nCode deployment and local container lifecycle are not available yet.")
		return 0, true
	}
	if argv[0] == "pull" {
		return cmdNterchangePull(root, metadata, argv[1:]), true
	}
	if len(argv) != 1 || argv[0] != "show" {
		fmt.Fprintln(os.Stderr, "This nterchange env operation is not implemented; no state was changed. Use nf env show for the current configuration.")
		return 1, true
	}
	fmt.Printf("Project: %s\nApplication: nterchange\nCompose: %s\nServices: app, db\nRuntime state: %s\n", metadata.Project.Slug, filepath.Join(root, "compose.yaml"), filepath.Join(root, "var"))
	fmt.Println("Media (excluded from code deployment):")
	for _, media := range metadata.Nterchange.MediaPaths() {
		fmt.Printf("  %s\n", filepath.Join(root, media))
	}
	fmt.Println("Remotes:")
	names := make([]string, 0, len(metadata.Remotes))
	for name := range metadata.Remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Printf("  %s -> %s\n", name, metadata.Remotes[name])
	}
	return 0, true
}

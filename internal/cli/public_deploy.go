package cli

// Static public artifact deploy.
//
// Public deploys copy explicit, configured repo directories to URL paths under
// a remote WordPress document root. They are separate from theme releases.

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type publicDeployOptions struct {
	DryRun bool
	Yes    bool
}

type publicDeployPath struct {
	Source    string
	URLPath   string
	RemoteDir string
	Delete    bool
	FileCount int
}

func parsePublicDeployArgs(args []string) (string, publicDeployOptions, bool) {
	var opts publicDeployOptions
	positionals := make([]string, 0, 1)
	for _, arg := range args {
		switch arg {
		case "--dry-run":
			opts.DryRun = true
		case "--yes":
			opts.Yes = true
		default:
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(os.Stderr, "unknown public deploy flag: %s\n", arg)
				return "", opts, false
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 1 {
		fmt.Fprintln(os.Stderr, "public deploy takes at most one remote")
		return "", opts, false
	}
	if len(positionals) == 1 {
		return positionals[0], opts, true
	}
	return "", opts, true
}

func cmdPublicDeploy(remoteName string, opts publicDeployOptions) int {
	remoteName = strings.TrimSpace(remoteName)
	if remoteName == "" {
		fmt.Fprintln(os.Stderr, "public deploy requires a non-empty remote")
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
	target, err := resolveThemeDeployTarget(remoteName, "theme", metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	paths, err := loadPublicDeployPaths(root, metadata, target.WordPressPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "No public paths configured. Add nf.json public.paths[].")
		return 1
	}
	needsYes := false
	for _, item := range paths {
		if item.Delete {
			needsYes = true
			break
		}
	}
	if needsYes && !opts.DryRun && !opts.Yes {
		fmt.Fprintln(os.Stderr, "public deploy with delete=true requires --yes")
		return 1
	}

	fmt.Println("Public deploy plan:")
	fmt.Printf("  remote:      %s\n", target.RemoteName)
	fmt.Printf("  site:        %s\n", target.SiteID)
	fmt.Printf("  env:         %s\n", target.Env)
	fmt.Printf("  provider:    %s\n", target.Provider)
	if target.URL != "" {
		fmt.Printf("  url:         %s\n", target.URL)
	}
	if opts.DryRun {
		fmt.Println("  mode:        dry-run")
	}
	for _, item := range paths {
		fmt.Println("  path:")
		fmt.Printf("    source:    %s\n", item.Source)
		fmt.Printf("    url path:  %s\n", item.URLPath)
		fmt.Printf("    remote:    %s\n", item.RemoteDir)
		fmt.Printf("    files:     %d\n", item.FileCount)
		fmt.Printf("    delete:    %t\n", item.Delete)
	}

	for _, item := range paths {
		mkdirArgs := publicDeployMkdirArgs(target, item.RemoteDir)
		printCommandArgs(mkdirArgs)
		rsyncArgs := publicDeployRsyncArgs(target, item)
		printCommandArgs(rsyncArgs)
		if opts.DryRun {
			continue
		}
		if err := runSSHCommandFn(mkdirArgs); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := runRsyncCommandFn(rsyncArgs); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if opts.DryRun {
		fmt.Println("No remote files were changed.")
	} else {
		fmt.Println("Public files deployed.")
	}
	return 0
}

func loadPublicDeployPaths(root string, metadata map[string]any, docroot string) ([]publicDeployPath, error) {
	rawPublic, ok := metadata["public"].(map[string]any)
	if !ok || rawPublic == nil {
		return nil, nil
	}
	rawPaths, ok := rawPublic["paths"].([]any)
	if !ok {
		return nil, ProjectError{Msg: "nf.json public.paths must be an array"}
	}
	paths := make([]publicDeployPath, 0, len(rawPaths))
	for index, raw := range rawPaths {
		item, ok := raw.(map[string]any)
		if !ok || item == nil {
			return nil, ProjectError{Msg: fmt.Sprintf("nf.json public.paths[%d] must be an object", index)}
		}
		source := strings.TrimSpace(recordValueString(item["source"]))
		urlPath := strings.TrimSpace(recordValueString(item["path"]))
		if source == "" || urlPath == "" {
			return nil, ProjectError{Msg: fmt.Sprintf("nf.json public.paths[%d] requires source and path", index)}
		}
		absSource, err := publicDeploySourcePath(root, source)
		if err != nil {
			return nil, ProjectError{Msg: fmt.Sprintf("nf.json public.paths[%d] source: %s", index, err)}
		}
		cleanURLPath, err := validatePublicDeployURLPath(urlPath)
		if err != nil {
			return nil, ProjectError{Msg: fmt.Sprintf("nf.json public.paths[%d] path: %s", index, err)}
		}
		count, err := countPublicDeployFiles(absSource)
		if err != nil {
			return nil, ProjectError{Msg: fmt.Sprintf("nf.json public.paths[%d] source: %s", index, err)}
		}
		paths = append(paths, publicDeployPath{
			Source:    absSource,
			URLPath:   cleanURLPath,
			RemoteDir: path.Join(docroot, strings.TrimPrefix(cleanURLPath, "/")),
			Delete:    publicDeployBool(item["delete"]),
			FileCount: count,
		})
	}
	return paths, nil
}

func publicDeployBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "y", "on":
			return true
		}
	}
	return false
}

func publicDeploySourcePath(root, source string) (string, error) {
	if filepath.IsAbs(source) {
		return "", fmt.Errorf("must be relative to the repo")
	}
	clean := filepath.Clean(source)
	if clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("must stay inside the repo")
	}
	abs := filepath.Join(root, clean)
	linkInfo, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("directory does not exist: %s", abs)
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("must not be a symlink: %s", abs)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("directory does not exist: %s", abs)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("must be a directory: %s", abs)
	}
	return abs, nil
}

func validatePublicDeployURLPath(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\\") {
		return "", fmt.Errorf("must not contain NUL or backslash")
	}
	if !strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("must start with /")
	}
	for _, segment := range strings.Split(strings.TrimPrefix(value, "/"), "/") {
		if segment == ".." {
			return "", fmt.Errorf("must not contain ..")
		}
	}
	clean := path.Clean(value)
	if clean == "/" || clean == "." {
		return "", fmt.Errorf("must not be /")
	}
	first := strings.TrimPrefix(clean, "/")
	if slash := strings.IndexByte(first, '/'); slash >= 0 {
		first = first[:slash]
	}
	switch strings.ToLower(first) {
	case "wp-admin", "wp-content", "wp-includes", "uploads":
		return "", fmt.Errorf("must not target reserved WordPress path %q", "/"+first)
	}
	return clean, nil
}

func countPublicDeployFiles(source string) (int, error) {
	count := 0
	err := filepath.WalkDir(source, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("must not contain symlinks: %s", path)
		}
		if !d.IsDir() {
			count++
		}
		return nil
	})
	return count, err
}

func publicDeployMkdirArgs(target themeDeployTarget, remoteDir string) []string {
	remoteCommand := "mkdir -p " + shellQuoteArg(remoteDir)
	return []string{"ssh", "-p", target.SSHPort, target.SSHUser + "@" + target.SSHHost, remoteCommand}
}

func publicDeployRsyncArgs(target themeDeployTarget, item publicDeployPath) []string {
	args := []string{"rsync", "-az"}
	if item.Delete {
		args = append(args, "--delete")
	}
	args = append(args, "-e", "ssh -p "+target.SSHPort, item.Source+string(filepath.Separator), target.SSHUser+"@"+target.SSHHost+":"+item.RemoteDir+"/")
	return args
}

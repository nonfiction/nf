package cli

// Project-only command runners for init, env, theme, and top-level help.

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func runInitHelp() int {
	fmt.Println("init")
	fmt.Println("\nUsage:")
	fmt.Println("  nf init [flags]")
	fmt.Println("\nFlags:")
	for _, line := range []string{
		"--project-slug string   project slug (defaults to the current git root name)",
		"--theme-slug string     mounted theme slug",
		"--theme-source string   theme source directory",
		"--type string           project type (default wordpress-theme)",
		"--force                 overwrite nf.json",
	} {
		fmt.Printf("  %s\n", line)
	}
	return 0
}

func runEnvHelp() int {
	printGroupHelp("env", []helpLine{
		{"show", "show paths, ports, and URLs"},
		{"password", "show admin password only"},
		{"up", "start the local env"},
		{"down", "stop the local env"},
		{"logs", "tail WordPress logs"},
		{"shell, ssh", "open a shell in the local env"},
		{"wp -- <args>", "run wp-cli in the local env"},
		{"plugins", "manage configured WordPress plugins"},
		{"snapshot", "manage env snapshots"},
		{"pull [remote] [--dry-run] [--execute] [--yes]", "pull database and mutable wp-content from a remote env"},
		{"push [remote] [--dry-run] [--execute] [--yes]", "push database and mutable wp-content to a remote env"},
		{"reset", "destroy and recreate the local env"},
	})
	return 0
}

func runThemeHelp() int {
	lines := []helpLine{
		{"tasks", "list configured theme tasks"},
		{"package [--dry-run] [--source] [--output]", "package theme files"},
		{"deploy <remote> [--dry-run]", "deploy a packaged theme release"},
		{"rollback <remote> [--dry-run]", "roll back to the previous theme release"},
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

func parseEnvRemoteSyncArgs(action string, args []string) (string, envRemoteSyncOptions, bool) {
	var opts envRemoteSyncOptions
	positionals := make([]string, 0, 1)
	for _, arg := range args {
		switch arg {
		case "--dry-run":
			opts.dryRun = true
		case "--execute":
			opts.execute = true
		case "--yes":
			opts.yes = true
		case "--non-interactive":
			opts.nonInteractive = true
		default:
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(os.Stderr, "unknown env %s flag: %s\n", action, arg)
				return "", opts, false
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 1 {
		fmt.Fprintf(os.Stderr, "env %s takes at most one remote\n", action)
		return "", opts, false
	}
	if len(positionals) == 0 {
		return "", opts, true
	}
	return positionals[0], opts, true
}

func runInit(argv []string) int {
	if len(argv) > 0 && (argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h") {
		return runInitHelp()
	}
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	projectSlug := fs.String("project-slug", "", "")
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
	return cmdProjectInit(projectInitArgs{projectSlug: *projectSlug, themeSlug: *themeSlug, themeSource: *themeSource, projectType: *projectType, force: *force})
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
	case "deploy":
		if err := requireProjectContext("theme deploy"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		remote, dryRun, ok := parseThemeDeployArgs(argv[1:])
		if !ok {
			fmt.Fprintln(os.Stderr, "theme deploy takes exactly one remote")
			return 1
		}
		if strings.TrimSpace(remote) == "" {
			selected, err := chooseProjectRemote("deploy theme to")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			remote = selected
		}
		return cmdThemeDeploy(remote, dryRun)
	case "rollback":
		if err := requireProjectContext("theme rollback"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		remote, dryRun, ok := parseThemeDeployArgs(argv[1:])
		if !ok {
			fmt.Fprintln(os.Stderr, "theme rollback takes exactly one remote")
			return 1
		}
		if strings.TrimSpace(remote) == "" {
			selected, err := chooseProjectRemote("roll theme back on")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			remote = selected
		}
		return cmdThemeRollback(remote, dryRun)
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

func runEnv(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" {
		return runEnvHelp()
	}
	name := argv[0]
	name = cliCommandAlias(name)
	switch name {
	case "show", "password", "up", "down", "logs", "reset", "shell", "wp", "push", "pull", "plugins", "snapshot":
	default:
		fmt.Fprintln(os.Stderr, "unsupported env command")
		return 1
	}
	if name == "snapshot" {
		return runEnvSnapshot(argv[1:])
	}
	if name == "plugins" {
		return runEnvPlugins(argv[1:])
	}
	if name == "show" && len(argv) != 1 {
		fmt.Fprintln(os.Stderr, "env show takes no arguments")
		return 1
	}
	if name == "password" && len(argv) != 1 {
		fmt.Fprintln(os.Stderr, "env password takes no arguments")
		return 1
	}
	if name == "shell" && len(argv) != 1 {
		fmt.Fprintln(os.Stderr, "env shell takes no arguments")
		return 1
	}
	if name == "push" || name == "pull" {
		remoteName, opts, ok := parseEnvRemoteSyncArgs(name, argv[1:])
		if !ok {
			return 1
		}
		argv = []string{name, remoteName}
		if opts.dryRun {
			argv = append(argv, "--dry-run")
		}
		if opts.execute {
			argv = append(argv, "--execute")
		}
		if opts.yes {
			argv = append(argv, "--yes")
		}
		if opts.nonInteractive {
			argv = append(argv, "--non-interactive")
		}
	}
	if name == "up" {
		if err := ensureEnvProjectMetadata(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if err := requireProjectContext("env " + name); err != nil {
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
	cfg, ok := loadEnvConfig(root, metadata)
	if !ok {
		fmt.Fprintln(os.Stderr, "Missing env metadata in nf.json. Run nf env up first.")
		return 1
	}
	if name == "show" {
		credentialCfg, err := envConfigWithAdminCredentials(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(renderEnvInfo(credentialCfg, true))
		return 0
	}
	if name == "password" {
		return cmdEnvPassword(cfg)
	}
	if name == "push" || name == "pull" {
		remoteName, opts, ok := parseEnvRemoteSyncArgs(name, argv[1:])
		if !ok {
			return 1
		}
		return cmdEnvRemoteSyncPlan(name, remoteName, cfg, metadata, opts)
	}
	if name == "up" {
		if err := preflightEnvPorts(cfg); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	extraArgs := argv[1:]
	if name == "wp" {
		extraArgs = normalizePassthroughArgs(extraArgs)
	}
	if err := (envCommandRunner{name: name, cfg: cfg}).Execute(root, extraArgs); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	switch name {
	case "up":
		fmt.Println("Env started.")
		fmt.Println()
		credentialCfg, err := envConfigWithAdminCredentials(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(renderEnvInfo(credentialCfg, true))
	case "reset":
		fmt.Println("Env reset.")
		fmt.Println()
		credentialCfg, err := envConfigWithAdminCredentials(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(renderEnvInfo(credentialCfg, true))
	case "down":
		fmt.Println("Env stopped.")
		fmt.Println()
		fmt.Println(renderEnvInfo(cfg, false))
	}
	return 0
}

func runEnvPlugins(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" {
		printGroupHelp("env plugins", []helpLine{
			{"list, ls", "list configured WordPress plugins"},
			{"add <plugin> [--source <source>] [--no-activate] [--no-auto-update]", "add a WordPress plugin to nf.json"},
			{"remove, rm <plugin>", "remove a WordPress plugin from nf.json"},
			{"status [remote]", "show configured WordPress plugin status"},
			{"diff [remote]", "show configured WordPress plugin drift"},
			{"install [remote] [--dry-run] [--yes]", "install and activate configured WordPress plugins"},
		})
		return 0
	}
	cmd := cliCommandAlias(argv[0])
	args := argv[1:]
	var addOpts envPluginAddOptions
	var installOpts envPluginInstallOptions
	remoteName := ""
	removeSlug := ""
	switch cmd {
	case "list":
		if len(args) != 0 {
			fmt.Fprintf(os.Stderr, "env plugins %s takes no arguments\n", cmd)
			return 1
		}
	case "status", "diff":
		if len(args) > 1 {
			fmt.Fprintf(os.Stderr, "env plugins %s takes at most one remote\n", cmd)
			return 1
		}
		if len(args) == 1 {
			if strings.HasPrefix(args[0], "-") {
				fmt.Fprintf(os.Stderr, "unknown env plugins %s flag: %s\n", cmd, args[0])
				return 1
			}
			remoteName = args[0]
		}
	case "add":
		var ok bool
		addOpts, ok = parseEnvPluginAddArgs(args)
		if !ok {
			return 1
		}
	case "remove":
		var ok bool
		removeSlug, ok = parseEnvPluginRemoveArgs(args)
		if !ok {
			return 1
		}
	case "install":
		var ok bool
		installOpts, ok = parseEnvPluginInstallArgs(args)
		if !ok {
			return 1
		}
	default:
		fmt.Fprintln(os.Stderr, "unsupported env plugins command")
		return 1
	}
	if err := requireProjectContext("env plugins " + cmd); err != nil {
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
	if cmd == "list" {
		return cmdEnvPluginsList(metadata)
	}
	if cmd == "add" {
		return cmdEnvPluginsAdd(root, metadata, addOpts)
	}
	if cmd == "remove" {
		return cmdEnvPluginsRemove(root, metadata, removeSlug)
	}
	if cmd == "status" {
		return cmdEnvPluginsStatusWithOptions(root, metadata, remoteName)
	}
	if cmd == "diff" {
		return cmdEnvPluginsDiffWithOptions(root, metadata, remoteName)
	}
	return cmdEnvPluginsInstallWithOptions(root, metadata, installOpts)
}

func parseEnvPluginRemoveArgs(args []string) (string, bool) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "env plugins remove requires exactly one plugin slug")
		return "", false
	}
	slug := strings.TrimSpace(args[0])
	if slug == "" || strings.HasPrefix(slug, "-") {
		fmt.Fprintln(os.Stderr, "env plugins remove requires exactly one plugin slug")
		return "", false
	}
	return slug, true
}

func parseEnvPluginAddArgs(args []string) (envPluginAddOptions, bool) {
	opts := envPluginAddOptions{Activate: true, AutoUpdate: true}
	positionals := make([]string, 0, 1)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--source":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "env plugins add --source requires a value")
				return opts, false
			}
			i++
			opts.Source = strings.TrimSpace(args[i])
			if opts.Source == "" {
				fmt.Fprintln(os.Stderr, "env plugins add --source must not be empty")
				return opts, false
			}
		case "--no-activate":
			opts.Activate = false
			opts.HasActivate = true
		case "--no-auto-update":
			opts.AutoUpdate = false
			opts.HasAutoUpdate = true
		default:
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(os.Stderr, "unknown env plugins add flag: %s\n", arg)
				return opts, false
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 1 {
		fmt.Fprintln(os.Stderr, "env plugins add requires exactly one plugin slug")
		return opts, false
	}
	opts.Slug = strings.TrimSpace(positionals[0])
	if opts.Slug == "" {
		fmt.Fprintln(os.Stderr, "env plugins add plugin slug must not be empty")
		return opts, false
	}
	return opts, true
}

func parseEnvPluginInstallArgs(args []string) (envPluginInstallOptions, bool) {
	var opts envPluginInstallOptions
	positionals := make([]string, 0, 1)
	for _, arg := range args {
		switch arg {
		case "--dry-run":
			opts.DryRun = true
		case "--yes":
			opts.Yes = true
		default:
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(os.Stderr, "unknown env plugins install flag: %s\n", arg)
				return opts, false
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 1 {
		fmt.Fprintln(os.Stderr, "env plugins install takes at most one remote")
		return opts, false
	}
	if len(positionals) == 1 {
		opts.RemoteName = positionals[0]
	}
	return opts, true
}

func runEnvSnapshot(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" {
		printGroupHelp("env snapshot", []helpLine{
			{"list, ls", "list env snapshots"},
			{"add [name]", "create an env snapshot"},
			{"import [remote] [--name name]", "import a remote snapshot"},
			{"use [name] [--remote remote] [--name name] [--yes]", "restore an env snapshot"},
			{"remove, rm [name]", "delete an env snapshot"},
			{"prune [--keep N] [--dry-run] [--yes]", "delete old auto snapshots"},
		})
		return 0
	}
	cmd := cliCommandAlias(argv[0])
	args := argv[1:]
	switch cmd {
	case "list":
		if len(args) != 0 {
			fmt.Fprintln(os.Stderr, "env snapshot list takes no arguments")
			return 1
		}
	case "add", "remove":
		if len(args) > 1 {
			fmt.Fprintln(os.Stderr, "env snapshot command takes at most one name")
			return 1
		}
	case "use":
	case "import":
	case "prune":
	default:
		fmt.Fprintln(os.Stderr, "unsupported env snapshot command")
		return 1
	}
	if err := requireProjectContext("env snapshot " + cmd); err != nil {
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
	cfg, ok := loadEnvConfig(root, metadata)
	if !ok {
		fmt.Fprintln(os.Stderr, "Missing env metadata in nf.json. Run nf env up first.")
		return 1
	}
	switch cmd {
	case "list":
		return cmdEnvSnapshotList(cfg)
	case "add":
		name := ""
		if len(args) == 1 {
			name = args[0]
		}
		return cmdEnvSnapshotCreate(cfg, name, false)
	case "import":
		remoteName, localName, err := parseEnvSnapshotImportArgs(args)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return cmdEnvSnapshotImport(cfg, remoteName, localName, false)
	case "use":
		opts, err := parseEnvSnapshotUseArgs(args)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return cmdEnvSnapshotUse(cfg, opts, false)
	case "remove":
		name := ""
		if len(args) == 1 {
			name = args[0]
		}
		return cmdEnvSnapshotDelete(cfg, name, false)
	case "prune":
		opts, err := parseEnvSnapshotPruneArgs(args)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return cmdEnvSnapshotPrune(cfg, opts)
	default:
		return 1
	}
}

func runHelp() int {
	lines := []helpLine{
		{"init", "initialize project metadata"},
		{"provider", "manage provider integrations"},
		{"target", "manage deployable targets"},
		{"site", "manage remote sites and envs"},
	}
	if projectContextAvailable() {
		lines = append(lines,
			helpLine{"remote", "manage repo remotes"},
			helpLine{"env", "manage the local development env"},
			helpLine{"theme", "package files and run theme tasks"},
		)
	}
	lines = append(lines,
		helpLine{"config", "manage global config"},
		helpLine{"password", "derive passwords"},
		helpLine{"completion", "print shell completion scripts"},
		helpLine{"help", "show help"},
	)
	printGroupHelp("nf", lines)
	return 0
}

func projectOnlyCommand(name string) bool {
	switch name {
	case "remote", "theme", "env":
		return true
	default:
		return false
	}
}

func rejectOutsideProject(command string) bool {
	if !projectOnlyCommand(command) {
		return false
	}
	if err := requireProjectContext(command); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return true
	}
	return false
}

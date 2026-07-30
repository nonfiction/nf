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
		"--theme-slug string     WordPress theme slug",
		"--theme-source string   theme source directory",
		"--type string           project type (default wordpress-theme)",
		"--force                 overwrite nf.json",
	} {
		fmt.Printf("  %s\n", line)
	}
	return 0
}

func runEnvHelp() int {
	printCommandHelp("env", []helpLine{
		{"up", "start the local env"},
		{"down", "stop the local env"},
		{"show", "show paths, ports, and URLs"},
		{"password [remote]", "print only a local or remote env password"},
		{"logs [remote]", "tail local or remote WordPress logs"},
		{"shell, sh [remote]", "open a local or remote shell"},
		{"wp -- <args>", "run WP-CLI in the local env"},
		{},
		{"snapshot", "manage env snapshots"},
		{},
		{"pull [remote]", "pull database and mutable wp-content from a remote env"},
		{"push [remote]", "push database and mutable wp-content to a remote env"},
		{"import <source>", "import an external WordPress site into local env"},
		{},
		{"reset", "destroy and recreate the local env"},
	}, helpSection{"Up/Reset Options", []helpLine{
		{"--rebuild", "rebuild the local WordPress image"},
	}}, helpSection{"Password Options", []helpLine{
		{"--wp", "show WordPress admin password"},
		{"--db", "show database password"},
		{"--basicauth", "show basic-auth password"},
	}}, helpSection{"Import Options", []helpLine{
		{"--db <path>", "database dump path"},
		{"--source-url <url>", "source URL for import search-replace"},
		{"--name <name>", "snapshot name for the imported source"},
		{"--dry-run", "show the import plan without making changes"},
		{"--yes", "skip destructive import confirmation"},
	}}, helpSection{"Sync Options", []helpLine{
		{"--dry-run", "show the sync plan without making changes"},
		{"--execute", "apply push or pull"},
		{"--yes", "skip confirmation"},
		{"--non-interactive", "do not prompt"},
	}})
	return 0
}

func runEnvImportHelp() int {
	fmt.Println("env import")
	fmt.Println("\nUsage:")
	fmt.Println("  nf env import <source> [--db path] [--source-url url] [--name name] [--dry-run] [--yes]")
	fmt.Println("\nSource layout:")
	fmt.Println("  source/")
	fmt.Println("    database.sql.gz        # preferred; any *.sql.gz or *.sql is also detected")
	fmt.Println("    wp-content/")
	fmt.Println("      uploads/")
	fmt.Println("      plugins/")
	fmt.Println("      mu-plugins/")
	fmt.Println("      languages/")
	fmt.Println("\nUploads-only layout:")
	fmt.Println("  source/")
	fmt.Println("    database.sql.gz        # preferred; any *.sql.gz or *.sql is also detected")
	fmt.Println("    uploads/")
	fmt.Println("\nnf site export layout:")
	fmt.Println("  export/")
	fmt.Println("    manifest.json")
	fmt.Println("    database.sql.gz")
	fmt.Println("    files/")
	fmt.Println("      wp-content/")
	fmt.Println("        uploads/")
	fmt.Println("        plugins/")
	fmt.Println("        mu-plugins/")
	fmt.Println("        languages/")
	fmt.Println("\nImported paths:")
	fmt.Println("  database, wp-content/uploads, plugins, languages")
	fmt.Println("  skips target-specific wp-content/mu-plugins")
	fmt.Println("\nOptions:")
	printHelpLines([]helpLine{
		{"--db <path>", "database dump path when source does not contain a .sql/.sql.gz file"},
		{"--source-url <url>", "source URL for import search-replace"},
		{"--name <name>", "snapshot name for the imported source"},
		{"--dry-run", "show the import plan only"},
		{"--yes", "confirm destructive local import"},
	})
	return 0
}

func runPluginHelp() int {
	printCommandHelp("plugin", []helpLine{
		{"list, ls", "list configured WordPress plugins"},
		{"status [remote]", "show plugin state against nf.json"},
		{"diff [remote]", "show plugin changes needed to match nf.json"},
		{},
		{"install [remote]", "apply configured plugin installation settings"},
		{},
		{"add <plugin>", "add a WordPress plugin to nf.json"},
		{"remove, rm <plugin>", "remove a WordPress plugin from nf.json"},
	}, helpSection{"Add Options", []helpLine{
		{"--source <source>", "wordpress.org, repo, cache, URL/path, or env-var zip"},
		{"--manual", "check only; never install this plugin"},
		{"--note <note>", "store an install note for humans"},
		{"--no-activate", "install without activating"},
		{"--no-auto-update", "do not enable WordPress auto-updates"},
	}}, helpSection{"Install Options", []helpLine{
		{"--dry-run", "show the remote install plan without making changes"},
		{"--yes", "skip confirmation"},
	}}, helpSection{"Cache Commands", []helpLine{
		{"cache add <plugin> <zip>", "add a plugin zip to the local nf plugin cache"},
		{"cache save <plugin>", "save an installed local plugin to the local nf plugin cache"},
		{"cache list, cache ls", "list cached WordPress plugin zips"},
		{"cache show <plugin>", "show local plugin cache details"},
		{"cache remove, cache rm <plugin>", "remove a plugin from the local nf plugin cache"},
	}})
	return 0
}

func runPluginCacheHelp() int {
	printGroupHelp("plugin cache", []helpLine{
		{"add <plugin> <zip>", "add a plugin zip to the local nf plugin cache"},
		{"save <plugin>", "save an installed local plugin to the local nf plugin cache"},
		{"list, ls", "list cached WordPress plugin zips"},
		{"show <plugin>", "show local plugin cache details"},
		{"remove, rm <plugin>", "remove a plugin from the local nf plugin cache"},
	})
	return 0
}

func runThemeHelp() int {
	lines := []helpLine{
		{"list, ls", "list configured WordPress themes"},
		{"status [remote]", "show theme state against nf.json"},
		{"diff [remote]", "show theme changes needed to match nf.json"},
		{},
		{"install [remote]", "apply configured theme installation settings"},
		{},
		{"add <theme>", "add a WordPress theme to nf.json"},
		{"activate <theme>", "make a configured theme first and active in nf.json"},
		{"remove, rm <theme>", "remove a WordPress theme from nf.json"},
		{},
		{"tasks", "list configured theme tasks"},
		{"package", "package a clean theme artifact"},
		{},
		{"deploy <remote>", "deploy a packaged theme release"},
		{"rollback <remote>", "roll back to the previous theme release"},
	}
	printCommandHelp("theme", lines, helpSection{"Add Options", []helpLine{
		{"--source <source>", "wordpress.org, repo, cache, URL/path, or env-var zip"},
		{"--path <path>", "repo theme source path (default theme with --source repo)"},
		{"--auto-update", "enable WordPress auto-updates for this non-repo theme"},
		{"--note <note>", "store an install note for humans"},
	}}, helpSection{"Install Options", []helpLine{
		{"--dry-run", "show the remote install plan without making changes"},
		{"--yes", "skip confirmation"},
	}}, helpSection{"Cache Commands", []helpLine{
		{"cache add <theme> <zip>", "add a theme zip to the local nf theme cache"},
		{"cache save <theme>", "save an installed local theme to the local nf theme cache"},
		{"cache list, cache ls", "list cached WordPress theme zips"},
		{"cache show <theme>", "show local theme cache details"},
		{"cache remove, cache rm <theme>", "remove a theme from the local nf theme cache"},
	}}, helpSection{"Package Options", []helpLine{
		{"--dry-run", "show package actions without writing a zip"},
		{"--source <path>", "theme source directory"},
		{"--output <path>", "package output path"},
	}}, helpSection{"Deploy Options", []helpLine{
		{"--dry-run", "preview deploy or rollback"},
		{"--restart", "restart Kinsta PHP after deploy"},
	}})
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

func runThemeCacheHelp() int {
	printGroupHelp("theme cache", []helpLine{
		{"add <theme> <zip>", "add a theme zip to the local nf theme cache"},
		{"save <theme>", "save an installed local theme to the local nf theme cache"},
		{"list, ls", "list cached WordPress theme zips"},
		{"show <theme>", "show local theme cache details"},
		{"remove, rm <theme>", "remove a theme from the local nf theme cache"},
	})
	return 0
}

func runAliasHelp() int {
	printCommandHelp("alias", []helpLine{
		{"list, ls", "list configured root aliases"},
		{"status [remote]", "show local or remote alias symlink status"},
		{"sync [remote]", "sync local or remote alias symlinks"},
		{},
		{"add <alias> <target>", "add an alias to nf.json"},
		{"remove, rm <alias>", "remove an alias from nf.json"},
	})
	return 0
}

func runDefineHelp() int {
	printCommandHelp("define", []helpLine{
		{"list, ls", "list configured wp-config.php constants"},
		{"get [name]", "choose or print one configured constant value"},
		{"status [remote]", "show local or remote constant state against nf.json"},
		{"sync [remote]", "apply configured constants to wp-config.php"},
		{},
		{"set", "open an interactive define editor"},
		{"set <name> <value>", "set a literal define in nf.json"},
		{"set <name> --secret", "prompt for an encrypted define value"},
		{"set <name> --secret-stdin", "read an encrypted define value from stdin"},
		{"remove, rm", "choose and remove a define from nf.json"},
		{"remove, rm <name>", "remove a define from nf.json"},
		{},
		{"migrate-env", "migrate project .env and legacy env defines to nf.age"},
		{"rekey", "re-encrypt nf.age for the current age recipient"},
	}, helpSection{"Options", []helpLine{
		{"--for <selector>", "apply the value only for a remote/env selector"},
		{"--dry-run", "preview migration or rekey changes"},
		{"--delete-source", "remove project .env after verified migration"},
		{"--add-recipient <age1...>", "add a recipient before password salt rotation"},
	}})
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
	case "list", "ls":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "theme list takes no arguments")
			return 1
		}
		if err := requireProjectContext("theme list"); err != nil {
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
		return cmdEnvThemesList(metadata)
	case "status", "diff":
		cmd := argv[0]
		if len(argv) > 2 {
			fmt.Fprintf(os.Stderr, "theme %s takes at most one remote\n", cmd)
			return 1
		}
		remoteName := ""
		if len(argv) == 2 {
			if strings.HasPrefix(argv[1], "-") {
				fmt.Fprintf(os.Stderr, "unknown theme %s flag: %s\n", cmd, argv[1])
				return 1
			}
			remoteName = argv[1]
		}
		if err := requireProjectContext("theme " + cmd); err != nil {
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
		if cmd == "status" {
			return cmdEnvThemesStatusWithOptions(root, metadata, remoteName)
		}
		return cmdEnvThemesDiffWithOptions(root, metadata, remoteName)
	case "add":
		addOpts, ok := parseEnvThemeAddArgs(argv[1:])
		if !ok {
			return 1
		}
		if err := requireProjectContext("theme add"); err != nil {
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
		return cmdEnvThemesAdd(root, metadata, addOpts)
	case "activate":
		activateSlug, ok := parseEnvThemeSingleSlugArgs("activate", argv[1:])
		if !ok {
			return 1
		}
		if err := requireProjectContext("theme activate"); err != nil {
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
		return cmdEnvThemesActivate(root, metadata, activateSlug)
	case "remove", "rm":
		removeSlug, ok := parseEnvThemeRemoveArgs(argv[1:])
		if !ok {
			return 1
		}
		if err := requireProjectContext("theme remove"); err != nil {
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
		return cmdEnvThemesRemove(root, metadata, removeSlug)
	case "install":
		installOpts, ok := parseEnvThemeInstallArgs(argv[1:])
		if !ok {
			return 1
		}
		if err := requireProjectContext("theme install"); err != nil {
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
		return cmdEnvThemesInstallWithOptions(root, metadata, installOpts)
	case "cache":
		if len(argv) == 1 || (len(argv) == 2 && (argv[1] == "help" || argv[1] == "--help" || argv[1] == "-h")) {
			return runThemeCacheHelp()
		}
		cacheOpts, ok := parseEnvThemeCacheArgs(argv[1:])
		if !ok {
			return 1
		}
		if err := requireProjectContext("theme cache"); err != nil {
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
		if cacheOpts.Command != "save" {
			return cmdEnvThemesCache(envConfig{}, cacheOpts)
		}
		cfg, ok := loadEnvConfig(root, metadata)
		if !ok {
			fmt.Fprintln(os.Stderr, "Invalid local project metadata in nf.json.")
			return 1
		}
		return cmdEnvThemesCache(cfg, cacheOpts)
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
		remote, dryRun, restart, ok := parseThemeDeployArgs(argv[1:])
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
		return cmdThemeDeploy(remote, dryRun, restart)
	case "rollback":
		if err := requireProjectContext("theme rollback"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		remote, dryRun, restart, ok := parseThemeDeployArgs(argv[1:])
		if !ok || restart {
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

func runAlias(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runAliasHelp()
	}
	cmd := cliCommandAlias(argv[0])
	args := argv[1:]
	remoteName := ""
	addAlias := ""
	addTarget := ""
	removeAlias := ""
	switch cmd {
	case "list":
		if len(args) != 0 {
			fmt.Fprintln(os.Stderr, "alias list takes no arguments")
			return 1
		}
	case "status", "sync":
		if len(args) > 1 {
			fmt.Fprintf(os.Stderr, "alias %s takes at most one remote\n", cmd)
			return 1
		}
		if len(args) == 1 {
			if strings.HasPrefix(args[0], "-") {
				fmt.Fprintf(os.Stderr, "unknown alias %s flag: %s\n", cmd, args[0])
				return 1
			}
			remoteName = args[0]
		}
	case "add":
		if len(args) != 2 || strings.HasPrefix(args[0], "-") || strings.HasPrefix(args[1], "-") {
			fmt.Fprintln(os.Stderr, "alias add requires exactly one alias and one target")
			return 1
		}
		addAlias = args[0]
		addTarget = args[1]
	case "remove":
		if len(args) != 1 || strings.HasPrefix(args[0], "-") {
			fmt.Fprintln(os.Stderr, "alias remove requires exactly one alias")
			return 1
		}
		removeAlias = args[0]
	default:
		fmt.Fprintln(os.Stderr, "unsupported alias command")
		return 1
	}
	if err := requireProjectContext("alias " + cmd); err != nil {
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
	switch cmd {
	case "list":
		return cmdAliasesList(metadata)
	case "status":
		return cmdAliasesStatusWithOptions(root, metadata, remoteName)
	case "sync":
		return cmdAliasesSyncWithOptions(root, metadata, remoteName)
	case "add":
		return cmdAliasesAdd(root, metadata, addAlias, addTarget)
	case "remove":
		return cmdAliasesRemove(root, metadata, removeAlias)
	default:
		return 1
	}
}

func runEnv(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runEnvHelp()
	}
	name := argv[0]
	name = cliCommandAlias(name)
	switch name {
	case "show", "password", "up", "down", "logs", "reset", "shell", "wp", "push", "pull", "snapshot", "import":
	default:
		fmt.Fprintln(os.Stderr, "unsupported env command")
		return 1
	}
	if name == "snapshot" {
		return runEnvSnapshot(argv[1:])
	}
	var envImportOpts envImportOptions
	if name == "import" {
		if len(argv) == 1 || argv[1] == "help" || argv[1] == "--help" || argv[1] == "-h" {
			return runEnvImportHelp()
		}
		var err error
		envImportOpts, err = parseEnvImportArgs(argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if name == "show" && len(argv) != 1 {
		fmt.Fprintln(os.Stderr, "env show takes no arguments")
		return 1
	}
	rebuild := false
	if name == "up" || name == "reset" {
		positionals := []string{}
		for _, arg := range argv[1:] {
			switch arg {
			case "--rebuild":
				rebuild = true
			default:
				if strings.HasPrefix(arg, "-") {
					fmt.Fprintf(os.Stderr, "unknown env %s flag: %s\n", name, arg)
					return 1
				}
				positionals = append(positionals, arg)
			}
		}
		if len(positionals) > 0 {
			fmt.Fprintf(os.Stderr, "env %s takes no arguments\n", name)
			return 1
		}
		argv = []string{name}
	}
	envPasswordScope := passwordScopeWP
	envPasswordRemote := ""
	if name == "password" {
		positionals, scope, err := parsePasswordScopeFlags(argv[1:], map[string]passwordScope{"--wp": passwordScopeWP, "--db": passwordScopeDB, "--basicauth": passwordScopeBasicAuth}, passwordScopeWP, "env password")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if len(positionals) > 1 {
			fmt.Fprintln(os.Stderr, "env password takes at most one remote")
			return 1
		}
		if len(positionals) == 1 {
			envPasswordRemote = positionals[0]
		}
		envPasswordScope = scope
	}
	if name == "shell" {
		for _, arg := range argv[1:] {
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(os.Stderr, "unknown env shell flag: %s\n", arg)
				return 1
			}
		}
		if len(argv) > 2 {
			fmt.Fprintln(os.Stderr, "env shell takes at most one remote")
			return 1
		}
	}
	if name == "logs" {
		for _, arg := range argv[1:] {
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(os.Stderr, "unknown env logs flag: %s\n", arg)
				return 1
			}
		}
		if len(argv) > 2 {
			fmt.Fprintln(os.Stderr, "env logs takes at most one remote")
			return 1
		}
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
	if _, err := loadWordPressThemeSpecs(metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cfg, ok := loadEnvConfig(root, metadata)
	if !ok {
		fmt.Fprintln(os.Stderr, "Invalid local project metadata in nf.json.")
		return 1
	}
	if name == "show" {
		credentialCfg, err := envConfigWithAdminCredentials(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		remoteRows, err := envRemoteURLRows(metadata)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(renderEnvInfo(envConfigWithLiveAdminUser(credentialCfg), true, remoteRows...))
		return 0
	}
	if name == "password" {
		if envPasswordRemote != "" {
			return cmdEnvRemotePassword(metadata, envPasswordRemote, envPasswordScope)
		}
		return cmdEnvPassword(cfg, envPasswordScope)
	}
	if name == "shell" && len(argv) == 2 {
		remoteName := strings.TrimSpace(argv[1])
		siteID, remoteEnv, ok, err := projectRemoteAlias(metadata, remoteName)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !ok {
			fmt.Fprintf(os.Stderr, "No configured remote matched %q.\n", remoteName)
			return 1
		}
		return cmdSiteRemoteCommandPlan("shell", canonicalEnvID(siteID, remoteEnv), nil)
	}
	if name == "logs" && len(argv) == 2 {
		remoteName := strings.TrimSpace(argv[1])
		siteID, remoteEnv, ok, err := projectRemoteAlias(metadata, remoteName)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !ok {
			fmt.Fprintf(os.Stderr, "No configured remote matched %q.\n", remoteName)
			return 1
		}
		return cmdSiteRemoteCommandPlan("logs", canonicalEnvID(siteID, remoteEnv), nil)
	}
	if name == "push" || name == "pull" {
		remoteName, opts, ok := parseEnvRemoteSyncArgs(name, argv[1:])
		if !ok {
			return 1
		}
		return cmdEnvRemoteSyncPlan(name, remoteName, cfg, metadata, opts)
	}
	if name == "import" {
		return cmdEnvImport(cfg, envImportOpts)
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
	if err := (envCommandRunner{name: name, cfg: cfg, metadata: metadata, rebuild: rebuild}).Execute(root, extraArgs); err != nil {
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
		remoteRows, err := envRemoteURLRows(metadata)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(renderEnvInfo(credentialCfg, true, remoteRows...))
	case "reset":
		fmt.Println("Env reset.")
		fmt.Println()
		credentialCfg, err := envConfigWithAdminCredentials(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		remoteRows, err := envRemoteURLRows(metadata)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(renderEnvInfo(credentialCfg, true, remoteRows...))
	case "down":
		fmt.Println("Env stopped.")
		fmt.Println()
		fmt.Println(renderEnvInfo(cfg, false))
	}
	return 0
}

func runPlugin(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runPluginHelp()
	}
	cmd := cliCommandAlias(argv[0])
	args := argv[1:]
	var addOpts envPluginAddOptions
	var installOpts envPluginInstallOptions
	var cacheOpts envPluginCacheOptions
	remoteName := ""
	removeSlug := ""
	switch cmd {
	case "list":
		if len(args) != 0 {
			fmt.Fprintf(os.Stderr, "plugin %s takes no arguments\n", cmd)
			return 1
		}
	case "status", "diff":
		if len(args) > 1 {
			fmt.Fprintf(os.Stderr, "plugin %s takes at most one remote\n", cmd)
			return 1
		}
		if len(args) == 1 {
			if strings.HasPrefix(args[0], "-") {
				fmt.Fprintf(os.Stderr, "unknown plugin %s flag: %s\n", cmd, args[0])
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
	case "cache":
		if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h")) {
			return runPluginCacheHelp()
		}
		var ok bool
		cacheOpts, ok = parseEnvPluginCacheArgs(args)
		if !ok {
			return 1
		}
	default:
		fmt.Fprintln(os.Stderr, "unsupported plugin command")
		return 1
	}
	if err := requireProjectContext("plugin " + cmd); err != nil {
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
	if cmd == "cache" {
		if cacheOpts.Command != "save" {
			return cmdEnvPluginsCache(envConfig{}, cacheOpts)
		}
		cfg, ok := loadEnvConfig(root, metadata)
		if !ok {
			fmt.Fprintln(os.Stderr, "Invalid local project metadata in nf.json.")
			return 1
		}
		return cmdEnvPluginsCache(cfg, cacheOpts)
	}
	return cmdEnvPluginsInstallWithOptions(root, metadata, installOpts)
}

func parseEnvPluginRemoveArgs(args []string) (string, bool) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "plugin remove requires exactly one plugin slug")
		return "", false
	}
	slug := strings.TrimSpace(args[0])
	if slug == "" || strings.HasPrefix(slug, "-") {
		fmt.Fprintln(os.Stderr, "plugin remove requires exactly one plugin slug")
		return "", false
	}
	return slug, true
}

func parseEnvPluginAddArgs(args []string) (envPluginAddOptions, bool) {
	opts := envPluginAddOptions{Install: true, Activate: true, AutoUpdate: true}
	positionals := make([]string, 0, 1)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--source":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "plugin add --source requires a value")
				return opts, false
			}
			i++
			opts.Source = strings.TrimSpace(args[i])
			if opts.Source == "" {
				fmt.Fprintln(os.Stderr, "plugin add --source must not be empty")
				return opts, false
			}
		case "--manual":
			opts.Install = false
			opts.HasInstall = true
		case "--note":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "plugin add --note requires a value")
				return opts, false
			}
			i++
			opts.Note = strings.TrimSpace(args[i])
			if opts.Note == "" {
				fmt.Fprintln(os.Stderr, "plugin add --note must not be empty")
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
				fmt.Fprintf(os.Stderr, "unknown plugin add flag: %s\n", arg)
				return opts, false
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 1 {
		fmt.Fprintln(os.Stderr, "plugin add requires exactly one plugin slug")
		return opts, false
	}
	opts.Slug = strings.TrimSpace(positionals[0])
	if opts.Slug == "" {
		fmt.Fprintln(os.Stderr, "plugin add plugin slug must not be empty")
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
				fmt.Fprintf(os.Stderr, "unknown plugin install flag: %s\n", arg)
				return opts, false
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 1 {
		fmt.Fprintln(os.Stderr, "plugin install takes at most one remote")
		return opts, false
	}
	if len(positionals) == 1 {
		opts.RemoteName = positionals[0]
	}
	return opts, true
}

func parseEnvPluginCacheArgs(args []string) (envPluginCacheOptions, bool) {
	var opts envPluginCacheOptions
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "plugin cache requires a command")
		return opts, false
	}
	cmd := cliCommandAlias(args[0])
	opts.Command = cmd
	switch cmd {
	case "add":
		if len(args) != 3 {
			fmt.Fprintln(os.Stderr, "plugin cache add requires a plugin slug and zip path")
			return opts, false
		}
		opts.Slug = strings.TrimSpace(args[1])
		opts.Source = strings.TrimSpace(args[2])
		if opts.Slug == "" || opts.Source == "" || strings.HasPrefix(opts.Slug, "-") || strings.HasPrefix(opts.Source, "-") {
			fmt.Fprintln(os.Stderr, "plugin cache add requires a plugin slug and zip path")
			return opts, false
		}
	case "save", "show", "remove":
		if len(args) != 2 {
			fmt.Fprintf(os.Stderr, "plugin cache %s requires exactly one plugin slug\n", cmd)
			return opts, false
		}
		opts.Slug = strings.TrimSpace(args[1])
		if opts.Slug == "" || strings.HasPrefix(opts.Slug, "-") {
			fmt.Fprintf(os.Stderr, "plugin cache %s requires exactly one plugin slug\n", cmd)
			return opts, false
		}
	case "list":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "plugin cache list takes no arguments")
			return opts, false
		}
	default:
		fmt.Fprintln(os.Stderr, "unsupported plugin cache command")
		return opts, false
	}
	return opts, true
}

func parseEnvThemeSingleSlugArgs(command string, args []string) (string, bool) {
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "theme %s requires exactly one theme slug\n", command)
		return "", false
	}
	slug := strings.TrimSpace(args[0])
	if slug == "" || strings.HasPrefix(slug, "-") {
		fmt.Fprintf(os.Stderr, "theme %s requires exactly one theme slug\n", command)
		return "", false
	}
	return slug, true
}

func parseEnvThemeRemoveArgs(args []string) (string, bool) {
	return parseEnvThemeSingleSlugArgs("remove", args)
}

func parseEnvThemeAddArgs(args []string) (envThemeAddOptions, bool) {
	var opts envThemeAddOptions
	positionals := make([]string, 0, 1)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--source":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "theme add --source requires a value")
				return opts, false
			}
			i++
			opts.Source = strings.TrimSpace(args[i])
			if opts.Source == "" {
				fmt.Fprintln(os.Stderr, "theme add --source must not be empty")
				return opts, false
			}
		case "--path":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "theme add --path requires a value")
				return opts, false
			}
			i++
			opts.Path = strings.TrimSpace(args[i])
			if opts.Path == "" {
				fmt.Fprintln(os.Stderr, "theme add --path must not be empty")
				return opts, false
			}
		case "--auto-update":
			opts.AutoUpdate = true
		case "--note":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "theme add --note requires a value")
				return opts, false
			}
			i++
			opts.Note = strings.TrimSpace(args[i])
			if opts.Note == "" {
				fmt.Fprintln(os.Stderr, "theme add --note must not be empty")
				return opts, false
			}
		default:
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(os.Stderr, "unknown theme add flag: %s\n", arg)
				return opts, false
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 1 {
		fmt.Fprintln(os.Stderr, "theme add requires exactly one theme slug")
		return opts, false
	}
	opts.Slug = strings.TrimSpace(positionals[0])
	if opts.Slug == "" {
		fmt.Fprintln(os.Stderr, "theme add theme slug must not be empty")
		return opts, false
	}
	if strings.TrimSpace(opts.Source) == "" {
		opts.Source = "wordpress.org"
	}
	if strings.EqualFold(strings.TrimSpace(opts.Source), wordpressThemeRepoSource) && strings.TrimSpace(opts.Path) == "" {
		opts.Path = "theme"
	}
	return opts, true
}

func parseEnvThemeInstallArgs(args []string) (envThemeInstallOptions, bool) {
	var opts envThemeInstallOptions
	positionals := make([]string, 0, 1)
	for _, arg := range args {
		switch arg {
		case "--dry-run":
			opts.DryRun = true
		case "--yes":
			opts.Yes = true
		default:
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(os.Stderr, "unknown theme install flag: %s\n", arg)
				return opts, false
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 1 {
		fmt.Fprintln(os.Stderr, "theme install takes at most one remote")
		return opts, false
	}
	if len(positionals) == 1 {
		opts.RemoteName = positionals[0]
	}
	return opts, true
}

func parseEnvThemeCacheArgs(args []string) (envThemeCacheOptions, bool) {
	var opts envThemeCacheOptions
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "theme cache requires a command")
		return opts, false
	}
	cmd := cliCommandAlias(args[0])
	opts.Command = cmd
	switch cmd {
	case "add":
		if len(args) != 3 {
			fmt.Fprintln(os.Stderr, "theme cache add requires a theme slug and zip path")
			return opts, false
		}
		opts.Slug = strings.TrimSpace(args[1])
		opts.Source = strings.TrimSpace(args[2])
		if opts.Slug == "" || opts.Source == "" || strings.HasPrefix(opts.Slug, "-") || strings.HasPrefix(opts.Source, "-") {
			fmt.Fprintln(os.Stderr, "theme cache add requires a theme slug and zip path")
			return opts, false
		}
	case "save", "show", "remove":
		if len(args) != 2 {
			fmt.Fprintf(os.Stderr, "theme cache %s requires exactly one theme slug\n", cmd)
			return opts, false
		}
		opts.Slug = strings.TrimSpace(args[1])
		if opts.Slug == "" || strings.HasPrefix(opts.Slug, "-") {
			fmt.Fprintf(os.Stderr, "theme cache %s requires exactly one theme slug\n", cmd)
			return opts, false
		}
	case "list":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "theme cache list takes no arguments")
			return opts, false
		}
	default:
		fmt.Fprintln(os.Stderr, "unsupported theme cache command")
		return opts, false
	}
	return opts, true
}

func runEnvSnapshot(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		printGroupHelp("env snapshot", []helpLine{
			{"list, ls", "list env snapshots"},
			{"add [name]", "create an env snapshot"},
			{"import [remote] [--name name]", "import a remote snapshot"},
			{},
			{"use [name] [--remote remote] [--name name] [--yes]", "restore an env snapshot"},
			{},
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
		fmt.Fprintln(os.Stderr, "Invalid local project metadata in nf.json.")
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
		{"provider", "manage provider integrations"},
		{"target", "manage deployable targets"},
		{"site", "manage remote sites and envs"},
		{"domain", "manage remote env domains"},
		{"password", "manage and derive passwords"},
	}
	if projectContextAvailable() {
		lines = append(lines,
			helpLine{},
			helpLine{"env", "manage the local development env"},
			helpLine{"theme", "manage configured WordPress themes"},
			helpLine{"plugin", "manage configured WordPress plugins"},
			helpLine{"define", "manage configured WordPress constants"},
			helpLine{"alias", "manage root-level WordPress content aliases"},
			helpLine{"remote", "manage repo remotes"},
		)
	}
	lines = append(lines,
		helpLine{},
		helpLine{"init", "initialize project metadata"},
		helpLine{"config", "manage global config"},
		helpLine{"completion", "print shell completion scripts"},
		helpLine{"refresh", "refresh all provider, target, and site caches"},
		helpLine{"version", "show nf version"},
		helpLine{"help", "show help"},
	)
	printGroupHelp("nf", lines)
	return 0
}

func projectOnlyCommand(name string) bool {
	switch name {
	case "remote", "plugin", "theme", "env", "alias", "define":
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

package cli

// Command-group dispatchers for target, remote, site, and project commands.

import (
	"fmt"
	"os"
	"strings"

	"github.com/nonfiction/nf/internal/target"
)

func runTarget(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runTargetHelp()
	}
	argv[0] = cliCommandAlias(argv[0])
	switch argv[0] {
	case "refresh":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "target refresh takes no arguments")
			return 1
		}
		return cmdTargetRefresh()
	case "list":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "target list takes no arguments")
			return 1
		}
		targets, err := cachedTargets()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return cmdListTargets(targets)
	case "show":
		needle, jsonOutput, err := parseTargetShowArgs(argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if needle == "" {
			selected, err := chooseTargetForShow()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			needle = selected
		}
		return cmdShowTarget(needle, jsonOutput)
	case "adminer":
		return runTargetAdminer(argv[1:])
	case "password":
		return runTargetPassword(argv[1:])
	case "add":
		return runTargetAdd(argv[1:])
	case "remove":
		needle, opts, err := parseRemoveTargetArgs(argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if needle == "" {
			if opts.nonInteractive {
				fmt.Fprintln(os.Stderr, "target remove requires a target in non-interactive mode")
				return 1
			}
			selected, err := chooseTargetForRemove()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			needle = selected
		}
		return cmdRemoveTarget(needle, opts.dryRun, opts.execute, opts.yes, opts.nonInteractive)
	default:
		fmt.Fprintln(os.Stderr, "unsupported target command")
		return 1
	}
}

func runTargetAdd(argv []string) int {
	return target.RunAdd(argv, os.Stderr)
}

func runRemote(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runRemoteHelp()
	}
	argv[0] = cliCommandAlias(argv[0])
	if err := requireProjectContext("remote " + argv[0]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	switch argv[0] {
	case "add":
		if len(argv) > 3 {
			fmt.Fprintln(os.Stderr, "remote add takes at most name and env ref")
			return 1
		}
		name := ""
		envRef := ""
		if len(argv) >= 2 {
			name = argv[1]
		} else {
			prompted, err := remotePromptString("Remote name", "", false)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			name = strings.TrimSpace(prompted)
		}
		if len(argv) == 3 {
			envRef = argv[2]
		} else {
			selected, err := selectSiteEnv("Choose a remote env", "")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			envRef = selected
		}
		return cmdRemoteAdd(name, envRef)
	case "remove":
		if len(argv) > 2 {
			fmt.Fprintln(os.Stderr, "remote remove takes at most one name")
			return 1
		}
		name := ""
		if len(argv) == 2 {
			name = argv[1]
		} else {
			selected, err := chooseProjectRemote("remove")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			name = selected
		}
		return cmdRemoteRemove(name)
	case "show":
		if len(argv) > 2 {
			fmt.Fprintln(os.Stderr, "remote show takes at most one name")
			return 1
		}
		name := ""
		if len(argv) == 2 {
			name = argv[1]
		} else {
			selected, err := chooseProjectRemoteOrOnly("show")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			name = selected
		}
		return cmdRemoteShow(name)
	case "list":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "remote list takes no arguments")
			return 1
		}
		return cmdRemoteList()
	default:
		fmt.Fprintln(os.Stderr, "unsupported remote command")
		return 1
	}
}

func runSite(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" {
		return runSiteHelp()
	}
	argv[0] = cliCommandAlias(argv[0])
	switch argv[0] {
	case "add":
		return runSiteAdd(argv[1:])
	case "staging":
		return runSiteStaging(argv[1:])
	case "refresh":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "site refresh takes no arguments")
			return 1
		}
		return cmdSiteRefresh()
	case "list":
		refresh := false
		listArgs := []string{}
		for _, arg := range argv[1:] {
			if arg == "--refresh" {
				refresh = true
				continue
			}
			listArgs = append(listArgs, arg)
		}
		listEnvs, siteID, err := parseSiteListArgs(listArgs)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if refresh {
			if code := cmdSiteRefresh(); code != 0 {
				return code
			}
		}
		if listEnvs {
			return cmdListSiteEnvs(siteID)
		}
		return cmdList("sites")
	case "show":
		needle, jsonOutput, err := parseSiteShowArgs(argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if needle == "" {
			selected, err := chooseSiteForShow()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			needle = selected
		}
		return cmdShowSiteRef(needle, jsonOutput)
	case "shell":
		envRef, ok := parseSiteShellArgs(argv[1:])
		if !ok {
			return 1
		}
		return cmdSiteRemoteCommandPlan("shell", envRef, nil)
	case "wp":
		envRef, command, ok := parseSiteWPArgs(argv[1:])
		if !ok {
			return 1
		}
		return cmdSiteRemoteCommandPlan("wp", envRef, command)
	case "export":
		envRef, opts, err := parseSiteExportArgs(argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return cmdSiteExport(envRef, opts)
	case "snapshot":
		if len(argv) == 2 && (argv[1] == "list" || argv[1] == "ls") {
			return cmdSiteSnapshotList()
		}
		if len(argv) >= 2 && (argv[1] == "remove" || argv[1] == "rm") {
			name, yes, err := parseSiteSnapshotRemoveArgs(argv[2:])
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			return cmdSiteSnapshotRemove(name, yes)
		}
		if len(argv) >= 2 && argv[1] == "prune" {
			opts, err := parseSiteSnapshotPruneArgs(argv[2:])
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			return cmdSiteSnapshotPrune(opts)
		}
		envRef, opts, err := parseSiteSnapshotArgs(argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return cmdSiteSnapshot(envRef, opts)
	case "password":
		positionals, scope, err := parsePasswordScopeFlags(argv[1:], map[string]passwordScope{"--wp": passwordScopeWP, "--db": passwordScopeDB, "--basicauth": passwordScopeBasicAuth}, passwordScopeWP, "site password")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if len(positionals) > 1 {
			fmt.Fprintln(os.Stderr, "site password takes at most one site")
			return 1
		}
		needle := ""
		if len(positionals) == 1 {
			needle = positionals[0]
		}
		if needle == "" {
			selected, err := chooseSiteForPassword()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			needle = selected
		}
		if siteID, _, ok := splitSiteEnvRef(needle); ok && scope != passwordScopeDB {
			fmt.Fprintf(os.Stderr, "site password takes a site, not an env; use %q.\n", siteID)
			return 1
		}
		return cmdSitePassword(needle, scope)
	case "basicauth":
		return runSiteBasicAuth(argv[1:])
	case "remove":
		needle, opts, err := parseRemoveSiteArgs(argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if needle == "" {
			if opts.nonInteractive {
				fmt.Fprintln(os.Stderr, "site remove requires a site in non-interactive mode")
				return 1
			}
			selected, err := chooseSiteForRemove()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			needle = selected
		}
		if siteID, env, ok := splitSiteEnvRef(needle); ok {
			if normalizedRecordString(env) == "staging" {
				fmt.Fprintf(os.Stderr, "Use nf site staging remove %s to delete staging.\n", siteID)
			} else {
				fmt.Fprintf(os.Stderr, "Cannot remove env %q; remove site %q to delete the whole site.\n", env, siteID)
			}
			return 1
		}
		return cmdSiteRemove(needle, opts.dryRun, opts.execute, opts.yes, opts.nonInteractive)
	default:
		fmt.Fprintln(os.Stderr, "unsupported site command")
		return 1
	}
}

func runSiteAdd(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		printGroupHelp("site add", []helpLine{
			{"<target> <site> [flags]", "create a live env"},
			{},
			{"--with-staging", "also create staging"},
			{"--kinsta-slug <slug>", "Kinsta provider slug when it differs from project slug"},
			{"--region <region>", "Kinsta region override"},
			{"--php <version>", "Kinsta PHP version override"},
			{},
			{"--dry-run", "show the plan only"},
			{"--execute", "execute the plan"},
			{"--yes", "confirm execution"},
			{"--non-interactive", "fail instead of prompting"},
		})
		return 0
	}
	args := siteAddArgs{}
	positionals := []string{}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch arg {
		case "--execute":
			args.execute = true
		case "--yes":
			args.yes = true
		case "--non-interactive":
			args.nonInteractive = true
		case "--dry-run":
			args.dryRun = true
		case "--with-staging":
			args.withStaging = true
		case "--kinsta-slug":
			if i+1 >= len(argv) || strings.TrimSpace(argv[i+1]) == "" {
				fmt.Fprintln(os.Stderr, "--kinsta-slug requires a value")
				return 1
			}
			i++
			args.kinstaSlug = argv[i]
		case "--region":
			if i+1 >= len(argv) || strings.TrimSpace(argv[i+1]) == "" {
				fmt.Fprintln(os.Stderr, "--region requires a value")
				return 1
			}
			i++
			args.region = argv[i]
		case "--php":
			if i+1 >= len(argv) || strings.TrimSpace(argv[i+1]) == "" {
				fmt.Fprintln(os.Stderr, "--php requires a value")
				return 1
			}
			i++
			args.phpVersion = argv[i]
		default:
			if strings.HasPrefix(arg, "--kinsta-slug=") {
				args.kinstaSlug = strings.TrimPrefix(arg, "--kinsta-slug=")
				if strings.TrimSpace(args.kinstaSlug) == "" {
					fmt.Fprintln(os.Stderr, "--kinsta-slug requires a value")
					return 1
				}
				continue
			}
			if strings.HasPrefix(arg, "--region=") {
				args.region = strings.TrimPrefix(arg, "--region=")
				if strings.TrimSpace(args.region) == "" {
					fmt.Fprintln(os.Stderr, "--region requires a value")
					return 1
				}
				continue
			}
			if strings.HasPrefix(arg, "--php=") {
				args.phpVersion = strings.TrimPrefix(arg, "--php=")
				if strings.TrimSpace(args.phpVersion) == "" {
					fmt.Fprintln(os.Stderr, "--php requires a value")
					return 1
				}
				continue
			}
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(os.Stderr, "unknown site add flag: %s\n", arg)
				return 1
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 2 {
		fmt.Fprintln(os.Stderr, "site add takes exactly target and site")
		return 1
	}
	args.target = positionals[0]
	args.site = positionals[1]
	if err := validateSiteAddSlug(args.site); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return cmdSiteAdd(args)
}

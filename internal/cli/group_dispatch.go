package cli

// Command-group dispatchers for target, remote, server, site, and provision.

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nonfiction/nf/internal/target"
	"github.com/nonfiction/nf/internal/target/provision"
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

func runServer(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" {
		return runServerHelp()
	}
	argv[0] = cliCommandAlias(argv[0])
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
		return cmdShowServer(needle)
	case "root-password":
		if len(argv) != 2 {
			fmt.Fprintln(os.Stderr, "server root-password takes exactly one identifier")
			return 1
		}
		return cmdServerRootPassword(argv[1])
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
	argv[0] = cliCommandAlias(argv[0])
	switch argv[0] {
	case "add":
		return runSiteAdd(argv[1:])
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
		if len(argv) > 2 {
			fmt.Fprintln(os.Stderr, "site password takes at most one site")
			return 1
		}
		needle := ""
		if len(argv) == 2 {
			needle = argv[1]
		}
		if needle == "" {
			selected, err := chooseSiteForPassword()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			needle = selected
		}
		if siteID, _, ok := splitSiteEnvRef(needle); ok {
			fmt.Fprintf(os.Stderr, "site password takes a site, not an env; use %q.\n", siteID)
			return 1
		}
		return cmdSitePassword(needle)
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
		if siteID, _, ok := splitSiteEnvRef(needle); ok {
			fmt.Fprintf(os.Stderr, "Cannot remove one env; remove site %q to delete live and staging.\n", siteID)
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
			{"<target> <site> [flags]", "create live and staging envs"},
			{"--region <region>", "Kinsta region override"},
			{"--php <version>", "Kinsta PHP version override"},
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
	return cmdSiteAdd(args)
}

func runProvision(argv []string) int {
	fs := flag.NewFlagSet("server provision", flag.ContinueOnError)
	args := provision.Args{}
	fs.StringVar(&args.Provider, "provider", "", "server provider (linode)")
	fs.StringVar(&args.DnsProvider, "dns-provider", "", "DNS provider (dnsimple)")
	fs.StringVar(&args.UbuntuVersion, "ubuntu-version", "", "Ubuntu LTS version to use (26.04, 24.04, 22.04, 20.04)")
	fs.StringVar(&args.Firewall, "firewall", "", "Linode cloud firewall mode (managed or none)")
	fs.StringVar(&args.FirewallID, "firewall-id", "", "existing Linode cloud firewall id")
	fs.StringVar(&args.Name, "name", "", "server name")
	fs.StringVar(&args.Region, "region", "", "Linode region")
	fs.StringVar(&args.Type, "type", "", "Linode type")
	fs.StringVar(&args.Image, "image", "", "advanced Linode image override")
	fs.StringVar(&args.SshUser, "ssh-user", "", "deployment SSH user")
	fs.StringVar(&args.SshKeySource, "ssh-key-source", "", "SSH key source (linode-profile or file)")
	fs.StringVar(&args.SshKeyLabel, "ssh-key-label", "", "filter Linode profile SSH keys by label")
	fs.StringVar(&args.SshKeyID, "ssh-key-id", "", "filter Linode profile SSH keys by id")
	fs.BoolVar(&args.AllLinodeSshKeys, "all-linode-ssh-keys", false, "use all Linode profile SSH keys")
	fs.StringVar(&args.SshPublicKeyFile, "ssh-public-key-file", "", "SSH public key file fallback for --ssh-key-source file")
	fs.StringVar(&args.DnsimpleAccountID, "dnsimple-account-id", "", "DNSimple account ID")
	fs.BoolVar(&args.Wait, "wait", false, "wait for SSH, TLS, and health checks")
	fs.BoolVar(&args.NoWait, "no-wait", false, "skip SSH, TLS, and health checks")
	fs.DurationVar(&args.SshTimeout, "ssh-timeout", 5*time.Minute, "timeout for waiting on SSH port 22")
	fs.DurationVar(&args.CloudInitTimeout, "cloud-init-timeout", 10*time.Minute, "timeout for cloud-init and TLS setup")
	fs.DurationVar(&args.TLSTimeout, "tls-timeout", 5*time.Minute, "timeout budget for TLS setup")
	fs.DurationVar(&args.HealthTimeout, "health-timeout", 2*time.Minute, "timeout for HTTPS health checks")
	fs.StringVar(&args.WriteCloudInit, "write-cloud-init", "", "write cloud-init preview to a file")
	fs.BoolVar(&args.NonInteractive, "non-interactive", false, "")
	fs.BoolVar(&args.ShowCloudInit, "show-cloud-init", false, "show cloud-init preview in the terminal")
	fs.BoolVar(&args.Execute, "execute", false, "execute remote provisioning")
	fs.BoolVar(&args.Yes, "yes", false, "confirm execution in non-interactive mode")
	fs.BoolVar(&args.DryRun, "dry-run", false, "show the plan without executing")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(argv); err != nil {
		return 1
	}
	if args.Provider != "" && args.Provider != "linode" {
		fmt.Fprintln(os.Stderr, "Only --provider linode is supported in this slice.")
		return 1
	}
	if args.DnsProvider != "" && args.DnsProvider != "dnsimple" {
		fmt.Fprintln(os.Stderr, "Only --dns-provider dnsimple is supported in this slice.")
		return 1
	}
	if args.Execute && args.DryRun {
		fmt.Fprintln(os.Stderr, "Choose either --execute or --dry-run, not both.")
		return 1
	}
	if args.Wait && args.NoWait {
		fmt.Fprintln(os.Stderr, "Choose either --wait or --no-wait, not both.")
		return 1
	}
	if !args.Execute {
		args.DryRun = true
	}
	if args.Execute && !args.NoWait {
		args.Wait = true
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

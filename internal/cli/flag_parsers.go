package cli

// Shared flag parsers for destructive and snapshot commands.

import (
	"fmt"
	"strconv"
	"strings"
)

type deleteServerOptions struct {
	dryRun         bool
	execute        bool
	yes            bool
	nonInteractive bool
}

func parseDeleteServerArgs(argv []string) (string, deleteServerOptions, error) {
	var opts deleteServerOptions
	positionals := make([]string, 0, 1)
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch arg {
		case "--":
			positionals = append(positionals, argv[i+1:]...)
			i = len(argv)
		case "--non-interactive":
			opts.nonInteractive = true
		case "--execute":
			opts.execute = true
		case "--yes":
			opts.yes = true
		case "--dry-run":
			opts.dryRun = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", opts, fmt.Errorf("unsupported flag %s", arg)
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 1 {
		return "", opts, fmt.Errorf("server delete takes at most one id or name")
	}
	if len(positionals) == 0 {
		return "", opts, nil
	}
	return positionals[0], opts, nil
}

func parseRemoveTargetArgs(argv []string) (string, deleteServerOptions, error) {
	needle, opts, err := parseDeleteServerArgs(argv)
	if err != nil {
		return "", opts, fmt.Errorf("%s", strings.Replace(err.Error(), "server delete", "target remove", 1))
	}
	return needle, opts, nil
}

func parseRemoveSiteArgs(argv []string) (string, deleteServerOptions, error) {
	needle, opts, err := parseDeleteServerArgs(argv)
	if err != nil {
		return "", opts, fmt.Errorf("%s", strings.Replace(err.Error(), "server delete", "site remove", 1))
	}
	return needle, opts, nil
}

func parseSiteSnapshotArgs(argv []string) (string, siteSnapshotOptions, error) {
	var opts siteSnapshotOptions
	positionals := make([]string, 0, 1)
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch arg {
		case "--dry-run":
			opts.dryRun = true
		case "--output":
			if i+1 >= len(argv) || strings.TrimSpace(argv[i+1]) == "" {
				return "", opts, fmt.Errorf("site snapshot --output requires a path")
			}
			i++
			opts.output = argv[i]
		default:
			if strings.HasPrefix(arg, "--output=") {
				opts.output = strings.TrimPrefix(arg, "--output=")
				if strings.TrimSpace(opts.output) == "" {
					return "", opts, fmt.Errorf("site snapshot --output requires a path")
				}
				continue
			}
			if strings.HasPrefix(arg, "-") {
				return "", opts, fmt.Errorf("unsupported flag %s", arg)
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 1 {
		return "", opts, fmt.Errorf("site snapshot takes at most one env ref")
	}
	if len(positionals) == 0 {
		return "", opts, nil
	}
	return positionals[0], opts, nil
}

func parseSiteExportArgs(argv []string) (string, siteExportOptions, error) {
	var opts siteExportOptions
	positionals := make([]string, 0, 1)
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch arg {
		case "--dry-run":
			opts.dryRun = true
		case "--output":
			if i+1 >= len(argv) || strings.TrimSpace(argv[i+1]) == "" {
				return "", opts, fmt.Errorf("site export --output requires a path")
			}
			i++
			opts.output = argv[i]
		default:
			if strings.HasPrefix(arg, "--output=") {
				opts.output = strings.TrimPrefix(arg, "--output=")
				if strings.TrimSpace(opts.output) == "" {
					return "", opts, fmt.Errorf("site export --output requires a path")
				}
				continue
			}
			if strings.HasPrefix(arg, "-") {
				return "", opts, fmt.Errorf("unsupported flag %s", arg)
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 1 {
		return "", opts, fmt.Errorf("site export takes at most one env ref")
	}
	if len(positionals) == 0 {
		return "", opts, nil
	}
	return positionals[0], opts, nil
}

func parseSiteSnapshotRemoveArgs(argv []string) (string, bool, error) {
	name := ""
	yes := false
	for _, arg := range argv {
		switch arg {
		case "--yes":
			yes = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", yes, fmt.Errorf("unsupported flag %s", arg)
			}
			if name != "" {
				return "", yes, fmt.Errorf("site snapshot remove takes at most one name")
			}
			name = arg
		}
	}
	return name, yes, nil
}

func parseSiteSnapshotPruneArgs(args []string) (envSnapshotPruneOptions, error) {
	opts := envSnapshotPruneOptions{keep: 3}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--dry-run":
			opts.dryRun = true
		case arg == "--yes":
			opts.yes = true
		case arg == "--keep":
			if i+1 >= len(args) {
				return opts, ProjectError{Msg: "site snapshot prune --keep requires a number"}
			}
			i++
			keep, err := strconv.Atoi(args[i])
			if err != nil || keep < 0 {
				return opts, ProjectError{Msg: "site snapshot prune --keep must be 0 or greater"}
			}
			opts.keep = keep
		case strings.HasPrefix(arg, "--keep="):
			keepText := strings.TrimPrefix(arg, "--keep=")
			keep, err := strconv.Atoi(keepText)
			if err != nil || keep < 0 {
				return opts, ProjectError{Msg: "site snapshot prune --keep must be 0 or greater"}
			}
			opts.keep = keep
		default:
			return opts, ProjectError{Msg: fmt.Sprintf("unsupported site snapshot prune option %q", arg)}
		}
	}
	return opts, nil
}

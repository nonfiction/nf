package cli

// Password command dispatch.

import (
	"fmt"
	"os"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/passwords"
)

type passwordDeriveArgs struct {
	scope      string
	identity   string
	version    string
	versionSet bool
}

func runPassword(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runPasswordHelp()
	}
	switch argv[0] {
	case "set-salt":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "password set-salt takes exactly one salt")
			return 1
		}
		if _, err := config.SetEnvFile(config.EnvFile(), map[string]string{"NF_PASSWORD_SALT": argv[1]}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("Password Salt: Set")
		return 0
	case "show-salt":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "password show-salt takes no arguments")
			return 1
		}
		salt, err := passwords.SecretSalt()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("Password Salt: %s\n", maskSecret(salt))
		return 0
	case "derive":
		args, err := parsePasswordDeriveArgs(argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if args.scope == "" {
			selected, err := passwordDeriveSelectFn("Choose a password scope", passwordDeriveScopeOptions())
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			args.scope = selected
		}
		if args.identity == "" {
			prompted, err := passwordDerivePromptString(passwordDeriveIdentityPrompt(args.scope), "", false)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			args.identity = strings.TrimSpace(prompted)
		}
		return cmdPasswordDerive(args.scope, args.identity, args.version, args.versionSet, false)
	default:
		fmt.Fprintln(os.Stderr, "unsupported password command")
		return 1
	}
}

func parsePasswordDeriveArgs(argv []string) (passwordDeriveArgs, error) {
	args := passwordDeriveArgs{}
	positionals := []string{}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch arg {
		case "--password-version":
			if i+1 >= len(argv) || strings.TrimSpace(argv[i+1]) == "" {
				return args, fmt.Errorf("--password-version requires a value")
			}
			i++
			version, err := parseExplicitPasswordVersion(argv[i])
			if err != nil {
				return args, err
			}
			args.version = version
			args.versionSet = true
		default:
			if strings.HasPrefix(arg, "--password-version=") {
				value := strings.TrimPrefix(arg, "--password-version=")
				if strings.TrimSpace(value) == "" {
					return args, fmt.Errorf("--password-version requires a value")
				}
				version, err := parseExplicitPasswordVersion(value)
				if err != nil {
					return args, err
				}
				args.version = version
				args.versionSet = true
				continue
			}
			if strings.HasPrefix(arg, "-") {
				return args, fmt.Errorf("unknown password derive flag: %s", arg)
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 0 {
		args.scope = strings.TrimSpace(positionals[0])
	}
	if len(positionals) > 1 {
		args.identity = strings.TrimSpace(strings.Join(positionals[1:], ":"))
	}
	return args, nil
}

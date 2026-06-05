package cli

// Password command dispatch.

import (
	"fmt"
	"os"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/passwords"
)

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
		if len(argv) < 3 {
			fmt.Fprintln(os.Stderr, "password derive requires a scope and at least one value")
			return 1
		}
		return cmdPasswordDerive(argv[1], strings.Join(argv[2:], ":"), false)
	default:
		fmt.Fprintln(os.Stderr, "unsupported password command")
		return 1
	}
}

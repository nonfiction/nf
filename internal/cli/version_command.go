package cli

import (
	"fmt"
	"os"

	"github.com/nonfiction/nf/internal/version"
)

func runVersion(argv []string) int {
	if len(argv) > 1 {
		fmt.Fprintln(os.Stderr, "version takes at most one flag: --short")
		return 1
	}
	if len(argv) == 1 {
		switch argv[0] {
		case "--short":
			fmt.Println(version.Version)
			return 0
		case "help", "--help", "-h":
			return runVersionHelp()
		default:
			fmt.Fprintf(os.Stderr, "unknown version flag: %s\n", argv[0])
			return 1
		}
	}
	for _, line := range version.Details() {
		fmt.Println(line)
	}
	return 0
}

func runVersionHelp() int {
	printGroupHelp("version", []helpLine{
		{"version", "show version, commit, and build date"},
		{"version --short", "show version only"},
	})
	return 0
}

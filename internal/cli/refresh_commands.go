package cli

// Top-level aggregate refresh command.

import (
	"fmt"
	"os"
	"strings"
)

func runRefresh(argv []string) int {
	if len(argv) > 0 && (argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h") {
		return runRefreshHelp()
	}
	if len(argv) != 0 {
		fmt.Fprintln(os.Stderr, "refresh takes no arguments")
		return 1
	}
	return cmdRefresh()
}

func runRefreshHelp() int {
	fmt.Println("refresh")
	fmt.Println("\nUsage:")
	fmt.Println("  nf refresh")
	fmt.Println("\nRefreshes all configured provider metadata, target records, and site/env records.")
	return 0
}

func cmdRefresh() int {
	fmt.Println("Refresh updates all provider, target, and site caches.")
	failures := []string{}
	fmt.Println("Refreshing providers...")
	for _, status := range providerConfigStatuses() {
		if cmdProviderCheck(status.Name, false) != 0 {
			failures = append(failures, "provider "+status.Name)
		}
	}
	fmt.Println("Refreshing sites...")
	if cmdSiteRefresh() != 0 {
		failures = append(failures, "sites")
	}
	if len(failures) > 0 {
		fmt.Fprintf(os.Stderr, "refresh failed for: %s\n", strings.Join(failures, ", "))
		return 1
	}
	fmt.Println("Refresh complete.")
	return 0
}

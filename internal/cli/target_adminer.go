package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/state"
)

func validateAdminerDefaultUser(user string) error {
	trimmed := strings.TrimSpace(user)
	if trimmed == "" {
		return ProjectError{Msg: "adminer_default_user must be a non-empty MySQL username"}
	}
	if len(trimmed) > 32 {
		return ProjectError{Msg: "adminer_default_user must be 32 characters or fewer"}
	}
	for _, r := range trimmed {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return ProjectError{Msg: "adminer_default_user must use only letters, numbers, underscores, and hyphens"}
	}
	return nil
}

func runTargetAdminer(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runTargetAdminerHelp()
	}
	switch argv[0] {
	case "show":
		if len(argv) > 2 {
			fmt.Fprintln(os.Stderr, "target adminer show takes at most one target")
			return 1
		}
		needle := ""
		if len(argv) == 2 {
			needle = argv[1]
		}
		if needle == "" {
			selected, err := chooseTarget("show adminer for")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			needle = selected
		}
		return cmdTargetAdminerShow(needle)
	default:
		fmt.Fprintf(os.Stderr, "unsupported target adminer command: %s\n", argv[0])
		return 1
	}
}

func cmdTargetAdminerShow(needle string) int {
	targets, err := cachedTargets()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	record := state.MatchingRecord(targets, needle)
	if record == nil {
		fmt.Fprintf(os.Stderr, "No target matched %q.\n", needle)
		return 1
	}
	if provider := strings.ToLower(strings.TrimSpace(recordValueString(record["provider"]))); provider != "linode" {
		fmt.Fprintf(os.Stderr, "Target %q is provider %q; Adminer is only available on linode targets.\n", needle, provider)
		return 1
	}
	remote, err := readLinodeTargetFile(record)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read /var/lib/nf/target.json: %v\n", err)
		return 1
	}
	adminer, ok := remote["adminer"].(map[string]any)
	if !ok {
		fmt.Fprintf(os.Stderr, "Target %q has no Adminer metadata in /var/lib/nf/target.json.\n", needle)
		return 1
	}
	identity := firstNonEmpty(
		mapStringAtPath(remote, "adminer", "auth", "password", "identity"),
		recordValueString(remote["hostname"]),
		firstRecordString(record, "hostname", "host"),
	)
	if identity == "" {
		fmt.Fprintf(os.Stderr, "Target %q is missing Adminer password identity.\n", needle)
		return 1
	}
	url := targetAdminerURL(remote)
	if url == "" {
		fmt.Fprintf(os.Stderr, "Target %q is missing Adminer URL metadata.\n", needle)
		return 1
	}
	user := targetAdminerUser(remote)
	if user == "" {
		fmt.Fprintf(os.Stderr, "Target %q is missing Adminer user metadata.\n", needle)
		return 1
	}
	salt, err := passwords.SecretSalt()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	password := passwords.DerivePassword(identity, "adminer", salt)
	tool := firstNonEmpty(recordValueString(adminer["tool"]), "AdminNeo")
	version := recordValueString(adminer["version"])
	if version != "" {
		tool += " " + version
	}
	fmt.Println("Adminer:")
	fmt.Printf("  url: %s\n", url)
	fmt.Printf("  user: %s\n", user)
	fmt.Printf("  password: %s\n", password)
	fmt.Printf("  database host: %s\n", firstNonEmpty(mapStringAtPath(remote, "adminer", "database", "host"), "localhost"))
	fmt.Printf("  engine: %s\n", tool)
	return 0
}

func targetAdminerURL(record map[string]any) string {
	adminer, ok := record["adminer"].(map[string]any)
	if !ok {
		return ""
	}
	return firstNonEmpty(recordValueString(adminer["url"]), deriveAdminerURLFromHostname(recordValueString(adminer["hostname"])))
}

func targetAdminerUser(record map[string]any) string {
	adminer, ok := record["adminer"].(map[string]any)
	if !ok {
		return ""
	}
	return firstNonEmpty(recordValueString(adminer["user"]), mapStringAtPath(record, "adminer", "auth", "user"))
}

func deriveAdminerURLFromHostname(hostname string) string {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return ""
	}
	return "https://" + hostname + "/"
}

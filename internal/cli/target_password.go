package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/state"
)

func runTargetPassword(argv []string) int {
	positionals, scope, err := parsePasswordScopeFlags(argv, map[string]passwordScope{"--root": passwordScopeRoot, "--db": passwordScopeDB}, passwordScopeRoot, "target password")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(positionals) > 1 {
		fmt.Fprintln(os.Stderr, "target password takes at most one target")
		return 1
	}
	needle := ""
	if len(positionals) == 1 {
		needle = positionals[0]
	}
	if needle == "" {
		selected, err := chooseLinodeTarget("show password for")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		needle = selected
	}
	password, err := targetPassword(needle, scope)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(password)
	return 0
}

func targetPassword(needle string, scope passwordScope) (string, error) {
	targets, err := cachedTargets()
	if err != nil {
		return "", err
	}
	record := state.MatchingRecord(targets, needle)
	if record == nil {
		return "", ProjectError{Msg: fmt.Sprintf("No target matched %q.", needle)}
	}
	if provider := strings.ToLower(strings.TrimSpace(recordValueString(record["provider"]))); provider != "linode" {
		return "", ProjectError{Msg: fmt.Sprintf("Target %q is provider %q; target passwords are only available on linode targets.", needle, provider)}
	}
	salt, err := passwords.SecretSalt()
	if err != nil {
		return "", err
	}
	switch scope {
	case passwordScopeRoot:
		identity := firstRecordString(record, "hostname", "host")
		if identity == "" {
			return "", ProjectError{Msg: fmt.Sprintf("Target %q is missing hostname.", needle)}
		}
		return passwords.DerivePassword(identity, "linode-root", salt), nil
	case passwordScopeDB:
		remote, err := readLinodeTargetFile(record)
		if err != nil {
			return "", fmt.Errorf("read /var/lib/nf/target.json: %w", err)
		}
		identity := firstNonEmpty(
			mapStringAtPath(remote, "db", "auth", "password", "identity"),
			mapStringAtPath(remote, "adminer", "auth", "password", "identity"),
			recordValueString(remote["hostname"]),
			firstRecordString(record, "hostname", "host"),
		)
		if identity == "" {
			return "", ProjectError{Msg: fmt.Sprintf("Target %q is missing database password identity.", needle)}
		}
		purpose := firstNonEmpty(mapStringAtPath(remote, "db", "auth", "password", "purpose"), mapStringAtPath(remote, "adminer", "auth", "password", "purpose"), "db")
		return passwords.DerivePassword(identity, purpose, salt), nil
	default:
		return "", ProjectError{Msg: "unsupported target password scope"}
	}
}

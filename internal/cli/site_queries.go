package cli

// Site show, password, picker, and argument parsing helpers.

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/state"
	"github.com/nonfiction/nf/internal/ui"
)

func cmdServerRootPassword(needle string) int {
	servers, err := state.LoadStateRecords("servers")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	record := state.MatchingRecord(servers, needle)
	if record == nil {
		fmt.Fprintf(os.Stderr, "No server matched %q.\n", needle)
		return 1
	}
	hostname := firstRecordString(record, "hostname")
	if hostname == "" {
		fmt.Fprintf(os.Stderr, "Server %q is missing hostname.\n", needle)
		return 1
	}
	salt, err := passwords.SecretSalt()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	password := passwords.DerivePassword(hostname, "linode-root", salt)
	fmt.Printf("Root password for %s:\n\n%s\n", hostname, password)
	return 0
}

func cmdShowSite(needle string, jsonOutput bool) int {
	resolved, _, projectFileExists, targetAliasUsed, err := resolveSiteTarget(needle)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if siteID, env, ok := splitSiteEnvRef(resolved); ok {
		return cmdShowSiteEnv(siteID, env, jsonOutput)
	}
	bundle, err := state.LoadStateBundle()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	records := siteRecordsByID(bundle.Sites, resolved)
	if len(records) == 0 {
		_, _ = projectFileExists, targetAliasUsed
		fmt.Fprintf(os.Stderr, "No site matched %q.\n", needle)
		return 1
	}
	out := siteDetailsOutput(needle, resolved, records, bundle.Servers)
	if !jsonOutput {
		printSiteDetails(out)
		return 0
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(data))
	return 0
}

func cmdSitePassword(needle string) int {
	resolved, _, projectFileExists, targetAliasUsed, err := resolveSiteTarget(needle)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if siteID, _, ok := splitSiteEnvRef(resolved); ok {
		resolved = siteID
	}
	bundle, err := state.LoadStateBundle()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	records := siteRecordsByID(bundle.Sites, resolved)
	if len(records) == 0 {
		_, _ = projectFileExists, targetAliasUsed
		fmt.Fprintf(os.Stderr, "No site matched %q.\n", needle)
		return 1
	}
	password, err := siteAdminPassword(preferredPasswordSiteRecord(records))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if password == "" {
		fmt.Fprintf(os.Stderr, "Site %q has no derivable admin password.\n", resolved)
		return 1
	}
	fmt.Println(password)
	return 0
}

func cmdEnvPassword(cfg envConfig) int {
	password, err := envAdminPassword(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(password)
	return 0
}

func preferredPasswordSiteRecord(records []map[string]any) map[string]any {
	for _, record := range records {
		if normalizedRecordString(siteEnvName(record)) == "live" {
			return record
		}
	}
	return records[0]
}

func printSiteDetails(out map[string]any) {
	siteID := recordValueString(out["site_id"])
	if siteID != "" {
		fmt.Println(siteID)
		fmt.Println(strings.Repeat("─", len(siteID)))
	}
	printDetailRows([]detailRow{
		{label: "Site", value: siteID},
		{label: "Name", value: recordValueString(out["name"])},
		{label: "Provider", value: recordValueString(out["provider"])},
		{label: "Target", value: recordValueString(out["target"])},
	})
	requested := recordValueString(out["requested_site"])
	resolved := recordValueString(out["resolved_site"])
	if requested != "" && resolved != "" && requested != resolved {
		printDetailRows([]detailRow{
			{label: "Requested", value: requested},
			{label: "Resolved", value: resolved},
		})
	}
	envs, _ := out["envs"].([]map[string]any)
	if len(envs) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("Environments:")
	rows := [][]string{{"env", "php", "url"}}
	for _, env := range envs {
		rows = append(rows, []string{
			siteEnvName(env),
			sitePHPVersion(env),
			firstRecordString(env, "url", "site_url", "home_url", "hostname"),
		})
	}
	fmt.Println(formatTable(rows))
	if siteHasEnv(envs, "live") && !siteHasEnv(envs, "staging") {
		fmt.Println()
		fmt.Println("Staging: not created")
		fmt.Printf("Create staging: nf site staging add %s\n", siteID)
	}
}

func siteHasEnv(records []map[string]any, env string) bool {
	for _, record := range records {
		if normalizedRecordString(siteEnvName(record)) == normalizedRecordString(env) {
			return true
		}
	}
	return false
}

func siteEnvSSHDisplay(record map[string]any) string {
	host := firstNonEmpty(mapStringAtPath(record, "ssh", "host"), mapStringAtPath(record, "kinsta", "ssh", "host"), firstRecordString(record, "ssh_host"))
	if host == "" {
		return ""
	}
	value := host
	if user := firstNonEmpty(mapStringAtPath(record, "ssh", "user"), mapStringAtPath(record, "kinsta", "ssh", "user"), firstRecordString(record, "ssh_user", "ssh_username")); user != "" {
		value = user + "@" + value
	}
	if port := firstNonEmpty(mapStringAtPath(record, "ssh", "port"), mapStringAtPath(record, "kinsta", "ssh", "port"), firstRecordString(record, "ssh_port")); port != "" && port != "22" {
		value += ":" + port
	}
	return value
}

func siteRecordsByID(records []map[string]any, siteID string) []map[string]any {
	needle := normalizedRecordString(siteID)
	if needle == "" {
		return nil
	}
	matches := []map[string]any{}
	for _, record := range records {
		if normalizedRecordString(siteRecordID(record)) == needle {
			matches = append(matches, record)
		}
	}
	return matches
}

func siteRecordsMatchingSite(records []map[string]any, needle string) ([]map[string]any, string, error) {
	normalized := normalizedRecordString(needle)
	if normalized == "" {
		return nil, "", nil
	}
	matchedIDs := []string{}
	seen := map[string]bool{}
	for _, record := range records {
		id := siteRecordID(record)
		if id == "" {
			continue
		}
		candidates := []string{id, siteEnvSiteID(record), siteRecordName(record), siteTargetName(record), firstRecordString(record, "hostname", "url", "site_url", "home_url")}
		for _, candidate := range candidates {
			if normalizedRecordString(candidate) != normalized {
				continue
			}
			if !seen[id] {
				seen[id] = true
				matchedIDs = append(matchedIDs, id)
			}
			break
		}
	}
	if len(matchedIDs) == 0 {
		return nil, "", nil
	}
	if len(matchedIDs) > 1 {
		sort.Strings(matchedIDs)
		return nil, "", ProjectError{Msg: fmt.Sprintf("Site %q matched multiple sites: %s.", needle, strings.Join(matchedIDs, ", "))}
	}
	return siteRecordsByID(records, matchedIDs[0]), matchedIDs[0], nil
}

func siteDetailsOutput(requested, resolved string, records []map[string]any, servers []map[string]any) map[string]any {
	first := records[0]
	out := map[string]any{
		"requested_site":   requested,
		"resolved_site":    resolved,
		"requested_target": requested,
		"resolved_target":  resolved,
		"site_id":          siteRecordID(first),
		"name":             siteRecordName(first),
		"target":           siteProviderTarget(first),
		"provider":         recordValueString(first["provider"]),
	}
	envs := make([]map[string]any, 0, len(records))
	for _, record := range records {
		env := cloneRecord(record)
		env["resolved_site"] = siteEnvDisplaySite(record)
		env["resolved_env"] = siteEnvName(record)
		env["resolved_target"] = siteProviderTarget(record)
		if err := validateSiteRecord(record); err == nil {
			_ = enrichSiteOutput(env, record, servers)
		}
		envs = append(envs, env)
	}
	sort.SliceStable(envs, func(i, j int) bool {
		left := siteEnvName(envs[i])
		right := siteEnvName(envs[j])
		li, ri := siteListEnvOrder(left), siteListEnvOrder(right)
		if li != ri {
			return li < ri
		}
		return left < right
	})
	out["envs"] = envs
	return out
}

func chooseSiteForShow() (string, error) {
	return chooseSite("show")
}

func chooseSiteForRemove() (string, error) {
	return chooseSite("remove")
}

func chooseSiteForPassword() (string, error) {
	return chooseSite("show password for")
}

func chooseSite(action string) (string, error) {
	bundle, err := state.LoadStateBundle()
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	options := []ui.SelectOption{}
	for _, record := range bundle.Sites {
		siteID := siteRecordID(record)
		if siteID == "" || seen[siteID] {
			continue
		}
		seen[siteID] = true
		options = append(options, ui.SelectOption{Value: siteID, Label: siteID})
	}
	if len(options) == 0 {
		return "", ProjectError{Msg: "No selectable sites found."}
	}
	return siteSelectFn(fmt.Sprintf("Choose a site to %s", action), options)
}

func chooseSiteEnv(action, siteID string) (string, error) {
	return selectSiteEnv(fmt.Sprintf("Choose a remote env to %s", action), siteID)
}

func selectSiteEnv(title, siteID string) (string, error) {
	bundle, err := state.LoadStateBundle()
	if err != nil {
		return "", err
	}
	options := []ui.SelectOption{}
	seen := map[string]bool{}
	for _, record := range bundle.Sites {
		if !siteEnvMatchesSite(record, siteID) {
			continue
		}
		envID := siteRecordEnvID(record)
		if envID == "" || seen[envID] {
			continue
		}
		seen[envID] = true
		options = append(options, ui.SelectOption{Value: envID, Label: envID})
	}
	if len(options) == 0 {
		if strings.TrimSpace(siteID) != "" {
			return "", ProjectError{Msg: fmt.Sprintf("No selectable envs found for %q.", siteID)}
		}
		return "", ProjectError{Msg: "No selectable envs found."}
	}
	sort.SliceStable(options, func(i, j int) bool {
		leftSite, leftEnv, _ := splitSiteEnvRef(options[i].Value)
		rightSite, rightEnv, _ := splitSiteEnvRef(options[j].Value)
		if leftSite != rightSite {
			return leftSite < rightSite
		}
		li, ri := siteListEnvOrder(leftEnv), siteListEnvOrder(rightEnv)
		if li != ri {
			return li < ri
		}
		return leftEnv < rightEnv
	})
	return siteSelectFn(title, options)
}

func parseSiteShowArgs(argv []string) (string, bool, error) {
	needle := ""
	jsonOutput := false
	for _, arg := range argv {
		switch arg {
		case "--json":
			jsonOutput = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, fmt.Errorf("unknown site show flag: %s", arg)
			}
			if needle != "" {
				return "", false, fmt.Errorf("site show takes at most one site")
			}
			needle = arg
		}
	}
	return needle, jsonOutput, nil
}

func parseSiteListArgs(argv []string) (bool, string, error) {
	envs := false
	siteID := ""
	for _, arg := range argv {
		switch arg {
		case "--envs":
			envs = true
		default:
			if strings.HasPrefix(arg, "-") {
				return false, "", fmt.Errorf("unknown site list flag: %s", arg)
			}
			if siteID != "" {
				return false, "", fmt.Errorf("site list --envs takes at most one site")
			}
			siteID = arg
		}
	}
	if siteID != "" && !envs {
		return false, "", fmt.Errorf("site list takes no arguments unless --envs is used")
	}
	return envs, siteID, nil
}

func resolveSiteCommandEnvRef(action, ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		if !siteIsInteractiveFn() {
			fmt.Fprintf(os.Stderr, "site %s requires an env ref like site.target:env\n", action)
			return "", false
		}
		selected, err := chooseSiteEnv(action, "")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return "", false
		}
		return selected, true
	}
	if _, _, ok := splitSiteEnvRef(ref); ok {
		return ref, true
	}
	if !siteIsInteractiveFn() {
		fmt.Fprintf(os.Stderr, "site %s requires an env ref like %s:live or %s:staging\n", action, ref, ref)
		return "", false
	}
	selected, err := chooseSiteEnv(action, ref)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return "", false
	}
	return selected, true
}

func parseSiteShellArgs(argv []string) (string, bool) {
	if len(argv) > 1 {
		fmt.Fprintln(os.Stderr, "site shell takes at most one env ref")
		return "", false
	}
	ref := ""
	if len(argv) == 1 {
		ref = argv[0]
		if strings.HasPrefix(ref, "-") {
			fmt.Fprintf(os.Stderr, "unknown site shell flag: %s\n", ref)
			return "", false
		}
	}
	return resolveSiteCommandEnvRef("shell", ref)
}

func parseSiteWPArgs(argv []string) (string, []string, bool) {
	if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
		fmt.Fprintln(os.Stderr, "site wp requires an env ref and wp-cli command")
		return "", nil, false
	}
	envRef, ok := resolveSiteCommandEnvRef("wp", argv[0])
	if !ok {
		return "", nil, false
	}
	command := normalizePassthroughArgs(argv[1:])
	if len(command) == 0 {
		fmt.Fprintln(os.Stderr, "site wp requires an env ref and wp-cli command")
		return "", nil, false
	}
	return envRef, command, true
}

func chooseTargetForShow() (string, error) {
	return chooseTarget("show")
}

func chooseTargetForRemove() (string, error) {
	return chooseTarget("remove")
}

func chooseTarget(action string) (string, error) {
	targets, err := cachedTargets()
	if err != nil {
		return "", err
	}
	if len(targets) == 0 {
		return "", ProjectError{Msg: "No targets found."}
	}
	options := make([]ui.SelectOption, 0, len(targets))
	for _, target := range targets {
		value := firstRecordString(target, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id")
		if value == "" {
			continue
		}
		options = append(options, ui.SelectOption{Label: value, Value: value})
	}
	if len(options) == 0 {
		return "", ProjectError{Msg: "No selectable targets found."}
	}
	return targetSelectFn(fmt.Sprintf("Choose a target to %s", action), options)
}

func parseTargetShowArgs(argv []string) (string, bool, error) {
	needle := ""
	jsonOutput := false
	for _, arg := range argv {
		switch arg {
		case "--json":
			jsonOutput = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, fmt.Errorf("unknown target show flag: %s", arg)
			}
			if needle != "" {
				return "", false, fmt.Errorf("target show takes at most one target")
			}
			needle = arg
		}
	}
	return needle, jsonOutput, nil
}

package cli

// Site show, password, picker, and argument parsing helpers.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/kinsta"
	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/state"
	"github.com/nonfiction/nf/internal/ui"
)

type passwordScope string

const (
	passwordScopeWP        passwordScope = "wp"
	passwordScopeDB        passwordScope = "db"
	passwordScopeBasicAuth passwordScope = "basicauth"
	passwordScopeRoot      passwordScope = "root"
	passwordScopeAdminer   passwordScope = "adminer"
)

func parsePasswordScopeFlags(args []string, allowed map[string]passwordScope, defaultScope passwordScope, command string) ([]string, passwordScope, error) {
	positionals := []string{}
	scope := defaultScope
	explicit := ""
	for _, arg := range args {
		if strings.HasPrefix(arg, "--") {
			candidate, ok := allowed[arg]
			if !ok {
				return nil, scope, ProjectError{Msg: fmt.Sprintf("unknown %s flag: %s", command, arg)}
			}
			if explicit != "" {
				return nil, scope, ProjectError{Msg: fmt.Sprintf("%s accepts only one password flag", command)}
			}
			explicit = arg
			scope = candidate
			continue
		}
		positionals = append(positionals, arg)
	}
	return positionals, scope, nil
}

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

func cmdSitePassword(needle string, scope passwordScope) int {
	resolved, _, projectFileExists, targetAliasUsed, err := resolveSiteTarget(needle)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	requestedEnv := ""
	if siteID, env, ok := splitSiteEnvRef(resolved); ok {
		if scope != passwordScopeDB {
			fmt.Fprintf(os.Stderr, "site password takes a site, not an env; use %q.\n", siteID)
			return 1
		}
		resolved = siteID
		requestedEnv = env
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
	record := preferredPasswordSiteRecord(records)
	if scope == passwordScopeDB {
		record = preferredPasswordSiteRecordForEnv(records, firstNonEmpty(requestedEnv, "live"))
		if record == nil {
			fmt.Fprintf(os.Stderr, "Site %q has no %s env.\n", resolved, firstNonEmpty(requestedEnv, "live"))
			return 1
		}
	}
	password, err := sitePasswordForRecord(record, scope)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if password == "" {
		fmt.Fprintf(os.Stderr, "Site %q has no derivable %s password.\n", resolved, passwordScopeLabel(scope))
		return 1
	}
	fmt.Println(password)
	return 0
}

func cmdEnvPassword(cfg envConfig, scope passwordScope) int {
	password, err := envPasswordForScope(cfg, scope)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(password)
	return 0
}

func cmdEnvRemotePassword(metadata map[string]any, remoteName string, scope passwordScope) int {
	remoteName = strings.TrimSpace(remoteName)
	if remoteName == "" {
		fmt.Fprintln(os.Stderr, "env password requires a non-empty remote")
		return 1
	}
	siteID, remoteEnv, ok, err := projectRemoteAlias(metadata, remoteName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "No remote named %q in nf.json remotes.\n", remoteName)
		return 1
	}
	record, _, err := cachedSiteEnv(siteID, remoteEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if record == nil {
		fmt.Fprintf(os.Stderr, "No cached remote env matched site %q env %q. Run nf site refresh after target cache is current, or update the local state cache.\n", siteID, remoteEnv)
		return 1
	}
	password, err := sitePasswordForRecord(record, scope)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if password == "" {
		fmt.Fprintf(os.Stderr, "Remote %q has no derivable %s password.\n", remoteName, passwordScopeLabel(scope))
		return 1
	}
	fmt.Println(password)
	return 0
}

func sitePasswordForRecord(record map[string]any, scope passwordScope) (string, error) {
	switch scope {
	case passwordScopeWP:
		return siteAdminPassword(record)
	case passwordScopeDB:
		return siteDatabasePassword(record)
	case passwordScopeBasicAuth:
		slug := sitePasswordSlug(record)
		if slug == "" {
			return "", nil
		}
		return deriveSiteBasicAuthPassword(slug)
	default:
		return "", ProjectError{Msg: "unsupported site password scope"}
	}
}

func envPasswordForScope(cfg envConfig, scope passwordScope) (string, error) {
	switch scope {
	case passwordScopeWP:
		return envAdminPassword(cfg)
	case passwordScopeDB:
		return envDBPassword(cfg)
	case passwordScopeBasicAuth:
		return deriveProjectPassword(cfg.ProjectSlug, "basic-auth", cfg.PasswordVersion)
	default:
		return "", ProjectError{Msg: "unsupported env password scope"}
	}
}

func passwordScopeLabel(scope passwordScope) string {
	switch scope {
	case passwordScopeWP:
		return "admin"
	case passwordScopeDB:
		return "database"
	case passwordScopeBasicAuth:
		return "basic-auth"
	case passwordScopeRoot:
		return "root"
	case passwordScopeAdminer:
		return "adminer"
	default:
		return string(scope)
	}
}

func preferredPasswordSiteRecordForEnv(records []map[string]any, env string) map[string]any {
	for _, record := range records {
		if normalizedRecordString(siteEnvName(record)) == normalizedRecordString(env) {
			return record
		}
	}
	return nil
}

func siteDatabasePassword(record map[string]any) (string, error) {
	if password := firstRecordString(record, "db_password", "database_password", "mysql_password"); password != "" {
		return password, nil
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(record["provider"])))
	if provider == "kinsta" {
		return kinstaSiteDatabasePassword(record)
	}
	slug := sitePasswordSlug(record)
	if slug == "" {
		return "", nil
	}
	version := currentProjectPasswordVersionForSite(slug)
	return deriveProjectPassword(slug, "mysql", version)
}

func kinstaSiteDatabasePassword(record map[string]any) (string, error) {
	remoteSiteID := mapStringAtPath(record, "kinsta", "site_id")
	remoteEnvID := mapStringAtPath(record, "kinsta", "environment_id")
	if remoteSiteID == "" || remoteEnvID == "" {
		return "", ProjectError{Msg: fmt.Sprintf("Kinsta site %q is missing API identifiers. Run nf site refresh and try again.", siteRecordID(record))}
	}
	token := envwizard.Value("KINSTA_API_KEY")
	if token == "" {
		return "", ProjectError{Msg: fmt.Sprintf("Expected KINSTA_API_KEY in the environment or %s.", config.EnvFile())}
	}
	client := kinsta.NewClient(firstNonEmpty(envwizard.Value("KINSTA_BASE_URL"), "https://api.kinsta.com/v2"), token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := client.SFTPConfig(ctx, remoteSiteID, remoteEnvID)
	if err != nil {
		return "", err
	}
	if _, err := client.SFTPPassword(ctx, remoteEnvID); err != nil {
		return "", err
	}
	host := firstNonEmpty(cfg.Host, mapStringAtPath(record, "ssh", "host"))
	user := firstNonEmpty(cfg.User, mapStringAtPath(record, "ssh", "user"))
	port := firstNonEmpty(cfg.Port, mapStringAtPath(record, "ssh", "port"), "22")
	if host == "" || user == "" {
		return "", ProjectError{Msg: fmt.Sprintf("Kinsta site %q is missing SSH connection details.", siteRecordID(record))}
	}
	wpPath := firstNonEmpty(normalizeKinstaCachedPath(firstRecordString(record, "path")), kinstaEnvPath(user, ""))
	if wpPath == "" {
		return "", ProjectError{Msg: fmt.Sprintf("Kinsta site %q is missing path. Run nf site refresh.", siteRecordID(record))}
	}
	remoteCommand := "cd " + shellQuoteArg(wpPath) + " && wp --path=" + shellQuoteArg(wpPath) + " config get DB_PASSWORD --type=constant"
	args := []string{"ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-p", port, user + "@" + host, remoteCommand}
	output, err := runSSHOutputFn(args)
	if err != nil {
		return "", err
	}
	password := strings.TrimSpace(string(output))
	if password == "" {
		return "", ProjectError{Msg: fmt.Sprintf("Kinsta site %q returned an empty database password.", siteRecordID(record))}
	}
	return password, nil
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
	if siteHasEnv(envs, "live") && !siteHasEnv(envs, "staging") {
		printDetailRows([]detailRow{
			{label: "Staging", value: "not created"},
			{label: "Next", value: "nf site staging add " + siteID},
		})
	}
	fmt.Println()
	fmt.Println("Environments")
	rows := [][]string{{"env", "php", "url"}}
	for _, env := range envs {
		rows = append(rows, []string{
			siteEnvName(env),
			sitePHPVersion(env),
			firstRecordString(env, "url", "site_url", "home_url", "hostname"),
		})
	}
	fmt.Println(formatTable(rows))
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
	return chooseTargetFromRecords(action, targets)
}

func chooseLinodeTarget(action string) (string, error) {
	targets, err := cachedTargets()
	if err != nil {
		return "", err
	}
	linodeTargets := make([]map[string]any, 0, len(targets))
	for _, target := range targets {
		if strings.EqualFold(strings.TrimSpace(recordValueString(target["provider"])), "linode") {
			linodeTargets = append(linodeTargets, target)
		}
	}
	if len(linodeTargets) == 0 {
		return "", ProjectError{Msg: "No linode targets found."}
	}
	return chooseTargetFromRecords(action, linodeTargets)
}

func chooseTargetFromRecords(action string, targets []map[string]any) (string, error) {
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

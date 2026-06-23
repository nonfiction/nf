package cli

// Public domain readiness for cached remote site environments.
//
// This command prepares provider/server routing and TLS readiness, but never
// mutates client/public DNS. Kinsta returns the records the client must create;
// Linode prints the A/AAAA/CNAME-style target instructions.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/kinsta"
	"github.com/nonfiction/nf/internal/state"
	"github.com/nonfiction/nf/internal/ui"
)

type siteDomainOptions struct {
	domains        []string
	proxyMode      string
	proxySet       bool
	searchReplace  bool
	searchSet      bool
	deleteCert     bool
	force          bool
	waitTimeout    time.Duration
	waitInterval   time.Duration
	dryRun         bool
	execute        bool
	yes            bool
	nonInteractive bool
}

type siteDomainPlan struct {
	Action           string
	EnvID            string
	SiteID           string
	SiteName         string
	Env              string
	Provider         string
	Record           map[string]any
	Target           envRemoteSyncTarget
	Canonical        string
	Aliases          []string
	Domains          []string
	Primary          bool
	RedirectTarget   string
	ProxyMode        string
	DomainProxyModes map[string]string
	SearchReplace    bool
	DeleteCert       bool
	CurrentURL       string
	CurrentHostname  string
	InternalURL      string
	InternalHostname string
	FileSlug         string
	KinstaSiteID     string
	KinstaEnvID      string
	KinstaDomainID   string
	TargetHostname   string
	TargetIPv4       string
	TargetIPv6       string
	PHPVersion       string
}

type siteDomainProviderResult struct {
	Domains []siteDomainProviderDomain
}

type siteDomainProviderDomain struct {
	Name     string
	Role     string
	DomainID string
	Records  kinsta.DomainRecords
}

var (
	kinstaDomainAddPointingWaitTimeout  = 30 * time.Second
	kinstaDomainAddPointingWaitInterval = 2 * time.Second
)

const kinstaDomainSetupType = "avoid_downtime"

func runSiteDomain(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runSiteDomainHelp()
	}
	action := cliCommandAlias(strings.TrimSpace(argv[0]))
	if action == "list" {
		filter, ok := parseSiteDomainListArgs(argv[1:])
		if !ok {
			return 1
		}
		return cmdSiteDomainList(filter)
	}
	if action != "add" && action != "primary" && action != "check" && action != "remove" {
		fmt.Fprintf(os.Stderr, "unsupported domain action: %s\n", action)
		return 1
	}
	envRef, opts, ok := parseSiteDomainActionArgs(action, argv[1:])
	if !ok {
		return 1
	}
	return cmdSiteDomain(envRef, action, opts)
}

func runDomain(argv []string) int {
	return runSiteDomain(argv)
}

func runSiteDomainHelp() int {
	printCommandHelp("domain", []helpLine{
		{"list, ls [site|env|remote]", "list cached domain bindings"},
		{"add [env|remote] [domain...]", "add external domains; prompts when omitted"},
		{"check <env|remote> [domain]...", "check DNS, provider, HTTP, and HTTPS readiness"},
		{"primary [env|remote] [domain]", "make one domain primary"},
		{"remove, rm [env|remote] [domain...]", "remove external domain bindings"},
	}, helpSection{"Domain Options", []helpLine{
		{"--proxy <mode|ip>", "Linode proxy mode: cloudflare or reverse proxy IP"},
		{"--no-proxy", "Linode direct/no-proxy mode"},
		{"--search-replace", "run provider/wp search-replace during primary"},
		{"--no-search-replace", "update primary home/siteurl without database-wide search-replace"},
		{"--force", "launch primary without waiting for readiness checks"},
		{"--wait-timeout <duration>", "maximum primary readiness wait; default 30m"},
		{"--wait-interval <duration>", "primary readiness poll interval; default 30s"},
		{"--delete-cert", "also delete the Linode Let's Encrypt certificate lineage"},
	}}, helpSection{"Mutation Options", []helpLine{
		{"--dry-run", "show the mutation plan only"},
		{"--execute", "execute the mutation plan"},
		{"--yes", "confirm mutation execution"},
		{"--non-interactive", "fail instead of prompting"},
	}})
	return 0
}

func parseSiteDomainListArgs(argv []string) (string, bool) {
	filter := ""
	for _, arg := range argv {
		if strings.HasPrefix(arg, "-") {
			fmt.Fprintf(os.Stderr, "unknown domain list flag: %s\n", arg)
			return "", false
		}
		if filter != "" {
			fmt.Fprintln(os.Stderr, "domain list takes at most one site, env, or remote")
			return "", false
		}
		filter = arg
	}
	return filter, true
}

func cmdSiteDomainList(filter string) int {
	resolved := strings.TrimSpace(filter)
	if resolved != "" {
		var err error
		resolved, _, _, _, err = resolveSiteTarget(resolved)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	projectRefs, inProject, err := siteDomainProjectRemoteRefs("")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	projectEnvIDs := map[string]bool{}
	if inProject {
		for _, ref := range projectRefs {
			projectEnvIDs[ref.envID] = true
		}
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	rows := [][]string{{"domain", "env", "role", "management", "status", "provider", "proxy"}}
	for _, record := range records {
		if inProject && !projectEnvIDs[siteRecordEnvID(record)] {
			continue
		}
		if resolved != "" && !siteDomainListRecordMatches(record, resolved) {
			continue
		}
		for _, domain := range siteDomainListDomains(record) {
			rows = append(rows, []string{
				domain.name,
				siteRecordEnvID(record),
				domain.role,
				domain.management,
				domain.status,
				recordValueString(record["provider"]),
				displaySiteDomainProxyMode(domain.proxyMode),
			})
		}
	}
	if len(rows) == 1 {
		if resolved != "" {
			fmt.Printf("No domains found for %q.\n", filter)
		} else {
			fmt.Println("No domains found.")
		}
		return 0
	}
	fmt.Println(formatTable(rows))
	return 0
}

func siteDomainListRecordMatches(record map[string]any, filter string) bool {
	if siteID, env, ok := splitSiteEnvRef(filter); ok {
		return siteEnvMatchesSite(record, siteID) && siteEnvMatchesEnv(record, env)
	}
	return siteEnvMatchesSite(record, filter)
}

type siteDomainListDomain struct {
	name       string
	role       string
	management string
	status     string
	proxyMode  string
}

func siteDomainListDomains(record map[string]any) []siteDomainListDomain {
	seen := map[string]int{}
	domains := []siteDomainListDomain{}
	domainEntries := siteDomainEntryValues(record["domains"])
	envProxyMode := ""
	if len(domainEntries) == 0 {
		envProxyMode = firstRecordString(record, "proxy_mode")
	}
	addDomain := func(name, role, management, status, proxyMode string) {
		name = normalizeDomainName(hostnameFromURLish(name))
		if name == "" || siteDomainWildcardName(name) {
			return
		}
		management = firstNonEmpty(normalizeSiteDomainManagement(management), "external")
		if management == "external" && proxyMode == "" {
			proxyMode = envProxyMode
		}
		if management != "external" {
			proxyMode = ""
		}
		entry := siteDomainListDomain{
			name:       name,
			role:       normalizeSiteDomainRole(role),
			management: management,
			status:     normalizeSiteDomainStatus(status),
			proxyMode:  proxyMode,
		}
		if i, ok := seen[name]; ok {
			if entry.role == "primary" {
				domains[i].role = "primary"
			}
			if entry.management == "internal" {
				domains[i].management = "internal"
			}
			if domains[i].status == "" && entry.status != "" {
				domains[i].status = entry.status
			}
			if entry.proxyMode != "" {
				domains[i].proxyMode = entry.proxyMode
			}
			return
		}
		seen[name] = len(domains)
		domains = append(domains, entry)
	}
	publicStatus := firstRecordString(record, "domain_state")
	hasExternalPrimary := false
	for _, entry := range domainEntries {
		if typed, ok := entry.(map[string]any); ok {
			role := normalizeSiteDomainRole(firstRecordString(typed, "role", "type"))
			management := firstNonEmpty(normalizeSiteDomainManagement(firstRecordString(typed, "management")), "external")
			if management == "external" && role == "primary" {
				hasExternalPrimary = true
				break
			}
		}
	}
	if firstRecordString(record, "primary_domain") != "" {
		hasExternalPrimary = true
	}
	if host := hostnameFromURLish(firstRecordString(record, "hostname")); host != "" && !looksLikeInternalSiteHostname(record, host) {
		hasExternalPrimary = true
	}
	if host := hostnameFromURLish(firstRecordString(record, "url", "site_url", "home_url")); host != "" && !looksLikeInternalSiteHostname(record, host) {
		hasExternalPrimary = true
	}
	if defaultHost := siteDomainDefaultHostname(record); defaultHost != "" {
		role := "primary"
		if hasExternalPrimary {
			role = "secondary"
		}
		addDomain(defaultHost, role, "internal", "active", "")
	}
	for _, entry := range domainEntries {
		role := ""
		management := ""
		status := publicStatus
		proxyMode := ""
		if typed, ok := entry.(map[string]any); ok {
			role = firstRecordString(typed, "role", "type")
			management = firstRecordString(typed, "management")
			status = firstNonEmpty(firstRecordString(typed, "status"), status)
			proxyMode = firstRecordString(typed, "proxy_mode")
		}
		addDomain(siteDomainEntryName(entry), role, management, status, proxyMode)
	}
	addDomain(firstRecordString(record, "primary_domain"), "primary", "external", firstNonEmpty(publicStatus, "active"), envProxyMode)
	if host := hostnameFromURLish(firstRecordString(record, "hostname")); !looksLikeInternalSiteHostname(record, host) {
		addDomain(host, "primary", "external", firstNonEmpty(publicStatus, "active"), envProxyMode)
	}
	if host := hostnameFromURLish(firstRecordString(record, "url", "site_url", "home_url")); !looksLikeInternalSiteHostname(record, host) {
		addDomain(host, "primary", "external", firstNonEmpty(publicStatus, "active"), envProxyMode)
	}
	return domains
}

func normalizeSiteDomainRole(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "primary", "canonical", "current":
		return "primary"
	case "secondary", "redirect", "alias", "default":
		return "secondary"
	default:
		return "secondary"
	}
}

func normalizeSiteDomainManagement(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "internal", "managed":
		return "internal"
	case "external", "public", "client":
		return "external"
	default:
		return ""
	}
}

func normalizeSiteDomainStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "active", "verified", "unverified", "pending":
		return strings.ToLower(strings.TrimSpace(value))
	case "connected", "pointed", "dns_verified":
		return "active"
	case "pending_dns", "dns_pending", "needs_dns", "requires_dns", "not_pointed", "not-pointed", "verifying", "waiting":
		return "pending"
	case "failed", "error", "invalid":
		return "error"
	case "inactive", "disabled":
		return "inactive"
	case "primary", "managed", "ready":
		return "active"
	case "prepared", "prepare":
		return "pending"
	default:
		return "pending"
	}
}

func siteDomainWildcardName(name string) bool {
	return strings.HasPrefix(normalizeDomainName(name), "*.")
}

func parseSiteDomainActionArgs(action string, argv []string) (string, siteDomainOptions, bool) {
	var opts siteDomainOptions
	positionals := []string{}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch arg {
		case "--dry-run":
			opts.dryRun = true
		case "--execute":
			opts.execute = true
		case "--yes":
			opts.yes = true
		case "--non-interactive":
			opts.nonInteractive = true
		case "--search-replace":
			if opts.searchSet && !opts.searchReplace {
				fmt.Fprintln(os.Stderr, "Choose either --search-replace or --no-search-replace, not both.")
				return "", opts, false
			}
			opts.searchReplace = true
			opts.searchSet = true
		case "--no-search-replace":
			if opts.searchSet && opts.searchReplace {
				fmt.Fprintln(os.Stderr, "Choose either --search-replace or --no-search-replace, not both.")
				return "", opts, false
			}
			opts.searchReplace = false
			opts.searchSet = true
		case "--delete-cert":
			opts.deleteCert = true
		case "--force":
			opts.force = true
		case "--wait-timeout":
			if i+1 >= len(argv) || strings.TrimSpace(argv[i+1]) == "" {
				fmt.Fprintln(os.Stderr, "--wait-timeout requires a value")
				return "", opts, false
			}
			i++
			duration, ok := parseSiteDomainWaitDuration("--wait-timeout", argv[i])
			if !ok {
				return "", opts, false
			}
			opts.waitTimeout = duration
		case "--wait-interval":
			if i+1 >= len(argv) || strings.TrimSpace(argv[i+1]) == "" {
				fmt.Fprintln(os.Stderr, "--wait-interval requires a value")
				return "", opts, false
			}
			i++
			duration, ok := parseSiteDomainWaitDuration("--wait-interval", argv[i])
			if !ok {
				return "", opts, false
			}
			opts.waitInterval = duration
		case "--setup":
			fmt.Fprintln(os.Stderr, "--setup is no longer supported; Kinsta domain setup always uses avoid-downtime")
			return "", opts, false
		case "--proxy":
			if opts.proxySet && strings.TrimSpace(opts.proxyMode) == "" {
				fmt.Fprintln(os.Stderr, "Choose either --proxy or --no-proxy, not both.")
				return "", opts, false
			}
			if i+1 >= len(argv) || strings.TrimSpace(argv[i+1]) == "" {
				fmt.Fprintln(os.Stderr, "--proxy requires a value")
				return "", opts, false
			}
			i++
			opts.proxyMode = argv[i]
			opts.proxySet = true
		case "--no-proxy":
			if opts.proxySet && strings.TrimSpace(opts.proxyMode) != "" {
				fmt.Fprintln(os.Stderr, "Choose either --proxy or --no-proxy, not both.")
				return "", opts, false
			}
			opts.proxyMode = ""
			opts.proxySet = true
		default:
			if strings.HasPrefix(arg, "--canonical=") {
				fmt.Fprintln(os.Stderr, "--canonical was replaced by positional domains")
				return "", opts, false
			}
			if strings.HasPrefix(arg, "--alias=") {
				fmt.Fprintln(os.Stderr, "--alias was replaced by positional domains")
				return "", opts, false
			}
			if strings.HasPrefix(arg, "--setup=") {
				fmt.Fprintln(os.Stderr, "--setup is no longer supported; Kinsta domain setup always uses avoid-downtime")
				return "", opts, false
			}
			if strings.HasPrefix(arg, "--proxy=") {
				if opts.proxySet && strings.TrimSpace(opts.proxyMode) == "" {
					fmt.Fprintln(os.Stderr, "Choose either --proxy or --no-proxy, not both.")
					return "", opts, false
				}
				opts.proxyMode = strings.TrimPrefix(arg, "--proxy=")
				if strings.TrimSpace(opts.proxyMode) == "" {
					fmt.Fprintln(os.Stderr, "--proxy requires a value")
					return "", opts, false
				}
				opts.proxySet = true
				continue
			}
			if strings.HasPrefix(arg, "--wait-timeout=") {
				duration, ok := parseSiteDomainWaitDuration("--wait-timeout", strings.TrimPrefix(arg, "--wait-timeout="))
				if !ok {
					return "", opts, false
				}
				opts.waitTimeout = duration
				continue
			}
			if strings.HasPrefix(arg, "--wait-interval=") {
				duration, ok := parseSiteDomainWaitDuration("--wait-interval", strings.TrimPrefix(arg, "--wait-interval="))
				if !ok {
					return "", opts, false
				}
				opts.waitInterval = duration
				continue
			}
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(os.Stderr, "unknown domain flag: %s\n", arg)
				return "", opts, false
			}
			positionals = append(positionals, arg)
		}
	}
	if opts.searchSet && action != "primary" {
		fmt.Fprintln(os.Stderr, "--search-replace and --no-search-replace only apply to domain primary")
		return "", opts, false
	}
	if action != "primary" && (opts.force || opts.waitTimeout > 0 || opts.waitInterval > 0) {
		fmt.Fprintln(os.Stderr, "--force, --wait-timeout, and --wait-interval only apply to domain primary")
		return "", opts, false
	}
	if action == "primary" && opts.force && (opts.waitTimeout > 0 || opts.waitInterval > 0) {
		fmt.Fprintln(os.Stderr, "--wait-timeout and --wait-interval cannot be used with --force")
		return "", opts, false
	}
	if action != "remove" && opts.deleteCert {
		fmt.Fprintln(os.Stderr, "--delete-cert only applies to domain remove")
		return "", opts, false
	}
	if action == "remove" && opts.searchSet {
		fmt.Fprintln(os.Stderr, "domain remove does not support --search-replace or --no-search-replace")
		return "", opts, false
	}
	if action == "check" {
		if opts.dryRun || opts.execute || opts.yes || opts.searchSet || opts.deleteCert {
			fmt.Fprintln(os.Stderr, "domain check is read-only; use only domains, --proxy, --no-proxy, and --non-interactive")
			return "", opts, false
		}
	}
	envRef := ""
	rawDomains := []string{}
	if len(positionals) > 0 {
		envRef = positionals[0]
		rawDomains = positionals[1:]
	}
	if action == "primary" && len(rawDomains) != 1 {
		if len(rawDomains) > 1 {
			fmt.Fprintln(os.Stderr, "domain primary takes exactly one domain")
			return "", opts, false
		}
	}
	normalized, err := normalizePublicDomainList(rawDomains)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return "", opts, false
	}
	opts.domains = normalized
	return envRef, opts, true
}

func parseSiteDomainWaitDuration(flag, value string) (time.Duration, bool) {
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || duration <= 0 {
		fmt.Fprintf(os.Stderr, "%s must be a positive duration like 30s or 30m\n", flag)
		return 0, false
	}
	return duration, true
}

func cmdSiteDomain(envRef, action string, opts siteDomainOptions) int {
	if opts.execute && opts.dryRun {
		fmt.Fprintln(os.Stderr, "Choose either --execute or --dry-run, not both.")
		return 1
	}
	envRefExplicit := strings.TrimSpace(envRef) != ""
	resolved, ok := resolveSiteDomainEnvRef(action, envRef, opts.nonInteractive)
	if !ok {
		return 1
	}
	siteID, env, ok := splitSiteEnvRef(resolved)
	if !ok {
		fmt.Fprintf(os.Stderr, "domain %s requires an env ref like site.target:live\n", action)
		return 1
	}
	record, _, err := cachedSiteEnv(siteID, env)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if record == nil {
		fmt.Fprintf(os.Stderr, "No cached remote env matched %q.\n", canonicalEnvID(siteID, env))
		return 1
	}
	opts, err = resolveSiteDomainActionOptions(action, opts, record, !envRefExplicit)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	plan, err := buildSiteDomainPlan(siteID, env, action, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if action == "check" {
		return cmdSiteDomainCheck(plan)
	}
	if action != "remove" {
		printSiteDomainStaleWarnings(plan)
	}
	willExecute := opts.execute || (!opts.dryRun && !opts.nonInteractive)
	mode := "dry-run"
	if willExecute {
		mode = "execute"
	}
	printSiteDomainPlan(plan, mode)
	if !willExecute {
		fmt.Println("No data was changed. Re-run with --execute to apply public-domain changes.")
		return 0
	}
	if opts.nonInteractive && !opts.yes {
		fmt.Fprintln(os.Stderr, "Remote execution requires both --execute and --yes in non-interactive mode.")
		return 1
	}
	if !opts.yes {
		message := fmt.Sprintf("%s domain%s %s for %s?", strings.ToUpper(action[:1])+action[1:], pluralSuffix(len(plan.allDomains())), strings.Join(plan.allDomains(), ", "), plan.EnvID)
		if action == "primary" {
			message = fmt.Sprintf("Wait until public checks pass, then launch primary domain %q for %s without another prompt?", plan.Canonical, plan.EnvID)
			if opts.force {
				message = fmt.Sprintf("Force primary domain %q for %s without waiting for public checks?", plan.Canonical, plan.EnvID)
			}
		}
		confirmed, err := ui.Confirm(message, false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	if action == "primary" && !opts.force {
		proceed, err := waitForSiteDomainPrimaryReadiness(plan, opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !proceed {
			return 0
		}
		fmt.Println("Launching primary domain now...")
	}
	result := siteDomainProviderResult{}
	switch plan.Provider {
	case "kinsta":
		if action == "remove" {
			result, err = kinstaRemoveDomainFn(plan)
		} else if plan.Primary {
			result, err = kinstaPrimaryDomainFn(plan)
		} else {
			result, err = kinstaPrepareDomainFn(plan)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if action != "remove" {
			printKinstaDomainRecords(result)
		}
	case "linode":
		script := renderLinodeDomainBindingScript(plan)
		if action == "remove" {
			script = renderLinodeDomainBindingRemoveScript(plan, opts.deleteCert)
		}
		if err := runSSHCommandFn(remoteSudoBashArgs(plan.Target, script)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	default:
		fmt.Fprintf(os.Stderr, "domain is not implemented for provider %q; no data was changed.\n", plan.Provider)
		return 1
	}
	if action == "remove" {
		err = removeSiteDomainCache(plan)
	} else {
		err = updateSiteDomainCache(plan, result)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if action == "remove" {
		if plan.Provider == "linode" && !opts.deleteCert {
			fmt.Println("Certificate lineage was kept for rollback safety. Use --delete-cert after the rollback window if you want to remove it.")
		}
		fmt.Println("Domain removed.")
	} else if plan.Primary {
		fmt.Println("Domain launched as primary.")
	} else {
		fmt.Println("Domain added.")
	}
	return 0
}

func waitForSiteDomainPrimaryReadiness(plan siteDomainPlan, opts siteDomainOptions) (bool, error) {
	timeout := opts.waitTimeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	interval := opts.waitInterval
	if interval == 0 {
		interval = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	fmt.Println("Waiting for public domain checks.")
	fmt.Println("Approval already captured; launch will run automatically when checks pass.")
	for attempt := 1; ; attempt++ {
		if attempt > 1 {
			fmt.Println()
			fmt.Println("Rechecking public domain readiness...")
		}
		result, err := printSiteDomainReadinessCheck(plan)
		if err != nil {
			return false, err
		}
		printSiteDomainCheckNextStep(plan, result)
		if result.Ready {
			fmt.Println("Overall: ready")
			if result.Primary {
				fmt.Println("Domain is already primary; no launch needed.")
				return false, nil
			}
			fmt.Println("Checks ready.")
			return true, nil
		}
		fmt.Println("Overall: pending")
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, ProjectError{Msg: "Timed out waiting for public domain checks; primary was not changed."}
		}
		sleepFor := interval
		if sleepFor > remaining {
			sleepFor = remaining
		}
		fmt.Printf("Next check in %s. Timeout in %s.\n", formatSiteDomainWaitDuration(sleepFor), formatSiteDomainWaitDuration(remaining))
		time.Sleep(sleepFor)
	}
}

func formatSiteDomainWaitDuration(duration time.Duration) string {
	if duration > time.Second {
		duration = duration.Round(time.Second)
	}
	return duration.String()
}

func resolveSiteDomainEnvRef(action, ref string, nonInteractive bool) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		if nonInteractive || !siteIsInteractiveFn() {
			fmt.Fprintf(os.Stderr, "domain %s requires an env ref like site.target:live or a project remote\n", action)
			return "", false
		}
		selected, err := chooseSiteDomainEnvRef(action, "")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return "", false
		}
		ref = selected
	}
	resolved, _, _, _, err := resolveSiteTarget(ref)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return "", false
	}
	if _, _, ok := splitSiteEnvRef(resolved); ok {
		return resolved, true
	}
	if nonInteractive || !siteIsInteractiveFn() {
		fmt.Fprintf(os.Stderr, "domain %s requires an env ref like %s:live or a project remote in non-interactive mode\n", action, ref)
		return "", false
	}
	selected, err := chooseSiteDomainEnvRef(action, resolved)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return "", false
	}
	resolved, _, _, _, err = resolveSiteTarget(selected)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return "", false
	}
	if _, _, ok := splitSiteEnvRef(resolved); !ok {
		fmt.Fprintf(os.Stderr, "domain %s requires an env ref like %s:live or a project remote\n", action, selected)
		return "", false
	}
	return resolved, true
}

func chooseSiteDomainEnvRef(action, siteID string) (string, error) {
	projectRefs, inProject, err := siteDomainProjectRemoteRefs(siteID)
	if err != nil {
		return "", err
	}
	if inProject {
		if len(projectRefs) == 0 {
			if strings.TrimSpace(siteID) != "" {
				return "", ProjectError{Msg: fmt.Sprintf("No configured project remotes found for %q.", siteID)}
			}
			return "", ProjectError{Msg: "No configured project remotes found."}
		}
		options := make([]ui.SelectOption, 0, len(projectRefs))
		for _, ref := range projectRefs {
			options = append(options, ui.SelectOption{Value: ref.name, Label: ref.name + " (" + ref.envID + ")"})
		}
		return siteSelectFn("Choose an env or remote for domain "+action, options)
	}
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
		if envID == "" || seen["env|"+envID] {
			continue
		}
		seen["env|"+envID] = true
		options = append(options, ui.SelectOption{Value: envID, Label: envID})
	}
	if len(options) == 0 {
		if strings.TrimSpace(siteID) != "" {
			return "", ProjectError{Msg: fmt.Sprintf("No selectable envs found for %q.", siteID)}
		}
		return "", ProjectError{Msg: "No selectable envs found."}
	}
	sort.SliceStable(options, func(i, j int) bool { return options[i].Label < options[j].Label })
	return siteSelectFn("Choose an env or remote for domain "+action, options)
}

type siteDomainProjectRemoteRef struct {
	name  string
	envID string
}

func siteDomainProjectRemoteRefs(siteID string) ([]siteDomainProjectRemoteRef, bool, error) {
	root, ok := currentNFProjectRoot()
	if !ok {
		return nil, false, nil
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		return nil, true, err
	}
	remotes, err := projectRemotes(metadata, false)
	if err != nil {
		return nil, true, err
	}
	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	var cachedRecords []map[string]any
	if strings.TrimSpace(siteID) != "" {
		cachedRecords, err = state.LoadStateRecords("sites")
		if err != nil {
			return nil, true, err
		}
	}
	refs := make([]siteDomainProjectRemoteRef, 0, len(names))
	for _, name := range names {
		remoteSiteID, remoteEnv, ok, err := projectRemoteAlias(metadata, name)
		if err != nil {
			return nil, true, err
		}
		if !ok {
			continue
		}
		envID := canonicalEnvID(remoteSiteID, remoteEnv)
		if strings.TrimSpace(siteID) != "" {
			record := map[string]any{"site_id": remoteSiteID, "env_id": envID, "env": remoteEnv}
			matches := siteEnvMatchesSite(record, siteID)
			for _, cached := range cachedRecords {
				if siteRecordEnvID(cached) == envID && siteEnvMatchesSite(cached, siteID) {
					matches = true
					break
				}
			}
			if !matches {
				continue
			}
		}
		refs = append(refs, siteDomainProjectRemoteRef{name: name, envID: envID})
	}
	return refs, true, nil
}

func resolveSiteDomainActionOptions(action string, opts siteDomainOptions, record map[string]any, promptCheckDomains bool) (siteDomainOptions, error) {
	var err error
	opts, err = resolveSiteDomainDomains(action, opts, record, promptCheckDomains)
	if err != nil {
		return opts, err
	}
	if action == "primary" && len(opts.domains) == 1 && !recordHasCachedExternalSiteDomain(record, opts.domains[0]) {
		return opts, uncachedPrimaryDomainError(siteRecordEnvID(record), opts.domains[0])
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(record["provider"])))
	switch provider {
	case "linode":
		opts, err = resolveSiteDomainProxyDecision(action, opts, record)
		if err != nil {
			return opts, err
		}
	case "kinsta":
		if opts.proxySet {
			return opts, ProjectError{Msg: "--proxy and --no-proxy only apply to Linode domains"}
		}
	default:
		if opts.proxySet {
			return opts, ProjectError{Msg: "--proxy and --no-proxy only apply to Linode domains"}
		}
	}
	if action == "primary" {
		opts, err = resolveSiteDomainSearchDecision(action, opts)
		if err != nil {
			return opts, err
		}
	}
	return opts, nil
}

func resolveSiteDomainDomains(action string, opts siteDomainOptions, record map[string]any, promptCheckDomains bool) (siteDomainOptions, error) {
	if len(opts.domains) > 0 {
		return opts, nil
	}
	switch action {
	case "add":
		if opts.nonInteractive {
			return opts, ProjectError{Msg: "domain add requires at least one domain in non-interactive mode"}
		}
		if !siteIsInteractiveFn() {
			return opts, ProjectError{Msg: "domain add requires at least one domain"}
		}
		prompted, err := siteDomainPromptStringFn("Domain(s) to add", "", false)
		if err != nil {
			return opts, err
		}
		domains, err := normalizePublicDomainList(splitSiteDomainPromptDomains(prompted))
		if err != nil {
			return opts, err
		}
		if len(domains) == 0 {
			return opts, ProjectError{Msg: "domain add requires at least one domain"}
		}
		opts.domains = domains
	case "primary":
		if opts.nonInteractive {
			return opts, ProjectError{Msg: "domain primary requires exactly one domain in non-interactive mode"}
		}
		if !siteIsInteractiveFn() {
			return opts, ProjectError{Msg: "domain primary requires exactly one domain"}
		}
		selected, err := promptSiteDomainSingleDomain(record, "make primary")
		if err != nil {
			return opts, err
		}
		opts.domains = []string{selected}
	case "remove":
		if opts.nonInteractive {
			return opts, ProjectError{Msg: "domain remove requires at least one domain in non-interactive mode"}
		}
		if !siteIsInteractiveFn() {
			return opts, ProjectError{Msg: "domain remove requires at least one domain"}
		}
		selected, err := promptSiteDomainMultipleDomains(record, "remove")
		if err != nil {
			return opts, err
		}
		opts.domains = selected
	case "check":
		if !promptCheckDomains {
			return opts, nil
		}
		if opts.nonInteractive || !siteIsInteractiveFn() {
			return opts, nil
		}
		selected, err := promptSiteDomainSingleDomain(record, "check")
		if err != nil {
			return opts, err
		}
		opts.domains = []string{selected}
	}
	return opts, nil
}

func splitSiteDomainPromptDomains(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
}

func promptSiteDomainSingleDomain(record map[string]any, action string) (string, error) {
	options := siteDomainCachedExternalOptions(record, true)
	if len(options) == 0 {
		return "", ProjectError{Msg: "No cached external domains found. Add a domain first or pass one explicitly."}
	}
	return siteDomainSelectFn("Choose a domain to "+action, options)
}

func promptSiteDomainMultipleDomains(record map[string]any, action string) ([]string, error) {
	options := siteDomainCachedExternalOptions(record, false)
	if action == "remove" {
		options = siteDomainCachedExternalSecondaryOptions(record)
	}
	if len(options) == 0 {
		if action == "remove" {
			return nil, ProjectError{Msg: "No cached external secondary domains found. Make another domain primary first if needed."}
		}
		return nil, ProjectError{Msg: "No cached external domains found. Add a domain first or pass domains explicitly."}
	}
	multiSelectFn := siteDomainMultiSelectFn
	if action == "remove" {
		multiSelectFn = siteDomainMultiSelectNoneFn
	}
	selected, err := multiSelectFn("Choose domains to "+action, options)
	if err != nil {
		return nil, err
	}
	domains, err := normalizePublicDomainList(selected)
	if err != nil {
		return nil, err
	}
	if len(domains) == 0 {
		return nil, ProjectError{Msg: "At least one domain must be selected."}
	}
	return domains, nil
}

func siteDomainCachedExternalOptions(record map[string]any, defaultPrimary bool) []ui.SelectOption {
	options := []ui.SelectOption{}
	for _, domain := range siteDomainListDomains(record) {
		if domain.management != "external" || domain.name == "" {
			continue
		}
		label := domain.name
		if domain.role != "" {
			label += " (" + domain.role + ")"
		}
		options = append(options, ui.SelectOption{Value: domain.name, Label: label, Default: defaultPrimary && domain.role == "primary"})
	}
	return options
}

func siteDomainCachedExternalSecondaryOptions(record map[string]any) []ui.SelectOption {
	options := []ui.SelectOption{}
	for _, domain := range siteDomainListDomains(record) {
		if domain.management != "external" || domain.role != "secondary" || domain.name == "" {
			continue
		}
		label := domain.name
		if domain.role != "" {
			label += " (" + domain.role + ")"
		}
		options = append(options, ui.SelectOption{Value: domain.name, Label: label})
	}
	return options
}

func cachedSiteDomainProxyMode(record map[string]any, domains []string) (string, bool, error) {
	targets := siteDomainNameSet(domains)
	if len(targets) == 0 {
		return "", false, nil
	}
	domainEntries := siteDomainEntryValues(record["domains"])
	if len(domainEntries) > 0 {
		matched := map[string]bool{}
		modes := map[string]bool{}
		cachedMode := ""
		for _, entry := range domainEntries {
			typed, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			name := normalizeDomainName(siteDomainEntryName(entry))
			if !targets[name] {
				continue
			}
			management := firstNonEmpty(normalizeSiteDomainManagement(firstRecordString(typed, "management")), "external")
			if management != "external" {
				continue
			}
			proxyMode, err := normalizeSiteDomainProxyMode(firstRecordString(typed, "proxy_mode"))
			if err != nil {
				return "", false, err
			}
			matched[name] = true
			modes[proxyMode] = true
			cachedMode = proxyMode
		}
		if len(matched) != len(targets) {
			return "", false, nil
		}
		if len(modes) > 1 {
			return "", false, ProjectError{Msg: "cached domains use mixed proxy modes; pass domains with matching proxy modes or specify --proxy/--no-proxy"}
		}
		return cachedMode, true, nil
	}

	proxyMode, err := normalizeSiteDomainProxyMode(firstRecordString(record, "proxy_mode"))
	if err != nil {
		return "", false, err
	}
	if proxyMode == "" {
		return "", false, nil
	}
	return proxyMode, true, nil
}

func siteDomainPlanProxyModes(record map[string]any, domains []string, requestedDomains map[string]bool, proxyMode string) (map[string]string, error) {
	modes := map[string]string{}
	for _, domain := range domains {
		name := normalizeDomainName(domain)
		if name == "" {
			continue
		}
		mode := proxyMode
		if !requestedDomains[name] {
			cachedMode, ok, err := cachedSiteDomainProxyMode(record, []string{name})
			if err != nil {
				return nil, err
			}
			if ok {
				mode = cachedMode
			}
		}
		modes[name] = mode
	}
	return modes, nil
}

func (plan siteDomainPlan) proxyModeForDomain(domain string) string {
	if mode, ok := plan.DomainProxyModes[normalizeDomainName(domain)]; ok {
		return mode
	}
	return plan.ProxyMode
}

func (plan siteDomainPlan) withProxyModeForDomain(domain string) siteDomainPlan {
	plan.ProxyMode = plan.proxyModeForDomain(domain)
	return plan
}

func resolveSiteDomainProxyDecision(action string, opts siteDomainOptions, record map[string]any) (siteDomainOptions, error) {
	if opts.proxySet {
		return opts, nil
	}
	proxyDomains := opts.domains
	if action == "check" && len(proxyDomains) == 0 {
		proxyDomains = cachedExternalSiteDomainNames(record)
	}
	cachedProxyMode, cachedProxyModeKnown, err := cachedSiteDomainProxyMode(record, proxyDomains)
	if err != nil {
		return opts, err
	}
	if action == "check" && cachedProxyModeKnown {
		opts.proxyMode = cachedProxyMode
		opts.proxySet = true
		return opts, nil
	}
	if opts.nonInteractive {
		if cachedProxyModeKnown {
			opts.proxyMode = cachedProxyMode
			opts.proxySet = true
			return opts, nil
		}
		return opts, ProjectError{Msg: fmt.Sprintf("domain %s requires --proxy or --no-proxy in non-interactive mode", action)}
	}
	if !siteIsInteractiveFn() {
		if cachedProxyModeKnown {
			opts.proxyMode = cachedProxyMode
			opts.proxySet = true
			return opts, nil
		}
		return opts, ProjectError{Msg: fmt.Sprintf("domain %s requires --proxy or --no-proxy", action)}
	}
	options := []ui.SelectOption{
		{Value: "direct", Label: "Direct (no proxy)", Default: !cachedProxyModeKnown || cachedProxyMode == ""},
		{Value: "cloudflare", Label: "Cloudflare", Default: cachedProxyModeKnown && cachedProxyMode == "cloudflare"},
		{Value: "ip", Label: "Reverse proxy IP", Default: cachedProxyModeKnown && cachedProxyMode != "" && cachedProxyMode != "cloudflare"},
	}
	selected, err := siteDomainSelectFn("Choose Linode proxy mode", options)
	if err != nil {
		return opts, err
	}
	switch selected {
	case "direct":
		opts.proxyMode = ""
	case "cloudflare":
		opts.proxyMode = "cloudflare"
	case "ip":
		defaultIP := ""
		if cachedProxyModeKnown && cachedProxyMode != "" && cachedProxyMode != "cloudflare" {
			defaultIP = cachedProxyMode
		}
		value, err := siteDomainPromptStringFn("Reverse proxy public IP", defaultIP, false)
		if err != nil {
			return opts, err
		}
		normalized, err := normalizeSiteDomainProxyMode(value)
		if err != nil {
			return opts, err
		}
		if normalized == "" || normalized == "cloudflare" {
			return opts, ProjectError{Msg: "reverse proxy mode requires an IP address"}
		}
		opts.proxyMode = normalized
	}
	opts.proxySet = true
	return opts, nil
}

func resolveSiteDomainSearchDecision(action string, opts siteDomainOptions) (siteDomainOptions, error) {
	if opts.searchSet {
		return opts, nil
	}
	if opts.nonInteractive {
		return opts, ProjectError{Msg: fmt.Sprintf("domain %s requires --search-replace or --no-search-replace in non-interactive mode", action)}
	}
	if !siteIsInteractiveFn() {
		return opts, ProjectError{Msg: fmt.Sprintf("domain %s requires --search-replace or --no-search-replace", action)}
	}
	selected, err := siteDomainSelectFn("Database search-replace", []ui.SelectOption{
		{Value: "yes", Label: "Yes, replace old/internal URLs"},
		{Value: "no", Label: "No, only update home/siteurl"},
	})
	if err != nil {
		return opts, err
	}
	opts.searchReplace = selected == "yes"
	opts.searchSet = true
	return opts, nil
}

func buildSiteDomainPlan(siteID, env, action string, opts siteDomainOptions) (siteDomainPlan, error) {
	record, _, err := cachedSiteEnv(siteID, env)
	if err != nil {
		return siteDomainPlan{}, err
	}
	if record == nil {
		return siteDomainPlan{}, ProjectError{Msg: fmt.Sprintf("No cached remote env matched %q.", canonicalEnvID(siteID, env))}
	}
	domains := append([]string{}, opts.domains...)
	if action == "check" && len(domains) == 0 {
		domains = cachedExternalSiteDomainNames(record)
		if len(domains) == 0 {
			return siteDomainPlan{}, ProjectError{Msg: fmt.Sprintf("No cached external domains matched %q. Add a domain first or pass domains explicitly.", canonicalEnvID(siteID, env))}
		}
	}
	domains = uniqueDomainList(domains)
	if len(domains) == 0 {
		return siteDomainPlan{}, ProjectError{Msg: fmt.Sprintf("domain %s requires at least one domain", action)}
	}
	requestedDomains := append([]string{}, domains...)
	requestedDomainSet := siteDomainNameSet(requestedDomains)
	currentURL := firstRecordString(record, "url", "site_url", "home_url")
	currentHostname := hostnameFromURLish(firstNonEmpty(currentURL, firstRecordString(record, "hostname")))
	internalHostname := firstRecordString(record, "internal_hostname")
	internalURL := firstRecordString(record, "internal_url")
	if internalHostname == "" && looksLikeInternalSiteHostname(record, currentHostname) {
		internalHostname = currentHostname
	}
	if internalURL == "" && internalHostname != "" {
		internalURL = firstNonEmpty(currentURL, "https://"+internalHostname)
	}
	if action == "remove" {
		for _, domain := range domains {
			if siteDomainIsDefaultHostname(record, domain) {
				return siteDomainPlan{}, ProjectError{Msg: fmt.Sprintf("%s is an nf-managed default domain for %s and cannot be removed with domain remove.", domain, canonicalEnvID(siteID, env))}
			}
			if !recordHasCachedExternalSecondarySiteDomain(record, domain) {
				return siteDomainPlan{}, ProjectError{Msg: fmt.Sprintf("%s is not a cached external secondary domain for %s. Only external secondary domains can be removed; make another domain primary first if needed.", normalizeDomainName(domain), canonicalEnvID(siteID, env))}
			}
		}
	}
	existingPrimary := cachedExternalPrimaryDomain(record)
	primary := action == "primary"
	canonical := domains[0]
	if primary && !recordHasCachedExternalSiteDomain(record, canonical) {
		return siteDomainPlan{}, uncachedPrimaryDomainError(canonicalEnvID(siteID, env), canonical)
	}
	if primary {
		for _, existing := range cachedExternalSiteDomainNames(record) {
			if existing != canonical {
				domains = uniqueDomainList(append(domains, existing))
			}
		}
	}
	aliases := []string{}
	for _, domain := range domains {
		if domain != canonical {
			aliases = append(aliases, domain)
		}
	}
	target, err := envRemoteSyncTargetFromSiteRecord(record, canonicalEnvID(siteID, env), siteID, env)
	if err != nil {
		return siteDomainPlan{}, err
	}
	redirectTarget := existingPrimary
	if primary {
		redirectTarget = canonical
	}
	if action == "add" && firstNonEmpty(redirectTarget, currentHostname, internalHostname) == "" {
		return siteDomainPlan{}, ProjectError{Msg: "Domain add requires an existing primary or internal domain to redirect to."}
	}
	proxyMode := opts.proxyMode
	if !opts.proxySet && strings.TrimSpace(proxyMode) == "" {
		cachedProxyMode, cachedProxyModeKnown, err := cachedSiteDomainProxyMode(record, requestedDomains)
		if err != nil {
			return siteDomainPlan{}, err
		}
		if cachedProxyModeKnown {
			proxyMode = cachedProxyMode
		}
	}
	plan := siteDomainPlan{
		Action:           action,
		EnvID:            canonicalEnvID(siteID, env),
		SiteID:           siteID,
		SiteName:         siteRecordName(record),
		Env:              env,
		Provider:         target.Provider,
		Record:           record,
		Target:           target,
		Canonical:        canonical,
		Aliases:          aliases,
		Domains:          domains,
		Primary:          primary,
		RedirectTarget:   firstNonEmpty(redirectTarget, currentHostname, internalHostname),
		ProxyMode:        proxyMode,
		SearchReplace:    opts.searchReplace,
		DeleteCert:       opts.deleteCert,
		CurrentURL:       currentURL,
		CurrentHostname:  currentHostname,
		InternalURL:      internalURL,
		InternalHostname: internalHostname,
		FileSlug:         envIDFileSlug(firstNonEmpty(siteRecordEnvID(record), canonicalEnvID(siteID, env))),
		PHPVersion:       sitePHPVersion(record),
	}
	switch plan.Provider {
	case "kinsta":
		if opts.deleteCert {
			return siteDomainPlan{}, ProjectError{Msg: "--delete-cert only applies to Linode domains"}
		}
		if opts.proxySet || strings.TrimSpace(plan.ProxyMode) != "" {
			return siteDomainPlan{}, ProjectError{Msg: "--proxy and --no-proxy only apply to Linode domains"}
		}
		plan.KinstaSiteID = mapStringAtPath(record, "kinsta", "site_id")
		plan.KinstaEnvID = mapStringAtPath(record, "kinsta", "environment_id")
		plan.KinstaDomainID = mapStringAtPath(record, "kinsta", "domain_id")
		if plan.KinstaEnvID == "" {
			return siteDomainPlan{}, ProjectError{Msg: fmt.Sprintf("Kinsta env %q is missing API identifiers. Run nf site refresh and try again.", plan.EnvID)}
		}
	case "linode":
		proxyMode, err := normalizeSiteDomainProxyMode(plan.ProxyMode)
		if err != nil {
			return siteDomainPlan{}, err
		}
		plan.ProxyMode = proxyMode
		domainProxyModes, err := siteDomainPlanProxyModes(record, domains, requestedDomainSet, proxyMode)
		if err != nil {
			return siteDomainPlan{}, err
		}
		plan.DomainProxyModes = domainProxyModes
		resolvedTarget, err := cachedSiteTarget(target.TargetRef)
		if err != nil {
			return siteDomainPlan{}, err
		}
		if resolvedTarget == nil {
			return siteDomainPlan{}, ProjectError{Msg: fmt.Sprintf("Linode site %q references target %q, but no cached target matched. Run nf provider check linode.", siteSummary(record), target.TargetRef)}
		}
		plan.TargetHostname = firstNonEmpty(firstRecordString(resolvedTarget, "hostname", "host"), target.SSHHost)
		plan.TargetIPv4 = firstRecordString(resolvedTarget, "public_ipv4", "ipv4", "ip")
		plan.TargetIPv6 = firstRecordString(resolvedTarget, "public_ipv6", "ipv6")
		plan.PHPVersion = firstNonEmpty(plan.PHPVersion, targetPHPVersion(resolvedTarget), "8.3")
	default:
		if opts.proxySet || strings.TrimSpace(plan.ProxyMode) != "" {
			return siteDomainPlan{}, ProjectError{Msg: "--proxy and --no-proxy only apply to Linode domains"}
		}
		return siteDomainPlan{}, ProjectError{Msg: fmt.Sprintf("domain is not implemented for provider %q", plan.Provider)}
	}
	return plan, nil
}

func normalizePublicDomain(input string) (string, error) {
	domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input), "."))
	if domain == "" {
		return "", ProjectError{Msg: "domain cannot be empty"}
	}
	if strings.Contains(domain, "://") || strings.ContainsAny(domain, "/\\?#@:") {
		return "", ProjectError{Msg: fmt.Sprintf("domain %q must be a hostname, not a URL", input)}
	}
	if strings.ContainsAny(domain, " \t\r\n") || strings.Contains(domain, "..") || !strings.Contains(domain, ".") {
		return "", ProjectError{Msg: fmt.Sprintf("domain %q is not a valid public hostname", input)}
	}
	if len(domain) > 253 {
		return "", ProjectError{Msg: fmt.Sprintf("domain %q is too long", input)}
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", ProjectError{Msg: fmt.Sprintf("domain %q is not a valid public hostname", input)}
		}
		for _, r := range label {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
				continue
			}
			return "", ProjectError{Msg: fmt.Sprintf("domain %q is not a valid public hostname", input)}
		}
	}
	return domain, nil
}

func normalizePublicDomainList(raw []string) ([]string, error) {
	domains := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, value := range raw {
		domain, err := normalizePublicDomain(value)
		if err != nil {
			return nil, err
		}
		if seen[domain] {
			continue
		}
		seen[domain] = true
		domains = append(domains, domain)
	}
	return domains, nil
}

func uniqueDomainList(raw []string) []string {
	values := []string{}
	seen := map[string]bool{}
	for _, value := range raw {
		domain := normalizeDomainName(hostnameFromURLish(value))
		if domain == "" || seen[domain] {
			continue
		}
		seen[domain] = true
		values = append(values, domain)
	}
	return values
}

func cachedExternalPrimaryDomain(record map[string]any) string {
	for _, domain := range siteDomainListDomains(record) {
		if domain.management == "external" && domain.role == "primary" {
			return domain.name
		}
	}
	return ""
}

func normalizeSiteDomainProxyMode(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	switch strings.ToLower(trimmed) {
	case "", "none", "direct", "dns-only", "dns_only":
		return "", nil
	case "cloudflare", "cloudflare-strict", "cloudflare_strict":
		return "cloudflare", nil
	}
	addr, err := netip.ParseAddr(trimmed)
	if err == nil {
		return addr.String(), nil
	}
	return "", ProjectError{Msg: "--proxy must be cloudflare or an IP address"}
}

func displaySiteDomainProxyMode(value string) string {
	if value == "cloudflare" || value == "cloudflare-strict" || value == "cloudflare_strict" {
		return "cloudflare"
	}
	if addr, err := netip.ParseAddr(strings.TrimSpace(value)); err == nil {
		return addr.String()
	}
	return value
}

func siteDomainCloudflareStrict(plan siteDomainPlan) bool {
	return plan.ProxyMode == "cloudflare"
}

func siteDomainCloudflareProxy(plan siteDomainPlan) bool {
	return siteDomainCloudflareStrict(plan)
}

func siteDomainReverseProxyIP(plan siteDomainPlan) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(strings.TrimSpace(plan.ProxyMode))
	if err != nil {
		return netip.Addr{}, false
	}
	return addr, true
}

func siteDomainProxyIPRecordType(addr netip.Addr) string {
	if addr.Is6() {
		return "AAAA"
	}
	return "A"
}

func siteDomainBundledCloudflareIPRangeStrings() []string {
	ranges := bundledCloudflareIPRanges()
	out := make([]string, 0, len(ranges.Prefixes))
	for _, prefix := range ranges.Prefixes {
		out = append(out, prefix.String())
	}
	return out
}

func hostnameFromURLish(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if _, after, ok := strings.Cut(value, "://"); ok {
		value = after
	}
	if before, _, ok := strings.Cut(value, "/"); ok {
		value = before
	}
	if before, _, ok := strings.Cut(value, ":"); ok {
		value = before
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
}

func looksLikeInternalSiteHostname(record map[string]any, host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(record["provider"])))
	if provider == "kinsta" {
		return strings.Contains(host, ".kinsta.")
	}
	target := strings.ToLower(strings.TrimSpace(siteProviderTarget(record)))
	return target != "" && (strings.Contains(host, "."+target+".") || strings.HasSuffix(host, "."+target))
}

func siteDomainDefaultHostname(record map[string]any) string {
	if host := normalizeDomainName(hostnameFromURLish(firstRecordString(record, "internal_hostname"))); host != "" {
		return host
	}
	if host := normalizeDomainName(hostnameFromURLish(firstRecordString(record, "hostname"))); looksLikeInternalSiteHostname(record, host) {
		return host
	}
	if host := normalizeDomainName(hostnameFromURLish(firstRecordString(record, "url", "site_url", "home_url"))); looksLikeInternalSiteHostname(record, host) {
		return host
	}
	return ""
}

func siteDomainIsDefaultHostname(record map[string]any, host string) bool {
	host = normalizeDomainName(hostnameFromURLish(host))
	if host == "" {
		return false
	}
	if defaultHost := siteDomainDefaultHostname(record); defaultHost != "" && host == defaultHost {
		return true
	}
	return looksLikeInternalSiteHostname(record, host)
}

func (p siteDomainPlan) allDomains() []string {
	if len(p.Domains) > 0 {
		return append([]string{}, p.Domains...)
	}
	domains := []string{p.Canonical}
	domains = append(domains, p.Aliases...)
	return domains
}

func (p siteDomainProviderResult) domainID(name string) string {
	needle := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	for _, domain := range p.Domains {
		if strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain.Name), ".")) == needle {
			return domain.DomainID
		}
	}
	return ""
}

func printSiteDomainPlan(plan siteDomainPlan, mode string) {
	title := "Add domain plan:"
	if plan.Primary {
		title = "Primary domain plan:"
	} else if plan.Action == "remove" {
		title = "Remove domain plan:"
	}
	fmt.Println(title)
	fmt.Printf("  env:       %s\n", plan.EnvID)
	fmt.Printf("  provider:  %s\n", plan.Provider)
	if plan.Target.TargetRef != "" {
		fmt.Printf("  target:    %s\n", plan.Target.TargetRef)
	}
	if plan.CurrentURL != "" {
		fmt.Printf("  current:   %s\n", plan.CurrentURL)
	}
	if plan.InternalURL != "" {
		fmt.Printf("  fallback:  %s\n", plan.InternalURL)
	}
	if plan.Action == "remove" {
		fmt.Printf("  domains:   %s\n", strings.Join(plan.allDomains(), ", "))
	} else if plan.Primary {
		fmt.Printf("  primary:   %s\n", plan.Canonical)
		if len(plan.Aliases) > 0 {
			fmt.Printf("  secondary: %s\n", strings.Join(plan.Aliases, ", "))
		}
	} else {
		fmt.Printf("  secondary: %s\n", strings.Join(plan.allDomains(), ", "))
		if plan.RedirectTarget != "" {
			fmt.Printf("  redirects: https://%s\n", plan.RedirectTarget)
		}
	}
	if plan.ProxyMode != "" {
		fmt.Printf("  proxy:     %s\n", displaySiteDomainProxyMode(plan.ProxyMode))
	} else if plan.Provider == "linode" {
		fmt.Println("  proxy:     none")
	}
	if plan.Provider == "kinsta" {
		if plan.Primary {
			fmt.Printf("  search-replace: %t\n", plan.SearchReplace)
		}
		fmt.Println("  public DNS: no DNS records will be changed by nf")
		if plan.Action == "remove" {
			fmt.Println("  provider: remove non-primary domain from Kinsta")
		} else {
			fmt.Println("  DNS records: returned by Kinsta after execution")
		}
	} else if plan.Provider == "linode" {
		fmt.Println("  public DNS: no DNS records will be changed by nf")
		if plan.Action == "remove" {
			fmt.Println("  provider: remove nf-managed domain vhosts, scripts, timers, and cached metadata")
		} else {
			printLinodeDomainDNSInstructions(plan)
		}
		if plan.Primary {
			fmt.Printf("  search-replace: %t\n", plan.SearchReplace)
		}
		if plan.Action == "remove" {
			if plan.DeleteCert {
				fmt.Println("  TLS: delete the Let's Encrypt certificate lineage")
			} else {
				fmt.Println("  TLS: certificate lineage is kept for rollback safety")
			}
		} else if siteDomainCloudflareStrict(plan) {
			fmt.Println("  TLS: Cloudflare uses Full (strict) with a public Let's Encrypt origin cert; certbot waits for Cloudflare DNS and ACME challenge reachability")
		} else if _, ok := siteDomainReverseProxyIP(plan); ok {
			fmt.Println("  TLS: reverse proxy terminates public HTTPS; Linode origin uses the target wildcard certificate")
		} else {
			fmt.Println("  TLS: HTTP-01 certbot retry timer will issue HTTPS after DNS points at the target")
		}
	}
	fmt.Printf("  local state: %s\n", state.StatePath("sites"))
	if plan.Provider == "linode" {
		fmt.Println("  remote state: /var/lib/nf/sites.json")
	}
	fmt.Printf("  mode:      %s\n", mode)
}

func printLinodeDomainDNSInstructions(plan siteDomainPlan) {
	domains := plan.allDomains()
	if siteDomainCloudflareProxy(plan) {
		fmt.Println("  client DNS records:")
		for _, domain := range domains {
			if plan.TargetIPv4 != "" {
				fmt.Printf("    Cloudflare proxied A     %s -> %s\n", domain, plan.TargetIPv4)
			}
			if plan.TargetIPv6 != "" {
				fmt.Printf("    Cloudflare proxied AAAA  %s -> %s\n", domain, plan.TargetIPv6)
			}
			if plan.TargetIPv4 == "" && plan.TargetIPv6 == "" && plan.TargetHostname != "" {
				fmt.Printf("    Cloudflare proxied record for %s should point at target %s\n", domain, plan.TargetHostname)
			}
		}
		fmt.Println("  Cloudflare SSL/TLS mode: Full (strict)")
		return
	}
	if proxyIP, ok := siteDomainReverseProxyIP(plan); ok {
		fmt.Println("  client DNS records:")
		for _, domain := range domains {
			fmt.Printf("    %s     %s -> %s\n", siteDomainProxyIPRecordType(proxyIP), domain, proxyIP.String())
		}
		origin := firstNonEmpty(plan.TargetHostname, plan.TargetIPv4, plan.TargetIPv6, plan.Target.SSHHost)
		if origin != "" {
			fmt.Println("  reverse proxy origin:")
			fmt.Printf("    upstream: https://%s\n", origin)
			fmt.Println("    Host header: preserve the requested public domain")
			fmt.Println("    origin TLS: target wildcard cert; disable hostname verification or trust the origin hostname")
		}
		return
	}
	if plan.TargetIPv4 != "" || plan.TargetIPv6 != "" {
		fmt.Println("  client DNS records:")
		for _, domain := range domains {
			if plan.TargetIPv4 != "" {
				fmt.Printf("    A     %s -> %s\n", domain, plan.TargetIPv4)
			}
			if plan.TargetIPv6 != "" {
				fmt.Printf("    AAAA  %s -> %s\n", domain, plan.TargetIPv6)
			}
		}
		return
	}
	if plan.TargetHostname != "" {
		fmt.Println("  client DNS records:")
		for _, domain := range domains {
			fmt.Printf("    point %s at target %s (A/AAAA records from the target)\n", domain, plan.TargetHostname)
		}
	}
}

func prepareKinstaSiteDomain(plan siteDomainPlan) (siteDomainProviderResult, error) {
	return runKinstaSiteDomain(plan, false)
}

func primaryKinstaSiteDomain(plan siteDomainPlan) (siteDomainProviderResult, error) {
	return runKinstaSiteDomain(plan, true)
}

func removeKinstaSiteDomain(plan siteDomainPlan) (siteDomainProviderResult, error) {
	token := envwizard.Value("KINSTA_API_KEY")
	if token == "" {
		return siteDomainProviderResult{}, fmt.Errorf("Expected KINSTA_API_KEY in the environment or %s.", config.EnvFile())
	}
	client := newKinstaClient(token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	domains, err := client.ListDomains(ctx, plan.KinstaEnvID)
	if err != nil {
		return siteDomainProviderResult{}, err
	}
	result := siteDomainProviderResult{Domains: []siteDomainProviderDomain{}}
	ids := []string{}
	for _, name := range plan.allDomains() {
		domain, ok := kinsta.FindDomain(domains, name)
		if !ok {
			fmt.Printf("Kinsta domain %s is not attached; skipping.\n", name)
			continue
		}
		if domain.IsPrimary {
			return siteDomainProviderResult{}, ProjectError{Msg: fmt.Sprintf("Kinsta domain %q is primary. Change primary to another domain before removing it.", name)}
		}
		ids = append(ids, domain.ID)
		result.Domains = append(result.Domains, siteDomainProviderDomain{Name: name, DomainID: domain.ID})
	}
	if len(ids) == 0 {
		return result, nil
	}
	fmt.Printf("Removing Kinsta domain%s %s...\n", pluralSuffix(len(ids)), strings.Join(plan.allDomains(), ", "))
	opID, err := client.DeleteDomains(ctx, plan.KinstaEnvID, ids)
	if err != nil {
		return siteDomainProviderResult{}, err
	}
	if err := waitKinstaOperation(ctx, client, opID); err != nil {
		return siteDomainProviderResult{}, err
	}
	return result, nil
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func runKinstaSiteDomain(plan siteDomainPlan, primary bool) (siteDomainProviderResult, error) {
	token := envwizard.Value("KINSTA_API_KEY")
	if token == "" {
		return siteDomainProviderResult{}, fmt.Errorf("Expected KINSTA_API_KEY in the environment or %s.", config.EnvFile())
	}
	client := newKinstaClient(token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	result := siteDomainProviderResult{Domains: make([]siteDomainProviderDomain, 0, 1+len(plan.Aliases))}
	canonicalDomain := kinsta.Domain{}
	for i, name := range plan.allDomains() {
		role := "secondary"
		if primary && i == 0 {
			role = "primary"
		}
		domain, err := ensureKinstaSiteDomain(ctx, client, plan.KinstaEnvID, name)
		if err != nil {
			return siteDomainProviderResult{}, err
		}
		if i == 0 {
			canonicalDomain = domain
		}
		records, err := kinstaSiteDomainRecords(ctx, client, domain, name)
		if err != nil {
			return siteDomainProviderResult{}, err
		}
		result.Domains = append(result.Domains, siteDomainProviderDomain{Name: name, Role: role, DomainID: domain.ID, Records: records})
	}
	if primary && canonicalDomain.ID != "" && !canonicalDomain.IsPrimary {
		fmt.Printf("Changing Kinsta primary domain to %s...\n", plan.Canonical)
		opID, err := client.ChangePrimaryDomain(ctx, plan.KinstaEnvID, canonicalDomain.ID, plan.SearchReplace)
		if err != nil {
			return siteDomainProviderResult{}, err
		}
		if err := waitKinstaOperation(ctx, client, opID); err != nil {
			return siteDomainProviderResult{}, err
		}
	}
	return result, nil
}

func ensureKinstaSiteDomain(ctx context.Context, client *kinsta.Client, envID, domainName string) (kinsta.Domain, error) {
	domains, err := client.ListDomains(ctx, envID)
	if err != nil {
		return kinsta.Domain{}, err
	}
	if domain, ok := kinsta.FindDomain(domains, domainName); ok {
		return domain, nil
	}
	fmt.Printf("Adding Kinsta domain %s...\n", domainName)
	opID, err := client.AddDomain(ctx, envID, kinsta.AddDomainRequest{DomainName: domainName, IsWildcardless: false, AddWithWWWSubdomain: false, SetupType: kinstaDomainSetupType})
	if err != nil {
		return kinsta.Domain{}, err
	}
	if err := waitKinstaOperation(ctx, client, opID); err != nil {
		return kinsta.Domain{}, err
	}
	domains, err = client.ListDomains(ctx, envID)
	if err != nil {
		return kinsta.Domain{}, err
	}
	if domain, ok := kinsta.FindDomain(domains, domainName); ok {
		return domain, nil
	}
	return kinsta.Domain{}, fmt.Errorf("Kinsta domain %q was added but was not found in domain list", domainName)
}

func kinstaSiteDomainRecords(ctx context.Context, client *kinsta.Client, domain kinsta.Domain, domainName string) (kinsta.DomainRecords, error) {
	records, err := client.DomainRecords(ctx, domain.ID)
	if err != nil {
		return kinsta.DomainRecords{}, err
	}
	if kinstaDomainRecordsHavePointing(records, domainName) {
		return records, nil
	}

	fmt.Printf("Waiting briefly for Kinsta routing records for %s...\n", domainName)
	waited, err := waitKinstaDomainPointingRecordsEvery(ctx, client, domain.ID, domainName, kinstaDomainAddPointingWaitTimeout, kinstaDomainAddPointingWaitInterval, nil)
	if err == nil {
		fmt.Printf("Kinsta returned routing records for %s.\n", domainName)
		return kinstaMergeDomainRecords(records, waited), nil
	}
	if kinstaDomainRecordsHaveAny(waited) {
		records = kinstaMergeDomainRecords(records, waited)
	}
	fmt.Printf("Kinsta has not returned routing records for %s yet. Open https://my.kinsta.com/sites/domains/ and follow Kinsta's domain DNS instructions for this site.\n", domainName)
	return records, nil
}

func printKinstaDomainRecords(result siteDomainProviderResult) {
	if len(result.Domains) == 0 {
		return
	}
	fmt.Println("Kinsta DNS records for client DNS:")
	for _, domain := range result.Domains {
		fmt.Printf("  %s (%s):\n", domain.Name, domain.Role)
		printed := printKinstaDomainRecordGroup("verification (prove ownership and TLS validation)", domain.Records.Verification)
		printed = printKinstaDomainRecordGroup("routing (point public DNS at Kinsta)", domain.Records.Pointing) || printed
		if !printed {
			fmt.Println("    Kinsta returned no DNS records.")
		}
	}
}

func printKinstaDomainRecordGroup(label string, records []kinsta.DNSRecord) bool {
	if len(records) == 0 {
		return false
	}
	fmt.Printf("    %s:\n", label)
	for _, record := range records {
		name := record.RecordName()
		recordType := strings.ToUpper(record.RecordTypeName())
		content := record.RecordContent()
		if name == "" || recordType == "" || content == "" {
			continue
		}
		if record.TTL > 0 {
			fmt.Printf("      %s  %s  %s  TTL %d\n", recordType, name, content, record.TTL)
		} else {
			fmt.Printf("      %s  %s  %s\n", recordType, name, content)
		}
	}
	return true
}

func updateSiteDomainCache(plan siteDomainPlan, result siteDomainProviderResult) error {
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		return err
	}
	updated := false
	for _, record := range records {
		if !siteEnvMatchesSite(record, plan.SiteID) || !siteEnvMatchesEnv(record, plan.Env) {
			continue
		}
		applySiteDomainCacheFields(record, plan, result)
		updated = true
	}
	if !updated {
		return ProjectError{Msg: fmt.Sprintf("No cached remote env matched %q.", plan.EnvID)}
	}
	return state.SaveStateRecords("sites", records)
}

func removeSiteDomainCache(plan siteDomainPlan) error {
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		return err
	}
	updated := false
	for _, record := range records {
		if !siteEnvMatchesSite(record, plan.SiteID) || !siteEnvMatchesEnv(record, plan.Env) {
			continue
		}
		applySiteDomainRemoveCacheFields(record, plan)
		updated = true
	}
	if !updated {
		return ProjectError{Msg: fmt.Sprintf("No cached remote env matched %q.", plan.EnvID)}
	}
	return state.SaveStateRecords("sites", records)
}

func applySiteDomainRemoveCacheFields(record map[string]any, plan siteDomainPlan) {
	removeSet := siteDomainNameSet(plan.allDomains())
	remaining := []any{}
	for _, entry := range siteDomainEntryValues(record["domains"]) {
		if removeSet[normalizeDomainName(siteDomainEntryName(entry))] {
			continue
		}
		remaining = append(remaining, entry)
	}
	if len(remaining) == 0 {
		delete(record, "domains")
		delete(record, "domain_state")
		delete(record, "proxy_mode")
	} else {
		record["domains"] = remaining
		delete(record, "domain_state")
		delete(record, "proxy_mode")
	}
	if removeSet[normalizeDomainName(firstRecordString(record, "primary_domain"))] || removeSet[normalizeDomainName(hostnameFromURLish(firstNonEmpty(firstRecordString(record, "url", "site_url", "home_url"), firstRecordString(record, "hostname"))))] {
		delete(record, "primary_domain")
		if plan.InternalHostname != "" {
			record["hostname"] = plan.InternalHostname
		}
		if plan.InternalURL != "" {
			record["url"] = plan.InternalURL
		}
	}
	if plan.Provider == "kinsta" {
		kinstaData := mapMapAtPath(record, "kinsta")
		if kinstaData != nil && removeSet[normalizeDomainName(plan.Canonical)] && firstRecordString(record, "primary_domain") == "" {
			delete(kinstaData, "domain_id")
		}
	}
}

func applySiteDomainCacheFields(record map[string]any, plan siteDomainPlan, result siteDomainProviderResult) {
	if firstRecordString(record, "internal_hostname") == "" && plan.InternalHostname != "" {
		record["internal_hostname"] = plan.InternalHostname
	}
	if firstRecordString(record, "internal_url") == "" && plan.InternalURL != "" {
		record["internal_url"] = plan.InternalURL
	}
	existing := []map[string]any{}
	index := map[string]int{}
	for _, entry := range siteDomainEntryValues(record["domains"]) {
		copied := siteDomainEntryMap(entry)
		name := normalizeDomainName(siteDomainEntryName(copied))
		if name == "" {
			continue
		}
		copied["name"] = name
		copied["role"] = normalizeSiteDomainRole(firstRecordString(copied, "role", "type"))
		copied["management"] = firstNonEmpty(normalizeSiteDomainManagement(firstRecordString(copied, "management")), "external")
		copied["status"] = normalizeSiteDomainStatus(firstRecordString(copied, "status", "domain_state"))
		index[name] = len(existing)
		existing = append(existing, copied)
	}
	if plan.Primary {
		for _, entry := range existing {
			if firstRecordString(entry, "management") == "external" {
				entry["role"] = "secondary"
			}
		}
	}
	for _, entry := range siteDomainCacheEntries(plan, result) {
		name := firstRecordString(entry, "name")
		if i, ok := index[name]; ok {
			for key, value := range entry {
				existing[i][key] = value
			}
			continue
		}
		index[name] = len(existing)
		existing = append(existing, entry)
	}
	if len(existing) > 0 {
		record["domains"] = existing
	} else {
		delete(record, "domains")
	}
	delete(record, "domain_state")
	delete(record, "proxy_mode")
	if plan.Primary {
		record["hostname"] = plan.Canonical
		record["url"] = "https://" + plan.Canonical
		record["primary_domain"] = plan.Canonical
		if plan.Provider == "kinsta" {
			kinstaData := mapMapAtPath(record, "kinsta")
			if kinstaData == nil {
				kinstaData = map[string]any{}
				record["kinsta"] = kinstaData
			}
			if domainID := result.domainID(plan.Canonical); domainID != "" {
				kinstaData["domain_id"] = domainID
			}
		}
	}
}

func siteDomainCacheEntries(plan siteDomainPlan, result siteDomainProviderResult) []map[string]any {
	entries := make([]map[string]any, 0, len(plan.allDomains()))
	for i, name := range plan.allDomains() {
		role := "secondary"
		if plan.Primary && i == 0 && name == plan.Canonical {
			role = "primary"
		}
		status := "pending"
		if plan.Action == "primary" && role == "primary" {
			status = "active"
		}
		entry := map[string]any{"name": name, "role": role, "management": "external", "status": status}
		if proxyMode := plan.proxyModeForDomain(name); proxyMode != "" {
			entry["proxy_mode"] = proxyMode
		}
		if domainID := result.domainID(name); domainID != "" {
			entry["domain_id"] = domainID
		}
		entries = append(entries, entry)
	}
	return entries
}

func siteDomainEntryMap(entry any) map[string]any {
	out := map[string]any{}
	if typed, ok := entry.(map[string]any); ok {
		for key, value := range typed {
			out[key] = value
		}
	}
	return out
}

func normalizeDomainName(value string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
}

func siteDomainArtifactSlug(domain string) string {
	domain = normalizeDomainName(domain)
	var b strings.Builder
	lastDash := false
	for _, r := range domain {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if valid {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "domain"
	}
	return out
}

type siteDomainLinodeArtifacts struct {
	Domain        string
	Slug          string
	ServiceName   string
	RefreshScript string
	IssueScript   string
	Vhost         string
	Enabled       string
	CertDir       string
}

func linodeDomainArtifacts(domain string) siteDomainLinodeArtifacts {
	slug := siteDomainArtifactSlug(domain)
	return siteDomainLinodeArtifacts{
		Domain:        normalizeDomainName(domain),
		Slug:          slug,
		ServiceName:   "nf-domain-" + slug + "-tls",
		RefreshScript: "/usr/local/bin/nf-refresh-domain-" + slug,
		IssueScript:   "/usr/local/bin/nf-issue-domain-cert-" + slug,
		Vhost:         "/etc/nginx/sites-available/nf-site-domain-" + slug,
		Enabled:       "/etc/nginx/sites-enabled/nf-site-domain-" + slug,
		CertDir:       "/etc/letsencrypt/live/" + normalizeDomainName(domain),
	}
}

func siteDomainNameSet(domains []string) map[string]bool {
	set := map[string]bool{}
	for _, domain := range domains {
		if normalized := normalizeDomainName(domain); normalized != "" {
			set[normalized] = true
		}
	}
	return set
}

func siteDomainEntryValues(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []map[string]any:
		out := make([]any, 0, len(typed))
		for _, entry := range typed {
			out = append(out, entry)
		}
		return out
	default:
		return nil
	}
}

func siteDomainEntryName(entry any) string {
	if typed, ok := entry.(map[string]any); ok {
		return firstRecordString(typed, "name", "domain", "domain_name", "hostname")
	}
	return ""
}

func cachedSiteDomainNames(record map[string]any) []string {
	seen := map[string]bool{}
	values := []string{}
	add := func(value string) {
		value = normalizeDomainName(hostnameFromURLish(value))
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		values = append(values, value)
	}
	for _, entry := range siteDomainEntryValues(record["domains"]) {
		add(siteDomainEntryName(entry))
	}
	add(firstRecordString(record, "primary_domain"))
	if host := hostnameFromURLish(firstRecordString(record, "hostname")); !looksLikeInternalSiteHostname(record, host) {
		add(host)
	}
	if host := hostnameFromURLish(firstRecordString(record, "url", "site_url", "home_url")); !looksLikeInternalSiteHostname(record, host) {
		add(host)
	}
	return values
}

func cachedExternalSiteDomainNames(record map[string]any) []string {
	seen := map[string]bool{}
	values := []string{}
	add := func(value string) {
		value = normalizeDomainName(hostnameFromURLish(value))
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		values = append(values, value)
	}
	for _, entry := range siteDomainEntryValues(record["domains"]) {
		management := "external"
		if typed, ok := entry.(map[string]any); ok {
			management = firstNonEmpty(normalizeSiteDomainManagement(firstRecordString(typed, "management")), "external")
		}
		if management == "external" {
			add(siteDomainEntryName(entry))
		}
	}
	add(firstRecordString(record, "primary_domain"))
	if host := hostnameFromURLish(firstRecordString(record, "hostname")); !looksLikeInternalSiteHostname(record, host) {
		add(host)
	}
	if host := hostnameFromURLish(firstRecordString(record, "url", "site_url", "home_url")); !looksLikeInternalSiteHostname(record, host) {
		add(host)
	}
	return values
}

func recordHasSiteDomain(record map[string]any, domain string) bool {
	needle := normalizeDomainName(domain)
	if needle == "" {
		return false
	}
	for _, cached := range cachedSiteDomainNames(record) {
		if cached == needle {
			return true
		}
	}
	return false
}

func recordHasCachedExternalSiteDomain(record map[string]any, domain string) bool {
	needle := normalizeDomainName(domain)
	if needle == "" {
		return false
	}
	for _, cached := range cachedExternalSiteDomainNames(record) {
		if cached == needle {
			return true
		}
	}
	return false
}

func recordHasCachedExternalSecondarySiteDomain(record map[string]any, domain string) bool {
	needle := normalizeDomainName(domain)
	if needle == "" {
		return false
	}
	for _, cached := range siteDomainListDomains(record) {
		if cached.name == needle && cached.management == "external" && cached.role == "secondary" {
			return true
		}
	}
	return false
}

func uncachedPrimaryDomainError(envID, domain string) ProjectError {
	domain = normalizeDomainName(domain)
	envID = strings.TrimSpace(envID)
	return ProjectError{Msg: fmt.Sprintf("%s is not a cached external domain for %s. Run nf domain add %s %s first.", domain, envID, envID, domain)}
}

func printSiteDomainStaleWarnings(plan siteDomainPlan) {
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		return
	}
	printed := map[string]bool{}
	for _, record := range records {
		if siteEnvMatchesSite(record, plan.SiteID) && siteEnvMatchesEnv(record, plan.Env) {
			continue
		}
		for _, domain := range plan.allDomains() {
			if !recordHasSiteDomain(record, domain) {
				continue
			}
			envID := siteRecordEnvID(record)
			if envID == "" || printed[envID+"|"+domain] {
				continue
			}
			printed[envID+"|"+domain] = true
			proxyMode := ""
			if cachedProxyMode, ok, err := cachedSiteDomainProxyMode(record, []string{domain}); err == nil && ok {
				proxyMode = cachedProxyMode
			}
			proxyArg := siteDomainProxyArg(proxyMode)
			fmt.Printf("Warning: %s is also cached on %s; after cutover run: nf domain remove %s %s%s\n", domain, envID, envID, domain, proxyArg)
		}
	}
}

func linodeDomainCacheUpdateJQFilter() string {
	return `  map(if (.site_id == $site_id and .env == $env) then
    (
      (if ((.internal_hostname // "") == "" and $internal_hostname != "") then .internal_hostname = $internal_hostname else . end)
      | (if ((.internal_url // "") == "" and $internal_url != "") then .internal_url = $internal_url else . end)
      | (.domains = (((.domains // []) | map(select((.name // .domain // .domain_name // .hostname // "") as $name | (($names | index($name)) == null))) | map(if $primary == "1" and ((.management // "external") == "external") then (.role = "secondary") else . end)) + $domains))
      | del(.domain_state, .proxy_mode)
      | (if $primary == "1" then (.hostname = $canonical | .url = $url | .primary_domain = $canonical) else . end)
    )
  else . end)`
}

func linodeDomainCacheRemoveJQFilter() string {
	return `  map(if (.site_id == $site_id and .env == $env) then
    (.domains = ((.domains // []) | map(select((.name // .domain // .domain_name // .hostname // "") as $name | (($remove_domains | index($name)) == null)))))
    | (if ((.domains // []) | length) == 0 then del(.domains, .domain_state, .proxy_mode) else del(.domain_state, .proxy_mode) end)
    | (if $reset_primary == "1" then (del(.primary_domain) | (if $internal_hostname != "" then .hostname = $internal_hostname else . end) | (if $internal_url != "" then .url = $internal_url else . end)) else . end)
  else . end)`
}

func renderLinodeDomainBindingScript(plan siteDomainPlan) string {
	q := shellQuoteArg
	domains := plan.allDomains()
	domainEntries, _ := json.Marshal(siteDomainCacheEntries(plan, siteDomainProviderResult{}))
	domainNames, _ := json.Marshal(domains)
	fileSlug := plan.FileSlug
	if fileSlug == "" {
		fileSlug = envIDFileSlug(plan.EnvID)
	}
	primaryFlag := "0"
	if plan.Primary {
		primaryFlag = "1"
	}
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	b.WriteString("install -d -m 0755 /var/www/letsencrypt /etc/nginx/conf.d /etc/nginx/sites-available /etc/nginx/sites-enabled /etc/systemd/system /var/lib/nf\n")
	b.WriteString("cat >/etc/nginx/conf.d/nf-server-names-hash.conf <<'EOF'\n")
	b.WriteString("server_names_hash_bucket_size 128;\nserver_names_hash_max_size 4096;\n")
	b.WriteString("EOF\n")
	b.WriteString("touch /var/lib/nf/sites.json\n")
	b.WriteString("if ! jq empty /var/lib/nf/sites.json >/dev/null 2>&1; then printf '[]\\n' >/var/lib/nf/sites.json; fi\n")
	b.WriteString("exec 9>/run/nf-domain-state.lock\nflock 9\n")
	b.WriteString("domains=(")
	b.WriteString(shellArrayValues(domains))
	b.WriteString(")\n")
	b.WriteString("conflict=$(jq -r --arg site_id ")
	b.WriteString(q(plan.SiteID))
	b.WriteString(" --arg env ")
	b.WriteString(q(plan.Env))
	b.WriteString(" --argjson names ")
	b.WriteString(q(string(domainNames)))
	b.WriteString(" '\n")
	b.WriteString("  .[]? | select((.site_id // .site // .id // \"\") != $site_id or (.env // \"\") != $env)\n")
	b.WriteString("  | (.domains // [])[]? as $domain\n")
	b.WriteString("  | ($domain.name // $domain.domain // $domain.domain_name // $domain.hostname // \"\") as $name\n")
	b.WriteString("  | select(($names | index($name)) != null)\n")
	b.WriteString("  | \"\\($name) on \\(.site_id // .site // .id // \"unknown\"):\\(.env // \"unknown\")\"\n")
	b.WriteString("' /var/lib/nf/sites.json | sed -n '1p')\n")
	b.WriteString("if [ -n \"$conflict\" ]; then echo \"Domain already exists on this target: $conflict\" >&2; exit 1; fi\n")
	needsDaemonReload := false
	for _, domain := range domains {
		domainPlan := plan.withProxyModeForDomain(domain)
		art := linodeDomainArtifacts(domain)
		role := "secondary"
		if plan.Primary && domain == plan.Canonical {
			role = "primary"
		}
		redirectTarget := plan.RedirectTarget
		if redirectTarget == "" {
			redirectTarget = plan.Canonical
		}
		expectedIPs := []string{}
		_, reverseProxyIP := siteDomainReverseProxyIP(domainPlan)
		if !siteDomainCloudflareStrict(domainPlan) && !reverseProxyIP {
			for _, ip := range []string{plan.TargetIPv4, plan.TargetIPv6} {
				if strings.TrimSpace(ip) != "" {
					expectedIPs = append(expectedIPs, ip)
				}
			}
		}
		cloudflareRanges := []string{}
		if siteDomainCloudflareProxy(domainPlan) {
			cloudflareRanges = siteDomainBundledCloudflareIPRangeStrings()
		}
		if reverseProxyIP {
			needsDaemonReload = true
			b.WriteString("systemctl disable --now ")
			b.WriteString(q(art.ServiceName + ".timer"))
			b.WriteString(" >/dev/null 2>&1 || true\n")
			b.WriteString("systemctl stop ")
			b.WriteString(q(art.ServiceName + ".service"))
			b.WriteString(" >/dev/null 2>&1 || true\n")
			b.WriteString("rm -f /etc/systemd/system/")
			b.WriteString(art.ServiceName)
			b.WriteString(".service /etc/systemd/system/")
			b.WriteString(art.ServiceName)
			b.WriteString(".timer ")
			b.WriteString(q(art.IssueScript))
			b.WriteByte('\n')
			b.WriteString(renderLinodeDomainRefreshScript(domainPlan, art, fileSlug, role, redirectTarget))
			continue
		}
		b.WriteString(renderLinodeDomainRefreshScript(domainPlan, art, fileSlug, role, redirectTarget))
		b.WriteString(renderLinodeDomainIssueScript(art, expectedIPs, cloudflareRanges))
		b.WriteString("cat >/etc/systemd/system/")
		b.WriteString(art.ServiceName)
		b.WriteString(".service <<EOF\n")
		b.WriteString("[Unit]\nDescription=Issue nf domain TLS certificate for ")
		b.WriteString(domain)
		b.WriteString("\nWants=network-online.target\nAfter=network-online.target nginx.service\n\n[Service]\nType=oneshot\nExecStart=")
		b.WriteString(art.IssueScript)
		b.WriteString("\nEOF\n")
		b.WriteString("cat >/etc/systemd/system/")
		b.WriteString(art.ServiceName)
		b.WriteString(".timer <<EOF\n")
		b.WriteString("[Unit]\nDescription=Retry nf domain TLS certificate for ")
		b.WriteString(domain)
		b.WriteString("\n\n[Timer]\nOnBootSec=2min\nOnUnitActiveSec=5min\nPersistent=true\nUnit=")
		b.WriteString(art.ServiceName)
		b.WriteString(".service\n\n[Install]\nWantedBy=timers.target\nEOF\n")
		b.WriteString("systemctl daemon-reload\n")
		b.WriteString("systemctl enable --now ")
		b.WriteString(q(art.ServiceName + ".timer"))
		b.WriteByte('\n')
		b.WriteString("systemctl start ")
		b.WriteString(q(art.ServiceName + ".service"))
		b.WriteString(" || true\n")
	}
	if needsDaemonReload {
		b.WriteString("systemctl daemon-reload\n")
	}
	if plan.Primary {
		b.WriteString("wp_cmd=(sudo -u www-data wp --path=")
		b.WriteString(q(plan.Target.WordPressPath))
		b.WriteString(" --allow-root)\n")
		b.WriteString("old_home=$(${wp_cmd[@]} option get home 2>/dev/null || true)\n")
		b.WriteString("old_siteurl=$(${wp_cmd[@]} option get siteurl 2>/dev/null || true)\n")
		b.WriteString("${wp_cmd[@]} option update home ")
		b.WriteString(q("https://" + plan.Canonical))
		b.WriteByte('\n')
		b.WriteString("${wp_cmd[@]} option update siteurl ")
		b.WriteString(q("https://" + plan.Canonical))
		b.WriteByte('\n')
		if plan.SearchReplace {
			b.WriteString("for old_url in \"$old_home\" \"$old_siteurl\" ")
			if plan.InternalURL != "" {
				b.WriteString(q(plan.InternalURL))
			} else {
				b.WriteString("''")
			}
			b.WriteString("; do\n")
			b.WriteString("  if [ -n \"$old_url\" ] && [ \"$old_url\" != ")
			b.WriteString(q("https://" + plan.Canonical))
			b.WriteString(" ]; then ${wp_cmd[@]} search-replace \"$old_url\" ")
			b.WriteString(q("https://" + plan.Canonical))
			b.WriteString(" --all-tables --skip-columns=guid; fi\n")
			b.WriteString("done\n")
		}
	}
	b.WriteString("tmp=$(mktemp)\n")
	b.WriteString("if ! jq --arg site_id ")
	b.WriteString(q(plan.SiteID))
	b.WriteString(" --arg env ")
	b.WriteString(q(plan.Env))
	b.WriteString(" --arg canonical ")
	b.WriteString(q(plan.Canonical))
	b.WriteString(" --arg url ")
	b.WriteString(q("https://" + plan.Canonical))
	b.WriteString(" --arg internal_hostname ")
	b.WriteString(q(plan.InternalHostname))
	b.WriteString(" --arg internal_url ")
	b.WriteString(q(plan.InternalURL))
	b.WriteString(" --arg proxy_mode ")
	b.WriteString(q(plan.ProxyMode))
	b.WriteString(" --arg primary ")
	b.WriteString(q(primaryFlag))
	b.WriteString(" --argjson names ")
	b.WriteString(q(string(domainNames)))
	b.WriteString(" --argjson domains ")
	b.WriteString(q(string(domainEntries)))
	b.WriteString(" '\n")
	b.WriteString(linodeDomainCacheUpdateJQFilter())
	b.WriteString("\n' /var/lib/nf/sites.json >\"$tmp\"; then\n")
	b.WriteString("  rm -f \"$tmp\"\n")
	b.WriteString("  exit 1\n")
	b.WriteString("fi\n")
	b.WriteString("install -o ")
	b.WriteString(q(plan.Target.SSHUser))
	b.WriteString(" -g www-data -m 0664 \"$tmp\" /var/lib/nf/sites.json\n")
	b.WriteString("rm -f \"$tmp\"\n")
	b.WriteString("nginx -t\n")
	b.WriteString("systemctl reload nginx\n")
	return b.String()
}

func renderLinodeDomainRefreshScript(plan siteDomainPlan, art siteDomainLinodeArtifacts, fileSlug, role, redirectTarget string) string {
	q := shellQuoteArg
	originWildcardTLS := "0"
	if _, ok := siteDomainReverseProxyIP(plan); ok {
		originWildcardTLS = "1"
	}
	var b strings.Builder
	b.WriteString("cat >")
	b.WriteString(q(art.RefreshScript))
	b.WriteString(" <<'REFRESH'\n")
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("set -euo pipefail\n")
	b.WriteString("file_slug=")
	b.WriteString(q(fileSlug))
	b.WriteByte('\n')
	b.WriteString("site_path=")
	b.WriteString(q(plan.Target.WordPressPath))
	b.WriteByte('\n')
	b.WriteString("domain=")
	b.WriteString(q(art.Domain))
	b.WriteByte('\n')
	b.WriteString("role=")
	b.WriteString(q(role))
	b.WriteByte('\n')
	b.WriteString("redirect_target=")
	b.WriteString(q(redirectTarget))
	b.WriteByte('\n')
	b.WriteString("vhost=")
	b.WriteString(q(art.Vhost))
	b.WriteByte('\n')
	b.WriteString("enabled=")
	b.WriteString(q(art.Enabled))
	b.WriteByte('\n')
	b.WriteString("cert_dir=")
	b.WriteString(q(art.CertDir))
	b.WriteByte('\n')
	b.WriteString("origin_wildcard_tls=")
	b.WriteString(q(originWildcardTLS))
	b.WriteByte('\n')
	b.WriteString(`php_version=$(jq -r '.php_version // .php.version // ""' /var/lib/nf/target.json 2>/dev/null || true)
if [ -z "$php_version" ]; then php_version=`)
	b.WriteString(q(plan.PHPVersion))
	b.WriteString(`; fi
cert_ready=0
wildcard_cert_snippet="/etc/nginx/snippets/nf-wildcard-cert.conf"
if [ "$origin_wildcard_tls" = "1" ]; then
  if [ ! -f "$wildcard_cert_snippet" ]; then echo "Missing $wildcard_cert_snippet for reverse-proxy origin TLS." >&2; exit 1; fi
  cert_ready=1
elif [ -f "$cert_dir/fullchain.pem" ] && [ -f "$cert_dir/privkey.pem" ]; then cert_ready=1; fi
basic_auth_snippet="/etc/nginx/snippets/nf-basic-auth-$file_slug.conf"
write_wordpress_block() {
  cat <<WPBLOCK
    root $site_path;
    access_log /var/log/nginx/sites/$file_slug.$domain.access.log;
    error_log /var/log/nginx/sites/$file_slug.$domain.error.log;
WPBLOCK
  if [ -f "$basic_auth_snippet" ]; then printf '    include %s;\n' "$basic_auth_snippet"; fi
  cat <<WPBLOCK
    include /etc/nginx/snippets/nf-security-headers.conf;
    location ^~ /.well-known/acme-challenge/ { default_type text/plain; root /var/www/letsencrypt; }
    include /etc/nginx/snippets/nf-wordpress.conf;
    include /etc/nginx/snippets/nf-static-assets.conf;
    location ~ \.php$ { include /etc/nginx/snippets/nf-fastcgi-php.conf; fastcgi_pass unix:/run/php/php${php_version}-fpm.sock; }
WPBLOCK
}
write_redirect_block() {
  cat <<REDIRECT
    location ^~ /.well-known/acme-challenge/ { default_type text/plain; root /var/www/letsencrypt; }
    location / {
        add_header Cache-Control "no-store" always;
        add_header Pragma "no-cache" always;
        add_header Expires "0" always;
        return 302 https://$redirect_target\$request_uri;
    }
REDIRECT
}
write_tls_certificate_block() {
  if [ "$origin_wildcard_tls" = "1" ]; then
    printf '    include %s;\n' "$wildcard_cert_snippet"
  else
    cat <<TLSBLOCK
    ssl_certificate $cert_dir/fullchain.pem;
    ssl_certificate_key $cert_dir/privkey.pem;
    ssl_trusted_certificate $cert_dir/fullchain.pem;
TLSBLOCK
  fi
}
tmp=$(mktemp)
{
  if [ "$role" = "primary" ]; then
    cat <<EOF
server {
    listen 80;
    listen [::]:80;
    server_name $domain;
EOF
    if [ "$cert_ready" = "1" ]; then
      cat <<EOF
    location ^~ /.well-known/acme-challenge/ { default_type text/plain; root /var/www/letsencrypt; }
    location / { return 301 https://$domain\$request_uri; }
}

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name $domain;
EOF
      write_tls_certificate_block
      write_wordpress_block
      cat <<EOF
}
EOF
    else
      write_wordpress_block
      cat <<EOF
}
EOF
    fi
  else
    cat <<EOF
server {
    listen 80;
    listen [::]:80;
    server_name $domain;
EOF
    write_redirect_block
    cat <<EOF
}
EOF
    if [ "$cert_ready" = "1" ]; then
      cat <<EOF

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name $domain;
EOF
      write_tls_certificate_block
      write_redirect_block
      cat <<EOF
}
EOF
    fi
  fi
} >"$tmp"
install -m 0644 "$tmp" "$vhost"
rm -f "$tmp"
ln -sf "$vhost" "$enabled"
nginx -t
systemctl reload nginx
REFRESH
`)
	b.WriteString("chmod 0755 ")
	b.WriteString(q(art.RefreshScript))
	b.WriteByte('\n')
	b.WriteString(q(art.RefreshScript))
	b.WriteByte('\n')
	return b.String()
}

func renderLinodeDomainIssueScript(art siteDomainLinodeArtifacts, expectedIPs, cloudflareRanges []string) string {
	q := shellQuoteArg
	var b strings.Builder
	b.WriteString("cat >")
	b.WriteString(q(art.IssueScript))
	b.WriteString(" <<'ISSUE'\n")
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("set -euo pipefail\n")
	b.WriteString("domain=")
	b.WriteString(q(art.Domain))
	b.WriteByte('\n')
	b.WriteString("expected_ips=(")
	b.WriteString(shellArrayValues(expectedIPs))
	b.WriteString(")\n")
	b.WriteString("cloudflare_ranges=(")
	b.WriteString(shellArrayValues(cloudflareRanges))
	b.WriteString(")\n")
	b.WriteString("refresh_script=")
	b.WriteString(q(art.RefreshScript))
	b.WriteByte('\n')
	b.WriteString(`domain_points_here() {
  local domain=$1 observed_ip expected_ip
  if [ ${#expected_ips[@]} -eq 0 ]; then return 0; fi
  while read -r observed_ip _; do
    for expected_ip in "${expected_ips[@]}"; do
      if [ "$observed_ip" = "$expected_ip" ]; then return 0; fi
    done
  done < <(getent ahosts "$domain" 2>/dev/null || true)
  return 1
}
domain_resolves_to_cloudflare() {
  local domain=$1 observed_ip rest
  local hosts=()
  if ! command -v python3 >/dev/null 2>&1; then
    echo "python3 is required to validate Cloudflare DNS; timer will retry."
    return 1
  fi
  while read -r observed_ip rest; do
    if [ -n "$observed_ip" ]; then hosts+=("$observed_ip"); fi
  done < <(getent ahosts "$domain" 2>/dev/null || true)
  if [ ${#hosts[@]} -eq 0 ]; then
    echo "$domain has no public DNS records yet; timer will retry."
    return 1
  fi
  python3 - "$domain" "${cloudflare_ranges[@]}" -- "${hosts[@]}" <<'PY'
import ipaddress
import sys

domain = sys.argv[1]
separator = sys.argv.index("--")
ranges = [ipaddress.ip_network(value, strict=False) for value in sys.argv[2:separator]]
hosts = sys.argv[separator + 1:]
outside = []
for host in hosts:
    try:
        addr = ipaddress.ip_address(host)
    except ValueError:
        outside.append(host)
        continue
    if not any(addr in network for network in ranges):
        outside.append(host)
if outside:
    print(f"{domain} resolves publicly to {', '.join(hosts)}; {', '.join(outside)} not in Cloudflare IP ranges; timer will retry.")
    sys.exit(1)
PY
}
cloudflare_http_challenge_reachable() {
  local domain=$1 token probe_dir probe_file expected observed
  token="nf-probe-$(date +%s)-$$"
  probe_dir=/var/www/letsencrypt/.well-known/acme-challenge
  probe_file="$probe_dir/$token"
  expected="nf-acme-probe:$domain:$token"
  install -d -m 0755 "$probe_dir"
  printf '%s' "$expected" >"$probe_file"
  observed=$(curl --fail --silent --show-error --max-time 10 "http://$domain/.well-known/acme-challenge/$token" 2>/dev/null || true)
  rm -f "$probe_file"
  if [ "$observed" != "$expected" ]; then
    echo "$domain ACME HTTP challenge path is not reachable through Cloudflare yet; timer will retry."
    return 1
  fi
}
if ! domain_points_here "$domain"; then
  echo "$domain does not resolve to this target yet; timer will retry."
  exit 0
fi
if [ ${#cloudflare_ranges[@]} -gt 0 ]; then
  if ! domain_resolves_to_cloudflare "$domain"; then exit 0; fi
  if ! cloudflare_http_challenge_reachable "$domain"; then exit 0; fi
fi
args=(certbot certonly --non-interactive --agree-tos --webroot -w /var/www/letsencrypt -m web@nonfiction.ca --keep-until-expiring --cert-name "$domain" -d "$domain" --deploy-hook "$refresh_script")
tmp=$(mktemp)
set +e
flock -n -E 75 /run/nf-certbot.lock "${args[@]}" >"$tmp" 2>&1
status=$?
set -e
if [ "$status" -eq 75 ]; then
  echo "Another nf certbot job is already running; timer will retry."
  rm -f "$tmp"
  exit 0
fi
if [ "$status" -ne 0 ]; then
  if grep -qi "Another instance of Certbot is already running" "$tmp"; then
    echo "Certbot is already running; timer will retry."
    rm -f "$tmp"
    exit 0
  fi
  cat "$tmp" >&2
  rm -f "$tmp"
  exit "$status"
fi
cat "$tmp"
rm -f "$tmp"
"$refresh_script"
ISSUE
`)
	b.WriteString("chmod 0755 ")
	b.WriteString(q(art.IssueScript))
	b.WriteByte('\n')
	return b.String()
}

func renderLinodeDomainBindingRemoveScript(plan siteDomainPlan, deleteCert bool) string {
	q := shellQuoteArg
	domains := plan.allDomains()
	domainNames, _ := json.Marshal(domains)
	resetPrimary := "0"
	if siteDomainNameSet(domains)[normalizeDomainName(plan.CurrentHostname)] || siteDomainNameSet(domains)[normalizeDomainName(firstRecordString(plan.Record, "primary_domain"))] {
		resetPrimary = "1"
	}
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	b.WriteString("exec 9>/run/nf-domain-state.lock\nflock 9\n")
	for _, domain := range domains {
		art := linodeDomainArtifacts(domain)
		b.WriteString("rm -f ")
		b.WriteString(q(art.Enabled))
		b.WriteByte(' ')
		b.WriteString(q(art.Vhost))
		b.WriteByte(' ')
		b.WriteString(q(art.RefreshScript))
		b.WriteByte(' ')
		b.WriteString(q(art.IssueScript))
		b.WriteByte('\n')
		b.WriteString("systemctl disable --now ")
		b.WriteString(q(art.ServiceName + ".timer"))
		b.WriteString(" >/dev/null 2>&1 || true\n")
		b.WriteString("systemctl stop ")
		b.WriteString(q(art.ServiceName + ".service"))
		b.WriteString(" >/dev/null 2>&1 || true\n")
		b.WriteString("rm -f /etc/systemd/system/")
		b.WriteString(art.ServiceName)
		b.WriteString(".service /etc/systemd/system/")
		b.WriteString(art.ServiceName)
		b.WriteString(".timer\n")
		if deleteCert {
			b.WriteString("certbot delete --cert-name ")
			b.WriteString(q(domain))
			b.WriteString(" --non-interactive >/dev/null 2>&1 || true\n")
		}
	}
	b.WriteString("systemctl daemon-reload\n")
	b.WriteString("touch /var/lib/nf/sites.json\n")
	b.WriteString("if ! jq empty /var/lib/nf/sites.json >/dev/null 2>&1; then printf '[]\\n' >/var/lib/nf/sites.json; fi\n")
	b.WriteString("tmp=$(mktemp)\n")
	b.WriteString("if ! jq --arg site_id ")
	b.WriteString(q(plan.SiteID))
	b.WriteString(" --arg env ")
	b.WriteString(q(plan.Env))
	b.WriteString(" --arg internal_hostname ")
	b.WriteString(q(plan.InternalHostname))
	b.WriteString(" --arg internal_url ")
	b.WriteString(q(plan.InternalURL))
	b.WriteString(" --arg reset_primary ")
	b.WriteString(q(resetPrimary))
	b.WriteString(" --argjson remove_domains ")
	b.WriteString(q(string(domainNames)))
	b.WriteString(" '\n")
	b.WriteString(linodeDomainCacheRemoveJQFilter())
	b.WriteString("\n' /var/lib/nf/sites.json >\"$tmp\"; then\n")
	b.WriteString("  rm -f \"$tmp\"\n")
	b.WriteString("  exit 1\n")
	b.WriteString("fi\n")
	b.WriteString("install -o ")
	b.WriteString(q(plan.Target.SSHUser))
	b.WriteString(" -g www-data -m 0664 \"$tmp\" /var/lib/nf/sites.json\n")
	b.WriteString("rm -f \"$tmp\"\n")
	b.WriteString("nginx -t\n")
	b.WriteString("systemctl reload nginx\n")
	return b.String()
}

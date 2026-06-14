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
	"os"
	"strings"
	"time"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/kinsta"
	"github.com/nonfiction/nf/internal/state"
	"github.com/nonfiction/nf/internal/ui"
)

type siteDomainOptions struct {
	canonical      string
	aliases        []string
	setupType      string
	proxyMode      string
	searchReplace  bool
	deleteCert     bool
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
	SetupType        string
	ProxyMode        string
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
	if action != "prepare" && action != "primary" && action != "check" && action != "remove" {
		fmt.Fprintf(os.Stderr, "unsupported site domain action: %s\n", action)
		return 1
	}
	envRef, opts, ok := parseSiteDomainActionArgs(action, argv[1:])
	if !ok {
		return 1
	}
	return cmdSiteDomain(envRef, action, opts)
}

func runSiteDomainHelp() int {
	printGroupHelp("site domain", []helpLine{
		{"list [site|env|remote]", "list cached public domain bindings"},
		{"prepare <env|remote> <domain> [flags]", "make a provider/env ready for a public domain"},
		{"check <env|remote> <domain> [flags]", "check DNS, provider, HTTP, and HTTPS readiness"},
		{"primary <env|remote> <domain> [flags]", "launch a canonical public domain"},
		{"remove <env|remote> <domain> [flags]", "remove a public domain binding"},
		{},
		{"--canonical <domain>", "canonical public hostname"},
		{"--alias <domain>", "redirect/alternate hostname; repeatable"},
		{"--proxy <mode>", "Linode proxy mode: cloudflare-strict or cloudflare-full"},
		{"--setup <type>", "Kinsta setup type for prepare/primary: avoid-downtime or quick"},
		{"--search-replace", "run provider/wp search-replace during primary"},
		{"--delete-cert", "also delete the Linode Let's Encrypt certificate lineage"},
		{},
		{"--dry-run", "show the mutation plan only"},
		{"--execute", "execute the mutation plan"},
		{"--yes", "confirm mutation execution"},
		{"--non-interactive", "fail instead of prompting"},
		{},
		{"refresh", "run nf site refresh to update cached domain listings"},
	})
	return 0
}

func parseSiteDomainListArgs(argv []string) (string, bool) {
	filter := ""
	for _, arg := range argv {
		if strings.HasPrefix(arg, "-") {
			fmt.Fprintf(os.Stderr, "unknown site domain list flag: %s\n", arg)
			return "", false
		}
		if filter != "" {
			fmt.Fprintln(os.Stderr, "site domain list takes at most one site, env, or remote")
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
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	rows := [][]string{{"domain", "site", "env", "role", "state", "provider", "proxy", "url"}}
	for _, record := range records {
		if resolved != "" && !siteDomainListRecordMatches(record, resolved) {
			continue
		}
		for _, domain := range siteDomainListDomains(record) {
			rows = append(rows, []string{
				domain.name,
				siteRecordID(record),
				siteEnvName(record),
				domain.role,
				firstRecordString(record, "domain_state"),
				recordValueString(record["provider"]),
				firstRecordString(record, "proxy_mode"),
				firstRecordString(record, "url", "site_url", "home_url"),
			})
		}
	}
	if len(rows) == 1 {
		if resolved != "" {
			fmt.Printf("No site domains found for %q.\n", filter)
		} else {
			fmt.Println("No site domains found.")
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
	name string
	role string
}

func siteDomainListDomains(record map[string]any) []siteDomainListDomain {
	seen := map[string]bool{}
	domains := []siteDomainListDomain{}
	add := func(name, role string) {
		name = normalizeDomainName(hostnameFromURLish(name))
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		domains = append(domains, siteDomainListDomain{name: name, role: firstNonEmpty(role, "public")})
	}
	for _, entry := range siteDomainEntryValues(record["domains"]) {
		role := ""
		if typed, ok := entry.(map[string]any); ok {
			role = firstRecordString(typed, "role")
		}
		add(siteDomainEntryName(entry), role)
	}
	add(firstRecordString(record, "primary_domain"), "primary")
	if host := hostnameFromURLish(firstRecordString(record, "hostname")); !looksLikeInternalSiteHostname(record, host) {
		add(host, "current")
	}
	if host := hostnameFromURLish(firstRecordString(record, "url", "site_url", "home_url")); !looksLikeInternalSiteHostname(record, host) {
		add(host, "current")
	}
	return domains
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
			opts.searchReplace = true
		case "--delete-cert":
			opts.deleteCert = true
		case "--canonical":
			if i+1 >= len(argv) || strings.TrimSpace(argv[i+1]) == "" {
				fmt.Fprintln(os.Stderr, "--canonical requires a value")
				return "", opts, false
			}
			i++
			opts.canonical = argv[i]
		case "--alias":
			if i+1 >= len(argv) || strings.TrimSpace(argv[i+1]) == "" {
				fmt.Fprintln(os.Stderr, "--alias requires a value")
				return "", opts, false
			}
			i++
			opts.aliases = append(opts.aliases, argv[i])
		case "--setup":
			if i+1 >= len(argv) || strings.TrimSpace(argv[i+1]) == "" {
				fmt.Fprintln(os.Stderr, "--setup requires a value")
				return "", opts, false
			}
			i++
			opts.setupType = argv[i]
		case "--proxy":
			if i+1 >= len(argv) || strings.TrimSpace(argv[i+1]) == "" {
				fmt.Fprintln(os.Stderr, "--proxy requires a value")
				return "", opts, false
			}
			i++
			opts.proxyMode = argv[i]
		default:
			if strings.HasPrefix(arg, "--canonical=") {
				opts.canonical = strings.TrimPrefix(arg, "--canonical=")
				if strings.TrimSpace(opts.canonical) == "" {
					fmt.Fprintln(os.Stderr, "--canonical requires a value")
					return "", opts, false
				}
				continue
			}
			if strings.HasPrefix(arg, "--alias=") {
				alias := strings.TrimPrefix(arg, "--alias=")
				if strings.TrimSpace(alias) == "" {
					fmt.Fprintln(os.Stderr, "--alias requires a value")
					return "", opts, false
				}
				opts.aliases = append(opts.aliases, alias)
				continue
			}
			if strings.HasPrefix(arg, "--setup=") {
				opts.setupType = strings.TrimPrefix(arg, "--setup=")
				if strings.TrimSpace(opts.setupType) == "" {
					fmt.Fprintln(os.Stderr, "--setup requires a value")
					return "", opts, false
				}
				continue
			}
			if strings.HasPrefix(arg, "--proxy=") {
				opts.proxyMode = strings.TrimPrefix(arg, "--proxy=")
				if strings.TrimSpace(opts.proxyMode) == "" {
					fmt.Fprintln(os.Stderr, "--proxy requires a value")
					return "", opts, false
				}
				continue
			}
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(os.Stderr, "unknown site domain flag: %s\n", arg)
				return "", opts, false
			}
			positionals = append(positionals, arg)
		}
	}
	if action == "prepare" && opts.searchReplace {
		fmt.Fprintln(os.Stderr, "site domain prepare does not run search-replace")
		return "", opts, false
	}
	if action != "remove" && opts.deleteCert {
		fmt.Fprintln(os.Stderr, "--delete-cert only applies to site domain remove")
		return "", opts, false
	}
	if action == "remove" && (opts.searchReplace || strings.TrimSpace(opts.setupType) != "") {
		fmt.Fprintln(os.Stderr, "site domain remove does not support --setup or --search-replace")
		return "", opts, false
	}
	if action == "check" {
		if opts.dryRun || opts.execute || opts.yes || opts.searchReplace || opts.deleteCert || strings.TrimSpace(opts.setupType) != "" {
			fmt.Fprintln(os.Stderr, "site domain check is read-only; use only --canonical, --alias, --proxy, and --non-interactive")
			return "", opts, false
		}
	}
	if len(positionals) > 2 {
		fmt.Fprintf(os.Stderr, "site domain %s takes at most env and canonical domain\n", action)
		return "", opts, false
	}
	envRef := ""
	if len(positionals) > 0 {
		envRef = positionals[0]
	}
	if len(positionals) == 2 {
		if strings.TrimSpace(opts.canonical) != "" {
			fmt.Fprintln(os.Stderr, "pass the canonical domain either positionally or with --canonical, not both")
			return "", opts, false
		}
		opts.canonical = positionals[1]
	}
	if strings.TrimSpace(opts.canonical) == "" {
		fmt.Fprintf(os.Stderr, "site domain %s requires a canonical domain\n", action)
		return "", opts, false
	}
	if opts.nonInteractive && strings.TrimSpace(envRef) == "" {
		fmt.Fprintf(os.Stderr, "site domain %s requires an explicit env ref or project remote in non-interactive mode\n", action)
		return "", opts, false
	}
	return envRef, opts, true
}

func cmdSiteDomain(envRef, action string, opts siteDomainOptions) int {
	if opts.execute && opts.dryRun {
		fmt.Fprintln(os.Stderr, "Choose either --execute or --dry-run, not both.")
		return 1
	}
	resolved, ok := resolveSiteDomainEnvRef(action, envRef, opts.nonInteractive)
	if !ok {
		return 1
	}
	siteID, env, ok := splitSiteEnvRef(resolved)
	if !ok {
		fmt.Fprintf(os.Stderr, "site domain %s requires an env ref like site.target:live\n", action)
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
	if action == "remove" && plan.Provider == "linode" {
		if remaining := remainingCachedSiteDomainsAfterRemove(plan); len(remaining) > 0 {
			fmt.Fprintf(os.Stderr, "Linode public domain bindings are env-scoped; remove all cached domains for %s or replace them with prepare/primary first. Remaining cached domains: %s\n", plan.EnvID, strings.Join(remaining, ", "))
			return 1
		}
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
		confirmed, err := ui.Confirm(fmt.Sprintf("%s public domain %q for %s?", strings.ToUpper(action[:1])+action[1:], plan.Canonical, plan.EnvID), false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	result := siteDomainProviderResult{}
	switch plan.Provider {
	case "kinsta":
		if action == "remove" {
			result, err = kinstaRemoveDomainFn(plan)
		} else if action == "primary" {
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
		script := renderLinodeDomainScript(plan)
		if action == "remove" {
			script = renderLinodeDomainRemoveScript(plan, opts.deleteCert)
		}
		if err := runSSHCommandFn(remoteSudoBashArgs(plan.Target, script)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	default:
		fmt.Fprintf(os.Stderr, "site domain is not implemented for provider %q; no data was changed.\n", plan.Provider)
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
		fmt.Println("Site domain removed.")
	} else if action == "primary" {
		fmt.Println("Site domain launched as primary.")
	} else {
		fmt.Println("Site domain prepared.")
	}
	return 0
}

func resolveSiteDomainEnvRef(action, ref string, nonInteractive bool) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		if !siteIsInteractiveFn() {
			fmt.Fprintf(os.Stderr, "site domain %s requires an env ref like site.target:live or a project remote\n", action)
			return "", false
		}
		selected, err := chooseSiteEnv("domain "+action, "")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return "", false
		}
		return selected, true
	}
	resolved, _, _, _, err := resolveSiteTarget(ref)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return "", false
	}
	if _, _, ok := splitSiteEnvRef(resolved); ok {
		return resolved, true
	}
	if nonInteractive {
		fmt.Fprintf(os.Stderr, "site domain %s requires an env ref like %s:live or a project remote in non-interactive mode\n", action, ref)
		return "", false
	}
	selected, err := chooseSiteEnv("domain "+action, resolved)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return "", false
	}
	return selected, true
}

func buildSiteDomainPlan(siteID, env, action string, opts siteDomainOptions) (siteDomainPlan, error) {
	canonical, err := normalizePublicDomain(opts.canonical)
	if err != nil {
		return siteDomainPlan{}, err
	}
	aliases, err := normalizePublicDomainAliases(canonical, opts.aliases)
	if err != nil {
		return siteDomainPlan{}, err
	}
	record, _, err := cachedSiteEnv(siteID, env)
	if err != nil {
		return siteDomainPlan{}, err
	}
	if record == nil {
		return siteDomainPlan{}, ProjectError{Msg: fmt.Sprintf("No cached remote env matched %q.", canonicalEnvID(siteID, env))}
	}
	target, err := envRemoteSyncTargetFromSiteRecord(record, canonicalEnvID(siteID, env), siteID, env)
	if err != nil {
		return siteDomainPlan{}, err
	}
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
		ProxyMode:        firstNonEmpty(opts.proxyMode, firstRecordString(record, "proxy_mode")),
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
		if strings.TrimSpace(plan.ProxyMode) != "" {
			return siteDomainPlan{}, ProjectError{Msg: "--proxy only applies to Linode domains"}
		}
		setup, err := normalizeKinstaDomainSetupType(opts.setupType)
		if err != nil {
			return siteDomainPlan{}, err
		}
		plan.SetupType = setup
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
		if strings.TrimSpace(opts.setupType) != "" {
			return siteDomainPlan{}, ProjectError{Msg: "--setup only applies to Kinsta domains"}
		}
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
		if strings.TrimSpace(plan.ProxyMode) != "" {
			return siteDomainPlan{}, ProjectError{Msg: "--proxy only applies to Linode domains"}
		}
		return siteDomainPlan{}, ProjectError{Msg: fmt.Sprintf("site domain is not implemented for provider %q", plan.Provider)}
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

func normalizePublicDomainAliases(canonical string, raw []string) ([]string, error) {
	seen := map[string]bool{canonical: true}
	aliases := []string{}
	for _, value := range raw {
		alias, err := normalizePublicDomain(value)
		if err != nil {
			return nil, err
		}
		if alias == canonical {
			return nil, ProjectError{Msg: fmt.Sprintf("alias %q matches the canonical domain", value)}
		}
		if seen[alias] {
			continue
		}
		seen[alias] = true
		aliases = append(aliases, alias)
	}
	return aliases, nil
}

func normalizeKinstaDomainSetupType(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "avoid-downtime", "avoid_downtime":
		return "avoid_downtime", nil
	case "quick":
		return "quick", nil
	default:
		return "", ProjectError{Msg: "--setup must be quick or avoid-downtime"}
	}
}

func displayKinstaSetupType(value string) string {
	if value == "avoid_downtime" {
		return "avoid-downtime"
	}
	return value
}

func normalizeSiteDomainProxyMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "none", "direct", "dns-only", "dns_only":
		return "", nil
	case "cloudflare-strict", "cloudflare_strict":
		return "cloudflare_strict", nil
	case "cloudflare-full", "cloudflare_full":
		return "cloudflare_full", nil
	default:
		return "", ProjectError{Msg: "--proxy must be cloudflare-strict or cloudflare-full"}
	}
}

func displaySiteDomainProxyMode(value string) string {
	if value == "cloudflare_strict" {
		return "cloudflare-strict"
	}
	if value == "cloudflare_full" {
		return "cloudflare-full"
	}
	return value
}

func siteDomainCloudflareStrict(plan siteDomainPlan) bool {
	return plan.ProxyMode == "cloudflare_strict"
}

func siteDomainCloudflareFull(plan siteDomainPlan) bool {
	return plan.ProxyMode == "cloudflare_full"
}

func siteDomainCloudflareProxy(plan siteDomainPlan) bool {
	return siteDomainCloudflareStrict(plan) || siteDomainCloudflareFull(plan)
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

func (p siteDomainPlan) allDomains() []string {
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
	title := "Prepare public domain plan:"
	if plan.Action == "primary" {
		title = "Launch primary domain plan:"
	} else if plan.Action == "remove" {
		title = "Remove public domain plan:"
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
	fmt.Printf("  canonical: %s\n", plan.Canonical)
	if len(plan.Aliases) > 0 {
		fmt.Printf("  aliases:   %s\n", strings.Join(plan.Aliases, ", "))
	}
	if plan.ProxyMode != "" {
		fmt.Printf("  proxy:     %s\n", displaySiteDomainProxyMode(plan.ProxyMode))
	}
	if plan.Provider == "kinsta" {
		if plan.Action != "remove" {
			fmt.Printf("  kinsta setup: %s\n", displayKinstaSetupType(plan.SetupType))
		}
		if plan.Action == "primary" {
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
			fmt.Println("  provider: remove nf-managed public vhost, scripts, timer, and cached metadata")
		} else {
			printLinodeDomainDNSInstructions(plan)
		}
		if plan.Action == "primary" {
			fmt.Printf("  search-replace: %t\n", plan.SearchReplace)
		}
		if plan.Action == "remove" {
			if plan.DeleteCert {
				fmt.Println("  TLS: delete the Let's Encrypt certificate lineage")
			} else {
				fmt.Println("  TLS: certificate lineage is kept for rollback safety")
			}
		} else if siteDomainCloudflareStrict(plan) {
			fmt.Println("  TLS: Cloudflare Full (strict) uses a public Let's Encrypt origin cert; certbot renewal stays enabled")
		} else if siteDomainCloudflareFull(plan) {
			fmt.Println("  TLS: Cloudflare Full uses the target wildcard cert for origin HTTPS; hostname validation is handled at the Cloudflare edge")
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
		if siteDomainCloudflareStrict(plan) {
			fmt.Println("  Cloudflare SSL/TLS mode: Full (strict)")
		} else {
			fmt.Println("  Cloudflare SSL/TLS mode: Full")
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
	client := kinsta.NewClient(firstNonEmpty(envwizard.Value("KINSTA_BASE_URL"), "https://api.kinsta.com/v2"), token)
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
	client := kinsta.NewClient(firstNonEmpty(envwizard.Value("KINSTA_BASE_URL"), "https://api.kinsta.com/v2"), token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	result := siteDomainProviderResult{Domains: make([]siteDomainProviderDomain, 0, 1+len(plan.Aliases))}
	canonicalDomain := kinsta.Domain{}
	for i, name := range plan.allDomains() {
		role := "canonical"
		if i > 0 {
			role = "redirect"
		}
		domain, err := ensureKinstaDomainWithSetup(ctx, client, plan.KinstaEnvID, name, plan.SetupType)
		if err != nil {
			return siteDomainProviderResult{}, err
		}
		if i == 0 {
			canonicalDomain = domain
		}
		records, err := client.DomainRecords(ctx, domain.ID)
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

func ensureKinstaDomainWithSetup(ctx context.Context, client *kinsta.Client, envID, domainName, setupType string) (kinsta.Domain, error) {
	domains, err := client.ListDomains(ctx, envID)
	if err != nil {
		return kinsta.Domain{}, err
	}
	if domain, ok := kinsta.FindDomain(domains, domainName); ok {
		return domain, nil
	}
	setupType, err = normalizeKinstaDomainSetupType(setupType)
	if err != nil {
		return kinsta.Domain{}, err
	}
	fmt.Printf("Adding Kinsta domain %s...\n", domainName)
	opID, err := client.AddDomain(ctx, envID, kinsta.AddDomainRequest{DomainName: domainName, IsWildcardless: false, AddWithWWWSubdomain: false, SetupType: setupType})
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

func printKinstaDomainRecords(result siteDomainProviderResult) {
	if len(result.Domains) == 0 {
		return
	}
	fmt.Println("DNS records for client DNS:")
	for _, domain := range result.Domains {
		fmt.Printf("  %s (%s):\n", domain.Name, domain.Role)
		printed := printKinstaDomainRecordGroup("verification", domain.Records.Verification)
		printed = printKinstaDomainRecordGroup("pointing", domain.Records.Pointing) || printed
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
	record["domains"] = siteDomainCacheEntries(plan, result)
	record["domain_state"] = map[string]string{"prepare": "prepared", "primary": "primary"}[plan.Action]
	record["proxy_mode"] = plan.ProxyMode
	if plan.Action == "primary" {
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
	entries := make([]map[string]any, 0, 1+len(plan.Aliases))
	for i, name := range plan.allDomains() {
		role := "canonical"
		if i > 0 {
			role = "redirect"
		}
		entry := map[string]any{"name": name, "role": role}
		if domainID := result.domainID(name); domainID != "" {
			entry["domain_id"] = domainID
		}
		entries = append(entries, entry)
	}
	return entries
}

func normalizeDomainName(value string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
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

func remainingCachedSiteDomainsAfterRemove(plan siteDomainPlan) []string {
	cached := cachedSiteDomainNames(plan.Record)
	if len(cached) == 0 {
		return nil
	}
	removeSet := siteDomainNameSet(plan.allDomains())
	remaining := []string{}
	for _, domain := range cached {
		if !removeSet[domain] {
			remaining = append(remaining, domain)
		}
	}
	return remaining
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
			proxyArg := siteDomainProxyArg(firstRecordString(record, "proxy_mode"))
			fmt.Printf("Warning: %s is also cached on %s; after cutover run: nf site domain remove %s %s%s\n", domain, envID, envID, domain, proxyArg)
		}
	}
}

func renderLinodeDomainScript(plan siteDomainPlan) string {
	domainEntries, _ := json.Marshal(siteDomainCacheEntries(plan, siteDomainProviderResult{}))
	q := shellQuoteArg
	allDomains := shellArrayValues(plan.allDomains())
	aliasDomains := shellArrayValues(plan.Aliases)
	expectedIPs := []string{}
	if !siteDomainCloudflareStrict(plan) {
		for _, ip := range []string{plan.TargetIPv4, plan.TargetIPv6} {
			if strings.TrimSpace(ip) != "" {
				expectedIPs = append(expectedIPs, ip)
			}
		}
	}
	fileSlug := plan.FileSlug
	if fileSlug == "" {
		fileSlug = envIDFileSlug(plan.EnvID)
	}
	serviceName := "nf-public-domain-" + fileSlug + "-tls"
	refreshScript := "/usr/local/bin/nf-refresh-public-domain-" + fileSlug
	issueScript := "/usr/local/bin/nf-issue-public-domain-cert-" + fileSlug
	publicVhost := "/etc/nginx/sites-available/nf-site-public-" + fileSlug
	publicEnabled := "/etc/nginx/sites-enabled/nf-site-public-" + fileSlug
	domainState := "prepared"
	if plan.Action == "primary" {
		domainState = "primary"
	}
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	b.WriteString("install -d -m 0755 /var/www/letsencrypt /etc/nginx/conf.d /etc/nginx/sites-available /etc/nginx/sites-enabled /etc/systemd/system /var/lib/nf\n")
	b.WriteString("cat >/etc/nginx/conf.d/nf-server-names-hash.conf <<'EOF'\n")
	b.WriteString("server_names_hash_bucket_size 128;\nserver_names_hash_max_size 4096;\n")
	b.WriteString("EOF\n")
	b.WriteString("cat >")
	b.WriteString(q(refreshScript))
	b.WriteString(" <<'REFRESH'\n")
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("set -euo pipefail\n")
	b.WriteString("file_slug=")
	b.WriteString(q(fileSlug))
	b.WriteByte('\n')
	b.WriteString("site_path=")
	b.WriteString(q(plan.Target.WordPressPath))
	b.WriteByte('\n')
	b.WriteString("canonical=")
	b.WriteString(q(plan.Canonical))
	b.WriteByte('\n')
	b.WriteString("mode=")
	b.WriteString(q(plan.Action))
	b.WriteByte('\n')
	b.WriteString("proxy_mode=")
	b.WriteString(q(plan.ProxyMode))
	b.WriteByte('\n')
	b.WriteString("vhost=")
	b.WriteString(q(publicVhost))
	b.WriteByte('\n')
	b.WriteString("enabled=")
	b.WriteString(q(publicEnabled))
	b.WriteByte('\n')
	b.WriteString("domains=(")
	b.WriteString(allDomains)
	b.WriteString(")\n")
	b.WriteString("aliases=(")
	b.WriteString(aliasDomains)
	b.WriteString(")\n")
	b.WriteString(`php_version=$(jq -r '.php_version // .php.version // ""' /var/lib/nf/target.json 2>/dev/null || true)
if [ -z "$php_version" ]; then php_version=`)
	b.WriteString(q(plan.PHPVersion))
	b.WriteString(`; fi
cert_dir="/etc/letsencrypt/live/$canonical"
cert_ready=0
if [ -f "$cert_dir/fullchain.pem" ] && [ -f "$cert_dir/privkey.pem" ]; then cert_ready=1; fi
server_names="${domains[*]}"
alias_names="${aliases[*]}"
basic_auth_snippet="/etc/nginx/snippets/nf-basic-auth-$file_slug.conf"
write_wordpress_block() {
  cat <<WPBLOCK
    root $site_path;
    access_log /var/log/nginx/sites/$file_slug.public.access.log;
    error_log /var/log/nginx/sites/$file_slug.public.error.log;
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
tmp=$(mktemp)
{
  if [ "$proxy_mode" = "cloudflare_full" ]; then
    if [ "$mode" = "primary" ]; then
      cat <<EOF
server {
    listen 80;
    listen [::]:80;
    server_name $server_names;
    return 301 https://$canonical\$request_uri;
}

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name $canonical;
    include /etc/nginx/snippets/nf-wildcard-cert.conf;
EOF
      write_wordpress_block
      cat <<EOF
}
EOF
      if [ -n "$alias_names" ]; then
        cat <<EOF

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name $alias_names;
    include /etc/nginx/snippets/nf-wildcard-cert.conf;
    return 301 https://$canonical\$request_uri;
}
EOF
      fi
    else
      cat <<EOF
server {
    listen 80;
    listen [::]:80;
    server_name $server_names;
EOF
      write_wordpress_block
      cat <<EOF
}

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name $server_names;
    include /etc/nginx/snippets/nf-wildcard-cert.conf;
EOF
      write_wordpress_block
      cat <<EOF
}
EOF
    fi
  elif [ "$mode" = "primary" ] && [ "$cert_ready" = "1" ]; then
    cat <<EOF
server {
    listen 80;
    listen [::]:80;
    server_name $server_names;
    location ^~ /.well-known/acme-challenge/ { default_type text/plain; root /var/www/letsencrypt; }
    return 301 https://$canonical\$request_uri;
}

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name $canonical;
    ssl_certificate $cert_dir/fullchain.pem;
    ssl_certificate_key $cert_dir/privkey.pem;
    ssl_trusted_certificate $cert_dir/fullchain.pem;
EOF
    write_wordpress_block
    cat <<EOF
}
EOF
    if [ -n "$alias_names" ]; then
      cat <<EOF

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name $alias_names;
    ssl_certificate $cert_dir/fullchain.pem;
    ssl_certificate_key $cert_dir/privkey.pem;
    ssl_trusted_certificate $cert_dir/fullchain.pem;
    return 301 https://$canonical\$request_uri;
}
EOF
    fi
  else
    cat <<EOF
server {
    listen 80;
    listen [::]:80;
    server_name $server_names;
EOF
    write_wordpress_block
    cat <<EOF
}
EOF
    if [ "$cert_ready" = "1" ]; then
      cat <<EOF

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name $server_names;
    ssl_certificate $cert_dir/fullchain.pem;
    ssl_certificate_key $cert_dir/privkey.pem;
    ssl_trusted_certificate $cert_dir/fullchain.pem;
EOF
      write_wordpress_block
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
	b.WriteString(q(refreshScript))
	b.WriteByte('\n')
	b.WriteString(q(refreshScript))
	b.WriteByte('\n')
	if siteDomainCloudflareFull(plan) {
		b.WriteString("systemctl disable --now ")
		b.WriteString(q(serviceName + ".timer"))
		b.WriteString(" >/dev/null 2>&1 || true\n")
		b.WriteString("systemctl stop ")
		b.WriteString(q(serviceName + ".service"))
		b.WriteString(" >/dev/null 2>&1 || true\n")
		b.WriteString("rm -f ")
		b.WriteString(q(issueScript))
		b.WriteString(" /etc/systemd/system/")
		b.WriteString(serviceName)
		b.WriteString(".service /etc/systemd/system/")
		b.WriteString(serviceName)
		b.WriteString(".timer\n")
		b.WriteString("systemctl daemon-reload\n")
	} else {
		b.WriteString("cat >")
		b.WriteString(q(issueScript))
		b.WriteString(" <<'ISSUE'\n")
		b.WriteString("#!/usr/bin/env bash\n")
		b.WriteString("set -euo pipefail\n")
		b.WriteString("canonical=")
		b.WriteString(q(plan.Canonical))
		b.WriteByte('\n')
		b.WriteString("domains=(")
		b.WriteString(allDomains)
		b.WriteString(")\n")
		b.WriteString("expected_ips=(")
		b.WriteString(shellArrayValues(expectedIPs))
		b.WriteString(")\n")
		b.WriteString("refresh_script=")
		b.WriteString(q(refreshScript))
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
for domain in "${domains[@]}"; do
  if ! domain_points_here "$domain"; then
    echo "$domain does not resolve to this target yet; timer will retry."
    exit 0
  fi
done
args=(certbot certonly --non-interactive --agree-tos --webroot -w /var/www/letsencrypt -m web@nonfiction.ca --keep-until-expiring --deploy-hook "$refresh_script")
for domain in "${domains[@]}"; do args+=(-d "$domain"); done
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
		b.WriteString(q(issueScript))
		b.WriteByte('\n')
		b.WriteString("cat >/etc/systemd/system/")
		b.WriteString(serviceName)
		b.WriteString(".service <<EOF\n")
		b.WriteString("[Unit]\nDescription=Issue nf public domain TLS certificate for ")
		b.WriteString(plan.EnvID)
		b.WriteString("\nWants=network-online.target\nAfter=network-online.target nginx.service\n\n[Service]\nType=oneshot\nExecStart=")
		b.WriteString(issueScript)
		b.WriteString("\nEOF\n")
		b.WriteString("cat >/etc/systemd/system/")
		b.WriteString(serviceName)
		b.WriteString(".timer <<EOF\n")
		b.WriteString("[Unit]\nDescription=Retry nf public domain TLS certificate for ")
		b.WriteString(plan.EnvID)
		b.WriteString("\n\n[Timer]\nOnBootSec=2min\nOnUnitActiveSec=5min\nPersistent=true\nUnit=")
		b.WriteString(serviceName)
		b.WriteString(".service\n\n[Install]\nWantedBy=timers.target\nEOF\n")
		b.WriteString("systemctl daemon-reload\n")
		b.WriteString("systemctl enable --now ")
		b.WriteString(serviceName)
		b.WriteString(".timer\n")
		b.WriteString("systemctl start ")
		b.WriteString(serviceName)
		b.WriteString(".service || true\n")
	}
	if plan.Action == "primary" {
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
	b.WriteString("touch /var/lib/nf/sites.json\n")
	b.WriteString("if ! jq empty /var/lib/nf/sites.json >/dev/null 2>&1; then printf '[]\\n' >/var/lib/nf/sites.json; fi\n")
	b.WriteString("tmp=$(mktemp)\n")
	b.WriteString("jq --arg site_id ")
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
	b.WriteString(" --arg domain_state ")
	b.WriteString(q(domainState))
	b.WriteString(" --arg proxy_mode ")
	b.WriteString(q(plan.ProxyMode))
	b.WriteString(" --argjson domains ")
	b.WriteString(q(string(domainEntries)))
	b.WriteString(" '\n")
	b.WriteString("  map(if (.site_id == $site_id and .env == $env) then\n")
	b.WriteString("    (if ((.internal_hostname // \"\") == \"\" and $internal_hostname != \"\") then .internal_hostname = $internal_hostname else . end)\n")
	b.WriteString("    | (if ((.internal_url // \"\") == \"\" and $internal_url != \"\") then .internal_url = $internal_url else . end)\n")
	b.WriteString("    | .domains = $domains\n")
	b.WriteString("    | .domain_state = $domain_state\n")
	b.WriteString("    | .proxy_mode = $proxy_mode\n")
	if plan.Action == "primary" {
		b.WriteString("    | .hostname = $canonical | .url = $url | .primary_domain = $canonical\n")
	}
	b.WriteString("  else . end)\n")
	b.WriteString("' /var/lib/nf/sites.json >\"$tmp\" && install -o ")
	b.WriteString(q(plan.Target.SSHUser))
	b.WriteString(" -g www-data -m 0664 \"$tmp\" /var/lib/nf/sites.json && rm -f \"$tmp\"\n")
	b.WriteString("nginx -t\n")
	b.WriteString("systemctl reload nginx\n")
	return b.String()
}

func renderLinodeDomainRemoveScript(plan siteDomainPlan, deleteCert bool) string {
	q := shellQuoteArg
	fileSlug := plan.FileSlug
	if fileSlug == "" {
		fileSlug = envIDFileSlug(plan.EnvID)
	}
	serviceName := "nf-public-domain-" + fileSlug + "-tls"
	refreshScript := "/usr/local/bin/nf-refresh-public-domain-" + fileSlug
	issueScript := "/usr/local/bin/nf-issue-public-domain-cert-" + fileSlug
	publicVhost := "/etc/nginx/sites-available/nf-site-public-" + fileSlug
	publicEnabled := "/etc/nginx/sites-enabled/nf-site-public-" + fileSlug
	domains, _ := json.Marshal(plan.allDomains())
	resetPrimary := "0"
	if siteDomainNameSet(plan.allDomains())[normalizeDomainName(plan.CurrentHostname)] || siteDomainNameSet(plan.allDomains())[normalizeDomainName(firstRecordString(plan.Record, "primary_domain"))] {
		resetPrimary = "1"
	}
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	b.WriteString("rm -f ")
	b.WriteString(q(publicEnabled))
	b.WriteByte(' ')
	b.WriteString(q(publicVhost))
	b.WriteByte(' ')
	b.WriteString(q(refreshScript))
	b.WriteByte(' ')
	b.WriteString(q(issueScript))
	b.WriteByte('\n')
	b.WriteString("systemctl disable --now ")
	b.WriteString(q(serviceName + ".timer"))
	b.WriteString(" >/dev/null 2>&1 || true\n")
	b.WriteString("systemctl stop ")
	b.WriteString(q(serviceName + ".service"))
	b.WriteString(" >/dev/null 2>&1 || true\n")
	b.WriteString("rm -f /etc/systemd/system/")
	b.WriteString(serviceName)
	b.WriteString(".service /etc/systemd/system/")
	b.WriteString(serviceName)
	b.WriteString(".timer\n")
	b.WriteString("systemctl daemon-reload\n")
	if deleteCert {
		b.WriteString("certbot delete --cert-name ")
		b.WriteString(q(plan.Canonical))
		b.WriteString(" --non-interactive >/dev/null 2>&1 || true\n")
	}
	b.WriteString("touch /var/lib/nf/sites.json\n")
	b.WriteString("if ! jq empty /var/lib/nf/sites.json >/dev/null 2>&1; then printf '[]\\n' >/var/lib/nf/sites.json; fi\n")
	b.WriteString("tmp=$(mktemp)\n")
	b.WriteString("jq --arg site_id ")
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
	b.WriteString(q(string(domains)))
	b.WriteString(" '\n")
	b.WriteString("  map(if (.site_id == $site_id and .env == $env) then\n")
	b.WriteString("    .domains = ((.domains // []) | map(select((.name // .domain // .domain_name // .hostname // \"\") as $name | (($remove_domains | index($name)) | not))))\n")
	b.WriteString("    | (if ((.domains // []) | length) == 0 then del(.domains, .domain_state, .proxy_mode) else . end)\n")
	b.WriteString("    | (if $reset_primary == \"1\" then (del(.primary_domain) | (if $internal_hostname != \"\" then .hostname = $internal_hostname else . end) | (if $internal_url != \"\" then .url = $internal_url else . end)) else . end)\n")
	b.WriteString("  else . end)\n")
	b.WriteString("' /var/lib/nf/sites.json >\"$tmp\" && install -o ")
	b.WriteString(q(plan.Target.SSHUser))
	b.WriteString(" -g www-data -m 0664 \"$tmp\" /var/lib/nf/sites.json && rm -f \"$tmp\"\n")
	b.WriteString("nginx -t\n")
	b.WriteString("systemctl reload nginx\n")
	return b.String()
}

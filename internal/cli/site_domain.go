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
	searchReplace  bool
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
	SearchReplace    bool
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
	action := strings.TrimSpace(argv[0])
	if action != "prepare" && action != "primary" && action != "check" {
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
		{"prepare <env|remote> <domain> [flags]", "make a provider/env ready for a public domain"},
		{"check <env|remote> <domain> [flags]", "check DNS, provider, HTTP, and HTTPS readiness"},
		{"primary <env|remote> <domain> [flags]", "launch a canonical public domain"},
		{},
		{"--canonical <domain>", "canonical public hostname"},
		{"--alias <domain>", "redirect/alternate hostname; repeatable"},
		{"--setup <type>", "Kinsta setup type for prepare/primary: avoid-downtime or quick"},
		{"--search-replace", "run provider/wp search-replace during primary"},
		{},
		{"--dry-run", "show the prepare/primary plan only"},
		{"--execute", "execute the prepare/primary plan"},
		{"--yes", "confirm prepare/primary execution"},
		{"--non-interactive", "fail instead of prompting"},
	})
	return 0
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
	if action == "check" {
		if opts.dryRun || opts.execute || opts.yes || opts.searchReplace || strings.TrimSpace(opts.setupType) != "" {
			fmt.Fprintln(os.Stderr, "site domain check is read-only; use only --canonical, --alias, and --non-interactive")
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
	willExecute := opts.execute || (!opts.dryRun && !opts.nonInteractive)
	mode := "dry-run"
	if willExecute {
		mode = "execute"
	}
	printSiteDomainPlan(plan, mode)
	if !willExecute {
		fmt.Println("No data was changed. Re-run with --execute to apply public-domain readiness.")
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
		if action == "primary" {
			result, err = kinstaPrimaryDomainFn(plan)
		} else {
			result, err = kinstaPrepareDomainFn(plan)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		printKinstaDomainRecords(result)
	case "linode":
		if err := runSSHCommandFn(remoteSudoBashArgs(plan.Target, renderLinodeDomainScript(plan))); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	default:
		fmt.Fprintf(os.Stderr, "site domain is not implemented for provider %q; no data was changed.\n", plan.Provider)
		return 1
	}
	if err := updateSiteDomainCache(plan, result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if action == "primary" {
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
		SearchReplace:    opts.searchReplace,
		CurrentURL:       currentURL,
		CurrentHostname:  currentHostname,
		InternalURL:      internalURL,
		InternalHostname: internalHostname,
		FileSlug:         envIDFileSlug(firstNonEmpty(siteRecordEnvID(record), canonicalEnvID(siteID, env))),
		PHPVersion:       sitePHPVersion(record),
	}
	switch plan.Provider {
	case "kinsta":
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
	if plan.Provider == "kinsta" {
		fmt.Printf("  kinsta setup: %s\n", displayKinstaSetupType(plan.SetupType))
		if plan.Action == "primary" {
			fmt.Printf("  search-replace: %t\n", plan.SearchReplace)
		}
		fmt.Println("  public DNS: no DNS records will be changed by nf")
		fmt.Println("  DNS records: returned by Kinsta after execution")
	} else if plan.Provider == "linode" {
		fmt.Println("  public DNS: no DNS records will be changed by nf")
		printLinodeDomainDNSInstructions(plan)
		if plan.Action == "primary" {
			fmt.Printf("  search-replace: %t\n", plan.SearchReplace)
		}
		fmt.Println("  TLS: HTTP-01 certbot retry timer will issue HTTPS after DNS points at the target")
	}
	fmt.Printf("  local state: %s\n", state.StatePath("sites"))
	if plan.Provider == "linode" {
		fmt.Println("  remote state: /var/lib/nf/sites.json")
	}
	fmt.Printf("  mode:      %s\n", mode)
}

func printLinodeDomainDNSInstructions(plan siteDomainPlan) {
	domains := plan.allDomains()
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

func applySiteDomainCacheFields(record map[string]any, plan siteDomainPlan, result siteDomainProviderResult) {
	if firstRecordString(record, "internal_hostname") == "" && plan.InternalHostname != "" {
		record["internal_hostname"] = plan.InternalHostname
	}
	if firstRecordString(record, "internal_url") == "" && plan.InternalURL != "" {
		record["internal_url"] = plan.InternalURL
	}
	record["domains"] = siteDomainCacheEntries(plan, result)
	record["domain_state"] = map[string]string{"prepare": "prepared", "primary": "primary"}[plan.Action]
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

func renderLinodeDomainScript(plan siteDomainPlan) string {
	domainEntries, _ := json.Marshal(siteDomainCacheEntries(plan, siteDomainProviderResult{}))
	q := shellQuoteArg
	allDomains := shellArrayValues(plan.allDomains())
	aliasDomains := shellArrayValues(plan.Aliases)
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
  if [ "$mode" = "primary" ] && [ "$cert_ready" = "1" ]; then
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
	b.WriteString("refresh_script=")
	b.WriteString(q(refreshScript))
	b.WriteByte('\n')
	b.WriteString(`args=(certbot certonly --non-interactive --agree-tos --webroot -w /var/www/letsencrypt -m web@nonfiction.ca --keep-until-expiring --deploy-hook "$refresh_script")
for domain in "${domains[@]}"; do args+=(-d "$domain"); done
"${args[@]}"
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
	b.WriteString(" --argjson domains ")
	b.WriteString(q(string(domainEntries)))
	b.WriteString(" '\n")
	b.WriteString("  map(if (.site_id == $site_id and .env == $env) then\n")
	b.WriteString("    (if ((.internal_hostname // \"\") == \"\" and $internal_hostname != \"\") then .internal_hostname = $internal_hostname else . end)\n")
	b.WriteString("    | (if ((.internal_url // \"\") == \"\" and $internal_url != \"\") then .internal_url = $internal_url else . end)\n")
	b.WriteString("    | .domains = $domains\n")
	b.WriteString("    | .domain_state = $domain_state\n")
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

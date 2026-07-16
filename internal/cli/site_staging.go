package cli

// Explicit staging environment lifecycle for hosted sites.

import (
	"fmt"
	"os"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/state"
	"github.com/nonfiction/nf/internal/ui"
)

type siteStagingAddPlan struct {
	Provider string
	SiteID   string
	Linode   siteAddPlan
	Kinsta   kinstaSiteAddPlan
}

func runSiteStaging(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runSiteStagingHelp()
	}
	action := cliCommandAlias(argv[0])
	switch action {
	case "status":
		if len(argv) > 2 {
			fmt.Fprintln(os.Stderr, "site staging status takes at most one site")
			return 1
		}
		siteRef := ""
		if len(argv) == 2 {
			siteRef = argv[1]
		}
		selected, err := resolveSiteStagingSite("status", siteRef, false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return cmdSiteStagingStatus(selected)
	case "add":
		siteID, opts, err := parseSiteStagingLifecycleArgs("add", argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		siteID, err = resolveSiteStagingSite("add", siteID, opts.nonInteractive)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return cmdSiteStagingAdd(siteID, opts)
	case "remove":
		siteID, opts, err := parseSiteStagingLifecycleArgs("remove", argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		siteID, err = resolveSiteStagingSite("remove", siteID, opts.nonInteractive)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return cmdSiteStagingRemove(siteID, opts)
	default:
		fmt.Fprintf(os.Stderr, "unsupported site staging action: %s\n", argv[0])
		return 1
	}
}

func runSiteStagingHelp() int {
	printGroupHelp("site staging", []helpLine{
		{"status [site]", "show whether staging exists"},
		{},
		{"add [site] [--dry-run] [--execute] [--yes] [--non-interactive]", "create staging env"},
		{"remove, rm [site] [--dry-run] [--execute] [--yes] [--non-interactive]", "delete staging env"},
	})
	return 0
}

func parseSiteStagingLifecycleArgs(action string, argv []string) (string, deleteServerOptions, error) {
	needle, opts, err := parseDeleteServerArgs(argv)
	if err != nil {
		return "", opts, fmt.Errorf("%s", strings.Replace(err.Error(), "server delete", "site staging "+action, 1))
	}
	return needle, opts, nil
}

func resolveSiteStagingSite(action, siteRef string, nonInteractive bool) (string, error) {
	siteRef = strings.TrimSpace(siteRef)
	if siteRef != "" {
		return siteRef, nil
	}
	if nonInteractive {
		return "", ProjectError{Msg: fmt.Sprintf("site staging %s requires a site in non-interactive mode", action)}
	}
	return chooseSiteForStaging(action)
}

func chooseSiteForStaging(action string) (string, error) {
	switch action {
	case "status":
		return chooseSite("show staging status for")
	case "add":
		return chooseSite("add staging to")
	case "remove":
		return chooseSite("remove staging from")
	default:
		return chooseSite("manage staging for")
	}
}

func siteLifecycleMode(opts deleteServerOptions) (bool, string, error) {
	if opts.execute && opts.dryRun {
		return false, "", fmt.Errorf("Choose either --execute or --dry-run, not both.")
	}
	if opts.nonInteractive && opts.execute && !opts.yes {
		return false, "", fmt.Errorf("Remote execution requires both --execute and --yes in non-interactive mode.")
	}
	if !opts.execute && (opts.dryRun || opts.nonInteractive) {
		opts.dryRun = true
	}
	willExecute := opts.execute || (!opts.dryRun && !opts.nonInteractive)
	mode := "dry-run"
	if willExecute {
		mode = "execute"
	}
	return willExecute, mode, nil
}

func cmdSiteStagingStatus(siteRef string) int {
	matches, resolvedSiteID, err := loadSiteRecordsForStaging(siteRef)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	live := findSiteRecordByEnv(matches, "live")
	staging := findSiteRecordByEnv(matches, "staging")
	base := live
	if base == nil {
		base = matches[0]
	}
	fmt.Println("Site staging status:")
	fmt.Printf("  site id: %s\n", resolvedSiteID)
	fmt.Printf("  provider: %s\n", recordValueString(base["provider"]))
	if target := siteProviderTarget(base); target != "" {
		fmt.Printf("  target: %s\n", target)
	}
	if live == nil {
		fmt.Println("  live: not found")
	} else {
		fmt.Println("  live: active")
	}
	if staging == nil {
		fmt.Println("  staging: not created")
		fmt.Printf("  create staging: nf site staging add %s\n", resolvedSiteID)
		return 0
	}
	fmt.Println("  staging: active")
	if envID := siteRecordEnvID(staging); envID != "" {
		fmt.Printf("  env id: %s\n", envID)
	}
	if url := firstRecordString(staging, "url", "site_url", "home_url", "hostname"); url != "" {
		fmt.Printf("  url: %s\n", url)
	}
	return 0
}

func cmdSiteStagingAdd(siteRef string, opts deleteServerOptions) int {
	willExecute, mode, err := siteLifecycleMode(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	plan, err := buildSiteStagingAddPlan(siteRef)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	printSiteStagingAddPlan(plan, mode)
	if !willExecute {
		return 0
	}
	if !opts.yes {
		confirmed, err := ui.Confirm(fmt.Sprintf("Add staging env for %q?", plan.SiteID), false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	if plan.Provider == "kinsta" {
		result, err := kinstaProvisionStagingFn(plan.Kinsta)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			fmt.Fprintln(os.Stderr, "Kinsta staging add is resumable; rerun the same command after fixing the error.")
			return 1
		}
		if err := upsertKinstaSiteEnvRecords(plan.Kinsta, result); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	} else {
		if err := runSSHScriptFn(plan.Linode.SSHUser, plan.Linode.SSHHost, renderSiteAddScript(plan.Linode)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := appendSiteStagingRecord(plan.Linode); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	fmt.Println("Staging env added.")
	return 0
}

func cmdSiteStagingRemove(siteRef string, opts deleteServerOptions) int {
	willExecute, mode, err := siteLifecycleMode(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	plan, err := buildSiteStagingRemovePlan(siteRef)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	printSiteRemovePlan(plan, mode)
	if !willExecute {
		return 0
	}
	if !opts.yes {
		confirmed, err := ui.Confirm(fmt.Sprintf("Remove staging env for %q?", plan.SiteID), false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	if plan.Provider == "kinsta" {
		if err := kinstaRemoveSiteFn(plan); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	} else {
		if err := runSSHScriptFn(plan.SSHUser, plan.SSHHost, renderSiteRemoveScript(plan)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if err := removeSiteEnvFromLocalCache(plan.SiteID, "staging"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Staging env removed.")
	return 0
}

func buildSiteStagingAddPlan(siteRef string) (siteStagingAddPlan, error) {
	matches, resolvedSiteID, err := loadSiteRecordsForStaging(siteRef)
	if err != nil {
		return siteStagingAddPlan{}, err
	}
	staging := findSiteRecordByEnv(matches, "staging")
	live := findSiteRecordByEnv(matches, "live")
	if live == nil {
		return siteStagingAddPlan{}, ProjectError{Msg: fmt.Sprintf("Site %q has no live env to stage from.", resolvedSiteID)}
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(live["provider"])))
	if staging != nil && provider != "kinsta" {
		return siteStagingAddPlan{}, ProjectError{Msg: fmt.Sprintf("Site %q already has staging env.", resolvedSiteID)}
	}
	switch provider {
	case "linode":
		plan, err := buildLinodeSiteStagingAddPlan(live, resolvedSiteID)
		if err != nil {
			return siteStagingAddPlan{}, err
		}
		return siteStagingAddPlan{Provider: provider, SiteID: resolvedSiteID, Linode: plan}, nil
	case "kinsta":
		plan, err := buildKinstaSiteStagingAddPlan(live, resolvedSiteID)
		if err != nil {
			return siteStagingAddPlan{}, err
		}
		return siteStagingAddPlan{Provider: provider, SiteID: resolvedSiteID, Kinsta: plan}, nil
	default:
		return siteStagingAddPlan{}, ProjectError{Msg: fmt.Sprintf("Unsupported provider %q. Only linode and kinsta staging add are available.", provider)}
	}
}

func buildSiteStagingRemovePlan(siteRef string) (siteRemovePlan, error) {
	matches, resolvedSiteID, err := loadSiteRecordsForStaging(siteRef)
	if err != nil {
		return siteRemovePlan{}, err
	}
	staging := findSiteRecordByEnv(matches, "staging")
	if staging == nil {
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("Site %q has no staging env.", resolvedSiteID)}
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(staging["provider"])))
	var plan siteRemovePlan
	switch provider {
	case "linode":
		plan, err = buildLinodeSiteRemovePlan([]map[string]any{staging}, resolvedSiteID)
	case "kinsta":
		plan, err = buildKinstaSiteRemovePlan([]map[string]any{staging}, resolvedSiteID)
	default:
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("Unsupported provider %q. Only linode and kinsta staging remove are available.", provider)}
	}
	if err != nil {
		return siteRemovePlan{}, err
	}
	plan.EnvOnly = true
	return plan, nil
}

func buildLinodeSiteStagingAddPlan(live map[string]any, resolvedSiteID string) (siteAddPlan, error) {
	values, err := loadGlobalConfig()
	if err != nil {
		return siteAddPlan{}, err
	}
	baseDomain := strings.TrimSuffix(strings.TrimSpace(values["base_domain"]), ".")
	if baseDomain == "" {
		return siteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Expected base_domain in %s. Set it with nf config set-base-domain <domain>.", config.ConfigFile())}
	}
	adminEmail := strings.TrimSpace(values["default_wp_email"])
	if adminEmail == "" {
		return siteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Expected default_wp_email in %s. Set it with nf config set-default-wp-email <email>.", config.ConfigFile())}
	}
	adminUser := firstNonEmpty(values["default_wp_user"], defaultWordPressAdminUser)
	targetName := siteProviderTarget(live)
	if targetName == "" {
		return siteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Site %q is missing a target.", resolvedSiteID)}
	}
	targets, err := cachedTargets()
	if err != nil {
		return siteAddPlan{}, err
	}
	target := state.MatchingRecord(targets, targetName)
	if target == nil {
		return siteAddPlan{}, ProjectError{Msg: fmt.Sprintf("No target matched site target %q.", targetName)}
	}
	sshHost := serverSSHHost(target)
	if sshHost == "" {
		return siteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Target %q is missing an SSH host.", targetName)}
	}
	sshUser := firstNonEmpty(serverSSHUser(target), values["linode_default_user"])
	if sshUser == "" {
		return siteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Target %q is missing an SSH user. Set linode_default_user with nf config set-linode-default-user <user>.", targetName)}
	}
	siteSlug, err := cleanSiteSlug(siteSlugFromRecord(live, resolvedSiteID, targetName))
	if err != nil {
		return siteAddPlan{}, err
	}
	passwordVersion := currentProjectPasswordVersionForSite(siteSlug)
	adminPassword, err := deriveProjectPassword(siteSlug, "wp-admin", passwordVersion)
	if err != nil {
		return siteAddPlan{}, err
	}
	dbPassword, err := deriveProjectPassword(siteSlug, "mysql", passwordVersion)
	if err != nil {
		return siteAddPlan{}, err
	}
	hostname := siteEnvHostname(siteSlug, targetName, baseDomain, "staging")
	return siteAddPlan{
		Target:          target,
		TargetName:      targetName,
		SSHUser:         sshUser,
		SSHHost:         sshHost,
		Site:            siteSlug,
		SiteID:          resolvedSiteID,
		BaseDomain:      baseDomain,
		PasswordVersion: passwordVersion,
		PHPVersion:      firstNonEmpty(sitePHPVersion(live), targetPHPVersion(target)),
		AdminUser:       adminUser,
		AdminEmail:      adminEmail,
		AdminPassword:   adminPassword,
		DBPassword:      dbPassword,
		Envs: []siteEnvPlan{{
			Env:      "staging",
			Path:     siteEnvPath(siteSlug, "staging"),
			Database: siteDBName(siteSlug, "staging"),
			Hostname: hostname,
			URL:      "https://" + hostname,
			Title:    siteEnvTitle(siteSlug, "staging"),
		}},
	}, nil
}

func buildKinstaSiteStagingAddPlan(live map[string]any, resolvedSiteID string) (kinstaSiteAddPlan, error) {
	values, err := loadGlobalConfig()
	if err != nil {
		return kinstaSiteAddPlan{}, err
	}
	baseDomain := strings.TrimSuffix(strings.TrimSpace(values["base_domain"]), ".")
	if baseDomain == "" {
		return kinstaSiteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Expected base_domain in %s. Set it with nf config set-base-domain <domain>.", config.ConfigFile())}
	}
	dnsAccountID := firstNonEmpty(values["dnsimple_account_id"], dnsimpleAccountIDValue())
	if dnsAccountID == "" {
		return kinstaSiteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Expected dnsimple_account_id in %s. Run nf provider check dnsimple.", config.ConfigFile())}
	}
	remoteSiteID := siteKinstaID(live, "site_id")
	if remoteSiteID == "" {
		return kinstaSiteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Selected Kinsta site %q has no Kinsta site_id. Run nf site refresh and try again.", resolvedSiteID)}
	}
	targetName := firstNonEmpty(siteProviderTarget(live), "kinsta")
	siteSlug, err := cleanSiteSlug(siteSlugFromRecord(live, resolvedSiteID, targetName))
	if err != nil {
		return kinstaSiteAddPlan{}, err
	}
	domain := kinstaSiteDomain(siteSlug, baseDomain, "staging")
	return kinstaSiteAddPlan{
		TargetName:   targetName,
		CompanyID:    firstRecordString(live, "company_id", "company"),
		KinstaSiteID: remoteSiteID,
		KinstaSlug:   firstNonEmpty(siteKinstaID(live, "slug"), siteSlug),
		Site:         siteSlug,
		SiteID:       resolvedSiteID,
		BaseDomain:   baseDomain,
		PHPVersion:   sitePHPVersion(live),
		DNSZone:      baseDomain,
		DNSAccountID: dnsAccountID,
		Envs: []kinstaSiteAddEnvPlan{{
			Env:    "staging",
			Domain: domain,
			URL:    "https://" + domain,
			Title:  siteEnvTitle(siteSlug, "staging"),
			Branch: "develop",
		}},
	}, nil
}

func printSiteStagingAddPlan(plan siteStagingAddPlan, mode string) {
	fmt.Println("Add staging env plan:")
	fmt.Printf("  site id: %s\n", plan.SiteID)
	fmt.Printf("  provider: %s\n", plan.Provider)
	if plan.Provider == "kinsta" {
		fmt.Printf("  target: %s\n", plan.Kinsta.TargetName)
		fmt.Printf("  kinsta site id: %s\n", plan.Kinsta.KinstaSiteID)
		if plan.Kinsta.PHPVersion != "" {
			fmt.Printf("  php: %s\n", plan.Kinsta.PHPVersion)
		}
		for _, env := range plan.Kinsta.Envs {
			fmt.Printf("  env %s:\n", env.Env)
			fmt.Printf("    domain: %s\n", env.Domain)
			fmt.Printf("    url: %s\n", env.URL)
		}
		fmt.Printf("  dns: dnsimple zone %s account %s\n", plan.Kinsta.DNSZone, plan.Kinsta.DNSAccountID)
		fmt.Printf("  local state: %s\n", state.StatePath("sites"))
		fmt.Printf("  mode: %s\n", mode)
		return
	}
	fmt.Printf("  target: %s\n", plan.Linode.TargetName)
	fmt.Printf("  ssh: %s@%s\n", plan.Linode.SSHUser, plan.Linode.SSHHost)
	for _, env := range plan.Linode.Envs {
		fmt.Printf("  env %s:\n", env.Env)
		fmt.Printf("    path: %s\n", env.Path)
		fmt.Printf("    database: %s\n", env.Database)
		if plan.Linode.PHPVersion != "" {
			fmt.Printf("    php: %s\n", plan.Linode.PHPVersion)
		}
		fmt.Printf("    vhost: %s\n", env.Hostname)
		fmt.Printf("    url: %s\n", env.URL)
	}
	fmt.Printf("  remote state: /var/lib/nf/sites.json\n")
	fmt.Printf("  local state: %s\n", state.StatePath("sites"))
	fmt.Printf("  mode: %s\n", mode)
}

func appendSiteStagingRecord(plan siteAddPlan) error {
	existing, err := state.LoadStateRecords("sites")
	if err != nil {
		return err
	}
	if siteEnvRecordExists(existing, plan.SiteID, "staging") {
		return ProjectError{Msg: fmt.Sprintf("Site %q already has staging env.", plan.SiteID)}
	}
	existing = append(existing, siteAddRecords(plan)...)
	return state.SaveStateRecords("sites", existing)
}

func loadSiteRecordsForStaging(siteRef string) ([]map[string]any, string, error) {
	if siteID, _, ok := splitSiteEnvRef(siteRef); ok {
		siteRef = siteID
	}
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		return nil, "", err
	}
	matches, resolvedSiteID, err := siteRecordsMatchingSite(records, siteRef)
	if err != nil {
		return nil, "", err
	}
	if len(matches) == 0 {
		return nil, "", ProjectError{Msg: fmt.Sprintf("No site matched %q.", siteRef)}
	}
	return matches, resolvedSiteID, nil
}

func findSiteRecordByEnv(records []map[string]any, env string) map[string]any {
	for _, record := range records {
		if normalizedRecordString(siteEnvName(record)) == normalizedRecordString(env) {
			return record
		}
	}
	return nil
}

func siteEnvRecordExists(records []map[string]any, siteID, env string) bool {
	for _, record := range records {
		if normalizedRecordString(siteRecordID(record)) == normalizedRecordString(siteID) && normalizedRecordString(siteEnvName(record)) == normalizedRecordString(env) {
			return true
		}
	}
	return false
}

func siteSlugFromRecord(record map[string]any, resolvedSiteID, targetName string) string {
	if name := siteRecordName(record); name != "" {
		return name
	}
	siteID := firstNonEmpty(siteRecordID(record), resolvedSiteID)
	if targetName != "" {
		suffix := "." + targetName
		if strings.HasSuffix(siteID, suffix) {
			return strings.TrimSuffix(siteID, suffix)
		}
	}
	if left, _, ok := strings.Cut(siteID, "."); ok {
		return left
	}
	return siteID
}

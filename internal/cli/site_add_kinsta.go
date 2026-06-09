package cli

// Kinsta site add planning, provisioning, DNS, and cache writes.

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/kinsta"
	"github.com/nonfiction/nf/internal/state"
	"github.com/nonfiction/nf/internal/ui"
)

func kinstaSiteID(site string) string {
	return site + ".kinsta"
}

func kinstaSiteDomain(site, baseDomain, env string) string {
	label := site
	if env == "staging" {
		label += "-staging"
	}
	return label + ".kinsta." + baseDomain
}

func buildKinstaSiteAddPlan(args siteAddArgs) (kinstaSiteAddPlan, error) {
	siteSlug, err := cleanSiteSlug(args.site)
	if err != nil {
		return kinstaSiteAddPlan{}, err
	}
	values, err := loadGlobalConfig()
	if err != nil {
		return kinstaSiteAddPlan{}, err
	}
	baseDomain := strings.TrimSuffix(strings.TrimSpace(values["base_domain"]), ".")
	if baseDomain == "" {
		return kinstaSiteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Expected base_domain in %s. Set it with nf config set-base-domain <domain>.", config.ConfigFile())}
	}
	adminEmail := strings.TrimSpace(values["default_wp_email"])
	if adminEmail == "" {
		return kinstaSiteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Expected default_wp_email in %s. Set it with nf config set-default-wp-email <email>.", config.ConfigFile())}
	}
	adminUser := firstNonEmpty(values["default_wp_user"], "admin")
	region := firstNonEmpty(args.region, values["kinsta_default_region"], "ca-toronto-1")
	phpVersion := firstNonEmpty(args.phpVersion, values["kinsta_default_php"], "8.3")
	targets, err := cachedTargets()
	if err != nil {
		return kinstaSiteAddPlan{}, err
	}
	target := state.MatchingRecord(targets, args.target)
	if target == nil {
		return kinstaSiteAddPlan{}, ProjectError{Msg: fmt.Sprintf("No target matched %q.", args.target)}
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(target["provider"])))
	if provider != "kinsta" {
		return kinstaSiteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Unsupported provider %q. Only kinsta site add is available.", provider)}
	}
	targetName := firstRecordString(target, "target_name", "name", "slug", "label", "_state_key")
	if targetName == "" {
		targetName = "kinsta"
	}
	companyID := firstRecordString(target, "company_id", "company")
	dnsAccountID := firstNonEmpty(values["dnsimple_account_id"], dnsimpleAccountIDValue())
	if dnsAccountID == "" {
		return kinstaSiteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Expected dnsimple_account_id in %s. Run nf provider check dnsimple.", config.ConfigFile())}
	}
	passwordVersion := currentProjectPasswordVersionForSite(siteSlug)
	adminPassword, err := deriveProjectPassword(siteSlug, "wp-admin", passwordVersion)
	if err != nil {
		return kinstaSiteAddPlan{}, err
	}
	plan := kinstaSiteAddPlan{
		Target:          target,
		TargetName:      targetName,
		CompanyID:       companyID,
		Site:            siteSlug,
		SiteID:          kinstaSiteID(siteSlug),
		BaseDomain:      baseDomain,
		PasswordVersion: passwordVersion,
		Region:          region,
		PHPVersion:      phpVersion,
		AdminUser:       adminUser,
		AdminEmail:      adminEmail,
		AdminPassword:   adminPassword,
		DNSZone:         baseDomain,
		DNSAccountID:    dnsAccountID,
	}
	for _, env := range []string{"live", "staging"} {
		domain := kinstaSiteDomain(siteSlug, baseDomain, env)
		branch := "main"
		if env == "staging" {
			branch = "develop"
		}
		plan.Envs = append(plan.Envs, kinstaSiteAddEnvPlan{Env: env, Domain: domain, URL: "https://" + domain, Title: siteEnvTitle(siteSlug, env), Branch: branch})
	}
	return plan, nil
}

func siteAddRecord(plan siteAddPlan, env siteEnvPlan) map[string]any {
	envID := canonicalEnvID(plan.SiteID, env.Env)
	sshPort := firstNonEmpty(mapStringAtPath(plan.Target, "ssh", "port"), firstRecordString(plan.Target, "ssh_port"), "22")
	return map[string]any{
		"provider":    "linode",
		"env_id":      envID,
		"site_id":     plan.SiteID,
		"name":        plan.Site,
		"env":         env.Env,
		"target":      plan.TargetName,
		"hostname":    env.Hostname,
		"url":         env.URL,
		"path":        env.Path,
		"database":    env.Database,
		"php_version": plan.PHPVersion,
		"status":      "active",
		"ssh":         sshRecord(plan.SSHUser, plan.SSHHost, sshPort, sshCommand(plan.SSHUser, plan.SSHHost, sshPort)),
	}
}

func siteAddRecords(plan siteAddPlan) []map[string]any {
	records := make([]map[string]any, 0, len(plan.Envs))
	for _, env := range plan.Envs {
		records = append(records, siteAddRecord(plan, env))
	}
	return records
}

func appendSiteAddRecords(plan siteAddPlan) error {
	existing, err := state.LoadStateRecords("sites")
	if err != nil {
		return err
	}
	if err := ensureSiteNotCached(existing, plan.SiteID); err != nil {
		return err
	}
	existing = append(existing, siteAddRecords(plan)...)
	return state.SaveStateRecords("sites", existing)
}

func kinstaSiteAddRecord(plan kinstaSiteAddPlan, env kinstaSiteAddEnvPlan, result kinstaProvisionResult) map[string]any {
	return map[string]any{
		"provider":    "kinsta",
		"env_id":      canonicalEnvID(plan.SiteID, env.Env),
		"site_id":     plan.SiteID,
		"name":        plan.Site,
		"env":         env.Env,
		"target":      plan.TargetName,
		"hostname":    env.Domain,
		"url":         env.URL,
		"path":        env.Path,
		"database":    env.Database,
		"php_version": plan.PHPVersion,
		"status":      "active",
		"ssh":         sshRecord(env.SSHUser, env.SSHHost, env.SSHPort, env.SSHCmd),
		"kinsta": map[string]any{
			"site_id":        result.SiteID,
			"environment_id": env.EnvID,
			"domain_id":      env.DomainID,
			"branch":         env.Branch,
		},
	}
}

func upsertKinstaSiteAddRecords(plan kinstaSiteAddPlan, result kinstaProvisionResult) error {
	existing, err := state.LoadStateRecords("sites")
	if err != nil {
		return err
	}
	kept := make([]map[string]any, 0, len(existing)+len(result.Envs))
	for _, record := range existing {
		if siteEnvMatchesSite(record, plan.SiteID) {
			continue
		}
		kept = append(kept, record)
	}
	for _, env := range result.Envs {
		kept = append(kept, kinstaSiteAddRecord(plan, env, result))
	}
	return state.SaveStateRecords("sites", kept)
}

func printKinstaSiteAddPlan(plan kinstaSiteAddPlan, mode string) {
	fmt.Println("Add site plan:")
	fmt.Printf("  target: %s\n", plan.TargetName)
	fmt.Printf("  provider: kinsta\n")
	if plan.CompanyID != "" {
		fmt.Printf("  company id: %s\n", plan.CompanyID)
	}
	fmt.Printf("  site: %s\n", plan.Site)
	fmt.Printf("  site id: %s\n", plan.SiteID)
	fmt.Printf("  password version: %s\n", firstNonEmpty(plan.PasswordVersion, "0"))
	fmt.Printf("  region: %s\n", plan.Region)
	fmt.Printf("  php: %s\n", plan.PHPVersion)
	fmt.Printf("  admin user: %s\n", plan.AdminUser)
	fmt.Printf("  admin email: %s\n", plan.AdminEmail)
	fmt.Printf("  admin password: derived from %s\n", plan.Site)
	for _, env := range plan.Envs {
		fmt.Printf("  env %s:\n", env.Env)
		fmt.Printf("    domain: %s\n", env.Domain)
		fmt.Printf("    url: %s\n", env.URL)
	}
	fmt.Printf("  dns: dnsimple zone %s account %s\n", plan.DNSZone, plan.DNSAccountID)
	fmt.Printf("  local state: %s\n", state.StatePath("sites"))
	fmt.Printf("  mode: %s\n", mode)
}

func cmdKinstaSiteAdd(args siteAddArgs) int {
	if args.execute && args.dryRun {
		fmt.Fprintln(os.Stderr, "Choose either --execute or --dry-run, not both.")
		return 1
	}
	if !args.execute && (args.dryRun || args.nonInteractive) {
		args.dryRun = true
	}
	if args.nonInteractive && args.execute && !args.yes {
		fmt.Fprintln(os.Stderr, "Remote execution requires both --execute and --yes in non-interactive mode.")
		return 1
	}
	plan, err := buildKinstaSiteAddPlan(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	willExecute := args.execute || (!args.dryRun && !args.nonInteractive)
	mode := "dry-run"
	if willExecute {
		mode = "execute"
	}
	printKinstaSiteAddPlan(plan, mode)
	if !willExecute {
		return 0
	}
	if !args.yes {
		confirmed, err := ui.Confirm(fmt.Sprintf("Add site %q with live and staging envs on target %q?", plan.Site, plan.TargetName), false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	result, err := kinstaProvisionSiteFn(plan)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, "Kinsta site add is resumable; rerun the same command after fixing the error.")
		return 1
	}
	if err := upsertKinstaSiteAddRecords(plan, result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Site added.")
	return 0
}

func provisionKinstaSite(plan kinstaSiteAddPlan) (kinstaProvisionResult, error) {
	token := envwizard.Value("KINSTA_API_KEY")
	if token == "" {
		return kinstaProvisionResult{}, fmt.Errorf("Expected KINSTA_API_KEY in the environment or %s.", config.EnvFile())
	}
	dnsToken := envwizard.Value("DNSIMPLE_TOKEN")
	if dnsToken == "" {
		return kinstaProvisionResult{}, fmt.Errorf("Expected DNSIMPLE_TOKEN in the environment or %s.", config.EnvFile())
	}
	client := kinsta.NewClient(firstNonEmpty(envwizard.Value("KINSTA_BASE_URL"), "https://api.kinsta.com/v2"), token)
	ctx := context.Background()
	companyID := plan.CompanyID
	if companyID == "" {
		validate, err := client.Validate(ctx)
		if err != nil {
			return kinstaProvisionResult{}, err
		}
		companyID = strings.TrimSpace(validate.Company)
		if companyID == "" {
			return kinstaProvisionResult{}, fmt.Errorf("Kinsta /v2/validate did not return a company uuid")
		}
		plan.CompanyID = companyID
	}
	kinstaSite, err := ensureKinstaSite(ctx, client, plan, companyID)
	if err != nil {
		return kinstaProvisionResult{}, err
	}
	liveEnv, stagingEnv, err := ensureKinstaEnvironments(ctx, client, kinstaSite.ID, plan.PHPVersion)
	if err != nil {
		return kinstaProvisionResult{}, err
	}
	result := kinstaProvisionResult{CompanyID: companyID, SiteID: kinstaSite.ID, Envs: make([]kinstaSiteAddEnvPlan, 0, len(plan.Envs))}
	for _, env := range plan.Envs {
		remoteEnv := liveEnv
		if env.Env == "staging" {
			remoteEnv = stagingEnv
		}
		domain, err := ensureKinstaDomain(ctx, client, remoteEnv.ID, env.Domain)
		if err != nil {
			return kinstaProvisionResult{}, err
		}
		records, err := client.DomainRecords(ctx, domain.ID)
		if err != nil {
			return kinstaProvisionResult{}, err
		}
		if err := upsertKinstaDNSRecords(dnsToken, plan.DNSAccountID, plan.DNSZone, env.Domain, records); err != nil {
			return kinstaProvisionResult{}, err
		}
		if !domain.IsPrimary {
			opID, err := client.ChangePrimaryDomain(ctx, remoteEnv.ID, domain.ID, true)
			if err != nil {
				return kinstaProvisionResult{}, err
			}
			if err := waitKinstaOperation(ctx, client, opID); err != nil {
				return kinstaProvisionResult{}, err
			}
		}
		env.EnvID = remoteEnv.ID
		env.DomainID = domain.ID
		if cfg, err := client.SFTPConfig(ctx, result.SiteID, remoteEnv.ID); err == nil {
			env.SSHHost = cfg.Host
			env.SSHPort = firstNonEmpty(cfg.Port, "22")
			env.SSHUser = cfg.User
			env.SSHCmd = cfg.SSHCommand
			env.Path = kinstaEnvPath(cfg.User, remoteEnv.WebRoot)
			env.Database = cfg.User
		} else {
			env.SSHHost = remoteEnv.SSHConnection.SSHIP.ExternalIP
			env.SSHPort = firstNonEmpty(remoteEnv.SSHConnection.SSHPort, "22")
			env.Path = kinstaEnvPath(plan.Site, remoteEnv.WebRoot)
			env.Database = plan.Site
		}
		result.Envs = append(result.Envs, env)
	}
	return result, nil
}

func kinstaEnvPath(user, webRoot string) string {
	user = strings.TrimSpace(user)
	if user == "" {
		return ""
	}
	root := path.Join("/www", user, "public")
	webRoot = strings.TrimSpace(webRoot)
	if webRoot == "" || webRoot == "/" {
		return root
	}
	if strings.HasPrefix(webRoot, "/www/") {
		return path.Clean(webRoot)
	}
	return path.Join(root, webRoot)
}

func ensureKinstaSite(ctx context.Context, client *kinsta.Client, plan kinstaSiteAddPlan, companyID string) (kinsta.Site, error) {
	sites, err := client.ListSites(ctx, companyID)
	if err != nil {
		return kinsta.Site{}, err
	}
	if site, ok := kinsta.FindSite(sites, plan.Site); ok {
		return site, nil
	}
	fmt.Printf("Creating Kinsta site %s in %s...\n", plan.Site, plan.Region)
	opID, err := client.CreateSite(ctx, kinsta.CreateSiteRequest{
		Company:              companyID,
		DisplayName:          plan.Site,
		Region:               plan.Region,
		InstallMode:          "new",
		AdminEmail:           plan.AdminEmail,
		AdminPassword:        plan.AdminPassword,
		AdminUser:            plan.AdminUser,
		SiteTitle:            plan.Site,
		WPLanguage:           "en_US",
		IsSubdomainMultisite: false,
		IsMultisite:          false,
		WooCommerce:          false,
		WordPressSEO:         false,
	})
	if err != nil {
		return kinsta.Site{}, err
	}
	if err := waitKinstaOperation(ctx, client, opID); err != nil {
		return kinsta.Site{}, err
	}
	sites, err = client.ListSites(ctx, companyID)
	if err != nil {
		return kinsta.Site{}, err
	}
	if site, ok := kinsta.FindSite(sites, plan.Site); ok {
		return site, nil
	}
	return kinsta.Site{}, fmt.Errorf("Kinsta site %q was created but was not found in site list", plan.Site)
}

func ensureKinstaEnvironments(ctx context.Context, client *kinsta.Client, siteID, phpVersion string) (kinsta.Environment, kinsta.Environment, error) {
	envs, err := waitKinstaEnvironments(ctx, client, siteID, func(envs []kinsta.Environment) (kinsta.Environment, bool) {
		return findKinstaLiveEnvironment(envs)
	})
	if err != nil {
		return kinsta.Environment{}, kinsta.Environment{}, err
	}
	live, ok := findKinstaLiveEnvironment(envs)
	if !ok {
		return kinsta.Environment{}, kinsta.Environment{}, fmt.Errorf("Kinsta site %s is missing live environment; found: %s", siteID, kinstaEnvironmentSummary(envs))
	}
	if err := ensureKinstaEnvironmentPHP(ctx, client, live, phpVersion); err != nil {
		return kinsta.Environment{}, kinsta.Environment{}, err
	}
	envs, err = waitKinstaEnvironments(ctx, client, siteID, func(envs []kinsta.Environment) (kinsta.Environment, bool) {
		return findKinstaLiveEnvironment(envs)
	})
	if err != nil {
		return kinsta.Environment{}, kinsta.Environment{}, err
	}
	live, ok = findKinstaLiveEnvironment(envs)
	if !ok {
		return kinsta.Environment{}, kinsta.Environment{}, fmt.Errorf("Kinsta site %s is missing live environment; found: %s", siteID, kinstaEnvironmentSummary(envs))
	}
	staging, ok := findKinstaStagingEnvironment(envs, live)
	if ok {
		if err := ensureKinstaEnvironmentPHP(ctx, client, staging, phpVersion); err != nil {
			return kinsta.Environment{}, kinsta.Environment{}, err
		}
		return live, staging, nil
	}
	fmt.Println("Creating Kinsta staging environment...")
	opID, err := client.CloneEnvironment(ctx, siteID, kinsta.CloneEnvironmentRequest{DisplayName: "Staging", IsPremium: false, SourceEnvID: live.ID})
	if err != nil {
		return kinsta.Environment{}, kinsta.Environment{}, err
	}
	if err := waitKinstaOperation(ctx, client, opID); err != nil {
		return kinsta.Environment{}, kinsta.Environment{}, err
	}
	envs, err = waitKinstaEnvironments(ctx, client, siteID, func(envs []kinsta.Environment) (kinsta.Environment, bool) {
		return findKinstaStagingEnvironment(envs, live)
	})
	if err != nil {
		return kinsta.Environment{}, kinsta.Environment{}, err
	}
	staging, ok = findKinstaStagingEnvironment(envs, live)
	if !ok {
		return kinsta.Environment{}, kinsta.Environment{}, fmt.Errorf("Kinsta staging environment was created but was not found in environment list; found: %s", kinstaEnvironmentSummary(envs))
	}
	if err := ensureKinstaEnvironmentPHP(ctx, client, staging, phpVersion); err != nil {
		return kinsta.Environment{}, kinsta.Environment{}, err
	}
	return live, staging, nil
}

func ensureKinstaEnvironmentPHP(ctx context.Context, client *kinsta.Client, env kinsta.Environment, phpVersion string) error {
	phpVersion = strings.TrimSpace(phpVersion)
	if phpVersion == "" || env.ID == "" || env.CurrentPHPVersion() == phpVersion {
		return nil
	}
	fmt.Printf("Setting Kinsta PHP %s on environment %s...\n", phpVersion, firstNonEmpty(env.Name, env.DisplayName, env.ID))
	opID, err := client.ModifyPHPVersion(ctx, kinsta.ModifyPHPVersionRequest{EnvironmentID: env.ID, PHPVersion: phpVersion, IsOptOutFromAutomaticPHPUpdate: false})
	if err != nil {
		return err
	}
	return waitKinstaOperation(ctx, client, opID)
}

func waitKinstaEnvironments(ctx context.Context, client *kinsta.Client, siteID string, ready func([]kinsta.Environment) (kinsta.Environment, bool)) ([]kinsta.Environment, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var last []kinsta.Environment
	for {
		envs, err := client.ListEnvironments(ctx, siteID)
		if err != nil {
			return nil, err
		}
		last = envs
		if _, ok := ready(envs); ok {
			return envs, nil
		}
		select {
		case <-ctx.Done():
			return last, fmt.Errorf("timed out waiting for Kinsta environments for site %s; found: %s", siteID, kinstaEnvironmentSummary(last))
		case <-ticker.C:
		}
	}
}

func findKinstaLiveEnvironment(envs []kinsta.Environment) (kinsta.Environment, bool) {
	if live, ok := kinsta.FindEnvironment(envs, "live"); ok {
		return live, true
	}
	if len(envs) == 1 {
		return envs[0], true
	}
	return kinsta.Environment{}, false
}

func findKinstaStagingEnvironment(envs []kinsta.Environment, live kinsta.Environment) (kinsta.Environment, bool) {
	if staging, ok := kinsta.FindEnvironment(envs, "staging"); ok {
		return staging, true
	}
	if len(envs) == 2 {
		for _, env := range envs {
			if env.ID != live.ID {
				return env, true
			}
		}
	}
	return kinsta.Environment{}, false
}

func kinstaEnvironmentSummary(envs []kinsta.Environment) string {
	if len(envs) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(envs))
	for _, env := range envs {
		parts = append(parts, fmt.Sprintf("id=%s name=%q display_name=%q", env.ID, env.Name, env.DisplayName))
	}
	return strings.Join(parts, "; ")
}

func ensureKinstaDomain(ctx context.Context, client *kinsta.Client, envID, domainName string) (kinsta.Domain, error) {
	domains, err := client.ListDomains(ctx, envID)
	if err != nil {
		return kinsta.Domain{}, err
	}
	if domain, ok := kinsta.FindDomain(domains, domainName); ok {
		return domain, nil
	}
	fmt.Printf("Adding Kinsta domain %s...\n", domainName)
	opID, err := client.AddDomain(ctx, envID, kinsta.AddDomainRequest{DomainName: domainName, IsWildcardless: false, AddWithWWWSubdomain: false, SetupType: "quick"})
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

func waitKinstaOperation(parent context.Context, client *kinsta.Client, opID string) error {
	ctx, cancel := context.WithTimeout(parent, 30*time.Minute)
	defer cancel()
	return client.WaitOperation(ctx, opID, 5*time.Second)
}

func upsertKinstaDNSRecords(token, accountID, zone, domain string, records kinsta.DomainRecords) error {
	all := append([]kinsta.DNSRecord{}, records.Verification...)
	all = append(all, records.Pointing...)
	for _, record := range all {
		fqdn := record.RecordName()
		if !kinstaDNSRecordBelongsToDomain(fqdn, domain) {
			continue
		}
		name := dnsimpleRelativeName(fqdn, zone)
		recordType := strings.ToUpper(record.RecordTypeName())
		content := record.RecordContent()
		if fqdn == "" || recordType == "" || content == "" {
			continue
		}
		ttl := record.TTL
		if ttl <= 0 {
			ttl = 300
		}
		if err := upsertDNSRecordFn(token, accountID, zone, name, recordType, content, ttl); err != nil {
			return err
		}
	}
	return nil
}

func kinstaDNSRecordBelongsToDomain(recordName, domain string) bool {
	recordName = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(recordName), "."))
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if recordName == "" || domain == "" {
		return false
	}
	return recordName == domain || strings.HasSuffix(recordName, "."+domain)
}

func dnsimpleRelativeName(fqdn, zone string) string {
	fqdn = strings.TrimSuffix(strings.TrimSpace(fqdn), ".")
	zone = strings.TrimSuffix(strings.TrimSpace(zone), ".")
	if zone == "" || fqdn == zone {
		return ""
	}
	suffix := "." + zone
	if strings.HasSuffix(strings.ToLower(fqdn), strings.ToLower(suffix)) {
		return strings.TrimSuffix(fqdn, suffix)
	}
	return fqdn
}

func dnsimpleFQDNForRelativeName(name, zone string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	zone = strings.TrimSuffix(strings.TrimSpace(zone), ".")
	if name == "" {
		return zone
	}
	if zone == "" || strings.HasSuffix(strings.ToLower(name), "."+strings.ToLower(zone)) || strings.EqualFold(name, zone) {
		return name
	}
	return name + "." + zone
}

func dnsimpleTLSChallengeName(name string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	if name == "" {
		return "_acme-challenge"
	}
	return "_acme-challenge." + name
}

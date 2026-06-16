package cli

// Kinsta site add planning, provisioning, DNS, and cache writes.

import (
	"context"
	"errors"
	"fmt"
	"net"
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

var (
	kinstaDomainRecordsWaitTimeout  = 30 * time.Minute
	kinstaDomainRecordsWaitInterval = 30 * time.Second
	kinstaDomainPhantomWaitTimeout  = 2 * time.Minute
	kinstaEnvironmentWaitTimeout    = 30 * time.Minute
	kinstaEnvironmentWaitInterval   = 5 * time.Second
	kinstaOperationWaitTimeout      = 30 * time.Minute
	kinstaOperationWaitInterval     = 5 * time.Second
	kinstaLookupHost                = net.LookupHost
)

func buildKinstaSiteAddPlan(args siteAddArgs) (kinstaSiteAddPlan, error) {
	siteSlug, err := cleanSiteSlug(args.site)
	if err != nil {
		return kinstaSiteAddPlan{}, err
	}
	values, err := loadGlobalConfig()
	if err != nil {
		return kinstaSiteAddPlan{}, err
	}
	adminEmail := strings.TrimSpace(values["default_wp_email"])
	if adminEmail == "" {
		return kinstaSiteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Expected default_wp_email in %s. Set it with nf config set-default-wp-email <email>.", config.ConfigFile())}
	}
	adminUser := firstNonEmpty(values["default_wp_user"], "admin")
	baseDomain := strings.TrimSuffix(strings.TrimSpace(values["base_domain"]), ".")
	if baseDomain == "" {
		return kinstaSiteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Expected base_domain in %s. Set it with nf config set-base-domain <domain>.", config.ConfigFile())}
	}
	dnsAccountID := firstNonEmpty(values["dnsimple_account_id"], dnsimpleAccountIDValue())
	if dnsAccountID == "" {
		return kinstaSiteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Expected dnsimple_account_id in %s. Run nf provider check dnsimple.", config.ConfigFile())}
	}
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
	for _, env := range siteAddEnvNames(args.withStaging) {
		branch := "main"
		if env == "staging" {
			branch = "develop"
		}
		domain := kinstaSiteDomain(siteSlug, baseDomain, env)
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
	return upsertKinstaSiteRecords(plan, result, true)
}

func upsertKinstaSiteEnvRecords(plan kinstaSiteAddPlan, result kinstaProvisionResult) error {
	return upsertKinstaSiteRecords(plan, result, false)
}

func upsertKinstaSiteRecords(plan kinstaSiteAddPlan, result kinstaProvisionResult, replaceWholeSite bool) error {
	existing, err := state.LoadStateRecords("sites")
	if err != nil {
		return err
	}
	kept := make([]map[string]any, 0, len(existing)+len(result.Envs))
	for _, record := range existing {
		if replaceWholeSite && siteEnvMatchesSite(record, plan.SiteID) {
			continue
		}
		if !replaceWholeSite && siteEnvMatchesSite(record, plan.SiteID) {
			replaceEnv := false
			for _, env := range result.Envs {
				if normalizedRecordString(siteEnvName(record)) == normalizedRecordString(env.Env) {
					replaceEnv = true
					break
				}
			}
			if replaceEnv {
				continue
			}
		}
		kept = append(kept, record)
	}
	for _, env := range result.Envs {
		kept = append(kept, kinstaSiteAddRecord(plan, env, result))
	}
	return state.SaveStateRecords("sites", kept)
}

func printKinstaSiteAddPlan(plan kinstaSiteAddPlan, mode string) {
	printKinstaSiteAddPlanWithTitle(plan, mode, "Add site plan:")
}

func printKinstaSiteAddPlanWithTitle(plan kinstaSiteAddPlan, mode, title string) {
	fmt.Println(title)
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
	existing, err := state.LoadStateRecords("sites")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resume, err := kinstaSiteAddResumeState(existing, plan.SiteID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	willExecute := args.execute || (!args.dryRun && !args.nonInteractive)
	mode := "dry-run"
	if willExecute {
		mode = "execute"
	}
	if resume {
		printKinstaSiteAddPlanWithTitle(plan, mode, "Resume Kinsta site add plan:")
	} else {
		printKinstaSiteAddPlan(plan, mode)
	}
	if !willExecute {
		return 0
	}
	if !args.yes {
		message := fmt.Sprintf("Add site %q with live env on target %q?", plan.Site, plan.TargetName)
		if args.withStaging {
			message = fmt.Sprintf("Add site %q with live and staging envs on target %q?", plan.Site, plan.TargetName)
		}
		if resume {
			message = fmt.Sprintf("Resume site %q with live env on target %q?", plan.Site, plan.TargetName)
			if args.withStaging {
				message = fmt.Sprintf("Resume site %q with live and staging envs on target %q?", plan.Site, plan.TargetName)
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

func kinstaSiteAddResumeState(records []map[string]any, siteID string) (bool, error) {
	resume := false
	for _, record := range records {
		if !siteEnvMatchesSite(record, siteID) {
			continue
		}
		if !strings.EqualFold(firstRecordString(record, "provider"), "kinsta") {
			return false, ProjectError{Msg: fmt.Sprintf("Site %q already exists in local site cache.", siteID)}
		}
		resume = true
	}
	return resume, nil
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
	client := newKinstaClient(token)
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
	liveEnv, err := ensureKinstaLiveEnvironment(ctx, client, kinstaSite.ID, plan.PHPVersion)
	if err != nil {
		return kinstaProvisionResult{}, err
	}
	return provisionKinstaSelectedEnvs(ctx, client, dnsToken, plan, companyID, kinstaSite.ID, liveEnv)
}

func provisionKinstaStaging(plan kinstaSiteAddPlan) (kinstaProvisionResult, error) {
	token := envwizard.Value("KINSTA_API_KEY")
	if token == "" {
		return kinstaProvisionResult{}, fmt.Errorf("Expected KINSTA_API_KEY in the environment or %s.", config.EnvFile())
	}
	dnsToken := envwizard.Value("DNSIMPLE_TOKEN")
	if dnsToken == "" {
		return kinstaProvisionResult{}, fmt.Errorf("Expected DNSIMPLE_TOKEN in the environment or %s.", config.EnvFile())
	}
	kinstaSiteID := strings.TrimSpace(plan.KinstaSiteID)
	if kinstaSiteID == "" {
		return kinstaProvisionResult{}, fmt.Errorf("Selected Kinsta site %q has no Kinsta site_id. Run nf site refresh and try again.", plan.SiteID)
	}
	client := newKinstaClient(token)
	ctx := context.Background()
	liveEnv, err := ensureKinstaLiveEnvironment(ctx, client, kinstaSiteID, plan.PHPVersion)
	if err != nil {
		return kinstaProvisionResult{}, err
	}
	return provisionKinstaSelectedEnvs(ctx, client, dnsToken, plan, plan.CompanyID, kinstaSiteID, liveEnv)
}

func newKinstaClient(token string) *kinsta.Client {
	options := []kinsta.Option{}
	if graphqlURL := strings.TrimSpace(envwizard.Value("KINSTA_GRAPHQL_URL")); graphqlURL != "" {
		options = append(options, kinsta.WithGraphQLURL(graphqlURL))
	}
	return kinsta.NewClient(firstNonEmpty(envwizard.Value("KINSTA_BASE_URL"), "https://api.kinsta.com/v2"), token, options...)
}

func provisionKinstaSelectedEnvs(ctx context.Context, client *kinsta.Client, dnsToken string, plan kinstaSiteAddPlan, companyID, kinstaSiteID string, liveEnv kinsta.Environment) (kinstaProvisionResult, error) {
	result := kinstaProvisionResult{CompanyID: companyID, SiteID: kinstaSiteID, Envs: make([]kinstaSiteAddEnvPlan, 0, len(plan.Envs))}
	var stagingEnv kinsta.Environment
	for _, env := range plan.Envs {
		remoteEnv := liveEnv
		if env.Env == "staging" {
			if stagingEnv.ID == "" {
				var err error
				stagingEnv, err = ensureKinstaStagingEnvironment(ctx, client, companyID, kinstaSiteID, liveEnv, plan.PHPVersion)
				if err != nil {
					return kinstaProvisionResult{}, err
				}
			}
			remoteEnv = stagingEnv
		} else if env.Env != "live" {
			return kinstaProvisionResult{}, fmt.Errorf("Unsupported Kinsta env %q. Only live and staging are supported.", env.Env)
		}
		domain, err := ensureKinstaDomain(ctx, client, remoteEnv, env.Domain)
		if err != nil {
			return kinstaProvisionResult{}, err
		}
		dnsResult, err := ensureKinstaDomainDNSRecords(ctx, client, dnsToken, plan, env, remoteEnv, domain)
		if err != nil {
			return kinstaProvisionResult{}, err
		}
		if dnsResult.UsedFallback {
			return kinstaProvisionResult{}, kinstaDomainFallbackManualVerificationError(env.Domain)
		}
		if !domain.IsPrimary {
			fmt.Printf("Changing Kinsta primary domain to %s...\n", env.Domain)
			opID, err := client.ChangePrimaryDomain(ctx, remoteEnv.ID, domain.ID, false)
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

type kinstaDomainDNSResult struct {
	UsedFallback bool
}

func ensureKinstaDomainDNSRecords(ctx context.Context, client *kinsta.Client, dnsToken string, plan kinstaSiteAddPlan, env kinstaSiteAddEnvPlan, remoteEnv kinsta.Environment, domain kinsta.Domain) (kinstaDomainDNSResult, error) {
	records, err := client.DomainRecords(ctx, domain.ID)
	if err != nil {
		return kinstaDomainDNSResult{}, err
	}
	if err := upsertKinstaDNSRecords(dnsToken, plan.DNSAccountID, plan.DNSZone, env.Domain, records); err != nil {
		return kinstaDomainDNSResult{}, err
	}
	if kinstaDomainRecordsHavePointing(records, env.Domain) {
		return kinstaDomainDNSResult{}, nil
	}
	if domain.IsPrimary && !kinstaDomainRecordsHaveAny(records) {
		return kinstaDomainDNSResult{}, nil
	}
	syncedRecords := kinstaDomainRecordKeySet(records)
	validationRecords := kinsta.DomainRecords{}
	fmt.Println("Waiting for Kinsta to detect domain verification DNS...")
	if _, err := waitKinstaDomainVerificationRecords(ctx, client, domain.ID, env.Domain, kinstaDomainPhantomWaitTimeout, func(validation kinsta.DomainVerificationValidation) error {
		validationRecords = kinstaDomainVerificationDNSRecords(validation)
		unsynced := kinstaDomainRecordsUnsynced(validationRecords, syncedRecords)
		if !kinstaDomainRecordsHaveAny(unsynced) {
			return nil
		}
		if err := upsertKinstaDNSRecords(dnsToken, plan.DNSAccountID, plan.DNSZone, env.Domain, unsynced); err != nil {
			return err
		}
		kinstaAddDomainRecordKeys(syncedRecords, unsynced)
		return nil
	}); err != nil {
		fallback, fallbackErr := kinstaSiteAddFallbackDNSRecords(ctx, client, plan, env, remoteEnv, kinstaMergeDomainRecords(records, validationRecords))
		if fallbackErr != nil {
			return kinstaDomainDNSResult{}, err
		}
		fmt.Printf("Kinsta has not detected verification DNS yet; using generated Kinsta DNS fallback %s -> %s.\n", env.Domain, fallback.Pointing[0].RecordContent())
		if err := upsertKinstaDNSRecords(dnsToken, plan.DNSAccountID, plan.DNSZone, env.Domain, fallback); err != nil {
			return kinstaDomainDNSResult{}, err
		}
		return kinstaDomainDNSResult{UsedFallback: true}, nil
	}
	fmt.Println("Kinsta detected verification records.")
	actionID, err := client.ConfirmCloudflareVerification(ctx, remoteEnv.ID, domain.ID)
	if err != nil {
		return kinstaDomainDNSResult{}, err
	}
	if actionID != 0 {
		action, err := client.GraphQLAction(ctx, actionID)
		if err != nil {
			return kinstaDomainDNSResult{}, err
		}
		phantomAction := false
		if !action.Found {
			fmt.Printf("Kinsta accepted domain verification for %s; waiting for updated verification records.\n", env.Domain)
			phantomAction = true
		}
		if action.Error != "" {
			return kinstaDomainDNSResult{}, fmt.Errorf("Kinsta domain verification for %s failed: %s", env.Domain, action.Error)
		}
		if phantomAction {
			return waitKinstaDomainDNSAfterPhantomVerification(ctx, client, dnsToken, plan, env, remoteEnv, domain, syncedRecords)
		}
	}
	fmt.Println("Started Kinsta domain verification.")
	fmt.Println("Waiting for Kinsta domain verification...")
	records, err = waitKinstaDomainPointingRecords(ctx, client, domain.ID, env.Domain, 0, func(records kinsta.DomainRecords) error {
		unsynced := kinstaDomainRecordsUnsynced(records, syncedRecords)
		if !kinstaDomainRecordsHaveAny(unsynced) {
			return nil
		}
		if err := upsertKinstaDNSRecords(dnsToken, plan.DNSAccountID, plan.DNSZone, env.Domain, unsynced); err != nil {
			return err
		}
		kinstaAddDomainRecordKeys(syncedRecords, unsynced)
		return nil
	})
	if err != nil {
		return kinstaDomainDNSResult{}, err
	}
	fmt.Println("Kinsta returned pointing records.")
	return kinstaDomainDNSResult{}, nil
}

func waitKinstaDomainDNSAfterPhantomVerification(ctx context.Context, client *kinsta.Client, dnsToken string, plan kinstaSiteAddPlan, env kinstaSiteAddEnvPlan, remoteEnv kinsta.Environment, domain kinsta.Domain, syncedRecords map[string]struct{}) (kinstaDomainDNSResult, error) {
	fmt.Println("Started Kinsta domain verification.")
	fmt.Println("Waiting briefly for Kinsta domain verification...")
	records, err := waitKinstaDomainPointingRecords(ctx, client, domain.ID, env.Domain, kinstaDomainPhantomWaitTimeout, func(records kinsta.DomainRecords) error {
		unsynced := kinstaDomainRecordsUnsynced(records, syncedRecords)
		if !kinstaDomainRecordsHaveAny(unsynced) {
			return nil
		}
		if err := upsertKinstaDNSRecords(dnsToken, plan.DNSAccountID, plan.DNSZone, env.Domain, unsynced); err != nil {
			return err
		}
		kinstaAddDomainRecordKeys(syncedRecords, unsynced)
		return nil
	})
	if err != nil {
		fallback, fallbackErr := kinstaSiteAddFallbackDNSRecords(ctx, client, plan, env, remoteEnv, records)
		if fallbackErr != nil {
			return kinstaDomainDNSResult{}, fmt.Errorf("Kinsta accepted domain verification for %s but did not expose a runnable verification action to the API token and did not return pointing records. Verification records were written; verify the domain in MyKinsta, then rerun site add: %w", env.Domain, err)
		}
		fmt.Printf("Kinsta did not return pointing records; using generated Kinsta DNS fallback %s -> %s.\n", env.Domain, fallback.Pointing[0].RecordContent())
		if err := upsertKinstaDNSRecords(dnsToken, plan.DNSAccountID, plan.DNSZone, env.Domain, fallback); err != nil {
			return kinstaDomainDNSResult{}, err
		}
		return kinstaDomainDNSResult{UsedFallback: true}, nil
	}
	fmt.Println("Kinsta returned pointing records.")
	return kinstaDomainDNSResult{}, nil
}

func kinstaDomainFallbackManualVerificationError(domain string) error {
	return fmt.Errorf("Kinsta did not return authoritative pointing records for %s. DNS records were written using the generated Kinsta domain fallback, but Kinsta has not marked the custom domain live or issued HTTPS yet. Open MyKinsta, click Verify domain for %s, then rerun site add/staging add; the command is resumable", domain, domain)
}

func kinstaSiteAddFallbackDNSRecords(ctx context.Context, client *kinsta.Client, plan kinstaSiteAddPlan, env kinstaSiteAddEnvPlan, remoteEnv kinsta.Environment, records kinsta.DomainRecords) (kinsta.DomainRecords, error) {
	fallback, err := kinstaSiteAddGeneratedDNSFallback(ctx, client, plan, env, remoteEnv)
	if err != nil {
		return kinsta.DomainRecords{}, err
	}
	if !kinstaDomainRecordsHaveTLSChallenge(records, env.Domain) {
		fallback.Verification = append(fallback.Verification, kinstaSiteAddTLSChallengeFallback(env.Domain))
	}
	return fallback, nil
}

func kinstaDomainVerificationDNSRecords(validation kinsta.DomainVerificationValidation) kinsta.DomainRecords {
	records := kinsta.DomainRecords{Verification: make([]kinsta.DNSRecord, 0, len(validation.Records))}
	for _, record := range validation.Records {
		name := strings.TrimSpace(record.Name)
		recordType := strings.TrimSpace(record.Type)
		content := strings.TrimSpace(record.Value)
		if name == "" || recordType == "" || content == "" {
			continue
		}
		records.Verification = append(records.Verification, kinsta.DNSRecord{Name: name, Type: recordType, Content: content, TTL: 300})
	}
	return records
}

func kinstaMergeDomainRecords(records ...kinsta.DomainRecords) kinsta.DomainRecords {
	merged := kinsta.DomainRecords{}
	seen := map[string]struct{}{}
	for _, records := range records {
		for _, record := range records.Verification {
			key := kinstaDomainRecordKey(record)
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			merged.Verification = append(merged.Verification, record)
		}
		for _, record := range records.Pointing {
			key := kinstaDomainRecordKey(record)
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			merged.Pointing = append(merged.Pointing, record)
		}
	}
	return merged
}

func kinstaSiteAddGeneratedDNSFallback(ctx context.Context, client *kinsta.Client, plan kinstaSiteAddPlan, env kinstaSiteAddEnvPlan, remoteEnv kinsta.Environment) (kinsta.DomainRecords, error) {
	if !kinstaSiteAddInternalDomain(env.Domain, plan.BaseDomain) {
		return kinsta.DomainRecords{}, fmt.Errorf("%s is not an nf-internal Kinsta site-add domain", env.Domain)
	}
	target := kinstaGeneratedDomainForEnv(remoteEnv, env.Domain)
	if target == "" && remoteEnv.ID != "" {
		domains, err := client.ListDomains(ctx, remoteEnv.ID)
		if err != nil {
			return kinsta.DomainRecords{}, err
		}
		target = kinstaGeneratedDomainForDomains(domains, env.Domain)
	}
	if target == "" {
		return kinsta.DomainRecords{}, fmt.Errorf("Kinsta did not return a generated *.kinsta.cloud domain for environment %s", remoteEnv.ID)
	}
	content, recordType := kinstaGeneratedPointingRecord(target)
	return kinsta.DomainRecords{Pointing: []kinsta.DNSRecord{{Name: env.Domain, Type: recordType, Content: content, TTL: 300}}}, nil
}

func kinstaGeneratedPointingRecord(target string) (string, string) {
	for _, host := range []string{target, strings.TrimSuffix(target, ".")} {
		if host == "" {
			continue
		}
		addresses, err := kinstaLookupHost(host)
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip := net.ParseIP(address)
			if ip != nil && ip.To4() != nil {
				return ip.String(), "A"
			}
		}
	}
	return target, "CNAME"
}

func kinstaSiteAddTLSChallengeFallback(domain string) kinsta.DNSRecord {
	domain = normalizeDomainName(domain)
	return kinsta.DNSRecord{Name: "_acme-challenge." + domain, Type: "CNAME", Content: domain + ".kinstavalidation.app", TTL: 300}
}

func kinstaDomainRecordsHaveTLSChallenge(records kinsta.DomainRecords, domain string) bool {
	for _, record := range records.Verification {
		fqdn := normalizeDomainName(record.RecordName())
		if !kinstaDNSRecordBelongsToDomain(fqdn, domain) {
			continue
		}
		if strings.HasPrefix(fqdn, "_acme-challenge.") && strings.EqualFold(record.RecordTypeName(), "CNAME") && record.RecordContent() != "" {
			return true
		}
	}
	return false
}

func kinstaSiteAddInternalDomain(domain, baseDomain string) bool {
	domain = normalizeDomainName(domain)
	baseDomain = normalizeDomainName(baseDomain)
	return domain != "" && baseDomain != "" && strings.HasSuffix(domain, ".kinsta."+baseDomain)
}

func kinstaGeneratedDomainForEnv(env kinsta.Environment, customDomain string) string {
	if target := kinstaGeneratedDomainForDomains([]kinsta.Domain{env.PrimaryDomain}, customDomain); target != "" {
		return target
	}
	return kinstaGeneratedDomainForDomains(env.Domains, customDomain)
}

func kinstaGeneratedDomainForDomains(domains []kinsta.Domain, customDomain string) string {
	customDomain = normalizeDomainName(customDomain)
	for _, domain := range domains {
		name := normalizeDomainName(domainName(domain))
		if name == "" || name == customDomain || strings.HasPrefix(name, "*.") {
			continue
		}
		if strings.HasSuffix(name, ".kinsta.cloud") {
			return name
		}
	}
	return ""
}

func waitKinstaDomainVerificationRecords(parent context.Context, client *kinsta.Client, domainID, domainName string, timeout time.Duration, onValidation func(kinsta.DomainVerificationValidation) error) (kinsta.DomainVerificationValidation, error) {
	if timeout <= 0 {
		timeout = kinstaDomainRecordsWaitTimeout
	}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	interval := kinstaDomainRecordsWaitInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var last kinsta.DomainVerificationValidation
	for {
		validation, err := client.ValidateDomainVerification(ctx, domainID)
		if err != nil {
			if ctx.Err() != nil {
				return last, kinstaDomainVerificationRecordsTimeoutError(domainName)
			}
			return kinsta.DomainVerificationValidation{}, err
		}
		last = validation
		if onValidation != nil {
			if err := onValidation(validation); err != nil {
				return kinsta.DomainVerificationValidation{}, err
			}
		}
		if validation.Valid {
			return validation, nil
		}
		select {
		case <-ctx.Done():
			return last, kinstaDomainVerificationRecordsTimeoutError(domainName)
		case <-ticker.C:
		}
	}
}

func kinstaDomainVerificationRecordsTimeoutError(domainName string) error {
	return fmt.Errorf("timed out waiting for Kinsta to detect verification DNS for %s. Verification records were written when available; rerun site add after DNS propagates", domainName)
}

func waitKinstaDomainPointingRecords(parent context.Context, client *kinsta.Client, domainID, domainName string, timeout time.Duration, onRecords func(kinsta.DomainRecords) error) (kinsta.DomainRecords, error) {
	if timeout <= 0 {
		timeout = kinstaDomainRecordsWaitTimeout
	}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	interval := kinstaDomainRecordsWaitInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var last kinsta.DomainRecords
	var lastErr error
	for {
		records, err := client.DomainRecords(ctx, domainID)
		if err != nil {
			if ctx.Err() != nil {
				return last, kinstaDomainPointingRecordsTimeoutError(domainName, lastErr)
			}
			if kinsta.IsTemporary(err) {
				lastErr = err
				select {
				case <-ctx.Done():
					return last, kinstaDomainPointingRecordsTimeoutError(domainName, lastErr)
				case <-ticker.C:
				}
				continue
			}
			return kinsta.DomainRecords{}, err
		}
		lastErr = nil
		last = records
		if onRecords != nil {
			if err := onRecords(records); err != nil {
				return kinsta.DomainRecords{}, err
			}
		}
		if kinstaDomainRecordsHavePointing(records, domainName) {
			return records, nil
		}
		select {
		case <-ctx.Done():
			return last, kinstaDomainPointingRecordsTimeoutError(domainName, lastErr)
		case <-ticker.C:
		}
	}
}

func kinstaDomainPointingRecordsTimeoutError(domainName string, lastErr error) error {
	msg := fmt.Sprintf("timed out waiting for Kinsta pointing DNS records for %s. Verification records were written when available; rerun site add after Kinsta verifies the domain", domainName)
	if lastErr != nil {
		return fmt.Errorf("%s; last temporary Kinsta error: %w", msg, lastErr)
	}
	return errors.New(msg)
}

func kinstaDomainRecordsHavePointing(records kinsta.DomainRecords, domain string) bool {
	for _, record := range records.Pointing {
		fqdn := record.RecordName()
		if !kinstaDNSRecordBelongsToDomain(fqdn, domain) {
			continue
		}
		recordType := strings.ToUpper(record.RecordTypeName())
		if recordType != "A" && recordType != "AAAA" && recordType != "CNAME" {
			continue
		}
		if record.RecordContent() != "" {
			return true
		}
	}
	return false
}

func kinstaDomainRecordsHaveAny(records kinsta.DomainRecords) bool {
	return len(records.Verification) > 0 || len(records.Pointing) > 0
}

func kinstaDomainRecordKeySet(records kinsta.DomainRecords) map[string]struct{} {
	keys := map[string]struct{}{}
	kinstaAddDomainRecordKeys(keys, records)
	return keys
}

func kinstaAddDomainRecordKeys(keys map[string]struct{}, records kinsta.DomainRecords) {
	for _, record := range append(append([]kinsta.DNSRecord{}, records.Verification...), records.Pointing...) {
		keys[kinstaDomainRecordKey(record)] = struct{}{}
	}
}

func kinstaDomainRecordsUnsynced(records kinsta.DomainRecords, synced map[string]struct{}) kinsta.DomainRecords {
	var out kinsta.DomainRecords
	for _, record := range records.Verification {
		if _, ok := synced[kinstaDomainRecordKey(record)]; !ok {
			out.Verification = append(out.Verification, record)
		}
	}
	for _, record := range records.Pointing {
		if _, ok := synced[kinstaDomainRecordKey(record)]; !ok {
			out.Pointing = append(out.Pointing, record)
		}
	}
	return out
}

func kinstaDomainRecordKey(record kinsta.DNSRecord) string {
	return strings.Join([]string{strings.ToUpper(strings.TrimSpace(record.RecordTypeName())), strings.ToLower(strings.TrimSuffix(strings.TrimSpace(record.RecordName()), ".")), strings.TrimSpace(record.RecordContent()), fmt.Sprint(record.TTL)}, "\x00")
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

func ensureKinstaLiveEnvironment(ctx context.Context, client *kinsta.Client, siteID, phpVersion string) (kinsta.Environment, error) {
	envs, err := waitKinstaEnvironments(ctx, client, siteID, func(envs []kinsta.Environment) (kinsta.Environment, bool) {
		return findKinstaLiveEnvironment(envs)
	})
	if err != nil {
		return kinsta.Environment{}, err
	}
	live, ok := findKinstaLiveEnvironment(envs)
	if !ok {
		return kinsta.Environment{}, fmt.Errorf("Kinsta site %s is missing live environment; found: %s", siteID, kinstaEnvironmentSummary(envs))
	}
	if err := ensureKinstaEnvironmentPHP(ctx, client, live, phpVersion); err != nil {
		return kinsta.Environment{}, err
	}
	envs, err = waitKinstaEnvironments(ctx, client, siteID, func(envs []kinsta.Environment) (kinsta.Environment, bool) {
		return findKinstaLiveEnvironment(envs)
	})
	if err != nil {
		return kinsta.Environment{}, err
	}
	live, ok = findKinstaLiveEnvironment(envs)
	if !ok {
		return kinsta.Environment{}, fmt.Errorf("Kinsta site %s is missing live environment; found: %s", siteID, kinstaEnvironmentSummary(envs))
	}
	return live, nil
}

func ensureKinstaStagingEnvironment(ctx context.Context, client *kinsta.Client, companyID, siteID string, live kinsta.Environment, phpVersion string) (kinsta.Environment, error) {
	envs, err := waitKinstaEnvironments(ctx, client, siteID, func(envs []kinsta.Environment) (kinsta.Environment, bool) {
		if staging, ok := findKinstaStagingEnvironment(envs, live); ok {
			return staging, !staging.IsBlocked
		}
		return findKinstaLiveEnvironment(envs)
	})
	if err != nil {
		return kinsta.Environment{}, err
	}
	staging, ok := findKinstaStagingEnvironment(envs, live)
	if ok && !staging.IsBlocked {
		if err := ensureKinstaEnvironmentPHP(ctx, client, staging, phpVersion); err != nil {
			return kinsta.Environment{}, err
		}
		return staging, nil
	}
	ctx, cancel := context.WithTimeout(ctx, kinstaWaitTimeout(kinstaEnvironmentWaitTimeout, 30*time.Minute))
	defer cancel()
	for {
		lastActivityID := int64(0)
		if companyID != "" {
			logs, err := client.ListActivityLogs(ctx, companyID, siteID, "siteActions", 10)
			if err != nil {
				return kinsta.Environment{}, err
			}
			lastActivityID = latestKinstaActivityID(logs)
		}
		fmt.Println("Creating Kinsta staging environment...")
		if _, err := client.CloneEnvironment(ctx, siteID, kinsta.CloneEnvironmentRequest{DisplayName: "Staging", IsPremium: false, SourceEnvID: live.ID}); err != nil {
			return kinsta.Environment{}, err
		}
		staging, err := waitKinstaStagingClone(ctx, client, companyID, siteID, live, lastActivityID)
		if err == nil {
			if err := ensureKinstaEnvironmentPHP(ctx, client, staging, phpVersion); err != nil {
				return kinsta.Environment{}, err
			}
			return staging, nil
		}
		if !isKinstaEnvironmentBlockedError(err) {
			return kinsta.Environment{}, err
		}
		fmt.Println("Kinsta reported the live environment is busy; retrying staging creation...")
		select {
		case <-ctx.Done():
			return kinsta.Environment{}, fmt.Errorf("timed out waiting to create Kinsta staging environment for site %s: %w", siteID, err)
		case <-time.After(kinstaWaitInterval(kinstaEnvironmentWaitInterval, 5*time.Second)):
		}
	}
}

func waitKinstaStagingClone(ctx context.Context, client *kinsta.Client, companyID, siteID string, live kinsta.Environment, afterActivityID int64) (kinsta.Environment, error) {
	ticker := time.NewTicker(kinstaWaitInterval(kinstaEnvironmentWaitInterval, 5*time.Second))
	defer ticker.Stop()
	var last []kinsta.Environment
	var lastErr error
	for {
		envs, err := client.ListEnvironments(ctx, siteID)
		if err != nil {
			if !kinsta.IsTemporary(err) {
				return kinsta.Environment{}, err
			}
			lastErr = err
		} else {
			lastErr = nil
			last = envs
			if staging, ok := findKinstaStagingEnvironment(envs, live); ok && !staging.IsBlocked {
				return staging, nil
			}
		}
		if companyID != "" {
			logs, err := client.ListActivityLogs(ctx, companyID, siteID, "siteActions", 10)
			if err != nil {
				if !kinsta.IsTemporary(err) {
					return kinsta.Environment{}, err
				}
				lastErr = err
			} else if activity, ok := latestKinstaStagingAddActivity(logs, afterActivityID); ok && activity.Done && activity.Failed {
				return kinsta.Environment{}, kinstaStagingActivityError{activity: activity}
			}
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return kinsta.Environment{}, fmt.Errorf("timed out waiting for Kinsta staging environment for site %s after temporary Kinsta error: %w", siteID, lastErr)
			}
			return kinsta.Environment{}, fmt.Errorf("timed out waiting for Kinsta staging environment for site %s; found: %s", siteID, kinstaEnvironmentSummary(last))
		case <-ticker.C:
		}
	}
}

type kinstaStagingActivityError struct {
	activity kinsta.ActivityLog
}

func (e kinstaStagingActivityError) Error() string {
	message := strings.TrimSpace(e.activity.PublicError)
	if message == "" && len(e.activity.Descriptions) > 0 {
		message = strings.Join(e.activity.Descriptions, "; ")
	}
	if message == "" {
		message = "unknown Kinsta activity failure"
	}
	return "Kinsta staging environment creation failed: " + message
}

func isKinstaEnvironmentBlockedError(err error) bool {
	var activityErr kinstaStagingActivityError
	if !errors.As(err, &activityErr) {
		return false
	}
	message := strings.ToLower(activityErr.activity.PublicError)
	return strings.Contains(message, "blocked by another process")
}

func latestKinstaActivityID(logs []kinsta.ActivityLog) int64 {
	var latest int64
	for _, log := range logs {
		if log.ID > latest {
			latest = log.ID
		}
	}
	return latest
}

func latestKinstaStagingAddActivity(logs []kinsta.ActivityLog, afterID int64) (kinsta.ActivityLog, bool) {
	var latest kinsta.ActivityLog
	for _, log := range logs {
		if log.ID <= afterID || !strings.EqualFold(strings.TrimSpace(log.Type), "addEnvironment") {
			continue
		}
		if !kinstaActivityMentionsStaging(log) {
			continue
		}
		if latest.ID == 0 || log.ID > latest.ID {
			latest = log
		}
	}
	return latest, latest.ID != 0
}

func kinstaActivityMentionsStaging(log kinsta.ActivityLog) bool {
	for _, description := range log.Descriptions {
		description = strings.ToLower(description)
		if strings.Contains(description, "staging") {
			return true
		}
	}
	return false
}

func kinstaWaitTimeout(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func kinstaWaitInterval(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
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
	ctx, cancel := context.WithTimeout(ctx, kinstaWaitTimeout(kinstaEnvironmentWaitTimeout, 30*time.Minute))
	defer cancel()
	ticker := time.NewTicker(kinstaWaitInterval(kinstaEnvironmentWaitInterval, 5*time.Second))
	defer ticker.Stop()
	var last []kinsta.Environment
	var lastErr error
	for {
		envs, err := client.ListEnvironments(ctx, siteID)
		if err != nil {
			if !kinsta.IsTemporary(err) {
				return nil, err
			}
			lastErr = err
		} else {
			lastErr = nil
			last = envs
			if _, ok := ready(envs); ok {
				return envs, nil
			}
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return last, fmt.Errorf("timed out waiting for Kinsta environments for site %s after temporary Kinsta error: %w", siteID, lastErr)
			}
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

func ensureKinstaDomain(ctx context.Context, client *kinsta.Client, env kinsta.Environment, domainName string) (kinsta.Domain, error) {
	envID := env.ID
	domains, err := client.ListDomains(ctx, envID)
	if err != nil {
		return kinsta.Domain{}, err
	}
	if domain, ok := kinsta.FindDomain(domains, domainName); ok {
		return markKinstaDomainPrimary(domain, env), nil
	}
	fmt.Printf("Adding Kinsta domain %s...\n", domainName)
	opID, err := client.AddDomain(ctx, envID, kinsta.AddDomainRequest{DomainName: domainName, IsWildcardless: true, AddWithWWWSubdomain: false, SetupType: "quick"})
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
		return markKinstaDomainPrimary(domain, env), nil
	}
	return kinsta.Domain{}, fmt.Errorf("Kinsta domain %q was added but was not found in domain list", domainName)
}

func markKinstaDomainPrimary(domain kinsta.Domain, env kinsta.Environment) kinsta.Domain {
	primaryID := strings.TrimSpace(env.PrimaryDomain.ID)
	primaryName := strings.TrimSpace(domainName(env.PrimaryDomain))
	name := strings.TrimSpace(domainName(domain))
	if (primaryID != "" && strings.TrimSpace(domain.ID) == primaryID) || (primaryName != "" && strings.EqualFold(name, primaryName)) {
		domain.IsPrimary = true
	}
	return domain
}

func waitKinstaOperation(parent context.Context, client *kinsta.Client, opID string) error {
	ctx, cancel := context.WithTimeout(parent, kinstaWaitTimeout(kinstaOperationWaitTimeout, 30*time.Minute))
	defer cancel()
	return client.WaitOperation(ctx, opID, kinstaWaitInterval(kinstaOperationWaitInterval, 5*time.Second))
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
		if err := deleteKinstaDNSRecordConflicts(token, accountID, zone, name, recordType, fqdn, domain); err != nil {
			return err
		}
		if err := upsertDNSRecordFn(token, accountID, zone, name, recordType, content, ttl); err != nil {
			return err
		}
	}
	return nil
}

func deleteKinstaDNSRecordConflicts(token, accountID, zone, name, recordType, fqdn, domain string) error {
	if !strings.EqualFold(normalizeDomainName(fqdn), normalizeDomainName(domain)) {
		return nil
	}
	switch strings.ToUpper(strings.TrimSpace(recordType)) {
	case "CNAME":
		for _, conflictType := range []string{"A", "AAAA"} {
			if err := deleteDNSTypedRecordFn(token, accountID, zone, name, conflictType); err != nil {
				return err
			}
		}
	case "A", "AAAA":
		if err := deleteDNSTypedRecordFn(token, accountID, zone, name, "CNAME"); err != nil {
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

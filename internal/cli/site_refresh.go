package cli

// Remote site discovery and site-cache refresh.
//
// Refresh reads known targets from provider cache, discovers site records where
// implemented, and prunes cached sites whose target no longer exists.

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
	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/state"
)

func cmdShowServer(needle string) int {
	bundle, err := state.LoadStateBundle()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	record := state.MatchingRecord(bundle.Servers, needle)
	if record == nil {
		fmt.Fprintf(os.Stderr, "No server matched %q.\n", needle)
		return 1
	}
	if err := validateServerRecord(record); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	data, _ := json.MarshalIndent(record, "", "  ")
	fmt.Println(string(data))
	return 0
}

func cmdShowTarget(needle string, jsonOutput bool) int {
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
	if err := validateTargetRecord(record); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !jsonOutput {
		record = targetWithRemoteDBMetadata(record)
		printTargetDetails(record)
		return 0
	}
	data, _ := json.MarshalIndent(record, "", "  ")
	fmt.Println(string(data))
	return 0
}

func printTargetDetails(record map[string]any) {
	name := firstRecordString(record, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id")
	lines := []string{}
	if name != "" {
		lines = append(lines, name, strings.Repeat("─", len(name)))
	}
	lines = append(lines, detailRowLines([]detailRow{
		{label: "Provider", value: recordValueString(record["provider"])},
		{label: "Hostname", value: firstRecordString(record, "hostname", "host", "public_ipv4", "ipv4", "ip")},
		{label: "ID", value: firstRecordString(record, "id", "provider_id", "linode_id")},
		{label: "Status", value: targetLiveStatus(record)},
		{label: "Cached status", value: recordValueString(record["status"])},
		{label: "Region", value: firstRecordString(record, "region")},
		{label: "Type", value: firstRecordString(record, "type", "linode_type")},
		{label: "Image", value: firstRecordString(record, "image")},
	}, 0)...)
	dbURL, dbUser, dbPassword := targetDBLogin(record)
	accessRows := []detailRow{{label: "SSH", value: targetSSHCommand(record)}, {label: "Database", value: dbURL}}
	if hasDetailRows(accessRows) {
		lines = append(lines, "", "Access")
		lines = append(lines, detailRowLines(accessRows, 2)...)
		if dbURL != "" {
			lines = append(lines, detailRowLines([]detailRow{{label: "- User", value: dbUser}, {label: "- Pass", value: dbPassword}}, 3)...)
		}
	}
	fmt.Println(strings.Join(lines, "\n"))
}

func targetWithRemoteDBMetadata(record map[string]any) map[string]any {
	if strings.ToLower(strings.TrimSpace(recordValueString(record["provider"]))) != "linode" || targetDBURL(record) != "" {
		return record
	}
	remote, err := readLinodeTargetFile(record)
	if err != nil {
		return record
	}
	db := targetDBMetadata(remote)
	if len(db) == 0 {
		return record
	}
	hydrated := cloneRecord(record)
	hydrated["db"] = db
	for _, key := range []string{"hostname", "host"} {
		if recordValueString(hydrated[key]) == "" && recordValueString(remote[key]) != "" {
			hydrated[key] = remote[key]
		}
	}
	return hydrated
}

func targetDBLogin(record map[string]any) (string, string, string) {
	url := targetDBURL(record)
	user := targetDBUser(record)
	identity := firstNonEmpty(
		mapStringAtPath(record, "db", "auth", "password", "identity"),
		mapStringAtPath(record, "adminer", "auth", "password", "identity"),
		firstRecordString(record, "hostname", "host"),
	)
	if url == "" || user == "" || identity == "" {
		return url, user, ""
	}
	salt, err := passwords.SecretSalt()
	if err != nil {
		return url, user, ""
	}
	purpose := firstNonEmpty(mapStringAtPath(record, "db", "auth", "password", "purpose"), mapStringAtPath(record, "adminer", "auth", "password", "purpose"), passwordDeriveScopeDBAdmin)
	return url, user, passwords.DerivePassword(identity, purpose, salt)
}

func targetSSHCommand(record map[string]any) string {
	host := serverSSHHost(record)
	if host == "" {
		return ""
	}
	destination := host
	if user := serverSSHUser(record); user != "" {
		destination = user + "@" + destination
	}
	if port := firstNonEmpty(mapStringAtPath(record, "ssh", "port"), firstRecordString(record, "ssh_port")); port != "" && port != "22" {
		return "ssh " + destination + " -p " + port
	}
	return "ssh " + destination
}

func cmdSiteRefresh() int {
	if err := os.MkdirAll(config.StateDir(), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	targets, err := cachedTargets()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Site refresh discovers sites from cached targets.")
	fmt.Printf("Sites cache: %s\n", state.StatePath("sites"))
	fmt.Printf("Targets cache: %s\n", state.StatePath("providers"))
	if len(targets) == 0 {
		fmt.Println("No cached targets found. Run nf provider check <provider> to refresh target metadata.")
		return 0
	}
	fmt.Printf("Targets: %d\n", len(targets))
	for _, target := range targets {
		fmt.Printf("  %s (%s)\n", firstRecordString(target, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id"), recordValueString(target["provider"]))
	}
	result, err := refreshRemoteTargetSites(targets)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if result.Skipped > 0 {
		fmt.Printf("Skipped targets: %d\n", result.Skipped)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
	}
	if result.Refreshed == 0 {
		if result.Pruned == 0 {
			fmt.Println("No remote targets were refreshed; no site cache was changed.")
		}
		if len(result.Warnings) > 0 {
			return 1
		}
		if result.Pruned == 0 {
			return 0
		}
	} else {
		fmt.Printf("Refreshed targets: %d\n", result.Refreshed)
		fmt.Printf("Discovered remote site envs: %d\n", result.Discovered)
	}
	if result.Pruned > 0 {
		fmt.Printf("Pruned stale site envs: %d\n", result.Pruned)
	}
	fmt.Printf("Saved site cache: %s\n", state.StatePath("sites"))
	if len(result.Warnings) > 0 {
		return 1
	}
	return 0
}

type siteRefreshResult struct {
	Refreshed  int
	Skipped    int
	Discovered int
	Pruned     int
	Warnings   []string
}

func refreshRemoteTargetSites(targets []map[string]any) (siteRefreshResult, error) {
	result := siteRefreshResult{}
	currentTargets := currentTargetNames(targets)
	refreshedTargets := map[string]bool{}
	discovered := []map[string]any{}
	for _, target := range targets {
		provider := strings.ToLower(strings.TrimSpace(recordValueString(target["provider"])))
		if provider != "linode" && provider != "kinsta" {
			result.Skipped++
			continue
		}
		targetName := firstRecordString(target, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id")
		if provider == "kinsta" {
			remote, err := discoverKinstaTargetSites(target)
			if err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("target %q: %v", targetName, err))
				continue
			}
			result.Refreshed++
			refreshedTargets[normalizedRecordString(targetName)] = true
			discovered = append(discovered, remote...)
			continue
		}
		sshHost := serverSSHHost(target)
		if sshHost == "" {
			result.Warnings = append(result.Warnings, fmt.Sprintf("target %q has no SSH host", targetName))
			continue
		}
		remote, err := discoverLinodeTargetSites(target)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("target %q: %v", targetName, err))
			continue
		}
		result.Refreshed++
		refreshedTargets[normalizedRecordString(targetName)] = true
		for _, record := range remote {
			normalizeRemoteSiteRecord(record, target)
			discovered = append(discovered, record)
		}
	}
	existing, err := state.LoadStateRecords("sites")
	if err != nil {
		return result, err
	}
	merged := make([]map[string]any, 0, len(existing)+len(discovered))
	for _, record := range existing {
		if staleSiteTarget(record, currentTargets) {
			result.Pruned++
			continue
		}
		if refreshedTargets[normalizedRecordString(siteProviderTarget(record))] {
			continue
		}
		if refreshedTargets[normalizedRecordString(siteServerReference(record))] {
			continue
		}
		merged = append(merged, record)
	}
	if result.Refreshed == 0 && result.Pruned == 0 {
		return result, nil
	}
	merged = append(merged, discovered...)
	if err := state.SaveStateRecords("sites", merged); err != nil {
		return result, err
	}
	result.Discovered = len(discovered)
	return result, nil
}

func currentTargetNames(targets []map[string]any) map[string]bool {
	names := map[string]bool{}
	for _, target := range targets {
		for _, key := range []string{"_state_key", "target_name", "target", "name", "slug", "hostname", "host", "label", "id"} {
			if value := normalizedRecordString(recordValueString(target[key])); value != "" {
				names[value] = true
			}
		}
	}
	return names
}

func staleSiteTarget(site map[string]any, currentTargets map[string]bool) bool {
	provider := strings.ToLower(strings.TrimSpace(recordValueString(site["provider"])))
	if provider != "linode" {
		return false
	}
	target := normalizedRecordString(siteProviderTarget(site))
	if target == "" {
		return false
	}
	return !currentTargets[target]
}

func discoverKinstaTargetSites(target map[string]any) ([]map[string]any, error) {
	token := envwizard.Value("KINSTA_API_KEY")
	if token == "" {
		return nil, fmt.Errorf("Expected KINSTA_API_KEY in the environment or %s.", config.EnvFile())
	}
	targetName := firstNonEmpty(firstRecordString(target, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id"), "kinsta")
	companyID := firstNonEmpty(firstRecordString(target, "company_id", "company"), mapStringAtPath(target, "kinsta", "company_id"))
	client := kinsta.NewClient(os.Getenv("KINSTA_BASE_URL"), token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if companyID == "" {
		validate, err := client.Validate(ctx)
		if err != nil {
			return nil, err
		}
		companyID = strings.TrimSpace(validate.Company)
	}
	sites, err := client.ListSites(ctx, companyID)
	if err != nil {
		return nil, err
	}
	values, _ := loadGlobalConfig()
	baseDomain := strings.TrimSuffix(strings.TrimSpace(values["base_domain"]), ".")
	type discoveredKinstaEnv struct {
		Env        kinsta.Environment
		EnvName    string
		PHPVersion string
		Domains    []kinsta.Domain
		Primary    kinsta.Domain
	}
	projectClaims := map[string]string{}
	records := []map[string]any{}
	for _, site := range sites {
		kinstaSlug := firstNonEmpty(site.Name, site.DisplayName, site.ID)
		if kinstaSlug == "" || site.ID == "" {
			continue
		}
		envs, err := client.ListEnvironments(ctx, site.ID)
		if err != nil {
			return nil, fmt.Errorf("site %s environments: %w", kinstaSlug, err)
		}
		discovered := make([]discoveredKinstaEnv, 0, len(envs))
		projectSlug := ""
		for i, env := range envs {
			envName := kinstaCacheEnvName(env, i)
			phpVersion := env.CurrentPHPVersion()
			domains, err := client.ListDomains(ctx, env.ID)
			if err != nil {
				return nil, fmt.Errorf("site %s env %s domains: %w", kinstaSlug, envName, err)
			}
			envProjectSlug, err := kinstaProjectSlugFromDomains(domains, baseDomain)
			if err != nil {
				return nil, fmt.Errorf("site %s env %s domains: %w", kinstaSlug, envName, err)
			}
			if envProjectSlug != "" {
				if projectSlug != "" && projectSlug != envProjectSlug {
					return nil, fmt.Errorf("Kinsta site %s (%s) has conflicting nf project slugs %q and %q in internal domains", kinstaSlug, site.ID, projectSlug, envProjectSlug)
				}
				projectSlug = envProjectSlug
			}
			domain := kinstaEnvPrimaryDomain(env)
			if domain.ID == "" || domainName(domain) == "" {
				domain = preferredKinstaDomain(domains)
			}
			discovered = append(discovered, discoveredKinstaEnv{Env: env, EnvName: envName, PHPVersion: phpVersion, Domains: domains, Primary: domain})
		}
		projectSlug = firstNonEmpty(projectSlug, kinstaSlug)
		if owner := projectClaims[projectSlug]; owner != "" && owner != site.ID {
			return nil, fmt.Errorf("Kinsta sites %s and %s both claim nf project slug %q", owner, site.ID, projectSlug)
		}
		projectClaims[projectSlug] = site.ID
		siteID := kinstaSiteID(projectSlug)
		for _, item := range discovered {
			env := item.Env
			envName := item.EnvName
			phpVersion := item.PHPVersion
			domains := item.Domains
			domain := item.Primary
			cfg, _ := client.SFTPConfig(ctx, site.ID, env.ID)
			user := firstNonEmpty(cfg.User, kinstaSlug)
			pathValue := kinstaEnvPath(user, env.WebRoot)
			database := firstNonEmpty(user, kinstaSlug)
			host := firstNonEmpty(cfg.Host, env.SSHConnection.SSHIP.ExternalIP)
			port := firstNonEmpty(cfg.Port, env.SSHConnection.SSHPort, "22")
			domainValue := domainName(domain)
			internalDomain := kinstaInternalDomain(domains)
			sshCmd := firstNonEmpty(cfg.SSHCommand, sshCommand(user, host, port))
			record := map[string]any{
				"provider":     "kinsta",
				"env_id":       canonicalEnvID(siteID, envName),
				"site_id":      siteID,
				"name":         projectSlug,
				"project_slug": projectSlug,
				"env":          envName,
				"target":       targetName,
				"hostname":     domainValue,
				"url":          kinstaURL(domainValue),
				"path":         pathValue,
				"database":     database,
				"php_version":  phpVersion,
				"status":       "active",
				"ssh":          sshRecord(user, host, port, sshCmd),
				"kinsta": map[string]any{
					"site_id":        site.ID,
					"slug":           kinstaSlug,
					"environment_id": env.ID,
					"domain_id":      domain.ID,
					"branch":         kinstaEnvBranch(envName),
				},
			}
			if internalDomain != "" {
				record["internal_hostname"] = internalDomain
				record["internal_url"] = kinstaURL(internalDomain)
			}
			if entries := kinstaDomainCacheEntries(domains, domain); len(entries) > 0 {
				record["domains"] = entries
			}
			if primaryDomain := kinstaPrimaryPublicDomain(domains, domain); primaryDomain != "" {
				record["primary_domain"] = primaryDomain
			}
			records = append(records, record)
		}
	}
	return records, nil
}

func kinstaCacheEnvName(env kinsta.Environment, index int) string {
	value := strings.ToLower(strings.TrimSpace(firstNonEmpty(env.Name, env.DisplayName)))
	if strings.Contains(value, "stag") {
		return "staging"
	}
	if value == "live" || index == 0 {
		return "live"
	}
	return value
}

func kinstaEnvPrimaryDomain(env kinsta.Environment) kinsta.Domain {
	if env.PrimaryDomain.ID != "" || domainName(env.PrimaryDomain) != "" {
		return env.PrimaryDomain
	}
	return preferredKinstaDomain(env.Domains)
}

func preferredKinstaDomain(domains []kinsta.Domain) kinsta.Domain {
	for _, domain := range domains {
		if domain.IsPrimary || strings.EqualFold(strings.TrimSpace(domain.Type), "live") {
			return domain
		}
	}
	if len(domains) > 0 {
		return domains[0]
	}
	return kinsta.Domain{}
}

func domainName(domain kinsta.Domain) string {
	return firstNonEmpty(domain.Name, domain.Domain, domain.DomainName)
}

func kinstaDomainCacheEntries(domains []kinsta.Domain, primary kinsta.Domain) []map[string]any {
	entries := []map[string]any{}
	seen := map[string]bool{}
	primaryID := strings.TrimSpace(primary.ID)
	primaryName := normalizeDomainName(domainName(primary))
	hasListedPrimary := false
	for _, domain := range domains {
		if domain.IsPrimary {
			hasListedPrimary = true
			break
		}
	}
	for _, domain := range domains {
		name := normalizeDomainName(domainName(domain))
		if name == "" || siteDomainWildcardName(name) || seen[name] {
			continue
		}
		seen[name] = true
		role := "secondary"
		if domain.IsPrimary || !hasListedPrimary && ((primaryID != "" && domain.ID == primaryID) || (primaryName != "" && name == primaryName)) {
			role = "primary"
		}
		management := "external"
		status := kinstaDomainStatus(domain, false)
		if kinstaInternalDomainName(name) {
			management = "internal"
			status = kinstaDomainStatus(domain, true)
		}
		entry := map[string]any{"name": name, "role": role, "management": management, "status": status}
		if domain.ID != "" {
			entry["domain_id"] = domain.ID
		}
		entries = append(entries, entry)
	}
	return entries
}

func kinstaDomainStatus(domain kinsta.Domain, internal bool) string {
	status := normalizeSiteDomainStatus(firstNonEmpty(domain.Status, domain.State, domain.DomainStatus, domain.DNSStatus, domain.VerificationStatus))
	if status != "pending" {
		return status
	}
	if domain.IsVerified != nil {
		if *domain.IsVerified {
			return "active"
		}
		return "pending"
	}
	if domain.IsPointing != nil {
		if *domain.IsPointing {
			return "active"
		}
		return "pending"
	}
	if internal {
		return "active"
	}
	return "pending"
}

func kinstaInternalDomain(domains []kinsta.Domain) string {
	for _, domain := range domains {
		name := normalizeDomainName(domainName(domain))
		if name != "" && domain.IsPrimary && kinstaInternalDomainName(name) {
			return name
		}
	}
	for _, domain := range domains {
		name := normalizeDomainName(domainName(domain))
		if name != "" && kinstaInternalDomainName(name) && !strings.HasSuffix(name, ".kinsta.cloud") {
			return name
		}
	}
	for _, domain := range domains {
		name := normalizeDomainName(domainName(domain))
		if name != "" && kinstaInternalDomainName(name) {
			return name
		}
	}
	return ""
}

func kinstaProjectSlugFromDomains(domains []kinsta.Domain, baseDomain string) (string, error) {
	baseDomain = normalizeDomainName(baseDomain)
	if baseDomain == "" {
		return "", nil
	}
	slug := ""
	for _, domain := range domains {
		name := normalizeDomainName(domainName(domain))
		if name == "" || siteDomainWildcardName(name) {
			continue
		}
		candidate, ok := kinstaProjectSlugFromDomain(name, baseDomain)
		if !ok {
			continue
		}
		if slug != "" && slug != candidate {
			return "", fmt.Errorf("conflicting nf internal domains imply project slugs %q and %q", slug, candidate)
		}
		slug = candidate
	}
	return slug, nil
}

func kinstaProjectSlugFromDomain(name, baseDomain string) (string, bool) {
	name = normalizeDomainName(name)
	baseDomain = normalizeDomainName(baseDomain)
	suffix := ".kinsta." + baseDomain
	if name == "" || baseDomain == "" || !strings.HasSuffix(name, suffix) {
		return "", false
	}
	label := strings.TrimSuffix(name, suffix)
	if label == "" || strings.Contains(label, ".") {
		return "", false
	}
	label = strings.TrimSuffix(label, "-staging")
	if validateSiteAddSlug(label) != nil {
		return "", false
	}
	return label, true
}

func kinstaPrimaryPublicDomain(domains []kinsta.Domain, primary kinsta.Domain) string {
	primaryID := strings.TrimSpace(primary.ID)
	primaryName := normalizeDomainName(domainName(primary))
	for _, domain := range domains {
		name := normalizeDomainName(domainName(domain))
		if name == "" || kinstaInternalDomainName(name) {
			continue
		}
		if domain.IsPrimary || (primaryID != "" && domain.ID == primaryID) || (primaryName != "" && name == primaryName) {
			return name
		}
	}
	if primaryName != "" && !kinstaInternalDomainName(primaryName) {
		return primaryName
	}
	return ""
}

func kinstaInternalDomainName(name string) bool {
	return strings.Contains(normalizeDomainName(name), ".kinsta.")
}

func kinstaURL(domain string) string {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return ""
	}
	return "https://" + domain
}

func kinstaEnvBranch(env string) string {
	if env == "staging" {
		return "develop"
	}
	return "main"
}

func discoverLinodeTargetSites(target map[string]any) ([]map[string]any, error) {
	remoteTarget := map[string]any{}
	if data, err := readLinodeTargetFile(target); err == nil {
		remoteTarget = data
	}
	mergedTarget := cloneRecord(target)
	for key, value := range remoteTarget {
		if recordValueString(mergedTarget[key]) == "" {
			mergedTarget[key] = value
		}
	}
	sshHost := serverSSHHost(target)
	sshUser := serverSSHUser(target)
	args := []string{"ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null"}
	if port := firstNonEmpty(mapStringAtPath(target, "ssh", "port"), firstRecordString(target, "ssh_port")); port != "" {
		args = append(args, "-p", port)
	}
	destination := sshHost
	if sshUser != "" {
		destination = sshUser + "@" + sshHost
	}
	args = append(args, destination, "cat", "/var/lib/nf/sites.json")
	data, err := runSSHOutputFn(args)
	if err != nil {
		return nil, err
	}
	records, err := parseRemoteSiteRecords(data)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		normalizeRemoteSiteRecord(record, mergedTarget)
	}
	return records, nil
}

func readLinodeTargetFile(target map[string]any) (map[string]any, error) {
	sshHost := serverSSHHost(target)
	sshUser := serverSSHUser(target)
	args := []string{"ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null"}
	if port := firstNonEmpty(mapStringAtPath(target, "ssh", "port"), firstRecordString(target, "ssh_port")); port != "" {
		args = append(args, "-p", port)
	}
	destination := sshHost
	if sshUser != "" {
		destination = sshUser + "@" + sshHost
	}
	args = append(args, destination, "cat", "/var/lib/nf/target.json")
	data, err := runSSHOutputFn(args)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	record := map[string]any{}
	if err := dec.Decode(&record); err != nil {
		return nil, fmt.Errorf("parse /var/lib/nf/target.json: %w", err)
	}
	return record, nil
}

func parseRemoteSiteRecords(data []byte) ([]map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	var payload any
	if err := dec.Decode(&payload); err != nil {
		return nil, fmt.Errorf("parse /var/lib/nf/sites.json: %w", err)
	}
	switch typed := payload.(type) {
	case []any:
		records := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			record, ok := item.(map[string]any)
			if !ok {
				return nil, ProjectError{Msg: "/var/lib/nf/sites.json must contain site objects"}
			}
			records = append(records, record)
		}
		return records, nil
	case map[string]any:
		if list, ok := typed["sites"].([]any); ok {
			records := make([]map[string]any, 0, len(list))
			for _, item := range list {
				record, ok := item.(map[string]any)
				if !ok {
					return nil, ProjectError{Msg: "/var/lib/nf/sites.json sites must contain site objects"}
				}
				records = append(records, record)
			}
			return records, nil
		}
	}
	return nil, ProjectError{Msg: "Unsupported JSON shape in /var/lib/nf/sites.json"}
}

func normalizeRemoteSiteRecord(record, target map[string]any) {
	targetName := firstRecordString(target, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id")
	if targetName != "" {
		if firstRecordString(record, "target", "server", "server_name", "server_id", "server_hostname", "server_label") == "" {
			record["target"] = targetName
		}
	}
	if recordValueString(record["provider"]) == "" {
		record["provider"] = "linode"
	}
	if firstRecordString(record, "site_id") == "" {
		if siteID := siteCanonicalID(siteRecordName(record), siteProviderTarget(record)); siteID != "" {
			record["site_id"] = siteID
		}
	}
	if envID := canonicalEnvID(siteRecordID(record), siteEnvName(record)); envID != "" {
		record["env_id"] = envID
	}
	if sitePHPVersion(record) == "" {
		if phpVersion := targetPHPVersion(target); phpVersion != "" {
			record["php_version"] = phpVersion
		}
	}
	if mapStringAtPath(record, "ssh", "host") == "" {
		sshHost := serverSSHHost(target)
		sshUser := serverSSHUser(target)
		sshPort := firstNonEmpty(mapStringAtPath(target, "ssh", "port"), firstRecordString(target, "ssh_port"), "22")
		if sshHost != "" || sshUser != "" || sshPort != "" {
			record["ssh"] = sshRecord(sshUser, sshHost, sshPort, sshCommand(sshUser, sshHost, sshPort))
		}
	}
	if strings.EqualFold(recordValueString(record["provider"]), "linode") {
		if linode := mapMapAtPath(record, "linode"); linode != nil {
			delete(linode, "target_hostname")
			if len(linode) == 0 {
				delete(record, "linode")
			}
		}
		delete(record, "server")
		delete(record, "server_name")
		delete(record, "server_hostname")
		delete(record, "environment")
	}
}

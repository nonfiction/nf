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
		printTargetDetails(record)
		return 0
	}
	data, _ := json.MarshalIndent(record, "", "  ")
	fmt.Println(string(data))
	return 0
}

func printTargetDetails(record map[string]any) {
	name := firstRecordString(record, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id")
	provider := recordValueString(record["provider"])
	fmt.Printf("Target: %s\n", name)
	fmt.Printf("Provider: %s\n", provider)
	if hostname := firstRecordString(record, "hostname", "host", "public_ipv4", "ipv4", "ip"); hostname != "" {
		fmt.Printf("Hostname: %s\n", hostname)
	}
	if id := firstRecordString(record, "id", "provider_id", "linode_id"); id != "" {
		fmt.Printf("ID: %s\n", id)
	}
	if status := targetLiveStatus(record); status != "" {
		fmt.Printf("Status: %s\n", status)
	}
	if cachedStatus := recordValueString(record["status"]); cachedStatus != "" {
		fmt.Printf("Cached status: %s\n", cachedStatus)
	}
	if region := firstRecordString(record, "region"); region != "" {
		fmt.Printf("Region: %s\n", region)
	}
	if targetType := firstRecordString(record, "type", "linode_type"); targetType != "" {
		fmt.Printf("Type: %s\n", targetType)
	}
	if image := firstRecordString(record, "image"); image != "" {
		fmt.Printf("Image: %s\n", image)
	}
	if sshHost := serverSSHHost(record); sshHost != "" {
		ssh := sshHost
		if sshUser := serverSSHUser(record); sshUser != "" {
			ssh = sshUser + "@" + ssh
		}
		if port := firstNonEmpty(mapStringAtPath(record, "ssh", "port"), firstRecordString(record, "ssh_port")); port != "" && port != "22" {
			ssh += ":" + port
		}
		fmt.Printf("SSH: %s\n", ssh)
	}
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
	records := []map[string]any{}
	for _, site := range sites {
		siteName := firstNonEmpty(site.Name, site.DisplayName, site.ID)
		if siteName == "" || site.ID == "" {
			continue
		}
		siteID := kinstaSiteID(siteName)
		envs, err := client.ListEnvironments(ctx, site.ID)
		if err != nil {
			return nil, fmt.Errorf("site %s environments: %w", siteName, err)
		}
		for i, env := range envs {
			envName := kinstaCacheEnvName(env, i)
			phpVersion := env.CurrentPHPVersion()
			domain := kinstaEnvPrimaryDomain(env)
			if domain.ID == "" || domainName(domain) == "" {
				domains, err := client.ListDomains(ctx, env.ID)
				if err == nil {
					domain = preferredKinstaDomain(domains)
				}
			}
			cfg, _ := client.SFTPConfig(ctx, site.ID, env.ID)
			pathValue := kinstaEnvPath(firstNonEmpty(cfg.User, siteName), env.WebRoot)
			database := firstNonEmpty(cfg.User, siteName)
			host := firstNonEmpty(cfg.Host, env.SSHConnection.SSHIP.ExternalIP)
			port := firstNonEmpty(cfg.Port, env.SSHConnection.SSHPort, "22")
			user := cfg.User
			domainValue := domainName(domain)
			records = append(records, map[string]any{
				"provider":    "kinsta",
				"env_id":      canonicalEnvID(siteID, envName),
				"site_id":     siteID,
				"name":        siteName,
				"env":         envName,
				"target":      targetName,
				"hostname":    domainValue,
				"url":         kinstaURL(domainValue),
				"path":        pathValue,
				"database":    database,
				"php_version": phpVersion,
				"status":      "active",
				"ssh":         sshRecord(user, host, port, cfg.SSHCommand),
				"kinsta": map[string]any{
					"site_id":        site.ID,
					"environment_id": env.ID,
					"domain_id":      domain.ID,
					"branch":         kinstaEnvBranch(envName),
				},
			})
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

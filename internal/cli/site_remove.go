package cli

// Site removal planning and execution for Linode and Kinsta targets.

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/kinsta"
	"github.com/nonfiction/nf/internal/state"
	"github.com/nonfiction/nf/internal/ui"
)

func siteAddTargetProvider(targetRef string) (string, error) {
	targets, err := cachedTargets()
	if err != nil {
		return "", err
	}
	target := state.MatchingRecord(targets, targetRef)
	if target == nil {
		return "", ProjectError{Msg: fmt.Sprintf("No target matched %q.", targetRef)}
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(target["provider"])))
	if provider == "" {
		return "", ProjectError{Msg: fmt.Sprintf("Target %q is missing provider.", targetRef)}
	}
	return provider, nil
}

func buildSiteRemovePlan(siteID string) (siteRemovePlan, error) {
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		return siteRemovePlan{}, err
	}
	matches, resolvedSiteID, err := siteRecordsMatchingSite(records, siteID)
	if err != nil {
		return siteRemovePlan{}, err
	}
	if len(matches) == 0 {
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("No site matched %q.", siteID)}
	}
	first := matches[0]
	provider := strings.ToLower(strings.TrimSpace(recordValueString(first["provider"])))
	if provider == "kinsta" {
		return buildKinstaSiteRemovePlan(matches, resolvedSiteID)
	}
	if provider != "linode" {
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("Unsupported provider %q. Only linode and kinsta site remove are available.", provider)}
	}
	return buildLinodeSiteRemovePlan(matches, resolvedSiteID)
}

func buildLinodeSiteRemovePlan(matches []map[string]any, resolvedSiteID string) (siteRemovePlan, error) {
	first := matches[0]
	provider := strings.ToLower(strings.TrimSpace(recordValueString(first["provider"])))
	targetName := siteProviderTarget(first)
	if targetName == "" {
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("Site %q is missing a target.", resolvedSiteID)}
	}
	targets, err := cachedTargets()
	if err != nil {
		return siteRemovePlan{}, err
	}
	target := state.MatchingRecord(targets, targetName)
	if target == nil {
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("No target matched site target %q.", targetName)}
	}
	sshHost := serverSSHHost(target)
	if sshHost == "" {
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("Target %q is missing an SSH host.", targetName)}
	}
	values, err := loadGlobalConfig()
	if err != nil {
		return siteRemovePlan{}, err
	}
	sshUser := firstNonEmpty(serverSSHUser(target), values["linode_default_user"])
	if sshUser == "" {
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("Target %q is missing an SSH user. Set linode_default_user with nf config set-linode-default-user <user>.", targetName)}
	}
	plan := siteRemovePlan{
		SiteID:     resolvedSiteID,
		Name:       siteRecordName(first),
		Provider:   provider,
		Target:     target,
		TargetName: targetName,
		SSHUser:    sshUser,
		SSHHost:    sshHost,
		Envs:       make([]siteRemoveEnvPlan, 0, len(matches)),
	}
	for _, record := range matches {
		env := siteRemoveEnvPlan{
			Env:      siteEnvName(record),
			EnvID:    firstRecordString(record, "env_id"),
			Path:     firstRecordString(record, "path"),
			Database: firstRecordString(record, "database", "db_name"),
			Hostname: firstRecordString(record, "hostname", "url"),
		}
		if env.EnvID == "" {
			env.EnvID = linodeEnvID(siteRecordName(record), targetName, env.Env)
		}
		if err := validateSiteRemoveEnv(env); err != nil {
			return siteRemovePlan{}, err
		}
		plan.Envs = append(plan.Envs, env)
	}
	sort.SliceStable(plan.Envs, func(i, j int) bool {
		left, right := siteListEnvOrder(plan.Envs[i].Env), siteListEnvOrder(plan.Envs[j].Env)
		if left != right {
			return left < right
		}
		return plan.Envs[i].Env < plan.Envs[j].Env
	})
	return plan, nil
}

func buildKinstaSiteRemovePlan(matches []map[string]any, resolvedSiteID string) (siteRemovePlan, error) {
	first := matches[0]
	values, err := loadGlobalConfig()
	if err != nil {
		return siteRemovePlan{}, err
	}
	dnsZone := strings.TrimSuffix(strings.TrimSpace(values["base_domain"]), ".")
	if dnsZone == "" {
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("Expected base_domain in %s. Set it with nf config set-base-domain <domain>.", config.ConfigFile())}
	}
	dnsAccountID := firstNonEmpty(values["dnsimple_account_id"], dnsimpleAccountIDValue())
	if dnsAccountID == "" {
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("Expected dnsimple_account_id in %s. Run nf provider check dnsimple.", config.ConfigFile())}
	}
	targetName := firstNonEmpty(siteProviderTarget(first), "kinsta")
	kinstaSiteID := ""
	plan := siteRemovePlan{
		SiteID:       resolvedSiteID,
		Name:         siteRecordName(first),
		Provider:     "kinsta",
		TargetName:   targetName,
		DNSZone:      dnsZone,
		DNSAccountID: dnsAccountID,
		Envs:         make([]siteRemoveEnvPlan, 0, len(matches)),
	}
	if targets, err := cachedTargets(); err == nil {
		if target := state.MatchingRecord(targets, targetName); target != nil {
			plan.Target = target
		}
	}
	for _, record := range matches {
		if kinstaSiteID == "" {
			kinstaSiteID = siteKinstaID(record, "site_id")
		}
		hostname := kinstaRemoveEnvHostname(record)
		env := siteRemoveEnvPlan{
			Env:      siteEnvName(record),
			EnvID:    firstNonEmpty(siteKinstaID(record, "environment_id"), firstRecordString(record, "env_id")),
			DomainID: siteKinstaID(record, "domain_id"),
			Path:     firstNonEmpty(siteKinstaID(record, "path"), firstRecordString(record, "path")),
			Database: firstNonEmpty(siteKinstaID(record, "database"), firstRecordString(record, "database", "db_name")),
			Hostname: hostname,
		}
		if strings.TrimSpace(env.Env) == "" {
			return siteRemovePlan{}, ProjectError{Msg: "Selected Kinsta site has an env with no name."}
		}
		if strings.TrimSpace(env.EnvID) == "" {
			return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("Selected Kinsta site env %q has no environment_id.", env.Env)}
		}
		plan.Envs = append(plan.Envs, env)
		if hostname != "" {
			plan.DNSNames = append(plan.DNSNames, dnsimpleRelativeName(hostname, dnsZone))
		}
	}
	if strings.TrimSpace(kinstaSiteID) == "" {
		return siteRemovePlan{}, ProjectError{Msg: fmt.Sprintf("Selected Kinsta site %q has no Kinsta site_id. Run nf site refresh and try again.", resolvedSiteID)}
	}
	plan.KinstaSiteID = kinstaSiteID
	plan.DNSNames = uniqueNonEmptyStrings(plan.DNSNames)
	sort.SliceStable(plan.Envs, func(i, j int) bool {
		left, right := siteListEnvOrder(plan.Envs[i].Env), siteListEnvOrder(plan.Envs[j].Env)
		if left != right {
			return left < right
		}
		return plan.Envs[i].Env < plan.Envs[j].Env
	})
	return plan, nil
}

func kinstaRemoveEnvHostname(record map[string]any) string {
	for _, value := range []string{firstRecordString(record, "hostname"), firstRecordString(record, "url", "site_url", "home_url")} {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if strings.Contains(value, "://") {
			parsed, err := url.Parse(value)
			if err == nil && strings.TrimSpace(parsed.Hostname()) != "" {
				return strings.TrimSpace(parsed.Hostname())
			}
		}
		return strings.TrimSuffix(value, "/")
	}
	return ""
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func sortedMapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func validateSiteRemoveEnv(env siteRemoveEnvPlan) error {
	if strings.TrimSpace(env.Env) == "" {
		return ProjectError{Msg: "Selected site has an env with no name."}
	}
	if strings.TrimSpace(env.EnvID) == "" {
		return ProjectError{Msg: fmt.Sprintf("Selected site env %q has no env_id.", env.Env)}
	}
	if !safeDatabaseName(env.Database) {
		return ProjectError{Msg: fmt.Sprintf("Selected site env %q has unsafe database name %q.", env.Env, env.Database)}
	}
	if !safeSitePath(env.Path) {
		return ProjectError{Msg: fmt.Sprintf("Selected site env %q has unsafe path %q.", env.Env, env.Path)}
	}
	return nil
}

func safeDatabaseName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func safeSitePath(sitePath string) bool {
	cleaned := path.Clean(sitePath)
	if strings.Contains(cleaned, "..") || !strings.HasPrefix(cleaned, "/var/www/sites/") {
		return false
	}
	if strings.HasSuffix(cleaned, "/public") {
		return true
	}
	rel := strings.TrimPrefix(cleaned, "/var/www/sites/")
	return rel != "" && !strings.Contains(rel, "/")
}

func printSiteRemovePlan(plan siteRemovePlan, mode string) {
	title := "Remove site plan:"
	if plan.EnvOnly {
		title = "Remove staging env plan:"
	}
	fmt.Println(title)
	fmt.Printf("  site id: %s\n", plan.SiteID)
	if plan.Name != "" {
		fmt.Printf("  site: %s\n", plan.Name)
	}
	fmt.Printf("  target: %s\n", plan.TargetName)
	fmt.Printf("  provider: %s\n", plan.Provider)
	if plan.Provider == "kinsta" {
		fmt.Printf("  kinsta site id: %s\n", plan.KinstaSiteID)
		fmt.Printf("  dns: dnsimple zone %s account %s\n", plan.DNSZone, plan.DNSAccountID)
		for _, name := range plan.DNSNames {
			fmt.Printf("  dns delete: A %s\n", dnsimpleFQDNForRelativeName(name, plan.DNSZone))
			fmt.Printf("  dns delete: TXT %s\n", dnsimpleFQDNForRelativeName(dnsimpleTLSChallengeName(name), plan.DNSZone))
		}
		for _, env := range plan.Envs {
			fmt.Printf("  env %s:\n", env.Env)
			fmt.Printf("    kinsta environment id: %s\n", env.EnvID)
			if env.DomainID != "" {
				fmt.Printf("    kinsta domain id: %s\n", env.DomainID)
			}
			if env.Hostname != "" {
				fmt.Printf("    domain: %s\n", env.Hostname)
			}
		}
		if plan.EnvOnly {
			fmt.Printf("  remote actions: delete Kinsta staging environment\n")
		} else {
			fmt.Printf("  remote actions: delete Kinsta environments, delete Kinsta site\n")
		}
		fmt.Printf("  local state: %s\n", state.StatePath("sites"))
		fmt.Printf("  mode: %s\n", mode)
		return
	}
	fmt.Printf("  ssh: %s@%s\n", plan.SSHUser, plan.SSHHost)
	fmt.Println("  dns actions: none")
	for _, env := range plan.Envs {
		fmt.Printf("  env %s:\n", env.Env)
		fmt.Printf("    env id: %s\n", env.EnvID)
		fmt.Printf("    delete path: %s\n", env.Path)
		fmt.Printf("    drop database: %s\n", env.Database)
		if env.Hostname != "" {
			fmt.Printf("    vhost: %s\n", env.Hostname)
		}
	}
	fmt.Printf("  remote state: /var/lib/nf/sites.json\n")
	fmt.Printf("  local state: %s\n", state.StatePath("sites"))
	fmt.Printf("  mode: %s\n", mode)
}

func renderSiteRemoveScript(plan siteRemovePlan) string {
	q := shellQuoteArg
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	b.WriteString("remove_env() {\n")
	b.WriteString("  env_id=$1 file_slug=$2 site_path=$3 db_name=$4\n")
	b.WriteString("  rm -f /etc/nginx/sites-enabled/nf-site-$file_slug /etc/nginx/sites-available/nf-site-$file_slug\n")
	b.WriteString("  rm -f /var/log/nginx/sites/$file_slug.access.log /var/log/nginx/sites/$file_slug.error.log\n")
	b.WriteString("  if [ \"$file_slug\" != \"$env_id\" ]; then\n")
	b.WriteString("    rm -f /etc/nginx/sites-enabled/nf-site-$env_id /etc/nginx/sites-available/nf-site-$env_id\n")
	b.WriteString("    rm -f /var/log/nginx/sites/$env_id.access.log /var/log/nginx/sites/$env_id.error.log\n")
	b.WriteString("  fi\n")
	b.WriteString("  rm -rf -- \"$site_path\"\n")
	b.WriteString("  parent=$(dirname \"$site_path\")\n")
	b.WriteString("  if [ \"$parent\" != /var/www/sites ]; then rmdir --ignore-fail-on-non-empty -- \"$parent\" 2>/dev/null || true; fi\n")
	b.WriteString("  mariadb -uroot <<SQL\n")
	b.WriteString("DROP DATABASE IF EXISTS \\`$db_name\\`;\n")
	b.WriteString("DROP USER IF EXISTS '$db_name'@'localhost';\n")
	b.WriteString("FLUSH PRIVILEGES;\n")
	b.WriteString("SQL\n")
	b.WriteString("}\n")
	for _, env := range plan.Envs {
		b.WriteString("remove_env ")
		b.WriteString(q(env.EnvID))
		b.WriteByte(' ')
		b.WriteString(q(envIDFileSlug(env.EnvID)))
		b.WriteByte(' ')
		b.WriteString(q(env.Path))
		b.WriteByte(' ')
		b.WriteString(q(env.Database))
		b.WriteByte('\n')
	}
	b.WriteString("if [ -f /var/lib/nf/sites.json ]; then\n")
	b.WriteString("  tmp=$(mktemp)\n")
	if plan.EnvOnly && len(plan.Envs) == 1 {
		b.WriteString("  jq --arg site_id ")
		b.WriteString(q(plan.SiteID))
		b.WriteString(" --arg env ")
		b.WriteString(q(plan.Envs[0].Env))
		b.WriteString(" 'map(select(.site_id != $site_id or .env != $env))' /var/lib/nf/sites.json >\"$tmp\" && install -o ")
	} else {
		b.WriteString("  jq --arg site_id ")
		b.WriteString(q(plan.SiteID))
		b.WriteString(" 'map(select(.site_id != $site_id))' /var/lib/nf/sites.json >\"$tmp\" && install -o ")
	}
	b.WriteString(q(plan.SSHUser))
	b.WriteString(" -g www-data -m 0664 \"$tmp\" /var/lib/nf/sites.json && rm -f \"$tmp\"\n")
	b.WriteString("fi\n")
	b.WriteString("nginx -t\n")
	b.WriteString("systemctl reload nginx\n")
	b.WriteString("systemctl reload php8.3-fpm || systemctl restart php8.3-fpm\n")
	return b.String()
}

func removeSiteFromLocalCache(siteID string) error {
	_, err := state.DeleteStateRecords("sites", func(record map[string]any) bool {
		return normalizedRecordString(siteRecordID(record)) == normalizedRecordString(siteID)
	})
	return err
}

func removeSiteEnvFromLocalCache(siteID, env string) error {
	_, err := state.DeleteStateRecords("sites", func(record map[string]any) bool {
		return normalizedRecordString(siteRecordID(record)) == normalizedRecordString(siteID) && normalizedRecordString(siteEnvName(record)) == normalizedRecordString(env)
	})
	return err
}

func removeKinstaSite(plan siteRemovePlan) error {
	token := envwizard.Value("KINSTA_API_KEY")
	if token == "" {
		return fmt.Errorf("Expected KINSTA_API_KEY in the environment or %s.", config.EnvFile())
	}
	dnsToken := envwizard.Value("DNSIMPLE_TOKEN")
	if dnsToken == "" {
		return fmt.Errorf("Expected DNSIMPLE_TOKEN in the environment or %s.", config.EnvFile())
	}
	client := kinsta.NewClient(firstNonEmpty(envwizard.Value("KINSTA_BASE_URL"), "https://api.kinsta.com/v2"), token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	aRecords := map[string]bool{}
	txtRecords := map[string]bool{}
	for _, name := range plan.DNSNames {
		aRecords[name] = true
		txtRecords[dnsimpleTLSChallengeName(name)] = true
	}
	for _, env := range plan.Envs {
		if env.DomainID == "" {
			continue
		}
		records, err := client.DomainRecords(ctx, env.DomainID)
		if err != nil {
			return err
		}
		for _, record := range append(append([]kinsta.DNSRecord{}, records.Pointing...), records.Verification...) {
			fqdn := record.RecordName()
			if !kinstaDNSRecordBelongsToDomain(fqdn, env.Hostname) {
				continue
			}
			name := dnsimpleRelativeName(fqdn, plan.DNSZone)
			switch strings.ToUpper(record.RecordTypeName()) {
			case "A":
				aRecords[name] = true
			case "TXT":
				txtRecords[name] = true
			}
		}
	}
	for _, name := range sortedMapKeys(aRecords) {
		fmt.Printf("Deleting DNS A %s...\n", dnsimpleFQDNForRelativeName(name, plan.DNSZone))
		if err := deleteDNSRecordFn(dnsToken, plan.DNSAccountID, plan.DNSZone, name); err != nil {
			return err
		}
	}
	for _, txtName := range sortedMapKeys(txtRecords) {
		fmt.Printf("Deleting DNS TXT %s...\n", dnsimpleFQDNForRelativeName(txtName, plan.DNSZone))
		if err := deleteDNSTXTRecordFn(dnsToken, plan.DNSAccountID, plan.DNSZone, txtName); err != nil {
			return err
		}
	}
	for _, env := range plan.Envs {
		fmt.Printf("Deleting Kinsta environment %s (%s)...\n", env.Env, env.EnvID)
		opID, err := client.DeleteEnvironment(ctx, env.EnvID)
		if err != nil {
			return err
		}
		if err := waitKinstaOperation(ctx, client, opID); err != nil {
			return err
		}
	}
	if plan.EnvOnly {
		return nil
	}
	fmt.Printf("Deleting Kinsta site %s...\n", plan.KinstaSiteID)
	opID, err := client.DeleteSite(ctx, plan.KinstaSiteID)
	if err != nil {
		return err
	}
	return waitKinstaOperation(ctx, client, opID)
}

func cmdSiteRemove(siteID string, dryRun, execute, yes, nonInteractive bool) int {
	if execute && dryRun {
		fmt.Fprintln(os.Stderr, "Choose either --execute or --dry-run, not both.")
		return 1
	}
	if nonInteractive && execute && !yes {
		fmt.Fprintln(os.Stderr, "Remote execution requires both --execute and --yes in non-interactive mode.")
		return 1
	}
	if !execute && (dryRun || nonInteractive) {
		dryRun = true
	}
	plan, err := buildSiteRemovePlan(siteID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	willExecute := execute || (!dryRun && !nonInteractive)
	mode := "dry-run"
	if willExecute {
		mode = "execute"
	}
	printSiteRemovePlan(plan, mode)
	if !willExecute {
		return 0
	}
	if !yes {
		message := fmt.Sprintf("Remove site %q from target %q and delete its databases and files?", plan.SiteID, plan.TargetName)
		if plan.Provider == "kinsta" {
			message = fmt.Sprintf("Remove Kinsta site %q, delete all cached Kinsta environments, and free it from the Kinsta account?", plan.SiteID)
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
	if err := removeSiteFromLocalCache(plan.SiteID); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Site removed.")
	return 0
}

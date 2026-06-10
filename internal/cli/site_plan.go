package cli

// Site add/remove plan data and shared site naming helpers.

import (
	"fmt"
	"path"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/state"
)

type siteAddArgs struct {
	target         string
	site           string
	region         string
	phpVersion     string
	withStaging    bool
	execute        bool
	dryRun         bool
	yes            bool
	nonInteractive bool
}

type siteEnvPlan struct {
	Env      string
	Path     string
	Database string
	Hostname string
	URL      string
	Title    string
}

type siteAddPlan struct {
	Target          map[string]any
	TargetName      string
	SSHUser         string
	SSHHost         string
	Site            string
	SiteID          string
	BaseDomain      string
	PasswordVersion string
	PHPVersion      string
	AdminUser       string
	AdminEmail      string
	AdminPassword   string
	DBPassword      string
	Envs            []siteEnvPlan
}

type kinstaSiteAddEnvPlan struct {
	Env      string
	Domain   string
	URL      string
	Title    string
	Branch   string
	EnvID    string
	DomainID string
	Path     string
	Database string
	SSHHost  string
	SSHPort  string
	SSHUser  string
	SSHCmd   string
}

type kinstaSiteAddPlan struct {
	Target          map[string]any
	TargetName      string
	CompanyID       string
	KinstaSiteID    string
	Site            string
	SiteID          string
	BaseDomain      string
	PasswordVersion string
	Region          string
	PHPVersion      string
	AdminUser       string
	AdminEmail      string
	AdminPassword   string
	DNSZone         string
	DNSAccountID    string
	Envs            []kinstaSiteAddEnvPlan
}

type kinstaProvisionResult struct {
	CompanyID string
	SiteID    string
	Envs      []kinstaSiteAddEnvPlan
}

type siteRemoveEnvPlan struct {
	Env      string
	EnvID    string
	DomainID string
	Path     string
	Database string
	Hostname string
}

type siteDNSDeletePlan struct {
	Name       string
	RecordType string
	Inferred   bool
}

type siteRemovePlan struct {
	SiteID       string
	Name         string
	Provider     string
	EnvOnly      bool
	Target       map[string]any
	TargetName   string
	KinstaSiteID string
	DNSZone      string
	DNSAccountID string
	DNSRecords   []siteDNSDeletePlan
	SSHUser      string
	SSHHost      string
	Envs         []siteRemoveEnvPlan
}

func cleanSiteSlug(input string) (string, error) {
	slug := strings.ToLower(strings.TrimSpace(input))
	if slug == "" {
		return "", ProjectError{Msg: "site name cannot be empty"}
	}
	if strings.HasPrefix(slug, "-") || strings.HasSuffix(slug, "-") {
		return "", ProjectError{Msg: fmt.Sprintf("site name %q must not start or end with '-'", input)}
	}
	for _, r := range slug {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			continue
		}
		return "", ProjectError{Msg: fmt.Sprintf("site name %q must use only lowercase letters, numbers, and '-'", input)}
	}
	return slug, nil
}

func siteDBName(site, env string) string {
	name := strings.ReplaceAll(site, "-", "_")
	if env == "staging" {
		return name + "_staging"
	}
	return name
}

func siteEnvPath(site, env string) string {
	if env == "staging" {
		return path.Join("/var/www/sites", site+"_staging", "public")
	}
	return path.Join("/var/www/sites", site, "public")
}

func siteEnvHostname(site, targetName, baseDomain, env string) string {
	label := site
	if env == "staging" {
		label += "-staging"
	}
	return label + "." + targetName + "." + baseDomain
}

func linodeSiteID(site, targetName string) string {
	return site + "." + targetName
}

func linodeEnvID(site, targetName, env string) string {
	return canonicalEnvID(linodeSiteID(site, targetName), env)
}

func sshRecord(user, host, port, command string) map[string]any {
	ssh := map[string]any{}
	if host != "" {
		ssh["host"] = host
	}
	if port != "" {
		ssh["port"] = port
	}
	if user != "" {
		ssh["user"] = user
	}
	if command != "" {
		ssh["command"] = command
	}
	return ssh
}

func sshCommand(user, host, port string) string {
	if host == "" {
		return ""
	}
	destination := host
	if user != "" {
		destination = user + "@" + host
	}
	if port != "" {
		return "ssh " + destination + " -p " + port
	}
	return "ssh " + destination
}

func siteEnvTitle(site, env string) string {
	title := slugToTitle(site)
	if env == "staging" {
		return title + " Staging"
	}
	return title
}

func siteAddEnvNames(withStaging bool) []string {
	if withStaging {
		return []string{"live", "staging"}
	}
	return []string{"live"}
}

func buildSiteAddPlan(args siteAddArgs) (siteAddPlan, error) {
	siteSlug, err := cleanSiteSlug(args.site)
	if err != nil {
		return siteAddPlan{}, err
	}
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
	adminUser := firstNonEmpty(values["default_wp_user"], "admin")
	targets, err := cachedTargets()
	if err != nil {
		return siteAddPlan{}, err
	}
	target := state.MatchingRecord(targets, args.target)
	if target == nil {
		return siteAddPlan{}, ProjectError{Msg: fmt.Sprintf("No target matched %q.", args.target)}
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(target["provider"])))
	if provider != "linode" {
		return siteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Unsupported provider %q. Only linode site add is available.", provider)}
	}
	targetName := firstRecordString(target, "target_name", "name", "slug", "label", "_state_key")
	if targetName == "" {
		return siteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Target %q is missing a name.", args.target)}
	}
	sshHost := serverSSHHost(target)
	if sshHost == "" {
		return siteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Target %q is missing an SSH host.", targetName)}
	}
	sshUser := firstNonEmpty(serverSSHUser(target), values["linode_default_user"])
	if sshUser == "" {
		return siteAddPlan{}, ProjectError{Msg: fmt.Sprintf("Target %q is missing an SSH user. Set linode_default_user with nf config set-linode-default-user <user>.", targetName)}
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
	plan := siteAddPlan{
		Target:          target,
		TargetName:      targetName,
		SSHUser:         sshUser,
		SSHHost:         sshHost,
		Site:            siteSlug,
		SiteID:          linodeSiteID(siteSlug, targetName),
		BaseDomain:      baseDomain,
		PasswordVersion: passwordVersion,
		PHPVersion:      targetPHPVersion(target),
		AdminUser:       adminUser,
		AdminEmail:      adminEmail,
		AdminPassword:   adminPassword,
		DBPassword:      dbPassword,
	}
	for _, env := range siteAddEnvNames(args.withStaging) {
		hostname := siteEnvHostname(siteSlug, targetName, baseDomain, env)
		plan.Envs = append(plan.Envs, siteEnvPlan{
			Env:      env,
			Path:     siteEnvPath(siteSlug, env),
			Database: siteDBName(siteSlug, env),
			Hostname: hostname,
			URL:      "https://" + hostname,
			Title:    siteEnvTitle(siteSlug, env),
		})
	}
	return plan, nil
}

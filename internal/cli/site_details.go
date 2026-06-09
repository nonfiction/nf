package cli

// Site and environment list/show rendering plus cached credential helpers.

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/state"
)

func cmdListSites(records, servers []map[string]any) int {
	if len(records) == 0 {
		fmt.Println("No sites found.")
		return 0
	}
	type siteListRow struct {
		SiteID     string
		Name       string
		Target     string
		Envs       map[string]bool
		FirstIndex int
	}
	grouped := map[string]*siteListRow{}
	for _, record := range records {
		siteID := siteEnvDisplaySite(record)
		if siteID == "" {
			siteID = siteSummary(record)
		}
		if siteID == "" {
			continue
		}
		row := grouped[siteID]
		if row == nil {
			row = &siteListRow{SiteID: siteID, Name: siteRecordName(record), Target: siteProviderTarget(record), Envs: map[string]bool{}, FirstIndex: len(grouped)}
			grouped[siteID] = row
		}
		if row.Name == "" {
			row.Name = siteRecordName(record)
		}
		if row.Target == "" {
			row.Target = siteProviderTarget(record)
		}
		env := siteEnvName(record)
		row.Envs[env] = true
	}
	if len(grouped) == 0 {
		fmt.Println("No sites found.")
		return 0
	}
	rowsBySite := make([]*siteListRow, 0, len(grouped))
	for _, row := range grouped {
		rowsBySite = append(rowsBySite, row)
	}
	sort.Slice(rowsBySite, func(i, j int) bool { return rowsBySite[i].FirstIndex < rowsBySite[j].FirstIndex })
	rows := [][]string{{"site id", "name", "target", "envs"}}
	for _, row := range rowsBySite {
		rows = append(rows, []string{
			row.SiteID,
			row.Name,
			row.Target,
			strings.Join(sortedSiteListEnvs(row.Envs), ","),
		})
	}
	fmt.Println(formatTable(rows))
	return 0
}

func cmdListSiteEnvs(siteID string) int {
	bundle, err := state.LoadStateBundle()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	rows := [][]string{{"env id", "site", "env", "php", "url"}}
	for _, record := range bundle.Sites {
		if !siteEnvMatchesSite(record, siteID) {
			continue
		}
		rows = append(rows, []string{
			siteRecordEnvID(record),
			siteEnvDisplaySite(record),
			siteEnvName(record),
			sitePHPVersion(record),
			firstRecordString(record, "url", "site_url", "home_url", "hostname"),
		})
	}
	if len(rows) == 1 {
		if strings.TrimSpace(siteID) != "" {
			fmt.Printf("No remote envs found for %q.\n", siteID)
		} else {
			fmt.Println("No remote envs found.")
		}
		return 0
	}
	fmt.Println(formatTable(rows))
	return 0
}

func cmdShowSiteRef(needle string, jsonOutput bool) int {
	if siteID, env, ok := splitSiteEnvRef(needle); ok {
		return cmdShowSiteEnv(siteID, env, jsonOutput)
	}
	return cmdShowSite(needle, jsonOutput)
}

func cmdShowSiteEnv(siteID, env string, jsonOutput bool) int {
	siteID, env = normalizeSiteEnvRequest(siteID, env)
	if siteID == "" || env == "" {
		fmt.Fprintln(os.Stderr, "site show requires a site or env ref")
		return 1
	}
	bundle, err := state.LoadStateBundle()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var record map[string]any
	for _, candidate := range bundle.Sites {
		if siteEnvMatchesSite(candidate, siteID) && siteEnvMatchesEnv(candidate, env) {
			record = candidate
			break
		}
	}
	if record == nil {
		fmt.Fprintf(os.Stderr, "No remote env matched site %q env %q.\n", siteID, env)
		return 1
	}
	if err := validateSiteRecord(record); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	out := siteEnvDetailsOutput(siteID, env, record)
	if err := enrichSiteOutput(out, record, bundle.Servers); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := enrichSiteAdminCredentials(out, record); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !jsonOutput {
		printSiteEnvDetails(out)
		return 0
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(data))
	return 0
}

func siteEnvDetailsOutput(siteID, env string, record map[string]any) map[string]any {
	out := cloneRecord(record)
	out["requested_site"] = siteID
	out["requested_env"] = env
	out["resolved_site"] = siteEnvDisplaySite(record)
	out["resolved_env"] = siteEnvName(record)
	out["resolved_target"] = siteProviderTarget(record)
	if phpVersion := sitePHPVersion(record); phpVersion != "" {
		out["php_version"] = phpVersion
	}
	if host := firstNonEmpty(mapStringAtPath(record, "ssh", "host"), mapStringAtPath(record, "kinsta", "ssh", "host")); host != "" {
		out["ssh_host"] = host
	}
	if port := firstNonEmpty(mapStringAtPath(record, "ssh", "port"), mapStringAtPath(record, "kinsta", "ssh", "port")); port != "" {
		out["ssh_port"] = port
	}
	if user := firstNonEmpty(mapStringAtPath(record, "ssh", "user"), mapStringAtPath(record, "kinsta", "ssh", "user")); user != "" {
		out["ssh_user"] = user
	}
	if command := firstNonEmpty(mapStringAtPath(record, "ssh", "command"), mapStringAtPath(record, "kinsta", "ssh", "command")); command != "" {
		out["ssh_command"] = command
	}
	return out
}

func printSiteEnvDetails(out map[string]any) {
	site := recordValueString(out["resolved_site"])
	env := recordValueString(out["resolved_env"])
	title := site
	if env != "" {
		title += ":" + env
	}
	if title != "" {
		fmt.Println(title)
		fmt.Println(strings.Repeat("─", len(title)))
	}
	printDetailRows([]detailRow{
		{label: "Site", value: site},
		{label: "Env", value: env},
		{label: "Provider", value: recordValueString(out["provider"])},
		{label: "Target", value: recordValueString(out["resolved_target"])},
		{label: "URL", value: firstRecordString(out, "url", "site_url", "home_url")},
		{label: "Path", value: firstRecordString(out, "path", "root", "document_root")},
		{label: "Branch", value: firstNonEmpty(firstRecordString(out, "branch"), mapStringAtPath(out, "kinsta", "branch"))},
		{label: "PHP", value: firstRecordString(out, "php_version")},
		{label: "Database", value: firstRecordString(out, "database", "db_name")},
	})
	requestedSite := recordValueString(out["requested_site"])
	if requestedSite != "" && site != "" && requestedSite != site {
		printDetailRows([]detailRow{{label: "Requested site", value: requestedSite}})
	}
	requestedEnv := recordValueString(out["requested_env"])
	if requestedEnv != "" && env != "" && requestedEnv != env {
		printDetailRows([]detailRow{{label: "Requested env", value: requestedEnv}})
	}
	providerRows := []detailRow{
		{label: "Kinsta site", value: firstRecordString(out, "kinsta_site_id")},
		{label: "Kinsta env", value: firstRecordString(out, "kinsta_environment_id")},
	}
	if hasDetailRows(providerRows) {
		fmt.Println()
		fmt.Println("Provider IDs")
		printIndentedDetailRows(providerRows, 2)
	}
	ssh := siteEnvSSHInfo(out)
	accessRows := []detailRow{
		{label: "SSH command", value: ssh.command()},
		{label: "Admin user", value: firstRecordString(out, "resolved_admin_user", "admin_user", "admin_username", "wp_admin_user", "wordpress_admin_user")},
		{label: "Admin pass", value: firstRecordString(out, "resolved_admin_password", "admin_password", "wp_admin_password", "wordpress_admin_password")},
	}
	if hasDetailRows(accessRows) {
		fmt.Println()
		fmt.Println("Access")
		printIndentedDetailRows(accessRows, 2)
	}
}

type detailRow struct {
	label string
	value string
}

func hasDetailRows(rows []detailRow) bool {
	for _, row := range rows {
		if strings.TrimSpace(row.value) != "" {
			return true
		}
	}
	return false
}

func printDetailRows(rows []detailRow) {
	printIndentedDetailRows(rows, 0)
}

func printIndentedDetailRows(rows []detailRow, indent int) {
	for _, line := range detailRowLines(rows, indent) {
		fmt.Println(line)
	}
}

func detailRowLines(rows []detailRow, indent int) []string {
	width := 0
	for _, row := range rows {
		if strings.TrimSpace(row.value) == "" {
			continue
		}
		if len(row.label) > width {
			width = len(row.label)
		}
	}
	if width == 0 {
		return nil
	}
	prefix := strings.Repeat(" ", indent)
	lines := []string{}
	for _, row := range rows {
		if strings.TrimSpace(row.value) == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s%-*s   %s", prefix, width, row.label, row.value))
	}
	return lines
}

type siteEnvSSHInfoValue struct {
	host       string
	port       string
	user       string
	sshCommand string
}

func siteEnvSSHInfo(record map[string]any) siteEnvSSHInfoValue {
	info := siteEnvSSHInfoValue{
		host:       firstRecordString(record, "ssh_host"),
		port:       firstRecordString(record, "ssh_port"),
		user:       firstRecordString(record, "ssh_user", "ssh_username"),
		sshCommand: firstRecordString(record, "ssh_command"),
	}
	if info.host == "" {
		info.host = firstNonEmpty(mapStringAtPath(record, "ssh", "host"), mapStringAtPath(record, "kinsta", "ssh", "host"))
	}
	if info.port == "" {
		info.port = firstNonEmpty(mapStringAtPath(record, "ssh", "port"), mapStringAtPath(record, "kinsta", "ssh", "port"))
	}
	if info.user == "" {
		info.user = firstNonEmpty(mapStringAtPath(record, "ssh", "user"), mapStringAtPath(record, "kinsta", "ssh", "user"))
	}
	if info.sshCommand == "" {
		info.sshCommand = firstNonEmpty(mapStringAtPath(record, "ssh", "command"), mapStringAtPath(record, "kinsta", "ssh", "command"))
	}
	target := mapMapAtPath(record, "resolved_target_record")
	if target != nil {
		if info.host == "" {
			info.host = serverSSHHost(target)
		}
		if info.port == "" {
			info.port = firstNonEmpty(mapStringAtPath(target, "ssh", "port"), firstRecordString(target, "ssh_port"))
		}
		if info.user == "" {
			info.user = serverSSHUser(target)
		}
	}
	return info
}

func (info siteEnvSSHInfoValue) command() string {
	if info.sshCommand != "" {
		return info.sshCommand
	}
	if info.host == "" {
		return ""
	}
	destination := info.host
	if info.user != "" {
		destination = info.user + "@" + destination
	}
	if info.port != "" {
		return "ssh " + destination + " -p " + info.port
	}
	return "ssh " + destination
}

func enrichSiteAdminCredentials(out, record map[string]any) error {
	if user := firstRecordString(record, "admin_user", "admin_username", "wp_admin_user", "wordpress_admin_user"); user != "" {
		out["resolved_admin_user"] = user
	} else {
		values, err := loadGlobalConfig()
		if err != nil {
			return err
		}
		out["resolved_admin_user"] = firstNonEmpty(values["default_wp_user"], "admin")
	}

	password, err := siteAdminPassword(record)
	if err != nil {
		if _, ok := err.(passwords.PasswordError); ok {
			return nil
		}
		return err
	}
	if password != "" {
		out["resolved_admin_password"] = password
	}
	return nil
}

func siteAdminPassword(record map[string]any) (string, error) {
	if password := firstRecordString(record, "admin_password", "wp_admin_password", "wordpress_admin_password"); password != "" {
		return password, nil
	}
	slug := sitePasswordSlug(record)
	if slug == "" {
		return "", nil
	}
	version := currentProjectPasswordVersionForSite(slug)
	return deriveProjectPassword(slug, "wp-admin", version)
}

func sitePasswordSlug(record map[string]any) string {
	if slug := firstRecordString(record, "password_scope", "admin_password_scope", "name", "site_name", "project", "project_slug", "wordpress_site"); slug != "" {
		return slug
	}
	siteID := siteEnvSiteID(record)
	target := siteProviderTarget(record)
	for _, suffix := range []string{"." + target, "-" + target} {
		if target != "" && strings.HasSuffix(siteID, suffix) {
			return strings.TrimSuffix(siteID, suffix)
		}
	}
	return siteID
}

func cachedSiteEnv(siteID, env string) (map[string]any, []map[string]any, error) {
	siteID, env = normalizeSiteEnvRequest(siteID, env)
	bundle, err := state.LoadStateBundle()
	if err != nil {
		return nil, nil, err
	}
	for _, candidate := range bundle.Sites {
		if siteEnvMatchesSite(candidate, siteID) && siteEnvMatchesEnv(candidate, env) {
			return candidate, bundle.Servers, nil
		}
	}
	return nil, bundle.Servers, nil
}

func cachedSiteTarget(targetRef string) (map[string]any, error) {
	targets, err := cachedTargets()
	if err != nil {
		return nil, err
	}
	return state.MatchingRecord(targets, targetRef), nil
}

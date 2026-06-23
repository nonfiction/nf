package cli

// Cached record, picker, site identity, and site-env helper functions.

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/nonfiction/nf/internal/state"
	"github.com/nonfiction/nf/internal/ui"
)

func recordPickerValue(kind string, record map[string]any) string {
	switch kind {
	case "server":
		return firstRecordString(record, "name", "slug", "hostname", "label", "linode_id", "id", "_state_key")
	case "site":
		return firstRecordString(record, "hostname", "name", "slug", "label", "id", "_state_key")
	default:
		return firstRecordString(record, "name", "slug", "hostname", "label", "id", "_state_key")
	}
}

func recordPickerLabel(kind string, record map[string]any) string {
	switch kind {
	case "server":
		label := serverSummary(record)
		if hostname := firstRecordString(record, "hostname"); hostname != "" && !strings.Contains(label, hostname) {
			label += " / " + hostname
		}
		return label
	case "site":
		parts := []string{}
		if name := siteSummary(record); name != "" {
			parts = append(parts, name)
		}
		if server := firstRecordString(record, "server_name", "server", "server_hostname", "server_label"); server != "" {
			parts = append(parts, "server "+server)
		}
		if status := recordValueString(record["status"]); status != "" {
			parts = append(parts, status)
		}
		return strings.Join(parts, " / ")
	default:
		return firstRecordString(record, "name", "slug", "hostname", "label", "id", "_state_key")
	}
}

func chooseRecord(kind, action string) (string, error) {
	stateKind := kind + "s"
	records, err := state.LoadStateRecords(stateKind)
	if err != nil {
		return "", err
	}
	if len(records) == 0 {
		return "", fmt.Errorf("No %ss found.", kind)
	}
	options := make([]ui.SelectOption, 0, len(records))
	for _, record := range records {
		value := recordPickerValue(kind, record)
		if value == "" {
			continue
		}
		label := recordPickerLabel(kind, record)
		if label == "" {
			label = value
		}
		options = append(options, ui.SelectOption{Label: label, Value: value})
	}
	if len(options) == 0 {
		return "", fmt.Errorf("No selectable %ss found.", kind)
	}
	return ui.Select(fmt.Sprintf("Choose a %s to %s", kind, action), options)
}

func chooseServerForDelete() (string, error) {
	return chooseRecord("server", "delete")
}

func cmdList(kind string) int {
	bundle, err := state.LoadStateBundle()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	switch kind {
	case "servers":
		return cmdListServers(bundle.Servers)
	case "sites":
		return cmdListSites(bundle.Sites, bundle.Servers)
	default:
		fmt.Fprintln(os.Stderr, "unsupported list kind")
		return 1
	}
}

func firstRecordValue(record map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := record[key]; ok && recordValueString(value) != "" {
			return value
		}
	}
	return ""
}

func firstRecordString(record map[string]any, keys ...string) string {
	return recordValueString(firstRecordValue(record, keys...))
}

func mapValueAtPath(value any, keys ...string) any {
	current := value
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		next, ok := m[key]
		if !ok {
			return nil
		}
		current = next
	}
	return current
}

func mapStringAtPath(value any, keys ...string) string {
	return recordValueString(mapValueAtPath(value, keys...))
}

func mapMapAtPath(value any, keys ...string) map[string]any {
	nested, _ := mapValueAtPath(value, keys...).(map[string]any)
	return nested
}

func cloneRecord(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func serverSSHHost(server map[string]any) string {
	if sshHost := mapStringAtPath(server, "linode", "ssh", "host"); sshHost != "" {
		return sshHost
	}
	sshHost := mapStringAtPath(server, "ssh", "host")
	if sshHost != "" {
		return sshHost
	}
	return firstRecordString(server, "ssh_host", "hostname")
}

func serverSSHUser(server map[string]any) string {
	sshUser := mapStringAtPath(server, "ssh", "user")
	if sshUser != "" {
		return sshUser
	}
	return firstRecordString(server, "ssh_user", "ssh_username")
}

func serverSummary(server map[string]any) string {
	name := firstRecordString(server, "name", "slug", "_state_key", "hostname", "label")
	id := firstRecordString(server, "provider_id", "id", "linode_id")
	provider := recordValueString(server["provider"])
	sshHost := serverSSHHost(server)
	parts := make([]string, 0, 4)
	if name != "" {
		parts = append(parts, name)
	}
	if id != "" {
		parts = append(parts, "id "+id)
	}
	if provider != "" && provider != "<nil>" {
		parts = append(parts, provider)
	}
	if sshHost != "" {
		if sshUser := serverSSHUser(server); sshUser != "" {
			parts = append(parts, "ssh "+sshUser+"@"+sshHost)
		} else {
			parts = append(parts, "ssh "+sshHost)
		}
	}
	return strings.Join(parts, " / ")
}

func siteSummary(site map[string]any) string {
	return firstRecordString(site, "_state_key", "hostname", "name", "slug", "label", "server_name")
}

func siteTargetName(site map[string]any) string {
	return firstRecordString(site, "_state_key", "target_name", "target", "hostname", "name", "slug", "label")
}

func siteProviderTarget(site map[string]any) string {
	if target := firstRecordString(site, "target", "server", "server_name", "server_id", "server_hostname", "server_label"); target != "" {
		return target
	}
	return recordValueString(site["provider"])
}

func siteServerReference(site map[string]any) string {
	return firstRecordString(site, "server", "server_id", "server_name", "server_hostname", "server_label")
}

func siteKinstaID(site map[string]any, key string) string {
	if value := mapStringAtPath(site, "kinsta", key); value != "" {
		return value
	}
	return firstRecordString(site, "kinsta_"+key)
}

func sitePHPVersion(site map[string]any) string {
	return firstNonEmpty(firstRecordString(site, "php_version"), mapStringAtPath(site, "kinsta", "php_version"), mapStringAtPath(site, "php", "version"), firstRecordString(site, "php"))
}

func targetPHPVersion(target map[string]any) string {
	if phpVersion := firstRecordString(target, "php_version"); phpVersion != "" {
		return phpVersion
	}
	if phpVersion := mapStringAtPath(target, "php", "version"); phpVersion != "" {
		return phpVersion
	}
	if _, ok := target["php"].(map[string]any); ok {
		return ""
	}
	return firstRecordString(target, "php")
}

func normalizedRecordString(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func siteEnvName(site map[string]any) string {
	return firstRecordString(site, "env", "environment", "environment_name", "environment_slug")
}

func siteEnvSiteID(site map[string]any) string {
	return firstRecordString(site, "site_id", "project_slug", "project", "site", "site_name", "wordpress_site")
}

func siteRecordName(site map[string]any) string {
	return firstRecordString(site, "project_slug", "project", "name", "site_name", "wordpress_site")
}

func siteCanonicalID(name, target string) string {
	name = strings.TrimSpace(name)
	target = strings.TrimSpace(target)
	if name == "" || target == "" {
		return name
	}
	return name + "." + target
}

func canonicalEnvID(siteID, env string) string {
	siteID = strings.TrimSpace(siteID)
	env = strings.TrimSpace(env)
	if siteID == "" || env == "" {
		return siteID
	}
	return siteID + ":" + env
}

func splitSiteEnvRef(ref string) (siteID, env string, ok bool) {
	left, right, found := strings.Cut(strings.TrimSpace(ref), ":")
	if !found || strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return "", "", false
	}
	return strings.TrimSpace(left), strings.TrimSpace(right), true
}

func envIDFileSlug(envID string) string {
	if siteID, env, ok := splitSiteEnvRef(envID); ok {
		return siteID + "." + env
	}
	return strings.TrimSpace(envID)
}

func normalizeSiteEnvRequest(siteID, env string) (string, string) {
	if parsedSiteID, parsedEnv, ok := splitSiteEnvRef(siteID); ok {
		return parsedSiteID, parsedEnv
	}
	env = strings.TrimSpace(env)
	if env == "" {
		env = "live"
	}
	return strings.TrimSpace(siteID), env
}

func siteRecordEnvID(site map[string]any) string {
	if envID := firstRecordString(site, "env_id"); envID != "" {
		return envID
	}
	return canonicalEnvID(siteRecordID(site), siteEnvName(site))
}

func siteRecordID(site map[string]any) string {
	if id := siteEnvSiteID(site); id != "" {
		return id
	}
	if id := siteCanonicalID(siteRecordName(site), siteProviderTarget(site)); id != "" {
		return id
	}
	return firstRecordString(site, "_state_key")
}

func siteEnvMatchesSite(site map[string]any, siteID string) bool {
	if parsedSiteID, _, ok := splitSiteEnvRef(siteID); ok {
		siteID = parsedSiteID
	}
	needle := normalizedRecordString(siteID)
	if needle == "" {
		return true
	}
	for _, candidate := range []string{siteRecordID(site), siteEnvSiteID(site), siteRecordName(site), siteKinstaID(site, "slug"), siteRecordEnvID(site), siteTargetName(site), siteSummary(site), firstRecordString(site, "hostname", "url", "site_url", "home_url")} {
		if normalizedRecordString(candidate) == needle {
			return true
		}
	}
	return false
}

func siteEnvMatchesEnv(site map[string]any, env string) bool {
	if _, parsedEnv, ok := splitSiteEnvRef(env); ok {
		env = parsedEnv
	}
	needle := normalizedRecordString(env)
	if needle == "" {
		return true
	}
	if normalizedRecordString(siteEnvName(site)) == needle {
		return true
	}
	if _, parsedEnv, ok := splitSiteEnvRef(siteRecordEnvID(site)); ok && normalizedRecordString(parsedEnv) == needle {
		return true
	}
	stateKey := normalizedRecordString(siteTargetName(site))
	return strings.HasPrefix(stateKey, needle+"-") || strings.HasSuffix(stateKey, "-"+needle)
}

func siteEnvDisplaySite(site map[string]any) string {
	if siteID := siteRecordID(site); siteID != "" {
		return siteID
	}
	return siteTargetName(site)
}

func siteListEnvOrder(env string) int {
	switch env {
	case "live":
		return 0
	case "staging":
		return 1
	default:
		return 2
	}
}

func sortedSiteListEnvs(envs map[string]bool) []string {
	names := make([]string, 0, len(envs))
	for env := range envs {
		if env != "" {
			names = append(names, env)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		left, right := siteListEnvOrder(names[i]), siteListEnvOrder(names[j])
		if left != right {
			return left < right
		}
		return names[i] < names[j]
	})
	return names
}

func enrichSiteOutput(out map[string]any, record map[string]any, servers []map[string]any) error {
	provider := strings.ToLower(strings.TrimSpace(recordValueString(record["provider"])))
	if provider == "linode" {
		targetRef := siteProviderTarget(record)
		targets, err := cachedTargets()
		if err != nil {
			return err
		}
		candidates := append([]map[string]any{}, servers...)
		candidates = append(candidates, targets...)
		target := state.MatchingRecord(candidates, targetRef)
		if target == nil {
			return ProjectError{Msg: fmt.Sprintf("Linode site %q references target %q, but no cached target matched. Run nf provider check linode.", siteSummary(record), targetRef)}
		}
		if err := validateTargetRecord(target); err != nil {
			return err
		}
		out["resolved_target_summary"] = serverSummary(target)
		out["resolved_target_record"] = target
	}
	if provider == "kinsta" {
		if value := siteKinstaID(record, "site_id"); value != "" {
			out["kinsta_site_id"] = value
		}
		if value := siteKinstaID(record, "environment_id"); value != "" {
			out["kinsta_environment_id"] = value
		}
	}
	return nil
}

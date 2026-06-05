package cli

// Target cache loading, reconciliation, validation, listing, and live status.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/state"
)

func projectDeployTargetAlias(metadata map[string]any, targetAlias string) (string, bool, error) {
	deploy := mapMapAtPath(metadata, "deploy")
	if deploy == nil {
		return "", false, nil
	}
	targets := mapMapAtPath(deploy, "targets")
	if targets == nil {
		return "", false, nil
	}
	value, ok := targets[targetAlias]
	if !ok {
		return "", false, nil
	}
	resolved, ok := value.(string)
	if !ok || strings.TrimSpace(resolved) == "" {
		return "", false, ProjectError{Msg: fmt.Sprintf(".nf/project.json deploy.targets.%s must be a string target alias", targetAlias)}
	}
	return strings.TrimSpace(resolved), true, nil
}

func projectRemoteAlias(metadata map[string]any, name string) (string, string, bool, error) {
	deploy := mapMapAtPath(metadata, "deploy")
	if deploy == nil {
		return "", "", false, nil
	}
	remotes := mapMapAtPath(deploy, "remotes")
	if remotes == nil {
		return "", "", false, nil
	}
	value, ok := remotes[name]
	if !ok {
		return "", "", false, nil
	}
	remote, ok := value.(map[string]any)
	if !ok || remote == nil {
		return "", "", false, ProjectError{Msg: fmt.Sprintf(".nf/project.json deploy.remotes.%s must be an object", name)}
	}
	siteID := strings.TrimSpace(recordValueString(remote["site_id"]))
	env := strings.TrimSpace(recordValueString(remote["env"]))
	if siteID == "" || env == "" {
		return "", "", false, ProjectError{Msg: fmt.Sprintf(".nf/project.json deploy.remotes.%s must include site_id and env", name)}
	}
	return siteID, env, true, nil
}

func resolveSiteTarget(requested string) (string, map[string]any, bool, bool, error) {
	resolved := strings.TrimSpace(requested)
	if resolved == "" {
		return "", nil, false, false, ProjectError{Msg: "site show requires a target or target alias"}
	}
	root, ok := currentGitRoot()
	if !ok {
		return resolved, nil, false, false, nil
	}
	projectFile := config.ProjectFile(root)
	projectFileExists := false
	if _, err := os.Stat(projectFile); err == nil {
		projectFileExists = true
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		return "", nil, false, false, err
	}
	if targetAlias, targetAliasFound, err := projectDeployTargetAlias(metadata, resolved); err != nil {
		return "", nil, false, false, err
	} else if targetAliasFound {
		return targetAlias, metadata, projectFileExists, true, nil
	}
	if remoteSiteID, remoteEnv, remoteFound, err := projectRemoteAlias(metadata, resolved); err != nil {
		return "", nil, false, false, err
	} else if remoteFound {
		return canonicalEnvID(remoteSiteID, remoteEnv), metadata, projectFileExists, true, nil
	}
	return resolved, metadata, projectFileExists, false, nil
}

func validateServerRecord(server map[string]any) error {
	if strings.TrimSpace(recordValueString(server["provider"])) == "" {
		return ProjectError{Msg: fmt.Sprintf("Server %q is missing provider.", serverSummary(server))}
	}
	return nil
}

func validateTargetRecord(target map[string]any) error {
	if strings.TrimSpace(recordValueString(target["provider"])) == "" {
		return ProjectError{Msg: fmt.Sprintf("Target %q is missing provider.", serverSummary(target))}
	}
	return nil
}

func cachedTargets() ([]map[string]any, error) {
	providers, err := state.LoadStateRecords("providers")
	if err != nil {
		return nil, err
	}
	if reconcileProviderTargetHandoffs(providers) {
		if err := state.SaveStateRecords("providers", providers); err != nil {
			return nil, err
		}
	}
	targets := providerTargetRecords(providers)
	if len(providers) > 0 {
		return targets, nil
	}
	return state.LoadStateRecords("servers")
}

func reconcileProviderTargetHandoffs(providers []map[string]any) bool {
	updated := false
	for _, provider := range providers {
		providerName := strings.ToLower(strings.TrimSpace(recordValueString(provider["provider"])))
		if providerName == "" {
			providerName = strings.ToLower(strings.TrimSpace(recordValueString(provider["_state_key"])))
		}
		if providerName != "linode" {
			continue
		}
		for _, target := range targetMaps(provider["targets"]) {
			if reconcileLinodeTargetHandoff(target) {
				updated = true
			}
		}
	}
	return updated
}

func reconcileLinodeTargetHandoff(target map[string]any) bool {
	status := strings.ToLower(strings.TrimSpace(recordValueString(target["status"])))
	phase := strings.ToLower(strings.TrimSpace(recordValueString(target["phase"])))
	if status != "provisioning" {
		return false
	}
	switch phase {
	case "dns_configured", "tls_configured":
	default:
		return false
	}
	healthURL := targetHealthURL(target)
	if healthURL == "" || !targetHealthReady(healthURL, target) {
		return false
	}
	now := time.Now().UTC().Format(time.RFC3339)
	target["status"] = "provisioned"
	target["phase"] = "complete"
	target["updated_at"] = now
	return true
}

func targetHealthURL(target map[string]any) string {
	healthURL := strings.TrimSpace(recordValueString(target["health_url"]))
	if healthURL == "" {
		hostname := firstRecordString(target, "hostname", "host")
		if hostname == "" {
			return ""
		}
		healthURL = "https://" + hostname
	}
	healthURL = strings.TrimRight(healthURL, "/")
	if strings.HasSuffix(healthURL, "/healthz") {
		return healthURL
	}
	return healthURL + "/healthz"
}

func targetHealthReady(healthURL string, target map[string]any) bool {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(healthURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return false
	}
	text := strings.ToLower(string(body))
	if !strings.Contains(text, "ready") {
		return false
	}
	name := strings.ToLower(firstRecordString(target, "target_name", "name", "label"))
	hostname := strings.ToLower(firstRecordString(target, "hostname", "host"))
	return (name != "" && strings.Contains(text, name)) || (hostname != "" && strings.Contains(text, hostname))
}

func providerTargetRecords(providers []map[string]any) []map[string]any {
	targets := make([]map[string]any, 0)
	for _, provider := range providers {
		providerName := recordValueString(provider["provider"])
		if providerName == "" {
			providerName = recordValueString(provider["_state_key"])
		}
		for _, target := range targetMaps(provider["targets"]) {
			record := cloneRecord(target)
			if recordValueString(record["provider"]) == "" && providerName != "" {
				record["provider"] = providerName
			}
			if strings.EqualFold(providerName, "kinsta") && recordValueString(record["status"]) == "" {
				if status := recordValueString(provider["status"]); status != "" {
					record["status"] = status
				}
			}
			targets = append(targets, record)
		}
	}
	return targets
}

func targetMaps(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if record, ok := item.(map[string]any); ok {
				out = append(out, record)
			}
		}
		return out
	default:
		return nil
	}
}

func validateSiteRecord(site map[string]any) error {
	provider := strings.ToLower(strings.TrimSpace(recordValueString(site["provider"])))
	if provider == "" {
		return ProjectError{Msg: fmt.Sprintf("Site %q is missing provider.", siteSummary(site))}
	}
	if provider == "linode" && siteProviderTarget(site) == "" {
		return ProjectError{Msg: fmt.Sprintf("Linode site %q is missing a target reference.", siteSummary(site))}
	}
	return nil
}

func cmdListServers(records []map[string]any) int {
	if len(records) == 0 {
		fmt.Println("No servers found.")
		return 0
	}
	return cmdListTargets(records)
}

func cmdListTargets(records []map[string]any) int {
	if len(records) == 0 {
		fmt.Println("No targets found.")
		return 0
	}
	rows := [][]string{{"target", "provider", "hostname", "status"}}
	for _, record := range records {
		rows = append(rows, []string{
			firstRecordString(record, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id"),
			recordValueString(record["provider"]),
			firstRecordString(record, "hostname", "host", "public_ipv4", "ipv4", "ip"),
			targetLiveStatus(record),
		})
	}
	fmt.Println(formatTable(rows))
	return 0
}

func targetLiveStatus(record map[string]any) string {
	switch strings.ToLower(strings.TrimSpace(recordValueString(record["provider"]))) {
	case "kinsta":
		return kinstaTargetLiveStatus(record)
	case "linode":
		return linodeTargetLiveStatus(record)
	default:
		return recordValueString(record["status"])
	}
}

func kinstaTargetLiveStatus(record map[string]any) string {
	result, err := providerCheckKinstaFn()
	if err != nil {
		return "unreachable"
	}
	for _, target := range targetMaps(result.Record["targets"]) {
		if recordValueString(target["id"]) == "kinsta" || strings.EqualFold(recordValueString(target["provider"]), "kinsta") {
			if status := recordValueString(target["status"]); status != "" {
				return status
			}
		}
	}
	if status := recordValueString(result.Record["status"]); status != "" {
		return status
	}
	return firstNonEmpty(recordValueString(record["status"]), "active")
}

func linodeTargetLiveStatus(record map[string]any) string {
	if targetSSHReachableFn(record) {
		return "reachable"
	}
	if serverSSHHost(record) != "" {
		return "ssh unavailable"
	}
	return recordValueString(record["status"])
}

func targetSSHReachable(record map[string]any) bool {
	host := serverSSHHost(record)
	if host == "" {
		return false
	}
	user := serverSSHUser(record)
	destination := host
	if user != "" {
		destination = user + "@" + host
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=3", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", destination, "true")
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

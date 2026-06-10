package cli

// Project remote commands backed by nf.json.

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

func cmdRemoteAdd(name, envRef string) int {
	name = strings.TrimSpace(name)
	siteID, env, ok := splitSiteEnvRef(strings.TrimSpace(envRef))
	if name == "" {
		fmt.Fprintln(os.Stderr, "remote add requires a non-empty name")
		return 1
	}
	if !ok || siteID == "" || env == "" {
		fmt.Fprintln(os.Stderr, "remote add requires an env ref like site.target:env")
		return 1
	}
	root, ok := currentGitRoot()
	if !ok {
		fmt.Fprintln(os.Stderr, "remote add requires a .git repository above the current directory")
		return 1
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	remotes, err := projectRemotes(metadata, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	record, _, err := cachedSiteEnv(siteID, env)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if record == nil {
		fmt.Fprintf(os.Stderr, "No cached remote env matched site %q env %q. Run nf site refresh after target cache is current, or update the local state cache.\n", siteID, env)
		return 1
	}
	if err := validateSiteRecord(record); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	remotes[name] = canonicalEnvID(siteID, env)
	if err := saveProjectMetadata(root, metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Added remote %s -> %s\n", name, canonicalEnvID(siteID, env))
	return 0
}

func cmdRemoteRemove(name string) int {
	name = strings.TrimSpace(name)
	if name == "" {
		fmt.Fprintln(os.Stderr, "remote remove requires a non-empty name")
		return 1
	}
	root, ok := currentGitRoot()
	if !ok {
		fmt.Fprintln(os.Stderr, "remote remove requires a .git repository above the current directory")
		return 1
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	remotes, err := projectRemotes(metadata, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if remotes == nil {
		fmt.Fprintf(os.Stderr, "No remote named %q.\n", name)
		return 1
	}
	if _, ok := remotes[name]; !ok {
		fmt.Fprintf(os.Stderr, "No remote named %q.\n", name)
		return 1
	}
	delete(remotes, name)
	if err := saveProjectMetadata(root, metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Removed remote %s\n", name)
	return 0
}

func cmdRemoteShow(name string) int {
	name = strings.TrimSpace(name)
	if name == "" {
		fmt.Fprintln(os.Stderr, "remote show requires a non-empty name")
		return 1
	}
	root, ok := currentGitRoot()
	if !ok {
		fmt.Fprintln(os.Stderr, "remote show requires a .git repository above the current directory")
		return 1
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	siteID, remoteEnv, ok, err := projectRemoteAlias(metadata, name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "No remote named %q in nf.json remotes.\n", name)
		return 1
	}
	envID := canonicalEnvID(siteID, remoteEnv)
	lines := []string{name, strings.Repeat("─", len(name))}
	rows := []detailRow{{label: "Env", value: envID}}
	record, _, err := cachedSiteEnv(siteID, remoteEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if record == nil {
		rows = append(rows, detailRow{label: "Cache", value: "no matching cached remote env"})
		lines = append(lines, detailRowLines(rows, 0)...)
		fmt.Println(strings.Join(lines, "\n"))
		return 0
	}
	if err := validateSiteRecord(record); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(record["provider"])))
	rows = append(rows, detailRow{label: "Provider", value: provider})
	targetName := siteTargetName(record)
	if provider == "linode" && siteServerReference(record) != "" {
		targetName = siteServerReference(record)
	}
	rows = append(rows, detailRow{label: "Target", value: targetName})
	accessRows := []detailRow{
		{label: "URL", value: firstRecordString(record, "url", "site_url", "home_url", "hostname")},
	}
	if provider == "linode" {
		targetRef := siteProviderTarget(record)
		targetRecord, err := cachedSiteTarget(targetRef)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if targetRecord == nil {
			fmt.Fprintf(os.Stderr, "Linode site %q references target %q, but no cached target matched. Run nf provider check linode.\n", siteSummary(record), targetRef)
			return 1
		}
		rows = append(rows, detailRow{label: "Target ID", value: firstRecordString(targetRecord, "provider_id", "id", "linode_id")})
		accessRows = append(accessRows, detailRow{label: "SSH", value: remoteTargetSSHCommand(targetRecord)})
	}
	lines = append(lines, detailRowLines(rows, 0)...)
	if hasDetailRows(accessRows) {
		lines = append(lines, "", "Access")
		lines = append(lines, detailRowLines(accessRows, 2)...)
	}
	fmt.Println(strings.Join(lines, "\n"))
	return 0
}

func remoteTargetSSHCommand(target map[string]any) string {
	host := serverSSHHost(target)
	if host == "" {
		return ""
	}
	if user := serverSSHUser(target); user != "" {
		return "ssh " + user + "@" + host
	}
	return "ssh " + host
}

func cmdRemoteList() int {
	root, ok := currentGitRoot()
	if !ok {
		fmt.Fprintln(os.Stderr, "remote list requires a .git repository above the current directory")
		return 1
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	remotes, err := projectRemotes(metadata, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(remotes) == 0 {
		fmt.Println("No remotes found.")
		return 0
	}
	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := [][]string{{"remote", "env"}}
	for _, name := range names {
		siteID, env, ok, err := projectRemoteAlias(metadata, name)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !ok {
			fmt.Fprintf(os.Stderr, "No remote named %q in nf.json remotes.\n", name)
			return 1
		}
		rows = append(rows, []string{name, canonicalEnvID(siteID, env)})
	}
	fmt.Println(formatTable(rows))
	return 0
}

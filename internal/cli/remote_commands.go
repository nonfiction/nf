package cli

// Project remote commands backed by .nf/project.json.

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
	remotes[name] = map[string]any{"site_id": siteID, "env": env}
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
		fmt.Fprintf(os.Stderr, "No remote named %q in .nf/project.json deploy.remotes.\n", name)
		return 1
	}
	envID := canonicalEnvID(siteID, remoteEnv)
	fmt.Printf("Remote: %s\n", name)
	fmt.Printf("Env: %s\n", envID)
	record, _, err := cachedSiteEnv(siteID, remoteEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if record == nil {
		fmt.Println("Cache: no matching cached remote env")
		return 0
	}
	if err := validateSiteRecord(record); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(record["provider"])))
	fmt.Printf("Provider: %s\n", provider)
	target := siteTargetName(record)
	if provider == "linode" && siteServerReference(record) != "" {
		target = siteServerReference(record)
	}
	fmt.Printf("Target: %s\n", target)
	if url := firstRecordString(record, "url", "site_url", "home_url", "hostname"); url != "" {
		fmt.Printf("URL: %s\n", url)
	}
	if provider == "linode" {
		targetRef := siteProviderTarget(record)
		target, err := cachedSiteTarget(targetRef)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if target == nil {
			fmt.Fprintf(os.Stderr, "Linode site %q references target %q, but no cached target matched. Run nf provider check linode.\n", siteSummary(record), targetRef)
			return 1
		}
		fmt.Printf("Target record: %s\n", serverSummary(target))
	}
	return 0
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
		remote, ok := remotes[name].(map[string]any)
		if !ok || remote == nil {
			fmt.Fprintf(os.Stderr, ".nf/project.json deploy.remotes.%s must be an object\n", name)
			return 1
		}
		siteID := strings.TrimSpace(recordValueString(remote["site_id"]))
		env := strings.TrimSpace(recordValueString(remote["env"]))
		if siteID == "" || env == "" {
			fmt.Fprintf(os.Stderr, ".nf/project.json deploy.remotes.%s must include site_id and env\n", name)
			return 1
		}
		rows = append(rows, []string{name, canonicalEnvID(siteID, env)})
	}
	fmt.Println(formatTable(rows))
	return 0
}

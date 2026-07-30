package cli

// Shell completion scripts and completion candidate discovery.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nonfiction/nf/internal/state"
)

func runCompletion(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runCompletionHelp()
	}
	if len(argv) != 1 {
		fmt.Fprintln(os.Stderr, "completion takes exactly one shell: bash or zsh")
		return 1
	}
	switch argv[0] {
	case "bash":
		fmt.Print(bashCompletionScript())
		return 0
	case "zsh":
		fmt.Print(zshCompletionScript())
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unsupported completion shell %q; use bash or zsh\n", argv[0])
		return 1
	}
}

func bashCompletionScript() string {
	return `# bash completion for nf
_nf_completion() {
  local cur nf_command
  cur="${COMP_WORDS[COMP_CWORD]}"
  nf_command="$(command -v nf)"
  COMPREPLY=( $(compgen -W "$("$nf_command" __complete -- "${COMP_WORDS[@]:1:$COMP_CWORD}")" -- "$cur") )
}
complete -F _nf_completion nf
`
}

func zshCompletionScript() string {
	return `#compdef nf
# zsh completion for nf
_nf() {
  local -a args completions
  local i nf_command
  args=()
  for (( i = 2; i <= CURRENT; i++ )); do
    args+=("${words[i]}")
  done
  if [[ -n "$NF_COMPLETION_DEBUG" ]]; then
    print -ru2 -- "nf completion debug: CURRENT=$CURRENT PREFIX=$PREFIX SUFFIX=$SUFFIX words=(${words[*]}) args=(${args[*]})"
  fi
  nf_command="$(command -v nf)"
  completions=( ${(f)"$("$nf_command" __complete -- "${args[@]}")"} )
  compadd -Q -U -S ' ' -- "${completions[@]}"
}
compctl -d nf 2>/dev/null || true
compdef _nf nf
`
}

func runComplete(argv []string) int {
	if len(argv) > 0 && argv[0] == "--" {
		argv = argv[1:]
	}
	for _, value := range completeCandidates(argv) {
		fmt.Println(value)
	}
	return 0
}

func completeCandidates(argv []string) []string {
	prefix := ""
	args := append([]string{}, argv...)
	if len(args) > 0 {
		prefix = args[len(args)-1]
		args = args[:len(args)-1]
	}
	candidates := completeContextCandidates(args)
	return filterCompletionCandidates(candidates, prefix)
}

func completeContextCandidates(args []string) []string {
	if len(args) == 0 {
		return rootCompletionCandidates()
	}
	switch args[0] {
	case "help":
		return rootCompletionCandidates()
	case "completion":
		return []string{"bash", "zsh"}
	case "version":
		return []string{"--short", "help"}
	case "provider":
		return providerCompletionCandidates(args[1:])
	case "target":
		return targetCompletionCandidates(args[1:])
	case "site":
		return siteCompletionCandidates(args[1:])
	case "refresh":
		return []string{"help"}
	case "domain":
		return siteDomainCompletionCandidates(args[1:])
	case "config":
		return configCompletionCandidates(args[1:])
	case "password":
		return passwordCompletionCandidates(args[1:])
	case "remote":
		return remoteCompletionCandidates(args[1:])
	case "plugin":
		return pluginCompletionCandidates(args[1:])
	case "env":
		return envCompletionCandidates(args[1:])
	case "theme":
		return themeCompletionCandidates(args[1:])
	case "alias":
		return aliasCompletionCandidates(args[1:])
	case "define":
		return defineCompletionCandidates(args[1:])
	default:
		return nil
	}
}

func configCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"show", "get", "set", "unset", "keys", "edit", "init", "help"}
	}
	switch args[0] {
	case "get", "set", "unset":
		if len(args) == 1 {
			return configKeyNames()
		}
		if args[0] == "set" && len(args) == 2 {
			return configSetValueCompletionCandidates(args[1])
		}
	case "init":
		return []string{"--non-interactive"}
	}
	return nil
}

func configSetValueCompletionCandidates(key string) []string {
	spec, ok := lookupConfigKey(key)
	if !ok || spec.Sensitive {
		return nil
	}
	values, err := loadGlobalConfig()
	if err != nil {
		return nil
	}
	value := spec.promptDefault(values)
	if value == "" {
		return nil
	}
	return []string{value}
}

func passwordCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"show-salt", "set-salt", "age-identity", "age-recipient", "derive", "help"}
	}
	if args[0] != "derive" {
		return nil
	}
	if len(args) == 1 {
		return append(passwordDeriveScopeCandidates(), "--password-version")
	}
	if args[len(args)-1] == "--password-version" {
		return []string{"0"}
	}
	if len(args) == 2 {
		return append(passwordDeriveIdentityCompletionNames(args[1]), "--password-version")
	}
	return []string{"--password-version"}
}

func passwordDeriveIdentityCompletionNames(scope string) []string {
	switch strings.TrimSpace(scope) {
	case passwordDeriveScopeWPAdmin, passwordDeriveScopeMySQL, passwordDeriveScopeBasicAuth:
		values := cachedSitePasswordSlugCompletionNames()
		if root, ok := currentNFProjectRoot(); ok {
			if metadata, err := loadProjectMetadataOrError(root); err == nil {
				values = append(values, metadata.Project.Slug)
			}
		}
		return uniqueSortedStrings(values)
	case passwordDeriveScopeLinodeRoot, passwordDeriveScopeDBAdmin:
		return cachedLinodeTargetHostCompletionNames()
	default:
		return nil
	}
}

func rootCompletionCandidates() []string {
	candidates := []string{"init", "provider", "target", "site", "refresh", "domain", "config", "password", "completion", "version", "help"}
	if projectContextAvailable() {
		candidates = append(candidates, "remote", "plugin", "env", "theme", "alias", "define")
	}
	sort.Strings(candidates)
	return candidates
}

func providerCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"list", "ls", "check", "show", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "check", "show":
		return []string{"dnsimple", "kinsta", "linode", "--json"}
	default:
		return nil
	}
}

func targetCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"list", "ls", "show", "password", "refresh", "add", "remove", "rm", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "add":
		if len(args) == 1 {
			return []string{"linode"}
		}
		return targetAddFlagCandidates()
	case "show":
		return append(cachedTargetCompletionNames(), "--json")
	case "password":
		return append(cachedLinodeTargetCompletionNames(), "--root", "--db")
	case "remove":
		return append(cachedTargetCompletionNames(), "--dry-run", "--execute", "--yes", "--non-interactive")
	default:
		return nil
	}
}

func targetAddFlagCandidates() []string {
	return []string{"--region", "--type", "--image", "--ubuntu-version", "--firewall", "--firewall-id", "--db-user", "--ssh-user", "--ssh-key-source", "--ssh-key-label", "--ssh-key-id", "--all-linode-ssh-keys", "--ssh-public-key-file", "--write-cloud-init", "--show-cloud-init", "--wait", "--no-wait", "--execute", "--yes", "--non-interactive", "--dry-run"}
}

func siteCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"list", "ls", "show", "shell", "sh", "wp", "export", "snapshot", "password", "basicauth", "refresh", "cache", "repair", "add", "staging", "remove", "rm", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "list":
		for _, arg := range args[1:] {
			if arg == "--envs" {
				return cachedSiteCompletionNames()
			}
		}
		return []string{"--envs", "--refresh"}
	case "show", "cache":
		return cachedSiteAndEnvCompletionNames()
	case "repair":
		return append(cachedSiteAndEnvCompletionNames(), "--project-slug", "--dry-run", "--execute", "--yes", "--non-interactive")
	case "shell", "wp":
		return cachedSiteEnvCompletionNames()
	case "export":
		return append(cachedSiteEnvCompletionNames(), "--output", "--dry-run")
	case "snapshot":
		if len(args) == 1 {
			values := append([]string{"list", "ls", "remove", "rm", "prune", "help"}, cachedSiteEnvCompletionNames()...)
			return append(values, "--output", "--dry-run")
		}
		if len(args) >= 2 && (args[1] == "remove" || args[1] == "rm") {
			return append(remoteSnapshotCompletionNames(), "--yes")
		}
		if len(args) >= 2 && args[1] == "prune" {
			return []string{"--keep", "--dry-run", "--yes"}
		}
		return []string{"--output", "--dry-run"}
	case "password":
		return append(cachedSiteAndEnvCompletionNames(), "--wp", "--db", "--basicauth")
	case "basicauth":
		return siteBasicAuthCompletionCandidates(args[1:])
	case "staging":
		return siteStagingCompletionCandidates(args[1:])
	case "remove":
		return cachedSiteCompletionNames()
	case "add":
		if len(args) == 1 {
			return cachedTargetCompletionNames()
		}
		return []string{"--with-staging", "--password-version", "--kinsta-slug", "--region", "--php", "--dry-run", "--execute", "--yes", "--non-interactive"}
	default:
		return nil
	}
}

func siteDomainCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"list", "ls", "add", "check", "primary", "remove", "rm", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "list", "ls":
		return append(cachedSiteAndEnvCompletionNames(), projectRemoteCompletionNames()...)
	case "add", "primary", "check", "remove":
		flags := []string{"--proxy", "--no-proxy", "--dry-run", "--execute", "--yes", "--non-interactive"}
		if args[0] == "check" {
			flags = []string{"--proxy", "--no-proxy", "--non-interactive"}
		} else if args[0] == "remove" {
			flags = []string{"--proxy", "--no-proxy", "--delete-cert", "--dry-run", "--execute", "--yes", "--non-interactive"}
		}
		if args[0] == "primary" {
			flags = append(flags, "--search-replace", "--no-search-replace", "--force", "--wait-timeout", "--wait-interval")
		}
		if len(args) > 1 {
			switch args[len(args)-1] {
			case "--proxy":
				return []string{"cloudflare"}
			}
		}
		if len(args) == 1 {
			values := append(cachedSiteEnvCompletionNames(), projectRemoteCompletionNames()...)
			values = append(values, flags...)
			return uniqueSortedStrings(values)
		}
		return flags
	default:
		return nil
	}
}

func siteStagingCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"status", "add", "remove", "rm", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "status":
		if len(args) == 1 {
			return cachedSiteCompletionNames()
		}
	case "add":
		if len(args) >= 1 {
			return siteStagingLifecycleCompletionCandidates()
		}
	case "remove":
		if len(args) >= 1 {
			return siteStagingLifecycleCompletionCandidates()
		}
	}
	return nil
}

func siteStagingLifecycleCompletionCandidates() []string {
	values := append(cachedSiteCompletionNames(), "--dry-run", "--execute", "--yes", "--non-interactive")
	return uniqueSortedStrings(values)
}

func siteBasicAuthCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"status", "enable", "disable", "password", "help"}
	}
	switch args[0] {
	case "password":
		return cachedSiteCompletionNames()
	case "status":
		if len(args) == 1 {
			return cachedSiteEnvCompletionNames()
		}
	case "enable", "disable":
		if len(args) == 1 {
			return cachedSiteEnvCompletionNames()
		}
		if len(args) == 2 {
			return []string{"--dry-run", "--execute", "--yes", "--non-interactive"}
		}
	}
	return nil
}

func remoteCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"list", "ls", "show", "add", "remove", "rm", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "show", "remove":
		return projectRemoteCompletionNames()
	case "add":
		if len(args) == 2 {
			return cachedSiteEnvCompletionNames()
		}
	}
	return nil
}

func envCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"show", "password", "up", "down", "logs", "shell", "sh", "wp", "snapshot", "import", "pull", "push", "reset", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "password":
		return append(projectRemoteCompletionNames(), "--wp", "--db", "--basicauth")
	case "up", "reset":
		return []string{"--rebuild"}
	case "logs":
		return projectRemoteCompletionNames()
	case "pull", "push":
		return append(projectRemoteCompletionNames(), "--dry-run", "--execute", "--yes", "--non-interactive")
	case "shell":
		return projectRemoteCompletionNames()
	case "snapshot":
		return envSnapshotCompletionCandidates(args[1:])
	case "import":
		return []string{"--db", "--source-url", "--name", "--dry-run", "--yes"}
	default:
		return nil
	}
}

func pluginCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"list", "ls", "add", "remove", "rm", "status", "diff", "install", "cache", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	if args[0] == "add" {
		return []string{"--source", "--manual", "--note", "--no-activate", "--no-auto-update"}
	}
	if args[0] == "remove" {
		return projectPluginCompletionNames()
	}
	if args[0] == "install" {
		return append(projectRemoteCompletionNames(), "--dry-run", "--yes")
	}
	if args[0] == "status" || args[0] == "diff" {
		return projectRemoteCompletionNames()
	}
	if args[0] == "cache" {
		return pluginCacheCompletionCandidates(args[1:])
	}
	return nil
}

func pluginCacheCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"add", "save", "list", "ls", "show", "remove", "rm", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "save", "show", "remove":
		return projectPluginCompletionNames()
	default:
		return nil
	}
}

func envSnapshotCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"list", "ls", "add", "import", "use", "remove", "rm", "prune", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "import":
		return append(remoteSnapshotCompletionNames(), "--name")
	case "use":
		candidates := append(envSnapshotCompletionNames(), remoteSnapshotCompletionNames()...)
		return append(candidates, "--remote", "--name", "--yes")
	case "remove":
		return envSnapshotCompletionNames()
	case "prune":
		return []string{"--keep", "--dry-run", "--yes"}
	default:
		return nil
	}
}

func themeCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		candidates := []string{"list", "ls", "add", "activate", "remove", "rm", "status", "diff", "install", "cache", "tasks", "package", "deploy", "rollback", "help"}
		candidates = append(candidates, projectTaskCompletionNames()...)
		return uniqueSortedStrings(candidates)
	}
	args[0] = cliCommandAlias(args[0])
	if args[0] == "add" {
		return []string{"--source", "--path", "--auto-update", "--note"}
	}
	if args[0] == "activate" || args[0] == "remove" {
		return projectThemeCompletionNames()
	}
	if args[0] == "install" {
		return append(projectRemoteCompletionNames(), "--dry-run", "--yes")
	}
	if args[0] == "status" || args[0] == "diff" {
		return projectRemoteCompletionNames()
	}
	if args[0] == "cache" {
		return themeCacheCompletionCandidates(args[1:])
	}
	switch args[0] {
	case "package":
		return []string{"--dry-run", "--source", "--output"}
	case "deploy":
		return append(projectRemoteCompletionNames(), "--dry-run", "--restart")
	case "rollback":
		return append(projectRemoteCompletionNames(), "--dry-run")
	default:
		return nil
	}
}

func themeCacheCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"add", "save", "list", "ls", "show", "remove", "rm", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "save", "show", "remove":
		return projectThemeCompletionNames()
	default:
		return nil
	}
}

func aliasCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"list", "ls", "status", "sync", "add", "remove", "rm", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "status", "sync":
		return projectRemoteCompletionNames()
	case "remove":
		return projectAliasCompletionNames()
	default:
		return nil
	}
}

func defineCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"list", "ls", "status", "sync", "add", "remove", "rm", "migrate-env", "rekey", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "status", "sync":
		return projectRemoteCompletionNames()
	case "remove":
		return append(projectDefineCompletionNames(), "--for")
	case "add":
		if len(args) > 1 && args[len(args)-1] == "--for" {
			return defineSelectorCompletionNames()
		}
		return []string{"--secret", "--secret-stdin", "--for"}
	case "migrate-env":
		return []string{"--dry-run", "--delete-source"}
	case "rekey":
		return []string{"--dry-run", "--add-recipient"}
	default:
		return nil
	}
}

func cachedTargetCompletionNames() []string {
	targets, err := completionCachedTargets()
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(targets))
	for _, target := range targets {
		values = append(values, firstRecordString(target, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id"))
	}
	return uniqueSortedStrings(values)
}

func cachedLinodeTargetCompletionNames() []string {
	targets, err := completionCachedTargets()
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(targets))
	for _, target := range targets {
		if !strings.EqualFold(strings.TrimSpace(recordValueString(target["provider"])), "linode") {
			continue
		}
		values = append(values, firstRecordString(target, "_state_key", "target_name", "target", "name", "slug", "hostname", "label", "id"))
	}
	return uniqueSortedStrings(values)
}

func cachedLinodeTargetHostCompletionNames() []string {
	targets, err := completionCachedTargets()
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(targets))
	for _, target := range targets {
		if !strings.EqualFold(strings.TrimSpace(recordValueString(target["provider"])), "linode") {
			continue
		}
		values = append(values, firstRecordString(target, "hostname", "host"))
	}
	return uniqueSortedStrings(values)
}

func completionCachedTargets() ([]map[string]any, error) {
	providers, err := state.LoadStateRecords("providers")
	if err != nil {
		return nil, err
	}
	if len(providers) > 0 {
		return providerTargetRecords(providers), nil
	}
	return state.LoadStateRecords("servers")
}

func cachedSiteCompletionNames() []string {
	sites, err := state.LoadStateRecords("sites")
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(sites))
	for _, site := range sites {
		values = append(values, siteRecordID(site))
	}
	return uniqueSortedStrings(values)
}

func cachedSitePasswordSlugCompletionNames() []string {
	sites, err := state.LoadStateRecords("sites")
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(sites))
	for _, site := range sites {
		values = append(values, sitePasswordSlug(site))
	}
	return uniqueSortedStrings(values)
}

func cachedSiteEnvCompletionNames() []string {
	sites, err := state.LoadStateRecords("sites")
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(sites))
	for _, site := range sites {
		values = append(values, siteRecordEnvID(site))
	}
	return uniqueSortedStrings(values)
}

func cachedSiteAndEnvCompletionNames() []string {
	values := append(cachedSiteCompletionNames(), cachedSiteEnvCompletionNames()...)
	return uniqueSortedStrings(values)
}

func projectRemoteCompletionNames() []string {
	root, ok := currentNFProjectRoot()
	if !ok {
		return nil
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		return nil
	}
	remotes, err := projectRemotes(metadata, false)
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(remotes))
	for name := range remotes {
		values = append(values, name)
	}
	return uniqueSortedStrings(values)
}

func projectTaskCompletionNames() []string {
	root, ok := currentNFProjectRoot()
	if !ok {
		return nil
	}
	tasks, err := loadProjectTasks(root)
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(tasks))
	for name := range tasks {
		values = append(values, name)
	}
	return uniqueSortedStrings(values)
}

func projectPluginCompletionNames() []string {
	root, ok := currentNFProjectRoot()
	if !ok {
		return nil
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		return nil
	}
	plugins, err := loadWordPressPluginSpecs(metadata)
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(plugins))
	for _, plugin := range plugins {
		values = append(values, plugin.Slug)
	}
	return uniqueSortedStrings(values)
}

func projectThemeCompletionNames() []string {
	root, ok := currentNFProjectRoot()
	if !ok {
		return nil
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		return nil
	}
	themes, err := loadWordPressThemeSpecs(metadata)
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(themes))
	for _, theme := range themes {
		values = append(values, theme.Slug)
	}
	return uniqueSortedStrings(values)
}

func projectDefineCompletionNames() []string {
	root, ok := currentNFProjectRoot()
	if !ok {
		return nil
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		return nil
	}
	defines, _, err := configuredDefineArray(metadata, false)
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(defines))
	for _, raw := range defines {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		values = append(values, recordValueString(item["name"]))
	}
	return uniqueSortedStrings(values)
}

func defineSelectorCompletionNames() []string {
	values := []string{"local"}
	values = append(values, projectRemoteCompletionNames()...)
	return uniqueSortedStrings(values)
}

func envSnapshotCompletionNames() []string {
	root, ok := currentNFProjectRoot()
	if !ok {
		return nil
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		return nil
	}
	cfg, ok := loadEnvConfig(root, metadata)
	if !ok {
		return nil
	}
	records, err := loadEnvSnapshots(cfg)
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(records))
	for _, record := range records {
		values = append(values, firstNonEmpty(record.Metadata.Name, filepath.Base(record.Directory)))
	}
	return uniqueSortedStrings(values)
}

func filterCompletionCandidates(candidates []string, prefix string) []string {
	values := make([]string, 0, len(candidates))
	for _, candidate := range uniqueStrings(candidates) {
		if strings.HasPrefix(candidate, prefix) {
			values = append(values, candidate)
		}
	}
	return values
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	unique := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || strings.ContainsAny(value, " \t\n\r") {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func uniqueSortedStrings(values []string) []string {
	unique := uniqueStrings(values)
	sort.Strings(unique)
	return unique
}

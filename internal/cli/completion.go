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
	case "config":
		return []string{"init", "show", "set-base-domain", "set-default-wp-email", "set-default-wp-user", "set-basicauth-default-user", "set-adminer-default-user", "set-kinsta-default-region", "set-kinsta-default-php", "set-linode-default-region", "set-linode-default-type", "set-linode-default-image", "set-linode-default-user", "help"}
	case "password":
		return []string{"show-salt", "set-salt", "derive", "help"}
	case "remote":
		return remoteCompletionCandidates(args[1:])
	case "env":
		return envCompletionCandidates(args[1:])
	case "theme":
		return themeCompletionCandidates(args[1:])
	case "public":
		return publicCompletionCandidates(args[1:])
	default:
		return nil
	}
}

func rootCompletionCandidates() []string {
	candidates := []string{"init", "provider", "target", "site", "config", "password", "completion", "version", "help"}
	if projectContextAvailable() {
		candidates = append(candidates, "remote", "env", "theme", "public")
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
		return []string{"dnsimple", "kinsta", "linode"}
	default:
		return nil
	}
}

func targetCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"list", "ls", "show", "adminer", "refresh", "add", "remove", "rm", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "add":
		if len(args) == 1 {
			return []string{"linode"}
		}
		return targetAddFlagCandidates()
	case "show":
		return cachedTargetCompletionNames()
	case "adminer":
		return targetAdminerCompletionCandidates(args[1:])
	case "remove":
		return cachedTargetCompletionNames()
	default:
		return nil
	}
}

func targetAdminerCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"show", "help"}
	}
	if args[0] == "show" {
		return cachedTargetCompletionNames()
	}
	return nil
}

func targetAddFlagCandidates() []string {
	return []string{"--region", "--type", "--image", "--ssh-user", "--execute", "--yes", "--non-interactive", "--dry-run"}
}

func siteCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"list", "ls", "show", "shell", "ssh", "wp", "snapshot", "password", "basicauth", "refresh", "add", "staging", "remove", "rm", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "list":
		for _, arg := range args[1:] {
			if arg == "--envs" {
				return cachedSiteCompletionNames()
			}
		}
		return []string{"--envs"}
	case "show":
		return cachedSiteAndEnvCompletionNames()
	case "shell", "wp":
		return cachedSiteEnvCompletionNames()
	case "snapshot":
		if len(args) == 1 {
			return append([]string{"list", "ls", "remove", "rm", "prune"}, cachedSiteEnvCompletionNames()...)
		}
		if len(args) >= 2 && (args[1] == "remove" || args[1] == "rm") {
			return remoteSnapshotCompletionNames()
		}
		if len(args) >= 2 && args[1] == "prune" {
			return []string{"--keep", "--dry-run", "--yes"}
		}
		return nil
	case "password":
		return cachedSiteCompletionNames()
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
		return []string{"--with-staging", "--region", "--php", "--dry-run", "--execute", "--yes", "--non-interactive"}
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
		return []string{"show", "password", "up", "down", "logs", "shell", "ssh", "wp", "plugins", "snapshot", "pull", "push", "reset", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	switch args[0] {
	case "pull", "push":
		return projectRemoteCompletionNames()
	case "plugins":
		return envPluginsCompletionCandidates(args[1:])
	case "snapshot":
		return envSnapshotCompletionCandidates(args[1:])
	default:
		return nil
	}
}

func envPluginsCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"list", "ls", "add", "remove", "rm", "status", "diff", "install", "help"}
	}
	args[0] = cliCommandAlias(args[0])
	if args[0] == "add" {
		return []string{"--source", "--no-activate", "--no-auto-update"}
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
	return nil
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
		candidates := []string{"tasks", "package", "deploy", "rollback", "help"}
		candidates = append(candidates, projectTaskCompletionNames()...)
		return uniqueSortedStrings(candidates)
	}
	switch args[0] {
	case "package":
		return []string{"--dry-run", "--source", "--output"}
	case "deploy", "rollback":
		return projectRemoteCompletionNames()
	default:
		return nil
	}
}

func publicCompletionCandidates(args []string) []string {
	if len(args) == 0 {
		return []string{"deploy", "help"}
	}
	if args[0] == "deploy" {
		return append(projectRemoteCompletionNames(), "--dry-run", "--yes")
	}
	return nil
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

package cli

// Root-level WordPress docroot aliases backed by wordpress.aliases in nf.json.

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
)

const aliasTargetRoot = "wp-content"

type aliasSpec struct {
	Alias  string
	Target string
}

func loadAliasSpecs(metadata *projectMetadata) ([]aliasSpec, error) {
	raw := metadata.WordPress.Aliases
	specs := make([]aliasSpec, 0, len(raw))
	for name, rawTarget := range raw {
		aliasName, _, err := normalizeAliasName(name)
		if err != nil {
			return nil, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.aliases.%s: %s", name, err)}
		}
		target, err := normalizeAliasTarget(rawTarget)
		if err != nil {
			return nil, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.aliases.%s target: %s", name, err)}
		}
		specs = append(specs, aliasSpec{Alias: aliasName, Target: target})
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Alias < specs[j].Alias })
	return specs, nil
}

func projectAliases(metadata *projectMetadata, create bool) (map[string]string, error) {
	if metadata.WordPress.Aliases == nil {
		if !create {
			return nil, nil
		}
		metadata.WordPress.Aliases = map[string]string{}
	}
	return metadata.WordPress.Aliases, nil
}

func normalizeAliasName(value string) (string, string, error) {
	name := strings.TrimSpace(value)
	if name == "" {
		return "", "", ProjectError{Msg: "alias must not be empty"}
	}
	if name == "." || name == ".." || strings.Contains(name, "..") {
		return "", "", ProjectError{Msg: "alias must be one top-level name without traversal"}
	}
	if strings.HasPrefix(name, "/") || strings.ContainsAny(name, "/\\\x00") {
		return "", "", ProjectError{Msg: "alias must be one top-level name without slashes"}
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			continue
		}
		return "", "", ProjectError{Msg: "alias may contain only letters, numbers, dots, underscores, and hyphens"}
	}
	reserved := map[string]struct{}{
		"wp-admin":     {},
		"wp-content":   {},
		"wp-includes":  {},
		"index.php":    {},
		"wp-login.php": {},
		"xmlrpc.php":   {},
		"robots.txt":   {},
		"favicon.ico":  {},
		"sitemap.xml":  {},
		"uploads":      {},
	}
	lower := strings.ToLower(name)
	if _, ok := reserved[lower]; ok {
		return "", "", ProjectError{Msg: fmt.Sprintf("alias %q is reserved by WordPress or nf", name)}
	}
	warnings := map[string]string{
		"feed":     "alias /feed may conflict with WordPress feed routes",
		"author":   "alias /author may conflict with WordPress author routes",
		"category": "alias /category may conflict with WordPress category routes",
		"tag":      "alias /tag may conflict with WordPress tag routes",
		"page":     "alias /page may conflict with WordPress page routes",
	}
	return name, warnings[lower], nil
}

func normalizeAliasTarget(value string) (string, error) {
	target := strings.TrimSpace(value)
	if target == "" {
		return "", ProjectError{Msg: "target must not be empty"}
	}
	if strings.ContainsAny(target, "\\\x00") {
		return "", ProjectError{Msg: "target must not contain NUL or backslash"}
	}
	if path.IsAbs(target) || strings.HasPrefix(target, "/") {
		return "", ProjectError{Msg: "target must be relative to the WordPress docroot"}
	}
	for _, segment := range strings.Split(target, "/") {
		if segment == ".." {
			return "", ProjectError{Msg: "target must not contain traversal"}
		}
	}
	clean := path.Clean(target)
	if clean == "." {
		return "", ProjectError{Msg: "target must point inside wp-content"}
	}
	if clean != aliasTargetRoot && !strings.HasPrefix(clean, aliasTargetRoot+"/") {
		return "", ProjectError{Msg: "target must point to wp-content or a path inside wp-content"}
	}
	return clean, nil
}

func cmdAliasesList(metadata *projectMetadata) int {
	specs, err := loadAliasSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(specs) == 0 {
		fmt.Println("No aliases configured.")
		return 0
	}
	fmt.Println(formatAliasListTable(specs))
	return 0
}

func formatAliasListTable(specs []aliasSpec) string {
	rows := [][]string{{"alias", "target"}}
	for _, spec := range specs {
		rows = append(rows, []string{"/" + spec.Alias, spec.Target})
	}
	return formatTable(rows)
}

func projectAliasCompletionNames() []string {
	root, ok := currentNFProjectRoot()
	if !ok {
		return nil
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		return nil
	}
	specs, err := loadAliasSpecs(metadata)
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(specs))
	for _, spec := range specs {
		values = append(values, spec.Alias)
	}
	return values
}

func cmdAliasesAdd(root string, metadata *projectMetadata, aliasName, target string) int {
	if _, err := loadAliasSpecs(metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	aliasName, warning, err := normalizeAliasName(aliasName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	target, err = normalizeAliasTarget(target)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	aliases, err := projectAliases(metadata, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if _, ok := aliases[aliasName]; ok {
		fmt.Fprintf(os.Stderr, "nf.json wordpress.aliases already contains /%s\n", aliasName)
		return 1
	}
	aliases[aliasName] = target
	if err := saveProjectMetadata(root, metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if warning != "" {
		fmt.Fprintf(os.Stderr, "Warning: %s.\n", warning)
	}
	fmt.Printf("Added alias /%s -> %s to nf.json.\n", aliasName, target)
	return 0
}

func cmdAliasesRemove(root string, metadata *projectMetadata, aliasName string) int {
	if _, err := loadAliasSpecs(metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	aliasName, _, err := normalizeAliasName(aliasName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	aliases, err := projectAliases(metadata, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if aliases == nil {
		fmt.Fprintf(os.Stderr, "nf.json wordpress.aliases does not contain /%s\n", aliasName)
		return 1
	}
	if _, ok := aliases[aliasName]; !ok {
		fmt.Fprintf(os.Stderr, "nf.json wordpress.aliases does not contain /%s\n", aliasName)
		return 1
	}
	delete(aliases, aliasName)
	if err := saveProjectMetadata(root, metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Removed alias /%s from nf.json. Run nf alias sync to prune the symlink.\n", aliasName)
	return 0
}

func cmdAliasesStatusWithOptions(root string, metadata *projectMetadata, remoteName string) int {
	specs, err := loadAliasSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if strings.TrimSpace(remoteName) == "" {
		cfg, ok := loadEnvConfig(root, metadata)
		if !ok {
			fmt.Fprintln(os.Stderr, "Invalid local project metadata in nf.json.")
			return 1
		}
		return cmdAliasesStatusLocal(cfg, specs)
	}
	return cmdAliasesStatusRemote(metadata, remoteName, specs)
}

func cmdAliasesSyncWithOptions(root string, metadata *projectMetadata, remoteName string) int {
	specs, err := loadAliasSpecs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if strings.TrimSpace(remoteName) == "" {
		cfg, ok := loadEnvConfig(root, metadata)
		if !ok {
			fmt.Fprintln(os.Stderr, "Invalid local project metadata in nf.json.")
			return 1
		}
		return cmdAliasesSyncLocal(cfg, specs)
	}
	return cmdAliasesSyncRemote(metadata, remoteName, specs)
}

func cmdAliasesStatusLocal(cfg envConfig, specs []aliasSpec) int {
	output, err := runCommandSpecOutputSilentFn(execSpec{Dir: localEnvDir(cfg), Args: envWordpressRootExecArgs(cfg, "sh", "-lc", aliasStatusScript("/var/www/html", specs))})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Alias status:")
	printAliasStatusOutput(output)
	return 0
}

func cmdAliasesStatusRemote(metadata *projectMetadata, remoteName string, specs []aliasSpec) int {
	target, err := resolveEnvRemoteSyncTarget("alias status", remoteName, metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	output, err := runSSHOutputFn(aliasRemoteArgs(target, aliasStatusScript(target.WordPressPath, specs)))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Alias status:")
	printAliasRemoteHeader(target)
	fmt.Println()
	printAliasStatusOutput(string(output))
	return 0
}

func cmdAliasesSyncLocal(cfg envConfig, specs []aliasSpec) int {
	output, err := runCommandSpecOutputSilentFn(execSpec{Dir: localEnvDir(cfg), Args: envWordpressRootExecArgs(cfg, "sh", "-lc", aliasSyncScript("/var/www/html", specs))})
	fmt.Println("Alias sync:")
	printAliasSyncOutput(output)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func cmdAliasesSyncRemote(metadata *projectMetadata, remoteName string, specs []aliasSpec) int {
	target, err := resolveEnvRemoteSyncTarget("alias sync", remoteName, metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	output, err := runSSHOutputFn(aliasRemoteArgs(target, aliasSyncScript(target.WordPressPath, specs)))
	fmt.Println("Alias sync:")
	printAliasRemoteHeader(target)
	fmt.Println()
	printAliasSyncOutput(string(output))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func aliasRemoteArgs(target envRemoteSyncTarget, script string) []string {
	if target.SudoFileOps {
		return remoteSudoBashArgs(target, script)
	}
	return remoteSSHArgs(target, script)
}

func printAliasRemoteHeader(target envRemoteSyncTarget) {
	fmt.Printf("  remote:   %s\n", target.RemoteName)
	fmt.Printf("  site:     %s\n", target.SiteID)
	fmt.Printf("  env:      %s\n", target.Env)
	fmt.Printf("  provider: %s\n", target.Provider)
}

func printAliasStatusOutput(output string) {
	rows := aliasStatusRows(output)
	if len(rows) == 1 {
		fmt.Println("No aliases configured and no stale alias symlinks found.")
		return
	}
	fmt.Println(formatTable(rows))
}

func aliasStatusRows(output string) [][]string {
	rows := [][]string{{"alias", "target", "status"}}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		rows = append(rows, []string{parts[0], parts[1], parts[2]})
	}
	return rows
}

func printAliasSyncOutput(output string) {
	output = strings.TrimRight(output, "\n")
	if output == "" {
		fmt.Println("Alias symlinks already synced.")
		return
	}
	fmt.Println(output)
}

func aliasStatusScript(docroot string, specs []aliasSpec) string {
	return aliasScript(docroot, specs, false)
}

func aliasSyncScript(docroot string, specs []aliasSpec) string {
	return aliasScript(docroot, specs, true)
}

func aliasScript(docroot string, specs []aliasSpec, sync bool) string {
	var b strings.Builder
	b.WriteString("set -u\n")
	b.WriteString("docroot=")
	b.WriteString(shellQuoteArg(docroot))
	b.WriteString("\n")
	b.WriteString(`allowed="$docroot/wp-content"
allowed_real=$(readlink -f "$allowed" 2>/dev/null || true)
errors=0

within_wp_content() {
  candidate=$1
  [ -n "$allowed_real" ] || return 1
  case "$candidate" in
    "$allowed_real"|"$allowed_real"/*) return 0 ;;
    *) return 1 ;;
  esac
}

emit_status() {
  printf '%s\t%s\t%s\n' "$1" "$2" "$3"
}

target_problem() {
  target_path=$1
  if [ -z "$allowed_real" ]; then
    printf '%s\n' 'Missing wp-content'
    return 0
  fi
  if [ ! -e "$target_path" ]; then
    printf '%s\n' 'Missing target'
    return 0
  fi
  target_real=$(readlink -f "$target_path" 2>/dev/null || true)
  if [ -z "$target_real" ] || ! within_wp_content "$target_real"; then
    printf '%s\n' 'Target outside wp-content'
    return 0
  fi
  return 1
}

is_configured_alias() {
  case "$1" in
`)
	for _, spec := range specs {
		b.WriteString("    ")
		b.WriteString(shellQuoteArg(spec.Alias))
		b.WriteString(") return 0 ;;\n")
	}
	b.WriteString(`    *) return 1 ;;
  esac
}

status_alias() {
  alias_name=$1
  target=$2
  alias_path="$docroot/$alias_name"
  target_path="$docroot/$target"
  display_alias="/$alias_name"
  problem=$(target_problem "$target_path" || true)
  if [ -n "$problem" ]; then
    emit_status "$display_alias" "$target" "$problem"
    return
  fi
  if [ -L "$alias_path" ]; then
    link_target=$(readlink "$alias_path" 2>/dev/null || true)
    if [ "$link_target" = "$target" ]; then
      emit_status "$display_alias" "$target" 'OK'
    else
      emit_status "$display_alias" "$link_target" 'Wrong symlink target'
    fi
    return
  fi
  if [ -e "$alias_path" ]; then
    if [ -d "$alias_path" ]; then
      emit_status "$display_alias" "$target" 'Conflict: real directory exists'
    else
      emit_status "$display_alias" "$target" 'Conflict: real file exists'
    fi
    return
  fi
  emit_status "$display_alias" "$target" 'Missing symlink'
}

sync_alias() {
  alias_name=$1
  target=$2
  alias_path="$docroot/$alias_name"
  target_path="$docroot/$target"
  display_alias="/$alias_name"
  problem=$(target_problem "$target_path" || true)
  if [ -n "$problem" ]; then
    printf 'Skipped %s: %s: %s\n' "$display_alias" "$problem" "$target"
    errors=1
    return
  fi
  if [ -L "$alias_path" ]; then
    link_target=$(readlink "$alias_path" 2>/dev/null || true)
    if [ "$link_target" = "$target" ]; then
      printf 'OK %s -> %s\n' "$display_alias" "$target"
      return
    fi
    if rm -f "$alias_path" && ln -s "$target" "$alias_path"; then
      printf 'Updated %s -> %s\n' "$display_alias" "$target"
    else
      printf 'Failed %s: could not replace symlink\n' "$display_alias"
      errors=1
    fi
    return
  fi
  if [ -e "$alias_path" ]; then
    if [ -d "$alias_path" ]; then
      printf 'Conflict %s: real directory exists at web root\n' "$display_alias"
    else
      printf 'Conflict %s: real file exists at web root\n' "$display_alias"
    fi
    errors=1
    return
  fi
  if ln -s "$target" "$alias_path"; then
    printf 'Created %s -> %s\n' "$display_alias" "$target"
  else
    printf 'Failed %s: could not create symlink\n' "$display_alias"
    errors=1
  fi
}

status_stale_aliases() {
  for alias_path in "$docroot"/* "$docroot"/.[!.]* "$docroot"/..?*; do
    [ -e "$alias_path" ] || [ -L "$alias_path" ] || continue
    [ -L "$alias_path" ] || continue
    alias_name=${alias_path##*/}
    is_configured_alias "$alias_name" && continue
    link_target=$(readlink "$alias_path" 2>/dev/null || true)
    emit_status "/$alias_name" "$link_target" 'Stale symlink'
  done
}

prune_stale_aliases() {
  for alias_path in "$docroot"/* "$docroot"/.[!.]* "$docroot"/..?*; do
    [ -e "$alias_path" ] || [ -L "$alias_path" ] || continue
    [ -L "$alias_path" ] || continue
    alias_name=${alias_path##*/}
    is_configured_alias "$alias_name" && continue
    if rm -f "$alias_path"; then
      printf 'Pruned /%s\n' "$alias_name"
    else
      printf 'Failed /%s: could not prune stale symlink\n' "$alias_name"
      errors=1
    fi
  done
}

`)
	for _, spec := range specs {
		if sync {
			b.WriteString("sync_alias ")
		} else {
			b.WriteString("status_alias ")
		}
		b.WriteString(shellQuoteArg(spec.Alias))
		b.WriteByte(' ')
		b.WriteString(shellQuoteArg(spec.Target))
		b.WriteByte('\n')
	}
	if sync {
		b.WriteString("prune_stale_aliases\n")
		b.WriteString("exit $errors\n")
	} else {
		b.WriteString("status_stale_aliases\n")
	}
	return b.String()
}

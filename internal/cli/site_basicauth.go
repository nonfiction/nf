package cli

import (
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/nonfiction/nf/internal/state"
	"github.com/nonfiction/nf/internal/ui"
)

type siteBasicAuthOptions struct {
	dryRun         bool
	execute        bool
	yes            bool
	nonInteractive bool
}

type siteBasicAuthPlan struct {
	Action      string
	EnvID       string
	SiteID      string
	SiteSlug    string
	Env         string
	Provider    string
	Target      envRemoteSyncTarget
	User        string
	Password    string
	PasswordSHA string
	FileSlugs   []string
}

func runSiteBasicAuth(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runSiteBasicAuthHelp()
	}
	action := strings.TrimSpace(argv[0])
	if action == "password" {
		return runSiteBasicAuthPassword(argv[1:])
	}
	if action != "status" && action != "enable" && action != "disable" {
		fmt.Fprintf(os.Stderr, "unsupported site basicauth action: %s\n", action)
		return 1
	}
	envRef, opts, ok := parseSiteBasicAuthActionArgs(action, argv[1:])
	if !ok {
		return 1
	}
	return cmdSiteBasicAuth(envRef, action, opts)
}

func runSiteBasicAuthHelp() int {
	printGroupHelp("site basicauth", []helpLine{
		{"status <env>", "show provider basic-auth status"},
		{"password [site]", "show derived basic-auth password only"},
		{},
		{"enable <env> [--dry-run] [--execute] [--yes] [--non-interactive]", "enable provider basic auth"},
		{"disable <env> [--dry-run] [--execute] [--yes] [--non-interactive]", "disable provider basic auth"},
	})
	return 0
}

func parseSiteBasicAuthActionArgs(action string, argv []string) (string, siteBasicAuthOptions, bool) {
	var opts siteBasicAuthOptions
	ref := ""
	for _, arg := range argv {
		switch arg {
		case "--dry-run":
			opts.dryRun = true
		case "--execute":
			opts.execute = true
		case "--yes":
			opts.yes = true
		case "--non-interactive":
			opts.nonInteractive = true
		default:
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(os.Stderr, "unknown site basicauth flag: %s\n", arg)
				return "", opts, false
			}
			if ref != "" {
				fmt.Fprintf(os.Stderr, "site basicauth %s takes at most one env ref\n", action)
				return "", opts, false
			}
			ref = arg
		}
	}
	if opts.nonInteractive && strings.TrimSpace(ref) == "" {
		fmt.Fprintf(os.Stderr, "site basicauth %s requires an explicit env ref in non-interactive mode\n", action)
		return "", opts, false
	}
	if opts.nonInteractive {
		if _, _, ok := splitSiteEnvRef(ref); !ok {
			fmt.Fprintf(os.Stderr, "site basicauth %s requires an explicit env ref in non-interactive mode\n", action)
			return "", opts, false
		}
	}
	envRef, ok := resolveSiteCommandEnvRef("basic-auth "+action, ref)
	if !ok {
		return "", opts, false
	}
	return envRef, opts, true
}

func runSiteBasicAuthPassword(argv []string) int {
	if len(argv) > 1 {
		fmt.Fprintln(os.Stderr, "site basicauth password takes at most one site")
		return 1
	}
	needle := ""
	if len(argv) == 1 {
		needle = argv[0]
	}
	if needle == "" {
		selected, err := chooseSiteForPassword()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		needle = selected
	}
	if siteID, _, ok := splitSiteEnvRef(needle); ok {
		fmt.Fprintf(os.Stderr, "site basicauth password takes a site, not an env; use %q.\n", siteID)
		return 1
	}
	password, err := siteBasicAuthPasswordForSite(needle)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(password)
	return 0
}

func siteBasicAuthPasswordForSite(needle string) (string, error) {
	resolved, _, projectFileExists, targetAliasUsed, err := resolveSiteTarget(needle)
	if err != nil {
		return "", err
	}
	if siteID, _, ok := splitSiteEnvRef(resolved); ok {
		resolved = siteID
	}
	bundle, err := loadSiteBundleForBasicAuth()
	if err != nil {
		return "", err
	}
	records := siteRecordsByID(bundle, resolved)
	if len(records) == 0 {
		_, _ = projectFileExists, targetAliasUsed
		return "", ProjectError{Msg: fmt.Sprintf("No site matched %q.", needle)}
	}
	slug := sitePasswordSlug(preferredPasswordSiteRecord(records))
	if slug == "" {
		return "", ProjectError{Msg: fmt.Sprintf("Site %q has no derivable basic-auth password.", resolved)}
	}
	return deriveSiteBasicAuthPassword(slug)
}

func loadSiteBundleForBasicAuth() ([]map[string]any, error) {
	records, err := state.LoadStateRecords("sites")
	if err != nil {
		return nil, err
	}
	return records, nil
}

func cmdSiteBasicAuth(envRef, action string, opts siteBasicAuthOptions) int {
	if opts.execute && opts.dryRun {
		fmt.Fprintln(os.Stderr, "Choose either --execute or --dry-run, not both.")
		return 1
	}
	siteID, env, ok := splitSiteEnvRef(envRef)
	if !ok {
		fmt.Fprintln(os.Stderr, "site basicauth requires an explicit env ref like site.target:staging")
		return 1
	}
	plan, err := buildSiteBasicAuthPlan(siteID, env, action)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if action == "status" {
		return cmdSiteBasicAuthStatus(plan)
	}
	willExecute := opts.execute || (!opts.dryRun && !opts.nonInteractive)
	mode := "dry-run"
	if willExecute {
		mode = "execute"
	}
	printSiteBasicAuthPlan(plan, mode)
	if !willExecute {
		fmt.Println("No data was changed. Re-run with --execute to apply provider basic auth.")
		return 0
	}
	if opts.nonInteractive && !opts.yes {
		fmt.Fprintln(os.Stderr, "Remote execution requires both --execute and --yes in non-interactive mode.")
		return 1
	}
	if !opts.yes {
		confirmed, err := ui.Confirm(fmt.Sprintf("%s basic auth for %s?", strings.ToUpper(plan.Action[:1])+plan.Action[1:], plan.EnvID), false)
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
		fmt.Fprintln(os.Stderr, kinstaBasicAuthUnsupportedMessage())
		return 1
	}
	if err := runSSHCommandFn(remoteSudoBashArgs(plan.Target, renderLinodeBasicAuthScript(plan))); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	pastTense := map[string]string{"enable": "enabled", "disable": "disabled"}[plan.Action]
	fmt.Printf("Basic auth %s.\n", pastTense)
	return 0
}

func buildSiteBasicAuthPlan(siteID, env, action string) (siteBasicAuthPlan, error) {
	record, _, err := cachedSiteEnv(siteID, env)
	if err != nil {
		return siteBasicAuthPlan{}, err
	}
	if record == nil {
		return siteBasicAuthPlan{}, ProjectError{Msg: fmt.Sprintf("No cached remote env matched %q.", canonicalEnvID(siteID, env))}
	}
	target, err := envRemoteSyncTargetFromSiteRecord(record, canonicalEnvID(siteID, env), siteID, env)
	if err != nil {
		return siteBasicAuthPlan{}, err
	}
	slug := sitePasswordSlug(record)
	if slug == "" {
		return siteBasicAuthPlan{}, ProjectError{Msg: fmt.Sprintf("Site env %q has no derivable password scope.", canonicalEnvID(siteID, env))}
	}
	password := ""
	passwordSHA := ""
	if action == "enable" {
		var err error
		password, err = deriveSiteBasicAuthPassword(slug)
		if err != nil {
			return siteBasicAuthPlan{}, err
		}
		passwordSHA = basicAuthSHA(password)
	}
	user, err := basicAuthDefaultUser()
	if err != nil {
		return siteBasicAuthPlan{}, err
	}
	return siteBasicAuthPlan{
		Action:      action,
		EnvID:       canonicalEnvID(siteID, env),
		SiteID:      siteID,
		SiteSlug:    slug,
		Env:         env,
		Provider:    target.Provider,
		Target:      target,
		User:        user,
		Password:    password,
		PasswordSHA: passwordSHA,
		FileSlugs:   linodeBasicAuthFileSlugs(record, target, siteID, env),
	}, nil
}

func linodeBasicAuthFileSlugs(record map[string]any, target envRemoteSyncTarget, siteID, env string) []string {
	candidates := []string{
		envIDFileSlug(siteRecordEnvID(record)),
		envIDFileSlug(canonicalEnvID(siteID, env)),
		linodeHostnameFileSlug(record, target),
	}
	seen := map[string]bool{}
	result := []string{}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		result = append(result, candidate)
	}
	return result
}

func linodeHostnameFileSlug(record map[string]any, target envRemoteSyncTarget) string {
	host := firstRecordString(record, "hostname", "url", "site_url", "home_url")
	if _, after, ok := strings.Cut(host, "://"); ok {
		host = after
	}
	if before, _, ok := strings.Cut(host, "/"); ok {
		host = before
	}
	host = strings.TrimSpace(host)
	targetRef := strings.TrimSpace(target.TargetRef)
	if host == "" || targetRef == "" {
		return ""
	}
	needle := "." + targetRef + "."
	if idx := strings.Index(host, needle); idx >= 0 {
		return host[:idx+1+len(targetRef)]
	}
	if strings.HasSuffix(host, "."+targetRef) {
		return host
	}
	return ""
}

func basicAuthDefaultUser() (string, error) {
	values, err := loadGlobalConfig()
	if err != nil {
		return "", err
	}
	user := strings.TrimSpace(firstNonEmpty(values["basicauth_default_user"], "nonfiction"))
	if user == "" || strings.Contains(user, ":") || strings.ContainsAny(user, " \t\r\n") {
		return "", ProjectError{Msg: "basicauth_default_user must be a non-empty username without whitespace or colon"}
	}
	return user, nil
}

func basicAuthSHA(password string) string {
	sum := sha1.Sum([]byte(password))
	return "{SHA}" + base64.StdEncoding.EncodeToString(sum[:])
}

func printSiteBasicAuthPlan(plan siteBasicAuthPlan, mode string) {
	fmt.Println("Site basic-auth plan:")
	fmt.Printf("  env:      %s\n", plan.EnvID)
	fmt.Printf("  provider: %s\n", plan.Provider)
	if plan.Target.TargetRef != "" {
		fmt.Printf("  target:   %s\n", plan.Target.TargetRef)
	}
	if plan.Target.URL != "" {
		fmt.Printf("  url:      %s\n", plan.Target.URL)
	}
	fmt.Printf("  action:   %s\n", plan.Action)
	fmt.Printf("  user:     %s\n", plan.User)
	if plan.Action == "enable" {
		fmt.Printf("  password: derived from %s\n", plan.SiteSlug)
	}
	fmt.Printf("  mode:     %s\n", mode)
}

func cmdSiteBasicAuthStatus(plan siteBasicAuthPlan) int {
	fmt.Println("Site basic-auth status:")
	fmt.Printf("  env:      %s\n", plan.EnvID)
	fmt.Printf("  provider: %s\n", plan.Provider)
	fmt.Printf("  user:     %s\n", plan.User)
	if plan.Provider == "kinsta" {
		fmt.Printf("  status:   unsupported\n")
		fmt.Println(kinstaBasicAuthUnsupportedMessage())
		return 1
	}
	output, err := runSSHOutputFn(remoteSudoBashArgs(plan.Target, renderLinodeBasicAuthStatusScript(plan)))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Print(string(output))
	return 0
}

func remoteSudoBashArgs(target envRemoteSyncTarget, script string) []string {
	return remoteSSHArgs(target, "sudo bash -c "+shellQuoteArg(script))
}

func renderLinodeBasicAuthScript(plan siteBasicAuthPlan) string {
	q := shellQuoteArg
	fileSlugs := shellArrayValues(firstNonEmptySlice(plan.FileSlugs, []string{envIDFileSlug(plan.EnvID)}))
	if plan.Action == "disable" {
		return fmt.Sprintf(`set -euo pipefail
file_slugs=(%s)
vhosts=()
snippets=()
htpasswds=()
for file_slug in "${file_slugs[@]}"; do
  vhost="/etc/nginx/sites-available/nf-site-$file_slug"
  snippet="/etc/nginx/snippets/nf-basic-auth-$file_slug.conf"
  htpasswd="/var/lib/nf/basic-auth/$file_slug.htpasswd"
  snippets+=("$snippet")
  htpasswds+=("$htpasswd")
  if [ -f "$vhost" ]; then vhosts+=("$vhost"); fi
done
if [ "${#vhosts[@]}" -eq 0 ]; then echo "missing nginx vhost for any of: ${file_slugs[*]}" >&2; exit 1; fi
for vhost in "${vhosts[@]}"; do
  tmp=$(mktemp)
  cp "$vhost" "$tmp"
  for snippet in "${snippets[@]}"; do
    next=$(mktemp)
    awk -v inc="    include $snippet;" '$0 != inc { print }' "$tmp" >"$next"
    rm -f "$tmp"
    tmp="$next"
  done
  install -m 0644 "$tmp" "$vhost"
  rm -f "$tmp"
done
rm -f "${snippets[@]}" "${htpasswds[@]}"
nginx -t
systemctl reload nginx
`, fileSlugs)
	}
	return fmt.Sprintf(`set -euo pipefail
file_slugs=(%s)
selected_file_slug=""
vhost=""
for file_slug in "${file_slugs[@]}"; do
  candidate="/etc/nginx/sites-available/nf-site-$file_slug"
  if [ -f "$candidate" ]; then selected_file_slug="$file_slug"; vhost="$candidate"; break; fi
done
if [ -z "$vhost" ]; then echo "missing nginx vhost for any of: ${file_slugs[*]}" >&2; exit 1; fi
snippet="/etc/nginx/snippets/nf-basic-auth-$selected_file_slug.conf"
htpasswd="/var/lib/nf/basic-auth/$selected_file_slug.htpasswd"
install -d -m 0755 /etc/nginx/snippets /var/lib/nf/basic-auth
tmp_ht=$(mktemp)
printf '%%s:%%s\n' %s %s >"$tmp_ht"
install -o root -g www-data -m 0640 "$tmp_ht" "$htpasswd"
rm -f "$tmp_ht"
cat >"$snippet" <<EOF
auth_basic "Restricted";
auth_basic_user_file $htpasswd;
EOF
if ! grep -Fxq "    include $snippet;" "$vhost"; then
  tmp=$(mktemp)
  awk -v inc="    include $snippet;" '{ print; if ($0 ~ /^[[:space:]]*root[[:space:]].*;[[:space:]]*$/ && inserted == 0) { print inc; inserted=1 } } END { if (inserted == 0) exit 42 }' "$vhost" >"$tmp" || { rm -f "$tmp"; echo "could not insert basic-auth include into $vhost" >&2; exit 1; }
  install -m 0644 "$tmp" "$vhost"
  rm -f "$tmp"
fi
nginx -t
systemctl reload nginx
`, fileSlugs, q(plan.User), q(plan.PasswordSHA))
}

func renderLinodeBasicAuthStatusScript(plan siteBasicAuthPlan) string {
	fileSlugs := shellArrayValues(firstNonEmptySlice(plan.FileSlugs, []string{envIDFileSlug(plan.EnvID)}))
	return fmt.Sprintf(`set -eu
status="disabled"
remote_user=""
file_slugs=(%s)
vhosts=()
snippets=()
for file_slug in "${file_slugs[@]}"; do
  vhost="/etc/nginx/sites-available/nf-site-$file_slug"
  snippet="/etc/nginx/snippets/nf-basic-auth-$file_slug.conf"
  htpasswd="/var/lib/nf/basic-auth/$file_slug.htpasswd"
  snippets+=("$snippet")
  if [ -f "$vhost" ]; then vhosts+=("$vhost"); fi
  if [ -z "$remote_user" ] && [ -f "$htpasswd" ]; then
    remote_user=$(cut -d: -f1 "$htpasswd" | head -n 1)
  fi
done
for vhost in "${vhosts[@]}"; do
  for snippet in "${snippets[@]}"; do
    if [ -f "$snippet" ] && grep -Fxq "    include $snippet;" "$vhost"; then
      status="enabled"
    fi
  done
done
echo "  status:   $status"
if [ -n "$remote_user" ]; then echo "  remote user: $remote_user"; fi
`, fileSlugs)
}

func firstNonEmptySlice(values []string, fallback []string) []string {
	if len(values) > 0 {
		return values
	}
	return fallback
}

func shellArrayValues(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		quoted = append(quoted, shellQuoteArg(value))
	}
	return strings.Join(quoted, " ")
}

func kinstaBasicAuthUnsupportedMessage() string {
	return "Kinsta Password protection exists in MyKinsta, but no public API endpoint is exposed for automation yet; use MyKinsta manually."
}

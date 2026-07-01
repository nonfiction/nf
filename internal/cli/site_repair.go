package cli

import (
	"fmt"
	"os"
	"strings"
)

const kinstaMUPluginsZipURL = "https://kinsta.com/kinsta-tools/kinsta-mu-plugins.zip"

type siteRepairPlan struct {
	EnvID    string
	SiteID   string
	Env      string
	Provider string
	Target   envRemoteSyncTarget
	Actions  []string
	Warnings []string
	Script   string
}

func parseSiteRepairArgs(argv []string) (string, deleteServerOptions, error) {
	var opts deleteServerOptions
	positionals := make([]string, 0, 1)
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch arg {
		case "--":
			positionals = append(positionals, argv[i+1:]...)
			i = len(argv)
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
				return "", opts, fmt.Errorf("unknown site repair flag: %s", arg)
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 1 {
		return "", opts, fmt.Errorf("site repair takes at most one site or env ref")
	}
	if len(positionals) == 0 {
		if opts.nonInteractive || !siteIsInteractiveFn() {
			return "", opts, fmt.Errorf("site repair requires a site or env ref like site.target or site.target:staging")
		}
		selected, err := chooseSiteEnv("repair", "")
		if err != nil {
			return "", opts, err
		}
		return selected, opts, nil
	}
	ref := strings.TrimSpace(positionals[0])
	if _, _, ok := splitSiteEnvRef(ref); ok {
		return ref, opts, nil
	}
	return canonicalEnvID(ref, "live"), opts, nil
}

func siteRepairMode(opts deleteServerOptions) (bool, string, error) {
	if opts.execute && opts.dryRun {
		return false, "", fmt.Errorf("Choose either --execute or --dry-run, not both.")
	}
	if opts.nonInteractive && opts.execute && !opts.yes {
		return false, "", fmt.Errorf("Remote execution requires both --execute and --yes in non-interactive mode.")
	}
	if !opts.execute && (opts.dryRun || opts.nonInteractive) {
		opts.dryRun = true
	}
	willExecute := opts.execute || (!opts.dryRun && !opts.nonInteractive)
	if willExecute {
		return true, "execute", nil
	}
	return false, "dry-run", nil
}

func cmdSiteRepair(envRef string, opts deleteServerOptions) int {
	willExecute, mode, err := siteRepairMode(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	plan, err := buildSiteRepairPlan(envRef)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	printSiteRepairPlan(plan, mode)
	if !willExecute {
		return 0
	}
	if !opts.yes {
		confirmed, err := siteAddConfirmFn(fmt.Sprintf("Repair provider platform files for %q?", plan.EnvID), false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	args := siteRepairSSHArgs(plan)
	printSiteRepairCommand(plan)
	if err := runSSHCommandFn(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Site repaired.")
	return 0
}

func buildSiteRepairPlan(envRef string) (siteRepairPlan, error) {
	siteID, env, ok := splitSiteEnvRef(envRef)
	if !ok {
		return siteRepairPlan{}, ProjectError{Msg: "site repair requires a site or env ref like site.target or site.target:staging"}
	}
	record, _, err := cachedSiteEnv(siteID, env)
	if err != nil {
		return siteRepairPlan{}, err
	}
	if record == nil {
		return siteRepairPlan{}, ProjectError{Msg: fmt.Sprintf("No cached remote env matched %q. Run nf site refresh after target cache is current.", canonicalEnvID(siteID, env))}
	}
	if err := validateSiteRecord(record); err != nil {
		return siteRepairPlan{}, err
	}
	target, err := envRemoteSyncTargetFromSiteRecord(record, "repair", siteID, env)
	if err != nil {
		return siteRepairPlan{}, err
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(record["provider"])))
	plan := siteRepairPlan{EnvID: canonicalEnvID(siteID, env), SiteID: siteID, Env: env, Provider: provider, Target: target}
	switch provider {
	case "kinsta":
		plan.Actions = []string{
			"remove local-only wp-content/mu-plugins/nf-mailpit.php if present",
			"restore Kinsta's required MU plugin when kinsta-mu-plugins.php or kinsta-mu-plugins/ is missing",
			"ensure KINSTAMU_WHITELABEL is enabled in wp-config.php",
		}
		plan.Script = renderKinstaSiteRepairScript(target.WordPressPath)
	case "linode":
		hostname := linodeRepairInternalHostname(siteID, env, record, target)
		if hostname == "" {
			return siteRepairPlan{}, ProjectError{Msg: fmt.Sprintf("Linode site env %q is missing an internal hostname. Run nf site refresh.", plan.EnvID)}
		}
		fileSlugs := linodeBasicAuthFileSlugs(record, target, siteID, env)
		if len(fileSlugs) == 0 {
			fileSlugs = []string{envIDFileSlug(plan.EnvID)}
		}
		phpVersion := firstNonEmpty(sitePHPVersion(record), "8.3")
		plan.Actions = []string{
			"remove local-only wp-content/mu-plugins/nf-mailpit.php if present",
			"install or refresh the nf Linode cache MU plugin",
			"install or refresh nginx FastCGI cache snippets and per-env cache config",
			"rewrite the internal env nginx vhost with cache includes while preserving existing basic-auth",
			"test and reload nginx",
		}
		plan.Warnings = linodeRepairWarnings(record)
		plan.Script = renderLinodeSiteRepairScript(target.WordPressPath, hostname, phpVersion, fileSlugs)
	default:
		return siteRepairPlan{}, ProjectError{Msg: fmt.Sprintf("site repair is not implemented for provider %q; no files were changed.", provider)}
	}
	return plan, nil
}

func printSiteRepairPlan(plan siteRepairPlan, mode string) {
	fmt.Println("Site repair plan:")
	fmt.Printf("  env:      %s\n", plan.EnvID)
	fmt.Printf("  provider: %s\n", plan.Provider)
	if plan.Target.TargetRef != "" {
		fmt.Printf("  target:   %s\n", plan.Target.TargetRef)
	}
	if plan.Target.URL != "" {
		fmt.Printf("  url:      %s\n", plan.Target.URL)
	}
	if plan.Target.WordPressPath != "" {
		fmt.Printf("  path:     %s\n", plan.Target.WordPressPath)
	}
	fmt.Printf("  mode:     %s\n", mode)
	fmt.Println("  actions:")
	for _, action := range plan.Actions {
		fmt.Printf("    - %s\n", action)
	}
	for _, warning := range plan.Warnings {
		fmt.Printf("  warning:  %s\n", warning)
	}
}

func siteRepairSSHArgs(plan siteRepairPlan) []string {
	if plan.Provider == "linode" {
		return remoteSudoBashArgs(plan.Target, plan.Script)
	}
	return remoteSSHArgs(plan.Target, plan.Script)
}

func printSiteRepairCommand(plan siteRepairPlan) {
	scriptLabel := "<site repair script>"
	if plan.Provider == "linode" {
		scriptLabel = "<sudo site repair script>"
	}
	fmt.Printf("> ssh -p %s %s@%s %s\n", plan.Target.SSHPort, plan.Target.SSHUser, plan.Target.SSHHost, shellQuoteArg(scriptLabel))
}

func renderKinstaSiteRepairScript(sitePath string) string {
	q := shellQuoteArg
	return fmt.Sprintf(`set -eu
site_path=%s
mu_dir="$site_path/wp-content/mu-plugins"
plugin_file="$mu_dir/kinsta-mu-plugins.php"
plugin_dir="$mu_dir/kinsta-mu-plugins"
mailpit_file="$mu_dir/nf-mailpit.php"
zip_url=%s
mkdir -p "$mu_dir"
rm -f "$mailpit_file"
if [ ! -f "$plugin_file" ] || [ ! -d "$plugin_dir" ]; then
  command -v curl >/dev/null 2>&1 || { echo "curl is required to download Kinsta MU plugins" >&2; exit 1; }
  command -v unzip >/dev/null 2>&1 || { echo "unzip is required to restore Kinsta MU plugins" >&2; exit 1; }
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  curl -fsSL "$zip_url" -o "$tmp/kinsta-mu-plugins.zip"
  mkdir -p "$tmp/extract"
  unzip -q "$tmp/kinsta-mu-plugins.zip" -d "$tmp/extract"
  test -f "$tmp/extract/kinsta-mu-plugins.php"
  test -d "$tmp/extract/kinsta-mu-plugins"
  rm -rf "$plugin_file" "$plugin_dir"
  cp "$tmp/extract/kinsta-mu-plugins.php" "$plugin_file"
  cp -R "$tmp/extract/kinsta-mu-plugins" "$plugin_dir"
fi
%s
echo "Kinsta MU plugins repaired."
`, q(sitePath), q(kinstaMUPluginsZipURL), renderWPConfigDefineScript(sitePath, []wpConfigDefine{kinstaWhitelabelWPConfigDefine()}))
}

func renderLinodeSiteRepairScript(sitePath, hostname, phpVersion string, fileSlugs []string) string {
	q := shellQuoteArg
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	b.WriteString("site_path=")
	b.WriteString(q(sitePath))
	b.WriteByte('\n')
	b.WriteString("host_name=")
	b.WriteString(q(hostname))
	b.WriteByte('\n')
	b.WriteString("fallback_php_version=")
	b.WriteString(q(firstNonEmpty(phpVersion, "8.3")))
	b.WriteByte('\n')
	b.WriteString("file_slugs=(")
	b.WriteString(shellArrayValues(fileSlugs))
	b.WriteString(")\n")
	b.WriteString(`install -d -m 0755 /etc/nginx/sites-available /etc/nginx/sites-enabled /var/log/nginx/sites /etc/nginx/conf.d
cat >/etc/nginx/conf.d/nf-server-names-hash.conf <<'EOF'
server_names_hash_bucket_size 128;
server_names_hash_max_size 4096;
EOF
php_version=$(jq -r '.php_version // .php.version // ""' /var/lib/nf/target.json 2>/dev/null || true)
if [ -z "$php_version" ]; then php_version="$fallback_php_version"; fi
`)
	b.WriteString(linodeNginxCacheShellFunctions())
	b.WriteString(`selected_file_slug=""
vhost=""
for file_slug in "${file_slugs[@]}"; do
  candidate="/etc/nginx/sites-available/nf-site-$file_slug"
  if [ -f "$candidate" ]; then selected_file_slug="$file_slug"; vhost="$candidate"; break; fi
done
if [ -z "$selected_file_slug" ]; then selected_file_slug="${file_slugs[0]}"; vhost="/etc/nginx/sites-available/nf-site-$selected_file_slug"; fi
basic_auth_snippet="/etc/nginx/snippets/nf-basic-auth-$selected_file_slug.conf"
nf_linode_write_cache_snippets
cache_zone=$(nf_linode_ensure_cache_config "$site_path")
nf_linode_install_cache_mu_plugin "$site_path"
rm -f "$site_path/wp-content/mu-plugins/nf-mailpit.php"
tmp=$(mktemp)
{
  cat <<EOF
server {
    listen 80;
    listen [::]:80;
    server_name $host_name;
    return 301 https://$host_name\$request_uri;
}

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name $host_name;
    include /etc/nginx/snippets/nf-wildcard-cert.conf;
    include /etc/nginx/snippets/nf-security-headers.conf;
    include /etc/nginx/snippets/nf-fastcgi-cache-bypass.conf;
    root $site_path;
EOF
  if [ -f "$basic_auth_snippet" ]; then printf '    include %s;\n' "$basic_auth_snippet"; fi
  cat <<EOF
    access_log /var/log/nginx/sites/$selected_file_slug.access.log;
    error_log /var/log/nginx/sites/$selected_file_slug.error.log;
    include /etc/nginx/snippets/nf-wordpress.conf;
    include /etc/nginx/snippets/nf-static-assets.conf;
    location ~ \.php$ { include /etc/nginx/snippets/nf-fastcgi-php.conf; fastcgi_cache $cache_zone; include /etc/nginx/snippets/nf-fastcgi-cache.conf; fastcgi_pass unix:/run/php/php${php_version}-fpm.sock; }
}
EOF
} >"$tmp"
install -m 0644 "$tmp" "$vhost"
rm -f "$tmp"
ln -sf "$vhost" "/etc/nginx/sites-enabled/nf-site-$selected_file_slug"
nginx -t
systemctl reload nginx
systemctl reload "php${php_version}-fpm" || systemctl restart "php${php_version}-fpm"
echo "Linode site platform files repaired."
`)
	return b.String()
}

func linodeRepairInternalHostname(siteID, env string, record map[string]any, target envRemoteSyncTarget) string {
	if host := siteDomainDefaultHostname(record); host != "" {
		return host
	}
	siteName := firstRecordString(record, "name", "slug")
	if siteName == "" && target.TargetRef != "" && strings.HasSuffix(siteID, "."+target.TargetRef) {
		siteName = strings.TrimSuffix(siteID, "."+target.TargetRef)
	}
	baseDomain := ""
	if target.TargetRef != "" && strings.HasPrefix(target.SSHHost, target.TargetRef+".") {
		baseDomain = strings.TrimPrefix(target.SSHHost, target.TargetRef+".")
	}
	if baseDomain == "" {
		if values, err := loadGlobalConfig(); err == nil {
			baseDomain = values["base_domain"]
		}
	}
	if siteName != "" && target.TargetRef != "" && baseDomain != "" {
		return siteEnvHostname(siteName, target.TargetRef, baseDomain, env)
	}
	return normalizeDomainName(hostnameFromURLish(firstRecordString(record, "internal_hostname", "hostname", "url", "site_url", "home_url")))
}

func linodeRepairWarnings(record map[string]any) []string {
	for _, domain := range siteDomainListDomains(record) {
		if domain.management == "external" {
			return []string{"cached external domain vhosts are not rewritten; refresh those domains separately if they predate nf cache support"}
		}
	}
	return nil
}

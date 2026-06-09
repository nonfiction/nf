package cli

// Linode site add planning, SSH script rendering, and cache writes.

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/nonfiction/nf/internal/state"
	"github.com/nonfiction/nf/internal/ui"
)

func ensureSiteNotCached(records []map[string]any, site string) error {
	for _, record := range records {
		if siteEnvMatchesSite(record, site) {
			return ProjectError{Msg: fmt.Sprintf("Site %q already exists in local site cache.", site)}
		}
	}
	return nil
}

func runSSHScript(user, host, script string) error {
	destination := host
	if strings.TrimSpace(user) != "" {
		destination = user + "@" + host
	}
	cmd := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", destination, "sudo", "bash", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runSSHCommand(args []string) error {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	stderr := newExactLineFilterWriter(os.Stderr, wpCLIPasswordlessLoginWarning)
	cmd.Stderr = stderr
	cmd.Stdin = os.Stdin
	err := cmd.Run()
	if flushErr := stderr.Flush(); flushErr != nil && err == nil {
		err = flushErr
	}
	return err
}

func runSSHStdinCommand(args []string, script string) error {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	stderr := newExactLineFilterWriter(os.Stderr, wpCLIPasswordlessLoginWarning)
	cmd.Stderr = stderr
	cmd.Stdin = strings.NewReader(script)
	err := cmd.Run()
	if flushErr := stderr.Flush(); flushErr != nil && err == nil {
		err = flushErr
	}
	return err
}

func runSSHOutput(args []string) ([]byte, error) {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

func renderSiteAddScript(plan siteAddPlan) string {
	q := shellQuoteArg
	phpVersion := firstNonEmpty(plan.PHPVersion, "8.3")
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	b.WriteString("export DEBIAN_FRONTEND=noninteractive\n")
	b.WriteString("if ! command -v wp >/dev/null 2>&1; then\n")
	b.WriteString("  curl -fsSL https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar -o /usr/local/bin/wp\n")
	b.WriteString("  chmod 0755 /usr/local/bin/wp\n")
	b.WriteString("fi\n")
	b.WriteString("install -d -o www-data -g www-data -m 2775 /var/www/sites /var/log/nginx/sites /var/lib/nf\n")
	b.WriteString("install -d -m 0755 /etc/nginx/conf.d\n")
	b.WriteString("cat >/etc/nginx/conf.d/nf-server-names-hash.conf <<'EOF'\n")
	b.WriteString("server_names_hash_bucket_size 128;\nserver_names_hash_max_size 4096;\n")
	b.WriteString("EOF\n")
	b.WriteString("touch /var/lib/nf/sites.json\n")
	b.WriteString("if ! jq empty /var/lib/nf/sites.json >/dev/null 2>&1; then printf '[]\\n' >/var/lib/nf/sites.json; fi\n")
	b.WriteString("target_php_version=$(jq -r '.php_version // .php.version // \"\"' /var/lib/nf/target.json 2>/dev/null || true)\n")
	b.WriteString("if [ -z \"$target_php_version\" ]; then target_php_version=")
	b.WriteString(q(phpVersion))
	b.WriteString("; fi\n")
	b.WriteString("create_env() {\n")
	b.WriteString("  env_name=$1 site_path=$2 db_name=$3 host_name=$4 site_url=$5 site_title=$6 state_target=$7 file_slug=$8\n")
	b.WriteString("  install -d -o www-data -g www-data -m 2775 \"$site_path\"\n")
	b.WriteString("  mariadb -uroot <<SQL\n")
	b.WriteString("CREATE DATABASE IF NOT EXISTS \\`$db_name\\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n")
	b.WriteString("CREATE USER IF NOT EXISTS '$db_name'@'localhost' IDENTIFIED BY '")
	b.WriteString(plan.DBPassword)
	b.WriteString("';\n")
	b.WriteString("ALTER USER '$db_name'@'localhost' IDENTIFIED BY '")
	b.WriteString(plan.DBPassword)
	b.WriteString("';\n")
	b.WriteString("GRANT ALL PRIVILEGES ON \\`$db_name\\`.* TO '$db_name'@'localhost';\n")
	b.WriteString("FLUSH PRIVILEGES;\n")
	b.WriteString("SQL\n")
	b.WriteString("  if [ ! -f \"$site_path/wp-load.php\" ]; then sudo -u www-data wp core download --path=\"$site_path\" --allow-root; fi\n")
	b.WriteString("  sudo -u www-data wp config create --path=\"$site_path\" --dbname=\"$db_name\" --dbuser=\"$db_name\" --dbpass=")
	b.WriteString(q(plan.DBPassword))
	b.WriteString(" --dbhost=localhost --skip-check --force --allow-root\n")
	b.WriteString("  if ! sudo -u www-data wp core is-installed --path=\"$site_path\" --allow-root >/dev/null 2>&1; then\n")
	b.WriteString("    sudo -u www-data wp core install --path=\"$site_path\" --url=\"$site_url\" --title=\"$site_title\" --admin_user=")
	b.WriteString(q(plan.AdminUser))
	b.WriteString(" --admin_password=")
	b.WriteString(q(plan.AdminPassword))
	b.WriteString(" --admin_email=")
	b.WriteString(q(plan.AdminEmail))
	b.WriteString(" --skip-email --allow-root\n")
	b.WriteString("  fi\n")
	b.WriteString("  cat >/etc/nginx/sites-available/nf-site-$file_slug <<EOF\n")
	b.WriteString("server {\n    listen 80;\n    listen [::]:80;\n    server_name $host_name;\n    return 301 https://$host_name\\$request_uri;\n}\n\n")
	b.WriteString("server {\n    listen 443 ssl http2;\n    listen [::]:443 ssl http2;\n    server_name $host_name;\n    include /etc/nginx/snippets/nf-wildcard-cert.conf;\n    include /etc/nginx/snippets/nf-security-headers.conf;\n    root $site_path;\n    access_log /var/log/nginx/sites/$file_slug.access.log;\n    error_log /var/log/nginx/sites/$file_slug.error.log;\n    include /etc/nginx/snippets/nf-wordpress.conf;\n    include /etc/nginx/snippets/nf-static-assets.conf;\n    location ~ \\.php$ { include /etc/nginx/snippets/nf-fastcgi-php.conf; fastcgi_pass unix:/run/php/php${target_php_version}-fpm.sock; }\n}\n")
	b.WriteString("EOF\n")
	b.WriteString("  ln -sf /etc/nginx/sites-available/nf-site-$file_slug /etc/nginx/sites-enabled/nf-site-$file_slug\n")
	b.WriteString("  tmp=$(mktemp)\n")
	b.WriteString("  jq --arg provider linode --arg site_id ")
	b.WriteString(q(plan.SiteID))
	b.WriteString(" --arg name ")
	b.WriteString(q(plan.Site))
	b.WriteString(" --arg env_id \"$state_target\" --arg env \"$env_name\" --arg target ")
	b.WriteString(q(plan.TargetName))
	b.WriteString(" --arg ssh_user ")
	b.WriteString(q(plan.SSHUser))
	b.WriteString(" --arg ssh_host ")
	b.WriteString(q(plan.SSHHost))
	b.WriteString(" --arg ssh_port ")
	b.WriteString(q(firstNonEmpty(mapStringAtPath(plan.Target, "ssh", "port"), firstRecordString(plan.Target, "ssh_port"), "22")))
	b.WriteString(" --arg hostname \"$host_name\" --arg url \"$site_url\" --arg path \"$site_path\" --arg database \"$db_name\" --arg php_version \"$target_php_version\" '\n")
	b.WriteString("    map(select(.site_id != $site_id or .env != $env)) + [{provider:$provider,env_id:$env_id,site_id:$site_id,name:$name,env:$env,target:$target,hostname:$hostname,url:$url,path:$path,database:$database,php_version:$php_version,status:\"active\",ssh:{host:$ssh_host,port:$ssh_port,user:$ssh_user,command:(\"ssh \" + $ssh_user + \"@\" + $ssh_host + \" -p \" + $ssh_port)}}]\n")
	b.WriteString("  ' /var/lib/nf/sites.json >\"$tmp\" && install -o ")
	b.WriteString(q(plan.SSHUser))
	b.WriteString(" -g www-data -m 0664 \"$tmp\" /var/lib/nf/sites.json && rm -f \"$tmp\"\n")
	b.WriteString("}\n")
	for _, env := range plan.Envs {
		stateTarget := linodeEnvID(plan.Site, plan.TargetName, env.Env)
		b.WriteString("create_env ")
		b.WriteString(q(env.Env))
		b.WriteByte(' ')
		b.WriteString(q(env.Path))
		b.WriteByte(' ')
		b.WriteString(q(env.Database))
		b.WriteByte(' ')
		b.WriteString(q(env.Hostname))
		b.WriteByte(' ')
		b.WriteString(q(env.URL))
		b.WriteByte(' ')
		b.WriteString(q(env.Title))
		b.WriteByte(' ')
		b.WriteString(q(stateTarget))
		b.WriteByte(' ')
		b.WriteString(q(envIDFileSlug(stateTarget)))
		b.WriteByte('\n')
	}
	b.WriteString("nginx -t\n")
	b.WriteString("systemctl reload nginx\n")
	b.WriteString("systemctl reload php${target_php_version}-fpm || systemctl restart php${target_php_version}-fpm\n")
	return b.String()
}

func printSiteAddPlan(plan siteAddPlan, mode string) {
	fmt.Println("Add site plan:")
	fmt.Printf("  target: %s\n", plan.TargetName)
	fmt.Printf("  provider: linode\n")
	fmt.Printf("  ssh: %s@%s\n", plan.SSHUser, plan.SSHHost)
	fmt.Printf("  site: %s\n", plan.Site)
	fmt.Printf("  site id: %s\n", plan.SiteID)
	fmt.Printf("  password version: %s\n", firstNonEmpty(plan.PasswordVersion, "0"))
	fmt.Printf("  admin user: %s\n", plan.AdminUser)
	fmt.Printf("  admin email: %s\n", plan.AdminEmail)
	fmt.Printf("  admin password: derived from %s\n", plan.Site)
	for _, env := range plan.Envs {
		fmt.Printf("  env %s:\n", env.Env)
		fmt.Printf("    path: %s\n", env.Path)
		fmt.Printf("    database: %s\n", env.Database)
		if plan.PHPVersion != "" {
			fmt.Printf("    php: %s\n", plan.PHPVersion)
		}
		fmt.Printf("    vhost: %s\n", env.Hostname)
		fmt.Printf("    url: %s\n", env.URL)
	}
	fmt.Printf("  remote state: /var/lib/nf/sites.json\n")
	fmt.Printf("  local state: %s\n", state.StatePath("sites"))
	fmt.Printf("  mode: %s\n", mode)
}

func cmdSiteAdd(args siteAddArgs) int {
	provider, err := siteAddTargetProvider(args.target)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if provider == "kinsta" {
		return cmdKinstaSiteAdd(args)
	}
	if strings.TrimSpace(args.phpVersion) != "" {
		fmt.Fprintln(os.Stderr, "--php only applies to kinsta targets")
		return 1
	}
	if args.execute && args.dryRun {
		fmt.Fprintln(os.Stderr, "Choose either --execute or --dry-run, not both.")
		return 1
	}
	if !args.execute && (args.dryRun || args.nonInteractive) {
		args.dryRun = true
	}
	if args.nonInteractive && args.execute && !args.yes {
		fmt.Fprintln(os.Stderr, "Remote execution requires both --execute and --yes in non-interactive mode.")
		return 1
	}
	plan, err := buildSiteAddPlan(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	existing, err := state.LoadStateRecords("sites")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := ensureSiteNotCached(existing, plan.SiteID); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	willExecute := args.execute || (!args.dryRun && !args.nonInteractive)
	mode := "dry-run"
	if willExecute {
		mode = "execute"
	}
	printSiteAddPlan(plan, mode)
	if !willExecute {
		return 0
	}
	if !args.yes {
		confirmed, err := ui.Confirm(fmt.Sprintf("Add site %q with live and staging envs on target %q?", plan.Site, plan.TargetName), false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	if err := runSSHScriptFn(plan.SSHUser, plan.SSHHost, renderSiteAddScript(plan)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := appendSiteAddRecords(plan); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Site added.")
	return 0
}

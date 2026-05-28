package provision

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/theme"
	"github.com/nonfiction/nf/internal/ui"
)

type Error struct{ Msg string }

func (e Error) Error() string { return e.Msg }

type Args struct {
	Provider          string
	ProjectSlug       string
	ServerName        string
	SiteDomain        string
	Label             string
	Region            string
	Type              string
	Image             string
	SshUser           string
	SshPublicKeyFile  string
	RemoteWpPath      string
	PhpFpmSocket      string
	DbName            string
	DbUser            string
	WpAdminUser       string
	WpAdminEmail      string
	SiteTitle         string
	DnsZone           string
	DnsimpleAccountID string
	WriteCloudInit    string
	NonInteractive    bool
	ShowCloudInit     bool
	Execute           bool
	Yes               bool
	DryRun            bool
}

type Plan struct {
	Provider          string
	ProjectRoot       string
	ProjectSlug       string
	ProjectName       string
	ServerName        string
	Label             string
	Region            string
	LinodeType        string
	Image             string
	SshUser           string
	SshPublicKeyFile  string
	SiteDomain        string
	RemoteWpPath      string
	PhpFpmSocket      string
	DbName            string
	DbUser            string
	WpAdminUser       string
	WpAdminEmail      string
	SiteTitle         string
	DnsZone           string
	DnsimpleAccountID string
	WriteCloudInit    string
	Execute           bool
	Yes               bool
	DryRun            bool
	NonInteractive    bool
	ShowCloudInit     bool
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func slugToTitle(value string) string {
	parts := strings.Split(strings.ReplaceAll(value, "_", "-"), "-")
	titles := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		titles = append(titles, strings.ToUpper(part[:1])+strings.ToLower(part[1:]))
	}
	return strings.Join(titles, " ")
}

func requiredEnv(name string) (string, error) {
	if v := envwizard.Value(name); v != "" {
		return v, nil
	}
	return "", Error{Msg: fmt.Sprintf("Expected %s in the environment or %s.", name, config.EnvFile())}
}

func projectContext() (string, map[string]any, error) {
	root, ok := config.DiscoverProjectRoot("")
	if !ok {
		return "", map[string]any{}, nil
	}
	metadata, err := theme.LoadProjectMetadata(root)
	if err != nil {
		return "", nil, err
	}
	return root, metadata, nil
}

func inferProjectSlug(explicit string, metadata map[string]any, projectRoot string) string {
	if v := strings.TrimSpace(explicit); v != "" {
		return v
	}
	if project, ok := metadata["project"].(map[string]any); ok {
		if v, ok := project["slug"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if strings.TrimSpace(projectRoot) != "" {
		return filepath.Base(projectRoot)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Base(cwd)
}

func inferProjectName(metadata map[string]any, projectSlug string) string {
	if project, ok := metadata["project"].(map[string]any); ok {
		if v, ok := project["name"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return slugToTitle(projectSlug)
}

func provisionRequirements() []envwizard.Requirement {
	return []envwizard.Requirement{
		{Keys: []string{"NF_SECRET_SALT"}, Prompt: "NF_SECRET_SALT (used for derived passwords): ", Secret: true, WriteKey: "NF_SECRET_SALT", Required: true},
		{Keys: []string{"DNSIMPLE_TOKEN"}, Prompt: "DNSimple token: ", Secret: true, WriteKey: "DNSIMPLE_TOKEN", Required: true},
		{Keys: []string{"LINODE_CLI_TOKEN", "LINODE_TOKEN"}, Prompt: "Linode token: ", Secret: true, WriteKey: "LINODE_CLI_TOKEN", Required: true},
	}
}

func defaultRemoteWpPath(projectSlug string) string {
	if projectSlug == "" {
		return ""
	}
	return "/var/www/" + projectSlug
}

func phpFpmServiceName(socketPath string) string {
	return strings.TrimSuffix(filepath.Base(socketPath), ".sock")
}

func cleanPath(value string) string {
	if value == "" {
		return ""
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			if value == "~" {
				return home
			}
			return filepath.Join(home, value[2:])
		}
	}
	return filepath.Clean(value)
}

var cloudInitTemplate = strings.TrimSpace(`#cloud-config
package_update: true
package_upgrade: true
packages:
  - nginx
  - mariadb-server
  - php-fpm
  - php-mysql
  - php-xml
  - php-mbstring
  - php-curl
  - php-zip
  - php-gd
  - php-intl
  - unzip
  - curl
  - certbot
  - python3-certbot-dns-dnsimple
  - composer
  - rsync
  - zip

users:
  - name: __SSH_USER__
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    groups: [sudo]
    ssh_authorized_keys:
      - __SSH_PUBLIC_KEY__

write_files:
  - path: /etc/nginx/sites-available/__SERVER_NAME__
    permissions: '0644'
    content: |
      server {
          listen 80;
          listen [::]:80;
          server_name __SITE_DOMAIN__;
          root __REMOTE_WP_PATH__;
          index index.php index.html;
          client_max_body_size 64M;

          access_log /var/log/nginx/__SERVER_NAME__.access.log;
          error_log /var/log/nginx/__SERVER_NAME__.error.log;

          location / {
              try_files $uri $uri/ /index.php?$args;
          }

          location ~ \.php$ {
              include snippets/fastcgi-php.conf;
              fastcgi_pass unix:__PHP_FPM_SOCKET__;
          }

          location ~* \.(css|js|jpg|jpeg|png|gif|ico|svg|webp)$ {
              expires 7d;
              access_log off;
          }
      }
  - path: /usr/local/bin/__SERVER_NAME__-enable-tls
    permissions: '0755'
    content: |
      #!/usr/bin/env bash
      set -euo pipefail

      cat >/etc/nginx/sites-available/__SERVER_NAME__ <<'EOF'
      server {
          listen 80;
          listen [::]:80;
          server_name __SITE_DOMAIN__;

          return 301 https://$host$request_uri;
      }

      server {
          listen 443 ssl http2;
          listen [::]:443 ssl http2;
          server_name __SITE_DOMAIN__;
          root __REMOTE_WP_PATH__;
          index index.php index.html;
          client_max_body_size 64M;

          ssl_certificate /etc/letsencrypt/live/__SITE_DOMAIN__/fullchain.pem;
          ssl_certificate_key /etc/letsencrypt/live/__SITE_DOMAIN__/privkey.pem;

          access_log /var/log/nginx/__SERVER_NAME__.access.log;
          error_log /var/log/nginx/__SERVER_NAME__.error.log;

          location / {
              try_files $uri $uri/ /index.php?$args;
          }

          location ~ \.php$ {
              include snippets/fastcgi-php.conf;
              fastcgi_pass unix:__PHP_FPM_SOCKET__;
          }

          location ~* \.(css|js|jpg|jpeg|png|gif|ico|svg|webp)$ {
              expires 7d;
              access_log off;
          }
      }
      EOF

      nginx -t
      systemctl reload nginx
  - path: /root/.secrets/certbot/dnsimple.ini
    permissions: '0600'
    content: |
      dns_dnsimple_token = __DNSIMPLE_TOKEN__
      dns_dnsimple_account = __DNSIMPLE_ACCOUNT_ID__

runcmd:
  - mkdir -p __REMOTE_WP_PATH__
  - chown -R __SSH_USER__:www-data __REMOTE_WP_PATH__
  - chmod -R 775 __REMOTE_WP_PATH__
  - bash -lc 'cd /tmp && curl -O https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar && chmod +x wp-cli.phar && mv wp-cli.phar /usr/local/bin/wp'
  - bash -lc 'cd /tmp && curl -O https://wordpress.org/latest.zip && unzip -o latest.zip && rsync -av wordpress/ __REMOTE_WP_PATH__/'
  - chown -R __SSH_USER__:www-data __REMOTE_WP_PATH__
  - bash -lc 'find __REMOTE_WP_PATH__ -type d -exec chmod 775 {} + && find __REMOTE_WP_PATH__ -type f -exec chmod 664 {} +'
  - mysql -e "CREATE DATABASE IF NOT EXISTS __BACKTICK____DB_NAME____BACKTICK__ CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
  - mysql -e "CREATE USER IF NOT EXISTS '__DB_USER__'@'localhost' IDENTIFIED BY '__DB_PASS__';"
  - mysql -e "ALTER USER '__DB_USER__'@'localhost' IDENTIFIED BY '__DB_PASS__';"
  - mysql -e "GRANT ALL PRIVILEGES ON __BACKTICK____DB_NAME____BACKTICK__.* TO '__DB_USER__'@'localhost'; FLUSH PRIVILEGES;"
  - bash -lc 'cd __REMOTE_WP_PATH__ && wp config create --dbname=__DB_NAME__ --dbuser=__DB_USER__ --dbpass=__DB_PASS__ --dbhost=localhost --allow-root --skip-check'
  - bash -lc 'cd __REMOTE_WP_PATH__ && wp config set WP_DEBUG false --raw --type=constant --allow-root && wp config set WP_DEBUG_LOG true --raw --type=constant --allow-root && wp config set WP_DEBUG_DISPLAY false --raw --type=constant --allow-root && wp config set FS_METHOD direct --type=constant --allow-root'
  - rm -f /etc/nginx/sites-enabled/default
  - ln -sf /etc/nginx/sites-available/__SERVER_NAME__ /etc/nginx/sites-enabled/__SERVER_NAME__
  - nginx -t
  - systemctl enable nginx
  - systemctl restart nginx
  - systemctl enable __PHP_FPM_SERVICE__
  - systemctl restart __PHP_FPM_SERVICE__
  - bash -lc 'wp core install --path=__REMOTE_WP_PATH__ --url=__SITE_URL__ --title="__SITE_TITLE__" --admin_user=__WP_ADMIN_USER__ --admin_password=__WP_ADMIN_PASS__ --admin_email=__WP_ADMIN_EMAIL__ --allow-root'
  - bash -lc 'wp option update blog_public 0 --path=__REMOTE_WP_PATH__ --allow-root && wp rewrite structure "/%postname%/" --path=__REMOTE_WP_PATH__ --allow-root && wp rewrite flush --path=__REMOTE_WP_PATH__ --allow-root || true'
  - bash -lc 'certbot certonly --non-interactive --agree-tos --dns-dnsimple --dns-dnsimple-credentials /root/.secrets/certbot/dnsimple.ini -m __WP_ADMIN_EMAIL__ -d __SITE_DOMAIN__ -d *.__SITE_DOMAIN__'
  - /usr/local/bin/__SERVER_NAME__-enable-tls
`)

func renderTemplate(template string, replacements map[string]string) string {
	rendered := template
	keys := make([]string, 0, len(replacements))
	for k := range replacements {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, placeholder := range keys {
		rendered = strings.ReplaceAll(rendered, placeholder, replacements[placeholder])
	}
	return rendered
}

func cloudInitReplacements(plan Plan, sshPublicKey, dbPass, wpAdminPass, dnsimpleToken string) map[string]string {
	return map[string]string{
		"__SSH_USER__":            plan.SshUser,
		"__SSH_PUBLIC_KEY__":      sshPublicKey,
		"__SERVER_NAME__":         plan.ServerName,
		"__SITE_DOMAIN__":         plan.SiteDomain,
		"__SITE_URL__":            "https://" + plan.SiteDomain,
		"__REMOTE_WP_PATH__":      plan.RemoteWpPath,
		"__PHP_FPM_SOCKET__":      plan.PhpFpmSocket,
		"__DB_NAME__":             plan.DbName,
		"__DB_USER__":             plan.DbUser,
		"__DB_PASS__":             dbPass,
		"__WP_ADMIN_USER__":       plan.WpAdminUser,
		"__WP_ADMIN_PASS__":       wpAdminPass,
		"__WP_ADMIN_EMAIL__":      plan.WpAdminEmail,
		"__DNSIMPLE_TOKEN__":      dnsimpleToken,
		"__DNSIMPLE_ACCOUNT_ID__": plan.DnsimpleAccountID,
		"__SITE_TITLE__":          plan.SiteTitle,
		"__PHP_FPM_SERVICE__":     phpFpmServiceName(plan.PhpFpmSocket),
		"__BACKTICK__":            "`",
	}
}

func renderCloudInit(plan Plan, actual bool, dbPass, wpAdminPass, dnsimpleToken string) (string, error) {
	if actual {
		if dbPass == "" || wpAdminPass == "" || dnsimpleToken == "" {
			return "", Error{Msg: "Missing secrets for cloud-init rendering."}
		}
		data, err := os.ReadFile(plan.SshPublicKeyFile)
		if err != nil {
			return "", err
		}
		return renderTemplate(cloudInitTemplate, cloudInitReplacements(plan, strings.TrimSpace(string(data)), dbPass, wpAdminPass, dnsimpleToken)), nil
	}
	return renderTemplate(cloudInitTemplate, cloudInitReplacements(plan, "<ssh public key>", "<derived database password>", "<derived wp admin password>", "<dnsimple token>")), nil
}

func writeText(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func validateActualExecution(plan Plan) error {
	if plan.Provider != "linode" {
		return Error{Msg: fmt.Sprintf("Unsupported provider %q. Only linode is available in this slice.", plan.Provider)}
	}
	if _, err := passwords.SecretSalt(); err != nil {
		return err
	}
	if envwizard.Value("LINODE_CLI_TOKEN") == "" && envwizard.Value("LINODE_TOKEN") == "" {
		return Error{Msg: fmt.Sprintf("Expected LINODE_CLI_TOKEN or LINODE_TOKEN in the environment or %s.", config.EnvFile())}
	}
	if _, err := requiredEnv("DNSIMPLE_TOKEN"); err != nil {
		return err
	}
	if _, err := os.Stat(plan.SshPublicKeyFile); err != nil {
		return Error{Msg: fmt.Sprintf("Missing SSH public key file: %s", plan.SshPublicKeyFile)}
	}
	return nil
}

func linodeTokenEnv() (string, error) {
	if token := envwizard.Value("LINODE_CLI_TOKEN"); token != "" {
		return token, nil
	}
	if token := envwizard.Value("LINODE_TOKEN"); token != "" {
		return token, nil
	}
	return "", Error{Msg: fmt.Sprintf("Expected LINODE_CLI_TOKEN or LINODE_TOKEN in the environment or %s.", config.EnvFile())}
}

func runLinodeCLI(args []string) (map[string]any, error) {
	token, err := linodeTokenEnv()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("linode-cli", append([]string{"--suppress-warnings", "--json"}, args...)...)
	cmd.Env = append(os.Environ(), "LINODE_CLI_TOKEN="+token, "LINODE_TOKEN="+token)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		details := strings.TrimSpace(stderr.String())
		if details == "" {
			details = strings.TrimSpace(stdout.String())
		}
		if details == "" {
			details = "linode-cli failed"
		}
		return nil, Error{Msg: details}
	}
	var payload any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		return nil, Error{Msg: fmt.Sprintf("Unexpected linode-cli JSON output: %v", err)}
	}
	if m, ok := payload.(map[string]any); ok {
		return m, nil
	}
	if list, ok := payload.([]any); ok && len(list) > 0 {
		if m, ok := list[0].(map[string]any); ok {
			return m, nil
		}
	}
	return nil, Error{Msg: "Unexpected Linode CLI response while creating the instance."}
}

func dnsimpleRequest(method, rawURL, token string, payload map[string]any) (map[string]any, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, Error{Msg: fmt.Sprintf("DNSimple API request failed: %s %s: %v", method, rawURL, err)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, Error{Msg: fmt.Sprintf("DNSimple API request failed: %s %s (HTTP %d)\n%s", method, rawURL, resp.StatusCode, string(data))}
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, Error{Msg: fmt.Sprintf("Unexpected DNSimple API response shape: %v", err)}
	}
	return parsed, nil
}

func dnsimpleURL(accountID, path string) string {
	return fmt.Sprintf("https://api.dnsimple.com/v2/%s%s", accountID, path)
}

func findDnsimpleZone(plan Plan, token string) (string, error) {
	if plan.DnsZone != "" {
		return plan.DnsZone, nil
	}
	parts := strings.Split(plan.SiteDomain, ".")
	for i := 0; i < len(parts)-1; i++ {
		candidate := strings.Join(parts[i:], ".")
		encoded := url.PathEscape(candidate)
		_, err := dnsimpleRequest("GET", dnsimpleURL(plan.DnsimpleAccountID, "/zones/"+encoded), token, nil)
		if err == nil {
			return candidate, nil
		}
		if !strings.Contains(err.Error(), "HTTP 404") {
			return "", err
		}
	}
	return "", Error{Msg: fmt.Sprintf("Could not find a matching DNSimple zone for %s", plan.SiteDomain)}
}

func relativeRecordName(fqdn, zone string) string {
	if fqdn == zone {
		return ""
	}
	suffix := "." + zone
	if strings.HasSuffix(fqdn, suffix) {
		return strings.TrimSuffix(fqdn, suffix)
	}
	return fqdn
}

func dnsimpleUpsertARecord(token, accountID, zone, name, ip string) error {
	encodedZone := url.PathEscape(zone)
	payload, err := dnsimpleRequest("GET", dnsimpleURL(accountID, "/zones/"+encodedZone+"/records?type=A"), token, nil)
	if err != nil {
		return err
	}
	rawRecords, ok := payload["data"]
	if !ok {
		rawRecords = payload
	}
	records, ok := rawRecords.([]any)
	if !ok {
		return Error{Msg: "Unexpected DNSimple records response shape."}
	}
	var existing map[string]any
	for _, record := range records {
		if m, ok := record.(map[string]any); ok && fmt.Sprint(m["name"]) == name {
			existing = m
			break
		}
	}
	if existing != nil {
		recordID := fmt.Sprint(existing["id"])
		currentIP := fmt.Sprint(existing["content"])
		if currentIP == ip {
			return nil
		}
		if recordID == "" || recordID == "<nil>" {
			return Error{Msg: "DNSimple record is missing an id."}
		}
		_, err := dnsimpleRequest("PATCH", dnsimpleURL(accountID, "/zones/"+encodedZone+"/records/"+url.PathEscape(recordID)), token, map[string]any{"content": ip, "ttl": 60})
		return err
	}
	_, err = dnsimpleRequest("POST", dnsimpleURL(accountID, "/zones/"+encodedZone+"/records"), token, map[string]any{"name": name, "type": "A", "content": ip, "ttl": 60})
	return err
}

func planLines(plan Plan, cloudInitPath string) []string {
	lines := []string{
		"provider: " + plan.Provider,
		"server name: " + plan.ServerName,
		"label: " + plan.Label,
		"region: " + plan.Region,
		"type: " + plan.LinodeType,
		"image: " + plan.Image,
		"server domain: " + plan.SiteDomain,
		"dns zone: " + firstNonEmpty(plan.DnsZone, "inferred during execution"),
		"dnsimple account id: " + plan.DnsimpleAccountID,
	}
	if cloudInitPath != "" {
		lines = append(lines, "cloud-init preview: "+cloudInitPath)
	}
	return lines
}

func renderPlan(plan Plan, cloudInitPath, cloudInitPreview string) string {
	header := "Provision server dry-run plan"
	if plan.Execute {
		header = "Provision server plan"
	}
	if plan.Execute && plan.NonInteractive && !plan.Yes {
		header = "Provision server blocked (missing --yes)"
	}
	lines := []string{header, ""}
	for _, line := range planLines(plan, cloudInitPath) {
		lines = append(lines, "- "+line)
	}
	if cloudInitPreview != "" {
		lines = append(lines, "", "cloud-init preview:", strings.TrimRight(cloudInitPreview, "\n"))
	}
	return strings.Join(lines, "\n") + "\n"
}

func base64UserData(content string) string {
	return base64.StdEncoding.EncodeToString([]byte(content))
}

func resolveValue(explicit, prompt, defaultValue string, nonInteractive, allowBlank bool) (string, error) {
	if v := strings.TrimSpace(explicit); v != "" {
		return v, nil
	}
	if nonInteractive {
		if allowBlank && defaultValue == "" {
			return "", nil
		}
		return defaultValue, nil
	}
	v, err := ui.PromptString(prompt, defaultValue, allowBlank)
	if err != nil {
		return "", err
	}
	v = strings.TrimSpace(v)
	if v == "" && allowBlank {
		return "", nil
	}
	if v == "" {
		return defaultValue, nil
	}
	return v, nil
}

func BuildPlan(args Args) (Plan, error) {
	nonInteractive := args.NonInteractive
	projectRoot, metadata, err := projectContext()
	if err != nil {
		return Plan{}, err
	}
	provider := firstNonEmpty(args.Provider, "linode")
	projectSlug := inferProjectSlug(args.ProjectSlug, metadata, projectRoot)
	if strings.TrimSpace(projectSlug) == "" {
		projectSlug = "site"
	}
	projectName := inferProjectName(metadata, projectSlug)
	// TODO: move project-aware inference into the future project deploy flow.
	serverName, err := resolveValue(args.ServerName, "Server name: ", "app1", nonInteractive, false)
	if err != nil {
		return Plan{}, err
	}
	label, err := resolveValue(args.Label, "Linode label: ", firstNonEmpty(serverName, projectSlug), nonInteractive, false)
	if err != nil {
		return Plan{}, err
	}
	siteDomain, err := resolveValue(args.SiteDomain, "Server domain: ", serverName+".nfweb.dev", nonInteractive, false)
	if err != nil {
		return Plan{}, err
	}
	region, err := resolveValue(args.Region, "Linode region: ", "ca-central", nonInteractive, false)
	if err != nil {
		return Plan{}, err
	}
	linodeType, err := resolveValue(args.Type, "Linode type: ", "g6-standard-1", nonInteractive, false)
	if err != nil {
		return Plan{}, err
	}
	image, err := resolveValue(args.Image, "Linode image: ", "linode/ubuntu24.04", nonInteractive, false)
	if err != nil {
		return Plan{}, err
	}
	sshUser, err := resolveValue(args.SshUser, "Deployment SSH user: ", "nonfiction", nonInteractive, false)
	if err != nil {
		return Plan{}, err
	}
	sshPublicKeyFile, err := resolveValue(args.SshPublicKeyFile, "SSH public key file: ", "~/.ssh/id_ed25519.pub", nonInteractive, false)
	if err != nil {
		return Plan{}, err
	}
	phpFpmSocket, err := resolveValue(args.PhpFpmSocket, "PHP-FPM socket: ", "/var/run/php/php8.3-fpm.sock", nonInteractive, false)
	if err != nil {
		return Plan{}, err
	}
	dnsimpleAccountID := firstNonEmpty(args.DnsimpleAccountID, envwizard.Value("DNSIMPLE_ACCOUNT_ID"), "14")
	dnsZone := strings.TrimSpace(args.DnsZone)
	writeCloudInit := strings.TrimSpace(args.WriteCloudInit)
	return Plan{
		Provider:          provider,
		ProjectRoot:       projectRoot,
		ProjectSlug:       projectSlug,
		ProjectName:       projectName,
		ServerName:        firstNonEmpty(serverName, "app1"),
		Label:             firstNonEmpty(label, serverName, projectSlug),
		Region:            firstNonEmpty(region, "ca-central"),
		LinodeType:        firstNonEmpty(linodeType, "g6-standard-1"),
		Image:             firstNonEmpty(image, "linode/ubuntu24.04"),
		SshUser:           firstNonEmpty(sshUser, "nonfiction"),
		SshPublicKeyFile:  cleanPath(firstNonEmpty(sshPublicKeyFile, "~/.ssh/id_ed25519.pub")),
		SiteDomain:        firstNonEmpty(siteDomain, firstNonEmpty(serverName, "app1")+".nfweb.dev"),
		RemoteWpPath:      firstNonEmpty(args.RemoteWpPath, defaultRemoteWpPath(projectSlug)),
		PhpFpmSocket:      firstNonEmpty(phpFpmSocket, "/var/run/php/php8.3-fpm.sock"),
		DbName:            firstNonEmpty(args.DbName, projectSlug),
		DbUser:            firstNonEmpty(args.DbUser, projectSlug),
		WpAdminUser:       firstNonEmpty(args.WpAdminUser, "nf-"+projectSlug),
		WpAdminEmail:      firstNonEmpty(args.WpAdminEmail, "web@nonfiction.ca"),
		SiteTitle:         firstNonEmpty(args.SiteTitle, projectName, slugToTitle(projectSlug)),
		DnsZone:           dnsZone,
		DnsimpleAccountID: firstNonEmpty(dnsimpleAccountID, "14"),
		WriteCloudInit:    cleanPath(writeCloudInit),
		Execute:           args.Execute,
		Yes:               args.Yes,
		DryRun:            args.DryRun || !args.Execute,
		NonInteractive:    nonInteractive,
		ShowCloudInit:     args.ShowCloudInit,
	}, nil
}

func preparePlan(plan Plan) (Plan, string, error) {
	preview, err := renderCloudInit(plan, false, "", "", "")
	if err != nil {
		return Plan{}, "", err
	}
	if plan.WriteCloudInit != "" {
		if err := writeText(plan.WriteCloudInit, preview); err != nil {
			return Plan{}, "", err
		}
	}
	previewText := ""
	if plan.ShowCloudInit {
		previewText = preview
	}
	fmt.Print(renderPlan(plan, plan.WriteCloudInit, previewText))
	if plan.NonInteractive {
		if plan.Execute && !plan.Yes {
			return Plan{}, plan.WriteCloudInit, Error{Msg: "Remote execution requires both --execute and --yes."}
		}
		return plan, plan.WriteCloudInit, nil
	}
	if plan.Execute && plan.Yes {
		return plan, plan.WriteCloudInit, nil
	}
	answer, err := ui.Confirm("Execute remote provisioning?", false)
	if err != nil {
		return Plan{}, plan.WriteCloudInit, err
	}
	if !answer {
		return Plan{}, plan.WriteCloudInit, nil
	}
	answer, err = ui.Confirm("This will create a Linode and DNS records. Continue?", false)
	if err != nil {
		return Plan{}, plan.WriteCloudInit, err
	}
	if !answer {
		return Plan{}, plan.WriteCloudInit, nil
	}
	plan.Execute = true
	plan.Yes = true
	plan.DryRun = false
	return plan, plan.WriteCloudInit, nil
}

func upsertStateRecord(path string, candidate map[string]any) error {
	records, err := loadStatePayload(path)
	if err != nil {
		return err
	}
	updated := false
	for i, record := range records {
		if recordMatches(record, candidate) {
			records[i] = candidate
			updated = true
			break
		}
	}
	if !updated {
		records = append(records, candidate)
	}
	return saveStatePayload(path, records)
}

func ProvisionServer(plan Plan) (*struct{ LinodeID, IPv4, DnsZone, ServerStatePath, SiteStatePath string }, error) {
	if plan.Provider != "linode" {
		return nil, Error{Msg: fmt.Sprintf("Unsupported provider %q. Only linode is available in this slice.", plan.Provider)}
	}
	effectivePlan, previewPath, err := preparePlan(plan)
	if err != nil {
		return nil, err
	}
	_ = previewPath
	if !effectivePlan.Execute {
		return nil, nil
	}
	if err := envwizard.Ensure(provisionRequirements(), effectivePlan.NonInteractive); err != nil {
		return nil, err
	}
	if err := validateActualExecution(effectivePlan); err != nil {
		return nil, err
	}
	salt, err := passwords.SecretSalt()
	if err != nil {
		return nil, err
	}
	rootPass := passwords.DerivePassword(effectivePlan.ProjectSlug, "root", salt)
	dbPass := passwords.DerivePassword(effectivePlan.ProjectSlug, "db", salt)
	wpAdminPass := passwords.DerivePassword(effectivePlan.ProjectSlug, "wp", salt)
	dnsimpleToken, err := requiredEnv("DNSIMPLE_TOKEN")
	if err != nil {
		return nil, err
	}
	rendered, err := renderCloudInit(effectivePlan, true, dbPass, wpAdminPass, dnsimpleToken)
	if err != nil {
		return nil, err
	}
	sshPublicKey, err := os.ReadFile(effectivePlan.SshPublicKeyFile)
	if err != nil {
		return nil, err
	}
	linodePayload, err := runLinodeCLI([]string{
		"linodes", "create",
		"--region", effectivePlan.Region,
		"--type", effectivePlan.LinodeType,
		"--image", effectivePlan.Image,
		"--label", effectivePlan.Label,
		"--root_pass", rootPass,
		"--authorized_keys", strings.TrimSpace(string(sshPublicKey)),
		"--metadata.user_data", base64UserData(rendered),
	})
	if err != nil {
		return nil, err
	}
	linodeID := fmt.Sprint(linodePayload["id"])
	var linodeIP string
	switch ipv4 := linodePayload["ipv4"].(type) {
	case []any:
		if len(ipv4) > 0 {
			linodeIP = fmt.Sprint(ipv4[0])
		}
	case string:
		linodeIP = ipv4
	}
	if linodeIP == "" {
		return nil, Error{Msg: "Linode response did not include an IPv4 address."}
	}
	dnsZone, err := findDnsimpleZone(effectivePlan, dnsimpleToken)
	if err != nil {
		return nil, err
	}
	if err := dnsimpleUpsertARecord(dnsimpleToken, effectivePlan.DnsimpleAccountID, dnsZone, relativeRecordName(effectivePlan.SiteDomain, dnsZone), linodeIP); err != nil {
		return nil, err
	}
	if err := dnsimpleUpsertARecord(dnsimpleToken, effectivePlan.DnsimpleAccountID, dnsZone, relativeRecordName("*."+effectivePlan.SiteDomain, dnsZone), linodeIP); err != nil {
		return nil, err
	}
	serverStatePath := filepath.Join(config.StateDir(), "servers.json")
	siteStatePath := filepath.Join(config.StateDir(), "sites.json")
	now := time.Now().UTC().Format(time.RFC3339)
	serverRecord := map[string]any{
		"id":             linodeID,
		"provider":       effectivePlan.Provider,
		"project_slug":   effectivePlan.ProjectSlug,
		"name":           effectivePlan.ServerName,
		"label":          effectivePlan.Label,
		"hostname":       effectivePlan.SiteDomain,
		"status":         "provisioned",
		"linode_id":      linodeID,
		"ipv4":           linodeIP,
		"region":         effectivePlan.Region,
		"type":           effectivePlan.LinodeType,
		"image":          effectivePlan.Image,
		"ssh_user":       effectivePlan.SshUser,
		"remote_wp_path": effectivePlan.RemoteWpPath,
		"dns_zone":       dnsZone,
		"created_at":     now,
	}
	siteRecord := map[string]any{
		"provider":       effectivePlan.Provider,
		"project_slug":   effectivePlan.ProjectSlug,
		"slug":           effectivePlan.ProjectSlug,
		"name":           effectivePlan.ProjectSlug,
		"hostname":       effectivePlan.SiteDomain,
		"site_url":       "https://" + effectivePlan.SiteDomain,
		"server":         effectivePlan.ServerName,
		"label":          effectivePlan.Label,
		"status":         "provisioned",
		"remote_wp_path": effectivePlan.RemoteWpPath,
		"db_name":        effectivePlan.DbName,
		"db_user":        effectivePlan.DbUser,
		"wp_admin_user":  effectivePlan.WpAdminUser,
		"dns_zone":       dnsZone,
		"created_at":     now,
	}
	if err := upsertStateRecord(serverStatePath, serverRecord); err != nil {
		return nil, err
	}
	if err := upsertStateRecord(siteStatePath, siteRecord); err != nil {
		return nil, err
	}
	fmt.Printf("created linode id: %s\n", linodeID)
	fmt.Printf("ipv4: %s\n", linodeIP)
	fmt.Printf("dns zone: %s\n", dnsZone)
	fmt.Printf("cloud-init preview: %s\n", firstNonEmpty(previewPath, "not written"))
	fmt.Printf("state updated: %s\n", serverStatePath)
	fmt.Printf("state updated: %s\n", siteStatePath)
	return &struct{ LinodeID, IPv4, DnsZone, ServerStatePath, SiteStatePath string }{linodeID, linodeIP, dnsZone, serverStatePath, siteStatePath}, nil
}

func loadStatePayload(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []map[string]any{}, nil
		}
		return nil, err
	}
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	switch typed := payload.(type) {
	case []any:
		records := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				records = append(records, m)
			}
		}
		return records, nil
	case map[string]any:
		base := filepath.Base(strings.TrimSuffix(path, filepath.Ext(path)))
		if records, ok := typed[base].([]any); ok {
			out := make([]map[string]any, 0, len(records))
			for _, item := range records {
				if m, ok := item.(map[string]any); ok {
					out = append(out, m)
				}
			}
			return out, nil
		}
		allMaps := true
		for _, value := range typed {
			if _, ok := value.(map[string]any); !ok {
				allMaps = false
				break
			}
		}
		if allMaps {
			out := make([]map[string]any, 0, len(typed))
			for name, value := range typed {
				record := cloneMap(value.(map[string]any))
				record["_state_key"] = name
				out = append(out, record)
			}
			return out, nil
		}
	}
	return nil, Error{Msg: fmt.Sprintf("Unsupported JSON shape in %s", path)}
}

func cloneMap(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func saveStatePayload(path string, records []map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func recordMatches(record, candidate map[string]any) bool {
	for _, key := range []string{"linode_id", "hostname", "name", "slug", "label"} {
		left, lok := record[key]
		right, rok := candidate[key]
		if lok && rok && fmt.Sprint(left) == fmt.Sprint(right) {
			return true
		}
	}
	return false
}

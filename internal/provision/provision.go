package provision

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/ui"
)

type Error struct{ Msg string }

func (e Error) Error() string { return e.Msg }

type Args struct {
	Provider          string
	DnsProvider       string
	DnsZone           string
	UbuntuVersion     string
	Firewall          string
	FirewallID        string
	Name              string
	Hostname          string
	Label             string
	Region            string
	Type              string
	Image             string
	SshUser           string
	SshKeySource      string
	SshKeyLabel       string
	SshKeyID          string
	AllLinodeSshKeys  bool
	SshPublicKeyFile  string
	DnsimpleAccountID string
	Wait              bool
	NoWait            bool
	SshTimeout        time.Duration
	CloudInitTimeout  time.Duration
	TLSTimeout        time.Duration
	HealthTimeout     time.Duration
	WriteCloudInit    string
	NonInteractive    bool
	ShowCloudInit     bool
	Execute           bool
	Yes               bool
	DryRun            bool
}

type OSPlan struct {
	UbuntuVersion string   `json:"ubuntu_version"`
	Label         string   `json:"label"`
	Image         string   `json:"image"`
	PackageSource string   `json:"package_source"`
	Packages      []string `json:"packages,omitempty"`
}

type PHPPlan struct {
	Version       string   `json:"version"`
	PackageSource string   `json:"package_source"`
	Service       string   `json:"service"`
	Socket        string   `json:"socket"`
	Packages      []string `json:"packages"`
}

type FirewallRulePlan struct {
	Label    string `json:"label"`
	Action   string `json:"action"`
	Protocol string `json:"protocol"`
	Ports    string `json:"ports"`
}

type FirewallPlan struct {
	Mode           string             `json:"mode"`
	ID             string             `json:"id,omitempty"`
	DeviceID       string             `json:"device_id,omitempty"`
	Label          string             `json:"label,omitempty"`
	InboundPolicy  string             `json:"inbound_policy,omitempty"`
	OutboundPolicy string             `json:"outbound_policy,omitempty"`
	Rules          []FirewallRulePlan `json:"rules,omitempty"`
}

type Plan struct {
	Provider          string
	DnsProvider       string
	DnsZone           string
	Domain            string
	UbuntuVersion     string
	PHPVersion        string
	Firewall          FirewallPlan
	Name              string
	Hostname          string
	Label             string
	WildcardHostname  string
	HealthURL         string
	Region            string
	LinodeType        string
	Image             string
	SshUser           string
	SshKeySource      string
	SshKeyLabel       string
	SshKeyID          string
	AllLinodeSshKeys  bool
	SshPublicKeyFile  string
	DnsimpleAccountID string
	Wait              bool
	SshTimeout        time.Duration
	CloudInitTimeout  time.Duration
	TLSTimeout        time.Duration
	HealthTimeout     time.Duration
	WriteCloudInit    string
	OS                OSPlan
	PHP               PHPPlan
	AuthorizedKeys    []SSHAuthorizedKey
	Execute           bool
	Yes               bool
	DryRun            bool
	NonInteractive    bool
	ShowCloudInit     bool
}

type ubuntuRelease struct {
	version string
	label   string
	image   string
	php     string
}

type ubuntuStack struct {
	version   string
	label     string
	image     string
	php       string
	menuLabel string
}

const packageSourceUbuntuNative = "ubuntu-native"
const firewallManagedLabel = "nf-web"
const firewallInboundPolicy = "DROP"
const firewallOutboundPolicy = "ACCEPT"

var firewallInboundPorts = []string{"22", "80", "443"}

var ubuntuStackMatrix = []ubuntuStack{
	{version: "26.04", label: "Ubuntu 26.04 LTS", image: "linode/ubuntu26.04", php: "8.5", menuLabel: "Ubuntu 26.04 LTS / PHP 8.5"},
	{version: "24.04", label: "Ubuntu 24.04 LTS", image: "linode/ubuntu24.04", php: "8.3", menuLabel: "Ubuntu 24.04 LTS / PHP 8.3 recommended/default"},
	{version: "22.04", label: "Ubuntu 22.04 LTS", image: "linode/ubuntu22.04", php: "8.1", menuLabel: "Ubuntu 22.04 LTS / PHP 8.1 legacy compatibility"},
	{version: "20.04", label: "Ubuntu 20.04 LTS legacy/ESM", image: "linode/ubuntu20.04", php: "7.4", menuLabel: "Ubuntu 20.04 LTS / PHP 7.4 legacy/ESM only"},
}

func supportedUbuntuVersions() []string {
	return []string{"26.04", "24.04", "22.04", "20.04"}
}

func releaseForUbuntu(version string) (ubuntuRelease, error) {
	trimmed := strings.TrimSpace(version)
	for _, stack := range ubuntuStackMatrix {
		if stack.version == trimmed {
			return ubuntuRelease{version: stack.version, label: stack.label, image: stack.image, php: stack.php}, nil
		}
	}
	return ubuntuRelease{}, Error{Msg: fmt.Sprintf("Unsupported Ubuntu LTS version %q. Supported versions: %s.", version, strings.Join(supportedUbuntuVersions(), ", "))}
}

func phpPackages(version string) []string {
	prefix := "php" + version + "-"
	return []string{
		prefix + "fpm",
		prefix + "cli",
		prefix + "common",
		prefix + "mysql",
		prefix + "curl",
		prefix + "gd",
		prefix + "imagick",
		prefix + "intl",
		prefix + "mbstring",
		prefix + "xml",
		prefix + "zip",
		prefix + "bcmath",
		prefix + "soap",
		prefix + "opcache",
		prefix + "readline",
	}
}

func firewallRules() []FirewallRulePlan {
	return []FirewallRulePlan{
		{Label: "allow-ssh", Action: "ACCEPT", Protocol: "TCP", Ports: "22"},
		{Label: "allow-http", Action: "ACCEPT", Protocol: "TCP", Ports: "80"},
		{Label: "allow-https", Action: "ACCEPT", Protocol: "TCP", Ports: "443"},
	}
}

func managedFirewallPlan(id string) FirewallPlan {
	return FirewallPlan{
		Mode:           "managed",
		ID:             strings.TrimSpace(id),
		Label:          firewallManagedLabel,
		InboundPolicy:  firewallInboundPolicy,
		OutboundPolicy: firewallOutboundPolicy,
		Rules:          firewallRules(),
	}
}

func firewallRulesPayload() []map[string]any {
	rules := make([]map[string]any, 0, len(firewallInboundPorts))
	for _, port := range firewallInboundPorts {
		var label string
		switch port {
		case "22":
			label = "allow-ssh"
		case "80":
			label = "allow-http"
		case "443":
			label = "allow-https"
		}
		rules = append(rules, map[string]any{
			"label":    label,
			"action":   "ACCEPT",
			"protocol": "TCP",
			"ports":    port,
			"addresses": map[string]any{
				"ipv4": []string{"0.0.0.0/0"},
				"ipv6": []string{"::/0"},
			},
		})
	}
	return rules
}

func firewallRulesJSON() (string, error) {
	data, err := json.Marshal(firewallRulesPayload())
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func rootCredentialState(hostname string) map[string]any {
	return map[string]any{
		"derived":  true,
		"identity": hostname,
		"purpose":  "linode-root",
		"stored":   false,
	}
}

func phpReleaseForUbuntu(version string) (PHPPlan, error) {
	release, err := releaseForUbuntu(version)
	if err != nil {
		return PHPPlan{}, err
	}
	phpVersion := release.php
	return PHPPlan{
		Version:       phpVersion,
		PackageSource: packageSourceUbuntuNative,
		Service:       "php" + phpVersion + "-fpm",
		Socket:        filepath.Clean("/run/php/php" + phpVersion + "-fpm.sock"),
		Packages:      phpPackages(phpVersion),
	}, nil
}

func osReleasePlan(version, overrideImage string) (OSPlan, error) {
	release, err := releaseForUbuntu(version)
	if err != nil {
		return OSPlan{}, err
	}
	image := release.image
	if strings.TrimSpace(overrideImage) != "" {
		image = strings.TrimSpace(overrideImage)
	}
	return OSPlan{
		UbuntuVersion: release.version,
		Label:         release.label,
		Image:         image,
		PackageSource: packageSourceUbuntuNative,
		Packages:      serverBasePackages(),
	}, nil
}

func stackOptions() []ui.SelectOption {
	options := make([]ui.SelectOption, 0, len(ubuntuStackMatrix))
	for _, stack := range ubuntuStackMatrix {
		options = append(options, ui.SelectOption{Label: stack.menuLabel, Value: stack.version, Default: stack.version == "24.04"})
	}
	return options
}

func selectUbuntuStack(explicit string, nonInteractive bool) (ubuntuRelease, error) {
	if v := strings.TrimSpace(explicit); v != "" {
		return releaseForUbuntu(v)
	}
	if nonInteractive {
		return releaseForUbuntu("24.04")
	}
	selected, err := selectVersionFn("Choose an Ubuntu/PHP stack", stackOptions())
	if err != nil {
		return ubuntuRelease{}, err
	}
	return releaseForUbuntu(selected)
}

func packageLines(packages []string) []string {
	lines := make([]string, 0, len(packages))
	for _, pkg := range packages {
		lines = append(lines, "    - "+pkg)
	}
	return lines
}

func joinedPackages(packages []string) string { return strings.Join(packages, ", ") }

func apiIDString(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "", fmt.Errorf("missing id")
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" || trimmed == "<nil>" {
			return "", fmt.Errorf("missing id")
		}
		if parsed, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return strconv.FormatInt(parsed, 10), nil
		}
		if parsed, err := strconv.ParseFloat(trimmed, 64); err == nil {
			if math.Trunc(parsed) != parsed {
				return "", fmt.Errorf("id %q is not an integer", typed)
			}
			return strconv.FormatInt(int64(parsed), 10), nil
		}
		return "", fmt.Errorf("id %q is not numeric", typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return strconv.FormatInt(parsed, 10), nil
		}
		f, err := typed.Float64()
		if err != nil {
			return "", fmt.Errorf("id %q is not numeric", typed.String())
		}
		if math.Trunc(f) != f {
			return "", fmt.Errorf("id %q is not an integer", typed.String())
		}
		return strconv.FormatInt(int64(f), 10), nil
	case float64:
		if math.Trunc(typed) != typed {
			return "", fmt.Errorf("id %v is not an integer", typed)
		}
		return strconv.FormatInt(int64(typed), 10), nil
	case float32:
		f := float64(typed)
		if math.Trunc(f) != f {
			return "", fmt.Errorf("id %v is not an integer", typed)
		}
		return strconv.FormatInt(int64(f), 10), nil
	case int:
		return strconv.Itoa(typed), nil
	case int8:
		return strconv.FormatInt(int64(typed), 10), nil
	case int16:
		return strconv.FormatInt(int64(typed), 10), nil
	case int32:
		return strconv.FormatInt(int64(typed), 10), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case uint:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint64:
		return strconv.FormatUint(typed, 10), nil
	default:
		return "", fmt.Errorf("unsupported id type %T", value)
	}
}

func valueString(value any) string {
	if id, err := apiIDString(value); err == nil {
		return id
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "<nil>" {
		return ""
	}
	return text
}

func waitForTCPPort(host string, port int, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	address := net.JoinHostPort(host, strconv.Itoa(port))
	lastErr := error(nil)
	for {
		conn, err := net.DialTimeout("tcp", address, 10*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return Error{Msg: fmt.Sprintf("Timed out waiting for %s:%d to accept TCP connections: %v", host, port, lastErr)}
		}
		time.Sleep(5 * time.Second)
	}
}

func runSSHCommand(user, host, command string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=2",
		user + "@" + host,
		command,
	}
	cmd := exec.CommandContext(ctx, "ssh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		details := strings.TrimSpace(stderr.String())
		if details == "" {
			details = strings.TrimSpace(stdout.String())
		}
		if details == "" {
			details = err.Error()
		}
		return Error{Msg: details}
	}
	return nil
}

func checkHTTPSHealth(url, name, hostname string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	client := healthHTTPClientFn()
	if client == nil {
		client = defaultHealthHTTPClient()
	}
	nameLower := strings.ToLower(strings.TrimSpace(name))
	hostnameLower := strings.ToLower(strings.TrimSpace(hostname))
	var lastErr error
	for {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8192))
			_ = resp.Body.Close()
			if readErr != nil {
				lastErr = readErr
			} else {
				text := strings.ToLower(string(body))
				if resp.StatusCode == http.StatusOK && strings.Contains(text, "ready") && (strings.Contains(text, nameLower) || strings.Contains(text, hostnameLower)) {
					return nil
				}
				lastErr = Error{Msg: fmt.Sprintf("unexpected health response: status=%d body=%s", resp.StatusCode, dnsimpleResponseExcerpt(body))}
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return Error{Msg: fmt.Sprintf("Timed out waiting for %s to report ready: %v", url, lastErr)}
		}
		time.Sleep(5 * time.Second)
	}
}

func defaultHealthHTTPClient() *http.Client { return &http.Client{Timeout: 20 * time.Second} }

func sshAuthorizedKeyMetadata(keys []SSHAuthorizedKey) []map[string]any {
	if len(keys) == 0 {
		return nil
	}
	metadata := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		entry := map[string]any{"source": key.Source}
		if key.ID != "" {
			entry["id"] = key.ID
		}
		if key.Label != "" {
			entry["label"] = key.Label
		}
		if key.Fingerprint != "" {
			entry["fingerprint"] = key.Fingerprint
		}
		if key.Path != "" {
			entry["path"] = key.Path
		}
		if key.Created != "" {
			entry["created"] = key.Created
		}
		metadata = append(metadata, entry)
	}
	return metadata
}

func sshKeyBodies(keys []SSHAuthorizedKey) []string {
	bodies := make([]string, 0, len(keys))
	for _, key := range keys {
		if body := strings.TrimSpace(key.PublicKey); body != "" {
			bodies = append(bodies, body)
		}
	}
	return bodies
}

func appendLinodeAuthorizedKeyArgs(args []string, keys []SSHAuthorizedKey) []string {
	for _, body := range sshKeyBodies(keys) {
		args = append(args, "--authorized_keys", body)
	}
	return args
}

func sshKeySummary(plan Plan) string {
	source := firstNonEmpty(plan.SshKeySource, "linode-profile")
	switch source {
	case "file":
		if plan.SshPublicKeyFile != "" {
			return plan.SshPublicKeyFile
		}
		return "file"
	default:
		parts := []string{"all Linode profile keys"}
		if plan.AllLinodeSshKeys {
			parts = []string{"all Linode profile keys"}
		}
		if plan.SshKeyLabel != "" {
			parts = append(parts, "label "+plan.SshKeyLabel)
		}
		if plan.SshKeyID != "" {
			parts = append(parts, "id "+plan.SshKeyID)
		}
		return strings.Join(parts, "; ")
	}
}

func sshKeyLabels(keys []SSHAuthorizedKey) string {
	labels := make([]string, 0, len(keys))
	for _, key := range keys {
		label := strings.TrimSpace(key.Label)
		if label == "" {
			label = strings.TrimSpace(key.ID)
		}
		if label == "" {
			label = strings.TrimSpace(key.Path)
		}
		if label != "" {
			labels = append(labels, label)
		}
	}
	return strings.Join(labels, ", ")
}

type CreatedServer struct {
	Provider   string `json:"provider"`
	ProviderID string `json:"provider_id"`
	Name       string `json:"name"`
	Hostname   string `json:"hostname"`
	IPv4       string `json:"ipv4"`
}

type SSHAuthorizedKey struct {
	Source      string `json:"source"`
	ID          string `json:"id,omitempty"`
	Label       string `json:"label,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Path        string `json:"path,omitempty"`
	Created     string `json:"created,omitempty"`
	PublicKey   string `json:"-"`
}

type DNSRecord struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

type DNSState struct {
	Provider       string    `json:"provider"`
	Zone           string    `json:"zone"`
	HostnameRecord DNSRecord `json:"hostname_record"`
	WildcardRecord DNSRecord `json:"wildcard_record"`
}

type TLSState struct {
	Provider    string   `json:"provider"`
	Domains     []string `json:"domains"`
	Certificate string   `json:"certificate"`
	Key         string   `json:"key"`
}

type ServerCreateResult struct {
	Server        CreatedServer
	DNS           DNSState
	TLS           TLSState
	StatePath     string
	CloudInitPath string
}

var (
	runLinodeCLICommand      = runLinodeCLI
	runLinodeCLIValueFn      = runLinodeCLIValue
	dnsimpleZoneLookup       = findDnsimpleZone
	dnsimpleRequestFn        = dnsimpleRequest
	dnsimpleListARecordsFn   = dnsimpleListARecords
	dnsimpleUpsertARecordRun = dnsimpleUpsertARecord
	waitForTCPPortFn         = waitForTCPPort
	runSSHCommandFn          = runSSHCommand
	checkHTTPSHealthFn       = checkHTTPSHealth
	healthHTTPClientFn       = defaultHealthHTTPClient
	selectVersionFn          = ui.Select
	promptStringFn           = ui.PromptString
	confirmFn                = ui.Confirm
	multiSelectFn            = ui.MultiSelect
	secretSaltFn             = passwords.SecretSalt
	currentTime              = time.Now
)

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

func serverDomain() string {
	return firstNonEmpty(envwizard.Value("NF_SERVER_DOMAIN"), envwizard.Value("DNSIMPLE_ZONE_NAME"), "nfweb.dev")
}

func validateServerName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return Error{Msg: "Server name cannot be empty. Use 1-63 lowercase letters, digits, and hyphens, starting and ending with a letter or digit."}
	}
	if len(trimmed) > 63 {
		return Error{Msg: fmt.Sprintf("Invalid server name %q: must be 63 characters or fewer.", name)}
	}
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return Error{Msg: fmt.Sprintf("Invalid server name %q. Use lowercase letters, digits, and hyphens only; no dots, underscores, spaces, or uppercase letters.", name)}
		}
	}
	if trimmed[0] == '-' || trimmed[len(trimmed)-1] == '-' {
		return Error{Msg: fmt.Sprintf("Invalid server name %q. It must start and end with a letter or digit.", name)}
	}
	return nil
}

func deriveHostname(name, domain string) string {
	name = strings.TrimSpace(name)
	domain = strings.TrimSpace(domain)
	if name == "" || domain == "" {
		return ""
	}
	return name + "." + domain
}

func deriveWildcardHostname(hostname string) string {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return ""
	}
	return "*." + hostname
}

func deriveHealthURL(hostname string) string {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return ""
	}
	return "https://" + hostname
}

func provisionRequirements() []envwizard.Requirement {
	return []envwizard.Requirement{
		{Keys: []string{"NF_SECRET_SALT"}, Prompt: "NF_SECRET_SALT (used for derived passwords): ", Secret: true, WriteKey: "NF_SECRET_SALT", Required: true},
		{Keys: []string{"DNSIMPLE_TOKEN"}, Prompt: "DNSimple token: ", Secret: true, WriteKey: "DNSIMPLE_TOKEN", Required: true},
		{Keys: []string{"LINODE_CLI_TOKEN", "LINODE_TOKEN"}, Prompt: "Linode token: ", Secret: true, WriteKey: "LINODE_CLI_TOKEN", Required: true},
	}
}

func shortHostname(hostname string) string {
	trimmed := strings.TrimSpace(hostname)
	if trimmed == "" {
		return ""
	}
	if idx := strings.IndexByte(trimmed, '.'); idx >= 0 {
		return trimmed[:idx]
	}
	return trimmed
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
hostname: __SHORT_HOSTNAME__
fqdn: __HOSTNAME__
preserve_hostname: false
manage_etc_hosts: true
timezone: UTC
package_update: true
package_upgrade: true
ssh_pwauth: false
swap:
  filename: /swapfile
  size: 2G
packages:
__PACKAGE_LIST__

users:
  - name: __SSH_USER__
    shell: /bin/bash
    groups: [sudo, www-data]
    sudo: ALL=(ALL) NOPASSWD:ALL
    lock_passwd: true
    ssh_authorized_keys:
__SSH_PUBLIC_KEYS__

write_files:
  - path: /etc/ssh/sshd_config.d/99-nf-hardening.conf
    permissions: '0644'
    content: |
      PasswordAuthentication no
      PubkeyAuthentication yes
      PermitRootLogin prohibit-password
      KbdInteractiveAuthentication no
      ChallengeResponseAuthentication no
  - path: /etc/apt/apt.conf.d/20auto-upgrades
    permissions: '0644'
    content: |
      APT::Periodic::Update-Package-Lists "1";
      APT::Periodic::Unattended-Upgrade "1";
  - path: /etc/php/__PHP_VERSION__/fpm/conf.d/99-nf-wordpress.ini
    permissions: '0644'
    content: |
      memory_limit = 256M
      upload_max_filesize = 64M
      post_max_size = 64M
      max_execution_time = 120
      max_input_time = 120
      max_input_vars = 5000
      cgi.fix_pathinfo = 0
      expose_php = Off
      date.timezone = UTC
      opcache.enable = 1
      opcache.memory_consumption = 128
      opcache.max_accelerated_files = 10000
  - path: /etc/nginx/snippets/nf-fastcgi-php.conf
    permissions: '0644'
    content: |
      include fastcgi_params;
      fastcgi_split_path_info ^(.+\.php)(/.*)$;
      fastcgi_index index.php;
      fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
      fastcgi_param PATH_INFO $fastcgi_path_info;
  - path: /etc/nginx/snippets/nf-wordpress.conf
    permissions: '0644'
    content: |
      index index.php index.html;
      location / {
          try_files $uri $uri/ /index.php?$args;
      }
  - path: /etc/nginx/snippets/nf-static-assets.conf
    permissions: '0644'
    content: |
      location ~* \.(?:css|js|mjs|map|jpg|jpeg|png|gif|ico|svg|webp|avif|woff|woff2|ttf|otf)$ {
          expires 30d;
          access_log off;
          add_header Cache-Control "public, max-age=2592000, immutable";
      }
  - path: /etc/nginx/snippets/nf-security-headers.conf
    permissions: '0644'
    content: |
      add_header X-Content-Type-Options nosniff always;
      add_header X-Frame-Options SAMEORIGIN always;
      add_header Referrer-Policy strict-origin-when-cross-origin always;
      add_header Permissions-Policy "geolocation=(), microphone=(), camera=()" always;
  - path: /etc/nginx/snippets/nf-wildcard-cert.conf
    permissions: '0644'
    content: |
      ssl_certificate /etc/letsencrypt/live/__HOSTNAME__/fullchain.pem;
      ssl_certificate_key /etc/letsencrypt/live/__HOSTNAME__/privkey.pem;
      ssl_trusted_certificate /etc/letsencrypt/live/__HOSTNAME__/fullchain.pem;
      ssl_session_cache shared:NFSSL:10m;
      ssl_session_timeout 10m;
      ssl_protocols TLSv1.2 TLSv1.3;
      ssl_prefer_server_ciphers off;
  - path: /etc/nginx/sites-available/nf-server
    permissions: '0644'
    content: |
      server {
          listen 80;
          listen [::]:80;
          server_name __HOSTNAME__;

          root /var/www/nf-server;
          index index.html;

          location = /healthz {
              default_type application/json;
              return 200 '{ "server": "__NAME__", "hostname": "__HOSTNAME__", "status": "ready" }';
          }

          location / {
              try_files $uri $uri/ /index.html;
          }
      }
  - path: /usr/local/bin/nf-write-server-health-page
    permissions: '0755'
    content: |
      #!/usr/bin/env bash
      set -euo pipefail

      install -d -o __SSH_USER__ -g www-data -m 2775 /var/www/nf-server
      cat >/var/www/nf-server/index.html <<'EOF'
      <!doctype html>
      <html lang="en">
      <head>
        <meta charset="utf-8">
        <meta name="viewport" content="width=device-width, initial-scale=1">
        <title>nf server __NAME__</title>
        <style>
          :root {
            color-scheme: light;
          }

          body {
            margin: 0;
            min-height: 100vh;
            display: grid;
            place-items: center;
            background: #f4f7fb;
            color: #0f172a;
            font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
          }

          main {
            width: min(92vw, 28rem);
            padding: 2rem;
            border-radius: 1.25rem;
            background: #fff;
            box-shadow: 0 1rem 3rem rgba(15, 23, 42, 0.12);
            text-align: center;
          }

          .pill {
            display: inline-block;
            margin-bottom: 1rem;
            padding: 0.35rem 0.8rem;
            border-radius: 999px;
            background: #dcfce7;
            color: #166534;
            font-size: 0.875rem;
            font-weight: 700;
            letter-spacing: 0.04em;
            text-transform: uppercase;
          }

          h1 {
            margin: 0 0 0.75rem;
            font-size: clamp(2rem, 5vw, 3rem);
            line-height: 1.1;
          }

          p {
            margin: 0.5rem 0;
            font-size: 1rem;
          }

          .label {
            color: #475569;
          }
        </style>
      </head>
      <body>
        <main>
          <div class="pill">ready</div>
          <h1>nf server __NAME__</h1>
          <p><span class="label">hostname:</span> __HOSTNAME__</p>
          <p><span class="label">health:</span> https://__HOSTNAME__</p>
        </main>
      </body>
      </html>
      EOF
  - path: /etc/letsencrypt/renewal-hooks/deploy/reload-nginx
    permissions: '0755'
    content: |
      #!/usr/bin/env bash
      set -euo pipefail

      systemctl reload nginx
  - path: /usr/local/bin/nf-enable-wildcard-tls
    permissions: '0755'
    content: |
      #!/usr/bin/env bash
      set -euo pipefail

      certbot certonly --non-interactive --agree-tos --dns-dnsimple --dns-dnsimple-credentials /root/.secrets/certbot/dnsimple.ini -m web@nonfiction.ca -d __HOSTNAME__ -d "*.__HOSTNAME__"

      cat >/etc/nginx/sites-available/nf-server <<'EOF'
      server {
          listen 80;
          listen [::]:80;
          server_name __HOSTNAME__;

          root /var/www/nf-server;
          index index.html;

          location = /healthz {
              default_type application/json;
              return 200 '{ "server": "__NAME__", "hostname": "__HOSTNAME__", "status": "ready" }';
          }

          location / {
              try_files $uri $uri/ /index.html;
          }
      }

      server {
          listen 443 ssl http2;
          listen [::]:443 ssl http2;
          server_name __HOSTNAME__;

          include /etc/nginx/snippets/nf-wildcard-cert.conf;
          root /var/www/nf-server;
          index index.html;

          location = /healthz {
              default_type application/json;
              return 200 '{ "server": "__NAME__", "hostname": "__HOSTNAME__", "status": "ready" }';
          }

          location / {
              try_files $uri $uri/ /index.html;
          }
      }
      EOF

      nginx -t
      systemctl reload nginx
  - path: /usr/local/bin/nf-write-server-marker
    permissions: '0755'
    content: |
      #!/usr/bin/env bash
      set -euo pipefail

      mkdir -p /etc/nf
      created_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
      cat >/etc/nf/server.json <<EOF
      {
        "schema": 1,
        "managed_by": "nf",
        "name": "__NAME__",
        "hostname": "__HOSTNAME__",
        "server_provider": "__SERVER_PROVIDER__",
        "dns_provider": "__DNS_PROVIDER__",
        "ubuntu_version": "__UBUNTU_VERSION__",
        "image": "__IMAGE__",
        "php_version": "__PHP_VERSION__",
        "php_service": "__PHP_FPM_SERVICE__",
        "php_socket": "__PHP_FPM_SOCKET__",
        "sites_root": "/var/www/sites",
        "created_at": "${created_at}"
      }
      EOF
  - path: /etc/update-motd.d/99-nf
    permissions: '0755'
    content: |
      #!/usr/bin/env bash
      set -euo pipefail

      echo 'Managed by nf'
      echo 'Server: __NAME__'
      echo 'Hostname: __HOSTNAME__'
      echo 'PHP: __PHP_VERSION__ (__PHP_FPM_SERVICE__)'
      echo 'Sites root: /var/www/sites'
  - path: /root/.secrets/certbot/dnsimple.ini
    permissions: '0600'
    content: |
      dns_dnsimple_token = __DNSIMPLE_TOKEN__
      dns_dnsimple_account = __DNSIMPLE_ACCOUNT_ID__

runcmd:
  - hostnamectl set-hostname __HOSTNAME__
  - systemctl enable --now mariadb
  - systemctl enable --now fail2ban
  - install -d -o __SSH_USER__ -g www-data -m 2775 /var/www /var/www/nf-server /var/www/sites /var/www/shared /var/log/nginx/sites
  - usermod -aG www-data __SSH_USER__
  - chown -R __SSH_USER__:www-data /var/www /var/log/nginx/sites
  - chmod 2775 /var/www /var/www/sites /var/www/shared /var/log/nginx/sites
  - rm -f /etc/nginx/sites-enabled/default
  - ln -sf /etc/nginx/sites-available/nf-server /etc/nginx/sites-enabled/nf-server
  - /usr/local/bin/nf-write-server-health-page
  - nginx -t
  - systemctl enable nginx
  - systemctl restart nginx
  - systemctl enable __PHP_FPM_SERVICE__
  - systemctl restart __PHP_FPM_SERVICE__
  - ufw default deny incoming
  - ufw default allow outgoing
  - ufw allow 22/tcp
  - ufw allow 80/tcp
  - ufw allow 443/tcp
  - ufw --force enable
  - /usr/local/bin/nf-write-server-marker
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

func serverBasePackages() []string {
	return []string{"nginx", "mariadb-server", "mariadb-client", "unattended-upgrades", "fail2ban", "unzip", "curl", "ufw", "certbot", "python3-certbot-dns-dnsimple", "composer", "rsync", "zip", "imagemagick", "ghostscript", "ca-certificates", "gnupg", "lsb-release", "acl", "htop", "ncdu", "lsof", "iproute2", "dnsutils", "jq", "vim", "git", "logrotate"}
}

func cloudInitPackages(plan Plan) []string {
	return append(serverBasePackages(), plan.PHP.Packages...)
}

func cloudInitPackageBlock(plan Plan) string {
	return strings.Join(packageLines(cloudInitPackages(plan)), "\n")
}

func cloudInitSSHKeyLines(keys []string) string {
	if len(keys) == 0 {
		keys = []string{"<ssh public key>"}
	}
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, "      - "+strings.TrimSpace(key))
	}
	return strings.Join(lines, "\n")
}

func cloudInitReplacements(plan Plan, sshPublicKeys []string, dnsimpleToken string) map[string]string {
	return map[string]string{
		"__PACKAGE_LIST__":        cloudInitPackageBlock(plan),
		"__SHORT_HOSTNAME__":      shortHostname(plan.Hostname),
		"__SSH_USER__":            plan.SshUser,
		"__SSH_PUBLIC_KEYS__":     cloudInitSSHKeyLines(sshPublicKeys),
		"__NAME__":                plan.Name,
		"__HOSTNAME__":            plan.Hostname,
		"__DNSIMPLE_TOKEN__":      dnsimpleToken,
		"__DNSIMPLE_ACCOUNT_ID__": plan.DnsimpleAccountID,
		"__SERVER_PROVIDER__":     plan.Provider,
		"__DNS_PROVIDER__":        plan.DnsProvider,
		"__UBUNTU_VERSION__":      plan.UbuntuVersion,
		"__IMAGE__":               plan.Image,
		"__PHP_FPM_SERVICE__":     plan.PHP.Service,
		"__PHP_FPM_SOCKET__":      plan.PHP.Socket,
		"__PHP_VERSION__":         plan.PHP.Version,
	}
}

func stackSummary(plan Plan) string {
	return plan.OS.Label + " / PHP " + plan.PHP.Version
}

func ubuntuDisplayLabel(plan Plan) string {
	return strings.TrimPrefix(plan.OS.Label, "Ubuntu ")
}

func renderCloudInit(plan Plan, actual bool, dnsimpleToken string, sshPublicKeys []string) (string, error) {
	if actual {
		if dnsimpleToken == "" {
			return "", Error{Msg: "Missing secrets for cloud-init rendering."}
		}
		return renderTemplate(cloudInitTemplate, cloudInitReplacements(plan, sshPublicKeys, dnsimpleToken)), nil
	}
	return renderTemplate(cloudInitTemplate, cloudInitReplacements(plan, sshPublicKeys, "<dnsimple token>")), nil
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
	if _, err := secretSaltFn(); err != nil {
		return err
	}
	if envwizard.Value("LINODE_CLI_TOKEN") == "" && envwizard.Value("LINODE_TOKEN") == "" {
		return Error{Msg: fmt.Sprintf("Expected LINODE_CLI_TOKEN or LINODE_TOKEN in the environment or %s.", config.EnvFile())}
	}
	if _, err := requiredEnv("DNSIMPLE_TOKEN"); err != nil {
		return err
	}
	source := firstNonEmpty(plan.SshKeySource, "linode-profile")
	if source == "file" {
		if _, err := os.Stat(plan.SshPublicKeyFile); err != nil {
			return Error{Msg: fmt.Sprintf("Missing SSH public key file: %s", plan.SshPublicKeyFile)}
		}
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

func runLinodeCLIValue(args []string) (any, error) {
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
	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	dec.UseNumber()
	var payload any
	if err := dec.Decode(&payload); err != nil {
		return nil, Error{Msg: fmt.Sprintf("Unexpected linode-cli JSON output: %v", err)}
	}
	return payload, nil
}

func runLinodeCLI(args []string) (map[string]any, error) {
	payload, err := runLinodeCLIValueFn(args)
	if err != nil {
		return nil, err
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

func linodeListByLabel(label string) ([]map[string]any, error) {
	payload, err := runLinodeCLIValueFn([]string{"linodes", "list", "--json"})
	if err != nil {
		return nil, err
	}
	var items []any
	switch typed := payload.(type) {
	case []any:
		items = typed
	case map[string]any:
		if list, ok := typed["data"].([]any); ok {
			items = list
		} else if list, ok := typed["linodes"].([]any); ok {
			items = list
		}
	}
	matched := make([]map[string]any, 0)
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(valueString(record["label"]), strings.TrimSpace(label)) {
			matched = append(matched, record)
		}
	}
	return matched, nil
}

func linodeIPFromRecord(record map[string]any) string {
	switch ipv4 := record["ipv4"].(type) {
	case []any:
		if len(ipv4) > 0 {
			return valueString(ipv4[0])
		}
	case string:
		return strings.TrimSpace(ipv4)
	}
	return ""
}

func dnsimpleEndpointPath(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	path := parsed.EscapedPath()
	if parsed.RawQuery != "" {
		path += "?" + parsed.RawQuery
	}
	return path
}

func dnsimpleResponseExcerpt(data []byte) string {
	text := strings.Join(strings.Fields(strings.TrimSpace(string(data))), " ")
	if text == "" {
		return ""
	}
	const maxExcerpt = 240
	if len(text) > maxExcerpt {
		text = text[:maxExcerpt] + "..."
	}
	return text
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
		return nil, Error{Msg: fmt.Sprintf("DNSimple API request failed: %s %s: %v", method, dnsimpleEndpointPath(rawURL), err)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		excerpt := dnsimpleResponseExcerpt(data)
		msg := fmt.Sprintf("DNSimple API request failed: %s %s (HTTP %d)", method, dnsimpleEndpointPath(rawURL), resp.StatusCode)
		if excerpt != "" {
			msg += ": " + excerpt
		}
		return nil, Error{Msg: msg}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var parsed map[string]any
	if err := dec.Decode(&parsed); err != nil {
		return nil, Error{Msg: fmt.Sprintf("Unexpected DNSimple API response shape: %v", err)}
	}
	return parsed, nil
}

func dnsimpleURL(accountID, path string) string {
	return fmt.Sprintf("https://api.dnsimple.com/v2/%s%s", accountID, path)
}

func findDnsimpleZone(plan Plan, token string) (string, error) {
	zone := firstNonEmpty(plan.DnsZone, plan.Domain)
	if zone == "" {
		zone = serverDomain()
	}
	encoded := url.PathEscape(zone)
	if _, err := dnsimpleRequestFn("GET", dnsimpleURL(plan.DnsimpleAccountID, "/zones/"+encoded), token, nil); err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return "", Error{Msg: fmt.Sprintf("DNSimple zone %s was not found for account %s. Check NF_SERVER_DOMAIN, DNSIMPLE_ACCOUNT_ID, and DNSIMPLE_TOKEN.", zone, plan.DnsimpleAccountID)}
		}
		return "", err
	}
	return zone, nil
}

func dnsimpleListARecords(token, accountID, zone string) ([]map[string]any, error) {
	encodedZone := url.PathEscape(zone)
	payload, err := dnsimpleRequestFn("GET", dnsimpleURL(accountID, "/zones/"+encodedZone+"/records?type=A"), token, nil)
	if err != nil {
		return nil, err
	}
	rawRecords, ok := payload["data"]
	if !ok {
		rawRecords = payload
	}
	items, ok := rawRecords.([]any)
	if !ok {
		return nil, Error{Msg: "Unexpected DNSimple records response shape."}
	}
	records := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if record, ok := item.(map[string]any); ok {
			records = append(records, record)
		}
	}
	return records, nil
}

func dnsimpleRecordByName(records []map[string]any, name string) (map[string]any, bool) {
	for _, record := range records {
		if fmt.Sprint(record["name"]) == name {
			return record, true
		}
	}
	return nil, false
}

func dnsimpleUpsertHostnameRecords(token, accountID, zone, hostname, ip string) error {
	if err := dnsimpleUpsertARecordRun(token, accountID, zone, relativeRecordName(hostname, zone), ip); err != nil {
		return err
	}
	return dnsimpleUpsertARecordRun(token, accountID, zone, relativeRecordName("*."+hostname, zone), ip)
}

func dnsStateRecord(plan Plan, zone, ip string) DNSState {
	return DNSState{
		Provider:       plan.DnsProvider,
		Zone:           zone,
		HostnameRecord: DNSRecord{Name: relativeRecordName(plan.Hostname, zone), Type: "A", Content: ip},
		WildcardRecord: DNSRecord{Name: relativeRecordName("*."+plan.Hostname, zone), Type: "A", Content: ip},
	}
}

func tlsStateRecord(plan Plan) TLSState {
	hostname := strings.TrimSpace(plan.Hostname)
	return TLSState{
		Provider:    "certbot-dnsimple",
		Domains:     []string{hostname, "*." + hostname},
		Certificate: "/etc/letsencrypt/live/" + hostname + "/fullchain.pem",
		Key:         "/etc/letsencrypt/live/" + hostname + "/privkey.pem",
	}
}

func dnsStateFromRecord(record map[string]any) DNSState {
	if record == nil {
		return DNSState{}
	}
	block, _ := record["dns"].(map[string]any)
	if block == nil {
		return DNSState{}
	}
	state := DNSState{Provider: valueString(block["provider"]), Zone: valueString(block["zone"])}
	if hostnameRecord, ok := block["hostname_record"].(map[string]any); ok {
		state.HostnameRecord = DNSRecord{Name: valueString(hostnameRecord["name"]), Type: valueString(hostnameRecord["type"]), Content: valueString(hostnameRecord["content"])}
	}
	if wildcardRecord, ok := block["wildcard_record"].(map[string]any); ok {
		state.WildcardRecord = DNSRecord{Name: valueString(wildcardRecord["name"]), Type: valueString(wildcardRecord["type"]), Content: valueString(wildcardRecord["content"])}
	}
	return state
}

func tlsStateFromRecord(record map[string]any) TLSState {
	if record == nil {
		return TLSState{}
	}
	block, _ := record["tls"].(map[string]any)
	if block == nil {
		return TLSState{}
	}
	state := TLSState{Provider: valueString(block["provider"]), Certificate: valueString(block["certificate"]), Key: valueString(block["key"])}
	if domains, ok := block["domains"].([]any); ok {
		state.Domains = make([]string, 0, len(domains))
		for _, domain := range domains {
			if text := valueString(domain); text != "" {
				state.Domains = append(state.Domains, text)
			}
		}
	}
	return state
}

func firewallStateFromRecord(record map[string]any) map[string]any {
	if record == nil {
		return nil
	}
	firewall, _ := record["firewall"].(map[string]any)
	return firewall
}

func firewallStateString(record map[string]any, keys ...string) string {
	firewall := firewallStateFromRecord(record)
	if firewall == nil {
		return ""
	}
	for _, key := range keys {
		if value := valueString(firewall[key]); value != "" {
			return value
		}
	}
	return ""
}

func firewallStateDeviceID(record map[string]any) string {
	firewall := firewallStateFromRecord(record)
	if firewall == nil {
		return ""
	}
	if device, ok := firewall["device"].(map[string]any); ok {
		return valueString(device["id"])
	}
	return ""
}

func firewallStateID(record map[string]any) string {
	return firewallStateString(record, "id")
}

func loadManagedFirewallIDByLabel(label string) (string, error) {
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		return "", nil
	}
	payload, err := runLinodeCLIValueFn([]string{"firewalls", "list", "--json"})
	if err != nil {
		return "", err
	}
	var items []any
	switch typed := payload.(type) {
	case []any:
		items = typed
	case map[string]any:
		if list, ok := typed["data"].([]any); ok {
			items = list
		} else if list, ok := typed["firewalls"].([]any); ok {
			items = list
		}
	}
	for _, item := range items {
		firewall, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(valueString(firewall["label"]), trimmed) {
			return apiIDString(firewall["id"])
		}
	}
	return "", nil
}

func firewallPlanMode(plan Plan) string {
	return strings.ToLower(strings.TrimSpace(plan.Firewall.Mode))
}

func ensureManagedFirewall(plan Plan, created CreatedServer, existingRecord map[string]any, statePath, now string) (string, error) {
	if firewallPlanMode(plan) != "managed" {
		return "", nil
	}
	firewallID := strings.TrimSpace(plan.Firewall.ID)
	if firewallID == "" {
		firewallID = firewallStateID(existingRecord)
	}
	deviceID := firewallStateDeviceID(existingRecord)
	if firewallID == "" {
		var err error
		firewallID, err = loadManagedFirewallIDByLabel(firstNonEmpty(plan.Firewall.Label, firewallManagedLabel))
		if err != nil {
			return "", Error{Msg: renderProvisionFirewallPartialFailure(plan, created, statePath, err)}
		}
	}
	if firewallID == "" {
		createArgs := []string{"firewalls", "create", "--label", firstNonEmpty(plan.Firewall.Label, firewallManagedLabel), "--rules.inbound_policy", firstNonEmpty(plan.Firewall.InboundPolicy, firewallInboundPolicy), "--rules.outbound_policy", firstNonEmpty(plan.Firewall.OutboundPolicy, firewallOutboundPolicy)}
		payload, err := runLinodeCLICommand(createArgs)
		if err != nil {
			return "", Error{Msg: renderProvisionFirewallPartialFailure(plan, created, statePath, err)}
		}
		firewallID, err = apiIDString(payload["id"])
		if err != nil {
			return "", Error{Msg: renderProvisionFirewallPartialFailure(plan, created, statePath, fmt.Errorf("Linode firewall response included an invalid id: %v", err))}
		}
		createdPlan := plan
		createdPlan.Firewall.ID = firewallID
		createdPlan.Firewall.DeviceID = ""
		if err := upsertStateRecord(statePath, serverStateRecordWithStatus(createdPlan, created, DNSState{}, TLSState{}, now, now, "provisioning", "linode_created")); err != nil {
			return "", err
		}
	}
	statePlan := plan
	statePlan.Firewall.ID = firewallID
	statePlan.Firewall.DeviceID = deviceID
	if err := upsertStateRecord(statePath, serverStateRecordWithStatus(statePlan, created, DNSState{}, TLSState{}, now, now, "provisioning", "linode_created")); err != nil {
		return "", err
	}
	rulesJSON, err := firewallRulesJSON()
	if err != nil {
		return "", Error{Msg: renderProvisionFirewallPartialFailure(statePlan, created, statePath, err)}
	}
	if _, err := runLinodeCLICommand([]string{"firewalls", "rules-update", firewallID, "--inbound", rulesJSON, "--inbound_policy", firstNonEmpty(plan.Firewall.InboundPolicy, firewallInboundPolicy)}); err != nil {
		return "", Error{Msg: renderProvisionFirewallPartialFailure(statePlan, created, statePath, err)}
	}
	if deviceID != created.ProviderID {
		if _, err := runLinodeCLICommand([]string{"firewalls", "device-create", "--type", "linode", "--id", created.ProviderID, firewallID}); err != nil {
			return "", Error{Msg: renderProvisionFirewallPartialFailure(statePlan, created, statePath, err)}
		}
		deviceID = created.ProviderID
	}
	if deviceID == created.ProviderID {
		createdPlan := plan
		createdPlan.Firewall.ID = firewallID
		createdPlan.Firewall.DeviceID = deviceID
		if err := upsertStateRecord(statePath, serverStateRecordWithStatus(createdPlan, created, DNSState{}, TLSState{}, now, now, "provisioning", "linode_created")); err != nil {
			return "", err
		}
	}
	return firewallID, nil
}

func renderProvisionFirewallPartialFailure(plan Plan, created CreatedServer, statePath string, setupErr error) string {
	plan = normalizePlan(plan)
	lines := []string{"Server provisioning paused.", ""}
	lines = append(lines, serverPlanBlock(plan, &created, "provisioning", "linode_created")...)
	lines = append(lines,
		"Paths",
		"  state: "+statePath,
		"Firewall error",
		"  "+strings.TrimSpace(setupErr.Error()),
		"Next",
		"  rerun the same provision command to resume firewall, DNS, and TLS setup.",
	)
	return strings.Join(lines, "\n") + "\n"
}

func sshPlanBlock(plan Plan) []string {
	return []string{
		"Access",
		"  ssh user: " + plan.SshUser,
		"  auth: SSH keys only",
		"  sudo: passwordless",
		"  key source: " + firstNonEmpty(plan.SshKeySource, "linode-profile"),
		"  authorized keys: " + sshKeySummary(plan),
	}
}

func rootPlanBlock(plan Plan) []string {
	return []string{
		"  root password: derived from hostname + purpose linode-root",
		"  root stored in state: no",
		"  root reveal: nf server root-password " + plan.Name,
	}
}

func ubuntuFirewallPlanBlock() []string {
	return []string{
		"Ubuntu firewall",
		"  ufw: enabled",
		"  ufw default: deny incoming",
		"  ufw outbound: allow",
		"  allow: 22/tcp, 80/tcp, 443/tcp",
	}
}

func phpBaselinePlanBlock(plan Plan) []string {
	return []string{
		"PHP baseline",
		"  timezone: UTC",
		"  swap: 2G",
		"  stack: " + stackSummary(plan),
		"  ubuntu: " + ubuntuDisplayLabel(plan),
		"  image: " + plan.OS.Image,
		"  php version: " + plan.PHP.Version,
		"  php service: " + plan.PHP.Service,
		"  php socket: " + plan.PHP.Socket,
		"  package source: " + plan.OS.PackageSource,
		"  base packages: " + joinedPackages(plan.OS.Packages),
		"  packages: " + joinedPackages(plan.PHP.Packages),
	}
}

func healthPlanBlock(plan Plan) []string {
	return []string{
		"Server health URL: " + plan.HealthURL,
	}
}

func pathsPlanBlock(cloudInitPath string) []string {
	lines := []string{"Paths"}
	if cloudInitPath != "" {
		lines = append(lines, "  cloud-init preview: "+cloudInitPath)
	}
	lines = append(lines,
		"  marker: /etc/nf/server.json",
		"  motd: /etc/update-motd.d/99-nf",
		"  sites root: /var/www/sites",
		"  shared root: /var/www/shared",
		"  nginx site logs: /var/log/nginx/sites",
	)
	return lines
}

func sshSuccessBlock(plan Plan, keys []SSHAuthorizedKey) []string {
	lines := []string{
		"Access",
		"  ssh user: " + plan.SshUser,
		"  auth: SSH keys only",
		"  sudo: passwordless",
		"  key source: " + firstNonEmpty(plan.SshKeySource, "linode-profile"),
	}
	if labels := sshKeyLabels(keys); labels != "" {
		lines = append(lines, "  authorized keys: "+labels)
	}
	lines = append(lines,
		"  root password: derived from hostname + purpose linode-root",
		"  root stored in state: no",
		"  root reveal: nf server root-password "+plan.Name,
	)
	return lines
}

func rootSuccessBlock(plan Plan) []string { return nil }

func sshStateBlock(plan Plan, keys []SSHAuthorizedKey) map[string]any {
	return map[string]any{
		"user":            plan.SshUser,
		"host":            plan.Hostname,
		"port":            22,
		"auth":            "ssh_keys_only",
		"sudo":            "passwordless",
		"source":          firstNonEmpty(plan.SshKeySource, "linode-profile"),
		"authorized_keys": sshAuthorizedKeyMetadata(keys),
	}
}

func credentialsStateBlock(plan Plan) map[string]any {
	return map[string]any{"root": rootCredentialState(plan.Hostname)}
}

func firewallPlanBlock(plan Plan) []string {
	if plan.Firewall.Mode == "" {
		return nil
	}
	lines := []string{
		"Linode firewall",
		"  provider: " + plan.Provider,
		"  mode: " + plan.Firewall.Mode,
	}
	if plan.Firewall.Mode == "managed" {
		lines = append(lines,
			"  managed label: "+firstNonEmpty(plan.Firewall.Label, firewallManagedLabel),
			"  inbound: 22/tcp, 80/tcp, 443/tcp",
			"  inbound policy: "+firstNonEmpty(plan.Firewall.InboundPolicy, firewallInboundPolicy),
			"  outbound policy: "+firstNonEmpty(plan.Firewall.OutboundPolicy, firewallOutboundPolicy),
		)
		if plan.Firewall.ID != "" {
			lines = append(lines, "  firewall id: "+plan.Firewall.ID)
		}
	}
	return lines
}

func firewallStateBlock(plan Plan, firewallID, deviceID string) map[string]any {
	if plan.Firewall.Mode == "" {
		return nil
	}
	block := map[string]any{
		"mode": plan.Firewall.Mode,
	}
	if plan.Firewall.Mode == "managed" {
		block["label"] = firstNonEmpty(plan.Firewall.Label, firewallManagedLabel)
		block["inbound_policy"] = firstNonEmpty(plan.Firewall.InboundPolicy, firewallInboundPolicy)
		block["outbound_policy"] = firstNonEmpty(plan.Firewall.OutboundPolicy, firewallOutboundPolicy)
		block["rules"] = firewallRulesPayload()
		if firewallID != "" {
			block["id"] = firewallID
		}
		if deviceID != "" {
			block["device"] = map[string]any{"type": "linode", "id": deviceID}
		}
	}
	return block
}

func sshKeyPlanValue(plan Plan) string {
	if source := firstNonEmpty(plan.SshKeySource, "linode-profile"); source == "file" {
		return plan.SshPublicKeyFile
	}
	return sshKeySummary(plan)
}

func sshKeySourceIsFile(plan Plan) bool {
	return firstNonEmpty(plan.SshKeySource, "linode-profile") == "file"
}

func filterLinodeSSHKeys(keys []SSHAuthorizedKey, plan Plan) []SSHAuthorizedKey {
	if len(keys) == 0 {
		return nil
	}
	if plan.AllLinodeSshKeys {
		return append([]SSHAuthorizedKey{}, keys...)
	}
	filtered := make([]SSHAuthorizedKey, 0, len(keys))
	for _, key := range keys {
		if plan.SshKeyLabel != "" && key.Label != plan.SshKeyLabel {
			continue
		}
		if plan.SshKeyID != "" && key.ID != plan.SshKeyID {
			continue
		}
		filtered = append(filtered, key)
	}
	if len(filtered) > 0 {
		return filtered
	}
	return nil
}

func parseLinodeSSHKeysPayload(payload any) ([]SSHAuthorizedKey, error) {
	var items []any
	if list, ok := payload.([]any); ok {
		items = list
	} else if raw, ok := payload.(map[string]any); ok {
		if list, ok := raw["data"].([]any); ok {
			items = list
		} else if list, ok := raw["sshkeys"].([]any); ok {
			items = list
		} else if list, ok := raw["keys"].([]any); ok {
			items = list
		} else {
			for _, value := range raw {
				if list, ok := value.([]any); ok {
					items = list
					break
				}
			}
		}
	} else {
		return nil, Error{Msg: fmt.Sprintf("Unexpected Linode SSH key response shape: %T", payload)}
	}
	keys := make([]SSHAuthorizedKey, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, err := apiIDString(m["id"])
		if err != nil {
			return nil, err
		}
		key := SSHAuthorizedKey{Source: "linode-profile", ID: id, Label: valueString(m["label"]), Fingerprint: valueString(m["fingerprint"]), Created: valueString(m["created"]), PublicKey: valueString(m["ssh_key"])}
		keys = append(keys, key)
	}
	return keys, nil
}

func loadLinodeProfileSSHKeys(plan Plan) ([]SSHAuthorizedKey, error) {
	args := []string{"sshkeys", "list", "--json"}
	if !plan.AllLinodeSshKeys {
		if plan.SshKeyLabel != "" {
			args = append(args, "--label", plan.SshKeyLabel)
		}
		if plan.SshKeyID != "" {
			args = append(args, "--id", plan.SshKeyID)
		}
	}
	payload, err := runLinodeCLIValueFn(args)
	if err != nil {
		return nil, err
	}
	keys, err := parseLinodeSSHKeysPayload(payload)
	if err != nil {
		return nil, err
	}
	return keys, nil
}

func selectLinodeSSHKeys(plan Plan, keys []SSHAuthorizedKey) ([]SSHAuthorizedKey, error) {
	if len(keys) == 0 {
		return nil, Error{Msg: "No Linode profile SSH keys were found. Use --ssh-key-source file --ssh-public-key-file <path> or add keys to the Linode profile."}
	}
	if plan.NonInteractive || plan.AllLinodeSshKeys || plan.SshKeyLabel != "" || plan.SshKeyID != "" {
		return keys, nil
	}
	options := make([]ui.SelectOption, 0, len(keys))
	for _, key := range keys {
		label := key.Label
		if label == "" {
			label = key.ID
		}
		if key.Fingerprint != "" {
			label += " / " + key.Fingerprint
		}
		options = append(options, ui.SelectOption{Label: label, Value: key.ID, Default: true})
	}
	selectedIDs, err := multiSelectFn("Choose Linode SSH keys", options)
	if err != nil {
		return nil, err
	}
	selected := make([]SSHAuthorizedKey, 0, len(selectedIDs))
	selectedSet := map[string]struct{}{}
	for _, id := range selectedIDs {
		selectedSet[id] = struct{}{}
	}
	for _, key := range keys {
		if _, ok := selectedSet[key.ID]; ok {
			selected = append(selected, key)
		}
	}
	return selected, nil
}

func resolveAuthorizedKeys(plan Plan, actual bool) ([]SSHAuthorizedKey, error) {
	source := firstNonEmpty(plan.SshKeySource, "linode-profile")
	switch source {
	case "file":
		data, err := os.ReadFile(plan.SshPublicKeyFile)
		if err != nil {
			return nil, err
		}
		return []SSHAuthorizedKey{{Source: "file", Path: plan.SshPublicKeyFile, PublicKey: strings.TrimSpace(string(data)), Label: filepath.Base(plan.SshPublicKeyFile)}}, nil
	default:
		if !actual {
			return nil, nil
		}
		keys, err := loadLinodeProfileSSHKeys(plan)
		if err != nil {
			return nil, err
		}
		return selectLinodeSSHKeys(plan, keys)
	}
}

func sshBodiesFromKeys(keys []SSHAuthorizedKey) []string {
	return sshKeyBodies(keys)
}

func findProvisionStateRecord(plan Plan) (map[string]any, int, error) {
	records, err := loadStatePayload(filepath.Join(config.StateDir(), "servers.json"))
	if err != nil {
		return nil, -1, err
	}
	for i, record := range records {
		if matchesProvisionStateRecord(record, plan.Provider, "", plan.Hostname, plan.Name, plan.Label) {
			return record, i, nil
		}
	}
	return nil, -1, nil
}

func existingProvisionStatus(record map[string]any) string {
	return strings.ToLower(valueString(record["status"]))
}

func existingProvisionPhase(record map[string]any) string {
	return strings.ToLower(valueString(record["phase"]))
}

func provisioningPhaseRank(phase string) int {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "complete":
		return 5
	case "tls_configured":
		return 4
	case "cloud_init_ready":
		return 3
	case "dns_configured":
		return 2
	case "firewall_configured":
		return 1
	case "linode_created", "":
		return 0
	default:
		return 0
	}
}

func createdServerFromRecord(plan Plan, record map[string]any) (CreatedServer, error) {
	providerID, err := apiIDString(record["provider_id"])
	if err != nil {
		providerID, err = apiIDString(record["id"])
		if err != nil {
			return CreatedServer{}, Error{Msg: fmt.Sprintf("Existing provisioning record for %q is missing a usable provider id.", plan.Hostname)}
		}
	}
	ipv4 := valueString(record["ipv4"])
	if ipv4 == "" {
		return CreatedServer{}, Error{Msg: fmt.Sprintf("Existing provisioning record for %q is missing an IPv4 address and cannot resume.", plan.Hostname)}
	}
	return CreatedServer{Provider: plan.Provider, ProviderID: providerID, Name: plan.Name, Hostname: plan.Hostname, IPv4: ipv4}, nil
}

func existingCreatedAt(record map[string]any, fallback string) string {
	if createdAt := strings.TrimSpace(valueString(record["created_at"])); createdAt != "" {
		return createdAt
	}
	return fallback
}

func serverStateRecord(plan Plan, created CreatedServer, dns DNSState, tls TLSState, createdAt string) map[string]any {
	return serverStateRecordWithStatus(plan, created, dns, tls, createdAt, createdAt, "provisioned", "complete")
}

func serverStateRecordWithStatus(plan Plan, created CreatedServer, dns DNSState, tls TLSState, createdAt, updatedAt, status, phase string) map[string]any {
	sshState := sshStateBlock(plan, plan.AuthorizedKeys)
	record := map[string]any{
		"provider":          plan.Provider,
		"provider_id":       created.ProviderID,
		"name":              plan.Name,
		"hostname":          plan.Hostname,
		"label":             plan.Label,
		"domain":            plan.Domain,
		"wildcard_hostname": plan.WildcardHostname,
		"health_url":        plan.HealthURL,
		"status":            status,
		"phase":             phase,
		"ipv4":              created.IPv4,
		"region":            plan.Region,
		"type":              plan.LinodeType,
		"image":             plan.Image,
		"ssh":               sshState,
		"created_at":        createdAt,
		"updated_at":        updatedAt,
	}
	if firewall := firewallStateBlock(plan, plan.Firewall.ID, plan.Firewall.DeviceID); firewall != nil {
		record["firewall"] = firewall
	}
	record["credentials"] = credentialsStateBlock(plan)
	record["linode"] = map[string]any{
		"id":          created.ProviderID,
		"instance_id": created.ProviderID,
		"label":       plan.Label,
		"region":      plan.Region,
		"type":        plan.LinodeType,
		"image":       plan.Image,
		"ssh":         sshState,
	}
	if dns.Provider != "" {
		record["dns"] = map[string]any{
			"provider":        dns.Provider,
			"zone":            dns.Zone,
			"hostname_name":   dns.HostnameRecord.Name,
			"wildcard_name":   dns.WildcardRecord.Name,
			"hostname_record": dns.HostnameRecord,
			"wildcard_record": dns.WildcardRecord,
		}
	}
	if tls.Provider != "" {
		record["tls"] = map[string]any{
			"provider":    tls.Provider,
			"domains":     tls.Domains,
			"certificate": tls.Certificate,
			"key":         tls.Key,
		}
	}
	record["os"] = map[string]any{
		"family":         "ubuntu",
		"ubuntu_version": plan.OS.UbuntuVersion,
		"version":        plan.OS.UbuntuVersion,
		"label":          strings.TrimPrefix(plan.OS.Label, "Ubuntu "),
		"image":          plan.OS.Image,
		"package_source": plan.OS.PackageSource,
		"packages":       plan.OS.Packages,
	}
	record["php"] = map[string]any{
		"version":        plan.PHP.Version,
		"package_source": plan.PHP.PackageSource,
		"service":        plan.PHP.Service,
		"socket":         plan.PHP.Socket,
		"packages":       plan.PHP.Packages,
	}
	record["services"] = map[string]any{
		"nginx":   true,
		"mariadb": true,
		"php_fpm": plan.PHP.Service,
		"wp_cli":  "/usr/local/bin/wp",
	}
	if created.ProviderID != "" {
		record["id"] = created.ProviderID
		record["linode_id"] = created.ProviderID
	}
	return record
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
		recordID, err := apiIDString(existing["id"])
		if err != nil {
			return Error{Msg: fmt.Sprintf("DNSimple record is missing a valid id: %v", err)}
		}
		currentIP := fmt.Sprint(existing["content"])
		if currentIP == ip {
			return nil
		}
		_, err = dnsimpleRequestFn("PATCH", dnsimpleURL(accountID, "/zones/"+encodedZone+"/records/"+recordID), token, map[string]any{"content": ip, "ttl": 60})
		return err
	}
	_, err = dnsimpleRequestFn("POST", dnsimpleURL(accountID, "/zones/"+encodedZone+"/records"), token, map[string]any{"name": name, "type": "A", "content": ip, "ttl": 60})
	return err
}

func planLines(plan Plan, cloudInitPath string) []string {
	plan = normalizePlan(plan)
	lines := serverPlanBlock(plan, nil, "", "")
	lines = append(lines, configPlanBlock(plan)...)
	lines = append(lines, availabilityPlanBlock(plan)...)
	lines = append(lines, sshPlanBlock(plan)...)
	lines = append(lines, rootPlanBlock(plan)...)
	lines = append(lines, ubuntuFirewallPlanBlock()...)
	lines = append(lines, firewallPlanBlock(plan)...)
	lines = append(lines, phpBaselinePlanBlock(plan)...)
	lines = append(lines,
		"DNS",
		"  provider: "+plan.DnsProvider,
		"  zone: "+plan.Domain,
		"  hostname A: "+relativeRecordName(plan.Hostname, plan.Domain)+" -> <created after server IP is known>",
		"  wildcard A: "+relativeRecordName(plan.WildcardHostname, plan.Domain)+" -> <created after server IP is known>",
		"TLS",
		"  provider: certbot-dnsimple",
		"  domains: "+plan.Hostname+", "+plan.WildcardHostname,
		"  certificate: /etc/letsencrypt/live/"+plan.Hostname+"/fullchain.pem",
		"  key: /etc/letsencrypt/live/"+plan.Hostname+"/privkey.pem",
	)
	lines = append(lines, pathsPlanBlock(cloudInitPath)...)
	lines = append(lines, "Mode", fmt.Sprintf("  dry-run: %t", plan.DryRun || !plan.Execute))
	return lines
}

func renderPlan(plan Plan, cloudInitPath, cloudInitPreview string) string {
	plan = normalizePlan(plan)
	header := "Server provision dry-run plan"
	if plan.Execute {
		header = "Server provision plan"
	}
	if plan.Execute && plan.NonInteractive && !plan.Yes {
		header = "Server provision blocked (missing --yes)"
	}
	lines := []string{header, ""}
	for _, line := range planLines(plan, cloudInitPath) {
		lines = append(lines, line)
	}
	if cloudInitPreview != "" {
		lines = append(lines, "", "cloud-init preview:", strings.TrimRight(cloudInitPreview, "\n"))
	}
	return strings.Join(lines, "\n") + "\n"
}

func serverPlanBlock(plan Plan, created *CreatedServer, status, phase string) []string {
	lines := []string{
		"Server",
		"  provider: " + plan.Provider,
		"  name: " + plan.Name,
		"  hostname: " + plan.Hostname,
		"  label: " + plan.Label,
		"  wildcard hostname: " + plan.WildcardHostname,
		"  health url: " + plan.HealthURL,
		"  region: " + plan.Region,
		"  type: " + plan.LinodeType,
	}
	if created != nil {
		lines = append(lines,
			"  ipv4: "+created.IPv4,
			"  linode instance id: "+created.ProviderID,
		)
	}
	if status != "" {
		lines = append(lines,
			"  health check: "+plan.HealthURL+"/healthz",
			"  status: "+status,
		)
	}
	if phase != "" {
		lines = append(lines, "  phase: "+phase)
	}
	return lines
}

func configPlanBlock(plan Plan) []string {
	return []string{
		"Config",
		"  server domain: " + plan.Domain,
		"  dns provider: " + plan.DnsProvider,
		"  dnsimple account id: " + plan.DnsimpleAccountID,
	}
}

func availabilityPlanBlock(plan Plan) []string {
	mode := "checked before create"
	if plan.DryRun {
		mode = "not checked (dry-run)"
	}
	return []string{
		"Availability",
		"  local state: " + mode,
		"  linode label: " + mode,
		"  dns records: " + mode,
	}
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
	v, err := promptStringFn(prompt, defaultValue, allowBlank)
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

func resolveServerName(explicit string, nonInteractive bool) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if nonInteractive {
		return "app1", nil
	}
	v, err := promptStringFn("Server name: ", "app1", false)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(v) == "" {
		return "app1", nil
	}
	return v, nil
}

func normalizePlan(plan Plan) Plan {
	if strings.TrimSpace(plan.Domain) == "" {
		plan.Domain = firstNonEmpty(plan.DnsZone, serverDomain())
	}
	if strings.TrimSpace(plan.DnsZone) == "" {
		plan.DnsZone = plan.Domain
	}
	if strings.TrimSpace(plan.Hostname) == "" && strings.TrimSpace(plan.Name) != "" && strings.TrimSpace(plan.Domain) != "" {
		plan.Hostname = deriveHostname(plan.Name, plan.Domain)
	}
	if strings.TrimSpace(plan.Label) == "" {
		plan.Label = plan.Name
	}
	if strings.TrimSpace(plan.WildcardHostname) == "" {
		plan.WildcardHostname = deriveWildcardHostname(plan.Hostname)
	}
	if strings.TrimSpace(plan.HealthURL) == "" {
		plan.HealthURL = deriveHealthURL(plan.Hostname)
	}
	if strings.TrimSpace(plan.DnsimpleAccountID) == "" {
		plan.DnsimpleAccountID = firstNonEmpty(envwizard.Value("DNSIMPLE_ACCOUNT_ID"), "14")
	}
	if !plan.DryRun && !plan.Execute {
		plan.DryRun = true
	}
	return plan
}

func BuildPlan(args Args) (Plan, error) {
	nonInteractive := args.NonInteractive
	provider := firstNonEmpty(args.Provider, "linode")
	dnsProvider := firstNonEmpty(args.DnsProvider, "dnsimple")
	ubuntuRelease, err := selectUbuntuStack(args.UbuntuVersion, nonInteractive)
	if err != nil {
		return Plan{}, err
	}
	firewallMode := strings.ToLower(firstNonEmpty(args.Firewall, "managed"))
	switch firewallMode {
	case "managed", "none":
	default:
		return Plan{}, Error{Msg: fmt.Sprintf("Unsupported firewall mode %q. Use managed or none.", firewallMode)}
	}
	if firewallMode == "none" && strings.TrimSpace(args.FirewallID) != "" {
		return Plan{}, Error{Msg: "--firewall-id requires --firewall managed."}
	}
	name, err := resolveServerName(args.Name, nonInteractive)
	if err != nil {
		return Plan{}, err
	}
	if err := validateServerName(name); err != nil {
		return Plan{}, err
	}
	domain := serverDomain()
	hostname := deriveHostname(name, domain)
	wildcardHostname := deriveWildcardHostname(hostname)
	healthURL := deriveHealthURL(hostname)
	if hostname == "" || wildcardHostname == "" || healthURL == "" {
		return Plan{}, Error{Msg: "Server name and domain are required to derive the hostname, wildcard hostname, and health URL."}
	}
	label := name
	if strings.TrimSpace(label) == "" {
		return Plan{}, Error{Msg: "Server name cannot be empty."}
	}
	region, err := resolveValue(args.Region, "Linode region: ", "ca-central", nonInteractive, false)
	if err != nil {
		return Plan{}, err
	}
	linodeType, err := resolveValue(args.Type, "Linode type: ", "g6-standard-1", nonInteractive, false)
	if err != nil {
		return Plan{}, err
	}
	sshUser, err := resolveValue(args.SshUser, "Deployment SSH user: ", "nonfiction", nonInteractive, false)
	if err != nil {
		return Plan{}, err
	}
	sshKeySource := firstNonEmpty(args.SshKeySource, "linode-profile")
	explicitSSHPublicKeyFile := strings.TrimSpace(args.SshPublicKeyFile)
	sshPublicKeyFile := ""
	if explicitSSHPublicKeyFile != "" || sshKeySource == "file" {
		if explicitSSHPublicKeyFile != "" && strings.TrimSpace(args.SshKeySource) == "" {
			sshKeySource = "file"
		}
		sshPublicKeyFile, err = resolveValue(explicitSSHPublicKeyFile, "SSH public key file: ", "~/.ssh/id_ed25519.pub", nonInteractive, false)
		if err != nil {
			return Plan{}, err
		}
		sshPublicKeyFile = cleanPath(sshPublicKeyFile)
	}
	dnsimpleAccountID := firstNonEmpty(args.DnsimpleAccountID, envwizard.Value("DNSIMPLE_ACCOUNT_ID"), "14")
	writeCloudInit := strings.TrimSpace(args.WriteCloudInit)
	if provider != "linode" {
		return Plan{}, Error{Msg: fmt.Sprintf("Unsupported provider %q. Only linode is available in this slice.", provider)}
	}
	if dnsProvider != "dnsimple" {
		return Plan{}, Error{Msg: fmt.Sprintf("Unsupported DNS provider %q. Only dnsimple is available in this slice.", dnsProvider)}
	}
	firewallPlan := FirewallPlan{Mode: "none"}
	if firewallMode == "managed" {
		firewallPlan = managedFirewallPlan(args.FirewallID)
	}
	osPlan, err := osReleasePlan(ubuntuRelease.version, args.Image)
	if err != nil {
		return Plan{}, err
	}
	phpPlan, err := phpReleaseForUbuntu(ubuntuRelease.version)
	if err != nil {
		return Plan{}, err
	}
	return normalizePlan(Plan{
		Provider:          provider,
		DnsProvider:       dnsProvider,
		DnsZone:           domain,
		Domain:            domain,
		UbuntuVersion:     ubuntuRelease.version,
		PHPVersion:        ubuntuRelease.php,
		Firewall:          firewallPlan,
		Name:              firstNonEmpty(name, "app1"),
		Hostname:          hostname,
		Label:             label,
		WildcardHostname:  wildcardHostname,
		HealthURL:         healthURL,
		Region:            firstNonEmpty(region, "ca-central"),
		LinodeType:        firstNonEmpty(linodeType, "g6-standard-1"),
		Image:             osPlan.Image,
		SshUser:           firstNonEmpty(sshUser, "nonfiction"),
		SshKeySource:      sshKeySource,
		SshKeyLabel:       strings.TrimSpace(args.SshKeyLabel),
		SshKeyID:          strings.TrimSpace(args.SshKeyID),
		AllLinodeSshKeys:  args.AllLinodeSshKeys,
		SshPublicKeyFile:  sshPublicKeyFile,
		DnsimpleAccountID: firstNonEmpty(dnsimpleAccountID, "14"),
		WriteCloudInit:    cleanPath(writeCloudInit),
		OS:                osPlan,
		PHP:               phpPlan,
		Execute:           args.Execute,
		Yes:               args.Yes,
		Wait:              !args.NoWait,
		SshTimeout:        firstDuration(args.SshTimeout, 5*time.Minute),
		CloudInitTimeout:  firstDuration(args.CloudInitTimeout, 10*time.Minute),
		TLSTimeout:        firstDuration(args.TLSTimeout, 5*time.Minute),
		HealthTimeout:     firstDuration(args.HealthTimeout, 2*time.Minute),
		DryRun:            args.DryRun || !args.Execute,
		NonInteractive:    nonInteractive,
		ShowCloudInit:     args.ShowCloudInit,
	}), nil
}

func firstDuration(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func dnsRecordLine(label string, record DNSRecord) string {
	return fmt.Sprintf("  %s: %s -> %s", label, record.Name, record.Content)
}

func healthSuccessBlock(plan Plan) []string {
	return []string{
		"Server",
		"  health: " + plan.HealthURL,
		"  health check: " + plan.HealthURL + "/healthz",
		"  status: ready",
	}
}

func renderProvisionSuccess(plan Plan, created CreatedServer, dns DNSState, tls TLSState, statePath, cloudInitPath string, sshKeys []SSHAuthorizedKey) string {
	plan = normalizePlan(plan)
	lines := []string{"Server provisioned.", ""}
	lines = append(lines, serverPlanBlock(plan, &created, "ready", "complete")...)
	lines = append(lines, sshSuccessBlock(plan, sshKeys)...)
	lines = append(lines, rootSuccessBlock(plan)...)
	lines = append(lines, ubuntuFirewallPlanBlock()...)
	lines = append(lines, firewallPlanBlock(plan)...)
	lines = append(lines, phpBaselinePlanBlock(plan)...)
	lines = append(lines,
		"DNS",
		"  provider: "+dns.Provider,
		"  zone: "+dns.Zone,
		dnsRecordLine("hostname A", dns.HostnameRecord),
		dnsRecordLine("wildcard A", dns.WildcardRecord),
		"TLS",
		"  provider: "+tls.Provider,
		"  domains: "+strings.Join(tls.Domains, ", "),
		"  certificate: "+tls.Certificate,
		"  key: "+tls.Key,
		"Paths",
		"  state: "+statePath,
	)
	if cloudInitPath != "" {
		lines = append(lines, "  cloud-init: "+cloudInitPath)
	}
	lines = append(lines,
		"  marker: /etc/nf/server.json",
		"  motd: /etc/update-motd.d/99-nf",
		"  sites root: /var/www/sites",
		"  shared root: /var/www/shared",
		"  nginx site logs: /var/log/nginx/sites",
	)
	return strings.Join(lines, "\n") + "\n"
}

func renderProvisionPaused(plan Plan, created CreatedServer, dns DNSState, statePath string, phase string, sshKeys []SSHAuthorizedKey) string {
	plan = normalizePlan(plan)
	sshTarget := plan.SshUser + "@" + plan.Hostname
	lines := []string{"Server provisioning paused.", ""}
	lines = append(lines, serverPlanBlock(plan, &created, "provisioning", phase)...)
	lines = append(lines, sshSuccessBlock(plan, sshKeys)...)
	lines = append(lines, rootSuccessBlock(plan)...)
	lines = append(lines, ubuntuFirewallPlanBlock()...)
	lines = append(lines, firewallPlanBlock(plan)...)
	lines = append(lines, phpBaselinePlanBlock(plan)...)
	lines = append(lines,
		"DNS",
		"  provider: "+dns.Provider,
		"  zone: "+dns.Zone,
		dnsRecordLine("hostname A", dns.HostnameRecord),
		dnsRecordLine("wildcard A", dns.WildcardRecord),
		"Paths",
		"  state: "+statePath,
		"  marker: /etc/nf/server.json",
		"  motd: /etc/update-motd.d/99-nf",
		"  sites root: /var/www/sites",
		"  shared root: /var/www/shared",
		"  nginx site logs: /var/log/nginx/sites",
		"Next",
		"  wait for SSH:",
		"  ssh -o BatchMode=yes "+sshTarget+" \"cloud-init status --wait\"",
		"  enable wildcard TLS:",
		"  ssh "+sshTarget+" \"sudo /usr/local/bin/nf-enable-wildcard-tls\"",
		"  check HTTPS health:",
		"  curl -fsS "+plan.HealthURL+"/healthz",
	)
	return strings.Join(lines, "\n") + "\n"
}

func renderProvisionHealthFailure(plan Plan, created CreatedServer, dns DNSState, tls TLSState, statePath string, sshKeys []SSHAuthorizedKey, err error) string {
	plan = normalizePlan(plan)
	sshTarget := plan.SshUser + "@" + plan.Hostname
	lines := []string{"Server provisioning paused.", ""}
	lines = append(lines, serverPlanBlock(plan, &created, "tls_configured", "tls_configured")...)
	lines = append(lines, sshSuccessBlock(plan, sshKeys)...)
	lines = append(lines, rootSuccessBlock(plan)...)
	lines = append(lines, ubuntuFirewallPlanBlock()...)
	lines = append(lines, firewallPlanBlock(plan)...)
	lines = append(lines, phpBaselinePlanBlock(plan)...)
	lines = append(lines,
		"DNS",
		"  provider: "+dns.Provider,
		"  zone: "+dns.Zone,
		dnsRecordLine("hostname A", dns.HostnameRecord),
		dnsRecordLine("wildcard A", dns.WildcardRecord),
		"TLS",
		"  provider: "+tls.Provider,
		"  domains: "+strings.Join(tls.Domains, ", "),
		"  certificate: "+tls.Certificate,
		"  key: "+tls.Key,
		"Paths",
		"  state: "+statePath,
		"  marker: /etc/nf/server.json",
		"  motd: /etc/update-motd.d/99-nf",
		"  sites root: /var/www/sites",
		"  shared root: /var/www/shared",
		"  nginx site logs: /var/log/nginx/sites",
		"Health error",
		"  "+strings.TrimSpace(err.Error()),
		"Recovery",
		"  ssh "+sshTarget+" \"sudo nginx -t && sudo systemctl status nginx\"",
		"  curl -I "+plan.HealthURL,
	)
	return strings.Join(lines, "\n") + "\n"
}

func renderProvisionPartialFailure(plan Plan, created CreatedServer, statePath string, dnsErr error) string {
	plan = normalizePlan(plan)
	lines := []string{"Server provisioning paused.", ""}
	lines = append(lines, serverPlanBlock(plan, &created, "provisioning", "linode_created")...)
	lines = append(lines,
		"Paths",
		"  state: "+statePath,
		"DNS error",
		"  "+strings.TrimSpace(dnsErr.Error()),
		"Next",
		"  rerun the same provision command to resume DNS and TLS setup.",
	)
	return strings.Join(lines, "\n") + "\n"
}

func preparePlan(plan Plan) (Plan, string, error) {
	plan = normalizePlan(plan)
	preview, err := renderCloudInit(plan, false, "", nil)
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
	if len(plan.AuthorizedKeys) == 0 {
		sshKeys, err := resolveAuthorizedKeys(plan, true)
		if err != nil {
			return Plan{}, plan.WriteCloudInit, err
		}
		plan.AuthorizedKeys = sshKeys
	}
	if plan.Execute && plan.Yes {
		return plan, plan.WriteCloudInit, nil
	}
	answer, err := confirmFn("This will create a Linode server and DNS records. Continue?", false)
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

func ProvisionServer(plan Plan) (*ServerCreateResult, error) {
	plan = normalizePlan(plan)
	effectivePlan, previewPath, err := preparePlan(plan)
	if err != nil {
		return nil, err
	}
	_ = previewPath
	if !effectivePlan.Execute {
		return nil, nil
	}
	serverStatePath := filepath.Join(config.StateDir(), "servers.json")
	existingRecord, _, err := findProvisionStateRecord(effectivePlan)
	if err != nil {
		return nil, err
	}
	if existingRecord != nil {
		status := existingProvisionStatus(existingRecord)
		phase := existingProvisionPhase(existingRecord)
		if status == "provisioned" || phase == "complete" {
			return nil, Error{Msg: fmt.Sprintf("Server %q is already provisioned. No changes made.\nState: %s", effectivePlan.Hostname, serverStatePath)}
		}
	}
	if err := envwizard.Ensure(provisionRequirements(), effectivePlan.NonInteractive); err != nil {
		return nil, err
	}
	if err := validateActualExecution(effectivePlan); err != nil {
		return nil, err
	}
	sshKeys := effectivePlan.AuthorizedKeys
	if len(sshKeys) == 0 {
		var err error
		sshKeys, err = resolveAuthorizedKeys(effectivePlan, true)
		if err != nil {
			return nil, err
		}
		effectivePlan.AuthorizedKeys = sshKeys
	}
	salt, err := secretSaltFn()
	if err != nil {
		return nil, err
	}
	rootPass := passwords.DerivePassword(effectivePlan.Hostname, "linode-root", salt)
	dnsimpleToken, err := requiredEnv("DNSIMPLE_TOKEN")
	if err != nil {
		return nil, err
	}
	rendered, err := renderCloudInit(effectivePlan, true, dnsimpleToken, sshKeyBodies(sshKeys))
	if err != nil {
		return nil, err
	}
	now := currentTime().UTC().Format(time.RFC3339)
	createdAt := now
	var created CreatedServer
	var linodeID string
	var linodeIP string
	currentPhase := ""
	dnsState := DNSState{}
	tlsState := TLSState{}
	if existingRecord != nil {
		created, err = createdServerFromRecord(effectivePlan, existingRecord)
		if err != nil {
			return nil, err
		}
		fmt.Println("Reusing Linode")
		linodeID = created.ProviderID
		linodeIP = created.IPv4
		createdAt = existingCreatedAt(existingRecord, now)
		currentPhase = existingProvisionPhase(existingRecord)
		dnsState = dnsStateFromRecord(existingRecord)
		tlsState = tlsStateFromRecord(existingRecord)
		effectivePlan.Firewall.ID = firewallStateID(existingRecord)
		effectivePlan.Firewall.DeviceID = firewallStateDeviceID(existingRecord)
	} else {
		linodeMatches, err := linodeListByLabel(effectivePlan.Label)
		if err != nil {
			return nil, err
		}
		if len(linodeMatches) > 0 {
			label := valueString(linodeMatches[0]["label"])
			linodeID := valueString(linodeMatches[0]["id"])
			if linodeID == "" {
				linodeID = valueString(linodeMatches[0]["instance_id"])
			}
			return nil, Error{Msg: fmt.Sprintf("Server name %q is unavailable. Linode label %q already exists (id %s).", effectivePlan.Name, label, linodeID)}
		}
		dnsZone, err := dnsimpleZoneLookup(effectivePlan, dnsimpleToken)
		if err != nil {
			return nil, err
		}
		records, err := dnsimpleListARecordsFn(dnsimpleToken, effectivePlan.DnsimpleAccountID, dnsZone)
		if err != nil {
			return nil, err
		}
		hostnameRecordName := relativeRecordName(effectivePlan.Hostname, dnsZone)
		if record, ok := dnsimpleRecordByName(records, hostnameRecordName); ok {
			return nil, Error{Msg: fmt.Sprintf("Server name %q is unavailable. DNSimple record %s already exists with content %s.", effectivePlan.Name, effectivePlan.Hostname, valueString(record["content"]))}
		}
		wildcardRecordName := relativeRecordName(effectivePlan.WildcardHostname, dnsZone)
		if record, ok := dnsimpleRecordByName(records, wildcardRecordName); ok {
			return nil, Error{Msg: fmt.Sprintf("Server name %q is unavailable. DNSimple record %s already exists with content %s.", effectivePlan.Name, effectivePlan.WildcardHostname, valueString(record["content"]))}
		}
		createArgs := []string{
			"linodes", "create",
			"--region", effectivePlan.Region,
			"--type", effectivePlan.LinodeType,
			"--image", effectivePlan.Image,
			"--label", effectivePlan.Label,
			"--root_pass", rootPass,
		}
		createArgs = appendLinodeAuthorizedKeyArgs(createArgs, sshKeys)
		createArgs = append(createArgs, "--metadata.user_data", base64UserData(rendered))
		linodePayload, err := runLinodeCLICommand(createArgs)
		if err != nil {
			return nil, err
		}
		linodeID, err = apiIDString(linodePayload["id"])
		if err != nil {
			return nil, Error{Msg: fmt.Sprintf("Linode response included an invalid id: %v", err)}
		}
		linodeIP = linodeIPFromRecord(linodePayload)
		if linodeIP == "" {
			return nil, Error{Msg: "Linode response did not include an IPv4 address."}
		}
		created = CreatedServer{Provider: effectivePlan.Provider, ProviderID: linodeID, Name: effectivePlan.Name, Hostname: effectivePlan.Hostname, IPv4: linodeIP}
		fmt.Println("Creating Linode")
		currentPhase = "linode_created"
		partial := serverStateRecordWithStatus(effectivePlan, created, DNSState{}, TLSState{}, createdAt, now, "provisioning", currentPhase)
		if err := upsertStateRecord(serverStatePath, partial); err != nil {
			return nil, err
		}
	}
	phaseRank := provisioningPhaseRank(currentPhase)
	if firewallMode := firewallPlanMode(effectivePlan); firewallMode == "managed" && phaseRank < provisioningPhaseRank("firewall_configured") {
		fmt.Println("Configuring firewall")
		firewallID, err := ensureManagedFirewall(effectivePlan, created, existingRecord, serverStatePath, now)
		if err != nil {
			return nil, err
		}
		effectivePlan.Firewall.ID = firewallID
		effectivePlan.Firewall.DeviceID = created.ProviderID
		phaseRank = provisioningPhaseRank("firewall_configured")
		currentPhase = "firewall_configured"
		if err := upsertStateRecord(serverStatePath, serverStateRecordWithStatus(effectivePlan, created, DNSState{}, TLSState{}, createdAt, now, "provisioning", currentPhase)); err != nil {
			return nil, err
		}
	}
	if phaseRank < provisioningPhaseRank("dns_configured") {
		fmt.Println("Configuring DNS")
		dnsZone, err := dnsimpleZoneLookup(effectivePlan, dnsimpleToken)
		if err != nil {
			return nil, err
		}
		if err := dnsimpleUpsertHostnameRecords(dnsimpleToken, effectivePlan.DnsimpleAccountID, dnsZone, effectivePlan.Hostname, linodeIP); err != nil {
			return nil, Error{Msg: renderProvisionPartialFailure(effectivePlan, created, serverStatePath, err)}
		}
		dnsState = dnsStateRecord(effectivePlan, dnsZone, linodeIP)
		if tlsState.Provider == "" {
			tlsState = tlsStateRecord(effectivePlan)
		}
		currentPhase = "dns_configured"
		if err := upsertStateRecord(serverStatePath, serverStateRecordWithStatus(effectivePlan, created, dnsState, TLSState{}, createdAt, now, "provisioning", currentPhase)); err != nil {
			return nil, err
		}
	}
	if !effectivePlan.Wait {
		fmt.Print(renderProvisionPaused(effectivePlan, created, dnsState, serverStatePath, currentPhase, sshKeys))
		return &ServerCreateResult{Server: created, DNS: dnsState, TLS: tlsState, StatePath: serverStatePath, CloudInitPath: previewPath}, nil
	}
	if phaseRank < provisioningPhaseRank("cloud_init_ready") {
		fmt.Println("Waiting for SSH")
		if err := waitForTCPPortFn(created.IPv4, 22, effectivePlan.SshTimeout); err != nil {
			return nil, err
		}
		fmt.Println("Waiting for cloud-init and enabling wildcard TLS")
		if err := runSSHCommandFn(effectivePlan.SshUser, created.IPv4, "cloud-init status --wait && sudo /usr/local/bin/nf-enable-wildcard-tls", effectivePlan.CloudInitTimeout+effectivePlan.TLSTimeout); err != nil {
			return nil, Error{Msg: fmt.Sprintf("%s\nRecovery: ssh %s@%s \"sudo /usr/local/bin/nf-enable-wildcard-tls\"", strings.TrimSpace(err.Error()), effectivePlan.SshUser, effectivePlan.Hostname)}
		}
		currentPhase = "tls_configured"
		if err := upsertStateRecord(serverStatePath, serverStateRecordWithStatus(effectivePlan, created, dnsState, tlsStateRecord(effectivePlan), createdAt, now, "provisioning", currentPhase)); err != nil {
			return nil, err
		}
	} else if currentPhase != "tls_configured" {
		currentPhase = "tls_configured"
	}
	tlsState = tlsStateRecord(effectivePlan)
	fmt.Println("Checking server health")
	healthURL := effectivePlan.HealthURL + "/healthz"
	if err := checkHTTPSHealthFn(healthURL, effectivePlan.Name, effectivePlan.Hostname, effectivePlan.HealthTimeout); err != nil {
		return nil, Error{Msg: renderProvisionHealthFailure(effectivePlan, created, dnsState, tlsState, serverStatePath, sshKeys, err)}
	}
	if err := upsertStateRecord(serverStatePath, serverStateRecordWithStatus(effectivePlan, created, dnsState, tlsState, createdAt, now, "provisioned", "complete")); err != nil {
		return nil, err
	}
	fmt.Print(renderProvisionSuccess(effectivePlan, created, dnsState, tlsState, serverStatePath, firstNonEmpty(previewPath, "not written"), sshKeys))
	return &ServerCreateResult{Server: created, DNS: dnsState, TLS: tlsState, StatePath: serverStatePath, CloudInitPath: previewPath}, nil
}

func matchesProvisionStateRecord(record map[string]any, provider, providerID, hostname, name, label string) bool {
	if strings.ToLower(valueString(record["provider"])) != strings.ToLower(strings.TrimSpace(provider)) {
		return false
	}
	if providerID != "" {
		for _, key := range []string{"provider_id", "linode_id", "id"} {
			if valueString(record[key]) == providerID {
				return true
			}
		}
	}
	if hostname != "" && valueString(record["hostname"]) == hostname {
		return true
	}
	if name != "" && valueString(record["name"]) == name {
		return true
	}
	if label != "" && valueString(record["label"]) == label {
		return true
	}
	return false
}

func loadStatePayload(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []map[string]any{}, nil
		}
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var payload any
	if err := dec.Decode(&payload); err != nil {
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
	if strings.ToLower(valueString(record["provider"])) != strings.ToLower(valueString(candidate["provider"])) {
		return false
	}
	if candidateID := firstNonEmpty(valueString(candidate["provider_id"]), valueString(candidate["linode_id"]), valueString(candidate["id"])); candidateID != "" {
		for _, key := range []string{"provider_id", "linode_id", "id"} {
			if valueString(record[key]) == candidateID {
				return true
			}
		}
	}
	for _, key := range []string{"hostname", "name", "slug", "label"} {
		candidateValue := strings.TrimSpace(valueString(candidate[key]))
		if candidateValue != "" && valueString(record[key]) == candidateValue {
			return true
		}
	}
	return false
}

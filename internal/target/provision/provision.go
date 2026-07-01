package provision

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/linode/linodego"
	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/ui"
	"golang.org/x/oauth2"
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
	AdminerUser       string
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
	TargetMode        bool
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
	AdminerUser       string
	AdminerHostname   string
	AdminerURL        string
	AdminerHTPasswd   string
	AdminerMySQLHash  string
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
	TargetMode        bool
	ReuseExisting     bool
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
const adminerToolName = "AdminNeo"
const adminerVersion = "5.4.1"
const adminerDownloadURL = "https://www.adminneo.org/files/5.4.1/mysql_en_default/adminneo-5.4.1.php"
const dbDefaultUser = "admin"

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

func releaseForImage(image string) (ubuntuRelease, bool) {
	trimmed := strings.TrimSpace(image)
	for _, stack := range ubuntuStackMatrix {
		if stack.image == trimmed {
			return ubuntuRelease{version: stack.version, label: stack.label, image: stack.image, php: stack.php}, true
		}
	}
	return ubuntuRelease{}, false
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

func adminerCredentialState(plan Plan) map[string]any {
	return map[string]any{
		"derived":  true,
		"identity": plan.Hostname,
		"purpose":  "db-admin",
		"stored":   false,
		"user":     plan.AdminerUser,
	}
}

func adminerHtpasswdHash(password string) string {
	sum := sha1.Sum([]byte(password))
	return "{SHA}" + base64.StdEncoding.EncodeToString(sum[:])
}

func adminerMySQLPasswordHash(password string) string {
	first := sha1.Sum([]byte(password))
	second := sha1.Sum(first[:])
	return "*" + strings.ToUpper(fmt.Sprintf("%x", second[:]))
}

func deriveAdminerHostname(hostname, user string) string {
	hostname = strings.TrimSpace(hostname)
	user = strings.TrimSpace(user)
	if hostname == "" || user == "" {
		return ""
	}
	return user + "." + hostname
}

func deriveAdminerURL(hostname, user string) string {
	adminerHostname := deriveAdminerHostname(hostname, user)
	if adminerHostname == "" {
		return ""
	}
	return "https://" + adminerHostname + "/"
}

func validateAdminerUser(user string) error {
	trimmed := strings.TrimSpace(user)
	if trimmed == "" {
		return Error{Msg: "Database user must be a non-empty MySQL username"}
	}
	if len(trimmed) > 32 {
		return Error{Msg: "Database user must be 32 characters or fewer"}
	}
	for _, r := range trimmed {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return Error{Msg: "Database user must use only letters, numbers, underscores, and hyphens"}
	}
	return nil
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

func selectUbuntuStack(explicit, image string, nonInteractive bool) (ubuntuRelease, error) {
	if v := strings.TrimSpace(explicit); v != "" {
		return releaseForUbuntu(v)
	}
	if release, ok := releaseForImage(image); ok {
		return release, nil
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

func valueInt(value any) int {
	switch typed := value.(type) {
	case nil:
		return 0
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
		if parsed, err := strconv.ParseFloat(typed.String(), 64); err == nil {
			return int(parsed)
		}
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	case int:
		return typed
	case int8:
		return int(typed)
	case int16:
		return int(typed)
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case uint:
		return int(typed)
	case uint8:
		return int(typed)
	case uint16:
		return int(typed)
	case uint32:
		return int(typed)
	case uint64:
		return int(typed)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed
		}
	}
	return 0
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
	if plan.ReuseExisting {
		return "unchanged on existing target"
	}
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
	Provider   string   `json:"provider"`
	ProviderID string   `json:"provider_id"`
	Name       string   `json:"name"`
	Hostname   string   `json:"hostname"`
	IPv4       string   `json:"ipv4"`
	Region     string   `json:"region,omitempty"`
	Type       string   `json:"type,omitempty"`
	Image      string   `json:"image,omitempty"`
	Tags       []string `json:"tags,omitempty"`
}

type ServerCreatePlan struct {
	Plan     Plan
	RootPass string
	UserData string
	SSHKeys  []SSHAuthorizedKey
}

type FirewallResult struct {
	ID       string
	DeviceID string
}

type ServerProvider interface {
	Name() string
	ListSSHKeys(ctx context.Context) ([]SSHAuthorizedKey, error)
	FindServerByLabel(ctx context.Context, label string) (*CreatedServer, error)
	CreateServer(ctx context.Context, plan ServerCreatePlan) (*CreatedServer, error)
	EnsureFirewall(ctx context.Context, plan FirewallPlan) (*FirewallResult, error)
	AssignFirewall(ctx context.Context, firewallID, serverID string) error
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
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	TTL     int    `json:"ttl,omitempty"`
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
	dnsimpleZoneLookup                  = defaultDNSimpleZoneLookup
	dnsimpleListARecordsFn              = defaultDNSimpleListARecords
	dnsimpleUpsertARecordRun            = defaultDNSimpleUpsertARecord
	dnsimpleWaitForRecordDistributionFn = defaultDNSimpleWaitForRecordDistribution
	waitForTCPPortFn                    = waitForTCPPort
	runSSHCommandFn                     = runSSHCommand
	checkHTTPSHealthFn                  = checkHTTPSHealth
	healthHTTPClientFn                  = defaultHealthHTTPClient
	selectVersionFn                     = ui.Select
	promptStringFn                      = ui.PromptString
	confirmFn                           = ui.Confirm
	multiSelectFn                       = ui.MultiSelect
	secretSaltFn                        = passwords.SecretSalt
	currentTime                         = time.Now
	serverProviderFactory               = newServerProvider
)

func newServerProvider(plan Plan) (ServerProvider, error) {
	switch strings.ToLower(strings.TrimSpace(plan.Provider)) {
	case "linode":
		token, err := linodeTokenEnv()
		if err != nil {
			return nil, err
		}
		return &linodeProvider{client: NewLinodeClient(token)}, nil
	default:
		return nil, Error{Msg: fmt.Sprintf("Unsupported provider %q. Only linode is available in this slice.", plan.Provider)}
	}
}

func NewLinodeClient(token string) linodego.Client {
	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	oauth2Client := &http.Client{Transport: &oauth2.Transport{Source: tokenSource}}
	client := linodego.NewClient(oauth2Client)
	client.SetUserAgent("nf")
	return client
}

type linodeProvider struct{ client linodego.Client }

func (p *linodeProvider) Name() string { return "linode" }

func (p *linodeProvider) ListSSHKeys(ctx context.Context) ([]SSHAuthorizedKey, error) {
	keys, err := p.client.ListSSHKeys(ctx, nil)
	if err != nil {
		return nil, Error{Msg: fmt.Sprintf("listing SSH keys: %v", err)}
	}
	out := make([]SSHAuthorizedKey, 0, len(keys))
	for _, key := range keys {
		created := ""
		if key.Created != nil {
			created = key.Created.UTC().Format(time.RFC3339)
		}
		out = append(out, SSHAuthorizedKey{Source: "linode-profile", ID: strconv.Itoa(key.ID), Label: key.Label, Created: created, PublicKey: key.SSHKey})
	}
	return out, nil
}

func (p *linodeProvider) FindServerByLabel(ctx context.Context, label string) (*CreatedServer, error) {
	instances, err := p.client.ListInstances(ctx, nil)
	if err != nil {
		return nil, Error{Msg: fmt.Sprintf("checking label availability: %v", err)}
	}
	for _, inst := range instances {
		if !strings.EqualFold(strings.TrimSpace(inst.Label), strings.TrimSpace(label)) {
			continue
		}
		return createdServerFromLinodeInstance(inst), nil
	}
	return nil, nil
}

func createdServerFromLinodeInstance(inst linodego.Instance) *CreatedServer {
	ip := ""
	for _, candidate := range inst.IPv4 {
		if candidate != nil {
			ip = candidate.String()
			break
		}
	}
	id := strconv.Itoa(inst.ID)
	return &CreatedServer{Provider: "linode", ProviderID: id, Name: inst.Label, Hostname: inst.Label, IPv4: ip, Region: inst.Region, Type: inst.Type, Image: inst.Image, Tags: inst.Tags}
}

func createdServerHasTag(server CreatedServer, tag string) bool {
	for _, candidate := range server.Tags {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(tag)) {
			return true
		}
	}
	return false
}

func (p *linodeProvider) CreateServer(ctx context.Context, create ServerCreatePlan) (*CreatedServer, error) {
	inst, err := p.client.CreateInstance(ctx, linodego.InstanceCreateOptions{
		Region:         create.Plan.Region,
		Type:           create.Plan.LinodeType,
		Image:          create.Plan.Image,
		Label:          create.Plan.Label,
		Tags:           []string{"nf"},
		RootPass:       create.RootPass,
		AuthorizedKeys: sshKeyBodies(create.SSHKeys),
		Metadata:       &linodego.InstanceMetadataOptions{UserData: base64UserData(create.UserData)},
	})
	if err != nil {
		return nil, Error{Msg: fmt.Sprintf("creating Linode: %v", err)}
	}
	created := createdServerFromLinodeInstance(*inst)
	created.Name = create.Plan.Name
	created.Hostname = create.Plan.Hostname
	if created.IPv4 == "" {
		return nil, Error{Msg: "Linode response did not include an IPv4 address."}
	}
	return created, nil
}

func (p *linodeProvider) EnsureFirewall(ctx context.Context, plan FirewallPlan) (*FirewallResult, error) {
	if strings.TrimSpace(plan.ID) != "" {
		return &FirewallResult{ID: strings.TrimSpace(plan.ID), DeviceID: strings.TrimSpace(plan.DeviceID)}, nil
	}
	label := firstNonEmpty(plan.Label, firewallManagedLabel)
	firewalls, err := p.client.ListFirewalls(ctx, nil)
	if err != nil {
		return nil, Error{Msg: fmt.Sprintf("creating firewall: listing existing firewalls: %v", err)}
	}
	for _, firewall := range firewalls {
		if strings.EqualFold(strings.TrimSpace(firewall.Label), label) {
			id := strconv.Itoa(firewall.ID)
			if err := p.updateFirewallRules(ctx, firewall.ID, plan); err != nil {
				return &FirewallResult{ID: id, DeviceID: strings.TrimSpace(plan.DeviceID)}, err
			}
			return &FirewallResult{ID: id, DeviceID: strings.TrimSpace(plan.DeviceID)}, nil
		}
	}
	firewall, err := p.client.CreateFirewall(ctx, linodego.FirewallCreateOptions{Label: label, Rules: linodeFirewallRuleSet(plan)})
	if err != nil {
		return nil, Error{Msg: fmt.Sprintf("creating firewall: %v", err)}
	}
	return &FirewallResult{ID: strconv.Itoa(firewall.ID), DeviceID: strings.TrimSpace(plan.DeviceID)}, nil
}

func (p *linodeProvider) updateFirewallRules(ctx context.Context, id int, plan FirewallPlan) error {
	if _, err := p.client.UpdateFirewallRules(ctx, id, linodeFirewallRuleSet(plan)); err != nil {
		return Error{Msg: fmt.Sprintf("creating firewall: updating rules: %v", err)}
	}
	return nil
}

func linodeFirewallRuleSet(plan FirewallPlan) linodego.FirewallRuleSet {
	ipv4 := []string{"0.0.0.0/0"}
	ipv6 := []string{"::/0"}
	rules := make([]linodego.FirewallRule, 0, len(firewallRules()))
	for _, rule := range firewallRules() {
		rules = append(rules, linodego.FirewallRule{Action: rule.Action, Label: rule.Label, Protocol: linodego.NetworkProtocol(rule.Protocol), Ports: rule.Ports, Addresses: linodego.NetworkAddresses{IPv4: &ipv4, IPv6: &ipv6}})
	}
	return linodego.FirewallRuleSet{Inbound: rules, InboundPolicy: firstNonEmpty(plan.InboundPolicy, firewallInboundPolicy), Outbound: []linodego.FirewallRule{}, OutboundPolicy: firstNonEmpty(plan.OutboundPolicy, firewallOutboundPolicy)}
}

func (p *linodeProvider) AssignFirewall(ctx context.Context, firewallID, serverID string) error {
	fid, err := strconv.Atoi(strings.TrimSpace(firewallID))
	if err != nil {
		return Error{Msg: fmt.Sprintf("assigning firewall: invalid firewall id %q", firewallID)}
	}
	sid, err := strconv.Atoi(strings.TrimSpace(serverID))
	if err != nil {
		return Error{Msg: fmt.Sprintf("assigning firewall: invalid server id %q", serverID)}
	}
	devices, err := p.client.ListFirewallDevices(ctx, fid, nil)
	if err != nil {
		return Error{Msg: fmt.Sprintf("assigning firewall: listing devices: %v", err)}
	}
	for _, device := range devices {
		if device.Entity.Type == linodego.FirewallDeviceLinode && device.Entity.ID == sid {
			return nil
		}
	}
	if _, err := p.client.CreateFirewallDevice(ctx, fid, linodego.FirewallDeviceCreateOptions{ID: sid, Type: linodego.FirewallDeviceLinode}); err != nil {
		return Error{Msg: fmt.Sprintf("assigning firewall: %v", err)}
	}
	return nil
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

func serverDomain() string {
	return firstNonEmpty(baseDomainConfigValue(), envwizard.Value("NF_SERVER_DOMAIN"), envwizard.Value("DNSIMPLE_ZONE_NAME"), "nonfiction.dev")
}

func baseDomainConfigValue() string {
	data, err := os.ReadFile(config.ConfigFile())
	if err != nil {
		return ""
	}
	values := map[string]string{}
	if err := json.Unmarshal(data, &values); err != nil {
		return ""
	}
	return strings.TrimSpace(values["base_domain"])
}

func dnsimpleAccountIDConfigValue() string {
	data, err := os.ReadFile(config.ConfigFile())
	if err != nil {
		return ""
	}
	values := map[string]string{}
	if err := json.Unmarshal(data, &values); err != nil {
		return ""
	}
	return strings.TrimSpace(values["dnsimple_account_id"])
}

func globalConfigValue(key string) string {
	data, err := os.ReadFile(config.ConfigFile())
	if err != nil {
		return ""
	}
	values := map[string]string{}
	if err := json.Unmarshal(data, &values); err != nil {
		return ""
	}
	return strings.TrimSpace(values[key])
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
		{Keys: []string{"NF_PASSWORD_SALT", "NF_SECRET_SALT"}, Prompt: "NF_PASSWORD_SALT (used for derived passwords): ", Secret: true, WriteKey: "NF_PASSWORD_SALT", Required: true},
		{Keys: []string{"DNSIMPLE_TOKEN"}, Prompt: "DNSimple token: ", Secret: true, WriteKey: "DNSIMPLE_TOKEN", Required: true},
		{Keys: []string{"LINODE_TOKEN", "LINODE_CLI_TOKEN"}, Prompt: "LINODE_TOKEN (Linode API token): ", Secret: true, WriteKey: "LINODE_TOKEN", Required: true},
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
      upload_max_filesize = 1024M
      post_max_size = 1024M
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
  - path: /etc/nginx/conf.d/nf-server-names-hash.conf
    permissions: '0644'
    content: |
      server_names_hash_bucket_size 128;
      server_names_hash_max_size 4096;
  - path: /etc/nginx/sites-available/nf-server
    permissions: '0644'
    content: |
      server { listen 80 default_server; listen [::]:80 default_server; server_name __HOSTNAME__; root /var/www/nf; index index.html; location = /healthz { default_type application/json; return 200 '{"server":"__NAME__","hostname":"__HOSTNAME__","status":"ready"}'; } location / { try_files $uri $uri/ /index.html; } }
  - path: /usr/local/bin/nf-install-db-ui
    permissions: '0755'
    content: |
      #!/usr/bin/env bash
      set -euo pipefail
      install -d -o root -g www-data -m 0750 /var/www/shared/db /var/lib/nf
      curl -fsSL __ADMINER_DOWNLOAD_URL__ -o /var/www/shared/db/index.php
      cat >/var/www/shared/db/adminneo-config.php <<'EOF'
      <?php return ["defaultDriver"=>"mysql","defaultServer"=>"localhost","navigationMode"=>"dual","preferSelection"=>true,"hiddenDatabases"=>["__system"]];
      EOF
      printf '%s:%s\n' '__ADMINER_USER__' '__ADMINER_HTPASSWD__' >/var/lib/nf/db.htpasswd
      chown -R root:www-data /var/www/shared/db /var/lib/nf/db.htpasswd
      chmod 0640 /var/www/shared/db/index.php /var/www/shared/db/adminneo-config.php /var/lib/nf/db.htpasswd
      mariadb -uroot <<SQL
      CREATE USER IF NOT EXISTS '__ADMINER_USER__'@'localhost' IDENTIFIED BY PASSWORD '__ADMINER_MYSQL_HASH__';
      ALTER USER '__ADMINER_USER__'@'localhost' IDENTIFIED BY PASSWORD '__ADMINER_MYSQL_HASH__';
      FLUSH PRIVILEGES;
      SQL
  - path: /usr/local/bin/nf-write-server-health-page
    permissions: '0755'
    content: |
      #!/usr/bin/env bash
      set -euo pipefail

      install -d -o __SSH_USER__ -g www-data -m 2775 /var/www/nf
      cat >/var/www/nf/index.html <<'EOF'
      <!doctype html>
      <html lang="en">
      <head>
        <meta charset="utf-8">
        <meta name="viewport" content="width=device-width, initial-scale=1">
        <link rel="icon" href="/favicon.svg">
        <title>nf target __NAME__</title>
        <style>
          :root{color-scheme:dark}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;overflow:hidden;background:radial-gradient(circle at 20% 20%,rgba(144,93,250,.22),transparent 32rem),radial-gradient(circle at 80% 10%,rgba(85,1,210,.24),transparent 28rem),linear-gradient(135deg,#09090f,#11111c 50%,#08070d);color:#f8fafc;font-family:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}main{width:min(70vw,34rem);padding:clamp(2rem,6vw,3.5rem);border:1px solid rgba(255,255,255,.12);border-radius:1.75rem;background:rgba(15,15,26,.78);box-shadow:0 2rem 6rem rgba(0,0,0,.45);text-align:center;backdrop-filter:blur(18px)}.logo{display:block;width:5.5rem;height:5.5rem;margin:0 auto 1.5rem;border-radius:1.25rem;box-shadow:0 1.25rem 3rem rgba(85,1,210,.35)}.pill{display:inline-block;margin-bottom:1rem;padding:.35rem .8rem;border-radius:999px;background:rgba(34,197,94,.14);color:#86efac;font-size:.875rem;font-weight:700;letter-spacing:.04em;text-transform:uppercase}h1{margin:0 0 1rem;font-size:clamp(2rem,5vw,3rem);line-height:1.1}p{margin:.5rem 0;font-size:1rem;color:#cbd5e1}.label{color:#94a3b8}
        </style>
      </head>
      <body>
        <main>
          <svg class="logo" xmlns="http://www.w3.org/2000/svg" width="400" height="400" viewBox="0 0 400 400" role="img" aria-label="Nonfiction logo">
            <defs>
              <linearGradient id="background" x1="0" y1="0" x2="400" y2="0" gradientUnits="userSpaceOnUse">
                <stop offset="0%" stop-color="#905DFA"/>
                <stop offset="100%" stop-color="#5501D2"/>
              </linearGradient>
            </defs>
            <rect width="400" height="400" fill="url(#background)"/>
            <path d="M0 0 C5.64804263 4.84760347 10.76425856 10.21957697 14 17 C14 17.66 14 18.32 14 19 C14.66 19 15.32 19 16 19 C16 10.42 16 1.84 16 -7 C22.93 -7 29.86 -7 37 -7 C36.505 78.14 36.505 78.14 36 165 C29.07 165 22.14 165 15 165 C14.970271 160.07235718 14.970271 160.07235718 14.93994141 155.04516602 C14.87295699 144.17659327 14.79531851 133.30811528 14.71247292 122.43965244 C14.66258242 115.85179807 14.61625163 109.26395918 14.578125 102.67602539 C14.54118084 96.31524064 14.4947959 89.95458765 14.44193649 83.59391594 C14.42344107 81.17034154 14.40831532 78.74673895 14.39665604 76.32312202 C14.37971063 72.92173978 14.35065521 69.52065612 14.31884766 66.11938477 C14.31668747 65.12316101 14.31452728 64.12693726 14.31230164 63.1005249 C14.24145194 57.14790433 13.53078941 51.75007168 12 46 C11.72800781 44.89914063 11.45601563 43.79828125 11.17578125 42.6640625 C7.57502313 29.98809046 1.26288994 19.75708373 -10.10546875 12.69140625 C-21.82380098 6.50358285 -38.1594566 5.38036188 -50.921875 8.875 C-53.84955042 10.027867 -56.3566324 11.29916218 -59 13 C-59.84691406 13.54269531 -60.69382813 14.08539063 -61.56640625 14.64453125 C-66.56825242 18.20122122 -69.38521907 21.47990693 -72 27 C-72.34675781 27.62777344 -72.69351563 28.25554687 -73.05078125 28.90234375 C-77.81511998 37.65140214 -78.29955614 46.3138163 -78.31884766 56.12915039 C-78.32889328 57.31064667 -78.3389389 58.49214294 -78.34928894 59.70944214 C-78.38001571 63.5888422 -78.39712705 67.46817357 -78.4140625 71.34765625 C-78.43170795 74.04765785 -78.45226179 76.74763523 -78.4727478 79.44761658 C-78.51937759 85.81153307 -78.55619769 92.1754663 -78.58942068 98.53946537 C-78.62775432 105.78989266 -78.67713469 113.04023186 -78.72740483 120.29058468 C-78.83048942 135.19365879 -78.91943368 150.09678591 -79 165 C-85.93 165 -92.86 165 -100 165 C-100.11642661 149.43864646 -100.20531801 133.87745068 -100.25906086 118.3157692 C-100.28469311 111.08796483 -100.31955119 103.86043832 -100.37719727 96.6328125 C-100.42749738 90.32313631 -100.45939939 84.01366159 -100.47044247 77.70379043 C-100.47688406 74.37165164 -100.49444201 71.04053952 -100.52865028 67.70850372 C-100.77489859 42.70278559 -98.43472708 20.34664384 -80.75 1.375 C-59.58507528 -18.86770578 -22.71659706 -16.31398929 0 0 Z " fill="#fff" transform="translate(231,131)"/>
          </svg>
          <div class="pill">READY</div>
          <h1>__NAME__</h1>
          <p>https://__HOSTNAME__/healthz</p>
        </main>
      </body>
      </html>
      EOF
      cat >/var/www/nf/favicon.svg <<'EOF'
          <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 400"><defs><linearGradient id="a"><stop stop-color="#905dfa"/><stop offset="1" stop-color="#5501d2"/></linearGradient></defs><path fill="url(#a)" d="M0 0h400v400H0z"/><path d="M231 131c5.648 4.848 10.764 10.22 14 17v2h2v-26h21l-1 172h-21l-.06-9.955q-.102-16.303-.228-32.605-.076-9.882-.134-19.764-.056-9.54-.136-19.082-.029-3.636-.045-7.27a2335 2335 0 0 0-.078-10.205l-.007-3.018c-.07-5.953-.781-11.35-2.312-17.101l-.824-3.336c-3.601-12.676-9.913-22.907-21.281-29.973-11.719-6.187-28.054-7.31-40.817-3.816-2.928 1.153-5.435 2.424-8.078 4.125l-2.566 1.645C164.432 149.2 161.614 152.48 159 158l-1.05 1.902c-4.765 8.75-5.25 17.412-5.269 27.227l-.03 3.58c-.031 3.88-.048 7.76-.065 11.639q-.027 4.05-.059 8.1-.068 9.546-.116 19.091c-.039 7.25-.088 14.501-.138 21.752A29590 29590 0 0 0 152 296h-21q-.176-23.342-.26-46.684c-.025-7.228-.06-14.456-.117-21.683q-.076-9.465-.093-18.93-.008-4.996-.059-9.994c-.246-25.006 2.094-47.362 19.779-66.334 21.165-20.243 58.033-17.689 80.75-1.375" fill="#fff"/></svg>
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
      server { listen 80 default_server; listen [::]:80 default_server; server_name __HOSTNAME__; root /var/www/nf; index index.html; location = /healthz { default_type application/json; return 200 '{"server":"__NAME__","hostname":"__HOSTNAME__","status":"ready"}'; } location / { try_files $uri $uri/ /index.html; } }
      server { listen 443 ssl http2 default_server; listen [::]:443 ssl http2 default_server; server_name __HOSTNAME__; include /etc/nginx/snippets/nf-wildcard-cert.conf; root /var/www/nf; index index.html; location = /healthz { default_type application/json; return 200 '{"server":"__NAME__","hostname":"__HOSTNAME__","status":"ready"}'; } location / { try_files $uri $uri/ /index.html; } }
      EOF

      cat >/etc/nginx/sites-available/nf-db <<'EOF'
      server { listen 80; listen [::]:80; server_name __ADMINER_HOSTNAME__; return 301 https://__ADMINER_HOSTNAME__$request_uri; }
      server { listen 443 ssl http2; listen [::]:443 ssl http2; server_name __ADMINER_HOSTNAME__; include /etc/nginx/snippets/nf-wildcard-cert.conf; include /etc/nginx/snippets/nf-security-headers.conf; client_max_body_size 1024M; root /var/www/shared/db; index index.php; access_log /var/log/nginx/sites/db.access.log; error_log /var/log/nginx/sites/db.error.log; auth_basic "nf database"; auth_basic_user_file /var/lib/nf/db.htpasswd; location / { try_files $uri $uri/ /index.php?$args; } location ~ \.php$ { include /etc/nginx/snippets/nf-fastcgi-php.conf; fastcgi_pass unix:__PHP_FPM_SOCKET__; } }
      EOF
      ln -sf /etc/nginx/sites-available/nf-db /etc/nginx/sites-enabled/nf-db
      nginx -t
      systemctl reload nginx
      systemctl disable --now nf-wildcard-tls.timer || true
  - path: /etc/systemd/system/nf-wildcard-tls.service
    permissions: '0644'
    content: |
      [Unit]
      Description=Issue nf wildcard TLS certificate
      Wants=network-online.target
      After=network-online.target nginx.service

      [Service]
      Type=oneshot
      ExecStart=/usr/local/bin/nf-enable-wildcard-tls
  - path: /etc/systemd/system/nf-wildcard-tls.timer
    permissions: '0644'
    content: |
      [Unit]
      Description=Retry nf wildcard TLS certificate setup

      [Timer]
      OnBootSec=2min
      OnUnitActiveSec=5min
      Persistent=true
      Unit=nf-wildcard-tls.service

      [Install]
      WantedBy=timers.target
  - path: /usr/local/bin/nf-write-server-marker
    permissions: '0755'
    content: |
      #!/usr/bin/env bash
      set -euo pipefail

      mkdir -p /etc/nf /var/lib/nf
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
      cat >/var/lib/nf/target.json <<EOF
      {
        "schema": 1,
        "managed_by": "nf",
        "provider": "__SERVER_PROVIDER__",
        "name": "__NAME__",
        "hostname": "__HOSTNAME__",
        "php_version": "__PHP_VERSION__",
        "php_service": "__PHP_FPM_SERVICE__",
        "php_socket": "__PHP_FPM_SOCKET__",
        "db": {"tool":"__ADMINER_TOOL__","version":"__ADMINER_VERSION__","hostname":"__ADMINER_HOSTNAME__","url":"__ADMINER_URL__","path":"/var/www/shared/db/index.php","config_path":"/var/www/shared/db/adminneo-config.php","download_url":"__ADMINER_DOWNLOAD_URL__","user":"__ADMINER_USER__","auth":{"type":"basic","user":"__ADMINER_USER__","password":{"derived":true,"identity":"__HOSTNAME__","purpose":"db-admin","stored":false}},"database":{"host":"localhost","user":"__ADMINER_USER__","grants":"site-env-databases"}},
        "sites_path": "/var/lib/nf/sites.json",
        "created_at": "${created_at}"
      }
      EOF
      if [ ! -f /var/lib/nf/sites.json ]; then
        printf '[]\n' >/var/lib/nf/sites.json
      fi
      chown -R __SSH_USER__:www-data /var/lib/nf
      chmod 2775 /var/lib/nf
      chmod 0664 /var/lib/nf/sites.json /var/lib/nf/target.json
      if [ -f /var/lib/nf/db.htpasswd ]; then chown root:www-data /var/lib/nf/db.htpasswd && chmod 0640 /var/lib/nf/db.htpasswd; fi
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
  - install -d -o __SSH_USER__ -g www-data -m 2775 /var/www /var/www/nf /var/www/sites /var/www/shared /var/log/nginx/sites /var/lib/nf
  - install -d -o www-data -g www-data -m 2775 /var/cache/nginx/nf/sites
  - usermod -aG www-data __SSH_USER__
  - if [ -f /root/.ssh/authorized_keys ]; then install -d -o __SSH_USER__ -g __SSH_USER__ -m 0700 /home/__SSH_USER__/.ssh && cp /root/.ssh/authorized_keys /home/__SSH_USER__/.ssh/authorized_keys && chown __SSH_USER__:__SSH_USER__ /home/__SSH_USER__/.ssh/authorized_keys && chmod 0600 /home/__SSH_USER__/.ssh/authorized_keys; fi
  - chown -R __SSH_USER__:www-data /var/www /var/log/nginx/sites
  - chmod 2775 /var/www /var/www/sites /var/www/shared /var/log/nginx/sites
  - rm -f /etc/nginx/sites-enabled/default
  - ln -sf /etc/nginx/sites-available/nf-server /etc/nginx/sites-enabled/nf-server
  - /usr/local/bin/nf-write-server-health-page
  - /usr/local/bin/nf-install-db-ui
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
  - systemctl daemon-reload
  - systemctl enable --now nf-wildcard-tls.timer
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

func cloudInitReplacements(plan Plan, sshPublicKeys []string, dnsimpleToken string) map[string]string {
	return map[string]string{
		"__PACKAGE_LIST__":         cloudInitPackageBlock(plan),
		"__SHORT_HOSTNAME__":       shortHostname(plan.Hostname),
		"__SSH_USER__":             plan.SshUser,
		"__NAME__":                 plan.Name,
		"__HOSTNAME__":             plan.Hostname,
		"__ADMINER_HOSTNAME__":     plan.AdminerHostname,
		"__ADMINER_URL__":          plan.AdminerURL,
		"__ADMINER_USER__":         plan.AdminerUser,
		"__ADMINER_TOOL__":         adminerToolName,
		"__ADMINER_VERSION__":      adminerVersion,
		"__ADMINER_DOWNLOAD_URL__": adminerDownloadURL,
		"__ADMINER_HTPASSWD__":     firstNonEmpty(plan.AdminerHTPasswd, "<adminer htpasswd hash>"),
		"__ADMINER_MYSQL_HASH__":   firstNonEmpty(plan.AdminerMySQLHash, "<adminer mysql password hash>"),
		"__DNSIMPLE_TOKEN__":       dnsimpleToken,
		"__DNSIMPLE_ACCOUNT_ID__":  plan.DnsimpleAccountID,
		"__SERVER_PROVIDER__":      plan.Provider,
		"__DNS_PROVIDER__":         plan.DnsProvider,
		"__UBUNTU_VERSION__":       plan.UbuntuVersion,
		"__IMAGE__":                plan.Image,
		"__PHP_FPM_SERVICE__":      plan.PHP.Service,
		"__PHP_FPM_SOCKET__":       plan.PHP.Socket,
		"__PHP_VERSION__":          plan.PHP.Version,
	}
}

func stackSummary(plan Plan) string {
	return plan.OS.Label + " / PHP " + plan.PHP.Version
}

func ubuntuDisplayLabel(plan Plan) string {
	return strings.TrimPrefix(plan.OS.Label, "Ubuntu ")
}

func renderCloudInit(plan Plan, actual bool, dnsimpleToken string, sshPublicKeys []string) (string, error) {
	plan = normalizePlan(plan)
	replacements := cloudInitReplacements(plan, sshPublicKeys, "<dnsimple token>")
	if actual {
		if dnsimpleToken == "" {
			return "", Error{Msg: "Missing secrets for cloud-init rendering."}
		}
		replacements = cloudInitReplacements(plan, sshPublicKeys, dnsimpleToken)
	}
	return compactCloudInitHealthPage(renderTemplate(cloudInitTemplate, replacements)), nil
}

func compactCloudInitHealthPage(rendered string) string {
	rendered = compactCloudInitHealthStyle(rendered)
	return compactCloudInitHealthLogo(rendered)
}

func compactCloudInitHealthStyle(rendered string) string {
	styleStart := strings.Index(rendered, ":root{color-scheme:dark}")
	if styleStart == -1 {
		return rendered
	}
	lineStart := strings.LastIndex(rendered[:styleStart], "\n") + 1
	lineEnd := strings.Index(rendered[styleStart:], "\n")
	if lineEnd == -1 {
		return rendered
	}
	indent := rendered[lineStart:styleStart]
	compact := indent + `body{margin:0;min-height:100vh;display:grid;place-items:center;background:radial-gradient(at 20% 20%,#2a1d4a,#0000 32rem),radial-gradient(at 80% 10%,#230b4b,#0000 28rem),#09090f;color:#f8fafc;font-family:system-ui,sans-serif}main{width:min(70vw,34rem);padding:3rem;border:1px solid #30234f;border-radius:1.75rem;background:#11111ccc;text-align:center}.logo{display:block;width:5.5rem;margin:0 auto 1rem;border-radius:1.25rem}.pill{display:inline-block;margin:0 0 1rem;padding:.35rem .8rem;border-radius:9em;background:#1b2f2d;color:#86efac;font-weight:700}h1{font-size:3rem;margin:.5rem 0}`
	return rendered[:lineStart] + compact + rendered[styleStart+lineEnd:]
}

func compactCloudInitHealthLogo(rendered string) string {
	svgLineStart := strings.Index(rendered, "\n          <svg class=\"logo\"")
	if svgLineStart == -1 {
		return rendered
	}
	svgStart := svgLineStart + 1
	svgEnd := strings.Index(rendered[svgStart:], "</svg>")
	if svgEnd == -1 {
		return rendered
	}
	markupStart := svgStart + strings.Index(rendered[svgStart:], "<svg")
	indent := rendered[svgStart:markupStart]
	compact := indent + `<img class="logo" src="/favicon.svg" alt="Nonfiction logo">`
	return rendered[:svgStart] + compact + rendered[svgStart+svgEnd+len("</svg>"):]
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
	if envwizard.Value("LINODE_TOKEN") == "" && envwizard.Value("LINODE_CLI_TOKEN") == "" {
		return Error{Msg: fmt.Sprintf("Expected LINODE_TOKEN in the environment or %s. LINODE_CLI_TOKEN is also accepted for convenience.", config.EnvFile())}
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
	if token := envwizard.Value("LINODE_TOKEN"); token != "" {
		return token, nil
	}
	if token := envwizard.Value("LINODE_CLI_TOKEN"); token != "" {
		return token, nil
	}
	return "", Error{Msg: fmt.Sprintf("Expected LINODE_TOKEN in the environment or %s. LINODE_CLI_TOKEN is also accepted for convenience.", config.EnvFile())}
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

func dnsStateRecord(plan Plan, zone string, hostnameRecord, wildcardRecord DNSRecord) DNSState {
	return DNSState{
		Provider:       plan.DnsProvider,
		Zone:           zone,
		HostnameRecord: hostnameRecord,
		WildcardRecord: wildcardRecord,
	}
}

func dnsRecordByName(records []DNSRecord, name string) (DNSRecord, bool) {
	for _, record := range records {
		if record.Name == name {
			return record, true
		}
	}
	return DNSRecord{}, false
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
		state.HostnameRecord = DNSRecord{ID: valueString(hostnameRecord["id"]), Name: valueString(hostnameRecord["name"]), Type: valueString(hostnameRecord["type"]), Content: valueString(hostnameRecord["content"]), TTL: valueInt(hostnameRecord["ttl"])}
	}
	if wildcardRecord, ok := block["wildcard_record"].(map[string]any); ok {
		state.WildcardRecord = DNSRecord{ID: valueString(wildcardRecord["id"]), Name: valueString(wildcardRecord["name"]), Type: valueString(wildcardRecord["type"]), Content: valueString(wildcardRecord["content"]), TTL: valueInt(wildcardRecord["ttl"])}
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

func firewallPlanMode(plan Plan) string {
	return strings.ToLower(strings.TrimSpace(plan.Firewall.Mode))
}

func ensureManagedFirewall(ctx context.Context, provider ServerProvider, plan Plan, created CreatedServer, existingRecord map[string]any, statePath, now string) (string, error) {
	if firewallPlanMode(plan) != "managed" {
		return "", nil
	}
	firewallID := strings.TrimSpace(plan.Firewall.ID)
	if firewallID == "" {
		firewallID = firewallStateID(existingRecord)
	}
	deviceID := firewallStateDeviceID(existingRecord)
	if firewallID == "" {
		result, err := provider.EnsureFirewall(ctx, plan.Firewall)
		if result != nil && strings.TrimSpace(result.ID) != "" {
			firewallID = result.ID
			createdPlan := plan
			createdPlan.Firewall.ID = firewallID
			createdPlan.Firewall.DeviceID = ""
			if err := upsertProvisionRecord(statePath, plan, serverStateRecordWithStatus(createdPlan, created, DNSState{}, TLSState{}, now, now, "provisioning", "linode_created")); err != nil {
				return "", err
			}
		}
		if err != nil {
			return "", Error{Msg: renderProvisionFirewallPartialFailure(plan, created, statePath, err)}
		}
		if result == nil {
			return "", Error{Msg: renderProvisionFirewallPartialFailure(plan, created, statePath, fmt.Errorf("firewall provider did not return a firewall id"))}
		}
		if firewallID == "" {
			firewallID = result.ID
		}
	}
	statePlan := plan
	statePlan.Firewall.ID = firewallID
	statePlan.Firewall.DeviceID = deviceID
	if err := upsertProvisionRecord(statePath, plan, serverStateRecordWithStatus(statePlan, created, DNSState{}, TLSState{}, now, now, "provisioning", "linode_created")); err != nil {
		return "", err
	}
	if deviceID != created.ProviderID {
		if err := provider.AssignFirewall(ctx, firewallID, created.ProviderID); err != nil {
			return "", Error{Msg: renderProvisionFirewallPartialFailure(statePlan, created, statePath, err)}
		}
		deviceID = created.ProviderID
	}
	if deviceID == created.ProviderID {
		createdPlan := plan
		createdPlan.Firewall.ID = firewallID
		createdPlan.Firewall.DeviceID = deviceID
		if err := upsertProvisionRecord(statePath, plan, serverStateRecordWithStatus(createdPlan, created, DNSState{}, TLSState{}, now, now, "provisioning", "linode_created")); err != nil {
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

func adminerPlanBlock(plan Plan) []string {
	return []string{
		"Database access",
		"  tool: " + adminerToolName + " " + adminerVersion,
		"  url: " + plan.AdminerURL,
		"  hostname: " + plan.AdminerHostname,
		"  user: " + plan.AdminerUser,
		"  password: derived from hostname + purpose db-admin",
		"  reveal: nf target show " + plan.Name,
		"  mysql grants: site env databases only",
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
	return map[string]any{
		"root": rootCredentialState(plan.Hostname),
		"db":   adminerCredentialState(plan),
	}
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

func loadLinodeProfileSSHKeys(ctx context.Context, provider ServerProvider, plan Plan) ([]SSHAuthorizedKey, error) {
	keys, err := provider.ListSSHKeys(ctx)
	if err != nil {
		return nil, err
	}
	return filterLinodeSSHKeys(keys, plan), nil
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

func resolveAuthorizedKeys(ctx context.Context, provider ServerProvider, plan Plan, actual bool) ([]SSHAuthorizedKey, error) {
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
		if provider == nil {
			return nil, Error{Msg: "Missing server provider for Linode profile SSH key lookup."}
		}
		keys, err := loadLinodeProfileSSHKeys(ctx, provider, plan)
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

func nestedMapValue(record map[string]any, keys ...string) string {
	var current any = record
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = m[key]
	}
	return valueString(current)
}

func sshAuthorizedKeysFromRecord(record map[string]any) []SSHAuthorizedKey {
	ssh, ok := record["ssh"].(map[string]any)
	if !ok {
		return nil
	}
	rawKeys, ok := ssh["authorized_keys"].([]any)
	if !ok {
		return nil
	}
	keys := make([]SSHAuthorizedKey, 0, len(rawKeys))
	for _, raw := range rawKeys {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		keys = append(keys, SSHAuthorizedKey{
			Source:      firstNonEmpty(valueString(m["source"]), "linode-profile"),
			ID:          valueString(m["id"]),
			Label:       valueString(m["label"]),
			Fingerprint: valueString(m["fingerprint"]),
			Path:        valueString(m["path"]),
			Created:     valueString(m["created"]),
		})
	}
	if len(keys) == 0 {
		return nil
	}
	return keys
}

func applyProvisionResumeDefaults(args Args, record map[string]any) (Args, []SSHAuthorizedKey) {
	if args.UbuntuVersion == "" {
		args.UbuntuVersion = firstNonEmpty(
			nestedMapValue(record, "os", "ubuntu_version"),
			nestedMapValue(record, "os", "version"),
			valueString(record["ubuntu_version"]),
		)
	}
	if args.Firewall == "" {
		args.Firewall = firstNonEmpty(nestedMapValue(record, "firewall", "mode"), "managed")
	}
	if args.FirewallID == "" {
		args.FirewallID = firewallStateID(record)
	}
	if args.Region == "" {
		args.Region = firstNonEmpty(valueString(record["region"]), nestedMapValue(record, "linode", "region"))
	}
	if args.Type == "" {
		args.Type = firstNonEmpty(valueString(record["type"]), nestedMapValue(record, "linode", "type"))
	}
	if args.Image == "" {
		args.Image = firstNonEmpty(valueString(record["image"]), nestedMapValue(record, "os", "image"))
	}
	if args.SshUser == "" {
		args.SshUser = nestedMapValue(record, "ssh", "user")
	}
	if args.SshKeySource == "" {
		args.SshKeySource = nestedMapValue(record, "ssh", "source")
	}
	if args.SshPublicKeyFile == "" {
		args.SshPublicKeyFile = firstNonEmpty(
			nestedMapValue(record, "ssh", "public_key_file"),
			nestedMapValue(record, "ssh", "path"),
		)
	}
	if args.DnsimpleAccountID == "" {
		args.DnsimpleAccountID = nestedMapValue(record, "dns", "account_id")
	}
	return args, sshAuthorizedKeysFromRecord(record)
}

func applyExistingTargetDefaults(args Args, existing CreatedServer, defaultRegion, defaultType, defaultImage, defaultUser string) Args {
	if args.Region == "" {
		args.Region = firstNonEmpty(existing.Region, defaultRegion)
	}
	if args.Type == "" {
		args.Type = firstNonEmpty(existing.Type, defaultType)
	}
	if args.Image == "" {
		args.Image = firstNonEmpty(existing.Image, defaultImage)
	}
	if args.UbuntuVersion == "" && args.Image == "" {
		args.UbuntuVersion = "24.04"
	}
	if args.SshUser == "" {
		args.SshUser = defaultUser
	}
	return args
}

func detectExistingTarget(ctx context.Context, providerName, label string) (*CreatedServer, error) {
	plan := Plan{Provider: providerName, Label: label}
	provider, err := serverProviderFactory(plan)
	if err != nil {
		return nil, err
	}
	return provider.FindServerByLabel(ctx, label)
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
	return CreatedServer{Provider: plan.Provider, ProviderID: providerID, Name: plan.Name, Hostname: plan.Hostname, IPv4: ipv4, Region: valueString(record["region"]), Type: valueString(record["type"]), Image: firstNonEmpty(valueString(record["image"]), nestedMapValue(record, "os", "image"))}, nil
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
	plan = normalizePlan(plan)
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
			"account_id":      plan.DnsimpleAccountID,
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
		"nginx":       true,
		"mariadb":     true,
		"database_ui": adminerToolName,
		"php_fpm":     plan.PHP.Service,
		"wp_cli":      "/usr/local/bin/wp",
	}
	record["db"] = map[string]any{
		"tool":         adminerToolName,
		"version":      adminerVersion,
		"hostname":     plan.AdminerHostname,
		"url":          plan.AdminerURL,
		"path":         "/var/www/shared/db/index.php",
		"config_path":  "/var/www/shared/db/adminneo-config.php",
		"download_url": adminerDownloadURL,
		"user":         plan.AdminerUser,
		"auth": map[string]any{
			"type":     "basic",
			"user":     plan.AdminerUser,
			"password": adminerCredentialState(plan),
		},
		"database": map[string]any{
			"host":   "localhost",
			"user":   plan.AdminerUser,
			"grants": "site-env-databases",
		},
	}
	if created.ProviderID != "" {
		record["id"] = created.ProviderID
		record["linode_id"] = created.ProviderID
	}
	if plan.TargetMode {
		record["target_name"] = plan.Name
		record["sites_path"] = "/var/lib/nf/sites.json"
		record["target_path"] = "/var/lib/nf/target.json"
	}
	if len(created.Tags) > 0 {
		record["tags"] = created.Tags
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
	lines = append(lines, adminerPlanBlock(plan)...)
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
	if strings.TrimSpace(plan.AdminerUser) == "" {
		plan.AdminerUser = firstNonEmpty(globalConfigValue("db_default_user"), globalConfigValue("adminer_default_user"), dbDefaultUser)
	}
	if strings.TrimSpace(plan.AdminerHostname) == "" {
		plan.AdminerHostname = deriveAdminerHostname(plan.Hostname, plan.AdminerUser)
	}
	if strings.TrimSpace(plan.AdminerURL) == "" {
		plan.AdminerURL = deriveAdminerURL(plan.Hostname, plan.AdminerUser)
	}
	if strings.TrimSpace(plan.DnsimpleAccountID) == "" {
		plan.DnsimpleAccountID = firstNonEmpty(dnsimpleAccountIDConfigValue(), "14")
	}
	if !plan.DryRun && !plan.Execute {
		plan.DryRun = true
	}
	return plan
}

func targetName(name string) string {
	return strings.TrimSpace(name)
}

func validateTargetName(name string) error {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "kinsta", "linode", "dnsimple", "digitalocean", "droplet":
		return Error{Msg: fmt.Sprintf("Target name %q is reserved. Use a more specific name such as linode1 or droplet1.", name)}
	default:
		return nil
	}
}

func BuildPlan(args Args) (Plan, error) {
	nonInteractive := args.NonInteractive
	provider := firstNonEmpty(args.Provider, "linode")
	dnsProvider := firstNonEmpty(args.DnsProvider, "dnsimple")
	name, err := resolveServerName(args.Name, nonInteractive)
	if err != nil {
		return Plan{}, err
	}
	if args.TargetMode {
		name = targetName(name)
	}
	if err := validateServerName(name); err != nil {
		return Plan{}, err
	}
	if args.TargetMode {
		if err := validateTargetName(name); err != nil {
			return Plan{}, err
		}
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
	resumeKeys := []SSHAuthorizedKey(nil)
	reuseExisting := false
	resumeRecord, _, err := findProvisionRecord(Plan{Provider: provider, Name: name, Hostname: hostname, Label: label, TargetMode: args.TargetMode})
	if err != nil {
		return Plan{}, err
	}
	defaultRegion := firstNonEmpty(globalConfigValue("linode_default_region"), "ca-central")
	defaultType := firstNonEmpty(globalConfigValue("linode_default_type"), "g6-standard-1")
	defaultImage := globalConfigValue("linode_default_image")
	defaultUser := firstNonEmpty(globalConfigValue("linode_default_user"), "nonfiction")
	defaultAdminerUser := firstNonEmpty(globalConfigValue("db_default_user"), globalConfigValue("adminer_default_user"), dbDefaultUser)
	if resumeRecord != nil {
		status := existingProvisionStatus(resumeRecord)
		phase := existingProvisionPhase(resumeRecord)
		if args.TargetMode || (status != "provisioned" && phase != "complete") {
			args, resumeKeys = applyProvisionResumeDefaults(args, resumeRecord)
		}
		if args.TargetMode && (status == "provisioned" || phase == "complete") {
			reuseExisting = true
			args = applyExistingTargetDefaults(args, CreatedServer{}, defaultRegion, defaultType, defaultImage, defaultUser)
		}
	} else if args.TargetMode && !nonInteractive {
		if existing, err := detectExistingTarget(context.Background(), provider, label); err == nil && existing != nil && createdServerHasTag(*existing, "nf") {
			reuseExisting = true
			args = applyExistingTargetDefaults(args, *existing, defaultRegion, defaultType, defaultImage, defaultUser)
		}
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
	ubuntuRelease, err := selectUbuntuStack(args.UbuntuVersion, firstNonEmpty(args.Image, defaultImage), nonInteractive)
	if err != nil {
		return Plan{}, err
	}
	region, err := resolveValue(args.Region, "Linode region: ", defaultRegion, nonInteractive, false)
	if err != nil {
		return Plan{}, err
	}
	linodeType, err := resolveValue(args.Type, "Linode type: ", defaultType, nonInteractive, false)
	if err != nil {
		return Plan{}, err
	}
	sshUser, err := resolveValue(args.SshUser, "Deployment SSH user: ", defaultUser, nonInteractive, false)
	if err != nil {
		return Plan{}, err
	}
	adminerUser, err := resolveValue(args.AdminerUser, "Database user: ", defaultAdminerUser, nonInteractive, false)
	if err != nil {
		return Plan{}, err
	}
	if err := validateAdminerUser(adminerUser); err != nil {
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
	dnsimpleAccountID := firstNonEmpty(args.DnsimpleAccountID, dnsimpleAccountIDConfigValue(), "14")
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
	osPlan, err := osReleasePlan(ubuntuRelease.version, firstNonEmpty(args.Image, defaultImage))
	if err != nil {
		return Plan{}, err
	}
	phpPlan, err := phpReleaseForUbuntu(ubuntuRelease.version)
	if err != nil {
		return Plan{}, err
	}
	plan := normalizePlan(Plan{
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
		AdminerUser:       adminerUser,
		AdminerHostname:   deriveAdminerHostname(hostname, adminerUser),
		AdminerURL:        deriveAdminerURL(hostname, adminerUser),
		Region:            firstNonEmpty(region, defaultRegion),
		LinodeType:        firstNonEmpty(linodeType, defaultType),
		Image:             osPlan.Image,
		SshUser:           firstNonEmpty(sshUser, defaultUser),
		SshKeySource:      sshKeySource,
		SshKeyLabel:       strings.TrimSpace(args.SshKeyLabel),
		SshKeyID:          strings.TrimSpace(args.SshKeyID),
		AllLinodeSshKeys:  args.AllLinodeSshKeys,
		SshPublicKeyFile:  sshPublicKeyFile,
		DnsimpleAccountID: dnsimpleAccountID,
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
		TargetMode:        args.TargetMode,
		ReuseExisting:     reuseExisting,
	})
	plan.AuthorizedKeys = resumeKeys
	return plan, nil
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
	lines = append(lines, adminerPlanBlock(plan)...)
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
	title := "Server provisioning paused."
	if plan.TargetMode {
		title = "Target provisioning handed off."
	}
	lines := []string{title, ""}
	lines = append(lines, serverPlanBlock(plan, &created, "provisioning", phase)...)
	lines = append(lines, sshSuccessBlock(plan, sshKeys)...)
	lines = append(lines, rootSuccessBlock(plan)...)
	lines = append(lines, ubuntuFirewallPlanBlock()...)
	lines = append(lines, firewallPlanBlock(plan)...)
	lines = append(lines, phpBaselinePlanBlock(plan)...)
	lines = append(lines, adminerPlanBlock(plan)...)
	lines = append(lines,
		"DNS",
		"  provider: "+dns.Provider,
		"  zone: "+dns.Zone,
		dnsRecordLine("hostname A", dns.HostnameRecord),
		dnsRecordLine("wildcard A", dns.WildcardRecord),
		"TLS",
		"  status: queued on target by nf-wildcard-tls.timer",
		"  retry: every 5 minutes until certificate succeeds",
		"Paths",
		"  state: "+statePath,
		"  marker: /etc/nf/server.json",
		"  motd: /etc/update-motd.d/99-nf",
		"  sites root: /var/www/sites",
		"  shared root: /var/www/shared",
		"  nginx site logs: /var/log/nginx/sites",
	)
	if plan.TargetMode {
		lines = append(lines,
			"Next",
			"  no rerun required; cloud-init starts TLS retry on the target.",
			"  inspect cloud-init:",
			"  ssh -o BatchMode=yes "+sshTarget+" \"cloud-init status --wait\"",
			"  inspect TLS timer:",
			"  ssh "+sshTarget+" \"sudo systemctl status nf-wildcard-tls.timer nf-wildcard-tls.service\"",
			"  check HTTPS health when DNS has settled:",
			"  curl -fsS "+plan.HealthURL+"/healthz",
		)
	} else {
		lines = append(lines,
			"Next",
			"  wait for SSH:",
			"  ssh -o BatchMode=yes "+sshTarget+" \"cloud-init status --wait\"",
			"  enable wildcard TLS:",
			"  ssh "+sshTarget+" \"sudo /usr/local/bin/nf-enable-wildcard-tls\"",
			"  check HTTPS health:",
			"  curl -fsS "+plan.HealthURL+"/healthz",
		)
	}
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
	lines = append(lines, adminerPlanBlock(plan)...)
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

func renderProvisionPartialFailure(plan Plan, created CreatedServer, statePath string, phase string, dnsErr error) string {
	plan = normalizePlan(plan)
	title := "Server provisioning paused."
	if plan.TargetMode {
		title = "Target provisioning paused."
	}
	lines := []string{title, ""}
	lines = append(lines, serverPlanBlock(plan, &created, "provisioning", phase)...)
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
	if len(plan.AuthorizedKeys) == 0 && !plan.ReuseExisting {
		provider, err := serverProviderFactory(plan)
		if err != nil {
			return Plan{}, plan.WriteCloudInit, err
		}
		sshKeys, err := resolveAuthorizedKeys(context.Background(), provider, plan, true)
		if err != nil {
			return Plan{}, plan.WriteCloudInit, err
		}
		plan.AuthorizedKeys = sshKeys
	}
	if plan.Execute && plan.Yes {
		return plan, plan.WriteCloudInit, nil
	}
	prompt := "This will create a Linode server and DNS records. Continue?"
	if plan.ReuseExisting {
		prompt = "This will reuse the existing Linode target and reconcile DNS/firewall state. Continue?"
	}
	answer, err := confirmFn(prompt, false)
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

func upsertLinodeProviderTarget(path string, candidate map[string]any) error {
	records, err := loadStatePayload(path)
	if err != nil {
		return err
	}
	var provider map[string]any
	for _, record := range records {
		if strings.EqualFold(valueString(record["provider"]), "linode") || strings.EqualFold(valueString(record["_state_key"]), "linode") {
			provider = record
			break
		}
	}
	if provider == nil {
		provider = map[string]any{"provider": "linode"}
		records = append(records, provider)
	}
	targets := targetRecordMaps(provider["targets"])
	updated := false
	for i, target := range targets {
		if recordMatches(target, candidate) {
			targets[i] = candidate
			updated = true
			break
		}
	}
	if !updated {
		targets = append(targets, candidate)
	}
	provider["targets"] = targets
	return saveStatePayload(path, records)
}

func targetRecordMaps(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if record, ok := item.(map[string]any); ok {
				out = append(out, record)
			}
		}
		return out
	default:
		return []map[string]any{}
	}
}

func upsertProvisionRecord(path string, plan Plan, candidate map[string]any) error {
	if plan.TargetMode {
		return upsertLinodeProviderTarget(path, candidate)
	}
	return upsertStateRecord(path, candidate)
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
	if effectivePlan.TargetMode {
		serverStatePath = filepath.Join(config.StateDir(), "providers.json")
	}
	existingRecord, _, err := findProvisionRecord(effectivePlan)
	if err != nil {
		return nil, err
	}
	if existingRecord != nil {
		status := existingProvisionStatus(existingRecord)
		phase := existingProvisionPhase(existingRecord)
		if (status == "provisioned" || phase == "complete") && !effectivePlan.TargetMode {
			return nil, Error{Msg: fmt.Sprintf("Server %q is already provisioned. No changes made.\nState: %s", effectivePlan.Hostname, serverStatePath)}
		}
	}
	if err := envwizard.Ensure(provisionRequirements(), effectivePlan.NonInteractive); err != nil {
		return nil, err
	}
	if err := validateActualExecution(effectivePlan); err != nil {
		return nil, err
	}
	provider, err := serverProviderFactory(effectivePlan)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	sshKeys := effectivePlan.AuthorizedKeys
	if len(sshKeys) == 0 && !effectivePlan.ReuseExisting {
		var err error
		sshKeys, err = resolveAuthorizedKeys(ctx, provider, effectivePlan, true)
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
	dbPass := passwords.DerivePassword(effectivePlan.Hostname, "db-admin", salt)
	effectivePlan.AdminerHTPasswd = adminerHtpasswdHash(dbPass)
	effectivePlan.AdminerMySQLHash = adminerMySQLPasswordHash(dbPass)
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
		linodeIP = created.IPv4
		createdAt = existingCreatedAt(existingRecord, now)
		currentPhase = existingProvisionPhase(existingRecord)
		dnsState = dnsStateFromRecord(existingRecord)
		tlsState = tlsStateFromRecord(existingRecord)
		effectivePlan.Firewall.ID = firewallStateID(existingRecord)
		effectivePlan.Firewall.DeviceID = firewallStateDeviceID(existingRecord)
	} else {
		linodeMatch, err := provider.FindServerByLabel(ctx, effectivePlan.Label)
		if err != nil {
			return nil, err
		}
		if linodeMatch != nil {
			if !effectivePlan.TargetMode || !createdServerHasTag(*linodeMatch, "nf") {
				return nil, Error{Msg: fmt.Sprintf("Server name %q is unavailable. Linode label %q already exists (id %s).", effectivePlan.Name, effectivePlan.Label, linodeMatch.ProviderID)}
			}
			created = *linodeMatch
			created.Name = effectivePlan.Name
			created.Hostname = effectivePlan.Hostname
			linodeIP = created.IPv4
			fmt.Println("Reusing Linode")
			currentPhase = "linode_created"
			partial := serverStateRecordWithStatus(effectivePlan, created, DNSState{}, TLSState{}, createdAt, now, "provisioning", currentPhase)
			if err := upsertProvisionRecord(serverStatePath, effectivePlan, partial); err != nil {
				return nil, err
			}
		} else {
			if effectivePlan.ReuseExisting {
				return nil, Error{Msg: fmt.Sprintf("Existing Linode target %q was detected before confirmation, but it was not found during execution. No changes made.", effectivePlan.Name)}
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
			if record, ok := dnsRecordByName(records, hostnameRecordName); ok {
				return nil, Error{Msg: fmt.Sprintf("Server name %q is unavailable.\nReason:\n  DNS record %s already exists and points to %s.\nNext:\n  Choose a different server name or clean up DNS first.", effectivePlan.Name, effectivePlan.Hostname, record.Content)}
			}
			wildcardRecordName := relativeRecordName(effectivePlan.WildcardHostname, dnsZone)
			if record, ok := dnsRecordByName(records, wildcardRecordName); ok {
				return nil, Error{Msg: fmt.Sprintf("Server name %q is unavailable.\nReason:\n  DNS record %s already exists and points to %s.\nNext:\n  Choose a different server name or clean up DNS first.", effectivePlan.Name, effectivePlan.WildcardHostname, record.Content)}
			}
			createdPtr, err := provider.CreateServer(ctx, ServerCreatePlan{Plan: effectivePlan, RootPass: rootPass, UserData: rendered, SSHKeys: sshKeys})
			if err != nil {
				return nil, err
			}
			created = *createdPtr
			if effectivePlan.TargetMode && len(created.Tags) == 0 {
				created.Tags = []string{"nf"}
			}
			linodeIP = created.IPv4
			fmt.Println("Creating Linode")
			currentPhase = "linode_created"
			partial := serverStateRecordWithStatus(effectivePlan, created, DNSState{}, TLSState{}, createdAt, now, "provisioning", currentPhase)
			if err := upsertProvisionRecord(serverStatePath, effectivePlan, partial); err != nil {
				return nil, err
			}
		}
	}
	phaseRank := provisioningPhaseRank(currentPhase)
	if firewallMode := firewallPlanMode(effectivePlan); firewallMode == "managed" && phaseRank < provisioningPhaseRank("firewall_configured") {
		fmt.Println("Configuring firewall")
		firewallID, err := ensureManagedFirewall(ctx, provider, effectivePlan, created, existingRecord, serverStatePath, now)
		if err != nil {
			return nil, err
		}
		effectivePlan.Firewall.ID = firewallID
		effectivePlan.Firewall.DeviceID = created.ProviderID
		phaseRank = provisioningPhaseRank("firewall_configured")
		currentPhase = "firewall_configured"
		if err := upsertProvisionRecord(serverStatePath, effectivePlan, serverStateRecordWithStatus(effectivePlan, created, DNSState{}, TLSState{}, createdAt, now, "provisioning", currentPhase)); err != nil {
			return nil, err
		}
	}
	if phaseRank < provisioningPhaseRank("dns_configured") {
		fmt.Println("Configuring DNS")
		dnsZone, err := dnsimpleZoneLookup(effectivePlan, dnsimpleToken)
		if err != nil {
			return nil, err
		}
		hostnameRecordName := relativeRecordName(effectivePlan.Hostname, dnsZone)
		wildcardRecordName := relativeRecordName(effectivePlan.WildcardHostname, dnsZone)
		if err := dnsimpleUpsertARecordRun(dnsimpleToken, effectivePlan.DnsimpleAccountID, dnsZone, hostnameRecordName, linodeIP); err != nil {
			return nil, Error{Msg: renderProvisionPartialFailure(effectivePlan, created, serverStatePath, "linode_created", err)}
		}
		if err := dnsimpleUpsertARecordRun(dnsimpleToken, effectivePlan.DnsimpleAccountID, dnsZone, wildcardRecordName, linodeIP); err != nil {
			return nil, Error{Msg: renderProvisionPartialFailure(effectivePlan, created, serverStatePath, "linode_created", err)}
		}
		records, err := dnsimpleListARecordsFn(dnsimpleToken, effectivePlan.DnsimpleAccountID, dnsZone)
		if err != nil {
			return nil, err
		}
		hostnameRecord, _ := dnsRecordByName(records, hostnameRecordName)
		wildcardRecord, _ := dnsRecordByName(records, wildcardRecordName)
		dnsState = dnsStateRecord(effectivePlan, dnsZone, hostnameRecord, wildcardRecord)
		if tlsState.Provider == "" {
			tlsState = tlsStateRecord(effectivePlan)
		}
		currentPhase = "dns_configured"
		phaseRank = provisioningPhaseRank(currentPhase)
		if err := upsertProvisionRecord(serverStatePath, effectivePlan, serverStateRecordWithStatus(effectivePlan, created, dnsState, TLSState{}, createdAt, now, "provisioning", currentPhase)); err != nil {
			return nil, err
		}
		if effectivePlan.Wait && !effectivePlan.TargetMode {
			// Keep distribution failures in the dns_configured pause/resume path.
			// This preserves the current resume behavior without re-creating the Linode.
			if err := dnsimpleWaitForRecordDistributionFn(dnsimpleToken, effectivePlan.DnsimpleAccountID, dnsZone, hostnameRecordName, effectivePlan.TLSTimeout); err != nil {
				return nil, Error{Msg: renderProvisionPartialFailure(effectivePlan, created, serverStatePath, currentPhase, err)}
			}
			if err := dnsimpleWaitForRecordDistributionFn(dnsimpleToken, effectivePlan.DnsimpleAccountID, dnsZone, wildcardRecordName, effectivePlan.TLSTimeout); err != nil {
				return nil, Error{Msg: renderProvisionPartialFailure(effectivePlan, created, serverStatePath, currentPhase, err)}
			}
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
		if err := upsertProvisionRecord(serverStatePath, effectivePlan, serverStateRecordWithStatus(effectivePlan, created, dnsState, tlsStateRecord(effectivePlan), createdAt, now, "provisioning", currentPhase)); err != nil {
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
	if err := upsertProvisionRecord(serverStatePath, effectivePlan, serverStateRecordWithStatus(effectivePlan, created, dnsState, tlsState, createdAt, now, "provisioned", "complete")); err != nil {
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

func findTargetProvisionStateRecord(plan Plan) (map[string]any, int, error) {
	providers, err := loadStatePayload(filepath.Join(config.StateDir(), "providers.json"))
	if err != nil {
		return nil, -1, err
	}
	idx := 0
	for _, provider := range providers {
		if !strings.EqualFold(valueString(provider["provider"]), "linode") && !strings.EqualFold(valueString(provider["_state_key"]), "linode") {
			continue
		}
		for _, target := range targetRecordMaps(provider["targets"]) {
			if matchesProvisionStateRecord(target, plan.Provider, "", plan.Hostname, plan.Name, plan.Label) {
				return target, idx, nil
			}
			idx++
		}
	}
	return nil, -1, nil
}

func findProvisionRecord(plan Plan) (map[string]any, int, error) {
	if plan.TargetMode {
		return findTargetProvisionStateRecord(plan)
	}
	return findProvisionStateRecord(plan)
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

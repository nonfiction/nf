package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/ui"
)

func TestMain(m *testing.M) {
	if os.Getenv("NF_CONFIG_HOME") == "" {
		dir, err := os.MkdirTemp("", "nf-provision-config-*")
		if err != nil {
			panic(err)
		}
		_ = os.Setenv("NF_CONFIG_HOME", dir)
		defer os.RemoveAll(dir)
	}
	os.Exit(m.Run())
}

func TestSlugToTitle(t *testing.T) {
	tests := map[string]string{
		"demo":             "Demo",
		"demo-site":        "Demo Site",
		"demo_site":        "Demo Site",
		"demo--site":       "Demo Site",
		"demo_site-public": "Demo Site Public",
		"already-Titled":   "Already Titled",
		"":                 "",
		"__demo__site__":   "Demo Site",
	}

	for input, want := range tests {
		if got := slugToTitle(input); got != want {
			t.Fatalf("slugToTitle(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRelativeRecordName(t *testing.T) {
	tests := map[string]string{
		relativeRecordName("app1.nonfiction.dev", "nonfiction.dev"):   "app1",
		relativeRecordName("*.app1.nonfiction.dev", "nonfiction.dev"): "*.app1",
		relativeRecordName("nonfiction.dev", "nonfiction.dev"):        "",
		relativeRecordName("example.org", "nonfiction.dev"):           "example.org",
	}

	for got, want := range tests {
		if got != want {
			t.Fatalf("relativeRecordName() = %q, want %q", got, want)
		}
	}
}

func TestShortHostname(t *testing.T) {
	tests := map[string]string{
		"app1.nonfiction.dev": "app1",
		"app1":                "app1",
		"":                    "",
	}
	for input, want := range tests {
		if got := shortHostname(input); got != want {
			t.Fatalf("shortHostname(%q) = %q, want %q", input, got, want)
		}
	}
}

func stubEmptyDNSRecords(t *testing.T) {
	t.Helper()
	oldList := dnsimpleListARecordsFn
	oldWait := dnsimpleWaitForRecordDistributionFn
	t.Cleanup(func() { dnsimpleListARecordsFn = oldList })
	t.Cleanup(func() { dnsimpleWaitForRecordDistributionFn = oldWait })
	dnsimpleListARecordsFn = func(token, accountID, zone string) ([]DNSRecord, error) {
		return []DNSRecord{}, nil
	}
	dnsimpleWaitForRecordDistributionFn = func(token, accountID, zone, name string, timeout time.Duration) error {
		return nil
	}
}

func TestAPIIDString(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "json.Number", value: json.Number("77496734"), want: "77496734"},
		{name: "float64", value: float64(77496734), want: "77496734"},
		{name: "string", value: "77496734", want: "77496734"},
		{name: "scientific string", value: "7.7496734e+07", want: "77496734"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := apiIDString(tt.value)
			if err != nil {
				t.Fatalf("apiIDString() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("apiIDString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseLinodeSSHKeysPayload(t *testing.T) {
	keys, err := parseLinodeSSHKeysPayload([]any{
		map[string]any{"id": float64(77496734), "label": "team-a", "fingerprint": "fp-a", "created": "2026-05-29", "ssh_key": "ssh-rsa AAAA"},
		map[string]any{"id": json.Number("77496735"), "label": "team-b", "fingerprint": "fp-b", "created": "2026-05-29", "ssh_key": "ssh-ed25519 BBBB"},
	})
	if err != nil {
		t.Fatalf("parseLinodeSSHKeysPayload() error = %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("parseLinodeSSHKeysPayload() len = %d, want 2", len(keys))
	}
	if got, want := keys[0].ID, "77496734"; got != want {
		t.Fatalf("key[0].ID = %q, want %q", got, want)
	}
	if got, want := keys[1].ID, "77496735"; got != want {
		t.Fatalf("key[1].ID = %q, want %q", got, want)
	}
}

func TestUbuntuReleaseMatrix(t *testing.T) {
	tests := []struct {
		ubuntu string
		image  string
		php    string
		label  string
	}{
		{"26.04", "linode/ubuntu26.04", "8.5", "Ubuntu 26.04 LTS"},
		{"24.04", "linode/ubuntu24.04", "8.3", "Ubuntu 24.04 LTS"},
		{"22.04", "linode/ubuntu22.04", "8.1", "Ubuntu 22.04 LTS"},
		{"20.04", "linode/ubuntu20.04", "7.4", "Ubuntu 20.04 LTS legacy/ESM"},
	}
	for _, tt := range tests {
		t.Run(tt.ubuntu, func(t *testing.T) {
			release, err := releaseForUbuntu(tt.ubuntu)
			if err != nil {
				t.Fatalf("releaseForUbuntu() error = %v", err)
			}
			if release.image != tt.image || release.php != tt.php || release.label != tt.label {
				t.Fatalf("releaseForUbuntu() = %#v, want image %q php %q label %q", release, tt.image, tt.php, tt.label)
			}
		})
	}
}

func TestBuildPlanDefaults(t *testing.T) {
	plan, err := BuildPlan(Args{NonInteractive: true})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if got, want := plan.Provider, "linode"; got != want {
		t.Fatalf("Provider = %q, want %q", got, want)
	}
	if got, want := plan.DnsProvider, "dnsimple"; got != want {
		t.Fatalf("DnsProvider = %q, want %q", got, want)
	}
	if got, want := plan.DnsZone, "nonfiction.dev"; got != want {
		t.Fatalf("DnsZone = %q, want %q", got, want)
	}
	if got, want := plan.Domain, "nonfiction.dev"; got != want {
		t.Fatalf("Domain = %q, want %q", got, want)
	}
	if got, want := plan.UbuntuVersion, "24.04"; got != want {
		t.Fatalf("UbuntuVersion = %q, want %q", got, want)
	}
	if got, want := plan.PHPVersion, "8.3"; got != want {
		t.Fatalf("PHPVersion = %q, want %q", got, want)
	}
	if got, want := plan.Name, "app1"; got != want {
		t.Fatalf("Name = %q, want %q", got, want)
	}
	if got, want := plan.Hostname, "app1.nonfiction.dev"; got != want {
		t.Fatalf("Hostname = %q, want %q", got, want)
	}
	if got, want := plan.Label, "app1"; got != want {
		t.Fatalf("Label = %q, want %q", got, want)
	}
	if got, want := plan.WildcardHostname, "*.app1.nonfiction.dev"; got != want {
		t.Fatalf("WildcardHostname = %q, want %q", got, want)
	}
	if got, want := plan.HealthURL, "https://app1.nonfiction.dev"; got != want {
		t.Fatalf("HealthURL = %q, want %q", got, want)
	}
	if got, want := plan.Region, "ca-central"; got != want {
		t.Fatalf("Region = %q, want %q", got, want)
	}
	if got, want := plan.LinodeType, "g6-standard-1"; got != want {
		t.Fatalf("LinodeType = %q, want %q", got, want)
	}
	if got, want := plan.Image, "linode/ubuntu24.04"; got != want {
		t.Fatalf("Image = %q, want %q", got, want)
	}
	if got, want := plan.OS.Label, "Ubuntu 24.04 LTS"; got != want {
		t.Fatalf("OS.Label = %q, want %q", got, want)
	}
	if got, want := plan.OS.Image, "linode/ubuntu24.04"; got != want {
		t.Fatalf("OS.Image = %q, want %q", got, want)
	}
	if got, want := plan.OS.PackageSource, packageSourceUbuntuNative; got != want {
		t.Fatalf("OS.PackageSource = %q, want %q", got, want)
	}
	if got, want := plan.PHP.Version, "8.3"; got != want {
		t.Fatalf("PHP.Version = %q, want %q", got, want)
	}
	if got, want := plan.PHP.Service, "php8.3-fpm"; got != want {
		t.Fatalf("PHP.Service = %q, want %q", got, want)
	}
	if got, want := plan.PHP.Socket, filepath.Clean("/run/php/php8.3-fpm.sock"); got != want {
		t.Fatalf("PHP.Socket = %q, want %q", got, want)
	}
	if !plan.Wait {
		t.Fatalf("Wait = false, want true")
	}
	planNoWait, err := BuildPlan(Args{NoWait: true, NonInteractive: true})
	if err != nil {
		t.Fatalf("BuildPlan(NoWait) error = %v", err)
	}
	if planNoWait.Wait {
		t.Fatalf("NoWait plan Wait = true, want false")
	}
	if got, want := plan.PHP.PackageSource, packageSourceUbuntuNative; got != want {
		t.Fatalf("PHP.PackageSource = %q, want %q", got, want)
	}
	for _, want := range []string{"ca-certificates", "gnupg", "lsb-release", "acl", "htop", "ncdu", "lsof", "iproute2", "dnsutils", "jq", "vim", "git", "logrotate", "mariadb-client", "unattended-upgrades", "fail2ban"} {
		if !containsString(plan.OS.Packages, want) {
			t.Fatalf("OS.Packages missing %q: %#v", want, plan.OS.Packages)
		}
	}
	for _, unwanted := range []string{"node", "npm", "nodejs", "npm-cli"} {
		if containsString(plan.OS.Packages, unwanted) || containsString(plan.PHP.Packages, unwanted) {
			t.Fatalf("package lists unexpectedly contained %q: os=%#v php=%#v", unwanted, plan.OS.Packages, plan.PHP.Packages)
		}
	}
	if got, want := plan.Firewall.Mode, "managed"; got != want {
		t.Fatalf("Firewall.Mode = %q, want %q", got, want)
	}
	if got, want := plan.Firewall.Label, firewallManagedLabel; got != want {
		t.Fatalf("Firewall.Label = %q, want %q", got, want)
	}
	if got, want := plan.Firewall.InboundPolicy, firewallInboundPolicy; got != want {
		t.Fatalf("Firewall.InboundPolicy = %q, want %q", got, want)
	}
	if got, want := plan.Firewall.OutboundPolicy, firewallOutboundPolicy; got != want {
		t.Fatalf("Firewall.OutboundPolicy = %q, want %q", got, want)
	}
	if got, want := len(plan.Firewall.Rules), 3; got != want {
		t.Fatalf("Firewall.Rules length = %d, want %d", got, want)
	}
	for _, want := range []string{"php8.3-fpm", "php8.3-cli", "php8.3-imagick", "php8.3-opcache"} {
		if !containsString(plan.PHP.Packages, want) {
			t.Fatalf("PHP.Packages missing %q: %#v", want, plan.PHP.Packages)
		}
	}
	if containsString(plan.PHP.Packages, "php8.3-xdebug") {
		t.Fatalf("PHP.Packages unexpectedly included xdebug: %#v", plan.PHP.Packages)
	}
	if got, want := plan.SshUser, "nonfiction"; got != want {
		t.Fatalf("SshUser = %q, want %q", got, want)
	}
	if got, want := plan.AdminerUser, "admin"; got != want {
		t.Fatalf("AdminerUser = %q, want %q", got, want)
	}
	if got, want := plan.AdminerHostname, "admin.app1.nonfiction.dev"; got != want {
		t.Fatalf("AdminerHostname = %q, want %q", got, want)
	}
	if got, want := plan.AdminerURL, "https://admin.app1.nonfiction.dev/"; got != want {
		t.Fatalf("AdminerURL = %q, want %q", got, want)
	}
	if got, want := plan.SshKeySource, "linode-profile"; got != want {
		t.Fatalf("SshKeySource = %q, want %q", got, want)
	}
	if got, want := plan.SshPublicKeyFile, ""; got != want {
		t.Fatalf("SshPublicKeyFile = %q, want %q", got, want)
	}
	if got, want := plan.DnsimpleAccountID, "14"; got != want {
		t.Fatalf("DnsimpleAccountID = %q, want %q", got, want)
	}
}

func TestBuildPlanInteractiveUsesSelectForUbuntuPHPStack(t *testing.T) {
	oldSelect := selectVersionFn
	oldPrompt := promptStringFn
	t.Cleanup(func() { selectVersionFn = oldSelect })
	t.Cleanup(func() { promptStringFn = oldPrompt })

	var calls []string
	selectVersionFn = func(title string, options []ui.SelectOption) (string, error) {
		calls = append(calls, title)
		switch title {
		case "Choose an Ubuntu/PHP stack":
			if len(options) != 4 || options[1].Value != "24.04" || options[1].Label != "Ubuntu 24.04 LTS / PHP 8.3 recommended/default" || !options[1].Default {
				t.Fatalf("ubuntu select options = %#v", options)
			}
			return "22.04", nil
		default:
			t.Fatalf("unexpected select title %q", title)
			return "", nil
		}
	}
	promptStringFn = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		t.Fatalf("unexpected prompt %q with default %q", prompt, defaultValue)
		return "", nil
	}

	plan, err := BuildPlan(Args{
		Provider:          "linode",
		DnsProvider:       "dnsimple",
		DnsZone:           "example.test",
		Name:              "app1",
		Hostname:          "app1.nonfiction.dev",
		Label:             "app1",
		Region:            "ca-central",
		Type:              "g6-standard-1",
		AdminerUser:       "nonfiction",
		SshUser:           "nonfiction",
		DnsimpleAccountID: "14",
	})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if got, want := plan.UbuntuVersion, "22.04"; got != want {
		t.Fatalf("UbuntuVersion = %q, want %q", got, want)
	}
	if got, want := plan.PHPVersion, "8.1"; got != want {
		t.Fatalf("PHPVersion = %q, want %q", got, want)
	}
	if got, want := plan.Image, "linode/ubuntu22.04"; got != want {
		t.Fatalf("Image = %q, want %q", got, want)
	}
	if got, want := plan.PHP.Service, "php8.1-fpm"; got != want {
		t.Fatalf("PHP.Service = %q, want %q", got, want)
	}
	if got, want := plan.SshKeySource, "linode-profile"; got != want {
		t.Fatalf("SshKeySource = %q, want %q", got, want)
	}
	if got, want := plan.SshPublicKeyFile, ""; got != want {
		t.Fatalf("SshPublicKeyFile = %q, want %q", got, want)
	}
	if len(calls) != 1 {
		t.Fatalf("select calls = %#v, want 1 call", calls)
	}
}

func TestBuildPlanInteractivePromptsForAdminerUser(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	data, err := json.Marshal(map[string]string{"adminer_default_user": "dbadmin"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "config.json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile(config.json) error = %v", err)
	}

	oldPrompt := promptStringFn
	t.Cleanup(func() { promptStringFn = oldPrompt })

	var promptTitle, promptDefault string
	promptStringFn = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		promptTitle = prompt
		promptDefault = defaultValue
		if allowBlank {
			t.Fatalf("allowBlank = true, want false")
		}
		return "site_admin", nil
	}

	plan, err := BuildPlan(Args{
		Provider:          "linode",
		DnsProvider:       "dnsimple",
		Name:              "app1",
		Region:            "ca-central",
		Type:              "g6-standard-1",
		SshUser:           "nonfiction",
		DnsimpleAccountID: "14",
		UbuntuVersion:     "24.04",
	})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if got, want := promptTitle, "Database user: "; got != want {
		t.Fatalf("prompt title = %q, want %q", got, want)
	}
	if got, want := promptDefault, "dbadmin"; got != want {
		t.Fatalf("prompt default = %q, want %q", got, want)
	}
	if got, want := plan.AdminerUser, "site_admin"; got != want {
		t.Fatalf("AdminerUser = %q, want %q", got, want)
	}
}

func TestBuildPlanAdminerUserFlagOverridesConfig(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	data, err := json.Marshal(map[string]string{"adminer_default_user": "dbadmin"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "config.json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile(config.json) error = %v", err)
	}

	plan, err := BuildPlan(Args{AdminerUser: "target_admin", NonInteractive: true})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if got, want := plan.AdminerUser, "target_admin"; got != want {
		t.Fatalf("AdminerUser = %q, want %q", got, want)
	}
	if got, want := plan.AdminerHostname, "target_admin.app1.nonfiction.dev"; got != want {
		t.Fatalf("AdminerHostname = %q, want %q", got, want)
	}
	if got, want := plan.AdminerURL, "https://target_admin.app1.nonfiction.dev/"; got != want {
		t.Fatalf("AdminerURL = %q, want %q", got, want)
	}
}

func TestBuildPlanTargetModeDetectsExistingLinodeBeforeCreationPrompts(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", filepath.Join(configHome, "state"))
	t.Setenv("NF_SERVER_DOMAIN", "nonfiction.dev")

	oldProviderFactory := serverProviderFactory
	oldRunLinodeValue := runLinodeCLIValueFn
	oldSelect := selectVersionFn
	oldPrompt := promptStringFn
	oldMultiSelect := multiSelectFn
	oldConfirm := confirmFn
	t.Cleanup(func() {
		serverProviderFactory = oldProviderFactory
		runLinodeCLIValueFn = oldRunLinodeValue
		selectVersionFn = oldSelect
		promptStringFn = oldPrompt
		multiSelectFn = oldMultiSelect
		confirmFn = oldConfirm
	})

	serverProviderFactory = func(plan Plan) (ServerProvider, error) { return legacyTestProvider{}, nil }
	runLinodeCLIValueFn = func(args []string) (any, error) {
		if len(args) >= 2 && args[0] == "linodes" && args[1] == "list" {
			return []any{map[string]any{"id": json.Number("98589908"), "label": "app4", "ipv4": []any{"172.105.101.108"}, "region": "ca-central", "type": "g6-standard-1", "image": "linode/ubuntu24.04", "tags": []any{"nf"}}}, nil
		}
		t.Fatalf("unexpected linode-cli value args: %v", args)
		return nil, nil
	}
	selectVersionFn = func(title string, options []ui.SelectOption) (string, error) {
		t.Fatalf("unexpected stack prompt for existing target: %q", title)
		return "", nil
	}
	promptStringFn = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		t.Fatalf("unexpected prompt for existing target: %q", prompt)
		return "", nil
	}
	multiSelectFn = func(title string, options []ui.SelectOption) ([]string, error) {
		t.Fatalf("unexpected SSH key prompt for existing target: %q", title)
		return nil, nil
	}
	var confirmPrompt string
	confirmFn = func(prompt string, defaultYes bool) (bool, error) {
		confirmPrompt = prompt
		return false, nil
	}

	plan, err := BuildPlan(Args{Provider: "linode", DnsProvider: "dnsimple", Name: "app4", AdminerUser: "nonfiction", TargetMode: true})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if !plan.ReuseExisting {
		t.Fatal("ReuseExisting = false, want true")
	}
	if got, want := plan.Name, "app4"; got != want {
		t.Fatalf("Name = %q, want %q", got, want)
	}
	if got, want := plan.Region, "ca-central"; got != want {
		t.Fatalf("Region = %q, want %q", got, want)
	}
	if got, want := plan.LinodeType, "g6-standard-1"; got != want {
		t.Fatalf("LinodeType = %q, want %q", got, want)
	}
	if got, want := plan.UbuntuVersion, "24.04"; got != want {
		t.Fatalf("UbuntuVersion = %q, want %q", got, want)
	}
	if got, want := sshKeySummary(plan), "unchanged on existing target"; got != want {
		t.Fatalf("sshKeySummary() = %q, want %q", got, want)
	}
	if _, _, err := preparePlan(plan); err != nil {
		t.Fatalf("preparePlan() error = %v", err)
	}
	if got, want := confirmPrompt, "This will reuse the existing Linode target and reconcile DNS/firewall state. Continue?"; got != want {
		t.Fatalf("confirm prompt = %q, want %q", got, want)
	}
}

func TestBuildPlanTargetModeRejectsReservedTargetNames(t *testing.T) {
	for _, name := range []string{"kinsta", "linode", "dnsimple", "digitalocean", "droplet"} {
		t.Run(name, func(t *testing.T) {
			_, err := BuildPlan(Args{Provider: "linode", DnsProvider: "dnsimple", Name: name, TargetMode: true, NonInteractive: true})
			if err == nil {
				t.Fatal("BuildPlan() error = nil, want reserved name error")
			}
			if !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("BuildPlan() error = %q, want reserved name error", err)
			}
		})
	}
	for _, name := range []string{"linode1", "droplet1"} {
		if err := validateTargetName(name); err != nil {
			t.Fatalf("validateTargetName(%q) error = %v, want nil", name, err)
		}
	}
}

func TestBuildPlanInteractiveFileSSHSourcePromptsForKeyPath(t *testing.T) {
	oldSelect := selectVersionFn
	oldPrompt := promptStringFn
	t.Cleanup(func() { selectVersionFn = oldSelect })
	t.Cleanup(func() { promptStringFn = oldPrompt })

	var selectCalls []string
	selectVersionFn = func(title string, options []ui.SelectOption) (string, error) {
		selectCalls = append(selectCalls, title)
		return "24.04", nil
	}
	var promptTitle, promptDefault string
	promptStringFn = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		promptTitle = prompt
		promptDefault = defaultValue
		return defaultValue, nil
	}

	plan, err := BuildPlan(Args{
		Provider:          "linode",
		DnsProvider:       "dnsimple",
		DnsZone:           "example.test",
		Name:              "app1",
		Hostname:          "app1.nonfiction.dev",
		Label:             "app1",
		Region:            "ca-central",
		Type:              "g6-standard-1",
		AdminerUser:       "nonfiction",
		SshUser:           "nonfiction",
		SshKeySource:      "file",
		DnsimpleAccountID: "14",
	})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if got, want := promptTitle, "SSH public key file: "; got != want {
		t.Fatalf("prompt title = %q, want %q", got, want)
	}
	if got, want := promptDefault, "~/.ssh/id_ed25519.pub"; got != want {
		t.Fatalf("prompt default = %q, want %q", got, want)
	}
	if got, want := plan.SshKeySource, "file"; got != want {
		t.Fatalf("SshKeySource = %q, want %q", got, want)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}
	if got, want := plan.SshPublicKeyFile, filepath.Join(home, ".ssh", "id_ed25519.pub"); got != want {
		t.Fatalf("SshPublicKeyFile = %q, want %q", got, want)
	}
	if len(selectCalls) != 1 {
		t.Fatalf("select calls = %#v, want 1 call", selectCalls)
	}
}

func TestBuildPlanInteractiveResumeUsesSavedProvisioningRecord(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", filepath.Join(configHome, "state"))
	t.Setenv("NF_SERVER_DOMAIN", "nonfiction.dev")
	record := map[string]any{
		"provider":    "linode",
		"provider_id": "98375388",
		"name":        "prod3",
		"hostname":    "prod3.nonfiction.dev",
		"label":       "prod3",
		"status":      "provisioning",
		"phase":       "linode_created",
		"ipv4":        "172.105.101.108",
		"region":      "ca-central",
		"type":        "g6-standard-1",
		"ssh": map[string]any{
			"user":   "nonfiction",
			"source": "linode-profile",
			"authorized_keys": []any{
				map[string]any{"source": "linode-profile", "id": "77496734", "label": "jon", "fingerprint": "fp-jon"},
			},
		},
		"dns":      map[string]any{"provider": "dnsimple", "account_id": "14", "zone": "nonfiction.dev"},
		"os":       map[string]any{"ubuntu_version": "24.04", "version": "24.04", "image": "linode/ubuntu24.04"},
		"firewall": map[string]any{"mode": "managed", "id": "27516345"},
	}
	if err := saveStatePayload(filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json"), []map[string]any{record}); err != nil {
		t.Fatalf("saveStatePayload() error = %v", err)
	}

	oldSelect := selectVersionFn
	oldPrompt := promptStringFn
	t.Cleanup(func() { selectVersionFn = oldSelect })
	t.Cleanup(func() { promptStringFn = oldPrompt })

	var prompts []string
	selectVersionFn = func(title string, options []ui.SelectOption) (string, error) {
		t.Fatalf("unexpected select during resume: %q", title)
		return "", nil
	}
	promptStringFn = func(prompt, defaultValue string, allowBlank bool) (string, error) {
		prompts = append(prompts, prompt)
		if prompt != "Server name: " {
			t.Fatalf("unexpected prompt during resume: %q", prompt)
		}
		return "prod3", nil
	}

	plan, err := BuildPlan(Args{Execute: true, AdminerUser: "nonfiction"})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if got, want := prompts, []string{"Server name: "}; !reflect.DeepEqual(got, want) {
		t.Fatalf("prompts = %#v, want %#v", got, want)
	}
	if got, want := plan.Name, "prod3"; got != want {
		t.Fatalf("Name = %q, want %q", got, want)
	}
	if got, want := plan.UbuntuVersion, "24.04"; got != want {
		t.Fatalf("UbuntuVersion = %q, want %q", got, want)
	}
	if got, want := plan.Region, "ca-central"; got != want {
		t.Fatalf("Region = %q, want %q", got, want)
	}
	if got, want := plan.LinodeType, "g6-standard-1"; got != want {
		t.Fatalf("LinodeType = %q, want %q", got, want)
	}
	if got, want := plan.SshUser, "nonfiction"; got != want {
		t.Fatalf("SshUser = %q, want %q", got, want)
	}
	if got, want := plan.DnsimpleAccountID, "14"; got != want {
		t.Fatalf("DnsimpleAccountID = %q, want %q", got, want)
	}
	if got, want := plan.Firewall.ID, "27516345"; got != want {
		t.Fatalf("Firewall.ID = %q, want %q", got, want)
	}
	if len(plan.AuthorizedKeys) != 1 || plan.AuthorizedKeys[0].Label != "jon" {
		t.Fatalf("AuthorizedKeys = %#v, want saved SSH key metadata", plan.AuthorizedKeys)
	}
	if !plan.Wait {
		t.Fatal("Wait = false, want true for execute resume")
	}
}

func TestPreparePlanInteractiveExecutePromptsForKeysBeforeConfirmAndReusesThem(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", filepath.Join(configHome, "state"))
	t.Setenv("NF_SERVER_DOMAIN", "example.test")
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	t.Setenv("DNSIMPLE_TOKEN", "dns-token")
	t.Setenv("LINODE_CLI_TOKEN", "linode-token")

	oldRunLinode := runLinodeCLICommand
	oldRunLinodeValue := runLinodeCLIValueFn
	oldZoneLookup := dnsimpleZoneLookup
	oldUpsert := dnsimpleUpsertARecordRun
	oldSalt := secretSaltFn
	oldNow := currentTime
	oldConfirm := confirmFn
	oldMultiSelect := multiSelectFn
	t.Cleanup(func() {
		runLinodeCLICommand = oldRunLinode
		runLinodeCLIValueFn = oldRunLinodeValue
		dnsimpleZoneLookup = oldZoneLookup
		dnsimpleUpsertARecordRun = oldUpsert
		secretSaltFn = oldSalt
		currentTime = oldNow
		confirmFn = oldConfirm
		multiSelectFn = oldMultiSelect
	})
	stubEmptyDNSRecords(t)

	var events []string
	runLinodeCLIValueFn = func(args []string) (any, error) {
		if len(args) >= 2 && args[0] == "sshkeys" && args[1] == "list" {
			return []any{
				map[string]any{"id": json.Number("77496734"), "label": "team-a", "fingerprint": "fp-a", "created": "2026-05-29", "ssh_key": "ssh-rsa AAAA team-a"},
				map[string]any{"id": json.Number("77496735"), "label": "team-b", "fingerprint": "fp-b", "created": "2026-05-29", "ssh_key": "ssh-ed25519 BBBB team-b"},
			}, nil
		}
		if len(args) >= 2 && args[0] == "linodes" && args[1] == "list" {
			return []any{}, nil
		}
		t.Fatalf("unexpected linode-cli value args: %v", args)
		return nil, nil
	}
	multiSelectFn = func(title string, options []ui.SelectOption) ([]string, error) {
		events = append(events, "multi:"+title)
		if len(options) != 2 {
			t.Fatalf("multiSelect options = %#v, want 2", options)
		}
		return []string{"77496734", "77496735"}, nil
	}
	confirmFn = func(prompt string, defaultYes bool) (bool, error) {
		events = append(events, "confirm:"+prompt)
		if defaultYes {
			t.Fatalf("confirm defaultYes = true, want false")
		}
		return true, nil
	}
	runLinodeCLICommand = func(args []string) (map[string]any, error) {
		return map[string]any{"id": "12345", "ipv4": "198.51.100.10"}, nil
	}
	dnsimpleZoneLookup = func(plan Plan, token string) (string, error) { return "nonfiction.dev", nil }
	dnsimpleUpsertARecordRun = func(token, accountID, zone, name, ip string) error { return nil }
	secretSaltFn = func() (string, error) { return "test-salt", nil }
	currentTime = func() time.Time { return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC) }

	plan, err := BuildPlan(Args{Execute: true, NoWait: true, Firewall: "none", Name: "app1", Region: "ca-central", Type: "g6-standard-1", AdminerUser: "nonfiction", SshUser: "nonfiction", DnsimpleAccountID: "14", UbuntuVersion: "24.04"})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	prepared, _, err := preparePlan(plan)
	if err != nil {
		t.Fatalf("preparePlan() error = %v", err)
	}
	if got, want := strings.Join(events, "|"), "multi:Choose Linode SSH keys|confirm:This will create a Linode server and DNS records. Continue?"; got != want {
		t.Fatalf("prompt order = %q, want %q", got, want)
	}
	if len(prepared.AuthorizedKeys) != 2 {
		t.Fatalf("prepared AuthorizedKeys = %#v, want 2 keys", prepared.AuthorizedKeys)
	}

	if _, err := ProvisionServer(prepared); err != nil {
		t.Fatalf("ProvisionServer() error = %v", err)
	}
	if got, want := strings.Join(events, "|"), "multi:Choose Linode SSH keys|confirm:This will create a Linode server and DNS records. Continue?"; got != want {
		t.Fatalf("prompt order changed after ProvisionServer = %q, want %q", got, want)
	}
}

func TestPreparePlanInteractiveDefaultPromptsForKeysBeforeConfirmAndReusesThem(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", filepath.Join(configHome, "state"))
	t.Setenv("NF_SERVER_DOMAIN", "example.test")
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	t.Setenv("DNSIMPLE_TOKEN", "dns-token")
	t.Setenv("LINODE_CLI_TOKEN", "linode-token")

	oldRunLinode := runLinodeCLICommand
	oldRunLinodeValue := runLinodeCLIValueFn
	oldZoneLookup := dnsimpleZoneLookup
	oldUpsert := dnsimpleUpsertARecordRun
	oldSalt := secretSaltFn
	oldNow := currentTime
	oldConfirm := confirmFn
	oldMultiSelect := multiSelectFn
	t.Cleanup(func() {
		runLinodeCLICommand = oldRunLinode
		runLinodeCLIValueFn = oldRunLinodeValue
		dnsimpleZoneLookup = oldZoneLookup
		dnsimpleUpsertARecordRun = oldUpsert
		secretSaltFn = oldSalt
		currentTime = oldNow
		confirmFn = oldConfirm
		multiSelectFn = oldMultiSelect
	})
	stubEmptyDNSRecords(t)

	var events []string
	runLinodeCLIValueFn = func(args []string) (any, error) {
		if len(args) >= 2 && args[0] == "sshkeys" && args[1] == "list" {
			return []any{
				map[string]any{"id": json.Number("77496734"), "label": "team-a", "fingerprint": "fp-a", "created": "2026-05-29", "ssh_key": "ssh-rsa AAAA team-a"},
				map[string]any{"id": json.Number("77496735"), "label": "team-b", "fingerprint": "fp-b", "created": "2026-05-29", "ssh_key": "ssh-ed25519 BBBB team-b"},
			}, nil
		}
		if len(args) >= 2 && args[0] == "linodes" && args[1] == "list" {
			return []any{}, nil
		}
		t.Fatalf("unexpected linode-cli value args: %v", args)
		return nil, nil
	}
	multiSelectFn = func(title string, options []ui.SelectOption) ([]string, error) {
		events = append(events, "multi:"+title)
		if len(options) != 2 {
			t.Fatalf("multiSelect options = %#v, want 2", options)
		}
		return []string{"77496734", "77496735"}, nil
	}
	confirmFn = func(prompt string, defaultYes bool) (bool, error) {
		events = append(events, "confirm:"+prompt)
		if defaultYes {
			t.Fatalf("confirm defaultYes = true, want false")
		}
		return true, nil
	}
	runLinodeCLICommand = func(args []string) (map[string]any, error) {
		return map[string]any{"id": "12345", "ipv4": "198.51.100.10"}, nil
	}
	dnsimpleZoneLookup = func(plan Plan, token string) (string, error) { return "nonfiction.dev", nil }
	dnsimpleUpsertARecordRun = func(token, accountID, zone, name, ip string) error { return nil }
	secretSaltFn = func() (string, error) { return "test-salt", nil }
	currentTime = func() time.Time { return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC) }

	plan, err := BuildPlan(Args{Execute: false, NoWait: true, Firewall: "none", Name: "app1", Region: "ca-central", Type: "g6-standard-1", AdminerUser: "nonfiction", SshUser: "nonfiction", DnsimpleAccountID: "14", UbuntuVersion: "24.04"})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	prepared, _, err := preparePlan(plan)
	if err != nil {
		t.Fatalf("preparePlan() error = %v", err)
	}
	if got, want := strings.Join(events, "|"), "multi:Choose Linode SSH keys|confirm:This will create a Linode server and DNS records. Continue?"; got != want {
		t.Fatalf("prompt order = %q, want %q", got, want)
	}
	if len(prepared.AuthorizedKeys) != 2 {
		t.Fatalf("prepared AuthorizedKeys = %#v, want 2 keys", prepared.AuthorizedKeys)
	}

	if _, err := ProvisionServer(prepared); err != nil {
		t.Fatalf("ProvisionServer() error = %v", err)
	}
	if got, want := strings.Join(events, "|"), "multi:Choose Linode SSH keys|confirm:This will create a Linode server and DNS records. Continue?"; got != want {
		t.Fatalf("prompt order changed after ProvisionServer = %q, want %q", got, want)
	}
}

func TestBuildPlanDerivesStackFromUbuntuVersion(t *testing.T) {
	tests := []struct {
		ubuntu string
		php    string
		image  string
	}{
		{"26.04", "8.5", "linode/ubuntu26.04"},
		{"24.04", "8.3", "linode/ubuntu24.04"},
		{"22.04", "8.1", "linode/ubuntu22.04"},
		{"20.04", "7.4", "linode/ubuntu20.04"},
	}
	for _, tt := range tests {
		t.Run(tt.ubuntu, func(t *testing.T) {
			plan, err := BuildPlan(Args{UbuntuVersion: tt.ubuntu, NonInteractive: true})
			if err != nil {
				t.Fatalf("BuildPlan() error = %v", err)
			}
			if got, want := plan.PHPVersion, tt.php; got != want {
				t.Fatalf("PHPVersion = %q, want %q", got, want)
			}
			if got, want := plan.Image, tt.image; got != want {
				t.Fatalf("Image = %q, want %q", got, want)
			}
			if got, want := plan.PHP.Service, "php"+tt.php+"-fpm"; got != want {
				t.Fatalf("PHP.Service = %q, want %q", got, want)
			}
			if got, want := plan.PHP.Socket, filepath.Clean("/run/php/php"+tt.php+"-fpm.sock"); got != want {
				t.Fatalf("PHP.Socket = %q, want %q", got, want)
			}
		})
	}
}

func TestBuildPlanOverrides(t *testing.T) {
	t.Setenv("NF_SERVER_DOMAIN", "env.example.test")
	plan, err := BuildPlan(Args{
		Provider:          "linode",
		DnsProvider:       "dnsimple",
		UbuntuVersion:     "22.04",
		Name:              "app2",
		Region:            "us-east",
		Type:              "g6-standard-2",
		Image:             "linode/custom-override",
		AdminerUser:       "dbadmin",
		SshUser:           "ubuntu",
		SshPublicKeyFile:  "/tmp/id.pub",
		DnsimpleAccountID: "99",
		NonInteractive:    true,
	})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if got, want := plan.UbuntuVersion, "22.04"; got != want {
		t.Fatalf("UbuntuVersion = %q, want %q", got, want)
	}
	if got, want := plan.PHPVersion, "8.1"; got != want {
		t.Fatalf("PHPVersion = %q, want %q", got, want)
	}
	if got, want := plan.Image, "linode/custom-override"; got != want {
		t.Fatalf("Image = %q, want %q", got, want)
	}
	if got, want := plan.OS.Image, "linode/custom-override"; got != want {
		t.Fatalf("OS.Image = %q, want %q", got, want)
	}
	if got, want := plan.OS.Label, "Ubuntu 22.04 LTS"; got != want {
		t.Fatalf("OS.Label = %q, want %q", got, want)
	}
	if got, want := plan.PHP.Service, "php8.1-fpm"; got != want {
		t.Fatalf("PHP.Service = %q, want %q", got, want)
	}
	if got, want := plan.PHP.Socket, filepath.Clean("/run/php/php8.1-fpm.sock"); got != want {
		t.Fatalf("PHP.Socket = %q, want %q", got, want)
	}
	if got, want := plan.Name, "app2"; got != want {
		t.Fatalf("Name = %q, want %q", got, want)
	}
	if got, want := plan.Domain, "env.example.test"; got != want {
		t.Fatalf("Domain = %q, want %q", got, want)
	}
	if got, want := plan.DnsZone, "env.example.test"; got != want {
		t.Fatalf("DnsZone = %q, want %q", got, want)
	}
	if got, want := plan.Hostname, "app2.env.example.test"; got != want {
		t.Fatalf("Hostname = %q, want %q", got, want)
	}
	if got, want := plan.Label, "app2"; got != want {
		t.Fatalf("Label = %q, want %q", got, want)
	}
	if got, want := plan.WildcardHostname, "*.app2.env.example.test"; got != want {
		t.Fatalf("WildcardHostname = %q, want %q", got, want)
	}
	if got, want := plan.HealthURL, "https://app2.env.example.test"; got != want {
		t.Fatalf("HealthURL = %q, want %q", got, want)
	}
	if got, want := plan.Region, "us-east"; got != want {
		t.Fatalf("Region = %q, want %q", got, want)
	}
	if got, want := plan.LinodeType, "g6-standard-2"; got != want {
		t.Fatalf("LinodeType = %q, want %q", got, want)
	}
	if got, want := plan.AdminerUser, "dbadmin"; got != want {
		t.Fatalf("AdminerUser = %q, want %q", got, want)
	}
	if got, want := plan.SshUser, "ubuntu"; got != want {
		t.Fatalf("SshUser = %q, want %q", got, want)
	}
	if got, want := plan.SshKeySource, "file"; got != want {
		t.Fatalf("SshKeySource = %q, want %q", got, want)
	}
	if got, want := plan.SshPublicKeyFile, filepath.Clean("/tmp/id.pub"); got != want {
		t.Fatalf("SshPublicKeyFile = %q, want %q", got, want)
	}
	if got, want := plan.DnsimpleAccountID, "99"; got != want {
		t.Fatalf("DnsimpleAccountID = %q, want %q", got, want)
	}
}

func TestBuildPlanRejectsUnsupportedUbuntuVersion(t *testing.T) {
	_, err := BuildPlan(Args{UbuntuVersion: "18.04", NonInteractive: true})
	if err == nil || !strings.Contains(err.Error(), "Unsupported Ubuntu LTS version") {
		t.Fatalf("BuildPlan() error = %v, want unsupported ubuntu version", err)
	}
}

func TestPHPReleaseForUbuntuDerivesServiceSocketAndPackages(t *testing.T) {
	release, err := phpReleaseForUbuntu("20.04")
	if err != nil {
		t.Fatalf("phpReleaseForUbuntu() error = %v", err)
	}
	if got, want := release.Version, "7.4"; got != want {
		t.Fatalf("Version = %q, want %q", got, want)
	}
	if got, want := release.Service, "php7.4-fpm"; got != want {
		t.Fatalf("Service = %q, want %q", got, want)
	}
	if got, want := release.Socket, filepath.Clean("/run/php/php7.4-fpm.sock"); got != want {
		t.Fatalf("Socket = %q, want %q", got, want)
	}
	if got, want := release.PackageSource, packageSourceUbuntuNative; got != want {
		t.Fatalf("PackageSource = %q, want %q", got, want)
	}
	if containsString(release.Packages, "php7.4-xdebug") {
		t.Fatalf("Packages unexpectedly included xdebug: %#v", release.Packages)
	}
	for _, want := range []string{"php7.4-fpm", "php7.4-cli", "php7.4-imagick"} {
		if !containsString(release.Packages, want) {
			t.Fatalf("Packages missing %q: %#v", want, release.Packages)
		}
	}
}

func TestBuildPlanInfersUbuntuStackFromImage(t *testing.T) {
	oldSelect := selectVersionFn
	t.Cleanup(func() { selectVersionFn = oldSelect })
	selectVersionFn = func(title string, options []ui.SelectOption) (string, error) {
		t.Fatalf("selectVersionFn should not run when --image maps to a known Ubuntu stack")
		return "", nil
	}

	plan, err := BuildPlan(Args{Name: "app2", Region: "ca-central", Type: "g6-standard-1", AdminerUser: "nonfiction", SshUser: "nonfiction", Image: "linode/ubuntu24.04", DnsimpleAccountID: "14"})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if got, want := plan.UbuntuVersion, "24.04"; got != want {
		t.Fatalf("UbuntuVersion = %q, want %q", got, want)
	}
	if got, want := plan.PHPVersion, "8.3"; got != want {
		t.Fatalf("PHPVersion = %q, want %q", got, want)
	}
	if got, want := plan.Image, "linode/ubuntu24.04"; got != want {
		t.Fatalf("Image = %q, want %q", got, want)
	}
}

func TestBuildPlanRejectsUnsupportedProvider(t *testing.T) {
	_, err := BuildPlan(Args{Provider: "digitalocean", NonInteractive: true})
	if err == nil || !strings.Contains(err.Error(), "Unsupported provider") {
		t.Fatalf("BuildPlan() error = %v, want unsupported provider", err)
	}
}

func TestBuildPlanRejectsUnsupportedDNSProvider(t *testing.T) {
	_, err := BuildPlan(Args{DnsProvider: "cloudflare", NonInteractive: true})
	if err == nil || !strings.Contains(err.Error(), "Unsupported DNS provider") {
		t.Fatalf("BuildPlan() error = %v, want unsupported DNS provider", err)
	}
}

func TestCloudInitTemplateIsServerOnly(t *testing.T) {
	osPlan, err := osReleasePlan("24.04", "")
	if err != nil {
		t.Fatalf("osReleasePlan() error = %v", err)
	}
	phpPlan, err := phpReleaseForUbuntu("24.04")
	if err != nil {
		t.Fatalf("phpReleaseForUbuntu() error = %v", err)
	}
	plan := Plan{Name: "app1", SshUser: "ubuntu", Hostname: "app1.nonfiction.dev", DnsimpleAccountID: "14", OS: osPlan, PHP: phpPlan}
	rendered, err := renderCloudInit(plan, false, "", nil)
	if err != nil {
		t.Fatalf("renderCloudInit() error = %v", err)
	}
	for _, want := range []string{
		"hostname: app1",
		"fqdn: app1.nonfiction.dev",
		"preserve_hostname: false",
		"manage_etc_hosts: true",
		"timezone: UTC",
		"swap:",
		"lock_passwd: true",
		"PasswordAuthentication no",
		"PermitRootLogin prohibit-password",
		"/etc/php/8.3/fpm/conf.d/99-nf-wordpress.ini",
		"/etc/nginx/snippets/nf-fastcgi-php.conf",
		"/etc/nginx/snippets/nf-wordpress.conf",
		"/etc/nginx/snippets/nf-static-assets.conf",
		"/etc/nginx/snippets/nf-security-headers.conf",
		"/etc/nginx/snippets/nf-wildcard-cert.conf",
		"/etc/nginx/conf.d/nf-server-names-hash.conf",
		"server_names_hash_bucket_size 128;",
		"server_names_hash_max_size 4096;",
		"/etc/nginx/sites-available/nf-server",
		"server_name app1.nonfiction.dev;",
		"location = /healthz",
		"default_type application/json;",
		"{\"server\":\"app1\",\"hostname\":\"app1.nonfiction.dev\",\"status\":\"ready\"}",
		"/usr/local/bin/nf-write-server-health-page",
		"/var/www/nf/index.html",
		"cat >/var/www/nf/index.html <<'EOF'",
		"<!doctype html>",
		"</html>",
		"EOF",
		"<link rel=\"icon\" href=\"/favicon.svg\">",
		"<title>nf target app1</title>",
		"radial-gradient(at 20% 20%",
		".logo{display:block;width:5.5rem;margin:0 auto 1rem",
		"h1{font-size:3rem;margin:.5rem 0}",
		"<img class=\"logo\" src=\"/favicon.svg\" alt=\"Nonfiction logo\">",
		"/var/www/nf/favicon.svg",
		"cat >/var/www/nf/favicon.svg <<'EOF'",
		"<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 400 400\">",
		"linearGradient id=\"a\"",
		"<stop stop-color=\"#905dfa\"/>",
		"<stop offset=\"1\" stop-color=\"#5501d2\"/>",
		"<path fill=\"url(#a)\" d=\"M0 0h400v400H0z\"/>",
		"M231 131c5.648 4.848 10.764 10.22 14 17v2h2v-26h21",
		"font-family:system-ui,sans-serif",
		"<main>",
		"<div class=\"pill\">READY</div>",
		"<h1>app1</h1>",
		"<p>https://app1.nonfiction.dev/healthz</p>",
		"/etc/letsencrypt/renewal-hooks/deploy/reload-nginx",
		"/usr/local/bin/nf-enable-wildcard-tls",
		"/etc/systemd/system/nf-wildcard-tls.service",
		"/etc/systemd/system/nf-wildcard-tls.timer",
		"ExecStart=/usr/local/bin/nf-enable-wildcard-tls",
		"OnUnitActiveSec=5min",
		"systemctl enable --now nf-wildcard-tls.timer",
		"/usr/local/bin/nf-write-server-marker",
		"/var/lib/nf/target.json",
		"/var/lib/nf/sites.json",
		"printf '[]\\n' >/var/lib/nf/sites.json",
		"chown -R ubuntu:www-data /var/lib/nf",
		"/etc/update-motd.d/99-nf",
		"systemctl enable --now mariadb",
		"systemctl enable --now fail2ban",
		"ufw allow 22/tcp",
		"ufw allow 80/tcp",
		"ufw allow 443/tcp",
		"ufw --force enable",
		"opcache.enable = 1",
		"opcache.memory_consumption = 128",
		"opcache.max_accelerated_files = 10000",
		"upload_max_filesize = 1024M",
		"post_max_size = 1024M",
		"php8.3-fpm",
		"php8.3-cli",
		"imagemagick",
		"ghostscript",
		"mariadb-server",
		"python3-certbot-dns-dnsimple",
		"unattended-upgrades",
		"fail2ban",
		"install -d -o ubuntu -g www-data -m 2775 /var/www /var/www/nf /var/www/sites /var/www/shared /var/log/nginx/sites",
		"install -d -o www-data -g www-data -m 2775 /var/cache/nginx/nf/sites",
		"hostnamectl set-hostname app1.nonfiction.dev",
		"mkdir -p /etc/nf",
		"Managed by nf",
		"Sites root: /var/www/sites",
		"certbot certonly --non-interactive --agree-tos --dns-dnsimple --dns-dnsimple-credentials /root/.secrets/certbot/dnsimple.ini -m web@nonfiction.ca -d app1.nonfiction.dev -d \"*.app1.nonfiction.dev\"",
		"listen 80 default_server;",
		"listen [::]:80 default_server;",
		"listen 443 ssl http2 default_server;",
		"listen [::]:443 ssl http2 default_server;",
		"include /etc/nginx/snippets/nf-wildcard-cert.conf;",
		"/usr/local/bin/nf-install-db-ui",
		"https://www.adminneo.org/files/5.4.1/mysql_en_default/adminneo-5.4.1.php",
		"/var/www/shared/db/index.php",
		"/var/lib/nf/db.htpasswd",
		"server_name admin.app1.nonfiction.dev;",
		"client_max_body_size 1024M;",
		"auth_basic \"nf database\";",
		"CREATE USER IF NOT EXISTS 'admin'@'localhost' IDENTIFIED BY PASSWORD '<adminer mysql password hash>';",
		`"purpose":"db-admin"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("renderCloudInit() output missing %q:\n%s", want, rendered)
		}
	}
	lines := strings.Split(strings.ReplaceAll(rendered, "\r\n", "\n"), "\n")
	openerIdx, closerIdx := -1, -1
	for i, line := range lines {
		if line == "      cat >/var/www/nf/index.html <<'EOF'" {
			openerIdx = i
			for j := i + 1; j < len(lines); j++ {
				if lines[j] == "      EOF" {
					closerIdx = j
					break
				}
			}
			break
		}
	}
	if openerIdx == -1 || closerIdx == -1 || openerIdx+1 >= len(lines) || closerIdx+1 >= len(lines) || lines[closerIdx+1] != "      cat >/var/www/nf/favicon.svg <<'EOF'" {
		t.Fatalf("renderCloudInit() output missing normalized heredoc delimiters:\n%s", rendered)
	}
	if openerIdx+1 >= len(lines) || lines[openerIdx+1] != "      <!doctype html>" {
		t.Fatalf("renderCloudInit() output did not keep index heredoc indentation:\n%s", rendered)
	}
	foundCompactedLogo := false
	for i := openerIdx + 1; i < closerIdx; i++ {
		if strings.Contains(lines[i], `<img class="logo" src="/favicon.svg" alt="Nonfiction logo">`) {
			foundCompactedLogo = true
			if !strings.HasPrefix(lines[i], "      ") {
				t.Fatalf("renderCloudInit() compacted logo line lost runcmd indentation: %q", lines[i])
			}
		}
	}
	if !foundCompactedLogo {
		t.Fatalf("renderCloudInit() output missing compacted logo line:\n%s", rendered)
	}
	faviconCloserIdx := -1
	for i := closerIdx + 2; i < len(lines); i++ {
		if lines[i] == "      EOF" {
			faviconCloserIdx = i
			break
		}
	}
	if faviconCloserIdx == -1 || faviconCloserIdx+1 >= len(lines) || lines[faviconCloserIdx+1] != "  - path: /etc/letsencrypt/renewal-hooks/deploy/reload-nginx" {
		t.Fatalf("renderCloudInit() output missing favicon heredoc delimiters:\n%s", rendered)
	}
	if got, limit := len([]byte(rendered)), 15_000; got > limit {
		t.Fatalf("renderCloudInit() size = %d bytes, want <= %d to stay under Linode user_data limit", got, limit)
	}
	for _, unwanted := range []string{
		"wordpress.org/latest.zip",
		"wp core install",
		"CREATE DATABASE",
		"wp config create",
		"xdebug",
		"client.app1.nonfiction.dev",
		"server_name *.app1.nonfiction.dev",
		"GRANT ALL PRIVILEGES",
		"composer install",
		"npm install",
		"npx",
		"/var/www/nf-server",
		"color-scheme: light",
		"role=\"img\"",
		"aria-label=\"Nonfiction logo\"",
		"linearGradient id=\"background\"",
		"stop-color=\"#905DFA\"",
		"stop-color=\"#5501D2\"",
		"M112 88h48l80 128V88h48v224h-48l-80-128v128h-48z",
	} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("renderCloudInit() output unexpectedly contained %q:\n%s", unwanted, rendered)
		}
	}
	if allow22, enable := strings.Index(rendered, "ufw allow 22/tcp"), strings.Index(rendered, "ufw --force enable"); allow22 == -1 || enable == -1 || allow22 > enable {
		t.Fatalf("ufw allow/enable order incorrect:\n%s", rendered)
	}
}

func TestRenderCloudInitCopiesLinodeRootSSHKeysToSSHUser(t *testing.T) {
	osPlan, err := osReleasePlan("24.04", "linode/ubuntu24.04")
	if err != nil {
		t.Fatalf("osReleasePlan() error = %v", err)
	}
	phpPlan, err := phpReleaseForUbuntu("24.04")
	if err != nil {
		t.Fatalf("phpReleaseForUbuntu() error = %v", err)
	}
	plan := Plan{SshUser: "ubuntu", Hostname: "app1.nonfiction.dev", DnsimpleAccountID: "14", OS: osPlan, PHP: phpPlan}
	rendered, err := renderCloudInit(plan, true, "dns-token", []string{"ssh-rsa AAAA team-a", "ssh-ed25519 BBBB team-b"})
	if err != nil {
		t.Fatalf("renderCloudInit() error = %v", err)
	}
	for _, want := range []string{"/root/.ssh/authorized_keys", "/home/ubuntu/.ssh/authorized_keys", "chown ubuntu:ubuntu /home/ubuntu/.ssh/authorized_keys", "chmod 0600 /home/ubuntu/.ssh/authorized_keys"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("renderCloudInit() output missing %q:\n%s", want, rendered)
		}
	}
	for _, unwanted := range []string{"ssh_authorized_keys:", "ssh-rsa AAAA team-a", "ssh-ed25519 BBBB team-b"} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("renderCloudInit() output unexpectedly included %q in user_data:\n%s", unwanted, rendered)
		}
	}
}

func TestRenderCloudInitRedactsDnsimpleTokenInPreview(t *testing.T) {
	osPlan, err := osReleasePlan("24.04", "linode/ubuntu24.04")
	if err != nil {
		t.Fatalf("osReleasePlan() error = %v", err)
	}
	phpPlan, err := phpReleaseForUbuntu("24.04")
	if err != nil {
		t.Fatalf("phpReleaseForUbuntu() error = %v", err)
	}
	plan := Plan{SshUser: "ubuntu", Hostname: "app1.nonfiction.dev", DnsimpleAccountID: "14", OS: osPlan, PHP: phpPlan}
	rendered, err := renderCloudInit(plan, false, "dns-token-secret", nil)
	if err != nil {
		t.Fatalf("renderCloudInit() error = %v", err)
	}
	if !strings.Contains(rendered, "<dnsimple token>") {
		t.Fatalf("renderCloudInit() preview did not redact token:\n%s", rendered)
	}
	if strings.Contains(rendered, "dns-token-secret") {
		t.Fatalf("renderCloudInit() preview leaked token:\n%s", rendered)
	}
}

func TestAppendLinodeAuthorizedKeyArgsUsesRepeatedSingleLineValues(t *testing.T) {
	args := appendLinodeAuthorizedKeyArgs([]string{"linodes", "create"}, []SSHAuthorizedKey{
		{PublicKey: "ssh-rsa AAAA team-a"},
		{PublicKey: "   "},
		{PublicKey: "ssh-ed25519 BBBB team-b"},
	})

	var values []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--authorized_keys" {
			if i+1 >= len(args) {
				t.Fatalf("authorized key flag missing value: %v", args)
			}
			values = append(values, args[i+1])
		}
	}

	if len(values) != 2 {
		t.Fatalf("authorized key values = %#v, want 2 entries", values)
	}
	for _, value := range values {
		if strings.Contains(value, "\n") {
			t.Fatalf("authorized key value unexpectedly contained newline: %q", value)
		}
	}
	for i, want := range []string{"ssh-rsa AAAA team-a", "ssh-ed25519 BBBB team-b"} {
		if values[i] != want {
			t.Fatalf("authorized key value[%d] = %q, want %q", i, values[i], want)
		}
	}
}

func TestResolveAuthorizedKeysFileFallback(t *testing.T) {
	file := filepath.Join(t.TempDir(), "id.pub")
	if err := os.WriteFile(file, []byte("ssh-ed25519 AAAA-file team@example\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	keys, err := resolveAuthorizedKeys(context.Background(), nil, Plan{SshKeySource: "file", SshPublicKeyFile: file}, true)
	if err != nil {
		t.Fatalf("resolveAuthorizedKeys() error = %v", err)
	}
	if len(keys) != 1 || keys[0].Path != file || keys[0].PublicKey != "ssh-ed25519 AAAA-file team@example" {
		t.Fatalf("resolveAuthorizedKeys() = %#v, want file fallback", keys)
	}
}

func TestServerStateRecordShapeDoesNotContainSecrets(t *testing.T) {
	osPlan, err := osReleasePlan("24.04", "linode/ubuntu24.04")
	if err != nil {
		t.Fatalf("osReleasePlan() error = %v", err)
	}
	phpPlan, err := phpReleaseForUbuntu("24.04")
	if err != nil {
		t.Fatalf("phpReleaseForUbuntu() error = %v", err)
	}
	plan := Plan{Provider: "linode", DnsProvider: "dnsimple", Name: "app1", Hostname: "app1.nonfiction.dev", Label: "app1", Domain: "nonfiction.dev", WildcardHostname: "*.app1.nonfiction.dev", HealthURL: "https://app1.nonfiction.dev", Region: "us-east", LinodeType: "g6-standard-1", Image: osPlan.Image, SshUser: "ubuntu", DnsimpleAccountID: "14", OS: osPlan, PHP: phpPlan, Firewall: managedFirewallPlan("fw-123")}
	plan.AuthorizedKeys = []SSHAuthorizedKey{{Source: "linode-profile", ID: "77496734", Label: "team-a", Fingerprint: "fp-a", PublicKey: "ssh-rsa AAAAtest"}, {Source: "file", Path: "/tmp/id.pub", Label: "id.pub", PublicKey: "ssh-ed25519 BBBBtest"}}
	plan.Firewall.DeviceID = "12345"
	created := CreatedServer{Provider: "linode", ProviderID: "12345", Name: plan.Name, Hostname: plan.Hostname, IPv4: "198.51.100.10"}
	dns := dnsStateRecord(plan, "nonfiction.dev", DNSRecord{ID: "77496734", Name: "app1", Type: "A", Content: created.IPv4, TTL: 60}, DNSRecord{ID: "77496735", Name: "*.app1", Type: "A", Content: created.IPv4, TTL: 60})
	tls := tlsStateRecord(plan)
	record := serverStateRecord(plan, created, dns, tls, "2026-05-29T12:00:00Z")

	for _, legacy := range []string{"project", "remote_path", "database", "wp_admin_user", "wp_admin_email", "site", "dns_zone"} {
		if _, ok := record[legacy]; ok {
			t.Fatalf("server record unexpectedly contains %q: %#v", legacy, record[legacy])
		}
	}
	if got, want := record["provider_id"], "12345"; got != want {
		t.Fatalf("provider_id = %#v, want %q", got, want)
	}
	if linode, ok := record["linode"].(map[string]any); !ok || linode["instance_id"] != "12345" {
		t.Fatalf("linode block = %#v, want instance_id 12345", record["linode"])
	}
	if dnsBlock, ok := record["dns"].(map[string]any); !ok || dnsBlock["provider"] != "dnsimple" || dnsBlock["zone"] != "nonfiction.dev" {
		t.Fatalf("dns block = %#v, want dnsimple provider", record["dns"])
	} else if hostname, ok := dnsBlock["hostname_record"].(DNSRecord); !ok || hostname.ID != "77496734" || hostname.Name != "app1" {
		t.Fatalf("dns hostname record = %#v, want decimal id", dnsBlock["hostname_record"])
	} else if wildcard, ok := dnsBlock["wildcard_record"].(DNSRecord); !ok || wildcard.ID != "77496735" || wildcard.Name != "*.app1" {
		t.Fatalf("dns wildcard record = %#v, want decimal id", dnsBlock["wildcard_record"])
	}
	if got, want := record["domain"], "nonfiction.dev"; got != want {
		t.Fatalf("domain = %#v, want %q", got, want)
	}
	if got, want := record["wildcard_hostname"], "*.app1.nonfiction.dev"; got != want {
		t.Fatalf("wildcard_hostname = %#v, want %q", got, want)
	}
	if got, want := record["health_url"], "https://app1.nonfiction.dev"; got != want {
		t.Fatalf("health_url = %#v, want %q", got, want)
	}
	if tlsBlock, ok := record["tls"].(map[string]any); !ok || tlsBlock["provider"] != "certbot-dnsimple" {
		t.Fatalf("tls block = %#v, want certbot-dnsimple provider", record["tls"])
	}
	if osBlock, ok := record["os"].(map[string]any); !ok || osBlock["ubuntu_version"] != "24.04" || osBlock["image"] != "linode/ubuntu24.04" || osBlock["package_source"] != packageSourceUbuntuNative || osBlock["label"] != "24.04 LTS" || osBlock["family"] != "ubuntu" {
		t.Fatalf("os block = %#v, want ubuntu metadata", record["os"])
	}
	if osBlock, ok := record["os"].(map[string]any); !ok || osBlock["family"] != "ubuntu" || osBlock["version"] != "24.04" {
		t.Fatalf("os block missing family/version = %#v", record["os"])
	}
	if phpBlock, ok := record["php"].(map[string]any); !ok || phpBlock["version"] != "8.3" || phpBlock["service"] != "php8.3-fpm" || phpBlock["socket"] != filepath.Clean("/run/php/php8.3-fpm.sock") || phpBlock["package_source"] != packageSourceUbuntuNative {
		t.Fatalf("php block = %#v, want php metadata", record["php"])
	}
	if services, ok := record["services"].(map[string]any); !ok || services["nginx"] != true || services["mariadb"] != true || services["database_ui"] != adminerToolName || services["php_fpm"] != "php8.3-fpm" || services["wp_cli"] != "/usr/local/bin/wp" {
		t.Fatalf("services block = %#v, want nginx/mariadb/php_fpm/wp_cli", record["services"])
	}
	if credentials, ok := record["credentials"].(map[string]any); !ok {
		t.Fatalf("credentials block missing: %#v", record["credentials"])
	} else if root, ok := credentials["root"].(map[string]any); !ok || root["derived"] != true || root["identity"] != "app1.nonfiction.dev" || root["purpose"] != "linode-root" || root["stored"] != false {
		t.Fatalf("credentials.root = %#v, want derived metadata", credentials["root"])
	} else if _, ok := root["password"]; ok {
		t.Fatalf("credentials.root unexpectedly stored a password: %#v", root)
	} else if db, ok := credentials["db"].(map[string]any); !ok || db["derived"] != true || db["identity"] != "app1.nonfiction.dev" || db["purpose"] != "db-admin" || db["stored"] != false || db["user"] != "admin" {
		t.Fatalf("credentials.db = %#v, want derived metadata", credentials["db"])
	} else if _, ok := db["password"]; ok {
		t.Fatalf("credentials.db unexpectedly stored a password: %#v", db)
	}
	if db, ok := record["db"].(map[string]any); !ok || db["tool"] != adminerToolName || db["version"] != adminerVersion || db["hostname"] != "admin.app1.nonfiction.dev" || db["url"] != "https://admin.app1.nonfiction.dev/" || db["user"] != "admin" {
		t.Fatalf("db block = %#v, want AdminNeo metadata", record["db"])
	} else if password, ok := db["auth"].(map[string]any)["password"].(map[string]any); !ok || password["purpose"] != "db-admin" || password["stored"] != false {
		t.Fatalf("db auth password = %#v, want derived metadata", db["auth"])
	}
	if firewall, ok := record["firewall"].(map[string]any); !ok || firewall["mode"] != "managed" || firewall["id"] != "fw-123" {
		t.Fatalf("firewall block = %#v, want managed firewall metadata", record["firewall"])
	} else if device, ok := firewall["device"].(map[string]any); !ok || valueString(device["id"]) != "12345" {
		t.Fatalf("firewall block = %#v, want managed firewall metadata", record["firewall"])
	}
}

func TestRenderPlanShowsDnsZoneAndRecords(t *testing.T) {
	osPlan, err := osReleasePlan("24.04", "linode/ubuntu24.04")
	if err != nil {
		t.Fatalf("osReleasePlan() error = %v", err)
	}
	phpPlan, err := phpReleaseForUbuntu("24.04")
	if err != nil {
		t.Fatalf("phpReleaseForUbuntu() error = %v", err)
	}
	plan := Plan{Provider: "linode", DnsProvider: "dnsimple", Name: "app1", Hostname: "app1.nonfiction.dev", Label: "app1", Domain: "nonfiction.dev", WildcardHostname: "*.app1.nonfiction.dev", HealthURL: "https://app1.nonfiction.dev", Region: "ca-central", LinodeType: "g6-standard-1", Image: osPlan.Image, SshUser: "nonfiction", SshKeySource: "file", SshPublicKeyFile: "/tmp/id.pub", OS: osPlan, PHP: phpPlan, Firewall: managedFirewallPlan("")}
	output := renderPlan(plan, "/tmp/cloud-init.yaml", "")
	for _, want := range []string{"Server provision dry-run plan", "Server", "  provider: linode", "  name: app1", "  hostname: app1.nonfiction.dev", "  label: app1", "  wildcard hostname: *.app1.nonfiction.dev", "  health url: https://app1.nonfiction.dev", "Config", "  server domain: nonfiction.dev", "  dns provider: dnsimple", "  dnsimple account id: 14", "Availability", "  local state: not checked (dry-run)", "  linode label: not checked (dry-run)", "  dns records: not checked (dry-run)", "Access", "  ssh user: nonfiction", "  auth: SSH keys only", "  sudo: passwordless", "  key source: file", "  authorized keys: /tmp/id.pub", "  root password: derived from hostname + purpose linode-root", "  root stored in state: no", "  root reveal: nf server root-password app1", "Ubuntu firewall", "  ufw default: deny incoming", "  ufw outbound: allow", "  allow: 22/tcp, 80/tcp, 443/tcp", "Linode firewall", "  provider: linode", "  mode: managed", "  managed label: nf-web", "  inbound: 22/tcp, 80/tcp, 443/tcp", "  inbound policy: DROP", "  outbound policy: ACCEPT", "PHP baseline", "  timezone: UTC", "  swap: 2G", "  stack: Ubuntu 24.04 LTS / PHP 8.3", "  ubuntu: 24.04 LTS", "  image: linode/ubuntu24.04", "  php version: 8.3", "  php service: php8.3-fpm", "  php socket: /run/php/php8.3-fpm.sock", "  package source: ubuntu-native", "  base packages: nginx, mariadb-server", "  packages: php8.3-fpm, php8.3-cli", "DNS", "  provider: dnsimple", "  zone: nonfiction.dev", "  hostname A: app1 -> <created after server IP is known>", "  wildcard A: *.app1 -> <created after server IP is known>", "TLS", "  provider: certbot-dnsimple", "  domains: app1.nonfiction.dev, *.app1.nonfiction.dev", "  certificate: /etc/letsencrypt/live/app1.nonfiction.dev/fullchain.pem", "  key: /etc/letsencrypt/live/app1.nonfiction.dev/privkey.pem", "Paths", "  cloud-init preview: /tmp/cloud-init.yaml", "  marker: /etc/nf/server.json", "  motd: /etc/update-motd.d/99-nf", "  sites root: /var/www/sites", "  shared root: /var/www/shared", "  nginx site logs: /var/log/nginx/sites", "Mode", "  dry-run: true"} {
		if !strings.Contains(output, want) {
			t.Fatalf("renderPlan() output missing %q:\n%s", want, output)
		}
	}
}

func TestRenderPlanDefaultSSHSourceShowsLinodeProfile(t *testing.T) {
	plan, err := BuildPlan(Args{NonInteractive: true})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	output := renderPlan(plan, "/tmp/cloud-init.yaml", "")
	for _, want := range []string{"Access", "  key source: linode-profile", "  authorized keys: all Linode profile keys", "  root reveal: nf server root-password app1"} {
		if !strings.Contains(output, want) {
			t.Fatalf("renderPlan() output missing %q:\n%s", want, output)
		}
	}
	for _, unwanted := range []string{"id_ed25519.pub", "SSH public key file:"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("renderPlan() output unexpectedly contained %q:\n%s", unwanted, output)
		}
	}
}

func TestRenderProvisionSuccessShowsNextSteps(t *testing.T) {
	osPlan, err := osReleasePlan("24.04", "linode/ubuntu24.04")
	if err != nil {
		t.Fatalf("osReleasePlan() error = %v", err)
	}
	phpPlan, err := phpReleaseForUbuntu("24.04")
	if err != nil {
		t.Fatalf("phpReleaseForUbuntu() error = %v", err)
	}
	plan := Plan{Provider: "linode", DnsProvider: "dnsimple", Name: "app1", Hostname: "app1.nonfiction.dev", Label: "app1", Domain: "nonfiction.dev", WildcardHostname: "*.app1.nonfiction.dev", HealthURL: "https://app1.nonfiction.dev", SshUser: "nonfiction", SshPublicKeyFile: "/tmp/id.pub", OS: osPlan, PHP: phpPlan, Firewall: managedFirewallPlan("")}
	created := CreatedServer{ProviderID: "12345", IPv4: "198.51.100.10"}
	dns := DNSState{Provider: "dnsimple", Zone: "nonfiction.dev", HostnameRecord: DNSRecord{Name: "app1", Type: "A", Content: "198.51.100.10"}, WildcardRecord: DNSRecord{Name: "*.app1", Type: "A", Content: "198.51.100.10"}}
	tls := TLSState{Provider: "certbot-dnsimple", Domains: []string{"app1.nonfiction.dev", "*.app1.nonfiction.dev"}, Certificate: "/etc/letsencrypt/live/app1.nonfiction.dev/fullchain.pem", Key: "/etc/letsencrypt/live/app1.nonfiction.dev/privkey.pem"}
	sshKeys := []SSHAuthorizedKey{{Source: "linode-profile", ID: "1", Label: "team-a"}, {Source: "linode-profile", ID: "2", Label: "team-b"}}
	plan.AuthorizedKeys = sshKeys
	plan.Firewall.ID = "fw-123"
	plan.Firewall.DeviceID = created.ProviderID
	output := renderProvisionSuccess(plan, created, dns, tls, "/tmp/state/servers.json", "/tmp/cloud-init.yaml", sshKeys)
	for _, want := range []string{"Server provisioned.", "Server", "  provider: linode", "  name: app1", "  hostname: app1.nonfiction.dev", "  label: app1", "  wildcard hostname: *.app1.nonfiction.dev", "  health url: https://app1.nonfiction.dev", "  ipv4: 198.51.100.10", "  linode instance id: 12345", "  health check: https://app1.nonfiction.dev/healthz", "  status: ready", "Access", "  ssh user: nonfiction", "  auth: SSH keys only", "  sudo: passwordless", "  root password: derived from hostname + purpose linode-root", "  root stored in state: no", "  root reveal: nf server root-password app1", "Ubuntu firewall", "  ufw default: deny incoming", "  ufw outbound: allow", "  allow: 22/tcp, 80/tcp, 443/tcp", "Linode firewall", "  provider: linode", "  mode: managed", "  managed label: nf-web", "  firewall id: fw-123", "PHP baseline", "  timezone: UTC", "  swap: 2G", "DNS", "  provider: dnsimple", "  zone: nonfiction.dev", "TLS", "  provider: certbot-dnsimple", "  domains: app1.nonfiction.dev, *.app1.nonfiction.dev", "Paths", "  state: /tmp/state/servers.json", "  cloud-init: /tmp/cloud-init.yaml", "  marker: /etc/nf/server.json", "  sites root: /var/www/sites", "  shared root: /var/www/shared", "  nginx site logs: /var/log/nginx/sites", "stack: Ubuntu 24.04 LTS / PHP 8.3", "php service: php8.3-fpm", "php socket: /run/php/php8.3-fpm.sock", "authorized keys: team-a, team-b", "hostname A: app1 -> 198.51.100.10", "wildcard A: *.app1 -> 198.51.100.10"} {
		if !strings.Contains(output, want) {
			t.Fatalf("renderProvisionSuccess() output missing %q:\n%s", want, output)
		}
	}
}

func TestProvisionServerWritesOnlyServersJSON(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", filepath.Join(configHome, "state"))
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	t.Setenv("DNSIMPLE_TOKEN", "dns-token")
	t.Setenv("LINODE_CLI_TOKEN", "linode-token")

	oldRunLinode := runLinodeCLICommand
	oldRunLinodeValue := runLinodeCLIValueFn
	oldZoneLookup := dnsimpleZoneLookup
	oldUpsert := dnsimpleUpsertARecordRun
	oldWaitForTCP := waitForTCPPortFn
	oldRunSSH := runSSHCommandFn
	oldHealthCheck := checkHTTPSHealthFn
	oldSalt := secretSaltFn
	oldNow := currentTime
	t.Cleanup(func() {
		runLinodeCLICommand = oldRunLinode
		runLinodeCLIValueFn = oldRunLinodeValue
		dnsimpleZoneLookup = oldZoneLookup
		dnsimpleUpsertARecordRun = oldUpsert
		waitForTCPPortFn = oldWaitForTCP
		runSSHCommandFn = oldRunSSH
		checkHTTPSHealthFn = oldHealthCheck
		secretSaltFn = oldSalt
		currentTime = oldNow
	})
	stubEmptyDNSRecords(t)

	var recordedNames []string
	var createArgs []string
	runLinodeCLIValueFn = func(args []string) (any, error) {
		if len(args) >= 2 && args[0] == "sshkeys" && args[1] == "list" {
			return []any{
				map[string]any{"id": json.Number("77496734"), "label": "team-a", "fingerprint": "fp-a", "created": "2026-05-29", "ssh_key": "ssh-rsa AAAA team-a"},
				map[string]any{"id": json.Number("77496735"), "label": "team-b", "fingerprint": "fp-b", "created": "2026-05-29", "ssh_key": "ssh-ed25519 BBBB team-b"},
			}, nil
		}
		if len(args) >= 2 && args[0] == "linodes" && args[1] == "list" {
			return []any{}, nil
		}
		t.Fatalf("unexpected linode-cli value args: %v", args)
		return nil, nil
	}
	runLinodeCLICommand = func(args []string) (map[string]any, error) {
		createArgs = append([]string(nil), args...)
		if !containsArg(args, "--root_pass") {
			t.Fatalf("linode create args missing --root_pass: %v", args)
		}
		if !containsArg(args, "--metadata.user_data") {
			t.Fatalf("linode create args missing --metadata.user_data: %v", args)
		}
		if !containsArg(args, "--image") || !containsArg(args, "linode/ubuntu24.04") {
			t.Fatalf("linode create args missing default ubuntu image: %v", args)
		}
		return map[string]any{"id": "12345", "ipv4": "198.51.100.10"}, nil
	}
	dnsimpleZoneLookup = func(plan Plan, token string) (string, error) {
		if plan.Hostname != "app1.nonfiction.dev" {
			t.Fatalf("unexpected hostname passed to zone lookup: %s", plan.Hostname)
		}
		return "nonfiction.dev", nil
	}
	dnsimpleUpsertARecordRun = func(token, accountID, zone, name, ip string) error {
		recordedNames = append(recordedNames, name)
		return nil
	}
	waitForTCPPortFn = func(host string, port int, timeout time.Duration) error { return nil }
	runSSHCommandFn = func(user, host, command string, timeout time.Duration) error { return nil }
	checkHTTPSHealthFn = func(url, name, hostname string, timeout time.Duration) error {
		data, err := os.ReadFile(filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json"))
		if err != nil {
			t.Fatalf("ReadFile(servers.json) during health check error = %v", err)
		}
		var records []map[string]any
		if err := json.Unmarshal(data, &records); err != nil {
			t.Fatalf("Unmarshal(servers.json) during health check error = %v", err)
		}
		if got, want := valueString(records[0]["phase"]), "tls_configured"; got != want {
			t.Fatalf("phase before health check = %q, want %q", got, want)
		}
		if got, want := valueString(records[0]["status"]), "provisioning"; got != want {
			t.Fatalf("status before health check = %q, want %q", got, want)
		}
		return nil
	}
	secretSaltFn = func() (string, error) { return "test-salt", nil }
	currentTime = func() time.Time { return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC) }

	plan, err := BuildPlan(Args{NonInteractive: true, Execute: true, Yes: true, Firewall: "none", Name: "app1", Hostname: "app1.nonfiction.dev"})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	var result *ServerCreateResult
	var provisionErr error
	output := captureStdout(t, func() {
		result, provisionErr = ProvisionServer(plan)
	})
	if provisionErr != nil {
		t.Fatalf("ProvisionServer() error = %v", provisionErr)
	}
	for _, want := range []string{"Creating Linode", "Configuring DNS", "Waiting for SSH", "Waiting for cloud-init and enabling wildcard TLS", "Checking server health"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	if result == nil || result.StatePath == "" {
		t.Fatalf("ProvisionServer() result = %#v, want state path", result)
	}
	serverStatePath := filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json")
	data, err := os.ReadFile(serverStatePath)
	if err != nil {
		t.Fatalf("ReadFile(servers.json) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("NF_STATE_HOME"), "sites.json")); !os.IsNotExist(err) {
		t.Fatalf("sites.json unexpectedly exists: %v", err)
	}
	var records []map[string]any
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("Unmarshal(servers.json) error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("servers.json record count = %d, want 1", len(records))
	}
	record := records[0]
	for _, unwanted := range []string{"remote_path", "database", "wp_admin_user", "wp_admin_email"} {
		if _, ok := record[unwanted]; ok {
			t.Fatalf("server record unexpectedly contained %q: %#v", unwanted, record[unwanted])
		}
	}
	if osBlock, ok := record["os"].(map[string]any); !ok || osBlock["ubuntu_version"] != "24.04" || osBlock["image"] != "linode/ubuntu24.04" || osBlock["label"] != "24.04 LTS" || osBlock["family"] != "ubuntu" {
		t.Fatalf("os block = %#v, want ubuntu metadata", record["os"])
	}
	if phpBlock, ok := record["php"].(map[string]any); !ok || phpBlock["version"] != "8.3" || phpBlock["service"] != "php8.3-fpm" || phpBlock["socket"] != filepath.Clean("/run/php/php8.3-fpm.sock") {
		t.Fatalf("php block = %#v, want php metadata", record["php"])
	}
	if got, want := record["provider_id"], "12345"; got != want {
		t.Fatalf("provider_id = %#v, want %q", got, want)
	}
	if ssh, ok := record["ssh"].(map[string]any); !ok || ssh["user"] != "nonfiction" || ssh["host"] != "app1.nonfiction.dev" || valueString(ssh["port"]) != "22" || ssh["source"] != "linode-profile" {
		t.Fatalf("ssh block = %#v, want metadata", record["ssh"])
	} else {
		keys, ok := ssh["authorized_keys"].([]any)
		if !ok || len(keys) != 2 {
			t.Fatalf("ssh authorized_keys = %#v, want 2 metadata entries", ssh["authorized_keys"])
		}
		for _, key := range keys {
			m, ok := key.(map[string]any)
			if !ok {
				t.Fatalf("ssh authorized key = %#v, want map", key)
			}
			if _, ok := m["ssh_key"]; ok {
				t.Fatalf("ssh authorized key unexpectedly stored body: %#v", m)
			}
		}
	}
	if got, want := record["hostname"], "app1.nonfiction.dev"; got != want {
		t.Fatalf("hostname = %#v, want %q", got, want)
	}
	rootPass := passwords.DerivePassword("app1.nonfiction.dev", "linode-root", "test-salt")
	for i := 0; i < len(createArgs); i++ {
		if createArgs[i] == "--root_pass" {
			if i+1 >= len(createArgs) {
				t.Fatalf("linode create args missing value for --root_pass: %v", createArgs)
			}
			if got := createArgs[i+1]; got != rootPass {
				t.Fatalf("root password arg = %q, want derived hostname password", got)
			}
		}
	}
	var authorizedValues []string
	for i := 0; i < len(createArgs); i++ {
		if createArgs[i] == "--authorized_keys" {
			if i+1 >= len(createArgs) {
				t.Fatalf("linode create args missing value for --authorized_keys: %v", createArgs)
			}
			authorizedValues = append(authorizedValues, createArgs[i+1])
		}
	}
	if len(authorizedValues) != 2 {
		t.Fatalf("authorized key args = %#v, want 2 single-key entries", authorizedValues)
	}
	for _, value := range authorizedValues {
		if strings.Contains(value, "\n") {
			t.Fatalf("authorized key arg unexpectedly contained newline: %q", value)
		}
	}
	for i, want := range []string{"ssh-rsa AAAA team-a", "ssh-ed25519 BBBB team-b"} {
		if authorizedValues[i] != want {
			t.Fatalf("authorized key arg[%d] = %q, want %q", i, authorizedValues[i], want)
		}
	}
	if len(recordedNames) != 2 || recordedNames[0] != "app1" || recordedNames[1] != "*.app1" {
		t.Fatalf("DNS record names = %#v, want [app1 *.app1]", recordedNames)
	}
	data, err = os.ReadFile(serverStatePath)
	if err != nil {
		t.Fatalf("ReadFile(servers.json) error = %v", err)
	}
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("Unmarshal(servers.json) error = %v", err)
	}
	if got, want := valueString(records[0]["status"]), "provisioned"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	if got, want := valueString(records[0]["phase"]), "complete"; got != want {
		t.Fatalf("phase = %q, want %q", got, want)
	}
}

func TestProvisionServerManagedFirewallCreatesRulesAndAttachesDevice(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", filepath.Join(configHome, "state"))
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	t.Setenv("DNSIMPLE_TOKEN", "dns-token")
	t.Setenv("LINODE_CLI_TOKEN", "linode-token")

	oldRunLinode := runLinodeCLICommand
	oldRunLinodeValue := runLinodeCLIValueFn
	oldZoneLookup := dnsimpleZoneLookup
	oldUpsert := dnsimpleUpsertARecordRun
	oldWaitForTCP := waitForTCPPortFn
	oldRunSSH := runSSHCommandFn
	oldHealthCheck := checkHTTPSHealthFn
	oldSalt := secretSaltFn
	oldNow := currentTime
	t.Cleanup(func() {
		runLinodeCLICommand = oldRunLinode
		runLinodeCLIValueFn = oldRunLinodeValue
		dnsimpleZoneLookup = oldZoneLookup
		dnsimpleUpsertARecordRun = oldUpsert
		waitForTCPPortFn = oldWaitForTCP
		runSSHCommandFn = oldRunSSH
		checkHTTPSHealthFn = oldHealthCheck
		secretSaltFn = oldSalt
		currentTime = oldNow
	})
	stubEmptyDNSRecords(t)

	var calls [][]string
	runLinodeCLIValueFn = func(args []string) (any, error) {
		if len(args) >= 2 && args[0] == "sshkeys" && args[1] == "list" {
			return []any{map[string]any{"id": json.Number("77496734"), "label": "team-a", "fingerprint": "fp-a", "created": "2026-05-29", "ssh_key": "ssh-rsa AAAA team-a"}}, nil
		}
		if len(args) >= 2 && args[0] == "linodes" && args[1] == "list" {
			return []any{}, nil
		}
		if len(args) >= 2 && args[0] == "firewalls" && args[1] == "list" {
			return []any{}, nil
		}
		t.Fatalf("unexpected linode-cli value args: %v", args)
		return nil, nil
	}
	runLinodeCLICommand = func(args []string) (map[string]any, error) {
		calls = append(calls, append([]string(nil), args...))
		switch {
		case len(args) >= 2 && args[0] == "linodes" && args[1] == "create":
			return map[string]any{"id": "12345", "ipv4": "198.51.100.10"}, nil
		case len(args) >= 2 && args[0] == "firewalls" && args[1] == "create":
			if !containsArg(args, "--label") || !containsArg(args, firewallManagedLabel) {
				t.Fatalf("firewall create args missing label: %v", args)
			}
			if !containsArg(args, "--rules.inbound_policy") || !containsArg(args, firewallInboundPolicy) || !containsArg(args, "--rules.outbound_policy") || !containsArg(args, firewallOutboundPolicy) {
				t.Fatalf("firewall create args missing policies: %v", args)
			}
			return map[string]any{"id": json.Number("987")}, nil
		case len(args) >= 2 && args[0] == "firewalls" && args[1] == "rules-update":
			if len(args) < 5 || args[2] != "987" {
				t.Fatalf("firewall rules-update args = %v, want firewall id 987", args)
			}
			if !containsArg(args, "--inbound_policy") || !containsArg(args, firewallInboundPolicy) {
				t.Fatalf("firewall rules-update args missing inbound policy: %v", args)
			}
			if !containsArg(args, "--inbound") {
				t.Fatalf("firewall rules-update args missing inbound JSON: %v", args)
			}
			for _, want := range []string{"allow-ssh", "allow-http", "allow-https", "22", "80", "443"} {
				if !containsArg(args, want) && !strings.Contains(strings.Join(args, " "), want) {
					t.Fatalf("firewall rules-update args missing %q: %v", want, args)
				}
			}
			return map[string]any{}, nil
		case len(args) >= 2 && args[0] == "firewalls" && args[1] == "device-create":
			if !containsArg(args, "987") || !containsArg(args, "12345") {
				t.Fatalf("firewall device-create args = %v, want firewall and linode ids", args)
			}
			return map[string]any{}, nil
		default:
			t.Fatalf("unexpected linode-cli command args: %v", args)
			return nil, nil
		}
	}
	dnsimpleZoneLookup = func(plan Plan, token string) (string, error) {
		if plan.Hostname != "app1.nonfiction.dev" {
			t.Fatalf("unexpected hostname passed to zone lookup: %s", plan.Hostname)
		}
		return "nonfiction.dev", nil
	}
	dnsimpleUpsertARecordRun = func(token, accountID, zone, name, ip string) error { return nil }
	secretSaltFn = func() (string, error) { return "test-salt", nil }
	currentTime = func() time.Time { return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC) }

	plan, err := BuildPlan(Args{NonInteractive: true, Execute: true, Yes: true, NoWait: true, Name: "app1", Hostname: "app1.nonfiction.dev"})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	result, err := ProvisionServer(plan)
	if err != nil {
		t.Fatalf("ProvisionServer() error = %v", err)
	}
	if result == nil || result.StatePath == "" {
		t.Fatalf("ProvisionServer() result = %#v, want state path", result)
	}
	if got, want := len(calls), 4; got != want {
		t.Fatalf("linode-cli call count = %d, want %d (%v)", got, want, calls)
	}
	for i, want := range []string{"linodes create", "firewalls create", "firewalls rules-update", "firewalls device-create"} {
		if got := strings.Join(calls[i][:2], " "); got != want {
			t.Fatalf("linode-cli call[%d] = %q, want %q", i, got, want)
		}
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json"))
	if err != nil {
		t.Fatalf("ReadFile(servers.json) error = %v", err)
	}
	var records []map[string]any
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("Unmarshal(servers.json) error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("servers.json record count = %d, want 1", len(records))
	}
	if firewall, ok := records[0]["firewall"].(map[string]any); !ok || valueString(firewall["id"]) != "987" {
		t.Fatalf("firewall block = %#v, want managed firewall metadata", records[0]["firewall"])
	} else if device, ok := firewall["device"].(map[string]any); !ok || valueString(device["id"]) != "12345" {
		t.Fatalf("firewall block = %#v, want managed firewall metadata", records[0]["firewall"])
	}
}

func TestProvisionServerManagedFirewallReusesExistingFirewallByLabel(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", filepath.Join(configHome, "state"))
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	t.Setenv("DNSIMPLE_TOKEN", "dns-token")
	t.Setenv("LINODE_CLI_TOKEN", "linode-token")

	oldRunLinode := runLinodeCLICommand
	oldRunLinodeValue := runLinodeCLIValueFn
	oldZoneLookup := dnsimpleZoneLookup
	oldList := dnsimpleListARecordsFn
	oldUpsert := dnsimpleUpsertARecordRun
	oldWait := dnsimpleWaitForRecordDistributionFn
	oldSalt := secretSaltFn
	oldNow := currentTime
	t.Cleanup(func() {
		runLinodeCLICommand = oldRunLinode
		runLinodeCLIValueFn = oldRunLinodeValue
		dnsimpleZoneLookup = oldZoneLookup
		dnsimpleListARecordsFn = oldList
		dnsimpleUpsertARecordRun = oldUpsert
		dnsimpleWaitForRecordDistributionFn = oldWait
		secretSaltFn = oldSalt
		currentTime = oldNow
	})
	stubEmptyDNSRecords(t)

	var calls [][]string
	runLinodeCLIValueFn = func(args []string) (any, error) {
		if len(args) >= 2 && args[0] == "sshkeys" && args[1] == "list" {
			return []any{map[string]any{"id": json.Number("77496734"), "label": "team-a", "fingerprint": "fp-a", "created": "2026-05-29", "ssh_key": "ssh-rsa AAAA team-a"}}, nil
		}
		if len(args) >= 2 && args[0] == "linodes" && args[1] == "list" {
			return []any{}, nil
		}
		if len(args) >= 2 && args[0] == "firewalls" && args[1] == "list" {
			return []any{map[string]any{"id": json.Number("987"), "label": firewallManagedLabel}}, nil
		}
		t.Fatalf("unexpected linode-cli value args: %v", args)
		return nil, nil
	}
	runLinodeCLICommand = func(args []string) (map[string]any, error) {
		calls = append(calls, append([]string(nil), args...))
		switch {
		case len(args) >= 2 && args[0] == "linodes" && args[1] == "create":
			return map[string]any{"id": "12345", "ipv4": "198.51.100.10"}, nil
		case len(args) >= 2 && args[0] == "firewalls" && args[1] == "create":
			t.Fatalf("firewall create should not run when matching firewall exists: %v", args)
			return nil, nil
		case len(args) >= 2 && args[0] == "firewalls" && args[1] == "rules-update":
			return map[string]any{}, nil
		case len(args) >= 2 && args[0] == "firewalls" && args[1] == "device-create":
			if !containsArg(args, "987") || !containsArg(args, "12345") {
				t.Fatalf("firewall device-create args = %v, want firewall and linode ids", args)
			}
			return map[string]any{}, nil
		default:
			t.Fatalf("unexpected linode-cli command args: %v", args)
			return nil, nil
		}
	}
	dnsimpleZoneLookup = func(plan Plan, token string) (string, error) { return "nonfiction.dev", nil }
	dnsimpleUpsertARecordRun = func(token, accountID, zone, name, ip string) error { return nil }
	secretSaltFn = func() (string, error) { return "test-salt", nil }
	currentTime = func() time.Time { return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC) }

	plan, err := BuildPlan(Args{NonInteractive: true, Execute: true, Yes: true, NoWait: true, Name: "app1", Hostname: "app1.nonfiction.dev"})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	result, err := ProvisionServer(plan)
	if err != nil {
		t.Fatalf("ProvisionServer() error = %v", err)
	}
	if result == nil || result.StatePath == "" {
		t.Fatalf("ProvisionServer() result = %#v, want state path", result)
	}
	if got, want := len(calls), 3; got != want {
		t.Fatalf("linode-cli call count = %d, want %d (%v)", got, want, calls)
	}
	for i, want := range []string{"linodes create", "firewalls rules-update", "firewalls device-create"} {
		if got := strings.Join(calls[i][:2], " "); got != want {
			t.Fatalf("linode-cli call[%d] = %q, want %q", i, got, want)
		}
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json"))
	if err != nil {
		t.Fatalf("ReadFile(servers.json) error = %v", err)
	}
	var records []map[string]any
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("Unmarshal(servers.json) error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("servers.json record count = %d, want 1", len(records))
	}
	if firewall, ok := records[0]["firewall"].(map[string]any); !ok || valueString(firewall["id"]) != "987" {
		t.Fatalf("firewall block = %#v, want reused firewall metadata", records[0]["firewall"])
	}
}

func TestProvisionServerFirewallFailureKeepsPartialStateAndExplainsRecovery(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", filepath.Join(configHome, "state"))
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	t.Setenv("DNSIMPLE_TOKEN", "dns-token")
	t.Setenv("LINODE_CLI_TOKEN", "linode-token")

	oldRunLinode := runLinodeCLICommand
	oldRunLinodeValue := runLinodeCLIValueFn
	oldZoneLookup := dnsimpleZoneLookup
	oldList := dnsimpleListARecordsFn
	oldUpsert := dnsimpleUpsertARecordRun
	oldWait := dnsimpleWaitForRecordDistributionFn
	oldSalt := secretSaltFn
	oldNow := currentTime
	t.Cleanup(func() {
		runLinodeCLICommand = oldRunLinode
		runLinodeCLIValueFn = oldRunLinodeValue
		dnsimpleZoneLookup = oldZoneLookup
		dnsimpleListARecordsFn = oldList
		dnsimpleUpsertARecordRun = oldUpsert
		dnsimpleWaitForRecordDistributionFn = oldWait
		secretSaltFn = oldSalt
		currentTime = oldNow
	})
	stubEmptyDNSRecords(t)

	runLinodeCLIValueFn = func(args []string) (any, error) {
		if len(args) >= 2 && args[0] == "sshkeys" && args[1] == "list" {
			return []any{map[string]any{"id": json.Number("77496734"), "label": "team-a", "fingerprint": "fp-a", "created": "2026-05-29", "ssh_key": "ssh-rsa AAAA team-a"}}, nil
		}
		if len(args) >= 2 && args[0] == "linodes" && args[1] == "list" {
			return []any{}, nil
		}
		if len(args) >= 2 && args[0] == "firewalls" && args[1] == "list" {
			return []any{}, nil
		}
		t.Fatalf("unexpected linode-cli value args: %v", args)
		return nil, nil
	}
	runLinodeCLICommand = func(args []string) (map[string]any, error) {
		switch {
		case len(args) >= 2 && args[0] == "linodes" && args[1] == "create":
			return map[string]any{"id": "12345", "ipv4": "198.51.100.10"}, nil
		case len(args) >= 2 && args[0] == "firewalls" && args[1] == "create":
			return map[string]any{"id": json.Number("987")}, nil
		case len(args) >= 2 && args[0] == "firewalls" && args[1] == "rules-update":
			return nil, fmt.Errorf("simulated firewall failure")
		default:
			t.Fatalf("unexpected linode-cli command args: %v", args)
			return nil, nil
		}
	}
	dnsimpleZoneLookup = func(plan Plan, token string) (string, error) { return "nonfiction.dev", nil }
	dnsimpleUpsertARecordRun = func(token, accountID, zone, name, ip string) error {
		t.Fatal("dns upsert should not run after firewall failure")
		return nil
	}
	secretSaltFn = func() (string, error) { return "test-salt", nil }
	currentTime = func() time.Time { return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC) }

	plan, err := BuildPlan(Args{NonInteractive: true, Execute: true, Yes: true, NoWait: true, Name: "app1", Hostname: "app1.nonfiction.dev"})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	_, err = ProvisionServer(plan)
	if err == nil || !strings.Contains(err.Error(), "Server provisioning paused.") || !strings.Contains(err.Error(), "Firewall error") || !strings.Contains(err.Error(), "rerun the same provision command") {
		t.Fatalf("ProvisionServer() error = %v, want firewall recovery message", err)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json"))
	if err != nil {
		t.Fatalf("ReadFile(servers.json) error = %v", err)
	}
	var records []map[string]any
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("Unmarshal(servers.json) error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("servers.json record count = %d, want 1", len(records))
	}
	if got, want := valueString(records[0]["phase"]), "linode_created"; got != want {
		t.Fatalf("phase = %q, want %q", got, want)
	}
	if firewall, ok := records[0]["firewall"].(map[string]any); !ok || valueString(firewall["id"]) != "987" {
		t.Fatalf("firewall block = %#v, want partial firewall metadata", records[0]["firewall"])
	}
}

func TestProvisionServerWritesPartialStateOnDNSFailure(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", filepath.Join(configHome, "state"))
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	t.Setenv("DNSIMPLE_TOKEN", "dns-token")
	t.Setenv("LINODE_CLI_TOKEN", "linode-token")

	oldRunLinode := runLinodeCLICommand
	oldRunLinodeValue := runLinodeCLIValueFn
	oldZoneLookup := dnsimpleZoneLookup
	oldUpsert := dnsimpleUpsertARecordRun
	oldSalt := secretSaltFn
	oldNow := currentTime
	t.Cleanup(func() {
		runLinodeCLICommand = oldRunLinode
		runLinodeCLIValueFn = oldRunLinodeValue
		dnsimpleZoneLookup = oldZoneLookup
		dnsimpleUpsertARecordRun = oldUpsert
		secretSaltFn = oldSalt
		currentTime = oldNow
	})
	stubEmptyDNSRecords(t)

	runLinodeCLIValueFn = func(args []string) (any, error) {
		if len(args) >= 2 && args[0] == "sshkeys" && args[1] == "list" {
			return []any{map[string]any{"id": json.Number("77496734"), "label": "team-a", "fingerprint": "fp-a", "created": "2026-05-29", "ssh_key": "ssh-rsa AAAA team-a"}}, nil
		}
		if len(args) >= 2 && args[0] == "linodes" && args[1] == "list" {
			return []any{}, nil
		}
		t.Fatalf("unexpected linode-cli value args: %v", args)
		return nil, nil
	}
	runLinodeCLICommand = func(args []string) (map[string]any, error) {
		return map[string]any{"id": float64(12345), "ipv4": "198.51.100.10"}, nil
	}
	dnsimpleZoneLookup = func(plan Plan, token string) (string, error) { return "nonfiction.dev", nil }
	callCount := 0
	dnsimpleUpsertARecordRun = func(token, accountID, zone, name, ip string) error {
		callCount++
		return fmt.Errorf("simulated dns failure")
	}
	secretSaltFn = func() (string, error) { return "test-salt", nil }
	currentTime = func() time.Time { return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC) }

	plan, err := BuildPlan(Args{NonInteractive: true, Execute: true, Yes: true, NoWait: true, Firewall: "none", Name: "app1", Hostname: "app1.nonfiction.dev"})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	_, err = ProvisionServer(plan)
	if err == nil || !strings.Contains(err.Error(), "Server provisioning paused.") || !strings.Contains(err.Error(), "DNS error") {
		t.Fatalf("ProvisionServer() error = %v, want partial failure message", err)
	}
	if callCount != 1 {
		t.Fatalf("dnsimpleUpsertARecordRun called %d times, want 1", callCount)
	}
	serverStatePath := filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json")
	data, err := os.ReadFile(serverStatePath)
	if err != nil {
		t.Fatalf("ReadFile(servers.json) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("NF_STATE_HOME"), "sites.json")); !os.IsNotExist(err) {
		t.Fatalf("sites.json unexpectedly exists: %v", err)
	}
	var records []map[string]any
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("Unmarshal(servers.json) error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("servers.json record count = %d, want 1", len(records))
	}
	record := records[0]
	if got, want := record["status"], "provisioning"; got != want {
		t.Fatalf("status = %#v, want %q", got, want)
	}
	if got, want := record["phase"], "linode_created"; got != want {
		t.Fatalf("phase = %#v, want %q", got, want)
	}
	if got, want := record["provider_id"], "12345"; got != want {
		t.Fatalf("provider_id = %#v, want %q", got, want)
	}
	if _, ok := record["dns"]; ok {
		t.Fatalf("dns block unexpectedly present in partial record: %#v", record["dns"])
	}
	if ssh, ok := record["ssh"].(map[string]any); !ok || ssh["source"] != "linode-profile" {
		t.Fatalf("ssh block = %#v, want profile metadata", record["ssh"])
	}
}

func TestProvisionServerHealthFailureExplainsRecovery(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", filepath.Join(configHome, "state"))
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	t.Setenv("DNSIMPLE_TOKEN", "dns-token")
	t.Setenv("LINODE_CLI_TOKEN", "linode-token")

	oldRunLinode := runLinodeCLICommand
	oldRunLinodeValue := runLinodeCLIValueFn
	oldZoneLookup := dnsimpleZoneLookup
	oldUpsert := dnsimpleUpsertARecordRun
	oldWaitForTCP := waitForTCPPortFn
	oldRunSSH := runSSHCommandFn
	oldHealthCheck := checkHTTPSHealthFn
	oldSalt := secretSaltFn
	oldNow := currentTime
	t.Cleanup(func() {
		runLinodeCLICommand = oldRunLinode
		runLinodeCLIValueFn = oldRunLinodeValue
		dnsimpleZoneLookup = oldZoneLookup
		dnsimpleUpsertARecordRun = oldUpsert
		waitForTCPPortFn = oldWaitForTCP
		runSSHCommandFn = oldRunSSH
		checkHTTPSHealthFn = oldHealthCheck
		secretSaltFn = oldSalt
		currentTime = oldNow
	})
	stubEmptyDNSRecords(t)

	runLinodeCLIValueFn = func(args []string) (any, error) {
		if len(args) >= 2 && args[0] == "sshkeys" && args[1] == "list" {
			return []any{map[string]any{"id": json.Number("77496734"), "label": "team-a", "fingerprint": "fp-a", "created": "2026-05-29", "ssh_key": "ssh-rsa AAAA team-a"}}, nil
		}
		if len(args) >= 2 && args[0] == "linodes" && args[1] == "list" {
			return []any{}, nil
		}
		if len(args) >= 2 && args[0] == "firewalls" && args[1] == "list" {
			return []any{}, nil
		}
		t.Fatalf("unexpected linode-cli value args: %v", args)
		return nil, nil
	}
	runLinodeCLICommand = func(args []string) (map[string]any, error) {
		switch {
		case len(args) >= 2 && args[0] == "linodes" && args[1] == "create":
			return map[string]any{"id": "12345", "ipv4": "198.51.100.10"}, nil
		case len(args) >= 2 && args[0] == "firewalls" && args[1] == "create":
			return map[string]any{}, nil
		case len(args) >= 2 && args[0] == "firewalls" && args[1] == "rules-update":
			return map[string]any{}, nil
		case len(args) >= 2 && args[0] == "firewalls" && args[1] == "device-create":
			return map[string]any{}, nil
		default:
			t.Fatalf("unexpected linode-cli command args: %v", args)
			return nil, nil
		}
	}
	dnsimpleZoneLookup = func(plan Plan, token string) (string, error) { return "nonfiction.dev", nil }
	dnsimpleListARecordsFn = func(token, accountID, zone string) ([]DNSRecord, error) { return []DNSRecord{}, nil }
	dnsimpleUpsertARecordRun = func(token, accountID, zone, name, ip string) error { return nil }
	dnsimpleWaitForRecordDistributionFn = func(token, accountID, zone, name string, timeout time.Duration) error { return nil }
	waitForTCPPortFn = func(host string, port int, timeout time.Duration) error { return nil }
	runSSHCommandFn = func(user, host, command string, timeout time.Duration) error { return nil }
	checkHTTPSHealthFn = func(url, name, hostname string, timeout time.Duration) error {
		data, err := os.ReadFile(filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json"))
		if err != nil {
			t.Fatalf("ReadFile(servers.json) during health check error = %v", err)
		}
		var records []map[string]any
		if err := json.Unmarshal(data, &records); err != nil {
			t.Fatalf("Unmarshal(servers.json) during health check error = %v", err)
		}
		if got, want := valueString(records[0]["phase"]), "tls_configured"; got != want {
			t.Fatalf("phase before health failure = %q, want %q", got, want)
		}
		return fmt.Errorf("unexpected health body")
	}
	secretSaltFn = func() (string, error) { return "test-salt", nil }
	currentTime = func() time.Time { return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC) }

	plan, err := BuildPlan(Args{NonInteractive: true, Execute: true, Yes: true, Firewall: "none", Name: "app1", Hostname: "app1.nonfiction.dev"})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	_, err = ProvisionServer(plan)
	if err == nil || !strings.Contains(err.Error(), "Health error") || !strings.Contains(err.Error(), "ssh nonfiction@app1.nonfiction.dev \"sudo nginx -t && sudo systemctl status nginx\"") || !strings.Contains(err.Error(), "curl -I https://app1.nonfiction.dev") {
		t.Fatalf("ProvisionServer() error = %v, want health recovery message", err)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json"))
	if err != nil {
		t.Fatalf("ReadFile(servers.json) error = %v", err)
	}
	var records []map[string]any
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("Unmarshal(servers.json) error = %v", err)
	}
	if got, want := valueString(records[0]["phase"]), "tls_configured"; got != want {
		t.Fatalf("phase = %q, want %q", got, want)
	}
	if got, want := valueString(records[0]["status"]), "provisioning"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
}

func TestProvisionServerResumesProvisioningRecordWithoutCreatingNewLinode(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", filepath.Join(configHome, "state"))
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	t.Setenv("DNSIMPLE_TOKEN", "dns-token")
	t.Setenv("LINODE_CLI_TOKEN", "linode-token")

	record := map[string]any{
		"provider":    "linode",
		"provider_id": "12345",
		"name":        "app1",
		"hostname":    "app1.nonfiction.dev",
		"label":       "app1",
		"status":      "provisioning",
		"phase":       "linode_created",
		"ipv4":        "198.51.100.10",
		"region":      "ca-central",
		"type":        "g6-standard-1",
		"image":       "linode/ubuntu24.04",
		"created_at":  "2026-05-29T12:00:00Z",
		"updated_at":  "2026-05-29T12:00:00Z",
		"ssh":         map[string]any{"user": "nonfiction", "host": "app1.nonfiction.dev", "port": 22, "source": "linode-profile", "authorized_keys": []map[string]any{{"source": "linode-profile", "id": "77496734", "label": "team-a", "fingerprint": "fp-a"}}},
	}
	if err := saveStatePayload(filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json"), []map[string]any{record}); err != nil {
		t.Fatalf("saveStatePayload() error = %v", err)
	}

	oldRunLinode := runLinodeCLICommand
	oldRunLinodeValue := runLinodeCLIValueFn
	oldZoneLookup := dnsimpleZoneLookup
	oldUpsert := dnsimpleUpsertARecordRun
	oldSalt := secretSaltFn
	oldNow := currentTime
	t.Cleanup(func() {
		runLinodeCLICommand = oldRunLinode
		runLinodeCLIValueFn = oldRunLinodeValue
		dnsimpleZoneLookup = oldZoneLookup
		dnsimpleUpsertARecordRun = oldUpsert
		secretSaltFn = oldSalt
		currentTime = oldNow
	})

	runLinodeCLIValueFn = func(args []string) (any, error) {
		if len(args) >= 2 && args[0] == "sshkeys" && args[1] == "list" {
			return []any{map[string]any{"id": json.Number("77496734"), "label": "team-a", "fingerprint": "fp-a", "created": "2026-05-29", "ssh_key": "ssh-rsa AAAA team-a"}}, nil
		}
		if len(args) >= 2 && args[0] == "linodes" && args[1] == "list" {
			return []any{}, nil
		}
		t.Fatalf("unexpected linode-cli value args: %v", args)
		return nil, nil
	}
	createCalled := false
	runLinodeCLICommand = func(args []string) (map[string]any, error) {
		createCalled = true
		return nil, fmt.Errorf("should not create a second Linode")
	}
	dnsimpleZoneLookup = func(plan Plan, token string) (string, error) { return "nonfiction.dev", nil }
	dnsimpleListARecordsFn = func(token, accountID, zone string) ([]DNSRecord, error) { return []DNSRecord{}, nil }
	dnsimpleUpsertARecordRun = func(token, accountID, zone, name, ip string) error { return nil }
	dnsimpleWaitForRecordDistributionFn = func(token, accountID, zone, name string, timeout time.Duration) error { return nil }
	waitForTCPPortFn = func(host string, port int, timeout time.Duration) error { return nil }
	runSSHCommandFn = func(user, host, command string, timeout time.Duration) error { return nil }
	checkHTTPSHealthFn = func(url, name, hostname string, timeout time.Duration) error { return nil }
	secretSaltFn = func() (string, error) { return "test-salt", nil }
	currentTime = func() time.Time { return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC) }

	plan, err := BuildPlan(Args{NonInteractive: true, Execute: true, Yes: true, Firewall: "none", Name: "app1", Hostname: "app1.nonfiction.dev"})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	result, err := ProvisionServer(plan)
	if err != nil {
		t.Fatalf("ProvisionServer() error = %v", err)
	}
	if createCalled {
		t.Fatal("runLinodeCLICommand was called, want resume without new create")
	}
	if result == nil || result.Server.ProviderID != "12345" {
		t.Fatalf("ProvisionServer() result = %#v, want resumed provider id", result)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json"))
	if err != nil {
		t.Fatalf("ReadFile(servers.json) error = %v", err)
	}
	var records []map[string]any
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("Unmarshal(servers.json) error = %v", err)
	}
	if got, want := valueString(records[0]["status"]), "provisioned"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	if got, want := valueString(records[0]["phase"]), "complete"; got != want {
		t.Fatalf("phase = %q, want %q", got, want)
	}
}

func TestProvisionServerStopsWhenAlreadyProvisioned(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", filepath.Join(configHome, "state"))
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	t.Setenv("DNSIMPLE_TOKEN", "dns-token")
	t.Setenv("LINODE_CLI_TOKEN", "linode-token")
	record := map[string]any{"provider": "linode", "provider_id": "12345", "name": "app1", "hostname": "app1.nonfiction.dev", "label": "app1", "status": "provisioned", "phase": "complete", "ipv4": "198.51.100.10"}
	if err := saveStatePayload(filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json"), []map[string]any{record}); err != nil {
		t.Fatalf("saveStatePayload() error = %v", err)
	}

	oldRunLinode := runLinodeCLICommand
	oldRunLinodeValue := runLinodeCLIValueFn
	oldZoneLookup := dnsimpleZoneLookup
	oldUpsert := dnsimpleUpsertARecordRun
	t.Cleanup(func() {
		runLinodeCLICommand = oldRunLinode
		runLinodeCLIValueFn = oldRunLinodeValue
		dnsimpleZoneLookup = oldZoneLookup
		dnsimpleUpsertARecordRun = oldUpsert
	})
	runLinodeCLIValueFn = func(args []string) (any, error) {
		t.Fatalf("ssh key lookup should not run when already provisioned: %v", args)
		return nil, nil
	}
	createCalled := false
	runLinodeCLICommand = func(args []string) (map[string]any, error) {
		createCalled = true
		return nil, fmt.Errorf("should not create")
	}
	dnsimpleZoneLookup = func(plan Plan, token string) (string, error) {
		t.Fatal("dns lookup should not run when already provisioned")
		return "", nil
	}
	dnsimpleUpsertARecordRun = func(token, accountID, zone, name, ip string) error {
		t.Fatal("dns upsert should not run when already provisioned")
		return nil
	}

	plan, err := BuildPlan(Args{NonInteractive: true, Execute: true, Yes: true, Firewall: "none", NoWait: true, Name: "app1", Hostname: "app1.nonfiction.dev"})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	_, err = ProvisionServer(plan)
	if err == nil || !strings.Contains(err.Error(), "already provisioned") {
		t.Fatalf("ProvisionServer() error = %v, want already provisioned", err)
	}
	if createCalled {
		t.Fatal("runLinodeCLICommand was called for an already provisioned record")
	}
}

func TestCheckHTTPSHealthRequiresReadyIdentityAndStatus(t *testing.T) {
	oldClientFn := healthHTTPClientFn
	t.Cleanup(func() { healthHTTPClientFn = oldClientFn })

	t.Run("success", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"server":"app1","hostname":"app1.nonfiction.dev","status":"ready"}`)
		}))
		defer srv.Close()
		healthHTTPClientFn = srv.Client
		if err := checkHTTPSHealth(srv.URL, "app1", "app1.nonfiction.dev", time.Second); err != nil {
			t.Fatalf("checkHTTPSHealth() error = %v", err)
		}
	})

	t.Run("missing identity fails", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"status":"ready"}`)
		}))
		defer srv.Close()
		healthHTTPClientFn = srv.Client
		if err := checkHTTPSHealth(srv.URL, "app1", "app1.nonfiction.dev", time.Nanosecond); err == nil || !strings.Contains(err.Error(), "unexpected health response") {
			t.Fatalf("checkHTTPSHealth() error = %v, want unexpected health response", err)
		}
	})

	t.Run("bad status fails", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"server":"app1","hostname":"app1.nonfiction.dev","status":"ready"}`)
		}))
		defer srv.Close()
		healthHTTPClientFn = srv.Client
		if err := checkHTTPSHealth(srv.URL, "app1", "app1.nonfiction.dev", time.Nanosecond); err == nil || !strings.Contains(err.Error(), "unexpected health response") {
			t.Fatalf("checkHTTPSHealth() error = %v, want unexpected health response", err)
		}
	})
}

func TestProvisionServerNoWaitLeavesDnsConfiguredAndPrintsManualSteps(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", filepath.Join(configHome, "state"))
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	t.Setenv("DNSIMPLE_TOKEN", "dns-token")
	t.Setenv("LINODE_CLI_TOKEN", "linode-token")

	oldRunLinode := runLinodeCLICommand
	oldRunLinodeValue := runLinodeCLIValueFn
	oldZoneLookup := dnsimpleZoneLookup
	oldUpsert := dnsimpleUpsertARecordRun
	oldWaitForTCP := waitForTCPPortFn
	oldRunSSH := runSSHCommandFn
	oldHealthCheck := checkHTTPSHealthFn
	oldSalt := secretSaltFn
	oldNow := currentTime
	t.Cleanup(func() {
		runLinodeCLICommand = oldRunLinode
		runLinodeCLIValueFn = oldRunLinodeValue
		dnsimpleZoneLookup = oldZoneLookup
		dnsimpleUpsertARecordRun = oldUpsert
		waitForTCPPortFn = oldWaitForTCP
		runSSHCommandFn = oldRunSSH
		checkHTTPSHealthFn = oldHealthCheck
		secretSaltFn = oldSalt
		currentTime = oldNow
	})
	stubEmptyDNSRecords(t)

	runLinodeCLIValueFn = func(args []string) (any, error) {
		if len(args) >= 2 && args[0] == "sshkeys" && args[1] == "list" {
			return []any{map[string]any{"id": json.Number("77496734"), "label": "team-a", "fingerprint": "fp-a", "created": "2026-05-29", "ssh_key": "ssh-rsa AAAA team-a"}}, nil
		}
		if len(args) >= 2 && args[0] == "linodes" && args[1] == "list" {
			return []any{}, nil
		}
		t.Fatalf("unexpected linode-cli value args: %v", args)
		return nil, nil
	}
	runLinodeCLICommand = func(args []string) (map[string]any, error) {
		return map[string]any{"id": "12345", "ipv4": "198.51.100.10"}, nil
	}
	dnsimpleZoneLookup = func(plan Plan, token string) (string, error) { return "nonfiction.dev", nil }
	dnsimpleUpsertARecordRun = func(token, accountID, zone, name, ip string) error { return nil }
	waitForTCPPortFn = func(host string, port int, timeout time.Duration) error {
		t.Fatal("waitForTCPPortFn should not run in --no-wait mode")
		return nil
	}
	runSSHCommandFn = func(user, host, command string, timeout time.Duration) error {
		t.Fatal("runSSHCommandFn should not run in --no-wait mode")
		return nil
	}
	checkHTTPSHealthFn = func(url, name, hostname string, timeout time.Duration) error {
		t.Fatal("checkHTTPSHealthFn should not run in --no-wait mode")
		return nil
	}
	secretSaltFn = func() (string, error) { return "test-salt", nil }
	currentTime = func() time.Time { return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC) }

	plan, err := BuildPlan(Args{NonInteractive: true, Execute: true, Yes: true, NoWait: true, Firewall: "none", Name: "app1", Hostname: "app1.nonfiction.dev"})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	output := captureStdout(t, func() {
		if _, err := ProvisionServer(plan); err != nil {
			t.Fatalf("ProvisionServer() error = %v", err)
		}
	})
	for _, want := range []string{"Server provisioning paused.", "phase: dns_configured", "ssh -o BatchMode=yes nonfiction@app1.nonfiction.dev \"cloud-init status --wait\"", "ssh nonfiction@app1.nonfiction.dev \"sudo /usr/local/bin/nf-enable-wildcard-tls\"", "curl -fsS https://app1.nonfiction.dev/healthz"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json"))
	if err != nil {
		t.Fatalf("ReadFile(servers.json) error = %v", err)
	}
	var records []map[string]any
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("Unmarshal(servers.json) error = %v", err)
	}
	if got, want := valueString(records[0]["status"]), "provisioning"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	if got, want := valueString(records[0]["phase"]), "dns_configured"; got != want {
		t.Fatalf("phase = %q, want %q", got, want)
	}
}

func TestRenderTargetProvisionPausedShowsTLSHandoff(t *testing.T) {
	plan := normalizePlan(Plan{Name: "app1-linode", Hostname: "app1-linode.nonfiction.dev", SshUser: "nonfiction", HealthURL: "https://app1-linode.nonfiction.dev", TargetMode: true})
	created := CreatedServer{Name: "app1-linode", Provider: "linode", ProviderID: "12345", IPv4: "198.51.100.10"}
	dns := DNSState{Provider: "dnsimple", Zone: "nonfiction.dev", HostnameRecord: DNSRecord{Name: "app1-linode", Content: "198.51.100.10"}, WildcardRecord: DNSRecord{Name: "*.app1-linode", Content: "198.51.100.10"}}

	output := renderProvisionPaused(plan, created, dns, "/tmp/providers.json", "dns_configured", nil)
	for _, want := range []string{
		"Target provisioning handed off.",
		"phase: dns_configured",
		"status: queued on target by nf-wildcard-tls.timer",
		"no rerun required; cloud-init starts TLS retry on the target.",
		"sudo systemctl status nf-wildcard-tls.timer nf-wildcard-tls.service",
		"curl -fsS https://app1-linode.nonfiction.dev/healthz",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "sudo /usr/local/bin/nf-enable-wildcard-tls") {
		t.Fatalf("target handoff output should not require manual TLS command:\n%s", output)
	}
}

func TestProvisionServerResumesFromDnsConfigured(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", filepath.Join(configHome, "state"))
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	t.Setenv("DNSIMPLE_TOKEN", "dns-token")
	t.Setenv("LINODE_CLI_TOKEN", "linode-token")
	record := map[string]any{"provider": "linode", "provider_id": "12345", "name": "app1", "hostname": "app1.nonfiction.dev", "label": "app1", "status": "provisioning", "phase": "dns_configured", "ipv4": "198.51.100.10", "dns": map[string]any{"provider": "dnsimple", "zone": "nonfiction.dev", "hostname_record": map[string]any{"name": "app1", "type": "A", "content": "198.51.100.10"}, "wildcard_record": map[string]any{"name": "*.app1", "type": "A", "content": "198.51.100.10"}}}
	if err := saveStatePayload(filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json"), []map[string]any{record}); err != nil {
		t.Fatalf("saveStatePayload() error = %v", err)
	}

	oldRunLinode := runLinodeCLICommand
	oldRunLinodeValue := runLinodeCLIValueFn
	oldZoneLookup := dnsimpleZoneLookup
	oldUpsert := dnsimpleUpsertARecordRun
	oldWaitForTCP := waitForTCPPortFn
	oldRunSSH := runSSHCommandFn
	oldHealthCheck := checkHTTPSHealthFn
	oldSalt := secretSaltFn
	oldNow := currentTime
	t.Cleanup(func() {
		runLinodeCLICommand = oldRunLinode
		runLinodeCLIValueFn = oldRunLinodeValue
		dnsimpleZoneLookup = oldZoneLookup
		dnsimpleUpsertARecordRun = oldUpsert
		waitForTCPPortFn = oldWaitForTCP
		runSSHCommandFn = oldRunSSH
		checkHTTPSHealthFn = oldHealthCheck
		secretSaltFn = oldSalt
		currentTime = oldNow
	})

	createCalled := false
	runLinodeCLICommand = func(args []string) (map[string]any, error) {
		createCalled = true
		return nil, fmt.Errorf("should not create")
	}
	runLinodeCLIValueFn = func(args []string) (any, error) {
		if len(args) >= 2 && args[0] == "sshkeys" && args[1] == "list" {
			return []any{map[string]any{"id": json.Number("77496734"), "label": "team-a", "fingerprint": "fp-a", "created": "2026-05-29", "ssh_key": "ssh-rsa AAAA team-a"}}, nil
		}
		t.Fatalf("unexpected linode-cli value args for dns-configured resume: %v", args)
		return nil, nil
	}
	dnsimpleZoneLookup = func(plan Plan, token string) (string, error) {
		t.Fatalf("dns lookup should not run for dns-configured resume: %v", plan.Hostname)
		return "", nil
	}
	dnsimpleUpsertARecordRun = func(token, accountID, zone, name, ip string) error {
		t.Fatalf("dns upsert should not run for dns-configured resume")
		return nil
	}
	waitForTCPPortFn = func(host string, port int, timeout time.Duration) error { return nil }
	var events []string
	runSSHCommandFn = func(user, host, command string, timeout time.Duration) error {
		events = append(events, "ssh")
		return nil
	}
	checkHTTPSHealthFn = func(url, name, hostname string, timeout time.Duration) error {
		events = append(events, "health")
		data, err := os.ReadFile(filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json"))
		if err != nil {
			t.Fatalf("ReadFile(servers.json) during health check error = %v", err)
		}
		var records []map[string]any
		if err := json.Unmarshal(data, &records); err != nil {
			t.Fatalf("Unmarshal(servers.json) during health check error = %v", err)
		}
		if got, want := valueString(records[0]["phase"]), "tls_configured"; got != want {
			t.Fatalf("phase before health check = %q, want %q", got, want)
		}
		return nil
	}
	secretSaltFn = func() (string, error) { return "test-salt", nil }
	currentTime = func() time.Time { return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC) }

	plan, err := BuildPlan(Args{NonInteractive: true, Execute: true, Yes: true, Firewall: "none", Name: "app1", Hostname: "app1.nonfiction.dev"})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	result, err := ProvisionServer(plan)
	if err != nil {
		t.Fatalf("ProvisionServer() error = %v", err)
	}
	if createCalled {
		t.Fatal("runLinodeCLICommand was called for dns_configured resume")
	}
	if got, want := strings.Join(events, ","), "ssh,health"; got != want {
		t.Fatalf("event sequence = %q, want %q", got, want)
	}
	if result == nil || result.Server.ProviderID != "12345" {
		t.Fatalf("ProvisionServer() result = %#v, want resumed provider id", result)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json"))
	if err != nil {
		t.Fatalf("ReadFile(servers.json) error = %v", err)
	}
	var records []map[string]any
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("Unmarshal(servers.json) error = %v", err)
	}
	if got, want := valueString(records[0]["status"]), "provisioned"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	if got, want := valueString(records[0]["phase"]), "complete"; got != want {
		t.Fatalf("phase = %q, want %q", got, want)
	}
}

func TestProvisionServerResumesFromTLSConfigured(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", filepath.Join(configHome, "state"))
	t.Setenv("NF_PASSWORD_SALT", "test-salt")
	t.Setenv("DNSIMPLE_TOKEN", "dns-token")
	t.Setenv("LINODE_CLI_TOKEN", "linode-token")
	record := map[string]any{"provider": "linode", "provider_id": "12345", "name": "app1", "hostname": "app1.nonfiction.dev", "label": "app1", "status": "provisioning", "phase": "tls_configured", "ipv4": "198.51.100.10", "dns": map[string]any{"provider": "dnsimple", "zone": "nonfiction.dev", "hostname_record": map[string]any{"name": "app1", "type": "A", "content": "198.51.100.10"}, "wildcard_record": map[string]any{"name": "*.app1", "type": "A", "content": "198.51.100.10"}}, "tls": map[string]any{"provider": "certbot-dnsimple", "domains": []any{"app1.nonfiction.dev", "*.app1.nonfiction.dev"}, "certificate": "/etc/letsencrypt/live/app1.nonfiction.dev/fullchain.pem", "key": "/etc/letsencrypt/live/app1.nonfiction.dev/privkey.pem"}}
	if err := saveStatePayload(filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json"), []map[string]any{record}); err != nil {
		t.Fatalf("saveStatePayload() error = %v", err)
	}

	oldRunLinode := runLinodeCLICommand
	oldRunLinodeValue := runLinodeCLIValueFn
	oldZoneLookup := dnsimpleZoneLookup
	oldUpsert := dnsimpleUpsertARecordRun
	oldWaitForTCP := waitForTCPPortFn
	oldRunSSH := runSSHCommandFn
	oldHealthCheck := checkHTTPSHealthFn
	oldSalt := secretSaltFn
	oldNow := currentTime
	t.Cleanup(func() {
		runLinodeCLICommand = oldRunLinode
		runLinodeCLIValueFn = oldRunLinodeValue
		dnsimpleZoneLookup = oldZoneLookup
		dnsimpleUpsertARecordRun = oldUpsert
		waitForTCPPortFn = oldWaitForTCP
		runSSHCommandFn = oldRunSSH
		checkHTTPSHealthFn = oldHealthCheck
		secretSaltFn = oldSalt
		currentTime = oldNow
	})

	createCalled := false
	runLinodeCLICommand = func(args []string) (map[string]any, error) {
		createCalled = true
		return nil, fmt.Errorf("should not create")
	}
	runLinodeCLIValueFn = func(args []string) (any, error) {
		if len(args) >= 2 && args[0] == "sshkeys" && args[1] == "list" {
			return []any{map[string]any{"id": json.Number("77496734"), "label": "team-a", "fingerprint": "fp-a", "created": "2026-05-29", "ssh_key": "ssh-rsa AAAA team-a"}}, nil
		}
		t.Fatalf("unexpected linode-cli value args for tls-configured resume: %v", args)
		return nil, nil
	}
	dnsimpleZoneLookup = func(plan Plan, token string) (string, error) {
		t.Fatalf("dns lookup should not run for tls-configured resume")
		return "", nil
	}
	dnsimpleUpsertARecordRun = func(token, accountID, zone, name, ip string) error {
		t.Fatalf("dns upsert should not run for tls-configured resume")
		return nil
	}
	waitForTCPPortFn = func(host string, port int, timeout time.Duration) error {
		t.Fatalf("waitForTCPPortFn should not run for tls-configured resume")
		return nil
	}
	runSSHCommandFn = func(user, host, command string, timeout time.Duration) error {
		t.Fatalf("runSSHCommandFn should not run for tls-configured resume")
		return nil
	}
	checkHTTPSHealthFn = func(url, name, hostname string, timeout time.Duration) error {
		data, err := os.ReadFile(filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json"))
		if err != nil {
			t.Fatalf("ReadFile(servers.json) during health check error = %v", err)
		}
		var records []map[string]any
		if err := json.Unmarshal(data, &records); err != nil {
			t.Fatalf("Unmarshal(servers.json) during health check error = %v", err)
		}
		if got, want := valueString(records[0]["phase"]), "tls_configured"; got != want {
			t.Fatalf("phase before health check = %q, want %q", got, want)
		}
		return nil
	}
	secretSaltFn = func() (string, error) { return "test-salt", nil }
	currentTime = func() time.Time { return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC) }

	plan, err := BuildPlan(Args{NonInteractive: true, Execute: true, Yes: true, Firewall: "none", Name: "app1", Hostname: "app1.nonfiction.dev"})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	result, err := ProvisionServer(plan)
	if err != nil {
		t.Fatalf("ProvisionServer() error = %v", err)
	}
	if createCalled {
		t.Fatal("runLinodeCLICommand was called for tls_configured resume")
	}
	if result == nil || result.Server.ProviderID != "12345" {
		t.Fatalf("ProvisionServer() result = %#v, want resumed provider id", result)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json"))
	if err != nil {
		t.Fatalf("ReadFile(servers.json) error = %v", err)
	}
	var records []map[string]any
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("Unmarshal(servers.json) error = %v", err)
	}
	if got, want := valueString(records[0]["status"]), "provisioned"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	if got, want := valueString(records[0]["phase"]), "complete"; got != want {
		t.Fatalf("phase = %q, want %q", got, want)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("io.Copy() error = %v", err)
	}
	return buf.String()
}

func TestFindProvisionStateRecordMatchesExactServer(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", filepath.Join(configHome, "state"))
	record := map[string]any{"provider": "linode", "provider_id": "98323103", "linode_id": "98323103", "id": "98323103", "name": "prod2", "hostname": "prod2.nonfiction.dev", "label": "prod2", "status": "provisioned", "phase": "complete"}
	if err := saveStatePayload(filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json"), []map[string]any{record}); err != nil {
		t.Fatalf("saveStatePayload() error = %v", err)
	}

	got, idx, err := findProvisionStateRecord(Plan{Provider: "linode", Name: "prod2", Hostname: "prod2.nonfiction.dev", Label: "prod2"})
	if err != nil {
		t.Fatalf("findProvisionStateRecord() error = %v", err)
	}
	if idx != 0 {
		t.Fatalf("findProvisionStateRecord() index = %d, want 0", idx)
	}
	if got == nil || valueString(got["hostname"]) != "prod2.nonfiction.dev" || valueString(got["name"]) != "prod2" {
		t.Fatalf("findProvisionStateRecord() = %#v, want prod2 record", got)
	}
}

func TestFindProvisionStateRecordRejectsDifferentServer(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", filepath.Join(configHome, "state"))
	record := map[string]any{"provider": "linode", "provider_id": "98323103", "linode_id": "98323103", "id": "98323103", "name": "prod2", "hostname": "prod2.nonfiction.dev", "label": "prod2", "status": "provisioned", "phase": "complete"}
	if err := saveStatePayload(filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json"), []map[string]any{record}); err != nil {
		t.Fatalf("saveStatePayload() error = %v", err)
	}

	got, idx, err := findProvisionStateRecord(Plan{Provider: "linode", Name: "prod3", Hostname: "prod3.nonfiction.dev", Label: "prod3"})
	if err != nil {
		t.Fatalf("findProvisionStateRecord() error = %v", err)
	}
	if got != nil || idx != -1 {
		t.Fatalf("findProvisionStateRecord() = %#v, %d, want nil, -1", got, idx)
	}
}

func TestUpsertStateRecordDoesNotReplaceDifferentServerWithEmptyLabel(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("NF_CONFIG_HOME", configHome)
	t.Setenv("NF_STATE_HOME", filepath.Join(configHome, "state"))
	existing := map[string]any{"provider": "linode", "provider_id": "98323103", "linode_id": "98323103", "id": "98323103", "name": "prod2", "hostname": "prod2.nonfiction.dev", "label": "", "status": "provisioned", "phase": "complete"}
	candidate := map[string]any{"provider": "linode", "provider_id": "98323104", "linode_id": "98323104", "id": "98323104", "name": "prod3", "hostname": "prod3.nonfiction.dev", "label": "", "status": "provisioning", "phase": "linode_created"}
	path := filepath.Join(os.Getenv("NF_STATE_HOME"), "servers.json")
	if err := saveStatePayload(path, []map[string]any{existing}); err != nil {
		t.Fatalf("saveStatePayload() error = %v", err)
	}
	if err := upsertStateRecord(path, candidate); err != nil {
		t.Fatalf("upsertStateRecord() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var records []map[string]any
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("upsertStateRecord() record count = %d, want 2", len(records))
	}
	if valueString(records[0]["hostname"]) != "prod2.nonfiction.dev" || valueString(records[1]["hostname"]) != "prod3.nonfiction.dev" {
		t.Fatalf("upsertStateRecord() records = %#v, want prod2 and prod3", records)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

package provision

import (
	"strings"
	"testing"
)

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

func TestCloudInitTemplateIncludesRuncmd(t *testing.T) {
	plan := Plan{
		SshUser:      "ubuntu",
		ServerName:   "demo",
		SiteDomain:   "demo.example.test",
		RemoteWpPath: "/var/www/demo",
		PhpFpmSocket: "/run/php/php8.3-fpm.sock",
	}

	rendered, err := renderCloudInit(plan, false, "", "", "")
	if err != nil {
		t.Fatalf("renderCloudInit() error = %v", err)
	}
	if !strings.Contains(rendered, "\nruncmd:\n") {
		t.Fatalf("renderCloudInit() output missing runcmd key:\n%s", rendered)
	}
	if !strings.Contains(rendered, "\n  - mkdir -p /var/www/demo\n") {
		t.Fatalf("renderCloudInit() output missing mkdir command:\n%s", rendered)
	}
}

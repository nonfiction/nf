package provision

import (
	"os"
	"path/filepath"
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

func TestBuildPlanInfersProjectDefaultsWithoutPrompting(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	workdir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	plan, err := BuildPlan(Args{NonInteractive: true})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if got, want := plan.ProjectSlug, filepath.Base(workdir); got != want {
		t.Fatalf("ProjectSlug = %q, want %q", got, want)
	}
	if got, want := plan.RemoteWpPath, "/var/www/"+filepath.Base(workdir); got != want {
		t.Fatalf("RemoteWpPath = %q, want %q", got, want)
	}
	if got, want := plan.DbName, filepath.Base(workdir); got != want {
		t.Fatalf("DbName = %q, want %q", got, want)
	}
	if got, want := plan.WpAdminUser, "nf-"+filepath.Base(workdir); got != want {
		t.Fatalf("WpAdminUser = %q, want %q", got, want)
	}
	if got, want := plan.SiteDomain, "app1.nfweb.dev"; got != want {
		t.Fatalf("SiteDomain = %q, want %q", got, want)
	}
}

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

func TestInferProjectMetadataIgnoresLegacyTopLevelFields(t *testing.T) {
	metadata := map[string]any{
		"project_slug": "legacy-project",
		"project_name": "Legacy Project",
	}
	root := filepath.Join(t.TempDir(), "sanjel")
	if got, want := inferProjectSlug("", metadata, root), filepath.Base(root); got != want {
		t.Fatalf("inferProjectSlug() = %q, want %q", got, want)
	}
	if got, want := inferProjectName(metadata, "sanjel"), "Sanjel"; got != want {
		t.Fatalf("inferProjectName() = %q, want %q", got, want)
	}
}

func TestStateRecordsUseNestedFields(t *testing.T) {
	plan := Plan{
		Provider:     "linode",
		ProjectSlug:  "sanjel",
		ServerName:   "app1",
		Label:        "sanjel-app1-production",
		SiteDomain:   "sanjel.app1.nfweb.dev",
		Region:       "us-east",
		LinodeType:   "g6-standard-1",
		Image:        "linode/ubuntu24.04",
		SshUser:      "nonfiction",
		RemoteWpPath: "/var/www/sanjel",
		PhpFpmSocket: "/run/php/php8.3-fpm.sock",
		DbName:       "sanjel",
		DbUser:       "sanjel",
		WpAdminUser:  "nf-sanjel",
	}

	server := serverStateRecord(plan, "12345", "198.51.100.10", "nfweb.dev", "2026-05-27T00:00:00Z")
	for _, legacy := range []string{"project_slug", "ssh_user", "remote_wp_path"} {
		if _, ok := server[legacy]; ok {
			t.Fatalf("server record unexpectedly contains legacy field %q: %#v", legacy, server[legacy])
		}
	}
	if got, ok := server["project"].(string); !ok || got != plan.ProjectSlug {
		t.Fatalf("server project = %#v, want %q", server["project"], plan.ProjectSlug)
	}
	if linode, ok := server["linode"].(map[string]any); !ok || linode["instance_id"] != "12345" {
		t.Fatalf("server linode = %#v, want instance_id 12345", server["linode"])
	}
	if ssh, ok := server["ssh"].(map[string]any); !ok || ssh["host"] != plan.SiteDomain || ssh["user"] != plan.SshUser || ssh["port"] != 22 {
		t.Fatalf("server ssh = %#v, want host/user/port", server["ssh"])
	}
	if services, ok := server["services"].(map[string]any); !ok || services["php_fpm"] != "php8.3-fpm" {
		t.Fatalf("server services = %#v, want php_fpm service", server["services"])
	}

	site := siteStateRecord(plan, "nfweb.dev", "2026-05-27T00:00:00Z")
	for _, legacy := range []string{"project_slug", "site_url", "remote_wp_path", "db_name", "db_user"} {
		if _, ok := site[legacy]; ok {
			t.Fatalf("site record unexpectedly contains legacy field %q: %#v", legacy, site[legacy])
		}
	}
	if got, ok := site["project"].(string); !ok || got != plan.ProjectSlug {
		t.Fatalf("site project = %#v, want %q", site["project"], plan.ProjectSlug)
	}
	if got, ok := site["url"].(string); !ok || got != "https://"+plan.SiteDomain {
		t.Fatalf("site url = %#v, want https://%s", site["url"], plan.SiteDomain)
	}
	if got, ok := site["remote_path"].(string); !ok || got != plan.RemoteWpPath {
		t.Fatalf("site remote_path = %#v, want %q", site["remote_path"], plan.RemoteWpPath)
	}
	if wordpress, ok := site["wordpress"].(map[string]any); !ok || wordpress["wp_path"] != plan.RemoteWpPath {
		t.Fatalf("site wordpress = %#v, want wp_path %q", site["wordpress"], plan.RemoteWpPath)
	}
	if database, ok := site["database"].(map[string]any); !ok || database["name"] != plan.DbName || database["user"] != plan.DbUser {
		t.Fatalf("site database = %#v, want name/user", site["database"])
	}
}

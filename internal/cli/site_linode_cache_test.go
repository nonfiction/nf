package cli

import (
	"strings"
	"testing"
)

func TestLinodeCachedPHPLocationsUseOriginalRequestURI(t *testing.T) {
	const wantKey = `fastcgi_cache_key "\$scheme|\$request_method|\$host|\$request_uri";`
	for _, tc := range []struct {
		name   string
		script string
	}{
		{"site add", renderSiteAddScript(siteAddPlan{})},
		{"site repair", renderLinodeSiteRepairScript("/var/www/sites/client/public", "client.linode1.nonfiction.dev", "8.3", []string{"client.linode1.live"})},
		{"domain refresh", renderLinodeDomainRefreshScript(siteDomainPlan{}, linodeDomainArtifacts("example.com"), "client.linode1.live", "primary", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found := false
			for line := range strings.SplitSeq(tc.script, "\n") {
				if !strings.Contains(line, "fastcgi_cache $cache_zone;") {
					continue
				}
				found = true
				if !strings.Contains(line, wantKey) {
					t.Errorf("cache-enabled PHP location missing %s:\n%s", wantKey, line)
				}
			}
			if !found {
				t.Fatal("no cache-enabled PHP location in rendered script")
			}
		})
	}
}

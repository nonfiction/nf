package cli

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
)

func parseSiteCacheArgs(argv []string) (string, bool) {
	if len(argv) > 1 {
		fmt.Fprintln(os.Stderr, "site cache takes at most one site or env ref")
		return "", false
	}
	if len(argv) == 0 {
		if !siteIsInteractiveFn() {
			fmt.Fprintln(os.Stderr, "site cache requires a site or env ref like site.target or site.target:staging")
			return "", false
		}
		selected, err := chooseSiteEnv("clear cache", "")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return "", false
		}
		return selected, true
	}
	ref := strings.TrimSpace(argv[0])
	if strings.HasPrefix(ref, "-") {
		fmt.Fprintf(os.Stderr, "unknown site cache flag: %s\n", ref)
		return "", false
	}
	if _, _, ok := splitSiteEnvRef(ref); ok {
		return ref, true
	}
	return canonicalEnvID(ref, "live"), true
}

func cmdSiteCache(envRef string) int {
	siteID, env, ok := splitSiteEnvRef(envRef)
	if !ok {
		fmt.Fprintln(os.Stderr, "site cache requires a site or env ref like site.target or site.target:staging")
		return 1
	}
	record, _, err := cachedSiteEnv(siteID, env)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if record == nil {
		fmt.Fprintf(os.Stderr, "No cached remote env matched %q. Run nf site refresh after target cache is current.\n", canonicalEnvID(siteID, env))
		return 1
	}
	if err := validateSiteRecord(record); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(record["provider"])))
	printSiteCachePreflight(siteID, env, provider, record)
	switch provider {
	case "kinsta":
		return cmdSiteCacheKinsta(siteID, env, record)
	case "linode":
		return cmdSiteCacheLinode(record)
	default:
		fmt.Fprintf(os.Stderr, "site cache is not implemented for provider %q; no cache was cleared.\n", provider)
		return 1
	}
}

func printSiteCachePreflight(siteID, env, provider string, record map[string]any) {
	fmt.Println("Site cache preflight:")
	fmt.Printf("  site:     %s\n", siteID)
	fmt.Printf("  env:      %s\n", env)
	fmt.Printf("  provider: %s\n", provider)
	if target := siteProviderTarget(record); target != "" {
		fmt.Printf("  target:   %s\n", target)
	}
	if url := firstRecordString(record, "url", "site_url", "home_url", "hostname"); url != "" {
		fmt.Printf("  url:      %s\n", url)
	}
}

func cmdSiteCacheKinsta(siteID, env string, record map[string]any) int {
	environmentID := siteKinstaID(record, "environment_id")
	if environmentID == "" {
		fmt.Fprintf(os.Stderr, "Kinsta site env %q is missing environment_id. Run nf site refresh.\n", canonicalEnvID(siteID, env))
		return 1
	}
	token := envwizard.Value("KINSTA_API_KEY")
	if token == "" {
		fmt.Fprintf(os.Stderr, "Expected KINSTA_API_KEY in the environment or %s.\n", config.EnvFile())
		return 1
	}
	fmt.Printf("  kinsta env: %s\n", environmentID)
	fmt.Println("  action:   clear Kinsta site cache")
	client := newKinstaClient(token)
	ctx := context.Background()
	opID, err := client.ClearSiteCache(ctx, environmentID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if opID != "" {
		fmt.Printf("Kinsta operation: %s\n", opID)
	}
	if err := waitKinstaOperation(ctx, client, opID); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Site cache cleared.")
	return 0
}

func cmdSiteCacheLinode(record map[string]any) int {
	cachePath := linodeSiteCachePath(firstRecordString(record, "path"))
	if cachePath == "" {
		fmt.Fprintf(os.Stderr, "Site env %q is missing a cacheable WordPress path. Run nf site refresh.\n", siteSummary(record))
		return 1
	}
	fmt.Println("  action:   purge nginx page cache and flush WordPress object cache")
	fmt.Printf("  cache:    %s\n", cachePath)
	sshArgs, err := linodeSiteEnvSSHArgs(record, "wp", []string{"cache", "flush"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(sshArgs) == 0 {
		fmt.Fprintln(os.Stderr, "failed to build Linode cache command")
		return 1
	}
	remoteCommand := "cache_dir=" + shellQuoteArg(cachePath) + "; if [ -d \"$cache_dir\" ]; then sudo find \"$cache_dir\" -mindepth 1 -maxdepth 1 -exec rm -rf {} +; fi; " + sshArgs[len(sshArgs)-1]
	sshArgs = append([]string(nil), sshArgs...)
	sshArgs[len(sshArgs)-1] = remoteCommand
	printCommandArgs(sshArgs)
	if err := runSSHCommandFn(sshArgs); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Site cache cleared.")
	return 0
}

func linodeSiteCacheSlug(sitePath string) string {
	sitePath = strings.TrimSpace(sitePath)
	if sitePath == "" {
		return ""
	}
	parent := path.Base(path.Dir(path.Clean(sitePath)))
	if parent == "." || parent == "/" {
		return ""
	}
	return parent
}

func linodeSiteCachePath(sitePath string) string {
	slug := linodeSiteCacheSlug(sitePath)
	if slug == "" {
		return ""
	}
	return "/var/cache/nginx/nf/sites/" + slug
}

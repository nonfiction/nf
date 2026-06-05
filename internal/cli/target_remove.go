package cli

// Target and legacy server removal commands.

import (
	"fmt"
	"os"
	"strings"

	"github.com/nonfiction/nf/internal/state"
	"github.com/nonfiction/nf/internal/ui"
)

func cmdDeleteServer(needle string, dryRun, execute, yes, nonInteractive bool) int {
	servers, err := state.LoadStateRecords("servers")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	server := state.MatchingRecord(servers, needle)
	if server == nil {
		fmt.Fprintf(os.Stderr, "No server matched %q.\n", needle)
		return 1
	}
	provider := strings.ToLower(strings.TrimSpace(fmt.Sprint(server["provider"])))
	relatedSites, err := state.LoadStateRecords("sites")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	matchedSites := make([]map[string]any, 0)
	for _, site := range relatedSites {
		if siteMatchesServer(site, server) {
			matchedSites = append(matchedSites, site)
		}
	}
	remoteID := firstRecordString(server, "linode_id", "id", "_state_key")
	dnsDeletes := serverDNSDeleteTargets(server)
	if !execute && (dryRun || nonInteractive) {
		dryRun = true
	}
	if execute && dryRun {
		fmt.Fprintln(os.Stderr, "Choose either --execute or --dry-run, not both.")
		return 1
	}
	if nonInteractive && execute && !yes {
		fmt.Fprintln(os.Stderr, "Remote execution requires both --execute and --yes in non-interactive mode.")
		return 1
	}
	willExecute := execute || (!dryRun && !nonInteractive)
	if willExecute && provider != "linode" {
		fmt.Fprintf(os.Stderr, "Unsupported provider %q. Only linode is available for server deletion.\n", provider)
		return 1
	}
	if willExecute && provider == "linode" && strings.TrimSpace(remoteID) == "" {
		fmt.Fprintln(os.Stderr, "Selected server is missing a Linode id.")
		return 1
	}
	if willExecute {
		if err := validateServerDNSDeleteTargets(dnsDeletes); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	mode := "dry-run"
	if willExecute {
		mode = "execute"
	}
	serverLabel := serverSummary(server)
	if serverLabel == "" {
		serverLabel = needle
	}
	fmt.Println("Delete server plan:")
	fmt.Printf("  server: %s\n", serverLabel)
	fmt.Printf("  provider: %s\n", provider)
	if remoteID != "" {
		if provider == "linode" {
			fmt.Printf("  remote action: Linode API delete instance %s\n", remoteID)
		} else {
			fmt.Printf("  remote action: unavailable for provider %q\n", provider)
		}
	}
	if len(dnsDeletes) == 0 {
		fmt.Println("  dns actions: none")
	} else {
		for _, target := range dnsDeletes {
			recordType := ""
			if !strings.EqualFold(target.recordType, "A") && strings.TrimSpace(target.recordType) != "" {
				recordType = " " + strings.ToUpper(strings.TrimSpace(target.recordType))
			}
			fmt.Printf("  dns action: delete %s%s %s\n", target.provider, recordType, provisionDNSRecordFQDN(target))
		}
	}
	if len(matchedSites) == 0 {
		fmt.Println("  related sites: none")
	} else {
		names := make([]string, 0, len(matchedSites))
		for _, site := range matchedSites {
			if summary := siteSummary(site); summary != "" {
				names = append(names, summary)
			}
		}
		if len(names) == 0 {
			fmt.Printf("  related sites: %d\n", len(matchedSites))
		} else {
			fmt.Printf("  related sites: %d (%s)\n", len(matchedSites), strings.Join(names, ", "))
		}
	}
	fmt.Printf("  mode: %s\n", mode)
	if !willExecute {
		return 0
	}
	if !yes {
		confirmed, err := ui.Confirm(fmt.Sprintf("Delete server %q and matching sites from remote infrastructure and shared state?", needle), false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	if err := runLinodeDeleteFn(remoteID); err != nil {
		if isLinodeNotFoundError(err) {
			fmt.Fprintln(os.Stderr, "Remote Linode was not found; removing stale local state.")
		} else {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	for _, target := range dnsDeletes {
		if err := deleteServerDNSRecord(target); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if _, err := state.DeleteStateRecords("servers", func(record map[string]any) bool { return serverMatchesRecord(record, server) }); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if _, err := state.DeleteStateRecords("sites", func(record map[string]any) bool { return siteMatchesServer(record, server) }); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func cmdRemoveTarget(needle string, dryRun, execute, yes, nonInteractive bool) int {
	providers, err := state.LoadStateRecords("providers")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	target, providerName := findProviderTarget(providers, needle)
	if target == nil {
		fmt.Fprintf(os.Stderr, "No target matched %q.\n", needle)
		return 1
	}
	if providerName == "" {
		providerName = strings.ToLower(strings.TrimSpace(recordValueString(target["provider"])))
	}
	if providerName == "kinsta" {
		fmt.Fprintln(os.Stderr, "Kinsta target cannot be removed.")
		return 1
	}
	if providerName != "linode" {
		fmt.Fprintf(os.Stderr, "Unsupported provider %q. Only linode targets can be removed.\n", providerName)
		return 1
	}
	sites, err := state.LoadStateRecords("sites")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	matchedSites := make([]map[string]any, 0)
	for _, site := range sites {
		if siteMatchesTarget(site, target) {
			matchedSites = append(matchedSites, site)
		}
	}
	relatedSiteNames := make([]string, 0, len(matchedSites))
	seenRelatedSites := map[string]bool{}
	for _, site := range matchedSites {
		summary := siteEnvDisplaySite(site)
		if summary == "" {
			summary = siteSummary(site)
		}
		if summary != "" && !seenRelatedSites[summary] {
			seenRelatedSites[summary] = true
			relatedSiteNames = append(relatedSiteNames, summary)
		}
	}
	remoteID := firstRecordString(target, "linode_id", "id", "provider_id", "_state_key")
	dnsDeletes := serverDNSDeleteTargets(target)
	if !execute && (dryRun || nonInteractive) {
		dryRun = true
	}
	if execute && dryRun {
		fmt.Fprintln(os.Stderr, "Choose either --execute or --dry-run, not both.")
		return 1
	}
	if nonInteractive && execute && !yes {
		fmt.Fprintln(os.Stderr, "Remote execution requires both --execute and --yes in non-interactive mode.")
		return 1
	}
	willExecute := execute || (!dryRun && !nonInteractive)
	if willExecute && strings.TrimSpace(remoteID) == "" {
		fmt.Fprintln(os.Stderr, "Selected target is missing a Linode id.")
		return 1
	}
	if willExecute {
		if err := validateServerDNSDeleteTargets(dnsDeletes); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	mode := "dry-run"
	if willExecute {
		mode = "execute"
	}
	targetLabel := serverSummary(target)
	if targetLabel == "" {
		targetLabel = needle
	}
	fmt.Println("Remove target plan:")
	fmt.Printf("  target: %s\n", targetLabel)
	fmt.Printf("  provider: %s\n", providerName)
	if remoteID != "" {
		fmt.Printf("  remote action: Linode API delete instance %s\n", remoteID)
	}
	if len(dnsDeletes) == 0 {
		fmt.Println("  dns actions: none")
	} else {
		for _, target := range dnsDeletes {
			recordType := ""
			if !strings.EqualFold(target.recordType, "A") && strings.TrimSpace(target.recordType) != "" {
				recordType = " " + strings.ToUpper(strings.TrimSpace(target.recordType))
			}
			fmt.Printf("  dns action: delete %s%s %s\n", target.provider, recordType, provisionDNSRecordFQDN(target))
		}
	}
	if len(relatedSiteNames) == 0 {
		fmt.Println("  related sites: none")
	} else {
		fmt.Printf("  related sites: %s\n", strings.Join(relatedSiteNames, ", "))
		fmt.Printf("  site cache action: remove %d site(s) from local cache\n", len(relatedSiteNames))
	}
	fmt.Printf("  mode: %s\n", mode)
	if !willExecute {
		return 0
	}
	if !yes {
		message := fmt.Sprintf("Remove target %q, delete its Linode, and delete its DNS records?", needle)
		if len(relatedSiteNames) > 0 {
			message = fmt.Sprintf("Remove target %q, delete its Linode, delete its DNS records, and remove %d related site(s) from local cache?", needle, len(relatedSiteNames))
		}
		confirmed, err := ui.Confirm(message, false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	if err := runLinodeDeleteFn(remoteID); err != nil {
		if isLinodeNotFoundError(err) {
			fmt.Fprintln(os.Stderr, "Remote Linode was not found; removing stale local state.")
		} else {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	for _, target := range dnsDeletes {
		if err := deleteServerDNSRecord(target); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if !removeProviderTarget(providers, target) {
		fmt.Fprintf(os.Stderr, "No target matched %q.\n", needle)
		return 1
	}
	if err := state.SaveStateRecords("providers", providers); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(matchedSites) > 0 {
		if _, err := state.DeleteStateRecords("sites", func(record map[string]any) bool { return siteMatchesTarget(record, target) }); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return 0
}

func findProviderTarget(providers []map[string]any, needle string) (map[string]any, string) {
	for _, provider := range providers {
		providerName := strings.ToLower(strings.TrimSpace(recordValueString(provider["provider"])))
		if providerName == "" {
			providerName = strings.ToLower(strings.TrimSpace(recordValueString(provider["_state_key"])))
		}
		for _, target := range targetMaps(provider["targets"]) {
			candidate := cloneRecord(target)
			if recordValueString(candidate["provider"]) == "" && providerName != "" {
				candidate["provider"] = providerName
			}
			if state.MatchingRecord([]map[string]any{candidate}, needle) != nil {
				return target, providerName
			}
		}
	}
	return nil, ""
}

func removeProviderTarget(providers []map[string]any, target map[string]any) bool {
	for _, provider := range providers {
		targets := targetMaps(provider["targets"])
		kept := make([]map[string]any, 0, len(targets))
		removed := false
		for _, candidate := range targets {
			if !removed && serverMatchesRecord(candidate, target) {
				removed = true
				continue
			}
			kept = append(kept, candidate)
		}
		if removed {
			provider["targets"] = kept
			return true
		}
	}
	return false
}

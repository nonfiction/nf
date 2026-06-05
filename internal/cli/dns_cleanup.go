package cli

// DNS cleanup planning for target removal.
//
// Cached targets may contain explicit DNS records, but older caches do not. The
// inference helpers here rebuild expected DNSimple records from target data so
// removal can be safe and repeatable.

import (
	"fmt"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
)

type serverDNSDeleteTarget struct {
	provider   string
	accountID  string
	zone       string
	name       string
	recordType string
}

func serverDNSDeleteTargets(server map[string]any) []serverDNSDeleteTarget {
	dns, _ := server["dns"].(map[string]any)
	if dns == nil {
		return inferredServerDNSDeleteTargets(server)
	}
	provider := strings.ToLower(strings.TrimSpace(firstRecordString(dns, "provider")))
	zone := firstRecordString(dns, "zone")
	if provider == "" || zone == "" {
		return inferredServerDNSDeleteTargets(server)
	}
	accountID := firstRecordString(dns, "account_id")
	if provider == "dnsimple" && accountID == "" {
		accountID = dnsimpleAccountIDValue()
	}
	seen := map[string]struct{}{}
	targets := make([]serverDNSDeleteTarget, 0, 2)
	for _, name := range []string{
		mapStringAtPath(server, "dns", "hostname_record", "name"),
		mapStringAtPath(server, "dns", "wildcard_record", "name"),
		firstRecordString(dns, "hostname_name"),
		firstRecordString(dns, "wildcard_name"),
	} {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := "A:" + name
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, serverDNSDeleteTarget{provider: provider, accountID: accountID, zone: zone, name: name, recordType: "A"})
	}
	if provider == "dnsimple" {
		for _, target := range inferredServerDNSDeleteTargetsForZone(server, zone, accountID) {
			key := target.recordType + ":" + target.name
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			targets = append(targets, target)
		}
		for _, target := range inferredServerACMETXTDeleteTargetsForZone(server, zone, accountID) {
			key := target.recordType + ":" + target.name
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			targets = append(targets, target)
		}
	}
	return targets
}

func inferredServerDNSDeleteTargets(server map[string]any) []serverDNSDeleteTarget {
	zone := baseDomainValue()
	accountID := dnsimpleAccountIDValue()
	targets := inferredServerDNSDeleteTargetsForZone(server, zone, accountID)
	targets = append(targets, inferredServerACMETXTDeleteTargetsForZone(server, zone, accountID)...)
	return targets
}

func inferredServerDNSDeleteTargetsForZone(server map[string]any, zone, accountID string) []serverDNSDeleteTarget {
	hostname := firstRecordString(server, "hostname", "host")
	if hostname == "" || zone == "" {
		return nil
	}
	hostname = strings.TrimSuffix(strings.TrimSpace(hostname), ".")
	zone = strings.TrimSuffix(strings.TrimSpace(zone), ".")
	suffix := "." + zone
	if hostname == zone || !strings.HasSuffix(hostname, suffix) {
		return nil
	}
	name := strings.TrimSuffix(hostname, suffix)
	if name == "" {
		return nil
	}
	return []serverDNSDeleteTarget{
		{provider: "dnsimple", accountID: accountID, zone: zone, name: name, recordType: "A"},
		{provider: "dnsimple", accountID: accountID, zone: zone, name: "*." + name, recordType: "A"},
	}
}

func inferredServerACMETXTDeleteTargetsForZone(server map[string]any, zone, accountID string) []serverDNSDeleteTarget {
	hostname := firstRecordString(server, "hostname", "host")
	if hostname == "" || zone == "" {
		return nil
	}
	hostname = strings.TrimSuffix(strings.TrimSpace(hostname), ".")
	zone = strings.TrimSuffix(strings.TrimSpace(zone), ".")
	suffix := "." + zone
	if hostname == zone || !strings.HasSuffix(hostname, suffix) {
		return nil
	}
	name := strings.TrimSuffix(hostname, suffix)
	if name == "" {
		return nil
	}
	return []serverDNSDeleteTarget{{provider: "dnsimple", accountID: accountID, zone: zone, name: "_acme-challenge." + name, recordType: "TXT"}}
}

func provisionDNSRecordFQDN(target serverDNSDeleteTarget) string {
	name := strings.TrimSpace(target.name)
	zone := strings.TrimSpace(target.zone)
	switch {
	case name == "":
		return zone
	case zone == "":
		return name
	default:
		return name + "." + zone
	}
}

func isDNSimpleRecordAlreadyAbsentError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "deleting dnsimple") && strings.Contains(message, "404") && strings.Contains(message, "not found")
}

func validateServerDNSDeleteTargets(targets []serverDNSDeleteTarget) error {
	for _, target := range targets {
		switch target.provider {
		case "", "none":
			continue
		case "dnsimple":
			if strings.TrimSpace(envwizard.Value("DNSIMPLE_TOKEN")) == "" {
				return fmt.Errorf("Expected DNSIMPLE_TOKEN in the environment or %s.", config.EnvFile())
			}
			if strings.TrimSpace(target.accountID) == "" {
				return fmt.Errorf("Expected dnsimple_account_id in %s. Run nf provider check dnsimple.", config.ConfigFile())
			}
			if strings.TrimSpace(target.zone) == "" {
				return fmt.Errorf("Expected base_domain in %s.", config.ConfigFile())
			}
		default:
			return fmt.Errorf("unsupported DNS provider %q for server deletion", target.provider)
		}
	}
	return nil
}

func deleteServerDNSRecord(target serverDNSDeleteTarget) error {
	switch target.provider {
	case "", "none":
		return nil
	case "dnsimple":
		if err := validateServerDNSDeleteTargets([]serverDNSDeleteTarget{target}); err != nil {
			return err
		}
		token := envwizard.Value("DNSIMPLE_TOKEN")
		deleteFn := deleteDNSRecordFn
		if strings.EqualFold(target.recordType, "TXT") {
			deleteFn = deleteDNSTXTRecordFn
		}
		if err := deleteFn(token, target.accountID, target.zone, target.name); err != nil {
			if isDNSimpleRecordAlreadyAbsentError(err) {
				fmt.Printf("DNSimple record %s already absent (%v)\n", provisionDNSRecordFQDN(target), err)
				return nil
			}
			return err
		}
		return nil
	default:
		return fmt.Errorf("unsupported DNS provider %q for server deletion", target.provider)
	}
}

package provision

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dnsimple/dnsimple-go/v9/dnsimple"
	"github.com/nonfiction/nf/internal/config"
)

type dnsimpleProvider struct {
	client    *dnsimple.Client
	accountID string
}

var dnsimpleProviderFactory = newDNSimpleProvider

func newDNSimpleProvider(ctx context.Context, token, accountID string) (*dnsimpleProvider, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(token) == "" {
		return nil, Error{Msg: fmt.Sprintf("Expected DNSIMPLE_TOKEN in the environment or %s.", config.EnvFile())}
	}
	httpClient := dnsimple.StaticTokenHTTPClient(ctx, token)
	client := dnsimple.NewClient(httpClient)
	client.SetUserAgent("nf")
	return &dnsimpleProvider{client: client, accountID: strings.TrimSpace(accountID)}, nil
}

func (p *dnsimpleProvider) Name() string { return "dnsimple" }

func (p *dnsimpleProvider) findZone(ctx context.Context, zone string) (string, error) {
	zone = strings.TrimSpace(zone)
	if zone == "" {
		return "", Error{Msg: fmt.Sprintf("DNSimple zone is required for account %s. Check base_domain, dnsimple_account_id, and DNSIMPLE_TOKEN.", p.accountID)}
	}
	if _, err := p.client.Zones.GetZone(ctx, p.accountID, zone); err != nil {
		var apiErr *dnsimple.ErrorResponse
		if errors.As(err, &apiErr) && apiErr.HTTPResponse != nil && apiErr.HTTPResponse.StatusCode == http.StatusNotFound {
			return "", Error{Msg: fmt.Sprintf("DNSimple zone %s was not found for account %s. Check base_domain, dnsimple_account_id, and DNSIMPLE_TOKEN.", zone, p.accountID)}
		}
		return "", Error{Msg: fmt.Sprintf("Checking DNSimple zone %s for account %s: %v", zone, p.accountID, err)}
	}
	return zone, nil
}

func (p *dnsimpleProvider) listARecords(ctx context.Context, zone string) ([]DNSRecord, error) {
	resp, err := p.client.Zones.ListRecords(ctx, p.accountID, zone, &dnsimple.ZoneRecordListOptions{Type: dnsimple.String("A")})
	if err != nil {
		return nil, Error{Msg: fmt.Sprintf("Listing DNSimple A records for zone %s: %v", zone, err)}
	}
	records := make([]DNSRecord, 0, len(resp.Data))
	for _, record := range resp.Data {
		records = append(records, dnsRecordFromZoneRecord(record))
	}
	return records, nil
}

func (p *dnsimpleProvider) upsertARecord(ctx context.Context, zone, name, ip string) (DNSRecord, string, error) {
	records, err := p.listARecords(ctx, zone)
	if err != nil {
		return DNSRecord{}, "", err
	}
	for _, record := range records {
		if record.Name != name {
			continue
		}
		if record.Content == ip && record.TTL == 60 {
			return record, "already points", nil
		}
		recordID, err := strconv.ParseInt(record.ID, 10, 64)
		if err != nil {
			return DNSRecord{}, "", Error{Msg: fmt.Sprintf("DNSimple record %s has invalid id %q", name, record.ID)}
		}
		resp, err := p.client.Zones.UpdateRecord(ctx, p.accountID, zone, recordID, dnsimple.ZoneRecordAttributes{Content: ip, TTL: 60})
		if err != nil {
			return DNSRecord{}, "", Error{Msg: fmt.Sprintf("Updating DNSimple A record %s in zone %s: %v", name, zone, err)}
		}
		if resp == nil || resp.Data == nil {
			return DNSRecord{}, "", Error{Msg: fmt.Sprintf("Updating DNSimple A record %s in zone %s: empty response", name, zone)}
		}
		updated := dnsRecordFromZoneRecord(*resp.Data)
		if updated.ID == "" {
			updated.ID = record.ID
		}
		return updated, "updated", nil
	}
	resp, err := p.client.Zones.CreateRecord(ctx, p.accountID, zone, dnsimple.ZoneRecordAttributes{Type: "A", Name: dnsimple.String(name), Content: ip, TTL: 60})
	if err != nil {
		return DNSRecord{}, "", Error{Msg: fmt.Sprintf("Creating DNSimple A record %s in zone %s: %v", name, zone, err)}
	}
	if resp == nil || resp.Data == nil {
		return DNSRecord{}, "", Error{Msg: fmt.Sprintf("Creating DNSimple A record %s in zone %s: empty response", name, zone)}
	}
	created := dnsRecordFromZoneRecord(*resp.Data)
	return created, "created", nil
}

func (p *dnsimpleProvider) deleteARecord(ctx context.Context, zone, name string) (DNSRecord, string, error) {
	records, err := p.listARecords(ctx, zone)
	if err != nil {
		return DNSRecord{}, "", err
	}
	for _, record := range records {
		if record.Name != name {
			continue
		}
		recordID, err := strconv.ParseInt(record.ID, 10, 64)
		if err != nil {
			return DNSRecord{}, "", Error{Msg: fmt.Sprintf("DNSimple record %s has invalid id %q", name, record.ID)}
		}
		if _, err := p.client.Zones.DeleteRecord(ctx, p.accountID, zone, recordID); err != nil {
			return DNSRecord{}, "", Error{Msg: fmt.Sprintf("Deleting DNSimple A record %s in zone %s: %v", name, zone, err)}
		}
		return record, "deleted", nil
	}
	return DNSRecord{Name: strings.TrimSpace(name), Type: "A"}, "already absent", nil
}

func (p *dnsimpleProvider) waitForRecordDistribution(ctx context.Context, zone, recordID string, timeout time.Duration) error {
	recordID = strings.TrimSpace(recordID)
	if recordID == "" {
		return Error{Msg: fmt.Sprintf("Waiting for DNSimple record distribution in zone %s: missing record id", zone)}
	}
	id, err := strconv.ParseInt(recordID, 10, 64)
	if err != nil {
		return Error{Msg: fmt.Sprintf("Waiting for DNSimple record distribution in zone %s: invalid record id %q", zone, recordID)}
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		resp, err := p.client.Zones.CheckZoneRecordDistribution(ctx, p.accountID, zone, id)
		if err == nil && resp != nil && resp.Data != nil && resp.Data.Distributed {
			return nil
		}
		if ctx.Err() != nil {
			if err != nil {
				return Error{Msg: fmt.Sprintf("Timed out waiting for DNSimple record %s in zone %s to distribute: %v", recordID, zone, err)}
			}
			return Error{Msg: fmt.Sprintf("Timed out waiting for DNSimple record %s in zone %s to distribute", recordID, zone)}
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func dnsRecordFromZoneRecord(record dnsimple.ZoneRecord) DNSRecord {
	return DNSRecord{
		ID:      strconv.FormatInt(record.ID, 10),
		Name:    strings.TrimSpace(record.Name),
		Type:    strings.TrimSpace(record.Type),
		Content: strings.TrimSpace(record.Content),
		TTL:     record.TTL,
	}
}

func fqdnForDNSRecordName(name, zone string) string {
	name = strings.TrimSpace(name)
	zone = strings.TrimSpace(zone)
	switch {
	case name == "":
		return zone
	case zone == "":
		return name
	default:
		return name + "." + zone
	}
}

func defaultDNSimpleZoneLookup(plan Plan, token string) (string, error) {
	provider, err := dnsimpleProviderFactory(context.Background(), token, plan.DnsimpleAccountID)
	if err != nil {
		return "", err
	}
	return provider.findZone(context.Background(), firstNonEmpty(plan.DnsZone, plan.Domain, serverDomain()))
}

func defaultDNSimpleListARecords(token, accountID, zone string) ([]DNSRecord, error) {
	provider, err := dnsimpleProviderFactory(context.Background(), token, accountID)
	if err != nil {
		return nil, err
	}
	return provider.listARecords(context.Background(), zone)
}

func defaultDNSimpleUpsertARecord(token, accountID, zone, name, ip string) error {
	provider, err := dnsimpleProviderFactory(context.Background(), token, accountID)
	if err != nil {
		return err
	}
	record, action, err := provider.upsertARecord(context.Background(), zone, name, ip)
	if err != nil {
		return err
	}
	switch action {
	case "created":
		fmt.Printf("Created DNS %s -> %s\n", fqdnForDNSRecordName(name, zone), ip)
	case "updated":
		fmt.Printf("Updated DNS %s -> %s\n", fqdnForDNSRecordName(name, zone), ip)
	default:
		fmt.Printf("DNS %s already points to %s\n", fqdnForDNSRecordName(name, zone), ip)
	}
	_ = record
	return nil
}

func defaultDNSimpleDeleteARecord(token, accountID, zone, name string) error {
	provider, err := dnsimpleProviderFactory(context.Background(), token, accountID)
	if err != nil {
		return err
	}
	record, action, err := provider.deleteARecord(context.Background(), zone, name)
	if err != nil {
		return err
	}
	switch action {
	case "deleted":
		fmt.Printf("Deleted DNS %s\n", fqdnForDNSRecordName(name, zone))
	default:
		fmt.Printf("DNS %s already absent\n", fqdnForDNSRecordName(record.Name, zone))
	}
	return nil
}

func DeleteDNSimpleARecord(token, accountID, zone, name string) error {
	return defaultDNSimpleDeleteARecord(token, accountID, zone, name)
}

func defaultDNSimpleWaitForRecordDistribution(token, accountID, zone, name string, timeout time.Duration) error {
	provider, err := dnsimpleProviderFactory(context.Background(), token, accountID)
	if err != nil {
		return err
	}
	records, err := provider.listARecords(context.Background(), zone)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Name == name {
			return provider.waitForRecordDistribution(context.Background(), zone, record.ID, timeout)
		}
	}
	return Error{Msg: fmt.Sprintf("Waiting for DNSimple record distribution in zone %s: record %s was not found", zone, name)}
}

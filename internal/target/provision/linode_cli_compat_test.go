package provision

import (
	"context"
	"fmt"
)

var (
	runLinodeCLIValueFn = func(args []string) (any, error) { return nil, fmt.Errorf("unhandled fake Linode value call: %v", args) }
	runLinodeCLICommand = func(args []string) (map[string]any, error) {
		return nil, fmt.Errorf("unhandled fake Linode command call: %v", args)
	}
)

func recordStringList(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := valueString(item); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

type legacyTestProvider struct{}

func (legacyTestProvider) Name() string { return "linode" }

func (legacyTestProvider) ListSSHKeys(ctx context.Context) ([]SSHAuthorizedKey, error) {
	payload, err := runLinodeCLIValueFn([]string{"sshkeys", "list", "--json"})
	if err != nil {
		return nil, err
	}
	return parseLinodeSSHKeysPayload(payload)
}

func (legacyTestProvider) FindServerByLabel(ctx context.Context, label string) (*CreatedServer, error) {
	payload, err := runLinodeCLIValueFn([]string{"linodes", "list", "--json"})
	if err != nil {
		return nil, err
	}
	items, _ := payload.([]any)
	for _, item := range items {
		record, _ := item.(map[string]any)
		if record == nil || valueString(record["label"]) != label {
			continue
		}
		id := valueString(record["id"])
		if id == "" {
			id = valueString(record["instance_id"])
		}
		return &CreatedServer{Provider: "linode", ProviderID: id, Name: label, Hostname: label, IPv4: linodeIPFromRecord(record), Region: valueString(record["region"]), Type: valueString(record["type"]), Image: valueString(record["image"]), Tags: recordStringList(record["tags"])}, nil
	}
	return nil, nil
}

func (legacyTestProvider) CreateServer(ctx context.Context, plan ServerCreatePlan) (*CreatedServer, error) {
	args := []string{"linodes", "create", "--region", plan.Plan.Region, "--type", plan.Plan.LinodeType, "--image", plan.Plan.Image, "--label", plan.Plan.Label, "--root_pass", plan.RootPass}
	args = appendLinodeAuthorizedKeyArgs(args, plan.SSHKeys)
	args = append(args, "--metadata.user_data", base64UserData(plan.UserData))
	payload, err := runLinodeCLICommand(args)
	if err != nil {
		return nil, err
	}
	id, err := apiIDString(payload["id"])
	if err != nil {
		return nil, err
	}
	return &CreatedServer{Provider: "linode", ProviderID: id, Name: plan.Plan.Name, Hostname: plan.Plan.Hostname, IPv4: linodeIPFromRecord(payload), Tags: []string{"nf"}}, nil
}

func (legacyTestProvider) EnsureFirewall(ctx context.Context, plan FirewallPlan) (*FirewallResult, error) {
	rulesJSON, _ := firewallRulesJSON()
	if plan.ID != "" {
		if _, err := runLinodeCLICommand([]string{"firewalls", "rules-update", plan.ID, "--inbound", rulesJSON, "--inbound_policy", firstNonEmpty(plan.InboundPolicy, firewallInboundPolicy)}); err != nil {
			return &FirewallResult{ID: plan.ID, DeviceID: plan.DeviceID}, err
		}
		return &FirewallResult{ID: plan.ID, DeviceID: plan.DeviceID}, nil
	}
	if payload, err := runLinodeCLIValueFn([]string{"firewalls", "list", "--json"}); err == nil {
		if items, ok := payload.([]any); ok {
			for _, item := range items {
				firewall, _ := item.(map[string]any)
				if firewall != nil && valueString(firewall["label"]) == firstNonEmpty(plan.Label, firewallManagedLabel) {
					id, _ := apiIDString(firewall["id"])
					if _, err := runLinodeCLICommand([]string{"firewalls", "rules-update", id, "--inbound", rulesJSON, "--inbound_policy", firstNonEmpty(plan.InboundPolicy, firewallInboundPolicy)}); err != nil {
						return &FirewallResult{ID: id, DeviceID: plan.DeviceID}, err
					}
					return &FirewallResult{ID: id, DeviceID: plan.DeviceID}, nil
				}
			}
		}
	}
	payload, err := runLinodeCLICommand([]string{"firewalls", "create", "--label", firstNonEmpty(plan.Label, firewallManagedLabel), "--rules.inbound_policy", firstNonEmpty(plan.InboundPolicy, firewallInboundPolicy), "--rules.outbound_policy", firstNonEmpty(plan.OutboundPolicy, firewallOutboundPolicy)})
	if err != nil {
		return nil, err
	}
	id, err := apiIDString(payload["id"])
	if err != nil {
		return nil, err
	}
	if _, err := runLinodeCLICommand([]string{"firewalls", "rules-update", id, "--inbound", rulesJSON, "--inbound_policy", firstNonEmpty(plan.InboundPolicy, firewallInboundPolicy)}); err != nil {
		return &FirewallResult{ID: id, DeviceID: plan.DeviceID}, err
	}
	return &FirewallResult{ID: id, DeviceID: plan.DeviceID}, nil
}

func (legacyTestProvider) AssignFirewall(ctx context.Context, firewallID, serverID string) error {
	_, err := runLinodeCLICommand([]string{"firewalls", "device-create", "--type", "linode", "--id", serverID, firewallID})
	return err
}

func init() {
	serverProviderFactory = func(plan Plan) (ServerProvider, error) { return legacyTestProvider{}, nil }
}

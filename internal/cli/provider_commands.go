package cli

// Provider status, health checks, and target discovery.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dnsimple/dnsimple-go/v9/dnsimple"
	"github.com/linode/linodego"
	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/state"
	"github.com/nonfiction/nf/internal/ui"
)

func runProvider(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runProviderHelp()
	}
	argv[0] = cliCommandAlias(argv[0])
	switch argv[0] {
	case "list":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "provider list takes no arguments")
			return 1
		}
		return cmdProviderList()
	case "show":
		name, jsonOutput, err := parseProviderActionArgs(argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if name == "" {
			selected, err := chooseProvider("show")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			name = selected
		}
		return cmdProviderShow(name, jsonOutput)
	case "check":
		name, jsonOutput, err := parseProviderActionArgs(argv[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if name == "" {
			selected, err := chooseProvider("check")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			name = selected
		}
		return cmdProviderCheck(name, jsonOutput)
	default:
		fmt.Fprintln(os.Stderr, "unsupported provider command")
		return 1
	}
}

func parseProviderActionArgs(argv []string) (string, bool, error) {
	var name string
	jsonOutput := false
	for _, arg := range argv {
		switch arg {
		case "--json":
			jsonOutput = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, fmt.Errorf("unsupported flag %s", arg)
			}
			if name != "" {
				return "", false, fmt.Errorf("provider command takes at most one provider")
			}
			name = arg
		}
	}
	return name, jsonOutput, nil
}

func chooseProvider(action string) (string, error) {
	statuses := providerConfigStatuses()
	options := make([]ui.SelectOption, 0, len(statuses))
	for _, status := range statuses {
		options = append(options, ui.SelectOption{Value: status.Name, Label: status.Name})
	}
	if len(options) == 0 {
		return "", ProjectError{Msg: "No selectable providers found."}
	}
	return providerSelectFn(fmt.Sprintf("Choose a provider to %s", action), options)
}

type providerConfigKey struct {
	Keys     []string
	Required bool
	Default  string
	Secret   bool
}

type providerConfigStatus struct {
	Name    string
	Keys    []providerConfigKey
	Missing []string
	Values  map[string]string
}

type providerHealthResult struct {
	Provider string
	Details  map[string]string
	Record   map[string]any
}

func providerConfigStatuses() []providerConfigStatus {
	return []providerConfigStatus{
		providerConfigStatusFor("dnsimple", []providerConfigKey{
			{Keys: []string{"base_domain"}, Required: true},
			{Keys: []string{"DNSIMPLE_TOKEN"}, Required: true, Secret: true},
			{Keys: []string{"dnsimple_account_id"}},
		}),
		providerConfigStatusFor("kinsta", []providerConfigKey{
			{Keys: []string{"KINSTA_API_KEY"}, Required: true, Secret: true},
		}),
		providerConfigStatusFor("linode", []providerConfigKey{
			{Keys: []string{"LINODE_TOKEN", "LINODE_CLI_TOKEN"}, Required: true, Secret: true},
		}),
	}
}

func providerConfigStatusFor(name string, keys []providerConfigKey) providerConfigStatus {
	status := providerConfigStatus{Name: name, Keys: keys, Values: map[string]string{}}
	for _, group := range keys {
		value := ""
		for _, key := range group.Keys {
			if v := providerConfigValue(key); v != "" {
				value = v
				status.Values[key] = v
				break
			}
		}
		if value == "" && group.Default != "" {
			value = group.Default
			status.Values[group.Keys[0]] = group.Default
		}
		if value == "" && group.Required {
			status.Missing = append(status.Missing, strings.Join(group.Keys, " or "))
		}
	}
	return status
}

func providerConfigValue(key string) string {
	switch key {
	case "base_domain":
		return baseDomainValue()
	case "dnsimple_account_id":
		return dnsimpleAccountIDValue()
	default:
		return envwizard.Value(key)
	}
}

func providerConfigStatusByName(name string) (providerConfigStatus, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, status := range providerConfigStatuses() {
		if status.Name == name {
			return status, true
		}
	}
	return providerConfigStatus{}, false
}

func providerStatusLabel(status providerConfigStatus) string {
	if len(status.Missing) == 0 {
		return "configured"
	}
	return "missing"
}

func providerMissingLabel(status providerConfigStatus) string {
	if len(status.Missing) == 0 {
		return "-"
	}
	return strings.Join(status.Missing, ", ")
}

func providerValueLabel(status providerConfigStatus, group providerConfigKey) string {
	for _, key := range group.Keys {
		if value := strings.TrimSpace(status.Values[key]); value != "" {
			if group.Default != "" && value == group.Default && envwizard.Value(key) == "" {
				return value + " (default)"
			}
			if !group.Secret {
				return value
			}
			return maskSecret(value)
		}
	}
	return "unset"
}

func cmdProviderList() int {
	rows := [][]string{{"provider", "status", "missing"}}
	for _, status := range providerConfigStatuses() {
		rows = append(rows, []string{status.Name, providerStatusLabel(status), providerMissingLabel(status)})
	}
	fmt.Println(formatTable(rows))
	return 0
}

func cmdProviderShow(name string, jsonOutput bool) int {
	status, ok := providerConfigStatusByName(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "unsupported provider %q\n", name)
		return 1
	}
	records, err := state.LoadStateRecords("providers")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	record := providerRecordByName(records, status.Name)
	if record == nil {
		fmt.Fprintf(os.Stderr, "No cached provider metadata matched %q. Run nf provider check %s.\n", name, status.Name)
		return 1
	}
	if jsonOutput {
		data, _ := json.MarshalIndent(record, "", "  ")
		fmt.Println(string(data))
		return 0
	}
	printProviderDetails(status, record)
	return 0
}

func printProviderDetails(status providerConfigStatus, record map[string]any) {
	fmt.Printf("Provider: %s\n", status.Name)
	fmt.Printf("Status: %s\n", providerStatusLabel(status))
	if len(status.Missing) > 0 {
		fmt.Printf("Missing: %s\n", providerMissingLabel(status))
	}
	fmt.Printf("Cache: %s\n", state.StatePath("providers"))
	if checkedAt := recordValueString(record["checked_at"]); checkedAt != "" {
		fmt.Printf("Checked at: %s\n", checkedAt)
	}
	for _, field := range []struct {
		Label string
		Keys  []string
	}{
		{Label: "Account ID", Keys: []string{"account_id"}},
		{Label: "Account email", Keys: []string{"account_email", "email"}},
		{Label: "Username", Keys: []string{"username", "user"}},
		{Label: "Company ID", Keys: []string{"company", "company_id"}},
		{Label: "Provider status", Keys: []string{"status"}},
	} {
		if value := firstRecordString(record, field.Keys...); value != "" {
			fmt.Printf("%s: %s\n", field.Label, value)
		}
	}
	targets := targetMaps(record["targets"])
	fmt.Printf("Targets: %d\n", len(targets))
	for _, target := range targets {
		name := firstRecordString(target, "name", "label", "id")
		if name == "" {
			continue
		}
		status := firstRecordString(target, "status", "phase")
		if status != "" {
			fmt.Printf("  - %s (%s)\n", name, status)
			continue
		}
		fmt.Printf("  - %s\n", name)
	}
}

func providerRecordByName(records []map[string]any, name string) map[string]any {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, record := range records {
		for _, key := range []string{"provider", "_state_key", "name", "id"} {
			if strings.ToLower(strings.TrimSpace(recordValueString(record[key]))) == name {
				return record
			}
		}
	}
	return nil
}

func cmdProviderCheck(name string, jsonOutput bool) int {
	status, ok := providerConfigStatusByName(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "unsupported provider %q\n", name)
		return 1
	}
	if len(status.Missing) > 0 {
		fmt.Printf("Provider %s preflight failed.\n", status.Name)
		fmt.Printf("Missing: %s\n", providerMissingLabel(status))
		fmt.Printf("Set values in the environment or %s.\n", config.EnvFile())
		fmt.Println("No remote API call was made.")
		return 1
	}
	result, err := runProviderHealthcheck(status.Name)
	if err != nil {
		fmt.Printf("Provider %s healthcheck failed.\n", status.Name)
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if result.Record == nil {
		result.Record = map[string]any{}
	}
	if err := saveProviderHealthRecord(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if jsonOutput {
		data, _ := json.MarshalIndent(result.Record, "", "  ")
		fmt.Println(string(data))
		return 0
	}
	fmt.Printf("Provider %s healthcheck passed.\n", status.Name)
	for _, line := range providerHealthDetailLines(result.Details) {
		fmt.Println(line)
	}
	fmt.Printf("Saved provider metadata to %s.\n", state.StatePath("providers"))
	return 0
}

func runProviderHealthcheck(provider string) (providerHealthResult, error) {
	switch provider {
	case "dnsimple":
		return providerCheckDNSimpleFn()
	case "kinsta":
		return providerCheckKinstaFn()
	case "linode":
		return providerCheckLinodeFn()
	default:
		return providerHealthResult{}, fmt.Errorf("unsupported provider %q", provider)
	}
}

func providerHealthDetailLines(details map[string]string) []string {
	keys := make([]string, 0, len(details))
	for key := range details {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s: %s", key, details[key]))
	}
	return lines
}

func saveProviderHealthRecord(result providerHealthResult) error {
	record := result.Record
	if record == nil {
		record = map[string]any{}
	}
	record["provider"] = result.Provider
	record["checked_at"] = time.Now().UTC().Format(time.RFC3339)
	if _, ok := record["targets"]; !ok {
		record["targets"] = []map[string]any{}
	}
	records, err := state.LoadStateRecords("providers")
	if err != nil {
		return err
	}
	replaced := false
	for i, existing := range records {
		if strings.EqualFold(recordValueString(existing["provider"]), result.Provider) || strings.EqualFold(recordValueString(existing["_state_key"]), result.Provider) {
			records[i] = record
			replaced = true
			break
		}
	}
	if !replaced {
		records = append(records, record)
	}
	return state.SaveStateRecords("providers", records)
}

func providerHealthContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

func checkDNSimpleProvider() (providerHealthResult, error) {
	token := envwizard.Value("DNSIMPLE_TOKEN")
	if token == "" {
		return providerHealthResult{}, fmt.Errorf("Expected DNSIMPLE_TOKEN in the environment or %s.", config.EnvFile())
	}
	managedDomain := baseDomainValue()
	if managedDomain == "" {
		return providerHealthResult{}, fmt.Errorf("Expected base_domain in %s. Set it with nf config set-base-domain <domain>.", config.ConfigFile())
	}
	ctx, cancel := providerHealthContext()
	defer cancel()
	client := dnsimple.NewClient(dnsimple.StaticTokenHTTPClient(ctx, token))
	if baseURL := envwizard.Value("DNSIMPLE_BASE_URL"); baseURL != "" {
		client.BaseURL = baseURL
	}
	resp, err := client.Identity.Whoami(ctx)
	if err != nil {
		return providerHealthResult{}, err
	}
	if resp == nil || resp.Data == nil || resp.Data.Account == nil || resp.Data.Account.ID == 0 {
		return providerHealthResult{}, fmt.Errorf("DNSimple /v2/whoami did not return an account id")
	}
	accountID := strconv.FormatInt(resp.Data.Account.ID, 10)
	zoneResp, err := client.Zones.GetZone(ctx, accountID, managedDomain)
	if err != nil {
		var apiErr *dnsimple.ErrorResponse
		if errors.As(err, &apiErr) && apiErr.HTTPResponse != nil && apiErr.HTTPResponse.StatusCode == http.StatusNotFound {
			return providerHealthResult{}, fmt.Errorf("DNSimple zone %s was not found for account %s. Check base_domain and DNSIMPLE_TOKEN", managedDomain, accountID)
		}
		return providerHealthResult{}, fmt.Errorf("Checking DNSimple zone %s for account %s: %v", managedDomain, accountID, err)
	}
	if zoneResp == nil || zoneResp.Data == nil || strings.TrimSpace(zoneResp.Data.Name) == "" {
		return providerHealthResult{}, fmt.Errorf("DNSimple zone %s did not return zone metadata", managedDomain)
	}
	details := map[string]string{"account_id": accountID, "managed_domain": managedDomain}
	if resp.Data.Account.Email != "" {
		details["account_email"] = resp.Data.Account.Email
	}
	if resp.Data.Account.Name != "" {
		details["account_name"] = resp.Data.Account.Name
	}
	details["zone_id"] = strconv.FormatInt(zoneResp.Data.ID, 10)
	record := map[string]any{
		"provider":       "dnsimple",
		"account_id":     accountID,
		"managed_domain": managedDomain,
		"zone_id":        strconv.FormatInt(zoneResp.Data.ID, 10),
		"zone_active":    zoneResp.Data.Active,
		"targets":        []map[string]any{},
	}
	if resp.Data.Account.Email != "" {
		record["account_email"] = resp.Data.Account.Email
	}
	if resp.Data.Account.Name != "" {
		record["account_name"] = resp.Data.Account.Name
	}
	if values, err := loadGlobalConfig(); err == nil {
		values["dnsimple_account_id"] = accountID
		if err := saveGlobalConfig(values); err != nil {
			return providerHealthResult{}, err
		}
	} else {
		return providerHealthResult{}, err
	}
	return providerHealthResult{Provider: "dnsimple", Details: details, Record: record}, nil
}

func baseDomainValue() string {
	values, err := loadGlobalConfig()
	if err == nil {
		if value := strings.TrimSpace(values["base_domain"]); value != "" {
			return value
		}
	}
	return firstNonEmpty(envwizard.Value("NF_SERVER_DOMAIN"), envwizard.Value("DNSIMPLE_ZONE_NAME"))
}

func dnsimpleAccountIDValue() string {
	values, err := loadGlobalConfig()
	if err == nil {
		return strings.TrimSpace(values["dnsimple_account_id"])
	}
	return ""
}

type kinstaValidateResponse struct {
	Name      string  `json:"name"`
	ExpiresAt *string `json:"expires_at"`
	Company   string  `json:"company"`
	Status    string  `json:"status"`
}

func checkKinstaProvider() (providerHealthResult, error) {
	token := envwizard.Value("KINSTA_API_KEY")
	if token == "" {
		return providerHealthResult{}, fmt.Errorf("Expected KINSTA_API_KEY in the environment or %s.", config.EnvFile())
	}
	ctx, cancel := providerHealthContext()
	defer cancel()
	baseURL := firstNonEmpty(envwizard.Value("KINSTA_BASE_URL"), "https://api.kinsta.com/v2")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/validate", nil)
	if err != nil {
		return providerHealthResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return providerHealthResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return providerHealthResult{}, fmt.Errorf("Kinsta /v2/validate returned %s", resp.Status)
	}
	var payload kinstaValidateResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return providerHealthResult{}, err
	}
	company := strings.TrimSpace(payload.Company)
	if company == "" {
		return providerHealthResult{}, fmt.Errorf("Kinsta /v2/validate did not return a company uuid")
	}
	details := map[string]string{"company_id": company}
	if payload.Name != "" {
		details["api_key_name"] = payload.Name
	}
	if payload.Status != "" {
		details["status"] = payload.Status
	}
	if payload.ExpiresAt != nil && strings.TrimSpace(*payload.ExpiresAt) != "" {
		details["expires_at"] = strings.TrimSpace(*payload.ExpiresAt)
	}
	targetStatus := firstNonEmpty(strings.TrimSpace(payload.Status), "active")
	record := map[string]any{
		"provider":   "kinsta",
		"company_id": company,
		"targets": []map[string]any{{
			"id":         "kinsta",
			"name":       "kinsta",
			"provider":   "kinsta",
			"company_id": company,
			"status":     targetStatus,
		}},
	}
	if payload.Name != "" {
		record["api_key_name"] = payload.Name
	}
	if payload.Status != "" {
		record["status"] = payload.Status
	}
	if payload.ExpiresAt != nil && strings.TrimSpace(*payload.ExpiresAt) != "" {
		record["expires_at"] = strings.TrimSpace(*payload.ExpiresAt)
	}
	return providerHealthResult{Provider: "kinsta", Details: details, Record: record}, nil
}

func checkLinodeProvider() (providerHealthResult, error) {
	token, err := linodeTokenEnv()
	if err != nil {
		return providerHealthResult{}, err
	}
	ctx, cancel := providerHealthContext()
	defer cancel()
	client := linodego.NewClient(nil)
	client.SetToken(token)
	profile, err := client.GetProfile(ctx)
	if err != nil {
		return providerHealthResult{}, err
	}
	if profile == nil || strings.TrimSpace(profile.Username) == "" {
		return providerHealthResult{}, fmt.Errorf("Linode profile endpoint did not return a username")
	}
	details := map[string]string{"username": profile.Username}
	if profile.Email != "" {
		details["email"] = profile.Email
	}
	details["restricted"] = strconv.FormatBool(profile.Restricted)
	instances, err := client.ListInstances(ctx, nil)
	if err != nil {
		return providerHealthResult{}, err
	}
	targets := make([]map[string]any, 0)
	for _, instance := range instances {
		if !linodeInstanceHasTag(instance, "nf") {
			continue
		}
		targets = append(targets, linodeInstanceTargetRecord(instance))
	}
	details["targets"] = strconv.Itoa(len(targets))
	record := map[string]any{
		"provider":   "linode",
		"username":   profile.Username,
		"restricted": profile.Restricted,
		"targets":    targets,
	}
	if profile.Email != "" {
		record["email"] = profile.Email
	}
	return providerHealthResult{Provider: "linode", Details: details, Record: record}, nil
}

func linodeInstanceHasTag(instance linodego.Instance, tag string) bool {
	for _, candidate := range instance.Tags {
		if strings.EqualFold(strings.TrimSpace(candidate), tag) {
			return true
		}
	}
	return false
}

func linodeInstanceTargetRecord(instance linodego.Instance) map[string]any {
	record := map[string]any{
		"id":       strconv.Itoa(instance.ID),
		"name":     instance.Label,
		"provider": "linode",
		"region":   instance.Region,
		"status":   string(instance.Status),
		"tags":     instance.Tags,
	}
	if len(instance.IPv4) > 0 && instance.IPv4[0] != nil {
		record["ipv4"] = instance.IPv4[0].String()
	}
	if hostname := linodeInstanceHostname(instance.Label); hostname != "" {
		record["hostname"] = hostname
		ssh := map[string]any{"host": hostname}
		if user := linodeDefaultSSHUser(); user != "" {
			ssh["user"] = user
		}
		record["ssh"] = ssh
	}
	return record
}

func linodeInstanceHostname(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	if strings.Contains(label, ".") {
		return strings.TrimSuffix(label, ".")
	}
	domain := baseDomainValue()
	if domain == "" {
		return ""
	}
	return label + "." + domain
}

func linodeDefaultSSHUser() string {
	values, err := loadGlobalConfig()
	if err == nil {
		if value := strings.TrimSpace(values["linode_default_user"]); value != "" {
			return value
		}
	}
	return "nonfiction"
}

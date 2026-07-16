package cli

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/kinsta"
)

type siteDomainExpectedDNSRecord struct {
	Domain string
	Type   string
	Name   string
	Value  string
	Values []string
}

type siteDomainHTTPCheckResult struct {
	StatusCode int
	Location   string
	Error      string
}

type siteDomainTLSCheckResult struct {
	OK       bool
	NotAfter time.Time
	Issuer   string
	Error    string
}

type siteDomainOutputField struct {
	Key   string
	Value string
}

type siteDomainDNSCheckResult struct {
	OK     bool
	Result string
	Detail string
}

type siteDomainProviderCheck struct {
	Ready                     bool
	Primary                   bool
	DNSRecords                []siteDomainExpectedDNSRecord
	Description               []string
	KinstaVerificationPending bool
	KinstaPointingPending     bool
}

type siteDomainIPRangeSet struct {
	Prefixes []netip.Prefix
	Source   string
	Warning  string
}

type siteDomainReadinessResult struct {
	Ready    bool
	Primary  bool
	NextStep string
}

const siteDomainOutputWidth = 88

func siteDomainField(key, value string) siteDomainOutputField {
	return siteDomainOutputField{Key: key, Value: value}
}

func siteDomainStatus(ok bool) string {
	if ok {
		return "ok"
	}
	return "pending"
}

func printSiteDomainField(indent int, key, value string) {
	for _, line := range siteDomainWrappedFieldLines(indent, key, value) {
		fmt.Println(line)
	}
}

func printSiteDomainStatusBlock(status, subject string, fields ...siteDomainOutputField) {
	for _, line := range siteDomainStatusBlockLines(status, subject, fields...) {
		fmt.Println(line)
	}
}

func siteDomainStatusBlockLines(status, subject string, fields ...siteDomainOutputField) []string {
	lines := append([]string{}, siteDomainWrapWithPrefix("  ["+status+"] ", strings.TrimSpace(subject))...)
	for _, field := range fields {
		lines = append(lines, siteDomainWrappedFieldLines(4, field.Key, field.Value)...)
	}
	return lines
}

func siteDomainWrappedFieldLines(indent int, key, value string) []string {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return nil
	}
	prefix := strings.Repeat(" ", indent) + key + ": "
	return siteDomainWrapWithPrefix(prefix, value)
}

func siteDomainWrapWithPrefix(prefix, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return []string{prefix}
	}
	limit := siteDomainOutputWidth - len(prefix)
	if limit < 20 {
		limit = 20
	}
	parts := siteDomainWrapText(value, limit)
	lines := make([]string, 0, len(parts))
	continuation := strings.Repeat(" ", len(prefix))
	for i, part := range parts {
		if i == 0 {
			lines = append(lines, prefix+part)
		} else {
			lines = append(lines, continuation+part)
		}
	}
	return lines
}

func siteDomainWrapText(value string, limit int) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return []string{""}
	}
	lines := []string{}
	for len(value) > limit {
		cut := strings.LastIndex(value[:limit+1], " ")
		if cut <= 0 {
			cut = limit
		}
		lines = append(lines, strings.TrimSpace(value[:cut]))
		value = strings.TrimSpace(value[cut:])
	}
	if value != "" {
		lines = append(lines, value)
	}
	return lines
}

func siteDomainErrorSummary(detail string) string {
	lower := strings.ToLower(detail)
	switch {
	case strings.Contains(lower, "no such host") || strings.Contains(lower, "not found"):
		return "lookup failed"
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded"):
		return "timed out"
	case strings.Contains(lower, "certificate") || strings.Contains(lower, "tls"):
		return "TLS failed"
	default:
		return "request failed"
	}
}

func cmdSiteDomainCheck(plan siteDomainPlan) int {
	result, err := printSiteDomainReadinessCheck(plan)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	fmt.Println()
	printSiteDomainCheckNextStep(plan, result)
	if result.Ready {
		fmt.Println("Overall: ready")
		return 0
	}
	fmt.Println("Overall: pending")
	return 2
}

func printSiteDomainReadinessCheck(plan siteDomainPlan) (siteDomainReadinessResult, error) {
	printSiteDomainCheckHeader(plan)
	providerCheck, err := checkSiteDomainProvider(plan)
	if err != nil {
		return siteDomainReadinessResult{}, err
	}
	fmt.Println()
	for _, line := range providerCheck.Description {
		fmt.Println(line)
	}
	fmt.Println()
	dnsReady := printSiteDomainDNSCheck(plan, providerCheck.DNSRecords)
	fmt.Println()
	httpReady := printSiteDomainHTTPChecks(plan)
	fmt.Println()
	tlsReady := printSiteDomainTLSChecks(plan)
	originTLSReady := true
	if siteDomainCloudflareStrict(plan) {
		fmt.Println()
		originTLSReady = printSiteDomainOriginTLSChecks(plan)
	}
	ready := providerCheck.Ready && dnsReady && httpReady && tlsReady && originTLSReady
	nextStep := ""
	if !ready && plan.Provider == "kinsta" {
		if providerCheck.KinstaVerificationPending {
			nextStep = "create or fix Kinsta verification DNS records, then run nf domain check again"
		} else if providerCheck.KinstaPointingPending {
			if dnsReady {
				nextStep = "wait for Kinsta to detect domain pointing, then run nf domain check again"
			} else {
				nextStep = "point public DNS records at Kinsta, then run nf domain check again"
			}
		} else if !dnsReady {
			nextStep = "point public DNS records at Kinsta, then run nf domain check again"
		}
	}
	return siteDomainReadinessResult{Ready: ready, Primary: providerCheck.Primary, NextStep: nextStep}, nil
}

func printSiteDomainCheckNextStep(plan siteDomainPlan, result siteDomainReadinessResult) {
	fmt.Println("Next step")
	if result.Ready && result.Primary {
		fmt.Println("  domain is primary and public checks passed")
	} else if result.Ready {
		domains := plan.allDomains()
		if len(domains) == 1 && cachedExternalPlanDomainRole(plan, domains[0]) == "secondary" {
			fmt.Println("  domain is ready as a secondary redirect")
			printSiteDomainField(2, "optional primary promotion", fmt.Sprintf("nf domain primary %s %s%s", plan.EnvID, plan.Canonical, siteDomainProxyArg(plan.ProxyMode)))
		} else if allPlanDomainsCachedExternal(plan) {
			if len(domains) == 1 {
				fmt.Println("  domain is ready in its current role")
			} else {
				fmt.Println("  domains are ready in their current roles")
			}
		} else {
			fmt.Println("  domain checks passed")
		}
	} else if strings.TrimSpace(result.NextStep) != "" {
		fmt.Printf("  %s\n", result.NextStep)
	} else {
		fmt.Println("  wait for pending checks, then run nf domain check again")
	}
}

func allPlanDomainsCachedExternal(plan siteDomainPlan) bool {
	domains := plan.allDomains()
	if len(domains) == 0 {
		return false
	}
	for _, domain := range domains {
		if !recordHasCachedExternalSiteDomain(plan.Record, domain) {
			return false
		}
	}
	return true
}

func cachedExternalPlanDomainRole(plan siteDomainPlan, domain string) string {
	needle := normalizeDomainName(domain)
	for _, cached := range siteDomainListDomains(plan.Record) {
		if cached.name == needle && cached.management == "external" {
			return cached.role
		}
	}
	return ""
}

func siteDomainProxyArg(proxyMode string) string {
	if proxyMode == "" {
		return ""
	}
	return " --proxy " + displaySiteDomainProxyMode(proxyMode)
}

func printSiteDomainCheckHeader(plan siteDomainPlan) {
	fmt.Println("Public domain check")
	printSiteDomainField(2, "env", plan.EnvID)
	printSiteDomainField(2, "provider", plan.Provider)
	if plan.Target.TargetRef != "" {
		printSiteDomainField(2, "target", plan.Target.TargetRef)
	}
	if plan.CurrentURL != "" {
		printSiteDomainField(2, "current", plan.CurrentURL)
	}
	if plan.InternalURL != "" {
		printSiteDomainField(2, "fallback", plan.InternalURL)
	}
	if plan.Primary {
		printSiteDomainField(2, "primary", plan.Canonical)
		if len(plan.Aliases) > 0 {
			printSiteDomainField(2, "secondary", strings.Join(plan.Aliases, ", "))
		}
	} else if len(plan.allDomains()) == 1 {
		printSiteDomainField(2, "domain", plan.allDomains()[0])
	} else {
		printSiteDomainField(2, "domains", strings.Join(plan.allDomains(), ", "))
	}
	if plan.ProxyMode != "" {
		printSiteDomainField(2, "proxy", displaySiteDomainProxyMode(plan.ProxyMode))
	} else if plan.Provider == "linode" {
		printSiteDomainField(2, "proxy", "none")
	}
}

func checkSiteDomainProvider(plan siteDomainPlan) (siteDomainProviderCheck, error) {
	switch plan.Provider {
	case "kinsta":
		return checkKinstaSiteDomainProvider(plan)
	case "linode":
		return checkLinodeSiteDomainProvider(plan), nil
	default:
		return siteDomainProviderCheck{}, ProjectError{Msg: fmt.Sprintf("domain check is not implemented for provider %q", plan.Provider)}
	}
}

func checkKinstaSiteDomainProvider(plan siteDomainPlan) (siteDomainProviderCheck, error) {
	token := envwizard.Value("KINSTA_API_KEY")
	if token == "" {
		return siteDomainProviderCheck{}, fmt.Errorf("Expected KINSTA_API_KEY in the environment or %s.", config.EnvFile())
	}
	client := newKinstaClient(token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	domains, err := client.ListDomains(ctx, plan.KinstaEnvID)
	if err != nil {
		return siteDomainProviderCheck{}, err
	}
	out := siteDomainProviderCheck{Ready: true, Description: []string{"Kinsta"}}
	for i, name := range plan.allDomains() {
		role := "secondary"
		if plan.Primary && i == 0 {
			role = "primary"
		}
		domain, ok := kinsta.FindDomain(domains, name)
		if !ok {
			out.Ready = false
			out.Description = append(out.Description, siteDomainStatusBlockLines("pending", name,
				siteDomainField("role", role),
				siteDomainField("status", "missing"),
			)...)
			continue
		}
		if i == 0 && domain.IsPrimary {
			out.Primary = true
		}
		if domain.IsPrimary {
			role = "primary"
		}
		records, recordsErr := kinstaDomainRecords(ctx, client, domain.ID)
		verificationLabel, verificationReady, verificationKnown := kinstaDomainVerificationState(domain)
		verificationDetail := ""
		if !verificationKnown && strings.TrimSpace(domain.ID) != "" {
			validation, err := client.ValidateDomainVerification(ctx, domain.ID)
			if err != nil {
				verificationDetail = fmt.Sprintf(" (Kinsta validation check unavailable: %v)", err)
			} else if validationLabel, validationReady, validationKnown := kinstaDomainVerificationValidationState(validation); validationKnown {
				verificationLabel = validationLabel
				verificationReady = validationReady
				verificationKnown = true
			}
		}
		fields := []siteDomainOutputField{
			siteDomainField("role", role),
			siteDomainField("status", "present"),
			siteDomainField("verification", verificationLabel),
		}
		if domain.IsPrimary {
			fields = append(fields, siteDomainField("primary", "yes"))
		}
		if recordsErr != nil {
			fields = append(fields,
				siteDomainField("provider DNS records", "unavailable"),
				siteDomainField("detail", recordsErr.Error()),
			)
		}
		if verificationDetail != "" {
			fields = append(fields, siteDomainField("verification detail", strings.Trim(verificationDetail, " ()")))
		}
		if verificationKnown && !verificationReady {
			out.Ready = false
			out.KinstaVerificationPending = true
		}
		routingLabel, routingReady, routingKnown := kinstaDomainPointingState(domain)
		expectations := kinstaDNSExpectations(name, records)
		routingEvidence := true
		if plan.Primary {
			expectations, routingEvidence = kinstaPrimaryRoutingExpectations(ctx, client, plan, domains, domain, records)
			if routingKnown && routingReady {
				routingEvidence = true
			}
			if !routingEvidence && !(routingKnown && routingReady) {
				out.Ready = false
				out.KinstaPointingPending = true
				routingLabel = "pending"
				routingKnown = true
			}
		}
		fields = append(fields, siteDomainField("routing", kinstaDomainRoutingDescription(routingLabel, routingKnown)))
		if routingKnown && !routingReady {
			out.Ready = false
			out.KinstaPointingPending = true
		} else if !routingKnown && len(records.Pointing) > 0 {
			out.KinstaPointingPending = true
		}
		providerStatus := "ok"
		if (verificationKnown && !verificationReady) || (routingKnown && !routingReady) || !routingEvidence {
			providerStatus = "pending"
		}
		out.Description = append(out.Description, siteDomainStatusBlockLines(providerStatus, name, fields...)...)
		out.DNSRecords = append(out.DNSRecords, expectations...)
	}
	return out, nil
}

type kinstaOwnedDomainRecords struct {
	Domain  string
	Records kinsta.DomainRecords
}

func kinstaPrimaryRoutingExpectations(ctx context.Context, client *kinsta.Client, plan siteDomainPlan, domains []kinsta.Domain, selected kinsta.Domain, selectedRecords kinsta.DomainRecords) ([]siteDomainExpectedDNSRecord, bool) {
	requested := normalizeDomainName(domainName(selected))
	owned := []kinstaOwnedDomainRecords{{Domain: requested, Records: selectedRecords}}
	if expectations, complete := kinstaRoutingExpectations(requested, owned); complete {
		return expectations, true
	}
	for _, owner := range kinstaRoutingRecordOwners(domains, requested) {
		if sameKinstaDomain(owner, selected) {
			continue
		}
		records, err := kinstaDomainRecords(ctx, client, owner.ID)
		if err != nil {
			continue
		}
		owned = append(owned, kinstaOwnedDomainRecords{Domain: domainName(owner), Records: records})
		if expectations, complete := kinstaRoutingExpectations(requested, owned); complete {
			return expectations, true
		}
	}
	if normalizeDomainName(plan.InternalHostname) == requested {
		if expectation, ok := kinstaInternalRoutingExpectation(requested, domains); ok {
			return []siteDomainExpectedDNSRecord{expectation}, true
		}
	}
	return nil, false
}

func kinstaRoutingRecordOwners(domains []kinsta.Domain, requested string) []kinsta.Domain {
	requested = normalizeDomainName(requested)
	owners := []kinsta.Domain{}
	for _, domain := range domains {
		name := normalizeDomainName(domainName(domain))
		if name == "" || name == requested || !strings.HasSuffix(requested, "."+name) {
			continue
		}
		owners = append(owners, domain)
	}
	sort.SliceStable(owners, func(i, j int) bool {
		return len(normalizeDomainName(domainName(owners[i]))) > len(normalizeDomainName(domainName(owners[j])))
	})
	return owners
}

func kinstaRoutingExpectations(requested string, owned []kinstaOwnedDomainRecords) ([]siteDomainExpectedDNSRecord, bool) {
	requested = normalizeDomainName(requested)
	byName := map[string][]siteDomainExpectedDNSRecord{}
	for _, owner := range owned {
		ownerName := normalizeDomainName(owner.Domain)
		for _, record := range owner.Records.Pointing {
			recordType := strings.ToUpper(strings.TrimSpace(record.RecordTypeName()))
			if recordType != "A" && recordType != "AAAA" && recordType != "CNAME" {
				continue
			}
			name := kinstaRoutingRecordName(ownerName, record.RecordName())
			value := strings.TrimSpace(record.RecordContent())
			if name == "" || value == "" {
				continue
			}
			if recordType == "CNAME" {
				value = kinstaRoutingRecordName(ownerName, value)
			}
			byName[name] = append(byName[name], siteDomainExpectedDNSRecord{Domain: requested, Type: recordType, Name: name, Value: value})
		}
	}

	expectations := []siteDomainExpectedDNSRecord{}
	seenRecords := map[string]bool{}
	visiting := map[string]bool{}
	var walk func(string) bool
	walk = func(name string) bool {
		name = normalizeDomainName(name)
		if name == "" || visiting[name] {
			return false
		}
		visiting[name] = true
		defer delete(visiting, name)
		complete := false
		for _, record := range byName[name] {
			key := record.Type + "\x00" + record.Name + "\x00" + record.Value
			if !seenRecords[key] {
				expectations = append(expectations, record)
				seenRecords[key] = true
			}
			switch record.Type {
			case "A", "AAAA":
				complete = true
			case "CNAME":
				target := normalizeDomainName(record.Value)
				if strings.HasSuffix(target, ".kinsta.cloud") || walk(target) {
					complete = true
				}
			}
		}
		return complete
	}
	if !walk(requested) {
		return nil, false
	}
	return expectations, len(expectations) > 0
}

func kinstaRoutingRecordName(owner, name string) string {
	owner = normalizeDomainName(owner)
	name = normalizeDomainName(name)
	if name == "@" || name == "" {
		return owner
	}
	if label, _, ok := strings.Cut(owner, "."); ok && name == label {
		return owner
	}
	if owner == "" || name == owner || strings.HasSuffix(name, "."+owner) || strings.Contains(name, ".") {
		return name
	}
	return name + "." + owner
}

func kinstaInternalRoutingExpectation(requested string, domains []kinsta.Domain) (siteDomainExpectedDNSRecord, bool) {
	for _, domain := range domains {
		generated := normalizeDomainName(domainName(domain))
		if generated == "" || !strings.HasSuffix(generated, ".kinsta.cloud") {
			continue
		}
		addresses, err := siteDomainLookupHostFn(generated)
		if err != nil || len(addresses) == 0 {
			continue
		}
		values := []string{}
		for _, address := range addresses {
			ip := net.ParseIP(strings.TrimSpace(address))
			if ip != nil && ip.To4() != nil {
				values = append(values, ip.String())
			}
		}
		if len(values) > 0 {
			return siteDomainExpectedDNSRecord{Domain: requested, Type: "A", Name: requested, Values: values}, true
		}
	}
	return siteDomainExpectedDNSRecord{}, false
}

func kinstaDomainRecords(ctx context.Context, client *kinsta.Client, domainID string) (kinsta.DomainRecords, error) {
	recordsCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return client.DomainRecords(recordsCtx, domainID)
}

func kinstaDomainVerificationState(domain kinsta.Domain) (string, bool, bool) {
	if domain.IsVerified != nil {
		if *domain.IsVerified {
			return "verified", true, true
		}
		return "pending", false, true
	}
	for _, value := range []string{domain.VerificationStatus, domain.Status, domain.State, domain.DomainStatus} {
		if ready, ok := kinstaDomainVerificationStatusValue(value); ok {
			if ready {
				return "verified", true, true
			}
			return "pending", false, true
		}
	}
	return "unknown", false, false
}

func kinstaDomainVerificationStatusValue(value string) (bool, bool) {
	status := normalizeKinstaDomainStatusValue(value)
	switch status {
	case "verified", "valid", "active", "complete", "completed", "success", "successful", "domain_live", "live", "point_domain", "point_your_domain", "pointing_required", "ready_to_point":
		return true, true
	case "unverified", "not_verified", "not_valid", "invalid", "pending", "pending_verification", "verification_pending", "requires_verification", "needs_verification", "verifying", "checking":
		return false, true
	default:
		return false, false
	}
}

func kinstaDomainVerificationValidationState(validation kinsta.DomainVerificationValidation) (string, bool, bool) {
	if validation.Valid {
		return "verified", true, true
	}
	if len(validation.Records) > 0 {
		return "pending", false, true
	}
	return "unknown", false, false
}

func kinstaDomainPointingState(domain kinsta.Domain) (string, bool, bool) {
	if domain.IsPointing != nil {
		if *domain.IsPointing {
			return "pointed", true, true
		}
		return "pending", false, true
	}
	for _, value := range []string{domain.DNSStatus, domain.DomainStatus, domain.Status, domain.State} {
		if ready, ok := kinstaDomainPointingStatusValue(value); ok {
			if ready {
				return "pointed", true, true
			}
			return "pending", false, true
		}
	}
	return "unknown", false, false
}

func kinstaDomainPointingStatusValue(value string) (bool, bool) {
	status := normalizeKinstaDomainStatusValue(value)
	switch status {
	case "pointed", "live", "domain_live", "active", "connected", "dns_verified":
		return true, true
	case "not_pointed", "unpointed", "pending", "pending_dns", "dns_pending", "requires_dns", "needs_dns", "checking", "point_domain", "point_your_domain", "pointing_required", "ready_to_point", "verified":
		return false, true
	default:
		return false, false
	}
}

func normalizeKinstaDomainStatusValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	for strings.Contains(value, "__") {
		value = strings.ReplaceAll(value, "__", "_")
	}
	return strings.Trim(value, "_")
}

func kinstaDomainRoutingDescription(label string, known bool) string {
	switch label {
	case "pointed":
		return "pointed"
	case "pending":
		return "pending; point public DNS to Kinsta"
	}
	if !known {
		return "check public DNS below"
	}
	return "unknown"
}

func kinstaDNSExpectations(domain string, records kinsta.DomainRecords) []siteDomainExpectedDNSRecord {
	out := []siteDomainExpectedDNSRecord{}
	for _, record := range append(records.Verification, records.Pointing...) {
		name := record.RecordName()
		recordType := strings.ToUpper(record.RecordTypeName())
		value := record.RecordContent()
		if name == "" || recordType == "" || value == "" {
			continue
		}
		out = append(out, siteDomainExpectedDNSRecord{Domain: domain, Type: recordType, Name: name, Value: value})
	}
	return out
}

func checkLinodeSiteDomainProvider(plan siteDomainPlan) siteDomainProviderCheck {
	out := siteDomainProviderCheck{Ready: true, Description: []string{"Linode target"}}
	if siteDomainCloudflareStrict(plan) {
		out.Description = append(out.Description, siteDomainStatusBlockLines("unchecked", "proxy mode",
			siteDomainField("mode", "cloudflare"),
			siteDomainField("detail", "Cloudflare validates visitor HTTPS and origin certificate hostname"),
		)...)
	} else if proxyIP, ok := siteDomainReverseProxyIP(plan); ok {
		out.Description = append(out.Description, siteDomainStatusBlockLines("unchecked", "proxy mode",
			siteDomainField("mode", "reverse proxy "+proxyIP.String()),
			siteDomainField("detail", "public DNS resolves to proxy; origin uses target wildcard certificate"),
		)...)
	}
	remote, err := runSSHOutputFn(remoteSudoBashArgs(plan.Target, renderLinodeDomainCheckScript(plan)))
	if err != nil {
		out.Ready = false
		out.Description = append(out.Description, siteDomainStatusBlockLines("pending", "remote readiness", siteDomainField("detail", err.Error()))...)
	} else {
		values := keyValueLines(string(remote))
		for _, item := range []struct{ key, label string }{
			{"vhost", "nginx vhost"},
			{"enabled", "nginx enabled"},
			{"timer", "certbot timer"},
			{"service", "last certbot service"},
			{"cert", "certificate"},
		} {
			value := firstNonEmpty(values[item.key], "unknown")
			out.Description = append(out.Description, siteDomainStatusBlockLines(linodeDomainProviderStatus(plan, item.key, value), item.label, siteDomainField("status", value))...)
		}
		if values["vhost"] != "present" || values["enabled"] != "present" {
			out.Ready = false
		} else if _, ok := siteDomainReverseProxyIP(plan); ok {
			if values["cert"] == "wildcard-missing" {
				out.Ready = false
			}
		} else if values["timer"] != "active" && values["cert"] != "ready" {
			out.Ready = false
		}
	}
	if proxyIP, ok := siteDomainReverseProxyIP(plan); ok {
		for _, domain := range plan.allDomains() {
			out.DNSRecords = append(out.DNSRecords, siteDomainExpectedDNSRecord{Domain: domain, Type: siteDomainProxyIPRecordType(proxyIP), Name: domain, Value: proxyIP.String()})
		}
	} else if !siteDomainCloudflareProxy(plan) {
		for _, domain := range plan.allDomains() {
			if plan.TargetIPv4 != "" {
				out.DNSRecords = append(out.DNSRecords, siteDomainExpectedDNSRecord{Domain: domain, Type: "A", Name: domain, Value: plan.TargetIPv4})
			}
			if plan.TargetIPv6 != "" {
				out.DNSRecords = append(out.DNSRecords, siteDomainExpectedDNSRecord{Domain: domain, Type: "AAAA", Name: domain, Value: plan.TargetIPv6})
			}
		}
	}
	if strings.EqualFold(plan.CurrentHostname, plan.Canonical) || strings.EqualFold(firstRecordString(plan.Record, "primary_domain"), plan.Canonical) {
		out.Primary = true
	}
	return out
}

func linodeDomainProviderStatus(plan siteDomainPlan, key, value string) string {
	if _, ok := siteDomainReverseProxyIP(plan); ok {
		switch key {
		case "vhost", "enabled":
			if value == "present" {
				return "ok"
			}
			return "pending"
		case "cert":
			if value == "wildcard-missing" {
				return "pending"
			}
			return "unchecked"
		default:
			return "unchecked"
		}
	}
	switch key {
	case "vhost", "enabled":
		if value == "present" {
			return "ok"
		}
		return "pending"
	case "timer":
		if value == "active" || value == "not-required" {
			return "ok"
		}
		return "pending"
	case "service":
		if value == "failed" {
			return "pending"
		}
		return "unchecked"
	case "cert":
		if value == "ready" || value == "wildcard-origin" {
			return "ok"
		}
		return "pending"
	default:
		return "unchecked"
	}
}

func renderLinodeDomainCheckScript(plan siteDomainPlan) string {
	q := shellQuoteArg
	_, reverseProxyIP := siteDomainReverseProxyIP(plan)
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	if reverseProxyIP {
		b.WriteString("vhost=present\nenabled=present\ntimer=not-required\ncert=wildcard-origin\nservice=not-required\n")
		b.WriteString("if [ ! -f /etc/nginx/snippets/nf-wildcard-cert.conf ]; then cert=wildcard-missing; fi\n")
	} else {
		b.WriteString("vhost=present\nenabled=present\ntimer=active\ncert=ready\nservice=unknown\n")
	}
	for _, domain := range plan.allDomains() {
		art := linodeDomainArtifacts(domain)
		b.WriteString("if [ ! -f ")
		b.WriteString(q(art.Vhost))
		b.WriteString(" ]; then vhost=missing; fi\n")
		b.WriteString("if [ ! -L ")
		b.WriteString(q(art.Enabled))
		b.WriteString(" ] && [ ! -f ")
		b.WriteString(q(art.Enabled))
		b.WriteString(" ]; then enabled=missing; fi\n")
		if reverseProxyIP {
			continue
		}
		b.WriteString("if ! systemctl is-active --quiet ")
		b.WriteString(q(art.ServiceName + ".timer"))
		b.WriteString("; then if systemctl list-unit-files ")
		b.WriteString(q(art.ServiceName + ".timer"))
		b.WriteString(" >/dev/null 2>&1; then timer=inactive; else timer=missing; fi; fi\n")
		b.WriteString("state=$(systemctl is-active ")
		b.WriteString(q(art.ServiceName + ".service"))
		b.WriteString(" 2>/dev/null || true); if [ -n \"$state\" ]; then service=$state; fi\n")
		b.WriteString("if [ ! -f ")
		b.WriteString(q(art.CertDir + "/fullchain.pem"))
		b.WriteString(" ] || [ ! -f ")
		b.WriteString(q(art.CertDir + "/privkey.pem"))
		b.WriteString(" ]; then cert=missing; fi\n")
	}
	b.WriteString("echo vhost=$vhost\necho enabled=$enabled\necho timer=$timer\necho service=$service\necho cert=$cert\n")
	return b.String()
}

func keyValueLines(output string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return values
}

func printSiteDomainDNSCheck(plan siteDomainPlan, records []siteDomainExpectedDNSRecord) bool {
	fmt.Println("DNS")
	if siteDomainCloudflareProxy(plan) {
		return printSiteDomainCloudflareDNSCheck(plan)
	}
	if len(records) == 0 {
		if plan.Provider == "kinsta" {
			printSiteDomainStatusBlock("unchecked", "provider DNS records",
				siteDomainField("result", "unavailable from provider"),
				siteDomainField("fallback", "checking public DNS resolution"),
			)
			return printSiteDomainPublicDNSCheck(plan)
		}
		if plan.Provider == "linode" && plan.TargetHostname != "" {
			printSiteDomainStatusBlock("unchecked", "provider DNS records",
				siteDomainField("domains", strings.Join(plan.allDomains(), ", ")),
				siteDomainField("target", plan.TargetHostname),
			)
		} else {
			printSiteDomainStatusBlock("unchecked", "provider DNS records",
				siteDomainField("result", "no provider DNS records available"),
			)
		}
		return false
	}
	ready := true
	for _, record := range records {
		result := checkSiteDomainDNSRecord(record)
		if !result.OK {
			ready = false
		}
		printSiteDomainStatusBlock(siteDomainStatus(result.OK), record.Type,
			siteDomainField("name", record.Name),
			siteDomainField("expected", siteDomainExpectedDNSValue(record)),
			siteDomainField("result", result.Result),
			siteDomainField("detail", result.Detail),
		)
	}
	return ready
}

func printSiteDomainPublicDNSCheck(plan siteDomainPlan) bool {
	ready := true
	for _, domain := range plan.allDomains() {
		hosts, err := siteDomainLookupHostFn(domain)
		if err != nil || len(hosts) == 0 {
			ready = false
			if err != nil {
				printSiteDomainStatusBlock("pending", "public DNS",
					siteDomainField("domain", domain),
					siteDomainField("result", "lookup failed"),
					siteDomainField("detail", err.Error()),
				)
			} else {
				printSiteDomainStatusBlock("pending", "public DNS",
					siteDomainField("domain", domain),
					siteDomainField("result", "no public DNS records found"),
				)
			}
			continue
		}
		printSiteDomainStatusBlock("ok", "public DNS",
			siteDomainField("domain", domain),
			siteDomainField("resolves to", strings.Join(hosts, ", ")),
		)
	}
	return ready
}

func printSiteDomainCloudflareDNSCheck(plan siteDomainPlan) bool {
	ranges := siteDomainCloudflareIPRangesFn()
	if ranges.Warning != "" {
		printSiteDomainStatusBlock("warning", "Cloudflare IP ranges", siteDomainField("detail", ranges.Warning))
	}
	ready := true
	for _, domain := range plan.allDomains() {
		hosts, err := siteDomainLookupHostFn(domain)
		if err != nil || len(hosts) == 0 {
			ready = false
			if err != nil {
				printSiteDomainStatusBlock("pending", "Cloudflare DNS",
					siteDomainField("domain", domain),
					siteDomainField("result", "lookup failed"),
					siteDomainField("detail", err.Error()),
				)
			} else {
				printSiteDomainStatusBlock("pending", "Cloudflare DNS",
					siteDomainField("domain", domain),
					siteDomainField("result", "no public DNS records found"),
				)
			}
			continue
		}
		outside := cloudflareDNSHostsOutsideRanges(hosts, ranges)
		if len(outside) > 0 {
			ready = false
			printSiteDomainStatusBlock("pending", "Cloudflare DNS",
				siteDomainField("domain", domain),
				siteDomainField("resolves to", strings.Join(hosts, ", ")),
				siteDomainField("result", "non-Cloudflare address found"),
				siteDomainField("outside ranges", strings.Join(outside, ", ")),
			)
			continue
		}
		printSiteDomainStatusBlock("ok", "Cloudflare DNS",
			siteDomainField("domain", domain),
			siteDomainField("resolves to", strings.Join(hosts, ", ")),
			siteDomainField("origin check", "skipped for "+displaySiteDomainProxyMode(plan.ProxyMode)),
		)
	}
	return ready
}

func cloudflareDNSHostsOutsideRanges(hosts []string, ranges siteDomainIPRangeSet) []string {
	if len(ranges.Prefixes) == 0 {
		return hosts
	}
	outside := []string{}
	for _, host := range hosts {
		addr, err := netip.ParseAddr(strings.TrimSpace(host))
		if err != nil || !cloudflareRangesContain(ranges, addr) {
			outside = append(outside, host)
		}
	}
	return outside
}

func cloudflareRangesContain(ranges siteDomainIPRangeSet, addr netip.Addr) bool {
	for _, prefix := range ranges.Prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func loadCloudflareIPRanges() siteDomainIPRangeSet {
	ranges, err := fetchCloudflareIPRanges()
	if err == nil {
		return ranges
	}
	fallback := bundledCloudflareIPRanges()
	fallback.Warning = fmt.Sprintf("Cloudflare IP range fetch failed; using bundled fallback from %s", fallback.Source)
	return fallback
}

func fetchCloudflareIPRanges() (siteDomainIPRangeSet, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	prefixes := []netip.Prefix{}
	for _, url := range []string{"https://www.cloudflare.com/ips-v4", "https://www.cloudflare.com/ips-v6"} {
		body, err := fetchCloudflareIPRangeURL(ctx, url)
		if err != nil {
			return siteDomainIPRangeSet{}, err
		}
		parsed, err := parseCloudflareIPRangeText(body)
		if err != nil {
			return siteDomainIPRangeSet{}, err
		}
		prefixes = append(prefixes, parsed...)
	}
	return siteDomainIPRangeSet{Prefixes: prefixes, Source: "https://www.cloudflare.com/ips/"}, nil
}

func fetchCloudflareIPRangeURL(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "nf-domain-check")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func parseCloudflareIPRangeText(text string) ([]netip.Prefix, error) {
	prefixes := []netip.Prefix{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(line)
		if err != nil {
			return nil, err
		}
		prefixes = append(prefixes, prefix)
	}
	if len(prefixes) == 0 {
		return nil, fmt.Errorf("Cloudflare IP range list was empty")
	}
	return prefixes, nil
}

func bundledCloudflareIPRanges() siteDomainIPRangeSet {
	const source = "2023-09-28"
	text := `173.245.48.0/20
103.21.244.0/22
103.22.200.0/22
103.31.4.0/22
141.101.64.0/18
108.162.192.0/18
190.93.240.0/20
188.114.96.0/20
197.234.240.0/22
198.41.128.0/17
162.158.0.0/15
104.16.0.0/13
104.24.0.0/14
172.64.0.0/13
131.0.72.0/22
2400:cb00::/32
2606:4700::/32
2803:f800::/32
2405:b500::/32
2405:8100::/32
2a06:98c0::/29
2c0f:f248::/32`
	prefixes, err := parseCloudflareIPRangeText(text)
	if err != nil {
		return siteDomainIPRangeSet{Source: source}
	}
	return siteDomainIPRangeSet{Prefixes: prefixes, Source: source}
}

func checkSiteDomainDNSRecord(record siteDomainExpectedDNSRecord) siteDomainDNSCheckResult {
	expected := siteDomainExpectedDNSValues(record)
	switch strings.ToUpper(record.Type) {
	case "A", "AAAA":
		hosts, err := siteDomainLookupHostFn(record.Name)
		if err != nil {
			return siteDomainDNSCheckResult{Result: "lookup failed", Detail: err.Error()}
		}
		for _, host := range hosts {
			for _, value := range expected {
				if strings.TrimSpace(host) == value {
					return siteDomainDNSCheckResult{OK: true, Result: "matches expected"}
				}
			}
		}
		return siteDomainDNSCheckResult{Result: "unexpected DNS value", Detail: "got " + strings.Join(hosts, ", ")}
	case "TXT":
		values, err := siteDomainLookupTXTFn(record.Name)
		if err != nil {
			return siteDomainDNSCheckResult{Result: "lookup failed", Detail: err.Error()}
		}
		for _, value := range values {
			for _, want := range expected {
				if value == want {
					return siteDomainDNSCheckResult{OK: true, Result: "matches expected"}
				}
			}
		}
		return siteDomainDNSCheckResult{Result: "unexpected DNS value", Detail: "got " + strings.Join(values, ", ")}
	case "CNAME":
		value, err := siteDomainLookupCNAMEFn(record.Name)
		if err != nil {
			return siteDomainDNSCheckResult{Result: "lookup failed", Detail: err.Error()}
		}
		for _, want := range expected {
			if normalizeDNSName(value) == normalizeDNSName(want) {
				return siteDomainDNSCheckResult{OK: true, Result: "matches expected"}
			}
		}
		return siteDomainDNSCheckResult{Result: "unexpected DNS value", Detail: "got " + strings.TrimSuffix(value, ".")}
	default:
		return siteDomainDNSCheckResult{Result: "record type is not checked"}
	}
}

func siteDomainExpectedDNSValues(record siteDomainExpectedDNSRecord) []string {
	if len(record.Values) > 0 {
		return record.Values
	}
	if strings.TrimSpace(record.Value) != "" {
		return []string{strings.TrimSpace(record.Value)}
	}
	return nil
}

func siteDomainExpectedDNSValue(record siteDomainExpectedDNSRecord) string {
	return strings.Join(siteDomainExpectedDNSValues(record), ", ")
}

func normalizeDNSName(value string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
}

func printSiteDomainHTTPChecks(plan siteDomainPlan) bool {
	fmt.Println("HTTP")
	ready := true
	for _, domain := range plan.allDomains() {
		result := siteDomainHTTPStatusFn(domain)
		url := "http://" + domain + "/"
		if result.Error != "" {
			printSiteDomainStatusBlock("pending", "HTTP",
				siteDomainField("url", url),
				siteDomainField("result", siteDomainErrorSummary(result.Error)),
				siteDomainField("detail", result.Error),
			)
			ready = false
			continue
		}
		if siteDomainRedirectsToUnexpectedInternalHostname(plan, result.Location) {
			printSiteDomainStatusBlock("pending", "HTTP",
				siteDomainField("url", url),
				siteDomainField("result", "redirects to internal hostname"),
				siteDomainField("location", result.Location),
				siteDomainField("internal hostname", plan.InternalHostname),
			)
			ready = false
			continue
		}
		if result.StatusCode >= 500 || result.StatusCode == 0 {
			printSiteDomainStatusBlock("pending", "HTTP",
				siteDomainField("url", url),
				siteDomainField("status", fmt.Sprint(result.StatusCode)),
			)
			ready = false
			continue
		}
		printSiteDomainStatusBlock("ok", "HTTP",
			siteDomainField("url", url),
			siteDomainField("status", fmt.Sprint(result.StatusCode)),
			siteDomainField("location", result.Location),
		)
	}
	return ready
}

func checkSiteDomainHTTP(domain string) siteDomainHTTPCheckResult {
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequest(http.MethodGet, "http://"+domain+"/", nil)
	if err != nil {
		return siteDomainHTTPCheckResult{Error: err.Error()}
	}
	req.Header.Set("User-Agent", "nf-domain-check")
	resp, err := client.Do(req)
	if err != nil {
		return siteDomainHTTPCheckResult{Error: err.Error()}
	}
	defer resp.Body.Close()
	return siteDomainHTTPCheckResult{StatusCode: resp.StatusCode, Location: resp.Header.Get("Location")}
}

func checkSiteDomainHTTPS(domain string) siteDomainHTTPCheckResult {
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequest(http.MethodGet, "https://"+domain+"/", nil)
	if err != nil {
		return siteDomainHTTPCheckResult{Error: err.Error()}
	}
	req.Header.Set("User-Agent", "nf-domain-check")
	resp, err := client.Do(req)
	if err != nil {
		return siteDomainHTTPCheckResult{Error: err.Error()}
	}
	defer resp.Body.Close()
	return siteDomainHTTPCheckResult{StatusCode: resp.StatusCode, Location: resp.Header.Get("Location")}
}

func printSiteDomainTLSChecks(plan siteDomainPlan) bool {
	fmt.Println("HTTPS")
	ready := true
	for _, domain := range plan.allDomains() {
		url := "https://" + domain + "/"
		result := siteDomainTLSStatusFn(domain)
		if !result.OK {
			detail := firstNonEmpty(result.Error, "TLS certificate is not ready")
			printSiteDomainStatusBlock("pending", "HTTPS",
				siteDomainField("url", url),
				siteDomainField("result", siteDomainErrorSummary(detail)),
				siteDomainField("detail", detail),
			)
			ready = false
			continue
		}
		httpsResult := siteDomainHTTPSStatusFn(domain)
		if httpsResult.Error != "" {
			printSiteDomainStatusBlock("pending", "HTTPS",
				siteDomainField("url", url),
				siteDomainField("result", siteDomainErrorSummary(httpsResult.Error)),
				siteDomainField("detail", httpsResult.Error),
			)
			ready = false
			continue
		}
		if isSameHTTPSRedirect(domain, httpsResult.Location) {
			printSiteDomainStatusBlock("pending", "HTTPS",
				siteDomainField("url", url),
				siteDomainField("result", "redirect loop"),
				siteDomainField("detail", "redirects to itself; likely Cloudflare Flexible SSL or HTTPS redirect loop"),
			)
			ready = false
			continue
		}
		if siteDomainRedirectsToUnexpectedInternalHostname(plan, httpsResult.Location) {
			printSiteDomainStatusBlock("pending", "HTTPS",
				siteDomainField("url", url),
				siteDomainField("result", "redirects to internal hostname"),
				siteDomainField("location", httpsResult.Location),
				siteDomainField("internal hostname", plan.InternalHostname),
			)
			ready = false
			continue
		}
		if httpsResult.StatusCode >= 500 || httpsResult.StatusCode == 0 {
			printSiteDomainStatusBlock("pending", "HTTPS",
				siteDomainField("url", url),
				siteDomainField("status", fmt.Sprint(httpsResult.StatusCode)),
			)
			ready = false
			continue
		}
		fields := []siteDomainOutputField{siteDomainField("url", url)}
		if !result.NotAfter.IsZero() {
			fields = append(fields, siteDomainField("expires", result.NotAfter.Format("2006-01-02")))
		}
		if result.Issuer != "" {
			fields = append(fields, siteDomainField("issuer", result.Issuer))
		}
		if httpsResult.StatusCode != 0 {
			fields = append(fields, siteDomainField("status", fmt.Sprint(httpsResult.StatusCode)))
		}
		fields = append(fields, siteDomainField("location", httpsResult.Location))
		printSiteDomainStatusBlock("ok", "HTTPS", fields...)
	}
	return ready
}

func siteDomainRedirectsToInternalHostname(plan siteDomainPlan, location string) bool {
	return siteDomainRedirectsToHostname(location, plan.InternalHostname)
}

func siteDomainRedirectsToUnexpectedInternalHostname(plan siteDomainPlan, location string) bool {
	if !siteDomainRedirectsToInternalHostname(plan, location) {
		return false
	}
	return !siteDomainRedirectsToHostname(location, currentSiteDomainCanonicalHostname(plan))
}

func currentSiteDomainCanonicalHostname(plan siteDomainPlan) string {
	return firstNonEmpty(cachedExternalPrimaryDomain(plan.Record), plan.CurrentHostname, plan.InternalHostname)
}

func siteDomainRedirectsToHostname(location, hostname string) bool {
	hostname = normalizeDomainName(hostname)
	if hostname == "" {
		return false
	}
	return hostnameFromURLish(location) == hostname
}

func isSameHTTPSRedirect(domain, location string) bool {
	location = strings.ToLower(strings.TrimSpace(location))
	if location == "" {
		return false
	}
	if location == "/" {
		return true
	}
	root := "https://" + strings.ToLower(strings.TrimSuffix(domain, "."))
	return strings.TrimRight(location, "/") == root
}

func printSiteDomainOriginTLSChecks(plan siteDomainPlan) bool {
	fmt.Println("Origin HTTPS")
	origin := firstNonEmpty(plan.TargetIPv4, plan.TargetIPv6, plan.TargetHostname, plan.Target.SSHHost)
	if origin == "" {
		printSiteDomainStatusBlock("unchecked", "origin HTTPS", siteDomainField("result", "no cached Linode origin address"))
		return false
	}
	ready := true
	for _, domain := range plan.allDomains() {
		result := siteDomainOriginTLSFn(domain, origin)
		if !result.OK {
			detail := firstNonEmpty(result.Error, "origin TLS certificate is not ready")
			printSiteDomainStatusBlock("pending", "origin HTTPS",
				siteDomainField("domain", domain),
				siteDomainField("origin", origin),
				siteDomainField("result", siteDomainErrorSummary(detail)),
				siteDomainField("detail", detail),
			)
			ready = false
			continue
		}
		fields := []siteDomainOutputField{
			siteDomainField("domain", domain),
			siteDomainField("origin", origin),
		}
		if !result.NotAfter.IsZero() {
			fields = append(fields, siteDomainField("expires", result.NotAfter.Format("2006-01-02")))
		}
		if result.Issuer != "" {
			fields = append(fields, siteDomainField("issuer", result.Issuer))
		}
		printSiteDomainStatusBlock("ok", "origin HTTPS", fields...)
	}
	return ready
}

func checkSiteDomainTLS(domain string) siteDomainTLSCheckResult {
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(domain, "443"), &tls.Config{ServerName: domain, MinVersion: tls.VersionTLS12})
	if err != nil {
		return siteDomainTLSCheckResult{Error: err.Error()}
	}
	defer conn.Close()
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return siteDomainTLSCheckResult{Error: "no peer certificate"}
	}
	cert := state.PeerCertificates[0]
	return siteDomainTLSCheckResult{OK: true, NotAfter: cert.NotAfter, Issuer: cert.Issuer.CommonName}
}

func checkSiteDomainOriginTLS(domain, origin string) siteDomainTLSCheckResult {
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(origin, "443"), &tls.Config{ServerName: domain, MinVersion: tls.VersionTLS12})
	if err != nil {
		return siteDomainTLSCheckResult{Error: err.Error()}
	}
	defer conn.Close()
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return siteDomainTLSCheckResult{Error: "no peer certificate"}
	}
	cert := state.PeerCertificates[0]
	return siteDomainTLSCheckResult{OK: true, NotAfter: cert.NotAfter, Issuer: cert.Issuer.CommonName}
}

package cli

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
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

type siteDomainProviderCheck struct {
	Ready       bool
	Primary     bool
	DNSRecords  []siteDomainExpectedDNSRecord
	Description []string
}

func cmdSiteDomainCheck(plan siteDomainPlan) int {
	printSiteDomainCheckHeader(plan)
	providerCheck, err := checkSiteDomainProvider(plan)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	for _, line := range providerCheck.Description {
		fmt.Println(line)
	}
	dnsReady := printSiteDomainDNSCheck(plan, providerCheck.DNSRecords)
	httpReady := printSiteDomainHTTPChecks(plan)
	tlsReady := printSiteDomainTLSChecks(plan)
	originTLSReady := true
	if siteDomainCloudflareStrict(plan) {
		originTLSReady = printSiteDomainOriginTLSChecks(plan)
	}
	ready := providerCheck.Ready && dnsReady && httpReady && tlsReady && originTLSReady
	fmt.Println("Next step:")
	if ready && providerCheck.Primary {
		fmt.Println("  domain is primary and public checks passed")
	} else if ready {
		fmt.Printf("  ready for primary: nf site domain primary %s %s%s%s\n", plan.EnvID, plan.Canonical, siteDomainAliasArgs(plan.Aliases), siteDomainProxyArg(plan.ProxyMode))
	} else {
		fmt.Println("  wait for pending checks, then run nf site domain check again")
	}
	if ready {
		fmt.Println("Overall: ready")
		return 0
	}
	fmt.Println("Overall: pending")
	return 2
}

func siteDomainProxyArg(proxyMode string) string {
	if proxyMode == "" {
		return ""
	}
	return " --proxy " + displaySiteDomainProxyMode(proxyMode)
}

func printSiteDomainCheckHeader(plan siteDomainPlan) {
	fmt.Println("Public domain check:")
	fmt.Printf("  env:       %s\n", plan.EnvID)
	fmt.Printf("  provider:  %s\n", plan.Provider)
	if plan.Target.TargetRef != "" {
		fmt.Printf("  target:    %s\n", plan.Target.TargetRef)
	}
	if plan.CurrentURL != "" {
		fmt.Printf("  current:   %s\n", plan.CurrentURL)
	}
	if plan.InternalURL != "" {
		fmt.Printf("  fallback:  %s\n", plan.InternalURL)
	}
	fmt.Printf("  canonical: %s\n", plan.Canonical)
	if len(plan.Aliases) > 0 {
		fmt.Printf("  aliases:   %s\n", strings.Join(plan.Aliases, ", "))
	}
	if plan.ProxyMode != "" {
		fmt.Printf("  proxy:     %s\n", displaySiteDomainProxyMode(plan.ProxyMode))
	}
}

func siteDomainAliasArgs(aliases []string) string {
	if len(aliases) == 0 {
		return ""
	}
	var b strings.Builder
	for _, alias := range aliases {
		b.WriteString(" --alias ")
		b.WriteString(alias)
	}
	return b.String()
}

func checkSiteDomainProvider(plan siteDomainPlan) (siteDomainProviderCheck, error) {
	switch plan.Provider {
	case "kinsta":
		return checkKinstaSiteDomainProvider(plan)
	case "linode":
		return checkLinodeSiteDomainProvider(plan), nil
	default:
		return siteDomainProviderCheck{}, ProjectError{Msg: fmt.Sprintf("site domain check is not implemented for provider %q", plan.Provider)}
	}
}

func checkKinstaSiteDomainProvider(plan siteDomainPlan) (siteDomainProviderCheck, error) {
	token := envwizard.Value("KINSTA_API_KEY")
	if token == "" {
		return siteDomainProviderCheck{}, fmt.Errorf("Expected KINSTA_API_KEY in the environment or %s.", config.EnvFile())
	}
	client := kinsta.NewClient(firstNonEmpty(envwizard.Value("KINSTA_BASE_URL"), "https://api.kinsta.com/v2"), token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	domains, err := client.ListDomains(ctx, plan.KinstaEnvID)
	if err != nil {
		return siteDomainProviderCheck{}, err
	}
	out := siteDomainProviderCheck{Ready: true, Description: []string{"Kinsta:"}}
	for i, name := range plan.allDomains() {
		role := "canonical"
		if i > 0 {
			role = "redirect"
		}
		domain, ok := kinsta.FindDomain(domains, name)
		if !ok {
			out.Ready = false
			out.Description = append(out.Description, fmt.Sprintf("  domain %s (%s): missing", name, role))
			continue
		}
		if i == 0 && domain.IsPrimary {
			out.Primary = true
		}
		primaryLabel := ""
		if domain.IsPrimary {
			primaryLabel = ", primary"
		}
		out.Description = append(out.Description, fmt.Sprintf("  domain %s (%s): present%s", name, role, primaryLabel))
		records, err := client.DomainRecords(ctx, domain.ID)
		if err != nil {
			return siteDomainProviderCheck{}, err
		}
		out.DNSRecords = append(out.DNSRecords, kinstaDNSExpectations(name, records)...)
	}
	return out, nil
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
	out := siteDomainProviderCheck{Ready: true, Description: []string{"Linode target:"}}
	if siteDomainCloudflareStrict(plan) {
		out.Description = append(out.Description, "  proxy mode: cloudflare (Cloudflare validates visitor HTTPS and origin certificate hostname)")
	}
	remote, err := runSSHOutputFn(remoteSudoBashArgs(plan.Target, renderLinodeDomainCheckScript(plan)))
	if err != nil {
		out.Ready = false
		out.Description = append(out.Description, fmt.Sprintf("  remote readiness: error: %v", err))
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
			out.Description = append(out.Description, fmt.Sprintf("  %s: %s", item.label, value))
		}
		if values["vhost"] != "present" || values["enabled"] != "present" || values["timer"] != "active" && values["cert"] != "ready" {
			out.Ready = false
		}
	}
	if !siteDomainCloudflareProxy(plan) {
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

func renderLinodeDomainCheckScript(plan siteDomainPlan) string {
	q := shellQuoteArg
	fileSlug := plan.FileSlug
	if fileSlug == "" {
		fileSlug = envIDFileSlug(plan.EnvID)
	}
	serviceName := "nf-public-domain-" + fileSlug + "-tls"
	publicVhost := "/etc/nginx/sites-available/nf-site-public-" + fileSlug
	publicEnabled := "/etc/nginx/sites-enabled/nf-site-public-" + fileSlug
	certDir := "/etc/letsencrypt/live/" + plan.Canonical
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	b.WriteString("if [ -f ")
	b.WriteString(q(publicVhost))
	b.WriteString(" ]; then echo vhost=present; else echo vhost=missing; fi\n")
	b.WriteString("if [ -L ")
	b.WriteString(q(publicEnabled))
	b.WriteString(" ] || [ -f ")
	b.WriteString(q(publicEnabled))
	b.WriteString(" ]; then echo enabled=present; else echo enabled=missing; fi\n")
	b.WriteString("if systemctl is-active --quiet ")
	b.WriteString(q(serviceName + ".timer"))
	b.WriteString("; then echo timer=active; elif systemctl list-unit-files ")
	b.WriteString(q(serviceName + ".timer"))
	b.WriteString(" >/dev/null 2>&1; then echo timer=inactive; else echo timer=missing; fi\n")
	b.WriteString("service_state=$(systemctl is-active ")
	b.WriteString(q(serviceName + ".service"))
	b.WriteString(" 2>/dev/null || true); if [ -n \"$service_state\" ]; then echo service=$service_state; else echo service=unknown; fi\n")
	b.WriteString("if [ -f ")
	b.WriteString(q(certDir + "/fullchain.pem"))
	b.WriteString(" ] && [ -f ")
	b.WriteString(q(certDir + "/privkey.pem"))
	b.WriteString(" ]; then echo cert=ready; else echo cert=missing; fi\n")
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
	fmt.Println("DNS:")
	if siteDomainCloudflareProxy(plan) {
		return printSiteDomainCloudflareDNSCheck(plan)
	}
	if len(records) == 0 {
		if plan.Provider == "linode" && plan.TargetHostname != "" {
			fmt.Printf("  unchecked: point %s at target %s\n", strings.Join(plan.allDomains(), ", "), plan.TargetHostname)
		} else {
			fmt.Println("  unchecked: no provider DNS records available")
		}
		return false
	}
	ready := true
	for _, record := range records {
		ok, detail := checkSiteDomainDNSRecord(record)
		status := "ok"
		if !ok {
			status = "pending"
			ready = false
		}
		fmt.Printf("  %s %s -> %s: %s%s\n", record.Type, record.Name, record.Value, status, detail)
	}
	return ready
}

func printSiteDomainCloudflareDNSCheck(plan siteDomainPlan) bool {
	ready := true
	for _, domain := range plan.allDomains() {
		hosts, err := siteDomainLookupHostFn(domain)
		if err != nil || len(hosts) == 0 {
			ready = false
			if err != nil {
				fmt.Printf("  %s: pending (%s)\n", domain, err)
			} else {
				fmt.Printf("  %s: pending (no public DNS records found)\n", domain)
			}
			continue
		}
		fmt.Printf("  %s: ok (resolves publicly to %s; origin IP match skipped for %s)\n", domain, strings.Join(hosts, ", "), displaySiteDomainProxyMode(plan.ProxyMode))
	}
	return ready
}

func checkSiteDomainDNSRecord(record siteDomainExpectedDNSRecord) (bool, string) {
	switch strings.ToUpper(record.Type) {
	case "A", "AAAA":
		hosts, err := siteDomainLookupHostFn(record.Name)
		if err != nil {
			return false, " (" + err.Error() + ")"
		}
		for _, host := range hosts {
			if strings.TrimSpace(host) == record.Value {
				return true, ""
			}
		}
		return false, fmt.Sprintf(" (got %s)", strings.Join(hosts, ", "))
	case "TXT":
		values, err := siteDomainLookupTXTFn(record.Name)
		if err != nil {
			return false, " (" + err.Error() + ")"
		}
		for _, value := range values {
			if value == record.Value {
				return true, ""
			}
		}
		return false, fmt.Sprintf(" (got %s)", strings.Join(values, ", "))
	case "CNAME":
		value, err := siteDomainLookupCNAMEFn(record.Name)
		if err != nil {
			return false, " (" + err.Error() + ")"
		}
		if normalizeDNSName(value) == normalizeDNSName(record.Value) {
			return true, ""
		}
		return false, fmt.Sprintf(" (got %s)", strings.TrimSuffix(value, "."))
	default:
		return false, " (record type is not checked)"
	}
}

func normalizeDNSName(value string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
}

func printSiteDomainHTTPChecks(plan siteDomainPlan) bool {
	fmt.Println("HTTP:")
	ready := true
	for _, domain := range plan.allDomains() {
		result := siteDomainHTTPStatusFn(domain)
		if result.Error != "" {
			fmt.Printf("  http://%s: pending (%s)\n", domain, result.Error)
			ready = false
			continue
		}
		if plan.InternalHostname != "" && strings.Contains(strings.ToLower(result.Location), strings.ToLower(plan.InternalHostname)) {
			fmt.Printf("  http://%s: pending (redirects to internal hostname %s)\n", domain, plan.InternalHostname)
			ready = false
			continue
		}
		if result.StatusCode >= 500 || result.StatusCode == 0 {
			fmt.Printf("  http://%s: pending (status %d)\n", domain, result.StatusCode)
			ready = false
			continue
		}
		location := ""
		if result.Location != "" {
			location = " -> " + result.Location
		}
		fmt.Printf("  http://%s: ok (status %d%s)\n", domain, result.StatusCode, location)
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
	fmt.Println("HTTPS:")
	ready := true
	for _, domain := range plan.allDomains() {
		result := siteDomainTLSStatusFn(domain)
		if !result.OK {
			fmt.Printf("  https://%s: pending (%s)\n", domain, firstNonEmpty(result.Error, "TLS certificate is not ready"))
			ready = false
			continue
		}
		httpsResult := siteDomainHTTPSStatusFn(domain)
		if httpsResult.Error != "" {
			fmt.Printf("  https://%s: pending (%s)\n", domain, httpsResult.Error)
			ready = false
			continue
		}
		if isSameHTTPSRedirect(domain, httpsResult.Location) {
			fmt.Printf("  https://%s: pending (redirects to itself; likely Cloudflare Flexible SSL or HTTPS redirect loop)\n", domain)
			ready = false
			continue
		}
		if httpsResult.StatusCode >= 500 || httpsResult.StatusCode == 0 {
			fmt.Printf("  https://%s: pending (status %d)\n", domain, httpsResult.StatusCode)
			ready = false
			continue
		}
		expires := ""
		if !result.NotAfter.IsZero() {
			expires = " expires " + result.NotAfter.Format("2006-01-02")
		}
		issuer := ""
		if result.Issuer != "" {
			issuer = " issuer " + result.Issuer
		}
		status := ""
		if httpsResult.StatusCode != 0 {
			status = fmt.Sprintf(" status %d", httpsResult.StatusCode)
		}
		fmt.Printf("  https://%s: ok%s%s%s\n", domain, expires, issuer, status)
	}
	return ready
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
	fmt.Println("Origin HTTPS:")
	origin := firstNonEmpty(plan.TargetIPv4, plan.TargetIPv6, plan.TargetHostname, plan.Target.SSHHost)
	if origin == "" {
		fmt.Println("  unchecked: no cached Linode origin address")
		return false
	}
	ready := true
	for _, domain := range plan.allDomains() {
		result := siteDomainOriginTLSFn(domain, origin)
		if !result.OK {
			fmt.Printf("  https://%s @ %s: pending (%s)\n", domain, origin, firstNonEmpty(result.Error, "origin TLS certificate is not ready"))
			ready = false
			continue
		}
		expires := ""
		if !result.NotAfter.IsZero() {
			expires = " expires " + result.NotAfter.Format("2006-01-02")
		}
		issuer := ""
		if result.Issuer != "" {
			issuer = " issuer " + result.Issuer
		}
		fmt.Printf("  https://%s @ %s: ok%s%s\n", domain, origin, expires, issuer)
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

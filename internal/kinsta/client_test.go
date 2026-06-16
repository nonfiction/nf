package kinsta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientSiteEnvironmentDomainFlow(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		if got, want := r.Header.Get("Authorization"), "Bearer kinsta-token"; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /validate":
			_ = json.NewEncoder(w).Encode(map[string]any{"company": "company-123", "status": "active"})
		case "GET /sites":
			if got := r.URL.Query().Get("company"); got != "company-123" {
				t.Fatalf("company query = %q, want company-123", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"company": map[string]any{"sites": []map[string]any{{"id": "ksite123", "name": "foobar", "display_name": "foobar"}}}})
		case "GET /sites/ksite123/environments":
			_ = json.NewEncoder(w).Encode(map[string]any{"site": map[string]any{"environments": []map[string]any{{"id": "kenv-live", "name": "live", "container_info": map[string]any{"php_engine_version": "php8.3"}}, {"id": "kenv-staging", "display_name": "staging", "container_info": map[string]any{"php_engine_version": "php8.3"}}}}})
		case "GET /sites/ksite123/environments/kenv-live/ssh/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"host": "1.2.3.4", "port": "2222", "user": "foobar"})
		case "GET /sites/environments/kenv-live/ssh/password":
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"id": "kenv-live", "sftp_password": "sftp-pass"}})
		case "GET /sites/environments/kenv-live/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{{"id": "kdom-live", "name": "foobar.kinsta.nonfiction.dev", "is_primary": true, "status": "verified", "is_verified": true}}}})
		case "GET /sites/environments/domains/kdom-live/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"verification_records": []map[string]any{{"name": "_acme-challenge.foobar.kinsta.nonfiction.dev", "type": "TXT", "content": "token"}}, "pointing_records": []map[string]any{{"name": "foobar.kinsta.nonfiction.dev", "type": "A", "content": "203.0.113.10", "ttl": 300}}}})
		case "POST /sites/environments/kenv-live/domains":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("add domain decode error = %v", err)
			}
			if payload["domain_name"] != "www.client.com" || payload["setup_type"] != "avoid_downtime" || payload["is_wildcardless"] != false || payload["add_with_www_subdomain"] != false {
				t.Fatalf("add domain payload = %#v", payload)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-add-domain"})
		case "PUT /sites/environments/kenv-live/change-primary-domain":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("primary domain decode error = %v", err)
			}
			if payload["domain_id"] != "kdom-live" || payload["run_search_and_replace"] != true {
				t.Fatalf("primary domain payload = %#v", payload)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-primary-domain"})
		case "DELETE /sites/environments/kenv-live/domains":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("delete domain decode error = %v", err)
			}
			domainIDs, ok := payload["domain_ids"].([]any)
			if !ok || len(domainIDs) != 1 || domainIDs[0] != "kdom-old" {
				t.Fatalf("delete domain payload = %#v", payload)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-delete-domain", "status": 202})
		case "GET /operations/op123":
			_ = json.NewEncoder(w).Encode(map[string]any{"operation": map[string]any{"id": "op123", "status": "complete"}})
		case "PUT /sites/tools/modify-php-version":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("modify php decode error = %v", err)
			}
			if payload["environment_id"] != "kenv-live" || payload["php_version"] != "8.3" || payload["is_opt_out_from_automatic_php_update"] != false {
				t.Fatalf("modify php payload = %#v", payload)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-modify-php"})
		case "DELETE /sites/environments/kenv-staging":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-delete-env", "status": 202})
		case "DELETE /sites/ksite123":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "op-delete-site", "status": 202})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	client := NewClient(server.URL, "kinsta-token")
	ctx := context.Background()
	validate, err := client.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if validate.Company != "company-123" {
		t.Fatalf("company = %q, want company-123", validate.Company)
	}
	sites, err := client.ListSites(ctx, validate.Company)
	if err != nil {
		t.Fatalf("ListSites() error = %v", err)
	}
	if site, ok := FindSite(sites, "foobar"); !ok || site.ID != "ksite123" {
		t.Fatalf("FindSite() = %#v, %v; want ksite123", site, ok)
	}
	envs, err := client.ListEnvironments(ctx, "ksite123")
	if err != nil {
		t.Fatalf("ListEnvironments() error = %v", err)
	}
	if env, ok := FindEnvironment(envs, "staging"); !ok || env.ID != "kenv-staging" {
		t.Fatalf("FindEnvironment() = %#v, %v; want kenv-staging", env, ok)
	}
	if envs[0].CurrentPHPVersion() != "8.3" {
		t.Fatalf("CurrentPHPVersion() = %q, want 8.3", envs[0].CurrentPHPVersion())
	}
	sftpCfg, err := client.SFTPConfig(ctx, "ksite123", "kenv-live")
	if err != nil {
		t.Fatalf("SFTPConfig() error = %v", err)
	}
	if sftpCfg.Host != "1.2.3.4" || sftpCfg.Port != "2222" || sftpCfg.User != "foobar" {
		t.Fatalf("SFTPConfig() = %#v", sftpCfg)
	}
	sftpPassword, err := client.SFTPPassword(ctx, "kenv-live")
	if err != nil {
		t.Fatalf("SFTPPassword() error = %v", err)
	}
	if sftpPassword.Password != "sftp-pass" {
		t.Fatalf("SFTPPassword() = %#v", sftpPassword)
	}
	domains, err := client.ListDomains(ctx, "kenv-live")
	if err != nil {
		t.Fatalf("ListDomains() error = %v", err)
	}
	if domain, ok := FindDomain(domains, "foobar.kinsta.nonfiction.dev"); !ok || domain.ID != "kdom-live" || !domain.IsPrimary || domain.Status != "verified" || domain.IsVerified == nil || !*domain.IsVerified {
		t.Fatalf("FindDomain() = %#v, %v; want primary kdom-live", domain, ok)
	}
	records, err := client.DomainRecords(ctx, "kdom-live")
	if err != nil {
		t.Fatalf("DomainRecords() error = %v", err)
	}
	if len(records.Verification) != 1 || records.Verification[0].RecordContent() != "token" || len(records.Pointing) != 1 || records.Pointing[0].Content != "203.0.113.10" {
		t.Fatalf("records = %#v, want verification and pointing", records)
	}
	if opID, err := client.AddDomain(ctx, "kenv-live", AddDomainRequest{DomainName: "www.client.com", SetupType: "avoid_downtime"}); err != nil || opID != "op-add-domain" {
		t.Fatalf("AddDomain() = %q, %v; want op-add-domain", opID, err)
	}
	if opID, err := client.ChangePrimaryDomain(ctx, "kenv-live", "kdom-live", true); err != nil || opID != "op-primary-domain" {
		t.Fatalf("ChangePrimaryDomain() = %q, %v; want op-primary-domain", opID, err)
	}
	if opID, err := client.DeleteDomains(ctx, "kenv-live", []string{"kdom-old"}); err != nil || opID != "op-delete-domain" {
		t.Fatalf("DeleteDomains() = %q, %v; want op-delete-domain", opID, err)
	}
	if err := client.WaitOperation(ctx, "op123", 0); err != nil {
		t.Fatalf("WaitOperation() error = %v", err)
	}
	if opID, err := client.ModifyPHPVersion(ctx, ModifyPHPVersionRequest{EnvironmentID: "kenv-live", PHPVersion: "8.3", IsOptOutFromAutomaticPHPUpdate: false}); err != nil || opID != "op-modify-php" {
		t.Fatalf("ModifyPHPVersion() = %q, %v; want op-modify-php", opID, err)
	}
	if opID, err := client.DeleteEnvironment(ctx, "kenv-staging"); err != nil || opID != "op-delete-env" {
		t.Fatalf("DeleteEnvironment() = %q, %v; want op-delete-env", opID, err)
	}
	if opID, err := client.DeleteSite(ctx, "ksite123"); err != nil || opID != "op-delete-site" {
		t.Fatalf("DeleteSite() = %q, %v; want op-delete-site", opID, err)
	}
	if len(requests) == 0 {
		t.Fatalf("server saw no requests")
	}
}

func TestWaitOperationHandlesKinstaNumericStatus(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/operations/op-numeric" {
			http.NotFound(w, r)
			return
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		status := 202
		message := "Operation in progress"
		if calls > 1 {
			status = 200
			message = "Successfully finished request"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "message": message})
	}))
	t.Cleanup(server.Close)

	client := NewClient(server.URL, "kinsta-token")
	if err := client.WaitOperation(context.Background(), "op-numeric", time.Millisecond); err != nil {
		t.Fatalf("WaitOperation() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("operation calls = %d, want 2", calls)
	}
}

func TestWaitOperationHandlesKinstaStringStatus(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/operations/op-string" {
			http.NotFound(w, r)
			return
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		status := "is_running"
		if calls > 1 {
			status = "has_completed"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "message": "operation status"})
	}))
	t.Cleanup(server.Close)

	client := NewClient(server.URL, "kinsta-token")
	if err := client.WaitOperation(context.Background(), "op-string", time.Millisecond); err != nil {
		t.Fatalf("WaitOperation() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("operation calls = %d, want 2", calls)
	}
}

func TestWaitOperationReturnsKinstaFailedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/operations/op-failed" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "has_failed", "message": "Staging clone failed"})
	}))
	t.Cleanup(server.Close)

	client := NewClient(server.URL, "kinsta-token")
	err := client.WaitOperation(context.Background(), "op-failed", time.Millisecond)
	if err == nil {
		t.Fatal("WaitOperation() error = nil, want failed status error")
	}
	if msg := err.Error(); !strings.Contains(msg, "has_failed") || !strings.Contains(msg, "Staging clone failed") {
		t.Fatalf("WaitOperation() error = %q, want status and message", msg)
	}
}

func TestClientMarksServerErrorsTemporary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": 500, "message": "Server Error"})
	}))
	t.Cleanup(server.Close)

	client := NewClient(server.URL, "kinsta-token")
	_, err := client.ListEnvironments(context.Background(), "ksite123")
	if err == nil {
		t.Fatal("ListEnvironments() error = nil, want Kinsta server error")
	}
	if !IsTemporary(err) {
		t.Fatalf("IsTemporary(%v) = false, want true", err)
	}
}

package kinsta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
			_ = json.NewEncoder(w).Encode(map[string]any{"company": map[string]any{"sites": []map[string]any{{"id": "ksite123", "name": "sanjel", "display_name": "sanjel"}}}})
		case "GET /sites/ksite123/environments":
			_ = json.NewEncoder(w).Encode(map[string]any{"site": map[string]any{"environments": []map[string]any{{"id": "kenv-live", "name": "live"}, {"id": "kenv-staging", "display_name": "staging"}}}})
		case "GET /sites/environments/kenv-live/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"site_domains": []map[string]any{{"id": "kdom-live", "name": "sanjel.kinsta.nonfiction.dev", "is_primary": true}}}})
		case "GET /sites/environments/domains/kdom-live/verification-records":
			_ = json.NewEncoder(w).Encode(map[string]any{"site_domain": map[string]any{"verification_records": []map[string]any{{"name": "_acme-challenge.sanjel.kinsta.nonfiction.dev", "type": "TXT", "content": "token"}}, "pointing_records": []map[string]any{{"name": "sanjel.kinsta.nonfiction.dev", "type": "A", "content": "203.0.113.10", "ttl": 300}}}})
		case "GET /operations/op123":
			_ = json.NewEncoder(w).Encode(map[string]any{"operation": map[string]any{"id": "op123", "status": "complete"}})
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
	if site, ok := FindSite(sites, "sanjel"); !ok || site.ID != "ksite123" {
		t.Fatalf("FindSite() = %#v, %v; want ksite123", site, ok)
	}
	envs, err := client.ListEnvironments(ctx, "ksite123")
	if err != nil {
		t.Fatalf("ListEnvironments() error = %v", err)
	}
	if env, ok := FindEnvironment(envs, "staging"); !ok || env.ID != "kenv-staging" {
		t.Fatalf("FindEnvironment() = %#v, %v; want kenv-staging", env, ok)
	}
	domains, err := client.ListDomains(ctx, "kenv-live")
	if err != nil {
		t.Fatalf("ListDomains() error = %v", err)
	}
	if domain, ok := FindDomain(domains, "sanjel.kinsta.nonfiction.dev"); !ok || domain.ID != "kdom-live" || !domain.IsPrimary {
		t.Fatalf("FindDomain() = %#v, %v; want primary kdom-live", domain, ok)
	}
	records, err := client.DomainRecords(ctx, "kdom-live")
	if err != nil {
		t.Fatalf("DomainRecords() error = %v", err)
	}
	if len(records.Verification) != 1 || records.Verification[0].RecordContent() != "token" || len(records.Pointing) != 1 || records.Pointing[0].Content != "203.0.113.10" {
		t.Fatalf("records = %#v, want verification and pointing", records)
	}
	if err := client.WaitOperation(ctx, "op123", 0); err != nil {
		t.Fatalf("WaitOperation() error = %v", err)
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

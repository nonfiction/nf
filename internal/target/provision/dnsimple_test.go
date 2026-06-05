package provision

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dnsimple/dnsimple-go/v9/dnsimple"
	"github.com/nonfiction/nf/internal/config"
)

func TestNewDNSimpleProviderRejectsBlankToken(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())
	provider, err := newDNSimpleProvider(context.Background(), "   ", "14")
	if err == nil {
		t.Fatal("newDNSimpleProvider() error = nil, want error")
	}
	if provider != nil {
		t.Fatalf("newDNSimpleProvider() provider = %#v, want nil", provider)
	}
	msg := err.Error()
	if !strings.Contains(msg, "DNSIMPLE_TOKEN") || !strings.Contains(msg, config.EnvFile()) {
		t.Fatalf("newDNSimpleProvider() error = %q, want env reference", msg)
	}
	if strings.Contains(msg, "   ") || strings.Contains(msg, "secret-token") {
		t.Fatalf("newDNSimpleProvider() error leaked token: %q", msg)
	}
}

func TestDNSimpleProviderFindZoneUsesTokenAndUserAgent(t *testing.T) {
	var authHeader, userAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		userAgent = r.Header.Get("User-Agent")
		if got, want := r.URL.Path, "/v2/14/zones/example.test"; got != want {
			t.Fatalf("request path = %q, want %q", got, want)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": 1, "name": "example.test"}})
	}))
	defer srv.Close()

	provider, err := newDNSimpleProvider(context.Background(), "secret-token", "14")
	if err != nil {
		t.Fatalf("newDNSimpleProvider() error = %v", err)
	}
	provider.client.BaseURL = srv.URL
	zone, err := provider.findZone(context.Background(), "example.test")
	if err != nil {
		t.Fatalf("findZone() error = %v", err)
	}
	if got, want := zone, "example.test"; got != want {
		t.Fatalf("findZone() = %q, want %q", got, want)
	}
	if got, want := authHeader, "Bearer secret-token"; got != want {
		t.Fatalf("authorization header = %q, want %q", got, want)
	}
	if !strings.Contains(userAgent, "nf") {
		t.Fatalf("user-agent = %q, want nf", userAgent)
	}
}

func TestDNSimpleProviderUpsertARecordCreatesUpdatesAndUsesDecimalIDs(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		var createBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/v2/14/zones/example.test/records":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
			case r.Method == http.MethodPost && r.URL.Path == "/v2/14/zones/example.test/records":
				body, _ := io.ReadAll(r.Body)
				createBody = string(body)
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": 77496734, "name": "app1", "type": "A", "content": "198.51.100.10", "ttl": 60}})
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
		}))
		defer srv.Close()

		provider, err := newDNSimpleProvider(context.Background(), "secret-token", "14")
		if err != nil {
			t.Fatalf("newDNSimpleProvider() error = %v", err)
		}
		provider.client.BaseURL = srv.URL
		record, action, err := provider.upsertARecord(context.Background(), "example.test", "app1", "198.51.100.10")
		if err != nil {
			t.Fatalf("upsertARecord() error = %v", err)
		}
		if got, want := action, "created"; got != want {
			t.Fatalf("action = %q, want %q", got, want)
		}
		if got, want := record.ID, "77496734"; got != want {
			t.Fatalf("record.ID = %q, want %q", got, want)
		}
		if got, want := record.TTL, 60; got != want {
			t.Fatalf("record.TTL = %d, want %d", got, want)
		}
		if !strings.Contains(createBody, `"ttl":60`) || !strings.Contains(createBody, `"name":"app1"`) {
			t.Fatalf("create body = %s", createBody)
		}
	})

	t.Run("update", func(t *testing.T) {
		var patchBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/v2/14/zones/example.test/records":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": 77496734, "name": "app1", "type": "A", "content": "198.51.100.20", "ttl": 30}}})
			case r.Method == http.MethodPatch && r.URL.Path == "/v2/14/zones/example.test/records/77496734":
				body, _ := io.ReadAll(r.Body)
				patchBody = string(body)
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": 77496734, "name": "app1", "type": "A", "content": "198.51.100.10", "ttl": 60}})
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
		}))
		defer srv.Close()

		provider, err := newDNSimpleProvider(context.Background(), "secret-token", "14")
		if err != nil {
			t.Fatalf("newDNSimpleProvider() error = %v", err)
		}
		provider.client.BaseURL = srv.URL
		record, action, err := provider.upsertARecord(context.Background(), "example.test", "app1", "198.51.100.10")
		if err != nil {
			t.Fatalf("upsertARecord() error = %v", err)
		}
		if got, want := action, "updated"; got != want {
			t.Fatalf("action = %q, want %q", got, want)
		}
		if got, want := record.ID, "77496734"; got != want {
			t.Fatalf("record.ID = %q, want %q", got, want)
		}
		if got, want := record.TTL, 60; got != want {
			t.Fatalf("record.TTL = %d, want %d", got, want)
		}
		if !strings.Contains(patchBody, `"ttl":60`) || !strings.Contains(patchBody, `"content":"198.51.100.10"`) {
			t.Fatalf("patch body = %s", patchBody)
		}
	})

	t.Run("already points", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/v2/14/zones/example.test/records" {
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": 77496734, "name": "app1", "type": "A", "content": "198.51.100.10", "ttl": 60}}})
		}))
		defer srv.Close()

		provider, err := newDNSimpleProvider(context.Background(), "secret-token", "14")
		if err != nil {
			t.Fatalf("newDNSimpleProvider() error = %v", err)
		}
		provider.client.BaseURL = srv.URL
		record, action, err := provider.upsertARecord(context.Background(), "example.test", "app1", "198.51.100.10")
		if err != nil {
			t.Fatalf("upsertARecord() error = %v", err)
		}
		if got, want := action, "already points"; got != want {
			t.Fatalf("action = %q, want %q", got, want)
		}
		if got, want := record.ID, "77496734"; got != want {
			t.Fatalf("record.ID = %q, want %q", got, want)
		}
	})
}

func TestDefaultDNSimpleUpsertARecordPrintsFQDNs(t *testing.T) {
	tests := []struct {
		name        string
		recordName  string
		listResp    map[string]any
		finalMethod string
		finalPath   string
		finalResp   map[string]any
		expect      string
	}{
		{name: "created", recordName: "prod2", listResp: map[string]any{"data": []any{}}, finalMethod: http.MethodPost, finalPath: "/v2/14/zones/nonfiction.dev/records", finalResp: map[string]any{"data": map[string]any{"id": 1, "name": "prod2", "type": "A", "content": "203.0.113.10", "ttl": 60}}, expect: "Created DNS prod2.nonfiction.dev -> 203.0.113.10\n"},
		{name: "updated", recordName: "*.prod2", listResp: map[string]any{"data": []any{map[string]any{"id": 1, "name": "*.prod2", "type": "A", "content": "203.0.113.20", "ttl": 30}}}, finalMethod: http.MethodPatch, finalPath: "/v2/14/zones/nonfiction.dev/records/1", finalResp: map[string]any{"data": map[string]any{"id": 1, "name": "*.prod2", "type": "A", "content": "203.0.113.10", "ttl": 60}}, expect: "Updated DNS *.prod2.nonfiction.dev -> 203.0.113.10\n"},
		{name: "already points", recordName: "prod2", listResp: map[string]any{"data": []any{map[string]any{"id": 1, "name": "prod2", "type": "A", "content": "203.0.113.10", "ttl": 60}}}, expect: "DNS prod2.nonfiction.dev already points to 203.0.113.10\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					if r.Method != http.MethodGet || r.URL.Path != "/v2/14/zones/nonfiction.dev/records" {
						t.Fatalf("unexpected list request: %s %s", r.Method, r.URL.Path)
					}
					_ = json.NewEncoder(w).Encode(tt.listResp)
					return
				}
				if r.Method != tt.finalMethod || r.URL.Path != tt.finalPath {
					t.Fatalf("unexpected final request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.finalResp)
			}))
			defer srv.Close()

			oldFactory := dnsimpleProviderFactory
			dnsimpleProviderFactory = func(ctx context.Context, token, accountID string) (*dnsimpleProvider, error) {
				provider, err := newDNSimpleProvider(ctx, token, accountID)
				if err != nil {
					return nil, err
				}
				provider.client.BaseURL = srv.URL
				return provider, nil
			}
			t.Cleanup(func() { dnsimpleProviderFactory = oldFactory })
			output := captureStdout(t, func() {
				if err := defaultDNSimpleUpsertARecord("secret-token", "14", "nonfiction.dev", tt.recordName, "203.0.113.10"); err != nil {
					t.Fatalf("defaultDNSimpleUpsertARecord() error = %v", err)
				}
			})
			if got := output; got != tt.expect {
				t.Fatalf("output = %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestDNSimpleProviderDeleteARecordDeletesAndUsesDecimalIDs(t *testing.T) {
	t.Run("deleted", func(t *testing.T) {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			switch calls {
			case 1:
				if r.Method != http.MethodGet || r.URL.Path != "/v2/14/zones/example.test/records" {
					t.Fatalf("unexpected list request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": 77496734, "name": "app1", "type": "A", "content": "198.51.100.10", "ttl": 60}}})
			case 2:
				if r.Method != http.MethodDelete || r.URL.Path != "/v2/14/zones/example.test/records/77496734" {
					t.Fatalf("unexpected delete request: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Fatalf("unexpected request count %d", calls)
			}
		}))
		defer srv.Close()

		provider, err := newDNSimpleProvider(context.Background(), "secret-token", "14")
		if err != nil {
			t.Fatalf("newDNSimpleProvider() error = %v", err)
		}
		provider.client.BaseURL = srv.URL
		record, action, err := provider.deleteARecord(context.Background(), "example.test", "app1")
		if err != nil {
			t.Fatalf("deleteARecord() error = %v", err)
		}
		if got, want := action, "deleted"; got != want {
			t.Fatalf("action = %q, want %q", got, want)
		}
		if got, want := record.ID, "77496734"; got != want {
			t.Fatalf("record.ID = %q, want %q", got, want)
		}
	})

	t.Run("already absent", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/v2/14/zones/example.test/records" {
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		}))
		defer srv.Close()

		provider, err := newDNSimpleProvider(context.Background(), "secret-token", "14")
		if err != nil {
			t.Fatalf("newDNSimpleProvider() error = %v", err)
		}
		provider.client.BaseURL = srv.URL
		record, action, err := provider.deleteARecord(context.Background(), "example.test", "app1")
		if err != nil {
			t.Fatalf("deleteARecord() error = %v", err)
		}
		if got, want := action, "already absent"; got != want {
			t.Fatalf("action = %q, want %q", got, want)
		}
		if got, want := record.Name, "app1"; got != want {
			t.Fatalf("record.Name = %q, want %q", got, want)
		}
	})
}

func TestDefaultDNSimpleDeleteARecordPrintsFQDNs(t *testing.T) {
	tests := []struct {
		name   string
		list   map[string]any
		status int
		expect string
	}{
		{name: "deleted", list: map[string]any{"data": []any{map[string]any{"id": 1, "name": "prod2", "type": "A", "content": "203.0.113.10", "ttl": 60}}}, status: http.StatusNoContent, expect: "Deleted DNS prod2.nonfiction.dev\n"},
		{name: "already absent", list: map[string]any{"data": []any{}}, expect: "DNS prod2.nonfiction.dev already absent\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					if r.Method != http.MethodGet || r.URL.Path != "/v2/14/zones/nonfiction.dev/records" {
						t.Fatalf("unexpected list request: %s %s", r.Method, r.URL.Path)
					}
					_ = json.NewEncoder(w).Encode(tt.list)
					return
				}
				if r.Method != http.MethodDelete || r.URL.Path != "/v2/14/zones/nonfiction.dev/records/1" {
					t.Fatalf("unexpected delete request: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			oldFactory := dnsimpleProviderFactory
			dnsimpleProviderFactory = func(ctx context.Context, token, accountID string) (*dnsimpleProvider, error) {
				provider, err := newDNSimpleProvider(ctx, token, accountID)
				if err != nil {
					return nil, err
				}
				provider.client.BaseURL = srv.URL
				return provider, nil
			}
			t.Cleanup(func() { dnsimpleProviderFactory = oldFactory })

			output := captureStdout(t, func() {
				if err := defaultDNSimpleDeleteARecord("secret-token", "14", "nonfiction.dev", "prod2"); err != nil {
					t.Fatalf("defaultDNSimpleDeleteARecord() error = %v", err)
				}
			})
			if got := output; got != tt.expect {
				t.Fatalf("output = %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestDNSimpleProviderWaitForRecordDistribution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/14/zones/example.test/records/77496734/distribution" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"distributed": true}})
	}))
	defer srv.Close()

	provider, err := newDNSimpleProvider(context.Background(), "secret-token", "14")
	if err != nil {
		t.Fatalf("newDNSimpleProvider() error = %v", err)
	}
	provider.client.BaseURL = srv.URL
	if err := provider.waitForRecordDistribution(context.Background(), "example.test", "77496734", time.Second); err != nil {
		t.Fatalf("waitForRecordDistribution() error = %v", err)
	}
}

func TestDNSRecordFromZoneRecordUsesDecimalID(t *testing.T) {
	record := dnsRecordFromZoneRecord(dnsimple.ZoneRecord{ID: 77496734, Name: "app1", Type: "A", Content: "198.51.100.10", TTL: 60})
	if got, want := record.ID, "77496734"; got != want {
		t.Fatalf("record.ID = %q, want %q", got, want)
	}
	if got, want := record.Name, "app1"; got != want {
		t.Fatalf("record.Name = %q, want %q", got, want)
	}
}

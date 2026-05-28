package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveStateRecordsWritesPrettyJSON(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())

	records := []map[string]any{
		{"id": 1, "name": "alpha"},
		{"id": 2, "name": "beta"},
	}
	if err := SaveStateRecords("servers", records); err != nil {
		t.Fatalf("SaveStateRecords() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(os.Getenv("NF_CONFIG_HOME"), "state", "servers.json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	want := "[\n  {\n    \"id\": 1,\n    \"name\": \"alpha\"\n  },\n  {\n    \"id\": 2,\n    \"name\": \"beta\"\n  }\n]\n"
	if got := string(data); got != want {
		t.Fatalf("saved JSON = %q, want %q", got, want)
	}
}

func TestDeleteStateRecordsRemovesMatchingRecordAndPreservesMissingFile(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())

	removed, err := DeleteStateRecords("projects", func(map[string]any) bool { return true })
	if err != nil {
		t.Fatalf("DeleteStateRecords() error on missing file = %v", err)
	}
	if removed != 0 {
		t.Fatalf("DeleteStateRecords() removed = %d, want 0", removed)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("NF_CONFIG_HOME"), "state", "projects.json")); !os.IsNotExist(err) {
		t.Fatalf("missing-file delete created state file or returned unexpected error: %v", err)
	}

	records := []map[string]any{
		{"id": 1, "name": "alpha"},
		{"id": 2, "name": "beta"},
	}
	if err := SaveStateRecords("sites", records); err != nil {
		t.Fatalf("SaveStateRecords() error = %v", err)
	}
	removed, err = DeleteStateRecords("sites", func(record map[string]any) bool {
		return record["name"] == "beta"
	})
	if err != nil {
		t.Fatalf("DeleteStateRecords() error = %v", err)
	}
	if removed != 1 {
		t.Fatalf("DeleteStateRecords() removed = %d, want 1", removed)
	}

	data, err := os.ReadFile(filepath.Join(os.Getenv("NF_CONFIG_HOME"), "state", "sites.json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	want := "[\n  {\n    \"id\": 1,\n    \"name\": \"alpha\"\n  }\n]\n"
	if got := string(data); got != want {
		t.Fatalf("saved JSON after delete = %q, want %q", got, want)
	}
}

func TestLoadStateRecordsSupportsWrappedObjectShapes(t *testing.T) {
	t.Setenv("NF_CONFIG_HOME", t.TempDir())

	servers := map[string]any{
		"servers": map[string]any{
			"app1": map[string]any{"id": 1, "hostname": "app1.nfweb.dev", "provider": "linode", "ssh": map[string]any{"host": "10.0.0.10"}},
		},
	}
	serverData, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(os.Getenv("NF_CONFIG_HOME"), "state"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("NF_CONFIG_HOME"), "state", "servers.json"), append(serverData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	sites := map[string]any{
		"sites": map[string]any{
			"sanjel-app1-production": map[string]any{"provider": "linode", "server": "app1", "hostname": "sanjel.app1.nfweb.dev"},
		},
	}
	siteData, err := json.MarshalIndent(sites, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("NF_CONFIG_HOME"), "state", "sites.json"), append(siteData, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	loadedServers, err := LoadStateRecords("servers")
	if err != nil {
		t.Fatalf("LoadStateRecords(servers) error = %v", err)
	}
	if len(loadedServers) != 1 || loadedServers[0]["_state_key"] != "app1" {
		t.Fatalf("LoadStateRecords(servers) = %#v, want wrapped app1 record", loadedServers)
	}
	loadedSites, err := LoadStateRecords("sites")
	if err != nil {
		t.Fatalf("LoadStateRecords(sites) error = %v", err)
	}
	if len(loadedSites) != 1 || loadedSites[0]["_state_key"] != "sanjel-app1-production" {
		t.Fatalf("LoadStateRecords(sites) = %#v, want wrapped sanjel-app1-production record", loadedSites)
	}
}

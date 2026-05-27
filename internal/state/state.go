package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nonfiction/nf/internal/config"
)

type StateError struct{ Msg string }

func (e StateError) Error() string { return e.Msg }

type Bundle struct {
	Servers  []map[string]any
	Sites    []map[string]any
	Projects []map[string]any
}

func cloneMap(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func loadJSONFile(path string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func recordsFromPayload(payload any, key string) ([]map[string]any, error) {
	if payload == nil {
		return []map[string]any{}, nil
	}
	switch typed := payload.(type) {
	case []any:
		records := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				records = append(records, m)
			}
		}
		return records, nil
	case map[string]any:
		if value, ok := typed[key]; ok {
			if list, ok := value.([]any); ok {
				records := make([]map[string]any, 0, len(list))
				for _, item := range list {
					if m, ok := item.(map[string]any); ok {
						records = append(records, m)
					}
				}
				return records, nil
			}
		}
		allMaps := true
		for _, value := range typed {
			if _, ok := value.(map[string]any); !ok {
				allMaps = false
				break
			}
		}
		if allMaps {
			records := make([]map[string]any, 0, len(typed))
			for name, value := range typed {
				record := cloneMap(value.(map[string]any))
				record["_state_key"] = name
				records = append(records, record)
			}
			return records, nil
		}
	}
	return nil, StateError{Msg: fmt.Sprintf("Unsupported JSON shape in %s.json", key)}
}

func LoadStateRecords(kind string) ([]map[string]any, error) {
	path := StatePath(kind)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return []map[string]any{}, nil
		}
		return nil, err
	}
	payload, err := loadJSONFile(path)
	if err != nil {
		return nil, err
	}
	return recordsFromPayload(payload, kind)
}

func StatePath(kind string) string {
	return filepath.Join(config.StateDir(), kind+".json")
}

func SaveStateRecords(kind string, records []map[string]any) error {
	path := StatePath(kind)
	if records == nil {
		records = []map[string]any{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func DeleteStateRecords(kind string, remove func(map[string]any) bool) (int, error) {
	path := StatePath(kind)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	records, err := LoadStateRecords(kind)
	if err != nil {
		return 0, err
	}
	kept := make([]map[string]any, 0, len(records))
	removed := 0
	for _, record := range records {
		if remove(record) {
			removed++
			continue
		}
		kept = append(kept, record)
	}
	if removed == 0 {
		return 0, nil
	}
	if err := SaveStateRecords(kind, kept); err != nil {
		return 0, err
	}
	return removed, nil
}

func LoadStateBundle() (Bundle, error) {
	servers, err := LoadStateRecords("servers")
	if err != nil {
		return Bundle{}, err
	}
	sites, err := LoadStateRecords("sites")
	if err != nil {
		return Bundle{}, err
	}
	projects, err := LoadStateRecords("projects")
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{Servers: servers, Sites: sites, Projects: projects}, nil
}

func MatchingRecord(records []map[string]any, needle string) map[string]any {
	normalized := strings.ToLower(strings.TrimSpace(needle))
	if normalized == "" {
		return nil
	}
	candidateFields := []string{"id", "_state_key", "name", "slug", "hostname", "label"}
	matches := func(value any) bool {
		str, ok := value.(string)
		return ok && strings.ToLower(strings.TrimSpace(str)) == normalized
	}
	for _, field := range candidateFields {
		for _, record := range records {
			if matches(record[field]) {
				return record
			}
		}
	}
	for _, record := range records {
		for _, value := range record {
			if matches(value) {
				return record
			}
		}
	}
	return nil
}

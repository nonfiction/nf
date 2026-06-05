package cli

// Generic CLI helpers for config fallback, git/project discovery, table output,
// record matching, and Linode deletion plumbing.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/target/provision"
)

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func maskSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	prefix := 3
	if len(value) < prefix {
		prefix = len(value)
	}
	return value[:prefix] + strings.Repeat("*", 11)
}

func currentGitRoot() (string, bool) {
	wd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	return discoverGitRoot(wd)
}

func currentGitRootBase() (string, error) {
	root, ok := currentGitRoot()
	if !ok {
		return "", ProjectError{Msg: "init requires a .git repository above the current directory when --project-slug is not set"}
	}
	base := filepath.Base(filepath.Clean(root))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "", ProjectError{Msg: fmt.Sprintf("init could not derive a project slug from git root %q; pass --project-slug", root)}
	}
	return base, nil
}

func discoverGitRoot(start string) (string, bool) {
	if strings.TrimSpace(start) == "" {
		return "", false
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	if info, err := os.Stat(abs); err == nil && !info.IsDir() {
		abs = filepath.Dir(abs)
	}
	for {
		if info, err := os.Stat(filepath.Join(abs, ".git")); err == nil {
			if info.IsDir() || !info.IsDir() {
				return abs, true
			}
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", false
		}
		abs = parent
	}
}

func projectContextAvailable() bool {
	_, ok := currentNFProjectRoot()
	return ok
}

func requireProjectContext(command string) error {
	if _, ok := currentNFProjectRoot(); !ok {
		return ProjectError{Msg: fmt.Sprintf("%s requires an nf project with .nf next to .git", command)}
	}
	return nil
}

func currentNFProjectRoot() (string, bool) {
	root, ok := currentGitRoot()
	if !ok {
		return "", false
	}
	info, err := os.Stat(filepath.Join(root, ".nf"))
	if err != nil || !info.IsDir() {
		return "", false
	}
	return root, true
}

func formatTable(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	lines := make([]string, 0, len(rows)*2)
	for i, row := range rows {
		parts := make([]string, len(row))
		for j, cell := range row {
			parts[j] = cell + strings.Repeat(" ", widths[j]-len(cell))
		}
		lines = append(lines, strings.TrimRight(strings.Join(parts, "  "), " "))
		if i == 0 {
			headers := make([]string, len(widths))
			for j, width := range widths {
				headers[j] = strings.Repeat("-", width)
			}
			lines = append(lines, strings.TrimRight(strings.Join(headers, "  "), " "))
		}
	}
	return strings.Join(lines, "\n")
}

func renderCommandRun(run any) string {
	switch typed := run.(type) {
	case string:
		return typed
	case []string:
		return strings.Join(typed, " ")
	case repoCommandRunner:
		return typed.Render()
	default:
		return fmt.Sprint(run)
	}
}

func recordValueString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		value := strings.TrimSpace(typed)
		if strings.ContainsAny(value, "eE") {
			if parsed, err := strconv.ParseFloat(value, 64); err == nil && parsed == float64(int64(parsed)) {
				return strconv.FormatInt(int64(parsed), 10)
			}
		}
		return value
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		f := float64(typed)
		if f == float64(int64(f)) {
			return strconv.FormatInt(int64(f), 10)
		}
		return strconv.FormatFloat(f, 'f', -1, 32)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case json.Number:
		return typed.String()
	default:
		value := strings.TrimSpace(fmt.Sprint(typed))
		if value == "<nil>" {
			return ""
		}
		return value
	}
}

func recordStringValues(record map[string]any, keys ...string) []string {
	values := make([]string, 0, len(keys))
	seen := map[string]struct{}{}
	for _, key := range keys {
		value := recordValueString(record[key])
		if value == "" || value == "<nil>" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values
}

func recordMatchesAnyValue(record map[string]any, keys, values []string) bool {
	if len(values) == 0 {
		return false
	}
	needle := map[string]struct{}{}
	for _, value := range values {
		needle[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
	}
	for _, key := range keys {
		value := strings.ToLower(recordValueString(record[key]))
		if value == "" || value == "<nil>" {
			continue
		}
		if _, ok := needle[value]; ok {
			return true
		}
	}
	return false
}

func serverIdentityValues(server map[string]any) []string {
	return recordStringValues(server, "id", "linode_id", "_state_key", "name", "slug", "hostname", "label")
}

func siteMatchesServer(site, server map[string]any) bool {
	return recordMatchesAnyValue(site, []string{"server_id", "server", "server_name", "server_hostname", "server_label", "hostname", "label"}, serverIdentityValues(server))
}

func siteMatchesTarget(site, target map[string]any) bool {
	return recordMatchesAnyValue(site, []string{"target", "target_name", "server_id", "server", "server_name", "server_hostname", "server_label", "hostname", "label"}, serverIdentityValues(target))
}

func serverMatchesRecord(record, server map[string]any) bool {
	return recordMatchesAnyValue(record, []string{"id", "linode_id", "_state_key", "name", "slug", "hostname", "label"}, serverIdentityValues(server))
}

func linodeTokenEnv() (string, error) {
	if token := envwizard.Value("LINODE_TOKEN"); token != "" {
		return token, nil
	}
	if token := envwizard.Value("LINODE_CLI_TOKEN"); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("Expected LINODE_TOKEN in the environment or %s. LINODE_CLI_TOKEN is also accepted for convenience.", config.EnvFile())
}

func runLinodeDelete(id string) error {
	token, err := linodeTokenEnv()
	if err != nil {
		return err
	}
	linodeID, err := strconv.Atoi(strings.TrimSpace(id))
	if err != nil {
		return fmt.Errorf("invalid Linode id %q", id)
	}
	client := provision.NewLinodeClient(token)
	if err := client.DeleteInstance(context.Background(), linodeID); err != nil {
		return fmt.Errorf("deleting Linode: %w", err)
	}
	return nil
}

func isLinodeNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "request failed: 404") || strings.Contains(message, "not found")
}

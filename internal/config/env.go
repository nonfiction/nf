package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func ReadEnvFile(path string) (map[string]string, error) {
	values := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return values, nil
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			first, last := value[0], value[len(value)-1]
			if first == last && (first == '"' || first == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func ensureEnvFileDir(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

func writeEnvFile(path string, lines []string) error {
	if err := ensureEnvFileDir(path); err != nil {
		return err
	}
	content := strings.Join(lines, "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func formatEnvAssignments(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s=%s", key, values[key]))
	}
	return lines
}

func UpdateEnvFile(path string, updates map[string]string) ([]string, error) {
	return updateEnvFile(path, updates, false)
}

func SetEnvFile(path string, updates map[string]string) ([]string, error) {
	return updateEnvFile(path, updates, true)
}

func updateEnvFile(path string, updates map[string]string, overwrite bool) ([]string, error) {
	if len(updates) == 0 {
		return nil, nil
	}

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if os.IsNotExist(err) {
		written := make([]string, 0, len(updates))
		for key := range updates {
			written = append(written, key)
		}
		sort.Strings(written)
		if err := writeEnvFile(path, formatEnvAssignments(updates)); err != nil {
			return nil, err
		}
		return written, nil
	}

	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	seen := map[string]bool{}
	written := make([]string, 0, len(updates))

	for i, rawLine := range lines {
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		prefix := ""
		content := trimmed
		if strings.HasPrefix(content, "export ") {
			prefix = "export "
			content = strings.TrimSpace(strings.TrimPrefix(content, "export "))
		}
		key, value, ok := strings.Cut(content, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		seen[key] = true
		update, ok := updates[key]
		if !ok {
			continue
		}
		if strings.TrimSpace(value) != "" && !overwrite {
			continue
		}
		lines[i] = fmt.Sprintf("%s%s=%s", prefix, key, update)
		written = append(written, key)
	}

	keys := make([]string, 0, len(updates))
	for key := range updates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := updates[key]
		if seen[key] {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s=%s", key, value))
		written = append(written, key)
	}

	sort.Strings(written)
	if err := writeEnvFile(path, lines); err != nil {
		return nil, err
	}
	return written, nil
}

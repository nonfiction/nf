package cli

import (
	"strings"
)

func validateDBDefaultUser(user string) error {
	trimmed := strings.TrimSpace(user)
	if trimmed == "" {
		return ProjectError{Msg: "db_default_user must be a non-empty MySQL username"}
	}
	if len(trimmed) > 32 {
		return ProjectError{Msg: "db_default_user must be 32 characters or fewer"}
	}
	for _, r := range trimmed {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return ProjectError{Msg: "db_default_user must use only letters, numbers, underscores, and hyphens"}
	}
	return nil
}

func targetDBURL(record map[string]any) string {
	db := targetDBMetadata(record)
	if len(db) == 0 {
		return ""
	}
	return firstNonEmpty(recordValueString(db["url"]), deriveAdminerURLFromHostname(recordValueString(db["hostname"])))
}

func targetDBUser(record map[string]any) string {
	db := targetDBMetadata(record)
	if len(db) == 0 {
		return ""
	}
	return firstNonEmpty(recordValueString(db["user"]), mapStringAtPath(record, "db", "auth", "user"), mapStringAtPath(record, "adminer", "auth", "user"))
}

func targetDBMetadata(record map[string]any) map[string]any {
	if db, ok := record["db"].(map[string]any); ok {
		return db
	}
	if adminer, ok := record["adminer"].(map[string]any); ok {
		return adminer
	}
	return nil
}

func deriveAdminerURLFromHostname(hostname string) string {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return ""
	}
	return "https://" + hostname + "/"
}

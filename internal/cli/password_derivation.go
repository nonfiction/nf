package cli

import (
	"strings"

	"github.com/nonfiction/nf/internal/passwords"
)

func normalizedPasswordVersion(value any) string {
	version := strings.TrimSpace(recordValueString(value))
	if version == "0" {
		return ""
	}
	return version
}

func deriveProjectPassword(slug, purpose, version string) (string, error) {
	salt, err := passwords.SecretSalt()
	if err != nil {
		return "", err
	}
	slug = strings.TrimSpace(slug)
	version = normalizedPasswordVersion(version)
	if version != "" {
		slug += ":v" + version
	}
	return passwords.DerivePassword(slug, purpose, salt), nil
}

func projectPasswordVersion(metadata map[string]any) string {
	return normalizedPasswordVersion(mapStringAtPath(metadata, "project", "password_version"))
}

func currentProjectPasswordVersionForSite(siteSlug string) string {
	root, ok := currentGitRoot()
	if !ok {
		return ""
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		return ""
	}
	if normalizedRecordString(mapStringAtPath(metadata, "project", "slug")) != normalizedRecordString(siteSlug) {
		return ""
	}
	return projectPasswordVersion(metadata)
}

func deriveSiteBasicAuthPassword(slug string) (string, error) {
	version := currentProjectPasswordVersionForSite(slug)
	return deriveProjectPassword(slug, "basic-auth", version)
}

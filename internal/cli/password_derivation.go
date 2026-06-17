package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/ui"
)

const (
	passwordDeriveScopeWPAdmin    = "wp-admin"
	passwordDeriveScopeMySQL      = "mysql"
	passwordDeriveScopeBasicAuth  = "basic-auth"
	passwordDeriveScopeLinodeRoot = "linode-root"
	passwordDeriveScopeDBAdmin    = "db-admin"
)

func normalizedPasswordVersion(value any) string {
	version := strings.TrimSpace(recordValueString(value))
	if version == "0" {
		return ""
	}
	return version
}

func parseExplicitPasswordVersion(value string) (string, error) {
	version := strings.TrimSpace(value)
	if version == "" {
		return "", fmt.Errorf("password version must be an unsigned integer")
	}
	for _, r := range version {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("password version %q must be an unsigned integer", value)
		}
	}
	parsed, err := strconv.ParseUint(version, 10, 64)
	if err != nil {
		return "", fmt.Errorf("password version %q must be an unsigned integer", value)
	}
	if parsed == 0 {
		return "", nil
	}
	return strconv.FormatUint(parsed, 10), nil
}

func passwordDeriveScopeCandidates() []string {
	return []string{passwordDeriveScopeWPAdmin, passwordDeriveScopeMySQL, passwordDeriveScopeBasicAuth, passwordDeriveScopeLinodeRoot, passwordDeriveScopeDBAdmin}
}

func passwordDeriveScopeOptions() []ui.SelectOption {
	labels := map[string]string{
		passwordDeriveScopeWPAdmin:    "wp-admin - project slug (client)",
		passwordDeriveScopeMySQL:      "mysql - project slug (client)",
		passwordDeriveScopeBasicAuth:  "basic-auth - project slug (client)",
		passwordDeriveScopeLinodeRoot: "linode-root - target hostname (app1-linode.nonfiction.dev)",
		passwordDeriveScopeDBAdmin:    "db-admin - target hostname (app1-linode.nonfiction.dev)",
	}
	options := make([]ui.SelectOption, 0, len(passwordDeriveScopeCandidates()))
	for _, scope := range passwordDeriveScopeCandidates() {
		options = append(options, ui.SelectOption{Value: scope, Label: labels[scope]})
	}
	return options
}

func passwordDeriveIdentityPrompt(scope string) string {
	switch strings.TrimSpace(scope) {
	case passwordDeriveScopeWPAdmin, passwordDeriveScopeMySQL, passwordDeriveScopeBasicAuth:
		return "Site/project slug (example: client)"
	case passwordDeriveScopeLinodeRoot, passwordDeriveScopeDBAdmin:
		return "Target hostname (example: app1-linode.nonfiction.dev)"
	default:
		return "Password identity"
	}
}

func passwordDeriveScopeUsesProjectVersion(scope string) bool {
	switch strings.TrimSpace(scope) {
	case passwordDeriveScopeWPAdmin, passwordDeriveScopeMySQL, passwordDeriveScopeBasicAuth:
		return true
	default:
		return false
	}
}

func passwordDeriveDefaultVersion(scope, identity string) string {
	if !passwordDeriveScopeUsesProjectVersion(scope) {
		return ""
	}
	return currentProjectPasswordVersionForSite(identity)
}

func passwordDeriveIdentity(scope, identity, version string) string {
	identity = strings.TrimSpace(identity)
	if !passwordDeriveScopeUsesProjectVersion(scope) {
		return identity
	}
	version = normalizedPasswordVersion(version)
	if version == "" {
		return identity
	}
	return identity + ":v" + version
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

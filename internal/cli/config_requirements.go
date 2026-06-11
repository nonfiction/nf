package cli

// Global setup requirements and small naming helpers shared by provider and
// password flows.

import (
	"strings"

	"github.com/nonfiction/nf/internal/envwizard"
)

func configInitRequirements() []envwizard.Requirement {
	return []envwizard.Requirement{
		{Keys: []string{"DNSIMPLE_TOKEN"}, Prompt: "DNSimple token: ", Secret: true, WriteKey: "DNSIMPLE_TOKEN", Required: true},
		{Keys: []string{"KINSTA_API_KEY"}, Prompt: "Kinsta API key: ", Secret: true, WriteKey: "KINSTA_API_KEY", Required: true},
		{Keys: []string{"LINODE_TOKEN", "LINODE_CLI_TOKEN"}, Prompt: "LINODE_TOKEN (Linode API token): ", Secret: true, WriteKey: "LINODE_TOKEN", Required: true},
		{Keys: []string{"NF_PASSWORD_SALT", "NF_SECRET_SALT"}, Prompt: "NF_PASSWORD_SALT (used for derived passwords): ", Secret: true, WriteKey: "NF_PASSWORD_SALT", Required: true},
	}
}

type configInitSetting struct {
	Key      string
	Prompt   string
	Default  string
	Required bool
	Validate func(string) error
}

func configInitSettings() []configInitSetting {
	return []configInitSetting{
		{Key: "base_domain", Prompt: "Base domain: ", Required: true},
		{Key: "default_wp_email", Prompt: "Default WordPress email: ", Required: true},
		{Key: "default_wp_user", Prompt: "Default WordPress user: ", Default: "admin", Required: true},
		{Key: "basicauth_default_user", Prompt: "Basic auth default user: ", Default: "nonfiction", Required: true},
		{Key: "adminer_default_user", Prompt: "Adminer default user: ", Default: "adminer", Required: true, Validate: validateAdminerDefaultUser},
		{Key: "kinsta_default_php", Prompt: "Kinsta default PHP version: ", Default: "8.3", Required: true},
		{Key: "linode_default_region", Prompt: "Linode default region: ", Default: "ca-central", Required: true},
		{Key: "linode_default_user", Prompt: "Linode default SSH user: ", Default: "nonfiction", Required: true},
		{Key: "linode_default_type", Prompt: "Linode default type: ", Default: "g6-standard-1", Required: true},
	}
}

func passwordRequirements() []envwizard.Requirement {
	return []envwizard.Requirement{
		{Keys: []string{"NF_PASSWORD_SALT", "NF_SECRET_SALT"}, Prompt: "NF_PASSWORD_SALT (used for derived passwords): ", Secret: true, WriteKey: "NF_PASSWORD_SALT", Required: true},
	}
}

func slugToTitle(value string) string {
	parts := strings.Split(strings.ReplaceAll(value, "_", "-"), "-")
	titles := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		titles = append(titles, strings.ToUpper(part[:1])+strings.ToLower(part[1:]))
	}
	return strings.Join(titles, " ")
}

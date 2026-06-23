package cli

import (
	"fmt"
	"net/mail"
	"os"
	"strconv"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/ui"
)

type configKeySpec struct {
	Key               string
	Group             string
	Label             string
	Description       string
	StorageKey        string
	LegacyStorageKeys []string
	EnvKey            string
	LegacyEnvKeys     []string
	Default           string
	Sensitive         bool
	CanUnset          bool
	Normalize         func(string) (string, error)
	Validate          func(string) error
}

type configResolvedValue struct {
	Value  string
	Source string
}

var configKeyRegistry = []configKeySpec{
	{Key: "core.base-domain", Group: "Core", Label: "Base domain", Description: "provider base domain", StorageKey: "base_domain", CanUnset: true, Normalize: normalizeConfigBaseDomain},
	{Key: "core.password-salt", Group: "Core", Label: "Password salt", Description: "password derivation salt", EnvKey: "NF_PASSWORD_SALT", LegacyEnvKeys: []string{"NF_SECRET_SALT"}, Sensitive: true, CanUnset: true},

	{Key: "wordpress.admin-email", Group: "WordPress", Label: "Admin email", Description: "default WordPress admin email", StorageKey: "default_wp_email", CanUnset: true, Validate: validateConfigEmail},
	{Key: "wordpress.admin-user", Group: "WordPress", Label: "Admin user", Description: "default WordPress admin username", StorageKey: "default_wp_user", Default: defaultWordPressAdminUser, CanUnset: true, Validate: validateConfigSimpleUser("wordpress.admin-user")},
	{Key: "wordpress.basic-auth-user", Group: "WordPress", Label: "Basic auth user", Description: "default basic-auth username", StorageKey: "basicauth_default_user", Default: "nonfiction", CanUnset: true, Validate: validateConfigSimpleUser("wordpress.basic-auth-user")},

	{Key: "database.user", Group: "Database", Label: "User", Description: "default database user", StorageKey: "db_default_user", LegacyStorageKeys: []string{"adminer_default_user"}, Default: defaultDatabaseUser, CanUnset: true, Validate: validateDBDefaultUser},

	{Key: "docker.user", Group: "Docker", Label: "User", Description: "default Docker shell user", StorageKey: "docker_user", Default: defaultDockerUser, CanUnset: true, Validate: validateDockerUser},
	{Key: "docker.images.db", Group: "Docker", Label: "DB image", Description: "default Docker database image", StorageKey: "docker_db_image", Default: defaultDockerDBImage, CanUnset: true, Validate: validateConfigNoWhitespace("docker.images.db")},
	{Key: "docker.images.wordpress", Group: "Docker", Label: "WordPress image", Description: "default Docker WordPress image", StorageKey: "docker_wordpress_image", Default: defaultDockerWordpressImage, CanUnset: true, Validate: validateConfigNoWhitespace("docker.images.wordpress")},

	{Key: "dnsimple.account-id", Group: "DNSimple", Label: "Account ID", Description: "DNSimple account ID", StorageKey: "dnsimple_account_id", CanUnset: true, Validate: validateConfigNumeric("dnsimple.account-id")},

	{Key: "kinsta.region", Group: "Kinsta", Label: "Region", Description: "default Kinsta region", StorageKey: "kinsta_default_region", CanUnset: true, Validate: validateConfigNoWhitespace("kinsta.region")},
	{Key: "kinsta.php", Group: "Kinsta", Label: "PHP", Description: "default Kinsta PHP version", StorageKey: "kinsta_default_php", Default: "8.3", CanUnset: true, Validate: validateKinstaPHPVersion},

	{Key: "linode.region", Group: "Linode", Label: "Region", Description: "default Linode region", StorageKey: "linode_default_region", Default: "ca-central", CanUnset: true, Validate: validateConfigNoWhitespace("linode.region")},
	{Key: "linode.type", Group: "Linode", Label: "Type", Description: "default Linode instance type", StorageKey: "linode_default_type", Default: "g6-standard-1", CanUnset: true, Validate: validateConfigNoWhitespace("linode.type")},
	{Key: "linode.image", Group: "Linode", Label: "Image", Description: "default Linode image", StorageKey: "linode_default_image", CanUnset: true, Validate: validateConfigNoWhitespace("linode.image")},
	{Key: "linode.user", Group: "Linode", Label: "User", Description: "default Linode SSH user", StorageKey: "linode_default_user", Default: "nonfiction", CanUnset: true, Validate: validateConfigLinuxUser("linode.user")},
}

func lookupConfigKey(key string) (configKeySpec, bool) {
	key = strings.TrimSpace(key)
	for _, spec := range configKeyRegistry {
		if spec.Key == key {
			return spec, true
		}
	}
	return configKeySpec{}, false
}

func configKeyNames() []string {
	names := make([]string, 0, len(configKeyRegistry))
	for _, spec := range configKeyRegistry {
		names = append(names, spec.Key)
	}
	return names
}

func configKeySelectOptions() []ui.SelectOption {
	options := make([]ui.SelectOption, 0, len(configKeyRegistry))
	for _, spec := range configKeyRegistry {
		label := spec.Key
		if spec.Description != "" {
			label += " -- " + spec.Description
		}
		options = append(options, ui.SelectOption{Label: label, Value: spec.Key})
	}
	return options
}

func (s configKeySpec) path() string {
	if s.EnvKey != "" {
		return config.EnvFile()
	}
	return config.ConfigFile()
}

func (s configKeySpec) resolve(values map[string]string) configResolvedValue {
	if s.EnvKey != "" {
		for _, key := range append([]string{s.EnvKey}, s.LegacyEnvKeys...) {
			if value := strings.TrimSpace(os.Getenv(key)); value != "" {
				return configResolvedValue{Value: value, Source: "env"}
			}
		}
		envValues, err := config.ReadEnvFile(config.EnvFile())
		if err == nil {
			for _, key := range append([]string{s.EnvKey}, s.LegacyEnvKeys...) {
				if value := strings.TrimSpace(envValues[key]); value != "" {
					return configResolvedValue{Value: value, Source: "global"}
				}
			}
		}
		return configResolvedValue{Source: "unset"}
	}

	if value := strings.TrimSpace(values[s.StorageKey]); value != "" {
		return configResolvedValue{Value: value, Source: "global"}
	}
	for _, key := range s.LegacyStorageKeys {
		if value := strings.TrimSpace(values[key]); value != "" {
			return configResolvedValue{Value: value, Source: "global"}
		}
	}
	if strings.TrimSpace(s.Default) != "" {
		return configResolvedValue{Value: strings.TrimSpace(s.Default), Source: "default"}
	}
	return configResolvedValue{Source: "unset"}
}

func (s configKeySpec) displayValue(values map[string]string) string {
	resolved := s.resolve(values)
	if s.Sensitive {
		if resolved.Source == "unset" {
			return "unset"
		}
		return "set"
	}
	if resolved.Source == "unset" {
		return "unset"
	}
	if resolved.Source == "default" {
		return resolved.Value + " (default)"
	}
	return resolved.Value
}

func (s configKeySpec) getValue(values map[string]string) string {
	resolved := s.resolve(values)
	if s.Sensitive {
		if resolved.Source == "unset" {
			return "unset"
		}
		return "set"
	}
	if resolved.Source == "unset" {
		return "unset"
	}
	return resolved.Value
}

func (s configKeySpec) promptDefault(values map[string]string) string {
	if s.Sensitive {
		return ""
	}
	resolved := s.resolve(values)
	if resolved.Source == "unset" {
		return ""
	}
	return resolved.Value
}

func (s configKeySpec) setValue(values map[string]string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s cannot be empty", s.Key)
	}
	if s.Normalize != nil {
		normalized, err := s.Normalize(value)
		if err != nil {
			return "", err
		}
		value = normalized
	}
	if s.Validate != nil {
		if err := s.Validate(value); err != nil {
			return "", err
		}
	}
	if s.EnvKey != "" {
		_, err := config.SetEnvFile(config.EnvFile(), map[string]string{s.EnvKey: value})
		return value, err
	}
	values[s.StorageKey] = value
	return value, nil
}

func (s configKeySpec) unsetValue(values map[string]string) error {
	if !s.CanUnset {
		return fmt.Errorf("%s cannot be unset", s.Key)
	}
	if s.EnvKey != "" {
		_, err := config.UnsetEnvFile(config.EnvFile(), append([]string{s.EnvKey}, s.LegacyEnvKeys...))
		return err
	}
	delete(values, s.StorageKey)
	for _, key := range s.LegacyStorageKeys {
		delete(values, key)
	}
	return nil
}

func (s configKeySpec) safeSetOutput(value string) string {
	if s.Sensitive {
		return "set"
	}
	return value
}

func normalizeConfigBaseDomain(value string) (string, error) {
	return normalizePublicDomain(value)
}

func validateConfigEmail(value string) error {
	trimmed := strings.TrimSpace(value)
	addr, err := mail.ParseAddress(trimmed)
	if err != nil || addr.Address != trimmed || !strings.Contains(trimmed, "@") {
		return ProjectError{Msg: fmt.Sprintf("wordpress.admin-email must be an email address")}
	}
	return nil
}

func validateConfigSimpleUser(key string) func(string) error {
	return func(value string) error {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || strings.Contains(trimmed, ":") || strings.ContainsAny(trimmed, " \t\r\n") {
			return ProjectError{Msg: fmt.Sprintf("%s must be a non-empty username without whitespace or colon", key)}
		}
		return nil
	}
}

func validateConfigNoWhitespace(key string) func(string) error {
	return func(value string) error {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, " \t\r\n") {
			return ProjectError{Msg: fmt.Sprintf("%s must be a non-empty value without whitespace", key)}
		}
		return nil
	}
}

func validateConfigNumeric(key string) func(string) error {
	return func(value string) error {
		if _, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64); err != nil {
			return ProjectError{Msg: fmt.Sprintf("%s must be numeric", key)}
		}
		return nil
	}
}

func validateKinstaPHPVersion(value string) error {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) == 0 || len(parts) > 2 {
		return ProjectError{Msg: "kinsta.php must be a PHP version like 8.3"}
	}
	for _, part := range parts {
		if part == "" {
			return ProjectError{Msg: "kinsta.php must be a PHP version like 8.3"}
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return ProjectError{Msg: "kinsta.php must be a PHP version like 8.3"}
			}
		}
	}
	return nil
}

func validateConfigLinuxUser(key string) func(string) error {
	return func(value string) error {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return ProjectError{Msg: fmt.Sprintf("%s must be a non-empty Linux username", key)}
		}
		if len(trimmed) > 32 {
			return ProjectError{Msg: fmt.Sprintf("%s must be 32 characters or fewer", key)}
		}
		for i, r := range trimmed {
			if r >= 'a' && r <= 'z' || r == '_' || i > 0 && r >= '0' && r <= '9' || i > 0 && r == '-' {
				continue
			}
			return ProjectError{Msg: fmt.Sprintf("%s must start with a lowercase letter or underscore and use only lowercase letters, numbers, underscores, and hyphens", key)}
		}
		return nil
	}
}

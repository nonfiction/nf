package cli

// Global config commands and provider refresh after init.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/state"
)

func runConfig(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runConfigHelp()
	}
	switch argv[0] {
	case "init":
		fs := flag.NewFlagSet("config init", flag.ContinueOnError)
		nonInteractive := fs.Bool("non-interactive", false, "")
		fs.SetOutput(os.Stderr)
		if err := fs.Parse(argv[1:]); err != nil {
			if err == flag.ErrHelp {
				return 0
			}
			return 1
		}
		return cmdConfigInit(*nonInteractive)
	case "set-base-domain":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-base-domain takes exactly one domain")
			return 1
		}
		return cmdConfigSet("base_domain", argv[1])
	case "set-default-wp-email":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-default-wp-email takes exactly one email")
			return 1
		}
		return cmdConfigSet("default_wp_email", argv[1])
	case "set-default-wp-user":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-default-wp-user takes exactly one user")
			return 1
		}
		return cmdConfigSet("default_wp_user", argv[1])
	case "set-basicauth-default-user":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-basicauth-default-user takes exactly one user")
			return 1
		}
		return cmdConfigSet("basicauth_default_user", argv[1])
	case "set-db-default-user":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-db-default-user takes exactly one user")
			return 1
		}
		if err := validateDBDefaultUser(argv[1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return cmdConfigSet("db_default_user", argv[1])
	case "set-docker-db-image":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-docker-db-image takes exactly one image")
			return 1
		}
		return cmdConfigSet("docker_db_image", argv[1])
	case "set-docker-wordpress-image":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-docker-wordpress-image takes exactly one image")
			return 1
		}
		return cmdConfigSet("docker_wordpress_image", argv[1])
	case "set-docker-user":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-docker-user takes exactly one user")
			return 1
		}
		if err := validateDockerUser(argv[1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return cmdConfigSet("docker_user", argv[1])
	case "set-kinsta-default-region":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-kinsta-default-region takes exactly one region")
			return 1
		}
		return cmdConfigSet("kinsta_default_region", argv[1])
	case "set-kinsta-default-php":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-kinsta-default-php takes exactly one version")
			return 1
		}
		return cmdConfigSet("kinsta_default_php", argv[1])
	case "set-linode-default-region":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-linode-default-region takes exactly one region")
			return 1
		}
		return cmdConfigSet("linode_default_region", argv[1])
	case "set-linode-default-type":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-linode-default-type takes exactly one type")
			return 1
		}
		return cmdConfigSet("linode_default_type", argv[1])
	case "set-linode-default-image":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-linode-default-image takes exactly one image")
			return 1
		}
		return cmdConfigSet("linode_default_image", argv[1])
	case "set-linode-default-user":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config set-linode-default-user takes exactly one user")
			return 1
		}
		return cmdConfigSet("linode_default_user", argv[1])
	case "show":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "config show takes no arguments")
			return 1
		}
		return cmdConfigShow()
	default:
		fmt.Fprintln(os.Stderr, "unsupported config command")
		return 1
	}
}

func loadGlobalConfig() (map[string]string, error) {
	path := config.ConfigFile()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	values := map[string]string{}
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func saveGlobalConfig(values map[string]string) error {
	path := config.ConfigFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func cmdConfigInit(nonInteractive bool) int {
	if err := envwizard.Init(configInitRequirements(), nonInteractive); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := initGlobalConfig(configInitSettings(), nonInteractive); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := checkProvidersAfterConfigInit(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func checkProvidersAfterConfigInit() error {
	fmt.Println("Checking providers...")
	failed := []string{}
	for _, status := range providerConfigStatuses() {
		if cmdProviderCheck(status.Name, false) != 0 {
			failed = append(failed, status.Name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("provider checks failed: %s", strings.Join(failed, ", "))
	}
	return nil
}

func cmdTargetRefresh() int {
	fmt.Println("Target refresh updates target metadata from configured providers.")
	refreshed := []string{}
	skipped := []string{}
	failed := []string{}
	totalTargets := 0
	for _, status := range providerConfigStatuses() {
		if !providerHasTargets(status.Name) {
			continue
		}
		if len(status.Missing) > 0 {
			skipped = append(skipped, fmt.Sprintf("%s (missing %s)", status.Name, providerMissingLabel(status)))
			continue
		}
		result, err := runProviderHealthcheck(status.Name)
		if err != nil {
			failed = append(failed, status.Name)
			fmt.Fprintf(os.Stderr, "%s: %v\n", status.Name, err)
			continue
		}
		if result.Provider == "" {
			result.Provider = status.Name
		}
		if err := saveProviderHealthRecord(result); err != nil {
			failed = append(failed, status.Name)
			fmt.Fprintf(os.Stderr, "%s: %v\n", status.Name, err)
			continue
		}
		count := len(targetMaps(result.Record["targets"]))
		totalTargets += count
		refreshed = append(refreshed, status.Name)
		fmt.Printf("Provider %s refreshed. Targets: %d\n", status.Name, count)
	}
	if len(skipped) > 0 {
		fmt.Printf("Skipped providers: %d\n", len(skipped))
		for _, line := range skipped {
			fmt.Printf("  - %s\n", line)
		}
	}
	if len(refreshed) > 0 {
		fmt.Printf("Refreshed providers: %d\n", len(refreshed))
		fmt.Printf("Targets: %d\n", totalTargets)
		fmt.Printf("Saved provider metadata to %s.\n", state.StatePath("providers"))
	}
	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "target refresh failed for providers: %s\n", strings.Join(failed, ", "))
		return 1
	}
	if len(refreshed) == 0 {
		fmt.Println("No target providers were refreshed.")
		return 1
	}
	return 0
}

func providerHasTargets(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "kinsta", "linode":
		return true
	default:
		return false
	}
}

func initGlobalConfig(settings []configInitSetting, nonInteractive bool) error {
	values, err := loadGlobalConfig()
	if err != nil {
		return err
	}
	updates := map[string]string{}
	for _, setting := range settings {
		if configInitSettingHasValue(values, setting) {
			continue
		}
		if nonInteractive || !configIsInteractive() {
			if strings.TrimSpace(setting.Default) != "" {
				value := strings.TrimSpace(setting.Default)
				if err := validateConfigInitSetting(setting, value); err != nil {
					return err
				}
				updates[setting.Key] = value
				continue
			}
			if setting.Required {
				return fmt.Errorf("Missing %s. It is not set in %s. Run `nf config init` interactively to populate it.", setting.Key, config.ConfigFile())
			}
			continue
		}
		value, err := configPromptString(setting.Prompt, setting.Default, false)
		if err != nil {
			return err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			value = strings.TrimSpace(setting.Default)
		}
		if value == "" && setting.Required {
			return fmt.Errorf("%s is required", setting.Key)
		}
		if value != "" {
			if err := validateConfigInitSetting(setting, value); err != nil {
				return err
			}
			updates[setting.Key] = value
		}
	}
	if len(updates) == 0 {
		return nil
	}
	for key, value := range updates {
		values[key] = value
	}
	if err := saveGlobalConfig(values); err != nil {
		return err
	}
	fmt.Printf("Updated %s\n", config.ConfigFile())
	return nil
}

func configInitSettingHasValue(values map[string]string, setting configInitSetting) bool {
	if strings.TrimSpace(values[setting.Key]) != "" {
		return true
	}
	for _, key := range setting.LegacyKeys {
		if strings.TrimSpace(values[key]) != "" {
			return true
		}
	}
	return false
}

func validateConfigInitSetting(setting configInitSetting, value string) error {
	if setting.Validate == nil {
		return nil
	}
	return setting.Validate(value)
}

func cmdConfigSet(key, value string) int {
	values, err := loadGlobalConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	values[key] = strings.TrimSpace(value)
	if err := saveGlobalConfig(values); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Set %s\n", key)
	return 0
}

func cmdConfigShow() int {
	values, err := loadGlobalConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	saltStatus := "unset"
	if _, err := passwords.SecretSalt(); err == nil {
		saltStatus = "set"
	}
	fmt.Println("config")
	fmt.Println(strings.Repeat("─", len("config")))
	printDetailRows([]detailRow{{label: "Path", value: config.ConfigFile()}})
	fmt.Println()
	fmt.Println("Core")
	printIndentedDetailRows([]detailRow{
		{label: "Base domain", value: configShowValue(values, "base_domain", "")},
		{label: "Password salt", value: saltStatus},
	}, 2)
	fmt.Println()
	fmt.Println("WordPress")
	printIndentedDetailRows([]detailRow{
		{label: "Admin email", value: configShowValue(values, "default_wp_email", "")},
		{label: "Admin user", value: configShowValue(values, "default_wp_user", defaultWordPressAdminUser)},
		{label: "Basic auth user", value: configShowValue(values, "basicauth_default_user", "nonfiction")},
	}, 2)
	fmt.Println()
	fmt.Println("Database")
	printIndentedDetailRows([]detailRow{
		{label: "User", value: configShowValueAny(values, []string{"db_default_user", "adminer_default_user"}, defaultDatabaseUser)},
	}, 2)
	fmt.Println()
	fmt.Println("Docker")
	printIndentedDetailRows([]detailRow{
		{label: "User", value: configShowValue(values, "docker_user", defaultDockerUser)},
		{label: "DB image", value: configShowValue(values, "docker_db_image", defaultDockerDBImage)},
		{label: "WordPress image", value: configShowValue(values, "docker_wordpress_image", defaultDockerWordpressImage)},
	}, 2)
	fmt.Println()
	fmt.Println("DNSimple")
	printIndentedDetailRows([]detailRow{{label: "Account ID", value: configShowValue(values, "dnsimple_account_id", "")}}, 2)
	fmt.Println()
	fmt.Println("Kinsta")
	printIndentedDetailRows([]detailRow{
		{label: "Region", value: configShowValue(values, "kinsta_default_region", "")},
		{label: "PHP", value: configShowValue(values, "kinsta_default_php", "8.3")},
	}, 2)
	fmt.Println()
	fmt.Println("Linode")
	printIndentedDetailRows([]detailRow{
		{label: "Region", value: configShowValue(values, "linode_default_region", "ca-central")},
		{label: "Type", value: configShowValue(values, "linode_default_type", "g6-standard-1")},
		{label: "Image", value: configShowValue(values, "linode_default_image", "")},
		{label: "User", value: configShowValue(values, "linode_default_user", "nonfiction")},
	}, 2)
	return 0
}

func configShowValue(values map[string]string, key, fallback string) string {
	if value := strings.TrimSpace(values[key]); value != "" {
		return value
	}
	if fallback != "" {
		return fallback + " (default)"
	}
	return "unset"
}

func configShowValueAny(values map[string]string, keys []string, fallback string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(values[key]); value != "" {
			return value
		}
	}
	if fallback != "" {
		return fallback + " (default)"
	}
	return "unset"
}

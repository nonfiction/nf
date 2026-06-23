package cli

// Global config commands and provider refresh after init.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
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
	case "show":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "config show takes no arguments")
			return 1
		}
		return cmdConfigShow()
	case "get":
		if len(argv) == 1 {
			return cmdConfigGetInteractive()
		}
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config get takes exactly one key")
			return 1
		}
		return cmdConfigGet(argv[1])
	case "set":
		if len(argv) == 1 {
			return cmdConfigSetInteractive("")
		}
		if len(argv) == 2 && strings.TrimSpace(argv[1]) != "" {
			return cmdConfigSetInteractive(argv[1])
		}
		if len(argv) != 3 || strings.TrimSpace(argv[1]) == "" || strings.TrimSpace(argv[2]) == "" {
			fmt.Fprintln(os.Stderr, "config set takes exactly one key and one value")
			return 1
		}
		return cmdConfigSetKey(argv[1], argv[2])
	case "unset":
		if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
			fmt.Fprintln(os.Stderr, "config unset takes exactly one key")
			return 1
		}
		return cmdConfigUnset(argv[1])
	case "keys":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "config keys takes no arguments")
			return 1
		}
		return cmdConfigKeys()
	case "edit":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "config edit takes no arguments")
			return 1
		}
		return cmdConfigEdit()
	default:
		if key, ok := deprecatedConfigSetterKey(argv[0]); ok {
			if len(argv) != 2 || strings.TrimSpace(argv[1]) == "" {
				fmt.Fprintf(os.Stderr, "config %s takes exactly one value\n", argv[0])
				return 1
			}
			return cmdDeprecatedConfigSet(argv[0], key, argv[1])
		}
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

func cmdConfigGet(key string) int {
	spec, ok := lookupConfigKey(key)
	if !ok {
		printUnknownConfigKey(key)
		return 1
	}
	values, err := loadGlobalConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(spec.getValue(values))
	return 0
}

func cmdConfigGetInteractive() int {
	if !configIsInteractive() {
		fmt.Fprintln(os.Stderr, "config get requires a key")
		return 1
	}
	spec, err := promptConfigKey("Choose a config key")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return cmdConfigGet(spec.Key)
}

func cmdConfigSetKey(key, value string) int {
	spec, ok := lookupConfigKey(key)
	if !ok {
		printUnknownConfigKey(key)
		return 1
	}
	return cmdConfigSetSpec(spec, value)
}

func cmdConfigSetInteractive(key string) int {
	if !configIsInteractive() {
		fmt.Fprintln(os.Stderr, "config set requires a key and value")
		return 1
	}
	var spec configKeySpec
	if strings.TrimSpace(key) == "" {
		selected, err := promptConfigKey("Choose a config key")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		spec = selected
	} else {
		selected, ok := lookupConfigKey(key)
		if !ok {
			printUnknownConfigKey(key)
			return 1
		}
		spec = selected
	}

	values, err := loadGlobalConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	value, err := promptConfigValue(spec, values)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return cmdConfigSetSpec(spec, value)
}

func promptConfigKey(title string) (configKeySpec, error) {
	selected, err := configSelectFn(title, configKeySelectOptions())
	if err != nil {
		return configKeySpec{}, err
	}
	spec, ok := lookupConfigKey(selected)
	if !ok {
		return configKeySpec{}, fmt.Errorf("Unknown config key: %s", strings.TrimSpace(selected))
	}
	return spec, nil
}

func promptConfigValue(spec configKeySpec, values map[string]string) (string, error) {
	prompt := fmt.Sprintf("Value for %s", spec.Key)
	if spec.Sensitive {
		return configPromptSecret(prompt)
	}
	defaultValue := spec.promptDefault(values)
	value, err := configPromptString(prompt, defaultValue, false)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = defaultValue
	}
	return value, nil
}

func cmdConfigSetSpec(spec configKeySpec, value string) int {
	values, err := loadGlobalConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	stored, err := spec.setValue(values, value)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if spec.EnvKey == "" {
		if err := saveGlobalConfig(values); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	fmt.Printf("Set %s = %s\n", spec.Key, spec.safeSetOutput(stored))
	fmt.Printf("Path %s\n", spec.path())
	return 0
}

func cmdConfigUnset(key string) int {
	spec, ok := lookupConfigKey(key)
	if !ok {
		printUnknownConfigKey(key)
		return 1
	}
	values, err := loadGlobalConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := spec.unsetValue(values); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if spec.EnvKey == "" {
		if err := saveGlobalConfig(values); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	fmt.Printf("Unset %s\n", spec.Key)
	fmt.Printf("Path %s\n", spec.path())
	return 0
}

func cmdConfigKeys() int {
	fmt.Println("Config keys")
	fmt.Println(strings.Repeat("─", len("Config keys")))
	group := ""
	rows := []helpLine{}
	flush := func() {
		if group == "" {
			return
		}
		fmt.Println()
		fmt.Println(group)
		printHelpLinesWithIndent(rows, "  ", 30)
	}
	for _, spec := range configKeyRegistry {
		if spec.Group != group {
			flush()
			group = spec.Group
			rows = []helpLine{}
		}
		rows = append(rows, helpLine{Command: spec.Key, Description: spec.Description})
	}
	flush()
	return 0
}

func cmdConfigEdit() int {
	path := config.ConfigFile()
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := saveGlobalConfig(map[string]string{}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	editor := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi")
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		parts = []string{"vi"}
	}
	cmd := exec.Command(parts[0], append(parts[1:], path)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func cmdConfigShow() int {
	values, err := loadGlobalConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("config")
	fmt.Println(strings.Repeat("─", len("config")))
	printDetailRows([]detailRow{{label: "Path", value: config.ConfigFile()}})
	for _, group := range configShowGroups(values) {
		fmt.Println()
		fmt.Println(group.name)
		printIndentedDetailRows(group.rows, 2)
	}
	return 0
}

type configShowGroup struct {
	name string
	rows []detailRow
}

func configShowGroups(values map[string]string) []configShowGroup {
	groups := []configShowGroup{}
	current := configShowGroup{}
	flush := func() {
		if current.name != "" {
			groups = append(groups, current)
		}
	}
	for _, spec := range configKeyRegistry {
		if spec.Group != current.name {
			flush()
			current = configShowGroup{name: spec.Group}
		}
		current.rows = append(current.rows, detailRow{label: spec.Label, value: spec.displayValue(values)})
	}
	flush()
	return groups
}

func printUnknownConfigKey(key string) {
	fmt.Fprintf(os.Stderr, "Unknown config key: %s\n\n", strings.TrimSpace(key))
	fmt.Fprintln(os.Stderr, "Run:")
	fmt.Fprintln(os.Stderr, "  nf config keys")
}

func deprecatedConfigSetterKey(command string) (string, bool) {
	switch command {
	case "set-base-domain":
		return "core.base-domain", true
	case "set-default-wp-email":
		return "wordpress.admin-email", true
	case "set-default-wp-user":
		return "wordpress.admin-user", true
	case "set-basicauth-default-user":
		return "wordpress.basic-auth-user", true
	case "set-db-default-user":
		return "database.user", true
	case "set-docker-db-image":
		return "docker.images.db", true
	case "set-docker-wordpress-image":
		return "docker.images.wordpress", true
	case "set-docker-user":
		return "docker.user", true
	case "set-kinsta-default-region":
		return "kinsta.region", true
	case "set-kinsta-default-php":
		return "kinsta.php", true
	case "set-linode-default-region":
		return "linode.region", true
	case "set-linode-default-type":
		return "linode.type", true
	case "set-linode-default-image":
		return "linode.image", true
	case "set-linode-default-user":
		return "linode.user", true
	default:
		return "", false
	}
}

func cmdDeprecatedConfigSet(command, key, value string) int {
	spec, ok := lookupConfigKey(key)
	if !ok {
		printUnknownConfigKey(key)
		return 1
	}
	fmt.Println("Deprecated. Use:")
	fmt.Printf("  nf config set %s %s\n", spec.Key, strings.TrimSpace(value))
	return cmdConfigSetSpec(spec, value)
}

package cli

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/ui"
)

const (
	wpConfigDefineModeForce        = "force"
	wpConfigDefineModeReplaceFalse = "replace_false"
	defineSetValueSourceLiteral    = "literal"
	defineSetValueSourceSecret     = "secret"
	defineSetSelectorAll           = "__all__"
	defineSetSelectorCustom        = "__custom__"
	wpConfigProjectBlockBegin      = "/* nf-managed wp-config defines: begin */"
	wpConfigProjectBlockEnd        = "/* nf-managed wp-config defines: end */"
	wpConfigProviderBlockBegin     = "/* nf-managed provider wp-config defines: begin */"
	wpConfigProviderBlockEnd       = "/* nf-managed provider wp-config defines: end */"
	wpConfigLegacyModeProject      = "project"
	wpConfigLegacyModeProvider     = "provider"
)

var wpConfigDefineNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var providerOwnedWPConfigDefines = map[string]string{
	"KINSTAMU_WHITELABEL": "Kinsta whitelabel is managed by nf site repair",
}

func providerOwnedWPConfigDefineNames() []string {
	names := make([]string, 0, len(providerOwnedWPConfigDefines))
	for name := range providerOwnedWPConfigDefines {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type wpConfigDefine struct {
	Name     string
	PHPValue string
	Source   string
	Mode     string
}

type wpConfigDefineSelector struct {
	Local      bool
	RemoteName string
	EnvID      string
	Env        string
}

type wpConfigPatchDefinition struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Mode   string `json:"mode"`
	Source string `json:"source,omitempty"`
}

type defineValueSpec struct {
	Env         string
	SecretRef   string
	SecretValue string
	Value       any
	IsEnv       bool
	IsSecret    bool
}

type defineSetPartial struct {
	Positionals     []string
	SecretSet       bool
	SecretStdin     bool
	Selector        string
	SelectorSet     bool
	SelectorMissing bool
}

type defineGetPartial struct {
	Positionals     []string
	Selector        string
	SelectorSet     bool
	SelectorMissing bool
}

type defineRemovePartial struct {
	Positionals     []string
	Selector        string
	SelectorSet     bool
	SelectorMissing bool
}

func runDefine(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		return runDefineHelp()
	}
	cmd := cliCommandAlias(argv[0])
	switch cmd {
	case "list", "get", "status", "sync", "set", "remove", "migrate-env", "rekey":
	default:
		fmt.Fprintln(os.Stderr, "unsupported define command")
		return 1
	}
	if err := requireProjectContext("define " + cmd); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root, err := discoverProjectRootOrError()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	switch cmd {
	case "list":
		if len(argv) != 1 {
			fmt.Fprintln(os.Stderr, "define list takes no arguments")
			return 1
		}
		return cmdDefineList(metadata)
	case "get":
		return cmdDefineGet(root, metadata, argv[1:])
	case "status":
		remoteName, ok := parseDefineRemoteArg("status", argv[1:])
		if !ok {
			return 1
		}
		return cmdDefineStatus(root, metadata, remoteName)
	case "sync":
		remoteName, ok := parseDefineRemoteArg("sync", argv[1:])
		if !ok {
			return 1
		}
		return cmdDefineSync(root, metadata, remoteName)
	case "set":
		return cmdDefineSet(root, metadata, argv[1:])
	case "remove":
		return cmdDefineRemove(root, metadata, argv[1:])
	case "migrate-env":
		return cmdDefineMigrateEnv(root, metadata, argv[1:])
	case "rekey":
		return cmdDefineRekey(root, metadata, argv[1:])
	default:
		return 1
	}
}

func parseDefineRemoteArg(action string, args []string) (string, bool) {
	if len(args) > 1 {
		fmt.Fprintf(os.Stderr, "define %s takes at most one remote\n", action)
		return "", false
	}
	if len(args) == 1 {
		if strings.HasPrefix(args[0], "-") {
			fmt.Fprintf(os.Stderr, "unknown define %s flag: %s\n", action, args[0])
			return "", false
		}
		return strings.TrimSpace(args[0]), true
	}
	return "", true
}

func cmdDefineList(metadata *projectMetadata) int {
	entries, err := configuredDefineEntries(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Configured defines:")
	if len(entries) == 0 {
		fmt.Println("  none")
		return 0
	}
	rows := [][]string{{"name", "selector", "source"}}
	for _, entry := range entries {
		rows = append(rows, []string{entry.Name, entry.Selector, entry.Source})
	}
	fmt.Println(formatTable(rows))
	return 0
}

func cmdDefineGet(root string, metadata *projectMetadata, args []string) int {
	name, selector, err := resolveDefineGetArgs(metadata, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	value, err := configuredDefineRawValue(root, metadata, name, selector)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	output, err := defineRawValueString(value)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(output)
	return 0
}

func cmdDefineStatus(root string, metadata *projectMetadata, remoteName string) int {
	if strings.TrimSpace(remoteName) == "" {
		cfg, ok := loadEnvConfig(root, metadata)
		if !ok {
			fmt.Fprintln(os.Stderr, "Invalid local project metadata in nf.json.")
			return 1
		}
		defines, err := loadWordPressConfigDefines(root, metadata, wpConfigDefineSelector{Local: true})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return cmdDefineStatusLocal(cfg, defines)
	}
	target, err := resolveEnvRemoteSyncTarget("define status", remoteName, metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	selector := wpConfigDefineSelector{RemoteName: target.RemoteName, EnvID: canonicalEnvID(target.SiteID, target.Env), Env: target.Env}
	defines, err := loadWordPressConfigDefines(root, metadata, selector)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return cmdDefineStatusRemote(target, defines)
}

func cmdDefineStatusLocal(cfg envConfig, defines []wpConfigDefine) int {
	fmt.Println("Define status:")
	if len(defines) == 0 {
		fmt.Println("No defines configured for local.")
		return 0
	}
	script := renderWPConfigDefineStatusScript("/var/www/html", defines)
	service := firstNonEmpty(cfg.WordpressService, "wordpress")
	args := envComposeArgs(cfg, "exec", "-T", "--user", "root", service, "sh", "-s")
	output, err := runCommandSpecStdinOutputSilent(execSpec{Dir: localEnvDir(cfg), Args: args}, script)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("  target: local\n")
	fmt.Printf("  path:   /var/www/html/wp-config.php\n\n")
	printDefineStatusOutput(string(output))
	return defineStatusExitCode(string(output))
}

func cmdDefineStatusRemote(target envRemoteSyncTarget, defines []wpConfigDefine) int {
	fmt.Println("Define status:")
	printDefineRemoteHeader(target)
	fmt.Printf("  path:     %s/wp-config.php\n\n", strings.TrimRight(target.WordPressPath, "/"))
	if len(defines) == 0 {
		fmt.Printf("No defines configured for %s.\n", target.RemoteName)
		return 0
	}
	script := renderWPConfigDefineStatusScript(target.WordPressPath, defines)
	output, err := runSSHStdinOutputFn(remoteDefineStdinArgs(target), script)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	printDefineStatusOutput(string(output))
	return defineStatusExitCode(string(output))
}

func cmdDefineSync(root string, metadata *projectMetadata, remoteName string) int {
	if strings.TrimSpace(remoteName) == "" {
		cfg, ok := loadEnvConfig(root, metadata)
		if !ok {
			fmt.Fprintln(os.Stderr, "Invalid local project metadata in nf.json.")
			return 1
		}
		defines, err := loadWordPressConfigDefines(root, metadata, wpConfigDefineSelector{Local: true})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return cmdDefineSyncLocal(cfg, defines)
	}
	target, err := resolveEnvRemoteSyncTarget("define sync", remoteName, metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	selector := wpConfigDefineSelector{RemoteName: target.RemoteName, EnvID: canonicalEnvID(target.SiteID, target.Env), Env: target.Env}
	defines, err := loadWordPressConfigDefines(root, metadata, selector)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return cmdDefineSyncRemote(target, defines)
}

func cmdDefineSyncLocal(cfg envConfig, defines []wpConfigDefine) int {
	fmt.Println("Define sync:")
	if len(defines) == 0 {
		fmt.Println("No defines configured for local; syncing removes any nf-managed project define block.")
	}
	if err := ensureManagedEnv(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := runCommandSpecWithPreview(execSpec{Dir: localEnvDir(cfg), Args: envWpBootstrapReadyArgs(cfg)}, envWpBootstrapPreviewArgs(cfg, "wait for WordPress files")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	script := renderWPConfigDefineScript("/var/www/html", defines)
	service := firstNonEmpty(cfg.WordpressService, "wordpress")
	args := envComposeArgs(cfg, "exec", "-T", "--user", "root", service, "sh", "-s")
	preview := envComposeArgs(cfg, "exec", "-T", "--user", "root", service, "<sync defines>")
	if err := runCommandSpecStdinWithPreview(execSpec{Dir: localEnvDir(cfg), Args: args}, preview, script); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Defines synced.")
	return 0
}

func cmdDefineSyncRemote(target envRemoteSyncTarget, defines []wpConfigDefine) int {
	fmt.Println("Define sync:")
	printDefineRemoteHeader(target)
	if len(defines) == 0 {
		fmt.Printf("No defines configured for %s; syncing removes any nf-managed project define block.\n", target.RemoteName)
	}
	script := renderWPConfigDefineScript(target.WordPressPath, defines)
	args := remoteDefineStdinArgs(target)
	printDefineRemoteCommand(target)
	if err := runSSHStdinCommandFn(args, script); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Defines synced.")
	return 0
}

func cmdDefineSet(root string, metadata *projectMetadata, args []string) int {
	name, selector, spec, err := resolveDefineSetArgs(root, metadata, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	beforeRefs, err := configuredDefineSecretRefs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var store *defineSecretStore
	if spec.IsSecret {
		store, err = loadDefineSecretStore(root, metadata, len(beforeRefs) == 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		existing := configuredDefineSpec(metadata, name, selector)
		if existing != nil {
			spec.SecretRef = strings.TrimSpace(recordValueString(existing["secret"]))
		}
		if spec.SecretRef == "" {
			spec.SecretRef, err = generateDefineSecretRef(beforeRefs)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		}
	}
	if err := upsertConfiguredDefine(metadata, name, selector, spec); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	afterRefs, err := configuredDefineSecretRefs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if spec.IsSecret {
		store.Secrets[spec.SecretRef] = spec.SecretValue
		if err := pruneDefineSecretStore(root, metadata, store); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if _, err := loadDefineSecretStore(root, metadata, false); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := saveProjectMetadata(root, metadata); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	} else {
		if removedDefineSecretRefs(beforeRefs, afterRefs) {
			store, err = loadDefineSecretStore(root, nil, false)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		}
		if err := saveProjectMetadata(root, metadata); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if store != nil {
			if err := pruneDefineSecretStore(root, metadata, store); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		}
	}
	if selector == "" {
		fmt.Printf("Set define %s.\n", name)
	} else {
		fmt.Printf("Set define %s for %s.\n", name, selector)
	}
	return 0
}

func cmdDefineRemove(root string, metadata *projectMetadata, args []string) int {
	name, selector, err := resolveDefineRemoveArgs(metadata, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	beforeRefs, err := configuredDefineSecretRefs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := removeConfiguredDefine(metadata, name, selector); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	afterRefs, err := configuredDefineSecretRefs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var store *defineSecretStore
	if removedDefineSecretRefs(beforeRefs, afterRefs) {
		store, err = loadDefineSecretStore(root, nil, false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if err := saveProjectMetadata(root, metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if store != nil {
		if err := pruneDefineSecretStore(root, metadata, store); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if selector == "" {
		fmt.Printf("Removed define %s.\n", name)
	} else {
		fmt.Printf("Removed define %s for %s.\n", name, selector)
	}
	return 0
}

func resolveDefineGetArgs(metadata *projectMetadata, args []string) (string, string, error) {
	partial, err := parseDefineGetPartial(args)
	if err != nil {
		return "", "", err
	}
	if len(partial.Positionals) != 1 {
		return "", "", fmt.Errorf("define get requires exactly one name")
	}
	name := strings.TrimSpace(partial.Positionals[0])
	if err := validateDefineName(name); err != nil {
		return "", "", err
	}
	item := configuredDefineItem(metadata, name)
	if item == nil {
		return "", "", fmt.Errorf("define %s is not configured", name)
	}
	selector := strings.TrimSpace(partial.Selector)
	if partial.SelectorSet && !partial.SelectorMissing && selector == "" {
		return "", "", fmt.Errorf("define get --for requires a selector")
	}
	_, hasValues := item["values"]
	if !hasValues {
		if partial.SelectorSet {
			return "", "", fmt.Errorf("define %s uses a shared value and does not accept --for", name)
		}
		return name, "", nil
	}
	if !partial.SelectorSet || partial.SelectorMissing {
		if !siteIsInteractiveFn() {
			return "", "", fmt.Errorf("define get %s requires --for because it has selector-specific values", name)
		}
		selector, err = promptConfiguredDefineSelector(name, item, false)
		if err != nil {
			return "", "", err
		}
	}
	if configuredDefineSpec(metadata, name, selector) == nil {
		return "", "", fmt.Errorf("define %s is not configured for %s", name, selector)
	}
	return name, selector, nil
}

func parseDefineGetPartial(args []string) (defineGetPartial, error) {
	partial := defineGetPartial{Positionals: []string{}}
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; arg {
		case "--for":
			if partial.SelectorSet {
				return partial, fmt.Errorf("define get accepts --for only once")
			}
			partial.SelectorSet = true
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				partial.SelectorMissing = true
				continue
			}
			partial.Selector = strings.TrimSpace(args[i+1])
			i++
		default:
			if strings.HasPrefix(arg, "-") {
				return partial, fmt.Errorf("unknown define get flag: %s", arg)
			}
			partial.Positionals = append(partial.Positionals, arg)
		}
	}
	return partial, nil
}

func resolveDefineSetArgs(root string, metadata *projectMetadata, args []string) (string, string, defineValueSpec, error) {
	partial, err := parseDefineSetPartial(args)
	if err != nil {
		return "", "", defineValueSpec{}, err
	}
	name, selector, spec, err := strictDefineSetArgs(partial)
	if err == nil {
		return name, selector, spec, nil
	}
	if !siteIsInteractiveFn() {
		return "", "", defineValueSpec{}, err
	}
	return promptDefineSetArgs(root, metadata, partial)
}

func parseDefineSetPartial(args []string) (defineSetPartial, error) {
	partial := defineSetPartial{Positionals: []string{}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--env":
			return partial, fmt.Errorf("define set --env is no longer supported; use --secret or run `nf define migrate-env`")
		case "--secret":
			if partial.SecretSet {
				return partial, fmt.Errorf("define set accepts only one secret input option")
			}
			partial.SecretSet = true
		case "--secret-stdin":
			if partial.SecretSet {
				return partial, fmt.Errorf("define set accepts only one secret input option")
			}
			partial.SecretSet = true
			partial.SecretStdin = true
		case "--for":
			if partial.SelectorSet {
				return partial, fmt.Errorf("define set accepts --for only once")
			}
			partial.SelectorSet = true
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				partial.SelectorMissing = true
				continue
			}
			partial.Selector = strings.TrimSpace(args[i+1])
			i++
		default:
			if strings.HasPrefix(arg, "-") {
				return partial, fmt.Errorf("unknown define set flag: %s", arg)
			}
			partial.Positionals = append(partial.Positionals, arg)
		}
	}
	return partial, nil
}

func strictDefineSetArgs(partial defineSetPartial) (string, string, defineValueSpec, error) {
	if partial.SelectorSet && partial.SelectorMissing {
		return "", "", defineValueSpec{}, fmt.Errorf("define set --for requires a selector")
	}
	if partial.SecretSet && len(partial.Positionals) != 1 {
		return "", "", defineValueSpec{}, fmt.Errorf("define set with a secret input option requires exactly one name")
	}
	if !partial.SecretSet && len(partial.Positionals) != 2 {
		return "", "", defineValueSpec{}, fmt.Errorf("define set requires a name and value, or a name with --secret")
	}
	name := strings.TrimSpace(partial.Positionals[0])
	if err := validateProjectDefineName(name); err != nil {
		return "", "", defineValueSpec{}, err
	}
	selector := strings.TrimSpace(partial.Selector)
	if partial.SelectorSet && selector == "" {
		return "", "", defineValueSpec{}, fmt.Errorf("define set --for requires a selector")
	}
	if partial.SecretSet {
		if !partial.SecretStdin {
			return "", "", defineValueSpec{}, fmt.Errorf("define set --secret requires an interactive terminal; use --secret-stdin for automation")
		}
		value, err := readDefineSecretStdin()
		if err != nil {
			return "", "", defineValueSpec{}, err
		}
		if strings.TrimSpace(value) == "" {
			return "", "", defineValueSpec{}, fmt.Errorf("encrypted define value must not be empty")
		}
		return name, selector, defineValueSpec{SecretValue: value, IsSecret: true}, nil
	}
	return name, selector, defineValueSpec{Value: parseDefineCLIValue(partial.Positionals[1])}, nil
}

func promptDefineSetArgs(root string, metadata *projectMetadata, partial defineSetPartial) (string, string, defineValueSpec, error) {
	if len(partial.Positionals) > 2 || (partial.SecretSet && len(partial.Positionals) > 1) {
		return strictDefineSetArgs(partial)
	}
	name := ""
	if len(partial.Positionals) > 0 {
		name = strings.TrimSpace(partial.Positionals[0])
	} else {
		prompted, err := definePromptStringFn("Define name (usually ALL_CAPS)", "", false)
		if err != nil {
			return "", "", defineValueSpec{}, err
		}
		name = strings.TrimSpace(prompted)
	}
	if err := validateProjectDefineName(name); err != nil {
		return "", "", defineValueSpec{}, err
	}
	if !isAllCapsDefineName(name) {
		confirmed, err := defineConfirmFn(fmt.Sprintf("Define names are normally ALL_CAPS. Use %q exactly?", name), false)
		if err != nil {
			return "", "", defineValueSpec{}, err
		}
		if !confirmed {
			return "", "", defineValueSpec{}, fmt.Errorf("define set cancelled")
		}
	}

	selector := strings.TrimSpace(partial.Selector)
	item := configuredDefineItem(metadata, name)
	if partial.SelectorSet && (partial.SelectorMissing || selector == "") {
		selected, err := promptDefineSelector(metadata)
		if err != nil {
			return "", "", defineValueSpec{}, err
		}
		selector = selected
	} else if !partial.SelectorSet && len(partial.Positionals) < 2 {
		if item == nil {
			if !partial.SecretSet {
				selected, err := promptDefineSelector(metadata)
				if err != nil {
					return "", "", defineValueSpec{}, err
				}
				selector = selected
			}
		} else if _, hasValues := item["values"]; hasValues {
			selected, err := promptConfiguredDefineSelector(name, item, true)
			if err != nil {
				return "", "", defineValueSpec{}, err
			}
			if selected == defineSetSelectorCustom {
				selected, err = promptDefineSelector(metadata)
				if err != nil {
					return "", "", defineValueSpec{}, err
				}
			}
			selector = selected
		}
	}

	existingSpec := configuredDefineSpec(metadata, name, selector)
	var existingValue any
	if existingSpec != nil {
		var err error
		existingValue, err = configuredDefineSpecRawValue(root, metadata, name, existingSpec)
		if err != nil {
			return "", "", defineValueSpec{}, err
		}
	}
	existingText, err := defineRawValueString(existingValue)
	if err != nil {
		return "", "", defineValueSpec{}, err
	}

	source := defineSetValueSourceLiteral
	if partial.SecretSet {
		source = defineSetValueSourceSecret
	} else if len(partial.Positionals) < 2 {
		if existingSpec != nil {
			switch {
			case strings.TrimSpace(recordValueString(existingSpec["secret"])) != "":
				source = defineSetValueSourceSecret
			case existingSpec["env"] != nil:
				return "", "", defineValueSpec{}, fmt.Errorf("define %s uses a legacy env source; run `nf define migrate-env` before editing it", name)
			}
		} else {
			selected, err := defineSelectFn("Choose value source", defineSetValueSourceOptions())
			if err != nil {
				return "", "", defineValueSpec{}, err
			}
			source = selected
		}
	}

	var spec defineValueSpec
	switch source {
	case defineSetValueSourceSecret:
		var value string
		var err error
		if partial.SecretStdin {
			value, err = readDefineSecretStdin()
		} else {
			value, err = definePromptSecretFn("Encrypted define value", existingText)
		}
		if err != nil {
			return "", "", defineValueSpec{}, err
		}
		if strings.TrimSpace(value) == "" {
			return "", "", defineValueSpec{}, fmt.Errorf("encrypted define value must not be empty")
		}
		spec = defineValueSpec{SecretValue: value, IsSecret: true}
	case defineSetValueSourceLiteral:
		value := ""
		if len(partial.Positionals) >= 2 {
			value = partial.Positionals[1]
		} else {
			prompted, err := definePromptStringFn("Define value", existingText, true)
			if err != nil {
				return "", "", defineValueSpec{}, err
			}
			value = prompted
		}
		spec = defineValueSpec{Value: parseEditedDefineCLIValue(value, existingValue)}
	default:
		return "", "", defineValueSpec{}, fmt.Errorf("unsupported define value source")
	}

	return name, selector, spec, nil
}

func defineSetValueSourceOptions() []ui.SelectOption {
	return []ui.SelectOption{
		{Value: defineSetValueSourceLiteral, Label: "Literal value stored in nf.json", Default: true},
		{Value: defineSetValueSourceSecret, Label: "Encrypted secret stored in nf.age"},
	}
}

func promptConfiguredDefineSelector(name string, item map[string]any, allowNew bool) (string, error) {
	values, err := defineValuesMap(item)
	if err != nil {
		return "", err
	}
	selectors := make([]string, 0, len(values))
	for selector := range values {
		selectors = append(selectors, selector)
	}
	sort.Strings(selectors)
	options := make([]ui.SelectOption, 0, len(selectors)+1)
	for _, selector := range selectors {
		label := selector
		if selector == "default" {
			label += " (shared default)"
		}
		options = append(options, ui.SelectOption{Value: selector, Label: label})
	}
	if allowNew {
		options = append(options, ui.SelectOption{Value: defineSetSelectorCustom, Label: "Set another selector..."})
	}
	return defineSelectFn("Choose a selector for "+name, options)
}

func promptDefineSelector(metadata *projectMetadata) (string, error) {
	selected, err := defineSelectFn("Choose where this define applies", defineSelectorOptions(metadata))
	if err != nil {
		return "", err
	}
	switch selected {
	case defineSetSelectorAll:
		return "", nil
	case defineSetSelectorCustom:
		prompted, err := definePromptStringFn("Define selector", "", false)
		if err != nil {
			return "", err
		}
		selector := strings.TrimSpace(prompted)
		if selector == "" {
			return "", fmt.Errorf("define set --for requires a selector")
		}
		return selector, nil
	default:
		return strings.TrimSpace(selected), nil
	}
}

func defineSelectorOptions(metadata *projectMetadata) []ui.SelectOption {
	options := []ui.SelectOption{{Value: defineSetSelectorAll, Label: "All environments (shared default)", Default: true}}
	options = append(options, ui.SelectOption{Value: "local", Label: "local"})
	remotes, err := projectRemotes(metadata, false)
	if err == nil {
		names := make([]string, 0, len(remotes))
		for name := range remotes {
			names = append(names, name)
		}
		sort.Strings(names)
		seen := map[string]bool{}
		for _, option := range options {
			seen[option.Value] = true
		}
		for _, name := range names {
			if !seen[name] {
				label := name
				if remoteRef := strings.TrimSpace(remotes[name]); remoteRef != "" {
					label += " (" + remoteRef + ")"
				}
				options = append(options, ui.SelectOption{Value: name, Label: label})
			}
		}
	}
	options = append(options, ui.SelectOption{Value: defineSetSelectorCustom, Label: "Custom selector..."})
	return options
}

func isAllCapsDefineName(name string) bool {
	return name == strings.ToUpper(name)
}

func parseDefineRemoveArgs(args []string) (string, string, error) {
	partial, err := parseDefineRemovePartial(args)
	if err != nil {
		return "", "", err
	}
	return strictDefineRemoveArgs(partial)
}

func resolveDefineRemoveArgs(metadata *projectMetadata, args []string) (string, string, error) {
	partial, err := parseDefineRemovePartial(args)
	if err != nil {
		return "", "", err
	}
	name, selector, err := strictDefineRemoveArgs(partial)
	if err == nil {
		return name, selector, nil
	}
	if !siteIsInteractiveFn() {
		return "", "", err
	}
	return promptDefineRemoveArgs(metadata, partial)
}

func parseDefineRemovePartial(args []string) (defineRemovePartial, error) {
	partial := defineRemovePartial{Positionals: []string{}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--for":
			if partial.SelectorSet {
				return partial, fmt.Errorf("define remove accepts --for only once")
			}
			partial.SelectorSet = true
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				partial.SelectorMissing = true
				continue
			}
			partial.Selector = strings.TrimSpace(args[i+1])
			i++
		default:
			if strings.HasPrefix(arg, "-") {
				return partial, fmt.Errorf("unknown define remove flag: %s", arg)
			}
			partial.Positionals = append(partial.Positionals, arg)
		}
	}
	return partial, nil
}

func strictDefineRemoveArgs(partial defineRemovePartial) (string, string, error) {
	if partial.SelectorSet && partial.SelectorMissing {
		return "", "", fmt.Errorf("define remove --for requires a selector")
	}
	if len(partial.Positionals) != 1 {
		return "", "", fmt.Errorf("define remove requires exactly one name")
	}
	name := strings.TrimSpace(partial.Positionals[0])
	if err := validateDefineName(name); err != nil {
		return "", "", err
	}
	selector := strings.TrimSpace(partial.Selector)
	if partial.SelectorSet && selector == "" {
		return "", "", fmt.Errorf("define remove --for requires a selector")
	}
	return name, selector, nil
}

func promptDefineRemoveArgs(metadata *projectMetadata, partial defineRemovePartial) (string, string, error) {
	if len(partial.Positionals) > 1 {
		return strictDefineRemoveArgs(partial)
	}
	name := ""
	if len(partial.Positionals) == 1 {
		name = strings.TrimSpace(partial.Positionals[0])
	} else {
		selected, err := defineSelectFn("Choose a define to remove", configuredDefineNameOptions(metadata))
		if err != nil {
			return "", "", err
		}
		name = strings.TrimSpace(selected)
	}
	if err := validateDefineName(name); err != nil {
		return "", "", err
	}
	selector := strings.TrimSpace(partial.Selector)
	if partial.SelectorSet && (partial.SelectorMissing || selector == "") {
		selected, err := promptDefineSelector(metadata)
		if err != nil {
			return "", "", err
		}
		selector = selected
	}
	return name, selector, nil
}

func configuredDefineNameOptions(metadata *projectMetadata) []ui.SelectOption {
	defines, _, err := configuredDefineArray(metadata, false)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(defines))
	for _, raw := range defines {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		name := strings.TrimSpace(recordValueString(item["name"]))
		if name != "" {
			names = append(names, name)
		}
	}
	names = uniqueSortedStrings(names)
	options := make([]ui.SelectOption, 0, len(names))
	for _, name := range names {
		options = append(options, ui.SelectOption{Value: name, Label: name})
	}
	return options
}

func validateDefineName(name string) error {
	if name == "" {
		return fmt.Errorf("define name is required")
	}
	if !wpConfigDefineNamePattern.MatchString(name) {
		return fmt.Errorf("define name must be a PHP constant name")
	}
	return nil
}

func validateProjectDefineName(name string) error {
	if err := validateDefineName(name); err != nil {
		return err
	}
	if reason, ok := providerOwnedWPConfigDefines[name]; ok {
		return fmt.Errorf("define %s is provider-owned; %s", name, reason)
	}
	return nil
}

func validateConfiguredProjectDefineName(index int, name string) error {
	if err := validateDefineName(name); err != nil {
		return ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d].name: %s", index, err)}
	}
	if reason, ok := providerOwnedWPConfigDefines[name]; ok {
		return ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d].name %s is provider-owned; %s. Remove it from nf.json and run nf site repair for Kinsta platform repair.", index, name, reason)}
	}
	return nil
}

func validateDefineEnvName(name string) error {
	if name == "" {
		return fmt.Errorf("define env name is required")
	}
	if !wpConfigDefineNamePattern.MatchString(name) {
		return fmt.Errorf("define env name must be an environment variable name")
	}
	return nil
}

func configuredDefineItem(metadata *projectMetadata, name string) map[string]any {
	for _, raw := range metadata.WordPress.Defines {
		item, _ := raw.(map[string]any)
		if item != nil && strings.TrimSpace(recordValueString(item["name"])) == name {
			return item
		}
	}
	return nil
}

func configuredDefineRawValue(root string, metadata *projectMetadata, name, selector string) (any, error) {
	item := configuredDefineItem(metadata, name)
	if item == nil {
		return nil, fmt.Errorf("define %s is not configured", name)
	}
	spec := item
	if _, hasValues := item["values"]; hasValues {
		if strings.TrimSpace(selector) == "" {
			return nil, fmt.Errorf("define get %s requires --for because it has selector-specific values", name)
		}
		spec = configuredDefineSpec(metadata, name, selector)
		if spec == nil {
			return nil, fmt.Errorf("define %s is not configured for %s", name, selector)
		}
	}
	return configuredDefineSpecRawValue(root, metadata, name, spec)
}

func configuredDefineSpecRawValue(root string, metadata *projectMetadata, name string, spec map[string]any) (any, error) {
	if envName := strings.TrimSpace(recordValueString(spec["env"])); envName != "" {
		value := envwizard.Value(envName)
		if value == "" {
			return nil, ProjectError{Msg: fmt.Sprintf("Expected %s in the environment or %s for define %s.", envName, config.EnvFile(), name)}
		}
		return value, nil
	}
	if ref := strings.TrimSpace(recordValueString(spec["secret"])); ref != "" {
		store, err := loadDefineSecretStore(root, metadata, false)
		if err != nil {
			return nil, err
		}
		value, ok := store.Secrets[ref]
		if !ok {
			return nil, ProjectError{Msg: fmt.Sprintf("%s has no encrypted value for %s", defineSecretStoreFilename, name)}
		}
		return value, nil
	}
	if value, ok := spec["value"]; ok {
		return value, nil
	}
	return nil, fmt.Errorf("define %s has no configured value", name)
}

func defineRawValueString(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "", nil
	case string:
		return typed, nil
	case bool:
		return strconv.FormatBool(typed), nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return "", fmt.Errorf("define value number must be finite")
		}
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("define value has unsupported type %T", value)
	}
}

func parseDefineCLIValue(value string) any {
	switch strings.ToLower(value) {
	case "true":
		return true
	case "false":
		return false
	default:
		return value
	}
}

func parseEditedDefineCLIValue(value string, existing any) any {
	switch existing.(type) {
	case string:
		return value
	case bool:
		if parsed, err := strconv.ParseBool(strings.ToLower(value)); err == nil {
			return parsed
		}
	case float64:
		if parsed, err := strconv.ParseFloat(value, 64); err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) {
			return parsed
		}
	}
	return parseDefineCLIValue(value)
}

func configuredDefineArray(metadata *projectMetadata, create bool) ([]any, *[]any, error) {
	if metadata.WordPress.Defines == nil && create {
		metadata.WordPress.Defines = []any{}
	}
	return metadata.WordPress.Defines, &metadata.WordPress.Defines, nil
}

func upsertConfiguredDefine(metadata *projectMetadata, name, selector string, spec defineValueSpec) error {
	if err := validateProjectDefineName(name); err != nil {
		return err
	}
	defines, definesTarget, err := configuredDefineArray(metadata, true)
	if err != nil {
		return err
	}
	idx := -1
	var item map[string]any
	for i, raw := range defines {
		candidate, ok := raw.(map[string]any)
		if !ok || candidate == nil {
			return ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d] must be an object", i)}
		}
		if recordValueString(candidate["name"]) == name {
			idx = i
			item = candidate
			break
		}
	}
	if item == nil {
		item = map[string]any{"name": name}
		defines = append(defines, item)
		idx = len(defines) - 1
	}
	if selector == "" {
		if _, hasValues := item["values"]; hasValues {
			values, err := defineValuesMap(item)
			if err != nil {
				return err
			}
			values["default"] = defineValueSpecMap(spec)
			delete(item, "value")
			delete(item, "env")
		} else {
			setDefineValueSpec(item, spec)
		}
	} else {
		values, err := ensureDefineValuesMap(item)
		if err != nil {
			return err
		}
		if _, hasValue := item["value"]; hasValue {
			values["default"] = map[string]any{"value": item["value"]}
			delete(item, "value")
		}
		if _, hasEnv := item["env"]; hasEnv {
			values["default"] = map[string]any{"env": item["env"]}
			delete(item, "env")
		}
		if _, hasSecret := item["secret"]; hasSecret {
			values["default"] = map[string]any{"secret": item["secret"]}
			delete(item, "secret")
		}
		values[selector] = defineValueSpecMap(spec)
	}
	defines[idx] = item
	sort.SliceStable(defines, func(i, j int) bool {
		left, _ := defines[i].(map[string]any)
		right, _ := defines[j].(map[string]any)
		return recordValueString(left["name"]) < recordValueString(right["name"])
	})
	*definesTarget = defines
	return nil
}

func removeConfiguredDefine(metadata *projectMetadata, name, selector string) error {
	defines, definesTarget, err := configuredDefineArray(metadata, false)
	if err != nil {
		return err
	}
	if len(defines) == 0 {
		return fmt.Errorf("define %s is not configured", name)
	}
	idx := -1
	var item map[string]any
	for i, raw := range defines {
		candidate, ok := raw.(map[string]any)
		if !ok || candidate == nil {
			return ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d] must be an object", i)}
		}
		if recordValueString(candidate["name"]) == name {
			idx = i
			item = candidate
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("define %s is not configured", name)
	}
	if selector == "" {
		defines = append(defines[:idx], defines[idx+1:]...)
		*definesTarget = defines
		return nil
	}
	values, err := defineValuesMap(item)
	if err != nil {
		return err
	}
	if _, ok := values[selector]; !ok {
		return fmt.Errorf("define %s is not configured for %s", name, selector)
	}
	delete(values, selector)
	if len(values) == 0 && item["value"] == nil && item["env"] == nil && item["secret"] == nil {
		defines = append(defines[:idx], defines[idx+1:]...)
	} else if len(values) == 0 {
		delete(item, "values")
		defines[idx] = item
	} else {
		item["values"] = values
		defines[idx] = item
	}
	*definesTarget = defines
	return nil
}

func setDefineValueSpec(item map[string]any, spec defineValueSpec) {
	delete(item, "value")
	delete(item, "env")
	delete(item, "secret")
	if spec.IsSecret {
		item["secret"] = spec.SecretRef
		return
	}
	if spec.IsEnv {
		item["env"] = spec.Env
		return
	}
	item["value"] = spec.Value
}

func defineValueSpecMap(spec defineValueSpec) map[string]any {
	if spec.IsSecret {
		return map[string]any{"secret": spec.SecretRef}
	}
	if spec.IsEnv {
		return map[string]any{"env": spec.Env}
	}
	return map[string]any{"value": spec.Value}
}

func ensureDefineValuesMap(item map[string]any) (map[string]any, error) {
	if raw, ok := item["values"]; ok {
		return normalizeDefineValuesMap(raw)
	}
	values := map[string]any{}
	item["values"] = values
	return values, nil
}

func defineValuesMap(item map[string]any) (map[string]any, error) {
	raw, ok := item["values"]
	if !ok {
		return nil, fmt.Errorf("define %s has no selector-specific values", recordValueString(item["name"]))
	}
	return normalizeDefineValuesMap(raw)
}

func normalizeDefineValuesMap(raw any) (map[string]any, error) {
	values, ok := raw.(map[string]any)
	if !ok || values == nil {
		return nil, ProjectError{Msg: "nf.json wordpress.defines values must be an object"}
	}
	return values, nil
}

type configuredDefineEntry struct {
	Name     string
	Selector string
	Source   string
}

func configuredDefineEntries(metadata *projectMetadata) ([]configuredDefineEntry, error) {
	defines, _, err := configuredDefineArray(metadata, false)
	if err != nil {
		return nil, err
	}
	entries := []configuredDefineEntry{}
	for i, raw := range defines {
		item, ok := raw.(map[string]any)
		if !ok || item == nil {
			return nil, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d] must be an object", i)}
		}
		name := strings.TrimSpace(recordValueString(item["name"]))
		if name == "" {
			return nil, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d].name is required", i)}
		}
		if err := validateConfiguredProjectDefineName(i, name); err != nil {
			return nil, err
		}
		if _, hasValue := item["value"]; hasValue {
			entries = append(entries, configuredDefineEntry{Name: name, Selector: "all", Source: "literal value"})
		}
		if envName := strings.TrimSpace(recordValueString(item["env"])); envName != "" {
			entries = append(entries, configuredDefineEntry{Name: name, Selector: "all", Source: "legacy env " + envName})
		}
		if secretRef := strings.TrimSpace(recordValueString(item["secret"])); secretRef != "" {
			entries = append(entries, configuredDefineEntry{Name: name, Selector: "all", Source: "encrypted secret"})
		}
		if rawValues, ok := item["values"]; ok {
			values, ok := rawValues.(map[string]any)
			if !ok || values == nil {
				return nil, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d].values must be an object", i)}
			}
			keys := make([]string, 0, len(values))
			for key := range values {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				spec, ok := values[key].(map[string]any)
				if !ok || spec == nil {
					return nil, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d].values.%s for %s must be an object", i, key, name)}
				}
				entries = append(entries, configuredDefineEntry{Name: name, Selector: key, Source: defineSpecSource(spec)})
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name == entries[j].Name {
			return entries[i].Selector < entries[j].Selector
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

func validateConfiguredDefineMetadata(metadata *projectMetadata) error {
	seen := map[string]struct{}{}
	seenSecretRefs := map[string]string{}
	for i, raw := range metadata.WordPress.Defines {
		item, ok := raw.(map[string]any)
		if !ok || item == nil {
			return ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d] must be an object", i)}
		}
		location := fmt.Sprintf("nf.json wordpress.defines[%d]", i)
		if err := validateProjectObjectFields(location, item, "name", "value", "env", "secret", "values"); err != nil {
			return err
		}
		name, err := projectObjectStringField(location, item, "name", true)
		if err != nil {
			return err
		}
		if err := validateConfiguredProjectDefineName(i, name); err != nil {
			return err
		}
		if _, exists := seen[name]; exists {
			return ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines contains duplicate name %q", name)}
		}
		seen[name] = struct{}{}
		shared := map[string]any{}
		if value, ok := item["value"]; ok {
			shared["value"] = value
		}
		if envName, ok := item["env"]; ok {
			shared["env"] = envName
		}
		if secretRef, ok := item["secret"]; ok {
			shared["secret"] = secretRef
		}
		if len(shared) > 0 {
			if err := validateDefineValueSpecShape(i, name, "", shared, seenSecretRefs); err != nil {
				return err
			}
		}
		if rawValues, ok := item["values"]; ok {
			if len(shared) > 0 {
				return ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d] for %s must use either a shared value/env or selector values, not both", i, name)}
			}
			values, ok := rawValues.(map[string]any)
			if !ok || values == nil {
				return ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d].values must be an object", i)}
			}
			if len(values) == 0 {
				return ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d].values for %s must not be empty", i, name)}
			}
			for selector, rawSpec := range values {
				if strings.TrimSpace(selector) == "" {
					return ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d].values for %s contains an empty selector", i, name)}
				}
				spec, ok := rawSpec.(map[string]any)
				if !ok || spec == nil {
					return ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d].values.%s for %s must be an object", i, selector, name)}
				}
				if err := validateDefineValueSpecShape(i, name, selector, spec, seenSecretRefs); err != nil {
					return err
				}
			}
		}
		if len(shared) == 0 {
			if _, ok := item["values"]; !ok {
				return ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d] for %s requires value, env, secret, or values", i, name)}
			}
		}
	}
	return nil
}

func validateDefineValueSpecShape(index int, name, selector string, spec map[string]any, seenSecretRefs map[string]string) error {
	value, hasValue := spec["value"]
	envValue, hasEnv := spec["env"]
	secretValue, hasSecret := spec["secret"]
	location := fmt.Sprintf("nf.json wordpress.defines[%d]", index)
	if selector != "" {
		location += ".values." + selector
	}
	if err := validateProjectObjectFields(location, spec, "value", "env", "secret"); err != nil {
		return err
	}
	sourceCount := 0
	for _, present := range []bool{hasValue, hasEnv, hasSecret} {
		if present {
			sourceCount++
		}
	}
	if sourceCount != 1 {
		return ProjectError{Msg: fmt.Sprintf("%s for %s must use exactly one of value, env, or secret", location, name)}
	}
	if hasEnv {
		envName, ok := envValue.(string)
		if !ok || validateDefineEnvName(strings.TrimSpace(envName)) != nil {
			return ProjectError{Msg: fmt.Sprintf("%s.env for %s must be an environment variable name", location, name)}
		}
		return nil
	}
	if hasSecret {
		secretRef, ok := secretValue.(string)
		secretRef = strings.TrimSpace(secretRef)
		if !ok || !defineSecretRefPattern.MatchString(secretRef) {
			return ProjectError{Msg: fmt.Sprintf("%s.secret for %s must be an nf encrypted define reference", location, name)}
		}
		label := name
		if selector != "" {
			label += " (" + selector + ")"
		}
		if previous, exists := seenSecretRefs[secretRef]; exists {
			return ProjectError{Msg: fmt.Sprintf("%s.secret for %s duplicates the encrypted reference used by %s", location, name, previous)}
		}
		seenSecretRefs[secretRef] = label
		return nil
	}
	if _, err := phpConfigDefineValueLiteral(value); err != nil {
		return ProjectError{Msg: fmt.Sprintf("%s.value for %s: %s", location, name, err)}
	}
	return nil
}

func defineSpecSource(spec map[string]any) string {
	if envName := strings.TrimSpace(recordValueString(spec["env"])); envName != "" {
		return "legacy env " + envName
	}
	if secretRef := strings.TrimSpace(recordValueString(spec["secret"])); secretRef != "" {
		return "encrypted secret"
	}
	if _, ok := spec["value"]; ok {
		return "literal value"
	}
	return "unconfigured"
}

func printDefineRemoteHeader(target envRemoteSyncTarget) {
	fmt.Printf("  remote:   %s\n", target.RemoteName)
	fmt.Printf("  site:     %s\n", target.SiteID)
	fmt.Printf("  env:      %s\n", target.Env)
	fmt.Printf("  provider: %s\n", target.Provider)
}

func printDefineRemoteCommand(target envRemoteSyncTarget) {
	label := "<sync defines>"
	if target.SudoFileOps {
		label = "<sudo sync defines>"
	}
	fmt.Printf("> ssh -p %s %s@%s %s\n", target.SSHPort, target.SSHUser, target.SSHHost, shellQuoteArg(label))
}

func remoteDefineStdinArgs(target envRemoteSyncTarget) []string {
	if target.SudoFileOps {
		return remoteSSHArgs(target, "sudo bash -s")
	}
	return remoteSSHArgs(target, "bash -s")
}

func ensureLocalWPConfigDefines(cfg envConfig, metadata *projectMetadata) error {
	defines, err := loadWordPressConfigDefines(cfg.RepoRoot, metadata, wpConfigDefineSelector{Local: true})
	if err != nil {
		return err
	}
	if len(defines) == 0 {
		return nil
	}
	script := renderWPConfigDefineScript("/var/www/html", defines)
	service := firstNonEmpty(cfg.WordpressService, "wordpress")
	args := envComposeArgs(cfg, "exec", "-T", "--user", "root", service, "sh", "-s")
	preview := envComposeArgs(cfg, "exec", "-T", "--user", "root", service, "<sync defines>")
	return runCommandSpecStdinWithPreview(execSpec{Dir: localEnvDir(cfg), Args: args}, preview, script)
}

func loadWordPressConfigDefines(root string, metadata *projectMetadata, selector wpConfigDefineSelector) ([]wpConfigDefine, error) {
	raw := metadata.WordPress.Defines
	defines := make([]wpConfigDefine, 0, len(raw))
	seen := map[string]struct{}{}
	var secretStore *defineSecretStore
	resolveSecret := func(ref, name string) (string, error) {
		if secretStore == nil {
			var err error
			secretStore, err = loadDefineSecretStore(root, metadata, false)
			if err != nil {
				return "", err
			}
		}
		value, ok := secretStore.Secrets[ref]
		if !ok {
			return "", ProjectError{Msg: fmt.Sprintf("%s has no encrypted value for %s", defineSecretStoreFilename, name)}
		}
		return value, nil
	}
	for i, item := range raw {
		define, ok, err := parseWordPressConfigDefine(i, item, selector, resolveSecret)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if _, exists := seen[define.Name]; exists {
			return nil, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines contains duplicate name %q", define.Name)}
		}
		seen[define.Name] = struct{}{}
		defines = append(defines, define)
	}
	sort.Slice(defines, func(i, j int) bool { return defines[i].Name < defines[j].Name })
	return defines, nil
}

func parseWordPressConfigDefine(index int, value any, selector wpConfigDefineSelector, resolveSecret func(string, string) (string, error)) (wpConfigDefine, bool, error) {
	item, ok := value.(map[string]any)
	if !ok || item == nil {
		return wpConfigDefine{}, false, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d] must be an object", index)}
	}
	name := strings.TrimSpace(recordValueString(item["name"]))
	if name == "" {
		return wpConfigDefine{}, false, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d].name is required", index)}
	}
	if !wpConfigDefineNamePattern.MatchString(name) {
		return wpConfigDefine{}, false, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d].name must be a PHP constant name", index)}
	}
	if err := validateConfiguredProjectDefineName(index, name); err != nil {
		return wpConfigDefine{}, false, err
	}
	valueSpec := item
	if values, ok := item["values"]; ok {
		selected, matched, err := selectWordPressConfigDefineValue(index, name, values, selector)
		if err != nil {
			return wpConfigDefine{}, false, err
		}
		if matched {
			valueSpec = selected
		} else if _, hasValue := item["value"]; !hasValue && strings.TrimSpace(recordValueString(item["env"])) == "" && strings.TrimSpace(recordValueString(item["secret"])) == "" {
			return wpConfigDefine{}, false, nil
		}
	}
	phpValue, source, err := parseWordPressConfigDefineValue(index, name, valueSpec, resolveSecret)
	if err != nil {
		return wpConfigDefine{}, false, err
	}
	return wpConfigDefine{Name: name, PHPValue: phpValue, Source: source, Mode: wpConfigDefineModeForce}, true, nil
}

func selectWordPressConfigDefineValue(index int, name string, values any, selector wpConfigDefineSelector) (map[string]any, bool, error) {
	valueMap, ok := values.(map[string]any)
	if !ok || valueMap == nil {
		return nil, false, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d].values must be an object", index)}
	}
	for _, key := range selector.wpConfigValueKeys() {
		if raw, ok := valueMap[key]; ok {
			selected, ok := raw.(map[string]any)
			if !ok || selected == nil {
				return nil, false, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d].values.%s for %s must be an object", index, key, name)}
			}
			return selected, true, nil
		}
	}
	return nil, false, nil
}

func (s wpConfigDefineSelector) wpConfigValueKeys() []string {
	keys := []string{}
	if s.Local {
		keys = append(keys, "local")
	} else {
		if strings.TrimSpace(s.RemoteName) != "" {
			keys = append(keys, strings.TrimSpace(s.RemoteName))
		}
		if strings.TrimSpace(s.EnvID) != "" {
			keys = append(keys, strings.TrimSpace(s.EnvID))
		}
		if strings.TrimSpace(s.Env) != "" {
			keys = append(keys, strings.TrimSpace(s.Env))
		}
	}
	keys = append(keys, "default")
	return uniqueStringsPreserveOrder(keys)
}

func parseWordPressConfigDefineValue(index int, name string, spec map[string]any, resolveSecret func(string, string) (string, error)) (string, string, error) {
	_, hasEnv := spec["env"]
	value, hasValue := spec["value"]
	secretRef := strings.TrimSpace(recordValueString(spec["secret"]))
	hasSecret := secretRef != ""
	sourceCount := 0
	for _, present := range []bool{hasEnv, hasValue, hasSecret} {
		if present {
			sourceCount++
		}
	}
	if sourceCount != 1 {
		return "", "", ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d] for %s must use exactly one of env, value, or secret", index, name)}
	}
	if hasEnv {
		envName := strings.TrimSpace(recordValueString(spec["env"]))
		if envName == "" {
			return "", "", ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d].env for %s must not be empty", index, name)}
		}
		if !wpConfigDefineNamePattern.MatchString(envName) {
			return "", "", ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d].env for %s must be an environment variable name", index, name)}
		}
		resolved := envwizard.Value(envName)
		if resolved == "" {
			return "", "", ProjectError{Msg: fmt.Sprintf("Expected %s in the environment or %s for nf.json wordpress.defines[%d] %s.", envName, config.EnvFile(), index, name)}
		}
		return phpConfigDefineLiteral(resolved), "legacy env " + envName, nil
	}
	if hasSecret {
		if !defineSecretRefPattern.MatchString(secretRef) {
			return "", "", ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d].secret for %s must be an nf encrypted define reference", index, name)}
		}
		resolved, err := resolveSecret(secretRef, name)
		if err != nil {
			return "", "", err
		}
		return phpConfigDefineLiteral(resolved), "encrypted secret", nil
	}
	if hasValue {
		literal, err := phpConfigDefineValueLiteral(value)
		if err != nil {
			return "", "", ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d].value for %s: %s", index, name, err)}
		}
		return literal, "literal value", nil
	}
	return "", "", ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d] for %s requires env, value, or secret", index, name)}
}

func phpConfigDefineValueLiteral(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return phpConfigDefineLiteral(typed), nil
	case bool:
		if typed {
			return "true", nil
		}
		return "false", nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return "", fmt.Errorf("number must be finite")
		}
		if math.Trunc(typed) == typed {
			return strconv.FormatInt(int64(typed), 10), nil
		}
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case int:
		return strconv.Itoa(typed), nil
	case nil:
		return "", fmt.Errorf("null is not supported")
	default:
		return "", fmt.Errorf("value must be a string, boolean, or number")
	}
}

func phpConfigDefineLiteral(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `'`, `\'`)
	return "'" + value + "'"
}

func kinstaWhitelabelWPConfigDefine() wpConfigDefine {
	return wpConfigDefine{Name: "KINSTAMU_WHITELABEL", PHPValue: "true", Source: "Kinsta whitelabel", Mode: wpConfigDefineModeReplaceFalse}
}

func renderWPConfigDefineScript(sitePath string, defines []wpConfigDefine) string {
	return renderWPConfigDefineScriptWithBlock(sitePath, defines, wpConfigProjectBlockBegin, wpConfigProjectBlockEnd, wpConfigLegacyModeProject)
}

func renderWPConfigProviderDefineScript(sitePath string, defines []wpConfigDefine) string {
	return renderWPConfigDefineScriptWithBlock(sitePath, defines, wpConfigProviderBlockBegin, wpConfigProviderBlockEnd, wpConfigLegacyModeProvider)
}

func renderWPConfigDefineScriptWithBlock(sitePath string, defines []wpConfigDefine, beginMarker string, endMarker string, legacyMode string) string {
	patches := make([]wpConfigPatchDefinition, 0, len(defines))
	for _, define := range defines {
		mode := firstNonEmpty(define.Mode, wpConfigDefineModeForce)
		patches = append(patches, wpConfigPatchDefinition{Name: define.Name, Value: define.PHPValue, Mode: mode, Source: define.Source})
	}
	data, _ := json.Marshal(patches)
	providerNames, _ := json.Marshal(providerOwnedWPConfigDefineNames())
	return fmt.Sprintf(`set -eu
site_path=%s
config_file="$site_path/wp-config.php"
if [ ! -f "$config_file" ]; then
  printf 'wp-config.php not found at %%s\n' "$config_file" >&2
  exit 1
fi
export NF_WP_CONFIG_FILE="$config_file"
php <<'PHP'
<?php
$configFile = getenv('NF_WP_CONFIG_FILE');
$definitionsJson = <<<'JSON'
%s
JSON;
$providerNamesJson = <<<'JSON'
%s
JSON;
$legacyMode = %s;

$definitions = json_decode($definitionsJson, true);
if (!is_array($definitions)) {
    fwrite(STDERR, "Invalid nf wp-config definitions.\n");
    exit(1);
}
$providerNames = json_decode($providerNamesJson, true);
if (!is_array($providerNames)) {
    fwrite(STDERR, "Invalid nf wp-config provider define list.\n");
    exit(1);
}

$contents = file_get_contents($configFile);
if ($contents === false) {
    fwrite(STDERR, "Could not read wp-config.php.\n");
    exit(1);
}

function nf_wp_config_define_statement($name, $value) {
    return "define('" . str_replace("'", "\\'", $name) . "', " . $value . ");";
}

function nf_wp_config_managed_begin_marker() {
    return %s;
}

function nf_wp_config_managed_end_marker() {
    return %s;
}

function nf_wp_config_managed_names($definitions) {
    $names = [];
    foreach ($definitions as $definition) {
        $name = (string) ($definition['name'] ?? '');
        if ($name !== '') {
            $names[$name] = true;
        }
    }
    return $names;
}

function nf_wp_config_insert_block($contents, $block) {
    if ($block === '') {
        return $contents;
    }
    $insert = "\n" . $block . "\n";
    $marker = "/* That's all, stop editing! Happy publishing. */";
    $pos = strpos($contents, $marker);
    if ($pos !== false) {
        return substr($contents, 0, $pos) . $insert . substr($contents, $pos);
    }
    if (preg_match('/require_once\s*\(?\s*ABSPATH\s*\.\s*[\'\"]wp-settings\.php[\'\"]\s*\)?\s*;/', $contents, $match, PREG_OFFSET_CAPTURE)) {
        $pos = $match[0][1];
        return substr($contents, 0, $pos) . $insert . substr($contents, $pos);
    }
    throw new RuntimeException('Could not find a safe insertion point in wp-config.php.');
}

function nf_wp_config_strip_managed_blocks($contents) {
    $begin = nf_wp_config_managed_begin_marker();
    $end = nf_wp_config_managed_end_marker();
    if (substr_count($contents, $begin) !== substr_count($contents, $end)) {
        throw new RuntimeException('Refusing to manage wp-config.php with unmatched nf-managed define block markers.');
    }
    $pattern = '/\s*' . preg_quote($begin, '/') . '.*?' . preg_quote($end, '/') . '\s*/s';
    $updated = preg_replace($pattern, "\n", $contents);
    if ($updated === null) {
        throw new RuntimeException('Could not inspect nf-managed wp-config define block.');
    }
    return $updated;
}

function nf_wp_config_strip_legacy_managed_defines($contents, $legacyMode, $managedNames, $providerNames) {
    $pattern = '/\s*\/\* nf-managed wp-config defines \*\/\s*(define\s*\(\s*[\'\"]([A-Za-z_][A-Za-z0-9_]*)[\'\"]\s*,.*?\)\s*;)\s*/s';
    $updated = preg_replace_callback($pattern, function ($match) use ($legacyMode, $managedNames, $providerNames) {
        $name = $match[2];
        $strip = false;
        if ($legacyMode === 'project') {
            $strip = !in_array($name, $providerNames, true);
        } elseif ($legacyMode === 'provider') {
            $strip = isset($managedNames[$name]);
        }
        if ($strip) {
            return "\n";
        }
        return $match[0];
    }, $contents);
    if ($updated === null) {
        throw new RuntimeException('Could not inspect legacy nf-managed wp-config define markers.');
    }
    return $updated;
}

function nf_wp_config_define_matches($contents, $name) {
    $pattern = '/define\s*\(\s*[\'\"]' . preg_quote($name, '/') . '[\'\"]\s*,(.*?)\)\s*;/s';
    $count = preg_match_all($pattern, $contents, $matches, PREG_OFFSET_CAPTURE);
    if ($count === false) {
        throw new RuntimeException('Could not inspect wp-config.php define for ' . $name . '.');
    }
    return [$count, $matches];
}

$strippedContents = $contents;
try {
    $managedNames = nf_wp_config_managed_names($definitions);
    $strippedContents = nf_wp_config_strip_managed_blocks($strippedContents);
    $strippedContents = nf_wp_config_strip_legacy_managed_defines($strippedContents, $legacyMode, $managedNames, $providerNames);
} catch (RuntimeException $error) {
    fwrite(STDERR, $error->getMessage() . "\n");
    exit(1);
}

$statements = [];
foreach ($definitions as $definition) {
    $name = (string) ($definition['name'] ?? '');
    $value = (string) ($definition['value'] ?? '');
    $mode = (string) ($definition['mode'] ?? 'force');
    if ($name === '' || $value === '') {
        fwrite(STDERR, "Invalid nf wp-config definition.\n");
        exit(1);
    }
    $statement = nf_wp_config_define_statement($name, $value);
    try {
        [$matchCount, $matches] = nf_wp_config_define_matches($strippedContents, $name);
    } catch (RuntimeException $error) {
        fwrite(STDERR, $error->getMessage() . "\n");
        exit(1);
    }
    if ($matchCount > 1) {
        fwrite(STDERR, 'Refusing to manage duplicate wp-config define for ' . $name . ". Remove duplicate definitions first.\n");
        exit(1);
    }
    if ($matchCount === 1) {
        $existing = trim($matches[1][0][0]);
        if ($mode === 'replace_false' && !preg_match('/^false$/i', $existing)) {
            continue;
        }
        if ($mode !== 'replace_false') {
            fwrite(STDERR, 'Refusing to manage wp-config define for ' . $name . ' because it already exists outside the nf-managed block. Move it into nf.json or remove the manual definition first.' . "\n");
            exit(1);
        }
        $start = $matches[0][0][1];
        $length = strlen($matches[0][0][0]);
        $strippedContents = substr($strippedContents, 0, $start) . substr($strippedContents, $start + $length);
    }
    $statements[] = $statement;
}

$block = '';
if (count($statements) > 0) {
    $block = nf_wp_config_managed_begin_marker() . "\n" . implode("\n", $statements) . "\n" . nf_wp_config_managed_end_marker();
}
try {
    $contents = nf_wp_config_insert_block($strippedContents, $block);
} catch (RuntimeException $error) {
    fwrite(STDERR, $error->getMessage() . "\n");
    exit(1);
}

if ($contents === file_get_contents($configFile)) {
    echo "wp-config.php already matches nf defines.\n";
    exit(0);
}
if (strpos($contents, 'wp-settings.php') === false) {
    fwrite(STDERR, "Refusing to write wp-config.php without wp-settings.php.\n");
    exit(1);
}

$dir = dirname($configFile);
$tmp = tempnam($dir, '.nf-wp-config-');
if ($tmp === false) {
    fwrite(STDERR, "Could not create temporary wp-config file.\n");
    exit(1);
}
$mode = fileperms($configFile) & 0777;
$owner = fileowner($configFile);
$group = filegroup($configFile);
if (file_put_contents($tmp, $contents) === false) {
    @unlink($tmp);
    fwrite(STDERR, "Could not write temporary wp-config file.\n");
    exit(1);
}
@chown($tmp, $owner);
@chgrp($tmp, $group);
@chmod($tmp, $mode);
if (!@rename($tmp, $configFile)) {
    @unlink($tmp);
    fwrite(STDERR, "Could not replace wp-config.php.\n");
    exit(1);
}
@chmod($configFile, $mode);
echo "wp-config.php updated.\n";
PHP
`, shellQuoteArg(sitePath), string(data), string(providerNames), phpConfigDefineLiteral(legacyMode), phpConfigDefineLiteral(beginMarker), phpConfigDefineLiteral(endMarker))
}

func renderWPConfigDefineStatusScript(sitePath string, defines []wpConfigDefine) string {
	patches := make([]wpConfigPatchDefinition, 0, len(defines))
	for _, define := range defines {
		mode := firstNonEmpty(define.Mode, wpConfigDefineModeForce)
		patches = append(patches, wpConfigPatchDefinition{Name: define.Name, Value: define.PHPValue, Mode: mode, Source: define.Source})
	}
	data, _ := json.Marshal(patches)
	providerNames, _ := json.Marshal(providerOwnedWPConfigDefineNames())
	return fmt.Sprintf(`set -eu
site_path=%s
config_file="$site_path/wp-config.php"
if [ ! -f "$config_file" ]; then
  printf 'wp-config.php not found at %%s\n' "$config_file" >&2
  exit 1
fi
export NF_WP_CONFIG_FILE="$config_file"
php <<'PHP'
<?php
$configFile = getenv('NF_WP_CONFIG_FILE');
$definitionsJson = <<<'JSON'
%s
JSON;
$providerNamesJson = <<<'JSON'
%s
JSON;

$definitions = json_decode($definitionsJson, true);
if (!is_array($definitions)) {
    fwrite(STDERR, "Invalid nf wp-config definitions.\n");
    exit(1);
}
$providerNames = json_decode($providerNamesJson, true);
if (!is_array($providerNames)) {
    fwrite(STDERR, "Invalid nf wp-config provider define list.\n");
    exit(1);
}
$contents = file_get_contents($configFile);
if ($contents === false) {
    fwrite(STDERR, "Could not read wp-config.php.\n");
    exit(1);
}
function nf_wp_config_define_matches($contents, $name) {
    $pattern = '/define\s*\(\s*[\'\"]' . preg_quote($name, '/') . '[\'\"]\s*,(.*?)\)\s*;/s';
    $count = preg_match_all($pattern, $contents, $matches, PREG_OFFSET_CAPTURE);
    if ($count === false) {
        throw new RuntimeException('Could not inspect wp-config.php define for ' . $name . '.');
    }
    return [$count, $matches];
}
function nf_wp_config_managed_parts($contents) {
    $begin = %s;
    $end = %s;
    if (substr_count($contents, $begin) !== substr_count($contents, $end)) {
        throw new RuntimeException('Refusing to inspect wp-config.php with unmatched nf-managed define block markers.');
    }
    $managed = '';
    $blockPattern = '/\s*' . preg_quote($begin, '/') . '(.*?)' . preg_quote($end, '/') . '\s*/s';
    $blockCount = preg_match_all($blockPattern, $contents, $blocks);
    if ($blockCount === false) {
        throw new RuntimeException('Could not inspect nf-managed wp-config define block.');
    }
    if ($blockCount > 0) {
        $managed .= "\n" . implode("\n", $blocks[1]);
    }
    $outside = preg_replace($blockPattern, "\n", $contents);
    if ($outside === null) {
        throw new RuntimeException('Could not inspect nf-managed wp-config define block.');
    }
    $legacyPattern = '/\s*\/\* nf-managed wp-config defines \*\/\s*(define\s*\(\s*[\'\"]([A-Za-z_][A-Za-z0-9_]*)[\'\"]\s*,.*?\)\s*;)\s*/s';
    $outside = preg_replace_callback($legacyPattern, function ($match) use (&$managed, $providerNames) {
        if (in_array($match[2], $providerNames, true)) {
            return $match[0];
        }
        $managed .= "\n" . $match[1];
        return "\n";
    }, $outside);
    if ($outside === null) {
        throw new RuntimeException('Could not inspect legacy nf-managed wp-config define markers.');
    }
    return [$managed, $outside];
}
try {
    [$managedContents, $outsideContents] = nf_wp_config_managed_parts($contents);
} catch (RuntimeException $error) {
    fwrite(STDERR, $error->getMessage() . "\n");
    exit(1);
}
foreach ($definitions as $definition) {
    $name = (string) ($definition['name'] ?? '');
    $value = (string) ($definition['value'] ?? '');
    $source = (string) ($definition['source'] ?? 'configured value');
    $status = 'missing';
    if ($name !== '' && $value !== '') {
        try {
            [$managedCount, $managedMatches] = nf_wp_config_define_matches($managedContents, $name);
            [$outsideCount, $_outsideMatches] = nf_wp_config_define_matches($outsideContents, $name);
        } catch (RuntimeException $error) {
            fwrite(STDERR, $error->getMessage() . "\n");
            exit(1);
        }
        if ($outsideCount > 0 || $managedCount > 1) {
            $status = 'duplicate';
        } elseif ($managedCount === 1) {
            $status = trim($managedMatches[1][0][0]) === $value ? 'matched' : 'different';
        }
    }
    echo $name . "\t" . $source . "\t" . $status . "\n";
}
PHP
`, shellQuoteArg(sitePath), string(data), string(providerNames), phpConfigDefineLiteral(wpConfigProjectBlockBegin), phpConfigDefineLiteral(wpConfigProjectBlockEnd))
}

func printDefineStatusOutput(output string) {
	rows := [][]string{{"name", "source", "status"}}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		for len(parts) < 3 {
			parts = append(parts, "")
		}
		rows = append(rows, []string{parts[0], parts[1], parts[2]})
	}
	if len(rows) == 1 {
		fmt.Println("No defines configured.")
		return
	}
	fmt.Println(formatTable(rows))
}

func defineStatusExitCode(output string) int {
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) >= 3 && strings.TrimSpace(parts[2]) == "duplicate" {
			return 1
		}
	}
	return 0
}

func uniqueStringsPreserveOrder(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

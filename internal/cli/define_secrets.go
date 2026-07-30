package cli

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"filippo.io/age"
	"filippo.io/age/armor"
	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/passwords"
)

const (
	defineSecretStoreFilename    = "nf.age"
	defineSecretStoreVersion     = 1
	defineSecretStoreMaxFileSize = 4 << 20
	defineSecretStoreMaxJSONSize = 1 << 20
	defineSecretStdinMaxSize     = 4 << 10
)

var defineSecretRefPattern = regexp.MustCompile(`^wpdef_[0-9a-f]{32}$`)

type defineSecretStore struct {
	Version    int               `json:"version"`
	Recipients []string          `json:"recipients"`
	Secrets    map[string]string `json:"secrets"`
}

type defineEnvAssignment struct {
	Name  string
	Value string
	Line  int
}

type defineEnvMigration struct {
	Name      string
	Selector  string
	EnvName   string
	Value     string
	Spec      map[string]any
	NewDefine bool
	SecretRef string
}

func defineSecretStorePath(root string) string {
	return filepath.Join(root, defineSecretStoreFilename)
}

func currentDefineAgeIdentity() (*age.X25519Identity, string, error) {
	salt, err := passwords.SecretSalt()
	if err != nil {
		return nil, "", err
	}
	identityText, recipient, err := passwords.DeriveAgeIdentity(salt)
	if err != nil {
		return nil, "", err
	}
	identity, err := age.ParseX25519Identity(identityText)
	if err != nil {
		return nil, "", fmt.Errorf("parse derived age identity: %w", err)
	}
	return identity, recipient, nil
}

func newDefineSecretStore() (*defineSecretStore, error) {
	_, recipient, err := currentDefineAgeIdentity()
	if err != nil {
		return nil, err
	}
	return &defineSecretStore{
		Version:    defineSecretStoreVersion,
		Recipients: []string{recipient},
		Secrets:    map[string]string{},
	}, nil
}

func loadDefineSecretStore(root string, metadata *projectMetadata, allowMissing bool) (*defineSecretStore, error) {
	path := defineSecretStorePath(root)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) && allowMissing {
			return newDefineSecretStore()
		}
		if os.IsNotExist(err) {
			return nil, ProjectError{Msg: fmt.Sprintf("%s is required by encrypted WordPress defines", path)}
		}
		return nil, fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, ProjectError{Msg: fmt.Sprintf("%s must be a regular file", path)}
	}
	if info.Size() > defineSecretStoreMaxFileSize {
		return nil, ProjectError{Msg: fmt.Sprintf("%s exceeds the %d-byte size limit", path, defineSecretStoreMaxFileSize)}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	identity, currentRecipient, err := currentDefineAgeIdentity()
	if err != nil {
		return nil, err
	}
	reader, err := age.Decrypt(armor.NewReader(bytes.NewReader(data)), identity)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return nil, ProjectError{Msg: fmt.Sprintf("could not decrypt %s with the current NF_PASSWORD_SALT", path)}
		}
		return nil, fmt.Errorf("decrypt %s: %w", path, err)
	}
	plaintext, err := io.ReadAll(io.LimitReader(reader, defineSecretStoreMaxJSONSize+1))
	if err != nil {
		return nil, fmt.Errorf("decrypt %s: %w", path, err)
	}
	if len(plaintext) > defineSecretStoreMaxJSONSize {
		return nil, ProjectError{Msg: fmt.Sprintf("decrypted %s exceeds the %d-byte size limit", path, defineSecretStoreMaxJSONSize)}
	}
	store, err := decodeDefineSecretStore(plaintext)
	if err != nil {
		return nil, ProjectError{Msg: fmt.Sprintf("invalid %s: %s", path, err)}
	}
	if !containsString(store.Recipients, currentRecipient) {
		return nil, ProjectError{Msg: fmt.Sprintf("%s does not record the recipient for the current NF_PASSWORD_SALT", path)}
	}
	if metadata != nil {
		if err := validateDefineSecretStoreReferences(metadata, store); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func decodeDefineSecretStore(data []byte) (*defineSecretStore, error) {
	var store defineSecretStore
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&store); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("unexpected data after JSON document")
		}
		return nil, err
	}
	if store.Version != defineSecretStoreVersion {
		return nil, fmt.Errorf("version must be %d", defineSecretStoreVersion)
	}
	if len(store.Recipients) == 0 {
		return nil, fmt.Errorf("recipients must not be empty")
	}
	seenRecipients := map[string]struct{}{}
	for i, recipientText := range store.Recipients {
		recipientText = strings.TrimSpace(recipientText)
		if _, err := age.ParseX25519Recipient(recipientText); err != nil {
			return nil, fmt.Errorf("recipient is not an age X25519 recipient")
		}
		if _, exists := seenRecipients[recipientText]; exists {
			return nil, fmt.Errorf("recipients contains a duplicate")
		}
		seenRecipients[recipientText] = struct{}{}
		store.Recipients[i] = recipientText
	}
	if store.Secrets == nil {
		return nil, fmt.Errorf("secrets must be an object")
	}
	for ref, value := range store.Secrets {
		if !defineSecretRefPattern.MatchString(ref) {
			return nil, fmt.Errorf("secrets contains an invalid reference")
		}
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("secret %s must not be empty", ref)
		}
	}
	return &store, nil
}

func writeDefineSecretStore(root string, store *defineSecretStore) error {
	if store == nil {
		return fmt.Errorf("encrypted define store is required")
	}
	plain, err := json.Marshal(store)
	if err != nil {
		return fmt.Errorf("encode encrypted define store: %w", err)
	}
	validated, err := decodeDefineSecretStore(plain)
	if err != nil {
		return fmt.Errorf("validate encrypted define store: %w", err)
	}
	plain, err = json.Marshal(validated)
	if err != nil {
		return fmt.Errorf("encode validated encrypted define store: %w", err)
	}
	if len(plain)+1 > defineSecretStoreMaxJSONSize {
		return ProjectError{Msg: fmt.Sprintf("encrypted define store exceeds the %d-byte plaintext size limit", defineSecretStoreMaxJSONSize)}
	}
	recipients := make([]age.Recipient, 0, len(store.Recipients))
	for _, recipientText := range validated.Recipients {
		recipient, err := age.ParseX25519Recipient(recipientText)
		if err != nil {
			return fmt.Errorf("parse encrypted define recipient: %w", err)
		}
		recipients = append(recipients, recipient)
	}
	var ciphertext bytes.Buffer
	armorWriter := armor.NewWriter(&ciphertext)
	ageWriter, err := age.Encrypt(armorWriter, recipients...)
	if err != nil {
		_ = armorWriter.Close()
		return fmt.Errorf("encrypt %s: %w", defineSecretStoreFilename, err)
	}
	if _, err := ageWriter.Write(append(plain, '\n')); err != nil {
		_ = ageWriter.Close()
		_ = armorWriter.Close()
		return fmt.Errorf("encrypt %s: %w", defineSecretStoreFilename, err)
	}
	if err := ageWriter.Close(); err != nil {
		_ = armorWriter.Close()
		return fmt.Errorf("finish encrypting %s: %w", defineSecretStoreFilename, err)
	}
	if err := armorWriter.Close(); err != nil {
		return fmt.Errorf("armor %s: %w", defineSecretStoreFilename, err)
	}
	return writeDefineSecretCiphertext(root, ciphertext.Bytes())
}

func writeDefineSecretCiphertext(root string, data []byte) error {
	path := defineSecretStorePath(root)
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	temp, err := os.CreateTemp(root, ".nf.age-*")
	if err != nil {
		return fmt.Errorf("create temporary %s: %w", defineSecretStoreFilename, err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return fmt.Errorf("set temporary %s permissions: %w", defineSecretStoreFilename, err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write temporary %s: %w", defineSecretStoreFilename, err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync temporary %s: %w", defineSecretStoreFilename, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary %s: %w", defineSecretStoreFilename, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return syncDirectory(root)
}

func removeDefineSecretStore(root string) error {
	path := defineSecretStorePath(root)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return syncDirectory(root)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func configuredDefineSecretRefs(metadata *projectMetadata) (map[string]string, error) {
	refs := map[string]string{}
	for i, raw := range metadata.WordPress.Defines {
		item, ok := raw.(map[string]any)
		if !ok || item == nil {
			return nil, ProjectError{Msg: fmt.Sprintf("nf.json wordpress.defines[%d] must be an object", i)}
		}
		name := strings.TrimSpace(recordValueString(item["name"]))
		if ref := strings.TrimSpace(recordValueString(item["secret"])); ref != "" {
			if previous, exists := refs[ref]; exists {
				return nil, ProjectError{Msg: fmt.Sprintf("encrypted define reference is shared by %s and %s", previous, name)}
			}
			refs[ref] = name + " (all)"
		}
		values, _ := item["values"].(map[string]any)
		for selector, rawSpec := range values {
			spec, _ := rawSpec.(map[string]any)
			if spec == nil {
				continue
			}
			if ref := strings.TrimSpace(recordValueString(spec["secret"])); ref != "" {
				location := name + " (" + selector + ")"
				if previous, exists := refs[ref]; exists {
					return nil, ProjectError{Msg: fmt.Sprintf("encrypted define reference is shared by %s and %s", previous, location)}
				}
				refs[ref] = location
			}
		}
	}
	return refs, nil
}

func validateDefineSecretStoreReferences(metadata *projectMetadata, store *defineSecretStore) error {
	refs, err := configuredDefineSecretRefs(metadata)
	if err != nil {
		return err
	}
	for ref, location := range refs {
		if _, ok := store.Secrets[ref]; !ok {
			return ProjectError{Msg: fmt.Sprintf("%s has no encrypted value for %s", defineSecretStoreFilename, location)}
		}
	}
	return nil
}

func pruneDefineSecretStore(root string, metadata *projectMetadata, store *defineSecretStore) error {
	refs, err := configuredDefineSecretRefs(metadata)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return removeDefineSecretStore(root)
	}
	pruned := make(map[string]string, len(refs))
	for ref, location := range refs {
		value, ok := store.Secrets[ref]
		if !ok {
			return ProjectError{Msg: fmt.Sprintf("%s has no encrypted value for %s", defineSecretStoreFilename, location)}
		}
		pruned[ref] = value
	}
	store.Secrets = pruned
	return writeDefineSecretStore(root, store)
}

func generateDefineSecretRef(existing map[string]string) (string, error) {
	for attempts := 0; attempts < 8; attempts++ {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", fmt.Errorf("generate encrypted define reference: %w", err)
		}
		ref := "wpdef_" + hex.EncodeToString(raw[:])
		if _, exists := existing[ref]; !exists {
			return ref, nil
		}
	}
	return "", fmt.Errorf("could not generate a unique encrypted define reference")
}

func readDefineSecretStdin() (string, error) {
	data, err := io.ReadAll(io.LimitReader(defineSecretStdin, defineSecretStdinMaxSize+3))
	if err != nil {
		return "", fmt.Errorf("read encrypted define from stdin: %w", err)
	}
	value := string(data)
	value = strings.TrimSuffix(value, "\n")
	value = strings.TrimSuffix(value, "\r")
	if len(value) > defineSecretStdinMaxSize {
		return "", fmt.Errorf("encrypted define from stdin exceeds %d bytes", defineSecretStdinMaxSize)
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("encrypted define value must not be empty")
	}
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("encrypted define value must be one line")
	}
	return value, nil
}

func removedDefineSecretRefs(before, after map[string]string) bool {
	for ref := range before {
		if _, ok := after[ref]; !ok {
			return true
		}
	}
	return false
}

func configuredDefineSpec(metadata *projectMetadata, name, selector string) map[string]any {
	for _, raw := range metadata.WordPress.Defines {
		item, _ := raw.(map[string]any)
		if item == nil || strings.TrimSpace(recordValueString(item["name"])) != name {
			continue
		}
		if selector == "" {
			if values, ok := item["values"].(map[string]any); ok {
				spec, _ := values["default"].(map[string]any)
				return spec
			}
			return item
		}
		values, _ := item["values"].(map[string]any)
		spec, _ := values[selector].(map[string]any)
		return spec
	}
	return nil
}

func parseProjectDefineEnv(path string) ([]defineEnvAssignment, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, ProjectError{Msg: fmt.Sprintf("%s must be a regular file", path)}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), defineSecretStoreMaxJSONSize)
	assignments := []defineEnvAssignment{}
	seen := map[string]int{}
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		name, rawValue, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, ProjectError{Msg: fmt.Sprintf("%s line %d must be NAME=VALUE", path, lineNumber)}
		}
		if err := validateProjectDefineName(name); err != nil {
			return nil, ProjectError{Msg: fmt.Sprintf("%s line %d name %s: %s", path, lineNumber, name, err)}
		}
		if previous, exists := seen[name]; exists {
			return nil, ProjectError{Msg: fmt.Sprintf("%s line %d duplicates %s from line %d", path, lineNumber, name, previous)}
		}
		value := strings.TrimSpace(rawValue)
		if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
			quote := value[:1]
			if len(value) < 2 || !strings.HasSuffix(value, quote) {
				return nil, ProjectError{Msg: fmt.Sprintf("%s line %d for %s has an unmatched quote", path, lineNumber, name)}
			}
			value = value[1 : len(value)-1]
		} else if strings.ContainsAny(value, "\"'") {
			return nil, ProjectError{Msg: fmt.Sprintf("%s line %d for %s has an unmatched quote", path, lineNumber, name)}
		}
		if strings.TrimSpace(value) == "" {
			return nil, ProjectError{Msg: fmt.Sprintf("%s line %d for %s must not be empty", path, lineNumber, name)}
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, ProjectError{Msg: fmt.Sprintf("%s line %d for %s contains unsupported control characters", path, lineNumber, name)}
		}
		seen[name] = lineNumber
		assignments = append(assignments, defineEnvAssignment{Name: name, Value: value, Line: lineNumber})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return assignments, nil
}

func cmdDefineMigrateEnv(root string, metadata *projectMetadata, args []string) int {
	dryRun := false
	deleteSource := false
	for _, arg := range args {
		switch arg {
		case "--dry-run":
			dryRun = true
		case "--delete-source":
			deleteSource = true
		default:
			fmt.Fprintf(os.Stderr, "unknown define migrate-env flag: %s\n", arg)
			return 1
		}
	}
	if dryRun && deleteSource {
		fmt.Fprintln(os.Stderr, "define migrate-env --dry-run cannot be combined with --delete-source")
		return 1
	}
	envPath := filepath.Join(root, ".env")
	_, envStatErr := os.Lstat(envPath)
	envExists := envStatErr == nil
	if envStatErr != nil && !os.IsNotExist(envStatErr) {
		fmt.Fprintf(os.Stderr, "inspect %s: %s\n", envPath, envStatErr)
		return 1
	}
	assignments, err := parseProjectDefineEnv(envPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	assignmentValues := map[string]string{}
	for _, assignment := range assignments {
		assignmentValues[assignment.Name] = assignment.Value
	}
	consumed := map[string]bool{}
	migrations := []defineEnvMigration{}
	existingNames := map[string]bool{}
	for i, raw := range metadata.WordPress.Defines {
		item, _ := raw.(map[string]any)
		name := strings.TrimSpace(recordValueString(item["name"]))
		existingNames[name] = true
		collect := func(selector string, spec map[string]any) error {
			envName := strings.TrimSpace(recordValueString(spec["env"]))
			if envName == "" {
				return nil
			}
			value := assignmentValues[envName]
			if value != "" {
				consumed[envName] = true
			} else {
				value = envwizard.Value(envName)
			}
			if value == "" {
				location := fmt.Sprintf("nf.json wordpress.defines[%d] %s", i, name)
				if selector != "" {
					location += " selector " + selector
				}
				return ProjectError{Msg: fmt.Sprintf("Expected %s in project .env, the process environment, or the global nf config for %s.", envName, location)}
			}
			migrations = append(migrations, defineEnvMigration{Name: name, Selector: selector, EnvName: envName, Value: value, Spec: spec})
			return nil
		}
		if err := collect("", item); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		values, _ := item["values"].(map[string]any)
		selectors := make([]string, 0, len(values))
		for selector := range values {
			selectors = append(selectors, selector)
		}
		sort.Strings(selectors)
		for _, selector := range selectors {
			spec, _ := values[selector].(map[string]any)
			if err := collect(selector, spec); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		}
	}
	for _, assignment := range assignments {
		if consumed[assignment.Name] {
			continue
		}
		if existingNames[assignment.Name] {
			fmt.Fprintf(os.Stderr, "%s line %d for %s conflicts with an existing define that does not reference it\n", envPath, assignment.Line, assignment.Name)
			return 1
		}
		migrations = append(migrations, defineEnvMigration{Name: assignment.Name, EnvName: assignment.Name, Value: assignment.Value, NewDefine: true})
		existingNames[assignment.Name] = true
	}
	if len(migrations) == 0 {
		fmt.Println("No environment-backed defines or project .env assignments to migrate.")
		return 0
	}
	fmt.Println("Define environment migration:")
	for _, migration := range migrations {
		location := "all"
		if migration.Selector != "" {
			location = migration.Selector
		}
		fmt.Printf("  %s (%s) <- %s\n", migration.Name, location, migration.EnvName)
	}
	if dryRun {
		fmt.Println("No files were changed.")
		return 0
	}
	existingRefs, err := configuredDefineSecretRefs(metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	store, err := loadDefineSecretStore(root, metadata, len(existingRefs) == 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for i := range migrations {
		ref, err := generateDefineSecretRef(existingRefs)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		migrations[i].SecretRef = ref
		existingRefs[ref] = migrations[i].Name
		store.Secrets[ref] = migrations[i].Value
		if migrations[i].NewDefine {
			metadata.WordPress.Defines = append(metadata.WordPress.Defines, map[string]any{"name": migrations[i].Name, "secret": ref})
			continue
		}
		delete(migrations[i].Spec, "env")
		migrations[i].Spec["secret"] = ref
	}
	if err := validateConfiguredDefineMetadata(metadata); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
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
	verifiedMetadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if _, err := loadDefineSecretStore(root, verifiedMetadata, false); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if deleteSource && envExists {
		if err := os.Remove(envPath); err != nil {
			fmt.Fprintf(os.Stderr, "defines were migrated, but could not remove %s: %s\n", envPath, err)
			return 1
		}
		if err := syncDirectory(root); err != nil {
			fmt.Fprintf(os.Stderr, "defines were migrated, but could not sync removal of %s: %s\n", envPath, err)
			return 1
		}
	}
	fmt.Printf("Migrated %d values to %s.\n", len(migrations), defineSecretStoreFilename)
	return 0
}

func cmdDefineRekey(root string, metadata *projectMetadata, args []string) int {
	dryRun := false
	addRecipient := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dryRun = true
		case "--add-recipient":
			if addRecipient != "" || i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				fmt.Fprintln(os.Stderr, "define rekey --add-recipient requires exactly one age recipient")
				return 1
			}
			addRecipient = strings.TrimSpace(args[i+1])
			i++
		default:
			fmt.Fprintf(os.Stderr, "unknown define rekey flag: %s\n", args[i])
			return 1
		}
	}
	store, err := loadDefineSecretStore(root, metadata, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_, currentRecipient, err := currentDefineAgeIdentity()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if addRecipient != "" {
		if _, err := age.ParseX25519Recipient(addRecipient); err != nil {
			fmt.Fprintln(os.Stderr, "define rekey --add-recipient requires an age X25519 recipient")
			return 1
		}
		if !containsString(store.Recipients, addRecipient) {
			store.Recipients = append(store.Recipients, addRecipient)
		}
	} else {
		store.Recipients = []string{currentRecipient}
	}
	sort.Strings(store.Recipients)
	fmt.Printf("Rekey %s for %d recipient(s).\n", defineSecretStoreFilename, len(store.Recipients))
	if dryRun {
		fmt.Println("No files were changed.")
		return 0
	}
	if err := writeDefineSecretStore(root, store); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if _, err := loadDefineSecretStore(root, metadata, false); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Encrypted defines rekeyed.")
	return 0
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

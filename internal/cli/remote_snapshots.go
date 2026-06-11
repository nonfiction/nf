package cli

// Imported remote snapshot listing, selection, pruning, and import helpers.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/ui"
)

func remoteSnapshotMetadataFromFile(path string) (remoteSnapshotMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return remoteSnapshotMetadata{}, err
	}
	var meta remoteSnapshotMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return remoteSnapshotMetadata{}, err
	}
	return meta, nil
}

func loadRemoteSnapshots() ([]remoteSnapshotRecord, error) {
	dir := config.RemoteSnapshotsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	records := make([]remoteSnapshotRecord, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		snapshotDir := filepath.Join(dir, name)
		meta, err := remoteSnapshotMetadataFromFile(remoteSnapshotMetadataPath(snapshotDir))
		if err != nil {
			continue
		}
		if meta.Source != "" && meta.Source != "remote" {
			continue
		}
		record := remoteSnapshotRecord{
			Name:             name,
			Metadata:         meta,
			Directory:        snapshotDir,
			DatabaseArchive:  remoteSnapshotDatabaseArchive(snapshotDir),
			WpContentArchive: remoteSnapshotWpContentArchive(snapshotDir),
			DatabaseSize:     envSnapshotArchiveSize(remoteSnapshotDatabaseArchive(snapshotDir)),
			WpContentSize:    envSnapshotArchiveSize(remoteSnapshotWpContentArchive(snapshotDir)),
			CreatedAt:        envSnapshotCreatedAt(envSnapshotMetadata{CreatedAt: meta.CreatedAt}),
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		left := records[i]
		right := records[j]
		if !left.CreatedAt.IsZero() && !right.CreatedAt.IsZero() && !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.After(right.CreatedAt)
		}
		if left.Metadata.CreatedAt != right.Metadata.CreatedAt {
			return left.Metadata.CreatedAt > right.Metadata.CreatedAt
		}
		return left.Name < right.Name
	})
	return records, nil
}

func remoteSnapshotRows(records []remoteSnapshotRecord) [][]string {
	rows := [][]string{{"name", "created", "env", "database", "wp-content", "path"}}
	for _, record := range records {
		rows = append(rows, []string{
			record.Name,
			formatEnvSnapshotTime(record.Metadata.CreatedAt),
			record.Metadata.EnvID,
			formatEnvSnapshotSize(record.DatabaseSize),
			formatEnvSnapshotSize(record.WpContentSize),
			record.Directory,
		})
	}
	return rows
}

func remoteSnapshotSafeName(input string) (string, error) {
	name := strings.TrimSpace(input)
	if name == "" {
		return "", ProjectError{Msg: "remote snapshot name is required"}
	}
	if filepath.Clean(name) != name || strings.Contains(name, string(filepath.Separator)) || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return "", ProjectError{Msg: fmt.Sprintf("remote snapshot name %q must not contain path traversal", input)}
	}
	return name, nil
}

func chooseRemoteSnapshot(records []remoteSnapshotRecord, action string) (remoteSnapshotRecord, error) {
	options := make([]ui.SelectOption, 0, len(records))
	for _, record := range records {
		if record.Name == "" {
			continue
		}
		label := fmt.Sprintf("%s / %s / %s / %s", record.Name, record.Metadata.EnvID, formatEnvSnapshotSize(record.DatabaseSize), formatEnvSnapshotSize(record.WpContentSize))
		options = append(options, ui.SelectOption{Label: label, Value: record.Name})
	}
	if len(options) == 0 {
		return remoteSnapshotRecord{}, fmt.Errorf("No remote snapshots found.")
	}
	selected, err := envSnapshotSelect(fmt.Sprintf("Choose a remote snapshot to %s", action), options)
	if err != nil {
		return remoteSnapshotRecord{}, err
	}
	for _, record := range records {
		if record.Name == selected {
			return record, nil
		}
	}
	return remoteSnapshotRecord{}, fmt.Errorf("No remote snapshot matched %q.", selected)
}

func remoteSnapshotByName(records []remoteSnapshotRecord, name string) (remoteSnapshotRecord, bool) {
	for _, record := range records {
		if record.Name == name {
			return record, true
		}
	}
	return remoteSnapshotRecord{}, false
}

func remoteSnapshotCompletionNames() []string {
	records, err := loadRemoteSnapshots()
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(records))
	for _, record := range records {
		if record.Name != "" {
			values = append(values, record.Name)
		}
	}
	return uniqueSortedStrings(values)
}

func remoteSnapshotTotalSize(record remoteSnapshotRecord) int64 {
	total := int64(0)
	if record.DatabaseSize > 0 {
		total += record.DatabaseSize
	}
	if record.WpContentSize > 0 {
		total += record.WpContentSize
	}
	return total
}

func remoteSnapshotPrunePlan(records []remoteSnapshotRecord, keep int) []remoteSnapshotRecord {
	if keep < 0 {
		keep = 0
	}
	keptByEnv := map[string]int{}
	prune := make([]remoteSnapshotRecord, 0)
	for _, record := range records {
		envID := strings.TrimSpace(record.Metadata.EnvID)
		if envID == "" {
			envID = "unknown"
		}
		if keptByEnv[envID] < keep {
			keptByEnv[envID]++
			continue
		}
		prune = append(prune, record)
	}
	return prune
}

func importedRemoteSnapshotName(record remoteSnapshotRecord) string {
	base := strings.TrimSpace(record.Name)
	if base == "" {
		base = strings.TrimSpace(record.Metadata.EnvID)
	}
	if base == "" {
		base = "remote-snapshot"
	}
	var b strings.Builder
	lastDash := false
	for _, r := range base {
		allowed := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if allowed {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	name := strings.Trim(b.String(), "-_")
	if name == "" {
		name = "remote-snapshot"
	}
	return "remote-" + name
}

func createEnvSnapshotFromRemote(cfg envConfig, record remoteSnapshotRecord, localName string) (string, error) {
	if strings.TrimSpace(localName) == "" {
		localName = importedRemoteSnapshotName(record)
	}
	normalizedLocalName, err := envSnapshotNormalizedName(localName)
	if err != nil {
		return "", err
	}
	if envSnapshotExists(cfg, normalizedLocalName) {
		return "", fmt.Errorf("env snapshot %q already exists.", normalizedLocalName)
	}
	if err := copyFile(record.DatabaseArchive, envSnapshotHostDatabaseArchive(cfg, normalizedLocalName)); err != nil {
		return "", err
	}
	if err := copyFile(record.WpContentArchive, envSnapshotHostWpContentArchive(cfg, normalizedLocalName)); err != nil {
		return "", err
	}
	meta := newEnvSnapshotMetadata(cfg, normalizedLocalName, time.Now())
	meta.WordpressURL = firstNonEmpty(normalizeWordPressURL(record.Metadata.URL, true), meta.WordpressURL)
	jsonText, err := envSnapshotMetadataJSON(meta)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(envSnapshotMetadataPath(cfg, normalizedLocalName), []byte(jsonText), 0o644); err != nil {
		return "", err
	}
	return normalizedLocalName, nil
}

func selectRemoteSnapshot(remoteName string, nonInteractive bool, action string) (remoteSnapshotRecord, error) {
	records, err := loadRemoteSnapshots()
	if err != nil {
		return remoteSnapshotRecord{}, err
	}
	selectedName := strings.TrimSpace(remoteName)
	if selectedName == "" {
		if nonInteractive || !envSnapshotIsInteractive() {
			return remoteSnapshotRecord{}, ProjectError{Msg: fmt.Sprintf("env snapshot %s requires a remote snapshot name when stdin is not interactive", action)}
		}
		return chooseRemoteSnapshot(records, action)
	}
	normalized, err := remoteSnapshotSafeName(selectedName)
	if err != nil {
		return remoteSnapshotRecord{}, err
	}
	found, ok := remoteSnapshotByName(records, normalized)
	if !ok {
		return remoteSnapshotRecord{}, fmt.Errorf("No remote snapshot matched %q.", normalized)
	}
	return found, nil
}

func cmdEnvSnapshotImport(cfg envConfig, remoteName, localName string, nonInteractive bool) int {
	record, err := selectRemoteSnapshot(remoteName, nonInteractive, "import")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	normalizedLocalName, err := createEnvSnapshotFromRemote(cfg, record, localName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Remote snapshot imported.\n\nSnapshot:\n  project: %s\n  name: %s\n  remote: %s\n  env: %s\n  path: %s\n  database: database.sql.gz\n  wp-content: wp-content.tar.gz\n\nNext: nf env snapshot use %s\n", cfg.ProjectSlug, normalizedLocalName, record.Name, record.Metadata.EnvID, envSnapshotDir(cfg, normalizedLocalName), normalizedLocalName)
	return 0
}

func cmdSiteSnapshotList() int {
	records, err := loadRemoteSnapshots()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(records) == 0 {
		fmt.Println("No remote snapshots found.")
		return 0
	}
	fmt.Println(formatTable(remoteSnapshotRows(records)))
	return 0
}

func cmdSiteSnapshotRemove(name string, yes bool) int {
	records, err := loadRemoteSnapshots()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	selectedName := strings.TrimSpace(name)
	var record remoteSnapshotRecord
	if selectedName == "" {
		if !envSnapshotIsInteractive() {
			fmt.Fprintln(os.Stderr, "site snapshot remove requires a name when stdin is not interactive")
			return 1
		}
		selected, err := chooseRemoteSnapshot(records, "delete")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		record = selected
		selectedName = selected.Name
	} else {
		normalized, err := remoteSnapshotSafeName(selectedName)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		selectedName = normalized
		found, ok := remoteSnapshotByName(records, selectedName)
		if !ok {
			fmt.Fprintf(os.Stderr, "No remote snapshot matched %q.\n", selectedName)
			return 1
		}
		record = found
	}
	if !yes {
		if !envSnapshotIsInteractive() {
			fmt.Fprintln(os.Stderr, "site snapshot remove requires --yes when stdin is not interactive")
			return 1
		}
		confirmed, err := envSnapshotConfirm(fmt.Sprintf("Delete remote snapshot %q? This removes %s.", record.Name, record.Directory), false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	if err := os.RemoveAll(record.Directory); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Deleted remote snapshot.\n\nDeleted:\n  name: %s\n  env: %s\n  path: %s\n", record.Name, record.Metadata.EnvID, record.Directory)
	return 0
}

func cmdSiteSnapshotPrune(opts envSnapshotPruneOptions) int {
	if opts.keep < 0 {
		fmt.Fprintln(os.Stderr, "site snapshot prune --keep must be 0 or greater")
		return 1
	}
	records, err := loadRemoteSnapshots()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	prune := remoteSnapshotPrunePlan(records, opts.keep)
	if len(prune) == 0 {
		fmt.Printf("No remote snapshots to prune. Keeping %d newest snapshots per env.\n", opts.keep)
		return 0
	}
	total := int64(0)
	for _, record := range prune {
		total += remoteSnapshotTotalSize(record)
	}
	fmt.Printf("Remote snapshot prune plan:\n  keep newest per env: %d\n  delete snapshots:    %d\n  reclaim about:       %s\n\n", opts.keep, len(prune), formatEnvSnapshotSize(total))
	fmt.Println(formatTable(remoteSnapshotRows(prune)))
	if opts.dryRun {
		fmt.Println("\nNo remote snapshots were deleted. Re-run without --dry-run to prune.")
		return 0
	}
	if !opts.yes {
		if !envSnapshotIsInteractive() {
			fmt.Fprintln(os.Stderr, "site snapshot prune requires --yes when stdin is not interactive")
			return 1
		}
		confirmed, err := envSnapshotConfirm(fmt.Sprintf("Delete %d remote snapshots? This removes %s.", len(prune), formatEnvSnapshotSize(total)), false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	for _, record := range prune {
		if err := os.RemoveAll(record.Directory); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	fmt.Printf("\nDeleted %d remote snapshots. Reclaimed about %s.\n", len(prune), formatEnvSnapshotSize(total))
	return 0
}

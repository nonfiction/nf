package cli

// Local snapshot create, list, delete, prune, use, and restore commands.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nonfiction/nf/internal/ui"
)

func chooseEnvSnapshot(records []envSnapshotRecord, action string) (envSnapshotRecord, error) {
	options := make([]ui.SelectOption, 0, len(records))
	for _, record := range records {
		name := firstNonEmpty(record.Metadata.Name, filepath.Base(record.Directory))
		if name == "" {
			continue
		}
		label := fmt.Sprintf("%s / %s / %s / %s", name, formatEnvSnapshotTime(record.Metadata.CreatedAt), formatEnvSnapshotSize(record.DatabaseSize), formatEnvSnapshotSize(record.WpContentSize))
		options = append(options, ui.SelectOption{Label: label, Value: name})
	}
	if len(options) == 0 {
		return envSnapshotRecord{}, fmt.Errorf("No env snapshots found.")
	}
	selected, err := envSnapshotSelect(fmt.Sprintf("Choose an env snapshot to %s", action), options)
	if err != nil {
		return envSnapshotRecord{}, err
	}
	for _, record := range records {
		if firstNonEmpty(record.Metadata.Name, filepath.Base(record.Directory)) == selected {
			return record, nil
		}
	}
	return envSnapshotRecord{}, fmt.Errorf("env snapshot %q was not found", selected)
}

func cmdEnvSnapshotCreate(cfg envConfig, name string, nonInteractive bool) int {
	if strings.TrimSpace(name) == "" {
		if nonInteractive || !envSnapshotIsInteractive() {
			fmt.Fprintln(os.Stderr, "env snapshot add requires a name when stdin is not interactive")
			return 1
		}
		defaultName := defaultEnvSnapshotName(time.Now())
		prompted, err := envSnapshotPromptString("Snapshot name", defaultName, false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		name = prompted
	}
	normalized, err := envSnapshotNormalizedName(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if envSnapshotExists(cfg, normalized) {
		fmt.Fprintf(os.Stderr, "env snapshot %q already exists.\n", normalized)
		return 1
	}
	if err := ensureEnvReadyForSnapshot(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := envSnapshotCreateArchives(cfg, normalized); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	meta := newEnvSnapshotMetadata(cfg, normalized, time.Now())
	jsonText, err := envSnapshotMetadataJSON(meta)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(envSnapshotMetadataPath(cfg, normalized), []byte(jsonText), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Snapshot created.\n\nSnapshot:\n  project: %s\n  name: %s\n  path: %s\n  database: database.sql.gz\n  wp-content: wp-content.tar.gz\n", cfg.ProjectSlug, normalized, envSnapshotDir(cfg, normalized))
	return 0
}

func cmdEnvSnapshotList(cfg envConfig) int {
	records, err := loadEnvSnapshots(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(records) == 0 {
		fmt.Println("No env snapshots found.")
		return 0
	}
	fmt.Println(formatTable(envSnapshotRows(records)))
	return 0
}

func cmdEnvSnapshotDelete(cfg envConfig, name string, nonInteractive bool) int {
	records, err := loadEnvSnapshots(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	selectedName := strings.TrimSpace(name)
	if selectedName == "" {
		if nonInteractive || !envSnapshotIsInteractive() {
			fmt.Fprintln(os.Stderr, "env snapshot remove requires a name when stdin is not interactive")
			return 1
		}
		record, err := chooseEnvSnapshot(records, "delete")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		selectedName = firstNonEmpty(record.Metadata.Name, filepath.Base(record.Directory))
	}
	normalized, err := envSnapshotNormalizedName(selectedName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	path := envSnapshotDir(cfg, normalized)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "No env snapshot matched %q.\n", normalized)
			return 1
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if nonInteractive || !envSnapshotIsInteractive() {
		fmt.Fprintln(os.Stderr, "env snapshot remove requires an interactive terminal for confirmation")
		return 1
	}
	confirmed, err := envSnapshotConfirm(fmt.Sprintf("Delete env snapshot %q? This removes %s.", normalized, path), false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !confirmed {
		fmt.Fprintln(os.Stderr, "Aborted.")
		return 1
	}
	if err := os.RemoveAll(path); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Deleted env snapshot.\n\nDeleted:\n  name: %s\n  path: %s\n", normalized, path)
	return 0
}

func envSnapshotAutoPruneCandidate(name string) bool {
	return strings.HasPrefix(name, "pull-") || strings.HasPrefix(name, "push-") || strings.HasSuffix(name, "-pre-restore")
}

func envSnapshotTotalSize(record envSnapshotRecord) int64 {
	total := int64(0)
	if record.DatabaseSize > 0 {
		total += record.DatabaseSize
	}
	if record.WpContentSize > 0 {
		total += record.WpContentSize
	}
	return total
}

func envSnapshotPruneRows(records []envSnapshotRecord) [][]string {
	rows := [][]string{{"name", "created", "database", "wp-content", "path"}}
	for _, record := range records {
		rows = append(rows, []string{
			firstNonEmpty(record.Metadata.Name, filepath.Base(record.Directory)),
			formatEnvSnapshotTime(record.Metadata.CreatedAt),
			formatEnvSnapshotSize(record.DatabaseSize),
			formatEnvSnapshotSize(record.WpContentSize),
			record.Directory,
		})
	}
	return rows
}

func envSnapshotPrunePlan(records []envSnapshotRecord, keep int) []envSnapshotRecord {
	if keep < 0 {
		keep = 0
	}
	candidates := make([]envSnapshotRecord, 0)
	for _, record := range records {
		name := firstNonEmpty(record.Metadata.Name, filepath.Base(record.Directory))
		if envSnapshotAutoPruneCandidate(name) {
			candidates = append(candidates, record)
		}
	}
	if len(candidates) <= keep {
		return nil
	}
	return candidates[keep:]
}

func cmdEnvSnapshotPrune(cfg envConfig, opts envSnapshotPruneOptions) int {
	if opts.keep < 0 {
		fmt.Fprintln(os.Stderr, "env snapshot prune --keep must be 0 or greater")
		return 1
	}
	records, err := loadEnvSnapshots(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	prune := envSnapshotPrunePlan(records, opts.keep)
	if len(prune) == 0 {
		fmt.Printf("No env snapshots to prune. Keeping %d newest auto snapshots.\n", opts.keep)
		return 0
	}
	total := int64(0)
	for _, record := range prune {
		total += envSnapshotTotalSize(record)
	}
	fmt.Printf("Env snapshot prune plan:\n  keep newest auto snapshots: %d\n  delete snapshots:            %d\n  reclaim about:               %s\n\n", opts.keep, len(prune), formatEnvSnapshotSize(total))
	fmt.Println(formatTable(envSnapshotPruneRows(prune)))
	if opts.dryRun {
		fmt.Println("\nNo snapshots were deleted. Re-run without --dry-run to prune.")
		return 0
	}
	if !opts.yes {
		if !envSnapshotIsInteractive() {
			fmt.Fprintln(os.Stderr, "env snapshot prune requires --yes when stdin is not interactive")
			return 1
		}
		confirmed, err := envSnapshotConfirm(fmt.Sprintf("Delete %d auto env snapshots? This removes %s.", len(prune), formatEnvSnapshotSize(total)), false)
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
	fmt.Printf("\nDeleted %d env snapshots. Reclaimed about %s.\n", len(prune), formatEnvSnapshotSize(total))
	return 0
}

func parseEnvSnapshotPruneArgs(args []string) (envSnapshotPruneOptions, error) {
	opts := envSnapshotPruneOptions{keep: 3}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--dry-run":
			opts.dryRun = true
		case arg == "--yes":
			opts.yes = true
		case arg == "--keep":
			if i+1 >= len(args) {
				return opts, ProjectError{Msg: "env snapshot prune --keep requires a number"}
			}
			i++
			keep, err := strconv.Atoi(args[i])
			if err != nil || keep < 0 {
				return opts, ProjectError{Msg: "env snapshot prune --keep must be 0 or greater"}
			}
			opts.keep = keep
		case strings.HasPrefix(arg, "--keep="):
			keepText := strings.TrimPrefix(arg, "--keep=")
			keep, err := strconv.Atoi(keepText)
			if err != nil || keep < 0 {
				return opts, ProjectError{Msg: "env snapshot prune --keep must be 0 or greater"}
			}
			opts.keep = keep
		default:
			return opts, ProjectError{Msg: fmt.Sprintf("unsupported env snapshot prune option %q", arg)}
		}
	}
	return opts, nil
}

func parseEnvSnapshotImportArgs(args []string) (string, string, error) {
	remoteName := ""
	localName := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--name":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return "", "", ProjectError{Msg: "env snapshot import --name requires a name"}
			}
			i++
			localName = args[i]
		case strings.HasPrefix(arg, "--name="):
			localName = strings.TrimPrefix(arg, "--name=")
			if strings.TrimSpace(localName) == "" {
				return "", "", ProjectError{Msg: "env snapshot import --name requires a name"}
			}
		default:
			if strings.HasPrefix(arg, "-") {
				return "", "", ProjectError{Msg: fmt.Sprintf("unsupported env snapshot import option %q", arg)}
			}
			if remoteName != "" {
				return "", "", ProjectError{Msg: "env snapshot import takes at most one remote snapshot name"}
			}
			remoteName = arg
		}
	}
	return remoteName, localName, nil
}

func parseEnvSnapshotUseArgs(args []string) (envSnapshotUseOptions, error) {
	var opts envSnapshotUseOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--yes":
			opts.yes = true
		case arg == "--remote":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return opts, ProjectError{Msg: "env snapshot use --remote requires a remote snapshot name"}
			}
			i++
			opts.remoteName = args[i]
		case strings.HasPrefix(arg, "--remote="):
			opts.remoteName = strings.TrimPrefix(arg, "--remote=")
			if strings.TrimSpace(opts.remoteName) == "" {
				return opts, ProjectError{Msg: "env snapshot use --remote requires a remote snapshot name"}
			}
		case arg == "--name":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return opts, ProjectError{Msg: "env snapshot use --name requires a name"}
			}
			i++
			opts.localName = args[i]
		case strings.HasPrefix(arg, "--name="):
			opts.localName = strings.TrimPrefix(arg, "--name=")
			if strings.TrimSpace(opts.localName) == "" {
				return opts, ProjectError{Msg: "env snapshot use --name requires a name"}
			}
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, ProjectError{Msg: fmt.Sprintf("unsupported env snapshot use option %q", arg)}
			}
			if opts.name != "" {
				return opts, ProjectError{Msg: "env snapshot use takes at most one name"}
			}
			opts.name = arg
		}
	}
	if opts.remoteName != "" && opts.name != "" {
		return opts, ProjectError{Msg: "env snapshot use takes either a local snapshot name or --remote, not both"}
	}
	if opts.remoteName == "" && opts.localName != "" {
		return opts, ProjectError{Msg: "env snapshot use --name requires --remote"}
	}
	return opts, nil
}

func cmdEnvSnapshotUse(cfg envConfig, opts envSnapshotUseOptions, nonInteractive bool) int {
	name := opts.name
	if opts.remoteName != "" {
		record, err := selectRemoteSnapshot(opts.remoteName, nonInteractive, "restore")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		importedName, err := createEnvSnapshotFromRemote(cfg, record, opts.localName)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("Remote snapshot imported.\n\nSnapshot:\n  project: %s\n  name: %s\n  remote: %s\n  env: %s\n  path: %s\n\n", cfg.ProjectSlug, importedName, record.Name, record.Metadata.EnvID, envSnapshotDir(cfg, importedName))
		name = importedName
	}
	return cmdEnvSnapshotRestore(cfg, name, nonInteractive, opts.yes)
}

func cmdEnvSnapshotRestore(cfg envConfig, name string, nonInteractive bool, yes bool) int {
	records, err := loadEnvSnapshots(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	selectedName := strings.TrimSpace(name)
	if selectedName == "" {
		if nonInteractive || !envSnapshotIsInteractive() {
			fmt.Fprintln(os.Stderr, "env snapshot use requires a name when stdin is not interactive")
			return 1
		}
		record, err := chooseEnvSnapshot(records, "restore")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		selectedName = firstNonEmpty(record.Metadata.Name, filepath.Base(record.Directory))
	}
	normalized, err := envSnapshotNormalizedName(selectedName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	path := envSnapshotDir(cfg, normalized)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "No env snapshot matched %q.\n", normalized)
			return 1
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	sourceMeta, err := envSnapshotMetadataFromFile(envSnapshotMetadataPath(cfg, normalized))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !yes && (nonInteractive || !envSnapshotIsInteractive()) {
		fmt.Fprintln(os.Stderr, "env snapshot use requires an interactive terminal for confirmation")
		return 1
	}
	if !yes {
		confirmed, err := envSnapshotConfirm(fmt.Sprintf("Restore env snapshot %q? This will overwrite the current local env database and mutable wp-content.", normalized), false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	if err := ensureEnvReadyForSnapshot(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	safetyName := defaultPreRestoreSnapshotName(time.Now())
	if envSnapshotExists(cfg, safetyName) {
		fmt.Fprintf(os.Stderr, "env snapshot %q already exists.\n", safetyName)
		return 1
	}
	if err := envSnapshotCreateArchives(cfg, safetyName); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	safetyMeta := newEnvSnapshotMetadata(cfg, safetyName, time.Now())
	jsonText, err := envSnapshotMetadataJSON(safetyMeta)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(envSnapshotMetadataPath(cfg, safetyName), []byte(jsonText), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := envSnapshotRestoreArchives(cfg, normalized); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := envFinalizeLocalRestore(cfg, sourceMeta.WordpressURL); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Snapshot restored.\n\nRestored:\n  project: %s\n  name: %s\n\nSafety snapshot:\n  name: %s\n  path: %s\n", cfg.ProjectSlug, normalized, safetyName, envSnapshotDir(cfg, safetyName))
	return 0
}

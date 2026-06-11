package cli

// Remote env sync and remote command preflights.
//
// Pull, push, shell, wp, and site snapshot operations all resolve an explicit
// cached remote target before printing or executing a reviewable plan.

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nonfiction/nf/internal/state"
)

func resolveEnvRemoteSyncTarget(action, remoteName string, metadata map[string]any) (envRemoteSyncTarget, error) {
	remoteName = strings.TrimSpace(remoteName)
	if remoteName == "" {
		return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("env %s requires a non-empty remote", action)}
	}
	siteID, remoteEnv, ok, err := projectRemoteAlias(metadata, remoteName)
	if err != nil {
		return envRemoteSyncTarget{}, err
	}
	if !ok {
		return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("No remote named %q in nf.json remotes.", remoteName)}
	}
	record, _, err := cachedSiteEnv(siteID, remoteEnv)
	if err != nil {
		return envRemoteSyncTarget{}, err
	}
	if record == nil {
		return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("No cached remote env matched site %q env %q. Run nf site refresh after target cache is current, or update the local state cache.", siteID, remoteEnv)}
	}
	target, err := envRemoteSyncTargetFromSiteRecord(record, remoteName, siteID, remoteEnv)
	if err != nil {
		return envRemoteSyncTarget{}, err
	}
	return target, nil
}

func envRemoteSyncTargetFromSiteRecord(record map[string]any, remoteName, siteID, remoteEnv string) (envRemoteSyncTarget, error) {
	if err := validateSiteRecord(record); err != nil {
		return envRemoteSyncTarget{}, err
	}
	if siteID == "" {
		siteID = siteRecordID(record)
	}
	if remoteEnv == "" {
		remoteEnv = siteEnvName(record)
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(record["provider"])))
	target := envRemoteSyncTarget{Provider: provider, RemoteName: remoteName, SiteID: siteID, Env: remoteEnv, URL: firstRecordString(record, "url", "site_url", "home_url", "hostname"), TargetLabel: "target", TargetRef: siteProviderTarget(record), AccessLabel: "target record"}
	switch provider {
	case "kinsta":
		target.TargetLabel = ""
		target.TargetRef = ""
		target.AccessLabel = "environment ssh"
		target.SSHHost = firstNonEmpty(mapStringAtPath(record, "ssh", "host"), mapStringAtPath(record, "kinsta", "ssh", "host"), firstRecordString(record, "ssh_host"))
		if target.SSHHost == "" {
			return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("Kinsta env %q is missing SSH host in the site cache. Run nf site refresh.", siteSummary(record))}
		}
		target.SSHUser = firstNonEmpty(mapStringAtPath(record, "ssh", "user"), mapStringAtPath(record, "kinsta", "ssh", "user"), firstRecordString(record, "ssh_user", "ssh_username"))
		if target.SSHUser == "" {
			return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("Kinsta env %q is missing SSH user in the site cache. Run nf site refresh.", siteSummary(record))}
		}
		target.SSHPort = firstNonEmpty(mapStringAtPath(record, "ssh", "port"), mapStringAtPath(record, "kinsta", "ssh", "port"), firstRecordString(record, "ssh_port"), "22")
		target.WordPressPath = normalizeKinstaCachedPath(firstRecordString(record, "path"))
		if target.WordPressPath == "" {
			return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("Kinsta env %q is missing path in the site cache. Run nf site refresh.", siteSummary(record))}
		}
		target.AccessSummary = fmt.Sprintf("%s@%s", target.SSHUser, target.SSHHost)
		target.WPCommand = "wp"
	case "linode":
		resolved, err := cachedSiteTarget(target.TargetRef)
		if err != nil {
			return envRemoteSyncTarget{}, err
		}
		if resolved == nil {
			return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("Linode site %q references target %q, but no cached target matched. Run nf provider check linode.", siteSummary(record), target.TargetRef)}
		}
		target.AccessSummary = serverSummary(resolved)
		target.SSHUser = firstNonEmpty(mapStringAtPath(record, "ssh", "user"), firstRecordString(record, "ssh_user", "ssh_username"), serverSSHUser(resolved))
		if target.SSHUser == "" {
			values, err := loadGlobalConfig()
			if err != nil {
				return envRemoteSyncTarget{}, err
			}
			target.SSHUser = values["linode_default_user"]
		}
		if target.SSHUser == "" {
			return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("Target %q is missing an SSH user. Set linode_default_user with nf config set-linode-default-user <user>.", target.TargetRef)}
		}
		target.SSHHost = firstNonEmpty(mapStringAtPath(record, "ssh", "host"), firstRecordString(record, "ssh_host"), serverSSHHost(resolved), firstRecordString(record, "hostname"))
		if target.SSHHost == "" {
			return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("Site env %q is missing hostname.", siteSummary(record))}
		}
		target.SSHPort = firstNonEmpty(mapStringAtPath(record, "ssh", "port"), firstRecordString(record, "ssh_port"), mapStringAtPath(resolved, "ssh", "port"), firstRecordString(resolved, "ssh_port"), "22")
		target.WordPressPath = firstRecordString(record, "path")
		if target.WordPressPath == "" {
			return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("Site env %q is missing path.", siteSummary(record))}
		}
		target.WPCommand = "sudo -u www-data wp"
		target.SudoFileOps = true
	default:
		return envRemoteSyncTarget{}, ProjectError{Msg: fmt.Sprintf("Remote env sync is not implemented for provider %q; no data was changed.", provider)}
	}
	return target, nil
}

func cmdEnvRemoteSyncPlan(action, remoteName string, cfg envConfig, metadata map[string]any, opts envRemoteSyncOptions) int {
	if opts.execute && opts.dryRun {
		fmt.Fprintln(os.Stderr, "Choose either --execute or --dry-run, not both.")
		return 1
	}
	if strings.TrimSpace(remoteName) == "" {
		if opts.nonInteractive {
			fmt.Fprintf(os.Stderr, "env %s requires a remote in non-interactive mode\n", action)
			return 1
		}
		selected, err := chooseProjectRemote(action)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		remoteName = selected
	}
	target, err := resolveEnvRemoteSyncTarget(action, remoteName, metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Env %s preflight:\n", action)
	fmt.Printf("  local project: %s\n", cfg.ProjectSlug)
	fmt.Printf("  local env:     %s\n", localEnvDir(cfg))
	fmt.Printf("  remote:        %s\n", target.RemoteName)
	fmt.Printf("  site:          %s\n", target.SiteID)
	fmt.Printf("  env:           %s\n", target.Env)
	fmt.Printf("  provider:      %s\n", target.Provider)
	if target.TargetLabel != "" && target.TargetRef != "" {
		fmt.Printf("  %s:        %s\n", target.TargetLabel, target.TargetRef)
	}
	if target.URL != "" {
		fmt.Printf("  url:           %s\n", target.URL)
	}
	if target.AccessSummary != "" {
		fmt.Printf("  %s: %s\n", target.AccessLabel, target.AccessSummary)
	}
	willExecute := opts.execute || (!opts.dryRun && !opts.nonInteractive)
	mode := "execute"
	if !willExecute {
		mode = "dry-run"
	} else if opts.execute {
		mode = "execute"
	}
	fmt.Printf("  mode:          %s\n", mode)
	if !willExecute {
		fmt.Println("No data was changed. Re-run with --execute to sync database and mutable wp-content.")
		return 0
	}
	if target.Provider != "linode" && target.Provider != "kinsta" {
		fmt.Fprintf(os.Stderr, "Remote env sync execution is not implemented for provider %q; no data was changed.\n", target.Provider)
		return 1
	}
	if opts.nonInteractive && !opts.yes {
		fmt.Fprintln(os.Stderr, "Remote execution requires both --execute and --yes in non-interactive mode.")
		return 1
	}
	if !opts.yes {
		displayAction := action
		if displayAction != "" {
			displayAction = strings.ToUpper(displayAction[:1]) + displayAction[1:]
		}
		message := fmt.Sprintf("%s %s:%s %s local env %s? This syncs the database and mutable wp-content.", displayAction, target.SiteID, target.Env, map[string]string{"pull": "into", "push": "from"}[action], cfg.ProjectSlug)
		confirmed, err := envRemoteSyncConfirm(message, false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	if action == "pull" {
		return executeEnvPull(cfg, target)
	}
	return executeEnvPush(cfg, target)
}

func remoteSyncTempDir(cfg envConfig, target envRemoteSyncTarget, action string) string {
	return path.Join("/tmp", "nf-"+action+"-"+cleanEnvSlug(cfg.ProjectSlug)+"-"+strconv.FormatInt(time.Now().Unix(), 10))
}

func remoteFileOpPrefix(target envRemoteSyncTarget) string {
	if target.SudoFileOps {
		return "sudo "
	}
	return ""
}

func remoteExportScript(target envRemoteSyncTarget, remoteTmp string) string {
	fileOp := remoteFileOpPrefix(target)
	return fmt.Sprintf(`set -eu
rm -rf %s
mkdir -p %s
chmod 777 %s
cd %s
%s --path=%s db export %s/database.sql
%sgzip -f %s/database.sql
%schmod 644 %s/database.sql.gz
dirs=""
for dir in wp-content/uploads wp-content/plugins wp-content/mu-plugins wp-content/languages; do
  if [ -e %s/$dir ]; then dirs="$dirs $dir"; fi
done
if [ -n "$dirs" ]; then %star -C %s -czf %s/wp-content.tar.gz $dirs; else %star -C %s -czf %s/wp-content.tar.gz --files-from /dev/null; fi
%schmod 644 %s/wp-content.tar.gz
`, shellQuoteArg(remoteTmp), shellQuoteArg(remoteTmp), shellQuoteArg(remoteTmp), shellQuoteArg(target.WordPressPath), target.WPCommand, shellQuoteArg(target.WordPressPath), shellQuoteArg(remoteTmp), fileOp, shellQuoteArg(remoteTmp), fileOp, shellQuoteArg(remoteTmp), shellQuoteArg(target.WordPressPath), fileOp, shellQuoteArg(target.WordPressPath), shellQuoteArg(remoteTmp), fileOp, shellQuoteArg(target.WordPressPath), shellQuoteArg(remoteTmp), fileOp, shellQuoteArg(remoteTmp))
}

func remoteImportScript(target envRemoteSyncTarget, remoteTmp string) string {
	fileOp := remoteFileOpPrefix(target)
	chown := ""
	if target.SudoFileOps {
		chown = fmt.Sprintf("sudo chown -R www-data:www-data %s/wp-content/uploads %s/wp-content/plugins %s/wp-content/mu-plugins %s/wp-content/languages 2>/dev/null || true\n", shellQuoteArg(target.WordPressPath), shellQuoteArg(target.WordPressPath), shellQuoteArg(target.WordPressPath), shellQuoteArg(target.WordPressPath))
	}
	return fmt.Sprintf(`set -eu
tmp_sql=%s/database.sql
gzip -cd %s/database.sql.gz > "$tmp_sql"
%s --path=%s db import "$tmp_sql"
%srm -rf %s/wp-content/uploads %s/wp-content/plugins %s/wp-content/mu-plugins %s/wp-content/languages
%star -xzf %s/wp-content.tar.gz -C %s
%s`, shellQuoteArg(remoteTmp), shellQuoteArg(remoteTmp), target.WPCommand, shellQuoteArg(target.WordPressPath), fileOp, shellQuoteArg(target.WordPressPath), shellQuoteArg(target.WordPressPath), shellQuoteArg(target.WordPressPath), shellQuoteArg(target.WordPressPath), fileOp, shellQuoteArg(remoteTmp), shellQuoteArg(target.WordPressPath), chown)
}

func remoteWPSearchReplaceLine(target envRemoteSyncTarget, sourceURL, destinationURL string) string {
	sourceURL = normalizeWordPressURL(sourceURL, false)
	destinationURL = normalizeWordPressURL(destinationURL, true)
	if sourceURL == "" || destinationURL == "" || sourceURL == destinationURL {
		return ""
	}
	return fmt.Sprintf("%s --path=%s search-replace %s %s --all-tables-with-prefix --skip-columns=guid\n", target.WPCommand, shellQuoteArg(target.WordPressPath), shellQuoteArg(sourceURL), shellQuoteArg(destinationURL))
}

func remoteFinalizeImportScript(target envRemoteSyncTarget, themeSlug, sourceURL, destinationURL string) string {
	return fmt.Sprintf(`set -eu
%s%s --path=%s theme activate %s
%s --path=%s cache flush
`, remoteWPSearchReplaceLine(target, sourceURL, destinationURL), target.WPCommand, shellQuoteArg(target.WordPressPath), shellQuoteArg(themeSlug), target.WPCommand, shellQuoteArg(target.WordPressPath))
}

func remoteSSHArgs(target envRemoteSyncTarget, script string) []string {
	return []string{"ssh", "-p", target.SSHPort, target.SSHUser + "@" + target.SSHHost, script}
}

func remoteSnapshotMetadataJSON(target envRemoteSyncTarget, envID, outputDir string, now time.Time) string {
	data, _ := json.MarshalIndent(map[string]any{
		"schema":     1,
		"source":     "remote",
		"env_id":     envID,
		"site_id":    target.SiteID,
		"env":        target.Env,
		"provider":   target.Provider,
		"target":     target.TargetRef,
		"url":        target.URL,
		"created_at": now.Format(time.RFC3339),
		"path":       outputDir,
		"contents": map[string]any{
			"database":         "database.sql.gz",
			"wp_content":       "wp-content.tar.gz",
			"wp_content_paths": envSnapshotContentPaths(),
		},
	}, "", "  ")
	return string(append(data, '\n'))
}

func cmdSiteSnapshot(envRef string, opts siteSnapshotOptions) int {
	if strings.TrimSpace(envRef) == "" {
		selected, err := chooseSiteEnv("snapshot", "")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		envRef = selected
	}
	siteID, env, ok := splitSiteEnvRef(envRef)
	if !ok {
		fmt.Fprintln(os.Stderr, "site snapshot requires an env ref like site.target:env")
		return 1
	}
	record, _, err := cachedSiteEnv(siteID, env)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if record == nil {
		fmt.Fprintf(os.Stderr, "No cached remote env matched %q.\n", canonicalEnvID(siteID, env))
		return 1
	}
	target, err := envRemoteSyncTargetFromSiteRecord(record, canonicalEnvID(siteID, env), siteID, env)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if target.Provider != "linode" && target.Provider != "kinsta" {
		fmt.Fprintf(os.Stderr, "site snapshot is not implemented for provider %q; no data was changed.\n", target.Provider)
		return 1
	}
	envID := canonicalEnvID(siteID, env)
	outputDir := strings.TrimSpace(opts.output)
	now := time.Now()
	if outputDir == "" {
		outputDir = remoteSnapshotDir(envID, now)
	}
	absOutputDir, err := filepath.Abs(outputDir)
	if err == nil {
		outputDir = absOutputDir
	}
	remoteTmp := path.Join("/tmp", "nf-snapshot-"+cleanEnvSlug(envIDFileSlug(envID))+"-"+strconv.FormatInt(now.Unix(), 10))
	fmt.Println("Site snapshot plan:")
	fmt.Printf("  env:           %s\n", envID)
	fmt.Printf("  provider:      %s\n", target.Provider)
	if target.TargetLabel != "" && target.TargetRef != "" {
		fmt.Printf("  %s:        %s\n", target.TargetLabel, target.TargetRef)
	}
	if target.URL != "" {
		fmt.Printf("  url:           %s\n", target.URL)
	}
	if target.AccessSummary != "" {
		fmt.Printf("  %s: %s\n", target.AccessLabel, target.AccessSummary)
	}
	fmt.Printf("  output:        %s\n", outputDir)
	if opts.dryRun {
		fmt.Println("  mode:          dry-run")
		fmt.Println("No data was changed. Re-run without --dry-run to create a remote snapshot.")
		return 0
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := runSSHCommandFn(remoteSSHArgs(target, remoteExportScript(target, remoteTmp))); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := runRsyncCommandFn([]string{"rsync", "-az", "-e", "ssh -p " + target.SSHPort, target.SSHUser + "@" + target.SSHHost + ":" + remoteTmp + "/", outputDir + string(filepath.Separator)}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(outputDir, "snapshot.json"), []byte(remoteSnapshotMetadataJSON(target, envID, outputDir, now)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = runSSHCommandFn(remoteSSHArgs(target, "rm -rf "+shellQuoteArg(remoteTmp)))
	fmt.Printf("Site snapshot created.\n\nSnapshot:\n  source: remote\n  env: %s\n  path: %s\n  database: database.sql.gz\n  wp-content: wp-content.tar.gz\n", envID, outputDir)
	return 0
}

func executeEnvPull(cfg envConfig, target envRemoteSyncTarget) int {
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
	meta, _ := envSnapshotMetadataJSON(newEnvSnapshotMetadata(cfg, safetyName, time.Now()))
	if err := os.WriteFile(envSnapshotMetadataPath(cfg, safetyName), []byte(meta), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	name := "pull-" + target.RemoteName + "-" + defaultEnvSnapshotName(time.Now())
	if err := os.MkdirAll(envSnapshotDir(cfg, name), 0o777); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	remoteTmp := remoteSyncTempDir(cfg, target, "pull")
	if err := runSSHCommandFn(remoteSSHArgs(target, remoteExportScript(target, remoteTmp))); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := runRsyncCommandFn([]string{"rsync", "-az", "-e", "ssh -p " + target.SSHPort, target.SSHUser + "@" + target.SSHHost + ":" + remoteTmp + "/", envSnapshotDir(cfg, name) + string(filepath.Separator)}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	meta, _ = envSnapshotMetadataJSON(newEnvSnapshotMetadata(cfg, name, time.Now()))
	if err := os.WriteFile(envSnapshotMetadataPath(cfg, name), []byte(meta), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := envSnapshotRestoreArchives(cfg, name); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := envFinalizeLocalRestore(cfg, normalizeWordPressURL(target.URL, true)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Env pulled.\n\nRestored snapshot: %s\nSafety snapshot: %s\n", name, safetyName)
	return 0
}

func executeEnvPush(cfg envConfig, target envRemoteSyncTarget) int {
	if err := ensureEnvReadyForSnapshot(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	name := "push-" + target.RemoteName + "-" + defaultEnvSnapshotName(time.Now())
	if err := envSnapshotCreateArchives(cfg, name); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	meta, _ := envSnapshotMetadataJSON(newEnvSnapshotMetadata(cfg, name, time.Now()))
	if err := os.WriteFile(envSnapshotMetadataPath(cfg, name), []byte(meta), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	remoteTmp := remoteSyncTempDir(cfg, target, "push")
	if err := runSSHCommandFn(remoteSSHArgs(target, "rm -rf "+shellQuoteArg(remoteTmp)+" && mkdir -p "+shellQuoteArg(remoteTmp))); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := runRsyncCommandFn([]string{"rsync", "-az", "-e", "ssh -p " + target.SSHPort, envSnapshotDir(cfg, name) + string(filepath.Separator), target.SSHUser + "@" + target.SSHHost + ":" + remoteTmp + "/"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	backupTmp := remoteSyncTempDir(cfg, target, "backup")
	if err := runSSHCommandFn(remoteSSHArgs(target, remoteExportScript(target, backupTmp))); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := runSSHCommandFn(remoteSSHArgs(target, remoteImportScript(target, remoteTmp))); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := runSSHCommandFn(remoteSSHArgs(target, remoteFinalizeImportScript(target, firstNonEmpty(cfg.ThemeSlug, cfg.ProjectSlug, cfg.ThemeMountSlug, "theme"), envLocalWordPressURL(cfg), target.URL))); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Env pushed.\n\nLocal snapshot: %s\nRemote backup: %s\n", name, backupTmp)
	return 0
}

func cmdSiteRemoteCommandPlan(action, envRef string, args []string) int {
	siteID, env, ok := splitSiteEnvRef(envRef)
	if siteID == "" || env == "" {
		fmt.Fprintf(os.Stderr, "site %s requires an env ref like site.target:env\n", action)
		return 1
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "site %s requires an env ref like site.target:env\n", action)
		return 1
	}
	if action == "wp" {
		args = normalizePassthroughArgs(args)
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, "site wp requires an env ref and wp-cli command")
			return 1
		}
	}
	record, _, err := cachedSiteEnv(siteID, env)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if record == nil {
		fmt.Fprintf(os.Stderr, "No cached remote env matched %q.\n", canonicalEnvID(siteID, env))
		return 1
	}
	if err := validateSiteRecord(record); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(record["provider"])))
	fmt.Printf("Site %s preflight:\n", action)
	fmt.Printf("  site:     %s\n", siteID)
	fmt.Printf("  env:      %s\n", env)
	fmt.Printf("  provider: %s\n", provider)
	fmt.Printf("  target:   %s\n", siteProviderTarget(record))
	if url := firstRecordString(record, "url", "site_url", "home_url", "hostname"); url != "" {
		fmt.Printf("  url:      %s\n", url)
	}
	if provider != "linode" && provider != "kinsta" {
		if action == "wp" {
			fmt.Printf("  wp args:  %s\n", strings.Join(args, " "))
		}
		fmt.Fprintf(os.Stderr, "Remote site env %s is not implemented for provider %q; no command was run.\n", action, provider)
		return 1
	}
	var sshArgs []string
	if provider == "kinsta" {
		sshArgs, err = kinstaSiteEnvSSHArgs(record, action, args)
	} else {
		sshArgs, err = linodeSiteEnvSSHArgs(record, action, args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if action == "wp" {
		fmt.Printf("  wp args:  %s\n", strings.Join(args, " "))
	}
	printCommandArgs(sshArgs)
	if err := runSSHCommandFn(sshArgs); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func kinstaSiteEnvSSHArgs(record map[string]any, action string, wpArgs []string) ([]string, error) {
	host := firstNonEmpty(mapStringAtPath(record, "ssh", "host"), mapStringAtPath(record, "kinsta", "ssh", "host"), firstRecordString(record, "ssh_host"))
	if host == "" {
		return nil, ProjectError{Msg: fmt.Sprintf("Kinsta site env %q is missing SSH host. Run nf site refresh.", siteSummary(record))}
	}
	user := firstNonEmpty(mapStringAtPath(record, "ssh", "user"), mapStringAtPath(record, "kinsta", "ssh", "user"), firstRecordString(record, "ssh_user", "ssh_username"))
	if user == "" {
		return nil, ProjectError{Msg: fmt.Sprintf("Kinsta site env %q is missing SSH user. Run nf site refresh.", siteSummary(record))}
	}
	port := firstNonEmpty(mapStringAtPath(record, "ssh", "port"), mapStringAtPath(record, "kinsta", "ssh", "port"), firstRecordString(record, "ssh_port"), "22")
	path := firstRecordString(record, "path")
	if path == "" {
		return nil, ProjectError{Msg: fmt.Sprintf("Kinsta site env %q is missing path. Run nf site refresh.", siteSummary(record))}
	}
	path = normalizeKinstaCachedPath(path)
	destination := user + "@" + host
	switch action {
	case "shell":
		remoteCommand := "cd " + shellQuoteArg(path) + " && exec ${SHELL:-/bin/bash} -i"
		return []string{"ssh", "-t", "-p", port, destination, remoteCommand}, nil
	case "logs":
		remoteCommand := remoteDebugLogTailCommand(path)
		return []string{"ssh", "-p", port, destination, remoteCommand}, nil
	}
	if action != "wp" {
		return nil, ProjectError{Msg: fmt.Sprintf("unsupported remote site env command %q", action)}
	}
	sshArgs := []string{"ssh", "-p", port, destination}
	remoteCommand := "cd " + shellQuoteArg(path) + " && wp --path=" + shellQuoteArg(path)
	if normalized := normalizePassthroughArgs(wpArgs); len(normalized) > 0 {
		remoteCommand += " " + renderCommandArgs(normalized)
	}
	return append(sshArgs, remoteCommand), nil
}

func normalizeKinstaCachedPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	const duplicatedPublicRoot = "/public/www/"
	if index := strings.Index(value, duplicatedPublicRoot); index >= 0 {
		return path.Clean("/www/" + value[index+len(duplicatedPublicRoot):])
	}
	return path.Clean(value)
}

func linodeSiteEnvSSHArgs(record map[string]any, action string, wpArgs []string) ([]string, error) {
	targetRef := siteProviderTarget(record)
	targets, err := cachedTargets()
	if err != nil {
		return nil, err
	}
	target := state.MatchingRecord(targets, targetRef)
	if target == nil {
		return nil, ProjectError{Msg: fmt.Sprintf("Linode site %q references target %q, but no cached target matched. Run nf provider check linode.", siteSummary(record), targetRef)}
	}
	values, err := loadGlobalConfig()
	if err != nil {
		return nil, err
	}
	user := firstNonEmpty(mapStringAtPath(record, "ssh", "user"), firstRecordString(record, "ssh_user", "ssh_username"), serverSSHUser(target), values["linode_default_user"])
	if user == "" {
		return nil, ProjectError{Msg: fmt.Sprintf("Target %q is missing an SSH user. Set linode_default_user with nf config set-linode-default-user <user>.", targetRef)}
	}
	host := firstNonEmpty(mapStringAtPath(record, "ssh", "host"), firstRecordString(record, "ssh_host"), serverSSHHost(target), firstRecordString(record, "hostname"))
	if host == "" {
		return nil, ProjectError{Msg: fmt.Sprintf("Site env %q is missing hostname.", siteSummary(record))}
	}
	port := firstNonEmpty(mapStringAtPath(record, "ssh", "port"), firstRecordString(record, "ssh_port"), mapStringAtPath(target, "ssh", "port"), firstRecordString(target, "ssh_port"), "22")
	destination := user + "@" + host
	path := firstRecordString(record, "path")
	if path == "" {
		return nil, ProjectError{Msg: fmt.Sprintf("Site env %q is missing path.", siteSummary(record))}
	}
	switch action {
	case "shell":
		remoteCommand := "cd " + shellQuoteArg(path) + " && exec ${SHELL:-/bin/bash} -i"
		return []string{"ssh", "-t", "-p", port, destination, remoteCommand}, nil
	case "logs":
		remoteCommand := remoteDebugLogTailCommand(path)
		return []string{"ssh", "-p", port, destination, remoteCommand}, nil
	}
	if action != "wp" {
		return nil, ProjectError{Msg: fmt.Sprintf("unsupported remote site env command %q", action)}
	}
	sshArgs := []string{"ssh", "-p", port, destination}
	remoteCommand := "cd " + shellQuoteArg(path) + " && sudo -u www-data wp --path=" + shellQuoteArg(path)
	if normalized := normalizePassthroughArgs(wpArgs); len(normalized) > 0 {
		remoteCommand += " " + renderCommandArgs(normalized)
	}
	return append(sshArgs, remoteCommand), nil
}

func remoteDebugLogTailCommand(wordPressPath string) string {
	quotedPath := shellQuoteArg(wordPressPath)
	return "cd " + quotedPath + " && mkdir -p wp-content && touch wp-content/debug.log && tail -f wp-content/debug.log"
}

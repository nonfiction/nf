package cli

// Packaged theme deploy and rollback.
//
// Deploys upload immutable release artifacts, activate the selected release,
// and keep remote release metadata so rollback can restore the previous build.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nonfiction/nf/internal/state"
	"github.com/nonfiction/nf/internal/theme"
)

type themeDeployTarget struct {
	Provider       string
	RemoteName     string
	SiteID         string
	Env            string
	URL            string
	SSHUser        string
	SSHHost        string
	SSHPort        string
	WordPressPath  string
	RemoteThemeDir string
	WPCommand      string
	PHPVersion     string
}

type themeDeployArtifact struct {
	SourceDir   string
	OutputPath  string
	FileName    string
	FileCount   int
	ArchiveRoot string
	Version     string
	Checksum    string
	ReleaseID   string
}

const themeReleaseKeep = 5

func cmdThemeDeploy(remoteName string, dryRun bool) int {
	remoteName = strings.TrimSpace(remoteName)
	if remoteName == "" {
		fmt.Fprintln(os.Stderr, "theme deploy requires a non-empty remote")
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
	repoTheme, themeSource, themes, err := projectRepoTheme(root, metadata, "theme deploy", true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	themeSlug := repoTheme.Slug
	activeThemeSlug := firstNonEmpty(activeWordPressThemeSlug(themes), themeSlug)
	if err := validateThemeDeploySlug(themeSlug); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	target, err := resolveThemeDeployTarget(remoteName, themeSlug, metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	artifact, err := prepareThemeDeployArtifact(root, metadata, themeSource, themeSlug, dryRun)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	syncTarget := themeDeploySyncTarget(target)
	dependencyThemes := filterNonRepoThemes(themes)
	remoteDependencyThemes := []remoteThemeInstallSpec{}
	dependencyUploads := []remoteThemeUpload{}
	dependencyTmp := remoteThemeInstallTempDir(syncTarget)
	if len(dependencyThemes) > 0 {
		remoteDependencyThemes, dependencyUploads, err = remoteThemeInstallSpecs(root, dependencyThemes, dependencyTmp, !dryRun, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	releaseBase := path.Join(target.WordPressPath, "wp-content", "themes", ".nf-releases", themeSlug)
	releaseDir := path.Join(releaseBase, artifact.ReleaseID)
	remoteArtifact := path.Join(releaseBase, "_uploads", artifact.FileName)

	fmt.Println("Theme deploy plan:")
	fmt.Printf("  remote:      %s\n", target.RemoteName)
	fmt.Printf("  site:        %s\n", target.SiteID)
	fmt.Printf("  env:         %s\n", target.Env)
	fmt.Printf("  provider:    %s\n", target.Provider)
	if target.URL != "" {
		fmt.Printf("  url:         %s\n", target.URL)
	}
	fmt.Printf("  source:      %s\n", themeSource)
	fmt.Printf("  artifact:    %s\n", artifact.OutputPath)
	fmt.Printf("  release id:  %s\n", artifact.ReleaseID)
	fmt.Printf("  release dir: %s\n", releaseDir)
	fmt.Printf("  active dir:  %s\n", target.RemoteThemeDir)
	if activeThemeSlug != themeSlug {
		fmt.Printf("  activate:    %s\n", activeThemeSlug)
	}
	if len(dependencyThemes) > 0 {
		fmt.Printf("  dependencies: %d non-repo theme(s)\n", len(dependencyThemes))
	}
	fmt.Printf("  keep:        last %d releases\n", themeReleaseKeep)
	if dryRun {
		fmt.Println("  mode:        dry-run")
	}

	if dryRun {
		fmt.Printf("Would package %s -> %s (%d files)\n", artifact.SourceDir, artifact.OutputPath, artifact.FileCount)
	}
	mkdirArgs := themeDeployReleaseMkdirArgs(target, releaseBase)
	printCommandArgs(mkdirArgs)
	uploadArgs := themeDeployArtifactUploadArgs(artifact.OutputPath, target, remoteArtifact)
	printCommandArgs(uploadArgs)
	if len(remoteDependencyThemes) > 0 {
		fmt.Println("  dependency script: install configured non-repo themes")
		printRemoteThemeInstallCommand(syncTarget)
	}
	activateArgs := themeDeployActivateArgs(target, activeThemeSlug)
	releaseScript := themeDeployReleaseScript(target, themeSlug, activeThemeSlug, artifact, remoteArtifact, releaseBase, releaseDir, activateArgs[len(activateArgs)-1])
	releaseArgs := themeRemoteScriptCommandArgs(target, "nf-theme-deploy-release")
	fmt.Println("  remote script: extract release, switch active theme, refresh runtime mtimes, activate, record metadata, prune old releases")
	printCommandArgs(releaseArgs)
	rewriteArgs := remoteWPSSHArgs(syncTarget, wpRewriteFlushArgs()...)
	fmt.Println("  post-deploy: regenerate WordPress rewrite rules")
	printCommandArgs(rewriteArgs)
	if !dryRun {
		if len(dependencyUploads) > 0 {
			if err := uploadRemoteThemeSources(syncTarget, dependencyTmp, dependencyUploads); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		}
		if len(remoteDependencyThemes) > 0 {
			if err := runSSHCommandFn(remoteSSHArgs(syncTarget, remoteThemeInstallScript(syncTarget, remoteDependencyThemes, ""))); err != nil {
				fmt.Fprintln(os.Stderr, err)
				if len(dependencyUploads) > 0 {
					_ = runSSHCommandFn(remoteSSHArgs(syncTarget, "rm -rf "+shellQuoteArg(dependencyTmp)))
				}
				return 1
			}
			if len(dependencyUploads) > 0 {
				_ = runSSHCommandFn(remoteSSHArgs(syncTarget, "rm -rf "+shellQuoteArg(dependencyTmp)))
			}
		}
		if err := runSSHCommandFn(mkdirArgs); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := runRsyncCommandFn(uploadArgs); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := runSSHStdinCommandFn(releaseArgs, releaseScript); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := flushRemoteRewriteRules(syncTarget); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if dryRun {
		fmt.Println("No remote files were changed.")
	} else {
		fmt.Println("Theme release deployed.")
	}
	return 0
}

func cmdThemeRollback(remoteName string, dryRun bool) int {
	remoteName = strings.TrimSpace(remoteName)
	if remoteName == "" {
		fmt.Fprintln(os.Stderr, "theme rollback requires a non-empty remote")
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
	repoTheme, _, themes, err := projectRepoTheme(root, metadata, "theme rollback", false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	themeSlug := repoTheme.Slug
	activeThemeSlug := firstNonEmpty(activeWordPressThemeSlug(themes), themeSlug)
	if err := validateThemeDeploySlug(themeSlug); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	target, err := resolveThemeDeployTarget(remoteName, themeSlug, metadata)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	releaseBase := path.Join(target.WordPressPath, "wp-content", "themes", ".nf-releases", themeSlug)
	metadataFile := path.Join(releaseBase, "releases.json")
	rollbackScript := themeRollbackScript(target, themeSlug, activeThemeSlug, releaseBase, metadataFile)
	rollbackArgs := themeRemoteScriptCommandArgs(target, "nf-theme-rollback-release")
	syncTarget := themeDeploySyncTarget(target)
	rewriteArgs := remoteWPSSHArgs(syncTarget, wpRewriteFlushArgs()...)

	fmt.Println("Theme rollback plan:")
	fmt.Printf("  remote:      %s\n", target.RemoteName)
	fmt.Printf("  site:        %s\n", target.SiteID)
	fmt.Printf("  env:         %s\n", target.Env)
	fmt.Printf("  provider:    %s\n", target.Provider)
	if target.URL != "" {
		fmt.Printf("  url:         %s\n", target.URL)
	}
	fmt.Printf("  releases:    %s\n", metadataFile)
	fmt.Printf("  release dir: %s/<previous-release>\n", releaseBase)
	fmt.Printf("  active dir:  %s\n", target.RemoteThemeDir)
	if activeThemeSlug != themeSlug {
		fmt.Printf("  activate:    %s\n", activeThemeSlug)
	}
	if dryRun {
		fmt.Println("  mode:        dry-run")
	}
	fmt.Println("  remote script: select previous release, switch active theme, refresh runtime mtimes, activate, record rollback")
	printCommandArgs(rollbackArgs)
	fmt.Println("  post-rollback: regenerate WordPress rewrite rules")
	printCommandArgs(rewriteArgs)
	if !dryRun {
		if err := runSSHStdinCommandFn(rollbackArgs, rollbackScript); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := flushRemoteRewriteRules(syncTarget); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if dryRun {
		fmt.Println("No remote files were changed.")
	} else {
		fmt.Println("Theme release rolled back.")
	}
	return 0
}

func resolveThemeDeployTarget(remoteName, themeSlug string, metadata *projectMetadata) (themeDeployTarget, error) {
	siteID, remoteEnv, ok, err := projectRemoteAlias(metadata, remoteName)
	if err != nil {
		return themeDeployTarget{}, err
	}
	if !ok {
		return themeDeployTarget{}, ProjectError{Msg: fmt.Sprintf("No remote named %q in nf.json remotes.", remoteName)}
	}
	record, _, err := cachedSiteEnv(siteID, remoteEnv)
	if err != nil {
		return themeDeployTarget{}, err
	}
	if record == nil {
		return themeDeployTarget{}, ProjectError{Msg: fmt.Sprintf("No cached remote env matched site %q env %q. Run nf site refresh after target cache is current, or update the local state cache.", siteID, remoteEnv)}
	}
	if err := validateSiteRecord(record); err != nil {
		return themeDeployTarget{}, err
	}
	provider := strings.ToLower(strings.TrimSpace(recordValueString(record["provider"])))
	target := themeDeployTarget{Provider: provider, RemoteName: remoteName, SiteID: siteID, Env: remoteEnv, URL: firstRecordString(record, "url", "site_url", "home_url", "hostname"), PHPVersion: sitePHPVersion(record)}
	switch provider {
	case "kinsta":
		target.SSHHost = firstNonEmpty(mapStringAtPath(record, "ssh", "host"), mapStringAtPath(record, "kinsta", "ssh", "host"), firstRecordString(record, "ssh_host"))
		target.SSHUser = firstNonEmpty(mapStringAtPath(record, "ssh", "user"), mapStringAtPath(record, "kinsta", "ssh", "user"), firstRecordString(record, "ssh_user", "ssh_username"))
		target.SSHPort = firstNonEmpty(mapStringAtPath(record, "ssh", "port"), mapStringAtPath(record, "kinsta", "ssh", "port"), firstRecordString(record, "ssh_port"), "22")
		target.WordPressPath = normalizeKinstaCachedPath(firstRecordString(record, "path"))
		target.WPCommand = "wp"
	case "linode":
		sshUser, sshHost, sshPort, wpPath, err := linodeThemeDeploySSHInfo(record)
		if err != nil {
			return themeDeployTarget{}, err
		}
		target.SSHUser = sshUser
		target.SSHHost = sshHost
		target.SSHPort = sshPort
		target.WordPressPath = wpPath
		target.WPCommand = "sudo -u www-data wp"
	default:
		return themeDeployTarget{}, ProjectError{Msg: fmt.Sprintf("Theme deploy is not implemented for provider %q; no files were changed.", provider)}
	}
	if target.SSHHost == "" {
		return themeDeployTarget{}, ProjectError{Msg: fmt.Sprintf("Site env %q is missing SSH host. Run nf site refresh.", siteSummary(record))}
	}
	if target.SSHUser == "" {
		return themeDeployTarget{}, ProjectError{Msg: fmt.Sprintf("Site env %q is missing SSH user. Run nf site refresh.", siteSummary(record))}
	}
	if target.WordPressPath == "" {
		return themeDeployTarget{}, ProjectError{Msg: fmt.Sprintf("Site env %q is missing path. Run nf site refresh.", siteSummary(record))}
	}
	target.RemoteThemeDir = path.Join(target.WordPressPath, "wp-content", "themes", themeSlug)
	return target, nil
}

func themeDeploySyncTarget(target themeDeployTarget) envRemoteSyncTarget {
	return envRemoteSyncTarget{
		Provider:      target.Provider,
		RemoteName:    target.RemoteName,
		SiteID:        target.SiteID,
		Env:           target.Env,
		URL:           target.URL,
		SSHUser:       target.SSHUser,
		SSHHost:       target.SSHHost,
		SSHPort:       target.SSHPort,
		WordPressPath: target.WordPressPath,
		WPCommand:     target.WPCommand,
	}
}

func linodeThemeDeploySSHInfo(record map[string]any) (user, host, port, wpPath string, err error) {
	targetRef := siteProviderTarget(record)
	targets, err := cachedTargets()
	if err != nil {
		return "", "", "", "", err
	}
	target := state.MatchingRecord(targets, targetRef)
	if target == nil {
		return "", "", "", "", ProjectError{Msg: fmt.Sprintf("Linode site %q references target %q, but no cached target matched. Run nf provider check linode.", siteSummary(record), targetRef)}
	}
	values, err := loadGlobalConfig()
	if err != nil {
		return "", "", "", "", err
	}
	user = firstNonEmpty(mapStringAtPath(record, "ssh", "user"), firstRecordString(record, "ssh_user", "ssh_username"), serverSSHUser(target), values["linode_default_user"])
	if user == "" {
		return "", "", "", "", ProjectError{Msg: fmt.Sprintf("Target %q is missing an SSH user. Set linode_default_user with nf config set-linode-default-user <user>.", targetRef)}
	}
	host = firstNonEmpty(mapStringAtPath(record, "ssh", "host"), firstRecordString(record, "ssh_host"), serverSSHHost(target), firstRecordString(record, "hostname"))
	port = firstNonEmpty(mapStringAtPath(record, "ssh", "port"), firstRecordString(record, "ssh_port"), mapStringAtPath(target, "ssh", "port"), firstRecordString(target, "ssh_port"), "22")
	wpPath = firstRecordString(record, "path")
	return user, host, port, wpPath, nil
}

func prepareThemeDeployArtifact(root string, metadata *projectMetadata, sourceDir, themeSlug string, dryRun bool) (themeDeployArtifact, error) {
	projectSlug := firstNonEmpty(metadata.Project.Slug, "project")
	output, err := repoThemePackageOutput(metadata)
	if err != nil {
		return themeDeployArtifact{}, err
	}
	output = firstNonEmpty(output, filepath.ToSlash(filepath.Join("dist", projectSlug+"-v{version}.zip")))
	version, versionErr := readThemeVersion(sourceDir)
	if strings.Contains(output, "{version}") {
		if versionErr != nil {
			return themeDeployArtifact{}, versionErr
		}
		output = strings.ReplaceAll(output, "{version}", version)
	} else if versionErr != nil {
		version = ""
	}
	if !filepath.IsAbs(output) {
		if strings.HasSuffix(strings.ToLower(output), ".zip") {
			output = filepath.Join(root, output)
		} else {
			output = filepath.Join(root, output, projectSlug+".zip")
		}
	}
	themeSlug = firstNonEmpty(themeSlug, projectSlug, filepath.Base(filepath.Clean(sourceDir)), "theme")
	if err := validateThemeDeploySlug(themeSlug); err != nil {
		return themeDeployArtifact{}, err
	}
	result, err := theme.PackageTheme(sourceDir, output, themeSlug, dryRun)
	if err != nil {
		return themeDeployArtifact{}, err
	}
	checksum := ""
	if !dryRun {
		checksum, err = fileSHA256(output)
		if err != nil {
			return themeDeployArtifact{}, err
		}
	}
	releaseID := themeDeployReleaseID(version, root)
	return themeDeployArtifact{
		SourceDir:   result.SourceDir,
		OutputPath:  result.OutputPath,
		FileName:    filepath.Base(result.OutputPath),
		FileCount:   result.FileCount,
		ArchiveRoot: result.ArchiveRoot,
		Version:     version,
		Checksum:    checksum,
		ReleaseID:   releaseID,
	}, nil
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum), nil
}

func themeDeployReleaseID(version, root string) string {
	parts := make([]string, 0, 3)
	if clean := cleanReleaseIDPart(version); clean != "" {
		parts = append(parts, "v"+clean)
	}
	if sha := gitShortSHA(root); sha != "" {
		parts = append(parts, sha)
	}
	parts = append(parts, time.Now().UTC().Format("20060102T150405.000000000Z"))
	return strings.Join(parts, "-")
}

func cleanReleaseIDPart(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else if b.Len() > 0 {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-._")
}

func gitShortSHA(root string) string {
	cmd := exec.Command("git", "-C", root, "rev-parse", "--short=12", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func themeDeployReleaseMkdirArgs(target themeDeployTarget, releaseBase string) []string {
	remoteCommand := "mkdir -p " + shellQuoteArg(path.Join(releaseBase, "_uploads"))
	return []string{"ssh", "-p", target.SSHPort, target.SSHUser + "@" + target.SSHHost, remoteCommand}
}

func themeDeployArtifactUploadArgs(artifactPath string, target themeDeployTarget, remoteArtifact string) []string {
	return []string{"rsync", "-az", "-e", "ssh -p " + target.SSHPort, artifactPath, target.SSHUser + "@" + target.SSHHost + ":" + remoteArtifact}
}

func themeRemoteScriptCommandArgs(target themeDeployTarget, label string) []string {
	return []string{"ssh", "-p", target.SSHPort, target.SSHUser + "@" + target.SSHHost, "sh -s -- " + label}
}

func themeDeployActivateArgs(target themeDeployTarget, themeSlug string) []string {
	remoteCommand := remoteWPCommand(themeDeploySyncTarget(target), "theme", "activate", themeSlug, "--allow-root")
	return []string{"ssh", "-p", target.SSHPort, target.SSHUser + "@" + target.SSHHost, remoteCommand}
}

func themeRuntimeMtimeRefreshCommand() string {
	return `find "$active_dir" -type f \( -name '*.php' -o -name '*.twig' -o -name '*.json' -o -name '*.css' -o -name '*.js' -o -name '*.mjs' -o -name '*.map' \) -exec touch {} +`
}

func themeRuntimeOpcacheResetCommand() string {
	return `php -r 'if (function_exists("opcache_reset")) { @opcache_reset(); }'`
}

func themeRuntimeFPMReloadCommand(target themeDeployTarget) string {
	if target.Provider != "linode" {
		return "true"
	}
	service := safePHPFPMService(firstNonEmpty(target.PHPVersion, "8.3"))
	if service == "" {
		return "true"
	}
	quotedService := shellQuoteArg(service)
	return "if command -v systemctl >/dev/null 2>&1; then sudo -n systemctl reload " + quotedService + " >/dev/null 2>&1 || sudo -n systemctl restart " + quotedService + " >/dev/null 2>&1 || true; fi"
}

func safePHPFPMService(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return ""
	}
	for _, r := range version {
		if (r < '0' || r > '9') && r != '.' {
			return ""
		}
	}
	return "php" + version + "-fpm"
}

func themeDeployReleaseScript(target themeDeployTarget, themeSlug, activeThemeSlug string, artifact themeDeployArtifact, remoteArtifact, releaseBase, releaseDir, activateCommand string) string {
	activeDir := target.RemoteThemeDir
	metadata := map[string]any{
		"release_id":        artifact.ReleaseID,
		"theme_slug":        themeSlug,
		"active_theme_slug": firstNonEmpty(activeThemeSlug, themeSlug),
		"artifact_filename": artifact.FileName,
		"artifact_checksum": artifact.Checksum,
		"deployed_at":       time.Now().UTC().Format(time.RFC3339),
		"provider":          target.Provider,
		"site_id":           target.SiteID,
		"env":               target.Env,
		"remote":            target.RemoteName,
	}
	metadataJSON, _ := json.Marshal(metadata)
	metadataFile := path.Join(releaseBase, "releases.json")
	// Kinsta and Linode both support normal directories. Keep the public active
	// theme path as a real directory instead of a symlink, so WordPress and host
	// tooling do not need special symlink handling. The active copy happens only
	// after the artifact has been fully extracted into an immutable release dir.
	phpAppend := `$m=json_decode($argv[1], true); if ($argv[3] !== "") { $m["previous_active_path"]=$argv[3]; } $f=$argv[2]; $l=[]; if (is_file($f)) { $o=json_decode(file_get_contents($f), true); if (is_array($o)) { $l=$o; } } $l[]=$m; file_put_contents($f, json_encode($l, JSON_PRETTY_PRINT|JSON_UNESCAPED_SLASHES).PHP_EOL);`
	phpPrune := `$base=$argv[1]; $meta=$argv[2]; $keep=max(1,(int)$argv[3]); $ok=function($v){return is_string($v)&&preg_match('/^[A-Za-z0-9._-]+$/',$v);}; $rm=function($p) use (&$rm){ if (is_link($p)||is_file($p)) { @unlink($p); return; } if (is_dir($p)) { foreach (scandir($p) ?: [] as $c) { if ($c!=='.'&&$c!=='..') { $rm($p.'/'.$c); } } @rmdir($p); } }; $l=[]; if (is_file($meta)) { $o=json_decode(file_get_contents($meta), true); if (is_array($o)) { $l=$o; } } $ids=[]; $art=[]; for ($i=count($l)-1; $i>=0 && count($ids)<$keep; $i--) { $id=$l[$i]['release_id']??''; if (!$ok($id) || isset($ids[$id])) { continue; } $ids[$id]=true; $f=$l[$i]['artifact_filename']??''; if (is_string($f) && basename($f)===$f && $f!=='' && strpos($f,'/')===false && strpos($f,"\0")===false) { $art[$f]=true; } } foreach (glob($base.'/*', GLOB_ONLYDIR) ?: [] as $d) { $n=basename($d); if ($n==='_uploads' || str_starts_with($n,'.') || !$ok($n) || isset($ids[$n])) { continue; } $rm($d); } foreach (glob($base.'/_uploads/*') ?: [] as $f) { if (is_file($f) && !isset($art[basename($f)])) { @unlink($f); } } foreach (glob($base.'/.extract-*') ?: [] as $d) { $rm($d); } foreach (glob($base.'/*.tmp') ?: [] as $d) { $rm($d); }`
	script := strings.Join([]string{
		"set -e",
		"artifact=" + shellQuoteArg(remoteArtifact),
		"release_dir=" + shellQuoteArg(releaseDir),
		"release_tmp=" + shellQuoteArg(releaseDir+".tmp"),
		"extract_tmp=" + shellQuoteArg(path.Join(releaseBase, ".extract-"+artifact.ReleaseID)),
		"active_dir=" + shellQuoteArg(activeDir),
		"active_tmp=" + shellQuoteArg(activeDir+".nf-next-"+artifact.ReleaseID),
		"archive_root=" + shellQuoteArg(artifact.ArchiveRoot),
		"rm -rf \"$extract_tmp\" \"$release_tmp\" \"$active_tmp\"",
		"mkdir -p \"$extract_tmp\" \"$release_tmp\"",
		"unzip -q \"$artifact\" -d \"$extract_tmp\"",
		"content=\"$extract_tmp/$archive_root\"",
		"if [ ! -d \"$content\" ]; then content=\"$extract_tmp\"; fi",
		"cp -a \"$content\"/. \"$release_tmp\"/",
		"rm -rf \"$release_dir\"",
		"mv \"$release_tmp\" \"$release_dir\"",
		"cp -a \"$release_dir\" \"$active_tmp\"",
		"previous=",
		"if [ -L \"$active_dir\" ]; then previous=$(readlink \"$active_dir\" || true); elif [ -d \"$active_dir\" ]; then previous=\"$active_dir\"; fi",
		"old_active=\"$active_dir.nf-prev\"",
		"rm -rf \"$old_active\"",
		"if [ -e \"$active_dir\" ] || [ -L \"$active_dir\" ]; then mv \"$active_dir\" \"$old_active\"; fi",
		"if mv \"$active_tmp\" \"$active_dir\"; then :; else if [ -e \"$old_active\" ] || [ -L \"$old_active\" ]; then mv \"$old_active\" \"$active_dir\"; fi; exit 1; fi",
		themeRuntimeMtimeRefreshCommand(),
		"if " + activateCommand + "; then rm -rf \"$old_active\"; else rm -rf \"$active_dir\"; if [ -e \"$old_active\" ] || [ -L \"$old_active\" ]; then mv \"$old_active\" \"$active_dir\"; fi; exit 1; fi",
		themeRuntimeOpcacheResetCommand(),
		themeRuntimeFPMReloadCommand(target),
		"php -r " + shellQuoteArg(phpAppend) + " " + shellQuoteArg(string(metadataJSON)) + " " + shellQuoteArg(metadataFile) + " \"$previous\"",
		"php -r " + shellQuoteArg(phpPrune) + " " + shellQuoteArg(releaseBase) + " " + shellQuoteArg(metadataFile) + " " + strconv.Itoa(themeReleaseKeep),
		"rm -rf \"$extract_tmp\"",
	}, " && ")
	return script
}

func themeRollbackScript(target themeDeployTarget, themeSlug, activeThemeSlug, releaseBase, metadataFile string) string {
	activeDir := target.RemoteThemeDir
	activateArgs := themeDeployActivateArgs(target, firstNonEmpty(activeThemeSlug, themeSlug))
	activateCommand := activateArgs[len(activateArgs)-1]
	readCurrentPHP := `$f=$argv[1]; $ok=function($v){return is_string($v)&&preg_match('/^[A-Za-z0-9._-]+$/',$v);}; $l=json_decode(file_get_contents($f), true); if (!is_array($l) || count($l) < 1) { fwrite(STDERR, "No theme releases found.\n"); exit(2); } for ($i=count($l)-1; $i>=0; $i--) { if (!empty($l[$i]["release_id"]) && $ok($l[$i]["release_id"])) { echo $l[$i]["release_id"]; exit; } } fwrite(STDERR, "No current release found.\n"); exit(2);`
	readPreviousPHP := `$f=$argv[1]; $ok=function($v){return is_string($v)&&preg_match('/^[A-Za-z0-9._-]+$/',$v);}; $l=json_decode(file_get_contents($f), true); if (!is_array($l) || count($l) < 2) { fwrite(STDERR, "No previous theme release found.\n"); exit(2); } $current=""; for ($i=count($l)-1; $i>=0; $i--) { if (!empty($l[$i]["release_id"]) && $ok($l[$i]["release_id"])) { $current=$l[$i]["release_id"]; break; } } for ($i=count($l)-2; $i>=0; $i--) { if (!empty($l[$i]["release_id"]) && $ok($l[$i]["release_id"]) && $l[$i]["release_id"] !== $current) { echo $l[$i]["release_id"]; exit; } } fwrite(STDERR, "No previous theme release found.\n"); exit(2);`
	appendPHP := `$f=$argv[1]; $target=$argv[2]; $from=$argv[3]; $slug=$argv[4]; $active=$argv[5]; $provider=$argv[6]; $site=$argv[7]; $env=$argv[8]; $remote=$argv[9]; $m=["action"=>"rollback","release_id"=>$target,"theme_slug"=>$slug,"active_theme_slug"=>$active,"rolled_back_from"=>$from,"deployed_at"=>gmdate("c"),"provider"=>$provider,"site_id"=>$site,"env"=>$env,"remote"=>$remote]; $l=[]; if (is_file($f)) { $o=json_decode(file_get_contents($f), true); if (is_array($o)) { $l=$o; } } $l[]=$m; file_put_contents($f, json_encode($l, JSON_PRETTY_PRINT|JSON_UNESCAPED_SLASHES).PHP_EOL);`
	script := strings.Join([]string{
		"set -e",
		"release_base=" + shellQuoteArg(releaseBase),
		"metadata_file=" + shellQuoteArg(metadataFile),
		"active_dir=" + shellQuoteArg(activeDir),
		"target_release=$(php -r " + shellQuoteArg(readPreviousPHP) + " \"$metadata_file\")",
		"current_release=$(php -r " + shellQuoteArg(readCurrentPHP) + " \"$metadata_file\")",
		"release_dir=\"$release_base/$target_release\"",
		"active_tmp=\"$active_dir.nf-rollback-$target_release\"",
		"old_active=\"$active_dir.nf-prev\"",
		"[ -d \"$release_dir\" ] || { echo \"Release directory not found: $release_dir\" >&2; exit 2; }",
		"rm -rf \"$active_tmp\" \"$old_active\"",
		"cp -a \"$release_dir\" \"$active_tmp\"",
		"if [ -e \"$active_dir\" ] || [ -L \"$active_dir\" ]; then mv \"$active_dir\" \"$old_active\"; fi",
		"if mv \"$active_tmp\" \"$active_dir\"; then :; else if [ -e \"$old_active\" ] || [ -L \"$old_active\" ]; then mv \"$old_active\" \"$active_dir\"; fi; exit 1; fi",
		themeRuntimeMtimeRefreshCommand(),
		"if " + activateCommand + "; then rm -rf \"$old_active\"; else rm -rf \"$active_dir\"; if [ -e \"$old_active\" ] || [ -L \"$old_active\" ]; then mv \"$old_active\" \"$active_dir\"; fi; exit 1; fi",
		themeRuntimeOpcacheResetCommand(),
		themeRuntimeFPMReloadCommand(target),
		"php -r " + shellQuoteArg(appendPHP) + " \"$metadata_file\" \"$target_release\" \"$current_release\" " + shellQuoteArg(themeSlug) + " " + shellQuoteArg(firstNonEmpty(activeThemeSlug, themeSlug)) + " " + shellQuoteArg(target.Provider) + " " + shellQuoteArg(target.SiteID) + " " + shellQuoteArg(target.Env) + " " + shellQuoteArg(target.RemoteName),
	}, " && ")
	return script
}

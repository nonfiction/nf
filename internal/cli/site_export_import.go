package cli

// Full site handoff exports and external WordPress imports into local envs.

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nonfiction/nf/internal/config"
)

const siteExportSchema = 1

func defaultSiteExportDir(envID string, now time.Time) string {
	return config.ExportDir(envIDFileSlug(envID) + "-" + defaultEnvSnapshotName(now))
}

func siteExportManifestPath(dir string) string {
	return filepath.Join(dir, "manifest.json")
}

func siteExportDatabasePath(dir string) string {
	return filepath.Join(dir, "database.sql.gz")
}

func siteExportFilesDir(dir string) string {
	return filepath.Join(dir, "files")
}

func remoteSiteExportScript(target envRemoteSyncTarget, remoteTmp string) string {
	fileOp := remoteFileOpPrefix(target)
	remoteTmpArg := shellQuoteArg(remoteTmp)
	wordpressPathArg := shellQuoteArg(target.WordPressPath)
	filesArchive := ""
	if target.SudoFileOps {
		filesArchive = fmt.Sprintf("%star -C %s -czf %s/files.tar.gz .\n%schmod 644 %s/files.tar.gz\n", fileOp, wordpressPathArg, remoteTmpArg, fileOp, remoteTmpArg)
	}
	return fmt.Sprintf(`set -eu
rm -rf %s
mkdir -p %s
chmod 777 %s
cd %s
%s --path=%s config get table_prefix > %s/%s
%s --path=%s db export %s/database.sql
%sgzip -f %s/database.sql
%schmod 644 %s/%s
%schmod 644 %s/database.sql.gz
%s`, remoteTmpArg, remoteTmpArg, remoteTmpArg, wordpressPathArg, target.WPCommand, wordpressPathArg, remoteTmpArg, tablePrefixFilename, target.WPCommand, wordpressPathArg, remoteTmpArg, fileOp, remoteTmpArg, fileOp, remoteTmpArg, tablePrefixFilename, fileOp, remoteTmpArg, filesArchive)
}

func remoteRsyncSource(target envRemoteSyncTarget, remotePath string) string {
	return target.SSHUser + "@" + target.SSHHost + ":" + shellQuoteArg(remotePath)
}

func remoteRsyncSSH(target envRemoteSyncTarget) string {
	return "ssh -p " + target.SSHPort
}

func cmdSiteExport(envRef string, opts siteExportOptions) int {
	if strings.TrimSpace(envRef) == "" {
		selected, err := chooseSiteEnv("export", "")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		envRef = selected
	}
	siteID, env, ok := splitSiteEnvRef(envRef)
	if !ok {
		fmt.Fprintln(os.Stderr, "site export requires an env ref like site.target:env")
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
		fmt.Fprintf(os.Stderr, "site export is not implemented for provider %q; no data was changed.\n", target.Provider)
		return 1
	}
	envID := canonicalEnvID(siteID, env)
	now := time.Now()
	outputDir := strings.TrimSpace(opts.output)
	if outputDir == "" {
		outputDir = defaultSiteExportDir(envID, now)
	}
	if absOutputDir, err := filepath.Abs(outputDir); err == nil {
		outputDir = absOutputDir
	}
	remoteTmp := path.Join("/tmp", "nf-export-"+cleanEnvSlug(envIDFileSlug(envID))+"-"+fmt.Sprint(now.Unix()))

	fmt.Println("Site export plan:")
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
	fmt.Printf("  source:        %s:%s\n", target.AccessSummary, target.WordPressPath)
	fmt.Printf("  output:        %s\n", outputDir)
	fmt.Println("  includes:      full WordPress filesystem, database.sql.gz")
	if opts.dryRun {
		fmt.Println("  mode:          dry-run")
		fmt.Println("No data was changed. Re-run without --dry-run to create a site export.")
		return 0
	}
	if err := os.MkdirAll(siteExportFilesDir(outputDir), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := runSSHCommandFn(remoteSSHArgs(target, remoteSiteExportScript(target, remoteTmp))); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := runRsyncCommandFn([]string{"rsync", "-az", "-e", remoteRsyncSSH(target), remoteRsyncSource(target, path.Join(remoteTmp, "database.sql.gz")), outputDir + string(filepath.Separator)}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := runRsyncCommandFn([]string{"rsync", "-az", "-e", remoteRsyncSSH(target), remoteRsyncSource(target, path.Join(remoteTmp, tablePrefixFilename)), outputDir + string(filepath.Separator)}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if target.SudoFileOps {
		archivePath := filepath.Join(outputDir, "files.tar.gz")
		if err := runRsyncCommandFn([]string{"rsync", "-az", "-e", remoteRsyncSSH(target), remoteRsyncSource(target, path.Join(remoteTmp, "files.tar.gz")), archivePath}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := extractTarGzArchive(archivePath, siteExportFilesDir(outputDir)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		_ = os.Remove(archivePath)
	} else if err := runRsyncCommandFn([]string{"rsync", "-az", "-e", remoteRsyncSSH(target), remoteRsyncSource(target, strings.TrimRight(target.WordPressPath, "/")+"/"), siteExportFilesDir(outputDir) + string(filepath.Separator)}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	tablePrefix, found, err := readWordPressTablePrefixFile(filepath.Join(outputDir, tablePrefixFilename))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !found {
		fmt.Fprintln(os.Stderr, "site export did not include a WordPress table prefix")
		return 1
	}
	manifest := siteExportManifest{Schema: siteExportSchema, Source: "remote-site-export", EnvID: envID, SiteID: siteID, Env: env, Provider: target.Provider, Target: target.TargetRef, URL: target.URL, CreatedAt: now.Format(time.RFC3339), WordPressPath: target.WordPressPath, Files: "files", Database: "database.sql.gz", TablePrefix: tablePrefix}
	if err := writeSiteExportManifest(outputDir, manifest); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := writeSiteExportReadme(outputDir, manifest); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = runSSHCommandFn(remoteSSHArgs(target, "rm -rf "+shellQuoteArg(remoteTmp)))
	fmt.Printf("Site export created.\n\nExport:\n  source: remote\n  env: %s\n  path: %s\n  files: files/\n  database: database.sql.gz\n", envID, outputDir)
	return 0
}

func writeSiteExportManifest(outputDir string, manifest siteExportManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(siteExportManifestPath(outputDir), append(data, '\n'), 0o644)
}

func writeSiteExportReadme(outputDir string, manifest siteExportManifest) error {
	text := fmt.Sprintf(`WordPress site export

Source env: %s
Source URL: %s
WordPress table prefix: %s

Contents:
- files/ contains the full WordPress filesystem from the source document root.
- database.sql.gz contains a compressed WordPress database dump.

Restore outline:
1. Copy files/ to the destination document root.
2. Create a destination database and import database.sql.gz.
3. Update wp-config.php for the destination database credentials.
4. Update URLs if the destination URL differs from the source URL.

Security note: files/wp-config.php may contain source server credentials and salts.
`, manifest.EnvID, firstNonEmpty(manifest.URL, "unknown"), firstNonEmpty(manifest.TablePrefix, "unknown"))
	return os.WriteFile(filepath.Join(outputDir, "README.txt"), []byte(text), 0o644)
}

func extractTarGzArchive(archivePath, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(header.Name)
		if name == "." || name == string(filepath.Separator) {
			continue
		}
		if filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(filepath.Separator)) || name == ".." {
			return fmt.Errorf("archive contains unsafe path %q", header.Name)
		}
		destinationPath := filepath.Join(destination, name)
		if !strings.HasPrefix(destinationPath, filepath.Clean(destination)+string(filepath.Separator)) && filepath.Clean(destinationPath) != filepath.Clean(destination) {
			return fmt.Errorf("archive contains unsafe path %q", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destinationPath, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
				return err
			}
			output, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode)&0o777)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(output, reader)
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink, tar.TypeLink:
			continue
		}
	}
}

func parseEnvImportArgs(args []string) (envImportOptions, error) {
	args = normalizeLongFlagValues(args, "--db", "--source-url", "--table-prefix", "--name")
	var opts envImportOptions
	positionals := make([]string, 0, 1)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--dry-run":
			opts.dryRun = true
		case arg == "--yes":
			opts.yes = true
		case arg == "--db":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return opts, ProjectError{Msg: "env import --db requires a path"}
			}
			i++
			opts.database = args[i]
		case arg == "--source-url":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return opts, ProjectError{Msg: "env import --source-url requires a URL"}
			}
			i++
			opts.sourceURL = args[i]
		case arg == "--table-prefix":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return opts, ProjectError{Msg: "env import --table-prefix requires a value"}
			}
			i++
			prefix, err := normalizeWordPressTablePrefix(args[i])
			if err != nil {
				return opts, err
			}
			opts.tablePrefix = prefix
		case arg == "--name":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return opts, ProjectError{Msg: "env import --name requires a name"}
			}
			i++
			opts.name = args[i]
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, ProjectError{Msg: fmt.Sprintf("unsupported env import option %q", arg)}
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 1 {
		return opts, ProjectError{Msg: "env import requires exactly one source path"}
	}
	opts.source = positionals[0]
	return opts, nil
}

type envImportSource struct {
	InputPath   string
	FilesPath   string
	Database    string
	SourceURL   string
	TablePrefix string
	Description string
}

func resolveEnvImportSource(opts envImportOptions) (envImportSource, error) {
	inputPath, err := filepath.Abs(opts.source)
	if err != nil {
		return envImportSource{}, err
	}
	info, err := os.Stat(inputPath)
	if err != nil {
		return envImportSource{}, err
	}
	if !info.IsDir() {
		return envImportSource{}, ProjectError{Msg: "env import source must be a directory"}
	}
	source := envImportSource{InputPath: inputPath, FilesPath: inputPath, Database: opts.database, SourceURL: normalizeWordPressURL(opts.sourceURL, true), TablePrefix: opts.tablePrefix, Description: "WordPress filesystem directory"}
	manifestPath := siteExportManifestPath(inputPath)
	if data, err := os.ReadFile(manifestPath); err == nil {
		var manifest siteExportManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return envImportSource{}, err
		}
		if strings.TrimSpace(manifest.Files) != "" {
			source.FilesPath = filepath.Join(inputPath, manifest.Files)
		}
		if strings.TrimSpace(manifest.Database) != "" && strings.TrimSpace(source.Database) == "" {
			source.Database = filepath.Join(inputPath, manifest.Database)
		}
		if source.SourceURL == "" {
			source.SourceURL = normalizeWordPressURL(manifest.URL, true)
		}
		if source.TablePrefix == "" && manifest.TablePrefix != "" {
			prefix, err := normalizeWordPressTablePrefix(manifest.TablePrefix)
			if err != nil {
				return envImportSource{}, err
			}
			source.TablePrefix = prefix
		}
		source.Description = "nf site export"
	} else if strings.TrimSpace(source.Database) == "" {
		source.Database = envImportDatabasePath(inputPath)
	}
	if source.TablePrefix == "" {
		prefix, found, err := readWordPressTablePrefixFile(filepath.Join(inputPath, tablePrefixFilename))
		if err != nil {
			return envImportSource{}, err
		}
		if found {
			source.TablePrefix = prefix
		}
	}
	if strings.TrimSpace(source.Database) == "" {
		return envImportSource{}, ProjectError{Msg: "env import requires --db unless the source contains manifest.json or a .sql/.sql.gz file"}
	}
	if !filepath.IsAbs(source.Database) {
		source.Database = filepath.Join(inputPath, source.Database)
	}
	if _, err := os.Stat(source.FilesPath); err != nil {
		return envImportSource{}, err
	}
	if _, err := os.Stat(source.Database); err != nil {
		return envImportSource{}, err
	}
	return source, nil
}

func envImportDatabasePath(inputPath string) string {
	for _, candidate := range []string{"database.sql.gz", "database.sql"} {
		candidatePath := filepath.Join(inputPath, candidate)
		if _, err := os.Stat(candidatePath); err == nil {
			return candidatePath
		}
	}
	for _, pattern := range []string{"*.sql.gz", "*.sql"} {
		matches, err := filepath.Glob(filepath.Join(inputPath, pattern))
		if err != nil || len(matches) == 0 {
			continue
		}
		sort.Strings(matches)
		return matches[0]
	}
	return ""
}

func cmdEnvImport(cfg envConfig, opts envImportOptions) int {
	source, err := resolveEnvImportSource(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	now := time.Now()
	name := strings.TrimSpace(opts.name)
	if name == "" {
		base := filepath.Base(source.InputPath)
		if base == "." || base == string(filepath.Separator) || base == "" {
			base = "wordpress"
		}
		name = "import-" + base + "-" + defaultEnvSnapshotName(now)
	}
	normalized, err := envSnapshotNormalizedName(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Env import plan:")
	fmt.Printf("  project:       %s\n", cfg.ProjectSlug)
	fmt.Printf("  source:        %s\n", source.InputPath)
	fmt.Printf("  source type:   %s\n", source.Description)
	fmt.Printf("  files:         %s\n", source.FilesPath)
	fmt.Printf("  database:      %s\n", source.Database)
	if source.SourceURL != "" {
		fmt.Printf("  source url:    %s\n", source.SourceURL)
	}
	if source.TablePrefix != "" {
		fmt.Printf("  table prefix:  %s\n", source.TablePrefix)
	}
	fmt.Printf("  local url:     %s\n", envLocalWordPressURL(cfg))
	fmt.Printf("  snapshot:      %s\n", normalized)
	fmt.Println("  imports:       database, wp-content/uploads, plugins, languages")
	fmt.Println("  skips:         target-specific wp-content/mu-plugins")
	if opts.dryRun {
		fmt.Println("  mode:          dry-run")
		fmt.Println("No data was changed. Re-run without --dry-run to import into the local env.")
		return 0
	}
	if envSnapshotExists(cfg, normalized) {
		fmt.Fprintf(os.Stderr, "env snapshot %q already exists.\n", normalized)
		return 1
	}
	if !opts.yes {
		if !envSnapshotIsInteractive() {
			fmt.Fprintln(os.Stderr, "env import requires --yes when stdin is not interactive")
			return 1
		}
		confirmed, err := envSnapshotConfirm(fmt.Sprintf("Import %q into local env %q? This will overwrite the current local env database and mutable wp-content.", normalized, cfg.ProjectSlug), false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return 1
		}
	}
	if err := createEnvImportSnapshot(cfg, normalized, source, now); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
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
	safetyMeta, err := envSnapshotMetadataJSON(newEnvSnapshotMetadata(cfg, safetyName, time.Now()))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(envSnapshotMetadataPath(cfg, safetyName), []byte(safetyMeta), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := envSnapshotRestoreArchives(cfg, normalized); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := envFinalizeLocalRestore(cfg, source.SourceURL); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("WordPress import restored.\n\nImported snapshot:\n  project: %s\n  name: %s\n  path: %s\n\nSafety snapshot:\n  name: %s\n  path: %s\n", cfg.ProjectSlug, normalized, envSnapshotDir(cfg, normalized), safetyName, envSnapshotDir(cfg, safetyName))
	return 0
}

func createEnvImportSnapshot(cfg envConfig, name string, source envImportSource, createdAt time.Time) error {
	if err := os.MkdirAll(envSnapshotDir(cfg, name), 0o755); err != nil {
		return err
	}
	if err := copyOrGzipDatabase(source.Database, envSnapshotHostDatabaseArchive(cfg, name)); err != nil {
		return err
	}
	if err := createWpContentImportArchive(source.FilesPath, envSnapshotHostWpContentArchive(cfg, name)); err != nil {
		return err
	}
	meta := newEnvSnapshotMetadata(cfg, name, createdAt)
	meta.WordpressURL = source.SourceURL
	jsonText, err := envSnapshotMetadataJSON(meta)
	if err != nil {
		return err
	}
	if err := os.WriteFile(envSnapshotMetadataPath(cfg, name), []byte(jsonText), 0o644); err != nil {
		return err
	}
	if source.TablePrefix != "" {
		return writeWordPressTablePrefixFile(envSnapshotHostTablePrefixPath(cfg, name), source.TablePrefix)
	}
	return nil
}

func copyOrGzipDatabase(sourcePath, destinationPath string) error {
	if strings.HasSuffix(strings.ToLower(sourcePath), ".gz") {
		return copyFile(sourcePath, destinationPath)
	}
	input, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		return err
	}
	output, err := os.Create(destinationPath)
	if err != nil {
		return err
	}
	defer output.Close()
	gz := gzip.NewWriter(output)
	if _, err := io.Copy(gz, input); err != nil {
		_ = gz.Close()
		return err
	}
	return gz.Close()
}

func createWpContentImportArchive(filesPath, destinationPath string) error {
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		return err
	}
	output, err := os.Create(destinationPath)
	if err != nil {
		return err
	}
	defer output.Close()
	gz := gzip.NewWriter(output)
	defer gz.Close()
	writer := tar.NewWriter(gz)
	defer writer.Close()
	for _, dir := range []string{"uploads", "plugins", "languages"} {
		sourceDir := envImportSourceDir(filesPath, dir)
		if _, err := os.Stat(sourceDir); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := addPathToTar(writer, sourceDir, filepath.ToSlash(filepath.Join("wp-content", dir))); err != nil {
			return err
		}
	}
	return nil
}

func envImportSourceDir(filesPath, dir string) string {
	wpContentDir := filepath.Join(filesPath, "wp-content", dir)
	if dir != "uploads" {
		return wpContentDir
	}
	if _, err := os.Stat(wpContentDir); err == nil {
		return wpContentDir
	}
	if filepath.Base(filepath.Clean(filesPath)) == "uploads" {
		return filesPath
	}
	return filepath.Join(filesPath, "uploads")
}

func addPathToTar(writer *tar.Writer, sourcePath, archiveRoot string) error {
	return filepath.WalkDir(sourcePath, func(currentPath string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(sourcePath, currentPath)
		if err != nil {
			return err
		}
		name := archiveRoot
		if rel != "." {
			name = filepath.ToSlash(filepath.Join(archiveRoot, rel))
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = name
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		input, err := os.Open(currentPath)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

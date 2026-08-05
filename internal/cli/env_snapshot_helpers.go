package cli

// Snapshot path, metadata, slug, and formatting helpers.

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/nonfiction/nf/internal/config"
)

const (
	defaultWordPressTablePrefix = "wp_"
	tablePrefixFilename         = "table-prefix.txt"
)

func defaultEnvSnapshotName(now time.Time) string {
	return now.Format("2006-01-02-150405")
}

func defaultPreRestoreSnapshotName(now time.Time) string {
	return defaultEnvSnapshotName(now) + "-pre-restore"
}

func envSnapshotProjectDir(cfg envConfig) string {
	return config.SnapshotProjectDir(cfg.ProjectSlug)
}

func remoteSnapshotDir(envID string, now time.Time) string {
	return config.RemoteSnapshotDir(envIDFileSlug(envID) + "-" + defaultEnvSnapshotName(now))
}

func remoteSnapshotMetadataPath(dir string) string {
	return filepath.Join(dir, "snapshot.json")
}

func remoteSnapshotDatabaseArchive(dir string) string {
	return filepath.Join(dir, "database.sql.gz")
}

func remoteSnapshotWpContentArchive(dir string) string {
	return filepath.Join(dir, "wp-content.tar.gz")
}

func remoteSnapshotTablePrefixPath(dir string) string {
	return filepath.Join(dir, tablePrefixFilename)
}

func envSnapshotDir(cfg envConfig, name string) string {
	return config.SnapshotDir(cfg.ProjectSlug, name)
}

func envSnapshotContainerDir(name string) string {
	return path.Join("/env-snapshots", name)
}

func envSnapshotContainerDatabaseArchive(name string) string {
	return path.Join(envSnapshotContainerDir(name), "database.sql.gz")
}

func envSnapshotContainerWpContentArchive(name string) string {
	return path.Join(envSnapshotContainerDir(name), "wp-content.tar.gz")
}

func envSnapshotContainerWpContentTransferArchive(name string) string {
	return path.Join(envSnapshotContainerDir(name), ".wp-content-transfer.tar.gz")
}

func envSnapshotContainerTablePrefixPath(name string) string {
	return path.Join(envSnapshotContainerDir(name), tablePrefixFilename)
}

func envSnapshotHostDatabaseArchive(cfg envConfig, name string) string {
	return filepath.Join(envSnapshotDir(cfg, name), "database.sql.gz")
}

func envSnapshotHostWpContentArchive(cfg envConfig, name string) string {
	return filepath.Join(envSnapshotDir(cfg, name), "wp-content.tar.gz")
}

func envSnapshotHostWpContentTransferArchive(cfg envConfig, name string) string {
	return filepath.Join(envSnapshotDir(cfg, name), ".wp-content-transfer.tar.gz")
}

func envSnapshotHostTablePrefixPath(cfg envConfig, name string) string {
	return filepath.Join(envSnapshotDir(cfg, name), tablePrefixFilename)
}

func envSnapshotMetadataPath(cfg envConfig, name string) string {
	return filepath.Join(envSnapshotDir(cfg, name), "snapshot.json")
}

func normalizeWordPressTablePrefix(value string) (string, error) {
	prefix := strings.TrimSpace(value)
	if prefix == "" {
		return "", ProjectError{Msg: "WordPress table prefix cannot be empty"}
	}
	for _, r := range prefix {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			continue
		}
		return "", ProjectError{Msg: fmt.Sprintf("invalid WordPress table prefix %q; use only letters, numbers, and underscores", prefix)}
	}
	return prefix, nil
}

func readWordPressTablePrefixFile(filePath string) (string, bool, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	prefix, err := normalizeWordPressTablePrefix(string(data))
	if err != nil {
		return "", true, err
	}
	return prefix, true, nil
}

func writeWordPressTablePrefixFile(filePath, value string) error {
	prefix, err := normalizeWordPressTablePrefix(value)
	if err != nil {
		return err
	}
	return os.WriteFile(filePath, []byte(prefix+"\n"), 0o644)
}

func envSnapshotComposeMount(cfg envConfig) string {
	return config.SnapshotProjectDir(cfg.ProjectSlug)
}

func envSnapshotContentPaths() []string {
	return []string{"wp-content/uploads", "wp-content/plugins", "wp-content/languages"}
}

func newEnvSnapshotMetadata(cfg envConfig, name string, createdAt time.Time) envSnapshotMetadata {
	envDir := localEnvDir(cfg)
	return envSnapshotMetadata{
		Schema:         envSnapshotSchema,
		Name:           name,
		ProjectSlug:    cfg.ProjectSlug,
		CreatedAt:      createdAt.Format(time.RFC3339),
		EnvPath:        envDir,
		ComposeProject: envComposeProjectName(cfg.ProjectSlug),
		WordpressURL:   envSnapshotWordPressURL(cfg),
		Contents: envSnapshotContents{
			Database:       "database.sql.gz",
			WpContent:      "wp-content.tar.gz",
			WpContentPaths: envSnapshotContentPaths(),
		},
	}
}

func envSnapshotWordPressURL(cfg envConfig) string {
	return fmt.Sprintf("http://localhost:%d", cfg.WordpressPort)
}

func envSnapshotNormalizedName(input string) (string, error) {
	name := strings.TrimSpace(input)
	if name == "" {
		return "", ProjectError{Msg: "env snapshot name cannot be empty"}
	}
	name = strings.Join(strings.Fields(name), "-")
	if name == "" {
		return "", ProjectError{Msg: "env snapshot name cannot be empty"}
	}
	if filepath.IsAbs(name) {
		return "", ProjectError{Msg: fmt.Sprintf("env snapshot name %q must not be absolute", input)}
	}
	if strings.ContainsAny(name, "/\\") {
		return "", ProjectError{Msg: fmt.Sprintf("env snapshot name %q must not contain path separators", input)}
	}
	if strings.Contains(name, "..") {
		return "", ProjectError{Msg: fmt.Sprintf("env snapshot name %q must not contain path traversal", input)}
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return "", ProjectError{Msg: fmt.Sprintf("env snapshot name %q contains unsafe characters", input)}
	}
	return name, nil
}

func envSnapshotExists(cfg envConfig, name string) bool {
	_, err := os.Stat(envSnapshotDir(cfg, name))
	return err == nil
}

func envSnapshotInteractive() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func envSnapshotMetadataJSON(meta envSnapshotMetadata) (string, error) {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return "", err
	}
	return string(append(data, '\n')), nil
}

func (c envConfig) managedUploadsDir() string {
	return filepath.Join(localEnvDir(c), firstNonEmpty(c.UploadsPath, "uploads"))
}

func (c envConfig) managedTransferDir() string {
	return filepath.Join(localEnvDir(c), envTransferPath)
}

func (c envConfig) uploadsContainerPath() string {
	return path.Join("/", "env", "uploads")
}

func localEnvDir(cfg envConfig) string {
	return cfg.EnvDir
}

func envPortBlockStart(projectSlug string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(cleanEnvSlug(projectSlug)))
	return 18000 + int(h.Sum32()%1000)*4
}

func envDerivedPorts(projectSlug string) (int, int, int) {
	base := envPortBlockStart(projectSlug)
	return base, base + 1, base + 2
}

func cleanEnvSlug(projectSlug string) string {
	cleaned := strings.ToLower(strings.TrimSpace(projectSlug))
	var b strings.Builder
	b.Grow(len(cleaned) + len("nf__env"))
	for _, r := range cleaned {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('_')
		default:
			b.WriteByte('_')
		}
	}
	slug := strings.Trim(b.String(), "_-")
	if slug == "" {
		slug = "project"
	}
	return slug
}

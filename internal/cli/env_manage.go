package cli

// Managed local env filesystem, credentials, ports, and compose args.

import (
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/nonfiction/nf/internal/config"
)

func envCommandDir(cfg envConfig) string {
	return localEnvDir(cfg)
}

func ensureManagedEnv(cfg envConfig) error {
	envDir := localEnvDir(cfg)
	if strings.TrimSpace(envDir) == "" {
		return fmt.Errorf("missing managed env directory")
	}
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(envSnapshotProjectDir(cfg), 0o755); err != nil {
		return err
	}
	if err := os.Chmod(envSnapshotProjectDir(cfg), 0o777); err != nil {
		return err
	}
	files := map[string]string{
		filepath.Join(envDir, "docker-compose.yml"):                                  renderEnvCompose(cfg),
		filepath.Join(envDir, ".env"):                                                renderEnvFile(cfg),
		filepath.Join(envDir, "php", "uploads.ini"):                                  renderEnvUploadsINI(),
		filepath.Join(envDir, "wordpress", "Dockerfile"):                             renderEnvDockerfile(),
		filepath.Join(envDir, "wordpress", "wordpress-rewrites.conf"):                renderEnvRewritesConf(),
		filepath.Join(envDir, firstNonEmpty(cfg.UploadsPath, "uploads"), ".gitkeep"): "",
	}
	for path, contents := range files {
		if err := writeManagedFile(path, contents, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func envConfigWithAdminCredentials(cfg envConfig) (envConfig, error) {
	if cfg.AdminUser != "" && cfg.AdminEmail != "" && cfg.AdminPassword != "" {
		return cfg, nil
	}
	values, err := loadGlobalConfig()
	if err != nil {
		return cfg, err
	}
	adminEmail := firstNonEmpty(cfg.AdminEmail, values["default_wp_email"])
	if adminEmail == "" {
		return cfg, ProjectError{Msg: fmt.Sprintf("Expected default_wp_email in %s. Set it with nf config set-default-wp-email <email>.", config.ConfigFile())}
	}
	adminUser := firstNonEmpty(cfg.AdminUser, values["default_wp_user"], "admin")
	adminPassword, err := envAdminPassword(cfg)
	if err != nil {
		return cfg, err
	}
	cfg.AdminUser = adminUser
	cfg.AdminEmail = adminEmail
	cfg.AdminPassword = adminPassword
	return cfg, nil
}

func envAdminPassword(cfg envConfig) (string, error) {
	if cfg.AdminPassword != "" {
		return cfg.AdminPassword, nil
	}
	return deriveProjectPassword(cfg.ProjectSlug, "wp-admin", cfg.PasswordVersion)
}

func writeManagedFile(path, contents string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) == contents {
			return os.Chmod(path, mode)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		return err
	}
	return nil
}

func envPortInUse(port int) bool {
	if port <= 0 || port > 65535 {
		return true
	}
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return true
	}
	_ = listener.Close()
	return false
}

func envPortsInUse(cfg envConfig) []int {
	occupied := make([]int, 0, 2)
	for _, port := range []int{cfg.WordpressPort, cfg.MailpitPort} {
		if envPortInUse(port) {
			occupied = append(occupied, port)
		}
	}
	return occupied
}

func envPortCollisionMessage(cfg envConfig, occupied []int) string {
	if len(occupied) == 0 {
		return ""
	}
	ports := append([]int(nil), occupied...)
	sort.Ints(ports)
	projectLabel := firstNonEmpty(cfg.ProjectSlug, "project")
	block := fmt.Sprintf("The %s env wants:\n  WordPress: http://localhost:%d\n  Mailpit:   http://localhost:%d\n\nSet env.ports.wordpress and env.ports.mailpit in nf.json to override.", projectLabel, cfg.WordpressPort, cfg.MailpitPort)
	if len(ports) == 1 {
		return fmt.Sprintf("Port %d is already in use.\n\n%s", ports[0], block)
	}
	parts := make([]string, 0, len(ports))
	for _, port := range ports {
		parts = append(parts, strconv.Itoa(port))
	}
	return fmt.Sprintf("Ports %s are already in use.\n\n%s", strings.Join(parts, " and "), block)
}

func preflightEnvPorts(cfg envConfig) error {
	if envManagedComposeExists(cfg) {
		return nil
	}
	if occupied := envPortsInUse(cfg); len(occupied) > 0 {
		return fmt.Errorf("%s", envPortCollisionMessage(cfg, occupied))
	}
	return nil
}

func envManagedComposeExists(cfg envConfig) bool {
	envDir := localEnvDir(cfg)
	if strings.TrimSpace(envDir) == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(envDir, "docker-compose.yml")); err != nil {
		return false
	}
	values, err := config.ReadEnvFile(filepath.Join(envDir, ".env"))
	if err != nil {
		return false
	}
	return values["COMPOSE_PROJECT_NAME"] == envComposeProjectName(cfg.ProjectSlug)
}

func envComposeArgs(cfg envConfig, args ...string) []string {
	fields := strings.Fields(firstNonEmpty(cfg.Compose, "docker compose"))
	return append(fields, args...)
}

func envCliArgs(cfg envConfig, args ...string) []string {
	return append(envComposeArgs(cfg, "run", "--rm", firstNonEmpty(cfg.CliService, "cli")), args...)
}

func envWpArgs(cfg envConfig, args ...string) []string {
	return append(envCliArgs(cfg, "wp"), append(args, "--allow-root")...)
}

func envShellArgs(cfg envConfig) []string {
	return envComposeArgs(cfg, "exec", firstNonEmpty(cfg.WordpressService, "wordpress"), "bash")
}

func envWpProbeArgs(cfg envConfig, args ...string) []string {
	return envWpArgs(cfg, args...)
}

func envWpThemeIsActiveArgs(cfg envConfig, slug string) []string {
	return envWpArgs(cfg, "theme", "is-active", firstNonEmpty(slug, cfg.ThemeMountSlug, cfg.ThemeSlug, "theme"))
}

func envWpCoreInstallArgs(cfg envConfig) []string {
	slug := firstNonEmpty(cfg.ThemeMountSlug, cfg.ThemeSlug, "theme")
	return append(envComposeArgs(cfg, "run", "--rm", firstNonEmpty(cfg.CliService, "cli"), "sh", "-lc"), `wp core install --url="$WP_URL" --title="$WP_TITLE" --admin_user="$ADMIN_USER" --admin_password="$ADMIN_PASSWORD" --admin_email="$ADMIN_EMAIL" --skip-email --allow-root && wp theme activate `+slug+` --allow-root`)
}

func envWpThemeActivateArgs(cfg envConfig, slug string) []string {
	return envWpArgs(cfg, "theme", "activate", firstNonEmpty(slug, cfg.ThemeMountSlug, cfg.ThemeSlug, "theme"))
}

func envThemeArchivePaths(cfg envConfig, sourcePath string) (string, string) {
	base := filepath.Base(sourcePath)
	host := filepath.Join(envCommandDir(cfg), firstNonEmpty(cfg.UploadsPath, "uploads"), base)
	container := path.Join("/", "env", firstNonEmpty(cfg.UploadsPath, "uploads"), base)
	return host, container
}

func envRepoPath(root, sourcePath string) string {
	if filepath.IsAbs(sourcePath) {
		return sourcePath
	}
	return filepath.Join(root, sourcePath)
}

func copyFile(sourcePath, destinationPath string) error {
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
	defer func() { _ = output.Close() }()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	return nil
}

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

const (
	defaultDockerDBImage        = "mariadb:11"
	defaultDockerWordpressImage = "wordpress:php8.3-apache"
	defaultDockerUser           = "nonfiction"
	envTransferPath             = ".nf-transfer"
	projectUploadsSymlink       = "uploads"
)

func envCommandDir(cfg envConfig) string {
	return localEnvDir(cfg)
}

func ensureManagedEnv(cfg envConfig) error {
	envDir := localEnvDir(cfg)
	if strings.TrimSpace(envDir) == "" {
		return fmt.Errorf("missing managed env directory")
	}
	if err := validateDockerUser(firstNonEmpty(cfg.DockerUser, defaultDockerUser)); err != nil {
		return err
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
		filepath.Join(envDir, "wordpress", "Dockerfile"):                             renderEnvDockerfile(cfg),
		filepath.Join(envDir, "wordpress", "wordpress-rewrites.conf"):                renderEnvRewritesConf(),
		filepath.Join(envDir, firstNonEmpty(cfg.UploadsPath, "uploads"), ".gitkeep"): "",
		filepath.Join(envDir, envTransferPath, ".gitkeep"):                           "",
	}
	for path, contents := range files {
		if err := writeManagedFile(path, contents, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func envConfigWithAdminCredentials(cfg envConfig) (envConfig, error) {
	values, err := loadGlobalConfig()
	if err != nil {
		return cfg, err
	}
	cfg = envConfigWithDockerSettings(cfg, values)
	if cfg.AdminUser != "" && cfg.AdminEmail != "" && cfg.AdminPassword != "" && cfg.DBUser != "" && cfg.DBPassword != "" {
		return cfg, nil
	}
	adminEmail := firstNonEmpty(cfg.AdminEmail, values["default_wp_email"])
	if adminEmail == "" {
		return cfg, ProjectError{Msg: fmt.Sprintf("Expected default_wp_email in %s. Set it with nf config set-default-wp-email <email>.", config.ConfigFile())}
	}
	adminUser := firstNonEmpty(cfg.AdminUser, values["default_wp_user"], defaultWordPressAdminUser)
	adminPassword, err := envAdminPassword(cfg)
	if err != nil {
		return cfg, err
	}
	cfg, err = envConfigWithDBCredentials(cfg)
	if err != nil {
		return cfg, err
	}
	cfg.AdminUser = adminUser
	cfg.AdminEmail = adminEmail
	cfg.AdminPassword = adminPassword
	return cfg, nil
}

func envConfigWithLiveAdminUser(cfg envConfig) envConfig {
	output, err := runCommandSpecOutputSilentFn(execSpec{Dir: localEnvDir(cfg), Args: envWpArgs(cfg, "user", "get", "1", "--field=user_login")})
	if err != nil {
		return cfg
	}
	if user := strings.TrimSpace(output); user != "" {
		cfg.AdminUser = user
	}
	return cfg
}

func envConfigWithDockerSettings(cfg envConfig, values map[string]string) envConfig {
	cfg.DockerDBImage = firstNonEmpty(cfg.DockerDBImage, values["docker_db_image"], defaultDockerDBImage)
	cfg.DockerWPImage = firstNonEmpty(cfg.DockerWPImage, values["docker_wordpress_image"], defaultDockerWordpressImage)
	cfg.DockerUser = firstNonEmpty(cfg.DockerUser, values["docker_user"], defaultDockerUser)
	return cfg
}

func envConfigWithDockerConfig(cfg envConfig) (envConfig, error) {
	values, err := loadGlobalConfig()
	if err != nil {
		return cfg, err
	}
	return envConfigWithDockerSettings(cfg, values), nil
}

func validateDockerUser(user string) error {
	trimmed := strings.TrimSpace(user)
	if trimmed == "" {
		return ProjectError{Msg: "docker_user must be a non-empty Linux username"}
	}
	if len(trimmed) > 32 {
		return ProjectError{Msg: "docker_user must be 32 characters or fewer"}
	}
	for i, r := range trimmed {
		if r >= 'a' && r <= 'z' || r == '_' || i > 0 && r >= '0' && r <= '9' || i > 0 && r == '-' {
			continue
		}
		return ProjectError{Msg: "docker_user must start with a lowercase letter or underscore and use only lowercase letters, numbers, underscores, and hyphens"}
	}
	return nil
}

func envConfigWithDBCredentials(cfg envConfig) (envConfig, error) {
	if cfg.DBUser != "" && cfg.DBPassword != "" {
		return cfg, nil
	}
	dbPassword, err := envDBPassword(cfg)
	if err != nil {
		return cfg, err
	}
	cfg.DBUser = firstNonEmpty(cfg.DBUser, cfg.ProjectSlug)
	cfg.DBPassword = dbPassword
	return cfg, nil
}

func envAdminPassword(cfg envConfig) (string, error) {
	if cfg.AdminPassword != "" {
		return cfg.AdminPassword, nil
	}
	return deriveProjectPassword(cfg.ProjectSlug, "wp-admin", cfg.PasswordVersion)
}

func envDBPassword(cfg envConfig) (string, error) {
	if cfg.DBPassword != "" {
		return cfg.DBPassword, nil
	}
	return deriveProjectPassword(cfg.ProjectSlug, "mysql", cfg.PasswordVersion)
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
	occupied := make([]int, 0, 3)
	for _, port := range []int{cfg.WordpressPort, cfg.MailpitPort, cfg.AdminerPort} {
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
	block := fmt.Sprintf("The %s env wants:\n  WordPress: http://localhost:%d\n  Mailpit:   http://localhost:%d\n  Database:  http://localhost:%d\n\nSet local.ports.wordpress, local.ports.mailpit, and local.ports.db in nf.json to override.", projectLabel, cfg.WordpressPort, cfg.MailpitPort, cfg.AdminerPort)
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

func envWordpressExecArgs(cfg envConfig, args ...string) []string {
	return append(envComposeArgs(cfg, "exec", "--user", firstNonEmpty(cfg.DockerUser, defaultDockerUser), firstNonEmpty(cfg.WordpressService, "wordpress")), args...)
}

func envWordpressRootExecArgs(cfg envConfig, args ...string) []string {
	return append(envComposeArgs(cfg, "exec", "--user", "root", firstNonEmpty(cfg.WordpressService, "wordpress")), args...)
}

func envWpArgs(cfg envConfig, args ...string) []string {
	return append(envWordpressExecArgs(cfg, "wp"), args...)
}

func envShellArgs(cfg envConfig) []string {
	return envComposeArgs(cfg, "exec", "--user", firstNonEmpty(cfg.DockerUser, defaultDockerUser), firstNonEmpty(cfg.WordpressService, "wordpress"), "bash")
}

func envWpProbeArgs(cfg envConfig, args ...string) []string {
	return envWpArgs(cfg, args...)
}

func envWpBootstrapReadyArgs(cfg envConfig) []string {
	return append(envWordpressExecArgs(cfg, "sh", "-lc"), `set -eu
for i in $(seq 1 60); do
  if [ -f wp-config.php ] && [ -f wp-settings.php ] && grep -q "wp-settings.php" wp-config.php; then
    exit 0
  fi
  sleep 1
done
printf '%s\n' 'WordPress files are not ready yet.' >&2
exit 1`)
}

func envWpBootstrapPreviewArgs(cfg envConfig, label string) []string {
	return envWordpressExecArgs(cfg, "<"+label+">")
}

func envWpThemeIsActiveArgs(cfg envConfig, slug string) []string {
	return envWpArgs(cfg, "theme", "is-active", firstNonEmpty(slug, activeEnvThemeSlug(cfg)))
}

func envWpThemeIsInstalledArgs(cfg envConfig, slug string) []string {
	return envWpArgs(cfg, "theme", "is-installed", firstNonEmpty(slug, activeEnvThemeSlug(cfg)))
}

func envWpCoreInstallArgs(cfg envConfig) []string {
	return append(envWordpressExecArgs(cfg, "sh", "-lc"), `wp core install --url="$WP_URL" --title="$WP_TITLE" --admin_user="$ADMIN_USER" --admin_password="$ADMIN_PASSWORD" --admin_email="$ADMIN_EMAIL" --skip-email`)
}

func envWpMailpitSMTPArgs(cfg envConfig) []string {
	return append(envWordpressExecArgs(cfg, "sh", "-lc"), `set -eu
mkdir -p wp-content/mu-plugins
cat > wp-content/mu-plugins/nf-mailpit.php <<'PHP'
<?php
/**
 * Route local WordPress email through Mailpit.
 */
add_action('phpmailer_init', static function ($phpmailer) {
    $phpmailer->isSMTP();
    $phpmailer->Host = 'mailpit';
    $phpmailer->Port = 1025;
    $phpmailer->SMTPAuth = false;
});
PHP`)
}

func envWpContentPermissionsArgs(cfg envConfig) []string {
	dockerUser := firstNonEmpty(cfg.DockerUser, defaultDockerUser)
	return append(envWordpressRootExecArgs(cfg, "sh", "-lc"), fmt.Sprintf(`set -eu
cd /var/www/html
mkdir -p wp-content/uploads
repo_plugins=%s
for dir in wp-content/uploads wp-content/plugins wp-content/mu-plugins wp-content/languages; do
  if [ -e "$dir" ]; then
    if [ "$dir" = "wp-content/plugins" ] && [ -n "$repo_plugins" ]; then
      for entry in "$dir"/* "$dir"/.[!.]* "$dir"/..?*; do
        [ -e "$entry" ] || continue
        base="${entry##*/}"
        case " $repo_plugins " in *" $base "*) continue ;; esac
        chown -R %s:www-data "$entry"
        chmod -R u+rwX,g+rwX,o-rwx "$entry"
        find "$entry" -type d -exec chmod g+s {} +
      done
    else
      chown -R %s:www-data "$dir"
      chmod -R u+rwX,g+rwX,o-rwx "$dir"
      find "$dir" -type d -exec chmod g+s {} +
    fi
  fi
done`, shellQuoteArg(envRepoPluginSlugList(cfg)), dockerUser, dockerUser))
}

func envWpThemeActivateArgs(cfg envConfig, slug string) []string {
	return envWpArgs(cfg, "theme", "activate", firstNonEmpty(slug, activeEnvThemeSlug(cfg)))
}

func envThemeArchivePaths(cfg envConfig, sourcePath string) (string, string) {
	base := filepath.Base(sourcePath)
	host := filepath.Join(envCommandDir(cfg), envTransferPath, base)
	container := path.Join("/", "env", "uploads", base)
	return host, container
}

func ensureProjectUploadsSymlink(root string, cfg envConfig) error {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	target := cfg.managedUploadsDir()
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	linkPath := filepath.Join(root, projectUploadsSymlink)
	info, err := os.Lstat(linkPath)
	if err != nil {
		if os.IsNotExist(err) {
			return os.Symlink(target, linkPath)
		}
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return ProjectError{Msg: fmt.Sprintf("refusing to replace existing project uploads path: %s", linkPath)}
	}
	if ok, err := projectUploadsSymlinkMatches(linkPath, target); err != nil {
		return err
	} else if !ok {
		return ProjectError{Msg: fmt.Sprintf("refusing to replace existing uploads symlink pointing outside this env: %s", linkPath)}
	}
	return nil
}

func removeProjectUploadsSymlink(root string, cfg envConfig) error {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	linkPath := filepath.Join(root, projectUploadsSymlink)
	info, err := os.Lstat(linkPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	if ok, err := projectUploadsSymlinkMatches(linkPath, cfg.managedUploadsDir()); err != nil {
		return err
	} else if !ok {
		return ProjectError{Msg: fmt.Sprintf("refusing to remove uploads symlink pointing outside this env: %s", linkPath)}
	}
	return os.Remove(linkPath)
}

func projectUploadsSymlinkMatches(linkPath, target string) (bool, error) {
	existing, err := os.Readlink(linkPath)
	if err != nil {
		return false, err
	}
	if !filepath.IsAbs(existing) {
		existing = filepath.Join(filepath.Dir(linkPath), existing)
	}
	existingAbs, err := filepath.Abs(existing)
	if err != nil {
		return false, err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return false, err
	}
	return filepath.Clean(existingAbs) == filepath.Clean(targetAbs), nil
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

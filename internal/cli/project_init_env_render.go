package cli

// Project initialization plus generated local env file renderers.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nonfiction/nf/internal/config"
	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/passwords"
	"github.com/nonfiction/nf/internal/project"
	"github.com/nonfiction/nf/internal/theme"
	"github.com/nonfiction/nf/internal/ui"
)

func cmdProjectInit(args projectInitArgs) int {
	root := projectInitRoot()
	if err := writeProjectInit(root, args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func projectInitRoot() string {
	if root, ok := currentGitRoot(); ok {
		return root
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func writeProjectInit(root string, args projectInitArgs) error {
	metadata := projectInitMetadata(args)
	if err := validateProjectMetadata(metadata); err != nil {
		return err
	}
	projectPath := config.ProjectFile(root)
	if !args.force {
		if _, err := os.Stat(projectPath); err == nil {
			return ProjectError{Msg: fmt.Sprintf("%s already exists; use --force to overwrite.", projectPath)}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if err := project.Save(root, metadata); err != nil {
		return err
	}
	fmt.Printf("Wrote %s\n", projectPath)
	return nil
}

func ensureEnvProjectMetadata() error {
	root, ok := currentGitRoot()
	if !ok {
		return ProjectError{Msg: "env up requires a .git repository above the current directory"}
	}
	projectPath := config.ProjectFile(root)
	if _, err := os.Stat(projectPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	slug, err := currentGitRootBase()
	if err != nil {
		return err
	}
	return writeProjectInit(root, projectInitArgs{projectSlug: slug, projectType: "wordpress-theme"})
}

func projectInitMetadata(args projectInitArgs) *projectMetadata {
	themePath := firstNonEmpty(args.themeSource, "theme")
	projectSlug := args.projectSlug
	themeSlug := firstNonEmpty(args.themeSlug, projectSlug, "theme")
	return &projectMetadata{
		Version: project.ManifestVersion,
		Project: project.Project{Slug: projectSlug},
		WordPress: project.WordPress{
			Themes: projectInitThemeList(projectSlug, themeSlug, themePath),
		},
	}
}

func projectInitThemeList(projectSlug, themeSlug, themePath string) []any {
	themes := []any{
		orderedObject{Pairs: []orderedPair{
			{Key: "slug", Value: themeSlug},
			{Key: "source", Value: wordpressThemeRepoSource},
			{Key: "path", Value: themePath},
			{Key: "package", Value: orderedObject{Pairs: []orderedPair{
				{Key: "output", Value: filepath.ToSlash(filepath.Join("dist", projectSlug+"-v{version}.zip"))},
			}}},
			{Key: "tasks", Value: defaultProjectTasks(themePath)},
		}},
	}
	seen := map[string]struct{}{themeSlug: {}}
	for _, slug := range defaultBundledWordPressThemes() {
		if _, ok := seen[slug]; ok {
			continue
		}
		themes = append(themes, slug)
		seen[slug] = struct{}{}
	}
	return themes
}

func defaultBundledWordPressThemes() []string {
	return []string{"twentytwentyfive", "twentytwentyfour", "twentytwentythree"}
}

type projectInitArgs struct {
	projectSlug string
	themeSlug   string
	themeSource string
	projectType string
	force       bool
}

type orderedObject struct {
	Pairs []orderedPair
}

type orderedPair struct {
	Key   string
	Value any
}

func orderedObjectMap(object orderedObject) map[string]any {
	result := make(map[string]any, len(object.Pairs))
	for _, pair := range object.Pairs {
		value := pair.Value
		if nested, ok := value.(orderedObject); ok {
			value = orderedObjectMap(nested)
		}
		result[pair.Key] = value
	}
	return result
}

func (o orderedObject) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, pair := range o.Pairs {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := marshalProjectJSONValue(pair.Key)
		if err != nil {
			return nil, err
		}
		value, err := marshalProjectJSONValue(pair.Value)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(value)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

func marshalProjectJSONValue(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}

func renderEnvCompose(cfg envConfig) string {
	wordpressService := firstNonEmpty(cfg.WordpressService, "wordpress")
	dbImage := firstNonEmpty(cfg.DockerDBImage, defaultDockerDBImage)
	wordpressImage := firstNonEmpty(cfg.DockerWPImage, defaultDockerWordpressImage)
	dockerUser := firstNonEmpty(cfg.DockerUser, defaultDockerUser)
	uploadsPath := firstNonEmpty(cfg.UploadsPath, "uploads")
	repoMounts := renderEnvRepoThemeMounts(cfg) + renderEnvRepoPluginMounts(cfg)
	return fmt.Sprintf(`services:
  db:
    image: %s
    command: --character-set-server=utf8mb4 --collation-server=utf8mb4_unicode_ci
    environment:
      MARIADB_DATABASE: ${DB_NAME}
      MARIADB_USER: ${DB_USER}
      MARIADB_PASSWORD: ${DB_PASSWORD}
      MARIADB_ROOT_PASSWORD: ${DB_ROOT_PASSWORD}
    healthcheck:
      test: ["CMD", "healthcheck.sh", "--connect", "--innodb_initialized"]
      interval: 10s
      timeout: 5s
      retries: 10
    volumes:
      - db_data:/var/lib/mysql

  %s:
    build:
      context: .
      dockerfile: wordpress/Dockerfile
    depends_on:
      db:
        condition: service_healthy
    ports:
      - "${WP_PORT}:80"
    environment:
      WORDPRESS_DB_HOST: db:3306
      WORDPRESS_DB_NAME: ${DB_NAME}
      WORDPRESS_DB_USER: ${DB_USER}
      WORDPRESS_DB_PASSWORD: ${DB_PASSWORD}
      WP_URL: ${WP_URL}
      WP_TITLE: ${WP_TITLE}
      ADMIN_USER: ${ADMIN_USER}
      ADMIN_PASSWORD: ${ADMIN_PASSWORD}
      ADMIN_EMAIL: ${ADMIN_EMAIL}
      HOME: /home/%s
      WP_CLI_CACHE_DIR: /tmp/wp-cli-cache
      WORDPRESS_CONFIG_EXTRA: |
        define('WP_HOME', getenv('WP_URL'));
        define('WP_SITEURL', getenv('WP_URL'));
        define('FS_METHOD', 'direct');
        if ( ! defined('WP_DEBUG') ) define('WP_DEBUG', true);
        if ( ! defined('WP_DEBUG_LOG') ) define('WP_DEBUG_LOG', true);
        if ( ! defined('WP_DEBUG_DISPLAY') ) define('WP_DEBUG_DISPLAY', false);
    volumes:
      - wp_data:/var/www/html
%s      - ./php/uploads.ini:/usr/local/etc/php/conf.d/uploads.ini:ro
      - ./%s:/var/www/html/wp-content/uploads
      - ./%s:%s
      - %s:/env-snapshots

  mailpit:
    image: axllent/mailpit
    ports:
      - "${MAILPIT_PORT}:8025"

  db-ui:
    image: %s
    depends_on:
      db:
        condition: service_healthy
    command: >
      sh -lc "mkdir -p /var/www/html
      && php -r 'copy(\"https://www.adminneo.org/files/5.4.1/mysql_en_default/adminneo-5.4.1.php\", \"/var/www/html/index.php\");'
      && php -S 0.0.0.0:80 -t /var/www/html"
    ports:
      - "${DB_UI_PORT}:80"
    volumes:
      - ./php/uploads.ini:/usr/local/etc/php/conf.d/uploads.ini:ro

volumes:
  db_data:
  wp_data:
`, dbImage, wordpressService, dockerUser, repoMounts, uploadsPath, envTransferPath, path.Join("/", "env", "uploads"), envSnapshotComposeMount(cfg), wordpressImage)
}

func renderEnvRepoThemeMounts(cfg envConfig) string {
	if len(cfg.RepoThemeMounts) == 0 {
		return ""
	}
	mounts := make([]envThemeMount, 0, len(cfg.RepoThemeMounts))
	for _, mount := range cfg.RepoThemeMounts {
		if strings.TrimSpace(mount.Slug) == "" || strings.TrimSpace(mount.Host) == "" {
			continue
		}
		mounts = append(mounts, mount)
	}
	sort.Slice(mounts, func(i, j int) bool { return mounts[i].Slug < mounts[j].Slug })
	var builder strings.Builder
	for _, mount := range mounts {
		builder.WriteString("      - ")
		builder.WriteString(mount.Host)
		builder.WriteString(":/var/www/html/wp-content/themes/")
		builder.WriteString(mount.Slug)
		builder.WriteByte('\n')
	}
	return builder.String()
}

func renderEnvRepoPluginMounts(cfg envConfig) string {
	if len(cfg.RepoPluginMounts) == 0 {
		return ""
	}
	mounts := make([]envPluginMount, 0, len(cfg.RepoPluginMounts))
	for _, mount := range cfg.RepoPluginMounts {
		if strings.TrimSpace(mount.Slug) == "" || strings.TrimSpace(mount.Host) == "" {
			continue
		}
		mounts = append(mounts, mount)
	}
	sort.Slice(mounts, func(i, j int) bool { return mounts[i].Slug < mounts[j].Slug })
	var builder strings.Builder
	for _, mount := range mounts {
		builder.WriteString("      - ")
		builder.WriteString(mount.Host)
		builder.WriteString(":/var/www/html/wp-content/plugins/")
		builder.WriteString(mount.Slug)
		builder.WriteByte('\n')
	}
	return builder.String()
}

func renderEnvFile(cfg envConfig) string {
	wpTitle := slugToTitle(cfg.ProjectSlug)
	dbUser := firstNonEmpty(cfg.DBUser, cfg.ProjectSlug)
	dbPassword := firstNonEmpty(cfg.DBPassword, "wordpress")
	adminUser := firstNonEmpty(cfg.AdminUser, defaultWordPressAdminUser)
	adminPassword := firstNonEmpty(cfg.AdminPassword, "admin")
	adminEmail := firstNonEmpty(cfg.AdminEmail, "web@nonfiction.ca")
	return fmt.Sprintf(`COMPOSE_PROJECT_NAME=%s
WP_PORT=%d
MAILPIT_PORT=%d
DB_UI_PORT=%d
DB_NAME=%s
DB_USER=%s
DB_PASSWORD=%s
DB_ROOT_PASSWORD=root
WP_URL=http://localhost:%d
WP_TITLE=%s
ADMIN_USER=%s
ADMIN_PASSWORD=%s
ADMIN_EMAIL=%s
`, envComposeProjectName(cfg.ProjectSlug), cfg.WordpressPort, cfg.MailpitPort, cfg.AdminerPort, cfg.ProjectSlug, envFileValue(dbUser), envFileValue(dbPassword), cfg.WordpressPort, envFileValue(wpTitle), envFileValue(adminUser), envFileValue(adminPassword), envFileValue(adminEmail))
}

func envFileValue(value string) string {
	if value == "" {
		return "''"
	}
	if strings.IndexFunc(value, func(r rune) bool {
		return (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && !strings.ContainsRune("_./:@%+-", r)
	}) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func envComposeProjectName(projectSlug string) string {
	return "nf_" + cleanEnvSlug(projectSlug) + "_env"
}

func renderEnvInfo(cfg envConfig, includeURLs bool, remoteRows ...detailRow) string {
	title := cfg.ProjectSlug + ":local"
	siteURL := fmt.Sprintf("http://localhost:%d", cfg.WordpressPort)
	dbURL := localEnvDBURL(cfg)
	mailpitURL := fmt.Sprintf("http://localhost:%d", cfg.MailpitPort)
	lines := []string{title, strings.Repeat("─", len(title))}
	rows := []detailRow{
		{label: "Site", value: cfg.ProjectSlug},
		{label: "Env", value: "local"},
	}
	rows = append(rows,
		detailRow{label: "Path", value: localEnvDir(cfg)},
		detailRow{label: "PHP", value: localEnvPHPVersion()},
		detailRow{label: "Compose", value: envComposeProjectName(cfg.ProjectSlug)},
	)
	lines = append(lines, detailRowLines(rows, 0)...)
	if !includeURLs {
		return strings.Join(lines, "\n")
	}
	dbRows := []detailRow{
		{label: "DB URL", value: dbURL},
		{label: "DB user", value: firstNonEmpty(cfg.DBUser, cfg.ProjectSlug)},
		{label: "DB pass", value: cfg.DBPassword},
	}
	emailRows := []detailRow{
		{label: "Mailpit URL", value: mailpitURL},
	}
	wordpressRows := []detailRow{
		{label: "Site URL", value: siteURL},
		{label: "Admin URL", value: siteURL + "/wp-login.php"},
		{label: "WP user", value: cfg.AdminUser},
		{label: "WP pass", value: cfg.AdminPassword},
	}
	sectionWidth := detailRowsWidth(dbRows, emailRows, wordpressRows, remoteRows)
	if hasDetailRows(dbRows) {
		lines = append(lines, "", "Database")
		lines = append(lines, detailRowLinesWithWidth(dbRows, 2, sectionWidth)...)
	}
	if hasDetailRows(emailRows) {
		lines = append(lines, "", "Email")
		lines = append(lines, detailRowLinesWithWidth(emailRows, 2, sectionWidth)...)
	}
	if hasDetailRows(wordpressRows) {
		lines = append(lines, "", "WordPress")
		lines = append(lines, detailRowLinesWithWidth(wordpressRows, 2, sectionWidth)...)
	}
	if hasDetailRows(remoteRows) {
		lines = append(lines, "", "Remotes")
		lines = append(lines, detailRowLinesWithWidth(remoteRows, 2, sectionWidth)...)
	}
	return strings.Join(lines, "\n")
}

func envRemoteURLRows(metadata *projectMetadata) ([]detailRow, error) {
	remotes, err := projectRemotes(metadata, false)
	if err != nil {
		return nil, err
	}
	if len(remotes) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]detailRow, 0, len(names))
	for _, name := range names {
		siteID, remoteEnv, ok, err := projectRemoteAlias(metadata, name)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		record, _, err := cachedSiteEnv(siteID, remoteEnv)
		if err != nil {
			return nil, err
		}
		if record == nil {
			continue
		}
		remoteURL := firstRecordString(record, "url", "site_url", "home_url", "hostname")
		if remoteURL == "" {
			continue
		}
		rows = append(rows, detailRow{label: envRemoteURLLabel(name), value: remoteURL})
	}
	return rows, nil
}

func envRemoteURLLabel(remoteName string) string {
	parts := strings.FieldsFunc(strings.TrimSpace(remoteName), func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == ' '
	})
	for i, part := range parts {
		part = strings.ToLower(part)
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	label := strings.Join(parts, " ")
	if label == "" {
		label = strings.TrimSpace(remoteName)
	}
	return label + " URL"
}

func localEnvDBURL(cfg envConfig) string {
	dbName := cfg.ProjectSlug
	dbUser := firstNonEmpty(cfg.DBUser, cfg.ProjectSlug)
	return fmt.Sprintf("http://localhost:%d/?mysql=db&username=%s&db=%s", cfg.AdminerPort, url.QueryEscape(dbUser), url.QueryEscape(dbName))
}

func localEnvPHPVersion() string { return "8.3" }

func renderEnvUploadsINI() string {
	return "file_uploads=On\nmemory_limit=1024M\nupload_max_filesize=1024M\npost_max_size=1024M\nmax_execution_time=120\nmax_input_time=120\n"
}

func renderEnvDockerfile(cfg envConfig) string {
	return fmt.Sprintf(`FROM %s

RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    curl \
    dnsutils \
    iputils-ping \
    less \
    mariadb-client \
    nano \
    procps \
    vim \
  && rm -rf /var/lib/apt/lists/*

RUN a2enmod rewrite \
  && sed -ri 's/AllowOverride None/AllowOverride All/g' /etc/apache2/apache2.conf

RUN curl -fsSL -o /usr/local/bin/wp https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar \
  && chmod +x /usr/local/bin/wp

RUN useradd --create-home --shell /bin/bash --groups www-data %s \
  && printf '\nexport APACHE_RUN_USER=%s\nexport APACHE_RUN_GROUP=www-data\numask 0002\n' >> /etc/apache2/envvars \
  && mkdir -p /tmp/wp-cli-cache /home/%s/.wp-cli \
  && chown -R %s:www-data /home/%s /tmp/wp-cli-cache /usr/src/wordpress /var/www/html \
  && chmod -R g+rwX /tmp/wp-cli-cache /usr/src/wordpress /var/www/html

COPY wordpress/wordpress-rewrites.conf /etc/apache2/conf-enabled/wordpress-rewrites.conf
`, firstNonEmpty(cfg.DockerWPImage, defaultDockerWordpressImage), firstNonEmpty(cfg.DockerUser, defaultDockerUser), firstNonEmpty(cfg.DockerUser, defaultDockerUser), firstNonEmpty(cfg.DockerUser, defaultDockerUser), firstNonEmpty(cfg.DockerUser, defaultDockerUser), firstNonEmpty(cfg.DockerUser, defaultDockerUser))
}

func renderEnvRewritesConf() string {
	return `<Directory /var/www/html>
  Options FollowSymLinks
  AllowOverride All
  Require all granted

  RewriteEngine On
  RewriteBase /
  RewriteRule ^index\.php$ - [L]
  RewriteCond %{REQUEST_FILENAME} !-f
  RewriteCond %{REQUEST_FILENAME} !-d
  RewriteRule . /index.php [L]
</Directory>
`
}

func cmdPasswordDerive(scope, identity, version string, versionSet, nonInteractive bool) int {
	scope = strings.TrimSpace(scope)
	identity = strings.TrimSpace(identity)
	if scope == "" || identity == "" {
		fmt.Fprintln(os.Stderr, "password derive requires a scope and at least one value")
		return 1
	}
	if passwordDeriveScopeUsesProjectVersion(scope) && !versionSet {
		version = passwordDeriveDefaultVersion(scope, identity)
	}
	if err := envwizard.Ensure(passwordRequirements(), nonInteractive); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	salt, err := passwords.SecretSalt()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(passwords.DerivePassword(passwordDeriveIdentity(scope, identity, version), scope, salt))
	return 0
}

func cmdThemePackage(source, output string, dryRun bool) int {
	return cmdPackage("theme package", source, output, dryRun)
}

func parseThemeDeployArgs(args []string) (remote string, dryRun, restart, ok bool) {
	for _, arg := range args {
		switch arg {
		case "--dry-run":
			dryRun = true
		case "--restart":
			restart = true
		case "--":
			return "", false, false, false
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, false, false
			}
			if remote != "" {
				return "", false, false, false
			}
			remote = arg
		}
	}
	return remote, dryRun, restart, true
}

func chooseProjectRemote(action string) (string, error) {
	options, err := projectRemoteSelectOptions(action)
	if err != nil {
		return "", err
	}
	return remoteSelectFn(projectRemoteSelectTitle(action), options)
}

func chooseProjectRemoteOrOnly(action string) (string, error) {
	options, err := projectRemoteSelectOptions(action)
	if err != nil {
		return "", err
	}
	if len(options) == 1 {
		return options[0].Value, nil
	}
	return remoteSelectFn(projectRemoteSelectTitle(action), options)
}

func projectRemoteSelectTitle(action string) string {
	switch action {
	case "push":
		return "Choose a remote to push to"
	case "pull":
		return "Choose a remote to pull from"
	default:
		return "Choose a remote to " + action
	}
}

func projectRemoteSelectOptions(action string) ([]ui.SelectOption, error) {
	root, ok := currentGitRoot()
	if !ok {
		return nil, ProjectError{Msg: fmt.Sprintf("%s requires a .git repository above the current directory", action)}
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		return nil, err
	}
	remotes, err := projectRemotes(metadata, false)
	if err != nil {
		return nil, err
	}
	if len(remotes) == 0 {
		return nil, ProjectError{Msg: "No remotes found. Add one with nf remote add <name> <site.target:env>."}
	}
	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	options := make([]ui.SelectOption, 0, len(names))
	for _, name := range names {
		label := name
		if _, _, ok, err := projectRemoteAlias(metadata, name); err != nil {
			return nil, err
		} else if ok {
			label += " (" + strings.TrimSpace(remotes[name]) + ")"
		}
		options = append(options, ui.SelectOption{Value: name, Label: label})
	}
	return options, nil
}

func validateThemeDeploySlug(slug string) error {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return ProjectError{Msg: "theme slug cannot be empty"}
	}
	if err := project.ValidateName(slug); err != nil {
		return ProjectError{Msg: fmt.Sprintf("theme slug %q must be one safe directory name", slug)}
	}
	return nil
}

func cmdPackage(commandName, source, output string, dryRun bool) int {
	root, _ := config.DiscoverProjectRoot("")
	if root == "" {
		root = "."
	}
	metadata, err := loadProjectMetadataOrError(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if source == "" {
		_, sourceDir, _, err := projectRepoTheme(root, metadata, commandName, true)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		source = sourceDir
	}
	sourceDir := source
	if !filepath.IsAbs(sourceDir) {
		sourceDir = filepath.Join(root, sourceDir)
	}
	repoTheme, _, _, err := projectRepoTheme(root, metadata, commandName, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	projectSlug := firstNonEmpty(metadata.Project.Slug, "project")
	versionedOutput := output
	if versionedOutput == "" {
		versionedOutput, err = repoThemePackageOutput(metadata)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		versionedOutput = firstNonEmpty(versionedOutput, filepath.ToSlash(filepath.Join("dist", projectSlug+"-v{version}.zip")))
	}
	versionedOutput, err = resolveVersionedArtifactPath(sourceDir, versionedOutput)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	output = versionedOutput
	if !filepath.IsAbs(output) {
		if strings.HasSuffix(strings.ToLower(output), ".zip") {
			output = filepath.Join(root, output)
		} else {
			output = filepath.Join(root, output, projectSlug+".zip")
		}
	}
	themeSlug := firstNonEmpty(repoTheme.Slug, projectSlug, filepath.Base(filepath.Clean(sourceDir)), "theme")
	if err := validateThemeDeploySlug(themeSlug); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	result, err := theme.PackageTheme(sourceDir, output, themeSlug, dryRun)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if result.DryRun {
		fmt.Printf("Would package %s -> %s (%d files)\n", result.SourceDir, result.OutputPath, result.FileCount)
	} else {
		fmt.Printf("Wrote %s (%d files)\n", result.OutputPath, result.FileCount)
	}
	return 0
}

func resolveVersionedArtifactPath(sourceDir, template string) (string, error) {
	if !strings.Contains(template, "{version}") {
		return template, nil
	}
	version, err := readThemeVersion(sourceDir)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(template, "{version}", version), nil
}

func readThemeVersion(sourceDir string) (string, error) {
	stylePath := filepath.Join(sourceDir, "style.css")
	if version, found, err := readThemeStyleVersion(stylePath); err != nil {
		return "", err
	} else if found {
		return version, nil
	}

	packagePath := filepath.Join(sourceDir, "package.json")
	if version, found, err := readThemePackageVersion(packagePath); err != nil {
		return "", err
	} else if found {
		return version, nil
	}

	return "", fmt.Errorf("theme version not found: no Version header in %s and no version field in %s", stylePath, packagePath)
}

func readThemeStyleVersion(stylePath string) (string, bool, error) {
	data, err := os.ReadFile(stylePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "*"))
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "version") {
			continue
		}
		version := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "*/"))
		if version != "" {
			return version, true, nil
		}
	}
	return "", false, nil
}

func readThemePackageVersion(packagePath string) (string, bool, error) {
	data, err := os.ReadFile(packagePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", false, err
	}
	version := strings.TrimSpace(payload.Version)
	if version == "" {
		return "", false, nil
	}
	return version, true, nil
}

func normalizePassthroughArgs(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}

package cli

// Local env, snapshot, and remote-sync data types.
//
// These types are package-private because the CLI owns the on-disk layout and
// command behavior around them.

import (
	"time"

	"github.com/nonfiction/nf/internal/envwizard"
	"github.com/nonfiction/nf/internal/ui"
)

type envConfig struct {
	ProjectSlug      string
	PasswordVersion  string
	RepoRoot         string
	ThemePath        string
	EnvDir           string
	WordpressPort    int
	MailpitPort      int
	AdminerPort      int
	Compose          string
	WordpressService string
	DockerDBImage    string
	DockerWPImage    string
	DockerUser       string
	ThemeMountSlug   string
	UploadsPath      string
	ThemeSlug        string
	DBUser           string
	DBPassword       string
	AdminUser        string
	AdminEmail       string
	AdminPassword    string
	TablePrefix      string
	Themes           []wordpressThemeSpec
	RepoThemeMounts  []envThemeMount
	RepoPluginMounts []envPluginMount
}

type envThemeMount struct {
	Slug string
	Host string
}

type envPluginMount struct {
	Slug string
	Host string
}

type envSnapshotContents struct {
	Database       string   `json:"database"`
	WpContent      string   `json:"wp_content"`
	WpContentPaths []string `json:"wp_content_paths"`
}

type envSnapshotMetadata struct {
	Schema         int                 `json:"schema"`
	Name           string              `json:"name"`
	ProjectSlug    string              `json:"project_slug"`
	CreatedAt      string              `json:"created_at"`
	EnvPath        string              `json:"env_path"`
	ComposeProject string              `json:"compose_project"`
	WordpressURL   string              `json:"wordpress_url"`
	Contents       envSnapshotContents `json:"contents"`
}

type envSnapshotRecord struct {
	Metadata         envSnapshotMetadata
	Directory        string
	DatabaseArchive  string
	WpContentArchive string
	DatabaseSize     int64
	WpContentSize    int64
	CreatedAt        time.Time
}

type remoteSnapshotMetadata struct {
	Schema    int                 `json:"schema"`
	Source    string              `json:"source"`
	EnvID     string              `json:"env_id"`
	SiteID    string              `json:"site_id"`
	Env       string              `json:"env"`
	Provider  string              `json:"provider"`
	Target    string              `json:"target"`
	URL       string              `json:"url"`
	CreatedAt string              `json:"created_at"`
	Path      string              `json:"path"`
	Contents  envSnapshotContents `json:"contents"`
}

type remoteSnapshotRecord struct {
	Name             string
	Metadata         remoteSnapshotMetadata
	Directory        string
	DatabaseArchive  string
	WpContentArchive string
	DatabaseSize     int64
	WpContentSize    int64
	CreatedAt        time.Time
}

type envSnapshotPruneOptions struct {
	keep   int
	dryRun bool
	yes    bool
}

type envSnapshotUseOptions struct {
	name       string
	yes        bool
	remoteName string
	localName  string
}

type envRemoteSyncOptions struct {
	dryRun         bool
	execute        bool
	yes            bool
	nonInteractive bool
}

type envRemoteSyncTarget struct {
	Provider      string
	RemoteName    string
	SiteID        string
	Env           string
	URL           string
	TargetLabel   string
	TargetRef     string
	AccessLabel   string
	AccessSummary string
	SSHUser       string
	SSHHost       string
	SSHPort       string
	WordPressPath string
	WPCommand     string
	SudoFileOps   bool
}

type siteSnapshotOptions struct {
	output string
	dryRun bool
}

type siteExportOptions struct {
	output string
	dryRun bool
}

type siteExportManifest struct {
	Schema        int    `json:"schema"`
	Source        string `json:"source"`
	EnvID         string `json:"env_id"`
	SiteID        string `json:"site_id"`
	Env           string `json:"env"`
	Provider      string `json:"provider"`
	Target        string `json:"target,omitempty"`
	URL           string `json:"url,omitempty"`
	CreatedAt     string `json:"created_at"`
	WordPressPath string `json:"wordpress_path,omitempty"`
	Files         string `json:"files"`
	Database      string `json:"database"`
	TablePrefix   string `json:"table_prefix,omitempty"`
}

type envImportOptions struct {
	source      string
	database    string
	sourceURL   string
	tablePrefix string
	name        string
	dryRun      bool
	yes         bool
}

const envSnapshotSchema = 1

const wpCLIPasswordlessLoginWarning = "WARNING: option --ssl-verify-server-cert is disabled, because of an insecure passwordless login."

var (
	envSnapshotPromptString  = ui.PromptString
	envSnapshotConfirm       = ui.Confirm
	envSnapshotSelect        = ui.Select
	envSnapshotIsInteractive = envSnapshotInteractive
	envRemoteSyncConfirm     = ui.Confirm
	configSelectFn           = ui.Select
	configPromptString       = ui.PromptString
	configPromptSecret       = ui.PromptSecret
	configIsInteractive      = envwizard.IsInteractiveTerminal
)

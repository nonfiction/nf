# nf project specification

This document defines the project model, current implementation, and roadmap for `nf`.

Use:

* `README.md` for human CLI usage.
* `AGENTS.md` for repo shortcuts and implementation gotchas.
* `SPEC.md` for the durable product model and phased roadmap.

## Purpose

`nf` is nonfiction’s internal CLI for standard agency WordPress theme work.

It should replace scattered per-project scripts with one command surface for:

* project metadata
* local WordPress development environments
* theme tasks and packaging
* provider inventory
* deploy targets
* remote site/env cache
* repo remotes
* guarded future deploy and sync workflows
* password derivation

`nf` is not a public WordPress framework. It is an internal workflow tool optimized for nonfiction’s hosting and theme practices.

## System contexts

There are two main contexts.

### Global/provider context

Global/provider context models remote infrastructure and hosted WordPress environments.

Hierarchy:

```text
provider -> target -> site -> env
```

Definitions:

* **provider**: external platform or service integration.
* **target**: deployable place. Examples: `kinsta`, `linode1`.
* **site**: WordPress site hosted on a target. Site IDs use `<site>.<target>`.
* **env**: remote environment for a site. A site has `live`; `staging` is optional and intentionally managed.

Remote env display IDs use:

```text
<site>.<target>:<env>
```

Examples:

```text
provider: linode
target:   linode1
site:     client.linode1
env:      live
display:  client.linode1:live
```

### Repo/local context

Repo/local context models the current project repository.

It includes:

* `nf.json`
* local WordPress dev env
* WordPress plugin bootstrap intent
* theme tasks
* theme packaging/artifact recipe
* repo-local remotes such as `production` or `staging`

Repo-local remotes map names to global remote site/env records:

```json
{
  "remotes": {
    "production": "client.linode1:live"
  }
}
```

Repo-local config must not store global provider inventory or secrets.

## Providers

Current providers:

* `dnsimple`
* `kinsta`
* `linode`

### DNSimple

DNSimple manages the configured base domain and its DNS records.

Config:

```json
{
  "base_domain": "nonfiction.dev",
  "dnsimple_account_id": "14"
}
```

Secrets:

```env
DNSIMPLE_TOKEN=
```

`NF_SERVER_DOMAIN` and `DNSIMPLE_ZONE_NAME` may remain as legacy fallbacks during migration, but new behavior should use `base_domain` in `config.json`.

`DNSIMPLE_ACCOUNT_ID` must not be set in `.env`. The DNSimple provider determines the account ID from `/v2/whoami` using `DNSIMPLE_TOKEN` and saves it as `dnsimple_account_id` in `config.json`.

`nf provider check dnsimple` must:

* call DNSimple `/v2/whoami`
* save the fetched account ID to `config.json`
* read the configured `base_domain` zone
* fail clearly if the zone cannot be read
* write DNSimple account and zone metadata to `providers.json`
* write zero targets

DNSimple is not a deploy target provider in this model.

### Kinsta

Kinsta is canonical for Kinsta-hosted sites and envs.

Secrets:

```env
KINSTA_API_KEY=
```

`nf provider check kinsta` must:

* call Kinsta `/v2/validate`
* write company/API-key metadata to `providers.json`
* write one target named `kinsta`

Kinsta records should use Kinsta IDs from API state. Kinsta must not assume Linode-specific SSH fields, `nonfiction.dev` hostnames, or Linode provisioning/delete paths.

### Linode

Linode is canonical for Linode-hosted targets.

Secrets:

```env
LINODE_TOKEN=
# LINODE_CLI_TOKEN accepted as convenience fallback
```

`nf provider check linode` must:

* read Linode profile metadata
* list Linode instances
* treat instances tagged `nf` as targets
* write target records to `providers.json`

Linode-hosted site/env truth is intended to live on each target at:

```text
/var/lib/nf/sites.json
```

`nf site refresh` should discover sites by reading that file over SSH as the standard user.

Linode targets also write non-secret target metadata to:

```text
/var/lib/nf/target.json
```

That file includes database UI metadata such as URL, username, engine version, and derived credential identity/purpose, but never the raw password. The database UI is exposed at `https://<db-user>.<target-hostname>/` through the target wildcard certificate. The shared database access MySQL user is created during target provisioning and granted privileges per site-env database during site creation; site creation refuses a DB username that would collide with the shared database access MySQL user.

## State and config layout

Config root:

```text
~/.config/nf/
  config.json
  .env
```

State/cache root:

```text
~/.local/state/nf/
  providers.json
  sites.json
  projects.json
```

Generated data root:

```text
~/.local/share/nf/
  envs/<project-slug>/
  snapshots/local/<project-slug>/<snapshot-name>/
  snapshots/remote/<env-id-slug>-YYYY-MM-DD-HHMMSS/
```

Overrides:

```sh
NF_CONFIG_HOME=/path/to/nf-config
NF_STATE_HOME=/path/to/nf-state
NF_DATA_HOME=/path/to/nf-data
```

Rules:

* `config.json` stores non-secret config.
* `.env` stores secrets and account values.
* Docker local env defaults in `config.json` include `docker_user`, `docker_db_image`, and `docker_wordpress_image`.
* State cache is disposable.
* Provider truth is canonical remotely.
* Project repos track repo-local metadata only.
* Secrets must not be written to `nf.json`, state cache, generated env metadata, docs, or tests.

## Command surface

Current always-available command groups:

```text
nf init
nf provider ...
nf target ...
nf site ...
nf config ...
nf password ...
```

Project-only command groups are available when the current repo has `nf.json` next to `.git`:

```text
nf remote ...
nf theme ...
nf env ...
nf alias ...
```

Remote env operations stay under `nf site ...` as `nf site list --envs`, `nf site show <site:env>`, `nf site shell <site:env>`, `nf site wp <site:env>`, `nf site cache`, `nf site repair`, `nf site snapshot ...`, and `nf site export ...`.

Old public routes intentionally removed:

```text
nf server ...
nf instance ...
nf up/down/logs/reset/info/shell/wp
```

Do not add old compatibility routes unless explicitly requested.

## Current command status

### Done / working

* [x] `nf init`
* [x] `nf provider list`
* [x] `nf provider show <provider>` reads `providers.json`
* [x] `nf provider check <provider>` read-only API healthcheck and structured cache write
* [x] `nf target refresh` refreshes target provider metadata
* [x] `nf target list`
* [x] `nf target show <target>`
* [x] `nf target password [target] [--root|--db]`
* [x] `nf target add linode <name>` create/ensure target scaffold
* [x] `nf target remove <target>` remove an empty Linode target
* [x] `nf site add <target> <site> [--with-staging] [--password-version <version>] [--kinsta-slug <slug>]` create live env scaffolding by default, with optional staging, password-version override, and Kinsta provider-slug override
* [x] `nf site staging status/add/remove` manage optional staging env lifecycle
* [x] `nf site refresh` discovers remote site/env records from cached Kinsta and Linode targets
* [x] `nf site list [--refresh] [--envs]`
* [x] `nf site show <site-id-or-alias-or-env-id>`
* [x] `nf site shell <env-id>`
* [x] `nf site wp <env-id> -- <args>`
* [x] `nf site password [site|env] [--wp|--db|--basicauth]`
* [x] `nf domain list [site|env|remote]` shows cached domain inventory
* [x] `nf domain add <env|remote> <domain> [domain...]` attaches public domains without mutating public DNS or primary state
* [x] `nf domain check <env|remote> [domain...]` reports provider, DNS, HTTP, and HTTPS readiness
* [x] `nf domain primary <env|remote> <domain>` launches a primary public domain
* [x] `nf domain remove <env|remote> <domain> [domain...]` removes public-domain bindings after domain renames or target moves
* [x] `nf site remove [site]` remove a whole site
* [x] `nf remote add [name] [env-id]` with cache validation and prompts for omitted values
* [x] `nf remote show <name>`
* [x] `nf remote remove <name>`
* [x] `nf remote list`
* [x] `nf alias list/status/sync/add/remove`
* [x] `nf theme tasks`
* [x] `nf theme package`
* [x] direct repo-theme tasks from `nf.json`
* [x] `nf env up [--rebuild]`
* [x] `nf env down`
* [x] `nf env password [remote] [--wp|--db|--basicauth]`
* [x] `nf env logs [remote]`
* [x] `nf env reset [--rebuild]`
* [x] `nf env show`
* [x] `nf env shell`
* [x] `nf env wp -- <args>`
* [x] `nf define list/status/sync/add/remove`
* [x] `nf plugin list`
* [x] `nf plugin add <plugin> [--source <source>] [--manual] [--note <note>] [--no-activate] [--no-auto-update]`
* [x] `nf plugin remove <plugin>`
* [x] `nf plugin status [remote]`
* [x] `nf plugin diff [remote]`
* [x] `nf plugin install [remote] [--dry-run] [--yes]`
* [x] `nf env snapshot add [name]`
* [x] `nf env snapshot list`
* [x] `nf env snapshot import [remote] [--name name]`
* [x] `nf env snapshot use [name] [--yes]`
* [x] `nf env snapshot use --remote <remote> [--name name] [--yes]`
* [x] `nf env snapshot remove [name]`
* [x] `nf env snapshot prune [--keep N] [--dry-run] [--yes]`
* [x] `nf site snapshot <env> [--output path] [--dry-run]`
* [x] `nf site snapshot list/remove/prune`
* [x] `nf site export <env> [--output path] [--dry-run]`
* [x] `nf env import <source> [--db path] [--source-url url] [--name name] [--dry-run] [--yes]`
* [x] `nf config init`
* [x] `nf config set-base-domain <domain>`
* [x] `nf config set-default-wp-email <email>`
* [x] `nf config set-default-wp-user <user>`
* [x] `nf config set-basicauth-default-user <user>`
* [x] `nf config set-db-default-user <user>`
* [x] `nf config set-docker-db-image <image>`
* [x] `nf config set-docker-wordpress-image <image>`
* [x] `nf config set-docker-user <user>`
* [x] `nf config set-kinsta-default-php <version>`
* [x] `nf config set-kinsta-default-region <region>`
* [x] `nf config set-linode-default-region <region>`
* [x] `nf config set-linode-default-type <type>`
* [x] `nf config set-linode-default-image <image>`
* [x] `nf config set-linode-default-user <user>`
* [x] `nf config show`
* [x] `nf password set-salt <salt>`
* [x] `nf password show-salt`
* [x] `nf password derive <scope> <value...> [--password-version N]`
* [x] `nf password age-identity`: deterministically derive the agency age identity from `NF_PASSWORD_SALT`, materialize it securely under config home, and print only its path
* [x] `nf password age-recipient`: ensure the agency age identity and print only its public recipient

### Guarded / destructive

These commands are implemented, but intentionally guarded because they touch remote hosts or sync mutable WordPress data:

* [x] `nf site shell <env-id>`: validates cache, previews SSH, then executes the remote shell command
* [x] `nf site wp <env-id> -- <cmd>`: validates cache, previews SSH/wp-cli, then executes remote wp-cli
* [x] `nf env logs <remote>`: validates repo remote/cache, previews SSH, ensures `wp-content/debug.log` exists, then tails it
* [x] `nf env password <remote> [--wp|--db|--basicauth]`: resolves repo remote/cache and prints only the selected password
* [x] `nf plugin install <remote>`: validates repo remote/cache, prints a reviewable plugin plan, and asks for confirmation unless `--yes` is passed
* [x] `nf site export <env-id>`: validates cache, exports full remote WordPress filesystem plus database dump as a portable handoff directory
* [x] `nf env import <source>`: creates a local import snapshot, creates a pre-restore safety snapshot, and restores external WordPress data into the local env only
* [x] `nf define sync <remote>`: validates repo remote/cache and patches configured `wp-config.php` defines through stdin with redacted command output and atomic no-backup writes; it owns only the nf-managed project define block, prunes removed configured entries from that block, preserves manual constants outside it, migrates legacy per-define nf markers, rejects provider-owned constants from project defines, and blocks sync when configured constants also exist outside the managed block
* [x] `nf env push <remote>`: validates repo remote/cache, prints a reviewable plan, and syncs with execute/confirmation gates
* [x] `nf env pull <remote>`: validates repo remote/cache, prints a reviewable plan, and syncs with execute/confirmation gates

## Provider inventory flow

Target discovery starts with provider checks:

```sh
nf provider check dnsimple
nf provider check kinsta
nf provider check linode
```

These write structured provider metadata to:

```text
~/.local/state/nf/providers.json
```

Target commands read from that cache:

```sh
nf target refresh
nf target list
nf target show linode1
```

`nf refresh` is the top-level best-effort refresh: it runs all provider checks, including DNSimple, then refreshes site/env records from whatever target cache is available. It attempts all phases and exits non-zero if any phase fails.

`nf target refresh` runs configured target-provider health checks and rewrites those provider records so added or removed targets are reflected in the cache.

`nf site refresh` then fans out from the cached target list. It must not refresh providers directly.

## Site/env cache flow

Current site/env cache file:

```text
~/.local/state/nf/sites.json
```

Current readers:

* `nf site list`
* `nf site show`
* `nf site list --envs`
* `nf site show <site:env>`
* `nf remote add`
* `nf remote show`
* `nf env push/pull` guarded sync with dry-run/execute mode
* `nf site shell/wp` remote command execution
* `nf env logs [remote]` remote debug log tailing
* `nf env password [remote]` local/remote password lookup
* `nf domain add/check/primary/remove` public-domain readiness, launch, and retirement state

Desired refresh behavior:

1. Load cached targets.
2. For each target, discover site/env truth by provider-specific method.
3. Normalize records into `sites.json`.
4. Never treat `sites.json` as canonical source of truth.

Provider-specific desired discovery:

* DNSimple: no sites/envs.
* Kinsta: use Kinsta API site/env/domain endpoints. Prefer the canonical `nf` project slug inferred from attached internal domains under `kinsta.<base_domain>`; fall back to the Kinsta provider slug when no `nf` internal domain is present.
* Linode: read `/var/lib/nf/sites.json` over SSH from each target.

Public launch domains are provider/env state, not repo remote identity. `nf domain add` attaches/configures hostnames and prints the DNS records clients must create, but does not mutate client DNS. `nf domain primary` sets the primary public URL for the env and preserves the generated internal hostname as fallback state when known. Apex/www pairing is explicit through positional domains so arbitrary subdomain launches such as `reports.client.com` are not accidentally coupled to `client.com`. `nf domain list` shows full env IDs and columns for `role` (`primary` or `secondary`), `management` (`internal` or `external`), and `status` (`active`, `verified`, `unverified`, or `pending`). The generated provider hostname is internal and is primary only until an external primary is set; after that it remains an internal secondary fallback.

For Kinsta, the generated internal hostname also anchors the canonical `nf` project slug. This matters when the Kinsta provider slug must differ from the repo `project.slug`: cache records use `project_slug` and `site_id` from the canonical slug, while `kinsta.slug` stores the provider slug. `nf site add kinsta acme --kinsta-slug acmeinc` should create/adopt Kinsta site `acmeinc`, attach `acme.kinsta.<base_domain>`, cache the site as `acme.kinsta`, and preserve an existing public primary domain. The `nf` internal Kinsta domain only becomes primary when the current Kinsta primary is still a `*.kinsta.cloud` default domain.

Every env should have exactly one primary domain and zero or more secondaries. `nf domain add` attaches external domains as secondaries that redirect to the current primary or internal fallback. `nf domain primary` asks for launch approval before waiting, validates the provider route required by the exact requested hostname, and runs the primary launch automatically as soon as checks pass. Kinsta readiness follows bare, CNAME, or nf-managed identity routing without assuming an apex/`www` pair and must not treat healthy HTTP/TLS from an old host as proof that DNS points to Kinsta. It must not prompt again after checks pass; the default behavior is unattended wait-then-cutover. If checks never pass before the timeout, it exits without changing primary state. `--force` is the explicit bypass for launching immediately without readiness checks.

`nf domain remove` retires public-domain bindings when domains are renamed or moved between targets. Linode removal deletes nf-managed per-domain public vhosts, scripts, certbot units, and local/remote domain metadata, then resets the cached current URL to the generated internal fallback when the removed domain was primary. Kinsta removal deletes non-primary domains from the Kinsta environment and refuses to remove the current primary domain. Public DNS remains client-managed.

Linode public domains default to direct/DNS-only origin behavior: public DNS is expected to point at the target IP and a public Let's Encrypt certificate is issued with HTTP-01 for each external domain. Secondary Linode domains 302 redirect to the current primary. Domain names must be unique within a Linode target because nginx rejects duplicate `server_name` values; target-side updates lock and validate `/var/lib/nf/sites.json` before writing per-domain vhosts. `--proxy cloudflare` is the explicit Linode mode for Cloudflare orange-cloud domains using Cloudflare SSL/TLS `Full (strict)`: public DNS must resolve to Cloudflare IP ranges, certbot continues to issue/renew public-hostname Let's Encrypt origin certificates only after Cloudflare-fronted ACME challenge reachability is confirmed, `nf` skips public origin-IP DNS matching, and checks direct origin HTTPS separately from Cloudflare edge HTTPS.

## Project metadata model

Project metadata lives in:

```text
nf.json
```

Tracked fields include:

* manifest version
* project slug and password version
* non-empty WordPress theme checklist
* WordPress plugin bootstrap intent
* WordPress `wp-config.php` define intent, with secrets referenced by env var instead of stored in the repo
* optional local env overrides
* optional repo-theme package recipe and task map
* optional root-level alias map for paths under `wp-content`
* repo remotes

Example shape:

```json
{
  "version": 2,
  "project": {
    "slug": "client",
    "password_version": 0
  },
  "wordpress": {
    "themes": [
      {
        "slug": "client",
        "source": "repo",
        "path": "theme",
        "package": {
          "output": "dist/client-v{version}.zip"
        },
        "tasks": {
          "build": "npm run build",
          "lint": ["npm", "run", "lint"],
          "dev": {
            "description": "Start the theme development server",
            "run": ["npm", "run", "dev"]
          }
        }
      },
      "twentytwentyfive",
      "twentytwentyfour",
      "twentytwentythree",
      {
        "slug": "paid-parent-theme",
        "source": "cache",
        "auto_update": false
      }
    ],
    "plugins": [
      "stream",
      "wp-crontrol",
      {
        "slug": "acf-pro",
        "source": "$NF_PLUGIN_ACF_PRO_ZIP",
        "activate": true,
        "auto_update": true
      }
    ],
    "defines": [
      {
        "name": "SOME_PLUGIN_LICENSE_KEY",
        "env": "CLIENT_PLUGIN_LICENSE_KEY"
      },
      {
        "name": "OTGS_INSTALLER_SITE_KEY_WPML",
        "values": {
          "production": { "env": "CLIENT_WPML_SITE_KEY" },
          "default": { "env": "CLIENT_WPML_STAGING_SITE_KEY" }
        }
      }
    ],
    "aliases": {
      "files": "wp-content/uploads/public/files",
      "annual-report-2026": "wp-content/uploads/public/annual-report-2026"
    }
  },
  "local": {
    "compose": "docker compose -f docker-compose.local.yml",
    "wordpress_service": "cms",
    "uploads_path": ".nf-uploads",
    "admin_user": "client-admin",
    "ports": {
      "wordpress": 9080,
      "mailpit": 9025,
      "db": 9081
    }
  },
  "remotes": {
    "production": "client.linode1:live"
  }
}
```

The root `version` must be exactly `2`. `project` contains only `slug` and `password_version`. `wordpress.themes` is required and non-empty; `wordpress.plugins`, `wordpress.defines`, and `wordpress.aliases` are optional. `local` and `remotes` are optional.

## Theme workflow

Theme task rules:

* tasks come from the `tasks` map on the one `wordpress.themes` entry with `source: "repo"`
* string tasks run through `sh -lc`
* argv array tasks execute directly
* object tasks contain an optional `description` and a `run` value in string or argv array form
* passthrough args follow `--`
* print the command preview before execution

Packaging rules:

* `nf theme package` builds a clean staged artifact instead of zipping the development checkout as-is
* staging copies runtime theme files to a temporary directory and does not mutate the working project's `vendor/`
* when `composer.json` exists, staging runs `composer install --no-dev --no-interaction --prefer-dist --optimize-autoloader --no-progress` so the artifact is ready to upload/install/activate with `vendor/autoload.php` and runtime Composer packages from `require`, while excluding `require-dev` packages and dev-only Composer tooling binaries such as PHP-CS-Fixer, PHPCS, and PHPCBF
* it does not run npm or asset builds first
* if `package.json` has a `build` script, packaging requires built files under `dist/` or `assets/dist/` and fails clearly when they are missing
* package archives exclude obvious development-only files such as `node_modules`, editor config, formatter/linter/static-analysis config, npm manifests and lockfiles, common frontend tooling config, and Composer manifest files after staging
* package archives use the configured repo theme slug as the zip root directory, even when source files live in a differently named repo theme path
* deploy artifacts must include built files when needed, such as `vendor/autoload.php`, `dist/`, or `assets/dist/`
* the repo theme's optional `package.output` may contain `{version}`
* when `package.output` is omitted, it defaults to `dist/<project-slug>-v{version}.zip`
* `{version}` resolves from the repo theme `style.css` `Version:` first, then the repo theme `package.json` `version`
* fail clearly if neither version source exists

Deploy rules:

* deploy UX stays `nf theme deploy <remote> [--dry-run] [--restart]`
* deploy is a one-command packaged release deploy, not manual WordPress zip upload
* deploy uses the same packaging behavior as `nf theme package`; it stages Composer production dependencies but does not run npm or asset builds automatically
* deploy installs configured non-repo themes before uploading the repo theme release when needed for parent-theme dependencies
* direct in-place source rsync to the active theme directory is superseded
* remote releases live under `wp-content/themes/.nf-releases/<repo-theme-slug>/<release-id>/`
* active repo theme files live at `wp-content/themes/<repo-theme-slug>/`
* current deploy copies the fully extracted release into the active theme directory instead of making it a symlink, to keep Kinsta/Linode host and WordPress behavior boring
* release metadata is recorded at `wp-content/themes/.nf-releases/<repo-theme-slug>/releases.json` without secrets
* deploy prunes remote release storage after success, keeping the last 5 distinct releases and their matching uploaded artifacts
* deploy also removes stale extraction/temp release dirs under `.nf-releases/<repo-theme-slug>/`
* after switching the release, deploy regenerates WordPress rewrite rules; Kinsta deploys then clear site cache through the Kinsta API, and `--restart` restarts the environment PHP engine before that cache clear
* rollback UX is `nf theme rollback <remote> [--dry-run]`
* rollback selects the previous distinct `release_id` from remote `releases.json`, copies that release back into the repo theme directory, runs wp-cli activation for the first configured theme, and appends a rollback metadata entry
* rollback does not rebuild or upload artifacts
* rollback regenerates rewrite rules and always restarts Kinsta PHP before clearing site cache after switching releases

## Root aliases

Aliases expose existing WordPress content from `wp-content` at root-level URL paths by managing webroot symlinks.

Config:

```json
{
  "wordpress": {
    "aliases": {
      "files": "wp-content/uploads/public/files",
      "annual-report-2026": "wp-content/uploads/public/annual-report-2026"
    }
  }
}
```

Rules:

* alias UX is `nf alias list`, `nf alias status [remote]`, `nf alias sync [remote]`, `nf alias add <alias> <target>`, and `nf alias remove <alias>`
* alias names are top-level URL path names stored without a leading slash and displayed with one
* targets are document-root-relative paths and must be `wp-content` or descendants
* existing target symlinks are resolved before validation; targets that resolve outside `wp-content` are unsafe
* status reports missing symlinks, missing targets, real-file or real-directory conflicts, wrong symlink targets, and stale root symlinks
* sync creates or updates configured root symlinks and prunes stale root symlinks
* sync never overwrites or removes real files/directories and exits non-zero when unsafe paths prevent reconciliation
* aliases do not upload files, package artifacts, protect content, route through PHP, activate themes, update the database, or manage WordPress uploads

## Local env model

Built-in env commands work with built-in defaults when `local` is absent from `nf.json`. Use `local` only for non-default overrides.

Current built-ins:

* `up`
* `up --rebuild`
* `down`
* `logs`
* `logs [remote]`
* `password [remote] [--wp|--db|--basicauth]`
* `reset`
* `reset --rebuild`
* `show`
* `shell`
* `sh`
* `wp`
* `theme list`
* `theme add <theme> [--source <source>] [--path <path>] [--auto-update] [--note <note>]`
* `theme remove <theme>`
* `theme status [remote]`
* `theme diff [remote]`
* `theme install [remote] [--dry-run] [--yes]`
* `theme cache add <theme> <zip>`
* `theme cache save <theme>`
* `theme cache list`
* `theme cache show <theme>`
* `plugin list`
* `plugin add <plugin> [--source <source>] [--manual] [--note <note>] [--no-activate] [--no-auto-update]`
* `plugin remove <plugin>`
* `plugin status [remote]`
* `plugin diff [remote]`
* `plugin install [remote] [--dry-run] [--yes]`

Rules:

* local ports are derived deterministically from project slug
* `local.ports.wordpress` and `local.ports.mailpit` may override individually
* `local.ports.db` may override the local database UI port
* zero or missing ports fall back to derived ports
* `local.compose`, `local.wordpress_service`, `local.uploads_path`, and `local.admin_user` override their built-in defaults only when needed
* `nf env up` should be idempotent
* `nf env up --rebuild` rebuilds the generated WordPress image before starting Compose
* `nf env reset --rebuild` recreates the env after rebuilding the generated WordPress image
* `nf env up` preflights WordPress, Mailpit, and database UI host ports before Docker Compose starts
* `nf env up` configures WordPress to send local mail through Mailpit
* generated WordPress config enables `WP_DEBUG` and `WP_DEBUG_LOG` and disables debug display
* generated local env includes WordPress, MariaDB, Mailpit, and database UI containers
* generated local WordPress image includes useful CLI tools and wp-cli
* generated local WordPress image runs Apache/PHP as `docker_user` so bind-mounted uploads stay manageable by the host developer user
* `local.uploads_path` is the managed local WordPress media bind mounted at `wp-content/uploads`
* `nf env up` creates a project-root `uploads` symlink to the managed local uploads directory; `nf env down` removes only that managed symlink
* internal local zip handoffs use generated `.nf-transfer` storage mounted at `/env/uploads`; that path is not the WordPress media library
* Docker DB and WordPress image defaults can be overridden by `docker_db_image` and `docker_wordpress_image` in global config
* local shell/wp-cli user defaults to `docker_user` from global config, falling back to `nonfiction`
* `nf env show` prints paths, compose project name, DB URL, Mailpit URL, and WordPress URLs without starting Docker
* `nf env logs` tails Docker logs for the local WordPress service
* `nf env logs <remote>` resolves a repo remote and tails remote `wp-content/debug.log` over SSH after creating the file if needed
* `nf env password [remote] [--wp|--db|--basicauth]` prints only the selected local or remote env password; `--wp` is the default
* `nf site password [site|env] [--wp|--db|--basicauth]` accepts env refs for `--db`; site refs are required for `--wp` and `--basicauth`
* `wordpress.themes` is a non-empty bootstrap checklist, not a full theme lifecycle manager
* the first `wordpress.themes` entry is the theme nf activates
* string theme entries install from wordpress.org and default auto-updates to false
* object theme entries require `slug`, support `source`, `path`, `note`, and `auto_update`; `path` is only valid for repo themes
* theme `source` may be `wordpress.org`, `repo`, `cache`, a zip URL/path, or an env-var-backed value such as `$NF_THEME_PARENT_ZIP`
* new project metadata starts with the repo theme first, followed by WordPress 7.0 bundled wordpress.org themes `twentytwentyfive`, `twentytwentyfour`, and `twentytwentythree`
* `wordpress.themes` may contain zero repo themes when the active theme is installed from wordpress.org, cache, or another source; it may contain at most one repo theme, and a repo theme defaults to `path: "theme"`
* local envs bind mount the configured repo theme into `wp-content/themes/<slug>` using the real theme slug, never a generic mount slot
* `nf theme add <theme>` appends to `wordpress.themes` in `nf.json`, creates the array if missing, rejects duplicate slugs, and does not install anything; `--source repo` requires an existing repo path and does not scaffold files
* `nf theme activate <theme>` moves an existing configured theme to the top of `wordpress.themes`; it mutates desired state only and does not run WP-CLI directly
* `nf theme remove <theme>` removes a configured theme from `nf.json`, rejects missing slugs, refuses to remove the last configured theme, and does not uninstall anything
* `nf theme status [remote]` compares configured themes against local or remote WordPress state and reports installed, active, and auto-update status
* `nf theme diff [remote]` reports needed install/activate/auto-update changes for configured themes and installed themes that are not configured in `nf.json`; it mutates nothing, exits 0 when configured themes match and no extras are installed, and exits 2 when drift exists
* `nf theme install` with no remote targets the local env
* `nf theme install <remote>` validates the repo remote/cache, prints a remote theme plan, and asks for yes/no confirmation unless `--yes` is passed
* `nf theme install <remote> --dry-run` previews only and does not run SSH
* remote theme installs run WP-CLI on the remote host; URL sources must be reachable from that host, and local zip/cache/repo sources are uploaded to a temporary remote directory before install and cleaned up afterward
* theme install is idempotent: it installs only missing configured themes, activates only the first configured theme when inactive, and enables native WordPress auto-updates only for non-repo themes that request it; it does not update, remove, pin, disable auto-updates, or manage licenses
* `wordpress.plugins` is a bootstrap checklist, not a full plugin lifecycle manager
* string plugin entries install from wordpress.org, activate, and enable auto-updates by default
* object plugin entries require `slug`, support `source`, `install`, `note`, and `auto_update`, and default `install`, `activate`, and `auto_update` to true
* plugin `source` may be a wp.org marker, zip URL/path, or env-var-backed value such as `$NF_PLUGIN_ACF_PRO_ZIP`
* `nf plugin add <plugin>` appends to `wordpress.plugins` in `nf.json`, creates the array if missing, rejects duplicate slugs, and does not install anything
* `nf plugin remove <plugin>` removes a configured plugin from `nf.json`, rejects missing slugs, and does not uninstall anything
* `nf plugin install` with no remote targets the local env
* `nf plugin status [remote]` compares configured plugins against local or remote WordPress state and reports installed, active, and auto-update status
* `nf plugin diff [remote]` reports needed install/activate/auto-update changes, missing manual plugins, and installed plugins that are not configured in `nf.json`; it mutates nothing, exits 0 when configured plugins match and no extras are installed, and exits 2 when drift exists
* `nf plugin install <remote>` validates the repo remote/cache, prints a remote plugin plan, and asks for yes/no confirmation unless `--yes` is passed
* `nf plugin install <remote> --dry-run` previews only and does not run SSH
* remote plugin installs run WP-CLI on the remote host; URL sources must be reachable from that host, and local zip sources are uploaded to a temporary remote directory before install and cleaned up afterward
* plugin install is source-aware: remote repo plugins replace the installed copy from current repo source on every explicit install; cached plugins replace an installed copy only when the cache package's declared WordPress plugin version is newer; other sources install only missing plugins; it activates inactive plugins when requested and enables native WordPress auto-updates only when not already enabled; it does not install manual plugins, update WordPress.org/URL plugins, remove, pin, disable auto-updates, or manage licenses
* secrets, license keys, and private signed URLs must not be stored directly in `nf.json`
* generated env scaffolding stays under `NF_DATA_HOME`, not in project repos

### Planned shared artifact cache

`nf` may support an optional SSH-backed team cache for private or paid plugin and theme zips. This is an artifact cache, not project metadata, provider inventory, or shared application state.

Planned model:

* the SSH host alias and remote base path belong in global config, never `nf.json`
* SSH authentication remains in the user's SSH configuration; nf does not store SSH credentials
* the remote layout mirrors the local cache layout at `plugins/<slug>/<slug>.zip` and `themes/<slug>/<slug>.zip`
* `source: "cache"` remains explicit; nf must not silently fall back from a wordpress.org source to the shared cache
* when an install explicitly requests `source: "cache"`, nf uses the local zip first and may hydrate a missing local zip from the configured shared cache
* a zip missing from both local and shared caches remains an error
* planned explicit operations are `plugin cache fetch/publish/list --remote` and equivalent `theme cache` operations
* fetching is missing-only by default; publishing is explicit and does not happen automatically after local cache add/save
* publishing a different artifact over an existing remote zip requires explicit replacement and confirmation
* transfers use temporary files, SHA-256 verification, archive/path validation, and atomic rename
* removing a local cache entry does not remove the shared artifact
* if the shared server becomes the only durable copy of an artifact, it needs backup and version-retention policy beyond the cache feature itself

### Planned plugin and theme pulls

`nf` may support pulling one installed plugin or theme from a configured Kinsta or Linode repo remote back to the developer's machine. This is separate from `nf env pull`, which moves the database and mutable `wp-content`, and from shared artifact cache fetches, which copy an already-published zip between caches.

Primary use cases:

* refresh a local cache zip after a paid plugin or theme updates itself on a remote site
* recover theme or plugin changes made directly on a remote site by a third-party developer
* bring those remote source changes into a clean Git worktree for review, local testing, a normal commit, and a later explicit install or deploy

Planned command shape:

```text
nf plugin pull [remote] [plugin]
nf theme pull [remote] [theme]
```

Omitted values may use interactive selectors. Non-interactive execution requires an explicit remote and plugin or theme. The configured `source` determines the local destination:

* `source: "cache"` downloads the installed remote directory, packages it under the configured slug, validates it, and atomically replaces the local cache zip
* `source: "repo"` mirrors the installed remote directory into the configured repo source path so the resulting changes are visible to Git
* other source types are rejected initially rather than guessing whether to mutate a cache or repo path

Repo-source safety rules:

* require the entire Git worktree to be clean before downloading or applying remote files
* download into temporary local storage and validate the complete transfer before mutating the repo
* recheck that the worktree is still clean immediately before applying the staged transfer
* print an itemized plan showing additions, modifications, and deletions before execution
* mirror remote additions, modifications, and deletions for deployable/runtime files
* preserve local files and directories intentionally excluded by that resource's packaging rules, such as theme package manifests, lockfiles, development tooling config, and development-only directories
* leave the worktree dirty after a successful pull; nf does not stage, commit, run build tasks, install, or deploy the result

General transfer rules:

* cache pulls do not require a clean Git worktree because they write only under `NF_DATA_HOME`
* validate configured slugs and remote paths, reject unsafe symlinks or traversal, and keep transfers inside the expected `wp-content/plugins/<slug>` or `wp-content/themes/<slug>` directory
* use temporary files/directories, archive validation, SHA-256 verification where an archive is produced, and atomic replacement of cache zips
* show the selected project, provider, environment, remote path, local destination, source type, and execution mode in a reviewable preflight
* support `--dry-run`; interactive execution prompts for confirmation, while non-interactive execution requires `--execute --yes --non-interactive`
* do not update `nf.json`, plugin/theme activation state, licenses, or WordPress auto-update settings
* do not automatically publish a newly pulled cache zip to the planned shared artifact cache; publishing remains a separate explicit operation
* do not add generic plugin/theme `push` commands: remote `plugin/theme install` and `theme deploy` remain the outbound workflows

Snapshots:

* local env snapshots are stored under `NF_DATA_HOME/snapshots/local/<project-slug>/<snapshot-name>/`
* remote site snapshots are stored under `NF_DATA_HOME/snapshots/remote/<env-id-slug>-YYYY-MM-DD-HHMMSS/`
* local and remote snapshots contain `snapshot.json`, `database.sql.gz`, `wp-content.tar.gz`
* `wp-content.tar.gz` includes uploads/plugins/languages only; `wp-content/mu-plugins` are target-owned platform files and are skipped by env push/pull/import/snapshot restore
* local restore creates a pre-restore safety snapshot
* remote snapshots are imported into local snapshots before restore; direct restore from remote files is intentionally avoided

## Remote execution and sync safety

High-risk operations:

* production database push
* uploads push to production
* full site sync toward production
* overwriting live credentials
* overwriting live uploads
* destroying remote infrastructure

Future DB/uploads sync must:

* require explicit source and destination
* identify provider and environment
* preserve production passwords
* preserve sensitive options and users where possible
* avoid silently replacing live credentials with local/staging credentials
* print a reviewable plan
* require confirmation for destructive changes

Core safety rule:

```text
never silently clobber production credentials
```

## Phased roadmap

### Phase 1: Command surface cleanup

Goal: make the public command model match `provider -> target -> site` plus repo/local commands, with remote env operations hanging under `site`.

Status:

* [x] remove old public `server`/`instance` routes
* [x] remove top-level local env aliases
* [x] add `provider`, `target`, `site`, `remote`, `theme`, `env`, `config`, `password`
* [x] update docs/tests for current command surface

### Phase 2: Config/state split

Goal: keep non-secret config, secrets, disposable state, and generated data separate.

Status:

* [x] XDG config/state/data homes
* [x] `config.json` for non-secret config
* [x] `.env` for secrets/account values
* [x] `base_domain` moved to `config.json`
* [x] provider cache in `providers.json`
* [x] site cache in `sites.json`
* [x] local env data under `NF_DATA_HOME`

### Phase 3: Provider and target inventory

Goal: build a usable provider check and target cache.

Status:

* [x] `nf provider list`
* [x] `nf provider show <provider>` from cache
* [x] DNSimple read-only healthcheck and zone validation
* [x] Kinsta read-only healthcheck and `kinsta` target
* [x] Linode read-only healthcheck and tagged target discovery
* [x] `nf target list/show` from `providers.json`
* [x] `nf target add linode <name>` creates Linode targets with DNS, queued TLS retry, database UI at `https://<db-user>.<target-hostname>/`, a per-target `--db-user` override, and empty remote site inventory. Existing completed targets that predate the database UI are not reconciled in place by provider checks or target refresh.
* [x] legacy `servers.json` fallback during cache migration

### Phase 4: Site/env inventory

Goal: discover remote site/env records from targets.

Status:

* [x] `nf site refresh` fans out from cached targets
* [x] cached site/env readers
* [x] repo remote validation against cache
* [x] Linode target SSH reader for `/var/lib/nf/sites.json`
* [x] Kinsta site/env API reader
* [x] normalized `sites.json` writer from discovered records
* [x] stale Linode site/env pruning when a target disappears
* [ ] conflict/error reporting for stale or invalid target cache

### Phase 5: Remote execution

Goal: safely run remote shell and wp-cli through provider-aware env records.

Status:

* [x] `nf site shell`
* [x] `nf site wp`
* [x] `nf env logs [remote]`
* [x] Linode SSH execution adapter
* [x] Kinsta SSH/wp-cli execution adapter
* [x] command previews
* [ ] richer audit output

### Phase 6: Theme artifact deployment

Goal: deploy packaged theme artifacts through repo remotes.

Status:

* [x] theme task execution
* [x] theme packaging
* [x] repo remote model
* [x] packaged release deploy via `nf theme deploy <remote> [--dry-run] [--restart]`
* [x] Linode and Kinsta SSH/rsync artifact deploy paths
* [x] release metadata layout for rollback/history
* [x] public rollback command

### Phase 6b: Root aliases

Goal: expose existing WordPress content under root-level URL paths through managed webroot symlinks.

Status:

* [x] repo `wordpress.aliases` metadata
* [x] `nf alias list/status/sync/add/remove`
* [x] local and remote status/sync through project remotes
* [x] alias and target safety checks for traversal, reserved WordPress names, and targets escaping `wp-content`
* [x] sync creates/updates configured symlinks and prunes stale root symlinks without touching real files/directories

### Phase 7: Database/uploads sync

Goal: add guarded pull/push workflows.

Status:

* [x] guarded `nf env push [remote]`
* [x] guarded `nf env pull [remote]`
* [x] explicit source/destination planning
* [ ] production credential preservation
* [ ] uploads protections
* [x] confirmation gates for destructive direction
* [x] Kinsta sync adapter
* [x] Linode sync adapter

### Phase 8: Distribution and team polish

Goal: make `nf` comfortable for team-wide daily use.

Status:

* [ ] improved interactive selectors
* [ ] richer diagnostics for missing config/cache
* [ ] optional SSH-backed shared plugin/theme artifact cache
* [ ] remote-to-local plugin/theme pull workflows
* [ ] release/update workflow
* [ ] onboarding docs

No separate shared state sync is planned. The optional shared artifact cache does not become project or provider truth. Shared truth comes from provider APIs, Kinsta API, each Linode target's `/var/lib/nf/sites.json`, and deterministic password derivation from the agreed `NF_PASSWORD_SALT` plus the repo-local `project.password_version` when non-zero.

## Non-goals for now

* public/general-purpose WordPress framework behavior
* old command compatibility aliases
* hidden mutation of provider inventory from repo-local commands
* production sync without explicit review and confirmation
* storing secrets in project metadata or state cache

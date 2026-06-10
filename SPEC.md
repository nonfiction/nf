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
* **target**: deployable place. Examples: `kinsta`, `app1-linode`.
* **site**: WordPress site hosted on a target. Site IDs use `<site>-<target>`.
* **env**: remote environment for a site. A site has `live`; `staging` is optional and intentionally managed.

Remote env display IDs use:

```text
<env>-<site>-<target>
```

Examples:

```text
provider: linode
target:   app1-linode
site:     client-app1-linode
env:      live
display:  live-client-app1-linode
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
    "production": "client.app1-linode:live"
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
* State cache is disposable.
* Provider truth is canonical remotely.
* Project repos track repo-local metadata only.
* Secrets must not be written to `nf.json`, state cache, generated env metadata, docs, or tests.

## Command surface

Current public command groups:

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
nf public ...
```

Remote env operations stay under `nf site ...` as `nf site list --envs`, `nf site show <site:env>`, `nf site shell <site:env>`, `nf site wp <site:env>`, and `nf site snapshot ...`.

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
* [x] `nf target add linode <name>` create/ensure target scaffold
* [x] `nf target remove <target>` remove an empty Linode target
* [x] `nf site add <target> <site> [--with-staging]` create live env scaffolding by default, with optional staging
* [x] `nf site staging status/add/remove` manage optional staging env lifecycle
* [x] `nf site refresh` discovers remote site/env records from cached Kinsta and Linode targets
* [x] `nf site list [--refresh] [--envs]`
* [x] `nf site show <site-id-or-alias-or-env-id>`
* [x] `nf site shell <env-id>`
* [x] `nf site wp <env-id> -- <args>`
* [x] `nf site password [site]`
* [x] `nf site remove [site]` remove a whole site
* [x] `nf remote add [name] [env-id]` with cache validation and prompts for omitted values
* [x] `nf remote show <name>`
* [x] `nf remote remove <name>`
* [x] `nf remote list`
* [x] `nf public deploy <remote> [--dry-run] [--yes]`
* [x] `nf theme tasks`
* [x] `nf theme package`
* [x] direct theme tasks from `nf.json`
* [x] `nf env up`
* [x] `nf env down`
* [x] `nf env logs`
* [x] `nf env reset`
* [x] `nf env show`
* [x] `nf env shell`
* [x] `nf env wp -- <args>`
* [x] `nf env plugins list`
* [x] `nf env plugins add <plugin> [--source <source>] [--no-activate] [--no-auto-update]`
* [x] `nf env plugins remove <plugin>`
* [x] `nf env plugins status [remote]`
* [x] `nf env plugins diff [remote]`
* [x] `nf env plugins install [remote] [--dry-run] [--yes]`
* [x] `nf env snapshot add [name]`
* [x] `nf env snapshot list`
* [x] `nf env snapshot import [remote] [--name name]`
* [x] `nf env snapshot use [name] [--yes]`
* [x] `nf env snapshot use --remote <remote> [--name name] [--yes]`
* [x] `nf env snapshot remove [name]`
* [x] `nf env snapshot prune [--keep N] [--dry-run] [--yes]`
* [x] `nf site snapshot <env> [--output path] [--dry-run]`
* [x] `nf site snapshot list/remove/prune`
* [x] `nf config init`
* [x] `nf config set-base-domain <domain>`
* [x] `nf config set-default-wp-email <email>`
* [x] `nf config set-default-wp-user <user>`
* [x] `nf config set-kinsta-default-php <version>`
* [x] `nf config set-kinsta-default-region <region>`
* [x] `nf config set-linode-default-region <region>`
* [x] `nf config set-linode-default-type <type>`
* [x] `nf config set-linode-default-image <image>`
* [x] `nf config set-linode-default-user <user>`
* [x] `nf config show`
* [x] `nf password set-salt <salt>`
* [x] `nf password show-salt`
* [x] `nf password derive <scope> <value...>`

### Guarded / destructive

These commands are implemented, but intentionally guarded because they touch remote hosts or sync mutable WordPress data:

* [x] `nf site shell <env-id>`: validates cache, previews SSH, then executes the remote shell command
* [x] `nf site wp <env-id> -- <cmd>`: validates cache, previews SSH/wp-cli, then executes remote wp-cli
* [x] `nf env plugins install <remote>`: validates repo remote/cache, prints a reviewable plugin plan, and asks for confirmation unless `--yes` is passed
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
nf target show app1-linode
```

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

Desired refresh behavior:

1. Load cached targets.
2. For each target, discover site/env truth by provider-specific method.
3. Normalize records into `sites.json`.
4. Never treat `sites.json` as canonical source of truth.

Provider-specific desired discovery:

* DNSimple: no sites/envs.
* Kinsta: use Kinsta API site/env endpoints.
* Linode: read `/var/lib/nf/sites.json` over SSH from each target.

## Project metadata model

Project metadata lives in:

```text
nf.json
```

Tracked fields include:

* manifest version
* project slug/name/type
* WordPress/theme structure
* WordPress plugin bootstrap intent
* local env intent
* artifact recipe
* static public path deploy recipe
* repo remotes
* theme tasks

Example shape:

```json
{
  "version": 1,
  "project": {
    "slug": "client",
    "type": "wordpress-theme"
  },
  "wordpress": {
    "deploy_unit": "theme",
    "theme_slug": "client",
    "theme_path": "theme",
    "plugins": [
      "stream",
      "wp-crontrol",
      {
        "slug": "acf-pro",
        "source": "$NF_PLUGIN_ACF_PRO_ZIP",
        "activate": true,
        "auto_update": true
      }
    ]
  },
  "env": {
    "compose": "docker compose",
    "wordpress_service": "wordpress",
    "cli_service": "cli",
    "theme_mount_slug": "theme",
    "uploads_path": "uploads"
  },
  "artifact": {
    "path": "dist/client-v{version}.zip"
  },
  "public": {
    "paths": [
      {
        "source": "public/annual-report-2026",
        "path": "/annual-report-2026"
      }
    ]
  },
  "remotes": {
    "production": "client.app1-linode:live"
  },
  "tasks": {}
}
```

## Theme workflow

Theme task rules:

* tasks come from `nf.json` `tasks`
* string tasks run through `sh -lc`
* array tasks execute directly
* passthrough args follow `--`
* print the command preview before execution

Packaging rules:

* `nf theme package` builds a clean staged artifact instead of zipping the development checkout as-is
* staging copies runtime theme files to a temporary directory and does not mutate the working project's `vendor/`
* when `composer.json` exists, staging runs `composer install --no-dev --no-interaction --prefer-dist --optimize-autoloader --no-progress` so the artifact is ready to upload/install/activate with `vendor/autoload.php` and runtime Composer packages from `require`, while excluding `require-dev` packages and dev-only Composer tooling binaries such as PHP-CS-Fixer, PHPCS, and PHPCBF
* it does not run npm or asset builds first
* if `package.json` has a `build` script, packaging requires built files under `dist/` or `assets/dist/` and fails clearly when they are missing
* package archives exclude obvious development-only files such as `node_modules`, editor config, formatter/linter/static-analysis config, npm manifests and lockfiles, common frontend tooling config, and Composer manifest files after staging
* package archives use `wordpress.theme_slug` as the zip root directory, even when source files live in `wordpress.theme_path`
* deploy artifacts must include built files when needed, such as `vendor/autoload.php`, `dist/`, or `assets/dist/`
* `artifact.path` may contain `{version}`
* `{version}` resolves from `theme/style.css` `Version:` first, then `theme/package.json` `version`
* fail clearly if neither version source exists

Deploy rules:

* public UX stays `nf theme deploy <remote> [--dry-run]`
* deploy is a one-command packaged release deploy, not manual WordPress zip upload
* deploy uses the same packaging behavior as `nf theme package`; it stages Composer production dependencies but does not run npm or asset builds automatically
* direct in-place source rsync to the active theme directory is superseded
* remote releases live under `wp-content/themes/.nf-releases/<theme-slug>/<release-id>/`
* active theme files live at `wp-content/themes/<theme-slug>/`
* current deploy copies the fully extracted release into the active theme directory instead of making it a symlink, to keep Kinsta/Linode host and WordPress behavior boring
* release metadata is recorded at `wp-content/themes/.nf-releases/<theme-slug>/releases.json` without secrets
* deploy prunes remote release storage after success, keeping the last 5 distinct releases and their matching uploaded artifacts
* deploy also removes stale extraction/temp release dirs under `.nf-releases/<theme-slug>/`
* public rollback UX is `nf theme rollback <remote> [--dry-run]`
* rollback selects the previous distinct `release_id` from remote `releases.json`, copies that release back into the active theme directory, runs wp-cli activation, and appends a rollback metadata entry
* rollback does not rebuild or upload artifacts

## Static public artifacts

Static files that must live at non-WordPress URL paths can be tracked in the repo and deployed separately from the WordPress theme.

Convention:

```text
public/
  annual-report-2026/
    index.html
    assets/...
```

Config:

```json
{
  "public": {
    "paths": [
      {
        "source": "public/annual-report-2026",
        "path": "/annual-report-2026",
        "delete": false
      }
    ]
  }
}
```

Rules:

* public deploy UX is `nf public deploy <remote> [--dry-run] [--yes]`
* `public.paths` is explicit; `nf` never uploads every folder under `public/` by inference
* `source` must be a repo-relative directory and must not contain symlinks
* `path` must be an absolute URL path under the remote document root
* deploy refuses `/`, traversal, backslashes, and reserved WordPress paths such as `/wp-admin`, `/wp-content`, `/wp-includes`, and `/uploads`
* deploy uses rsync over the configured remote SSH path
* `delete: true` adds `rsync --delete` and requires `--yes` when not in dry-run mode
* public deploy does not package, build, activate WordPress themes, update the database, or manage uploads
* large or remote artifacts should be materialized into the configured repo `source` by a project task before `nf public deploy`
* HTTP crawling, archive download/extract, and rsync side-loaded source types are intentionally deferred

## Local env model

Built-in env commands come from `env` metadata in `nf.json`.

Current built-ins:

* `up`
* `down`
* `logs`
* `reset`
* `show`
* `shell`
* `wp`
* `plugins list`
* `plugins add <plugin> [--source <source>] [--no-activate] [--no-auto-update]`
* `plugins remove <plugin>`
* `plugins status [remote]`
* `plugins diff [remote]`
* `plugins install [remote] [--dry-run] [--yes]`

Rules:

* env ports are derived deterministically from project slug
* `env.ports.wordpress` and `env.ports.mailpit` may override individually
* zero or missing ports fall back to derived ports
* `nf env up` should be idempotent
* `nf env up` preflights WordPress and Mailpit host ports before Docker Compose starts
* `nf env show` prints paths, compose project name, and URLs without starting Docker
* `wordpress.plugins` is a bootstrap checklist, not a full plugin lifecycle manager
* string plugin entries install from wordpress.org, activate, and enable auto-updates by default
* object plugin entries require `slug`, support `source`, support `auto_update`, and default `activate` and `auto_update` to true
* plugin `source` may be a wp.org marker, zip URL/path, or env-var-backed value such as `$NF_PLUGIN_ACF_PRO_ZIP`
* `nf env plugins add <plugin>` appends to `wordpress.plugins` in `nf.json`, creates the array if missing, rejects duplicate slugs, and does not install anything
* `nf env plugins remove <plugin>` removes a configured plugin from `nf.json`, rejects missing slugs, and does not uninstall anything
* `nf env plugins install` with no remote targets the local env
* `nf env plugins status [remote]` compares configured plugins against local or remote WordPress state and reports installed, active, and auto-update status
* `nf env plugins diff [remote]` reports needed install/activate/auto-update changes and installed plugins that are not configured in `nf.json`; it mutates nothing, exits 0 when configured plugins match and no extras are installed, and exits 2 when drift exists
* `nf env plugins install <remote>` validates the repo remote/cache, prints a remote plugin plan, and asks for yes/no confirmation unless `--yes` is passed
* `nf env plugins install <remote> --dry-run` previews only and does not run SSH
* remote plugin installs run WP-CLI on the remote host; URL sources must be reachable from that host, and local zip sources are uploaded to a temporary remote directory before install and cleaned up afterward
* plugin install is idempotent: it installs only missing plugins, activates only inactive plugins when requested, and enables native WordPress auto-updates only when not already enabled; it does not update, remove, pin, disable auto-updates, or manage licenses
* secrets, license keys, and private signed URLs must not be stored directly in `nf.json`
* generated env scaffolding stays under `NF_DATA_HOME`, not in project repos

Snapshots:

* local env snapshots are stored under `NF_DATA_HOME/snapshots/local/<project-slug>/<snapshot-name>/`
* remote site snapshots are stored under `NF_DATA_HOME/snapshots/remote/<env-id-slug>-YYYY-MM-DD-HHMMSS/`
* local and remote snapshots contain `snapshot.json`, `database.sql.gz`, `wp-content.tar.gz`
* `wp-content.tar.gz` includes uploads/plugins/mu-plugins/languages only
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
* [x] `nf target add linode <name>` create/ensure Linode target with DNS, queued TLS retry, and empty remote site inventory
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
* [x] packaged release deploy via `nf theme deploy <remote> [--dry-run]`
* [x] Linode and Kinsta SSH/rsync artifact deploy paths
* [x] release metadata layout for rollback/history
* [x] public rollback command

### Phase 6b: Static public artifact deployment

Goal: deploy explicit static artifact directories to non-WordPress URL paths under a remote document root.

Status:

* [x] repo `public.paths` metadata
* [x] `nf public deploy <remote> [--dry-run] [--yes]`
* [x] path safety checks for root/traversal/reserved WordPress paths
* [ ] release metadata and rollback for public artifact deploys

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
* [ ] release/update workflow
* [ ] onboarding docs

No separate shared state sync is planned. Shared truth comes from provider APIs, Kinsta API, each Linode target's `/var/lib/nf/sites.json`, and deterministic password derivation from the agreed `NF_PASSWORD_SALT` plus the repo-local `project.password_version` when non-zero.

## Non-goals for now

* public/general-purpose WordPress framework behavior
* old command compatibility aliases
* hidden mutation of provider inventory from repo-local commands
* production sync without explicit review and confirmation
* storing secrets in project metadata or state cache

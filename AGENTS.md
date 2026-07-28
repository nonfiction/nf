# nf agent guide

Use this file for repo shortcuts and learned implementation gotchas. Put durable product model and roadmap in `SPEC.md`. Put human CLI usage in `README.md`.

## Project shape

* `nf` is a Go CLI.
* Executable entrypoint: `cmd/nf/main.go`.
* CLI dispatcher: `internal/cli.Run`.
* Primary always-visible command groups: `init`, `provider`, `target`, `site`, `config`, `password`.
* Project-only command groups: `remote`, `plugin`, `theme`, `env`, `alias`, `define`. They appear only when the current repo has `nf.json` next to `.git`.
* Remote env operations live under `site` (`site list --envs`, `site show <site:env>`, `site shell`, `site wp`, `site cache`, `site repair`, `site snapshot`, `site export`), not as a separate `site env` group.
* Do not re-add public `nf server ...`, `nf instance ...`, or top-level local env aliases (`nf up/down/logs/reset/info/shell/wp`) unless explicitly requested.

## Fast checks

```sh
go test ./...
go test ./internal/cli
```

CLI smoke checks:

```sh
go run ./cmd/nf --help
go run ./cmd/nf version
go run ./cmd/nf provider list
go run ./cmd/nf site list
go run ./cmd/nf site list --envs
```

Project-context smoke checks need an `nf` project repo with `nf.json` next to `.git`:

```sh
go run ./cmd/nf theme help
go run ./cmd/nf remote help
go run ./cmd/nf env help
go run ./cmd/nf alias help
go run ./cmd/nf define help
```

Provider checks call live APIs when credentials are present:

```sh
go run ./cmd/nf provider check dnsimple
go run ./cmd/nf provider check kinsta
go run ./cmd/nf provider check linode
```

Nix checks:

```sh
nix run .#nf -- --help
nix build .#nf -L
nix develop -c nf --help
```

Nix flakes use the git source snapshot. Stage newly added source files before trusting `nix run` or `nix build`; otherwise Nix may silently build without untracked Go files.

Release versions use date-based `YYYY.MM.DD.N` values. The source version lives in `internal/version/VERSION`. Build release artifacts with `scripts/release.sh [version]`; it writes stamped binaries and checksums under ignored `dist/`.

## Local paths and test isolation

Defaults:

```text
config: ~/.config/nf/
state:  ~/.local/state/nf/
data:   ~/.local/share/nf/
```

Use overrides in tests and smoke runs:

```sh
NF_CONFIG_HOME=/tmp/nf-config
NF_STATE_HOME=/tmp/nf-state
NF_DATA_HOME=/tmp/nf-data
```

Do not read or write the user's real config/state in tests. `internal/target/provision` has a `TestMain` guard for this because provision code can read `config.ConfigFile()`.

## Current cache files

```text
config.json     non-secret global config, including base_domain, dnsimple_account_id, basicauth_default_user, and db_default_user
.env            secrets/account tokens
providers.json  provider check metadata and targets
sites.json      cached remote site/env records
projects.json   disposable project cache if needed
plugins/        local plugin cache zips under NF_DATA_HOME
```

Local state is disposable. Provider truth is canonical remotely.

## Learned model facts

* `base_domain` belongs in `config.json`, not `.env`. Legacy `NF_SERVER_DOMAIN` can remain as fallback during migration.
* `dnsimple_account_id` belongs in `config.json`, fetched by DNSimple provider check from `DNSIMPLE_TOKEN`; do not set `DNSIMPLE_ACCOUNT_ID` in `.env`.
* Password salt is `NF_PASSWORD_SALT`; legacy `NF_SECRET_SALT` is migration-only fallback.
* `project.password_version` belongs in `nf.json`, defaults to `0`, is safe to commit, and rotates project/site derived passwords when set non-zero without changing `NF_PASSWORD_SALT`.
* `basicauth_default_user` belongs in `config.json`, defaults to `nonfiction`, and is used with a per-site derived `basic-auth` password.
* `db_default_user` belongs in `config.json`, defaults to `admin`, and is used for Linode target database UI HTTP Basic auth, the shared MySQL admin user, and the database UI subdomain label unless `nf target add linode --db-user` overrides it for that target. Legacy `adminer_default_user` may remain as fallback during migration.
* DNSimple provider check validates it can read the configured `base_domain` zone and writes zero targets.
* Kinsta provider check writes one target named `kinsta`.
* Linode provider check discovers targets from Linode instances tagged `nf`.
* `nf target remove <target>` removes an empty Linode target.
* `nf target list/show` read targets from `providers.json`; legacy `servers.json` fallback may remain during cache migration.
* `nf target password [target] [--root|--db]` prints only the derived Linode target root or database UI password.
* `nf site add <target> <site>` creates the live WordPress env on a target. Use `--with-staging` for one-command live+staging setup.
* `nf site staging status/add/remove` manages optional staging env lifecycle. `rm` is a shorthand for `remove`.
* `nf site refresh` fans out from cached targets. It must not claim to refresh providers.
* `nf site cache [site|env]` clears a remote env cache. Site-only refs default to `:live`; Kinsta uses the Kinsta clear-site-cache API; Linode purges the env nginx FastCGI cache directory and runs `wp cache flush`.
* `nf site repair [site|env]` repairs provider platform state. It previews with `--dry-run`; interactive execution prompts for confirmation; non-interactive execution requires `--execute --yes`. Kinsta repair restores a missing nf identity domain and DNS records while preserving an external primary domain. If no cached env establishes the identity, interactive repair prompts for the canonical project slug and non-interactive repair requires `--project-slug`. It also removes remote `nf-mailpit.php`, restores Kinsta MU plugins when missing, and ensures `KINSTAMU_WHITELABEL` is enabled in `wp-config.php` with an atomic no-backup patch. Linode repair refreshes nginx cache snippets/config, the nf Server Cache MU plugin, and the internal env vhost while preserving basic-auth includes; cached external domain vhosts are not rewritten by this command.
* `nf site password [site|env] [--wp|--db|--basicauth]` shows only one selected site password. `--wp` is the default. Env refs are accepted for `--db`; use a site ref for `--wp` or `--basicauth`. Linode values are derived; Kinsta DB password output uses the Kinsta SFTP password endpoint.
* `nf site remove [site]` removes a whole Linode site and deletes its env data.
* Remote target site discovery is not implemented yet.
* Linode-hosted site/env truth is intended to live on each target at `/var/lib/nf/sites.json`, read over SSH as the standard user.
* Linode target metadata lives at `/var/lib/nf/target.json`. It includes database UI metadata but no raw database UI password.
* Linode database UI is deployed as pinned AdminNeo at `https://<db-user>.<target-hostname>/` during target provisioning, protected by HTTP Basic auth and the wildcard target certificate. New target database UI/admin credentials use the password derivation purpose `db-admin`; legacy target metadata may still record `db`, `adminer`, or `adminer-console` and must remain readable.
* Linode site add grants the shared database access MySQL user privileges only on created site/env databases; site remove revokes those per-database grants before dropping DBs.
* `nf remote add` validates the requested site/env exists in local cache before writing `nf.json`.
* `nf site shell/wp` validate the cache, preview the SSH or wp-cli command, then execute the remote command.
* `nf site export <site:env>` creates a full WordPress handoff export under `NF_DATA_HOME/exports/<env-id-slug>-YYYY-MM-DD-HHMMSS/` by default. It writes `files/` with the full WordPress filesystem, `database.sql.gz`, `manifest.json`, and `README.txt`. This is distinct from snapshots and includes themes, core files, plugins, uploads, and `wp-config.php`.
* `nf env logs [remote]` tails local Docker WordPress logs with no remote, or tails remote `wp-content/debug.log` over SSH for a configured repo remote.
* `nf env password [remote] [--wp|--db|--basicauth]` prints only one selected local or remote env password. `--wp` is the default.
* `nf env push/pull [remote]` defaults to an interactive confirmation before executing remote sync. Use `--dry-run` or `--non-interactive` without `--execute` for preflight-only output. Non-interactive execution requires `--execute --yes`.
* `nf define list/status/sync/add/remove` manages project `wp-config.php` constants declared in `wordpress.defines`. `sync [remote]` patches local or remote `wp-config.php` with a temp file and atomic replace, never persistent `.bak`/dated backups. It owns only the `/* nf-managed wp-config defines: begin */` to `/* nf-managed wp-config defines: end */` project block; removed `nf.json` entries are pruned from that block on sync, while manual constants outside the block are preserved. Legacy per-define `/* nf-managed wp-config defines */` markers are migrated into the block on sync. Status and list output show define names and sources only, never resolved secret values. Duplicate target constants or configured constants outside the nf-managed project block are treated as unsafe and block sync/repair until manually resolved.
* `wordpress.defines` in `nf.json` is the desired project `wp-config.php` constant checklist. Entries require `name` and either top-level `value`/`env` for all envs, or `values` keyed by remote name, canonical env id, env name, `local`, or `default`. `nf define add` with no or incomplete args opens an interactive wizard; the picker shows the shared default, `local`, and configured remotes only, though explicit `--for` still accepts advanced selectors. `nf define remove` with no name opens a picker for configured defines. `nf define add <name> <value>` writes a shared literal; `nf define add <name> --env <VAR>` writes a shared env reference; `--for <selector>` writes selector-specific values and promotes an existing shared value to `values.default`. Never store secret values or license keys directly in `nf.json`; use `env` references and keep the real value in the shell or `~/.config/nf/.env`. `--env` names a local env/config variable that nf resolves during sync; it is not a Kinsta runtime env var. Provider-owned constants such as `KINSTAMU_WHITELABEL` are rejected from `nf define`; Kinsta repair manages them.
* `wordpress.plugins` in `nf.json` is an env bootstrap checklist, not a full lifecycle manager. String entries install from wordpress.org, activate, and enable auto-updates by default; object entries require `slug`, support `source`, `install`, `note`, and `auto_update`, and default `install`, `activate`, and `auto_update` to true. Use `install: false` for manual/documentation-only plugins that nf should check but never install. Use `source: "repo"` for project-specific plugin source directories at `plugins/<slug>/`; local envs bind mount configured repo plugins for live development, while remote installs zip/upload and replace them on demand before cleaning up temporary artifacts. Use `source: "cache"` for explicit local cached zips under `$NF_DATA_HOME/plugins/<slug>/<slug>.zip`; cached packages must declare a WordPress plugin version and replace installed copies only when newer. nf does not silently fall back to cache for wordpress.org plugins.

## Project-context gotchas

* `nf env ...`, `nf init`, `nf theme ...`, `nf plugin ...`, `nf alias ...`, `nf define ...`, and `nf remote ...` are repo/local commands.
* Project-context commands should be hidden or rejected outside a `.git` repo when they require repo metadata.
* `nf.json` version must be exactly `2`. The root contains `project`, `wordpress`, optional `local`, and optional `remotes`. `project` contains only `slug` and `password_version`; `wordpress` contains a required non-empty `themes` list and optional direct `plugins`, `defines`, and `aliases`.
* Never store secrets, generated caches, or global provider inventory in `nf.json`.
* Basic-auth enablement is provider/env state, not project metadata. Linode envs own it in nginx target config. Kinsta Password protection exists in MyKinsta, but currently requires manual MyKinsta use because no public API endpoint is exposed.
* Default WordPress theme convention is `theme/`.
* Generated project metadata should default `wordpress.themes` to one repo theme with `slug` equal to the project slug, `source: "repo"`, and `path: "theme"`, followed by WordPress 7.0 bundled wordpress.org themes `twentytwentyfive`, `twentytwentyfour`, and `twentytwentythree`.
* `wordpress.themes` in `nf.json` is a non-empty ordered env bootstrap checklist. The first listed theme is active. String entries install from wordpress.org and default `auto_update` to false. Object entries require `slug`, support `source`, `path`, `note`, and `auto_update`; `path` is only valid for repo themes. Use `source: "cache"` for explicit local cached zips under `$NF_DATA_HOME/themes/<slug>/<slug>.zip`. Zero repo themes is valid when the active theme comes from wordpress.org, cache, or another install source. At most one theme may use `source: "repo"`; local envs mount it into `wp-content/themes/<slug>` using the real slug. The repo theme owns optional `package` and `tasks` fields.
* Repo-theme tasks may be strings, argv arrays, or objects with optional `description` and required string/argv `run`. String tasks run through `sh -lc`; arrays execute directly; passthrough args follow `--`.
* Print the underlying command preview before running theme/env commands.
* `nf theme package` creates a clean staged artifact for the configured repo theme, runs Composer `install --no-dev` inside the stage when `composer.json` exists, and does not mutate the working theme `vendor/`. It does not run npm or asset builds first. The zip root directory is the repo theme slug, not necessarily the local theme path basename. The repo theme's optional `package.output` defaults to `dist/<project-slug>-v{version}.zip`.
* `nf theme deploy <remote> [--dry-run] [--restart]` is a packaged release deploy. It installs configured non-repo themes first when needed, remains one command, does not require manual WP admin upload, and supersedes direct in-place rsync of the source theme. After a Kinsta release switch and rewrite flush, it clears site cache through the Kinsta API. Pass `--restart` when changed PHP code must also restart PHP-FPM to invalidate stale bytecode.
* Theme deploy releases live under `wp-content/themes/.nf-releases/<theme-slug>/`; metadata lives in `releases.json` there for rollback/history.
* Theme deploy keeps the last 5 distinct releases and matching uploaded artifacts; older release dirs/zips and stale temp dirs are pruned after successful deploy.
* `nf theme rollback <remote> [--dry-run]` restores the previous recorded release and does not rebuild or upload artifacts. Kinsta rollback always restarts PHP and clears site cache after switching back to older code.
* `wordpress.aliases` in `nf.json` maps one top-level URL name to `wp-content` or a descendant path, for example `"files": "wp-content/uploads/public/files"`. Store aliases without a leading slash; display them with one.
* `nf alias sync [remote]` manages root-level webroot symlinks only. It creates or updates configured symlinks and prunes stale root symlinks, but never overwrites or removes real files/directories. Missing targets and targets that resolve outside `wp-content` are unsafe and make sync non-zero.

## Local env gotchas

* Env-generated files stay under `NF_DATA_HOME` / `~/.local/share/nf/envs/<project-slug>/`.
* Local snapshot files stay under `NF_DATA_HOME` / `~/.local/share/nf/snapshots/local/<project-slug>/<snapshot-name>/`.
* Remote snapshot files stay under `NF_DATA_HOME` / `~/.local/share/nf/snapshots/remote/<env-id-slug>-YYYY-MM-DD-HHMMSS/`.
* `nf env up` should be idempotent: ensure env exists, start Compose, configure Mailpit SMTP, reconcile configured `wp-config.php` defines, install WordPress if missing, install configured themes, and activate the first configured theme. `--rebuild` rebuilds the generated WordPress image first.
* `nf env reset --rebuild` creates the normal safety snapshot, removes Docker Compose volumes, rebuilds the generated WordPress image, and recreates the env.
* Local env includes WordPress, MariaDB, Mailpit, and database UI containers. `nf env show` prints Mailpit and prefilled DB URLs.
* Local env generated WordPress config enables `WP_DEBUG` and `WP_DEBUG_LOG`, disables debug display, and routes local mail through Mailpit.
* Local env generated WordPress Dockerfile installs useful CLI tools and wp-cli, and runs Apache/PHP as `docker_user` so bind-mounted uploads stay manageable by the host developer user. `nf env shell`, `nf env sh`, and `nf env wp` execute in the WordPress container as `docker_user`, defaulting to `nonfiction`.
* Local env commands use built-in defaults when `local` is absent. `local` is only for non-default `compose`, `wordpress_service`, `uploads_path`, `admin_user`, and `ports.wordpress`/`ports.mailpit`/`ports.db` overrides.
* `local.uploads_path` is the managed WordPress media uploads directory mounted at `wp-content/uploads`. `nf env up` creates a project-root `uploads` symlink to it, and `nf env down` removes only that managed symlink.
* Local env internal zip handoffs use generated `.nf-transfer` storage mounted at `/env/uploads`; do not confuse `/env/uploads` with the WordPress media library.
* Global config can override local env Docker defaults with `docker_db_image`, `docker_wordpress_image`, and `docker_user`.
* `nf theme add <theme>` appends to `wordpress.themes` in `nf.json`, creates the array if missing, rejects duplicate slugs, and does not install anything. `--source` defaults to wordpress.org; `--source repo` defaults `--path` to `theme`, requires the directory to exist, does not scaffold files, and is limited to one repo theme. `nf theme activate <theme>` moves an existing configured theme to the top of `wordpress.themes` and does not run WP-CLI directly. `nf theme remove <theme>` removes a configured theme from `nf.json`, rejects missing slugs, refuses to remove the last configured theme, and does not uninstall anything. `nf theme status [remote]` compares configured themes against local or remote WordPress state and reports installed, active, and auto-update status. `nf theme diff [remote]` reports needed install/activate/auto-update changes for configured themes and installed themes that are not configured in `nf.json`; it mutates nothing, exits 0 when configured themes match and no extras are installed, and exits 2 when drift exists. `nf theme install` with no remote targets local env. `nf theme install <remote>` targets a configured repo remote, prints a plan, and asks yes/no unless `--yes`; `--dry-run` must not SSH. Remote URL theme sources must be reachable from the remote host; local zip/cache/repo sources are uploaded to a temporary remote directory and cleaned up after install. Theme install is idempotent: install only missing configured themes, activate only the first configured theme when inactive, and enable native WordPress auto-updates only for non-repo themes that request it. It must not update, remove, pin, disable auto-updates, or manage theme licenses.
* `nf plugin add <plugin>` appends to `wordpress.plugins` in `nf.json`, creates the array if missing, rejects duplicate slugs, and does not install anything. With `--source repo`, it creates a minimal `plugins/<slug>/<slug>.php` scaffold only when the plugin directory is missing. `nf plugin remove <plugin>` removes a configured plugin from `nf.json`, rejects missing slugs, and does not uninstall anything. `nf plugin status [remote]` compares configured plugins against local or remote WordPress state and reports installed, active, and auto-update status. `nf plugin diff [remote]` reports needed install/activate/auto-update changes, missing manual plugins, and installed plugins that are not configured in `nf.json`; it mutates nothing, exits 0 when configured plugins match and no extras are installed, and exits 2 when drift exists. `nf plugin install` with no remote targets local env. `nf plugin install <remote>` targets a configured repo remote, prints a plan, and asks yes/no unless `--yes`; `--dry-run` must not SSH. Remote URL plugin sources must be reachable from the remote host; local zip sources are uploaded to a temporary remote directory and cleaned up after install. Plugin install is source-aware: remote repo plugins always replace the installed copy, cache plugins replace it only when their declared version is newer, and other sources install only when missing. It activates inactive plugins when requested and enables native WordPress auto-updates only when not already enabled. It must not install manual plugins, update WordPress.org/URL plugins, remove, pin, disable auto-updates, or manage plugin licenses.
* `nf env reset` is destructive for local env only.
* Snapshot archives include uploads/plugins/languages, not themes or target-owned mu-plugins.
* `nf env snapshot use` creates a safety snapshot named `YYYY-MM-DD-HHMMSS-pre-restore` before restore.
* `nf env snapshot use --remote <name>` imports the remote snapshot into local snapshots, then restores the local copy.
* `wp-content/mu-plugins` are target-owned platform files, not project mutable content. Env push/pull/import/snapshot restore must skip them so local `nf-mailpit.php`, Kinsta MU plugins, and Linode nf cache MU plugins are not clobbered.
* `nf env import <source>` imports an external WordPress handoff into the local env only. It accepts `nf site export` directories or generic WordPress filesystem directories with `--db`, creates an import snapshot, creates a pre-restore safety snapshot, restores database plus uploads/plugins/languages, optionally search-replaces from `--source-url` or export manifest URL, activates the configured local theme when installed, and flushes cache. It intentionally does not import WordPress core, `wp-config.php`, or `wp-content/mu-plugins` into the local env.

## Safety rules

Treat these as high risk:

* production database push
* uploads push to production
* full site sync toward production
* workflows that can overwrite live credentials or uploads
* workflows that destroy remote infrastructure

Future sync/deploy work must require explicit source and destination, identify provider/environment, print a reviewable plan, preserve production credentials where possible, and require confirmation for destructive changes.

Key rule: never silently clobber production credentials.

## Examples and hygiene

Use neutral fictional examples:

* project slug: `client`
* target: `linode1`
* target hostname: `linode1.nonfiction.dev`
* Linode site ID: `client.linode1`
* Linode site URL: `https://client.linode1.nonfiction.dev/`
* Kinsta site ID: `client-kinsta`
* Kinsta placeholder URL: `https://www.example.com/`

Do not commit secrets, generated caches, `.direnv`, build outputs, local state, generated env files, or temporary artifacts.

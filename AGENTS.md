# nf agent guide

Use this file for repo shortcuts and learned implementation gotchas. Put durable product model and roadmap in `SPEC.md`. Put human CLI usage in `README.md`.

## Project shape

* `nf` is a Go CLI.
* Executable entrypoint: `cmd/nf/main.go`.
* CLI dispatcher: `internal/cli.Run`.
* Primary always-visible command groups: `init`, `provider`, `target`, `site`, `config`, `password`.
* Project-only command groups: `remote`, `theme`, `env`, `public`. They appear only when the current repo has `nf.json` next to `.git`.
* Remote env operations live under `site` (`site list --envs`, `site show <site:env>`, `site shell`, `site wp`, `site snapshot`, `site export`), not as a separate `site env` group.
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
go run ./cmd/nf public help
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
* `wordpress.plugins` in `nf.json` is an env bootstrap checklist, not a full lifecycle manager. String entries install from wordpress.org, activate, and enable auto-updates by default; object entries require `slug`, support `source`, `install`, `note`, and `auto_update`, and default `install`, `activate`, and `auto_update` to true. Use `install: false` for manual/documentation-only plugins that nf should check but never install. Use `source: "repo"` for project-specific plugin source directories at `plugins/<slug>/`; local envs bind mount configured repo plugins for live development, while remote installs zip/upload them on demand and clean up temporary artifacts. Use `source: "cache"` for explicit local cached zips under `$NF_DATA_HOME/plugins/<slug>/<slug>.zip`; nf does not silently fall back to cache for wordpress.org plugins.

## Project-context gotchas

* `nf env ...`, `nf init`, `nf theme ...`, and `nf remote ...` are repo/local commands.
* Project-context commands should be hidden or rejected outside a `.git` repo when they require repo metadata.
* `nf.json` should store project metadata, local env intent, theme tasks, artifact recipe, and repo remotes only.
* Never store secrets, generated caches, or global provider inventory in `nf.json`.
* Basic-auth enablement is provider/env state, not project metadata. Linode envs own it in nginx target config. Kinsta Password protection exists in MyKinsta, but currently requires manual MyKinsta use because no public API endpoint is exposed.
* Default WordPress theme convention is `theme/`.
* Generated project metadata should default `wordpress.theme_path` and `env.theme_mount_slug` to `theme`, but `wordpress.theme_slug` to the project slug unless explicitly overridden.
* Theme string tasks run through `sh -lc`; array tasks execute directly; passthrough args follow `--`.
* Print the underlying command preview before running theme/env commands.
* `nf theme package` creates a clean staged artifact, runs Composer `install --no-dev` inside the stage when `composer.json` exists, and does not mutate the working theme `vendor/`. It does not run npm or asset builds first. The zip root directory is `wordpress.theme_slug`, not necessarily the local `wordpress.theme_path` basename.
* `nf theme deploy <remote> [--dry-run]` is a packaged release deploy. It remains one command, does not require manual WP admin upload, and supersedes direct in-place rsync of the source theme.
* Theme deploy releases live under `wp-content/themes/.nf-releases/<theme-slug>/`; metadata lives in `releases.json` there for rollback/history.
* Theme deploy keeps the last 5 distinct releases and matching uploaded artifacts; older release dirs/zips and stale temp dirs are pruned after successful deploy.
* `nf theme rollback <remote> [--dry-run]` restores the previous recorded release and does not rebuild or upload artifacts.
* Static public artifacts live under repo `public/` by convention, but deploy only when explicitly listed in `nf.json` `public.paths` entries. Each entry requires repo-relative symlink-free `source` and URL `path`; optional `delete: true` mirrors deletes and requires `--yes` for execution. `nf public deploy <remote> [--dry-run] [--yes]` deploys these paths to the remote document root and refuses `/`, traversal, and reserved WordPress paths like `/wp-admin`, `/wp-content`, `/wp-includes`, and `/uploads`.
* Large or remote public artifacts should be fetched into `public/` by a project task first; do not add HTTP crawling, archive source, or rsync source support unless explicitly requested.

## Local env gotchas

* Env-generated files stay under `NF_DATA_HOME` / `~/.local/share/nf/envs/<project-slug>/`.
* Local snapshot files stay under `NF_DATA_HOME` / `~/.local/share/nf/snapshots/local/<project-slug>/<snapshot-name>/`.
* Remote snapshot files stay under `NF_DATA_HOME` / `~/.local/share/nf/snapshots/remote/<env-id-slug>-YYYY-MM-DD-HHMMSS/`.
* `nf env up` should be idempotent: ensure env exists, start Compose, configure Mailpit SMTP, install WordPress if missing, activate mounted theme. `--rebuild` rebuilds the generated WordPress image first.
* `nf env reset --rebuild` creates the normal safety snapshot, removes Docker Compose volumes, rebuilds the generated WordPress image, and recreates the env.
* Local env includes WordPress, MariaDB, Mailpit, and database UI containers. `nf env show` prints Mailpit and prefilled DB URLs.
* Local env generated WordPress config enables `WP_DEBUG` and `WP_DEBUG_LOG`, disables debug display, and routes local mail through Mailpit.
* Local env generated WordPress Dockerfile installs useful CLI tools and wp-cli. `nf env shell`, `nf env sh`, and `nf env wp` execute in the WordPress container as `docker_user`, defaulting to `nonfiction`.
* Global config can override local env Docker defaults with `docker_db_image`, `docker_wordpress_image`, and `docker_user`.
* `nf plugin add <plugin>` appends to `wordpress.plugins` in `nf.json`, creates the array if missing, rejects duplicate slugs, and does not install anything. With `--source repo`, it creates a minimal `plugins/<slug>/<slug>.php` scaffold only when the plugin directory is missing. `nf plugin remove <plugin>` removes a configured plugin from `nf.json`, rejects missing slugs, and does not uninstall anything. `nf plugin status [remote]` compares configured plugins against local or remote WordPress state and reports installed, active, and auto-update status. `nf plugin diff [remote]` reports needed install/activate/auto-update changes, missing manual plugins, and installed plugins that are not configured in `nf.json`; it mutates nothing, exits 0 when configured plugins match and no extras are installed, and exits 2 when drift exists. `nf plugin install` with no remote targets local env. `nf plugin install <remote>` targets a configured repo remote, prints a plan, and asks yes/no unless `--yes`; `--dry-run` must not SSH. Remote URL plugin sources must be reachable from the remote host; local zip sources are uploaded to a temporary remote directory and cleaned up after install. Plugin install is idempotent: install only missing configured plugins where `install` is true, activate only inactive plugins when requested, and enable native WordPress auto-updates only when not already enabled. It must not install manual plugins, update, remove, pin, disable auto-updates, or manage plugin licenses.
* `nf env reset` is destructive for local env only.
* Snapshot archives include uploads/plugins/mu-plugins/languages, not themes.
* `nf env snapshot use` creates a safety snapshot named `YYYY-MM-DD-HHMMSS-pre-restore` before restore.
* `nf env snapshot use --remote <name>` imports the remote snapshot into local snapshots, then restores the local copy.
* `nf env import <source>` imports an external WordPress handoff into the local env only. It accepts `nf site export` directories or generic WordPress filesystem directories with `--db`, creates an import snapshot, creates a pre-restore safety snapshot, restores database plus uploads/plugins/mu-plugins/languages, optionally search-replaces from `--source-url` or export manifest URL, activates the configured local theme when installed, and flushes cache. It intentionally does not import WordPress core or `wp-config.php` into the local env.

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
* target: `app1-linode`
* target hostname: `app1.nonfiction.dev`
* Linode site ID: `client-app1-linode`
* Linode site URL: `https://client.app1.nonfiction.dev/`
* Kinsta site ID: `client-kinsta`
* Kinsta placeholder URL: `https://www.example.com/`

Do not commit secrets, generated caches, `.direnv`, build outputs, local state, generated env files, or temporary artifacts.

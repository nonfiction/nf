# nf

`nf` is nonfiction’s internal CLI for agency WordPress theme work.

It gives the team one command surface for project metadata, local WordPress dev envs, theme tasks, theme packaging, provider inventory, repo remotes, password derivation, and guarded future deploy/sync workflows.

This is an internal agency tool, not a general-purpose public WordPress framework.

For the full project model, state layout, implementation phases, and roadmap, see [`SPEC.md`](SPEC.md).

## Install and run

```sh
nix run .#nf -- --help
nix develop -c nf --help
go run ./cmd/nf --help
```

Build and test:

```sh
nix build .#nf -L
go test ./...
```

Nix flakes build from the git source snapshot. Stage newly added source files before trusting `nix run` or `nix build`.

## Commands

Top-level help is context-sensitive.

Always available:

```text
nf

Commands:
  init        initialize project metadata
  provider    manage provider integrations
  target      manage deployable targets
  site        manage remote sites and envs
  config      manage global config
  password    derive passwords
  completion  print shell completion scripts
  help        show help
```

Inside an `nf` project repo with `nf.json` next to `.git`, help also shows:

```text
  remote      manage repo remotes
  env         manage the local development env
  theme       package clean artifacts and run theme tasks
  public      deploy static public paths
```

## Shell completion

`nf` can print completion scripts for bash and zsh:

```sh
nf completion bash
nf completion zsh
```

Temporary setup for the current shell:

```sh
# bash
source <(nf completion bash)

# zsh
source <(nf completion zsh)
```

Persistent setup depends on your shell config. Save the generated script into a file loaded by your shell, such as a bash completion directory or a zsh `$fpath` completion file.

## Quick start

Create repo metadata:

```sh
nf init
```

After `nf init`, run project-local commands from that repo so `theme`, `env`, and `remote` are available.

Start local WordPress:

```sh
nf env up
nf env show
nf env wp -- plugin list
nf env plugins list
nf env plugins add stream
nf env plugins remove stream
nf env plugins status
nf env plugins diff
nf env plugins install
nf env plugins install production --dry-run
```

List theme tasks and run one:

```sh
nf theme tasks
nf theme build
```

Package the theme:

```sh
nf theme package
```

Deploy the packaged theme release to a configured repo remote:

```sh
nf theme deploy production [--dry-run]
nf theme rollback production [--dry-run]
nf public deploy production [--dry-run]
```

## Project metadata

Project repositories use:

```text
nf.json
```

This file is safe to commit. It must not contain API tokens, SSH keys, live database passwords, provider secrets, or mutable provider inventory.

Common init flags:

```sh
nf init \
  --project-slug client \
  --theme-slug client \
  --theme-source theme
```

By default, `nf init` derives the project slug from the current git root folder and assumes the WordPress theme lives in `theme/`.

## Theme tasks and packaging

```sh
nf theme tasks
nf theme package [--dry-run] [--source path] [--output path]
nf theme deploy <remote> [--dry-run]
nf theme rollback <remote> [--dry-run]
nf theme <task> [-- args]
```

`nf theme tasks` lists project tasks from `nf.json`.

String tasks run through `sh -lc` from the project root. Array tasks execute directly. The underlying command is printed before execution.

`nf theme package` creates a clean staged release artifact instead of zipping the development checkout as-is. It copies runtime theme files to a temporary staging directory, excludes obvious local development files, and when `composer.json` is present runs `composer install --no-dev --no-interaction --prefer-dist --optimize-autoloader --no-progress` in that staging directory before writing the zip. This preserves the working tree's `theme/vendor/` while ensuring the artifact contains production Composer dependencies such as `vendor/autoload.php` and excludes `require-dev` tooling.

It still does not run npm or asset builds. Run the right theme task before packaging:

```sh
nf theme build
nf theme package
```

If `package.json` has a `build` script, packaging requires built files under `dist/` or `assets/dist/` and fails clearly when they are missing. Development-only files such as `node_modules`, editor config, PHP-CS-Fixer/PHPCS/PHPStan/Psalm config, npm manifests and lockfiles, and common frontend tooling config are excluded from the artifact. The zip root remains `wordpress.theme_slug`.

If `artifact.path` contains `{version}`, `nf` resolves it from:

1. `theme/style.css` `Version:`
2. `theme/package.json` `version`

`nf theme deploy <remote>` is a one-command packaged release deploy. It builds the same theme artifact, uploads it to the selected remote env, extracts it under `wp-content/themes/.nf-releases/<theme-slug>/`, copies the release into the active theme directory, activates the configured theme slug with wp-cli, and records release metadata. It keeps the last 5 releases and matching uploaded zips, so release storage does not grow indefinitely. It does not require manual WordPress admin zip upload and supersedes direct in-place source rsync deploys.

`nf theme rollback <remote>` switches the active theme directory back to the previous recorded release and activates the configured theme slug again. It uses remote `releases.json`; it does not rebuild or upload artifacts.

## Static public artifacts

Use `public/` for static artifacts that must live at specific non-WordPress URL paths, such as annual report microsites.

```json
{
  "public": {
    "paths": [
      {
        "source": "public/annual-report-2026",
        "path": "/annual-report-2026"
      }
    ]
  }
}
```

Deploy them separately from the theme:

```sh
nf public deploy production --dry-run
nf public deploy production
```

`nf` only deploys paths explicitly listed in `nf.json`. `source` must be repo-relative and symlink-free. `path` must be an absolute URL path and cannot target `/`, traversal, `/wp-admin`, `/wp-content`, `/wp-includes`, or `/uploads`. Add `"delete": true` to mirror removals with `rsync --delete`; execution then requires `--yes`.

For large artifacts that should not live in git, use a project task to materialize the deployable files into `public/` first, then deploy the local directory:

```json
{
  "tasks": {
    "fetch-annual-report-2026": {
      "description": "Fetch annual report static export",
      "run": "scripts/fetch-annual-report-2026"
    }
  },
  "public": {
    "paths": [
      {
        "source": "public/annual-report-2026",
        "path": "/annual-report-2026"
      }
    ]
  }
}
```

```sh
nf theme fetch-annual-report-2026
nf public deploy production --dry-run
```

Remote HTTP crawling, archives, and rsync side-loaded sources are intentionally outside this first slice.

## Local WordPress env

The local env is `nf`'s generated WordPress dev environment for a project.

Common workflow:

```sh
nf env up
nf env show
nf env logs
nf env shell
nf env wp -- plugin list
nf env plugins list
nf env plugins add stream
nf env plugins remove stream
nf env plugins status
nf env plugins status production
nf env plugins diff production
nf env plugins install
nf env down
```

`nf env up` is idempotent. It starts Docker Compose, installs WordPress if needed, and ensures the mounted theme is active.

`nf env reset` is destructive for the local env only. It removes Docker Compose volumes and recreates the env.

Configured WordPress plugins live in `nf.json` under `wordpress.plugins`:

```json
{
  "wordpress": {
    "plugins": [
      "stream",
      "wp-crontrol",
      "insert-headers-and-footers",
      "block-visibility",
      "imsanity",
      {
        "slug": "acf-pro",
        "source": "$NF_PLUGIN_ACF_PRO_ZIP",
        "activate": true,
        "auto_update": true
      }
    ]
  }
}
```

String entries install from wordpress.org, activate, and enable auto-updates by default. Object entries require `slug`, may set `source` to a zip URL/path or env var, and may set `activate` or `auto_update` to `false`. Keep private plugin URLs and license data in environment variables, not `nf.json`.

`nf env plugins add <plugin>` appends a plugin to `nf.json` without installing it. Add `--source <source>`, `--no-activate`, or `--no-auto-update` when defaults are not enough. `nf env plugins remove <plugin>` removes a plugin from `nf.json` without uninstalling it.

`nf env plugins install` with no remote targets the local env. `nf env plugins install <remote>` targets a configured repo remote, prints a remote plan, and asks for yes/no confirmation before changing the remote unless `--yes` is passed. Use `--dry-run` to preview only. Remote installs run WP-CLI on the remote host. URL sources must be reachable from that host; local zip sources are uploaded to a temporary remote directory before install and cleaned up afterward.

`nf env plugins status [remote]` compares `nf.json` against the local env or configured remote and reports whether each configured plugin is installed, active, and auto-update enabled.

`nf env plugins diff [remote]` reports the install/activate/auto-update changes needed to make the local env or remote match `nf.json`. It also reports installed plugins that are not configured in `nf.json`. It does not mutate anything. It exits `0` when configured plugins match and no extras are installed, and `2` when drift exists.

Plugin install is idempotent: it installs only missing plugins, activates only inactive plugins when `activate` is true, and enables native WordPress auto-updates only when not already enabled. It does not update, remove, pin, disable auto-updates, or manage plugin licenses.

Generated env data lives under:

```text
~/.local/share/nf/envs/<project-slug>/
```

Override for tests or isolated runs:

```sh
NF_DATA_HOME=/tmp/nf-data
```

## Env snapshots

```sh
nf env snapshot add [name]
nf env snapshot list
nf env snapshot import [remote-snapshot] [--name name]
nf env snapshot use [name] [--yes]
nf env snapshot use --remote <remote-snapshot> [--name name] [--yes]
nf env snapshot remove [name]
nf env snapshot prune [--keep N] [--dry-run] [--yes]
```

Local env snapshots live under:

```text
~/.local/share/nf/snapshots/local/<project-slug>/<snapshot-name>/
```

Each snapshot contains:

* `snapshot.json`
* `database.sql.gz`
* `wp-content.tar.gz`

The `wp-content` archive includes only `uploads/`, `plugins/`, `mu-plugins/`, and `languages/`. It skips themes.

`nf env snapshot use` creates a safety snapshot named `YYYY-MM-DD-HHMMSS-pre-restore` before restoring the selected snapshot. Add `--yes` to skip the interactive confirmation.

Remote snapshots are downloaded from cached remote site env records and live under:

```text
~/.local/share/nf/snapshots/remote/<env-id-slug>-YYYY-MM-DD-HHMMSS/
```

Remote snapshot workflow:

```sh
nf site snapshot <site.target:env>
nf site snapshot list
nf env snapshot import <remote-snapshot-name> --name live-copy
nf env snapshot use live-copy --yes
```

Shortcut restore from a remote snapshot:

```sh
nf env snapshot use --remote <remote-snapshot-name> --name live-copy --yes
```

This imports the remote snapshot into the current project's local snapshots, restores it, creates the normal pre-restore safety snapshot first, and keeps the imported local snapshot for audit/reuse.

## Config and secrets

Config lives under:

```text
~/.config/nf/
  config.json
  .env
```

Non-secret config goes in `config.json`:

```json
{
  "base_domain": "nonfiction.dev",
  "dnsimple_account_id": "14"
}
```

Secrets go in `.env`:

```env
NF_PASSWORD_SALT=
DNSIMPLE_TOKEN=
LINODE_TOKEN=
KINSTA_API_KEY=
```

`dnsimple_account_id` is fetched from DNSimple with `DNSIMPLE_TOKEN` by `nf provider check dnsimple`; do not set `DNSIMPLE_ACCOUNT_ID` in `.env`.

Use:

```sh
nf config init
nf config set-base-domain nonfiction.dev
nf config set-default-wp-email dev@example.com
nf config set-default-wp-user admin
nf config set-kinsta-default-php 8.3
nf config set-kinsta-default-region us-central1
nf config set-linode-default-region us-east
nf config set-linode-default-type g6-standard-1
nf config set-linode-default-image linode/ubuntu24.04
nf config set-linode-default-user nonfiction
nf config show
nf password set-salt <salt>
```

Overrides for tests or isolated runs:

```sh
NF_CONFIG_HOME=/tmp/nf-config
NF_STATE_HOME=/tmp/nf-state
NF_DATA_HOME=/tmp/nf-data
```

## Providers, targets, sites, and remotes

Commands:

```sh
nf provider list
nf provider show <provider>
nf provider check <provider>
nf target add linode <name> [--region region] [--type type] [--image image] [--user user] [--keys all] [--execute --yes] [--wait]
nf target remove <target> [--dry-run] [--execute --yes]
nf target refresh
nf target list
nf target show <target>
nf site add <target> <site> [--region region] [--php version] [--execute --yes]
nf site refresh
nf site list [--refresh] [--envs]
nf site show <site-id-or-alias-or-env-id>
nf site shell <site.target:env>
nf site wp <site.target:env> -- <cmd>
nf site snapshot <site.target:env> [--output path] [--dry-run]
nf site snapshot list
nf site snapshot remove <name> [--yes]
nf site snapshot prune [--keep N] [--dry-run] [--yes]
nf site password [site-id-or-alias]
nf site remove [site-id-or-alias] [--dry-run] [--execute --yes]
nf remote add [name] [site.target:env]
nf remote show <name>
nf remote remove <name>
nf remote list
```

Standard Linode target example:

```sh
nf target add linode app1 \
  --region ca-central \
  --type g6-standard-1 \
  --image linode/ubuntu24.04 \
  --user nonfiction \
  --keys all
```

Current behavior:

* `nf provider list` reports local credential status.
* `nf provider check` calls safe read-only provider health endpoints and writes `providers.json`.
* `nf provider show <provider>` reads cached provider metadata.
* `nf target add linode <name>` creates or ensures a Linode target named `<name>-linode`, tags it `nf`, creates host and wildcard DNS records under `base_domain`, queues HTTPS setup on the target with a systemd retry timer, and records the target under the Linode provider in `providers.json`. Add `--wait` to keep the CLI attached through SSH, TLS, and health checks.
* `nf target remove <target>` removes an empty Linode target.
* `nf target refresh` updates target records from configured target providers so added and removed targets are reflected in `providers.json`.
* `nf target list/show` read target records from `providers.json`, with a legacy `servers.json` fallback.
* `nf site add <target> <site>` creates live and staging WordPress envs on a target.
* `nf site refresh` discovers sites from the cached target list. Remote target site discovery is not implemented yet.
* `nf site list --envs`, `nf site show`, `nf site shell`, `nf site wp`, and `nf site snapshot` read the local disposable site cache for now.
* `nf site password [site]` shows the derived admin password only.
* `nf site remove [site]` removes a Linode site and deletes its env data.
* `nf remote add` validates an env ID against the cache, then repo remotes are stored in `nf.json` under `remotes` as `<site>.<target>:<env>` refs.
* `nf site shell/wp ...` currently preflights against the cache, then stops without running remote commands.
* `nf env push/pull [remote]` syncs database and mutable `wp-content` after an interactive confirmation. Omit `remote` to pick from configured repo remotes. Add `--dry-run` for a non-mutating plan, or use `--non-interactive` without `--execute` for preflight-only output.

State/cache lives under:

```text
~/.local/state/nf/
  providers.json
  sites.json
  projects.json
```

Local state is disposable cache, not source of truth.

## Password derivation

```sh
nf password set-salt <salt>
nf password show-salt
nf password derive <scope> <value...>
```

Password derivation uses `NF_PASSWORD_SALT` from the environment or `~/.config/nf/.env`. Legacy `NF_SECRET_SALT` is accepted only as a migration fallback.

## Safety

Database and uploads sync are high risk. Sync commands must print a reviewable plan, preserve production credentials where possible, and require confirmation before destructive changes.

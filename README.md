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

```text
nf

Commands:
  init          initialize project metadata
  provider      manage provider integrations
  target        list and show deployable targets
  site          refresh, list, and show remote sites/envs
  remote        manage repo deploy remotes
  theme         package artifacts and run theme tasks
  env           manage the local development env
  config        manage global config
  password      derive passwords
  help          show help
```

Removed old public routes:

* `nf server ...`
* `nf instance ...`
* top-level `nf up`, `nf down`, `nf logs`, `nf reset`, `nf info`, `nf shell`, `nf wp`

## Quick start

Create repo metadata:

```sh
nf init
```

Start local WordPress:

```sh
nf env up
nf env show
nf env wp -- plugin list
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

## Project metadata

Project repositories use:

```text
.nf/project.json
```

This file is safe to commit. It must not contain API tokens, SSH keys, live database passwords, provider secrets, or mutable provider inventory.

Common init flags:

```sh
nf init \
  --project-slug client \
  --project-name "Client" \
  --theme-slug theme \
  --theme-source theme
```

By default, `nf init` derives the project slug from the current git root folder and assumes the WordPress theme lives in `theme/`.

## Theme tasks and packaging

```sh
nf theme tasks
nf theme package [--dry-run] [--source path] [--output path]
nf theme <task> [-- args]
```

`nf theme tasks` lists project tasks from `.nf/project.json`.

String tasks run through `sh -lc` from the project root. Array tasks execute directly. The underlying command is printed before execution.

`nf theme package` zips existing theme files only. It does not run Composer, npm, or asset builds first. Run the right theme task before packaging:

```sh
nf theme build
nf theme package
```

If `artifact.path` contains `{version}`, `nf` resolves it from:

1. `theme/style.css` `Version:`
2. `theme/package.json` `version`

## Local WordPress env

The local env is `nf`'s generated WordPress dev environment for a project.

Common workflow:

```sh
nf env up
nf env show
nf env logs
nf env shell
nf env wp -- plugin list
nf env down
```

`nf env up` is idempotent. It starts Docker Compose, installs WordPress if needed, and ensures the mounted theme is active.

`nf env reset` is destructive for the local env only. It removes Docker Compose volumes and recreates the env.

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
nf env snapshot use [name]
nf env snapshot remove [name]
```

Snapshots live under:

```text
~/.local/share/nf/snapshots/<project-slug>/<snapshot-name>/
```

Each snapshot contains:

* `snapshot.json`
* `database.sql.gz`
* `wp-content.tar.gz`

The `wp-content` archive includes only `uploads/`, `plugins/`, `mu-plugins/`, and `languages/`. It skips themes.

`nf env snapshot use` creates a safety snapshot named `YYYY-MM-DD-HHMMSS-pre-restore` before restoring the selected snapshot.

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
nf target list
nf target show <target>
nf site refresh
nf site list [--refresh]
nf site show <site-id-or-alias>
nf site env list [site-id]
nf site env show <site-id> <env>
nf site env shell <site-id> <env>
nf site env wp <site-id> <env> -- <args>
nf remote add <name> <site-id> <env>
nf remote show <name>
nf remote remove <name>
nf remote list
```

Current behavior:

* `nf provider list` reports local credential status.
* `nf provider check` calls safe read-only provider health endpoints and writes `providers.json`.
* `nf provider show <provider>` reads cached provider metadata.
* `nf target list/show` read target records from `providers.json`, with a legacy `servers.json` fallback.
* `nf site refresh` discovers sites from the cached target list. Remote target site discovery is not implemented yet.
* `nf site list/show` and `nf site env list/show` read the local disposable site cache for now.
* `nf remote add` validates against the cache, then repo remotes are stored in `.nf/project.json` under `deploy.remotes`.
* `nf site env shell/wp ...` currently preflights against the cache, then stops without running remote commands.
* `nf env push/pull <remote>` currently preflights against the cache, then stops without syncing data.

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

Database and uploads sync are high risk. Future implementation must print a reviewable plan, preserve production credentials where possible, and require confirmation before destructive changes.

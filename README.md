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

Inside an `nf` project repo with `.nf/` next to `.git`, help also shows:

```text
  remote      manage repo remotes
  env         manage the local development env
  theme       package files and run theme tasks
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

During rapid development, avoid wrappers that run `nix run` for every completion query. In the dev shell, refresh a cached binary:

```sh
nix develop
nf-dev-build
```

Then point completion queries at `.cache/nf` from your local wrapper:

```sh
#!/usr/bin/env sh

DEV_NF=/home/jon/src/nonfiction/nf/.cache/nf

case "$1" in
  __complete|completion)
    if [ -x "$DEV_NF" ]; then
      exec "$DEV_NF" "$@"
    fi
    ;;
esac

exec nix run /home/jon/src/nonfiction/nf -- "$@"
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

After `nf init`, run project-local commands from that repo so `theme`, `env`, and `remote` are available.

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
nf site list [--refresh]
nf site show <site-id-or-alias>
nf site password [site-id-or-alias]
nf site remove [site-id-or-alias] [--dry-run] [--execute --yes]
nf site env list [site-id]
nf site env show [site-id] [--live|--staging] [--json]
nf site env shell [site-id] [--live|--staging]
nf site env wp <site-id> [--live|--staging] <cmd>
nf remote add <name> <site-id> <env>
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
* `nf site list/show` and `nf site env list/show` read the local disposable site cache for now.
* `nf site password [site]` shows the derived admin password only.
* `nf site remove [site]` removes a Linode site and deletes its env data.
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

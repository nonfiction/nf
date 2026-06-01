# nf

`nf` is nonfiction’s internal CLI for agency WordPress theme work.

It gives the team one command surface for project metadata, local WordPress dev envs, theme tasks, theme packaging, provider inventory, repo remotes, password derivation, and guarded future deploy/sync workflows.

This is an internal agency tool, not a general-purpose public WordPress framework.

## Status

Working or stubbed now:

* `nf init`
* `nf provider list`
* `nf target list` / `nf target show` (stubbed)
* `nf site refresh` (stubbed)
* `nf site list`
* `nf site show`
* `nf site env ...` (stubbed)
* `nf remote add|remove|list` (stubbed)
* `nf theme tasks`
* `nf theme package`
* direct theme tasks from `.nf/project.json`
* `nf env up`
* `nf env down`
* `nf env logs`
* `nf env reset`
* `nf env show`
* `nf env shell`
* `nf env wp`
* `nf env push <remote>` (stubbed)
* `nf env pull <remote>` (stubbed)
* `nf env snapshot add [name]`
* `nf env snapshot list`
* `nf env snapshot use [name]`
* `nf env snapshot remove [name]`
* `nf config init`
* `nf config set-default-wp-email <email>`
* `nf config set-default-wp-user <user>`
* `nf config show`
* `nf password set-salt <salt>`
* `nf password show-salt`
* `nf password derive <scope> <value...>`

Removed public routes:

* `nf server ...`
* `nf instance ...`
* top-level `nf up`, `nf down`, `nf logs`, `nf reset`, `nf info`, `nf shell`, `nf wp`
* old snapshot verbs `create`, `restore`, `delete`, and alias `snapshots`

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

## Command overview

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

Global/provider context:

* providers: `dnsimple`, `kinsta`, `linode`
* targets: deployable places, such as `kinsta` or `app1-linode`
* sites: `<site>-<target>`
* remote envs: `live` / `staging`, displayed as `<env>-<site>-<target>`

Repo/local context:

* `.nf/project.json` stores project metadata and repo remotes
* `nf env ...` manages the local WordPress dev env
* `nf remote ...` maps repo-local names to remote site envs
* `nf theme ...` packages/runs theme tasks

## Project metadata

Project repositories use:

```text
.nf/project.json
```

This file is safe to commit. It must not contain API tokens, SSH keys, live database passwords, provider secrets, or mutable provider inventory.

Create it with:

```sh
nf init
```

Common flags:

```sh
nf init \
  --project-slug client \
  --project-name "Client" \
  --theme-slug theme \
  --theme-source theme
```

By default, `nf init` derives the project slug from the current git root folder and assumes the WordPress theme lives in `theme/`.

Example `.nf/project.json` shape:

```json
{
  "schema": 1,
  "project": {
    "slug": "client",
    "name": "Client",
    "type": "wordpress-theme"
  },
  "wordpress": {
    "deploy_unit": "theme",
    "theme_slug": "theme",
    "theme_path": "theme"
  },
  "env": {
    "compose": "docker compose",
    "wordpress_service": "wordpress",
    "cli_service": "cli",
    "theme_mount_slug": "theme",
    "uploads_path": "uploads",
    "ports": {
      "wordpress": 18432,
      "mailpit": 18433
    }
  },
  "deploy": {
    "remotes": {}
  },
  "tasks": {
    "build": {
      "description": "Build the theme assets",
      "run": "npm --prefix theme run build"
    }
  }
}
```

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

The local env is `nf`'s generated WordPress dev environment for a project. It contains Docker/WordPress scaffolding and mutable local state used to run, reset, snapshot, and sync during development.

Generated env files live under XDG data:

```text
~/.local/share/nf/envs/<project-slug>/
```

Override for tests or isolated runs:

```sh
NF_DATA_HOME=/tmp/nf-data
```

Env ports are derived deterministically from the project slug. Set `env.ports.wordpress` and `env.ports.mailpit` in `.nf/project.json` to override them individually; zero or missing values fall back to the derived ports.

Common env workflow:

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

## Env snapshots

Snapshots live under XDG data:

```text
~/.local/share/nf/snapshots/<project-slug>/<snapshot-name>/
```

Each snapshot contains:

* `snapshot.json`
* `database.sql.gz`
* `wp-content.tar.gz`

The `wp-content` archive includes only `uploads/`, `plugins/`, `mu-plugins/`, and `languages/`. It skips themes.

Commands:

```sh
nf env snapshot add [name]
nf env snapshot list
nf env snapshot use [name]
nf env snapshot remove [name]
```

`nf env snapshot use` creates a safety snapshot named `YYYY-MM-DD-HHMMSS-pre-restore` before restoring the selected snapshot.

## Config, state, and secrets

Config lives under XDG config:

```text
~/.config/nf/
  config.json
  .env
```

State/cache lives under XDG state:

```text
~/.local/state/nf/
  sites.json
  projects.json
```

Generated env data lives under XDG data:

```text
~/.local/share/nf/
  envs/
  snapshots/
```

Overrides:

```sh
NF_CONFIG_HOME=/tmp/nf-config
NF_STATE_HOME=/tmp/nf-state
NF_DATA_HOME=/tmp/nf-data
```

Local secrets and account-specific values live in:

```text
~/.config/nf/.env
```

Expected values include:

```env
NF_SECRET_SALT=
DNSIMPLE_TOKEN=
LINODE_TOKEN=
DNSIMPLE_ACCOUNT_ID=14
```

Use:

```sh
nf config init
nf password set-salt <salt>
```

to create/update local config. Secrets are masked by default in user-facing output.

## Provider, target, site, and remote model

Provider truth is canonical remotely:

* Kinsta API is canonical for Kinsta sites/envs.
* Linode API is canonical for Linode servers/targets.
* Linode-hosted site/env truth lives on each target at `/var/lib/nf/sites.json` and is read over SSH by the standard user.

Local state is a disposable normalized inventory cache, not source of truth.

Commands:

```sh
nf provider list
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
nf remote remove <name>
nf remote list
```

Remote env and repo remote commands are stubs in this pass unless noted by command output.

## Password derivation

```sh
nf password set-salt <salt>
nf password show-salt
nf password derive <scope> <value...>
```

Password derivation uses `NF_SECRET_SALT` from the environment or `~/.config/nf/.env`.

## Production safety

Database and uploads sync are high risk.

Future implementation must:

* require explicit source and destination
* identify provider and environment
* print a reviewable plan
* preserve production passwords and sensitive options where possible
* require confirmation for destructive changes
* never silently clobber production credentials

## Development notes

Primary entrypoint:

```text
cmd/nf/main.go
```

Common checks:

```sh
go test ./...
go test ./internal/cli
go run ./cmd/nf --help
go run ./cmd/nf provider list
go run ./cmd/nf site list
go run ./cmd/nf env help
```

Keep `README.md` and `AGENTS.md` aligned when command names, state layout, safety posture, or provider behavior change.

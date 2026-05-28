# nf planning spec

`nf` is the planned agency-level CLI for nonfiction WordPress infrastructure,
deployment, and shared operational state.

This repository now includes the first safe executable CLI slice.
It is local-only and read-only except for `.nf/project.json` init, local repo
command running, local theme zip packaging, and guarded `nf server provision`
and `nf server delete` slices.
`nf server provision` is interactive-first through Bubble Tea/Bubbles/Lip Gloss;
flags are shortcuts, and `--non-interactive` is available for scripting and tests.
Remote deploy, sync, and destructive workflows remain policy-gated work.

The intended packaging path for `nf` is a `flake.nix` in this repository that
builds and distributes the Go CLI to the nonfiction team and lets WordPress
project flakes consume it as an input.

## Try it

- `nix run .#nf -- --help`
- `nix develop -c nf --help`
- Grouped commands: `nf server ...`, `nf site ...`, `nf repo ...`
- With direnv: run `direnv allow` once, then enter the repo to auto-load the
  flake dev shell.

## Problem

nonfiction needs one shared tool for provisioning, installing, deploying, and
synchronizing WordPress sites across providers and environments.

The tool must not be project-specific.
It should capture the lessons currently embedded in the `siafintech`
deployment scripts without carrying over project-only assumptions.

## Goals

- One command surface for agency-level WordPress operations.
- Shared server/site state across a small team.
- Provider abstraction for Linode now and Kinsta later.
- Safe deployment workflows for theme, database, and uploads sync.
- Clear separation between project repos and shared infrastructure state.
- Human-reviewable configuration and recovery-friendly operations.

## Non-goals

- Project-specific deployment scripts.
- A generic WordPress management framework for the public.
- Automatic secret generation and secret storage in git.
- Immediate executable implementation in this repo.
- Replacing WordPress itself or project-level content workflows.

## Terminology

- **Project**: a client or site codebase, such as a client project.
- **Server**: a reusable host target, such as `app1.nfweb.dev`.
- **Site**: a WordPress install on a server, such as `client.app1.nfweb.dev`.
- **Environment**: a deployment target like staging or production.
- **Provider**: infrastructure back end such as Linode or Kinsta.
- **State repo**: the shared private git repo that stores synced nf state.
- **Project metadata**: safe repository-local metadata, such as
  `.nf/project.json`.

## High-level architecture

`nf` should be split into three layers:

1. **Local config layer**
   - `~/.config/nf/.env` for local secrets and static account values.
   - `~/.config/nf/config.*` for non-secret defaults.

2. **Shared state layer**
   - `~/.config/nf/state` as a private git checkout, or a config pointing to a
     private checkout.
   - Stores synced server records, site records, environment mappings, and
     derived operational metadata.

3. **Project metadata layer**
   - Repository-local safe metadata only.
   - Example path: `.nf/project.json`.
   - Contains project identity and deployment hints, not secrets.

The CLI should read from local config, then shared state, then project metadata.
It should never require project repos to contain sensitive operational state.

## Flake packaging and distribution

`nf` should eventually be packaged with `flake.nix` in this repository.

The flake should:

- expose `packages.${system}.default` containing the `nf` executable
- optionally expose `devShells.default` for `nf` development

WordPress project repositories should consume `nf` as a flake input in their own
`flake.nix` files. They should not vendor or copy `nf` scripts into project
repos.

Team install and use options should include:

- using `nf` from a project dev shell input
- checking out this repo directly for local development
- optionally installing the package into a Nix profile

This packaging path is separate from the private `nf` state repo and from
`~/.config/nf/.env`, which remain local secrets and machine state.

## Config and state layout

Planned home layout:

```text
~/.config/nf/
  .env
  config.json
  state/
    servers.json
    sites.json
    projects.json
    providers/
      linode.json
      kinsta.json
```

The exact file split can evolve, but the separation must stay:

- secrets in `.env`
- shared machine state in `state/`
- project-safe metadata in project repos

### Secrets file

`~/.config/nf/.env` is manual setup only.

It may include values such as:

- API credentials for Linode
- DNSimple credentials
- GitHub auth for state sync automation
- `NF_SECRET_SALT`

It must never be committed.

### State sync

Shared state should live in a private git repo so a team of two to three people
can match server and site state.

The repo can be checked out at `~/.config/nf/state` or referenced through a
config option, but the model is the same: one shared, private, versioned state
source.

## Project metadata example

Example `.nf/project.json` for a Sanjel project:

```json
{
  "schema": 1,
  "project": {
    "slug": "sanjel",
    "name": "Sanjel",
    "type": "wordpress-theme"
  },
  "wordpress": {
    "deploy_unit": "theme",
    "theme_slug": "sanjel",
    "theme_path": "theme"
  },
  "build": {
    "commands": [
      "composer install",
      "npm run build"
    ]
  },
  "artifact": {
    "path": "dist/sanjel.zip",
    "include": [
      "vendor/",
      "assets/dist/"
    ],
    "exclude": [
      "node_modules/",
      ".git/"
    ]
  },
  "deploy": {
    "aliases": {
      "app1": "sanjel-app1-production",
      "staging": "sanjel-kinsta-staging",
      "production": "sanjel-kinsta-production"
    }
  },
  "commands": {}
}
```

Notes:

- This file is safe to store in the project repo.
- It must not contain secrets.
- It should not duplicate mutable server state.
- It should describe intent, not replace shared nf state.
- Site targets may use names like `sanjel-app1-production`, `sanjel-app1-staging`, or `app1`.
- Kinsta targets can carry `kinsta.company_id`, `kinsta.site_id`, and `kinsta.environment_id` without SSH info.
- The hostname `https://sanjel.app1.nfweb.dev/` is the sort of URL the shared site state should surface.
- The generated default `commands` block uses direct project-local commands,
  not project Makefiles. `nf` owns these workflows going forward.
- The default source layout assumes `theme/` for the theme and `workbench/`
  for the local compose environment.
- Commands such as `install-theme` and `activate-theme` can be customized per
  project if a repo wants named arguments instead of the default positional
  form.

The current CLI also reads an optional `commands` block from this file for
local project command aliases and direct workbench wrappers.

## Shared state examples

`~/.config/nf/state/servers.json`

```json
{
  "servers": {
    "app1": {
      "provider": "linode",
      "hostname": "app1.nfweb.dev",
      "ssh": {
        "user": "nonfiction",
        "host": "app1.nfweb.dev"
      },
      "linode_id": 98222343
    }
  }
}
```

`~/.config/nf/state/sites.json`

```json
{
  "sites": {
    "sanjel-app1-staging": {
      "provider": "linode",
      "server": "app1",
      "environment": "staging",
      "hostname": "sanjel.app1.nfweb.dev",
      "url": "https://sanjel.app1.nfweb.dev/",
      "branch": "main"
    },
    "sanjel-app1-production": {
      "provider": "linode",
      "server": "app1",
      "environment": "production",
      "hostname": "sanjel.app1.nfweb.dev",
      "url": "https://sanjel.app1.nfweb.dev/",
      "branch": "main"
    },
    "sanjel-kinsta-staging": {
      "provider": "kinsta",
      "environment": "staging",
      "hostname": "sanjel-staging.kinsta.cloud",
      "url": "https://sanjel-staging.kinsta.cloud",
      "kinsta": {
        "company_id": "123456",
        "site_id": "234567",
        "environment_id": "345678"
      }
    },
    "sanjel-kinsta-production": {
      "provider": "kinsta",
      "environment": "production",
      "hostname": "www.sanjel.com",
      "url": "https://www.sanjel.com",
      "kinsta": {
        "company_id": "123456",
        "site_id": "234567",
        "environment_id": "345679"
      }
    },
    "sanjel-production": {
      "provider": "kinsta",
      "environment": "production",
      "hostname": "www.sanjel.com",
      "url": "https://www.sanjel.com",
      "kinsta": {
        "company_id": "123456",
        "site_id": "234567",
        "environment_id": "345679"
      }
    }
  }
}
```

## Command design

The current command surface is grouped and should stay that way:

- `nf server provision|list|show|delete`
- `nf site list|show`
- `nf repo init|commands|run <name>|package`
- `nf config init`
- `nf password derive <project-slug> <purpose>`

Provider-specific workflows should hang off those groups rather than inventing
new top-level commands.

The current CLI uses grouped commands:

- `nf server provision|list|show|delete`
- `nf site list|show` plus future site actions as stubs
- `nf repo init|commands|run <name>|package`
- `nf config init`
- `nf password derive <project-slug> <purpose>`

Repo-context commands only appear when `nf` is run inside a `.git`
repository.
Interactive commands prefer pickers over required positional arguments where a
safe choice can be made from known state; for example, `nf server show`,
`nf site show`, and `nf server delete` open a selector when no identifier is
provided.

The first guarded remote slice is `nf server provision`.
It defaults to dry-run, guides interactive use through Bubble Tea/Bubbles/Lip Gloss,
prints a reviewable plan, and can write a redacted cloud-init preview locally.
Flags act as shortcuts for prompts, and `--non-interactive` supports
scripted/test usage.
Actual remote execution is Linode-only for now and requires both `--execute`
and `--yes` plus the required local credentials.
Basic auth is not included in this first slice.

`nf server delete [id-or-name]` opens a server picker when no identifier is
provided, then prints a plan and asks for confirmation in interactive mode.
Non-interactive deletion remains dry-run unless explicitly run with
`--execute --yes`.

`nf repo init` writes `.nf/project.json`, `nf repo package` writes a local zip
artifact, and `nf server provision --execute --yes` can create a remote Linode
and DNS records. Local repo commands are direct repo workflows owned by `nf`,
not Makefile wrappers. They may mutate local repo or workbench files and
containers when explicitly invoked. Deploy and sync workflows are still not
implemented.

## Planned workflows

### Provision server

`nf server provision` creates a reusable host target such as `app1.nfweb.dev`.

For Linode, this includes Linode API provisioning, DNSimple A records, and TLS
challenge support for `nfweb.dev`.

The guarded first slice behaves as follows:

- default mode is dry-run
- `--execute` enables real remote work
- `--execute` must be paired with `--yes` for actual changes
- `--provider` currently accepts only `linode`
- DNSimple zone discovery happens only during actual execution
- `NF_SECRET_SALT` is used for derived passwords
- basic auth is intentionally omitted in this slice

Required environment for actual execution:

- `LINODE_CLI_TOKEN` or `LINODE_TOKEN`
- `DNSIMPLE_TOKEN`
- `NF_SECRET_SALT`

`DNSIMPLE_ACCOUNT_ID` defaults to `14` unless overridden.

Use `nf config init` to populate `~/.config/nf/.env` interactively.

### Shared state and site targeting

The CLI should keep shared server/site state separate from repository metadata.
Site resolution should understand named targets like `app1`, `sanjel-app1-staging`,
and `sanjel-app1-production`, with `app1` acting as a convenient alias for the
current project's primary Linode site.

Kinsta site records should store company, site, and environment IDs only; they do
not need SSH data.

## Provider model

### Linode

Linode is the first provider.

Expected responsibilities:

- create server instances
- support server inventory and metadata
- integrate with DNSimple for `nfweb.dev` hostnames
- support site installation on the created host

### Kinsta

Kinsta is a future provider.

Important implications:

- no Linode provisioning step
- deploy and sync workflows must still work
- push/pull behavior should be expressed through provider adapters
- production behavior may differ from Linode staging/light production

## Password derivation

Passwords for the same project slug should match across Linode environments like
`app1` and `app2`.

Derivation should use:

- `project.slug`
- `purpose`
- `NF_SECRET_SALT`

It should not use `server_host`.

This keeps cross-environment testing and workflows predictable.

Kinsta production is different and may use unique real passwords.

## Production database safety

Database pushes toward production or Kinsta must be treated as high risk.

The design should include guardrails such as:

- explicit environment selection
- provider policy checks
- confirmation for destructive actions
- safe exclusion or preservation of sensitive options and users where possible
- no silent password replacement on push-back flows

The key rule is that a database sync must not clobber passwords when pushing
back to production.

If the source database contains local or staging credentials, the push flow must
preserve or remap them according to provider policy.

## Suggested state responsibilities

Shared `nf` state should track:

- providers
- servers
- sites
- server-to-site relationships
- environment labels
- hostnames
- derived identifiers
- last known sync metadata

Project repos should only track:

- project slug/type
- WordPress/theme structure
- build/artifact recipe
- deploy aliases
- repo-local commands

## Initial implementation phases

### Phase 1: spec and state model

- finalize terminology
- finalize shared state schema
- finalize project metadata schema
- define provider policy interfaces
- define flake packaging and distribution shape

### Phase 2: safe local CLI skeleton

- add command parsing
- add config loading
- add state loading
- add read-only inspection commands

This phase now has an initial implementation in the repository. The remaining
provisioning, deploy, and sync phases stay future work.

### Phase 3: Linode provider

- provision server
- manage server records
- install site

### Phase 4: theme deploy

- build theme assets
- package theme artifact
- deploy theme artifact

### Phase 5: sync workflows

- database sync workflows
- uploads sync workflows
- full site sync workflows
- provider-safe production protections

### Phase 6: Kinsta adapter

- provider-specific deploy and sync behavior
- no Linode assumptions in shared command layer

### Phase 7: flake-based distribution

- publish the `nf` executable through `flake.nix`
- support consumption from WordPress project flakes
- support optional local dev shells and Nix profile install workflows

## Open questions

- Exact shared state file format.
- Whether state sync should use one repo or multiple private repos.
- Final provider policy shape for destructive operations.
- How to represent environments that share a project slug but differ by
  provider.
- Whether package naming should be standardized across all projects or allow
  project overrides.
- How much push/pull automation should be allowed for production compared with
  staging.
- How server inventory should record externally managed hosts.
- How the initial `flake.nix` should structure package, app, and dev shell
  outputs.

## Current source of truth for examples

- Client repo: `/home/jon/src/nonfiction/client`
- Client runtime lives in `theme/`
- Local workbench lives in `workbench/`
- Local URL: `http://localhost:18181`
- Theme source: `theme`
- Theme slug likely: `theme`

This README should stay aligned with those facts and with the shared planning
decisions listed in the task description.

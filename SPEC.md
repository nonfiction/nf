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
* **env**: remote environment for a site, usually `live` or `staging`.

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

* `.nf/project.json`
* local WordPress dev env
* theme tasks
* theme packaging/artifact recipe
* repo-local remotes such as `production` or `staging`

Repo-local remotes map names to global remote site/env records:

```json
{
  "deploy": {
    "remotes": {
      "production": {
        "site_id": "client-app1-linode",
        "env": "live"
      }
    }
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

Kinsta records should use Kinsta IDs from API state. Kinsta must not assume Linode-specific SSH fields, `nfweb.dev` hostnames, or Linode provisioning/delete paths.

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
  snapshots/<project-slug>/<snapshot-name>/
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
* Secrets must not be written to `.nf/project.json`, state cache, generated env metadata, docs, or tests.

## Command surface

Current public command groups:

```text
nf init
nf provider ...
nf target ...
nf site ...
nf site env ...
nf remote ...
nf theme ...
nf env ...
nf config ...
nf password ...
```

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
* [x] `nf target list`
* [x] `nf target show <target>`
* [x] `nf target add linode <name>` create/ensure target scaffold
* [x] `nf site refresh` target-based scaffold
* [x] `nf site list [--refresh]`
* [x] `nf site show <site-id-or-alias>`
* [x] `nf site env list [site-id]`
* [x] `nf site env show <site-id> <env>`
* [x] `nf remote add <name> <site-id> <env>` with cache validation
* [x] `nf remote show <name>`
* [x] `nf remote remove <name>`
* [x] `nf remote list`
* [x] `nf theme tasks`
* [x] `nf theme package`
* [x] direct theme tasks from `.nf/project.json`
* [x] `nf env up`
* [x] `nf env down`
* [x] `nf env logs`
* [x] `nf env reset`
* [x] `nf env show`
* [x] `nf env shell`
* [x] `nf env wp -- <args>`
* [x] `nf env snapshot add [name]`
* [x] `nf env snapshot list`
* [x] `nf env snapshot use [name]`
* [x] `nf env snapshot remove [name]`
* [x] `nf config init`
* [x] `nf config set-base-domain <domain>`
* [x] `nf config set-default-wp-email <email>`
* [x] `nf config set-default-wp-user <user>`
* [x] `nf config set-linode-default-region <region>`
* [x] `nf config set-linode-default-type <type>`
* [x] `nf config set-linode-default-image <image>`
* [x] `nf config set-linode-default-user <user>`
* [x] `nf config show`
* [x] `nf password set-salt <salt>`
* [x] `nf password show-salt`
* [x] `nf password derive <scope> <value...>`

### Preflight-only / scaffolded

* [ ] `nf site refresh`: currently lists cached targets; remote target site discovery not implemented yet
* [ ] `nf site env shell <site-id> <env>`: validates cache, does not execute remote shell yet
* [ ] `nf site env wp <site-id> <env> -- <args>`: validates cache, does not run remote wp-cli yet
* [ ] `nf env push <remote>`: validates repo remote/cache, does not sync yet
* [ ] `nf env pull <remote>`: validates repo remote/cache, does not sync yet

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
nf target list
nf target show app1-linode
```

`nf site refresh` then fans out from the cached target list. It must not refresh providers directly.

## Site/env cache flow

Current site/env cache file:

```text
~/.local/state/nf/sites.json
```

Current readers:

* `nf site list`
* `nf site show`
* `nf site env list`
* `nf site env show`
* `nf remote add`
* `nf remote show`
* `nf env push/pull` preflight
* `nf site env shell/wp` preflight

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
.nf/project.json
```

Tracked fields include:

* schema version
* project slug/name/type
* WordPress/theme structure
* local env intent
* build/artifact recipe
* repo remotes
* theme tasks

Example shape:

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
    "uploads_path": "uploads"
  },
  "deploy": {
    "remotes": {}
  },
  "tasks": {}
}
```

## Theme workflow

Theme task rules:

* tasks come from `.nf/project.json` `tasks`
* string tasks run through `sh -lc`
* array tasks execute directly
* passthrough args follow `--`
* print the command preview before execution

Packaging rules:

* `nf theme package` zips existing theme files only
* it does not run Composer, npm, or asset builds first
* deploy artifacts must include built files when needed, such as `vendor/` or `assets/dist/`
* `artifact.path` may contain `{version}`
* `{version}` resolves from `theme/style.css` `Version:` first, then `theme/package.json` `version`
* fail clearly if neither version source exists

## Local env model

Built-in env commands come from `env` metadata in `.nf/project.json`.

Current built-ins:

* `up`
* `down`
* `logs`
* `reset`
* `show`
* `shell`
* `wp`

Rules:

* env ports are derived deterministically from project slug
* `env.ports.wordpress` and `env.ports.mailpit` may override individually
* zero or missing ports fall back to derived ports
* `nf env up` should be idempotent
* `nf env up` preflights WordPress and Mailpit host ports before Docker Compose starts
* `nf env show` prints paths, compose project name, and URLs without starting Docker
* generated env scaffolding stays under `NF_DATA_HOME`, not in project repos

Snapshots:

* stored under `NF_DATA_HOME/snapshots/<project-slug>/<snapshot-name>/`
* contain `snapshot.json`, `database.sql.gz`, `wp-content.tar.gz`
* `wp-content.tar.gz` includes uploads/plugins/mu-plugins/languages only
* restore creates a pre-restore safety snapshot

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

Goal: make the public command model match `provider -> target -> site -> env` plus repo/local commands.

Status:

* [x] remove old public `server`/`instance` routes
* [x] remove top-level local env aliases
* [x] add `provider`, `target`, `site`, `site env`, `remote`, `theme`, `env`, `config`, `password`
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

* [x] `nf site refresh` target-based scaffold
* [x] cached site/env readers
* [x] repo remote validation against cache
* [ ] Linode target SSH reader for `/var/lib/nf/sites.json`
* [ ] Kinsta site/env API reader
* [ ] normalized `sites.json` writer from discovered records
* [ ] conflict/error reporting for stale or invalid target cache

### Phase 5: Remote execution

Goal: safely run remote shell and wp-cli through provider-aware env records.

Status:

* [x] preflight-only `nf site env shell`
* [x] preflight-only `nf site env wp`
* [ ] Linode SSH execution adapter
* [ ] Kinsta execution adapter or explicit unsupported behavior
* [ ] audit output and command previews

### Phase 6: Theme artifact deployment

Goal: deploy packaged theme artifacts through repo remotes.

Status:

* [x] theme task execution
* [x] theme packaging
* [x] repo remote model
* [ ] artifact upload/publish command
* [ ] provider adapters for artifact deploy
* [ ] rollback/release metadata

### Phase 7: Database/uploads sync

Goal: add guarded pull/push workflows.

Status:

* [x] preflight-only `nf env push <remote>`
* [x] preflight-only `nf env pull <remote>`
* [ ] explicit source/destination planning
* [ ] production credential preservation
* [ ] uploads protections
* [ ] confirmation gates for destructive direction
* [ ] Kinsta sync adapter
* [ ] Linode sync adapter

### Phase 8: Distribution and team polish

Goal: make `nf` comfortable for team-wide daily use.

Status:

* [ ] shared state sync or team cache workflow
* [ ] improved interactive selectors
* [ ] richer diagnostics for missing config/cache
* [ ] release/update workflow
* [ ] onboarding docs

## Non-goals for now

* public/general-purpose WordPress framework behavior
* old command compatibility aliases
* hidden mutation of provider inventory from repo-local commands
* production sync without explicit review and confirmation
* storing secrets in project metadata or state cache

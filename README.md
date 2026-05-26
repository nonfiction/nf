# nf planning spec

`nf` is the planned agency-level CLI for nonfiction WordPress infrastructure,
deployment, and shared operational state.

This repository documents the design before implementation.
No executable CLI code should be added yet.

The intended packaging path for `nf` is a `flake.nix` in this repository that
builds and distributes the CLI to the nonfiction team and lets WordPress
project flakes consume it as an input.

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

Example `.nf/project.json` for a client project:

```json
{
  "project_slug": "client",
  "project_name": "Client",
  "theme_slug": "theme",
  "theme_source": "theme",
  "local_workbench_url": "http://localhost:18181",
  "default_provider": "linode",
  "deploy_targets": {
    "staging": {
      "provider": "linode",
      "server": "app1",
      "site": "client.app1.nfweb.dev"
    },
    "production": {
      "provider": "kinsta",
      "site": "client"
    }
  }
}
```

Notes:

- This file is safe to store in the project repo.
- It must not contain secrets.
- It should not duplicate mutable server state.
- It should describe intent, not replace shared nf state.

## Command design

The planned command surface should include at least:

- `nf provision-server`
- `nf import-server`
- `nf list servers`
- `nf show server`
- `nf remove server`
- `nf install-site`
- `nf list sites`
- `nf show site`
- `nf remove site`
- `nf build-theme`
- `nf package-theme`
- `nf deploy-theme`
- `nf push db`
- `nf pull db`
- `nf push uploads`
- `nf pull uploads`
- `nf push site`
- `nf pull site`

The command set should stay provider-agnostic where possible and delegate to
provider adapters when it must not.

## Planned workflows

### Provision server

`provision-server` creates a reusable host target such as `app1.nfweb.dev`.

For Linode, this includes Linode API provisioning and any required DNSimple
records and TLS challenge support for `nfweb.dev`.

### Import server

`import-server` records an already-existing host into shared nf state.

This is needed when a server exists outside `nf` or was created manually.

### Install site

`install-site` creates the WordPress site on a server.

For a client project on `app1`, the planned names are:

- remote path: `/var/www/client`
- database: `client`
- database user: `client`

No generic `wordpress` database or folder names should be used for that site.

The resulting hostname is expected to look like `client.app1.nfweb.dev`.

### Build theme

`build-theme` performs the local build needed for deployment.

It must include the final deploy artifact contents, especially:

- `vendor/`
- `assets/dist/`

### Package theme

`package-theme` creates a versioned theme archive such as `theme.zip`.

The packaged artifact must match the direct deploy artifact posture.

### Deploy theme

`deploy-theme` syncs the built theme artifact to the selected site.

The local build is the source of truth.
Deploy should not rely on an incomplete source tree.

### Push and pull database/uploads/site

The CLI should support targeted sync operations:

- database only
- uploads only
- full site bundle where appropriate

These commands must be provider-aware and policy-controlled.

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

- `project_slug`
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

- project slug
- theme slug
- local workbench hints
- optional deployment preferences

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

### Phase 3: Linode provider

- provision server
- import server
- list/show/remove servers
- install site

### Phase 4: theme deploy

- build theme
- package theme
- deploy theme

### Phase 5: sync workflows

- push/pull db
- push/pull uploads
- push/pull site
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

# nf agent guide

## Project shape

* `nf` is a Go CLI.
* The executable entrypoint is `cmd/nf/main.go`.
* The entrypoint calls `internal/cli.Run`.
* Main command groups are:
  * `nf server ...`
  * `nf site ...`
  * `nf runtime ...`
  * `nf repo ...`
  * `nf config ...`
  * `nf password ...`
* Do not add old compatibility routes or alternate top-level command shapes unless explicitly requested.
* Keep the command surface grouped by responsibility. Provider-specific behavior should live behind these command groups, not in new unrelated top-level commands.

## Command surface

The current project command surface is intentionally small:

* `nf repo init`
* `nf repo tasks`
* `nf repo package`
* `nf runtime up`
* `nf runtime down`
* `nf runtime logs`
* `nf runtime reset`
* `nf runtime info`
* `nf runtime shell`
* `nf runtime wp`
* `nf up`
* `nf down`
* `nf logs`
* `nf reset`
* `nf info`
* `nf shell`
* `nf wp`
* direct repo tasks from `.nf/project.json`

Do not re-add public routes such as:

* `repo run`
* `setup`
* `fresh`
* `restart`
* `install-theme`
* `activate-theme`
* top-level repo aliases
* top-level `list` / `show`

unless the user explicitly asks for that command design.

Reserved site commands exist in help output but are not implemented yet:

* `nf site install`
* `nf site delete`
* `nf site deploy`
* `nf site push`
* `nf site pull`

If implementing them, keep them policy-gated and provider-aware.

## Commands worth using

Fast checks:

```sh
go test ./...
go test ./internal/cli
```

CLI smoke checks:

```sh
go run ./cmd/nf --help
go run ./cmd/nf server list
go run ./cmd/nf site list
go run ./cmd/nf repo help
```

Nix smoke checks:

```sh
nix run .#nf -- --help
nix build .#nf -L
nix develop -c nf --help
```

Flake builds use the git source snapshot. Stage newly added files before trusting `nix run .#nf` or `nix build .#nf`; otherwise Nix may silently build without untracked Go files.

## Config, state, and secrets

Local config defaults to:

```text
~/.config/nf/
```

If `XDG_CONFIG_HOME` is set, config lives under:

```text
$XDG_CONFIG_HOME/nf
```

Tests and isolated runs may override this with:

```sh
NF_CONFIG_HOME=/path/to/nf-config
```

Expected local layout:

```text
~/.config/nf/
  .env
  state/
    servers.json
    sites.json
    projects.json
  runtimes/
    <project-slug>/
```

Local secrets/config are read from:

```text
~/.config/nf/.env
```

`nf config init` can populate missing values interactively.

Shared state lives under:

```text
~/.config/nf/state/
```

Current shared state files:

* `servers.json`
* `sites.json`
* `projects.json`

Local WordPress runtime lives under:

```text
~/.config/nf/runtimes/<project-slug>/
```

or the equivalent `NF_CONFIG_HOME` test path.

A runtime is `nf`'s generated local WordPress environment for a project. It contains the Docker/WordPress scaffolding and mutable local state used to run, reset, snapshot, and sync the project during development.

The runtime is generated and owned by `nf`. Do not scaffold Docker runtime files into project repos by default.

Repo-local metadata lives at:

```text
.nf/project.json
```

This file is safe project intent/config only. Do not put any of the following in `.nf/project.json`:

* API tokens
* SSH keys
* database credentials
* live passwords
* mutable server state
* mutable site state
* provider secrets

Required/expected environment values:

* `NF_SECRET_SALT` for password derivation
* `LINODE_CLI_TOKEN` or `LINODE_TOKEN` for Linode execution
* `DNSIMPLE_TOKEN` for DNSimple operations
* optional `DNSIMPLE_ACCOUNT_ID`, defaulting to `14`

## Repo-context behavior

`nf runtime ...` and `nf repo ...` commands are the local project command surface.

Repo tasks come from:

```text
.nf/project.json tasks
```

They execute from the project root.

`nf repo init` defaults `project.slug` from the current git root folder.

The default WordPress theme directory convention is:

```text
theme/
```

Generated metadata should default these values to `theme` unless an explicit override is provided:

* `wordpress.theme_path`
* `wordpress.theme_slug`
* `runtime.theme_mount_slug`

String tasks run through:

```sh
sh -lc
```

Array tasks execute directly as argv.

Passthrough args follow `--`.

Command execution should print the underlying command preview before running it.

Repo-context commands are hidden or rejected outside a `.git` repo. Keep that distinction when adding local workflow commands.

`nf repo package` only zips existing theme files. It does not run Composer, npm, or asset builds first.

Deploy artifacts must include built files when the project expects them, such as:

* `vendor/`
* `assets/dist/`

`artifact.path` may contain:

```text
{version}
```

Resolve `{version}` from:

1. `theme/style.css` `Version:`
2. `theme/package.json` `version`

Fail clearly if neither exists.

## Runtime behavior

Built-in runtime commands come from `runtime` metadata in `.nf/project.json`.

Current built-ins:

* `up`
* `down`
* `logs`
* `reset`
* `info`
* `shell`
* `wp`

Runtime ports are derived deterministically from the project slug. `runtime.ports.wordpress` and `runtime.ports.mailpit` may override them individually; zero or missing values fall back to the derived ports.

`nf runtime up` should be idempotent:

* ensure the managed runtime exists
* start Docker Compose
* install WordPress if missing
* ensure the mounted theme is active

`nf runtime up` should preflight the WordPress and Mailpit host ports before Docker Compose starts. `nf runtime info` should print the local runtime paths, compose project name, and URLs without starting Docker.

`nf runtime up` and `nf runtime reset` should print a success line followed by the full runtime info block. `nf runtime down` should print a success line followed by the short runtime info block.

`nf runtime reset` is destructive for the local runtime only:

* run Docker Compose down with volumes removed
* recreate the runtime
* reinstall WordPress if missing
* ensure the mounted theme is active

Runtime-generated files should stay under `~/.config/nf/runtimes/<project-slug>/`.

Do not write generated runtime scaffolding into project repos unless the user explicitly changes that design.

## Theme packaging behavior

`nf repo package` should:

* load project metadata from `.nf/project.json`
* default source to `wordpress.theme_path`, falling back to `theme`
* default output to `artifact.path`, falling back to `dist/<project-slug>-v{version}.zip`
* resolve `{version}` from the theme source
* create a ZIP from existing files
* skip `.git`, `.DS_Store`, `node_modules`, and the output ZIP itself
* support `--dry-run`

It should not:

* run Composer
* run npm
* build assets
* install dependencies
* deploy the artifact

Build/test/prep steps belong in repo tasks such as:

* `nf repo build`
* `nf repo test`
* `nf repo composer`
* `nf repo npm`

## State behavior

Shared state should stay separate from project metadata.

Project repos should track:

* project slug/type
* WordPress/theme structure
* runtime intent
* build/artifact recipe
* deploy targets
* repo tasks

Shared state should track:

* providers
* servers
* sites
* projects
* server-to-site relationships
* environment labels
* hostnames
* provider IDs
* remote paths
* last known operational metadata when needed

`nf server list` and `nf site list` read shared state and print tables.

`nf server show` and `nf site show` print matching records as JSON.

When no identifier is supplied in interactive mode, `server show`, `site show`, and `server delete` should prefer selectors over forcing positional arguments.

`nf site show` may resolve deploy targets from `.nf/project.json`.

Example placeholder target alias shape:

```json
{
  "deploy": {
    "targets": {
      "app1": "client-app1-production",
      "staging": "client-kinsta-staging",
      "production": "client-kinsta-production"
    }
  }
}
```

Use neutral placeholder examples such as:

* project slug: `client`
* project name: `Client`
* server: `app1`
* server hostname: `app1.nfweb.dev`
* Linode site target: `client-app1-production`
* Linode site URL: `https://client.app1.nfweb.dev/`
* Kinsta target: `client-kinsta-production`
* Kinsta placeholder URL: `https://www.example.com/`

Do not use real client names in docs, tests, or examples unless the user explicitly asks.

## UI behavior

Interactive UI prompts/selectors live in:

```text
internal/ui
```

They use Bubble Tea/Bubbles/Lip Gloss.

Interactive commands should prefer selectors over required positional args when the choice can be safely inferred from known state.

Non-interactive commands must not prompt.

If a remote operation is potentially destructive or creates infrastructure, non-interactive execution must require explicit flags such as:

```text
--execute --yes
```

## Provider rules

### Linode

Linode is the only implemented remote provider right now.

Current Linode responsibilities:

* server provisioning
* server inventory from shared state
* server deletion
* DNSimple-backed hostnames for `nfweb.dev`
* writing state records for provisioned servers/sites

`nf server provision` is dry-run by default.

Actual remote execution requires:

```text
--execute --yes
```

in non-interactive mode, plus credentials.

`nf server delete` is interactive by default with a picker and confirmation.

In non-interactive mode, deletion remains dry-run unless:

```text
--execute --yes
```

is supplied.

A Linode 404/not-found during deletion means the remote server is already gone and stale local state can still be cleaned.

### Kinsta

Kinsta is future adapter work.

Kinsta records should use Kinsta IDs from site state.

Kinsta must not:

* use Linode provisioning paths
* require SSH server fields
* assume an `nfweb.dev` host
* share Linode-specific delete/provision behavior

Future Kinsta work should focus on:

* deploying packaged theme artifacts
* inspecting environments
* pulling/pushing database and uploads where supported
* respecting production safety policies

## Safety rules

Treat these as high risk:

* production database push
* uploads push to production
* full site sync toward production
* any workflow that can overwrite live credentials
* any workflow that can overwrite live uploads
* any workflow that destroys remote infrastructure

Future DB/uploads sync must:

* require explicit source and destination
* identify provider and environment
* preserve production passwords
* preserve sensitive options and users where possible
* avoid silently replacing live credentials with local/staging credentials
* print a reviewable plan
* require confirmation for destructive changes

The key rule: never silently clobber production credentials.

## Roadmap

Near-term order:

1. Keep the standard agency WordPress theme repo workflow comfortable enough to replace per-project scripts.
2. Implement Linode site lifecycle after server provisioning.
3. Add theme artifact deployment.
4. Add database/uploads pull-push workflows with production protections.
5. Add Kinsta deploy/sync adapters.
6. Polish team distribution and shared state sync.

Keep README and this file aligned as command names, state layout, safety posture, and provider behavior change.

## Hygiene

Do not touch unrelated repositories unless the user explicitly asks.

Do not commit:

* secrets
* generated caches
* `.direnv`
* build outputs
* local state
* runtime files
* temporary artifacts

Keep examples neutral and fictional.

Use the README as the user-facing source of truth. Use this file as the implementation guide for agents working in the repo.

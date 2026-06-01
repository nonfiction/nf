# nf agent guide

## Project shape

* `nf` is a Go CLI.
* The executable entrypoint is `cmd/nf/main.go`.
* The entrypoint calls `internal/cli.Run`.
* Main command groups are:
  * `nf init`
  * `nf provider ...`
  * `nf target ...`
  * `nf site ...`
  * `nf site env ...`
  * `nf remote ...`
  * `nf theme ...`
  * `nf env ...`
  * `nf config ...`
  * `nf password ...`
* Do not add old compatibility routes or alternate top-level command shapes unless explicitly requested.
* Do not re-add `nf server ...`, `nf instance ...`, or top-level `nf up/down/logs/reset/info/shell/wp` aliases unless explicitly requested.

## Current command surface

* `nf init`
* `nf provider list`
* `nf provider show <provider>`
* `nf provider check <provider>` (config preflight only; no remote API call)
* `nf target list` / `nf target show <target>`
* `nf site refresh` (local cache path only; provider fetch not implemented yet)
* `nf site list [--refresh]`
* `nf site show <site-id-or-alias>`
* `nf site env list [site-id]`
* `nf site env show <site-id> <env>`
* `nf site env shell <site-id> <env>` (preflight only; remote execution not implemented yet)
* `nf site env wp <site-id> <env> -- <args>` (preflight only; remote execution not implemented yet)
* `nf remote add <name> <site-id> <env>`
* `nf remote remove <name>`
* `nf remote list`
* `nf theme tasks`
* `nf theme package`
* direct theme tasks from `.nf/project.json`
* `nf env up`
* `nf env down`
* `nf env logs`
* `nf env reset`
* `nf env show`
* `nf env shell`
* `nf env wp -- <args>`
* `nf env push <remote>` (preflight only; sync not implemented yet)
* `nf env pull <remote>` (preflight only; sync not implemented yet)
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

## Commands worth using

Fast checks:

```sh
go test ./...
go test ./internal/cli
```

CLI smoke checks:

```sh
go run ./cmd/nf --help
go run ./cmd/nf provider list
go run ./cmd/nf provider check linode
go run ./cmd/nf site list
go run ./cmd/nf env help
go run ./cmd/nf site env help
```

Nix smoke checks:

```sh
nix run .#nf -- --help
nix build .#nf -L
nix develop -c nf --help
```

Flake builds use the git source snapshot. Stage newly added files before trusting `nix run .#nf` or `nix build .#nf`; otherwise Nix may silently build without untracked Go files.

## Config, state, data, and secrets

Config defaults to:

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

State/cache defaults to:

```text
~/.local/state/nf/
```

If `XDG_STATE_HOME` is set, state lives under:

```text
$XDG_STATE_HOME/nf
```

Tests and isolated runs may override this with:

```sh
NF_STATE_HOME=/path/to/nf-state
```

Generated env data defaults to:

```text
~/.local/share/nf/
```

If `XDG_DATA_HOME` is set, data lives under:

```text
$XDG_DATA_HOME/nf
```

Tests and isolated runs may override this with:

```sh
NF_DATA_HOME=/path/to/nf-data
```

Expected local layout:

```text
~/.config/nf/
  config.json
  .env
~/.local/state/nf/
  sites.json
  projects.json
~/.local/share/nf/
  envs/<project-slug>/
  snapshots/<project-slug>/<snapshot-name>/
```

Local secrets/config are read from:

```text
~/.config/nf/.env
```

`nf config init` can populate missing values interactively.

Required/expected environment values:

* `NF_SECRET_SALT` for password derivation
* `LINODE_TOKEN` for Linode execution (`LINODE_CLI_TOKEN` is accepted for convenience)
* `DNSIMPLE_TOKEN` for DNSimple operations
* `KINSTA_API_KEY` for Kinsta operations
* optional `DNSIMPLE_ACCOUNT_ID`, defaulting to `14`

Do not store secrets in `.nf/project.json`, local state cache, or generated env metadata.

## Provider/target/site/env model

There are two contexts:

* global/provider context: provider inventory and remote envs
* repo/local context: local env, theme tasks, packaging, and repo remotes

Global hierarchy:

```text
provider -> target -> site -> env
```

Providers:

* `dnsimple`
* `kinsta`
* `linode`

Targets are deployable places, such as `kinsta` or `app1-linode`.

Sites are named `<site>-<target>`.

Remote env display IDs are `<env>-<site>-<target>`, with env values such as `live` and `staging`.

Provider truth is canonical remotely:

* Kinsta API is canonical for Kinsta sites/envs.
* Linode API is canonical for Linode servers/targets.
* Linode-hosted site/env truth lives on each target at `/var/lib/nf/sites.json`, read over SSH as the standard user.

Local inventory cache under `NF_STATE_HOME` is disposable. Repo-local config should store remotes only, not global inventory.

## Project-context behavior

`nf env ...`, `nf init`, `nf theme ...`, and `nf remote ...` are the local project command surface.

Theme tasks come from:

```text
.nf/project.json tasks
```

They execute from the project root.

`nf init` defaults `project.slug` from the current git root folder.

The default WordPress theme directory convention is:

```text
theme/
```

Generated metadata should default these values to `theme` unless an explicit override is provided:

* `wordpress.theme_path`
* `wordpress.theme_slug`
* `env.theme_mount_slug`

String tasks run through:

```sh
sh -lc
```

Array tasks execute directly as argv.

Passthrough args follow `--`.

Command execution should print the underlying command preview before running it.

Project-context commands are hidden or rejected outside a `.git` repo. Keep that distinction when adding local workflow commands.

`nf theme package` only zips existing theme files. It does not run Composer, npm, or asset builds first.

Deploy artifacts must include built files when the project expects them, such as:

* `vendor/`
* `assets/dist/`

`artifact.path` may contain `{version}`. Resolve `{version}` from:

1. `theme/style.css` `Version:`
2. `theme/package.json` `version`

Fail clearly if neither exists.

## Local env behavior

Built-in env commands come from `env` metadata in `.nf/project.json`.

Current built-ins:

* `up`
* `down`
* `logs`
* `reset`
* `show`
* `shell`
* `wp`

Env ports are derived deterministically from the project slug. `env.ports.wordpress` and `env.ports.mailpit` may override them individually; zero or missing values fall back to the derived ports.

`nf env up` should be idempotent:

* ensure the managed env exists
* start Docker Compose
* install WordPress if missing
* ensure the mounted theme is active

`nf env up` should preflight the WordPress and Mailpit host ports before Docker Compose starts. `nf env show` should print the local env paths, compose project name, and URLs without starting Docker.

`nf env up` and `nf env reset` should print a success line followed by the full env info block. `nf env down` should print a success line followed by the short env info block.

`nf env reset` is destructive for the local env only:

* run Docker Compose down with volumes removed
* recreate the env
* reinstall WordPress if missing
* ensure the mounted theme is active

Env-generated files should stay under `~/.local/share/nf/envs/<project-slug>/` or equivalent `NF_DATA_HOME` path.

Env snapshots should stay under `~/.local/share/nf/snapshots/<project-slug>/<snapshot-name>/` or equivalent `NF_DATA_HOME` path.

Snapshot contents:

* `snapshot.json`
* `database.sql.gz`
* `wp-content.tar.gz`

The `wp-content` archive should include only `uploads/`, `plugins/`, `mu-plugins/`, and `languages/`. Do not include themes.

`nf env snapshot use` should create a safety snapshot named `YYYY-MM-DD-HHMMSS-pre-restore` before importing the selected snapshot.

Do not write generated env scaffolding into project repos unless the user explicitly changes that design.

## State behavior

Shared state/cache stays separate from project metadata.

Project repos should track:

* project slug/type
* WordPress/theme structure
* local env intent
* build/artifact recipe
* repo remotes
* theme tasks

Shared state/cache should track normalized provider inventory only. It is disposable and must not be treated as canonical provider truth.

`nf target list/show` read target records from the local `servers.json` cache for now. Keep user-facing wording as target.

`nf site refresh` currently reports local cache paths only. It must not claim provider refresh until remote provider fetch is implemented.

`nf site list`, `nf site show`, and `nf site env list/show` read local cached site records for now.

`nf site show` may resolve repo remote aliases from `.nf/project.json`.

Use neutral placeholder examples such as:

* project slug: `client`
* project name: `Client`
* target: `app1-linode`
* target hostname: `app1.nfweb.dev`
* Linode site ID: `client-app1-linode`
* Linode site URL: `https://client.app1.nfweb.dev/`
* Kinsta site ID: `client-kinsta`
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

Provider commands are being refit around `provider -> target -> site -> env`.

Kinsta records should use Kinsta IDs from site/env state.

Kinsta must not:

* use Linode provisioning paths
* require SSH target fields unless the API/env requires them
* assume an `nfweb.dev` host
* share Linode-specific delete/provision behavior

Linode target work should remain provider-aware and must not recreate the old public `nf server ...` command surface without explicit approval.

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

1. Finish the provider/target/site/env command refactor.
2. Keep the standard agency WordPress theme repo workflow comfortable enough to replace per-project scripts.
3. Implement provider inventory refresh.
4. Add theme artifact deployment through repo remotes.
5. Add database/uploads pull-push workflows with production protections.
6. Add Kinsta deploy/sync adapters.
7. Polish team distribution and shared state sync.

Keep README and this file aligned as command names, state layout, safety posture, and provider behavior change.

## Hygiene

Do not touch unrelated repositories unless the user explicitly asks.

Do not commit:

* secrets
* generated caches
* `.direnv`
* build outputs
* local state
* generated env files
* temporary artifacts

Keep examples neutral and fictional.

Use the README as the user-facing source of truth. Use this file as the implementation guide for agents working in the repo.

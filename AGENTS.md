# nf agent guide

## Project shape

* `nf` is a Go CLI.
* The executable entrypoint is `cmd/nf/main.go`.
* The entrypoint calls `internal/cli.Run`.
* Main command groups are:
  * `nf server ...`
  * `nf site ...`
  * `nf instance ...`
  * `nf init`
  * `nf theme ...`
  * `nf config ...`
  * `nf password ...`
* Do not add old compatibility routes or alternate top-level command shapes unless explicitly requested.
* Keep the command surface grouped by responsibility. Provider-specific behavior should live behind these command groups, not in new unrelated top-level commands.

## Command surface

The current project command surface is intentionally small:

* `nf init`
* `nf theme tasks`
* `nf theme package`
* `nf instance up`
* `nf instance down`
* `nf instance logs`
* `nf instance reset`
* `nf instance info`
* `nf instance shell`
* `nf instance wp`
* `nf instance snapshot create [name]`
* `nf instance snapshot list`
* `nf instance snapshot restore [name]`
* `nf instance snapshot delete [name]`
* `nf instance snapshots` (alias for `nf instance snapshot list`)
* `nf up`
* `nf down`
* `nf logs`
* `nf reset`
* `nf info`
* `nf shell`
* `nf wp`
* direct theme tasks from `.nf/project.json`

Do not re-add public routes such as:

* `legacy run route`
* `setup`
* `fresh`
* `restart`
* `install-theme`
* `activate-theme`
* top-level legacy aliases
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
go run ./cmd/nf theme help
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
  instances/
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

Local WordPress instance lives under:

```text
~/.config/nf/instances/<project-slug>/
```

or the equivalent `NF_CONFIG_HOME` test path.

An instance is `nf`'s generated local WordPress environment for a project. It contains the Docker/WordPress scaffolding and mutable local state used to run, reset, snapshot, and sync the project during development.

The instance is generated and owned by `nf`. Do not scaffold Docker instance files into project repos by default.

Project metadata lives at:

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

Treat cloud-init user-data as sensitive because it may include live provider credentials; previews should redact the DNSimple token.

## Project-context behavior

`nf instance ...`, `nf init`, and `nf theme ...` commands are the local project command surface.

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
* `instance.theme_mount_slug`

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

`artifact.path` may contain:

```text
{version}
```

Resolve `{version}` from:

1. `theme/style.css` `Version:`
2. `theme/package.json` `version`

Fail clearly if neither exists.

## Instance behavior

Built-in instance commands come from `instance` metadata in `.nf/project.json`.

Current built-ins:

* `up`
* `down`
* `logs`
* `reset`
* `info`
* `shell`
* `wp`

Instance ports are derived deterministically from the project slug. `instance.ports.wordpress` and `instance.ports.mailpit` may override them individually; zero or missing values fall back to the derived ports.

`nf instance up` should be idempotent:

* ensure the managed instance exists
* start Docker Compose
* install WordPress if missing
* ensure the mounted theme is active

`nf instance up` should preflight the WordPress and Mailpit host ports before Docker Compose starts. `nf instance info` should print the local instance paths, compose project name, and URLs without starting Docker.

`nf instance up` and `nf instance reset` should print a success line followed by the full instance info block. `nf instance down` should print a success line followed by the short instance info block.

`nf instance reset` is destructive for the local instance only:

* run Docker Compose down with volumes removed
* recreate the instance
* reinstall WordPress if missing
* ensure the mounted theme is active

Instance-generated files should stay under `~/.config/nf/instances/<project-slug>/`.

Instance snapshots should stay under `~/.config/nf/snapshots/<project-slug>/<snapshot-name>/`.

Snapshot contents:

* `snapshot.json`
* `database.sql.gz`
* `wp-content.tar.gz`

The `wp-content` archive should include only `uploads/`, `plugins/`, `mu-plugins/`, and `languages/`. Do not include themes.

`nf instance snapshot restore` should create a safety snapshot named `YYYY-MM-DD-HHMMSS-pre-restore` before importing the selected snapshot.

Do not write generated instance scaffolding into project repos unless the user explicitly changes that design.

## Theme packaging behavior

`nf theme package` should:

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

Build/test/prep steps belong in theme tasks such as:

* `nf theme build`
* `nf theme test`
* `nf theme composer`
* `nf theme npm`

## State behavior

Shared state should stay separate from project metadata.

Project repos should track:

* project slug/type
* WordPress/theme structure
* instance intent
* build/artifact recipe
* target aliases
* theme tasks

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

For server provisioning records, keep the Ubuntu/PHP metadata in dedicated `os` and `php` objects with `os.family: ubuntu`, `os.version`, `package_source: ubuntu-native`, the derived PHP service/socket, and the versioned package list.

`nf server list` and `nf site list` read shared state and print tables.

`nf server show`, `nf server root-password`, and `nf site show` print matching records or derived values from shared state.

`nf server provision` is site-agnostic. It uses `--name` and `--hostname`, defaults to Linode plus DNSimple, and writes only `servers.json` with generic provider_id, nested linode, os, php, firewall, dns/tls/services, and no secrets. It waits for SSH/TLS/health by default, `--no-wait` skips that finalization, and reruns resume partial phases instead of recreating the Linode.

Server baseline expectations:

* cloud-init sets `hostname`/`fqdn`, manages `/etc/hosts`, and hardens SSH for key-only access
* UFW is configured before enable, and the managed Linode firewall mirrors the same 22/80/443 inbound set when used
* baseline directories are `/var/www`, `/var/www/sites`, `/var/www/shared`, and `/var/log/nginx/sites`
* PHP tuning lands in `/etc/php/<version>/fpm/conf.d/99-nf-wordpress.ini`
* MariaDB is enabled but no database/users/grants are created at provision time
* Nginx snippets are written for future site vhost generation, and a neutral server health vhost is created for the server hostname only; that health page lives at `https://<hostname>` and `/healthz`, while future site vhosts are separate subdomains and are not created during provisioning
* certbot renewal hooks reload nginx
* `/etc/nf/server.json` stores non-secret machine facts only
* Node/npm are not installed by default
* if TLS finalization fails, the recovery helper is `ssh nonfiction@<hostname> "sudo /usr/local/bin/nf-enable-wildcard-tls"`

Record root credential metadata as `credentials.root` with `derived: true`, `identity: <hostname>`, `purpose: linode-root`, and `stored: false`; do not store a password.

The DNSimple token is used to render cloud-init but is not stored in state.

Ubuntu/PHP provisioning uses a single stock stack picker:

* supported Ubuntu LTS values are `26.04`, `24.04` (default), `22.04`, and `20.04`
* matching native PHP versions are `8.5`, `8.3` (default), `8.1`, and `7.4`
* `--ubuntu-version` is the normal non-interactive selector; there is no public `--php-version`
* `--image` is an advanced override; it does not replace the Ubuntu/PHP metadata
* do not treat PHP-FPM socket path as user input; derive the service and socket from the selected stack
* keep the base image and PHP packages Ubuntu-native for now
* common WordPress PHP extensions are `curl`, `gd`, `imagick`, `intl`, `mbstring`, `xml`, `zip`, `bcmath`, `soap`, `opcache`, and `readline`; do not include Xdebug

When no identifier is supplied in interactive mode, `server show`, `site show`, and `server delete` should prefer selectors over forcing positional arguments.

`nf site show` may resolve target aliases from `.nf/project.json`.

Target aliases map project-local names like `staging`, `production`, and `app1` to shared `nf` site records.

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

`nf server` manages infrastructure hosts owned by nonfiction, currently Linode and potentially DigitalOcean later. Kinsta is not modeled as a server provider; Kinsta environments are modeled as site targets.

### Linode

Linode is the only implemented remote provider right now.

Current Linode responsibilities:

* server provisioning
* server inventory from shared state
* server deletion
* server root-password lookup
* UFW and managed firewall allow 22/tcp, 80/tcp, and 443/tcp
* DNSimple-backed hostnames for `nfweb.dev`
* writing state records for provisioned servers

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
* instance files
* temporary artifacts

Keep examples neutral and fictional.

Use the README as the user-facing source of truth. Use this file as the implementation guide for agents working in the repo.

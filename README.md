# nf

`nf` is nonfiction’s internal CLI for agency WordPress theme work.

It gives the team one command surface for project metadata, local WordPress instances, theme tasks, theme packaging, shared server/site state, password derivation, and guarded infrastructure operations.

This is an internal agency tool, not a general-purpose public WordPress framework.

## Status

Working now:

* `nf init`
* `nf theme tasks`
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
* `nf theme package`
* `nf server list`
* `nf server show`
* `nf server root-password <id-or-name>`
* `nf server provision`
* `nf server delete`
* `nf site list`
* `nf site show`
* `nf config init`
* `nf password derive`

Reserved but not implemented yet:

* `nf site install`
* `nf site delete`
* `nf site deploy`
* `nf site push`
* `nf site pull`

Linode is the server provider today. DNS/TLS provisioning uses DNSimple by default. `nf server provision` uses a single stock Ubuntu/PHP stack picker: the Ubuntu LTS release determines the Ubuntu-native PHP version, Linode image, service, socket, and package set. Kinsta site records can be represented in shared state, but Kinsta deploy/sync workflows are future adapter work.

`nf server root-password <id-or-name>` derives the Linode root password from `NF_SECRET_SALT` + the server hostname + purpose `linode-root`. The password is not stored in state.

Remote infrastructure workflows are intentionally guarded. Server provisioning defaults toward dry-run/planning behavior unless explicitly executed, and non-interactive execution requires `--execute --yes`.

## Install and run

From this repository:

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

The flake exposes:

* `packages.default`
* `packages.nf`
* `apps.default`
* `apps.nf`
* `devShells.default`

When developing with Nix flakes, remember that Nix builds from the git source snapshot. Stage newly added source files before trusting `nix run` or `nix build`.

## Requirements

For normal development:

* Nix with flakes enabled
* Go, when working outside the Nix dev shell
* Docker with Docker Compose, for local instances

For remote Linode provisioning:

* Linode API token
* DNSimple API token
* `NF_SECRET_SALT`
* an SSH public key

Treat cloud-init user-data as sensitive because it can contain live provider credentials.

## Command overview

```text
nf

Commands:
  init          initialize project metadata
  theme         package artifacts and run theme tasks
  instance      manage the local WordPress instance
  site          list, show, deploy/sync remote sites
  server        provision, list, show, delete infrastructure hosts
  config        init local config
  password      derive passwords
  help          show help
```

Inside a project repository, `nf instance` manages the local WordPress instance and `nf theme tasks` lists project tasks from `.nf/project.json`.

`nf server provision` is site-agnostic. It uses `--name` as the canonical server identity, derives the hostname, wildcard hostname, and health URL from `NF_SERVER_DOMAIN`, upserts DNSimple A records, then waits for SSH, runs cloud-init/TLS finalization, and checks HTTPS health unless `--no-wait` is supplied. It creates a neutral server health vhost for the derived hostname only. There is no public `--hostname`, `--label`, or `--dns-zone` flag. It does not install WordPress, create databases, create future site vhosts, or write `sites.json`, and reruns resume partial phases instead of recreating the Linode.

Server baseline details:

* cloud-init sets both `hostname` and `fqdn`, uses `preserve_hostname: false`, and manages `/etc/hosts`
* SSH is key-only with `PermitRootLogin prohibit-password`, `PasswordAuthentication no`, and passwordless sudo for the deployment user
* UFW allows 22/tcp, 80/tcp, and 443/tcp before enable; the Linode cloud firewall uses the same inbound set when enabled
* baseline directories are `/var/www`, `/var/www/sites`, `/var/www/shared`, and `/var/log/nginx/sites`
* PHP tuning is written to `/etc/php/<version>/fpm/conf.d/99-nf-wordpress.ini`
* MariaDB is enabled without creating databases, users, or grants
* a neutral nginx server health vhost is created for the server hostname only; future site vhosts are not created during provisioning
* certbot's deploy hook reloads nginx after renewal
* `/etc/nf/server.json` records non-secret server facts only
* Node/npm are not installed by default for the server baseline
* the server health vhost serves `/var/www/nf-server` at `https://<hostname>` and exposes `/healthz`
* that health page is server-level only; future site vhosts are separate subdomains
* cloud-init previews redact the DNSimple token

Server access defaults:

* SSH user: `nonfiction`
* SSH auth: SSH keys only
* sudo: passwordless
* root password: derived, not stored in state, revealable with `nf server root-password <id-or-name>`
* DNSimple token: used for cloud-init rendering and not stored in state

Derived server identity:

* server name: `app1`
* server domain: `NF_SERVER_DOMAIN` (default `nfweb.dev`)
* hostname: `app1.nfweb.dev`
* label: `app1`
* wildcard hostname: `*.app1.nfweb.dev`
* health URL: `https://app1.nfweb.dev`

Example:

```sh
NF_SERVER_DOMAIN=nfweb.dev nf server provision --name app1
```

Defaults:

* server provider: `linode`
* DNS/TLS provider: `dnsimple`
* server name: `app1`
* server domain: `NF_SERVER_DOMAIN` (default `nfweb.dev`)
* hostname: `app1.nfweb.dev`
* label: server name
* Ubuntu/PHP stack: `Ubuntu 24.04 LTS / PHP 8.3`
* Ubuntu LTS version: `24.04`
* PHP version: `8.3` (derived from the selected stack)
* package source: `ubuntu-native`
* Ubuntu image: `linode/ubuntu24.04`
* PHP service: `php8.3-fpm`
* PHP socket: `/run/php/php8.3-fpm.sock`
* firewall mode: `managed`
* firewall label: `nf-web`
* UFW allows 22/tcp, 80/tcp, and 443/tcp before enabling
* managed Linode Cloud Firewall allows 22/tcp, 80/tcp, and 443/tcp

Supported Ubuntu/PHP matrix:

* `26.04` -> `linode/ubuntu26.04` / PHP `8.5`
* `24.04` -> `linode/ubuntu24.04` / PHP `8.3`
* `22.04` -> `linode/ubuntu22.04` / PHP `8.1`
* `20.04` -> `linode/ubuntu20.04` / PHP `7.4` (legacy/ESM)

Server health URL:

* `https://<hostname>`

Use `--ubuntu-version` for normal non-interactive selection. `--image` is only an advanced override; it does not replace the recorded Ubuntu/PHP metadata. Arbitrary PHP selection is not supported in this pass, and there is no public socket flag. `NF_SERVER_DOMAIN` controls the derived hostname, wildcard hostname, and health URL; there is no public `--hostname`, `--label`, or `--dns-zone` flag.

If SSH appears to hang after provisioning, test by IP first: `ssh nonfiction@<ipv4>`.

Common WordPress PHP extensions are installed from Ubuntu packages by default: `curl`, `gd`, `imagick`, `intl`, `mbstring`, `xml`, `zip`, `bcmath`, `soap`, `opcache`, and `readline`. Xdebug is intentionally not included.

The server state `os` block records `family: ubuntu`, `version`, `label`, and `image` alongside the legacy `ubuntu_version` convenience field.

## Project metadata

Project repositories use:

```text
.nf/project.json
```

This file is safe to commit. It describes project intent, theme paths, local instance behavior, artifact naming, target aliases, and theme tasks.

It must not contain secrets, API tokens, SSH keys, live database passwords, or mutable infrastructure state.

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

Example `.nf/project.json`:

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
  "instance": {
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
  "build": {
    "steps": [
      "composer install",
      "npm run build"
    ]
  },
  "artifact": {
    "path": "dist/client-v{version}.zip",
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
    "targets": {
      "app1": "client-app1-production",
      "staging": "client-kinsta-staging",
      "production": "client-kinsta-production"
    }
  },
  "tasks": {
    "composer": {
      "description": "Update theme Composer dependencies",
      "run": "composer --working-dir=theme update && composer --working-dir=theme dump-autoload -o"
    },
    "npm": {
      "description": "Refresh theme development dependencies",
      "run": "npm --prefix theme update --save-dev"
    },
    "build": {
      "description": "Build the theme assets",
      "run": "npm --prefix theme run build"
    },
    "watch": {
      "description": "Watch theme assets during development",
      "run": "npm --prefix theme start"
    },
    "test": {
      "description": "Run the theme test suite",
      "run": "composer --working-dir=theme test"
    }
  }
}
```

## Theme tasks

```sh
nf theme tasks
nf theme package [--dry-run] [--source path] [--output path]
nf theme <task>
```

`nf theme tasks` lists custom project tasks from `.nf/project.json`.

`nf theme <task>` runs a task defined in `.nf/project.json`.

String tasks run through the shell from the project root. Array tasks execute directly. The underlying command is printed before execution.

Examples:

```sh
nf theme tasks
nf theme build
nf theme test
nf instance shell
nf shell
nf instance wp -- plugin list
```

## Local WordPress instance

An instance is `nf`'s generated local WordPress environment for a project. It contains the Docker/WordPress scaffolding and mutable local state used to run, reset, snapshot, and sync the project during development.

The instance is generated and owned by `nf`.

Project repositories should contain the theme source and `.nf/project.json` instance definition. They should not need committed Docker instance scaffolding.

Instance ports are derived deterministically from the project slug. Set `instance.ports.wordpress` and `instance.ports.mailpit` in `.nf/project.json` to override them individually; zero or missing values fall back to the derived ports.

Generated instance files live under:

```text
~/.config/nf/instances/<project-slug>/
```

For the placeholder project:

```text
~/.config/nf/instances/client/
```

Derived local URLs:

```text
WordPress: http://localhost:<wordpress-port>
Mailpit:   http://localhost:<mailpit-port>
```

Instance info and startup output include both WordPress and Mailpit URLs, for example:

```text
Instance:
  project: client
  path: ~/.config/nf/instances/client
  compose project: nf_client_instance
  WordPress: http://localhost:<wordpress-port>
  Mailpit:   http://localhost:<mailpit-port>
```

`nf instance up` and `nf instance reset` print a success line followed by the full instance info block. `nf instance down` prints a success line followed by the short instance info block.

Default local WordPress credentials:

```text
admin / admin
```

Default Docker Compose project name:

```text
nf_client_instance
```

Common instance workflow:

```sh
nf instance up
nf instance info
nf instance logs
nf instance shell
nf instance wp -- plugin list
nf instance down
```

The top-level shortcuts `nf up`, `nf down`, `nf logs`, `nf reset`, `nf info`, `nf shell`, and `nf wp` behave the same as `nf instance ...`.

Reset the local instance:

```sh
nf instance reset
```

`nf instance up` is idempotent. It starts Docker Compose, installs WordPress if needed, and ensures the mounted theme is active.

`nf instance reset` runs a volume-destroying reset and recreates the same managed instance state.

## Instance snapshots

Instance snapshots are stored under:

```text
~/.config/nf/snapshots/<project-slug>/<snapshot-name>/
```

For the placeholder project:

```text
~/.config/nf/snapshots/client/<snapshot-name>/
```

Each snapshot contains:

* `snapshot.json`
* `database.sql.gz`
* `wp-content.tar.gz`

The `wp-content` archive includes only `uploads/`, `plugins/`, `mu-plugins/`, and `languages/`. It skips themes.

Commands:

```sh
nf instance snapshot create [name]
nf instance snapshot list
nf instance snapshot restore [name]
nf instance snapshot delete [name]
```

Snapshot names default to `YYYY-MM-DD-HHMMSS`. Spaces become dashes. Empty names, path traversal, separators, and unsafe characters are rejected.

`nf instance snapshot restore` creates a safety snapshot named `YYYY-MM-DD-HHMMSS-pre-restore` before restoring the selected snapshot.

## Theme packaging

Package the current theme:

```sh
nf theme package
```

Preview packaging:

```sh
nf theme package --dry-run
```

Use explicit paths:

```sh
nf theme package --source theme --output dist/client.zip
```

The default source is `wordpress.theme_path` from `.nf/project.json`, falling back to `theme`.

The default output comes from `artifact.path`, falling back to:

```text
dist/<project-slug>-v{version}.zip
```

For the placeholder project:

```text
dist/client-v{version}.zip
```

When the output path contains `{version}`, `nf` resolves it from:

1. `style.css` `Version:` header
2. `package.json` `version`

If neither exists, packaging fails clearly.

Packaging only zips the existing theme files. It does not run Composer, npm, or asset builds first.

Run the appropriate theme task before packaging:

```sh
nf theme build
nf theme package
```

The packager skips common non-theme paths such as:

* `.git`
* `.DS_Store`
* `node_modules`
* the output ZIP itself

## Local config

By default, `nf` uses:

```text
~/.config/nf/
```

If `XDG_CONFIG_HOME` is set, `nf` uses:

```text
$XDG_CONFIG_HOME/nf
```

For tests or isolated runs, override the config home with:

```sh
NF_CONFIG_HOME=/path/to/nf-config
```

Expected layout:

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

## Secrets

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

`LINODE_TOKEN` is preferred. `LINODE_CLI_TOKEN` is accepted for convenience.

Use:

```sh
nf config init
```

to create the file or fill missing values.

The `.env` file is local machine state and must not be committed.

## Shared state

Shared state lives in JSON files under:

```text
~/.config/nf/state/
```

Current state files:

```text
servers.json
sites.json
projects.json
```

`nf` supports simple arrays as well as keyed object shapes.

Example `servers.json`:

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

Example `sites.json`:

```json
{
  "sites": {
    "client-app1-production": {
      "provider": "linode",
      "server": "app1",
      "environment": "production",
      "hostname": "client.app1.nfweb.dev",
      "url": "https://client.app1.nfweb.dev/",
      "branch": "main"
    },
    "client-kinsta-production": {
      "provider": "kinsta",
      "environment": "production",
      "hostname": "www.example.com",
      "url": "https://www.example.com",
      "kinsta": {
        "company_id": "123456",
        "site_id": "234567",
        "environment_id": "345679"
      }
    }
  }
}
```

Project repositories should not duplicate this mutable state.

## Server commands

```sh
nf server list
nf server show [id-or-name]
nf server root-password <id-or-name>
nf server provision [flags]
nf server delete [flags] [id-or-name]
```

`nf server list` reads shared server state and prints a table.

`nf server show` prints the matching server record as JSON. Without an identifier, interactive mode opens a selector.

`nf server provision` builds a guarded provisioning plan and can create a Linode server with DNS/TLS bootstrap. By default it waits for SSH, completes cloud-init/TLS, and verifies `/healthz`; `--no-wait` leaves the server in provisioning state so you can finish it manually or resume later. It does not install WordPress or write site state.

`nf server delete` prints a deletion plan first. In non-interactive mode it remains dry-run unless `--execute --yes` is supplied.

## Site commands

```sh
nf site list
nf site show [id-or-name]
```

`nf site list` reads shared site state and prints a table.

`nf site show` prints the matching site record as JSON. Without an identifier, interactive mode opens a selector.

When run inside a project repository, `nf site show` can resolve target aliases from `.nf/project.json`.

Target aliases map project-local names like `staging`, `production`, and `app1` to shared `nf` site records.

For example:

```sh
nf site show production
```

may resolve through:

```json
{
  "deploy": {
    "targets": {
      "production": "client-kinsta-production"
    }
  }
}
```

Future site lifecycle commands are reserved but not implemented yet:

```sh
nf site install
nf site delete
nf site deploy
nf site push
nf site pull
```

## Server provisioning

Provisioning currently targets Linode and DNSimple.

Dry-run plan:

```sh
nf server provision \
  --non-interactive \
  --name app1
```

Actual execution requires both:

```text
--execute --yes
```

Example execution:

```sh
nf server provision \
  --non-interactive \
  --name app1 \
  --execute \
  --yes
```

Use `--no-wait` to stop after DNS if you want to finish cloud-init, TLS, and health checks yourself:

```sh
nf server provision \
  --non-interactive \
  --name app1 \
  --execute \
  --yes \
  --no-wait
```

Rerunning the same provision command resumes from the saved partial phase instead of creating a new Linode.

If TLS finalization fails, rerun the recovery helper over SSH: `ssh nonfiction@<hostname> "sudo /usr/local/bin/nf-enable-wildcard-tls"`.

Useful flags:

```text
--provider
--dns-provider
--ubuntu-version
--name
--region
--type
--image
--ssh-user
--ssh-public-key-file
--dnsimple-account-id
--write-cloud-init
--show-cloud-init
--non-interactive
--wait
--no-wait
--ssh-timeout
--cloud-init-timeout
--tls-timeout
--health-timeout
--dry-run
--execute
--yes
```

Defaults include:

```text
provider: linode
dns provider: dnsimple
server name: app1
server domain: nfweb.dev
hostname: app1.nfweb.dev
label: app1
region: ca-central
type: g6-standard-1
image: linode/ubuntu24.04
Ubuntu/PHP stack: Ubuntu 24.04 LTS / PHP 8.3
Ubuntu LTS version: 24.04
PHP version: 8.3 (derived)
package source: ubuntu-native
ssh user: nonfiction
ssh key source: linode-profile
ssh public key file fallback: ~/.ssh/id_ed25519.pub
DNSimple account ID: 14
```

The PHP-FPM service and socket are derived from the selected Ubuntu/PHP stack.

Required environment for execution:

```env
NF_SERVER_DOMAIN=nfweb.dev
NF_SECRET_SALT=
DNSIMPLE_TOKEN=
LINODE_TOKEN=
```

`LINODE_CLI_TOKEN` is also accepted for convenience.

Provisioning prints a reviewable plan. It can also write or display the generated cloud-init preview.

Write cloud-init preview:

```sh
nf server provision \
  --name app1 \
  --write-cloud-init /tmp/app1-cloud-init.yml
```

Show cloud-init preview in the terminal:

```sh
nf server provision \
  --name app1 \
  --show-cloud-init
```

## Server deletion

Preview deletion:

```sh
nf server delete app1 --non-interactive
```

Execute deletion:

```sh
nf server delete app1 --non-interactive --execute --yes
```

Without an identifier, interactive mode opens a server picker.

Deletion prints a plan, shows related sites, and only performs the remote Linode deletion when execution is explicitly requested or confirmed.

If the remote Linode is already gone, `nf` treats that as stale local state and can still clean matching local records.

## Password derivation

Derive a password:

```sh
nf password derive <project-slug> <purpose>
```

Example:

```sh
nf password derive client db-password
```

Password derivation uses:

* project slug
* purpose
* `NF_SECRET_SALT`

It does not use the server hostname, so derived passwords can remain stable across related environments.

`NF_SECRET_SALT` must be present in the environment or in:

```text
~/.config/nf/.env
```

## Provider model

`nf server` manages infrastructure hosts owned by nonfiction, currently Linode and potentially DigitalOcean later. Kinsta is not modeled as a server provider; Kinsta environments are modeled as site targets.

### Linode

Implemented now:

* server provisioning
* server inventory from shared state
* server deletion
* DNSimple-backed hostnames for `nfweb.dev`
* state records for provisioned servers and sites

### Kinsta

Future adapter work:

* deploy packaged theme artifacts
* pull/push database and uploads where supported
* inspect and target environments by Kinsta IDs

Kinsta records should store Kinsta IDs and provider metadata. They should not require Linode SSH fields or reuse Linode provisioning paths.

## Production safety

Database and uploads sync are future workflows and should be treated as high risk.

Rules for future implementation:

* require explicit environment selection
* separate staging and production behavior
* preserve production passwords
* preserve sensitive options and users where possible
* never silently push local/staging credentials back to production
* make destructive actions confirmable and auditable

## Development notes

Primary entrypoint:

```text
cmd/nf/main.go
```

Main packages:

```text
internal/cli
internal/config
internal/envwizard
internal/passwords
internal/provision
internal/state
internal/theme
internal/ui
```

Common checks:

```sh
go test ./...
go test ./internal/cli
go run ./cmd/nf --help
nix run .#nf -- --help
nix build .#nf -L
```

Keep `README.md` and `AGENTS.md` aligned when command names, state layout, safety posture, or provider behavior changes.

## Roadmap

Near-term order:

1. Keep the standard agency WordPress theme repo workflow comfortable enough to replace per-project scripts.
2. Implement Linode site lifecycle after server provisioning.
3. Add theme artifact deployment.
4. Add database/uploads pull-push workflows with production protections.
5. Add Kinsta deploy/sync adapters.
6. Polish team distribution and shared state sync.

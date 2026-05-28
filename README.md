# nf

`nf` is nonfiction’s internal CLI for agency WordPress theme work.

It gives the team one command surface for project metadata, local WordPress runtimes, repo-local build/test commands, theme packaging, shared server/site state, password derivation, and guarded infrastructure operations.

This is an internal agency tool, not a general-purpose public WordPress framework.

## Status

Working now:

* `nf repo init`
* `nf repo commands`
* `nf repo up`
* `nf repo down`
* `nf repo logs`
* `nf repo reset`
* `nf repo wp`
* `nf repo package`
* `nf server list`
* `nf server show`
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

Linode is the first implemented remote provider. Kinsta site records can be represented in shared state, but Kinsta deploy/sync workflows are future adapter work.

Remote infrastructure workflows are intentionally guarded. Provisioning and deletion default toward dry-run/planning behavior unless explicitly executed.

## Install and run

From this repository:

```sh
nix run .#nf -- --help
nix develop -c nf --help
go run ./cmd/nf -- --help
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
* Docker with Docker Compose, for local runtimes

For remote Linode provisioning:

* `linode-cli`
* Linode API token
* DNSimple API token
* an SSH public key

## Command overview

```text
nf

Commands:
  server        provision, list, show, delete servers
  site          list, show, future install/delete/deploy/sync
  repo          init repo metadata and manage runtime
  config        init local config
  password      derive passwords
  help          show help
```

Inside a project repository, `nf repo` also exposes runtime commands and repo-local aliases from `.nf/project.json`.

## Repo workflow

Project repositories use:

```text
.nf/project.json
```

This file is safe to commit. It describes project intent, theme paths, local runtime behavior, artifact naming, deploy aliases, and repo-local commands.

It must not contain secrets, API tokens, SSH keys, live database passwords, or mutable infrastructure state.

Create it with:

```sh
nf repo init
```

Common flags:

```sh
nf repo init \
  --project-slug client \
  --project-name "Client" \
  --theme-slug theme \
  --theme-source theme
```

By default, `nf repo init` derives the project slug from the current git root folder and assumes the WordPress theme lives in `theme/`.

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
  "runtime": {
    "compose": "docker compose",
    "wordpress_service": "wordpress",
    "cli_service": "cli",
    "theme_mount_slug": "theme",
    "uploads_path": "uploads"
  },
  "build": {
    "commands": [
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
    "aliases": {
      "app1": "client-app1-production",
      "staging": "client-kinsta-staging",
      "production": "client-kinsta-production"
    }
  },
  "commands": {
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

## Repo commands

```sh
nf repo init
nf repo commands
nf repo up
nf repo down
nf repo logs
nf repo reset
nf repo wp -- <wp-cli args>
nf repo package [--dry-run] [--source path] [--output path]
nf repo <alias>
```

`nf repo commands` lists built-in runtime commands plus custom project aliases.

`nf repo <alias>` runs a command defined in `.nf/project.json`.

String commands run through the shell from the project root. Array commands execute directly. The underlying command is printed before execution.

Examples:

```sh
nf repo commands
nf repo build
nf repo test
nf repo wp -- plugin list
```

## Local WordPress runtime

A runtime is `nf`'s generated local WordPress environment for a project. It contains the Docker/WordPress scaffolding and mutable local state used to run, reset, snapshot, and sync the project during development.

The runtime is generated and owned by `nf`.

Project repositories should contain the theme source and `.nf/project.json` runtime definition. They should not need committed Docker runtime scaffolding.

Generated runtime files live under:

```text
~/.config/nf/runtimes/<project-slug>/
```

For the placeholder project:

```text
~/.config/nf/runtimes/client/
```

Default local URL:

```text
http://localhost:18080
```

Default local WordPress credentials:

```text
admin / admin
```

Default Docker Compose project name:

```text
nf_client_runtime
```

Common workflow:

```sh
nf repo up
nf repo logs
nf repo wp -- plugin list
nf repo down
```

Reset the local runtime:

```sh
nf repo reset
```

`nf repo up` is idempotent. It starts Docker Compose, installs WordPress if needed, and ensures the mounted theme is active.

`nf repo reset` runs a volume-destroying reset and recreates the same managed runtime state.

## Theme packaging

Package the current theme:

```sh
nf repo package
```

Preview packaging:

```sh
nf repo package --dry-run
```

Use explicit paths:

```sh
nf repo package --source theme --output dist/client.zip
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

Run the appropriate repo-local alias before packaging:

```sh
nf repo build
nf repo package
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
  runtimes/
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
LINODE_CLI_TOKEN=
DNSIMPLE_ACCOUNT_ID=14
```

`LINODE_TOKEN` is also accepted where Linode credentials are needed.

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
nf server provision [flags]
nf server delete [flags] [id-or-name]
```

`nf server list` reads shared server state and prints a table.

`nf server show` prints the matching server record as JSON. Without an identifier, interactive mode opens a selector.

`nf server provision` builds a guarded provisioning plan and can create a Linode-backed WordPress host.

`nf server delete` prints a deletion plan first. In non-interactive mode it remains dry-run unless `--execute --yes` is supplied.

## Site commands

```sh
nf site list
nf site show [id-or-name]
```

`nf site list` reads shared site state and prints a table.

`nf site show` prints the matching site record as JSON. Without an identifier, interactive mode opens a selector.

When run inside a project repository, `nf site show` can resolve deploy aliases from `.nf/project.json`.

For example:

```sh
nf site show production
```

may resolve through:

```json
{
  "deploy": {
    "aliases": {
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
  --project-slug client \
  --server-name app1 \
  --site-domain app1.nfweb.dev
```

Actual execution requires both:

```text
--execute --yes
```

Example execution:

```sh
nf server provision \
  --non-interactive \
  --project-slug client \
  --server-name app1 \
  --site-domain app1.nfweb.dev \
  --execute \
  --yes
```

Useful flags:

```text
--provider
--project-slug
--server-name
--site-domain
--label
--region
--type
--image
--ssh-user
--ssh-public-key-file
--remote-wp-path
--php-fpm-socket
--db-name
--db-user
--wp-admin-user
--wp-admin-email
--site-title
--dns-zone
--dnsimple-account-id
--write-cloud-init
--show-cloud-init
--non-interactive
--dry-run
--execute
--yes
```

Defaults include:

```text
provider: linode
server name: app1
server domain: app1.nfweb.dev
region: ca-central
type: g6-standard-1
image: linode/ubuntu24.04
ssh user: nonfiction
ssh public key: ~/.ssh/id_ed25519.pub
remote WordPress path: /var/www/<project-slug>
PHP-FPM socket: /var/run/php/php8.3-fpm.sock
DNSimple account ID: 14
```

Required environment for execution:

```env
NF_SECRET_SALT=
DNSIMPLE_TOKEN=
LINODE_CLI_TOKEN=
```

or:

```env
LINODE_TOKEN=
```

Provisioning prints a reviewable plan. It can also write or display the generated cloud-init preview.

Write cloud-init preview:

```sh
nf server provision \
  --project-slug client \
  --server-name app1 \
  --site-domain app1.nfweb.dev \
  --write-cloud-init /tmp/app1-cloud-init.yml
```

Show cloud-init preview in the terminal:

```sh
nf server provision \
  --project-slug client \
  --server-name app1 \
  --site-domain app1.nfweb.dev \
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

# Usage

Use this page as a map for day-to-day work. Detailed command reference lives in [Commands](commands.md); implementation and contributor notes live in [Development](development.md) and [Architecture](architecture.md).

## First-Time Setup

Configure global settings before creating or managing projects:

```sh
nf config init
nf password set-salt <shared-salt>
```

`nf config init` walks through required settings, including provider API keys and `base_domain`. Use `nf password set-salt` with the shared team salt so derived passwords match across machines. See [Configuration](configuration.md) for config files, secrets, and password derivation.

## Project Setup

Create repo metadata from the project repository:

```sh
nf init
```

Project repositories use `nf.json`. This file is safe to commit and must not contain API tokens, SSH keys, live database passwords, provider secrets, or mutable provider inventory.

By default, `nf init` derives the project slug from the current git root folder and assumes the WordPress theme lives in `theme/`:

```sh
nf init \
  --project-slug client \
  --theme-slug client \
  --theme-source theme
```

After `nf init`, run project-local commands from that repo so `theme`, `env`, `remote`, and `public` are available.

## Common Workflows

Start local WordPress from a repo:

```sh
nf env up
nf env show
```

Read [Local Development](local-development.md) for Docker env startup, WP-CLI, logs, Mailpit, the database UI, resets, and local passwords.

Create infrastructure and hosted WordPress envs:

```sh
nf provider check linode
nf target add linode app1 --dry-run
nf site add app1-linode client --dry-run
```

Read [Targets](targets.md) for target creation and [Sites](sites.md) for remote WordPress site creation, inspection, passwords, shell access, WP-CLI, and exports.

Add or manage staging:

```sh
nf site staging status client.app1-linode
nf site staging add client.app1-linode --dry-run
```

Read [Staging](staging.md) for staging lifecycle commands.

Connect the current repo to a remote env:

```sh
nf site refresh
nf remote add production client.app1-linode:live
nf remote show production
```

Read [Remotes](remotes.md) for repo remote setup. Remotes stay pointed at env IDs such as `client.app1-linode:live`, not public domains.

Build and deploy a theme release:

```sh
nf theme tasks
nf theme build
nf theme package
nf theme deploy production --dry-run
```

Read [Themes](themes.md) for task execution, clean release artifacts, deploys, and rollback.

Deploy static public artifacts:

```sh
nf public deploy production --dry-run
```

Read [Public Artifacts](public-artifacts.md) for configured non-WordPress URL paths.

Manage WordPress plugins declared in `nf.json`:

```sh
nf plugin status
nf plugin diff production
nf plugin install production --dry-run
```

Read [Plugins](plugins.md) for plugin configuration, status, diff, and install behavior.

Move data safely:

```sh
nf env snapshot add before-content-work
nf env snapshot list
nf env pull production --dry-run
```

Read [Snapshots](snapshots.md) for local and remote snapshots. Read [Sync](sync.md) before pushing or pulling a database or mutable `wp-content`.

Launch a public domain:

```sh
nf domain list production
nf domain add production www.client.com client.com --primary --dry-run
nf domain check production www.client.com client.com
nf domain primary production www.client.com --search-replace --dry-run
```

Read [Domains and Launch](domains.md) before a launch window. Public DNS remains the client's responsibility; `nf` attaches domains, prints DNS instructions, verifies readiness, and performs the primary cutover.

## Safety Pattern

Guarded infrastructure and sync mutations support a plan-first flow:

```sh
nf <command> --dry-run
nf <command> --execute --yes
```

When run interactively without `--yes`, these commands prompt before changing remote infrastructure or content. In non-interactive mode, remote execution requires both `--execute` and `--yes`.

Theme deploy, public deploy, and plugin install also support dry-run or confirmation flags, but their exact execution flags differ. Check the specific guide before changing a remote env.

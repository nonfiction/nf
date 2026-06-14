# nf

<img width="150" align="right" src="nf.svg">

`nf` is nonfiction’s internal CLI for agency WordPress theme work.

It gives the team one command surface for project metadata, local WordPress dev envs, theme tasks, theme packaging, provider inventory, repo remotes, password derivation, and guarded future deploy/sync workflows.

This is an internal agency tool, not a general-purpose public WordPress framework.

For the full project model, state layout, implementation phases, and roadmap, see [`SPEC.md`](SPEC.md).

## Install and run

Install the latest `nf` from the GitHub flake into your Nix profile:

```sh
nix profile add github:nonfiction/nf
```

Run from a checkout during development:

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

## Commands

Top-level help is context-sensitive.

Always available:

```text
nf

Commands:
  provider    manage provider integrations
  target      manage deployable targets
  site        manage remote sites and envs
  password    derive passwords

  init        initialize project metadata
  config      manage global config
  completion  print shell completion scripts
  version     show nf version
  help        show help
```

Inside an `nf` project repo with `nf.json` next to `.git`:

```text
nf

Commands:
  provider    manage provider integrations
  target      manage deployable targets
  site        manage remote sites and envs
  password    derive passwords

  remote      manage repo remotes
  env         manage the local development env
  theme       package clean artifacts and run theme tasks
  public      deploy static public paths

  init        initialize project metadata
  config      manage global config
  completion  print shell completion scripts
  version     show nf version
  help        show help
```

## Versioning and releases

`nf` uses date-based versions in `YYYY.MM.DD.N` form. `N` increments for multiple releases on the same UTC date. The current source version lives in `internal/version/VERSION`.

Check the installed binary:

```sh
nf version
nf version --short
```

Build release artifacts for the version in `internal/version/VERSION`:

```sh
scripts/release.sh
```

Build artifacts for an explicit version:

```sh
scripts/release.sh 2026.06.09.2
```

The release script writes binaries and checksums under `dist/`, stamping the version, git commit, and release date into the binary with Go linker flags. The release date is derived from the `YYYY.MM.DD` prefix.

## Shell completion

`nf` can print completion scripts for bash and zsh:

```sh
nf completion bash
nf completion zsh
```

Temporary setup for the current shell:

```sh
# bash
source <(nf completion bash)

# zsh
source <(nf completion zsh)
```

Persistent setup depends on your shell config. Save the generated script into a file loaded by your shell, such as a bash completion directory or a zsh `$fpath` completion file.

## Quick start

Configure global settings before creating or managing projects:

```sh
nf config init
nf password set-salt <shared-salt>
```

`nf config init` walks through required settings, including provider API keys and `base_domain`. Use `nf password set-salt` with the shared team salt so derived passwords match across machines.

Create repo metadata:

```sh
nf init
```

After `nf init`, run project-local commands from that repo so `theme`, `env`, and `remote` are available.

Start local WordPress:

```sh
nf env up
nf env up --rebuild
nf env show
nf env password
nf env logs
nf env wp -- plugin list
nf env plugins list
nf env plugins add stream
nf env plugins remove stream
nf env plugins status
nf env plugins diff
nf env plugins install
nf env plugins install production --dry-run
```

List theme tasks and run one:

```sh
nf theme tasks
nf theme build
```

Package the theme:

```sh
nf theme package
```

Deploy the packaged theme release to a configured repo remote:

```sh
nf theme deploy production [--dry-run]
nf theme rollback production [--dry-run]
nf public deploy production [--dry-run]
```

## Project metadata

Project repositories use:

```text
nf.json
```

This file is safe to commit. It must not contain API tokens, SSH keys, live database passwords, provider secrets, or mutable provider inventory.

Generated project metadata includes `project.password_version: 0`. Leave it at `0` for stable derived passwords; increment it to rotate derived passwords for that project/site without changing the shared `NF_PASSWORD_SALT`.

Common init flags:

```sh
nf init \
  --project-slug client \
  --theme-slug client \
  --theme-source theme
```

By default, `nf init` derives the project slug from the current git root folder and assumes the WordPress theme lives in `theme/`.

## Theme tasks and packaging

```sh
nf theme tasks
nf theme package [--dry-run] [--source path] [--output path]
nf theme deploy <remote> [--dry-run]
nf theme rollback <remote> [--dry-run]
nf theme <task> [-- args]
```

`nf theme tasks` lists project tasks from `nf.json`.

String tasks run through `sh -lc` from the project root. Array tasks execute directly. The underlying command is printed before execution.

`nf theme package` creates a clean staged release artifact instead of zipping the development checkout as-is. It copies runtime theme files to a temporary staging directory, excludes obvious local development files, and when `composer.json` is present runs `composer install --no-dev --no-interaction --prefer-dist --optimize-autoloader --no-progress` in that staging directory before writing the zip. This preserves the working tree's `theme/vendor/` while ensuring the artifact is ready to upload/install/activate: it includes `vendor/autoload.php` and runtime Composer packages from `require`, but excludes `require-dev` packages and dev-only Composer tooling binaries such as PHP-CS-Fixer, PHPCS, and PHPCBF.

It still does not run npm or asset builds. Run the right theme task before packaging:

```sh
nf theme build
nf theme package
```

If `package.json` has a `build` script, packaging requires built files under `dist/` or `assets/dist/` and fails clearly when they are missing. Development-only files such as `node_modules`, editor config, PHP-CS-Fixer/PHPCS/PHPStan/Psalm config, npm manifests and lockfiles, and common frontend tooling config are excluded from the artifact. The zip root remains `wordpress.theme_slug`.

If `artifact.path` contains `{version}`, `nf` resolves it from:

1. `theme/style.css` `Version:`
2. `theme/package.json` `version`

`nf theme deploy <remote>` is a one-command packaged release deploy. It builds the same theme artifact, uploads it to the selected remote env, extracts it under `wp-content/themes/.nf-releases/<theme-slug>/`, copies the release into the active theme directory, activates the configured theme slug with wp-cli, and records release metadata. It keeps the last 5 releases and matching uploaded zips, so release storage does not grow indefinitely. It does not require manual WordPress admin zip upload and supersedes direct in-place source rsync deploys.

`nf theme rollback <remote>` switches the active theme directory back to the previous recorded release and activates the configured theme slug again. It uses remote `releases.json`; it does not rebuild or upload artifacts.

## Static public artifacts

Use `public/` for static artifacts that must live at specific non-WordPress URL paths, such as annual report microsites.

```json
{
  "public": {
    "paths": [
      {
        "source": "public/annual-report-2026",
        "path": "/annual-report-2026"
      }
    ]
  }
}
```

Deploy them separately from the theme:

```sh
nf public deploy production --dry-run
nf public deploy production
```

`nf` only deploys paths explicitly listed in `nf.json`. `source` must be repo-relative and symlink-free. `path` must be an absolute URL path and cannot target `/`, traversal, `/wp-admin`, `/wp-content`, `/wp-includes`, or `/uploads`. Add `"delete": true` to mirror removals with `rsync --delete`; execution then requires `--yes`.

For large artifacts that should not live in git, use a project task to materialize the deployable files into `public/` first, then deploy the local directory:

```json
{
  "tasks": {
    "fetch-annual-report-2026": {
      "description": "Fetch annual report static export",
      "run": "scripts/fetch-annual-report-2026"
    }
  },
  "public": {
    "paths": [
      {
        "source": "public/annual-report-2026",
        "path": "/annual-report-2026"
      }
    ]
  }
}
```

```sh
nf theme fetch-annual-report-2026
nf public deploy production --dry-run
```

Remote HTTP crawling, archives, and rsync side-loaded sources are intentionally outside this first slice.

## Local WordPress env

The local env is `nf`'s generated WordPress dev environment for a project.

Common workflow:

```sh
nf env up
nf env up --rebuild
nf env show
nf env password [remote] [--wp|--db|--basicauth]
nf env logs
nf env shell
nf env sh [remote]
nf env wp -- plugin list
nf env plugins list
nf env plugins add stream
nf env plugins remove stream
nf env plugins status
nf env plugins status production
nf env plugins diff production
nf env plugins install
nf env down
nf env reset --rebuild
```

`nf env up` is idempotent. It starts Docker Compose, configures Mailpit SMTP, installs WordPress if needed, and ensures the mounted theme is active. Add `--rebuild` to rebuild the generated WordPress image first.

`nf env reset` is destructive for the local env only. It creates a safety snapshot, removes Docker Compose volumes, and recreates the env. Add `--rebuild` to rebuild the generated WordPress image during recreation.

The generated local env includes WordPress, MariaDB, Mailpit, and an Adminer/AdminNeo container. `nf env show` prints the site URL, WordPress login, Mailpit URL, and an Adminer URL prefilled with the database host/user/name. Generated WordPress config enables `WP_DEBUG` and `WP_DEBUG_LOG`, disables debug display, and `nf env logs` tails Docker logs for the WordPress service.

The generated WordPress Docker image includes useful CLI tools plus `wp-cli`. `nf env shell`, `nf env sh`, and `nf env wp` run in the WordPress container as `docker_user`, which defaults to `nonfiction` and can be changed with `nf config set-docker-user <user>`.

`nf env password [remote] [--wp|--db|--basicauth]` prints only one local or remote env password. `--wp` is the default.

`nf env logs <remote>` resolves a configured repo remote, prints the SSH command preview, ensures `wp-content/debug.log` exists, and tails it on the remote host.

Configured WordPress plugins live in `nf.json` under `wordpress.plugins`:

```json
{
  "wordpress": {
    "plugins": [
      "stream",
      "wp-crontrol",
      "insert-headers-and-footers",
      "block-visibility",
      "imsanity",
      {
        "slug": "acf-pro",
        "source": "$NF_PLUGIN_ACF_PRO_ZIP",
        "activate": true,
        "auto_update": true
      }
    ]
  }
}
```

String entries install from wordpress.org, activate, and enable auto-updates by default. Object entries require `slug`, may set `source` to a zip URL/path or env var, and may set `activate` or `auto_update` to `false`. Keep private plugin URLs and license data in environment variables, not `nf.json`.

`nf env plugins add <plugin>` appends a plugin to `nf.json` without installing it. Add `--source <source>`, `--no-activate`, or `--no-auto-update` when defaults are not enough. `nf env plugins remove <plugin>` removes a plugin from `nf.json` without uninstalling it.

`nf env plugins install` with no remote targets the local env. `nf env plugins install <remote>` targets a configured repo remote, prints a remote plan, and asks for yes/no confirmation before changing the remote unless `--yes` is passed. Use `--dry-run` to preview only. Remote installs run WP-CLI on the remote host. URL sources must be reachable from that host; local zip sources are uploaded to a temporary remote directory before install and cleaned up afterward.

`nf env plugins status [remote]` compares `nf.json` against the local env or configured remote and reports whether each configured plugin is installed, active, and auto-update enabled.

`nf env plugins diff [remote]` reports the install/activate/auto-update changes needed to make the local env or remote match `nf.json`. It also reports installed plugins that are not configured in `nf.json`. It does not mutate anything. It exits `0` when configured plugins match and no extras are installed, and `2` when drift exists.

Plugin install is idempotent: it installs only missing plugins, activates only inactive plugins when `activate` is true, and enables native WordPress auto-updates only when not already enabled. It does not update, remove, pin, disable auto-updates, or manage plugin licenses.

Generated env data lives under:

```text
~/.local/share/nf/envs/<project-slug>/
```

Override for tests or isolated runs:

```sh
NF_DATA_HOME=/tmp/nf-data
```

## Env snapshots

```sh
nf env snapshot add [name]
nf env snapshot list
nf env snapshot import [remote-snapshot] [--name name]
nf env snapshot use [name] [--yes]
nf env snapshot use --remote <remote-snapshot> [--name name] [--yes]
nf env snapshot remove [name]
nf env snapshot prune [--keep N] [--dry-run] [--yes]
```

Local env snapshots live under:

```text
~/.local/share/nf/snapshots/local/<project-slug>/<snapshot-name>/
```

Each snapshot contains:

* `snapshot.json`
* `database.sql.gz`
* `wp-content.tar.gz`

The `wp-content` archive includes only `uploads/`, `plugins/`, `mu-plugins/`, and `languages/`. It skips themes.

`nf env snapshot use` creates a safety snapshot named `YYYY-MM-DD-HHMMSS-pre-restore` before restoring the selected snapshot. Add `--yes` to skip the interactive confirmation.

Remote snapshots are downloaded from cached remote site env records and live under:

```text
~/.local/share/nf/snapshots/remote/<env-id-slug>-YYYY-MM-DD-HHMMSS/
```

Remote snapshot workflow:

```sh
nf site snapshot <site.target:env>
nf site snapshot list
nf env snapshot import <remote-snapshot-name> --name live-copy
nf env snapshot use live-copy --yes
```

Shortcut restore from a remote snapshot:

```sh
nf env snapshot use --remote <remote-snapshot-name> --name live-copy --yes
```

This imports the remote snapshot into the current project's local snapshots, restores it, creates the normal pre-restore safety snapshot first, and keeps the imported local snapshot for audit/reuse.

## WordPress handoff export/import

```sh
nf site export <site.target:env> [--output path] [--dry-run]
nf env import <source> [--db path] [--source-url url] [--name name] [--dry-run] [--yes]
```

`nf site export` creates a full handoff copy of a managed remote WordPress env. It is different from snapshots: export includes the full WordPress filesystem, including core files, themes, plugins, uploads, mu-plugins, languages, and `wp-config.php`, plus a compressed database dump.

Default exports live under:

```text
~/.local/share/nf/exports/<env-id-slug>-YYYY-MM-DD-HHMMSS/
```

Each export contains:

* `files/`
* `database.sql.gz`
* `manifest.json`
* `README.txt`

`nf env import` is the inbound onboarding workflow. It imports into the current project's local env only. It accepts an `nf site export` directory, or a generic WordPress filesystem directory when paired with `--db`. It creates an import snapshot, creates the normal pre-restore safety snapshot, restores the database plus `wp-content/uploads`, `plugins`, `mu-plugins`, and `languages`, runs URL search-replace when a source URL is known, activates the configured local theme when installed, and flushes cache. It does not import WordPress core or `wp-config.php` into the local env.

## Config and secrets

Config lives under:

```text
~/.config/nf/
  config.json
  .env
```

Non-secret config goes in `config.json`:

```json
{
  "base_domain": "nonfiction.dev",
  "dnsimple_account_id": "14",
  "basicauth_default_user": "nonfiction",
  "adminer_default_user": "adminer",
  "docker_user": "nonfiction",
  "docker_db_image": "mariadb:11",
  "docker_wordpress_image": "wordpress:php8.3-apache"
}
```

Secrets go in `.env`:

```env
NF_PASSWORD_SALT=
DNSIMPLE_TOKEN=
LINODE_TOKEN=
KINSTA_API_KEY=
```

`dnsimple_account_id` is fetched from DNSimple with `DNSIMPLE_TOKEN` by `nf provider check dnsimple`; do not set `DNSIMPLE_ACCOUNT_ID` in `.env`.

Use:

```sh
nf config init
nf config set-base-domain nonfiction.dev
nf config set-default-wp-email dev@example.com
nf config set-default-wp-user admin
nf config set-basicauth-default-user nonfiction
nf config set-adminer-default-user adminer
nf config set-docker-db-image mariadb:11
nf config set-docker-wordpress-image wordpress:php8.3-apache
nf config set-docker-user nonfiction
nf config set-kinsta-default-php 8.3
nf config set-kinsta-default-region us-central1
nf config set-linode-default-region us-east
nf config set-linode-default-type g6-standard-1
nf config set-linode-default-image linode/ubuntu24.04
nf config set-linode-default-user nonfiction
nf config show
nf password set-salt <salt>
```

Overrides for tests or isolated runs:

```sh
NF_CONFIG_HOME=/tmp/nf-config
NF_STATE_HOME=/tmp/nf-state
NF_DATA_HOME=/tmp/nf-data
```

## Providers, targets, sites, and remotes

Commands:

```sh
nf provider list
nf provider show <provider>
nf provider check <provider>
nf target add linode <name> [--region region] [--type type] [--image image] [--adminer-user user] [--user user] [--keys all] [--execute --yes] [--wait]
nf target remove <target> [--dry-run] [--execute --yes]
nf target refresh
nf target list
nf target show <target>
nf target adminer show <target>
nf target password [target] [--root|--adminer]
nf site add <target> <site> [--with-staging] [--region region] [--php version] [--execute --yes]
nf site refresh
nf site list [--refresh] [--envs]
nf site show <site-id-or-alias-or-env-id>
nf site staging status <site-id-or-alias>
nf site staging add <site-id-or-alias> [--dry-run] [--execute --yes]
nf site staging remove <site-id-or-alias> [--dry-run] [--execute --yes]
nf site shell <site.target:env>
nf site wp <site.target:env> -- <cmd>
nf site export <site.target:env> [--output path] [--dry-run]
nf site snapshot <site.target:env> [--output path] [--dry-run]
nf site snapshot list
nf site snapshot remove <name> [--yes]
nf site snapshot prune [--keep N] [--dry-run] [--yes]
nf site password [site-id-or-alias-or-env-id] [--wp|--db|--basicauth]
nf site basicauth status <site.target:env>
nf site basicauth enable <site.target:env> [--dry-run] [--execute --yes]
nf site basicauth disable <site.target:env> [--dry-run] [--execute --yes]
nf site basicauth password [site-id-or-alias]
nf site domain prepare <site.target:env|remote> <domain> [--alias domain] [--proxy cloudflare] [--setup avoid-downtime|quick] [--dry-run] [--execute --yes]
nf site domain check <site.target:env|remote> <domain> [--alias domain] [--proxy cloudflare]
nf site domain primary <site.target:env|remote> <domain> [--alias domain] [--proxy cloudflare] [--setup avoid-downtime|quick] [--search-replace] [--dry-run] [--execute --yes]
nf site domain remove <site.target:env|remote> <domain> [--alias domain] [--delete-cert] [--dry-run] [--execute --yes]
nf site remove [site-id-or-alias] [--dry-run] [--execute --yes]
nf remote add [name] [site.target:env]
nf remote show <name>
nf remote remove <name>
nf remote list
```

Standard Linode target example:

```sh
nf target add linode app1 \
  --region ca-central \
  --type g6-standard-1 \
  --image linode/ubuntu24.04 \
  --adminer-user adminer \
  --user nonfiction \
  --keys all
```

Current behavior:

* `nf provider list` reports local credential status.
* `nf provider check` calls safe read-only provider health endpoints and writes `providers.json`.
* `nf provider show <provider>` reads cached provider metadata.
* `nf target add linode <name>` creates a Linode target named `<name>-linode`, tags it `nf`, creates host and wildcard DNS records under `base_domain`, queues HTTPS setup on the target with a systemd retry timer, installs AdminNeo at `https://<adminer-user>.<target-hostname>/` behind HTTP Basic auth during target provisioning, and records the target under the Linode provider in `providers.json`. Add `--adminer-user` to override `adminer_default_user` for that target only; the same value controls the Adminer HTTP Basic user, shared MySQL user, and Adminer subdomain label. Add `--wait` to keep the CLI attached through SSH, TLS, and health checks. Existing completed targets that predate AdminNeo are not upgraded in place by provider checks or target refresh.
* `nf target remove <target>` removes an empty Linode target.
* `nf target refresh` updates target records from configured target providers so added and removed targets are reflected in `providers.json`.
* `nf target list/show` read target records from `providers.json`, with a legacy `servers.json` fallback.
* `nf target adminer show <target>` reads `/var/lib/nf/target.json` over SSH and prints the Adminer URL, username, and derived password. The username defaults to `adminer_default_user`; the password is derived from the target metadata identity/purpose and `NF_PASSWORD_SALT`.
* `nf target password [target] [--root|--adminer]` prints only the derived Linode target root or Adminer password. It does not support Kinsta targets.
* `nf site add <target> <site>` creates the live WordPress env on a target. Add `--with-staging` to create live and staging in one operation.
* `nf site staging status/add/remove` manages an optional staging env for an existing site. `rm` is a shorthand for `remove`.
* `nf site refresh` discovers sites from the cached target list. Remote target site discovery is not implemented yet.
* `nf site list --envs`, `nf site show`, `nf site shell`, `nf site wp`, `nf site snapshot`, and `nf site export` read the local disposable site cache for now.
* `nf site password [site|env] [--wp|--db|--basicauth]` prints only one selected site password. `--wp` is the default. Env refs are accepted for `--db`; use a site ref for `--wp` or `--basicauth`. Linode WordPress, DB, and basic-auth passwords are derived from the site slug, purpose, `NF_PASSWORD_SALT`, and `project.password_version`; Kinsta DB password output uses the Kinsta SFTP password endpoint.
* Linode site/env database creation grants the shared Adminer MySQL user privileges only on created site env databases and refuses to create a site DB user with the same name as the shared Adminer MySQL user. Site removal revokes per-database grants before dropping the databases.
* `nf site basicauth ...` uses `basicauth_default_user` from `config.json` and a per-site derived password with `project.password_version` as the rotation source. Linode envs are managed over SSH by updating the selected env nginx vhost, including multi-vhost target nginx scripts. Kinsta Password protection exists in MyKinsta, but currently requires manual MyKinsta use because no public API endpoint is exposed.
* `nf site domain prepare ...` makes the provider/env ready to answer a public hostname and prints the DNS records the client must create. It never mutates public/client DNS. Kinsta domains are added through the Kinsta API and Kinsta-provided verification/pointing records are printed. Linode domains update nginx on the target. By default Linode installs a certbot HTTP-01 retry timer so HTTPS is issued after client DNS points at the target. Add `--proxy cloudflare` for Cloudflare-proxied Linode domains using Cloudflare SSL/TLS `Full (strict)` and a real Let's Encrypt origin certificate that continues to renew.
* `nf site domain check ...` is read-only and reports provider/server readiness, expected public DNS, HTTP reachability, HTTPS certificate status, and whether the domain is already primary. It exits `0` when public checks are ready and `2` when DNS, HTTP, HTTPS, or provider readiness is still pending. With `--proxy cloudflare`, Linode DNS checks verify public DNS resolves to Cloudflare IP ranges from Cloudflare's published list, skip origin-IP matching, and check direct Linode origin HTTPS with SNI so `Full (strict)` renewal problems are visible before Cloudflare starts returning 526 errors.
* `nf site domain primary ...` launches the canonical public hostname for the env. Pass aliases explicitly, for example `--alias client.com` when `www.client.com` should be canonical. Repo remotes in `nf.json` continue to point at env IDs, not domains.
* `nf site domain remove ...` retires a public-domain binding after a domain rename or target move. Linode removal deletes the nf-managed public vhost, public-domain scripts, certbot timer/service, and domain metadata, then resets cached `hostname`/`url` to the generated internal fallback when the removed domain was primary. It keeps the Let's Encrypt lineage by default for rollback safety; add `--delete-cert` only after the rollback window. Kinsta removal deletes non-primary domains from the Kinsta environment and refuses to remove the current primary domain.
* `nf site remove [site]` removes a whole Linode site and deletes its env data.
* `nf remote add` validates an env ID against the cache, then repo remotes are stored in `nf.json` under `remotes` as `<site>.<target>:<env>` refs.
* `nf site shell/wp ...` validate the cache, print the SSH or wp-cli command preview, then execute the remote command.
* `nf env logs <remote>` resolves a configured repo remote, prints the SSH command preview, ensures `wp-content/debug.log` exists, and tails it on the remote host.
* `nf env import <source>` imports external WordPress data into the local env after creating a safety snapshot. It never writes directly to a remote env.
* `nf env push/pull [remote]` syncs database and mutable `wp-content` after an interactive confirmation. Omit `remote` to pick from configured repo remotes. Add `--dry-run` for a non-mutating plan, or use `--non-interactive` without `--execute` for preflight-only output.

State/cache lives under:

```text
~/.local/state/nf/
  providers.json
  sites.json
  projects.json
```

Local state is disposable cache, not source of truth.

### Public domain launch checklist

Use this checklist when launching a remote env on a client-owned public domain. Public DNS remains the client's responsibility; `nf` prepares the provider/env, prints DNS instructions, verifies readiness, and performs the canonical cutover.

1. Confirm the env identity. Repo remotes should still point at env IDs, for example `production -> client.kinsta:live` or `production -> client.app1-linode:live`.

2. Choose the canonical hostname and explicit aliases. Use `www.client.com --alias client.com` for www-primary launches, `client.com --alias www.client.com` for apex-primary launches, and just `reports.client.com` for a subdomain launch with no apex/www pairing.

3. Prepare the provider/env as soon as the launch domain and target are stable. Days or weeks ahead is fine. Do not run this before the domain choice or target/env might still change.

```sh
nf site domain prepare production www.client.com --alias client.com
```

4. Send the printed DNS records to the client. Kinsta records come from the Kinsta API. Linode records point the public hostnames at the target IPs. `nf` does not create or change public DNS records.

For Cloudflare-proxied Linode domains, use Cloudflare SSL/TLS mode `Full (strict)` and include `--proxy cloudflare` on prepare/check/primary. Cloudflare should still be configured with the Linode target IP as the origin record, but public DNS will return Cloudflare IPs. In this mode `nf` keeps the public-hostname Let's Encrypt certificate and renewal timer, verifies public DNS resolves to Cloudflare IP ranges, skips public origin-IP DNS matching, and checks direct origin HTTPS separately from Cloudflare edge HTTPS. Keep Cloudflare WAF/cache/redirect rules from interfering with `/.well-known/acme-challenge/`; if issuing the first cert while orange-clouded fails, temporarily use DNS-only or add a Cloudflare rule that bypasses redirects, cache, and security checks for that path, then re-run `nf site domain check`.

```sh
nf site domain prepare production www.client.com --alias client.com --proxy cloudflare
```

5. Check readiness after the client says DNS has changed, or periodically if they might change DNS early.

```sh
nf site domain check production www.client.com --alias client.com
```

6. Wait for `check` to report ready before planned cutover when possible. If the client points DNS early, the prepared domain may already reach the site, but WordPress/provider canonical state remains unchanged until `primary`; run `primary` as soon as the launch is approved.

7. Launch the canonical public hostname at the cutover window.

```sh
nf site domain primary production www.client.com --alias client.com --search-replace
```

8. Run `check` again after `primary`. Confirm the domain is primary, DNS is still correct, HTTP does not redirect to the generated internal hostname, HTTPS is valid, and aliases behave as expected.

9. If this launch moved a domain from another target/env, retire the old binding after cutover. Include the same aliases that were attached to the old env.

```sh
nf site domain remove client.app1-linode:live www.client.com --alias client.com --proxy cloudflare
```

Use `--delete-cert` only after the rollback window if you also want to remove the old Let's Encrypt lineage. Otherwise certbot may later try to renew the old cert after DNS has moved, but keeping it briefly makes rollback safer.

10. Keep the generated internal hostname as fallback metadata. Do not change `nf.json` remotes from env IDs to public domains.

## Password derivation

```sh
nf password set-salt <salt>
nf password show-salt
nf password derive <scope> <value...>
nf env password [remote] [--wp|--db|--basicauth]
nf site password [site|env] [--wp|--db|--basicauth]
nf target password [target] [--root|--adminer]
```

Password derivation uses `NF_PASSWORD_SALT` from the environment or `~/.config/nf/.env`. Legacy `NF_SECRET_SALT` is accepted only as a migration fallback. Project site passwords, including provider basic-auth passwords, also include `project.password_version` from `nf.json` when it is non-zero; missing or `0` preserves the original derivation.

## Safety

Database and uploads sync are high risk. Sync commands must print a reviewable plan, preserve production credentials where possible, and require confirmation before destructive changes.

# Configuration

## Config and Secrets

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
  "db_default_user": "admin",
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

## Project Defines

Project `wp-config.php` constants belong in `nf.json` only when their values are safe to commit. Secrets and license keys must use environment variable indirection and live in the shell environment or `~/.config/nf/.env`.

```json
{
  "wordpress": {
    "defines": [
      {
        "name": "SOME_PLUGIN_FEATURE_FLAG",
        "value": true
      },
      {
        "name": "SOME_PLUGIN_LICENSE_KEY",
        "env": "CLIENT_PLUGIN_LICENSE_KEY"
      },
      {
        "name": "WP_ENVIRONMENT_TYPE",
        "values": {
          "local": { "value": "local" },
          "production": { "value": "production" },
          "default": { "value": "staging" }
        }
      },
      {
        "name": "OTGS_INSTALLER_SITE_KEY_WPML",
        "values": {
          "production": { "env": "CLIENT_WPML_SITE_KEY" },
          "default": { "env": "CLIENT_WPML_STAGING_SITE_KEY" }
        }
      }
    ]
  }
}
```

Use `nf define` to manage and reconcile these entries:

```sh
nf define list
nf define status
nf define status production
nf define sync
nf define sync production
nf define add
nf define add SOME_PLUGIN_FEATURE_FLAG true
nf define add SOME_PLUGIN_LICENSE_KEY --env CLIENT_PLUGIN_LICENSE_KEY
nf define add WP_ENVIRONMENT_TYPE production --for production
nf define remove
nf define remove SOME_PLUGIN_FEATURE_FLAG
```

`nf define add` with no or incomplete arguments opens an interactive wizard. `nf define remove` with no name opens a picker for configured defines. Define names are usually all caps, but nf preserves the exact PHP constant name because plugins may document a specific spelling.

`nf define add` writes shared top-level `value` or `env` entries by default. `--env <VAR>` stores only the local env/config variable name in `nf.json`; during `nf define sync`, nf resolves that value from the shell or `~/.config/nf/.env` and writes the real PHP constant value into the target `wp-config.php`. Add `--for <selector>` only when a value differs for a remote, canonical env id, env name, `local`, or `default`. When a shared entry already exists, adding a selector-specific value promotes the shared entry to `values.default`.

The interactive selector picker intentionally shows only the shared default, `local`, and remotes configured in `nf.json`. Advanced selectors such as `default`, env names, or canonical env ids remain valid when typed explicitly with `--for`.

`nf define status` and `nf define list` show define names and sources only; they do not print resolved secret values. `nf define sync` patches `wp-config.php` with an atomic temp-file replace and does not create persistent backup files. It owns only the `/* nf-managed wp-config defines: begin */` to `/* nf-managed wp-config defines: end */` project block. Removing a define from `nf.json` and running `nf define sync` removes it from that managed block, while manual constants outside the block are left alone. Older per-define `/* nf-managed wp-config defines */` markers are migrated into the block on sync. Provider-owned constants such as `KINSTAMU_WHITELABEL` are rejected from project defines and are managed by provider repair commands instead.

If `wp-config.php` already contains duplicate definitions for a configured constant, or contains that constant outside the nf-managed project block, `nf define status` reports `duplicate` and `nf define sync` refuses to patch the file until the duplicate or manual constant is resolved manually.

Use:

```sh
nf config init
nf config set core.base-domain nonfiction.dev
nf config set wordpress.admin-email dev@example.com
nf config set wordpress.admin-user admin
nf config set wordpress.basic-auth-user nonfiction
nf config set database.user admin
nf config set docker.images.db mariadb:11
nf config set docker.images.wordpress wordpress:php8.3-apache
nf config set docker.user nonfiction
nf config set kinsta.php 8.3
nf config set kinsta.region us-central1
nf config set linode.region us-east
nf config set linode.type g6-standard-1
nf config set linode.image linode/ubuntu24.04
nf config set linode.user nonfiction
nf config show
nf password set-salt <salt>
```

## Password Derivation

```sh
nf password set-salt <salt>
nf password show-salt
nf password derive <scope> <value...> [--password-version N]
nf env password [remote] [--wp|--db|--basicauth]
nf site password [site|env] [--wp|--db|--basicauth]
nf target password [target] [--root|--db]
```

Password derivation uses `NF_PASSWORD_SALT` from the environment or `~/.config/nf/.env`. Legacy `NF_SECRET_SALT` is accepted only as a migration fallback. Project site scopes are `wp-admin`, `mysql`, and `basic-auth`; target scopes are `linode-root` and `db-admin`. Project site passwords, including provider basic-auth passwords, also include `project.password_version` from `nf.json` when it is non-zero; missing or `0` preserves the original derivation. Use `--password-version` when deriving a project site password outside the matching repo context.

## Test and Isolation Overrides

Use overrides in tests or isolated runs:

```sh
NF_CONFIG_HOME=/tmp/nf-config
NF_STATE_HOME=/tmp/nf-state
NF_DATA_HOME=/tmp/nf-data
```

Generated env data lives under:

```text
~/.local/share/nf/envs/<project-slug>/
```

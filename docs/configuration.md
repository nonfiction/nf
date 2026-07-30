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

All project-managed `wp-config.php` constants are declared in `nf.json`. Commit-safe values are stored directly. Secrets and license keys use opaque references whose values are encrypted in the committed `nf.age` file beside `nf.json`.

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
        "secret": "wpdef_0123456789abcdef0123456789abcdef"
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
          "production": { "secret": "wpdef_11111111111111111111111111111111" },
          "default": { "secret": "wpdef_22222222222222222222222222222222" }
        }
      }
    ]
  }
}
```

Use `nf define` to manage and reconcile these entries:

```sh
nf define list
nf define get
nf define get SOME_PLUGIN_LICENSE_KEY
nf define get WP_ENVIRONMENT_TYPE --for production
nf define status
nf define status production
nf define sync
nf define sync production
nf define set
nf define set SOME_PLUGIN_FEATURE_FLAG true
nf define set SOME_PLUGIN_LICENSE_KEY --secret
nf define set WP_ENVIRONMENT_TYPE production --for production
nf define remove
nf define remove SOME_PLUGIN_FEATURE_FLAG
```

Bare `nf define set` opens a picker containing every configured define plus `Add a new define...`. Selecting an existing literal or encrypted value prepopulates the editor; encrypted input remains masked and is never shown as visible default text. Existing booleans and numbers retain their types. Incomplete forms prompt for missing input. `nf define remove` with no name opens a picker for configured defines. Define names are usually all caps, but nf preserves the exact PHP constant name because plugins may document a specific spelling.

`nf define set` writes or replaces a shared top-level literal by default. `--secret` prompts with input hidden and stores the encrypted value in `nf.age`; `--secret-stdin` reads one non-empty line for automation. Do not pass secret plaintext as a positional argument. Add `--for <selector>` only when a value differs for a remote, canonical env id, env name, `local`, or `default`. When a shared entry already exists, setting a selector-specific value promotes the shared entry to `values.default`.

The interactive selector picker intentionally shows only the shared default, `local`, and remotes configured in `nf.json`. Advanced selectors such as `default`, env names, or canonical env ids remain valid when typed explicitly with `--for`.

Bare `nf define get` opens a picker containing every configured define and reports an error without opening a picker when none exist. It prints only the raw configured value to stdout, including decrypted secret values. A define using `values` requires an exact `--for <selector>`; interactive use opens a configured-selector picker when it is omitted, while non-interactive use fails. `get` never silently falls back to `default`. Treat its output as sensitive and avoid logs or terminal history when retrieving secrets.

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

## Encrypted Project Defines

`nf.age` is an ASCII-armored age ciphertext managed entirely by nf. It exists only when `nf.json` contains encrypted define references and may be committed safely. The agency identity is deterministically derived from `NF_PASSWORD_SALT`; `project.password_version` does not affect it. A committed ciphertext permits offline guesses against weak salts, so the shared salt must be generated randomly and stored in the team password vault.

List operations do not decrypt values. Commands that need a value, including local `nf env up`, `nf define status`, and `nf define sync`, fail before modifying `wp-config.php` if `NF_PASSWORD_SALT`, `nf.age`, or a referenced encrypted value is unavailable or invalid.

To migrate a project-root `.env` and persisted legacy env-backed defines:

```sh
nf define migrate-env --dry-run
nf define migrate-env --delete-source
```

Migration reads `.env` directly, preserves existing define names and selectors, and adds any remaining assignment as a same-named shared secret define. It validates the complete source before writing, verifies both `nf.age` and `nf.json`, and removes `.env` only when `--delete-source` is explicit. Legacy `env` entries remain readable during migration but cannot be newly authored.

### Rotate The Shared Salt

Salt rotation is a coordinated two-commit rekey across every repository containing `nf.age`:

1. Keep the old `NF_PASSWORD_SALT` active and generate the future public recipient with a one-command environment override: `new_recipient="$(NF_PASSWORD_SALT="$new_salt" nf password age-recipient)"`.
2. In every affected repository, run `nf define rekey --add-recipient "$new_recipient"`, verify the change, and commit the dual-recipient `nf.age` files.
3. Only after all repositories are available to both recipients, rotate `NF_PASSWORD_SALT` in the team vault and local nf config.
4. With the new salt active, run `nf define rekey` in every affected repository and commit the resulting single-recipient files.
5. Retain the old salt until all step-four commits are verified and available to the team.

`nf define rekey --dry-run --add-recipient "$new_recipient"` previews the first phase. Running `nf define rekey --dry-run` under the new salt previews pruning to the current recipient. Ordinary secret edits preserve all recorded recipients during the transition.

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

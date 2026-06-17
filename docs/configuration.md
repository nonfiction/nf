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

Use:

```sh
nf config init
nf config set-base-domain nonfiction.dev
nf config set-default-wp-email dev@example.com
nf config set-default-wp-user admin
nf config set-basicauth-default-user nonfiction
nf config set-db-default-user admin
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

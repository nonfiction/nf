# Sites

Sites are hosted WordPress installs on a target. A site has one or more envs, usually `live` and optionally `staging`.

## Add a Site

Preview a Linode site:

```sh
nf site add linode1 client --dry-run
```

Create the live env:

```sh
nf site add linode1 client --execute --yes
```

Create live and staging in one operation:

```sh
nf site add linode1 client --with-staging --execute --yes
```

Create a site for a project with a rotated password version:

```sh
nf site add linode1 client --password-version 2 --execute --yes
```

Preview a Kinsta site:

```sh
nf site add kinsta client --region us-central1 --php 8.3 --dry-run
```

`nf site add <target> <site>` creates the live WordPress env on the selected target. `--with-staging` creates live and staging together. `--password-version` sets the unsigned integer version used for derived WordPress, database, and Basic Auth passwords when creating a site outside the matching repo context; omit it or pass `0` for the default. Kinsta supports `--region` and `--php`; `--php` does not apply to Linode targets.

For Kinsta, `<site>` is the canonical `nf` project slug. It is used for the `nf` site ID, repo remotes, derived passwords, and the generated internal domains under `kinsta.<base_domain>`.

If the Kinsta `*.kinsta.cloud` slug is unavailable or a site was already created in MyKinsta with a different slug, keep `<site>` as the project slug and pass the provider slug explicitly:

```sh
nf site add kinsta acme --kinsta-slug acmeinc --region us-central1 --php 8.3 --dry-run
```

That creates or adopts the Kinsta site named `acmeinc`, but caches it as `acme.kinsta` and attaches `acme.kinsta.<base_domain>` as the `nf` internal domain. `nf site refresh` can later rediscover the canonical project slug from that attached internal domain, so local cache aliases are not the source of truth.

The canonical `<site>` slug must be lowercase ASCII letters and digits, start with a letter, and be at most 32 characters. `--kinsta-slug` accepts Kinsta-style lowercase ASCII letters, digits, and hyphens up to 63 characters.

## Refresh and List Sites

```sh
nf site refresh
nf site list
nf site list --envs
nf site show client.linode1
nf site show client.linode1:live
```

`nf site refresh` fans out from cached targets. It must not be treated as provider refresh. Use `nf provider check ...` or `nf target refresh` when target cache may be stale.

Use `nf refresh` when you want a best-effort refresh of everything: all provider metadata, target records, then site/env records.

For Kinsta, refresh uses Kinsta API site/env/domain data. When an env has an `nf` internal domain like `client.kinsta.nonfiction.dev` or `client-staging.kinsta.nonfiction.dev`, refresh treats `client` as the canonical project slug even if the Kinsta provider slug differs.

## Passwords

```sh
nf site password client.linode1 --wp
nf site password client.linode1:live --db
nf site password client.linode1 --basicauth
```

`nf site password [site|env] [--wp|--db|--basicauth]` prints only one selected password. `--wp` is the default. Env refs are accepted for `--db`; use a site ref for `--wp` or `--basicauth`.

Linode WordPress, DB, and basic-auth values are derived. Kinsta DB password output uses the Kinsta SFTP password endpoint.

## Remote Shell and WP-CLI

```sh
nf site shell client.linode1:live
nf site wp client.linode1:live -- plugin list
```

These commands validate the cache, print the SSH or wp-cli command preview, then execute the remote command.

From a project repo with a configured remote, use the env commands:

```sh
nf env logs production
nf env sh production
```

Repo remotes are covered in [Remotes](remotes.md). For remote WP-CLI, use `nf site wp <site.target:env> -- <cmd>` with the explicit env ref.

## Cache And Platform Repair

```sh
nf site cache client.linode1
nf site cache client.linode1:staging
nf site repair client.linode1 --dry-run
nf site repair client.linode1
```

`nf site cache [site|env]` clears cache for a remote env. A site-only ref defaults to `:live`. Kinsta uses the provider clear-cache API. Linode purges the env nginx page-cache directory and flushes the WordPress object cache.

`nf site repair [site|env]` repairs provider platform files for an existing env. Use `--dry-run` to preview. Interactive execution prompts for confirmation; non-interactive execution requires `--execute --yes`. It restores Kinsta's required MU plugin when missing, removes local-only `nf-mailpit.php` from remotes, and refreshes Linode cache snippets/vhost wiring plus the nf Linode cache MU plugin.

## Handoff Export

```sh
nf site export client.linode1:live --dry-run
nf site export client.linode1:live
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

Importing a handoff into the local env is covered in [Snapshots](snapshots.md#import-a-handoff-into-local-development).

## Remove a Site

```sh
nf site remove client.linode1 --dry-run
nf site remove client.linode1 --execute --yes
```

`nf site remove [site]` removes a whole Linode site and deletes its env data. To delete only staging, use [Staging](staging.md).

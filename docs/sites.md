# Sites

Sites are hosted WordPress installs on a target. A site has one or more envs, usually `live` and optionally `staging`.

## Add a Site

Preview a Linode site:

```sh
nf site add app1-linode client --dry-run
```

Create the live env:

```sh
nf site add app1-linode client --execute --yes
```

Create live and staging in one operation:

```sh
nf site add app1-linode client --with-staging --execute --yes
```

Preview a Kinsta site:

```sh
nf site add kinsta client --region us-central1 --php 8.3 --dry-run
```

`nf site add <target> <site>` creates the live WordPress env on the selected target. `--with-staging` creates live and staging together. Kinsta supports `--region` and `--php`; `--php` does not apply to Linode targets.

## Refresh and List Sites

```sh
nf site refresh
nf site list
nf site list --envs
nf site show client.app1-linode
nf site show client.app1-linode:live
```

`nf site refresh` fans out from cached targets. It must not be treated as provider refresh. Use `nf provider check ...` or `nf target refresh` when target cache may be stale.

## Passwords

```sh
nf site password client.app1-linode --wp
nf site password client.app1-linode:live --db
nf site password client.app1-linode --basicauth
```

`nf site password [site|env] [--wp|--db|--basicauth]` prints only one selected password. `--wp` is the default. Env refs are accepted for `--db`; use a site ref for `--wp` or `--basicauth`.

Linode WordPress, DB, and basic-auth values are derived. Kinsta DB password output uses the Kinsta SFTP password endpoint.

## Remote Shell and WP-CLI

```sh
nf site shell client.app1-linode:live
nf site wp client.app1-linode:live -- plugin list
```

These commands validate the cache, print the SSH or wp-cli command preview, then execute the remote command.

From a project repo with a configured remote, use the env commands:

```sh
nf env logs production
nf env sh production
```

Repo remotes are covered in [Remotes](remotes.md). For remote WP-CLI, use `nf site wp <site.target:env> -- <cmd>` with the explicit env ref.

## Handoff Export

```sh
nf site export client.app1-linode:live --dry-run
nf site export client.app1-linode:live
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
nf site remove client.app1-linode --dry-run
nf site remove client.app1-linode --execute --yes
```

`nf site remove [site]` removes a whole Linode site and deletes its env data. To delete only staging, use [Staging](staging.md).

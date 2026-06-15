# Local Development

The local env is `nf`'s generated WordPress development environment for a project repo.

## Start From a Repo

Create project metadata if the repo does not have `nf.json` yet:

```sh
nf init
```

Start WordPress:

```sh
nf env up
nf env show
```

`nf env up` is idempotent. It starts Docker Compose, configures Mailpit SMTP, installs WordPress if needed, and ensures the mounted theme is active.

Rebuild the generated WordPress image when needed:

```sh
nf env up --rebuild
```

## Inspect the Env

```sh
nf env show
nf env password
nf env password --db
nf env logs
```

`nf env show` prints the site URL, WordPress login, Mailpit URL, and an Adminer/AdminNeo URL prefilled with the database host/user/name.

Generated WordPress config enables `WP_DEBUG` and `WP_DEBUG_LOG`, disables debug display, and routes local mail through Mailpit. `nf env logs` tails Docker logs for the WordPress service.

## Run Shell and WP-CLI

```sh
nf env shell
nf env sh
nf env wp -- plugin list
```

The generated WordPress Docker image includes useful CLI tools plus `wp-cli`. `nf env shell`, `nf env sh`, and `nf env wp` run in the WordPress container as `docker_user`, which defaults to `nonfiction` and can be changed with:

```sh
nf config set-docker-user <user>
```

## Stop or Reset

```sh
nf env down
nf env reset
nf env reset --rebuild
```

`nf env reset` is destructive for the local env only. It creates a safety snapshot, removes Docker Compose volumes, and recreates the env. Add `--rebuild` to rebuild the generated WordPress image during recreation.

## Data Location

Generated env data lives under:

```text
~/.local/share/nf/envs/<project-slug>/
```

Override for tests or isolated runs:

```sh
NF_DATA_HOME=/tmp/nf-data
```

Local snapshots are covered in [Snapshots](snapshots.md). Remote data sync is covered in [Sync](sync.md).

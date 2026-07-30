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

`nf env up` is idempotent. It starts Docker Compose, configures Mailpit SMTP, reconciles configured `wp-config.php` defines, installs WordPress if needed, and ensures the mounted theme is active.

Local env commands use built-in defaults when `local` is absent from `nf.json`. Add `local` only for non-default overrides:

```json
{
  "local": {
    "compose": "docker compose -f docker-compose.local.yml",
    "wordpress_service": "cms",
    "uploads_path": ".nf-uploads",
    "admin_user": "client-admin",
    "ports": {
      "wordpress": 9080,
      "mailpit": 9025,
      "db": 9081
    }
  }
}
```

All fields within `local` are optional. Missing or zero port values use ports derived from the project slug.

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

`nf env show` prints the site URL, WordPress login, Mailpit URL, and a DB URL prefilled with the database host/user/name.

Generated WordPress config enables `WP_DEBUG` and `WP_DEBUG_LOG`, disables debug display, and routes local mail through Mailpit. `nf env logs` tails Docker logs for the WordPress service.

## Run Shell and WP-CLI

```sh
nf env shell
nf env sh
nf env wp -- plugin list
```

The generated WordPress Docker image includes useful CLI tools plus WP-CLI. `nf env shell`, `nf env sh`, and `nf env wp` run in the WordPress container as `docker_user`, which defaults to `nonfiction` and can be changed with:

```sh
nf config set docker.user <user>
```

## Reconcile Defines

Project-required `wp-config.php` constants can be declared in `nf.json` under `wordpress.defines`. `nf env up` applies local configured defines automatically. You can inspect or apply them explicitly with:

```sh
nf define status
nf define sync
```

Remote envs use repo remotes:

```sh
nf define status production
nf define sync production
```

Status and list output show define names and whether each value is literal, encrypted, or from a legacy environment reference, but do not print resolved secret values. Encrypted define values are read from the committed project `nf.age` file using the agency identity derived from `NF_PASSWORD_SALT`.

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

# Nterchange and SSH targets

Nterchange support covers project metadata, registration of an existing SSH target, read-only site discovery, repo remote aliases, local configuration inspection and guarded SQL/media pull. It does not provision DigitalOcean resources, deploy code, roll back releases or manage local container lifecycle yet.

WordPress commands stay separate. In a nterchange project, unsupported environment mutations and WordPress theme/plugin/define/alias commands fail without changing the environment.

## Initialize an existing repository

Keep the site's existing `compose.yaml`. Initialization does not generate a replacement stack or change its data.

```sh
nf init --type nterchange
nf env show
```

The project slug defaults to the Git root's name. `--project-slug` overrides it. Initialization refuses an existing `nf.json`, theme options and `--force`.

A minimal manifest is:

```json
{
  "version": 2,
  "project": {"slug": "client", "password_version": 0},
  "nterchange": {}
}
```

The default media directory is `public_html/upload`. Declare additional mutable media explicitly, such as Alaris's untracked videos:

```json
"nterchange": {
  "media": ["public_html/upload", "public_html/videos"]
}
```

Media paths must be clean, non-overlapping paths below `public_html`. Hidden paths and directory traversal are rejected. WordPress configuration and its `local` overrides cannot be combined with `nterchange`.

The local recipe uses services named `app` and `db`. Existing database volumes, bind mounts, credentials, integration restrictions and mail capture remain owned by that recipe. `nf env show` reads metadata only. It does not start containers, load application code, or generate environment files.

## Register an existing SSH target

```sh
nf target add ssh php85 --host php85.nfweb.ca --user nonfiction
nf target list
nf target show php85 --json
nf site refresh php85
```

Optional flags are `--port`, `--identity-file` and `--known-hosts-file`. Identity and known-hosts files must already exist. Registration stores their paths, never their contents. Establish and verify host trust first. Discovery uses batch mode and strict host-key checking; it will not accept an unknown host key automatically.

`nf site refresh <target>` reads only that target and replaces only its cached environments. It preserves unrelated providers and stale records belonging to other targets. Without a target, refresh retains its existing all-target discovery and stale-target pruning behavior.

Registration writes `ssh-targets.json` under `NF_CONFIG_HOME`, normally `~/.config/nf/`. This is durable connection configuration, not provider inventory. Provider cache refreshes do not erase it. Duplicate names and collisions with cached cloud targets are rejected. Registration does not connect to the host or change it. `target remove` remains Linode-only; it cannot delete an SSH-registered Droplet.

## Publish non-secret site inventory

The target must have a readable `/var/lib/nf/sites.json`. It is an array of site/environment records:

```json
[
  {
    "name": "client",
    "env": "review",
    "kind": "nterchange",
    "url": "https://client.php85.nfweb.ca",
    "path": "/srv/php85/sites/client",
    "compose_file": "/srv/php85/compose.yaml",
    "service": "client",
    "database": "client",
    "media": ["public_html/upload"]
  }
]
```

These are application facts, not credentials. Never put passwords, private keys, environment file contents or API tokens in this file. Unknown fields, duplicate environments, invalid paths, credential-bearing URLs and inventories above 1 MiB are rejected. A failed refresh preserves the existing site cache for that target.

Discovery produces normal nf environment refs such as `client.php85:review`. Connect a project to that environment with existing remote commands:

```sh
nf remote add review client.php85:review
nf remote show review
nf env show
```

Only nterchange records are supported by this SSH inventory reader. Aero Rentals and Tagg need a separate application model; do not label those sites as nterchange to make discovery accept them.

## Data ownership on php85

All persistent Docker mounts on the current php85 host are bind mounts:

- MySQL data is under `/srv/php85/data/mysql`, shared by seven isolated schemas.
- Site code is under `/srv/php85/sites/<site>` and mounted read-only.
- Each site's `public_html/upload` and `var` directories are writable bind mounts.
- Environment files are under `/srv/php85/secrets` and mounted read-only.
- Mailpit data is under `/srv/php85/data/mailpit`.

Alaris's `public_html/videos` currently sits inside its read-only site tree but is untracked media. It must be transferred as media and kept outside future code replacement. Local recipes currently use named database volumes plus bind-mounted source, uploads and runtime directories. Container replacement does not erase either kind of persistent storage.

## Preview and verify a data pull

Run from the site's nterchange repository with an explicit configured remote:

```sh
nf env pull review --dry-run
nf env pull review --check
```

`--dry-run` prints the source site/environment, SSH host, local Compose file and media paths without SSH or Docker calls. Bare `pull review` and `--non-interactive` without execution also print a plan only. `--check` makes read-only SSH, Docker and SQL metadata calls. It does not export rows, transfer media, create snapshots, restart containers or write the site cache. Do not combine `--check` with `--dry-run` or `--execute`. To import, use `--execute` and confirm the local replacement. Non-interactive import requires `--execute --yes`.

Preflight requires the project slug and media declarations to match the selected remote. It re-reads target inventory so stale cached paths or database names cannot silently select a different source. Both runtimes must have one running, healthy app and db container, a read-only site-root bind, writable site-specific `var` and upload binds, real media directories and persistent MySQL storage. Additional media such as Alaris videos is checked too.

Local checks use the existing `app` and `db` services and read the mounted env file when present, otherwise the repo's `.env`, without changing it. Container environment assignments take precedence. Most sites use `DB_SERVER`, `DB_DATABASE`, `DB_SERVER_USERNAME`, `DB_SERVER_PASSWORD` and `PUBLIC_URL`; PetroChina uses container `DB_HOST`, `DB_NAME`, `DB_USER`, `DB_PASSWORD` and `PUBLIC_SITE`. The app's host must be `db`, its schema and credentials must match the database container's `MYSQL_DATABASE`, `MYSQL_USER` and `MYSQL_PASSWORD`, and its public URL must be loopback HTTP(S). Actual app/db networks must be internal, and MySQL `event_scheduler` must be OFF or DISABLED. MySQL 8.4 can default to ON. Disable it in a local recipe before importing production data; preflight will not change that setting for you. Mail capture and other integration restrictions remain owned by the local recipe, not managed or fully audited by this check.

The current remote adapter follows php85's Compose contract: the app service comes from inventory, the database service is `db`, its client configuration is a read-only bind at `/run/secrets/mysql-client.cnf`, and the app's mounted env file supplies `DB_SERVER=db` and `DB_DATABASE`. Conflicting host/schema overrides in the container environment are rejected. Python 3 reads only these two non-secret values on the target. Other dotenv contents stay there. SQL checks inspect schema defaults and table engine counts, not content rows. Reported schema charset/collation defaults need not match every existing table's charset.

## Pull SQL and media into local development

```sh
nf env pull review --execute
# Explicit non-interactive replacement of local data:
nf env pull review --execute --yes --non-interactive
```

Pull stages one schema dump and all declared media before changing local data. Media staging is seeded from local files and updated with checksum-based rsync, so only changed bytes travel over SSH. Removed source files are removed from the local media copy too. The source needs rsync and non-interactive sudo access to Docker and media.

InnoDB-only schemas use a single-transaction dump. Mixed MyISAM/InnoDB schemas hold explicit READ locks on that schema's tables for the dump, blocking writes, including MyISAM inserts, without a global read lock. Avoid editorial work during a mixed-engine export. SQL and media are separate transfers, not an atomic snapshot of a site under active editing.

Both schemas must contain only InnoDB/MyISAM base tables, with no views, triggers, stored routines or events. Unsupported objects fail rather than being silently omitted. Imports use the local container's non-root `MYSQL_USER`, which must have privileges only on its own schema. No database grants or users are copied.

After staging, nf rechecks the local runtime, pauses only the local app and saves the previous SQL and media. A private safety copy remains under `NF_DATA_HOME/nterchange/pulls/<project>/pull-*/before/`; `snapshot.json` marks a complete copy. The import replaces local tables, preserves per-table charsets and sets the local schema defaults to the source defaults. Local environment files, credentials, URLs, source, sessions and other `var` files are not copied or rewritten. Never copy raw MySQL storage for development or run WordPress URL rewriting on nterchange data.

If apply fails, nf restores the previous SQL/media and resumes the app. If recovery fails, it leaves the app paused and prints the retained recovery directory. A killed process can leave the local app paused and a `pull.lock` containing its PID/root. Inspect the app state and retained safety copy before removing that lock or retrying. There is no nterchange `snapshot use` command yet; interrupted or failed recovery requires manual restoration. Safety copies have no automatic retention policy. They contain site data and must stay private.

## Remaining code deployment work

Code deployment will package reviewed source and locked production dependencies, then activate a per-site release with health checks and rollback. A normal code deployment must not replace a database, media, `var`, or environment credentials. Schema/content migrations need separate approval.

DigitalOcean automatic backups are the host-recovery baseline. They are not a development sync mechanism. No recurring whole-host download to a developer's machine is needed for either workflow.

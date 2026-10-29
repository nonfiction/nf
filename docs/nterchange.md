# Nterchange and SSH targets

Nterchange support covers project metadata, registration of an existing SSH target, read-only site discovery, repo remote aliases, local configuration inspection, guarded SQL/media pull, locked code packaging and per-site code activation/rollback. It does not provision DigitalOcean resources or manage local container lifecycle. Release tests used a disposable synthetic PHP target only; existing CMS deployment still requires separate review.

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

Human-readable output follows the existing WordPress conventions: `project:local` headings, aligned detail rows, separate sections, shared pull preflight layout and operation plans. Nterchange details remain application-specific; `env show` does not infer or expose database credentials or runtime settings. Target/site/remote tables and details still use the shared handlers; supported JSON output is unchanged.

## Prepare a target without a provisioner

Use the [agent setup prompt](nterchange-target-setup.org) and [single-client configuration examples](../examples/nterchange-target/) for a future target. They are reviewed inputs for an operator, not an installer or permission to create resources. Supply approved source/images, private credential files, hostnames, certificates, schema/account setup and persistent directories before starting anything. Do not install the fictional example over an existing stack. The example images/configuration have not been booted with a real CMS by this documentation change.

Copy the selected Compose, Docker and nginx configuration to `/etc/nf/nterchange/`, excluding any real secrets from Git and the Docker build context. Adapt the example's client and certificate names explicitly. Create bind sources with the verified ownership/modes first; `create_host_path: false` deliberately rejects missing paths. Only approved data imports or activation may populate a site/database. The example captures mail and disables MySQL events, but actual integration restrictions and session retention still require site review.

The TLS proxy template includes `proxy_redirect http://$host/ https://$host/;` so Apache-generated same-host directory redirects stay on HTTPS. Preserve CSP and review authentication rather than disabling `form-action 'self'`. It does not rewrite external-host redirects. The example's static health endpoint is `/_nf/health`; existing targets may retain their established health URL.

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
    "env": "live",
    "kind": "nterchange",
    "url": "https://client.php85.nfweb.ca",
    "path": "/var/www/sites/client",
    "compose_file": "/etc/nf/nterchange/compose.yaml",
    "service": "client",
    "database": "client",
    "media": ["public_html/upload"]
  }
]
```

These are application facts, not credentials. Never put passwords, private keys, environment file contents or API tokens in this file. Unknown fields, duplicate environments, invalid paths, credential-bearing URLs and inventories above 1 MiB are rejected. A failed refresh preserves the existing site cache for that target.

Discovery produces normal nf environment refs such as `client.php85:live`. Use `live` for the canonical environment when a site has one deployment on this target. A protected review hostname can still serve that environment; public-domain cutover and integration approval are separate decisions, not inferred from the nf name. Connect a project to that environment with existing remote commands:

```sh
nf remote add live client.php85:live
nf remote show live
nf env show
```

Only nterchange records are supported by this SSH inventory reader. Aero Rentals and Tagg need a separate application model; do not label those sites as nterchange to make discovery accept them.

## Host layout and data ownership

Use hostname-neutral paths. The target alias, DNS name and PHP version do not determine filesystem roots. The original review stack used `/srv/php85`; nf did not require that prefix. Explicit inventory and Compose paths are authoritative, so existing targets can be relocated without changing application code or consumer manifests.

The recommended bind-mount layout is:

- Site code under `/var/www/sites/<site>`, mounted read-only with its native `public_html` directory.
- Writable site-specific `public_html/upload` and `var` binds; never reset sessions during container replacement.
- Compose, Docker/Apache/gateway and retained TLS proxy configuration under `/etc/nf/nterchange/`.
- Private environment/client files under `/etc/nf/nterchange/secrets`, mounted read-only.
- MySQL data under `/var/lib/nf/nterchange/mysql`, with a separate schema/account per CMS.
- Mailpit data under `/var/lib/nf/nterchange/mailpit`.
- Non-secret discovery inventory at `/var/lib/nf/sites.json`; do not fabricate WordPress/Linode target metadata for an SSH target.

A layout migration needs its own approval. Stop writers and shut MySQL down cleanly before same-filesystem renames, update every bind/inventory/tool reference and recreate services with existing cached images and unchanged Compose project/network identity. Refuse a cross-filesystem copy fallback or an empty database bind. Keep a small config/rename rollback record and existing recovery archives, not a duplicate live data tree. Do not run a full backup, dump or restore merely to rename directories. This is separate from nf code activation and from any public cutover.

Alaris's `public_html/videos` currently sits inside its read-only site tree but is untracked media. It must be transferred as media and kept outside future code replacement. Local recipes currently use named database volumes plus bind-mounted source, uploads and runtime directories. Container replacement does not erase either kind of persistent storage.

## Preview and verify a data pull

Run from the site's nterchange repository with an explicit configured remote:

```sh
nf env pull live --dry-run
nf env pull live --check
```

`--dry-run` prints the source site/environment, SSH host, local Compose file and media paths without SSH or Docker calls. Bare `pull live` and `--non-interactive` without execution also print a plan only. `--check` makes read-only SSH, Docker and SQL metadata calls. It does not export rows, transfer media, create snapshots, restart containers or write the site cache. Do not combine `--check` with `--dry-run` or `--execute`. To import, use `--execute` and confirm the local replacement. Non-interactive import requires `--execute --yes`.

Preflight requires the project slug and media declarations to match the selected remote. It re-reads target inventory so stale cached paths or database names cannot silently select a different source. Both runtimes must have one running, healthy app and db container, a read-only code-root bind, writable site-specific `var` and upload binds, real media directories and persistent MySQL storage. Local code must remain bound to the project checkout. Remote code may use the original inventory root or a verified managed release root. Additional media such as Alaris videos is checked too.

Local checks use the existing `app` and `db` services and read the mounted env file when present, otherwise the repo's `.env`, without changing it. Container environment assignments take precedence. Most sites use `DB_SERVER`, `DB_DATABASE`, `DB_SERVER_USERNAME`, `DB_SERVER_PASSWORD` and `PUBLIC_URL`; PetroChina uses container `DB_HOST`, `DB_NAME`, `DB_USER`, `DB_PASSWORD` and `PUBLIC_SITE`. The app's host must be `db`, its schema and credentials must match the database container's `MYSQL_DATABASE`, `MYSQL_USER` and `MYSQL_PASSWORD`, and its public URL must be loopback HTTP(S). Actual app/db networks must be internal, and MySQL `event_scheduler` must be OFF or DISABLED. MySQL 8.4 can default to ON. Disable it in a local recipe before importing production data; preflight will not change that setting for you. Mail capture and other integration restrictions remain owned by the local recipe, not managed or fully audited by this check.

The current remote adapter follows php85's Compose contract: the app service comes from inventory, the database service is `db`, its client configuration is a read-only bind at `/run/secrets/mysql-client.cnf`, and the app's mounted env file supplies `DB_SERVER=db` and `DB_DATABASE`. Conflicting host/schema overrides in the container environment are rejected. Python 3 reads only these two non-secret values on the target. Other dotenv contents stay there. SQL checks inspect schema defaults and table engine counts, not content rows. Reported schema charset/collation defaults need not match every existing table's charset.

## Pull SQL and media into local development

```sh
nf env pull live --execute
# Explicit non-interactive replacement of local data:
nf env pull live --execute --yes --non-interactive
```

Pull stages one schema dump and all declared media before changing local data. Media staging is seeded from local files and updated with checksum-based rsync, so only changed bytes travel over SSH. Removed source files are removed from the local media copy too. The source needs rsync and non-interactive sudo access to Docker and media.

InnoDB-only schemas use a single-transaction dump. Mixed MyISAM/InnoDB schemas hold explicit READ locks on that schema's tables for the dump, blocking writes, including MyISAM inserts, without a global read lock. Avoid editorial work during a mixed-engine export. SQL and media are separate transfers, not an atomic snapshot of a site under active editing.

Both schemas must contain only InnoDB/MyISAM base tables, with no views, triggers, stored routines or events. Unsupported objects fail rather than being silently omitted. Imports use the local container's non-root `MYSQL_USER`, which must have privileges only on its own schema. No database grants or users are copied.

After staging, nf rechecks the local runtime, pauses only the local app and saves the previous SQL and media. A private safety copy remains under `NF_DATA_HOME/nterchange/pulls/<project>/pull-*/before/`; `snapshot.json` marks a complete copy. The import replaces local tables, preserves per-table charsets and sets the local schema defaults to the source defaults. Local environment files, credentials, URLs, source, sessions and other `var` files are not copied or rewritten. Never copy raw MySQL storage for development or run WordPress URL rewriting on nterchange data.

If apply fails, nf restores the previous SQL/media and resumes the app. If recovery fails, it leaves the app paused and prints the retained recovery directory. A killed process can leave the local app paused and a `pull.lock` containing its PID/root. Inspect the app state and retained safety copy before removing that lock or retrying. There is no nterchange `snapshot use` command yet; interrupted or failed recovery requires manual restoration. Safety copies have no automatic retention policy. They contain site data and must stay private.

## Package a reviewed code commit

```sh
nf env package --ref <full-reviewed-commit-id>
```

The full lowercase Git commit ID is required. Branch names, `HEAD`, abbreviated IDs and dirty-worktree packaging are not accepted. The operation reads Git objects directly, without index/worktree files, export attributes, replacement objects or submodule fetching. Changes staged or edited after that commit cannot enter its payload. The selected commit must contain a valid nterchange `nf.json` matching the current project slug and ordered media declarations, non-empty `composer.json`, `composer.lock`, `conf/conf.php`, `public_html/index.php` and site `app/` code. Uncommitted consumer setup cannot be packaged until it belongs to a reviewed commit. This command does not authorize creating or publishing that commit.

Packages contain `code/` and `manifest.json`, under private `NF_DATA_HOME/nterchange/packages/<project>-<commit-prefix>-<unique>/`. Build trees stay private in the same parent and are removed on ordinary failure. A killed process may leave a `.building-*` directory; inspect it before removing it. There is no retention policy. Data storage must not overlap the checkout, config home or state home, including through existing symlinks. No SSH, Docker, site cache, settings or runtime operation occurs.

Only committed `app/`, `conf/`, `lib/`, `public_html/`, legacy `vendor/`, `nf.json` and root Composer manifests are source inputs. Composer-owned `vendor/autoload.php`, `vendor/composer/` and `vendor/nonfiction/` are never copied from source. Composer rebuilds those paths in staging. Existing tracked legacy libraries under other vendor directories remain, as required by six current consumers. The two existing `public_html/nterchange -> ../vendor/nonfiction/nterchange/nterchange` links are supported; other source links and included submodules are rejected. Final links must be relative, resolve within the package and not target excluded data.

Both the source selection and final dependency tree exclude declared media, always including `public_html/upload`, plus hidden paths except `.htaccess`, environment/key/auth files, SQL/database dumps, sessions, `var`, caches, logs, temporary files, backups and node_modules. Declared extra media such as `public_html/videos` stays out. Git metadata, Compose/Docker recipes, tests, frontend tooling and other root files are not code inputs. PEAR's `vendor/nonfiction/nterchange/compat/pear/Cache/` contains required PHP library code, not runtime cache data, and is retained alongside PEAR `Log/`. Other libraries' cache directories and credential files inside either library remain excluded. Name-based exclusions cannot recognize arbitrary credentials embedded in PHP or assets. Review the selected source for secrets before packaging.

Composer runs `validate --check-lock`, `install --no-dev --prefer-dist --optimize-autoloader` and `check-platform-reqs --no-dev` in staging, always with `--no-interaction --no-plugins --no-scripts`. Its environment contains only tool paths and private temporary home/cache paths, not inherited authentication, database or application environment values. Stale locks, missing platform requirements, install failures, changed staged locks and an installed dependency set differing from the production lock reject the package. No `update` runs. Only library/metapackage dependencies are supported; libraries require an HTTPS zip and full commit reference, with a matching HTTPS Git source fallback if present. Custom repositories and Composer config outside the current consumers' `platform`, `allow-plugins` and `sort-packages` settings are rejected rather than allowed to redirect filesystem writes. Private dependency authentication is not implemented. Composer is not a general sandbox for untrusted source.

The manifest records source commit, project, excluded media, lock SHA-256, production dependency versions/references and each file's path, size, mode and SHA-256. Link entries record the target and hash its bytes. This is an inspectable directory package, not a signed artifact or an activation command. Regular code files use mode 0644, or 0755 when executable, regardless of the build process's umask; the containing package stays private. Deployment dry-runs reject unsupported file modes before upload. Rebuild older artifacts whose restrictive build umask produced owner-only executables. Composer home/cache files do not survive in it.

No npm/webpack build or application bootstrap runs. Required assets must already be committed in the selected code tree; the packager does not know every inherited frontend output convention. Successful packaging and PHP platform checks do not prove CMS behavior, integrations or target health. File integrity proves that listed bytes are intact, not that the package contains every runtime dependency. Rebuild artifacts created by a faulty exclusion policy; upgrading nf alone cannot restore omitted files in an existing package. Existing duplicate-class Composer warnings remain visible and need application review, not a packaging workaround.

## Activate or roll back code

```sh
# Connection-free plans, including local package validation:
nf env deploy live --package /absolute/path/to/reviewed-package --dry-run
nf env rollback live --dry-run

# Only after target-specific approval:
nf env deploy live --package /absolute/path/to/reviewed-package --execute
nf env rollback live --execute
```

Both commands require a configured remote and default to a plan. `--execute` asks for confirmation before any connection; unattended use requires `--execute --yes`. `--yes` alone and `--execute --dry-run` are rejected. Deploy consumes an existing package rather than rebuilding current source. It validates project/media identity, complete file hashes/modes, links, lock/dependency metadata and installed production dependencies before connecting. It does not run Composer on the target. Upload staging is private under `NF_DATA_HOME/nterchange/transfers/`, separate from checkout/config/state/package, and removed on ordinary return. The target requires Python 3.9+, Docker Compose v2 and non-interactive sudo permission for the fixed worker. This is privileged code deployment, not a sandbox for untrusted code.

The worker rereads `/var/lib/nf/sites.json` and rejects changed inventory or a service shared by two records. It requires running healthy app/db containers, a read-only code-root bind at `/var/www/site`, stable writable site-specific `var` and upload binds, and a read-only regular environment-file bind. Unrecognized nested code mounts fail. Declared extra media receives an explicit stable bind if it was previously visible only through the original root, read-only by default; an existing media bind keeps its access mode. Docker volumes and database schemas are never imported, replaced or migrated by this operation.

State lives beside the original site root, in `<site-root-parent>/.nf-releases/<site>-<env>/`. A per-site flock excludes concurrent operations. Uploads go into private temporary directories and are verified against the file manifest before becoming `release-*` directories. The code bind is read-only. Empty `.env`, `var` and declared-media mount targets are added to staged code because Docker cannot create nested targets through a read-only bind. These empty targets are not credentials or runtime content; stable host binds cover them. Verification rejects content written into those empty targets. Other files and links remain covered by the manifest.

A private `active.compose.json` changes only the selected service's root bind and stable nested binds, pinning its currently running image ID. Compose recreates that app with `--no-deps --no-build --pull never`; it does not restart db, mail or other sites. Base Compose files and the original site tree remain unchanged. Subsequent operations reject changes to base Compose, image, inventory, original code fingerprint or managed package contents. Container replacement invalidates that app's PHP process cache and causes brief downtime. This is not zero-downtime deployment or an image-upgrade command.

Health requires the selected app to run without a failing Docker health status, with the expected image/environment and mounts, and the same network names. The db container ID and mounts must remain unchanged. PHP inside the app requests `http://127.0.0.1/` on port 80 with the inventory URL's Host header and requires HTTP 200 without following redirects. It retries up to 15 times, sleeping one second between failures; commands have separate 90-second limits. The URL is never requested over the public network. Redirect-only roots, non-port-80 apps and missing PHP fail this contract. HTTP 200 is a smoke check, not proof of CMS functions, schema compatibility or integrations. The request boots the app and can have normal application side effects. Separately review migrations and integration restrictions before approving a real site.

`state.json` records current/previous release IDs, stable site identity, image and base/original-code fingerprints. First activation records the untouched original code tree as the previous baseline. Rollback restores the previous recorded code, or that baseline after the first activation, without building/uploading anything. A successful rollback swaps current and previous, so another rollback toggles those two versions. Old and failed release directories are retained; there is no automatic pruning.

Before recreating the app, `pending.json` records the intended transition. If recreation, health or state recording fails, nf recreates the previous code and runs the same health checks. Successful recovery leaves recorded state unchanged and removes the journal, but the command still fails. Failure output retains the original cause, including the numeric container-local HTTP status or `none` when unavailable, without printing response bodies. Failed recovery reports both the original and recovery failure. Failed recovery retains the journal and reports its path. Interrupts, SSH loss or process termination may leave either release running and the journal present. Further deploy/rollback commands refuse to proceed. An interrupted upload before the journal can leave a private `.upload-*` directory without switching the app.

For manual recovery, inspect `pending.json`, `state.json`, `active.compose.json` and the actual app root mount before editing or deleting anything. `null` means the original inventory root; a release ID means that directory's `code/`. Restore the selected known-good root in `active.compose.json`, then use the original Compose recipe plus that override to recreate only the inventory app service with the same no-deps/no-build/pull-never flags. Verify HTTP, image, all stable mounts and the unchanged database container. Reconcile recorded current/previous state with what is actually running before removing the journal; inspect leftover `.tmp` files if an atomic write was interrupted. No automatic journal-repair command exists. Keep these files private and do not copy environment files into release directories.

After activation, manual service recreation must include the managed override; base Compose alone would mount the original code. Data-pull preflight uses the release worker's read-only `--inspect-root` mode to verify original or managed code ownership. It takes a shared nonblocking lock on an existing operation lock, without creating state or lock files. Pending recovery or an exclusive release operation blocks the check. Managed code requires matching fresh inventory, running image, base Compose hash, original-code fingerprint, selected release and complete package integrity. Missing state, mismatched mounts, symlinked state paths and tampered code fail closed. Rolling back to the original baseline remains valid with recorded state. Media and var checks still use the stable original site root. The verifier never recreates containers, requests HTTP, exports SQL or writes state. It verifies ownership at preflight time, not a lock across the later SQL/media transfer.

Managed-root compatibility has unit coverage for complete pull-check orchestration with mocked adapters and actual read-only worker coverage over strict loopback SSH. Its disposable fixture used a real container bind, real Compose config and synthetic release state, checked successful ownership plus pending/missing/locked/tampered state failures, and verified unchanged files and container ID. That fixture did not repeat SQL/media transfer, CMS boot, activation or production sudo checks.

Release verification used real loopback SSH, Docker Compose, PHP/Apache HTTP and a MySQL sentinel on an agent-owned target, with synthetic zero-dependency packages. The fixture exercised two activations, rollback, HTTP 500 recovery, integrity rejection, concurrent locking and an injected exit after real HTTP calls to test failed-recovery journaling. It stripped sudo in a forced-command SSH adapter, so it does not establish production sudo permissions. No existing CMS, frontend build, target health or php85 deployment was accepted by these tests. A normal code deployment must not replace a database, media, `var`, or environment credentials. Schema/content migrations need separate approval.

DigitalOcean automatic backups are the host-recovery baseline. They are not a development sync mechanism. No recurring whole-host download to a developer's machine is needed for either workflow.

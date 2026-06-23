# Commands

Top-level help is context-sensitive.

## Always Available Commands

```text
nf

Commands:
  provider    manage provider integrations
  target      manage deployable targets
  site        manage remote sites and envs
  domain      manage remote env domains
  password    derive passwords

  init        initialize project metadata
  config      manage global config
  completion  print shell completion scripts
  version     show nf version
  help        show help
```

## Project Commands

Inside an `nf` project repo with `nf.json` next to `.git`:

```text
nf

Commands:
  provider    manage provider integrations
  target      manage deployable targets
  site        manage remote sites and envs
  domain      manage remote env domains
  password    derive passwords

  remote      manage repo remotes
  env         manage the local development env
  theme       package clean artifacts and run theme tasks
  alias       manage root-level WordPress content aliases

  init        initialize project metadata
  config      manage global config
  completion  print shell completion scripts
  version     show nf version
  help        show help
```

## Providers, Targets, Sites, Domains, and Remotes

Commands:

```sh
nf provider list
nf provider show <provider>
nf provider check <provider>
nf refresh
nf target add linode <name> [--region region] [--type type] [--image image] [--db-user user] [--user user] [--keys all] [--execute --yes] [--wait]
nf target remove <target> [--dry-run] [--execute --yes]
nf target refresh
nf target list
nf target show <target>
nf target password [target] [--root|--db]
nf site add <target> <site> [--with-staging] [--password-version version] [--kinsta-slug slug] [--region region] [--php version] [--execute --yes]
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
nf domain list [site|site.target:env|remote]
nf domain add <site.target:env|remote> <domain> [domain...] [--proxy cloudflare|<ip>|--no-proxy] [--dry-run] [--execute --yes]
nf domain check <site.target:env|remote> [domain...] [--proxy cloudflare|<ip>|--no-proxy]
nf domain primary <site.target:env|remote> <domain> [--proxy cloudflare|<ip>|--no-proxy] [--search-replace|--no-search-replace] [--force] [--wait-timeout 30m] [--wait-interval 30s] [--dry-run] [--execute --yes]
nf domain remove <site.target:env|remote> <domain> [domain...] [--proxy cloudflare|<ip>|--no-proxy] [--delete-cert] [--dry-run] [--execute --yes]
nf site remove [site-id-or-alias] [--dry-run] [--execute --yes]
nf remote add [name] [site.target:env]
nf remote show <name>
nf remote remove <name>
nf remote list
nf alias list
nf alias status [remote]
nf alias sync [remote]
nf alias add <alias> <target>
nf alias remove <alias>
```

Standard Linode target example:

```sh
nf target add linode linode1 \
  --region ca-central \
  --type g6-standard-1 \
  --image linode/ubuntu24.04 \
  --db-user admin \
  --user nonfiction \
  --keys all
```

## Current Behavior

* `nf provider list` reports local credential status.
* `nf provider check` calls safe read-only provider health endpoints and writes `providers.json`.
* `nf provider show <provider>` reads cached provider metadata.
* `nf refresh` best-effort refreshes all configured provider metadata, target records, and site/env records. It runs every provider check, including DNSimple, then runs site discovery from whatever target cache is available, and exits non-zero if any provider or site refresh phase failed.
* `nf target add linode <name>` creates a Linode target named exactly `<name>`, tags it `nf`, creates host and wildcard DNS records under `base_domain`, queues HTTPS setup on the target with a systemd retry timer, installs the database UI at `https://<db-user>.<target-hostname>/` behind HTTP Basic auth during target provisioning, and records the target under the Linode provider in `providers.json`. Exact provider names such as `kinsta`, `linode`, `dnsimple`, `digitalocean`, and `droplet` are reserved; numbered variants such as `linode1` and `droplet1` are valid. Add `--db-user` to override `db_default_user` for that target only; the same value controls the database UI HTTP Basic user, shared MySQL user, and database UI subdomain label. Add `--wait` to keep the CLI attached through SSH, TLS, and health checks. Existing completed targets that predate the database UI are not upgraded in place by provider checks or target refresh.
* `nf target remove <target>` removes an empty Linode target.
* `nf target refresh` updates target records from configured target providers so added and removed targets are reflected in `providers.json`.
* `nf target list/show` read target records from `providers.json`, with a legacy `servers.json` fallback.
* `nf target show <target>` reads target cache and, for Linode targets, can hydrate `/var/lib/nf/target.json` over SSH to print the database UI URL, username, and derived password. The username defaults to `db_default_user`; the password is derived from the target metadata identity/purpose and `NF_PASSWORD_SALT`.
* `nf target password [target] [--root|--db]` prints only the derived Linode target root or database UI password. It does not support Kinsta targets.
* `nf site add <target> <site>` creates the live WordPress env on a target. Add `--with-staging` to create live and staging in one operation. Add `--password-version <version>` when the site must use a non-zero `project.password_version` from a repo that is not available to the command; the value must be an unsigned integer and `0` is the default. For Kinsta, `<site>` is the canonical `nf` project slug; add `--kinsta-slug <slug>` only when the Kinsta provider slug differs. The cache then stores `site_id` and `project_slug` from `<site>` plus `kinsta.slug` for the provider slug.
* `nf site staging status/add/remove` manages an optional staging env for an existing site. `rm` is a shorthand for `remove`.
* `nf site refresh` discovers sites from the cached target list. Kinsta refresh uses Kinsta API site/env/domain data and infers the canonical project slug from attached `nf` internal domains under `kinsta.<base_domain>` when present. Remote target site discovery is not implemented yet.
* `nf site list --envs`, `nf site show`, `nf site shell`, `nf site wp`, `nf site snapshot`, and `nf site export` read the local disposable site cache for now.
* `nf site password [site|env] [--wp|--db|--basicauth]` prints only one selected site password. `--wp` is the default. Env refs are accepted for `--db`; use a site ref for `--wp` or `--basicauth`. Linode WordPress, DB, and basic-auth passwords are derived from the site slug, purpose, `NF_PASSWORD_SALT`, and `project.password_version`; Kinsta DB password output uses the Kinsta SFTP password endpoint.
* `nf password derive <scope> <value...> [--password-version N]` derives a raw password using common scopes `wp-admin`, `mysql`, `basic-auth`, `linode-root`, and `db-admin`. Project scopes use `project.password_version` from the matching repo when available, or an explicit `--password-version`; target scopes do not use password versions.
* Linode site/env database creation grants the shared database access MySQL user privileges only on created site env databases and refuses to create a site DB user with the same name as the shared database access MySQL user. Site removal revokes per-database grants before dropping the databases.
* `nf site basicauth ...` uses `basicauth_default_user` from `config.json` and a per-site derived password with `project.password_version` as the rotation source. Linode envs are managed over SSH by updating the selected env nginx vhost, including multi-vhost target nginx scripts. Kinsta Password protection exists in MyKinsta, but currently requires manual MyKinsta use because no public API endpoint is exposed.
* `nf domain list` shows cached domain inventory keyed by full env IDs like `client.linode1:live`. Columns are `role` (`primary` or `secondary`), `management` (`internal` or `external`), and `status` (`active`, `verified`, `unverified`, or `pending`). The generated provider hostname is internal and is primary only until an external primary is set; after that it remains listed as an internal secondary fallback. For Kinsta, the internal domain also anchors the canonical `nf` project slug for refresh/adoption.
* `nf domain add ...` attaches external domains as secondaries and prints the DNS records the client must create. It never mutates public/client DNS, changes the primary domain, or runs search-replace. Kinsta domains are added through the Kinsta API and Kinsta verification/routing records are printed when available; if routing records are unavailable, the command points the user to MyKinsta Domains for the remaining DNS instructions. Linode domains create one nginx vhost, certbot script, and retry timer per domain on the target.
* `nf domain check ...` is read-only and reports provider/server readiness, expected public DNS, HTTP reachability, HTTPS certificate status, and whether the domain is already primary. With no explicit domains, it checks cached external domains for the env. It exits `0` when public checks are ready and `2` when DNS, HTTP, HTTPS, or provider readiness is still pending. With `--proxy cloudflare`, Linode DNS checks verify public DNS resolves to Cloudflare IP ranges from Cloudflare's published list, skip origin-IP matching, and check direct Linode origin HTTPS with SNI so `Full (strict)` renewal problems are visible before Cloudflare starts returning 526 errors.
* `nf domain primary ...` launches one external domain as the primary public hostname for the env. By default execution approves once up front, polls the same readiness checks as `nf domain check`, then launches immediately when checks pass without a second prompt. `--wait-timeout` defaults to `30m`; `--wait-interval` defaults to `30s`. Add `--force` only to bypass readiness checks and launch immediately. Repo remotes in `nf.json` continue to point at env IDs, not domains.
* `nf domain remove ...` retires external domain bindings after a rename or target move. Linode removal deletes each nf-managed domain vhost, script, certbot timer/service, and domain metadata, then resets cached `hostname`/`url` to the generated internal fallback when the removed domain was primary. It keeps Let's Encrypt lineages by default for rollback safety; add `--delete-cert` only after the rollback window. Kinsta removal deletes non-primary domains from the Kinsta environment and refuses to remove the current primary domain.
* `nf site remove [site]` removes a whole Linode site and deletes its env data.
* `nf remote add` validates an env ID against the cache, then repo remotes are stored in `nf.json` under `remotes` as `<site>.<target>:<env>` refs.
* `nf alias ...` manages root-level webroot symlinks declared in top-level `aliases` in `nf.json`. Alias targets must be `wp-content` or descendants. `nf alias status [remote]` reports configured, missing, conflicting, and stale symlinks. `nf alias sync [remote]` creates or updates configured symlinks and prunes stale root symlinks, but never overwrites or removes real files/directories.
* `nf site shell/wp ...` validate the cache, print the SSH or wp-cli command preview, then execute the remote command.
* `nf env logs <remote>` resolves a configured repo remote, prints the SSH command preview, ensures `wp-content/debug.log` exists, and tails it on the remote host.
* `nf env import <source>` imports external WordPress data into the local env after creating a safety snapshot. It never writes directly to a remote env.
* `nf env push/pull [remote]` syncs database and mutable `wp-content` after an interactive confirmation. Omit `remote` to pick from configured repo remotes. Add `--dry-run` for a non-mutating plan, or use `--non-interactive` without `--execute` for preflight-only output.

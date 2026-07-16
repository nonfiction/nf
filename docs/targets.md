# Targets

Targets are deployable destinations for hosted WordPress sites. Linode targets are provisioned by `nf`; Kinsta appears as a provider target named `kinsta` after provider checks.

## Refresh Provider Inventory

Check provider credentials and update the local target cache:

```sh
nf provider list
nf provider check dnsimple
nf provider check linode
nf provider check kinsta
nf target list
```

Provider checks call live APIs when credentials are present. `nf provider check dnsimple` validates access to the configured `base_domain` zone and writes no targets. `nf provider check linode` discovers Linode instances tagged `nf`. `nf provider check kinsta` writes one target named `kinsta`.

## Add a Linode Target

Preview first:

```sh
nf target add linode linode1 \
  --region ca-central \
  --type g6-standard-1 \
  --image linode/ubuntu24.04 \
  --db-user admin \
  --ssh-user nonfiction \
  --all-linode-ssh-keys \
  --dry-run
```

Execute explicitly in automation:

```sh
nf target add linode linode1 \
  --region ca-central \
  --type g6-standard-1 \
  --image linode/ubuntu24.04 \
  --db-user admin \
  --ssh-user nonfiction \
  --all-linode-ssh-keys \
  --execute --yes --wait
```

`nf target add linode <name>` creates a target named exactly `<name>`, tags it `nf`, creates host and wildcard DNS records under `base_domain`, queues HTTPS setup on the target with a systemd retry timer, installs the database UI at `https://<db-user>.<target-hostname>/` behind HTTP Basic auth, and records the target under the Linode provider in `providers.json`. Reserved provider names such as `kinsta`, `linode`, `dnsimple`, `digitalocean`, and `droplet` cannot be used as exact target names; use a specific name such as `linode1` or `droplet1` instead.

Use `--wait` to keep the CLI attached through SSH, TLS, and health checks. Without `--execute`, target add is a dry-run.

## Inspect a Target

```sh
nf target list
nf target show linode1
nf target password linode1 --root
nf target password linode1 --db
```

`nf target show <target>` prints the database UI URL, username, and derived password for Linode targets when metadata is available. The raw database UI password is not stored.

## Remove an Empty Target

```sh
nf target remove linode1 --dry-run
nf target remove linode1 --execute --yes
```

`nf target remove <target>` removes an empty Linode target. It does not remove non-empty targets.

## Notes

Local target records are cache. Provider truth is canonical remotely.

Remote target site discovery is not implemented yet. After target and site changes, use the relevant refresh commands so local cache is current:

```sh
nf refresh
nf target refresh
nf site refresh
```

`nf refresh` is the broad best-effort command: it runs all provider checks, including DNSimple, then refreshes site/env records from the resulting target cache.

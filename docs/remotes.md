# Remotes

Repo remotes connect an `nf` project repository to cached remote env IDs. They are stored in `nf.json` under `remotes`.

## Add a Remote

Refresh the site cache first when needed:

```sh
nf site refresh
nf site list --envs
```

Add a remote from the project repo:

```sh
nf remote add production client.linode1:live
```

`nf remote add` validates that the requested site/env exists in the local cache before writing `nf.json`. The env ref format is:

```text
<site>.<target>:<env>
```

Examples:

```text
client.linode1:live
client.linode1:staging
client.kinsta:live
```

## Inspect Remotes

```sh
nf remote list
nf remote show production
```

`nf remote show <name>` prints the env ID, provider, target, URL, and access details when the cache has a matching env.

## Use Remotes

Commands that accept repo remotes include:

```sh
nf theme deploy production --dry-run
nf theme rollback production --dry-run
nf alias status production
nf alias sync production
nf plugin status production
nf define status production
nf define sync production
nf env logs production
nf env pull production --dry-run
nf domain list production
```

Keep remotes pointed at env IDs, not public domains. A public launch changes domain bindings for the env; it should not change `nf.json` remotes.

## Remove a Remote

```sh
nf remote remove production
```

This removes the repo metadata entry only. It does not modify the remote site/env.

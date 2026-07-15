# Sync

`nf env push` and `nf env pull` sync the database and mutable `wp-content` between the local env and a configured repo remote.

Database and uploads sync are high risk. Treat every production push as potentially destructive.

## Direction

Pull remote data into local development:

```sh
nf env pull production --dry-run
```

Push local data to a remote env:

```sh
nf env push production --dry-run
```

If you omit the remote, `nf` prompts you to choose from configured repo remotes:

```sh
nf env pull --dry-run
```

In non-interactive mode, provide the remote explicitly.

## What Sync Moves

Sync includes:

* database
* `wp-content/uploads`
* `wp-content/plugins`
* `wp-content/languages`

Sync intentionally skips `wp-content/mu-plugins`. MU plugins are target-owned platform files: local Docker owns local-only helpers such as `nf-mailpit.php`, Kinsta owns `kinsta-mu-plugins.php` and its companion directory, and Linode owns nf cache integration. Use `nf site repair <site:env>` to restore provider platform files when an existing env was damaged by older sync behavior.

Theme deployment is separate. Use [Themes](themes.md) for theme releases.

After the destination database is imported, `nf` updates the destination URLs, runs search-replace when needed, activates the configured theme when installed, and runs `wp rewrite flush`. This regenerates WordPress rewrite rules without `--hard`. The existing object-cache flush remains a separate later step. A rewrite failure fails the sync and prevents the cache step from being reported as complete.

## Review Before Execution

Dry-run prints the local project, local env path, remote name, site, env, provider, URL, access summary, and mode:

```sh
nf env pull production --dry-run
nf env push production --dry-run
```

No data is changed in dry-run mode.

Non-interactive preflight without execution is also non-mutating:

```sh
nf env pull production --non-interactive
```

## Execute a Sync

Interactive execution prompts for confirmation:

```sh
nf env pull production
nf env push production
```

Non-interactive execution requires both `--execute` and `--yes`:

```sh
nf env pull production --execute --yes --non-interactive
nf env push production --execute --yes --non-interactive
```

## Safety Checklist

Before pulling remote data into local:

* Confirm the remote points at the intended env with `nf remote show <remote>`.
* Create a local snapshot if you may need to restore current local work.
* Use `--dry-run` and read the plan.

Before pushing local data to a remote:

* Confirm the remote is not production unless production overwrite is intended.
* Confirm stakeholders understand this syncs the database and mutable `wp-content`.
* Create or verify a recoverable snapshot of the destination env.
* Use `--dry-run` and read the plan.
* Execute only when the source and destination are explicit and correct.

Key rule: never silently clobber production credentials or content.

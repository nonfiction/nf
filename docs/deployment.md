# Deployment

This page is an overview of deployment-related workflows. Use the practical guides for step-by-step commands.

## Theme Releases

```sh
nf theme deploy <remote> [--dry-run]
nf theme rollback <remote> [--dry-run]
```

`nf theme deploy <remote>` is a one-command packaged release deploy. It builds the same theme artifact as `nf theme package`, uploads it to the selected remote env, extracts it under `wp-content/themes/.nf-releases/<theme-slug>/`, copies the release into the active theme directory, activates the configured theme slug with WP-CLI, records release metadata, and then regenerates WordPress rewrite rules with `wp rewrite flush`.

Theme deploy keeps the last 5 releases and matching uploaded zips, so release storage does not grow indefinitely. It does not require manual WordPress admin zip upload and supersedes direct in-place source rsync deploys.

`nf theme rollback <remote>` switches the active theme directory back to the previous recorded release, activates the configured theme slug again, and then regenerates WordPress rewrite rules. It uses remote `releases.json`; it does not rebuild or upload artifacts. See [Themes](themes.md).

## Root Aliases

Use aliases when existing WordPress content under `wp-content` must also be reachable from a root-level URL path, such as `/files` or `/annual-report-2026`.

Inspect and sync aliases separately from theme releases:

```sh
nf alias status production
nf alias sync production
```

`nf` manages symlinks only. It does not upload files, manage access control, or route through PHP. `nf alias sync` creates or updates configured symlinks and prunes stale root symlinks, but never overwrites real files/directories. See [Aliases](aliases.md).

## Public Domain Launches

Use the domain guide when launching a remote env on a client-owned public domain:

```sh
nf domain add production www.client.com client.com --dry-run
nf domain check production www.client.com client.com
nf domain primary production www.client.com --search-replace --dry-run
```

Public DNS remains the client's responsibility; `nf` attaches domains, prints DNS instructions, verifies readiness, and performs the primary cutover. See [Domains and Launch](domains.md).

## Sync Safety

Database and uploads sync are high risk. Sync commands must print a reviewable plan, preserve production credentials where possible, and require confirmation before destructive changes.

`nf env push/pull [remote]` syncs database and mutable `wp-content` after an interactive confirmation. After destination URL and theme finalization, sync regenerates WordPress rewrite rules before separately flushing object cache. Omit `remote` to pick from configured repo remotes. Add `--dry-run` for a non-mutating plan, or use `--non-interactive` without `--execute` for preflight-only output. See [Sync](sync.md).

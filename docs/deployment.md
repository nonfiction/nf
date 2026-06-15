# Deployment

This page is an overview of deployment-related workflows. Use the practical guides for step-by-step commands.

## Theme Releases

```sh
nf theme deploy <remote> [--dry-run]
nf theme rollback <remote> [--dry-run]
```

`nf theme deploy <remote>` is a one-command packaged release deploy. It builds the same theme artifact as `nf theme package`, uploads it to the selected remote env, extracts it under `wp-content/themes/.nf-releases/<theme-slug>/`, copies the release into the active theme directory, activates the configured theme slug with wp-cli, and records release metadata.

Theme deploy keeps the last 5 releases and matching uploaded zips, so release storage does not grow indefinitely. It does not require manual WordPress admin zip upload and supersedes direct in-place source rsync deploys.

`nf theme rollback <remote>` switches the active theme directory back to the previous recorded release and activates the configured theme slug again. It uses remote `releases.json`; it does not rebuild or upload artifacts. See [Themes](themes.md).

## Static Public Artifacts

Use `public/` for static artifacts that must live at specific non-WordPress URL paths, such as annual report microsites.

Deploy them separately from the theme:

```sh
nf public deploy production --dry-run
nf public deploy production
```

`nf` only deploys paths explicitly listed in `nf.json`. `source` must be repo-relative and symlink-free. `path` must be an absolute URL path and cannot target `/`, traversal, `/wp-admin`, `/wp-content`, `/wp-includes`, or `/uploads`. Add `"delete": true` to mirror removals with `rsync --delete`; execution then requires `--yes`.

Remote HTTP crawling, archives, and rsync side-loaded sources are intentionally outside this first slice. See [Public Artifacts](public-artifacts.md).

## Public Domain Launches

Use the domain guide when launching a remote env on a client-owned public domain:

```sh
nf domain add production www.client.com client.com --primary --dry-run
nf domain check production www.client.com client.com
nf domain primary production www.client.com --search-replace --dry-run
```

Public DNS remains the client's responsibility; `nf` attaches domains, prints DNS instructions, verifies readiness, and performs the primary cutover. See [Domains and Launch](domains.md).

## Sync Safety

Database and uploads sync are high risk. Sync commands must print a reviewable plan, preserve production credentials where possible, and require confirmation before destructive changes.

`nf env push/pull [remote]` syncs database and mutable `wp-content` after an interactive confirmation. Omit `remote` to pick from configured repo remotes. Add `--dry-run` for a non-mutating plan, or use `--non-interactive` without `--execute` for preflight-only output. See [Sync](sync.md).

# Public Artifacts

Use `public/` for static artifacts that must live at specific non-WordPress URL paths, such as annual report microsites.

## Configure Paths

`nf` only deploys paths explicitly listed in `nf.json`:

```json
{
  "public": {
    "paths": [
      {
        "source": "public/annual-report-2026",
        "path": "/annual-report-2026"
      }
    ]
  }
}
```

`source` must be repo-relative and symlink-free. `path` must be an absolute URL path and cannot target `/`, traversal, `/wp-admin`, `/wp-content`, `/wp-includes`, or `/uploads`.

Add `"delete": true` to mirror removals with `rsync --delete`; execution then requires `--yes`.

## Deploy Paths

Configure a repo remote first. See [Remotes](remotes.md).

Preview:

```sh
nf public deploy production --dry-run
```

Deploy:

```sh
nf public deploy production
```

If any configured path has `"delete": true`, execute with:

```sh
nf public deploy production --yes
```

## Materialize Large Artifacts

For large artifacts that should not live in git, use a project task to materialize the deployable files into `public/` first, then deploy the local directory:

```json
{
  "tasks": {
    "fetch-annual-report-2026": {
      "description": "Fetch annual report static export",
      "run": "scripts/fetch-annual-report-2026"
    }
  },
  "public": {
    "paths": [
      {
        "source": "public/annual-report-2026",
        "path": "/annual-report-2026"
      }
    ]
  }
}
```

```sh
nf theme fetch-annual-report-2026
nf public deploy production --dry-run
```

Remote HTTP crawling, archives, and rsync side-loaded sources are intentionally outside this first slice.

# Aliases

Use aliases when content that already lives under WordPress `wp-content` should also be reachable from a root-level URL path.

Examples:

```text
/files -> wp-content/uploads/public/files
/esg-report-2025 -> wp-content/uploads/public/esg-report-2025
/annual-report.pdf -> wp-content/uploads/2026/annual-report.pdf
```

`nf` manages the root symlinks only. It does not upload files, copy artifacts from the repo, protect content, or route requests through PHP.

## Configure Aliases

Aliases live in `wordpress.aliases` in `nf.json`:

```json
{
  "wordpress": {
    "aliases": {
      "files": "wp-content/uploads/public/files",
      "esg-report-2025": "wp-content/uploads/public/esg-report-2025",
      "annual-report.pdf": "wp-content/uploads/2026/annual-report.pdf"
    }
  }
}
```

Store alias names without a leading slash. `nf` displays them with one.

Targets are relative to the WordPress document root and must be `wp-content` or a descendant path. Targets may be files or directories. Existing target symlinks are resolved before sync, and targets that resolve outside `wp-content` are unsafe.

Alias names are one top-level path segment. They cannot contain slashes, traversal, or reserved WordPress root names such as `wp-admin`, `wp-content`, `wp-includes`, `index.php`, `wp-login.php`, `xmlrpc.php`, `robots.txt`, `favicon.ico`, `sitemap.xml`, or `uploads`.

## Edit Aliases

From the project repo:

```sh
nf alias add files wp-content/uploads/public/files
nf alias remove files
nf alias list
```

`add` and `remove` edit `nf.json` only. They do not create, remove, upload, or delete WordPress content.

## Status And Sync

With no remote argument, status and sync target the local Docker env:

```sh
nf alias status
nf alias sync
```

For a repo remote, pass the remote as the final positional argument:

```sh
nf alias status production
nf alias sync production
```

`status` reports configured aliases, missing symlinks, missing targets, real-file or real-directory conflicts, wrong symlink targets, and stale root symlinks.

`sync` is authoritative over root-level symlinks. It creates missing configured symlinks, updates configured symlinks that point elsewhere, and prunes stale root-level symlinks that are no longer configured.

`sync` never overwrites or removes real files or real directories. It exits non-zero when a configured target is missing, resolves outside `wp-content`, or is blocked by real webroot content.

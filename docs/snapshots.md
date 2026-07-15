# Snapshots

Snapshots move WordPress database and mutable `wp-content` data through explicit, reviewable files. Theme files are not included in env snapshots.

## Local Snapshots

```sh
nf env snapshot add [name]
nf env snapshot list
nf env snapshot use [name] [--yes]
nf env snapshot remove [name]
nf env snapshot prune [--keep N] [--dry-run] [--yes]
```

Local env snapshots live under:

```text
~/.local/share/nf/snapshots/local/<project-slug>/<snapshot-name>/
```

Each snapshot contains:

* `snapshot.json`
* `database.sql.gz`
* `wp-content.tar.gz`

The `wp-content` archive includes only `uploads/`, `plugins/`, and `languages/`. It skips themes and target-owned `mu-plugins/`.

`nf env snapshot use` creates a safety snapshot named `YYYY-MM-DD-HHMMSS-pre-restore` before restoring the selected snapshot. Add `--yes` to skip the interactive confirmation.

## Remote Snapshots

Create a remote snapshot from cached remote site env records:

```sh
nf site snapshot client.linode1:live
nf site snapshot list
```

Remote snapshots live under:

```text
~/.local/share/nf/snapshots/remote/<env-id-slug>-YYYY-MM-DD-HHMMSS/
```

Import a remote snapshot into the current project's local snapshots:

```sh
nf env snapshot import <remote-snapshot-name> --name live-copy
nf env snapshot use live-copy --yes
```

Shortcut restore from a remote snapshot:

```sh
nf env snapshot use --remote <remote-snapshot-name> --name live-copy --yes
```

This imports the remote snapshot into the current project's local snapshots, restores it, creates the normal pre-restore safety snapshot first, and keeps the imported local snapshot for audit/reuse.

## Remote Snapshot Cleanup

```sh
nf site snapshot remove <name> [--yes]
nf site snapshot prune [--keep N] [--dry-run] [--yes]
```

Use dry-run before pruning when possible.

## Import a Handoff Into Local Development

```sh
nf env import <source> [--db path] [--source-url url] [--name name] [--dry-run] [--yes]
```

`nf env import` imports into the current project's local env only. It accepts an `nf site export` directory, or a generic WordPress filesystem directory when paired with `--db`.

The import creates an import snapshot, creates the normal pre-restore safety snapshot, restores the database plus `wp-content/uploads`, `plugins`, and `languages`, runs URL search-replace when a source URL is known, activates the configured local theme when installed, regenerates WordPress rewrite rules with `wp rewrite flush`, and then flushes object cache as a separate step. Local snapshot restores use the same finalization sequence.

It does not import WordPress core, `wp-config.php`, or target-owned `wp-content/mu-plugins` into the local env.

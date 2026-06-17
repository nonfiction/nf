# Plugins

Configured WordPress plugins live in `nf.json` under `wordpress.plugins`. The list is an env bootstrap checklist, not a full plugin lifecycle manager.

## Configure Plugins

```json
{
  "wordpress": {
    "plugins": [
      "stream",
      "wp-crontrol",
      "insert-headers-and-footers",
      "block-visibility",
      "imsanity",
      {
        "slug": "acf-pro",
        "source": "cache",
        "activate": true,
        "auto_update": false
      },
      {
        "slug": "client-plugin",
        "source": "repo"
      },
      {
        "slug": "sitepress-multilingual-cms",
        "install": false,
        "note": "WPML; install manually from wpml.org account"
      }
    ]
  }
}
```

String entries install from wordpress.org, activate, and enable auto-updates by default. Object entries require `slug`, may set `source` to a zip URL/path, env var, `repo`, or `cache`, may set `activate` or `auto_update` to `false`, and may set `install` to `false` for manual/documentation-only plugins that nf should check but never install.

Use `source: "repo"` for project-specific plugins stored at `plugins/<slug>/` in the repo. Local envs bind mount configured repo plugins into `/var/www/html/wp-content/plugins/<slug>` for live development, then `nf plugin install` activates them and configures auto-updates as requested. Remote installs package repo plugins into temporary zips, upload them through WP-CLI, and clean up. No plugin artifact is written to `dist/` or committed to the repo.

Use `source: "cache"` for paid/private plugins whose installable zip is kept in nf's local plugin cache under `$NF_DATA_HOME/plugins/<slug>/<slug>.zip`. This is explicit; nf does not silently fall back from wordpress.org to the cache.

Keep private plugin URLs and license data in environment variables, not `nf.json`.

## Edit the Plugin List

```sh
nf plugin list
nf plugin add stream
nf plugin add acf-pro --source '$NF_PLUGIN_ACF_PRO_ZIP'
nf plugin add acf-pro --source cache --no-auto-update
nf plugin add client-plugin --source repo
nf plugin add sitepress-multilingual-cms --manual --note 'WPML; install manually from wpml.org account'
nf plugin remove stream
```

`nf plugin add <plugin>` appends a plugin to `nf.json` without installing it. Add `--source <source>`, `--manual`, `--note <note>`, `--no-activate`, or `--no-auto-update` when defaults are not enough. With `--source repo`, nf also creates a minimal `plugins/<slug>/<slug>.php` plugin scaffold when that plugin directory does not exist yet.

`nf plugin remove <plugin>` removes a plugin from `nf.json` without uninstalling it.

## Manage the Local Plugin Cache

```sh
nf plugin cache add acf-pro ~/Downloads/acf-pro.zip
nf plugin cache save sitepress-multilingual-cms
nf plugin cache list
nf plugin cache show acf-pro
```

`nf plugin cache add <plugin> <zip>` copies an existing zip into `$NF_DATA_HOME/plugins/<slug>/<slug>.zip`.

`nf plugin cache save <plugin>` archives the plugin currently installed in the local WordPress env and stores it as the cached zip. Use this as a local recovery aid for paid/manual plugins that were installed through WordPress admin or vendor updaters.

`nf plugin cache list` and `nf plugin cache show <plugin>` inspect the local cache. Cached zips are local machine state, not project metadata, and are not committed.

## Compare Config to WordPress State

Local env:

```sh
nf plugin status
nf plugin diff
```

Configured remote:

```sh
nf plugin status production
nf plugin diff production
```

`nf plugin status [remote]` compares `nf.json` against the local env or configured remote and reports whether each configured plugin is installed, active, and auto-update enabled.

`nf plugin diff [remote]` reports the install/activate/auto-update changes needed to make the local env or remote match `nf.json`. Manual plugins with `install: false` are still checked and report `manual install required` when missing. It also reports installed plugins that are not configured in `nf.json`. It does not mutate anything. It exits `0` when configured plugins match and no extras are installed, and `2` when drift exists.

## Install Configured Plugins

Local env:

```sh
nf plugin install
```

Remote dry-run:

```sh
nf plugin install production --dry-run
```

Remote install:

```sh
nf plugin install production --yes
```

Local installs use configured repo plugin bind mounts and cached zips when requested. Remote installs run WP-CLI on the remote host. URL sources must be reachable from that host; local zip, cache, and repo sources are uploaded to a temporary remote directory before install and cleaned up afterward. Repo sources are zipped locally on demand before upload.

Plugin install is idempotent: it installs only missing configured plugins where `install` is true, activates only inactive plugins when `activate` is true, and enables native WordPress auto-updates only when not already enabled. It does not install manual plugins, update, remove, pin, disable auto-updates, or manage plugin licenses.

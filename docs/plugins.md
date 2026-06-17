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
        "source": "$NF_PLUGIN_ACF_PRO_ZIP",
        "activate": true,
        "auto_update": true
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

String entries install from wordpress.org, activate, and enable auto-updates by default. Object entries require `slug`, may set `source` to a zip URL/path, env var, or `repo`, may set `activate` or `auto_update` to `false`, and may set `install` to `false` for manual/documentation-only plugins that nf should check but never install.

Use `source: "repo"` for project-specific plugins stored at `plugins/<slug>/` in the repo. During install, nf packages that directory into a temporary zip with `<slug>/` as the archive root, installs or uploads it through WP-CLI, and removes the temporary zip. No plugin artifact is written to `dist/` or committed to the repo.

Keep private plugin URLs and license data in environment variables, not `nf.json`.

## Edit the Plugin List

```sh
nf env plugin list
nf env plugin add stream
nf env plugin add acf-pro --source '$NF_PLUGIN_ACF_PRO_ZIP'
nf env plugin add client-plugin --source repo
nf env plugin add sitepress-multilingual-cms --manual --note 'WPML; install manually from wpml.org account'
nf env plugin remove stream
```

`nf env plugin add <plugin>` appends a plugin to `nf.json` without installing it. Add `--source <source>`, `--manual`, `--note <note>`, `--no-activate`, or `--no-auto-update` when defaults are not enough.

`nf env plugin remove <plugin>` removes a plugin from `nf.json` without uninstalling it.

## Compare Config to WordPress State

Local env:

```sh
nf env plugin status
nf env plugin diff
```

Configured remote:

```sh
nf env plugin status production
nf env plugin diff production
```

`nf env plugin status [remote]` compares `nf.json` against the local env or configured remote and reports whether each configured plugin is installed, active, and auto-update enabled.

`nf env plugin diff [remote]` reports the install/activate/auto-update changes needed to make the local env or remote match `nf.json`. Manual plugins with `install: false` are still checked and report `manual install required` when missing. It also reports installed plugins that are not configured in `nf.json`. It does not mutate anything. It exits `0` when configured plugins match and no extras are installed, and `2` when drift exists.

## Install Configured Plugins

Local env:

```sh
nf env plugin install
```

Remote dry-run:

```sh
nf env plugin install production --dry-run
```

Remote install:

```sh
nf env plugin install production --yes
```

Remote installs run WP-CLI on the remote host. URL sources must be reachable from that host; local zip sources are uploaded to a temporary remote directory before install and cleaned up afterward. Repo sources are zipped locally on demand, uploaded like local zip sources, and cleaned up locally and remotely after install.

Plugin install is idempotent: it installs only missing configured plugins where `install` is true, activates only inactive plugins when `activate` is true, and enables native WordPress auto-updates only when not already enabled. It does not install manual plugins, update, remove, pin, disable auto-updates, or manage plugin licenses.

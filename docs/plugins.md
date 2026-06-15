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
      }
    ]
  }
}
```

String entries install from wordpress.org, activate, and enable auto-updates by default. Object entries require `slug`, may set `source` to a zip URL/path or env var, and may set `activate` or `auto_update` to `false`.

Keep private plugin URLs and license data in environment variables, not `nf.json`.

## Edit the Plugin List

```sh
nf env plugins list
nf env plugins add stream
nf env plugins add acf-pro --source '$NF_PLUGIN_ACF_PRO_ZIP'
nf env plugins remove stream
```

`nf env plugins add <plugin>` appends a plugin to `nf.json` without installing it. Add `--source <source>`, `--no-activate`, or `--no-auto-update` when defaults are not enough.

`nf env plugins remove <plugin>` removes a plugin from `nf.json` without uninstalling it.

## Compare Config to WordPress State

Local env:

```sh
nf env plugins status
nf env plugins diff
```

Configured remote:

```sh
nf env plugins status production
nf env plugins diff production
```

`nf env plugins status [remote]` compares `nf.json` against the local env or configured remote and reports whether each configured plugin is installed, active, and auto-update enabled.

`nf env plugins diff [remote]` reports the install/activate/auto-update changes needed to make the local env or remote match `nf.json`. It also reports installed plugins that are not configured in `nf.json`. It does not mutate anything. It exits `0` when configured plugins match and no extras are installed, and `2` when drift exists.

## Install Configured Plugins

Local env:

```sh
nf env plugins install
```

Remote dry-run:

```sh
nf env plugins install production --dry-run
```

Remote install:

```sh
nf env plugins install production --yes
```

Remote installs run WP-CLI on the remote host. URL sources must be reachable from that host; local zip sources are uploaded to a temporary remote directory before install and cleaned up afterward.

Plugin install is idempotent: it installs only missing configured plugins, activates only inactive plugins when `activate` is true, and enables native WordPress auto-updates only when not already enabled. It does not update, remove, pin, disable auto-updates, or manage plugin licenses.

# Themes

Theme commands run from an `nf` project repo with `nf.json` next to `.git`.

Configured WordPress themes live in `nf.json` under `wordpress.themes`. The list is an env bootstrap checklist, must include at least one theme, and the first theme listed is the theme nf activates.

## Configure Themes

```json
{
  "wordpress": {
    "themes": [
      {
        "slug": "client",
        "source": "repo",
        "path": "theme",
        "package": {
          "output": "dist/client-v{version}.zip"
        },
        "tasks": {
          "build": "npm run build",
          "lint": ["npm", "run", "lint"],
          "dev": {
            "description": "Start the theme development server",
            "run": ["npm", "run", "dev"]
          }
        }
      },
      "twentytwentyfive",
      "twentytwentyfour",
      "twentytwentythree",
      {
        "slug": "paid-parent-theme",
        "source": "cache",
        "auto_update": false,
        "note": "Vendor zip is cached locally"
      }
    ]
  }
}
```

String entries install from wordpress.org and default `auto_update` to `false`. Object entries require `slug`, may set `source` to `wordpress.org`, `cache`, `repo`, a zip URL/path, or an env-var-backed source, and may set `auto_update` for non-repo themes.

Use `source: "repo"` for the one theme directory stored in the project repo. A repo theme defaults to `path: "theme"`, is bind mounted into the local env as `/var/www/html/wp-content/themes/<slug>`, and is the theme packaged by `nf theme package` and `nf theme deploy`. A project may have zero repo themes when the active theme comes from wordpress.org, cache, or another install source. Only one repo theme is allowed.

Use `source: "cache"` for paid/private themes whose installable zip is kept in nf's local theme cache under `$NF_DATA_HOME/themes/<slug>/<slug>.zip`. This is explicit; nf does not silently fall back from wordpress.org to the cache.

For a parent theme plus custom child theme, put the child repo theme first so it is active, then list the parent theme after it.

## Edit the Theme List

```sh
nf theme list
nf theme add twentytwentyfive
nf theme add paid-parent-theme --source cache
nf theme add client --source repo
nf theme activate client
nf theme remove twentytwentyfive
```

`nf theme add <theme>` appends a theme to `nf.json` without installing it. Add `--source <source>`, `--path <path>`, `--auto-update`, or `--note <note>` when defaults are not enough. With `--source repo`, `--path` defaults to `theme` and the directory must already exist.

`nf theme activate <theme>` moves an existing configured theme to the top of `wordpress.themes`. It updates desired state only; run `nf theme install`, `nf env up`, or a later deploy/sync path to apply the active theme in WordPress.

`nf theme remove <theme>` removes a theme from `nf.json` without uninstalling it. It refuses to remove the last configured theme.

## Manage the Local Theme Cache

```sh
nf theme cache add paid-parent-theme ~/Downloads/paid-parent-theme.zip
nf theme cache save paid-parent-theme
nf theme cache list
nf theme cache show paid-parent-theme
nf theme cache remove paid-parent-theme
```

`nf theme cache add <theme> <zip>` copies an existing zip into `$NF_DATA_HOME/themes/<slug>/<slug>.zip`.

`nf theme cache save <theme>` archives the theme currently installed in the local WordPress env and stores it as the cached zip. Use this as a local recovery aid for paid/manual themes that were installed through WordPress admin or vendor updaters.

`nf theme cache list` and `nf theme cache show <theme>` inspect the local cache. `nf theme cache remove <theme>` deletes one cached theme zip. Cached zips are local machine state, not project metadata, and are not committed.

## Compare Config to WordPress State

Local env:

```sh
nf theme status
nf theme diff
```

Configured remote:

```sh
nf theme status production
nf theme diff production
```

`nf theme status [remote]` compares `nf.json` against the local env or configured remote and reports whether each configured theme is installed, active, and auto-update enabled.

`nf theme diff [remote]` reports the install/activate/auto-update changes needed to make the local env or remote match `nf.json`, including installed themes that are not configured. It does not mutate anything. It exits `0` when configured themes match and no extras are installed, and `2` when drift exists.

## Install Configured Themes

Local env:

```sh
nf theme install
```

Remote dry-run:

```sh
nf theme install production --dry-run
```

Remote install:

```sh
nf theme install production --yes
```

Local installs use the configured repo theme bind mount and cached zips when requested. Remote installs run WP-CLI on the remote host. URL sources must be reachable from that host; local zip, cache, and repo sources are uploaded to a temporary remote directory before install and cleaned up afterward.

Theme install is idempotent: it installs only missing configured themes, activates the first listed theme when inactive, and enables native WordPress auto-updates for non-repo themes only when requested. When installation actually changes the active theme, `nf` runs `wp rewrite flush` afterward. It does not update, remove, pin, disable auto-updates, or manage licenses.

## Run Theme Tasks

```sh
nf theme tasks
nf theme build
nf theme <task> -- <args>
```

`nf theme tasks` lists the tasks on the configured `source: "repo"` theme.

Each task may be a shell string, an argv array, or an object with an optional `description` and a required `run` value in either form. String tasks run through `sh -lc` from the project root. Array tasks execute directly. The underlying command is printed before execution.

## Package a Release

```sh
nf theme package [--dry-run] [--source path] [--output path]
```

`nf theme package` creates a clean staged release artifact instead of zipping the development checkout as-is. It copies runtime theme files to a temporary staging directory, excludes obvious local development files, and when `composer.json` is present runs this in the staging directory:

```sh
composer install --no-dev --no-interaction --prefer-dist --optimize-autoloader --no-progress
```

This preserves the working tree's `theme/vendor/` while ensuring the artifact is ready to upload/install/activate. It includes `vendor/autoload.php` and runtime Composer packages from `require`, but excludes `require-dev` packages and dev-only Composer tooling binaries such as PHP-CS-Fixer, PHPCS, and PHPCBF.

Packaging does not run npm or asset builds. Run the right theme task before packaging:

```sh
nf theme build
nf theme package
```

If `package.json` has a `build` script, packaging requires built files under `dist/` or `assets/dist/` and fails clearly when they are missing. Development-only files such as `node_modules`, editor config, PHP-CS-Fixer/PHPCS/PHPStan/Psalm config, npm manifests and lockfiles, and common frontend tooling config are excluded from the artifact.

The zip root is the configured repo theme slug, not necessarily the local repo theme path basename. Set the repo theme's optional `package.output` to override the output path. When omitted, the output defaults to `dist/<project-slug>-v{version}.zip`.

If `package.output` contains `{version}`, `nf` resolves it from:

1. the repo theme `style.css` `Version:`
2. the repo theme `package.json` `version`

## Deploy a Theme Release

Configure a repo remote first. See [Remotes](remotes.md).

Preview the deployment:

```sh
nf theme deploy production --dry-run
```

Deploy:

```sh
nf theme deploy production
```

`nf theme deploy <remote>` builds the same repo theme artifact as `nf theme package`, installs any configured non-repo themes first, uploads the repo theme release to the selected remote env, extracts it under `wp-content/themes/.nf-releases/<repo-theme-slug>/`, copies the release into `wp-content/themes/<repo-theme-slug>/`, activates the first configured theme with WP-CLI, and records release metadata. After the release switch succeeds, it runs `wp rewrite flush`; a flush failure makes the deploy command fail without undoing the recorded release. Kinsta deploys then clear the site cache through the provider API. Pass `--restart` to restart PHP before that cache clear when changed PHP code must invalidate PHP-FPM bytecode. Kinsta maintenance requires the cached environment ID from `nf site refresh` and `KINSTA_API_KEY`.

Theme deploy keeps the last 5 releases and matching uploaded zips, so release storage does not grow indefinitely. It does not require manual WordPress admin zip upload and supersedes direct in-place source rsync deploys.

## Roll Back a Theme Release

Preview rollback:

```sh
nf theme rollback production --dry-run
```

Roll back:

```sh
nf theme rollback production
```

`nf theme rollback <remote>` switches the repo theme directory back to the previous recorded release, activates the first configured theme again, and then runs `wp rewrite flush`. Kinsta rollbacks also restart PHP and clear the site cache before reporting success. Rollback uses remote `releases.json`; it does not rebuild or upload artifacts.

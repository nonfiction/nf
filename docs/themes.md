# Themes

Theme commands run from an `nf` project repo with `nf.json` next to `.git`.

## Run Theme Tasks

```sh
nf theme tasks
nf theme build
nf theme <task> -- <args>
```

`nf theme tasks` lists project tasks from `nf.json`.

String tasks run through `sh -lc` from the project root. Array tasks execute directly. The underlying command is printed before execution.

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

The zip root remains `wordpress.theme_slug`, not necessarily the local `wordpress.theme_path` basename.

If `artifact.path` contains `{version}`, `nf` resolves it from:

1. `theme/style.css` `Version:`
2. `theme/package.json` `version`

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

`nf theme deploy <remote>` builds the same theme artifact as `nf theme package`, uploads it to the selected remote env, extracts it under `wp-content/themes/.nf-releases/<theme-slug>/`, copies the release into the active theme directory, activates the configured theme slug with wp-cli, and records release metadata.

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

`nf theme rollback <remote>` switches the active theme directory back to the previous recorded release and activates the configured theme slug again. It uses remote `releases.json`; it does not rebuild or upload artifacts.

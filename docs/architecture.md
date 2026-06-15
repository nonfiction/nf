# Architecture

## Project Shape

`nf` is a Go CLI.

* Executable entrypoint: `cmd/nf/main.go`
* CLI dispatcher: `internal/cli.Run`
* Primary always-visible command groups: `init`, `provider`, `target`, `site`, `config`, `password`
* Project-only command groups: `remote`, `theme`, `env`, `public`

Project-only commands appear only when the current repo has `nf.json` next to `.git`.

## Project Metadata Model

Project repositories use:

```text
nf.json
```

This file is safe to commit. It must not contain API tokens, SSH keys, live database passwords, provider secrets, or mutable provider inventory.

Generated project metadata includes `project.password_version: 0`. Leave it at `0` for stable derived passwords; increment it to rotate derived passwords for that project/site without changing the shared `NF_PASSWORD_SALT`.

Default WordPress theme convention is `theme/`. By default, `nf init` derives the project slug from the current git root folder and assumes the WordPress theme lives in `theme/`.

## Config, State, and Data Layout

Defaults:

```text
config: ~/.config/nf/
state:  ~/.local/state/nf/
data:   ~/.local/share/nf/
```

Current config files:

```text
config.json     non-secret global config, including base_domain, dnsimple_account_id, basicauth_default_user, and adminer_default_user
.env            secrets/account tokens
providers.json  provider check metadata and targets
sites.json      cached remote site/env records
projects.json   disposable project cache if needed
```

State/cache lives under:

```text
~/.local/state/nf/
  providers.json
  sites.json
  projects.json
```

Local state is disposable cache, not source of truth. Provider truth is canonical remotely.

## Provider, Target, and Site Model

* DNSimple provider check validates it can read the configured `base_domain` zone and writes zero targets.
* Kinsta provider check writes one target named `kinsta`.
* Linode provider check discovers targets from Linode instances tagged `nf`.
* `nf target list/show` read targets from `providers.json`; legacy `servers.json` fallback may remain during cache migration.
* Remote target site discovery is not implemented yet.
* Linode-hosted site/env truth is intended to live on each target at `/var/lib/nf/sites.json`, read over SSH as the standard user.
* Linode target metadata lives at `/var/lib/nf/target.json`. It includes Adminer/AdminNeo metadata but no raw Adminer password.

## Password and Basic Auth Model

Password derivation uses `NF_PASSWORD_SALT` from the environment or `~/.config/nf/.env`. Legacy `NF_SECRET_SALT` is accepted only as a migration fallback.

Project site passwords, including provider basic-auth passwords, also include `project.password_version` from `nf.json` when it is non-zero; missing or `0` preserves the original derivation.

`basicauth_default_user` belongs in `config.json`, defaults to `nonfiction`, and is used with a per-site derived `basic-auth` password.

`adminer_default_user` belongs in `config.json`, defaults to `adminer`, and is used for Linode target Adminer HTTP Basic auth, the shared MySQL admin user, and the Adminer subdomain label unless `nf target add linode --adminer-user` overrides it for that target.

## Safety Boundaries

Treat these as high risk:

* production database push
* uploads push to production
* full site sync toward production
* workflows that can overwrite live credentials or uploads
* workflows that destroy remote infrastructure

Sync and deploy workflows must require explicit source and destination, identify provider/environment, print a reviewable plan, preserve production credentials where possible, and require confirmation for destructive changes.

Key rule: never silently clobber production credentials.

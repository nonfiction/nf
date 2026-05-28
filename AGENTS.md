# nf agent guide

## Shape and entrypoints

- Go CLI only: `cmd/nf/main.go` calls `internal/cli.Run`; there is no supported
  Python implementation left.
- Main command groups are `nf server ...`, `nf site ...`, `nf repo ...`,
  `nf config ...`, and `nf password ...`; do not add old compatibility routes
  such as `provision-server`, top-level `list/show`, or top-level repo aliases.
- Current repo command surface is intentionally small: `nf repo init`,
  `commands`, `package`, built-in workbench commands `up`, `down`, `logs`,
  `reset`, `wp`, and direct repo-local aliases from metadata. Do not re-add
  public `repo run`, `setup`, `fresh`, `restart`, `install-theme`, or
  `activate-theme` routes unless explicitly requested.
- UI prompts/selectors live in `internal/ui` and use Bubble Tea/Bubbles/Lip
  Gloss. Interactive commands should prefer selectors over required positional
  args when the choice can be inferred from state.
- `internal/provision` still provisions a first Linode-focused WordPress host
  slice; future deploy/sync/site install work should stay policy-gated.

## Commands worth using

- Fast checks: `go test ./...`; focused package test: `go test ./internal/cli`.
- CLI smoke: `go run ./cmd/nf --help` and grouped commands such as
  `go run ./cmd/nf server list`.
- Nix smoke: `nix run .#nf -- --help`; build: `nix build .#nf -L`.
- Dev shell: `nix develop -c nf --help`. `.envrc` watches `flake.nix`,
  `go.mod`, and `go.sum`, then `use flake`.
- Flake builds use the git source snapshot. Stage newly added files before
  trusting `nix run .#nf`/`nix build .#nf`; otherwise Nix may silently build
  without untracked Go files.

## Config, state, and secrets

- Local secrets/config are read from `~/.config/nf/.env` (or `NF_CONFIG_HOME` in
  tests). `nf config init` can populate missing values interactively.
- Shared state lives under `~/.config/nf/state` as JSON files such as
  `servers.json`, `sites.json`, and `projects.json`.
- Local WordPress workbench runtime lives under
  `~/.config/nf/workbenches/<project-slug>/` or the equivalent `NF_CONFIG_HOME`
  test path. It is generated/owned by `nf`; do not scaffold Docker workbench
  files into project repos by default.
- Repo-local metadata is `.nf/project.json`; it is safe intent/config only. Do
  not put API tokens, SSH keys, DB credentials, live passwords, or mutable
  server/site state there.
- `NF_SECRET_SALT` is required for password derivation; Linode execution uses
  `LINODE_CLI_TOKEN` or `LINODE_TOKEN`; DNS work uses `DNSIMPLE_TOKEN` and
  optional `DNSIMPLE_ACCOUNT_ID`.

## Repo-context behavior

- `nf repo ...` commands are the only local project command surface. Repo-local
  aliases come from `.nf/project.json` `commands` and execute from the project
  root.
- `nf repo init` defaults `project.slug` from the current git root folder. The
  WordPress theme directory convention is `theme/`; generated metadata should
  default `wordpress.theme_path`, `wordpress.theme_slug`, and
  `workbench.theme_mount_slug` to `theme` unless an explicit override is given.
- String commands run through `sh -lc`; argv-list commands execute directly;
  passthrough args follow `--`. Command execution should print the underlying
  command preview before running it.
- Repo-context commands are hidden/rejected outside a `.git` repo. Keep that
  distinction when adding local workflow commands.
- `nf repo package` only zips existing theme files; it does not run Composer,
  npm, or asset builds first. Deploy artifacts must include built `vendor/` and
  `assets/dist/` when present.
- `artifact.path` may contain `{version}`. Resolve it from `theme/style.css`
  `Version:` first, then `theme/package.json`; fail clearly if neither exists.

## Current roadmap

- First priority: keep the Sanjel-style local repo workflow good enough to
  replace per-project Makefiles/scripts with `.nf/project.json` plus
  `nf repo ...` commands.
- Next: implement Linode site lifecycle after server provisioning: install a
  site on an existing `app1` host, write normalized site state, and deploy the
  packaged theme artifact.
- Then: implement database/uploads pull-push workflows with production password
  and sensitive-option protections.
- Later: add Kinsta deploy/sync adapters from Kinsta IDs in site state. Kinsta
  must not use Linode provisioning paths or require SSH server fields.
- Keep flake/team distribution and private state-sync polish aligned with the
  README as the command surface stabilizes.

## Safety and provider rules

- Linode is the only implemented remote provider; DNSimple supports `nfweb.dev`
  DNS/TLS. Kinsta is future work and should not use Linode provisioning paths.
- `nf server provision` is dry-run by default. Actual remote execution requires
  `--execute --yes` in non-interactive mode plus credentials.
- `nf server delete` is interactive by default with a picker + confirm; in
  non-interactive mode it remains dry-run unless `--execute --yes` is supplied.
  Linode 404/not-found means “already deleted remotely” and should still clean
  stale local state.
- Treat DB/uploads sync and production DB push as high risk; preserve production
  passwords and sensitive options.

## Hygiene

- Keep README and this file aligned when command names, safety posture, or state
  layout changes.
- Do not touch sibling client repos such as `sanjel` or `siafintech` unless the
  user explicitly asks.
- Do not commit secrets or generated caches (`.direnv`, build outputs,
  leftover `__pycache__`).

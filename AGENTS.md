# nf agent guide

## Current shape

- `nf` is a Python 3.11+ CLI (`nf/`, entrypoint `nf.cli:main`) packaged by
  `pyproject.toml` and `flake.nix`.
- The implemented slice is local-only: shared state inspection, `.nf/project.json`
  init, local project command running, password derivation, and local theme zip
  packaging.
- Remote provisioning now has a guarded first slice: `nf provision-server`
  defaults to dry-run. Interactive use is gum-first; flags are shortcuts, and
  `--non-interactive` exists for scripts/tests. Actual remote execution is
  Linode-only for now and requires both `--execute` and `--yes`, plus the
  required env credentials. Keep deploy, sync, and destructive workflows
  policy-gated.

## Commands that work now

- Try the CLI: `nix run .#nf -- --help`.
- Dev shell: `nix develop -c nf --help`; inside `nix develop`, `nf` is on PATH.
- Direnv: `.envrc` runs `watch_file flake.nix`, `watch_file pyproject.toml`,
  then `use flake`; user must run `direnv allow` locally.
- Focused checks used so far:
  - `python -m nf --help`
  - `python -m nf list servers`
  - `NF_SECRET_SALT=test-salt python -m nf password derive demo db`
  - `nix flake check`
  - `nix run .#nf -- --help`
  - `nix develop -c nf --help`
  - `python -m nf provision-server --help`
  - `python -m nf provision-server --non-interactive --project-slug demo --site-domain demo.ln.nfweb.dev --write-cloud-init /tmp/opencode/nf-provision-gum-preview.yaml`

## State and config boundaries

- Local secrets: `~/.config/nf/.env`; current salt name is only
  `NF_SECRET_SALT`.
- Shared machine-readable state: `~/.config/nf/state`, loaded from
  `servers.json`, `sites.json`, and `projects.json` when present.
- Project-safe metadata: `.nf/project.json` inside client repos. It may contain
  project identity, layout hints, deploy intent, and a local `commands` registry.
- Never store API tokens, SSH keys, DB credentials, live server passwords, or
  mutable server/site state in `.nf/project.json`.

## Project context behavior

- Project context is discovered by walking upward for `.nf/project.json`.
- `nf project init` assumes the Sanjel project shape: `theme/` for the theme and
  `workbench/` for Docker Compose local WordPress.
- Project command aliases (`nf build`, `nf up`, `nf wp`, etc.) come from
  `.nf/project.json` `commands`; they are direct `nf`-owned workflows, not
  Makefile wrappers. The long-term goal is to remove per-project Makefiles.
- String commands execute from the project root through `sh -lc` and receive
  passthrough args after `--`; argv-list commands execute directly.
- Local command aliases can mutate local files or Docker containers when invoked;
  they are still not remote workflows.

## WordPress deployment design constraints

- Theme deploy artifacts must include the built theme, especially `vendor/` and
  `assets/dist/` when present.
- `nf theme package` only zips existing files; it does not run Composer, npm, or
  build steps first.
- Future direct deploy and versioned zip flows should use the same artifact
  posture.
- Future DB/uploads sync must preserve production passwords and sensitive options;
  treat production DB push as high risk.

## Provider model to preserve

- Linode is first provider; DNSimple handles `nfweb.dev` DNS/TLS support.
- Kinsta is a future provider and has no Linode provisioning step.
- Keep provider-specific behavior behind shared command contracts and policy
  checks.

## Change discipline

- Keep docs and `AGENTS.md` aligned when command scope or safety posture changes.
- Do not touch sibling client repos such as `sanjel` or `siafintech` unless the
  user explicitly asks.
- Do not commit secrets or generated caches (`__pycache__`, `.direnv`, build
  outputs).

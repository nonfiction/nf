# nf agent guide

## Purpose

This repository is the planning home for `nf`, an agency-level CLI for
nonfiction WordPress infrastructure, deployment, and state management.

It is not a project-specific script repo.
It is not yet an executable CLI implementation repo.

The current goal is to design the command set, state model, provider
abstractions, safety rules, and WordPress deployment workflows before any code
is written.

The CLI should also be designed for eventual `flake.nix` packaging in this repo
so the nonfiction team can consume it from WordPress project flakes.

## Current status

- Planning only.
- No executable CLI code should be added here yet.
- Docs should stay internally consistent and reflect the current design
  decisions.
- Future implementation work should follow the architecture described in
  `README.md` unless a later decision explicitly replaces it.

## Flake packaging rules

- `nf` should eventually be created and distributed through a `flake.nix` in
  this repository.
- The flake should expose `packages.${system}.default` containing the `nf`
  executable.
- The flake may also expose `devShells.default` for `nf` development.
- WordPress project repositories should consume `nf` as a flake input in their
  own `flake.nix` files.
- Do not vendor or copy `nf` scripts into project repositories as the normal
  distribution path.
- Team usage may come from a project dev shell input, a direct checkout of this
  repo for local development, or an optional Nix profile install.
- This packaging and distribution path is distinct from the private `nf` state
  repo and from `~/.config/nf/.env`, which remain for shared state and local
  secrets respectively.

## What this repo should contain

- Strategy and design docs.
- Future implementation notes.
- Agent instructions for later coding work.

## What this repo should not contain yet

- CLI entrypoints.
- Build scripts.
- Provisioning scripts.
- Provider API implementations.
- Secrets.
- Project-specific deployment logic.

## Core design rules

1. The CLI name is `nf`.
2. `nf` is an agency-level tool, not a per-project script collection.
3. Shared config/state lives under `~/.config/nf`.
4. Long-lived shared state should be synced through a private GitHub repo.
5. Secrets live locally in `~/.config/nf/.env` and are manually copied from
   1Password or an equivalent secure source.
6. Do not commit secrets, tokens, passwords, or private keys.
7. Treat server state, site state, and project metadata as separate concerns.
8. Keep provider behavior abstract so Linode and future providers can share the
   same command surface.
9. Do not destroy or overwrite remote state unless the command and target are
   explicitly confirmed and the safety rules in `README.md` are satisfied.
10. Package and distribute `nf` through `flake.nix`, and consume it from
    WordPress project flakes instead of vendoring scripts.

## Repository hygiene

- Prefer Markdown documentation updates over speculative implementation.
- Keep examples realistic and aligned with the client and nonfiction context.
- Preserve the distinction between project repositories and shared `nf` state.
- Avoid adding hidden behavior or undocumented assumptions.

## Safety rules for future implementation work

- Never print secrets in logs or docs.
- Never store secrets in project repositories.
- Never assume a destructive remote action is safe because it is a staging
  environment.
- Require explicit confirmation before destructive actions such as server
  removal, site removal, DB overwrite, or uploads overwrite.
- Preserve production passwords and sensitive options when performing database
  push/pull flows unless a provider-specific policy explicitly allows a change.
- Treat `push db` to production as high risk.
- Prefer reversible workflows and clear prompts over implicit automation.

## State model rules

- Shared machine-readable state belongs in the `nf` state repo or state checkout
  under `~/.config/nf/state`.
- Project repositories should only contain safe metadata, such as
  `.nf/project.json`.
- Do not store live server passwords, API tokens, or production secrets in the
  project repository.
- Server and site records must remain separate even when a server hosts only
  one site today.

## WordPress deployment rules

- The deploy artifact for themes is the fully built theme, including `vendor/`
  and `assets/dist/`.
- Direct deploy and versioned `theme.zip` must use the same artifact posture.
- Deploy logic should be provider-adapter based, not hardcoded to Linode.
- Theme deploy should be local-build-first, then sync the built artifact.
- Future DB and uploads workflows must preserve the current production layout
  and avoid accidental clobbering.

## Provider abstraction rules

- Linode is the first provider.
- DNSimple handles DNS hostnames and DNS/TLS challenge support for `nfweb.dev`.
- Kinsta must be considered a future provider with no Linode provisioning.
- Provider-specific provisioning, deploy, push, and pull behavior should live
  behind shared command contracts.
- Use provider policies to control what is allowed for server creation, site
  install, DB sync, and uploads sync.

## Testing and validation expectations for future code

- Add narrow tests for parsing, state handling, and provider policy logic.
- Validate any command behavior with focused tests before broad integration
  checks.
- Avoid broad destructive tests against live infrastructure.
- Prefer dry-run style checks where possible.
- If a change affects docs only, verify consistency by reading the diff and the
  rendered Markdown structure.

## Working style for future agents

- Inspect existing docs before editing.
- Keep changes small and bounded.
- Preserve existing terminology once introduced.
- If a design decision changes, update all docs that depend on it.
- When uncertain about infrastructure safety, stop and ask rather than guess.

## Explicitly excluded actions unless requested

- Initializing git.
- Creating executable files.
- Building a CLI.
- Writing provider integrations.
- Touching unrelated nonfiction repos.

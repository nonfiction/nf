# nf Documentation

`nf` is nonfiction's internal CLI for agency WordPress theme work. It manages project metadata, local WordPress dev environments, theme tasks and packaging, provider inventory, repo remotes, password derivation, and guarded future deploy/sync workflows.

This documentation expands on the concise repo [README](../README.md). For the full project model, state layout, implementation phases, and roadmap, see [`SPEC.md`](../SPEC.md).

## Start Here

* [Usage](usage.md): workflow map for common agency work.
* [Installation](installation.md): install, run, build, shell completion, and releases.
* [Configuration](configuration.md): global config, secrets, provider tokens, password derivation, and test isolation paths.

## Practical Guides

* [Local Development](local-development.md): spin up a local WordPress env from a repo.
* [Targets](targets.md): add and inspect deployable targets.
* [Sites](sites.md): add remote WordPress sites and inspect envs.
* [Staging](staging.md): create, inspect, and remove staging envs.
* [Remotes](remotes.md): connect a repo to a cached remote env.
* [Themes](themes.md): run theme tasks, package releases, deploy, and rollback.
* [Public Artifacts](public-artifacts.md): deploy configured static public paths.
* [Plugins](plugins.md): configure, compare, and install WordPress plugins.
* [Snapshots](snapshots.md): create, import, restore, prune, and remove env snapshots.
* [Sync](sync.md): push or pull database and mutable `wp-content` with reviewable preflights.
* [Domains and Launch](domains.md): manage public domains and launch a primary domain.

## Reference

* [Commands](commands.md): command groups, provider/target/site/domain/remotes reference, and current behavior notes.
* [Deployment](deployment.md): deployment workflow overview and links to detailed guides.
* [Development](development.md): development commands, tests, Nix checks, release builds, and contributor caveats.
* [Architecture](architecture.md): repository shape, state/cache layout, project model, provider model, and safety boundaries.

## First Steps

```sh
nix profile add github:nonfiction/nf
nf config init
nf password set-salt <shared-salt>
nf init
nf env up
```

After `nf init`, run project-local commands from that repo so `theme`, `env`, `remote`, and `public` are available.

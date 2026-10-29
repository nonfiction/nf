# nf

<img width="150" align="right" src="docs/nf.svg">

`nf` is nonfiction's internal CLI for agency website work. WordPress workflows are established. [Nterchange support](docs/nterchange.md) covers project metadata, SSH/Compose discovery, guarded SQL/media pulls, locked code packaging and per-site code activation/rollback. Activation has been tested only on a disposable synthetic PHP target, not an existing CMS site.

It manages local WordPress environments, theme and plugin work, provider inventory, repo remotes, password derivation, and guarded deployment and data sync.

This is an internal agency tool, not a general-purpose public WordPress framework.

## Quick Start

Install the latest `nf` from the GitHub flake into your Nix profile:

```sh
nix profile add github:nonfiction/nf
```

Configure global settings before creating or managing projects:

```sh
nf config init
nf password set-salt <shared-salt>
```

Create repo metadata, then run project-local commands from that repo:

```sh
nf init
nf env up
nf env show
nf theme tasks
nf theme package
```

## Common Commands

```sh
nf --help
nf version
nf provider list
nf site list --envs
nf env wp -- plugin list
nf theme deploy production --dry-run
nf alias status production
```

Long options that take values accept both `--flag value` and `--flag=value`.

## Documentation

Start with the [documentation index](docs/index.md) or the [usage guide](docs/usage.md).

Common practical guides:

- [Local development](docs/local-development.md)
- [Nterchange and SSH targets](docs/nterchange.md)
- [Targets](docs/targets.md)
- [Sites](docs/sites.md)
- [Staging](docs/staging.md)
- [Remotes](docs/remotes.md)
- [Themes](docs/themes.md)
- [Aliases](docs/aliases.md)
- [Snapshots](docs/snapshots.md)
- [Sync](docs/sync.md)
- [Domains and launch](docs/domains.md)

For the full project model, state layout, implementation phases, and roadmap, see [`SPEC.md`](SPEC.md).

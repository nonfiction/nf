# Development

## Run From a Checkout

```sh
nix run .#nf -- --help
nix develop -c nf --help
go run ./cmd/nf --help
```

## Build and Test

```sh
nix build .#nf -L
go test ./...
```

Nix flakes build from the git source snapshot. Stage newly added source files before trusting `nix run` or `nix build`.

## Smoke Checks

```sh
go run ./cmd/nf --help
go run ./cmd/nf version
go run ./cmd/nf provider list
go run ./cmd/nf site list
go run ./cmd/nf site list --envs
```

Project-context smoke checks need an `nf` project repo with `nf.json` next to `.git`:

```sh
go run ./cmd/nf theme help
go run ./cmd/nf remote help
go run ./cmd/nf env help
go run ./cmd/nf alias help
```

Provider checks call live APIs when credentials are present:

```sh
go run ./cmd/nf provider check dnsimple
go run ./cmd/nf provider check kinsta
go run ./cmd/nf provider check linode
```

## Releases

`nf` uses date-based versions in `YYYY.MM.DD.N` form. `N` increments for multiple releases on the same UTC date. The current source version lives in `internal/version/VERSION`.

Build release artifacts for the version in `internal/version/VERSION`:

```sh
scripts/release.sh
```

Build artifacts for an explicit version:

```sh
scripts/release.sh 2026.06.09.2
```

The release script writes binaries and checksums under `dist/`, stamping the version, git commit, and release date into the binary with Go linker flags. The release date is derived from the `YYYY.MM.DD` prefix.

## Repository Entry Points

`nf` is a Go CLI.

* Executable entrypoint: `cmd/nf/main.go`
* CLI dispatcher: `internal/cli.Run`

Primary always-visible command groups are `init`, `provider`, `target`, `site`, `config`, and `password`.

Project-only command groups are `remote`, `plugin`, `theme`, `env`, and `alias`. They appear only when the current repo has `nf.json` next to `.git`.

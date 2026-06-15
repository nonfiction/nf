# Installation

## Install and Run

Install the latest `nf` from the GitHub flake into your Nix profile:

```sh
nix profile add github:nonfiction/nf
```

Run from a checkout during development:

```sh
nix run .#nf -- --help
nix develop -c nf --help
go run ./cmd/nf --help
```

Build and test:

```sh
nix build .#nf -L
go test ./...
```

Nix flakes build from the git source snapshot. Stage newly added source files before trusting `nix run` or `nix build`.

## Shell Completion

`nf` can print completion scripts for bash and zsh:

```sh
nf completion bash
nf completion zsh
```

Temporary setup for the current shell:

```sh
# bash
source <(nf completion bash)

# zsh
source <(nf completion zsh)
```

Persistent setup depends on your shell config. Save the generated script into a file loaded by your shell, such as a bash completion directory or a zsh `$fpath` completion file.

## Versioning and Releases

`nf` uses date-based versions in `YYYY.MM.DD.N` form. `N` increments for multiple releases on the same UTC date. The current source version lives in `internal/version/VERSION`.

Check the installed binary:

```sh
nf version
nf version --short
```

Build release artifacts for the version in `internal/version/VERSION`:

```sh
scripts/release.sh
```

Build artifacts for an explicit version:

```sh
scripts/release.sh 2026.06.09.2
```

The release script writes binaries and checksums under `dist/`, stamping the version, git commit, and release date into the binary with Go linker flags. The release date is derived from the `YYYY.MM.DD` prefix.

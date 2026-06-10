#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
if [ -z "$version" ]; then
  version="$(tr -d '[:space:]' < internal/version/VERSION)"
fi

if [[ ! "$version" =~ ^[0-9]{4}\.[0-9]{2}\.[0-9]{2}\.[0-9]+$ ]]; then
  echo "release version must use YYYY.MM.DD.N format: $version" >&2
  exit 1
fi

commit="$(git rev-parse --short HEAD 2>/dev/null || printf unknown)"
date="${version:0:4}-${version:5:2}-${version:8:2}"
out_dir="dist"
ldflags="-s -w -X github.com/nonfiction/nf/internal/version.Version=$version -X github.com/nonfiction/nf/internal/version.Commit=$commit -X github.com/nonfiction/nf/internal/version.Date=$date"

mkdir -p "$out_dir"

targets=(
  "linux amd64"
  "linux arm64"
  "darwin amd64"
  "darwin arm64"
)

for target in "${targets[@]}"; do
  read -r goos goarch <<<"$target"
  output="$out_dir/nf-$version-$goos-$goarch"
  if [ "$goos" = "windows" ]; then
    output="$output.exe"
  fi
  printf 'building %s\n' "$output"
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath -ldflags "$ldflags" -o "$output" ./cmd/nf
done

checksums="$out_dir/nf-$version-checksums.txt"
checksums_tmp="$out_dir/.nf-$version-checksums.tmp"
(
  cd "$out_dir"
  sha256sum nf-"$version"-* | sort -k2
) >"$checksums_tmp"
mv "$checksums_tmp" "$checksums"

printf 'release artifacts written to %s\n' "$out_dir"
printf 'checksums written to %s\n' "$checksums"

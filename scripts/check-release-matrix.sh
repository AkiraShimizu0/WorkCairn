#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
temporary_root=$(mktemp -d "${TMPDIR:-/tmp}/workspace-os-build-matrix.XXXXXX")
trap 'rm -rf "$temporary_root"' EXIT HUP INT TERM

module=github.com/AkiraShimizu0/workspace-os/go/internal/buildinfo
version=$(sed -n '1p' "$repository_root/VERSION")
ldflags="-s -w -X $module.Version=$version -X $module.Commit=matrix-check -X $module.BuildDate=unknown"

for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
  target_os=${target%/*}
  target_arch=${target#*/}
  for command in workspace-core workspace-run workspace-daemon; do
    (
      cd "$repository_root/go"
      CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" GOTELEMETRY=off \
        go build -trimpath -buildvcs=false -ldflags "$ldflags" \
        -o "$temporary_root/${command}_${target_os}_${target_arch}" "./cmd/$command"
    )
  done
done

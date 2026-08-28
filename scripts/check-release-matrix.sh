#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
temporary_root=$(mktemp -d "${TMPDIR:-/tmp}/workcairn-build-matrix.XXXXXX")
trap 'rm -rf "$temporary_root"' EXIT HUP INT TERM

module=github.com/AkiraShimizu0/WorkCairn/go/internal/buildinfo
version=$(sed -n '1p' "$repository_root/VERSION")
ldflags="-s -w -X $module.Version=$version -X $module.Commit=matrix-check -X $module.BuildDate=unknown"
host_os=$(cd "$repository_root/go" && go env GOOS)

for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
  target_os=${target%/*}
  target_arch=${target#*/}
  if [ "$target_os" = darwin ]; then
    if [ "$host_os" = darwin ]; then
      target_cgo=1
    else
      # Non-macOS CI can still check the portable compile surface, but cannot
      # validate or package the Security.framework-backed Keychain Adapter.
      target_cgo=0
    fi
  else
    target_cgo=0
  fi
  for command in workcairn-core workcairn workcairn-daemon; do
    (
      cd "$repository_root/go"
      CGO_ENABLED="$target_cgo" GOOS="$target_os" GOARCH="$target_arch" GOTELEMETRY=off \
        go build -trimpath -buildvcs=false -ldflags "$ldflags" \
        -o "$temporary_root/${command}_${target_os}_${target_arch}" "./cmd/$command"
    )
  done
done

#!/bin/sh
set -eu

case "${RELEASE_VERSION:-}" in
  ""|*[!A-Za-z0-9._-]*)
    echo "RELEASE_VERSION must contain only letters, digits, dot, underscore, or hyphen" >&2
    exit 2
    ;;
esac

case "${RELEASE_GOOS:-}/${RELEASE_GOARCH:-}" in
  /|/*|*/)
    echo "RELEASE_GOOS and RELEASE_GOARCH are required" >&2
    exit 2
    ;;
esac

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
dist_root=${DIST_DIR:-dist}
case "$dist_root" in
  /*) ;;
  *) dist_root="$repository_root/$dist_root" ;;
esac

archive_name="workspace-os_${RELEASE_VERSION}_${RELEASE_GOOS}_${RELEASE_GOARCH}"
package_dir="$dist_root/$archive_name"
archive_path="$dist_root/$archive_name.tar.gz"
checksum_path="$archive_path.sha256"

if [ -e "$package_dir" ] || [ -e "$archive_path" ] || [ -e "$checksum_path" ]; then
  echo "release output already exists; refusing to overwrite: $archive_name" >&2
  exit 1
fi

mkdir -p "$package_dir/bin" "$package_dir/docs"

module=github.com/AkiraShimizu0/workspace-os/go/internal/buildinfo
commit=${BUILD_COMMIT:-unknown}
build_date=${BUILD_DATE:-unknown}
ldflags="-s -w -X $module.Version=$RELEASE_VERSION -X $module.Commit=$commit -X $module.BuildDate=$build_date"

for command in workspace-core workspace-run workspace-daemon; do
  (
    cd "$repository_root/go"
    CGO_ENABLED=0 GOOS="$RELEASE_GOOS" GOARCH="$RELEASE_GOARCH" GOTELEMETRY=off \
      go build -trimpath -buildvcs=false -ldflags "$ldflags" -o "$package_dir/bin/$command" "./cmd/$command"
  )
done

cp "$repository_root/LICENSE" "$repository_root/README.md" "$repository_root/CHANGELOG.md" "$package_dir/"
cp "$repository_root"/docs/*.md "$repository_root"/docs/*.mmd "$package_dir/docs/"
mkdir -p "$package_dir/docs/adr"
cp "$repository_root"/docs/adr/*.md "$package_dir/docs/adr/"

(
  cd "$dist_root"
  tar -czf "$archive_name.tar.gz" "$archive_name"
  shasum -a 256 "$archive_name.tar.gz" > "$archive_name.tar.gz.sha256"
)

echo "$archive_path"
echo "$checksum_path"

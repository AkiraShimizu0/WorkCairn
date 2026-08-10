#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <workcairn archive.tar.gz>" >&2
  exit 2
fi

archive=$1
case "$archive" in
  /*) ;;
  *) archive=$(CDPATH= cd -- "$(dirname -- "$archive")" && pwd)/$(basename -- "$archive") ;;
esac
checksum_path="$archive.sha256"
archive_directory=$(dirname -- "$archive")
archive_filename=$(basename -- "$archive")

if [ ! -f "$archive" ] || [ ! -f "$checksum_path" ]; then
  echo "archive and adjacent .sha256 file are required" >&2
  exit 1
fi

(
  cd "$archive_directory"
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 -c "$(basename -- "$checksum_path")"
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c "$(basename -- "$checksum_path")"
  else
    echo "shasum or sha256sum is required" >&2
    exit 1
  fi
)

root=${archive_filename%.tar.gz}
temporary_list=$(mktemp "${TMPDIR:-/tmp}/workcairn-archive-list.XXXXXX")
trap 'rm -f "$temporary_list"' EXIT HUP INT TERM
tar -tzf "$archive" > "$temporary_list"

for required in \
  "$root/" \
  "$root/bin/" \
  "$root/bin/workcairn-core" \
  "$root/bin/workcairn" \
  "$root/bin/workcairn-daemon" \
  "$root/VERSION" \
  "$root/LICENSE" \
  "$root/README.md" \
  "$root/CHANGELOG.md" \
  "$root/SECURITY.md" \
  "$root/CONTRIBUTING.md" \
  "$root/docs/" \
  "$root/docs/adr/"
do
  if ! grep -Fqx "$required" "$temporary_list"; then
    echo "required release asset is missing: $required" >&2
    exit 1
  fi
done

while IFS= read -r path; do
  case "$path" in
    "$root/"|"$root/bin/"|"$root/bin/workcairn-core"|"$root/bin/workcairn"|"$root/bin/workcairn-daemon"|\
    "$root/VERSION"|"$root/LICENSE"|"$root/README.md"|"$root/CHANGELOG.md"|"$root/SECURITY.md"|"$root/CONTRIBUTING.md"|\
    "$root/docs/"|"$root/docs/"*.md|"$root/docs/"*.mmd|"$root/docs/adr/"|"$root/docs/adr/"*.md)
      ;;
    *)
      echo "unexpected release asset: $path" >&2
      exit 1
      ;;
  esac
done < "$temporary_list"

version=$(tar -xOzf "$archive" "$root/VERSION" | tr -d '\r\n')
case "$root" in
  workcairn_"$version"_*) ;;
  *)
    echo "archive root and VERSION disagree: $root / $version" >&2
    exit 1
    ;;
esac

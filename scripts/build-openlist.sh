#!/usr/bin/env bash
# Build the OpenList fork (with the MiCloud driver) and install it.
#
#   scripts/build-openlist.sh [install-path]     default: ~/.local/bin/openlist
#
# The frontend version must match the one embedded at build time; it is
# fetched into openlist/public/dist when missing.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
src="$root/openlist"
dest="${1:-$HOME/.local/bin/openlist}"
web_version="${WEB_VERSION:-v4.2.9}"

cd "$src"
if [[ "$(cat public/dist/VERSION 2>/dev/null)" != "$web_version" ]]; then
  tmp="$(mktemp -d)"
  gh release download "$web_version" -R OpenListTeam/OpenList-Frontend \
    -p "openlist-frontend-dist-$web_version.tar.gz" -D "$tmp"
  find public/dist -mindepth 1 ! -name README.md -exec rm -rf {} +
  tar -xzf "$tmp/openlist-frontend-dist-$web_version.tar.gz" -C public/dist
  rm -rf "$tmp"
fi

commit="$(git rev-parse --short HEAD)"
pkg=github.com/OpenListTeam/OpenList/v4/internal/conf
ldflags="-w -s \
  -X '$pkg.BuiltAt=$(date +'%F %T %z')' \
  -X '$pkg.GitAuthor=$(git log -1 --format=%an)' \
  -X '$pkg.GitCommit=$commit' \
  -X '$pkg.Version=v4-micloud-$commit' \
  -X '$pkg.WebVersion=${web_version#v}'"

go build -tags=jsoniter -ldflags="$ldflags" -o "$dest.new" .
mv "$dest.new" "$dest"
echo "installed $dest ($("$dest" version 2>/dev/null | grep -i '^version' || true))"

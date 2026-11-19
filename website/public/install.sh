#!/bin/sh
# Copyright 2026 The Goliash Authors
# SPDX-License-Identifier: AGPL-3.0-only
#
# Installs the goliash command from the GitHub release: the archive for this system, checked against the
# release's checksums, unpacked into ~/.local/bin (or GOLIASH_INSTALL_DIR). Nothing else is touched, no sudo.
#
#   curl -fsSL https://goliash.dev/install.sh | sh
#   curl -fsSL https://goliash.dev/install.sh | GOLIASH_VERSION=1.269.0 sh
set -eu

repo=https://github.com/pipozzz/goliash
dir=${GOLIASH_INSTALL_DIR:-$HOME/.local/bin}

fail() {
  echo "install.sh: $1" >&2
  exit 1
}

case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "no archive for $(uname -s): see $repo/releases" ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "no archive for $(uname -m): see $repo/releases" ;;
esac

version=${GOLIASH_VERSION:-}
if [ -z "$version" ]; then
  # The latest release's address ends in its tag.
  version=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$repo/releases/latest" | sed 's#.*/tag/v##')
fi
version=${version#v}
case $version in
  '' | *[!0-9.]*) fail "could not find the latest release: set GOLIASH_VERSION" ;;
esac

if command -v sha256sum >/dev/null 2>&1; then
  sum() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sum() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  fail "sha256sum or shasum is needed to check the download"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
archive=goliash_${version}_${os}_${arch}.tar.gz
echo "Downloading goliash $version ($os/$arch) …"
curl -fsSL -o "$tmp/$archive" "$repo/releases/download/v$version/$archive" || fail "no $archive in release v$version"
curl -fsSL -o "$tmp/checksums.txt" "$repo/releases/download/v$version/checksums.txt"
want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "$archive is not in the release's checksums"
[ "$(sum "$tmp/$archive")" = "$want" ] || fail "$archive does not match the release's checksum"

tar -xzf "$tmp/$archive" -C "$tmp" goliash
mkdir -p "$dir"
mv "$tmp/goliash" "$dir/goliash"
chmod 755 "$dir/goliash"

echo "Installed $dir/goliash"
case ":$PATH:" in
  *":$dir:"*) run=goliash ;;
  *)
    run=$dir/goliash
    echo "$dir is not on your PATH: add it, or run it by its path."
    ;;
esac
echo
echo "See what runs here and what is behind (reads your kubeconfig's context and Docker, read-only):"
echo
echo "  $run scan"
echo

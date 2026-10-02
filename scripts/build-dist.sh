#!/bin/sh
# Builds the ctx release archives for the four supported platforms plus
# checksums.txt, in the layout goreleaser writes (ctx_<version>_<os>_<arch>.tar.gz),
# so the server (GHOSTTREE_DIST_DIR) or a GitHub release can serve them alike.
#
#   scripts/build-dist.sh [outdir]      VERSION=1.2.3 overrides the version
set -eu

out=${1:-dist/release}
version=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' | tr -c 'A-Za-z0-9._+\n-' '-')}
version=$(printf '%s' "${version:-0.0.0-dev}" | tr -c 'A-Za-z0-9._+-' '-')

# Refuse to wipe anything that is not an empty directory or an earlier output.
case "$out" in
  "" | / | . | .. | "$HOME" | "$HOME"/) echo "refusing output directory '$out'" >&2; exit 1 ;;
esac
if [ -e "$out" ] && [ -n "$(ls -A "$out" 2>/dev/null)" ] && [ ! -f "$out/checksums.txt" ]; then
  echo "refusing to overwrite '$out': not empty and not a previous build-dist output" >&2
  exit 1
fi

if tar --version 2>/dev/null | grep -q 'GNU tar'; then
  tar_owner='--owner=0 --group=0 --numeric-owner'
else
  tar_owner='--uid 0 --gid 0 --numeric-owner'
fi

if command -v sha256sum >/dev/null 2>&1; then
  sum() { sha256sum "$@"; }
else
  sum() { shasum -a 256 "$@"; }
fi

rm -rf "$out"
mkdir -p "$out"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

for os in linux darwin; do
  for arch in amd64 arm64; do
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
      -ldflags "-s -w -X main.version=$version" -o "$work/ctx" ./cmd/ctx
    # shellcheck disable=SC2086
    tar $tar_owner -czf "$out/ctx_${version}_${os}_${arch}.tar.gz" -C "$work" ctx
    rm -f "$work/ctx"
  done
done

(cd "$out" && sum ctx_*.tar.gz > checksums.txt)
echo "wrote $out ($version):"
cat "$out/checksums.txt"

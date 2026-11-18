#!/bin/sh
# Runs the tests of the public installer (internal/installscript) inside fresh
# containers, one per distribution, with the real minisign and OpenSSL of that
# distribution and no terminal:
#
#   scripts/e2e/installer-containers.sh [image ...]
#
# Without arguments: debian:12, ubuntu:24.04 and fedora:42. The tests are
# compiled here as one static binary and run in the container against a
# read-only checkout, so the image needs nothing but its package manager.
# Needs Docker. The tests start their own release server on loopback and use
# throwaway keys; nothing leaves the machine except the package downloads.
set -eu

cd "$(dirname "$0")/../.."
root=$(pwd)
[ "$#" -gt 0 ] || set -- debian:12 ubuntu:24.04 fedora:42

out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
CGO_ENABLED=0 go test -c -o "$out/installscript.test" ./internal/installscript

failed=0
for image in "$@"; do
  echo "== $image"
  if docker run --rm --network host \
    -v "$root:/repo:ro" -v "$out:/out:ro" -w /repo/internal/installscript "$image" sh -c '
      set -eu
      if command -v apt-get >/dev/null 2>&1; then
        export DEBIAN_FRONTEND=noninteractive
        apt-get update -qq >/dev/null
        apt-get install -y -qq curl minisign openssl ca-certificates >/dev/null
      elif command -v dnf >/dev/null 2>&1; then
        dnf install -y -q curl minisign openssl tar gzip >/dev/null
      else
        echo "no supported package manager" >&2; exit 1
      fi
      minisign -v | head -n 1; openssl version
      /out/installscript.test -test.v -test.count=1 2>&1 | tee /tmp/t.log | grep -E "^(--- |ok|PASS|FAIL)|/minisign|SKIP"
      grep -q "/minisign" /tmp/t.log || { echo "the minisign verifier was not exercised" >&2; exit 1; }
      ! grep -q "^--- SKIP\|^    --- SKIP" /tmp/t.log || { echo "a test was skipped" >&2; exit 1; }
      grep -q "^PASS" /tmp/t.log
    ' </dev/null; then
    echo "-- $image: ok"
  else
    echo "-- $image: FAILED" >&2
    failed=1
  fi
done
exit "$failed"

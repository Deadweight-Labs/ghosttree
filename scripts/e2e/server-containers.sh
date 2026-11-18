#!/bin/sh
# The server installation (REQ-723) in fresh containers that run systemd, one per
# distribution: the one-line installer sets up a server (systemd service, key,
# nightly backup, accounts, login link), the link signs in, ctx server update
# moves it to a newer release, and an update to a release that does not come up
# goes back. No terminal. Throwaway keys; releases come from a local server in
# the container, so nothing is downloaded from GitHub.
#
#   scripts/e2e/server-containers.sh [image ...]     default: debian:12 ubuntu:24.04 fedora:42
#
# Needs Docker and permission to start privileged containers (systemd needs
# the cgroup tree). The containers are removed afterwards.
set -eu

cd "$(dirname "$0")/../.."
[ "$#" -gt 0 ] || set -- debian:12 ubuntu:24.04 fedora:42

work=$(mktemp -d)
cname=gt-e2e-$$
cleanup() {
  docker rm -f "$cname" >/dev/null 2>&1 || true
  # Files made by the containers belong to root.
  docker run --rm -v "$work:/w" debian:12 rm -rf /w/key /w/key.pub >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

# One throwaway key pair, made by the real minisign.
docker run --rm -v "$work:/w" debian:12 sh -c '
  apt-get update -qq >/dev/null && apt-get install -y -qq minisign >/dev/null &&
  minisign -G -W -f -p /w/key.pub -s /w/key >/dev/null && chmod 644 /w/key /w/key.pub'
publine=$(sed -n 2p "$work/key.pub")

# ctx built with that key as its release key, in two versions.
arch=$(go env GOARCH)
for v in 0.1.0 0.1.1; do
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -X main.version=$v -X github.com/Deadweight-Labs/ghosttree/internal/releasekey.override=$publine" \
    -o "$work/ctx-$v" ./cmd/ctx
done
cp deploy/downloads/ghosttree/install.sh scripts/e2e/server-container-test.sh "$work/"

failed=0
for image in "$@"; do
  echo "== $image"
  docker rm -f "$cname" >/dev/null 2>&1 || true
  docker run -d --name "$cname" --privileged --cgroupns=host \
    -v /sys/fs/cgroup:/sys/fs/cgroup:rw --tmpfs /run --tmpfs /run/lock "$image" sh -c '
      if command -v apt-get >/dev/null 2>&1; then
        export DEBIAN_FRONTEND=noninteractive
        apt-get update -qq >/dev/null
        apt-get install -y -qq systemd systemd-sysv curl minisign python3-minimal ca-certificates sudo >/dev/null
        init=/lib/systemd/systemd
      else
        dnf install -y -q systemd curl minisign python3 sudo tar gzip >/dev/null
        init=/usr/lib/systemd/systemd
      fi
      exec $init' >/dev/null
  # Wait for the packages and for systemd to be up.
  i=0
  until docker exec "$cname" sh -c 'command -v minisign >/dev/null && command -v python3 >/dev/null && [ "$(systemctl is-system-running 2>/dev/null)" != "" ] && systemctl is-system-running 2>/dev/null | grep -Eq "running|degraded"' 2>/dev/null; do
    i=$((i + 1))
    if [ "$i" -gt 180 ]; then echo "container did not come up" >&2; docker logs "$cname" | tail -n 20 >&2; failed=1; continue 2; fi
    sleep 2
  done
  docker exec "$cname" mkdir -p /e2e
  docker cp "$work/." "$cname:/e2e/"
  docker exec "$cname" sh -c 'mkdir -p /srv/rel; sh /e2e/server-container-test.sh' </dev/null || failed=1
  if docker exec "$cname" journalctl -u ghosttree --no-pager -n 20 >/dev/null 2>&1; then :; fi
  if [ "$failed" -ne 0 ]; then
    echo "-- journal of ghosttree.service in $image:" >&2
    docker exec "$cname" journalctl -u ghosttree --no-pager -n 40 >&2 2>/dev/null || true
  fi
done
exit "$failed"

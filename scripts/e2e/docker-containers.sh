#!/bin/sh
# The Docker installation (REQ-723) from fresh distribution containers: the
# one-line installer sets a server up with Docker, the login link signs in,
# ctx server update moves it to a newer image and goes back from one that does
# not come up. The images are built here from the Dockerfile (nothing is pulled
# from ghcr.io); the fresh container talks to this machine's Docker through its
# socket, with a free port for the server and its own compose project, so a
# server of yours is not touched.
#
#   scripts/e2e/docker-containers.sh [image ...]     default: debian:12 ubuntu:24.04 fedora:42
#
# Needs Docker with the compose plugin and buildx. Everything it makes is removed.
set -eu

cd "$(dirname "$0")/../.."
[ "$#" -gt 0 ] || set -- debian:12 ubuntu:24.04 fedora:42

work=$(mktemp -d /tmp/gt-docker-e2e.XXXXXX)
project=gte2e$$
repo=ghcr.io/deadweight-labs/ghosttree
cleanup() {
  docker compose -p "$project" down -v >/dev/null 2>&1 || true
  for v in 0.1.0 0.1.1 0.1.2; do docker rmi "$repo:$v" >/dev/null 2>&1 || true; done
  # Files made by containers belong to root.
  docker run --rm -v "$work:/w" debian:12 sh -c 'rm -rf /w/* /w/.[!.]*' >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])'; }
port=$(free_port)
relport=$(free_port)

# A throwaway key pair, made by the real minisign.
docker run --rm -v "$work:/w" debian:12 sh -c '
  apt-get update -qq >/dev/null && apt-get install -y -qq minisign >/dev/null &&
  minisign -G -W -f -p /w/key.pub -s /w/key >/dev/null && chmod 644 /w/key /w/key.pub'
publine=$(sed -n 2p "$work/key.pub")

# ctx with that key as its release key.
CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) go build -trimpath \
  -ldflags "-s -w -X main.version=0.1.0 -X github.com/Deadweight-Labs/ghosttree/internal/releasekey.override=$publine" \
  -o "$work/ctx-0.1.0" ./cmd/ctx

# The image of two releases, and one that cannot come up: the entry point is
# `ctx version`, so it runs, upgrades nothing and never serves.
docker build -q --build-arg VERSION=0.1.0 -t "$repo:0.1.0" . >/dev/null
docker build -q --build-arg VERSION=0.1.1 -t "$repo:0.1.1" . >/dev/null
printf 'FROM %s:0.1.1\nENTRYPOINT ["/usr/local/bin/ctx", "version"]\n' "$repo" | docker build -q -t "$repo:0.1.2" - >/dev/null

cp deploy/downloads/ghosttree/install.sh scripts/e2e/docker-container-test.sh "$work/"

# Where the Docker client and its compose plugin are, to give the fresh container.
plugin=
for p in /usr/lib/docker/cli-plugins /usr/libexec/docker/cli-plugins /usr/local/lib/docker/cli-plugins /usr/local/libexec/docker/cli-plugins "$HOME/.docker/cli-plugins"; do
  [ -x "$p/docker-compose" ] && plugin=$p && break
done
[ -n "$plugin" ] || { echo "no docker compose plugin found" >&2; exit 1; }
docker_bin=$(command -v docker)

failed=0
for image in "$@"; do
  echo "== $image"
  mkdir -p "$work/setup"
  if ! docker run --rm --network host \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$docker_bin:/usr/local/bin/docker:ro" \
    -v "$plugin:/usr/local/lib/docker/cli-plugins:ro" \
    -v "$work:/e2e:ro" -v "$work/setup:$work/setup" \
    -e PORT="$port" -e RELPORT="$relport" -e PROJECT="$project" -e DIR="$work/setup/ghosttree" \
    "$image" sh -c '
      if command -v apt-get >/dev/null 2>&1; then
        export DEBIAN_FRONTEND=noninteractive
        apt-get update -qq >/dev/null
        apt-get install -y -qq curl minisign python3-minimal ca-certificates procps >/dev/null
      else
        dnf install -y -q curl minisign python3 tar gzip procps-ng >/dev/null
      fi
      mkdir -p /srv/rel
      sh /e2e/docker-container-test.sh' </dev/null; then
    failed=1
    docker compose -p "$project" logs --tail 30 >&2 2>/dev/null || true
  fi
  docker compose -p "$project" down -v >/dev/null 2>&1 || true
  docker run --rm -v "$work/setup:/w" debian:12 sh -c 'rm -rf /w/* /w/.[!.]*' >/dev/null 2>&1 || true
done
exit "$failed"

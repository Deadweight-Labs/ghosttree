#!/bin/bash
# Baut das Sandbox-Image: build.sh [<claude-binary>] [<tag>]
# Ohne Argument wird der claude aus dem PATH genommen (Symlinks aufgelöst). Die Version steht im Image
# und im Isolationsprotokoll (image_id); wer sie pinnen will, gibt die Binärdatei der gewünschten Version an.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
claude=$(readlink -f "${1:-$(command -v claude)}")
tag=${2:-${BENCH_IMAGE:-agent-compare:local}}
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "unbekannte Architektur $(uname -m)" >&2; exit 1 ;;
esac
ctx=$(mktemp -d)
trap 'rm -rf "$ctx"' EXIT
cp "$here/Dockerfile" "$ctx/"
cp "$claude" "$ctx/claude-$arch"
docker build --build-arg "TARGETARCH=$arch" -t "$tag" "$ctx"
echo "Image $tag gebaut (claude: $("$claude" --version 2>/dev/null | head -1))"

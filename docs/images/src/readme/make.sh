#!/usr/bin/env bash
# Rebuilds the README pictures in docs/images from invented demo data.
#
#   docs/images/src/readme/make.sh            all pictures and the loop GIF
#   PORT=19700 docs/images/src/readme/make.sh
#
# Needs Go, Node with Playwright (PLAYWRIGHT=/path/to/playwright/index.mjs if it
# is not installed where Node finds it), ffmpeg and pngquant. Everything runs in
# a temporary directory under an empty HOME, on 127.0.0.1, and is removed at
# the end. The server never sees a real configuration or a real database.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../../.." && pwd)
out="$repo/docs/images"
port=${PORT:-19631}
work=$(mktemp -d /tmp/ghosttree-readme.XXXXXX)
cleanup() {
  pkill -f -- "^$work/" 2>/dev/null || true
  for _ in $(seq 50); do pgrep -f -- "^$work/" > /dev/null || break; sleep 0.1; done
  rm -rf "$work"
}
trap cleanup EXIT

if [[ -n "$(ss -ltnH "sport = :$port" 2>/dev/null)" ]]; then
  echo "port $port is taken; set PORT" >&2; exit 1
fi
mkdir -p "$work"/{home/.config,tmp,shots,gif}
env=(env -i PATH=/usr/bin:/bin HOME="$work/home" XDG_CONFIG_HOME="$work/home/.config" TMPDIR="$work/tmp" TZ=UTC LANG=C.UTF-8
  GHOSTTREE_LANGUAGE=en GHOSTTREE_ENFORCE_ACCESS=1 GHOSTTREE_EXPLAIN=off GHOSTTREE_LLM_CONFIG="$work/home/.config/ghosttree/llm.json")

(cd "$repo" && CGO_ENABLED=0 go build -o "$work/ctx" ./cmd/ctx && CGO_ENABLED=0 go build -tags readmeshots -o "$work/seed" ./docs/images/src/readme)
"${env[@]}" "$work/seed" "$work/gt.db" team > /dev/null
(cd "$work" && exec "${env[@]}" "$work/ctx" serve --db "$work/gt.db" --listen "127.0.0.1:$port" > "$work/server.log" 2>&1 < /dev/null) &
(cd "$work" && exec "${env[@]}" "$work/seed" pulse "$work/gt.db" 30 > /dev/null 2>&1 < /dev/null) &
for _ in $(seq 80); do curl -fsS -o /dev/null "http://127.0.0.1:$port/ui/login" 2>/dev/null && break; sleep 0.25; done
link() { "${env[@]}" "$work/ctx" account login-link Mia --db "$work/gt.db" --url "http://127.0.0.1:$port" 2>&1 | grep -o 'http[^ ]*' | tail -1; }

node "$here/shoot.mjs" "http://127.0.0.1:$port" "$(link)" "$work/shots" ui-context ui-code ui-idea ui-pr ui-room
node "$here/shoot.mjs" "http://127.0.0.1:$port" "$(link)" "$work/gif" gif-1 gif-2 gif-3 gif-4 gif-5

for f in "$work"/shots/*.png; do pngquant --quality 85-98 --speed 1 --force --output "$out/$(basename "$f")" "$f"; done
: > "$work/gif/list.txt"
for i in 1 2 3 4 5; do printf "file 'gif-%s.png'\nduration 2.6\n" "$i" >> "$work/gif/list.txt"; done
printf "file 'gif-5.png'\n" >> "$work/gif/list.txt"
(cd "$work/gif" && ffmpeg -loglevel error -y -f concat -i list.txt \
  -vf "scale=880:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=96:stats_mode=full[p];[b][p]paletteuse=dither=none" \
  -loop 0 "$out/demo-loop.gif")
ls -la "$out"/ui-*.png "$out/demo-loop.gif"

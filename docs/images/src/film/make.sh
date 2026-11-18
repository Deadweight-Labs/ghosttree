#!/usr/bin/env bash
# Rebuilds the README loop docs/images/demo-loop.webp from invented demo data.
#
#   docs/images/src/film/make.sh            capture the moments, compose and render the README loop
#   KEEP=/path docs/images/src/film/make.sh  also keep the stills and HyperFrames projects there
#   TAKES=all KEEP=/path ...                 also record the screen takes of the website clips
#
# Needs Go, Node with Playwright (PLAYWRIGHT=/path/to/playwright/index.mjs if Node does not find it),
# ffmpeg with libwebp, and npx (HyperFrames renders the composition). Everything runs in a temporary
# directory under an empty HOME, on 127.0.0.1, and is removed at the end unless KEEP is set.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../../.." && pwd)
port=${PORT:-19632}
work=$(mktemp -d /tmp/ghosttree-film.XXXXXX)
cleanup() {
  pkill -f -- "^$work/" 2>/dev/null || true
  for _ in $(seq 50); do pgrep -f -- "^$work/" > /dev/null || break; sleep 0.1; done
  if [[ -n "${KEEP:-}" ]]; then mkdir -p "$KEEP" && cp -r "$work/film/." "$KEEP/"; fi
  rm -rf "$work"
}
trap cleanup EXIT

if [[ -n "$(ss -ltnH "sport = :$port" 2>/dev/null)" ]]; then
  echo "port $port is taken; set PORT" >&2; exit 1
fi
mkdir -p "$work"/{home/.config,tmp} "$work/film/assets/rec"
env=(env -i PATH=/usr/bin:/bin HOME="$work/home" XDG_CONFIG_HOME="$work/home/.config" TMPDIR="$work/tmp" TZ=UTC LANG=C.UTF-8
  GHOSTTREE_LANGUAGE=en GHOSTTREE_ENFORCE_ACCESS=1 GHOSTTREE_EXPLAIN=off GHOSTTREE_LLM_CONFIG="$work/home/.config/ghosttree/llm.json")

(cd "$repo" && CGO_ENABLED=0 go build -o "$work/ctx" ./cmd/ctx && CGO_ENABLED=0 go build -tags readmeshots -o "$work/seed" ./docs/images/src/readme)
# A fresh copy of the demo data and a server on it; the stills and the screen takes each start from one.
serve_fresh() {
  pkill -f -- "^$work/" 2>/dev/null || true
  for _ in $(seq 50); do pgrep -f -- "^$work/" > /dev/null || break; sleep 0.1; done
  rm -f "$work"/gt.db*
  "${env[@]}" "$work/seed" "$work/gt.db" team > /dev/null
  (cd "$work" && exec "${env[@]}" "$work/ctx" serve --db "$work/gt.db" --listen "127.0.0.1:$port" > "$work/server.log" 2>&1 < /dev/null) &
  (cd "$work" && exec "${env[@]}" "$work/seed" pulse "$work/gt.db" 30 > /dev/null 2>&1 < /dev/null) &
  for _ in $(seq 80); do curl -fsS -o /dev/null "http://127.0.0.1:$port/ui/login" 2>/dev/null && break; sleep 0.25; done
}

# The moments are stills at phone width without a pointer; "checked" approves the pitfall the later ones show.
serve_fresh
BASE="http://127.0.0.1:$port" WORK="$work" OUT="$work/film/assets/moments" node "$here/moments.mjs"
if [[ "${TAKES:-}" == all ]]; then
  # Screen takes with a drawn pointer for the website clips; "approve" needs the pitfall still waiting.
  serve_fresh
  rec="$work/film/assets/rec"
  BASE="http://127.0.0.1:$port" WORK="$work" OUT="$rec" node "$here/takes.mjs" found approve brief code payoff checklist team gardener workshop workflows --w=1000 --h=625
  (cd "$rec" && for f in *.mp4; do printf '%s %s\n' "${f%.mp4}" "$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$f")"; done) \
    | awk 'BEGIN{printf "{"} {printf "%s\"%s\": %s", (NR>1?", ":""), $1, $2} END{print "}"}' > "$rec/lengths.json"
fi

cp -r "$here/../fonts" "$work/film/assets/fonts"
cp "$here/gen.mjs" "$here/split.mjs" "$work/film/"
printf '{"paths":{"assets":"assets"}}\n' > "$work/film/hyperframes.json"
FILM_ROOT="$work/film" node "$work/film/split.mjs" hero
[[ "${TAKES:-}" == all ]] && FILM_ROOT="$work/film" REC=2000x1250 node "$work/film/gen.mjs" clip-briefing clip-workshop clip-checklist clip-room clip-gardener clip-workflows clip-agent-memory
(cd "$work/film/films/hero" && npx --yes hyperframes@0.9.65 render --workers 3 --quality delivery --no-browser-gpu -o "$work/film/hero.mp4")
ffmpeg -loglevel error -y -i "$work/film/hero.mp4" -vf "fps=15,scale=960:-1:flags=lanczos" -c:v libwebp_anim -quality 72 -compression_level 6 -loop 0 -an "$repo/docs/images/demo-loop.webp"
ls -la "$repo/docs/images/demo-loop.webp"

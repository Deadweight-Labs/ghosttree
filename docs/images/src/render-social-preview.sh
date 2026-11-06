#!/bin/sh
# Renders docs/images/social-preview.png (1280x640) from social-preview.html.
# Needs chromium or google-chrome on PATH. Run from anywhere.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
browser=$(command -v chromium || command -v chromium-browser || command -v google-chrome || true)
[ -n "$browser" ] || { echo "no chromium/chrome found" >&2; exit 1; }
"$browser" --headless --disable-gpu --hide-scrollbars --force-device-scale-factor=1 \
  --window-size=1280,640 --screenshot="$here/../social-preview.png" \
  "file://$here/social-preview.html"

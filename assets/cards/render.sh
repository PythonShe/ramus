#!/usr/bin/env bash
# Renders each card's HTML to a 1600x1000 JPEG beside it. Needs Chrome and
# ImageMagick, and the network once for the Inter font.
set -euo pipefail
cd "$(dirname "$0")"
chrome="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
for card in midnight ivory bento; do
  # Drawn at twice the size and scaled down, which keeps the small text sharp.
  "$chrome" --headless=new --disable-gpu --hide-scrollbars \
    --force-device-scale-factor=2 --window-size=1600,1000 \
    --virtual-time-budget=8000 --screenshot="$tmp/$card.png" \
    "file://$PWD/$card.html" >/dev/null 2>&1
  magick "$tmp/$card.png" -filter Lanczos -resize 1600x1000 -strip -quality 88 "$card.jpg"
done

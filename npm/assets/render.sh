#!/bin/sh
# Renders npm/assets/header.html to npm/assets/header.png at twice its CSS size.
# Needs Google Chrome and a checkout of nearhost/landing with dependencies
# installed, for the fonts: $LANDING, by default ~/Code/nearhost/landing.
set -eu
here="$(cd "$(dirname "$0")" && pwd)"
landing="$(cd "${LANDING:-$HOME/Code/nearhost/landing}" && pwd)"
chrome="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
sed "s#LANDING/#file://$landing/#g" "$here/header.html" > "$work/header.html"
"$chrome" --headless=new --disable-gpu --hide-scrollbars \
  --user-data-dir="$work/profile" \
  --force-device-scale-factor=2 --window-size=1280,460 \
  --default-background-color=00000000 \
  --screenshot="$here/header.png" "file://$work/header.html" >/dev/null 2>&1
echo "wrote $here/header.png"

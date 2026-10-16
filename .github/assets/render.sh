#!/bin/sh
# Renders header.html twice at twice its CSS size: header.png with the curl
# installer for README.md, and header-npm.png with npx for npm/cli/README.md.
# Needs Google Chrome and a checkout of nearhost/landing with dependencies
# installed, for the fonts: $LANDING, by default ~/Code/nearhost/landing.
set -eu
here="$(cd "$(dirname "$0")" && pwd)"
landing="$(cd "${LANDING:-$HOME/Code/nearhost/landing}" && pwd)"
chrome="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

render() { # render <output> <install command>
  sed -e "s#LANDING/#file://$landing/#g" -e "s#INSTALL#$2#" "$here/header.html" > "$work/header.html"
  "$chrome" --headless=new --disable-gpu --hide-scrollbars \
    --user-data-dir="$work/profile" \
    --force-device-scale-factor=2 --window-size=1280,460 \
    --default-background-color=00000000 \
    --screenshot="$here/$1" "file://$work/header.html" >/dev/null 2>&1
  echo "wrote $here/$1"
}

render header.png 'curl -fsSL https://nearhost.dev/install.sh | sh'
render header-npm.png 'npx @nearhost/cli'

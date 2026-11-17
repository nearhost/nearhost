#!/bin/sh
# Prints "on" when a release builds Windows, otherwise "off", for
# .goreleaser.yaml's NEARHOST_WINDOWS. release.yml runs it for each release,
# and CI's release-config job checks it for every combination.
#
# From the environment:
#   TAG               the release's tag, such as v0.13.0 or v0.13.0-rc.1
#   PRERELEASE        "true" when the GitHub release is a prerelease
#   WINDOWS_RELEASES  "on" when normal releases build Windows too
#
# A tag with a prerelease part must be a GitHub prerelease, and the reverse,
# or npm and Homebrew would treat the release wrongly; it then prints an
# error for the workflow log and exits 1.
set -eu

case "$TAG" in
  *-*) tag_pre=true ;;
  *) tag_pre=false ;;
esac
if [ "$tag_pre" != "$PRERELEASE" ]; then
  echo "::error::tag $TAG and the release's prerelease flag ($PRERELEASE) disagree"
  exit 1
fi
if [ "$PRERELEASE" = true ] || [ "${WINDOWS_RELEASES:-}" = on ]; then
  echo on
else
  echo off
fi

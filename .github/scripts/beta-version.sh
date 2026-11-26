#!/bin/sh
# Prints the next beta of a version, such as 0.16.0-beta.3, for beta.yml,
# which publishes one from every push to the release-please pull request.
# CI's release-config job checks it.
#
#   git tag -l | sh .github/scripts/beta-version.sh <version>
#
# <version> is the release PR's version, X.Y.Z with no v. Standard input is
# the repository's tags, one per line. The beta is one past the highest
# v<version>-beta.N tag, or beta.1 when there is none; other versions' tags
# and other prerelease kinds (-rc.N) don't count.
#
# Exits 2, printing nothing, when v<version> is already a tag: release-please
# first pushes its branch at main's own commit, before it bumps the version,
# and that push has no beta. Exits 1 when <version> isn't X.Y.Z.
set -eu

version=${1:-}
if ! printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "::error::version must look like 0.16.0, got: $version" >&2
  exit 1
fi

tags=$(cat)
# Dots in the version are literal in the patterns below.
escaped=$(printf '%s' "$version" | sed 's/\./\\./g')

if printf '%s\n' "$tags" | grep -Eqx "v$escaped"; then
  exit 2
fi

highest=$(printf '%s\n' "$tags" \
  | sed -n "s/^v$escaped-beta\.\([0-9][0-9]*\)\$/\1/p" \
  | sort -n | tail -n 1)
echo "$version-beta.$(( ${highest:-0} + 1 ))"

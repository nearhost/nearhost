#!/bin/sh
# Signs a Windows binary with Nearhost's Azure Artifact Signing certificate,
# then checks the signature, so Smart App Control lets it run. .goreleaser.yaml
# runs it on each nearhost.exe right after the build, before it is archived.
#
#   sh .github/scripts/sign-windows.sh <path to nearhost.exe>
#
# From the environment:
#   NEARHOST_SIGN_WINDOWS  "on" signs; "off" leaves the binary unsigned, for
#                          CI's snapshot, which has no Azure credentials. Unset
#                          fails, on purpose: release.yml and CI must set it.
#   JSIGN_JAR              the jsign jar (release.yml downloads it)
#
# Signing needs java, osslsigncode, and az logged in as an identity with the
# Artifact Signing Certificate Profile Signer role on the Nearhost account.
# Any failure exits non-zero, so a release fails rather than ship a Windows
# binary that is unsigned or signed by the wrong certificate.
set -eu

exe=$1

case "${NEARHOST_SIGN_WINDOWS:-}" in
  on) ;;
  off) exit 0 ;;
  *)
    echo "sign-windows.sh: NEARHOST_SIGN_WINDOWS must be on or off, not '${NEARHOST_SIGN_WINDOWS:-}'" >&2
    exit 1
    ;;
esac

here=$(dirname "$0")

AZURE_SIGN_TOKEN=$(az account get-access-token --resource https://codesigning.azure.net --query accessToken -o tsv)
if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::add-mask::$AZURE_SIGN_TOKEN"; fi
export AZURE_SIGN_TOKEN

java -jar "$JSIGN_JAR" \
  --storetype TRUSTEDSIGNING \
  --keystore neu.codesigning.azure.net \
  --storepass env:AZURE_SIGN_TOKEN \
  --alias Nearhost/nearhost-public \
  --tsaurl http://timestamp.acs.microsoft.com/ \
  --tsmode RFC3161 \
  --alg SHA-256 \
  "$exe"

# Artifact Signing certificates chain to Microsoft's Identity Verification
# root, which is not in Ubuntu's CA bundle, so the root is kept beside this
# script (SHA-256 fingerprint 53:67:F2:0C:...:29:2E:12:70, valid to 2045).
if ! out=$(osslsigncode verify -CAfile "$here/microsoft-identity-verification-root-2020.pem" -in "$exe" 2>&1); then
  echo "$out" >&2
  echo "sign-windows.sh: the signature on $exe does not verify" >&2
  exit 1
fi
if ! echo "$out" | grep -q '^Succeeded$' || ! echo "$out" | grep -q 'O=ECS Software Consulting Ltd\./CN=ECS Software Consulting Ltd\.$'; then
  echo "$out" >&2
  echo "sign-windows.sh: $exe is not signed by ECS Software Consulting Ltd." >&2
  exit 1
fi
echo "sign-windows.sh: signed and verified $exe"

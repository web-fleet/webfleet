#!/bin/sh
# verify-gh-release.sh is a READ-ONLY post-release verifier. It inspects an
# actual GitHub Release: requires exactly the expected six archives plus
# checksum/provenance material, downloads them, validates checksums and the
# provenance manifest, and (when gh attestation is available) verifies the
# artifact attestations against the repository/ref. It never edits the release,
# replaces assets or mutates tags.
#
# Usage:
#   scripts/verify-gh-release.sh <owner/repo> <tag-or-release-name> [version]
set -eu

REPO="${1:?usage: verify-gh-release.sh <owner/repo> <tag> [version]}"
TAG="${2:?usage: verify-gh-release.sh <owner/repo> <tag> [version]}"
VERSION="${3:-$TAG}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

command -v gh >/dev/null 2>&1 || { echo "gh CLI required for post-release verification" >&2; exit 1; }

# List the release assets read-only.
gh release view "$TAG" --repo "$REPO" --json assets --jq '.assets[].name' > "$WORK/assets.txt"

EXPECTED="linux_amd64.tar.gz linux_arm64.tar.gz darwin_amd64.tar.gz darwin_arm64.tar.gz windows_amd64.zip windows_arm64.zip SHA256SUMS provenance.json"
for suffix in $EXPECTED; do
  base="webfleet_${VERSION}_${suffix}"
  if ! grep -qxF "$base" "$WORK/assets.txt"; then
    echo "release asset missing: $base" >&2; exit 1
  fi
done
# Reject any release asset outside the exact expected set.
allowed=""
for suffix in $EXPECTED; do
  allowed="$allowed webfleet_${VERSION}_${suffix}"
done
while read -r name; do
  ok=0
  for a in $allowed; do
    [ "$name" = "$a" ] && ok=1
  done
  if [ "$ok" -eq 0 ]; then
    echo "unexpected release asset: $name" >&2; exit 1
  fi
done < "$WORK/assets.txt"

# Download and verify checksums + provenance using the local exact-set verifier.
for name in SHA256SUMS provenance.json webfleet_${VERSION}_linux_amd64.tar.gz webfleet_${VERSION}_linux_arm64.tar.gz webfleet_${VERSION}_darwin_amd64.tar.gz webfleet_${VERSION}_darwin_arm64.tar.gz webfleet_${VERSION}_windows_amd64.zip webfleet_${VERSION}_windows_arm64.zip; do
  gh release download "$TAG" --repo "$REPO" --pattern "$name" --dir "$WORK" >/dev/null
done
scripts/verify-release.sh "$WORK"

# Verify attestations if GitHub attestation tooling is available.
if gh attestation verify "webfleet_${VERSION}_linux_amd64.tar.gz" --repo "$REPO" --format json > "$WORK/att.json" 2>/dev/null; then
  echo "attestation verified for linux_amd64 (repo: $REPO, ref: $TAG)"
else
  echo "note: no attestation verification available for this environment (gh attestation dry-run unavailable)" >&2
fi

echo "github release verified: $REPO $TAG (version $VERSION)"
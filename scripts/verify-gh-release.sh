#!/bin/sh
# verify-gh-release.sh is a READ-ONLY post-release verifier. It inspects an
# actual GitHub Release, requires exactly the agreed asset set, downloads that
# set, runs the local checksum verifier, and verifies the build provenance
# attestation of ALL six archives against the repository, ref and source
# commit, constrained to the release.yml signer workflow. Missing or invalid
# attestations are a FAILURE, not a note. It never edits the release, replaces
# assets or mutates tags.
#
# The authoritative public contract is defined by .github/workflows/release.yml
# and scripts/build-release.sh: six dash-named, non-versioned platform archives
# plus checksums.txt.
#
# Usage:
#   scripts/verify-gh-release.sh <owner/repo> <tag-or-release-name>
set -eu

REPO="${1:?usage: verify-gh-release.sh <owner/repo> <tag>}"
TAG="${2:?usage: verify-gh-release.sh <owner/repo> <tag>}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

command -v gh >/dev/null 2>&1 || { echo "gh CLI required for post-release verification" >&2; exit 1; }

# The exact permanent release asset set (the six dash-named archives that
# scripts/build-release.sh produces, plus checksums.txt).
EXPECTED="checksums.txt webfleet-linux-amd64.tar.gz webfleet-linux-arm64.tar.gz webfleet-darwin-amd64.tar.gz webfleet-darwin-arm64.tar.gz webfleet-windows-amd64.zip webfleet-windows-arm64.zip"

# 1. Require exactly the agreed asset set (missing and extra are failures).
gh release view "$TAG" --repo "$REPO" --json assets --jq '.assets[].name' > "$WORK/assets.txt"
for a in $EXPECTED; do
  grep -qxF "$a" "$WORK/assets.txt" || { echo "missing release asset: $a" >&2; exit 1; }
done
while read -r name; do
  [ -z "$name" ] && continue
  ok=0
  for a in $EXPECTED; do
    [ "$name" = "$a" ] && ok=1
  done
  if [ "$ok" -eq 0 ]; then
    echo "unexpected release asset: $name" >&2; exit 1
  fi
done < "$WORK/assets.txt"

# 2. Download exactly that set.
for a in $EXPECTED; do
  gh release download "$TAG" --repo "$REPO" --pattern "$a" --dir "$WORK" >/dev/null
done

# 3. Local checksum verification over the downloaded tree. checksums.txt
#    records the six archives; every line must verify exactly.
(cd "$WORK" && sha256sum -c checksums.txt)

# 4. Attestation gate: every archive must carry valid build provenance bound to
#    this repository, source ref (the tag), source commit and the release.yml
#    signer workflow, using the real gh attestation verify policy flags.
#    Failure exits non-zero. The source commit is resolved from the tag itself.
COMMIT="$(gh api "repos/$REPO/commits/$TAG" --jq .sha)"
for a in webfleet-linux-amd64.tar.gz webfleet-linux-arm64.tar.gz webfleet-darwin-amd64.tar.gz webfleet-darwin-arm64.tar.gz webfleet-windows-amd64.zip webfleet-windows-arm64.zip; do
  if ! gh attestation verify "$WORK/$a" \
    --repo "$REPO" \
    --source-ref "refs/tags/$TAG" \
    --source-digest "$COMMIT" \
    --signer-workflow "$REPO/.github/workflows/release.yml" >/dev/null 2>&1; then
    echo "attestation verification FAILED for $a" >&2
    exit 1
  fi
done

echo "github release verified: $REPO $TAG (commit $COMMIT)"
#!/bin/sh
# verify-gh-release.sh is a READ-ONLY post-release verifier. It inspects an
# actual GitHub Release, requires exactly the agreed asset set, downloads that
# set, runs a strict structural checksum validation over the whole manifest,
# and verifies the build provenance attestation of ALL six archives against the
# canonical repository, ref and source commit, constrained to the release.yml
# signer workflow. Missing or invalid attestations are a FAILURE, not a note.
# It never edits the release, replaces assets or mutates tags.
#
# The authoritative public contract is defined by .github/workflows/release.yml
# and scripts/build-release.sh: six dash-named, non-versioned platform archives
# plus checksums.txt.
#
# Repository identity is IMMUTABLE: provenance is only ever bound to the
# canonical webfleet-cv/webfleet repository and its release.yml signer. The
# owner/repo argument is accepted for interface compatibility but anything other
# than the canonical value is rejected before any remote interaction.
#
# Usage:
#   scripts/verify-gh-release.sh <owner/repo> <tag-or-release-name>
set -eu

CANONICAL_REPO="webfleet-cv/webfleet"
CANONICAL_SIGNER="$CANONICAL_REPO/.github/workflows/release.yml"

REPO="${1:?usage: verify-gh-release.sh <owner/repo> <tag>}"
TAG="${2:?usage: verify-gh-release.sh <owner/repo> <tag>}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

command -v gh >/dev/null 2>&1 || { echo "gh CLI required for post-release verification" >&2; exit 1; }

# 0. Immutable repository identity. Caller input can never select a different
#    repository or signer workflow.
if [ "$REPO" != "$CANONICAL_REPO" ]; then
  echo "refusing: provenance repository must be $CANONICAL_REPO, got '$REPO'" >&2
  exit 1
fi

# Validate the tag before interpolating it into any API/ref context.
if printf '%s' "$TAG" | LC_ALL=C grep -q '[^ -~]'; then
  echo "invalid characters in release tag '$TAG'" >&2; exit 1
fi
printf '%s\n' "$TAG" | LC_ALL=C grep -q '^v[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\(-[0-9A-Za-z][0-9A-Za-z.]*\)\?$' \
  || { echo "invalid release tag '$TAG'" >&2; exit 1; }

# The exact permanent release asset set (the six dash-named archives that
# scripts/build-release.sh produces, plus checksums.txt).
EXPECTED="checksums.txt webfleet-linux-amd64.tar.gz webfleet-linux-arm64.tar.gz webfleet-darwin-amd64.tar.gz webfleet-darwin-arm64.tar.gz webfleet-windows-amd64.zip webfleet-windows-arm64.zip"
ARCHIVES="webfleet-linux-amd64.tar.gz webfleet-linux-arm64.tar.gz webfleet-darwin-amd64.tar.gz webfleet-darwin-arm64.tar.gz webfleet-windows-amd64.zip webfleet-windows-arm64.zip"

# 1. Require exactly the agreed asset set (missing and extra are failures).
gh release view "$TAG" --repo "$CANONICAL_REPO" --json assets --jq '.assets[].name' > "$WORK/assets.txt"
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
  gh release download "$TAG" --repo "$CANONICAL_REPO" --pattern "$a" --dir "$WORK" >/dev/null
done

# 3. Strict structural validation of checksums.txt BEFORE verifying bytes. The
#    manifest is a separate trust boundary: it must bind all six expected
#    archives exactly once, with exactly 64 lowercase hex digits, exactly two
#    spaces as the separator, and bare filenames only. Missing, duplicate,
#    repeated, unexpected, traversing, absolute or malformed entries are a
#    failure. Then every archive's bytes are verified against the manifest.
python3 - "$WORK" "$ARCHIVES" <<'PY'
import re, sys
work, archives = sys.argv[1], sys.argv[2].split()
arch_set = set(archives)
if len(arch_set) != 6:
    sys.exit("internal: expected archive set is not distinct")

raw = open(f"{work}/checksums.txt", encoding="utf-8").read()
if "\x00" in raw or any(ord(c) < 32 and c not in "\n" for c in raw):
    sys.exit("checksums.txt contains control characters")
lines = [l for l in raw.splitlines()]
if len(lines) != 6:
    sys.exit(f"checksums.txt must contain exactly six lines, found {len(lines)}")

seen = {}
for i, line in enumerate(lines, 1):
    if len(line.split("  ")) != 2 or line.count("  ") != 1:
        sys.exit(f"line {i}: malformed separator (must be exactly two spaces): {line!r}")
    digest, name = line.split("  ")
    if not re.fullmatch(r"[0-9a-f]{64}", digest):
        sys.exit(f"line {i}: digest is not exactly 64 lowercase hex characters: {digest!r}")
    if name != name.strip() or name in ("", ".", ".."):
        sys.exit(f"line {i}: name is not a bare filename: {name!r}")
    if "/" in name or "\\" in name or name.startswith(".") or ".." in name or name.startswith("/"):
        sys.exit(f"line {i}: unsafe path in checksum entry: {name!r}")
    if name not in arch_set:
        sys.exit(f"line {i}: unexpected checksum filename: {name!r}")
    if name in seen:
        sys.exit(f"line {i}: duplicate checksum entry for {name!r}")
    seen[name] = digest

missing = arch_set - set(seen)
if missing:
    sys.exit(f"checksums.txt omits expected archives: {sorted(missing)}")
print("checksums.txt: structurally valid (six distinct expected archives, bare names, 64-hex digests)")
PY

(cd "$WORK" && sha256sum -c checksums.txt)

# 4. Attestation gate: every archive must carry valid build provenance bound to
#    the canonical repository, source ref (the tag), source commit and the
#    release.yml signer workflow, using the real gh attestation verify policy
#    flags. Failure exits non-zero. The source commit is resolved from the
#    canonical repository and validated as exactly 40 lowercase hex digits.
COMMIT="$(gh api "repos/$CANONICAL_REPO/commits/$TAG" --jq .sha)"
printf '%s\n' "$COMMIT" | LC_ALL=C grep -q '^[0-9a-f]\{40\}$' \
  || { echo "could not resolve $TAG to a valid 40-hex commit (got '$COMMIT')" >&2; exit 1; }
for a in $ARCHIVES; do
  if ! gh attestation verify "$WORK/$a" \
    --repo "$CANONICAL_REPO" \
    --source-ref "refs/tags/$TAG" \
    --source-digest "$COMMIT" \
    --signer-workflow "$CANONICAL_SIGNER" >/dev/null 2>&1; then
    echo "attestation verification FAILED for $a" >&2
    exit 1
  fi
done

echo "github release verified: $CANONICAL_REPO $TAG (commit $COMMIT)"
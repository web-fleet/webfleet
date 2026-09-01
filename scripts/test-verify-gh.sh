#!/bin/sh
# test-verify-gh.sh proves scripts/verify-gh-release.sh against a mocked `gh`
# command, before any real GitHub Release exists. It builds a local dist tree
# with the exact asset set, then exercises valid and adversarial cases.
set -eu
cd "$(dirname "$0")/.."

./scripts/release.sh >/dev/null 2>&1
DIST="$PWD/dist"
VERSION="$(python3 -c "import json;print(json.load(open('$DIST/provenance.json'))['version'])")"
COMMIT="$(python3 -c "import json;print(json.load(open('$DIST/provenance.json'))['commit'])")"

FAKEBIN="$(mktemp -d)"
FLOG="$(mktemp)"
trap 'rm -rf "$FAKEBIN" "$FLOG"' EXIT

cat > "$FAKEBIN/gh" <<'EOF'
#!/bin/sh
# A mocked gh that simulates a GitHub Release. Behavior is controlled by:
#   FAKE_ASSETS    newline-separated release asset list
#   FAKE_DIST      directory whose files are served by release download
#   FAKE_EXPECT_REPO / FAKE_EXPECT_REF / FAKE_EXPECT_SHA
#   FAKE_LOG       file receiving attestation verify args (for binding proof)
case "$1" in
  release)
    case "$2" in
      view)
        printf '%s\n' $FAKE_ASSETS
        exit 0
        ;;
      download)
        dir=""
        while [ $# -gt 2 ]; do
          [ "$3" = "--dir" ] && { dir="$4"; break; }
          shift
        done
        for f in "$FAKE_DIST"/*; do cp "$f" "$dir"/; done
        exit 0
        ;;
    esac
    ;;
  attestation)
    # verify <file> --repo R --ref T --sha S ...
    printf '%s\n' "$*" >> "$FAKE_LOG"
    repo=""; ref=""; sha=""
    while [ $# -gt 1 ]; do
      case "$2" in
        --repo) repo="$3"; shift 2 ;;
        --ref) ref="$3"; shift 2 ;;
        --sha) sha="$3"; shift 2 ;;
        *) shift ;;
      esac
    done
    [ "$repo" = "$FAKE_EXPECT_REPO" ] && [ "$ref" = "$FAKE_EXPECT_REF" ] && [ "$sha" = "$FAKE_EXPECT_SHA" ] || {
      echo "attestation verification failed" >&2; exit 1
    }
    exit 0
    ;;
esac
exit 1
EOF
chmod +x "$FAKEBIN/gh"

ASSETS="$(cd "$DIST" && ls | tr '\n' ' ')"

run() { # run <expected_repo> <assets> <repo_arg>
  set +e
  FAKE_ASSETS="$2" FAKE_DIST="$DIST" FAKE_EXPECT_REPO="$1" FAKE_EXPECT_REF="$VERSION" FAKE_EXPECT_SHA="$COMMIT" \
    FAKE_LOG="$FLOG" PATH="$FAKEBIN:$PATH" \
    ./scripts/verify-gh-release.sh "$3" "$VERSION" "$VERSION" >/dev/null 2>&1
  rc=$?
  set -e
  echo $rc
}

fail=0
check() { # check <desc> <got> <want>
  if [ "$2" != "$3" ]; then echo "FAIL: $1 (got $2 want $3)" >&2; fail=1; else echo "ok: $1"; fi
}

: > "$FLOG"  # reset the attestation log for the valid case
check "valid release verifies" "$(run web-fleet/webfleet "$ASSETS" web-fleet/webfleet)" 0

# Attestation binding proof: the fake records the verify args; assert they carry
# repo/ref/sha for every archive.
COUNT="$(wc -l < "$FLOG")"
check "all six archives attested" "$COUNT" 6
grep -q -- "--repo web-fleet/webfleet --ref $VERSION --sha $COMMIT" "$FLOG" || { echo "FAIL: attestation args do not bind repo/ref/sha" >&2; fail=1; }

check "missing asset rejected" "$(run web-fleet/webfleet "SHA256SUMS provenance.json" web-fleet/webfleet)" 1
check "extra asset rejected" "$(run web-fleet/webfleet "$ASSETS unexpected.tar.gz" web-fleet/webfleet)" 1
check "wrong repository rejected" "$(run web-fleet/webfleet "$ASSETS" other/org)" 1

[ "$fail" -eq 0 ] && echo "verify-gh-release contract tests: PASS" || { echo "verify-gh-release contract tests: FAIL" >&2; exit 1; }
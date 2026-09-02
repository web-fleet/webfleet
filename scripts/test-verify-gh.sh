#!/bin/sh
# test-verify-gh.sh proves scripts/verify-gh-release.sh against a mocked `gh`
# that implements the REAL gh attestation verify policy-flag interface
# (--repo, --source-ref, --source-digest, --signer-workflow), before any real
# GitHub Release exists. Cases: valid, missing/extra asset, missing/invalid
# attestation, wrong repository, wrong source ref, wrong source digest, wrong
# signer workflow, and all-six-archives binding.
set -eu
cd "$(dirname "$0")/.."

./scripts/release.sh >/dev/null 2>&1
DIST="$PWD/dist"
VERSION="$(python3 -c "import json;print(json.load(open('$DIST/provenance.json'))['version'])")"
COMMIT="$(python3 -c "import json;print(json.load(open('$DIST/provenance.json'))['commit'])")"
REPO="web-fleet/webfleet"
WORKFLOW="$REPO/.github/workflows/ci.yml"

FAKEBIN="$(mktemp -d)"
FLOG="$(mktemp)"
trap 'rm -rf "$FAKEBIN" "$FLOG"' EXIT

cat > "$FAKEBIN/gh" <<'EOF'
#!/bin/sh
# Mocked gh implementing the real attestation-verify policy flags.
# Env: FAKE_ASSETS, FAKE_DIST, FAKE_EXPECT_REPO/REF/SHA/WF, FAKE_LOG,
#      FAKE_FAIL_FILE, FAKE_ATTEST (bad => all fail).
case "$1" in
  release)
    case "$2" in
      view) printf '%s\n' $FAKE_ASSETS; exit 0 ;;
      download)
        dir=""
        while [ $# -gt 2 ]; do
          [ "$3" = "--dir" ] && { dir="$4"; break; }
          shift
        done
        for f in "$FAKE_DIST"/*; do cp "$f" "$dir"/; done
        exit 0 ;;
    esac ;;
  attestation)
    # verify <file> --repo R --source-ref X --source-digest D --signer-workflow W
    printf '%s\n' "$* FF=$FAKE_FAIL_FILE" >> "$FAKE_LOG"
    [ "${FAKE_ATTEST:-ok}" = "ok" ] || { echo "attestation verification failed" >&2; exit 1; }
    file="$3"
    repo=""; sref=""; sdg=""; swf=""
    shift 3
    while [ $# -gt 0 ]; do
      case "$1" in
        --repo) repo="$2"; shift 2 ;;
        --source-ref) sref="$2"; shift 2 ;;
        --source-digest) sdg="$2"; shift 2 ;;
        --signer-workflow) swf="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    if [ "$repo" != "$FAKE_EXPECT_REPO" ] || [ "$sref" != "$FAKE_EXPECT_REF" ] \
       || [ "$sdg" != "$FAKE_EXPECT_SHA" ] || [ "$swf" != "$FAKE_EXPECT_WF" ]; then
      echo "attestation verification failed" >&2; exit 1
    fi
    if [ -n "${FAKE_FAIL_FILE:-}" ]; then
      case "$file" in *"$FAKE_FAIL_FILE"*) echo "attestation verification failed" >&2; exit 1 ;; esac
    fi
    exit 0 ;;
esac
exit 1
EOF
chmod +x "$FAKEBIN/gh"

ASSETS="$(cd "$DIST" && ls | tr '\n' ' ')"

# run <repo_arg> <tag_arg> <version> <assets> <expect_sha> <expect_wf> <fail_file> [attest]
run() {
  set +e
  FAKE_ASSETS="$4" FAKE_DIST="$DIST" \
    FAKE_EXPECT_REPO="$REPO" FAKE_EXPECT_REF="refs/tags/$VERSION" FAKE_EXPECT_SHA="$5" FAKE_EXPECT_WF="$6" \
    FAKE_LOG="$FLOG" FAKE_FAIL_FILE="$7" FAKE_ATTEST="${8:-ok}" PATH="$FAKEBIN:$PATH" \
    ./scripts/verify-gh-release.sh "$1" "$2" "$3" >/dev/null 2>&1
  rc=$?
  set -e
  echo $rc
}

fail=0
check() { if [ "$2" != "$3" ]; then echo "FAIL: $1 (got $2 want $3)" >&2; fail=1; else echo "ok: $1"; fi; }

: > "$FLOG"
check "valid release verifies" "$(run "$REPO" "$VERSION" "$VERSION" "$ASSETS" "$COMMIT" "$WORKFLOW" "")" 0
COUNT="$(wc -l < "$FLOG")"
check "all six archives attested" "$COUNT" 6
grep -q -- "--repo $REPO --source-ref refs/tags/$VERSION --source-digest $COMMIT --signer-workflow $WORKFLOW" "$FLOG" \
  || { echo "FAIL: attestation flags do not use the real CLI vocabulary" >&2; fail=1; }

check "missing asset rejected" "$(run "$REPO" "$VERSION" "$VERSION" "SHA256SUMS provenance.json" "$COMMIT" "$WORKFLOW" "")" 1
check "extra asset rejected" "$(run "$REPO" "$VERSION" "$VERSION" "$ASSETS unexpected.tar.gz" "$COMMIT" "$WORKFLOW" "")" 1
check "wrong repository rejected" "$(run "other/org" "$VERSION" "$VERSION" "$ASSETS" "$COMMIT" "$WORKFLOW" "")" 1
check "wrong source ref rejected" "$(run "$REPO" "wrongtag" "$VERSION" "$ASSETS" "$COMMIT" "$WORKFLOW" "")" 1
check "wrong source digest rejected" "$(run "$REPO" "$VERSION" "$VERSION" "$ASSETS" "0000000000000000000000000000000000000000" "$WORKFLOW" "")" 1
check "wrong signer workflow rejected" "$(run "$REPO" "$VERSION" "$VERSION" "$ASSETS" "$COMMIT" "other/org/.github/workflows/x.yml" "")" 1
check "missing attestation fails verifier" "$(run "$REPO" "$VERSION" "$VERSION" "$ASSETS" "$COMMIT" "$WORKFLOW" "windows_arm64")" 1
check "bad attestation fails verifier" "$(run "$REPO" "$VERSION" "$VERSION" "$ASSETS" "$COMMIT" "$WORKFLOW" "" "bad")" 1

rm -rf "$DIST"
[ "$fail" -eq 0 ] && echo "verify-gh-release contract tests: PASS" || { echo "verify-gh-release contract tests: FAIL" >&2; exit 1; }
#!/bin/sh
# test-verify-gh.sh proves scripts/verify-gh-release.sh against a mocked `gh`
# that implements the REAL gh attestation verify policy-flag interface
# (--repo, --source-ref, --source-digest, --signer-workflow), before any real
# GitHub Release exists.
#
# The asset contract under test is the authoritative release.yml/build-release.sh
# contract: six dash-named, non-versioned archives plus checksums.txt.
#
# Security boundaries proven:
#   - repository identity is IMMUTABLE: only webfleet-cv/webfleet is accepted,
#     and caller input can never change the signer workflow;
#   - checksums.txt is structurally validated (six distinct bare expected
#     archives, 64-lowercase-hex digests, exactly two-space separators) before
#     byte verification;
#   - attestations are verified against the canonical repo/ref/digest/signer.
set -eu
cd "$(dirname "$0")/.."

./scripts/build-release.sh v0.0.0-test >/dev/null 2>&1
DIST="$PWD/dist"
COMMIT="0000000000000000000000000000000000000000"
REPO="webfleet-cv/webfleet"
WORKFLOW="$REPO/.github/workflows/release.yml"

# The exact set release.yml publishes (checksums.txt + six dash-named archives).
ASSETS="checksums.txt webfleet-linux-amd64.tar.gz webfleet-linux-arm64.tar.gz webfleet-darwin-amd64.tar.gz webfleet-darwin-arm64.tar.gz webfleet-windows-amd64.zip webfleet-windows-arm64.zip"

FAKEBIN="$(mktemp -d)"
FLOG="$(mktemp)"
WORKTMP="$(mktemp -d)"
trap 'rm -rf "$FAKEBIN" "$FLOG" "$DIST" "$WORKTMP"' EXIT

cat > "$FAKEBIN/gh" <<'EOF'
#!/bin/sh
# Mocked gh implementing the real attestation-verify policy flags.
# Env: FAKE_ASSETS, FAKE_DIST, FAKE_EXPECT_REPO/REF/SHA/WF, FAKE_LOG,
#      FAKE_FAIL_FILE, FAKE_ATTEST (bad => all fail), FAKE_COMMIT,
#      FAKE_SUMS (overrides checksums.txt content).
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
        for f in "$FAKE_DIST"/*; do
          b=$(basename "$f")
          if [ "${FAKE_SUMS_OVERRIDE:-0}" = "1" ] && [ "$b" = "checksums.txt" ]; then
            printf '%s' "$FAKE_SUMS" > "$dir/$b"
          else
            cp "$f" "$dir"/
          fi
        done
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
  api)
    # repos/<owner>/<repo>/commits/<tag> --jq .sha -> FAKE_COMMIT
    printf '%s\n' "$FAKE_COMMIT"
    exit 0 ;;
esac
exit 1
EOF
chmod +x "$FAKEBIN/gh"

# Good six-archive checksum content derived from the real dist tree.
GOOD_SUMS=""
for a in webfleet-linux-amd64.tar.gz webfleet-linux-arm64.tar.gz webfleet-darwin-amd64.tar.gz webfleet-darwin-arm64.tar.gz webfleet-windows-amd64.zip webfleet-windows-arm64.zip; do
  h=$(sha256sum "$DIST/$a" | cut -d' ' -f1)
  GOOD_SUMS="${GOOD_SUMS}${h}  $a
"
done

TAG="v0.0.0-test"

# run <repo_arg> <tag_arg> <assets> <expect_sha> <expect_wf> <fail_file> [attest] [sums] [force_sums]
run() {
  set +e
  FAKE_ASSETS="$3" FAKE_DIST="$DIST" \
    FAKE_EXPECT_REPO="$REPO" FAKE_EXPECT_REF="refs/tags/$TAG" FAKE_EXPECT_SHA="$4" FAKE_EXPECT_WF="$5" \
    FAKE_LOG="$FLOG" FAKE_FAIL_FILE="$6" FAKE_ATTEST="${7:-ok}" FAKE_COMMIT="$COMMIT" \
    FAKE_SUMS_OVERRIDE="${9:-0}" FAKE_SUMS="${8:-}" PATH="$FAKEBIN:$PATH" \
    ./scripts/verify-gh-release.sh "$1" "$2" >/dev/null 2>&1
  rc=$?
  set -e
  echo $rc
}

fail=0
check() { if [ "$2" != "$3" ]; then echo "FAIL: $1 (got $2 want $3)" >&2; fail=1; else echo "ok: $1"; fi; }
# fails <name> <rc>: passes when rc is any non-zero (usage guard exits 2).
fails() { if [ "$2" -eq 0 ]; then echo "FAIL: $1 (got 0 want non-zero)" >&2; fail=1; else echo "ok: $1"; fi; }

# --- Repository-identity immutability ---------------------------------------
: > "$FLOG"
check "canonical repository succeeds" "$(run "$REPO" "$TAG" "$ASSETS" "$COMMIT" "$WORKFLOW" "" ok "$GOOD_SUMS")" 0
grep -q -- "--repo $REPO --source-ref refs/tags/$TAG --source-digest $COMMIT --signer-workflow $WORKFLOW" "$FLOG" \
  || { echo "FAIL: canonical attestation flags do not use the real CLI vocabulary" >&2; fail=1; }
COUNT="$(wc -l < "$FLOG")"
check "all six archives attested" "$COUNT" 6

: > "$FLOG"
set +e
rc=$(FAKE_ASSETS="$ASSETS" FAKE_DIST="$DIST" FAKE_EXPECT_REPO="$REPO" FAKE_EXPECT_REF="refs/tags/$TAG" FAKE_EXPECT_SHA="$COMMIT" FAKE_EXPECT_WF="$WORKFLOW" FAKE_LOG="$FLOG" FAKE_ATTEST=ok FAKE_COMMIT="$COMMIT" FAKE_SUMS_OVERRIDE=1 FAKE_SUMS="$GOOD_SUMS" PATH="$FAKEBIN:$PATH" sh -c './scripts/verify-gh-release.sh "" "$TAG" >/dev/null 2>&1'; echo $?)
set -e
fails "omitted repository fails" "$rc"
check "alternate repository fails" "$(run "other/org/webfleet" "$TAG" "$ASSETS" "$COMMIT" "$WORKFLOW" "" ok "$GOOD_SUMS")" 1
check "alternate org same repo name fails" "$(run "evil-corp/webfleet" "$TAG" "$ASSETS" "$COMMIT" "$WORKFLOW" "" ok "$GOOD_SUMS")" 1
check "malformed repository fails" "$(run "webfleet-cv" "$TAG" "$ASSETS" "$COMMIT" "$WORKFLOW" "" ok "$GOOD_SUMS")" 1

# Caller input cannot alter the signer workflow: an alternate repo must fail
# even when the mock would otherwise accept its signer.
check "alternate repo rejected before attestation" \
  "$(FAKE_EXPECT_REPO="evil-corp/webfleet" FAKE_EXPECT_WF="evil-corp/webfleet/.github/workflows/release.yml" run "evil-corp/webfleet" "$TAG" "$ASSETS" "$COMMIT" "evil-corp/webfleet/.github/workflows/release.yml" "" ok "$GOOD_SUMS")" 1

# --- Tag validation ---------------------------------------------------------
check "invalid tag rejected" "$(run "$REPO" "not-a-tag" "$ASSETS" "$COMMIT" "$WORKFLOW" "" ok "$GOOD_SUMS")" 1
check "hostile tag rejected" "$(run "$REPO" 'v0.0.0"; rm -rf /' "$ASSETS" "$COMMIT" "$WORKFLOW" "" ok "$GOOD_SUMS")" 1
check "invalid commit rejected" "$(FAKE_COMMIT="not-hex" run "$REPO" "$TAG" "$ASSETS" "not-hex" "$WORKFLOW" "" ok "$GOOD_SUMS")" 1

# --- Checksum structural validation ----------------------------------------
# Missing archive entry
MISSING_SUMS=$(printf '%s' "$GOOD_SUMS" | grep -v "linux-arm64.tar.gz")
check "checksum missing archive rejected" "$(run "$REPO" "$TAG" "$ASSETS" "$COMMIT" "$WORKFLOW" "" ok "$MISSING_SUMS" 1)" 1
# Duplicate entry (one archive listed twice, one omitted)
DUP_SUMS=$(printf '%s' "$GOOD_SUMS" | grep -v "linux-arm64.tar.gz"; printf '%s\n' "$GOOD_SUMS" | grep "linux-amd64.tar.gz")
check "checksum duplicate entry rejected" "$(run "$REPO" "$TAG" "$ASSETS" "$COMMIT" "$WORKFLOW" "" ok "$DUP_SUMS" 1)" 1
# Repeated single archive six times (substitute all with the first archive)
ONE=$(printf '%s' "$GOOD_SUMS" | grep "webfleet-linux-amd64.tar.gz")
REPEAT_SUMS=$(printf '%s%.0s' "$ONE" 1 2 3 4 5 6)
check "checksum repeated single archive rejected" "$(run "$REPO" "$TAG" "$ASSETS" "$COMMIT" "$WORKFLOW" "" ok "$REPEAT_SUMS" 1)" 1
# Unexpected filename
UNEXPECTED_SUMS=$(printf '%s' "$GOOD_SUMS" | sed 's/webfleet-darwin-amd64.tar.gz/evil.tar.gz/')
check "checksum unexpected filename rejected" "$(run "$REPO" "$TAG" "$ASSETS" "$COMMIT" "$WORKFLOW" "" ok "$UNEXPECTED_SUMS" 1)" 1
# Traversal path
TRAVERSAL_SUMS=$(printf '%s' "$GOOD_SUMS" | sed 's/webfleet-darwin-amd64.tar.gz/..\/webfleet-linux-amd64.tar.gz/')
check "checksum traversal path rejected" "$(run "$REPO" "$TAG" "$ASSETS" "$COMMIT" "$WORKFLOW" "" ok "$TRAVERSAL_SUMS" 1)" 1
# Absolute path
ABSOLUTE_SUMS=$(printf '%s' "$GOOD_SUMS" | sed 's/webfleet-darwin-amd64.tar.gz/\/tmp\/webfleet-linux-amd64.tar.gz/')
check "checksum absolute path rejected" "$(run "$REPO" "$TAG" "$ASSETS" "$COMMIT" "$WORKFLOW" "" ok "$ABSOLUTE_SUMS" 1)" 1
# Malformed digest (uppercase / short)
MALFORMED_DIGEST_SUMS=$(printf '%s' "$GOOD_SUMS" | sed '0,/^\([0-9a-f]\{64\}\)/s//ABCDEF/' )
check "checksum malformed digest rejected" "$(run "$REPO" "$TAG" "$ASSETS" "$COMMIT" "$WORKFLOW" "" ok "$MALFORMED_DIGEST_SUMS" 1)" 1
# Malformed separator (single space)
MALFORMED_SEP_SUMS=$(printf '%s' "$GOOD_SUMS" | sed 's/  / /g')
check "checksum malformed separator rejected" "$(run "$REPO" "$TAG" "$ASSETS" "$COMMIT" "$WORKFLOW" "" ok "$MALFORMED_SEP_SUMS" 1)" 1
# Digest mismatch (wrong checksum for an archive)
WRONG=$(printf '%064d' 0)
MISMATCH_SUMS=$(printf '%s' "$GOOD_SUMS" | sed "0,/^\([0-9a-f]\{64\}\)  webfleet-linux-amd64.tar.gz/s//$WRONG  webfleet-linux-amd64.tar.gz/")
check "checksum digest mismatch rejected" "$(run "$REPO" "$TAG" "$ASSETS" "$COMMIT" "$WORKFLOW" "" ok "$MISMATCH_SUMS" 1)" 1
# Empty checksums
check "empty checksums rejected" "$(run "$REPO" "$TAG" "$ASSETS" "$COMMIT" "$WORKFLOW" "" ok "" 1)" 1

# --- Asset-set and attestation boundaries (unchanged contract) --------------
check "missing asset rejected" "$(run "$REPO" "$TAG" "checksums.txt" "$COMMIT" "$WORKFLOW" "" ok "$GOOD_SUMS")" 1
check "extra asset rejected" "$(run "$REPO" "$TAG" "$ASSETS unexpected.tar.gz" "$COMMIT" "$WORKFLOW" "" ok "$GOOD_SUMS")" 1
check "wrong source ref rejected" "$(run "$REPO" "wrongtag" "$ASSETS" "$COMMIT" "$WORKFLOW" "" ok "$GOOD_SUMS")" 1
check "wrong source digest rejected" "$(run "$REPO" "$TAG" "$ASSETS" "1111111111111111111111111111111111111111" "$WORKFLOW" "" ok "$GOOD_SUMS")" 1
check "wrong signer workflow rejected" "$(run "$REPO" "$TAG" "$ASSETS" "$COMMIT" "other/org/.github/workflows/x.yml" "" ok "$GOOD_SUMS")" 1
check "missing attestation fails verifier" "$(run "$REPO" "$TAG" "$ASSETS" "$COMMIT" "$WORKFLOW" "windows-arm64" ok "$GOOD_SUMS")" 1
check "bad attestation fails verifier" "$(run "$REPO" "$TAG" "$ASSETS" "$COMMIT" "$WORKFLOW" "" bad "$GOOD_SUMS")" 1

[ "$fail" -eq 0 ] && echo "verify-gh-release contract tests: PASS" || { echo "verify-gh-release contract tests: FAIL" >&2; exit 1; }
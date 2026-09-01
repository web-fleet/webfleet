#!/bin/sh
# verify-release.sh is a read-only release verifier: it checks that every file
# listed in SHA256SUMS exists with the recorded checksum and that the provenance
# manifest is present and consistent with the checksums. A verifier needs no
# write permission anywhere; it only reads the release directory.
set -eu

DIST="${1:-dist}"
cd "$DIST"

[ -f SHA256SUMS ] || { echo "missing SHA256SUMS in $DIST" >&2; exit 1; }
[ -f provenance.json ] || { echo "missing provenance.json in $DIST" >&2; exit 1; }

fail=0
# Every archive must exist and match its recorded SHA-256.
while read -r want file; do
  [ -n "$file" ] || continue
  if [ ! -f "$file" ]; then
    echo "missing artifact: $file" >&2
    fail=1
    continue
  fi
  got="$(sha256sum "$file" | cut -d' ' -f1)"
  if [ "$got" != "$want" ]; then
    echo "checksum mismatch: $file (got $got want $want)" >&2
    fail=1
  fi
done < SHA256SUMS

# The provenance manifest must reference the same set of files with the same
# checksums.
if command -v python3 >/dev/null 2>&1; then
  python3 - "$PWD" <<'PY'
import json, hashlib, pathlib, sys
root = pathlib.Path(sys.argv[1])
prov = json.loads((root / "provenance.json").read_text())
files = prov["files"]
sums = (root / "SHA256SUMS").read_text().splitlines()
for line in sums:
    h, _, name = line.partition(" ")
    name = name.strip()
    if name and files.get(name) != h:
        print(f"provenance mismatch: {name}", file=sys.stderr)
        raise SystemExit(1)
# No extra/missing files between manifest and checksum list.
if len(files) != len([l for l in sums if l.strip()]):
    print("file count mismatch between provenance and SHA256SUMS", file=sys.stderr)
    raise SystemExit(1)
PY
  fail=$((fail | $?))
fi

if [ "$fail" -eq 0 ]; then
  echo "release verified: $DIST"
else
  echo "release verification FAILED" >&2
  exit 1
fi
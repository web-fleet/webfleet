#!/bin/sh
# verify-release.sh is a read-only release verifier. It enforces the exact
# six-archive matrix for the recorded version, rejects missing/extra/duplicate/
# malformed assets, and checks checksum agreement between SHA256SUMS and the
# provenance manifest. A verifier needs no write permission anywhere.
set -eu

DIST="${1:-dist}"
cd "$DIST"

[ -f SHA256SUMS ] || { echo "missing SHA256SUMS in $DIST" >&2; exit 1; }
[ -f provenance.json ] || { echo "missing provenance.json in $DIST" >&2; exit 1; }

python3 - "$PWD" <<'PY'
import json, pathlib, re, sys

root = pathlib.Path(sys.argv[1])
prov = json.loads((root / "provenance.json").read_text())
version = prov.get("version", "")
commit = prov.get("commit", "")
files = prov.get("files", {})

expected = {
    "linux_amd64": "tar.gz", "linux_arm64": "tar.gz",
    "darwin_amd64": "tar.gz", "darwin_arm64": "tar.gz",
    "windows_amd64": "zip", "windows_arm64": "zip",
}

if not version:
    print("provenance.json has no version", file=sys.stderr); sys.exit(1)

# Parse SHA256SUMS into a list, rejecting duplicates and path-like names.
sums = []
seen = set()
for line in (root / "SHA256SUMS").read_text().splitlines():
    line = line.strip()
    if not line:
        continue
    parts = line.split()
    if len(parts) != 2 or not re.fullmatch(r"[0-9a-f]{64}", parts[0]):
        print(f"malformed SHA256SUMS line: {line!r}", file=sys.stderr); sys.exit(1)
    name = parts[1]
    if "/" in name or ".." in name or name in ("", ".", "SHA256SUMS", "provenance.json"):
        print(f"invalid asset name: {name!r}", file=sys.stderr); sys.exit(1)
    if name in seen:
        print(f"duplicate SHA256SUMS entry: {name}", file=sys.stderr); sys.exit(1)
    seen.add(name)
    sums.append((name, parts[0]))

# The exact archive set for the version, no more and no fewer.
want = set()
for target, ext in expected.items():
    osname, arch = target.rsplit("_", 1)
    want.add(f"webfleet_{version}_{osname}_{arch}.{ext}")
got = {name for name, _ in sums}
missing = want - got
extra = got - want
if missing:
    print(f"missing expected archives: {sorted(missing)}", file=sys.stderr); sys.exit(1)
if extra:
    print(f"unexpected archives present: {sorted(extra)}", file=sys.stderr); sys.exit(1)
if got != set(files):
    print(f"provenance file set differs from SHA256SUMS", file=sys.stderr); sys.exit(1)

# Every archive must exist, match its recorded checksum, and agree with
# provenance.
for name, want_sum in sums:
    if files.get(name) != want_sum:
        print(f"provenance/checksum disagreement for {name}", file=sys.stderr); sys.exit(1)
    p = root / name
    if not p.is_file():
        print(f"missing artifact: {name}", file=sys.stderr); sys.exit(1)
    import hashlib
    got_sum = hashlib.sha256(p.read_bytes()).hexdigest()
    if got_sum != want_sum:
        print(f"checksum mismatch: {name}", file=sys.stderr); sys.exit(1)

print(f"release verified: {root} (version {version}, commit {commit})")
PY
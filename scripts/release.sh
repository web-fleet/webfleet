#!/bin/sh
# release.sh builds the Web Fleet release matrix, produces checksums and a
# provenance manifest. It never publishes a release or tag; publishing is a
# separate, deliberate step.
#
# Usage:
#   scripts/release.sh            # build the full matrix into dist/
#   scripts/release.sh --version v0.1.0   # pin the version string
#   scripts/release.sh --verify   # verify an existing dist/ tree (read-only)
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
DIST="${DIST:-$ROOT/dist}"

if [ "${1:-}" = "--verify" ]; then
  exec scripts/verify-release.sh "$DIST"
fi

VERSION="${VERSION:-}"
if [ -z "$VERSION" ]; then
  VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
fi
# Release archive names must not contain '/' or spaces.
VERSION="$(echo "$VERSION" | tr '/ ' '--')"
COMMIT="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
GOVER="$(go version | awk '{print $3}')"

rm -rf "$DIST"
mkdir -p "$DIST"

# os/arch/ext matrix. Native binaries first for the host, then the matrix.
MATRIX="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"
# Prefer GOOS/GOARCH for a single-target build if provided.
if [ -n "${GOOS:-}" ] && [ -n "${GOARCH:-}" ]; then
  MATRIX="$GOOS/$GOARCH"
fi

for target in $MATRIX; do
  os="${target%/*}"
  arch="${target#*/}"
  ext=""
  [ "$os" = "windows" ] && ext=".exe"
  dir="$DIST/webfleet_${VERSION}_${os}_${arch}"
  mkdir -p "$dir"
  echo "building $os/$arch"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
    -o "$dir/webfleet$ext" ./cmd/webfleet
  if [ -f LICENSE ]; then cp LICENSE "$dir/"; fi
  case "$os" in
    windows) (cd "$DIST" && zip -qr "webfleet_${VERSION}_${os}_${arch}.zip" "webfleet_${VERSION}_${os}_${arch}") ;;
    *) (cd "$DIST" && tar -czf "webfleet_${VERSION}_${os}_${arch}.tar.gz" "webfleet_${VERSION}_${os}_${arch}") ;;
  esac
  rm -rf "$dir"
done

cd "$DIST"
sha256sum webfleet_* > SHA256SUMS

{
  printf '{\n'
  printf '  "version": "%s",\n' "$VERSION"
  printf '  "commit": "%s",\n' "$COMMIT"
  printf '  "go": "%s",\n' "$GOVER"
  printf '  "builtAt": "%s",\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf '  "matrix": "%s",\n' "$MATRIX"
  printf '  "files": {\n'
  first=1
  for f in webfleet_*.*; do
    [ $first -eq 1 ] || printf ',\n'
    first=0
    printf '    "%s": "%s"' "$f" "$(sha256sum "$f" | cut -d' ' -f1)"
  done
  printf '\n  }\n}\n'
} > provenance.json
echo "done: $DIST"
echo "version: $VERSION"
cat "$DIST/SHA256SUMS"
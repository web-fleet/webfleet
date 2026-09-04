#!/bin/sh
set -eu
version=${1:?usage: build-release.sh VERSION [OUTPUT]}
output=${2:-dist}
case "$version" in v[0-9]*.[0-9]*.[0-9]*) ;; *) echo "version must be vMAJOR.MINOR.PATCH" >&2; exit 2;; esac
case "$output" in ''|/|.|..) echo "unsafe output directory" >&2; exit 2;; esac
mkdir -p "$output"
find "$output" -mindepth 1 -maxdepth 1 -delete
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}; arch=${target#*/}; ext=; [ "$os" = windows ] && ext=.exe
  stage=$(mktemp -d); stem="webfleet-${os}-${arch}"
  trap 'rm -rf "$stage"' EXIT HUP INT TERM
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=${version#v}" -o "$stage/webfleet$ext" ./cmd/webfleet
  cp LICENSE README.md "$stage/"
  if [ "$os" = windows ]; then (cd "$stage" && zip -Xq "$OLDPWD/$output/$stem.zip" "webfleet$ext" LICENSE README.md); else tar --sort=name --owner=0 --group=0 --numeric-owner -C "$stage" -czf "$output/$stem.tar.gz" webfleet LICENSE README.md; fi
  rm -rf "$stage"; trap - EXIT HUP INT TERM
done
(cd "$output" && sha256sum webfleet-* > checksums.txt && sha256sum -c checksums.txt)
test "$(find "$output" -maxdepth 1 -type f | wc -l)" -eq 7
echo "verified six Web Fleet release archives for $version"

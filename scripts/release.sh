#!/usr/bin/env bash
# Build release archives of the verity CLI for every supported platform.
#
#   scripts/release.sh <version> [out-dir]
#
# Produces <out-dir>/verity_<version>_<os>_<arch>.tar.gz (.zip on Windows),
# each holding the static binary plus LICENSE and README.md, the same for the
# gRPC protocol plugin (verity-grpc_<version>_<os>_<arch>, binary
# verity-grpc, from the plugins/grpc module), and a SHA256SUMS file over all
# archives. Builds are reproducible: CGO off,
# -trimpath, and the commit date (not the wall clock) as the build date.
# Used by .github/workflows/release.yml;
# see docs/releasing.md.
set -euo pipefail

version=${1:?usage: scripts/release.sh <version> [out-dir]}
out=${2:-dist/release}
commit=$(git rev-parse --short=12 HEAD 2>/dev/null || echo none)
date=$(git log -1 --format=%cI 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)
targets=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64)

rm -rf "$out"
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# archive <name> <os>: pack $work/<name> into $out, reproducibly.
archive() {
  local name=$1 os=$2
  cp LICENSE "$work/$name/"
  touch -d "$date" "$work/$name" "$work/$name"/*  # stable archive timestamps
  if [ "$os" = windows ]; then
    if command -v zip >/dev/null; then
      (cd "$work" && zip -qr "$out/$name.zip" "$name")
    else
      (cd "$work" && python3 -m zipfile -c "$out/$name.zip" "$name")
    fi
  else
    tar -C "$work" --owner=0 --group=0 --mtime="$date" -czf "$out/$name.tar.gz" "$name"
  fi
}

ldflags="-s -w -X main.version=$version -X main.commit=$commit -X main.date=$date"
for t in "${targets[@]}"; do
  os=${t%/*} arch=${t#*/}
  ext=""
  [ "$os" = windows ] && ext=.exe

  name="verity_${version}_${os}_${arch}"
  mkdir -p "$work/$name"
  echo "building $name"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$ldflags" \
    -o "$work/$name/verity$ext" ./cmd/verity
  cp README.md "$work/$name/"
  archive "$name" "$os"

  name="verity-grpc_${version}_${os}_${arch}"
  mkdir -p "$work/$name"
  echo "building $name"
  (cd plugins/grpc && CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$ldflags" \
    -o "$work/$name/verity-grpc$ext" .)
  cp plugins/grpc/README.md "$work/$name/"
  archive "$name" "$os"
done

(cd "$out" && sha256sum -- * > SHA256SUMS)
echo "wrote $(ls "$out" | wc -l) files to $out"

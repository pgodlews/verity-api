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
# -trimpath, and the commit date (not the wall clock) as the build date;
# archives list entries in sorted order with fixed owners, modes, and
# timestamps (tar.gz via GNU tar and gzip -n, zip via Python's zipfile).
# Needs GNU tar, sha256sum (coreutils), and python3; on macOS install
# gnu-tar and coreutils and put their gnubin directories first on PATH.
# Used by .github/workflows/release.yml;
# see docs/releasing.md.
set -euo pipefail

version=${1:?usage: scripts/release.sh <version> [out-dir]}

need() { echo "release.sh: $1" >&2; exit 2; }
tar --version 2>/dev/null | grep -q 'GNU tar' || need "GNU tar is required (macOS: brew install gnu-tar, then put its gnubin first on PATH)"
command -v sha256sum >/dev/null || need "sha256sum is required (macOS: brew install coreutils, then put its gnubin first on PATH)"
command -v python3 >/dev/null || need "python3 is required to write reproducible zip archives"
out=${2:-dist/release}
commit=$(git rev-parse --short=12 HEAD 2>/dev/null || echo none)
date=$(git log -1 --format=%cI 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)
targets=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64)

rm -rf "$out"
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# archive <name> <os>: pack $work/<name> into $out, reproducibly: sorted
# entries, fixed owner/mode, every timestamp set to the commit date, and no
# gzip name or time stamp.
archive() {
  local name=$1 os=$2
  cp LICENSE "$work/$name/"
  if [ "$os" = windows ]; then
    python3 - "$work" "$name" "$out/$name.zip" "$date" <<'PY'
import os, sys, zipfile
from datetime import datetime
root, name, out, date = sys.argv[1:5]
stamp = datetime.fromisoformat(date.replace("Z", "+00:00")).timetuple()[:6]
paths = []
for dirpath, dirs, files in os.walk(os.path.join(root, name)):
    dirs.sort()
    paths += [os.path.join(dirpath, f) for f in files]
with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
    for p in sorted(paths):
        info = zipfile.ZipInfo(os.path.relpath(p, root).replace(os.sep, "/"), stamp)
        info.external_attr = (0o755 if os.access(p, os.X_OK) else 0o644) << 16
        info.compress_type = zipfile.ZIP_DEFLATED
        with open(p, "rb") as f:
            z.writestr(info, f.read())
PY
  else
    tar -C "$work" --sort=name --owner=0 --group=0 --numeric-owner --mtime="$date" -cf - "$name" \
      | gzip -n -9 > "$out/$name.tar.gz"
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

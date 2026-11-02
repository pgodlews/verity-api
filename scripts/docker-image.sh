#!/usr/bin/env bash
# Publish a multi-arch (linux/amd64, linux/arm64) image of the verity CLI.
#
#   scripts/docker-image.sh <version> <image-repo> [release-dir]
#
# The image holds exactly the binaries scripts/release.sh put in the linux
# archives in <release-dir> (default dist/release), plus CA certificates. It
# is assembled with crane, with no Docker daemon and no emulation, and pushed
# as <image-repo>:<version>; a version without a pre-release suffix is also
# tagged latest. Log in first (crane auth login). Set CRANE_INSECURE=1 for a
# registry served over plain HTTP. Same layout as the Dockerfile: scratch,
# non-root (65534), workdir /work, entrypoint verity.
set -euo pipefail

version=${1:?usage: scripts/docker-image.sh <version> <image-repo> [release-dir]}
repo=${2:?usage: scripts/docker-image.sh <version> <image-repo> [release-dir]}
rel=${3:-dist/release}
certs=${CA_CERTS:-/etc/ssl/certs/ca-certificates.crt}
date=$(git log -1 --format=%cI 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)
crane=(crane)
[ "${CRANE_INSECURE:-}" = 1 ] && crane+=(--insecure)

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

manifests=()
for arch in amd64 arm64; do
  name="verity_${version}_linux_${arch}"
  root="$work/$arch/root"
  mkdir -p "$root/usr/local/bin" "$root/etc/ssl/certs" "$root/work"
  tar -xzf "$rel/$name.tar.gz" -C "$work/$arch"
  install -m 0755 "$work/$arch/$name/verity" "$root/usr/local/bin/verity"
  install -m 0644 "$certs" "$root/etc/ssl/certs/ca-certificates.crt"
  tar -C "$root" --sort=name --owner=0 --group=0 --numeric-owner --mtime="$date" \
    -cf "$work/$arch.tar" usr etc work
  # The single-arch image goes to the version tag first; the index pushed
  # below replaces that tag.
  ref=$("${crane[@]}" append --oci-empty-base -f "$work/$arch.tar" -t "$repo:$version")
  ref=$("${crane[@]}" mutate "$ref" --set-platform "linux/$arch" \
    --entrypoint /usr/local/bin/verity --cmd --help --user 65534:65534 --workdir /work \
    --label "org.opencontainers.image.version=$version" \
    --label "org.opencontainers.image.source=${IMAGE_SOURCE:-}")
  echo "linux/$arch: $ref"
  manifests+=(-m "$ref")
done

"${crane[@]}" index append "${manifests[@]}" -t "$repo:$version"
case "$version" in
  *-*) ;;
  *) "${crane[@]}" tag "$repo:$version" latest ;;
esac
echo "pushed $repo:$version"

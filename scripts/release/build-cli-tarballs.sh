#!/usr/bin/env bash
# Build the retnd command line for every platform a GitHub Release carries,
# pack each as a tarball and write one SHA256SUMS over the lot (issue #1078).
#
# usage: build-cli-tarballs.sh <tag> <commit> <outdir>
#
# Static (CGO_ENABLED=0) builds with the same flags the image build uses
# (-trimpath, -buildvcs=false, -s -w, version and commit stamped in), so a
# tarball binary answers `retnd version` the way the image's does. The
# tarballs are checksummed and NOT signed: this script has no credentials and
# the job that runs it holds no id-token.
#
# Each tarball is retnd_<tag>_<os>_<arch>.tar.gz holding one file, `retnd`.
# Entries are normalised (owner, mtime, order) so a rebuild of the same commit
# produces the same tarball bytes as far as the binary itself is stable.
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <tag> <commit> <outdir>" >&2
  exit 2
fi
tag="$1"; commit="$2"; out="$3"
case "$tag" in
  [0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "refusing tag '$tag': want a semantic version such as 0.6.0 (no leading v)" >&2; exit 2 ;;
esac

root="$(cd "$(dirname "$0")/../.." && pwd)"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

platforms="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"
for p in $platforms; do
  os="${p%/*}"; arch="${p#*/}"
  d="$work/$os-$arch"
  mkdir -p "$d"
  (cd "$root/core" && GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build \
    -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.version=${tag} -X main.commit=${commit}" \
    -o "$d/retnd" ./cmd/retnd)
  name="retnd_${tag}_${os}_${arch}.tar.gz"
  tar --sort=name --owner=0 --group=0 --numeric-owner --mtime='UTC 2000-01-01' \
    -C "$d" -cf - retnd | gzip -n > "$out/$name"
  echo "built $name"
done

(cd "$out" && sha256sum retnd_"${tag}"_*.tar.gz > SHA256SUMS)
echo "wrote $out/SHA256SUMS"

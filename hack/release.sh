#!/usr/bin/env bash
# Build the release assets for one tag into ./dist: a stripped, reproducible
# binary and a deterministic .tar.gz per target, plus SHA256SUMS over all of them.
#
#   hack/release.sh v0.1.0
#
# Reproducible means: the same commit and the same Go toolchain give the same
# bytes on any machine. Inputs that could differ are pinned or refused here:
# the Go version (exactly the one in go.mod), CGO off, -trimpath, an empty
# build id, an explicit GOAMD64/GOARM64, a clean checkout (so the VCS stamp is
# the commit, never "modified"), and archive metadata taken from the commit.
# No build date, host name or path is embedded.
#
# The tag does not have to exist yet: CI runs this with a throwaway tag to
# prove two builds hash the same. The release workflow checks that the tag
# points at HEAD before it calls this.
set -euo pipefail

die() { echo "release.sh: $*" >&2; exit 1; }

tag="${1:-}"
[[ -n "$tag" ]] || die "usage: hack/release.sh <tag>  (for example v0.1.0)"
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || die "tag must look like v1.2.3 or v1.2.3-rc.1"
ver="${tag#v}"

cd "$(git rev-parse --show-toplevel)"
[[ -z "$(git status --porcelain)" ]] || die "working tree is not clean; build from a clean checkout"
commit="$(git rev-parse HEAD)"
mtime="$(git log -1 --format=%ct HEAD)"

gover="$(awk '$1 == "go" { print $2; exit }' go.mod)"
[[ -n "$gover" ]] || die "no go directive in go.mod"
# Exactly the toolchain go.mod names, downloaded if needed; never a newer local one.
export GOTOOLCHAIN="go${gover}"
export CGO_ENABLED=0 GOFLAGS=-mod=readonly GOAMD64=v1 GOARM64=v8.0
unset GOEXPERIMENT GOFIPS140 GOWORK GOOS GOARCH

ldflags="-s -w -buildid= -X github.com/Hedronite/scry/internal/cli.version=${tag}"
targets=("linux/amd64" "linux/arm64" "darwin/arm64")

{
  echo "release.sh: tag=${tag} commit=${commit}"
  echo "release.sh: $(go version)"
  echo "release.sh: ldflags=${ldflags}"
} >&2

rm -rf dist
mkdir -p dist
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
go build -o "$work/mkarchive" ./hack/mkarchive

for t in "${targets[@]}"; do
  os="${t%/*}"; arch="${t#*/}"
  name="scry_${ver}_${os}_${arch}"
  GOOS="$os" GOARCH="$arch" go build -trimpath -buildvcs=true -ldflags "$ldflags" -o "dist/${name}" ./cmd/scry

  info="$(go version -m "dist/${name}")"
  grep -q "vcs.revision=${commit}\$" <<<"$info" || die "${name}: vcs.revision is not ${commit}"
  grep -q "vcs.modified=false\$" <<<"$info" || die "${name}: vcs.modified is not false"
  grep -q "CGO_ENABLED=0\$" <<<"$info" || die "${name}: not a CGO-free build"

  "$work/mkarchive" -out "dist/${name}.tar.gz" -dir "$name" -mtime "$mtime" -exec scry \
    "scry=dist/${name}" "LICENSE=LICENSE" "README.md=README.md"
done

if command -v sha256sum >/dev/null 2>&1; then
  hash() { sha256sum "$@"; }
else
  hash() { shasum -a 256 "$@"; }
fi
# Sums are computed inside dist/ so the names are bare and `sha256sum -c` works
# from there; the list is sorted in the C locale so it is the same everywhere.
(
  cd dist
  LC_ALL=C hash $(LC_ALL=C ls | LC_ALL=C sort) > "$work/SHA256SUMS"
)
mv "$work/SHA256SUMS" dist/SHA256SUMS
echo "release.sh: wrote dist/ ($(ls dist | wc -l | tr -d ' ') files)" >&2
cat dist/SHA256SUMS

#!/usr/bin/env bash
# Build ntee-editor release binaries for macOS + Linux into dist/.
#
# Usage: scripts/build.sh
#   Produces dist/ntee-<os>-<arch> for each target, plus a macOS universal
#   binary (arm64 + amd64) when `lipo` is available.
set -euo pipefail

cd "$(dirname "$0")/.."   # run from the repo root

OUT="dist"
PKG="./cmd/ntee"
LDFLAGS="-s -w"           # strip debug info + symbol table → smaller binaries

# os/arch pairs to build.
targets=(
  "darwin arm64"   # Apple Silicon (M chips)
  "darwin amd64"   # Intel Macs
  "linux  amd64"   # Intel/AMD Linux
  "linux  arm64"   # ARM Linux (Pi 64-bit, Graviton, …)
)

rm -rf "$OUT"
mkdir -p "$OUT"

for t in "${targets[@]}"; do
  read -r os arch <<<"$t"
  bin="$OUT/ntee-$os-$arch"
  echo "building $bin"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags="$LDFLAGS" -o "$bin" "$PKG"
done

# macOS universal binary (needs lipo — macOS only).
if command -v lipo >/dev/null 2>&1; then
  echo "building $OUT/ntee-darwin-universal"
  lipo -create -output "$OUT/ntee-darwin-universal" \
    "$OUT/ntee-darwin-arm64" "$OUT/ntee-darwin-amd64"
fi

echo
echo "done:"
ls -lh "$OUT"

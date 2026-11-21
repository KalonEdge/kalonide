#!/bin/bash
# Build Kalonide release archives.
#
#   scripts/release.sh 0.1.0          (or: make release VERSION=0.1.0)
#
# Runs vet and the tests, then builds static binaries for macOS (arm64,
# amd64 and a universal binary when lipo is available) and Linux (amd64,
# arm64), each packed with the README, CHANGELOG and LICENSE, plus
# SHA256SUMS. Output goes to dist/<version>/. Nothing is uploaded.
set -euo pipefail

VERSION="${1:-}"
if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then
	echo "usage: $0 <version>   e.g. 0.1.0 or 0.2.0-rc1" >&2
	exit 2
fi
cd "$(dirname "$0")/.."

if [[ -n "$(git status --porcelain 2>/dev/null)" && "${ALLOW_DIRTY:-}" != "1" ]]; then
	echo "the working tree has uncommitted changes; commit them, or set ALLOW_DIRTY=1" >&2
	exit 1
fi

if [[ "${SKIP_TESTS:-}" != "1" ]]; then
	echo "== vet and tests"
	go vet ./...
	test -z "$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	go test -count=1 ./...
fi

OUT="dist/$VERSION"
rm -rf "$OUT"
mkdir -p "$OUT"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
LDFLAGS="-s -w -X github.com/KalonEdge/kalonide/internal/version.Version=$VERSION"

docs=(README.md)
for f in CHANGELOG.md LICENSE; do
	[[ -f "$f" ]] && docs+=("$f")
done

pack() { # pack <name> <binary>
	local dir="$OUT/stage/$1"
	mkdir -p "$dir"
	cp "$2" "$dir/kalonide"
	cp "${docs[@]}" "$dir/"
	tar -C "$OUT/stage" -czf "$OUT/$1.tar.gz" "$1"
}

for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
	os="${target%/*}"
	arch="${target#*/}"
	echo "== build $os/$arch"
	bin="$OUT/bin/kalonide-$os-$arch"
	GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$bin" ./cmd/kalonide
	pack "kalonide_${VERSION}_${os}_${arch}" "$bin"
done

if command -v lipo >/dev/null; then
	echo "== universal macOS binary"
	lipo -create -output "$OUT/bin/kalonide-darwin-universal" "$OUT/bin/kalonide-darwin-arm64" "$OUT/bin/kalonide-darwin-amd64"
	pack "kalonide_${VERSION}_darwin_universal" "$OUT/bin/kalonide-darwin-universal"
fi

# The binary for this machine must run and report the version.
host="$OUT/bin/kalonide-$(go env GOOS)-$(go env GOARCH)"
if [[ -x "$host" ]]; then
	got="$("$host" --version)"
	[[ "$got" == "kalonide $VERSION" ]] || { echo "version check failed: $got" >&2; exit 1; }
fi

(cd "$OUT" && shasum -a 256 ./*.tar.gz | sed 's| \./| |' > SHA256SUMS)
rm -rf "$OUT/stage"
echo
echo "Kalonide $VERSION ($COMMIT) → $OUT"
ls -lh "$OUT"/*.tar.gz | awk '{print "  " $5 "  " $9}'
echo
echo "Not done here: signing and notarizing the macOS binaries, and uploading."

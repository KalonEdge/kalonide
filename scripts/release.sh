#!/bin/bash
# Build Kalonide release archives.
#
#   scripts/release.sh 0.1.0          (or: make release VERSION=0.1.0)
#
# Runs vet and the tests, then builds static binaries for macOS (arm64,
# amd64 and a universal binary when lipo is available) and Linux (amd64,
# arm64), each packed with the README, CHANGELOG and LICENSE, plus
# SHA256SUMS. Output goes to dist/<version>/. Nothing is published.
#
# To sign and notarize the macOS binaries, set:
#   DEVELOPER_ID    the signing identity, "Developer ID Application: Name (TEAM)"
#   NOTARY_PROFILE  a notarytool keychain profile (xcrun notarytool store-credentials)
# A bare binary cannot be stapled; macOS checks it with Apple online.
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
	GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/bin/kalonide-$os-$arch" ./cmd/kalonide
done

macs=("$OUT/bin/kalonide-darwin-arm64" "$OUT/bin/kalonide-darwin-amd64")
if command -v lipo >/dev/null; then
	echo "== universal macOS binary"
	lipo -create -output "$OUT/bin/kalonide-darwin-universal" "${macs[@]}"
	macs+=("$OUT/bin/kalonide-darwin-universal")
fi

if [[ -n "${DEVELOPER_ID:-}" ]]; then
	echo "== sign macOS binaries"
	for b in "${macs[@]}"; do
		codesign --force --options runtime --timestamp --sign "$DEVELOPER_ID" "$b"
		codesign --verify --strict "$b"
	done
	if [[ -n "${NOTARY_PROFILE:-}" ]]; then
		echo "== notarize macOS binaries (this waits for Apple)"
		zip="$OUT/notarize.zip"
		ditto -c -k "$OUT/bin" "$zip"
		xcrun notarytool submit "$zip" --keychain-profile "$NOTARY_PROFILE" --wait | tee "$OUT/notarize.log"
		grep -q "status: Accepted" "$OUT/notarize.log" || { echo "notarization was not accepted; see $OUT/notarize.log" >&2; exit 1; }
		rm -f "$zip"
	else
		echo "   (signed, not notarized: set NOTARY_PROFILE to notarize)"
	fi
else
	echo "== macOS binaries are NOT signed (set DEVELOPER_ID and NOTARY_PROFILE)"
fi

for b in "$OUT"/bin/kalonide-*; do
	name="${b##*/kalonide-}"
	pack "kalonide_${VERSION}_${name//-/_}" "$b"
done

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
echo "Not done here: publishing."

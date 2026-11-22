#!/bin/sh
# Install Kalonide, the terminal coding agent for Kalon.
#
#   curl -fsSL https://raw.githubusercontent.com/KalonEdge/kalonide/main/install.sh | sh
#
# Options (environment variables):
#   KALONIDE_VERSION      version to install, e.g. 0.1.0 (default: latest release)
#   KALONIDE_INSTALL_DIR  where to put the binary (default: /usr/local/bin if
#                         writable, otherwise ~/.local/bin)
#
# The download is checked against the release's SHA256SUMS before anything
# is installed. This script never uses sudo.
set -eu

REPO="KalonEdge/kalonide"
BASE="${KALONIDE_DOWNLOAD_BASE:-https://github.com/$REPO/releases/download}"

say() { printf '%s\n' "$*"; }
fail() { printf 'kalonide install: %s\n' "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || fail "needs $1"; }
need uname
need tar
need mktemp
if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL "$1" -o "$2"; }
	fetch_text() { curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -qO "$2" "$1"; }
	fetch_text() { wget -qO- "$1"; }
else
	fail "needs curl or wget"
fi

case "$(uname -s)" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) fail "unsupported system $(uname -s) (macOS and Linux are supported)" ;;
esac
case "$(uname -m)" in
arm64 | aarch64) arch=arm64 ;;
x86_64 | amd64) arch=amd64 ;;
*) fail "unsupported CPU $(uname -m)" ;;
esac

version="${KALONIDE_VERSION:-}"
if [ -z "$version" ]; then
	version="$(fetch_text "https://api.github.com/repos/$REPO/releases/latest" |
		sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -n 1)"
	[ -n "$version" ] || fail "could not find the latest release; set KALONIDE_VERSION"
fi
version="${version#v}"

name="kalonide_${version}_${os}_${arch}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading Kalonide $version for $os/$arch…"
fetch "$BASE/v$version/$name.tar.gz" "$tmp/$name.tar.gz" || fail "download failed: $BASE/v$version/$name.tar.gz"
fetch "$BASE/v$version/SHA256SUMS" "$tmp/SHA256SUMS" || fail "could not download SHA256SUMS"

want="$(awk -v f="$name.tar.gz" '$2 == f { print $1 }' "$tmp/SHA256SUMS")"
[ -n "$want" ] || fail "$name.tar.gz is not listed in SHA256SUMS"
if command -v shasum >/dev/null 2>&1; then
	got="$(shasum -a 256 "$tmp/$name.tar.gz" | awk '{print $1}')"
elif command -v sha256sum >/dev/null 2>&1; then
	got="$(sha256sum "$tmp/$name.tar.gz" | awk '{print $1}')"
else
	fail "needs shasum or sha256sum to check the download"
fi
[ "$got" = "$want" ] || fail "checksum mismatch for $name.tar.gz; not installing"

tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
[ -f "$tmp/$name/kalonide" ] || fail "the archive has no kalonide binary"

dir="${KALONIDE_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
	if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
		dir=/usr/local/bin
	else
		dir="$HOME/.local/bin"
	fi
fi
mkdir -p "$dir" || fail "cannot create $dir"
[ -w "$dir" ] || fail "cannot write to $dir; set KALONIDE_INSTALL_DIR to a folder you own"
cp "$tmp/$name/kalonide" "$dir/kalonide.new"
chmod 755 "$dir/kalonide.new"
mv "$dir/kalonide.new" "$dir/kalonide"

say "Installed $("$dir/kalonide" --version) to $dir/kalonide"
case ":$PATH:" in
*":$dir:"*) ;;
*) say "Add $dir to your PATH, e.g.:  echo 'export PATH=\"$dir:\$PATH\"' >> ~/.zshrc" ;;
esac
say "Start it in a project folder with: kalonide"

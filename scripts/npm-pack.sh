#!/bin/bash
# Build the npm packages for a release from its binaries.
#
#   scripts/npm-pack.sh 0.1.0             build into dist/<version>/npm/
#   scripts/npm-pack.sh 0.1.0 --publish   build, then publish all five
#
# Run scripts/release.sh first: the packages reuse its signed and notarized
# binaries. The result is the main package `kalonide`, whose launcher runs
# the binary from one of four platform packages, @kalonedge/kalonide-<os>-<arch>,
# which npm installs as optional dependencies. No install scripts are used,
# so `npm i -g kalonide` and `npx kalonide` also work with scripts disabled.
set -euo pipefail

VERSION="${1:?usage: $0 <version> [--publish]}"
PUBLISH="${2:-}"
cd "$(dirname "$0")/.."
BIN="dist/$VERSION/bin"
OUT="dist/$VERSION/npm"
[[ -d "$BIN" ]] || { echo "no $BIN; run scripts/release.sh $VERSION first" >&2; exit 1; }

REPO="https://github.com/KalonEdge/kalonide"
DESC="Coding agent for your terminal. Works with Kalon out of the box, and with any OpenAI-compatible model server."
SCOPE="@kalonedge"

rm -rf "$OUT"
mkdir -p "$OUT"

# Go names → npm names.
targets=(darwin-arm64:darwin:arm64:arm64 darwin-amd64:darwin:x64:x64 linux-arm64:linux:arm64:arm64 linux-amd64:linux:x64:x64)

optional=""
for t in "${targets[@]}"; do
	IFS=: read -r gobin os cpu npmarch <<<"$t"
	name="kalonide-$os-$npmarch"
	dir="$OUT/$name"
	mkdir -p "$dir/bin"
	cp "$BIN/kalonide-$gobin" "$dir/bin/kalonide"
	chmod 755 "$dir/bin/kalonide"
	cp LICENSE "$dir/"
	cat >"$dir/package.json" <<EOF
{
  "name": "$SCOPE/$name",
  "version": "$VERSION",
  "description": "The $os $cpu binary for Kalonide. Install the kalonide package instead.",
  "license": "MIT",
  "homepage": "$REPO",
  "repository": { "type": "git", "url": "git+$REPO.git" },
  "os": ["$os"],
  "cpu": ["$cpu"],
  "files": ["bin/kalonide"],
  "preferUnplugged": true
}
EOF
	cat >"$dir/README.md" <<EOF
# $SCOPE/$name

The $os $cpu binary for [Kalonide]($REPO). Install the main package instead:

\`\`\`bash
npm install -g kalonide
\`\`\`
EOF
	optional+="    \"$SCOPE/$name\": \"$VERSION\",
"
done
optional="${optional%,
}"

main="$OUT/kalonide"
mkdir -p "$main/bin"
cp npm/kalonide.js "$main/bin/kalonide.js"
chmod 755 "$main/bin/kalonide.js"
cp LICENSE "$main/"
cat >"$main/package.json" <<EOF
{
  "name": "kalonide",
  "version": "$VERSION",
  "description": "$DESC",
  "keywords": ["kalon", "coding-agent", "cli", "terminal", "developer-tools", "openai-compatible", "ollama", "lm-studio"],
  "license": "MIT",
  "homepage": "$REPO",
  "bugs": "$REPO/issues",
  "repository": { "type": "git", "url": "git+$REPO.git" },
  "bin": { "kalonide": "bin/kalonide.js" },
  "files": ["bin/kalonide.js"],
  "engines": { "node": ">=16" },
  "os": ["darwin", "linux"],
  "cpu": ["arm64", "x64"],
  "optionalDependencies": {
$optional
  }
}
EOF
cat >"$main/README.md" <<EOF
# Kalonide

![Kalonide starting up](https://raw.githubusercontent.com/KalonEdge/kalonide/main/docs/startup-screen.png)

$DESC

\`\`\`bash
npm install -g kalonide
\`\`\`

Then, in any project folder:

\`\`\`bash
kalonide
\`\`\`

Or try it without installing: \`npx kalonide\`.

With the Kalon app open, there is nothing to configure: Kalonide connects
automatically and picks the best model available for coding. To use
another server, such as Ollama, LM Studio, OpenRouter or OpenAI, run
\`kalonide providers add\`.

Kalonide runs on macOS (Apple silicon and Intel) and Linux (arm64 and
x86-64), and the macOS builds are signed and notarized by Apple. It's also
available with Homebrew: \`brew install kalonedge/tap/kalonide\`.

For the full documentation, see [github.com/KalonEdge/kalonide]($REPO).

## License

MIT
EOF

echo "npm packages for $VERSION → $OUT"
for d in "$OUT"/*/; do
	(cd "$d" && npm pack --dry-run --json 2>/dev/null | node -e '
		const [p] = JSON.parse(require("fs").readFileSync(0, "utf8"));
		console.log(`  ${p.name.padEnd(36)} ${(p.size / 1048576).toFixed(1)} MB packed, ${p.entryCount} files`);')
done

if [[ "$PUBLISH" == "--publish" ]]; then
	# Platform packages first, so the main package never points at a missing
	# one. Versions already on npm are skipped, so a rerun picks up where an
	# interrupted one stopped.
	for d in "$OUT"/kalonide-*/ "$main/"; do
		name="$(cd "$d" && node -p 'require("./package.json").name')"
		if npm view "$name@$VERSION" version >/dev/null 2>&1; then
			echo "$name@$VERSION is already published; skipping"
			continue
		fi
		(cd "$d" && npm publish --access public)
	done
fi

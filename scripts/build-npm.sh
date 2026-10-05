#!/usr/bin/env bash
# Assembles the npm packages into dist/npm/: one wrapper plus one package per
# platform. Builds nothing that touches the registry -- `npm pack` on each
# output produces exactly what a publish would upload.
#
# It REFUSES an untagged or dirty tree, which the release build only warns
# about. The difference is that a package carries a version number as its
# identity: `git describe` on an untagged repo yields a bare commit, which is
# not valid semver, and a -dirty package would claim to be a commit it is not.
set -euo pipefail
cd "$(dirname "$0")/.."

DIST=${DIST:-dist}
OUT="$DIST/npm"
SCOPE="@draftplane"
# PKG is the platform packages' name stem, and it is NOT redundant with SCOPE.
# SCOPE is the whole npm scope, not this product; a second binary product
# shipped under it would want @draftplane/darwin-arm64 too, and a bare platform
# name gives the first product to publish a permanent claim on it. Prefixing
# keeps the scope open -- the shape sharp (@img/sharp-darwin-arm64), rollup and
# swc all use. It also means the last path segment still identifies the product
# when it is read on its own, in a lockfile, an advisory or an error message,
# where "darwin-arm64" alone says nothing.
PKG="draftplane"
PLATFORMS=(darwin-arm64 darwin-x64 linux-arm64 linux-x64)

# os/cpu per platform, in Node's vocabulary -- npm uses these to install
# exactly one platform package and skip the rest.
declare -A OS=( [darwin-arm64]=darwin [darwin-x64]=darwin [linux-arm64]=linux [linux-x64]=linux )
declare -A CPU=( [darwin-arm64]=arm64 [darwin-x64]=x64 [linux-arm64]=arm64 [linux-x64]=x64 )

RAW=$(git describe --tags --dirty 2>/dev/null || true)
if [ -z "$RAW" ]; then
  echo "refusing: no git tag reachable from HEAD." >&2
  echo "  A package needs a version, and an untagged commit has none." >&2
  echo "  Create one first, e.g.  git tag -a v0.1.0 -m 'first release'" >&2
  exit 1
fi
case "$RAW" in *-dirty)
  echo "refusing: the working tree is dirty ($RAW)." >&2
  echo "  A package must correspond to a commit. Commit or stash first." >&2
  exit 1 ;;
esac

# npm wants semver without the leading v; git tags carry it by Go convention.
VERSION="${RAW#v}"

[ -d "$DIST" ] || { echo "no $DIST/ -- run 'make dist' first" >&2; exit 1; }
for p in "${PLATFORMS[@]}"; do
  [ -x "$DIST/$p/bin/draftplane" ] || { echo "missing $DIST/$p/bin/draftplane -- run 'make dist'" >&2; exit 1; }
done

rm -rf "$OUT"; mkdir -p "$OUT"
echo "assembling npm packages at version $VERSION"

# --- platform packages -------------------------------------------------------
for p in "${PLATFORMS[@]}"; do
  d="$OUT/$p"; mkdir -p "$d/bin"
  cp "$DIST/$p/bin/draftplane" "$d/bin/draftplane"
  chmod +x "$d/bin/draftplane"
  cp LICENSE THIRD-PARTY-NOTICES "$d/"
  cat > "$d/package.json" <<JSON
{
  "name": "$SCOPE/$PKG-$p",
  "version": "$VERSION",
  "description": "draftplane binary for $p. Installed automatically by the draftplane package; not useful on its own.",
  "license": "MIT",
  "homepage": "https://draftplane.io",
  "bugs": { "email": "bugs@draftplane.io" },
  "repository": { "type": "git", "url": "git+https://github.com/draftplane/draftplane.git" },
  "os": ["${OS[$p]}"],
  "cpu": ["${CPU[$p]}"],
  "engines": { "node": ">=18" },
  "files": ["bin/", "LICENSE", "THIRD-PARTY-NOTICES"],
  "preferUnplugged": true,
  "publishConfig": { "access": "public" }
}
JSON
done

# --- the wrapper -------------------------------------------------------------
# optionalDependencies are pinned EXACTLY, not with a caret: the wrapper must
# get the binaries built from its own commit. A range would let npm satisfy a
# 0.1.0 wrapper with 0.1.3 binaries.
w="$OUT/draftplane"; mkdir -p "$w/bin"
cp npm/shim/draftplane.js "$w/bin/draftplane.js"; chmod +x "$w/bin/draftplane.js"
cp npm/README.md "$w/README.md"
cp LICENSE THIRD-PARTY-NOTICES "$w/"

deps=$(for p in "${PLATFORMS[@]}"; do printf '    "%s/%s-%s": "%s",\n' "$SCOPE" "$PKG" "$p" "$VERSION"; done | sed '$ s/,$//')
cat > "$w/package.json" <<JSON
{
  "name": "draftplane",
  "version": "$VERSION",
  "description": "Review AI-generated plans in the terminal.",
  "license": "MIT",
  "homepage": "https://draftplane.io",
  "bugs": { "email": "bugs@draftplane.io" },
  "repository": { "type": "git", "url": "git+https://github.com/draftplane/draftplane.git" },
  "keywords": ["cli", "code-review", "ai", "agents", "mcp", "tui"],
  "bin": { "draftplane": "bin/draftplane.js" },
  "engines": { "node": ">=18" },
  "files": ["bin/", "README.md", "LICENSE", "THIRD-PARTY-NOTICES"],
  "optionalDependencies": {
$deps
  }
}
JSON

for f in "$OUT"/*/package.json; do node -e "JSON.parse(require('fs').readFileSync('$f','utf8'))" || { echo "invalid JSON: $f" >&2; exit 1; }; done
echo "wrote $OUT/ — $(( ${#PLATFORMS[@]} + 1 )) packages at $VERSION"

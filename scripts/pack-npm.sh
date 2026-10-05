#!/usr/bin/env bash
# Packs the assembled npm packages in dist/npm/ into .tgz tarballs beside them.
#
# build-npm.sh deliberately stops at directories, because nothing it does may
# touch the registry and `npm publish` packs its own input anyway. This script
# is the step that makes those directories INSTALLABLE LOCALLY, which is a
# different job from publishing and needs a different artifact.
#
# WHY A TARBALL RATHER THAN THE DIRECTORY. `npm install ./dist/npm/draftplane`
# SYMLINKS the package, and the launcher resolves its platform binary with
# require.resolve, which walks up from the shim's REALPATH -- out of the
# installing project and past the node_modules that holds the binary. So a
# path install of the wrapper cannot find a platform package that is right
# there. A tarball is extracted rather than linked, so the walk stays inside
# the project and resolution works.
#
# It also fails LOUDER than the path install it replaces: installing the
# wrapper alone reports "added 1 package" and succeeds, because the platform
# packages are optionalDependencies and npm skips one it cannot fetch. The
# install command printed at the end names both halves for that reason.
set -euo pipefail
cd "$(dirname "$0")/.."

DIST=${DIST:-dist}
OUT="$DIST/npm"
PLATFORMS=(darwin-arm64 darwin-x64 linux-arm64 linux-x64)

[ -d "$OUT" ] || { echo "no $OUT/ -- run 'make npm' first" >&2; exit 1; }

# The version comes from the ASSEMBLED WRAPPER rather than from `git describe`,
# so this packs what is on disk instead of what the tree would produce now. A
# tag moved after `make npm` ran would otherwise name the tarballs one version
# and their package.json another, and npm believes the manifest.
VERSION=$(node -p "require('./$OUT/draftplane/package.json').version" 2>/dev/null) || {
  echo "cannot read $OUT/draftplane/package.json -- run 'make npm' first" >&2; exit 1; }

for p in "${PLATFORMS[@]}" draftplane; do
  [ -d "$OUT/$p" ] || { echo "missing $OUT/$p -- run 'make npm' first" >&2; exit 1; }
done

rm -f "$OUT"/*.tgz
for p in "${PLATFORMS[@]}" draftplane; do
  # 2>&1 as well as >/dev/null: npm pack writes its per-file "npm notice"
  # inventory to STDERR, so redirecting stdout alone leaves ~80 lines of it
  # in front of this script's own summary.
  ( cd "$OUT/$p" && npm pack --pack-destination .. >/dev/null 2>&1 )
done

echo "packed $(( ${#PLATFORMS[@]} + 1 )) tarballs at $VERSION into $OUT/"
for f in "$OUT"/*.tgz; do
  printf '  %-46s %s\n' "$(basename "$f")" "$(du -h "$f" | cut -f1 | tr -d ' ')"
done

# The install line names THIS machine's platform, because the wrapper alone
# installs cleanly and then cannot run -- naming both halves is the whole
# point of printing a command rather than a directory listing.
case "$(uname -s)/$(uname -m)" in
  Darwin/arm64)  HOST=darwin-arm64 ;;
  Darwin/x86_64) HOST=darwin-x64 ;;
  Linux/aarch64|Linux/arm64) HOST=linux-arm64 ;;
  Linux/x86_64)  HOST=linux-x64 ;;
  *)             HOST="" ;;
esac

echo
if [ -n "$HOST" ]; then
  echo "install locally:"
  echo "  npm install -g $OUT/draftplane-$VERSION.tgz $OUT/draftplane-draftplane-$HOST-$VERSION.tgz"
  echo
  echo "BOTH tarballs are required. The wrapper alone installs and reports success,"
  echo "then fails at run time: its platform binaries are optionalDependencies and"
  echo "npm silently skips one it cannot fetch from a registry."
else
  echo "no draftplane build for $(uname -s)/$(uname -m); install on a supported host:"
  echo "  ${PLATFORMS[*]}"
fi

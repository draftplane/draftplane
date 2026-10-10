#!/usr/bin/env bash
# Publishes the tarballs pack-npm.sh left in dist/npm/ to the npm registry: the
# four platform packages, then a wait until every one of them resolves, then
# the wrapper.
#
# THE ORDER IS THE POINT. The wrapper pins each platform package exactly as an
# optionalDependency, and npm silently skips an optional dependency it cannot
# fetch. A wrapper that goes live before its binaries resolve installs cleanly
# for anyone who catches the gap, and then cannot run.
#
# A 404 straight after publishing is registry lag (3-7 minutes for 0.2.0), not
# a failure, so the wait polls until a deadline rather than checking once. On
# timeout it stops WITHOUT publishing the wrapper.
#
# It is SAFE TO RE-RUN. A package that already resolves at this version is
# skipped, since npm refuses to publish a version twice, so a run that timed
# out picks up at the wait. One gap: a package published moments ago may not
# resolve yet, and re-running then makes npm refuse it as a duplicate. Wait a
# few minutes and run it again.
#
# It publishes the TARBALLS, not the directories, so what ships is exactly what
# `make npm:pack` produced and what a local install was tested against.
set -euo pipefail
cd "$(dirname "$0")/.."

DIST=${DIST:-dist}
OUT="$DIST/npm"
SCOPE="@draftplane"
PKG="draftplane"
PLATFORMS=(darwin-arm64 darwin-x64 linux-arm64 linux-x64)
POLL_INTERVAL=${POLL_INTERVAL:-30}
POLL_TIMEOUT=${POLL_TIMEOUT:-1200}

# The version comes from the ASSEMBLED WRAPPER, for the reason pack-npm.sh
# gives: publish what is on disk, not what the tree would produce now.
VERSION=$(node -p "require('./$OUT/draftplane/package.json').version" 2>/dev/null) || {
  echo "cannot read $OUT/draftplane/package.json -- run 'make npm' first" >&2; exit 1; }

# npm pack names a scoped package's tarball with the @ dropped and the / as a
# hyphen: @draftplane/draftplane-linux-x64 -> draftplane-draftplane-linux-x64.
platform_tgz() { echo "$OUT/${SCOPE#@}-$PKG-$1-$VERSION.tgz"; }
WRAPPER_TGZ="$OUT/$PKG-$VERSION.tgz"

for t in $(for p in "${PLATFORMS[@]}"; do platform_tgz "$p"; done) "$WRAPPER_TGZ"; do
  [ -f "$t" ] || { echo "missing $t -- run 'make npm:pack' first" >&2; exit 1; }
done

# Only an exact echo of the version counts. npm view prints nothing and exits
# non-zero for a version or package it cannot find, which is what registry lag
# looks like.
resolves() { [ "$(npm view "$1@$VERSION" version 2>/dev/null)" = "$VERSION" ]; }

# plan prints one line of the summary, and fails for a package already there.
plan() {
  if resolves "$1"; then echo "  $1  (already published, skipping)"; return 1; fi
  echo "  $1"
}

echo "publishing $VERSION:"
PENDING=()
for p in "${PLATFORMS[@]}"; do
  plan "$SCOPE/$PKG-$p" && PENDING+=("$(platform_tgz "$p")")
done
WRAPPER_PENDING=false
plan "$PKG" && WRAPPER_PENDING=true

if [ ${#PENDING[@]} -eq 0 ] && ! $WRAPPER_PENDING; then
  echo "nothing to publish"; exit 0
fi

# Login comes after the check above because npm view needs no session, so a
# run with nothing left to publish never prompts for one.
npm whoami >/dev/null 2>&1 || npm login
WHO=$(npm whoami)

# A published version can never be reused, even after an unpublish, so this is
# the one place to back out.
read -r -p "publish as $WHO? [y/N] " answer || true
case "${answer:-}" in y|Y) ;; *) echo "aborted, nothing published"; exit 1 ;; esac

# Guarded because a resume can leave only the wrapper, and bash before 4.4
# treats expanding an empty array under set -u as an unbound variable.
if [ ${#PENDING[@]} -gt 0 ]; then
  for t in "${PENDING[@]}"; do
    npm publish "$t" --access public
  done
fi

echo "waiting for all ${#PLATFORMS[@]} platform packages to resolve at $VERSION"
start=$SECONDS
while :; do
  waiting=()
  for p in "${PLATFORMS[@]}"; do resolves "$SCOPE/$PKG-$p" || waiting+=("$p"); done
  [ ${#waiting[@]} -gt 0 ] || break
  if (( SECONDS - start >= POLL_TIMEOUT )); then
    echo "timed out after ${POLL_TIMEOUT}s still waiting on: ${waiting[*]}" >&2
    echo "the wrapper was NOT published; re-run 'make npm:publish' to resume" >&2
    exit 1
  fi
  echo "  $(( SECONDS - start ))s: not resolving yet: ${waiting[*]}"
  sleep "$POLL_INTERVAL"
done
echo "all resolve after $(( SECONDS - start ))s"

if $WRAPPER_PENDING; then
  npm publish "$WRAPPER_TGZ"
fi
echo "published $PKG $VERSION"

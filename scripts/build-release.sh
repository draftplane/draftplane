#!/usr/bin/env bash
# Cross-compiles every distributable target into dist/, laid out so that each
# directory is already the payload of one npm platform package.
#
# DIRECTORIES ARE NAMED BY NODE'S CONVENTION, NOT GO'S. Node reports amd64 as
# "x64", and the shim resolves `@draftplane/draftplane-${process.platform}-${process.arch}`
# at run time -- so naming these darwin-x64 rather than darwin-amd64 is what
# makes the package name a substitution rather than a lookup table. GOARCH is
# mapped on the way in; that mapping is the only place the two vocabularies
# meet.
set -euo pipefail

cd "$(dirname "$0")/.."

DIST=${DIST:-dist}
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo "")}

# The host whose artifact can actually be RUN, which is the only one the
# version check below can read the linked value out of.
HOSTOS=$(go env GOOS)
HOSTARCH=$(go env GOARCH)
verified=0
LDFLAGS="-s -w -X github.com/draftplane/draftplane/version.version=${VERSION}"

# THE TOOLCHAIN FLOOR, READ FROM go.mod RATHER THAN REPEATED HERE. go.mod's
# toolchain directive is what actually makes `go build` below use go1.26.6 or
# newer; this script only VERIFIES it, and a second copy of the version string
# would be a second thing to forget. See that directive's own comment for why
# the floor is what it is.
#
# WHY VERIFY AT ALL, WHEN THE DIRECTIVE ALREADY SWITCHES. GOTOOLCHAIN=local in
# the environment defeats the switch silently -- the go command does not error,
# it just builds with whatever is installed -- and a release is the one build
# where proceeding quietly is unacceptable. This is also the only place that can
# answer the SHIPPING question: `govulncheck ./...` scans source and says which
# advisories this module's own code reaches, while `govulncheck -mode=binary`
# scans an artifact and says which are reachable in what we ship. Those are
# different questions and they gave different answers (4 against 8) on the same
# eight advisories. The artifact's own recorded toolchain is the fact that
# closes the second one, so it is read back out of each binary after it is
# built, not assumed from the environment before.
FLOOR=$(awk '$1 == "toolchain" { print $2 }' go.mod)
if [ -z "$FLOOR" ]; then
  echo "ERROR: go.mod carries no toolchain directive." >&2
  echo "       Without one this builds against whatever Go is on PATH, which is how" >&2
  echo "       every shipped binary came to be go1.26.5. Refusing to cut a release." >&2
  exit 1
fi

# meets_floor <recorded-go-version>; version-sorts the pair and asks whether the
# floor is the lower of the two. sort -V, not a string compare: go1.26.10 sorts
# BELOW go1.26.6 as text and above it as a version.
meets_floor() {
  [ "$(printf '%s\n%s\n' "${FLOOR#go}" "${1#go}" | sort -V | head -1)" = "${FLOOR#go}" ]
}

# node-name:GOOS:GOARCH
TARGETS=(
  "darwin-arm64:darwin:arm64"
  "darwin-x64:darwin:amd64"
  "linux-arm64:linux:arm64"
  "linux-x64:linux:amd64"
)

# AN EMPTY VERSION IS REFUSED, because the check below could not measure it. An
# unstamped binary reports its commit, or "unknown", never an empty version, so
# that check would blame the stamp for a value this script never had.
case "$VERSION" in
  "")
    echo "ERROR: no VERSION to stamp, and git could not describe this tree." >&2
    echo "       Set VERSION, or build from a git checkout." >&2
    exit 1
    ;;
  *-dirty)
    echo "WARNING: building from a dirty tree ($VERSION)." >&2
    echo "         Fine for development. A publish must refuse this." >&2
    ;;
esac

rm -rf "$DIST"
echo "building ${#TARGETS[@]} targets at version ${VERSION}"
for t in "${TARGETS[@]}"; do
  IFS=: read -r nodename goos goarch <<< "$t"
  out="$DIST/$nodename/bin/draftplane"
  mkdir -p "$(dirname "$out")"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$out" ./cmd/draftplane
  built=$(go version -m "$out" | awk 'NR == 1 { print $2 }')
  if ! meets_floor "$built"; then
    echo "ERROR: $out was built by $built, below go.mod's floor of $FLOOR." >&2
    echo "       Unset GOTOOLCHAIN (or set it to auto) and build again." >&2
    exit 1
  fi
  # THE CHECK PROVES WHAT draftplane version PRINTS, BY RUNNING THE ARTIFACT,
  # AND IT HAS TO BE RUN TO PROVE THAT. `go version -m` does NOT record
  # -ldflags when -trimpath is set: the build settings carry `-trimpath=true`
  # and no ldflags line at all. Every string-inspection form of this question
  # has that shape -- it reads what was ASKED FOR, or nothing, never what a
  # user would see. Running the binary reads the value that actually prints.
  #
  # IT DOES NOT PROVE THE -X STAMP TOOK, SPECIFICALLY. Since Go 1.24, a build
  # from a clean, tagged tree has Go's own VCS stamping set the main module's
  # version to that same tag, and version.Resolve falls back to it whenever
  # the -X stamp is empty -- so a passing probe here cannot tell a working -X
  # path from a failed one that Go's own stamp papered over. What it proves is
  # the one fact that actually matters: the version a user running this binary
  # sees is the one we meant to ship.
  #
  # Only the artifact matching THIS host can be executed, and that is enough:
  # all four targets are linked from one $LDFLAGS, so a stamp that took for one
  # took for all. What must never happen is the check being skipped without
  # anyone noticing, which is what `verified` below exists for.
  if [ "$goos" = "$HOSTOS" ] && [ "$goarch" = "$HOSTARCH" ]; then
    probe=$("$out" version)
    if [ "$probe" != "draftplane ${VERSION}" ]; then
      echo "ERROR: $out reports '$probe', expected 'draftplane ${VERSION}'." >&2
      echo "       The version a user would see is wrong." >&2
      exit 1
    fi
    verified=1
    echo "  verified version=${VERSION} by running $nodename"
  fi
  # Every platform package carries the licence and the notices. The obligation
  # attaches to the binary, so it travels with the binary rather than living
  # only in the package a user happens to install directly.
  cp LICENSE THIRD-PARTY-NOTICES "$DIST/$nodename/"
  # The toolchain is printed, not just checked: the build log is where anyone
  # re-running govulncheck -mode=binary against these artifacts later goes to
  # find out what they were built with.
  printf '  %-14s %-9s %-7s %s\n' "$nodename" "$built" "$(du -h "$out" | cut -f1 | tr -d ' ')" "$(file -b "$out" | cut -d, -f1-2)"
done
# A CHECK THAT DID NOT RUN MUST NOT READ AS A CHECK THAT PASSED. If no target
# matches this host, nothing above executed a binary and the version stamp is
# unverified -- the silent-skip trap, refused here rather than discovered later.
if [ "$verified" != "1" ]; then
  echo "ERROR: no target matches this host ($HOSTOS/$HOSTARCH), so version ${VERSION} was never verified." >&2
  echo "       Add this platform to TARGETS, or build on one that is in it." >&2
  exit 1
fi

echo "wrote $DIST/"

.PHONY: check fmt vet lint test notices notices-check dist npm npm\:pack clean

# Run every gate the CI workflow used to run.
# VERSION is the release identity stamped into the binary. `git describe`
# because it is the only thing that knows about TAGS -- the Go toolchain
# records the commit on its own but has no idea a tag points at it.
#
#   on a tag        v0.1.0
#   past a tag      v0.1.0-3-gabc1234
#   uncommitted     ...-dirty
#   no tags at all  abc1234           (--always, so a build never fails for it)
#
# There is deliberately NO BUILD TIMESTAMP. It would make two builds of the
# same commit differ, trading reproducibility for a fact the commit already
# answers better.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null)
LDFLAGS := -X github.com/draftplane/draftplane/version.version=$(VERSION)

check: fmt vet lint notices-check test

lint:
	golangci-lint run ./...

# Fail if any file is not gofmt-clean, listing the offenders.
fmt:
	@files=$$(gofmt -l .); \
	if [ -n "$$files" ]; then \
		echo "gofmt needs to run on:"; echo "$$files"; \
		exit 1; \
	fi

vet:
	go vet ./...

test:
	go test ./... -count=1
	go test ./app/ ./client/ ./client/config/ ./client/identity/ ./client/localfs/ ./client/recent/ ./cmd/draftplane/ ./mcptools/ -race -count=1

build:
	go build -ldflags '$(LDFLAGS)' -o bin/draftplane ./cmd/draftplane

run: build
	./bin/draftplane review $(PLAN)

# notices regenerates THIRD-PARTY-NOTICES from the modules linked into a
# freshly built binary. Run it whenever a dependency is added, removed or
# bumped -- the file is a distribution obligation (MIT and BSD both require
# the notice to accompany binary distributions, Apache-2.0 additionally
# requires its NOTICE), so it travels in the npm package rather than living
# only in this repository.
notices:
	@go build -ldflags '$(LDFLAGS)' -o bin/draftplane ./cmd/draftplane
	@./scripts/gen-third-party-notices.sh bin/draftplane

# notices-check fails if THIRD-PARTY-NOTICES is out of date with the code.
# The file is generated, so the failure mode is that a dependency changes and
# nobody regenerates it -- which is invisible until someone reads it.
#
# IT IS PART OF `make check` DELIBERATELY, and the reason is evidentiary as
# much as technical. MIT and BSD both require their notice
# to accompany a binary distribution; an enforced, automated check that the
# notice matches what actually ships is evidence of a good-faith compliance
# effort in a way that a file someone remembers to regenerate is not. The
# argument for it is not that staleness is likely -- it is that being able to
# show the check exists costs nothing and is worth having if it is ever asked
# for.
#
# Like `fmt`, it repairs as it checks: a stale run leaves the corrected file
# in the working tree, so the fix is already applied and only needs
# committing. A dirty tree after `make check` is itself a finding.
#
# WHAT IT DETECTS, stated precisely because the obvious reading is wrong. It
# regenerates BEFORE comparing, so what it answers is "does the COMMITTED file
# match what the generator produces" -- which catches the case that matters, a
# dependency changing with nobody regenerating. It cannot catch an uncommitted
# hand-edit, because regeneration overwrites one before the comparison runs.
# Verified by mutation: editing the working file passes, and changing the
# generated CONTENT fails with exit 2. Do not read this as tamper-detection.
notices-check: notices
	@git diff --quiet --exit-code THIRD-PARTY-NOTICES || { \
	  echo "THIRD-PARTY-NOTICES is stale -- run 'make notices' and commit the result"; exit 1; }
	@echo "THIRD-PARTY-NOTICES is current"

# dist cross-compiles every distributable target into dist/, one directory per
# npm platform package, each carrying the licence and the notices alongside the
# binary. Local only; nothing here publishes.
#
# Verified by running all four: the version stamp survives every
# cross-compile and each binary reports its own platform.
dist:
	@./scripts/build-release.sh

# npm assembles dist/npm/ -- the wrapper plus one package per platform, ready
# for `npm pack`. Depends on dist/, and REFUSES an untagged or dirty tree,
# because a package carries a version as its identity in a way a development
# build does not.
npm: dist
	@./scripts/build-npm.sh

# npm:pack turns the assembled package DIRECTORIES into .tgz tarballs, which is
# what makes them installable locally: `npm install <path>` symlinks, and the
# launcher's require.resolve walks up from the shim's realpath, out of the
# installing project and past the node_modules holding its platform binary. A
# tarball is extracted rather than linked, so that walk stays inside the project.
#
# It deliberately does NOT depend on npm. The script reads its version out of
# the ASSEMBLED WRAPPER, so it packs what is on disk; a prerequisite would
# rebuild instead, and would refuse a dirty tree for a dist/ that is already
# valid. It errors naming 'make npm' when there is nothing assembled to pack.
#
# The colon is escaped because make reads an unescaped one as the rule
# separator -- `npm:pack:` unescaped is a parse error. It is still invoked
# unquoted, as `make npm:pack`.
npm\:pack:
	@./scripts/pack-npm.sh

# clean removes the build outputs and nothing else. The two paths are the ones
# .gitignore already names, which is this repository's own standing answer to
# "what did a build write": bin/ from `build` and `notices`, dist/ from `dist`,
# `npm` and `npm:pack`.
#
# IT DELIBERATELY DOES NOT DELETE THIRD-PARTY-NOTICES. `make notices` generates
# that file, so it looks like a build output, but it is COMMITTED and it is a
# distribution obligation -- MIT and BSD both require the notice to accompany a
# binary. Removing it would redden notices-check and drop a licence file out of
# the tree, which is the opposite of what cleaning is for.
#
# Nor does it clear Go's build and test cache. `go clean -cache` is a different
# request: it is not this repository's state, and it costs every later build
# minutes to rebuild. Run it by hand if that is what you actually want.
clean:
	@rm -rf bin dist
	@echo "removed bin/ dist/"

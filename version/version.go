// Package version answers "which build of draftplane is this?" for the three
// callers that need it: the `version` command, the MCP initialize handshake,
// and anyone reading a bug report.
package version

import (
	"regexp"
	"runtime/debug"
)

// version is stamped at link time from `git describe --tags --always --dirty`
// (see the Makefile's LDFLAGS). It stays unexported and is read through the
// functions below, so no caller can hold a copy that outlives a change in how
// it is derived.
//
// Empty means nobody stamped it -- a plain `go build`, or `go test`. That is a
// legitimate state rather than an error, and Resolve falls back.
var version string

// Info is what a build can say about itself. Every field may be empty; a
// caller renders what it has rather than asserting a shape.
type Info struct {
	Version  string // "v0.2.1", or "v0.2.1-3-gabc1234-dirty", or "" if unstamped
	Revision string // VCS commit, from the build, when the toolchain recorded one
	Modified bool   // the tree had uncommitted changes at build time
}

// Resolve reports what this binary knows about itself.
//
// The two sources are different in kind and neither subsumes the other. The
// LINK-TIME stamp knows about TAGS, which the toolchain does not. The BUILD
// INFO knows the commit even when nobody passed a flag, which is what lets a
// bare `go build` still identify itself.
func Resolve() Info {
	i := Info{Version: version}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return i
	}
	// Main.Version is the module version for `go install pkg@vX.Y.Z`. For a
	// build with no such version the toolchain supplies "(devel)" or a
	// PSEUDO-VERSION synthesised from the commit; both are rejected so Short
	// falls through to the revision, which says the same thing in twelve
	// characters.
	if i.Version == "" && bi.Main.Version != "" &&
		bi.Main.Version != "(devel)" && !isPseudoVersion(bi.Main.Version) {
		i.Version = bi.Main.Version
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			i.Revision = s.Value
		case "vcs.modified":
			i.Modified = s.Value == "true"
		}
	}
	return i
}

// Short is the one-token answer, for places that have room for a version and
// nothing else -- the MCP handshake among them. It never returns empty: a
// build that can identify itself by commit says so, and one that cannot says
// "unknown" rather than an empty string a reader would take for a bug.
func Short() string { return short(Resolve()) }

// short is Short's decision, split out so it can be driven with constructed
// inputs. Resolve reads the real build, which cannot be faked, so a test that
// went through it could only ever assert whatever this binary happens to be.
func short(i Info) string {
	switch {
	case i.Version != "":
		return i.Version
	case i.Revision != "":
		r := shortRev(i.Revision)
		if i.Modified {
			r += "-dirty"
		}
		return r
	default:
		return "unknown"
	}
}

// pseudoVersion matches the timestamp-and-commit core the toolchain builds a
// pseudo-version around: fourteen digits, then a twelve-character hash. Both
// widths are fixed by the format, which is why matching them is safe.
//
// The leading class must stay [-.] rather than [-]: there are THREE
// pseudo-version forms and they do not agree on that character --
// "v0.0.0-TS-hash" with no base tag, but "v1.2.4-0.TS-hash" and
// "v1.2.3-rc.1.0.TS-hash" with one.
var pseudoVersion = regexp.MustCompile(`[-.][0-9]{14}-[0-9a-f]{12}`)

func isPseudoVersion(v string) bool { return pseudoVersion.MatchString(v) }

func shortRev(r string) string {
	if len(r) > 12 {
		return r[:12]
	}
	return r
}

package version

import (
	gover "go/version"
	"os"
	"runtime"
	"strings"
	"testing"
)

// TestShortPrefersTheStampAndNeverReturnsEmpty pins the ORDER, which is the
// only thing about Short that can silently go wrong: each source is correct
// on its own, and the defect would be preferring the wrong one.
func TestShortPrefersTheStampAndNeverReturnsEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Info
		want string
	}{
		{"a stamped tag wins over everything",
			Info{Version: "v0.2.1", Revision: "abcdef012345", Modified: true}, "v0.2.1"},
		{"no stamp falls back to the revision",
			Info{Revision: "abcdef0123456789"}, "abcdef012345"},
		{"a dirty tree says so on the fallback",
			Info{Revision: "abcdef0123456789", Modified: true}, "abcdef012345-dirty"},
		{"a short revision is not padded or truncated",
			Info{Revision: "abc123"}, "abc123"},
		{"nothing known is stated, not left empty",
			Info{}, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := short(tc.in); got != tc.want {
				t.Fatalf("short(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestPseudoVersionsAreRejected is the reason Resolve does not simply trust
// bi.Main.Version. The toolchain synthesises one of these for any build
// without a module version, so accepting them would make every local build
// report a 30-character string that is really just a commit and a timestamp.
func TestPseudoVersionsAreRejected(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"v0.0.0-20260821052827-cd7e37cae8c1", true},
		{"v0.0.0-20260821052827-cd7e37cae8c1+dirty", true},
		{"v1.2.3-0.20260821052827-cd7e37cae8c1", true},
		{"v0.1.0", false},
		{"v1.2.3-rc.1", false},
		{"v0.2.1-3-gabc1234", false}, // git describe output, NOT a pseudo-version
		{"v0.2.1-3-gabc1234-dirty", false},
	} {
		if got := isPseudoVersion(tc.in); got != tc.want {
			t.Errorf("isPseudoVersion(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// minToolchain is the lowest Go toolchain this repository may be built with,
// and it is a SECURITY floor rather than a language-feature one -- go.mod's
// `go` line still says 1.25.0, which is what decides the language version.
//
// go1.26.5 carries eight Go standard-library advisories, all fixed in go1.26.6;
// the floor names the requirement, so anything above it satisfies it too.
//
// It cannot notice that a NEW advisory has raised the floor -- that takes
// someone running govulncheck again and editing this line.
const minToolchain = "go1.26.6"

// meetsToolchainFloor reports whether the Go toolchain named by v is at or
// above minToolchain.
//
// Use go/version.Compare, never a string compare (go1.26.10 sorts below
// go1.26.6) or a hand-rolled parse (the release-candidate suffix). An
// unrecognizable version -- a devel toolchain, or an empty string -- does NOT
// meet the floor: a floor refuses what it cannot vouch for.
func meetsToolchainFloor(v string) bool {
	return gover.IsValid(v) && gover.Compare(v, minToolchain) >= 0
}

func TestMeetsToolchainFloor(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"go1.26.5", false}, // what the shipped binaries were built with
		{"go1.26.6", true},  // the floor itself
		{"go1.26.7", true},
		{"go1.26.10", true}, // a string compare puts this below go1.26.6
		{"go1.25.13", false},
		{"go1.27.0", true},
		{"go1.27rc1", true},
		{"devel go1.28-0123456789ab", false},
		{"", false},
	} {
		if got := meetsToolchainFloor(tc.in); got != tc.want {
			t.Errorf("meetsToolchainFloor(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// The one assertion that catches a real build on a real old toolchain.
//
// With GOTOOLCHAIN at its default of `auto`, go.mod's toolchain directive
// switches the go command up before this test compiles, so it never fires. With
// GOTOOLCHAIN=local, which defeats that switch, this is the only thing left to
// catch the old toolchain. Something NEWER passes: this is a floor, not a pin.
func TestThisBuildMeetsTheToolchainFloor(t *testing.T) {
	if !meetsToolchainFloor(runtime.Version()) {
		t.Fatalf("built by %s, below the floor of %s -- go1.26.5 and earlier carry eight "+
			"reachable Go standard-library advisories. go.mod's toolchain directive normally "+
			"switches for you; GOTOOLCHAIN=local defeats it.", runtime.Version(), minToolchain)
	}
}

// The floor is stated twice -- here and in go.mod, which is the only statement
// the go command and scripts/build-release.sh act on -- so this asserts the two
// agree. Otherwise the constant could be raised and the directive left behind.
func TestGoModPinsTheSameToolchainFloor(t *testing.T) {
	// go test runs with the working directory set to the package's own
	// directory, so the module root is exactly one level up.
	b, err := os.ReadFile("../go.mod")
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}
	var got string
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "toolchain" {
			got = f[1]
		}
	}
	if got == "" {
		t.Fatalf("go.mod carries no toolchain directive; without one the release script "+
			"builds against whatever Go is on PATH, which is how the shipped binaries came "+
			"to be go1.26.5. Add: toolchain %s", minToolchain)
	}
	if got != minToolchain {
		t.Errorf("go.mod says toolchain %s, this package's floor is %s -- they must be the same string", got, minToolchain)
	}
}

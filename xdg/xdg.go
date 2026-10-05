// Package xdg resolves the two base directories draftplane keeps state under,
// and is the one place their environment variables are read.
//
// It exists because nine call sites had written the same three lines --
// getenv, fall back to a home-relative default, join -- and none of them
// validated what came back. The spec they implement is explicit that they must:
//
//	All paths set in these environment variables must be absolute. If an
//	implementation encounters a relative path in any of these variables it
//	should consider the path invalid and ignore it.
//	  -- XDG Base Directory Specification
//
// DRAFTPLANE REFUSES RATHER THAN IGNORING, which is a deliberate departure from
// that last word. Ignoring suits a desktop application that must start whatever
// its environment says; here the fallback is the developer's REAL state, so a
// shell someone set up to be a separate identity would quietly read and write
// their own pseudonym, plan list and working set instead. That failure is
// invisible -- everything succeeds, against the wrong home -- and the check it
// silently breaks is exactly the one an isolated shell was created to make. A
// refusal costs one message; a silent fallback costs a session's worth of
// results nobody can trust.
//
// THE MISTAKE THIS CATCHES IS ALMOST ALWAYS QUOTING. `export
// XDG_CONFIG_HOME=~/x` expands, because assignment is a tilde-expansion
// context; `export XDG_CONFIG_HOME="~/x"` does not, and the tilde survives as
// an ordinary character. filepath.Join then produces a RELATIVE path, which the
// operating system resolves against the process's working directory -- so
// draftplane created a literal `~` directory wherever it happened to be run
// from, reported success, and put a different copy of the state under every
// directory the human started it in.
package xdg

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	// ConfigHomeVar and DataHomeVar are named rather than spelled at each use
	// so the refusal below can quote the variable it is talking about.
	ConfigHomeVar = "XDG_CONFIG_HOME"
	DataHomeVar   = "XDG_DATA_HOME"
)

// ConfigHome is $XDG_CONFIG_HOME, or ~/.config when it is unset or empty.
func ConfigHome() (string, error) {
	return base(ConfigHomeVar, ".config")
}

// DataHome is $XDG_DATA_HOME, or ~/.local/share when it is unset or empty.
func DataHome() (string, error) {
	return base(DataHomeVar, filepath.Join(".local", "share"))
}

// base resolves one variable against one home-relative default.
//
// An empty value is treated as unset, which is what the spec says and what
// every call site this replaced already did: `export XDG_CONFIG_HOME=` is a
// variable that carries no path, not a request to use the current directory.
func base(name, fallback string) (string, error) {
	if v := os.Getenv(name); v != "" {
		if !filepath.IsAbs(v) {
			return "", fmt.Errorf(
				"%s must be an absolute path, but is %q", name, v)
		}
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, fallback), nil
}

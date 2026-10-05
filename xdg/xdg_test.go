package xdg_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/draftplane/draftplane/xdg"
)

// TestARelativeValueIsRefused is the whole point of this package. Before it,
// nine call sites joined the variable straight onto a filename, so a relative
// value produced a relative path -- resolved by the OS against whatever
// directory draftplane happened to be started in. The literal case that found
// it was a quoted tilde, which the shell does not expand, and draftplane
// created a directory actually named "~" and reported success.
func TestARelativeValueIsRefused(t *testing.T) {
	for _, tt := range []struct {
		name, value string
	}{
		{"a quoted tilde, the case that found this", "~/user-tests/alice"},
		{"a bare relative path", "state"},
		{"an explicitly relative path", "./state"},
		{"a parent-relative path", "../state"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, v := range []string{xdg.ConfigHomeVar, xdg.DataHomeVar} {
				t.Setenv(v, tt.value)
			}
			for fn, get := range map[string]func() (string, error){
				"ConfigHome": xdg.ConfigHome, "DataHome": xdg.DataHome,
			} {
				got, err := get()
				if err == nil {
					t.Fatalf("%s() = %q, nil for %q; a relative base is resolved against the working "+
						"directory, so state would follow the human around", fn, got, tt.value)
				}
				// The message has to carry BOTH the variable and the value: a
				// refusal that names neither leaves someone re-reading their
				// shell profile to find which of the two is wrong.
				if !strings.Contains(err.Error(), tt.value) {
					t.Errorf("%s() error does not quote the offending value: %v", fn, err)
				}
			}
		})
	}
}

// TestTheRefusalNamesTheVariableItIsAbout guards the half a table cannot: with
// only one of the two set, the error must name THAT one. Both readers share
// base(), so an error built from the wrong constant would still mention "an
// XDG variable" and read fine.
func TestTheRefusalNamesTheVariableItIsAbout(t *testing.T) {
	t.Setenv(xdg.ConfigHomeVar, "relative")
	t.Setenv(xdg.DataHomeVar, "")
	if _, err := xdg.ConfigHome(); err == nil || !strings.Contains(err.Error(), xdg.ConfigHomeVar) {
		t.Errorf("ConfigHome() error = %v, want it to name %s", err, xdg.ConfigHomeVar)
	}
	t.Setenv(xdg.ConfigHomeVar, "")
	t.Setenv(xdg.DataHomeVar, "relative")
	if _, err := xdg.DataHome(); err == nil || !strings.Contains(err.Error(), xdg.DataHomeVar) {
		t.Errorf("DataHome() error = %v, want it to name %s", err, xdg.DataHomeVar)
	}
}

// TestAnAbsoluteValueIsTakenAndUnsetFallsBack pins the two paths that must keep
// working, because a refusal that also refused valid input would be caught by
// nobody until someone's real state stopped opening.
func TestAnAbsoluteValueIsTakenAndUnsetFallsBack(t *testing.T) {
	abs := t.TempDir()
	t.Setenv(xdg.ConfigHomeVar, abs)
	t.Setenv(xdg.DataHomeVar, abs)
	for fn, get := range map[string]func() (string, error){
		"ConfigHome": xdg.ConfigHome, "DataHome": xdg.DataHome,
	} {
		got, err := get()
		if err != nil || got != abs {
			t.Errorf("%s() = %q, %v; want %q", fn, got, err, abs)
		}
	}

	// Empty is UNSET, not "use the working directory" -- which is what every
	// call site this replaced already assumed, and what the spec says.
	t.Setenv(xdg.ConfigHomeVar, "")
	t.Setenv(xdg.DataHomeVar, "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory here: %v", err)
	}
	if got, err := xdg.ConfigHome(); err != nil || got != filepath.Join(home, ".config") {
		t.Errorf("ConfigHome() = %q, %v; want %q", got, err, filepath.Join(home, ".config"))
	}
	want := filepath.Join(home, ".local", "share")
	if got, err := xdg.DataHome(); err != nil || got != want {
		t.Errorf("DataHome() = %q, %v; want %q", got, err, want)
	}
}

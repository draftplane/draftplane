package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/draftplane/draftplane/client/config"
)

// userHome points XDG_CONFIG_HOME at a fresh directory and returns the path
// runUser reads and writes. It creates nothing: a fresh config, minted on
// first read, is a case below.
func userHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", home)
	return filepath.Join(home, "draftplane", "config.json")
}

// TestRunUserGetPrintsAMintedNameAndIsStable pins the shape that makes a
// bare `draftplane user` trustworthy: it prints exactly what
// config.MachinePseudonym would mint and return, and a second call reads back
// the SAME name rather than minting again.
func TestRunUserGetPrintsAMintedNameAndIsStable(t *testing.T) {
	path := userHome(t)

	var first bytes.Buffer
	if err := runUser(&first, nil); err != nil {
		t.Fatalf("runUser: %v", err)
	}
	got := strings.TrimSpace(first.String())
	if got == "" {
		t.Fatal("runUser printed an empty name on a fresh config")
	}

	var second bytes.Buffer
	if err := runUser(&second, nil); err != nil {
		t.Fatalf("runUser: %v", err)
	}
	if again := strings.TrimSpace(second.String()); again != got {
		t.Errorf("runUser printed %q then %q; the second read must not re-mint", got, again)
	}

	// Whatever it printed must be exactly what is on disk.
	stored, err := config.MachinePseudonym(path)
	if err != nil {
		t.Fatalf("MachinePseudonym: %v", err)
	}
	if stored != got {
		t.Errorf("runUser printed %q, but config.json holds %q", got, stored)
	}
}

// TestRunUserSetThenGetRoundTrips: setting a name and then reading it back
// must yield exactly that name, and the set output must name the file it
// wrote -- naming it is the whole discoverability point (theme_test.go's
// TestRunThemeSetWritesAndConfirms makes the same assertion for theme.json).
func TestRunUserSetThenGetRoundTrips(t *testing.T) {
	path := userHome(t)

	var setOut bytes.Buffer
	if err := runUser(&setOut, []string{"luke"}); err != nil {
		t.Fatalf("runUser set luke: %v", err)
	}
	if !strings.Contains(setOut.String(), "luke") {
		t.Errorf("set output %q does not name the user name it set", setOut.String())
	}
	if !strings.Contains(setOut.String(), path) {
		t.Errorf("set output %q does not name the file it wrote", setOut.String())
	}

	var getOut bytes.Buffer
	if err := runUser(&getOut, nil); err != nil {
		t.Fatalf("runUser get: %v", err)
	}
	if got := strings.TrimSpace(getOut.String()); got != "luke" {
		t.Errorf("runUser printed %q after setting luke, want luke", got)
	}
}

// TestRunUserRefusals covers the arguments that must never reach
// config.SetMachinePseudonym: too many words, and an empty or blank one. A
// refusal must also touch no file -- a command that errors out has no
// business minting a pseudonym nobody asked for.
func TestRunUserRefusals(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "two arguments", args: []string{"Luke", "Skywalker"}, want: `unexpected argument "Skywalker"`},
		{name: "empty", args: []string{""}, want: "name a user name"},
		{name: "blank", args: []string{"   "}, want: "name a user name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := userHome(t)
			var out bytes.Buffer
			err := runUser(&out, tc.args)
			if err == nil {
				t.Fatalf("runUser(%v) = nil error, want a refusal", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to contain %q", err, tc.want)
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Errorf("a refused runUser wrote %s; it must touch no file", path)
			}
		})
	}
}

// TestRunDispatchesUser pins the switch arm in run() (main.go): every other
// test here calls runUser directly, so without this the command could be
// unreachable from the binary and the suite would stay green. It drives the
// SET form because the file left on disk is the only observation that
// separates a get that ran from one that did not -- the same reasoning as
// theme_test.go's TestRunDispatchesTheme.
func TestRunDispatchesUser(t *testing.T) {
	path := userHome(t)
	if err := run([]string{"user", "luke"}); err != nil {
		t.Fatalf("run user luke: %v", err)
	}
	name, err := config.MachinePseudonym(path)
	if err != nil {
		t.Fatalf("MachinePseudonym: %v", err)
	}
	if name != "luke" {
		t.Errorf("user = %q after `draftplane user luke`, want luke -- run() may not dispatch the subcommand", name)
	}
}

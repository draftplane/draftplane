package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/draftplane/draftplane/theme"
)

// themeHome points XDG_CONFIG_HOME at a fresh directory and returns the path
// runTheme reads and writes. It creates nothing: an absent file is a case below.
func themeHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", home)
	return filepath.Join(home, "draftplane", "theme.json")
}

// TestRunThemeGetPrintsTheConfiguredSetting pins the shape that makes this
// command scriptable: stdout carries the SETTING, bare, and an absent file
// prints "system" rather than nothing.
//
// It asserts on the setting, not the effective preset: detection asks the
// terminal over OSC 11, so down a pipe there is no terminal to ask.
func TestRunThemeGetPrintsTheConfiguredSetting(t *testing.T) {
	cases := []struct {
		name  string
		write string // theme to Save first; empty writes no file at all
		want  string
	}{
		{name: "no file at all reads as system", write: "", want: "system"},
		{name: "a configured preset", write: "dark", want: "dark"},
		{name: "an explicit system", write: "system", want: "system"},
		{name: "the other preset", write: "light", want: "light"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := themeHome(t)
			if tc.write != "" {
				if err := theme.Save(path, tc.write); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			if err := runTheme(&out, nil); err != nil {
				t.Fatalf("runTheme: %v", err)
			}
			if got := strings.TrimSpace(out.String()); got != tc.want {
				t.Errorf("runTheme printed %q, want %q", got, tc.want)
			}
			// Whatever it printed must be a value the setter accepts.
			if _, err := theme.Setting(strings.TrimSpace(out.String())); err != nil {
				t.Errorf("runTheme printed %q, which theme.Setting refuses: %v", out.String(), err)
			}
		})
	}
}

func TestRunThemeSetWritesAndConfirms(t *testing.T) {
	for _, name := range theme.SettingNames() {
		t.Run(name, func(t *testing.T) {
			path := themeHome(t)
			var out bytes.Buffer
			if err := runTheme(&out, []string{name}); err != nil {
				t.Fatalf("runTheme set %q: %v", name, err)
			}
			got, _, err := theme.Load(path)
			if err != nil {
				t.Fatalf("Load after set: %v", err)
			}
			if got != name {
				t.Errorf("file holds %q, want %q", got, name)
			}
			if !strings.Contains(out.String(), name) {
				t.Errorf("output %q does not name the theme it set", out.String())
			}
			if !strings.Contains(out.String(), path) {
				t.Errorf("output %q does not name the file it wrote; naming it is the whole discoverability point", out.String())
			}
		})
	}
}

// TestRunThemeSetSystemSaysWhatWillHappen: "system" is the one setting whose
// effect is not self-evident from the word, so only it gets an explaining line.
func TestRunThemeSetSystemSaysWhatWillHappen(t *testing.T) {
	themeHome(t)
	var sys, preset bytes.Buffer
	if err := runTheme(&sys, []string{"system"}); err != nil {
		t.Fatal(err)
	}
	if err := runTheme(&preset, []string{"dark"}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(sys.String(), "\n") < 2 {
		t.Errorf("system output = %q, want a second line explaining detection", sys.String())
	}
	if strings.Count(preset.String(), "\n") != 1 {
		t.Errorf("preset output = %q, want exactly one line", preset.String())
	}
}

func TestRunThemeRefusals(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "an unknown theme", args: []string{"neon-nowhere"}, want: "neon-nowhere"},
		{name: "a typo", args: []string{"lgiht"}, want: "lgiht"},
		{name: "too many arguments", args: []string{"dark", "light"}, want: "light"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := themeHome(t)
			var out bytes.Buffer
			err := runTheme(&out, tc.args)
			if err == nil {
				t.Fatalf("runTheme(%v) = nil error, want a refusal", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to name %q", err, tc.want)
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Errorf("a refused runTheme wrote %s; it must touch no file", path)
			}
		})
	}
}

func TestRunThemeRefusalNamesTheLegalValues(t *testing.T) {
	themeHome(t)
	var out bytes.Buffer
	err := runTheme(&out, []string{"neon-nowhere"})
	if err == nil {
		t.Fatal("want a refusal")
	}
	for _, name := range theme.SettingNames() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("err = %q, want it to list the legal value %q", err, name)
		}
	}
}

// TestThemeRefusesAnEmptyOrBlankSetting covers the arm that reads the argument
// before theme.Save sees it. Both rows would otherwise reach Save and come back
// as `unknown theme ""`, which names no remedy; the blank row would not have
// reached it at all before the trim.
func TestThemeRefusesAnEmptyOrBlankSetting(t *testing.T) {
	for _, arg := range []string{"", "   "} {
		path := themeHome(t)
		var buf bytes.Buffer
		err := runTheme(&buf, []string{arg})
		if err == nil {
			t.Fatalf("runTheme(%q) = nil; an unnamed setting must be refused", arg)
		}
		if !strings.Contains(err.Error(), "name a setting") {
			t.Errorf("runTheme(%q) said %q, want the remedy that names the get form", arg, err)
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Errorf("runTheme(%q) wrote %s; a refusal must not create the file", arg, path)
		}
	}
}

// TestThemeTrimsTheSettingItIsGiven is the other half of that trim: a value a
// shell handed over with spaces around it is the value the user typed.
func TestThemeTrimsTheSettingItIsGiven(t *testing.T) {
	path := themeHome(t)
	var buf bytes.Buffer
	if err := runTheme(&buf, []string{" light "}); err != nil {
		t.Fatalf("runTheme(\" light \"): %v", err)
	}
	name, _, err := theme.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if name != "light" {
		t.Errorf("theme = %q, want light", name)
	}
}

// TestRunDispatchesTheme pins the switch arm: every other test here calls
// runTheme directly, so without this the command could be unreachable from the
// binary and the suite would stay green. It drives the SET form because the
// file left on disk is the only observation that separates a get that ran from
// one that did not.
func TestRunDispatchesTheme(t *testing.T) {
	path := themeHome(t)
	if err := run([]string{"theme", "light"}); err != nil {
		t.Fatalf("run theme light: %v", err)
	}
	name, _, err := theme.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if name != "light" {
		t.Errorf("theme = %q after `draftplane theme light`, want light -- run() may not dispatch the subcommand", name)
	}
}

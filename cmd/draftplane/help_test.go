package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
// run() prints help to the real os.Stdout by design, so a test that drives
// run() rather than writeHelp has to take it back this way. Driving run() is the point of the tests below: the
// guarantee being pinned is about the DISPATCH, not about the page.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	runErr := fn()
	os.Stdout = saved
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return b.String(), runErr
}

func TestTheCommandTable(t *testing.T) {
	var got []string
	for _, c := range commands {
		got = append(got, c.name)
	}
	want := []string{"review", "theme", "mcp", "version"}
	if !slices.Equal(got, want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
}

// TestHelpNeverReachesACommandsArgumentParsing is the load-bearing test in this
// file, and it is a REFUSAL test wearing a help test's clothes.
//
// review reads its first argument as a plan path and theme reads its as a
// setting. If --help reached either, `draftplane review --help` would look for
// a plan file called "--help" and `draftplane theme --help` would try to save a
// theme of that name -- a command that WRITES theme.json, reached by someone
// who typed the universal "explain yourself" flag.
//
// So the assertion is not "help printed something". It is that the output is
// EXACTLY the command's page and that nothing on disk moved: both XDG homes are
// this test's own and are required to be empty afterwards, which is what would
// catch a command that got far enough to resolve a path or save a setting.
func TestHelpNeverReachesACommandsArgumentParsing(t *testing.T) {
	for _, c := range commands {
		t.Run(c.name, func(t *testing.T) {
			configHome, dataHome := t.TempDir(), t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", configHome)
			t.Setenv("XDG_DATA_HOME", dataHome)

			got, err := captureStdout(t, func() error { return run([]string{c.name, "--help"}) })
			if err != nil {
				t.Fatalf("run([%q --help]) = %v, want nil -- help is a success", c.name, err)
			}
			if got != c.help {
				t.Errorf("run([%q --help]) did not print that command's page:\ngot:\n%s", c.name, got)
			}
			for _, home := range []string{configHome, dataHome} {
				entries, err := os.ReadDir(home)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Errorf("%q --help touched %s: %v -- help must answer before a command reads or writes anything",
						c.name, filepath.Base(home), entries)
				}
			}
		})
	}
}

// TestEveryCommandHasHelpThatNamesIt is the anti-vacuity guard for the test
// above, which compares against c.help and would pass just as happily if a
// command's page were the empty string or another command's.
func TestEveryCommandHasHelpThatNamesIt(t *testing.T) {
	if len(commands) == 0 {
		t.Fatal("the commands table is empty; every test in this file would be vacuous")
	}
	for _, c := range commands {
		if c.summary == "" || c.form == "" {
			t.Errorf("command %q is missing a form or a summary", c.name)
		}
		want := "draftplane " + c.name + " —"
		if !strings.HasPrefix(c.help, want) {
			t.Errorf("command %q's page does not open with %q:\n%s", c.name, want, firstLine(c.help))
		}
	}
}

// TestTheThreeSpellingsOfHelpAreTheSameBytes pins that `--help`, `-h` and
// `help <command>` are one answer rather than three that drift. The comparison
// is byte equality rather than "all three printed something", because three
// doors onto one page is only worth having while they cannot disagree.
func TestTheThreeSpellingsOfHelpAreTheSameBytes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, c := range commands {
		t.Run(c.name, func(t *testing.T) {
			var pages []string
			for _, args := range [][]string{{c.name, "--help"}, {c.name, "-h"}, {"help", c.name}} {
				got, err := captureStdout(t, func() error { return run(args) })
				if err != nil {
					t.Fatalf("run(%q) = %v, want nil", args, err)
				}
				pages = append(pages, got)
			}
			for i, got := range pages[1:] {
				if got != pages[0] {
					t.Errorf("spelling %d printed different bytes than --help", i+1)
				}
			}
		})
	}
}

// TestTheRootPageAndTheUsageTextNameEveryCommand is what makes the commands
// table the single place a command is written down. Both surfaces are DERIVED
// from it, so this fails when the derivation is replaced by a literal -- which
// is exactly what it replaced.
func TestTheRootPageAndTheUsageTextNameEveryCommand(t *testing.T) {
	root := rootHelp()
	for _, c := range commands {
		if !strings.Contains(root, c.summary) {
			t.Errorf("the root help page does not carry %q's summary", c.name)
		}
		if !strings.Contains(usage, "draftplane "+c.form) {
			t.Errorf("the usage text does not carry %q's form %q:\n%s", c.name, c.form, usage)
		}
	}
}

// TestNoHelpLineIsWiderThanEightyColumns measures every page rather than
// trusting that whoever wrote them counted. Eighty is the width a terminal is
// assumed to have when nothing says otherwise, and a help page that wraps is
// one whose two-column Examples block stops lining up -- the one place the
// wrapping is actually load-bearing.
//
// Measured in RUNES, not bytes: these pages carry em dashes and the arithmetic
// a byte count produces is wrong by two per dash.
//
// ⚠️ LINES CARRYING ONE OF THIS MACHINE'S RESOLVED STATE ROOTS ARE EXEMPT, and
// the exemption is narrow on purpose: it applies to a line containing a path
// stateRoots resolved, and to nothing else. Those lines are DATA rather than
// copy -- the pages print where this machine really keeps its state (see
// stateRoots for why they print the answer instead of describing how to derive
// it) -- and a path cannot be rewrapped, so a developer whose home directory is
// long would otherwise get a red on work that had nothing to do with any of
// these pages. Everything a person WROTE stays measured, including the
// sentences those lines sit under.
//
// TWO LINES QUALIFY TODAY, both in the root page's purge block. The check is
// written as "contains a resolved root" rather than "equals one of two lines"
// so that any page which prints one of these paths is covered by the same rule
// rather than needing a second exemption -- which is what made the move of
// that block onto the root page cost nothing here.
//
// It is not a blanket skip of that section: the exempt lines are found by
// asking stateRoots what it answered, so a section that grew a long
// hand-written line would still fail.
func TestNoHelpLineIsWiderThanEightyColumns(t *testing.T) {
	configRoot, dataRoot := stateRoots()
	pages := map[string]string{"<root>": rootHelp(), "<usage>": usage}
	for _, c := range commands {
		pages[c.name] = c.help
	}
	for name, page := range pages {
		for i, line := range strings.Split(page, "\n") {
			if strings.Contains(line, configRoot) || strings.Contains(line, dataRoot) {
				continue
			}
			if n := utf8.RuneCountInString(line); n > 80 {
				t.Errorf("%s line %d is %d columns:\n%s", name, i+1, n, line)
			}
		}
	}
}

// TestRootHelpPrintsThisMachinesStateRoots is the check that the purge block
// answers rather than describes.
//
// ⚠️ IT DRIVES THE XDG VARIABLES, WHICH IS THE CASE THE OLD COPY GOT WRONG.
// The help used to print `~/.config/draftplane` and `~/.local/share/draftplane`
// with a clause underneath saying those were only the fallbacks -- so the two
// commands it handed a person to paste were wrong for exactly the people who
// had configured anything, and right for everyone who had not. A test run with
// the variables UNSET could not see that, because both spellings agree there.
//
// The absence assertion is the load-bearing half. Printing the resolved roots
// while ALSO leaving the tilde forms in the page would look correct in a
// screenshot and still hand somebody a command that deletes the wrong
// directory, or nothing at all.
func TestRootHelpPrintsThisMachinesStateRoots(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "somewhere-else", "config")
	dataHome := filepath.Join(t.TempDir(), "somewhere-else", "data")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)

	page := rootHelp()
	for _, want := range []string{
		"rm -r " + filepath.Join(configHome, "draftplane"),
		"rm -r " + filepath.Join(dataHome, "draftplane"),
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the root help does not carry %q; it must name where THIS machine keeps its "+
				"state, not where an unconfigured one would:\n%s", want, page)
		}
	}
	// The fallbacks are what stateRoots prints only when xdg REFUSES a root,
	// which these two absolute paths do not. A page carrying both would be
	// telling a configured machine to delete a directory it does not use.
	for _, absent := range []string{fallbackConfigRoot, fallbackDataRoot} {
		if strings.Contains(page, absent) {
			t.Errorf("the root help still carries the fallback %q even though both XDG roots "+
				"resolved:\n%s", absent, page)
		}
	}
}

// TestUnknownCommandsAreAnErrorRatherThanHelp keeps the split this file's
// whole design rests on: help is a SUCCESS on stdout, and everything else is a
// refusal carrying the usage text. Before this, `draftplane --help` was the
// second of those -- usage on stderr, prefixed "draftplane:", exit 1.
func TestUnknownCommandsAreAnErrorRatherThanHelp(t *testing.T) {
	err := run([]string{"reviwe"})
	if err == nil {
		t.Fatal("run([reviwe]) = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), `unknown command "reviwe"`) {
		t.Errorf("error %q does not name the word that was not understood", err)
	}
	if !strings.Contains(err.Error(), "usage: draftplane") {
		t.Errorf("error %q does not show the usage text", err)
	}
	if err := writeHelp(&strings.Builder{}, "reviwe"); err == nil {
		t.Error("writeHelp accepted a command that does not exist")
	}
}

// TestRunSwitchMatchesCommandsTable closes the gap every other test in this
// file leaves open. Most of the tests above iterate `for _, c := range
// commands`, so the table IS the enumeration to them -- a command added to
// the table but never given a case in run's switch (cmd/draftplane/main.go)
// passes every one of them, because wantsHelp answers `theme --help` before
// the switch is ever reached, and only the bare, argument-less form falls
// through to main's "unknown command" default.
//
// It is a STATIC comparison, parsed out of main.go with go/ast rather than by
// calling run itself -- findNewViewCalls' precedent (app/mouse_test.go). This
// file carries none of the XDG guards main's own dispatch needs: run reaches
// a Bubbletea program, a blocking stdio MCP server, and runTheme reading and
// writing the real $XDG_CONFIG_HOME. Comparing sets of names parsed from
// source touches none of that.
//
// The comparison runs both directions: a table entry with no case would leave
// exactly the silent gap above, and a case with no table entry would dispatch
// a command that `draftplane help` can never describe.
func TestRunSwitchMatchesCommandsTable(t *testing.T) {
	cases := findRunSwitchCases(t)
	if len(cases) == 0 {
		t.Fatal("no case labels were found in run's switch: the parse below is broken, and a broken " +
			"parse would pass this test by having nothing left to compare")
	}

	table := map[string]bool{}
	for _, c := range commands {
		table[c.name] = true
	}

	for name := range cases {
		if !table[name] {
			t.Errorf("run's switch (main.go) has a case %q with no entry in the commands table (help.go)", name)
		}
	}
	for name := range table {
		if !cases[name] {
			t.Errorf("the commands table (help.go) has an entry %q with no case in run's switch (main.go) -- "+
				"it would print its own help but answer %q itself with \"unknown command\"", name, name)
		}
	}
}

// findRunSwitchCases parses main.go in this package's own directory and
// returns the set of string case labels on the switch statement inside func
// run. The "default" clause carries no label and contributes nothing.
func findRunSwitchCases(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing main.go: %v", err)
	}

	var run *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Recv == nil && f.Name.Name == "run" {
			run = f
			break
		}
	}
	if run == nil {
		t.Fatal(`main.go has no top-level function named "run"`)
	}

	cases := map[string]bool{}
	ast.Inspect(run, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		for _, stmt := range sw.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, expr := range cc.List {
				lit, ok := expr.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				val, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unquoting case label %s: %v", lit.Value, err)
				}
				cases[val] = true
			}
		}
		return true
	})
	return cases
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

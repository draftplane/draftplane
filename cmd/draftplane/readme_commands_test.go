package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// npmReadme is the text published to the npm registry -- scripts/build-npm.sh
// copies it verbatim into every platform package -- which makes it the command
// reference most readers meet first, ahead of `draftplane --help`.
const npmReadme = "../../npm/README.md"

// readmeRow matches one row of the README's command table. The command name is
// the word after `draftplane`, and it is optional: the table's first row is the
// bare `draftplane`, the plan list, which is not a subcommand.
var readmeRow = regexp.MustCompile("^\\| `draftplane ?([a-z]*)")

// bareREADMERow is the table's `| `draftplane` |` row. It documents starting the
// TUI with no subcommand, so it has no entry in commands and must not be read as
// one naming a command that does not exist.
const bareREADMERow = ""

// TestTheREADMEDocumentsEveryCommand pins the published command table against
// commands, in both directions.
//
// ⚠️ THIS TEST EXISTS BECAUSE THE TABLE ONCE LEFT OUT A SHIPPED COMMAND AND
// NOTHING NOTICED FOR A LONG TIME.
//
// Prose about a command cannot be checked by a machine and is not checked here.
// What is checked is the only part that is mechanical: that the SET of commands
// agrees. A summary that has gone stale still needs a human; a command that was
// added and never documented no longer does.
//
// The precedent for a test reading a sibling file is version_test.go, which
// pins go.mod's toolchain directive against this module's own security floor for
// the same reason: two statements of one fact, in different files, with nothing
// between them.
//
// ⚠️ README.md AT THE REPOSITORY ROOT IS DELIBERATELY NOT PINNED. It documents
// commands in prose, one invocation at a time, under headings that group them by
// what a reader is trying to do rather than by command -- there is no set to
// compare against without inventing a structure the file does not have. It had
// the same gap, and it was fixed by hand in the same change that added this
// test. That asymmetry is a known gap, not an oversight.
func TestTheREADMEDocumentsEveryCommand(t *testing.T) {
	b, err := os.ReadFile(npmReadme)
	if err != nil {
		t.Fatalf("reading %s: %v", npmReadme, err)
	}

	documented := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		m := readmeRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if m[1] == bareREADMERow {
			continue
		}
		documented[m[1]] = true
	}
	if len(documented) == 0 {
		t.Fatalf("%s has no command table rows at all; readmeRow no longer matches the table's shape", npmReadme)
	}

	declared := map[string]bool{}
	for _, c := range commands {
		declared[c.name] = true
	}

	for name := range declared {
		if !documented[name] {
			t.Errorf("`draftplane %s` is a command and %s does not document it; "+
				"add a row to the Commands table", name, npmReadme)
		}
	}
	for name := range documented {
		if !declared[name] {
			t.Errorf("%s documents `draftplane %s`, which is not a command; "+
				"a reader would run it and be told it is unknown", npmReadme, name)
		}
	}

	if t.Failed() {
		t.Logf("commands: %s", sortedKeys(declared))
		t.Logf("documented: %s", sortedKeys(documented))
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

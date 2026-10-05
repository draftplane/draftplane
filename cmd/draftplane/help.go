package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/draftplane/draftplane/xdg"
)

// command is one entry in draftplane's command surface, and this table is the
// ONE place a command's existence is written down. The short usage text every
// error falls back to is DERIVED from it (see usage, below) rather than
// hand-maintained beside it: two lists of the same commands is how one of them
// comes to be missing the command somebody just added, and this file already
// has to hold each command's long help anyway.
//
// form is the usage line minus the leading "draftplane ", so the derivation can
// align them; summary is the one-liner the root page lists; help is the whole
// page, printed verbatim.
type command struct {
	name    string
	form    string
	summary string
	help    string
}

// commands is in the order the root page lists them, which is the order a
// person meets them rather than alphabetical: review is what the product is
// for, theme is the one row that changes nothing but this machine's own
// comfort, mcp is the other audience entirely, and version answers a question
// nobody asks twice.
var commands = []command{
	{name: "review", form: "review [<path>|<plan id>]", summary: "review a plan by file path or plan id", help: reviewHelp},
	{name: "theme", form: "theme [dark|light|system]", summary: "show or set the color theme", help: themeHelp},
	{name: "mcp", form: "mcp [--list-tools]", summary: "command for agent stdio MCP server (add to agent config)", help: mcpHelp},
	{name: "version", form: "version", summary: "print draftplane version", help: versionHelp},
}

// lookupCommand answers the table rather than a switch, so a command that
// exists is a command with help by construction.
func lookupCommand(name string) (command, bool) {
	for _, c := range commands {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

// wantsHelp reports whether args ask for help, and it is asked BEFORE any
// command parses its own arguments. That ordering is the whole point rather
// than a convenience: review reads its first non-flag argument as a plan path
// and theme reads its as a setting, and each would otherwise take --help as
// that argument -- `draftplane review --help` would look for a plan file of
// that name.
//
// Any position matches, because a flag that means "explain yourself" cannot
// also be an argument to the thing it is asking about.
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return false
}

// writeHelp prints one command's page, or the root page for an empty name.
// Help is a SUCCESS: it goes to stdout and the caller exits 0. The usage text
// below is the other half of that split -- it is what an error carries, it goes
// to stderr, and it exits 1. Before this file the product had only the second
// and answered `draftplane --help` with it.
func writeHelp(w io.Writer, name string) error {
	if name == "" {
		_, err := fmt.Fprint(w, rootHelp())
		return err
	}
	c, ok := lookupCommand(name)
	if !ok {
		return fmt.Errorf("unknown command %q\n%s", name, usage)
	}
	_, err := fmt.Fprint(w, c.help)
	return err
}

// usage is the short form every refusal in this package appends. It is derived
// from the table so it cannot omit a command, and it deliberately carries the
// forms alone: a refusal is read by someone who mistyped something, not by
// someone learning the product, and the last line tells them where the rest is.
var usage = buildUsage()

func buildUsage() string {
	var b strings.Builder
	b.WriteString("usage: draftplane")
	for _, c := range commands {
		b.WriteString("\n       draftplane ")
		b.WriteString(c.form)
	}
	b.WriteString("\n\nRun \"draftplane help\" to get started.")
	return b.String()
}

// rootHelp is `draftplane --help`. It is composed rather than a constant for
// two reasons: the Commands block is the table's summaries, so a command added
// to the table appears here without anyone remembering to add it twice; and
// the purge block's two paths come from stateRoots, which prints where THIS
// machine keeps its state rather than describing how to work it out.
//
// The purge block sits after Examples rather than in the prose above, so the
// reference blocks -- Usage, Commands, Options, Examples -- stay contiguous.
// It is an adjacent gesture, not part of using Draftplane, and it reads as one.
func rootHelp() string {
	configRoot, dataRoot := stateRoots()
	var b strings.Builder
	b.WriteString(`draftplane — agent plan review in your terminal

Draftplane is an agent plan review surface in your terminal. It lets you
place feedback threads anywhere on an agent-written plan, directly in your
terminal. Your agents can read threads, answer, and revise. Draftplane
tracks plans, comment threads, and approvals, but never writes to your plan
files.

Usage
  draftplane                               open the plan list
  draftplane <command> [arguments]
  draftplane help <command>

Commands
`)
	for _, c := range commands {
		fmt.Fprintf(&b, "  %-9s %s\n", c.name, c.summary)
	}
	b.WriteString(`
Options
  --help   print this help

Examples
  draftplane                              the plan list
  draftplane review ~/plans/rollout.md    review a plan in a file

`)
	fmt.Fprintf(&b, `To purge local Draftplane storage on this machine:
  rm -r %s
  rm -r %s

Run "draftplane <command> --help" for more detail on a command
`, configRoot, dataRoot)
	return b.String()
}

const reviewHelp = `draftplane review — review a plan

Opens a plan for review. Navigate the plan block by block, place comment
threads, reply, resolve and approve. Draftplane never writes to the file.

With no argument, opens the plan list.

Usage
  draftplane review [<path>|<plan id>]

Arguments
  <path>      a plan markdown file on this machine
  <plan id>   a plan an agent created here

Options
  --help   print this help

Examples
  draftplane review ~/plans/rollout.md    a plan in a file
  draftplane review                       the plan list
`

const themeHelp = `draftplane theme — show or set the color theme

Usage
  draftplane theme [dark|light|system]

Arguments
  dark     set theme to dark
  light    set theme to light
  system   match terminal/system

Options
  --help   print this help

Examples
  draftplane theme            show current theme
  draftplane theme light      set the light theme
`

const mcpHelp = `draftplane mcp — agent stdio MCP server (add to agent config)

Command added to your agent's MCP configuration to connect it to Draftplane's
stdio MCP. Gives your agent plan creation and review capabilities: read a plan
and its threads, comment, reply, resolve, approve, and save or download a plan
Draftplane holds. You do not run this yourself.

Each agent is issued its own identity on first call so that different agents
comment and review individually.

Usage
  draftplane mcp
  draftplane mcp --list-tools

Options
  --list-tools   print the tool names this server registers
  --help         print this help

Examples
  Clients with JSON configuration:
  {
    "mcpServers": {
      "draftplane": {
        "command": "draftplane",
        "args": ["mcp"]
      }
    }
  }

  Claude Code:
  claude mcp add --scope user draftplane -- draftplane mcp
`

const versionHelp = `draftplane version — print the draftplane version

Prints the installed version of draftplane.

Usage
  draftplane version

Options
  --help   print this help

Example
  $ draftplane version
  draftplane v0.2.1
`

// stateRoots answers the two directories THIS MACHINE keeps Draftplane state
// under, resolved live through the same package every one of those stores
// resolves its own path through (xdg.ConfigHome and xdg.DataHome).
//
// ⚠️ IT PRINTS THE ANSWER RATHER THAN DESCRIBING HOW TO WORK IT OUT, and that
// is the whole reason it exists. The purge block used to spell
// `~/.config/draftplane` and `~/.local/share/draftplane` and then explain, in
// a clause underneath, that those are only the XDG fallbacks and a machine
// with either variable set keeps its state somewhere else. That clause is a rule the reader has to
// apply to themselves, in a section whose whole job is to hand somebody two
// commands they can paste -- and the commands were wrong for exactly the
// people who had configured anything. There is no version of that sentence
// that is both short and true, so the sentence is gone and the paths are real.
//
// ⚠️ THE FALLBACKS BELOW ARE FOR A REFUSAL, NOT FOR AN UNSET VARIABLE. An
// unset variable is not an error here -- xdg answers the home-relative default
// and this prints that, expanded. The error arm is xdg's own refusal of a
// RELATIVE path (`export XDG_CONFIG_HOME="~/x"`, where the quotes stop the
// shell expanding the tilde), plus a failed home lookup. In that state every
// other draftplane command refuses outright, so there is no live answer to
// give and nothing this help can truthfully interpolate; the tilde forms are
// printed because they are where the state of a machine whose variable was
// only just broken will actually be. Help is answered before any command
// parses its arguments (see wantsHelp), which is why this page renders at all
// on a machine in that state.
func stateRoots() (configRoot, dataRoot string) {
	configRoot, dataRoot = fallbackConfigRoot, fallbackDataRoot
	if home, err := xdg.ConfigHome(); err == nil {
		configRoot = filepath.Join(home, "draftplane")
	}
	if home, err := xdg.DataHome(); err == nil {
		dataRoot = filepath.Join(home, "draftplane")
	}
	return configRoot, dataRoot
}

// fallbackConfigRoot and fallbackDataRoot are what stateRoots prints when xdg
// REFUSES to resolve a root -- see its doc comment for when that happens and
// why these two spellings are the honest answer in that state. They are the
// tilde forms rather than filepath.Join'd absolutes because there is no home
// directory to join against on the path that reaches them.
const (
	fallbackConfigRoot = "~/.config/draftplane"
	fallbackDataRoot   = "~/.local/share/draftplane"
)

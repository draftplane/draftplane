// Command draftplane reviews plans in the terminal, and serves the review
// tools to agents over MCP. The surface is declared once, in help.go's
// commands table, and every usage line and help page is derived from it --
// so this comment deliberately does NOT restate it. A second copy here
// would have nothing keeping it honest, which is what the list it replaces
// demonstrated: it never gained `draftplane version` at all.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/draftplane/draftplane/app"
	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/client/config"
	"github.com/draftplane/draftplane/client/localfs"
	"github.com/draftplane/draftplane/client/recent"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/keymap"
	"github.com/draftplane/draftplane/mcptools"
	"github.com/draftplane/draftplane/session"
	"github.com/draftplane/draftplane/theme"

	"github.com/draftplane/draftplane/version"
	"github.com/draftplane/draftplane/xdg"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "draftplane:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return runList()
	}

	// HELP IS ANSWERED BEFORE ANY COMMAND PARSES ITS OWN ARGUMENTS, and that
	// ordering is the guarantee rather than an optimisation. review reads its
	// first argument as a plan path and theme reads its as a setting, so a
	// --help reaching either would be taken as that argument -- `draftplane
	// review --help` would look for a plan file called "--help", and
	// `draftplane theme --help` would try to save a theme of that name.
	// Deciding it up here makes that unreachable for every command at once,
	// including commands nobody has written yet, rather than fixing it once per
	// command.
	//
	// `draftplane help` and `draftplane help <command>` are the same answer by
	// a third spelling; a bare `help` is the root page, which is also what
	// `--help` with no command gives.
	if args[0] == "help" {
		topic := ""
		if len(args) > 1 {
			topic = args[1]
		}
		return writeHelp(os.Stdout, topic)
	}
	if wantsHelp(args) {
		if _, ok := lookupCommand(args[0]); ok {
			return writeHelp(os.Stdout, args[0])
		}
		return writeHelp(os.Stdout, "")
	}

	switch args[0] {
	case "review":
		return runReview(args[1:])
	case "theme":
		return runTheme(os.Stdout, args[1:])
	case "mcp":
		return runMCP(args[1:])
	case "version":
		// Short(), not Resolve(): the version is the only thing this command
		// answers. The commit, the toolchain and the platform are facts about
		// how the binary was built rather than about which draftplane this is,
		// and printing them put implementation detail on a surface a user
		// quotes back in a bug report. It is the same value the MCP handshake
		// reports (mcptools.serverVersion), which is what keeps the two from
		// ever disagreeing.
		fmt.Println("draftplane " + version.Short())
		return nil
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

func runReview(args []string) error {
	rawPath, err := parseReviewArgs(args)
	if err != nil {
		return err
	}
	if rawPath == "" {
		// `draftplane review` with no path is the list's front door too — same
		// as bare `draftplane`, just reached through the review subcommand.
		return runList()
	}

	th, err := resolveTheme()
	if err != nil {
		return err
	}

	svc, km, statePath, pseudonym, err := buildDeps()
	if err != nil {
		return err
	}

	recentPath, err := recent.DefaultPath()
	if err != nil {
		return err
	}

	// Decorated like every context app itself originates (app.attributedCtx),
	// though nothing openReviewTarget reaches writes today: leaving the one startup
	// context cmd/draftplane builds undecorated is how the next attribution-bearing
	// call down there becomes a startup failure nobody looked for. The recent
	// record openAndRemember makes once the open succeeds is not that call --
	// RecordOpen takes no context, so it carries no attribution either way.
	s, err := openAndRemember(client.WithAttribution(context.Background(), domain.Attribution{}), svc, rawPath, recentPath, time.Now())
	if err != nil {
		return err
	}

	ch, stopWatch := startWatch(statePath)
	defer stopWatch()

	root := app.NewRootInReview(svc, km, th, pseudonym, ch, s)
	root.SetRecents(recentPath)
	_, err = tea.NewProgram(root).Run()
	return err
}

// openReviewTarget applies review's disambiguation rule to rawPath: an argument
// that resolves to a readable, regular file on this machine IS that file, opened
// live and unchanged (session.Open); anything else is a plan id, opened by
// session.OpenByID. This mirrors mcptools.resolve's identical local-capability
// rule for a source argument, restated rather than imported because that
// function is unexported in another package.
//
// A path that is not a file is asked of the index before it is treated as an
// id, which is the one step that stopped this door dead-ending on the case it
// meets most often: the user reviewed a file yesterday, the file is gone today,
// and they typed the same path again, while this machine's own index knows
// exactly which plan that path belonged to. A HIT opens that plan through
// session.OpenPlan, the same function the list's own door calls, so both doors
// reach the identical session and the identical fault on it. A MISS keeps
// today's error verbatim, which is the right answer for a genuine typo and is
// what makes this step non-vacuous: it widens what a real path can reach, not
// what a wrong one can.
//
// The absolute path is what is asked, never the raw argument: a plan's
// SourceHint is absolutized when it is created, so the raw `./rollout.md` a user
// types would match nothing.
//
// If the id interpretation ALSO fails, statErr (why rawPath was not a file) is
// folded into the returned error rather than discarded: this argument is raw
// human keyboard input, not a value another tool call already validated, so a
// fat-fingered path is the realistic failure this disambiguation has to stay
// honest about. Without it, a mistyped path answers only that no plan has that
// id -- true of the id interpretation, and silent about what actually went
// wrong.
func openReviewTarget(ctx context.Context, svc client.PlanService, rawPath string) (*session.Session, error) {
	abs, statErr := localReviewFile(rawPath)
	if statErr == nil {
		return session.Open(ctx, svc, abs)
	}

	if abs != "" {
		plan, err := svc.ResolvePlan(ctx, abs)
		switch {
		case err == nil:
			return session.OpenPlan(ctx, svc, plan)
		case !errors.Is(err, client.ErrNoPlan):
			// A miss is client.ErrNoPlan and ONLY client.ErrNoPlan. Anything else means
			// the index could not be ASKED -- an unreadable state.json, say -- which
			// must not be folded into the typo sentence below. Swallowing it would
			// report the user's spelling as the problem while this machine's own
			// bookkeeping was the one that failed, and would leave a plan they really
			// do have looking like one they never created.
			return nil, fmt.Errorf("%s: %w", abs, err)
		}
	}

	s, err := session.OpenByID(ctx, svc, domain.PlanID(rawPath))
	if err != nil {
		return nil, fmt.Errorf("%w; if you meant a local file: %v", err, statErr)
	}
	return s, nil
}

// openAndRemember is openReviewTarget plus the one write `draftplane review`
// makes that opening does not: it remembers the open for the plan list's
// Recently opened. openReviewTarget itself writes nothing, and stays that way.
// A failed record never fails the review.
func openAndRemember(ctx context.Context, svc client.PlanService, rawPath, recentPath string, now time.Time) (*session.Session, error) {
	s, err := openReviewTarget(ctx, svc, rawPath)
	if err != nil {
		return nil, err
	}
	_ = app.RecordOpen(recentPath, s, now)
	return s, nil
}

// localReviewFile resolves rawPath as a file on this machine -- session.Open's
// own precondition (os.ReadFile), checked ahead of time so a plan id is never
// mistaken for a (nonexistent) relative path and handed to os.ReadFile for its
// own, file-flavored not-found error. A directory is
// deliberately not a file here, matching mcptools.localFile.
//
// The failure is returned, not collapsed to a bool: openReviewTarget folds it
// into its own error when the id interpretation ALSO fails, so a mistyped path
// is diagnosed as a mistyped path rather than left to whatever unrelated
// complaint the id path happens to produce.
//
// The path is returned alongside the failure, and only the two failures that
// HAVE a path return one: absolutization is what makes the string addressable
// at all, and openReviewTarget's index lookup needs the very path the stat
// just failed on.
func localReviewFile(rawPath string) (string, error) {
	abs, err := filepath.Abs(rawPath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return abs, err
	}
	if info.IsDir() {
		return abs, fmt.Errorf("%s is a directory, not a file", abs)
	}
	return abs, nil
}

// runList opens the plan list — bare `draftplane`, or `draftplane review` with
// no path.
func runList() error {
	th, err := resolveTheme()
	if err != nil {
		return err
	}

	svc, km, statePath, pseudonym, err := buildDeps()
	if err != nil {
		return err
	}

	recentPath, err := recent.DefaultPath()
	if err != nil {
		return err
	}

	ch, stopWatch := startWatch(statePath)
	defer stopWatch()

	root := app.NewRoot(svc, km, th, pseudonym, ch)
	root.SetRecents(recentPath)
	_, err = tea.NewProgram(root).Run()
	return err
}

// startWatch arms live refresh on statePath's directory, shared by runReview and
// runList. Best-effort: a watch failure (an unsupported filesystem, say) degrades
// to manual-reload behavior -- nil channel, a stderr notice, and a no-op stop.
func startWatch(statePath string) (<-chan struct{}, func()) {
	ch, stop, err := app.WatchState(filepath.Dir(statePath))
	if err != nil {
		fmt.Fprintln(os.Stderr, "draftplane: live refresh disabled:", err)
		return nil, func() {}
	}
	return ch, stop
}

// pseudonymFallback is what resolvePseudonym reports when it cannot obtain this
// machine's real one -- an unwritable or unreadable $XDG_CONFIG_HOME, say.
// Rendered as "user-unknown-pseudonym" (ui.FormatAttribution), it is deliberately
// not itself a plausible mint -- identity.MintPair's words are lowercase-
// hyphenated adjective-noun pairs with no third segment -- so it never reads as
// if this machine's real pseudonym merely happened to be dull, and deliberately
// not blank: an agent-less write must still render as *something*.
const pseudonymFallback = "unknown-pseudonym"

// resolvePseudonym returns this machine's stable display pseudonym for a write
// whose attribution names nobody, minting one under lock on first read
// (client/config.MachinePseudonym). Shared by buildDeps (the TUI) and
// buildMCPTools (the MCP server) -- whichever process reads it first wins the
// mint race and the other reads back the identical value, so a human's TUI
// session and their own `draftplane mcp` process never disagree on what to call
// them.
//
// Never fails outward: a pseudonym is a display name, so losing it must never
// stop Draftplane from starting on either door. That matters more on
// buildMCPTools' door, a stdio JSON-RPC channel with no way to prompt or
// recover from a startup error. Falls back to pseudonymFallback on any failure.
func resolvePseudonym() string {
	path, err := config.DefaultPath()
	if err != nil {
		return pseudonymFallback
	}
	pseudonym, err := config.MachinePseudonym(path)
	if err != nil {
		return pseudonymFallback
	}
	return pseudonym
}

// buildDeps constructs the dependencies shared by every interactive subcommand
// (review and list): the plan service, the keymap, the state.json path the
// watcher watches, and the machine pseudonym the review UI substitutes for a
// write whose attribution names nobody (see ui.FormatAttribution). Theme
// resolution stays caller-side: it reads theme.json and, failing that, the
// terminal, and buildDeps has no business touching either.
//
// svc is the local store itself, and no content store is returned beside it:
// content comes from svc.Content, and client/localfs.New derives the store that
// answers it.
func buildDeps() (svc client.PlanService, km keymap.Map, statePath string, pseudonym string, err error) {
	pseudonym = resolvePseudonym()

	statePath, err = dataPath("state.json")
	if err != nil {
		return nil, nil, "", "", err
	}
	// localfs.New only creates statePath's directory lazily, on its first save --
	// so on a fresh install, before any write has happened, it does not exist yet.
	// startWatch, called right after buildDeps returns by both runList and
	// runReview, needs it to exist now: a missing directory fails app.WatchState and
	// permanently degrades that process to manual-reload, even after a later write
	// creates it, since the watch is only armed once at startup.
	//
	// localfs.EnsureDir, not an os.MkdirAll of its own: the directory's mode belongs
	// to the store that owns the file, so it is spelled there once.
	if err = localfs.EnsureDir(statePath); err != nil {
		return nil, nil, "", "", err
	}
	local, err := localfs.New(statePath)
	if err != nil {
		return nil, nil, "", "", err
	}
	kmPath, err := keymap.DefaultPath()
	if err != nil {
		return nil, nil, "", "", err
	}
	km, err = keymap.Load(kmPath)
	if err != nil {
		return nil, nil, "", "", err
	}
	return local, km, statePath, pseudonym, nil
}

// buildMCPTools constructs the mcptools.Tools instance runMCP serves over MCP,
// pulled out of runMCP so a test can exercise the wiring without going through
// its blocking stdio server.Run.
//
// It opens its own local store over the same state.json buildDeps opens for
// the TUI. mcptools stamps each call's own context with a minted or
// caller-supplied agent identity, and the pseudonym comes from
// resolvePseudonym, so an agent's own comments and a human's TUI comments
// substitute the identical name for this machine.
func buildMCPTools(statePath string) (*mcptools.Tools, error) {
	local, err := localfs.New(statePath)
	if err != nil {
		return nil, err
	}
	return mcptools.New(local, resolvePseudonym()), nil
}

func runMCP(args []string) error {
	listTools := false
	for _, a := range args {
		switch a {
		case "--list-tools":
			listTools = true
		default:
			return fmt.Errorf("unknown argument %q\n%s", a, usage)
		}
	}
	if listTools {
		for _, n := range mcptools.ToolNames() {
			fmt.Println(n)
		}
		return nil
	}
	statePath, err := dataPath("state.json")
	if err != nil {
		return err
	}
	tools, err := buildMCPTools(statePath)
	if err != nil {
		return err
	}
	server := mcptools.NewServer(tools)
	return server.Run(context.Background(), &mcp.StdioTransport{})
}

// parseReviewArgs reads review's one argument: the plan path or id, empty when
// absent (`draftplane review` with no path is the list's front door, not an
// error).
//
// It used to also carry a --theme flag, accepted on either side of the path,
// which is why review parsed its arguments by hand instead of reaching for the
// flag package. That flag is gone, so what is left is one positional argument
// and a refusal for anything after it.
func parseReviewArgs(args []string) (string, error) {
	switch len(args) {
	case 0:
		return "", nil
	case 1:
		return args[0], nil
	default:
		// args[1] is the first word past the one plan this takes -- the same
		// bound, and the same reason for naming it, that runTheme states for its
		// own single setting.
		return "", fmt.Errorf("draftplane review takes at most one plan; unexpected argument %q\n%s", args[1], usage)
	}
}

// resolveTheme picks the theme for this session: theme.json if it names one,
// otherwise the terminal is asked. `draftplane theme` is what writes that file.
func resolveTheme() (*theme.Theme, error) {
	themePath, err := theme.DefaultPath()
	if err != nil {
		return nil, err
	}
	_, th, err := theme.Load(themePath)
	if err != nil {
		return nil, err
	}
	// The discriminator is the theme and not the name. Load answers a "system" file
	// with the NAME "system" and a NIL theme, so testing the name would hand a nil
	// *Theme to the renderer for exactly the setting that exists to mean "choose one
	// for me". A missing file and a system file are one state to this function,
	// which is what makes them one state to the user.
	if th != nil {
		return th, nil
	}
	return detectTheme(), nil
}

// detectTheme answers what the terminal itself is, querying the background via
// lipgloss's OSC 11 facility to choose between "dark" and "light".
// lipgloss.HasDarkBackground defaults to true (dark) whenever the query is
// unavailable or errors, so no separate fallback is needed here.
func detectTheme() *theme.Theme {
	name := "light"
	if lipgloss.HasDarkBackground(os.Stdin, os.Stdout) {
		name = "dark"
	}
	th, _ := theme.Lookup(name) // known-good built-in name: never errors
	return th
}

func dataPath(name string) (string, error) {
	dir, err := xdg.DataHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "draftplane", name), nil
}

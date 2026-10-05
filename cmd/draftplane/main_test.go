package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/draftplane/draftplane/app"
	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/client/identity"
	"github.com/draftplane/draftplane/client/localfs"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/mcptools"
	"github.com/draftplane/draftplane/store/localcas"
	"github.com/draftplane/draftplane/theme"
)

// TestBuildDepsCreatesStateDirForWatch: buildDeps must leave statePath's
// directory in place before returning, because runList and runReview arm
// app.WatchState against it immediately afterwards -- before the directory has
// necessarily seen the first write that localfs.New defers creating it to.
// Without it, WatchState's fsnotify.Add fails ENOENT and live refresh silently
// degrades to manual reload for the rest of the process's life.
func TestBuildDepsCreatesStateDirForWatch(t *testing.T) {
	dataHome := filepath.Join(t.TempDir(), "xdg-data")
	configHome := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)

	_, _, statePath, _, err := buildDeps()
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}

	ch, stop, err := app.WatchState(filepath.Dir(statePath))
	if err != nil {
		t.Fatalf("WatchState against buildDeps's statePath dir: %v", err)
	}
	defer stop()
	if ch == nil {
		t.Fatal("WatchState returned a nil channel with no error")
	}
}

// TestDataPathObjectsSiblingMatchesLocalCASDefaultRoot pins that two
// independent computations still land on one directory: client/localfs.Store
// derives its content store as a sibling "objects" directory next to whatever
// path New was given -- in production always dataPath("state.json"), built here
// -- and store/localcas.DefaultRoot resolves there by its own algorithm, across
// every XDG_DATA_HOME shape below.
//
// DefaultRoot has no production caller left. The test is kept anyway because
// client/localfs.New's doc comment argues, in prose, that its path already names
// the one directory this Store's content belongs in BECAUSE the two
// computations agree -- and prose is not a guarantee.
func TestDataPathObjectsSiblingMatchesLocalCASDefaultRoot(t *testing.T) {
	tests := []struct {
		name string
		xdg  string
	}{
		{"XDG_DATA_HOME unset, both fall back to the home directory", ""},
		{"a plain absolute path", filepath.Join(t.TempDir(), "xdg-data")},
		{"a path with a trailing slash", filepath.Join(t.TempDir(), "xdg-data") + string(filepath.Separator)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", tc.xdg)

			statePath, err := dataPath("state.json")
			if err != nil {
				t.Fatalf("dataPath: %v", err)
			}
			fromStatePath := filepath.Join(filepath.Dir(statePath), "objects")

			fromDefaultRoot, err := localcas.DefaultRoot()
			if err != nil {
				t.Fatalf("localcas.DefaultRoot: %v", err)
			}

			if fromStatePath != fromDefaultRoot {
				t.Errorf("sibling of dataPath(\"state.json\") = %s, want localcas.DefaultRoot() = %s -- "+
					"client/localfs.New's own doc comment claims these agree; they no longer do",
					fromStatePath, fromDefaultRoot)
			}
		})
	}

	// THEY MUST ALSO AGREE ABOUT WHAT THEY REFUSE, which is the half a
	// success-only table cannot see. This table used to carry "a relative
	// path" and "." as ordinary rows; both are now refused, because a
	// relative base is resolved against the working directory and the state
	// would follow the human around. If one of these two computations kept
	// accepting such a value while the other refused it, the disagreement
	// would surface as a content store that is simply not where the working
	// set expects -- the exact failure the rest of this test exists to catch,
	// arriving through the door it does not watch.
	t.Run("both refuse a relative base", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", "relative-xdg-data")
		if _, err := dataPath("state.json"); err == nil {
			t.Error("dataPath accepted a relative XDG_DATA_HOME")
		}
		if _, err := localcas.DefaultRoot(); err == nil {
			t.Error("localcas.DefaultRoot accepted a relative XDG_DATA_HOME")
		}
	})
}

// TestMCPWriteAlwaysCarriesAnAgentIdentity pins that mcptools -- not the
// caller's environment -- is what attributes an MCP write. Nothing in this chain
// has ever read $DRAFTPLANE_ACTOR; the sentinel below stays as a guard against
// that mistake arriving here.
//
// A minted identity is random by construction, so this asserts
// identity.Canonical accepts whatever lands in Attribution.Agent rather than a
// fixed string.
func TestMCPWriteAlwaysCarriesAnAgentIdentity(t *testing.T) {
	const sentinel = "draftplane-actor-should-be-ignored"
	t.Setenv("DRAFTPLANE_ACTOR", sentinel)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg-config"))
	statePath := filepath.Join(t.TempDir(), "state.json")

	tools, err := buildMCPTools(statePath)
	if err != nil {
		t.Fatalf("buildMCPTools: %v", err)
	}

	// A bare, undecorated context, exactly like the one the MCP transport hands
	// every call in production: if this call's identity came from anywhere other
	// than mcptools deciding it internally, this would fail closed instead.
	res, err := tools.Save(context.Background(), mcptools.SaveArgs{
		Content: "# Plan\n\nBody text.\n", Source: "notion://mcp-identity-guard",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	local, err := localfs.New(statePath)
	if err != nil {
		t.Fatalf("localfs.New: %v", err)
	}
	versions, err := local.Versions(context.Background(), domain.PlanID(res.Plan.ID))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("got %d versions, want 1", len(versions))
	}
	agent := versions[0].Attribution.Agent
	if agent == "" {
		t.Fatal("Attribution.Agent is empty -- an MCP write must always carry an agent identity")
	}
	if _, ok := identity.Canonical(agent); !ok {
		t.Fatalf("Attribution.Agent = %q, not a valid minted identity", agent)
	}
}

// unwritableXDGConfigHome points XDG_CONFIG_HOME at a directory that exists but
// cannot have a "draftplane" subdirectory created inside it, reproducing the
// error config.MachinePseudonym surfaces on a read-only config home. Registers
// its own cleanup so t.TempDir()'s later removal isn't blocked by the missing
// write bit.
func unwritableXDGConfigHome(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "xdg-config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	return dir
}

// TestBuildDepsSurvivesUnresolvablePseudonym: a machine pseudonym is a display
// name, so failing to mint one (here, an unwritable XDG_CONFIG_HOME) must never
// stop buildDeps from succeeding. What buildDeps returns instead is
// pseudonymFallback, never empty: a write whose attribution names nobody must
// still render as something, not a blank author (see ui.FormatAttribution).
func TestBuildDepsSurvivesUnresolvablePseudonym(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	t.Setenv("XDG_CONFIG_HOME", unwritableXDGConfigHome(t))

	_, _, _, pseudonym, err := buildDeps()
	if err != nil {
		t.Fatalf("buildDeps: %v -- an unresolvable pseudonym must not be fatal", err)
	}
	if pseudonym != pseudonymFallback {
		t.Fatalf("pseudonym = %q, want the fallback %q", pseudonym, pseudonymFallback)
	}
}

// TestBuildMCPToolsSurvivesUnresolvablePseudonym is the same rule at the MCP
// door, where it matters more than on the TUI: stdio JSON-RPC has no way to
// prompt or recover from a startup error, so a fatal resolution here would
// silently take down an agent's whole session over what to call an anonymous
// human.
func TestBuildMCPToolsSurvivesUnresolvablePseudonym(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", unwritableXDGConfigHome(t))
	statePath := filepath.Join(t.TempDir(), "state.json")

	if _, err := buildMCPTools(statePath); err != nil {
		t.Fatalf("buildMCPTools: %v -- an unresolvable pseudonym must not be fatal", err)
	}
}

// newLocalFixture builds a fresh local store under t.TempDir(), for tests that
// drive a *localfs.Store directly rather than through buildDeps.
func newLocalFixture(t *testing.T) *localfs.Store {
	t.Helper()
	local, err := localfs.New(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("localfs.New: %v", err)
	}
	return local
}

// attrCtx stands in for the actor string localfs.New used to take: tests in this
// package that call a *localfs.Store's write methods directly -- rather than
// through buildDeps or mcptools, which decorate a context themselves -- must
// decorate their own. It takes one string and writes two, deriving the
// ActorLogin half through attrLogin rather than accepting it, so no caller can
// make the two equal by accident and leave an assertion that passes whichever
// half was read.
func attrCtx(actorDisplay string) context.Context {
	return client.WithAttribution(context.Background(), domain.Attribution{
		ActorLogin:   attrLogin(actorDisplay),
		ActorDisplay: actorDisplay,
	})
}

// attrLogin derives the ActorLogin every fixture in this file pairs with a
// display name, and guarantees the two are never the same string.
//
// It lowercases and hyphenates whatever it is handed, so its output holds only
// [a-z0-9-] and cannot spell ui.FormatAttribution's agent composite or a
// "user-<pseudonym>" fallback. The mapping is length-preserving in runes, so a
// mapped string plus a non-empty suffix can never equal its own input, whatever
// that input is.
func attrLogin(actorDisplay string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return '-'
	}, actorDisplay) + "-codes"
}

// TestTheWiringCanRepointAPlan covers re-point on the service buildDeps
// hands every door.
//
// client.SetSourceHint finds the capability by asserting
// client.SourceHintSetter on svc, and svc is typed client.PlanService: a
// wrapper that embedded that INTERFACE would not promote
// localfs.Store.SetSourceHint, and every re-point would fail with nothing else
// in this package noticing. It is driven end to end (write, then resolve on the
// new path) rather than shape-asserted, because a forward that compiles and
// reaches the wrong store would satisfy a type assertion just as well.
func TestTheWiringCanRepointAPlan(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg-config"))
	svc, _, _, _, err := buildDeps()
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}

	ctx := attrCtx("alice")
	plan, err := svc.CreatePlan(ctx, "Rate Limiter", "/plans/before.md", "", []byte("# Rate Limiter\n"))
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if err := client.SetSourceHint(ctx, svc, plan.ID, "/plans/after.md"); err != nil {
		t.Fatalf("SetSourceHint through buildDeps' wiring: %v", err)
	}
	resolved, err := svc.ResolvePlan(ctx, "/plans/after.md")
	if err != nil || resolved.ID != plan.ID {
		t.Fatalf("resolve on the new path = %+v, %v, want plan %s", resolved, err, plan.ID)
	}
}

// TestResolveThemeHonoursTheFile is the arm the detect tests cannot reach: with
// no theme.json, and with one naming "system", both doors end at detectTheme,
// so a resolveTheme that never read the file at all would pass them both.
func TestResolveThemeHonoursTheFile(t *testing.T) {
	home := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", home)
	if err := theme.Save(filepath.Join(home, "draftplane", "theme.json"), "light"); err != nil {
		t.Fatal(err)
	}
	th, err := resolveTheme()
	if err != nil {
		t.Fatalf("resolveTheme: %v", err)
	}
	// Compared against the preset rather than a hex literal: this pins that the
	// FILE chose, not what the palette happens to hold today. The th != nil arm
	// returns before detection, so the terminal is never asked and this stays
	// deterministic.
	want, err := theme.Lookup("light")
	if err != nil {
		t.Fatal(err)
	}
	if th != want {
		t.Errorf("resolveTheme with a light file = %v, want the light preset -- the file was not consulted", th)
	}
}

// TestResolveThemeDetectsWhenTheFileSaysSystem is what breaks if the
// discriminator ever becomes "did Load return a name" instead of "did Load
// return a theme": a system file returns the NAME "system" with a NIL theme.
func TestResolveThemeDetectsWhenTheFileSaysSystem(t *testing.T) {
	home := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", home)
	path := filepath.Join(home, "draftplane", "theme.json")
	if err := theme.Save(path, "system"); err != nil {
		t.Fatal(err)
	}
	th, err := resolveTheme()
	if err != nil {
		t.Fatalf("resolveTheme: %v", err)
	}
	if th == nil {
		t.Fatal("a system file must resolve to a detected theme, not a nil one")
	}
}

package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"github.com/clipperhouse/displaywidth"
	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/client/localfs"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/keymap"
	"github.com/draftplane/draftplane/placement"
	"github.com/draftplane/draftplane/reanchor"
	"github.com/draftplane/draftplane/session"
	"github.com/draftplane/draftplane/store/localcas"
	"github.com/draftplane/draftplane/theme"
	"github.com/draftplane/draftplane/ui"
)

const testDoc = `# Rate Limiter Plan

## Context

Requests are currently unbounded and the database suffers under load spikes.

## Design

We will use a token bucket with a burst capacity of fifty requests.
`

type fixture struct {
	svc  client.PlanService
	cas  *localcas.Store
	path string
	ctx  context.Context
}

// attrCtx builds an attribution context from one display name, deriving a
// DIFFERENT ActorLogin half: a fixture holding the same string in both halves
// cannot say which half a renderer read, so it would prove nothing.
func attrCtx(actorDisplay string) context.Context {
	return client.WithAttribution(context.Background(), domain.Attribution{
		ActorLogin:   attrLogin(actorDisplay),
		ActorDisplay: actorDisplay,
	})
}

// attrLogin is the ActorLogin every fixture in this file pairs with a display
// name, and it guarantees the two are never the same string: the rune mapping
// is length-preserving, so a mapped string plus a non-empty suffix can never
// equal its own input, whatever the caller passes. Its output holds only
// [a-z0-9-], so it cannot spell ui.FormatAttribution's agent composite or a
// "user-<pseudonym>" fallback.
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

// newFixtureStore builds a fresh local store at path. Bare, with no
// attribution attached to it: localfs reads the attribution off each write's
// context (client.AttributionFrom), and supplying one here instead would
// make every Model-dispatched write in this package pass whether or not the
// Model stamped its own context at all.
func newFixtureStore(tb testing.TB, path string) client.PlanService {
	tb.Helper()
	local, err := localfs.New(path)
	if err != nil {
		tb.Fatal(err)
	}
	return local
}

func setup(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	svc := newFixtureStore(t, filepath.Join(dir, "state.json"))
	path := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(path, []byte(testDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	return fixture{svc: svc, cas: localcas.New(filepath.Join(dir, "objects")), path: path, ctx: attrCtx("alice")}
}

// openModel takes testing.TB so the benchmarks in search_test.go open a model
// the same way every test does, rather than assembling a second one that drifts.
func openModel(tb testing.TB, f fixture) *Model {
	tb.Helper()
	s, err := session.Open(f.ctx, f.svc, f.path)
	if err != nil {
		tb.Fatal(err)
	}
	m := New(s, keymap.Default(), nil, "", nil)
	if err := m.RefreshFromSession(f.ctx); err != nil {
		tb.Fatal(err)
	}
	m.width, m.height = 100, 30
	m.rerender()
	return m
}

func press(m *Model, keys ...string) *Model {
	cur := tea.Model(m)
	for _, k := range keys {
		var key tea.KeyPressMsg
		switch k {
		case "enter":
			key = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "ctrl+r":
			key = tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
		case "tab":
			key = tea.KeyPressMsg{Code: tea.KeyTab}
		case "shift+tab":
			key = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
		case "pgdown":
			key = tea.KeyPressMsg{Code: tea.KeyPgDown}
		case "pgup":
			key = tea.KeyPressMsg{Code: tea.KeyPgUp}
		default:
			key = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		cur, _ = cur.Update(key)
	}
	return cur.(*Model)
}

// pressComposePost presses the composer's post button the way a reviewer does
// -- tab until the ring lands on it, then enter -- and hands the post's own cmd
// back for the caller to drain. It is what every compose-post site in this
// package drives now that the ctrl+d chord that used to do it has been
// deleted; NOTHING here is shared with the path pane (re-point, which commits
// on enter through pressRepointCommit) or with the plan list's rename, whose
// own ctrl+d stands.
//
// IT WALKS THE RING RATHER THAN ASSIGNING m.composeFocus, so a build that posts
// from the wrong ring stop fails at the sites below -- on their own assertions,
// with the draft either unposted or thrown away -- instead of being papered over
// by their own setup. A build where tab stops moving the ring at all fails HERE,
// by the bound: without one this loop spins forever and thirteen call sites turn
// into a package timeout and a goroutine dump, which names nothing. The bound is
// the RING'S OWN SIZE, so three tabs that have not arrived mean the ring is not
// turning rather than that the walk was too short.
//
// The walk is also what makes it safe to call twice inside ONE compose session,
// which the refusal path does: a second call from a ring already parked on post
// presses enter and nothing else, where a hardcoded single tab would walk on to
// cancel and throw the draft away.
func pressComposePost(t *testing.T, m *Model) (*Model, tea.Cmd) {
	t.Helper()
	if m.mode != modeCompose {
		t.Fatalf("pressComposePost: mode = %v, want modeCompose -- there is no post button to press", m.mode)
	}
	for i := 0; m.composeFocus != composeFocusPost; i++ {
		if i == 3 {
			t.Fatalf("pressComposePost: three tabs did not reach composeFocusPost; the ring is stuck at %v", m.composeFocus)
		}
		m = press(m, "tab")
	}
	cur, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return cur.(*Model), cmd
}

// longDocFixture builds a fixture whose document is long enough that
// scrolling and paging actually move: 60 headed sections, each with its own
// content paragraph.
func longDocFixture(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	svc := newFixtureStore(t, filepath.Join(dir, "state.json"))
	var doc strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&doc, "## Section %d\n\nParagraph content for section %d, its own line in the review pane.\n\n", i, i)
	}
	path := filepath.Join(dir, "long.md")
	if err := os.WriteFile(path, []byte(doc.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return fixture{svc: svc, cas: localcas.New(filepath.Join(dir, "objects")), path: path, ctx: attrCtx("alice")}
}

// TestPageDownAdvancesScrollAndLandsCursorOnScreen pins the page-down
// contract: scroll by viewHeight-2 (leaving two lines of continuity), clamped
// to the document's end, cursor onto a block visible at the new scroll position.
func TestPageDownAdvancesScrollAndLandsCursorOnScreen(t *testing.T) {
	f := longDocFixture(t)
	m := openModel(t, f)
	m.width, m.height = 100, 20
	m.rerender()

	vh := m.viewHeight()
	wantScroll := min(len(m.lines)-vh, vh-2)

	m = press(m, "pgdown")

	if m.scroll != wantScroll {
		t.Fatalf("scroll = %d, want %d (viewHeight-2, clamped)", m.scroll, wantScroll)
	}
	first := ui.FirstLineOf(m.lines, m.CursorBlock())
	if first < m.scroll || first >= m.scroll+vh {
		t.Fatalf("cursor block %d (first line %d) is off-screen: scroll=%d viewHeight=%d", m.CursorBlock(), first, m.scroll, vh)
	}
}

// TestPageDownPastEndClampsAndKeepsCursorVisible drives page down past the end
// of a long document: scroll clamps to the last full viewport and the cursor
// lands on a block visible on that partial last page.
func TestPageDownPastEndClampsAndKeepsCursorVisible(t *testing.T) {
	f := longDocFixture(t)
	m := openModel(t, f)
	m.width, m.height = 100, 20
	m.rerender()
	vh := m.viewHeight()

	pages := len(m.lines)/(vh-2) + 3 // enough presses to sail well past the end
	for i := 0; i < pages; i++ {
		m = press(m, "pgdown")
	}

	if want := len(m.lines) - vh; m.scroll != want {
		t.Fatalf("scroll = %d, want end clamp %d", m.scroll, want)
	}
	first := ui.FirstLineOf(m.lines, m.CursorBlock())
	if first < m.scroll || first >= m.scroll+vh {
		t.Fatalf("cursor block %d (first line %d) off-screen on the last page: scroll=%d viewHeight=%d", m.CursorBlock(), first, m.scroll, vh)
	}
}

// TestPageUpAtTopClampsToZero pins the clamp half: page up from the very top
// of the document must not scroll negative.
func TestPageUpAtTopClampsToZero(t *testing.T) {
	f := longDocFixture(t)
	m := openModel(t, f)
	m.width, m.height = 100, 20
	m.rerender()

	m = press(m, "pgup")

	if m.scroll != 0 {
		t.Fatalf("scroll = %d, want 0 (page up at top must clamp)", m.scroll)
	}
	if m.CursorBlock() != 0 {
		t.Fatalf("cursor = %d, want 0 (top of a clamped page up)", m.CursorBlock())
	}
}

// TestEnsureVisibleScrollingDownLeavesContextMargin pins ensureVisible's
// overshoot contract: revealing a target below the viewport leaves scrollMargin
// lines of following context visible below it.
func TestEnsureVisibleScrollingDownLeavesContextMargin(t *testing.T) {
	f := longDocFixture(t)
	m := openModel(t, f)
	m.width, m.height = 100, 20
	m.rerender()
	vh := m.viewHeight()

	// A block comfortably below the initial viewport but far from the
	// document's end, so the overshoot is not clamped by end-of-document.
	m.cursor = 20
	m.rerender()
	m.ensureVisible()

	target := ui.FirstLineOf(m.lines, m.cursor)
	if target < m.scroll || target >= m.scroll+vh {
		t.Fatalf("target line %d not visible: scroll=%d viewHeight=%d", target, m.scroll, vh)
	}
	if got := m.scroll + vh - 1 - target; got != scrollMargin {
		t.Fatalf("context lines below target = %d, want %d (target should sit %d lines above the bottom edge)", got, scrollMargin, scrollMargin)
	}
}

// TestEnsureVisibleScrollingDownClampsAtDocumentEnd pins the clamp half of
// the down-overshoot contract: revealing a target near the document's end
// must never scroll past it into blank overscroll, short documents included.
func TestEnsureVisibleScrollingDownClampsAtDocumentEnd(t *testing.T) {
	f := longDocFixture(t)
	m := openModel(t, f)
	m.width, m.height = 100, 20
	m.rerender()
	vh := m.viewHeight()

	m.cursor = len(m.blocks) - 1
	m.rerender()
	m.ensureVisible()

	if want := len(m.lines) - vh; m.scroll != want {
		t.Fatalf("scroll = %d, want end clamp %d (overshoot must not scroll past the document end)", m.scroll, want)
	}
	target := ui.FirstLineOf(m.lines, m.cursor)
	if target < m.scroll || target >= m.scroll+vh {
		t.Fatalf("last block's target line %d not visible: scroll=%d viewHeight=%d", target, m.scroll, vh)
	}
}

// TestEnsureVisibleScrollingUpLeavesContextMargin pins the symmetric
// up-overshoot contract: revealing a target above the viewport leaves
// scrollMargin lines of context visible above it.
func TestEnsureVisibleScrollingUpLeavesContextMargin(t *testing.T) {
	f := longDocFixture(t)
	m := openModel(t, f)
	m.width, m.height = 100, 20
	m.rerender()

	// Scroll deep into the document first, so revealing an earlier target
	// isn't clamped by the top boundary and the margin is actually observed.
	m.cursor = 40
	m.rerender()
	m.ensureVisible()
	if m.scroll < scrollMargin+5 {
		t.Fatalf("test setup: scroll = %d too small to observe the up-margin without clamping", m.scroll)
	}

	m.cursor = 5
	m.rerender()
	m.ensureVisible()

	target := ui.FirstLineOf(m.lines, m.cursor)
	vh := m.viewHeight()
	if target < m.scroll || target >= m.scroll+vh {
		t.Fatalf("target line %d not visible: scroll=%d viewHeight=%d", target, m.scroll, vh)
	}
	if got := target - m.scroll; got != scrollMargin {
		t.Fatalf("context lines above target = %d, want %d", got, scrollMargin)
	}
}

// TestEnsureVisibleScrollingUpClampsAtTop pins the clamp half of the
// up-overshoot contract: revealing a target near the top of the document
// must not scroll negative.
func TestEnsureVisibleScrollingUpClampsAtTop(t *testing.T) {
	f := longDocFixture(t)
	m := openModel(t, f)
	m.width, m.height = 100, 20
	m.rerender()

	m.cursor = 20
	m.rerender()
	m.ensureVisible()

	m.cursor = 1 // near top: scroll - margin would otherwise go negative
	m.rerender()
	m.ensureVisible()

	if m.scroll != 0 {
		t.Fatalf("scroll = %d, want 0 (up-margin overshoot must clamp at the top)", m.scroll)
	}
	target := ui.FirstLineOf(m.lines, m.cursor)
	if target < m.scroll || target >= m.scroll+m.viewHeight() {
		t.Fatalf("target line %d not visible after top clamp: scroll=%d", target, m.scroll)
	}
}

// TestEnsureVisibleSmallViewportNeverHidesTarget pins the boundary the naive
// overshoot missed: when viewHeight is at or below scrollMargin (a very short
// terminal, or compose/confirm mode eating rows), applying the full margin
// unclamped scrolls the target itself out of view. The margin must shrink to fit.
func TestEnsureVisibleSmallViewportNeverHidesTarget(t *testing.T) {
	f := longDocFixture(t)
	m := openModel(t, f)
	m.width, m.height = 100, 5 // viewHeight() == 3, equal to scrollMargin
	m.rerender()
	vh := m.viewHeight()

	m.cursor = 20
	m.rerender()
	m.ensureVisible()

	target := ui.FirstLineOf(m.lines, m.cursor)
	if target < m.scroll || target >= m.scroll+vh {
		t.Fatalf("target line %d not visible in a %d-line viewport: scroll=%d", target, vh, m.scroll)
	}
}

func TestRendersDocumentWithSourceMappedCursor(t *testing.T) {
	f := setup(t)
	m := openModel(t, f)
	// Headings render bold+underline; lipgloss v2 emits per-rune SGR
	// sequences for underlined runs, so strip ANSI before substring checks.
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Rate Limiter Plan") {
		t.Fatal("document not rendered")
	}
	if m.CursorBlock() != 0 {
		t.Fatal("cursor should start at block 0")
	}
	m = press(m, "j", "j")
	if m.CursorBlock() != 2 {
		t.Fatalf("cursor = %d after two moves, want 2", m.CursorBlock())
	}
}

func TestThreadsRenderAtPlacements(t *testing.T) {
	f := setup(t)
	// Seed a thread through a real session so it flows back via Placements.
	s, err := session.Open(f.ctx, f.svc, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	blocks := ui.ParseBlocks([]byte(testDoc), nil)
	var target ui.Block
	for _, b := range blocks {
		if strings.Contains(b.Text, "token bucket") {
			target = b
		}
	}
	a, err := session.AnchorForBlockText(testDoc, ui.BlockAnchorSpan(target), target.HeadingPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Comment(f.ctx, a, "why fifty?"); err != nil {
		t.Fatal(err)
	}

	m := openModel(t, f)
	// Walk to the commented block rather than assuming one press reaches it:
	// n is a synonym for down and steps one row.
	for i := 0; i < len(m.blocks) && len(m.views[m.cursor]) == 0; i++ {
		m = press(m, "n")
	}
	m = press(m, "enter")
	view := m.View().Content
	if !strings.Contains(view, "why fifty?") {
		t.Fatal("expanded thread body missing from view")
	}
	// Stripped: the mark and its trailing space are separate Renders, so an
	// SGR reset sits between them in the raw view.
	if !strings.Contains(ansi.Strip(view), "※ ") {
		t.Fatal("thread marker missing")
	}
}

func TestRebindGoverns(t *testing.T) {
	f := setup(t)
	m := openModel(t, f)
	m.km = keymap.Map{"x": keymap.ActMoveDown, "q": keymap.ActQuit}
	m = press(m, "x")
	if m.CursorBlock() != 1 {
		t.Fatal("rebound key must drive the action")
	}
	m = press(m, "j")
	if m.CursorBlock() != 1 {
		t.Fatal("unbound default must be inert after rebind")
	}
}

func TestCommentFlowCreatesPlanAndThread(t *testing.T) {
	f := setup(t)
	m := openModel(t, f)

	// Move off the H1 heading onto the Context paragraph (block 2):
	// headings refuse comments, so the flow starts from a content block.
	m = press(m, "j", "j")
	if m.CursorBlock() != 2 {
		t.Fatalf("cursor = %d, want 2 (content block)", m.CursorBlock())
	}
	// c on a plan-less file → straight to compose; no title panel.
	m = press(m, "c")
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose", m.mode)
	}
	m = press(m, "h", "i")
	m, cmd := pressComposePost(t, m)
	m = drain(t, m, cmd)

	threads, err := f.svc.Threads(f.ctx, m.sess.Plan.ID)
	if err != nil || len(threads) != 1 {
		t.Fatalf("threads = %v, %v", threads, err)
	}
	if threads[0].Comments[0].Body != "hi" {
		t.Fatalf("body = %q", threads[0].Comments[0].Body)
	}
	if m.mode != modeRead {
		t.Fatal("must return to read mode after post")
	}
	// Title is inferred from the fixture's H1, not the filename.
	if m.sess.Plan.Title != "Rate Limiter Plan" {
		t.Fatalf("title = %q, want the inferred H1", m.sess.Plan.Title)
	}
}

// TestCtrlEMovesTheCursorRatherThanOpeningAnEditor pins what was traded:
// compose mode gave ctrl+e back to the textarea, whose own
// KeyMap.LineEnd binds it, so the keystroke a reader spends on end-of-line no
// longer suspends the TUI into $EDITOR.
//
// IT ASSERTS THROUGH THE VALUE AND NOT THE CURSOR'S COORDINATES. Where the
// cursor sits is the textarea's own state to describe, and a test reading it
// would pass against a build that moved the cursor and dropped the keystroke on
// the floor. Typing after the motion is what a reader actually observes, and it
// fails identically for a swallowed ctrl+e and for one that landed wrong.
func TestCtrlEMovesTheCursorRatherThanOpeningAnEditor(t *testing.T) {
	f := setup(t)
	m := openModel(t, f)

	// Headings refuse comments; block 2 is the first content block.
	m = press(m, "j", "j")
	m = press(m, "c")
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose", m.mode)
	}
	m = press(m, "a", "b")

	// ctrl+a is the textarea's own LineStart. Without it the cursor is already
	// at the end of the line and a ctrl+e that did nothing whatsoever would be
	// indistinguishable from one that worked.
	cur, _ := m.Update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
	m = cur.(*Model)
	cur, _ = m.Update(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m = cur.(*Model)
	m = press(m, "c")

	if got := m.ta.Value(); got != "abc" {
		t.Fatalf("value = %q, want \"abc\" -- ctrl+e did not reach the textarea as LineEnd", got)
	}
}

// TestComposeHintNamesNoEditorKey guards the other half of that change, which
// the behaviour test above cannot see. A hint may name fewer keys than its
// mode owns and may NEVER name one its handler does not answer, so dropping
// the binding without its hint clause would leave the bar advertising a
// keystroke compose mode now ignores.
//
// ctrl+d LATER JOINED THE GONE LIST TOO, which is the same rule spent a
// second time on a different key: the post moved onto a button the panel draws,
// and the chord that used to do it is the textarea's own DeleteCharacterForward
// again. The assertion here is the exact reverse of the one this test shipped
// with, and it is EDITED RATHER THAN JOINED BY A SECOND TEST -- two tests
// disagreeing about one constant is how a hint comes to name a dead key at all.
func TestComposeHintNamesNoEditorKey(t *testing.T) {
	for _, gone := range []string{"ctrl+e", "EDITOR", "ctrl+d"} {
		if strings.Contains(composeHint, gone) {
			t.Fatalf("composeHint = %q, still names %q, which compose mode no longer answers", composeHint, gone)
		}
	}
	for _, live := range []string{"esc"} {
		if !strings.Contains(composeHint, live) {
			t.Fatalf("composeHint = %q, does not name %q", composeHint, live)
		}
	}
}

// TestComposeTabCyclesFocus pins the keyboard focus ring over the composer's
// panel: editor, post, cancel, and back to editor -- tab walking it forward,
// shift+tab walking the same ring backward.
//
// IT ASSERTS THROUGH A TYPED KEYSTROKE ACTUALLY LANDING, not merely through
// m.composeFocus toggling: a build that flipped the field without ever
// calling m.ta.Blur() would satisfy a field-only assertion while still
// routing every keystroke into the draft, which is the exact defect a
// focus-stealing button exists to prevent.
//
// NO ctrl+d ANYWHERE IN THIS TEST. This test is about the focus ring alone; a
// later change deletes that binding, and a test that posted through it here
// would break there for a reason having nothing to do with focus.
func TestComposeTabCyclesFocus(t *testing.T) {
	tests := []struct {
		name string
		key  string
		// want is composeFocus after each successive press of key, walking
		// the same three-stop ring forward (tab) or backward (shift+tab),
		// starting from and returning to composeFocusEditor.
		want []composeFocus
	}{
		{
			name: "tab",
			key:  "tab",
			want: []composeFocus{composeFocusPost, composeFocusCancel, composeFocusEditor},
		},
		{
			name: "shift+tab",
			key:  "shift+tab",
			want: []composeFocus{composeFocusCancel, composeFocusPost, composeFocusEditor},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Headings refuse comments; block 2 is the first content block.
			m := press(openModel(t, setup(t)), "j", "j", "c")
			if m.mode != modeCompose {
				t.Fatalf("test setup: mode = %v, want modeCompose", m.mode)
			}
			if m.composeFocus != composeFocusEditor {
				t.Fatalf("test setup: composeFocus = %v, want composeFocusEditor (the composer's zero value)", m.composeFocus)
			}

			for i, want := range tt.want {
				m = press(m, tt.key)
				if m.composeFocus != want {
					t.Fatalf("after %d %s press(es): composeFocus = %v, want %v", i+1, tt.key, m.composeFocus, want)
				}
				before := m.ta.Value()
				m = press(m, "x")
				landed := m.ta.Value() != before
				wantLanded := want == composeFocusEditor
				if landed != wantLanded {
					t.Fatalf("after %d %s press(es) (composeFocus=%v): typed key landed in textarea = %v, want %v",
						i+1, tt.key, want, landed, wantLanded)
				}
			}
		})
	}
}

// TestComposeFocusResetsOnReentry pins the other half of the same rule: a
// position left on the ring by a PRIOR compose session must never leak into
// the next one.
// It also exercises enterCompose's resolved asymmetry (see that function's
// own doc comment) -- the textarea must come back focused without a second,
// separate Focus() at the call site.
func TestComposeFocusResetsOnReentry(t *testing.T) {
	m := press(openModel(t, setup(t)), "j", "j", "c")
	m = press(m, "tab", "tab") // land on cancel
	if m.composeFocus != composeFocusCancel {
		t.Fatalf("test setup: composeFocus = %v, want composeFocusCancel", m.composeFocus)
	}
	m = press(m, "esc")
	if m.mode != modeRead {
		t.Fatalf("test setup: mode = %v, want modeRead after esc", m.mode)
	}

	m = press(m, "c")
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose", m.mode)
	}
	if m.composeFocus != composeFocusEditor {
		t.Fatalf("composeFocus = %v, want composeFocusEditor: a stale ring position leaked across re-entry", m.composeFocus)
	}
	if !m.ta.Focused() {
		t.Fatal("textarea must be focused on re-entry without a caller-side Focus() call")
	}
}

// TestComposeEnterPressesTheFocusedButton pins the key that replaced ctrl+d.
// enter is the only key in this panel whose MEANING depends on the ring: on a
// button it presses that button, on the editor it must still reach the textarea
// as InsertNewline -- and the editor path must still hand back whatever cmd that
// widget returns, which the editor subtest guards through a key that actually
// produces one (see the note inside it).
//
// ctrl+m IS DRIVEN AS ITS OWN SUBTEST because it is the same key and not a
// second one: a terminal sends ASCII CR for both, and this package has been
// bitten twice by an arm that matched only "enter" (app/actions.go's repoint
// pane and app/list.go's re-point row both carry the scar). A build with a bare
// `case "enter"` passes every assertion below under the first subtest and fails
// under this one, with ctrl+m falling through to ta.Update while a button holds
// the focus.
func TestComposeEnterPressesTheFocusedButton(t *testing.T) {
	for _, key := range []struct {
		name string
		msg  tea.KeyPressMsg
	}{
		{"enter", tea.KeyPressMsg{Code: tea.KeyEnter}},
		{"ctrl+m", tea.KeyPressMsg{Code: 'm', Mod: tea.ModCtrl}},
	} {
		t.Run(key.name, func(t *testing.T) {
			t.Run("editor", func(t *testing.T) {
				m := press(openModel(t, setup(t)), "j", "j", "c", "h", "i")
				cur, _ := m.Update(key.msg)
				m = cur.(*Model)
				if got := m.ta.Value(); got != "hi\n" {
					t.Fatalf("ta = %q, want %q -- enter on the editor must reach the textarea as InsertNewline", got, "hi\n")
				}
				if m.mode != modeCompose {
					t.Fatalf("mode = %v, want modeCompose -- a newline is not a post", m.mode)
				}

				// THE CMD THE EDITOR PATH HANDS BACK IS THE TEXTAREA'S OWN, and
				// enter cannot be the key that proves it. MEASURED against
				// bubbles v2.1.1: this textarea returns a nil cmd for enter and
				// for every other ordinary keystroke, so a handler that swallowed
				// the cmd and one that returned it are indistinguishable there --
				// an assertion on enter's own cmd would pin nothing and would
				// fail the day the widget starts blinking its cursor for us.
				//
				// ctrl+v is the one key this editor answers that DOES produce a
				// cmd (textarea.Paste), so it is what stands guard over the
				// `return m, cmd` this arm falls through to. It is ASSERTED AND
				// NEVER RUN: running it reads the machine's real clipboard, which
				// is the whole of what makes it undrivable as a behaviour test.
				if _, cmd := m.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl}); cmd == nil {
					t.Fatal("the editor path swallowed the textarea's own cmd: ctrl+v produced no Paste")
				}
			})

			t.Run("post", func(t *testing.T) {
				f := setup(t)
				m := press(openModel(t, f), "j", "j", "c", "h", "i", "tab")
				if m.composeFocus != composeFocusPost {
					t.Fatalf("test setup: composeFocus = %v, want composeFocusPost", m.composeFocus)
				}
				cur, cmd := m.Update(key.msg)
				m = drain(t, cur.(*Model), cmd)
				if m.mode != modeRead {
					t.Fatalf("mode = %v, want modeRead after the post", m.mode)
				}
				if m.status != "comment posted" {
					t.Fatalf("status = %q, want %q", m.status, "comment posted")
				}
				threads, err := f.svc.Threads(f.ctx, m.sess.Plan.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(threads) != 1 || threads[0].Comments[0].Body != "hi" {
					t.Fatalf("threads = %+v, want one thread carrying the typed draft", threads)
				}
			})

			t.Run("cancel", func(t *testing.T) {
				f := setup(t)
				m := press(openModel(t, f), "j", "j", "c", "h", "i", "tab", "tab")
				if m.composeFocus != composeFocusCancel {
					t.Fatalf("test setup: composeFocus = %v, want composeFocusCancel", m.composeFocus)
				}
				cur, cmd := m.Update(key.msg)
				m = drain(t, cur.(*Model), cmd)
				if m.mode != modeRead {
					t.Fatalf("mode = %v, want modeRead after the cancel", m.mode)
				}
				if got := m.ta.Value(); got != "" {
					t.Fatalf("ta = %q, want the draft dropped", got)
				}
				// The fixture is plan-less, so "nothing was written" is
				// observable without a plan id to ask threads for: a cancel that
				// had dispatched would have created the plan to hold the comment.
				if m.sess.Exists {
					t.Fatal("cancel created a plan: the cancel button dispatched a write")
				}
			})
		})
	}
}

// TestComposeCtrlDDeletesForwardRatherThanPosting is the deletion half of the
// ctrl+d change, and it is the reason that change exists. ctrl+d is bubbles'
// own textarea.KeyMap.DeleteCharacterForward, so for as long as compose mode
// answered it the reader's edit key was also the key that posted -- and a
// posted comment can be edited nowhere in this product. The chord must now do
// the ONE thing the widget under the caret means by it.
func TestComposeCtrlDDeletesForwardRatherThanPosting(t *testing.T) {
	f := setup(t)
	m := press(openModel(t, f), "j", "j", "c", "h", "i")

	// ctrl+a is the textarea's LineStart: it puts the caret back on the h so
	// there IS a character forward of it to delete. Both chords go through
	// Update directly -- press() translates no ctrl-modified key but ctrl+r.
	cur, _ := m.Update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
	m = cur.(*Model)
	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m = drain(t, cur.(*Model), cmd)

	if got := m.ta.Value(); got != "i" {
		t.Fatalf("ta = %q, want %q -- ctrl+d must reach the textarea as DeleteCharacterForward", got, "i")
	}
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose -- ctrl+d still posts", m.mode)
	}
	if m.sess.Exists {
		t.Fatal("a plan was created: ctrl+d still dispatches the post")
	}
}

// TestComposeEscCancelsFromEveryRingPosition holds the exit still while the
// keyboard's other keys move around it. esc is the one key in this panel that
// means the same thing at every stop -- it is not a button and never takes the
// ring's focus -- and a reader who has tabbed onto [ post ] and thought better
// of it must be able to leave without walking the ring to find [ cancel ].
func TestComposeEscCancelsFromEveryRingPosition(t *testing.T) {
	for _, tt := range []struct {
		name  string
		tabs  []string
		focus composeFocus
	}{
		{name: "editor", focus: composeFocusEditor},
		{name: "post", tabs: []string{"tab"}, focus: composeFocusPost},
		{name: "cancel", tabs: []string{"tab", "tab"}, focus: composeFocusCancel},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := setup(t)
			m := press(openModel(t, f), "j", "j", "c", "h", "i")
			m = press(m, tt.tabs...)
			if m.composeFocus != tt.focus {
				t.Fatalf("test setup: composeFocus = %v, want %v", m.composeFocus, tt.focus)
			}
			m = press(m, "esc")
			if m.mode != modeRead {
				t.Fatalf("mode = %v, want modeRead", m.mode)
			}
			if got := m.ta.Value(); got != "" {
				t.Fatalf("ta = %q, want the draft dropped", got)
			}
			if m.sess.Exists {
				t.Fatal("esc created a plan: the cancel dispatched a write")
			}
		})
	}
}

// TestComposeButtonRowReplacesTheHintStrip: the composer's SECOND row is the two
// buttons, and it costs the panel no row at all. One row out, one row in --
// which is the whole reason viewHeight needs no edit here, and the reason
// TestComposePaintedRowsFullyPainted's rows[2:2+m.ta.Height()] slice still
// names the textarea.
//
// THE LABELS ARE SPELLED OUT HERE rather than compared against the constants the
// renderer draws from. What is being pinned is the WORDS ON SCREEN: a test that
// asked the production constants what they said would pass over a row reading
// anything at all.
//
// The row's paint -- exactly m.width cells, no unpainted gap -- is not asserted
// here because TestComposePaintedRowsFullyPainted already walks every row of this
// panel and asserts both, this one included.
func TestComposeButtonRowReplacesTheHintStrip(t *testing.T) {
	f := setup(t)
	m := press(openModel(t, f), "j", "j", "c")
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose", m.mode)
	}
	panel, _ := m.panelViewPainted()
	rows := strings.Split(strings.TrimSuffix(panel, "\n"), "\n")
	if want := m.ta.Height() + 2; len(rows) != want {
		t.Fatalf("panel drew %d rows, want ta.Height()+2 = %d -- the buttons must REPLACE the hint strip, not add a row", len(rows), want)
	}
	buttons := ansi.Strip(rows[1])
	for _, label := range []string{"[ post ]", "[ cancel ]"} {
		if !strings.Contains(buttons, label) {
			t.Fatalf("panel row 1 = %q, does not name %q", buttons, label)
		}
	}
}

// TestComposeButtonRowStartsUnderTheHeaderText puts the '[' of [ post ] in the
// same column as the 'c' of "comment on:" one row above it. The two rows are the
// panel's own two lines of chrome, read one after the other, and a button row
// that begins a column or two to the left of the words naming what is being
// commented on reads as a ragged edge rather than as a second line of the same
// panel.
//
// BOTH COLUMNS ARE MEASURED OFF THE DRAWN ROWS, and neither is compared against
// a 3. The indent is the width of whatever prefix the header draws in front of
// its text -- today "── ", two box-drawing runes and a space -- so a header
// prefix that gains or loses a cell must take the buttons with it, and a test
// naming the number would have to be found and edited by hand for the rows to
// agree again.
//
// IT REFUSES A HEADER THAT DRAWS NO PREFIX AT ALL, because a header whose text
// began at column 0 would make this assertion true of the row the buttons were
// drawn on BEFORE the indent existed: zero equals zero, and the test would pass
// having measured nothing.
//
// ansi.StringWidth AND NOT THE BYTE INDEX on the header side: '─' is three bytes
// and one cell, so the byte offset of the target text is 6 where its column is 3.
// The button side is measured the same way for one reason only -- the two sides
// have to be the same measure to be comparable -- rather than because a lead of
// spaces could ever disagree with itself.
func TestComposeButtonRowStartsUnderTheHeaderText(t *testing.T) {
	f := setup(t)
	m := press(openModel(t, f), "j", "j", "c")
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose", m.mode)
	}
	panel, _ := m.panelViewPainted()
	rows := strings.Split(strings.TrimSuffix(panel, "\n"), "\n")
	header, buttons := ansi.Strip(rows[0]), ansi.Strip(rows[1])

	target := m.composeTarget()
	at := strings.Index(header, target)
	if at < 0 {
		t.Fatalf("the panel header %q does not carry its target %q -- there is no column here to align to", header, target)
	}
	want := ansi.StringWidth(header[:at])
	if want == 0 {
		t.Fatalf("the panel header %q draws nothing in front of its text: this test cannot tell an indented button row from an unindented one", header)
	}
	label := composeButtonBar[0].label
	drawn := strings.Index(buttons, label)
	if drawn < 0 {
		t.Fatalf("the button row %q does not draw %q", buttons, label)
	}
	if got := ansi.StringWidth(buttons[:drawn]); got != want {
		t.Fatalf("%q begins at column %d and the header's own text at column %d -- the buttons must start under the header, not under the rule in front of it", label, got, want)
	}
}

// sgrSpan is one run of printable text from a rendered row together with the
// SGR escape open over it. code is empty for the run in front of the first
// escape -- text the renderer drew with nothing open, which every caller here
// treats as a failure rather than as a ground.
type sgrSpan struct {
	code string
	text string
}

// sgrSpans is ansiSGR's own split (app/painted.go), the same reading
// hasUnpaintedGap does, paired up so a caller does not have to carry the
// off-by-one between the escape list and the text list. groundOver and
// groundAtCell ask two different questions of this one walk -- which escape
// covers this LABEL, which escape covers this CELL -- and share it rather than
// each keeping a private ANSI parser that could drift from the renderer's.
func sgrSpans(raw string) []sgrSpan {
	codes := ansiSGR.FindAllString(raw, -1)
	texts := ansiSGR.Split(raw, -1)
	spans := make([]sgrSpan, len(texts))
	for i, text := range texts {
		spans[i].text = text
		if i > 0 {
			spans[i].code = codes[i-1]
		}
	}
	return spans
}

// groundOver answers the SGR escape that is open over the first run of text
// carrying label in raw -- i.e. the style the renderer drew that label in. It
// reads the rendered row rather than searching for a whole pre-rendered span:
// comparing against st.Chip.Render(label) would only assert that the test
// builds the string the same way the renderer does, which is true of any
// string at all.
func groundOver(t *testing.T, raw, label string) string {
	t.Helper()
	for _, span := range sgrSpans(raw) {
		if !strings.Contains(span.text, label) {
			continue
		}
		if span.code == "" {
			t.Fatalf("%q is drawn with no escape open in front of it: %q", label, raw)
		}
		return span.code
	}
	t.Fatalf("%q does not appear in %q", label, raw)
	return ""
}

// sgrBackground answers the background parameter of one SGR escape -- the
// "48;2;R;G;B" or "48;5;N" run inside it -- and "" when the escape sets no
// background at all, which the widget's own reverse-video cursor cell (a bare
// SGR 7) is the live example of.
//
// IT WALKS THE PARAMETER LIST rather than searching the escape for "48;",
// because a truecolor FOREGROUND whose components happen to spell 48;2 would
// answer such a search with a colour nothing was ever drawn on. Only the
// extended forms are recognised: every style on this path takes its background
// from a theme hex, which lipgloss always emits as truecolor.
func sgrBackground(code string) string {
	params := strings.Split(strings.TrimSuffix(strings.TrimPrefix(code, "\x1b["), "m"), ";")
	for i := 0; i < len(params); i++ {
		span := 1
		if i+1 < len(params) && (params[i] == "38" || params[i] == "48") {
			switch params[i+1] {
			case "2":
				span = 5
			case "5":
				span = 3
			}
		}
		if params[i] == "48" && i+span <= len(params) {
			return strings.Join(params[i:i+span], ";")
		}
		i += span - 1
	}
	return ""
}

// groundAtCell is groundOver's question asked of a COLUMN instead of a glyph:
// the ground open over display cell `cell` of one rendered row. A band painted
// under blank fill carries no text to name it by, so there is nothing for
// groundOver to search for -- the only handle on it is where it sits.
//
// IT ANSWERS THE BACKGROUND ALONE where groundOver answers the whole escape,
// and that asymmetry is forced rather than chosen: lipgloss drops the
// foreground from a span holding no text, so a fill span and a widget-drawn
// span on the IDENTICAL ground spell it with different escapes ("48;2;43;50;60"
// against "38;2;139;138;133;48;2;43;50;60"). Comparing whole escapes would call
// those two different grounds, which is precisely the question this helper
// exists to answer correctly.
//
// CELLS, NOT BYTES OR RUNES. ansi.StringWidth is the same measure lipgloss pads
// by, so a row whose gutter holds a box-drawing prompt rune counts here exactly
// as it does on screen; a byte offset would put the answer several columns left
// of the column the caller named.
func groundAtCell(t *testing.T, row string, cell int) string {
	t.Helper()
	at := 0
	for _, span := range sgrSpans(row) {
		w := ansi.StringWidth(span.text)
		if cell < at+w {
			ground := sgrBackground(span.code)
			if ground == "" {
				t.Fatalf("cell %d of %q is drawn on no ground at all (escape %q)", cell, ansi.Strip(row), span.code)
			}
			return ground
		}
		at += w
	}
	t.Fatalf("cell %d is past the end of %q, which is %d cells wide", cell, ansi.Strip(row), at)
	return ""
}

// TestComposeButtonRowMarksTheFocusedButton is the ring made VISIBLE: tab moves
// the focus (TestComposeTabCyclesFocus) and enter presses whatever holds it
// (TestComposeEnterPressesTheFocusedButton), so a row that drew both buttons the
// same way would leave a reader pressing enter with no way to know which of the
// two they were about to press.
//
// THE GROUNDS ARE NAMED BY THEIR RAW BACKGROUND PARAMETER, the idiom
// TestComposePaintedRowsFullyPainted's stripEscape already uses: dark's Chip is
// #6189e8 (RGB 97,137,232) and dark's Control is #404a59 (RGB 64,74,89), and
// each substring only ever appears in that zone's own background escape.
//
// EDITOR IS A CASE AND NOT AN OMISSION: the zero value of the ring is a state a
// reader spends most of the panel's life in, and the row has to say that NEITHER
// button is armed rather than leaving the previous stop's chip standing.
func TestComposeButtonRowMarksTheFocusedButton(t *testing.T) {
	const (
		chipGround    = "48;2;97;137;232"
		controlGround = "48;2;64;74;89"
	)
	for _, tt := range []struct {
		name  string
		tabs  []string
		focus composeFocus
		armed string // the label wearing the chip, "" for neither
	}{
		{name: "editor", focus: composeFocusEditor},
		{name: "post", tabs: []string{"tab"}, focus: composeFocusPost, armed: "[ post ]"},
		{name: "cancel", tabs: []string{"tab", "tab"}, focus: composeFocusCancel, armed: "[ cancel ]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := setup(t)
			m := press(openModel(t, f), "j", "j", "c")
			m = press(m, tt.tabs...)
			if m.composeFocus != tt.focus {
				t.Fatalf("test setup: composeFocus = %v, want %v", m.composeFocus, tt.focus)
			}
			panel, _ := m.panelViewPainted()
			row := strings.Split(panel, "\n")[1]
			for _, label := range []string{"[ post ]", "[ cancel ]"} {
				ground, want := groundOver(t, row, label), controlGround
				if label == tt.armed {
					want = chipGround
				}
				if !strings.Contains(ground, want) {
					t.Fatalf("%s is drawn on %q, want a ground carrying %s", label, ground, want)
				}
			}
		})
	}
}

// TestTheComposePanelHeadlineWearsThePanelHeadRole is the client catching up
// with the product's reference design, which draws this line in amber --
// theme.Accent's own hex -- while the client spent theme.Focus's blue on it.
//
// THE COLOUR IS NAMED BY ITS RAW FOREGROUND PARAMETER, the idiom
// TestComposeButtonRowMarksTheFocusedButton uses one test up for the button
// grounds. An expectation read back off st.FocusHeader would agree with the
// style about a mutation and stay green on a headline painted in anything at
// all.
//
// ONE PANEL STANDS FOR FIVE. The compose headline, the uncentred confirm
// strips, the relocate header and the plan list's sort modal and rename prompt
// are ONE style; driving the compose panel through the real paint path is what
// proves that style reaches a screen, and theme's
// TestPanelHeadIsDarksGoldAndLeavesLightWhereItWas is what pins the hex it
// carries. Repeating the walk for the other four would measure the same field
// five times.
//
// LIGHT IS A CASE BECAUSE IT DID NOT MOVE. A change asked to touch one preset
// is not tested by the preset it touched: the light row is what fails if the
// role is ever collapsed back onto a single shared token.
func TestTheComposePanelHeadlineWearsThePanelHeadRole(t *testing.T) {
	// The blue this headline used to wear. It is still in the palette as dark's
	// Chip -- the armed button's ground -- which is why its absence is worth
	// asserting rather than implied by the hex above it.
	const wasBlue = "38;2;97;137;232"
	for _, tc := range []struct {
		name string
		want string
	}{
		{"dark", "38;2;226;163;86"}, // #e2a356
		{"light", "38;2;0;105;159"}, // #00699f
	} {
		t.Run(tc.name, func(t *testing.T) {
			th, err := theme.Lookup(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			m := press(openModel(t, setup(t)), "j", "j", "c")
			if m.mode != modeCompose {
				t.Fatalf("mode = %v, want modeCompose -- without the panel open there is no headline to read", m.mode)
			}
			// panelViewPainted reads m.styles directly and never m.lines, so
			// swapping the styles is the whole of driving the panel under the
			// other preset and a rerender would do nothing.
			m.styles = ui.NewStyles(th)

			panel, _ := m.panelViewPainted()
			head := strings.Split(panel, "\n")[0]
			if plain := ansi.Strip(head); !strings.Contains(plain, "comment on:") {
				t.Fatalf("the panel's first row is %q, want the headline -- this is reading the wrong row", plain)
			}
			if !strings.Contains(head, tc.want) {
				t.Fatalf("%s: the headline is painted %q, want a foreground carrying %s", tc.name, head, tc.want)
			}
			if strings.Contains(head, wasBlue) {
				t.Fatalf("%s: the headline still carries %s, the focus blue the marketing site never drew here: %q", tc.name, wasBlue, head)
			}
		})
	}
}

// composeWrappingHeadingDoc is a document whose DRAWN WINDOW holds a heading too
// wide for the frame, and that is the whole of what it is for.
// renderBlockPainted's heading arm neither wraps nor truncates, so lipgloss's own
// Width().Render breaks the heading instead and the ui.Line carrying it comes back
// with an EMBEDDED NEWLINE: two screen rows written under one row of viewHeight's
// budget. That is ui/render.go's live residual, and it is why lineAtFrameRow walks
// the drawn window rather than subtracting from m.scroll.
//
// EVERY OTHER FIXTURE IN THIS FILE USES SHORT HEADINGS, which is why this one had
// to be written rather than borrowed. A button-row hit test built on
// docTopRow + viewHeight() + 1 lands on exactly the right row for all of them, and
// is off by the drawn window's embedded-newline count -- measured at up to +18 rows
// over a corpus of real plans -- for a document shaped like this one.
//
// THE WIDE HEADING SITS BELOW THE BLOCK THE COMPOSER OPENS OVER, deliberately: a
// block underneath it would carry the whole heading in its Block.HeadingPath, the
// panel's own header would then word-wrap under the same Width().Render, and the
// panel would come out a row taller than ta.Height()+2. That is a SECOND source of
// drift, and a fixture carrying both leaves a failure unable to say which one it
// caught.
const composeWrappingHeadingDoc = "# Notes\n\n" +
	"A paragraph the composer opens over.\n\n" +
	"## a heading too wide for this frame a heading too wide for this frame " +
	"a heading too wide for this frame a heading too wide for this frame end\n\n" +
	"Body under the wide heading.\n"

// openComposeOverAWrappingHeading opens the composer over
// composeWrappingHeadingDoc with the cursor on its one ordinary paragraph, and
// REFUSES A FIXTURE THAT HAS STOPPED DRIFTING: if ui ever makes Line.Text one
// screen row again, or the width this opens at stops being narrower than the
// heading, every test built on this helper would still pass while measuring
// nothing at all. The guard is what keeps them honest, and it fails HERE, by
// name, rather than leaving each caller green over a document that no longer
// wraps.
func openComposeOverAWrappingHeading(t *testing.T) (fixture, *Model) {
	t.Helper()
	f, m := openReviewOnDoc(t, composeWrappingHeadingDoc)
	m.cursor = 1
	m.rerender()
	m = press(m, "c")
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose -- without the panel open there is no button row to measure", m.mode)
	}
	embedded := 0
	for i := m.scroll; i < min(m.scroll+m.viewHeight(), len(m.lines)); i++ {
		embedded += strings.Count(m.lines[i].Text, "\n")
	}
	if embedded == 0 {
		t.Fatalf("the drawn window carries no embedded newline at width %d: this fixture no longer drives the drift it exists for", m.width)
	}
	return f, m
}

// TestComposeButtonGeometryIsWhereTheFrameDrewTheRow is measured rather than
// argued: the row and columns the model holds are the row and columns the frame
// actually put the buttons on, on a document where the two disagree with the
// arithmetic anybody would reach for first.
//
// IT ASSERTS THE DISCARDED FORMULA IS WRONG HERE, and that assertion is the
// fixture's own non-vacuity check rather than a second opinion about the code: on
// a short-heading document docTopRow + viewHeight() + 1 gives the right answer, so
// a test that did not check the two disagree could be passing because the
// renderer is right or because nothing was ever at stake.
//
// The stored columns are byte-indexed into the stripped row, which is sound
// because both labels and the row's own spacing are pure ASCII -- and it is a
// fact about today's labels rather than a promise, which is exactly why the spans
// are stored instead of recomputed at the click.
func TestComposeButtonGeometryIsWhereTheFrameDrewTheRow(t *testing.T) {
	_, m := openComposeOverAWrappingHeading(t)
	rows := strings.Split(m.View().Content, "\n")
	drawn := -1
	for i, row := range rows {
		if strings.Contains(ansi.Strip(row), "[ post ]") {
			drawn = i
			break
		}
	}
	if drawn < 0 {
		t.Fatal("no frame row carries [ post ]")
	}
	if m.composeButtons.row != drawn {
		t.Fatalf("stored button row = %d, but the frame drew the buttons on row %d", m.composeButtons.row, drawn)
	}
	if naive := docTopRow + m.viewHeight() + 1; naive == drawn {
		t.Fatalf("docTopRow + viewHeight() + 1 = %d is the row the frame drew: this fixture does not drift, so it cannot tell a stored row from a recomputed one", naive)
	}

	stripped := ansi.Strip(rows[drawn])
	want := map[composeFocus]string{composeFocusPost: "[ post ]", composeFocusCancel: "[ cancel ]"}
	if len(m.composeButtons.spans) != len(want) {
		t.Fatalf("stored %d spans, want one per button (%d)", len(m.composeButtons.spans), len(want))
	}
	for _, sp := range m.composeButtons.spans {
		label, ok := want[sp.focus]
		if !ok {
			t.Fatalf("a span names focus %v, which is not a button", sp.focus)
		}
		delete(want, sp.focus)
		if sp.col < 0 || sp.col+sp.width > len(stripped) {
			t.Fatalf("%s spans columns [%d,%d) of a row %d cells wide", label, sp.col, sp.col+sp.width, len(stripped))
		}
		if got := stripped[sp.col : sp.col+sp.width]; got != label {
			t.Fatalf("columns [%d,%d) of the button row hold %q, but the span says %q", sp.col, sp.col+sp.width, got, label)
		}
	}
}

// TestComposeButtonGeometryIsWrittenOnlyByTheFrame closes the one way the stored
// row could still be wrong: panelViewPainted can only know a PANEL-RELATIVE row,
// because only viewPainted can count the document rows above the panel. If a
// panel render could write that value onto the model, the field would be left
// holding non-empty spans -- which every reader takes as "there are buttons on
// screen at this row" -- beside a row naming a DOCUMENT row near the top of the
// frame. A click would resolve against it and post a comment on the wrong block
// -- the exact failure the stored-geometry field exists to prevent, arriving
// through that same field.
//
// SO THE PANEL DRAW IS RUN AGAINST A MODEL THAT ALREADY HOLDS A COMPLETED
// GEOMETRY, and the geometry has to come back unchanged. Anything that writes
// m.composeButtons from inside panelViewPainted fails here, whatever it writes.
//
// The wrapping-heading fixture is the one to run it on: on a short-heading
// document the panel-relative row and the absolute row can coincide, and this
// would pass over a build that had reintroduced the write.
func TestComposeButtonGeometryIsWrittenOnlyByTheFrame(t *testing.T) {
	_, m := openComposeOverAWrappingHeading(t)
	m.View() // the frame is what completes the geometry
	want := m.composeButtons
	if len(want.spans) == 0 {
		t.Fatal("the frame stored no button spans: there is nothing here to leave unchanged")
	}
	if panel, _ := m.panelViewPainted(); panel == "" {
		t.Fatal("the compose panel drew nothing: this is not exercising the draw it claims to")
	}
	if !reflect.DeepEqual(m.composeButtons, want) {
		t.Fatalf("a bare panel draw changed the stored geometry to %+v, want the frame's own %+v -- only viewPainted may write m.composeButtons", m.composeButtons, want)
	}
}

// TestCommentFlowFilenameFallback covers a doc with no H1: the inferred title
// falls back to the filename stem.
func TestCommentFlowFilenameFallback(t *testing.T) {
	dir := t.TempDir()
	svc := newFixtureStore(t, filepath.Join(dir, "state.json"))
	path := filepath.Join(dir, "no-h1.md")
	noH1 := "## Section\n\nSome content without a top-level heading.\n"
	if err := os.WriteFile(path, []byte(noH1), 0o644); err != nil {
		t.Fatal(err)
	}
	f := fixture{svc: svc, cas: localcas.New(filepath.Join(dir, "objects")), path: path, ctx: attrCtx("alice")}
	m := openModel(t, f)

	// Block 0 is the H2 "Section" heading, which refuses comments; move onto
	// the content paragraph (block 1) first.
	m = press(m, "j")
	if m.CursorBlock() != 1 {
		t.Fatalf("cursor = %d, want 1 (content block)", m.CursorBlock())
	}
	m = press(m, "c")
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose", m.mode)
	}
	m = press(m, "h", "i")
	m, cmd := pressComposePost(t, m)
	m = drain(t, m, cmd)

	if m.sess.Plan.Title != "no-h1" {
		t.Fatalf("title = %q, want the filename stem", m.sess.Plan.Title)
	}
}

func TestCommentOnHeadingCreatesSectionThread(t *testing.T) {
	f := setup(t)
	m := openModel(t, f)

	// Cursor starts on block 0, the H1 heading.
	m = press(m, "c")
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose (heading comment composes a section thread)", m.mode)
	}
	if !strings.Contains(m.View().Content, "(section)") {
		t.Fatal("panel header must mark a section comment")
	}
	m = press(m, "h", "i")
	m, cmd := pressComposePost(t, m)
	m = drain(t, m, cmd)

	threads, err := f.svc.Threads(f.ctx, m.sess.Plan.ID)
	if err != nil || len(threads) != 1 {
		t.Fatalf("threads = %v, %v", threads, err)
	}
	th := threads[0]
	if th.Anchor.Span != "" {
		t.Fatalf("anchor span = %q, want empty (section anchor)", th.Anchor.Span)
	}
	wantPath := []string{"Rate Limiter Plan"}
	if !reflect.DeepEqual(th.Anchor.HeadingPath, wantPath) {
		t.Fatalf("anchor heading path = %v, want %v", th.Anchor.HeadingPath, wantPath)
	}

	// The thread renders at the heading block (block 0), auto-expanded by
	// the post's own expandBlock capture — no extra keypress needed.
	view := m.View().Content
	if !strings.Contains(view, "hi") {
		t.Fatal("expanded thread body missing from view")
	}
}

// quotedHeadingDoc puts a quoted heading inside a real section, which is the
// shape quoted headings take in real plans.
const quotedHeadingDoc = `# Rate Limiter Plan

## Context

Requests are currently unbounded and the database suffers under load spikes.

> ## Someone else's section
>
> Their words.
`

// TestCommentOnQuotedHeadingIsNotASectionComment pins a seam at the layer
// that reads the heading path rather than the one that builds it.
//
// A quoted heading is a real KindHeading, and three sites in this package
// branch on that kind alone and treat the block as A SECTION: the first-comment
// path and relocate both call session.SectionAnchor with the block's
// HeadingPath, and the compose panel labels the target "(section)".
//
// But reanchor.ParseSections matches `^(#{1,6})\s+` anchored at column 0, so a
// `> #` line is no section to it and HeadingPath is THE ENCLOSING SECTION'S:
// the anchor is created against a section the reviewer never selected, and
// ResolveAnchor with an empty span returns the FIRST block with that path,
// redisplaying the thread on the real section heading.
//
// Both write paths are driven because both branch on the kind, and the label is
// asserted with them because a panel that says "(section)" over a span anchor
// is the same wrong answer one layer up.
func TestCommentOnQuotedHeadingIsNotASectionComment(t *testing.T) {
	quotedHeadingBlock := func(t *testing.T, m *Model) int {
		t.Helper()
		for i, b := range m.blocks {
			if b.Kind == ui.KindHeading && b.QuoteDepth > 0 {
				return i
			}
		}
		t.Fatal("fixture assumption broken: no quoted heading block")
		return -1
	}
	// The anchor a quoted heading must get: a SPAN anchor on its own words,
	// which resolves back to the block the reviewer had the cursor on.
	assertAnchoredOnTheQuotedHeading := func(t *testing.T, m *Model, th domain.Thread, target int) {
		t.Helper()
		if th.Anchor.Span == "" {
			t.Fatalf("anchor span is empty, so this was written as a SECTION anchor on %v -- a section the reviewer never selected", th.Anchor.HeadingPath)
		}
		if got := ui.ResolveAnchor(m.blocks, th.Anchor); got != target {
			t.Fatalf("thread displays on block %d, want the quoted heading at %d", got, target)
		}
	}

	t.Run("compose", func(t *testing.T) {
		f := setup(t)
		if err := os.WriteFile(f.path, []byte(quotedHeadingDoc), 0o644); err != nil {
			t.Fatal(err)
		}
		m := openModel(t, f)
		target := quotedHeadingBlock(t, m)
		m.cursor = target
		m.rerender()

		m = press(m, "c")
		if m.mode != modeCompose {
			t.Fatalf("mode = %v, want modeCompose", m.mode)
		}
		if strings.Contains(m.View().Content, "(section)") {
			t.Fatal("the compose panel calls a quoted heading a section")
		}
		m = press(m, "h", "i")
		m, cmd := pressComposePost(t, m)
		m = drain(t, m, cmd)

		threads, err := f.svc.Threads(f.ctx, m.sess.Plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		th, ok := threadByBody(threads, "hi")
		if !ok {
			t.Fatalf("the comment was not written at all: %v", threads)
		}
		assertAnchoredOnTheQuotedHeading(t, m, th, target)
	})

	t.Run("relocate", func(t *testing.T) {
		f := setup(t)
		if err := os.WriteFile(f.path, []byte(quotedHeadingDoc), 0o644); err != nil {
			t.Fatal(err)
		}
		seedOrphan(t, f, "lost comment")
		m := openFixtureModel(t, f)

		m = press(m, "m")
		if m.mode != modeRelocate {
			t.Fatal("must enter relocate mode")
		}
		target := quotedHeadingBlock(t, m)
		m.cursor = target
		m.rerender()

		cur, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = drain(t, cur.(*Model), cmd)

		threads, err := f.svc.Threads(f.ctx, m.sess.Plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		th, ok := threadByBody(threads, "lost comment")
		if !ok {
			t.Fatal("service must still have the thread")
		}
		assertAnchoredOnTheQuotedHeading(t, m, th, target)
	})
}

// ruleDoc is a plan with a section separator in it.
const ruleDoc = `# Rate Limiter Plan

## Context

Requests are currently unbounded.

---

## Design

We will use a token bucket.
`

// TestARuleRefusesTheWritesAimedAtIt covers this package's half of the
// behaviour: the cursor MAY land on a rule -- teaching it to skip blocks was
// ruled against -- so every write aimed at the block under the cursor has to
// answer for landing there.
//
// REFUSE RATHER THAN CLAMP is the ruling: a write is answered with a sentence,
// never re-aimed at some neighbouring block the reviewer did not choose. Both
// block-aimed writes are driven, because both would otherwise reach
// reanchor.CreateAnchor with the markup `---` as their span and fail there
// instead -- LATE, and in the compose case after the reviewer has typed a
// comment and after a plan-less file has been lazily created to hold it.
//
// THE UNWRITTEN PLAN IS ASSERTED BY DRIVING THE WHOLE GESTURE, not by checking
// after the `c`. Checked straight after the refusal the assertion has no
// subject -- the plan is created inside the post handler, so nothing could have
// written one yet and the check passes with its own premise deleted. NONE OF THE
// FOUR KEYS CAN WRITE A PLAN OR OPEN A PANEL IN READ MODE, which is the property
// this needs and is weaker than the one it used to have: h and i are unbound
// there, while tab and enter are read mode's own ActCycleThread and
// ActToggleExpand and merely move the rail and the card. Mutate ui.Anchorable to
// accept a rule and the line fails, because `c` then opens the panel and
// tab-then-enter posts.
//
// The help bar is in the same test because it is the same fact said in advance:
// read mode's bar may not name a key its handler rejects (helpLine).
func TestARuleRefusesTheWritesAimedAtIt(t *testing.T) {
	ruleBlock := func(t *testing.T, m *Model) int {
		t.Helper()
		for i, b := range m.blocks {
			if b.Kind == ui.KindRule {
				return i
			}
		}
		t.Fatal("fixture assumption broken: no rule block")
		return -1
	}
	onRule := func(t *testing.T, m *Model) *Model {
		t.Helper()
		m.cursor = ruleBlock(t, m)
		m.rerender()
		return m
	}
	openOnRuleDoc := func(t *testing.T) (fixture, *Model) {
		t.Helper()
		f := setup(t)
		if err := os.WriteFile(f.path, []byte(ruleDoc), 0o644); err != nil {
			t.Fatal(err)
		}
		return f, openModel(t, f)
	}

	t.Run("comment", func(t *testing.T) {
		_, m := openOnRuleDoc(t)
		m = onRule(t, m)
		m = press(m, "c")
		if m.status != noContentAtCursor {
			t.Fatalf("status = %q, want %q", m.status, noContentAtCursor)
		}
		if m.mode == modeCompose {
			t.Fatal("the compose panel opened on a rule, so the refusal comes at the post and throws the typed comment away")
		}
		// The rest of the gesture, driven blind past the refusal -- see this
		// test's own comment for why the check below needs it. Spelled out
		// rather than driven through pressComposePost, which refuses a model
		// that is not in compose mode: being refused is this test's premise.
		m = press(m, "h", "i", "tab")
		cur, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = drain(t, cur.(*Model), cmd)
		if m.sess.Exists {
			t.Fatal("a plan was created for a comment that cannot be anchored")
		}
		if m.mode != modeRead {
			t.Fatalf("mode = %v after the whole gesture, want modeRead -- nothing after the refusal may open a panel", m.mode)
		}
	})

	t.Run("relocate", func(t *testing.T) {
		f := setup(t)
		if err := os.WriteFile(f.path, []byte(ruleDoc), 0o644); err != nil {
			t.Fatal(err)
		}
		seedOrphan(t, f, "lost comment")
		m := openFixtureModel(t, f)
		m = press(m, "m")
		if m.mode != modeRelocate {
			t.Fatalf("mode = %v, want modeRelocate", m.mode)
		}
		// On an ordinary block first, so the swap below is a change and not
		// the only thing this mode ever says. Block 0 is the H1.
		m.cursor = 0
		m.rerender()
		if got := m.helpBar(); !strings.Contains(got, "enter place here") {
			t.Fatalf("relocate does not offer enter on a block that takes a placement: %q", got)
		}
		m = onRule(t, m)
		// BOTH SURFACES, because relocateHintText feeds the mode's help bar
		// and the panel's own hint strip, and enter is refused here.
		for name, got := range map[string]string{"help bar": m.helpBar(), "panel hint": m.relocateHint()} {
			switch {
			case strings.Contains(got, "enter place here"):
				t.Fatalf("the relocate %s offers enter over a block this mode refuses: %q", name, got)
			case !strings.Contains(got, noContentAtCursor):
				t.Fatalf("the relocate %s does not say why enter is gone: %q", name, got)
			}
		}
		cur, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = drain(t, cur.(*Model), cmd)

		if m.status != noContentAtCursor {
			t.Fatalf("status = %q, want %q", m.status, noContentAtCursor)
		}
		// Still in triage with the orphan still orphaned: a refusal here is
		// not an exit, and the next cursor move can place it.
		if m.mode != modeRelocate {
			t.Fatalf("mode = %v, want modeRelocate -- a refused placement left triage", m.mode)
		}
		if n := len(m.unresolvedOrphans()); n != 1 {
			t.Fatalf("unresolved orphans = %d, want the one this refused to place", n)
		}
	})

	t.Run("the help bar", func(t *testing.T) {
		_, m := openOnRuleDoc(t)
		prose := m.helpBar()
		if !strings.Contains(prose, "c comment") {
			t.Fatalf("the bar on an ordinary block does not offer the comment key: %q", prose)
		}
		m = onRule(t, m)
		switch got := m.helpBar(); {
		case strings.Contains(got, "c comment"):
			t.Fatalf("the bar offers a key the cursor's block refuses: %q", got)
		case !strings.Contains(got, noContentAtCursor):
			t.Fatalf("the bar does not say why: %q, want a clause reading %q", got, noContentAtCursor)
		}
	})

	t.Run("a cursor past the end of the document", func(t *testing.T) {
		_, m := openOnRuleDoc(t)
		// The review cursor is not clamped to m.blocks (seekMatch says why),
		// and the compose path subscripts it -- at the post button and again
		// in composeTarget -- so the guard that refuses a rule is also what
		// stands between that state and an index panic.
		m.cursor = len(m.blocks) + 3
		m = press(m, "c")
		if m.mode != modeRead || m.status != noContentAtCursor {
			t.Fatalf("mode = %v, status = %q, want read mode and %q", m.mode, m.status, noContentAtCursor)
		}
	})
}

// reviewControlBytes is the three control bytes the review frame's own tests
// name, with each glyph WRITTEN OUT rather than computed -- see listControlBytes
// (list_test.go) for why a test that re-ran ui/control.go's arithmetic would
// assert nothing.
var reviewControlBytes = []struct{ name, ctl, glyph string }{
	{"BS", "\b", "␈"},
	{"CR", "\r", "␍"},
	{"DEL", "\x7f", "␡"},
}

// openReviewOnDoc opens a painted review model at 100x30 over doc, with no
// plan created: that is the state ui.InferTitle's display call site
// (statusIdentity) is reached from.
func openReviewOnDoc(t *testing.T, doc string) (fixture, *Model) {
	t.Helper()
	f := setup(t)
	if err := os.WriteFile(f.path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := session.Open(f.ctx, f.svc, f.path)
	if err != nil {
		t.Fatal(err)
	}
	m := New(sess, keymap.Default(), th, "", nil)
	if err := m.RefreshFromSession(f.ctx); err != nil {
		t.Fatal(err)
	}
	m.width, m.height = 100, 30
	m.rerender()
	return f, m
}

// assertReviewFrame is the review frame's own version of the plan list's:
// exactly m.height rows, none of them wider than m.width, no raw control byte
// anywhere, and the glyph present.
//
// "NOT WIDER THAN" AND NOT "EXACTLY", deliberately: renderDocPainted pads a
// document row to the rail and leaves a short LAST row of a wrapped block
// alone, so an exact-width assertion here would be measuring the document's own
// padding rather than the filter. The row COUNT is exact, and it is the
// assertion that catches the overflow.
func assertReviewFrame(t *testing.T, m, twin *Model, ctl, glyph string) {
	t.Helper()
	// THE BENIGN TWIN IS THE FIXTURE GUARD, and it is here because this frame
	// has a KNOWN unbounded row that is nothing to do with control bytes:
	// renderBlockPainted's heading arm neither wraps nor truncates, and the
	// compose header is not clipped either. A payload long enough to put the
	// STATUS BAR at its budget can trip one of those instead. The twin is the
	// identical document with the control byte swapped for an ordinary
	// character: if IT does not fit the terminal, the fixture is wrong rather
	// than the code.
	if twinRows := len(strings.Split(twin.View().Content, "\n")); twinRows != twin.height {
		t.Fatalf("the BENIGN twin is %d rows against a %d-row terminal -- this fixture is tripping an overflow that has nothing to do with a control byte, so the case below would prove nothing:\n%s",
			twinRows, twin.height, ansi.Strip(twin.View().Content))
	}
	content := m.View().Content
	// THE BYTE FIRST AND THE FRAME SECOND, so a filter taken back out fails by
	// naming the byte that got through rather than by reporting a row count.
	plain := ansi.Strip(content)
	if strings.Contains(plain, ctl) {
		t.Fatalf("the byte %q reached the screen unfiltered: %q", ctl, plain)
	}
	if !strings.Contains(plain, glyph) {
		t.Fatalf("the byte %q is gone from the screen and %q is not there either: %q -- this view visualises and does not strip", ctl, glyph, plain)
	}
	lines := strings.Split(content, "\n")
	if len(lines) != m.height {
		t.Fatalf("the frame is %d rows against a %d-row terminal where the benign twin is %d -- one control byte here is zero cells to ansi.Truncate and one to lipgloss's Width().Render, so a row budgeted to exactly its width came back as two:\n%s",
			len(lines), m.height, twin.height, ansi.Strip(content))
	}
}

// reviewTwinByte is the ordinary character a benign twin carries where the
// hostile document carries a control byte: one cell to both width authorities,
// which is exactly what the substituted Control Picture is.
const reviewTwinByte = "~"

// TestTheReviewFrameVisualisesItsTwoRawCarriers covers the two carriers no
// leaf filter can reach.
//
// ui.InferTitle RETURNS Block.Text RAW (ui/title.go). Its other call sites
// CREATE A PLAN with what it answers; app/model.go's statusIdentity is the only
// one that DRAWS it, and it is the one filtered -- the transform belongs where
// the bytes are drawn, so the stored title stays byte-identical to the
// document's own H1 and every frame that paints it shows the glyph.
//
// Block.HeadingPath IS THE THIRD RAW CARRIER OF A HEADING'S BYTES, beside
// Block.Text and InferTitle: the compose panel's header is "comment on: " +
// strings.Join(HeadingPath, " › "), so filtering renderBlockPainted's heading
// arm does not reach it -- the reviewer sees the forged heading in the panel
// that asks them to comment on it.
func TestTheReviewFrameVisualisesItsTwoRawCarriers(t *testing.T) {
	// LONG ENOUGH THAT THE STATUS BAR IS ALREADY AT ITS BUDGET, the only
	// condition under which the two width authorities can disagree into an extra
	// ROW rather than into one extra cell, and SHORT enough that the unbounded
	// heading row and the unclipped compose header still fit.
	pad := strings.Repeat("x", 40)
	title := func(ctl string) string { return "Requires approval" + ctl + "No approval needed " + pad }
	doc := func(ctl string) string { return "# " + title(ctl) + "\n\nbody text\n" }
	compose := func(t *testing.T, ctl string) *Model {
		t.Helper()
		_, m := openReviewOnDoc(t, doc(ctl))
		m.cursor = 1
		m.rerender()
		m = press(m, "c")
		if m.mode != modeCompose {
			t.Fatalf("mode = %v, want modeCompose -- without the panel open this asserts nothing about HeadingPath", m.mode)
		}
		return m
	}
	for _, b := range reviewControlBytes {
		t.Run(b.name+"/the inferred title -- ui.InferTitle", func(t *testing.T) {
			_, m := openReviewOnDoc(t, doc(b.ctl))
			if m.planExists {
				t.Fatal("the fixture created a plan, so statusIdentity draws m.title and not ui.InferTitle -- this case would prove nothing about the inferred half")
			}
			if got := ui.InferTitle(m.blocks, m.sess.Path); !strings.Contains(got, b.ctl) {
				t.Fatalf("ui.InferTitle answered %q, which does not carry %q -- it must still answer Block.Text RAW, because the transform belongs at the draw and this test is about that placement", got, b.ctl)
			}
			_, twin := openReviewOnDoc(t, doc(reviewTwinByte))
			assertReviewFrame(t, m, twin, b.ctl, b.glyph)
		})
		t.Run(b.name+"/a plan's own title", func(t *testing.T) {
			build := func(ctl string) *Model {
				f, m := openReviewOnDoc(t, "# Benign heading\n\nbody text\n")
				if err := m.sess.Create(f.ctx, title(ctl)); err != nil {
					t.Fatal(err)
				}
				if err := m.RefreshFromSession(f.ctx); err != nil {
					t.Fatal(err)
				}
				m.rerender()
				return m
			}
			assertReviewFrame(t, build(b.ctl), build(reviewTwinByte), b.ctl, b.glyph)
		})
		t.Run(b.name+"/the compose header -- Block.HeadingPath", func(t *testing.T) {
			m := compose(t, b.ctl)
			if !strings.Contains(m.composeTarget(), b.ctl) {
				t.Fatalf("composeTarget() = %q and does not carry %q -- HeadingPath is the subject here and this fixture is not driving it", m.composeTarget(), b.ctl)
			}
			assertReviewFrame(t, m, compose(t, reviewTwinByte), b.ctl, b.glyph)
		})
	}
}

// TestStatusIdentityThreadCountIsSingularAtOne pins statusIdentity's English:
// "1 thread", never "1 threads". 0 and 2 are the plural's own boundaries,
// driven alongside it so a fix that only special-cased 1 cannot pass by
// accident. Built directly (hyphenwrap_test.go's &Model{} pattern) because the
// count comes off m.threadCount(), which only reads m.views/m.unanchored --
// no session or store is needed to drive it.
func TestStatusIdentityThreadCountIsSingularAtOne(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{0, "Plan · block 1/1 · 0 threads"},
		{1, "Plan · block 1/1 · 1 thread"},
		{2, "Plan · block 1/1 · 2 threads"},
	} {
		t.Run(fmt.Sprintf("%d", tc.n), func(t *testing.T) {
			m := &Model{
				title:      "Plan",
				planExists: true,
				blocks:     []ui.Block{{}},
				unanchored: make([]ui.ThreadView, tc.n),
			}
			if got := m.statusIdentity(); got != tc.want {
				t.Fatalf("statusIdentity() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTheReviewStatusBarVisualisesAnErrorMessage covers the OTHER supplied
// string on the review status bar.
//
// THE FRAME INVARIANT IS A PROPERTY OF THE ROW AND NOT OF THE FIELD. This bar
// is composed from statusIdentity (the plan's title, or ui.InferTitle over the
// document) AND m.status, budgeted once to exactly barWidth cells, and drawn
// through one Width().Render. Filtering the title half and leaving the message
// half raw would leave the row overflowable from the other end -- a half-closed
// invariant, which reads as closed. m.status carries error strings verbatim
// (app/actions.go's err.Error() sites), so those bytes are as supplied as the
// title's.
func TestTheReviewStatusBarVisualisesAnErrorMessage(t *testing.T) {
	for _, b := range reviewControlBytes {
		t.Run(b.name, func(t *testing.T) {
			build := func(ctl string) *Model {
				_, m := openReviewOnDoc(t, "# Benign heading\n\nbody text\n")
				// Long enough to put the bar at its budget, the only
				// condition the two width authorities can disagree into a
				// row under. The approved tint is on as well, because it
				// puts an st.OK Render immediately in front of this
				// fragment -- the reason it is drawn with
				// ui.RenderControls rather than ui.VisibleControls.
				m.approved = true
				m.status = "error: could not write the plan (ap" + ctl + "proved) " + strings.Repeat("x", 60)
				m.rerender()
				return m
			}
			assertReviewFrame(t, build(b.ctl), build(reviewTwinByte), b.ctl, b.glyph)
		})
	}
}

// TestTheRenamePrefillVisualisesRatherThanStrips covers the one surface in
// this product that was already transforming a control byte, silently, and in
// the direction opposite the one chosen.
//
// bubbles' textarea STRIPS C0 and DEL out of a value handed to SetValue and
// turns a bare CR into a newline, so there was never a "no mutation" property
// here to protect: the only question is whether the mutation strips or
// visualises, and this package visualises. The prefill carries the same
// Control Pictures the plan's own row carries, so what the reader reads is
// what ctrl+d writes.
func TestTheRenamePrefillVisualisesRatherThanStrips(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range reviewControlBytes {
		t.Run(b.name, func(t *testing.T) {
			title := "Requires approval" + b.ctl + "No approval needed"
			m := NewList(nil, keymap.Default(), th)
			m.width, m.height = 100, 30
			m.applyRefresh([]planItem{{plan: domain.Plan{ID: "l_a", Title: title, SourceHint: "~/p/a.md"}}})
			m.enterRename()
			got := m.ta.Value()
			if strings.Contains(got, b.ctl) {
				t.Fatalf("the prefill holds the raw byte %q: %q -- a textarea is not a renderer and cannot visualise it", b.ctl, got)
			}
			if !strings.Contains(got, b.glyph) {
				t.Fatalf("the prefill is %q and carries no %q -- the widget strips the byte on its own, so leaving the prefill raw is a SILENT STRIP, which is the rejected option", got, b.glyph)
			}
			if !strings.Contains(got, "Requires approval") || !strings.Contains(got, "No approval needed") {
				t.Fatalf("the prefill lost the words either side of the byte: %q", got)
			}
		})
	}
}

// TestComposePaintedHeaderMarksSection pins that the compose header is derived
// from composeTarget, so a heading target shows "(section)".
func TestComposePaintedHeaderMarksSection(t *testing.T) {
	f := setup(t)
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.Open(f.ctx, f.svc, f.path)
	if err != nil {
		t.Fatal(err)
	}
	m := New(s, keymap.Default(), th, "", nil)
	if err := m.RefreshFromSession(f.ctx); err != nil {
		t.Fatal(err)
	}
	m.width, m.height = 100, 30
	m.rerender()

	// Cursor starts on block 0, the H1 heading.
	m = press(m, "c")
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose", m.mode)
	}
	panel, _ := m.panelViewPainted()
	out := ansi.Strip(panel)
	if !strings.Contains(out, "(section)") {
		t.Fatalf("painted compose header must mark a section comment: %q", strings.SplitN(out, "\n", 2)[0])
	}
}

// TestSectionThreadSurvivesHeadingRename pins that a section comment's
// placement follows a heading rename (leaf similarity is high enough to
// clear SectionMinSim) while the thread record itself — Anchor and
// AnchorHash — is left untouched by reload: placements are a recomputed
// materialized view, never a write, so only an explicit Rehome would change
// the stored anchor.
func TestSectionThreadSurvivesHeadingRename(t *testing.T) {
	f := setup(t)
	s, err := session.Open(f.ctx, f.svc, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	designPath := []string{"Rate Limiter Plan", "Design"}
	a, err := session.SectionAnchor(string(s.Content), designPath)
	if err != nil {
		t.Fatal(err)
	}
	seeded, err := s.Comment(f.ctx, a, "section-level note")
	if err != nil {
		t.Fatal(err)
	}

	m := openModel(t, f)

	revised := strings.Replace(testDoc, "## Design", "## Design details", 1)
	if err := os.WriteFile(f.path, []byte(revised), 0o644); err != nil {
		t.Fatal(err)
	}
	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = drain(t, cur.(*Model), cmd)

	// Headings render bold+underline with per-rune SGR sequences in lipgloss
	// v2, so strip ANSI before substring checks.
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Design details") {
		t.Fatal("reload must re-read the file")
	}
	if !strings.Contains(view, "moved") {
		t.Fatal("renamed section thread must show its moved badge")
	}

	threads, err := f.svc.Threads(f.ctx, m.sess.Plan.ID)
	if err != nil || len(threads) != 1 {
		t.Fatalf("threads = %v, %v", threads, err)
	}
	got := threads[0]
	if got.AnchorHash != seeded.AnchorHash {
		t.Fatalf("anchor hash = %v, want unchanged %v (reload must not auto-rehome)", got.AnchorHash, seeded.AnchorHash)
	}
	if !reflect.DeepEqual(got.Anchor, a) {
		t.Fatalf("anchor = %+v, want unchanged %+v (reload must not auto-rehome)", got.Anchor, a)
	}
}

func TestReplyAndResolve(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "why fifty?")
	m := openModel(t, f)
	m = press(m, "n") // jump to the thread's block

	m = press(m, "r")
	if m.mode != modeCompose {
		t.Fatal("reply must open compose")
	}
	m = press(m, "o", "k")
	m, cmd := pressComposePost(t, m)
	m = drain(t, m, cmd)
	threads, _ := f.svc.Threads(f.ctx, m.sess.Plan.ID)
	if len(threads[0].Comments) != 2 {
		t.Fatalf("comments = %d, want 2", len(threads[0].Comments))
	}

	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	m = drain(t, cur.(*Model), cmd)
	threads, _ = f.svc.Threads(f.ctx, m.sess.Plan.ID)
	if !threads[0].Resolved {
		t.Fatal("thread must be resolved")
	}
}

// TestPostingACommentSelectsTheThreadItCreated: the post opens the new card AND
// aims the focus at the thread it just made, so the very next r/R/d writes to
// it with no intervening keystroke. See selectCreated for the ruling itself and
// for why read mode's enter keeps its opposite rule.
//
// THE BLOCK CARRIES TWO THREADS ALREADY, and that is the whole test. On a
// one-thread block "selected the thread it created" and "selected the block's
// first thread" are the same index. With two seeds in front of it, only an
// implementation that finds the new thread BY ID puts the focus where this
// asserts.
func TestPostingACommentSelectsTheThreadItCreated(t *testing.T) {
	f := setup(t)
	seedThreadOn(t, f, "Rate Limiter", "token bucket", "the first seeded thread")
	seedThreadOn(t, f, "Rate Limiter", "token bucket", "the second seeded thread")
	m := openModel(t, f)
	m = press(m, "n") // onto the token-bucket block, focus on its thread 0

	const posted = "the comment this test just wrote"
	m = press(m, "c")
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose", m.mode)
	}
	m = press(m, strings.Split(posted, "")...)
	m, cmd := pressComposePost(t, m)
	m = drain(t, m, cmd)

	vs := m.views[m.cursor]
	if len(vs) != 3 {
		t.Fatalf("block %d carries %d threads, want 3 (two seeded, one posted)", m.cursor, len(vs))
	}
	got, ok := m.selectedThread()
	if !ok {
		t.Fatalf("no thread selected after the post; m.selected[%d] = %d", m.cursor, m.selected[m.cursor])
	}
	if body := got.Thread.Comments[0].Body; body != posted {
		t.Fatalf("the post selected the thread whose first comment is %q, want the thread it created, %q", body, posted)
	}

	// The gesture the ruling exists for: R with no keystroke in between
	// resolves the thread just written, rather than refusing with "no thread
	// at cursor".
	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	if cmd == nil {
		t.Fatalf("R immediately after the post dispatched no write; status = %q", cur.(*Model).status)
	}
	m = drain(t, cur.(*Model), cmd)
	threads, err := f.svc.Threads(f.ctx, m.sess.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range threads {
		if th.Resolved != (th.Comments[0].Body == posted) {
			t.Fatalf("thread %q resolved = %v; R must resolve the posted thread and nothing else", th.Comments[0].Body, th.Resolved)
		}
	}
}

// everyBlockCommentedFixture builds a document long enough that a page of
// scrolling moves, EVERY block of which carries a thread of its own.
//
// The universal commenting is what makes the test below about the selection
// rather than about the document: g, G and page-scroll each land the cursor
// somewhere the fixture does not get to choose, and on a block carrying no
// threads at all the three writes refuse for a different reason entirely --
// which would pass while proving nothing.
func everyBlockCommentedFixture(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	svc := newFixtureStore(t, filepath.Join(dir, "state.json"))
	var doc strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&doc, "Paragraph %d of a document whose every block carries a thread of its own.\n\n", i)
	}
	path := filepath.Join(dir, "commented.md")
	if err := os.WriteFile(path, []byte(doc.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	f := fixture{svc: svc, cas: localcas.New(filepath.Join(dir, "objects")), path: path, ctx: attrCtx("alice")}
	for i := 0; i < 20; i++ {
		seedThreadOn(t, f, "Every Block Commented", fmt.Sprintf("Paragraph %d of a document", i), fmt.Sprintf("a thread on paragraph %d", i))
	}
	return f
}

// TestWritesRefuseABlockNoThreadGestureReached pins this property.
//
// g, G and page-scroll move m.cursor and write NOTHING to m.selected -- only
// THREAD gestures write it -- so the block they land on has no entry at all,
// and Go's zero value for a missing int is 0: a real thread. Read bare, that
// put the rail on the block's own LINE while r replied to that block's first
// thread, R resolved it and d opened the confirm panel aimed at DESTROYING it
// -- a thread the reader had made no gesture to choose.
//
// THE SUBJECT IS THE DISAGREEMENT, not the sentence: the keystroke must act on
// what the screen says it acts on, so where focusedThread answers the block's
// line, the three writes must answer "no thread at cursor" too.
func TestWritesRefuseABlockNoThreadGestureReached(t *testing.T) {
	f := everyBlockCommentedFixture(t)

	// Three cursor movers, one property: each writes nothing to m.selected.
	// pageScroll's landing is chosen by firstBlockAtOrBelow rather than by this
	// table, which is the other half of why the fixture comments every block.
	navs := []struct {
		name string
		keys []string
	}{
		{"G", []string{"G"}},
		{"g", []string{"G", "g"}},
		{"page", []string{"pgdown"}},
	}
	writes := []struct {
		name string
		key  rune
	}{
		{"reply", 'r'},
		{"resolve", 'R'},
		{"delete", 'd'},
	}
	for _, nav := range navs {
		for _, w := range writes {
			t.Run(nav.name+"/"+w.name, func(t *testing.T) {
				m := openModel(t, f)
				m = press(m, nav.keys...)
				if n := len(m.views[m.cursor]); n == 0 {
					t.Fatalf("fixture assumption broken: %s landed on block %d, which carries no threads -- a refusal there proves nothing", nav.name, m.cursor)
				}
				if i, ok := m.selected[m.cursor]; ok {
					t.Fatalf("fixture assumption broken: %s left m.selected[%d] = %d, so this is not the absent-entry case this test is about", nav.name, m.cursor, i)
				}
				if got := m.focusedThread(); got != ui.NoThread {
					t.Fatalf("the rail is on thread %d after %s, want the block's own line", got, nav.name)
				}
				cur, cmd := m.Update(tea.KeyPressMsg{Code: w.key, Text: string(w.key)})
				got := cur.(*Model)
				if cmd != nil {
					t.Fatalf("%q dispatched a write on a block no thread gesture ever reached", string(w.key))
				}
				if got.mode != modeRead {
					t.Fatalf("%q left mode = %v, want modeRead: it must open no panel aimed at a thread nobody selected", string(w.key), got.mode)
				}
				if got.deleteTID != "" {
					t.Fatalf("%q aimed the delete at thread %q; nothing may be aimed at a thread nobody selected", string(w.key), got.deleteTID)
				}
				if got.status != "no thread at cursor" {
					t.Fatalf("%q left status %q, want %q", string(w.key), got.status, "no thread at cursor")
				}
			})
		}
	}
}

// TestNextThreadAimsTheWritesAtACollapsedCard pins the ONE disagreement
// between the rail and the writes that is deliberate, and it is the evidence
// that forbids deriving selectedThread from focusedThread outright.
//
// jumpThread walks to the block and STORES the index it walked to, and it
// deliberately does not open the card, so focusedThread answers NoThread while
// the reader has unambiguously chosen a thread. Making the writes inherit that
// refusal would answer "no thread at cursor" one keystroke after the reader
// asked for the next thread.
//
// r stands for all three here: they ask selectedThread and nothing else.
func TestNextThreadAimsTheWritesAtACollapsedCard(t *testing.T) {
	f := setup(t)
	const body = "the thread n walks to"
	seedThreadOn(t, f, "Rate Limiter", "token bucket", body)
	m := openModel(t, f)
	m = press(m, "n")

	if m.expanded[m.cursor] {
		t.Fatalf("fixture assumption broken: n expanded block %d, so this is not the collapsed case", m.cursor)
	}
	if got := m.focusedThread(); got != ui.NoThread {
		t.Fatalf("the rail is on thread %d; a collapsed card has no thread row to carry one", got)
	}
	v, ok := m.selectedThread()
	if !ok {
		t.Fatal("n walked to a thread the writes cannot see: r/R/d would refuse the thread the reader just asked for")
	}
	if got := v.Thread.Comments[0].Body; got != body {
		t.Fatalf("the writes are aimed at %q, want the thread n walked to, %q", got, body)
	}
	cur, _ := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	after := cur.(*Model)
	if after.mode != modeCompose {
		t.Fatalf("r after n opened no reply: mode = %v, status = %q", after.mode, after.status)
	}
	if after.replyTo != v.Thread.ID.String() {
		t.Fatalf("r is replying to %q, want the thread n walked to, %s", after.replyTo, v.Thread.ID)
	}
}

// TestCycleThreadStartsAtTheBlocksFirstThread pins the third reader of
// m.selected, which took the map bare for the same reason the other two did.
//
// tab advances the selection by one within the cursor's block. On a block the
// reader has made no thread gesture on there is no entry, the bare read
// answered 0, and (0+1)%n selected thread TWO -- the first press skipped the
// thread the reader was looking at. Through selectedIndex the absent entry is
// ui.NoThread, and (-1+1)%n is thread one. The wrap is asserted with it because
// a fix that special-cased the first press would pass the assertion above and
// break the cycle.
func TestCycleThreadStartsAtTheBlocksFirstThread(t *testing.T) {
	f := setup(t)
	const first, second = "the first thread on the block", "the second thread on the block"
	seedThreadOn(t, f, "Rate Limiter", "token bucket", first)
	seedThreadOn(t, f, "Rate Limiter", "token bucket", second)
	m := openModel(t, f)
	m = press(m, "G")
	if i, ok := m.selected[m.cursor]; ok {
		t.Fatalf("fixture assumption broken: G left m.selected[%d] = %d, so this is not the absent-entry case", m.cursor, i)
	}

	for i, want := range []struct {
		body   string
		status string
	}{
		{first, "thread 1/2 selected"},
		{second, "thread 2/2 selected"},
		{first, "thread 1/2 selected"},
	} {
		m = press(m, "tab")
		v, ok := m.selectedThread()
		if !ok {
			t.Fatalf("press %d: tab selected no thread", i+1)
		}
		if body := v.Thread.Comments[0].Body; body != want.body {
			t.Fatalf("press %d selected %q, want %q", i+1, body, want.body)
		}
		if m.status != want.status {
			t.Fatalf("press %d left status %q, want %q", i+1, m.status, want.status)
		}
	}
}

func TestApproveConfirmFlow(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "seed") // ensures the plan exists
	m := openModel(t, f)

	m = press(m, "a")
	if m.mode != modeConfirmApprove {
		t.Fatal("approve must ask for confirmation")
	}
	m = press(m, "n")
	if m.mode != modeRead || m.approved {
		t.Fatal("n must cancel without approving")
	}
	m = press(m, "a")
	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = drain(t, cur.(*Model), cmd)
	if !m.approved {
		t.Fatal("approval state must reflect after confirm")
	}
	if !strings.Contains(m.View().Content, "✓ approved") {
		t.Fatal("status line must show approval")
	}
	if !strings.Contains(m.status, "review saved") {
		t.Fatalf("status %q must confirm save", m.status)
	}
}

// TestApproveNamesASourcelessPlanByItsTitle pins planLabel's fallback at both
// of actions.go's call sites -- the approve confirm text and the post-write
// status -- for a plan with no path at all. filepath.Base("") answers ".", and
// a sourceless plan (session.ErrNoSourcePath, built here as root_test.go's
// TestRootOpensASourcelessPlan builds it: OpenSupplied with "" then Create) has
// no filename to fall back to, only the plan's own title.
func TestApproveNamesASourcelessPlanByItsTitle(t *testing.T) {
	f := newListFixture(t)
	content := []byte("# Sourceless Plan\n\nBytes handed to draftplane directly, no file involved.\n")
	s, err := session.OpenSupplied(f.ctx, f.svc, content, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(f.ctx, "Sourceless Plan"); err != nil {
		t.Fatal(err)
	}
	if s.Path != "" {
		t.Fatalf("test setup: Path = %q, want empty -- this plan must have no source at all", s.Path)
	}

	m := New(s, keymap.Default(), nil, "", nil)
	if err := m.RefreshFromSession(f.ctx); err != nil {
		t.Fatal(err)
	}
	m.width, m.height = 100, 30

	m = press(m, "a")
	if m.mode != modeConfirmApprove {
		t.Fatalf("mode = %v, want modeConfirmApprove", m.mode)
	}
	if want := "approve the current state of Sourceless Plan? (y/n)"; m.confirm != want {
		t.Fatalf("confirm = %q, want %q", m.confirm, want)
	}

	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = drain(t, cur.(*Model), cmd)
	if !strings.Contains(m.status, "review saved for Sourceless Plan") {
		t.Fatalf("status = %q, want it to name the plan by its title, not filepath.Base(\"\")", m.status)
	}
}

func TestReloadPicksUpFileChanges(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "why fifty?")
	m := openModel(t, f)

	revised := strings.Replace(testDoc,
		"We will use a token bucket with a burst capacity of fifty requests.",
		"A token bucket limiter with burst capacity fifty is the chosen approach.", 1)
	if err := os.WriteFile(f.path, []byte(revised), 0o644); err != nil {
		t.Fatal(err)
	}
	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = drain(t, cur.(*Model), cmd)
	view := m.View().Content
	if !strings.Contains(view, "chosen approach") {
		t.Fatal("reload must re-read the file")
	}
	if !strings.Contains(view, "fuzzy") {
		t.Fatal("re-anchored thread must show its fuzzy badge")
	}
}

// readFailingSvc makes ResolvePlan -- the first service call session.Open makes
// through resolveIdentity -- answer a fixed error, so a reload can be driven
// into failure through the REAL runReload producer rather than by constructing
// its message by hand.
type readFailingSvc struct {
	client.PlanService
	err error
}

func (s readFailingSvc) ResolvePlan(context.Context, string) (domain.Plan, error) {
	return domain.Plan{}, s.err
}

// TestReloadNamesWhatHappenedToTheFileItCouldNotRead is ctrl+r's share of a
// property shared by three doors: one missing file, one answer. The list's
// door (root_test.go's TestRootFileVanishedFallsBackToSnapshot) and the
// CLI's (cmd/draftplane's TestOpenReviewTargetResolvesAGoneFileAgainstTheIndex)
// reach the identical session.SourceFileGone for the identical file; this is
// the third, and it is the door where the user is already reading the
// document when the file goes.
//
// It is driven through runReload's own cmd rather than through a constructed
// message: the classification happens INSIDE that cmd, so a hand-built
// msgActionDone could not catch its absence.
//
// THE SECOND ROW IS THE REGRESSION: session.Open reads the file AND resolves
// identity, so a store that cannot answer fails the same call with the
// document sitting readable on disk, and classifying that as a file fault makes
// errors.Is(err, session.ErrSourceFileGone) answer true for a failure that is
// not the file's.
//
// THE PANEL IS ASSERTED BY BYTE EQUALITY against the panel the constructor door
// opens over the identical file, because the property that matters is that
// `draftplane review <a gone file>` and ctrl+r reach the SAME panel -- a
// headline this test spelled out for itself would pass just as happily over two
// doors drifting apart in wording. The store row is the other direction: no
// fault, no panel, and the plain error line.
//
// THE STATUS LINE IS EMPTY WHERE THE PANEL IS UP: a panel wraps where a bar
// clips, and the classified sentence does not fit an 80-column bar.
func TestReloadNamesWhatHappenedToTheFileItCouldNotRead(t *testing.T) {
	storeErr := errors.New("the plan store could not be read")
	tests := []struct {
		name       string
		planless   bool
		storeFails bool
		wantState  session.SourceFileState
		wantPanel  bool
	}{
		{name: "a plan's file is gone", wantState: session.SourceFileGone, wantPanel: true},
		{name: "the store cannot answer and the file is untouched", storeFails: true},
		// No plan was ever made for this file, so there is no cache to show and
		// nothing for f, o or d to write: the fault is named on the error line.
		{name: "a file with no plan behind it is gone", planless: true, wantState: session.SourceFileGone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			if !tc.planless {
				seedThread(t, f, "why fifty?")
			}
			m := openModel(t, f)
			before := m.sess
			id := m.sess.Plan.ID
			if tc.planless != (id == "") {
				t.Fatalf("test setup: plan id = %q, want a plan-less session only on the plan-less row", id)
			}
			svc := client.PlanService(f.svc)
			if tc.storeFails {
				svc = readFailingSvc{PlanService: f.svc, err: storeErr}
			} else if err := os.Remove(f.path); err != nil {
				t.Fatal(err)
			}
			if tc.wantPanel {
				// Non-empty on purpose: the assertion below is that the panel
				// arm clears it. Left at New's own "" default, that assertion
				// would pass whether or not the clear runs.
				m.status = "stale status the fault must clear"
			}

			cmd := m.runReload(svc, m.sess.Path, m.sess.FromSnapshot(), id)
			msg, ok := cmd().(msgActionDone)
			if !ok {
				t.Fatalf("runReload's cmd produced %T, want msgActionDone", cmd())
			}
			if msg.err == nil {
				t.Fatal("test setup: this reload was supposed to fail")
			}

			var fault *session.SourceFileError
			if tc.wantState == 0 {
				if errors.As(msg.err, &fault) {
					t.Errorf("err = %v classified as %s -- a backend that could not answer is not a broken file", msg.err, fault.State)
				}
				if errors.Is(msg.err, session.ErrSourceFileGone) {
					t.Errorf("err = %v answers errors.Is(err, ErrSourceFileGone) -- the document is on disk, untouched", msg.err)
				}
				if !errors.Is(msg.err, storeErr) {
					t.Errorf("err = %v, want the backend's own failure to reach the door unchanged", msg.err)
				}
			} else {
				if !errors.As(msg.err, &fault) {
					t.Fatalf("err = %v, want errors.As(err, **session.SourceFileError) -- a bare file error names nothing a panel can act on", msg.err)
				}
				if fault.State != tc.wantState {
					t.Errorf("State = %s, want %s", fault.State, tc.wantState)
				}
				if fault.PlanID != id {
					t.Errorf("PlanID = %s, want %s", fault.PlanID, id)
				}
				if fault.Path != f.path {
					t.Errorf("Path = %q, want %q", fault.Path, f.path)
				}
				if !errors.Is(msg.err, os.ErrNotExist) {
					t.Errorf("err = %v, want it to still answer errors.Is(err, os.ErrNotExist) -- the cause must survive the classification", msg.err)
				}
			}

			cur, _ := m.Update(msg)
			m = cur.(*Model)
			if m.sess != before {
				t.Error("a failed reload must keep the session the user is reading on screen")
			}
			if !tc.wantPanel {
				if m.mode != modeRead {
					t.Errorf("mode = %v, want modeRead -- no panel for this row", m.mode)
				}
				if want := "error: " + msg.err.Error(); m.status != want {
					t.Errorf("status = %q, want the error line this door has always shown, %q", m.status, want)
				}
				return
			}
			if m.mode != modeSourceFault {
				t.Fatalf("mode = %v, want modeSourceFault -- ctrl+r is a third door and must reach the same panel the other two do", m.mode)
			}
			if m.status != "" {
				t.Errorf("status = %q, want nothing: the panel is the account, and a bar carrying it clipped its own remedy at 80 columns", m.status)
			}
			// THE OTHER DOOR, over the same file: session.OpenPlan is what
			// the plan list's enter and `draftplane review <path>` both go
			// through, and New is where they open the panel. Equality here is
			// the whole property being pinned.
			sess, err := session.OpenPlan(f.ctx, f.svc, before.Plan)
			if err != nil {
				t.Fatalf("OpenPlan = %v, want the snapshot fallback the other doors get", err)
			}
			byPlan := New(sess, keymap.Default(), nil, "", nil)
			if byPlan.mode != modeSourceFault {
				t.Fatalf("test setup: the constructor door left mode %v, so there is no panel to compare against", byPlan.mode)
			}
			if m.confirm != byPlan.confirm {
				t.Errorf("ctrl+r's panel and the constructor door's differ:\ngot:  %q\nwant: %q", m.confirm, byPlan.confirm)
			}
		})
	}
}

func TestEmptyDocumentDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	svc := newFixtureStore(t, filepath.Join(dir, "state.json"))
	path := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	f := fixture{svc: svc, cas: localcas.New(filepath.Join(dir, "objects")), path: path, ctx: attrCtx("alice")}
	m := openModel(t, f)

	// No blocks: navigation must no-op and writes must refuse cleanly,
	// never index into an empty m.blocks.
	m = press(m, "c", "j", "k")
	if m.CursorBlock() != 0 {
		t.Fatalf("cursor = %d on a blockless document, want 0", m.CursorBlock())
	}
	if !strings.Contains(m.status, "no content blocks") {
		t.Fatalf("status = %q, want a no-content-blocks hint", m.status)
	}
	// d joins the same guard: the three writes that aim at a block all report
	// this state in one sentence, and a fourth reporting "no thread at cursor"
	// for it would describe the cursor when the document is what is missing.
	m = press(m, "d")
	if m.mode != modeRead || !strings.Contains(m.status, "no content blocks") {
		t.Fatalf("d on a blockless document: mode=%v status=%q, want read mode and the no-content-blocks hint", m.mode, m.status)
	}

	// Approving an empty plan is legal — it has a hash — and goes straight to
	// the y/n confirm; y creates the plan (inferred title falls back to the
	// filename stem, since there are no blocks) then approves.
	m = press(m, "a")
	if m.mode != modeConfirmApprove {
		t.Fatalf("mode = %v, want modeConfirmApprove", m.mode)
	}
	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = drain(t, cur.(*Model), cmd)
	if m.mode != modeRead || !m.sess.Exists {
		t.Fatalf("mode = %v, exists = %v, want modeRead with the plan created", m.mode, m.sess.Exists)
	}
	if !m.approved {
		t.Fatal("approving an empty plan must succeed")
	}
	if m.sess.Plan.Title != "empty" {
		t.Fatalf("title = %q, want the filename-stem fallback", m.sess.Plan.Title)
	}
}

// TestFindKeyIsDeterministic guards findKey against Go's randomized map
// iteration -- called repeatedly against the SAME map it must answer
// identically every time -- and pins the shortest-wins half of its tie-break:
// ActMoveDown is bound to both j and down by default, so the one-character key
// must win.
//
// IT CALLS findKey DIRECTLY rather than through helpLine's rendered bar, which
// the move-segment removal below leaves with no window onto this property at
// all: helpLine no longer looks ActMoveDown/ActMoveUp up, even
// though the keymap itself still carries both bindings and findKey's own rule
// about them is still real.
func TestFindKeyIsDeterministic(t *testing.T) {
	km := keymap.Default()
	want := findKey(km, keymap.ActMoveDown)
	for i := 0; i < 50; i++ {
		if got := findKey(km, keymap.ActMoveDown); got != want {
			t.Fatalf("findKey nondeterministic across repaints: %q vs %q", got, want)
		}
	}
	if want != "j" {
		t.Fatalf("want the shortest key j to win over down, got %q", want)
	}
}

// TestFindKeyTieBreaksLexicographically is findKey's other rule, alongside
// TestFindKeyIsDeterministic's shortest-wins: among bindings of EQUAL length it
// answers the lexicographically smallest. b and z are a synthetic pair chosen
// only for the ordering.
func TestFindKeyTieBreaksLexicographically(t *testing.T) {
	km := keymap.Map{}
	for k, v := range keymap.Default() {
		km[k] = v
	}
	delete(km, "j")
	km["z"] = keymap.ActMoveDown
	km["b"] = keymap.ActMoveDown
	if got := findKey(km, keymap.ActMoveDown); got != "b" {
		t.Fatalf("want lexicographically-first tie winner b, got %q", got)
	}
}

// TestHelpLineHidesRelocateWhenNoOrphans pins the conditional verb: the
// help bar advertises "m relocate" only when there's an orphan to triage —
// the m key itself stays live either way, with its own "no orphaned
// threads" status when there's nothing to do.
func TestHelpLineHidesRelocateWhenNoOrphans(t *testing.T) {
	fx := setup(t)
	m := openModel(t, fx)
	if strings.Contains(helpLine(m.km, len(m.unresolvedOrphans()), m.searchState(), ""), "relocate") {
		t.Fatal("help line must omit relocate with no orphans")
	}

	seedOrphan(t, fx, "lost comment")
	if err := m.RefreshFromSession(fx.ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(helpLine(m.km, len(m.unresolvedOrphans()), m.searchState(), ""), "relocate") {
		t.Fatal("help line must include relocate once an orphan exists")
	}
}

// TestEveryModeDeclaresItsOwnHelpBar is the test that fails when the NEXT mode
// is added without a hint. Every mode once answered with read mode's line, so
// every panel advertised comment/reply/resolve/approve/quit while a y/n prompt
// held the keyboard.
//
// THE SET IS DRIVEN FROM THE ENUM, not from a list written here: modeCount
// moves when a constant is added above it and this loop moves with it, where a
// hand-typed table would go stale.
//
// "?" IS findKey's MISS MARKER, and it is checked because of the trap this
// mechanism is shaped around: y/n/esc/ctrl+d/enter/tab are literals bound to no
// action, so a hint built by looking actions up would render "? approve · ?/?
// cancel" for exactly the confirm panels the defect was first found on. A bar
// carrying one means a literal mode was routed through the keymap.
//
// ⚠️ "?" STOPPED BEING ONLY A MISS MARKER: it is ActKeys' own
// default binding, so modeRead's bar (its leading "? keys") and modeKeys' own
// (its "esc/? close") now carry a LEGITIMATE "?" apiece. Both are stripped
// before the miss-marker check runs, by the exact bytes findKey itself would
// render for ActKeys today -- so a rebind that moved "?" off ActKeys changes
// what gets stripped right along with it, and a genuinely broken segment
// FURTHER ALONG either line still trips the check that follows.
func TestEveryModeDeclaresItsOwnHelpBar(t *testing.T) {
	m := openModel(t, setup(t))
	browse := helpLine(m.km, len(m.unresolvedOrphans()), m.searchState(), "")
	keysKey := findKey(m.km, keymap.ActKeys)
	for md := mode(0); md < modeCount; md++ {
		m.mode = md
		got := m.helpBar()
		checkedForMiss := got
		switch md {
		case modeRead:
			checkedForMiss = strings.TrimPrefix(checkedForMiss, keysKey+" keys · ")
		case modeKeys:
			checkedForMiss = strings.ReplaceAll(checkedForMiss, "esc/"+keysKey, "")
		}
		switch {
		case got == "":
			t.Errorf("mode %d draws helpHint's silent default: nobody declared what its keys are", md)
		case strings.Contains(checkedForMiss, "?"):
			t.Errorf("mode %d's help bar %q carries findKey's miss marker -- a literal key looked up as an action", md, got)
		case md != modeRead && got == browse:
			t.Errorf("mode %d shows read mode's own line %q while it owns the keyboard", md, got)
		}
	}
	for md := range modeHints {
		if md < 0 || md >= modeCount {
			t.Errorf("modeHints declares a hint for %d, which is not a mode", md)
		}
	}
}

// TestAnUnregisteredModeGetsSilenceNotBrowse pins the half of the mechanism
// nothing else can reach: helpHint's zero value. The test above proves every
// mode that exists today declares a hint, which is exactly what makes this
// branch unreachable through any call site -- so it is pinned by calling
// helpBar directly with a value no mode ever holds.
//
// An exhaustive switch with a browse fallback passes the test above and still
// hands the next mode a wrong line on the day it is added.
func TestAnUnregisteredModeGetsSilenceNotBrowse(t *testing.T) {
	m := openModel(t, setup(t))
	m.mode = modeCount
	if got := m.helpBar(); got != "" {
		t.Fatalf("an unregistered mode's help bar = %q, want silence", got)
	}
}

// TestAConfirmPanelNamesItsLiteralKeysAndTakesOnlyThose drives the wrong-hint
// rule: the bar names y and n/esc, those keys resolve the panel, and the browse
// keys it does not name leave it standing.
//
// modeConfirmApprove stands for the y/n panels this model has, all of which
// share one composer (confirmHint), and the assertion is by identity rather
// than by spelling so a rewording cannot leave the others out of step.
func TestAConfirmPanelNamesItsLiteralKeysAndTakesOnlyThose(t *testing.T) {
	m := openModel(t, setup(t))
	m = press(m, "a")
	if m.mode != modeConfirmApprove {
		t.Fatalf("test setup: mode = %v, want the approve confirm", m.mode)
	}
	if got, want := m.helpBar(), confirmHint("approve"); got != want {
		t.Fatalf("the approve panel's help bar = %q, want %q", got, want)
	}
	for _, dead := range []string{"comment", "reply", "list", "quit"} {
		if strings.Contains(m.helpBar(), dead) {
			t.Fatalf("the approve panel's bar advertises %q, which the panel does not take: %q", dead, m.helpBar())
		}
	}

	// Each browse gesture the bar no longer names, pressed inside the panel:
	// naming a key you do not take is one defect and taking one you did not name
	// is the other, so the panel must still be standing after every one.
	for _, dead := range []string{"c", "l", "q"} {
		cur, cmd := m.Update(tea.KeyPressMsg{Code: rune(dead[0]), Text: dead})
		m = cur.(*Model)
		if m.mode != modeConfirmApprove || cmd != nil {
			t.Fatalf("%q in the approve panel left mode = %v with cmd %v, want the panel standing and nothing", dead, m.mode, cmd != nil)
		}
	}

	m = press(m, "n")
	if m.mode != modeRead {
		t.Fatalf("n in the approve panel left mode = %v, want modeRead", m.mode)
	}
}

// TestConcurrentWriteRefusedWhileInFlight exercises the serialization
// invariant: only one write cmd may be in flight at a time, and a second
// dispatch while one is running must be refused rather than racing the model.
func TestConcurrentWriteRefusedWhileInFlight(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "why fifty?")
	m := openModel(t, f)
	m = press(m, "n") // land on the thread's block

	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}) // reload
	m = cur.(*Model)
	if !m.inFlight {
		t.Fatal("dispatching a write must claim inFlight immediately")
	}

	cur2, cmd2 := m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"}) // resolve, while reload is in flight
	m2 := cur2.(*Model)
	if cmd2 != nil {
		t.Fatal("a write dispatched while another is in flight must not run")
	}
	if !strings.Contains(m2.status, "in progress") {
		t.Fatalf("status = %q, want an in-progress hint", m2.status)
	}

	m = drain(t, m, cmd)
	if m.inFlight {
		t.Fatal("inFlight must clear once the write's result lands")
	}
}

// TestComposePostRefusedWhileInFlightKeepsText pins the no-data-loss rule
// for the refusal path: a post while another write is in flight must
// leave compose open with the typed text intact so the user just retries,
// never close the panel and silently discard the comment.
//
// THE RETRY PRESSES enter AND NOTHING ELSE, which is what the second
// pressComposePost below resolves to: the refusal returned before touching the
// ring, so the focus is still parked on the post button the first press walked
// to. A retry that tabbed again would land on cancel and throw away the very
// text this test exists to protect.
func TestComposePostRefusedWhileInFlightKeepsText(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "why fifty?")
	m := openModel(t, f)
	m = press(m, "n") // land on the thread's block

	// Dispatch a reload but hold its cmd un-delivered: inFlight stays true.
	cur, held := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = cur.(*Model)
	if !m.inFlight {
		t.Fatal("reload dispatch must claim inFlight")
	}

	// Opening compose is read-only and stays live while a write runs.
	m = press(m, "r")
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose while a write is in flight", m.mode)
	}
	m = press(m, "h", "i")

	m, cmd := pressComposePost(t, m)
	if cmd != nil {
		t.Fatal("post must not dispatch while another write is in flight")
	}
	if m.mode != modeCompose {
		t.Fatal("refused post must stay in compose")
	}
	if m.ta.Value() != "hi" {
		t.Fatalf("ta = %q, refused post must keep the typed text", m.ta.Value())
	}
	if !strings.Contains(m.status, "action in progress") {
		t.Fatalf("status = %q, want an in-progress hint", m.status)
	}

	// Deliver the held reload, then retry the post: it must land.
	m = drain(t, m, held)
	if m.inFlight {
		t.Fatal("inFlight must clear once the reload's result lands")
	}
	m, cmd = pressComposePost(t, m)
	m = drain(t, m, cmd)
	if m.mode != modeRead {
		t.Fatal("accepted post must return to read mode")
	}
	threads, err := f.svc.Threads(f.ctx, m.sess.Plan.ID)
	if err != nil || len(threads) != 1 {
		t.Fatalf("threads = %v, %v", threads, err)
	}
	if len(threads[0].Comments) != 2 || threads[0].Comments[1].Body != "hi" {
		t.Fatalf("comments = %+v, want the retried reply appended", threads[0].Comments)
	}
}

// TestPaintedModeRendersFullCanvasAndCollapsesRailBelow80 pins the full-canvas
// contract: a themed Model's raw output carries background escapes end to end
// (never falling back to a blank terminal background), the document content
// still comes through once ANSI is stripped, and the rail band — the one purely
// additive piece of painted layout — is present at 80 columns and gone at 79.
func TestPaintedModeRendersFullCanvasAndCollapsesRailBelow80(t *testing.T) {
	f := setup(t)
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.Open(f.ctx, f.svc, f.path)
	if err != nil {
		t.Fatal(err)
	}
	m := New(s, keymap.Default(), th, "", nil)
	if err := m.RefreshFromSession(f.ctx); err != nil {
		t.Fatal(err)
	}

	m.width, m.height = 100, 30
	m.rerender()
	raw := m.View().Content
	if !strings.Contains(raw, "\x1b[48;") {
		t.Fatal("painted view must carry background escape sequences")
	}
	if !strings.Contains(ansi.Strip(raw), "Rate Limiter Plan") {
		t.Fatal("ansi.Strip of the painted view must still contain the document content")
	}

	// Every rendered row — document, status bar, help bar alike — must fill
	// exactly the terminal width with no unpainted tail: a naive len() would
	// miscount the gutter's box-drawing cursor glyph, so width is measured
	// with the same display-width library lipgloss itself uses internally.
	for i, row := range strings.Split(raw, "\n") {
		if row == "" {
			continue
		}
		if w := displaywidth.String(ansi.Strip(row)); w != m.width {
			t.Fatalf("row %d: display width = %d, want %d (%q)", i, w, m.width, ansi.Strip(row))
		}
		if hasUnpaintedGap(row) {
			t.Fatalf("row %d has an unpainted gap: %q", i, ansi.Strip(row))
		}
	}

	// dark's Rail is #14171c (RGB 20,23,28); this substring only ever
	// appears in the rail band's own background escape.
	const railEscape = "48;2;20;23;28"
	m.width = 80
	m.rerender()
	if wide := m.View().Content; !strings.Contains(wide, railEscape) {
		t.Fatal("rail band must be present at width 80")
	}

	m.width = 79
	m.rerender()
	if narrow := m.View().Content; strings.Contains(narrow, railEscape) {
		t.Fatal("rail band must collapse below width 80")
	}
}

// TestReviewPaintedStatusHelpBarsNeverWrap: viewPainted's status and help bars
// are handed to a styled Width().Render call, and lipgloss's Width().Render
// silently word-wraps (not truncates) content wider than that width -- turning
// each bar into two or more real rows and overflowing the fixed altscreen by
// that much. The default help line alone runs well past the 80-column floor, so
// this fires at ordinary terminal widths, not just exotic narrow ones.
func TestReviewPaintedStatusHelpBarsNeverWrap(t *testing.T) {
	for _, width := range []int{80, 40} {
		f := setup(t)
		th, err := theme.Lookup("dark")
		if err != nil {
			t.Fatal(err)
		}
		s, err := session.Open(f.ctx, f.svc, f.path)
		if err != nil {
			t.Fatal(err)
		}
		m := New(s, keymap.Default(), th, "", nil)
		if err := m.RefreshFromSession(f.ctx); err != nil {
			t.Fatal(err)
		}
		m.width, m.height = width, 24
		m.rerender()

		content := m.View().Content
		lines := strings.Split(content, "\n")
		if len(lines) > m.height {
			t.Fatalf("width %d: total lines = %d, want <= %d (%q)", width, len(lines), m.height, content)
		}

		status := ansi.Strip(lines[len(lines)-2])
		help := ansi.Strip(lines[len(lines)-1])
		if w := displaywidth.String(status); w != width {
			t.Fatalf("width %d: status bar width = %d, want exactly %d (%q)", width, w, width, status)
		}
		if w := displaywidth.String(help); w != width {
			t.Fatalf("width %d: help bar width = %d, want exactly %d (%q)", width, w, width, help)
		}
	}
}

// TestComposeInputGroundIsTheSameEmptyOrNot pins the property: the field is
// ONE ground for the whole of its life, not grey
// while empty and black from the first keystroke on. A rebuilt blank row
// (paintTextareaRows' own hasUnpaintedGap arm) is compared before and after a
// keystroke instead of against a re-rendered style, matching groundOver's own
// shape -- reading the ground the renderer actually drew rather than asserting
// the test happens to build the same string the renderer does.
//
// IT READS A CELL INSIDE THE FIELD AND NOT THE ROW'S FIRST SPAN. A rebuilt row
// carries no glyph of its own, so this test once read it by searching for a
// single space -- and the gutter band gave such a row a SECOND span, whose
// spaces come first. That search would now answer with the band on both sides
// of the keystroke and agree with itself no matter what the field did, which is
// a test that passes rather than a test that holds.
func TestComposeInputGroundIsTheSameEmptyOrNot(t *testing.T) {
	_, m := openReviewOnDoc(t, testDoc)
	m.cursor = 1
	m.rerender()
	m = press(m, "c")
	empty := strings.Split(m.composeTextareaPainted(), "\n")
	m = press(m, "x")
	typed := strings.Split(m.composeTextareaPainted(), "\n")
	// The last row is blank in both states: rebuilt while empty, rebuilt after
	// a keystroke. Its field must not differ between the two, and the field
	// starts at the first cell past the gutter.
	firstFieldCell := textareaGutterWidth(m.ta)
	if got, want := groundAtCell(t, empty[len(empty)-1], firstFieldCell), groundAtCell(t, typed[len(typed)-1], firstFieldCell); got != want {
		t.Fatalf("blank row ground = %q empty vs %q typed -- the first keystroke still changes the field", got, want)
	}
}

// TestTextareaGutterWidthMatchesWhatTheWidgetDraws pins textareaGutterWidth
// (app/painted.go) to what bubbles actually draws rather than to a restated
// arithmetic: row 0 is the one row that carries a gutter in every draft
// state, so it typed a known first glyph and measures the CELLS in front of
// it (ansi.StringWidth, never a byte offset — the default prompt is
// lipgloss.ThickBorder().Left, a three-byte rune one cell wide).
func TestTextareaGutterWidthMatchesWhatTheWidgetDraws(t *testing.T) {
	_, m := openReviewOnDoc(t, testDoc)
	m.cursor = 1
	m.rerender()
	m = press(m, "c")
	m = press(m, "z") // a known first glyph, so the gutter is what precedes it
	row0 := strings.Split(m.ta.View(), "\n")[0]
	plain := ansi.Strip(row0)
	at := strings.Index(plain, "z")
	if at < 0 {
		t.Fatalf("row 0 = %q, want it to contain the typed glyph", plain)
	}
	// CELLS, NOT BYTES. The default prompt is lipgloss.ThickBorder().Left, a
	// three-byte rune one cell wide, so a byte offset would overstate the gutter
	// by two and this test would pass against a band that is visibly wrong.
	measured := ansi.StringWidth(plain[:at])
	if got := textareaGutterWidth(m.ta); got != measured {
		t.Fatalf("textareaGutterWidth = %d, widget drew %d cells before the text -- bubbles' gutter arithmetic has moved (see its own XXX comment about the gap constant)", got, measured)
	}
}

// TestComposeBlankRowsCarryTheGutterBand is a symptom of the same gap:
// bubbles writes a line number for row 0 and for the rows its
// placeholder text occupies and NOTHING for the rest, so an empty ten-row
// composer showed a gutter on one row and gave the reader no left edge to read
// the field's shape from until they typed. The rebuild arm paints that edge
// back on the rows the widget left blank.
//
// BOTH GROUNDS ARE TAKEN OFF ROW 0 RATHER THAN NAMED BY THEIR RGB, so the
// assertion is "the band is the ground the widget itself draws its gutter on"
// and not "the band is #2b323c" -- the latter would still pass against a band
// painted in the right colour at the wrong place, and would have to be
// rewritten for every preset.
//
// IT WALKS ROW 0's OWN GROUND TRANSITIONS FIRST, and that half of it is the pin
// on textareaPromptWidth: it asserts the widget draws its TEXT ground at cell
// prompt-1 and its GUTTER ground at cell prompt -- that the boundary the band is
// built around is the boundary the widget actually draws. A prompt of another
// width would move that transition and redden here, instead of silently
// notching every rebuilt row.
//
// CELL gutter IS READ ON THE BLANK ROW BUT NEVER ON ROW 0, because on row 0 it
// holds the reverse-video cursor over the placeholder's first letter -- an SGR 7
// with no ground of its own -- so row 0's field ground has to be read a cell
// further in, at gutter+1.
func TestComposeBlankRowsCarryTheGutterBand(t *testing.T) {
	_, m := openReviewOnDoc(t, testDoc)
	m.cursor = 1
	m.rerender()
	m = press(m, "c")
	rows := strings.Split(m.composeTextareaPainted(), "\n")
	prompt, gutter := textareaPromptWidth(m.ta), textareaGutterWidth(m.ta)
	last := rows[len(rows)-1]

	band, text := groundAtCell(t, rows[0], gutter-1), groundAtCell(t, rows[0], gutter+1)
	if band == text {
		t.Fatalf("the widget draws its gutter and its text on the same ground %q, so this test can no longer tell a band from the absence of one. paintedTextareaStyles' Placeholder field is what collapses the two: handed st.Strip again it puts row 0's placeholder text back on the gutter's ground -- symptom 1 returning and this test being disabled in the same move", band)
	}
	// Where the widget's own left column changes ground, measured off the
	// rendering rather than restated -- and the whole reason the band starts at
	// cell prompt instead of at cell 0.
	if got := groundAtCell(t, rows[0], prompt-1); got != text {
		t.Fatalf("the widget draws row 0's last prompt cell (%d) on %q, not the text ground %q -- textareaPromptWidth is not measuring the prompt", prompt-1, got, text)
	}
	if got := groundAtCell(t, rows[0], prompt); got != band {
		t.Fatalf("the widget's gutter ground does not begin at cell %d (it is %q there, want %q) -- textareaPromptWidth disagrees with where the widget starts its line-number field", prompt, got, band)
	}
	for _, tt := range []struct {
		cell int
		want string
		why  string
	}{
		{cell: 0, want: text, why: "the band notches the left edge, starting left of the widget's own gutter"},
		{cell: prompt - 1, want: text, why: "the band covers a prompt cell the widget draws on the text's ground"},
		{cell: prompt, want: band, why: "the band does not start where the widget's gutter starts"},
		{cell: gutter - 1, want: band, why: "the band is narrower than the gutter it continues"},
		{cell: gutter, want: text, why: "the band runs past the gutter into the field"},
		{cell: gutter + 1, want: text, why: "the field is not one ground"},
	} {
		if got := groundAtCell(t, last, tt.cell); got != tt.want {
			t.Fatalf("blank row cell %d is on ground %q, want %q -- %s", tt.cell, got, tt.want, tt.why)
		}
	}

	// A row the widget painted itself is never rebuilt, only padded, so its
	// own rendering -- prompt, line number and reverse-video cursor cell alike
	// -- survives verbatim inside the painted row. A band laid over every row
	// instead of only the rebuilt ones would take all three away.
	if taRow0 := strings.Split(m.ta.View(), "\n")[0]; !strings.Contains(rows[0], taRow0) {
		t.Fatalf("row 0 no longer carries the widget's own rendering verbatim -- the rebuild has reached a row the widget painted itself")
	}

	// The clamp. A frame too narrow to hold the gutter -- or too narrow even for
	// the prompt in front of it -- must still yield a row of exactly its own
	// width with nothing unpainted, rather than a span of negative width. Three
	// spans have two interior boundaries, so every one of them is walked: below
	// the prompt, at it, between it and the gutter, and at the gutter.
	// paintTextareaRows is called directly at these widths because the compose
	// panel is unusable long before them and this is a claim about the function,
	// not about a terminal anyone will drive.
	for _, width := range []int{0, prompt - 1, prompt, prompt + 1, gutter - 1, gutter} {
		narrow := strings.Split(paintTextareaRows(m.ta, width, m.styles), "\n")
		row := narrow[len(narrow)-1]
		if got := displaywidth.String(ansi.Strip(row)); got != width {
			t.Fatalf("width %d: the blank row is %d cells wide, want %d", width, got, width)
		}
		if hasUnpaintedGap(row) {
			t.Fatalf("width %d: the blank row has an unpainted gap: %q", width, ansi.Strip(row))
		}
	}
}

// TestComposePaintedRowsFullyPainted: every row of the compose panel —
// including the textarea rows, both while the draft is still empty
// (placeholder showing) and once real text exists — must carry a themed
// background across its full display width, with no gap where the user's own
// terminal background bleeds through. bubbles' textarea only reliably paints a
// row when it carries a real glyph; its own blank filler rows leave an
// unstyled gap after an internal, un-backgrounded width pad.
func TestComposePaintedRowsFullyPainted(t *testing.T) {
	f := setup(t)
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.Open(f.ctx, f.svc, f.path)
	if err != nil {
		t.Fatal(err)
	}
	m := New(s, keymap.Default(), th, "", nil)
	if err := m.RefreshFromSession(f.ctx); err != nil {
		t.Fatal(err)
	}
	m.width, m.height = 100, 30
	m.rerender()
	m = press(m, "j", "j", "c") // onto a content block, then open compose
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose", m.mode)
	}

	checkFullyPainted := func(label string) {
		t.Helper()
		out, _ := m.panelViewPainted()
		rows := strings.Split(out, "\n")
		if len(rows) < 2+m.ta.Height() {
			t.Fatalf("%s: panel has %d rows, want at least header+hint+%d textarea rows", label, len(rows), m.ta.Height())
		}
		for i, row := range rows {
			if row == "" {
				continue
			}
			stripped := ansi.Strip(row)
			if w := displaywidth.String(stripped); w != m.width {
				t.Fatalf("%s row %d: display width = %d, want %d (stripped %q)", label, i, w, m.width, stripped)
			}
			if hasUnpaintedGap(row) {
				t.Fatalf("%s row %d has an unpainted gap (stripped content %q)", label, i, stripped)
			}
		}
	}

	// Empty draft: the placeholder is showing, and every one of the
	// textarea's rows — not just the first — must be painted.
	checkFullyPainted("empty draft")

	// The fresh empty panel must come from the widget's own rendering, not a
	// hand-built substitute: the reverse-video cursor cell (SGR 7) over the
	// placeholder's first character is the visible cursor, and losing it
	// would leave a fresh compose with no cursor at all.
	const cursorSGR = "\x1b[7m"
	out, _ := m.panelViewPainted()
	if !strings.Contains(out, cursorSGR) {
		t.Fatal("empty draft: the widget's reverse-video cursor cell must be visible")
	}

	// Every one of the textarea's rows must carry the Chrome background in its
	// FIELD specifically while the draft is empty — the SAME ground this field
	// keeps for the rest of its life, not just some paint of any color —
	// dark's Chrome is #1a1e25 (RGB 26,30,37), which sgrBackground spells
	// "48;2;26;30;37".
	//
	// THE FIELD'S OWN CELL IS READ, NOT THE WHOLE ROW SEARCHED, and that is the
	// difference between a check and a claim. paintTextareaRows gives every
	// rebuilt row a gutter band, so such a row now opens with a two-cell
	// Chrome LEAD span for the prompt: a strings.Contains for the Chrome escape is
	// satisfied by that lead alone, and would stay green with the field itself
	// reverted to Strip — exactly the regression this check exists to catch.
	// The first cell past the gutter is the field, so that is where the ground
	// is read, and it is read a cell further in on row 0 because there the
	// gutter cell holds the reverse-video cursor over the placeholder's first
	// letter, an SGR 7 with no ground of its own (the same offset
	// TestComposeBlankRowsCarryTheGutterBand takes, for the same reason).
	const chromeGround = "48;2;26;30;37"
	field := textareaGutterWidth(m.ta)
	rows := strings.Split(out, "\n")
	taRows := rows[2 : 2+m.ta.Height()]
	for i, row := range taRows {
		cell := field
		if i == 0 {
			cell = field + 1
		}
		if got := groundAtCell(t, row, cell); got != chromeGround {
			t.Fatalf("empty-draft textarea row %d is on ground %q at field cell %d, want Chrome %q: %q", i, got, cell, chromeGround, ansi.Strip(row))
		}
	}

	// The first typed character must not shift the layout: the widget's
	// line-number gutter is part of the empty frame too (never stripped by a
	// bypass), so the body x-offset of row 0's content is identical across
	// the empty and first-keystroke frames.
	emptyRow0 := ansi.Strip(taRows[0])
	phOff := strings.Index(emptyRow0, "Write a comment…")
	if phOff < 0 {
		t.Fatalf("empty draft: placeholder text missing from the first textarea row: %q", emptyRow0)
	}
	m = press(m, "h")
	out, _ = m.panelViewPainted()
	if !strings.Contains(out, cursorSGR) {
		t.Fatal("first keystroke: the widget's cursor cell must stay visible")
	}
	typedRow0 := ansi.Strip(strings.Split(out, "\n")[2])
	if hOff := strings.Index(typedRow0, "h"); hOff != phOff {
		t.Fatalf("layout shift on first keystroke: content offset %d -> %d (%q -> %q)", phOff, hOff, emptyRow0, typedRow0)
	}
	checkFullyPainted("first keystroke")

	// A draft with real content exercises the textarea's own trailing filler
	// rows past the typed lines — the second place the unpainted gap showed up.
	m.ta.SetValue("first line of the draft\nsecond line of the draft")
	checkFullyPainted("typed draft")

	// ARMED, WHICH IS A FOURTH FRAME AND NOT A REPEAT OF THE THIRD: one tab
	// walks the ring off the editor onto [ post ], and that changes the paint in
	// two places at once. The button row swaps that button's ground from Control
	// to Chip — a style the three states above never render — and the widget is
	// BLURRED, so every textarea row below comes from bubbles' blurred path
	// rather than its focused one. Neither is expected to break the invariant
	// (the two labels are equal width and padRightIn pads in Strip either way,
	// and paintedTextareaStyles hands the widget the same StyleState for both
	// states), and that is precisely why the state would otherwise go
	// unexercised — a frame nobody suspects is the frame no case covers.
	m = press(m, "tab")
	if m.composeFocus != composeFocusPost {
		t.Fatalf("composeFocus = %v, want composeFocusPost — the armed frame below would be the idle one again", m.composeFocus)
	}
	checkFullyPainted("armed")
}

// TestComposeWhitespaceOnlyDraftRendersThroughWidget pins that the
// empty-vs-content rendering gate is the literal empty string, not TrimSpace: a
// whitespace-only draft is real input mid-edit and must render through the
// widget — actual spaces, visible cursor — never fall back to the placeholder
// as though nothing had been typed. (TrimSpace remains the submission gate on
// the post button, which is a different rule.)
func TestComposeWhitespaceOnlyDraftRendersThroughWidget(t *testing.T) {
	f := setup(t)
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.Open(f.ctx, f.svc, f.path)
	if err != nil {
		t.Fatal(err)
	}
	m := New(s, keymap.Default(), th, "", nil)
	if err := m.RefreshFromSession(f.ctx); err != nil {
		t.Fatal(err)
	}
	m.width, m.height = 100, 30
	m.rerender()
	m = press(m, "j", "j", "c", " ", " ")
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose", m.mode)
	}
	if m.ta.Value() != "  " {
		t.Fatalf("ta = %q, want the two typed spaces", m.ta.Value())
	}

	out, _ := m.panelViewPainted()
	if strings.Contains(ansi.Strip(out), "Write a comment…") {
		t.Fatal("whitespace-only draft must not fall back to the placeholder")
	}
	if !strings.Contains(out, "\x1b[7m") {
		t.Fatal("whitespace-only draft must keep the widget's visible cursor")
	}
	for i, row := range strings.Split(out, "\n") {
		if row == "" {
			continue
		}
		if w := displaywidth.String(ansi.Strip(row)); w != m.width {
			t.Fatalf("row %d: display width = %d, want %d", i, w, m.width)
		}
		if hasUnpaintedGap(row) {
			t.Fatalf("row %d has an unpainted gap: %q", i, ansi.Strip(row))
		}
	}
}

// rendersBackground reports whether a style's rendered output carries an
// actual SGR background parameter (a 48 that opens a background spec, not a
// 48 appearing as an RGB component inside a foreground spec).
func rendersBackground(rendered string) bool {
	for _, seq := range ansiSGR.FindAllString(rendered, -1) {
		params := strings.Split(strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m"), ";")
		for i := 0; i < len(params); i++ {
			switch params[i] {
			case "48":
				return true
			case "38":
				if i+1 < len(params) && params[i+1] == "2" {
					i += 4
				} else if i+1 < len(params) && params[i+1] == "5" {
					i += 2
				}
			}
		}
	}
	return false
}

// paintedBG extracts a style's background SGR parameter (e.g.
// "48;2;32;43;58") from its rendered output, so a test can check WHICH
// zone's background a row actually carries rather than merely that some
// background is present.
func paintedBG(rendered string) string {
	for _, seq := range ansiSGR.FindAllString(rendered, -1) {
		params := strings.Split(strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m"), ";")
		for i := 0; i < len(params); i++ {
			switch params[i] {
			case "48":
				if i+1 < len(params) && params[i+1] == "2" && i+4 < len(params) {
					return strings.Join(params[i:i+5], ";")
				}
				if i+1 < len(params) && params[i+1] == "5" && i+2 < len(params) {
					return strings.Join(params[i:i+3], ";")
				}
			case "38":
				if i+1 < len(params) && params[i+1] == "2" {
					i += 4
				} else if i+1 < len(params) && params[i+1] == "5" {
					i += 2
				}
			}
		}
	}
	return ""
}

// paintedFG is paintedBG's counterpart for the FOREGROUND parameter (e.g.
// "38;2;239;134;178"), for the assertions that are about which INK a fragment
// carries rather than which ground. It skips a background spec's own
// parameters for the same reason paintedBG skips a foreground's: a 38
// appearing as an RGB component inside a "48;2;..." is not a foreground
// introducer.
func paintedFG(rendered string) string {
	for _, seq := range ansiSGR.FindAllString(rendered, -1) {
		params := strings.Split(strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m"), ";")
		for i := 0; i < len(params); i++ {
			switch params[i] {
			case "38":
				if i+1 < len(params) && params[i+1] == "2" && i+4 < len(params) {
					return strings.Join(params[i:i+5], ";")
				}
				if i+1 < len(params) && params[i+1] == "5" && i+2 < len(params) {
					return strings.Join(params[i:i+3], ";")
				}
			case "48":
				if i+1 < len(params) && params[i+1] == "2" {
					i += 4
				} else if i+1 < len(params) && params[i+1] == "5" {
					i += 2
				}
			}
		}
	}
	return ""
}

// paintedFGNear is paintedFG's counterpart for a rendered string that MIXES
// ZONES -- several concatenated Render() calls, each its own combined
// "38;...;48;...m" escape (verified against the vendored lipgloss: one
// Style.Render() call emits foreground and background together in a single SGR
// sequence, never two) -- where paintedFG's own "first 38 anywhere in the
// string" is the WRONG answer whenever a caller's zone is not the string's
// first one.
//
// centredBox's field-band row is exactly this shape, Chrome.Render("┃  ") +
// Field.Render(text) + Chrome.Render("  ┃"), and paintedFG(row) returns
// CHROME's foreground -- the first Render() call in the row -- whatever Field's
// own foreground is, so an assertion written on it compares Chrome to Chrome
// and is vacuous by construction.
//
// bg IDENTIFIES THE RIGHT SEQUENCE, not position: it is the background
// parameter (paintedBG's own return shape) of the zone whose foreground is
// wanted, and this returns the 38 parameter out of the SAME escape sequence
// that carries it -- the one Render() call that actually painted that ground,
// whichever position in the string it occupies.
func paintedFGNear(rendered, bg string) string {
	for _, seq := range ansiSGR.FindAllString(rendered, -1) {
		if !strings.Contains(seq, bg) {
			continue
		}
		params := strings.Split(strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m"), ";")
		for i := 0; i < len(params); i++ {
			switch params[i] {
			case "38":
				if i+1 < len(params) && params[i+1] == "2" && i+4 < len(params) {
					return strings.Join(params[i:i+5], ";")
				}
				if i+1 < len(params) && params[i+1] == "5" && i+2 < len(params) {
					return strings.Join(params[i:i+3], ";")
				}
			case "48":
				if i+1 < len(params) && params[i+1] == "2" {
					i += 4
				} else if i+1 < len(params) && params[i+1] == "5" {
					i += 2
				}
			}
		}
	}
	return ""
}

// TestComposePaintStylesAlwaysCarryBackground guards hasUnpaintedGap's
// operating invariant (see its comment): the detector reads "any active SGR" as
// "background painted", which is only sound while every style on the compose
// paint path sets a background alongside its foreground. If a foreground-only
// style ever reaches that path, this fails before the detector can silently
// mislabel a naked-background run as painted.
//
// THE MAP IS HAND-MAINTAINED, SO WHAT MAY JOIN IT MATTERS AS MUCH AS WHAT MUST:
// membership is "panelViewPainted's modeCompose arm renders through it", not
// "ui.Styles exports it". This list already fails silently by OMISSION -- a new
// style on that arm that nobody adds here is simply unchecked -- and a member
// that is NOT on the arm costs the other direction, because a criterion with a
// counter-example in it stops being usable to decide the next entry.
// ui.Styles.Field is the near miss and is deliberately absent: it is re-point's
// input ground (centredBox's inkField, repointInputRow), a body spliced into
// the middle of the finished frame by composeCentredPanel and never drawn by
// the compose arm at all. It sat in this map from when the IDLE button wore
// Field, until a later change moved the button to Control and left the entry
// behind.
func TestComposePaintStylesAlwaysCarryBackground(t *testing.T) {
	for _, name := range []string{"dark", "light"} {
		th, err := theme.Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		st := ui.NewStyles(th)
		ta := paintedTextareaStyles(st)
		styles := map[string]interface{ Render(...string) string }{
			"Chrome":              st.Chrome,
			"Strip":               st.Strip,
			"FocusHeader":         st.FocusHeader,
			"Chip":                st.Chip,
			"Control":             st.Control,
			"ta.Base":             ta.Focused.Base,
			"ta.Text":             ta.Focused.Text,
			"ta.CursorLine":       ta.Focused.CursorLine,
			"ta.LineNumber":       ta.Focused.LineNumber,
			"ta.CursorLineNumber": ta.Focused.CursorLineNumber,
			"ta.Placeholder":      ta.Focused.Placeholder,
			"ta.Prompt":           ta.Focused.Prompt,
			"ta.EndOfBuffer":      ta.Focused.EndOfBuffer,
		}
		for label, s := range styles {
			if !rendersBackground(s.Render("x")) {
				t.Fatalf("%s: compose-path style %s renders without a background parameter, breaking hasUnpaintedGap's invariant", name, label)
			}
		}
	}
}

// drain runs returned commands to completion, feeding messages back.
func drain(t *testing.T, m *Model, cmd tea.Cmd) *Model {
	t.Helper()
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			break
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				m = drain(t, m, c)
			}
			break
		}
		cur, next := m.Update(msg)
		m = cur.(*Model)
		cmd = next
	}
	return m
}

// inferPlanTitle asks seedThreadOn for the title the status bar would infer
// for the document anyway (ui.InferTitle, which statusIdentity calls itself
// while no plan exists). A fixture seeded under it reads on screen exactly as
// the unseeded one does, so a thread can be added to a document without
// moving the identity segment a test is asserting on.
const inferPlanTitle = ""

// seedThreadOn posts a comment on the block whose text contains want, creating
// the plan under title first if this is the fixture's first write. It is the
// only seed that puts a PLACED thread on a block a test gets to choose:
// seedThread and addThread find their block by the literal "token bucket",
// which is setup()'s document and no other, and seedOrphan's variants work on
// any fixture but produce an ORPHAN, on no block at all.
//
// THE CREATE IS CONDITIONAL, as seedOrphanAtPath's is: a second Create on an
// existing plan errors, and a fixture that wants two placed threads calls this
// twice.
func seedThreadOn(t *testing.T, f fixture, title, want, body string) {
	t.Helper()
	s, err := session.Open(f.ctx, f.svc, f.path)
	if err != nil {
		t.Fatal(err)
	}
	blocks := ui.ParseBlocks(s.Content, nil)
	if !s.Exists {
		if title == inferPlanTitle {
			title = ui.InferTitle(blocks, s.Path)
		}
		if err := s.Create(f.ctx, title); err != nil {
			t.Fatal(err)
		}
	}
	var target ui.Block
	for _, b := range blocks {
		if strings.Contains(b.Text, want) {
			target = b
		}
	}
	// A miss here hands AnchorForBlockText a zero ui.Block, whose anchor spans
	// nothing: the seed silently produces an UNANCHORED thread and the test
	// that asked for one on a block fails somewhere else entirely.
	if target.Text == "" {
		t.Fatalf("no block of %s contains %q", f.path, want)
	}
	a, err := session.AnchorForBlockText(string(s.Content), ui.BlockAnchorSpan(target), target.HeadingPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Comment(f.ctx, a, body); err != nil {
		t.Fatal(err)
	}
}

// seedThread is seedThreadOn aimed at setup()'s document, which is what nearly
// every test in this package runs on.
//
// THE TITLE IS NOT THE DOCUMENT'S OWN H1 and is not inferred: a plan whose title
// is neither the file's base name nor the heading on screen lets a test tell
// which of the three a line was built from, and "Rate Limiter" beside
// "# Rate Limiter Plan" in plan.md is that.
func seedThread(t *testing.T, f fixture, body string) {
	t.Helper()
	seedThreadOn(t, f, "Rate Limiter", "token bucket", body)
}

func TestFullProgramSmoke(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "smoke thread")
	s, err := session.Open(f.ctx, f.svc, f.path)
	if err != nil {
		t.Fatal(err)
	}
	m := New(s, keymap.Default(), nil, "", nil)

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return bytes.Contains(bts, []byte("Rate Limiter Plan"))
	}, teatest.WithDuration(3*time.Second))
	tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

// TestFullProgramSmokeWithWrite drives a real, concurrently-running tea.Program
// (unlike the drain-based tests, which run cmds synchronously in the test
// goroutine and so never race with anything) through a plan create + comment
// post. bubbletea's event loop calls View() after every message it processes,
// so this is what actually exercises the window between dispatching a write and
// its result landing -- where a Model reading live *session.Session fields
// concurrently mutated by the write's goroutine (session.Create touches
// s.Plan/s.Exists) would show up under -race.
func TestFullProgramSmokeWithWrite(t *testing.T) {
	f := setup(t) // plan-less: the write below must create it
	s, err := session.Open(f.ctx, f.svc, f.path)
	if err != nil {
		t.Fatal(err)
	}
	m := New(s, keymap.Default(), nil, "", nil)

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return bytes.Contains(bts, []byte("Rate Limiter Plan"))
	}, teatest.WithDuration(3*time.Second))

	tm.Send(tea.KeyPressMsg{Code: 'j', Text: "j"})
	tm.Send(tea.KeyPressMsg{Code: 'j', Text: "j"}) // off the heading, onto a content block
	tm.Send(tea.KeyPressMsg{Code: 'c', Text: "c"}) // plan-less -> straight to compose
	tm.Send(tea.KeyPressMsg{Code: 'h', Text: "h"})
	tm.Send(tea.KeyPressMsg{Code: 'i', Text: "i"})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})   // off the editor, onto the post button
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter}) // post: creates the plan, then the comment

	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return bytes.Contains(bts, []byte("comment posted"))
	}, teatest.WithDuration(3*time.Second))
	tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

// secondFixtureStore opens a second localfs.Store on the same state path as f:
// the cross-process flock makes this safe, standing in for an agent writing
// review facts over MCP while the TUI has the same file open.
func secondFixtureStore(t *testing.T, f fixture) fixture {
	t.Helper()
	svc := newFixtureStore(t, filepath.Join(filepath.Dir(f.path), "state.json"))
	return fixture{svc: svc, cas: f.cas, path: f.path, ctx: attrCtx("agent")}
}

// addThread comments on a plan f already has and never calls Create: it is for
// the second writer, on a plan a prior seedThread (on this or another store on
// the same path) created, and it refuses to bring a plan into being.
func addThread(t *testing.T, f fixture, body string) {
	t.Helper()
	s, err := session.Open(f.ctx, f.svc, f.path)
	if err != nil {
		t.Fatal(err)
	}
	blocks := ui.ParseBlocks(s.Content, nil)
	var target ui.Block
	for _, b := range blocks {
		if strings.Contains(b.Text, "token bucket") {
			target = b
		}
	}
	a, err := session.AnchorForBlockText(string(s.Content), ui.BlockAnchorSpan(target), target.HeadingPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Comment(f.ctx, a, body); err != nil {
		t.Fatal(err)
	}
}

// TestStateChangeRefreshesInReadMode pins the immediate-refresh half of live
// review-fact refresh: a state change arriving in idle read mode reprojects
// from the session right on the loop and flashes a status, so a thread an
// agent posts over MCP shows up without a manual reload.
func TestStateChangeRefreshesInReadMode(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "seed") // plan must already exist: Session.Exists is a
	// snapshot taken at Open time, so a model opened before any plan
	// exists can never observe one appearing via refresh alone.
	m := openModel(t, f)

	addThread(t, secondFixtureStore(t, f), "written by another process")

	cur, cmd := m.Update(msgStateChanged{})
	m = drain(t, cur.(*Model), cmd)
	m = press(m, "n", "enter") // jump to the thread's block and expand it

	if !strings.Contains(m.View().Content, "written by another process") {
		t.Fatal("state change in read mode must refresh immediately")
	}
	if !strings.Contains(m.status, "review updated") {
		t.Fatalf("status = %q, want the live-refresh flash", m.status)
	}
}

// TestStateChangeRefreshesPlanCreatedAfterOpen: session.Session.Exists is a
// snapshot taken once, at Open. A model opened on a path with no plan record
// yet — the common case for a document nobody has reviewed — can never see a
// plan another actor (an agent's first MCP comment) creates afterward, because
// Placements and ApprovedCurrent both short-circuit on !Exists forever. Unlike
// TestStateChangeRefreshesInReadMode, no plan exists when m is opened here.
func TestStateChangeRefreshesPlanCreatedAfterOpen(t *testing.T) {
	f := setup(t)
	m := openModel(t, f)
	if m.planExists {
		t.Fatal("no plan should exist yet")
	}

	// Another actor lazily creates the plan and posts a thread through a
	// second store on the same state path — mirrors an agent's first
	// `comment` call over MCP racing ahead of the TUI opening the file.
	seedThread(t, secondFixtureStore(t, f), "lazily created plan thread")

	cur, cmd := m.Update(msgStateChanged{})
	m = drain(t, cur.(*Model), cmd)
	m = press(m, "n", "enter") // jump to the thread's block and expand it

	if !strings.Contains(m.View().Content, "lazily created plan thread") {
		t.Fatal("a plan (and thread) created after Open by another actor must become visible via live refresh")
	}
	if !strings.Contains(m.status, "review updated") {
		t.Fatalf("status = %q, want the live-refresh flash", m.status)
	}
}

// TestStateChangeFlashesOnPlanCreatedWithZeroThreads pins a fingerprint gap:
// factsFingerprint hashes m.approved and every thread's facts, but not
// m.planExists itself. A plan that appears via another actor's Create with no
// comment and no approval flips planExists false to true while leaving both the
// (empty) thread rows and approved unchanged, so the before/after fingerprints
// match and the live-refresh flash never fires even though the document went
// from unreviewed to under review.
func TestStateChangeFlashesOnPlanCreatedWithZeroThreads(t *testing.T) {
	f := setup(t)
	m := openModel(t, f)
	if m.planExists {
		t.Fatal("no plan should exist yet")
	}

	// Another actor creates the plan through a second store on the same
	// state path, with no comment and no approval — Exists flips, nothing
	// else about the projected facts does.
	f2 := secondFixtureStore(t, f)
	s2, err := session.Open(f2.ctx, f2.svc, f2.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Create(f2.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}

	cur, cmd := m.Update(msgStateChanged{})
	m = drain(t, cur.(*Model), cmd)

	if !m.planExists {
		t.Fatal("live refresh must pick up the plan another actor created")
	}
	if !strings.Contains(m.status, "review updated") {
		t.Fatalf("status = %q, want the live-refresh flash for the exists transition even with zero threads", m.status)
	}
}

// TestStateChangeEchoAfterWriteDoesNotClobberConfirmation pins the
// unchanged-projection guard: every TUI write triggers its own fsnotify event
// (the coalesced watch on state.json can't tell "another process wrote" from "I
// just wrote"), and that echo typically lands after handleActionDone has
// already set the write's own confirmation status. A msgStateChanged that finds
// nothing actually changed must leave that confirmation — and the rendered view
// — alone rather than clobbering it with the generic "review updated" flash.
func TestStateChangeEchoAfterWriteDoesNotClobberConfirmation(t *testing.T) {
	f := setup(t)
	m := openModel(t, f)

	// Complete a real comment post, same flow as
	// TestCommentFlowCreatesPlanAndThread, so status lands on the write's own
	// confirmation.
	m = press(m, "j", "j", "c") // onto a content block, then compose
	m = press(m, "h", "i")
	m, cmd := pressComposePost(t, m)
	m = drain(t, m, cmd)
	if m.status != "comment posted" {
		t.Fatalf("status = %q before the echo, want %q", m.status, "comment posted")
	}
	beforeView := m.View().Content

	// Nothing else touched state.json since: this stands in for the TUI's
	// own save echoing back through the watch.
	cur, cmd := m.Update(msgStateChanged{})
	m = cur.(*Model)
	_ = cmd // nil here: m.stateChanged is nil in this test, same as any other disabled-watch Update

	if m.status != "comment posted" {
		t.Fatalf("status = %q after a no-op echo, want the write's confirmation %q unclobbered", m.status, "comment posted")
	}
	if got := m.View().Content; got != beforeView {
		t.Fatal("a pure echo with no underlying state change must not alter the rendered view")
	}
}

// TestStateChangeFlashesOnPlacementTransitionNotJustBadge: Badge is "" both for
// an exactly-placed thread and for an orphaned one (RefreshFromSession never
// sets Badge on the unanchored path), so a thread crossing between placed and
// orphaned — same ID, same resolved flag, same comment count — must still
// register as a change via Placed, or the flash (and the fact that the thread
// now renders inline instead of in the unanchored section) would silently drop
// just like the badge-only echo case.
func TestStateChangeFlashesOnPlacementTransitionNotJustBadge(t *testing.T) {
	f := setup(t)
	s, err := session.Open(f.ctx, f.svc, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	// A heading path with no match anywhere in testDoc: guaranteed to reanchor
	// as orphaned, not just fuzzy/moved.
	orphanAnchor := reanchor.Anchor{HeadingPath: []string{"Nonexistent Heading Path Zzz"}}
	seeded, err := s.Comment(f.ctx, orphanAnchor, "orphaned from the start")
	if err != nil {
		t.Fatal(err)
	}

	m := openModel(t, f)
	if len(m.unanchored) != 1 {
		t.Fatalf("unanchored = %d, want the seeded thread to start orphaned", len(m.unanchored))
	}

	// A second actor rehomes the thread onto a real heading. The document
	// file itself never changes.
	f2 := secondFixtureStore(t, f)
	s2, err := session.Open(f2.ctx, f2.svc, f2.path)
	if err != nil {
		t.Fatal(err)
	}
	newAnchor := reanchor.Anchor{HeadingPath: []string{"Rate Limiter Plan", "Context"}}
	if err := s2.RehomeToAnchor(f2.ctx, seeded.ID, newAnchor); err != nil {
		t.Fatal(err)
	}

	cur, cmd := m.Update(msgStateChanged{})
	m = drain(t, cur.(*Model), cmd)

	if len(m.unanchored) != 0 {
		t.Fatalf("unanchored = %d, want 0 once the rehome lands the thread on a block", len(m.unanchored))
	}
	if !strings.Contains(m.status, "review updated") {
		t.Fatalf("status = %q, want the live-refresh flash for a placed<->orphaned transition (badge unchanged, both empty)", m.status)
	}
}

// TestStateChangeDeferredDuringCompose pins the no-data-loss half: a state
// change arriving mid-compose must never touch the open draft. It only sets
// pendingRefresh, which esc later consumes.
func TestStateChangeDeferredDuringCompose(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "seed") // see TestStateChangeRefreshesInReadMode
	m := openModel(t, f)
	m = press(m, "j", "j", "c") // onto a content block, then open compose
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose", m.mode)
	}
	m = press(m, "h", "i")

	addThread(t, secondFixtureStore(t, f), "written during compose")

	cur, cmd := m.Update(msgStateChanged{})
	m = cur.(*Model)
	_ = cmd // nil here: m.stateChanged is nil in this test, same as any other disabled-watch Update

	if m.mode != modeCompose {
		t.Fatal("a deferred state change must not leave compose")
	}
	if m.ta.Value() != "hi" {
		t.Fatalf("ta = %q, a deferred state change must not touch the draft", m.ta.Value())
	}
	if !m.pendingRefresh {
		t.Fatal("state change during compose must set pendingRefresh")
	}
	if strings.Contains(m.View().Content, "written during compose") {
		t.Fatal("refresh must be deferred, not applied immediately")
	}

	m = press(m, "esc")
	if m.mode != modeRead {
		t.Fatalf("mode = %v, want modeRead after esc", m.mode)
	}
	if m.pendingRefresh {
		t.Fatal("esc must consume the pending refresh")
	}
	m = press(m, "n", "enter")
	if !strings.Contains(m.View().Content, "written during compose") {
		t.Fatal("esc must apply the deferred refresh")
	}
}

// TestStateChangeDeferredWhileInFlight pins the third deferral case: a state
// change arriving while a write is in flight must wait for that write's own
// return-to-read seam (handleActionDone), never race the write's result. The
// held write is a resolve, not a reload, deliberately: a reload re-reads
// everything on its own, which would make the post-drain projection assertion
// prove nothing about this seam. handleActionDone must consume the deferral
// without clobbering the write's own status.
func TestStateChangeDeferredWhileInFlight(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "seed") // see TestStateChangeRefreshesInReadMode
	m := openModel(t, f)
	m = press(m, "n") // land on the seeded thread's block

	// Dispatch a resolve but hold its cmd un-delivered: inFlight stays true,
	// same pattern as TestConcurrentWriteRefusedWhileInFlight.
	cur, held := m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	m = cur.(*Model)
	if !m.inFlight {
		t.Fatal("resolve dispatch must claim inFlight")
	}

	addThread(t, secondFixtureStore(t, f), "written by another process")

	cur, cmd := m.Update(msgStateChanged{})
	m = cur.(*Model)
	_ = cmd // nil here: m.stateChanged is nil in this test, same as any other disabled-watch Update
	if !m.pendingRefresh {
		t.Fatal("state change while inFlight must set pendingRefresh")
	}
	if m.threadCount() != 1 {
		t.Fatalf("threadCount = %d, want 1 (only the pre-existing thread; refresh deferred until the write lands)", m.threadCount())
	}

	m = drain(t, m, held)
	if m.inFlight {
		t.Fatal("inFlight must clear once the write's result lands")
	}
	if m.pendingRefresh {
		t.Fatal("handleActionDone must consume the pending refresh")
	}
	if m.threadCount() != 2 {
		t.Fatalf("threadCount = %d, want 2 (handleActionDone's own refresh must project the other process's write)", m.threadCount())
	}
	if m.status != "thread updated" {
		t.Fatalf("status = %q, want the write's own %q (a deferred refresh must not clobber it)", m.status, "thread updated")
	}
}

// TestApproveCancelDuringInFlightWriteDoesNotConsumePendingRefresh pins a guard
// inside maybeApplyPendingRefresh: ActApprove opens the confirm panel without
// gating on dispatchOK, so its cancel ("n"/"esc") can run while an earlier write
// is still in flight. Consuming a pending refresh there would call
// RefreshFromSession — reading m.sess.Plan/m.sess.Exists — concurrently with
// that write's own goroutine mutating them (session.Create), so cancel must
// leave pendingRefresh set for handleActionDone to consume once it is safe.
func TestApproveCancelDuringInFlightWriteDoesNotConsumePendingRefresh(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "seed")
	m := openModel(t, f)

	cur, held := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}) // reload, held
	m = cur.(*Model)
	if !m.inFlight {
		t.Fatal("reload dispatch must claim inFlight")
	}

	m = press(m, "a") // ActApprove does not gate on dispatchOK
	if m.mode != modeConfirmApprove {
		t.Fatalf("mode = %v, want modeConfirmApprove even while a write is in flight", m.mode)
	}

	cur, cmd := m.Update(msgStateChanged{})
	m = cur.(*Model)
	_ = cmd
	if !m.pendingRefresh {
		t.Fatal("state change while inFlight must set pendingRefresh")
	}

	m = press(m, "n") // cancel
	if m.mode != modeRead {
		t.Fatalf("mode = %v, want modeRead after cancel", m.mode)
	}
	if !m.pendingRefresh {
		t.Fatal("cancel during an in-flight write must not consume pendingRefresh (would race the write's goroutine)")
	}
	if !m.inFlight {
		t.Fatal("cancel must not touch inFlight; the original reload is still running")
	}

	m = drain(t, m, held)
	if m.pendingRefresh {
		t.Fatal("the reload's own handleActionDone must consume the pending refresh once it's safe")
	}
}

// corruptState overwrites f's state.json with invalid JSON, so the next
// operation any Store on this path performs fails deterministically (no
// sleeps, no races) with a "corrupt local store" error.
func corruptState(t *testing.T, f fixture) {
	t.Helper()
	statePath := filepath.Join(filepath.Dir(f.path), "state.json")
	if err := os.WriteFile(statePath, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestStateChangeRefreshErrorInReadModeLeavesPendingRefreshSet: the idle
// msgStateChanged branch must attempt RefreshFromSession before clearing
// pendingRefresh, so an erroring refresh leaves the flag set for a later seam to
// retry rather than silently forfeiting it. pendingRefresh is seeded true here
// as an earlier deferred state change would have left it.
func TestStateChangeRefreshErrorInReadModeLeavesPendingRefreshSet(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "seed")
	m := openModel(t, f)
	m.pendingRefresh = true

	corruptState(t, f)

	cur, cmd := m.Update(msgStateChanged{})
	m = cur.(*Model)
	_ = cmd // nil here: m.stateChanged is nil in this test, same as any other disabled-watch Update

	if !m.pendingRefresh {
		t.Fatal("a refresh error in idle read mode must leave pendingRefresh set, not clear it before the refresh runs")
	}
	if !strings.Contains(m.status, "error") {
		t.Fatalf("status = %q, want an error status", m.status)
	}
}

// TestMaybeApplyPendingRefreshErrorLeavesPendingRefreshSet pins the matching
// fix in maybeApplyPendingRefresh: an erroring refresh at the compose-esc
// seam must leave pendingRefresh set instead of forfeiting the deferred
// signal, same contract as the idle msgStateChanged branch above.
func TestMaybeApplyPendingRefreshErrorLeavesPendingRefreshSet(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "seed")
	m := openModel(t, f)
	m = press(m, "j", "j", "c") // onto a content block, then open compose
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose", m.mode)
	}

	cur, cmd := m.Update(msgStateChanged{})
	m = cur.(*Model)
	_ = cmd
	if !m.pendingRefresh {
		t.Fatal("state change during compose must set pendingRefresh")
	}

	corruptState(t, f)

	m = press(m, "esc")
	if m.mode != modeRead {
		t.Fatalf("mode = %v, want modeRead after esc", m.mode)
	}
	if !m.pendingRefresh {
		t.Fatal("a refresh error at the compose-esc seam must leave pendingRefresh set, not forfeit it")
	}
	if !strings.Contains(m.status, "error") {
		t.Fatalf("status = %q, want an error status", m.status)
	}
}

// TestWaitStateChangeCmd is a narrow unit test of waitStateChange's two
// outcomes: a message when the channel delivers, nil (no message) once it's
// closed — the watcher-stopped signal the cmd must not turn into a bogus
// refresh.
func TestWaitStateChangeCmd(t *testing.T) {
	f := setup(t)
	m := openModel(t, f)
	ch := make(chan struct{}, 1)
	m.stateChanged = ch

	ch <- struct{}{}
	if msg := m.waitStateChange()(); msg == nil {
		t.Fatal("cmd must return a message when the channel delivers")
	} else if _, ok := msg.(msgStateChanged); !ok {
		t.Fatalf("msg = %#v, want msgStateChanged", msg)
	}

	close(ch)
	if msg := m.waitStateChange()(); msg != nil {
		t.Fatalf("msg = %#v, want nil once the channel is closed", msg)
	}
}

// TestWatchState drives WatchState against the real filesystem: a write to
// dir/state.json must produce an event, and stop must close the channel. The
// bounded selects below are an event wait against a real fsnotify watcher,
// not a sleep — there is no deterministic alternative to waiting on the
// actual OS notification.
func TestWatchState(t *testing.T) {
	dir := t.TempDir()
	ch, stop, err := WatchState(dir)
	if err != nil {
		t.Fatal(err)
	}

	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(statePath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("no event within 2s of writing state.json")
	}

	// Drain until the channel is CLOSED rather than asserting the very next
	// receive is the close. Asserting on the next receive additionally assumes
	// one os.WriteFile produces exactly one fsnotify event -- ch is buffered
	// (size 1), so a second event from the same write can already be sitting in
	// it when stop() runs, and the receive below reads that queued event instead
	// of the close. That was a repeated flake, passing on re-run every time.
	stop()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, open := <-ch:
			if !open {
				return
			}
			// A queued event from the write above; keep draining.
		case <-deadline:
			t.Fatal("channel did not close within 2s of stop")
		}
	}
}

// seedOrphan posts a comment anchored to a heading path absent from the fixture
// doc, with a span that shares vocabulary with the doc ("token bucket") but
// isn't a literal match — so it reanchors as orphaned *with* candidates rather
// than orphaned with none. It writes through a second store on f's path (as
// secondFixtureStore does for a concurrent actor) so it works whether or not
// f's own session is already open, and creates the plan first if this is the
// fixture's first write.
func seedOrphan(t *testing.T, f fixture, body string) {
	t.Helper()
	seedOrphanSpan(t, f, "token bucket refill configuration", body)
}

// seedOrphanSpan is seedOrphan's variant for tests that need several
// distinguishable orphans in the same fixture: each call takes its own span (so
// orphans in the same queue don't collide) and body (so a test can identify
// which orphan is which regardless of queue order).
func seedOrphanSpan(t *testing.T, f fixture, span, body string) {
	t.Helper()
	seedOrphanAtPath(t, f, []string{"nonexistent"}, span, body)
}

// seedOrphanAtPath is the fully-parameterized seed: the anchor's heading
// path is the caller's own, for tests that pin targetRelocate's
// surviving-ancestor walk over the orphan's ORIGINAL heading path
// (p.Thread.Anchor.HeadingPath) and so need paths whose prefixes do (or
// deliberately don't) survive in the fixture doc.
func seedOrphanAtPath(t *testing.T, f fixture, headingPath []string, span, body string) {
	t.Helper()
	f2 := secondFixtureStore(t, f)
	s, err := session.Open(f2.ctx, f2.svc, f2.path)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Exists {
		if err := s.Create(f2.ctx, "Rate Limiter"); err != nil {
			t.Fatal(err)
		}
	}
	orphanAnchor := reanchor.Anchor{
		HeadingPath: headingPath,
		Span:        span,
	}
	if _, err := s.Comment(f2.ctx, orphanAnchor, body); err != nil {
		t.Fatal(err)
	}
}

// openFixtureModel opens a Model on an existing fixture and drives it through
// Init() to completion, the path a real program takes at startup. Unlike
// openModel, which calls RefreshFromSession directly and bypasses Update, this
// exercises Update's msgRefreshed handling — needed for tests that pin
// startup-only behavior (the unresolved-orphan flash).
func openFixtureModel(t *testing.T, f fixture) *Model {
	t.Helper()
	s, err := session.Open(f.ctx, f.svc, f.path)
	if err != nil {
		t.Fatal(err)
	}
	m := New(s, keymap.Default(), nil, "", nil)
	m.width, m.height = 100, 30
	return drain(t, m, m.Init())
}

// TestRefreshRetainsOrphanCandidates pins that RefreshFromSession keeps the
// full orphan Placement (candidates included) rather than discarding it into
// a bare ui.ThreadView: relocate triages from the retained Thread (original
// anchor, full comments), and the candidates — no longer surfaced by the TUI
// — still matter to agents reading placements over MCP.
func TestRefreshRetainsOrphanCandidates(t *testing.T) {
	fx := setup(t)
	m := openModel(t, fx)
	seedOrphan(t, fx, "orphaned body")

	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = drain(t, cur.(*Model), cmd)

	if len(m.orphans) != 1 {
		t.Fatalf("orphans = %d, want 1", len(m.orphans))
	}
	if m.orphans[0].Thread.Comments[0].Body != "orphaned body" {
		t.Fatalf("wrong orphan retained: %+v", m.orphans[0].Thread)
	}
	// Candidates survive projection (the fixture's span shares vocabulary
	// with the doc, so reanchor produces at least one candidate).
	if len(m.orphans[0].Candidates) == 0 {
		t.Fatal("candidates were discarded by RefreshFromSession")
	}
}

// TestOpenFlashesUnresolvedOrphanCount pins the startup-only awareness
// flash: opening on a fixture that already has an unresolved orphan must
// surface the count and the relocate entry key, so a human never has to go
// looking for orphaned threads.
func TestOpenFlashesUnresolvedOrphanCount(t *testing.T) {
	fx := setup(t)
	seedOrphan(t, fx, "lost comment")

	m := openFixtureModel(t, fx) // opens AFTER seeding, so startup sees the orphan

	if !strings.Contains(m.status, "1 orphaned") || !strings.Contains(m.status, "m to relocate") {
		t.Fatalf("status = %q, want orphan flash with entry key", m.status)
	}
}

// TestRelocateKeyWithNoOrphans: ActRelocate with no unresolved orphans refuses
// and stays in read mode.
func TestRelocateKeyWithNoOrphans(t *testing.T) {
	fx := setup(t)
	m := openModel(t, fx)

	m = press(m, "m")

	if m.mode != modeRead {
		t.Fatal("must stay in read mode")
	}
	if !strings.Contains(m.status, "no orphaned threads") {
		t.Fatalf("status = %q", m.status)
	}
}

// TestRelocateEntryJumpsToOldSection pins targetRelocate's entry-jump contract:
// an orphan whose old heading path's leaf no longer exists, but whose parent
// heading survives, lands the cursor on that surviving parent — walking
// p.Thread.Anchor.HeadingPath from full depth toward the root via
// ui.FindHeadingBlock.
func TestRelocateEntryJumpsToOldSection(t *testing.T) {
	fx := setup(t)
	// The "Rate Limiter Plan › Design" section survives in the fixture doc
	// (testDoc has "## Design"); the leaf never existed as a real heading.
	seedOrphanAtPath(t, fx, []string{"Rate Limiter Plan", "Design", "Token Bucket Details"}, "token bucket refill configuration", "lost comment")
	m := openFixtureModel(t, fx)

	m = press(m, "m")
	if m.mode != modeRelocate {
		t.Fatal("m must enter relocate mode")
	}
	want := ui.FindHeadingBlock(m.blocks, []string{"Rate Limiter Plan", "Design"})
	if want < 0 {
		t.Fatal("fixture assumption broken: no surviving Design heading block")
	}
	if m.cursor != want {
		t.Fatalf("cursor = %d, want the surviving parent heading block %d", m.cursor, want)
	}
}

// TestRelocateEntryLeavesCursorWhenOldPathFullyVanished pins the jump's
// other half: when no prefix of the orphan's old heading path matches any
// surviving heading, the cursor is left exactly where it was.
func TestRelocateEntryLeavesCursorWhenOldPathFullyVanished(t *testing.T) {
	fx := setup(t)
	seedOrphanAtPath(t, fx, []string{"Nowhere", "Also Nowhere"}, "token bucket refill configuration", "lost comment")
	m := openFixtureModel(t, fx)
	m.cursor = 1
	before := m.cursor

	m = press(m, "m")
	if m.mode != modeRelocate {
		t.Fatal("m must enter relocate mode")
	}
	if m.cursor != before {
		t.Fatalf("cursor = %d, want unmoved at %d — no prefix of the vanished path matched any heading", m.cursor, before)
	}
}

// TestRelocateNAndNCyclesOrphans pins n/N's job in relocate mode: they move
// forward and backward through the unresolved-orphan queue itself (tab's own
// semantics), wrapping both ways. The queue is snapshotted right after entry as
// ground truth rather than assuming seed order, as every other multi-orphan
// test in this file identifies orphans by body rather than by index.
func TestRelocateNAndNCyclesOrphans(t *testing.T) {
	fx := setup(t)
	seedOrphanSpan(t, fx, "token bucket refill configuration", "orphan A")
	seedOrphanSpan(t, fx, "unbounded requests overwhelming the database", "orphan B")
	seedOrphanSpan(t, fx, "rate limiting a burst of concurrent client requests", "orphan C")
	m := openFixtureModel(t, fx)

	m = press(m, "m")
	if m.mode != modeRelocate {
		t.Fatal("must enter relocate mode")
	}
	q := m.unresolvedOrphans()
	if len(q) != 3 {
		t.Fatalf("fixture assumption broken: queue = %d, want 3", len(q))
	}
	if m.relocateTID != q[0].Thread.ID {
		t.Fatalf("relocateTID = %v, want the queue head %v", m.relocateTID, q[0].Thread.ID)
	}

	m = press(m, "n")
	if m.relocateTID != q[1].Thread.ID {
		t.Fatalf("relocateTID = %v, want %v after n", m.relocateTID, q[1].Thread.ID)
	}
	m = press(m, "n")
	if m.relocateTID != q[2].Thread.ID {
		t.Fatalf("relocateTID = %v, want %v after n n", m.relocateTID, q[2].Thread.ID)
	}
	m = press(m, "n") // wraps forward
	if m.relocateTID != q[0].Thread.ID {
		t.Fatalf("relocateTID = %v, want wrap back to %v", m.relocateTID, q[0].Thread.ID)
	}
	m = press(m, "N") // wraps backward
	if m.relocateTID != q[2].Thread.ID {
		t.Fatalf("relocateTID = %v, want backward wrap to %v", m.relocateTID, q[2].Thread.ID)
	}
	m = press(m, "N")
	if m.relocateTID != q[1].Thread.ID {
		t.Fatalf("relocateTID = %v, want %v after N", m.relocateTID, q[1].Thread.ID)
	}
}

// TestRelocateFreeNavigation pins that ordinary navigation keys pass through
// in relocate mode (for manual placement away from any candidate) without
// leaving the mode or closing the panel.
func TestRelocateFreeNavigation(t *testing.T) {
	fx := setup(t)
	seedOrphan(t, fx, "lost comment")
	m := openFixtureModel(t, fx)

	m = press(m, "m")
	if m.mode != modeRelocate {
		t.Fatal("must enter relocate mode")
	}
	m.cursor = 0
	m.rerender()

	m = press(m, "j")

	if m.mode != modeRelocate {
		t.Fatal("free navigation must not exit relocate mode")
	}
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 after j", m.cursor)
	}
	if !strings.Contains(m.View().Content, "relocate ·") {
		t.Fatal("panel must remain open during free navigation")
	}
}

// TestRelocateEscExits pins esc's return-to-read contract, including the
// same deferred-refresh consumption as compose's esc (mirrors
// TestStateChangeDeferredDuringCompose).
func TestRelocateEscExits(t *testing.T) {
	fx := setup(t)
	seedOrphan(t, fx, "lost comment")
	m := openFixtureModel(t, fx)

	m = press(m, "m")
	if m.mode != modeRelocate {
		t.Fatal("must enter relocate mode")
	}

	addThread(t, secondFixtureStore(t, fx), "written during relocate")

	cur, cmd := m.Update(msgStateChanged{})
	m = cur.(*Model)
	_ = cmd // nil here: m.stateChanged is nil in this test, same as any other disabled-watch Update

	if m.mode != modeRelocate {
		t.Fatal("a deferred state change must not leave relocate mode")
	}
	if !m.pendingRefresh {
		t.Fatal("state change during relocate must set pendingRefresh")
	}
	if strings.Contains(m.View().Content, "written during relocate") {
		t.Fatal("refresh must be deferred, not applied immediately")
	}

	m = press(m, "esc")
	if m.mode != modeRead {
		t.Fatalf("mode = %v, want modeRead after esc", m.mode)
	}
	if m.pendingRefresh {
		t.Fatal("esc must consume the pending refresh")
	}
	m = press(m, "n", "enter") // jump to the thread's block and expand it
	if !strings.Contains(m.View().Content, "written during relocate") {
		t.Fatal("esc must apply the deferred refresh")
	}
}

// TestViewHeightAccountsForRelocatePanel pins the DYNAMIC panel-height
// reservation: viewHeight in relocate mode is exactly read mode's height
// minus relocatePanelHeight() — the panel's own rendered line count (header
// + comment body + hint), never a fixed constant.
func TestViewHeightAccountsForRelocatePanel(t *testing.T) {
	fx := setup(t)
	seedOrphan(t, fx, "lost comment")
	m := openFixtureModel(t, fx)
	readVH := m.viewHeight()

	m = press(m, "m")
	if m.mode != modeRelocate {
		t.Fatal("must enter relocate mode")
	}
	if got, want := m.viewHeight(), readVH-m.relocatePanelHeight(); got != want {
		t.Fatalf("viewHeight = %d, want %d (read mode height - relocatePanelHeight)", got, want)
	}
}

// TestRelocatePanelShowsFullComment pins the rule: the panel shows the
// orphan's FULL first comment, not a short excerpt — a sentence from the MIDDLE
// of a long body must appear, rendered as a thread card with a "| author"
// header line alongside it. A thread with replies shows each reply's author
// natively inside the card instead of a "+N replies" marker.
func TestRelocatePanelShowsFullComment(t *testing.T) {
	fx := setup(t)
	middle := "the load shedding heuristic silently drops the newest requests instead of the oldest ones"
	// Short enough that this comment plus both replies below (each carrying
	// its own header line) still fit inside relocateBodyMaxLines, while still
	// spanning two wrapped lines so the excerpt-vs-full-body distinction below
	// is real.
	body := "This orphaned thread opens with context before the point: " + middle + "."
	seedOrphanSpan(t, fx, "token bucket refill configuration", body)
	m := openFixtureModel(t, fx)

	m = press(m, "m")
	if m.mode != modeRelocate {
		t.Fatal("must enter relocate mode")
	}
	// Each wrapped continuation line carries its own leading indent (part of
	// the card's visual language), so a naive space-join of the raw lines does
	// not reconstruct the original single-spaced prose at a wrap boundary.
	// Stripping styling and collapsing runs of whitespace checks the SEMANTIC
	// content survived intact, without caring where the renderer wrapped it.
	normalize := func(lines []string) string {
		return strings.Join(strings.Fields(ansi.Strip(strings.Join(lines, " "))), " ")
	}
	author := m.orphans[0].Thread.Comments[0].Attribution.ActorDisplay
	joined := normalize(m.relocateBodyLines())
	if !strings.Contains(joined, "| "+author) {
		t.Fatalf("panel body = %q, want a thread-card header line (\"| %s\")", joined, author)
	}
	if !strings.Contains(joined, middle) {
		t.Fatalf("panel body = %q, want it to contain the middle sentence %q", joined, middle)
	}

	orphan, ok := m.currentOrphan()
	if !ok {
		t.Fatal("fixture assumption broken: no current orphan")
	}
	s, err := session.Open(fx.ctx, fx.svc, fx.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reply(fx.ctx, orphan.Thread.ID, "reply one"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reply(fx.ctx, orphan.Thread.ID, "reply two"); err != nil {
		t.Fatal(err)
	}
	if err := m.RefreshFromSession(fx.ctx); err != nil {
		t.Fatal(err)
	}
	joined = normalize(m.relocateBodyLines())
	if strings.Contains(joined, "+2 replies") {
		t.Fatalf("panel body = %q, want no \"+N replies\" marker — replies render natively", joined)
	}
	if !strings.Contains(joined, "reply one") || !strings.Contains(joined, "reply two") {
		t.Fatalf("panel body = %q, want both replies' authors/bodies visible in the card", joined)
	}
}

// TestRelocatePanelCapsAtEightLines pins the 8-line cap: a very long body
// renders exactly relocateBodyMaxLines body lines plus a trailing "…"
// truncation marker, and the full View() line count equals m.height exactly,
// with no drift between relocatePanelHeight's reservation and panelView's
// output.
func TestRelocatePanelCapsAtEightLines(t *testing.T) {
	fx := setup(t)
	body := strings.Repeat("this orphaned comment keeps going and going ", 40)
	seedOrphanSpan(t, fx, "token bucket refill configuration", body)
	m := openFixtureModel(t, fx)

	m = press(m, "m")
	if m.mode != modeRelocate {
		t.Fatal("must enter relocate mode")
	}

	lines := m.relocateBodyLines()
	if len(lines) != relocateBodyMaxLines+1 {
		t.Fatalf("body lines = %d, want %d (%d body + 1 truncation marker)", len(lines), relocateBodyMaxLines+1, relocateBodyMaxLines)
	}
	if lines[relocateBodyMaxLines] != "…" {
		t.Fatalf("last line = %q, want the truncation marker", lines[relocateBodyMaxLines])
	}

	total := len(strings.Split(m.View().Content, "\n"))
	if total != m.height {
		t.Fatalf("rendered view has %d lines, want m.height = %d", total, m.height)
	}
}

// TestRelocatePanelShrinksBeforeViewportFloors pins the squeeze order on
// short terminals: the panel's body section yields lines to keep the
// document viewport at minRelocateViewport — viewHeight must never be
// driven to its floor of 1 by the panel alone, and the rendered view still
// fits the terminal exactly.
func TestRelocatePanelShrinksBeforeViewportFloors(t *testing.T) {
	fx := setup(t)
	body := strings.Repeat("this orphaned comment keeps going and going ", 40)
	seedOrphanSpan(t, fx, "token bucket refill configuration", body)
	m := openFixtureModel(t, fx)

	for _, height := range []int{12, 8, 6} {
		m.height = height
		m = press(m, "m")
		if m.mode != modeRelocate {
			t.Fatal("must enter relocate mode")
		}
		// -5, not -4: status, help, the panel's own header and hint, and the
		// row of ground above the document, which viewHeight counts.
		wantVH := height - 5 - len(m.relocateBodyLines())
		if wantVH < 1 {
			wantVH = 1
		}
		if got := m.viewHeight(); got != wantVH {
			t.Fatalf("height %d: viewHeight = %d, want %d", height, got, wantVH)
		}
		if height >= 5+minRelocateViewport && m.viewHeight() < minRelocateViewport {
			t.Fatalf("height %d: viewHeight = %d, want >= %d — the panel body must shrink first", height, m.viewHeight(), minRelocateViewport)
		}
		if total := len(strings.Split(m.View().Content, "\n")); total > height {
			t.Fatalf("height %d: rendered view has %d lines, must fit the terminal", height, total)
		}
		m = press(m, "esc")
	}
}

// deepHeadingFixture builds a fixture whose deepest block sits under four
// nested, verbosely-titled headings — so its joined HeadingPath is long — while
// the deepest heading's own title ("Token Bucket Algorithm Overview", 31 runes)
// is still short relative to the joined path, letting a clipped header keep the
// leaf intact while trimming the ancestors.
func deepHeadingFixture(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	svc := newFixtureStore(t, filepath.Join(dir, "state.json"))
	doc := "# Top Level Section With A Very Long And Verbose Title Indeed\n\n" +
		"## Second Level Section With An Equally Extensive And Wordy Title\n\n" +
		"### Third Level Section That Continues At Great And Unnecessary Length\n\n" +
		"#### Token Bucket Algorithm Overview\n\n" +
		"Some content in the deepest section so the block list is non-empty.\n"
	path := filepath.Join(dir, "deep.md")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	return fixture{svc: svc, cas: localcas.New(filepath.Join(dir, "objects")), path: path, ctx: attrCtx("alice")}
}

// seedSyntheticOrphan installs a single synthetic orphan (no real reanchor
// run) directly on m and drives the real enterRelocate entry path — used by
// the panel-geometry test below, which cares about the panel's width
// handling, not about producing a realistic orphan. comments[0] is the
// thread's opening comment; any further strings become replies, so a
// caller can exercise a card with both a long body and a long reply.
func seedSyntheticOrphan(m *Model, comments ...string) {
	cs := make([]domain.Comment, len(comments))
	for i, body := range comments {
		cs[i] = domain.Comment{Attribution: domain.Attribution{ActorLogin: "alice-codes", ActorDisplay: "alice"}, Body: body}
	}
	m.orphans = []placement.Placement{{
		Thread: domain.Thread{
			ID:       domain.ThreadID("synthetic"),
			Comments: cs,
		},
		Status: reanchor.StatusOrphaned,
	}}
	m.enterRelocate()
}

// TestRelocatePanelNeverWraps pins the multi-line panel's width contract: every
// rendered panel line — header, wrapped comment body, hint — must fit within
// m.width however long the first comment runs, or relocatePanelHeight()'s
// reservation would disagree with what actually renders and push the
// status/help lines off-screen. Run across widths down to 20, below the
// unclipped-header failure mode that only bit under ~23 columns.
func TestRelocatePanelNeverWraps(t *testing.T) {
	for _, width := range []int{80, 40, 24, 20} {
		t.Run(fmt.Sprintf("width=%d", width), func(t *testing.T) {
			fx := deepHeadingFixture(t)
			m := openModel(t, fx)
			m.width, m.height = width, 30
			seedSyntheticOrphan(m, strings.Repeat("this orphaned comment keeps going and going ", 6))
			m.cursor = len(m.blocks) - 1 // the deepest block, under the long joined path
			m.rerender()

			// Only the panel's own rows are this contract; the help line is
			// unrelated to relocate.
			for i, line := range strings.Split(m.panelView(), "\n") {
				if n := len([]rune(ansi.Strip(line))); n > m.width {
					t.Fatalf("panel line %d exceeds width %d (%d runes): %q", i, m.width, n, line)
				}
			}
			relocateLines := strings.Count(m.View().Content, "\n")

			m.mode = modeRead
			m.rerender()
			readLines := strings.Count(m.View().Content, "\n")
			m.mode = modeRelocate
			m.rerender()

			if relocateLines != readLines {
				t.Fatalf("relocate view has %d lines, read view has %d at width %d — the panel must not push status/help off-screen", relocateLines, readLines, width)
			}
		})
	}
}

// TestRelocatePanelPaintedNeverWraps is TestRelocatePanelNeverWraps' sibling,
// separated from it by its FIXTURE rather than by a code path -- panelView is a
// one-line delegate to panelViewPainted: this one seeds an orphan carrying a
// long REPLY as well as a long body, and calls panelViewPainted by name.
//
// The property either way: panelViewPainted routes each line through
// st.FocusHeader/Chrome/Strip.Width(m.width).Render, which word-wraps (inserts
// real newlines) instead of truncating anything handed to it wider than its own
// width. relocateHeaderLine is asserted directly here alongside the same
// rune-length and read/relocate line-count parity checks the test above makes.
func TestRelocatePanelPaintedNeverWraps(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{80, 40, 24, 20} {
		t.Run(fmt.Sprintf("width=%d", width), func(t *testing.T) {
			f := setup(t)
			s, err := session.Open(f.ctx, f.svc, f.path)
			if err != nil {
				t.Fatal(err)
			}
			m := New(s, keymap.Default(), th, "", nil)
			if err := m.RefreshFromSession(f.ctx); err != nil {
				t.Fatal(err)
			}
			m.width, m.height = width, 30
			m.rerender()
			seedSyntheticOrphan(m,
				strings.Repeat("this orphaned comment keeps going and going ", 6),
				strings.Repeat("and this reply keeps right on going too ", 6))
			m.cursor = len(m.blocks) - 1
			m.rerender()

			panel, _ := m.panelViewPainted()
			for i, line := range strings.Split(panel, "\n") {
				if n := len([]rune(ansi.Strip(line))); n > m.width {
					t.Fatalf("painted panel line %d exceeds width %d (%d runes): %q", i, m.width, n, ansi.Strip(line))
				}
			}
			relocateLines := strings.Count(m.View().Content, "\n")

			m.mode = modeRead
			m.rerender()
			readLines := strings.Count(m.View().Content, "\n")
			m.mode = modeRelocate
			m.rerender()

			if relocateLines != readLines {
				t.Fatalf("painted relocate view has %d lines, read view has %d at width %d — the panel must not push status/help off-screen", relocateLines, readLines, width)
			}
		})
	}
}

// TestRelocatePanelPaintedCardZoneDistinctFromChrome pins the visual fix: the
// relocate panel's comment gutter must render in the Card zone, not the
// surrounding Chrome zone. The NeverWraps tests above only
// check line count/width and would stay green even if panelViewPainted's
// st.Card.Width(...) wrap were reverted back to st.Chrome — this asserts the
// row's actual background zone directly.
func TestRelocatePanelPaintedCardZoneDistinctFromChrome(t *testing.T) {
	for _, name := range []string{"dark", "light"} {
		t.Run(name, func(t *testing.T) {
			th, err := theme.Lookup(name)
			if err != nil {
				t.Fatal(err)
			}
			st := ui.NewStyles(th)
			cardBG := paintedBG(st.Card.Render("x"))
			chromeBG := paintedBG(st.Chrome.Render("x"))
			if cardBG == "" || chromeBG == "" || cardBG == chromeBG {
				t.Fatalf("%s: Card bg = %q, Chrome bg = %q, want distinct non-empty backgrounds", name, cardBG, chromeBG)
			}

			f := setup(t)
			s, err := session.Open(f.ctx, f.svc, f.path)
			if err != nil {
				t.Fatal(err)
			}
			m := New(s, keymap.Default(), th, "", nil)
			if err := m.RefreshFromSession(f.ctx); err != nil {
				t.Fatal(err)
			}
			m.width, m.height = 80, 30
			m.rerender()
			seedSyntheticOrphan(m, "a short orphaned comment")
			m.rerender()

			panel, _ := m.panelViewPainted()
			rows := strings.Split(panel, "\n")
			if len(rows) < 2 {
				t.Fatalf("panel has %d rows, want at least a header row and a card row", len(rows))
			}
			body := rows[1] // rows[0] is the header; the card's own header line follows
			if !strings.Contains(body, cardBG) {
				t.Fatalf("%s: relocate card row = %q, want it painted in the Card zone (%s)", name, body, cardBG)
			}
			if strings.Contains(body, chromeBG) {
				t.Fatalf("%s: relocate card row = %q, want it NOT painted in the Chrome zone (%s) — that's the bug this change fixed", name, body, chromeBG)
			}
		})
	}
}

// orphanByBody finds the seeded orphan whose first comment has the given
// body — the identification strategy every multi-orphan test below uses
// instead of assuming a queue index, since queue order is projection order
// (session placement order), not seed order pinned by the test itself.
func orphanByBody(m *Model, body string) (placement.Placement, bool) {
	for _, p := range m.orphans {
		if len(p.Thread.Comments) > 0 && p.Thread.Comments[0].Body == body {
			return p, true
		}
	}
	return placement.Placement{}, false
}

// threadByBody is orphanByBody's service-side counterpart, for assertions
// against the durable store rather than the model's projection.
func threadByBody(threads []domain.Thread, body string) (domain.Thread, bool) {
	for _, th := range threads {
		if len(th.Comments) > 0 && th.Comments[0].Body == body {
			return th, true
		}
	}
	return domain.Thread{}, false
}

// TestRelocateEnterRehomesToCursorBlock pins the core write: entering relocate,
// moving the cursor to a known prose block and pressing enter rehomes the
// orphan there through session.AnchorForBlockText — the same constructor
// compose uses for a non-heading target. The service-side assertion fetches the
// thread through the fixture's own store, not just the model's projection.
func TestRelocateEnterRehomesToCursorBlock(t *testing.T) {
	fx := setup(t)
	seedOrphan(t, fx, "lost comment")
	m := openFixtureModel(t, fx)

	m = press(m, "m")
	if m.mode != modeRelocate {
		t.Fatal("must enter relocate mode")
	}

	target := -1
	for i, b := range m.blocks {
		if b.Kind != ui.KindHeading && strings.Contains(b.Text, "token bucket") {
			target = i
		}
	}
	if target < 0 {
		t.Fatal("fixture assumption broken: no prose block contains 'token bucket'")
	}
	m.cursor = target
	m.rerender()
	block := m.blocks[target]
	wantAnchor, err := session.AnchorForBlockText(string(m.sess.Content), ui.BlockAnchorSpan(block), block.HeadingPath)
	if err != nil {
		t.Fatal(err)
	}

	cur, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drain(t, cur.(*Model), cmd)

	if m.mode != modeRead {
		t.Fatalf("mode = %v, want modeRead — this fixture's only orphan was just handled", m.mode)
	}
	if m.status != "thread relocated" {
		t.Fatalf("status = %q, want the write's own status, no completion banner", m.status)
	}
	for _, vs := range m.unanchored {
		if vs.Thread.Comments[0].Body == "lost comment" {
			t.Fatal("relocated thread must no longer be unanchored")
		}
	}
	found := false
	for _, vs := range m.views[target] {
		if vs.Thread.Comments[0].Body == "lost comment" {
			found = true
		}
	}
	if !found {
		t.Fatal("relocated thread must appear in m.views at the target block")
	}

	threads, err := fx.svc.Threads(fx.ctx, m.sess.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := threadByBody(threads, "lost comment")
	if !ok {
		t.Fatal("service must still have the thread")
	}
	if !reflect.DeepEqual(got.Anchor, wantAnchor) {
		t.Fatalf("service anchor = %+v, want %+v", got.Anchor, wantAnchor)
	}
}

// TestRelocateOnHeadingMakesSectionAnchor pins the heading-cursor branch:
// enter on a KindHeading block makes a section anchor (empty Span, the
// heading's own path) via session.SectionAnchor — compose parity with its
// own heading-cursor comment path.
func TestRelocateOnHeadingMakesSectionAnchor(t *testing.T) {
	fx := setup(t)
	seedOrphan(t, fx, "lost comment")
	m := openFixtureModel(t, fx)

	m = press(m, "m")
	if m.mode != modeRelocate {
		t.Fatal("must enter relocate mode")
	}

	target := -1
	for i, b := range m.blocks {
		if b.Kind == ui.KindHeading {
			target = i
		}
	}
	if target < 0 {
		t.Fatal("fixture assumption broken: no heading block")
	}
	m.cursor = target
	m.rerender()
	wantPath := m.blocks[target].HeadingPath

	cur, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drain(t, cur.(*Model), cmd)

	threads, err := fx.svc.Threads(fx.ctx, m.sess.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := threadByBody(threads, "lost comment")
	if !ok {
		t.Fatal("service must still have the thread")
	}
	if got.Anchor.Span != "" {
		t.Fatalf("anchor span = %q, want empty for a section anchor", got.Anchor.Span)
	}
	if !reflect.DeepEqual(got.Anchor.HeadingPath, wantPath) {
		t.Fatalf("anchor heading path = %v, want %v", got.Anchor.HeadingPath, wantPath)
	}
}

// TestRelocateResolveAdvancesQueue pins R's write and auto-advance: two
// seeded orphans, resolving the current one (queue head) advances to the
// second orphan's ID while staying in relocate mode.
func TestRelocateResolveAdvancesQueue(t *testing.T) {
	fx := setup(t)
	seedOrphanSpan(t, fx, "token bucket refill configuration", "orphan A")
	seedOrphanSpan(t, fx, "unbounded requests overwhelming the database", "orphan B")
	m := openFixtureModel(t, fx)

	m = press(m, "m")
	if m.mode != modeRelocate {
		t.Fatal("must enter relocate mode")
	}
	first, ok := orphanByBody(m, "orphan A")
	if !ok || m.relocateTID != first.Thread.ID {
		t.Fatalf("fixture assumption broken: relocate must enter on orphan A's ID, got relocateTID=%v", m.relocateTID)
	}

	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	m = drain(t, cur.(*Model), cmd)

	if m.mode != modeRelocate {
		t.Fatalf("mode = %v, want to stay in relocate — a second orphan remains", m.mode)
	}
	second, ok := orphanByBody(m, "orphan B")
	if !ok {
		t.Fatal("orphan B must still be present")
	}
	if m.relocateTID != second.Thread.ID {
		t.Fatalf("relocateTID = %v, want orphan B's ID %v", m.relocateTID, second.Thread.ID)
	}

	threads, err := fx.svc.Threads(fx.ctx, m.sess.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotA, ok := threadByBody(threads, "orphan A")
	if !ok || !gotA.Resolved {
		t.Fatalf("orphan A resolved = %v, %v, want resolved", ok, gotA.Resolved)
	}
}

// TestRelocateLastActionExitsWithStatus pins the empty-queue exit: with only
// one orphan, resolving it drains the queue and exits to read mode with a
// completion status.
func TestRelocateLastActionExitsWithStatus(t *testing.T) {
	fx := setup(t)
	seedOrphan(t, fx, "lost comment")
	m := openFixtureModel(t, fx)

	m = press(m, "m")
	if m.mode != modeRelocate {
		t.Fatal("must enter relocate mode")
	}

	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	m = drain(t, cur.(*Model), cmd)

	if m.mode != modeRead {
		t.Fatalf("mode = %v, want modeRead once the queue drains", m.mode)
	}
	if m.status != "thread resolved" {
		t.Fatalf("status = %q, want the write's own status, no completion banner", m.status)
	}
}

// TestRelocateTabSkips pins tab's no-write skip: it advances relocateTID to
// the next unresolved orphan without dispatching anything (service state
// unchanged), and wraps back to the first orphan on a second tab.
func TestRelocateTabSkips(t *testing.T) {
	fx := setup(t)
	seedOrphanSpan(t, fx, "token bucket refill configuration", "orphan A")
	seedOrphanSpan(t, fx, "unbounded requests overwhelming the database", "orphan B")
	m := openFixtureModel(t, fx)

	m = press(m, "m")
	if m.mode != modeRelocate {
		t.Fatal("must enter relocate mode")
	}
	first, ok := orphanByBody(m, "orphan A")
	if !ok || m.relocateTID != first.Thread.ID {
		t.Fatalf("fixture assumption broken: relocate must enter on orphan A's ID")
	}
	planID := m.sess.Plan.ID
	beforeThreads, err := fx.svc.Threads(fx.ctx, planID)
	if err != nil {
		t.Fatal(err)
	}

	cur, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = cur.(*Model)
	if cmd != nil {
		t.Fatal("tab must not dispatch a write")
	}
	second, ok := orphanByBody(m, "orphan B")
	if !ok || m.relocateTID != second.Thread.ID {
		t.Fatalf("relocateTID = %v, want orphan B's ID after tab", m.relocateTID)
	}
	afterThreads, err := fx.svc.Threads(fx.ctx, planID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeThreads, afterThreads) {
		t.Fatal("tab must not change service state")
	}

	cur, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = cur.(*Model)
	if cmd != nil {
		t.Fatal("tab must not dispatch a write")
	}
	if m.relocateTID != first.Thread.ID {
		t.Fatalf("relocateTID = %v, want wrap back to orphan A's ID", m.relocateTID)
	}
}

// TestRelocateAdvanceMovesForward pins the forward-moving advance amendment:
// three orphans A, B, C in queue order; skipping A to land on B and then
// handling B (a write) advances to C — B's successor — never back to the
// skipped A. A comes around only at the wrap.
func TestRelocateAdvanceMovesForward(t *testing.T) {
	fx := setup(t)
	seedOrphanSpan(t, fx, "token bucket refill configuration", "orphan A")
	seedOrphanSpan(t, fx, "unbounded requests overwhelming the database", "orphan B")
	seedOrphanSpan(t, fx, "rate limiting a burst of concurrent client requests", "orphan C")
	m := openFixtureModel(t, fx)

	m = press(m, "m")
	a, ok := orphanByBody(m, "orphan A")
	if !ok || m.relocateTID != a.Thread.ID {
		t.Fatalf("fixture assumption broken: relocate must enter on orphan A's ID")
	}

	cur, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyTab}) // skip A -> B
	m = cur.(*Model)
	if cmd != nil {
		t.Fatal("tab must not dispatch a write")
	}
	b, ok := orphanByBody(m, "orphan B")
	if !ok || m.relocateTID != b.Thread.ID {
		t.Fatalf("relocateTID = %v, want orphan B's ID after tab", m.relocateTID)
	}

	cur, cmd = m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"}) // resolve B
	m = drain(t, cur.(*Model), cmd)

	c, ok := orphanByBody(m, "orphan C")
	if !ok {
		t.Fatal("orphan C must still be present")
	}
	if m.relocateTID != c.Thread.ID {
		t.Fatalf("relocateTID = %v, want orphan C's ID (B's successor) — not back to skipped A", m.relocateTID)
	}
	if aStill, ok := orphanByBody(m, "orphan A"); !ok || m.relocateTID == aStill.Thread.ID {
		t.Fatal("advance must not land back on the skipped orphan A immediately")
	}
}

// TestRelocateRefusalIsNonLossy pins the refusal path: a relocate enter while an
// earlier write is still held in flight must refuse without touching the queue
// or mode — no data loss, no accidental advance.
func TestRelocateRefusalIsNonLossy(t *testing.T) {
	fx := setup(t)
	seedOrphan(t, fx, "lost comment")
	m := openFixtureModel(t, fx)

	// Dispatch a reload but hold its cmd un-delivered: inFlight stays true.
	cur, held := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = cur.(*Model)
	if !m.inFlight {
		t.Fatal("reload dispatch must claim inFlight")
	}

	// Entering relocate is read-only (like opening compose) and stays live
	// while a write runs.
	m = press(m, "m")
	if m.mode != modeRelocate {
		t.Fatalf("mode = %v, want modeRelocate while a write is in flight", m.mode)
	}
	orphan, ok := orphanByBody(m, "lost comment")
	if !ok {
		t.Fatal("fixture assumption broken")
	}
	wantTID := m.relocateTID
	if wantTID != orphan.Thread.ID {
		t.Fatal("fixture assumption broken")
	}

	cur, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = cur.(*Model)
	if cmd != nil {
		t.Fatal("relocate enter must not dispatch while another write is in flight")
	}
	if m.mode != modeRelocate {
		t.Fatal("refused relocate must stay in relocate mode")
	}
	if m.relocateTID != wantTID {
		t.Fatalf("relocateTID = %v, want unchanged %v", m.relocateTID, wantTID)
	}
	if !strings.Contains(m.status, "action in progress") {
		t.Fatalf("status = %q, want an in-progress hint", m.status)
	}

	m = drain(t, m, held)
	if m.inFlight {
		t.Fatal("inFlight must clear once the reload's result lands")
	}
	q := m.unresolvedOrphans()
	if len(q) != 1 || q[0].Thread.ID != wantTID {
		t.Fatalf("queue = %+v, want the single orphan intact after the refused write", q)
	}
}

// TestRelocateFullJourney exercises the whole flow end to end: relocating
// one orphan right where entry left the cursor, one manually after a skip,
// and resolving the last — landing in read mode with zero unresolved
// orphans and the right service-side fact for each.
func TestRelocateFullJourney(t *testing.T) {
	fx := setup(t)
	seedOrphanSpan(t, fx, "token bucket refill configuration", "orphan A")
	seedOrphanSpan(t, fx, "unbounded requests overwhelming the database", "orphan B")
	seedOrphanSpan(t, fx, "rate limiting a burst of concurrent client requests", "orphan C")
	m := openFixtureModel(t, fx)

	m = press(m, "m")
	a, ok := orphanByBody(m, "orphan A")
	if !ok || m.relocateTID != a.Thread.ID {
		t.Fatal("fixture assumption broken: relocate must enter on orphan A's ID")
	}

	// Relocate A at the entry cursor: enter immediately, no navigation.
	cur, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drain(t, cur.(*Model), cmd)
	if m.mode != modeRelocate {
		t.Fatalf("mode = %v, want to stay in relocate — two orphans remain", m.mode)
	}

	// Skip whichever orphan auto-advance landed on, then place the other
	// manually by moving the cursor before pressing enter.
	skipped := m.relocateTID
	cur, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = cur.(*Model)
	if cmd != nil {
		t.Fatal("tab must not dispatch a write")
	}
	if m.relocateTID == skipped {
		t.Fatal("tab must move off the skipped orphan")
	}
	manualOrphan, ok := m.currentOrphan()
	if !ok {
		t.Fatal("must be targeting an orphan after tab")
	}
	manualBody := manualOrphan.Thread.Comments[0].Body
	m = press(m, "j", "j")
	manualBlock := m.blocks[m.cursor]
	var wantManualAnchor reanchor.Anchor
	var err error
	if manualBlock.Kind == ui.KindHeading {
		wantManualAnchor, err = session.SectionAnchor(string(m.sess.Content), manualBlock.HeadingPath)
	} else {
		wantManualAnchor, err = session.AnchorForBlockText(string(m.sess.Content), ui.BlockAnchorSpan(manualBlock), manualBlock.HeadingPath)
	}
	if err != nil {
		t.Fatal(err)
	}
	cur, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drain(t, cur.(*Model), cmd)
	if m.mode != modeRelocate {
		t.Fatalf("mode = %v, want to stay in relocate — one orphan remains", m.mode)
	}

	// Resolve the last remaining orphan.
	cur, cmd = m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	m = drain(t, cur.(*Model), cmd)

	if m.mode != modeRead {
		t.Fatalf("mode = %v, want modeRead once triage completes", m.mode)
	}
	if len(m.unresolvedOrphans()) != 0 {
		t.Fatalf("unresolvedOrphans = %d, want 0", len(m.unresolvedOrphans()))
	}

	threads, err := fx.svc.Threads(fx.ctx, m.sess.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	var rehomed, resolved int
	for _, body := range []string{"orphan A", "orphan B", "orphan C"} {
		th, ok := threadByBody(threads, body)
		if !ok {
			t.Fatalf("thread %q missing from service", body)
		}
		if th.Resolved {
			resolved++
			continue
		}
		if th.Anchor.HeadingPath[0] == "nonexistent" {
			t.Fatalf("thread %q was neither rehomed nor resolved: anchor %+v", body, th.Anchor)
		}
		rehomed++
	}
	if rehomed != 2 || resolved != 1 {
		t.Fatalf("rehomed = %d resolved = %d, want 2 and 1", rehomed, resolved)
	}

	manualThread, ok := threadByBody(threads, manualBody)
	if !ok {
		t.Fatalf("manually placed orphan %q missing from service", manualBody)
	}
	if !reflect.DeepEqual(manualThread.Anchor, wantManualAnchor) {
		t.Fatalf("manual placement anchor = %+v, want %+v", manualThread.Anchor, wantManualAnchor)
	}
}

// TestRelocateEscDuringInFlightWriteLandsWithoutAdvance pins the interaction
// between esc's immediate exitRelocate and a relocate write already in flight:
// handleActionDone's auto-advance step is gated on m.mode == modeRelocate, so an
// esc that flips to modeRead before the write's result lands must still land the
// write's projection (refresh, its own status) without re-entering relocate mode
// or retargeting relocateTID.
func TestRelocateEscDuringInFlightWriteLandsWithoutAdvance(t *testing.T) {
	fx := setup(t)
	seedOrphan(t, fx, "lost comment")
	m := openFixtureModel(t, fx)

	m = press(m, "m")
	orphan, ok := orphanByBody(m, "lost comment")
	if !ok {
		t.Fatal("fixture assumption broken")
	}
	wantTID := m.relocateTID
	if wantTID != orphan.Thread.ID {
		t.Fatal("fixture assumption broken: relocate must enter on the seeded orphan")
	}
	target := m.cursor

	// Dispatch the relocate write but hold its cmd un-delivered: inFlight
	// stays true while esc runs.
	cur, held := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = cur.(*Model)
	if !m.inFlight {
		t.Fatal("relocate dispatch must claim inFlight")
	}

	m = press(m, "esc")
	if m.mode != modeRead {
		t.Fatalf("mode = %v, want modeRead immediately after esc", m.mode)
	}

	m = drain(t, m, held)

	if m.mode != modeRead {
		t.Fatalf("mode = %v, want modeRead — the write landing after esc must not re-enter relocate", m.mode)
	}
	if m.relocateTID != wantTID {
		t.Fatalf("relocateTID = %v, want unchanged %v — no advance to a successor", m.relocateTID, wantTID)
	}
	if !strings.Contains(m.status, "thread relocated") {
		t.Fatalf("status = %q, want the write's own message", m.status)
	}

	threads, err := fx.svc.Threads(fx.ctx, m.sess.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	th, ok := threadByBody(threads, "lost comment")
	if !ok {
		t.Fatal("thread missing from service")
	}
	if th.Anchor.HeadingPath[0] == "nonexistent" {
		t.Fatalf("thread was not relocated: anchor %+v", th.Anchor)
	}
	if len(m.unresolvedOrphans()) != 0 {
		t.Fatalf("unresolvedOrphans = %d, want 0 — the placement must be reflected in the view", len(m.unresolvedOrphans()))
	}
	found := false
	for _, vs := range m.views[target] {
		if len(vs.Thread.Comments) > 0 && vs.Thread.Comments[0].Body == "lost comment" {
			found = true
		}
	}
	if !found {
		t.Fatal("relocated thread must appear in the target block's view")
	}
}

// TestCtrlCAlwaysQuitsEveryMode pins the rule: ctrl+c is the
// unconditional kill switch, intercepted at the top of Model.Update before any
// mode dispatch. Every mode must yield tea.QuitMsg, including compose with an
// unsaved draft (the draft must never reach the textarea).
// TestEmbeddedCtrlCAlwaysQuits (root_test.go) covers the embedded-via-Root case.
func TestCtrlCAlwaysQuitsEveryMode(t *testing.T) {
	tests := []struct {
		name  string
		build func(t *testing.T) *Model
	}{
		{
			name: "read",
			build: func(t *testing.T) *Model {
				return openModel(t, setup(t))
			},
		},
		{
			name: "compose with typed draft",
			build: func(t *testing.T) *Model {
				m := openModel(t, setup(t))
				m = press(m, "j", "j", "c", "h", "i")
				if m.mode != modeCompose {
					t.Fatalf("test setup: mode = %v, want modeCompose", m.mode)
				}
				return m
			},
		},
		{
			name: "confirm-approve",
			build: func(t *testing.T) *Model {
				m := press(openModel(t, setup(t)), "a")
				if m.mode != modeConfirmApprove {
					t.Fatalf("test setup: mode = %v, want modeConfirmApprove", m.mode)
				}
				return m
			},
		},
		{
			name: "relocate",
			build: func(t *testing.T) *Model {
				f := setup(t)
				m := openModel(t, f)
				seedOrphan(t, f, "lost comment")
				if err := m.RefreshFromSession(f.ctx); err != nil {
					t.Fatal(err)
				}
				m = press(m, "m")
				if m.mode != modeRelocate {
					t.Fatalf("test setup: mode = %v, want modeRelocate", m.mode)
				}
				return m
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.build(t)
			_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
			if cmd == nil {
				t.Fatal("ctrl+c must return a cmd")
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatal("ctrl+c must produce tea.QuitMsg")
			}
		})
	}
}

// TestActListReadModeEmitsCloseReview pins ActList's read-mode dispatch: l
// hands control to the list via msgCloseReview, the same message quit() uses —
// see showList's doc comment for why it is unconditional rather than gated on
// embedded.
func TestActListReadModeEmitsCloseReview(t *testing.T) {
	m := openModel(t, setup(t))
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if cmd == nil {
		t.Fatal("l in read mode must return a cmd")
	}
	if _, ok := cmd().(msgCloseReview); !ok {
		t.Fatal("l in read mode must emit msgCloseReview")
	}
}

// TestActListRelocateModeEmitsCloseReview mirrors the read-mode case for
// modeRelocate, where ActList is dispatched from its own keymap switch
// beside ActQuit.
func TestActListRelocateModeEmitsCloseReview(t *testing.T) {
	f := setup(t)
	m := openModel(t, f)
	seedOrphan(t, f, "lost comment")
	if err := m.RefreshFromSession(f.ctx); err != nil {
		t.Fatal(err)
	}
	m = press(m, "m")
	if m.mode != modeRelocate {
		t.Fatalf("test setup: mode = %v, want modeRelocate", m.mode)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if cmd == nil {
		t.Fatal("l in relocate mode must return a cmd")
	}
	if _, ok := cmd().(msgCloseReview); !ok {
		t.Fatal("l in relocate mode must emit msgCloseReview")
	}
}

// TestActListComposeTypesIntoTextarea pins compose as a literal-key panel:
// l must land in the draft, never dispatch ActList.
func TestActListComposeTypesIntoTextarea(t *testing.T) {
	m := openModel(t, setup(t))
	m = press(m, "c", "l")
	if m.mode != modeCompose {
		t.Fatalf("test setup: mode = %v, want modeCompose", m.mode)
	}
	if m.ta.Value() != "l" {
		t.Fatalf("textarea value = %q, want the literal l typed, not dispatched as ActList", m.ta.Value())
	}
}

// TestActListConfirmApproveIsNoOp pins the other literal-key panel:
// confirm-approve only recognizes y/n/esc, so l must be a no-op.
func TestActListConfirmApproveIsNoOp(t *testing.T) {
	m := press(openModel(t, setup(t)), "a")
	if m.mode != modeConfirmApprove {
		t.Fatalf("test setup: mode = %v, want modeConfirmApprove", m.mode)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if cmd != nil {
		t.Fatal("l in confirm-approve must be a no-op: no cmd")
	}
	if m.mode != modeConfirmApprove {
		t.Fatalf("mode = %v, want to stay in modeConfirmApprove", m.mode)
	}
}

// TestShowListRefusedWhileInFlight: showList must go through the same
// dispatchOK discipline as quit(), refusing rather than racing a write already
// in progress.
func TestShowListRefusedWhileInFlight(t *testing.T) {
	m := openModel(t, setup(t))

	// Dispatch a reload but hold its cmd un-delivered: inFlight stays true.
	cur, held := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = cur.(*Model)
	if !m.inFlight {
		t.Fatal("reload dispatch must claim inFlight")
	}

	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	m = cur.(*Model)
	if cmd != nil {
		t.Fatal("l must refuse while another write is in flight")
	}
	if m.mode != modeRead {
		t.Fatal("refused l must stay in read mode")
	}
	if !strings.Contains(m.status, "action in progress") {
		t.Fatalf("status = %q, want an in-progress hint", m.status)
	}

	m = drain(t, m, held)
	if m.inFlight {
		t.Fatal("inFlight must clear once the reload's result lands")
	}
}

// TestMsgCloseReviewUnhandledOnBareModel pins the fall-through invariant
// showList's doc comment relies on: Model.Update has no case for
// msgCloseReview (Root's Update does), so delivering it directly to a
// standalone Model is harmless — no state change, no cmd.
func TestMsgCloseReviewUnhandledOnBareModel(t *testing.T) {
	m := openModel(t, setup(t))
	wantMode, wantStatus, wantCursor := m.mode, m.status, m.cursor

	cur, cmd := m.Update(msgCloseReview{})
	if cmd != nil {
		t.Fatal("msgCloseReview must be a no-op on a bare Model: no cmd")
	}
	got := cur.(*Model)
	if got.mode != wantMode || got.status != wantStatus || got.cursor != wantCursor {
		t.Fatalf("msgCloseReview mutated model state: mode=%v status=%q cursor=%d", got.mode, got.status, got.cursor)
	}
}

// TestHelpLineIncludesList pins the help bar's advertisement of the list verb.
func TestHelpLineIncludesList(t *testing.T) {
	km := keymap.Default()
	if got := helpLine(km, 1, searchState{}, ""); !strings.Contains(got, "l list") {
		t.Fatalf("help line must include the list segment, got %q", got)
	}
}

// TestHelpLineGrowsOnlyDeliberately bounds the unsearched line at exactly what
// it measures today, so a segment added to it is a decision somebody took
// rather than a drift nobody saw.
//
// THE BOUND IS NOT ABOUT WRAPPING: viewPainted renders this bar through
// ansi.Truncate(" "+m.helpBar(), barWidth, "") unconditionally, so an over-long
// line is cut, never folded, and TestReviewPaintedStatusHelpBarsNeverWrap is
// what actually holds the frame's row count. What this guards is the line's
// CONTENT BUDGET, and the failure it exists to catch is a segment arriving
// without anyone deciding what it costs the segments behind it. The number is
// the line's own measured length with no headroom -- headroom is what lets the
// next segment in unnoticed -- so re-derive it by reading the failure message.
//
// ITS SUBJECT IS THE UNSEARCHED LINE, which is why it passes searchState{}
// rather than a live search. The search clause carries the reader's OWN TEXT and
// no length is impossible, so no rune bound could hold over it -- the same thing
// ListModel.hintLine says about its filter query. What holds there instead is
// the clip, and the clip is what
// TestTheSearchClauseSurvivesTheClipThatEatsTheTailOfTheBar asserts against.
func TestHelpLineGrowsOnlyDeliberately(t *testing.T) {
	// 146 -> 155: "? keys · " leads the bar, a deliberate decision (the panel
	// this segment points at is the whole remedy for everything else on this
	// line being over budget already) -- re-derived by running the code, not
	// by arithmetic on the old number, exactly as this test's own doc comment
	// asks.
	const budget = 155
	got := helpLine(keymap.Default(), 1, searchState{}, "")
	if n := len([]rune(got)); n > budget {
		t.Fatalf("help line is %d runes, want <= %d -- a segment was added without deciding what it costs the ones behind it: %q", n, budget, got)
	}
}

// TestHelpLineNamesTheReloadGesture pins the clause once missing: ctrl+r
// re-reads the plan, and until now the bar never said so.
//
// THE GESTURE IS THE ONLY WAY THE DOCUMENT'S OWN BYTES CHANGE ON SCREEN, which
// is what makes its absence a defect rather than a thin bar. WatchState watches
// state.json and refreshes the REVIEW FACTS live, so a comment an agent writes
// appears unaided -- but an agent that rewrites the PLAN leaves the reader
// looking at the old text with nothing on screen naming the way forward. That
// is the scenario's last beat ("the user returns to the terminal ... and sees
// that the plan has changed"), and it is reached by this key alone.
//
// THE SECOND HALF IS WHY THIS IS NOT COVERED BY THE PINNED LINE IN
// search_test.go: that literal holds the ORDER and cannot tell a derived clause
// from a hardcoded "ctrl+r reload". A rebound reload has to move the bar with
// it, like every other clause but "shift select".
func TestHelpLineNamesTheReloadGesture(t *testing.T) {
	km := keymap.Default()
	if got := helpLine(km, 1, searchState{}, ""); !strings.Contains(got, "ctrl+r reload") {
		t.Fatalf("help line must name the reload gesture, got %q", got)
	}

	rebound := keymap.Map{}
	for k, v := range km {
		rebound[k] = v
	}
	delete(rebound, "ctrl+r")
	rebound["b"] = keymap.ActReload
	got := helpLine(rebound, 1, searchState{}, "")
	if !strings.Contains(got, "b reload") {
		t.Fatalf("help line must name the LIVE reload binding, got %q", got)
	}
	if strings.Contains(got, "ctrl+r") {
		t.Fatalf("help line names a binding that no longer exists, got %q", got)
	}
}

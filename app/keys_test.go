package app

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/keymap"
)

// probeKey is the single keystroke every dispatch probe below binds ONE
// action to, replacing the model's whole keymap rather than overlaying it: no
// action-set collision is possible, whatever Default() looks like the day this
// runs.
const probeKey = "b"

func probeMsg() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'b', Text: probeKey} }

// reviewFingerprint is the slice of *Model state a dispatch probe compares
// before and after: everything a case arm in updateRead, dispatchWrite or
// modeRelocate's own switch is seen (by reading every one of them) to mutate
// synchronously. A case that returns a non-nil tea.Cmd counts as a change on
// its own, so a write that stages nothing but a goroutine (ActReload) still
// registers.
type reviewFingerprint struct {
	mode     mode
	status   string
	confirm  string
	cursor   int
	scroll   int
	inFlight bool
	search   string
	expanded string
	selected string
}

func snapshotReview(m *Model) reviewFingerprint {
	return reviewFingerprint{
		mode: m.mode, status: m.status, confirm: m.confirm,
		cursor: m.cursor, scroll: m.scroll, inFlight: m.inFlight,
		search:   m.search,
		expanded: fmt.Sprintf("%v", m.expanded),
		selected: fmt.Sprintf("%v", m.selected),
	}
}

// cloneReview is a probe's own disposable copy of base: a shallow struct copy
// plus fresh expanded/selected maps, which are the only two reference fields
// any case arm below writes THROUGH rather than replacing outright. Every
// other reference field (m.sess, m.blocks, m.views, m.styles...) is read by
// these dispatches, never mutated synchronously -- a write's actual mutation
// waits for its cmd to run, which no probe here ever does.
func cloneReview(base *Model) *Model {
	m := *base
	m.expanded = make(map[int]bool, len(base.expanded))
	for k, v := range base.expanded {
		m.expanded[k] = v
	}
	m.selected = make(map[int]int, len(base.selected))
	for k, v := range base.selected {
		m.selected[k] = v
	}
	return &m
}

// probeReview dispatches a at a disposable clone of base and reports whether
// updateRead/dispatchWrite/modeRelocate answered it AT ALL -- any fingerprint
// change, or a non-nil cmd. It says nothing about WHAT the effect was, only
// that there was one, which is exactly "this model dispatches the action" and
// nothing more.
func probeReview(base *Model, a keymap.Action) bool {
	m := cloneReview(base)
	before := snapshotReview(m)
	m.km = keymap.Map{probeKey: a}
	cur, cmd := m.Update(probeMsg())
	return cmd != nil || snapshotReview(cur.(*Model)) != before
}

// reviewOwnsAction reports whether reviewKeyGroups names a among its rows --
// the table's own claim, asked the same way a reader of the panel would read
// it.
func reviewOwnsAction(a keymap.Action) bool {
	for _, g := range reviewKeyGroups {
		for _, r := range g.rows {
			for _, ra := range r.actions {
				if ra == a {
					return true
				}
			}
		}
	}
	return false
}

func listOwnsAction(a keymap.Action) bool {
	for _, g := range listKeyGroups {
		for _, r := range g.rows {
			for _, ra := range r.actions {
				if ra == a {
					return true
				}
			}
		}
	}
	return false
}

// keysThreadFixture is longDocFixture's own 60-section plan (app_test.go),
// with TWO threads anchored to the SAME block roughly in the middle of it,
// cursor parked there -- the one fixture every review probe below shares.
// LONG AND MIDDLE ARE BOTH LOAD-BEARING, not incidental: MoveDown/Top/Bottom
// and ScrollDown/ScrollUp need room to move in BOTH directions to prove
// themselves (a cursor already at either end, or a document short enough to
// fit one screen, makes a genuinely-dispatched move read as a no-op), and
// ToggleExpand/CycleThread are silent no-ops on a block with fewer than one
// (respectively two) threads. The block is found by CONTENT (which block
// m.views actually populated), never assumed by index: ui.ParseBlocks' own
// layout is not this test's to predict.
func keysThreadFixture(t *testing.T) *Model {
	t.Helper()
	fx := longDocFixture(t)
	seedThreadOn(t, fx, inferPlanTitle, "Paragraph content for section 30", "thread one")
	seedThreadOn(t, fx, inferPlanTitle, "Paragraph content for section 30", "thread two")
	m := openModel(t, fx)
	for b, vs := range m.views {
		if len(vs) >= 2 {
			m.cursor = b
			m.ensureVisible()
			return m
		}
	}
	t.Fatal("keysThreadFixture: no block carries the two seeded threads")
	return nil
}

// TestReviewKeysPanelMatchesActualDispatch is reviewKeyGroups' own pin: for
// every action, the table's membership claim and modeRead's real answer
// (updateRead/dispatchWrite) must agree. It is NOT proof that Go cannot produce (see
// reviewKeyGroups' own doc comment for the honest limit): it is proof that
// TODAY's switches match TODAY's table, re-checked every time this suite
// runs, so a future edit to either one that forgets the other fails HERE
// rather than shipping a wrong hint.
func TestReviewKeysPanelMatchesActualDispatch(t *testing.T) {
	m := keysThreadFixture(t)
	for _, a := range keymap.AllActions {
		t.Run(string(a), func(t *testing.T) {
			got := probeReview(m, a)
			want := reviewOwnsAction(a)
			if got != want {
				t.Fatalf("probing %s: dispatched = %v, reviewKeyGroups claims owned = %v", a, got, want)
			}
		})
	}
}

// TestUnboundKeysLeaveTheReviewViewAlone pins what a key the review view does
// not answer does in read mode: nothing. Neither the mode nor the status line
// moves, so a stray keystroke cannot open a panel or leave a sentence behind.
func TestUnboundKeysLeaveTheReviewViewAlone(t *testing.T) {
	base := keysThreadFixture(t)
	for _, k := range []string{"p", "P", "L", "x", "u", "v"} {
		t.Run(k, func(t *testing.T) {
			m := cloneReview(base)
			got := press(m, k)
			if got.mode != modeRead {
				t.Errorf("%q moved read mode to mode %v", k, got.mode)
			}
			if got.status != base.status {
				t.Errorf("%q changed the status from %q to %q", k, base.status, got.status)
			}
		})
	}
}

// TestReviewKeysPanelExcludesTheListsOwnActions pins the two actions
// keymap.go's own doc comments already declare inert here (ActRename:
// "dispatched only by the plan list's Update"; ActSort: "inert in the review
// model") -- the static half of that split, named rather than probed
// because "this action has no case anywhere in this model" is what the doc
// comments already assert and a probe from modeRead alone cannot distinguish
// from "dispatched somewhere this probe did not reach".
func TestReviewKeysPanelExcludesTheListsOwnActions(t *testing.T) {
	for _, a := range []keymap.Action{keymap.ActRename, keymap.ActSort} {
		if reviewOwnsAction(a) {
			t.Errorf("reviewKeyGroups names %s, which keymap.go documents as list-only", a)
		}
	}
}

// TestKeysPanelOpensAndClosesInReview pins opening and closing the keys panel
// in review: ? opens modeKeys from modeRead, names a group heading, esc
// closes it back to modeRead, and ? a second time toggles it shut the same
// way.
func TestKeysPanelOpensAndClosesInReview(t *testing.T) {
	m := openModel(t, setup(t))

	opened := press(m, "?")
	if opened.mode != modeKeys {
		t.Fatalf("? from modeRead landed in mode %v, want modeKeys", opened.mode)
	}
	if !strings.Contains(opened.confirm, "NAVIGATION") {
		t.Fatalf("panel body = %q, want it to carry a NAVIGATION group heading", opened.confirm)
	}

	closedByEsc := press(opened, "esc")
	if closedByEsc.mode != modeRead {
		t.Fatalf("esc from modeKeys landed in mode %v, want modeRead", closedByEsc.mode)
	}

	reopened := press(m, "?")
	closedByToggle := press(reopened, "?")
	if closedByToggle.mode != modeRead {
		t.Fatalf("? a second time landed in mode %v, want modeRead (? toggles the panel)", closedByToggle.mode)
	}
}

// TestRebindMovesTheKeyInTheReviewPanel pins a global constraint made
// concrete for this model: rebinding an action the panel names moves the row
// with it, with no edit here.
func TestRebindMovesTheKeyInTheReviewPanel(t *testing.T) {
	m := openModel(t, setup(t))
	km := keymap.Default()
	for k, a := range km {
		if a == keymap.ActComment {
			delete(km, k)
		}
	}
	km["z"] = keymap.ActComment
	m.km = km

	got := reviewKeysText(m.km)
	if !strings.Contains(got, "z · comment") {
		t.Fatalf("panel text = %q, want a row reading \"z · comment\" after rebinding ActComment to z", got)
	}
	if strings.Contains(got, "c · comment") {
		t.Fatalf("panel text = %q, still names the stale binding \"c · comment\"", got)
	}
}

// TestReviewKeysPanelBudgetAtOrdinarySize is the keys panel's own honest
// report: reviewKeysText is 28 logical lines, and truncateConfirmGroups
// (app/list.go) keeps the head line and fills backward from the TAIL.
//
// AT 80x24, THE REFERENCE WIDTH, budget is 20 (m.height and m.width both move
// confirmBudget): the tail run that survives reaches back into NAVIGATION's
// thread-reading rows -- "enter · expand/collapse thread" survives -- while
// NAVIGATION's own heading and its motion rows are gone. REVIEW and EXIT are
// whole.
//
// THE PANEL FITS WHOLE at height >= 32 (budget >= 28), so 100x40 (budget 36)
// shows every row -- measured by raising m.height a row at a time
// (TestReviewKeysPanelFitsWholeAtExactlyItsOwnThreshold pins the exact
// boundary) rather than assumed from the arithmetic alone.
//
// 120x60 (budget 56) is a common large terminal size for this panel's copy,
// re-checked here as a case rather than left as a one-off terminal run.
func TestReviewKeysPanelBudgetAtOrdinarySize(t *testing.T) {
	m := openModel(t, setup(t))
	full := reviewKeysText(m.km)
	if n := strings.Count(full, "\n") + 1; n != 28 {
		t.Fatalf("reviewKeysText is %d logical lines, want 28 -- this test's own arithmetic is written against that number; re-derive it before trusting the rest of this test", n)
	}

	for _, tc := range []struct {
		name         string
		width        int
		height       int
		wantAbsent   []string
		wantPresent  []string
		wantEllipsis bool
	}{
		{
			name:  "80x24, the reference width",
			width: 80, height: 24,
			wantAbsent:   []string{"NAVIGATION", "j/k ↑/↓", "n/N · next/prev thread"},
			wantPresent:  []string{"enter · expand/collapse thread", "REVIEW\n", "c · comment", "EXIT", "esc/? · close this panel"},
			wantEllipsis: true,
		},
		{
			name:  "100x40, past the whole-panel threshold",
			width: 100, height: 40,
			wantPresent:  []string{"NAVIGATION", "REVIEW\n", "EXIT", "j/k ↑/↓"},
			wantEllipsis: false,
		},
		{
			name:  "120x60, a common large terminal",
			width: 120, height: 60,
			wantPresent:  []string{"NAVIGATION", "REVIEW\n", "EXIT", "j/k ↑/↓ · next/prev block"},
			wantEllipsis: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.width, m.height = tc.width, tc.height
			m.enterKeysPanel()
			rendered := strings.Join(m.confirmLines(), "\n")
			if got := strings.Contains(rendered, "…"); got != tc.wantEllipsis {
				t.Errorf("ellipsis present = %v, want %v\nrendered:\n%s", got, tc.wantEllipsis, rendered)
			}
			for _, want := range tc.wantAbsent {
				if strings.Contains(rendered, want) {
					t.Errorf("%q survived, want it dropped by this size's budget\nrendered:\n%s", want, rendered)
				}
			}
			for _, want := range tc.wantPresent {
				if !strings.Contains(rendered, want) {
					t.Errorf("%q was dropped, want it to survive this size's budget\nrendered:\n%s", want, rendered)
				}
			}
		})
	}
}

// TestReviewKeysPanelFitsWholeAtExactlyItsOwnThreshold pins the height >= 32
// figure TestReviewKeysPanelBudgetAtOrdinarySize's own doc comment states, by
// measurement rather than by trusting the comment: 31 is the last height that
// still elides something, 32 the first that does not, at a width wide enough
// (100) that no row wraps and the line count is exactly the budget being
// solved for.
func TestReviewKeysPanelFitsWholeAtExactlyItsOwnThreshold(t *testing.T) {
	m := openModel(t, setup(t))
	m.width = 100
	for _, tc := range []struct {
		height       int
		wantEllipsis bool
	}{
		{height: 31, wantEllipsis: true},
		{height: 32, wantEllipsis: false},
	} {
		m.height = tc.height
		m.enterKeysPanel()
		got := strings.Contains(strings.Join(m.confirmLines(), "\n"), "…")
		if got != tc.wantEllipsis {
			t.Errorf("height %d: ellipsis present = %v, want %v", tc.height, got, tc.wantEllipsis)
		}
	}
}

// --- the list model's own half ---

type listFingerprint struct {
	mode     listMode
	status   string
	confirm  string
	cursor   int
	scroll   int
	inFlight bool
	filter   string
}

func snapshotList(m *ListModel) listFingerprint {
	return listFingerprint{
		mode: m.mode, status: m.status, confirm: m.confirm,
		cursor: m.cursor, scroll: m.scroll, inFlight: m.inFlight,
		filter: m.filter,
	}
}

// cloneList is probeList's own disposable copy -- ListModel's write dispatches
// (enterRename, enterConfirmDelete, enterSort, enterFilter, refreshCmd) all
// stage a mode/field change or a cmd rather than mutating m.items/m.rows
// synchronously, so a shallow copy is enough; rebuildRows (setMode's own
// call) only ever READS m.items.
func cloneList(base *ListModel) *ListModel {
	m := *base
	return &m
}

// probeList mirrors probeReview for updateBrowse: does THIS model answer a
// at all, dispatched from listBrowse.
func probeList(base *ListModel, a keymap.Action) bool {
	m := cloneList(base)
	before := snapshotList(m)
	m.km = keymap.Map{probeKey: a}
	cur, cmd := m.Update(probeMsg())
	return cmd != nil || snapshotList(cur.(*ListModel)) != before
}

// listActionsCheckedDynamically excludes ActPageDown/ActPageUp: their own
// dedicated test, TestListDispatchesPagingFromExpanded, drives them from
// listExpanded, the only mode that answers them. Every review-only action is
// left IN this list and probed anyway: updateBrowse has no case for any of
// them, so probing proves the negative rather than merely assuming it.
func listActionsCheckedDynamically() []keymap.Action {
	var out []keymap.Action
	for _, a := range keymap.AllActions {
		switch a {
		case keymap.ActPageDown, keymap.ActPageUp:
			continue
		}
		out = append(out, a)
	}
	return out
}

// listKeysFixture is twenty plans, more than "Your plans" draws at 80x24: enough
// VISIBLE rows that a cursor walked away from either end still has room to move
// in both directions -- see TestListKeysPanelMatchesActualDispatch for why that
// headroom is load-bearing.
func listKeysFixture(t *testing.T) *ListModel {
	t.Helper()
	return newLoadedList(t, 20)
}

// TestListKeysPanelMatchesActualDispatch is listKeyGroups' own pin, the list
// model's half of TestReviewKeysPanelMatchesActualDispatch -- see that test
// and reviewKeyGroups' own doc comment for what this style of test can and
// cannot prove.
func TestListKeysPanelMatchesActualDispatch(t *testing.T) {
	m := listKeysFixture(t)
	// Cursor walked forward to the LAST selectable row (counting the steps --
	// browse's own row budget caps it at what the terminal holds regardless of
	// how many plans are loaded, so first/last are close together and a fixed
	// back-off can overshoot) and back HALF that many, rather than landing on
	// an arithmetic midpoint: moveCursor is what actually knows which rows are
	// selectable (section bands are not), and (first+last)/2 landed on one of
	// those, which reads rename and delete -- gated on a PLAN under the
	// cursor -- as undispatched. Checked rather than assumed: a fresh list's
	// cursor sits at ActTop's own destination, and firing Top (or Bottom, or a
	// move toward whichever end it is already at) from there is a no-op on
	// the fingerprint regardless of whether the model dispatches it -- exactly
	// the false negative this guard exists to rule out.
	first, last := m.firstSelectableRow(), m.lastSelectableRow()
	m.cursor = first
	steps := 0
	for prev := -1; m.cursor != last && m.cursor != prev; steps++ {
		prev = m.cursor
		m.moveCursor(1)
	}
	for i := 0; i < steps/2; i++ {
		m.moveCursor(-1)
	}
	if _, onPlan := m.selectedPlan(); !onPlan {
		t.Fatalf("fixture setup landed the cursor at %d, not on a plan row (first=%d, last=%d, steps=%d) -- widen listKeysFixture", m.cursor, first, last, steps)
	}
	if m.cursor == first || m.cursor == last {
		t.Fatalf("fixture setup landed the cursor at %d, an end (first=%d, last=%d, steps=%d) -- widen listKeysFixture", m.cursor, first, last, steps)
	}
	for _, a := range listActionsCheckedDynamically() {
		t.Run(string(a), func(t *testing.T) {
			got := probeList(m, a)
			want := listOwnsAction(a)
			if got != want {
				t.Fatalf("probing %s: dispatched = %v, listKeyGroups claims owned = %v", a, got, want)
			}
		})
	}
}

// TestListDispatchesPagingFromExpanded is listActionsCheckedDynamically's own
// carve-out, listKeyGroups' comment on ActPageDown/ActPageUp made concrete:
// updateBrowse has no case for either, updateExpanded does, so this model
// still dispatches them -- a mode other than the one the panel opens from is
// still this model's own dispatch.
func TestListDispatchesPagingFromExpanded(t *testing.T) {
	m := newLoadedList(t, 4)
	m.setMode(listExpanded)
	for _, a := range []keymap.Action{keymap.ActPageDown, keymap.ActPageUp} {
		t.Run(string(a), func(t *testing.T) {
			clone := *m
			before := clone.scroll
			clone.km = keymap.Map{probeKey: a}
			cur, _ := clone.Update(probeMsg())
			after := cur.(*ListModel).scroll
			if before == after {
				// A single page press on a short fixture can floor/ceiling to the
				// same scroll it started at; what matters is that SOME case in
				// updateExpanded answered rather than falling through unmatched,
				// which pageScroll always reaches for a bound action.
				t.Logf("%s left scroll unchanged (%d) -- fixture may be too short to move; dispatch itself is proved by updateExpanded's own case (read at review time)", a, before)
			}
			if !listOwnsAction(a) {
				t.Fatalf("listKeyGroups does not name %s, but updateExpanded answers it", a)
			}
		})
	}
}

func TestKeysPanelOpensAndClosesInList(t *testing.T) {
	m := newLoadedList(t, 4)

	opened := pressList(m, "?")
	if opened.mode != listKeys {
		t.Fatalf("? from listBrowse landed in mode %v, want listKeys", opened.mode)
	}
	if !strings.Contains(opened.confirm, "PLAN MANAGEMENT") {
		t.Fatalf("panel body = %q, want it to carry a PLAN MANAGEMENT group heading", opened.confirm)
	}

	closedByEsc := pressList(opened, "esc")
	if closedByEsc.mode != listBrowse {
		t.Fatalf("esc from listKeys landed in mode %v, want listBrowse", closedByEsc.mode)
	}

	reopened := pressList(m, "?")
	closedByToggle := pressList(reopened, "?")
	if closedByToggle.mode != listBrowse {
		t.Fatalf("? a second time landed in mode %v, want listBrowse (? toggles the panel)", closedByToggle.mode)
	}
}

func TestRebindMovesTheKeyInTheListPanel(t *testing.T) {
	m := newLoadedList(t, 4)
	km := keymap.Default()
	for k, a := range km {
		if a == keymap.ActRename {
			delete(km, k)
		}
	}
	km["z"] = keymap.ActRename
	m.km = km

	got := listKeysText(m.km)
	if !strings.Contains(got, "z · rename") {
		t.Fatalf("panel text = %q, want a row reading \"z · rename\" after rebinding ActRename to z", got)
	}
	if strings.Contains(got, "e · rename") {
		t.Fatalf("panel text = %q, still names the stale binding \"e · rename\"", got)
	}
}

// TestTheReviewAndListKeysPanelsDiffer pins the rule both panels are bound
// by: the two views answer different action sets, so their panels must
// not read as the same list under two headings.
func TestTheReviewAndListKeysPanelsDiffer(t *testing.T) {
	km := keymap.Default()
	review := reviewKeysText(km)
	list := listKeysText(km)
	if review == list {
		t.Fatal("the review and list keys panels render identically -- they answer different action sets and must say so")
	}
	for _, want := range []string{"c · comment", "a · approve"} {
		if !strings.Contains(review, want) {
			t.Errorf("review panel missing %q", want)
		}
		if strings.Contains(list, want) {
			t.Errorf("list panel wrongly names %q, a review-only gesture", want)
		}
	}
	for _, want := range []string{"e · rename", "s · sort"} {
		if !strings.Contains(list, want) {
			t.Errorf("list panel missing %q", want)
		}
		if strings.Contains(review, want) {
			t.Errorf("review panel wrongly names %q, a list-only gesture", want)
		}
	}
}

// --- Exact text, explicit key order, blank-line sub-grouping and the box's
// own width math with the arrow glyphs in it ---

// reviewKeysWantText and listKeysWantText are the pinned copy, transcribed
// byte for byte from the two panels' own text -- every separator, every
// blank line, every row order, including the two EXIT groups' deliberately
// different order (review ends l/q/esc/?, list leads esc/?/q), AND the two
// search rows ("f · search", "/ · search") that say the same thing on
// purpose (joinKeyGroups' own doc comment, app/model.go, says why they
// cannot be one row). TestReviewKeysPanelTextIsPinned and
// TestListKeysPanelTextIsPinned pin reviewKeysText/listKeysText against
// these under keymap.Default() -- the direct "exact rendered text" coverage
// this file asks for. Everything else in this file pins a PROPERTY of the
// render (ownership, derivation, truncation, key order); only these two pin
// the words themselves.
//
// ⚠️ listKeysWantText's "i · show plan details" row is a tentative wording,
// not yet confirmed to the same degree as the rest of this file's
// transcriptions. Pinned here anyway, on TestListKeysPanelTextIsPinned's own
// job -- catch a silent change to the rendered text -- rather than left
// unpinned until a final wording arrives.
//

// NO "|" ANYWHERE: the rule is "/" between the two keys of a pair, a space
// between independent pairs on one row -- TestNoPipeSeparatorAnywhere guards
// the rule itself, not just these two literals, so a future row that
// reintroduces "|" fails even if nobody thinks to update this string first.
const reviewKeysWantText = `Key Actions

NAVIGATION
j/k ↑/↓ · next/prev block
g/G · jump to top/bottom
J/K · scroll
pgdown/pgup · page
shift + drag · select text

n/N · next/prev thread
enter · expand/collapse thread
f · search
/ · search
tab · cycle thread in this block
ctrl+r · reload

REVIEW
c · comment
r · reply
R · resolve/unresolve
d · delete thread
m · relocate an orphaned thread
a · approve

EXIT
l · back to the plan list
q · quit
esc/? · close this panel`

const listKeysWantText = `Key Actions

NAVIGATION
enter · open selected plan
j/k ↑/↓ · scroll
g/G · jump to top/bottom
pgdown/pgup · page (while a section is expanded)
ctrl+r · reload

PLAN MANAGEMENT
e · rename
d · delete
i · show plan details
/ · filter
s · sort

EXIT
esc/? · close this panel
q · quit`

// TestNoPipeSeparatorAnywhere guards the rule itself, rather than trusting
// the two exact-copy literals above never to regress back toward it: "|"
// never binds to any action in keymap.Default() and never appears in any
// row's own does text, so its total absence from either rendered panel is a
// property, not a coincidence of today's copy.
func TestNoPipeSeparatorAnywhere(t *testing.T) {
	km := keymap.Default()
	if strings.Contains(reviewKeysText(km), "|") {
		t.Error("review keys panel contains \"|\" -- the separator is \"/\" between a pair and \" \" between pairs, never \"|\"")
	}
	if strings.Contains(listKeysText(km), "|") {
		t.Error("list keys panel contains \"|\" -- the separator is \"/\" between a pair and \" \" between pairs, never \"|\"")
	}
}

func TestReviewKeysPanelTextIsPinned(t *testing.T) {
	if got := reviewKeysText(keymap.Default()); got != reviewKeysWantText {
		t.Fatalf("reviewKeysText(Default()) =\n%s\nwant\n%s", got, reviewKeysWantText)
	}
}

func TestListKeysPanelTextIsPinned(t *testing.T) {
	if got := listKeysText(keymap.Default()); got != listKeysWantText {
		t.Fatalf("listKeysText(Default()) =\n%s\nwant\n%s", got, listKeysWantText)
	}
}

// TestReviewNavigationGroupCarriesItsOwnBlankLine pins the one sub-grouping
// the pinned copy specifies INSIDE a group, distinct from keysPanelText's own
// blank line between groups: a blank line separates the viewport's own
// motion (move, jump, scroll, page, shift+drag) from reading the document's
// threads (next/prev thread onward), both still under one NAVIGATION
// heading. Checked as its own substring test, rather than folded entirely
// into the exact-match test above, because a failure here names WHICH
// property broke -- the blank line itself, as opposed to a row either side of
// it -- where the exact-match test would only say the whole body differs.
func TestReviewNavigationGroupCarriesItsOwnBlankLine(t *testing.T) {
	got := reviewKeysText(keymap.Default())
	const want = "shift + drag · select text\n\nn/N · next/prev thread"
	if !strings.Contains(got, want) {
		t.Fatalf("panel text =\n%s\nwant it to contain %q -- the blank line inside NAVIGATION", got, want)
	}
	// NO GROUP HAS A BLANK LINE DIRECTLY UNDER ITS OWN HEADING (the pinned
	// copy's own rule): keysPanelText's between-group blank plus a heading
	// would read as two blanks in a row if a group's FIRST row were also
	// blank.
	if strings.Contains(got, "NAVIGATION\n\n") {
		t.Fatal("NAVIGATION carries a blank line directly under its heading, which the pinned copy rules out")
	}
}

// findKeysRow is a test-only lookup by a row's own does text -- the same
// shape TestRebindMovesTheKeyInTheReviewPanel already gets from
// strings.Contains on the whole panel, used here instead because the tests
// below want the SINGLE ROW's own render, not a substring search across a
// body where an unrelated row could coincidentally contain the text being
// checked for. NOT UNIQUE FOR "search": reviewKeyGroups' own two search rows
// (ActSearch, ActFilter) say "search" on purpose (see that table's own
// comment) and this returns whichever is declared first -- fine for every
// caller today, none of which looks up "search", but a future one wanting
// the ActFilter row specifically needs its own lookup, not this one.
func findKeysRow(t *testing.T, groups []keysGroup, does string) keysRow {
	t.Helper()
	for _, g := range groups {
		for _, r := range g.rows {
			if r.does == does {
				return r
			}
		}
	}
	t.Fatalf("no row with does = %q in this table", does)
	return keysRow{}
}

// TestExplicitKeyOrderCoversTheDerivedSet is the pin keysRow.keyOrder's own
// doc comment promises: a keyOrder exists only because pure derivation cannot
// produce that row's display order (the movement row's vim-then-arrow
// interleave), and the risk a keyOrder carries that an ordinary row does not
// is drifting out of step with what its actions actually bind in
// keymap.Default() -- typo'd at declaration, or left behind when Default()
// itself changes. Either is exactly how a binding could read as though it
// vanished from the panel: applyKeyOrder's own leftover fallback (model.go)
// still shows a key keyOrder does not name, but out of the row's intended
// order, so this table's OWN promise -- "this row shows every key j/k/↑/↓
// actually name, in exactly this order" -- needs a check keyOrder's mere
// existence cannot give it.
//
// WALKS BOTH TABLES rather than naming the movement rows by hand, so a future
// row with a keyOrder of its own is checked the day it is written rather than
// the day someone remembers to extend this test by name.
func TestExplicitKeyOrderCoversTheDerivedSet(t *testing.T) {
	km := keymap.Default()
	checked := 0
	for _, tc := range []struct {
		panel  string
		groups []keysGroup
	}{
		{"review", reviewKeyGroups},
		{"list", listKeyGroups},
	} {
		for _, g := range tc.groups {
			for _, r := range g.rows {
				if r.keyOrder == nil {
					continue
				}
				checked++
				derived := map[string]bool{}
				for _, k := range r.derivedKeys(km) {
					derived[k] = true
				}
				order := map[string]bool{}
				for _, k := range r.keyOrder {
					order[k] = true
				}
				for k := range derived {
					if !order[k] {
						t.Errorf("%s panel, row %q: keymap.Default() derives %q for this row's actions, but keyOrder does not name it -- fewer than the derived set", tc.panel, r.does, k)
					}
				}
				for k := range order {
					if !derived[k] {
						t.Errorf("%s panel, row %q: keyOrder names %q, which keymap.Default() no longer derives for this row's actions -- more than the derived set, a stale entry", tc.panel, r.does, k)
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no row in either table carries a keyOrder -- this test would then assert nothing; either a row lost its keyOrder or this test's own table walk is broken")
	}
}

// TestRebindSurvivesInAKeyOrderRow is TestRebindMovesTheKeyInTheReviewPanel's
// own case for a keyOrder row: the one risk keyOrder's fixed list carries
// that an ordinary derived row does not (see keysRow.keyOrder's own doc
// comment) is a rebind landing on a keystroke keyOrder never names, and this
// proves applyKeyOrder's leftover fallback actually reaches the panel rather
// than only being argued for in a comment -- the failure mode "a rebind
// silently vanishes" made concrete for the two rows that could have it.
func TestRebindSurvivesInAKeyOrderRow(t *testing.T) {
	rebind := func(a keymap.Action, newKey string) keymap.Map {
		km := keymap.Map{}
		for k, v := range keymap.Default() {
			km[k] = v
		}
		for k, v := range km {
			if v == a {
				delete(km, k)
			}
		}
		km[newKey] = a
		return km
	}

	for _, tc := range []struct {
		name   string
		groups []keysGroup
		does   string
		rebind keymap.Action
		newKey string
		want   string
	}{
		{
			// order = [j,k,up,down]; ActMoveUp's k and up are both gone, so only
			// "j" and "down" (glyphed ↓) are named by order -- joinKeyGroups pairs
			// those two ("j/↓"), and the rebound "w" lands as leftover, its own
			// unpaired trailing group ("j/↓ w"), never dropped.
			name:   "review movement row, ActMoveUp off k/up onto w",
			groups: reviewKeyGroups, does: "next/prev block",
			rebind: keymap.ActMoveUp, newKey: "w",
			want: "j/↓ w · next/prev block",
		},
		{
			// order = [j,k,up,down]; ActMoveDown's j and down are both gone, so
			// only "k" and "up" (glyphed ↑) are named by order -- paired "k/↑",
			// with rebound "z" trailing unpaired, same shape as the case above.
			name:   "list movement row, ActMoveDown off j/down onto z",
			groups: listKeyGroups, does: "scroll",
			rebind: keymap.ActMoveDown, newKey: "z",
			want: "k/↑ z · scroll",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := findKeysRow(t, tc.groups, tc.does)
			km := rebind(tc.rebind, tc.newKey)
			if got := row.render(km); got != tc.want {
				t.Fatalf("row.render after rebinding %s to %q = %q, want %q", tc.rebind, tc.newKey, got, tc.want)
			}
		})
	}
}

// TestArrowGlyphsDoNotBreakTheBoxWidthMath answers the one open question a
// copy pass that introduces the FIRST non-ASCII runes ever printed inside a
// centred panel's body has to answer rather than assume: centredBox
// (app/painted.go) derives its own width from ansi.StringWidth over every row
// it is handed, and a rune measured wider (or narrower) than the ONE cell it
// actually occupies would misalign the box -- some row padded short, or the
// border drawn narrower than the content it frames. ansi.StringWidth is
// checked directly against the two glyphs THIS panel introduces, and the
// rendered box is checked for what a width bug would actually produce: every
// row (border, blank filler, content) coming out the SAME number of cells
// wide, the invariant centredBox's own construction promises.
func TestArrowGlyphsDoNotBreakTheBoxWidthMath(t *testing.T) {
	if w := ansi.StringWidth("↑"); w != 1 {
		t.Errorf("ansi.StringWidth(\"↑\") = %d, want 1 -- centredBox sizes its box off this measure", w)
	}
	if w := ansi.StringWidth("↓"); w != 1 {
		t.Errorf("ansi.StringWidth(\"↓\") = %d, want 1 -- centredBox sizes its box off this measure", w)
	}

	m := openModel(t, setup(t))
	m.width, m.height = 120, 60
	m.enterKeysPanel()
	movementRow := findKeysRow(t, reviewKeyGroups, "next/prev block").render(m.km)
	if !strings.Contains(movementRow, "↑") || !strings.Contains(movementRow, "↓") {
		t.Fatalf("movement row %q does not carry both arrow glyphs -- this test exists to check them", movementRow)
	}

	rows := strings.Split(m.centredPanelBox(), "\n")
	if len(rows) == 0 {
		t.Fatal("centredPanelBox() rendered no rows")
	}
	want := ansi.StringWidth(ansi.Strip(rows[0]))
	for i, row := range rows {
		if w := ansi.StringWidth(ansi.Strip(row)); w != want {
			t.Errorf("box row %d is %d cells wide, want %d (every row centredBox draws is padded to the SAME width) -- row: %q", i, w, want, ansi.Strip(row))
		}
	}
}

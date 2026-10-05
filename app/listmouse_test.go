package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/client/recent"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/keymap"
)

// newLoadedList builds a list model over n plans through listOfSvc, sized 80x24
// and with its first load applied -- the state the very first frame is drawn
// in. listOfSvc answers ListPlans and panics on everything else through its nil
// embedded interface, so a handler that tried to write here fails loudly.
func newLoadedList(t *testing.T, n int) *ListModel {
	t.Helper()
	m := NewList(listOfSvc{plans: freshestFirstPlans(n)}, keymap.Default(), nil)
	cur, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = cur.(*ListModel)
	return drainList(t, m, m.Init())
}

// The wheel moves the CURSOR, one row per event, in listBrowse and
// listExpanded.
//
// TWO INDEPENDENT MODELS, NOT ONE MODEL COMPARED TO A RECOMPUTED EXPECTATION:
// a test that re-ran nextSelectableRow's own walk by hand to predict where the
// cursor should land would mirror the implementation and pass under a bug in
// that walk exactly as readily as under a correct one. Driving one identical
// twin through updateBrowse/updateExpanded's real ActMoveDown/ActMoveUp case
// and the other through the wheel handler, then requiring the same cursor,
// tests that the wheel reaches the SAME mechanism the keyboard already does --
// moveCursor -- rather than a parallel copy of its arithmetic. It also gives
// "N wheel events move the cursor by N selectable rows" for free.
//
// THE MUTATION THIS KILLS: route the wheel to m.scroll instead of moveCursor.
// The keyboard twin's cursor still walks forward or back; the wheel twin's
// does not move at all (browse structurally cannot scroll), so the two
// disagree and every case below reddens.
func TestMouseWheelMovesCursorLikeTheKeyboard(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   listMode
		button tea.MouseButton
		key    string
		n      int
	}{
		{"browse, wheel down moves like j", listBrowse, tea.MouseWheelDown, "j", 3},
		{"browse, wheel up moves like k", listBrowse, tea.MouseWheelUp, "k", 2},
		{"expanded, wheel down moves like j", listExpanded, tea.MouseWheelDown, "j", 3},
		{"expanded, wheel up moves like k", listExpanded, tea.MouseWheelUp, "k", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wheelModel := newLoadedList(t, 10)
			keyModel := newLoadedList(t, 10)
			for _, m := range []*ListModel{wheelModel, keyModel} {
				m.setMode(tc.mode)
			}

			// Moving UP needs somewhere to move up FROM: both twins first
			// walk down a few rows by keyboard, identically, so the case
			// under test is not vacuously parked at row 0 already.
			if tc.button == tea.MouseWheelUp {
				for i := 0; i < tc.n+2; i++ {
					wheelModel, _ = pressListKey(wheelModel, "j")
					keyModel, _ = pressListKey(keyModel, "j")
				}
			}
			if wheelModel.cursor != keyModel.cursor {
				t.Fatalf("test setup: twins disagree before the case under test runs: %d vs %d", wheelModel.cursor, keyModel.cursor)
			}
			start := wheelModel.cursor

			for i := 0; i < tc.n; i++ {
				cur, cmd := wheelModel.Update(tea.MouseWheelMsg{Button: tc.button})
				if cmd != nil {
					t.Fatalf("wheel event %d dispatched %T, want nothing -- the wheel only moves the cursor", i, cmd())
				}
				wheelModel = cur.(*ListModel)
			}
			for i := 0; i < tc.n; i++ {
				keyModel, _ = pressListKey(keyModel, tc.key)
			}

			if wheelModel.cursor == start {
				t.Fatalf("test setup: %d wheel events left the cursor at %d, unmoved -- the fixture needs more rows in this direction", tc.n, start)
			}
			if wheelModel.cursor != keyModel.cursor {
				t.Fatalf("%d wheel events put the cursor at %d, want %d (%d presses of %q)", tc.n, wheelModel.cursor, keyModel.cursor, tc.n, tc.key)
			}
		})
	}
}

// The other half of that rule: the wheel drives exactly listBrowse and
// listExpanded, each covered by the test above, and every other mode leaves
// it inert -- a panel (rename, confirm-delete) holds the keyboard for its own
// text or y/n, the filter consumes literal characters, and the sort modal
// claims j/k for its own three options.
//
// THE SET IS DRIVEN FROM THE ENUM: listModeCount moves when a mode is added,
// so a new mode is covered here automatically rather than needing to be added
// to a hand-picked list. m.mode is set directly rather than through setMode,
// so this needs no per-mode setup (a live rename draft, a confirm target) that
// this test has no interest in.
func TestMouseWheelIsInertOutsideBrowseAndExpanded(t *testing.T) {
	m := newLoadedList(t, 10)

	for lm := listMode(0); lm < listModeCount; lm++ {
		if lm == listBrowse || lm == listExpanded {
			continue
		}
		m.mode = lm
		cursor, scroll := m.cursor, m.scroll

		for _, button := range []tea.MouseButton{tea.MouseWheelDown, tea.MouseWheelUp} {
			cur, cmd := m.Update(tea.MouseWheelMsg{Button: button})
			if cmd != nil {
				t.Errorf("mode %d: wheel event returned %T, want nil", lm, cmd())
			}
			m = cur.(*ListModel)
		}

		if m.cursor != cursor {
			t.Errorf("mode %d: cursor = %d, want unchanged %d -- the wheel must be inert outside listBrowse/listExpanded", lm, m.cursor, cursor)
		}
		if m.scroll != scroll {
			t.Errorf("mode %d: scroll = %d, want unchanged %d", lm, m.scroll, scroll)
		}
	}
}

// A wheel turned past either end of the list must do nothing rather than error
// or wrap. moveCursor already guarantees this, so this pins the guarantee at
// the wheel's own entry point rather than trusting it by inference from
// moveCursor's other callers.
func TestMouseWheelAtListEdgeIsANoOp(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   listMode
		button tea.MouseButton
		atTail bool // true: park on the LAST selectable row and turn the wheel further down; false: the first, and turn it up.
	}{
		{"browse, wheel up at the top", listBrowse, tea.MouseWheelUp, false},
		{"browse, wheel down at the bottom", listBrowse, tea.MouseWheelDown, true},
		{"expanded, wheel up at the top", listExpanded, tea.MouseWheelUp, false},
		{"expanded, wheel down at the bottom", listExpanded, tea.MouseWheelDown, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newLoadedList(t, 10)
			m.setMode(tc.mode)
			if tc.atTail {
				m.cursor = m.lastSelectableRow()
			} else {
				m.cursor = m.firstSelectableRow()
			}
			cursor := m.cursor

			cur, cmd := m.Update(tea.MouseWheelMsg{Button: tc.button})
			if cmd != nil {
				t.Fatalf("wheel event at the edge dispatched %T, want nothing", cmd())
			}
			m = cur.(*ListModel)

			if m.cursor != cursor {
				t.Fatalf("cursor = %d, want unchanged %d -- a wheel past the edge of the list must be a no-op, not an error", m.cursor, cursor)
			}
		})
	}
}

// Below either minimum the screen is gateView's refusal and nothing else, so a
// wheel event must be refused there exactly as a key already is -- a cursor
// move that still landed would re-aim enter/e/d against a body the screen is
// not drawing.
//
// WIDTH ALONE FAILS THE GATE HERE, height stays at the fixture's own 24:
// viewHeight (and so rowBudget, and so how many selectable rows exist to move
// the cursor across) reads m.height only, never m.width, so shrinking width
// alone gates fitsMinimum() to false while leaving the SAME row layout the
// test above already proves the wheel can move a cursor across.
//
// NON-VACUITY: the SAME wheel events, against the SAME model, sent again after
// resizing back above the gate, must move the cursor -- otherwise "the cursor
// did not move" below could just as well be an empty list or a model wedged
// some other way, and the guard under test would be unproven.
//
// THE MUTATION THIS KILLS: delete the `if !m.fitsMinimum() { return m, nil }`
// guard from Update's tea.MouseWheelMsg case. The below-gate assertion reddens
// while the above-gate one, unaffected, stays green -- which is what proves
// this is the guard's own test and not a duplicate of the test above.
func TestMouseWheelIsInertBelowTheMinimumSizeGate(t *testing.T) {
	m := newLoadedList(t, 10)
	m.setMode(listBrowse)

	cur, _ := m.Update(tea.WindowSizeMsg{Width: listMinWidth - 1, Height: 24})
	m = cur.(*ListModel)
	if m.fitsMinimum() {
		t.Fatal("test setup: the terminal must be below the minimum-size gate")
	}
	belowCursor := m.cursor

	for i := 0; i < 3; i++ {
		cur, cmd := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		if cmd != nil {
			t.Fatalf("wheel event %d below the gate dispatched %T, want nothing", i, cmd())
		}
		m = cur.(*ListModel)
	}
	if m.cursor != belowCursor {
		t.Fatalf("cursor = %d after 3 wheel events below the minimum-size gate, want unchanged %d -- "+
			"a wheel event must be refused exactly as a key is", m.cursor, belowCursor)
	}

	// Non-vacuity: resize back above the gate and confirm the SAME wheel
	// events move the cursor there, on the SAME model.
	cur, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = cur.(*ListModel)
	if !m.fitsMinimum() {
		t.Fatal("test setup: resizing back up did not clear the gate")
	}
	aboveStart := m.cursor
	for i := 0; i < 3; i++ {
		cur, cmd := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		if cmd != nil {
			t.Fatalf("wheel event %d above the gate dispatched %T, want nothing", i, cmd())
		}
		m = cur.(*ListModel)
	}
	if m.cursor == aboveStart {
		t.Fatal("non-vacuity: the SAME wheel events above the gate must move the cursor, " +
			"or the assertion below the gate proves nothing about the gate")
	}
}

// frameLineContaining answers the index of the one line in a painted frame
// that contains want, and fails the test if no line does or more than one
// does. Every click test below uses this, never arithmetic on
// m.scroll/mastheadHeight, to find WHERE to click: the row the hit test must
// land on is read off the SAME frame a terminal would draw, since a fixture
// that trusted the arithmetic to find its own target would agree with a hit
// test that used the identical arithmetic wrongly.
func frameLineContaining(t *testing.T, frame []string, want string) int {
	t.Helper()
	found := -1
	for i, l := range frame {
		if strings.Contains(ansi.Strip(l), want) {
			if found >= 0 {
				t.Fatalf("test setup: %q is drawn on frame lines %d and %d -- cannot name one row by it", want, found, i)
			}
			found = i
		}
	}
	if found < 0 {
		t.Fatalf("test setup: no frame line contains %q", want)
	}
	return found
}

// mastheadHeight's own pin: updateMouseClick's whole hit test is
// rowIdx := m.scroll + (y - m.mastheadHeight()), and that subtraction is only
// the frame's own inverse if mastheadHeight()'s ANSWER is what the renderer
// actually reserves. This drives BOTH values mastheadHeight can give --
// browse's 5 and a panel mode's 4 -- and renders a REAL frame rather than
// trusting the constant, so a masthead row added above the body later fails
// here instead of sending every click one row high with nothing to say so.
//
// THE WANT SIDE IS renderBodyRowPainted, THE SAME FUNCTION viewPainted's body
// loop CALLS for this very row -- not a second guess at what m.rows[m.scroll]
// looks like painted. That is what makes this a pin on the OFFSET rather than
// a restatement of the row-paint tests elsewhere in this package.
func TestMastheadHeightIsPinnedToFrameRow(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode listMode
		want int
	}{
		{"browse", listBrowse, 5},
		{"a panel mode (rename)", listRename, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newLoadedList(t, 10)
			m.setMode(tc.mode)

			if got := m.mastheadHeight(); got != tc.want {
				t.Fatalf("mastheadHeight() = %d, want %d", got, tc.want)
			}
			if len(m.rows) == 0 {
				t.Fatal("test setup: m.rows is empty, nothing to pin the offset against")
			}

			frame := strings.Split(m.View().Content, "\n")
			if len(frame) <= m.mastheadHeight() {
				t.Fatalf("test setup: frame has %d rows, want more than mastheadHeight() (%d)", len(frame), m.mastheadHeight())
			}
			want := ansi.Strip(m.renderBodyRowPainted(m.rows[m.scroll], m.cursorOn(m.scroll)))
			got := ansi.Strip(frame[m.mastheadHeight()])
			if got != want {
				t.Fatalf("frame row %d (mastheadHeight()) = %q, want m.rows[m.scroll]'s own paint %q", m.mastheadHeight(), got, want)
			}
		})
	}
}

// MouseLeft-only is folded in here: the same fixture already proves the left
// half moves the cursor, so a right click at the SAME y is what proves
// msg.Button != tea.MouseLeft is load-bearing rather than vacuous.
//
// THE TARGET ROW IS FOUND BY TEXT, NEVER BY INDEX: "Plan 00" is the freshest
// mine plan, and its y comes from frameLineContaining -- the painted frame,
// not an index this file could get wrong in the same way the hit test could.
//
// PARKED ON A DIFFERENT PLAN FIRST, in both the left and right subtests: a
// left click that silently no-oped would leave selectedPlan() agreeing with
// the OLD cursor, which is exactly the state a vacuous assertion cannot tell
// from a working one.
func TestMouseClickOnPlanRowSelectsItAndEnterOpensIt(t *testing.T) {
	for _, mc := range []struct {
		name string
		mode listMode
	}{
		{"browse", listBrowse},
		{"expanded", listExpanded},
	} {
		t.Run(mc.name, func(t *testing.T) {
			m := newLoadedList(t, 10)
			m.setMode(mc.mode)

			const wantTitle = "Plan 00"
			frame := strings.Split(m.View().Content, "\n")
			y := frameLineContaining(t, frame, wantTitle)

			for _, tc := range []struct {
				name       string
				button     tea.MouseButton
				wantSelect bool
			}{
				{"left selects the plan", tea.MouseLeft, true},
				{"right is inert (MouseLeft only)", tea.MouseRight, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					m.cursor = m.firstSelectableRow()
					m.moveCursor(1)
					if it, ok := m.selectedPlan(); !ok || it.plan.Title == wantTitle {
						t.Fatalf("test setup: cursor parked on plan=%+v ok=%v, want a plan other than %q", it, ok, wantTitle)
					}
					before := m.cursor

					cur, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tc.button})
					if cmd != nil {
						t.Fatalf("click returned a non-nil cmd: %v, want nil", cmd)
					}
					m = cur.(*ListModel)

					if !tc.wantSelect {
						if m.cursor != before {
							t.Fatalf("cursor = %d, want unchanged %d -- %s", m.cursor, before, tc.name)
						}
						return
					}
					it, ok := m.selectedPlan()
					if !ok || it.plan.Title != wantTitle {
						t.Fatalf("selectedPlan() = %+v, ok=%v, want the plan drawn at frame row %d (%q)", it, ok, y, wantTitle)
					}

					cur, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
					if cmd == nil {
						t.Fatal("enter after the click returned a nil cmd, want msgOpenPlan")
					}
					msg := cmd()
					opened, ok := msg.(msgOpenPlan)
					if !ok {
						t.Fatalf("enter dispatched %T, want msgOpenPlan", msg)
					}
					if opened.plan.Title != wantTitle {
						t.Fatalf("enter opened %q, want the clicked plan %q", opened.plan.Title, wantTitle)
					}
					m = cur.(*ListModel)
				})
			}
		})
	}
}

// rowKind.selectable() is false for both a section's own name band and an
// empty section's hint, so updateMouseClick's gate refuses both without
// enumerating either kind by name. The fixture holds a file in Recently opened
// and no plans at all, so the cursor rests on the file row and "Your plans"
// draws its own real hint beneath its band; both targets are found by the
// exact text bandTitle and emptyHint hand the renderer -- not by row index.
func TestMouseClickOnSectionBandAndEmptyHintIsInert(t *testing.T) {
	notes := writeDoc(t, "notes.md", "# Scratch notes\n\nbody\n")
	m := newRecentList(t, nil, recent.Entry{Path: notes})
	cur, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = cur.(*ListModel)
	if m.mode != listBrowse {
		t.Fatalf("test setup: mode = %v, want listBrowse", m.mode)
	}
	if r, ok := m.cursorRow(); !ok || r.kind != rowFile {
		t.Fatalf("test setup: cursor row = %+v, want the file row", r)
	}

	frame := strings.Split(m.View().Content, "\n")
	bandY := frameLineContaining(t, frame, listMineSectionTitle)
	// The hint's first SENTENCE: the whole hint is wider than an 80-column
	// frame draws before clipping the row.
	hintFirstSentence := strings.SplitN(listEmptyHint, "! ", 2)[0] + "!"
	hintY := frameLineContaining(t, frame, hintFirstSentence)

	for _, tc := range []struct {
		name string
		y    int
	}{
		{"a section's own name band", bandY},
		{"an empty section's hint", hintY},
	} {
		cursor, scroll := m.cursor, m.scroll

		cur, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: tc.y, Button: tea.MouseLeft})
		if cmd != nil {
			t.Fatalf("%s: click returned a non-nil cmd: %v, want nil", tc.name, cmd)
		}
		m = cur.(*ListModel)

		if m.cursor != cursor {
			t.Fatalf("%s: cursor = %d, want unchanged %d -- chrome is not selectable", tc.name, m.cursor, cursor)
		}
		if m.scroll != scroll {
			t.Fatalf("%s: scroll = %d, want unchanged %d", tc.name, m.scroll, scroll)
		}
	}
}

// The tail row is clicked, and enter then EXPANDS the section: this
// proves the click's cursor placement reaches the SAME enter dispatch the
// keyboard does.
//
// tail.text IS THE SEARCH STRING, not a literal "+N more" this test would have
// to keep in sync with moreRow's own format by hand: it is the exact string
// moreRow already built for this row, so the frame lookup uses the production
// row's own words rather than a guess at them.
func TestMouseClickOnTailSelectsItAndEnterExpands(t *testing.T) {
	m := newLoadedList(t, 105)

	_, tail, ok := sectionMoreRow(m, sectionMine)
	if !ok {
		t.Fatal("test setup: no +N more tail row -- want more plans than browse's budget can show")
	}
	frame := strings.Split(m.View().Content, "\n")
	y := frameLineContaining(t, frame, strings.TrimSpace(tail.text))

	cur, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatalf("click returned a non-nil cmd: %v, want nil", cmd)
	}
	m = cur.(*ListModel)
	if r, ok := m.cursorRow(); !ok || r.kind != rowMore {
		t.Fatalf("cursorRow() = %+v, ok=%v, want the +N more row the click landed on", r, ok)
	}

	cur, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = cur.(*ListModel)
	if m.mode != listExpanded {
		t.Fatalf("mode = %v after enter on the clicked tail, want listExpanded (browse's enter expands)", m.mode)
	}
	if cmd != nil {
		t.Fatalf("enter that expands dispatched %T, want nothing -- expanding reads and writes nothing", cmd())
	}
}

// Above the body, viewPainted writes a ground row, the masthead line and a
// blank separator, so a click at any of those Y values is bubbletea's own
// ordinary zero-based (0,0)-upper-left coordinate, not a synthetic corner
// case. row < 0 is the same half of the click's row bound as
// row >= viewHeight(), and the row this file's other tests click is always
// >= mastheadHeight(), so nothing else here actually drives Y values above it.
func TestMouseClickAboveTheBodyIsInert(t *testing.T) {
	m := newLoadedList(t, 10)

	for y := 0; y < m.mastheadHeight(); y++ {
		cursor, scroll := m.cursor, m.scroll

		cur, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
		if cmd != nil {
			t.Fatalf("y=%d: click returned a non-nil cmd: %v, want nil", y, cmd)
		}
		m = cur.(*ListModel)

		if m.cursor != cursor {
			t.Fatalf("y=%d: cursor = %d, want unchanged %d -- a click above the body must be inert", y, m.cursor, cursor)
		}
		if m.scroll != scroll {
			t.Fatalf("y=%d: scroll = %d, want unchanged %d", y, m.scroll, scroll)
		}
	}
}

// A short list leaves blank filler below its last real row, and a click
// landing there must find no row at all rather than the nearest one -- the
// rowIdx < len(m.rows) half of the click's row bound. There is no frame TEXT
// to find there by construction, so this target is placed by arithmetic on the
// viewport's own last row -- not by m.scroll + a walk.
func TestMouseClickBelowTheBodyIsInert(t *testing.T) {
	m := NewList(nil, keymap.Default(), nil)
	m.width, m.height = 80, 24
	m.applyRefresh([]planItem{
		{plan: domain.Plan{ID: "m1", Title: "Solo One"}},
		{plan: domain.Plan{ID: "m2", Title: "Solo Two"}},
	})
	if got, want := len(m.rows), m.viewHeight(); got >= want {
		t.Fatalf("test setup: %d rows, want fewer than viewHeight() (%d) so blank filler exists below them", got, want)
	}
	cursor, scroll := m.cursor, m.scroll

	y := m.mastheadHeight() + m.viewHeight() - 1
	cur, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatalf("click returned a non-nil cmd: %v, want nil", cmd)
	}
	m = cur.(*ListModel)

	if m.cursor != cursor {
		t.Fatalf("cursor = %d, want unchanged %d -- a click on blank filler below a short body must be inert", m.cursor, cursor)
	}
	if m.scroll != scroll {
		t.Fatalf("scroll = %d, want unchanged %d", m.scroll, scroll)
	}
}

// The case that makes the row >= m.viewHeight() half of the click's row bound
// non-vacuous: with a list LONGER than the viewport, a rowIdx computed past
// the body without that bound would still be < len(m.rows) -- valid by the
// OTHER bound alone -- and would silently select a real, off-screen row instead
// of refusing. A short list cannot tell that bound from the len(m.rows) one at
// all, which is exactly why this needs its own 105-plan fixture.
func TestMouseClickOnStatusBarOfALongListIsInert(t *testing.T) {
	m := newLoadedList(t, 105)
	m.setMode(listExpanded)

	if got, want := len(m.rows), m.viewHeight(); got <= want {
		t.Fatalf("test setup: %d rows, want more than viewHeight() (%d) so an off-screen row exists to (wrongly) select", got, want)
	}
	cursor, scroll := m.cursor, m.scroll

	frame := strings.Split(m.View().Content, "\n")
	statusY := m.mastheadHeight() + m.viewHeight()
	if statusY >= len(frame) {
		t.Fatalf("test setup: statusY %d is past the frame's %d rows", statusY, len(frame))
	}
	if !strings.Contains(ansi.Strip(frame[statusY]), "plan") {
		t.Fatalf("test setup: frame row %d = %q, does not look like the status bar (planCountText)", statusY, ansi.Strip(frame[statusY]))
	}

	cur, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: statusY, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatalf("click returned a non-nil cmd: %v, want nil", cmd)
	}
	m = cur.(*ListModel)

	if m.cursor != cursor {
		t.Fatalf("cursor = %d, want unchanged %d -- a click on the status bar of a list longer than the viewport must be inert", m.cursor, cursor)
	}
	if m.scroll != scroll {
		t.Fatalf("scroll = %d, want unchanged %d", m.scroll, scroll)
	}
}

// The mode gate.
//
// Y: m.mastheadHeight() (row 0) IS VACUOUS AGAINST THIS GATE. Row 0 is always
// a rowBand: sectionRows unconditionally emits sectionHead (sectionMine)
// first, so m.rows[0] is a section's own name band whatever the mode, width or
// height. A click there is refused by rowKind.selectable() regardless of
// whether the mode gate exists at all, so deleting the gate leaves such a test
// green -- "a mode that forgot to gate would be caught landing it, not merely
// missing an unrelated row."
//
// row IS FOUND ONCE, OFF THE PAINTED FRAME, WHILE STILL IN BROWSE (m.mode's
// starting value): frameLineContaining locates "Plan 00", the freshest mine
// plan, and its OFFSET from that frame's own mastheadHeight() is kept rather
// than the absolute Y. m.mode = lm below is a bare field write, not setMode,
// so it never rebuilds m.rows -- the row this offset names stays "Plan 00" in
// every mode.
//
// Y ITSELF IS RECOMPUTED PER MODE, not reused as one constant: mastheadHeight()
// answers 4 in a panel mode (listRename, listConfirmDelete) and 3 in every
// other mode -- reusing browse's Y verbatim would send a panel-mode click one
// row short of "Plan 00", onto the column-label band beside it, which
// rowKind.selectable() refuses on its own and would make exactly those two
// modes vacuous again.
//
// THE CURSOR IS PARKED AWAY FROM THAT ROW BEFORE EVERY CLICK: "Plan 00" is
// firstSelectableRow(), where rebuildRows' own snapCursor already lands a fresh
// model's cursor, so a mutated click that (wrongly) accepted would set m.cursor
// to the very value it already held and be indistinguishable from a correctly
// refused one. Parking on the SECOND selectable row first is what makes an
// accepted click's landing observable as a change.
func TestMouseClickIsInertOutsideBrowseAndExpanded(t *testing.T) {
	m := newLoadedList(t, 10)

	frame := strings.Split(m.View().Content, "\n")
	row := frameLineContaining(t, frame, "Plan 00") - m.mastheadHeight()

	for lm := listMode(0); lm < listModeCount; lm++ {
		if lm == listBrowse || lm == listExpanded {
			continue
		}
		m.mode = lm
		m.cursor = m.firstSelectableRow()
		m.moveCursor(1)
		if r, ok := m.cursorRow(); !ok || r.kind != rowPlan || (r.item.plan.Title == "Plan 00") {
			t.Fatalf("mode %d: test setup: cursor parked on row=%+v ok=%v, want a plan other than \"Plan 00\"", lm, r, ok)
		}
		cursor, scroll := m.cursor, m.scroll

		y := m.mastheadHeight() + row
		cur, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
		if cmd != nil {
			t.Errorf("mode %d: click returned %T, want nil", lm, cmd())
		}
		m = cur.(*ListModel)

		if m.cursor != cursor {
			t.Errorf("mode %d: cursor = %d, want unchanged %d -- a click must be inert outside listBrowse/listExpanded", lm, m.cursor, cursor)
		}
		if m.scroll != scroll {
			t.Errorf("mode %d: scroll = %d, want unchanged %d", lm, m.scroll, scroll)
		}
	}
}

// The click's own gate in Update's tea.MouseClickMsg case: below either
// minimum the screen is gateView's refusal and nothing else, so a click there
// must be refused exactly as a key and a wheel event already are.
//
// Y: m.mastheadHeight() (row 0) IS VACUOUS AGAINST THIS GATE, the same trap
// the test above names: row 0 is always a rowBand, so a click there is refused
// by rowKind.selectable() whether or not the size gate exists at all. belowY
// targets the SAME "Plan 00" row instead.
//
// FOUND WHILE STILL ABOVE THE GATE, since gateView's own refusal screen draws
// no rows at all -- there is no frame below the gate to search with
// frameLineContaining. VERIFIED RATHER THAN ASSUMED after the width-only
// resize: viewHeight (and so rowBudget, and so the row layout) reads m.height
// only, never m.width, so "Plan 00" should still be m.rows[rowIdx]
// post-resize -- checked here rather than trusted, since nothing below the
// gate could catch it if that stopped being true.
//
// NON-VACUITY, BELOW THE GATE: "Plan 00" is firstSelectableRow(), where a
// fresh model's cursor already sits, and the resize-down carries the cursor by
// PLAN, so it is STILL there afterwards. A mutated click that (wrongly)
// reached updateMouseClick and accepted would set m.cursor back to the exact
// value it already held. Parked on the SECOND selectable row instead, after
// the resize, so an accepted click's landing on rowIdx is observable.
//
// NON-VACUITY, ABOVE THE GATE: the cursor is parked away from "Plan 00" before
// the ABOVE-gate click too -- otherwise "the cursor did not move" could just as
// well mean it was already there, and the assertion below the gate would prove
// nothing about the gate.
func TestMouseClickIsInertBelowTheMinimumSizeGate(t *testing.T) {
	m := newLoadedList(t, 10)
	m.setMode(listBrowse)

	frame := strings.Split(m.View().Content, "\n")
	rowIdx := m.scroll + (frameLineContaining(t, frame, "Plan 00") - m.mastheadHeight())

	cur, _ := m.Update(tea.WindowSizeMsg{Width: listMinWidth - 1, Height: 24})
	m = cur.(*ListModel)
	if m.fitsMinimum() {
		t.Fatal("test setup: the terminal must be below the minimum-size gate")
	}
	if rowIdx >= len(m.rows) || m.rows[rowIdx].kind != rowPlan || m.rows[rowIdx].item.plan.Title != "Plan 00" {
		t.Fatalf("test setup: m.rows[%d] is not still \"Plan 00\" after the width-only resize -- "+
			"the row layout moved, so belowY no longer names a real plan row", rowIdx)
	}
	belowY := m.mastheadHeight() + (rowIdx - m.scroll)

	m.cursor = m.firstSelectableRow()
	m.moveCursor(1)
	if r, ok := m.cursorRow(); !ok || r.kind != rowPlan || r.item.plan.Title == "Plan 00" {
		t.Fatalf("test setup: cursor parked on row=%+v ok=%v, want a plan other than \"Plan 00\"", r, ok)
	}
	belowCursor := m.cursor

	for i := 0; i < 3; i++ {
		cur, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: belowY, Button: tea.MouseLeft})
		if cmd != nil {
			t.Fatalf("click %d below the gate dispatched %T, want nothing", i, cmd())
		}
		m = cur.(*ListModel)
	}
	if m.cursor != belowCursor {
		t.Fatalf("cursor = %d after 3 clicks below the minimum-size gate, want unchanged %d -- "+
			"a click must be refused exactly as a key is", m.cursor, belowCursor)
	}

	// Non-vacuity: resize back above the gate and confirm the SAME click
	// selects a row there, on the SAME model.
	cur, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = cur.(*ListModel)
	if !m.fitsMinimum() {
		t.Fatal("test setup: resizing back up did not clear the gate")
	}

	aboveFrame := strings.Split(m.View().Content, "\n")
	y := frameLineContaining(t, aboveFrame, "Plan 00")
	m.cursor = m.firstSelectableRow()
	m.moveCursor(1)
	aboveStart := m.cursor

	cur, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatalf("click above the gate returned a non-nil cmd: %v, want nil", cmd)
	}
	m = cur.(*ListModel)
	if m.cursor == aboveStart {
		t.Fatal("non-vacuity: the SAME click above the gate must move the cursor, " +
			"or the assertion below the gate proves nothing about the gate")
	}
}

// A "the click does not scroll" assertion is unfalsifiable in this model: the
// click's row bound and this model's margin-less ensureVisible together
// guarantee, by construction, that adding ensureVisible() back after the
// click is always a no-op here. This is the invariant that rule stands in for,
// stated so it CAN fail: after ANY click -- accepted or refused -- the cursor
// sits inside the CURRENT viewport, m.scroll <= m.cursor <
// m.scroll+m.viewHeight(). That is exactly what the click's row bound claims
// to guarantee, and exactly what widening or deleting that bound would
// violate -- but only for a row whose rowIdx would otherwise be SELECTABLE;
// deleting the bound and clicking a row that lands on chrome is still refused
// by rowKind.selectable() alone, discriminating nothing.
//
// EXPANDED MODE AT A NON-ZERO SCROLL WITH ROOM BELOW IT: 20 presses of j (not
// G, which parks the cursor and the viewport at the very LAST selectable row,
// leaving nothing selectable below the window to click on and mistake for
// "inert" for the wrong reason) reach a scroll position with plan rows both
// inside and beyond the current viewport.
//
// FOUR ROWS: the top and bottom edges of the viewport and one in between --
// each an accepted click, verified against the plan drawn there, READ OFF THE
// PAINTED FRAME rather than computed -- plus one row past the bottom edge,
// onto a plan row this hit test must refuse (the click's row bound, not the
// selectable gate) because it is not currently on screen. That last one is
// what a widened or deleted bound reddens.
func TestMouseClickLeavesTheCursorInsideTheViewport(t *testing.T) {
	m := newLoadedList(t, 105)
	m.setMode(listExpanded)

	for i := 0; i < 20; i++ {
		m, _ = pressListKey(m, "j")
	}
	if m.scroll == 0 {
		t.Fatal("test setup: 20 presses of j left scroll at 0 -- want a scrolled viewport so the invariant is non-vacuous")
	}
	scroll, vh := m.scroll, m.viewHeight()
	belowIdx := scroll + vh
	if belowIdx >= len(m.rows) || m.rows[belowIdx].kind != rowPlan {
		t.Fatalf("test setup: rowIdx %d (one row below the viewport) is not a plan row -- "+
			"want headroom below the viewport for the below-viewport case to mean anything", belowIdx)
	}

	for _, tc := range []struct {
		name       string
		row        int // relative to the viewport, mastheadHeight()-offset
		wantInside bool
	}{
		{"top of the viewport", 0, true},
		{"middle of the viewport", vh / 2, true},
		{"bottom of the viewport", vh - 1, true},
		{"one row below the viewport", vh, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cursorBefore := m.cursor
			var y int
			var wantTitle string
			if tc.wantInside {
				rowIdx := scroll + tc.row
				if rowIdx >= len(m.rows) || m.rows[rowIdx].kind != rowPlan {
					t.Fatalf("test setup: row %d (rowIdx %d) is not a plan row inside the scrolled viewport", tc.row, rowIdx)
				}
				wantTitle = m.rows[rowIdx].item.plan.Title
				frame := strings.Split(m.View().Content, "\n")
				y = frameLineContaining(t, frame, wantTitle)
			} else {
				// One row below the viewport's own bottom edge: genuinely
				// off screen, so there is no frame text to search for here.
				// This is the one target in this test placed by arithmetic.
				y = m.mastheadHeight() + tc.row
			}

			cur, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
			if cmd != nil {
				t.Fatalf("click returned a non-nil cmd: %v, want nil", cmd)
			}
			m = cur.(*ListModel)

			if m.cursor < m.scroll || m.cursor >= m.scroll+m.viewHeight() {
				t.Fatalf("cursor = %d after the click, want it inside [m.scroll, m.scroll+viewHeight()) = [%d, %d)",
					m.cursor, m.scroll, m.scroll+m.viewHeight())
			}
			if tc.wantInside {
				it, ok := m.selectedPlan()
				if !ok || it.plan.Title != wantTitle {
					t.Fatalf("selectedPlan() = %+v, ok=%v, want the clicked plan %q", it, ok, wantTitle)
				}
				return
			}
			if m.cursor != cursorBefore {
				t.Fatalf("cursor = %d, want unchanged %d -- a click below the viewport onto an otherwise-selectable "+
					"row must still be refused (the click's own row bound, not rowKind.selectable())", m.cursor, cursorBefore)
			}
		})
	}
}

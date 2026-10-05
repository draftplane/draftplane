package app

import (
	tea "charm.land/bubbletea/v2"
)

// updateMouseWheel is the plan list's wheel routing table -- one file per
// model holds that model's pointer handlers: mouse.go for the review view,
// this file for the list, the wheel here and the click below it.
//
// THE CURSOR, NOT THE SCROLL: browse allocates over a fixed budget
// (rowBudget() = viewHeight() - listSectionChrome, which sectionRows never
// overfills), so len(m.rows) <= viewHeight() always holds there and
// clampScroll pins m.scroll at 0 -- a wheel routed to m.scroll would be inert
// on that screen. TestBrowseRowsNeverExceedTheViewport
// (app/list_test.go) is the committed measurement of it.
//
// listBrowse AND listExpanded ONLY, every other mode inert: a panel (rename,
// confirm-delete) holds the keyboard for its own text or y/n, the filter
// consumes literal characters into a query, and the sort modal claims j/k for
// its own three options -- none of the three has a list body under it for a
// wheel to move a cursor over.
//
// ONE ROW PER EVENT, NEVER A SCALED DELTA: wheelDelta (app/mouse.go) is reused
// rather than reimplemented, since a trackpad flick already arrives as a run of
// individual wheel events. moveCursor is reused unchanged too: it steps over
// section bands and empty-section hints and stops at either end rather than
// running off it, which makes a wheel at either end a no-op, not an error.
//
// THE MINIMUM-SIZE GATE IS NOT HERE: Update's own tea.MouseWheelMsg case
// answers !m.fitsMinimum() before this is called, the same refusal its
// tea.KeyPressMsg case makes for a keystroke. Repeating the check here would
// be a second place that rule could drift from the key gate's own.
func (m *ListModel) updateMouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case listBrowse, listExpanded:
		m.moveCursor(wheelDelta(msg))
	}
	return m, nil
}

// updateMouseClick is the list's click hit test, on app/mouse.go's own
// updateMouseClick precedent: a left click's PRESS selects whichever row it
// lands on, in listBrowse and listExpanded only -- the same two modes the wheel
// drives above, and for the identical reason.
//
// tea.MouseClickMsg IS THE PRESS, not the release: a Shift-bypassed drag -- the
// terminal's own text-selection escape -- leaks a release event with no press
// before it, so a handler keyed off release would move the cursor on a drag the
// reader meant entirely for the terminal, not this program.
//
// A SUBTRACTION, NOT A WALK: unlike the review view, where a ui.Line can occupy
// three frame rows and lineAtFrameRow has to count them, one list row is
// exactly one frame row here -- renderBodyRowPainted truncates every row
// through ansi.Truncate rather than wrapping it, and viewPainted writes exactly
// one "\n" per row it draws. So m.scroll + (y - m.mastheadHeight()) is the
// frame's exact inverse rather than a second guess at it.
//
// m.mastheadHeight() IS THE OFFSET, and naming that function rather than a
// literal is the point: it is the SAME function viewHeight() reserves with, so
// this agrees with the renderer by construction instead of through a second
// constant that merely happens to match it today.
// TestMastheadHeightIsPinnedToFrameRow renders a real frame and asserts
// m.rows[m.scroll] is drawn at frame row mastheadHeight(), so a masthead row
// added later fails there rather than every click landing one row high.
//
// BOTH BOUNDS, and the viewport one is load-bearing: viewPainted draws the
// status and help bars BELOW the body, so on a list longer than the viewport a
// click on either bar computes a row index still inside m.rows -- the
// len(m.rows) bound alone would select a row that is not on screen.
// TestMouseClickOnStatusBarOfALongListIsInert pins it, and deleting this bound
// is what makes that assertion non-vacuous.
//
// rowKind.selectable() IS THE GATE for what a gesture may land on, reused
// rather than reimplemented: a section band and an empty section's own hint are
// pure chrome, so a click there is inert exactly as moveCursor already steps
// over them and cursorOn already refuses to paint a cursor there.
func (m *ListModel) updateMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}
	if m.mode != listBrowse && m.mode != listExpanded {
		return m, nil
	}
	row := msg.Y - m.mastheadHeight()
	if row < 0 || row >= m.viewHeight() {
		return m, nil
	}
	rowIdx := m.scroll + row
	if rowIdx >= len(m.rows) || !m.rows[rowIdx].kind.selectable() {
		return m, nil
	}
	m.cursor = rowIdx
	// NO ensureVisible() -- and here that call would be a no-op BY CONSTRUCTION,
	// not merely by style, which is worth spelling out because the identical rule
	// is load-bearing one file over. This model's ensureVisible carries no
	// scrollMargin (unlike Model.ensureVisible's 3), and the row bound above
	// already puts the cursor inside [m.scroll, m.scroll+m.viewHeight()), which is
	// exactly the interval that bare comparison treats as fully visible. So no
	// test can redden on a reintroduced call; the invariant this rule stands in
	// for is TestMouseClickLeavesTheCursorInsideTheViewport, which a widened or
	// deleted bound above CAN redden.
	return m, nil
}

package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// stampMouseMode is the one place this package sets a tea.View's MouseMode:
// every tea.NewView construction that wants mouse reporting wraps its result
// in a call to this function -- stampMouseMode(tea.NewView(...)) -- rather
// than assigning the field itself, so turning mouse reporting off (or widening
// it) is a one-line edit here instead of several call sites that can drift
// apart. TestEveryNewViewIsStamped (mouse_test.go) is the source walk that
// keeps it that way, and it asserts every site is wrapped rather than
// tolerating an exemption.
//
// tea.MouseModeCellMotion, not tea.MouseModeAllMotion: the wider mode also
// reports bare pointer motion with no button held, which delivers an event per
// cell of travel for no benefit -- CellMotion already gives clicks, releases
// and the wheel.
func stampMouseMode(v tea.View) tea.View {
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// updateMouseWheel is the wheel's whole routing table: modeRead scrolls this
// session's own document by exactly one line (the same handler
// ActScrollDown/ActScrollUp already use); every other mode leaves the event inert,
// because a panel holds the keyboard for its own input and the wheel has nothing
// to say to it.
//
// ONE EVENT, ONE LINE: a wheel message is one tick of the wheel, not a distance, so
// this never scales delta -- a trackpad flick already arrives as a run of individual
// events, and scaling here would double-count its own speed.
func (m *Model) updateMouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if m.mode == modeRead {
		m.scroll += wheelDelta(msg)
		m.clampScroll()
	}
	return m, nil
}

// wheelDelta is the scroll direction a wheel event asks for: +1 for a wheel
// turned down, -1 for up -- the same sign m.scroll already climbs in, so
// callers add it directly rather than translating.
//
// ZERO, NOT A SECOND RETURN VALUE. Every other button this message could carry
// (a horizontal push, or a code this terminal encoding does not use) has no
// scrolling interpretation, and "scroll by nothing" IS that interpretation: an
// ok flag beside the delta gives callers a guard no test can discriminate,
// since deleting it adds 0 to m.scroll and changes nothing.
func wheelDelta(msg tea.MouseWheelMsg) int {
	switch msg.Button {
	case tea.MouseWheelDown:
		return 1
	case tea.MouseWheelUp:
		return -1
	default:
		return 0
	}
}

// docTopRow is the screen row viewPainted's document region begins at:
// viewPainted writes exactly one row of ground before it ever touches
// lines[scroll:scroll+vh], so the document's first line lands one row down
// from the frame's top edge, not on it. Which ui.Line is drawn at a given row
// of that region is lineAtFrameRow's business, and is not subtraction.
//
// PINNED, NOT ASSUMED: TestDocTopRowIsPinnedToFrameRowOne renders a real frame
// and asserts row 1 holds the document's first line, so a masthead row added
// above the document fails there instead of every click landing one row high.
const docTopRow = 1

// lineAtFrameRow maps a row of viewPainted's document region -- 0 is the row
// docTopRow names, counting DOWN THE FRAME -- to the index in m.lines of the Line
// actually drawn there, or false when the row is past the last Line the frame drew.
//
// IT WALKS, AND m.scroll + row IS WRONG. That subtraction rests on "one ui.Line is
// one frame row", which ui.Line's own doc comment records as FALSE: a Line's Text
// can still carry an embedded '\n' and viewPainted writes it out whole, so a heading
// long enough to wrap is ONE Line occupying THREE frame rows and every row below it
// is off by two. The loop mirrors viewPainted's own -- Text then one "\n", so
// lines[i] occupies 1 + strings.Count(Text, "\n") rows -- and when ui makes Line.Text
// one row again this walk collapses to the subtraction on its own.
//
// The caller's viewport bound runs BEFORE this, on the row, so the walk can never
// step past the vh Lines viewPainted drew: each iteration adds at least one row.
func (m *Model) lineAtFrameRow(row int) (int, bool) {
	drawn := 0
	for i := m.scroll; i < len(m.lines); i++ {
		drawn += 1 + strings.Count(m.lines[i].Text, "\n")
		if row < drawn {
			return i, true
		}
	}
	return 0, false
}

// updateMouseClick is the click's whole routing table, and it answers TWO hit
// tests. In modeRead a left click's PRESS lands the cursor on whichever block --
// or thread -- the pointer is over, reading BlockIdx and ThreadIdx straight off
// the Line it landed on (ui/render.go); they are already exactly ui.Cursor's own
// two fields, so no translation happens here beyond finding that Line. In
// modeCompose it presses whichever of the composer's buttons the pointer is over,
// and nothing else -- updateComposeButtonClick below.
//
// tea.MouseClickMsg IS THE PRESS, not the release: a Shift-bypassed drag --
// the terminal's own text-selection escape -- leaks a release event with no
// press before it, so a handler keyed off release would move the cursor on a
// drag the reader meant entirely for the terminal, not this program. That rule
// was written for the cursor and PROTECTS MORE ON THE BUTTON ROW than it does
// here: a leaked release there would post a comment.
//
// modeRead OR modeCompose, and MouseLeft only. The gate named modeRead alone
// at first, because a click must not re-aim the document cursor while a
// panel holds the keyboard. Admitting exactly modeCompose keeps that: the compose
// arm returns before the document walk, so no panel mode re-aims a cursor. A
// LOOSER WIDENING WOULD NOT, and a gate resting on the button geometry's own
// emptiness instead of on the mode would put a post one stale frame away from
// every mode there is -- see updateComposeButtonClick.
func (m *Model) updateMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft || (m.mode != modeRead && m.mode != modeCompose) {
		return m, nil
	}
	if m.mode == modeCompose {
		// ANSWERED HERE AND NEVER BELOW, which is the original gate's first reason
		// kept rather than relaxed. The document rows above the panel are still on
		// screen and still clickable, and falling through would let a click on one
		// of them MOVE m.cursor -- the very field dispatchComposePost reads to
		// choose the block it anchors on. The composer would then post the reader's
		// draft on a block they never opened it over.
		return m.updateComposeButtonClick(msg)
	}
	// BOTH bounds, and the viewport one is load-bearing. viewPainted draws the
	// status and help bars BELOW the document region, so on a document longer
	// than the viewport a click on either bar yields a row still inside m.lines
	// -- the buffer bound alone would select a block that is not on screen.
	//
	// row < 0 IS NOT DEAD CODE. bubbletea v2's mouse coordinates are zero-based,
	// (0,0) upper left, so Y: 0 -- viewPainted's own row of ground -- is an
	// ordinary click a real terminal produces. Without this half, lineAtFrameRow
	// is handed a negative row, which its first Line already satisfies, and the
	// click lands on whatever is at the top of the viewport.
	// TestMouseClickOnGroundRowIsInert pins it.
	row := msg.Y - docTopRow
	if row < 0 || row >= m.viewHeight() {
		return m, nil
	}
	lineIdx, ok := m.lineAtFrameRow(row)
	if !ok {
		// The blank filler below a short document: inert, and specifically not
		// clamped to the last block -- a click below the last line names nothing,
		// it does not name the nearest thing to it.
		return m, nil
	}
	line := m.lines[lineIdx]
	if line.BlockIdx < 0 {
		// The unanchored section's own rows (ui.UnanchoredIdx, -1) are real,
		// on-screen modeRead rows, but no keyboard path ever produces
		// m.cursor == -1: k is a permanent no-op from there, and j sends the
		// reader to block 0 -- teleporting them from the bottom of the document to
		// the top. RULED INERT: supporting cursor == -1 as a real navigable
		// position is a navigation design that must be scoped first, not a guard
		// to "fix" away. TestMouseClickOnUnanchoredSectionIsInert pins it.
		return m, nil
	}
	m.cursor = line.BlockIdx
	m.selected[line.BlockIdx] = line.ThreadIdx
	// THE REPAINT IS THE CLICK'S WHOLE VISIBLE EFFECT -- the rail is painted
	// into m.lines, so without this the reader clicks a paragraph and the cursor
	// moves somewhere only the model can see.
	m.rerender()
	// NO ensureVisible(): the clicked row is on screen by definition, and
	// scrollMargin's context would yank the viewport out from under a click that
	// landed near an edge.
	return m, nil
}

// updateComposeButtonClick is modeCompose's entire share of the click: the two
// controls the composer draws, and no other cell of the screen.
//
// A LEFT PRESS ACTIVATES: [ post ] dispatches the write and [ cancel ]
// throws the draft away, both outright rather than select-then-confirm. The
// point is to remove the reflex misfire of a chord the fingers already know,
// and a pointer that has to travel to an eight-cell target is not that reflex --
// so a second confirming gesture would buy no safety and cost every click one.
//
// THE ROW IS READ, NEVER RECOMPUTED. viewPainted is the only writer of
// m.composeButtons and it stores the row in the coordinate space msg.Y already
// arrives in. composeButtonGeometry carries the whole argument, including the two
// independent drifts -- a ui.Line the frame writes as more than one screen row,
// and the panel header's own word wrap -- that make docTopRow + viewHeight() + 1
// name the wrong row on real documents. Neither is knowable from here, and
// lineAtFrameRow's walk answers only the first.
//
// len(spans) == 0 IS THE ABSENT MARKER, never row == 0: row 0 is a real frame
// row, viewPainted's own band of ground. It is a fact about the LAST FRAME and
// not a mode check, which is why the caller gates on modeCompose as well --
// resolving a click against the spans alone would tie "may this gesture post a
// comment" to render bookkeeping, and the geometry outlives by one frame the mode
// that drew it.
//
// NOTHING IS CLAMPED TO THE NEAREST BUTTON, on this file's own words
// about the blank rows below a short document: a click that names no button names
// nothing, it does not name the nearest thing to it. The gap between the labels
// is two cells and is covered by no span for exactly that reason
// (composeButtonGap) -- a clamp would post because the reader clicked NEAR
// [ post ].
func (m *Model) updateComposeButtonClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	g := m.composeButtons
	if len(g.spans) == 0 || msg.Y != g.row {
		return m, nil
	}
	for _, sp := range g.spans {
		// HALF-OPEN, [col, col+width), which is composeButtonSpan's own contract and
		// not a convention chosen here: the renderer stores the label's cells, so the
		// cell at col+width is the first one that is not the label.
		if msg.X >= sp.col && msg.X < sp.col+sp.width {
			return m.pressComposeButton(sp.focus)
		}
	}
	return m, nil
}

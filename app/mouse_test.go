package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/keymap"
	"github.com/draftplane/draftplane/ui"
)

// stampMouseMode's only visible effect on the tea.View it is given is the
// MouseMode field. want is a copy of the input with MouseMode overwritten by
// hand and nothing else touched, so a stampMouseMode that also cleared, say,
// AltScreen or Content would fail this even though the end-to-end views
// below would not notice.
func TestStampMouseModeChangesOnlyMouseMode(t *testing.T) {
	v := tea.NewView("hello")
	v.AltScreen = true
	want := v
	want.MouseMode = tea.MouseModeCellMotion

	if got := stampMouseMode(v); !reflect.DeepEqual(got, want) {
		t.Fatalf("stampMouseMode(%+v) = %+v, want %+v", v, got, want)
	}
}

// The three real screens -- the review model, the plan list, and the list's
// minimum-size gate -- through their actual View() methods, checking the
// field a terminal actually reads. All three want CellMotion.
// TestEveryNewViewIsStamped is the source-level half of the same rule.
func TestEveryViewTakesTheMouse(t *testing.T) {
	_, review := openReviewOnDoc(t, "# Doc\n\nbody\n")

	list := NewList(nil, keymap.Default(), nil)
	list.width, list.height = 80, 24

	gated := NewList(nil, keymap.Default(), nil)
	gated.width, gated.height = 80, listMinHeight-1
	if gated.fitsMinimum() {
		t.Fatal("test setup: the terminal must be below the minimum-size gate")
	}

	for _, tc := range []struct {
		name string
		view tea.View
	}{
		{"the review model", review.View()},
		{"the plan list", list.View()},
		{"the list's minimum-size gate", gated.View()},
	} {
		if tc.view.MouseMode != tea.MouseModeCellMotion {
			t.Errorf("%s: View().MouseMode = %v, want %v", tc.name, tc.view.MouseMode, tea.MouseModeCellMotion)
		}
	}
}

// A source walk: every tea.NewView call in this package's non-test source
// must be found by parsing, and every one of them must be wrapped in
// stampMouseMode, so a view added later without the wrap fails here instead
// of shipping a screen where the pointer silently dies.
//
// IT MUST STILL FAIL ON AN EMPTY POPULATION: a parser silently returning
// zero sites would pass an empty range just as happily as a correct parse of
// a fully-wrapped package, so the population is checked non-empty below.
//
// It checks direct nesting rather than following stampMouseMode through this
// package's own call graph. Nothing here is registered indirectly -- every
// real site nests the two calls directly -- and a name-following walk has a
// live hazard here: three separate methods are all named View, so a bare-name
// collision could make an unrelated call graph "reach" stampMouseMode with no
// wrap anywhere on the real path. The walk below is answerable from syntax
// alone, and syntax alone is what it uses.
func TestEveryNewViewIsStamped(t *testing.T) {
	sites := findNewViewCalls(t)
	if len(sites) == 0 {
		t.Fatal("no tea.NewView calls were found in this package's source: the parse below is broken, " +
			"and a broken parse would pass this test by having nothing to check")
	}
	for _, s := range sites {
		if !s.wrapped {
			t.Errorf("%s: tea.NewView is not passed directly to stampMouseMode -- "+
				"wrap it as stampMouseMode(tea.NewView(...)) so this view's MouseMode is set", s.pos)
		}
	}
}

// newViewSite is one tea.NewView call found by findNewViewCalls: where it
// is, and whether it is nested directly inside a call to stampMouseMode.
type newViewSite struct {
	pos     string
	wrapped bool
}

// findNewViewCalls parses every non-test .go file in this package's own
// directory and returns one newViewSite per tea.NewView call found in them.
func findNewViewCalls(t *testing.T) []newViewSite {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	fset := token.NewFileSet()
	var sites []newViewSite

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}

		// wrapped collects the position of every tea.NewView call that
		// appears as a direct argument to a stampMouseMode call, keyed by
		// token.Pos (a unique offset within fset, so no string formatting).
		wrapped := map[token.Pos]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			outer, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := outer.Fun.(*ast.Ident)
			if !ok || ident.Name != "stampMouseMode" {
				return true
			}
			for _, arg := range outer.Args {
				if inner, ok := arg.(*ast.CallExpr); ok && isNewViewCall(inner) {
					wrapped[inner.Pos()] = true
				}
			}
			return true
		})

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isNewViewCall(call) {
				return true
			}
			sites = append(sites, newViewSite{
				pos:     fset.Position(call.Pos()).String(),
				wrapped: wrapped[call.Pos()],
			})
			return true
		})
	}
	return sites
}

// isNewViewCall reports whether call is syntactically tea.NewView(...).
func isNewViewCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "NewView" {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "tea"
}

// modeRead reuses ActScrollDown/ActScrollUp's own effect, one line per
// event. Cursor and selection are checked alongside scroll rather than
// scroll alone: routing the wheel to moveCursor instead would move m.cursor
// (and, off the current block's line, m.selected too), and moveCursor's own
// ensureVisible would make a scroll-only assertion pass under that mutation.
func TestMouseWheelInReadModeScrollsWithoutMovingCursorOrSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		button tea.MouseButton
		n      int
		want   int
	}{
		{"wheel down moves N lines", tea.MouseWheelDown, 5, 5},
		{"wheel up moves N lines the other way", tea.MouseWheelUp, 3, -3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := longDocFixture(t)
			m := openModel(t, f)
			if m.mode != modeRead {
				t.Fatalf("test setup: mode = %v, want modeRead", m.mode)
			}

			// Parked away from both clamp edges, so clampScroll never
			// silently absorbs the delta this test means to observe.
			m.scroll = 20
			m.clampScroll()
			if m.scroll != 20 {
				t.Fatalf("test setup: scroll clamped to %d immediately, need more headroom in the document", m.scroll)
			}
			cursor := m.CursorBlock()
			wantSelected := maps.Clone(m.selected)

			for i := 0; i < tc.n; i++ {
				cur, cmd := m.Update(tea.MouseWheelMsg{Button: tc.button})
				if cmd != nil {
					t.Fatalf("wheel event %d returned a non-nil cmd: %v, want nil", i, cmd)
				}
				m = cur.(*Model)
			}

			if got := m.scroll - 20; got != tc.want {
				t.Fatalf("scroll moved by %d, want %d", got, tc.want)
			}
			if m.CursorBlock() != cursor {
				t.Fatalf("cursor = %d, want unchanged %d -- the wheel must not move the cursor", m.CursorBlock(), cursor)
			}
			if !reflect.DeepEqual(m.selected, wantSelected) {
				t.Fatalf("selected = %v, want unchanged %v -- the wheel must not touch thread selection", m.selected, wantSelected)
			}
		})
	}
}

// The other half of that rule: the wheel drives read mode alone, covered by
// its own test elsewhere in this file, and every other mode leaves it inert.
// THE SET IS DRIVEN FROM THE ENUM, so a mode added later is covered automatically rather
// than needing to be added to a hand-picked list here.
func TestMouseWheelIsInertOutsideRead(t *testing.T) {
	f := longDocFixture(t)
	m := openModel(t, f)

	for md := mode(0); md < modeCount; md++ {
		if md == modeRead {
			continue
		}
		m.mode = md
		m.scroll = 7
		cursor := m.CursorBlock()
		wantSelected := maps.Clone(m.selected)

		for _, button := range []tea.MouseButton{tea.MouseWheelDown, tea.MouseWheelUp} {
			cur, cmd := m.Update(tea.MouseWheelMsg{Button: button})
			if cmd != nil {
				t.Errorf("mode %d: wheel event returned a non-nil cmd: %v, want nil", md, cmd)
			}
			m = cur.(*Model)
		}

		if m.scroll != 7 {
			t.Errorf("mode %d: scroll = %d, want unchanged 7 -- the wheel is inert outside modeRead", md, m.scroll)
		}
		if m.CursorBlock() != cursor {
			t.Errorf("mode %d: cursor = %d, want unchanged %d", md, m.CursorBlock(), cursor)
		}
		if !reflect.DeepEqual(m.selected, wantSelected) {
			t.Errorf("mode %d: selected = %v, want unchanged %v", md, m.selected, wantSelected)
		}
	}
}

// docTopRow's own pin: viewPainted writes one row of ground before it ever
// touches the document's own line buffer, so the document's first line lands
// on frame row 1, not row 0. If a masthead row is ever added above the
// document, this fails here rather than sending updateMouseClick's
// arithmetic one row high with nothing to say so.
func TestDocTopRowIsPinnedToFrameRowOne(t *testing.T) {
	f := setup(t)
	m := openModel(t, f)

	rows := strings.Split(m.View().Content, "\n")
	if len(rows) <= docTopRow {
		t.Fatalf("test setup: frame has %d rows, want more than docTopRow (%d)", len(rows), docTopRow)
	}
	if got, want := ansi.Strip(rows[docTopRow]), ansi.Strip(m.lines[0].Text); got != want {
		t.Fatalf("frame row %d = %q, want the document's own first line %q", docTopRow, got, want)
	}
}

// railGlyph is the cursor mark ui/painted.go's cursorGlyph draws in the
// gutter of every row of the focused block. Spelled out here because that
// closure is unexported and local to a render function.
const railGlyph = "┃"

// blockContaining finds a block by its rendered text rather than by a
// hand-counted index that would silently drift with the fixture.
func blockContaining(t *testing.T, m *Model, want string) int {
	t.Helper()
	for i, b := range m.blocks {
		if strings.Contains(b.Text, want) {
			return i
		}
	}
	t.Fatalf("test setup: no block contains %q", want)
	return -1
}

// frameY converts a line buffer index into the screen row updateMouseClick's
// own inverse mapping must land back on, given the model's current scroll.
// It counts rows the way the frame does: a ui.Line whose Text carries an
// embedded newline is drawn as more than one row, so this accumulates rather
// than subtracting.
//
// IT MIRRORS THE IMPLEMENTATION, WHICH IS WHY IT CANNOT BE THE TEST OF IT.
// Every caller here uses it to reach a row it has already located some other
// way; the mapping itself is proved by the wrapped-heading test below, which
// reads its row off the PAINTED FRAME and so is independent of this helper.
func frameY(m *Model, lineIdx int) int {
	rows := 0
	for i := m.scroll; i < lineIdx; i++ {
		rows += 1 + strings.Count(m.lines[i].Text, "\n")
	}
	return docTopRow + rows
}

// A click on an ordinary paragraph row lands the cursor on that block and
// clears the thread focus to the line -- ui.NoThread, exactly the value the
// clicked Line already carried as its own ThreadIdx.
//
// THE RIGHT-CLICK CASE IS FOLDED IN HERE: the same fixture already proves
// the left-click half moves the cursor, so a right-click at the identical y
// is what proves msg.Button != tea.MouseLeft is load-bearing rather than
// vacuous -- dropping it would let this case land on the block too.
//
// AND THE RAIL IS ASSERTED, NOT JUST THE MODEL: deleting m.rerender() from
// updateMouseClick left the entire app package green, because every click
// test here read m.cursor and m.selected -- which the mutation leaves
// exactly right -- and nothing read the buffer the reader actually looks at.
// The rail is painted INTO m.lines, so it is only visible as a glyph on the
// clicked block's own line. That assertion belongs to the case that LANDS:
// the right-click case is inert, so the rail there is still wherever the
// previous subtest left it, and asserting on it would be asserting on stale
// paint.
func TestMouseClickOnBodyRowSelectsThatBlockWithNoThread(t *testing.T) {
	f := setup(t)
	m := openModel(t, f)

	block := blockContaining(t, m, "unbounded")
	lineIdx := ui.FirstLineOfFocus(m.lines, ui.OnLine(block))
	y := frameY(m, lineIdx)

	for _, tc := range []struct {
		name       string
		button     tea.MouseButton
		wantCursor int
		wantSel    int
		wantRail   bool
	}{
		{"left selects the block", tea.MouseLeft, block, ui.NoThread, true},
		{"right is inert (MouseLeft only)", tea.MouseRight, 0, 7, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Parked on the wrong block and thread first, so the assertions
			// below prove the click MOVED them rather than merely finding them
			// already there -- m.selected's own zero value is 0, a real thread
			// index, exactly what a click that silently no-oped would leave.
			m.cursor = 0
			m.selected[block] = 7

			cur, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tc.button})
			if cmd != nil {
				t.Fatalf("click returned a non-nil cmd: %v, want nil", cmd)
			}
			m = cur.(*Model)

			if m.cursor != tc.wantCursor {
				t.Fatalf("cursor = %d, want %d", m.cursor, tc.wantCursor)
			}
			if got := m.selected[block]; got != tc.wantSel {
				t.Fatalf("selected[%d] = %d, want %d", block, got, tc.wantSel)
			}
			if !tc.wantRail {
				return
			}
			// THE MUTATION THIS KILLS: delete m.rerender() from
			// updateMouseClick and m.lines still carries the rail on the
			// block the cursor left, so the line the model now points at has
			// no cursor glyph on it at all.
			got := ansi.Strip(m.lines[ui.FirstLineOfFocus(m.lines, m.docCursor())].Text)
			if !strings.Contains(got, railGlyph) {
				t.Fatalf("the clicked block's own line does not carry the rail glyph %q: %q -- "+
					"the click moved the cursor without repainting, so the reader sees the rail "+
					"on the block they clicked away from", railGlyph, got)
			}
		})
	}
}

// The one case the rest of this file could not see: every other fixture here
// happens to hold a buffer where one ui.Line is one frame row, so
// "lineIdx := m.scroll + (y - docTopRow)" and a row walk agree everywhere and
// neither can be told from the other.
//
// THE PREMISE IS FALSE AND ui SAYS SO ITSELF: a Line's Text can still carry
// an embedded '\n' -- unwrapped headings, mostly -- and app/painted.go writes
// that Text out whole, so ONE Line lands as THREE frame rows here. From the
// row below it the subtraction is off by two: a click on the row displaying
// the next heading put the cursor on the block AFTER it, which is "acts on
// something the screen does not point at".
//
// EVERY ROW IS READ OFF THE PAINTED FRAME, never computed. frameY deliberately
// mirrors lineAtFrameRow, so a fixture that used it would agree with the
// implementation whichever mapping the implementation used; asking the frame
// what it drew is the only question that is independent of both.
func TestMouseClickBelowAWrappedHeadingLandsOnTheBlockDrawnThere(t *testing.T) {
	// Long enough to wrap at the fixture's width 100: the heading arm neither
	// wraps nor truncates, so lipgloss's own Width().Render breaks it INSIDE
	// one ui.Line.
	const heading = "Why the wheel scrolls the page by exactly one line per event, why a click selects " +
		"the block the reader pointed at rather than the one the arithmetic guessed, and why " +
		"every number here was measured rather than reasoned"
	_, m := openReviewOnDoc(t, "# "+heading+"\n\n## Coordinates are exact\n\nbody paragraph here\n")

	multi := -1
	for i, l := range m.lines {
		if strings.Contains(l.Text, "\n") {
			if multi >= 0 {
				t.Fatalf("test setup: lines %d and %d both carry an embedded newline; this fixture means to hold exactly one", multi, i)
			}
			multi = i
		}
	}
	if multi < 0 {
		t.Fatal("test setup: no ui.Line in this buffer carries an embedded newline -- " +
			"without a genuinely multi-row Line nothing here can tell a row walk from m.scroll+row")
	}
	if rows := 1 + strings.Count(m.lines[multi].Text, "\n"); rows < 3 {
		t.Fatalf("test setup: the wrapped heading is %d frame rows inside one ui.Line, want at least 3 -- "+
			"at 2 the naive arithmetic is only one index out, which lands on the SAME block's trailing "+
			"blank line and discriminates nothing", rows)
	}

	frame := strings.Split(m.View().Content, "\n")
	rowOf := func(t *testing.T, drawn string) int {
		t.Helper()
		found := -1
		for i, r := range frame {
			if strings.Contains(ansi.Strip(r), drawn) {
				if found >= 0 {
					t.Fatalf("test setup: %q is drawn on frame rows %d and %d -- cannot name one row by it", drawn, found, i)
				}
				found = i
			}
		}
		if found < 0 {
			t.Fatalf("test setup: no row of the painted frame draws %q", drawn)
		}
		return found
	}

	headingBlock := blockContaining(t, m, heading)
	secondBlock := blockContaining(t, m, "Coordinates are exact")
	bodyBlock := blockContaining(t, m, "body paragraph here")

	// WHAT THIS TEST DISCRIMINATES, asserted rather than assumed: on the row
	// that draws the second heading, the old arithmetic must name something
	// OTHER than that heading's block. -2 stands for "off the end of the
	// buffer", a value no Line's BlockIdx can equal (ui.UnanchoredIdx is -1).
	naiveBlock := -2
	if naive := m.scroll + (rowOf(t, "Coordinates are exact") - docTopRow); naive >= 0 && naive < len(m.lines) {
		naiveBlock = m.lines[naive].BlockIdx
	}
	if naiveBlock == secondBlock {
		t.Fatalf("test setup: m.scroll + (y - docTopRow) already names block %d on the second heading's own row -- "+
			"the two mappings agree on this fixture, so it proves nothing", secondBlock)
	}

	for _, tc := range []struct {
		name, drawn string
		wantBlock   int
	}{
		{"the wrapped heading's own last visual row", "measured rather than reasoned", headingBlock},
		{"the row that draws the next heading", "Coordinates are exact", secondBlock},
		{"the body paragraph below it", "body paragraph here", bodyBlock},
	} {
		t.Run(tc.name, func(t *testing.T) {
			y := rowOf(t, tc.drawn)

			// Parked on a different block first, so a pass means the click
			// MOVED the cursor rather than finding it already right.
			m.cursor = (tc.wantBlock + 1) % len(m.blocks)

			cur, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
			if cmd != nil {
				t.Fatalf("click returned a non-nil cmd: %v, want nil", cmd)
			}
			m = cur.(*Model)

			if m.cursor != tc.wantBlock {
				t.Fatalf("a click on frame row %d, which draws %q, put the cursor on block %d; want %d -- "+
					"the hit test is counting ui.Lines where the frame counts ROWS", y, tc.drawn, m.cursor, tc.wantBlock)
			}
		})
	}
}

// TWO threads sit on one block, so a click that merely landed on the BLOCK
// and defaulted the selection to thread 0 would still pass a single-thread
// version of this test. The click here targets the SECOND thread by its own
// Line, and r must follow it there.
func TestMouseClickOnCommentCardSelectsThatThreadForReply(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "thread A")
	addThread(t, f, "thread B")
	m := openModel(t, f)

	block := blockContaining(t, m, "token bucket")
	wantID := threadIDByHead(t, f, m.sess.Plan.ID, "thread B")
	vi := -1
	for i, v := range m.views[block] {
		if v.Thread.ID == wantID {
			vi = i
		}
	}
	if vi < 0 {
		t.Fatalf("test setup: thread B is not projected onto block %d: %+v", block, m.views[block])
	}

	m.expanded[block] = true
	m.rerender()
	lineIdx := ui.FirstLineOfFocus(m.lines, ui.OnThread(block, vi))
	if line := m.lines[lineIdx]; line.BlockIdx != block || !line.IsThread || line.ThreadIdx != vi {
		t.Fatalf("test setup: line %d is %+v, want thread B's own card row", lineIdx, line)
	}
	y := frameY(m, lineIdx)

	m.cursor = 0
	delete(m.selected, block)

	cur, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatalf("click returned a non-nil cmd: %v, want nil", cmd)
	}
	m = cur.(*Model)

	if m.cursor != block {
		t.Fatalf("cursor = %d, want %d", m.cursor, block)
	}
	if got := m.selected[block]; got != vi {
		t.Fatalf("selected[%d] = %d, want %d (thread B)", block, got, vi)
	}

	m = press(m, "r")
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose after r", m.mode)
	}
	if m.replyTo != wantID.String() {
		t.Fatalf("replyTo = %q, want %q (thread B, the clicked card) -- r targeted the wrong thread", m.replyTo, wantID.String())
	}
}

// borderRuneSet is every rune lipgloss.NormalBorder() can draw, recomputed
// from the same exported building block rather than reaching past the
// package boundary for ui's own unexported borderRunesOf.
func borderRuneSet() map[rune]bool {
	b := lipgloss.NormalBorder()
	set := make(map[rune]bool, 8)
	for _, part := range []string{
		b.Top, b.Bottom, b.Left, b.Right,
		b.TopLeft, b.TopRight, b.BottomLeft, b.BottomRight,
		b.MiddleLeft, b.MiddleRight, b.Middle, b.MiddleTop, b.MiddleBottom,
	} {
		for _, r := range part {
			set[r] = true
		}
	}
	return set
}

// isBorderOnlyLine reports whether text, once ansi.Strip'd and its
// surrounding gutter/margin padding trimmed, is drawn from border runes and
// nothing else -- how this file's fixtures confirm a line really is the rule
// they mean to click, rather than assuming a row count. Unlike ui's own
// borderOnlyLine, this runs against the fully painted app-level Line, which
// carries the gutter and rowWidth's own right-hand padding around the border
// it drew -- both spaces, and TrimSpace is what keeps them from reading as
// "not a border".
func isBorderOnlyLine(text string) bool {
	plain := strings.TrimSpace(ansi.Strip(text))
	if plain == "" {
		return false
	}
	runes := borderRuneSet()
	for _, r := range plain {
		if !runes[r] {
			return false
		}
	}
	return true
}

// gridRowGroup (ui/painted.go) rules that "the closing border belongs to the
// row it closes": a table's header-to-body separator is the rule that CLOSES
// the header row's own group, not one that OPENS the data row's, and it
// carries the header row's BlockIdx accordingly. A hit test that assumed a
// border "belongs" to whichever row it is visually adjacent to below would
// land this click on the data row instead -- exactly backwards.
func TestMouseClickOnGridBorderRowSelectsTheRowItCloses(t *testing.T) {
	_, m := openReviewOnDoc(t, "# Doc\n\n| Col |\n| --- |\n| val |\n")

	header := blockContaining(t, m, "Col")
	data := blockContaining(t, m, "val")

	var headerLines []int
	for i, l := range m.lines {
		if l.BlockIdx == header {
			headerLines = append(headerLines, i)
		}
	}
	if len(headerLines) < 2 {
		t.Fatalf("test setup: header row block %d has %d screen lines, want at least 2 (content plus its closing rule)", header, len(headerLines))
	}
	borderRow := headerLines[len(headerLines)-1]
	contentFound := false
	for _, i := range headerLines {
		if strings.Contains(ansi.Strip(m.lines[i].Text), "Col") {
			contentFound = true
			if isBorderOnlyLine(m.lines[i].Text) {
				t.Fatalf("test setup: line %d carries the header's own text %q yet reads as border-only -- isBorderOnlyLine is unreliable here", i, ansi.Strip(m.lines[i].Text))
			}
		}
	}
	if !contentFound {
		t.Fatalf("test setup: no line of header block %d's %d screen lines carries its own text %q", header, len(headerLines), "Col")
	}
	if !isBorderOnlyLine(m.lines[borderRow].Text) {
		t.Fatalf("test setup: line %d is not border-only text, cannot exercise the row-close case: %q", borderRow, ansi.Strip(m.lines[borderRow].Text))
	}
	y := frameY(m, borderRow)

	m.cursor = data

	cur, cmd := m.Update(tea.MouseClickMsg{X: 2, Y: y, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatalf("click returned a non-nil cmd: %v, want nil", cmd)
	}
	m = cur.(*Model)

	if m.cursor != header {
		t.Fatalf("cursor = %d, want %d (the header row this border closes) -- landed on %d instead", m.cursor, header, data)
	}
}

// The status-bar case, with its own non-vacuity note: on a SHORT document
// the buffer bound alone (lineAtFrameRow finding no Line there) would already
// catch this click, so the case has to run on a document longer than the
// viewport for the viewport bound to be what is actually under test.
func TestMouseClickBelowDocumentOnLongDocumentIsInert(t *testing.T) {
	f := longDocFixture(t)
	m := openModel(t, f)
	m.width, m.height = 100, 20
	m.rerender()

	vh := m.viewHeight()
	if len(m.lines) <= vh {
		t.Fatalf("test setup: document has %d lines, want more than the %d-line viewport", len(m.lines), vh)
	}

	y := docTopRow + vh // the status bar's own row, per viewPainted's layout
	if lineIdx, ok := m.lineAtFrameRow(y - docTopRow); !ok {
		t.Fatalf("test setup: the status-bar row falls past the %d Lines this buffer draws already (%d) -- "+
			"this case no longer discriminates the viewport bound", len(m.lines), lineIdx)
	}

	cursor, scroll, selected := m.cursor, m.scroll, maps.Clone(m.selected)

	cur, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatalf("click returned a non-nil cmd: %v, want nil", cmd)
	}
	m = cur.(*Model)

	if m.cursor != cursor {
		t.Fatalf("cursor = %d, want unchanged %d -- a click on the status bar must be inert", m.cursor, cursor)
	}
	if m.scroll != scroll {
		t.Fatalf("scroll = %d, want unchanged %d", m.scroll, scroll)
	}
	if !reflect.DeepEqual(m.selected, selected) {
		t.Fatalf("selected = %v, want unchanged %v", m.selected, selected)
	}
}

// The other half of that rule: THE DOCUMENT hit test is modeRead's alone, and
// every other mode refuses it -- driven from the mode enum rather than a
// hand-picked list so a mode added later is covered automatically. The click
// lands on a real body row -- the same y the body-row test above proves moves
// the cursor in modeRead -- so a mode that forgot to gate would be caught
// landing it, not merely missing an unrelated row.
//
// modeCompose NOW PASSES THE GATE AND STILL FAILS TO LAND HERE, which is the
// point rather than a gap: the gate widened to modeRead OR modeCompose, and
// the compose arm answers its own button row and returns before this walk.
// The model in this loop has drawn no compose frame, so its geometry is
// empty and that case is vacuous HERE by construction --
// TestMouseClickOffTheComposeButtonsIsInert is where a real button row proves a
// document click under an open composer is inert.
func TestMouseClickIsInertOutsideModeRead(t *testing.T) {
	f := setup(t)
	m := openModel(t, f)

	block := blockContaining(t, m, "unbounded")
	y := frameY(m, ui.FirstLineOfFocus(m.lines, ui.OnLine(block)))

	for md := mode(0); md < modeCount; md++ {
		if md == modeRead {
			continue
		}
		m.mode = md
		m.cursor = 0
		delete(m.selected, block)
		cursor, selected := m.cursor, maps.Clone(m.selected)

		cur, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
		if cmd != nil {
			t.Errorf("mode %d: click returned a non-nil cmd: %v, want nil", md, cmd)
		}
		m = cur.(*Model)

		if m.cursor != cursor {
			t.Errorf("mode %d: cursor = %d, want unchanged %d", md, m.cursor, cursor)
		}
		if !reflect.DeepEqual(m.selected, selected) {
			t.Errorf("mode %d: selected = %v, want unchanged %v", md, m.selected, selected)
		}
	}
}

// A click must never scroll. A "scroll did not move" check is vacuous unless
// the clicked row sits within scrollMargin (3) of a viewport edge, because
// ensureVisible would not have scrolled anywhere else either. The fixture scrolls a multi-row
// comment card's own HEADER line one row above the viewport, so the focus
// the click lands (the card's second row, still fully visible) has a
// FirstLineOfFocus that sits above m.scroll -- the one shape that can make
// ensureVisible move anything -- and clicks it at viewport row 0, inside the
// margin. Adding m.ensureVisible() back to updateMouseClick fails it.
func TestMouseClickDoesNotScroll(t *testing.T) {
	f := longDocFixture(t)
	seedThreadOn(t, f, "Long Doc", "Paragraph content for section 30",
		"a reply long enough that its own card spans more than one screen row, so the row a click lands on is not the row ensureVisible would scroll to")
	m := openModel(t, f)
	m.width, m.height = 100, 20
	m.rerender()

	block := blockContaining(t, m, "Paragraph content for section 30")
	if n := len(m.views[block]); n != 1 {
		t.Fatalf("test setup: block %d has %d threads, want 1", block, n)
	}
	m.expanded[block] = true
	m.rerender()

	var cardLines []int
	for i, l := range m.lines {
		if l.BlockIdx == block && l.IsThread && l.ThreadIdx == 0 {
			cardLines = append(cardLines, i)
		}
	}
	if len(cardLines) < 2 {
		t.Fatalf("test setup: thread card is %d screen lines, want at least 2 so its header can scroll off separately from its body", len(cardLines))
	}
	cardHeader, cardBody := cardLines[0], cardLines[1]

	// The card's header sits one row above the viewport; its body -- still
	// part of the same thread's focus -- is the viewport's own top row.
	m.scroll = cardHeader + 1
	if m.scroll != cardBody {
		t.Fatalf("test setup: scroll %d does not put the card's body line %d at the viewport's own top row", m.scroll, cardBody)
	}
	// The click below lands at Y = docTopRow, viewport row 0 -- inside
	// scrollMargin (3) of the top edge by construction, which is what makes
	// the assertion below non-vacuous.
	wantScroll := m.scroll

	m.cursor = 0
	delete(m.selected, block)

	cur, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: docTopRow, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatalf("click returned a non-nil cmd: %v, want nil", cmd)
	}
	m = cur.(*Model)

	// The click did land -- proving what follows is the no-scroll rule
	// holding, not the handler having done nothing at all.
	if m.cursor != block || m.selected[block] != 0 {
		t.Fatalf("test setup: click did not select block %d thread 0 (cursor=%d selected=%v) -- cannot exercise the no-scroll rule without it landing first", block, m.cursor, m.selected[block])
	}

	if m.scroll != wantScroll {
		t.Fatalf("scroll = %d, want unchanged %d -- a click must never scroll", m.scroll, wantScroll)
	}
}

// The row < 0 half of the viewport bound. bubbletea v2's mouse coordinates
// are zero-based, (0,0) upper left, so Y: 0 is an ordinary click on
// viewPainted's own row of ground above the document -- one row above
// docTopRow -- not a synthetic corner case. Without the row < 0 guard,
// lineAtFrameRow is handed row -1, which its very first Line already
// satisfies, and the click silently selects whatever sits at the top of the
// viewport. That mutation shows up in the SELECTION map, which is why this
// asserts on it and not on the cursor alone (the cursor may well already be
// on that block).
func TestMouseClickOnGroundRowIsInert(t *testing.T) {
	f := setup(t)
	m := openModel(t, f)

	cursor, scroll, selected := m.cursor, m.scroll, maps.Clone(m.selected)

	cur, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: 0, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatalf("click returned a non-nil cmd: %v, want nil", cmd)
	}
	m = cur.(*Model)

	if m.cursor != cursor {
		t.Fatalf("cursor = %d, want unchanged %d -- a click on the ground row must be inert", m.cursor, cursor)
	}
	if m.scroll != scroll {
		t.Fatalf("scroll = %d, want unchanged %d", m.scroll, scroll)
	}
	if !reflect.DeepEqual(m.selected, selected) {
		t.Fatalf("selected = %v, want unchanged %v", m.selected, selected)
	}
}

// The unanchored section's own rows carry BlockIdx == ui.UnanchoredIdx (-1),
// a real on-screen modeRead row, but no keyboard path ever produces
// m.cursor == -1: landing the cursor there is a trap (k a permanent no-op, j
// teleporting to block 0) rather than a state worth supporting. A click there
// must leave the cursor and selection exactly as they were.
func TestMouseClickOnUnanchoredSectionIsInert(t *testing.T) {
	f := setup(t)
	seedOrphan(t, f, "an orphaned comment")
	m := openModel(t, f)

	unanchoredLine := -1
	for i, l := range m.lines {
		if l.BlockIdx == ui.UnanchoredIdx {
			unanchoredLine = i
			break
		}
	}
	if unanchoredLine < 0 {
		t.Fatalf("test setup: no line in m.lines carries BlockIdx == ui.UnanchoredIdx -- the orphan did not project onto the unanchored section")
	}
	y := frameY(m, unanchoredLine)

	cursor, selected := m.cursor, maps.Clone(m.selected)

	cur, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatalf("click returned a non-nil cmd: %v, want nil", cmd)
	}
	m = cur.(*Model)

	if m.cursor != cursor {
		t.Fatalf("cursor = %d, want unchanged %d -- a click on the unanchored section must be inert", m.cursor, cursor)
	}
	if !reflect.DeepEqual(m.selected, selected) {
		t.Fatalf("selected = %v, want unchanged %v", m.selected, selected)
	}
}

// screenButton is one of the composer's controls as the PAINTED FRAME drew it:
// the columns the label actually occupies. mid is where a pointer aimed at a
// control lands; past is the first column that is NOT the control, which is the
// near miss the hit test has to refuse rather than clamp.
type screenButton struct {
	col, width int
}

func (b screenButton) mid() int  { return b.col + b.width/2 }
func (b screenButton) past() int { return b.col + b.width }

// composeButtonsOnScreen reads the composer's button row off the PAINTED FRAME:
// the frame row the labels were drawn on, and the columns each one occupies.
// Every click below aims with what this answers and never with m.composeButtons,
// because the stored geometry is the thing under test -- a fixture that asked the
// model where to click would agree with the hit test whatever either of them
// believed, and both could be wrong together.
//
// DRAWING THE FRAME IS ALSO WHAT COMPLETES THE GEOMETRY, viewPainted being its
// only writer, and that is the production order rather than a convenience:
// bubbletea draws after every Update, so a click always arrives against a frame
// that has already been painted.
//
// KEYED BY THE RING STOP AND NOT BY THE LABEL, and both come from composeButtonBar
// rather than from literals here. A label renamed in the renderer would leave a
// literal-keyed lookup answering the zero column silently; the focus constants
// cannot go missing that way.
func composeButtonsOnScreen(t *testing.T, m *Model) (int, map[composeFocus]screenButton) {
	t.Helper()
	rows := strings.Split(m.View().Content, "\n")
	row := -1
	for i, r := range rows {
		if !strings.Contains(ansi.Strip(r), composeButtonBar[0].label) {
			continue
		}
		if row >= 0 {
			t.Fatalf("test setup: %q is drawn on frame rows %d and %d -- cannot name one row by it",
				composeButtonBar[0].label, row, i)
		}
		row = i
	}
	if row < 0 {
		t.Fatalf("test setup: no row of the painted frame draws %q", composeButtonBar[0].label)
	}
	stripped := ansi.Strip(rows[row])
	buttons := make(map[composeFocus]screenButton, len(composeButtonBar))
	for _, b := range composeButtonBar {
		col := strings.Index(stripped, b.label)
		if col < 0 {
			t.Fatalf("test setup: the button row %q does not draw %q", stripped, b.label)
		}
		// ansi.StringWidth and not len: the same measure the renderer used to lay
		// the row out, so a label that stopped being pure ASCII moves the target
		// with the glyphs instead of leaving this a byte count.
		buttons[b.focus] = screenButton{col: col, width: ansi.StringWidth(b.label)}
	}
	return row, buttons
}

// assertComposeStillOpen is what "this click was inert" means on the compose
// panel, and it is four checks rather than one because the things a click here
// could wrongly do fail differently: a stray POST leaves modeRead, a stray CANCEL
// leaves modeRead with the draft gone, and a click that fell through to the
// DOCUMENT walk beneath the panel leaves the composer open with the cursor moved
// to whatever the pointer was over. The last is the one an assertion on the mode
// alone would miss entirely, and it is the case that matters most: it
// is m.cursor that dispatchComposePost reads to pick the block it anchors on.
//
// m.sess.Exists IS A BACKSTOP AND NOT THE PRIMARY DETECTOR. Every caller asserts
// on the returned cmd first, and today's post does its write in a goroutine that
// only a drain would run, so this can catch nothing that assertion has not
// already caught. It is cheap and it is here for the day a write becomes
// synchronous: the fixture openComposeOverAWrappingHeading gives every caller is
// PLAN-LESS, so the first comment on it has to create the plan to hold it and a
// landed write is visible without a plan id to ask threads for.
func assertComposeStillOpen(t *testing.T, m *Model, draft string, cursor int) {
	t.Helper()
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose -- the click pressed a button it did not land on", m.mode)
	}
	if got := m.ta.Value(); got != draft {
		t.Fatalf("ta = %q, want the draft %q intact", got, draft)
	}
	if m.sess.Exists {
		t.Fatal("a plan was created: the click dispatched a write")
	}
	if m.cursor != cursor {
		t.Fatalf("cursor = %d, want unchanged %d -- the click fell through to the document hit test under the panel", m.cursor, cursor)
	}
}

// A left click on a compose button PRESSES it: [ post ] dispatches the write and
// [ cancel ] throws the draft away, both outright, neither a select-then-confirm.
// That is deliberate, and it is why the cancel case asserts no plan was
// created rather than merely that the composer closed.
//
// THE FIXTURE IS THE POINT OF THIS TEST. composeWrappingHeadingDoc's drawn window
// carries a ui.Line the frame writes as two rows, so the row the buttons land on
// is NOT the row docTopRow + viewHeight() + 1 names -- and every other document in
// this package is one where those two agree. A hit test that recomputed the row
// passes the whole existing mouse suite and misses the button in a real terminal,
// which is why the disagreement is asserted below instead of assumed.
//
// THE TWO EDGE CELLS ARE HITS, AND THEY ARE HERE BECAUSE AIMING AT THE MIDDLE
// CANNOT SAY SO. A span is half-open, [col, col+width), so the label's opening
// '[' and its closing ']' are both the reader's to click -- and a hit test
// written msg.X > sp.col, or msg.X < sp.col+sp.width-1, passes every other case
// in this file while leaving one drawn cell of each button dead. Together with
// TestMouseClickOffTheComposeButtonsIsInert's col-1 and col+width aims, the two
// cells either side of both of POST's boundaries are now spelled out rather than
// interpolated -- post's alone, because the hit test applies ONE comparison to
// every span, so a boundary answered right for one label is answered right for
// both. This direction fails SAFE -- a dead cell is a click that does nothing,
// never a click that posts -- which is why it is pinned with two rows on the
// test that already exists and not a test of its own.
func TestMouseClickOnAComposeButtonPressesIt(t *testing.T) {
	// postLanded is shared by the three post rows below rather than written into
	// each: what changes across them is WHERE the pointer landed, and a check
	// copied three times would let one copy weaken without the others.
	postLanded := func(t *testing.T, f fixture, m *Model) {
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
	}
	for _, tt := range []struct {
		name  string
		focus composeFocus
		aim   func(screenButton) int
		check func(t *testing.T, f fixture, m *Model)
	}{
		{
			name:  "post dispatches the write",
			focus: composeFocusPost,
			aim:   screenButton.mid,
			check: postLanded,
		},
		{
			name:  "post's own first cell, the label's opening [",
			focus: composeFocusPost,
			aim:   func(b screenButton) int { return b.col },
			check: postLanded,
		},
		{
			name:  "post's own last cell, the label's closing ]",
			focus: composeFocusPost,
			aim:   func(b screenButton) int { return b.col + b.width - 1 },
			check: postLanded,
		},
		{
			name:  "cancel throws the draft away",
			focus: composeFocusCancel,
			aim:   screenButton.mid,
			check: func(t *testing.T, f fixture, m *Model) {
				if got := m.ta.Value(); got != "" {
					t.Fatalf("ta = %q, want the draft dropped", got)
				}
				// The fixture is plan-less, so "nothing was written" is observable
				// without a plan id to ask threads for: a cancel that had dispatched
				// would have created the plan to hold the comment.
				if m.sess.Exists {
					t.Fatal("cancel created a plan: the click dispatched a write")
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f, m := openComposeOverAWrappingHeading(t)
			m = press(m, "h", "i")
			row, buttons := composeButtonsOnScreen(t, m)

			// THE DISCARDED FORMULA MUST NAME A DIFFERENT ROW HERE, or this fixture
			// measures nothing: on a short-heading document it names the button row
			// exactly, and a hit test built on it would pass this test while landing
			// on the panel header in front of a reader.
			if naive := docTopRow + m.viewHeight() + 1; naive == row {
				t.Fatalf("docTopRow + viewHeight() + 1 = %d is the row the frame drew the buttons on: "+
					"this fixture no longer drifts, so it cannot tell a stored row from a recomputed one", naive)
			}

			x := tt.aim(buttons[tt.focus])
			cur, cmd := m.Update(tea.MouseClickMsg{X: x, Y: row, Button: tea.MouseLeft})
			m = drain(t, cur.(*Model), cmd)

			if m.mode != modeRead {
				t.Fatalf("mode = %v, want modeRead -- the click at (%d,%d) pressed nothing", m.mode, x, row)
			}
			tt.check(t, f, m)
		})
	}
}

// A click that names no button names NOTHING, and specifically is not clamped to
// the nearest one: a clamped click would post a comment because the reader clicked
// somewhere NEAR [ post ], which is the exact failure this test exists to
// catch. The two-cell gap between the labels is the miss zone between two
// controls that do opposite things, and it is the case here with the most riding
// on it.
//
// EVERY AIM IS DERIVED FROM THE DRAWN ROW, never from a constant, so the cases
// stay adjacent to the buttons however the row is laid out. The rows either side
// are the panel's own header and the textarea's first line -- both real, clickable
// parts of the same panel -- and the document case proves the compose arm answers
// the click instead of falling through to the document walk beneath the panel.
func TestMouseClickOffTheComposeButtonsIsInert(t *testing.T) {
	type aimAt func(row int, b map[composeFocus]screenButton, m *Model) (int, int)
	for _, tt := range []struct {
		name string
		aim  aimAt
	}{
		{"the panel header one row above", func(row int, b map[composeFocus]screenButton, _ *Model) (int, int) {
			return b[composeFocusPost].mid(), row - 1
		}},
		{"the textarea's own first row below", func(row int, b map[composeFocus]screenButton, _ *Model) (int, int) {
			return b[composeFocusPost].mid(), row + 1
		}},
		{"the document behind the panel", func(int, map[composeFocus]screenButton, *Model) (int, int) {
			return 5, docTopRow
		}},
		{"the row's own left inset", func(row int, b map[composeFocus]screenButton, _ *Model) (int, int) {
			return b[composeFocusPost].col - 1, row
		}},
		{"the gap between the two buttons", func(row int, b map[composeFocus]screenButton, _ *Model) (int, int) {
			return b[composeFocusPost].past(), row
		}},
		{"one cell past cancel's right edge", func(row int, b map[composeFocus]screenButton, _ *Model) (int, int) {
			return b[composeFocusCancel].past(), row
		}},
		{"the far right of the button row", func(row int, _ map[composeFocus]screenButton, m *Model) (int, int) {
			return m.width - 1, row
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, m := openComposeOverAWrappingHeading(t)
			m = press(m, "h", "i")
			row, buttons := composeButtonsOnScreen(t, m)
			cursor := m.cursor

			x, y := tt.aim(row, buttons, m)
			cur, cmd := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
			if cmd != nil {
				t.Fatalf("the click at (%d,%d) returned a non-nil cmd: %v, want nil -- it dispatched something", x, y, cmd)
			}
			m = cur.(*Model)
			assertComposeStillOpen(t, m, "hi", cursor)
		})
	}
}

// A LEFT PRESS AND NOTHING ELSE, inherited from the document hit test and worth
// more here than where it was written: on the document a stray gesture mis-aims a
// cursor, on this row it would POST A COMMENT -- and a posted comment can be
// edited nowhere in this product.
//
// THE RELEASE CASE IS NOT SYNTHETIC. A Shift-bypassed drag -- the terminal's own
// text-selection escape -- leaks a release with no press before it, so a handler
// keyed off release would post during a drag the reader meant entirely for the
// terminal. Update matches tea.MouseClickMsg and no other mouse message, and this
// is what says so out loud: routing tea.MouseReleaseMsg to updateMouseClick
// reddens here and nowhere else in the package.
func TestOnlyALeftPressPressesAComposeButton(t *testing.T) {
	for _, tt := range []struct {
		name string
		msg  func(x, y int) tea.Msg
	}{
		{"a right press", func(x, y int) tea.Msg {
			return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight}
		}},
		{"a left release", func(x, y int) tea.Msg {
			return tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, m := openComposeOverAWrappingHeading(t)
			m = press(m, "h", "i")
			row, buttons := composeButtonsOnScreen(t, m)
			cursor := m.cursor

			// Dead centre of [ post ], the one coordinate a left press is proved to
			// post from: anything inert here is inert because of the gesture and not
			// because it missed.
			x := buttons[composeFocusPost].mid()
			cur, cmd := m.Update(tt.msg(x, row))
			if cmd != nil {
				t.Fatalf("the gesture at (%d,%d) returned a non-nil cmd: %v, want nil", x, row, cmd)
			}
			m = cur.(*Model)
			assertComposeStillOpen(t, m, "hi", cursor)
		})
	}
}

// A GEOMETRY THAT OUTLIVED THE MODE THAT DREW IT MUST NOT MAKE A CLICK POST, and
// constructing that state is the whole of what this test is for. m.composeButtons
// is bookkeeping about the LAST FRAME, not a fact about the mode the model is in
// now: viewPainted fills it while the composer is open and nothing empties it
// until some later frame is drawn. A hit test gated on len(spans) > 0 rather than
// on the mode -- the shape updateComposeButtonClick's own comment warns against --
// reads that leftover row as a live button and posts a comment from whatever mode
// the model has moved on to.
//
// THE MODE IS ASSIGNED RATHER THAN NAVIGATED TO, and that is the point rather
// than a shortcut. Every real way out of the composer is an Update, and today
// bubbletea calls View after every one of them (charm.land/bubbletea/v2, tea.go:
// p.render(model) closes the event loop's every iteration), so a repaint empties
// the field before the next message can arrive: THE STALE WINDOW IS SHUT BY
// ANOTHER PACKAGE'S CONTRACT AND NOT BY THIS ONE'S CODE. Gating on the mode is
// what stops "may this gesture post a comment" resting on that contract, and
// assigning the mode is the only way to ask whether it does.
//
// NOT A DUPLICATE OF TestMouseClickIsInertOutsideModeRead, AND THE TWO CATCH
// DIFFERENT BUILDS -- worth spelling out, because the obvious summary of that
// test ("the mode gate is already covered") is right about only half of it. It
// walks the whole mode enum over a model that has drawn NO compose frame, so its
// geometry is empty, and THAT IS WHAT MAKES IT THE ONE that catches an ARM rested
// on emptiness behind an intact outer gate: its modeCompose iteration finds no
// spans, falls through to the document walk, and lands the cursor on a body row.
//
// WHAT AN EMPTY GEOMETRY CANNOT CATCH is the gate ITSELF rested on the geometry
// in place of the mode. A model with no spans is inert there whatever mode it is
// in, so that test stays green while every panel mode sits one leftover frame
// away from a post. THAT is the build this test pins, and pinning it takes a
// NON-EMPTY leftover geometry -- which is why the stored spans are checked to
// still cover this click before it is sent.
func TestMouseClickOnAStaleComposeButtonRowIsInert(t *testing.T) {
	for _, tt := range []struct {
		name string
		mode mode
	}{
		// modeRead IS THE CASE WITH TEETH: it is the mode the composer actually
		// leaves to, by post and by cancel alike, so it is the one a real stale
		// geometry would be found in -- and the outer gate admits it, which leaves
		// the arm's own m.mode == modeCompose as the only thing keeping an ordinary
		// document click off the button row. The confirm panel and the path pane are
		// the other half, where the outer gate is what refuses; both hold the
		// keyboard for their own input while the leftover row is still on the model.
		{"read mode, where the composer went", modeRead},
		{"a confirm panel", modeConfirmApprove},
		{"the path pane", modeRepoint},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, m := openComposeOverAWrappingHeading(t)
			m = press(m, "h", "i")
			row, buttons := composeButtonsOnScreen(t, m)
			x := buttons[composeFocusPost].mid()
			cursor := m.cursor

			m.mode = tt.mode

			// THE GEOMETRY HAS TO STILL NAME THIS CLICK, or the test below is the
			// vacuous one it exists to avoid: a model whose mode was merely set holds
			// an empty geometry, its clicks are inert for lack of anything to resolve
			// against, and a build gating on emptiness is as green as the real one.
			// The arithmetic is deliberately repeated instead of borrowed, because
			// what is being asserted is what the FIELD holds -- the hit test's own
			// answer is the thing under test and cannot vouch for its input.
			g := m.composeButtons
			covered := false
			for _, sp := range g.spans {
				if x >= sp.col && x < sp.col+sp.width {
					covered = true
				}
			}
			if !covered || g.row != row {
				t.Fatalf("test setup: the stored geometry %+v does not cover the click at (%d,%d) -- "+
					"this click would be inert for want of a target rather than because of the gate", g, x, row)
			}

			cur, cmd := m.Update(tea.MouseClickMsg{X: x, Y: row, Button: tea.MouseLeft})
			if cmd != nil {
				t.Fatalf("the click at (%d,%d) in mode %v returned a non-nil cmd: %v, want nil -- "+
					"it dispatched the write behind a button that is no longer on screen", x, row, tt.mode, cmd)
			}
			m = cur.(*Model)

			// THE DRAFT IS THE DETECTOR THAT WORKS IN ALL THREE CASES. Post and cancel
			// both Reset the textarea unconditionally, so an intact "hi" is what says
			// neither ran; the mode says so too, but only in the two panel cases --
			// both buttons leave modeRead, so in the modeRead case a mode assertion
			// cannot fail however wrong the gate is.
			if m.mode != tt.mode {
				t.Fatalf("mode = %v, want unchanged %v -- the click pressed a button the last frame drew and this one does not", m.mode, tt.mode)
			}
			if got := m.ta.Value(); got != "hi" {
				t.Fatalf("ta = %q, want the draft %q intact -- the click posted or cancelled it", got, "hi")
			}
			if m.sess.Exists {
				t.Fatal("a plan was created: the click dispatched a write")
			}
			// THE CURSOR IS COMPLETENESS AND NOT THE DETECTOR HERE, which is worth
			// saying rather than leaving a reader to assume the opposite: the stale
			// row lands in the blank filler BELOW this nine-line document, so
			// lineAtFrameRow names nothing at it and a build that reached the document
			// walk from any of these three modes would leave the cursor alone as well.
			// What separates a mode-gated hit test from a geometry-gated one at these
			// coordinates is the cmd and the draft above.
			if m.cursor != cursor {
				t.Fatalf("cursor = %d, want unchanged %d", m.cursor, cursor)
			}
		})
	}
}

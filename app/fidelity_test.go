package app

// This file states the review view's fidelity property on the SCREEN:
//
//	every block-level construct in the source is represented in the review view.
//
// WHY IT IS HERE AND NOT IN ui. The claim is about a FRAME -- a real terminal
// of a real height, with a status bar, a help bar and a scroll position -- and
// ui.RenderDoc produces a line LIST with no height at all. A construct whose
// rows exist in that list and which no scroll position ever puts on screen
// would pass every assertion in ui and be invisible to the reader, so the
// drive belongs where the viewport does.
//
// IT READS ui/testdata/fidelity.md, the same bytes ui's own assertions run
// over, rather than carrying its own copy of the document: two fixtures both
// claiming "every construct" is two places for the identical claim to rot. The
// cost is a test that reaches across a package boundary for its input, which
// is why the loader says so out loud.
//
// CONSTRUCTS, NOT BYTES. The property is deliberately not "every byte of the
// source is on screen": `**` is markup, the `|` between two cells is markup,
// and a `---` asking for a separator is markup. requireNoMarkupOnScreen is
// that half, and it is asserted from BOTH ends -- the source must still
// contain the markers, or "none on screen" is a fact about an empty fixture.
// It has exactly one exception, the DISCARDED TAIL, asserted from both ends
// too.
//
// WHAT IT CANNOT SEE:
//
//   - IT DRIVES ONE WIDTH AND ONE HEIGHT. 80x24 is the frame a real corpus
//     is written for; ui's own tests drive each construct at 80 and at
//     narrowWidth, which is where a width-arithmetic error shows up.
//   - IT IS A CONTAINMENT CHECK, not a layout check. A row landing in the
//     wrong ORDER, or twice, passes here; order and count live in ui.
//   - IT SAYS NOTHING ABOUT COMMENTS. Every frame it reads is of a document
//     with no threads on it, so the Card projection of every construct is
//     untouched by it.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/keymap"
	"github.com/draftplane/draftplane/session"
	"github.com/draftplane/draftplane/store/localcas"
	"github.com/draftplane/draftplane/theme"
	"github.com/draftplane/draftplane/ui"
)

// fidelitySource is ui/testdata/fidelity.md, read across the package boundary
// on purpose: see this file's header. `go test` runs with the package
// directory as the working directory, so the relative path is stable.
func fidelitySource(t *testing.T) []byte {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "ui", "testdata", "fidelity.md"))
	if err != nil {
		t.Fatalf("reading the shared fidelity fixture: %v", err)
	}
	return src
}

// fidelityDiscardedTail is the fixture's DISCARDED TAIL as it reaches the
// screen: the last cell of a body row that carries more cells than its header,
// with the bytes GFM threw away appended to it raw. It is what the column
// HOLDS at width 80, not what the source says, so it moves whenever the body
// budget does. A const because two assertions need the identical string -- the
// table case that says it is drawn, and requireNoMarkupOnScreen's one '|'
// exception -- and a stale value silently reports the tail's own pipe as
// markup that escaped.
const fidelityDiscardedTail = "sam | a third cell, which the header has no column f"

// fidelityContentCol is the screen column a block's own first character lands
// in: the painted rail band plus the cursor/thread-mark gutter. Written as a
// call plus a literal rather than as 6 so the rail half follows ui.RailWidth if
// the band ever changes; the gutter half has to be a literal because ui's
// gutterWidth is unexported. Nothing checks the literal at compile time, so the
// drive below checks it at run time against a row whose content is known.
//
// It is cut in CELLS and not bytes -- ansi.TruncateLeft rather than a slice --
// because the cursor glyph is one cell and three bytes, so a byte offset lands
// mid-row and every comparison after it is against text the renderer never
// produced.
func fidelityContentCol(width int) int { return ui.RailWidth(width) + 4 }

func fidelityContent(row string, width int) string {
	return strings.TrimRight(ansi.TruncateLeft(ansi.Strip(row), fidelityContentCol(width), ""), " ")
}

// Pages the whole fidelity fixture through a real 80x24 review view and
// asserts that every construct in it -- at every quote depth and list depth
// a real corpus reaches -- is drawn somewhere in the frames a reader would
// actually see.
//
// PAGING IS THE POINT. The document is deliberately longer than one frame, so
// this is several screens rather than one, and a construct that only exists
// below the fold is one this catches and a line-list assertion cannot.
func TestEveryConstructReachesTheReviewFrame(t *testing.T) {
	const width, height = 80, 24
	src := fidelitySource(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "fidelity.md")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}
	f := fixture{
		svc:  newFixtureStore(t, filepath.Join(dir, "state.json")),
		cas:  localcas.New(filepath.Join(dir, "objects")),
		path: path,
		ctx:  attrCtx("alice"),
	}
	th, err := theme.Lookup(theme.DefaultName)
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
	m.width, m.height = width, height
	m.rerender()

	// The fixture must not fit on one screen, or this is a line-list drive
	// wearing a viewport's clothes.
	if len(m.lines) <= m.viewHeight() {
		t.Fatalf("the fixture renders in %d lines and the viewport holds %d, so nothing here is below the fold", len(m.lines), m.viewHeight())
	}

	var rows []string
	frames := 0
	for {
		frames++
		for _, row := range strings.Split(m.View().Content, "\n") {
			rows = append(rows, fidelityContent(row, width))
		}
		before := m.scroll
		m = press(m, "pgdown")
		if m.scroll == before {
			break
		}
		if frames > len(m.lines) {
			t.Fatalf("paging did not terminate after %d frames", frames)
		}
	}
	t.Logf("%d blocks rendered into %d lines, read in %d frames of %dx%d", len(m.blocks), len(m.lines), frames, width, height)

	// The gutter offset the comparisons below depend on, checked against a row
	// whose content is known rather than trusted: get it wrong and every case
	// fails with an off-by-a-few that reads like a rendering bug.
	if !fidelityRowExists(rows, "§ Fidelity fixture") {
		t.Fatalf("the document's own H1 is not at content column %d, so every assertion below is measuring the wrong column", fidelityContentCol(width))
	}

	// One case per construct, each naming the shape in a real corpus it stands
	// in for. The counts in those names are what the gated corpus harness in ui
	// reported; they say why each case exists, and nothing asserts them -- a
	// real corpus changes and the harness recomputes.
	cases := []struct{ name, want string }{
		{"heading", "§ Fidelity fixture"},
		{"a quoted heading (33 in a corpus of real plans)", "▎ §§§ A quoted heading"},
		{"prose, with its inline markup resolved", "Prose with a code span, emphasis and a link."},

		{"quotation at depth 1 (435 paragraphs)", "▎ Quoted at depth one."},
		{"quotation at depth 2 (10 paragraphs)", "▎ ▎ Quoted at depth two."},
		{"quotation at depth 3 (exactly 1 paragraph)", "▎ ▎ ▎ Quoted at depth three, which is as deep as quotations in real"},
		{"a depth-3 continuation row keeps its bars", "▎ ▎ ▎ plans go, written long enough here that it wraps at eighty"},
		{"a quoted list item (144 at depth 1)", "▎ • a quoted bullet"},
		{"a list item quoted twice over (9 at depth 2)", "▎ ▎ • a bullet quoted two deep"},
		{"a list nested two deep INSIDE a quotation (8 in a corpus of real plans)", "▎   • a list nested two deep inside the quotation"},
		// THE BULLET IS PINNED, NOT ENDORSED. A quotation inside a list item
		// is drawn with a '•' in front of it, and it is not a bullet -- the
		// author wrote a quotation. ParseBlocks maps ANY paragraph with
		// listDepth > 0 to KindListItem, and renderBlockPainted's KindListItem
		// arm draws a bullet for every one, so the mark overstates what the
		// source says. The construct IS represented, which is this file's
		// property; the vocabulary is a separate, PRODUCT question, and a live
		// one -- a real share of corpus blocks are both quoted and inside a
		// list.
		//
		// So: if you are the person who fixes the bullet, THIS ASSERTION IS
		// SUPPOSED TO FAIL. Change the string; do not conclude the drive is
		// broken.
		{"a quotation inside a list item, drawn with a bullet it did not ask for (153 blocks are both)", "▎ • Quoted inside a list item."},
		{"a quoted code block (4 in a corpus of real plans)", "▎   quoted_code_line()"},

		{"a thematic break is DRAWN, at the body's full width", strings.Repeat("─", width-2*ui.RailWidth(width)-4-ui.DocRightMargin)},

		{"an ordinary list item", "• an ordered item"},
		{"a list item nested three deep", "    • and one deeper still"},
		{"a fenced code block", "  func fenced() int { return 0 }"},
		{"an indented code block", "  indented code"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !fidelityRowExists(rows, c.want) {
				t.Fatalf("no row of any frame is %q -- this construct is in the source and not in the review view", c.want)
			}
		})
	}

	// THE TABLE CASES ARE A CELL AND NOT A WHOLE ROW, and that is not a
	// weakening of the rule above. A table renders as a BORDERED GRID, so
	// several constructs share one screen row -- `│ Cutover │ sam │ Wednesday │
	// latency │ after the soak │` is five cells -- and "some row IS this
	// string" can no longer be asked of one of them. What replaces it keeps the
	// same property: the text must be a CELL of a grid row, delimited by the
	// box on both sides, and the row must begin with the lead the construct is
	// supposed to carry -- here the mark is the box rather than a bullet or a
	// sigil.
	//
	// THE LEAD IS THE OTHER HALF and it is why the quoted cases pass "▎ ". A
	// quoted table's grid is drawn INSIDE the quotation, one bar per level in
	// front of every line of the box, so a quoted cell found on an unled row
	// would be a table that had stepped out of its quotation.
	tableCases := []struct{ name, lead, cell string }{
		{"a two-column table's header, drawn once as a row of its own", "", "Step"},
		{"a two-column table row, its first cell", "", "Deploy the gateway"},
		{"a two-column table row, its second cell", "", "dana"},
		{"a five-column table row (2 tables have 5; the widest in a corpus of real plans is 7)", "", "Cutover"},
		{"the fifth cell of that row", "", "after the soak"},
		{"a quoted table's header row (3 quoted tables in a corpus of real plans)", "▎ ", "Quoted step"},
		{"a quoted table row (15 quoted rows in a corpus of real plans)", "▎ ", "Quoted deploy"},
		// BOTH CELLS, because one alone is what a walk that dropped every
		// column after the first would also draw.
		{"a table with no body rows draws its header, first cell", "", "Header with no rows"},
		{"a table with no body rows draws its header, second cell", "", "Second such header"},

		// Each of these is a table a real corpus holds; the share of a real
		// corpus each stands in for is recorded once, beside the census in
		// ui/ui_test.go, and deliberately not repeated here.
		{"a SEVEN-column table's header, its first cell", "", "Chan"},
		{"the seventh cell of that header, which a five-wide walk would drop", "", "State"},
		{"a seven-column body row's fourth cell", "", "yes"},
		// THE TAIL IS ON SCREEN, WHOLE AND RAW. It is the one span of this
		// document the review view draws without projecting -- the parser
		// discarded it before the AST existed, so there is no inline walk to
		// project it with -- and it is why requireNoMarkupOnScreen below
		// carries a '|' exception. Asserted here as well as excepted there,
		// because an exception with nothing behind it is a hole.
		{"a discarded tail, drawn raw at the end of the cell it was cut from", "", fidelityDiscardedTail},
		{"a cell of two thousand characters wraps rather than overflowing", "", "A cell can hold a paragraph, and real plans do"},
		// PINNED, NOT ENDORSED. A token longer than its column is broken
		// INSIDE the word -- `TestTheSearchGestur` / `eEndToEnd...` --
		// which is the cost accepted rather than writing a column allocator
		// for. If you are the person who writes that allocator, THIS CASE IS
		// SUPPOSED TO FAIL.
		{"a token no column can hold, broken mid-word on screen", "", "app/search_test.go:TestTheSearchGestur"},
	}
	for _, c := range tableCases {
		t.Run(c.name, func(t *testing.T) {
			if !fidelityCellExists(rows, c.lead, c.cell) {
				t.Fatalf("no grid row led by %q holds %q as a cell -- this construct is in the source and not in the review view", c.lead, c.cell)
			}
		})
	}
	// AND THE BOX ITSELF IS ON SCREEN, asserted separately because every case
	// above would also pass on a renderer that drew the dividers and no outer
	// border at all: a cell delimited on both sides is what a bare `│` between
	// columns already gives. Both leads, because the quoted table is a second
	// grid and not the same one seen through a bar.
	for _, lead := range []string{"", "▎ "} {
		if !fidelityGridIsClosed(rows, lead) {
			t.Fatalf("no frame row led by %q both opens and closes a grid, so that table is not drawn in a box", lead)
		}
	}

	// AND EVERY GRID ROW ON SCREEN IS SHUT ON BOTH SIDES, a stronger claim than
	// the one above: fidelityGridIsClosed asks that SOME grid opens and closes,
	// this asks it of every row of every grid in every frame. lipgloss
	// over-allocates columns in a band of budgets just below a table's natural
	// width and its own MaxWidth truncation then eats the right-hand border
	// (ui/painted.go, closeGridRight, which repairs it); this fixture's
	// discarded-tail table was open at a budget of 72, exactly what a terminal
	// of 80 hands a table, so a reader saw a box with no right edge.
	if drawn := fidelityGridRowsAllClosed(t, rows); drawn == 0 {
		t.Fatal("no frame row is a grid row at all, so the assertion above checked nothing")
	} else {
		t.Logf("%d grid rows on screen, every one closed on the right", drawn)
	}

	// AN EMPTY HEADER CELL IS A COLUMN AND NOT A MISSING ONE, and it needs a
	// whole row rather than a cell: fidelityCellExists asks "is this text one
	// of the cells", and "" is one of the cells of almost every row once the
	// padding is trimmed, so asking it about an empty cell answers yes for the
	// wrong reason. What this asks instead is that some grid row parts into
	// exactly these three cells in this order -- an empty header cell goes
	// wrong as a column that vanishes, taking its body cells' alignment along.
	if !fidelityGridRowIs(rows, "", []string{"", "Owner", "Note"}) {
		t.Fatal("no grid row is an empty cell followed by Owner and Note -- the table with an empty header cell has lost a column")
	}

	requireNoMarkupOnScreen(t, src, rows)
}

// fidelityRowExists reports whether some row of some frame IS want, exactly.
// Equality and not containment: "the row holds this text somewhere" is
// satisfied by a construct drawn with the wrong marks in front of it, and the
// marks are how a reader tells one construct from another.
func fidelityRowExists(rows []string, want string) bool {
	for _, row := range rows {
		if row == want {
			return true
		}
	}
	return false
}

// fidelityCellExists reports whether some row of some frame is a GRID row led
// by lead and holding want as one of its cells, trimmed of the padding the box
// puts around it -- fidelityRowExists' answer for a construct that shares its
// screen row with four others.
func fidelityCellExists(rows []string, lead, want string) bool {
	for _, row := range rows {
		box, ok := strings.CutPrefix(row, lead+"│")
		if !ok {
			continue
		}
		for _, cell := range strings.Split(strings.TrimSuffix(box, "│"), "│") {
			if strings.TrimSpace(cell) == want {
				return true
			}
		}
	}
	return false
}

// fidelityGridRowsAllClosed fails the test for any frame row that OPENS a grid
// and does not close it WITH THE GLYPH THAT PAIRS WITH ITS OPENER, and answers
// how many grid rows it checked so the caller can refuse a vacuous pass.
//
// A GRID ROW IS ONE THAT OPENS WITH THE BOX, at the content column or behind a
// quote bar: the four left-hand glyphs of the border, which is every line
// lipgloss draws for a table and nothing else in this document. Both sets are
// spelled out rather than read off lipgloss's Border, because ui's border is
// unexported and a test that reached for it would be asserting the renderer
// against itself.
//
// IT PAIRS RATHER THAN LISTING. Accepting any of the four right-hand glyphs on
// any line leaves ui's closeGridRight's four-branch closer-selection switch
// pinned by nothing: replacing that whole switch with the plain Right kept this
// suite and both gated corpus sweeps green while a repaired box rendered
// '┌───┬───│'. With the pairing in place that mutation fails here and in four
// more tests in ui.
func fidelityGridRowsAllClosed(t *testing.T, rows []string) int {
	t.Helper()
	closes := map[rune]rune{'┌': '┐', '├': '┤', '└': '┘', '│': '│'}
	drawn := 0
	for i, row := range rows {
		body := strings.TrimPrefix(row, "▎ ")
		opens := []rune(body)
		if len(opens) == 0 {
			continue
		}
		want, isGrid := closes[opens[0]]
		if !isGrid {
			continue
		}
		drawn++
		if got := opens[len(opens)-1]; got != want {
			t.Fatalf("frame row %d opens on %q and closes on %q, want %q: %q", i, string(opens[0]), string(got), string(want), row)
		}
	}
	return drawn
}

// fidelityGridRowIs reports whether some grid row led by lead parts into
// exactly these cells, in order, each trimmed of the padding the box puts
// around it -- fidelityCellExists' answer for a claim about a row's SHAPE
// rather than about one of its cells.
func fidelityGridRowIs(rows []string, lead string, want []string) bool {
	for _, row := range rows {
		box, ok := strings.CutPrefix(row, lead+"│")
		if !ok {
			continue
		}
		cells := strings.Split(strings.TrimSuffix(box, "│"), "│")
		if len(cells) != len(want) {
			continue
		}
		match := true
		for i, cell := range cells {
			if strings.TrimSpace(cell) != want[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// fidelityGridIsClosed reports whether ONE grid led by lead both opens and
// closes across the frames: the box a reader sees around a table, which no
// cell assertion can see.
//
// ONE GRID, AND THAT IS THE WHOLE DIFFICULTY. Tracking "a top was seen" and "a
// bottom was seen" independently passes on a `┌───┐` from one table and a
// `└───┘` from another. So this pairs a bottom with the nearest top ABOVE it
// and requires the two to agree on WHERE THE COLUMN DIVIDERS SIT -- '┬' on the
// top border and '┴' on the bottom, at the same cells. Every table renders to
// the same total width, so width cannot tell two tables apart; the column
// positions can.
func fidelityGridIsClosed(rows []string, lead string) bool {
	var open []int
	for _, row := range rows {
		box, ok := strings.CutPrefix(row, lead)
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(box, "┌") && strings.HasSuffix(box, "┐"):
			open = gridDividers(box, '┬')
		case strings.HasPrefix(box, "└") && strings.HasSuffix(box, "┘"):
			if open != nil && slices.Equal(open, gridDividers(box, '┴')) {
				return true
			}
		}
	}
	return false
}

// gridDividers is where a border row's column dividers sit, counted in cells.
// Runes and cells are the same here because every glyph a box is drawn from is
// one cell wide.
func gridDividers(box string, divider rune) []int {
	at := []int{}
	for i, r := range []rune(box) {
		if r == divider {
			at = append(at, i)
		}
	}
	return at
}

// requireNoMarkupOnScreen is the CONSTRUCTS-NOT-BYTES half of the property.
// Three markers, each asserted from both ends so that neither half can pass
// vacuously: the source must still carry them, and no frame may show one.
//
//   - '*' is emphasis, strong emphasis, and one of the three spellings of a
//     thematic break. renderInlines resolves the first two and the KindRule
//     arm draws the third, so none of the fixture's asterisks is on screen.
//   - '|' separates a table's cells and is not one of them. It is also
//     commentMarker, which is why this asserts over a document with no threads
//     on it: a comment header legitimately draws one.
//   - '`' delimits a code span, whose CONTENT is drawn and whose backticks are
//     not.
//
// A fenced code block's body is drawn verbatim, so a fixture that put any of
// these inside a fence would falsify this without anything being wrong. The
// fixture's fence is deliberately free of all three.
//
// '|' HAS EXACTLY ONE EXCEPTION AND IT IS NAMED RATHER THAN WAIVED. A body row
// with MORE cells than the header has the excess DISCARDED by the parser
// before the AST exists, so those bytes reach no cell and there is no inline
// walk to project them with. They are appended to the last cell RAW, pipes and
// all, because a reviewer can see them in their editor and this view must not
// be the one place they are missing. So the property is not "no '|' reaches the
// screen" -- it is "the only '|' that reaches the screen is inside that tail".
//
// BOTH ENDS, like every other marker here: the tail must BE on screen, or the
// exception is a hole rather than a fact. Its own case in the table drive
// above asserts that from the other side.
func requireNoMarkupOnScreen(t *testing.T, src []byte, rows []string) {
	t.Helper()
	tails := 0
	for _, marker := range []string{"*", "|", "`"} {
		inSource := strings.Count(string(src), marker)
		if inSource == 0 {
			t.Fatalf("the fixture no longer contains %q at all, so finding none on screen says nothing", marker)
		}
		for i, row := range rows {
			if !strings.Contains(row, marker) {
				continue
			}
			if marker == "|" && strings.Contains(row, fidelityDiscardedTail) {
				tails++
				continue
			}
			t.Fatalf("%q is markup and reached the screen: frame row %d is %q (the source has %d of them)", marker, i, row, inSource)
		}
		if marker == "|" {
			t.Logf("%d %q in the source, and the only ones on screen are the %d row(s) of the discarded tail", inSource, marker, tails)
			continue
		}
		t.Logf("%d %q in the source, none on screen", inSource, marker)
	}
	if tails == 0 {
		t.Fatalf("no frame row holds the discarded tail %q, so the '|' exception above is excusing nothing", fidelityDiscardedTail)
	}
	t.Logf("%d frame row(s) hold the discarded tail, which is the only markup this document draws", tails)
	// The rule's own bytes, which are markup for "a break goes here" and never
	// text. Checked separately from '*' because `---` is the spelling the
	// corpus overwhelmingly uses and it shares no character with the others.
	if !strings.Contains(string(src), "---") {
		t.Fatal("the fixture no longer contains a `---`, so finding none on screen says nothing")
	}
	for i, row := range rows {
		if strings.Contains(row, "---") {
			t.Fatalf("`---` is markup and reached the screen: frame row %d is %q", i, row)
		}
	}
}

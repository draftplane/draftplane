package ui

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/x/ansi"
)

// railBandWidth is the width of the painted rail band; railMinWidth is the
// terminal width below which it collapses to nothing. The cursor/thread-mark
// gutter keeps its usual width and position either way.
const (
	railBandWidth = 2
	railMinWidth  = 80
)

// DocRightMargin is the document background's own breathing room at the
// right-hand end of a row, before the rail band starts.
//
// IT IS SPENT OUT OF body AND NOT OUT OF rowWidth: the Width(rowWidth) every
// non-grid arm renders through pads the reserved cells back out IN THAT ROW'S
// OWN BACKGROUND -- doc ground for prose, CodeBg inside a fence, Card inside a
// comment. A margin painted once in one colour by the shared wrap would step
// two-tone down the right edge of every comment card. The grid arm does not go
// through Width(rowWidth) and pads this margin by hand.
//
// Exported because callers outside renderDocPainted -- the app's blank filler
// rows, the fidelity harness -- re-derive the same budget from RailWidth and
// gutterWidth.
const DocRightMargin = 2

const docRightMargin = DocRightMargin

// RailWidth reports the painted rail band's width for a terminal of the
// given width: railBandWidth once it's wide enough, 0 below railMinWidth.
// Exposed so callers rendering rows outside RenderDoc (the app's blank
// filler rows below a short document) can keep the band continuous with the
// document above it.
func RailWidth(width int) int {
	if width >= railMinWidth {
		return railBandWidth
	}
	return 0
}

// renderDocPainted is RenderDoc's full-canvas path: every returned line has
// its background filled to width, so the terminal background never shows
// through. Every literal fragment below is rendered through some style
// (never concatenated as a naked string) because a nested lipgloss.Render
// call ends with a full SGR reset — an unstyled fragment sitting after one
// mid-line would show the terminal's default background instead of the
// zone's, and only the trailing width-pad is automatically re-colored by an
// outer wrap. A grid row has no trailing width-pad at all, so on that one row
// the escape hatch does not exist.
func renderDocPainted(blocks []Block, views map[int][]ThreadView, unanchored []ThreadView, expanded map[int]bool, cur Cursor, width int, pseudonym string, st *Styles) []Line {
	var lines []Line

	railWidth := RailWidth(width)
	// A margin on BOTH sides: the left one carries the cursor, the right is
	// plain ground so the document does not run into the terminal's edge.
	rowWidth := width - 2*railWidth
	body := rowWidth - gutterWidth - docRightMargin

	band := func(isCursor bool) string {
		if railWidth == 0 {
			return ""
		}
		s := st.Rail
		if isCursor {
			s = st.RailCursor
		}
		return s.Render(strings.Repeat(" ", railWidth))
	}
	// EMPTY when the rail collapses, not a styled render of nothing: a style
	// with a background emits its escape even for an empty string, so the
	// margin would keep colouring a zero-width column below railMinWidth.
	rightMargin := ""
	if railWidth > 0 {
		rightMargin = st.Rail.Render(strings.Repeat(" ", railWidth))
	}
	// wrap puts a row between its two margins. Every line the document emits
	// goes through it, so a row that forgets one cannot be written.
	wrap := func(bandOn bool, body string) string {
		return band(bandOn) + body + rightMargin
	}
	// blankRow carries no quote bar and no focus of either kind, WHICH IS WHY
	// A BLOCK MUST PAINT AT LEAST ONE ROW OF ITS OWN. The separator below a
	// block is drawn here whatever the focus is, so a block whose body painted
	// zero rows still has a Line and still answers FirstLineOf -- what it does
	// not have is anywhere for the band or the '┃' to appear, and the cursor
	// would rest on it invisibly. That is why renderBlockPainted's KindTableRow
	// arm paints one blank line for an all-empty row rather than none.
	blankRow := func(idx int) Line {
		return Line{Text: wrap(false, st.DocBG.Width(rowWidth).Render("")), BlockIdx: idx, ThreadIdx: NoThread}
	}
	cursorGlyph := func(base lipgloss.Style, isCursor bool) string {
		if !isCursor {
			return base.Render("  ")
		}
		return base.Foreground(st.BrandColor).Render("┃ ")
	}

	// THE GRID PRE-PASS RUNS ONCE, BEFORE THE LOOP, and it has to: a column is
	// a property of the WHOLE table and this loop paints one block against one
	// width. See paintTables, which returns each table row's lines keyed by
	// block index, memoised because this function runs on every cursor step. A
	// block with no entry was not painted and falls back to renderBlockPainted's
	// own KindTableRow arm.
	//
	// onCardAt and cardBelowAt are this function's own questions, lifted out of
	// the loop so the pre-pass and the loop cannot come to disagree about them.
	// They are DIFFERENT questions: onCardAt asks which zone a block is painted
	// in, which a collapsed thread still decides, and cardBelowAt asks whether
	// card lines are actually DRAWN under it, which is what a grid has to close
	// itself around.
	onCardAt := func(idx int) bool {
		return len(views[idx]) > 0 && blocks[idx].Kind != KindCode
	}
	cardBelowAt := func(idx int) bool {
		return expanded[idx] && len(views[idx]) > 0
	}
	grids := paintTablesCached(blocks, body, st, onCardAt, cardBelowAt)
	// splitTableGrid's own classification, read once and applied below to tell
	// a group's BORDER lines from its content lines. It is built from the
	// border the grid was actually drawn with, so a later change of box moves
	// the renderer and this reader together.
	borderRunes := borderRunesOf(tableGridBorder())

	for i, b := range blocks {
		isCursor := i == cur.Block
		// The rail is ONE position: the block's own rows carry it only when the
		// focus rests on the line, and a focused thread's rows carry it instead.
		onLine := isCursor && cur.Thread == NoThread
		if b.Kind == KindHeading && len(lines) > 0 {
			lines = append(lines, blankRow(i))
		}
		blockViews := views[i]
		// A block that carries comments is painted on Card, the same zone as
		// the cards themselves, so the block and its threads read as one
		// region. blockSt carries the whole document zone rebased, because a
		// row's text is styled leaf by leaf and a background set only on the
		// outer wrap would leave Doc-coloured runs inside a Card-coloured row.
		// A code block keeps CodeBg even when commented: the alternative --
		// Card outside the row with CodeBg text inside it -- is a row painted
		// in two zones at once.
		onCard := onCardAt(i)
		blockSt := st
		if onCard {
			blockSt = st.OnCard()
		}
		rowBG := blockSt.DocBG
		gutterBase := blockSt.Text
		if b.Kind == KindCode {
			rowBG = blockSt.CodeBg
			gutterBase = blockSt.CodeBg
		}
		// A table row the pre-pass painted comes back as its slice of the grid;
		// every other block, and a table row the pre-pass drew nothing for, is
		// painted here. THE GROUP IS THE MEMO'S OWN SLICE and is never written
		// to: quoteBarLead returns a fresh one, which is what keeps a quoted
		// table from accumulating a bar per repaint in the cache.
		rows, gridded := grids[i]
		var borders []bool
		if gridded {
			borders = make([]bool, len(rows))
			for j, l := range rows {
				borders[j] = borderOnlyLine(l, borderRunes)
			}
			// The bar is drawn HERE and budgeted THERE: paintTables takes
			// quoteBarCols out of the width itself and leaves the drawing to
			// the row, exactly as withQuoteBar does for every other kind.
			rows = quoteBarLead(b, rows, blockSt)
		} else {
			rows = withQuoteBar(b, body, blockSt, onCard)
		}
		// THE MARK GOES ON THE ROW'S FIRST CONTENT LINE, WHICH UNDER A GRID IS
		// NOT LINE 0: the box's top border opens the FIRST row's group. -1 when
		// the block carries no threads, and it matches no j.
		markAt := -1
		if len(blockViews) > 0 {
			for j := range rows {
				if j >= len(borders) || !borders[j] {
					markAt = j
					break
				}
			}
		}
		for j, text := range rows {
			// NO FOCUS AND NO MARK ON A BORDER LINE. A row's group ends with
			// the rule that CLOSES it, one screen row BELOW the row's own text,
			// and that rule belongs to exactly one of two adjacent rows.
			//
			// FOCUS IS TWO THINGS AND onRow IS WHAT KEEPS THEM AGREEING: the
			// rail band that wrap() paints in the left margin, and the cursor
			// glyph '┃' that cursorGlyph paints in the gutter. Narrowing only
			// one of them leaves the band standing taller than the glyph.
			onBorder := j < len(borders) && borders[j]
			onRow := onLine && !onBorder
			gutter := cursorGlyph(gutterBase, onRow)
			mark := gutterBase.Render("  ")
			if j == markAt {
				// The trailing space goes through gutterBase rather than
				// riding along inside the mark's own Render: a fragment
				// after a nested Render's SGR reset would show the
				// terminal's own background, which is the rule the whole
				// file follows. BOLD to match the card's own status dot,
				// which takes its weight from CardHeader.
				mark = gutterBase.Foreground(st.AccentColor).Bold(true).Render(gutterMarkGlyph(allResolved(blockViews))) + gutterBase.Render(" ")
			}
			row := gutter + mark + text
			// A GRID ROW IS RENDERED WITHOUT Width(), AND MUST STAY THAT WAY.
			// Style.Width re-wraps what it is handed through lipgloss.Wrap,
			// which measures a bare C0 or DEL byte as ONE cell where
			// ansi.StringWidth -- what lipgloss/v2/table's own MaxWidth and
			// closeGridRight fit every grid line to -- measures ZERO. A grid row
			// is the only row in this view that arrives exactly its budget wide,
			// so that one-cell disagreement has nowhere to go: the wrap breaks
			// the line, the Line comes out holding TWO screen rows, and
			// app/painted.go writes one "\n" per Line. Pinned by
			// TestGridRowsSurviveAControlByte; putting Width(rowWidth) back
			// fails that test and nothing else.
			//
			// IT IS NOT SANITISATION AND MUST NOT BECOME ONE. No byte is
			// stripped, replaced or refused here.
			//
			// WHAT IT COSTS: nothing pads a short grid row any more, so the
			// pre-pass has to draw every line to exactly its budget.
			// closeGridRight's pad is what makes that true, and
			// TestAGridLineIsNeverShortOfItsBudget and
			// TestTableGridFitsItsWidthBudget are what assert it per line.
			painted := rowBG.Render(row)
			if !gridded {
				painted = rowBG.Width(rowWidth).Render(row)
			} else {
				// docRightMargin BY HAND, because this arm is the one that
				// does not go through Width(rowWidth). Literal spaces in the
				// row's own background, never Width(): re-running lipgloss's
				// wrap here is the defect TestGridRowsSurviveAControlByte
				// pins out.
				painted += rowBG.Render(strings.Repeat(" ", docRightMargin))
			}
			lines = append(lines, Line{Text: wrap(onRow, painted), BlockIdx: i, ThreadIdx: NoThread})
		}
		if cardBelowAt(i) {
			for vi, v := range blockViews {
				// The rail and the ┃ mark the SELECTED thread, not every
				// thread on the block: together they say which one reply
				// and resolve are aimed at.
				onSel := isCursor && vi == cur.Thread
				if vi > 0 {
					// A blank Card row between threads; without it,
					// neighbouring threads on one block run straight
					// together into a single wall of headers.
					gutter := cursorGlyph(st.Card, false)
					lines = append(lines, Line{Text: wrap(false, st.Card.Width(rowWidth).Render(gutter+st.Card.Render(docCardIndent))), BlockIdx: i, IsThread: true, ThreadIdx: NoThread})
				}
				// The Card indent between gutter and card is the nesting
				// cue: a card's text sits two columns in from its block's.
				// The budget subtracts cardIndentCols and NOT the indent's
				// own width -- the indent is already spent out of rowWidth
				// by the gutter arithmetic that produced body, and
				// subtracting it twice leaves every card a column short.
				for _, text := range renderThreadCardPainted(v, body-cardIndentCols, pseudonym, st) {
					gutter := cursorGlyph(st.Card, onSel)
					row := gutter + st.Card.Render(docCardIndent) + text
					lines = append(lines, Line{Text: wrap(onSel, st.Card.Width(rowWidth).Render(row)), BlockIdx: i, IsThread: true, ThreadIdx: vi})
				}
			}
		}
		// THE INTER-BLOCK SEPARATOR IS SUPPRESSED INSIDE A TABLE: two rows of
		// one grid are one box, and a blank row between them tears it in half.
		// Both sides must have been painted as a grid -- a table the pre-pass
		// drew nothing for is a run of ordinary blocks and keeps its
		// separators.
		if !continuesGrid(blocks, grids, i) {
			lines = append(lines, blankRow(i))
		}
	}

	if len(unanchored) > 0 {
		lines = append(lines, blankRow(UnanchoredIdx))
		header := st.DocBG.Render("  ") + st.SectionRule.Render(fmt.Sprintf("── unanchored (%d) ──", len(unanchored)))
		lines = append(lines, Line{Text: wrap(false, st.DocBG.Width(rowWidth).Render(header)), BlockIdx: UnanchoredIdx, ThreadIdx: NoThread})
		for vi, v := range unanchored {
			if vi > 0 {
				lines = append(lines, Line{Text: wrap(false, st.Card.Width(rowWidth).Render(st.Card.Render(strings.Repeat(" ", unanchoredIndentCols)))), BlockIdx: UnanchoredIdx, IsThread: true, ThreadIdx: NoThread})
			}
			// The unanchored section carries no gutter, so its cards are
			// budgeted against its own indent rather than body's -- but it
			// STILL OWES docRightMargin, which every arm has. Budgeting
			// from rowWidth alone drops it, and that loss is invisible to a
			// frame-width check because the margin is INSIDE the budget.
			for _, text := range renderThreadCardPainted(v, rowWidth-unanchoredIndentCols-cardIndentCols-docRightMargin, pseudonym, st) {
				row := st.Card.Render(strings.Repeat(" ", unanchoredIndentCols)) + text
				lines = append(lines, Line{Text: wrap(false, st.Card.Width(rowWidth).Render(row)), BlockIdx: UnanchoredIdx, IsThread: true, ThreadIdx: vi})
			}
		}
	}

	return lines
}

// continuesGrid answers whether the block at i is a grid row whose table
// continues into the very next block, which is the one case renderDocPainted
// draws no separator after.
//
// IT ASKS THE TABLE'S IDENTITY AND NOT ADJACENCY: two tables separated by a
// blank line are BLOCK-ADJACENT with nothing between them.
//
// AND IT ASKS BOTH SIDES WHETHER THEY WERE PAINTED, which is a guard no test
// covers and none can -- the pre-pass is all-or-nothing per table, so "this row
// painted and the next did not" is a state the painter cannot produce. It is
// kept because the alternative is a half-painted table drawn as one box with a
// hole in it.
func continuesGrid(blocks []Block, grids map[int][]string, i int) bool {
	if _, painted := grids[i]; !painted {
		return false
	}
	if i+1 >= len(blocks) {
		return false
	}
	next := blocks[i+1]
	if next.Kind != KindTableRow || next.Table.ID != blocks[i].Table.ID {
		return false
	}
	_, painted := grids[i+1]
	return painted
}

// quoteBarGlyph marks quoted text, ONE PER NESTING LEVEL in the content area
// -- "▎ quoted", "▎ ▎ nested" -- so depth stays countable at a glance. A real
// corpus makes the repeat affordable: 253 blockquotes sit at depth 1, 12 at
// depth 2 and exactly 1 at depth 3.
//
// IT IS ONE OF FOUR VERTICALS IN THIS VIEW AND THE OTHER THREE MEAN SOMETHING
// ELSE: '┃' in Brand is "the cursor is on this block" (cursorGlyph, above),
// '|' in Accent-and-bold -- Dim-and-bold-italic once the thread is resolved --
// is "this line is a comment header" (commentMarker, ui/render.go), and '│' in
// Dim is a table's column boundary (borderStyleOf and tableGridBorder, below).
// A reader must never read QUOTED as COMMENTED, which is what rules out '│'
// here: it is the same thin centred stroke as '|' in the same place, pixel IoU
// 0.49-0.87 against it. '▎' hugs the cell's LEFT EDGE and its ink is DISJOINT
// from all three, IoU 0.000 against each in every face that carries it, styled
// or not.
//
// THE BAR MARKS ROWS THAT CARRY QUOTED TEXT AND NOTHING ELSE. Every block is
// followed by a blank row (blankRow, above), so a quotation of two paragraphs
// renders as two barred regions with an unbarred row between them. Carrying the
// bar across that gap needs quote IDENTITY on the Block and not its depth,
// which is what this deliberately does not have: 40 blockquotes in a real
// corpus sit immediately after another one, and a bar drawn from depth alone
// would merge each of those pairs into one quotation and paint a bar on a row
// inside neither.
const quoteBarGlyph = "▎"

// quoteBarCols is what one block's bars cost on screen. Spent twice, like
// docCardIndent -- once rendered, once subtracted from the width budget --
// and a glyph plus its trailing space is 2 cells per level.
func quoteBarCols(depth int) int { return 2 * depth }

// withQuoteBar is renderBlockPainted with the quote bar in front of every row
// it returns.
//
// IT WRAPS THE SWITCH RATHER THAN LIVING INSIDE IT, so an arm that forgets the
// bar cannot be written. The width the arms wrap to is the budget MINUS the bar
// for the same reason: a row wrapped to the full width and then given a bar is
// wider than the row it is painted into, which lipgloss resolves by wrapping
// the whole thing again and putting two screen lines inside one Line.
func withQuoteBar(b Block, width int, st *Styles, onCard bool) []string {
	cols := quoteBarCols(b.QuoteDepth)
	rows := renderBlockPainted(b, width-cols, st, onCard)
	if cols == 0 {
		return rows
	}
	bar := quoteBarFor(b, st)
	for i, r := range rows {
		rows[i] = bar + r
	}
	return rows
}

// quoteBarFor is the run of bars one block's rows are led with, "" for an
// unquoted block. It has two callers that must lead their rows with the
// identical string: withQuoteBar, and a table row, whose lines come from the
// grid pre-pass -- that budgets for the bar (quoteBarCols) and leaves the
// drawing here.
func quoteBarFor(b Block, st *Styles) string {
	if b.QuoteDepth == 0 {
		return ""
	}
	return quoteBarStyle(b, st).Render(strings.Repeat(quoteBarGlyph+" ", b.QuoteDepth))
}

// quoteBarLead is quoteBarFor applied to lines that were rendered somewhere
// else -- the grid pre-pass's, today.
//
// IT ALWAYS RETURNS A NEW SLICE, unquoted blocks included, and that is why it
// is not withQuoteBar's in-place loop. The lines it is handed belong to the
// MEMO (tableGridCache), which hands the same slice back on every repaint;
// writing a bar into it would put a second bar in front of every quoted row on
// the next keypress and nothing would fail until somebody looked at the
// screen.
func quoteBarLead(b Block, rows []string, st *Styles) []string {
	bar := quoteBarFor(b, st)
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = bar + r
	}
	return out
}

// quoteBarStyle is Dim on whatever background the row it leads is painted in.
// A code block owns CodeBg wherever it appears (see rowBG above), so a bar
// rendered on Doc in front of one would leave the row painted in two zones at
// once. st.Dim carries Doc or Card already, whichever the caller's OnCard
// rebasing left it on.
func quoteBarStyle(b Block, st *Styles) lipgloss.Style {
	if b.Kind == KindCode {
		return st.CodeBg.Foreground(st.Dim.GetForeground())
	}
	return st.Dim
}

// renderBlockPainted lays one block out per kind and colours it from the
// theme's zone styles. Its width is the budget already less any quote bar --
// see withQuoteBar, which is what the document view calls.
func renderBlockPainted(b Block, width int, st *Styles, onCard bool) []string {
	switch b.Kind {
	case KindHeading:
		// ONE colour for every heading at every level, sigil and words
		// alike, separated only by weight. Level is carried by the sigil's
		// LENGTH -- § against §§ against §§§ -- and by nothing else.
		//
		// THE TAB AND EVERY OTHER CONTROL BYTE ARE SPENT HERE, where the bytes
		// are DRAWN, and Block.Text is left alone: Text is the anchor side of
		// the mapping and a heading is its section's key, so filtering it would
		// refuse a comment on every benign block beneath the heading. This arm
		// draws Text raw and never calls renderInlines, so renderControls
		// (ui/control.go) is the only filter in front of it -- and it is also
		// where an inline LINK's DESTINATION is filtered, this being one of the
		// three places a destination reaches a screen at all.
		glyphStyle, style := st.HeadingGlyph, st.Heading
		glyph := glyphStyle.Render(strings.Repeat("§", min(b.Level, 3)) + " ")
		return []string{glyph + renderControls(fourSpaceTabs(b.Text), style)}
	case KindCode:
		// STRAIGHT OFF Text, line for line: a fence's bytes are what renders,
		// so there is no projection to read and displayIn is never asked.
		// Which is why a QUOTED fence shows its own '> ' markers on every line
		// but the first -- Text is a raw source span and goldmark strips the
		// marker from the first line's segment only. Recorded at Block.Text
		// (ui/markdown.go) with what it would cost to fix; not fixed here.
		//
		// visibleControls (ui/control.go) runs BEFORE wrapCodeRow measures the
		// row: a bare C0 byte is ZERO cells to ansi.StringWidth and ONE to
		// lipgloss's wrap, where a Control Picture is one to both, so the filter
		// is a measure fix as well as a security one.
		//
		// IT WRAPS, IT DOES NOT TRUNCATE. Truncating here decided what a
		// reviewer was allowed to read out of a document whose bytes they are
		// being asked to approve; see wrapCodeRow and
		// TestCodeWrapsAndKeepsEveryByte.
		//
		// dropIndent runs BEFORE fourSpaceTabs, because CodeIndent counts SOURCE
		// bytes and fourSpaceTabs multiplies one of them by four.
		var out []string
		for _, l := range strings.Split(strings.TrimRight(b.Text, "\n"), "\n") {
			pieces, lead := wrapCodeRow(visibleControls(fourSpaceTabs(dropIndent(l, b.CodeIndent)), st.CodeText), width-2)
			for i, piece := range pieces {
				if i > 0 {
					piece = lead + piece
				}
				out = append(out, st.CodeText.Render("  "+piece))
			}
		}
		return out
	case KindListItem:
		indent := strings.Repeat("  ", b.ListDepth-1)
		lines := wrapPlain(b.displayIn(onCard, st.Text), width-len(indent)-2)
		out := make([]string, len(lines))
		for i, l := range lines {
			bullet := indent + "  "
			if i == 0 {
				bullet = indent + "• "
			}
			// b.display()'s inline runs each end in their own SGR reset, so
			// a wrap point that falls inside one leaves that physical line
			// starting with no color at all. Re-wrapping the whole
			// reconstructed line in st.Text restores a base color for that
			// line, falling back to plain Text rather than the split leaf's
			// own styling.
			out[i] = st.Text.Render(bullet + l)
		}
		return out
	case KindRule:
		// A THEMATIC BREAK IS THE ONE BLOCK DRAWN INSTEAD OF WRITTEN OUT: its
		// Text is `---` or `***` or `___`, which is markup for "a break goes
		// here". Every other arm in this switch renders the block's own words.
		//
		// IN st.Dim AND NOT st.SectionRule, which name the identical two
		// colours (ui/styles.go): Dim is one of the styles OnCard rebases and
		// SectionRule is not, so on a commented block's row SectionRule would
		// paint a Doc-coloured rule inside a Card-coloured row.
		//
		// THE WIDTH IS THE BUDGET IT IS HANDED, already less any quote bar
		// (withQuoteBar). Clamped at zero because that budget is arithmetic on
		// the terminal's own width and strings.Repeat PANICS on a negative
		// count where the other arms merely wrap badly.
		return []string{st.Dim.Render(strings.Repeat("─", max(width, 0)))}
	case KindTableRow:
		// ONE CELL PER LINE, each cell wrapped like prose. The cells arrive
		// newline-separated from joinCells (ui/markdown.go), which is the only
		// thing in a projection that can produce a '\n'.
		//
		// THIS ARM IS THE GRID'S FALLBACK AND NOT THE WAY A TABLE IS DRAWN. A
		// table is painted before the document loop (paintTables) and a row is
		// drawn from its slice of it, so this runs only for a row the pre-pass
		// drew NOTHING for -- one clamped budget of 1, a terminal far below
		// app/list.go's own minimum-size gate. It is kept because the
		// alternative is a row that draws no lines at all, which silently
		// satisfies "a block with no entry was not painted" while taking the
		// row's anchor off the screen.
		//
		// A CONTINUATION IS INDENTED AND A CELL IS NOT, which is the whole
		// reason this is an arm rather than the default's single wrap: every
		// cell begins at the left edge and only a cell does, so a wrapped value
		// reads as more of the same value instead of as the next column. The
		// indent comes out of the budget for every line, the way the
		// KindListItem arm above spends its bullet.
		var out []string
		for _, cell := range strings.Split(b.displayIn(onCard, st.Text), "\n") {
			for i, l := range wrapPlain(cell, width-tableContinuationCols) {
				if i > 0 {
					l = strings.Repeat(" ", tableContinuationCols) + l
				}
				out = append(out, st.Text.Render(l))
			}
		}
		return out
	default:
		lines := wrapPlain(b.displayIn(onCard, st.Text), width)
		out := make([]string, len(lines))
		for i, l := range lines {
			out[i] = st.Text.Render(l)
		}
		return out
	}
}

// tableContinuationCols is how far a wrapped table value is indented under
// the cell it belongs to. Spent twice like docCardIndent and the quote bar --
// once rendered, once subtracted from the width budget -- and two cells is
// what the list arm's bullet already costs, so a table row and a list item
// hang their continuations in the same column.
const tableContinuationCols = 2

// codeTabCols is what one TAB costs on the two arms that draw Block.Text raw
// -- KindHeading and KindCode. FOUR, and four exactly, because that is what
// the renderer below already does with one: lipgloss is a fixed-width tab
// REPLACER and not a tab-stop advancer, so `a\tb` paints `a    b` wherever
// the tab falls in the row. Four is also what the four PROJECTION arms
// already carry, so this constant does not introduce a convention -- it makes
// the two raw arms agree with the five things that already had one.
const codeTabCols = 4

// fourSpaceTabs is the tab a raw arm is about to draw, spelled as the four
// columns the renderer is going to paint it as.
//
// IT IS A MEASURE FIX AND ONLY INCIDENTALLY A LAYOUT ONE. ansi.StringWidth and
// ansi.Truncate count a tab as ZERO cells; lipgloss paints it as four. So an
// arm that measured Text and then rendered it through Width() believed a row
// fit, the renderer found it did not, and wrapped it there -- inside the single
// string that becomes one ui.Line. See TestACodeLineIsOneScreenRow.
//
// IT DOES NOT TOUCH Block.Text, and must not: Text is the anchor side of the
// mapping and reanchor searches the raw document for it verbatim. FOUR SPACES
// rather than a Control Picture is the same fact from the other side --
// reanchor.Normalize folds runs of whitespace, so four spaces and a tab
// normalise identically and no anchorable span moves, where U+2409 moves 175 of
// them.
func fourSpaceTabs(s string) string {
	return strings.ReplaceAll(s, "\t", strings.Repeat(" ", codeTabCols))
}

// dropIndent takes up to n leading SPACES off one source row of a fence -- the
// columns its container strips and Block.Text's span puts back. It stops at the
// first byte that is not a space, so a row indented LESS than its fence keeps
// what it has and a row indented more keeps the difference, which is the code's
// own structure and not the container's.
//
// n IS Block.CodeIndent and never a count taken from the text, which is the
// whole reason the field exists: the minimum indentation of a fence's own
// continuation lines is a property of the CODE, and removing it would be this
// arm deciding what a reviewer's code means.
func dropIndent(l string, n int) string {
	i := 0
	for i < n && i < len(l) && l[i] == ' ' {
		i++
	}
	return l[i:]
}

// codeContinuationFloor is the narrowest budget a wrapped code continuation
// may be left with. wrapPlain floors its own width at the same 10: a
// continuation given two or three columns makes almost no progress per row, so
// a long line comes back as a column of fragments rather than as wrapped code.
const codeContinuationFloor = 10

// wrapCodeRow splits ONE source row of a fenced block into the screen rows it
// is drawn as, and reports the indent every row after the first is drawn
// under. The pieces are returned BARE and the caller draws the lead, because a
// piece that already carried its own indent could not be checked against the
// source it came from.
//
// THE PROPERTY IS "NOTHING IS LOST", AND IT IS EXACT ON AN ESCAPE-FREE ROW AND
// NOT BYTE-EXACT ON ONE CARRYING AN ESCAPE SEQUENCE: ansi.Truncate and
// ansi.TruncateLeft close an SGR run or an OSC 8 hyperlink at the cut and
// reopen it on the next piece, so content is DUPLICATED there, never dropped
// and never split down the middle. TestCodeWrapsAndKeepsEveryByte asserts both
// halves. The arm runs visibleControls first, so no document escape reaches
// here and the reopening only ever duplicates a colour code.
//
// THE INDENT IS RELATIVE TO THE LINE'S OWN INDENT, NOT TO THE LEFT MARGIN,
// which is why lead is computed here rather than being a constant: a
// continuation of a four-levels-deep statement drawn at column 0 appears to the
// LEFT of the statement it continues and reads as a dedent.
//
// THE CUT IS ansi.Truncate AND THE REMAINDER IS ansi.TruncateLeft, both by
// CELLS and not by bytes, so neither a multi-byte rune nor an escape sequence
// can be split down the middle by the arithmetic. A grapheme too wide for the
// budget all on its own is the one case nothing can cut: the row goes out
// over-wide rather than the loop spinning.
func wrapCodeRow(l string, width int) (pieces []string, lead string) {
	if width < 1 || ansi.StringWidth(l) <= width {
		return []string{l}, ""
	}
	lead = codeContinuationLead(l, width)
	rest, budget := l, width
	for ansi.StringWidth(rest) > budget {
		head := ansi.Truncate(rest, budget, "")
		kept := ansi.StringWidth(head)
		if kept == 0 {
			break
		}
		pieces = append(pieces, head)
		rest, budget = ansi.TruncateLeft(rest, kept, ""), width-len(lead)
	}
	return append(pieces, rest), lead
}

// codeContinuationLead is the hanging indent for one source row: the row's own
// leading spaces plus tableContinuationCols. The row is already through
// fourSpaceTabs when it gets here, so its indent is spaces and len is cells.
//
// A ROW INDENTED ALMOST TO THE BUDGET WOULD HANG ITS CONTINUATION OFF THE
// RIGHT EDGE, so the lead gives way rather than the content: first back to the
// bare tableContinuationCols, then to nothing at all.
func codeContinuationLead(l string, width int) string {
	own := len(l) - len(strings.TrimLeft(l, " "))
	for _, cols := range []int{own + tableContinuationCols, tableContinuationCols, 0} {
		if width-cols >= codeContinuationFloor {
			return strings.Repeat(" ", cols)
		}
	}
	return ""
}

// clampTableWidth floors a table's width budget at 1 before tableGridString
// (below) hands it to lipgloss/v2/table's own Width call. It is a pure
// budget -> clamped budget function; the frame invariant it exists FOR -- no
// rendered line wider than the budget -- is asserted against the real grid, in
// TestTableGridFitsItsWidthBudget.
//
// A GRID CANNOT SHARE KindRule'S FLOOR OF ZERO, because zero means something
// different to the two callers. To strings.Repeat, 0 unambiguously means "draw
// nothing". To lipgloss/v2/table, Width(0), Width(a negative number) and Width
// NEVER CALLED are the same thing: NATURAL width, as wide as the widest cell
// wants to be, entirely ignoring the budget. A rule clamped to max(width, 0)
// fails loud, once, at the call that would have panicked; a grid clamped the
// same way fails SILENT -- every call succeeds and the grid is simply the wrong
// size. Unclamped, a real corpus's one 7-column table renders 221 cells wide
// against a budget of 76, every line of it carrying an embedded newline.
//
// The budget only reaches <= 0 at terminal widths 6 / 8 / 10 for blockquote
// depth 1 / 2 / 3, far below any real terminal, so this floor is insurance
// rather than a defect it is fixing in the field.
func clampTableWidth(width int) int {
	if width < 1 {
		return 1
	}
	return width
}

// tableGridCache is the grid pre-pass's memo: ONE ENTRY, holding the last
// arguments paintTables was called with and what it answered for them. It
// exists because RenderDoc runs on every cursor step (moveCursor, app/model.go)
// and painting the grids of the largest plan in a real corpus costs about 120
// ms where a hit on this cache costs about 7 us. BenchmarkTableGrid
// (ui/grid_bench_test.go) is the committed harness for both figures.
//
// WHAT THE KEY MUST CONTAIN IS EXACTLY WHAT paintTables READS, and the list is
// derived from that function rather than guessed at, because a key missing a
// dependency is a stale render that no test would show:
//
//   - THE WIDTH. It is the budget every column is allocated out of.
//   - THE *Styles, BY POINTER. A Styles is built once by NewStyles and never
//     written to after that, so the pointer stands for the contents. A second
//     pointer with identical contents is a MISS and not a wrong hit, which is
//     the direction an identity key is allowed to be wrong in.
//   - EVERY KindTableRow BLOCK, IN ORDER, AND ITS BLOCK INDEX. The index is
//     what the answer is keyed by, so a paragraph inserted above a table moves
//     every entry even though nothing about the table changed.
//   - ITS TableRef. ID groups the rows into tables and Header decides how the
//     row's cells are drawn.
//   - ITS QuoteDepth, which comes out of the width budget.
//   - ITS Cells, ALL THREE PROJECTIONS OF EVERY CELL. This is the text on
//     screen, and it is compared by value.
//   - THE ZONE ASSIGNMENT, one bool per table row. views reaches the painter
//     ONLY through onCard, so the boolean is the whole of that dependency and
//     the threads themselves are none of it.
//   - WHETHER A COMMENT CARD IS DRAWN UNDER THE ROW, one more bool per table
//     row, and a SECOND fact about the same threads rather than a restatement
//     of the one above. A card between two rows closes the box above it and
//     reopens it below (gridRowGroup), so expanding or collapsing a thread
//     changes which LINES the grid holds, where a thread that merely exists
//     changes only their colour. Pinned by the "whether a comment card is
//     drawn under a row" case of
//     TestPaintedTableCacheHitsOnACursorStepAndOnNothingElse.
//
// WHAT IT DELIBERATELY LEAVES OUT, and this is the half that makes it hit on
// every arrow press: THE CURSOR. Block painting is cursor-independent -- the
// cursor enters renderDocPainted only through cursorGlyph and band, both
// applied when a row is wrapped, and never reaches withQuoteBar or the
// pre-pass. unanchored and pseudonym are out for the same reason: they are read
// where the CARDS are drawn, not where a block is painted.
//
// SO A CHANGE THAT MAKES THE PAINTED GRID DEPEND ON ANYTHING ELSE MUST EXTEND
// THIS KEY IN THE SAME COMMIT, and the list above is to be re-derived from
// paintTables rather than trusted.
//
// IT IS BOUNDED AND IT NEVER SHRINKS. Bounded: one entry, replaced wholesale on
// a miss, rows[:0] reusing its backing array. Never shrinks: nothing
// invalidates the entry, so closing a document leaves its rendered grid and
// cell text -- about 1.6 MB for the largest plan in a real corpus -- alive for
// the process lifetime. Dropping the entry on view exit would cost one
// repaint on re-entry and nothing else.
//
// THE ANSWER IS SHARED, NOT COPIED, so callers must treat the returned map and
// its slices as READ-ONLY: see quoteBarLead, which is the one place that would
// otherwise write into them.
var tableGridCache struct {
	mu      sync.Mutex
	valid   bool
	width   int
	st      *Styles
	rows    []tableGridKeyRow
	painted map[int][]string
}

// tableGridKeyRow is one table row's contribution to the memo key: everything
// paintTables reads off a Block, plus the three things that are not on the
// Block at all -- where it sits in the block list, which zone it is painted in,
// and whether a comment card is drawn under it.
type tableGridKeyRow struct {
	idx       int
	ref       TableRef
	depth     int
	onCard    bool
	cardBelow bool
	cells     []TableCell
}

// paintTablesCached is paintTables behind tableGridCache. Same arguments, same
// answer; see the cache for what the key is and why.
func paintTablesCached(blocks []Block, width int, st *Styles, onCard, cardBelow func(blockIdx int) bool) map[int][]string {
	onCard, cardBelow = orNever(onCard), orNever(cardBelow)
	tableGridCache.mu.Lock()
	defer tableGridCache.mu.Unlock()
	if tableGridCacheHit(blocks, width, st, onCard, cardBelow) {
		return tableGridCache.painted
	}
	painted := paintTables(blocks, width, st, onCard, cardBelow)
	tableGridCache.valid = true
	tableGridCache.width, tableGridCache.st, tableGridCache.painted = width, st, painted
	tableGridCache.rows = tableGridCache.rows[:0]
	for i, b := range blocks {
		if b.Kind != KindTableRow {
			continue
		}
		tableGridCache.rows = append(tableGridCache.rows, tableGridKeyRow{
			idx:       i,
			ref:       b.Table,
			depth:     b.QuoteDepth,
			onCard:    onCard(i),
			cardBelow: cardBelow(i),
			// COPIED, not aliased: a caller that rewrote a cell in place
			// would otherwise rewrite the key along with the blocks, and
			// the comparison below would answer "unchanged" about a
			// document that had changed.
			cells: slices.Clone(b.Cells),
		})
	}
	return painted
}

// tableGridCacheHit answers whether the cached answer was painted for these
// arguments. It allocates nothing: the walk compares against the stored key in
// place, so a hit costs one pass over the document's table rows.
//
// The caller holds tableGridCache.mu.
func tableGridCacheHit(blocks []Block, width int, st *Styles, onCard, cardBelow func(int) bool) bool {
	if !tableGridCache.valid || tableGridCache.width != width || tableGridCache.st != st {
		return false
	}
	n := 0
	for i, b := range blocks {
		if b.Kind != KindTableRow {
			continue
		}
		if n == len(tableGridCache.rows) {
			return false
		}
		k := tableGridCache.rows[n]
		if k.idx != i || k.ref != b.Table || k.depth != b.QuoteDepth || k.onCard != onCard(i) || k.cardBelow != cardBelow(i) {
			return false
		}
		if !slices.Equal(k.cells, b.Cells) {
			return false
		}
		n++
	}
	return n == len(tableGridCache.rows)
}

// orNever reads a nil per-row question as "no row". Each of the pre-pass's two
// questions is asked in two places -- the memo's key and the painter -- so the
// default lives in one function rather than in four nil checks that can drift
// apart.
func orNever(ask func(int) bool) func(int) bool {
	if ask == nil {
		return func(int) bool { return false }
	}
	return ask
}

// paintTables is the grid pre-pass: every table in blocks drawn once as a
// bordered grid, cut into per-row line groups, and returned keyed by the BLOCK
// INDEX of the row each group belongs to.
//
// A BLOCK INDEX WITH NO ENTRY is a block this pass drew nothing for, which the
// caller has to handle. Three things get skipped: anything that is not a
// KindTableRow, a KindTableRow whose Table.ID is 0, and a table whose render
// would not split (splitTableGrid records the single budget at which that
// fires). The middle guard is unreachable -- no table is table 0 -- and is kept
// because a zero there would mean a KindTableRow that came from somewhere this
// function does not know about, which is not a shape to paint on a guess.
//
// IT IS A PRE-PASS BECAUSE A COLUMN IS A PROPERTY OF THE WHOLE TABLE and
// renderBlockPainted paints ONE block against ONE width. So the table is
// rendered whole, by lipgloss/v2/table, and then cut up -- which keeps that
// package's column resizer, its wrap and its corner arithmetic.
//
// THE WIDTH IS THE ONE withQuoteBar IS HANDED, not the one it passes on. This
// function takes the quote bar out of the budget itself, so a caller that
// subtracted it again would draw every quoted grid two cells narrow per level.
//
// onCard answers "is this block painted on the Card zone" and cardBelow "does
// renderDocPainted draw comment cards UNDER this block" -- renderDocPainted's
// own questions, passed in rather than recomputed so the two cannot come to
// disagree. They are different questions: a COLLAPSED thread puts its row on
// Card without putting anything between that row and the next. Nil means no row
// is, or has one.
func paintTables(blocks []Block, width int, st *Styles, onCard, cardBelow func(blockIdx int) bool) map[int][]string {
	if st == nil {
		return nil
	}
	onCard, cardBelow = orNever(onCard), orNever(cardBelow)
	// Table.ID and not adjacency, which is TableRef's own ruling: two tables
	// separated by a blank line are block-adjacent with nothing between them.
	// Grouping by the identity also means nothing here depends on a table's
	// rows being contiguous in the block list.
	byTable := map[int][]int{}
	var order []int
	for i, b := range blocks {
		if b.Kind != KindTableRow || b.Table.ID == 0 {
			continue
		}
		if _, seen := byTable[b.Table.ID]; !seen {
			order = append(order, b.Table.ID)
		}
		byTable[b.Table.ID] = append(byTable[b.Table.ID], i)
	}
	out := make(map[int][]string)
	for _, id := range order {
		paintOneTable(blocks, byTable[id], width, st, onCard, cardBelow, out)
	}
	return out
}

// paintOneTable draws the table whose rows are at idxs and writes each row's
// line group into out. It is where THE TWIN RENDER lives, which is what makes a
// grid survive this view's two colour zones.
//
// WHY TWICE. lipgloss/v2/table has ONE BorderStyle for the whole table, and
// nothing reaches a single row's borders: every border glyph goes through
// t.borderStyle, and constructRow builds a data row's LEFT border once and
// reuses that same string as the row's internal COLUMN DIVIDERS -- so a
// commented row painted from a Doc-coloured table would have Doc-coloured
// stripes running THROUGH it, not merely above and below. So the table is
// rendered twice, once per zone, and each row's group is taken from the twin
// matching that row's own zone.
//
// THE CONDITION THAT MAKES IT SAFE, STATED HERE RATHER THAN RELIED ON: the two
// twins are geometrically identical only because their cell styles have
// IDENTICAL PADDING, MARGINS, WIDTH AND HEIGHT and differ in colour alone.
// lipgloss's resize() reads exactly those four box metrics off a cell style and
// consults borderStyle for geometry nowhere at all, so a Card cell style that
// added padding "to make room" would silently move every commented row's
// columns and nothing would say so. tableGridStyles is where both twins get
// their styles, and it is one function for that reason.
//
// A SEPARATOR TAKES THE ZONE OF THE ROW IT IS ATTRIBUTED TO, and so does a
// border the splice puts in (gridRowGroup): renderDocPainted paints every Line
// onto its own block's background, so a rule drawn from the other twin is the
// same two-zone failure the twins exist to avoid, one row down.
func paintOneTable(blocks []Block, idxs []int, width int, st *Styles, onCard, cardBelow func(int) bool, out map[int][]string) {
	rows := make([]Block, len(idxs))
	for i, idx := range idxs {
		rows[i] = blocks[idx]
	}
	// One depth for the whole table, read off its first row. A table is ONE
	// AST node and ParseBlocks stamps every block it emits with the quote depth
	// of the containers it is standing in, so a table's rows cannot disagree
	// about how deeply the table is quoted. The bar itself is still drawn per
	// row, by withQuoteBar, on rows this function only budgets for.
	//
	// NOT CLAMPED HERE. clampTableWidth belongs to the one call that hands a
	// width to lipgloss (tableGridString), so this budget may legitimately be
	// zero or negative and is floored exactly once, where it is spent.
	budget := width - quoteBarCols(rows[0].QuoteDepth)
	doc, docOK := splitTableGrid(tableGridString(rows, budget, st, false), len(rows))
	card, cardOK := splitTableGrid(tableGridString(rows, budget, st, true), len(rows))
	if !docOK || !cardOK {
		return
	}
	for i, idx := range idxs {
		twin := doc
		if onCard(idx) {
			twin = card
		}
		// THE LAST ROW NEEDS NOTHING CLOSED: its group already ends with the
		// box's bottom border.
		out[idx] = gridRowGroup(twin, i, cardBelow(idx) && i < len(idxs)-1, i > 0 && cardBelow(idxs[i-1]))
	}
}

// gridRowGroup is row i's line group taken from twin, with the box CLOSED above
// a comment card and REOPENED below one: two boxes with the comment between
// them, and the card outside the content in the gutter's indent exactly as it
// sits under every other kind of block.
//
// closes SUBSTITUTES THE ROW'S TRAILING SEPARATOR AND MUST NOT FOLLOW IT. A
// group ends with the rule that CLOSES it (splitTableGrid), so a bottom border
// appended after that rule draws a '├───┼───┤' immediately above a '└───┴───┘'.
//
// reopens PREPENDS THE BOX'S OWN OPENING AND DOES NOT REPEAT THE HEADER: under
// a grid the header is drawn ONCE per table, commented or not.
//
// EVERY LINE HERE COMES OUT OF THE SINGLE RENDER, which is the constraint that
// rules out the obvious implementation. A SECOND table.Table built for the
// reopened half computes its own column widths from the rows it was handed, and
// lipgloss has no per-column width to force it back into line with the first:
// measured over a real corpus, 341 of the 826 (table, split point) pairs move a
// column boundary that way -- median 6 cells, 35 at the worst.
//
// AND OFF THIS TWIN AND NOT THE OTHER: the closing border belongs to the row it
// closes and the opening one to the row it opens, so each wears that row's
// zone. Either taken from the wrong twin is the two-zone failure the twin
// render exists to prevent.
func gridRowGroup(twin [][]string, i int, closes, reopens bool) []string {
	group := twin[i]
	if !closes && !reopens {
		return group
	}
	spliced := make([]string, 0, len(group)+1)
	if reopens {
		spliced = append(spliced, gridOpening(twin)...)
	}
	spliced = append(spliced, group...)
	if closes {
		spliced[len(spliced)-1] = gridClosing(twin)
	}
	return spliced
}

// gridOpening is the box's own top border: the lines of the FIRST row's group
// that come before its first content line.
//
// READ OFF THE RENDER AND NOT OFF THE BUILDER. Whether there IS a top border is
// decided by BorderTop, a builder call in tableGridString, and a "take line 0"
// that silently depended on it would prepend THE HEADER'S WORDS to every
// reopened box the day that call changed. With BorderTop off there is no
// opening line and this answers with none.
//
// The scan is a shape no test covers and none can -- BorderTop is set
// unconditionally, so `first[:j]` is always `first[:1]` and mutating this whole
// function to `return twin[0][:1]` leaves the suite green -- and it is kept for
// exactly the day that changes. A group with no content line at all cannot
// reach here: splitTableGrid closes a group only once it has seen one.
func gridOpening(twin [][]string) []string {
	runes := borderRunesOf(tableGridBorder())
	first := twin[0]
	for j, line := range first {
		if !borderOnlyLine(line, runes) {
			return first[:j]
		}
	}
	return nil
}

// gridClosing is the box's own bottom border: the last line of the LAST row's
// group. It needs no classification where gridOpening does -- splitTableGrid
// closes a group only ON a border line and answers false if the last row was
// left open, so a twin that reached here has one and it is that line.
func gridClosing(twin [][]string) string {
	last := twin[len(twin)-1]
	return last[len(last)-1]
}

// tableGridBorder is the box a grid is drawn in. It is a FUNCTION and not a
// literal at either call site because it has two callers that must never
// disagree: the table draws with it, and splitTableGrid classifies lines
// against the runes in it.
func tableGridBorder() lipgloss.Border { return lipgloss.NormalBorder() }

// tableGridPadCols is the horizontal padding inside every grid cell, spent by
// lipgloss out of the column width (it reads it back through
// GetHorizontalFrameSize) rather than by anything here.
//
// IT IS ONE NUMBER FOR BOTH TWINS AND FOR EVERY CELL, which is the condition
// paintOneTable states in full: twins whose cell styles differ in padding are
// twins whose columns do not line up.
//
// AND IT IS WHAT MAKES splitTableGrid's CLASSIFICATION SAFE. A content line is
// `│ text │ text │` and a separator is `├──────┼──────┤`; the padding is what
// guarantees at least one SPACE on every content line, so "every rune is a
// border rune" cannot match a row of content -- not even content that is ITSELF
// box-drawing. Set to 0, a one-column table whose only cell is `─` renders
// `│─│` and splitTableGrid answers false.
//
// closeGridRight is its other reader and wants the same cell: at a budget of 2
// the padding is the only cell a line has left for the closing border, and it
// declines there, for the reason above.
const tableGridPadCols = 1

// tableGridString renders one table as a style-A grid: a normal box, rules
// between every row and every column, all four outer borders, wrapping cells.
// The rows arrive in table order, rows[0] being the header.
//
// THE HEADER IS DATA ROW 0 AND NEVER Headers(), which is not a preference. In
// the pinned v2.0.5, constructHeaders truncates every header cell
// UNCONDITIONALLY -- unlike constructRow it does not ask whether Wrap is on --
// and truncateCell then forces the header's height to 1 whatever the resizer
// computed. So a header cell wider than its column LOSES TEXT; as data row 0 it
// wraps like any other row. Nothing is given up by not asking for Headers():
// style A draws a rule under EVERY row anyway, and a header cell is styled here
// before the table ever sees it (tableGridRow).
//
// THE CELLS COME FROM Block.Cells AND NEVER FROM Display, DisplayCard OR
// DisplayPlain. Those three are the cells JOINED with '\n', and the join drops
// an empty cell, so splitting one back gives the cells that had text in them
// and nothing about which COLUMN each sat in -- 31 of a real corpus's 2,543
// cells are empty and not the last of their row.
func tableGridString(rows []Block, width int, st *Styles, onCard bool) string {
	cell, text := tableGridStyles(st, onCard)
	grid := table.New().
		Border(tableGridBorder()).
		BorderTop(true).
		BorderBottom(true).
		BorderLeft(true).
		BorderRight(true).
		BorderRow(true).
		BorderColumn(true).
		Wrap(true).
		// ALWAYS SET, and clamped before it gets here: Width(0), Width(a
		// negative) and Width never called are one thing to this package --
		// natural width, as wide as the widest cell wants to be. See
		// clampTableWidth.
		Width(clampTableWidth(width)).
		BorderStyle(borderStyleOf(st, onCard)).
		StyleFunc(func(int, int) lipgloss.Style { return cell })
	for _, b := range rows {
		grid.Row(tableGridRow(b, text, onCard)...)
	}
	return closeGridRight(grid.Render(), clampTableWidth(width), borderStyleOf(st, onCard))
}

// closeGridRight puts the right-hand border back on any line of a rendered grid
// that came out without one, keeping the line exactly as wide as it was.
//
// WHAT IT IS REPAIRING, and it is lipgloss's arithmetic rather than this
// package's. table.Render ends with MaxWidth(t.width) over the whole table, and
// in a band of budgets immediately BELOW a table's natural width the resizer
// allocates columns that sum to one cell MORE than the budget -- so what that
// truncation cuts is the right-hand border: every line comes back exactly the
// budget wide, splitTableGrid still cuts it into rows, and the box has NO RIGHT
// EDGE AT ALL. The band only bites just below natural width, which is why 160
// and 80 -- the two widths anybody actually uses -- were both clean and the
// defect went unnoticed for so long.
//
// A RETRY IS NOT THE FIX. Re-rendering at the budget less 1, 2 or 3 closes 388
// of 1,538 open renders, 25.2%; the other three quarters stay open however far
// the retry is taken, and which widths they close at is unpredictable. The
// resizer does not converge; it has to be told.
//
// THE CLOSER IS RENDERED THROUGH THE TWIN'S OWN BORDER STYLE. A bare rune
// appended here lands UNSTYLED -- on a Card row, a zone-less cell inside a
// Card-coloured line, which is exactly the two-zone failure paintOneTable
// renders two twins to avoid. It takes the style rather than reading it back
// off st so that the twin decides it, once.
//
// IT TRUNCATES TO AN ABSOLUTE WIDTH rather than dropping one cell, so the
// arithmetic holds however many cells lipgloss over-allocated: the line is cut
// to width less the closer, PADDED BACK OUT IF THE CUT CAME UP SHORT, and the
// closer put back -- which is width exactly.
//
// AND IT COSTS A CHARACTER, SOMETIMES. Usually the cell it cuts is the last
// column's right-hand PADDING and nothing is lost, but on a line whose last
// cell fills its column EXACTLY the closer takes a character of the reader's
// text with it. Over a real 54-file corpus that is 93 of 383,993 cell renders,
// 0.024%, every one inside the band where the box was open, and there is no way
// round it that keeps the width. Pinned by the width-92 case of
// TestFidelityGridFiguresAreWhatWasMeasured.
//
// WHICH CLOSER depends on the row: a line opened by the box's top-left corner
// is closed by its top-right, a separator by the middle-right, the bottom by
// the bottom-right, and anything else -- a content row -- by the plain Right.
// All four come off the border the grid was drawn with (tableGridBorder), for
// the reason splitTableGrid reads its rune set off it rather than naming '├'.
//
// The ansi.Strip below is on a RENDERED line and stays one, where a prior fix
// replaced ansi.Strip with stripStyling everywhere it read a PROJECTION.
func closeGridRight(rendered string, width int, border lipgloss.Style) string {
	b := tableGridBorder()
	runes := borderRunesOf(b)
	closed := map[rune]bool{}
	for _, part := range []string{b.Right, b.TopRight, b.BottomRight, b.MiddleRight} {
		for _, r := range part {
			closed[r] = true
		}
	}
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		plain := []rune(ansi.Strip(line))
		if len(plain) == 0 || closed[plain[len(plain)-1]] {
			continue
		}
		closer := b.Right
		switch string(plain[0]) {
		case b.TopLeft:
			closer = b.TopRight
		case b.MiddleLeft:
			closer = b.MiddleRight
		case b.BottomLeft:
			closer = b.BottomRight
		}
		// THE CUT IS THEN PADDED BACK, because ansi.Truncate cannot do the
		// one thing this arithmetic assumed it could: it never SPLITS a
		// double-width glyph, so when the cut lands inside one it cuts
		// BEFORE it and hands back a line one cell short --
		// `ansi.Truncate("abc✅", 4, "")` is `"abc"`, three cells, against
		// `ansi.Truncate("abcd", 4, "")`'s four. The pad is spent where the
		// glyph was: inside the last column, immediately left of the
		// border, which keeps the box's right-hand edge at the frame edge.
		//
		// IT IS RENDERED WITH THE CLOSER, in ONE nested Render and not two,
		// for renderDocPainted's own rule: a naked fragment after a
		// Render's SGR reset shows the terminal's default background. The
		// pad goes through the BORDER's style rather than the cell's, which
		// is not a choice about colour -- the two carry the identical
		// background in both zones and a space has no ink to show -- but
		// the same discipline the closer itself follows.
		//
		// THE EARLY continue ABOVE CANNOT SKIP A SHORT LINE, and that is
		// closed by construction rather than by a branch: MaxWidth
		// truncates with the same ansi.Truncate, so a line comes back short
		// only when the cut lands inside a double-width glyph, and for the
		// SURVIVING last rune to be a border rune that glyph would have to
		// sit immediately after one. Nothing in this render does --
		// tableGridPadCols is 1, so every cell opens with a SPACE after its
		// left border, and a rule line is drawn entirely from single-cell
		// box glyphs.
		want := max(width-ansi.StringWidth(closer), 0)
		cut := ansi.Truncate(line, want, "")
		repaired := cut + border.Render(strings.Repeat(" ", max(want-ansi.StringWidth(cut), 0))+closer)
		// THE REPAIR YIELDS TO THE CUT, and this branch is the whole of the
		// trade. splitTableGrid tells a rule from a content row by asking
		// whether every rune of the line is a border rune, and what
		// guarantees a content row has one that is not is tableGridPadCols
		// -- the space every cell carries. At a budget so narrow that a
		// column is allocated no width at all, spending that space on the
		// closer turns a content row into something indistinguishable from
		// a separator: the cut then fails for the whole table and
		// paintTables draws NOTHING for any of its rows. A box open on the
		// right at a budget with no columns is the smaller loss.
		//
		// It fires at EXACTLY ONE budget, 2, where it fires for every table
		// in a real corpus; at 3 and above every box closes. The app cannot
		// reach any of them: listMinWidth is 31.
		if borderOnlyLine(repaired, runes) && !borderOnlyLine(line, runes) {
			continue
		}
		lines[i] = repaired
	}
	return strings.Join(lines, "\n")
}

// tableGridRow is one row's cells as the strings the grid is drawn from.
//
// A CELL IS ALREADY A PROJECTION -- Block.Cells carries the rendered inline
// walk for the Doc zone, for the Card zone, and with the styling taken back
// off -- so for a body cell this PICKS the one matching the zone rather than
// rendering one. The default arm covers an EMPTY body cell, whose three
// projections are all "", and a block ParseBlocks was handed no Styles, where
// only the plain projection was computed.
//
// THE HEADER IS DRAWN FROM THE PLAIN PROJECTION AND RE-RENDERED BOLD, and that
// is a real trade rather than a shortcut. A cell's Display is a run of
// independently rendered leaves, each ending in its own SGR reset, so a bold
// applied around the outside reaches the FIRST leaf and stops -- the first
// leaf's own reset clears it before the second begins, and a header of some
// bold cells and some half-bold ones reads as a rendering fault. What drawing
// every header cell from DisplayPlain costs is the colour of the 14 header
// cells in a real corpus that carry a code span. The markup CHARACTERS stay
// off screen either way -- DisplayPlain is the projection with the styling
// removed, not the source.
func tableGridRow(b Block, text lipgloss.Style, onCard bool) []string {
	out := make([]string, len(b.Cells))
	for j, c := range b.Cells {
		switch {
		case b.Table.Header:
			out[j] = text.Bold(true).Render(c.DisplayPlain)
		case onCard && c.DisplayCard != "":
			out[j] = c.DisplayCard
		case c.Display != "":
			out[j] = c.Display
		default:
			out[j] = text.Render(c.DisplayPlain)
		}
	}
	return out
}

// tableGridStyles is where BOTH twins get their cell styles, and it is one
// function so that the condition paintOneTable states cannot be broken in one
// twin and not the other: the two differ in BACKGROUND ALONE, and every box
// metric lipgloss's resizer reads is the same value on both sides.
//
// It returns two styles and not one because they are spent in different places.
// cell is what the table renders a cell WITH, padding included, and is handed
// to StyleFunc. text is what a header cell's words are rendered with BEFORE
// they reach the table, and it must carry no padding at all: the table adds
// cell's padding around whatever it is given, so padding here would be spent
// twice and the column would be two cells narrow.
func tableGridStyles(st *Styles, onCard bool) (cell, text lipgloss.Style) {
	if onCard {
		st = st.OnCard()
	}
	return st.Text.Padding(0, tableGridPadCols), st.Text
}

// borderStyleOf is the grid's box in the Dim/rule colour, on whatever zone the
// twin is being drawn for.
//
// st.Dim AND NOT st.SectionRule, which name the identical two colours
// (ui/styles.go): Dim is one of the styles OnCard rebases and SectionRule is
// not, so on a commented row SectionRule would paint a Doc-coloured box inside
// a Card-coloured row.
func borderStyleOf(st *Styles, onCard bool) lipgloss.Style {
	if onCard {
		return st.OnCard().Dim
	}
	return st.Dim
}

// splitTableGrid cuts a rendered grid into ONE LINE GROUP PER ROW, in table
// order, and answers false if it cannot. A group is the row's own content lines
// -- one for a row that fitted, several for a row lipgloss wrapped -- plus the
// rule that CLOSES it, so that the box's top border opens the first row's group
// and its bottom border closes the last row's. Every line of the table lands in
// exactly one group and no line lands in two.
//
// A LINE IS A RULE WHEN EVERY RUNE OF IT IS A BORDER RUNE, and the runes come
// from the border the grid was actually drawn with (tableGridBorder). The
// obvious test -- a prefix on the left edge -- is the one to avoid, because it
// reads a glyph this function does not choose: which rune a rule starts with is
// decided by BorderLeft, a builder call eighty lines up. Turning that one
// builder call off costs the prefix every table and costs this classification
// none.
//
// The classification also depends on tableGridPadCols being at least 1 -- see
// that constant for why a content line always carries a space and a rule never
// does. closeGridRight is a SECOND reader of that same space and yields to this
// one at the budget where the two compete.
//
// ANSWERING FALSE IS THE RIGHT ANSWER when the classification loses the thread:
// the tail guard below turns a classifier regression into no grouping rather
// than a wrong one, which is the difference a caller can act on. The only
// budget that fails is 1, where the outer MaxWidth truncates every line to a
// single border rune; app/list.go's listMinWidth of 31 puts it out of reach.
func splitTableGrid(rendered string, want int) ([][]string, bool) {
	if want <= 0 || rendered == "" {
		return nil, false
	}
	runes := borderRunesOf(tableGridBorder())
	var groups [][]string
	var cur []string
	content := false
	for _, line := range strings.Split(rendered, "\n") {
		cur = append(cur, line)
		if !borderOnlyLine(line, runes) {
			content = true
			continue
		}
		// A rule with no content above it yet is the box's TOP border, which
		// opens the first row's group rather than closing a row of its own.
		// Written as "no content yet" and not as "the first line" so it
		// stays right if BorderTop is ever turned off.
		if !content {
			continue
		}
		groups = append(groups, cur)
		cur, content = nil, false
	}
	// Anything left in cur is a row the table opened and never closed, which
	// means the classification lost the thread; a group count that misses is
	// the same failure seen from the other end. Neither is recoverable from
	// here.
	if len(cur) > 0 || len(groups) != want {
		return nil, false
	}
	return groups, true
}

// borderRunesOf is every rune the border b can draw, thirteen parts flattened
// into a set. It takes the border rather than reading tableGridBorder itself so
// the two facts stay separable: this is "the runes of a box", and which box is
// splitTableGrid's business.
func borderRunesOf(b lipgloss.Border) map[rune]bool {
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

// borderOnlyLine answers whether line is drawn from border runes and nothing
// else. ansi.Strip first, because every glyph in a rendered grid arrives
// wrapped in its zone's SGR and a byte comparison would see the escapes rather
// than the box -- ansi.Strip and not stripStyling, because what arrives here is
// a RENDERED line rather than a projection.
//
// AN EMPTY LINE IS NOT A RULE. It cannot occur inside a table lipgloss renders,
// but "" satisfies "every rune is a border rune" vacuously, and a vacuous true
// here would cut a group at a line that draws nothing.
// TestSplitTableGridAnswersFalseRatherThanGuess is what reaches it, with a
// hand-built box that has a blank line in it.
func borderOnlyLine(line string, runes map[rune]bool) bool {
	plain := ansi.Strip(line)
	if plain == "" {
		return false
	}
	for _, r := range plain {
		if !runes[r] {
			return false
		}
	}
	return true
}

// cardIndentCols is the width of the indent renderThreadCardPainted prepends
// to every line. IT SITS OUTSIDE THE WIDTH BUDGET PASSED IN -- indent + content
// is what actually lands on screen -- so a caller composing a full-width row
// around a card must subtract cardIndentCols before computing that budget,
// matching renderDocPainted's own call below and ui.ThreadCardLines'.
const cardIndentCols = 2

// docCardIndent is the Card-background indent renderDocPainted puts between
// the gutter and a card, and unanchoredIndentCols the same for the gutterless
// unanchored section. Named because each is spent twice -- once as rendered
// cells, once as a subtraction from the card's width budget -- and a literal
// that agrees in one place and not the other is invisible on screen.
const docCardIndent = "  "

const unanchoredIndentCols = 2

// renderThreadCardPainted draws v as a comment card: one header line per
// comment — every comment, not only the first — with its body indented
// beneath, and a "(resolved)" row above the lot when the thread is resolved.
// Its 2-space indent carries the Card background and nothing else.
// Threads on one block are separated by a blank row, which
// the CALLER inserts: a card renders one thread and cannot know it has a
// neighbour.
//
// The two strings it draws are stored review facts, not document text: they
// are never parsed, projected or split, so they carry the control-byte
// substitution HERE rather than inheriting a paragraph's accidental cover. The
// filter runs before wrapPlain for the same reason the fence arm's does: a
// bare C0 byte is zero cells to ansi.StringWidth and one to lipgloss's wrap.
// domain.Comment.Body and domain.Attribution are not touched -- this is a
// renderer.
func renderThreadCardPainted(v ThreadView, width int, pseudonym string, st *Styles) []string {
	indent := st.Card.Render("  ")
	var out []string
	if v.Thread.Resolved {
		out = append(out, indent+st.CardResolved.Render(resolvedLabel))
	}
	headStyle, bodyStyle := st.CardHeader, st.CardBody
	if v.Thread.Resolved {
		headStyle, bodyStyle = st.CardHeaderResolved, st.CardBodyResolved
	}
	for i, c := range v.Thread.Comments {
		head := renderControls(commentMarker+" "+FormatAttribution(c.Attribution, pseudonym), headStyle)
		if i == 0 && v.Badge != "" {
			head += st.Card.Render(" ") + st.Badge.Render("["+v.Badge+"]")
		}
		out = append(out, indent+head)
		for _, l := range wrapPlain(visibleControls(c.Body, bodyStyle), width-2) {
			out = append(out, indent+bodyStyle.Render("  "+l))
		}
	}
	return out
}

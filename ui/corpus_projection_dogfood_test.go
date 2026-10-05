package ui

// This file is a MEASUREMENT rather than a gate: a whole real corpus swept
// through the same projection the review view uses, asking one question of
// every document somebody actually wrote --
//
//	every block-level construct in the source is represented in the review view.
//
// WHY IT IS GATED AND NOT IN `make check`. The corpus is ~/plans, one machine's
// directory, outside this repository and changing daily. A test that depended
// on it would fail on every other machine, so the ASSERTIONS live on committed
// fixtures and this sweep is what says those fixtures still resemble the
// documents they stand in for. It skips cleanly when the corpus is absent.
//
// WHY IT IS IN package ui: the walk stays unexported. unanchoredSpans and
// undeclaredKinds (ui_test.go) are the coverage property, and being in this
// package is what lets the sweep call the SAME definition the unit tests call,
// with no second implementation to drift.
//
// IT IS NOT PURELY A REPORT. Two things here FAIL: a block-level node kind
// nobody has declared in blockRoles, and source text that reached no Block.
// EVERY OTHER NUMBER IS COMPUTED AND NONE IS ASSERTED -- this sweep reads the
// corpus as it stands when it runs, so a figure that has drifted and a figure
// measured differently are indistinguishable from here.
//
// WHAT IT CANNOT SEE:
//
//   - IT NEVER RENDERS -- TestCorpusProjectionDogfood, that is. Everything it
//     reports is measured off []Block, so a Block that renderBlockPainted draws
//     as nothing at all is invisible to it. TestCorpusGridDogfood, the second
//     sweep in this file, is the half that DOES render; the rendered FRAME is
//     driven on the committed fixture instead (app's
//     TestEveryConstructReachesTheReviewFrame).
//   - IT COUNTS BLOCKS, NOT AST NODES. "Blocks at quote depth 2" is not
//     "blockquotes nested two deep" -- one quotation holds many blocks. The two
//     agree on the MAXIMUM depth and disagree on every total.
//   - A CONSTRUCT THE PARSER DROPS ENTIRELY IS INVISIBLE: coverage is read off
//     the parse.
//   - IT IS NOT A REGRESSION TEST AND CANNOT DATE A CHANGE. Nothing is compared
//     against a stored baseline. Re-run it; do not reason about the delta.
//
// Run them with:
//
//	DRAFTPLANE_CORPUS_DOGFOOD=1 go test ./ui -run 'TestCorpus.*Dogfood' -v
//
// Inert without the gate, so it never runs under `make check`.

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

const (
	envCorpusGate = "DRAFTPLANE_CORPUS_DOGFOOD"
	envCorpusDir  = "DRAFTPLANE_CORPUS_DIR"
)

func requireCorpusDogfood(t *testing.T) {
	t.Helper()
	if os.Getenv(envCorpusGate) == "" {
		t.Skip("set DRAFTPLANE_CORPUS_DOGFOOD=1 to sweep the whole plan corpus through ParseBlocks and measure what the review view makes of it. Intentionally excluded from `go test ./...` because the corpus is one machine's ~/plans, outside this repository. See this file's own doc comment for what it checks and what it cannot.")
	}
}

// corpusRoot is the directory of markdown to sweep: ~/plans, or whatever
// DRAFTPLANE_CORPUS_DIR names.
//
// THE TWO ABSENCES ARE DIFFERENT and answered differently. A missing DEFAULT
// is an ordinary machine that has no plan corpus, and the sweep must be
// runnable there -- so it skips, which is what makes this file portable. A
// missing OVERRIDE is a typo in something the runner explicitly asked for, and
// skipping on it would quietly report success for a sweep of nothing.
func corpusRoot(t *testing.T) string {
	t.Helper()
	if v := os.Getenv(envCorpusDir); v != "" {
		if _, err := os.Stat(v); err != nil {
			t.Fatalf("%s names %s, which is not readable: %v", envCorpusDir, v, err)
		}
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory to find a corpus under: %v", err)
	}
	root := filepath.Join(home, "plans")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("no plan corpus at %s, so there is nothing to sweep -- point %s at a directory of markdown to run this elsewhere", root, envCorpusDir)
	}
	return root
}

// corpusMarkdownFiles is every .md file under root, recursively and sorted, so
// two runs on the same corpus report in the same order. Recursive because the
// corpus keeps superseded plans in a subdirectory and those are documents a
// reviewer opens too.
func corpusMarkdownFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".md") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	slices.Sort(out)
	if len(out) == 0 {
		t.Skipf("no .md files under %s", root)
	}
	return out
}

// corpusKindNames names BlockKind for the log. BlockKind has no String method
// and giving it one is a product change this sweep does not get to make.
// NOTHING CHECKS THIS TABLE IS COMPLETE -- a kind appended to the iota later is
// a kind with no line here, and corpusKindName answers with the bare number and
// says what to do about it, which is a failure a reader can act on rather than
// one that silently borrows a neighbour's name.
var corpusKindNames = map[BlockKind]string{
	KindHeading:   "heading",
	KindParagraph: "paragraph",
	KindCode:      "code",
	KindListItem:  "list item",
	KindTableRow:  "table row",
	KindRule:      "rule",
}

func corpusKindName(k BlockKind) string {
	if name, ok := corpusKindNames[k]; ok {
		return name
	}
	return fmt.Sprintf("kind %d (no name in corpusKindNames -- add one)", int(k))
}

// corpusShape is a block's kind and BOTH its nesting depths together, which is
// the key the fixture is checked against. It is deliberately the JOINT and not
// two marginals: a list nested two deep INSIDE a quotation is a shape neither
// "list items reach quote depth 1" nor "list items reach list depth 2" can
// tell you the corpus contains, and it is exactly the interaction an arm that
// handles one nesting and forgets the other gets wrong.
type corpusShape struct {
	Kind       BlockKind
	QuoteDepth int
	ListDepth  int
}

// corpusCounts is one sweep's running totals. Everything is a count of BLOCKS
// unless the field says otherwise; see the file comment for why that is not
// the same as a count of markdown constructs.
type corpusCounts struct {
	files, bytes int
	blocks       int

	byKind map[BlockKind]int
	// The JOINT distribution, which everything else is summed back out of --
	// see corpusShape for why the marginals are not enough and why deriving
	// them from this is what keeps the two from disagreeing about one corpus.
	byShape map[corpusShape]int

	quotedHeadings int

	filesWithRule  int
	filesWithTable int
	filesWithQuote int

	tables     int
	tableCols  map[int]int
	widestCell []int // one entry per TABLE: its widest projected cell, in cells
	cellWidths []int // one entry per CELL, in cells

	unanchored   int
	unanchoredIn []string
	undeclared   map[string]int

	maxQuote, maxList     int
	maxQuoteAt, maxListAt string
}

func TestCorpusProjectionDogfood(t *testing.T) {
	requireCorpusDogfood(t)

	root := corpusRoot(t)
	files := corpusMarkdownFiles(t, root)
	t.Logf("corpus root: %s", root)

	c := corpusCounts{
		byKind:     map[BlockKind]int{},
		byShape:    map[corpusShape]int{},
		tableCols:  map[int]int{},
		undeclared: map[string]int{},
	}

	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		c.files++
		c.bytes += len(src)

		// nil styles deliberately: Display and DisplayCard need a theme and
		// nothing here reads them, while DisplayPlain -- which the
		// header-label rule and every width figure below are measured off --
		// is computed whether or not one was supplied (see plainProjection).
		blocks := ParseBlocks(src, nil)
		c.blocks += len(blocks)

		for _, kind := range undeclaredKinds(parseDocument(src)) {
			c.undeclared[kind]++
		}
		if spans := unanchoredSpans(src, blocks); len(spans) > 0 {
			c.unanchored += len(spans)
			c.unanchoredIn = append(c.unanchoredIn, fmt.Sprintf("%s: %d span(s), first %s %d-%d %q", rel, len(spans), spans[0].Kind, spans[0].Start, spans[0].Stop, spans[0].Text))
		}

		fileHasRule, fileHasTable, fileHasQuote := false, false, false
		// prevTable is the table of the last TABLE ROW seen in this file, and
		// NOT of the previous block: the loop skips every other kind before
		// reaching it, so a heading between two tables never resets it. What
		// carries the count is that Table.ID is document-scoped, assigned in
		// walk order and contiguous per table, so two DIFFERENT tables never
		// share one and the rows of one are never interrupted by another's.
		prevTable := 0
		for _, b := range blocks {
			c.byKind[b.Kind]++
			c.byShape[corpusShape{b.Kind, b.QuoteDepth, b.ListDepth}]++
			if b.QuoteDepth > 0 {
				fileHasQuote = true
			}
			if b.QuoteDepth > c.maxQuote {
				c.maxQuote, c.maxQuoteAt = b.QuoteDepth, rel
			}
			if b.ListDepth > c.maxList {
				c.maxList, c.maxListAt = b.ListDepth, rel
			}
			if b.Kind == KindHeading && b.QuoteDepth > 0 {
				c.quotedHeadings++
			}
			if b.Kind == KindRule {
				fileHasRule = true
			}
			if b.Kind != KindTableRow {
				continue
			}
			fileHasTable = true
			widest := 0
			for _, cell := range b.Cells {
				w := ansi.StringWidth(cell.DisplayPlain)
				c.cellWidths = append(c.cellWidths, w)
				if w > widest {
					widest = w
				}
			}
			// A NEW TABLE, or another row of the one above? READ OFF THE
			// BLOCK: Block.Table.ID is the table this row belongs to, and
			// TableRef exists because nothing else here can answer it. Deriving
			// it from source POSITION -- one line break between two rows
			// continues a table -- is WRONG rather than merely inferior: a
			// header is a Block of its own and the |---| delimiter between it
			// and the first body row is two line breaks, so the gap test splits
			// every table into a header and a body (230 tables against the
			// identity's 115).
			//
			// The COLUMN COUNT is read off the header row for the same reason it
			// is exact: every cell is kept, empty or not, and the header is the
			// row GFM pads the others to.
			if b.Table.ID != prevTable {
				prevTable = b.Table.ID
				c.tables++
				c.tableCols[len(b.Cells)]++
				c.widestCell = append(c.widestCell, widest)
			} else if widest > c.widestCell[len(c.widestCell)-1] {
				c.widestCell[len(c.widestCell)-1] = widest
			}
		}
		if fileHasRule {
			c.filesWithRule++
		}
		if fileHasTable {
			c.filesWithTable++
		}
		if fileHasQuote {
			c.filesWithQuote++
		}
	}

	corpusReport(t, &c)

	// THE TWO ASSERTIONS. Both are the fixtures' own, run over documents
	// nobody wrote for a test -- which is the whole value of a corpus sweep:
	// a fixture can only fail on what its author thought to put in it.
	if len(c.undeclared) > 0 {
		t.Errorf("the corpus contains block-level node kinds blockRoles declares neither container nor content: %v -- give each a line in blockRoles (ui/markdown.go)", c.undeclared)
	}
	if c.unanchored > 0 {
		t.Errorf("%d span(s) of corpus source reached no block:\n%s", c.unanchored, strings.Join(c.unanchoredIn, "\n"))
	}
}

// corpusPercentile is the NEAREST-RANK percentile of a sorted-ascending
// sample: the smallest value at or above which p of the sample lies. Named
// because there are several definitions and the interpolating ones would
// invent a cell width no document has.
func corpusPercentile(sorted []int, p float64) int {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

func corpusReport(t *testing.T, c *corpusCounts) {
	t.Helper()
	t.Logf("%d files, %d bytes, %d blocks -- every figure below is derived from those blocks (ParseBlocks with nil styles), except where it says otherwise", c.files, c.bytes, c.blocks)

	kinds := make([]BlockKind, 0, len(c.byKind))
	for k := range c.byKind {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool { return c.byKind[kinds[i]] > c.byKind[kinds[j]] })
	for _, k := range kinds {
		t.Logf("blocks by kind: %-10s %6d   quote depths %s   list depths %s", corpusKindName(k), c.byKind[k], corpusHistogram(c.quoteMarginal(k)), corpusHistogram(c.listMarginal(k)))
	}

	// THE JOINT, printed in full and not only where it is interesting,
	// because "interesting" is the judgement the fixture is supposed to be
	// checked against rather than filtered by. This is the line
	// TestFidelityFixtureReachesEveryCorpusDepth's census is compared to by
	// hand when the corpus moves; without it the sweep reports two marginals
	// and a shape present in neither is invisible.
	for _, s := range c.shapesInOrder() {
		t.Logf("joint: %-10s quote %d, list %d: %d", corpusKindName(s.Kind), s.QuoteDepth, s.ListDepth, c.byShape[s])
	}

	t.Logf("quotations: %d of %d files hold one; deepest is depth %d, in %s -- Block.QuoteDepth, which counts Blockquote ancestors, so this is BLOCKS inside a quotation and not quotations", c.filesWithQuote, c.files, c.maxQuote, c.maxQuoteAt)
	t.Logf("quoted headings: %d (Block.Kind == heading && Block.QuoteDepth > 0 -- the shape InferTitle and AnchorsAsSection both have to refuse)", c.quotedHeadings)
	t.Logf("lists: deepest nesting is ListDepth %d, in %s", c.maxList, c.maxListAt)
	// SUMMED OUT OF THE JOINT and not counted alongside it, for the reason the
	// marginals are: two counters of one population drift. It is here because
	// app/fidelity_test.go cites it by number for the bullet-on-a-quotation
	// pin.
	t.Logf("blocks both quoted AND inside a list: %d -- every joint shape with quote > 0 and list > 0, summed; this is the figure app/fidelity_test.go's bullet case quotes", c.quotedInList())
	t.Logf("thematic breaks: %d blocks of Kind rule, in %d of %d files", c.byKind[KindRule], c.filesWithRule, c.files)

	t.Logf("tables: %d rows of Kind table row in %d of %d files, forming %d tables (grouped on Block.Table.ID; every table's HEADER is one of those rows)", c.byKind[KindTableRow], c.filesWithTable, c.files, c.tables)
	for _, n := range corpusSortedKeys(c.tableCols) {
		t.Logf("tables with %d column(s): %d (columns = len(Block.Cells) of the table's first row, which is its header; an empty cell is a cell and is counted)", n, c.tableCols[n])
	}

	sort.Ints(c.cellWidths)
	sort.Ints(c.widestCell)
	t.Logf("table cell widths, in terminal CELLS (ansi.StringWidth of one TableCell.DisplayPlain): %d cells, median %d, p90 %d, max %d", len(c.cellWidths), corpusPercentile(c.cellWidths, 0.5), corpusPercentile(c.cellWidths, 0.9), corpusPercentile(c.cellWidths, 1))
	// THE PROJECTED CELL AND NOT THE SOURCE CELL, which is worth a sentence
	// because the two are close enough to be mistaken for each other and a
	// grid's column widths are computed from THIS one: the cell with its
	// inline markup already resolved, counted in terminal cells. A source
	// measurement counts characters and keeps a link's URL, which is never
	// drawn.
	t.Logf("widest cell per table, same units: median %d, p90 %d, max %d -- what one column of a grid has to fit before it wraps", corpusPercentile(c.widestCell, 0.5), corpusPercentile(c.widestCell, 0.9), corpusPercentile(c.widestCell, 1))

	t.Logf("undeclared block-level kinds: %d distinct; source text reaching no block: %d span(s)", len(c.undeclared), c.unanchored)
}

// quotedInList is blocks carrying BOTH nestings at once, summed out of the
// joint for the same reason the marginals are.
func (c *corpusCounts) quotedInList() int {
	out := 0
	for s, n := range c.byShape {
		if s.QuoteDepth > 0 && s.ListDepth > 0 {
			out += n
		}
	}
	return out
}

// quoteMarginal and listMarginal sum the joint back down to one dimension, so
// the per-kind summary lines and the joint lines cannot disagree about the
// same corpus.
func (c *corpusCounts) quoteMarginal(k BlockKind) map[int]int {
	out := map[int]int{}
	for s, n := range c.byShape {
		if s.Kind == k {
			out[s.QuoteDepth] += n
		}
	}
	return out
}

func (c *corpusCounts) listMarginal(k BlockKind) map[int]int {
	out := map[int]int{}
	for s, n := range c.byShape {
		if s.Kind == k {
			out[s.ListDepth] += n
		}
	}
	return out
}

// shapesInOrder is every shape seen, kind-major then depth-ascending, so two
// runs of the sweep can be diffed line by line.
func (c *corpusCounts) shapesInOrder() []corpusShape {
	out := make([]corpusShape, 0, len(c.byShape))
	for s := range c.byShape {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Kind != b.Kind:
			return a.Kind < b.Kind
		case a.QuoteDepth != b.QuoteDepth:
			return a.QuoteDepth < b.QuoteDepth
		default:
			return a.ListDepth < b.ListDepth
		}
	})
	return out
}

// corpusHistogram renders a depth histogram as "0:n 1:m", depth-ascending.
func corpusHistogram(m map[int]int) string {
	if len(m) == 0 {
		return "none"
	}
	var parts []string
	for _, d := range corpusSortedKeys(m) {
		parts = append(parts, fmt.Sprintf("%d:%d", d, m[d]))
	}
	return strings.Join(parts, " ")
}

func corpusSortedKeys(m map[int]int) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// corpusGridWidths is the terminal widths TestCorpusGridDogfood renders every
// corpus table at. 160 is the real terminal width every performance figure in
// this package was taken at; 120, 100, 80 and 76 are the widths the recorded
// mid-word-break, header-truncation and twin measurements used; 40 and 20 are
// below railMinWidth, where the rail band collapses; 4 and 2 are below anything
// a terminal reaches and are here for the clamp and the cliff.
//
// THESE ARE TERMINAL WIDTHS AND NOT BUDGETS, which is the distinction the two
// narrow ones exist to keep honest. A quoted table's budget is the width less
// quoteBarCols, so at a terminal width of 2 a table at quote depth 1 is handed
// a budget of ZERO -- clamped to 1, the single budget splitTableGrid cannot cut
// at. Measured here: at width 2, 112 of the 115 tables still cut and the 3 that
// do not are exactly a real corpus's 3 QUOTED tables. The split assertion below
// is written on the CLAMPED BUDGET for that reason.
var corpusGridWidths = []int{160, 120, 100, 80, 76, 40, 20, 4, 2}

// corpusGridCounts is one width's readings over the whole corpus.
type corpusGridCounts struct {
	tables      int
	split       int
	brokeTables int
	brokeCells  int
	control     int
	unreadable  int
	openRight   int
	openIn      []string
}

// TestCorpusGridDogfood is the RENDERED half of this file's sweep: every table
// in a real corpus drawn as the grid the review view draws, at eight widths,
// in both colour twins.
//
// WHY IT IS SEPARATE FROM TestCorpusProjectionDogfood. That sweep's own
// contract is that it never renders, and the figures a grid owes are figures
// only a render has. Splitting them keeps each sweep's "what it cannot see"
// list true of itself. They share the gate, the corpus walk and every
// measurement helper, so there is no second definition of anything.
//
// THE SAME RULE ABOUT LITERALS APPLIES: nothing here compares a corpus figure
// to a number. Those literals live on the COMMITTED fixture instead, in
// ui_test.go's TestFidelityGridFiguresAreWhatWasMeasured. This computes, logs
// its derivation, and asserts only PROPERTIES -- claims true of any corpus.
//
// WHAT IT ASSERTS, all four of them properties and none of them counts:
//
//   - EVERY TABLE'S FIRST ROW IS ITS HEADER AND NO OTHER ROW IS, and the number
//     of KindTableRow blocks is the number of body rows plus the number of
//     tables.
//   - NO RENDERED LINE EXCEEDS THE CLAMPED BUDGET, at any width, in either
//     twin.
//   - EVERY TABLE CUTS INTO EXACTLY ITS OWN ROW COUNT at every budget of 2 or
//     more, in both twins. Row-level anchoring is what a grid refused to give
//     back, and this is the whole of what keeps it alive.
//   - EVERY GRID LINE ENDS IN A RIGHT-HAND BORDER RUNE, at every clamped budget
//     above 2, in both twins. The box is shut.
//
// THAT LAST ONE WAS A COUNT FOR ONE COMMIT, and the reason is worth the space.
// lipgloss/v2/table truncates the whole table to the width it was given; in a
// band of budgets just below a table's natural width the resizer allocates one
// cell too many, and what that truncation cut was the RIGHT-HAND BORDER. Every
// line still fitted the budget and the split still succeeded, so the assertions
// above were silent about it, and so was every test in the repository. This
// sweep measured its incidence over 115 tables in a real corpus:
//
//	width  160 120 100  80  76  40  20
//	open     0   2   2   1   2   1   1
//
// ZERO AT 160 AND ONE AT 80 is why it went unnoticed for so long: the band sits
// immediately below a table's NATURAL width, so the two widths anybody actually
// uses were almost clean. closeGridRight (ui/painted.go) is the fix.
//
// WHAT IT ONLY LOGS, because each is a count and counts drift: the mid-word
// breaks and the hard-floor distribution.
func TestCorpusGridDogfood(t *testing.T) {
	requireCorpusDogfood(t)

	root := corpusRoot(t)
	files := corpusMarkdownFiles(t, root)
	st := darkStyles(t)
	t.Logf("corpus root: %s -- rendered through tableGridString with the default theme's Styles, both twins", root)

	type corpusTable struct {
		file string
		rows []Block
	}
	var tables []corpusTable
	rowBlocks, bodyRows, tailed := 0, 0, 0
	var floors []int

	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		blocks := ParseBlocks(src, st)
		for _, b := range blocks {
			if b.Kind == KindTableRow {
				rowBlocks++
			}
		}
		for _, rows := range tablesOf(blocks) {
			tables = append(tables, corpusTable{rel, rows})
			floors = append(floors, gridHardFloor(rows))
			tailed += gridTailedRows(rows)
			for j, b := range rows {
				if (j == 0) != b.Table.Header {
					t.Errorf("%s, table %d row %d: Table.Header = %v -- the header is row 0 and nothing else is", rel, b.Table.ID, j, b.Table.Header)
				}
				if j > 0 {
					bodyRows++
				}
			}
		}
	}
	if len(tables) == 0 {
		t.Skipf("no table in any of the %d files under %s, so there is nothing to render", len(files), root)
	}
	// THE +1 PER TABLE, over the corpus. Stated as an identity between three
	// counts rather than as any one of them, so it holds whatever the corpus
	// becomes.
	if rowBlocks != bodyRows+len(tables) {
		t.Errorf("%d table-row blocks against %d body rows and %d tables -- the header Block is no longer one per table", rowBlocks, bodyRows, len(tables))
	}
	t.Logf("%d tables in %d files: %d table-row blocks = %d body rows + %d headers, exactly one apiece", len(tables), len(files), rowBlocks, bodyRows, len(tables))

	// THE DISCARDED TAIL, counted by the same predicate the committed fixture's
	// shape case uses (gridTailedRows), so the fixture cannot come to stand in
	// for a population measured another way. It is a body row whose SOURCE LINE
	// holds more unescaped-pipe-separated fields than its header has columns --
	// not a cell that happens to contain a pipe, which over this corpus is 15
	// rows rather than 2.
	t.Logf("discarded tails: %d body rows of %d carry one -- the excess GFM threw away before the AST existed, appended raw to the last cell (ui/markdown.go, tableRowCells)", tailed, bodyRows)

	sort.Ints(floors)
	t.Logf("hard floor, in terminal CELLS (every column at least its longest whitespace-delimited token, plus this grid's padding and borders -- gridHardFloor): median %d, p90 %d, max %d, min %d. It is the ceiling on any custom column allocator: at or below it a table CAN be drawn break-free, above it no allocator can",
		corpusPercentile(floors, 0.5), corpusPercentile(floors, 0.9), corpusPercentile(floors, 1), floors[0])

	for _, width := range corpusGridWidths {
		c := corpusGridCounts{tables: len(tables)}
		for _, tbl := range tables {
			budget := width - quoteBarCols(tbl.rows[0].QuoteDepth)
			clamped := clampTableWidth(budget)
			for _, onCard := range []bool{false, true} {
				rendered := tableGridString(tbl.rows, budget, st, onCard)
				for i, line := range strings.Split(rendered, "\n") {
					if w := ansi.StringWidth(ansi.Strip(line)); w > clamped {
						t.Errorf("%s, table %d at width %d (budget %d, clamped %d), onCard=%v: line %d is %d cells", tbl.file, tbl.rows[0].Table.ID, width, budget, clamped, onCard, i, w)
					}
				}
				groups, ok := splitTableGrid(rendered, len(tbl.rows))
				// AT A CLAMPED BUDGET OF 1 NOT CUTTING IS THE CONTRACT, not a
				// failure: every line of the render is a single border rune
				// and there is nothing left to tell a rule from a row, so
				// splitTableGrid answers false rather than guess. See its own
				// comment, and corpusGridWidths for how a terminal width of 2
				// reaches that budget for a quoted table and not for any other.
				if !ok {
					if clamped > 1 {
						t.Errorf("%s, table %d at width %d (clamped budget %d), onCard=%v: the render did not cut into its %d rows", tbl.file, tbl.rows[0].Table.ID, width, clamped, onCard, len(tbl.rows))
					}
					continue
				}
				// THE BOX MUST BE SHUT ON THE RIGHT, and this is an ASSERTION
				// and not a count -- "every line of every grid ends in a
				// right-hand border rune" is true of any corpus at all, which
				// is the test this file applies to everything it asserts.
				//
				// IT IS ABOVE THE onCard SHORT-CIRCUIT AND THAT POSITION IS THE
				// ASSERTION. It sat BELOW it for one commit, which made this
				// test's own "in both twins" wider than the code -- and the twin
				// it could not see was the CARD twin, whose absent or unstyled
				// closer is the two-zone failure paintOneTable renders two twins
				// to prevent. A one-twin-only closer would have passed this
				// sweep in silence.
				//
				// A CLAMPED BUDGET OF 2 IS EXEMPT for the reason closeGridRight
				// records in full: closing a line there costs the only
				// non-border cell it has and makes a content row
				// indistinguishable from a separator, so the whole table would
				// cut into nothing. 2 is the only budget where that arises.
				if clamped > 2 && !gridClosesOnTheRight(rendered) {
					if !onCard {
						c.openRight++
						if len(c.openIn) < 8 {
							c.openIn = append(c.openIn, fmt.Sprintf("%s table %d", tbl.file, tbl.rows[0].Table.ID))
						}
					}
					t.Errorf("%s, table %d at width %d (clamped budget %d), onCard=%v: the box does not close on the right", tbl.file, tbl.rows[0].Table.ID, width, clamped, onCard)
				}
				if onCard {
					// The COUNTS below are the Doc twin's. Both twins are
					// split, and both are checked for their right border,
					// because those are the assertions; only one is counted,
					// because the twins differ in colour alone and counting
					// both would double every figure.
					continue
				}
				c.split++
				breaks, control, readable := gridMidWordBreaks(tbl.rows, groups)
				c.control += control
				if !readable {
					c.unreadable++
					continue
				}
				if breaks > 0 {
					c.brokeTables++
					c.brokeCells += breaks
				}
			}
		}
		// AT THE TWO NARROW WIDTHS EVERY READING BUT THE ASSERTIONS DEGENERATES,
		// and the line says so rather than leaving a reader to wonder why 112
		// tables are suddenly unreadable: a box a few runes wide has no columns
		// to part into and no room for a right border. Those widths are here
		// for the clamp and the cliff; the measurements are the wide ones'.
		t.Logf("width %3d: %d of %d tables cut cleanly in the Doc twin; %d break a word (%d cells); %d have a cell this cannot read back (its own text holds a box rune, or the box is too narrow to have columns); %d cells skipped for a control byte; %d tables draw a box with NO RIGHT BORDER (which is asserted to be 0 above a clamped budget of 2)%s",
			width, c.split, c.tables, c.brokeTables, c.brokeCells, c.unreadable, c.control, c.openRight, corpusGridExamples(c.openIn))
	}
}

// corpusGridExamples formats the first few offenders for a log line, or nothing
// at all when there are none.
func corpusGridExamples(in []string) string {
	if len(in) == 0 {
		return ""
	}
	return " -- the first of them: " + strings.Join(in, ", ")
}

package ui

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/x/ansi"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/placement"
	"github.com/draftplane/draftplane/reanchor"
	"github.com/draftplane/draftplane/theme"
	"github.com/yuin/goldmark/ast"
)

func loadDoc(t *testing.T) []byte {
	t.Helper()
	source, err := os.ReadFile("testdata/original.md")
	if err != nil {
		t.Fatal(err)
	}
	return source
}

// loadFidelity reads ui/testdata/fidelity.md: one document carrying every
// construct a real corpus nests inside another one, at every depth the
// corpus reaches, plus the SHAPES its tables come in -- seven columns, an
// empty header cell, a body row with more cells than the header, a cell of two
// thousand characters, and a token no column can hold. It is a FILE and not a
// const because app's TestEveryConstructReachesTheReviewFrame drives the
// identical bytes through a real 80x24 review view, and two copies of a fixture
// that both claim "every construct" is two places for the same claim to rot.
//
// WHAT IT IS FOR, against totalityDoc, which is the obvious thing to confuse
// it with: totalityDoc is built to make goldmark emit every NODE KIND it can
// and is flat, where this one is built to make the walk carry DEPTH. Both are
// cases of TestParseBlocksWalkIsTotal, because an undeclared kind and a depth
// the walk mishandles are not the same bug and neither document catches the
// other's.
//
// It deliberately carries NO HTML block and NO link reference definition;
// totalityDoc owns those, and a real corpus has neither (see blockRoles).
//
// THE CENSUS BELOW CARRIES THE FILE COUNT and is the only place it is
// stated, deliberately: a second provenance claim about one set of figures
// is no way to tell which is current. Re-run
// corpus_projection_dogfood_test.go rather than trusting it.
func loadFidelity(t *testing.T) []byte {
	t.Helper()
	source, err := os.ReadFile("testdata/fidelity.md")
	if err != nil {
		t.Fatal(err)
	}
	return source
}

// TestFidelityFixtureReachesEveryCorpusDepth is what makes the fixture a
// fixture rather than a document somebody once wrote: it pins the EXACT
// multiset of shapes ParseBlocks makes of it, so neither an edit to the file
// nor a change to how the walk assigns depth can pass unnoticed.
//
// A SHAPE IS THE JOINT -- kind, quote depth AND list depth together -- and
// that is the whole claim, not a tidier way of writing two. "List items reach
// quote depth 1" and "list items reach list depth 2" are both true of a corpus
// that has no list nested two deep INSIDE a quotation and of one that has 8,
// and the second is what an arm handling one nesting and forgetting the other
// gets wrong.
//
// EQUALITY AND NOT COVERAGE, in both directions. "At least one block at quote
// depth 3" would stay green if the walk started reporting every block at depth
// 3, and "every shape below occurs" would stay green if a construct silently
// doubled. The two failures the equality catches are a construct LEAVING the
// fixture and a depth being MISASSIGNED -- and the depth is the whole of what
// says a block is quoted, so a walk that lost it would draw a quotation as the
// document's own words.
//
// EVERY SHAPE THE CORPUS CONTAINS IS HERE, which is the claim that makes the
// fixture stand in for the corpus at all. Re-derived by a corpus sweep over
// 54 files, blocks by kind and both depths:
//
//	heading    q0 l0:1283  q1 l0:33
//	paragraph  q0 l0:3638  q1 l0:435   q2 l0:10   q3 l0:1
//	code       q0 l0:426   q1 l0:4
//	list item  q0 l1:4464  q0 l2:264   q1 l1:136  q1 l2:8   q2 l1:9
//	table row  q0 l0:926   q1 l0:15
//	rule       q0 l0:471
//
// Sixteen shapes; the census below carries all sixteen and one more,
// {KindListItem, 0, 3}, which is slack rather than a claim -- a real corpus
// stops at list depth 2.
//
// WHAT IT IS SILENT ABOUT: everything on screen. A block with the right kind
// and the right depths that renderBlockPainted draws as nothing at all passes
// here. app's TestEveryConstructReachesTheReviewFrame drives the same file
// through a real frame.
func TestFidelityFixtureReachesEveryCorpusDepth(t *testing.T) {
	source := loadFidelity(t)
	blocks := ParseBlocks(source, nil)

	// One line per shape, with the corpus count it stands in for. A shape
	// with no corpus column is one a real corpus does not contain and the
	// code must still handle -- said so explicitly rather than left to be
	// inferred.
	want := map[corpusShape]int{
		{KindHeading, 0, 0}: 5,
		{KindHeading, 1, 0}: 1, // 33 quoted headings in a real corpus

		// Eight unquoted paragraphs and not two: six of them are the
		// fixture's own labels, one per table shape plus the note that says
		// why they are there. A fixture that carries a shape and does not
		// say which corpus shape it stands in for is a fixture the next
		// reader deletes.
		{KindParagraph, 0, 0}: 8,
		{KindParagraph, 1, 0}: 2,
		{KindParagraph, 2, 0}: 1,
		{KindParagraph, 3, 0}: 1, // a real corpus has exactly one block this deep

		{KindCode, 0, 0}: 2, // fenced and indented
		{KindCode, 1, 0}: 1, // 4 quoted code blocks in a real corpus

		{KindListItem, 0, 1}: 3,
		{KindListItem, 0, 2}: 1,
		{KindListItem, 0, 3}: 1, // deeper than a real corpus, which stops at list depth 2
		{KindListItem, 1, 1}: 3, // 136 in a real corpus. Two are list items inside a quotation; the third is the other nesting, a quotation inside a LIST ITEM
		{KindListItem, 1, 2}: 1, // 8 in a real corpus: a list nested two deep INSIDE a quotation, the joint shape neither marginal names
		{KindListItem, 2, 1}: 1, // 9 in a real corpus

		// THE +1 PER TABLE IS THE POINT OF THESE TWO LINES. The fixture holds
		// NINE tables -- eight unquoted and one quoted -- and every one of
		// them yields a Block for its HEADER as well as for each of its body
		// rows (see KindTableRow). Twelve body rows and eight headers
		// unquoted; one body row and one header quoted.
		//
		// FIVE OF THE EIGHT UNQUOTED TABLES ARE HERE BECAUSE A REAL CORPUS
		// CONTAINS THAT SHAPE AND NOTHING ELSE COMMITTED DOES:
		//
		//   - SEVEN COLUMNS, a real corpus's widest table: 1 of 115.
		//   - AN EMPTY HEADER CELL, a column with a heading nobody wrote: 33
		//     of a real corpus's 325 header cells. It is the cell tableGridRow's
		//     header arm draws and the one a join of the cells would lose the
		//     position of.
		//   - A BODY ROW WITH MORE CELLS THAN THE HEADER -- the discarded
		//     tail, 2 of a real corpus's 826 body rows. The parser throws the
		//     excess away before the AST exists and tableRowCells appends it
		//     to the last cell RAW, pipes and all; app's
		//     requireNoMarkupOnScreen carries that exception by name because
		//     of this row.
		//   - A CELL OF 2,039 CHARACTERS. Two of a real corpus's 115 tables
		//     hold a cell of 2,000 or more and its widest is 2,590: a
		//     paragraph boxed into one column, tens of screen rows tall.
		//   - A TOKEN NO COLUMN CAN HOLD. Nothing else makes a grid break a
		//     WORD rather than a line. Without this table the fixture breaks 0
		//     cells at all three widths, which is a figure that pins nothing --
		//     see TestFidelityGridFiguresAreWhatWasMeasured.
		{KindTableRow, 0, 0}: 20, // 926 unquoted rows in a real corpus
		{KindTableRow, 1, 0}: 2,  // 15 quoted rows in a real corpus, across its 3 quoted tables

		{KindRule, 0, 0}: 2, // 471 in a real corpus, every one of them at quote depth 0 and list depth 0
	}

	got := map[corpusShape]int{}
	for _, b := range blocks {
		got[corpusShape{b.Kind, b.QuoteDepth, b.ListDepth}]++
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the fixture no longer projects the shapes it stands in for:\n got  %v\n want %v", got, want)
	}

	total := 0
	for _, n := range want {
		total += n
	}
	if len(blocks) != total {
		t.Fatalf("blocks = %d, want %d -- the census and the block list disagree, so one of them is not counting what it thinks", len(blocks), total)
	}
}

func TestParseBlocksSourceMapping(t *testing.T) {
	source := loadDoc(t)
	blocks := ParseBlocks(source, nil)
	if len(blocks) < 60 {
		t.Fatalf("expected rich block list, got %d", len(blocks))
	}
	for i, b := range blocks {
		if b.SrcStart >= b.SrcEnd || b.SrcEnd > len(source) {
			t.Fatalf("block %d invalid range", i)
		}
		if b.Kind != KindHeading && b.Text != string(source[b.SrcStart:b.SrcEnd]) {
			t.Fatalf("block %d text is not its source slice", i)
		}
	}
}

// totalityDoc is built to make goldmark produce as many distinct block-level
// node kinds as it can, not to look like a plan: ALL TWELVE that ast/block.go
// declares -- Document, Heading (ATX and setext), Paragraph, TextBlock,
// CodeBlock, FencedCodeBlock, Blockquote (to depth 3), List, ListItem,
// ThematicBreak, HTMLBlock and LinkReferenceDefinition -- plus the four
// extension.Table brings. That is what makes the test below an assertion about
// this parser rather than about this document, and it only works because the
// test parses through parseDocument too.
//
// EVERY CELL OF THE FIRST TABLE IS A DIFFERENT WORD, which is a fixture
// property the assertions below depend on rather than decoration: a check that
// every cell reaches the screen cannot tell "both drawn" from "one drawn
// twice" when the two are the same string.
//
// THE SECOND TABLE HAS NO BODY ROWS -- a Table whose only child is a
// TableHeader, which is what goldmark builds from a header line and a
// delimiter line alone, and what every table in a document being edited is for
// a keystroke or two. It once rendered as nothing at all; what it guards now is
// that the ORDINARY path still covers it.
//
// The code fence is ~~~ and not ``` only so this can stay a raw string;
// goldmark makes the same FencedCodeBlock of either.
const totalityDoc = `# A heading

A paragraph with **bold**, *italic* and a [link](https://example.com).

Setext heading
==============

> A quotation.
>
> > Nested one level.
> >
> > > And two.

---

***

- a bullet
- another
  - nested
    - deeper

1. ordered
2. and again

~~~go
fenced()
~~~

    indented code

<div class="note">
raw html
</div>

[ref]: https://example.com
[titled]: https://example.com
  "with a title"

Using [ref] and [titled].

| first heading | second heading |
|---------------|----------------|
| one           | two            |
| three         | four           |

| a header with no rows | and a second column |
|-----------------------|---------------------|
`

// undeclaredKinds names every block-level node under root whose kind
// blockRoles does not declare. Both the test that asserts the walk is total
// and the test that proves that assertion CAN fail run THIS function, so the
// guard having teeth is measured rather than assumed.
//
// It filters inline nodes on Type and not on Kind, the way ParseBlocks' own
// walk does. Filtering FOR ast.TypeBlock instead would be the easy mistake:
// the Document root is TypeDocument, so that spelling would walk nothing.
//
// It reports one entry per OCCURRENCE and not per kind, which is what makes a
// failure message say how much of the document is affected. A caller that
// wants the SET of kinds dedupes, which is what the corpus sweep does.
func undeclaredKinds(root ast.Node) []string {
	var out []string
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && n.Type() != ast.TypeInline && blockRoleOf(n) == roleUndeclared {
			out = append(out, n.Kind().String())
		}
		return ast.WalkContinue, nil
	})
	return out
}

// unanchoredSpan is one span of source that goldmark attributed to a
// block-level node and that no Block covers: text the parser found and the
// review view has nowhere to put. Kind is the node it belongs to, which is
// what names the construct that went missing.
type unanchoredSpan struct {
	Kind        string
	Start, Stop int
	Text        string
}

// unanchoredSegments names every span of source that goldmark attributed to a
// block-level node and that no Block covers. It is the totality claim stated
// over TEXT rather than over node kinds, which is what lets it catch a
// declaration that is PRESENT AND WRONG, where undeclaredKinds is silent by
// construction. ui/testdata/original.md HAS NO TABLE IN IT, so it is silent
// about every table mutation -- part of why ui/testdata/fidelity.md carries
// tables at two quote depths.
//
// IT IS THE FORMATTER AND unanchoredSpans IS THE WALK, split so the corpus
// sweep can count spans while a failing unit test gets three examples and a
// total. There is ONE walk, which is why the sweep is a test in THIS package
// rather than a harness that would need the walk exported to reach it.
//
// WHAT IT DOES NOT CATCH, said here so the next reader does not assume it does:
//
//   - Declaring a real CONTAINER content is inert -- such a node has no
//     segments, so it reaches the same `!ok` branch and descends anyway.
//     TestParseBlocksUndeclaredContainerKeepsItsSubtree pins that instead.
//   - Byte coverage cannot tell ONE Block per table row from one per CELL:
//     both cover the same bytes, and the second renders as a column of
//     fragments. TestTableIsRowsAndNothingElse pins that instead.
//   - A THEMATIC BREAK GOING MISSING IS INVISIBLE TO IT, because the coverage
//     read here is over SEGMENTS and goldmark appends a ThematicBreak none.
//   - IT IS A CLAIM ABOUT BLOCKS AND NOT ABOUT THE SCREEN. A Block whose range
//     covers its source and which renderBlockPainted then draws as nothing at
//     all passes here untouched; app's
//     TestEveryConstructReachesTheReviewFrame reads the rendered frame.
//
// IT READS THE EXPECTED COVERAGE OFF THE AST, not off the raw source lines,
// because the parser is the authority on which bytes are text: a fence's
// delimiter lines, a setext heading's underline, a table's |---| rule and a
// thematic break appear in no node's segments at all, so a raw-line sweep would
// need a list of exceptions that grows with every construct.
//
// Whitespace-only segments are skipped: a blank line inside a fence is a
// segment with no text to lose.
func unanchoredSegments(src []byte, blocks []Block) []string {
	spans := unanchoredSpans(src, blocks)
	var out []string
	for i, s := range spans {
		if i == unanchoredReportCap {
			// Capped: one misdeclared kind loses every span of that kind in
			// the document, and a failure that prints the fixture back is
			// one nobody reads. The count is what says how bad it is; three
			// spans are enough to say what broke.
			out = append(out, "...", fmt.Sprintf("(%d spans in all)", len(spans)))
			break
		}
		out = append(out, fmt.Sprintf("%s %d-%d %q", s.Kind, s.Start, s.Stop, s.Text))
	}
	return out
}

const unanchoredReportCap = 3

// unanchoredSpans is the walk unanchoredSegments reports -- see it for the
// property, the measured arms and the limits. UNCAPPED: the cap is a
// reporting decision and belongs at the reporter.
func unanchoredSpans(src []byte, blocks []Block) []unanchoredSpan {
	covered := func(start, stop int) bool {
		for _, b := range blocks {
			if b.SrcStart <= start && stop <= b.SrcEnd {
				return true
			}
		}
		return false
	}
	var out []unanchoredSpan
	_ = ast.Walk(parseDocument(src), func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if n.Type() == ast.TypeInline {
			// Lines() panics on an inline node, so this must skip rather
			// than continue -- the same reason ParseBlocks' walk does.
			return ast.WalkSkipChildren, nil
		}
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			text := string(src[seg.Start:seg.Stop])
			if strings.TrimSpace(text) != "" && !covered(seg.Start, seg.Stop) {
				out = append(out, unanchoredSpan{Kind: n.Kind().String(), Start: seg.Start, Stop: seg.Stop, Text: text})
			}
		}
		return ast.WalkContinue, nil
	})
	return out
}

// TestParseBlocksWalkIsTotal pins the totality claim: not that any
// particular construct renders, but that no text can go missing from a
// document without somebody having said so. It asserts the two things that
// can go wrong independently -- a kind nobody declared, and a kind declared
// wrongly -- because the first check is silent about the second.
func TestParseBlocksWalkIsTotal(t *testing.T) {
	cases := []struct {
		name string
		src  []byte
	}{
		{"every construct", []byte(totalityDoc)},
		{"the review fixture", loadDoc(t)},
		// The depth fixture, which the other two cannot stand in for: both are
		// flat, so a walk that dropped a construct only when it was nested
		// would pass them and lose a quoted table from every document in the
		// corpus that has one.
		{"every depth", loadFidelity(t)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := undeclaredKinds(parseDocument(c.src)); len(got) > 0 {
				t.Fatalf("block-level kinds declared neither container nor content: %v -- give each a line in blockRoles (ui/markdown.go)", got)
			}
			if got := unanchoredSegments(c.src, ParseBlocks(c.src, nil)); len(got) > 0 {
				t.Fatalf("source text that reached no block: %v", got)
			}
		})
	}
}

// TestParseBlocksUndeclaredContainerKeepsItsSubtree pins the runtime half of
// blockRoleOf's promise: a container nobody declared keeps its whole subtree,
// because the walk descends when a node has no line segments of its own.
// Getting this wrong is not one missing block -- it is every word under that
// node, which is how enabling extension.Footnote or extension.DefinitionList
// without a declaration would empty a region of a document the reader can see
// in their editor.
//
// IT REMOVES A DECLARATION RATHER THAN PARSING SOMETHING EXOTIC, and that is
// the only way in: every kind the default parser produces is declared, so an
// undeclared container cannot be reached through any document. Safe because
// nothing in this package parses in parallel, and restored by t.Cleanup on
// every exit path.
//
// The assertion is EQUALITY with the declared parse, not merely "some blocks
// came out": what the walk owes an undeclared container is exactly what it
// owes a declared one. The Blocks it compares carry QuoteDepth, so this is
// also the measurement that the declaration buys no depth -- quoteDepth reads
// the AST parent chain, which is there whatever blockRoles says.
func TestParseBlocksUndeclaredContainerKeepsItsSubtree(t *testing.T) {
	const quoted = "> A quotation.\n>\n> > Nested one level.\n>\n> > > And two.\n"
	declared := ParseBlocks([]byte(quoted), nil)
	if len(declared) != 3 {
		t.Fatalf("fixture no longer yields the three quoted blocks this test is about: %d", len(declared))
	}

	delete(blockRoles, ast.KindBlockquote)
	t.Cleanup(func() { blockRoles[ast.KindBlockquote] = roleContainer })

	if got := blockRoleOf(&ast.Blockquote{}); got != roleUndeclared {
		t.Fatalf("blockRoleOf after delete = %v, want roleUndeclared", got)
	}
	undeclared := ParseBlocks([]byte(quoted), nil)
	if !reflect.DeepEqual(undeclared, declared) {
		t.Fatalf("undeclared container changed the blocks under it:\n undeclared %+v\n declared   %+v", undeclared, declared)
	}
}

// undeclaredNode is a block-level node of a kind nobody has declared -- the
// shape a goldmark upgrade or a newly enabled extension arrives in. Built by
// hand rather than by mutating blockRoles so that the declaration table under
// test stays the real one.
type undeclaredNode struct{ ast.BaseBlock }

var kindUndeclaredNode = ast.NewNodeKind("DraftplaneUndeclaredForTest")

func (*undeclaredNode) Kind() ast.NodeKind { return kindUndeclaredNode }

func (n *undeclaredNode) Dump(source []byte, level int) { ast.DumpHelper(n, source, level, nil, nil) }

// TestParseBlocksWalkTotalityCatchesANewKind is the other half of
// TestParseBlocksWalkIsTotal, because an assertion that has never failed is
// not known to be total: hand the same check a node kind nobody declared and
// it must name it.
func TestParseBlocksWalkTotalityCatchesANewKind(t *testing.T) {
	doc := parseDocument([]byte(totalityDoc))
	doc.AppendChild(doc, &undeclaredNode{})
	got := undeclaredKinds(doc)
	if len(got) != 1 || got[0] != kindUndeclaredNode.String() {
		t.Fatalf("undeclaredKinds = %v, want exactly [%s]", got, kindUndeclaredNode)
	}
}

// TestParseBlocksFallbackKeepsUnhandledContentOnScreen pins the content half:
// a kind with no arm of its own is carried by its source text rather than
// dropped. An HTML block is the live example -- declared content, no arm, and
// goldmark gives it line segments -- so this runs the fallback for real
// instead of standing in for it.
func TestParseBlocksFallbackKeepsUnhandledContentOnScreen(t *testing.T) {
	const html = "<div class=\"note\">\nraw html\n</div>"
	source := []byte("Before.\n\n" + html + "\n\nAfter.\n")
	st := darkStyles(t)
	blocks := ParseBlocks(source, st)
	found := 0
	for _, b := range blocks {
		if strings.Contains(b.Text, "raw html") {
			found++
			if !strings.Contains(b.Text, "<div") || !strings.Contains(b.Text, "</div>") {
				t.Fatalf("fallback block carries a fragment of its construct, not all of it: %q", b.Text)
			}
		}
	}
	if found != 1 {
		t.Fatalf("the html block yielded %d blocks, want 1", found)
	}
	painted := ansi.Strip(strings.Join(collectText(RenderDoc(blocks, nil, nil, nil, OnLine(0), 60, "cm", st)), "\n"))
	if !strings.Contains(painted, "raw html") {
		t.Fatal("fallback block is parsed but not painted")
	}
}

// TestTableIsRowsAndNothingElse pins the whole of the table ruling in one
// place, because the pieces only mean anything together: a table of a header
// and n body rows is exactly n+1 Blocks (so a reviewer comments on a ROW, and
// neither on a cell nor on the table), the HEADER IS ONE OF THEM, and every
// row carries its own cells and nothing else.
//
// IT COMPARES THE WHOLE BLOCK LIST, not a probe into it, because most of the
// ways this can break are things that APPEAR rather than things that change:
// a Block per cell instead of per row, the table emitting once around the lot,
// the header emitting twice. A test that looked up "the row containing Deploy"
// would pass through every one of them -- and so would unanchoredSegments,
// which sees the same bytes covered either way.
//
// Text is asserted as the row's WHOLE SOURCE LINE on each case, which is the
// anchor side of the source mapping -- see blockRange (ui/markdown.go) for
// why it is the line and not the span the cells cover.
//
// IT PARSES WITHOUT STYLES, so what it reads is DisplayPlain and the cells'
// DisplayPlain. The styled walk is TestTableRowCellProjectionsAgree's, which
// is where the two can differ.
func TestTableIsRowsAndNothingElse(t *testing.T) {
	// cells is every cell of the row, empties included, and plain is the
	// projection those cells are JOINED into. The two differ exactly where a
	// cell is empty: Cells keeps it because a grid needs the column, joinCells
	// drops it because an empty line draws a blank row.
	type row struct {
		text   string
		cells  []string
		plain  string
		header bool
	}
	cases := []struct {
		name string
		src  string
		want []row
		// around is a fixture with a paragraph either side of the table, which
		// the assertion below strips off before comparing; quote is the
		// QuoteDepth every row of the case must carry.
		around bool
		quote  int
	}{
		{
			name: "a row per row, the header among them",
			src:  "| Step | Owner |\n|---|---|\n| Deploy | dana |\n| Roll back | sam |\n",
			want: []row{
				{text: "| Step | Owner |", cells: []string{"Step", "Owner"}, plain: "Step\nOwner", header: true},
				{text: "| Deploy | dana |", cells: []string{"Deploy", "dana"}, plain: "Deploy\ndana"},
				{text: "| Roll back | sam |", cells: []string{"Roll back", "sam"}, plain: "Roll back\nsam"},
			},
		},
		{
			// The parser pads a short row with empty cells and the row KEEPS
			// them: under a grid "Notes" is a column, and a row that dropped
			// its empty cell would put the next cell under the wrong heading.
			// They are dropped from the JOIN, where a cell with nothing in it
			// is a blank line and draws a blank row.
			name: "fewer cells than the header",
			src:  "| Step | Owner | Notes |\n|---|---|---|\n| Deploy |\n",
			want: []row{
				{text: "| Step | Owner | Notes |", cells: []string{"Step", "Owner", "Notes"}, plain: "Step\nOwner\nNotes", header: true},
				{text: "| Deploy |", cells: []string{"Deploy", "", ""}, plain: "Deploy"},
			},
		},
		{
			// GFM DISCARDS the excess, so those bytes are in no cell and no
			// inline projection. They are still in the row's source line and
			// still on screen, appended raw to the last cell -- see
			// blockRange and tableRowCells. Without both halves the ", and
			// four" here is a byte range a reviewer can see in their editor
			// and cannot see in the tool reviewing it.
			name: "more cells than the header",
			src:  "| Step | Owner |\n|---|---|\n| Deploy | dana | sam, and four |\n",
			want: []row{
				{text: "| Step | Owner |", cells: []string{"Step", "Owner"}, plain: "Step\nOwner", header: true},
				{text: "| Deploy | dana | sam, and four |", cells: []string{"Deploy", "dana | sam, and four"}, plain: "Deploy\ndana | sam, and four"},
			},
		},
		{
			// Inline markup projects on both rows alike: a cell carries the
			// RENDERED inlines (see TableCell), so a bolded header cell
			// puts no asterisks on screen and neither does a linked value.
			name: "inline markup in the header and the cells",
			src:  "| **Step** | `cmd` |\n|---|---|\n| *deploy* | [run](https://x.io) |\n",
			want: []row{
				{text: "| **Step** | `cmd` |", cells: []string{"Step", "cmd"}, plain: "Step\ncmd", header: true},
				{text: "| *deploy* | [run](https://x.io) |", cells: []string{"deploy", "run"}, plain: "deploy\nrun"},
			},
		},
		{
			// An empty cell is a cell in both rows -- the header's second
			// column has no words in it and the body's first has none --
			// which is the column a record-per-row projection had no way to
			// draw and a grid has to.
			name: "an empty cell in the header and in the row",
			src:  "| Step |  |\n|---|---|\n|  | dana |\n",
			want: []row{
				{text: "| Step |  |", cells: []string{"Step", ""}, plain: "Step", header: true},
				{text: "|  | dana |", cells: []string{"", "dana"}, plain: "dana"},
			},
		},
		{
			// A ROW WITH NOTHING IN IT AT ALL, which is a spacer somebody
			// typed and is the boundary the two projections meet at: its
			// cells are two empty cells, and the string they join to is ""
			// -- the same "" a block with NO projection has. What keeps the
			// two apart is Block.Cells; see
			// TestAnAllEmptyTableRowIsEmptyAndNotItsSource, which drives the
			// screen and the search index rather than the model.
			name: "a row with nothing in any cell",
			src:  "| Step | Owner |\n|---|---|\n|  |  |\n",
			want: []row{
				{text: "| Step | Owner |", cells: []string{"Step", "Owner"}, plain: "Step\nOwner", header: true},
				{text: "|  |  |", cells: []string{"", ""}, plain: ""},
			},
		},
		{
			// A header line and a delimiter line and nothing else: what
			// goldmark builds before it has looked for a single body row, and
			// what every table in a document being edited is for a keystroke
			// or two. It yielded ZERO blocks when the header was a container
			// -- the table's only words gone from a view whose whole point is
			// that they cannot be. It is no longer a special case at all: the
			// header emits here for the same reason it emits everywhere.
			name: "a header with no body rows is the table's only row",
			src:  "Before.\n\n| Step | Owner |\n|---|---|\n\nAfter.\n",
			want: []row{
				{text: "| Step | Owner |", cells: []string{"Step", "Owner"}, plain: "Step\nOwner", header: true},
			},
			// Before./After. bracket it so a regression cannot pass by
			// dropping the whole document rather than just the table.
			around: true,
		},
		{
			name:  "a header with no body rows, quoted",
			src:   "> | Step | Owner |\n> |---|---|\n",
			want:  []row{{text: "| Step | Owner |", cells: []string{"Step", "Owner"}, plain: "Step\nOwner", header: true}},
			quote: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := []byte(c.src)
			blocks := ParseBlocks(src, nil)
			if c.around {
				if len(blocks) != len(c.want)+2 || blocks[0].Text != "Before." || blocks[len(blocks)-1].Text != "After." {
					t.Fatalf("the paragraphs bracketing the table did not survive: %+v", blocks)
				}
				blocks = blocks[1 : len(blocks)-1]
			}
			if len(blocks) != len(c.want) {
				t.Fatalf("blocks = %d, want %d (one per row, header included): %+v", len(blocks), len(c.want), blocks)
			}
			for i, b := range blocks {
				if b.Kind != KindTableRow {
					t.Fatalf("block %d Kind = %d, want KindTableRow (%d)", i, b.Kind, KindTableRow)
				}
				if b.QuoteDepth != c.quote {
					t.Fatalf("block %d QuoteDepth = %d, want %d", i, b.QuoteDepth, c.quote)
				}
				if b.Text != c.want[i].text {
					t.Fatalf("block %d Text = %q, want %q", i, b.Text, c.want[i].text)
				}
				if b.Text != string(src[b.SrcStart:b.SrcEnd]) {
					t.Fatalf("block %d Text is not its source slice: %q", i, b.Text)
				}
				if b.DisplayPlain != c.want[i].plain {
					t.Fatalf("block %d DisplayPlain = %q, want %q", i, b.DisplayPlain, c.want[i].plain)
				}
				var cells []string
				for _, cell := range b.Cells {
					cells = append(cells, cell.DisplayPlain)
				}
				if !reflect.DeepEqual(cells, c.want[i].cells) {
					t.Fatalf("block %d cells = %q, want %q", i, cells, c.want[i].cells)
				}
				// Every case is ONE table, so the identity is fully
				// determined: table 1, and the header is whichever row the
				// case says it is.
				if want := (TableRef{ID: 1, Header: c.want[i].header}); b.Table != want {
					t.Fatalf("block %d Table = %+v, want %+v", i, b.Table, want)
				}
			}
		})
	}
}

// TestAdjacentTablesAreToldApartByTheirBlocks is why Block carries a table
// identity at all: a renderer drawing one grid per table has to know where one
// table ends and the next begins, and the block list will not tell it.
//
// "A CONTIGUOUS RUN OF KindTableRow IS ONE TABLE" IS FALSE, and the falsifying
// document is the simplest one anybody would write -- two tables with a blank
// line between them, which is the first case below.
//
// NOR IS THE SOURCE GAP, in either direction. The heuristic in reach is
// "exactly one line break between two rows continues a table". It SPLITS what
// it should not: the gap between a header and its own first body row is
// "\n|---|---|\n", two line breaks, because the delimiter line belongs to no
// node -- so the rule cuts every table in a real corpus into a header and a
// body, 230 tables against the identity's 115. And it JOINS what it should
// not: across
// the two tables of the list-item case below the gap is "\n- ", one line break.
//
// THE HEADER FLAG IS THE OTHER HALF and is not separable from the first: under
// a grid the header row is styled differently from the rows beneath it, and
// tableBlock builds both out of one kind with one set of fields.
func TestAdjacentTablesAreToldApartByTheirBlocks(t *testing.T) {
	type row struct {
		text  string
		table TableRef
		quote int
	}
	cases := []struct {
		name string
		src  string
		want []row
	}{
		{
			// Four blocks, no other kind anywhere between them, and the only
			// thing that says the third starts a new table is its ID.
			name: "two tables separated by a blank line",
			src:  "| a | b |\n|---|---|\n| 1 | 2 |\n\n| c | d |\n|---|---|\n| 3 | 4 |\n",
			want: []row{
				{text: "| a | b |", table: TableRef{ID: 1, Header: true}},
				{text: "| 1 | 2 |", table: TableRef{ID: 1}},
				{text: "| c | d |", table: TableRef{ID: 2, Header: true}},
				{text: "| 3 | 4 |", table: TableRef{ID: 2}},
			},
		},
		{
			// The same, one quoted and one not: two tables, and the quote
			// depth is the only OTHER thing that differs -- which is a fact
			// about quotation, not about tables, and a renderer grouping on
			// it would join two unquoted tables just the same.
			name: "a quoted table beside an unquoted one",
			src:  "> | a | b |\n> |---|---|\n> | 1 | 2 |\n\n| c | d |\n|---|---|\n| 3 | 4 |\n",
			want: []row{
				{text: "| a | b |", table: TableRef{ID: 1, Header: true}, quote: 1},
				{text: "| 1 | 2 |", table: TableRef{ID: 1}, quote: 1},
				{text: "| c | d |", table: TableRef{ID: 2, Header: true}},
				{text: "| 3 | 4 |", table: TableRef{ID: 2}},
			},
		},
		{
			// TWO TABLES IN ADJACENT LIST ITEMS, which is the shape that
			// falsifies the source-gap heuristic in the OTHER direction: the
			// gap across the two tables is "\n- ", one line break, so a rule
			// reading "one line break continues a table" joins them. ListDepth
			// is deliberately not set on a table row (see tableBlock), so
			// nothing else here says these are two lists either.
			name: "two tables in adjacent list items",
			src:  "- | a | b |\n  |---|---|\n  | 1 | 2 |\n- | c | d |\n  |---|---|\n  | 3 | 4 |\n",
			want: []row{
				{text: "| a | b |", table: TableRef{ID: 1, Header: true}},
				{text: "| 1 | 2 |", table: TableRef{ID: 1}},
				{text: "| c | d |", table: TableRef{ID: 2, Header: true}},
				{text: "| 3 | 4 |", table: TableRef{ID: 2}},
			},
		},
		{
			// The one shape of the five that DOES put another block between
			// the two tables, and it is here to keep the claim above honest:
			// adjacency fails on three of five, not on all of them. The rule
			// carries TableRef's zero value, which is what says a block is
			// not a table row without asking its kind.
			name: "a thematic break between two tables",
			src:  "| a | b |\n|---|---|\n| 1 | 2 |\n\n---\n\n| c | d |\n|---|---|\n| 3 | 4 |\n",
			want: []row{
				{text: "| a | b |", table: TableRef{ID: 1, Header: true}},
				{text: "| 1 | 2 |", table: TableRef{ID: 1}},
				{text: "---"},
				{text: "| c | d |", table: TableRef{ID: 2, Header: true}},
				{text: "| 3 | 4 |", table: TableRef{ID: 2}},
			},
		},
		{
			// THE FIXTURE THIS SUITE OWED ITSELF, and it is latent rather
			// than hypothetical: with NO blank line between them goldmark
			// parses the two tables as ONE, so the second header line and its
			// DELIMITER become body rows of the first. A grid will draw
			// "---|---" as content, because by the time any of this code sees
			// it that is exactly what it is. Recorded here rather than
			// repaired: what the parser calls a table is the parser's
			// decision, and the row IS in the document.
			name: "two tables with no blank line are one table",
			src:  "| a | b |\n|---|---|\n| 1 | 2 |\n| c | d |\n|---|---|\n| 3 | 4 |\n",
			want: []row{
				{text: "| a | b |", table: TableRef{ID: 1, Header: true}},
				{text: "| 1 | 2 |", table: TableRef{ID: 1}},
				{text: "| c | d |", table: TableRef{ID: 1}},
				{text: "|---|---|", table: TableRef{ID: 1}},
				{text: "| 3 | 4 |", table: TableRef{ID: 1}},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			blocks := ParseBlocks([]byte(c.src), nil)
			if len(blocks) != len(c.want) {
				t.Fatalf("blocks = %d, want %d: %+v", len(blocks), len(c.want), blocks)
			}
			for i, b := range blocks {
				// A table identity and the kind agree in both directions:
				// every table row has one, and nothing else does.
				if isRow := b.Kind == KindTableRow; isRow != (c.want[i].table.ID != 0) {
					t.Fatalf("block %d Kind = %d with Table %+v -- a table row and a table identity must go together", i, b.Kind, b.Table)
				}
				if b.Text != c.want[i].text || b.Table != c.want[i].table || b.QuoteDepth != c.want[i].quote {
					t.Fatalf("block %d = %q %+v quote %d, want %q %+v quote %d", i, b.Text, b.Table, b.QuoteDepth, c.want[i].text, c.want[i].table, c.want[i].quote)
				}
			}
		})
	}

	// The delimiter row of the swallowed-table case, spelled out: its cells
	// are what a grid would draw, and they are the markup.
	blocks := ParseBlocks([]byte(cases[len(cases)-1].src), nil)
	if got := blocks[3].DisplayPlain; got != "---\n---" {
		t.Fatalf("the swallowed delimiter row projects as %q, want its two cells -- the point of the fixture is that a grid draws it as content", got)
	}
}

// TestTableRowCellProjectionsAgree pins the one place a cell's three
// projections can disagree about what is in it, and it is a disagreement a
// reader SEES: Display is drawn on an uncommented row, DisplayCard on a
// commented one, and DisplayPlain is what SearchBlocks matches. A cell that is
// empty in one and not in another is a column that draws itself differently
// depending on whether somebody has commented on the row.
//
// THE MECHANISM IS base.Render(""), which is why nil styles cannot catch it
// and why TestTableIsRowsAndNothingElse -- which parses with none -- passes
// either way. A style carrying a BACKGROUND emits its SGR even for the empty
// string (36 bytes of it in the default theme), so appending the discarded
// tail unconditionally turns an empty last cell into a non-empty value on the
// styled walks and only there.
//
// THE LAST CASE IS WHY THE GUARD IS ON THE TAIL AND NOT ON THE CELL. The
// discarded tail is real text a reviewer can see in their editor -- 2 rows of
// a real corpus -- and a fix that emptied the last cell whenever the CELL
// looked empty would take that with it, because the tail hangs off exactly
// that cell.
func TestTableRowCellProjectionsAgree(t *testing.T) {
	st := darkStyles(t)
	cases := []struct {
		name string
		src  string
		// cells is the body row's cells, plain; rows is what that row draws.
		cells []string
		rows  []string
	}{
		{
			// The corpus shape: the header's last column is empty, so is the
			// row's. The cell survives (a grid needs the column) and draws
			// nothing.
			name:  "an empty last column with an empty header cell",
			src:   "| file | sites | |\n|---|---|---|\n| render.go | 2 | |\n",
			cells: []string{"render.go", "2", ""},
			rows:  []string{"render.go", "2"},
		},
		{
			// The same table's other row, whose last column has a value: it
			// still draws, and it is what says the guard is about the TAIL
			// rather than about the last column.
			name:  "a value in that last column still draws",
			src:   "| file | sites | |\n|---|---|---|\n| title.go | 1 | the definition |\n",
			cells: []string{"title.go", "1", "the definition"},
			rows:  []string{"title.go", "1", "the definition"},
		},
		{
			// The discarded tail survives the guard: GFM throws the third
			// cell away before the AST exists, and those bytes are still
			// appended raw to the last cell.
			name:  "a discarded tail is still appended",
			src:   "| Step | Owner |\n|---|---|\n| Deploy | dana | sam, and four |\n",
			cells: []string{"Deploy", "dana | sam, and four"},
			rows:  []string{"Deploy", "dana | sam, and four"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			blocks := ParseBlocks([]byte(c.src), st)
			if len(blocks) != 2 || !blocks[0].Table.Header || blocks[1].Table.Header {
				t.Fatalf("blocks = %+v, want a header row and one body row", blocks)
			}
			for i, b := range blocks {
				// The three projections are ONE walk run three times
				// (tableBlock), so a cell that carries different text in two
				// of them is that walk disagreeing with itself about what is
				// on screen.
				for j, cell := range b.Cells {
					if got := ansi.Strip(cell.Display); got != cell.DisplayPlain {
						t.Fatalf("block %d cell %d: Display strips to %q, DisplayPlain is %q", i, j, got, cell.DisplayPlain)
					}
					if got := ansi.Strip(cell.DisplayCard); got != cell.DisplayPlain {
						t.Fatalf("block %d cell %d: DisplayCard strips to %q, DisplayPlain is %q", i, j, got, cell.DisplayPlain)
					}
					// AND EMPTY IN ONE IS EMPTY IN ALL THREE, which is the
					// half the strip above cannot see: pure SGR strips to ""
					// and is not "".
					if (cell.Display == "") != (cell.DisplayPlain == "") || (cell.DisplayCard == "") != (cell.DisplayPlain == "") {
						t.Fatalf("block %d cell %d is empty in some projections and not others: %q / %q / %q", i, j, cell.Display, cell.DisplayCard, cell.DisplayPlain)
					}
				}
			}
			body := blocks[1]
			var cells []string
			for _, cell := range body.Cells {
				cells = append(cells, cell.DisplayPlain)
			}
			if !reflect.DeepEqual(cells, c.cells) {
				t.Fatalf("cells = %q, want %q", cells, c.cells)
			}
			// And the rows a reader actually gets, which is where a cell that
			// is empty in one projection and not the other shows up as a
			// blank line inside the row.
			var got []string
			for _, r := range withQuoteBar(body, 72, st, false) {
				got = append(got, strings.TrimRight(ansi.Strip(r), " "))
			}
			if !reflect.DeepEqual(got, c.rows) {
				t.Fatalf("rows = %q, want %q", got, c.rows)
			}
		})
	}
}

// TestAnAllEmptyTableRowIsEmptyAndNotItsSource pins the boundary between the
// two things "" means to a projection: EMPTY, measured, and MISSING, never
// computed.
//
// A row whose cells are all empty joins to "" (joinCells drops an empty cell),
// and "" is the sentinel displayIn (ui/render.go) and searchText (ui/search.go)
// both read as "this block has no projection, draw and search its source".
// Unguarded, the row drew `| | |` -- its own markdown, on screen, which is the
// exact byte requireNoMarkupOnScreen (app/fidelity_test.go) exists to keep off
// it -- and SearchBlocks(blocks, "|") found it.
//
// LATENT: 0 of a real corpus's 941 table rows are all-empty. It is repaired
// anyway because a spacer row is ordinary markdown and because the grid
// painter cannot repair the search half -- it draws from Block.Cells and never
// touches DisplayPlain.
//
// THE LAST CASE IS THE OTHER DIRECTION, and without it the guard could be
// "table rows never fall back", which is a different and wrong rule: a row with
// TEXT in it, parsed without styles, still falls back to its source, because
// that Display really was never computed. That is the bargain ParseBlocks
// strikes with a caller that wants blocks without a screen.
func TestAnAllEmptyTableRowIsEmptyAndNotItsSource(t *testing.T) {
	st := darkStyles(t)
	t.Run("an all-empty row draws nothing and is searched for nothing", func(t *testing.T) {
		src := "| Step | Owner |\n|---|---|\n|  |  |\n| a | b |\n"
		blocks := ParseBlocks([]byte(src), st)
		if len(blocks) != 3 {
			t.Fatalf("blocks = %d, want a header and two body rows: %+v", len(blocks), blocks)
		}
		empty := blocks[1]
		if len(empty.Cells) != 2 || empty.Cells[0].DisplayPlain != "" || empty.Cells[1].DisplayPlain != "" || empty.DisplayPlain != "" {
			t.Fatalf("the fixture's spacer row is not all-empty: %+v", empty)
		}
		// The two readers, each in its own terms.
		for _, query := range []string{"|", "| |", "|  |  |"} {
			if got := SearchBlocks(blocks, query); got != nil {
				t.Fatalf("SearchBlocks(%q) = %v, want nothing -- the row's markup is in the search index", query, got)
			}
		}
		if got := empty.displayIn(false, lipgloss.Style{}); got != "" {
			t.Fatalf("displayIn = %q, want \"\" -- an empty projection is not a missing one", got)
		}
		if got := empty.displayIn(true, lipgloss.Style{}); got != "" {
			t.Fatalf("displayIn on the card zone = %q, want \"\"", got)
		}
		for i, l := range RenderDoc(blocks, nil, nil, nil, OnLine(0), 60, "cm", st) {
			if strings.Contains(ansi.Strip(l.Text), "|") {
				t.Fatalf("line %d puts the row's own markup on screen: %q", i, ansi.Strip(l.Text))
			}
		}
		// AND IT STILL DRAWS A ROW, ONE BLANK LINE AND NOT ZERO -- and the
		// reason is the CURSOR, not the line list. renderDocPainted appends
		// blankRow(i) after every block unconditionally, so the block keeps a
		// Line and FirstLineOf still finds it; but blankRow wraps with false,
		// so that separator never carries the rail, and a block with no
		// content line of its own has nowhere for the focus to be drawn.
		//
		// IT IS THE FALLBACK ARM THAT NEEDS IT. Painted as a grid this row has
		// content lines whatever its cells hold, so the hazard is live only
		// where the grid was not drawn -- which is what this call drives.
		if got := withQuoteBar(empty, 60, st, false); len(got) != 1 || strings.TrimSpace(ansi.Strip(got[0])) != "" {
			t.Fatalf("the empty row paints %d line(s) %q, want exactly one blank one", len(got), got)
		}
	})
	t.Run("a header-only table whose header is all-empty", func(t *testing.T) {
		blocks := ParseBlocks([]byte("|  |  |\n|---|---|\n"), st)
		if len(blocks) != 1 || !blocks[0].Table.Header || blocks[0].DisplayPlain != "" {
			t.Fatalf("blocks = %+v, want one all-empty header row", blocks)
		}
		if got := SearchBlocks(blocks, "|"); got != nil {
			t.Fatalf("SearchBlocks(%q) = %v, want nothing", "|", got)
		}
	})
	t.Run("a row with text, parsed without styles, still falls back to its source", func(t *testing.T) {
		blocks := ParseBlocks([]byte("| Step | Owner |\n|---|---|\n| a | b |\n"), nil)
		row := blocks[1]
		if row.Display != "" || row.DisplayPlain == "" {
			t.Fatalf("fixture is not the no-styles shape: %+v", row)
		}
		if got, want := row.displayIn(false, lipgloss.Style{}), "| a | b |"; got != want {
			t.Fatalf("displayIn = %q, want %q -- the guard is about an EMPTY projection, not about table rows", got, want)
		}
	})
}

// TestQuotedTableRowKeepsItsQuoteDepth: QuoteDepth is the whole of what says a
// block is quoted (a Blockquote emits no Block of its own), and a row built by
// an arm that forgot to set it would render inside a quotation with no bar in
// front of it -- the words of a quoted table reading as the document's own.
// BOTH ARMS ARE DRIVEN, because the header is built by its own arm and is a
// Block of its own.
func TestQuotedTableRowKeepsItsQuoteDepth(t *testing.T) {
	src := []byte("> | Step | Owner |\n> |---|---|\n> | Deploy | dana |\n")
	st := darkStyles(t)
	blocks := ParseBlocks(src, st)
	if len(blocks) != 2 || !blocks[0].Table.Header || blocks[1].Table.Header {
		t.Fatalf("blocks = %+v, want a quoted header row and a quoted body row", blocks)
	}
	for i, b := range blocks {
		if b.Kind != KindTableRow {
			t.Fatalf("block %d Kind = %d, want KindTableRow", i, b.Kind)
		}
		if b.QuoteDepth != 1 {
			t.Fatalf("block %d QuoteDepth = %d, want 1", i, b.QuoteDepth)
		}
	}
	if want := "| Deploy | dana |"; blocks[1].Text != want {
		t.Fatalf("Text = %q, want %q", blocks[1].Text, want)
	}
	lines := RenderDoc(blocks, nil, nil, nil, OnLine(0), 80, "cm", st)
	// THE BAR LEADS THE BOX, and the ORDER is what this asserts. A quoted
	// table's rows are drawn by the grid pre-pass, which takes the bar out of
	// the width budget (paintTables) and leaves the drawing to
	// renderDocPainted, so there are two ways for a quoted grid to come out
	// wrong that an unquoted one cannot: no bar at all, and a bar drawn INSIDE
	// the border, where it would read as a cell rather than as a quotation.
	for _, want := range []string{"Step", "Deploy"} {
		got := rowBody(rowContaining(t, lines, want), 80)
		if prefix := quoteBarGlyph + " " + string(tableGridBorder().Left); !strings.HasPrefix(got, prefix) {
			t.Fatalf("the row holding %q is %q, want it to begin %q -- the bar leads the grid", want, got, prefix)
		}
	}
	// AND EVERY LINE OF THE TABLE CARRIES ONE, borders included: the box is
	// inside the quotation, so a rule drawn without a bar in front of it steps
	// out of the quotation for one row and back in for the next.
	barred := 0
	for i, l := range lines {
		if l.BlockIdx < 0 || blocks[l.BlockIdx].Kind != KindTableRow {
			continue
		}
		got := rowBody(l.Text, 80)
		// The block's own trailing separator, which carries no bar BY DESIGN
		// -- see quoteBarGlyph's last paragraph for what that costs and what
		// closing it would take. It is the only blank row a table row emits,
		// and the count below is what keeps this skip from swallowing a grid
		// line that had lost its bar.
		if got == "" {
			continue
		}
		if !strings.HasPrefix(got, quoteBarGlyph+" ") {
			t.Fatalf("line %d of the quoted table carries no bar: %q", i, got)
		}
		barred++
	}
	// Two rows and three rules: the box's top and bottom and the one between
	// them.
	if barred != 5 {
		t.Fatalf("the quoted table drew %d barred lines, want 5 -- a header, a body row, and the three rules of the box around them", barred)
	}
}

// narrowWidth is the narrowest terminal the review view is ever drawn in: the
// app's own minimum-size gate, listMinWidth (app/list.go), which is 31 at this
// commit. Written as a literal because app imports ui and not the other way
// round. If the gate moves this drive measures a width the app no longer
// allows, which makes it a weaker test rather than a wrong one.
const narrowWidth = 31

// TestTableRowsFitTheirWidthBudget drives the row arm at both ends of the
// range a reader can put it in and asserts the frame invariant every other
// row in this view is held to: one Line is ONE screen line of exactly the
// terminal's width. Over-wide content does not spill into the margin -- it is
// re-wrapped by the lipgloss Width() the row is painted into, which puts two
// screen lines inside one Line and breaks the line-to-block mapping every
// cursor movement resolves through.
//
// CJK AND EMOJI ARE HERE BECAUSE REAL DOCUMENTS CARRY THEM, and what they buy
// is worth stating precisely: the arm does no clipping and no per-cell
// arithmetic, so wide characters cannot produce a clipper's off-by-one. What
// they exercise is ansi.Wordwrap measuring in CELLS and this arm passing a
// budget through unmangled -- a two-cell glyph costing twice an ASCII one, and
// a ZWJ family sequence costing two cells for seven runes.
//
// THE ARM'S OWN ARITHMETIC IS NOT WHAT THIS TEST HOLDS: subtracting nothing
// from the budget and still indenting is INVISIBLE here.
// TestTableRowSpendsItsContinuationIndentExactlyOnce is where that lives, with
// a fixture built to make it visible.
//
// The fixture's longest space-free run is 16 cells, inside the narrowest
// budget any case here gets. That is deliberate and it is the ONE hazard this
// test does not cover: see TestWideRunOverflowsWrapPlainButNotTheGrid.
func TestTableRowsFitTheirWidthBudget(t *testing.T) {
	const src = `| 手順 | note | 状態 |
|---|---|---|
| 配置する手順です | run after 5pm, once the freeze is over | 🎉 done |
| 確認する | check the on-call ack 👨‍👩‍👧‍👦 first | ⏳ |

> | 手順 | note |
> |---|---|
> | 配置する | quoted 🎉 row |
`
	st := darkStyles(t)
	blocks := ParseBlocks([]byte(src), st)
	// Five: a header and two body rows for the first table, a header and one
	// body row for the quoted one. The header is a row like any other (see
	// KindTableRow), so it is painted by the same arm and held to the same
	// invariant.
	if len(blocks) != 5 {
		t.Fatalf("blocks = %d, want 5 rows across two tables: %+v", len(blocks), blocks)
	}
	// The fixture cannot silently stop being the fixture: if the wide
	// characters ever fall out of it, the drive below is ASCII-only again and
	// says nothing about cells.
	var projected []string
	for _, b := range blocks {
		projected = append(projected, b.DisplayPlain)
	}
	for _, want := range []string{"手順", "🎉", "👨\u200d👩\u200d👧\u200d👦"} {
		if !strings.Contains(strings.Join(projected, "\n"), want) {
			t.Fatalf("the fixture no longer projects %q, so this is an ASCII-only drive", want)
		}
	}
	for _, width := range []int{80, narrowWidth} {
		for i, l := range RenderDoc(blocks, nil, nil, nil, OnLine(0), width, "cm", st) {
			plain := ansi.Strip(l.Text)
			if strings.Contains(plain, "\n") {
				t.Fatalf("width %d, line %d holds more than one screen row: %q", width, i, plain)
			}
			if got := ansi.StringWidth(plain); got != width {
				t.Fatalf("width %d, line %d is %d cells: %q", width, i, got, plain)
			}
		}
	}
}

// TestTableRowSpendsItsContinuationIndentExactlyOnce is the arm's width
// arithmetic, driven by a fixture built to make an error in it VISIBLE. The
// arm has exactly one sum -- wrapPlain(cell, width-tableContinuationCols)
// paired with the same constant prepended to every continuation -- and there
// are only two ways to get it wrong: not spend it, which puts a row over the
// width it is painted into, or spend it twice, which leaves every row short.
//
// BOTH SIDES ARE ASSERTED, because only one of them is a visible failure. A
// row over budget is caught by "widest <= budget"; a row short of budget
// breaks nothing and shows up only as slack, so it is caught by
// "widest >= budget-1" -- which doubles as the assertion that the FIXTURE
// still reaches its budget at all. A fixture that stopped wrapping would pass
// a one-sided test in silence.
//
// ONE-CELL WORDS ARE WHY IT WORKS AT EVERY WIDTH. ansi.Wordwrap breaks between
// words, so a value of single-character words fills every line to within one
// cell of whatever budget it is handed. That is what makes a two-cell error
// always cross the line rather than land in slack.
func TestTableRowSpendsItsContinuationIndentExactlyOnce(t *testing.T) {
	// Long enough to wrap more than once at the widest budget any case here
	// gets (72), so at least one INDENTED continuation exists to measure.
	value := strings.TrimSpace(strings.Repeat("a b c d e f g h i j k l m n o p q r s t u v w x y z ", 3))
	src := "| t |\n|---|\n| " + value + " |\n\n> | t |\n> |---|\n> | " + value + " |\n"
	st := darkStyles(t)
	parsed := ParseBlocks([]byte(src), st)
	// Four blocks now, two of them headers -- and the headers are NOT what
	// this measures: their one cell is "t", which wraps at no width, so the
	// "no continuation row" assertion below would fail on them for a reason
	// that is about the fixture and not about the arithmetic.
	if len(parsed) != 4 || parsed[0].QuoteDepth != 0 || parsed[2].QuoteDepth != 1 {
		t.Fatalf("fixture is no longer one unquoted and one quoted table: %+v", parsed)
	}
	var blocks []Block
	for _, b := range parsed {
		if !b.Table.Header {
			blocks = append(blocks, b)
		}
	}
	if len(blocks) != 2 || blocks[0].QuoteDepth != 0 || blocks[1].QuoteDepth != 1 {
		t.Fatalf("fixture is no longer one unquoted and one quoted body row: %+v", blocks)
	}
	for _, width := range []int{80, narrowWidth} {
		// The budget renderDocPainted hands the arms, quote bars included:
		// withQuoteBar takes those out itself, so every row it returns --
		// bar and all -- has to fit this.
		budget := width - 2*RailWidth(width) - gutterWidth
		for i, b := range blocks {
			widest, indented := 0, false
			for _, row := range withQuoteBar(b, budget, st, false) {
				plain := ansi.Strip(row)
				if w := ansi.StringWidth(plain); w > widest {
					widest = w
				}
				indented = indented || strings.HasPrefix(strings.TrimPrefix(plain, quoteBarGlyph+" "), strings.Repeat(" ", tableContinuationCols))
			}
			if widest > budget {
				t.Fatalf("width %d, block %d: widest row is %d cells against a budget of %d -- the continuation indent is not coming out of the budget", width, i, widest, budget)
			}
			if widest < budget-1 {
				t.Fatalf("width %d, block %d: widest row is only %d cells of a %d budget -- either the indent is spent twice or the fixture has stopped filling its lines", width, i, widest, budget)
			}
			if !indented {
				t.Fatalf("width %d, block %d: no continuation row, so nothing here measures the indent at all", width, i)
			}
		}
	}
}

// TestClampTableWidthFloorsAtOne pins clampTableWidth as a pure budget ->
// clamped budget function. See its own comment (ui/painted.go) for why the
// floor is 1 and not 0, matching KindRule's max(width, 0): to
// lipgloss/v2/table, Width(0) means the same "unset, natural width" that
// Width(a negative number) and never calling Width at all mean.
func TestClampTableWidthFloorsAtOne(t *testing.T) {
	cases := []struct {
		name  string
		width int
		want  int
	}{
		{"comfortable", 160, 160},
		{"the width the grid measurement was taken at", 76, 76},
		{"narrow", 40, 40},
		{"narrower", 20, 20},
		{"at the edge of the floor", 10, 10},
		{"the smallest positive width", 1, 1},
		{"zero -- lipgloss's own natural-width sentinel, not a legitimate table width", 0, 1},
		{"negative", -1, 1},
		{"far negative", -160, 1},
		{"minimum int", math.MinInt, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := clampTableWidth(c.width); got != c.want {
				t.Fatalf("clampTableWidth(%d) = %d, want %d", c.width, got, c.want)
			}
		})
	}
}

// gridFixture is the table these grid tests are driven against: three columns,
// a natural width of 71 cells, and wide characters in two of them. The natural
// width is what makes the budget cases two-sided -- the grid is EXPANDED to 160
// and 76 and SHRUNK to 40 and below, so a Width() call that stopped being made
// fails in both directions. The wide characters are not decoration either:
// lipgloss's resizer measures in CELLS.
const gridFixture = `| 手順 | note | 状態 |
|---|---|---|
| 配置する手順です | run after 5pm, once the freeze is over | 🎉 done |
| 確認する | check the on-call ack 👨‍👩‍👧‍👦 first | ⏳ |
`

// gridBlocks parses src and returns its table rows, checking the fixture is
// still the shape the caller thinks it is.
func gridBlocks(t *testing.T, src string, st *Styles, wantRows int) []Block {
	t.Helper()
	blocks := ParseBlocks([]byte(src), st)
	var rows []Block
	for _, b := range blocks {
		if b.Kind == KindTableRow {
			rows = append(rows, b)
		}
	}
	if len(rows) != wantRows || len(blocks) != wantRows {
		t.Fatalf("fixture parses to %d blocks, %d of them table rows, want %d of each", len(blocks), len(rows), wantRows)
	}
	if !rows[0].Table.Header {
		t.Fatalf("the fixture's first row is not the header: %+v", rows[0].Table)
	}
	return rows
}

// TestTableGridFitsItsWidthBudget is the FRAME INVARIANT, and it is here
// rather than beside clampTableWidth because "no rendered line exceeds the
// budget" needs a renderer.
//
// THIRTEEN BUDGETS. Eight are the plan's -- 160, 76, 40, 20, 10, 1, 0 and a
// large negative -- and the last three of those are the ones the clamp exists
// for: 1 is the floor itself, and 0 and a negative are the two values lipgloss
// reads as "unset, natural width". The ninth is 2, the boundary of the
// painter's own cliff below, because a boundary asserted only from one side is
// not asserted.
//
// AND 70, 69, 68, 67 ARE gridFixture'S OWN OPEN BAND, ALL FOUR OF IT: the
// fixture's natural width is 71, and the budgets where lipgloss over-allocates
// and its own MaxWidth then eats the right-hand border sit IMMEDIATELY BELOW
// that -- so the eight budgets above, which jump from 76 to 40, drove either
// side of the band and never through it. The right-border assertion below
// would have passed on a renderer that never closed a box at all. A test
// driven at a width where its subject does not exist is not a test of that
// subject.
//
// IT IS TWO-SIDED, PER LINE. EVERY line must EQUAL the clamped budget, not
// merely fit inside it -- see gridFixture for the natural width that makes both
// directions reachable. A Width() call that silently stopped being made would
// leave every line at 71, which fails on the wide budgets by being short and on
// the narrow ones by being over. The widest-equals form is not enough: ONE
// short line among full-width border lines satisfies both "no line over" and
// "the widest equals", and that is exactly what closeGridRight's unpadded
// repair produced.
//
// THE DEFECT AT ITS CONSUMER IS NOT ASSERTED HERE. This test used to MODEL
// renderDocPainted's outer Width() wrap, which the renderer no longer makes on
// a grid row -- so the model became a model of something the renderer does not
// do. TestGridRowsSurviveAControlByte drives the real ui.RenderDoc instead.
// What is left here is this function's own subject, the render's geometry
// against its budget.
//
// THE PAINTER'S OWN CLIFF IS PINNED HERE TOO, because it is a consequence of
// the same clamp: at a clamped budget of 1 every line is a single border rune,
// splitTableGrid cannot tell a rule from a row, and paintTables returns NOTHING
// for the table. A caller with no group for a block can draw it some other way;
// a caller handed a wrong grouping cannot tell.
func TestTableGridFitsItsWidthBudget(t *testing.T) {
	st := darkStyles(t)
	rows := gridBlocks(t, gridFixture, st, 3)
	for _, budget := range []int{160, 76, 70, 69, 68, 67, 40, 20, 10, 2, 1, 0, -160} {
		clamped := clampTableWidth(budget)
		for _, onCard := range []bool{false, true} {
			drawn := 0
			for i, line := range strings.Split(tableGridString(rows, budget, st, onCard), "\n") {
				plain := ansi.Strip(line)
				drawn++
				if w := ansi.StringWidth(plain); w != clamped {
					t.Fatalf("budget %d (clamped %d), onCard=%v, line %d is %d cells -- every line of a grid is exactly its budget, over OR under: %q", budget, clamped, onCard, i, w, plain)
				}
			}
			// A render of nothing satisfies the loop above vacuously. The
			// fixture is three rows in a box, so its smallest render is five
			// screen rows and it never gets smaller: the box is drawn at every
			// budget, including the clamped 1 where it is one rune per line.
			if drawn < 5 {
				t.Fatalf("budget %d (clamped %d), onCard=%v: the render is %d lines -- the fixture is a three-row box and cannot draw fewer than 5", budget, clamped, onCard, drawn)
			}
			// AND THE BOX IS SHUT ON THE RIGHT, which the width invariant
			// above is silent about: a grid whose right-hand border lipgloss
			// truncated away is exactly the budget wide and still splits into
			// rows. It is here rather than in a test of its own because "no
			// line over the budget" and "every line closed" are the two halves
			// of one claim about the same render, and a reader who finds one
			// without the other will assume the second follows. It does not.
			//
			// BOTH TWINS, because the closer is RENDERED and not appended: a
			// bare rune would land unstyled, and on a Card row that is the
			// two-zone failure paintOneTable draws two twins to avoid.
			//
			// A CLAMPED BUDGET OF 2 IS EXEMPT, and closeGridRight says why:
			// closing a line there would spend the only non-border cell it has
			// and make a content row indistinguishable from a separator, so
			// splitTableGrid would cut nothing for the table at all. 2 is the
			// only budget at which that trade arises.
			if clamped > 2 {
				for i, line := range strings.Split(tableGridString(rows, budget, st, onCard), "\n") {
					plain := []rune(ansi.Strip(line))
					if len(plain) == 0 {
						t.Fatalf("budget %d, onCard=%v, line %d is empty", budget, onCard, i)
					}
					if want, got, ok := gridLineCloser(line); !ok {
						t.Fatalf("budget %d (clamped %d), onCard=%v, line %d opens on %q and closes on %q, want %q: %q", budget, clamped, onCard, i, string(plain[0]), got, want, string(plain))
					}
				}
			}
		}
		painted := paintTables(rows, budget, st, nil, nil)
		want := len(rows)
		if clamped == 1 {
			want = 0
		}
		if len(painted) != want {
			t.Fatalf("budget %d (clamped %d): paintTables returned %d groups, want %d", budget, clamped, len(painted), want)
		}
		for idx, group := range painted {
			if len(group) == 0 {
				t.Fatalf("budget %d: block %d has an empty group", budget, idx)
			}
			for _, line := range group {
				if w := ansi.StringWidth(ansi.Strip(line)); w > clamped {
					t.Fatalf("budget %d: a painted line for block %d is %d cells: %q", budget, idx, w, ansi.Strip(line))
				}
			}
		}
	}
}

// gridBoxOpenings counts the Lines of ONE rendered document that open on the
// box's own TOP-LEFT CORNER, read off tableGridBorder(). It is the only
// non-vacuity guard in this package that can see whether the grid pre-pass ran
// at all, and it exists because A LINE COUNT CANNOT SEE IT: defeat the pre-pass
// entirely and a table row falls to renderBlockPainted's KindTableRow arm,
// which renders it through Width(rowWidth) -- padding it to exactly the frame
// and wrapping it into MORE Lines, not fewer:
//
//	                              table-row Lines   openings
//	as it renders                     11 / 6 / 6 / 6        1
//	pre-pass defeated                  5 / 5 / 5 / 5        0
//	gridRowGroup truncated to line 0   3 / 3 / 3 / 3        1
//
// The middle row does not merely fail to drop, it lands on exactly 5 -- ONE OFF
// a floor written as "a two-row box cannot paint fewer than 5". The two guards
// are kept together because they catch DIFFERENT things, and the third row is
// why: a grid that still opens but stops emitting its rows is invisible to this
// and caught by the count.
//
// A '┌' AT THE HEAD OF A ROW BODY CANNOT COME FROM ANYWHERE ELSE:
// renderBlockPainted's KindTableRow arm draws the row's own text and nothing
// else, so only the pre-pass's box puts a border glyph in the first column. The
// caller's blocks are consulted so a paragraph that happened to start with one
// could not answer for a table.
func gridBoxOpenings(lines []Line, blocks []Block, width int) int {
	opener := tableGridBorder().TopLeft
	n := 0
	for _, l := range lines {
		if l.IsThread || l.BlockIdx < 0 || l.BlockIdx >= len(blocks) || blocks[l.BlockIdx].Kind != KindTableRow {
			continue
		}
		// The rail band and the gutter off the left (rowBody), then the quote
		// bar: TrimLeft, so it is depth-agnostic and leaves an unquoted row
		// untouched -- no border rune is in the cut set.
		if strings.HasPrefix(strings.TrimLeft(rowBody(l.Text, width), quoteBarGlyph+" "), opener) {
			n++
		}
	}
	return n
}

// gridWideGlyphFixture is the table that puts a DOUBLE-WIDTH GLYPH ON THE
// TRUNCATION BOUNDARY, which is the one shape closeGridRight's repair could not
// measure its way out of. Two columns, an empty first cell so the second gets
// nearly the whole budget, and a trailing '✅' -- two cells to ansi.StringWidth,
// one rune, and indivisible.
//
// ITS BUDGET IS 16 AND THAT IS DERIVED, NOT DECORATIVE: at 17 the cell renders
// whole, at 16 the glyph is cut and the repair has to make up the cell, and at
// 15 the text is short enough that nothing is cut at all. The test below
// asserts that 16 really is the cut budget, so an allocator change that moved
// it fails LOUDLY rather than leaving a fixture that no longer reaches its
// case.
const gridWideGlyphFixture = "| Step | Owner |\n|---|---|\n|  | dana ✅ |\n"

// TestAGridLineIsNeverShortOfItsBudget is the OBSERVABLE for closeGridRight's
// pad, and it exists because the pad shipped with nothing pinning it:
// reverting it left `go test ./...` and both gated corpus sweeps green.
//
// WHAT GOES WRONG WITHOUT IT. closeGridRight repairs a truncated line as
// `ansi.Truncate(line, width-1, "") + closer`. ansi.Truncate never SPLITS a
// double-width glyph -- `ansi.Truncate("abc✅", 4, "")` is `"abc"`, three cells,
// against `ansi.Truncate("abcd", 4, "")`'s four -- so when the boundary falls
// inside one it cuts BEFORE it and the repaired line comes back a cell short of
// the budget every other line of the box is drawn to. On screen that is one
// column of the terminal's own background at the right-hand edge of one row,
// with that row's border pulled a column left of the rest of the box. It
// reached a real corpus: ten table-row Lines one cell short, at widths 61, 62
// and 96, every one far above listMinWidth's 31.
//
// WHY IT IS DRIVEN HERE AND NOT ON gridFixture: gridFixture carries wide glyphs
// too, but none of its thirteen driven budgets happens to put one on the
// boundary -- which is how a defect measured over 116 corpus tables at 199
// budgets can be invisible to a fixture at 13. So this fixture is constructed
// to land on it, and asserted to still land on it.
//
// Both halves -- the pre-pass and the consumer -- are kept because they fail
// for one reason in two places a reader looks.
func TestAGridLineIsNeverShortOfItsBudget(t *testing.T) {
	st := darkStyles(t)
	blocks := ParseBlocks([]byte(gridWideGlyphFixture), st)
	rows := gridBlocks(t, gridWideGlyphFixture, st, 2)

	// THE PREMISE, asserted rather than assumed. The glyph is two cells wide and
	// one rune, which is the whole reason ansi.Truncate cannot divide it.
	const glyph = "✅"
	if w := ansi.StringWidth(glyph); w != 2 {
		t.Fatalf("%q is %d cells here, not 2 -- this fixture no longer puts a double-width glyph anywhere", glyph, w)
	}
	if got := rows[1].Cells[1].DisplayPlain; !strings.HasSuffix(got, glyph) {
		t.Fatalf("the fixture's second cell projects to %q, which does not end in %q", got, glyph)
	}
	// AND THAT 16 IS STILL THE CUT BUDGET. At 17 the glyph survives the render;
	// at 16 it does not. If lipgloss's allocator ever moves that boundary this
	// fails, and re-deriving it is the right response -- a fixture that stopped
	// reaching its own case is worse than no fixture.
	const cutBudget = 16
	for _, c := range []struct {
		budget int
		whole  bool
	}{{cutBudget + 1, true}, {cutBudget, false}} {
		if got := strings.Contains(ansi.Strip(tableGridString(rows, c.budget, st, false)), glyph); got != c.whole {
			t.Fatalf("at budget %d the render %s %q, want %v -- %d is no longer the budget at which the glyph is cut", c.budget, map[bool]string{true: "carries", false: "does not carry"}[got], glyph, c.whole, cutBudget)
		}
	}

	// THE PRE-PASS SIDE. Every line of every render, both twins, exactly the
	// clamped budget -- the same claim TestTableGridFitsItsWidthBudget makes,
	// on the fixture that reaches the case it could not.
	for budget := 2; budget <= 40; budget++ {
		clamped := clampTableWidth(budget)
		for _, onCard := range []bool{false, true} {
			for i, line := range strings.Split(tableGridString(rows, budget, st, onCard), "\n") {
				plain := ansi.Strip(line)
				if w := ansi.StringWidth(plain); w != clamped {
					t.Fatalf("budget %d (clamped %d), onCard=%v, line %d is %d cells -- a wide glyph on the truncation boundary took a cell the repair did not put back: %q", budget, clamped, onCard, i, w, plain)
				}
			}
		}
	}

	// THE CONSUMER SIDE, which is where a reader meets it: a table-row Line
	// that is not exactly the frame width leaves the terminal's own background
	// showing. 20 is the terminal width at which this fixture's budget is 16 --
	// gutterWidth off the left and no rail band below railMinWidth -- and the
	// band around it is driven so the arithmetic is not a magic number.
	//
	// IT CARRIES BOTH FLOORS. The line count below cannot see whether a grid
	// was drawn (gridBoxOpenings records the measurement in full), so with only
	// that floor this loop PASSED with the pre-pass defeated entirely.
	for width := 14; width <= 48; width++ {
		lines := RenderDoc(blocks, nil, nil, nil, Cursor{Block: NoCursor}, width, "cm", st)
		checked := 0
		for j, l := range lines {
			if l.IsThread || l.BlockIdx < 0 || l.BlockIdx >= len(blocks) || blocks[l.BlockIdx].Kind != KindTableRow {
				continue
			}
			checked++
			plain := ansi.Strip(l.Text)
			if w := ansi.StringWidth(plain); w != width {
				t.Fatalf("terminal width %d: table-row Line %d (block %d) is %d cells, want exactly %d: %q", width, j, l.BlockIdx, w, width, plain)
			}
		}
		// A GRID WAS DRAWN. Without this the loop above measures whatever
		// renderBlockPainted painted instead and reports it fits, which it
		// always does -- that arm pads to the frame by construction.
		if opens := gridBoxOpenings(lines, blocks, width); opens == 0 {
			t.Fatalf("terminal width %d: no Line opens on %q -- the pre-pass drew no grid and every width assertion above passed anyway", width, tableGridBorder().TopLeft)
		}
		// AND IT DREW ITS WHOLE BOX. This one the opening cannot see: a grid
		// that still opens and stops emitting rows keeps its '┌' and loses its
		// lines. Five is the box's own minimum -- top border, header, separator,
		// body row, bottom border -- and not the loop's total, which also counts
		// the trailing blank row and would be a brittle thing to pin.
		if checked < 5 {
			t.Fatalf("terminal width %d: only %d table-row Lines -- the fixture is a two-row box and cannot paint fewer than 5", width, checked)
		}
	}
}

// gridControlBytes is every byte the C0/DEL class puts in a cell that a
// terminal EXECUTES rather than prints, minus '\t' -- which lipgloss expands to
// spaces before anything measures it, so it is not in this class at all.
var gridControlBytes = []string{"\x01", "\x07", "\x08", "\x0b", "\x0c", "\x0d", "\x1a", "\x7f"}

// TestGridRowsSurviveAControlByte is THE FRAME INVARIANT AT ITS CONSUMER, and
// it drives the real ui.RenderDoc rather than modelling it.
//
// WHAT IT IS PINNING. app/painted.go writes exactly one "\n" per ui.Line into
// the frame it hands bubbletea, so a Line holding TWO screen rows makes the
// rendered frame taller than the viewport it was cut to: the help bar is pushed
// off the canvas and every row below the table is one screen row out of step
// with what FirstLineOf says it is. A grid row is the only row in this view
// that arrives EXACTLY its budget wide, and it used to be re-measured by
// Style.Width -- a second width authority that counts a bare C0 or DEL byte as
// ONE cell where ansi.StringWidth, which the grid was fitted to, counts it as
// ZERO. Before the fix, 957 of 6,296 Lines over this matrix exceeded the
// terminal width and every one carried an embedded newline; after it, 0 and 0.
//
// EVERY ONE OF THE EIGHT BYTES CONTRIBUTES, so no subtest here is decoration:
// the five that are not unicode.IsSpace go over budget 123 times each and
// '\v' '\f' '\r' 114 times each, the two counts being the wrap's two arms.
//
// IT IS TWO-SIDED. Since renderDocPainted no longer calls Style.Width on a grid
// row, nothing pads a short one back out to the frame, so a table-row Line must
// be EXACTLY the terminal width and not merely within it.
//
// IT SAYS NOTHING ABOUT WHAT THE BYTES SHOULD BE, deliberately: what is
// asserted is the property (one Line, one screen row, exactly the frame's
// width) and not the mechanism, so a later sanitising pass satisfies this test
// rather than fighting it.
func TestGridRowsSurviveAControlByte(t *testing.T) {
	st := darkStyles(t)
	// FOUR SHAPES, and each is a different path to the same cell. The header
	// row is drawn from DisplayPlain and re-styled here (tableGridRow), a body
	// cell is PICKED from the zone's own projection, a quoted table has the bar
	// taken out of its budget by the pre-pass and put back by the row, and the
	// fourth puts all eight bytes in ONE cell -- so a fix that handled them one
	// at a time and not together fails -- with the swept byte in a SECOND cell
	// of the same row, which keeps the matrix rectangular and drives TWO
	// control-bearing cells in one row.
	shapes := []struct{ name, src string }{
		{"a body cell", "| 手順 | note | 状態 |\n|---|---|---|\n| a%[1]sb | run after 5pm, once the freeze is over | 🎉 done |\n"},
		{"a header cell", "| a%[1]sb | note | 状態 |\n|---|---|---|\n| 配置する手順です | run after 5pm | 🎉 done |\n"},
		{"a quoted cell", "> | 手順 | note |\n> |---|---|\n> | a%[1]sb | run after 5pm, once the freeze is over |\n"},
		{"all eight in one cell", "| 手順 | note | 状態 |\n|---|---|---|\n| \x01\x07\x08\x0b\x0c\x0d\x1a\x7f | run a%[1]sfter 5pm, once the freeze is over | 🎉 done |\n"},
	}
	// 31 is app/list.go's listMinWidth, the narrowest terminal the app will
	// render a document in at all; 76 and 80 straddle railMinWidth, so both the
	// banded and the unbanded margin arithmetic are driven.
	widths := []int{31, 40, 60, 76, 80, 100, 120, 160}
	v := ThreadView{Thread: domain.Thread{Comments: []domain.Comment{{Body: "a note"}}}, Placed: true}
	// THREE CARD STATES AND NOT TWO. A COLLAPSED thread moves the row into the
	// Card zone without drawing anything under it, and an EXPANDED one also
	// makes the pre-pass close the box above the card and reopen it below
	// (gridRowGroup) -- which is the arm that splices lines the single render
	// did not produce for that row, and the one a width fix could miss.
	cards := []struct {
		name     string
		expanded bool
		on       bool
	}{{"no comment", false, false}, {"a collapsed comment", false, true}, {"an expanded comment", true, true}}

	// A DOCUMENT MUST DRAW A BOX, which is the assertion the non-vacuity floor
	// below cannot make. gridBoxOpenings is the question and the evidence for
	// it; it is a shared helper because TestAGridLineIsNeverShortOfItsBudget
	// owes the same guard.
	checked, docs, boxes := 0, 0, 0
	for _, shape := range shapes {
		for _, ctl := range gridControlBytes {
			src := fmt.Sprintf(shape.src, ctl)
			blocks := ParseBlocks([]byte(src), st)
			last := -1
			for i, b := range blocks {
				if b.Kind == KindTableRow {
					last = i
				}
			}
			if last < 0 {
				t.Fatalf("%s with %q parses to no table row: %+v", shape.name, ctl, blocks)
			}
			for _, width := range widths {
				for _, card := range cards {
					docs++
					views := map[int][]ThreadView{}
					expanded := map[int]bool{}
					if card.on {
						views[last] = []ThreadView{v}
						expanded[last] = card.expanded
					}
					lines := RenderDoc(blocks, views, nil, expanded, OnLine(last), width, "cm", st)
					for j, l := range lines {
						plain := ansi.Strip(l.Text)
						if strings.Contains(plain, "\n") {
							t.Fatalf("%s, byte %q, width %d, %s: Line %d (block %d) holds two screen rows: %q", shape.name, ctl, width, card.name, j, l.BlockIdx, plain)
						}
						if w := ansi.StringWidth(plain); w > width {
							t.Fatalf("%s, byte %q, width %d, %s: Line %d (block %d) is %d cells against a %d-cell frame: %q", shape.name, ctl, width, card.name, j, l.BlockIdx, w, width, plain)
						}
						if l.IsThread || l.BlockIdx < 0 || l.BlockIdx >= len(blocks) || blocks[l.BlockIdx].Kind != KindTableRow {
							continue
						}
						checked++
						if w := ansi.StringWidth(plain); w != width {
							t.Fatalf("%s, byte %q, width %d, %s: table-row Line %d (block %d) is %d cells, want exactly %d -- a grid row is painted with no Width() to pad it: %q", shape.name, ctl, width, card.name, j, l.BlockIdx, w, width, plain)
						}
					}
					if gridBoxOpenings(lines, blocks, width) == 0 {
						t.Fatalf("%s, byte %q, width %d, %s: no Line opens on %q -- the pre-pass drew no grid and every assertion above passed anyway", shape.name, ctl, width, card.name, tableGridBorder().TopLeft)
					}
					boxes++
				}
			}
		}
	}
	// NON-VACUOUS, AND THE GUARD THAT DOES THAT WORK IS THE BOX ASSERTION
	// ABOVE, not the Line count below. THE COUNT CANNOT SEE WHETHER A GRID WAS
	// DRAWN AT ALL: defeating the pre-pass entirely left this test PASSING with
	// the count moving the WRONG WAY, 5,304 up to 6,288, because
	// renderBlockPainted's KindTableRow arm renders a plain row through
	// Width(rowWidth), which pads it to exactly the frame AND wraps it into MORE
	// Lines than a grid produces. Both assertions in the loop were then
	// satisfied trivially and the test degenerated into "plain table rows are
	// padded to width".
	//
	// THE THREE COUNTS BELOW ARE THE MATRIX'S OWN ARITHMETIC rather than round
	// numbers: 4 shapes x 8 bytes x 8 widths x 3 comment states is 768 driven
	// documents, every one of them must draw a box, and the smallest box any of
	// them can paint is 5 screen rows. The actual Line count is LOGGED and not
	// asserted, because it moves with any change to how tall a grid is.
	if want := len(shapes) * len(gridControlBytes) * len(widths) * len(cards); docs != want {
		t.Fatalf("%d documents driven, want %d -- the matrix is not the one this test's comment describes", docs, want)
	}
	if boxes != docs {
		t.Fatalf("%d of %d documents drew a box", boxes, docs)
	}
	if checked < 5*docs {
		t.Fatalf("only %d table-row Lines were checked over %d driven documents, want at least %d -- the matrix is not reaching the grid", checked, docs, 5*docs)
	}
	t.Logf("%d table-row Lines checked over %d documents, all %d drawing a box", checked, docs, boxes)
}

// fidelityTables is every table in the fidelity fixture, in document order,
// each as its own rows with the header first. Grouped on Block.Table.ID for
// the reason paintTables is (TableRef): two tables separated by a blank line
// are block-adjacent with nothing between them.
func fidelityTables(tb testing.TB, st *Styles) [][]Block {
	tb.Helper()
	src, err := os.ReadFile("testdata/fidelity.md")
	if err != nil {
		tb.Fatal(err)
	}
	return tablesOf(ParseBlocks(src, st))
}

// tablesOf groups a block list into its tables, in the order they are first
// seen. Shared with the gated corpus sweep, which asks the same questions of
// documents nobody wrote for a test -- one definition, so the two cannot come
// to disagree about what a table is.
func tablesOf(blocks []Block) [][]Block {
	byTable := map[int][]Block{}
	var order []int
	for _, b := range blocks {
		if b.Kind != KindTableRow || b.Table.ID == 0 {
			continue
		}
		if _, seen := byTable[b.Table.ID]; !seen {
			order = append(order, b.Table.ID)
		}
		byTable[b.Table.ID] = append(byTable[b.Table.ID], b)
	}
	out := make([][]Block, 0, len(order))
	for _, id := range order {
		out = append(out, byTable[id])
	}
	return out
}

// gridHardFloor is the narrowest budget at which NO column allocator could
// draw this table without breaking a word: every column at least its own
// longest unbreakable token, plus the padding every cell carries and the
// border between every pair.
//
// IT IS THE CEILING ON A CUSTOM ALLOCATOR and that is the only reason it is
// recorded. lipgloss's resizer is what ships; this is the figure any later
// attempt has to beat. A table whose hard floor is at or below the budget CAN
// be drawn break-free by some allocator; one above it cannot be drawn
// break-free by any.
//
// A TOKEN IS WHITESPACE-DELIMITED, which is not quite what the wrapper does
// and is the conservative direction: ansi's Wrap treats a hyphen as a
// breakpoint too, so a token this counts whole may in fact be breakable and the
// real floor may be lower.
func gridHardFloor(rows []Block) int {
	cols := len(rows[0].Cells)
	need := make([]int, cols)
	for _, b := range rows {
		for j, c := range b.Cells {
			if j >= cols {
				continue
			}
			for _, tok := range strings.Fields(c.DisplayPlain) {
				if w := ansi.StringWidth(tok); w > need[j] {
					need[j] = w
				}
			}
		}
	}
	total := cols + 1
	for _, n := range need {
		total += n + 2*tableGridPadCols
	}
	return total
}

// gridTailedRows is how many BODY rows of one table carry a discarded tail: a
// row whose source line holds more unescaped-pipe-separated fields than the
// header has columns, which is the condition GFM answers by throwing the excess
// away before the AST exists.
//
// IT READS THE SOURCE LINE AND NOT THE PROJECTED CELL, and the difference is
// the whole reason it exists. The obvious predicate --
// strings.Contains(lastCell.DisplayPlain, "|") -- names a different population:
// over a real corpus it matches 15 body rows where exactly 2 carry a tail, because
// a cell may hold a pipe of its own. On the committed fixture the two agree
// today, which is precisely how a shape assertion comes to pass on the wrong
// evidence.
//
// THE SPLIT IS ON UNESCAPED PIPES because GFM's is: a backslash-escaped '|' is
// a cell's own character and not a separator. The leading and trailing empty
// fields the outer pipes produce are dropped, which is what makes the count
// comparable to the header's cell count.
func gridTailedRows(rows []Block) int {
	cols := len(rows[0].Cells)
	n := 0
	for _, b := range rows[1:] {
		if len(gridSourceFields(b.Text)) > cols {
			n++
		}
	}
	return n
}

// gridSourceFields splits one table row's source line the way GFM does: on
// unescaped '|', with the empty fields the outer pipes create dropped.
func gridSourceFields(line string) []string {
	var out []string
	var cur strings.Builder
	esc := false
	for _, r := range line {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\':
			cur.WriteRune(r)
			esc = true
		case r == '|':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	out = append(out, cur.String())
	if len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return out
}

// gridColumnText cuts one rendered CONTENT line into its per-column text,
// trimmed of the padding the box puts around it, and answers false if the line
// does not part into exactly want columns.
//
// IT CUTS ON THE DIVIDER RUNE AND NOT ON A COLUMN OFFSET, and the choice is
// measured rather than stylistic: cutting at cell offsets means a rune-at-a-time
// walk, which gets a multi-rune grapheme wrong -- '⚠️' is one cell to the
// grapheme-aware measurement lipgloss wraps with and two to a rune-at-a-time
// one, so the offsets drift and a border rune lands inside the text. That
// produced six false mid-word-break readings before it was found. The divider
// rune has one different failure: a CELL whose own text contains '│' parts into
// too many fields, which is what the false return is for.
func gridColumnText(line string, want int) ([]string, bool) {
	plain := ansi.Strip(line)
	plain = strings.TrimPrefix(plain, "│")
	plain = strings.TrimSuffix(plain, "│")
	f := strings.Split(plain, "│")
	if len(f) != want {
		return nil, false
	}
	for i := range f {
		f[i] = strings.TrimSpace(f[i])
	}
	return f, true
}

// gridRejoin puts a cell's wrapped lines back together the way the wrapper
// took them apart: no separator after a hyphen, which ansi.Wrap always treats
// as a breakpoint, and one space everywhere else. A cell that rejoins to its
// own text was wrapped at boundaries the text already had; one that does not
// was broken INSIDE a token.
func gridRejoin(frag []string) string {
	var sb strings.Builder
	for _, l := range frag {
		if l == "" {
			continue
		}
		if sb.Len() > 0 && !strings.HasSuffix(sb.String(), "-") {
			sb.WriteString(" ")
		}
		sb.WriteString(l)
	}
	return strings.Join(strings.Fields(sb.String()), " ")
}

// gridMidWordBreaks is how many CELLS of one rendered table were broken inside
// a token: the baseline any later column allocator has to beat.
//
// BOTH SIDES ARE WHITESPACE-COLLAPSED before they are compared, and that is
// not cosmetic. A cell's own projection can carry a double space, and so can a
// rendered line; collapsing both leaves exactly one difference visible, the
// space gridRejoin puts back where the wrapper cut a token in half.
//
// WHAT IT REPORTS. Over a real 54-file corpus, against lipgloss's UNREPAIRED
// output, this definition reports 45 tables broken at width 76 and 12 at 120,
// reproducing the originally recorded figures. With closeGridRight it is 45
// and 13: the repair spends a cell of the last column to put the box's
// right-hand border back, and where that column had no padding to spare it
// takes a character of the cell's text with it.
//
//	width   without the repair   with it
//	  160    1 table  (2 cells)   1 table  (2 cells)
//	  120   12 tables (39 cells) 13 tables (41 cells)
//	  100   21 tables (69 cells) 21 tables (69 cells)
//	   80   40 tables (147)      41 tables (148)
//	   76   45 tables (168)      45 tables (168)
//	   40   94 tables (799)      94 tables (799)
//
// A CELL CARRYING A CONTROL BYTE IS SKIPPED, counted separately and returned:
// a '\r' inside a cell means the terminal redraws the line over itself, and
// what reaches the screen is not the cell's text by any definition this can
// compare against. DEL (0x7f) IS ONE OF THEM -- the whole class is C0 and DEL,
// minus '\t', which lipgloss expands to spaces before it measures anything.
//
// AND WITH THE CONTROL-BYTE SUBSTITUTION IN PLACE, THE SKIP IS UNREACHABLE
// FROM A DOCUMENT, which is worth writing down beside it rather than deleting
// it: renderInlines makes every control
// byte its Control Picture at the leaf, and a Control Picture is an ordinary
// printing rune one cell wide, so the cell this counted is now a cell it
// measures. TestAControlByteReachesTheGridAsItsGlyph asserts exactly that. The
// predicate stays because it is the honest statement of what this helper cannot
// compare, and because a cell that reached DisplayPlain unfiltered would be a
// filter regression this measurement must not silently absorb.
func gridMidWordBreaks(rows []Block, groups [][]string) (breaks, control int, ok bool) {
	runes := borderRunesOf(tableGridBorder())
	cols := len(rows[0].Cells)
	for i, g := range groups {
		var content [][]string
		for _, l := range g {
			if borderOnlyLine(l, runes) {
				continue
			}
			f, good := gridColumnText(l, cols)
			if !good {
				return 0, 0, false
			}
			content = append(content, f)
		}
		for j := range rows[i].Cells {
			if j >= cols {
				continue
			}
			want := rows[i].Cells[j].DisplayPlain
			if strings.ContainsFunc(want, func(r rune) bool { return (r < 0x20 || r == 0x7f) && r != '\t' }) {
				control++
				continue
			}
			frag := make([]string, 0, len(content))
			for _, f := range content {
				frag = append(frag, f[j])
			}
			if gridRejoin(frag) != strings.Join(strings.Fields(want), " ") {
				breaks++
			}
		}
	}
	return breaks, control, true
}

// TestAControlByteReachesTheGridAsItsGlyph is the control-byte substitution
// driven at the arm a leaf filter dissolves a defect on: with the filter at
// the inline leaf a cell's
// projection carries `a␍b` -- an ordinary printing rune, one cell wide to
// ansi.StringWidth and to lipgloss's wrap alike -- so the cell is measured like
// any other and gridMidWordBreaks' control-byte skip has nothing to skip.
//
// THE BYTE SITS INSIDE A TOKEN, `a<byte>b`, AND THAT IS FORCED RATHER THAN
// STYLISTIC: a cell whose whole content is a bare '\r' projects to the EMPTY
// STRING -- goldmark reads a lone carriage return as a line terminator -- so a
// fixture of `| <byte> |` would drive seven of the eight bytes and silently
// drive nothing for the eighth.
//
// AND IT ASSERTS THE REVERSE-VIDEO DISTINCTION ON THE SAME CELL, because the
// two halves are one fact: the STYLED projection carries the glyph in reverse
// video and DisplayPlain
// carries the same glyph bare. A grid header cell is drawn from DisplayPlain
// and re-styled, which is why the plain half has to be checked as well.
func TestAControlByteReachesTheGridAsItsGlyph(t *testing.T) {
	st := darkStyles(t)
	for _, ctl := range gridControlBytes {
		t.Run(fmt.Sprintf("%q", ctl), func(t *testing.T) {
			src := fmt.Sprintf("| Step | Owner |\n|---|---|\n| a%sb | dana |\n", ctl)
			rows := gridBlocks(t, src, st, 2)
			cell := rows[1].Cells[0]
			want := "a" + string(controlPicture(ctl[0])) + "b"
			if cell.DisplayPlain != want {
				t.Fatalf("the cell projects to %q, want %q -- a control byte must reach the reader as its Control Picture and not as itself", cell.DisplayPlain, want)
			}
			if strings.ContainsAny(cell.DisplayPlain, ctl) {
				t.Fatalf("the byte %q is still in the plain projection %q -- DisplayPlain is what the grid draws a header cell from and what SearchBlocks indexes", ctl, cell.DisplayPlain)
			}
			// The styled projection is not merely the same glyph. A
			// document WRITING ABOUT ␍ must not read like one CONTAINING a CR.
			if !strings.Contains(cell.Display, "\x1b[7") {
				t.Fatalf("the styled cell %q carries no reverse-video run -- a substituted glyph that is styled like the text around it is indistinguishable from a literal one", cell.Display)
			}
			const budget = 40
			groups, ok := splitTableGrid(tableGridString(rows, budget, st, false), len(rows))
			if !ok {
				t.Fatalf("the fixture did not cut into its %d rows at budget %d", len(rows), budget)
			}
			breaks, control, readable := gridMidWordBreaks(rows, groups)
			if !readable {
				t.Fatalf("the fixture has a cell gridColumnText cannot read back")
			}
			if control != 0 {
				t.Fatalf("%d cells skipped for a control byte, want 0 -- the filter is meant to leave the measurement nothing to skip", control)
			}
			if breaks != 0 {
				t.Fatalf("%d cells counted as broken mid-word, want 0 -- the glyph is one cell to both width authorities, so the column allocator has no reason to misplace it", breaks)
			}
		})
	}
}

// gridLineCloser is the right-hand glyph that must close a rendered grid line,
// the one it actually closes on, and whether they agree. want is read off
// tableGridBorder() and PAIRED WITH THE GLYPH THE LINE OPENS ON: a top-left
// corner is closed by the top-right, a middle-left by the middle-right, a
// bottom-left by the bottom-right, and anything else -- a content row -- by the
// plain Right.
//
// IT PAIRS RATHER THAN LISTING, and that is the whole point of it. The three
// right-border assertions in this repository all used to accept ANY of the four
// right-hand runes on ANY line whatever it opened with, so closeGridRight's
// four-branch closer-selection switch was pinned by nothing: replacing the whole
// switch with `closer := b.Right` left the suite AND both gated corpus sweeps
// green while a repaired box rendered
//
//	┌───┬───│
//	│ a │ b │
//	├───┼───│
//	└───┴───│
//
// -- every corner and joint on the right-hand edge a plain vertical, inside the
// band of budgets where the repair is the only thing drawing that edge at all.
//
// THE GLYPHS ARE READ OFF THE BORDER AND THE PAIRING IS RESTATED, which is the
// honest description of what this can and cannot do. lipgloss.Border is thirteen
// unrelated string fields with no notion of which closes which, so the pairing
// cannot be derived from it and is written out here -- once, for three callers.
// A change to the PAIRING would have to be made here as well as in
// closeGridRight, and that is the residue this helper does not remove.
func gridLineCloser(line string) (want, got string, ok bool) {
	p := []rune(ansi.Strip(line))
	if len(p) == 0 {
		return "", "", false
	}
	b := tableGridBorder()
	want = b.Right
	switch string(p[0]) {
	case b.TopLeft:
		want = b.TopRight
	case b.MiddleLeft:
		want = b.MiddleRight
	case b.BottomLeft:
		want = b.BottomRight
	}
	got = string(p[len(p)-1])
	return want, got, got == want
}

// gridClosesOnTheRight answers whether every line of a rendered grid ends in the
// right-hand border glyph that PAIRS with the one it opens on -- whether the box
// a reader sees is shut on that side, with the corners it ought to have. It is
// asked separately from the width invariant because the two are independent: see
// TestFidelityGridFiguresAreWhatWasMeasured, where a grid that fits its budget
// exactly and is nevertheless open on the right is pinned.
func gridClosesOnTheRight(rendered string) bool {
	for _, l := range strings.Split(rendered, "\n") {
		if _, _, ok := gridLineCloser(l); !ok {
			return false
		}
	}
	return true
}

// gridOpenBands is the budgets in [lo, hi] at which one table's box does not
// close on the right, compressed to runs -- "2-4, 70-72" -- so a failure names
// the band and not a hundred integers.
func gridOpenBands(rows []Block, st *Styles, lo, hi int) string {
	var runs []string
	start := -1
	for w := lo; w <= hi+1; w++ {
		open := w <= hi && !gridClosesOnTheRight(tableGridString(rows, w-quoteBarCols(rows[0].QuoteDepth), st, false))
		switch {
		case open && start < 0:
			start = w
		case !open && start >= 0:
			if start == w-1 {
				runs = append(runs, fmt.Sprintf("%d", start))
			} else {
				runs = append(runs, fmt.Sprintf("%d-%d", start, w-1))
			}
			start = -1
		}
	}
	if len(runs) == 0 {
		return "none"
	}
	return strings.Join(runs, ", ")
}

// TestFidelityTablesAreTheShapesTheCorpusHolds is the inventory half of the
// fidelity claim: the committed fixture must hold every table SHAPE a real
// corpus holds, because a fixture can only fail on what its author thought to
// put in it, and the shapes a GRID gets wrong are not the shapes a
// record-per-row renderer got wrong.
//
// THE +1 PER TABLE IS THE FIRST CASE, stated as an invariant rather than as a
// count: every table's FIRST row is its header, no other row is, and the number
// of KindTableRow blocks is the number of body rows plus the number of tables.
//
// THE REST ARE SHAPES, each with the share of the corpus it stands in for. They
// are asserted as PRESENT and not as counts: the fixture is committed and the
// corpus is somebody's working directory, so "the fixture still holds one of
// these" is the claim that can be kept and "the corpus still holds 33 of them"
// is not.
func TestFidelityTablesAreTheShapesTheCorpusHolds(t *testing.T) {
	st := darkStyles(t)
	tables := fidelityTables(t, st)
	if len(tables) < 9 {
		t.Fatalf("the fixture holds %d tables, want at least the 9 the shapes below need", len(tables))
	}

	body, rows := 0, 0
	for i, table := range tables {
		for j, b := range table {
			rows++
			if (j == 0) != b.Table.Header {
				t.Fatalf("table %d row %d: Table.Header = %v -- the header is row 0 and nothing else is", i, j, b.Table.Header)
			}
			if j > 0 {
				body++
			}
		}
	}
	if rows != body+len(tables) {
		t.Fatalf("%d table-row blocks against %d body rows and %d tables -- the +1 per table is not what the parse emits", rows, body, len(tables))
	}
	t.Logf("%d tables, %d body rows, %d table-row blocks -- exactly one header apiece", len(tables), body, rows)

	// Each case names the corpus population it stands in for. The predicate is
	// over the whole fixture because which table carries a shape is not the
	// claim; that the fixture carries it is.
	for _, c := range []struct {
		name string
		has  func([]Block) bool
	}{
		{"a table of 7 columns, the corpus's widest -- 1 of its 115", func(rs []Block) bool {
			return len(rs[0].Cells) == 7
		}},
		{"a table of 5 columns -- 2 of 115, a shape outside the usual 2-4 column range", func(rs []Block) bool {
			return len(rs[0].Cells) == 5
		}},
		{"a table inside a quotation -- 3 of 115, 15 rows", func(rs []Block) bool {
			return rs[0].QuoteDepth > 0
		}},
		{"a table with no body rows at all, which is every table between two keystrokes on its delimiter line", func(rs []Block) bool {
			return len(rs) == 1
		}},
		{"an EMPTY header cell -- 33 of the corpus's 325 header cells, one apiece in 33 tables", func(rs []Block) bool {
			for _, c := range rs[0].Cells {
				if strings.TrimSpace(c.DisplayPlain) == "" {
					return true
				}
			}
			return false
		}},
		{"a body row with MORE cells than the header, whose discarded tail is appended raw -- 2 of the corpus's 826 body rows", func(rs []Block) bool {
			return gridTailedRows(rs) == 1
		}},
		{"a cell of 2,000 cells or more -- 2 of 115 tables hold one, and the corpus's widest cell is 2,590", func(rs []Block) bool {
			for _, b := range rs {
				for _, c := range b.Cells {
					if ansi.StringWidth(c.DisplayPlain) >= 2000 {
						return true
					}
				}
			}
			return false
		}},
		{"a token no column can hold, which is the only thing that makes a grid break a WORD", func(rs []Block) bool {
			return gridHardFloor(rs) > 72
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, table := range tables {
				if c.has(table) {
					return
				}
			}
			t.Fatal("no table in ui/testdata/fidelity.md has this shape, so nothing in this repository renders it")
		})
	}
}

// TestFidelityGridFiguresAreWhatWasMeasured pins the grid figures "so a
// regression is a diff and not a judgment", on the one document that can carry
// a literal: the committed fixture.
//
// WHY NOT ON THE CORPUS. ~/plans is one machine's working directory and moves
// daily. This package's standing rule is that a corpus figure is COMPUTED and
// never compared to a literal (corpus_projection_dogfood_test.go says why at
// length), so the division is the same one the fixtures have always had -- the
// corpus sweep computes and logs, and the FIXTURE is where an exact value can
// be asserted.
//
// THE HARD FLOOR is the narrowest budget at which no allocator could avoid
// breaking a word: lipgloss's resizer is what ships, and these are the numbers
// any later attempt has to beat. Corpus distribution: median 54, p90 95, max
// 160.
//
// THE MID-WORD-BREAK COUNTS are the baseline that ruling turns on, and the
// corpus figure has TWO readings that must not be confused -- against
// lipgloss's unrepaired output and against this tree's repaired one. See
// gridMidWordBreaks for the table and the derivation.
//
// 40 IS HERE BECAUSE 160, 120 AND 76 ARE NOT ENOUGH: at those three widths this
// fixture breaks two cells in total and eight of its nine tables break none. At
// 40 it breaks 27 across three tables, which is what makes the counts below a
// measurement rather than a row of zeroes. AND 72 IS THE app FRAME'S OWN BUDGET
// at a terminal of 80, so the two files pin the same render.
func TestFidelityGridFiguresAreWhatWasMeasured(t *testing.T) {
	st := darkStyles(t)
	tables := fidelityTables(t, st)

	// THE HARD FLOOR, one literal per table, in document order.
	wantFloors := []int{19, 51, 19, 19, 27, 56, 20, 76, 22}
	if len(tables) != len(wantFloors) {
		t.Fatalf("the fixture holds %d tables and this test carries %d hard floors", len(tables), len(wantFloors))
	}
	for i, table := range tables {
		if got := gridHardFloor(table); got != wantFloors[i] {
			t.Errorf("table %d (%d columns): hard floor %d, want %d", i, len(table[0].Cells), got, wantFloors[i])
		}
	}

	// THE SPLIT AND THE BREAKS, at the three widths originally measured plus
	// the two this fixture needs. Every table must cut into exactly its own
	// row count -- 110/110 as first measured, 115/115 as re-derived -- and
	// the mid-word breaks are counted in CELLS, which is finer than the
	// original per-table figure and is what a later allocator would actually
	// be reducing.
	for _, c := range []struct {
		width, breaks int
	}{
		{160, 0},
		{120, 0},
		{76, 2},
		{72, 2},
		{40, 27},
		// 92 IS THE PRICE OF CLOSING THE BOX, on a committed document. It
		// is inside the long-token table's open band (91-93), and it is
		// the one width in this fixture where the closer has no padding to
		// spend and takes a character of the cell's own text instead: the
		// fixture breaks 0 cells here before closeGridRight and exactly 1
		// after. It is a case rather than a sentence so that a change to
		// the trade shows up as a diff.
		{92, 1},
	} {
		t.Run(fmt.Sprintf("width %d", c.width), func(t *testing.T) {
			broke := 0
			for i, table := range tables {
				budget := c.width - quoteBarCols(table[0].QuoteDepth)
				rendered := tableGridString(table, budget, st, false)
				for j, line := range strings.Split(rendered, "\n") {
					if w := ansi.StringWidth(ansi.Strip(line)); w > clampTableWidth(budget) {
						t.Fatalf("table %d line %d is %d cells against a budget of %d", i, j, w, clampTableWidth(budget))
					}
				}
				groups, ok := splitTableGrid(rendered, len(table))
				if !ok {
					t.Fatalf("table %d did not cut into its %d rows", i, len(table))
				}
				n, control, readable := gridMidWordBreaks(table, groups)
				if !readable {
					t.Fatalf("table %d has a cell this cannot read back -- see gridColumnText", i)
				}
				if control != 0 {
					t.Fatalf("table %d has %d cell(s) carrying a control byte; the fixture is not supposed to", i, control)
				}
				broke += n
			}
			if broke != c.breaks {
				t.Fatalf("%d cells broken mid-word, want %d", broke, c.breaks)
			}
		})
	}

	// THE BOX CLOSES ON THE RIGHT NOW, and this is the band that used to be
	// open, kept as a literal because the band is the interesting half of the
	// defect and the reason it went unnoticed for so long.
	//
	// WHAT IT WAS. lipgloss/v2/table renders the whole table and then truncates
	// it to the width it was given. In a band of budgets immediately BELOW a
	// table's natural width the resizer allocates columns summing to one cell
	// more than the budget, and what that truncation cut was the right-hand
	// border: every line came back exactly the budget wide, the split still
	// succeeded, and the box had no right edge at all. Nothing saw it.
	//
	// THE BANDS AS THEY WERE, ONE LINE PER TABLE in the order wantOpen below is
	// indexed, which is document order. Terminal widths, Doc twin:
	//
	//	0  2 cols            2-4    27-29
	//	1  5 cols            2-10   55-60
	//	2  2 cols, quoted    2-6    31-33
	//	3  2 cols, no body   2-4    41-43
	//	4  3 cols            2-6    64-67
	//	5  7 cols            2-14   48-55
	//	6  2 cols, the tail  2-4    70-72
	//	7  2 cols, the token 2-4    91-93
	//	8  2 cols, the essay 2-4    --
	//
	// Three things to read off that. The upper band sits IMMEDIATELY BELOW the
	// table's natural width and widens with the column count, which is why 160
	// and 80 were both clean. Table 6's is 70-72, and 72 is the budget the app
	// frame hands a table at a terminal of 80, so this was on screen in
	// app/fidelity_test.go's own drive. And table 8 has NO upper band: its cell
	// is 2,039 characters, so only the narrow-budget band is reachable.
	//
	// WHAT IS LEFT is a deliberate yield rather than a remnant: closeGridRight
	// declines to close a line when doing so would spend the last non-border
	// cell it has, because splitTableGrid would then cut nothing for the whole
	// table. That happens at exactly one budget, 2. A quoted table reaches
	// budget 2 at terminal width 4, which is why table 2's band ends at 4.
	//
	// Table 2 is also the only table here whose budget reaches
	// clampTableWidth's FLOOR inside this sweep: at terminal widths 2 and 3 the
	// render is `┐ │ ┤ │ ┘`, one rune per line, which the PAIRED predicate
	// (gridLineCloser) correctly calls open where a "any right-hand rune"
	// predicate called it closed. Unreachable in the app either way --
	// listMinWidth is 31.
	wantOpen := []string{"2", "2", "2-4", "2", "2", "2", "2", "2", "2"}
	for i, table := range tables {
		if got := gridOpenBands(table, st, 2, 200); got != wantOpen[i] {
			t.Errorf("table %d (%d columns): the box is open on the right at budgets %s, want %s", i, len(table[0].Cells), got, wantOpen[i])
		}
	}
}

// TestTableGridSplitsIntoOneGroupPerRow pins the cut, which is the whole of
// what keeps row-level anchoring alive under a grid: a rendered table is one
// string and a reviewer comments on a ROW, so every line of that string has to
// be attributed to exactly one of them.
//
// THE ATTRIBUTION: a group is the row's own content lines plus the rule that
// CLOSES it, so the box's top border opens the first row's group and its bottom
// border closes the last row's. The partition is asserted AS a partition --
// the groups concatenated are the rendered lines, in order, with nothing lost
// and nothing counted twice.
//
// THE SECOND DRIVE IS A TABLE OF NOTHING BUT BORDER RUNES, at the narrowest
// widths it can be drawn at: a row whose every cell is `─` is a content line
// made of the same glyphs a rule is. What keeps it out of the rule class is
// tableGridPadCols, the space every cell carries.
//
// AND THE LEFT EDGE IS NOT WHAT IS BEING READ. strings.HasPrefix(line, "├") is
// the shortcut this rejects: it happens to work while BorderLeft is on and
// silently reads every line as content when it is not, which would make a whole
// table one group. The last case shows the classifier answering correctly for a
// rule that has no left border at all.
func TestTableGridSplitsIntoOneGroupPerRow(t *testing.T) {
	const src = `| glyph | name |
|---|---|
| │ | the vertical this view already uses three of |
| ┼ | a crossing |
`
	st := darkStyles(t)
	rows := gridBlocks(t, src, st, 3)
	// Narrow enough that the second row's name wraps, so at least one group
	// holds more than one content line and "one group per row" is not the
	// same claim as "one line per row".
	rendered := tableGridString(rows, 30, st, false)
	lines := strings.Split(rendered, "\n")
	groups, ok := splitTableGrid(rendered, len(rows))
	if !ok {
		t.Fatalf("the grid did not split:\n%s", ansi.Strip(rendered))
	}
	var flat []string
	multi := false
	borderRunes := borderRunesOf(tableGridBorder())
	for i, g := range groups {
		flat = append(flat, g...)
		if !borderOnlyLine(g[len(g)-1], borderRunes) {
			t.Fatalf("group %d does not end in a rule: %q", i, ansi.Strip(g[len(g)-1]))
		}
		content := 0
		for _, l := range g[:len(g)-1] {
			if !borderOnlyLine(l, borderRunes) {
				content++
			}
		}
		if content > 1 {
			multi = true
		}
		if content == 0 {
			t.Fatalf("group %d holds no content line at all: %q", i, ansi.Strip(strings.Join(g, "\n")))
		}
	}
	if !reflect.DeepEqual(flat, lines) {
		t.Fatalf("the groups are not a partition of the render: %d lines in, %d out", len(lines), len(flat))
	}
	if !multi {
		t.Fatal("no row wrapped, so nothing here distinguishes one group per ROW from one group per LINE")
	}
	// The top border opens the first group and the bottom border closes the
	// last, which is the attribution rule stated as the two ends of the box.
	if first := ansi.Strip(groups[0][0]); !strings.HasPrefix(first, "┌") {
		t.Fatalf("the first group does not open with the top border: %q", first)
	}
	last := groups[len(groups)-1]
	if bottom := ansi.Strip(last[len(last)-1]); !strings.HasPrefix(bottom, "└") {
		t.Fatalf("the last group does not close with the bottom border: %q", bottom)
	}
	// Every row's own words are in its own group and in no other.
	for i, want := range []string{"glyph", "vertical", "crossing"} {
		for j, g := range groups {
			has := strings.Contains(ansi.Strip(strings.Join(g, "\n")), want)
			if has != (i == j) {
				t.Fatalf("%q is %sin group %d, want it only in group %d", want, map[bool]string{true: "", false: "not "}[has], j, i)
			}
		}
	}
	// A rule drawn with no left border is still a rule. This is the assertion
	// the HasPrefix shortcut fails.
	noLeft := st.Dim.Render("───┴───")
	if strings.HasPrefix(ansi.Strip(noLeft), "├") {
		t.Fatal("the no-left-border case has stopped being one")
	}
	if !borderOnlyLine(noLeft, borderRunes) {
		t.Fatalf("a rule with no left border is not classified as one: %q", ansi.Strip(noLeft))
	}
	// And a WHOLE TABLE drawn with BorderLeft off still splits, which is the
	// claim the classifier is chosen for and the one a prefix test cannot make:
	// with the left edge gone every line begins with '─' or a space, so any
	// prefix on ├/┌/└ matches nothing and the split fails. Measured over the
	// corpus at splitTableGrid; driven here so the difference is a test and not
	// only a comment.
	cell, text := tableGridStyles(st, false)
	sideless := table.New().
		Border(tableGridBorder()).
		BorderTop(true).BorderBottom(true).BorderLeft(false).BorderRight(true).
		BorderRow(true).BorderColumn(true).
		Wrap(true).
		Width(30).
		BorderStyle(borderStyleOf(st, false)).
		StyleFunc(func(int, int) lipgloss.Style { return cell })
	for _, b := range rows {
		sideless.Row(tableGridRow(b, text, false)...)
	}
	rendered = sideless.Render()
	if strings.HasPrefix(ansi.Strip(rendered), "┌") {
		t.Fatal("the sideless render still has a left border, so it measures nothing")
	}
	if g, ok := splitTableGrid(rendered, len(rows)); !ok || len(g) != len(rows) {
		t.Fatalf("a table drawn with no left border did not split:\n%s", ansi.Strip(rendered))
	}
	// A table whose every cell is a border rune, at the narrowest widths it
	// can be drawn at. Nothing but the cell padding separates its content
	// lines from its rules.
	for _, allBorders := range []string{"| ─ |\n|---|\n| ─ |\n", "| ─ | ─ |\n|---|---|\n| ─ | ─ |\n", "|  |  |\n|---|---|\n|  |  |\n"} {
		pathological := gridBlocks(t, allBorders, st, 2)
		for w := 3; w <= 8; w++ {
			grid := tableGridString(pathological, w, st, false)
			g, ok := splitTableGrid(grid, len(pathological))
			if !ok || len(g) != len(pathological) {
				t.Fatalf("a table of border runes at width %d did not split:\n%s", w, ansi.Strip(grid))
			}
		}
	}
}

// TestSplitTableGridAnswersFalseRatherThanGuess is the split's THREE GUARDS,
// each isolated, and it exists because a mutation round found all three inert
// against the whole repository:
//
//	borderOnlyLine drops its "" guard, so an empty line is a rule    0 tests
//	splitTableGrid drops `len(cur) > 0`, the unclosed-tail guard     0 tests
//	splitTableGrid drops `len(groups) != want`, the count guard      0 tests
//
// WHY THEY WERE INERT AND WHY THAT IS NOT AN EXCUSE. All three fire only on a
// render the classification has already lost the thread on, and lipgloss cannot
// produce one -- every line of a real grid carries at least the outer border,
// and a real grid ends in one. So the tests that drive REAL renders could not
// reach them however many widths and shapes they drove.
//
// SO THE RENDERS BELOW ARE HAND-BUILT, and that is the point rather than a
// compromise. What is pinned is the answer this function owes a CALLER when the
// classification is wrong -- nothing, so the caller can draw the rows some
// other way -- and the shape of a future bug is exactly a `want` that stops
// matching the render. paintOneTable indexes the returned slice by row, so a
// wrong count is a row drawn from another row's lines, or a panic.
//
// THE FIRST CASE IS THE CONTROL. Without it every line below could be satisfied
// by a function that answered false unconditionally.
func TestSplitTableGridAnswersFalseRatherThanGuess(t *testing.T) {
	for _, c := range []struct {
		name     string
		rendered string
		want     int
		ok       bool
		groups   int
	}{
		{"a well-formed two-row grid, which is the control", "┌─┐\n│a│\n├─┤\n│b│\n└─┘", 2, true, 2},
		{
			// THE UNCLOSED-TAIL GUARD, isolated: the group COUNT is right and
			// there are lines left over, so a function that only checked the
			// count would answer with one group and silently drop row b.
			"a row the box never closed, with the group count still right",
			"┌─┐\n│a│\n├─┤\n│b│", 1, false, 0,
		},
		{
			// THE COUNT GUARD, isolated the other way: nothing is left over
			// and there is one group too many, so a function that only
			// checked the tail would hand back two groups to a caller that
			// asked for one.
			"one group more than the caller asked for, and nothing left over",
			"┌─┐\n│a│\n├─┤\n│b│\n└─┘", 1, false, 0,
		},
		{"one group fewer than the caller asked for", "┌─┐\n│a│\n└─┘", 2, false, 0},
		{
			// borderOnlyLine's "" GUARD, isolated. An empty line inside the
			// box is NOT a rule; treating it as one closes a group early,
			// which leaves the real bottom border stranded with no content
			// above it and turns a grid that splits into one that does not.
			// So this case asserts TRUE, and the mutation turns it false.
			"an empty line inside the box is not a rule",
			"┌─┐\n│a│\n\n└─┘", 1, true, 1,
		},
		{"a caller that asked for no rows at all", "┌─┐\n│a│\n└─┘", 0, false, 0},
		{"nothing rendered", "", 1, false, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			groups, ok := splitTableGrid(c.rendered, c.want)
			if ok != c.ok || len(groups) != c.groups {
				t.Fatalf("splitTableGrid(%q, %d) = %d group(s), %v; want %d, %v", c.rendered, c.want, len(groups), ok, c.groups, c.ok)
			}
			if !ok {
				return
			}
			// A partition, checked here as it is on real renders: the groups
			// concatenated are the lines, in order, nothing lost and nothing
			// counted twice.
			var flat []string
			for _, g := range groups {
				flat = append(flat, g...)
			}
			if !slices.Equal(flat, strings.Split(c.rendered, "\n")) {
				t.Fatalf("the groups are not a partition of the render: %q", flat)
			}
		})
	}
}

// TestAClosedGridEdgeWearsItsTwinsBorderStyle is the half of closeGridRight
// that geometry cannot see, and it is the half a prototype of this fix got
// wrong.
//
// THE CLOSER IS RENDERED, NOT APPENDED. A bare '┐' put on the end of a line
// lands UNSTYLED -- and every other glyph of that box is drawn through
// borderStyleOf, which carries the twin's own background as well as the Dim
// foreground. On the Doc twin an unstyled rune shows the terminal's default
// background where the row's should be; on the CARD twin it is precisely the
// failure paintOneTable renders two twins to avoid.
//
// IT COMPARES THE CLOSER'S STYLE TO THE OPENER'S ON THE SAME LINE, rather than
// to a literal built here. The opener is lipgloss's own border glyph, drawn
// through the style the table was configured with, so "the same as that" is
// exactly the claim -- and it cannot drift when the theme does.
//
// THE BUDGET IS INSIDE THE BAND, which is the whole reason this test can see
// anything: closeGridRight only touches a line lipgloss left open.
func TestAClosedGridEdgeWearsItsTwinsBorderStyle(t *testing.T) {
	th, err := theme.Lookup(theme.DefaultName)
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	tables := fidelityTables(t, st)
	const budget = 92

	for _, c := range []struct {
		name   string
		onCard bool
		bg     string
	}{
		{"the Doc twin", false, th.Doc},
		{"the Card twin", true, th.Card},
	} {
		t.Run(c.name, func(t *testing.T) {
			lines := 0
			for i, rows := range tables {
				rendered := tableGridString(rows, budget-quoteBarCols(rows[0].QuoteDepth), st, c.onCard)
				if !gridClosesOnTheRight(rendered) {
					t.Fatalf("table %d is still open on the right at budget %d, so this twin has no repaired line to examine", i, budget)
				}
				for _, line := range strings.Split(rendered, "\n") {
					plain := []rune(ansi.Strip(line))
					// EVERY line, not only the repaired ones. Which lines
					// closeGridRight touched is not knowable from the outside,
					// and it should not have to be: what the box promises is
					// that its opening glyph and its closing glyph are drawn
					// the same way, whoever drew them.
					opener := styleBefore(t, line, string(plain[0]))
					closerStyle := styleBefore(t, line, string(plain[len(plain)-1]))
					if opener != closerStyle {
						t.Fatalf("table %d, %q: the line opens in %q and closes in %q -- the closer is not wearing the border's style", i, string(plain), opener, closerStyle)
					}
					if want := "48;2;" + sgrRGB(t, c.bg); !strings.Contains(closerStyle, want) {
						t.Fatalf("table %d, %q: the closer is styled %q, want this twin's own background %q", i, string(plain), closerStyle, want)
					}
					lines++
				}
			}
			if lines == 0 {
				t.Fatal("no line was examined, so nothing above compared anything")
			}
			t.Logf("%d grid lines at budget %d, every closer wearing the border's own style on this twin", lines, budget)
		})
	}
}

// TestTableGridTwinsAreOneGeometryAndTwoZones: the Doc twin and the Card twin
// are the SAME TABLE in two colours, so a row can be taken from whichever one
// matches its zone and the columns still line up.
//
// STRIPPED EQUALITY IS THE CLAIM, and it is what makes the splice legitimate --
// same line count, same characters, same column positions, byte for byte once
// the SGR is off. It holds because the twins' cell styles differ in BACKGROUND
// alone; see paintOneTable.
//
// AND THE SGR MUST ACTUALLY DIFFER, which is the other half and is not implied
// by the first. A twin pair that were identical strings would satisfy stripped
// equality perfectly and buy nothing at all.
func TestTableGridTwinsAreOneGeometryAndTwoZones(t *testing.T) {
	th, err := theme.Lookup(theme.DefaultName)
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	rows := gridBlocks(t, gridFixture, st, 3)
	for _, width := range []int{160, 76, 40} {
		doc := tableGridString(rows, width, st, false)
		card := tableGridString(rows, width, st, true)
		if ansi.Strip(doc) != ansi.Strip(card) {
			t.Fatalf("width %d: the twins are not one geometry:\n%s\n---\n%s", width, ansi.Strip(doc), ansi.Strip(card))
		}
		if doc == card {
			t.Fatalf("width %d: the twins are byte-identical, so neither carries a zone of its own", width)
		}
	}
	doc := tableGridString(rows, 76, st, false)
	card := tableGridString(rows, 76, st, true)
	// The internal column divider, not merely the outer frame: it is the glyph
	// that would put a Doc-coloured stripe THROUGH a Card-coloured row.
	for _, tc := range []struct {
		name     string
		rendered string
		bg       string
	}{
		{"doc twin", doc, th.Doc},
		{"card twin", card, th.Card},
	} {
		divider := styleBefore(t, tc.rendered, "┬")
		if want := "48;2;" + sgrRGB(t, tc.bg); !strings.Contains(divider, want) {
			t.Fatalf("%s: the column divider is styled %q, which does not carry %q", tc.name, divider, want)
		}
		if want := "38;2;" + sgrRGB(t, th.Dim); !strings.Contains(divider, want) {
			t.Fatalf("%s: the column divider is styled %q, which is not the Dim rule colour %q", tc.name, divider, want)
		}
	}
}

// TestPaintTablesTakesEachRowFromItsOwnZone is the splice: a commented row is
// painted on Card and its neighbours on Doc, in one table, with the columns
// still lining up.
//
// THE RULE BELOW A ROW BELONGS TO THAT ROW and therefore wears that row's zone,
// which is the one decision the twin render leaves open and is settled by the
// attribution rather than separately: renderDocPainted paints every Line onto
// its own block's background, so a rule taken from the other twin would be a
// Card-coloured glyph on a Doc-coloured row -- the failure the twins exist to
// prevent, one row down.
func TestPaintTablesTakesEachRowFromItsOwnZone(t *testing.T) {
	th, err := theme.Lookup(theme.DefaultName)
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	rows := gridBlocks(t, gridFixture, st, 3)
	const commented = 1
	painted := paintTables(rows, 76, st, func(i int) bool { return i == commented }, nil)
	if len(painted) != len(rows) {
		t.Fatalf("paintTables returned %d groups, want %d", len(painted), len(rows))
	}
	for i := range rows {
		want := th.Doc
		zone := "Doc"
		if i == commented {
			want, zone = th.Card, "Card"
		}
		group := painted[i]
		joined := strings.Join(group, "\n")
		for _, glyph := range []string{"│", "─"} {
			style := styleBefore(t, joined, glyph)
			if bg := "48;2;" + sgrRGB(t, want); !strings.Contains(style, bg) {
				t.Fatalf("block %d should be painted on %s: its %q is styled %q, which does not carry %q", i, zone, glyph, style, bg)
			}
		}
	}
	// The commented row's group still ends in the rule that closes it, and
	// that rule is on Card with the row above it on Doc -- the boundary the
	// zone question is actually about.
	closing := painted[commented][len(painted[commented])-1]
	if !borderOnlyLine(closing, borderRunesOf(tableGridBorder())) {
		t.Fatalf("the commented row's group does not end in a rule: %q", ansi.Strip(closing))
	}
	if bg := "48;2;" + sgrRGB(t, th.Card); !strings.Contains(closing, bg) {
		t.Fatalf("the rule closing the commented row is not on Card: %q", closing)
	}
	above := painted[commented-1][len(painted[commented-1])-1]
	if bg := "48;2;" + sgrRGB(t, th.Doc); !strings.Contains(above, bg) {
		t.Fatalf("the rule closing the row above is not on Doc: %q", above)
	}
	// And the splice is only legitimate if the two zones agree on geometry.
	for i := range rows {
		if got, want := ansi.StringWidth(ansi.Strip(painted[i][0])), 76; got != want {
			t.Fatalf("block %d's first line is %d cells, want %d -- the twins have come apart", i, got, want)
		}
	}
}

// TestTableGridHeaderIsARowAndKeepsItsWords: the header goes in as DATA ROW 0,
// where Wrap applies to it like any other row, and never through Headers(),
// which table.go truncates to one line whatever Wrap says.
//
// BOTH PATHS ARE RENDERED HERE, which is what makes this a measurement rather
// than an assertion about a call nobody makes. A test that only asserted "no
// ellipsis" would pass on a fixture whose header happened to fit, and would go
// on passing if the painter switched back.
func TestTableGridHeaderIsARowAndKeepsItsWords(t *testing.T) {
	const src = `| the step this row is about, spelled out at length | note |
|---|---|
| deploy | after the freeze |
`
	st := darkStyles(t)
	rows := gridBlocks(t, src, st, 2)
	const width = 40
	const tail = "at length"
	grid := ansi.Strip(tableGridString(rows, width, st, false))
	if !strings.Contains(grid, tail) {
		t.Fatalf("the header's last words are missing from the grid:\n%s", grid)
	}
	if strings.Contains(grid, "…") {
		t.Fatalf("the grid truncated something:\n%s", grid)
	}
	// The same table with the header handed to Headers() instead: the words
	// this painter keeps are the words that path drops.
	cell, text := tableGridStyles(st, false)
	viaHeaders := table.New().
		Border(tableGridBorder()).
		BorderTop(true).BorderBottom(true).BorderLeft(true).BorderRight(true).
		BorderRow(true).BorderColumn(true).
		Wrap(true).
		Width(width).
		BorderStyle(borderStyleOf(st, false)).
		StyleFunc(func(int, int) lipgloss.Style { return cell }).
		Headers(tableGridRow(rows[0], text, false)...)
	for _, b := range rows[1:] {
		viaHeaders.Row(tableGridRow(b, text, false)...)
	}
	if got := ansi.Strip(viaHeaders.Render()); strings.Contains(got, tail) {
		t.Fatalf("Headers() no longer truncates, so this test no longer measures anything:\n%s", got)
	}
	// And the header is STYLED as one: bold, over its whole text rather than
	// over its first inline run.
	if style := styleBefore(t, tableGridString(rows, width, st, false), "the step"); !strings.HasPrefix(style, "\x1b[1;") {
		t.Fatalf("the header row is not bold: %q", style)
	}
}

// TestTableGridKeepsAnEmptyCellsColumn is why the painter reads Block.Cells and
// never the joined Display: the join DROPS an empty cell, so a row whose middle
// cell is empty would come back as two cells and put its third cell's text in
// the second column. 31 of a real corpus's 2,543 cells are empty and not the
// last of their row, so this is a live shape and not a constructed one.
func TestTableGridKeepsAnEmptyCellsColumn(t *testing.T) {
	const src = `| first | second | third |
|---|---|---|
| one |  | three |
`
	st := darkStyles(t)
	rows := gridBlocks(t, src, st, 2)
	// The fixture must still be the fixture: the join has to disagree with
	// the cells, or nothing here distinguishes the two sources.
	if got := len(strings.Split(rows[1].Display, "\n")); got != 2 {
		t.Fatalf("the joined projection holds %d cells, want 2 -- the join has stopped dropping the empty one", got)
	}
	if got := len(rows[1].Cells); got != 3 {
		t.Fatalf("the row carries %d cells, want 3", got)
	}
	groups, ok := splitTableGrid(tableGridString(rows, 40, st, false), len(rows))
	if !ok {
		t.Fatal("the grid did not split")
	}
	columns := func(group []string) []string {
		for _, l := range group {
			plain := ansi.Strip(l)
			if strings.Count(plain, "│") == 4 {
				return strings.Split(plain, "│")[1:4]
			}
		}
		t.Fatalf("no three-column content line in %q", ansi.Strip(strings.Join(group, "\n")))
		return nil
	}
	head, body := columns(groups[0]), columns(groups[1])
	for i, want := range []string{"first", "second", "third"} {
		if !strings.Contains(head[i], want) {
			t.Fatalf("header column %d is %q, want %q in it", i, head[i], want)
		}
	}
	if strings.TrimSpace(body[1]) != "" {
		t.Fatalf("the empty cell's column holds %q, so a cell has slid into it", body[1])
	}
	if !strings.Contains(body[2], "three") {
		t.Fatalf("the third cell is not in the third column: %q", body[2])
	}
}

// TestPaintTablesKeepsTablesApartAndPaysForTheirQuoteBars drives the pre-pass's
// two remaining jobs on one document.
//
// TABLES ARE TOLD APART BY Table.ID AND NOT BY ADJACENCY, which is TableRef's
// own ruling and the reason the field exists: two tables separated by a blank
// line produce block-adjacent KindTableRow blocks with nothing between them, so
// a painter grouping by contiguity would draw them as one grid.
//
// AND A QUOTED TABLE PAYS FOR ITS BARS OUT OF THE BUDGET, exactly as
// withQuoteBar makes every other kind pay: the bar is drawn in front of each
// row by the caller, so a grid drawn to the full width would be wider than the
// row it is painted into. Two cells per level, which is quoteBarCols.
func TestPaintTablesKeepsTablesApartAndPaysForTheirQuoteBars(t *testing.T) {
	const src = `| a | b |
|---|---|
| one | two |

| c | d |
|---|---|
| three | four |

> | e | f |
> |---|---|
> | five | six |
`
	st := darkStyles(t)
	blocks := ParseBlocks([]byte(src), st)
	if len(blocks) != 6 {
		t.Fatalf("fixture parses to %d blocks, want 6: %+v", len(blocks), blocks)
	}
	for i, want := range []int{1, 1, 2, 2, 3, 3} {
		if blocks[i].Table.ID != want {
			t.Fatalf("block %d belongs to table %d, want %d", i, blocks[i].Table.ID, want)
		}
	}
	if blocks[4].QuoteDepth != 1 || blocks[0].QuoteDepth != 0 {
		t.Fatalf("the fixture is no longer one quoted table and two unquoted: %d / %d", blocks[0].QuoteDepth, blocks[4].QuoteDepth)
	}
	const width = 40
	painted := paintTables(blocks, width, st, nil, nil)
	if len(painted) != len(blocks) {
		t.Fatalf("paintTables returned %d groups, want %d", len(painted), len(blocks))
	}
	widthOf := func(i int) int { return ansi.StringWidth(ansi.Strip(painted[i][0])) }
	for _, i := range []int{0, 1, 2, 3} {
		if got := widthOf(i); got != width {
			t.Fatalf("block %d is %d cells wide, want %d", i, got, width)
		}
	}
	for _, i := range []int{4, 5} {
		if got, want := widthOf(i), width-quoteBarCols(1); got != want {
			t.Fatalf("quoted block %d is %d cells wide, want %d -- the quote bar is not coming out of the budget", i, got, want)
		}
	}
	// Told apart means each grid closes: the first table's last row ends in a
	// bottom border rather than running into the second table's header.
	for _, i := range []int{1, 3, 5} {
		g := painted[i]
		if bottom := ansi.Strip(g[len(g)-1]); !strings.HasPrefix(bottom, "└") {
			t.Fatalf("table row block %d does not close its own grid: %q", i, bottom)
		}
	}
	// And each grid opens on its own header -- once each here, because this
	// document carries no comments. A comment card reopens the box below
	// itself, which is gridRowGroup's business and
	// TestACommentCardClosesTheGridAndReopensIt's subject.
	for _, i := range []int{0, 2, 4} {
		if top := ansi.Strip(painted[i][0]); !strings.HasPrefix(top, "┌") {
			t.Fatalf("header block %d does not open a grid: %q", i, top)
		}
	}
}

// gridDocFixture is a three-row table with a paragraph on either side of it,
// which is the shape every claim about the DOCUMENT (rather than about the
// painter) needs: the table has an inside and two edges, and the paragraphs
// are what say the edges are edges.
const gridDocFixture = "before.\n\n| Step | Owner |\n|---|---|\n| Deploy the gateway | dana |\n| Roll back | sam |\n\nafter.\n"

// linesOfBlock is every Line tagged with blockIdx, in order, with the rail and
// the gutter cut off each -- the block's own content column and nothing else,
// which is what a claim about the BOX is about. See rowBody, which does the
// cutting in cells rather than bytes.
func linesOfBlock(lines []Line, blockIdx, width int) []string {
	var out []string
	for _, l := range lines {
		if l.BlockIdx == blockIdx {
			out = append(out, rowBody(l.Text, width))
		}
	}
	return out
}

// gutterOf is the rail-and-gutter columns of one rendered row: what
// cursorGlyph and the annotation mark wrote, with the block's own content cut
// away. In CELLS, because the glyphs on either side of that boundary run three
// bytes to the cell.
func gutterOf(row string, width int) string {
	return ansi.Truncate(ansi.TruncateLeft(ansi.Strip(row), RailWidth(width), ""), gutterWidth, "")
}

// TestDocumentDrawsATableAsOneGrid states the grid where a reader meets it:
// the whole table is ONE BOX, cut back into per-row Lines so that row-level
// anchoring survives. A commented table is two boxes with the comment between
// them -- TestACommentCardClosesTheGridAndReopensIt's subject, and a shape this
// fixture deliberately does not reach.
//
// THE ATTRIBUTION RULE IS THE ASSERTION AND IT IS NOT SYMMETRIC. The header and
// the top border go to the FIRST row; an interior separator goes to the row it
// CLOSES, which is the row ABOVE it; the bottom border goes to the LAST row.
// The alternative renders identically and is a different mapping underneath, so
// nothing on screen tells the two apart and only an assertion on BlockIdx can.
// It matters because every Line is painted on ITS OWN BLOCK'S background and a
// commented row is painted on Card: a separator taken from the wrong row is a
// Card-coloured rule on a Doc-coloured row.
//
// AND THE INTER-BLOCK BLANK ROW IS SUPPRESSED INSIDE THE TABLE. Every block is
// followed by one, which is what makes two paragraphs read as two; between two
// rows of one grid it tears the box in half.
func TestDocumentDrawsATableAsOneGrid(t *testing.T) {
	st := darkStyles(t)
	const width = 60
	blocks := ParseBlocks([]byte(gridDocFixture), st)
	// paragraph, header, two body rows, paragraph.
	if len(blocks) != 5 || blocks[1].Kind != KindTableRow || !blocks[1].Table.Header || blocks[3].Kind != KindTableRow || blocks[4].Kind != KindParagraph {
		t.Fatalf("fixture is no longer a table between two paragraphs: %+v", blocks)
	}
	lines := RenderDoc(blocks, nil, nil, nil, OnLine(0), width, "cm", st)

	// One box: opened once by the first row, closed once by the last, with a
	// rule closing every row in between.
	header := linesOfBlock(lines, 1, width)
	if len(header) != 3 || !strings.HasPrefix(header[0], "┌") || !strings.HasPrefix(header[2], "├") {
		t.Fatalf("the header row is %q, want the box's top border, its own words, and the rule that closes it", header)
	}
	if !strings.Contains(header[1], "Step") {
		t.Fatalf("the header row's words are not on its middle line: %q", header)
	}
	middle := linesOfBlock(lines, 2, width)
	if len(middle) != 2 || !strings.Contains(middle[0], "Deploy the gateway") || !strings.HasPrefix(middle[1], "├") {
		t.Fatalf("the first body row is %q, want its own words and the rule that closes it", middle)
	}
	last := linesOfBlock(lines, 3, width)
	if len(last) != 3 || !strings.Contains(last[0], "Roll back") || !strings.HasPrefix(last[1], "└") {
		t.Fatalf("the last body row is %q, want its own words, the box's bottom border, and the table's one blank row", last)
	}
	if strings.TrimSpace(last[2]) != "" {
		t.Fatalf("the table's last line is %q, want the blank row that separates it from the paragraph below", last[2])
	}

	// NOTHING BLANK INSIDE THE BOX, which is the half a border assertion
	// cannot see: a blank row between two rows of one table leaves the box
	// drawn correctly on both sides of a gap.
	for _, idx := range []int{1, 2} {
		for j, l := range linesOfBlock(lines, idx, width) {
			if strings.TrimSpace(l) == "" {
				t.Fatalf("block %d line %d is blank, which tears the grid in half: %q", idx, j, linesOfBlock(lines, idx, width))
			}
		}
	}
	// While the paragraphs on either side keep theirs, so this is the table's
	// rule and not a separator that has been deleted everywhere.
	for _, idx := range []int{0, 4} {
		rows := linesOfBlock(lines, idx, width)
		if len(rows) != 2 || strings.TrimSpace(rows[1]) != "" {
			t.Fatalf("paragraph block %d is %q, want its own row and a blank one after it", idx, rows)
		}
	}

	// TWO TABLES SEPARATED BY A BLANK LINE ARE TWO BOXES, and they are
	// BLOCK-ADJACENT with nothing at all between them (TableRef's own ruling),
	// so the suppression above cannot be "the next block is also a table row".
	// Without the identity check the second table's header would sit directly
	// under the first table's bottom border and the two would read as one.
	t.Run("two adjacent tables keep the blank row between them", func(t *testing.T) {
		two := ParseBlocks([]byte("| a |\n|---|\n| one |\n\n| b |\n|---|\n| two |\n"), st)
		if len(two) != 4 || two[0].Table.ID == two[2].Table.ID {
			t.Fatalf("fixture is no longer two tables of two rows: %+v", two)
		}
		rows := linesOfBlock(RenderDoc(two, nil, nil, nil, OnLine(0), width, "cm", st), 1, width)
		if len(rows) != 3 || !strings.HasPrefix(rows[1], "└") || strings.TrimSpace(rows[2]) != "" {
			t.Fatalf("the first table's last row is %q, want its words, its bottom border, and a blank row before the next table", rows)
		}
	})

	// AND THE MAPPING STILL RESOLVES PER ROW, which is what the whole cut-up
	// is for: FirstLineOfFocus is a bare linear scan on BlockIdx, so each row
	// scrolls to a line of its own and they stay in document order.
	prev := -1
	for _, idx := range []int{1, 2, 3} {
		at := FirstLineOf(lines, idx)
		if lines[at].BlockIdx != idx {
			t.Fatalf("FirstLineOf(%d) = %d, which is tagged block %d", idx, at, lines[at].BlockIdx)
		}
		if at <= prev {
			t.Fatalf("row %d scrolls to line %d, which is not past the previous row's %d", idx, at, prev)
		}
		prev = at
	}
}

// TestGridBorderLinesCarryNoFocusAndNoMark is the gutter's and the margin's
// half of the attribution rule, and it exists because the rule puts a line on
// screen that the block does not own the ROW of.
//
// A row's group ends with the rule that CLOSES it, one screen row below the
// row's own words. Focus says "your keys act on this row", so drawn down the
// whole group it would point at the rule as well -- and the rule between two
// rows belongs to exactly one of them. The same argument carries the '※': under
// a grid the FIRST line of the first row's group is the box's top border, so a
// j == 0 rule would put the mark beside a rule rather than beside the words.
//
// THERE ARE TWO FOCUS INDICATORS AND THIS TEST NAMES BOTH. The RAIL BAND is
// st.RailCursor painted in the left margin by wrap(); the CURSOR GLYPH is '┃'
// in the gutter, painted by cursorGlyph(). They are different code paths
// switched by the same fact, and only the glyph was narrowed when this was
// first written -- so the band went on painting a table's closing rule.
//
// SO THIS DRIVES A WIDTH WHERE THE BAND EXISTS: the band collapses below
// railMinWidth, so a test run at 60 alone could not fail on the thing it is
// named for. Both widths are driven and the band case is asserted non-vacuous.
//
// A QUOTED TABLE IS DRIVEN TOO, and it is not decoration: the classification
// has to happen on the grid line BEFORE the quote bar is put in front of it. A
// border line led by "▎ " is no longer drawn from border runes alone, so a
// classifier that ran after the bar would answer "content" for every rule in
// every quoted table.
func TestGridBorderLinesCarryNoFocusAndNoMark(t *testing.T) {
	st := darkStyles(t)
	runes := borderRunesOf(tableGridBorder())
	v := ThreadView{Thread: domain.Thread{Comments: []domain.Comment{{Body: "a note"}}}, Placed: true}
	// One width below railMinWidth and one above it. The assertion that the
	// second really is above it is the one that keeps the band half of this
	// test honest if railMinWidth ever moves.
	widths := []int{60, 100}
	withBand := 0
	for _, w := range widths {
		if RailWidth(w) > 0 {
			withBand++
		}
	}
	if withBand == 0 {
		t.Fatalf("no driven width %v has a rail band (railMinWidth is %d), so nothing here measures one", widths, railMinWidth)
	}
	for _, width := range widths {
		for _, tc := range []struct{ name, src, lead string }{
			{"an unquoted table", gridDocFixture, ""},
			{"a quoted table", "> | Step | Owner |\n> |---|---|\n> | Deploy | dana |\n", quoteBarGlyph + " "},
		} {
			t.Run(fmt.Sprintf("%s at width %d", tc.name, width), func(t *testing.T) {
				blocks := ParseBlocks([]byte(tc.src), st)
				header := -1
				for i, b := range blocks {
					if b.Kind == KindTableRow && b.Table.Header {
						header = i
						break
					}
				}
				if header < 0 {
					t.Fatalf("fixture has no header row: %+v", blocks)
				}
				// The cursor and the comment both on the HEADER row, whose
				// group is the only one carrying a border on BOTH sides of its
				// content.
				lines := RenderDoc(blocks, map[int][]ThreadView{header: {v}}, nil, nil, OnLine(header), width, "cm", st)
				// The exact prefix wrap() emits for a focused row, built from
				// the same two things it builds it from. Comparing the whole
				// rendered band and not just a colour is what makes this an
				// assertion about the margin rather than about a substring
				// that might occur anywhere in the row.
				cursorBand := st.RailCursor.Render(strings.Repeat(" ", RailWidth(width)))
				glyphs, marks, banded, borders := 0, 0, 0, 0
				for j, l := range lines {
					if l.BlockIdx != header {
						continue
					}
					gutter := gutterOf(l.Text, width)
					body, found := strings.CutPrefix(rowBody(l.Text, width), tc.lead)
					if !found {
						// The block's own trailing blank row, which carries no
						// bar by design (see quoteBarGlyph) and no focus of
						// any kind -- blankRow wraps with false.
						continue
					}
					onBand := RailWidth(width) > 0 && strings.HasPrefix(l.Text, cursorBand)
					if borderOnlyLine(body, runes) {
						borders++
						if strings.TrimSpace(gutter) != "" {
							t.Fatalf("line %d is a border and carries %q in the gutter: %q", j, gutter, ansi.Strip(l.Text))
						}
						if onBand {
							t.Fatalf("line %d is a border and carries the focus BAND, so the focus is %d screen rows tall on a row whose text is one: %q", j, borders+1, ansi.Strip(l.Text))
						}
						continue
					}
					if onBand {
						banded++
					}
					if strings.Contains(gutter, "┃") {
						glyphs++
					}
					if strings.Contains(gutter, "※") {
						marks++
					}
				}
				// The header's group is a top border, one content line at both
				// these widths, and the rule that closes it.
				if borders != 2 {
					t.Fatalf("the header row's group holds %d border lines, want 2 -- so this asserts nothing about borders", borders)
				}
				if glyphs != 1 || marks != 1 {
					t.Fatalf("the focused, commented header row carries '┃' on %d content lines and '※' on %d, want 1 of each", glyphs, marks)
				}
				wantBanded := 0
				if RailWidth(width) > 0 {
					wantBanded = 1
				}
				if banded != wantBanded {
					t.Fatalf("the focused header row carries the band on %d content lines, want %d at width %d (RailWidth %d)", banded, wantBanded, width, RailWidth(width))
				}
			})
		}
	}
}

// TestATableRowWithNoGridStillReachesTheReader is the pre-pass's own contract
// read from the caller's side: A BLOCK WITH NO ENTRY WAS NOT PAINTED, and the
// document must draw it anyway.
//
// THE HOLE IS REAL AND IT IS REACHED HERE THROUGH ui.RenderDoc, not simulated:
// at a clamped budget of 1 splitTableGrid answers false for the whole table. A
// terminal of 5 columns is what produces that budget, far below app/list.go's
// own minimum-size gate, so this is a hole nobody can fall into rather than a
// defect in the field. It is asserted because the contract is not obligatory:
// `for _, l := range grids[idx]` compiles and emits nothing, and a row that
// silently vanished would take its anchor off the screen.
func TestATableRowWithNoGridStillReachesTheReader(t *testing.T) {
	st := darkStyles(t)
	blocks := ParseBlocks([]byte(gridDocFixture), st)
	// The terminal width whose budget is exactly 1: the rail collapses below
	// railMinWidth, so it is gutterWidth + 1. Asserted rather than assumed,
	// because the hole this drives is a property of the BUDGET.
	width := gutterWidth + 1
	body := width - 2*RailWidth(width) - gutterWidth
	if body != 1 {
		t.Fatalf("a %d-column terminal budgets a block %d cells, want 1", width, body)
	}
	if painted := paintTables(blocks, body, st, nil, nil); len(painted) != 0 {
		t.Fatalf("the pre-pass painted %d groups at a budget of 1, so this no longer drives the fallback", len(painted))
	}
	lines := RenderDoc(blocks, nil, nil, nil, OnLine(0), width, "cm", st)
	for _, idx := range []int{1, 2, 3} {
		rows := linesOfBlock(lines, idx, width)
		if len(rows) == 0 {
			t.Fatalf("table row block %d drew no line at all, so the cursor can rest on a block that is not on screen", idx)
		}
		if strings.TrimSpace(strings.Join(rows, "")) == "" {
			t.Fatalf("table row block %d drew %d line(s) and none of them has anything on it: %q", idx, len(rows), rows)
		}
	}
	// AND THE FRAME INVARIANT IS NOT ASSERTED HERE, which is worth saying
	// rather than leaving as an omission: at five columns EVERY kind overflows
	// -- wrapPlain floors its width at 10, so the fixture's own paragraphs
	// spill too. That is the pre-existing defect Line's doc comment measures
	// (ui/render.go), not something the fallback does, and asserting it here
	// would be asserting it about the wrong subject. The budgets a real
	// terminal reaches are held to it by TestTableRowsFitTheirWidthBudget.
}

// TestQuotedCommentedGridNeedsNoThirdTwin closes the one combination the twin
// render leaves open: a table that is BOTH quoted AND commented. The painter
// renders two twins, one per colour zone, and the quote bar is drawn OUTSIDE
// the table string by renderDocPainted -- so the question was whether the bar
// and the box could end up in different zones, which would need a third render.
//
// THEY CANNOT, because quoteBarLead is handed blockSt, the row's own
// zone-rebased Styles, exactly as withQuoteBar is for prose. Measured here on
// every glyph a quoted commented row puts on screen, with the row above it
// asserted on Doc so the boundary is the subject.
func TestQuotedCommentedGridNeedsNoThirdTwin(t *testing.T) {
	th, err := theme.Lookup(theme.DefaultName)
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	const width = 60
	src := "> | Step | Owner |\n> |---|---|\n> | Deploy | dana |\n> | Roll back | sam |\n"
	blocks := ParseBlocks([]byte(src), st)
	if len(blocks) != 3 || blocks[2].QuoteDepth != 1 {
		t.Fatalf("fixture is no longer a quoted table of three rows: %+v", blocks)
	}
	const commented = 2
	v := ThreadView{Thread: domain.Thread{Comments: []domain.Comment{{Body: "note"}}}, Placed: true}
	lines := RenderDoc(blocks, map[int][]ThreadView{commented: {v}}, nil, nil, OnLine(commented), width, "cm", st)
	for _, tc := range []struct {
		block int
		zone  string
		bg    string
	}{
		{commented - 1, "Doc", th.Doc},
		{commented, "Card", th.Card},
	} {
		checked := 0
		for _, l := range lines {
			if l.BlockIdx != tc.block {
				continue
			}
			if strings.TrimSpace(rowBody(l.Text, width)) == "" {
				// The block's own trailing separator, which is blankRow's and
				// carries neither a bar nor a box.
				continue
			}
			for _, glyph := range []string{quoteBarGlyph, "│", "─"} {
				if !strings.Contains(ansi.Strip(l.Text), glyph) {
					continue
				}
				style := styleBefore(t, l.Text, glyph)
				if bg := "48;2;" + sgrRGB(t, tc.bg); !strings.Contains(style, bg) {
					t.Fatalf("block %d's %q is styled %q, which does not carry %s (%s) -- a quoted commented row is painted in two zones at once", tc.block, glyph, style, tc.zone, tc.bg)
				}
				checked++
			}
		}
		// Four checks per row at this width: the bar and a column divider on
		// the row's one content line, and the bar and the rule on the border
		// that closes it. A fixture that stopped reaching them would pass this
		// test vacuously.
		if checked < 4 {
			t.Fatalf("block %d put only %d glyph(s) through the zone check, so this asserts almost nothing", tc.block, checked)
		}
	}
}

// TestACommentCardClosesTheGridAndReopensIt is the one thing a grid does that
// a run of independent rows did not have to answer for: an expanded thread
// inserts card lines BETWEEN two rows of one box. Drawn naively the box is left
// open around them and the grid reads as broken.
//
// THE RULE: CLOSE the grid above the card, draw the card, REOPEN it below, so a
// commented table reads as two boxes with a comment between them. The card
// keeps exactly the shape it has under every other block kind -- outside the
// content, in the gutter's indent.
//
// THE REOPENED BOX DOES NOT REPEAT THE HEADER: under a grid the header is drawn
// ONCE per table, commented or not. The header case below asserts that
// directly -- a comment on the header row reopens the box on the FIRST BODY ROW.
//
// AND THE COMMENTED ROW'S OWN SEPARATOR IS SUPPRESSED IN FAVOUR OF THE CLOSING
// BORDER, which is the defect the first prototype shipped: a group ends with
// the rule that CLOSES it, so a bottom border APPENDED rather than SUBSTITUTED
// draws '├───┤' immediately followed by '└───┘'. The sketches below are
// exhaustive over the table's lines, in order, so a doubled rule cannot pass.
func TestACommentCardClosesTheGridAndReopensIt(t *testing.T) {
	th, err := theme.Lookup(theme.DefaultName)
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	const width = 60
	blocks := ParseBlocks([]byte(gridDocFixture), st)
	// paragraph, header, two body rows, paragraph -- the table is blocks 1-3.
	if len(blocks) != 5 || !blocks[1].Table.Header || blocks[2].Kind != KindTableRow || blocks[3].Kind != KindTableRow {
		t.Fatalf("fixture is no longer a three-row table between two paragraphs: %+v", blocks)
	}
	runes := borderRunesOf(tableGridBorder())
	v := ThreadView{Thread: domain.Thread{Comments: []domain.Comment{{Attribution: domain.Attribution{ActorLogin: "dana-codes", ActorDisplay: "dana"}, Body: "why after the freeze?"}}}, Placed: true}

	// sketch is every line the TABLE puts on screen, in order, reduced to the
	// one thing each case is about: which block owns the line and whether it is
	// a border (named by the corner it opens with), the row's own text, a card
	// row, or the blank one that ends the table. It is exhaustive on purpose --
	// an assertion that only looked for the lines it wanted could not see a
	// rule drawn twice.
	sketch := func(lines []Line) []string {
		var out []string
		for _, l := range lines {
			if l.BlockIdx < 1 || l.BlockIdx > 3 {
				continue
			}
			body := rowBody(l.Text, width)
			switch {
			case l.IsThread:
				out = append(out, fmt.Sprintf("%d card", l.BlockIdx))
			case strings.TrimSpace(body) == "":
				out = append(out, fmt.Sprintf("%d blank", l.BlockIdx))
			case borderOnlyLine(body, runes):
				out = append(out, fmt.Sprintf("%d %c", l.BlockIdx, []rune(body)[0]))
			default:
				out = append(out, fmt.Sprintf("%d text", l.BlockIdx))
			}
		}
		return out
	}

	for _, tc := range []struct {
		name      string
		commented []int
		expanded  bool
		want      []string
	}{
		{
			// The middle row: one box above the card, one below it, and the
			// row's own trailing separator gone.
			name:      "a body row",
			commented: []int{2},
			expanded:  true,
			want: []string{
				"1 ┌", "1 text", "1 ├",
				"2 text", "2 └",
				"2 card", "2 card",
				"3 ┌", "3 text", "3 └", "3 blank",
			},
		},
		{
			// THE HEADER CASE IS THE RULING, DRIVEN: the box reopens on the
			// first BODY row and the header's words are not drawn a second
			// time.
			name:      "the header row",
			commented: []int{1},
			expanded:  true,
			want: []string{
				"1 ┌", "1 text", "1 └",
				"1 card", "1 card",
				"2 ┌", "2 text", "2 ├",
				"3 text", "3 └", "3 blank",
			},
		},
		{
			// THE LAST ROW ALREADY ENDS IN THE BOTTOM BORDER, so there is
			// nothing to substitute and nothing to reopen -- and the case is
			// here because "append a bottom border" would draw two.
			name:      "the last row",
			commented: []int{3},
			expanded:  true,
			want: []string{
				"1 ┌", "1 text", "1 ├",
				"2 text", "2 ├",
				"3 text", "3 └",
				"3 card", "3 card", "3 blank",
			},
		},
		{
			// A COLLAPSED THREAD DRAWS NO CARD AND MUST NOT SPLIT THE BOX. The
			// row is still painted on Card -- it carries comments either way --
			// so this is the case that says the split follows where the CARD
			// LINES LAND and not the zone.
			name:      "a collapsed thread",
			commented: []int{2},
			expanded:  false,
			want: []string{
				"1 ┌", "1 text", "1 ├",
				"2 text", "2 ├",
				"3 text", "3 └", "3 blank",
			},
		},
		{
			// TWO ADJACENT ROWS, which is the shape the splice's two halves
			// meet in: row 2 closes for its own card and row 3 opens for it
			// and closes again for its own, so the middle of the table is a
			// ONE-ROW BOX. It is also the only shape in which the reopening
			// border's twin is a real choice -- see the zone drive below,
			// which is what that fact is actually pinned by.
			name:      "two adjacent rows",
			commented: []int{2, 3},
			expanded:  true,
			want: []string{
				"1 ┌", "1 text", "1 ├",
				"2 text", "2 └",
				"2 card", "2 card",
				"3 ┌", "3 text", "3 └",
				"3 card", "3 card", "3 blank",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			views := map[int][]ThreadView{}
			expanded := map[int]bool{}
			for _, idx := range tc.commented {
				views[idx] = []ThreadView{v}
				expanded[idx] = tc.expanded
			}
			lines := RenderDoc(blocks, views, nil, expanded, OnLine(tc.commented[0]), width, "cm", st)
			if got := sketch(lines); !slices.Equal(got, tc.want) {
				t.Fatalf("the table draws\n\t%v\nwant\n\t%v", got, tc.want)
			}
			// The header's words appear ONCE in the whole document, whichever
			// row carries the comment. The sketch above cannot see this: it
			// counts lines, and a repeated header is a line the shape already
			// accounts for.
			headers := 0
			for _, l := range lines {
				if strings.Contains(ansi.Strip(l.Text), "Step") {
					headers++
				}
			}
			if headers != 1 {
				t.Fatalf("the header's words are on %d lines, want 1 -- the reopened box is repeating them", headers)
			}
		})
	}

	// THE SPLICED BORDERS COME FROM THE TWIN OF THE ROW THEY ARE ATTRIBUTED
	// TO, which is the half only a commented table can show. One taken from the
	// wrong twin is a Card-coloured glyph on a Doc row or the reverse.
	//
	// THE THIRD CASE IS THE ONE THAT PINS AN INVARIANT, and the first two are
	// not. For a TABLE ROW cardBelow IMPLIES onCard, so the row a card sits
	// under is ALWAYS on Card and the CLOSING border's twin was never a choice.
	//
	// THE REOPENING BORDER IS THE ONLY TWIN-SENSITIVE LINE IN THE WHOLE SPLICE.
	// Its twin is the REOPENED row's, which is Card exactly when that row
	// carries comments of its own -- reachable only when two adjacent rows are
	// both commented. Hard-wiring it to the Doc twin leaves every package in the
	// tree green without case three, and puts a Doc-coloured '┌' inside a Card
	// row the first time a reviewer comments on two rows in a row.
	const commented = 2
	for _, tc := range []struct {
		name      string
		commented []int
		block     int
		glyph     string
		bg        string
		zone      string
	}{
		{"the border closing the box above the card", []int{commented}, commented, "└", th.Card, "Card"},
		{"the border reopening it below an uncommented row", []int{commented}, commented + 1, "┌", th.Doc, "Doc"},
		{"the border reopening it below a row that carries its own comment", []int{commented, commented + 1}, commented + 1, "┌", th.Card, "Card"},
	} {
		views := map[int][]ThreadView{}
		expanded := map[int]bool{}
		for _, idx := range tc.commented {
			views[idx] = []ThreadView{v}
			expanded[idx] = true
		}
		lines := RenderDoc(blocks, views, nil, expanded, OnLine(commented), width, "cm", st)
		found := 0
		for _, l := range lines {
			if l.BlockIdx != tc.block || l.IsThread || !strings.HasPrefix(rowBody(l.Text, width), tc.glyph) {
				continue
			}
			found++
			if bg := "48;2;" + sgrRGB(t, tc.bg); !strings.Contains(styleBefore(t, l.Text, tc.glyph), bg) {
				t.Fatalf("%s is styled %q, which does not carry %s (%s)", tc.name, styleBefore(t, l.Text, tc.glyph), tc.zone, tc.bg)
			}
		}
		if found != 1 {
			t.Fatalf("%s is on %d lines of block %d, want 1", tc.name, found, tc.block)
		}
	}

	// AND THE REOPENED BORDER CARRIES NO FOCUS, which is the rule
	// TestGridBorderLinesCarryNoFocusAndNoMark states about a line that did not
	// exist when it was written. renderDocPainted classifies a group's lines
	// AFTER the splice, so the prepended border is a border like any other.
	//
	// DRIVEN AT A WIDTH WHERE THE BAND EXISTS, and asserting that the band and
	// the '┃' agree line by line rather than checking either alone: they are two
	// code paths switched by one boolean, and the last time this was got wrong
	// it was the band that went unnoticed.
	const banded = 100
	if RailWidth(banded) == 0 {
		t.Fatalf("width %d has no rail band (railMinWidth is %d), so this would measure only half the focus", banded, railMinWidth)
	}
	cursorBand := st.RailCursor.Render(strings.Repeat(" ", RailWidth(banded)))
	const reopened = commented + 1
	lines := RenderDoc(blocks, map[int][]ThreadView{commented: {v}}, nil, map[int]bool{commented: true}, OnLine(reopened), banded, "cm", st)
	// AND A REOPENED ROW'S FIRST LINE IS ITS '┌', which is what FirstLineOf
	// answers for it and therefore where scroll-to-cursor puts the viewport.
	// That is right -- it is already what a table's FIRST row does -- and it is
	// pinned here because two things read a block's first line as its TEXT:
	// FirstLineOfFocus's own doc comment, and app/search_test.go's landing
	// assertion, which looks for the cursor glyph on that line.
	if first := rowBody(lines[FirstLineOf(lines, reopened)].Text, banded); !strings.HasPrefix(first, "┌") {
		t.Fatalf("FirstLineOf a reopened row is %q, want the border that opens its box", ansi.Strip(first))
	}
	onWords, onBorders := 0, 0
	for _, l := range lines {
		if l.BlockIdx != reopened || l.IsThread {
			continue
		}
		body := rowBody(l.Text, banded)
		if strings.TrimSpace(body) == "" {
			// The table's own trailing blank row, which carries no focus of
			// any kind by design (blankRow).
			continue
		}
		onBand := strings.HasPrefix(l.Text, cursorBand)
		if glyph := strings.Contains(gutterOf(l.Text, banded), "┃"); onBand != glyph {
			t.Fatalf("%q carries the focus band %v and the focus glyph %v -- the two indicators have come apart", ansi.Strip(body), onBand, glyph)
		}
		if borderOnlyLine(body, runes) {
			if onBand {
				t.Fatalf("the focus stands on the reopened row's border %q, which is not a line the row's words are on", ansi.Strip(body))
			}
			onBorders++
			continue
		}
		if !onBand {
			t.Fatalf("the focused reopened row's own words %q carry no focus at all", ansi.Strip(body))
		}
		onWords++
	}
	if onWords != 1 || onBorders != 2 {
		t.Fatalf("the focused reopened row carries the focus on %d of its own lines and skips %d borders, want 1 and 2", onWords, onBorders)
	}
}

// coldTableGridCache invalidates the memo, UNDER ITS OWN MUTEX. A bare
// `tableGridCache.valid = false` is a data race waiting for the first ui test
// that renders and calls t.Parallel; the lock is the type's contract and a test
// does not get an exemption from it.
//
// IT ALSO SAYS OUT LOUD THAT THE CACHE IS PACKAGE STATE THESE TESTS SHARE.
// Correctness does not depend on that, because the key is exact and a
// wrong-document entry misses, but a test that wants a COLD painter has to ask
// for one.
func coldTableGridCache() {
	tableGridCache.mu.Lock()
	defer tableGridCache.mu.Unlock()
	tableGridCache.valid = false
}

// samePainted answers whether two pre-pass results are the SAME map and not
// merely equal ones, which is the only observable difference between a cache
// hit and a cache miss: paintTables is deterministic, so a miss recomputes the
// identical content.
func samePainted(a, b map[int][]string) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

func paintedEqual(a, b map[int][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		if !slices.Equal(av, b[k]) {
			return false
		}
	}
	return true
}

// TestPaintedTableCacheHitsOnACursorStepAndOnNothingElse is two claims that
// have to be made together: the memo must HIT on a cursor step, and it must
// MISS on every input the painted grid depends on. A memo that only hits is a
// stale render; a memo that only misses is no memo. Block painting is
// cursor-independent, so the cursor is deliberately not in the key.
//
// THE STYLES ARE KEYED BY POINTER AND THAT IS CONSERVATIVE, which is why the
// case below uses a DIFFERENT THEME rather than a second Styles built from the
// same one: a second dark-theme Styles paints byte-identical grids, so it could
// not tell an incomplete key from a complete one.
//
// THE MISSES ARE CHECKED BY CONTENT AND NOT BY IDENTITY, which is the whole
// point: an incomplete key does not crash, it hands back the PREVIOUS
// document's grid, and the only assertion that catches it is "what came out
// equals what the painter answers for these arguments". Each case also has to
// change the painting at all, or it would pass against a key that ignored it.
func TestPaintedTableCacheHitsOnACursorStepAndOnNothingElse(t *testing.T) {
	st := darkStyles(t)
	const width = 76
	base := gridBlocks(t, gridFixture, st, 3)
	noCard := func(int) bool { return false }
	noCardBelow := func(int) bool { return false }

	// A repaint that changed nothing is the arrow-press case, and it is the
	// SAME map handed back.
	first := paintTablesCached(base, width, st, noCard, noCardBelow)
	if again := paintTablesCached(base, width, st, noCard, noCardBelow); !samePainted(first, again) {
		t.Fatal("two identical calls repainted, so no arrow press would ever hit this cache")
	}
	// And the document renders identically either way, which is what says the
	// hit is a hit and not a shortcut.
	cold := RenderDoc(base, nil, nil, nil, OnLine(0), width, "cm", st)
	coldTableGridCache()
	if warm := RenderDoc(base, nil, nil, nil, OnLine(0), width, "cm", st); !slices.Equal(collectText(cold), collectText(warm)) {
		t.Fatal("the same document renders differently through a cold cache and a warm one")
	}

	// A REPAINT MUST NOT WRITE INTO WHAT THE CACHE HANDED BACK, and a QUOTED
	// table is where that would show: the bar is drawn onto the cached lines
	// by renderDocPainted, so a bar written in place would accumulate one per
	// repaint and nothing would fail until somebody looked at the screen.
	quoted := ParseBlocks([]byte("> | Step | Owner |\n> |---|---|\n> | Deploy | dana |\n"), st)
	firstPaint := collectText(RenderDoc(quoted, nil, nil, nil, OnLine(0), width, "cm", st))
	for i := 1; i <= 3; i++ {
		if again := collectText(RenderDoc(quoted, nil, nil, nil, OnLine(0), width, "cm", st)); !slices.Equal(firstPaint, again) {
			t.Fatalf("repaint %d of a quoted table differs from the first, so the render is writing into the cache", i)
		}
	}

	withCell := slices.Clone(base)
	withCell[1].Cells = slices.Clone(base[1].Cells)
	withCell[1].Cells[0].Display = st.Text.Render("something else entirely")

	withID := slices.Clone(base)
	withID[2].Table.ID = base[2].Table.ID + 1

	withHeader := slices.Clone(base)
	withHeader[1].Table.Header = true

	withDepth := slices.Clone(base)
	for i := range withDepth {
		withDepth[i].QuoteDepth = 1
	}

	shifted := append([]Block{{Kind: KindParagraph, Display: st.Text.Render("intro")}}, base...)

	cases := []struct {
		name      string
		blocks    []Block
		width     int
		st        *Styles
		onCard    func(int) bool
		cardBelow func(int) bool
	}{
		{"the width", base, width - 1, st, noCard, noCardBelow},
		{"the styles", base, width, lightStyles(t), noCard, noCardBelow},
		{"the zone assignment", base, width, st, func(i int) bool { return i == 1 }, noCardBelow},
		// WHERE A COMMENT CARD SITS, MOVED ON ITS OWN AND NOT ALONGSIDE
		// THE ZONE. In a document the two arrive together, but a case that
		// moved both would MISS on the zone alone and never ask whether
		// this is in the key at all.
		{"whether a comment card is drawn under a row", base, width, st, noCard, func(i int) bool { return i == 1 }},
		{"a cell's own text", withCell, width, st, noCard, noCardBelow},
		{"which table a row belongs to", withID, width, st, noCard, noCardBelow},
		{"whether a row is the header", withHeader, width, st, noCard, noCardBelow},
		{"how deeply the table is quoted", withDepth, width, st, noCard, noCardBelow},
		{"where the rows sit in the block list", shifted, width, st, noCard, noCardBelow},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want := paintTables(c.blocks, c.width, c.st, c.onCard, c.cardBelow)
			if paintedEqual(want, first) {
				t.Fatalf("changing %s paints the identical grid, so this case would pass against a key that ignored it", c.name)
			}
			// Warm the cache on the BASE arguments, then ask for the changed
			// ones: a key missing this dependency answers with the base.
			paintTablesCached(base, width, st, noCard, noCardBelow)
			if got := paintTablesCached(c.blocks, c.width, c.st, c.onCard, c.cardBelow); !paintedEqual(got, want) {
				t.Fatalf("%s is not in the cache key: the cache answered with a grid painted for other arguments", c.name)
			}
		})
	}
}

// TestWideRunOverflowsWrapPlainButNotTheGrid measures the one thing the drive
// above stops short of, and says where it comes from, because a table cell is
// the construct most exposed to it: cells are terse, and terse CJK has no
// spaces in it at all.
//
// THIS TEST'S SUBJECT IS wrapPlain. ansi.Wordwrap breaks at spaces, so a run
// with none comes back at its own width whatever budget it was handed, and a
// PARAGRAPH of one still reaches the frame over-wide at 31 columns and fits at
// 80. The residual predates tables and is recorded rather than repaired here --
// repairing it is a change to how every kind that wraps wraps.
//
// THE TABLE VALUE IS HERE AS THE CONTRAST. A table row is drawn by
// lipgloss/v2/table now, whose Wrap BREAKS A RUN THAT HAS NO BREAK OPPORTUNITY
// IN IT rather than handing it back whole -- so the identical 62-cell run fits
// every budget as a cell and overflows every narrow one as a paragraph. The
// CAUSE is driven too: the arm the grid replaced is still reachable, and driven
// directly it still hands back the over-wide row, so this is the grid and not
// the fixture.
//
// THREE OF THE SIX ARMS NEVER TOUCH wrapPlain. KindHeading neither wraps nor
// truncates; KindCode has its own wrap, wrapCodeRow, which hard-breaks by CELLS
// and cannot hand back a run it could not fit; KindRule is a strings.Repeat.
// Only KindListItem, the default arm, and the table row's own FALLBACK call
// wrapPlain.
//
// AND THERE ARE TWO FAILURE MODES, not one. A SINGLE-ROW Line wider than the
// frame is a ragged right margin: the row runs past the terminal's column and
// the line-to-block mapping still holds. A Line whose Text carries a real
// newline is two screen rows inside one ui.Line, which breaks that mapping and
// makes the rendered frame taller than the viewport -- see
// TestOneUILineCanCarryTwoScreenRows. This test drives the FIRST mode, which is
// what wrapPlain's unbreakable run produces; the ragged mode is entirely
// wrapPlain's and always was.
//
// PINNED SO A LATER FIX HAS TO SAY SO. If wrapPlain learns to break a run that
// cannot fit, this test fails and names what changed, rather than the change
// landing silently under an assertion that was never true. wrapCodeRow is what
// a wrapPlain that could break a run would look like, and is deliberately NOT
// reused here: it hard-breaks, which is right for code and is not obviously
// right for prose.
func TestWideRunOverflowsWrapPlainButNotTheGrid(t *testing.T) {
	// 31 runes, 62 cells, and not one break opportunity anywhere in it.
	const run = "この手順は五時以降に実行してくださいオンコールの確認を待つこと"
	if got := ansi.StringWidth(run); got != 62 {
		t.Fatalf("the fixture run is %d cells, want 62", got)
	}
	if got := wrapPlain(run, 25); len(got) != 1 || ansi.StringWidth(got[0]) != 62 {
		t.Fatalf("wrapPlain(run, 25) = %q -- this test exists to record that it does NOT break the run", got)
	}
	st := darkStyles(t)
	tableSrc := []byte("| a |\n|---|\n| " + run + " |\n")
	paragraphSrc := []byte(run + "\n")
	spills := func(src []byte, width int) bool {
		for _, l := range RenderDoc(ParseBlocks(src, st), nil, nil, nil, OnLine(0), width, "cm", st) {
			if strings.Contains(ansi.Strip(l.Text), "\n") {
				return true
			}
		}
		return false
	}
	if !spills(paragraphSrc, narrowWidth) {
		t.Fatalf("at %d columns the 62-cell run no longer spills out of a paragraph -- wrapPlain has been taught to break it, which is the fix this test was recording the absence of", narrowWidth)
	}
	if spills(paragraphSrc, 80) {
		t.Fatal("at 80 columns the run fits the budget and must not spill out of a paragraph")
	}
	for _, width := range []int{narrowWidth, 80} {
		if spills(tableSrc, width) {
			t.Fatalf("at %d columns the same run spills out of a table cell -- the grid's own wrap has stopped breaking it", width)
		}
	}

	// THE CAUSE, so that "a table no longer overflows" cannot be satisfied by a
	// fixture that quietly stopped reaching its budget. The arm the grid
	// replaced is still reachable and driven directly against the same budget
	// it still hands back the 62-cell row, while the grid's own lines fit that
	// budget exactly.
	body := narrowWidth - 2*RailWidth(narrowWidth) - gutterWidth
	blocks := ParseBlocks(tableSrc, st)
	row := len(blocks) - 1
	widest := 0
	for _, l := range withQuoteBar(blocks[row], body, st, false) {
		if w := ansi.StringWidth(ansi.Strip(l)); w > widest {
			widest = w
		}
	}
	if widest <= body {
		t.Fatalf("the fallback arm's widest row is %d cells against a budget of %d -- it no longer overflows, so the contrast above says nothing about the grid", widest, body)
	}
	painted := paintTables(blocks, body, st, nil, nil)
	if len(painted[row]) == 0 {
		t.Fatalf("the pre-pass painted nothing for the row at budget %d, so there is no grid here to compare against", body)
	}
	for i, l := range painted[row] {
		if w := ansi.StringWidth(ansi.Strip(l)); w > body {
			t.Fatalf("grid line %d is %d cells against a budget of %d", i, w, body)
		}
	}
}

// dropSpacesAndNewlines is the content-preservation check every wrapPlain
// test below shares: wrapping only ever turns a run of whitespace into a
// line break or back again, so a fixture's non-whitespace runes must be
// identical before and after, in order, or wrapPlain lost or reordered
// something rather than merely choosing a different place to break.
func dropSpacesAndNewlines(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' {
			return -1
		}
		return r
	}, s)
}

// TestWrapPlainCarriesAHyphenatedWordDown: a hyphenated word whose own width
// already fits the line must move onto the next row WHOLE when it does not fit
// where it started, rather than splitting at the hyphen the way ansi.Wordwrap's
// hardcoded `case r == '-':` breakpoint always used to. The corpus evidence for
// why that split is wrong is not hypothetical: "gofmt -l -w" wrapped at column
// 100 used to leave "-" stranded at the end of one row and "w" orphaned at the
// start of the next.
//
// EACH CASE IS PROVEN NON-VACUOUS BY THE COMPARISON AGAINST RAW ansi.Wordwrap:
// if the raw call does not actually split the fixture's hyphenated word across
// two lines, the fixture is not exercising the defect this test exists to
// catch, and that is a failure in its own right rather than a silent pass.
func TestWrapPlainCarriesAHyphenatedWordDown(t *testing.T) {
	cases := []struct {
		name  string
		s     string
		width int
		want  []string
	}{
		{
			// "hand-placed" needs a same-row neighbour before it for the
			// defect to trigger at all: as the first word on a fresh row
			// its own hyphen never lands anywhere near the limit, so
			// nothing this test is written to catch would fire without
			// "abcdef" eating the first 6 cells first.
			name:  "a hyphenated word that fits the line carries down whole",
			s:     "abcdef hand-placed newlines",
			width: 11,
			want:  []string{"abcdef", "hand-placed", "newlines"},
		},
		{
			name:  "a leading-hyphen flag token carries down, the corpus case",
			s:     "so gofmt -l -w rewrote nothing",
			width: 12,
			want:  []string{"so gofmt -l", "-w rewrote", "nothing"},
		},
		{
			name:  "a standalone hyphen between spaces is not exempt from the check",
			s:     "aaaaaaaaa - bbbbbbbbb",
			width: 10,
			want:  []string{"aaaaaaaaa", "-", "bbbbbbbbb"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if raw := strings.Split(ansi.Wordwrap(c.s, c.width, ""), "\n"); slices.Equal(raw, c.want) {
				t.Fatalf("ansi.Wordwrap(%q, %d, \"\") already returns %q -- this fixture does not exercise the hyphen-split defect wrapPlain exists to work around", c.s, c.width, raw)
			}
			got := wrapPlain(c.s, c.width)
			if !slices.Equal(got, c.want) {
				t.Fatalf("wrapPlain(%q, %d) = %q, want %q", c.s, c.width, got, c.want)
			}
			for i, l := range got {
				if w := ansi.StringWidth(l); w > c.width {
					t.Fatalf("line %d of wrapPlain(%q, %d) is %d cells wide, over budget", i, c.s, c.width, w)
				}
			}
			if have, want := dropSpacesAndNewlines(strings.Join(got, "\n")), dropSpacesAndNewlines(c.s); have != want {
				t.Fatalf("wrapPlain(%q, %d) lost or reordered content: have %q, want %q", c.s, c.width, have, want)
			}
		})
	}
}

// TestWrapPlainNeverOverflowsAtAForcedHyphenBreak is the other half of
// protectHyphens' design: a hyphenated run WIDER than width cannot be spared a
// break somewhere, so its hyphens stay real breakpoints on the first pass and
// ansi.Wordwrap's upstream bug is left free to fire -- the row it breaks on
// lands one cell over budget. wrapPlain's retry loop is what repairs that, and
// this is its regression coverage.
//
// BOTH FIXTURES NEED MORE THAN ONE HYPHEN AVAILABLE, or need the ONE hyphen's
// own token to still fit once fully glued: if that atomic width is ITSELF still
// wider than the line, no amount of protecting helps --
// TestWrapPlainAcceptsAResidueWhenAHyphenTokenCannotFitEitherWay pins that case
// on its own, so this test only exercises the case the retry loop can close.
//
// NON-VACUITY: the raw-ansi.Wordwrap comparison below must itself produce an
// over-width row, or this fixture is not driving the retry loop at all.
func TestWrapPlainNeverOverflowsAtAForcedHyphenBreak(t *testing.T) {
	cases := []struct {
		name  string
		s     string
		width int
	}{
		{"a single hyphen, but its token fits once glued whole", "xxxxxxxxx left-right end", 14},
		{"a multi-hyphen compound wider than the line", "state-of-the-art tooling helps", 12},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			overwide := false
			for _, l := range strings.Split(ansi.Wordwrap(c.s, c.width, ""), "\n") {
				if ansi.StringWidth(l) > c.width {
					overwide = true
				}
			}
			if !overwide {
				t.Fatalf("ansi.Wordwrap(%q, %d, \"\") never exceeds %d cells -- this fixture does not reach the bug the retry loop exists to repair", c.s, c.width, c.width)
			}

			got := wrapPlain(c.s, c.width)
			for i, l := range got {
				if w := ansi.StringWidth(l); w > c.width {
					t.Fatalf("wrapPlain(%q, %d) line %d is %d cells: %q", c.s, c.width, i, w, l)
				}
			}
			if have, want := dropSpacesAndNewlines(strings.Join(got, "\n")), dropSpacesAndNewlines(c.s); have != want {
				t.Fatalf("wrapPlain(%q, %d) lost or reordered content: have %q, want %q", c.s, c.width, have, want)
			}
		})
	}
}

// TestWrapPlainAcceptsAResidueWhenAHyphenTokenCannotFitEitherWay pins a
// residue class: a token with exactly ONE hyphen, wide enough that
// ansi.Wordwrap's bug breaks it, but whose fully-glued (protected) width is
// STILL wider than the line. Splitting it would have kept it inside budget;
// keeping it whole does not, because there is no other hyphen to fall back to.
//
// THE ARITHMETIC IS NOT A COINCIDENCE, IT IS FORCED. The bug only fires when
// the text already on the row plus the token's part before the hyphen lands
// EXACTLY at width; protecting the hyphen makes the token's total width =
// (that same width) + 1 + (whatever follows the hyphen), which is strictly MORE
// than width the moment anything follows the hyphen at all. So a single-hyphen
// token that trips this bug can never be rescued by protecting its only hyphen:
// wrapPlain recognises it has run out of hyphens to try and returns the row
// exactly as ansi.Wordwrap drew it, over budget, rather than guess.
//
// THIS TEST ASSERTS THE RESIDUE, NOT ITS ABSENCE: characterization of a real,
// disclosed limit, not a regression this function is expected to close without
// becoming a hardwrap primitive.
func TestWrapPlainAcceptsAResidueWhenAHyphenTokenCannotFitEitherWay(t *testing.T) {
	const s = "see (app/actions.go:148-351) for the switch"
	const width = 23

	raw := strings.Split(ansi.Wordwrap(s, width, ""), "\n")
	rawOverwide := false
	for _, l := range raw {
		if ansi.StringWidth(l) > width {
			rawOverwide = true
		}
	}
	if !rawOverwide {
		t.Fatalf("ansi.Wordwrap(%q, %d, \"\") never exceeds %d cells -- this fixture no longer reaches the bug at all", s, width, width)
	}

	got := wrapPlain(s, width)
	overwide := false
	for _, l := range got {
		if ansi.StringWidth(l) > width {
			overwide = true
		}
	}
	if !overwide {
		t.Fatalf("wrapPlain(%q, %d) = %q, no longer over budget -- the residue this test exists to characterize has closed; update or remove this test rather than leave it silently vacuous", s, width, got)
	}
	if have, want := dropSpacesAndNewlines(strings.Join(got, "\n")), dropSpacesAndNewlines(s); have != want {
		t.Fatalf("wrapPlain(%q, %d) lost or reordered content even while over budget: have %q, want %q", s, width, have, want)
	}
}

// TestWrapPlainCatchesTwoHyphenRetryDefects pins two defects the hyphen fix
// introduced, neither of which `make check` could see: every ungated fixture
// in this file was free of both an NBSP-joined hyphenated compound and a
// hyphenated token wider than its own budget, and every corpus over-width row
// lives at budgets <= 50, well under the widths this file's other fixtures are
// driven at. The NBSP case below runs at width 36; the wide-token case runs at
// width 35.
//
// EACH CASE IS PROVEN NON-VACUOUS AGAINST THE SPECIFIC FIX IT PINS, BY
// MUTATION IN A DETACHED WORKTREE. Reverting ONLY protectHyphens' tokenizer
// predicate (back to bare unicode.IsSpace) reddens the NBSP case alone:
// wrapPlain at width 36 rises from 33 cells to 42. Reverting ONLY the retry
// loop's re-wrap-and-compare guard reddens the wide-token case alone: it rises
// from the accepted 36-cell residue to 45. Neither mutation moves the other
// case.
func TestWrapPlainCatchesTwoHyphenRetryDefects(t *testing.T) {
	t.Run("an NBSP-joined hyphenated compound", func(t *testing.T) {
		// Joined at the one seam that matters by a REAL non-breaking space
		// (U+00A0) rather than an ordinary one: "cache" and "plus-the-..."
		// are two SEPARATE whitespace-delimited tokens to protectHyphens' old,
		// bare-unicode.IsSpace tokenizer -- each individually short enough to
		// protect every hyphen in -- but ONE token to ansi.Wordwrap, which does
		// not treat NBSP as a break.
		const width = 36
		s := "Look at the multi-tenant-routing-layer-and-cache plus-the-request-shaping-and-retry-budget for details."
		if !strings.ContainsRune(s, ' ') {
			t.Fatal("fixture assumption broken: no NBSP in the fixture")
		}

		raw := strings.Split(ansi.Wordwrap(s, width, ""), "\n")
		for _, l := range raw {
			if w := ansi.StringWidth(l); w > width {
				t.Fatalf("ansi.Wordwrap(%q, %d, \"\") itself exceeds width (%d cells) -- this fixture is no longer bare-clean, so it cannot isolate the NBSP defect from ansi.Wordwrap's own pre-existing bug", s, width, w)
			}
		}

		got := wrapPlain(s, width)
		for i, l := range got {
			if w := ansi.StringWidth(l); w > width {
				t.Fatalf("wrapPlain(%q, %d) line %d is %d cells, over budget -- an NBSP-joined compound removed every break opportunity in the line: %q", s, width, i, w, got)
			}
		}
		if have, want := dropSpacesAndNewlines(strings.Join(got, "\n")), dropSpacesAndNewlines(s); have != want {
			t.Fatalf("wrapPlain(%q, %d) lost or reordered content: have %q, want %q", s, width, have, want)
		}
	})

	t.Run("a hyphenated token wider than its own budget", func(t *testing.T) {
		// A block as this package sees it -- ParseBlocks leaves markdown's
		// inline `**`/`` ` `` markers as literal characters in Block.Text,
		// which is what wrapPlain's call sites are actually handed. It
		// carries exactly one hyphen inside a run with no other break in it,
		// wide enough that protectHyphens' own guard never protects it on
		// the first pass -- so the retry loop is what has to hold the line.
		//
		// TOLERANCE, NOT EXACT WIDTH, AND THAT IS DELIBERATE: this block still
		// carries the pre-existing ansi.Wordwrap "+1" bug
		// TestWrapPlainAcceptsAResidueWhenAHyphenTokenCannotFitEitherWay
		// characterizes rather than closes, which at this width lands wrapPlain
		// one cell over budget on its own. What the guard closes is the RETRY
		// making that +1 into something far larger, not the +1 itself.
		const width = 35
		const tolerance = 1
		s := "**True of the station firmware as of the spring rollout.** *(This read \"half true as of the winter rollout\" until then, and named exactly what was missing: a write that stores a reading and its calibration in one transaction, and a field vocabulary for naming an existing reading by id. Both now exist -- see the rollout paragraph above for where.)* The keep-the-last-reading half is `firmware.KeepsLatest` (`stationnet-fw/sampler/sampler_reading.go`), **one decision with five answers as of the summer rollout** -- first reading, create-then-read, re-read, nothing to do, and a sampler holding nothing at all -- asked by both radios before either acts and by `sampler.Read` itself rather than restated in its own guard. `uplink.Retransmit` (`stationnet-fw/radio/uplink/retransmit.go`) tears the buffer down by reusing `queue.Drain`'s own ordered three-phase pass, so the exit is the same code the flush exit runs. The old stated limit, `sampler.ErrReadOnStaleClock`, is deleted."
		if n := len(hyphenatedCompoundPattern.FindAllStringIndex(s, -1)); n < 5 {
			t.Fatalf("fixture carries only %d letter-hyphen-letter occurrence(s), want several: %q", n, s)
		}

		got := wrapPlain(s, width)
		for i, l := range got {
			if w := ansi.StringWidth(l); w > width+tolerance {
				t.Fatalf("wrapPlain(%q, %d) line %d is %d cells, more than %d over budget -- the retry destroyed the only break opportunity in a token wider than the line and ansi.Wordwrap placed it whole: %q", s, width, i, w, tolerance, got)
			}
		}
		if have, want := dropSpacesAndNewlines(strings.Join(got, "\n")), dropSpacesAndNewlines(s); have != want {
			t.Fatalf("wrapPlain(%q, %d) lost or reordered content: have %q, want %q", s, width, have, want)
		}
	})
}

// TestWrapPlainFallsBackWhenTheSentinelIsAlreadyInTheInput: hyphenSentinel must
// be asserted absent from the input rather than assumed absent, and wrapPlain
// must fall back to ansi.Wordwrap's own behaviour -- hyphen bug included --
// rather than risk mistaking the caller's own U+E000 for one it inserted and
// mangling it on the way back out.
//
// PROVEN NON-VACUOUS TWO WAYS: the fixture is asserted to actually contain the
// sentinel, and the width is chosen so protectHyphens, if it ran anyway, would
// rewrite the fixture's own hyphen -- so a wrapPlain that forgot the guard
// would disagree with raw ansi.Wordwrap here.
func TestWrapPlainFallsBackWhenTheSentinelIsAlreadyInTheInput(t *testing.T) {
	const width = 15
	s := "hand-placed " + string(hyphenSentinel) + " newlines"
	if !strings.ContainsRune(s, hyphenSentinel) {
		t.Fatalf("fixture %q does not contain the sentinel -- this test proves nothing", s)
	}
	want := strings.Split(ansi.Wordwrap(s, width, ""), "\n")
	if got := wrapPlain(s, width); !slices.Equal(got, want) {
		t.Fatalf("wrapPlain(%q, %d) = %q, want exactly today's ansi.Wordwrap behaviour %q -- the sentinel guard did not engage", s, width, got, want)
	}
}

// TestWrapPlainHandlesRealStyledInput: every one of wrapPlain's four call sites
// can hand it text that already carries real SGR styling, and the fixtures this
// function shipped with were all plain, unstyled strings. This drives two
// adjacent lipgloss Render() calls, so the raw bytes carry an SGR reset
// immediately after the hyphen with ZERO visible characters in between.
//
// DISCRIMINATING, NOT DECORATIVE, two ways:
//
//   - Case A's token is 83 raw bytes and 11 visible cells. If protectHyphens
//     measured len(tok) instead of ansi.StringWidth(tok), it would judge this
//     token far too wide to protect and let ansi.Wordwrap split it at the
//     hyphen regardless of the line having room -- so this fails exactly when
//     the width measurement stops being ANSI-aware.
//   - Case B forces the retry path with the styled hyphen sitting where a naive
//     check of the row's RAW (unstripped) trailing byte could land on styling
//     instead of content. It establishes that the retry path is REACHED with a
//     real styled hyphen at that seam and produces content-preserving output;
//     it does NOT establish that ansi.Strip is why -- reverting that call left
//     this test, and the entire ui suite, green.
//
// Both cases also assert content is preserved once ANSI is stripped: the
// embedded styling has to survive the sentinel round-trip byte for byte.
func TestWrapPlainHandlesRealStyledInput(t *testing.T) {
	st := darkStyles(t)
	// Two Render() calls glued together, hyphen at the seam -- the exact
	// shape a styled compound word takes coming out of b.displayIn.
	word := st.CodeSpan.Render("hand-") + st.Text.Render("placed")
	if raw := ansi.StringWidth(word); raw != 11 {
		t.Fatalf("fixture word %q is %d cells, want 11 -- case A's non-vacuity depends on this", word, raw)
	}
	if len(word) <= ansi.StringWidth(word) {
		t.Fatalf("fixture word %q is not actually styled (byte length %d <= visible width %d) -- case A proves nothing without real SGR bytes in it", word, len(word), ansi.StringWidth(word))
	}

	stripJoin := func(lines []string) string {
		return dropSpacesAndNewlines(ansi.Strip(strings.Join(lines, "\n")))
	}

	t.Run("a styled hyphenated word that fits carries down whole", func(t *testing.T) {
		const width = 11
		s := "abcdef " + word + " newlines"
		got := wrapPlain(s, width)
		want := []string{"abcdef", word, "newlines"}
		if !slices.Equal(got, want) {
			t.Fatalf("wrapPlain(%d) = %q, want %q -- the styled word split even though it fits the line", width, got, want)
		}
		for i, l := range got {
			if w := ansi.StringWidth(l); w > width {
				t.Fatalf("line %d is %d cells wide, over budget: %q", i, w, l)
			}
		}
		if have, want := stripJoin(got), dropSpacesAndNewlines(ansi.Strip(s)); have != want {
			t.Fatalf("content not preserved: have %q, want %q", have, want)
		}
	})

	t.Run("a styled hyphenated word wider than the line never overflows", func(t *testing.T) {
		const width = 14
		s := "xxxxxxxxx " + word
		rawOver := false
		for _, l := range strings.Split(ansi.Wordwrap(s, width, ""), "\n") {
			if ansi.StringWidth(l) > width {
				rawOver = true
			}
		}
		if !rawOver {
			t.Fatalf("ansi.Wordwrap(%d, \"\") on the styled fixture never exceeds %d cells -- this case does not reach the retry path at all", width, width)
		}

		got := wrapPlain(s, width)
		for i, l := range got {
			if w := ansi.StringWidth(l); w > width {
				t.Fatalf("styled line %d is %d cells wide, over budget: %q", i, w, l)
			}
		}
		if have, want := stripJoin(got), dropSpacesAndNewlines(ansi.Strip(s)); have != want {
			t.Fatalf("content not preserved: have %q, want %q", have, want)
		}
	})
}

// TestWrapPlainCommentBodyNeverOverflows drives wrapPlain's comment-body call
// site (inside renderThreadCardPainted), which is reachable ONLY through
// RenderDoc's views parameter AND only when `expanded` also marks the block
// true. onCardAt draws the card's HEADER whenever a block has views; cardBelowAt
// gates the body lines this subject wraps. A views-only drive never reaches the
// arm at all and looks identical to success.
//
// THE CORPUS SWEEP DOES NOT COVER THIS. It walks ~/plans with nil views, so its
// "0 over-width rows" is three of wrapPlain's four call sites and never this
// one -- corpus markdown carries no thread data to populate views with.
//
// NON-VACUITY, ESTABLISHED BY MUTATION: reverting wrapPlain to its pre-fix base
// and re-running this fixture at exactly these four widths overflows at every
// one of them. The four widths are the RenderDoc widths where the card's
// internal wrapPlain budget lands exactly on the bug's own boundary for this
// fixture; neighbouring widths do not trigger it.
func TestWrapPlainCommentBodyNeverOverflows(t *testing.T) {
	st := darkStyles(t)
	blocks := ParseBlocks([]byte("# Heading\n\nSome body text.\n"), st)
	const body = "so gofmt -l -w rewrote nothing more than that plus extra words here to fill lines"
	v := ThreadView{Thread: domain.Thread{Comments: []domain.Comment{{Body: body}}}, Placed: true}
	views := map[int][]ThreadView{0: {v}}
	expanded := map[int]bool{0: true}

	for _, width := range []int{16, 17, 19, 20} {
		t.Run(fmt.Sprintf("width %d", width), func(t *testing.T) {
			lines := RenderDoc(blocks, views, nil, expanded, OnLine(0), width, "cm", st)
			sawCommentRow := false
			for _, l := range lines {
				for _, row := range strings.Split(l.Text, "\n") {
					plain := ansi.Strip(row)
					if strings.Contains(plain, "gofmt") {
						sawCommentRow = true
					}
					if w := ansi.StringWidth(plain); w > width {
						t.Fatalf("row %q is %d cells, over the %d-cell budget -- the comment-body call site overflowed", plain, w, width)
					}
				}
			}
			if !sawCommentRow {
				t.Fatalf("no rendered row at width %d contains the comment body at all -- expanded/views did not reach the card, so this case tests nothing", width)
			}
		})
	}
}

// TestOneUILineCanCarryTwoScreenRows is the OTHER failure mode named at
// TestWideRunOverflowsWrapPlainButNotTheGrid, pinned as characterization: one
// ui.Line whose Text holds a real '\n', which is two screen rows.
//
// IT ASSERTS THE DEFECT, NOT ITS ABSENCE. Its fixture is a long HEADING and not
// a tab in a fence, because the code arm no longer does this at all -- 990
// fenced-code cases went to 0 when the tab was spent at the arm and the row
// wrapped instead of being truncated. What is left is 184 headings (KindHeading
// neither wraps nor truncates, so a heading wider than its row is wrapped by
// lipgloss inside one Line) and 10 wrapPlain rows handed a run with no break
// opportunity. Zero of them comes from a block whose Text holds a C0 or DEL
// byte.
//
// WHY IT MATTERS MORE THAN A RAGGED MARGIN. app/painted.go writes exactly one
// "\n" per ui.Line into the frame, so an embedded newline makes the RENDERED
// FRAME TALLER THAN THE VIEWPORT, and it breaks the line-to-block mapping every
// cursor movement resolves through.
//
// THE ARITHMETIC, all measured at width 80: renderDocPainted hands the block 72
// (rail 2 x 2, gutter 4) and the arm draws '§ ' plus the heading's own words, so
// 71 words-cells is 73 into a 72-cell slot and splits, and 70 is 72 and fills it
// exactly. Both are asserted, so a change to the budget arithmetic fails here
// rather than passing silently on a fixture with slack in it.
//
// PINNED SO A LATER FIX HAS TO SAY SO.
func TestOneUILineCanCarryTwoScreenRows(t *testing.T) {
	st := darkStyles(t)
	const width = 80
	// 71 cells of heading words: 73 with the sigil, into a 72-cell slot.
	src := []byte("# " + strings.Repeat("h", 71) + "\n")
	rows := func(lines []Line) (total, split int) {
		for _, l := range lines {
			n := strings.Count(ansi.Strip(l.Text), "\n")
			total += 1 + n
			if n > 0 {
				split++
			}
		}
		return total, split
	}

	lines := RenderDoc(ParseBlocks(src, st), nil, nil, nil, OnLine(0), width, "cm", st)
	total, split := rows(lines)
	if split != 1 {
		t.Fatalf("%d of %d Lines carry a newline, want exactly 1 -- if this is 0, KindHeading has learned to wrap and the residual this test stands for is smaller than it says", split, len(lines))
	}
	if total != len(lines)+1 {
		t.Fatalf("%d Lines render as %d terminal rows, want %d -- app/painted.go writes one \"\\n\" per Line, so the frame is exactly this much taller than the viewport believes", len(lines), total, len(lines)+1)
	}

	// One cell narrower and it fits, which is what says the fixture is
	// measuring the budget rather than sitting comfortably past it.
	shorter := []byte("# " + strings.Repeat("h", 70) + "\n")
	if _, split := rows(RenderDoc(ParseBlocks(shorter, st), nil, nil, nil, OnLine(0), width, "cm", st)); split != 0 {
		t.Fatalf("the one-cell-narrower fixture also splits (%d Lines), so this test is not measuring the budget it claims to", split)
	}
}

// TestACodeLineIsOneScreenRow is the half of ui.Line's own contract -- "Text is
// ONE SCREEN ROW's worth of styled text" -- that is true today. It is the CODE
// half and not the whole, deliberately: the heading arm and wrapPlain still
// split, 194 Lines' worth over a real corpus.
//
// THE MECHANISM IS THE MEASURE AND NOT THE LAYOUT, which is why the fixtures
// below are tabs rather than long lines. A tab is 0 cells to ansi.StringWidth
// and four to lipgloss, so the arm believed the row fit and renderDocPainted's
// Width().Render found it did not and wrapped it there -- inside the single
// string that becomes one Line. WRAPPING WITHOUT FIXING THE MEASURE FIXES
// NOTHING: the row still lays out short and still overflows, which is why
// fourSpaceTabs and wrapCodeRow landed in one change.
//
// AND IT DRIVES THE BUDGET, not slack: at width 80 the arm is handed 70 and
// wraps at 68 (its own 2-column indent), so one tab and 65 x's is exactly one
// cell over and 64 comes back as one row. THE PAIR STRADDLES THE BUDGET BY ONE
// CELL AND THAT IS THE WHOLE FIXTURE -- a pair that no longer sits on the
// boundary tests nothing.
func TestACodeLineIsOneScreenRow(t *testing.T) {
	st := darkStyles(t)
	const width = 80
	for _, tc := range []struct {
		name string
		row  string
		want int // screen rows the fence's one source line is drawn as
	}{
		{"a tab the arm used to measure as nothing", "\t" + strings.Repeat("x", 65), 2},
		{"one cell narrower, and it fits", "\t" + strings.Repeat("x", 64), 1},
		{"tabs all the way past the budget", strings.Repeat("\t", 6) + strings.Repeat("x", 200), 5},
		{"no tab at all, simply too wide", strings.Repeat("x", 200), 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte("```\n" + tc.row + "\n```\n")
			blocks := ParseBlocks(src, st)
			lines := RenderDoc(blocks, nil, nil, nil, OnLine(0), width, "cm", st)
			split, drawn := 0, 0
			for _, l := range lines {
				if l.BlockIdx < 0 || l.BlockIdx >= len(blocks) || blocks[l.BlockIdx].Kind != KindCode {
					continue
				}
				drawn++
				if strings.Contains(ansi.Strip(l.Text), "\n") {
					split++
				}
			}
			if split != 0 {
				t.Fatalf("%d of %d Lines from a code block carries an embedded newline -- that is two screen rows inside one ui.Line, which makes the frame taller than the viewport and puts two rows on one BlockIdx", split, drawn)
			}
			// drawn counts the blank spacer row renderDocPainted appends
			// after every block, which is a Line of the block's and not a row
			// of the arm's.
			if drawn-1 != tc.want {
				t.Fatalf("the arm drew %d rows for one source line, want %d -- if this is 1 the row is not being wrapped at all and the case below it is what would have caught that", drawn-1, tc.want)
			}
		})
	}
}

// TestCodeWrapsAndKeepsEveryByte is the ruling stated as an assertion:
// ansi.Truncate does not decide what a reviewer may read out of a code block.
// The arm used to cut 2,105 of 14,413 corpus code rows and leave 41,547 runes
// behind an ellipsis; it now draws 17,076 rows for the same 14,413 (+18.5%) and
// loses nothing.
//
// THE TWO LEVELS ARE DIFFERENT ASSERTIONS AND NEITHER ALONE IS THE PROPERTY.
// wrapCodeRow's pieces must rejoin to the source row, which is what says no
// interior space was eaten; the frame comparison below is space-insensitive,
// because the arm's own indent and the hanging indent are both spaces and both
// deliberate, and is what says the pieces actually reached a screen with no
// ellipsis in between.
//
// AND THE REJOIN IS BYTE-EXACT ONLY WHERE IT CAN BE. ansi.Truncate and
// ansi.TruncateLeft CLOSE an open escape sequence at the cut and REOPEN it on
// the next piece, so a row carrying `\x1b[31m` rejoins with that sequence
// duplicated -- content preserved, bytes not identical. Every escape-free row
// rejoins byte for byte, and so does every row carrying a bare C0. Both
// spellings are driven below.
func TestCodeWrapsAndKeepsEveryByte(t *testing.T) {
	st := darkStyles(t)
	const width = 80
	for _, tc := range []struct {
		name string
		row  string
	}{
		{"a line one cell over the budget", strings.Repeat("x", 71)},
		{"a line with interior spaces the wrap must not eat", strings.Repeat("word ", 40)},
		{"a deeply indented line", strings.Repeat(" ", 8) + strings.Repeat("y", 192)},
		{"a tab-indented line", "\t\t" + strings.Repeat("z", 150)},
		{"a run of double-width glyphs", strings.Repeat("承", 80)},
		{"a line that already fits", "package ui"},
		{"the four C0 bytes that survive reanchor.Normalize", strings.Repeat("n", 40) + "\x00\x07\x08\x7f" + strings.Repeat("m", 60)},
		{"an SGR run, which is reopened rather than split", "\x1b[31m" + strings.Repeat("x", 120) + "\x1b[0m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The helper's own contract: the pieces ARE the source row --
			// byte for byte where no escape sequence is reopened across the
			// break, and ansi.Strip-for-ansi.Strip where one is.
			row := fourSpaceTabs(tc.row)
			pieces, lead := wrapCodeRow(row, 70)
			joined := strings.Join(pieces, "")
			if ansi.Strip(joined) != ansi.Strip(row) {
				t.Fatalf("wrapCodeRow's %d pieces rejoin to %q, want the row it was handed -- a wrap that cannot be rejoined has lost or invented content", len(pieces), joined)
			}
			if !strings.Contains(row, "\x1b") && joined != row {
				t.Fatalf("wrapCodeRow was not byte-exact on an escape-free row: %q against %q -- only a reopened escape sequence may make the rejoin differ", joined, row)
			}
			for i, p := range pieces {
				if i > 0 {
					p = lead + p
				}
				if w := ansi.StringWidth(p); w > 70 {
					t.Fatalf("piece %d is %d cells against a 70-cell budget -- a row wider than the column it is painted into is what lipgloss wraps, which is the defect this replaced", i, w)
				}
			}

			// And the frame: every rune of the source reaches a screen row.
			src := []byte("```\n" + tc.row + "\n```\n")
			blocks := ParseBlocks(src, st)
			var got strings.Builder
			// NoCursor and not OnLine(0): the cursor glyph is drawn in the
			// gutter of every row of the focused block, and it is not a space,
			// so it would count as content this comparison did not ask for.
			for _, l := range RenderDoc(blocks, nil, nil, nil, OnLine(NoCursor), width, "cm", st) {
				if l.BlockIdx >= 0 && l.BlockIdx < len(blocks) && blocks[l.BlockIdx].Kind == KindCode {
					got.WriteString(ansi.Strip(l.Text))
				}
			}
			// Space-insensitive because the arm's own 2-column indent, the
			// hanging indent and the row's background pad are all spaces
			// and all deliberate. Nothing else the arm draws is.
			// ansi.Strip on BOTH sides because the rows come back styled.
			//
			// AND THE EXPECTED SIDE IS THE ROW AS THE CONTROL-BYTE SUBSTITUTION
			// DRAWS IT, so the expectation goes through the same
			// visibleControls the arm does -- with a zero style, so ansi.Strip
			// leaves the GLYPH and takes the styling back off.
			bare := func(s string) string {
				return strings.NewReplacer(" ", "", "\t", "", "\n", "").Replace(ansi.Strip(s))
			}
			want := bare(visibleControls(row, lipgloss.Style{}))
			if bare(got.String()) != want {
				t.Fatalf("the screen is missing content: %d runes of source did not reach it, and the row it drew was %q", len([]rune(want))-len([]rune(bare(got.String()))), got.String())
			}
		})
	}
}

// TestACodeContinuationHangsUnderItsOwnIndent is a correctness detail rather
// than a preference: a continuation drawn at the left margin under a line
// indented four levels appears TO THE LEFT of the statement it continues and
// reads as a dedent. A wrap that lies about the code's structure is the same
// class of failure as the truncation it replaced, which lied about its content.
//
// tableContinuationCols IS REUSED AND NOT TWINNED, which is what this asserts:
// a wrapped code row, a wrapped table value and a wrapped list item all hang
// their continuations in the same column relative to what they continue. The
// indent's own cost is 24 rows over a real corpus, 0.14%, against the wrap's
// +18.5%.
func TestACodeContinuationHangsUnderItsOwnIndent(t *testing.T) {
	st := darkStyles(t)
	// Eight columns of indent and a run far past the budget, so the first row
	// fills the budget exactly and there is a continuation to look at.
	const indent = 8
	src := []byte("```\n" + strings.Repeat(" ", indent) + strings.Repeat("y", 192) + "\n```\n")
	var b Block
	for _, blk := range ParseBlocks(src, st) {
		if blk.Kind == KindCode {
			b = blk
		}
	}
	rows := renderBlockPainted(b, 72, st, false)
	if len(rows) < 3 {
		t.Fatalf("the fixture drew %d rows, want at least 3 -- with one continuation there is nothing here to measure", len(rows))
	}
	starts := make([]int, len(rows))
	for i, r := range rows {
		plain := ansi.Strip(r)
		starts[i] = len(plain) - len(strings.TrimLeft(plain, " "))
	}
	// The arm's own 2-column code indent plus the row's own 8.
	if starts[0] != 2+indent {
		t.Fatalf("the first row's content starts at column %d, want %d -- the arm draws its own 2-column indent and then the row's own %d", starts[0], 2+indent, indent)
	}
	for i := 1; i < len(rows); i++ {
		if starts[i] != starts[0]+tableContinuationCols {
			t.Fatalf("continuation %d starts at column %d, want %d -- a continuation to the LEFT of the line it continues reads as a dedent, which is the wrap lying about the code's structure", i, starts[i], starts[0]+tableContinuationCols)
		}
	}
}

// TestTheCraftedDocumentHoldsNoControlSequenceAtAnyWidth drives
// ui/testdata/hostile.md -- a COMMITTED document of real control bytes and real
// escape introducers -- through the real RenderDoc at every width from 20 to
// 200 and requires the frame invariant of the result.
//
// A COMMITTED FIXTURE IS THE POINT AND NOT THE CONVENIENCE. Two of the three
// defects this fixture exercises have no benign corpus witness, so a sweep of
// somebody's ~/plans can only ever say "not today"; and a width count quoted
// without its payload is not a fact, because the number of widths at which a
// cell wrap cuts an escape is a function of that escape's VISIBLE width. This
// fixture is the input that does not move.
//
// WHAT IT PINS. A source-supplied introducer reaches a Line at 0 of 181 widths,
// in a body cell and in a header cell alike, so there is no sequence left to
// split -- and the MUTATION says so rather than the reasoning: with
// isVisibleControl amended to exempt 0x1b, the introducer reaches a body-cell
// Line at 181 of 181 widths and the APC payload is CUT ACROSS TWO LINES at 44
// of them.
//
// AND THE SECOND IS THE DISCARDED TAIL, the one span in this channel that is
// not renderInlines output: the fixture's malformed row carries a sentinel past
// the header's column count, GFM throws those bytes away before the AST exists,
// and tableRowCells appends them to the last cell raw. The sentinel is asserted
// ON SCREEN so that a repair which dropped the tail instead of filtering it
// fails here.
//
// THE FIXTURE'S OWN BYTES ARE CHECKED FIRST, because the failure mode of a file
// like this is an editor or a formatter that silently cleans it up -- at which
// point every assertion below passes and proves nothing at all.
//
// IT DOES NOT ASSERT ONE LINE PER SCREEN ROW, and that is a deliberate omission
// rather than a gap: this fixture's own headings are longer than 20 columns, so
// an assertion here would restate the heading arm's known wrapping residual
// under a security test's name. TestACodeLineIsOneScreenRow is where the
// property this closed is pinned.
func TestTheCraftedDocumentHoldsNoControlSequenceAtAnyWidth(t *testing.T) {
	src, err := os.ReadFile("testdata/hostile.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct{ name, bytes string }{
		{"the APC introducer", "\x1b_"},
		{"the OSC introducer", "\x1b]"},
		{"the DCS introducer", "\x1bP"},
		{"the SOS introducer", "\x1bX"},
		{"the PM introducer", "\x1b^"},
		{"a backspace", "\b"},
		{"a carriage return", "\r"},
		{"a DEL", "\x7f"},
		{"a tab inside the indented fence", "\t"},
		{"the discarded tail's sentinel", "ZQTAILZQ"},
	} {
		if !bytes.Contains(src, []byte(want.bytes)) {
			t.Fatalf("testdata/hostile.md no longer holds %s (%q) -- something cleaned the fixture up, and every assertion below would pass vacuously", want.name, want.bytes)
		}
	}
	st := darkStyles(t)
	blocks := ParseBlocks(src, st)
	rows := 0
	for _, b := range blocks {
		if b.Kind == KindTableRow {
			rows++
		}
	}
	if rows < 9 {
		t.Fatalf("the fixture parses to %d table rows, want at least 9 -- its two tables and its malformed row are what the grid assertions are about", rows)
	}
	introducers, cut, checked := 0, 0, 0
	for width := 20; width <= 200; width++ {
		lines := RenderDoc(blocks, nil, nil, nil, OnLine(0), width, "cm", st)
		var frame strings.Builder
		for _, l := range lines {
			frame.WriteString(l.Text)
			frame.WriteByte('\n')
		}
		f := frame.String()
		checked++
		if v := FrameViolations(f); len(v) != 0 {
			t.Fatalf("width %d: the frame carries %d thing(s) a benign frame must not:\n  %v", width, len(v), v)
		}
		if csi := FrameCSICount(f); csi == 0 {
			t.Fatalf("width %d: the frame carries no CSI at all, so it is not a styled frame and the assertion above proves nothing", width)
		}
		for _, intro := range []string{"\x1b_", "\x1b]", "\x1bP", "\x1bX", "\x1b^"} {
			if strings.Contains(f, intro) {
				introducers++
			}
		}
		// THE CUT, asked directly rather than inferred from the violation
		// count: an introducer present in the frame but not intact on any one
		// Line is the split this test's own doc comment is about.
		for _, l := range lines {
			if k := strings.Index(l.Text, "\x1b_"); k >= 0 && !strings.Contains(l.Text, "\x1b_payload that never ends") {
				cut++
				break
			}
		}
		// AND THE BYTES ARE STILL ON SCREEN, which is the control-byte
		// substitution rather than a strip: a reviewer approves bytes, so
		// what a control byte becomes has to be visible and not absent.
		for _, glyph := range []string{"␛", "␈", "␍", "␡"} {
			if !strings.Contains(f, glyph) {
				t.Fatalf("width %d: %q is nowhere on screen -- the control-byte substitution VISUALISES a control byte and a strip would show clean text over different bytes", width, glyph)
			}
		}
		// FROM WIDTH 24 UP, and the floor is measured rather than guessed: a
		// grid's columns are clamped to the budget it is handed, and below the
		// floor the column holding the tail is narrower than the 8-character
		// sentinel, so what is missing there is the grid's own truncation and
		// not this span. 24 is the narrowest width at which the sentinel
		// appears, and it appears at every width above it.
		//
		// It was 22 before DocRightMargin took two cells of document ground off
		// the body budget. Recorded rather than absorbed: at widths 22 and 23
		// this view now stops showing bytes that are in the reviewer's own
		// file.
		if width >= 24 && !strings.Contains(ansi.Strip(f), "ZQTAILZQ") {
			t.Fatalf("width %d: the discarded tail's sentinel never reaches the screen -- those bytes are in the reviewer's own file and this view must not be the one place they are missing", width)
		}
	}
	if checked != 181 {
		t.Fatalf("the sweep drove %d widths, want 181", checked)
	}
	if introducers != 0 || cut != 0 {
		t.Fatalf("a source-supplied introducer reached a Line at %d widths and was cut across two at %d, want 0 and 0", introducers, cut)
	}
}

// TestAnIndentedFenceDrawsItsContentAndNotItsContainersIndent is the
// fence-indent leak ui/testdata/hostile.md exercises, and it is a defect in
// Block.Text's READERS rather than in Text.
//
// THE MECHANISM. Block.Text is the span from a node's FIRST line segment's
// start to its LAST one's stop. goldmark removes the fence's own indentation
// from every content line's segment -- CommonMark's rule for an indented fence,
// and for a fence inside a list item the indentation is the item's content
// column -- but the SPAN between the first segment and the last runs straight
// over the indentation of every line in between. So a fence indented two
// columns comes out with its first line flush and every line after it two
// columns further right: a code block whose structure is a lie about the
// source. Measured over a real corpus, 8 blocks / 44 rows.
//
// THE FIX IS AT THE ARM AND Block.CodeIndent IS WHAT CARRIES IT, for the reason
// fourSpaceTabs and the control-byte filter are both at the arm: Text is the
// anchor side of the source mapping and reanchor searches the raw document for
// it verbatim, so a span with its indentation removed is a range nothing could
// find again.
//
// A QUOTED FENCE IS DELIBERATELY UNTOUCHED, which is the fourth case here and
// is Chesterton's fence rather than an oversight. Its continuation lines carry
// the source's own "> " markers, which is recorded at Block.Text with what it
// would cost to fix -- which marker, at which depth, in a fence that may
// contain a real '>' -- and no measurement in reach settles it. What settles
// THIS one is that the column count is the source's own and the bytes removed
// are whitespace.
func TestAnIndentedFenceDrawsItsContentAndNotItsContainersIndent(t *testing.T) {
	st := darkStyles(t)
	for _, tc := range []struct {
		name   string
		src    string
		indent int
		want   []string
	}{
		{
			"a fence inside a list item -- the corpus's own shape",
			"- an item\n\n  ```\n  first\n  second\n  ```\n",
			2,
			[]string{"first", "second"},
		},
		{
			"and the code's OWN indentation survives it",
			"- an item\n\n  ```\n  func main() {\n      println()\n  }\n  ```\n",
			2,
			[]string{"func main() {", "    println()", "}"},
		},
		{
			"a fence indented three columns at the margin",
			"   ```\n   first\n   second\n   ```\n",
			3,
			[]string{"first", "second"},
		},
		{
			"a fence at the margin is unchanged, indentation and all",
			"```\nfirst\n  second\n```\n",
			0,
			[]string{"first", "  second"},
		},
		{
			"a QUOTED fence still draws its markers -- recorded at Block.Text, not fixed",
			"> ```\n> first\n> second\n> ```\n",
			0,
			[]string{"first", "> second"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b Block
			found := false
			for _, blk := range ParseBlocks([]byte(tc.src), st) {
				if blk.Kind == KindCode {
					b, found = blk, true
				}
			}
			if !found {
				t.Fatalf("the fixture parses to no KindCode block, so this case drives the wrong arm")
			}
			if b.CodeIndent != tc.indent {
				t.Fatalf("CodeIndent = %d, want %d -- the count is the source's own and everything below rests on it", b.CodeIndent, tc.indent)
			}
			// THE SPAN IS UNTOUCHED, asserted before the rows: a fix that
			// reached Text would satisfy every row assertion below and break
			// every anchor into the block. The continuation line is still in
			// Text with the container's indentation in front of it, which is
			// the byte range reanchor searches the raw document for.
			if tc.indent > 0 {
				if lead := "\n" + strings.Repeat(" ", tc.indent) + tc.want[1]; !strings.Contains(b.Text, lead) {
					t.Fatalf("Block.Text = %q no longer carries %q -- the span must keep the source's own indentation", b.Text, lead)
				}
			}
			rows := renderBlockPainted(b, 120, st, false)
			got := make([]string, len(rows))
			for i, r := range rows {
				got[i] = strings.TrimPrefix(ansi.Strip(r), "  ")
			}
			if len(got) != len(tc.want) {
				t.Fatalf("the arm drew %d rows %q, want %d %q", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("row %d = %q, want %q -- the whole render was %q", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// TestTheRawArmsSpendFourColumnsOnATab pins where the four-column tab applies:
// the two arms that draw Block.Text raw spell a tab the way the four
// PROJECTION arms already spelled it, and the way the renderer underneath was
// always going to paint it. So it is not a new convention -- what was missing
// was the arm AGREEING with both, which is what let ansi.Truncate spend none
// of its budget on a byte the screen spends four columns on.
//
// AND IT DOES NOT TOUCH Block.Text, which is the assertion that keeps anchors
// alive: reanchor searches the raw document for Text verbatim, so a tab folded
// INTO Text would be a range nothing could find again. Four spaces are safe at
// the arm for the same reason from the other side -- reanchor.Normalize folds
// runs of whitespace, so four spaces move 0 of 13,449 anchorable spans where
// U+2409 moves 175.
func TestTheRawArmsSpendFourColumnsOnATab(t *testing.T) {
	st := darkStyles(t)
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{"heading, a raw arm", "# a\tb\n", "§ a    b"},
		{"fenced code, the other raw arm", "```\na\tb\n```\n", "  a    b"},
		{"paragraph, which already agreed", "a\tb\n", "a    b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocks := ParseBlocks([]byte(tc.src), st)
			if len(blocks) != 1 {
				t.Fatalf("the fixture parsed to %d blocks, want 1", len(blocks))
			}
			if !strings.Contains(blocks[0].Text, "\t") {
				t.Fatalf("Block.Text is %q and holds no tab -- the arm must not fold one into the anchor side of the mapping", blocks[0].Text)
			}
			rows := renderBlockPainted(blocks[0], 72, st, false)
			if got := strings.TrimRight(ansi.Strip(rows[0]), " "); got != tc.want {
				t.Fatalf("the arm drew %q, want %q -- one tab is four columns on every arm or it is a different width on two of them", got, tc.want)
			}
		})
	}
}

// controlArms is one document shape per arm renderBlockPainted has, each with
// a single `%s` for the payload. SEVEN: four arms draw a projection and are
// reached by the filter at the inline leaf, two draw Block.Text raw and carry
// the filter at the arm, and the seventh is displayIn's fallback, which is
// neither -- an HTML block has no projection at all, so its raw source arrives
// through reanchor.Normalize and the filter sits inside displayIn.
//
// THE FALLBACK ROW IS THE ONE WITH NO CORPUS WITNESS: 2,570 of 14,611 corpus
// blocks take displayIn's fallback BRANCH and 0 of them are drawn through it --
// every one is a heading, a fence or a rule, and none of those three arms asks
// displayIn anything. So the arm is live code the corpus cannot reach, which is
// exactly why it is a committed fixture here.
var controlArms = []struct {
	name, src string
	// styled is whether the reverse video reaches the screen on this arm.
	// SIX OF SEVEN, and the exception is measured rather than assumed -- see
	// TestAControlByteIsNotItsOwnGlyph.
	styled bool
}{
	{"heading -- a raw arm", "# %s\n", true},
	{"paragraph -- the leaf", "%s\n", true},
	{"list item -- the leaf", "- %s\n", true},
	{"table header cell -- the leaf", "| %s | note |\n|---|---|\n| a | b |\n", false},
	{"table body cell -- the leaf", "| step | note |\n|---|---|\n| %s | b |\n", true},
	{"fenced code -- the other raw arm", "```\n%s\n```\n", true},
	{"the fallback arm -- neither", "<div>\n%s\n</div>\n", true},
}

// controlArmRows renders one arm's fixture and returns its non-blank rows,
// styled, plus the blocks it parsed to.
func controlArmRows(t *testing.T, st *Styles, shape, payload string, width int) ([]string, []Block) {
	t.Helper()
	blocks := ParseBlocks([]byte(fmt.Sprintf(shape, payload)), st)
	var out []string
	for _, l := range RenderDoc(blocks, nil, nil, nil, OnLine(NoCursor), width, "cm", st) {
		if strings.TrimSpace(ansi.Strip(l.Text)) != "" {
			out = append(out, l.Text)
		}
	}
	if len(out) == 0 {
		t.Fatalf("the fixture drew no rows at all: %q", fmt.Sprintf(shape, payload))
	}
	return out, blocks
}

// hasReverseVideo answers whether s carries SGR 7 anywhere, by reading the
// PARAMETERS of each CSI ... m rather than looking for a substring. `\x1b[7`
// as a substring is not the same question: lipgloss emits the reverse
// parameter first on a plain style and second under a bold one
// (`\x1b[1;7;38;2;…m`), and a 24-bit colour's own digits are full of sevens.
func hasReverseVideo(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] != 0x1b || s[i+1] != '[' {
			continue
		}
		j := i + 2
		for j < len(s) && (s[j] == ';' || (s[j] >= '0' && s[j] <= '9')) {
			j++
		}
		if j >= len(s) || s[j] != 'm' {
			continue
		}
		for _, p := range strings.Split(s[i+2:j], ";") {
			if p == "7" {
				return true
			}
		}
	}
	return false
}

// TestEveryArmDrawsAControlByteAsItsGlyph is the control-byte substitution and
// the leaf filter as one assertion, driven end to end on every arm rather than
// read off the code.
//
// THE TWO PAYLOADS NEED NO ESCAPE SEQUENCE, which is the whole reason the
// predicate is C0-and-DEL and not CSI-shaped:
//
//	source                                            what the reviewer read
//	Requires approval\b…\bNo approval needed          No approval needed
//	We will NOT rotate the production keys.\rWe will…  We will rotate them  tion keys.
//
// Every emulator honours CR and BS with no permission asked for, and what they
// falsify is the product's own definition of an approval.
//
// WHAT IT ASSERTS PER ARM: the byte itself must not reach a ui.Line; its
// Control Picture must; the words on BOTH sides of the byte must still be
// there, so a filter that fixed the forgery by dropping its payload fails too;
// and Block.Text must still hold the raw byte, because Text is the anchor side
// of the mapping.
//
// IT DRIVES THE WHOLE RENDERER and not renderBlockPainted, because a table
// header cell reaches the screen through DisplayPlain and a body cell through
// Display, a list item and a paragraph through displayIn, and the fallback arm
// through neither. Four different fields, one document each.
func TestEveryArmDrawsAControlByteAsItsGlyph(t *testing.T) {
	st := darkStyles(t)
	const width = 100
	for _, p := range []struct{ name, byteStr, left, right string }{
		{"BS", "\b", "Requires approval", "No approval needed"},
		{"CR", "\r", "We will NOT rotate the production keys.", "We will rotate them"},
	} {
		for _, arm := range controlArms {
			t.Run(p.name+"/"+arm.name, func(t *testing.T) {
				payload := p.left + p.byteStr + p.right
				rows, blocks := controlArmRows(t, st, arm.src, payload, width)
				joined := strings.Join(rows, "\n")
				plain := ansi.Strip(joined)
				if strings.Contains(plain, p.byteStr) {
					t.Fatalf("the byte %q reached the screen unfiltered on this arm: %q -- a reviewer approves BYTES, and this one rewrites the line they are approving", p.byteStr, plain)
				}
				glyph := string(controlPicture(p.byteStr[0]))
				if !strings.Contains(plain, glyph) {
					t.Fatalf("the byte %q is gone from the screen and %q is not there either: %q -- the control-byte substitution is VISUALISE and not strip, because a strip shows clean text while the hash covers something else", p.byteStr, glyph, plain)
				}
				for _, half := range []string{p.left, p.right} {
					if !strings.Contains(plain, half) {
						t.Fatalf("%q is missing from the screen: %q -- the filter must make the byte visible, not take the words with it", half, plain)
					}
				}
				var raw int
				for _, b := range blocks {
					if strings.Contains(b.Text, p.byteStr) {
						raw++
					}
				}
				if raw == 0 {
					t.Fatalf("no Block.Text holds %q any more -- Text is the anchor side of the mapping and reanchor.CreateAnchor searches the RAW document for it, so a filtered Text makes the block uncommentable (`span not found in document`) and, on a heading, takes every benign block beneath it with it", p.byteStr)
				}
			})
		}
	}
}

// TestEveryArmStripsSelector16 drives the U+FE0F strip over the same seven-arm
// fixture set TestEveryArmDrawsAControlByteAsItsGlyph uses: controlArms is
// already the fixture that answers which expressions the control-byte filter
// reaches, so this reuses it rather than building a second table of the same
// seven shapes.
//
// The base character must survive on screen; only the selector may go -- see
// ui/vs16.go for why the bare base is the one region every ruler and every
// terminal measured agrees on.
func TestEveryArmStripsSelector16(t *testing.T) {
	st := darkStyles(t)
	const width = 100
	const withSelector = "warning⚠️sign"
	const bareBase = "warning⚠sign"
	for _, arm := range controlArms {
		t.Run(arm.name, func(t *testing.T) {
			rows, blocks := controlArmRows(t, st, arm.src, withSelector, width)
			plain := ansi.Strip(strings.Join(rows, "\n"))
			if strings.Contains(plain, "️") {
				t.Fatalf("U+FE0F reached the screen on this arm: %q -- that is the one class of disagreement Zellij and GNU screen have with our own rulers, and it only closes if the selector never arrives", plain)
			}
			if !strings.Contains(plain, bareBase) {
				t.Fatalf("the base character is gone from the screen along with the selector: %q -- stripping the selector must not take the character it modifies with it", plain)
			}
			var raw int
			for _, b := range blocks {
				if strings.Contains(b.Text, withSelector) {
					raw++
				}
			}
			if raw == 0 {
				t.Fatalf("no Block.Text holds the selector sequence any more -- Text is the anchor side of the mapping and reanchor.CreateAnchor searches the RAW document for it, so a filtered Text makes the block uncommentable, exactly as it would for a filtered control byte")
			}
		})
	}
}

// TestAControlByteIsNotItsOwnGlyph pins the reverse-video distinction, written
// to be the thing that fails rather than the thing that passes vacuously.
//
// THE PROBLEM IT PINS. `␍` is an ordinary printing rune a document may quote,
// so a substitution that is merely a substitution makes a document WRITING
// ABOUT a carriage return byte-identical to one CONTAINING one. The reviewer
// would have no way to tell the documentation of the attack from the attack.
//
// SO IT ASSERTS BOTH HALVES, and the first makes the second non-vacuous: on
// screen, ansi.Strip'd, the two documents must READ THE SAME -- which is what
// says the glyph is right and the test is looking at a real collision -- and in
// the bytes that reach the terminal they must DIFFER.
//
// SIX ARMS OF SEVEN, AND THE SEVENTH IS THE TABLE HEADER CELL, where the
// reverse-video distinction is not achievable without reopening a different
// ruling. The GLYPH converges;
// the STYLING does not, for the reason tableGridRow states: a header cell is
// drawn from DisplayPlain and re-rendered bold BECAUSE DisplayPlain carries no
// styling, and ansi.Strip cannot keep the reverse run and drop everything else.
//
// SO ON A HEADER CELL A LITERAL ␍ AND A SUBSTITUTED ONE ARE THE SAME BYTES, and
// the reader still sees that SOMETHING is there -- the reduced form of the
// distinction, not its absence. Fixing it means either putting SGR into
// DisplayPlain, which is
// also the search index's field, or drawing a header cell from Display.
// Recorded with the arm's own flag so it cannot widen silently: any OTHER arm
// losing the run fails this test, and the header arm GAINING it fails it too.
func TestAControlByteIsNotItsOwnGlyph(t *testing.T) {
	st := darkStyles(t)
	const width = 100
	for _, arm := range controlArms {
		t.Run(arm.name, func(t *testing.T) {
			real, _ := controlArmRows(t, st, arm.src, "before\rafter", width)
			written, _ := controlArmRows(t, st, arm.src, "before␍after", width)
			gotReal, gotWritten := strings.Join(real, "\n"), strings.Join(written, "\n")
			if ansi.Strip(gotReal) != ansi.Strip(gotWritten) {
				t.Fatalf("the two documents already read differently with the styling stripped off:\n  a real CR: %q\n  the rune ␍: %q\nThere is no collision here for the styling to resolve, so this case proves nothing about the reverse-video distinction", ansi.Strip(gotReal), ansi.Strip(gotWritten))
			}
			if hasReverseVideo(gotWritten) {
				t.Fatalf("the literal rune ␍ was drawn in reverse video: %q -- then the styling says nothing, because it says the same thing about both documents", gotWritten)
			}
			if got := hasReverseVideo(gotReal); got != arm.styled {
				if arm.styled {
					t.Fatalf("a real carriage return drew no reverse-video run: %q -- an unstyled substitution makes a document writing about ␍ indistinguishable from one containing a CR", gotReal)
				}
				t.Fatalf("this arm now DOES carry the reverse-video run: %q -- that is the exception below closing, which is good news and makes this table wrong", gotReal)
			}
			if arm.styled && gotReal == gotWritten {
				t.Fatalf("the two documents render to identical bytes: %q -- the styling is what tells them apart and it is not there", gotReal)
			}
		})
	}
}

// TestLeafExpressionsOutsideTheNamedSetAreFilteredToo is the widening of the
// leaf filter, asserted rather than argued: renderInlines has FOUR expressions
// where a slice of the source becomes a rendered string -- the text segment, a
// code span, an autolink's URL and an *ast.String's value -- and a filter at
// the text segment alone covers neither `a\bb` inside backticks nor a hostile
// autolink.
//
// THE AUTOLINK's ONE REACHABLE BYTE IS DEL, AND THAT IS THE WHOLE OF IT.
// CommonMark's absolute-URI autolink excludes ASCII control characters, and
// goldmark implements the exclusion as a range that stops at 0x20 -- so
// `<https://e/a\bb>` is not an autolink at all and falls through to the text
// segment. 0x7f is above that range and goldmark lets it through, so the
// fixture below is DEL on purpose: with any other byte this subtest passes with
// the filter removed, because it is then measuring the text segment.
//
// AND THE FOURTH EXPRESSION IS DEAD IN THIS BUILD, which is worth an assertion
// rather than a sentence. *ast.String is produced by exactly one thing in
// goldmark -- extension.Typographer -- and parseDocument registers
// extension.Table and nothing else. It is filtered anyway, because a filter
// that has to be revisited when a parser option changes is a filter nobody will
// revisit.
//
// THE UNDERLINED CASES ARE THE ONES THAT CAUGHT A REAL DEFECT. An autolink's
// URL and an inline link's TEXT are both rendered through
// style.Underline(true), lipgloss renders an underlined string one grapheme at
// a time, and the first spelling of renderControls wrapped a Render around a
// string that already carried this renderer's own SGR -- so the sequence came
// out as printed characters. No other arm could have found it: every other
// style in this package is a foreground and a background, which lipgloss wraps
// in one run.
//
// THE LAST CASE IS NOT A LEAF AT ALL. discardedTail appends a RAW source span
// to the last cell of a malformed table row, after renderInlines has finished
// with it, so a filter at the leaf alone does not see it.
func TestLeafExpressionsOutsideTheNamedSetAreFilteredToo(t *testing.T) {
	st := darkStyles(t)
	const width = 100
	for _, tc := range []struct{ name, src, ctl, want string }{
		{"a code span", "prefix `a\bb` suffix\n", "\b", "a␈b"},
		{"an autolink URL", "<https://example.com/a\x7fb>\n", "\x7f", "https://example.com/a␡b"},
		{"an inline link's text, which is underlined", "see [a\bb](https://e/x) here\n", "\b", "a␈b"},
		{"a discarded tail", "| a | b |\n|---|---|\n| c | d | ZQTAIL\x7fZQ\n", "\x7f", "ZQTAIL␡ZQ"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, _ := controlArmRows(t, st, "%s", tc.src, width)
			plain := ansi.Strip(strings.Join(rows, "\n"))
			if strings.Contains(plain, tc.ctl) {
				t.Fatalf("the byte %q reached the screen through this expression: %q -- the leaf filter named the text segment and this is not it", tc.ctl, plain)
			}
			if !strings.Contains(plain, tc.want) {
				t.Fatalf("the screen reads %q, want it to contain %q", plain, tc.want)
			}
		})
	}
	t.Run("the *ast.String arm is unreachable with this parser", func(t *testing.T) {
		// Every construct extension.Typographer would rewrite, plus the
		// table and heading shapes that exercise the rest of the walk.
		src := "a --- b ... c \"q\" 'r' -- d\n\n# h\n\n| a | b |\n|---|---|\n| c | d |\n"
		var strings_ int
		_ = ast.Walk(parseDocument([]byte(src)), func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if entering {
				if _, ok := n.(*ast.String); ok {
					strings_++
				}
			}
			return ast.WalkContinue, nil
		})
		if strings_ != 0 {
			t.Fatalf("parseDocument produced %d *ast.String nodes -- the arm is live after all, and its Value is the extension's substitution table rather than document source, so what it should carry is a question this test's comment answers wrongly", strings_)
		}
	})
}

// TestTheFallbackArmKeepsTheCarriageReturnReanchorFolds closes a fork the
// original design did not account for, and it is here because the obvious fix
// is the wrong one.
//
// reanchor.Normalize IS AN UNDOCUMENTED PARTIAL C0 FILTER: it is
// strings.Join(strings.Fields(text), " ") and strings.Fields splits on
// unicode.IsSpace, which covers CR, VT and FF and does not cover NUL, BEL, BS
// or DEL. displayIn's fallback drew Normalize(Text), so on that arm the CR
// forgery arrived already folded to a space and the BACKSPACE forgery arrived
// whole -- one of the two headline rows eaten and the other kept, silently.
//
// AND IT MUST NOT BE FIXED IN reanchor. That function is the ANCHORING
// normaliser, and the carrying argument for four spaces over a Control Picture
// is a statement about what it folds: four spaces move 0 of 13,449 anchorable
// spans where ␉ moves 175, BECAUSE Normalize folds whitespace and not a glyph.
// Teaching it to keep a CR would move anchor spans in every document holding
// one. So the change is
// normalizeVisible, on the display side, and this asserts BOTH ends: the screen
// shows ␍, and reanchor.Normalize still folds it.
func TestTheFallbackArmKeepsTheCarriageReturnReanchorFolds(t *testing.T) {
	st := darkStyles(t)
	rows, blocks := controlArmRows(t, st, "<div>\n%s\n</div>\n", "left\rright", 100)
	if len(blocks) != 1 || blocks[0].Display != "" || blocks[0].DisplayPlain != "" {
		t.Fatalf("the fixture is not on the fallback arm: %d blocks, Display %q, DisplayPlain %q -- an HTML block is the shape with no projection at all, and without that this asserts nothing about displayIn", len(blocks), blocks[0].Display, blocks[0].DisplayPlain)
	}
	plain := ansi.Strip(strings.Join(rows, "\n"))
	if !strings.Contains(plain, "left␍right") {
		t.Fatalf("the fallback arm drew %q, want it to contain %q -- reanchor.Normalize folds a CR to a space, so on this arm the forgery used to be silently defanged and its neighbour \\b was not", plain, "left␍right")
	}
	if got := reanchor.Normalize("left\rright"); got != "left right" {
		t.Fatalf("reanchor.Normalize now answers %q for a CR -- it is the ANCHORING normaliser and this fix must not have moved it; the display side is normalizeVisible's job", got)
	}
	if got, want := normalizeVisible("a  b\tc\nd"), reanchor.Normalize("a  b\tc\nd"); got != want {
		t.Fatalf("normalizeVisible answered %q where reanchor.Normalize answers %q -- the two must agree on every string with no control byte in it, or displayIn has grown a second whitespace convention", got, want)
	}
}

// TestAThreadCardVisualisesItsStoredBytes is deliberately NOT a document test.
//
// THE TWO CHANNELS HERE HAVE NO ACCIDENTAL COVER, which is the whole reason
// they get their own assertion instead of riding on controlArms. A document's
// prose reaches the screen through renderInlines, whose leaves each end in
// their own SGR reset, so a long escape sequence planted in a paragraph gets
// cut by accident and a filter validated there can ship green over the exposed
// channels. A comment BODY and an ATTRIBUTION are stored as whole strings, are
// never parsed, are never projected, and are handed to one Render call each.
//
// BOTH FRAMES, because they are two View() methods with two budgets. RenderDoc
// draws the card inline in the document; ThreadCardLines draws the same card
// inside the relocate panel with its own ansi.Truncate over the top, and a
// filter that ran after that truncation would look fine in one frame and not
// the other.
//
// AND domain.Comment.Body IS UNTOUCHED, asserted here for the same reason
// Block.Text is asserted untouched on the document arms.
func TestAThreadCardVisualisesItsStoredBytes(t *testing.T) {
	st := darkStyles(t)
	const width = 100
	for _, p := range []struct{ name, byteStr, left, right string }{
		{"BS", "\b", "Requires approval", "No approval needed"},
		{"CR", "\r", "We will NOT rotate the production keys.", "We will rotate them"},
	} {
		for _, ch := range threadCardChannels {
			for _, fr := range threadCardFrames {
				t.Run(p.name+"/"+ch.name+"/"+fr.name, func(t *testing.T) {
					payload := p.left + p.byteStr + p.right
					v := ch.view(payload)
					got := strings.Join(fr.draw(t, v, ch.pseudonym(payload), width, st), "\n")
					plain := ansi.Strip(got)
					if strings.Contains(plain, p.byteStr) {
						t.Fatalf("the byte %q reached the screen unfiltered through this channel: %q -- a reviewer approves BYTES, and a comment body is the one channel whose bytes were written by somebody else", p.byteStr, plain)
					}
					glyph := string(controlPicture(p.byteStr[0]))
					if !strings.Contains(plain, glyph) {
						t.Fatalf("the byte %q is gone and %q is not there either: %q -- the control-byte substitution is VISUALISE and not strip", p.byteStr, glyph, plain)
					}
					for _, half := range []string{p.left, p.right} {
						if !strings.Contains(plain, half) {
							t.Fatalf("%q is missing from the screen: %q -- the filter must make the byte visible, not take the words with it", half, plain)
						}
					}
					if raw := ch.raw(v); raw != "" && !strings.Contains(raw, p.byteStr) {
						t.Fatalf("the stored value no longer holds %q -- this renderer must not edit the fact the store holds", p.byteStr)
					}
				})
			}
		}
	}
}

// TestAThreadCardControlByteIsNotItsOwnGlyph is the reverse-video distinction
// on the two stored channels, written the same shape as
// TestAControlByteIsNotItsOwnGlyph: the two
// documents must READ THE SAME with the styling stripped -- which is what says
// there is a real collision for the styling to resolve -- and must DIFFER in
// the bytes, with reverse video on the substituted one only.
//
// A comment body is the likeliest place in the whole product to find a literal
// ␍: it is where one human explains a control byte to another. Without the
// styling, "the plan contains a ␍ here" and a plan that actually contains one
// are the same pixels.
func TestAThreadCardControlByteIsNotItsOwnGlyph(t *testing.T) {
	st := darkStyles(t)
	const width = 100
	for _, ch := range threadCardChannels {
		for _, fr := range threadCardFrames {
			t.Run(ch.name+"/"+fr.name, func(t *testing.T) {
				const cr, glyph = "before\rafter", "before␍after"
				real := strings.Join(fr.draw(t, ch.view(cr), ch.pseudonym(cr), width, st), "\n")
				written := strings.Join(fr.draw(t, ch.view(glyph), ch.pseudonym(glyph), width, st), "\n")
				if ansi.Strip(real) != ansi.Strip(written) {
					t.Fatalf("the two cards already read differently with the styling stripped off:\n  a real CR: %q\n  the rune ␍: %q\nThere is no collision here for the styling to resolve", ansi.Strip(real), ansi.Strip(written))
				}
				if hasReverseVideo(written) {
					t.Fatalf("the literal rune ␍ was drawn in reverse video: %q -- then the styling says nothing, because it says the same thing about both", written)
				}
				if !hasReverseVideo(real) {
					t.Fatalf("a real carriage return drew no reverse-video run: %q -- an unstyled substitution makes a comment ABOUT a control byte indistinguishable from one carrying it", real)
				}
			})
		}
	}
}

// threadCardChannels is every string renderThreadCardPainted draws that this
// renderer did not write itself.
//
// THE PSEUDONYM IS HERE BECAUSE IT IS NOT A STORED FIELD and reaches the same
// Render call: it is client/config's mint-on-first-read machine pseudonym, so
// a control byte in it would be this machine's own config rather than anything
// a comment carried. It is covered because the filter is at the DRAW and not at
// the field.
//
// ⚠️ ActorLogin AND Agent CARRY NO SHAPE VALIDATION AND MUST NOT GAIN ANY HERE.
// These rows assert what a reader SEES and say nothing about what the decoder
// accepts.
var threadCardChannels = []struct {
	name string
	view func(payload string) ThreadView
	// pseudonym is what the frame is handed alongside the view. Only the
	// pseudonym row varies it; every other row passes a benign one so that
	// row's own payload is the only control byte in the frame.
	pseudonym func(payload string) string
	// raw is the stored value, which the renderer must not have edited.
	// "" means this channel stores nothing (the pseudonym is an argument).
	raw func(ThreadView) string
}{
	{
		name: "a comment body",
		view: func(payload string) ThreadView {
			return ThreadView{Thread: thread("reviewer", payload, false), Placed: true}
		},
		pseudonym: func(string) string { return "calm-mountain" },
		raw:       func(v ThreadView) string { return v.Thread.Comments[0].Body },
	},
	{
		name: "an attribution's login",
		view: func(payload string) ThreadView {
			return ThreadView{Thread: domain.Thread{Comments: []domain.Comment{{
				Attribution: domain.Attribution{ActorLogin: payload}, Body: "a note",
			}}}, Placed: true}
		},
		pseudonym: func(string) string { return "calm-mountain" },
		raw:       func(v ThreadView) string { return v.Thread.Comments[0].Attribution.ActorLogin },
	},
	{
		name: "an attribution's agent half",
		view: func(payload string) ThreadView {
			return ThreadView{Thread: domain.Thread{Comments: []domain.Comment{{
				Attribution: domain.Attribution{Agent: payload, ActorLogin: "alice"}, Body: "a note",
			}}}, Placed: true}
		},
		pseudonym: func(string) string { return "calm-mountain" },
		raw:       func(v ThreadView) string { return v.Thread.Comments[0].Attribution.Agent },
	},
	{
		name: "the machine pseudonym",
		view: func(string) ThreadView {
			return ThreadView{Thread: domain.Thread{Comments: []domain.Comment{{Body: "a note"}}}, Placed: true}
		},
		pseudonym: func(payload string) string { return payload },
		raw:       func(ThreadView) string { return "" },
	},
}

// threadCardFrames is the two View() methods that draw a thread card. They are
// not one frame with two callers: ThreadCardLines re-truncates every row to
// its own panel width, so a filter placed after that budget would pass in the
// document and fail in the relocate panel.
var threadCardFrames = []struct {
	name string
	draw func(t *testing.T, v ThreadView, pseudonym string, width int, st *Styles) []string
}{
	{"the document view", func(t *testing.T, v ThreadView, pseudonym string, width int, st *Styles) []string {
		t.Helper()
		blocks := ParseBlocks([]byte("a paragraph\n"), st)
		var out []string
		for _, l := range RenderDoc(blocks, map[int][]ThreadView{0: {v}}, nil, map[int]bool{0: true}, OnLine(0), width, pseudonym, st) {
			out = append(out, l.Text)
		}
		return out
	}},
	{"the relocate panel", func(t *testing.T, v ThreadView, pseudonym string, width int, st *Styles) []string {
		t.Helper()
		return ThreadCardLines(v, width, pseudonym, st)
	}},
}

// benchmarkHostileDoc is benchmarkDoc with one control byte in every prose
// line and one in every heading -- the same block count, the same shape, the
// same length to within the bytes themselves, so the two can be quoted side by
// side and the difference attributed to the filter rather than to the fixture.
//
// BS AND NOT CR, and the reason is goldmark rather than the renderer: a bare
// carriage return is a LINE TERMINATOR to the parser, so a document built from
// them is a different document with a different block count.
func benchmarkHostileDoc(n int) []byte {
	var b strings.Builder
	for i := range n {
		if i%10 == 0 {
			fmt.Fprintf(&b, "## Section\b%d\n\n", i/10)
			continue
		}
		fmt.Fprintf(&b, "The **deploy** step\b%d needs a rollback\bplan\nbefore the gate\bis run again.\n\n", i)
	}
	return []byte(b.String())
}

// BenchmarkControlFilter is the filter's cost, re-derivable rather than quoted.
//
// BOTH ARMS ARE HERE BECAUSE THE FILTER'S COST IS ENTIRELY A PROPERTY OF THE
// INPUT. On benign text visibleControls is one byte scan that finds nothing and
// returns its argument, so it allocates NOTHING; on hostile text it builds a
// string and calls Render once per control byte, which is where every
// allocation it has comes from. Quoting either figure as "the filter's cost"
// without the other is the mistake.
//
// AND THE PARSE ARM IS THE ONE THAT MATTERS, because that is where the filter
// actually runs -- three renderInlines walks per block, off the keystroke path,
// once per RefreshFromSession. The bare-filter arm is the same scan over the
// whole document source in one call, which is an upper bound on the volume the
// leaf sees per walk and is quoted as such.
func BenchmarkControlFilter(b *testing.B) {
	const size = 2500
	st := darkStyles(b)
	for _, doc := range []struct {
		name string
		src  []byte
	}{
		{"benign", benchmarkDoc(size)},
		{"hostile", benchmarkHostileDoc(size)},
	} {
		src := doc.src
		if got := len(ParseBlocks(src, st)); got != size {
			b.Fatalf("the %s fixture parsed to %d blocks, want %d -- the two documents must be the same shape or this is not a comparison", doc.name, got, size)
		}
		b.Run("ParseBlocks/"+doc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				ParseBlocks(src, st)
			}
		})
		text := string(src)
		b.Run("visibleControls/"+doc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				visibleControls(text, st.Text)
			}
		})
	}
}

func TestInlineDisplaySeparation(t *testing.T) {
	blocks := ParseBlocks(loadDoc(t), nil)
	for _, b := range blocks {
		if strings.Contains(b.Text, "**This is a pre-production clean break.**") {
			if strings.Contains(b.Display, "**") {
				t.Fatal("display still contains literal markers")
			}
			return
		}
	}
	t.Fatal("fixture paragraph not found")
}

// TestInferTitle pins two rules that meet in one function.
//
// The empty-path fallback: a sourceless plan (path "") with no H1 must not fall
// through to filepath.Base("")'s nonsensical ".", the way a real path's
// filename-stem fallback still does.
//
// And the quoted-heading rule one layer over the heading path: a QUOTED
// `# …` must not name the quoting document. InferTitle selects on Kind and
// Level alone, and a
// quoted heading is a real KindHeading at its real Level -- deliberately -- so
// without the quote depth in the filter the first thing a quoted document says
// is taken as its own title. The two quoted cases are the two arms that failure
// has.
func TestInferTitle(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		path string
		want string
	}{
		{"h1 wins over path", "# My Plan\n\nBody.\n", "/plans/foo.md", "My Plan"},
		{"no h1 falls back to filename stem", "Just a body.\n", "/plans/foo.md", "foo"},
		{"no h1 and no path falls back to a placeholder", "Just a body.\n", "", "Untitled"},
		{"a quoted h1 does not name the document", "> # Their plan\n\nJust a body.\n", "/plans/foo.md", "foo"},
		{"the document's own h1 wins over a quoted one above it", "> # Their plan\n\n# My plan\n", "/plans/foo.md", "My plan"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := InferTitle(ParseBlocks([]byte(c.doc), nil), c.path)
			if got != c.want {
				t.Fatalf("InferTitle = %q, want %q", got, c.want)
			}
		})
	}
}

// TestParseBlocksQuotedHeadingStaysOutOfTheHeadingPath pins the quoted-heading
// rule, whose two halves pull in opposite directions and are both asserted
// here.
//
// A quoted heading RENDERS as a heading -- it keeps KindHeading and its own
// Level, so it gets its sigil inside the quote -- and it does NOT enter the
// heading path. The heading path IS the anchor (reanchor.Anchor is heading
// path plus normalized span), so a heading inside a quotation restructures
// the anchor namespace of the document doing the quoting: every block after
// it anchors under a section its author never wrote, and re-anchoring then
// hunts for that section in a rewritten document that never had one.
//
// THE BLOCK AFTER THE QUOTE is the assertion that matters most and the one a
// smaller fix would miss: suppressing only the quoted heading's OWN path
// still leaves it on the stack, and the stack is what every later block
// reads.
func TestParseBlocksQuotedHeadingStaysOutOfTheHeadingPath(t *testing.T) {
	const source = "# Plan\n\nBefore the quote.\n\n> ## Someone else's section\n>\n> Their words.\n\nAfter the quote.\n\n> # A quoted level one\n\nAfter the quoted level one.\n"
	blocks := ParseBlocks([]byte(source), nil)
	want := []string{"Plan"}
	cases := []struct {
		name  string
		text  string
		kind  BlockKind
		level int
	}{
		{"the quoted heading itself", "Someone else's section", KindHeading, 2},
		{"the block quoted under it", "Their words.", KindParagraph, 0},
		{"the block after the quote", "After the quote.", KindParagraph, 0},
		// The quoted LEVEL ONE is the case that pins the pop half. A quoted
		// `## …` under a `# …` pops nothing whatever the rule is, so a
		// fixture with only that one would stay green with the pop left in
		// -- and the pop is the more damaging half, dropping every block
		// after the quotation to the root of the anchor namespace instead of
		// merely adding a level to it.
		{"the quoted level one itself", "A quoted level one", KindHeading, 1},
		{"the block after the quoted level one", "After the quoted level one.", KindParagraph, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := blockContaining(t, blocks, c.text)
			if !reflect.DeepEqual(b.HeadingPath, want) {
				t.Fatalf("HeadingPath = %v, want %v -- a quotation must not restructure the anchor namespace of the document quoting it", b.HeadingPath, want)
			}
			if b.Kind != c.kind || b.Level != c.level {
				t.Fatalf("Kind/Level = %v/%d, want %v/%d -- a quoted heading still renders as a heading", b.Kind, b.Level, c.kind, c.level)
			}
		})
	}
}

// blockContaining is the one block whose Text holds want, refusing an
// ambiguous match for the reason rowContaining does: a fixture that grew a
// second occurrence would otherwise have this assert about a block the caller
// did not mean.
func blockContaining(t *testing.T, blocks []Block, want string) Block {
	t.Helper()
	var found []Block
	for _, b := range blocks {
		if strings.Contains(b.Text, want) {
			found = append(found, b)
		}
	}
	if len(found) != 1 {
		t.Fatalf("blocks whose Text holds %q = %d, want exactly 1", want, len(found))
	}
	return found[0]
}

// quotedDoc carries a quotation at every depth a real corpus contains: 240
// blockquotes at depth 1, 12 at depth 2, exactly 1 at depth 3, so three is the
// deepest a marker has to count to. The depth-3 paragraph is long on purpose:
// the bar is spent cells, and only a line that wraps can show whether it was
// spent out of the width budget or added on top of it.
const quotedDoc = `Plain prose.

> A quotation.
>
> > Nested one level.
> >
> > > And two, which is as deep as anything in the corpus goes, written long enough here that it has to wrap at eighty columns and show what the continuation row does.

> ## A quoted heading
>
> ~~~
> quoted code
> ~~~
`

// TestQuotedBlocksCarryOneBarPerLevel is the marking half of the quote-marking
// rule, asserted from both ends of the same fixture: the depth ParseBlocks
// puts on the CONTAINED
// blocks -- a Blockquote is a container and emits no Block of its own, so there
// is nowhere else for it to live -- and the bars withQuoteBar draws from it.
//
// The row is compared WHOLE rather than by counting bars, so that an extra
// indent, a missing space or a bar in the wrong order is a failure too. The
// quoted heading is in the table because the quoted-heading rule's other half
// lives there.
func TestQuotedBlocksCarryOneBarPerLevel(t *testing.T) {
	const width = 80
	th, err := theme.Lookup(theme.DefaultName)
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	source := []byte(quotedDoc)
	blocks := ParseBlocks(source, st)
	// Where the cursor is does not matter to anything below: rowBody cuts
	// the gutter it lands in off every row it is handed.
	lines := RenderDoc(blocks, nil, nil, nil, OnLine(0), width, "cm", st)

	cases := []struct {
		name  string
		text  string
		depth int
		row   string
	}{
		{"unquoted", "Plain prose.", 0, "Plain prose."},
		{"depth 1", "A quotation.", 1, "▎ A quotation."},
		{"depth 2", "Nested one level.", 2, "▎ ▎ Nested one level."},
		{"depth 3", "And two, which is as deep", 3, "▎ ▎ ▎ And two, which is as deep as anything in the corpus goes,"},
		// THE CONTINUATION ROW IS ITS OWN CASE, because nothing else here
		// sees it: rowContaining matches the FIRST row holding its text, and
		// the frame-wide width invariant below cannot see a missing bar either
		// -- a continuation row short by two cells is padded back to width by
		// the row's own background fill.
		{"depth 3 continuation", "long enough here that it has to wrap", 3, "▎ ▎ ▎ written long enough here that it has to wrap at eighty columns"},
		{"a quoted heading", "A quoted heading", 1, "▎ §§ A quoted heading"},
		{"a quoted code block", "quoted code", 1, "▎   quoted code"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := blockContaining(t, blocks, c.text)
			if b.QuoteDepth != c.depth {
				t.Fatalf("QuoteDepth = %d, want %d", b.QuoteDepth, c.depth)
			}
			// Text is the anchor side of the source mapping and QuoteDepth
			// only adds metadata, so the quoted block's own bytes must still
			// be exactly what the parser attributed to it -- markers and all
			// left alone. A heading's Text is trimmed, so it is excluded the
			// same way TestParseBlocksSourceMapping excludes it.
			if b.Kind != KindHeading && b.Text != string(source[b.SrcStart:b.SrcEnd]) {
				t.Fatalf("Text is not its source slice: %q", b.Text)
			}
			if got := rowBody(rowContaining(t, lines, c.text), width); got != c.row {
				t.Fatalf("row = %q, want %q", got, c.row)
			}
		})
	}

	// A quoted CODE block keeps its own zone UNDER the bar. Code owns CodeBg
	// wherever it appears, so a bar drawn on Doc in front of one would leave
	// that row painted in two backgrounds at once -- the failure the whole
	// file's every-fragment-carries-its-own-background rule exists to stop,
	// and the one branch quoteBarStyle has.
	if want, got := "48;2;"+sgrRGB(t, th.CodeBg), styleBefore(t, rowContaining(t, lines, "quoted code"), quoteBarGlyph); !strings.Contains(got, want) {
		t.Fatalf("the bar in front of a quoted code block is styled %q, want the row's own CodeBg %q", got, want)
	}

	// EVERY row, not only the quoted ones, is one screen line of exactly the
	// terminal's width. This is what says the bars were taken out of the width
	// budget before the arms wrapped rather than added after: calling
	// renderBlockPainted with the full width leaves the depth-1 and depth-2
	// rows 156 cells wide with a newline in the middle.
	for i, l := range lines {
		if strings.Contains(l.Text, "\n") {
			t.Fatalf("line %d holds more than one screen row: %q", i, l.Text)
		}
		if got := ansi.StringWidth(ansi.Strip(l.Text)); got != width {
			t.Fatalf("line %d is %d cells wide, want %d: %q", i, got, width, l.Text)
		}
	}
}

// quoteBarWidths is the widths TestQuoteBarSpendsExactlyItsGlyphsWidth sweeps.
// 80 is the frame a real corpus is written for; 120 is wide enough that the
// fixture's tables stop wrapping; 40 is below railMinWidth, so the rail band
// collapses and the quoted rows are the widest thing on screen.
var quoteBarWidths = []int{40, 80, 120}

// TestQuoteBarSpendsExactlyItsGlyphsWidth is the bar's width contract stated as
// an invariant instead of as a report: a ONE-CELL glyph swapped for another
// one-cell glyph must move nothing, and if anything moves, the one-cell
// assumption is the thing that was wrong.
//
// IT IS IN THREE PARTS AND THE MIDDLE ONE IS THE ARGUMENT.
//
//  1. THE GLYPH. One rune and one cell. Both, not either: a two-rune grapheme
//     -- a base plus a variation selector, which is how '⚠️' is spelled -- can
//     measure one cell to a width function that walks GRAPHEMES and two to one
//     that walks RUNES, and a bar whose width depends on which walk asked is
//     not a one-cell glyph.
//
//  2. THE ONLY WAY THE GLYPH REACHES A ROW IS AS A PREFIX, and the only way it
//     reaches the LAYOUT is through quoteBarCols. Every quoted block is
//     rendered twice at each width -- once led, once bare at the budget less
//     the bar -- and the led rows must be the bare rows with quoteBarFor's
//     string in front and nothing else changed. Both paths are driven, because
//     there are two: withQuoteBar wraps renderBlockPainted for every kind, and
//     quoteBarLead leads the grid pre-pass's lines for a table row. Nothing
//     downstream of the prefix can see WHICH glyph it is, so a glyph of the
//     same width cannot move a row -- and quoteBarCols is asserted against the
//     glyph's own measured width rather than against 2, so a glyph of a
//     DIFFERENT width fails here and says so.
//
//  3. THE SWEEP, which is part 2's claim checked from the outside: the fixture
//     through RenderDoc at each width, every Line exactly the terminal's width
//     and exactly one screen row. Non-vacuous by its own count of barred rows.
//
// WHAT IT ADDS, STATED HONESTLY: IT IS NOT A NEW DETECTOR. Of five mutations
// run against the whole repository, this caught four and was the sole catcher
// of none -- and the fifth (withQuoteBar trimming the row it leads) is INERT
// rather than uncaught, since no row this fixture draws ends in a space. What
// it is for is the WHY: the assertions that already catch a glyph swap are row
// strings that spell '▎' out and leave the reader to work out that the bar is a
// prefix and that its width is spent twice. This says it, and it is the
// assertion that fires FIRST, before a single row is rendered.
func TestQuoteBarSpendsExactlyItsGlyphsWidth(t *testing.T) {
	st := darkStyles(t)
	blocks := ParseBlocks(loadFidelity(t), st)

	glyphCells := ansi.StringWidth(quoteBarGlyph)
	if n := utf8.RuneCountInString(quoteBarGlyph); n != 1 || glyphCells != 1 {
		t.Fatalf("quoteBarGlyph %q is %d rune(s) and %d cell(s), want 1 and 1 -- the bar's whole width contract is that it is one of each", quoteBarGlyph, n, glyphCells)
	}
	for depth := range 5 {
		if want := depth * (glyphCells + 1); quoteBarCols(depth) != want {
			t.Fatalf("quoteBarCols(%d) = %d, want %d -- the budget must be the glyph's own width plus its trailing space, per level, or a glyph swap moves every quoted row", depth, quoteBarCols(depth), want)
		}
	}

	quoted := 0
	for _, width := range quoteBarWidths {
		grids := paintTables(blocks, width, st, nil, nil)
		for i, b := range blocks {
			if b.QuoteDepth == 0 {
				continue
			}
			bar := quoteBarFor(b, st)
			if got, want := ansi.Strip(bar), strings.Repeat(quoteBarGlyph+" ", b.QuoteDepth); got != want {
				t.Fatalf("width %d, block %d at depth %d: the bar is %q, want %q", width, i, b.QuoteDepth, got, want)
			}
			if got := ansi.StringWidth(ansi.Strip(bar)); got != quoteBarCols(b.QuoteDepth) {
				t.Fatalf("width %d, block %d at depth %d: the bar draws %d cells and the budget spends %d", width, i, b.QuoteDepth, got, quoteBarCols(b.QuoteDepth))
			}
			bare, led, path := grids[i], []string(nil), "the grid pre-pass"
			if bare == nil {
				path = "withQuoteBar"
				bare = renderBlockPainted(b, width-quoteBarCols(b.QuoteDepth), st, false)
				led = withQuoteBar(b, width, st, false)
			} else {
				led = quoteBarLead(b, bare, st)
			}
			if len(led) != len(bare) {
				t.Fatalf("width %d, block %d (%s): %d led rows against %d bare ones", width, i, path, len(led), len(bare))
			}
			for j := range bare {
				if led[j] != bar+bare[j] {
					t.Fatalf("width %d, block %d row %d (%s): the led row is not the bare row behind the bar\n led  %q\n want %q", width, i, j, path, led[j], bar+bare[j])
				}
			}
			quoted++
		}
	}
	if quoted == 0 {
		t.Fatal("the fixture holds no quoted block, so nothing above compared anything")
	}

	for _, width := range quoteBarWidths {
		lines := RenderDoc(blocks, nil, nil, nil, OnLine(0), width, "cm", st)
		barred := 0
		for i, l := range lines {
			if strings.Contains(l.Text, "\n") {
				t.Fatalf("width %d, line %d holds more than one screen row: %q", width, i, l.Text)
			}
			if got := ansi.StringWidth(ansi.Strip(l.Text)); got != width {
				t.Fatalf("width %d, line %d is %d cells wide: %q", width, i, got, ansi.Strip(l.Text))
			}
			if strings.Contains(ansi.Strip(l.Text), quoteBarGlyph) {
				barred++
			}
		}
		if barred == 0 {
			t.Fatalf("width %d: no rendered row carries a bar, so the sweep above is vacuous", width)
		}
		t.Logf("width %d: %d lines, %d of them carrying a bar, every one exactly %d cells and one screen row", width, len(lines), barred, width)
	}
}

// rowBody is a rendered row's content: the styling stripped, the margin and
// the gutter cut off the front, and the background pad off the end. The
// gutter is skipped rather than asserted because the cursor and thread marks
// that live in it are other tests' subject.
//
// It cuts CELLS and not bytes -- ansi.TruncateLeft rather than a slice --
// because everything in the gutter and the margin is one cell and three bytes
// (the ┃ and the ※), so a byte offset lands mid-row and the assertion it
// feeds compares text the renderer never produced.
func rowBody(row string, width int) string {
	return strings.TrimRight(ansi.TruncateLeft(ansi.Strip(row), RailWidth(width)+gutterWidth, ""), " ")
}

// cellIndex is which screen column want starts in, which is what a claim
// about the gutter is actually about: strings.Index answers in BYTES, and the
// glyphs on either side of that boundary run three bytes to the cell.
func cellIndex(t *testing.T, plain, want string) int {
	t.Helper()
	i := strings.Index(plain, want)
	if i < 0 {
		t.Fatalf("%q is not in the row at all: %q", want, plain)
	}
	return ansi.StringWidth(plain[:i])
}

// ruleDoc carries a thematic break in every shape that decides something.
// CommonMark's set is open -- any of `-`, `_` or `*` repeated three or more
// times, with any spaces or tabs between them -- so this is not "the spellings"
// but the ones that change an answer: all three marks; one indented and trailed
// with whitespace (blockRange trims the tail and not the head); TWO WRITTEN
// WITH SPACES, which reanchor.Normalize counts as three words and CreateAnchor
// therefore accepts (see Anchorable); and one at quote depth 1 and one at 2.
//
// THE QUOTED ONES DO NOT STAND IN FOR ANYTHING IN A REAL CORPUS -- all 463
// thematic breaks there are at quote depth 0 and none is inside a list. They
// earn their place on the render side regardless: TestARuleDrawsARule reads
// them to say the quote bar is taken OUT of the rule's width budget rather
// than added to the row afterwards, which is a claim nothing at depth 0 can
// make.
const ruleDoc = `Before.

---

***

   ___   

- - -

* * *

> quoted
>
> ---
>
> > deeper
> >
> > ---

After.
`

// ruleBlocks is every KindRule block in document order, with the fixture
// assumption that there are as many as the caller expects checked here rather
// than in each test that walks them.
func ruleBlocks(t *testing.T, blocks []Block, want int) []Block {
	t.Helper()
	var out []Block
	for _, b := range blocks {
		if b.Kind == KindRule {
			out = append(out, b)
		}
	}
	if len(out) != want {
		t.Fatalf("KindRule blocks = %d, want %d", len(out), want)
	}
	return out
}

// TestThematicBreakIsARuleBlock is the model half: a `---` produced NO BLOCK AT
// ALL before it -- every one of them a section break the review view simply did
// not have -- and rendering is driven by blocks, so the separator needs one.
// There are 463 of them across 42 of a real corpus's 53 documents.
//
// ITS TEXT IS ITS OWN SOURCE LINE. goldmark appends this node no line segments,
// but every block node the parser opens carries a Pos, which is the technique
// blockRange uses for table rows -- whose text is not in their segments either
// -- so the line is available without them. An empty Text would have been a
// Block with an EMPTY RANGE, which TestParseBlocksSourceMapping's
// SrcStart < SrcEnd invariant already names as a defect.
//
// WHAT AN EMPTY TEXT WAS FOR SURVIVES ANYWAY: nothing can be anchored to a
// rule, and Anchorable is what says so at the two write sites.
//
// THE FLOOR UNDER CreateAnchor IS NOT A SECOND GUARANTEE, which is what
// floorRefuses is in the table below and why two cases set it FALSE. A break
// may be written `- - -`, reanchor.Normalize counts that as three words, and
// CreateAnchor hands back an anchor to a separator with no error at all. Both
// sides are asserted -- the tight spellings refused there, the spaced ones
// accepted -- because a reader who believed the floor was a backstop could
// replace the kind test in Anchorable with a length test and ship exactly that
// thread.
func TestThematicBreakIsARuleBlock(t *testing.T) {
	source := []byte(ruleDoc)
	blocks := ParseBlocks(source, nil)
	cases := []struct {
		text  string
		quote int
		// floorRefuses is whether reanchor.CreateAnchor's three-word floor
		// would refuse this rule's span on its own -- false for the spellings
		// that carry spaces, which is the whole point of the field.
		floorRefuses bool
	}{
		{"---", 0, true},
		{"***", 0, true},
		{"___", 0, true},
		{"- - -", 0, false},
		{"* * *", 0, false},
		{"---", 1, true},
		{"---", 2, true},
	}
	rules := ruleBlocks(t, blocks, len(cases))
	for i, c := range cases {
		b := rules[i]
		if b.SrcStart >= b.SrcEnd || b.SrcEnd > len(source) {
			t.Fatalf("rule %d has range %d-%d, which anchors nothing", i, b.SrcStart, b.SrcEnd)
		}
		if got := string(source[b.SrcStart:b.SrcEnd]); b.Text != got || b.Text != c.text {
			t.Fatalf("rule %d: Text = %q, source slice = %q, want both %q", i, b.Text, got, c.text)
		}
		if b.QuoteDepth != c.quote {
			t.Fatalf("rule %d: QuoteDepth = %d, want %d", i, b.QuoteDepth, c.quote)
		}
		if Anchorable(b) {
			t.Fatalf("rule %d is anchorable, so a comment aimed at it would be written against markup", i)
		}
		switch _, err := reanchor.CreateAnchor(ruleDoc, BlockAnchorSpan(b), b.HeadingPath); {
		case c.floorRefuses && err == nil:
			t.Fatalf("rule %d: CreateAnchor accepted %q, which the three-word floor is supposed to refuse", i, BlockAnchorSpan(b))
		case !c.floorRefuses && err != nil:
			t.Fatalf("rule %d: CreateAnchor refused %q (%v) -- if the floor catches a spaced rule now, Anchorable is no longer the only thing that does and the comments above are stale", i, BlockAnchorSpan(b), err)
		}
	}
	// The other side of the predicate, so it is not trivially false: ordinary
	// prose in the same document is anchorable.
	if b := blockContaining(t, blocks, "Before."); !Anchorable(b) {
		t.Fatal("a paragraph is not anchorable, so the predicate refuses everything")
	}
	// A rule computes no projection, so searchText falls back to Text and a
	// reader searching for those bytes finds the separators carrying them --
	// the same answer a fenced block's own Text gives for its lines. Asserted
	// because the fallback is what makes it true, and a projection added here
	// later would silently change the answer.
	if got := SearchBlocks(blocks, "---"); len(got) != 3 {
		t.Fatalf("SearchBlocks(\"---\") = %v, want the three rules spelled with dashes", got)
	}
}

// TestARuleDrawsARule is the screen half of the rule-drawing rule: the block
// is drawn as the break its markup asks for, at whatever width and quote
// depth it lands in.
//
// IT IS DRIVEN AT BOTH ENDS OF THE RANGE a reader can put it in -- 80, and
// narrowWidth where the painted rail collapses -- because the rule is the one
// arm in this view whose whole content is a function of the width it is
// handed, so a budget it spends wrongly is not a wrapped line but a rule of
// the wrong length. The quoted cases are what say the bar is taken out of
// that budget rather than added to the row afterwards.
func TestARuleDrawsARule(t *testing.T) {
	th, err := theme.Lookup(theme.DefaultName)
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	blocks := ParseBlocks([]byte(ruleDoc), st)
	rules := map[int]int{} // block index -> quote depth
	for i, b := range blocks {
		if b.Kind == KindRule {
			rules[i] = b.QuoteDepth
		}
	}
	if len(rules) != 7 {
		t.Fatalf("KindRule blocks = %d, want 7", len(rules))
	}
	for _, width := range []int{80, narrowWidth} {
		lines := RenderDoc(blocks, nil, nil, nil, OnLine(0), width, "cm", st)
		// docRightMargin is derived from the same constant renderDocPainted
		// spends, so a later change to the margin moves this with it instead
		// of leaving a rule two cells too long.
		body := width - 2*RailWidth(width) - gutterWidth - docRightMargin
		for idx, depth := range rules {
			var rows []string
			for _, l := range lines {
				if l.BlockIdx == idx && !l.IsThread {
					rows = append(rows, l.Text)
				}
			}
			// One rule row, plus the blank row every block ends with. A rule
			// that wrapped, or drew its markup as well, would be more.
			if len(rows) != 2 {
				t.Fatalf("width %d: rule block %d drew %d rows, want 1 and the blank after it", width, idx, len(rows)-1)
			}
			want := strings.Repeat(quoteBarGlyph+" ", depth) + strings.Repeat("─", body-quoteBarCols(depth))
			if got := rowBody(rows[0], width); got != want {
				t.Fatalf("width %d, depth %d: row = %q, want %q", width, depth, got, want)
			}
			// In the receding colour, not the body text's: a separator is
			// structure, and it sits on every second screen of a plan.
			if fg, got := "38;2;"+sgrRGB(t, th.Dim), styleBefore(t, rows[0], "─"); !strings.Contains(got, fg) {
				t.Fatalf("width %d, depth %d: the rule is styled %q, want the Dim foreground %q", width, depth, got, fg)
			}
		}
		for i, l := range lines {
			if strings.Contains(l.Text, "\n") {
				t.Fatalf("width %d, line %d holds more than one screen row: %q", width, i, l.Text)
			}
			if got := ansi.StringWidth(ansi.Strip(l.Text)); got != width {
				t.Fatalf("width %d, line %d is %d cells: %q", width, i, got, l.Text)
			}
		}
	}
}

// TestPaintedQuoteBarIsNeitherOfTheOtherTwoVerticals is the collision half of
// the quote-marking rule: the quote bar is the THIRD vertical in this view,
// and a reader who reads quoted as commented has been told the wrong thing
// about who said the words.
//
// One frame carries all three at once -- the cursor on a quoted block that also
// carries a comment -- because that is the frame where a reader could actually
// confuse them, and it lets the three be separated on all three axes the choice
// rests on: GLYPH (three distinct runes), COLOUR (Brand, Dim, Accent, checked
// to be three different colours in this preset rather than assumed to be), and
// POSITION (the cursor in the gutter, the bar a whole gutter to its right, the
// comment marker on a card row of its own). The thread here is UNRESOLVED: a
// resolved one's header is Dim too, so colour separates nothing in that frame.
//
// What it cannot assert is the axis that actually decided the glyph: that '▎'
// does not LOOK like '┃' or '|'. That was measured off the rasterised glyphs --
// see quoteBarGlyph (ui/painted.go).
func TestPaintedQuoteBarIsNeitherOfTheOtherTwoVerticals(t *testing.T) {
	const width, cursorGlyph = 80, "┃"
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	if quoteBarGlyph == cursorGlyph || quoteBarGlyph == commentMarker {
		t.Fatalf("the quote bar %q is one of the two verticals already spoken for", quoteBarGlyph)
	}
	if th.Dim == th.Brand || th.Dim == th.Accent {
		t.Fatalf("Dim is Brand or Accent in this preset, so colour separates nothing: %q/%q/%q", th.Dim, th.Brand, th.Accent)
	}
	v := singleCommentCard(domain.Attribution{ActorLogin: "dana-codes", ActorDisplay: "dana"})
	blocks := ParseBlocks([]byte("> A quotation carrying a comment.\n"), st)
	lines := RenderDoc(blocks, map[int][]ThreadView{0: {v}}, nil, map[int]bool{0: true}, OnLine(0), width, "cm", st)

	quoted := rowContaining(t, lines, "A quotation carrying a comment.")
	if got, want := fgOver(t, quoted, quoteBarGlyph), "38;2;"+sgrRGB(t, th.Dim); got != want {
		t.Fatalf("quote bar foreground = %q, want Dim %q: %q", got, want, quoted)
	}
	// THE ZONE, on the same row, because the foreground alone is blind to it:
	// OnCard() rebases backgrounds and leaves Dim's foreground exactly as it
	// was, so the check above passes whether the bar was handed the row's own
	// styles or the document's. This block carries a comment, so its row is
	// painted on Card -- the quote bar's other zone, the sibling of the
	// CodeBg case pinned in TestQuotedBlocksCarryOneBarPerLevel. Measured:
	// handing withQuoteBar st instead of blockSt leaves the bar on Doc and
	// fails only here.
	if want, got := "48;2;"+sgrRGB(t, th.Card), styleBefore(t, quoted, quoteBarGlyph); !strings.Contains(got, want) {
		t.Fatalf("the bar on a commented row is styled %q, want the row's own Card background %q", got, want)
	}
	if got, want := fgOver(t, quoted, cursorGlyph), "38;2;"+sgrRGB(t, th.Brand); got != want {
		t.Fatalf("cursor glyph foreground = %q, want Brand %q: %q", got, want, quoted)
	}
	// POSITION, off the same row: the cursor sits in the gutter and the bar a
	// whole gutter to its right, which is what "the bar lives in the content
	// area, not the gutter" means in cells. The gutter is already spoken for
	// by the cursor and the thread mark, so a bar drawn there would be the
	// collision in position that the glyph avoids in shape.
	plain := ansi.Strip(quoted)
	cursorAt, barAt := cellIndex(t, plain, cursorGlyph), cellIndex(t, plain, quoteBarGlyph)
	if barAt-cursorAt != gutterWidth {
		t.Fatalf("cursor at cell %d and bar at cell %d, want the bar a gutter (%d) to its right: %q", cursorAt, barAt, gutterWidth, plain)
	}
	if strings.Contains(plain, commentMarker) {
		t.Fatalf("a comment marker on a document row: %q", plain)
	}

	card := rowContaining(t, lines, "dana")
	if got, want := fgOver(t, card, commentMarker), "38;2;"+sgrRGB(t, th.Accent); got != want {
		t.Fatalf("comment marker foreground = %q, want Accent %q: %q", got, want, card)
	}
	if strings.Contains(ansi.Strip(card), quoteBarGlyph) {
		t.Fatalf("a quote bar on a comment card row: %q", card)
	}
}

// thread builds a one-comment thread attributed to author. It writes BOTH
// halves of the identity -- domain.Attribution carries an ActorLogin beside
// the display name, and a fixture holding the same string in both halves
// cannot say which half FormatAttribution read.
func thread(author, body string, resolved bool) domain.Thread {
	return domain.Thread{
		Resolved: resolved,
		Comments: []domain.Comment{{Attribution: domain.Attribution{
			ActorLogin: attrLogin(author), ActorDisplay: author,
		}, Body: body}},
	}
}

// attrLogin is the login every fixture in this file pairs with a display name,
// and it guarantees the two are never the same string.
//
// It lowercases and hyphenates whatever it is handed, then appends a suffix.
// The suffix is what makes the result DIFFER, by construction rather than by
// inspection of today's callers: the mapping is length-preserving in runes, so
// a mapped string plus a non-empty suffix can never equal its own input.
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

func TestRenderDocThreadsAndUnanchored(t *testing.T) {
	blocks := ParseBlocks(loadDoc(t), nil)
	views := map[int][]ThreadView{
		2: {{Thread: thread("reviewer", "state the attack concretely", false), Badge: "fuzzy ~85%", Placed: true}},
	}
	unanchored := []ThreadView{{Thread: thread("reviewer", "which flag system?", false)}}
	lines := RenderDoc(blocks, views, unanchored, map[int]bool{2: true}, OnLine(0), 100, "calm-mountain", darkStyles(t))

	joined := strings.Join(collectText(lines), "\n")
	if !strings.Contains(joined, "state the attack concretely") {
		t.Fatal("expanded thread body missing")
	}
	if !strings.Contains(joined, "fuzzy ~85%") {
		t.Fatal("badge missing")
	}
	if !strings.Contains(joined, "unanchored (1)") {
		t.Fatal("unanchored section missing")
	}
	sawUnanchored := false
	prev := 0
	for i, l := range lines {
		if l.BlockIdx == UnanchoredIdx {
			sawUnanchored = true
			continue
		}
		if sawUnanchored {
			t.Fatalf("line %d: document line after unanchored section", i)
		}
		if l.BlockIdx < prev || l.BlockIdx >= len(blocks) {
			t.Fatalf("line %d: mapping broken (%d after %d)", i, l.BlockIdx, prev)
		}
		prev = l.BlockIdx
	}
	if !sawUnanchored {
		t.Fatal("no unanchored lines emitted")
	}
}

func collectText(lines []Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Text
	}
	return out
}

// TestPaintedCodeZonesUseCodeAndCodeBg pins that painted code (inline
// spans and fenced blocks alike) render with the theme's Code foreground on
// CodeBg, not the surrounding Doc background — full width for a block row,
// the same as any other zone switch (thread cards on Card, and so on).
func TestPaintedCodeZonesUseCodeAndCodeBg(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)

	// Expected escapes are derived from the live preset, not hardcoded: the
	// palette is the repo owner's iteration surface; this test pins only that
	// code renders with whatever Code/CodeBg the theme says.
	codeOnCodeBg := "\x1b[38;2;" + sgrRGB(t, th.Code) + ";48;2;" + sgrRGB(t, th.CodeBg) + "m"
	codeBgEscape := "48;2;" + sgrRGB(t, th.CodeBg)

	source := []byte("Some prose with `inline code` in it.\n\n```\nfunc main() {}\n```\n")
	blocks := ParseBlocks(source, st)
	if !strings.Contains(blocks[0].Display, codeOnCodeBg) {
		t.Fatalf("inline code span missing Code-on-CodeBg escape: %q", blocks[0].Display)
	}

	lines := RenderDoc(blocks, nil, nil, nil, OnLine(0), 100, "calm-mountain", st)
	found := false
	for _, l := range lines {
		if !strings.Contains(l.Text, "func main") {
			continue
		}
		found = true
		if !strings.Contains(l.Text, codeBgEscape) {
			t.Fatalf("code-block row missing the full-width CodeBg fill: %q", l.Text)
		}
	}
	if !found {
		t.Fatal("no code-block row rendered")
	}
}

// sgrRGB converts "#rrggbb" to the "r;g;b" form used in SGR truecolor params.
func sgrRGB(t *testing.T, hex string) string {
	t.Helper()
	var r, g, b int
	if _, err := fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b); err != nil {
		t.Fatalf("bad hex %q: %v", hex, err)
	}
	return fmt.Sprintf("%d;%d;%d", r, g, b)
}

func TestResolveAnchorAndBadge(t *testing.T) {
	source := loadDoc(t)
	blocks := ParseBlocks(source, nil)
	target := -1
	for i, b := range blocks {
		if strings.Contains(b.Text, "anyone can claim to be anyone") {
			target = i
			break
		}
	}
	if target == -1 {
		t.Fatal("fixture block not found")
	}
	a := reanchor.Anchor{HeadingPath: blocks[target].HeadingPath, Span: BlockAnchorSpan(blocks[target])}
	if got := ResolveAnchor(blocks, a); got != target {
		t.Fatalf("ResolveAnchor = %d, want %d", got, target)
	}
	if got := ResolveAnchor(blocks, reanchor.Anchor{Span: "no such text at all"}); got != -1 {
		t.Fatalf("missing span must resolve to -1, got %d", got)
	}

	cases := []struct {
		p    placement.Placement
		want string
	}{
		{placement.Placement{Status: reanchor.StatusExact}, ""},
		{placement.Placement{Status: reanchor.StatusMoved}, "moved"},
		{placement.Placement{Status: reanchor.StatusFuzzy, Confidence: 0.853}, "fuzzy ~85%"},
	}
	for _, tc := range cases {
		if got := BadgeFor(tc.p); got != tc.want {
			t.Fatalf("BadgeFor(%s) = %q, want %q", tc.p.Status, got, tc.want)
		}
	}
}

// attributionCases is the EIGHT-row table the two card tests below share, so
// both are pinned against identical input: the four review-fact shapes
// domain.Attribution can take, the two where the author carries an id but no
// login, the one where it carries a DISPLAY NAME but no login -- the
// no-fallback case -- and a 39-column login, which is never truncated.
//
// EVERY want IS THE LOGIN AND NEVER THE DISPLAY NAME. Both halves are seeded on
// every row whose author HAS a login, and they are always different strings,
// so each of those wants is a choice between two values the fixture really
// holds -- which is the whole reason this table can fail at all.
//
// The id-without-a-login pair is what the pseudonym rule turns on: apart from
// it and the no-fallback row, every case that has an author sets that author's
// halves together, which is how a branch on one field alone rendered a
// recorded author as this machine's anonymous human for as long as no caller
// happened to construct one half without the other.
func attributionCases() []struct {
	name string
	attr domain.Attribution
	want string
} {
	return []struct {
		name string
		attr domain.Attribution
		want string
	}{
		{"author, no agent", domain.Attribution{ActorID: "u1", ActorLogin: "dana-loves-coding", ActorDisplay: "danaLovesCoding"}, "dana-loves-coding"},
		{"neither author nor agent", domain.Attribution{}, "user-calm-mountain"},
		{"author and agent", domain.Attribution{ActorID: "u1", ActorLogin: "dana-loves-coding", ActorDisplay: "danaLovesCoding", Agent: "blue-parakeet-f9"}, "agent-blue-parakeet-f9 ● dana-loves-coding"},
		{"agent, no author", domain.Attribution{Agent: "blue-parakeet-f9"}, "agent-blue-parakeet-f9"},
		{"author with no login", domain.Attribution{ActorID: "u1"}, "u1"},
		{"author with no login, and an agent", domain.Attribution{ActorID: "u1", Agent: "blue-parakeet-f9"}, "agent-blue-parakeet-f9 ● u1"},
		// THE NO-FALLBACK ROW, and it is an assertion about a branch that
		// must NOT exist rather than a test of one that does. A display
		// name with no login beside it is representable --
		// domain.Attribution is a PERSISTED type, and a stored fact that
		// holds ActorDisplay without ActorLogin decodes exactly so -- so
		// this is the one row that would pass either way if the chain
		// still stepped onto the display name.
		{"a display name with no login renders the id, never the name", domain.Attribution{ActorID: "u1", ActorDisplay: "danaLovesCoding"}, "u1"},
		{"a 39-column login is never truncated", domain.Attribution{ActorID: "u1", ActorLogin: "abcdefghijklmnopqrstuvwxyz-abcdefghijkl", ActorDisplay: "abcdefghijklmnopqrstuvwxyzabcdefghijklm"}, "abcdefghijklmnopqrstuvwxyz-abcdefghijkl"},
	}
}

func singleCommentCard(attr domain.Attribution) ThreadView {
	return ThreadView{Thread: domain.Thread{Comments: []domain.Comment{{Attribution: attr, Body: "a comment body"}}}}
}

// TestRenderThreadCardAttributionUnstripped pins the card's per-comment header
// against every case in attributionCases, through the same ui.ThreadCardLines
// entry point the document view and the relocate panel both call, WITHOUT
// stripping ANSI: the header text has to survive as one contiguous run in the
// styled string, which is a property its twin below cannot see.
func TestRenderThreadCardAttributionUnstripped(t *testing.T) {
	for _, c := range attributionCases() {
		t.Run(c.name, func(t *testing.T) {
			lines := ThreadCardLines(singleCommentCard(c.attr), 100, "calm-mountain", darkStyles(t))
			joined := strings.Join(lines, "\n")
			if !strings.Contains(joined, "| "+c.want) {
				t.Fatalf("lines = %q, want a header line %q", lines, "| "+c.want)
			}
		})
	}
}

// TestRenderThreadCardPaintedAttribution is
// TestRenderThreadCardAttributionUnstripped's twin, driven through the
// identical entry point and asserting on the STRIPPED render: a header the
// styles happened to split across two Render calls reads correctly here and
// fails there, so the pair pins the text and its contiguity separately.
func TestRenderThreadCardPaintedAttribution(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	for _, c := range attributionCases() {
		t.Run(c.name, func(t *testing.T) {
			lines := ThreadCardLines(singleCommentCard(c.attr), 100, "calm-mountain", st)
			joined := ansi.Strip(strings.Join(lines, "\n"))
			if !strings.Contains(joined, "| "+c.want) {
				t.Fatalf("lines = %q, want a header line %q", joined, "| "+c.want)
			}
		})
	}
}

// TestFormatAttributionBareLoginCollidesByDesign pins an attribution
// collision that STILL EXISTS, on purpose: FormatAttribution's bare-author arm
// draws a login with no prefix, so a login spelled like an agent's line or like
// this machine's pseudonym line draws identically to it.
//
// ⚠️ IF YOU ARE HERE BECAUSE THIS TEST LOOKS LIKE A BUG, READ THE REASON BELOW
// BEFORE CHANGING ANYTHING. The one-line fix -- namespacing
// FormatAttribution's bare-author branch, so a lone author renders
// "someone-else" as something no agent line could be -- is RULED OUT, not
// overlooked, and this test is what stops it being "fixed" back in: the
// attribution lines are already long, and a prefix would lengthen every one of
// them.
func TestFormatAttributionBareLoginCollidesByDesign(t *testing.T) {
	const pseudonym = "quiet-otter-31"

	// Two rows, and they are the two DIFFERENT genuine writes a bare login
	// can be mistaken for: an agent's, and this machine's anonymous human's.
	// One row would prove only that some collision exists; the pair says
	// which ones, so a later reader can tell a widened residual from this
	// one.
	for _, tc := range []struct {
		name    string
		hostile domain.Attribution
		genuine domain.Attribution
	}{
		{
			name:    "OPEN BY RULING: a login may spell an agent pseudonym",
			hostile: domain.Attribution{ActorID: "u1", ActorLogin: "agent-blue-parakeet-f9", ActorDisplay: "Blue Parakeet"},
			genuine: domain.Attribution{Agent: "blue-parakeet-f9"},
		},
		{
			name:    "OPEN BY RULING: a login may spell this machine's pseudonym",
			hostile: domain.Attribution{ActorID: "u1", ActorLogin: "user-" + pseudonym, ActorDisplay: "Quiet Otter"},
			genuine: domain.Attribution{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, want := FormatAttribution(tc.hostile, pseudonym), FormatAttribution(tc.genuine, pseudonym)
			if got != want {
				t.Fatalf("an author whose login is %q renders %q while the write it is imitating renders %q.\n"+
					"THEY ARE SUPPOSED TO BE IDENTICAL. If you just namespaced FormatAttribution's bare-author branch, that is exactly what is RULED OUT -- see this test's own comment for the ruling. Revert the prefix; do not update this expectation.",
					tc.hostile.ActorLogin, got, want)
			}
		})
	}
}

// TestRenderThreadCardEveryCommentHasOwnHeader: a reply carries its own header,
// exactly like the opening comment, rather than riding along under the thread's
// first one.
func TestRenderThreadCardEveryCommentHasOwnHeader(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	v := ThreadView{Thread: domain.Thread{Comments: []domain.Comment{
		{Attribution: domain.Attribution{ActorLogin: "dana-codes", ActorDisplay: "dana"}, Body: "first comment"},
		{Attribution: domain.Attribution{Agent: "blue-parakeet-f9"}, Body: "second comment"},
	}}}
	for _, tc := range []struct {
		name string
		st   *Styles
	}{{"painted", st}} {
		t.Run(tc.name, func(t *testing.T) {
			joined := ansi.Strip(strings.Join(ThreadCardLines(v, 100, "calm-mountain", tc.st), "\n"))
			if !strings.Contains(joined, "| dana-codes") {
				t.Fatalf("missing first comment's own header: %q", joined)
			}
			if !strings.Contains(joined, "| agent-blue-parakeet-f9") {
				t.Fatalf("missing second comment's own header: %q", joined)
			}
			if got := strings.Count(joined, "| "); got != 2 {
				t.Fatalf("want exactly 2 comment headers, got %d in %q", got, joined)
			}
		})
	}
}

// TestRenderThreadCardAgentAuthoredFirstCommentNoBulletCollision: an agent
// acting for a recorded author renders as
// "agent-blue-parakeet-f9 ● dana-loves-coding", so a '●' LEADING that same line
// would put two dots on it meaning two different things. There is no status
// glyph at all now, so '●' survives only as FormatAttribution's mid-line
// separator -- and the assertions follow: no line may BEGIN with '●', and the
// header carries exactly one.
func TestRenderThreadCardAgentAuthoredFirstCommentNoBulletCollision(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	v := ThreadView{
		Thread: domain.Thread{
			Resolved: false,
			Comments: []domain.Comment{
				{Attribution: domain.Attribution{ActorID: "u1", ActorLogin: "dana-loves-coding", ActorDisplay: "danaLovesCoding", Agent: "blue-parakeet-f9"}, Body: "agent-authored first comment"},
			},
		},
	}
	for _, tc := range []struct {
		name string
		st   *Styles
	}{{"painted", st}} {
		t.Run(tc.name, func(t *testing.T) {
			joined := ansi.Strip(strings.Join(ThreadCardLines(v, 100, "calm-mountain", tc.st), "\n"))

			if !strings.Contains(joined, "| agent-blue-parakeet-f9 ● dana-loves-coding") {
				t.Fatalf("composite attribution is not '|' led with a '●' separator: %q", joined)
			}
			for _, line := range strings.Split(joined, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "●") {
					t.Fatalf("a line leads with ●, which the attribution separator already spends on that line:\n%s", joined)
				}
			}
			if got := strings.Count(joined, "●"); got != 1 {
				t.Fatalf("want exactly 1 ● (the attribution separator), got %d in %q", got, joined)
			}
		})
	}
}

// TestOverlaySplicesRowAndLeavesOthersUntouched pins Overlay's basic shape:
// only the targeted row changes, the splice lands at exactly the given
// column, and every other row survives byte-for-byte.
func TestOverlaySplicesRowAndLeavesOthersUntouched(t *testing.T) {
	bg := "row0-untouched\nBACKGROUND-ROW-ONE\nrow2-untouched"
	rows := strings.Split(bg, "\n")
	fg := "FG"
	const x = 4

	got := Overlay(bg, fg, x, 1)
	gotRows := strings.Split(got, "\n")

	if gotRows[0] != rows[0] {
		t.Fatalf("row 0 changed: got %q, want %q", gotRows[0], rows[0])
	}
	if gotRows[2] != rows[2] {
		t.Fatalf("row 2 changed: got %q, want %q", gotRows[2], rows[2])
	}
	want := rows[1][:x] + fg + rows[1][x+len(fg):]
	if gotRows[1] != want {
		t.Fatalf("row 1 = %q, want %q", gotRows[1], want)
	}
}

// TestOverlayMultiRowForeground pins that fg's own rows land one-for-one on
// consecutive bg rows starting at y, in order.
func TestOverlayMultiRowForeground(t *testing.T) {
	bg := "0123456789\n0123456789\n0123456789"
	fg := "AA\nBB"
	got := Overlay(bg, fg, 3, 1)
	gotRows := strings.Split(got, "\n")
	want := []string{"0123456789", "012AA56789", "012BB56789"}
	for i, w := range want {
		if gotRows[i] != w {
			t.Fatalf("row %d = %q, want %q", i, gotRows[i], w)
		}
	}
}

// TestOverlaySkipsRowsOutsideBackground pins that a fg row landing above row
// 0 or at/past bg's last row is dropped rather than panicking or growing
// bg's own line count -- Overlay never adds or removes a row of bg, which is
// the fact its "reserves nothing" doc comment rests on.
func TestOverlaySkipsRowsOutsideBackground(t *testing.T) {
	bg := "a\nb\nc"
	cases := []struct {
		name string
		y    int
	}{
		{"row above bg", -1},
		{"row at bg's line count", 3},
		{"row well past bg", 50},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Overlay(bg, "X", 0, c.y); got != bg {
				t.Fatalf("Overlay(_, _, 0, %d) = %q, want bg unchanged %q", c.y, got, bg)
			}
		})
	}
}

// TestOverlaySGRContinuitySurvivesSplice pins a risk that was measured and
// retired: splicing plain text into a styled row must not strip the colour
// state from the row's tail. bgRow is built from raw escapes (not lipgloss) so
// the test is independent of any colour-profile detection. ansi.Cut's
// left-anchored truncation copies escape bytes through even while it is
// skipping the printable cells before the cut point, so the SAME sequence that
// opened the row reappears at the front of the tail fragment -- this test is
// the pin, not a defence: Overlay adds nothing to make it true.
func TestOverlaySGRContinuitySurvivesSplice(t *testing.T) {
	const setBG = "\x1b[48;5;235m"
	const reset = "\x1b[0m"
	bgRow := setBG + strings.Repeat(" ", 20) + reset
	fg := "hi"

	got := Overlay(bgRow, fg, 5, 0)

	wantPlain := strings.Repeat(" ", 5) + "hi" + strings.Repeat(" ", 13)
	if stripped := ansi.Strip(got); stripped != wantPlain {
		t.Fatalf("stripped content = %q, want %q", stripped, wantPlain)
	}

	tail := got[strings.Index(got, fg)+len(fg):]
	if !strings.Contains(tail, setBG) {
		t.Fatalf("tail after the splice lost the background color, want %q to reappear in %q", setBG, tail)
	}
	if ansi.StringWidth(got) != ansi.StringWidth(bgRow) {
		t.Fatalf("StringWidth(got) = %d, want %d (bgRow's own width)", ansi.StringWidth(got), ansi.StringWidth(bgRow))
	}
}

// TestOverlayClampKeepsPerRowWidthInvariant is the clamp's regression pin.
// THE REAL INVARIANT IS PER ROW WIDTH, not line count: bg and the composite
// always agree on line count regardless of any row's width, so a line-count
// assertion cannot see a too-wide row -- and a too-wide row is exactly what
// wraps in the terminal and adds a visual row. This sweep asserts
// ansi.StringWidth(out row) == ansi.StringWidth(bg row) for every row across a
// grid that deliberately runs well outside "x >= 0 && x+width(fg) <= width(bg)"
// on both sides.
//
// This is the absence pin: delete Overlay's clamp and it starts failing. Of
// these 180 swept (bgWidth, x, fgWidth) triples, 118 fall outside the safe
// condition, and the unclamped splice fails the per-row-width invariant on
// every one of those 118 -- while every one of them still passes a line-count
// assertion, which is what makes a line-count assertion worthless here.
func TestOverlayClampKeepsPerRowWidthInvariant(t *testing.T) {
	bgWidths := []int{1, 10, 40}
	var xs []int
	for x := -5; x <= 14; x++ {
		xs = append(xs, x)
	}
	fgWidths := []int{1, 5, 15}

	for _, bgw := range bgWidths {
		bg := strings.Repeat("x", bgw)
		for _, x := range xs {
			for _, fgw := range fgWidths {
				fg := strings.Repeat("y", fgw)
				got := Overlay(bg, fg, x, 0)
				gotRows := strings.Split(got, "\n")
				if len(gotRows) != 1 {
					t.Fatalf("bgWidth=%d x=%d fgWidth=%d: Overlay added a row: %q", bgw, x, fgw, got)
				}
				if w := ansi.StringWidth(gotRows[0]); w != bgw {
					t.Fatalf("bgWidth=%d x=%d fgWidth=%d: StringWidth(out) = %d, want %d (bg's own width)", bgw, x, fgw, w, bgw)
				}
			}
		}
	}
}

// TestOverlayWideRuneBackgroundBoundsWidth pins the residual the ASCII-only
// sweep above cannot see: a cut boundary landing INSIDE a double-width CJK
// grapheme can pull the whole cluster back into the kept fragment, producing a
// row WIDER than bg's own -- the literal "wraps and adds a visual row" failure
// this primitive exists to prevent, through a seam the cell-count clamp cannot
// see.
//
// The bound here is deliberately <=, not ==: over-wide is corruption and must
// never happen; under-wide is a cosmetic gap of at most one dropped cluster and
// is accepted. Overlay's final ansi.Cut pass is a no-op on any row already <=
// bg's width, so the ASCII sweep above keeps asserting == exactly.
//
// The reproducer: before the final-cut fix,
// Overlay(strings.Repeat("中",10), "A", 0, 0) measured width 21 against bg's own
// 20. This pins the fixed value (19: the straddled first "中" is dropped whole
// rather than half-kept). Over the 125-case sweep below, 42 were over-width
// before the fix and 0 after.
func TestOverlayWideRuneBackgroundBoundsWidth(t *testing.T) {
	bg := strings.Repeat("中", 10) // 10 double-width CJK clusters, width 20
	bgw := ansi.StringWidth(bg)

	if got := ansi.StringWidth(Overlay(bg, "A", 0, 0)); got != 19 {
		t.Fatalf(`Overlay(bg, "A", 0, 0) width = %d, want 19 (bg's width 20, minus the straddled cluster's dropped cell)`, got)
	}

	var xs []int
	for x := -5; x <= 19; x++ {
		xs = append(xs, x)
	}
	for _, x := range xs {
		for fgw := 1; fgw <= 5; fgw++ {
			fg := strings.Repeat("A", fgw)
			if w := ansi.StringWidth(Overlay(bg, fg, x, 0)); w > bgw {
				t.Fatalf("x=%d fgWidth=%d: StringWidth(out) = %d, want <= %d (bg's own width)", x, fgw, w, bgw)
			}
		}
	}
}

// rowContaining returns the single rendered row whose visible text contains
// want, failing if none or more than one does — a role assertion that
// silently matched the wrong row would pass for the wrong reason.
func rowContaining(t *testing.T, lines []Line, want string) string {
	t.Helper()
	var found []string
	for _, l := range lines {
		if strings.Contains(ansi.Strip(l.Text), want) {
			found = append(found, l.Text)
		}
	}
	if len(found) != 1 {
		t.Fatalf("rows containing %q = %d, want exactly 1", want, len(found))
	}
	return found[0]
}

// sgrSeq matches one SGR escape; fgOver walks a rendered row and reports the
// truecolor foreground ("38;2;r;g;b") active over the first text run that
// contains want. A whole-row strings.Contains cannot do this job: swapping
// two styles WITHIN a row leaves the row's set of escapes identical, so an
// assertion that only asks "is this colour present somewhere" passes on a
// sigil and its words wearing each other's colour -- verified by mutation,
// which is how this helper came to exist.
var sgrSeq = regexp.MustCompile("\x1b\\[[0-9;]*m")

func fgOver(t *testing.T, row, want string) string {
	t.Helper()
	var hits []string
	fg, pos := "", 0
	// An ambiguous want is refused rather than resolved to the first match,
	// for the reason rowContaining refuses an ambiguous row: a caller whose
	// want appears twice would be told the colour of a run it did not mean,
	// which is exactly the silent-wrong-answer this helper exists to stop.
	consider := func(text string) {
		if strings.Contains(text, want) {
			hits = append(hits, fg)
		}
	}
	// Both colour forms are parsed. Only 38;2 is reachable through
	// lipgloss.Render today, but skipping an indexed 38;5;n would leave the
	// PREVIOUS foreground standing and report it as this run's, and an
	// indexed value of 0 would be read as a reset -- so the arms exist for
	// the same reason app.paintedBG's do.
	for _, loc := range sgrSeq.FindAllStringIndex(row, -1) {
		consider(row[pos:loc[0]])
		params := strings.Split(strings.TrimSuffix(strings.TrimPrefix(row[loc[0]:loc[1]], "\x1b["), "m"), ";")
		for i := 0; i < len(params); i++ {
			switch params[i] {
			case "", "0":
				fg = ""
			case "38":
				switch {
				case i+4 < len(params) && params[i+1] == "2":
					fg = strings.Join(params[i:i+5], ";")
					i += 4
				case i+2 < len(params) && params[i+1] == "5":
					fg = strings.Join(params[i:i+3], ";")
					i += 2
				}
			case "48":
				switch {
				case i+4 < len(params) && params[i+1] == "2":
					i += 4
				case i+2 < len(params) && params[i+1] == "5":
					i += 2
				}
			}
		}
		pos = loc[1]
	}
	consider(row[pos:])
	if len(hits) != 1 {
		t.Fatalf("text runs containing %q = %d, want exactly 1: %q", want, len(hits), row)
	}
	return hits[0]
}

// TestPaintedHeadingsWearOneColourSigilAndWords pins the rule every heading
// level follows: sigil and words share one colour, Heading, separated only by
// weight — the words bold, the § run regular. Level is carried by the sigil's
// LENGTH and by nothing else, which is why all three levels assert the same two
// colours.
func TestPaintedHeadingsWearOneColourSigilAndWords(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	// The cursor is parked on the leading paragraph, NOT on a heading: the ┃
	// glyph carries a colour of its own and would answer for the sigil's.
	source := []byte("intro paragraph\n\n# One\n\n## Two\n\n### Three\n")
	lines := RenderDoc(ParseBlocks(source, st), nil, nil, nil, OnLine(0), 100, "calm-mountain", st)

	want := "38;2;" + sgrRGB(t, th.Heading)
	for _, tc := range []struct{ name, words string }{
		{"level1", "One"}, {"level2", "Two"}, {"level3", "Three"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := rowContaining(t, lines, tc.words)
			if got := fgOver(t, row, "§"); got != want {
				t.Fatalf("%s § sigil foreground = %q, want Heading %q: %q", tc.name, got, want, row)
			}
			if got := fgOver(t, row, tc.words); got != want {
				t.Fatalf("%s words foreground = %q, want Heading %q: %q", tc.name, got, want, row)
			}
		})
	}
}

// TestPaintedHeadingSigilIsLighterThanItsWords pins the one thing separating
// the sigil from the words now that they share a colour. Without it the two
// are byte-identical styles and the § run reads as part of the title.
func TestPaintedHeadingSigilIsLighterThanItsWords(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	if !st.Heading.GetBold() {
		t.Fatal("heading words are not bold; weight is the only thing left separating them from the sigil")
	}
	if st.HeadingGlyph.GetBold() {
		t.Fatal("heading sigil is bold; it must stay the lighter of the two")
	}
}

// TestPaintedHeadingsCarryWeightNotUnderline pins the other half of the heading
// change: weight and the § sigil carry a heading, and no level underlines.
//
// BOLD IS ASSERTED, not merely the absence of the underline: with the underline
// gone and levels 1 and 3+ on the body Text colour, weight is the only thing
// separating st.Heading from st.Text. Unpinned, deleting Bold(true) renders
// every heading as plain body text and the whole suite stays green.
func TestPaintedHeadingsCarryWeightNotUnderline(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	for _, tc := range []struct {
		name  string
		style lipgloss.Style
	}{{"Heading", st.Heading}} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.style.GetUnderline() {
				t.Fatalf("%s underlines; headings carry on weight and the § sigil instead", tc.name)
			}
			if !tc.style.GetBold() {
				t.Fatalf("%s is not bold; with the underline gone and level 1/3+ on the body Text colour, weight is the only signal left", tc.name)
			}
		})
	}
}

// TestPaintedCursorMarkersCarryBrand pins the position half of the split.
// Both of the document's cursor markers — the rail band's background and the
// ┃ glyph's foreground — take Brand where they took Focus's blue before, so
// identity and position are one colour and Accent is left free to mean state
// alone (TestPaintedThreadCardMarksStateNotWithARail guards the other side).
//
// The non-cursor row is checked too: a Brand that leaked onto every row would
// satisfy the positive assertions while marking nothing.
func TestPaintedCursorMarkersCarryBrand(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	source := []byte("the cursor block\n\nsome other block\n")
	lines := RenderDoc(ParseBlocks(source, st), nil, nil, nil, OnLine(0), 100, "calm-mountain", st)

	cursor := rowContaining(t, lines, "the cursor block")
	if want := "48;2;" + sgrRGB(t, th.Brand); !strings.Contains(cursor, want) {
		t.Fatalf("cursor row's rail band missing the Brand background: %q", cursor)
	}
	if want := "38;2;" + sgrRGB(t, th.Brand); !strings.Contains(cursor, want) {
		t.Fatalf("cursor row's ┃ glyph missing the Brand foreground: %q", cursor)
	}
	other := rowContaining(t, lines, "some other block")
	if strings.Contains(other, sgrRGB(t, th.Brand)) {
		t.Fatalf("non-cursor row carries Brand, so the marker marks nothing: %q", other)
	}
}

// TestPaintedThreadCardMarksStateNotWithARail is the negative half of the
// split, and it pins the marker's SHAPE as well as its colour.
//
// A comment is state, so its marks keep Accent while position moves to Brand
// — and the mark is the '|' each header carries, in Accent as a FOREGROUND.
// The solid two-cell Accent BACKGROUND that used to run down the card's left
// edge is gone: the site draws no such rail, and it sat immediately beside
// the '|' it duplicated, marking the same fact a second time and far louder.
// Asserting the background's absence is what stops the rail coming back.
func TestPaintedThreadCardMarksStateNotWithARail(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	lines := ThreadCardLines(singleCommentCard(domain.Attribution{ActorLogin: "dana-codes", ActorDisplay: "dana"}), 100, "calm-mountain", st)
	card := strings.Join(lines, "\n")

	var header string
	for _, l := range lines {
		if strings.Contains(ansi.Strip(l), "| dana") {
			header = l
		}
	}
	if header == "" {
		t.Fatalf("no comment header line in %q", card)
	}
	if got, want := fgOver(t, header, "| dana"), "38;2;"+sgrRGB(t, th.Accent); got != want {
		t.Fatalf("comment marker foreground = %q, want Accent %q: %q", got, want, header)
	}
	if rail := "48;2;" + sgrRGB(t, th.Accent); strings.Contains(card, rail) {
		t.Fatalf("the solid Accent rail is back on the card's left edge: %q", card)
	}
	if strings.Contains(card, sgrRGB(t, th.Brand)) {
		t.Fatalf("thread card carries Brand; state and position must not share a colour: %q", card)
	}
}

// TestThreadMarkKeepsOneColumnOffTheText pins BOTH halves of the gutter's mark
// field, which are separate invariants that a single marked row cannot
// distinguish between.
//
// The separator: the mark is a glyph and a trailing space. The field was once
// exactly glyph-plus-count wide with no space at all, so a block carrying
// threads rendered "●2For agentic workers", welded to its text.
//
// The alignment: unmarked rows pad to the mark's full width so body text starts
// in one column whether a block carries threads or not. Asserting only the
// separator leaves this free -- shrinking the pad back to two cells keeps every
// marked row correct and silently shifts every UNMARKED block one column left,
// which the whole suite passed through before this case existed.
func TestThreadMarkKeepsOneColumnOffTheText(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	source := []byte("For agentic workers: a block that carries threads.\n\nArchitecture: a block that carries none.\n")
	views := map[int][]ThreadView{0: {
		singleCommentCard(domain.Attribution{ActorLogin: "dana-codes", ActorDisplay: "dana"}),
		singleCommentCard(domain.Attribution{ActorLogin: "dana-codes", ActorDisplay: "dana"}),
	}}
	for _, tc := range []struct {
		name string
		st   *Styles
	}{{"painted", st}} {
		t.Run(tc.name, func(t *testing.T) {
			lines := RenderDoc(ParseBlocks(source, tc.st), views, nil, nil, OnLine(0), 78, "calm-mountain", tc.st)

			marked := ansi.Strip(rowContaining(t, lines, "For agentic workers"))
			if !strings.Contains(marked, "※ For agentic workers") {
				t.Fatalf("thread mark is welded to its text, want %q: %q", "※ For", marked)
			}
			unmarked := ansi.Strip(rowContaining(t, lines, "Architecture"))
			// DISPLAY columns, not byte offsets: ┃ and ● are three bytes
			// each, so strings.Index alone reports the marked row starting
			// nine "columns" in when it starts at five.
			col := func(row, sub string) int {
				return ansi.StringWidth(row[:strings.Index(row, sub)])
			}
			at, want := col(unmarked, "Architecture"), col(marked, "For agentic")
			if at != want {
				t.Fatalf("unmarked block's text starts at column %d, marked at %d; the pad must hold the mark's full width\n marked: %q\n  plain: %q", at, want, marked, unmarked)
			}
		})
	}
}

// TestPaintedCommentedBlockPaintsTheCardZone pins that a block carrying
// comments is painted on Card — the same zone as the cards beneath it, so the
// block and its threads read as one region and an annotated line is visible
// as annotated without hunting for the gutter mark.
//
// The two zone rules that bound it are pinned alongside, because each is a
// judgement that a later reader could reasonably reverse:
//
//   - An inline code span keeps CodeBg on a commented row. Code owns its zone
//     wherever it appears; rebasing the span would paint it the colour of the
//     row it sits in and erase the background the palette just gave it.
//   - A commented CODE BLOCK keeps CodeBg outright and is marked only in the
//     gutter, rather than being painted Card outside its text and CodeBg
//     inside it — one row in two zones at once.
func TestPaintedCommentedBlockPaintsTheCardZone(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	doc, card, codeBg := "48;2;"+sgrRGB(t, th.Doc), "48;2;"+sgrRGB(t, th.Card), "48;2;"+sgrRGB(t, th.CodeBg)

	source := []byte("commented prose holding `a span` inside it.\n\nplain prose holding `a span` too.\n\n```\ncommented fenced code\n```\n")
	v := singleCommentCard(domain.Attribution{ActorLogin: "dana-codes", ActorDisplay: "dana"})
	views := map[int][]ThreadView{0: {v}, 2: {v}}
	lines := RenderDoc(ParseBlocks(source, st), views, nil, nil, OnLine(9), 70, "calm-mountain", st)

	for _, tc := range []struct {
		name, find, want, absent string
	}{
		// No `absent` for the commented row: its own code span sits on Doc
		// by design, so Doc's presence somewhere in the row is expected.
		{"commented prose takes Card", "commented prose", card, ""},
		{"uncommented prose stays Doc", "plain prose", doc, card},
		{"commented code block keeps CodeBg", "commented fenced code", codeBg, card},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := rowContaining(t, lines, tc.find)
			if !strings.Contains(row, tc.want) {
				t.Fatalf("row is missing the %q background: %q", tc.want, row)
			}
			if tc.absent != "" && strings.Contains(row, tc.absent) {
				t.Fatalf("row carries %q, the zone it must not be in: %q", tc.absent, row)
			}
		})
	}

	// An inline span keeps a background of its own in BOTH zones, one step
	// below whatever row it sits in -- CodeBg under Doc, Doc under Card. The
	// step is deliberately NOT Card-to-CodeBg, which is two steps and reads
	// as a hole punched in a raised row.
	for _, tc := range []struct{ name, find, want, absent string }{
		{"span on a Doc row recesses to CodeBg", "plain prose", codeBg, card},
		{"span on a Card row recesses to Doc", "commented prose", doc, codeBg},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := rowContaining(t, lines, tc.find)
			if !strings.Contains(row, tc.want) {
				t.Fatalf("inline code span is missing %q: %q", tc.want, row)
			}
			if strings.Contains(row, tc.absent) {
				t.Fatalf("inline code span carries %q, a step too far: %q", tc.absent, row)
			}
		})
	}
}

// styleBefore returns the SGR sequence immediately preceding want — the whole
// style lipgloss emitted for that run, not just its foreground, so two runs
// can be compared on every attribute at once.
func styleBefore(t *testing.T, s, want string) string {
	t.Helper()
	i := strings.Index(s, want)
	if i < 0 {
		t.Fatalf("no %q in %q", want, s)
	}
	locs := sgrSeq.FindAllStringIndex(s[:i], -1)
	if len(locs) == 0 {
		t.Fatalf("no style precedes %q in %q", want, s)
	}
	last := locs[len(locs)-1]
	return s[last[0]:last[1]]
}

// TestPaintedAnnotationMarksAgreeOnStyle pins that the gutter's ※ and the
// card's '|' comment marker are styled identically -- not merely the same
// colour.
//
// Both are Accent and they looked like two colours, because the card's marker
// takes its weight from CardHeader and is bold while the gutter's mark was not,
// and terminals brighten bold. Asserting the colour alone would not have caught
// it, so this compares the ENTIRE emitted style. On a commented block both sit
// on Card, so the two should differ in nothing but the glyph.
func TestPaintedAnnotationMarksAgreeOnStyle(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	v := singleCommentCard(domain.Attribution{ActorLogin: "dana-codes", ActorDisplay: "dana"})
	lines := RenderDoc(ParseBlocks([]byte("a commented block.\n"), st),
		map[int][]ThreadView{0: {v}}, nil, map[int]bool{0: true}, OnLine(9), 70, "calm-mountain", st)

	gutter := styleBefore(t, rowContaining(t, lines, "a commented block"), "※")
	var marker string
	for _, l := range lines {
		if l.IsThread && strings.Contains(ansi.Strip(l.Text), "| dana") {
			marker = styleBefore(t, l.Text, "|")
			break
		}
	}
	if marker == "" {
		t.Fatal("no comment header rendered")
	}
	if gutter != marker {
		t.Fatalf("gutter mark and comment marker are styled differently:\n gutter %q\n marker %q", gutter, marker)
	}
}

// TestThreadCardHasNoStatusRowAndLabelsResolved pins the card's shape: an
// unresolved thread opens directly on its first comment's header, and a
// resolved one says so in words on a row of its own above the comments.
//
// The BADGE is asserted here because removing the status row took away the line
// it used to sit on, and a badge that quietly stopped rendering is the silent
// loss this change could most easily cause: it says the anchor moved.
func TestThreadCardHasNoStatusRowAndLabelsResolved(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	mk := func(resolved bool) ThreadView {
		return ThreadView{Badge: "moved", Thread: domain.Thread{Resolved: resolved, Comments: []domain.Comment{
			{Attribution: domain.Attribution{ActorLogin: "dana-codes", ActorDisplay: "dana"}, Body: "the body"},
			{Attribution: domain.Attribution{Agent: "spicy-cushion-3j"}, Body: "a reply"},
		}}}
	}
	for _, path := range []struct {
		name string
		st   *Styles
	}{{"painted", st}} {
		for _, tc := range []struct {
			name      string
			resolved  bool
			wantFirst string
		}{
			{"open opens on its first comment", false, "| dana-codes [moved]"},
			{"resolved is labelled above them", true, resolvedLabel},
		} {
			t.Run(path.name+"/"+tc.name, func(t *testing.T) {
				lines := ThreadCardLines(mk(tc.resolved), 100, "calm-mountain", path.st)
				if got := strings.TrimSpace(ansi.Strip(lines[0])); got != tc.wantFirst {
					t.Fatalf("first row = %q, want %q", got, tc.wantFirst)
				}
				joined := ansi.Strip(strings.Join(lines, "\n"))
				if strings.Count(joined, "[moved]") != 1 {
					t.Fatalf("badge must appear exactly once, on the first header: %q", joined)
				}
				if got, want := strings.Count(joined, resolvedLabel), map[bool]int{true: 1, false: 0}[tc.resolved]; got != want {
					t.Fatalf("%q appears %d times, want %d: %q", resolvedLabel, got, want, joined)
				}
				// NOT struck through. Striking the word reads as the word
				// being retracted -- resolved, then decided not to be --
				// which is the opposite of what it says.
				if tc.resolved && path.st != nil && path.st.CardResolved.GetStrikethrough() {
					t.Fatal("the (resolved) row is struck through, which reads as the resolution being undone")
				}
			})
		}
	}
}

// TestNeighbouringThreadsAreSeparated pins the blank row between two threads on
// one block; without it two threads run together into one wall of headers with
// nothing to say where the first ends.
//
// The separator belongs to the CALLER -- a card renders one thread and cannot
// know it has a neighbour -- so it is asserted through RenderDoc rather than
// ThreadCardLines.
func TestNeighbouringThreadsAreSeparated(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	mk := func(who, body string) ThreadView {
		return ThreadView{Thread: domain.Thread{Comments: []domain.Comment{
			{Attribution: domain.Attribution{ActorLogin: attrLogin(who), ActorDisplay: who}, Body: body}}}}
	}
	views := map[int][]ThreadView{0: {mk("first-voice", "the first body"), mk("second-voice", "the second body")}}
	for _, path := range []struct {
		name string
		st   *Styles
	}{{"painted", st}} {
		t.Run(path.name, func(t *testing.T) {
			lines := RenderDoc(ParseBlocks([]byte("a block.\n"), path.st), views, nil, map[int]bool{0: true}, OnLine(9), 70, "cm", path.st)
			var seen []string
			for _, l := range lines {
				if l.IsThread {
					seen = append(seen, strings.TrimSpace(ansi.Strip(l.Text)))
				}
			}
			want := []string{"| " + attrLogin("first-voice"), "the first body", "", "| " + attrLogin("second-voice"), "the second body"}
			if len(seen) != len(want) {
				t.Fatalf("thread rows = %#v, want %#v", seen, want)
			}
			for i := range want {
				if seen[i] != want[i] {
					t.Fatalf("thread row %d = %q, want %q (all: %#v)", i, seen[i], want[i], seen)
				}
			}
		})
	}
}

// TestRailIsOneFocusNeverTwo pins the rail's central property: it marks exactly
// one place, either a block's own line or one of its threads. Cursor cannot
// hold both states, and the negative half of each case is what pins it -- a
// rail that appeared in both places would satisfy every positive assertion
// here.
func TestRailIsOneFocusNeverTwo(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	mk := func(who string) ThreadView {
		return ThreadView{Thread: domain.Thread{Comments: []domain.Comment{
			{Attribution: domain.Attribution{ActorLogin: attrLogin(who), ActorDisplay: who}, Body: who + " body"}}}}
	}
	views := map[int][]ThreadView{0: {mk("first"), mk("second"), mk("third")}}
	rail := "48;2;" + sgrRGB(t, th.Brand)
	// 100 columns: RailWidth draws no rail at all below 80, so a narrower case
	// would pass every negative assertion for the wrong reason.
	render := func(cur Cursor) []Line {
		return RenderDoc(ParseBlocks([]byte("a block.\n"), st), views, nil, map[int]bool{0: true}, cur, 100, "cm", st)
	}

	for _, tc := range []struct {
		name  string
		cur   Cursor
		onRow string
	}{
		{"focused on the line, the line alone carries it", OnLine(0), "a block."},
		{"focused on a thread, that thread alone carries it", OnThread(0, 1), "second body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := render(tc.cur)
			for _, row := range []string{"a block.", "first body", "second body", "third body"} {
				want := row == tc.onRow
				if got := strings.Contains(rowContaining(t, lines, row), rail); got != want {
					t.Fatalf("row %q carries the rail = %v, want %v -- the focus is %+v", row, got, want, tc.cur)
				}
			}
		})
	}
}

// TestResolvedThreadRecedesWhole pins that a resolved thread's attribution and
// bodies fall back to Dim, the "(resolved)" row's own colour, so the card
// recedes as ONE piece. Before this only the label dimmed, leaving the
// attribution in Accent — the colour of live state — directly beneath a row
// saying the thread was settled.
//
// The open case is asserted alongside, because a rule that dimmed EVERY card
// would satisfy the resolved half and say nothing at all.
func TestResolvedThreadRecedesWhole(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	mk := func(resolved bool) ThreadView {
		return ThreadView{Thread: domain.Thread{Resolved: resolved, Comments: []domain.Comment{
			{Attribution: domain.Attribution{ActorLogin: "dana-codes", ActorDisplay: "dana"}, Body: "the body"}}}}
	}
	for _, tc := range []struct {
		name     string
		resolved bool
		want     string
	}{
		{"resolved recedes to Dim", true, th.Dim},
		{"open keeps Accent on its attribution", false, th.Accent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := ThreadCardLines(mk(tc.resolved), 100, "calm-mountain", st)
			var head, body string
			for _, l := range lines {
				switch {
				case strings.Contains(ansi.Strip(l), "| dana"):
					head = l
				case strings.Contains(ansi.Strip(l), "the body"):
					body = l
				}
			}
			if head == "" || body == "" {
				t.Fatalf("card is missing a header or a body: %q", lines)
			}
			if got, want := fgOver(t, head, "| dana"), "38;2;"+sgrRGB(t, tc.want); got != want {
				t.Fatalf("attribution foreground = %q, want %q (%s)", got, want, tc.want)
			}
			// ITALIC on the attribution is the other half: bodies are italic
			// whatever their state, so this is what carries the resolution up
			// into the header and settles the card as one voice. Asserted
			// against the OPEN case, which must stay upright -- an italic on
			// every header would say nothing.
			if got := st.CardHeaderResolved.GetItalic(); got != tc.resolved && tc.resolved {
				t.Fatal("a resolved thread's attribution is upright; the card only half recedes")
			}
			if !tc.resolved && st.CardHeader.GetItalic() {
				t.Fatal("an OPEN thread's attribution is italic, so italic cannot mean resolved")
			}
			// The BODY is checked too: dimming only the attribution would
			// leave a settled thread's words in full Text weight beneath it.
			bodyWant := th.Text
			if tc.resolved {
				bodyWant = th.Dim
			}
			if got, want := fgOver(t, body, "the body"), "38;2;"+sgrRGB(t, bodyWant); got != want {
				t.Fatalf("body foreground = %q, want %q (%s)", got, want, bodyWant)
			}
		})
	}
}

// TestEveryDocumentRowSitsBetweenMargins pins the ground either side of the
// document. The left margin has always been there and carries the cursor; the
// right one is new, and without it prose ran into the terminal's edge.
//
// EVERY line, including the blank ones: the first attempt painted the far
// margin only on rows that carried content, which drew a ragged block above
// the empty space beneath them rather than a margin.
func TestEveryDocumentRowSitsBetweenMargins(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := NewStyles(th)
	v := singleCommentCard(domain.Attribution{ActorLogin: "dana-codes", ActorDisplay: "dana"})
	source := []byte("# A heading\n\nsome prose.\n\n```\ncode\n```\n")
	rail := "48;2;" + sgrRGB(t, th.Rail)
	cursorBand := "48;2;" + sgrRGB(t, th.Brand)
	lines := RenderDoc(ParseBlocks(source, st), map[int][]ThreadView{1: {v}}, []ThreadView{v},
		map[int]bool{1: true}, OnLine(1), 100, "cm", st)
	if len(lines) == 0 {
		t.Fatal("no lines rendered")
	}
	for i, l := range lines {
		// Rail OR the cursor's Brand: the LEFT margin doubles as the cursor
		// band, which is the whole reason it was there before this change.
		if !strings.HasPrefix(l.Text, "\x1b["+rail) && !strings.HasPrefix(l.Text, "\x1b["+cursorBand) {
			t.Fatalf("line %d does not open on a margin: %q", i, l.Text)
		}
		// The LAST escape must be the margin's too, which is what says the
		// row ends in ground rather than merely containing some somewhere.
		seqs := sgrSeq.FindAllStringIndex(l.Text, -1)
		tail := l.Text[seqs[len(seqs)-2][0]:]
		if !strings.Contains(tail, rail) {
			t.Fatalf("line %d does not close on the ground margin: %q", i, l.Text)
		}
	}

	// And below railMinWidth there is no margin at all, on either side: a
	// style with a background emits its escape even for an empty string, so
	// a collapsed margin is easy to leave behind as a zero-width colour.
	for _, l := range RenderDoc(ParseBlocks(source, st), nil, nil, nil, OnLine(0), 79, "cm", st) {
		if strings.Contains(l.Text, rail) {
			t.Fatalf("the margin must collapse below railMinWidth: %q", l.Text)
		}
	}
}

// darkStyles is the default theme's Styles, built explicitly for the renderers
// rather than left to the nil that stands for it: a test that names the theme
// it expects is legible where a bare nil is not. It takes testing.TB so the
// benchmarks in search_test.go parse with the same styles the review view
// does.
func darkStyles(tb testing.TB) *Styles {
	tb.Helper()
	th, err := theme.Lookup(theme.DefaultName)
	if err != nil {
		tb.Fatal(err)
	}
	return NewStyles(th)
}

// lightStyles is the OTHER preset, for the one assertion that needs two Styles
// whose rendered output actually differs: two Styles built from the same theme
// paint byte-identical rows, so they cannot tell a key that reads the styles
// from one that ignores them.
func lightStyles(tb testing.TB) *Styles {
	tb.Helper()
	th, err := theme.Lookup("light")
	if err != nil {
		tb.Fatal(err)
	}
	return NewStyles(th)
}

// TestNilStylesAreTheDefaultStyles pins the rule both exported render doors
// answer for a nil *Styles: A NIL ONE IS THE DEFAULT ONE, NOT AN UNPAINTED
// RENDERING.
//
// EQUAL TO THE DEFAULT AND UNEQUAL TO THE OTHER PRESET is what makes this an
// assertion about WHICH styles were substituted rather than about surviving the
// call. A substitution that reached for light, or invented an empty Styles,
// passes the first half and fails the second; a door that quietly rendered
// nothing at all fails the length guard above both.
//
// THE BLOCKS ARE PARSED ONCE, WITH STYLES, and that is the isolation this test
// needs rather than an incidental tidiness: ParseBlocks takes a nil *Styles
// legitimately and leaves both projections empty for it, so re-parsing per case
// would feed the doors different DOCUMENTS and the comparison would be
// measuring ParseBlocks instead of the door.
func TestNilStylesAreTheDefaultStyles(t *testing.T) {
	dark, light := darkStyles(t), lightStyles(t)
	source := []byte("# Heading\n\nA paragraph with `code` in it.\n\n| a | b |\n| - | - |\n| 1 | 2 |\n")
	blocks := ParseBlocks(source, dark)
	view := singleCommentCard(domain.Attribution{ActorLogin: "dana-codes", ActorDisplay: "dana"})

	for _, tc := range []struct {
		name   string
		render func(st *Styles) []string
	}{
		{"RenderDoc", func(st *Styles) []string {
			return collectText(RenderDoc(blocks, map[int][]ThreadView{0: {view}}, nil, map[int]bool{0: true}, OnLine(0), 80, "cm", st))
		}},
		{"ThreadCardLines", func(st *Styles) []string {
			return ThreadCardLines(view, 80, "cm", st)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.render(nil)
			if len(got) == 0 {
				t.Fatalf("%s rendered no lines at all, which would make both comparisons below vacuous", tc.name)
			}
			if want := tc.render(dark); !slices.Equal(got, want) {
				t.Errorf("a nil *Styles did not render the DEFAULT theme:\n nil = %q\ndark = %q", got, want)
			}
			if other := tc.render(light); slices.Equal(got, other) {
				t.Errorf("a nil *Styles rendered byte-identically to the LIGHT preset, so equality with dark proves nothing about which styles were substituted: %q", got)
			}
		})
	}
}

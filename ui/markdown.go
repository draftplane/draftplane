package ui

import (
	"charm.land/lipgloss/v2"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

type BlockKind int

const (
	KindHeading BlockKind = iota
	KindParagraph
	KindCode
	KindListItem
	// KindTableRow is ONE ROW of a GFM table, THE HEADER ROW INCLUDED. The
	// Table itself is a container (see blockRoles), so a table of a header and
	// n body rows is n+1 Blocks and the thing a reviewer comments on is a row.
	// Two consequences, neither optional:
	//
	//   - the cells a row carries are ITS OWN and not the header's, which is
	//     what keeps SearchBlocks matching what the reader sees (see
	//     DisplayPlain); and
	//   - a header row and a body row are told apart by Block.Table and by
	//     nothing else, because both are built by the same tableBlock helper
	//     out of the same kind.
	//
	// The accepted consequence: a comment on "the table as a whole" is not
	// expressible -- there is no Block for the table, so a reviewer comments on
	// a row.
	//
	// APPENDED, never inserted: a later addition must not renumber this
	// constant or its siblings.
	KindTableRow
	// KindRule is a THEMATIC BREAK -- `---`, `***`, `___` -- and it is the
	// one kind whose Text is MARKUP ALONE. renderBlockPainted draws the rule
	// the markup means and never puts those bytes on screen.
	//
	// IT CARRIES ITS SOURCE LINE ANYWAY, because Text is the anchor side of
	// the source mapping and a Block that left it empty would be a hole in
	// that mapping -- and an empty range fails TestParseBlocksSourceMapping's
	// SrcStart < SrcEnd invariant.
	//
	// WHAT FOLLOWS FROM THE BYTES BEING MARKUP is that no review fact can be
	// keyed to them: Anchorable (ui/anchor.go) answers false here and both
	// block-aimed writes in app/ refuse on it. THAT REFUSAL IS THE WHOLE OF
	// WHAT STANDS BETWEEN A COMMENT AND A SEPARATOR -- reanchor.CreateAnchor's
	// three-word floor stops the span `---`, but a break may be written
	// `- - -`, which Normalize counts as three words and CreateAnchor accepts.
	//
	// APPENDED, never inserted -- see KindTableRow above.
	KindRule
)

// Block is one anchorable unit of the document. Every rendered line maps back
// to exactly one Block, and every Block knows its source byte range — this is
// the source-mapping facility the spike exists to prove out.
//
// Text is the exact source slice (the anchor side of the mapping); Display is
// the styled projection with inline markdown rendered (the screen side).
//
// A PROJECTION CAN CARRY '\n', on exactly one kind. renderInlines draws a
// soft or hard line break as a space, so for every other kind the three
// Display fields are one line; a KindTableRow's are one line PER CELL (see
// Cells). Both readers already cope: wrapPlain splits on '\n' after wrapping,
// and SearchBlocks matches through reanchor.Normalize, which collapses the
// newline along with every other whitespace run.
type Block struct {
	Kind        BlockKind
	HeadingPath []string
	Level       int
	SrcStart    int
	SrcEnd      int
	// Text is the EXACT source slice, source[SrcStart:SrcEnd] -- not
	// normalised, not sanitised, not stripped of markup. It is the anchor
	// side of the mapping and reanchor searches for it verbatim, so anything
	// done to it here is a range that cannot be found again.
	//
	// IT IS A BYTE SPAN AND NOT A JOIN OF THE NODE'S LINE SEGMENTS, and
	// inside a quotation that shows: goldmark strips the `> ` from the
	// FIRST line's segment but the span between the first segment's start
	// and the last one's stop still runs over every `> ` in between. So a
	// multi-line block inside a quotation carries the source's own
	// blockquote markers on its continuation lines. That is correct for an
	// anchor -- the markers ARE in the file -- and it reaches the screen on
	// the two arms that draw Text rather than a projection.
	//
	// RECORDED, NOT FIXED, and deliberately: stripping them here would
	// break the anchor side, and stripping them at the arm is a projection
	// decision (which marker, at which depth, in a fence that may contain a
	// real '>') that no measurement in reach settles.
	Text string
	// Display is the styled projection: the block's inline markdown
	// rendered for the Doc background, which is the screen side of the
	// mapping. "" for a block ParseBlocks was handed no Styles, and for the
	// kinds with no inline projection at all -- see DisplayPlain, which
	// lists them.
	//
	// ONE ARM APPENDS RAW SOURCE TO IT, which is the exception to "inline
	// markdown rendered" and is stated here rather than only at the arm
	// because a reader auditing this field as a channel will not find the
	// arm. A KindTableRow whose row has MORE cells than the header has the
	// excess discarded by the parser before the AST exists, so those bytes
	// reach no cell and no inline projection; tableRowCells appends them to
	// the last cell verbatim, pipes and all, rather than dropping text a
	// reviewer can see in their editor. Anything treating Display -- or the
	// Cells it is joined from -- as already-projected, escape-stripped,
	// markup-resolved is wrong for that one span. It is 2 of a real corpus's
	// 826 body rows, of 438 and 897 characters.
	Display string
	// DisplayCard is Display rendered against the Card background instead of
	// Doc, for the rows of a block that carries comments. It is precomputed
	// for EVERY block, not only commented ones, because a comment can be
	// added while the document is open and blocks are not re-parsed when it
	// is — so the alternative projection has to be waiting.
	DisplayCard string
	// DisplayPlain is Display with the terminal styling taken back off: the
	// characters the reader actually sees, and nothing else. It is what
	// SearchBlocks matches, because Text is the source and the source is not
	// what is on screen -- "The **deploy** step" shows no asterisks and a
	// link shows no URL.
	//
	// It is the SAME WALK as Display, deliberately, so the two cannot drift
	// into disagreeing about what text is on screen. Unlike Display and
	// DisplayCard it is computed even when ParseBlocks is handed no styles:
	// see plainProjection.
	//
	// A TABLE ROW'S IS ITS OWN CELLS AND NOTHING ELSE. Under a grid the header
	// is drawn once, on its own Block, so a per-row label would be searchable
	// text that is on screen nowhere near that row. The governing rule is
	// search what the reader sees.
	//
	// EMPTY on the four kinds with no inline projection, and what stands in
	// for it is NOT the same on all four. A HEADING and a FENCED CODE BLOCK
	// are drawn from Text directly (renderBlockPainted reads b.Text, never
	// displayIn). ParseBlocks' FALLBACK arm is drawn through displayIn, whose
	// own fallback is reanchor.Normalize(Text) -- whitespace-collapsed, so a
	// three-line HTML block draws as one row of words. And a KindRule draws
	// nothing of its Text at all (see KindRule). searchText (ui/search.go)
	// falls back to Text for all four, which is why `---` is findable: the
	// bytes are in the document even where none of them is on screen.
	//
	// A FIFTH THING IS EMPTY AND IS NOT ONE OF THEM: a KindTableRow whose
	// cells are ALL EMPTY, which joinCells joins to "". That "" is a
	// MEASUREMENT and not an absence -- the row has cells, they were
	// projected, and there is nothing in them -- so neither reader falls back
	// for it. Cells is what tells the two apart at both call sites: unguarded,
	// `|  |  |` drew `| | |` on screen and answered a search for `|`.
	DisplayPlain string
	// Cells is a KindTableRow's cells kept APART, each carrying the same
	// three projections the whole block does, and it is nil on every other
	// kind. The three fields above are these cells joined with '\n'; this is
	// the field a grid is drawn from, because THE JOIN DOES NOT INVERT INTO
	// COLUMNS. An empty cell contributes no line to it (joinCells), so
	// splitting one back gives the cells that had text in them and nothing
	// about which column each sat in: 31 of a real corpus's 2,543 cells are
	// empty and are NOT the last cell of their row.
	//
	// PRECOMPUTED THREE WAYS FOR EVERY ROW, on DisplayCard's rule and for its
	// reason: a comment can be added while the document is open and blocks
	// are NOT re-parsed when it is. Display and DisplayCard are "" for a
	// block ParseBlocks was handed no styles, exactly as the joined fields
	// are; DisplayPlain is computed either way.
	//
	// AN EMPTY CELL IS KEPT and only the JOIN drops it, which is the one
	// difference between this field and the three above. A grid is columns: a
	// row that omitted its empty cells would put the next cell's text in the
	// wrong column, and a body row that GFM padded to the header's width is a
	// row with something to say -- this column exists and holds nothing.
	//
	// IT IS ALSO WHAT SAYS AN EMPTY PROJECTION WAS COMPUTED, which is a second
	// reader and not a restatement of the first. A row whose cells are all
	// empty joins to "", the same "" a block with no projection at all has;
	// displayIn (ui/render.go) and searchText (ui/search.go) both ask this
	// field to tell the two apart.
	Cells []TableCell
	// Table is which table this row belongs to and which of its rows it is.
	// It is the ZERO VALUE -- ID 0, Header false -- on every kind but
	// KindTableRow, which is TableRef's own ruling: no table is table 0.
	Table     TableRef
	ListDepth int
	// QuoteDepth is how many blockquotes enclose this block, and it is the
	// whole of what says these words are a quotation: a Blockquote is a
	// CONTAINER and emits no Block of its own (see blockRoles), so the fact
	// lives on the blocks it contains or it lives nowhere. Two readers --
	// withQuoteBar (ui/painted.go) draws one bar per level from it, and
	// InferTitle (ui/title.go) refuses a quoted heading the document's own
	// title.
	QuoteDepth int
	// CodeIndent is how many columns of leading WHITESPACE the source strips
	// from a fence's own content lines, and it is set on KindCode alone --
	// zero on every other kind, where nothing reads it.
	//
	// IT EXISTS BECAUSE Text IS A SPAN AND NOT A JOIN, which Text's own doc
	// comment says and which has a second consequence beside the quoted-marker
	// one recorded there. goldmark removes a fence's indentation from every
	// content line's segment, but the span from the FIRST segment's start to
	// the LAST one's stop runs straight over the indentation of every line in
	// between -- so a fence indented two columns has a flush first line and
	// every line after it two columns further right.
	//
	// THE COUNT AND NOT THE REPAIR, which is the shape ListDepth and QuoteDepth
	// already have: the fact is taken once where the source is in hand, and the
	// arm decides what to do with it (renderBlockPainted's KindCode arm). Text
	// itself must not move -- it is the anchor side of the mapping and reanchor
	// searches the raw document for it verbatim.
	//
	// WHITESPACE ONLY, AND ZERO FOR A QUOTED FENCE. srcIndent answers 0 the
	// moment the prefix holds anything that is not a space, so a "> " marker is
	// never counted and the quoted case is left exactly as Block.Text records
	// it.
	//
	// THE SEARCH INDEX IS UNMOVED. searchText (ui/search.go) falls back to Text
	// for a KindCode block, so what is indexed still carries the container's
	// indentation while the screen no longer draws it. Neither side has a
	// reader: SearchBlocks normalises BOTH with reanchor.Normalize, which is
	// strings.Fields, so leading indentation is invisible to the index in
	// either direction.
	CodeIndent int
}

// TableCell is ONE CELL of a table row, projected the three ways a Block is:
// for the Doc background, for the Card background, and with the styling taken
// back off. See Block.Cells for why a cell is a payload of its own rather than
// something a renderer can split out of Block.Display.
//
// IT CARRIES THE RENDERED PROJECTION AND NEVER THE SOURCE SLICE, which is what
// makes it a different channel from Text. A cell whose source is
// "\x1b[31mred" comes back with THE INTRODUCER BROKEN -- goldmark ends a Text
// node at the '[' it has to consider a link opener, and renderInlines renders
// each leaf with its full style -- where the raw slice would carry the sequence
// whole. The plain projection is "red": with zero styles the leaves rejoin and
// stripStyling takes the reassembled sequence off.
//
// THE ONE SPAN THAT IS RAW is the DISCARDED TAIL appended to a row's last
// cell, recorded and measured at Block.Display. It is text GFM threw away
// before the AST existed, so there is no inline walk to project it with.
type TableCell struct {
	Display      string
	DisplayCard  string
	DisplayPlain string
}

// TableRef is which table a KindTableRow belongs to and which of its rows it
// is -- the whole of what a renderer has to answer both questions from, and
// the reason it exists is that NEITHER IS DERIVABLE from the block list.
//
// GROUPING IS NOT ADJACENCY, and the counter-example is the simplest document
// anybody would write: two GFM tables separated by a BLANK LINE, which produce
// four KindTableRow blocks in a row with nothing at all between them. Two
// tables in adjacent LIST ITEMS and one quoted beside one not do the same;
// only a thematic break between them puts a block of another kind in between.
// See TestAdjacentTablesAreToldApartByTheirBlocks, which pins all of it.
//
// NOR IS THE SOURCE GAP, which is the heuristic in reach -- "exactly one line
// break between two rows continues a table" -- and it is wrong in BOTH
// directions. It splits a table that should not be split: the gap between a
// header and its own first body row is "\n|---|---|\n", TWO line breaks,
// because the delimiter line belongs to no node. And it joins two tables that
// should not be joined: across two tables in adjacent list items the gap is
// "\n- ", ONE line break. Over a real corpus the gap rule reports 230 tables
// where the identity reports 115.
//
// HEADER OR BODY IS NOT DERIVABLE EITHER: tableBlock builds both out of the
// same kind with the same fields, and under a grid the header row is styled
// differently from the rows beneath it.
type TableRef struct {
	// ID numbers the document's tables in the order ParseBlocks meets them,
	// from 1. It is 0 on every block that is not a table row, which is the
	// zero value doing the right thing rather than a sentinel to remember:
	// no table is table 0.
	//
	// DOCUMENT-SCOPED, not global. ParseBlocks is called per document and
	// the numbering restarts with it, so an ID is only meaningful against
	// the block list it came out of.
	ID int
	// Header is true for the ONE row per table whose cells came from the
	// header line -- the row GFM's `|---|` delimiter sits under. Every table
	// has exactly one, because goldmark builds the TableHeader from the
	// header and delimiter lines before it looks for a body row, and
	// ParseBlocks emits it whether or not any body row follows.
	Header bool
}

func segmentRange(n ast.Node) (int, int, bool) {
	lines := n.Lines()
	if lines.Len() == 0 {
		return 0, 0, false
	}
	return lines.At(0).Start, lines.At(lines.Len() - 1).Stop, true
}

// blockRange is the source range a Block for n anchors to: n's own line
// segments, and for a table's header, one of its rows, or a thematic break
// that line's whole SOURCE LINE.
//
// THESE ARE THE CONTENT KINDS WHOSE TEXT IS NOT IN THEIR OWN SEGMENTS, which
// is why this sits in FRONT of the walk's segment test rather than in the
// switch behind it: asked for segmentRange each answers false and falls to the
// walk's descend branch, which is not where a node that owes a Block belongs.
// goldmark's table parser hands every segment to the CELLS, so a row's text is
// in its children; a THEMATIC BREAK has no text under it at all, so without
// this its `---` would be in no Block's range and the separator would render
// as nothing.
//
// Pos IS NOT A TABLE FIELD, which is what lets one technique serve both.
// goldmark calls SetPos on EVERY block node it opens, at the line's start plus
// the enclosing container's offset -- so a rule inside a blockquote points past
// the `> ` markers and an indented `   ---` points at the first dash.
//
// THE WHOLE LINE AND NOT THE SPAN ITS CELLS COVER, which is a real byte range
// and not a tidiness preference. A body row with MORE cells than the header
// has the excess DISCARDED by the parser (which is what GFM specifies), so
// those bytes are in no node's segments at all and a cell span would stop
// short of them -- by 901 bytes on the worse of a real corpus's two such
// rows. Text is the anchor side of the source mapping, so a range that stops
// short is a range that cannot be found again. tableRowCells is the other
// half of the same fact: it draws that discarded tail so the bytes are on
// screen as well as anchored.
//
// The trailing whitespace of the line is left out -- a source slice either
// way, since the trim only moves the end -- so a CRLF document does not hang a
// '\r' off the end of every row's anchor text.
func blockRange(n ast.Node, source []byte) (int, int, bool) {
	start, ok := 0, false
	switch n.(type) {
	case *east.TableRow, *east.TableHeader, *ast.ThematicBreak:
		// Pos is where the line begins, and goldmark sets it on every block
		// node its parser opens and again on every row and header its table
		// transformer builds, so the bounds test is not a case any document
		// reaches. It is here because what it would otherwise be is an index
		// panic, and ParseBlocks runs on the UI loop over whatever markdown a
		// reviewer opens. Falling through to segmentRange is also the answer a
		// future goldmark that gave these nodes segments of their own would
		// want.
		start = n.Pos()
		ok = start >= 0 && start <= len(source)
	}
	if !ok {
		return segmentRange(n)
	}
	stop := start
	for stop < len(source) && source[stop] != '\n' {
		stop++
	}
	for stop > start && isLineSpace(source[stop-1]) {
		stop--
	}
	// A row whose line is all whitespace has nothing to anchor, so answering
	// false sends it to the walk's descend branch, where its cells -- declared
	// CONTENT -- carry whatever is in them. That is the right answer rather
	// than a Block with an empty range, which TestParseBlocksSourceMapping's
	// SrcStart < SrcEnd invariant rules out anyway. A THEMATIC BREAK CANNOT
	// REACH IT: its line carries at least three marks by definition.
	return start, stop, start < stop
}

func isLineSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\r' }

// srcIndent is how many leading SPACES stand between the start of at's own
// source line and at itself -- the columns a container strips from a block's
// content and the span puts back on every line but the first. See
// Block.CodeIndent, which is its only field.
//
// SPACES AND NOT WHITESPACE, and 0 for anything else in front. A "> " marker
// makes this 0, which keeps the quoted-fence case exactly where Block.Text
// records it; a TAB makes it 0 too, because a tab in the indent is partially
// consumed by goldmark's own padding arithmetic and stripping it whole at the
// arm would take columns the source did not.
func srcIndent(source []byte, at int) int {
	start := at
	for start > 0 && source[start-1] != '\n' {
		start--
	}
	for i := start; i < at; i++ {
		if source[i] != ' ' {
			return 0
		}
	}
	return at - start
}

// listDepth and quoteDepth are how many lists, and how many blockquotes,
// enclose n. Both count off the PARENT CHAIN rather than off blockRoles: a
// container emits no Block, so the only place its depth survives is the chain
// above the blocks it holds. Reading the chain is also what keeps these two
// agreeing with the walk, which descends into a segment-less node whatever the
// table says about it (see blockRoleOf).
func listDepth(n ast.Node) int { return enclosing(n, ast.KindList) }

func quoteDepth(n ast.Node) int { return enclosing(n, ast.KindBlockquote) }

func enclosing(n ast.Node, kind ast.NodeKind) int {
	depth := 0
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Kind() == kind {
			depth++
		}
	}
	return depth
}

// blockRole is which side of ParseBlocks' partition a block-level node sits
// on: it either yields a Block of its own, or it is a container whose
// children do.
//
// BOTH IS WRONG -- a Blockquote and the Paragraph inside it each emitting
// means the quoted words render twice -- and so is NEITHER, which is how a
// construct comes to be silently absent from a document under review. Which
// of the two a kind is must therefore be DECLARED, in blockRoles, rather than
// fall out of the walk's control flow.
type blockRole int

const (
	// roleUndeclared is the absence of a decision rather than a third answer
	// to it: it is what blockRoles' zero value gives a kind nobody has put in
	// the table. See blockRoleOf for what the walk does with one -- which is
	// deliberately quiet -- and TestParseBlocksWalkIsTotal for where it is
	// loud.
	roleUndeclared blockRole = iota
	roleContainer
	roleContent
)

// blockRoles declares the partition for every block-level node goldmark's
// parser can hand the walk.
//
// THIS IS THE EXTENSION POINT. Teaching ParseBlocks a new construct is a line
// HERE and nothing else in the walk: no second allowlist, no branch of its own
// before the switch. A construct that needs a shape of its own on screen wants
// three more edits, all outside the walk's control flow -- a BlockKind
// APPENDED to the iota above, an arm in ParseBlocks' switch that builds it, and
// an arm in renderBlockPainted (ui/painted.go) that draws it. A CONTENT
// construct that only needs to be readable needs none of the three: the
// fallback in that switch's default carries its source text.
//
// A CONTAINER-SHAPED CONSTRUCT MUST BE DECLARED HERE, and the declaration is
// a statement of INTENT that the build-time check enforces -- not a supplier
// of anything the walk reads. An undeclared container's children still reach
// the screen (the walk descends when there are no segments, see ParseBlocks),
// and a fact hanging off the container is still derivable without it: deleting
// ast.KindBlockquote from this table gets byte-identical Blocks back,
// QuoteDepth included, because quoteDepth counts ancestors off the AST's own
// parent chain (TestParseBlocksUndeclaredContainerKeepsItsSubtree).
//
// WHERE A MISSING DECLARATION WOULD ACTUALLY BITE is a container that carries
// LINE SEGMENTS of its own: undeclared, it emits one Block for the container
// and skips its children, losing the subtree. goldmark v1.8.4 has none WITH THE
// EXTENSIONS THIS PARSER ENABLES, and the scope clause is load-bearing rather
// than pedantic -- extension/definition_list.go appends a line to
// DefinitionTerm, so turning that extension on would add a segment-carrying
// leaf. An extension goes on parseDocument's call in the commit that declares
// what it brings, and TestParseBlocksWalkIsTotal fails on the kind nobody has
// looked at yet.
//
// THE MIRROR HAZARD IS REAL AND WAS PAID FOR ONCE: a node declared a CONTAINER
// whose children emit nothing. The declaration is believed, so the walk
// descends and the region is simply gone -- no segment test saves it, because
// the segment test is only reached by content. WHEN A DECLARATION IS GENUINELY
// ARGUABLE, CONTENT IS THE SIDE THAT DEGRADES.
//
// An extension's INLINE nodes (TaskCheckBox, Strikethrough, FootnoteLink) are
// outside this partition altogether -- the walk turns them away on Type before
// it ever asks for a role -- so they owe no line and must not be given one.
var blockRoles = map[ast.NodeKind]blockRole{
	// Containers: the children carry the text, so a container that emitted
	// as well would put that text on screen twice.
	ast.KindDocument:   roleContainer,
	ast.KindList:       roleContainer,
	ast.KindListItem:   roleContainer,
	ast.KindBlockquote: roleContainer,

	// A Table is an ordinary container: no segments, children that carry the
	// text. Its header and its rows are content (below), so the walk never
	// descends past them into a cell.
	east.KindTable: roleContainer,

	// Content with an arm of its own in the switch below.
	ast.KindHeading:         roleContent,
	ast.KindParagraph:       roleContent,
	ast.KindTextBlock:       roleContent,
	ast.KindFencedCodeBlock: roleContent,
	ast.KindCodeBlock:       roleContent,
	// ThematicBreak is content with NO TEXT UNDER IT AT ALL, and it is the
	// one kind here whose Block range does not come from segments: goldmark
	// appends it none, so blockRange reads its line off Pos instead. Declared
	// content on the FALLBACK, it rendered as nothing at all, which is the
	// failure a fallback cannot catch: the fallback carries a node's SOURCE
	// TEXT, and a node with no segments never reaches it. That is what an arm
	// is for.
	ast.KindThematicBreak: roleContent,

	// extension.Table's other three, all content, and the reason is one rule
	// applied three times: CONTENT MEANS "THIS NODE IS ANSWERABLE FOR THE TEXT
	// UNDER IT", not "this node always emits".
	//
	// TableRow and TableHeader carry no line segments of their own -- see
	// blockRange, which is what gets them to the switch at all -- and their
	// arms return WalkSkipChildren, which is what puts a row's cells into ONE
	// Block rather than spilling them onto the screen as n fragments.
	//
	// TABLECELL IS THEREFORE UNREACHABLE by this walk, and is declared content
	// anyway, deliberately: a cell is a LEAF holding one line segment, so if
	// either arm above ever stopped claiming its subtree the cell would carry
	// its own text to the fallback instead of vanishing. Declaring it a
	// CONTAINER is what the first version of this did, and it cost a
	// regression -- a table with a header and no body rows rendered as
	// nothing.
	east.KindTableHeader: roleContent,
	east.KindTableRow:    roleContent,
	east.KindTableCell:   roleContent,

	// Content on the fallback. Both carry their own lines, so declaring them
	// is what puts them on screen; neither occurs in any document of the plan
	// corpus.
	ast.KindHTMLBlock:               roleContent,
	ast.KindLinkReferenceDefinition: roleContent,
}

// blockRoleOf is the role declared for n's kind, and roleUndeclared for a kind
// nobody has declared.
//
// THE WALK TREATS AN UNDECLARED KIND AS CONTENT rather than stopping on it.
// ParseBlocks runs on the UI loop over whatever markdown a reviewer opens and
// cannot report an error -- its three callers take a []Block -- so the runtime
// answer has to be one that keeps the text on screen either way. The loud half
// is TestParseBlocksWalkIsTotal, which fails on the undeclared kind at build
// time, where a missing declaration is a programmer's problem and not a
// reader's.
//
// WHAT MAKES IT SAFE FOR A CONTAINER IS NOT THIS FUNCTION but the segment test
// in the walk, and the distinction matters because getting it wrong loses a
// whole subtree rather than one node. An undeclared LEAF carries its own line
// segments, so it emits them once and skips its children, which is right. An
// undeclared CONTAINER carries NO segments -- on goldmark v1.8.4 only leaf
// parsers append lines -- so it reaches the walk's "no segments" branch, which
// DESCENDS, and its children are then classified on their own kinds. Emitting
// nothing and skipping the children is the failure mode here, and it is the one
// the walk is written to avoid.
//
// THE GUARANTEE IS ABOUT UNDECLARED KINDS AND NOT ABOUT WRONG ONES. A kind
// declared roleContainer is BELIEVED: the walk descends without asking about
// segments, and if nothing under it emits, the region is gone. There is no
// safety net on that side -- the segment test above is reached only by content
// -- so a declaration that is arguable belongs on the CONTENT side, and
// blockRoles says which of them are arguable and why.
func blockRoleOf(n ast.Node) blockRole {
	return blockRoles[n.Kind()]
}

// parseDocument is the ONE place this package builds a markdown parser:
// ParseBlocks reads documents with it and the totality tests parse with it,
// so the partition is always asserted against the parser production actually
// uses.
//
// A SECOND CONSTRUCTION WOULD MAKE TestParseBlocksWalkIsTotal BLIND TO EXACTLY
// THE KINDS MOST LIKELY TO BE UNDECLARED. A test holding its own goldmark.New()
// sees only what the DEFAULT parser emits, so an extension enabled here would
// introduce four or five new node kinds and that test would stay green over
// every one of them -- and an extension is where a new kind comes from in the
// first place.
//
// It is therefore also where an extension goes: goldmark.WithExtensions(...)
// belongs on this call, in the same commit that declares the kinds it brings
// in blockRoles. The parser is built per call rather than cached, which is what
// the costs recorded at plainProjection were measured against; a shared
// instance is a separate question about goldmark's concurrency.
func parseDocument(source []byte) ast.Node {
	return goldmark.New(goldmark.WithExtensions(extension.Table)).Parser().Parse(text.NewReader(source))
}

// ParseBlocks flattens the goldmark AST into anchorable blocks, tracking the
// heading path the same way the reanchor prototype does (a block's path ends
// at its nearest enclosing heading; a heading's path includes itself).
//
// THE WALK IS TOTAL over the AST, and blockRoles is what makes it so: every
// block-level node is declared either a container the walk descends through
// or content that yields a Block, and a kind nobody declared still yields a
// Block rather than disappearing.
//
// st supplies the inline styles baked into each block's Display, and a nil st
// leaves it empty -- see below. Display is computed once here rather than per
// render, so a Model must be constructed with its final styles before
// RefreshFromSession first runs.
func ParseBlocks(source []byte, st *Styles) []Block {
	// A nil st leaves both projections EMPTY rather than styling them from a
	// fixed palette, and displayIn falls back to the normalized source. That
	// is what a caller wanting blocks without a screen actually wants --
	// session.Title infers from the source and never from Display.
	var base, codeSpan, cardBase, cardCodeSpan lipgloss.Style
	var breakSpace, cardBreakSpace string
	if st != nil {
		base, codeSpan, breakSpace = st.Text, st.CodeSpan, st.Text.Render(" ")
		cs := st.OnCard()
		cardBase, cardCodeSpan, cardBreakSpace = cs.Text, cs.CodeSpan, cs.Text.Render(" ")
	}
	doc := parseDocument(source)

	type stackEntry struct {
		level int
		title string
	}
	var stack []stackEntry
	var blocks []Block

	path := func() []string {
		p := make([]string, len(stack))
		for i, e := range stack {
			p[i] = e.title
		}
		return p
	}

	// tableID numbers the document's tables in the order the walk meets them
	// -- see TableRef.ID -- keyed on the Table node itself, which is the
	// PARENT of every header and every row goldmark's transformer builds.
	// Keying on the parent is what makes one function answer for both arms
	// below, and reading the id off the AST rather than off a counter is what
	// keeps it right for a table whose header is somehow not its first child.
	tables := map[ast.Node]int{}
	tableID := func(row ast.Node) int {
		table := row.Parent()
		id, ok := tables[table]
		if !ok {
			id = len(tables) + 1
			tables[table] = id
		}
		return id
	}

	// tableBlock is the Block a table's header or one of its rows yields:
	// cells is the node holding them and header says which of the two this
	// is. Shared by the two arms below so that a header row and a body row are
	// the SAME SHAPE with one flag between them rather than two constructions
	// that happen to agree.
	//
	// THREE WALKS, ONE PER PROJECTION: the two styled ones are skipped for a
	// caller with no Styles and the plain one never is (see plainProjection for
	// the measured cost of that rule). Each returns the cells kept apart --
	// Block.Cells -- and the three joined strings are built from them here.
	//
	// ListDepth is NOT set on either. A table inside a list draws at the
	// document's own left margin: renderBlockPainted indents from ListDepth on
	// KindListItem alone, so a depth recorded here would be a fact with no
	// reader. QuoteDepth is set, because withQuoteBar reads it for every kind
	// there is.
	tableBlock := func(cells ast.Node, header bool, start, stop, quote int) Block {
		doc := tableRowProjection(st, cells, source, stop, base, codeSpan, breakSpace)
		card := tableRowProjection(st, cells, source, stop, cardBase, cardCodeSpan, cardBreakSpace)
		plain := tableRowPlain(cells, source, stop)
		return Block{
			Kind:         KindTableRow,
			HeadingPath:  path(),
			SrcStart:     start,
			SrcEnd:       stop,
			Text:         string(source[start:stop]),
			Display:      joinCells(doc),
			DisplayCard:  joinCells(card),
			DisplayPlain: joinCells(plain),
			Cells:        tableCells(doc, card, plain),
			Table:        TableRef{ID: tableID(cells), Header: header},
			QuoteDepth:   quote,
		}
	}

	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if n.Type() == ast.TypeInline {
			// Inlines belong to the block that contains them and are
			// projected by renderInlines, never by this walk. REACHABLE,
			// not defensive: the no-segments branch below descends, and
			// what it descends into can be a leaf's inline children. This
			// is what keeps segmentRange from ever being asked about one --
			// Lines() PANICS on an inline node -- so the two are a pair and
			// neither can be removed without the other.
			return ast.WalkSkipChildren, nil
		}
		if blockRoleOf(n) == roleContainer {
			return ast.WalkContinue, nil
		}
		// Content, declared or not: one Block and no descent. See blockRoles
		// for the partition and blockRoleOf for why an undeclared kind lands
		// here instead of stopping the walk.
		start, stop, ok := blockRange(n, source)
		if !ok {
			// No source range, so no text to anchor and no Block -- and the
			// walk DESCENDS rather than skipping. Only goldmark's leaf
			// parsers append lines, so a node with none is either a leaf with
			// nothing in it (an empty heading, which has no children to
			// descend into anyway) or a CONTAINER NOBODY HAS DECLARED, whose
			// whole subtree would vanish here on a skip.
			//
			// This is also the one branch that reaches a node the emit below
			// must never see: descending can only turn up inlines under a
			// leaf, and the TypeInline guard above turns those away before
			// segmentRange is asked for a range that Lines() would panic on.
			return ast.WalkContinue, nil
		}
		quote := quoteDepth(n)
		switch node := n.(type) {
		case *ast.Heading:
			title := strings.TrimSpace(string(source[start:stop]))
			// A QUOTED HEADING TOUCHES THE STACK NOT AT ALL: it still renders
			// as a heading -- KindHeading at its own Level, sigil and styling
			// -- but the HEADING PATH IS THE ANCHOR (reanchor.Anchor is
			// heading path plus normalized span), and a quotation must not
			// restructure the anchor namespace of the document quoting it.
			//
			// NEITHER HALF IS OPTIONAL, and the pop is the half that is easy
			// to leave in: a quoted `# …` closes the quoting document's own
			// H1 and leaves every block after the quote anchored at the root,
			// which is a worse namespace than the extra level the push adds.
			// On one real corpus file that opens with a quoted H1, the pop left
			// 99 of its 184 blocks rooted at that heading -- a path
			// reanchor.ParseSections cannot produce -- of which 87 could not be
			// anchored at all.
			//
			// Not pushing is all this site owes the heading's OWN path --
			// path() is read after it, so the block reports the section it
			// sits in -- and it is NOT all the change owes. A quoted heading
			// now carries a path that EXISTS, so a caller treating any
			// KindHeading as a section would build a section anchor on the
			// enclosing section instead of erroring: see ui.AnchorsAsSection
			// (ui/anchor.go), which is where that question is answered now.
			if quote == 0 {
				for len(stack) > 0 && stack[len(stack)-1].level >= node.Level {
					stack = stack[:len(stack)-1]
				}
				stack = append(stack, stackEntry{level: node.Level, title: title})
			}
			blocks = append(blocks, Block{
				Kind:        KindHeading,
				HeadingPath: path(),
				Level:       node.Level,
				SrcStart:    start,
				SrcEnd:      stop,
				Text:        title,
				QuoteDepth:  quote,
			})

		case *ast.Paragraph, *ast.TextBlock:
			kind := KindParagraph
			depth := listDepth(n)
			if depth > 0 {
				kind = KindListItem
			}
			blocks = append(blocks, Block{
				Kind:         kind,
				HeadingPath:  path(),
				SrcStart:     start,
				SrcEnd:       stop,
				Text:         string(source[start:stop]),
				Display:      inlineProjection(st, n, source, base, codeSpan, breakSpace),
				DisplayCard:  inlineProjection(st, n, source, cardBase, cardCodeSpan, cardBreakSpace),
				DisplayPlain: plainProjection(n, source),
				ListDepth:    depth,
				QuoteDepth:   quote,
			})

		case *east.TableRow:
			// ONE BLOCK PER ROW -- see KindTableRow for the ruling and
			// tableRowCells for the shape. Text is the row's whole source
			// line (blockRange), so what a comment anchors to is the line a
			// reviewer would copy out of their editor, pipes and all.
			blocks = append(blocks, tableBlock(n, false, start, stop, quote))

		case *east.TableHeader:
			// UNCONDITIONALLY: under a grid the header is a row of the grid,
			// so it is a Block like any other and carries Table.Header to say
			// which row it is.
			//
			// A TABLE WITH NO BODY ROWS still arrives here and still emits, and
			// the shape looks exotic and is not: a GFM table is a header line
			// and a delimiter line, and goldmark's transformer builds the Table
			// from those two before it loops for body rows, so a two-line table
			// is a Table whose only child is this. It is also every table in a
			// document being edited, between the second keystroke on the
			// delimiter line and the first character of the first row. That
			// shape once rendered as nothing at all.
			//
			// The delimiter line is NOT on screen either way: goldmark
			// attributes it to no node's segments, the same as a fence's ```
			// lines and a setext heading's ==== underline.
			blocks = append(blocks, tableBlock(n, true, start, stop, quote))

		case *ast.ThematicBreak:
			// THE RULE IS DRAWN, NOT WRITTEN OUT -- renderBlockPainted's
			// KindRule arm draws a line across the body and never reads Text
			// -- so no projection is computed here: there are no inlines to
			// project, and displayIn (ui/render.go) is never asked.
			//
			// searchText (ui/search.go) IS still asked, and it falls back to
			// Text for a block with no projection, so a reader searching for
			// `---` finds the separators.
			//
			// ListDepth is NOT set, on tableBlock's ruling and for its reason.
			// QuoteDepth is, because withQuoteBar reads it for every kind
			// there is -- a rule inside a quotation draws its bars and stops
			// short by exactly their width.
			blocks = append(blocks, Block{
				Kind:        KindRule,
				HeadingPath: path(),
				SrcStart:    start,
				SrcEnd:      stop,
				Text:        string(source[start:stop]),
				QuoteDepth:  quote,
			})

		case *ast.FencedCodeBlock, *ast.CodeBlock:
			blocks = append(blocks, Block{
				Kind:        KindCode,
				HeadingPath: path(),
				SrcStart:    start,
				SrcEnd:      stop,
				Text:        string(source[start:stop]),
				QuoteDepth:  quote,
				CodeIndent:  srcIndent(source, start),
			})

		default:
			// THE FALLBACK: a content kind with no arm of its own, carried
			// by its exact source text so the construct is readable rather
			// than absent. KindParagraph and not a kind of its own because
			// the point of the fallback is that nobody has decided how to
			// draw this construct yet, and renderBlockPainted's default arm
			// already wraps text like prose; a kind of its own would be a
			// decision, and a decision belongs in an arm above.
			//
			// It computes NO projections. Display, DisplayCard and
			// DisplayPlain stay empty, which sends displayIn (ui/render.go)
			// and searchText (ui/search.go) to Text.
			//
			// WHAT IS DRAWN HERE IS reanchor.Normalize(Text) where a fence's is
			// Text itself, line for line, so a three-line HTML block draws as
			// one row of whitespace-collapsed words. Inside a quotation it
			// would also draw the source's own '> ' markers (see Block.Text).
			// Latent on this arm and only this arm: a real corpus holds no HTML
			// block and no link reference definition.
			blocks = append(blocks, Block{
				Kind:        KindParagraph,
				HeadingPath: path(),
				SrcStart:    start,
				SrcEnd:      stop,
				Text:        string(source[start:stop]),
				QuoteDepth:  quote,
			})
		}
		return ast.WalkSkipChildren, nil
	})

	return blocks
}

// inlineProjection is renderInlines, or "" when there are no styles to render
// with -- see ParseBlocks.
func inlineProjection(st *Styles, n ast.Node, source []byte, base, codeSpan lipgloss.Style, breakSpace string) string {
	if st == nil {
		return ""
	}
	return renderInlines(n, source, base, codeSpan, breakSpace)
}

// tableRowProjection is tableRowCells, or nil when there are no styles to
// render with -- the same bargain inlineProjection strikes, for the same
// reason: a caller parsing without a theme gets no Display and displayIn falls
// back to Text.
func tableRowProjection(st *Styles, cells ast.Node, source []byte, rowEnd int, base, codeSpan lipgloss.Style, breakSpace string) []string {
	if st == nil {
		return nil
	}
	return tableRowCells(cells, source, rowEnd, base, codeSpan, breakSpace)
}

// tableRowPlain is to tableRowCells what plainProjection is to renderInlines:
// the same walk with the styling taken off, computed UNCONDITIONALLY so
// SearchBlocks can match a table row for a caller that parsed without a theme.
//
// stripStyling and not ansi.Strip, for the reason plainProjection gives.
func tableRowPlain(cells ast.Node, source []byte, rowEnd int) []string {
	var plain lipgloss.Style
	out := tableRowCells(cells, source, rowEnd, plain, plain, " ")
	for i, cell := range out {
		out[i] = stripStyling(cell)
	}
	return out
}

// tableCells zips the three walks into the per-cell payload a grid is drawn
// from. The plain one is the spine because it is the one that always ran (see
// tableRowPlain); the styled two are nil for a caller with no Styles, and a
// cell of a nil walk is "" exactly as Display is on such a block.
//
// THE BOUNDS TESTS BELOW ARE LOAD-BEARING AND ARE HIT ON EVERY NIL-STYLES
// PARSE. With no Styles tableRowProjection returns nil, so len(doc) and
// len(card) are 0 while len(plain) is the row's cell count: the three ARE of
// different lengths, for every table in the document. session.Title
// (session/session.go) is a production caller on exactly that arm, so deleting
// either test panics with an index out of range on the first document
// containing a table.
func tableCells(doc, card, plain []string) []TableCell {
	out := make([]TableCell, len(plain))
	for i := range plain {
		out[i].DisplayPlain = plain[i]
		if i < len(doc) {
			out[i].Display = doc[i]
		}
		if i < len(card) {
			out[i].DisplayCard = card[i]
		}
	}
	return out
}

// joinCells is a row's cells as the ONE string Display, DisplayCard and
// DisplayPlain each hold, '\n' between them. The separator is what
// renderBlockPainted splits on and nothing else in a projection can produce
// one -- renderInlines emits breakSpace for a soft or hard break and never a
// newline.
//
// AN EMPTY CELL CONTRIBUTES NO LINE, which is the one thing this does beyond
// joining: renderBlockPainted draws one screen row per line, so an empty cell
// joined in would draw a BLANK ROW inside a table row. It costs the search
// index nothing, and it costs a grid nothing, because a grid is drawn from
// Cells, where every cell is kept (see Block.Cells for why column position
// makes that mandatory there and pointless here).
func joinCells(cells []string) string {
	var drawn []string
	for _, cell := range cells {
		if cell != "" {
			drawn = append(drawn, cell)
		}
	}
	return strings.Join(drawn, "\n")
}

// tableRowCells projects one table row as ITS OWN CELLS, one string per cell,
// in column order: cells is the node holding them, which is a TableRow for a
// body row and the TableHeader itself for a header row.
//
// NO LABELS. Under a grid the header is drawn once, as its own row, so a label
// here would be the header's text repeated on every row of the table -- and
// searchable text sitting nowhere near the row that carried it (see
// Block.DisplayPlain).
//
// A CELL IS THE RENDERED INLINE PROJECTION and never the source slice -- see
// TableCell, which is where that contract and its one exception are recorded.
//
// EVERY CELL IS RETURNED, including one with nothing in it: a body row with
// FEWER cells than the header is padded by the parser with empty ones, and a
// grid needs them to keep its columns aligned. renderInlines answers "" for a
// node with no inline children and only for one, whatever styles it is handed
// -- it never calls Render on a childless node, and a style with a background
// emits escapes even for "" -- so an empty string here IS an empty cell, on
// the styled and the plain walk alike.
//
// THE DISCARDED TAIL is why this takes rowEnd. A body row with MORE cells than
// the header has the excess thrown away by the parser before the AST exists
// (see blockRange), so those bytes reach no cell and no inline projection.
// They are appended RAW to the last cell, pipes and all: raw because there is
// no AST to project them from, and appended rather than dropped because a
// reviewer can see them in their editor and this view must not be the one place
// they are missing. It is 2 of a real corpus's 826 body rows.
//
// THE TAIL IS APPENDED ONLY WHEN THERE IS ONE, and the guard is load-bearing
// rather than an optimisation. base.Render("") is not "" for a style with a
// background -- 36 bytes of SGR in the default theme -- so appending an EMPTY
// tail unconditionally makes an empty last cell non-empty on the styled walk
// and not on the plain one, which is a column that draws itself differently on
// a commented row than on an uncommented one. TestTableRowCellProjectionsAgree
// pins it from both ends: an empty cell must be empty in all three, and a REAL
// tail must still be appended -- so a guard written on the empty CELL rather
// than on the empty TAIL would take the two real tails with it.
func tableRowCells(cells ast.Node, source []byte, rowEnd int, base, codeSpan lipgloss.Style, breakSpace string) []string {
	var out []string
	lastStop := -1
	for cell := cells.FirstChild(); cell != nil; cell = cell.NextSibling() {
		if lines := cell.Lines(); lines.Len() > 0 {
			lastStop = lines.At(lines.Len() - 1).Stop
		}
		value := renderInlines(cell, source, base, codeSpan, breakSpace)
		if cell.NextSibling() == nil && lastStop >= 0 && lastStop < rowEnd {
			if tail := discardedTail(source, lastStop, rowEnd); tail != "" {
				value += renderControls(tail, base)
			}
		}
		out = append(out, value)
	}
	return out
}

// discardedTail is the source between the last cell the parser kept and the
// end of the row's line, with the closing pipe and its padding taken off. It
// is "" for every well-formed row; see tableRowCells for what makes it
// anything else and why that is drawn rather than dropped.
func discardedTail(source []byte, from, to int) string {
	tail := string(source[from:to])
	return strings.TrimRight(tail, "| \t")
}

// plainProjection is the text a reader sees for a block with inlines: the
// same renderInlines walk Display is built from, run with zero styles and a
// bare " " for the line break, with whatever escapes it still emits stripped
// back off.
//
// UNCONDITIONAL, unlike inlineProjection: it takes no st and is not skipped
// when there is none. Display and DisplayCard are "" for a caller that parses
// without a theme, and a projection that inherited that emptiness would make
// SearchBlocks silently match nothing for exactly those callers.
//
// Sharing renderInlines rather than writing a second, simpler walk is the
// point of it -- the searched text and the drawn text come out of one
// traversal, so they cannot come to disagree. plainText (ui/inline.go) looks
// like the cheaper answer and is not: it glues the words either side of a
// soft line break together with no space between them and drops an autolink's
// URL entirely. It survives because its only caller hands it a code span,
// which has neither.
//
// THE STRIP IS NOT BELT-AND-BRACES. A zero lipgloss.Style does render text
// unchanged, but renderInlines calls .Bold(true)/.Italic(true)/.Underline(true)
// on whatever base it is given, and those emit escapes from a zero base too.
//
// stripStyling AND NOT ansi.Strip, WHICH IS A CORRECTNESS DIFFERENCE AND NOT A
// TASTE ONE. ansi.Strip is a terminal parser: it reads a raw 0x9F as an 8-bit
// APC introducer and drops it AND EVERY BYTE AFTER IT, so this projection
// answered `alpha` for a paragraph reading `alpha\x9fbeta gamma` while the
// styled Display carried the whole line to the screen -- a cell drawn short, and
// a SearchBlocks("gamma") that answered nothing about text the reader can see.
// stripStyling (ui/control.go) denies the parser that state and strips the rest
// unchanged; a string with no such byte in it takes the identical ansi.Strip
// path and comes back byte-for-byte what it was.
//
// WHAT IT COSTS, because ParseBlocks runs on the UI loop in
// Model.RefreshFromSession and every reader pays this whether they ever press f
// or not: about a fifth of a STYLED parse, where this is the third of three
// renderInlines walks per block, and roughly five times the parse on the
// NO-STYLE arm, where it is the only walk. The review view is always the styled
// arm (Model.New never holds a nil Styles); quoting the no-style ratio as its
// cost overstates it by an order of magnitude. It is the traversal and not the
// strip that costs. Re-derive with BenchmarkParseBlocks.
func plainProjection(n ast.Node, source []byte) string {
	var plain lipgloss.Style
	return stripStyling(renderInlines(n, source, plain, plain, " "))
}

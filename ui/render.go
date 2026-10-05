// Package ui renders a plan document and its review threads into a flat,
// source-mapped line list: every rendered row knows the block (or the
// unanchored section) it came from, so cursor movement, gutter marks, and
// anchor creation all resolve through that mapping instead of re-parsing.
package ui

import (
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/domain"
)

// Line is one rendered screen row, tagged with the block it came from. The
// tag is what makes the render source-mapped: cursor position, gutter marks,
// and anchor creation all resolve through it. BlockIdx is UnanchoredIdx for
// rows belonging to the unanchored section.
type Line struct {
	// Text is ONE SCREEN ROW's worth of styled text -- that is the contract
	// every consumer is written against, and it is the contract this package
	// keeps on three of renderBlockPainted's six arms -- fenced code, the rule
	// and the table row -- and does not keep on the other three.
	//
	// A Line CAN STILL CARRY AN EMBEDDED '\n', and what follows from it is
	// worse than a wrong mapping. app/painted.go writes exactly one "\n" per
	// Line into the frame it hands bubbletea, so a Line holding two screen
	// rows makes the rendered frame TALLER THAN THE VIEWPORT it was cut to;
	// and cursor movement, gutter marks and anchor creation all resolve
	// through BlockIdx, so two rows answering to one Line is the mapping this
	// type exists for going soft.
	//
	// The code arm no longer does it, and it was 86% of the population:
	// fourSpaceTabs and wrapCodeRow (ui/painted.go) between them took 990
	// fenced-code cases to 0 over a real corpus at width 80. WHAT IS LEFT IS
	// TWO WRAPPING DEFECTS AND NO CONTROL BYTE -- 184 headings that neither
	// wrap nor truncate, and 10 Lines where wrapPlain was handed a run with no
	// break opportunity in it. Zero comes from a block whose Text holds a C0 or
	// DEL byte.
	//
	// TestACodeLineIsOneScreenRow is the gate for the half that is closed,
	// TestOneUILineCanCarryTwoScreenRows pins the heading half as
	// characterization, and TestWideRunOverflowsWrapPlainButNotTheGrid pins the
	// wrapPlain half. The assertion this package still owes, and still cannot
	// write green, is one line: no Line.Text contains '\n'.
	Text     string
	BlockIdx int
	IsThread bool
	// ThreadIdx is which of the block's threads this row belongs to, or
	// NoThread on a row that is the block's own text. It exists so a caller
	// can find the rows of one thread: BlockIdx and IsThread together locate
	// a block's whole card region and cannot tell its threads apart, which
	// is what scrolling to a focused thread needs.
	ThreadIdx int
}

// gutterWidth is cursor glyph (2) + thread mark (2). The mark is a glyph and
// ONE TRAILING SPACE, and unmarked rows pad to the same 2 so body text starts
// in one column whether a block carries threads or not.
const gutterWidth = 4

// Cursor is where the reader is: Block is the block their keys act on, and
// Thread which of that block's threads is focused, or NoThread when the focus
// is the block's own line rather than any of its threads.
//
// ONE position, never two. The rail draws Block's own rows when Thread is
// NoThread and the focused thread's rows otherwise -- so a focus that is both
// a line and a thread, which drew the rail in two places at once, is not a
// state this type can hold.
//
// A Block no block index equals (see NoCursor) means no cursor at all.
type Cursor struct {
	Block  int
	Thread int
}

// NoCursor is the Block value of a document with nothing to aim at.
const NoCursor = -1

// NoThread is the Thread value of a focus resting on a block's own line, and
// the ThreadIdx of a Line that is not part of a card.
const NoThread = -1

// OnLine and OnThread are the two focuses, named so that a call site says
// which it means.
//
// CONSTRUCT WITH THESE, not with a literal: Thread's zero value is 0, a valid
// thread index, so a bare Cursor{Block: n} reads as "the block's line" and
// means "its first thread" -- which draws the rail somewhere the reader did
// not ask for, or nowhere at all when the block has no threads.
func OnLine(block int) Cursor { return Cursor{Block: block, Thread: NoThread} }

func OnThread(block, thread int) Cursor { return Cursor{Block: block, Thread: thread} }

// UnanchoredIdx tags lines belonging to the unanchored section, rendered
// after every block.
const UnanchoredIdx = -1

// ThreadView is a thread prepared for display: badge computed from its
// placement, Placed=false for orphans (rendered in the unanchored section).
type ThreadView struct {
	Thread domain.Thread
	Badge  string
	Placed bool
}

// hyphenSentinel stands in for a '-' wrapPlain has decided to hide from
// ansi.Wordwrap -- see protectHyphens. U+E000, the first Private Use Area
// code point: nothing this package parses from a plan document or composes
// for one has a legitimate use for it, so wrapPlain treats its PRESENCE in
// the input as a fact to check for rather than a byte it may assume is
// never there (see the guard at the top of wrapPlain).
const hyphenSentinel = '\uE000'

// wrapPlain wraps s to width, carrying a hyphenated word down onto the next
// row whole rather than splitting it at the hyphen.
//
// THE DEFECT THIS WORKS AROUND IS ansi.Wordwrap'S OWN, at
// github.com/charmbracelet/x/ansi@v0.11.7/wrap.go: its `case r == '-':`
// arm writes the hyphen and charges it a cell with NO width check --
// `addSpace(); addWord(); buf.WriteByte(b[i]); curWidth++` -- where every
// other arm tests curWidth+wordLen against limit first. A hyphen landing
// exactly at the limit is written anyway, and the row it lands on is one
// cell wider than width. That is filed upstream and is not fixed here: this
// function works around it without patching, vendoring or forking ansi.
//
// protectHyphens keeps ansi.Wordwrap from ever taking that arm for a token
// that fits width, so its OWN width-checked word-buffer logic carries the
// token down instead. A token wider than width cannot be spared a break
// somewhere, so wrapPlain leaves ITS hyphens real on the first pass.
//
// WHEN THAT BREAK OVERFLOWS, wrapPlain protects the one hyphen responsible
// and calls ansi.Wordwrap AGAIN OVER THE WHOLE STRING, rather than patching
// the already-wrapped rows by hand: the row receiving such a splice can be
// one ansi.Wordwrap had already packed to exactly width on its own, and the
// character that then overflowed it was never a hyphen, so a hand-rolled
// patch had nothing left to trim and returned the row over budget in
// silence. Every re-flow decision, on every pass, is still made by
// ansi.Wordwrap itself -- this function only ever narrows which hyphens it
// is allowed to break at, never where a row ends.
//
// BOUNDED: each retry protects one more real '-' than the last and never
// fewer (protectHyphenAt only ever turns a real hyphen INTO the sentinel,
// never back), and a string has finitely many '-' runes in it.
//
// THE SUFFIX CHECK BELOW IS TAKEN AFTER ansi.Strip, DELIBERATELY. Every call
// site can hand this function text already carrying real SGR styling, and a
// styled run boundary can sit with zero visible characters between the hyphen
// and the following escape. Stripping first makes the check the reader's
// hyphen, always, and never a byte of styling that happens to trail it.
func wrapPlain(s string, width int) []string {
	if width < 10 {
		width = 10
	}
	// THE SENTINEL MUST BE ASSERTED ABSENT, NOT ASSUMED. If a caller's text
	// somehow already carries U+E000, protecting hyphens would mean
	// swapping some of THEIR characters for placeholders that get turned
	// back into plain hyphens on the way out -- silently corrupting content
	// that was never a hyphen. wrapPlain checks for it and falls back to
	// ansi.Wordwrap's own behaviour, hyphen bug included, rather than risk
	// that.
	if strings.ContainsRune(s, hyphenSentinel) {
		return strings.Split(ansi.Wordwrap(s, width, ""), "\n")
	}

	protected := protectHyphens(s, width)
	for {
		lines := strings.Split(ansi.Wordwrap(protected, width, ""), "\n")

		bad := firstOverWidthRow(lines, width)
		if bad < 0 {
			return unprotectLines(lines)
		}

		prefix := ansi.Strip(strings.Join(lines[:bad+1], "\n"))
		if !strings.HasSuffix(prefix, "-") {
			// Over budget with no hyphen at the end is not this function's
			// bug to fix: it is the documented non-goal
			// TestWideRunOverflowsWrapPlainButNotTheGrid pins -- a single
			// token with no break opportunity anywhere in it, wider than
			// width on its own. No hyphen anywhere can repair that.
			return unprotectLines(lines)
		}

		// ordinal is this hyphen's 1-based position among every REMAINING
		// (still-real, unprotected) '-' in `protected`, counting left to
		// right. Wrapping only ever inserts '\n' and relocates whitespace
		// -- it never reorders one non-whitespace rune relative to
		// another -- so this count is identical taken here or from
		// `protected` itself, which is what makes protectHyphenAt's raw
		// scan of `protected` agree with it.
		ordinal := strings.Count(prefix, "-")
		next := protectHyphenAt(protected, ordinal)
		if next == protected {
			// Cannot happen given the HasSuffix check above: prefix
			// ending in '-' means ordinal names a rune that is still
			// literally '-' in protected. Kept as a hard stop rather than
			// an assumption, so a future change to either function fails
			// loudly instead of spinning forever.
			return unprotectLines(lines)
		}

		// THE GUARD protectHyphens ITSELF APPLIES TO ITS OWN, FIRST PASS (a
		// token wider than width is never protected there) BUT protectHyphenAt
		// HAS NONE: it extends protection unconditionally, and doing so is only
		// safe while the run it makes atomic still fits somewhere. When it does
		// not, ansi.Wordwrap has nowhere to put that run but whole, on one row,
		// however wide it is -- turning the upstream +1 this retry exists to
		// repair into something much larger. So the retry's OWN result is
		// checked, not assumed: re-wrap with the new protection in hand and
		// compare the first over-width row EITHER SIDE produces, accepting only
		// if it did not grow. Checked by re-running ansi.Wordwrap rather than by
		// measuring the token in isolation, because a token's width alone cannot
		// say whether a NEIGHBOURING still-real hyphen leaves it a break point
		// once this retry's substitution is the only one applied: on a real
		// corpus block, an unguarded retry turned a 37-cell row (budget 36) into
		// a 46-cell one.
		nextLines := strings.Split(ansi.Wordwrap(next, width, ""), "\n")
		if nextBad := firstOverWidthRow(nextLines, width); nextBad >= 0 && ansi.StringWidth(nextLines[nextBad]) > ansi.StringWidth(lines[bad]) {
			return unprotectLines(lines)
		}
		protected = next
	}
}

// firstOverWidthRow returns the index of the first row in lines wider than
// width, or -1 if every row fits.
func firstOverWidthRow(lines []string, width int) int {
	for i, l := range lines {
		if ansi.StringWidth(l) > width {
			return i
		}
	}
	return -1
}

// unprotectLines restores every hyphenSentinel wrapPlain's loop left
// behind to a plain '-', the moment no line remains over width for it to
// protect.
func unprotectLines(lines []string) []string {
	for i, l := range lines {
		lines[i] = strings.ReplaceAll(l, string(hyphenSentinel), "-")
	}
	return lines
}

// protectHyphens swaps every '-' inside a whitespace-delimited token for
// hyphenSentinel, but only in a token whose OWN rendered width already fits
// within limit -- see wrapPlain for why. Everything else, including every
// run of whitespace, is copied through untouched: this is a substitution
// pass over ansi.Wordwrap's own two kinds of run (space, non-space), not a
// second wrapper -- it never measures a LINE, only ever one token at a time,
// and it decides nothing about where a row ends.
//
// ansi.Wordwrap does NOT end a token at every unicode.IsSpace rune. Its own
// vendored wordwrap() tests `r != nbsp` before treating a space as a break, so
// a non-breaking space (U+00A0) merges into whatever word Wordwrap is already
// building. Splitting on bare unicode.IsSpace disagreed with that: a phrase
// joined by one looked like two SEPARATE, individually narrower pieces here
// where Wordwrap sees one wider one, so every hyphen in BOTH got protected --
// removing every break opportunity from a run Wordwrap goes on to treat as a
// single unbreakable word. The predicate below is Wordwrap's own; see
// protectPanelHyphens (app/list.go) for the identical predicate.
func protectHyphens(s string, limit int) string {
	var b strings.Builder
	b.Grow(len(s))
	flushFrom := 0
	sawFirst, prevBreak := false, false
	flush := func(end int) {
		tok := s[flushFrom:end]
		if strings.ContainsRune(tok, '-') && ansi.StringWidth(tok) <= limit {
			tok = strings.ReplaceAll(tok, "-", string(hyphenSentinel))
		}
		b.WriteString(tok)
	}
	for i, r := range s {
		br := isWordwrapBreak(r)
		switch {
		case !sawFirst:
			sawFirst = true
		case br != prevBreak:
			flush(i)
			flushFrom = i
		}
		prevBreak = br
	}
	flush(len(s))
	return b.String()
}

// isWordwrapBreak is ansi.Wordwrap's own break predicate: every
// unicode.IsSpace rune ends a token EXCEPT a non-breaking space (U+00A0),
// which merges into the word being built like an ordinary letter.
// protectHyphens tokenizes by this rather than by bare unicode.IsSpace so that
// what THIS package calls a token is never narrower than what Wordwrap will
// actually treat as one -- see protectHyphens for what went wrong when it was
// not.
func isWordwrapBreak(r rune) bool { return unicode.IsSpace(r) && r != ' ' }

// protectHyphenAt returns a copy of protected with its ordinal-th
// REMAINING literal '-' rune (1-based, left to right, ignoring every rune
// already turned into hyphenSentinel) swapped for hyphenSentinel too. It
// is wrapPlain's retry step: protectHyphens already handles every hyphen
// whose own token fits width; this is what extends that protection, one
// more hyphen at a time, into a token that does not, each time
// ansi.Wordwrap's own break at the next hyphen still overflows.
func protectHyphenAt(protected string, ordinal int) string {
	if ordinal < 1 {
		return protected
	}
	var b strings.Builder
	b.Grow(len(protected))
	seen := 0
	for _, r := range protected {
		if r == '-' {
			seen++
			if seen == ordinal {
				b.WriteRune(hyphenSentinel)
				continue
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// FormatAttribution renders a review fact's Attribution as a reader sees it —
// the single formatter shared by the TUI's card renderer below and by mcptools'
// MCP surface, so a coordinator reading a thread over MCP sees the identical
// voice a human sees in the TUI.
//
// The author half is the fact's optional recorded author identity (see
// domain.Attribution): ActorLogin, then ActorID, with NO step onto
// ActorDisplay.
//
// The agent identity comes first when a.Agent is set, because in a review who
// is speaking is the question and on whose behalf is the qualifier. '●' here is
// a mid-line separator only — see commentMarker for why it never also leads the
// line.
//
// pseudonym substitutes for the author only when the write carries NEITHER a
// recorded author nor an agent: that combination means exactly one thing,
// written by the human on this machine with no agent involved, and the stored
// fact never carries what to call them. It is caller-resolved and passed in
// rather than read here, keeping this function pure.
//
// Nothing in this build records an author identity. The arms that render one
// stay so that a fact which does carry one is never drawn as the human at THIS
// keyboard, which would be the worse failure. Do not "fix" the ActorID arm into
// a step onto ActorDisplay: that is ruled out.
func FormatAttribution(a domain.Attribution, pseudonym string) string {
	author := a.ActorLogin
	if author == "" {
		author = a.ActorID
	}
	switch {
	case a.Agent != "" && author != "":
		return "agent-" + a.Agent + " ● " + author
	case a.Agent != "":
		return "agent-" + a.Agent
	case author != "":
		return author
	default:
		return "user-" + pseudonym
	}
}

// commentMarker leads every comment's own header line. It is not '●' so that
// '●' is never a leading glyph on the same line as a composite attribution's
// own mid-line separator (see FormatAttribution) — the two would otherwise sit
// side by side ("● agent-blue-parakeet-f9 ● dana...") whenever an agent acting
// for a recorded author opens a thread.
const commentMarker = "|"

// gutterMarkGlyph is the BLOCK GUTTER's annotation mark: '※' where the block
// carries an open thread, '○' where every thread on it is resolved.
//
// '※' is a reference mark — literally the typographic glyph for "there is an
// annotation about this", which is what a gutter mark is — and unlike '●' it is
// not the glyph FormatAttribution uses as a mid-line separator, the collision
// that keeps commentMarker at '|'.
func gutterMarkGlyph(resolved bool) string {
	if resolved {
		return "○"
	}
	return "※"
}

// resolvedLabel is the row that marks a resolved thread. A thread's state is a
// word rather than a glyph because the glyph had nothing to sit beside: an
// unresolved thread showed a lone dot on an otherwise empty row, which read as
// a rule that had failed to draw. Only resolved threads carry a row at all.
const resolvedLabel = "(resolved)"

// ThreadCardLines renders v the same way the document view renders an
// expanded thread card (a "(resolved)" row when the thread is resolved, then
// one header line per comment with its body indented beneath) — a thin
// dispatch onto the private renderThreadCardPainted below, so a caller
// outside this package (the relocate panel's comment gutter, currently the
// only one) shares the identical card layout instead of forking it. A NIL st
// IS THE DEFAULT THEME'S STYLES, NOT AN UNPAINTED CARD. pseudonym is
// client/config's mint-on-first-read machine pseudonym; see FormatAttribution
// for when it is substituted.
//
// EVERY LINE IT RETURNS IS <= width. wrapPlain can still leave an unbroken run
// over that width (a long token in a body, or — deliberately uncapped, see
// FormatAttribution — a long attribution on a header line), and a caller that
// feeds these lines through its own Width().Render would have such a row
// silently WORD-WRAPPED, inserting real newlines, instead of truncated.
// ansi.Truncate is escape-aware, so it clips an already-styled line to width
// display columns without corrupting the SGR the renderers applied.
func ThreadCardLines(v ThreadView, width int, pseudonym string, st *Styles) []string {
	// renderThreadCardPainted prepends its indent outside the width it's
	// handed (see cardIndentCols): budget for it here so a normal-width card
	// isn't clipped by ansi.Truncate below on every line.
	raw := renderThreadCardPainted(v, width-cardIndentCols, pseudonym, stylesOrDefault(st))
	out := make([]string, len(raw))
	for i, l := range raw {
		out[i] = ansi.Truncate(l, width, "…")
	}
	return out
}

func allResolved(views []ThreadView) bool {
	for _, v := range views {
		if !v.Thread.Resolved {
			return false
		}
	}
	return true
}

// RenderDoc renders every block plus expanded thread cards into a flat,
// source-mapped line list with a gutter: a cursor bar on the current block's
// rows and a thread marker on annotated blocks. Threads that could not be
// placed on any block are appended as an unanchored section after the
// document.
//
// A NIL st IS THE DEFAULT THEME'S STYLES, NOT AN UNPAINTED RENDERING -- the
// full-canvas painted path in painted.go runs either way, and the only thing a
// nil chooses is which palette it paints with. See stylesOrDefault. pseudonym
// is client/config's mint-on-first-read machine pseudonym; see
// FormatAttribution for when it is actually substituted.
func RenderDoc(blocks []Block, views map[int][]ThreadView, unanchored []ThreadView, expanded map[int]bool, cur Cursor, width int, pseudonym string, st *Styles) []Line {
	return renderDocPainted(blocks, views, unanchored, expanded, cur, width, pseudonym, stylesOrDefault(st))
}

// displayIn picks the projection for the zone the row is being painted in: the
// Card one for a block that carries comments, Doc otherwise. Both fall back to
// the normalized source, which is what a block parsed without styles has.
//
// AN EMPTY PROJECTION IS NOT THE SAME FACT AS A MISSING ONE, and telling them
// apart is what the second test below is for. "" reaches this function two
// ways: a block nobody computed a projection for, where the source is the best
// anybody can draw; and a KindTableRow whose cells are ALL EMPTY, where the
// projection was computed, is authoritative, and says there is nothing to draw.
// Falling back on the second put the row's own markup on screen -- `| | |` for
// `|  |  |` -- which is the one thing requireNoMarkupOnScreen
// (app/fidelity_test.go) exists to keep off it. Block.Cells is what separates
// them, and DisplayPlain is what makes the test exact rather than a guess about
// styles: the plain projection is computed for every caller, so a row with
// cells and no plain text is empty BY MEASUREMENT. searchText (ui/search.go)
// draws the same line for the same reason -- one keeps the markup off the
// screen and the other out of the search index.
//
// AND IT IS WHERE THE CONTROL-BYTE SUBSTITUTION IS APPLIED TO THE FALLBACK,
// WHICH IS WHY IT TAKES A STYLE.
// This is the one function in the package that knows which of two very
// different strings it is about to hand an arm:
//
//   - a PROJECTION, which is renderInlines output. Its control bytes were
//     already made visible at the leaf, and it is FULL of Draftplane's own SGR
//     -- so scanning it for a 0x1b would eat the renderer's own colour and
//     protect nothing: a C0 predicate cannot tell the ESC opening our colour
//     from one in a payload, and filtering here painted the SGR itself on
//     screen for every prose block in the document.
//
//   - the FALLBACK, which is a raw source span carrying no styling at all.
//     Every control byte in it is the document's. That is the one string on
//     this path a filter belongs on, and normalizeVisible is what stops
//     reanchor.Normalize folding the CR away before it can be drawn.
//
// style is the zone the caller is about to paint the row in, so the glyph lands
// on the same background as the words around it.
func (b Block) displayIn(onCard bool, style lipgloss.Style) string {
	d := b.Display
	if onCard && b.DisplayCard != "" {
		d = b.DisplayCard
	}
	if d != "" {
		return d
	}
	if len(b.Cells) > 0 && b.DisplayPlain == "" {
		return ""
	}
	return visibleControls(normalizeVisible(b.Text), style)
}

// FirstLineOf returns the index of the first non-thread-card line belonging
// to blockIdx, for scroll-to-cursor bookkeeping.
func FirstLineOf(lines []Line, blockIdx int) int {
	return FirstLineOfFocus(lines, Cursor{Block: blockIdx, Thread: NoThread})
}

// FirstLineOfFocus is the row a focus should scroll to: the block's first row
// when the focus is the line itself, and the focused thread's first row when it
// is a thread. A thread whose rows are not on screen -- its block collapsed --
// falls back to the block's own row, which is where the focus visibly is in
// that case.
//
// ITS FIRST ROW AND NOT ITS FIRST TEXT ROW. A table is drawn as one bordered
// grid cut back into per-row Lines (paintTables), so the FIRST row of every
// table begins with the box's top border, and the row under a comment card
// begins with one too (gridRowGroup). Both scroll to their box's opening rather
// than to their words, which is the right row to put at the top of a viewport.
//
// SO A CALLER MUST NOT ASSUME THIS LINE CARRIES THE CURSOR. renderDocPainted
// draws neither the rail band nor the '┃' on a border line -- deliberately, see
// onRow there -- so "the line this returns has the focus glyph on it" holds for
// every kind except those two rows.
func FirstLineOfFocus(lines []Line, cur Cursor) int {
	if cur.Thread != NoThread {
		for i, l := range lines {
			if l.BlockIdx == cur.Block && l.IsThread && l.ThreadIdx == cur.Thread {
				return i
			}
		}
	}
	for i, l := range lines {
		if l.BlockIdx == cur.Block && !l.IsThread {
			return i
		}
	}
	return 0
}

package ui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/reanchor"
)

// This file is what a control byte becomes on its way to a terminal, and where
// that is decided.
//
// THE PREDICATE IS C0 CONTROLS AND DEL, AND NOT ESCAPE SEQUENCES, which is the
// one thing here that is easy to get wrong and expensive to get wrong. The
// finding it exists for needs no \x1b at all:
//
//	Requires approval\b\b\b...\bNo approval needed   -> "No approval needed"
//	We will NOT rotate the keys.\rWe will rotate...  -> "We will rotate them"
//
// Both survive into the painted row on every arm, every emulator honours CR and
// BS with no permission asked for and none granted, and what they attack is the
// one property this product exists to provide: an approval is a statement that
// a named party read EXACTLY THESE BYTES and agreed.
//
// AND THE ANSWER IS VISUALISE RATHER THAN STRIP. A reviewer approves bytes: a
// strip would show them clean text while the hash covers something else, which
// is the same forgery with the renderer's help. A refusal is too blunt -- the
// document is usually the reader's own file and a refusal leaves no remedy.
//
// THE GLYPH IS STYLED AND NOT MERELY SUBSTITUTED, which is what keeps a
// document WRITING ABOUT a control byte distinguishable from one CONTAINING
// one: `␍` is a perfectly ordinary rune a plan may quote. See visibleControls
// for the styling and TestAControlByteIsNotItsOwnGlyph for the assertion.
//
// WHERE IT SITS IS TWO PLACES BECAUSE THE ARMS ARE TWO KINDS. Four of
// renderBlockPainted's arms draw a PROJECTION, which is renderInlines output,
// so one filter at that leaf covers paragraph, list item, table body cell and
// table header cell at once and runs BEFORE any styling, where Draftplane's own
// SGR cannot be confused with a payload's. Heading and fenced code draw
// Block.Text raw and never call renderInlines, so they carry the same filter at
// the arm: the transform happens where the bytes are DRAWN and never in Text.
//
// BLOCK.TEXT IS NOT FILTERED AND MUST NOT BE. reanchor.CreateAnchor normalises
// a span and then searches the normalised RAW document for it, so a comment on
// a filtered block would be "span not found in document" -- and because a
// heading is its section's key, one control byte in one heading refuses every
// wholly benign block beneath it. Filtering Text would make the attack visible
// and the document simultaneously uncommentable.
//
// The same arms spend a second, unrelated rule: U+FE0F, unconditionally. See
// ui/vs16.go.

// controlPictureBase is U+2400 SYMBOL FOR NULL. The Control Pictures block
// spells the 32 C0 bytes in order, so 0x08 is U+2408 ␈, 0x0d is U+240D ␍ and
// 0x1b is U+241B ␛: one addition, no table to keep in step with anything.
const controlPictureBase = 0x2400

// delPicture is U+2421 ␡, and it is NOT controlPictureBase+0x7f. DEL sits at
// the far end of ASCII while its glyph sits just past the C0 run, next to
// U+2420 ␠ for SPACE -- the one place the arithmetic above does not reach.
const delPicture = 0x2421

// isVisibleControl is the predicate, and it is every C0 byte and DEL LESS the
// two this renderer already answers.
//
// TAB IS NOT THIS ONE'S. It is spent as four spaces at the two raw arms and at
// both app-side entry points (fourSpaceTabs) and already projects to four on
// the other four arms. Four spaces fold the way a tab does through
// reanchor.Normalize and move 0 of 13,449 anchorable spans in a real corpus,
// where a Control Picture is not whitespace, survives the fold and moves 175 --
// so a tab reaching this predicate would undo that ruling.
//
// NEWLINE IS THE ROW SEPARATOR of every buffer this text ends up in: a table
// row's projection is cells joined with one, renderBlockPainted splits a
// fence's Text on them, and app/painted.go writes one per ui.Line. Drawing it
// as ␤ would be a layout change wearing a security fix's clothes.
//
// ⚠️ THE RESIDUAL: A C1 CONTROL REACHES THE TERMINAL UNVISUALISED, AND THAT IS
// A RULING RATHER THAN AN OVERSIGHT. This predicate stops at 0x7F and stays a
// BYTE SCAN, so nothing here answers for U+0080-U+009F. Closing it means the
// predicate stops being a byte scan: a C1 has two spellings, and the UTF-8 one
// -- which survives a JSON round trip, so an agent's save carries it intact --
// can only be caught by DECODING, where 0x80-0x9F is also the continuation-byte
// range indexControl's own argument rests on. There are no Control Pictures for
// C1 either. What is covered: stripStyling keeps a C1 from TRUNCATING
// DisplayPlain at itself, and ui.FrameViolations reports one in both spellings,
// so a frame carrying one fails an assertion rather than passing quietly.
//
// ⚠️ THE COLUMN HAZARD IS THE HALF OTHER FILES POINT BACK AT.
// `ansi.StringWidth("abcd\x9fefgh")` is 4, because the byte is an APC
// introducer to that parser -- so a row carrying one measures short, the
// wrapper is offered no break opportunity, and THE ROW IS NOT WRAPPED AT ALL.
// At width 60 a paragraph that draws 4 ui.Line of exactly 60 cells draws 2, the
// widest 170 cells, with nothing dropped. In a grid that is the box broken
// open, which is why closeGridRight and borderOnlyLine (ui/painted.go) carry a
// pointer here. The ROW hazard is absent: both width authorities agree on a C1.
//
// ⚠️ AND C1 IS NOT THE LAST CLASS THIS PREDICATE CANNOT SEE. Unicode BIDI
// OVERRIDES (U+202A-U+202E, U+2066-U+2069) and the invisible-format set
// (U+200B, U+200C/U+200D, U+2060, U+00AD, U+FEFF, U+180E) are not control
// BYTES, so nothing here has an opinion about them -- and they are the SHARPER
// problem, being valid UTF-8 that every channel carries intact, and needing no
// claim about an emulator, since reordering is what a bidi-capable renderer is
// SPECIFIED to do. Nothing is built for it here. The cheap half of an answer is
// to teach ui.FrameViolations to REPORT these runes -- but NOT the whole Cf
// category: U+200D is load-bearing in ordinary text, holding an emoji sequence
// together, and this repository's own width fixture draws one carrying three.
func isVisibleControl(b byte) bool {
	return b == 0x7f || (b < 0x20 && b != '\t' && b != '\n')
}

// controlPicture is the glyph for one byte the predicate accepts.
func controlPicture(b byte) rune {
	if b == 0x7f {
		return delPicture
	}
	return controlPictureBase + rune(b)
}

// indexControl is the first byte in s the predicate accepts, or -1. A byte
// scan and not a rune scan on purpose: every byte it looks for is ASCII, and
// no byte of a multi-byte UTF-8 sequence can be mistaken for one (a
// continuation byte is >= 0x80), so the scan cannot cut a rune in half.
func indexControl(s string) int {
	for i := 0; i < len(s); i++ {
		if isVisibleControl(s[i]) {
			return i
		}
	}
	return -1
}

// visibleControls applies the control-byte substitution to one string, for a
// caller that renders the string itself: every control byte becomes its
// Control Picture in REVERSE VIDEO, and the caller's own Render still covers
// everything around it. It strips U+FE0F first, unconditionally, before the
// control-byte scan runs -- see stripSelector16 (ui/vs16.go). Every leaf
// renderInlines draws, plus the heading and fenced-code raw arms, reaches the
// screen through this function or renderControls below, so the strip lands at
// all of them from here.
//
// IT ANSWERS s UNCHANGED WHEN THERE IS NOTHING TO DO, which is what makes every
// benign row byte-identical.
//
// REVERSE VIDEO IS DERIVED FROM THE STYLE THE CALLER WAS GOING TO USE rather
// than named as a colour of its own. It is legible in both presets and in none
// -- a caller with a zero lipgloss.Style (plainProjection) still emits
// `\x1b[7m`, which ansi.Strip takes back off, so the search index is a plain
// string -- and style.Reverse(true) keeps the ZONE: every style in this package
// declares its own background, so a glyph in a code block is still on the code
// background and one on a commented row is still on the card's.
//
// THE RUN AFTER A GLYPH RE-DECLARES THE STYLE, and the first run deliberately
// does not. lipgloss ends every Render with a full SGR reset, so a fragment
// following a nested Render would otherwise paint on the terminal's own
// background. The first run needs no such thing, because the caller's own
// Render still opens at position 0 -- which is exactly what makes the
// no-control answer above byte-identical.
//
// ⚠️ THE CALLER's OWN Render MUST NOT CARRY UNDERLINE OR STRIKETHROUGH, and
// that is a real constraint rather than a style note. A plain, bold or italic
// style wraps the whole string in one SGR run and passes an embedded sequence
// through untouched, but lipgloss renders Underline and Strikethrough PER
// GRAPHEME -- `Underline(true).Render("ab")` is
// `\x1b[4;4ma\x1b[m\x1b[4;4mb\x1b[m` -- and a grapheme walk treats an ESC as
// ordinary content, so the sequence comes out on screen as characters. The
// arms that DO carry underline are in renderInlines, and they use
// renderControls below, which never nests.
func visibleControls(s string, style lipgloss.Style) string {
	return visibleControlsKeepingSelector16(stripSelector16(s), style)
}

// visibleControlsKeepingSelector16 is visibleControls' own substitution,
// factored out so a caller that must NOT strip U+FE0F -- see
// VisibleControlsKeepingSelector16 below for the one place that is true --
// can still share every other decision this function makes (which bytes,
// which glyph, the reverse-video styling, the no-control fast path) rather
// than risk a second, drifted copy of them.
func visibleControlsKeepingSelector16(s string, style lipgloss.Style) string {
	first := indexControl(s)
	if first < 0 {
		return s
	}
	ctrl := style.Reverse(true)
	var b strings.Builder
	b.Grow(len(s) + 16)
	b.WriteString(s[:first])
	for i := first; i < len(s); {
		b.WriteString(ctrl.Render(string(controlPicture(s[i]))))
		i++
		next := len(s)
		if k := indexControl(s[i:]); k >= 0 {
			next = i + k
		}
		if next > i {
			b.WriteString(style.Render(s[i:next]))
		}
		i = next
	}
	return b.String()
}

// renderControls is style.Render with the control-byte substitution in front
// of it: the drop-in every caller that was already rendering the whole string
// uses, and byte-for-byte the old call when the string holds no control byte.
// It strips U+FE0F first on every path, the no-control fast path included.
//
// IT RENDERS THE RUNS ITSELF RATHER THAN WRAPPING visibleControls's OUTPUT, and
// the difference is not cosmetic -- `style.Render(visibleControls(s, style))`
// was the first spelling of this function and it CORRUPTED THE AUTOLINK ARM.
// renderInlines hands a link and an autolink style.Underline(true), lipgloss
// renders an underlined string one grapheme at a time, and a grapheme walk has
// no idea that the ESC it is looking at opened a sequence:
// `<https://example.com/a\x7fb>` came out on screen with this renderer's own
// SGR printed as text. Rendering each run here means nothing is ever handed to
// a Render that already contains an escape.
func renderControls(s string, style lipgloss.Style) string {
	s = stripSelector16(s)
	first := indexControl(s)
	if first < 0 {
		return style.Render(s)
	}
	if first == 0 {
		return visibleControls(s, style)
	}
	return style.Render(s[:first]) + visibleControls(s[first:], style)
}

// VisibleControls is the control-byte substitution for the frames that do not
// live in this package: the plan list and the review model's panels (app/),
// which draw stored strings -- titles, paths, status text -- that never pass
// through ParseBlocks and so have none of the document channel's cover at all.
//
// IT IS THE SAME PREDICATE AND THE SAME TRANSFORM, WHICH IS THE POINT OF IT
// BEING A WRAPPER RATHER THAN A COPY. This project's recurring defect is one
// rule written down in two places that then drift apart; a second C0 predicate
// in app/ would be that defect with a security label on it.
//
// visibleControls AND NOT renderControls, because every app-side caller already
// renders the composed row through a style of its own AFTER budgeting it. See
// visibleControls for why the first run is deliberately left unrendered, and
// for the ⚠️ on Underline and Strikethrough -- no style in NewStyles carries
// either.
//
// AND IT RUNS BEFORE THE BUDGET, EVERY TIME. A bare C0 byte is ZERO cells to
// ansi.StringWidth (what every truncation and pad in app/ measures with) and
// ONE to lipgloss's wrap (what Width().Render measures with), so a row budgeted
// to exactly its width comes back as two rows and the frame is one row taller
// than the terminal. A Control Picture is one cell to both authorities, so
// substituting first settles the disagreement instead of working around it.
//
// ⚠️ AND THE TAB IS SPENT HERE TOO: the same 0-cells-against-4 arithmetic with
// four times the overshoot. A tab in the status bar or in an H1 each drew one
// row past the terminal. A TAB IN AN H1 IS NOT EVEN HOSTILE -- ui.InferTitle
// answers Block.Text raw, so an ordinary plan whose title contains one pushed
// the review frame a row past the terminal.
//
// FOUR SPACES AND NOT ␉, which is a deliberate ruling and carries no conflict
// here: that ruling was scoped to the two raw arms because those draw an
// anchor's own text, and NONE of the surfaces this function serves carries an
// anchor.
func VisibleControls(s string, style lipgloss.Style) string {
	return visibleControls(fourSpaceTabs(s), style)
}

// VisibleControlsKeepingSelector16 is VisibleControls with ONE exemption:
// U+FE0F is left exactly where it was, rather than stripped. IT EXISTS FOR
// EXACTLY ONE CALLER, ListModel.enterRename, and nowhere else in app/ should
// reach for it without re-deriving why.
//
// Every other caller of VisibleControls draws a row a reader looks at and
// nothing else, so the strip is pure display hygiene there and no other
// caller's string is written back anywhere. enterRename's prefill is the
// exception: ListModel.updateRename's ctrl+d arm commits the textarea's value
// straight through svc.RenamePlan, so a plan titled "⚠️ Launch", opened and
// committed unedited, would silently become "⚠ Launch" on disk.
//
// THE CONTROL-BYTE SUBSTITUTION IS NOT EXEMPTED, ONLY THE SELECTOR STRIP. A raw
// '\r' in a title must still prefill and commit as its Control Picture, both
// because bubbles' own textarea folds a raw CR into a newline on the way back
// out of Value() and because committing a raw control byte is exactly what
// this file's control-byte substitution exists to keep off every value this
// package hands somewhere else.
func VisibleControlsKeepingSelector16(s string, style lipgloss.Style) string {
	return visibleControlsKeepingSelector16(fourSpaceTabs(s), style)
}

// RenderControls is renderControls for the frames outside this package: the
// exact drop-in wherever app/ already wrote style.Render(s) around a string
// somebody else supplied, and byte-for-byte that call when the string holds no
// control byte.
//
// USE THIS AND NOT VisibleControls WHEREVER A Render ALREADY STOOD, because
// the difference is whether the FIRST run is styled. VisibleControls leaves it
// bare on purpose -- its callers wrap the finished, budgeted row in one Render
// of their own, which opens the style at column 0. A fragment being appended to
// a row that already carries somebody else's Render has no such opener in front
// of it and would paint on the terminal's own background.
//
// The ⚠️ on Underline and Strikethrough at visibleControls applies here too,
// and no style in NewStyles carries either.
//
// AND IT NEEDS NO fourSpaceTabs, WHICH IS THE SAME ASYMMETRY READ FROM THE
// OTHER SIDE. VisibleControls spends the tab because it leaves runs UNRENDERED,
// so a tab reaches the caller's ansi.Truncate raw. This function renders every
// run it emits, and lipgloss's own Render already substitutes the identical
// four spaces before any budget sees the string.
func RenderControls(s string, style lipgloss.Style) string {
	return renderControls(s, style)
}

// stripStyling is ansi.Strip with its EIGHT-BIT C1 HANDLING TAKEN BACK OFF,
// and this package needs it because ansi.Strip is a TERMINAL PARSER rather
// than a styling remover.
//
// 0x9F is an APC introducer to that parser, exactly as `ESC _` is, and an APC
// swallows every byte after it until a string terminator the PAYLOAD sends. So
// ansi.Strip over a paragraph reading `alpha\x9fbeta gamma` answers `alpha`:
// not a filter, a TRUNCATION, and one that drops everything after the byte
// rather than the byte itself. The same answer for 0x90, 0x98, 0x9D and 0x9E;
// 0x9B eats a short run instead.
//
// WHAT THAT COSTS is that Block.DisplayPlain is both the text the grid draws
// and the text SearchBlocks looks in -- so before this function a reader could
// see `alpha…beta gamma` on screen (the styled Display carries the raw byte
// through untouched) and search for `gamma` and be told it is not there. A
// silent strip on the one index whose rule is "search what the reader sees".
//
// IT DOES NOT NEUTRALISE THE BYTE AND MUST NOT. The predicate stays a byte scan
// over C0 and DEL (see isVisibleControl's residual), so a C1 byte reaches the
// terminal exactly as it did; what this changes is that the bytes AROUND it
// survive the projection.
//
// THE SCAN IS RUNE-AWARE AND THE REST OF THIS FILE'S IS NOT, which is not an
// inconsistency: 0x80-0x9F is also the CONTINUATION-BYTE range, so a byte scan
// would split `␍` (E2 90 8D) and `–` (E2 80 93) down the middle. Only
// a byte that stands OUTSIDE a valid UTF-8 sequence ever reaches ansi.Strip's
// ground state, and only those are split here. A UTF-8-encoded C1 is left alone
// by ansi.Strip and so needs nothing.
func stripStyling(s string) string {
	i := indexRawC1(s)
	if i < 0 {
		return ansi.Strip(s)
	}
	var b strings.Builder
	b.Grow(len(s))
	for i >= 0 {
		b.WriteString(ansi.Strip(s[:i]))
		b.WriteByte(s[i])
		s = s[i+1:]
		i = indexRawC1(s)
	}
	b.WriteString(ansi.Strip(s))
	return b.String()
}

// indexRawC1 is the first byte of s in 0x80-0x9F that stands on its own rather
// than inside a UTF-8 sequence, or -1: exactly the bytes ansi.Strip's parser
// meets in its ground state and reads as an 8-bit C1 control.
func indexRawC1(s string) int {
	for i := 0; i < len(s); {
		if s[i] < utf8.RuneSelf {
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 && s[i] <= 0x9f {
			return i
		}
		i += size
	}
	return -1
}

// plainControls is the control-byte substitution with the styling taken back
// off: the Control Picture and nothing else, for a caller holding a plain
// string rather than a row.
//
// ITS ONE CALLER IS THE SEARCH INDEX (Block.searchText, ui/search.go), which
// reaches no renderer at all. Four of the seven arms already carry the glyph
// into that index for free -- DisplayPlain is a strip of renderInlines output,
// and renderInlines is where the leaf filter sits -- and the other three answer
// Block.Text, which is raw by design and must stay raw. This is the one line
// that lets those three agree with what their own arm draws, and it REUSES
// visibleControls rather than being a second transform for the same reason
// ui/control.go exists at all.
//
// stripStyling AND NOT ansi.Strip, for the reason that function documents: a
// heading whose Text holds BOTH a C0 byte and an 8-bit C1 byte would otherwise
// be indexed as far as the C1 byte and no further.
//
// IT STRIPS U+FE0F FIRST, UNCONDITIONALLY, EVEN WHEN s HOLDS NO CONTROL BYTE AT
// ALL, and it has to be spent here rather than left to visibleControls or
// renderControls, because this function's own no-control fast path returns s
// directly and never reaches either. Without it a heading, fence or fallback
// block's SEARCH INDEX would still carry the selector after the SCREEN stopped
// carrying it. The consequence is deliberate: a reader searching `⚠️` finds
// nothing and searching `⚠` finds everything, which is "search what the reader
// sees".
//
// It answers s unchanged when there is nothing to do, so a benign block pays
// one rune scan and one byte scan and allocates nothing.
func plainControls(s string) string {
	s = stripSelector16(s)
	if indexControl(s) < 0 {
		return s
	}
	var plain lipgloss.Style
	return stripStyling(visibleControls(s, plain))
}

// normalizeVisible is displayIn's fallback normaliser, and it exists because
// reanchor.Normalize is an UNDOCUMENTED PARTIAL C0 FILTER that eats exactly
// the byte this file most wants to show.
//
// Normalize is strings.Join(strings.Fields(text), " ") and strings.Fields
// splits on unicode.IsSpace, which covers CR, VT and FF and does not cover
// NUL, BEL, BS or DEL. So on the fallback arm -- an HTML block, a link
// reference definition, any content kind with no projection of its own -- the
// CR forgery arrived already folded to a space and the BACKSPACE forgery
// arrived whole.
//
// THE FIX IS ON THE DISPLAY SIDE AND reanchor.Normalize IS UNTOUCHED. That
// function is the ANCHORING normaliser: ResolveAnchor and
// reanchor.CreateAnchor both fold a span with it and search the folded raw
// document, so teaching it to keep a CR would move anchor spans in every
// document that holds one. Teaching THIS path to keep one moves nothing:
// displayIn feeds three renderBlockPainted arms and no anchor.
//
// The two agree on everything else BY CONSTRUCTION: a string with no control
// byte is handed to reanchor.Normalize itself, so the fallback cannot drift
// into a second whitespace convention. VT, FF and CR are the only three runes
// unicode.IsSpace accepts that the predicate above also accepts.
func normalizeVisible(text string) string {
	if indexControl(text) < 0 {
		return reanchor.Normalize(text)
	}
	return strings.Join(strings.FieldsFunc(text, func(r rune) bool {
		switch r {
		case '\v', '\f', '\r':
			return false
		}
		return unicode.IsSpace(r)
	}), " ")
}

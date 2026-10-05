package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/draftplane/draftplane/reanchor"
)

// searchDoc is real markdown parsed by the real ParseBlocks rather than a set
// of hand-built Blocks, because most of what SearchBlocks has to get right is
// a property of what the parser puts in Block.Text: a paragraph keeps the
// newline the author wrapped it at, a fence's Text is its content with no
// fences or language tag, and a heading's is the title with no `#`. A
// hand-built Block would let a test assert those without ever proving them.
const searchDoc = "# Rollout Plan\n\n## Deployment Steps\n\nThe deploy step\nneeds a rollback plan.\n\n```go\nfunc rollback() {\n\treturn nil\n}\n```\n\nThe café serves crème brûlée after the deploy.\n\n- A list item about MIGRATION  timing\n"

// The blocks searchDoc parses to, in order. Named because the cross-check
// against ResolveAnchor below has to name one twice.
const (
	searchTitleIdx = iota
	searchHeadingIdx
	searchParaIdx
	searchCodeIdx
	searchAccentIdx
	searchListIdx
)

func TestSearchBlocks(t *testing.T) {
	// Real styles, because that is what a review view parses with and the
	// block a search matches is now a projection built during the parse.
	blocks := ParseBlocks([]byte(searchDoc), darkStyles(t))

	cases := []struct {
		name  string
		query string
		want  []int
	}{
		{"a plain match", "rollback plan", []int{searchParaIdx}},
		// The document's FIRST block, which nothing else in the suite
		// reaches: every other want here starts at 1 or later, so a loop
		// that skipped index 0 outright would pass without this.
		{"the document's first block", "rollout plan", []int{searchTitleIdx}},
		{"no match anywhere", "kubernetes", nil},
		{"empty query", "", nil},
		{"whitespace-only query", " \t\n ", nil},
		// The source wraps between "step" and "needs", so a raw
		// strings.Contains for this query is false on that block. The
		// projection is what carries this one -- a soft break is a space by
		// the time it is drawn -- and the fence case below is what pins the
		// normalization on the fallback side, where there is no projection.
		{"words the source separates with a newline", "step needs", []int{searchParaIdx}},
		{"doubled space in the query", "deploy  step", []int{searchParaIdx}},
		// A doubled space the AUTHOR typed, which the projection carries
		// through verbatim: only normalizing the block side collapses it.
		{"doubled space in the block's own source", "migration timing", []int{searchListIdx}},
		// ASCII only, and that is the whole reach of the claim: ToLower is
		// simple lowercasing, not case folding, and misses pairs EqualFold
		// matches -- see the note at SearchBlocks.
		{"uppercase ASCII query over lowercase text", "ROLLBACK PLAN", []int{searchParaIdx}},
		{"lowercase ASCII query over uppercase text", "migration", []int{searchListIdx}},
		{"several matches come back in document order", "deploy", []int{searchHeadingIdx, searchParaIdx, searchAccentIdx}},
		// Inside a fence, and across one of its newlines: a fence has no
		// projection, so this is the block side falling back to the raw
		// source slice with every line break still in it.
		{"words a fence's source separates with a newline", "rollback() { return nil", []int{searchCodeIdx}},
		{"a non-ASCII term", "crème brûlée", []int{searchAccentIdx}},
		{"a heading by its title text", "Deployment Steps", []int{searchHeadingIdx}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SearchBlocks(blocks, c.query)
			// Checked apart from slices.Equal, which cannot tell nil from an
			// empty slice: "no hits is nil" is part of the contract a caller
			// ranging over the result relies on.
			if c.want == nil && got != nil {
				t.Fatalf("SearchBlocks(%q) = %v, want nil", c.query, got)
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("SearchBlocks(%q) = %v, want %v", c.query, got, c.want)
			}
		})
	}
}

// nfcAccent and nfdAccent are ONE word in the two Unicode normalization forms:
// a precomposed "\u00e9", and the same letter as "e" plus a combining acute.
// Spelled with escapes rather than typed, because an editor, a paste buffer or
// a checkout filter can renormalize a literal on its way into the file and
// leave the table below asserting nothing at all -- which is exactly how
// TestSearchBlocks's "a non-ASCII term" case came to be precomposed on both
// sides without saying so.
const (
	nfcAccent = "expos\u00e9 the retry budget"
	nfdAccent = "expose\u0301 the retry budget"
)

// TestSearchBlocksIsSensitiveToNormalizationForm pins a RESIDUAL and not a
// feature. SearchBlocks matches bytes after whitespace normalization and
// lowercasing, neither of which touches normalization form, so the two
// spellings of one word do not find each other -- see the note at SearchBlocks
// for why that is left standing and why macOS is where it bites.
//
// ALL FOUR CELLS, because the diagonals are what make the misses meaningful:
// a table that only drove the two mismatches would also pass against a
// SearchBlocks that had stopped matching accented text at all.
func TestSearchBlocksIsSensitiveToNormalizationForm(t *testing.T) {
	if nfcAccent == nfdAccent {
		t.Fatal("the two spellings are the same string -- the escapes were normalized away and this table proves nothing")
	}
	for _, tc := range []struct {
		name  string
		doc   string
		query string
		want  []int
	}{
		{"precomposed document, precomposed query", nfcAccent, nfcAccent, []int{0}},
		{"precomposed document, decomposed query", nfcAccent, nfdAccent, nil},
		{"decomposed document, precomposed query", nfdAccent, nfcAccent, nil},
		{"decomposed document, decomposed query", nfdAccent, nfdAccent, []int{0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocks := ParseBlocks([]byte(tc.doc+"\n"), darkStyles(t))
			if len(blocks) != 1 {
				t.Fatalf("the document parsed to %d blocks, want 1", len(blocks))
			}
			got := SearchBlocks(blocks, tc.query)
			if tc.want == nil && got != nil {
				t.Fatalf("SearchBlocks(%q) = %v, want nil", tc.query, got)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("SearchBlocks(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

// TestSearchAndResolveAnchorDisagreeOnCase pins the single deliberate
// divergence between the two "which block contains this text" predicates, so
// that unifying them fails here instead of silently widening what a stored
// anchor matches. Both are handed the same blocks and the same word; the
// search finds it whatever case it is typed in, the anchor only at the exact
// case the document spells it.
func TestSearchAndResolveAnchorDisagreeOnCase(t *testing.T) {
	blocks := ParseBlocks([]byte(searchDoc), nil)
	path := blocks[searchListIdx].HeadingPath

	if got := SearchBlocks(blocks, "migration"); !slices.Equal(got, []int{searchListIdx}) {
		t.Fatalf("SearchBlocks folds case: got %v, want %v", got, []int{searchListIdx})
	}
	if got := ResolveAnchor(blocks, reanchor.Anchor{HeadingPath: path, Span: "migration"}); got != -1 {
		t.Fatalf("ResolveAnchor must not fold case: got %d, want -1", got)
	}
	if got := ResolveAnchor(blocks, reanchor.Anchor{HeadingPath: path, Span: "MIGRATION"}); got != searchListIdx {
		t.Fatalf("ResolveAnchor at the document's own case = %d, want %d", got, searchListIdx)
	}
}

// searchRenderedDoc is markdown whose SOURCE and whose SCREEN say different
// things, which is the whole reason SearchBlocks matches a projection rather
// than Block.Text. Every block here is a case where a naive substring search
// over the source gives an answer the reader would call wrong: emphasis
// splits a phrase that is unbroken on screen, a link carries a URL nobody
// sees, an autolink carries one everybody does, a code span's backticks are
// not on screen but its text is, and a soft line break is a space by the time
// it is drawn. The heading and the fence are the other half of the claim --
// they get no projection, because for them the source IS what renders,
// asterisks and all.
const searchRenderedDoc = "## Ship the **deploy** gate\n\nThe **deploy** step needs work.\n\nSee [the runbook](https://x.io/deploy-only) for details.\n\nWatch <https://status.example.com/deploy> during the rollout.\n\nRun `kubectl rollout status`\nbefore paging anyone.\n\n```go\n// deploy marks the **start**\n```\n"

// The blocks searchRenderedDoc parses to, in order.
const (
	renderedHeadingIdx = iota
	renderedEmphIdx
	renderedLinkIdx
	renderedAutolinkIdx
	renderedCodeSpanIdx
	renderedFenceIdx
)

func TestSearchBlocksMatchesRenderedText(t *testing.T) {
	blocks := ParseBlocks([]byte(searchRenderedDoc), darkStyles(t))

	cases := []struct {
		name  string
		query string
		want  []int
	}{
		// Source: "The **deploy** step needs work." -- no "deploy step" in it.
		{"a phrase inline emphasis splits in the source", "deploy step", []int{renderedEmphIdx}},
		// Source: "[the runbook](https://x.io/deploy-only)" -- the reader sees
		// "the runbook" and no URL at all, so a hit here would land the cursor
		// on a block with nothing visible to show for it.
		{"a token only a link URL holds", "x.io", nil},
		// An autolink's URL is drawn, so it is fair game.
		{"an autolink URL, which the reader does see", "status.example.com", []int{renderedAutolinkIdx}},
		// The projection must not reintroduce plainText's gluing bug, which
		// would make this "statusbefore".
		{"a phrase spanning a soft line break", "status before paging", []int{renderedCodeSpanIdx}},
		{"a code span's text", "kubectl rollout", []int{renderedCodeSpanIdx}},
		// Neither of these has a projection, so both match their own source --
		// which is exactly what is drawn for them, markers included.
		{"a heading by its literal markers", "the **deploy** gate", []int{renderedHeadingIdx}},
		{"a fence by its literal markers", "the **start**", []int{renderedFenceIdx}},
		// Every block whose source contains "deploy" except the link, in
		// document order.
		{"the link block is the one omission", "deploy", []int{renderedHeadingIdx, renderedEmphIdx, renderedAutolinkIdx, renderedFenceIdx}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SearchBlocks(blocks, c.query)
			if c.want == nil && got != nil {
				t.Fatalf("SearchBlocks(%q) = %v, want nil", c.query, got)
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("SearchBlocks(%q) = %v, want %v", c.query, got, c.want)
			}
		})
	}
}

// TestSearchProjectionCarriesNoStyling asserts on DisplayPlain directly: the
// projection is built by the same styled walk Display is, so it has to have
// the escapes taken back off or every needle spanning a styled run would miss
// on a reset code the reader cannot see or type. Checking Display too keeps
// the test from going vacuous if renderInlines ever stopped emitting escapes
// from a zero style -- then there would be nothing left for it to prove.
func TestSearchProjectionCarriesNoStyling(t *testing.T) {
	blocks := ParseBlocks([]byte(searchRenderedDoc), darkStyles(t))
	for _, tc := range []struct {
		name string
		idx  int
	}{
		{"bold", renderedEmphIdx},
		{"link", renderedLinkIdx},
		{"code span", renderedCodeSpanIdx},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := blocks[tc.idx]
			if !strings.ContainsRune(b.Display, 0x1b) {
				t.Fatalf("Display %q carries no escapes, so this proves nothing", b.Display)
			}
			if strings.ContainsRune(b.DisplayPlain, 0x1b) {
				t.Fatalf("DisplayPlain = %q, want no escapes (stripped: %q)", b.DisplayPlain, ansi.Strip(b.DisplayPlain))
			}
		})
	}
}

// TestSearchBlocksWithoutStyles pins that the search projection is computed
// unconditionally, not off the back of Display. ParseBlocks leaves Display
// empty for a caller that wants blocks without a screen to draw them on, and
// a projection that inherited that would make search match nothing at all for
// exactly those callers -- a failure with no symptom other than an empty
// result.
func TestSearchBlocksWithoutStyles(t *testing.T) {
	blocks := ParseBlocks([]byte(searchRenderedDoc), nil)
	if got := blocks[renderedEmphIdx].Display; got != "" {
		t.Fatalf("Display without styles = %q, want empty", got)
	}
	if got := SearchBlocks(blocks, "deploy step"); !slices.Equal(got, []int{renderedEmphIdx}) {
		t.Fatalf("SearchBlocks(%q) = %v, want %v", "deploy step", got, []int{renderedEmphIdx})
	}
}

// TestSearchOverATableFindsTheRowTheWordIsOn is a change to what a table row
// contributes to this index and therefore to where the cursor lands. A row
// carries its OWN cells and nothing else -- the header is drawn once, as a row
// of its own, so the header's words are found on the header's row, which is
// where they are.
//
// THE THIRD CASE IS THE ONE THAT WOULD REGRESS SILENTLY. "step: deploy" is a
// hit for a projection that kept the labels, and no such text is on screen
// anywhere -- so answering it would point the reader at text they cannot see.
func TestSearchOverATableFindsTheRowTheWordIsOn(t *testing.T) {
	const tableDoc = "| Step | Owner |\n|---|---|\n| Deploy | dana |\n| Roll back | sam |\n"
	const (
		tableHeaderIdx = iota
		tableFirstRowIdx
		tableSecondRowIdx
	)
	blocks := ParseBlocks([]byte(tableDoc), darkStyles(t))
	if len(blocks) != 3 || !blocks[tableHeaderIdx].Table.Header {
		t.Fatalf("blocks = %+v, want a header row and two body rows", blocks)
	}
	for _, c := range []struct {
		name  string
		query string
		want  []int
	}{
		{"a header word, on the header's own row", "step", []int{tableHeaderIdx}},
		{"the other header word", "owner", []int{tableHeaderIdx}},
		{"a body word, on its own row", "roll back", []int{tableSecondRowIdx}},
		{"a body word from the other row", "dana", []int{tableFirstRowIdx}},
		{"the label a row used to be drawn with", "step: deploy", nil},
		{"a header word and a body word together are on no single row", "step deploy", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := SearchBlocks(blocks, c.query); !slices.Equal(got, c.want) {
				t.Fatalf("SearchBlocks(%q) = %v, want %v", c.query, got, c.want)
			}
		})
	}
}

// searchControlPayloads is the three spellings of one carriage return that a
// search has to keep apart: "a rendered ␍ is a search hit for ␍ even if the
// underlying text of the plan contained a carriage return", and "a search for
// the escape text should only match when that text has been printed to the plan
// view".
//
// THE ESCAPE TEXT AND THE GLYPH ARE DIFFERENT STRINGS AND MUST NOT COLLIDE IN
// EITHER DIRECTION, which is why all three payloads and all three queries are
// in one table rather than in three tests. A change that made the index carry
// the raw byte again loses the first row; one that "helpfully" mapped `\r` to
// the glyph loses the third; and a table with only the first row would pass
// under both.
//
// THE THIRD QUERY IS THE DRIFT AND IS ON NO SCREEN AT ALL. "left right" is what
// reanchor.Normalize made of a raw carriage return on the three arms that answer
// Block.Text, so searching it found a block whose drawn row has no space in it
// anywhere. It is a MISS on every row here.
//
// `drawn` IS THE FIXTURE GUARD and it is checked before any search answer: it is
// the screen that makes the query the right one to type.
var searchControlPayloads = []struct {
	name, payload, drawn string
	// The answer each of the three queries must give for this payload: the
	// block, or no hit at all.
	glyph, escapeText, folded bool
}{
	{"a real carriage return", "left\rright", "left␍right", true, false, false},
	{"the literal rune ␍, which a plan may simply be about", "left␍right", "left␍right", true, false, false},
	{"the escape text, which the author wrote out", `left\rright`, `left\rright`, false, true, false},
}

// searchArmBlock is the index of the one block an arm's fixture put the
// payload in -- 0 for six of the seven arms and 1 for a table BODY cell, whose
// document needs a header row above it. Found by looking in Block.Text, which
// is the raw source slice and is the one field this fix deliberately does not
// touch.
func searchArmBlock(t *testing.T, blocks []Block, payload string) int {
	t.Helper()
	found := -1
	for i, b := range blocks {
		if strings.Contains(b.Text, payload) {
			if found >= 0 {
				t.Fatalf("blocks %d and %d both hold %q, so a hit list cannot say which arm answered", found, i, payload)
			}
			found = i
		}
	}
	if found < 0 {
		t.Fatalf("no block's Text holds %q -- the fixture never carried the payload and every answer below is vacuous", payload)
	}
	return found
}

// TestSearchAgreesWithWhatEachArmDraws is channel 4, the search index, which
// reaches no renderer and no frame at all.
//
// FOUR OF THE SEVEN ARMS CAME OUT RIGHT FOR FREE and three did not, and the
// split is the finding rather than the fix. DisplayPlain is ansi.Strip of the
// same renderInlines walk the leaf filter sits in, so a paragraph, a list item
// and both table cells carried Control Pictures into this index the moment that
// filter landed. A heading, a fenced code block and the fallback arm have no
// projection and answer Block.Text, which is raw by design:
//
//	arm                 search ␍   search "left right"
//	heading             MISS       HIT   <- on text nowhere on screen
//	fenced code         MISS       HIT
//	fallback            MISS       HIT
//	paragraph           hit        miss
//	list item           hit        miss
//	table header cell   hit        miss
//	table body cell     hit        miss
//
// SO THE THREE RAW ARMS WERE WRONG IN BOTH DIRECTIONS AT ONCE, and the second
// matters more. reanchor.Normalize is strings.Fields, which splits on CR, VT and
// FF, so on those arms a carriage return was FOLDED TO A SPACE in the index
// while the arm drew a glyph and no space: the reader could not find the row
// they were looking at, and could find it by typing something that is not on it.
// plainControls (ui/control.go) is the one line that closes both.
func TestSearchAgreesWithWhatEachArmDraws(t *testing.T) {
	st := darkStyles(t)
	for _, arm := range controlArms {
		for _, p := range searchControlPayloads {
			t.Run(arm.name+"/"+p.name, func(t *testing.T) {
				rows, blocks := controlArmRows(t, st, arm.src, p.payload, 80)
				if drawn := ansi.Strip(strings.Join(rows, "\n")); !strings.Contains(drawn, p.drawn) {
					t.Fatalf("this arm drew %q, which does not contain %q -- the payload never reached the screen, so nothing below is a statement about what the reader sees", drawn, p.drawn)
				}
				idx := searchArmBlock(t, blocks, p.payload)
				for _, q := range []struct {
					name, query string
					want        bool
				}{
					{"the glyph", "left␍right", p.glyph},
					{"the escape text", `left\rright`, p.escapeText},
					{"the folded form, which is on no screen", "left right", p.folded},
				} {
					var want []int
					if q.want {
						want = []int{idx}
					}
					got := SearchBlocks(blocks, q.query)
					if want == nil && got != nil {
						t.Fatalf("searching %s (%q) returned %v, want no hit at all -- %q is not what this arm drew, and a hit here parks the cursor on a row the query is not on", q.name, q.query, got, q.query)
					}
					if !slices.Equal(got, want) {
						t.Fatalf("searching %s (%q) returned %v, want %v -- the arm drew %q and this index must answer for the same text", q.name, q.query, got, want, p.drawn)
					}
				}
			})
		}
	}
}

// eightBitControls is the C1 set's introducers in the two spellings a document
// can deliver them in, plus the one C0/C1 mixture that reaches the SAME defect
// by a different road.
//
// EVERY ROW HERE IS A BYTE ansi.Strip TREATS AS A TERMINAL COMMAND. 0x9F is an
// 8-bit APC to that parser and an APC swallows to a string terminator the
// payload never sends, so ansi.Strip over `alpha\x9fbeta gamma` answers
// `alpha`: five characters of an eighteen-character line, with no error and no
// mark on the screen. The others differ only in how much they eat.
var eightBitControls = []struct{ name, ctl string }{
	{"APC 0x9f", "\x9f"},
	{"OSC 0x9d", "\x9d"},
	{"DCS 0x90", "\x90"},
	{"SOS 0x98", "\x98"},
	{"PM 0x9e", "\x9e"},
	{"CSI 0x9b", "\x9b"},
	{"UTF-8-encoded CSI U+009B", "\u009b"},
	{"a backspace AND an APC", "\b\x9f"},
}

// TestSearchAfterSelector16StripAgreesWithTheScreen is stripSelector16's
// consequence for the search index, stated as an assertion rather than left for
// a reader to discover: DisplayPlain and Block.Text's searchText fallback both
// go through the same functions the draw arms do, so every arm draws the bare
// base and never the selector.
//
// BOTH SPELLINGS OF THE QUERY MUST FIND THAT SAME DRAWN TEXT. Before that,
// only the query's HAYSTACK side went through the strip and the needle did
// not, so searching the full base+U+FE0F sequence found NOTHING -- and a
// reader cannot see the difference between the two spellings on screen, while
// both an emoji picker and a copy out of the document source produce the
// selector form.
func TestSearchAfterSelector16StripAgreesWithTheScreen(t *testing.T) {
	st := darkStyles(t)
	const withSelector = "warning⚠️sign"
	const bareBase = "warning⚠sign"
	for _, arm := range controlArms {
		t.Run(arm.name, func(t *testing.T) {
			rows, blocks := controlArmRows(t, st, arm.src, withSelector, 80)
			if drawn := ansi.Strip(strings.Join(rows, "\n")); !strings.Contains(drawn, bareBase) {
				t.Fatalf("this arm drew %q, which does not contain %q -- the payload never reached the screen without its selector, so nothing below is a statement about what the reader sees", drawn, bareBase)
			}
			idx := searchArmBlock(t, blocks, withSelector)
			if got := SearchBlocks(blocks, withSelector); !slices.Equal(got, []int{idx}) {
				t.Fatalf("searching the full sequence (base + U+FE0F) returned %v, want [%d] -- a reader cannot see the selector on screen, so a query carrying it must still find what is drawn there", got, idx)
			}
			if got := SearchBlocks(blocks, bareBase); !slices.Equal(got, []int{idx}) {
				t.Fatalf("searching the bare base returned %v, want [%d] -- that is what every arm now draws", got, idx)
			}
		})
	}
}

// TestAnEightBitControlDoesNotTruncateWhatFollowsIt pins the rejected
// alternative -- a silent strip -- arriving on the document channel through a
// library rather than through a decision.
//
// ansi.Strip IS A TERMINAL PARSER AND NOT A STYLING REMOVER, which is the whole
// mechanism. Block.DisplayPlain was ansi.Strip of the projection walk, so a
// paragraph reading `alpha\x9fbeta gamma` projected to `alpha` while the styled
// Display carried the line to the screen WHOLE:
//
//	Text          "alpha\x9fbeta gamma"      intact -- hashed, anchored, approved
//	DisplayPlain  "alpha"                    everything after the byte GONE
//	painted row   ...alpha\x9fbeta gamma...   the reader sees the whole line
//	search(gamma) []                          on text that IS on the screen
//
// SO THE HALF THAT MATTERS IS THE SEARCH: the reader could see `gamma`, type
// `gamma`, and be told the document does not contain it. stripStyling
// (ui/control.go) is the fix; the byte itself still reaches the terminal, which
// is a ruling and is written down at isVisibleControl.
//
// THE SEVENTH ARM IS THE ONE THAT MOVED ON SCREEN. A table HEADER cell is drawn
// from DisplayPlain (tableGridRow), so before this it alone drew `alpha` and
// stopped. All seven arms now draw the line whole.
func TestAnEightBitControlDoesNotTruncateWhatFollowsIt(t *testing.T) {
	st := darkStyles(t)
	for _, arm := range controlArms {
		for _, c := range eightBitControls {
			t.Run(arm.name+"/"+c.name, func(t *testing.T) {
				payload := "alpha" + c.ctl + "beta gamma"
				rows, blocks := controlArmRows(t, st, arm.src, payload, 80)
				frame := strings.Join(rows, "\n")
				// THE SCREEN FIRST. A byte the arm never drew would make every
				// assertion below a statement about a payload nobody can see,
				// and the C1 residual is exactly that this byte IS drawn.
				if !strings.Contains(frame, "gamma") {
					t.Fatalf("this arm drew no %q at all: %q -- the payload was cut before it reached a row, so the index below has nothing to agree with", "gamma", stripStyling(frame))
				}
				idx := searchArmBlock(t, blocks, payload)
				if got := SearchBlocks(blocks, "gamma"); !slices.Equal(got, []int{idx}) {
					t.Fatalf("searching %q returned %v, want %v -- the byte %q is an 8-bit introducer to ansi.Strip, so the projection stopped at it while the arm drew %q: a reader cannot find text that is on their screen",
						"gamma", got, []int{idx}, c.ctl, stripStyling(frame))
				}
				if got := SearchBlocks(blocks, "beta"); !slices.Equal(got, []int{idx}) {
					t.Fatalf("searching %q returned %v, want %v -- the word immediately AFTER %q is the first thing an 8-bit introducer eats", "beta", got, []int{idx}, c.ctl)
				}
			})
		}
	}
}

// TestTheSearchIndexCollidesWhereTheFrameDistinguishes is the answer for
// channel 4, and the answer is that the reverse-video distinction does not
// live here and does not have to.
//
// searchText RETURNS A PLAIN STRING, which SearchBlocks then lowercases and
// whitespace-normalises, so there is nowhere in it for a reverse-video run to
// survive. A document WRITING about `␍` and a document CONTAINING a carriage
// return are therefore the same needle, and the ruling says that is right: both
// draw `␍` and both should be found by typing `␍`.
//
// WHAT CARRIES THAT DISTINCTION INSTEAD IS THE FRAME THE HIT TAKES THE READER
// TO. Both halves are asserted together on purpose: the collision has to be
// REAL for the frame's distinction to be the thing that resolves it, so a
// change that split the index would fail here rather than silently making the
// second half redundant.
func TestTheSearchIndexCollidesWhereTheFrameDistinguishes(t *testing.T) {
	st := darkStyles(t)
	for _, arm := range controlArms {
		t.Run(arm.name, func(t *testing.T) {
			realRows, realBlocks := controlArmRows(t, st, arm.src, "left\rright", 80)
			glyphRows, glyphBlocks := controlArmRows(t, st, arm.src, "left␍right", 80)

			realHits, glyphHits := SearchBlocks(realBlocks, "left␍right"), SearchBlocks(glyphBlocks, "left␍right")
			if len(realHits) == 0 || len(glyphHits) == 0 {
				t.Fatalf("one of the two documents is not found by %q at all (real %v, written %v) -- there is no collision here for the frame to resolve", "left␍right", realHits, glyphHits)
			}
			if !slices.Equal(realHits, glyphHits) {
				t.Fatalf("the index already tells the two apart (real %v, written %v) -- this test is asserting the wrong thing, and whichever way it now differs is a change to what a search means", realHits, glyphHits)
			}

			real, written := strings.Join(realRows, "\n"), strings.Join(glyphRows, "\n")
			if ansi.Strip(real) != ansi.Strip(written) {
				t.Fatalf("the two frames already read differently with the styling stripped off:\n  a real CR: %q\n  the rune ␍: %q", ansi.Strip(real), ansi.Strip(written))
			}
			if hasReverseVideo(written) {
				t.Fatalf("the literal rune ␍ was drawn in reverse video -- then the frame says nothing either, and a reader who searched ␍ has no way at all to tell a plan ABOUT a control byte from one carrying it")
			}
			if hasReverseVideo(real) != arm.styled {
				t.Fatalf("reverse video on a real carriage return = %v, want %v for this arm -- see TestAControlByteIsNotItsOwnGlyph for the one arm that is an exception and why", hasReverseVideo(real), arm.styled)
			}
		})
	}
}

// benchmarkDoc is n blocks of plan-shaped markdown: a heading every tenth
// block, and paragraphs carrying one emphasis span and one soft line break,
// which is what gives renderInlines real work to do. Deterministic, so two
// runs of the benchmark below measure the same document.
func benchmarkDoc(n int) []byte {
	var b strings.Builder
	for i := range n {
		if i%10 == 0 {
			fmt.Fprintf(&b, "## Section %d\n\n", i/10)
			continue
		}
		fmt.Fprintf(&b, "The **deploy** step %d needs a rollback plan\nbefore the gate is run again.\n\n", i)
	}
	return []byte(b.String())
}

// BenchmarkParseBlocks is the baseline the cost recorded at plainProjection is
// measured against, and it exists so that number can be re-derived rather than
// believed. Both style arms are here because the ratio depends entirely on
// which one you are in: with styles there are three renderInlines walks per
// block and DisplayPlain is the third of them, and without styles it is the
// ONLY one. RefreshFromSession is the with-styles arm (Model.New never holds a
// nil Styles); session.Title is the other.
func BenchmarkParseBlocks(b *testing.B) {
	for _, size := range []int{250, 2500} {
		for _, arm := range []struct {
			name   string
			styles func(testing.TB) *Styles
		}{
			{"with styles", darkStyles},
			{"without styles", func(testing.TB) *Styles { return nil }},
		} {
			b.Run(fmt.Sprintf("%d blocks/%s", size, arm.name), func(b *testing.B) {
				src := benchmarkDoc(size)
				st := arm.styles(b)
				if got := len(ParseBlocks(src, st)); got != size {
					b.Fatalf("the fixture parsed to %d blocks, want %d", got, size)
				}
				b.ReportAllocs()
				for b.Loop() {
					ParseBlocks(src, st)
				}
			})
		}
	}
}

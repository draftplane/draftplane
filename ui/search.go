package ui

import (
	"strings"

	"github.com/draftplane/draftplane/reanchor"
)

// SearchBlocks returns the indices of every block whose displayed text
// contains query, in document order, or nil for a query that is empty or all
// whitespace.
//
// IT MATCHES WHAT THE READER SEES, not the source. A Block's Text is the exact
// source slice: "The **deploy** step needs work." does not contain "deploy
// step" at all, while "See [the runbook](https://x.io/deploy)." does contain
// "deploy" though no URL is drawn. searchText, below, is what corrects for it.
//
// Matching runs over reanchor.Normalize'd text on BOTH sides, which also TRIMS
// the query -- so a trailing space, the only crude word-boundary control a
// substring search gives anyone, is gone. Kept anyway: a trailing space is far
// more often the tail of a copy-paste than a deliberate request.
//
// TWO RESIDUALS, both known and both left standing. An INVISIBLE CHARACTER is
// not whitespace to strings.Fields, so a query made of zero-width spaces or
// joiners normalizes to a non-empty needle that matches nothing. And UNICODE
// NORMALIZATION FORM IS NOT FOLDED: a precomposed U+00E9 and a decomposed "e"
// plus U+0301 stay two different needles in two different haystacks, so on
// macOS a reader whose keyboard disagrees with the file's own form asks for a
// word that is visibly on screen and is told there are no matches. Folding both
// sides is a different design with a per-keystroke cost and its own decision
// about which form wins; the residual is CHOSEN rather than inherited, and
// TestSearchBlocksIsSensitiveToNormalizationForm pins all four answers so an
// NFC pass has to change a test that says what it is changing.
//
// NIL ANSWERS TWO DIFFERENT QUESTIONS -- "you have not typed anything yet" and
// "nothing in this document matches" -- on purpose, because a caller ranging
// over the result to paint hits wants both to paint nothing. A caller that has
// to TELL them apart must branch on the query string.
//
// IT LOWERCASES, AND ResolveAnchor DELIBERATELY DOES NOT. An anchor span
// addresses the exact bytes a review fact is keyed to, so "MIGRATION" and
// "migration" are different spans and must resolve differently; a search is a
// person hunting a word they half-remember. Unifying the two predicates would
// quietly widen what an anchor matches, which changes what a stored review fact
// means. TestSearchAndResolveAnchorDisagreeOnCase pins them apart. It folds
// with strings.ToLower, which is not case folding, so an all-caps Greek word is
// not found by its lowercase spelling -- as it is not by strings.EqualFold
// either.
func SearchBlocks(blocks []Block, query string) []int {
	// Normalized and folded ONCE: the query is the same string for every
	// block, and a long document makes this the difference between one pass
	// over it and one per block.
	//
	// THE QUERY GOES THROUGH stripSelector16 BECAUSE THE HAYSTACK DOES.
	// searchText (below) strips U+FE0F, so without the identical strip here
	// the selector-bearing spelling of an emoji -- what every emoji picker and
	// a copy out of the document source both produce -- found nothing while
	// the bare base found everything. A reader cannot see the difference
	// between the two spellings on screen, so both must find the same blocks.
	needle := strings.ToLower(reanchor.Normalize(stripSelector16(query)))
	if needle == "" {
		return nil
	}
	var hits []int
	for i, b := range blocks {
		if strings.Contains(strings.ToLower(reanchor.Normalize(b.searchText())), needle) {
			hits = append(hits, i)
		}
	}
	return hits
}

// searchText is the text a search looks in: the plain projection of the
// block's rendered inlines when it has one, and the source slice when it does
// not.
//
// IT IS NOT A MIRROR OF displayIn's FALLBACK. Four kinds have no projection
// (see Block.DisplayPlain), and what stands in for it differs: a HEADING and a
// FENCED CODE BLOCK are drawn from Text DIRECTLY, so for them the source really
// is what renders and a heading spelled "A **bold** heading" is rightly found
// by its asterisks; ParseBlocks' FALLBACK ARM does go through displayIn, and
// searching its source is right for the same reason read the other way round,
// nobody having taught the walk to project that construct; and a KindRule draws
// NONE of its Text, where falling back to it anyway is deliberate, `---` being
// bytes that are genuinely in the document.
//
// A TABLE ROW SEARCHES ITS OWN CELLS AND NOTHING ELSE: the header is drawn once
// on its own Block, so a per-row label would be searchable text that is not on
// the row that carried it.
//
// AND IT IS NOT A FIFTH WAY INTO THE FALLBACK, which is the boundary the guard
// below holds. A row whose cells are ALL EMPTY projects to "", the same string
// the four kinds above arrive with -- so without the guard such a row searched
// its SOURCE LINE and `|` found it, which is markup the reader never sees.
// Block.Cells is what tells the two emptinesses apart, and DisplayPlain is
// computed for every caller, so the answer does not depend on whether the
// document was parsed with styles. displayIn (ui/render.go) holds the other
// half.
//
// A CONTROL BYTE IS SEARCHED AS THE GLYPH THE READER SEES. `# left\rright`
// DRAWS as `left␍right` on every arm, so a reader with that row in front of
// them must land on it by typing the glyph; and the escape sequence and the
// glyph are different strings, so a document that spells out `\r` is found by
// searching for `\r` and never by searching for `␍`.
//
// Four of the seven arms get that for free -- DisplayPlain is a strip of the
// same renderInlines walk the leaf filter sits in -- and the three that answer
// Block.Text, which is RAW BY DESIGN, are the three plainControls
// (ui/control.go) exists for. AND THE FOLD IS WHY IT IS NOT PURELY COSMETIC:
// reanchor.Normalize is strings.Fields, which splits on CR, VT and FF, so on
// those arms a real carriage return WAS FOLDED TO A SPACE in this index while
// the screen showed a glyph and no space. Substituting first settles it,
// because a Control Picture is not whitespace and survives the fold.
//
// THE REVERSE-VIDEO DISTINCTION IS UNIMPLEMENTABLE HERE and nothing replaces
// it in this string: a document WRITING about `␍` and one CONTAINING a
// carriage return are the same needle to a plain-string index, and the
// reverse-video run that tells them apart cannot live in a string
// SearchBlocks lowercases and normalises. What carries that distinction
// instead is the FRAME the hit takes the reader to;
// TestTheSearchIndexCollidesWhereTheFrameDistinguishes drives both halves.
func (b Block) searchText() string {
	if b.DisplayPlain != "" {
		return b.DisplayPlain
	}
	if len(b.Cells) > 0 {
		return ""
	}
	return plainControls(b.Text)
}

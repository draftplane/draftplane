package ui

import (
	"fmt"
	"strings"

	"github.com/draftplane/draftplane/placement"
	"github.com/draftplane/draftplane/reanchor"
)

// ResolveAnchor finds the block an anchor displays at: the block whose
// normalized text contains the anchor span, preferring an exact heading-path
// match. An empty span marks a section anchor, which resolves to the heading
// block whose path matches exactly, with no span handling at all. Returns -1
// when nothing contains it (belongs in unanchored).
//
// CASE-SENSITIVE ON PURPOSE, unlike SearchBlocks (ui/search.go), which asks a
// superficially identical "which block contains this text" question over the
// same blocks. A span is the identity of the bytes a review fact was keyed to
// -- the SOURCE bytes, where a search matches the projection of what is drawn
// -- and not a query, so folding case here would widen what an existing
// anchor matches, silently changing which block a stored comment claims to be
// about. Read the longer note at SearchBlocks before making the two share a
// predicate.
func ResolveAnchor(blocks []Block, a reanchor.Anchor) int {
	if a.Span == "" {
		for i, b := range blocks {
			if pathsEqual(b.HeadingPath, a.HeadingPath) {
				return i
			}
		}
		return -1
	}
	span := reanchor.Normalize(a.Span)
	fallback := -1
	for i, b := range blocks {
		if !strings.Contains(reanchor.Normalize(b.Text), span) {
			continue
		}
		if pathsEqual(b.HeadingPath, a.HeadingPath) {
			return i
		}
		if fallback == -1 {
			fallback = i
		}
	}
	return fallback
}

// FindHeadingBlock returns the index of the heading block whose HeadingPath
// equals path exactly, or -1 if none matches. Used by relocate's entry jump
// to walk an orphan's old heading path from its full depth up toward the
// root, landing on the nearest surviving ancestor once the leaf itself is
// gone.
func FindHeadingBlock(blocks []Block, path []string) int {
	for i, b := range blocks {
		if b.Kind == KindHeading && pathsEqual(b.HeadingPath, path) {
			return i
		}
	}
	return -1
}

func pathsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// BadgeFor renders a placement's migration state for display.
func BadgeFor(p placement.Placement) string {
	switch p.Status {
	case reanchor.StatusMoved:
		return "moved"
	case reanchor.StatusFuzzy:
		return fmt.Sprintf("fuzzy ~%d%%", int(p.Confidence*100))
	default:
		return ""
	}
}

// BlockAnchorSpan is the span a block-granularity comment anchors to.
func BlockAnchorSpan(b Block) string {
	return reanchor.Normalize(b.Text)
}

// AnchorsAsSection reports whether a comment aimed at b should be a SECTION
// anchor -- heading path, no span -- rather than a span anchor on b's own
// words. Every caller that asks "is this block a section?" must ask it here:
// today that is the first-comment path, relocate, and the compose panel's
// "(section)" label (all in app/actions.go), and they must agree, because two
// of them write the anchor and the third tells the reviewer what is about to
// be written.
//
// A RULE NEVER REACHES IT, and that is the answer this predicate owes
// KindRule rather than a case of its own: a thematic break carries no words,
// so both of the anchors below are wrong for it -- it is not a heading, so the
// false arm would send it to a SPAN anchor on its own markup. Anchorable,
// below, is what the two write sites ask FIRST, so the question "section or
// span?" is only ever put about a block that has words for one of the two
// answers.
//
// A QUOTED HEADING IS A HEADING AND IS NOT A SECTION, which is the whole
// reason this is a function rather than `b.Kind == KindHeading` at each site.
// It renders as a heading and is deliberately absent from the heading path, so
// its HeadingPath is the ENCLOSING section's -- and a section anchor built from
// that would be an anchor on a section the reviewer never had the cursor on.
// reanchor.ParseSections cannot see it either: its headings match
// `^(#{1,6})\s+`, anchored at column 0, so a `> #` line is not a section to the
// layer that would have to find it again. Falling through to a span anchor is
// right rather than merely safe: the heading's own text does occur inside the
// enclosing section, so CreateAnchor finds it and the thread resolves back to
// the quoted heading block itself.
func AnchorsAsSection(b Block) bool {
	return b.Kind == KindHeading && b.QuoteDepth == 0
}

// Anchorable reports whether a review fact can be aimed at b at all: false
// for a rule, whose Text is markup rather than words, and true for every
// other kind. The two block-aimed writes in app/ (the first comment on a
// block, and relocating an orphan onto one) ask it before they ask
// AnchorsAsSection, and refuse with "no content at cursor": REFUSE rather than
// clamp to some neighbouring block the reviewer did not put the cursor on.
//
// IT IS NOT A PROMISE THAT AN ANCHOR WILL BE CREATED, and the boundary is
// worth stating because the name invites the wider reading.
// reanchor.CreateAnchor refuses any span under three words and any span it
// cannot find in the document; a two-word paragraph is anchorable to this
// function and refused there. What this answers is the narrower question of
// whether there is anything to anchor TO -- and a rule is the only kind for
// which there is not.
//
// IT KEYS ON THE KIND, AND ON THE SPACED SPELLINGS THAT IS THE ONLY THING
// REFUSING THEM. The tempting alternative is a test on the text -- a rule's
// Text is markup, and `---` is one word -- and it is wrong by measurement
// rather than by taste. CommonMark lets a thematic break put spaces between
// its marks, so `- - -` and `* * *` are THREE WORDS to reanchor.Normalize and
// CreateAnchor accepts them, where it refuses `---` by the three-word floor. A
// word-count test here would pass exactly those through, and the floor would
// not catch them either. TestThematicBreakIsARuleBlock drives both spellings.
func Anchorable(b Block) bool {
	return b.Kind != KindRule
}

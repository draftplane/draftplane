package ui

import (
	"path/filepath"
	"strings"
)

// InferTitle derives a plan's title from its document: the text of the
// first UNQUOTED H1 heading, or the filename stem (path base without
// extension) if the document has no such H1 — including the empty-blocks
// case. A blank path (a sourceless plan, with no filename to fall back to)
// gets a fixed placeholder instead of filepath.Base("")'s nonsensical ".".
//
// QUOTED IS THE THIRD CLAUSE OF THE FILTER: a quotation must not name the
// document quoting it. Kind and Level alone do not exclude one, deliberately --
// a quoted heading IS a real KindHeading at its real Level, which is what gets
// it rendered as a heading inside the quote -- so the depth is what tells the
// two apart. It SKIPS rather than stops: a document's own H1 further down still
// wins, whatever order the two appear in.
//
// ⚠️ IT ANSWERS Block.Text RAW, CONTROL BYTES AND ALL, AND THAT IS DELIBERATE.
// Three of its four production call sites CREATE A PLAN with the answer, so a
// document whose H1 is "Requires approval\b...\bNo approval needed" is stored
// under that title and lands on the plan list.
//
// THE TRANSFORM IS AT THE DRAW AND NOT HERE, on three arguments:
//
//  1. IT IS NOT ENOUGH ON ITS OWN. plan.Title also comes from a rename, so the
//     list frame has to filter what it draws whatever this answers; filtering
//     here as well would be the same rule twice.
//  2. IT WOULD COLLAPSE A DISTINCTION THE SCREEN NEEDS. A substitution made
//     HERE is stored, so the drawing side could no longer tell a title that
//     CONTAINS a carriage return from one that spells "␍".
//  3. IT KEEPS THIS FUNCTION HONEST FOR ITS HEADLESS CALLERS. session and
//     mcptools have no terminal and no *Styles; a title is a LABEL to them.
//
// WHAT THAT LEAVES, SAID PLAINLY: the store keeps the raw bytes, and the MCP
// tools hand them to an agent as they are. Every Draftplane frame that paints
// them visualises them, so the forgery does not reach a reader; a surface that
// is not a Draftplane frame gets what the document held.
func InferTitle(blocks []Block, path string) string {
	for _, b := range blocks {
		if b.Kind == KindHeading && b.Level == 1 && b.QuoteDepth == 0 {
			return b.Text
		}
	}
	if path == "" {
		return "Untitled"
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

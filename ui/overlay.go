package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Overlay composites fg onto bg at column x, row y: each row of fg splices
// into the same row of bg, replacing that row's [x, x+width(fgRow)) span and
// leaving every other row -- and every column of the spliced row outside that
// span -- exactly as bg produced it. Exported because its one caller is in
// another package (composeSortModal, app/list.go).
//
// OVERLAY RESERVES NOTHING, and that is the point of building it as a primitive
// rather than one more panel: bg arrives as a complete, already-full-height
// render, and splicing fg into rows bg already has changes no row's line count
// and touches no layout arithmetic at all. A later reader tempted to add a
// modal to viewHeight's budget should stop here first -- it needs none, and a
// reservation that disagrees with what gets painted is its own class of defect.
//
// IT PAINTS NOTHING ITSELF. A CALLER'S fg MUST RENDER ITS OWN SOLID INTERIOR --
// background fill and any padding -- before reaching Overlay, or whatever bg
// had there shows through the gap.
//
// SGR CONTINUITY ACROSS THE SPLICE NEEDS NO MECHANISM HERE and deliberately has
// none: ansi.Cut's left-anchored truncation copies escape bytes through even
// while it is still skipping the printable cells before the cut point, so the
// trailing fragment carries the colour state that was active at the cut.
//
// PER-ROW WIDTH DOES NEED CODE. The invariant is
// ansi.StringWidth(out[i]) == ansi.StringWidth(bg[i]) for every row i, and it
// fails whenever x < 0, x > width(bg row), or x+width(fg row) overflows the bg
// row's width -- producing a row wider than bg's own, which WRAPS in the
// terminal and adds a visual row. Checking the composite's LINE COUNT cannot
// see this, because bg and the composite always agree on line count whatever
// one row's width is, so the clamp and any test of it must compare WIDTH PER
// ROW, never count. The clamp lives HERE rather than at any call site: it is an
// invariant of compositing itself, so a second consumer cannot reintroduce a
// wrapped row just by calling this the way the first one does.
//
// KEEP ansi.Cut, NOT ansi.CutWc. Cut accounts width by grapheme clusters, the
// same accounting ansi.StringWidth and therefore the clamp bounds use; CutWc
// measures individual wide runes, so pairing it with StringWidth-derived bounds
// lets the two disagree on any multi-codepoint cluster and reopens the mismatch
// this function exists to close -- and its worst case on this package's own
// wide-rune sweep is 37 columns against bg's 20, where Cut's is 21.
//
// A RESIDUAL SURVIVES Cut ITSELF: it will not split a grapheme cluster, so a
// cut boundary landing in the MIDDLE of one keeps the cluster WHOLE and can
// pull back a cell that was meant to be dropped -- one column over bg's own
// width on a CJK background. The bound applied is deliberately asymmetric: an
// over-wide row wraps and corrupts the display, an under-wide row leaves a
// cosmetic gap of at most one cluster. NEVER OVER, SOMETIMES UNDER. The fix is
// one more ansi.Cut pass on the fully-spliced row, which never returns a result
// WIDER than the length it is given and is a no-op on any row already within
// bgw.
func Overlay(bg, fg string, x, y int) string {
	lines := strings.Split(bg, "\n")
	for i, fgLine := range strings.Split(fg, "\n") {
		row := y + i
		if row < 0 || row >= len(lines) {
			continue
		}
		bgw := ansi.StringWidth(lines[row])
		cx := x
		if cx < 0 {
			cx = 0
		} else if cx > bgw {
			cx = bgw
		}
		if avail := bgw - cx; ansi.StringWidth(fgLine) > avail {
			fgLine = ansi.Cut(fgLine, 0, avail)
		}
		w := ansi.StringWidth(fgLine)
		spliced := ansi.Cut(lines[row], 0, cx) + fgLine + ansi.Cut(lines[row], cx+w, bgw)
		// A wide grapheme straddling the cx or cx+w seam can leave spliced
		// wider than bgw despite every width above having been clamped in
		// cells -- see the doc comment. This final cut is the hard upper
		// bound: never wider than bg's own row, only ever possibly
		// narrower.
		lines[row] = ansi.Cut(spliced, 0, bgw)
	}
	return strings.Join(lines, "\n")
}

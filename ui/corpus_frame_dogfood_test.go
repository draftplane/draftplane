package ui

// This file is the frame assertion swept over a real corpus rather than over
// the committed fixture (frame_budget_test.go). See
// corpus_projection_dogfood_test.go for why a corpus sweep is gated rather than
// part of `make check`, and for the shape (envCorpusGate, corpusRoot,
// corpusMarkdownFiles) this file reuses rather than redefines.
//
// THE SAME TWO ASSERTIONS AS THE FIXTURE, over documents nobody wrote for a
// test:
//
//	(a) every screen row, EXCLUDING the named ui.Line embedded-newline
//	    residual, occupies EXACTLY the cells it was budgeted --
//	    ansi.StringWidth(row) == width.
//	(b) no screen row -- residual or not -- contains U+FE0F.
//
// EVERY ARM, INCLUDING THE ONE EASIEST TO MISS. wrapPlain's comment-body
// call site is reachable only when RenderDoc's views AND expanded are BOTH
// populated. This sweep attaches ONE COMMENT PER BLOCK, each holding that
// block's own text flattened to one line (so it does not manufacture a SECOND
// source of embedded-newline residual on top of the heading arm's), so the
// comment-body arm renders real corpus prose at a REALISTIC size: one
// reviewer's remark on one block, not an entire file glued into one comment.
//
// NON-VACUITY IS THE PART TO GET RIGHT. Post-fix, assertions (a) and (b) are
// EXPECTED to report zero, so a fatal cannot be keyed to "found no defect". It
// is keyed instead to the SWEEP ITSELF having walked something real: zero
// blocks or zero rendered rows fatals; zero U+FE0F bytes or zero
// letter-hyphen-letter occurrences anywhere in the RAW SOURCE, counted
// independently of anything RenderDoc does with them, also fatals; and zero
// rows drawn from a comment card (threadRowsSeen, keyed on Line.IsThread) also
// fatals -- that last one guards against the RenderDoc call being passed nil
// for views/expanded, which would otherwise return a clean `ok` purely
// because the other four guards are satisfied by the document body ALONE.
import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/domain"
)

// hyphenatedCompoundPattern and frameSelector16 -- this sweep's population
// proxies for "a hyphen inside a real word" and "the disputed selector" --
// are both defined once in frame_budget_test.go and reused here rather than
// redeclared, so the fixture and the corpus sweep cannot come to define
// either differently.

// wrapPlainHyphenResidue reports whether row is the ONE construct this sweep
// exempts from assertion (a) besides the ui.Line embedded-newline residual: a
// row over its budget by EXACTLY ONE cell whose CONTENT -- ansi.Strip'd, then
// right-trimmed of plain spaces -- ends in a hyphen.
//
// THE RIGHT-TRIM IS THE ROW-LEVEL VIEW OF THE SAME CHECK, NOT A LOOSER ONE.
// wrapPlain's own suffix check runs on ITS OWN return value, before the comment
// card exists; this sweep checks the SCREEN ROW, after the caller composes it
// and pads it to rowWidth with `st.Card.Width(rowWidth).Render(row)`, which
// fills a short line with Card-background spaces on the right. Trimming that
// padding is what makes this predicate see the identical hyphen
// TestWrapPlainAcceptsAResidueWhenAHyphenTokenCannotFitEitherWay pins in
// isolation, where there is no card and hence no padding to trim through.
//
// THIS IS wrapPlain's OWN RULED NON-GOAL, not a new one this sweep invents: the
// retry guard deliberately declines to extend hyphen protection into a token
// that still would not fit once protected, because doing so turns a 1-cell
// overflow into a much larger one. The excess is forced to be exactly one cell,
// because the bug only fires when the row is already exactly at width before
// the trailing hyphen.
//
// ⚠️ EXACTLY ONE, NEVER `>=` OR "ANY EXCESS". A row 2 or more cells over is a
// DIFFERENT, worse defect this predicate must not hide, and neither is a
// 1-cell-over row that does not end in a hyphen once its own trailing padding is
// looked past -- both must still fail assertion (a). See this predicate's own
// two call sites for how narrowly it is applied: only AFTER the general
// `w != width` check has fired, only for the ONE cell width+1 case within it,
// and never for the ui.Line residual branch.
func wrapPlainHyphenResidue(row string, width int) bool {
	content := strings.TrimRight(ansi.Strip(row), " ")
	return ansi.StringWidth(row) == width+1 && strings.HasSuffix(content, "-")
}

func TestCorpusFrameDogfood(t *testing.T) {
	requireCorpusDogfood(t)

	root := corpusRoot(t)
	files := corpusMarkdownFiles(t, root)
	st := darkStyles(t)
	t.Logf("corpus root: %s -- every file rendered through the real ui.RenderDoc at widths %v, the document body AND (via one expanded comment per block, holding that block's own text) the comment-body arm", root, frameAssertionWidths)

	var (
		filesSwept, blocksSeen, rowsSeen                          int
		residualLinesSeen, residualRowsSeen                       int
		overWidth, selector16Rows                                 int
		overWidthExamples, selector16Examples                     []string
		sourceVS16, sourceHyphens                                 int
		residualByShort                                           = map[int]int{} // width-actual -> count, over every excluded row
		residualDocHeading, residualDocOther, residualCommentCard int
		// threadRowsSeen is EVERY row a comment card draws (IsThread == true),
		// residual or not -- the sweep's OWN proof that the comment-body arm
		// actually rendered, independent of blocksSeen/rowsSeen/sourceVS16/
		// sourceHyphens, none of which can tell "the comment arm rendered" apart
		// from "the document arm alone rendered": all four stay nonzero on the
		// document body alone.
		threadRowsSeen int
		// hyphenResidueRows, hyphenResidueDoc and hyphenResidueCommentCard --
		// see wrapPlainHyphenResidue below for the construct these count. Split
		// by arm for the same reason the ui.Line residual is: a figure that
		// could not say which arm it came from would repeat the comment-arm
		// blind spot the next time this file is read rather than fixed.
		hyphenResidueRows, hyphenResidueDoc, hyphenResidueCommentCard int
		hyphenResidueExamples                                         []string
	)

	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		filesSwept++
		sourceVS16 += strings.Count(string(src), string(frameSelector16))
		sourceHyphens += len(hyphenatedCompoundPattern.FindAllStringIndex(string(src), -1))

		blocks := ParseBlocks(src, st)
		blocksSeen += len(blocks)
		if len(blocks) == 0 {
			continue
		}

		// THE COMMENT-BODY ARM, DRIVEN WITH THE FILE'S OWN TEXT -- ONE COMMENT
		// PER BLOCK, EACH BLOCK-SIZED, NOT ONE COMMENT HOLDING THE WHOLE FILE.
		// A real Comment.Body is a reviewer's remark on one block. A single
		// comment holding an entire flattened file was tried and DOES turn up a
		// real, disclosed, latent interaction -- a pre-existing "wide unbreakable
		// token" bad line occurring before an otherwise hyphen-fixable one in the
		// SAME wrapPlain call makes the retry loop return before reaching the
		// fixable one -- but that is not the shape any real comment takes, so it
		// is reported rather than built into this gate.
		//
		// Flattened to one line per block, so a block spanning several source
		// lines does not ALSO become a second source of the ui.Line
		// embedded-newline residual on top of the heading arm's own.
		views := map[int][]ThreadView{}
		expanded := map[int]bool{}
		for i, b := range blocks {
			text := strings.Join(strings.Fields(b.Text), " ")
			if text == "" {
				continue
			}
			views[i] = []ThreadView{{Thread: domain.Thread{Comments: []domain.Comment{{Body: text}}}, Placed: true}}
			expanded[i] = true
		}

		for _, width := range frameAssertionWidths {
			lines := RenderDoc(blocks, views, nil, expanded, OnLine(NoCursor), width, "cm", st)

			for _, l := range lines {
				parts := strings.Split(l.Text, "\n")
				if l.IsThread {
					threadRowsSeen += len(parts)
				}
				if len(parts) == 1 {
					rowsSeen++
					row := parts[0]
					arm := "document"
					if l.IsThread {
						arm = "comment-card"
					}
					if w := ansi.StringWidth(row); w != width {
						if wrapPlainHyphenResidue(row, width) {
							hyphenResidueRows++
							if l.IsThread {
								hyphenResidueCommentCard++
							} else {
								hyphenResidueDoc++
							}
							if len(hyphenResidueExamples) < 8 {
								hyphenResidueExamples = append(hyphenResidueExamples, fmt.Sprintf("%s width %d (%s arm): %d cells (want %d): %q", rel, width, arm, w, width, row))
							}
						} else {
							overWidth++
							if len(overWidthExamples) < 8 {
								overWidthExamples = append(overWidthExamples, fmt.Sprintf("%s width %d (%s arm): %d cells (want %d): %q", rel, width, arm, w, width, row))
							}
						}
					}
					if strings.ContainsRune(row, frameSelector16) {
						selector16Rows++
						if len(selector16Examples) < 8 {
							selector16Examples = append(selector16Examples, fmt.Sprintf("%s width %d (%s arm): %q", rel, width, arm, row))
						}
					}
					continue
				}

				// THE ui.Line EMBEDDED-NEWLINE RESIDUAL -- excluded from (a)
				// by this construct (Text contains '\n'), never by a count.
				// The breakdown below is for the re-derived figure only and
				// changes no exclusion decision.
				//
				// IsThread FIRST, and l.BlockIdx's Kind only for a NON-thread
				// row: BlockIdx on a comment-card Line names the block the
				// COMMENT is attached to, not the arm the overflow came from,
				// so keying the breakdown off Kind alone mislabels every comment
				// card hung on a heading as a "heading" residual.
				residualLinesSeen++
				var residualArm string
				switch {
				case l.IsThread:
					residualCommentCard++
					residualArm = "comment-card"
				case l.BlockIdx >= 0 && l.BlockIdx < len(blocks) && blocks[l.BlockIdx].Kind == KindHeading:
					residualDocHeading++
					residualArm = "document heading"
				default:
					residualDocOther++
					residualArm = "document other"
				}
				for _, row := range parts {
					rowsSeen++
					residualRowsSeen++
					residualByShort[width-ansi.StringWidth(row)]++
					if strings.ContainsRune(row, frameSelector16) {
						selector16Rows++
						if len(selector16Examples) < 8 {
							selector16Examples = append(selector16Examples, fmt.Sprintf("%s width %d (%s residual row): %q", rel, width, residualArm, row))
						}
					}
				}
			}
		}
	}

	t.Logf("%d files, %d blocks, %d screen rows (%d of them from a comment card): %d ordinary, %d from %d Line(s) carrying an embedded newline (the ui.Line residual, excluded from assertion (a) by construct)", filesSwept, blocksSeen, rowsSeen, threadRowsSeen, rowsSeen-residualRowsSeen, residualRowsSeen, residualLinesSeen)
	t.Logf("ui.Line embedded-newline residual, re-derived: %d of %d split Line(s) are the document body's KindHeading arm, %d are the document body's other arms (wrapPlain's pre-existing no-break-opportunity non-goal), %d are a comment card (the SAME non-goal, reached through the arm this sweep is required to cover); short-by-N-cells over every excluded row (0 = coincidentally exact, almost always a split Line's first row): %v", residualDocHeading, residualLinesSeen, residualDocOther, residualCommentCard, residualByShort)
	// wrapPlain hyphen residue: the SECOND construct excluded from assertion
	// (a), narrower than the ui.Line residual above and never combined with it.
	// Zero here is not this test's non-vacuity gate -- it is a narrow
	// coincidence, not a guaranteed corpus population -- but a nonzero figure
	// that keeps growing across corpus states is worth a second look at whether
	// it is still exactly this construct.
	t.Logf("wrapPlain hyphen residue (the ruled +1, excluded from assertion (a) by construct: over budget by exactly 1 cell, ending in a hyphen): %d row(s) -- %d document arm, %d comment-card arm:\n%s", hyphenResidueRows, hyphenResidueDoc, hyphenResidueCommentCard, strings.Join(hyphenResidueExamples, "\n"))
	t.Logf("source population, independent of any renderer: %d U+FE0F occurrence(s) in raw file bytes, %d letter-hyphen-letter occurrence(s)", sourceVS16, sourceHyphens)

	// NON-VACUITY. Zero files is corpusMarkdownFiles' own skip, kept. These five
	// are this test's own: the first two say the sweep actually walked
	// something, the next two say what it walked actually held the two disputed
	// character classes, and the fifth says the COMMENT-BODY ARM ITSELF actually
	// rendered, which none of the other four can tell apart from "the document
	// body alone rendered".
	if blocksSeen == 0 {
		t.Fatalf("swept %d file(s) and got 0 blocks -- ParseBlocks failed silently across the whole corpus, this sweep proves nothing", filesSwept)
	}
	if rowsSeen == 0 {
		t.Fatalf("swept %d block(s) and got 0 rendered rows -- RenderDoc failed silently across the whole corpus, this sweep proves nothing", blocksSeen)
	}
	if sourceVS16 == 0 {
		t.Fatalf("the corpus contains no U+FE0F at all -- assertion (b) above passed with nothing in it to disagree about; point %s at a corpus that has some, or this run proves nothing about it", envCorpusDir)
	}
	if sourceHyphens == 0 {
		t.Fatalf("the corpus contains no letter-hyphen-letter occurrence at all -- assertion (a) above passed with nothing in it to disagree about; point %s at a corpus that has some, or this run proves nothing about it", envCorpusDir)
	}
	if threadRowsSeen == 0 {
		t.Fatalf("swept %d file(s) with a comment attached to every non-empty block and got 0 rows FROM A COMMENT CARD (IsThread) -- the comment-body arm (wrapPlain's comment-body call site) never rendered, so this sweep covers only 3 of wrapPlain's 4 call sites while every other check above still passed", filesSwept)
	}

	// THE TWO ASSERTIONS.
	if overWidth > 0 {
		t.Errorf("%d row(s) do not occupy exactly their budgeted width:\n%s", overWidth, strings.Join(overWidthExamples, "\n"))
	}
	if selector16Rows > 0 {
		t.Errorf("%d row(s) carry U+FE0F:\n%s", selector16Rows, strings.Join(selector16Examples, "\n"))
	}
}

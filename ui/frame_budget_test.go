package ui

// This file is the committed, UNGATED half of the frame assertion. See
// corpus_frame_dogfood_test.go for the gated half that sweeps the same two
// properties over a real corpus -- this half exists because a gated test
// never runs in `make check`, and the finding behind it is that nothing
// asserted this invariant at all.
//
// TWO ASSERTIONS, AND THE SECOND IS THE REAL DELIVERABLE:
//
//   (a) every rendered SCREEN ROW occupies EXACTLY the cells it was budgeted,
//       measured with ansi.StringWidth -- the same ruler wrapPlain measures its
//       own output with. That is the right ruler because the defect was our code
//       disagreeing with ITSELF: wrapPlain decided a row fit and
//       renderDocPainted's own Width() pad decided otherwise.
//
//   (b) no screen row contains U+FE0F. THIS REPLACES A "measure against a
//       terminal" REQUIREMENT AND IS STRONGER, NOT A CLIMBDOWN: four terminals
//       measured against a real VS16 cluster give four different width rules
//       (ui/vs16.go carries the table), so no WIDTH assertion can be right
//       against all four at once. An assertion that no disputed cluster reaches
//       the screen at all is right against all four simultaneously, needs no
//       PTY, and catches every future character of the class.
import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/domain"
)

// hyphenatedCompoundPattern is the non-vacuity proxy both this file and
// corpus_frame_dogfood_test.go use for "a hyphen inside a real word": a
// letter on both sides. NOT strings.Count(src, "-") -- a fixture that also
// carries a markdown table has "| --- | --- |" in it, which alone is 6
// hyphens with no letter beside any of them, so a raw count would keep
// passing even if a mutation emptied frameHyphenPhrase down to nothing.
// Caught exactly that way while building this file: strings.Count(src, "-")
// stayed >= 5 with frameHyphenPhrase replaced by a hyphen-free sentence,
// because the table delimiter alone cleared the threshold.
var hyphenatedCompoundPattern = regexp.MustCompile(`\p{L}-\p{L}`)

// frameSelector16 is THIS ASSERTION's OWN name for U+FE0F, deliberately not a
// reference to ui/vs16.go's own vs16 constant. The assertion has to define what
// it is looking for independently of the fix it is checking, or reverting that
// fix (which deletes vs16.go) breaks this file's own BUILD instead of making
// assertion (b) go red.
const frameSelector16 = '\uFE0F'

// frameAssertionWidths is the five widths this file and
// corpus_frame_dogfood_test.go both sweep, so a figure in either's logs is
// comparable to the other's.
var frameAssertionWidths = []int{80, 100, 120, 160, 200}

// frameRowSample is RenderDoc's []Line flattened into the SCREEN ROWS
// app/painted.go actually writes into the frame it hands bubbletea: one "\n"
// per Line -- so a Line whose Text ALREADY carries one produces MORE than one
// screen row from a single Line.
//
// THOSE EXTRA ROWS ARE THE ui.Line EMBEDDED-NEWLINE RESIDUAL, documented on
// Line's own doc comment (ui/render.go): a heading whose own words overflow the
// row it is painted into is not wrapped by wrapPlain at all (KindHeading draws
// Block.Text raw), so lipgloss's Width() pad wraps it FOR the arm instead, and
// the second and later physical rows of that wrap never receive the gutter
// prefix.
//
// EXCLUDED BY THE CONSTRUCT, NOT BY A COUNT: a Line's rows are excluded from
// assertion (a) because its Text CONTAINS '\n', never because a particular row
// happens to measure short. That is why residual holds every row of a split
// Line, including whichever one still happens to land on budget by
// coincidence.
type frameRowSample struct {
	rows          []string // one entry per screen row from a Line that did NOT split
	residual      []string // every screen row from a Line that DID split
	residualLines int      // how many Lines split (<= len(residual): one Line can split into more than two rows)
}

func sampleFrameRows(lines []Line) frameRowSample {
	var s frameRowSample
	for _, l := range lines {
		parts := strings.Split(l.Text, "\n")
		if len(parts) == 1 {
			s.rows = append(s.rows, l.Text)
			continue
		}
		s.residualLines++
		s.residual = append(s.residual, parts...)
	}
	return s
}

// allRows is every screen row a sample carries, ordinary and residual alike
// -- what assertion (b) is checked over, since stripSelector16 runs at the
// draw arm whether or not that arm's Line happens to wrap cleanly.
func (s frameRowSample) allRows() []string {
	out := make([]string, 0, len(s.rows)+len(s.residual))
	out = append(out, s.rows...)
	out = append(out, s.residual...)
	return out
}

// requireFrameRowsFillTheirBudget is assertion (a), applied to the
// NON-residual rows of one sample.
func requireFrameRowsFillTheirBudget(t *testing.T, sample frameRowSample, width int, context string) {
	t.Helper()
	if len(sample.rows) == 0 {
		t.Fatalf("%s: no non-residual row rendered at width %d -- this case tests nothing", context, width)
	}
	for i, row := range sample.rows {
		if w := ansi.StringWidth(row); w != width {
			t.Errorf("%s at width %d: row %d is %d cells, want exactly %d: %q", context, width, i, w, width, row)
		}
	}
}

// requireNoRowCarriesSelector16 is assertion (b), applied to every row a
// sample carries (see allRows).
func requireNoRowCarriesSelector16(t *testing.T, rows []string, width int, context string) {
	t.Helper()
	if len(rows) == 0 {
		t.Fatalf("%s: no row to check at width %d -- this case tests nothing", context, width)
	}
	for i, row := range rows {
		if strings.ContainsRune(row, frameSelector16) {
			t.Errorf("%s at width %d: row %d carries U+FE0F: %q", context, width, i, row)
		}
	}
}

// frameHyphenPhrase is a sentence carrying several hyphenated compounds of the
// shape the defect's whole population took: a letter on each side of the
// hyphen, inside prose rather than a contrived worst case. Several rather than
// one, so that across frameAssertionWidths at least one of them plausibly falls
// on a wrap boundary regardless of exactly where the surrounding words happen
// to break.
const frameHyphenPhrase = "the telemetry-26-sensor-feed fix keeps a filesystem-path-like acme-platform/docs/plans/q3-rollout.md whole instead of splitting it at a hyphen, the shape Step 3's own before-and-after comparison found and Step 2's state-of-the-art retry loop was built to close"

// frameSelectorPhrase carries two explicit base+VS16 sequences -- U+26A0
// (warning sign) and U+2705 (check mark), each immediately followed by
// U+FE0F -- the same explicit-presentation shape ui/vs16.go's own doc
// comment measures against all four terminals. Two different base
// characters, because stripSelector16 is a rule about the SELECTOR and not a
// table keyed to one base (see that file), and a fixture asserting the rule
// is base-agnostic ought to carry more than one base.
const frameSelectorPhrase = "reviewers should read the warning ⚠️ before approving ✅️ this change"

// frameAssertionFixture is the document driven through RenderDoc's document
// body: a heading long enough to overflow every width in
// frameAssertionWidths (the residual, demonstrated below rather than
// assumed), a paragraph and a list item each carrying frameHyphenPhrase and
// frameSelectorPhrase, and a small table whose cell carries the same hyphen
// phrase so the grid path is swept too.
func frameAssertionFixture() string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(strings.Repeat("h", 400))
	b.WriteString("\n\n")
	b.WriteString("A paragraph carrying a real compound: ")
	b.WriteString(frameHyphenPhrase)
	b.WriteString(". ")
	b.WriteString(frameSelectorPhrase)
	b.WriteString(".\n\n")
	b.WriteString("- a list item carrying its own compound: ")
	b.WriteString(frameHyphenPhrase)
	b.WriteString(". ")
	b.WriteString(frameSelectorPhrase)
	b.WriteString("\n\n")
	b.WriteString("| path | note |\n| --- | --- |\n| ")
	b.WriteString(frameHyphenPhrase)
	b.WriteString(" | ")
	b.WriteString(frameSelectorPhrase)
	b.WriteString(" |\n")
	return b.String()
}

// TestDocumentFrameRowsFillTheirBudgetAndCarryNoSelector16 is assertions (a)
// and (b) over the document body -- the heading, paragraph, list-item and
// table-cell arms of renderBlockPainted (its KindHeading, default,
// KindListItem and KindTableRow cases; the grid path for the table).
func TestDocumentFrameRowsFillTheirBudgetAndCarryNoSelector16(t *testing.T) {
	st := darkStyles(t)
	src := frameAssertionFixture()

	// THE FIXTURE GUARD. An assertion is worth nothing against a fixture
	// that does not carry what it claims to.
	if !strings.ContainsRune(src, frameSelector16) {
		t.Fatalf("the fixture carries no U+FE0F at all -- assertion (b) below would pass with nothing to disagree about: %q", src)
	}
	if n := len(hyphenatedCompoundPattern.FindAllStringIndex(src, -1)); n < 5 {
		t.Fatalf("the fixture carries only %d letter-hyphen-letter occurrence(s) -- not enough to plausibly exercise the hyphen-carry retry loop: %q", n, src)
	}

	for _, width := range frameAssertionWidths {
		t.Run(fmt.Sprintf("width %d", width), func(t *testing.T) {
			lines := RenderDoc(ParseBlocks([]byte(src), st), nil, nil, nil, OnLine(NoCursor), width, "cm", st)
			sample := sampleFrameRows(lines)

			// THE RESIDUAL, DEMONSTRATED RATHER THAN ASSUMED: the fixture's
			// heading is long enough to overflow at every width this loop
			// tries. A run that stops finding it is a run that stopped
			// exercising the exclusion assertion (a) below relies on.
			if sample.residualLines == 0 {
				t.Fatalf("no Line split at width %d -- the fixture's heading no longer overflows, so the ui.Line residual this test excludes is not exercised here and assertion (a) below is not the assertion this file claims to make", width)
			}
			t.Logf("width %d: %d ordinary row(s), %d residual row(s) from %d split Line(s)", width, len(sample.rows), len(sample.residual), sample.residualLines)

			requireFrameRowsFillTheirBudget(t, sample, width, "document body")
			requireNoRowCarriesSelector16(t, sample.allRows(), width, "document body")
		})
	}
}

// frameCommentAssertionWidths is frameAssertionWidths PLUS two widths where
// this file's exact comment fixture text lands a wrap boundary on a hyphen.
// Under the mutation that reverts the fix, the comment-body arm produces an
// off-budget row at 24 of the 181 widths in 40..220 for this fixture, and NONE
// of frameAssertionWidths is one of them -- so assertion (a) on this arm was
// unproven in `make check`. 82 and 118 are two of the 24, chosen from different
// parts of the range for redundancy against one boundary shifting if
// frameHyphenPhrase or frameSelectorPhrase is ever edited.
var frameCommentAssertionWidths = append(append([]int{}, frameAssertionWidths...), 82, 118)

// TestCommentBodyFrameRowsFillTheirBudgetAndCarryNoSelector16 is assertions
// (a) and (b) over the ONE arm a prior corpus measurement missed entirely:
// wrapPlain's comment-body call site, reachable only when RenderDoc's views AND
// expanded are BOTH populated. A sweep that passes nil for those covers 3 of 4
// wrapPlain call sites while looking complete.
func TestCommentBodyFrameRowsFillTheirBudgetAndCarryNoSelector16(t *testing.T) {
	st := darkStyles(t)
	blocks := ParseBlocks([]byte("# Heading\n\nAn ordinary paragraph.\n"), st)

	body := "a reviewer's comment carrying " + frameHyphenPhrase + ". " + frameSelectorPhrase + "."
	if !strings.ContainsRune(body, frameSelector16) {
		t.Fatalf("the comment fixture carries no U+FE0F: %q", body)
	}
	if n := len(hyphenatedCompoundPattern.FindAllStringIndex(body, -1)); n < 5 {
		t.Fatalf("the comment fixture carries only %d letter-hyphen-letter occurrence(s): %q", n, body)
	}

	v := ThreadView{Thread: domain.Thread{Comments: []domain.Comment{{Body: body}}}, Placed: true}
	views := map[int][]ThreadView{0: {v}}
	expanded := map[int]bool{0: true}

	for _, width := range frameCommentAssertionWidths {
		t.Run(fmt.Sprintf("width %d", width), func(t *testing.T) {
			lines := RenderDoc(blocks, views, nil, expanded, OnLine(0), width, "cm", st)
			sample := sampleFrameRows(lines)

			// THE ARM GUARD: the comment must actually have reached the
			// screen, or the two checks below say nothing about
			// wrapPlain's comment-body call site at all.
			reached := false
			for _, row := range sample.allRows() {
				if strings.Contains(ansi.Strip(row), "sensor-feed") {
					reached = true
					break
				}
			}
			if !reached {
				t.Fatalf("no rendered row at width %d carries the comment body -- views/expanded did not reach wrapPlain's comment-body call site, so this test exercises nothing", width)
			}

			requireFrameRowsFillTheirBudget(t, sample, width, "comment body")
			requireNoRowCarriesSelector16(t, sample.allRows(), width, "comment body")
		})
	}
}

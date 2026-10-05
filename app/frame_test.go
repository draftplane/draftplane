package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/session"
	"github.com/draftplane/draftplane/theme"
	"github.com/draftplane/draftplane/ui"
)

// This file drives the frame invariant over the two frames that live in this
// package: the review view and the plan list. The invariant itself is
// ui.FrameViolations -- one definition, shared with ui's own assertions and
// with the hostile-document harness -- and everything here is the drive.
//
// BOTH VIEWS AND NOT ONE, which is not a formality. The plan list never touches
// ui.Line: nothing it draws goes through ParseBlocks, renderInlines or
// RenderDoc, so a frame invariant proved on the document view says nothing at
// all about it.

// requireCleanFrame is the t.Fatalf wrapper and NOT a second definition of the
// rule: what a frame may carry is ui.FrameViolations' business, in ui, once.
// The CSI guard runs first because "no OSC and no stray C0" is true of the
// empty string, so a frame that had quietly stopped rendering would otherwise
// keep passing.
func requireCleanFrame(t *testing.T, what, frame string) {
	t.Helper()
	if csi := ui.FrameCSICount(frame); csi == 0 {
		t.Fatalf("%s carries no CSI sequence at all, so it is not a styled frame and this assertion proves nothing: %q", what, frame)
	}
	if v := ui.FrameViolations(frame); len(v) != 0 {
		t.Fatalf("%s carries %d thing(s) a benign frame must not:\n  %v", what, len(v), v)
	}
}

// frameHostilePayloads is what a document can put in a string this package
// draws, and it is DELIBERATELY WIDER THAN reviewControlBytes and
// listControlBytes. Those two carry \b, \r and \x7f -- the forgeries that need no
// escape sequence at all. This one adds the five string-terminated introducers,
// a different hazard with the same predicate behind them: each swallows every
// byte after it until a terminator the payload chooses, so an unterminated one
// eats the rest of the screen. The same filter covers them, ESC being one of
// the C0 bytes it visualises.
var frameHostilePayloads = []struct{ name, payload string }{
	{"the backspace forgery", "Requires approval\bNo approval needed"},
	{"the carriage-return forgery", "We will NOT rotate the keys.\rWe will rotate them"},
	{"DEL", "a\x7fb"},
	{"an OSC 8 hyperlink", "before\x1b]8;;https://evil.example\x07label\x1b]8;;\x07after"},
	{"an unterminated APC", "before\x1b_payload that never endsafter"},
	{"a DCS", "before\x1bPq#0;2;0;0;0\x1b\\after"},
	{"an SOS, the sibling the invariant does not name", "before\x1bXpayload\x1b\\after"},
	{"a PM, the other one", "before\x1b^payload\x1b\\after"},
}

// Pages the whole fidelity fixture through a real 100x30 review view and
// asserts the invariant on EVERY frame, not on the first.
//
// PAGING IS THE POINT: the fixture is longer than one screen, so a construct
// that only exists below the fold is one a single-frame assertion never looks
// at. The compose panel and a status message are driven too, because both
// compose a row this frame's own budgets have to fit and neither is on screen
// in the browse state.
func TestABenignReviewFrameHoldsNoControlSequence(t *testing.T) {
	_, m := openReviewOnDoc(t, string(fidelitySource(t)))
	frames, rows, csi := 0, 0, 0
	for {
		frames++
		content := m.View().Content
		rows += strings.Count(content, "\n") + 1
		csi += ui.FrameCSICount(content)
		requireCleanFrame(t, "the review frame", content)
		before := m.scroll
		m = press(m, "pgdown")
		if m.scroll == before {
			break
		}
		if frames > len(m.lines) {
			t.Fatalf("paging did not terminate after %d frames", frames)
		}
	}
	// A CSI count per frame is a function of how many rows the frame has and how
	// much of each is styled, so a figure quoted without its fixture is one
	// nobody can reproduce. Logged, never asserted.
	t.Logf("app.Model.View at %dx%d over the fidelity fixture: %d frames, %d rows, %d CSI, %d per frame, %d per row",
		m.width, m.height, frames, rows, csi, csi/frames, csi/rows)

	// EACH STATE SAYS WHICH MODE IT MEANT TO REACH, because requireCleanFrame
	// is happy with any styled frame at all: a gesture that stopped opening its
	// panel would leave these subtests passing over the browse screen and
	// asserting nothing about the rows they were added for.
	for _, tc := range []struct {
		name  string
		drive func(*Model) *Model
		want  mode
	}{
		{"the compose panel open", func(m *Model) *Model {
			m.cursor = 1
			m.rerender()
			return press(m, "c")
		}, modeCompose},
		{"the approve confirm open", func(m *Model) *Model {
			return press(m, "a")
		}, modeConfirmApprove},
		{"a status message on the bar", func(m *Model) *Model {
			m.status = "error: could not read the plan file " + strings.Repeat("x", 60)
			m.rerender()
			return m
		}, modeRead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, m := openReviewOnDoc(t, string(fidelitySource(t)))
			m = tc.drive(m)
			if m.mode != tc.want {
				t.Fatalf("mode = %v, want %v -- the gesture did not reach the state this case is about; status=%q", m.mode, tc.want, m.status)
			}
			requireCleanFrame(t, "the review frame with "+tc.name, m.View().Content)
		})
	}
}

// Plants each payload in the review frame's own supplied strings and requires
// the frame to come out clean.
//
// THE DOCUMENT'S H1 IS THREE CARRIERS AT ONCE and that is why it is the fixture
// here rather than three. With the compose panel open over a block beneath it,
// one hostile heading reaches the status bar through ui.InferTitle, the
// document body through renderBlockPainted's heading arm, and the panel header
// through Block.HeadingPath. A frame assertion covers all three without
// enumerating them, which is the whole argument for asserting a frame rather
// than a field.
//
// A FILE PATH'S OWN BYTES ARE WHAT THIS DRIVES, and not an assignment to
// m.confirm: Panel 1 puts the path of a missing file on screen verbatim, and a
// path may hold any byte but NUL and '/'. Driving the panel's own writer rather
// than the field is what makes this a test of the CHANNEL -- a fix that filtered
// at some writer and not at the draw would still pass a field assignment.
//
// The panel is drawn through Model.confirmLines (app/actions.go), the SIBLING
// of ListModel.confirmLines and the one of the pair that was left raw. The fix
// is the one line the list already spells: ui.VisibleControls before
// ansi.Wordwrap, so the substitution lands BEFORE the budget rather than after
// it.
func TestAHostileReviewFrameHoldsNoControlSequence(t *testing.T) {
	for _, p := range frameHostilePayloads {
		t.Run(p.name+"/a hostile H1, with the compose panel over it", func(t *testing.T) {
			_, m := openReviewOnDoc(t, "# "+p.payload+"\n\nbody text\n")
			if got := ui.InferTitle(m.blocks, m.sess.Path); !strings.Contains(got, p.payload) {
				t.Fatalf("ui.InferTitle answered %q, which does not carry the payload -- it must still answer Block.Text raw, and this fixture is not driving the carrier it claims", got)
			}
			m.cursor = 1
			m.rerender()
			m = press(m, "c")
			if m.mode != modeCompose {
				t.Fatalf("mode = %v, want modeCompose -- without the panel open this asserts nothing about HeadingPath", m.mode)
			}
			requireCleanFrame(t, "the review frame", m.View().Content)
		})
		t.Run(p.name+"/Panel 1, over a missing file's own path", func(t *testing.T) {
			m := faultedModel(t, &session.SourceFileError{State: session.SourceFileGone, Path: "/plans/" + p.payload + ".md"})
			if m.mode != modeSourceFault {
				t.Fatalf("mode = %v, want modeSourceFault -- without the panel open this asserts nothing about m.confirm; status=%q", m.mode, m.status)
			}
			if !strings.Contains(m.confirm, p.payload) {
				t.Fatalf("m.confirm = %q, which does not carry the payload -- the panel must still take the path verbatim, and this fixture is not driving the carrier it claims", m.confirm)
			}
			requireCleanFrame(t, "the review frame with Panel 1 open", m.View().Content)
		})
		t.Run(p.name+"/a status message", func(t *testing.T) {
			_, m := openReviewOnDoc(t, "# Benign heading\n\nbody text\n")
			m.approved = true
			m.status = "error: " + p.payload + " " + strings.Repeat("x", 40)
			m.rerender()
			requireCleanFrame(t, "the review frame", m.View().Content)
		})
	}
}

// The OTHER HALF of the confirm panel's fix, and a different claim from the
// frame assertion above rather than a restatement of it. A frame that carries
// no raw control byte can still be the WRONG SHAPE: the substitution has to
// land BEFORE ansi.Wordwrap measures, not after it, or the panel is budgeted in
// one width authority and painted in another.
//
// THE TWO AUTHORITIES, which is the mechanism and not a caveat. A bare C0 byte
// is ZERO cells to ansi.StringWidth -- what Wordwrap, Hardwrap and
// confirmLines' own budget measure with -- and ONE to the
// st.FocusHeader.Width(m.width).Render that paints the row. So a line the wrap
// believed fitted comes back over-width, lipgloss breaks it a second time on
// its own, and panelViewPainted draws more rows than confirmPanelHeight
// reserved. A Control Picture is one cell to both authorities, so substituting
// first settles the disagreement instead of working around it.
//
// MUTATION: ui.VisibleControls moved behind BOTH ansi.Wordwrap AND
// ansi.Hardwrap -- a pass over the finished lines, same glyphs, same bytes on
// screen, same clean frame -- leaves every other test in this package GREEN and
// reddens this one, at both sizes, on the first of its three assertions; the
// panel-row and screen-height assertions behind it are the consequence.
//
// ⚠️ BEHIND THE WORD WRAP ALONE IS NOT ENOUGH TO REDDEN IT, and saying so is
// the point of naming both. Moved to sit BETWEEN ansi.Wordwrap and
// ansi.Hardwrap this test is green -- Hardwrap re-enforces the budget on the
// line the substitution just made over-wide, and truncateConfirmLines clips the
// surplus row away.
//
// The payload is a single unbreakable token exactly the budget wide with four
// control bytes buried in it, because Wordwrap breaks only on a space or a "-"
// and a token with either would be split for reasons that have nothing to do
// with the filter.
func TestAConfirmPanelFiltersBeforeItBudgets(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
	}{
		{"80x24", 80, 24},
		{"100x30", 100, 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, m := openReviewOnDoc(t, "# Benign heading\n\nbody text\n")
			m.width, m.height = tc.width, tc.height
			m.rerender()
			budget := max(m.width-4, 10)
			half := budget / 2
			m.mode = modeConfirmDeleteThread
			m.confirm = strings.Repeat("x", half) + "\b\x7f\x01\x02" + strings.Repeat("x", budget-half)
			m.rerender()
			for i, l := range m.confirmLines() {
				if w := ansi.StringWidth(l); w > budget {
					t.Fatalf("confirmLines()[%d] = %d display cells, want <= %d (width-4) -- the glyph is a cell the budget never counted: %q", i, w, budget, l)
				}
			}
			panel, _ := m.panelViewPainted()
			if got, want := panelRowsDrawn(panel), m.confirmPanelHeight(); got != want {
				t.Fatalf("panelViewPainted() draws %d rows, want confirmPanelHeight() = %d -- the render disagrees with the reservation", got, want)
			}
			if got := len(strings.Split(m.View().Content, "\n")); got != m.height {
				t.Fatalf("the screen is %d rows against a terminal of %d -- the panel overflows it", got, m.height)
			}
		})
	}
}

// The OTHER MEMBER OF THE MATCHED PAIR. Model.confirmLines (app/actions.go) is
// a WHOLE PORT of ListModel.confirmLines (app/list.go) -- the two are
// line-for-line the same, the control-byte filter in front of ansi.Wordwrap
// included. A
// pair with a test on one member only is how the two drift back apart, so this
// is the same three assertions asked of the other one.
//
// WHY A FRAME ASSERTION CANNOT MAKE THIS CLAIM. Moving ui.VisibleControls from
// in front of the wrap to a pass over the finished lines leaves every byte on
// screen correct, every glyph right and ui.FrameViolations at zero -- and the
// panel the wrong shape, because the substitution then lands AFTER the two width
// authorities have already disagreed. A bare C0 byte is zero cells to
// ansi.Wordwrap/Hardwrap and one to the Width().Render that paints the row, so a
// line the wrap believed fitted comes back over-width, lipgloss breaks it a
// second time on its own, and more rows are painted than were reserved. The row
// that goes off the bottom is the help bar -- the one naming the keys that
// answer a question about deleting a plan and its comment threads.
//
// THE PAYLOAD IS FOUR NON-WHITESPACE CONTROL BYTES IN ONE UNBREAKABLE TOKEN,
// and every word of that is measured. Unbreakable, because ansi.Wordwrap breaks
// only on a space or a "-" and a token with either would be split for reasons
// that have nothing to do with the filter. Non-whitespace, because a CARRIAGE
// RETURN IS USELESS HERE: ansi.Hardwrap breaks the line at the \r, so a fixture
// built on one measures the break and never the width disagreement.
//
// MUTATIONS, both at app/list.go's confirmLines and nothing else touched:
//
//   - ui.VisibleControls moved BEHIND BOTH ansi.Wordwrap AND ansi.Hardwrap, i.e.
//     a pass over the finished lines -- the width assertion reddens. ⚠️ BEHIND
//     THE WORD WRAP ALONE LEAVES IT GREEN, on both members of the pair: Hardwrap
//     repairs the over-wide line and truncateConfirmLines clips the extra row,
//     so a mutation stopped there proves nothing about this placement either
//     way;
//   - ui.VisibleControls removed -- the width assertion stays GREEN (a raw byte
//     measures zero, so the line still looks as though it fits) and the two
//     behind it redden. THE TWO MUTATIONS FAIL AT DIFFERENT ASSERTIONS, which is
//     why all three are here rather than whichever one happened to be red
//     first.
func TestTheListConfirmPanelFiltersBeforeItBudgets(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name          string
		width, height int
	}{
		{"80x24", 80, 24},
		{"100x30", 100, 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// centredInterior(tc.width) AND NOT tc.width-4: this panel is drawn
			// in the centred box, which spends three cells of frame a side
			// rather than the strip's two.
			budget := centredInterior(tc.width)
			half := budget / 2
			confirm := strings.Repeat("x", half) + "\b\x7f\x01\x02" + strings.Repeat("x", budget-half)

			// THE PANEL'S OWN ARITHMETIC FIRST, on a model with styles at
			// construction: confirmLines() reads them, and a model without them
			// is not a model this method can be asked about.
			m := &ListModel{width: tc.width, height: tc.height, mode: listConfirmDelete, confirm: confirm, styles: ui.NewStyles(th)}
			lines := m.confirmLines()
			if len(lines) == 0 {
				t.Fatal("confirmLines() returned nothing, so the three assertions below have no subject")
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w > budget {
					t.Fatalf("confirmLines()[%d] = %d display cells, want <= %d (centredInterior) -- the glyph is a cell the budget never counted: %q", i, w, budget, l)
				}
			}
			// panelViewPainted DRAWS NOTHING for this mode (drawsCentredPanel),
			// so the cross-check is against the box instead: centredBox's own
			// row count must be exactly the rows it was handed plus its four
			// border/blank rows, or its render disagrees with what confirmGroups
			// fed it.
			groups := m.centredGroups()
			boxRows := strings.Split(centredBox(groups, m.styles), "\n")
			if got, want := len(boxRows), panelGroupRows(groups)+4; got != want {
				t.Fatalf("centredBox draws %d rows, want %d (confirmGroups' own rows plus the box's four border/blank rows) -- the render disagrees with what it was handed", got, want)
			}

			// AND THEN THE WHOLE SCREEN, through a real model with rows in it,
			// because the reservation agreeing with the render is not the same
			// claim as the screen fitting the terminal.
			full := onePlanList(th, nil, domain.Plan{ID: "l_a", Title: "Alpha", SourceHint: "~/plans/a.md"}, listBrowse)
			full.width, full.height = tc.width, tc.height
			full.confirm = confirm
			full.setMode(listConfirmDelete)
			content := full.View().Content
			rows := strings.Split(content, "\n")
			// THE HINT IS READ AT ROW height-1 AND NOT AT THE END OF THE
			// STRING, and that is measured rather than stylistic: an
			// overflowing panel does not replace the help bar, it pushes it one
			// row past what the terminal can show, so the string still ENDS
			// with the hint and an assertion reading rows[len-1] passes over
			// exactly the screen it exists to catch. Neither mutation above
			// reddens THIS line -- the panel-row assertion fires first -- so its
			// own mutation is a layout one: drawing this frame's help bar empty
			// fails it with the last visible row blank.
			if len(rows) < tc.height {
				t.Fatalf("the screen is %d rows against a terminal of %d -- it does not fill the screen at all:\n%s", len(rows), tc.height, ansi.Strip(content))
			}
			if last := ansi.Strip(rows[tc.height-1]); !strings.Contains(last, confirmDeleteHint) {
				t.Fatalf("the terminal's last visible row is %q and does not carry %q -- the bar naming the keys that answer this panel is off the bottom of the screen", last, confirmDeleteHint)
			}
			if len(rows) != tc.height {
				t.Fatalf("the screen is %d rows against a terminal of %d -- the panel overflows it:\n%s", len(rows), tc.height, ansi.Strip(content))
			}
			requireCleanFrame(t, "the list frame with the delete confirm open over a hostile payload", content)
		})
	}
}

// Whether a panel body can carry a variation-selector-16 sequence, driven
// rather than argued: sourceFaultText's four arms all interpolate a FILESYSTEM
// PATH verbatim, glued to the panel's own ">  " marker -- and a path is foreign
// text with no character restriction beyond NUL and "/", so it can carry
// U+FE0F exactly as a document's own prose can.
//
// The strip reaches this panel because wrapConfirmGroups already calls
// ui.VisibleControls (the control-byte choke point) on every logical line
// before either wrap pass runs, and ui.VisibleControls strips U+FE0F
// unconditionally. So this
// test calls wrapConfirmGroups directly, the same call every real composer's
// body reaches, rather than adding anything to it.
func TestPanelPathRowStripsSelector16(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := ui.NewStyles(th)
	const pathWithSelector = "/Users/reviewer/plans/warning⚠️sign.md"
	const bareBase = "warning⚠sign"
	body := "This plan's original file is gone or has moved:\n\n>  " + pathWithSelector + "\n\nDraftplane still has a cache of the most recent version."
	for _, width := range []int{40, 80, 120} {
		t.Run(fmt.Sprintf("width %d", width), func(t *testing.T) {
			groups := wrapConfirmGroups(body, width, st.FocusHeader)
			flat := ansi.Strip(strings.Join(flattenConfirmGroups(groups), "\n"))
			if strings.Contains(flat, "️") {
				t.Fatalf("U+FE0F reached a panel row: %q -- a path can carry the selector exactly as a document can, and this panel is pinned row-by-row at a fixed width the same way a document row is", flat)
			}
			if !strings.Contains(flat, bareBase) {
				t.Fatalf("the base character is gone from the panel along with the selector: %q -- stripping the selector must not take the character it modifies with it", flat)
			}
		})
	}
}

// panelRowsDrawn counts the rows a panel render actually draws. Every panel
// case in panelViewPainted ends its last row with a "\n", so the trailing
// empty field is not a row.
func panelRowsDrawn(out string) int {
	return len(strings.Split(strings.TrimSuffix(out, "\n"), "\n"))
}

// The second frame, in the three states that draw different rows: the browse
// list, the rename panel over it and the delete confirm.
func TestABenignListFrameHoldsNoControlSequence(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	plan := domain.Plan{ID: "l_a", Title: "Alpha", SourceHint: "~/plans/a.md"}
	for _, tc := range []struct {
		name  string
		build func() *ListModel
	}{
		{"browsing", func() *ListModel { return onePlanList(th, nil, plan, listBrowse) }},
		{"the rename panel", func() *ListModel {
			m := onePlanList(th, nil, plan, listBrowse)
			m.enterRename()
			return m
		}},
		{"the delete confirm", func() *ListModel {
			m := onePlanList(th, nil, plan, listBrowse)
			m.confirm = ordinaryDeleteConfirmText(plan)
			m.setMode(listConfirmDelete)
			return m
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := tc.build().View().Content
			rows, csi := strings.Count(content, "\n")+1, ui.FrameCSICount(content)
			t.Logf("app.ListModel.View at 100x30, %s: %d rows, %d CSI, %d per row", tc.name, rows, csi, csi/rows)
			requireCleanFrame(t, "the list frame while "+tc.name, content)
		})
	}
}

// The half of "one defect, two frames" that the ui.VisibleControls doc comment
// claimed and the code did not carry.
//
// A FRAME INVARIANT CANNOT MAKE THIS CLAIM, which is why this is a row count
// and not a requireCleanFrame call. A tab is not a violation -- ui/control.go's
// predicate excludes it on purpose -- and the defect here is SHAPE: ansi.StringWidth
// counts a tab as ZERO cells (what every truncation and pad in this package
// measures with) and lipgloss's Width().Render paints it as FOUR, so a row
// budgeted to exactly its width comes back as two and the frame is a row taller
// than the terminal. Four times the overshoot of the bare control byte the
// invariant above already closes.
//
// AND A TAB IN AN H1 IS NOT EVEN HOSTILE, which is what makes the review half
// worth its own case: ui.InferTitle answers Block.Text raw, so an ordinary plan
// whose title holds a tab pushed the review frame past the terminal.
//
// THE REVIEW CASE DRIVES THE H1 AND CARRIES ITS BENIGN TWIN, because an
// unrelated overflow lives in the same row. renderBlockPainted's heading arm
// neither wraps nor truncates, so a title long enough to fill the status bar
// eventually overflows the DOCUMENT row on its own -- benign or not -- and an
// unguarded assertion here would be measuring that instead. The twin is the
// guard: the title is
// sized to fill the bar and to leave the heading row inside its own budget, and
// the benign twin is required to draw exactly the terminal's rows before the
// hostile one is asked anything.
//
// AND m.status IS DELIBERATELY NOT THE PAYLOAD. It reaches this bar through
// ui.RenderControls, which renders every run it emits -- and lipgloss's Render
// substitutes the same four spaces before ansi.Truncate ever measures the row,
// so there is no disagreement there to close.
//
// MUTATION, at ui/control.go and nothing else: fourSpaceTabs taken back out of
// VisibleControls overflows every list row by one and the review rows with it.
func TestBothFramesSpendATabBeforeTheyBudget(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("the plan list", func(t *testing.T) {
		for _, f := range listDrawnFields {
			t.Run(f.name, func(t *testing.T) {
				m := f.build(t, th, f.payload("\t"))
				content := m.View().Content
				plain := ansi.Strip(content)
				if strings.Contains(plain, "\t") {
					t.Fatalf("a tab reached the screen through %s -- it is zero cells to every budget in this package and four to the renderer that paints it: %q", f.name, plain)
				}
				if rows := strings.Count(content, "\n") + 1; rows != m.height {
					t.Fatalf("the frame is %d rows against a %d-row terminal with one tab in %s -- the row was budgeted by ansi.StringWidth, which counts a tab as 0 cells, and painted by lipgloss, which spends 4:\n%s",
						rows, m.height, f.name, plain)
				}
			})
		}
	})
	t.Run("the review status bar, over an H1 with a tab in it", func(t *testing.T) {
		for _, tc := range []struct{ width, height int }{{60, 24}, {80, 24}, {100, 30}} {
			t.Run(fmt.Sprintf("%dx%d", tc.width, tc.height), func(t *testing.T) {
				// Long enough to fill the bar -- the only row shape the two
				// authorities can disagree about, since a title with room to
				// spare is padded either way -- and short enough to leave the
				// heading row inside its own budget.
				n := tc.width - 2*ui.RailWidth(tc.width) - 15
				draw := func(middle string) string {
					_, m := openReviewOnDoc(t, "# "+strings.Repeat("a", n/2)+middle+strings.Repeat("b", n-n/2)+"\n\nbody text\n")
					m.width, m.height = tc.width, tc.height
					m.rerender()
					return m.View().Content
				}
				if rows := strings.Count(draw(" "), "\n") + 1; rows != tc.height {
					t.Fatalf("the BENIGN twin is already %d rows against a %d-row terminal -- this title is long enough to overflow the unwrapped heading arm (an unrelated overflow), so the case below would measure that and not the tab", rows, tc.height)
				}
				content := draw("\t")
				plain := ansi.Strip(content)
				if strings.Contains(plain, "\t") {
					t.Fatalf("a tab reached the review status bar: %q", plain)
				}
				if rows := strings.Count(content, "\n") + 1; rows != tc.height {
					t.Fatalf("the review frame is %d rows against a %d-row terminal where its benign twin is %d -- one tab in an ordinary H1, through ui.InferTitle, which answers Block.Text raw:\n%s", rows, tc.height, tc.height, plain)
				}
			})
		}
	})
}

// Crosses every payload with every string the plan list draws.
//
// listDrawnFields IS REUSED RATHER THAN RESTATED, and that matters more than the
// line count: those payloads are each sized to their OWN column -- the origin
// clips from the left and everything else from the right -- so a table written
// fresh here would quietly drop its payload in half the columns and pass
// vacuously.
func TestAHostileListFrameHoldsNoControlSequence(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range frameHostilePayloads {
		for _, f := range listDrawnFields {
			t.Run(p.name+"/"+f.name, func(t *testing.T) {
				payload := f.payload(p.payload)
				if !strings.Contains(payload, p.payload) {
					t.Fatalf("the field's own payload builder dropped the byte: %q", payload)
				}
				requireCleanFrame(t, "the list frame with a payload in "+f.name, f.build(t, th, payload).View().Content)
			})
		}
	}
}

package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/client/recent"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/keymap"
	"github.com/draftplane/draftplane/session"
	"github.com/draftplane/draftplane/theme"
	"github.com/draftplane/draftplane/ui"
)

// panelPath and panelReason are the fixture values the byte-exact assertions
// below are written against. A real fault's path and reason are driven
// separately, through the real door, by
// TestTheMissingFilePanelOpensOnTheFaultTheSessionLatched below.
const (
	panelPath   = "~/plans/auth-redesign.md"
	panelReason = "permission denied"
)

// panelCause is the shape the real door produces: session.readCause stores the
// *fs.PathError os.ReadFile answered, and nothing else. Its path is long
// deliberately -- the panel must render the reason ALONE, and a bare
// fs.ErrPermission would pass whether the panel drew the cause or the whole
// error.
var panelCause = &fs.PathError{
	Op:   "read",
	Path: "/var/folders/mn/15vs2gq926l4fsrwlftpr90m0000gn/T/TestSomething/001/auth-redesign.md",
	Err:  fs.ErrPermission,
}

// panelLongReason is a genuine ELOOP message, long enough that the unreadable
// headline it produces (84 cells) exceeds panelMeasure (72 cells,
// app/actions.go) and so drives a real word-wrap. panelReason's "permission
// denied" makes a 68-cell headline: it fits under the measure by coincidence
// and never wraps.
const panelLongReason = "too many levels of symbolic links"

var panelCauseLongReason = &fs.PathError{
	Op:   "read",
	Path: "/var/folders/mn/15vs2gq926l4fsrwlftpr90m0000gn/T/TestSomething/002/auth-redesign.md",
	Err:  errors.New(panelLongReason),
}

// faultedModel latches fault by hand rather than through session.OpenPlan,
// deliberately: a real fault's path is a t.TempDir() one, 80-odd characters
// that wrap at 80 columns for reasons that have nothing to do with the panel's
// own wording. The door that latches the field for real is driven in its own
// test below.
func faultedModel(t *testing.T, fault *session.SourceFileError) *Model {
	t.Helper()
	f := setup(t)
	s, err := session.Open(f.ctx, f.svc, f.path)
	if err != nil {
		t.Fatal(err)
	}
	s.SourceFault = fault
	m := New(s, keymap.Default(), nil, "", nil)
	if err := m.RefreshFromSession(f.ctx); err != nil {
		t.Fatal(err)
	}
	m.width, m.height = 80, 24
	m.rerender()
	return m
}

// boxGeometry locates the centred panel's border in an ansi.Strip'd frame: the
// column its top-left corner sits in, the row, and the box's own width and
// height in cells. It MEASURES THE DRAWN BOX rather than recomputing what
// centredPanelBox would have produced -- a check built from the same arithmetic
// as the code would agree with it either way.
//
// Searching for the HEAVY corners is itself an assertion: every caller fails
// outright -- "no bordered box in the frame" -- if the border ever goes back to
// the light set. That is left as this helper's own fatal rather than duplicated
// as a glyph check in each caller.
func boxGeometry(t *testing.T, screen string) (x, y, w, h int) {
	t.Helper()
	rows := strings.Split(screen, "\n")
	top, bottom := -1, -1
	for i, r := range rows {
		if top < 0 && strings.Contains(r, "┏") {
			top = i
		}
		if strings.Contains(r, "┗") {
			bottom = i
		}
	}
	if top < 0 || bottom < top {
		t.Fatalf("no bordered box in the frame:\n%s", screen)
	}
	head := rows[top]
	l, r := strings.Index(head, "┏"), strings.Index(head, "┓")
	if r < l {
		t.Fatalf("the box's top row has no right corner: %q", head)
	}
	return ansi.StringWidth(head[:l]), top, ansi.StringWidth(head[l : r+len("┓")]), bottom - top + 1
}

// TestPanel1DrawsThePinnedTextWholeAtEightyColumns pins Panel 1 against its
// pinned wording, and the table covers ALL FOUR of its texts (gone,
// unreadable, claimed, released): an assertion over one of the four reads as
// covering the panel while leaving a truncation regression invisible on the
// others.
//
// THE JOINER IS "\n" AND NOT " " for the byte-exact `want` pin: this panel's
// body carries its own line breaks (a headline, the path row, a paragraph,
// four key lines), so what m.confirm must equal, unwrapped, is a "\n"-joined
// literal -- and that pin holds at any terminal width, since sourceFaultText
// never wraps anything itself.
//
// THE WRAPPED REJOIN CHECK IS PER-BLOCK, NOT A GLOBAL "\n" REJOIN, because
// the "unreadable long reason" case genuinely exceeds panelMeasure and
// ansi.Wordwrap turns a real space into a real newline inside the headline --
// which a byte-exact rejoin would read as dropped text. Splitting both sides
// on "\n\n" first recovers the blocks m.confirm already has, so a block merged
// or split is still caught while a legitimate wrap WITHIN a block is not.
//
// THE GEOMETRY HALF IS THE RESERVATION CHECK: the panel is composited into a
// finished frame (composeCentredPanel) and reserves nothing, so viewHeight is
// UNCHANGED from read mode, panelViewPainted draws NOTHING, and the screen is
// still exactly m.height rows by m.width cells. Keeping only the last of the
// three would let a half-finished move pass -- a strip still drawn under a
// reservation that no longer exists, or a reservation left standing under a
// splice that needs none -- because the frame's row count is the symptom both
// halves share; the two negative assertions are what say which half moved.
//
// THE BOX'S OWN NUMBERS ARE MEASURED OFF THE SCREEN and checked against the
// lines, not against constants: pinning the digits the pinned text happens
// to measure today would fail on a reword that is perfectly correct. What must
// hold is the RELATIONSHIP -- the box is the widest line plus four cells, its
// own line count plus two rows, and centred on both axes.
func TestPanel1DrawsThePinnedTextWholeAtEightyColumns(t *testing.T) {
	const tail = "Draftplane still has a cache of the most recent version.\n" +
		"\n" +
		"f · point at a new file location\n" +
		"o · keep it in Draftplane with no file (agent edits only)\n" +
		"d · delete this plan\n" +
		"esc · go back to the plan list"
	for _, tc := range []struct {
		name  string
		fault *session.SourceFileError
		want  string
	}{
		{
			name:  "gone",
			fault: &session.SourceFileError{Path: panelPath, State: session.SourceFileGone, Err: fs.ErrNotExist},
			want: "This plan's original file is gone or has moved:\n" +
				"\n" +
				">  " + panelPath + "\n" +
				"\n" + tail,
		},
		{
			name:  "unreadable",
			fault: &session.SourceFileError{Path: panelPath, State: session.SourceFileUnreadable, Err: panelCause},
			want: "Draftplane can't read this plan's original file (" + panelReason + "):\n" +
				"\n" +
				">  " + panelPath + "\n" +
				"\n" + tail,
		},
		{
			// One of three rows whose headline exceeds panelMeasure (72 cells,
			// this one at 84) and so drives a real word-wrap -- claimed and
			// released below are the other two.
			name:  "unreadable long reason",
			fault: &session.SourceFileError{Path: panelPath, State: session.SourceFileUnreadable, Err: panelCauseLongReason},
			want: "Draftplane can't read this plan's original file (" + panelLongReason + "):\n" +
				"\n" +
				">  " + panelPath + "\n" +
				"\n" + tail,
		},
		{
			// This state on this carrier originally answered "" -- see sourceFaultText's
			// own doc for when claimed and released gained their panels. 82-cell
			// headline, past panelMeasure, so this wraps too.
			name:  "claimed",
			fault: &session.SourceFileError{Path: panelPath, State: session.SourceFileClaimed, Plan: domain.Plan{ID: "l_other", Title: "Somebody Else's Plan"}},
			want: "Another plan is already following this file. Two plans can't follow the same file:\n" +
				"\n" +
				">  " + panelPath + "\n" +
				"\n" + tail,
		},
		{
			// Same doc pointer as claimed above. 88-cell headline, also wraps.
			name:  "released",
			fault: &session.SourceFileError{Path: panelPath, State: session.SourceFileReleased},
			want: "This plan's original file is still on disk, but it no longer resolves back to this plan:\n" +
				"\n" +
				">  " + panelPath + "\n" +
				"\n" + tail,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := faultedModel(t, tc.fault)
			// faultedModel comes back with the panel already open (New is what
			// opens it), so read mode's height is measured by backing it out first.
			m.mode = modeRead
			readVH := m.viewHeight()
			m.enterSourceFault(m.sess.SourceFault)

			if m.mode != modeSourceFault {
				t.Fatalf("mode = %v, want modeSourceFault", m.mode)
			}
			if m.confirm != tc.want {
				t.Fatalf("panel body mismatch:\ngot:  %q\nwant: %q", m.confirm, tc.want)
			}
			// The tail is where all four of this panel's keys live (f, o, d, esc),
			// asserted directly so a reworded headline that silently ate the tail
			// cannot pass merely because the table's own `want` was edited to match.
			if !strings.HasSuffix(m.confirm, tail) {
				t.Fatalf("%s does not end with the shared tail naming all four keys:\n%s", tc.name, m.confirm)
			}
			// Claimed's fixture carries a conflicting plan with a Title,
			// exactly as the real fault does, and the panel must not spend
			// it, a standing choice followed here rather than reopened.
			if title := tc.fault.Plan.Title; title != "" && strings.Contains(m.confirm, title) {
				t.Fatalf("%s: the panel names the conflicting plan (%q), which the panel must not:\n%s", tc.name, title, m.confirm)
			}
			// {reason} is readReason(f.Err), the innermost cause, not the whole
			// *fs.PathError -- driven off tc.fault.Err rather than by name, so a
			// third fixture added later is covered too. Both causes carry a long,
			// unrelated path, so its presence here would mean the whole error got
			// rendered and panelPath was drawn twice. gone's fs.ErrNotExist is not
			// a *fs.PathError, so it is skipped rather than asserted about.
			if pe, ok := tc.fault.Err.(*fs.PathError); ok {
				if strings.Contains(m.confirm, pe.Path) {
					t.Fatalf("the panel renders the fault's own path (%q), which means the whole *fs.PathError was rendered rather than readReason's innermost cause:\n%s", pe.Path, m.confirm)
				}
			}
			lines := m.confirmLines()
			// Per-block, not a global "\n" rejoin -- see this test's doc comment.
			gotBlocks := strings.Split(strings.Join(lines, "\n"), "\n\n")
			wantBlocks := strings.Split(m.confirm, "\n\n")
			if len(gotBlocks) != len(wantBlocks) {
				t.Fatalf("%s: the wrapped panel has %d blank-line-separated blocks, want %d -- a block was merged or split", tc.name, len(gotBlocks), len(wantBlocks))
			}
			flattenWS := func(s string) string { return strings.Join(strings.Fields(s), " ") }
			for i := range wantBlocks {
				if got, want := flattenWS(gotBlocks[i]), flattenWS(wantBlocks[i]); got != want {
					t.Fatalf("%s: block %d's words are %q, want %q -- something was dropped or truncated", tc.name, i, got, want)
				}
			}
			// Four blocks, always: headline, path row, tail paragraph, tail key
			// lines -- and no hand-placed line break inside the first three, which
			// is what panelMeasure/centredInterior (app/actions.go) exist to wrap.
			if len(wantBlocks) != 4 {
				t.Fatalf("%s has %d blank-line-separated blocks, want 4 (headline, path, tail paragraph, tail keys): %q", tc.name, len(wantBlocks), m.confirm)
			}
			for i, part := range []string{"headline", "path row", "tail paragraph"} {
				if strings.Contains(wantBlocks[i], "\n") {
					t.Errorf("%s: the %s carries a hand-placed line break -- the measure is supposed to be doing this wrap, not the string literal: %q", tc.name, part, wantBlocks[i])
				}
			}
			if got := strings.Count(wantBlocks[3], "\n"); got != 3 {
				t.Errorf("%s: the tail's key lines = %d newlines, want 3 (f, o, d and esc, one row each)", tc.name, got)
			}
			if got, _ := m.panelViewPainted(); got != "" {
				t.Fatalf("panelViewPainted() drew %q, want nothing -- a centred panel spliced into the frame must not draw the bottom strip as well", got)
			}
			if got := m.viewHeight(); got != readVH {
				t.Fatalf("viewHeight() under the panel = %d, want %d -- a composited panel splices into rows the frame already has and must reserve none", got, readVH)
			}
			screen := ansi.Strip(m.View().Content)
			rows := strings.Split(screen, "\n")
			if len(rows) != m.height {
				t.Fatalf("the screen is %d rows against a terminal of %d -- the panel overflows it", len(rows), m.height)
			}
			for i, r := range rows {
				if w := ansi.StringWidth(r); w != m.width {
					t.Fatalf("screen row %d is %d cells against a terminal of %d -- an over-wide row wraps and adds a visual row: %q", i, w, m.width, r)
				}
			}
			for _, line := range strings.Split(tc.want, "\n") {
				if line == "" {
					continue
				}
				// viewContainsFlowing (list_test.go), not strings.Contains: the
				// long-reason headline is one logical line longer than the 72-cell
				// measure, so it wraps across two box rows and does not survive as
				// one contiguous run on screen.
				if !viewContainsFlowing(t, screen, line) {
					t.Fatalf("the panel's %q never reaches the screen:\n%s", line, screen)
				}
			}

			widest := 0
			for _, l := range lines {
				widest = max(widest, ansi.StringWidth(l))
			}
			x, y, w, h := boxGeometry(t, screen)
			if want := widest + 6; w != want {
				t.Fatalf("the box is %d cells wide, want %d -- the widest line plus a border cell and TWO of padding each side", w, want)
			}
			if want := len(lines) + 4; h != want {
				t.Fatalf("the box is %d rows tall, want %d -- a border row and a blank row above the lines, and the same two below", h, want)
			}
			if wx, wy := (m.width-w)/2, (m.height-h)/2; x != wx || y != wy {
				t.Fatalf("the box sits at (%d,%d), want (%d,%d) -- centred on the frame it was spliced into:\n%s", x, y, wx, wy, screen)
			}
		})
	}
}

// TestTheMissingFilePanelOpensOnTheFaultTheSessionLatched drives the real
// door for the two states THIS TABLE's mechanism can produce: session.OpenPlan
// latches the fault, New opens the panel off it, and the text names the path
// the open actually tried -- not one this test supplied.
//
// UNREADABLE IS A DIRECTORY AND NOT chmod 0o000: a permission bit is a no-op
// under a root test runner, so that shape can pass for the wrong reason on one
// machine and fail on another. EISDIR is nobody's privilege to bypass and
// classifies identically.
//
// CLAIMED AND RELEASED ARE NOT DRIVEN HERE, but not because they are
// unreachable on this carrier -- they are reachable, and sourceFaultText has
// words for both now (see its own doc for when claimed and released gained
// their panels). breakFile below only breaks the READ (deletes the file, or
// puts a directory in its place); claimed and released both need the read to
// SUCCEED and IDENTITY to disagree instead, which needs a different fixture --
// a ResolvePlan that answers a different plan, or none, at this plan's own
// path. TestRepointReopensThisPlanAndNotWhateverAnswersAtThePath below drives
// claimed through exactly that shape, live; the table test above asserts both
// texts off a hand-built fault, the same way this table's own gone and
// unreadable rows were asserted before this test existed.
//
// The assertions deliberately avoid containing m.confirm against the panel's
// own input, which passes for ANY composition. They ask instead what a reader
// would notice: where the path appears and HOW MANY TIMES, that the headline
// ends with the reason in parentheses, and that Draftplane's marker sentence
// is not in it. The bytes as a whole are pinned by the test above.
func TestTheMissingFilePanelOpensOnTheFaultTheSessionLatched(t *testing.T) {
	for _, tc := range []struct {
		name      string
		breakFile func(t *testing.T, path string)
		wantState session.SourceFileState
		wantHead  string
	}{
		{
			name:      "gone",
			breakFile: func(t *testing.T, path string) { mustRemove(t, path) },
			wantState: session.SourceFileGone,
			wantHead:  "This plan's original file is gone or has moved:",
		},
		{
			name: "unreadable",
			breakFile: func(t *testing.T, path string) {
				mustRemove(t, path)
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantState: session.SourceFileUnreadable,
			// No closing ")" or ":" here: the reason inside the parentheses is the
			// OS's own EISDIR message, which this test does not control and must
			// not guess at -- it is checked against the real cause below.
			wantHead: "Draftplane can't read this plan's original file (",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newListFixture(t)
			s := seedListPlan(t, f, filepath.Dir(f.path), "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")
			tc.breakFile(t, s.Path)

			sess, err := session.OpenPlan(f.ctx, f.svc, s.Plan)
			if err != nil {
				t.Fatalf("OpenPlan = %v, want the snapshot fallback rather than a dead end", err)
			}
			if sess.SourceFault == nil {
				t.Fatal("SourceFault = nil -- this fixture is not producing the fault the panel is about")
			}
			if sess.SourceFault.State != tc.wantState {
				t.Fatalf("SourceFault.State = %s, want %s", sess.SourceFault.State, tc.wantState)
			}

			m := New(sess, keymap.Default(), nil, "", nil)
			if m.mode != modeSourceFault {
				t.Fatalf("mode = %v, want modeSourceFault -- the panel opens in the constructor every door shares", m.mode)
			}
			if !strings.HasPrefix(m.confirm, tc.wantHead) {
				t.Fatalf("panel body = %q, want it to open with %q", m.confirm, tc.wantHead)
			}
			// ONCE, and the count is the assertion: the path is the panel's own row
			// below the headline, and a headline that named the whole *fs.PathError
			// instead of readReason's answer would put it there again.
			if got := strings.Count(m.confirm, s.Path); got != 1 {
				t.Fatalf("the panel names %q %d times, want exactly 1:\n%s", s.Path, got, m.confirm)
			}
			if !strings.Contains(m.confirm, "\n>  "+s.Path+"\n") {
				t.Fatalf("panel body = %q, want the path on its own row behind a literal \">\"", m.confirm)
			}
			lines := strings.Split(m.confirm, "\n")
			if tc.wantState != session.SourceFileUnreadable {
				return
			}
			// The reason lives in the headline, in parentheses, so this checks the
			// headline's own suffix rather than searching the body for a standalone
			// row. cause is dug out of the fault here so the test states the shape
			// it expects rather than trusting the panel's word for it.
			var cause *fs.PathError
			if !errors.As(sess.SourceFault.Err, &cause) {
				t.Fatalf("SourceFault.Err = %#v, want the *fs.PathError os.ReadFile answered", sess.SourceFault.Err)
			}
			reason := cause.Err.Error()
			headline := lines[0]
			if want := "(" + reason + "):"; !strings.HasSuffix(headline, want) {
				t.Fatalf("headline = %q, want it to end with %q -- the reason is the remedy this state exists to offer", headline, want)
			}
			if strings.Contains(m.confirm, session.ErrSourceUnreadable.Error()) {
				t.Fatalf("the panel carries Draftplane's own marker %q, which restates its headline in worse words:\n%s",
					session.ErrSourceUnreadable, m.confirm)
			}
		})
	}
}

// TestAFaultWithNoPinnedWordsOpensNoPanel pins the residual of the silence
// rule now that sourceFaultText has words for all four SourceFileStates: a NIL
// fault, and a state its switch does not recognize, both still open nothing.
//
// SourceFileClaimed and SourceFileReleased WERE pinned here too, on the
// original premise that they were unreachable on this carrier. That premise was
// false -- TestRepointReopensThisPlanAndNotWhateverAnswersAtThePath reaches
// claimed live, and sourceFaultText's own doc records when claimed and released
// gained their panels. Both are now ordinary panel text, covered as table rows
// in TestPanel1DrawsThePinnedTextWholeAtEightyColumns above, not silence here.
//
// THE UNRECOGNIZED STATE IS THE FUTURE-PROOFING ROW: sourceFaultText's switch
// falls through to "" for any SourceFileState it has no case for, and a fifth
// state added to session someday without a matching case here must still fail
// closed -- silence over a guess -- rather than open a panel with an empty
// body.
func TestAFaultWithNoPinnedWordsOpensNoPanel(t *testing.T) {
	const unrecognized session.SourceFileState = 99
	m := faultedModel(t, &session.SourceFileError{Path: panelPath, State: unrecognized})
	if m.mode != modeRead {
		t.Fatalf("mode = %v, want modeRead -- a fault this panel has no words for must not open it", m.mode)
	}
	if m.confirm != "" {
		t.Fatalf("confirm = %q, want nothing", m.confirm)
	}
	if got := sourceFaultText(&session.SourceFileError{Path: panelPath, State: unrecognized}); got != "" {
		t.Errorf("sourceFaultText(state %d) = %q, want nothing -- an unrecognized state must fail closed", unrecognized, got)
	}
	// NIL IS NOT ONE OF THE FOUR STATES and cannot arrive from New, which tests
	// the pointer before it calls in -- but enterSourceFault's own doc says it
	// opens nothing for a fault this panel has no words for, and a reader takes
	// that as covering the absent fault too. One guard makes the sentence true.
	if got := sourceFaultText(nil); got != "" {
		t.Errorf("sourceFaultText(nil) = %q, want nothing at all", got)
	}
}

// TestPanel1RaisedByReloadAnswersTheKeysItNames is the half a "does the panel
// open" assertion leaves out. ctrl+r is the ONE door that raises Panel 1 over a
// session carrying no fault of its own -- the file was there when the session
// opened, so session.Session.SourceFault is nil and stays nil (OpenPlan is its
// only writer). Every gesture the panel offers then re-derives the panel, or
// the path it is about, from a fault, so a wiring that read the SESSION's fault
// at those sites would open a panel whose own keys quietly stop working.
//
// THE TWO SITES ARE THE TWO THAT BORROW m.confirm: modeConfirmDeletePlan's
// question and modeRepoint's pane, both of which hand Panel 1's body back
// rather than remembering a copy of it (see enterSourceFault). The third
// assertion is the pane's PREFILL, the one that fails silently rather than
// loudly, with an empty pane and nothing to say why.
//
// The session having no fault is asserted rather than assumed: it is what makes
// this a test of the reload door and not a second copy of
// TestEscFromTheRepointPaneHandsPanel1Back.
func TestPanel1RaisedByReloadAnswersTheKeysItNames(t *testing.T) {
	f := setup(t)
	// A REAL PLAN, not the plan-less session openModel alone produces: the
	// panel's f and o write a plan's SourceHint, and a session with no plan id
	// has none to write.
	seedThread(t, f, "why fifty?")
	m := openModel(t, f)
	mustRemove(t, f.path)

	cmd := m.runReload(f.svc, m.sess.Path, m.sess.FromSnapshot(), m.sess.Plan.ID)
	msg, ok := cmd().(msgActionDone)
	if !ok {
		t.Fatalf("runReload's cmd produced %T, want msgActionDone", cmd())
	}
	cur, _ := m.Update(msg)
	m = cur.(*Model)
	if m.mode != modeSourceFault {
		t.Fatalf("mode after ctrl+r = %v, want modeSourceFault", m.mode)
	}
	if m.sess.SourceFault != nil {
		t.Fatalf("test setup: the session latched %v, so this is not the door with no fault on its session", m.sess.SourceFault)
	}
	panel := m.confirm

	m = press(m, "d")
	if m.mode != modeConfirmDeletePlan {
		t.Fatalf("mode after d = %v, want modeConfirmDeletePlan", m.mode)
	}
	m = press(m, "n")
	if m.mode != modeSourceFault {
		t.Fatalf("mode after n = %v, want modeSourceFault -- declining the delete hands Panel 1 back", m.mode)
	}
	if m.confirm != panel {
		t.Fatalf("confirm after n = %q, want Panel 1's own body back %q", m.confirm, panel)
	}

	m = press(m, "f")
	if m.mode != modeRepoint {
		t.Fatalf("mode after f = %v, want modeRepoint", m.mode)
	}
	// EMPTY, AND FOCUSED: the pane does not open on the path the panel above it
	// has just called gone, and the widget takes the keyboard the moment it
	// appears.
	if got := m.pathTA.Value(); got != "" {
		t.Errorf("the re-point pane opened over %q, want an empty field", got)
	}
	if !m.pathTA.Focused() {
		t.Errorf("the re-point pane opened blurred -- the reader cannot type into it")
	}
	m = press(m, "esc")
	if m.mode != modeSourceFault {
		t.Fatalf("mode after esc = %v, want modeSourceFault -- backing out of the remedy hands Panel 1 back", m.mode)
	}
	if m.confirm != panel {
		t.Fatalf("confirm after esc = %q, want Panel 1's own body back %q", m.confirm, panel)
	}
}

// TestThePanelHintsNameExactlyTheKeysTheirHandlersAnswer applies this model's
// hardest rule to the panel's two modes: "a hint may name fewer keys than its
// mode owns and may NEVER name one its handler does not answer" (modeHints).
// It is asserted in BOTH directions -- named but unanswered is the defect the
// rule is about, answered but unnamed is how a key becomes undiscoverable --
// so the equality is what the test drives rather than a containment.
//
// THE ANSWERED SET IS MEASURED, NOT DECLARED: every candidate key is pressed
// against the real handler, and a key counts as answered when the model moved
// (mode, panel body or status) or a cmd came back. That is what makes this
// fail on the day a key is named in a hint before updatePanel answers it, or
// answered before it is named.
//
// THE CANDIDATE SET IS EVERY KEY EITHER PANEL COULD PLAUSIBLY TAKE, including
// the four Panel 1's own body names and the three every confirm panel in this
// package takes, so a key that quietly starts doing something is caught by the
// same equality.
func TestThePanelHintsNameExactlyTheKeysTheirHandlersAnswer(t *testing.T) {
	candidates := []string{"f", "o", "d", "u", "y", "n", "esc", "q", "j"}
	for _, tc := range []struct {
		name string
		mode mode
	}{
		{"the missing-file panel", modeSourceFault},
		{"its delete confirm", modeConfirmDeletePlan},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answered := map[string]bool{}
			for _, key := range candidates {
				m := faultedModel(t, &session.SourceFileError{Path: panelPath, State: session.SourceFileGone, Err: fs.ErrNotExist})
				if tc.mode == modeConfirmDeletePlan {
					m = press(m, "d")
				}
				if m.mode != tc.mode {
					t.Fatalf("test setup: mode = %v, want %v", m.mode, tc.mode)
				}
				stroke := tea.KeyPressMsg{Code: rune(key[0]), Text: key}
				if key == "esc" {
					stroke = tea.KeyPressMsg{Code: tea.KeyEscape}
				}
				before := *m
				cur, cmd := m.Update(stroke)
				after := cur.(*Model)
				if cmd != nil || after.mode != before.mode || after.confirm != before.confirm || after.status != before.status {
					answered[key] = true
				}
			}
			hinted := map[string]bool{}
			for _, seg := range strings.Split(modeHints[tc.mode].line(faultedModel(t, &session.SourceFileError{Path: panelPath, State: session.SourceFileGone, Err: fs.ErrNotExist})), " · ") {
				for _, key := range strings.Split(strings.Fields(seg)[0], "/") {
					hinted[key] = true
				}
			}
			for key := range hinted {
				if !answered[key] {
					t.Errorf("the help bar names %q, which the handler does not answer", key)
				}
			}
			for key := range answered {
				if !hinted[key] {
					t.Errorf("the handler answers %q and the help bar never names it -- the key is undiscoverable", key)
				}
			}
		})
	}
}

// TestDeletingThePlanFromThePanelEndsTheReview pins this gesture end to end,
// through Root because that is where the ending is observable: the plan the
// review was reading is destroyed, so there is nothing to return to and the
// list is what comes back.
//
// THE CONFIRM TEXT IS COMPARED AGAINST ordinaryDeleteConfirmText's OWN OUTPUT
// rather than a retyped string: this panel CALLS the plan list's text and does
// not copy it, and a test carrying its own copy of the wording would be the
// second copy the decision exists to prevent.
func TestDeletingThePlanFromThePanelEndsTheReview(t *testing.T) {
	f := newListFixture(t)
	s := seedListPlan(t, f, filepath.Dir(f.path), "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")
	mustRemove(t, s.Path)

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())
	r = openViaEnter(t, r)
	if r.review == nil || r.review.mode != modeSourceFault {
		t.Fatalf("test setup: the review must open under the panel; inReview=%v", r.inReview)
	}

	r = pressRoot(r, "d")
	if r.review.mode != modeConfirmDeletePlan {
		t.Fatalf("mode = %v, want modeConfirmDeletePlan -- d asks before it destroys", r.review.mode)
	}
	if got, want := r.review.confirm, ordinaryDeleteConfirmText(s.Plan); got != want {
		t.Fatalf("confirm = %q, want the plan list's own text %q", got, want)
	}

	// n hands the panel back whole: nothing was destroyed and the file is
	// still missing, so the question Panel 1 asks is still open.
	r = pressRoot(r, "n")
	if r.review.mode != modeSourceFault {
		t.Fatalf("mode after n = %v, want modeSourceFault", r.review.mode)
	}
	if !strings.HasPrefix(r.review.confirm, "This plan's original file is gone or has moved:") {
		t.Fatalf("confirm after n = %q, want Panel 1's own body back", r.review.confirm)
	}

	r = pressRoot(r, "d")
	cur, cmd := r.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("y on the delete confirm returned no cmd -- nothing was dispatched")
	}
	r = drainRoot(t, cur.(*Root), cmd)

	if r.inReview {
		t.Fatalf("root is still in review after the plan it was reading was deleted (mode %v)", r.review.mode)
	}
	plans, err := f.svc.ListPlans(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 0 {
		t.Fatalf("plans = %+v, want the plan destroyed", plans)
	}
	if len(r.list.items) != 0 {
		t.Fatalf("list items = %+v, want the row gone after the list refreshed", r.list.items)
	}
}

// TestDeletingThePlanFromThePanelForgetsIt covers Panel 1's own delete: the
// plan it destroys came from an open this session recorded, and the
// forget must land beside the destroy rather than only from the list's own d.
func TestDeletingThePlanFromThePanelForgetsIt(t *testing.T) {
	f := newListFixture(t)
	s := seedListPlan(t, f, filepath.Dir(f.path), "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")
	mustRemove(t, s.Path)
	path := filepath.Join(t.TempDir(), "recent.json")

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r.SetRecents(path)
	r = drainRoot(t, r, r.Init())
	r = openViaEnter(t, r)
	if r.review == nil || r.review.mode != modeSourceFault {
		t.Fatalf("test setup: the review must open under the panel; inReview=%v", r.inReview)
	}

	r = pressRoot(r, "d")
	cur, cmd := r.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	drainRoot(t, cur.(*Root), cmd)

	if got, _ := recent.Load(path); len(got) != 0 {
		t.Fatalf("recent.json = %+v, want the deleted plan forgotten", got)
	}
}

// TestEscFromTheMissingFilePanelWritesNothing is esc's half of the same
// property: esc backs out to the plan list and leaves everything exactly as
// it found it -- the plan, its threads, and the source hint still pointing at
// a file that is not there.
// There is deliberately no read-it-and-decide-later key on this panel, so this
// is the whole of what backing out means.
func TestEscFromTheMissingFilePanelWritesNothing(t *testing.T) {
	f := newListFixture(t)
	s := seedListPlan(t, f, filepath.Dir(f.path), "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")
	mustRemove(t, s.Path)

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())
	r = openViaEnter(t, r)
	if r.review == nil || r.review.mode != modeSourceFault {
		t.Fatalf("test setup: the review must open under the panel; inReview=%v", r.inReview)
	}

	cur, cmd := r.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc on the panel returned no cmd -- it must hand back to the list")
	}
	r = drainRoot(t, cur.(*Root), cmd)

	if r.inReview {
		t.Fatal("esc must leave the review for the plan list")
	}
	plans, err := f.svc.ListPlans(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].ID != s.Plan.ID {
		t.Fatalf("plans = %+v, want the plan untouched", plans)
	}
	if plans[0].SourceHint != s.Path {
		t.Fatalf("SourceHint = %q, want it still naming the missing file -- esc writes nothing", plans[0].SourceHint)
	}
}

// TestTheCentredPanelAddsNoRowThroughTheRealDoor is the reservation check
// driven through THE DOOR A USER TAKES -- NewRoot, Init, a WindowSizeMsg, and
// enter on the row -- rather than over a *Model assembled in the test. This
// package's other row-count assertions (app/delete_test.go among them) measure
// a directly constructed Model, and a panel
// that composited correctly there and wrongly under Root would pass every one.
//
// WHAT IT ASSERTS IS "THE PANEL ADDS NO ROW", NOT "THE FRAME IS m.height", and
// the difference is the whole point of the second case. The frame is m.height
// rows only while every ui.Line is one screen row, which is not true of every
// document: ui.Line.Text may carry an EMBEDDED NEWLINE and viewPainted writes
// exactly one "\n" per Line, so a heading too wide for its slot makes the frame
// one row taller than the viewport it was cut to. Asserting "== m.height" here
// would be red for a defect this panel does not own and cannot fix without
// hiding a mapping ui is answerable for. The wrapping-heading fixture below is
// ui_test.go's own TestOneUILineCanCarryTwoScreenRows fixture (71 "h" after the
// sigil, into a 72-cell slot at width 80), brought here to put a real document
// of that shape under the real door.
//
// What the panel DOES own is that raising it changes no row count, and that is
// the equality below -- taken on ONE model, one document, one size, with only
// the mode moved, so nothing else can explain a difference. It catches a strip
// still drawn under a splice, or a reservation left standing, on EITHER kind of
// document. The exact-height assertion is kept as well, on the case where it is
// legitimately true, so the pair says both things: the absolute number where it
// is knowable, and the delta where it is not.
func TestTheCentredPanelAddsNoRowThroughTheRealDoor(t *testing.T) {
	for _, tc := range []struct {
		name      string
		doc       string
		wantExact bool
	}{
		{
			name:      "an ordinary document",
			doc:       "# Rate Limiter\n\nBody of the plan.\n",
			wantExact: true,
		},
		{
			// ui.Line's open heading residual, under the real door. Not
			// wantExact: this frame is legitimately taller than the terminal
			// for a reason that predates this panel and survives its removal.
			name:      "a document whose heading wraps into two screen rows",
			doc:       "# " + strings.Repeat("h", 71) + "\n\nBody of the plan.\n",
			wantExact: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newListFixture(t)
			s := seedListPlan(t, f, filepath.Dir(f.path), "plan.md", "Rate Limiter", tc.doc)
			mustRemove(t, s.Path)

			r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
			r = drainRoot(t, r, r.Init())
			cur, cmd := r.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			r = drainRoot(t, cur.(*Root), cmd)
			r = openViaEnter(t, r)
			if r.review == nil || r.review.mode != modeSourceFault {
				t.Fatalf("test setup: review=%v mode=%v, want the panel up over a plan whose file is gone", r.review != nil, r.list.status)
			}
			if r.review.width != 80 || r.review.height != 24 {
				t.Fatalf("test setup: the review is %dx%d, want 80x24 -- Root must have replayed the size", r.review.width, r.review.height)
			}

			up := strings.Split(ansi.Strip(r.View().Content), "\n")
			// The same model with only the mode moved: no re-render, no
			// re-size, nothing else that could change a row count.
			r.review.mode = modeRead
			down := strings.Split(ansi.Strip(r.View().Content), "\n")
			r.review.mode = modeSourceFault

			if len(up) != len(down) {
				t.Fatalf("the frame is %d rows with the panel up and %d with it down -- a composited panel splices into rows the frame already has and must add none:\n%s",
					len(up), len(down), strings.Join(up, "\n"))
			}
			if !tc.wantExact {
				// The fixture has to actually exercise the residual, or this
				// case asserts nothing that the first one does not.
				if len(up) <= r.review.height {
					t.Fatalf("the frame is %d rows on a terminal of %d -- this fixture was meant to carry ui.Line's heading residual and no longer does, so it is measuring nothing",
						len(up), r.review.height)
				}
				return
			}
			if len(up) != r.review.height {
				t.Fatalf("the frame is %d rows against a terminal of %d:\n%s", len(up), r.review.height, strings.Join(up, "\n"))
			}
			for i, row := range up {
				if w := ansi.StringWidth(row); w != r.review.width {
					t.Fatalf("frame row %d is %d cells against a terminal of %d -- an over-wide row wraps and adds a visual row: %q", i, w, r.review.width, row)
				}
			}
			if !strings.Contains(ansi.Strip(r.View().Content), "esc · go back to the plan list") {
				t.Fatalf("the panel never reaches the screen through the real door:\n%s", strings.Join(up, "\n"))
			}
		})
	}
}

// TestTheCentredPanelBorderIsOneColourAndItsHeadIsWarn pins the panel's COLOUR,
// which the geometry assertions above cannot see: every one of them runs on an
// ansi.Strip'd frame, so the box drew in two colours with every test in this
// file green.
//
// THE TWO CLAIMS ARE INDEPENDENT. The BORDER is one colour all the way round,
// and the HEAD ROW is the warn colour (theme.Warn). They pull in opposite
// directions on purpose: the headline becomes the loudest thing in the box
// precisely because the rectangle round it stopped competing, so a change that
// moved the border onto Warn would satisfy "one colour" and undo the point of
// it. That is why the border's ink is asserted to be Chrome's SPECIFICALLY, and
// why Warn is asserted to appear on exactly one row. FocusHeader is checked for
// BY NAME rather than merely "not Warn", so a revert reports as itself instead
// of "some colour changed".
//
// The interior rows are walked for their '┃' and their two cells of padding
// while the rows are in hand; the heavy corners and the box's overall size are
// boxGeometry's and the geometry test's, and are not repeated here.
//
// IT IS DRIVEN AT TWO WIDTHS AND THE SECOND ONE IS THE POINT. At 80x24 the
// headline fits on ONE ROW, so "the warn colour is on row 2" and "the warn
// colour is on the headline" are the same assertion; at 40x12 the headline
// wraps, and a panel that coloured only the first row draws one sentence in two
// colours. A size that cannot fail is a green ornament.
//
// THE HEAD'S ROW COUNT IS RE-DERIVED FROM THE BODY rather than asked of
// m.confirmGroups(), for the reason TestPanel1ShowsEveryLineWholeOrNotAtAll
// gives at length: an expectation taken from the code under test agrees with it
// about a mutation. wantHead is stated in the table on top of that, so a
// reworded headline or a changed width that stopped exercising the wrap goes
// red rather than quietly measuring the one-row case twice.
func TestTheCentredPanelBorderIsOneColourAndItsHeadIsWarn(t *testing.T) {
	for _, name := range []string{"dark", "light"} {
		th, err := theme.Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		st := ui.NewStyles(th)
		chromeBG, chromeFG := paintedBG(st.Chrome.Render("x")), paintedFG(st.Chrome.Render("x"))
		warnFG, focusFG := paintedFG(st.WarnHeader.Render("x")), paintedFG(st.FocusHeader.Render("x"))
		if chromeBG == "" || chromeFG == "" || warnFG == "" {
			t.Fatalf("%s: Chrome bg/fg = %q/%q, Warn fg = %q, want all three set", name, chromeBG, chromeFG, warnFG)
		}
		// Without this the assertions below pass on a palette that spends one
		// colour on both roles, and would go on passing if Warn were quietly
		// pointed back at Focus.
		if warnFG == chromeFG || warnFG == focusFG {
			t.Fatalf("%s: Warn fg = %q, Chrome %q, Focus %q -- the warn role must be a colour of its own or this test asserts nothing", name, warnFG, chromeFG, focusFG)
		}
		if got := paintedBG(st.WarnHeader.Render("x")); got != chromeBG {
			t.Fatalf("%s: WarnHeader bg = %q, want Chrome's %q -- the two mix across one row, so a ground of its own draws a band the width of the headline", name, got, chromeBG)
		}

		for _, sz := range []struct {
			name          string
			width, height int
			// wantHead is how many screen rows the headline occupies at this
			// width: 1 where it fits, 2 where it wraps. Both must be driven
			// or this test measures only the case it cannot fail on.
			wantHead int
		}{
			{"80x24", 80, 24, 1},
			{"40x12", 40, 12, 2},
		} {
			for _, tc := range []struct {
				state string
				fault *session.SourceFileError
			}{
				{"gone", &session.SourceFileError{Path: panelPath, State: session.SourceFileGone, Err: fs.ErrNotExist}},
				{"unreadable", &session.SourceFileError{Path: panelPath, State: session.SourceFileUnreadable, Err: panelCause}},
			} {
				t.Run(name+"/"+sz.name+"/"+tc.state, func(t *testing.T) {
					m := faultedModel(t, tc.fault)
					// faultedModel builds on the default theme, and the styles are
					// what the box reads, so swapping them is the whole of driving
					// it under the other preset.
					m.styles = ui.NewStyles(th)
					m.width, m.height = sz.width, sz.height
					m.rerender()

					headRows := panelLineRows(strings.Split(m.confirm, "\n")[0], max(m.width-6, 10))
					if len(headRows) != sz.wantHead {
						t.Fatalf("%s: the headline is %d rows at %s, want %d -- this case was written to drive a headline of that many rows and no longer does: %q",
							name, len(headRows), sz.name, sz.wantHead, headRows)
					}

					rows := strings.Split(m.centredPanelBox(), "\n")
					if len(rows) < 5 {
						t.Fatalf("%s: the box is %d rows, want a rule, a blank, at least one line, a blank and a rule", name, len(rows))
					}
					// The panel must actually LEAD with the headline here, or
					// "the warn colour is on the headline" has no subject and
					// the walk below would be pinning the colour of whatever
					// the truncation left first.
					for k, want := range headRows {
						if got := ansi.Strip(rows[2+k]); !strings.Contains(got, want) {
							t.Fatalf("%s: box row %d is %q, want it to carry %q -- the panel does not lead with its headline at %s", name, 2+k, got, want, sz.name)
						}
					}
					for i, row := range rows {
						if got := paintedBG(row); got != chromeBG {
							t.Fatalf("%s: box row %d is painted on %q, want Chrome's %q -- one ground for the whole box: %q", name, i, got, chromeBG, ansi.Strip(row))
						}
						head := i >= 2 && i < 2+len(headRows)
						if strings.Contains(row, warnFG) != head {
							t.Fatalf("%s: box row %d %s the warn colour, want it on every row of the headline and nowhere else -- a sentence in two colours is the reading the border was corrected for: %q",
								name, i, map[bool]string{true: "carries", false: "does not carry"}[!head], ansi.Strip(row))
						}
						if strings.Contains(row, focusFG) {
							t.Fatalf("%s: box row %d carries the focus colour %q -- the border went back to FocusHeader, or the head row did: %q", name, i, focusFG, ansi.Strip(row))
						}
					}
					for _, i := range []int{0, len(rows) - 1} {
						if got := paintedFG(rows[i]); got != chromeFG {
							t.Fatalf("%s: the rule at row %d is inked %q, want Chrome's %q -- the border is one colour and it is the sides' one", name, i, got, chromeFG)
						}
					}
					for _, i := range []int{1, len(rows) - 2} {
						plain := ansi.Strip(rows[i])
						if want := "┃" + strings.Repeat(" ", len([]rune(plain))-2) + "┃"; plain != want {
							t.Fatalf("%s: row %d is %q, want a blank row inside the border -- the panel's vertical padding", name, i, plain)
						}
					}
					for i, row := range rows[2 : len(rows)-2] {
						plain := ansi.Strip(row)
						if !strings.HasPrefix(plain, "┃  ") || !strings.HasSuffix(plain, "  ┃") {
							t.Fatalf("%s: content row %d is %q, want a '┃' and two cells of padding each side", name, i, plain)
						}
					}
				})
			}
		}
	}
}

// mustRemove is os.Remove with the fixture's own fatal, used where the
// removal IS the fixture rather than something under test.
func mustRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// TestPanel1KeepsItsWayOutOnACrampedTerminal is the size coverage for a panel
// whose body carries its OWN line breaks: it arrives at truncateConfirmLines as
// ten or twelve short rows rather than as one sentence the wrap can reflow, and
// THERE IS NO MINIMUM-SIZE GATE ANYWHERE IN THIS MODEL -- only the plan list
// has one -- so nothing stops any size from arriving. minConfirmBudget is 1.
//
// WHAT MUST SURVIVE IS THE LAST ROW, which for this panel is `esc · go back to
// the plan list`: the way out. truncateConfirmLines keeps the head, an ellipsis
// and the tail for exactly that reason, and every one of its branches --
// including the budget==1 and budget==2 ones a short terminal reaches -- keeps
// the final row. It is the SAME function the bottom-anchored panels use rather
// than a second rule for the centred box; a second truncation rule is how two
// panels come to disagree about which line matters most.
//
// 30x11 IS THE RULED FLOOR and it is what the box must STRIP TO FIT rather than
// refuse: below it both centring numbers go negative and ui.Overlay's own clamp
// becomes a live correction, which is why the frame's row AND column counts are
// asserted at every size here.
//
// The long-path row is the list's counterpart's own second size, for its
// reason: a hyphen-free path is one unbreakable token to ansi.Wordwrap, so it is
// ansi.Hardwrap that has to force the break, and an over-wide row reaching the
// render is a row that wraps and adds one the frame never budgeted for.
func TestPanel1KeepsItsWayOutOnACrampedTerminal(t *testing.T) {
	const longPath = "/Users/somebody/Library/Application Support/draftplane/plans/authredesignrolloutplan.md"
	for _, sz := range []struct {
		name          string
		width, height int
		path          string
	}{
		{"100x30", 100, 30, panelPath},
		{"80x24", 80, 24, panelPath},
		{"80x24 long path", 80, 24, longPath},
		{"40x12", 40, 12, panelPath},
		{"30x11", 30, 11, panelPath},
		{"60x10 long path", 60, 10, longPath},
	} {
		for _, st := range []struct {
			name  string
			fault func(path string) *session.SourceFileError
		}{
			{"gone", func(path string) *session.SourceFileError {
				return &session.SourceFileError{Path: path, State: session.SourceFileGone, Err: &fs.PathError{Op: "read", Path: path, Err: fs.ErrNotExist}}
			}},
			{"unreadable", func(path string) *session.SourceFileError {
				return &session.SourceFileError{Path: path, State: session.SourceFileUnreadable, Err: panelCause}
			}},
		} {
			t.Run(sz.name+"/"+st.name, func(t *testing.T) {
				m := faultedModel(t, st.fault(sz.path))
				m.width, m.height = sz.width, sz.height
				m.rerender()

				lines := m.confirmLines()
				if got, want := len(lines), m.confirmBudget(); got > want {
					t.Fatalf("confirmLines() = %d rows against a budget of %d", got, want)
				}
				// m.width-6 AND NOT m.width-4: a centred box spends three
				// cells a side (confirmLines' own branch), and the looser
				// bound would pass on lines two cells too wide for the box
				// drawn round them -- which is exactly the overflow the
				// branch was added to stop.
				budget := max(m.width-6, 10)
				for i, l := range lines {
					if w := ansi.StringWidth(l); w > budget {
						t.Fatalf("confirmLines()[%d] = %d display cells, want <= %d: %q", i, w, budget, l)
					}
				}
				// THE LAST THING THE PANEL SAYS, not literally its last ROW:
				// at 30 columns the esc clause is wider than the interior and
				// the wrap makes it two rows, so an equality against the final
				// element passes at every width where it happens not to wrap
				// and fails at the one size this test was extended to reach.
				// The suffix is the property that was always meant -- nothing
				// gets appended after the way out, and no truncation eats it.
				const wayOut = "esc · go back to the plan list"
				if joined := strings.Join(lines, " "); !strings.HasSuffix(joined, wayOut) {
					t.Fatalf("the panel ends %q, want it to end with %q -- the way out is the one thing a truncation may never drop:\n%s",
						joined, wayOut, strings.Join(lines, "\n"))
				}
				if got, _ := m.panelViewPainted(); got != "" {
					t.Fatalf("panelViewPainted() drew %q, want nothing -- the centred panel must not also draw the bottom strip", got)
				}
				screen := ansi.Strip(m.View().Content)
				rows := strings.Split(screen, "\n")
				if len(rows) != m.height {
					t.Fatalf("the screen is %d rows against a terminal of %d", len(rows), m.height)
				}
				for i, r := range rows {
					if w := ansi.StringWidth(r); w != m.width {
						t.Fatalf("screen row %d is %d cells against a terminal of %d: %q", i, w, m.width, r)
					}
				}
				// The box itself fits, which is what "strip to fit" means:
				// the budget cut the LINES to what the terminal has room for,
				// so the border drawn around them lands inside it rather than
				// being rescued by ui.Overlay's clamp.
				_, y, w, h := boxGeometry(t, screen)
				if h > m.height || w > m.width {
					t.Fatalf("the box is %dx%d on a terminal of %dx%d -- it was meant to be stripped to fit, not clipped:\n%s",
						w, h, m.width, m.height, screen)
				}
				if y < 0 || y+h > m.height {
					t.Fatalf("the box runs from row %d to %d on a terminal of %d rows:\n%s", y, y+h, m.height, screen)
				}
			})
		}
	}
}

// TestPanel1ShowsEveryLineWholeOrNotAtAll pins a TRUNCATION defect rather than
// the horizontal one it looked like: confirmLines wraps from the right and
// always did, but the wrap handed truncateConfirmLines a FLAT list of screen
// rows, so the truncator could not tell a break between two lines from a break
// inside one. It cut `o · keep it in Draftplane with no file (agent edits
// only)` in half, keeping the two rows that no longer name the key and dropping
// the row that did.
//
// THE RULE THIS PINS IS "WHOLE OR NOT AT ALL": every logical line of the
// panel's body either occupies all of the screen rows it wraps to, in order and
// contiguously, or none of them. It is asserted over EVERY logical line of both
// pinned texts, not just the ones that misbehaved, and at the sizes where the
// panel genuinely cannot show everything as well as the ones where it can -- a
// size that never truncates cannot fail this.
//
// THE ROWS A LINE OCCUPIES ARE RE-DERIVED HERE rather than asked of
// wrapConfirmGroups, which is the whole difference between this test and a
// tautology: a mutation that flattened the grouping again would flatten
// production and the expectation together if the expectation came from
// production.
//
// The two literal assertions at the end are the two symptoms by name. They are
// redundant with the rule above and kept anyway: the rule fails with a message
// about "some logical line", and these fail with the sentence a human actually
// read off the screen.
func TestPanel1ShowsEveryLineWholeOrNotAtAll(t *testing.T) {
	const longPath = "/Users/somebody/Library/Application Support/draftplane/plans/authredesignrolloutplan.md"
	for _, sz := range []struct {
		name          string
		width, height int
		path          string
	}{
		{"30x11", 30, 11, panelPath},
		{"40x12", 40, 12, panelPath},
		{"60x10", 60, 10, panelPath},
		{"80x24", 80, 24, panelPath},
		{"100x30", 100, 30, panelPath},
		// The path row is one unbreakable token here, so the group whose
		// rows must stay together is ansi.Hardwrap's rather than
		// ansi.Wordwrap's -- the other way a line comes to occupy more than
		// one row, and the one no wording change can remove.
		{"40x12 long path", 40, 12, longPath},
	} {
		for _, st := range []struct {
			name  string
			fault func(path string) *session.SourceFileError
		}{
			{"gone", func(path string) *session.SourceFileError {
				return &session.SourceFileError{Path: path, State: session.SourceFileGone, Err: fs.ErrNotExist}
			}},
			{"unreadable", func(path string) *session.SourceFileError {
				return &session.SourceFileError{Path: path, State: session.SourceFileUnreadable, Err: panelCause}
			}},
		} {
			t.Run(sz.name+"/"+st.name, func(t *testing.T) {
				m := faultedModel(t, st.fault(sz.path))
				m.width, m.height = sz.width, sz.height
				m.rerender()

				lines := m.confirmLines()
				// m.width-6 is the centred box's interior -- confirmLines' own
				// branch, and the width every row below was wrapped to.
				width := max(m.width-6, 10)
				var groups [][]string
				for _, logical := range strings.Split(m.confirm, "\n") {
					groups = append(groups, panelLineRows(logical, width))
				}
				// Walk what the panel drew and account for every row of it:
				// each is either the "…" that stands for what was dropped, or
				// the start of a logical line the panel shows WHOLE, in the
				// order the body has them. A row that is neither is a line cut
				// in half -- the defect.
				for i, g := 0, 0; i < len(lines); {
					if lines[i] == "…" {
						i++
						continue
					}
					next := -1
					for j := g; j < len(groups); j++ {
						if indexOfRun(lines[i:], groups[j]) == 0 {
							next = j
							break
						}
					}
					if next < 0 {
						t.Fatalf("the panel shows %q, which is no whole line of its body -- a line it cannot show whole must not be shown at all:\n%s",
							lines[i], strings.Join(lines, "\n"))
					}
					i += len(groups[next])
					g = next + 1
				}

				joined := strings.Join(lines, " ")
				const (
					option   = "o · keep it in Draftplane with no file (agent edits only)"
					orphan   = "(agent edits only)"
					lede     = "This plan's original file is gone"
					headline = "This plan's original file is gone or has moved:"
				)
				if strings.Contains(joined, orphan) && !strings.Contains(joined, option) {
					t.Fatalf("the panel says %q with no key in front of it -- the reader is told about an option they cannot reach:\n%s", orphan, strings.Join(lines, "\n"))
				}
				if strings.Contains(joined, lede) && !strings.Contains(joined, headline) {
					t.Fatalf("the panel's headline stops at %q -- a sentence cut in half says the file is gone and never says it may have moved:\n%s", lede, strings.Join(lines, "\n"))
				}
			})
		}
	}
}

// panelLineRows is the screen rows ONE logical line of a panel body occupies at
// width: the same two ansi passes confirmLines runs, written out here so the
// expectation is derived independently of the code under test.
func panelLineRows(line string, width int) []string {
	return strings.Split(ansi.Hardwrap(ansi.Wordwrap(line, width, ""), width, true), "\n")
}

// indexOfRun is where run appears in lines as a contiguous block, or -1.
// Contiguity is the property: a line's rows arriving in the right order but with
// something spliced between them is as broken as a line half dropped.
//
// The walk above asks only whether a run starts AT the row it is looking at, and
// steps its own group pointer forward as it consumes one. That is what keeps it
// honest when two different lines wrap to an identical row -- at ten cells both
// the cache sentence and the o line put the bare word "Draftplane" on a row of
// their own -- since a whole run has to match, not a row.
func indexOfRun(lines, run []string) int {
	for i := 0; i+len(run) <= len(lines); i++ {
		if slices.Equal(lines[i:i+len(run)], run) {
			return i
		}
	}
	return -1
}

// TestEscIsRefusedWhileTheDeleteItAuthorizedIsStillRunning pins a RACE rather
// than a mistake: showList opens with its own dispatchOK refusal, so an esc arm
// that dropped the mode first and called it second dismissed Panel 1 and then
// declined to leave. d, y, then esc before the result lands, and the user is
// reading the document of a plan whose file is gone, in read mode, with read
// mode's writes available and no key anywhere that re-opens the panel -- New is
// its only entry. That is the "read it and decide later" this product
// forbids, arrived at sideways.
//
// The delete's cmd is deliberately NOT drained: not draining it is what holds
// the model in the in-flight state the real race occupies.
func TestEscIsRefusedWhileTheDeleteItAuthorizedIsStillRunning(t *testing.T) {
	f := newListFixture(t)
	s := seedListPlan(t, f, filepath.Dir(f.path), "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")
	mustRemove(t, s.Path)
	sess, err := session.OpenPlan(f.ctx, f.svc, s.Plan)
	if err != nil {
		t.Fatal(err)
	}
	m := New(sess, keymap.Default(), nil, "", nil)
	if err := m.RefreshFromSession(f.ctx); err != nil {
		t.Fatal(err)
	}

	m = press(m, "d")
	cur, cmd := m.Update(keyYes)
	m = cur.(*Model)
	if cmd == nil || !m.inFlight {
		t.Fatalf("test setup: y must dispatch and leave the write in flight (cmd=%v inFlight=%v)", cmd, m.inFlight)
	}
	panel := m.confirm

	cur, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = cur.(*Model)
	if cmd != nil {
		t.Fatalf("esc returned a cmd while a write was in flight: %v -- showList refuses, and the mode must refuse with it", cmd)
	}
	if m.mode != modeSourceFault {
		t.Fatalf("mode = %v, want modeSourceFault -- an esc that cannot leave must not dismiss the panel either; nothing re-opens it", m.mode)
	}
	if m.confirm != panel {
		t.Fatalf("confirm = %q, want the panel body untouched", m.confirm)
	}
	if m.status != "action in progress" {
		t.Fatalf("status = %q, want dispatchOK's own refusal on screen", m.status)
	}
}

// repointDoc and repointMovedDoc are deliberately DIFFERENT text: the whole
// claim of a re-point is that the session goes on to read the file it now
// names, and a fixture whose two files hold the same bytes cannot tell that
// apart from a session that never left the snapshot.
const (
	repointDoc      = "# Rate Limiter\n\nBody of the plan.\n"
	repointMovedDoc = "# Rate Limiter\n\nBody of the plan, one directory over.\n"
)

// repointEnv is Panel 1 standing over a plan whose file is gone, with a
// comment thread on it -- built through the real doors (seedListPlan,
// session.OpenPlan, New) rather than by latching a fault by hand, because
// every assertion below is about a write landing in the store those doors
// read from.
type repointEnv struct {
	f      fixture
	seeded *session.Session
	thread domain.Thread
	m      *Model
}

// pressRepointCommit is the path pane's commit keystroke, drained like every
// other write in this file. It is a helper of its own so that the three commit
// shapes this package now has -- this pane's plain enter, the plan list's own
// rename ctrl+d (ListModel.updateRename, a second model and so beyond this
// helper's *Model reach in any case), and compose's own ring-gated post button (also enter, but
// only once tab has walked the focus there) -- cannot be driven by one call that
// hides which key, or which ring position, each answers.
//
// What the three have in common is that each commits; what separates them is the
// widget underneath. Rename holds a ONE-LINE title (SetHeight(1)), so its ctrl+d
// chord costs its reader no editing key they had. Compose holds a multi-line
// draft, where ctrl+d IS the textarea's own DeleteCharacterForward -- which is
// why it was taken off the composer and a button put there instead, and why
// one call driving "the commit key" for all three would be asserting a uniformity
// none of them has.
func pressRepointCommit(t *testing.T, m *Model) *Model {
	t.Helper()
	cur, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return drain(t, cur.(*Model), cmd)
}

func newRepointEnv(t *testing.T) repointEnv {
	t.Helper()
	return newRepointEnvOver(t, nil)
}

// newRepointEnvOver is newRepointEnv with the service the MODEL is built over
// interposed on. The fixture's own store still seeds the plan and still
// answers every assertion, so `nothing was written` is always read from the
// real state.json; what wrap decides is only what the door in front of it
// does on the way through.
func newRepointEnvOver(t *testing.T, wrap func(client.PlanService) client.PlanService) repointEnv {
	t.Helper()
	f := newListFixture(t)
	seeded := seedListPlan(t, f, filepath.Dir(f.path), "plan.md", "Rate Limiter", repointDoc)
	thread := commentOnFirstParagraph(t, f, seeded, "still here after the file moved")
	mustRemove(t, seeded.Path)

	svc := f.svc
	if wrap != nil {
		svc = wrap(f.svc)
	}
	sess, err := session.OpenPlan(f.ctx, svc, seeded.Plan)
	if err != nil {
		t.Fatalf("OpenPlan = %v, want the snapshot fallback the panel opens over", err)
	}
	m := New(sess, keymap.Default(), nil, "", nil)
	if err := m.RefreshFromSession(f.ctx); err != nil {
		t.Fatal(err)
	}
	m.width, m.height = 100, 30
	m.rerender()
	if m.mode != modeSourceFault {
		t.Fatalf("test setup: mode = %v, want modeSourceFault", m.mode)
	}
	return repointEnv{f: f, seeded: seeded, thread: thread, m: m}
}

// repointSpy is the re-point's own call-counting double: an ordinary
// client.PlanService in front of the fixture's real store, with two knobs a
// test arms and nothing else changed.
//
// IT POISONS AFTER A CALL COUNT RATHER THAN ALWAYS, which is what makes the
// states below reachable deterministically at all. A re-point makes TWO
// ResolvePlan calls -- the claim check on the loop, then the re-open inside the
// cmd -- and every state worth driving here lives between them: a double that
// answered wrongly from the first call would be refused by the claim check and
// never reach the write. writeThrough does the same for the write: it lands the
// real mutation and then lets a test change the world exactly where the
// product's own unclosable window is.
type repointSpy struct {
	client.PlanService

	// writeErr, when set, fails SetSourceHint instead of performing it.
	writeErr error
	// afterWrite runs after a SUCCESSFUL SetSourceHint has landed in the real
	// store -- the window between the write and the re-open, which nothing
	// in the product can close (see runRepoint).
	afterWrite func()

	// swapTo, once armed, is what ResolvePlan answers after poisonAfter
	// calls. Its zero value disarms the knob entirely, so the ordinary tests
	// share this type without thinking about it.
	swapTo      domain.Plan
	poisonAfter int
	resolves    int

	// resolveErr, when set, fails ResolvePlan outright -- the store failure
	// client.PathClaimant's own err != nil arm answers. Checked ahead of the
	// poisoned() swap.
	resolveErr error
}

// arm points ResolvePlan at swap from the (after+1)th call onward, counting from
// now: the fixture's own construction has already made calls of its own, and
// this is what makes the count a fact about the gesture rather than the setup.
func (s *repointSpy) arm(after int, swap domain.Plan) {
	s.resolves, s.poisonAfter, s.swapTo = 0, after, swap
}

func (s *repointSpy) poisoned() bool { return s.swapTo.ID != "" && s.resolves > s.poisonAfter }

func (s *repointSpy) ResolvePlan(ctx context.Context, hint string) (domain.Plan, error) {
	s.resolves++
	if s.resolveErr != nil {
		return domain.Plan{}, s.resolveErr
	}
	if s.poisoned() {
		return s.swapTo, nil
	}
	return s.PlanService.ResolvePlan(ctx, hint)
}

func (s *repointSpy) SetSourceHint(ctx context.Context, id domain.PlanID, hint string) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	if err := client.SetSourceHint(ctx, s.PlanService, id, hint); err != nil {
		return err
	}
	if s.afterWrite != nil {
		s.afterWrite()
	}
	return nil
}

// planNow re-reads the plan record out of the store, which is where every
// "nothing was written" assertion below actually looks.
func (e repointEnv) planNow(t *testing.T) domain.Plan {
	t.Helper()
	plan, err := e.f.svc.PlanByID(e.f.ctx, e.seeded.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// TestRepointFollowsTheNewFileAndKeepsThePlan pins the re-point end to
// end: f opens the path pane over the missing path, the commit key writes the
// one field a re-point may write, and the session comes back reading the new
// file as the SAME plan.
//
// THE PREFILL IS ASSERTED FIRST because it is the whole reason this pane was
// reused rather than inventing one: the missing path is already in the box, so
// a file that moved one directory over is an edit rather than a retype.
//
// THE THREAD IS WHAT THIS ASSERTION RESTS ON: a re-point that replaced the plan
// wholesale would pass a test that checked only the path and silently empty the
// review of a plan whose file merely moved. This pins that the door spends the
// narrow write.
//
// AND IT REOPENS. The model's own session is one thing; what the NEXT open finds
// is another, and the shared door (session.OpenPlan) is what the plan list and
// `draftplane review` will both come back through. Panel 1 must not be waiting
// for them.
func TestRepointFollowsTheNewFileAndKeepsThePlan(t *testing.T) {
	e := newRepointEnv(t)
	moved := filepath.Join(filepath.Dir(e.f.path), "moved", "plan.md")
	if err := os.MkdirAll(filepath.Dir(moved), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(moved, []byte(repointMovedDoc), 0o644); err != nil {
		t.Fatal(err)
	}

	m := press(e.m, "f")
	if m.mode != modeRepoint {
		t.Fatalf("mode after f = %v, want modeRepoint", m.mode)
	}
	// THE PANE OPENS EMPTY, and the assertion is stated rather than left out
	// because "no prefill" is a ruling, not an absence: a default put back here
	// would be a string the reader has to clear before they can type, on a pane
	// reached from a panel that has just told them that path is gone.
	if got := m.pathTA.Value(); got != "" {
		t.Fatalf("the pane opened on %q, want an empty field", got)
	}
	// THE WIDGET IS FOCUSED: a blurred textarea.Model drops every key in its own
	// Update, so f would open a field that cannot be typed into. Driven with real
	// keystrokes below rather than SetValue for the same reason -- SetValue reaches
	// the field whether it is focused or not.
	if !m.pathTA.Focused() {
		t.Fatalf("the pane opened blurred -- nothing the reader types can reach it")
	}
	// The pane's OWN strings, on the rendered screen. It draws as a
	// CENTRED BOX, so the two checked here are the box's own headline and key lines
	// rather than a bottom strip's chrome; the help bar under the box is checked by
	// identity in TestThePanelHintsNameExactlyTheKeysTheirHandlersAnswer.
	pane := ansi.Strip(m.View().Content)
	for _, want := range []string{repointPaneHeadline, "enter · submit", "esc · back"} {
		if !strings.Contains(pane, want) {
			t.Fatalf("the re-point pane never renders %q:\n%s", want, pane)
		}
	}

	for _, ch := range moved {
		m = press(m, string(ch))
	}
	if got := m.pathTA.Value(); got != moved {
		t.Fatalf("the pane holds %q after real keystrokes, want %q -- Focus() and the Update forward must both be reachable", got, moved)
	}
	m = pressRepointCommit(t, m)

	if m.mode != modeRead {
		t.Fatalf("mode after commit = %v, want modeRead; status=%q", m.mode, m.status)
	}
	if !strings.Contains(m.status, moved) {
		t.Fatalf("status = %q, want it to name the file this plan now follows", m.status)
	}
	if m.sess.Plan.ID != e.seeded.Plan.ID {
		t.Fatalf("plan id = %q, want the plan the review started on (%q)", m.sess.Plan.ID, e.seeded.Plan.ID)
	}
	if m.sess.FromSnapshot() || m.sess.SourceFault != nil {
		t.Fatalf("the session is still on the snapshot (fromSnapshot=%v fault=%v) -- a re-point that does not start following the file has done nothing",
			m.sess.FromSnapshot(), m.sess.SourceFault)
	}
	if string(m.sess.Content) != repointMovedDoc {
		t.Fatalf("session content = %q, want the moved file's own bytes", m.sess.Content)
	}
	if got := e.planNow(t).SourceHint; got != moved {
		t.Fatalf("SourceHint = %q, want %q", got, moved)
	}
	threads, err := e.f.svc.Threads(e.f.ctx, e.seeded.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 || threads[0].ID != e.thread.ID {
		t.Fatalf("threads = %+v, want the one thread this plan already had (%s)", threads, e.thread.ID)
	}

	reopened, err := session.OpenPlan(e.f.ctx, e.f.svc, e.planNow(t))
	if err != nil {
		t.Fatal(err)
	}
	if reopened.SourceFault != nil {
		t.Fatalf("reopening raises %v -- the panel must not be waiting for the next door", reopened.SourceFault)
	}
	if reopened.Plan.ID != e.seeded.Plan.ID || string(reopened.Content) != repointMovedDoc {
		t.Fatalf("reopened as %+v with content %q, want the same plan reading the moved file", reopened.Plan, reopened.Content)
	}
}

// TestRepointPastesAPathIntoTheFieldAndCommitsIt pins the routing a bracketed
// paste needs: bubbletea emits tea.PasteMsg, and unless Model.Update carries its
// own case for it the message is dropped before bubbles' own textarea -- which
// DOES handle tea.PasteMsg -- ever sees one.
//
// A REAL tea.PasteMsg AND NOT m.pathTA.SetValue: every other repoint test in this
// file drives the field with SetValue or real keystrokes, neither of which
// exercises Model.Update's own message-routing switch. What
// TestRepointFollowsTheNewFileAndKeepsThePlan already pins exhaustively (the
// write, the thread, the reopen) is not repeated here.
func TestRepointPastesAPathIntoTheFieldAndCommitsIt(t *testing.T) {
	e := newRepointEnv(t)
	moved := writeRepointTarget(t, e, "moved.md", repointMovedDoc)

	m := press(e.m, "f")
	cur, _ := m.Update(tea.PasteMsg{Content: moved})
	m = cur.(*Model)
	if got := m.pathTA.Value(); got != moved {
		t.Fatalf("pathTA after a bracketed paste = %q, want the pasted path %q", got, moved)
	}

	m = pressRepointCommit(t, m)
	if m.mode != modeRead {
		t.Fatalf("mode after commit = %v, want modeRead; status=%q", m.mode, m.status)
	}
	if got := e.planNow(t).SourceHint; got != moved {
		t.Fatalf("SourceHint = %q, want %q -- the pasted path was never committed", got, moved)
	}
}

// TestAPastedTrailingNewlineIsStrippedBeforeItReachesTheField: the commonest
// ways to copy a path -- selecting a line, `pwd | pbcopy`, grabbing one off a
// log -- carry a trailing newline, and bubbles' own textarea holds that as a
// SECOND LOGICAL LINE regardless of SetHeight(1). Driven directly against the
// widget rather than through the rendered frame: Line() and Column() are what
// repointInputRow's cursor window actually reads, and a stray second line puts
// the cursor at column 0 of it, which repointInputRow's own leftClip then reads
// as "nothing typed before the cursor", stranding the window on the HEAD of the
// path.
//
// A GENUINE TWO-LINE PASTE IS THE SECOND ROW: the trailing-newline shape above
// -- one line of real content, one empty -- does not exercise
// sanitizePastedPath on a second line of real content, and the concatenation it
// produces (the two lines glued with no separator, since the function deletes
// rather than joins) is worth pinning rather than assumed. It is not driven
// through a commit: the glued result is not a path on disk, so this row pins
// the widget's own shape and stops there.
func TestAPastedTrailingNewlineIsStrippedBeforeItReachesTheField(t *testing.T) {
	for _, tc := range []struct {
		name       string
		paste      func(moved string) string
		want       func(moved string) string
		wantCommit bool
	}{
		{
			name:       "a trailing newline",
			paste:      func(moved string) string { return moved + "\n" },
			want:       func(moved string) string { return moved },
			wantCommit: true,
		},
		{
			name:  "a genuine two-line paste",
			paste: func(moved string) string { return moved + "\nsecond-line.md" },
			want:  func(moved string) string { return moved + "second-line.md" },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRepointEnv(t)
			moved := writeRepointTarget(t, e, "moved.md", repointMovedDoc)

			m := press(e.m, "f")
			cur, _ := m.Update(tea.PasteMsg{Content: tc.paste(moved)})
			m = cur.(*Model)

			want := tc.want(moved)
			if got := m.pathTA.Value(); got != want {
				t.Fatalf("pathTA after %s = %q, want %q", tc.name, got, want)
			}
			if got := m.pathTA.LineCount(); got != 1 {
				t.Fatalf("LineCount() = %d, want 1 -- a stripped newline must not leave a second, empty logical line", got)
			}
			if got := m.pathTA.Line(); got != 0 {
				t.Fatalf("Line() = %d, want 0 -- the cursor must stay on the path's own line, not a line the newline created", got)
			}
			if got, wantCol := m.pathTA.Column(), len([]rune(want)); got != wantCol {
				t.Fatalf("Column() = %d, want %d -- the cursor belongs immediately after the pasted text", got, wantCol)
			}

			if !tc.wantCommit {
				return
			}
			// AND THE COMMIT STILL LANDS, on the same path -- sanitizing does
			// not merely fix the display; there is no newline left for
			// repointTarget's own TrimSpace to have to answer for either.
			m = pressRepointCommit(t, m)
			if m.mode != modeRead {
				t.Fatalf("mode after commit = %v, want modeRead; status=%q", m.mode, m.status)
			}
			if got := e.planNow(t).SourceHint; got != moved {
				t.Fatalf("SourceHint = %q, want %q", got, moved)
			}
		})
	}
}

// TestCtrlMCommitsTheRepointPaneExactlyLikeEnter: modeRepoint's enter arm
// matched only "enter", and bubbles' own textarea.KeyMap.InsertNewline binds
// BOTH "enter" and "ctrl+m" -- the ASCII CR a terminal still sends for either
// (tea.KeyPressMsg{Code: 'm', Mod: tea.ModCtrl}.String() == "ctrl+m").
// Unmatched, ctrl+m falls through to pathTA.Update, which the widget's own bound
// key answers by inserting a newline -- LineCount() becoming 2, the exact
// cursor-window defect sanitizePastedPath closes for a paste, reached here from
// the keyboard instead.
//
// DRIVEN AS A COMMIT AND NOT ONLY A KEYSTROKE: the strongest proof that ctrl+m
// is treated as enter is that it DOES what enter does -- lands the write -- not
// merely that it declines to type a newline.
func TestCtrlMCommitsTheRepointPaneExactlyLikeEnter(t *testing.T) {
	e := newRepointEnv(t)
	moved := writeRepointTarget(t, e, "moved.md", repointMovedDoc)

	m := press(e.m, "f")
	for _, ch := range moved {
		m = press(m, string(ch))
	}
	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'm', Mod: tea.ModCtrl})
	m = drain(t, cur.(*Model), cmd)

	if m.mode != modeRead {
		t.Fatalf("mode after ctrl+m = %v, want modeRead; status=%q -- ctrl+m must commit exactly like enter", m.mode, m.status)
	}
	if got := e.planNow(t).SourceHint; got != moved {
		t.Fatalf("SourceHint = %q, want %q -- the ctrl+m commit never landed", got, moved)
	}
}

// TestTheRefusalSurvivesWhereTheStatusBarWouldHaveClipped reads the refusal off
// the RENDERED FRAME. Asserting it on m.status stays true while the screen shows
// nothing, because the status row is statusIdentity plus block/thread counts plus
// m.status, ansi.Truncate'd to the bar's width: a test that reads m.status and
// stops reproduces the defect rather than catching it.
//
// THE TITLE IS LONG ENOUGH TO PROVE THE CLIP ANALYTICALLY, not by inference:
// statusIdentity() alone is asserted to reach or exceed the terminal's own width
// as a TEST SETUP check, before the refusal is even considered -- so a refusal
// appended to m.status would have every one of its bytes discarded by
// ansi.Truncate, whatever they were. What is then asserted is that the STATUS ROW
// ITSELF (the frame's second-to-last row) never carries the refusal, while the
// box elsewhere in the very same frame does.
//
// TWO WIDTHS, NOT ONE: 60 is narrow enough that the title alone clips the bar
// (asserted as test setup, below); 200 is wide enough that it never would -- the
// SAME long title leaves statusIdentity() short of 200 cells, so that case
// proves the box's own behaviour does not depend on whether the bar had room.
func TestTheRefusalSurvivesWhereTheStatusBarWouldHaveClipped(t *testing.T) {
	// deleteConfirmInterior's own overflow fixture (sourcefault_test.go,
	// TestTheDeleteConfirmIsTheSameBoxPanel1WasIn): 126 cells, long enough to
	// fill a 60-column status bar on its own.
	longTitle := strings.Repeat("Very Long Plan Title ", 6)

	for _, tc := range []struct {
		width     int
		wantClips bool
	}{
		{60, true},
		{200, false},
	} {
		t.Run(strconv.Itoa(tc.width)+" columns", func(t *testing.T) {
			e := newRepointEnv(t)
			e.m.title = longTitle
			e.m.width = tc.width
			e.m.rerender()

			barWouldClip := ansi.StringWidth(e.m.statusIdentity()) >= tc.width
			if barWouldClip != tc.wantClips {
				t.Fatalf("test setup: statusIdentity() is %d cells against a %d-cell bar, want reaching-or-exceeding-it=%v", ansi.StringWidth(e.m.statusIdentity()), tc.width, tc.wantClips)
			}

			m := press(e.m, "f")
			m = pressRepointCommit(t, m) // the field is empty: repointTarget's own "path is required"

			if m.mode != modeRepoint {
				t.Fatalf("mode = %v, want modeRepoint -- a refused commit leaves the pane open to edit", m.mode)
			}
			if m.status != "" {
				t.Fatalf("status = %q, want empty -- the refusal belongs in the box and not on the bar", m.status)
			}

			screen := ansi.Strip(m.View().Content)
			rows := strings.Split(screen, "\n")
			if len(rows) < 2 {
				t.Fatalf("test setup: frame has %d rows, want at least 2 (status row, help bar)", len(rows))
			}
			statusRow := rows[len(rows)-2]
			wantLine := repointRefusalGlyph + "path is required"
			if strings.Contains(statusRow, "path is required") {
				t.Fatalf("the status row itself carries the refusal, which this test's own point is that it must not: %q", statusRow)
			}
			if !boxContainsRefusal(t, screen, wantLine) {
				t.Fatalf("the box never shows %q at %d columns:\n%s", wantLine, tc.width, screen)
			}
		})
	}
}

// TestTheEmptyPathRefusalIsPinnedVerbatim exists because every OTHER
// assertion in this suite builds its wantLine as repointRefusalGlyph + "...", so
// a change to the glyph constant and a change to the code that renders it move
// together and the comparison agrees with itself either way: changing
// repointRefusalGlyph from "⚠ " to "!" leaves the whole ./app/ suite green, and
// boxContainsRefusal's own whitespace-stripping additionally passes
// "⚠path is required" with the space missing.
//
// SO THIS ONE PINS THE BYTE STRING, ONCE, HARD-CODED RATHER THAN BUILT, and
// nothing about repointRefusalGlyph or repointTarget's error text is read back
// into the comparison. A plain strings.Contains on the raw, ansi-stripped frame
// is deliberately NOT boxContainsRefusal: the pinned sentence is far under
// this pane's own floor (repointMinInterior, 40), so it cannot wrap at the one
// width this test drives and the whitespace-tolerant helper's laxity is not
// needed.
//
// ONE CHECK IS ENOUGH: repointBody is the one function that composes the
// wording, and the source-fault panel's f (enterPathInput) is the one door into
// the pane that draws it.
func TestTheEmptyPathRefusalIsPinnedVerbatim(t *testing.T) {
	e := newRepointEnv(t)
	m := press(e.m, "f")
	m = pressRepointCommit(t, m) // the field is empty

	screen := ansi.Strip(m.View().Content)
	if !strings.Contains(screen, "⚠ path is required") {
		t.Fatalf("the box never renders the pinned text \"⚠ path is required\" verbatim:\n%s", screen)
	}
}

// TestRepointRefusesAndWritesNothing covers two refusals plus a third: a path
// some OTHER plan already claims.
//
// WHAT IS ASSERTED IS THE ABSENCE OF THE WRITE, not only the presence of the
// message -- a refusal that has already moved the hint is the defect wearing an
// error. Both stores are checked: this plan still names the file that is gone,
// and the claimant still owns its own.
//
// THE MESSAGE IS READ OFF THE RENDERED FRAME, not m.status, which stays true
// while the screen shows nothing (see
// TestTheRefusalSurvivesWhereTheStatusBarWouldHaveClipped). m.status is asserted
// EMPTY alongside it, which is the mutation this pins: a change that left the
// OLD write in place too, duplicating the refusal rather than relocating it,
// would still pass a bare "the frame shows it" check.
//
// THE PANE STAYS OPEN AND KEEPS WHAT WAS TYPED, which is the same affordance the
// prefill exists for one keystroke earlier: a refusal that dropped back to the
// panel would make the second attempt a retype.
func TestRepointRefusesAndWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func(t *testing.T, e repointEnv) string
		want   func(target string) string
	}{
		{
			name: "a path that does not exist",
			target: func(t *testing.T, e repointEnv) string {
				return filepath.Join(filepath.Dir(e.f.path), "never-written.md")
			},
			want: func(target string) string {
				return target + " does not exist -- re-point never creates a file"
			},
		},
		{
			name: "a directory",
			target: func(t *testing.T, e repointEnv) string {
				dir := filepath.Join(filepath.Dir(e.f.path), "plans")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			want: func(target string) string {
				return target + " is a directory, not a file"
			},
		},
		{
			name: "a file another plan already claims",
			target: func(t *testing.T, e repointEnv) string {
				other := seedListPlan(t, e.f, filepath.Dir(e.f.path), "other.md", "Cache Warming", repointDoc)
				return other.Path
			},
			want: func(target string) string {
				return target + ` is already followed by another plan: "Cache Warming". Only one plan can follow a file in Draftplane.`
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRepointEnv(t)
			target := tc.target(t, e)

			m := press(e.m, "f")
			m.pathTA.SetValue(target)
			m = pressRepointCommit(t, m)

			if m.mode != modeRepoint {
				t.Fatalf("mode = %v, want modeRepoint -- a refused commit leaves the pane open to edit", m.mode)
			}
			if got := m.pathTA.Value(); got != target {
				t.Fatalf("pathTA = %q, want the typed path %q still in hand", got, target)
			}
			if m.status != "" {
				t.Fatalf("status = %q, want empty -- the refusal belongs in the box and not on the bar", m.status)
			}
			screen := ansi.Strip(m.View().Content)
			wantLine := repointRefusalGlyph + tc.want(target)
			if !boxContainsRefusal(t, screen, wantLine) {
				t.Fatalf("the box never shows %q:\n%s", wantLine, screen)
			}
			if got := e.planNow(t).SourceHint; got != e.seeded.Path {
				t.Fatalf("SourceHint = %q, want it still naming the missing file %q -- a refusal writes nothing", got, e.seeded.Path)
			}
			claimant, err := e.f.svc.ResolvePlan(e.f.ctx, target)
			if err == nil && claimant.ID == e.seeded.Plan.ID {
				t.Fatalf("%q now resolves to the plan that was refused it", target)
			}
		})
	}
}

// TestRepointAtThePathThisPlanAlreadyClaimsIsAccepted pins the self-match rule
// at the door that first spends it, and it is a real journey rather than a
// contrivance: the file comes back where it was -- restored from a backup, a
// branch checked out again -- and the reader presses f and TYPES the path the
// plan already claims, the pane not opening on it.
//
// An any-claimant guard refuses exactly this, because the plan's own SourceHint
// still names that path: the panel's whole population is plans that claim a path
// nothing answers at.
func TestRepointAtThePathThisPlanAlreadyClaimsIsAccepted(t *testing.T) {
	e := newRepointEnv(t)
	if err := os.WriteFile(e.seeded.Path, []byte(repointMovedDoc), 0o644); err != nil {
		t.Fatal(err)
	}

	m := press(e.m, "f")
	for _, ch := range e.seeded.Path {
		m = press(m, string(ch))
	}
	m = pressRepointCommit(t, m)

	if m.mode != modeRead {
		t.Fatalf("mode = %v, want modeRead; status=%q -- a plan re-pointed at its own path is not a conflict", m.mode, m.status)
	}
	if m.sess.SourceFault != nil || m.sess.FromSnapshot() {
		t.Fatalf("the session did not start following the restored file (fault=%v fromSnapshot=%v)", m.sess.SourceFault, m.sess.FromSnapshot())
	}
	if string(m.sess.Content) != repointMovedDoc {
		t.Fatalf("session content = %q, want the restored file's bytes", m.sess.Content)
	}
	if got := e.planNow(t).SourceHint; got != e.seeded.Path {
		t.Fatalf("SourceHint = %q, want the path unchanged at %q", got, e.seeded.Path)
	}
}

// TestEscFromTheRepointPaneHandsPanel1Back pins where esc lands from this
// pane, back on Panel 1, and the reason is simple: cancelling a re-point
// writes nothing, the file is still missing, and Panel 1's question is still
// open -- while New is the panel's only other entry, so dropping to read mode
// here would leave a reader in the document of a plan whose file is gone with
// no key anywhere that re-opens the panel.
func TestEscFromTheRepointPaneHandsPanel1Back(t *testing.T) {
	e := newRepointEnv(t)
	panel := e.m.confirm

	m := press(e.m, "f")
	m.pathTA.SetValue("/should/never/be/used.md")
	m = press(m, "esc")

	if m.mode != modeSourceFault {
		t.Fatalf("mode after esc = %v, want modeSourceFault", m.mode)
	}
	if m.confirm != panel {
		t.Fatalf("confirm = %q, want Panel 1's own body back %q", m.confirm, panel)
	}
	if m.pathTA.Value() != "" {
		t.Errorf("pathTA after esc = %q, want reset", m.pathTA.Value())
	}
	if got := e.planNow(t).SourceHint; got != e.seeded.Path {
		t.Fatalf("SourceHint = %q, want it untouched at %q", got, e.seeded.Path)
	}
}

// TestTheRefusalClearsOnEditAndOnReopen pins the clearing rule: "the refusal
// line is absent until a refusal happens, and is cleared when the reader edits
// the field or re-opens the pane". A refusal that survived a correction would
// sit under a path that might now be right.
//
// TWO INDEPENDENT TRIGGERS, PROVEN INDEPENDENTLY. Phase 1 commits a refusal and
// then ONLY edits (never leaving the pane), proving updatePanel's modeRepoint
// default arm clears it. Phase 2 commits a FRESH refusal and then re-opens
// WITHOUT editing (esc, f, with nothing typed in between), proving
// enterPathInput's own clear is what does the work there -- a version that
// cleared on edit alone and never on re-open would otherwise still pass.
func TestTheRefusalClearsOnEditAndOnReopen(t *testing.T) {
	e := newRepointEnv(t)

	// PHASE 1: commit a refusal, then edit -- and only edit.
	m := press(e.m, "f")
	m = pressRepointCommit(t, m) // the field is empty: repointTarget's own "path is required"
	if m.repointRefusal == "" {
		t.Fatalf("test setup: no refusal standing after an empty commit")
	}
	screen := ansi.Strip(m.View().Content)
	wantLine := repointRefusalGlyph + "path is required"
	if !boxContainsRefusal(t, screen, wantLine) {
		t.Fatalf("test setup: the box never shows the refusal before the edit meant to clear it:\n%s", screen)
	}
	m = press(m, "x")
	if m.repointRefusal != "" {
		t.Fatalf("repointRefusal after an edit = %q, want empty", m.repointRefusal)
	}
	screen = ansi.Strip(m.View().Content)
	if boxContainsRefusal(t, screen, "path is required") {
		t.Fatalf("the box still shows the refusal after an edit:\n%s", screen)
	}

	// PHASE 2: back to Panel 1, commit a FRESH refusal, then re-open without
	// editing in between.
	m = press(m, "esc")
	if m.mode != modeSourceFault {
		t.Fatalf("mode after esc = %v, want modeSourceFault", m.mode)
	}
	m = press(m, "f")
	m = pressRepointCommit(t, m) // empty again: a fresh refusal
	if m.repointRefusal == "" {
		t.Fatalf("test setup: no refusal standing after the second empty commit")
	}
	m = press(m, "esc")
	m = press(m, "f") // re-open; nothing typed in between
	if m.repointRefusal != "" {
		t.Fatalf("repointRefusal after a re-open = %q, want empty", m.repointRefusal)
	}
	screen = ansi.Strip(m.View().Content)
	if boxContainsRefusal(t, screen, "path is required") {
		t.Fatalf("the box still shows the refusal after a re-open:\n%s", screen)
	}
}

// TestRepointIsRefusedWhileTheDeleteItAuthorizedIsStillRunning is Panel 1's
// other in-flight arm, beside esc's. It is a weaker refusal -- no race is being
// closed, because f opens a pane and writes nothing -- but it is deliberate: d,
// y, then f leaves a path pane standing over a plan whose destruction is already
// dispatched, and the commit that pane exists for would be refused by its own
// dispatchOK a moment later.
//
// The delete's cmd is deliberately NOT drained, which is what holds the model in
// the state the real gesture occupies while the write is outstanding.
func TestRepointIsRefusedWhileTheDeleteItAuthorizedIsStillRunning(t *testing.T) {
	e := newRepointEnv(t)

	m := press(e.m, "d")
	cur, cmd := m.Update(keyYes)
	m = cur.(*Model)
	if cmd == nil || !m.inFlight {
		t.Fatalf("test setup: y must dispatch and leave the write in flight (cmd=%v inFlight=%v)", cmd, m.inFlight)
	}
	panel := m.confirm

	m = press(m, "f")
	if m.mode != modeSourceFault {
		t.Fatalf("mode = %v, want modeSourceFault -- f must not open a pane over a plan being destroyed", m.mode)
	}
	if m.confirm != panel {
		t.Fatalf("confirm = %q, want the panel body untouched", m.confirm)
	}
	if m.status != "action in progress" {
		t.Fatalf("status = %q, want dispatchOK's own refusal on screen", m.status)
	}
}

// TestAFailedRepointKeepsPanel1Standing pins one of the outcomes
// msgActionDone.fromRepoint's doc names: a failed write leaves the old fault
// latched and the panel owed. A gesture that dropped to read mode BEFORE
// dispatching leaves the reader in the document of a plan whose file is still
// missing -- handleActionDone's error branch writes a status and nothing else
// -- with Panel 1 gone and no key anywhere that re-opens it (New is its only
// other entry). That is the "read it and decide later" this product forbids,
// reached by a write that did not land.
//
// THE PANEL BODY IS ASSERTED, not merely the mode: the session was never
// swapped, so the words a reader is left with must still be the ones they were
// reading, naming the path that is still missing.
func TestAFailedRepointKeepsPanel1Standing(t *testing.T) {
	var spy *repointSpy
	e := newRepointEnvOver(t, func(real client.PlanService) client.PlanService {
		spy = &repointSpy{PlanService: real, writeErr: errors.New("state.json is read-only")}
		return spy
	})
	moved := writeRepointTarget(t, e, "moved.md", repointMovedDoc)
	panel := e.m.confirm

	m := press(e.m, "f")
	m.pathTA.SetValue(moved)
	m = pressRepointCommit(t, m)

	if m.mode != modeSourceFault {
		t.Fatalf("mode after a failed re-point = %v, want modeSourceFault -- nothing else re-opens the panel", m.mode)
	}
	if m.confirm != panel {
		t.Fatalf("confirm = %q, want the body the reader was already looking at %q", m.confirm, panel)
	}
	if !strings.Contains(m.status, "state.json is read-only") {
		t.Fatalf("status = %q, want the write's own failure", m.status)
	}
	if got := e.planNow(t).SourceHint; got != e.seeded.Path {
		t.Fatalf("SourceHint = %q, want it untouched at %q", got, e.seeded.Path)
	}
}

// TestAPathClaimantStoreFailureShowsInTheBoxAndClearsAnyStaleBar covers
// client.PathClaimant's err != nil arm ("the store could not answer at all").
// Its sentence ("error: "+err.Error()) is drawn in the box, on the SAME
// m.repointRefusal field the other three refusals use and unreworded: "a panel
// wraps where a status bar clips" is about WHERE a sentence is drawn, not who
// wrote its words.
//
// PHASE 2 IS THE SHARPER HALF: unless a box refusal clears m.status, a failed
// attempt's "error: ..." can still be standing on the bar under a LATER box
// refusal that has nothing to do with it -- the bar and the box disagreeing on
// screen at once. Phase 1 drives the store failure and phase 2 immediately
// drives an ordinary validation refusal (an empty field) without ever clearing
// the field by hand, proving the SECOND refusal's own commit clears whatever the
// first left on the bar.
func TestAPathClaimantStoreFailureShowsInTheBoxAndClearsAnyStaleBar(t *testing.T) {
	var spy *repointSpy
	e := newRepointEnvOver(t, func(real client.PlanService) client.PlanService {
		spy = &repointSpy{PlanService: real}
		return spy
	})
	moved := writeRepointTarget(t, e, "moved.md", repointMovedDoc)

	// PHASE 1: repointTarget accepts the path (it is a real file), then
	// PathClaimant's own ResolvePlan call fails outright.
	m := press(e.m, "f")
	m.pathTA.SetValue(moved)
	spy.resolveErr = errors.New("the store could not be read")
	m = pressRepointCommit(t, m)

	if m.mode != modeRepoint {
		t.Fatalf("mode = %v, want modeRepoint -- a refused commit leaves the pane open to edit", m.mode)
	}
	if m.status != "" {
		t.Fatalf("status after the store failure = %q, want empty -- it belongs in the box now", m.status)
	}
	wantStoreErr := repointRefusalGlyph + "error: the store could not be read"
	screen := ansi.Strip(m.View().Content)
	if !boxContainsRefusal(t, screen, wantStoreErr) {
		t.Fatalf("the box never shows %q after a store failure:\n%s", wantStoreErr, screen)
	}

	// PHASE 2 INJECTS A STALE m.status DIRECTLY rather than relying on phase 1 to
	// have left something behind: phase 1's store-failure branch already clears
	// m.status as part of the SAME fix this phase means to isolate, so by the time
	// phase 2 runs m.status is "" regardless of whether repointTarget's own err arm
	// (below) clears it too -- a version that dropped only that arm's `m.status =
	// ""` would still pass. Setting it by hand, decoupled from any one refusal
	// site, is what makes phase 2 a test of THIS arm's clear rather than phase 1's.
	m.status = "some earlier, unrelated status line"
	m.pathTA.Reset()
	m = pressRepointCommit(t, m)

	if got := m.repointRefusal; got != "path is required" {
		t.Fatalf("repointRefusal after the second commit = %q, want %q", got, "path is required")
	}
	if m.status != "" {
		t.Fatalf("status after the second refusal = %q, want empty -- a box refusal must clear whatever the bar was showing", m.status)
	}
	screen = ansi.Strip(m.View().Content)
	if boxContainsRefusal(t, screen, "the store could not be read") {
		t.Fatalf("the box still shows the first refusal's text after a second, unrelated one fired:\n%s", screen)
	}
	wantSecond := repointRefusalGlyph + "path is required"
	if !boxContainsRefusal(t, screen, wantSecond) {
		t.Fatalf("the box never shows the second refusal %q:\n%s", wantSecond, screen)
	}

	// PHASE 3 IS THE THIRD REFUSAL SITE, the "taken" arm -- proven by the same
	// inject-then-fire technique, so its own `m.status = ""` is provable by
	// mutation rather than merely present: phases 1 and 2 between them leave
	// m.status at "" already, so reusing that ambient state would pass whether or
	// not this arm's clear still existed.
	other := seedListPlan(t, e.f, filepath.Dir(e.f.path), "other.md", "Cache Warming", repointDoc)
	spy.resolveErr = nil // phase 1's own knob, disarmed so PathClaimant answers for real
	m.status = "some earlier, unrelated status line"
	m.pathTA.SetValue(other.Path)
	m = pressRepointCommit(t, m)

	wantThird := other.Path + ` is already followed by another plan: "Cache Warming". Only one plan can follow a file in Draftplane.`
	if got := m.repointRefusal; got != wantThird {
		t.Fatalf("repointRefusal after the third commit = %q, want %q", got, wantThird)
	}
	if m.status != "" {
		t.Fatalf("status after the third refusal = %q, want empty -- a box refusal must clear whatever the bar was showing", m.status)
	}
}

// TestARepointOntoAFileThatVanishesRaisesThePanelAgain pins another of the
// outcomes msgActionDone.fromRepoint's doc names, and the one a
// success/failure test cannot reach: the write LANDS, and the file it now
// names is gone by the time the re-open reads it. The session installed is
// therefore back on the snapshot with a NEW fault latched, under a status that
// truthfully says the plan now follows that path -- so keying the outcome on
// "did the write succeed" would leave the reader in read mode over a plan
// whose file is missing all over again.
//
// THE PANEL MUST NAME THE NEW PATH. Re-deriving the body from the reloaded
// session is what makes that true; leaving the panel standing from before the
// dispatch would show the old path, which is no longer the one that is missing.
//
// The window is the product's own and cannot be closed from app (see
// runRepoint); repointSpy.afterWrite is what stands in it deterministically.
func TestARepointOntoAFileThatVanishesRaisesThePanelAgain(t *testing.T) {
	var moved string
	e := newRepointEnvOver(t, func(real client.PlanService) client.PlanService {
		return &repointSpy{PlanService: real, afterWrite: func() { mustRemove(t, moved) }}
	})
	moved = writeRepointTarget(t, e, "moved.md", repointMovedDoc)

	m := press(e.m, "f")
	m.pathTA.SetValue(moved)
	m = pressRepointCommit(t, m)

	if got := e.planNow(t).SourceHint; got != moved {
		t.Fatalf("SourceHint = %q, want %q -- this test is about a write that LANDED", got, moved)
	}
	if m.sess.SourceFault == nil {
		t.Fatalf("test setup: the reloaded session carries no fault, so there is nothing for this test to answer")
	}
	if m.mode != modeSourceFault {
		t.Fatalf("mode = %v, want modeSourceFault -- the panel is up if and only if a fault is latched", m.mode)
	}
	if !strings.Contains(m.confirm, moved) {
		t.Fatalf("panel body = %q, want it naming the file that is missing NOW (%q)", m.confirm, moved)
	}
	if strings.Contains(m.confirm, e.seeded.Path) {
		t.Fatalf("panel body = %q, still naming the OLD path -- the body must be re-derived from the session that came back", m.confirm)
	}
}

// TestRepointReopensThisPlanAndNotWhateverAnswersAtThePath pins runRepoint's
// re-open going through session.OpenPlan rather than session.Open.
//
// WHAT IT DRIVES is the window runRepoint's own comment names: the path stopped
// resolving to this plan between the claim check and the re-open. session.Open
// resolves by SOURCE and has no identity to check against, so it hands back
// whatever answers there -- a DIFFERENT plan's session, installed under a
// success message saying this one now follows the file. OpenPlan compares,
// misses, and falls back to this plan's own id.
//
// The double poisons AFTER the claim check rather than always, which is what
// makes the state reachable: an always-poisoned resolve is refused by the claim
// check as a path another plan claims and never reaches the write.
//
// It also pins a further case of the rule msgActionDone.fromRepoint's doc
// states -- the outcome keys on the reloaded session's fault: the write
// succeeds and the fallback latches a claimed fault, and Panel 1 now opens
// over it with the pinned text -- not, as this test once asserted, silence
// (the original claim that this carrier could never see claimed was false; see
// sourceFaultText's own doc for when claimed and released gained their
// panels).
func TestRepointReopensThisPlanAndNotWhateverAnswersAtThePath(t *testing.T) {
	var spy *repointSpy
	e := newRepointEnvOver(t, func(real client.PlanService) client.PlanService {
		spy = &repointSpy{PlanService: real}
		return spy
	})
	moved := writeRepointTarget(t, e, "moved.md", repointMovedDoc)
	interloper := seedListPlan(t, e.f, filepath.Dir(e.f.path), "interloper.md", "Cache Warming", repointDoc)

	m := press(e.m, "f")
	m.pathTA.SetValue(moved)
	// One truthful answer for the claim check on the loop, then the
	// interloper for the re-open inside the cmd.
	spy.arm(1, interloper.Plan)
	m = pressRepointCommit(t, m)

	if !spy.poisoned() {
		t.Fatalf("test setup: ResolvePlan was called %d times, so the re-open never reached the poisoned answer", spy.resolves)
	}
	if m.sess.Plan.ID != e.seeded.Plan.ID {
		t.Fatalf("the review is now on plan %q, want %q -- a success message must never ride over a different plan's session",
			m.sess.Plan.ID, e.seeded.Plan.ID)
	}
	if m.sess.SourceFault == nil || m.sess.SourceFault.State != session.SourceFileClaimed {
		t.Fatalf("SourceFault = %v, want the claimed state OpenPlan classifies this as", m.sess.SourceFault)
	}
	if m.mode != modeSourceFault {
		t.Fatalf("mode = %v, want modeSourceFault -- Panel 1 has pinned words for claimed now and must open over it", m.mode)
	}
	if want := sourceFaultText(m.sess.SourceFault); m.confirm != want {
		t.Fatalf("panel body = %q, want %q", m.confirm, want)
	}
	// The fault's own Plan names the interloper, and the
	// panel must not spend it.
	if strings.Contains(m.confirm, interloper.Plan.Title) {
		t.Fatalf("the panel names the conflicting plan (%q), which the panel must not:\n%s", interloper.Plan.Title, m.confirm)
	}
}

// writeRepointTarget creates a file beside the fixture's own and returns its
// path -- the target every re-point test above commits.
func writeRepointTarget(t *testing.T, e repointEnv, name, doc string) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(e.f.path), name)
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// pressO drives o and drains the write it dispatches (runRelease) -- like
// pressRepointCommit, because o's write is async like every other
// write in this file and press alone would leave its cmd undispatched.
func pressO(t *testing.T, m *Model) *Model {
	t.Helper()
	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	return drain(t, cur.(*Model), cmd)
}

// TestOReleasesTheHintAndKeepsThePlan pins the release valve end to end: o
// writes the same field f does, empty, then reopens through the same door
// (session.OpenPlan) runRepoint uses -- so the plan comes back a
// Draftplane-only plan, its threads and its most recent version untouched.
//
// THE STATUS LINE IS ASSERTED WHOLE and not by substring: snapshotOpenStatus
// (app/root.go) is keyed on path == "", and is
// UNREACHABLE while SourceHint is still set -- which is exactly the fact o's
// write, and nothing else, makes true.
//
// AND IT REOPENS, on TestRepointFollowsTheNewFileAndKeepsThePlan's own reason:
// the model's own session is one thing, what the NEXT open finds is another, and
// a released plan must not raise the panel it just closed.
func TestOReleasesTheHintAndKeepsThePlan(t *testing.T) {
	e := newRepointEnv(t)

	m := pressO(t, e.m)

	if m.mode != modeRead {
		t.Fatalf("mode after o = %v, want modeRead -- a released plan has no fault", m.mode)
	}
	if got, want := m.status, "reviewing from draftplane"; got != want {
		t.Fatalf("status after o = %q, want %q", got, want)
	}
	if m.sess.Plan.ID != e.seeded.Plan.ID {
		t.Fatalf("plan id = %q, want the plan the review started on (%q)", m.sess.Plan.ID, e.seeded.Plan.ID)
	}
	if got := e.planNow(t).SourceHint; got != "" {
		t.Fatalf("SourceHint = %q, want it released to empty", got)
	}
	threads, err := e.f.svc.Threads(e.f.ctx, e.seeded.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 || threads[0].ID != e.thread.ID {
		t.Fatalf("threads = %+v, want the one thread this plan already had (%s)", threads, e.thread.ID)
	}

	reopened, err := session.OpenPlan(e.f.ctx, e.f.svc, e.planNow(t))
	if err != nil {
		t.Fatal(err)
	}
	if reopened.SourceFault != nil {
		t.Fatalf("reopening raises %v -- a released plan must not re-fire the panel", reopened.SourceFault)
	}
	if reopened.Path != "" {
		t.Fatalf("reopened path = %q, want empty -- the plan follows no file", reopened.Path)
	}
}

// TestOIsRefusedWhileTheDeleteItAuthorizedIsStillRunning is o's own copy of
// TestRepointIsRefusedWhileTheDeleteItAuthorizedIsStillRunning and
// TestEscIsRefusedWhileTheDeleteItAuthorizedIsStillRunning: the one write
// that can already be in flight while this panel is up is the delete d
// authorized, and o must refuse rather than race it.
func TestOIsRefusedWhileTheDeleteItAuthorizedIsStillRunning(t *testing.T) {
	e := newRepointEnv(t)

	m := press(e.m, "d")
	cur, cmd := m.Update(keyYes)
	m = cur.(*Model)
	if cmd == nil || !m.inFlight {
		t.Fatalf("test setup: y must dispatch and leave the write in flight (cmd=%v inFlight=%v)", cmd, m.inFlight)
	}
	panel := m.confirm

	m = press(m, "o")
	if m.mode != modeSourceFault {
		t.Fatalf("mode = %v, want modeSourceFault -- o must not release the hint out from under a plan being destroyed", m.mode)
	}
	if m.confirm != panel {
		t.Fatalf("confirm = %q, want the panel body untouched", m.confirm)
	}
	if m.status != "action in progress" {
		t.Fatalf("status = %q, want dispatchOK's own refusal on screen", m.status)
	}
	if got := e.planNow(t).SourceHint; got != e.seeded.Path {
		t.Fatalf("SourceHint = %q, want it untouched at %q -- the refused o must write nothing", got, e.seeded.Path)
	}
}

// repointPane opens Panel 1 over a gone file and presses f, returning the
// model with the centred re-point pane up. It is the REAL door -- the panel's
// own key through updatePanel -- rather than a mode set by hand, because the
// pane's focus and its emptiness are both things that door does and a
// constructed model would have neither.
func repointPane(t *testing.T, preset string) *Model {
	t.Helper()
	th, err := theme.Lookup(preset)
	if err != nil {
		t.Fatal(err)
	}
	m := faultedModel(t, &session.SourceFileError{Path: panelPath, State: session.SourceFileGone, Err: fs.ErrNotExist})
	m.styles = ui.NewStyles(th)
	m.rerender()
	return press(m, "f")
}

// TestTheRepointPaneIsTheBoxPanel1WasIn asserts a RELATIONSHIP between two
// screens rather than geometry of its own: f is pressed on a box that is already
// centred, and the answer it asks for has to appear in that box.
//
// THE WIDTH AND THE COLUMN ARE THE ASSERTION AND THE HEIGHT IS NOT. The pane
// says less than the panel it replaces, so the box is shorter and re-centres
// vertically; what would be disorienting is the box moving SIDEWAYS or changing
// size under the reader at the moment it asks them to type, and that is what
// repointInterior exists to prevent. Measured off the two drawn frames, so a
// change to either body moves both numbers together or fails.
//
// THE THREE NEGATIVES ARE THE RESERVATION CHECK, the same three
// TestPanel1DrawsThePinnedTextWholeAtEightyColumns makes for the panel: a
// centred mode must reserve NO row (viewHeight unchanged from read mode), draw
// NO strip (panelViewPainted empty) and leave the frame exactly m.height rows.
// Keeping only the last would let a half-finished move pass -- a strip still
// drawn under a reservation that no longer exists, or the reverse.
func TestTheRepointPaneIsTheBoxPanel1WasIn(t *testing.T) {
	m := faultedModel(t, &session.SourceFileError{Path: panelPath, State: session.SourceFileGone, Err: fs.ErrNotExist})
	readVH := 0
	func() {
		mode := m.mode
		m.mode = modeRead
		readVH = m.viewHeight()
		m.mode = mode
	}()
	panelX, _, panelW, _ := boxGeometry(t, ansi.Strip(m.View().Content))

	m = press(m, "f")
	if m.mode != modeRepoint {
		t.Fatalf("mode after f = %v, want modeRepoint", m.mode)
	}
	screen := ansi.Strip(m.View().Content)
	paneX, _, paneW, _ := boxGeometry(t, screen)
	if paneW != panelW || paneX != panelX {
		t.Fatalf("the box is %d cells at column %d under the pane, want the panel's own %d at %d -- pressing f must not resize or move the box the reader is looking at",
			paneW, paneX, panelW, panelX)
	}
	if got := m.viewHeight(); got != readVH {
		t.Fatalf("viewHeight() under the pane = %d, want read mode's %d -- a centred pane displaces no document row", got, readVH)
	}
	if got, _ := m.panelViewPainted(); got != "" {
		t.Fatalf("panelViewPainted() under the pane = %q, want nothing -- a strip as well as the box puts the same question on screen twice", got)
	}
	rows := strings.Split(screen, "\n")
	if len(rows) != m.height {
		t.Fatalf("the frame is %d rows, want m.height (%d)", len(rows), m.height)
	}
	for i, r := range rows {
		if got := ansi.StringWidth(r); got != m.width {
			t.Fatalf("frame row %d is %d cells, want m.width (%d)", i, got, m.width)
		}
	}
}

// repointLongBreakFreePath is a nonexistent absolute path that is long AND has
// nothing for ansi.Wordwrap to break on: no "-" and no space, and "/" is
// deliberately NOT one of Wordwrap's own two breakpoints. It is the ONLY case
// that can prove repointBody wraps the refusal to the box's own interior rather
// than to some wider budget: repointTarget's other sentences ("path is
// required" among them) fit on one row at every width this suite runs at
// regardless of the wrap budget, so a version that stopped wrapping the refusal
// at all would still pass a test built on one of those.
const repointLongBreakFreePath = "/nonexistent/verylongsegmentwithnobreaksatall/anothersegmentthatdoesnotbreakeither/keepsgoingandgoingwithnohyphen/file.md"

// TestTheRepointPaneWithARefusalStillHoldsPanel1sWidth is
// TestTheRepointPaneIsTheBoxPanel1WasIn's own claim, one refusal further in:
// repointInterior derives the box's width from the PRECEDING panel's OWN lines,
// never from repointBody's content, so a refusal's extra row must not be able to
// feed back into it. THE HEIGHT IS DELIBERATELY NOT PINNED, on that test's own
// precedent -- an extra row is exactly what a standing refusal adds.
//
// THE REFUSAL IS THE LONG, BREAK-FREE PATH (repointLongBreakFreePath) AND NOT
// THE SHORT "path is required" ONE: handing repointBody's own wrapConfirmGroups
// call a budget ten times the real interior leaves this test GREEN against the
// short refusal (it fits on one row at any budget) and RED only against this
// one -- proof the short case cannot stand in for it.
func TestTheRepointPaneWithARefusalStillHoldsPanel1sWidth(t *testing.T) {
	m := faultedModel(t, &session.SourceFileError{Path: panelPath, State: session.SourceFileGone, Err: fs.ErrNotExist})
	panelX, _, panelW, _ := boxGeometry(t, ansi.Strip(m.View().Content))

	m = press(m, "f")
	m.pathTA.SetValue(repointLongBreakFreePath)
	m = pressRepointCommit(t, m)
	if m.repointRefusal == "" {
		t.Fatalf("test setup: no refusal standing after committing a path that does not exist")
	}

	screen := ansi.Strip(m.View().Content)
	paneX, _, paneW, _ := boxGeometry(t, screen)
	if paneW != panelW || paneX != panelX {
		t.Fatalf("the box is %d cells at column %d with a refusal standing, want the panel's own %d at %d -- the refusal row must not resize the box away from the panel it replaced",
			paneW, paneX, panelW, panelX)
	}
	wantLine := repointRefusalGlyph + repointLongBreakFreePath + " does not exist -- re-point never creates a file"
	if !boxContainsRefusal(t, screen, wantLine) {
		t.Fatalf("test setup: the box never shows the refusal at all:\n%s", screen)
	}
}

// TestTheDeleteConfirmDrawsAsACentredBoxNotAStrip covers the routing half:
// modeConfirmDeletePlan is named in this model's mode enum and answered by
// updatePanel's "d" arm, but unless drawsCentredPanel names it too it draws as a
// bottom-anchored strip while every other centred panel in this file is a box.
//
// REACHED THROUGH THE REAL DOOR -- Panel 1, then d -- and not by setting the
// mode by hand (repointPane's doc comment): a constructed model skips the very
// routing this asserts.
//
// THE THREE NEGATIVES ARE TestPanel1DrawsThePinnedTextWholeAtEightyColumns'S
// OWN, asked of the mode one keystroke further in: a centred mode reserves no
// row, draws no strip and leaves the frame exactly m.height rows of m.width
// cells. PLUS a fourth this file's other centred tests do not need: that the
// body actually renders as a BOX (boxGeometry, which fatals outright -- "no
// bordered box in the frame" -- if it does not). That fourth is what the three
// negatives cannot catch, because a panelViewPainted switch that simply never
// reaches modeConfirmDeletePlan's case draws nothing either, and nothing is also
// what the three negatives want to see.
func TestTheDeleteConfirmDrawsAsACentredBoxNotAStrip(t *testing.T) {
	m := faultedModel(t, &session.SourceFileError{Path: panelPath, State: session.SourceFileGone, Err: fs.ErrNotExist})
	readVH := 0
	func() {
		mode := m.mode
		m.mode = modeRead
		readVH = m.viewHeight()
		m.mode = mode
	}()

	m = press(m, "d")
	if m.mode != modeConfirmDeletePlan {
		t.Fatalf("mode after d = %v, want modeConfirmDeletePlan", m.mode)
	}

	screen := ansi.Strip(m.View().Content)
	// boxGeometry itself is the fourth assertion: it fatals if the panel drew
	// as the old strip (or drew nothing) instead of a bordered box.
	boxGeometry(t, screen)

	if got, _ := m.panelViewPainted(); got != "" {
		t.Fatalf("panelViewPainted() = %q, want nothing -- a strip as well as the box puts the same question on screen twice", got)
	}
	if got := m.viewHeight(); got != readVH {
		t.Fatalf("viewHeight() under the panel = %d, want read mode's %d -- a composited panel reserves no document row", got, readVH)
	}
	rows := strings.Split(screen, "\n")
	if len(rows) != m.height {
		t.Fatalf("the frame is %d rows, want m.height (%d)", len(rows), m.height)
	}
	for i, r := range rows {
		if got := ansi.StringWidth(r); got != m.width {
			t.Fatalf("frame row %d is %d cells, want m.width (%d)", i, got, m.width)
		}
	}

	// THE PINNED KEY LINES REACH THE SCREEN -- not a byte-exact rejoin, which
	// TestDeletingThePlanFromThePanelEndsTheReview already pins against
	// ordinaryDeleteConfirmText's own output. This check is narrower and answers
	// a different question: that the box measured above is THIS panel's, and not
	// some other border boxGeometry happened to find.
	for _, want := range []string{"y · delete", "esc · go back"} {
		if !viewContainsFlowing(t, screen, want) {
			t.Fatalf("the box never shows %q:\n%s", want, screen)
		}
	}
}

// TestTheDeleteConfirmIsTheSameBoxPanel1WasIn: modeConfirmDeletePlan's body is
// four short lines against Panel 1's ten, so a box that measured only its own
// content would draw NARROWER the instant d is pressed -- 40 cells against Panel
// 1's 63 at 80x24 with an empty-titled plan (deleteConfirmInterior's own doc
// comment carries the same figures), a third of the box gone in one keystroke.
// The property to hold is "the delete confirm is the same box, in the same
// place, naming its keys the same way."
//
// So this asserts TestTheRepointPaneIsTheBoxPanel1WasIn's own relationship, one
// keystroke further in: the box's WIDTH and its LEFT COLUMN, measured off the
// two drawn frames rather than recomputed from the arithmetic that produced
// them. THE HEIGHT IS DELIBERATELY NOT ASSERTED, for that test's own reason: the
// question says less than the panel it replaces, so the box is shorter and
// re-centres vertically.
//
// TWO CASES, NOT ONE: an empty title alone would only ever prove the WIDENING
// half of deleteConfirmInterior (repointInterior's own floor) and never the
// CAPPING half (centredInterior(m.width), which stops a title long enough to
// overflow Panel 1's own width from growing the box past it). The long-title
// case is a 126-cell title, which wraps under the cap rather than widening the
// box past Panel 1's own 63.
func TestTheDeleteConfirmIsTheSameBoxPanel1WasIn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		title string
	}{
		{"short body, default empty title", ""},
		{"title long enough to overflow Panel 1's width unless capped", strings.Repeat("Very Long Plan Title ", 6)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := faultedModel(t, &session.SourceFileError{Path: panelPath, State: session.SourceFileGone, Err: fs.ErrNotExist})
			m.sess.Plan.Title = tc.title
			panelX, _, panelW, _ := boxGeometry(t, ansi.Strip(m.View().Content))

			m = press(m, "d")
			if m.mode != modeConfirmDeletePlan {
				t.Fatalf("mode after d = %v, want modeConfirmDeletePlan", m.mode)
			}
			deleteX, _, deleteW, _ := boxGeometry(t, ansi.Strip(m.View().Content))
			if deleteW != panelW || deleteX != panelX {
				t.Fatalf("the box is %d cells at column %d under the delete confirm, want Panel 1's own %d at %d -- pressing d must not resize or move the box the reader is looking at",
					deleteW, deleteX, panelW, panelX)
			}
		})
	}
}

// TestTheRepointFieldIsABandInItsOwnZone pins the pane's specification: "the
// input box can be denoted by a different slightly darker background shade"
// instead of a second bordered rectangle inside the panel's own.
//
// IT RUNS ON AN UNSTRIPPED FRAME IN BOTH PRESETS: every geometry assertion in
// this file runs on a stripped frame, and a stripped frame cannot show a colour
// bug -- the two-colour border survived the whole suite that way. One preset is
// not enough for a ground either: theme.Strip is a step that reads in dark and
// THREE UNITS in light, which is why the field got a role of its own, and a
// one-preset test would pass on a version with no visible band at all in light.
//
// THE ANTI-VACUITY CHECK IS THE FIRST ASSERTION. If Field and Chrome ever hold
// one value the band disappears and every assertion below still passes, so the
// two grounds are required to differ before anything is measured over them.
func TestTheRepointFieldIsABandInItsOwnZone(t *testing.T) {
	for _, preset := range []string{"dark", "light"} {
		t.Run(preset, func(t *testing.T) {
			m := repointPane(t, preset)
			st := m.styles
			chromeBG, fieldBG := paintedBG(st.Chrome.Render("x")), paintedBG(st.Field.Render("x"))
			textFG, dimFG := paintedFG(st.Chrome.Render("x")), paintedFG(st.Strip.Render("x"))
			if fieldBG == "" || chromeBG == "" {
				t.Fatalf("Field bg = %q, Chrome bg = %q, want both set", fieldBG, chromeBG)
			}
			if fieldBG == chromeBG {
				t.Fatalf("Field bg = Chrome bg = %q -- there is no band, and every assertion below would pass anyway", fieldBG)
			}
			if textFG == dimFG {
				t.Fatalf("Text and Dim are the same colour (%q), so the ink check below asserts nothing", textFG)
			}

			rows := strings.Split(m.centredPanelBox(), "\n")
			band := -1
			for i, row := range rows {
				if !strings.Contains(row, fieldBG) {
					continue
				}
				if band >= 0 {
					t.Fatalf("box rows %d and %d are both in the field zone, want exactly one -- the band is one row", band, i)
				}
				band = i
			}
			if band < 0 {
				t.Fatalf("no box row is painted in the field zone:\n%s", strings.Join(rows, "\n"))
			}
			// THE BAND SPANS THE WHOLE INTERIOR. A field only as wide as what has
			// been typed into it -- nothing, here -- is a smear rather than
			// somewhere to type; repointInputRow pads itself out.
			plain := ansi.Strip(rows[band])
			interior := len([]rune(plain)) - len([]rune("┃    ┃"))
			if got := strings.Count(rows[band], fieldBG); got == 0 {
				t.Fatalf("the band carries no field ground at all: %q", plain)
			}
			inner := string([]rune(plain)[3 : 3+interior])
			if strings.TrimSpace(inner) != repointCursor {
				t.Fatalf("the band reads %q, want an empty field and the cursor -- the pane opens with nothing in it", inner)
			}
			// paintedFGNear, not paintedFG: the row mixes two Render()
			// calls (Chrome's border, Field's own span), and paintedFG
			// returns the FIRST foreground it finds anywhere in the string
			// -- Chrome's, since the border comes first -- regardless of
			// what Field's own foreground actually is. fieldBG identifies
			// the field's OWN escape sequence, out of which this reads the
			// foreground that was actually painted alongside it.
			if got := paintedFGNear(rows[band], fieldBG); got != textFG {
				t.Fatalf("the field's own ink is %q, want the Text colour %q -- Strip's own Dim foreground would render a typed path as if it were a hint", got, textFG)
			}
			for i, row := range rows {
				if i == band {
					continue
				}
				if got := paintedBG(row); got != chromeBG {
					t.Fatalf("box row %d is painted on %q, want Chrome's %q -- only the field leaves the panel's ground: %q", i, got, chromeBG, ansi.Strip(row))
				}
			}
		})
	}
}

// TestATypedPathKeepsItsTailAndTheBoxItsShape is the input row's own two
// rules, driven with real keystrokes over a path that does not fit.
//
// THE TAIL IS WHAT SURVIVES, not the head: the newest characters are the ones
// being typed, and a head-keeping clip freezes the visible text at the first
// keystroke past the edge, leaving backspace corresponding to nothing on
// screen (searchInputLine's rule, one panel over).
//
// AND THE BOX DOES NOT MOVE. A wrapped field would grow the box downward
// every time a path crossed the edge, which is the jitter the clip exists to
// prevent -- measured as the box's own width and height before and after,
// rather than as a property of the string.
func TestATypedPathKeepsItsTailAndTheBoxItsShape(t *testing.T) {
	m := repointPane(t, "dark")
	_, _, w0, h0 := boxGeometry(t, ansi.Strip(m.View().Content))

	long := "/Users/alice/plans/" + strings.Repeat("deeply-nested-directory/", 6) + "auth-redesign.md"
	for _, ch := range long {
		m = press(m, string(ch))
	}
	if got := m.pathTA.Value(); got != long {
		t.Fatalf("the field holds %q, want every keystroke %q", got, long)
	}
	screen := ansi.Strip(m.View().Content)
	_, _, w1, h1 := boxGeometry(t, screen)
	if w1 != w0 || h1 != h0 {
		t.Fatalf("the box went from %dx%d to %dx%d while a path was typed into it -- the field wraps or grows instead of clipping", w0, h0, w1, h1)
	}
	if !strings.Contains(screen, "auth-redesign.md"+repointCursor) {
		t.Fatalf("the end of the typed path is not on screen with the cursor after it:\n%s", screen)
	}
	if strings.Contains(screen, "/Users/alice/plans/deeply") {
		t.Fatalf("the head of the typed path is still on screen, so nothing was clipped -- this case no longer drives an overlong path")
	}
}

// TestTheBoxFitsAndKeepsItsFieldOnAShortTerminal is repointFit, and it asserts
// BOTH halves of what that function is for, because either one alone passes on a
// version that has neither.
//
// THE BOX MUST FIT. confirmBudget's whole derivation for a centred mode is "the
// box is its lines plus four rows, so the lines must be m.height-4" -- a body
// that ignores it is drawn into a frame with fewer rows than it has, and
// ui.Overlay drops the overflow: the bottom rule goes, and with it the box's own
// bottom edge. boxGeometry catches that by fatalling when there is no "┗" to
// find, so the height assertion below is belt to its braces.
//
// AND THE FIELD MUST SURVIVE, which is where this departs from
// truncateConfirmGroups. That function keeps the head and the LAST logical line
// -- the way out -- and elides the middle, which on this body is the input: a
// pane asking for a path with nowhere to type it. Here the key lines are what go,
// because the help bar one row under the box names both of them anyway.
//
// ASSERTING ONLY THE FIELD IS GREEN WITH repointFit DELETED: without the fit the
// box overflows, but the field sits in the box's TOP half, so Overlay's clamp
// keeps it on screen and only the bottom of the box is lost. The height is the
// assertion that fails.
//
// THE REVIEW MODEL HAS NO MINIMUM-SIZE GATE (only the list does), so these
// heights are reachable rather than hypothetical.
//
// THE REFUSAL-STANDING SUB-CASE drives the five-row body (headline, field, blank,
// keys, refusal) that every height above misses. At 60 columns with the short
// path it types the refusal is one row, so it pins the fit's SHAPE (box in frame,
// field on screen) rather than its drop ORDER -- see
// TestARefusalNeverHidesTheWayOutEvenWhenTheBoxCoversTheBar, below, for the order.
func TestTheBoxFitsAndKeepsItsFieldOnAShortTerminal(t *testing.T) {
	for _, height := range []int{12, 10, 9, 8, 7, 6, 5} {
		t.Run(strconv.Itoa(height), func(t *testing.T) {
			m := repointPane(t, "dark")
			m.width, m.height = 60, height
			m.rerender()
			for _, ch := range "/tmp/moved.md" {
				m = press(m, string(ch))
			}
			screen := ansi.Strip(m.View().Content)
			if rows := strings.Split(screen, "\n"); len(rows) != m.height {
				t.Fatalf("the frame is %d rows at 60x%d, want %d", len(rows), height, m.height)
			}
			_, y, _, h := boxGeometry(t, screen)
			if y < 0 || y+h > m.height {
				t.Fatalf("the box occupies rows %d..%d at 60x%d, want it inside the terminal -- the body ignored confirmBudget and Overlay cut the bottom off", y, y+h-1, height)
			}
			if !strings.Contains(screen, "/tmp/moved.md"+repointCursor) {
				t.Fatalf("the field is gone at 60x%d, and it is the one row this pane is for:\n%s", height, screen)
			}
		})
		t.Run(strconv.Itoa(height)+"/refusal standing", func(t *testing.T) {
			m := repointPane(t, "dark")
			m.width, m.height = 60, height
			m.rerender()
			m = pressRepointCommit(t, m) // the field is empty: a refusal now stands, a fifth row
			screen := ansi.Strip(m.View().Content)
			if rows := strings.Split(screen, "\n"); len(rows) != m.height {
				t.Fatalf("the frame is %d rows at 60x%d, want %d", len(rows), height, m.height)
			}
			_, y, _, h := boxGeometry(t, screen)
			if y < 0 || y+h > m.height {
				t.Fatalf("the box occupies rows %d..%d at 60x%d with a refusal standing, want it inside the terminal", y, y+h-1, height)
			}
			if !strings.Contains(screen, repointCursor) {
				t.Fatalf("the field is gone at 60x%d with a refusal standing:\n%s", height, screen)
			}
		})
	}
}

// TestARefusalNeverHidesTheWayOutEvenWhenTheBoxCoversTheBar: shedding BOTH key
// lines before anything else is safe only while the help bar one row under the
// box still names them (repointPaneHint) -- true of the four-row body repointFit
// was written for, and false the moment a standing refusal grows the box tall
// enough to cover that bar itself. At that point "esc · back" -- the way out --
// is named nowhere on screen at all: not in the box, because it was shed; not in
// the bar, because the box now stands over it.
//
// 31x12 LOOKS LIKE THE PLAN LIST'S DECLARED MINIMUM BUT ISN'T ANY MORE:
// listMinWidth is still 31, but listMinHeight moved off 12 once the masthead
// grew a box (app/list.go). The review model has no such gate of its own, so its
// 31x12 stays a plain literal. 80x10 is the review model's own cramped case.
//
// THE PATH IS A t.TempDir() ONE, deliberately, rather than a short "/tmp/x": it
// is long enough to also drive the 80-column band, so one fixture reaches both a
// 31-column and an 80-column box, and its non-existence is a fact about a freshly
// made temp directory rather than an assumption about the host's /tmp.
func TestARefusalNeverHidesTheWayOutEvenWhenTheBoxCoversTheBar(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "moved", "does-not-exist.md")

	for _, sz := range []struct{ width, height int }{
		{31, 12},
		{80, 10},
	} {
		name := fmt.Sprintf("%dx%d", sz.width, sz.height)
		t.Run(name+"/review door", func(t *testing.T) {
			m := repointPane(t, "dark")
			m.width, m.height = sz.width, sz.height
			m.rerender()
			for _, ch := range missing {
				m = press(m, string(ch))
			}
			m = pressRepointCommit(t, m)
			if m.repointRefusal == "" {
				t.Fatalf("test setup: no refusal is standing after committing a path that does not exist")
			}
			screen := ansi.Strip(m.View().Content)
			if !strings.Contains(screen, "esc") {
				t.Fatalf("esc is nowhere on screen at %s with a refusal standing:\n%s", name, screen)
			}
			_, y, _, h := boxGeometry(t, screen)
			if y+h < m.height {
				t.Fatalf("test setup: the box (rows %d..%d) does not cover the help bar at %s -- this case exists to drive that", y, y+h-1, name)
			}
			if !viewContainsFlowing(t, screen, "esc") {
				t.Fatalf("the box covers the help bar at %s and does not name esc IN THE BOX either:\n%s", name, screen)
			}
		})
	}
}

// TestTheCentredBoxHasAMeasure pins that the box sizes itself off its widest
// row, so without an upper bound ONE unwrapped paragraph takes it edge to edge.
//
// BOTH HALVES ARE HERE BECAUSE EITHER ALONE PASSES ON A VERSION THAT IS WRONG.
// A cap with no floor assertion would pass on a box pinned to one width whatever
// it holds; a "Panel 1 is unchanged" assertion alone would pass on an unbounded
// box, since Panel 1's own longest line is 57 and never reaches the measure. So:
// the pinned body must not move, and a body that WOULD have overflowed must
// stop at the measure.
//
// AND THE BOX MUST FIT AN 80-COLUMN TERMINAL, which is the whole derivation of
// the number: the box is its widest row plus six, so a 72-cell measure is a
// 78-cell box with a cell to spare either side. That is asserted as arithmetic
// over the drawn box rather than as the constant repeated.
func TestTheCentredBoxHasAMeasure(t *testing.T) {
	// One logical line, no hand-placed breaks -- the shape every body in this
	// surface takes once the measure exists.
	const prose = "Another plan is already following this file. Two plans can't follow the same file:\n\n>  /Users/alice/plans/auth-redesign.md\n\nesc · go back to the plan list"

	widths := map[int]int{}
	for _, w := range []int{80, 120, 200} {
		m := faultedModel(t, &session.SourceFileError{Path: panelPath, State: session.SourceFileGone, Err: fs.ErrNotExist})
		m.width, m.height = w, 40
		m.rerender()
		if panelW := boxWidth(t, m); panelW != 63 {
			t.Fatalf("Panel 1's box is %d cells at %d columns, want 63 -- the pinned body's longest line is 57 and must not reach the measure", panelW, w)
		}

		m.confirm = prose
		got := boxWidth(t, m)
		if got > panelMeasure+6 {
			t.Fatalf("a one-paragraph body draws a %d-cell box at %d columns, want at most %d -- the box has no measure", got, w, panelMeasure+6)
		}
		if got > w {
			t.Fatalf("the box is %d cells on a %d-column terminal", got, w)
		}
		widths[w] = got
	}
	// PAST THE MEASURE THE BOX STOPS GROWING WITH THE TERMINAL, which is the
	// property rather than any one figure. It is deliberately NOT an equality
	// against panelMeasure+6: ansi.Wordwrap breaks on a word boundary, so the
	// widest line it produces lands at or just under the measure and the exact
	// digit is a fact about where the spaces in this paragraph fall.
	if widths[80] != widths[120] || widths[120] != widths[200] {
		t.Fatalf("the box is %d/%d/%d cells at 80/120/200 columns -- past the measure it must not grow with the terminal", widths[80], widths[120], widths[200])
	}
	// And it must genuinely REACH the measure, or this test would pass on a
	// box capped at some much smaller number.
	if widths[80] < panelMeasure {
		t.Fatalf("the box is only %d cells for a body that should fill the measure (%d) -- this case no longer drives a body past it", widths[80], panelMeasure)
	}

	// Below the measure the TERMINAL binds, not the constant, or a narrow
	// screen gets a box wider than it is and ui.Overlay clips the right edge
	// off (composeCentredPanel's own note).
	for _, w := range []int{40, 60} {
		m := faultedModel(t, &session.SourceFileError{Path: panelPath, State: session.SourceFileGone, Err: fs.ErrNotExist})
		m.width, m.height = w, 40
		m.confirm = prose
		m.rerender()
		if got := boxWidth(t, m); got > w {
			t.Fatalf("the box is %d cells on a %d-column terminal -- the terminal has to bind below the measure", got, w)
		}
	}
}

// boxWidth is the drawn box's own width in cells, measured off the rendered
// box rather than recomputed from what the composer would have produced.
func boxWidth(t *testing.T, m *Model) int {
	t.Helper()
	rows := strings.Split(m.centredPanelBox(), "\n")
	return ansi.StringWidth(ansi.Strip(rows[0]))
}

// TestThePathRowMarkerLeadsTheFirstRowAtEveryWidth: the panel's ">  " marker
// and the path that follows it are ONE unbreakable unit, so a wrap that
// splits them -- putting ">" alone on its own row -- is the defect.
//
// THE TRIGGER IS A BREAK-FREE TAIL, not "any long path": ansi.Wordwrap breaks on
// hyphens too, so a hyphenated path stays fine well past the measure (the
// hyphenated and spaced sub-tests below are driven separately for that reason).
// Only a path with no space and no "-" anywhere in the stretch that would have
// to move strands the marker.
//
// m.confirm IS SET TO THE BARE PATH ROW, one logical line, rather than a full
// pinned body: what is under test is wrapConfirmGroups' own row-grouping of
// ONE line, and a full body would bury that behind three other lines' worth of
// wrapping this test does not need to re-prove.
func TestThePathRowMarkerLeadsTheFirstRowAtEveryWidth(t *testing.T) {
	rowsFor := func(t *testing.T, width int, path string) []string {
		t.Helper()
		m := faultedModel(t, &session.SourceFileError{Path: panelPath, State: session.SourceFileGone, Err: fs.ErrNotExist})
		m.width, m.height = width, 40
		m.confirm = ">  " + path
		return m.confirmLines()
	}
	assertMarkerLeadsAndSurvives := func(t *testing.T, rows []string, path string) {
		t.Helper()
		if len(rows) == 0 {
			t.Fatal("confirmLines() returned no rows for a non-empty path row")
		}
		if !strings.HasPrefix(rows[0], ">  ") || len(rows[0]) == len(">  ") {
			t.Fatalf("row 0 = %q, want the marker to LEAD the first row with at least one path character after it -- the marker-alone defect", rows[0])
		}
		// CONTINUATION ROWS CARRY NO INDENT: the ruling (list.go's
		// wrapConfirmGroups doc comment) is the plain option -- "the marker always
		// leads the first row and the path hard-wraps beneath it, with NO hanging
		// indent on the continuation rows". Checked on the raw row rather than
		// folded into the whitespace-stripped comparison below, because stripping
		// leading spaces is exactly what would let a hanging indent through.
		for i, row := range rows[1:] {
			if strings.HasPrefix(row, " ") {
				t.Fatalf("row %d = %q, continuation row opens with a leading space -- continuation rows are not indented", i+1, row)
			}
		}
		// THE GLUE MUST NEVER REACH THE SCREEN: wrapConfirmGroups reverses its own
		// nbsp substitution before returning, and this is what proves it rather
		// than assuming it -- dropping the restore step would leave every row here
		// still carrying it.
		for i, row := range rows {
			if strings.ContainsRune(row, ' ') {
				t.Fatalf("row %d = %q, carries the wrap's own glue rune (U+00A0) -- it must be restored to a plain space before this function returns", i, row)
			}
		}
		// EVERY CHARACTER OF THE PATH SURVIVES, IN ORDER, nothing dropped or
		// truncated by the glue/restore round-trip. Stripping whitespace from the
		// reconstituted rows and from the source path is the tolerance a forced
		// break needs: it costs a cosmetic space, never a path byte.
		stripWS := func(s string) string {
			return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '\n' }), "")
		}
		gotPath := strings.TrimPrefix(stripWS(strings.Join(rows, "\n")), ">")
		if gotPath != stripWS(path) {
			t.Fatalf("path across the wrapped rows = %q, want %q -- something was dropped or truncated", gotPath, stripWS(path))
		}
	}

	// THE BAND ITSELF: a break-free path (no space, no "-") swept 66..78 cells, at
	// two different terminal widths so the boundary is proven of the MECHANISM and
	// not of one interior. 72 is centredInterior(80); 54 is centredInterior(60) --
	// both above panelMeasure's own floor, so the measure is what is under test.
	for _, width := range []int{80, 60} {
		interior := centredInterior(width)
		t.Run(strconv.Itoa(width)+" columns", func(t *testing.T) {
			for _, n := range []int{66, 69, 70, 72, 74, 78} {
				t.Run(strconv.Itoa(n)+"-cell break-free path", func(t *testing.T) {
					path := "/" + strings.Repeat("a", n-1)
					rows := rowsFor(t, width, path)
					assertMarkerLeadsAndSurvives(t, rows, path)
					// The boundary is pinned at this width's own interior rather
					// than assumed to move with it: the marker spends 3 of those
					// cells (">" plus its own two spaces), so the path's share of
					// one row is interior-3 -- AT that many cells the glued row
					// still fits whole (one row); one cell over and the path must
					// wrap beneath the marker (two or more rows), which is the case
					// the defect made impossible to reach without stranding it. At
					// interior=72 (80 columns): 69 fits, 70 wraps.
					wantRows := 1
					if n > interior-len(">  ") {
						wantRows = 2
					}
					if (len(rows) == 1) != (wantRows == 1) {
						t.Fatalf("path of %d cells at interior %d wrapped to %d row(s), want %s",
							n, interior, len(rows), map[bool]string{true: "exactly 1 (fits whole)", false: "more than 1 (must wrap)"}[wantRows == 1])
					}
				})
			}
		})
	}

	// THE HYPHENATED CASE STILL BEHAVES: ansi.Wordwrap breaks on "-" as well as on
	// space, so a hyphenated path's own internal breaks are untouched by the glue,
	// which only ever touches the two ASCII bytes between the marker and the path.
	// Both an in-measure path (57 cells) and one past it (77 cells, forcing an
	// actual wrap) are driven, since the two exercise different branches of
	// wrapConfirmGroups: Wordwrap's hyphen breakpoint vs. Hardwrap's forced cut.
	t.Run("hyphenated path", func(t *testing.T) {
		for _, path := range []string{
			"/Users/alice/plans/stationnet-23-sensors-feed-notes.md",
			"/Users/alice/plans/" + strings.Repeat("a-", 27) + "z.md",
		} {
			rows := rowsFor(t, 80, path)
			assertMarkerLeadsAndSurvives(t, rows, path)
		}
	})

	// THE SPACED CASE STILL BEHAVES: a path with a real space in it (rare
	// but legal on every OS this product runs on) keeps that space as a
	// genuine Wordwrap breakpoint -- the glue is scoped to the fixed marker
	// bytes alone and never touches the path's own content.
	t.Run("spaced path", func(t *testing.T) {
		path := "/Users/alice/My Documents/plans/a redesign second pass long name here to force wrap.md"
		rows := rowsFor(t, 80, path)
		assertMarkerLeadsAndSurvives(t, rows, path)
	})
}

// TestThePathRowGlueRestoreOnlyReversesItsOwnTwoRunes: a path can carry its own
// U+00A0 -- legal in a filename on both macOS and Linux, routinely produced by
// copy-paste from a browser or a word processor, and reachable from an MCP save
// source hint -- and wrapConfirmGroups' restore step must reverse only the two
// glue runes it wrote itself, not every U+00A0 on the row. A strings.ReplaceAll
// rewrites the path's own nbsp to an ASCII space too, so a reader copying the
// path off the panel gets bytes that do not resolve, while the identical byte one
// row up in the headline is untouched: one character rendered two ways. A
// byte-exact comparison of the whole row is what this needs --
// assertMarkerLeadsAndSurvives' own stripWS tolerance folds U+00A0 and ASCII
// space together and would not see the difference.
func TestThePathRowGlueRestoreOnlyReversesItsOwnTwoRunes(t *testing.T) {
	path := "/Users/alice/My Documents/plan.md"
	m := faultedModel(t, &session.SourceFileError{Path: panelPath, State: session.SourceFileGone, Err: fs.ErrNotExist})
	m.width, m.height = 80, 40
	m.confirm = ">  " + path
	rows := m.confirmLines()
	if len(rows) == 0 {
		t.Fatal("confirmLines() returned no rows for a non-empty path row")
	}
	if want := ">  " + path; rows[0] != want {
		t.Fatalf("row 0 = %q, want %q byte-for-byte -- the path's own U+00A0 must survive the glue/restore round-trip, not come back as an ASCII space", rows[0], want)
	}
}

// refusalRows is the re-point pane's own refusal row, as the screen rows
// wrapConfirmGroups wrapped it to -- the one group repointBody marks inkWarn,
// found by ink rather than by position because repointFit can move where it sits
// once a short terminal sheds the key lines around it.
func refusalRows(t *testing.T, groups []panelGroup) []string {
	t.Helper()
	for _, g := range groups {
		if g.ink == inkWarn {
			return g.rows
		}
	}
	t.Fatal("no inkWarn group among the re-point pane's own groups -- the refusal never reached the render path")
	return nil
}

// TestTheRefusalMarkerLeadsTheFirstRowAtEveryWidth is
// TestThePathRowMarkerLeadsTheFirstRowAtEveryWidth's own claim at the re-point
// pane's refusal row: repointRefusalGlyph's "⚠ " and the refusal that follows it
// are ONE unbreakable unit exactly as the panel's ">  " and its path are, so a
// wrap that splits them -- putting "⚠" alone on its own row -- is the
// identical defect, reachable through a second marker gluePathRowMarker has
// to know about.
//
// m.repointRefusal IS SET TO THE BARE PATH, one logical line with no surrounding
// sentence, for the identical reason that test sets m.confirm to the bare path
// row: what is under test is the row-grouping of repointRefusalGlyph+refusal, and
// a full refusal sentence would bury that behind words this test need not re-wrap.
func TestTheRefusalMarkerLeadsTheFirstRowAtEveryWidth(t *testing.T) {
	rowsFor := func(t *testing.T, width int, path string) []string {
		t.Helper()
		m := repointPane(t, "dark")
		m.width, m.height = width, 30
		m.repointRefusal = path
		m.rerender()
		return refusalRows(t, m.repointGroups())
	}
	assertGlyphLeadsAndSurvives := func(t *testing.T, rows []string, path string) {
		t.Helper()
		if len(rows) == 0 {
			t.Fatal("no rows for a non-empty refusal")
		}
		if !strings.HasPrefix(rows[0], "⚠ ") || len(rows[0]) == len("⚠ ") {
			t.Fatalf("row 0 = %q, want the glyph to LEAD the first row with at least one refusal character after it -- the glyph-alone defect", rows[0])
		}
		// CONTINUATION ROWS CARRY NO INDENT, the identical ruling the path
		// marker's own test pins, applied here to the refusal's.
		for i, row := range rows[1:] {
			if strings.HasPrefix(row, " ") {
				t.Fatalf("row %d = %q, continuation row opens with a leading space -- continuation rows are not indented", i+1, row)
			}
		}
		// THE GLUE MUST NEVER REACH THE SCREEN.
		for i, row := range rows {
			if strings.ContainsRune(row, ' ') {
				t.Fatalf("row %d = %q, carries the wrap's own glue rune (U+00A0) -- it must be restored to a plain space before this function returns", i, row)
			}
		}
		stripWS := func(s string) string {
			return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '\n' }), "")
		}
		got := strings.TrimPrefix(stripWS(strings.Join(rows, "\n")), "⚠")
		if got != stripWS(path) {
			t.Fatalf("refusal text across the wrapped rows = %q, want %q -- something was dropped or truncated", got, stripWS(path))
		}
	}

	// THE BAND ITSELF, at two widths so the boundary is proven of the MECHANISM and
	// not of one interior: 80 columns leaves this pane's interior at 57 (Panel 1's
	// own widest line, uncapped by centredInterior(80)=72); 60 columns caps it at
	// 54 (centredInterior(60), narrower than Panel 1's 57). Both are above
	// repointMinInterior's own floor, so the pane's derived width is under test.
	for _, width := range []int{80, 60} {
		probe := repointPane(t, "dark")
		probe.width, probe.height = width, 30
		interior := repointInterior(probe.confirmLines(), probe.width)
		t.Run(strconv.Itoa(width)+" columns", func(t *testing.T) {
			for _, n := range []int{interior - 4, interior - 3, interior - 2, interior - 1, interior, interior + 3} {
				t.Run(strconv.Itoa(n)+"-cell break-free path", func(t *testing.T) {
					path := "/" + strings.Repeat("a", n-1)
					rows := rowsFor(t, width, path)
					assertGlyphLeadsAndSurvives(t, rows, path)
					// The boundary is pinned at this width's own interior rather
					// than assumed to move with it: the marker spends 2 of those
					// cells ("⚠" plus its own one space, against the path marker's
					// 3), so the refusal's share of one row is interior-2 -- AT that
					// many cells the glued row still fits whole; one cell over and
					// the refusal must wrap beneath the glyph. At interior=57 (this
					// pane's default fixture at 80 columns): 55 fits, 56 wraps.
					wantRows := 1
					if n > interior-ansi.StringWidth(repointRefusalGlyph) {
						wantRows = 2
					}
					if (len(rows) == 1) != (wantRows == 1) {
						t.Fatalf("refusal of %d cells at interior %d wrapped to %d row(s), want %s",
							n, interior, len(rows), map[bool]string{true: "exactly 1 (fits whole)", false: "more than 1 (must wrap)"}[wantRows == 1])
					}
				})
			}
		})
	}
}

// TestTheRefusalGlueRestoreOnlyReversesItsOwnOneRune is
// TestThePathRowGlueRestoreOnlyReversesItsOwnTwoRunes' own claim at the refusal
// row: an interpolated path can carry its own U+00A0 there too, and the restore
// step must reverse only the ONE glue rune gluePathRowMarker writes for
// repointRefusalGlyph's marker -- not the path marker's two, and not every U+00A0
// on the row. Proved by mutation: reversing count 2 here leaves a stray U+00A0 in
// place of the ASCII space between "⚠" and the path, reddening the byte-exact
// comparison below; reversing count 0 leaves the glue rune itself, reddening it
// the other way.
func TestTheRefusalGlueRestoreOnlyReversesItsOwnOneRune(t *testing.T) {
	path := "/Users/alice/My Documents/plan.md"
	m := repointPane(t, "dark")
	m.width, m.height = 80, 40
	m.repointRefusal = path
	m.rerender()
	rows := refusalRows(t, m.repointGroups())
	if len(rows) == 0 {
		t.Fatal("no rows for a non-empty refusal")
	}
	if want := "⚠ " + path; rows[0] != want {
		t.Fatalf("row 0 = %q, want %q byte-for-byte -- the refusal's own U+00A0 must survive the glue/restore round-trip, not come back as an ASCII space", rows[0], want)
	}
}

// TestAPasteReachesTheCommentComposer pins a real defect: quoting the comment
// being replied to is the commonest thing a reader pastes, and modeCompose
// dropped every paste on the floor. Model.Update routes
// tea.PasteMsg to updatePaste, which returned early for every mode but
// modeRepoint, so bubbles' textarea -- which DOES handle tea.PasteMsg -- never
// saw one.
//
// THE CRLF ROW IS THE ROUND-TRIP HALF AND IT IS NOT COSMETIC. bubbles'
// runeutil sanitizer maps '\r' and '\n' to a newline INDEPENDENTLY, so a "\r\n"
// pair reaches the draft as TWO newlines and a paragraph copied from anything
// Windows-authored arrives double-spaced. Measured against the real widget
// before this fix existed. The collapse happens before the widget is handed the
// message, because the widget is what would otherwise double it.
//
// WHAT IS DELIBERATELY NOT ASSERTED IS A CONTROL BYTE'S FATE BEYOND ITS
// ABSENCE. The widget deletes Cc runes and invalid UTF-8 on this path, where
// this package visualises them at the rename pane's prefill; the two doors
// disagree, the disagreement is ruled acceptable, and pinning "dropped" here
// is what keeps a later change to it deliberate.
//
// U+FE0F SURVIVES, and that is the one substitution this path must never make.
// A draft is WRITTEN BACK -- it is posted as a comment -- so stripping the
// selector here would persist "⚠" for a reader who pasted "⚠️", which is
// exactly the reasoning ui.VisibleControlsKeepingSelector16 carries for
// ListModel.enterRename. The strip is display-only and belongs at the renderer.
func TestAPasteReachesTheCommentComposer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		paste string
		want  string
	}{
		{name: "plain text lands", paste: "> the quoted comment", want: "hi> the quoted comment"},
		{name: "unix newlines are kept", paste: "a\nb", want: "hia\nb"},
		{name: "a CRLF pair is ONE newline, not two", paste: "a\r\nb", want: "hia\nb"},
		{name: "a lone CR is a newline", paste: "a\rb", want: "hia\nb"},
		{name: "control bytes are dropped", paste: "a\x00b\x07c", want: "hiabc"},
		{name: "the variation selector survives", paste: "⚠\ufe0f done", want: "hi⚠\ufe0f done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := openModel(t, setup(t))
			m = press(m, "j", "j", "c")
			if m.mode != modeCompose {
				t.Fatalf("mode = %v, want modeCompose", m.mode)
			}
			m = press(m, "h", "i")

			cur, _ := m.Update(tea.PasteMsg{Content: tc.paste})
			if got := cur.(*Model).ta.Value(); got != tc.want {
				t.Fatalf("draft after pasting %q = %q, want %q", tc.paste, got, tc.want)
			}
		})
	}
}

// TestAPastedQuoteRoundTripsIntoTheThread is the property the widget-level test
// above cannot claim: what a reader pastes is what the THREAD ends up holding.
// The draft is a model field, and a test that stops at m.ta.Value() proves the
// keystroke landed and nothing about what was stored -- the same gap
// TestTheRefusalSurvivesWhereTheStatusBarWouldHaveClipped names for m.status.
//
// THE FIXTURE IS A REAL GESTURE: quoting the comment being replied to, pasted
// as a multi-line block, with the CRLF a copy off a rendered page carries. It
// is asserted against the service's own read-back, so the
// collapse has to survive the post, the store and the projection -- not just the
// textarea.
func TestAPastedQuoteRoundTripsIntoTheThread(t *testing.T) {
	const pasted = "> does round-robin starve a tenant\r\n> with one very long job?"
	const want = "> does round-robin starve a tenant\n> with one very long job?"

	f := setup(t)
	m := openModel(t, f)
	m = press(m, "j", "j", "c")
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose", m.mode)
	}

	cur, _ := m.Update(tea.PasteMsg{Content: pasted})
	m = cur.(*Model)
	m, cmd := pressComposePost(t, m)
	m = drain(t, m, cmd)

	threads, err := f.svc.Threads(f.ctx, m.sess.Plan.ID)
	if err != nil {
		t.Fatalf("Threads: %v", err)
	}
	if len(threads) != 1 || len(threads[0].Comments) != 1 {
		t.Fatalf("threads = %+v, want exactly one thread carrying one comment", threads)
	}
	if got := threads[0].Comments[0].Body; got != want {
		t.Fatalf("stored comment body = %q, want %q -- the paste did not survive the post", got, want)
	}
}

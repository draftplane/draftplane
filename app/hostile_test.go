package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/theme"
	"github.com/draftplane/draftplane/ui"
)

// The hostile-document harness: committed fixtures carrying real control bytes,
// driven end to end through a real review model rather than through RenderDoc. A
// payload written inline in a test cannot be pinned to a figure -- a width count
// is a function of its payload's visible width -- and a committed fixture is the
// one input here that does not move.
//
// IT ASSERTS SHAPE AS WELL AS CONTENT. ui.FrameViolations scans for BYTES and has
// no opinion about rows or columns, so a frame can satisfy it completely and
// still be the wrong shape: a review confirm panel, clean and correctly glyphed,
// over its cell budget and a row taller than the terminal, with the help line off
// the bottom of a panel asking a human to authorize something. A payload that
// keeps every byte legal and blows out the geometry is a real attack on a
// reviewer.
//
// THE TWO WIDTH AUTHORITIES ARE THE WHOLE MECHANISM, and they are why shape is
// attackable at all. A bare C0 byte is ZERO cells to ansi.StringWidth -- what
// every truncation, pad and wrap in this repository measures with -- and ONE to
// the lipgloss Width().Render that paints the row. So a row budgeted to exactly
// its width by the first authority comes back over-width from the second,
// lipgloss breaks it, and the frame is a row taller than the terminal. A Control
// Picture is one cell to BOTH, so every shape assertion here is really an
// assertion that the substitution happened BEFORE the budget.
//
// THE BENIGN TWIN IS WHAT MAKES A SHAPE ASSERTION ATTRIBUTABLE, and without it
// this harness would be measuring somebody else's defect. Both fixtures overflow
// the terminal at some widths and their twins overflow at exactly the same ones:
// that is an unrelated wrapping residual this package does not own -- the
// heading arm neither wraps nor truncates, and both fixtures carry a heading
// longer than the arm's budget at a narrow enough terminal. AN UNGUARDED ABSOLUTE
// ROW-COUNT ASSERTION WOULD HAVE BEEN RED FOR A REASON THAT HAS NOTHING TO DO
// WITH A CONTROL BYTE. So the shape claim is stated in two halves: the hostile
// frame is the same shape as its twin at every width, which is the attributable
// half a control byte can falsify; and the hostile frame is exactly the
// terminal's own height AT THE WIDTHS WHERE THE TWIN IS TOO, guarded so it can
// never silently become a re-statement of that residual.
//
// The twin substitutes ONE BYTE FOR ONE BYTE, which is what makes it a shape
// reference rather than merely a benign document: the source lengths are
// identical and every line break falls in the same place. See benignTwin.

// hostileCorpus is the committed corpus, and it is TWO documents because the two
// views it has to reach take a document by different roads.
//
// hostile.md is the DOCUMENT channel -- five string-terminated introducers in
// table body cells, an APC in a header cell, a malformed row whose discarded tail
// carries a backspace, an indented fence with a tab in it, and the BS/CR/DEL
// forgeries on both raw arms.
//
// hostile-title.md is the LIST channel, and the plan list is why it exists at
// all: NOTHING the list draws goes through ParseBlocks, renderInlines or
// RenderDoc, so a document only reaches that frame if some field carries it
// there. Exactly one does. ui.InferTitle answers Block.Text raw,
// Session.InferredTitle is the seam every lazy create in the product goes
// through, and the title it answers is drawn on the review status bar and on
// THE PLAN LIST. So that fixture's H1 is the payload, and it is
// sized to fill a status bar rather than merely to contain a byte -- an overflow
// only fires on a row that was already exactly its width.
//
// THE REQUIRED BYTES ARE SPELT OUT AND CHECKED BEFORE ANYTHING IS RENDERED. The
// failure mode of a file like this is an editor, a formatter or a helpful
// pre-commit hook that cleans it up, at which point every assertion below passes
// and proves nothing -- and it is spelt out rather than computed from ui's own
// predicate for the reason listControlBytes gives about its glyphs: a fixture check
// that re-ran the implementation would agree with it by construction.
var hostileCorpus = []struct {
	name, file string
	// listTitle says whether this fixture's own H1 is the payload that reaches
	// the plan list. It is a FIELD rather than a t.Skip on an empty title,
	// because a skip is how this project loses coverage silently, and it is
	// asserted in both directions.
	listTitle bool
	bytes     []struct{ what, seq string }
	// onScreen is what must still be READABLE after the filter has run, and it
	// is the VISUALISE clause rather than a second content check. A reviewer
	// approves BYTES: option (a) -- strip -- was rejected because it shows a
	// reader clean text while the hash covers something else, so a repair that
	// dropped a payload instead of visualising it has to fail here.
	onScreen []string
}{
	{
		name: "the crafted document",
		file: "hostile.md",
		bytes: []struct{ what, seq string }{
			{"the APC introducer", "\x1b_"},
			{"the OSC introducer", "\x1b]"},
			{"the DCS introducer", "\x1bP"},
			{"the SOS introducer", "\x1bX"},
			{"the PM introducer", "\x1b^"},
			{"the backspace forgery", "Requires approval\bNo approval needed"},
			{"the carriage-return forgery", "\rWe will rotate them"},
			{"a DEL", "\x7f"},
			{"the discarded tail's sentinel", "ZQTAILZQ\b"},
		},
		onScreen: []string{
			// The one span in this channel that is NOT renderInlines output:
			// GFM discards it before the AST exists and tableRowCells appends
			// it to the last cell raw, so no leaf filter reaches it.
			"ZQTAILZQ",
			// An APC introducer's payload is TEXT once the introducer is a
			// glyph: what was hidden inside the sequence is what the reader
			// most needs to see.
			"payload that never ends",
		},
	},
	{
		name:      "the crafted title",
		file:      "hostile-title.md",
		listTitle: true,
		bytes: []struct{ what, seq string }{
			{"the backspace forgery, in the H1", "# Requires approval\bNo approval needed"},
			{"the carriage-return forgery", "\rWe will rotate them"},
			{"a DEL", "\x7f"},
			{"the APC introducer", "\x1b_"},
		},
		onScreen: []string{"payload that never ends", "We will rotate them"},
	},
}

// hostileSource reads one committed fixture and checks its bytes are still
// there. It returns the source and its benign twin together, because no
// assertion in this file may use one without the other.
func hostileSource(t *testing.T, file string, want []struct{ what, seq string }) (src, twin []byte) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "ui", "testdata", file))
	if err != nil {
		t.Fatalf("reading the committed fixture: %v", err)
	}
	for _, w := range want {
		if !strings.Contains(string(src), w.seq) {
			t.Fatalf("ui/testdata/%s no longer holds %s (%q) -- something cleaned the fixture up, and every assertion over it would pass vacuously", file, w.what, w.seq)
		}
	}
	twin = benignTwin(src)
	if string(twin) == string(src) {
		t.Fatalf("ui/testdata/%s and its benign twin are the same bytes -- the fixture carries no control byte at all", file)
	}
	if len(twin) != len(src) {
		t.Fatalf("the twin is %d bytes against the fixture's %d -- a twin that is not byte-for-byte the same length is not a shape reference", len(twin), len(src))
	}
	return src, twin
}

// benignTwin is the fixture with every control byte replaced by ONE ORDINARY
// CHARACTER, one byte for one byte.
//
// IT IS A SHAPE REFERENCE AND NOT MERELY A CLEAN DOCUMENT, which is why the
// substitution is length-preserving: the twin's every word, line and paragraph is
// exactly as long as the fixture's, so any difference in the rendered frame is
// the byte and nothing else. '~' is one cell to both width authorities, which is
// exactly what a Control Picture is.
//
// TAB IS DELIBERATELY NOT SUBSTITUTED THE SAME WAY: it is spent as FOUR
// SPACES on the raw arms and projects to four on the others, so replacing it
// with a single '~' would make the twin four cells narrower per tab and
// every shape comparison below would be measuring the tab rather than the
// payload. hostile.md contains one, inside its indented fence.
//
// IT SPELLS ITS OWN PREDICATE rather than calling ui's, the same deliberate
// independence listControlBytes takes with its glyphs: a twin built by the code
// under test would agree with it by construction. What the harness needs of this
// function is only that it leaves no control byte behind, which hostileSource
// re-checks against the fixture's own spelt-out bytes.
func benignTwin(src []byte) []byte {
	out := make([]byte, len(src))
	copy(out, src)
	for i, b := range out {
		if b == 0x7f || (b < 0x20 && b != '\t' && b != '\n') {
			out[i] = '~'
		}
	}
	return out
}

// termSize is a terminal this harness drives, named so a failure message says
// which one rather than reporting a bare pair of numbers.
type termSize struct{ w, h int }

func (s termSize) String() string { return fmt.Sprintf("%dx%d", s.w, s.h) }

// hostileSizes are the three terminals every content assertion here is taken
// at: the default 80x24, the 100x30 every other figure in this file is
// pinned to, and a wide one.
var hostileSizes = []termSize{{80, 24}, {100, 30}, {132, 43}}

// hostileGlyphs is what the filter puts on screen in place of each byte the
// corpus carries. WRITTEN OUT, not computed -- see listControlBytes.
var hostileGlyphs = []struct{ ctl, glyph string }{
	{"\x1b", "␛"},
	{"\b", "␈"},
	{"\r", "␍"},
	{"\x7f", "␡"},
}

// reviewFrames pages a review model from wherever it is to the bottom of its
// document and returns every frame it drew. PAGING AND NOT THE FIRST FRAME: both
// fixtures are longer than one screen at every width this file drives, so a
// construct that only exists below the fold is one a single-frame assertion never
// looks at -- and in hostile.md the discarded tail and the indented fence are
// both below it.
func reviewFrames(t *testing.T, m *Model) []string {
	t.Helper()
	m.scroll = 0
	m.rerender()
	var out []string
	for {
		out = append(out, m.View().Content)
		before := m.scroll
		m = press(m, "pgdown")
		if m.scroll == before {
			return out
		}
		if len(out) > len(m.lines)+8 {
			t.Fatalf("paging did not terminate after %d frames", len(out))
		}
	}
}

// resize is what a width sweep does instead of opening a new model per width.
//
// IT IS A PERFORMANCE DECISION WITH A CORRECTNESS CONDITION. openReviewOnDoc
// builds a temp XDG root, a service and a session and reparses the document;
// doing that once per width per arm put this file at 47 seconds under -race on
// its own. What a width sweep actually needs is the same document at a different
// size, which is exactly what a terminal resize hands this model in production.
// The condition is that nothing carried over from the previous size may survive:
// every caller puts the scroll back to the top, and every assertion here reads a
// FRAME rather than model state.
//
// IT TAKES BOTH DIMENSIONS, because defaulting the height quietly runs the
// three-terminal half of the shape sweep at one size -- a size table that
// silently collapses to one size is exactly the vacuity this file is written
// against.
func resize(m *Model, w, h int) *Model {
	m.width, m.height = w, h
	m.rerender()
	return m
}

// TestTheCraftedCorpusReachesBothViewsWithNoControlSequence is the CONTENT half,
// over both views, driven end to end.
//
// END TO END MEANS THE MODEL AND NOT THE RENDERER. ui's own sweep of hostile.md
// drives ui.RenderDoc directly; this opens a real session on the file, builds a
// real review model, and pages it -- so the status bar, the rail, the gutter, the
// block chrome and the panels are all in the frame the invariant is asserted
// over, and a channel nobody enumerated fails here even though no test names it.
//
// THE LIST IS REACHED THROUGH THE ONE FIELD THAT CARRIES A DOCUMENT THERE.
// Session.InferredTitle is the production seam -- mcptools' ensurePlan calls
// it -- so a hostile H1 becomes the plan's title and lands on the plan list.
// The test asserts the title still carries the raw byte before it draws
// anything, because a title laundered on the way would make the list assertion
// vacuous.
func TestTheCraftedCorpusReachesBothViewsWithNoControlSequence(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	for _, fx := range hostileCorpus {
		src, _ := hostileSource(t, fx.file, fx.bytes)
		t.Run(fx.name+"/the review document view", func(t *testing.T) {
			_, m := openReviewOnDoc(t, string(src))
			for _, sz := range hostileSizes {
				m.width, m.height = sz.w, sz.h
				m.rerender()
				frames := reviewFrames(t, m)
				if len(frames) < 2 {
					t.Fatalf("%s: the fixture fitted in %d frame(s) -- it must be longer than one screen or the paging above asserts nothing", sz, len(frames))
				}
				whole := strings.Join(frames, "\n")
				for i, f := range frames {
					requireCleanFrame(t, "the review frame over "+fx.file+" at "+sz.String()+", frame "+strconv.Itoa(i), f)
				}
				requireGlyphsOnScreen(t, "the review view over "+fx.file+" at "+sz.String(), src, whole)
				requireOnScreen(t, "the review view over "+fx.file+" at "+sz.String(), fx.onScreen, whole)
			}
		})
		t.Run(fx.name+"/the plan list view", func(t *testing.T) {
			_, m := openReviewOnDoc(t, string(src))
			title := m.sess.InferredTitle()
			// THE SEAM IS ASSERTED BEFORE THE FRAME IS DRAWN. The argument is
			// that InferTitle answers Block.Text RAW and that the transform
			// belongs at the draw; a future change that laundered the title on
			// the way here would leave every assertion below passing over a
			// clean string. The corpus table's listTitle flag says which answer
			// is expected, in both directions.
			if got := hostileTitleCarriesAByte(title); got != fx.listTitle {
				t.Fatalf("Session.InferredTitle() = %q carries a control byte: %v, want %v -- either the fixture's H1 changed or something filtered a field that must stay raw", title, got, fx.listTitle)
			}
			if !fx.listTitle {
				return
			}
			for _, sz := range hostileSizes {
				lm := onePlanList(th, nil, domain.Plan{ID: "l_a", Title: title, SourceHint: "~/plans/crafted.md"}, listBrowse)
				lm.width, lm.height = sz.w, sz.h
				content := lm.View().Content
				requireCleanFrame(t, "the list frame over "+fx.file+"'s inferred title at "+sz.String(), content)
				requireGlyphsOnScreen(t, "the list view over "+fx.file+"'s inferred title at "+sz.String(), []byte(title), content)
			}
		})
	}
}

// requireOnScreen is the VISUALISE clause: the bytes a payload was hiding are
// still readable. A repair that stripped rather than substituted would leave the
// frame clean, the invariant satisfied and the reader shown a document that is
// not the one they are approving.
//
// ITS ansi.Strip STAYS, and the reason is the direction it fails in. What this
// asserts is PRESENCE, so an introducer that truncates the strip hides the prose
// too and the assertion fires when it should not -- noisy, never silent. The
// check that needed closing was the ABSENCE half; see frameCarriesRaw.
func requireOnScreen(t *testing.T, what string, want []string, frame string) {
	t.Helper()
	plain := ansi.Strip(frame)
	for _, w := range want {
		if !strings.Contains(plain, w) {
			t.Fatalf("%s: %q never reaches the screen -- those bytes are in the reviewer's own file, and this view visualises rather than strips precisely so this view is not the one place they are missing", what, w)
		}
	}
}

// requireGlyphsOnScreen chooses visualising over a strip: a reviewer approves BYTES, so
// what a control byte becomes has to be VISIBLE and not absent. Only the glyphs
// whose byte is actually in this fixture are required, so a fixture that carries
// four of them is not held to a fifth.
func requireGlyphsOnScreen(t *testing.T, what string, src []byte, frame string) {
	t.Helper()
	plain := ansi.Strip(frame)
	for _, g := range hostileGlyphs {
		if !strings.Contains(string(src), g.ctl) {
			continue
		}
		if frameCarriesRaw(frame, g.ctl) {
			t.Fatalf("%s: the byte %q reached the screen unfiltered:\n  %v", what, g.ctl, ui.FrameViolations(frame))
		}
		// The GLYPH half still reads ansi.Strip's output, and that is safe in
		// the direction this one is not: an escape it truncates at hides the
		// glyph too, so the assertion fires SPURIOUSLY rather than not firing.
		if !strings.Contains(plain, g.glyph) {
			t.Fatalf("%s: the byte %q is nowhere on screen and %q is not there either -- this view visualises a control byte, and a strip would show a reader clean text over different bytes", what, g.ctl, g.glyph)
		}
	}
}

// frameCarriesRaw answers whether ctl -- one control byte out of hostileGlyphs --
// reached frame as ITSELF rather than as its Control Picture. It asks
// ui.FrameViolations and NOT ansi.Strip, and the difference is the whole point of
// the function existing.
//
// THE STRIP IS FAIL-OPEN HERE. ansi.Strip is a terminal parser rather than a
// styling remover (see stripStyling, ui/control.go): an introducer puts it into a
// string state that swallows everything after it until a terminator the payload
// never sends. Measured:
//
//	ansi.Strip("safe text \x9f then a backspace \b then more") == "safe text "
//	ansi.Strip("safe text \x1b_ then a backspace \b then more") == "safe text "
//
// so `strings.Contains(plain, "\b")` was FALSE for a frame that carries the
// backspace, and the assertion could not fire on the very frame it exists to fail
// on -- because the thing shadowing the byte is ITSELF something the filter was
// supposed to have removed. The seven-bit spelling is the worse one, because
// `ESC _` is a sequence this file's own hostile-title fixture already carries.
//
// ui.FrameViolations is Draftplane's own rune-aware scan. It reports each byte
// at its own offset and deliberately does NOT skip to a string terminator, so
// nothing after an introducer is hidden from it, and it exempts the ESC that
// opens one of Draftplane's own CSI sequences.
//
// A STRING ESCAPE IS REPORTED AT ITS INTRODUCING ESC and carries its own kind
// rather than ViolationC0, so an `ESC _` on the frame answers true for `\x1b`
// through the default arm below. ViolationC1 is excluded from it on purpose: an
// eight-bit introducer is one byte and no ESC reached the row at all.
func frameCarriesRaw(frame, ctl string) bool {
	for _, v := range ui.FrameViolations(frame) {
		switch v.Kind {
		case ui.ViolationC0:
			if v.Byte == ctl[0] {
				return true
			}
		case ui.ViolationC1:
		default:
			if ctl[0] == 0x1b {
				return true
			}
		}
	}
	return false
}

// hostileTitleCarriesAByte spells the predicate out for the same reason
// benignTwin does: what it checks is the FIXTURE and the seam in front of it,
// and a check that called the code under test would agree with it by
// construction.
func hostileTitleCarriesAByte(title string) bool {
	for i := 0; i < len(title); i++ {
		if b := title[i]; b == 0x7f || (b < 0x20 && b != '\t' && b != '\n') {
			return true
		}
	}
	return false
}

// hostileWidthLo and hostileWidthHi bound the shape sweep, and the RANGE is
// load-bearing rather than decorative.
//
// The overflow this harness is for only fires on a row that was ALREADY EXACTLY
// ITS WIDTH -- a row two cells short of its budget absorbs the extra cell a bare
// control byte paints and nothing moves. So no single terminal size can witness
// it for a fixed payload: what witnesses it is the width at which the payload's
// own measured width lands exactly on the budget it is drawn into, and that width
// is a function of the payload. Sweeping is how a committed fixture with a fixed
// payload finds its own.
const (
	hostileWidthLo = 60
	hostileWidthHi = 120
	hostileHeight  = 24
)

// TestTheCraftedCorpusKeepsTheFrameTheShapeOfItsBenignTwin is the SHAPE half, and
// it is the assertion ui.FrameViolations cannot make. See this file's header for
// the mechanism and for why the claim is split into an attributable half and a
// twin-guarded absolute half.
//
// What it asserts, per width, per frame: the hostile frame has the same NUMBER OF
// ROWS as its benign twin; every row has the same DISPLAY WIDTH as the twin's row
// at that index; where the twin fits the terminal, the hostile frame fits it too;
// and the reserved regions still hold what they reserve -- the TERMINAL'S LAST
// VISIBLE row is the help bar and the row above it is the status bar (see
// requireReservedRows for why that is read from the top).
//
// THE MUTATION THAT REDDENS THIS ONE IS THE STATUS BAR'S, AND IT IS NOT THE ONE A
// READER WOULD GUESS. Reverting app/painted.go's status bar to a bare
// st.StatusBar.Render(m.statusIdentity()) reddens here, and only over
// hostile-title.md, since it is ui.InferTitle's road onto the bar. Reverting
// ui/painted.go's heading arm does NOT redden here -- it reddens the content test
// by naming the byte -- and reverting app/list.go's title column reddens the list
// arm through the GLYPH COUNTER rather than through a shape assertion. So this
// test and the content test are not two witnesses to one defect: they see
// different sites, and each has a mutation the other is blind to.
func TestTheCraftedCorpusKeepsTheFrameTheShapeOfItsBenignTwin(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	for _, fx := range hostileCorpus {
		src, twin := hostileSource(t, fx.file, fx.bytes)
		t.Run(fx.name+"/the review document view", func(t *testing.T) {
			absolute, guarded, frames := 0, 0, 0
			_, hostileModel := openReviewOnDoc(t, string(src))
			_, benignModel := openReviewOnDoc(t, string(twin))
			check := func(where string, hostile, benign []string, reserved reservedRows, w, h int) {
				t.Helper()
				if len(hostile) != len(benign) {
					t.Fatalf("%s: the hostile document pages to %d frames where its benign twin pages to %d -- a control byte changed how much of the document fits on a screen", where, len(hostile), len(benign))
				}
				for i := range hostile {
					frames++
					at := where + ", frame " + strconv.Itoa(i)
					rows, twinFits := requireSameShape(t, at, hostile[i], benign[i], w, h)
					if !twinFits {
						guarded++
						continue
					}
					absolute++
					requireReservedRows(t, "the review frame over "+fx.file+" at "+at, rows, h, reserved)
					requireFits(t, "the review frame over "+fx.file+" at "+at, rows, w, h)
				}
			}
			// THE DENSE HALF IS ONE FRAME PER WIDTH. What a width sweep is FOR
			// is finding the width at which the payload's measured width lands
			// exactly on a budget, and every budget in this frame is a function
			// of the width alone -- the status bar's, the heading arm's, the
			// grid's clamp -- so the SIZE of the sweep is what matters and the
			// scroll position is not. Paging all of it at all of them put this
			// file at 47 seconds under -race for a regression guard.
			for w := hostileWidthLo; w <= hostileWidthHi; w++ {
				hostile, reserved := reviewFrame0At(t, hostileModel, w, hostileHeight)
				benign, _ := reviewFrame0At(t, benignModel, w, hostileHeight)
				check("width "+strconv.Itoa(w), []string{hostile}, []string{benign}, reserved, w, hostileHeight)
			}
			// AND THE PAGED HALF IS EVERY FRAME AT THREE REAL TERMINALS,
			// because a construct below the fold is one a first-frame assertion
			// never looks at -- and in hostile.md the discarded tail, the
			// indented fence and both raw-arm forgeries are all below it.
			for _, sz := range hostileSizes {
				hostile, reserved := reviewFramesAt(t, hostileModel, sz.w, sz.h)
				benign, _ := reviewFramesAt(t, benignModel, sz.w, sz.h)
				check(sz.String(), hostile, benign, reserved, sz.w, sz.h)
			}
			// THE GUARD IS ITSELF GUARDED. If the twin overflowed at every
			// width the absolute half would never run and this test would have
			// quietly decayed into the attributable half alone.
			if absolute == 0 {
				t.Fatalf("the benign twin overflowed the terminal at every one of %d frames, so the absolute half of this assertion never ran once", frames)
			}
			t.Logf("%s through the review view: %d widths at height %d (top frame each) plus %d terminals paged whole = %d frames compared against the benign twin, %d with the absolute assertion and %d guarded off by an unrelated overflow (the twin overflowed too)",
				fx.file, hostileWidthHi-hostileWidthLo+1, hostileHeight, len(hostileSizes), frames, absolute, guarded)
		})
		if !fx.listTitle {
			continue
		}
		t.Run(fx.name+"/the plan list view", func(t *testing.T) {
			hostileTitle := inferredTitleOf(t, string(src))
			benignTitle := inferredTitleOf(t, string(twin))
			if hostileTitle == benignTitle {
				t.Fatalf("the fixture and its twin infer the same title %q -- the payload is not in the H1 and this case would prove nothing", hostileTitle)
			}
			glyphs := 0
			for w := hostileWidthLo; w <= hostileWidthHi; w++ {
				hostile := listFrameAt(th, hostileTitle, w)
				benign := listFrameAt(th, benignTitle, w)
				rows, fits := requireSameShape(t, "width "+strconv.Itoa(w), hostile, benign, w, hostileHeight)
				// THE LIST PADS EVERY ROW TO EXACTLY ITS WIDTH, which the
				// document view does not, so the absolute half here is
				// unconditional rather than twin-guarded -- and the twin is
				// asserted to fit as a fixture check rather than as a licence.
				if !fits {
					t.Fatalf("width %d: the BENIGN twin's list frame does not fit a %d-row terminal -- this frame has no unrelated overflow residual in it, so a twin that does not fit means the fixture is wrong rather than the code:\n%s", w, hostileHeight, ansi.Strip(benign))
				}
				requireListReservedRows(t, "the list frame at width "+strconv.Itoa(w), rows, hostileHeight)
				requireFits(t, "the list frame at width "+strconv.Itoa(w), rows, w, hostileHeight)
				if strings.Contains(ansi.Strip(hostile), "␈") {
					glyphs++
				}
			}
			// NON-VACUITY, AND IT IS THIS ARM'S ONLY WITNESS. Plan.Title is the
			// one list field that does NOT overflow its column -- every other
			// column draws a row too many with the filter removed, and the title does
			// not -- so no shape assertion above can fail on this payload. What
			// CAN fail is the payload never arriving: with the title column's
			// own filter reverted the glyph is truncated away at every width and
			// the frames the assertions ran over hold nothing at all.
			if glyphs == 0 {
				t.Fatalf("the payload's glyph reached the list frame at NONE of the %d widths -- the title column truncated it away everywhere and every assertion above passed over a frame with no payload in it", hostileWidthHi-hostileWidthLo+1)
			}
			t.Logf("%s through the plan list, widths %d..%d at height %d: the inferred title's glyph is on screen at %d of %d widths",
				fx.file, hostileWidthLo, hostileWidthHi, hostileHeight, glyphs, hostileWidthHi-hostileWidthLo+1)
		})
	}
}

// reviewFramesAt opens a review model on doc at one width and pages it,
// returning every frame and the model's own help bar text.
func reviewFramesAt(t *testing.T, m *Model, w, h int) ([]string, reservedRows) {
	t.Helper()
	m = resize(m, w, h)
	// THE MARKERS COME OFF THE MODEL rather than out of a literal, so a bar whose
	// words change does not turn this into an assertion about a string nobody
	// draws any more. The status marker is the identity's FIRST WORD --
	// deliberately not its first clause -- because the clause is the title and the
	// title is where the payload is: comparing against it would mean running the
	// filter under test to build the expectation.
	return reviewFrames(t, m), reservedRows{help: firstClause(ansi.Strip(m.helpBar())), status: firstWord(m.statusIdentity())}
}

// reviewFrame0At is reviewFramesAt without the paging: the top screen only.
func reviewFrame0At(t *testing.T, m *Model, w, h int) (string, reservedRows) {
	t.Helper()
	m = resize(m, w, h)
	m.scroll = 0
	m.rerender()
	return m.View().Content, reservedRows{help: firstClause(ansi.Strip(m.helpBar())), status: firstWord(m.statusIdentity())}
}

// reservedRows is what the two bottom rows of a review frame must still carry.
type reservedRows struct{ help, status string }

// inferredTitleOf drives the production seam -- Session.InferredTitle, which
// mcptools' ensurePlan calls -- over a committed document.
func inferredTitleOf(t *testing.T, doc string) string {
	t.Helper()
	_, m := openReviewOnDoc(t, doc)
	return m.sess.InferredTitle()
}

// listFrameAt draws one plan, titled from a document, at one width.
func listFrameAt(th *theme.Theme, title string, w int) string {
	m := onePlanList(th, nil, domain.Plan{ID: "l_a", Title: title, SourceHint: "~/plans/crafted.md"}, listBrowse)
	m.width, m.height = w, hostileHeight
	return m.View().Content
}

// requireSameShape is the attributable half: the hostile frame's geometry against
// its benign twin's, row for row. It answers the hostile frame's rows and whether
// the TWIN fits the terminal, which is what licenses the absolute half. It NAMES
// THE DIMENSION, not a count of differences, for the same reason ui.FrameViolation
// names the byte: a failure saying "the frame moved" leaves the reader to find out
// what moved it.
func requireSameShape(t *testing.T, where, hostile, benign string, w, h int) (rows []string, twinFits bool) {
	t.Helper()
	rows = strings.Split(hostile, "\n")
	twin := strings.Split(benign, "\n")
	if len(rows) != len(twin) {
		t.Fatalf("%s: the hostile frame is %d rows where its benign twin is %d -- a control byte is zero cells to ansi.StringWidth and one to the lipgloss Width().Render that paints the row, so a row budgeted to exactly its width comes back as two:\n%s", where, len(rows), len(twin), ansi.Strip(hostile))
	}
	for i := range rows {
		hw, tw := ansi.StringWidth(rows[i]), ansi.StringWidth(twin[i])
		if hw != tw {
			t.Fatalf("%s: row %d is %d display cells where its benign twin's is %d: %q against %q", where, i, hw, tw, ansi.Strip(rows[i]), ansi.Strip(twin[i]))
		}
	}
	twinFits = len(twin) == h
	for _, r := range twin {
		if ansi.StringWidth(r) > w {
			twinFits = false
		}
	}
	return rows, twinFits
}

// requireFits is the absolute half: exactly the terminal's rows, and no row
// wider than the terminal.
func requireFits(t *testing.T, where string, rows []string, w, h int) {
	t.Helper()
	if len(rows) != h {
		t.Fatalf("%s is %d rows against a %d-row terminal", where, len(rows), h)
	}
	for i, r := range rows {
		if got := ansi.StringWidth(r); got > w {
			t.Fatalf("%s: row %d is %d display cells against a %d-cell terminal: %q", where, i, got, w, ansi.Strip(r))
		}
	}
}

// requireReservedRows is the third shape claim, and it is indexed from the TOP
// against the terminal's own height rather than from the end of the string --
// which is the whole of what makes it able to see anything.
//
// AN OVERFLOWING PANEL DOES NOT SHORTEN THE FRAME and it does not replace the
// help bar: the string still ENDS with the help bar, one row further down than
// the terminal can show, so an assertion reading rows[len-1] passes over exactly
// the screen it exists to catch. What a reader loses is the terminal's LAST
// VISIBLE row, which is row h-1 of the string, and that is what this reads. It is
// called BEFORE requireFits because "the bar that names the keys out of this
// screen is off the bottom of it" says what a reviewer has lost, where "the frame
// is 25 rows against 24" says only that something moved.
//
// ITS MUTATION IS A LAYOUT MUTATION AND NOT A FILTER ONE. Every control-byte
// mutation in this file is caught by the twin comparison above before it
// reaches here; what reddens this is drawing a help bar empty. So it is a guard
// on the REGION -- a panel or a bar that stopped being drawn -- beside a guard on
// the byte, not a second witness to the same defect.
func requireReservedRows(t *testing.T, where string, rows []string, h int, want reservedRows) {
	t.Helper()
	if len(rows) < h {
		t.Fatalf("%s is %d rows and the terminal reserves %d, so it does not fill the screen at all", where, len(rows), h)
	}
	if want.help == "" || want.status == "" {
		t.Fatalf("%s: the model's own bars are empty (%+v), so this assertion has no subject", where, want)
	}
	if last := ansi.Strip(rows[h-1]); !strings.Contains(last, want.help) {
		t.Fatalf("%s: the terminal's last visible row is %q and does not carry the help bar's own first clause %q -- the bar that names the keys out of this screen is off the bottom of it", where, last, want.help)
	}
	if status := ansi.Strip(rows[h-2]); !strings.Contains(status, want.status) {
		t.Fatalf("%s: the row above the help bar is %q and does not carry the status bar's own first word %q -- the reserved region does not hold what it reserves", where, status, want.status)
	}
}

// firstWord is the leading space-separated word of a bar's text: the part that
// survives truncation at every width this harness drives, and -- unlike the first
// CLAUSE of a status identity -- one that carries no payload, so an expectation
// built on it never has to run the filter under test.
func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}

// requireListReservedRows is the list frame's counterpart, indexed from the top
// for the reason requireReservedRows gives. Its help bar is hintLine's and its
// status row is planCountText's, each named by a word no piece of chrome around
// it uses.
func requireListReservedRows(t *testing.T, where string, rows []string, h int) {
	t.Helper()
	if len(rows) < h {
		t.Fatalf("%s is %d rows and the terminal reserves %d, so it does not fill the screen at all", where, len(rows), h)
	}
	if last := ansi.Strip(rows[h-1]); !strings.Contains(last, "enter open") {
		t.Fatalf("%s: the terminal's last visible row is %q and does not carry the browse help bar -- the bar is off the bottom of the screen", where, last)
	}
	if status := ansi.Strip(rows[h-2]); !strings.Contains(status, "plan") {
		t.Fatalf("%s: the row above the help bar is %q and does not carry the status line -- the reserved region does not hold what it reserves", where, status)
	}
}

// firstClause is the leading " · "-separated clause of a hint bar: the part
// that survives every truncation this program does, so an assertion built on it
// is about the bar being PRESENT rather than about how wide the terminal is.
func firstClause(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, " · "); i > 0 {
		return s[:i]
	}
	return s
}

// TestTheCraftedCorpusPinsItsPayloadDependentFigures is where every
// payload-dependent figure in this file becomes pinnable, because a
// committed fixture is the only input here that does not move.
//
// WHAT MAKES A FIGURE PAYLOAD-DEPENDENT: the number of widths at which a cell
// wrap cuts a sequence is a function of that sequence's VISIBLE width, and an
// OSC 8 hyperlink measures ZERO cells to ansi.StringWidth, so a probe built on one
// never reaches a cell budget and never wraps at all. Do not quote a width count
// without its payload; a committed fixture IS the payload, so the figures below
// carry theirs by construction.
//
// THE FIGURES IT PINS, all at height 24 over widths 60..120 through a real
// review model:
//
//	the discarded tail's sentinel on screen   at every width from its own floor up
//	a source-supplied introducer in the frame  0 widths
//	that introducer CUT across two rows        0 widths
//	Draftplane's own CSI in the frame          > 0 at every width
//
// THE FIRST ONE HAS A FLOOR AND THE FLOOR IS MEASURED, NOT GUESSED. Below it the
// grid's own clamp (clampTableWidth) leaves the column narrower than the
// eight-character sentinel, so what is missing there is the grid's truncation and
// not the span. The floor is asserted as an EXACT value rather than an upper
// bound, because a floor that quietly rose would be a regression this test would
// otherwise absorb.
//
// AND THE ZEROES ARE THE POINT OF THE OTHER TWO. "The grid's cell wrap splits an
// escape sequence across two ui.Line values" does not exist at this commit, and
// that was proved by mutation rather than by argument: with isVisibleControl
// amended to exempt 0x1b and nothing else changed, the introducer reaches a
// body-cell Line at every width and the APC payload is cut at many of them. What
// closed it is the substitution at the leaf: ESC is a C0 byte, so it is
// visualised before the grid ever measures a cell.
func TestTheCraftedCorpusPinsItsPayloadDependentFigures(t *testing.T) {
	src, _ := hostileSource(t, hostileCorpus[0].file, hostileCorpus[0].bytes)
	const sentinel = "ZQTAILZQ"
	introducers, cut, floor, widths := 0, 0, 0, 0
	_, m := openReviewOnDoc(t, string(src))
	for w := hostileTailFloorLo; w <= hostileWidthHi; w++ {
		widths++
		// THE WHOLE DOCUMENT AND NOT THE VISIBLE SCREEN, and the level is the
		// claim's own rather than a shortcut. The claim is "splits an escape
		// sequence across two ui.Line values", and m.lines IS the document's
		// Lines at this width -- what RenderDoc produced, before any scroll
		// position decides which of them a reader can see. Asking it here covers
		// every row at every width instead of only the rows a paging loop
		// happened to visit. That the tail also reaches a real paged FRAME is
		// asserted separately by this file's onScreen table.
		m = resize(m, w, hostileHeight)
		var doc strings.Builder
		for _, l := range m.lines {
			doc.WriteString(l.Text)
			doc.WriteByte('\n')
		}
		whole := doc.String()
		if strings.Contains(ansi.Strip(whole), sentinel) {
			if floor == 0 {
				floor = w
			}
		} else if floor != 0 {
			t.Fatalf("width %d: the discarded tail's sentinel %q is missing from the document although it was drawn at width %d -- a floor with a hole in it is not a floor", w, sentinel, floor)
		}
		for _, intro := range []string{"\x1b_", "\x1b]", "\x1bP", "\x1bX", "\x1b^"} {
			if strings.Contains(whole, intro) {
				introducers++
			}
		}
		// THE CUT, asked directly rather than inferred from the violation
		// count: an introducer whose payload is present but whose opening bytes
		// are not intact on any single Line is the split this test is about.
		for _, l := range m.lines {
			if k := strings.Index(l.Text, "\x1b_"); k >= 0 && !strings.Contains(l.Text, "\x1b_payload") {
				cut++
			}
		}
		if ui.FrameCSICount(whole) == 0 {
			t.Fatalf("width %d: the rendered document carries no CSI at all, so nothing above was measured on a styled document", w)
		}
	}
	if widths != hostileWidthHi-hostileTailFloorLo+1 {
		t.Fatalf("the sweep drove %d widths, want %d", widths, hostileWidthHi-hostileTailFloorLo+1)
	}
	if introducers != 0 || cut != 0 {
		t.Fatalf("a source-supplied introducer reached the review frame at %d widths and was cut across two rows at %d, want 0 and 0", introducers, cut)
	}
	if floor != hostileTailWidthFloor {
		t.Fatalf("the discarded tail's sentinel first reaches the review frame at width %d, want %d -- the floor is the grid's own column clamp and a floor that moved is a change in how much of a reviewer's own file this view is willing to show", floor, hostileTailWidthFloor)
	}
	t.Logf("hostile.md through the review view, widths %d..%d at height %d: the discarded tail's sentinel is drawn from width %d up, a source introducer reaches a Line at %d widths and is cut across two at %d",
		hostileTailFloorLo, hostileWidthHi, hostileHeight, floor, introducers, cut)
}

// hostileTailWidthFloor is the narrowest terminal at which the discarded tail's
// eight-character sentinel survives the grid's own column clamp into the review
// frame, and hostileTailFloorLo is where the sweep that finds it starts.
//
// It is pinned as an EQUALITY rather than a bound: a floor that quietly rose
// would be a change in how narrow a terminal can get before this view stops
// showing a reviewer bytes that are in their own file, and a "<=" assertion would
// absorb it. ui.DocRightMargin's two cells of document ground are already in this
// number -- that cost is recorded rather than absorbed, and whatever moves the
// floor next should have to say so out loud too.
//
// IT IS THE SAME FLOOR RenderDoc HAS ALONE, and that is worth saying because the
// obvious guess is wrong: the review frame spends six columns on a rail and a
// gutter, so a reader would expect the floor here to sit six higher. It does not,
// because clampTableWidth clamps the grid to the budget it is HANDED and the two
// budgets differ by exactly the chrome. The figure is a property of the grid's
// clamp and not of the frame around it.
const (
	hostileTailFloorLo    = 14
	hostileTailWidthFloor = 24
)

// TestTheGlyphAssertionSeesAByteAnIntroducerIsHiding is the non-vacuity guard on
// a non-vacuity guard: requireGlyphsOnScreen's "the byte reached the screen
// unfiltered" check COULD NOT FIRE in the presence of an introducer.
//
// A fail-open test helper is the defect this project has recorded more often than
// any other, and this one had the sharpest possible shape: the assertion that
// catches a broken filter was blinded by exactly the byte a broken filter lets
// through. Both spellings are driven -- the eight-bit C1 and the SEVEN-BIT
// `ESC _`, which matters more because ui/testdata/hostile-title.md carries that
// sequence already.
//
// THE `strip` COLUMN IS WHAT THE OLD SPELLING ANSWERED, carried in the table so
// the mutation is legible rather than described: it is the value
// `strings.Contains(ansi.Strip(frame), ctl)` returns, and every row where it
// disagrees with `want` is a row the helper used to get wrong.
//
// THE LAST TWO ROWS ARE THE OVER-FIRING GUARD. A check that answered "yes,
// unfiltered" for a properly visualised frame, or for Draftplane's own SGR, would
// be red everywhere and worth nothing.
func TestTheGlyphAssertionSeesAByteAnIntroducerIsHiding(t *testing.T) {
	for _, tc := range []struct {
		name, frame, ctl string
		// inFrame is whether the raw byte is in the frame at all, want is what
		// frameCarriesRaw must answer, and strip is what the ansi.Strip
		// spelling this replaced answered. The three differ on purpose.
		inFrame, want, strip bool
	}{
		{"a backspace behind a raw C1", "safe \x9f text \b more", "\b", true, true, false},
		{"a backspace behind a seven-bit APC", "safe \x1b_ text \b more", "\b", true, true, false},
		{"a DEL behind a raw C1", "safe \x9f text \x7f more", "\x7f", true, true, false},
		{"a carriage return behind a seven-bit OSC", "safe \x1b] text \r more", "\r", true, true, false},
		{"the APC introducer itself", "safe \x1b_payload", "\x1b", true, true, false},
		{"a backspace with nothing hiding it", "safe text \b more", "\b", true, true, true},
		{"a frame already visualised", "safe text ␈ more", "\b", false, false, false},
		{"Draftplane's own SGR", "\x1b[38;2;1;2;3msafe text\x1b[m", "\x1b", true, false, false},
		// THE C1 ARM'S OWN CASE. An eight-bit introducer is ONE byte, so no ESC
		// reached the row and the ESC question must answer false -- and without
		// the ViolationC1 arm this row is the only thing in the repository that
		// notices, because every other row here carries a seven-bit spelling or
		// no C1 at all. With that arm deleted, the whole package is still green.
		{"a raw C1, asked about ESC", "safe \x9f text", "\x1b", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// THE FIXTURE FIRST: a row whose frame does not hold the byte would
			// meet `want == false` for a reason that has nothing to do with the
			// check, and the SGR row is the one that has to hold it and answer
			// false anyway.
			if got := strings.Contains(tc.frame, tc.ctl); got != tc.inFrame {
				t.Fatalf("the frame %q holds %q: %v, want %v -- this row proves nothing about the check", tc.frame, tc.ctl, got, tc.inFrame)
			}
			if got := frameCarriesRaw(tc.frame, tc.ctl); got != tc.want {
				t.Fatalf("frameCarriesRaw(%q, %q) = %v, want %v -- ui.FrameViolations reported %v", tc.frame, tc.ctl, got, tc.want, ui.FrameViolations(tc.frame))
			}
			if got := strings.Contains(ansi.Strip(tc.frame), tc.ctl); got != tc.strip {
				t.Fatalf("ansi.Strip(%q) = %q and contains %q: %v, want %v -- the blindness this helper routes around has changed shape, so frameCarriesRaw's doc comment is now describing something that is not true",
					tc.frame, ansi.Strip(tc.frame), tc.ctl, got, tc.strip)
			}
		})
	}
}

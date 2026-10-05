package app

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/session"
	"github.com/draftplane/draftplane/theme"
	"github.com/draftplane/draftplane/ui"
)

// dropSpacesAndNewlines is wrapPlain's own content-preservation tolerance,
// copied because this package does not import ui's test helpers.
func dropSpacesAndNewlines(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '\n' }), "")
}

// longHyphenatedPath is a realistic macOS path whose "-" characters are
// breakpoints ansi.Wordwrap acts on ("/" is NOT one).
const longHyphenatedPath = "/Users/alice/development/acme-platform/docs/plans/q3-rollout.md"

// Applied to wrapConfirmGroups: a hyphenated compound survives a forced
// wrap WHOLE, on its own row if it must, rather than splitting at the hyphen
// and leaving the fragment behind it stranded alone. Both fixtures are
// pinned panel text carrying real hyphens -- the info panel's File line and
// the delete question over a hyphenated title -- not hyphen chains built to
// order.
//
// Non-vacuity: each case also builds what the plain two-pass shape produces
// -- ansi.Wordwrap then ansi.Hardwrap, no hyphen protection in front of
// either -- and asserts THAT construction does orphan a hyphen. A fixture
// that cannot reproduce the defect is not proof anything was fixed.
func TestWrapConfirmGroupsCarriesAHyphenatedWordDown(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := ui.NewStyles(th)

	for _, tc := range []struct {
		name  string
		line  string
		width int
		want  []string
	}{
		{
			// width 58 is centredInterior(64) -- the box interior a 64-column
			// terminal draws this panel at, and one of the widths where the
			// info panel's File line orphaned a hyphen.
			name:  "the info panel's File line",
			line:  "File: " + longHyphenatedPath,
			width: 58,
			want: []string{
				"File: /Users/alice/development/acme-",
				"platform/docs/plans/q3-rollout.md",
			},
		},
		{
			// width 29 is centredInterior(35) -- one of the widths where the
			// delete question over a hyphenated title orphaned a hyphen.
			name:  "a hyphenated title in the delete question",
			line:  "Delete \"q3-rollout-and-follow-ups\" and its comment threads?",
			width: 29,
			want: []string{
				"Delete",
				"\"q3-rollout-and-follow-ups\"",
				"and its comment threads?",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldShape := strings.Split(ansi.Hardwrap(ansi.Wordwrap(tc.line, tc.width, ""), tc.width, true), "\n")
			foundOrphan := false
			for _, row := range oldShape {
				if strings.TrimSpace(ansi.Strip(row)) == "-" {
					foundOrphan = true
				}
			}
			if !foundOrphan {
				t.Fatalf("fixture does not discriminate: the unprotected two-pass shape (Wordwrap then Hardwrap, no protection) never orphaned a hyphen for %q at width %d:\n%q", tc.line, tc.width, oldShape)
			}

			groups := wrapConfirmGroups(tc.line, tc.width, st.FocusHeader)
			if len(groups) != 1 {
				t.Fatalf("wrapConfirmGroups(%q, %d) returned %d groups for a one-line input, want 1", tc.line, tc.width, len(groups))
			}
			got := groups[0]
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("wrapConfirmGroups(%q, %d) =\n%q\nwant\n%q", tc.line, tc.width, got, tc.want)
			}
			for _, row := range got {
				if w := ansi.StringWidth(row); w > tc.width {
					t.Fatalf("row %q is %d cells, over the %d budget", row, w, tc.width)
				}
				if strings.TrimSpace(row) == "-" {
					t.Fatalf("row %q is an orphaned hyphen -- the rejected alternative", row)
				}
			}
			if got, want := dropSpacesAndNewlines(strings.Join(got, "\n")), dropSpacesAndNewlines(tc.line); got != want {
				t.Fatalf("content changed across the wrap: got %q, want %q", got, want)
			}
		})
	}
}

// The width sweep is wide enough to cross the upstream off-by-one boundary
// (ansi's wordwrap, the `case r == '-'` arm with no limit check) more than
// once, since which cell the boundary lands on moves with the width.
func TestWrapConfirmGroupsNeverOrphansAHyphenAcrossWidths(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := ui.NewStyles(th)
	fixtures := []string{
		"File: " + longHyphenatedPath,
		"Delete \"q3-rollout-and-follow-ups\" and its comment threads?",
		">  " + panelPath, // the panel's own marker, glued, over a hyphenated pinned path
	}
	for _, line := range fixtures {
		for w := 10; w <= 90; w++ {
			groups := wrapConfirmGroups(line, w, st.FocusHeader)
			for _, g := range groups {
				for _, row := range g {
					if width := ansi.StringWidth(ansi.Strip(row)); width > w {
						t.Fatalf("line %q at width %d: row %q is %d cells over budget", line, w, row, width)
					}
					if strings.TrimSpace(ansi.Strip(row)) == "-" {
						t.Fatalf("line %q at width %d: row %q is an orphaned hyphen", line, w, row)
					}
				}
			}
		}
	}
}

// If the input already carries panelHyphenSentinel, wrapConfirmLine must not
// protect a single hyphen -- it must wrap EXACTLY as ansi.Wordwrap alone
// would, hyphen bug included, rather than risk treating the caller's own
// U+E001 as a hyphen it hid.
func TestWrapConfirmLineFallsBackWhenTheSentinelIsAlreadyInTheInput(t *testing.T) {
	line := "already-" + string(panelHyphenSentinel) + "sentinelled and one-more-hyphen-here"
	if !strings.ContainsRune(line, panelHyphenSentinel) {
		t.Fatal("fixture assumption broken: the sentinel is not actually in the input")
	}
	for _, w := range []int{10, 20, 30} {
		got := wrapConfirmLine(line, w)
		want := ansi.Wordwrap(line, w, "")
		if got != want {
			t.Fatalf("width %d: wrapConfirmLine(%q) = %q, want exactly ansi.Wordwrap's own answer %q -- the sentinel-present guard did not fall back", w, line, got, want)
		}
	}
}

// The reason protectPanelHyphens exists rather than being a call into
// ui.protectHyphens: tokenizing on bare unicode.IsSpace treats
// gluePathRowMarker's pathRowGlueRune (U+00A0) as a token boundary, where
// ansi.Wordwrap's own wordwrap() does not -- an NBSP merges into the word
// being built exactly like a letter. So ">"+NBSP+NBSP+"ab-cdefg" is ONE word to Wordwrap, 11
// cells, and at width 8 that whole run cannot be protected (11 > 8) even
// though "ab-cdefg" READ ALONE would fit (8 <= 8).
//
// Mutation: with isBreak swapped for bare unicode.IsSpace, this fixture at
// this width wraps to [">  ab-cd" "efg"] instead -- an arbitrary break
// invented mid-word, the class this mechanism exists to prevent. Neither
// shape overflows or orphans a bare hyphen, so the difference is quality
// rather than correctness, which is why it needs a fixture and not an
// argument.
func TestProtectPanelHyphensTreatsTheGlueRuneAsNonBreaking(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := ui.NewStyles(th)
	groups := wrapConfirmGroups(">  ab-cdefg", 8, st.FocusHeader)
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	want := []string{">  ab-", "cdefg"}
	if got := groups[0]; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("wrapConfirmGroups(\">  ab-cdefg\", 8) = %q, want %q -- the marker-glued hyphen must break cleanly rather than have an arbitrary letter split invented downstream of it", got, want)
	}
}

// The sentinel collision hazard, checked rather than assumed. The fixture
// path carries BOTH its own literal U+00A0 AND a hyphen chain long enough to
// force wrapConfirmLine's retry loop, at a width narrow enough that the
// marker itself must also wrap beneath the path -- every mechanism in
// wrapConfirmGroups engaged in the same row.
//
// panelHyphenSentinel must never reach the screen, and neither rune the
// count-exact pathRowGlueRune reversal depends on may be disturbed: the
// path's own NBSP survives as NBSP rather than collapsing to a plain space,
// and the panel's marker still leads row 0. This does not re-prove that
// shape, only that hyphen protection running IN FRONT of it changes nothing
// about it.
func TestWrapConfirmGroupsPathRowGlueSurvivesHyphenProtection(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := ui.NewStyles(th)
	// A path with its own NBSP (legal on macOS/Linux) AND a hyphenated
	// compound long enough that a single protection pass is not enough at
	// these widths -- forcing wrapConfirmLine's retry loop to run at least
	// once for this row.
	path := "/Users/alice/My\u00a0Documents/acme-platform-rollout-notes-q3.md"
	if !strings.ContainsRune(path, '\u00a0') {
		t.Fatal("fixture assumption broken: the path carries no NBSP of its own")
	}
	if n := strings.Count(path, "-"); n < 3 {
		t.Fatalf("fixture assumption broken: only %d hyphens, want several so a single protection pass is not trivially enough", n)
	}
	line := ">  " + path
	for _, w := range []int{20, 30, 40} {
		t.Run(strconv.Itoa(w), func(t *testing.T) {
			groups := wrapConfirmGroups(line, w, st.FocusHeader)
			if len(groups) != 1 {
				t.Fatalf("got %d groups, want 1", len(groups))
			}
			rows := groups[0]
			joined := strings.Join(rows, "\n")
			if strings.ContainsRune(joined, panelHyphenSentinel) {
				t.Fatalf("width %d: the sentinel (U+E001) reached the output: %q", w, joined)
			}
			if !strings.HasPrefix(rows[0], ">  ") {
				t.Fatalf("width %d: row 0 = %q, want the marker to still lead", w, rows[0])
			}
			for _, row := range rows {
				if width := ansi.StringWidth(row); width > w {
					t.Fatalf("width %d: row %q is %d cells over budget", w, row, width)
				}
			}
			// The path's own NBSP survives and the marker's two glue runes do
			// not. An over-reaching ReplaceAll reversal would turn the NBSP
			// into a plain space and still "pass" under a whitespace-folding
			// tolerance, which is why this comparison does not fold NBSP.
			flat := strings.Join(strings.FieldsFunc(joined, func(r rune) bool { return r == ' ' || r == '\n' }), "")
			gotPath := strings.TrimPrefix(flat, ">")
			wantPath := path
			if gotPath != wantPath {
				t.Fatalf("width %d: reconstructed path = %q, want %q -- the path's own NBSP or a hyphen did not survive the round trip", w, gotPath, wantPath)
			}
		})
	}
}

// Every pinned panel body, through the REAL composer functions and the
// SAME fixtures their own byte-exact tests use, driven across the width
// range a centred panel can actually be drawn at -- listMinWidth through
// comfortably past panelMeasure's 72-cell cap, so the measure's ceiling is
// crossed and not merely approached.
//
// Two properties, matching wrapConfirmGroups' contract: no row exceeds its
// own interior, and no row is an orphaned hyphen (the rejected alternative).
// A regression guard only -- it deliberately does not pin what the bodies
// measure to, since the two named tests above own the exact shapes.
func TestPinnedPanelBodiesNeverOverflowOrOrphanAHyphen(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	st := ui.NewStyles(th)

	sourceGoneFault := &session.SourceFileError{Path: panelPath, State: session.SourceFileGone}
	sourceUnreadableFault := &session.SourceFileError{Path: panelPath, State: session.SourceFileUnreadable, Err: panelCause}
	sourceClaimed := &session.SourceFileError{Path: panelPath, State: session.SourceFileClaimed}
	sourceReleased := &session.SourceFileError{Path: panelPath, State: session.SourceFileReleased}

	bodies := []struct {
		name string
		text string
	}{
		{"sourceFault/gone", sourceFaultText(sourceGoneFault)},
		{"sourceFault/unreadable", sourceFaultText(sourceUnreadableFault)},
		{"sourceFault/claimed", sourceFaultText(sourceClaimed)},
		{"sourceFault/released", sourceFaultText(sourceReleased)},
		{"ordinaryDelete", ordinaryDeleteConfirmText(planWithHyphenatedTitle())},
		{"info", infoPanelText(planItem{plan: domain.Plan{ID: "l_q3", Title: "q3-rollout-and-follow-ups", SourceHint: longHyphenatedPath}})},
		{"repoint/headline", repointPaneHeadline},
		{"repoint/keys", repointPaneKeys},
	}

	for _, b := range bodies {
		if b.text == "" {
			t.Fatalf("body %s composed to the empty string -- fixture assumption broken", b.name)
		}
		for w := listMinWidth; w <= 120; w++ {
			checkPanelBodyAtWidth(t, b.name, b.text, centredInterior(w), st)
		}
		for _, w := range []int{160, 200, 300} {
			checkPanelBodyAtWidth(t, b.name, b.text, centredInterior(w), st)
		}
	}

	// modeConfirmDeletePlan narrows further too, to Panel 1's own width
	// (deleteConfirmInterior, which re-derives sourceFaultText): the review
	// model's own door onto ordinaryDeleteConfirmText, driven through the real
	// Model, since that derivation is not centredInterior alone.
	for w := listMinWidth; w <= 200; w++ {
		for _, tc := range []struct {
			name    string
			confirm string
		}{
			{"ordinaryDelete", ordinaryDeleteConfirmText(planWithHyphenatedTitle())},
		} {
			m := &Model{
				width: w, height: 24, mode: modeConfirmDeletePlan,
				sourceFault: sourceGoneFault,
				confirm:     tc.confirm,
				styles:      st,
			}
			for _, row := range m.confirmLines() {
				if width := ansi.StringWidth(ansi.Strip(row)); width > m.deleteConfirmInterior() {
					t.Fatalf("%s via modeConfirmDeletePlan at terminal width %d: row %q is %d cells, over the %d interior", tc.name, w, row, width, m.deleteConfirmInterior())
				}
				if strings.TrimSpace(ansi.Strip(row)) == "-" {
					t.Fatalf("%s via modeConfirmDeletePlan at terminal width %d: row %q is an orphaned hyphen", tc.name, w, row)
				}
			}
		}
	}
}

// planWithHyphenatedTitle gives the ordinary delete body a hyphen to orphan.
func planWithHyphenatedTitle() domain.Plan {
	return domain.Plan{Title: "auth-redesign-and-follow-ups"}
}

func checkPanelBodyAtWidth(t *testing.T, name, text string, interior int, st *ui.Styles) {
	t.Helper()
	for gi, g := range wrapConfirmGroups(text, interior, st.FocusHeader) {
		for ri, row := range g {
			stripped := ansi.Strip(row)
			if width := ansi.StringWidth(stripped); width > interior {
				t.Fatalf("%s at interior %d: group %d row %d = %q is %d cells, over budget", name, interior, gi, ri, row, width)
			}
			if strings.TrimSpace(stripped) == "-" {
				t.Fatalf("%s at interior %d: group %d row %d = %q is an orphaned hyphen -- the rejected alternative", name, interior, gi, ri, row)
			}
		}
	}
}

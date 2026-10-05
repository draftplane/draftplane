package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/keymap"
)

// infoTestUpdated is a fixed instant constructed in a non-UTC location, so a
// test that accidentally rendered it without UTC's own conversion --
// timestamps are UTC, absolute, never relative -- would show a different
// clock time and fail.
var infoTestUpdated = time.Date(2026, 3, 4, 5, 6, 0, 0, time.FixedZone("TEST", -5*3600))

// TestInfoPanelShapeIsPinnedByteForByte pins this panel's shape, byte for
// byte: line order, labels, capitalisation, blank lines -- the keys-panel
// copy tests' own discipline (keys_test.go) applied to this panel's body.
func TestInfoPanelShapeIsPinnedByteForByte(t *testing.T) {
	plan := domain.Plan{
		ID:             "l_xyz789",
		Title:          "Auth redesign",
		SourceHint:     "/Users/alice/plans/auth.md",
		LastActivityAt: infoTestUpdated,
	}
	got := infoPanelText(planItem{plan: plan})
	want := "Auth redesign\n" +
		"Plan ID: l_xyz789\n" +
		"Last Updated: 2026-03-04 10:06 UTC\n" +
		"File: /Users/alice/plans/auth.md\n" +
		"\n" +
		"Review with:\n" +
		"\n" +
		"draftplane review /Users/alice/plans/auth.md"
	if got != want {
		t.Fatalf("infoPanelText =\n%q\nwant\n%q", got, want)
	}
}

// TestInfoPanelCopyableLineIsNotClipped drives both copyable lines with a
// path, and an id, far longer than the list's own 28-cell SOURCE column and
// asserts the FULL path/id survives verbatim on its own line -- not clipped
// with "…", not home-relativised, and with nothing else sharing the row, so
// either can be copied out of the panel without editing.
func TestInfoPanelCopyableLineIsNotClipped(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	longPath := filepath.Join(home, "development", "draftplane", "a", "very", "deeply", "nested",
		"directory", "structure", "that", "is", "considerably", "longer", "than", "the", "list",
		"columns", "own", "twenty-eight", "cell", "budget", "plan.md")

	t.Run("the file path", func(t *testing.T) {
		plan := domain.Plan{ID: "l_long", Title: "Big plan", SourceHint: longPath, LastActivityAt: infoTestUpdated}
		got := infoPanelText(planItem{plan: plan})
		lines := strings.Split(got, "\n")
		last := lines[len(lines)-1]
		want := "draftplane review " + longPath
		if last != want {
			t.Fatalf("copyable line = %q, want %q -- exactly the command, nothing else on the row", last, want)
		}
		if strings.Contains(last, "…") || strings.Contains(last, "~") {
			t.Fatalf("copyable line = %q, want no clipping and no home-relativisation", last)
		}
		if !strings.Contains(got, "File: "+longPath+"\n") {
			t.Fatalf("infoPanelText =\n%q\nwant the File: line to carry the full path too", got)
		}
	})

	longID := domain.PlanID("l_" + strings.Repeat("x", 120))
	t.Run("a sourceless plan's id", func(t *testing.T) {
		plan := domain.Plan{ID: longID, Title: "Big plan"}
		got := infoPanelText(planItem{plan: plan})
		lines := strings.Split(got, "\n")
		last := lines[len(lines)-1]
		want := "draftplane review " + string(longID)
		if last != want {
			t.Fatalf("copyable line = %q, want %q", last, want)
		}
		if !strings.Contains(got, "Plan ID: "+string(longID)+"\n") {
			t.Fatalf("infoPanelText =\n%q\nwant the Plan ID: line to carry the full id too", got)
		}
	})
}

// TestListInfoOpensAndClosesOnEscAndI drives `i` from listBrowse end to end --
// through the real dispatch, not infoPanelText called directly -- and pins that
// both esc and i again (updateInfo's own toggle, listKeys' identical shape)
// return to listBrowse.
func TestListInfoOpensAndClosesOnEscAndI(t *testing.T) {
	m := newLoadedList(t, 3)
	it, ok := m.selectedPlan()
	if !ok {
		t.Fatal("test setup: no row under the cursor")
	}
	want := infoPanelText(it)

	m = pressList(m, "i")
	if m.mode != listInfo {
		t.Fatalf("mode after i = %v, want listInfo", m.mode)
	}
	if m.confirm != want {
		t.Fatalf("confirm =\n%q\nwant\n%q", m.confirm, want)
	}

	m = pressList(m, "esc")
	if m.mode != listBrowse {
		t.Fatalf("mode after esc = %v, want listBrowse", m.mode)
	}

	m = pressList(m, "i")
	if m.mode != listInfo {
		t.Fatalf("mode after second i = %v, want listInfo", m.mode)
	}
	m = pressList(m, "i")
	if m.mode != listBrowse {
		t.Fatalf("mode after i again = %v, want listBrowse -- i must close its own panel like listKeys' ? does", m.mode)
	}
}

// TestListInfoDoesNothingOffAPlanRow mirrors ActRename/ActDelete's own
// precedent in updateBrowse: a row-scoped action the cursor cannot resolve to
// a plan is silently inert, not a refusal.
func TestListInfoDoesNothingOffAPlanRow(t *testing.T) {
	m := NewList(nil, keymap.Default(), nil)
	m.width, m.height = 100, 30
	m = pressList(m, "i")
	if m.mode != listBrowse {
		t.Fatalf("mode after i on an empty list = %v, want listBrowse", m.mode)
	}
}

// TestReviewViewDoesNotDispatchActInfo pins, as an enforced property rather
// than a remembered comment, that `i` is deliberately not dispatched here --
// the same discipline the panel-table tests apply to a whole table, applied
// to a single key instead: `i` is a list action on a plan-as-object, and the
// review model has no row for it to be about.
func TestReviewViewDoesNotDispatchActInfo(t *testing.T) {
	f := setup(t)
	m := openModel(t, f)
	before := m.mode
	m2, cmd := m.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})
	m = m2.(*Model)
	if m.mode != before {
		t.Fatalf("mode after i in the review view = %v, want unchanged %v -- ActInfo is a list-only action", m.mode, before)
	}
	if cmd != nil {
		t.Fatalf("i in the review view dispatched a command, want nil -- ActInfo has no case here")
	}
}

// TestInfoPanelOmitsEveryFactThePlanDoesNotCarry is this panel's standing
// constraint driven rather than stated: "no row may state a fact the plan does
// not carry -- a blank label is worse than an absent line."
//
// ITS ROW IS ORDINARY RATHER THAN CONTRIVED: a plan MCP `save` created with no
// source argument, which nothing has happened on yet. Interpolating its empty
// SourceHint once produced the literal "draftplane review " under a heading
// that says to run it -- the defect that made infoPanelLine necessary rather
// than merely tidy -- so the command must fall back to the id, the only
// address such a plan has.
func TestInfoPanelOmitsEveryFactThePlanDoesNotCarry(t *testing.T) {
	bare := planItem{plan: domain.Plan{ID: "l_abc123", Title: "Bare"}}
	got := infoPanelText(bare)
	for _, absent := range []string{"Last Updated:", "File:"} {
		if strings.Contains(got, absent) {
			t.Errorf("panel = %q names %q for a plan carrying none of it", got, absent)
		}
	}
	for _, present := range []string{"Bare", "Plan ID: l_abc123", "draftplane review l_abc123"} {
		if !strings.Contains(got, present) {
			t.Errorf("panel = %q is missing %q, which this plan DOES carry", got, present)
		}
	}
	if strings.Contains(got, "draftplane review \n") || strings.HasSuffix(got, "draftplane review ") {
		t.Errorf("panel = %q offers an empty command", got)
	}
}

// TestInfoPanelOnAURLSourcedPlanOffersThePlanID covers client.URLSource's
// other branch: a plan whose SourceHint names a URL rather than a file on
// this machine. openReviewTarget has no file to stat and the URL is not
// shaped like a plan id either, so `draftplane review <that URL>` fails --
// the same defect the sourceless fallback above exists to avoid. The File:
// line still shows the URL (it is the plan's one fact worth stating); only
// the copyable command swaps it for the id, exactly as the sourceless case
// does.
func TestInfoPanelOnAURLSourcedPlanOffersThePlanID(t *testing.T) {
	plan := domain.Plan{ID: "l_notion1", Title: "Imported plan", SourceHint: "notion://workspace/page123"}
	got := infoPanelText(planItem{plan: plan})
	want := "Imported plan\n" +
		"Plan ID: l_notion1\n" +
		"File: notion://workspace/page123\n" +
		"\n" +
		"Review with:\n" +
		"\n" +
		"draftplane review l_notion1"
	if got != want {
		t.Fatalf("infoPanelText =\n%q\nwant\n%q", got, want)
	}
}

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/client/recent"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/keymap"
	"github.com/draftplane/draftplane/session"
	"github.com/draftplane/draftplane/store/localcas"
	"github.com/draftplane/draftplane/theme"
	"github.com/draftplane/draftplane/ui"
)

// seedListPlan writes filename under dir and creates a plan for it through a
// real session, so the returned session's Comment/Approve calls flow through
// the same registration path production code uses.
func seedListPlan(t *testing.T, f fixture, dir, filename, title, doc string) *session.Session {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := session.Open(f.ctx, f.svc, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(f.ctx, title); err != nil {
		t.Fatal(err)
	}
	return s
}

// commentOnFirstParagraph posts body as a thread on s's first non-heading block.
func commentOnFirstParagraph(t *testing.T, f fixture, s *session.Session, body string) domain.Thread {
	t.Helper()
	blocks := ui.ParseBlocks(s.Content, nil)
	var target ui.Block
	for _, b := range blocks {
		if b.Kind != ui.KindHeading {
			target = b
			break
		}
	}
	a, err := session.AnchorForBlockText(string(s.Content), ui.BlockAnchorSpan(target), target.HeadingPath)
	if err != nil {
		t.Fatal(err)
	}
	th, err := s.Comment(f.ctx, a, body)
	if err != nil {
		t.Fatal(err)
	}
	return th
}

// pressList mirrors app_test.go's press helper for *ListModel.
func pressList(m *ListModel, keys ...string) *ListModel {
	cur := tea.Model(m)
	for _, k := range keys {
		var key tea.KeyPressMsg
		switch k {
		case "enter":
			key = tea.KeyPressMsg{Code: tea.KeyEnter}
		default:
			key = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		cur, _ = cur.Update(key)
	}
	return cur.(*ListModel)
}

// drainList mirrors app_test.go's drain helper for *ListModel.
func drainList(t *testing.T, m *ListModel, cmd tea.Cmd) *ListModel {
	t.Helper()
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			break
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				m = drainList(t, m, c)
			}
			break
		}
		cur, next := m.Update(msg)
		m = cur.(*ListModel)
		cmd = next
	}
	return m
}

func newListFixture(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	svc := newFixtureStore(t, filepath.Join(dir, "state.json"))
	return fixture{svc: svc, cas: localcas.New(filepath.Join(dir, "objects")), path: filepath.Join(dir, "unused.md"), ctx: attrCtx("alice")}
}

// TestListDerivesItems pins refresh()'s derivation: counts, the approved
// glyph's underlying signal, and sort order by lastActivity.
//
// PLAN A'S FILE IS REMOVED BEFORE THE REFRESH, and that pins a deletion: the
// derivation's counts and order do not depend on the file, so a plan whose file
// went away derives exactly as before and keeps showing its source.
// TestPlanItemSourceLabel covers the source rule's values, which fire on the
// plan's SourceHint and never on a stat.
func TestListDerivesItems(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)

	// Plan A: one open thread, no approval, file present.
	sA := seedListPlan(t, f, dir, "plan-a.md", "Plan A", "# Plan A\n\nThis is the body of plan A.\n")
	commentOnFirstParagraph(t, f, sA, "question")

	// Plan B: a resolved thread plus an open thread, and an approval on its
	// latest registered version. Created after A, so its activity sorts
	// first.
	sB := seedListPlan(t, f, dir, "plan-b.md", "Plan B", "# Plan B\n\nThis is the body of plan B.\n")
	resolvedTh := commentOnFirstParagraph(t, f, sB, "resolved note")
	if err := sB.Resolve(f.ctx, resolvedTh.ID, true); err != nil {
		t.Fatal(err)
	}
	commentOnFirstParagraph(t, f, sB, "still open")
	if err := sB.Approve(f.ctx); err != nil {
		t.Fatal(err)
	}

	// Plan A's local file disappears: nothing in the derivation reads the
	// filesystem, so the row is unchanged and keeps showing its origin.
	if err := os.Remove(sA.Path); err != nil {
		t.Fatal(err)
	}

	m := NewList(f.svc, keymap.Default(), nil)
	m = drainList(t, m, m.Init())

	if len(m.items) != 2 {
		t.Fatalf("items = %d, want 2", len(m.items))
	}

	var itemA, itemB planItem
	for _, it := range m.items {
		switch it.plan.Title {
		case "Plan A":
			itemA = it
		case "Plan B":
			itemB = it
		}
	}

	if itemA.open != 1 || itemA.total != 1 {
		t.Fatalf("plan A open/total = %d/%d, want 1/1", itemA.open, itemA.total)
	}
	if itemA.approvedTip {
		t.Fatal("plan A has no approval; approvedTip must be false")
	}
	if got, want := itemA.sourceLabel(60, lipgloss.NewStyle()), ansi.Truncate(homeRelative(itemA.plan.SourceHint), 60, "…"); got != want {
		t.Fatalf("plan A source label = %q, want %q (the source survives the file's removal)", got, want)
	}

	if itemB.open != 1 || itemB.total != 2 {
		t.Fatalf("plan B open/total = %d/%d, want 1/2 (one resolved, one open)", itemB.open, itemB.total)
	}
	if !itemB.approvedTip {
		t.Fatal("plan B's latest version was approved; approvedTip must be true")
	}

	// Plan B's activity all happened after plan A's, so B sorts first.
	if m.items[0].plan.Title != "Plan B" {
		t.Fatalf("items[0] = %q, want Plan B (most recent activity first)", m.items[0].plan.Title)
	}
}

// listOfSvc answers ListPlans with a fixed set and panics on everything else
// through the nil embedded interface: the plans a test needs, with no store
// behind them.
type listOfSvc struct {
	client.PlanService
	plans []domain.Plan
}

func (l listOfSvc) ListPlans(context.Context) ([]domain.Plan, error) { return l.plans, nil }

// TestListCopyIsPinnedByLiteral compares the plan list's own vocabulary against
// HAND-WRITTEN LITERALS, and it exists because every other check on these words
// compares a constant to itself.
//
// ⚠️ THE DEFECT IT CLOSES WAS MEASURED, not imagined: renaming one of these
// constants failed ZERO tests, because every test that finds the words on screen
// searches the frame for THE CONSTANT, so both sides of the comparison move
// together under a rename.
//
// THIS TEST SUPPLIES ONLY THE MISSING LEG, deliberately, and does not re-drive
// the renderer: the tests that find these words on screen prove
// CONSTANT-reaches-SCREEN, and this one proves CONSTANT-EQUALS-LITERAL, so the two
// together give literal-reaches-screen without a second frame drive here.
func TestListCopyIsPinnedByLiteral(t *testing.T) {
	for _, tt := range []struct {
		what      string
		got, want string
	}{
		{"the catalog's band", listMineSectionTitle, "Your plans"},
		{"the working set's band", listRecentSectionTitle, "Recently opened"},
		{`the header over "Your plans"' trailing region`, listSourceLabel, "SOURCE"},
		{"the header over Recently opened's trailing region", listOpenedLabel, "OPENED"},
	} {
		t.Run(tt.want, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s reads %q, want exactly %q -- this is user-facing copy and is pinned by literal, not by containment", tt.what, tt.got, tt.want)
			}
		})
	}
}

// TestPlanItemSourceLabel pins the source rule directly: a non-empty
// SourceHint always displays verbatim — a notion:// source is not a readable
// file and never will be, but it is still the plan's source and must show — and
// a blank one reads "draftplane", the store that holds the plan.
//
// NO ROW SETS A FILE UP ON DISK, and that is the rule's own claim rather than
// a shortcut this test takes: the label is the hint, never
// os.Stat(SourceHint).
func TestPlanItemSourceLabel(t *testing.T) {
	// A path long enough that clipping to a 60-cell budget actually engages --
	// this pins sourceLabel's wiring: it clips with the width IT was given, and
	// degrades toward the LEADING directories rather than the filename, which
	// is the reversal this column exists to carry.
	const longPath = "/Users/priya/development/roci-crew/plans/quarterly-planning-and-followups.md"

	tests := []struct {
		name       string
		sourceHint string
		id         domain.PlanID
		want       string
	}{
		{"a file-backed plan shows its path", "/plan.md", "l_local1", "/plan.md"},
		{"a plan with a URL source shows the URI", "notion://abc123", "l_notion1", "notion://abc123"},
		{"a sourceless plan shows draftplane", "", "l_sourceless1", "draftplane"},
		{"a long path clips from the right, keeping its leading directories", longPath, "l_longpath1", ansi.Truncate(longPath, 60, "…")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := planItem{plan: domain.Plan{ID: tt.id, SourceHint: tt.sourceHint}}
			got := it.sourceLabel(60, lipgloss.NewStyle())
			if got != tt.want {
				t.Fatalf("sourceLabel = %q, want %q", got, tt.want)
			}
			if tt.sourceHint == longPath {
				// The HEAD is what has to survive now. Asserting on a prefix
				// of the input rather than on the whole rendered string is
				// what keeps this about the direction of the clip.
				if !strings.HasPrefix(got, "/Users/priya/development/roci-crew/") {
					t.Fatalf("sourceLabel = %q, want it to keep the leading directories", got)
				}
				if strings.HasSuffix(got, filepath.Base(longPath)) {
					t.Fatalf("sourceLabel = %q kept the filename tail; the clip is meant to drop it", got)
				}
			}
		})
	}
}

// TestListRenameRoundTrip: e opens a prefilled rename input, ctrl+d commits it
// through RenamePlan and the list re-renders, and esc leaves the title alone.
func TestListRenameRoundTrip(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	seedListPlan(t, f, dir, "plan.md", "Original Title", "# Original Title\n\nBody.\n")

	m := NewList(f.svc, keymap.Default(), nil)
	m.width, m.height = 100, 30
	m = drainList(t, m, m.Init())
	if len(m.items) != 1 {
		t.Fatalf("items = %d, want 1", len(m.items))
	}

	m = pressList(m, "e")
	if m.mode != listRename {
		t.Fatalf("mode = %v, want listRename", m.mode)
	}
	if m.ta.Value() != "Original Title" {
		t.Fatalf("ta = %q, want prefilled with the current title", m.ta.Value())
	}

	m = pressList(m, "!") // cursor starts at the end (CursorEnd), so this appends
	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m = drainList(t, cur.(*ListModel), cmd)

	if m.mode != listBrowse {
		t.Fatal("commit must return to browse mode")
	}
	plans, err := f.svc.ListPlans(f.ctx)
	if err != nil || len(plans) != 1 || plans[0].Title != "Original Title!" {
		t.Fatalf("plans = %+v, %v, want the renamed title", plans, err)
	}
	if len(m.items) != 1 || m.items[0].plan.Title != "Original Title!" {
		t.Fatalf("list must re-render the new title, got %+v", m.items)
	}

	m = pressList(m, "e", "?")
	m = pressList(m, "esc")
	if m.mode != listBrowse {
		t.Fatal("esc must return to browse mode")
	}
	plans, err = f.svc.ListPlans(f.ctx)
	if err != nil || plans[0].Title != "Original Title!" {
		t.Fatalf("esc must not persist edits, got title %q (err %v)", plans[0].Title, err)
	}
}

// TestListRenameUnEditedCommitRoundTripsU_FE0FButPicturesAControlByte: opening
// rename on a title carrying U+FE0F and committing without editing must
// persist that title BYTE-EXACT, not the stripSelector16 answer, because
// updateRename's ctrl+d commits m.ta.Value() through svc.RenamePlan -- so
// enterRename's prefill is the one VisibleControls caller whose output is
// PERSISTED rather than merely painted. A title carrying a raw control byte
// must still prefill and commit as its Control Picture.
func TestListRenameUnEditedCommitRoundTripsU_FE0FButPicturesAControlByte(t *testing.T) {
	for _, tc := range []struct {
		name       string
		title      string
		wantTA     string
		wantCommit string
	}{
		{
			name:       "a title carrying U+FE0F round-trips byte-exact",
			title:      "Warning ⚠️ Launch",
			wantTA:     "Warning ⚠️ Launch",
			wantCommit: "Warning ⚠️ Launch",
		},
		{
			name:       "a title carrying a raw control byte still commits as its Control Picture",
			title:      "Before\rAfter",
			wantTA:     "Before␍After",
			wantCommit: "Before␍After",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newListFixture(t)
			dir := filepath.Dir(f.path)
			seedListPlan(t, f, dir, "plan.md", tc.title, "# "+tc.title+"\n\nBody.\n")

			m := NewList(f.svc, keymap.Default(), nil)
			m.width, m.height = 100, 30
			m = drainList(t, m, m.Init())
			if len(m.items) != 1 {
				t.Fatalf("items = %d, want 1", len(m.items))
			}

			m = pressList(m, "e")
			if m.mode != listRename {
				t.Fatalf("mode = %v, want listRename", m.mode)
			}
			if got := m.ta.Value(); got != tc.wantTA {
				t.Fatalf("prefill = %q, want %q", got, tc.wantTA)
			}

			// Commit UNEDITED: what ctrl+d persists when nothing was changed.
			cur, cmd := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
			m = drainList(t, cur.(*ListModel), cmd)
			if m.mode != listBrowse {
				t.Fatal("commit must return to browse mode")
			}

			plans, err := f.svc.ListPlans(f.ctx)
			if err != nil || len(plans) != 1 {
				t.Fatalf("plans = %+v, %v, want one plan", plans, err)
			}
			if plans[0].Title != tc.wantCommit {
				t.Fatalf("committed title = %q, want %q", plans[0].Title, tc.wantCommit)
			}
		})
	}
}

// TestListRenameRefusedWhileInFlightKeepsText pins the non-lossy refusal path
// unique to rename: unlike confirm-delete, rename's panel opens
// unconditionally (read-only until its own ctrl+d commits, exactly what
// compose's post button does now instead), so it can stay open with a draft
// while some other write is in flight. Committing that draft must refuse
// without discarding the typed text.
func TestListRenameRefusedWhileInFlightKeepsText(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	seedListPlan(t, f, dir, "plan-a.md", "Plan A", "# Plan A\n\nBody of plan A goes here.\n")
	seedListPlan(t, f, dir, "plan-b.md", "Plan B", "# Plan B\n\nBody of plan B goes here.\n")

	m := NewList(f.svc, keymap.Default(), nil)
	m.width, m.height = 100, 30
	m = drainList(t, m, m.Init())
	if len(m.items) != 2 {
		t.Fatalf("items = %d, want 2", len(m.items))
	}

	// Dispatch a delete on the cursor's plan but hold its cmd undelivered,
	// so inFlight stays true for the rest of this test.
	m = pressList(m, "d")
	cur, held := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = cur.(*ListModel)
	if !m.inFlight {
		t.Fatal("dispatching delete must claim inFlight")
	}

	// Move onto the surviving plan before opening rename: the delete above
	// targets whichever plan the cursor was on, and renaming that same plan
	// would race the delete legitimately removing it.
	m = pressList(m, "j")
	m = pressList(m, "e")
	if m.mode != listRename {
		t.Fatalf("mode = %v, want listRename while a write is in flight", m.mode)
	}
	m = pressList(m, "!")

	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m = cur.(*ListModel)
	if cmd != nil {
		t.Fatal("rename commit must not dispatch while another write is in flight")
	}
	if m.mode != listRename {
		t.Fatal("refused commit must stay in rename")
	}
	if !strings.HasSuffix(m.ta.Value(), "!") {
		t.Fatalf("ta = %q, refused commit must keep the typed text", m.ta.Value())
	}
	if !strings.Contains(m.status, "in progress") {
		t.Fatalf("status = %q, want an in-progress hint", m.status)
	}

	// Deliver the held delete, then retry the commit: it must land.
	m = drainList(t, m, held)
	if m.inFlight {
		t.Fatal("inFlight must clear once the delete's result lands")
	}
	wantTitle := m.ta.Value()
	cur, cmd = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m = drainList(t, cur.(*ListModel), cmd)
	if m.mode != listBrowse {
		t.Fatal("accepted commit must return to browse mode")
	}
	found := false
	for _, it := range m.items {
		if it.plan.Title == wantTitle {
			found = true
		}
	}
	if !found {
		t.Fatalf("retried rename must land, want a plan titled %q among %+v", wantTitle, m.items)
	}
}

// TestListDeleteConfirmAndRefusal pins the delete flow: d names the plan with
// the imported ordinaryDeleteConfirmText wording (no count -- counts are ruled
// out of this panel), n cancels without touching the plan, y dispatches
// DeletePlan, and a second d while that delete is in flight is refused.
func TestListDeleteConfirmAndRefusal(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	s := seedListPlan(t, f, dir, "plan.md", "Doomed Plan", "# Doomed Plan\n\nThis plan is doomed to be deleted.\n")
	commentOnFirstParagraph(t, f, s, "a thread")

	m := NewList(f.svc, keymap.Default(), nil)
	m.width, m.height = 100, 30
	m = drainList(t, m, m.Init())
	if len(m.items) != 1 {
		t.Fatalf("items = %d, want 1", len(m.items))
	}

	m = pressList(m, "d")
	if m.mode != listConfirmDelete {
		t.Fatalf("mode = %v, want listConfirmDelete", m.mode)
	}
	if want := ordinaryDeleteConfirmText(m.items[0].plan); m.confirm != want {
		t.Fatalf("confirm = %q, want %q (the imported ordinaryDeleteConfirmText wording)", m.confirm, want)
	}

	// n cancels, plan intact.
	m = pressList(m, "n")
	if m.mode != listBrowse {
		t.Fatal("n must cancel back to browse")
	}
	plans, err := f.svc.ListPlans(f.ctx)
	if err != nil || len(plans) != 1 {
		t.Fatalf("n must not delete the plan: plans=%v err=%v", plans, err)
	}

	// y dispatches the delete; hold its cmd undelivered to exercise the
	// in-flight refusal.
	m = pressList(m, "d")
	cur, held := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = cur.(*ListModel)
	if !m.inFlight {
		t.Fatal("dispatching delete must claim inFlight immediately")
	}

	cur2, cmd2 := m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m2 := cur2.(*ListModel)
	if cmd2 != nil {
		t.Fatal("a second delete-confirm while one is in flight must not dispatch")
	}
	if m2.mode != listBrowse {
		t.Fatal("refused d must not open a second confirm panel")
	}
	if !strings.Contains(m2.status, "in progress") {
		t.Fatalf("status = %q, want an in-progress hint", m2.status)
	}

	m = drainList(t, m, held)
	if m.inFlight {
		t.Fatal("inFlight must clear once the delete's result lands")
	}
	plans, err = f.svc.ListPlans(f.ctx)
	if err != nil || len(plans) != 0 {
		t.Fatalf("y must delete the plan from the service: plans=%v err=%v", plans, err)
	}
	if len(m.items) != 0 {
		t.Fatal("y must remove the plan from the list")
	}
}

// TestListEnterRefusedWhileWriteInFlight pins openSelected's dispatch
// discipline: enter while a rename/delete write is in flight must refuse
// rather than emit msgOpenPlan — under Root, opening switches the active child
// to the review, so the write's msgListActionDone would be routed there and
// dropped, leaving inFlight (cleared only by handleListActionDone) stuck true.
func TestListEnterRefusedWhileWriteInFlight(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	seedListPlan(t, f, dir, "plan-a.md", "Plan A", "# Plan A\n\nBody of plan A goes here.\n")
	seedListPlan(t, f, dir, "plan-b.md", "Plan B", "# Plan B\n\nBody of plan B goes here.\n")

	m := NewList(f.svc, keymap.Default(), nil)
	m.width, m.height = 100, 30
	m = drainList(t, m, m.Init())

	// Dispatch a delete and hold its cmd undelivered, so inFlight stays true.
	m = pressList(m, "d")
	cur, held := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = cur.(*ListModel)
	if !m.inFlight {
		t.Fatal("test setup: dispatching delete must claim inFlight")
	}

	m = pressList(m, "j")
	cur, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = cur.(*ListModel)
	if cmd != nil {
		t.Fatal("enter while a write is in flight must not emit msgOpenPlan")
	}
	if !strings.Contains(m.status, "in progress") {
		t.Fatalf("status = %q, want an in-progress hint", m.status)
	}

	// The write resolves; the very next enter dispatches normally.
	m = drainList(t, m, held)
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter must dispatch once the write resolved")
	}
	if _, ok := cmd().(msgOpenPlan); !ok {
		t.Fatal("enter's cmd must produce msgOpenPlan")
	}
}

// TestListStateChangedRefreshes: a plan another actor creates over a second
// store handle on the same state file shows up after msgStateChanged.
func TestListStateChangedRefreshes(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	seedListPlan(t, f, dir, "plan-a.md", "Plan A", "# Plan A\n\nBody.\n")

	m := NewList(f.svc, keymap.Default(), nil)
	m.width, m.height = 100, 30
	m = drainList(t, m, m.Init())
	if len(m.items) != 1 {
		t.Fatalf("items = %d, want 1 before the second store's write", len(m.items))
	}

	f2 := secondFixtureStore(t, f)
	seedListPlan(t, f2, dir, "plan-b.md", "Plan B", "# Plan B\n\nBody.\n")

	cur, cmd := m.Update(msgStateChanged{})
	m = drainList(t, cur.(*ListModel), cmd)

	if len(m.items) != 2 {
		t.Fatalf("items = %d after state change, want 2 (the new plan must appear)", len(m.items))
	}
}

// TestListBrowseCtrlRRefreshesWithoutStateChanged pins the manual-reload
// fallback: ctrl+r in browse mode dispatches refreshCmd directly, so a second
// actor's write shows up even when no msgStateChanged ever arrives — the case a
// fresh install hits before its first write creates the state directory the
// watch needs, and the case any watch failure (an unsupported filesystem)
// degrades to permanently.
func TestListBrowseCtrlRRefreshesWithoutStateChanged(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	seedListPlan(t, f, dir, "plan-a.md", "Plan A", "# Plan A\n\nBody.\n")

	m := NewList(f.svc, keymap.Default(), nil)
	m.width, m.height = 100, 30
	m = drainList(t, m, m.Init())
	if len(m.items) != 1 {
		t.Fatalf("items = %d, want 1 before the second store's write", len(m.items))
	}

	f2 := secondFixtureStore(t, f)
	seedListPlan(t, f2, dir, "plan-b.md", "Plan B", "# Plan B\n\nBody.\n")

	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+r in browse mode must dispatch a refresh cmd")
	}
	m = drainList(t, cur.(*ListModel), cmd)

	if len(m.items) != 2 {
		t.Fatalf("items = %d after ctrl+r, want 2 (the new plan must appear without msgStateChanged)", len(m.items))
	}
}

// TestListStateChangedPreservesCursorIdentity pins that the cursor follows
// the plan the user actually had selected across a live resort, not the
// numeric index it happened to occupy: the list resorts by lastActivity on
// every refresh, so an agent bumping some OTHER plan's activity over MCP
// must not silently retarget e/d/enter at a plan the user never looked at.
func TestListStateChangedPreservesCursorIdentity(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	// Created A, B, C, so the initial lastActivity sort is [C, B, A].
	seedListPlan(t, f, dir, "plan-a.md", "Plan A", "# Plan A\n\nBody of plan A goes here.\n")
	seedListPlan(t, f, dir, "plan-b.md", "Plan B", "# Plan B\n\nBody of plan B goes here.\n")
	seedListPlan(t, f, dir, "plan-c.md", "Plan C", "# Plan C\n\nBody of plan C goes here.\n")

	m := NewList(f.svc, keymap.Default(), nil)
	m.width, m.height = 100, 30
	m = drainList(t, m, m.Init())
	if len(m.items) != 3 {
		t.Fatalf("items = %d, want 3", len(m.items))
	}
	if m.items[0].plan.Title != "Plan C" || m.items[2].plan.Title != "Plan A" {
		t.Fatalf("initial sort = %v, want [C, B, A] by lastActivity desc", titlesOf(m.items))
	}

	// Select Plan B (index 1).
	m = pressList(m, "j")
	if it, ok := m.selectedPlan(); !ok || it.plan.Title != "Plan B" {
		t.Fatalf("cursor = %d (%q, ok=%v), want Plan B", m.cursor, it.plan.Title, ok)
	}

	// A second actor bumps Plan A's activity (an MCP comment), which sorts
	// it ahead of both B and C without touching B at all.
	f2 := secondFixtureStore(t, f)
	sA, err := session.Open(f2.ctx, f2.svc, filepath.Join(dir, "plan-a.md"))
	if err != nil {
		t.Fatal(err)
	}
	commentOnFirstParagraph(t, f2, sA, "a fresh comment from another actor")

	cur, cmd := m.Update(msgStateChanged{})
	m = drainList(t, cur.(*ListModel), cmd)

	if m.items[0].plan.Title != "Plan A" {
		t.Fatalf("post-refresh sort = %v, want Plan A first (its activity was just bumped)", titlesOf(m.items))
	}
	it, ok := m.selectedPlan()
	if !ok || it.plan.Title != "Plan B" {
		t.Fatalf("cursor now selects %q (ok=%v) at row %d, want it to have followed Plan B to its new position", it.plan.Title, ok, m.cursor)
	}
}

func titlesOf(items []planItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.plan.Title
	}
	return out
}

// TestListStaleRefreshDoesNotClobberNewerState pins refreshCmd's ordering
// guard: msgStateChanged and handleListActionDone can each dispatch a fresh
// refresh while an earlier one is still outstanding, and bubbletea gives no
// guarantee the results land in dispatch order. A stale result arriving after
// a later one already landed must be dropped, not revert the list.
func TestListStaleRefreshDoesNotClobberNewerState(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	seedListPlan(t, f, dir, "plan-a.md", "Plan A", "# Plan A\n\nBody of plan A goes here.\n")

	m := NewList(f.svc, keymap.Default(), nil)
	m.width, m.height = 100, 30
	m = drainList(t, m, m.Init())
	if len(m.items) != 1 {
		t.Fatalf("items = %d, want 1", len(m.items))
	}
	base := m.appliedRefreshSeq

	newerItems := []planItem{m.items[0], {plan: domain.Plan{ID: "p-new", Title: "Plan New"}}}
	olderItems := []planItem{m.items[0]}

	// The newer-dispatched refresh's result lands first...
	cur, _ := m.Update(msgListRefreshed{seq: base + 2, items: newerItems})
	m = cur.(*ListModel)
	if len(m.items) != 2 {
		t.Fatalf("items = %d after the newer refresh landed, want 2", len(m.items))
	}

	// ...then a stale, older-dispatched refresh's result arrives late. It
	// must not revert the list to what it saw before the newer one ran.
	cur, _ = m.Update(msgListRefreshed{seq: base + 1, items: olderItems})
	m = cur.(*ListModel)
	if len(m.items) != 2 {
		t.Fatalf("items = %d after a stale refresh landed late, want the newer state (2) preserved", len(m.items))
	}
}

// TestListEmptyHintNeverExceedsWidth pins the width discipline of an empty
// store's own rows — the band, the column-label row and the empty-state hint,
// the widest content this view holds at a narrow terminal
// (listEmptyHint alone is 74 cells. A figure in a doc comment with nothing
// deriving it is the shape this repo keeps meeting, and the sweep below is
// what actually holds the property, so the number is narrative and is
// corrected rather than made load-bearing). The widths swept stop at
// listMinWidth because narrower than that is the minimum-size gate's screen,
// not this one — TestListRefusesBelowItsMinimumSize measures that one's
// width instead.
//
// EVERY LINE IS MEASURED, INCLUDING THE TWO BARS: the browse help line is 42
// cells at every width, so exempting it would let it overflow at 40 and at the
// gate's own minimum, and an overflowing line wraps and adds a row nothing
// budgeted.
func TestListEmptyHintNeverExceedsWidth(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{80, 40, listMinWidth} {
		m := NewList(nil, keymap.Default(), nil)
		m.width, m.height = width, 15
		for i, line := range strings.Split(m.View().Content, "\n") {
			if w := ansi.StringWidth(ansi.Strip(line)); w > width {
				t.Fatalf("width %d: empty-store line %d display width = %d, exceeds width (%q)", width, i, w, line)
			}
		}

		mp := NewList(nil, keymap.Default(), th)
		mp.width, mp.height = width, 15
		for i, line := range strings.Split(mp.View().Content, "\n") {
			if w := ansi.StringWidth(ansi.Strip(line)); w != width {
				t.Fatalf("painted width %d: empty-store line %d display width = %d, want exactly %d (%q)", width, i, w, width, ansi.Strip(line))
			}
		}
	}
}

// TestTheListIsRecentlyOpenedThenYourPlans pins the list's whole band set over
// a real store with two plans and a recent.json naming one of them: Recently
// opened above Your plans wherever there is room for it, Your plans alone where
// there is not, and nothing else at either size. The set is read off every
// band the body builds and then found on the drawn screen, so an extra band
// fails it as surely as a missing one.
func TestTheListIsRecentlyOpenedThenYourPlans(t *testing.T) {
	f := newListFixture(t)
	dir := t.TempDir()
	alpha := seedListPlan(t, f, dir, "alpha.md", "alpha plan", "# Alpha\n\nbody\n")
	seedListPlan(t, f, dir, "beta.md", "beta plan", "# Beta\n\nbody\n")
	recents := writeRecents(t, recent.Entry{PlanID: alpha.Plan.ID})

	for _, tc := range []struct {
		width, height int
		want          []string
	}{
		{listMinWidth, listMinHeight, []string{listMineSectionTitle}},
		{80, 24, []string{listRecentSectionTitle, listMineSectionTitle}},
	} {
		t.Run(fmt.Sprintf("%dx%d", tc.width, tc.height), func(t *testing.T) {
			m := NewList(f.svc, keymap.Default(), nil)
			m.SetRecents(recents)
			m.files = untimedFileCheck()
			m.now = func() time.Time { return listFixtureEpoch }
			cur, _ := m.Update(tea.WindowSizeMsg{Width: tc.width, Height: tc.height})
			m = cur.(*ListModel)
			m = drainList(t, m, m.Init())

			var bands []string
			for _, r := range m.rows {
				if r.kind == rowBand && r.head {
					bands = append(bands, r.text)
				}
			}
			if !slices.Equal(bands, tc.want) {
				t.Fatalf("bands = %q, want %q", bands, tc.want)
			}
			view := ansi.Strip(m.View().Content)
			for _, b := range bands {
				if !strings.Contains(view, b) {
					t.Fatalf("band %q is built but not drawn:\n%s", b, view)
				}
			}
		})
	}
}

// TestListBrowseChromeIsUnconditional pins the chrome invariant: browse mode
// draws the masthead, "Your plans"' band and its column-label row whether the
// store holds nothing or a plan. The labels belong to the section, and the
// section is drawn whether or not it has anything in it. A conditional band is
// the layout jump viewHeight's own masthead reservation exists to prevent, one
// level down.
func TestListBrowseChromeIsUnconditional(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	mine := planItem{plan: domain.Plan{ID: "l_mine", Title: "Mine"}}

	for _, tc := range []struct {
		name  string
		items []planItem
	}{
		{"empty store", nil},
		{"one plan", []planItem{mine}},
	} {
		for _, tm := range []*theme.Theme{nil, th} {
			t.Run(fmt.Sprintf("%s/painted=%v", tc.name, tm != nil), func(t *testing.T) {
				m := NewList(nil, keymap.Default(), tm)
				m.width, m.height = 80, 24
				m.applyRefresh(tc.items)
				content := ansi.Strip(m.View().Content)
				for _, want := range []string{"Draftplane", listMineSectionTitle, "NAME", "COMMENTS", listSourceLabel} {
					if !strings.Contains(content, want) {
						t.Fatalf("view is missing %q:\n%s", want, content)
					}
				}
				// With nothing selectable anywhere, no row wears the cursor
				// band: snapCursor parks the cursor on row 0, which is a
				// section band, and painting it there would offer a selection
				// no gesture can act on (cursorOn).
				if tm == nil && len(tc.items) == 0 && strings.Contains(content, "▌") {
					t.Fatalf("an empty store draws a cursor band on chrome:\n%s", content)
				}
			})
		}
	}
}

// TestListHeaderRowAlignsWithDataColumns positionally pins the header's
// column offsets against a real data row's own column math, not just widths:
// NAME must start exactly where the title column starts (offset 3, past the
// band+glyph cells), COMMENTS where the counts column starts, and each
// section's own trailing-region label where that section's rows put their
// own trailing cell.
//
// THE TRAILING REGION IS MEASURED PER SECTION because its label is the
// section's -- SOURCE over "Your plans", OPENED over Recently opened -- and the
// masthead block's own header row (rename's, drawn over the flat list of every
// plan) labels it SOURCE. A label one cell off its column is invisible to a
// width check.
//
// EVERY SECTION STARTS THE REGION AT THE SAME CELL: lastStart is computed
// ONCE below and used for every case, never re-derived per section, which is
// what a section-aware listColumnWidths could not promise.
//
// IT OPENS WITH TWO LITERALS, AND THEY ARE THE ONLY ASSERTIONS HERE THAT
// DERIVE FROM NOTHING: every other offset is computed from the constants the
// renderer uses, so they all slide when one moves. Reverting listLastGapWidth
// to listGapWidth leaves this test, and the whole ./app package, green while
// both section headers render "COMMENTSSOURCE"/"COMMENTSOPENED".
func TestListHeaderRowAlignsWithDataColumns(t *testing.T) {
	m := NewList(nil, keymap.Default(), nil)
	m.width = 80

	// The derivation listLastGapWidth was chosen by: "COMMENTS" spends the
	// counts column and its gap, so the span must be wider than the word for
	// a separating cell to exist at all.
	if listCountsWidth+listLastGapWidth <= len("COMMENTS") {
		t.Fatalf("the counts span is %d cells for an %d-cell label -- the next label starts in the cell COMMENTS ends in",
			listCountsWidth+listLastGapWidth, len("COMMENTS"))
	}
	// The same fact as a reader sees it. Written out rather than assembled from
	// the label constants, so a renamed label cannot rewrite what this demands.
	for _, width := range []int{80, 40} {
		mw := NewList(nil, keymap.Default(), nil)
		mw.width = width
		for section, want := range map[listSection]string{
			sectionMine:   "COMMENTS SOURCE",
			sectionRecent: "COMMENTS OPENED",
		} {
			labels := ansi.Strip(mw.renderBodyRowPainted(mw.sectionHead(section)[1], false))
			if !strings.Contains(labels, want) {
				t.Fatalf("width %d: %s labels read %q, want them separated: %q", width, section.title(), labels, want)
			}
		}
	}

	titleStart := listBandWidth + listGlyphWidth
	// m.rowWidth(), not min(80, listMaxRowWidth): a row's content is narrower
	// than the terminal by the two ground margins either side of it.
	titleWidth, _ := listColumnWidths(m.rowWidth())
	countsStart := titleStart + titleWidth + listGapWidth
	lastStart := countsStart + listCountsWidth + listLastGapWidth

	header := m.listHeaderRow()
	if idx := strings.Index(header, "NAME"); idx != titleStart {
		t.Fatalf("NAME at %d, want %d (title column start)", idx, titleStart)
	}
	if idx := strings.Index(header, "COMMENTS"); idx != countsStart {
		t.Fatalf("COMMENTS at %d, want %d (counts column start)", idx, countsStart)
	}
	if idx := strings.Index(header, listSourceLabel); idx != lastStart {
		t.Fatalf("%s at %d, want %d (the trailing region's start)", listSourceLabel, idx, lastStart)
	}

	// Each section's own column-label row is the same labels through the same
	// helper with one cell less lead, because renderBodyRowPainted prepends
	// the cursor-band cell itself. Measured after that prepend, against that
	// section's own row, so labels and cells cannot drift apart.
	opened := listFixtureEpoch.Add(-3 * time.Hour)
	m.now = func() time.Time { return listFixtureEpoch }
	plan := planItem{plan: domain.Plan{ID: "l_pub", Title: "Plan"}}
	for _, tc := range []struct {
		section   listSection
		row       row
		wantLabel string
		wantCell  string
	}{
		{sectionMine, row{kind: rowPlan, item: plan, section: sectionMine}, listSourceLabel, "draftplane"},
		{sectionRecent, row{kind: rowPlan, item: plan, section: sectionRecent, opened: opened}, listOpenedLabel, "3h ago"},
	} {
		t.Run(tc.section.title(), func(t *testing.T) {
			// Margin-trimmed: renderBodyRowPainted returns a whole row, which
			// opens with the left ground margin, while titleStart and the
			// column starts below it are content-relative.
			labels := strings.TrimPrefix(ansi.Strip(m.renderBodyRowPainted(m.sectionHead(tc.section)[1], false)), strings.Repeat(" ", m.listMargin()))
			if idx := strings.Index(labels, "NAME"); idx != titleStart {
				t.Fatalf("NAME is at %d, want %d (the same column the masthead block's copy and the plan rows use)", idx, titleStart)
			}
			if idx := strings.Index(labels, "COMMENTS"); idx != countsStart {
				t.Fatalf("COMMENTS is at %d, want %d", idx, countsStart)
			}
			if idx := strings.Index(labels, tc.wantLabel); idx != lastStart {
				t.Fatalf("%s is at %d, want %d (its own column's start)", tc.wantLabel, idx, lastStart)
			}

			// Display-cell offset, not a byte offset: the plan-state glyph
			// earlier in the row is a multi-byte rune, so strings.Index's raw
			// byte position would overcount against the column math above.
			// Margin-trimmed for the same reason the labels row above is.
			planLine := strings.TrimPrefix(ansi.Strip(m.renderBodyRowPainted(tc.row, false)), strings.Repeat(" ", m.listMargin()))
			byteIdx := strings.Index(planLine, tc.wantCell)
			if byteIdx < 0 {
				t.Fatalf("%q missing from row: %q", tc.wantCell, planLine)
			}
			if idx := ansi.StringWidth(planLine[:byteIdx]); idx != lastStart {
				t.Fatalf("row's last cell at %d, want %d (under its own label)", idx, lastStart)
			}
		})
	}
}

// TestListHeaderRowNeverExceedsWidth pins the header row's own width
// discipline across widths {160, 80, 40, 24}, mirroring
// TestListRowsNeverExceedWidth for data rows: the header row's own TEXT is
// capped at rowWidth = min(width, 120), and the header LINE as it reaches the
// screen — capped row plus surplus Doc fill — comes out to exactly m.width.
// The two loops below are those two subjects, not two render paths.
//
// THE FULL-LINE HALF IS DRIVEN IN A PANEL MODE, which is where the masthead
// block's own header row lives; browse spends that line on a section band.
// The width assertion is paired with one that the line under test actually
// holds the labels, because a painted band row is exactly m.width cells too,
// as EVERY painted row is -- the width check alone would pass over the wrong
// line.
func TestListHeaderRowNeverExceedsWidth(t *testing.T) {
	item := planItem{plan: domain.Plan{ID: "p1", Title: "Plan"}}

	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}

	// This loop measures the row builder directly, so it keeps its narrowest
	// width: 24 is below the minimum-size gate and no VIEW is drawn there any
	// more, but listHeaderRow is still asked at every width and its arithmetic
	// still has to hold. It is NOT a claim that a narrow panel lays out the
	// same way -- the layouts genuinely differ (NAME comes out 10 at 24, 15 at
	// 29 and 16 at 30).
	for _, width := range []int{160, 80, 40, 24} {
		m := NewList(nil, keymap.Default(), nil)
		m.width = width
		m.applyRefresh([]planItem{item})
		// The header is a row's CONTENT and stops at the margins, like every
		// other row's; m.inset is what takes it out to the terminal's edge.
		if w := ansi.StringWidth(m.listHeaderRow()); w != m.rowWidth() {
			t.Fatalf("width %d: header width = %d, want %d", width, w, m.rowWidth())
		}
	}

	for _, width := range []int{160, 80, 40, listMinWidth} {
		mp := NewList(nil, keymap.Default(), th)
		mp.width, mp.height = width, 24
		mp.applyRefresh([]planItem{item})
		mp.setMode(listRename)
		lines := strings.Split(mp.View().Content, "\n")
		headerLine := ansi.Strip(lines[3]) // 0: ground, 1: masthead, 2: blank, 3: header
		if !strings.Contains(headerLine, "NAME") {
			t.Fatalf("painted width %d: line 3 is %q, not the column-header row this test measures", width, headerLine)
		}
		if w := ansi.StringWidth(headerLine); w != width {
			t.Fatalf("painted width %d: header line width = %d, want exactly %d (%q)", width, w, width, headerLine)
		}
	}
}

// TestListViewHeightShrinksByMastheadInEveryMode pins viewHeight's
// unconditional masthead reservation against each mode's pre-masthead formula:
// two rows in browse and three in a panel mode (mastheadHeight), a panel mode
// drawing no section bands and keeping the single header row instead.
//
// CONFIRM-DELETE IS NOT IN THIS TABLE: it draws the centred box, which
// reserves nothing at all, so its pre-masthead formula is IDENTICAL to
// browse's and a row for it would measure nothing the browse row does not.
// TestCentredListPanelsReserveNothing pins "reserves nothing" directly.
func TestListViewHeightShrinksByMastheadInEveryMode(t *testing.T) {
	tests := []struct {
		name    string
		mode    listMode
		oldFunc func(m *ListModel) int
	}{
		{"browse", listBrowse, func(m *ListModel) int { return m.height - 2 }},
		{"rename", listRename, func(m *ListModel) int { return m.height - 2 - (m.ta.Height() + 2) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewList(nil, keymap.Default(), nil)
			m.width, m.height = 80, 24
			m.setMode(tt.mode)
			want := tt.oldFunc(m) - m.mastheadHeight()
			if want < 1 {
				want = 1
			}
			if got := m.viewHeight(); got != want {
				t.Fatalf("viewHeight() = %d, want %d (old formula minus this mode's masthead block)", got, want)
			}
		})
	}
}

// TestCentredListPanelsReserveNothing pins the plumbing across every mode in
// drawsCentredPanel's set: listConfirmDelete, listKeys and listInfo. A mode
// named there must be absent from every height reservation, draw no strip, and
// leave the masthead in its browse form.
//
// KEEPING ONLY "THE FRAME IS STILL m.height ROWS" WOULD LET A HALF-FINISHED
// MOVE PASS: a strip still drawn under a reservation that no longer exists
// overflows the SAME way a reservation left standing under a splice that
// needs none underflows, and the two can cancel back to the right total by
// coincidence at any one size. So every term is asserted on its own here:
//
//   - viewHeight() is unchanged from browse's, at the SAME width and height,
//     not merely some formula.
//   - panelViewPainted() returns nothing.
//   - the frame is exactly m.height rows of exactly m.width cells.
//   - THE MASTHEAD IS BROWSE'S, not a panel mode's -- the half only this
//     model has, since the review model has no masthead block to get wrong.
//     mastheadHeight() == listBrowseMastheadHeight and isPanelMode() ==
//     false, for every row.
//   - the box fits inside the minimum-size gate -- this model's own floor,
//     which the review model does not have. listMinWidth x listMinHeight is
//     asserted directly: a real screen is measured against the gate itself,
//     never against a literal that stands for it.
func TestCentredListPanelsReserveNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(m *ListModel)
	}{
		{
			name: "listConfirmDelete",
			setup: func(m *ListModel) {
				m.confirm = ordinaryDeleteConfirmText(domain.Plan{Title: "Rollout"})
				m.setMode(listConfirmDelete)
			},
		},
		{
			name: "listKeys",
			setup: func(m *ListModel) {
				m.confirm = listKeysText(m.km)
				m.setMode(listKeys)
			},
		},
		{
			name: "listInfo",
			setup: func(m *ListModel) {
				m.confirm = infoPanelText(planItem{plan: domain.Plan{ID: "l_rollout", Title: "Rollout", SourceHint: "/work/rollout.md"}})
				m.setMode(listInfo)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			browse := NewList(nil, keymap.Default(), nil)
			browse.width, browse.height = 80, 24
			browseVH := browse.viewHeight()

			m := NewList(nil, keymap.Default(), nil)
			m.width, m.height = 80, 24
			tc.setup(m)
			if !m.drawsCentredPanel() {
				t.Fatalf("test setup: mode %v does not answer drawsCentredPanel() -- this fixture is not exercising a centred mode", m.mode)
			}

			if got := m.viewHeight(); got != browseVH {
				t.Fatalf("viewHeight() = %d, want %d (browse's own -- a centred mode reserves nothing)", got, browseVH)
			}
			if got := m.panelViewPainted(); got != "" {
				t.Fatalf("panelViewPainted() = %q, want nothing -- the centred panel must not also draw the bottom strip", got)
			}
			if got := m.mastheadHeight(); got != listBrowseMastheadHeight {
				t.Fatalf("mastheadHeight() = %d, want %d (browse's -- a centred mode is not a panel mode)", got, listBrowseMastheadHeight)
			}
			if m.isPanelMode() {
				t.Fatal("isPanelMode() = true for a centred mode -- the masthead would draw a panel mode's column-header row over a body that is a splice, not a section")
			}
			screen := ansi.Strip(m.View().Content)
			rows := strings.Split(screen, "\n")
			if len(rows) != m.height {
				t.Fatalf("the frame is %d rows against a terminal of %d:\n%s", len(rows), m.height, screen)
			}
			for i, row := range rows {
				if w := ansi.StringWidth(row); w != m.width {
					t.Fatalf("frame row %d is %d cells against a terminal of %d: %q", i, w, m.width, row)
				}
			}

			// THE BOX FITS INSIDE THE MINIMUM-SIZE GATE, driven at exactly
			// listMinWidth x listMinHeight -- below fitsMinimum's floor View()
			// draws the refusal alone and there is no box to measure.
			g := NewList(nil, keymap.Default(), nil)
			g.width, g.height = listMinWidth, listMinHeight
			tc.setup(g)
			if !g.fitsMinimum() {
				t.Fatalf("test setup: %dx%d does not clear fitsMinimum() -- this fixture is not exercising the gate", g.width, g.height)
			}
			gScreen := ansi.Strip(g.View().Content)
			gRows := strings.Split(gScreen, "\n")
			if len(gRows) != g.height {
				t.Fatalf("at the minimum gate (%dx%d) the frame is %d rows, want %d:\n%s", g.width, g.height, len(gRows), g.height, gScreen)
			}
			for i, row := range gRows {
				if w := ansi.StringWidth(row); w != g.width {
					t.Fatalf("at the minimum gate, frame row %d is %d cells against %d: %q", i, w, g.width, row)
				}
			}
			x, y, w, h := boxGeometry(t, gScreen)
			if w > g.width || h > g.height {
				t.Fatalf("at the minimum gate (%dx%d) the box is %dx%d -- it was meant to be stripped to fit, not clipped:\n%s", g.width, g.height, w, h, gScreen)
			}
			if x < 0 || y < 0 || x+w > g.width || y+h > g.height {
				t.Fatalf("at the minimum gate the box runs from (%d,%d) to (%d,%d) on a %dx%d terminal:\n%s", x, y, x+w, y+h, g.width, g.height, gScreen)
			}
		})
	}
}

// TestListViewHeightAndLineCountIdenticalAcrossZeroToOneItems pins the
// invariant listMastheadHeight's unconditional reservation exists for: the
// 0->1-plan transition under live refresh must never jump the layout.
// viewHeight() doesn't reference m.items at all, so the guard is otherwise
// untested — this pins it end-to-end, checking both viewHeight() itself and
// the actual rendered line count come out identical whether the store is empty
// or holds exactly one plan, at the same size.
func TestListViewHeightAndLineCountIdenticalAcrossZeroToOneItems(t *testing.T) {
	item := planItem{plan: domain.Plan{ID: "p1", Title: "Plan"}}

	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}

	for _, tm := range []*theme.Theme{nil, th} {
		empty := NewList(nil, keymap.Default(), tm)
		empty.width, empty.height = 80, 24

		seeded := NewList(nil, keymap.Default(), tm)
		seeded.width, seeded.height = 80, 24
		seeded.applyRefresh([]planItem{item})

		if got, want := seeded.viewHeight(), empty.viewHeight(); got != want {
			t.Fatalf("painted=%v: viewHeight() with 1 item = %d, want %d (same as empty)", tm != nil, got, want)
		}

		emptyLines := len(strings.Split(empty.View().Content, "\n"))
		seededLines := len(strings.Split(seeded.View().Content, "\n"))
		if emptyLines != seededLines {
			t.Fatalf("painted=%v: total lines empty=%d, 1-item=%d, want equal", tm != nil, emptyLines, seededLines)
		}
	}
}

// listControlBytes is the three control bytes this test names, with the glyph
// each must reach the screen as WRITTEN OUT rather than computed: ui/control.go
// derives the Control Picture arithmetically (U+2400 + b, with U+2421 for DEL,
// the one that does not follow), and a test re-running that arithmetic would
// agree with the implementation by construction and assert nothing about it.
// \b and \r are the two forgeries that need no escape sequence at all; \x7f is
// the byte whose glyph is not base+offset.
var listControlBytes = []struct{ name, ctl, glyph string }{
	{"BS", "\b", "␈"},
	{"CR", "\r", "␍"},
	{"DEL", "\x7f", "␡"},
}

// listDrawnFields is every string the plan list draws that can carry any byte,
// each with a payload sized to its OWN column so the byte survives that column's
// own truncation and the column comes out full.
//
// domain.Plan.SourceHint has TWO sites, the status bar's origin and "Your plans"'
// own SOURCE column: both go through planItem.sourceLabel, but a bug in the row's
// OWN call -- a wrong width, a missed filter -- would not be caught by exercising
// the status bar's call alone. These are the channels with no accidental cover:
// nothing in this frame parses, projects or splits them at an inline leaf the way
// renderInlines splits a paragraph, so a filter proved on document prose says
// nothing at all about any of them.
//
// THE PAYLOADS ARE NOT INTERCHANGEABLE, which is the reason each row builds its
// own. Every column is clipped from the RIGHT, so a payload must put its proving
// byte near its START to survive, and a case that drops its own payload passes
// vacuously. Each is also long enough to FILL its budget, because the overflow
// this test exists for only fires on a row that was already exactly its width.
var listDrawnFields = []struct {
	name    string
	payload func(ctl string) string
	build   func(t *testing.T, th *theme.Theme, payload string) *ListModel
}{
	{
		name: "the row title",
		payload: func(ctl string) string {
			return "Requires approval" + ctl + "No approval needed" + strings.Repeat("x", 60)
		},
		build: func(t *testing.T, th *theme.Theme, payload string) *ListModel {
			return onePlanList(th, nil, domain.Plan{ID: "l_a", Title: payload, SourceHint: "~/plans/a.md"}, listBrowse)
		},
	},
	{
		name: "the status bar's origin",
		// THE BYTE LEADS, because sourceLabel clips from the RIGHT: a payload
		// carrying it in the filename tail (which this one did, while the
		// column kept tails) is dropped by the clip before it can be drawn,
		// and the case then passes while proving nothing.
		payload: func(ctl string) string { return "/ap" + ctl + "proved/" + strings.Repeat("x", 60) },
		build: func(t *testing.T, th *theme.Theme, payload string) *ListModel {
			return onePlanList(th, nil, domain.Plan{ID: "l_a", Title: "Alpha", SourceHint: payload}, listBrowse)
		},
	},
	{
		// The SAME renderer (planItem.sourceLabel) as "the status bar's
		// origin" above, exercised through its OTHER call site: the row's own
		// SOURCE column (renderRowPainted), on a narrower budget (the region,
		// 28 at this model's width) than the status bar's half-bar. The cursor
		// is moved off the row, so the status bar draws no copy of the path
		// for this case to find instead.
		name: "the source column",
		// Leading, for the same reason as the status bar's origin above --
		// and it matters more here, on the narrower 28-cell region.
		payload: func(ctl string) string { return "/ap" + ctl + "proved/" + strings.Repeat("x", 60) },
		build: func(t *testing.T, th *theme.Theme, payload string) *ListModel {
			m := onePlanList(th, nil, domain.Plan{ID: "l_a", Title: "Alpha", SourceHint: payload}, listBrowse)
			m.cursor = 0
			return m
		},
	},
	{
		name:    "the delete confirm's title",
		payload: func(ctl string) string { return "ap" + ctl + "proved" },
		build: func(t *testing.T, th *theme.Theme, payload string) *ListModel {
			plan := domain.Plan{ID: "l_a", Title: payload, SourceHint: "~/plans/a.md"}
			m := onePlanList(th, nil, plan, listBrowse)
			m.confirm = ordinaryDeleteConfirmText(plan)
			m.setMode(listConfirmDelete)
			return m
		},
	},
	{
		name:    "the status bar's message",
		payload: func(ctl string) string { return "error: ap" + ctl + "proved" },
		build: func(t *testing.T, th *theme.Theme, payload string) *ListModel {
			m := onePlanList(th, nil, domain.Plan{ID: "l_a", Title: "Alpha", SourceHint: "~/plans/a.md"}, listBrowse)
			m.status = payload
			return m
		},
	},
}

// onePlanList is one plan in a 100x30 list, painted, at the size this file's
// other frame tests use.
func onePlanList(th *theme.Theme, svc client.PlanService, plan domain.Plan, mode listMode) *ListModel {
	m := NewList(svc, keymap.Default(), th)
	m.width, m.height = 100, 30
	m.applyRefresh([]planItem{{plan: plan}})
	if mode != listBrowse {
		m.setMode(mode)
	}
	return m
}

// TestTheListFrameVisualisesAControlByte is deliberately NOT a document test.
//
// THE LIST IS A SECOND FRAME AND IT NEVER TOUCHES ui.Line. Nothing it draws
// goes through ParseBlocks, renderInlines or RenderDoc: a plan title, a source
// hint and a delete confirm's names arrive as whole strings and are truncated,
// padded and rendered straight, so a filter validated on paragraphs would ship
// green over exactly these channels. The payload goes in the FIELD.
//
// IT ASSERTS THE FRAME AS WELL AS THE TEXT, and the frame half is the one that
// was actually broken. ansi.StringWidth counts a bare C0 byte as ZERO cells
// and lipgloss's Width().Render counts it as ONE, so a row budgeted to exactly
// its width by the first authority is wrapped by the second: one C0 byte in a
// SourceHint drew 31 rows against a 30-row terminal. A Control Picture is one
// cell to both authorities, so the fix is the substitution rather than a second
// budget.
func TestTheListFrameVisualisesAControlByte(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range listControlBytes {
		for _, f := range listDrawnFields {
			t.Run(b.name+"/"+f.name, func(t *testing.T) {
				payload := f.payload(b.ctl)
				m := f.build(t, th, payload)
				content := m.View().Content
				// THE BYTE FIRST AND THE FRAME SECOND, so that a filter taken
				// back out of any one field fails by NAMING THE BYTE that got
				// through rather than by reporting a row count.
				plain := ansi.Strip(content)
				if strings.Contains(plain, b.ctl) {
					t.Fatalf("the byte %q reached the screen unfiltered through this field: %q -- this frame parses nothing, so nothing here covers it by accident", b.ctl, plain)
				}
				if !strings.Contains(plain, b.glyph) {
					t.Fatalf("the byte %q is gone from the screen and %q is not there either: %q -- control bytes are visualised and not stripped, because a strip shows a reader clean text while the plan holds something else", b.ctl, b.glyph, plain)
				}
				lines := strings.Split(content, "\n")
				if len(lines) != m.height {
					t.Fatalf("the frame is %d rows against a %d-row terminal -- one control byte in this field is zero cells to ansi.StringWidth and one to lipgloss's wrap, so a row budgeted to exactly its width came back as two:\n%s",
						len(lines), m.height, ansi.Strip(content))
				}
				for i, l := range lines {
					if w := ansi.StringWidth(ansi.Strip(l)); w != m.width {
						t.Fatalf("row %d is %d cells against a %d-cell terminal: %q", i, w, m.width, ansi.Strip(l))
					}
				}
			})
		}
	}
}

// TestAListControlByteIsNotItsOwnGlyph: a plan TITLED "the ␍ problem" and a
// plan whose title carries a real carriage return
// must read the same with the styling stripped and differ in the bytes, with
// reverse video on the second only. Without that, a reader cannot tell a plan
// about a control byte from a plan carrying one -- and the title is the one
// string in this frame every row draws.
func TestAListControlByteIsNotItsOwnGlyph(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	draw := func(title string) string {
		return onePlanList(th, nil, domain.Plan{ID: "l_a", Title: title, SourceHint: "~/plans/a.md"}, listBrowse).View().Content
	}
	real, written := draw("before\rafter"), draw("before␍after")
	if ansi.Strip(real) != ansi.Strip(written) {
		t.Fatalf("the two lists already read differently with the styling stripped off:\n  a real CR: %q\n  the rune ␍: %q\nThere is no collision here for the styling to resolve", ansi.Strip(real), ansi.Strip(written))
	}
	if listHasReverseVideo(written) {
		t.Fatalf("the literal rune ␍ was drawn in reverse video -- then the styling says nothing, because it says the same thing about both")
	}
	if !listHasReverseVideo(real) {
		t.Fatalf("a real carriage return drew no reverse-video run -- an unstyled substitution makes a plan ABOUT a control byte indistinguishable from one carrying it")
	}
}

// listHasReverseVideo answers whether s carries SGR 7 anywhere, by reading the
// PARAMETERS of each CSI ... m rather than looking for a substring: `\x1b[7`
// as a substring is a different question, since a 24-bit colour's own digits
// are full of sevens and lipgloss orders the parameters differently under a
// bold style. ui carries the twin for the document frame; this package cannot
// import it.
func listHasReverseVideo(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] != 0x1b || s[i+1] != '[' {
			continue
		}
		j := i + 2
		for j < len(s) && (s[j] == ';' || (s[j] >= '0' && s[j] <= '9')) {
			j++
		}
		if j >= len(s) || s[j] != 'm' {
			continue
		}
		for _, p := range strings.Split(s[i+2:j], ";") {
			if p == "7" {
				return true
			}
		}
	}
	return false
}

// TestListTotalLinesNeverExceedHeight is the altscreen overflow check: the
// total rendered line count must never exceed m.height, empty or seeded, at a
// normal size and a cramped one.
func TestListTotalLinesNeverExceedHeight(t *testing.T) {
	sizes := []struct{ w, h int }{{80, 24}, {40, 12}}
	item := planItem{plan: domain.Plan{ID: "p1", Title: "Plan"}}

	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}

	for _, sz := range sizes {
		for _, seeded := range []bool{false, true} {
			for _, tm := range []*theme.Theme{nil, th} {
				m := NewList(nil, keymap.Default(), tm)
				m.width, m.height = sz.w, sz.h
				if seeded {
					m.applyRefresh([]planItem{item})
				}
				lines := strings.Split(m.View().Content, "\n")
				if len(lines) > sz.h {
					t.Fatalf("size %dx%d seeded=%v painted=%v: total lines = %d, want <= %d",
						sz.w, sz.h, seeded, tm != nil, len(lines), sz.h)
				}
			}
		}
	}
}

// TestListTopBottomKeepCursorVisible pins g/G's contract — every jump lands
// the cursor on a row that is actually on screen — and, with it, the
// structural fact that makes that easy in the collapsed state: browse never
// builds more rows than viewHeight, so m.scroll is pinned at 0 (clampScroll's
// len(rows)-viewHeight is never positive) and nothing can leave the cursor off
// screen in the first place. Scrolling a collapsed browse is unreachable BY
// CONSTRUCTION: 30 plans in a 15-row terminal are 5 plan rows and a "+25 more".
func TestListTopBottomKeepCursorVisible(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	const n = 30
	for i := 0; i < n; i++ {
		title := fmt.Sprintf("Plan %02d", i)
		seedListPlan(t, f, dir, fmt.Sprintf("plan-%02d.md", i), title, fmt.Sprintf("# %s\n\nBody text for %s.\n", title, title))
	}

	m := NewList(f.svc, keymap.Default(), nil)
	m.width, m.height = 100, 15 // a viewport far shorter than n plans
	m = drainList(t, m, m.Init())
	if len(m.items) != n {
		t.Fatalf("items = %d, want %d", len(m.items), n)
	}
	if len(m.rows) > m.viewHeight() {
		t.Fatalf("rows = %d, viewHeight = %d -- the collapsed allocation must never overflow the body", len(m.rows), m.viewHeight())
	}

	for _, keys := range [][]string{{"j", "j", "j", "j", "j"}, {"g"}, {"G"}, {"k", "k", "k"}} {
		m = pressList(m, keys...)
		if m.scroll != 0 {
			t.Fatalf("scroll after %v = %d, want 0 -- the collapsed state has nothing to scroll", keys, m.scroll)
		}
		if m.cursor < 0 || m.cursor >= m.viewHeight() {
			t.Fatalf("cursor after %v = %d, not on screen (viewHeight %d)", keys, m.cursor, m.viewHeight())
		}
		if !m.rows[m.cursor].kind.selectable() {
			t.Fatalf("cursor after %v rests on chrome (row %d, %q)", keys, m.cursor, m.rows[m.cursor].text)
		}
	}

	// g lands on the first plan of "Your plans" (past its two band rows), and
	// G on the section's last selectable row -- here the "+N more" tail.
	m = pressList(m, "g")
	if it, ok := m.selectedPlan(); !ok || it.plan.ID != m.items[0].plan.ID {
		t.Fatalf("g selects (%q, %v), want the first plan (%q)", it.plan.ID, ok, m.items[0].plan.ID)
	}
	m = pressList(m, "G")
	if got := m.rows[m.cursor].kind; got != rowMore {
		t.Fatalf("G lands on row kind %v, want the +N more row -- 30 plans cannot fit a 15-row terminal", got)
	}
}

// TestBrowseRowsNeverExceedTheViewport is the structural bound the whole wheel
// semantics rest on: browse mode lays out over a fixed budget (rowBudget() =
// viewHeight() - listSectionChrome, which sectionRows never overfills), so
// len(m.rows) <= m.viewHeight() holds in browse BY CONSTRUCTION, not because any
// one fixture happens to fit. If this property ever stops holding, the wheel is
// wrong -- it would need to move m.scroll, which nothing in browse mode does
// today. It is also why pgdn/pgup wire into listExpanded ONLY and never
// listBrowse: a page gesture there would be a branch this property makes
// permanently unreachable, and *a branch no assertion can kill is worse than no
// branch*.
//
// THE GRID IS OVER BOTH THE VIEWPORT AND THE POPULATION, independently,
// because a fixture that varied only one would only prove the bound at the
// other's fixed value. Every height from listMinHeight (fitsMinimum's own
// floor; nothing below it ever reaches this code) through listMinHeight+24
// is crossed with population shapes that are empty, small and overflowing,
// with and without Recently opened drawn above "Your plans".
//
// NON-VACUOUS: sawOverflowDemand below is set only when a cell's raw content
// exceeds that height's own rowBudget(), i.e. a cell the layout COULD NOT have
// honoured without truncating something. The test fails if no cell in the
// grid ever makes that demand, since then len(rows) <= viewHeight() would hold
// whether or not anything capped the body.
func TestBrowseRowsNeverExceedTheViewport(t *testing.T) {
	shapes := []struct {
		name          string
		plans, recent int
	}{
		{"empty", 0, 0},
		{"one plan", 1, 0},
		{"overflow", 60, 0},
		{"one plan, recently opened", 1, 1},
		{"overflow, recently opened", 60, 5},
	}

	sawOverflowDemand := false
	for h := listMinHeight; h <= listMinHeight+24; h++ {
		for _, shape := range shapes {
			t.Run(fmt.Sprintf("h=%d/%s", h, shape.name), func(t *testing.T) {
				m := NewList(nil, keymap.Default(), nil)
				m.width, m.height = 80, h
				items := listSectionItems("Plan", shape.plans)
				if shape.recent > 0 {
					m.recentPath = "recent.json"
					for _, it := range items[:shape.recent] {
						m.recents = append(m.recents, recentEntry{id: it.plan.ID})
					}
				}
				m.applyRefresh(items)

				if got, want := m.mode, listBrowse; got != want {
					t.Fatalf("mode = %v, want %v -- this bound is browse's, not any other mode's", got, want)
				}
				if shape.plans+shape.recent > m.rowBudget() {
					sawOverflowDemand = true
				}
				if len(m.rows) > m.viewHeight() {
					t.Fatalf("rows = %d, viewHeight = %d -- browse drew more than its own budget",
						len(m.rows), m.viewHeight())
				}
			})
		}
	}

	if !sawOverflowDemand {
		t.Fatal("fixture assumption broken: no (height, shape) cell in this grid demands more rows than " +
			"rowBudget() can seat, so len(rows) <= viewHeight() would hold even with sectionBody's " +
			"own cap deleted -- the grid needs a shape or height that actually presses on the layout")
	}
}

// listChromeRows is a row shape built by hand: two bands per section (its own
// header band and its column-label row), plan rows, a +N more row, and -- for a
// section with nothing in it -- an empty-section hint. By hand rather than taken
// from buildRows: these tests are about the CURSOR's rules over an arbitrary row
// shape, and a fixture derived from the layout would only ever exercise the
// shapes it happens to emit today (it never, for instance, puts a +N more row
// directly above a section with nothing in it).
func listChromeRows() []row {
	return []row{
		{kind: rowBand, text: "Your plans"},
		{kind: rowBand, text: "NAME"},
		{kind: rowPlan, item: planItem{plan: domain.Plan{ID: "p1", Title: "Plan One"}}},
		{kind: rowPlan, item: planItem{plan: domain.Plan{ID: "p2", Title: "Plan Two"}}},
		{kind: rowMore, text: "+3 more"},
		{kind: rowBand, text: "Section two"},
		{kind: rowBand, text: "NAME"},
		{kind: rowHint, text: "Nothing here yet!"},
	}
}

// listBothSectionsRows is listChromeRows' other half: a plan in each
// section, so a downward walk out of the top section has somewhere to land
// and must cross two bands to get there.
func listBothSectionsRows() []row {
	return []row{
		{kind: rowBand, text: "Recently opened"},
		{kind: rowBand, text: "NAME"},
		{kind: rowPlan, item: planItem{plan: domain.Plan{ID: "p1", Title: "Plan One"}}},
		{kind: rowBand, text: "Your plans"},
		{kind: rowBand, text: "NAME"},
		{kind: rowPlan, item: planItem{plan: domain.Plan{ID: "p2", Title: "Plan Two"}}},
	}
}

// seedRows installs rows on m together with the plan set they were built from,
// keeping items and rows in step the way applyRefresh does in production.
func seedRows(t *testing.T, m *ListModel, rows []row, cursor int) *ListModel {
	t.Helper()
	m.rows = rows
	m.items = nil
	for _, r := range rows {
		if r.kind == rowPlan {
			m.items = append(m.items, r.item)
		}
	}
	m.cursor = cursor
	return m
}

// TestListCursorWalksRowsAndNeverRestsOnChrome pins the cursor discipline: the
// cursor indexes ROWS, and j/k/g/G may only ever leave it on a row a gesture
// can act on — a plan, or the +N more row whose enter gives it meaning (a row
// the cursor cannot reach cannot be pressed). Bands and the empty-section hint
// are chrome: walked over, jumped over, never landed on. Moving past a
// section's allocation does NOTHING — it neither scrolls nor expands.
func TestListCursorWalksRowsAndNeverRestsOnChrome(t *testing.T) {
	cases := []struct {
		name   string
		rows   []row
		start  int
		keys   []string
		want   int
		wantID domain.PlanID // "" when the cursor should not be on a plan
	}{
		{name: "j from a plan lands on the next plan", start: 2, keys: []string{"j"}, want: 3, wantID: "p2"},
		{name: "j from the last plan lands on +N more, which is selectable", start: 3, keys: []string{"j"}, want: 4},
		{name: "j past the last selectable row does nothing", start: 4, keys: []string{"j", "j"}, want: 4},
		{name: "k from the first plan stops there — rows 0 and 1 are bands", start: 2, keys: []string{"k", "k"}, want: 2, wantID: "p1"},
		{name: "k from +N more walks back onto a plan", start: 4, keys: []string{"k"}, want: 3, wantID: "p2"},
		{name: "g jumps to the first selectable row, not row 0", start: 3, keys: []string{"g"}, want: 2, wantID: "p1"},
		{name: "G jumps to the last selectable row, not the trailing hint", start: 2, keys: []string{"G"}, want: 4},
		{
			name: "j out of the top section crosses both of the next section's bands",
			rows: listBothSectionsRows(), start: 2, keys: []string{"j"}, want: 5, wantID: "p2",
		},
		{
			name: "k back into the top section crosses them again",
			rows: listBothSectionsRows(), start: 5, keys: []string{"k"}, want: 2, wantID: "p1",
		},
		{
			name: "G with a plan in each section lands on the lower one",
			rows: listBothSectionsRows(), start: 2, keys: []string{"G"}, want: 5, wantID: "p2",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := tc.rows
			if rows == nil {
				rows = listChromeRows()
			}
			m := seedRows(t, NewList(nil, keymap.Default(), nil), rows, tc.start)
			m = pressList(m, tc.keys...)

			if m.cursor != tc.want {
				t.Fatalf("cursor after %v = %d, want %d", tc.keys, m.cursor, tc.want)
			}
			if !m.rows[m.cursor].kind.selectable() {
				t.Fatalf("cursor rests on row %d, which is chrome (%q)", m.cursor, m.rows[m.cursor].text)
			}
			it, ok := m.selectedPlan()
			if tc.wantID == "" {
				if ok {
					t.Fatalf("selectedPlan() = %q, want no plan under the cursor at row %d", it.plan.ID, m.cursor)
				}
				return
			}
			if !ok || it.plan.ID != tc.wantID {
				t.Fatalf("selectedPlan() = (%q, %v), want (%q, true)", it.plan.ID, ok, tc.wantID)
			}
		})
	}
}

// TestListGesturesCannotReachAPlanFromAChromeRow is selectedPlan's whole
// reason to exist: it is the ONE way any gesture reaches a plan, so with the
// cursor on a band, a +N more row or an empty-section hint, enter/e/d must
// find nothing and do nothing.
//
// This is the mutation check for the row model. Making selectedPlan hand
// back the row's item regardless of kind turns each of these into a gesture
// aimed at the zero plan: d opens a confirm panel authorising the deletion
// of `""`, e opens a rename panel over an empty title, and enter dispatches
// msgOpenPlan for a plan that does not exist.
//
// ENTER ON A +N MORE IS THE ONE THAT DOES SOMETHING: it expands that section.
// What this test pins for that row is that no PLAN is reached, so no
// msgOpenPlan is dispatched. wantExpand carries the difference, and the e/d
// checks run from a fresh browse-mode model for that row so they measure the
// gesture rather than the mode the enter above just entered.
func TestListGesturesCannotReachAPlanFromAChromeRow(t *testing.T) {
	rows := listChromeRows()
	for _, tc := range []struct {
		name       string
		cursor     int
		wantExpand bool
	}{
		{name: "section header band", cursor: 0},
		{name: "column-label band", cursor: 1},
		{name: "+N more", cursor: 4, wantExpand: true},
		{name: "empty-section hint", cursor: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rows[tc.cursor].kind; got == rowPlan {
				t.Fatalf("fixture assumption broken: row %d is a plan row", tc.cursor)
			}

			m := seedRows(t, NewList(nil, keymap.Default(), nil), rows, tc.cursor)
			if it, ok := m.selectedPlan(); ok {
				t.Fatalf("selectedPlan() = (%q, true) on a %s, want no plan", it.plan.ID, tc.name)
			}

			cur, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = cur.(*ListModel)
			if cmd != nil {
				t.Fatalf("enter on a %s dispatched %T, want no cmd — there is no plan to open", tc.name, cmd())
			}
			if got := m.mode == listExpanded; got != tc.wantExpand {
				t.Fatalf("enter on a %s left mode = %v, want expanded = %v", tc.name, m.mode, tc.wantExpand)
			}
			if tc.wantExpand {
				m = seedRows(t, NewList(nil, keymap.Default(), nil), rows, tc.cursor)
			}

			m = pressList(m, "e")
			if m.mode != listBrowse || m.renameTarget != "" {
				t.Fatalf("e on a %s: mode = %v, renameTarget = %q, want browse and no target", tc.name, m.mode, m.renameTarget)
			}

			m = pressList(m, "d")
			if m.mode != listBrowse || m.deleteTarget != "" || m.confirm != "" {
				t.Fatalf("d on a %s: mode = %v, deleteTarget = %q, confirm = %q, want browse, no target, no panel",
					tc.name, m.mode, m.deleteTarget, m.confirm)
			}
		})
	}
}

// TestListRefreshFromAChromeRowKeepsPositionNotIdentity pins rebuildRows'
// answer for a cursor with no plan id to re-find across the rebuild: a cursor
// on a band or a +N more row keeps its POSITION — clamped into the new rows
// and snapped onto the nearest selectable one — and never inherits some
// unrelated row's plan. TestListStateChangedPreservesCursorIdentity covers the
// other half, where there IS an id and identity wins over position.
func TestListRefreshFromAChromeRowKeepsPositionNotIdentity(t *testing.T) {
	// The rebuilt rows are "Your plans"' band pair (rows 0-1) and then the three
	// plans (2-4). Distinct, descending activity so the three land in the order
	// written -- left at the zero value they would order by TITLE, which is not
	// what this test is about.
	fresh := []planItem{
		{plan: domain.Plan{ID: "n1", Title: "New One"}, lastActivity: listFixtureEpoch},
		{plan: domain.Plan{ID: "n2", Title: "New Two"}, lastActivity: listFixtureEpoch.Add(-time.Minute)},
		{plan: domain.Plan{ID: "n3", Title: "New Three"}, lastActivity: listFixtureEpoch.Add(-2 * time.Minute)},
	}

	for _, tc := range []struct {
		name   string
		cursor int
		want   domain.PlanID
	}{
		// Row 1 is a column-label row before AND after: still chrome, so the
		// cursor keeps its POSITION and snaps down to the first row it may
		// rest on.
		{"a column-label band still inside the rebuilt list", 1, "n1"},
		// Row 4 holds a plan after the rebuild — position kept, exactly as if
		// it had never been chrome.
		{"a chrome row that becomes a plan row", 4, "n3"},
		// Row 6 is past the end of the rebuilt rows: clamped into them, onto
		// the last row, which is a plan.
		{"a band past the end of the rebuilt list", 6, "n3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := seedRows(t, NewList(nil, keymap.Default(), nil), listChromeRows(), tc.cursor)
			m.applyRefresh(fresh)

			it, ok := m.selectedPlan()
			if !ok {
				t.Fatalf("cursor at row %d after the rebuild is on no plan at all, want a plan row", m.cursor)
			}
			if it.plan.ID != tc.want {
				t.Fatalf("cursor lands on %q, want %q", it.plan.ID, tc.want)
			}
		})
	}
}

// TestListCursorFallsBackToASelectableRow pins the other half of the cursor
// discipline: where the cursor goes when the row it is on is not one it may
// rest on. snapCursor's rule is into range first, then the nearest selectable
// row BELOW, and only failing that the nearest above.
//
// Driven directly rather than through a refresh, which is what lets the table
// cover the cases a real rebuild reaches only one at a time -- a cursor past
// the end, a negative one, and a row slice with nothing selectable anywhere.
func TestListCursorFallsBackToASelectableRow(t *testing.T) {
	chromeOnly := []row{
		{kind: rowBand, text: "Your plans"},
		{kind: rowBand, text: "NAME"},
		{kind: rowHint, text: "no plans yet"},
	}

	for _, tc := range []struct {
		name   string
		rows   []row
		cursor int
		want   int
	}{
		{"a leading band snaps down to the first plan", listChromeRows(), 0, 2},
		{"a trailing hint has nothing below and snaps up", listChromeRows(), 7, 4},
		{"a band between the sections snaps down, not up", listBothSectionsRows(), 4, 5},
		{"a cursor past the end is clamped first, then snapped", listChromeRows(), 99, 4},
		{"a negative cursor is clamped first, then snapped", listChromeRows(), -3, 2},
		{"nothing selectable anywhere parks the cursor at 0", chromeOnly, 2, 0},
		{"an empty list parks the cursor at 0", nil, 5, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := seedRows(t, NewList(nil, keymap.Default(), nil), tc.rows, tc.cursor)
			m.snapCursor()
			if m.cursor != tc.want {
				t.Fatalf("snapCursor left the cursor at %d, want %d", m.cursor, tc.want)
			}
		})
	}

	// g and G set the cursor directly rather than through moveCursor, so they
	// must agree with that fallback rather than land on row 0 or the last row:
	// with every row chrome there is nowhere to rest and nothing to reach.
	m := seedRows(t, NewList(nil, keymap.Default(), nil), chromeOnly, 0)
	m = pressList(m, "j", "k", "g", "G")
	if m.cursor != 0 {
		t.Fatalf("cursor after j/k/g/G over chrome-only rows = %d, want 0", m.cursor)
	}
	if it, ok := m.selectedPlan(); ok {
		t.Fatalf("selectedPlan() = (%q, true) with no plan row anywhere", it.plan.ID)
	}

	// j and k snap before they walk, so a cursor left past the end of a
	// shorter row slice recovers on the next keypress instead of doing
	// nothing until g or G rescues it.
	m = seedRows(t, NewList(nil, keymap.Default(), nil), listBothSectionsRows(), 99)
	m = pressList(m, "j")
	if it, ok := m.selectedPlan(); !ok || it.plan.ID != "p2" {
		t.Fatalf("j from a cursor past the end selects (%q, %v), want the last plan (%q, true)", it.plan.ID, ok, "p2")
	}
}

// TestListViewDrawsRowsInRowOrder pins the render loop itself —
// viewPainted's — rather than the row renderers it feeds, which
// TestListChromeRowRendersItsTextWithinWidth already drives directly. A body
// drawn from the plan set rather than from m.rows would silently skip every
// band, hint and +N more the allocator emits, and would put the cursor band on
// the wrong line, while every other test in this file stayed green.
func TestListViewDrawsRowsInRowOrder(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	// One identifying fragment per row of listChromeRows, in row order.
	want := []string{
		"Your plans", "NAME", "Plan One", "Plan Two", "+3 more",
		"Section two", "NAME", "Nothing here yet!",
	}

	for _, tm := range []*theme.Theme{nil, th} {
		m := NewList(nil, keymap.Default(), tm)
		m.width, m.height = 80, 24
		m = seedRows(t, m, listChromeRows(), 2)

		// The first listBrowseMastheadHeight lines are browse mode's masthead
		// block (ground, the box, blank -- the column labels belong to each
		// section's band pair); the body follows.
		lines := strings.Split(ansi.Strip(m.View().Content), "\n")
		if len(lines) < listBrowseMastheadHeight+len(want) {
			t.Fatalf("painted=%v: view has %d lines, too few to hold %d body rows", tm != nil, len(lines), len(want))
		}
		body := lines[listBrowseMastheadHeight:]
		for i, w := range want {
			if !strings.Contains(body[i], w) {
				t.Fatalf("painted=%v: body line %d = %q, want it to hold %q — the loop must draw m.rows, in order",
					tm != nil, i, body[i], w)
			}
		}
	}

	// The cursor band lands on the row the cursor indexes, not on the plan of
	// the same index. Read through ansi.Strip, which is all this needs: the
	// band is paintedBand's ▌ tinted onto the row's own background, and
	// stripping the SGR codes leaves the glyph itself.
	m := NewList(nil, keymap.Default(), nil)
	m.width, m.height = 80, 24
	m = seedRows(t, m, listChromeRows(), 3) // Plan Two, the fourth row
	body := strings.Split(ansi.Strip(m.View().Content), "\n")[listBrowseMastheadHeight:]
	for i := range want {
		holdsBand := strings.Contains(body[i], "▌")
		if holdsBand != (i == 3) {
			t.Fatalf("body line %d = %q: cursor band present = %v, want %v (the cursor is on row 3)",
				i, body[i], holdsBand, i == 3)
		}
	}

	// Scrolled, the loop draws from m.scroll and the band lands on the
	// cursor's own row — cursor minus scroll body lines down, not on the
	// cursor's absolute index. Both are the same line at scroll 0, which is
	// why this needs a row slice taller than the viewport.
	scrolled := []row{{kind: rowBand, text: "Your plans"}, {kind: rowBand, text: "NAME"}}
	for i := 0; i < 28; i++ {
		title := fmt.Sprintf("Plan %02d", i)
		scrolled = append(scrolled, row{kind: rowPlan, item: planItem{plan: domain.Plan{ID: domain.PlanID(title), Title: title}}})
	}
	m = seedRows(t, NewList(nil, keymap.Default(), nil), scrolled, 9)
	m.width, m.height = 80, 24
	m.scroll = 6
	body = strings.Split(ansi.Strip(m.View().Content), "\n")[listBrowseMastheadHeight:]
	if !strings.Contains(body[0], "Plan 04") { // rows[6] is the fifth plan
		t.Fatalf("first body line = %q, want the row at m.scroll (6): Plan 04", body[0])
	}
	for i := 0; i < m.viewHeight() && i < len(body); i++ {
		if holdsBand := strings.Contains(body[i], "▌"); holdsBand != (i == 3) {
			t.Fatalf("scrolled body line %d = %q: cursor band present = %v, want %v (cursor 9 - scroll 6)",
				i, body[i], holdsBand, i == 3)
		}
	}
}

// TestListChromeRowRendersItsTextWithinWidth: a non-plan row's text reaches
// the screen through the same clip the plan renderer uses — painted edge to
// edge, exactly as a plan row is.
func TestListChromeRowRendersItsTextWithinWidth(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	band := row{kind: rowBand, text: strings.Repeat("Your plans / a very long section band ", 6)}

	for _, width := range []int{160, 80, 40, 24} {
		m := NewList(nil, keymap.Default(), nil)
		m.width = width
		// EXACTLY the width, not at most the row cap: the render paints the
		// whole line, margins included.
		if w := ansi.StringWidth(ansi.Strip(m.renderBodyRowPainted(band, false))); w != width {
			t.Fatalf("width %d: band row = %d cells, want exactly %d", width, w, width)
		}

		mp := NewList(nil, keymap.Default(), th)
		mp.width = width
		rendered := mp.renderBodyRowPainted(band, false)
		if lines := strings.Split(rendered, "\n"); len(lines) != 1 {
			t.Fatalf("width %d: painted band row wrapped onto %d lines, want exactly 1", width, len(lines))
		}
		if w := ansi.StringWidth(ansi.Strip(rendered)); w != width {
			t.Fatalf("width %d: painted band row = %d cells, want the full terminal width", width, w)
		}
	}

	// The words themselves survive at an ordinary width.
	m := NewList(nil, keymap.Default(), nil)
	m.width = 80
	if got := ansi.Strip(m.renderBodyRowPainted(row{kind: rowBand, text: "Your plans"}, false)); !strings.Contains(got, "Your plans") {
		t.Fatalf("band row = %q, want it to carry its own text", got)
	}
}

// TestListRowsNeverExceedWidth pins the row-layout contract across widths 160,
// 80, 40, and 24: every row fills the terminal exactly, the plan-state glyph
// occupies the same column whether the row is approved (●) or unapproved with
// open threads (◐) — the positional-icon rule — and the title column gives up
// width before the last column does, which in turn gives up width before the
// fixed counts column ever would.
//
// THE LAST COLUMN IS FED ITS WORST CASE: a path far longer than the region at
// every width, so a row that stays inside its width only for short paths fails
// here.
func TestListRowsNeverExceedWidth(t *testing.T) {
	longTitle := strings.Repeat("Extremely Long Plan Title For Width Testing ", 3)
	longPath := "/Users/example/very/deeply/nested/project/tree/docs/plans/sub/sub/sub/width-testing.md"

	approvedItem := planItem{
		plan:        domain.Plan{ID: "p1", Title: longTitle, SourceHint: longPath},
		open:        3,
		total:       6,
		approvedTip: true,
	}
	unapprovedItem := planItem{
		plan:        domain.Plan{ID: "p2", Title: longTitle, SourceHint: longPath},
		open:        2,
		total:       6,
		approvedTip: false,
	}

	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}

	for _, width := range []int{160, 80, 40, 24} {
		m := NewList(nil, keymap.Default(), nil)
		m.width = width

		rowA := ansi.Strip(m.renderRowPainted(approvedItem, false))
		rowB := ansi.Strip(m.renderRowPainted(unapprovedItem, false))

		// A named palette at the same widths: one line, painted edge to edge
		// out to the terminal's own width whatever the row content is.
		mpw := NewList(nil, keymap.Default(), th)
		mpw.width = width
		for _, it := range []planItem{approvedItem, unapprovedItem} {
			painted := mpw.renderRowPainted(it, false)
			if lines := strings.Split(painted, "\n"); len(lines) != 1 {
				t.Fatalf("painted width %d: row wrapped onto %d lines", width, len(lines))
			}
			if w := ansi.StringWidth(ansi.Strip(painted)); w != width {
				t.Fatalf("painted width %d: row display width = %d, want exactly %d (%q)", width, w, width, ansi.Strip(painted))
			}
		}

		// Painted rows are exactly the terminal's width; rowWidth is what
		// their CONTENT is capped at, inside the margins.
		if w := ansi.StringWidth(rowA); w != width {
			t.Fatalf("width %d: approved row display width = %d, want %d (%q)", width, w, width, rowA)
		}
		if w := ansi.StringWidth(rowB); w != width {
			t.Fatalf("width %d: unapproved row display width = %d, want %d (%q)", width, w, width, rowB)
		}

		idxA := strings.IndexRune(rowA, '●')
		idxB := strings.IndexRune(rowB, '◐')
		if idxA < 0 || idxB < 0 {
			t.Fatalf("width %d: glyph missing: rowA=%q rowB=%q", width, rowA, rowB)
		}
		if idxA != idxB {
			t.Fatalf("width %d: glyph column mismatch: approved at %d, unapproved at %d", width, idxA, idxB)
		}

		if !strings.Contains(rowA, "3/6") {
			t.Fatalf("width %d: counts \"3/6\" missing from %q", width, rowA)
		}
	}

	// At width 80 the trailing region is at its full base, and a path longer
	// than that is clipped from the RIGHT -- its head says where the file lives
	// (planItem.sourceLabel).
	m80 := NewList(nil, keymap.Default(), nil)
	m80.width = 80
	// m80.rowWidth(), not 80: a row's content is narrower than the terminal by
	// the two margins ui.RailWidth adds at width >= 80.
	_, region80 := listColumnWidths(m80.rowWidth())
	if region80 != listRegionWidth {
		t.Fatalf("width 80: trailing region = %d, want its full %d", region80, listRegionWidth)
	}
	row80 := ansi.Strip(m80.renderRowPainted(approvedItem, false))
	if want := ansi.Truncate(homeRelative(longPath), listRegionWidth, "…"); !strings.Contains(row80, want) {
		t.Fatalf("width 80: row %q does not carry the head-kept path %q", row80, want)
	}

	// At width 40 the trailing region must have given up cells relative to its
	// width-80 budget — title-first, region-second, counts never.
	m40 := NewList(nil, keymap.Default(), nil)
	m40.width = 40
	_, region40 := listColumnWidths(m40.rowWidth())
	if region40 >= region80 {
		t.Fatalf("trailing region did not shrink from width 80 to 40: 80=%d 40=%d", region80, region40)
	}

	// Width 160: the row is the whole canvas. listMaxRowWidth (120) no longer
	// caps the content here -- see ListModel.rowWidth for why the cap froze
	// the wrong thing. What bounds the row now is NAME's own ceiling, so the
	// surplus lands in the REGION rather than in a widening gap inside NAME.
	m160 := NewList(nil, keymap.Default(), nil)
	m160.width = 160
	row160 := ansi.Strip(m160.renderRowPainted(approvedItem, false))
	if w := ansi.StringWidth(row160); w != 160 {
		t.Fatalf("width 160: row width = %d, want the full canvas 160", w)
	}
	title160, region160 := listColumnWidths(m160.rowWidth())
	if title160 != listMaxTitleWidth {
		t.Fatalf("width 160: NAME = %d, want its ceiling %d", title160, listMaxTitleWidth)
	}
	if region160 <= listRegionWidth {
		t.Fatalf("width 160: region = %d, want more than its base %d -- the surplus is the region's now", region160, listRegionWidth)
	}

	// Painted path: the full canvas out to the terminal's actual width is
	// painted background — never a bare unstyled gap.
	mp := NewList(nil, keymap.Default(), th)
	mp.width = 160
	rendered := mp.renderRowPainted(approvedItem, false)
	strippedPainted := ansi.Strip(rendered)
	if w := ansi.StringWidth(strippedPainted); w != 160 {
		t.Fatalf("painted row width = %d, want the full terminal width 160", w)
	}
	if !strings.Contains(rendered, "\x1b[48;") {
		t.Fatal("painted row must carry background escape sequences")
	}
}

// TestListRowsClipOrBlankCounts pins the counts column's own backstop at both
// its edges: a triple-digit open/total count (many threads piled onto one plan,
// plausible with agents posting over MCP) must clip in place rather than
// overflow, and a plan with no comments at all must render an EMPTY counts cell
// (formatCounts, "nothing to count") rather than "0/0". Either way, the gap and
// the last column must land at exactly the same offsets as a normal row, not
// just the same overall row width: an overall-width check alone cannot tell
// "100/10" truncated in place apart from "100/100" overflowing by one and
// shifting the tail left, and it cannot tell a blank cell padded to
// listCountsWidth apart from a blank cell that ate into the gap after it.
func TestListRowsClipOrBlankCounts(t *testing.T) {
	const source = "/plan.md"
	for _, tc := range []struct {
		name        string
		open, total int
		wantCounts  string
	}{
		// open:0 so every thread is resolved: the column now renders
		// resolved/total, and it is the resolved count that must overflow the
		// column to exercise the clip, not the open count.
		{"an oversized count clips in place", 0, 100, "100/10"},
		{"a plan with no comments renders blank, not 0/0", 0, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := planItem{
				plan:  domain.Plan{ID: "l_plan", Title: "Plan", SourceHint: source},
				open:  tc.open,
				total: tc.total,
			}

			m := NewList(nil, keymap.Default(), nil)
			m.width = 80
			planLine := []rune(ansi.Strip(m.renderRowPainted(item, false)))
			if w := len(planLine); w != 80 {
				t.Fatalf("system row: width = %d, want 80 (%q)", w, string(planLine))
			}

			titleWidth, _ := listColumnWidths(m.rowWidth())
			// A rendered row opens with its left margin, so every column sits
			// that much further in than the content-relative arithmetic below
			// gives.
			countsStart := m.listMargin() + listBandWidth + listGlyphWidth + titleWidth + listGapWidth
			countsEnd := countsStart + listCountsWidth
			if got := strings.TrimSpace(string(planLine[countsStart:countsEnd])); got != tc.wantCounts {
				t.Fatalf("counts column = %q, want %q", got, tc.wantCounts)
			}
			gapEnd := countsEnd + listLastGapWidth
			if got := string(planLine[countsEnd:gapEnd]); strings.TrimSpace(got) != "" {
				t.Fatalf("gap after counts = %q, want untouched spaces (the counts cell must not shift it)", got)
			}
			// The offset is what this test is about -- neither an oversized
			// count nor a blank one may shift SOURCE.
			if got := string(planLine[gapEnd : gapEnd+len(source)]); got != source {
				t.Fatalf("last column = %q, want %q starting exactly where a normal row would put it", got, source)
			}

			th, err := theme.Lookup("dark")
			if err != nil {
				t.Fatal(err)
			}
			mp := NewList(nil, keymap.Default(), th)
			mp.width = 80
			rendered := mp.renderRowPainted(item, false)
			if lines := strings.Split(rendered, "\n"); len(lines) != 1 {
				t.Fatalf("painted row wrapped onto %d lines, want exactly 1: %q", len(lines), rendered)
			}
			strippedPainted := []rune(ansi.Strip(rendered))
			if w := len(strippedPainted); w != 80 {
				t.Fatalf("painted row: width = %d, want 80", w)
			}
			if got := string(strippedPainted[gapEnd : gapEnd+len(source)]); got != source {
				t.Fatalf("painted last column = %q, want %q at the same offset as the system path", got, source)
			}
		})
	}
}

// TestListColumnWidthsYieldsTitleThenRegion re-derives the column arithmetic
// directly against listColumnWidths: NAME stops at its ceiling and hands the
// region every cell above it, and on the way down NAME floors at
// listMinTitleWidth before the trailing region gives up anything -- then the
// region gives up one cell for every cell the row loses, and nothing else moves.
//
// THE CURVE IS MONOTONIC, and that is asserted across every row width rather
// than left to the table: one cell more of row is never one cell less of either
// column.
func TestListColumnWidthsYieldsTitleThenRegion(t *testing.T) {
	for _, tc := range []struct {
		rowWidth              int
		wantTitle, wantRegion int
	}{
		{176, listMaxTitleWidth, 93}, // a 180-column terminal: NAME at its ceiling, every surplus cell the region's
		{112, listMaxTitleWidth, 29}, // the first width past the ceiling -- the region's first surplus cell
		{111, listMaxTitleWidth, 28}, // the LAST width where NAME's own growth and its ceiling agree
		{76, 33, 28},                 // an 80-column terminal's ACTUAL row (ui.RailWidth's 2-cell margin, twice)
		{59, 16, 28},                 // the last width before the region yields at all
		{58, 16, 27},                 // the region gives up its first cell
		{51, 16, 20},                 // ... and one more for every cell the row loses
		{listMinWidth, 16, 0},        // the gate's own floor: the region is fully spent, NAME still at its own floor
	} {
		t.Run(fmt.Sprintf("rowWidth=%d", tc.rowWidth), func(t *testing.T) {
			gotTitle, gotRegion := listColumnWidths(tc.rowWidth)
			if gotTitle != tc.wantTitle || gotRegion != tc.wantRegion {
				t.Fatalf("listColumnWidths(%d) = (%d, %d), want (%d, %d)",
					tc.rowWidth, gotTitle, gotRegion, tc.wantTitle, tc.wantRegion)
			}
		})
	}

	prevTitle, prevRegion := listColumnWidths(listMinWidth)
	for w := listMinWidth + 1; w <= 200; w++ {
		title, region := listColumnWidths(w)
		if title < prevTitle || region < prevRegion {
			t.Fatalf("listColumnWidths(%d) = (%d, %d) after (%d, %d) at one cell narrower -- a wider row took a cell from a column",
				w, title, region, prevTitle, prevRegion)
		}
		if title+region != w-listRowFixedWidth {
			t.Fatalf("listColumnWidths(%d) = (%d, %d), which does not spend the %d cells the fixed columns leave",
				w, title, region, w-listRowFixedWidth)
		}
		prevTitle, prevRegion = title, region
	}
}

// TestAWideTerminalSpendsItsSurplusOnTheRegion is what replaced an earlier
// test built around listMaxRowWidth's old reference width. That test existed
// because NO REAL TERMINAL EVER REACHED the 120-cell cap -- rowWidth was
// min(canvasWidth, listMaxRowWidth-2*listMargin), so the true ceiling any
// m.width could produce was 116 -- which made 120 a constant to drive
// functions with rather than a width to render at. The cap is gone now, so
// the interesting width is no longer a number no terminal reaches: it is an
// ORDINARY WIDE TERMINAL, and this drives one.
//
// IT ASSERTS THE THREE THINGS THE SURPLUS IS SUPPOSED TO DO: NAME stops at its
// ceiling, the region takes everything above it, and SOURCE spends that surplus
// on a path the base region would have clipped.
func TestAWideTerminalSpendsItsSurplusOnTheRegion(t *testing.T) {
	// A 180-column terminal, through the same arithmetic a live model uses.
	m := NewList(nil, keymap.Default(), nil)
	m.width = 180
	rowWidth := m.rowWidth()

	titleWidth, regionWidth := listColumnWidths(rowWidth)
	if titleWidth != listMaxTitleWidth {
		t.Fatalf("NAME = %d, want its ceiling %d", titleWidth, listMaxTitleWidth)
	}
	if want := rowWidth - listRowFixedWidth - listMaxTitleWidth; regionWidth != want {
		t.Fatalf("region = %d, want %d -- every cell NAME did not take", regionWidth, want)
	}
	if regionWidth <= listRegionWidth {
		t.Fatalf("region = %d, no bigger than its base %d; the surplus went somewhere else", regionWidth, listRegionWidth)
	}

	// The HEADER's label spans the whole region, from the cell the region starts at.
	labels := m.columnLabels(listBandWidth+listGlyphWidth, rowWidth, sectionMine)
	if got, want := labels[rowWidth-regionWidth:], padRight(listSourceLabel, regionWidth); got != want {
		t.Fatalf("Your plans region label = %q, want %q -- the label spans the whole region", got, want)
	}

	// And the BODY spends it: a path the base region clips comes whole.
	const longPath = "/work/a-path-longer-than-the-base-region/plan.md"
	if len(longPath) <= listRegionWidth || len(longPath) > regionWidth {
		t.Fatalf("test setup: %d-cell path must fall between the base region (%d) and this one (%d)", len(longPath), listRegionWidth, regionWidth)
	}
	item := planItem{plan: domain.Plan{ID: "l_src", Title: "Plan", SourceHint: longPath}}
	if got := ansi.Strip(m.renderRowPainted(item, false)); !strings.Contains(got, longPath) {
		t.Fatalf("row = %q, want the whole path %q -- the surplus is SOURCE's", got, longPath)
	}
}

// TestBothSectionsShareOneGeometryAtTheRemainingReferenceWidths drives the
// narrow reference widths through a LIVE ListModel: rowWidth 59, where the
// trailing region is still at its full base; 51, where it has yielded to NAME's
// floor; and listMinWidth, where it is fully spent. An 80-column terminal is
// TestListHeaderRowAlignsWithDataColumns' own width, and not repeated here.
//
// EVERY OFFSET BELOW IS COMPUTED ONCE, OUTSIDE THE PER-SECTION CHECKS, and
// reused for both sections and for the header AND the body: that is what
// "both sections start every column at the same cell" and "header and body
// agree" mean operationally -- a bug that gave one section its own titleWidth
// or its own region width would fail ONE of the two per-section checks below
// while the other still passed, not both together.
func TestBothSectionsShareOneGeometryAtTheRemainingReferenceWidths(t *testing.T) {
	const sourceHint = "/plan.md"
	opened := listFixtureEpoch.Add(-3 * time.Hour)

	for _, tc := range []struct {
		name                  string
		width                 int
		wantTitle, wantRegion int
	}{
		{"rowWidth 59 -- the region's own last unyielded width", 59, 16, 28},
		{"rowWidth 51 -- the region yielding to NAME's floor", 51, 16, 20},
		{"listMinWidth -- the region fully spent", listMinWidth, 16, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewList(nil, keymap.Default(), nil)
			m.width = tc.width
			m.now = func() time.Time { return listFixtureEpoch }
			// Below ui.RailWidth's 80-cell floor the margin is 0, so rowWidth()
			// equals m.width directly -- confirmed rather than assumed, since
			// that substitution is exactly what falsified two earlier drafts of
			// this arithmetic at width 80.
			if got := m.rowWidth(); got != tc.width {
				t.Fatalf("test setup: m.rowWidth() = %d at m.width = %d, want them equal", got, tc.width)
			}

			titleWidth, regionWidth := listColumnWidths(m.rowWidth())
			if titleWidth != tc.wantTitle || regionWidth != tc.wantRegion {
				t.Fatalf("listColumnWidths(%d) = (%d, %d), want (%d, %d)", tc.width, titleWidth, regionWidth, tc.wantTitle, tc.wantRegion)
			}

			titleStart := listBandWidth + listGlyphWidth
			countsStart := titleStart + titleWidth + listGapWidth
			lastStart := countsStart + listCountsWidth + listLastGapWidth

			plan := planItem{plan: domain.Plan{ID: "l_src", Title: "Plan", SourceHint: sourceHint}}
			for _, sc := range []struct {
				section  listSection
				label    string
				row      row
				wantCell string
			}{
				{sectionMine, listSourceLabel, row{kind: rowPlan, item: plan, section: sectionMine}, homeRelative(sourceHint)},
				{sectionRecent, listOpenedLabel, row{kind: rowPlan, item: plan, section: sectionRecent, opened: opened}, "3h ago"},
			} {
				header := strings.TrimPrefix(ansi.Strip(m.renderBodyRowPainted(m.sectionHead(sc.section)[1], false)), strings.Repeat(" ", m.listMargin()))
				if idx := strings.Index(header, "NAME"); idx != titleStart {
					t.Fatalf("%s: NAME at %d, want %d", sc.section.title(), idx, titleStart)
				}
				if idx := strings.Index(header, "COMMENTS"); idx != countsStart {
					t.Fatalf("%s: COMMENTS at %d, want %d", sc.section.title(), idx, countsStart)
				}
				if regionWidth == 0 {
					// A zero-width region has nothing left to search for -- the
					// label truncates to "", which strings.Index reports "found"
					// at 0, not at lastStart. The header must simply END at
					// lastStart instead: the region is there, and it is empty.
					if w := ansi.StringWidth(header); w != lastStart {
						t.Fatalf("%s header is %d cells, want it to end at %d (an empty region)", sc.section.title(), w, lastStart)
					}
				} else if idx := strings.Index(header, sc.label); idx != lastStart {
					t.Fatalf("%s: %s at %d, want %d (region start)", sc.section.title(), sc.label, idx, lastStart)
				}

				// The section's own ROW, sliced at the identical offsets the
				// header above just used -- header and body agreeing, not just
				// each internally consistent.
				body := []rune(strings.TrimPrefix(ansi.Strip(m.renderBodyRowPainted(sc.row, false)), strings.Repeat(" ", m.listMargin())))
				if got, want := string(body[lastStart:lastStart+regionWidth]), padRight(ansi.Truncate(sc.wantCell, regionWidth, "…"), regionWidth); got != want {
					t.Fatalf("%s row: region cell = %q, want %q", sc.section.title(), got, want)
				}
			}
		})
	}
}

// TestYourPlansHeaderRendersCOMMENTSInFullAt80Columns is the regression test,
// BY NAME, for a design that was tried and reversed: an earlier draft made
// listColumnWidths section-aware: "Your plans", which drew no trailing column
// of its own in that design, spent no listLastGapWidth on one, so the row
// overflowed by three cells and COMMENTS truncated to "COMMEN". That draft
// also put NAME 31 cells apart between sections at this same width; both
// defects were broken exactly at 80 columns and invisible in prose.
//
// TestListHeaderRowAlignsWithDataColumns already checks this width's header
// content more broadly ("COMMENTS SOURCE" as one separated pair); this test
// exists as its own, separately named anchor for that one historical defect, so
// a reader searching for "the reversed design broke COMMENTS" finds it directly
// rather than as one assertion inside a wider test.
func TestYourPlansHeaderRendersCOMMENTSInFullAt80Columns(t *testing.T) {
	m := NewList(nil, keymap.Default(), nil)
	m.width = 80
	if got := m.rowWidth(); got != 76 {
		t.Fatalf("test setup: m.rowWidth() = %d at m.width = 80, want 76 (ui.RailWidth's 2-cell margin, twice)", got)
	}
	header := ansi.Strip(m.renderBodyRowPainted(m.sectionHead(sectionMine)[1], false))
	if !strings.Contains(header, "COMMENTS") {
		t.Fatalf("Your plans header at an 80-column terminal = %q, want COMMENTS in full (not truncated to COMMEN)", header)
	}
}

// TestGlyphIsFourDerivedStates pins the glyph against the facts it is
// derived from rather than against a stored status. approvedTip wins over
// open threads deliberately: you may approve bytes with threads still
// unresolved -- COMMENTS still names that fact, but only by subtraction now
// (resolved/total), which is the only way it ever could: approvedTip has
// always beaten open here, and the ink-sharing paragraph below is a
// separate collapse. A missing file wins over both: it is what the reader
// has to act on first.
//
// THAT SUBTRACTION IS DELIBERATE, NOT A DEFECT. The old open/total
// column showed an open count directly, but got the COMMON case backwards:
// 1/1 reads as complete under the x/y convention every reader already
// brings to a fraction, while the glyph beside it said the opposite.
// resolved/total fixes the common case and costs the rarer one -- an
// approved plan with a thread still open now renders ● beside, say, 2/3,
// and that is the accepted trade working as intended, not a bug to file.
//
// TWO TINTS OVER FOUR STATES, and the table's own wantTint column is only half
// of that claim: glyph() answering the right tint is worth nothing if the
// RENDER collapses the wrong pair, so the sub-test below renders the circles
// and checks the GROUPING rather than each one alone.
//
// WHICH PAIR SHARES AN INK IS A DECISION AND IT CHANGED. ● approved and ◐ open
// wear OK's green together now, because both say "this plan has been engaged
// with" and the SHAPE says which -- the scheme the review view's gutter already
// used, where ※ open and ○ all-resolved share one colour (ui/painted.go). What
// the ink still separates is engaged from quiet, so ○ must not join them. ─
// shares ○'s dim, and its shape is what tells those two apart.
//
// THE OLD DEFECT IS THE OPPOSITE PAIRING AND IS STILL GUARDED: while glyph()
// returned a bool, ◐ and ○ came out in one accent -- a plan with open threads
// painted as a quiet one. That is the collapse this asserts against, and it is
// not the one now made on purpose.
func TestGlyphIsFourDerivedStates(t *testing.T) {
	states := []struct {
		name        string
		approvedTip bool
		open        int
		fileMissing bool
		wantGlyph   rune
		wantTint    glyphTint
	}{
		{"approved and quiet", true, 0, false, '●', tintApproved},
		{"approved with open threads", true, 4, false, '●', tintApproved},
		{"unapproved with open threads", false, 4, false, '◐', tintAttention},
		{"unapproved and quiet", false, 0, false, '○', tintQuiet},
		{"a missing file over an approval", true, 4, true, '─', tintQuiet},
		{"a missing file over open threads", false, 4, true, '─', tintQuiet},
		{"a missing file, quiet", false, 0, true, '─', tintQuiet},
	}
	item := func(approvedTip bool, open int, fileMissing bool) planItem {
		return planItem{
			plan:        domain.Plan{ID: "p1", Title: "Plan", SourceHint: "/plan.md"},
			approvedTip: approvedTip,
			open:        open,
			fileMissing: fileMissing,
		}
	}
	for _, tc := range states {
		t.Run(tc.name, func(t *testing.T) {
			g, tint := item(tc.approvedTip, tc.open, tc.fileMissing).glyph()
			if g != tc.wantGlyph || tint != tc.wantTint {
				t.Fatalf("glyph() = %q,%v; want %q,%v", g, tint, tc.wantGlyph, tc.wantTint)
			}
		})
	}

	// THE RUNES ARE CHECKED DISTINCT FIRST, because with ● and ◐ on one ink the
	// shape is the ONLY thing left telling them apart: a table edit that gave two
	// states the same circle would make every tint assertion below unfalsifiable
	// while staying green.
	t.Run("the three circles are three shapes", func(t *testing.T) {
		seen := map[rune]string{}
		for _, tc := range states[1:4] {
			if prev, dup := seen[tc.wantGlyph]; dup {
				t.Fatalf("%q and %q both draw %q; two states sharing an ink AND a shape are one state", prev, tc.name, tc.wantGlyph)
			}
			seen[tc.wantGlyph] = tc.name
		}
	})

	t.Run("engaged shares one tint and quiet keeps its own", func(t *testing.T) {
		th, err := theme.Lookup("dark")
		if err != nil {
			t.Fatal(err)
		}
		ms := NewList(nil, keymap.Default(), nil)
		ms.width = 80
		mp := NewList(nil, keymap.Default(), th)
		mp.width = 80

		for _, path := range []struct {
			name   string
			render func(planItem) string
		}{
			{"system", func(it planItem) string { return ms.renderRowPainted(it, false) }},
			{"painted", func(it planItem) string { return mp.renderRowPainted(it, false) }},
		} {
			tint := map[rune]string{}
			for _, tc := range states[1:4] { // one row per circle
				line := path.render(item(tc.approvedTip, tc.open, tc.fileMissing))
				idx := strings.IndexRune(ansi.Strip(line), tc.wantGlyph)
				if idx < 0 {
					t.Fatalf("%s: %q missing from %q", path.name, tc.wantGlyph, line)
				}
				// The styling in front of the glyph is the tint: everything up
				// to the glyph's own rune, escapes included.
				tint[tc.wantGlyph] = line[:strings.IndexRune(line, tc.wantGlyph)]
			}
			if tint['\u25cf'] != tint['\u25d0'] {
				t.Fatalf("%s: ● renders in %q and ◐ in %q -- both are engaged states and wear one ink, with the shape saying which",
					path.name, tint['\u25cf'], tint['\u25d0'])
			}
			if tint['\u25cb'] == tint['\u25cf'] {
				t.Fatalf("%s: ○ renders in the same tint as ● and ◐ (%q) -- engaged against quiet is the one distinction the ink still draws",
					path.name, tint['\u25cb'])
			}
		}
	})
}

// drawnGlyph is the state glyph section paints on the row of the plan titled
// title: the first cell after the margin and the cursor band, off the rendered
// row rather than off glyph(), so a renderer that stopped asking glyph() would
// show here.
func drawnGlyph(t *testing.T, m *ListModel, section listSection, title string) rune {
	t.Helper()
	for _, r := range sectionContentRows(m, section.title()) {
		if r.kind == rowPlan && r.item.plan.Title == title {
			return []rune(strings.TrimLeft(ansi.Strip(m.renderBodyRowPainted(r, false)), " ▌"))[0]
		}
	}
	t.Fatalf("%s draws no row for %q: %q", section.title(), title, sectionBodyText(m, section))
	return 0
}

// TestAPlanWhoseFileIsGoneDrawsADash pins this through a refresh: the plan's
// file removed, then put back, and its row following each at the next refresh.
func TestAPlanWhoseFileIsGoneDrawsADash(t *testing.T) {
	f := newListFixture(t)
	const doc = "# Rollout\n\nbody\n"
	sess := seedListPlan(t, f, filepath.Dir(f.path), "rollout.md", "Rollout", doc)
	m := NewList(f.svc, keymap.Default(), nil)
	m.files = untimedFileCheck()
	m = drainList(t, m, m.Init())
	for _, step := range []struct {
		name   string
		change func()
		want   rune
	}{
		{"there", func() {}, '○'},
		{"removed", func() { mustRemove(t, sess.Path) }, '─'},
		{"restored", func() {
			if err := os.WriteFile(sess.Path, []byte(doc), 0o644); err != nil {
				t.Fatal(err)
			}
		}, '○'},
	} {
		step.change()
		m = drainList(t, m, m.refreshCmd())
		if got := drawnGlyph(t, m, sectionMine, "Rollout"); got != step.want {
			t.Fatalf("file %s: Your plans draws %q, want %q", step.name, got, step.want)
		}
	}
}

// TestLeftClip pins leftClip's width contract at both boundaries the
// remove-n-cells accounting in ansi.TruncateLeft creates. The first is width
// 1, the ellipsis prefix's own width: that accounting never crosses its ">"
// threshold when n lands exactly on the string's full width, so it must be
// special-cased — verified red against the un-special-cased version, which
// returned "" instead of "…" at width 1.
//
// THE SECOND IS A DOUBLE-WIDTH CHARACTER AT AN EVEN BUDGET. TruncateLeft
// cannot cut a 2-cell character in half, so an even budget leaves it one cell
// OVER — verified red at 5 cells for a budget of 4. Budget 2 is the retry's
// own edge: it drops the whole string, and a bare cut mark is the only honest
// answer left.
func TestLeftClip(t *testing.T) {
	const s = "/very/long/path/to/plan.md"
	const wide = "測測測測"
	tests := []struct {
		in    string
		width int
		want  string
	}{
		{s, 0, ""},
		{s, 1, "…"},
		{s, 2, "…d"},
		{s, 3, "…md"},
		{s, len(s), s},     // fits exactly, no clip
		{s, len(s) + 5, s}, // wider than needed, unchanged
		{wide, 4, "…測"},
		{wide, 5, "…測測"},
		{wide, 2, "…"},
	}
	for _, tt := range tests {
		got := leftClip(tt.in, tt.width)
		if got != tt.want {
			t.Errorf("leftClip(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
		}
		if n := ansi.StringWidth(got); n > max(tt.width, 0) {
			t.Errorf("leftClip(%q, %d) is %d cells, over its budget: %q", tt.in, tt.width, n, got)
		}
	}
}

// TestCtrlCAlwaysQuitsEveryListMode: ctrl+c is the unconditional kill switch,
// intercepted at the top of ListModel.Update before any mode dispatch. Every
// mode must yield tea.QuitMsg, including rename with an unsaved draft (the
// draft must never reach the textarea).
func TestCtrlCAlwaysQuitsEveryListMode(t *testing.T) {
	newFixtureList := func(t *testing.T) *ListModel {
		t.Helper()
		f := newListFixture(t)
		dir := filepath.Dir(f.path)
		seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody.\n")
		m := NewList(f.svc, keymap.Default(), nil)
		m.width, m.height = 100, 30
		return drainList(t, m, m.Init())
	}

	tests := []struct {
		name  string
		build func(t *testing.T) *ListModel
	}{
		{
			name:  "browse",
			build: newFixtureList,
		},
		{
			name: "rename with typed draft",
			build: func(t *testing.T) *ListModel {
				m := pressList(newFixtureList(t), "e", "!")
				if m.mode != listRename {
					t.Fatalf("test setup: mode = %v, want listRename", m.mode)
				}
				return m
			},
		},
		{
			name: "confirm-delete",
			build: func(t *testing.T) *ListModel {
				m := pressList(newFixtureList(t), "d")
				if m.mode != listConfirmDelete {
					t.Fatalf("test setup: mode = %v, want listConfirmDelete", m.mode)
				}
				return m
			},
		},
		{
			// The filter is the mode where "c" itself is a character, so
			// ctrl+c reaching the kill switch rather than the query is the
			// whole claim: Update takes it ahead of every mode dispatch.
			name: "filter with a typed query",
			build: func(t *testing.T) *ListModel {
				m := pressList(newFixtureList(t), "/", "c")
				if m.mode != listFilter || m.filter != "c" {
					t.Fatalf("test setup: mode = %v, query = %q, want listFilter and %q", m.mode, m.filter, "c")
				}
				return m
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.build(t)
			_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
			if cmd == nil {
				t.Fatal("ctrl+c must return a cmd")
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatal("ctrl+c must produce tea.QuitMsg")
			}
		})
	}
}

// TestOrdinaryDeleteConfirmTextIsVerbatim pins ordinaryDeleteConfirmText
// against the pinned text, character for character, asserted whole rather
// than by substring.
//
// A `want` BUILT BY CALLING ordinaryDeleteConfirmText ITSELF would prove only
// that the arguments arrived, never that the composer still spells the
// pinned words correctly -- a mistake made once before, where four literal
// `want` strings became calls to the function under test and every mutation
// stayed green through them.
func TestOrdinaryDeleteConfirmTextIsVerbatim(t *testing.T) {
	want := "Delete \"Auth redesign\" and its comment threads?\n\n" +
		"y · delete\n" +
		"esc · go back"
	if got := ordinaryDeleteConfirmText(domain.Plan{Title: "Auth redesign"}); got != want {
		t.Fatalf("ordinaryDeleteConfirmText mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

// TestTheDeleteConfirmSpellsAControlByteOneWay: `Delete %q` escaped the TITLE
// the way Go prints a string, where ListModel.confirmLines visualises the
// same byte as a Control Picture everywhere else.
//
// THE %q IS NOT A DEFENCE, though it has been recorded as one: confirmLines
// filters the whole panel, so what the %q bought was a second spelling.
// Mutation: the %s back to %q -- "the panel spells the same byte two ways".
func TestTheDeleteConfirmSpellsAControlByteOneWay(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	const ctl, glyph = "\b", "␈"
	plan := domain.Plan{
		ID: "l_a", Title: "Requires approval" + ctl + "No approval needed",
		SourceHint: "~/plans/a.md",
	}
	for _, tc := range []struct {
		name    string
		confirm string
		want    int
	}{
		{"the ordinary panel", ordinaryDeleteConfirmText(plan), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// THE FIELD FIRST: every string this panel names has to arrive
			// carrying the byte, or the filter below has nothing to spell and
			// the count is met by accident.
			if got := strings.Count(tc.confirm, ctl); got != tc.want {
				t.Fatalf("the confirm text holds %d raw %q and the panel names %d supplied strings: %q -- a field spelled with %%q arrives already escaped, so the visualisation never sees it and the panel shows a Go escape beside a Control Picture",
					got, ctl, tc.want, tc.confirm)
			}
			m := onePlanList(th, nil, plan, listBrowse)
			m.confirm = tc.confirm
			m.setMode(listConfirmDelete)
			panel := ansi.Strip(strings.Join(m.confirmLines(), "\n"))
			if got := strings.Count(panel, glyph); got != tc.want {
				t.Fatalf("the panel draws %d %q against %d supplied strings carrying the byte: %q", got, glyph, tc.want, panel)
			}
			if strings.Contains(panel, `\b`) {
				t.Fatalf("the panel spells the same byte two ways -- a Go escape and a Control Picture, in one panel: %q", panel)
			}
		})
	}
}

// TestTruncateConfirmLinesKeepsTheHeadAndTailAtEveryBudget pins
// truncateConfirmLines' own doc comment directly, table-driven across every
// budget shape the function branches on: it prefers the first line and the
// last line over anything between them, INCLUDING at budget==1, where an
// earlier version returned a bare "…" instead -- dropping the panel's own
// final row, the one line this function exists to protect, at exactly the size
// where a human reading the screen needs it most.
//
// THE FIRST TABLE IS ONE ROW PER LINE, which is the shape a body of unwrapped
// one-row lines arrives in.
func TestTruncateConfirmLinesKeepsTheHeadAndTailAtEveryBudget(t *testing.T) {
	lines := []string{"question", "a", "b", "c", "d", "last"}
	groups := make([][]string, 0, len(lines))
	for _, l := range lines {
		groups = append(groups, []string{l})
	}
	tests := []struct {
		budget int
		want   []string
	}{
		{-1, []string{"last"}},     // floored to 1
		{0, []string{"last"}},      // floored to 1
		{1, []string{"last"}},      // no room for an ellipsis alongside the tail either
		{2, []string{"…", "last"}}, // no room for a head line too
		{3, []string{"question", "…", "last"}},
		{4, []string{"question", "…", "d", "last"}},
		{len(lines), lines},     // fits exactly, unmodified
		{len(lines) + 5, lines}, // wider than needed, unmodified
	}
	for _, tt := range tests {
		if got := truncateConfirmLines(groups, tt.budget); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("truncateConfirmLines(groups, %d) = %v, want %v", tt.budget, got, tt.want)
		}
	}
}

// TestTruncateConfirmLinesKeepsALineWholeOrDropsIt is the half the table above
// cannot state: with one row per line there is no line to cut in half, so
// every expectation there passes whether or not the function knows what a line
// is.
//
// The body is the shape the source-fault panel actually has -- a headline
// that wraps, a paragraph, and four key lines, the last of them the way out
// -- and the budgets walk it down from "everything fits" to "only the way out
// does".
func TestTruncateConfirmLinesKeepsALineWholeOrDropsIt(t *testing.T) {
	var (
		head   = []string{"the file is gone", "or has moved:"}
		body   = []string{"there is still a cache", "of the last version"}
		option = []string{"o · keep it with", "no file at all"}
		del    = []string{"d · delete this plan"}
		wayOut = []string{"esc · go back", "to the list"}
	)
	groups := [][]string{head, body, option, del, wayOut}
	for _, tt := range []struct {
		budget int
		want   []string
	}{
		// Everything fits: nothing is elided and there is no "…" at all.
		{9, []string{"the file is gone", "or has moved:", "there is still a cache", "of the last version", "o · keep it with", "no file at all", "d · delete this plan", "esc · go back", "to the list"}},
		// One row short of the whole body. The paragraph is the only line
		// that can go, and it goes ENTIRELY -- dropping one of its two rows
		// would have been enough for the budget and is what this function
		// exists to refuse.
		{8, []string{"the file is gone", "or has moved:", "…", "o · keep it with", "no file at all", "d · delete this plan", "esc · go back", "to the list"}},
		// SEVEN IS THE CASE A MUTATION LIVES IN and the only budget here
		// that leaves a row unspent: after the headline, the "…" and the two
		// lines below it there is exactly ONE row left and the option line
		// needs two, so the row goes UNUSED. A truncator that filled it with
		// the option's last row would come in on budget, look full, and say
		// "no file at all" with nothing naming the key -- which is the
		// defect, verbatim. As a mutation: keeping the tail rows of the line
		// that does not fit passes every other case in this table and every
		// panel-level test in this package, and fails only here.
		{7, []string{"the file is gone", "or has moved:", "…", "d · delete this plan", "esc · go back", "to the list"}},
		// The option line no longer fits whole, so it is not shown at all --
		// the defect this fix closed showed its last two rows instead, which
		// told a reader about an option and never named its key.
		{6, []string{"the file is gone", "or has moved:", "…", "d · delete this plan", "esc · go back", "to the list"}},
		// Down to the headline and the way out. The ordering is the one
		// this function has always had -- the way out first, the headline
		// second, more of the tail only after that -- so the two key lines
		// in between go before the headline does.
		{5, []string{"the file is gone", "or has moved:", "…", "esc · go back", "to the list"}},
		// No room for the headline either, and the row it would have left
		// spare goes to the key line above the way out rather than to half a
		// headline.
		{4, []string{"…", "d · delete this plan", "esc · go back", "to the list"}},
		// Room for the way out and an ellipsis, and nothing else.
		{3, []string{"…", "esc · go back", "to the list"}},
		// Room for the way out and NOT for an ellipsis beside it: the way
		// out wins outright, whole, which is the budget==1 rule of the
		// table above applied to a line of two rows.
		{2, wayOut},
		// The way out cannot be shown whole at all. This is the one case
		// where a fragment is right -- a screen with no visible way off it
		// is worse than half a sentence -- and it is the row-level fallback,
		// unchanged from what every other confirm panel has always done.
		{1, []string{"to the list"}},
	} {
		if got := truncateConfirmLines(groups, tt.budget); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("truncateConfirmLines(groups, %d) =\n%s\nwant\n%s", tt.budget, strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
		}
	}
}

// TestTruncateConfirmLinesFallsBackToRowsForOneLongLine is the OTHER panels'
// case, and it is why the whole-line fix could be made in the shared function
// rather than forked for the centred one. A body of a single logical line --
// the ordinary delete confirm, the approve, every text that is one
// sentence -- has no whole-line answer once it outgrows its
// budget: keeping it whole would show nothing, so head/ellipsis/tail rows are
// still exactly what it produces, legend included.
func TestTruncateConfirmLinesFallsBackToRowsForOneLongLine(t *testing.T) {
	one := [][]string{{"delete the plan", "and its threads?", "this cannot be", "undone. (y/n)"}}
	for _, tt := range []struct {
		budget int
		want   []string
	}{
		{4, []string{"delete the plan", "and its threads?", "this cannot be", "undone. (y/n)"}},
		{3, []string{"delete the plan", "…", "undone. (y/n)"}},
		{2, []string{"…", "undone. (y/n)"}},
		{1, []string{"undone. (y/n)"}},
	} {
		if got := truncateConfirmLines(one, tt.budget); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("truncateConfirmLines(one long line, %d) = %v, want %v", tt.budget, got, tt.want)
		}
	}
}

// TestListConfirmDeleteHelpBarDoesNotAdvertiseBrowseKeys: the defect is not a
// missing hint -- it is a wrong one. updateConfirmDelete owns every keystroke
// once listConfirmDelete is entered (y/n/esc, literal, not routed through the
// keymap), so the bottom help bar must not go on showing hintLine()'s
// browse-mode reference: that would tell the user those keys are still live.
// Asserted against m.hintLine() itself rather than retyped as a literal, so a
// future rebind or wording change cannot silently stop this test from meaning
// anything.
func TestListConfirmDeleteHelpBarDoesNotAdvertiseBrowseKeys(t *testing.T) {
	for _, tc := range []struct {
		name    string
		painted bool
	}{
		{"system", false},
		{"painted", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var th *theme.Theme
			if tc.painted {
				var err error
				if th, err = theme.Lookup("dark"); err != nil {
					t.Fatal(err)
				}
			}
			f := newListFixture(t)
			dir := filepath.Dir(f.path)
			seedListPlan(t, f, dir, "plan.md", "Some Plan", "# Some Plan\n\nBody.\n")

			m := NewList(f.svc, keymap.Default(), th)
			// 100, not 80: a plan row's bar is 79 cells -- past the 76 an 80-column
			// terminal actually offers (hintLine's own doc comment) -- so the FULL
			// text this test's own setup check looks for would clip there. Nothing
			// else about this test is about width; 100 is every other list
			// fixture's own default.
			m.width, m.height = 100, 24
			m = drainList(t, m, m.Init())

			browseHint := m.hintLine()
			if !strings.Contains(ansi.Strip(m.View().Content), browseHint) {
				t.Fatalf("test setup: browse mode's own View() does not contain hintLine()'s own text %q -- fixture is broken", browseHint)
			}

			m = pressList(m, "d")
			if m.mode != listConfirmDelete {
				t.Fatalf("mode = %v, want listConfirmDelete", m.mode)
			}
			view := ansi.Strip(m.View().Content)
			if strings.Contains(view, browseHint) {
				t.Fatalf("View() during listConfirmDelete still contains hintLine()'s browse-mode text %q -- "+
					"it advertises enter/e/d/q as live when updateConfirmDelete only accepts y/n/esc:\n%s", browseHint, view)
			}
			if !strings.Contains(view, confirmDeleteHint) {
				t.Fatalf("View() during listConfirmDelete does not render confirmDeleteHint %q:\n%s", confirmDeleteHint, view)
			}
		})
	}
}

// TestListDeleteConfirmOrdinaryRendersOnBothPathsAndReservesItsSpace covers
// the two places the delete panel was found disagreeing, panelViewPainted and
// viewHeight, over two palettes, on a plan read from a real store.
//
// THE RENDER CHECK IS PER LOGICAL LINE (viewContainsFlowing), NOT A SINGLE
// strings.Contains, because ordinaryDeleteConfirmText carries its own two key
// lines: the body is not one line the box never reflows, so a literal
// substring check would have to reproduce the box's own padding and border
// characters between "threads?" and "y · delete" to pass.
func TestListDeleteConfirmOrdinaryRendersOnBothPathsAndReservesItsSpace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		painted bool
	}{
		{"system", false},
		{"painted", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var th *theme.Theme
			if tc.painted {
				var err error
				if th, err = theme.Lookup("dark"); err != nil {
					t.Fatal(err)
				}
			}
			f := newListFixture(t)
			seedListPlan(t, f, filepath.Dir(f.path), "auth.md", "Auth redesign", "# Auth redesign\n\nBody.\n")
			m := NewList(f.svc, keymap.Default(), th)
			m.width, m.height = 80, 24
			m = drainList(t, m, m.Init())
			if len(m.items) != 1 {
				t.Fatalf("test setup: items = %d, want 1", len(m.items))
			}
			plan := m.items[0].plan
			browseVH := m.viewHeight()

			m = pressList(m, "d")
			if m.mode != listConfirmDelete {
				t.Fatalf("mode = %v, want listConfirmDelete", m.mode)
			}
			want := ordinaryDeleteConfirmText(plan)
			if m.confirm != want {
				t.Fatalf("confirm = %q, want %q", m.confirm, want)
			}
			view := ansi.Strip(m.View().Content)
			for _, frag := range strings.Split(want, "\n") {
				if frag == "" {
					continue
				}
				if !viewContainsFlowing(t, view, frag) {
					t.Fatalf("View() does not render %q from the imported ordinaryDeleteConfirmText wording:\n%s", frag, view)
				}
			}
			// THE PANEL DISPLACES NOTHING: this mode draws the centred box
			// (drawsCentredPanel), which reserves no row of its own, so
			// viewHeight() is browse's OWN number and panelViewPainted draws no
			// strip. Both are asserted directly rather than through a
			// reservation formula that no longer applies.
			if got := m.viewHeight(); got != browseVH {
				t.Fatalf("viewHeight() during the delete confirm = %d, want %d (browse's own -- a centred mode reserves nothing)", got, browseVH)
			}
			if got := m.panelViewPainted(); got != "" {
				t.Fatalf("panelViewPainted() drew %q, want nothing -- the centred panel must not also draw the bottom strip", got)
			}
		})
	}
}

// viewContainsFlowing reports whether frag's words, in order, appear as a
// contiguous run somewhere INSIDE THE BOX in screen -- strings.Contains's
// tolerant counterpart for a fragment that cannot be assumed to have stayed
// on one screen row. The centred box caps a line's width at panelMeasure
// REGARDLESS OF THE TERMINAL'S OWN WIDTH (centredInterior's whole point), so a
// fragment whose own text was never fitted to that measure can wrap across two
// box rows even at a very wide terminal.
//
// boxGeometry (sourcefault_test.go) CUTS OUT THE BOX FIRST, and that is not a
// convenience -- a flatten run over the WHOLE screen is wrong on a wide
// terminal, where the box shares each of its rows with unrelated list content
// either side of it (the masthead, a row's own columns, a section's
// empty-state hint), so the flattened text reads the sidebar's own words
// mid-sentence, between two halves of one wrapped line that never sit next to
// each other. Restricting the flatten to the box's own columns (x, x+w) on the
// box's own rows (y, y+h) removes everything that could cause that.
//
// Box-drawing runes are stripped before the comparison because a wrap
// boundary inside the box inserts them (the closing "  ┃" of one row and the
// opening "┃  " of the next) directly between what were adjacent words in
// the source string, which a plain whitespace-collapse would leave in place.
func viewContainsFlowing(t *testing.T, screen, frag string) bool {
	t.Helper()
	x, y, w, h := boxGeometry(t, screen)
	rows := strings.Split(screen, "\n")
	var boxText strings.Builder
	for i := y; i < y+h && i < len(rows); i++ {
		row := []rune(rows[i])
		end := min(x+w, len(row))
		if x > len(row) {
			continue
		}
		boxText.WriteString(string(row[x:end]))
		boxText.WriteString("\n")
	}
	strip := strings.NewReplacer("┃", " ", "┏", " ", "┓", " ", "┗", " ", "┛", " ", "━", " ")
	flatten := func(s string) string { return strings.Join(strings.Fields(strip.Replace(s)), " ") }
	return strings.Contains(flatten(boxText.String()), flatten(frag))
}

// boxContainsRefusal is viewContainsFlowing's own trick (cut the box out by
// (x,y,w,h), strip the border runes) with a DIFFERENT rejoin, and it is not
// safe to fold into that function's shared behaviour.
//
// viewContainsFlowing REJOINS A WRAPPED FRAGMENT'S ROWS WITH ONE SPACE, which
// reconstructs prose wrapped at a word boundary correctly: ansi.Wordwrap
// consumes the space it broke on, so putting one back is undoing exactly that.
// It is the wrong reconstruction for a refusal line's own unbounded half -- an
// interpolated absolute path, driven long and hyphen-free, as go's own
// t.TempDir names already are. A token with nothing for ansi.Wordwrap to break
// on is cut by ansi.Hardwrap instead, which inserts NO character at all, so
// viewContainsFlowing's inserted space would land INSIDE a path the test never
// sent.
//
// STRIPPING EVERY WHITESPACE RUNE ON BOTH SIDES, rather than inserting one at
// each row boundary, is correct under EITHER kind of break without having to
// know which one occurred: a word-wrap break already discarded its space on
// the box's own side, and want's matching space is discarded the identical
// way on this side; a hard-wrap break discarded nothing on either side. What
// is left to compare is the sequence of non-space runes alone, which a wrap of
// either kind never reorders or drops.
func boxContainsRefusal(t *testing.T, screen, want string) bool {
	t.Helper()
	x, y, w, h := boxGeometry(t, screen)
	rows := strings.Split(screen, "\n")
	var boxText strings.Builder
	for i := y; i < y+h && i < len(rows); i++ {
		row := []rune(rows[i])
		end := min(x+w, len(row))
		if x > len(row) {
			continue
		}
		boxText.WriteString(string(row[x:end]))
		boxText.WriteString("\n")
	}
	strip := strings.NewReplacer("┃", " ", "┏", " ", "┓", " ", "┗", " ", "┛", " ", "━", " ")
	compact := func(s string) string { return strings.Join(strings.Fields(strip.Replace(s)), "") }
	return strings.Contains(compact(boxText.String()), compact(want))
}

// longSingleTokenTitle is one 120-rune token with no space anywhere for
// ansi.Wordwrap to break on: a plan's own Title is unbounded, and a title an
// agent or a paste could produce reaches the delete panel.
var longSingleTokenTitle = strings.Repeat("x", 120)

// TestListDeleteConfirmLinesNeverExceedWidthBudget pins the WIDTH half of the
// hazard: ansi.Wordwrap's only breakpoints are a space or a "-", so a single
// token wider than confirmLines' own budget -- a title with neither -- survives
// word-wrap intact and over-width, and the render then hard-wraps it a second
// time, painting more rows than the box reserved.
//
// THE BUDGET IS centredInterior(tc.width) AND THE RENDER CHECK IS THE BOX'S
// OWN, because listConfirmDelete draws the centred box: confirmGroups' own doc
// comment gives the width (three cells of frame a side), and panelViewPainted
// draws NOTHING for this mode, so what remains to pin is that centredBox
// agrees with what confirmGroups fed it.
func TestListDeleteConfirmLinesNeverExceedWidthBudget(t *testing.T) {
	for _, tc := range []struct {
		name          string
		confirm       func() string
		width, height int
	}{
		{
			"ordinary panel, 120-char single-token title",
			func() string {
				return ordinaryDeleteConfirmText(domain.Plan{Title: longSingleTokenTitle})
			},
			80, 24,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The styles go on at construction because confirmLines() reads
			// them: the control-byte visualisation runs in front of the
			// word-wrap, so the ROW COUNT this panel reserves depends on the
			// same styled substitution the row is drawn with, and a model
			// without styles is not a model this method can be asked about.
			th, err := theme.Lookup("dark")
			if err != nil {
				t.Fatal(err)
			}
			m := &ListModel{width: tc.width, height: tc.height, mode: listConfirmDelete, confirm: tc.confirm(), styles: ui.NewStyles(th)}

			budget := centredInterior(tc.width)
			for i, l := range m.confirmLines() {
				if w := ansi.StringWidth(l); w > budget {
					t.Fatalf("confirmLines()[%d] = %d display cells, want <= %d (centredInterior): %q", i, w, budget, l)
				}
			}

			// ONE render, and what it must agree with is what it was handed:
			// the box's own row count against confirmGroups' rows plus the
			// box's four border/blank rows.
			groups := m.centredGroups()
			boxRows := strings.Split(centredBox(groups, m.styles), "\n")
			if got, want := len(boxRows), panelGroupRows(groups)+4; got != want {
				t.Fatalf("centredBox draws %d rows, want %d -- the render disagrees "+
					"with what it was handed exactly the way it did before this fix", got, want)
			}
		})
	}
}

// listSectionItems builds n plan rows. Titles carry a "Plan" marker no piece of
// chrome does, which is what lets the measurements below count content rows off
// the rendered screen rather than off m.rows.
func listSectionItems(prefix string, n int) []planItem {
	out := make([]planItem, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, planItem{
			plan: domain.Plan{ID: domain.PlanID(fmt.Sprintf("%s%03d", prefix, i)), Title: fmt.Sprintf("%s Plan %03d", prefix, i)},
			// EVERY ROW CARRIES A DISTINCT ACTIVITY, DESCENDING WITH THE INDEX,
			// so slice order IS the order the default sort draws, which is what
			// production always hands this model. Left at the zero value these
			// rows order by TITLE instead, and a fixture that retitles one row
			// moves it to the top of its section.
			lastActivity: listFixtureEpoch.Add(time.Duration(-i) * time.Minute),
		})
	}
	return out
}

// listFixtureEpoch is where listSectionItems' activity clock starts. A fixed
// instant, never time.Now: nothing in this file may depend on when it runs.
var listFixtureEpoch = time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

// sectionContentRows returns the rows m allocated to the named section: what
// follows its two band rows, up to the next band (the separator, or the other
// section's own header). Answers nil when the section is not on screen at all.
//
// IT MATCHES ON CONTAINMENT, not equality: the band carries its section's SORT
// and an active-section mark around the title (bandTitle), so the row's whole
// text is not the title alone. Neither section title is a substring of the
// other, so the match is still exact in the way that matters.
func sectionContentRows(m *ListModel, title string) []row {
	start := -1
	for i, r := range m.rows {
		if r.kind == rowBand && strings.Contains(r.text, title) {
			start = i + 2 // the band and its column-label row
			break
		}
	}
	if start < 0 {
		return nil
	}
	end := start
	for end < len(m.rows) && m.rows[end].kind != rowBand {
		end++
	}
	return m.rows[start:end]
}

// countKind counts rows of one kind in a slice.
func countKind(rows []row, kind rowKind) int {
	n := 0
	for _, r := range rows {
		if r.kind == kind {
			n++
		}
	}
	return n
}

// TestListChromeIsNineInBrowseAndSixInAPanel re-derives the row budget by
// MEASUREMENT rather than by arithmetic: the rows browse spends on the
// masthead box, the band and the column row have to be checked against the real
// renderer before anything is built on them.
//
// It measures off the rendered screen, not off viewHeight: chrome is
// m.height minus the rows that actually carry section CONTENT, counted by
// their own marks -- the plan-state glyph every plan row starts with, and the
// "+N more" tail. "Your plans" is seeded far past any allocation so every
// allocatable row is content and none of the budget is filler, which is what
// makes the subtraction meaningful.
//
// A panel mode draws no masthead box and no band -- rename's masthead stays the
// plain single line it always was (listMastheadHeight's own note) -- so its
// chrome is three rows short of browse's and its whole reservation is its own
// panel height on top.
func TestListChromeIsNineInBrowseAndSixInAPanel(t *testing.T) {
	// The two literals, and they are deliberately literals: every constant in
	// this file is derived from the others, so a test that computed its
	// expectation from listBrowseChrome would move with any of them and
	// measure nothing.
	const wantBrowseChrome, wantPanelChrome = 9, 6
	if listBrowseChrome != wantBrowseChrome {
		t.Fatalf("listBrowseChrome = %d, want %d (ground, the masthead box's 3 rows, blank, the band, the column row, status, help)",
			listBrowseChrome, wantBrowseChrome)
	}

	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	items := listSectionItems("Mine", 200)

	for _, tm := range []*theme.Theme{nil, th} {
		for _, width := range []int{listMinWidth, 40, 80, 120, 200} {
			for height := listMinHeight; height <= 30; height++ {
				// listFilter is in the sweep because THE FILTER RESERVES
				// NOTHING: its query is drawn in the help bar every mode
				// already reserves, so its chrome is browse's own. A filter
				// that grew a line of its own would move the whole
				// allocation, which is the "sections re-allocate under a
				// filter" clause meaning something it must not mean.
				//
				// listConfirmDelete IS NOT IN THIS SWEEP, and NOT because its
				// chrome measures browse's own -- it does not. This sweep's
				// whole method is height minus content ROWS (the ones
				// carrying a plan's own "○" or a "+N more" tail) minus
				// panelRows, and a CENTRED mode breaks that subtraction: the
				// box is COMPOSITED OVER THE BODY, so it covers whatever
				// content rows its own footprint lands on -- rows this sweep
				// can no longer count -- while panelRows (the STRIP's own
				// reservation) does not move to compensate.
				// Its OWN geometry is asserted by
				// TestListDeleteConfirmOrdinaryRendersOnBothPathsAndReservesItsSpace
				// and TestCentredListPanelsReserveNothing, both of which
				// measure the box directly.
				for _, mode := range []listMode{listBrowse, listFilter, listRename} {
					name := fmt.Sprintf("painted=%v/%dx%d/mode=%d", tm != nil, width, height, mode)
					t.Run(name, func(t *testing.T) {
						m := NewList(nil, keymap.Default(), tm)
						m.width, m.height = width, height
						m.applyRefresh(items)
						panelRows := 0
						switch mode {
						case listRename:
							m.setMode(listRename)
							panelRows = m.ta.Height() + 2
						case listFilter:
							m.filter = "plan"
							m.setMode(listFilter)
						case listBrowse:
						}

						lines := strings.Split(ansi.Strip(m.View().Content), "\n")
						if len(lines) > height {
							t.Fatalf("total lines = %d, want <= %d", len(lines), height)
						}
						content := 0
						for _, l := range lines {
							if strings.Contains(l, "○") || strings.Contains(l, " more") {
								content++
							}
						}
						want := wantBrowseChrome
						if mode == listRename {
							want = wantPanelChrome
						}
						if got := height - content - panelRows; got != want {
							t.Fatalf("chrome = %d (height %d - %d content rows - %d panel rows), want %d",
								got, height, content, panelRows, want)
						}
					})
				}
			}
		}
	}
}

// TestYourPlansTakesWhatRecentlyOpenedLeaves drives the layout through the real
// builder at 80x24, one row per shape it has to answer for: Recently opened is
// seated first, and "Your plans" draws into every row of the budget left after
// it -- all of it when Recently opened is not drawn -- showing every plan it can
// and a +N more for the rest.
//
// The rows are counted off m.rows, but the numbers are the SCREEN's: every
// row here is drawn, since browse never builds more than viewHeight.
func TestYourPlansTakesWhatRecentlyOpenedLeaves(t *testing.T) {
	// At 80x24: rowBudget = 24 - listBrowseChrome.
	const wantBudget = 15
	for _, tc := range []struct {
		name                 string
		plans, recent        int
		wantRecent, wantMine int
	}{
		{"twenty plans, nothing opened", 20, 0, 0, 15},
		{"three plans, nothing opened", 3, 0, 0, 3},
		{"no plans at all", 0, 0, 0, 1},
		// Recently opened's chrome (3) and its five rows come off the 15 first.
		{"twenty plans, five opened", 20, 5, 5, 7},
		{"three plans, two opened", 3, 2, 2, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := listSectionItems("Mine", tc.plans)
			var entries []recent.Entry
			for _, it := range items[:tc.recent] {
				entries = append(entries, recent.Entry{PlanID: it.plan.ID})
			}
			m := newRecentList(t, items, entries...)
			cur, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			m = cur.(*ListModel)
			if got := m.rowBudget(); got != wantBudget {
				t.Fatalf("rowBudget() = %d, want %d (height - %d)", got, wantBudget, listBrowseChrome)
			}

			recentRows := sectionContentRows(m, listRecentSectionTitle)
			mineRows := sectionContentRows(m, listMineSectionTitle)
			if len(recentRows) != tc.wantRecent || len(mineRows) != tc.wantMine {
				t.Fatalf("sections got %d/%d rows, want %d/%d", len(recentRows), len(mineRows), tc.wantRecent, tc.wantMine)
			}
			// Every plan "Your plans" could show, it shows: it is never handed
			// rows it leaves blank while plans sit behind a +N more.
			shown := countKind(mineRows, rowPlan)
			if tc.plans <= len(mineRows) && shown != tc.plans {
				t.Fatalf("Your plans shows %d of its %d plans in %d rows -- a section that fits must show all of them",
					shown, tc.plans, len(mineRows))
			}
			if tc.plans > len(mineRows) && countKind(mineRows, rowMore) != 1 {
				t.Fatalf("Your plans holds %d plans in %d rows with no +N more row", tc.plans, len(mineRows))
			}
		})
	}
}

// TestPlusNMoreCountsThePlansItsOwnRowDisplaced pins the off-by-one in the
// tail's count: the tail row CONSUMES one of the section's allocated rows (so
// the chrome never becomes conditional and the layout never jumps), which
// makes the count n - (r - 1) and not n - r.
//
// 10 plans in a 3-row section is the worked example. The fixture that
// makes it fail is the count itself: "+7 more" is the answer if the tail is
// assumed to sit beside the third plan rather than instead of it, and "+10
// more" if it is assumed to count everything.
func TestPlusNMoreCountsThePlansItsOwnRowDisplaced(t *testing.T) {
	for _, tc := range []struct {
		name          string
		height, plans int
		wantRows      int
		wantShown     int
		wantMore      string
	}{
		{name: "ten plans in three rows", height: listBrowseChrome + 3, plans: 10, wantRows: 3, wantShown: 2, wantMore: "+8 more"},
		{name: "ten plans in one row", height: listMinHeight, plans: 10, wantRows: 1, wantShown: 0, wantMore: "+10 more"},
		{name: "three plans in three rows shows no tail", height: listBrowseChrome + 3, plans: 3, wantRows: 3, wantShown: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewList(nil, keymap.Default(), nil)
			m.width, m.height = 80, tc.height
			m.applyRefresh(listSectionItems("Mine", tc.plans))

			rows := sectionContentRows(m, listMineSectionTitle)
			if len(rows) != tc.wantRows {
				t.Fatalf("%q got %d rows, want %d", listMineSectionTitle, len(rows), tc.wantRows)
			}
			if got := countKind(rows, rowPlan); got != tc.wantShown {
				t.Fatalf("section shows %d plans, want %d -- the +N more row consumes one of the %d allocated rows",
					got, tc.wantShown, tc.wantRows)
			}
			view := ansi.Strip(m.View().Content)
			if tc.wantMore == "" {
				if countKind(rows, rowMore) != 0 {
					t.Fatalf("section holding all %d of its plans still drew a +N more row", tc.plans)
				}
				if strings.Contains(view, " more") {
					t.Fatalf("view carries a +N more row it has no tail for:\n%s", view)
				}
				return
			}
			if !strings.Contains(view, tc.wantMore) {
				t.Fatalf("view does not carry %q -- the count is n - (r - 1):\n%s", tc.wantMore, view)
			}
		})
	}
}

// TestListResizeReallocatesTheSections: the layout's budget is read off the
// terminal's height, so tea.WindowSizeMsg owes the body a rebuild, not a bare
// clampScroll. Without it a terminal grown from 12 rows to 24 keeps showing
// two plans and a "+8 more" over twelve blank rows until some unrelated
// refresh happens to land.
func TestListResizeReallocatesTheSections(t *testing.T) {
	m := NewList(nil, keymap.Default(), nil)
	// The height whose budget leaves exactly two plan rows and the +N more.
	m.width, m.height = 80, listBrowseChrome+3
	m.applyRefresh(listSectionItems("Mine", 10))
	if got := countKind(sectionContentRows(m, listMineSectionTitle), rowPlan); got != 2 {
		t.Fatalf("test setup: %d plan rows at height %d, want 2", got, m.height)
	}
	// The cursor is on the second plan; the resize must keep it on that same
	// plan, not on that same row index.
	m = pressList(m, "j")
	before, ok := m.selectedPlan()
	if !ok {
		t.Fatal("test setup: cursor is not on a plan")
	}

	cur, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = cur.(*ListModel)
	if got := countKind(sectionContentRows(m, listMineSectionTitle), rowPlan); got != 10 {
		t.Fatalf("after growing to 24 rows the section shows %d plans, want all 10", got)
	}
	if strings.Contains(ansi.Strip(m.View().Content), " more") {
		t.Fatal("a section with room for every plan still draws a +N more row after the resize")
	}
	after, ok := m.selectedPlan()
	if !ok || after.plan.ID != before.plan.ID {
		t.Fatalf("cursor after the resize is on %q, want the plan it was on (%q)", after.plan.ID, before.plan.ID)
	}
}

// TestListRefusesBelowItsMinimumSize is the gate. Below either minimum the
// list refuses rather than degrades -- a screen that small does not let the
// user do anything and requires them to open to a satisfactory size -- and the
// refusal takes no key but quit.
//
// BOTH MINIMUMS ARE DERIVED, and the first two assertions are what makes that
// checkable rather than asserted: the height is browse chrome plus the section's
// floor, and the width is the row layout's own fixed columns plus its declared
// minimum title width. The third is what the width half is FOR --
// listMinWidth > 12 makes the m.width <= 12 painted-overflow residual (carried
// open in confirmLines, listRename's reservation and panelViewPainted)
// unreachable.
func TestListRefusesBelowItsMinimumSize(t *testing.T) {
	if listMinHeight != listBrowseChrome+listSectionFloor {
		t.Fatalf("listMinHeight = %d, want chrome %d plus the section's floor %d", listMinHeight, listBrowseChrome, listSectionFloor)
	}
	if listMinWidth != listRowFixedWidth+listMinTitleWidth {
		t.Fatalf("listMinWidth = %d, want the fixed columns (%d) plus the minimum title width (%d)",
			listMinWidth, listRowFixedWidth, listMinTitleWidth)
	}
	if listMinWidth <= 12 {
		t.Fatalf("listMinWidth = %d, want more than 12 -- the gate exists to make the m.width <= 12 residual unreachable", listMinWidth)
	}

	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name          string
		width, height int
		gated         bool
	}{
		{"one row shorter than the minimum", listMinWidth, listMinHeight - 1, true},
		{"one column narrower than the minimum", listMinWidth - 1, listMinHeight, true},
		{"the residual width", 12, 24, true},
		{"a 1x1 terminal", 1, 1, true},
		{"exactly the minimum", listMinWidth, listMinHeight, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, tm := range []*theme.Theme{nil, th} {
				m := NewList(nil, keymap.Default(), tm)
				m.applyRefresh(listSectionItems("Mine", 5))
				cur, _ := m.Update(tea.WindowSizeMsg{Width: tc.width, Height: tc.height})
				m = cur.(*ListModel)

				view := m.View().Content
				lines := strings.Split(view, "\n")
				if len(lines) > max(tc.height, 1) {
					t.Fatalf("painted=%v: %d lines at height %d", tm != nil, len(lines), tc.height)
				}
				stripped := ansi.Strip(view)
				// Every line of both screens, with nothing exempt: the
				// refusal has no bars, and the list's own two bars are clipped
				// to m.width rather than carved out of this check.
				for i, l := range lines {
					if w := ansi.StringWidth(ansi.Strip(l)); w > tc.width {
						t.Fatalf("painted=%v: line %d is %d cells wide at width %d: %q", tm != nil, i, w, tc.width, ansi.Strip(l))
					}
				}
				// A refused screen draws its message and NOTHING else: no
				// masthead, no bands, no help bar naming keys it has stopped
				// taking.
				drewList := strings.Contains(stripped, listMineSectionTitle) || strings.Contains(stripped, "enter open")
				if drewList == tc.gated {
					t.Fatalf("painted=%v: list chrome drawn = %v, want %v:\n%s", tm != nil, drewList, !tc.gated, stripped)
				}
				if !tc.gated {
					continue
				}
				// Trimmed and emptied of filler: every line is padded to the
				// terminal's width and the rows beneath the refusal are blank
				// canvas. What this pins is the WORDS.
				var drawn []string
				for _, l := range strings.Split(stripped, "\n") {
					if t := strings.TrimRight(l, " "); t != "" {
						drawn = append(drawn, t)
					}
				}
				if got, want := strings.Join(drawn, "\n"), strings.Join(m.gateLines(), "\n"); got != want {
					t.Fatalf("refused view = %q, want exactly the refusal (%q)", got, want)
				}
			}
		})
	}

	// What the refusal actually says, measured at a width wide enough that
	// none of it wraps: both minimums, the size on offer, and the one key
	// that still works.
	wide := NewList(nil, keymap.Default(), nil)
	cur, _ := wide.Update(tea.WindowSizeMsg{Width: 100, Height: listMinHeight - 1})
	wide = cur.(*ListModel)
	said := ansi.Strip(wide.View().Content)
	for _, want := range []string{
		fmt.Sprintf("%d×%d", listMinWidth, listMinHeight),
		fmt.Sprintf("%d×%d", 100, listMinHeight-1),
		"quit",
	} {
		if !strings.Contains(said, want) {
			t.Fatalf("the refusal does not name %q:\n%s", want, said)
		}
	}

	// No key but quit, driven through Update: the gate is ahead of mode
	// dispatch, so j/k/g/G move nothing, e/d open no panel, and enter
	// dispatches nothing at all.
	gated := func() *ListModel {
		m := NewList(nil, keymap.Default(), nil)
		m.applyRefresh(listSectionItems("Mine", 5))
		cur, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: listMinHeight - 1})
		return cur.(*ListModel)
	}
	for _, key := range []string{"j", "k", "g", "G", "e", "d"} {
		m := gated()
		before := m.cursor
		cur, cmd := m.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
		m = cur.(*ListModel)
		if cmd != nil {
			t.Fatalf("%q on a refused screen dispatched a cmd", key)
		}
		if m.cursor != before || m.mode != listBrowse {
			t.Fatalf("%q on a refused screen moved the cursor to %d / mode %v", key, m.cursor, m.mode)
		}
	}
	if _, cmd := gated().Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("enter on a refused screen dispatched a cmd")
	}
	for _, quit := range []tea.KeyPressMsg{{Code: 'q', Text: "q"}, {Code: 'c', Mod: tea.ModCtrl}} {
		_, cmd := gated().Update(quit)
		if cmd == nil {
			t.Fatalf("%v on a refused screen did not quit", quit)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("%v on a refused screen dispatched %T, want tea.QuitMsg", quit, cmd())
		}
	}
}

// TestModeSwitchKeepsTheCursorOnScreen: browse row indices and flat
// (panel-mode) row indices diverge WITHOUT BOUND, because browse draws Recently
// opened above "Your plans" and the flat body draws every plan in order. A plan
// opened recently and ninetieth by activity is browse row 2 and flat row 90,
// and a switch that only clamps scroll by the row count leaves the cursor off
// screen behind a panel editing a plan the user cannot see.
//
// The band assertion is the one that matters and is deliberately made against
// the RENDERED screen: a cursor index inside [scroll, scroll+viewHeight) is
// the arithmetic, and the band actually being drawn is the claim.
func TestModeSwitchKeepsTheCursorOnScreen(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		mode listMode
	}{
		{"rename", "e", listRename},
		{"confirm-delete", "d", listConfirmDelete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := listSectionItems("Mine", 100)
			m := newRecentList(t, items, recent.Entry{PlanID: items[90].plan.ID})
			cur, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			m = cur.(*ListModel)
			before, ok := m.selectedPlan()
			if !ok || before.plan.ID != items[90].plan.ID {
				t.Fatalf("test setup: cursor is on (%q, %v), want the recently opened plan", before.plan.ID, ok)
			}
			if m.cursor >= 60 {
				t.Fatalf("test setup: browse cursor = %d -- the fixture must make the browse and flat indices diverge", m.cursor)
			}

			assertVisible := func(stage string) {
				t.Helper()
				if m.cursor < m.scroll || m.cursor >= m.scroll+m.viewHeight() {
					t.Fatalf("%s: cursor %d is outside the drawn body [%d, %d)", stage, m.cursor, m.scroll, m.scroll+m.viewHeight())
				}
				body := strings.Split(ansi.Strip(m.View().Content), "\n")[m.mastheadHeight():]
				band := -1
				for i := 0; i < m.viewHeight() && i < len(body); i++ {
					if strings.Contains(body[i], "▌") {
						band = i
					}
				}
				if band < 0 {
					t.Fatalf("%s: no cursor band anywhere on screen (cursor %d, scroll %d, viewHeight %d)",
						stage, m.cursor, m.scroll, m.viewHeight())
				}
				if want := m.cursor - m.scroll; band != want {
					t.Fatalf("%s: cursor band on body line %d, want %d", stage, band, want)
				}
			}
			assertVisible("browse")

			m = pressList(m, tc.key)
			if m.mode != tc.mode {
				t.Fatalf("%q did not open the panel: mode = %v", tc.key, m.mode)
			}
			after, ok := m.selectedPlan()
			if !ok || after.plan.ID != before.plan.ID {
				t.Fatalf("the panel is over (%q, %v), want the plan the cursor was on (%q)", after.plan.ID, ok, before.plan.ID)
			}
			assertVisible("panel")

			// And back: esc/n returns to browse, where the row set is short
			// again and the viewport must return with it.
			m = pressList(m, "n")
			if m.mode != listBrowse {
				m = pressList(m, "\x1b")
			}
			if m.mode == listBrowse {
				assertVisible("back in browse")
				if m.scroll != 0 {
					t.Fatalf("scroll after returning to browse = %d, want 0 -- the collapsed body has nothing to scroll", m.scroll)
				}
			}
		})
	}
}

// TestGateDoesNotDiscardTypedText is the minimum-size gate's ONE exception,
// over every mode that holds text a human typed: rename's draft and the
// filter's query (typesLiterals). Above the gate q is a CHARACTER in both --
// measured for the textarea, structural for the filter, which never consults
// the keymap -- so a gate that quit on it would give one key two opposite
// meanings on either side of a resize, and the losing one silently discards
// text the user typed and cannot currently see.
//
// The rows are not copies of one another: the two reach the same exception
// through different machinery. THE SET IS THE PREDICATE'S, so a mode added to
// typesLiterals without a row here is the one gap this test cannot close on
// its own -- which is why each row's own `open` drives the real door rather
// than assigning m.mode.
func TestGateDoesNotDiscardTypedText(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  listMode
		open  func(*ListModel) *ListModel
		draft func(*ListModel) string
	}{
		{
			name:  "a rename draft",
			mode:  listRename,
			open:  func(m *ListModel) *ListModel { return pressList(m, "e") },
			draft: func(m *ListModel) string { return m.ta.Value() },
		},
		{
			name:  "a filter query",
			mode:  listFilter,
			open:  func(m *ListModel) *ListModel { return pressList(m, "/", "m", "a", "r") },
			draft: func(m *ListModel) string { return m.filter },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			open := func(w, h int) *ListModel {
				m := NewList(nil, keymap.Default(), nil)
				m.width, m.height = 80, 24
				m.applyRefresh(listSectionItems("Mine", 3))
				m = tc.open(m)
				if m.mode != tc.mode {
					t.Fatalf("test setup: mode = %v, want %v", m.mode, tc.mode)
				}
				cur, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
				return cur.(*ListModel)
			}

			// Above the gate: q is typed into the text and nothing quits. This
			// is the measurement the exception rests on, so it is pinned rather
			// than asserted in a comment.
			above := open(80, 24)
			draft := tc.draft(above)
			above = pressList(above, "q")
			if got := tc.draft(above); got != draft+"q" {
				t.Fatalf("above the gate, q produced %q, want %q -- the exception below rests on q being a character here", got, draft+"q")
			}

			// Below it: q is refused and the text stands.
			below := open(80, listMinHeight-1)
			if below.fitsMinimum() {
				t.Fatal("test setup: the terminal must be below the gate")
			}
			cur, cmd := below.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
			below = cur.(*ListModel)
			if cmd != nil {
				t.Fatalf("q below the gate dispatched %T -- it would end the process and discard the text", cmd())
			}
			if tc.draft(below) != draft || below.mode != tc.mode {
				t.Fatalf("q below the gate changed the text to %q / mode %v, want %q / %v", tc.draft(below), below.mode, draft, tc.mode)
			}
			// The refusal names the key that actually works here, rather than
			// the one it just refused.
			view := ansi.Strip(below.View().Content)
			if !strings.Contains(view, "ctrl+c") {
				t.Fatalf("the refusal over typed text does not name ctrl+c:\n%s", view)
			}
			if _, cmd := below.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); cmd == nil {
				t.Fatal("ctrl+c below the gate did not quit")
			}
		})
	}

	// Every other mode keeps the rebindable quit key, and says so.
	browse := NewList(nil, keymap.Default(), nil)
	cur, _ := browse.Update(tea.WindowSizeMsg{Width: 80, Height: listMinHeight - 1})
	browse = cur.(*ListModel)
	if got := browse.gateQuitKey(); got != findKey(browse.km, keymap.ActQuit) {
		t.Fatalf("gateQuitKey() in browse = %q, want the ActQuit binding", got)
	}
}

// ---------------------------------------------------------------------------
// +N more and expand.
// ---------------------------------------------------------------------------

// freshestFirstPlans builds n plans, freshest first so the derived order
// matches the order they are handed over in: plan i's lastActivity descends
// with i.
func freshestFirstPlans(n int) []domain.Plan {
	out := make([]domain.Plan, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, domain.Plan{
			ID:             domain.PlanID(fmt.Sprintf("l_p%02d", i)),
			Title:          fmt.Sprintf("Plan %02d", i),
			LastActivityAt: listFixtureEpoch.Add(-time.Duration(i) * time.Minute),
		})
	}
	return out
}

// pressListKey presses one key and hands back the cmd it produced, which
// pressList (which drops it) cannot do -- many assertions below are about
// whether a cmd exists at all. It also knows the NAMED keys the list reads as
// themselves: esc (leaves expand, and clears a filter), enter, backspace
// (deletes a character of a filter query), and pgdown/pgup. Each is built with
// an EMPTY Text, the way a terminal delivers it, since Text being empty is
// exactly what updateFilter tests to tell a named key from a typed character.
func pressListKey(m *ListModel, key string) (*ListModel, tea.Cmd) {
	var msg tea.KeyPressMsg
	switch key {
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		msg = tea.KeyPressMsg{Code: tea.KeyEsc}
	case "backspace":
		msg = tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "pgdown":
		msg = tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "pgup":
		msg = tea.KeyPressMsg{Code: tea.KeyPgUp}
	default:
		msg = tea.KeyPressMsg{Code: rune(key[0]), Text: key}
	}
	cur, cmd := m.Update(msg)
	return cur.(*ListModel), cmd
}

// sectionMoreRow finds a section's +N more row.
func sectionMoreRow(m *ListModel, section listSection) (int, row, bool) {
	for i, r := range m.rows {
		if r.kind == rowMore && r.section == section {
			return i, r, true
		}
	}
	return 0, row{}, false
}

// TestPlusNMoreIsSelectableAndEnterExpandsTheSection is driven from the
// keyboard rather than from the fields: the cursor reaches "Your plans"' +N more
// row, enter on it expands the section, and esc is how it is left.
func TestPlusNMoreIsSelectableAndEnterExpandsTheSection(t *testing.T) {
	m := NewList(nil, keymap.Default(), nil)
	m.width, m.height = 80, 24
	m.applyRefresh(listSectionItems("Mine", 40))

	idx, before, ok := sectionMoreRow(m, sectionMine)
	if !ok {
		t.Fatalf("fixture assumption broken: %q has no +N more row", listMineSectionTitle)
	}
	if !before.kind.selectable() {
		t.Fatal("a +N more row is not selectable, so the cursor can never reach it to press enter")
	}
	m.cursor = idx

	m, cmd := pressListKey(m, "enter")
	if cmd != nil {
		t.Fatalf("enter on a +N more dispatched %T, want no cmd -- expanding opens no plan", cmd())
	}
	if m.mode != listExpanded {
		t.Fatalf("enter left mode = %v; want expanded", m.mode)
	}
	opened, ok := m.selectedPlan()
	if !ok {
		t.Fatal("expanding left the cursor off a plan")
	}
	expandedOn := opened.plan.ID

	// The expanded section holds every plan.
	expanded := sectionContentRows(m, listMineSectionTitle)
	if got := countKind(expanded, rowPlan); got != 40 {
		t.Fatalf("the expanded %q holds %d plan rows, want all 40 of its plans", listMineSectionTitle, got)
	}

	// esc leaves, and the cursor is restored the way leaveExpanded rules: to
	// the same PLAN when the collapsed layout still has a row for it. Expanded
	// opens on the section's first plan, which the collapsed layout always
	// shows.
	m, cmd = pressListKey(m, "esc")
	if cmd != nil {
		t.Fatalf("esc out of expand dispatched %T, want no cmd", cmd())
	}
	if m.mode != listBrowse {
		t.Fatalf("esc left mode = %v, want browse", m.mode)
	}
	if it, ok := m.selectedPlan(); !ok || it.plan.ID != expandedOn {
		t.Fatalf("after esc the cursor is on %q, want the plan it was on (%q)", it.plan.ID, expandedOn)
	}

	// The case the rule actually exists for: deep in the section, on a plan the
	// collapsed layout has no room for. Keeping the row INDEX there lands
	// somewhere arbitrary in a body that just changed shape, so the cursor goes
	// back to the +N more row enter was pressed on.
	m.cursor = idx
	m, _ = pressListKey(m, "enter")
	m, _ = pressListKey(m, "G")
	deep, ok := m.selectedPlan()
	if !ok {
		t.Fatal("G in expanded left the cursor off a plan")
	}
	m, _ = pressListKey(m, "esc")
	if _, stillShown := m.planRowIndex(deep.plan.ID); stillShown {
		t.Fatalf("fixture assumption broken: %q is still a row in the collapsed layout", deep.plan.ID)
	}
	if r, ok := m.cursorRow(); !ok || r.kind != rowMore {
		t.Fatalf("after esc from deep in the section the cursor is on %+v, want the +N more row -- the row enter was pressed on", r)
	}
}

// TestLeavingExpandedNeverKeepsAnotherPlan is leaveExpanded's landing for a
// plan partway down the section: the collapsed body does not line up row for
// row with the expanded one -- Recently opened's rows sit above it in browse and
// nowhere in expanded -- so the row index the cursor falls back to can hold a
// different plan. Only a cursor back on its own plan stays; any other goes to
// the +N more row enter was pressed on.
func TestLeavingExpandedNeverKeepsAnotherPlan(t *testing.T) {
	items := listSectionItems("l_", 40)
	var entries []recent.Entry
	for _, it := range items[20:25] {
		entries = append(entries, recent.Entry{PlanID: it.plan.ID})
	}
	m := newRecentList(t, items, entries...)
	cur, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = cur.(*ListModel)

	idx, _, ok := sectionMoreRow(m, sectionMine)
	if !ok {
		t.Fatal("fixture assumption broken: Your plans has no +N more row")
	}
	m.cursor = idx
	m, _ = pressListKey(m, "enter")
	const from = domain.PlanID("l_009")
	i, ok := m.planRowIndex(from)
	if m.mode != listExpanded || !ok {
		t.Fatalf("expanded = %v, %s drawn = %v; want %s drawn in the expanded section", m.mode == listExpanded, from, ok, from)
	}
	m.cursor = i
	m, _ = pressListKey(m, "esc")
	if r, _ := m.cursorRow(); r.kind != rowMore || r.section != sectionMine {
		t.Fatalf("after esc from %s the cursor is on %v %v %q, want Your plans' +N more", from, r.kind, r.section, r.item.plan.ID)
	}
}

// TestExpandedKeepsBrowsesGeometryAndNeverOverflows checks against the real
// renderer that browse and expanded reserve the identical top block and the
// identical body budget, and that the drawn screen never exceeds the terminal
// at any scroll offset the viewport can reach.
//
// THE SECOND HALF IS THE ONE THAT COULD REGRESS. Expanded is the first mode
// whose row slice is allowed to run past viewHeight -- in the collapsed state
// browse never builds more rows than the viewport and m.scroll is pinned at 0 --
// so it is the first mode where a body loop that ignored the viewport would
// paint more rows than the terminal has.
func TestExpandedKeepsBrowsesGeometryAndNeverOverflows(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	for _, tm := range []*theme.Theme{nil, th} {
		for _, width := range []int{listMinWidth, 40, 80, 120} {
			for height := listMinHeight; height <= 30; height++ {
				t.Run(fmt.Sprintf("painted=%v/%dx%d", tm != nil, width, height), func(t *testing.T) {
					m := NewList(nil, keymap.Default(), tm)
					m.width, m.height = width, height
					m.applyRefresh(listSectionItems("Mine", 120))
					browseHead, browseBudget := m.mastheadHeight(), m.rowBudget()

					idx, _, ok := sectionMoreRow(m, sectionMine)
					if !ok {
						t.Fatalf("fixture assumption broken at %dx%d: no +N more to expand", width, height)
					}
					m.cursor = idx
					m, _ = pressListKey(m, "enter")

					if m.mastheadHeight() != browseHead || m.rowBudget() != browseBudget {
						t.Fatalf("expanded reserves masthead %d / budget %d, want browse's %d / %d -- the chrome is the same in both",
							m.mastheadHeight(), m.rowBudget(), browseHead, browseBudget)
					}
					// Every reachable scroll offset, including the last.
					for _, scroll := range []int{0, 1, len(m.rows) / 2, len(m.rows)} {
						m.scroll = scroll
						m.clampScroll()
						if lines := strings.Split(ansi.Strip(m.View().Content), "\n"); len(lines) > height {
							t.Fatalf("expanded at scroll %d draws %d lines, want <= %d", m.scroll, len(lines), height)
						}
					}
				})
			}
		}
	}
}

// TestPageDownAndPageUpMoveByAPageAndReseatTheCursor: pgdn/pgup move the
// expanded viewport by a full page -- pageSize(), viewHeight()-2 floored at 1,
// mirroring Model.pageScroll rather than restating it -- and re-seat the
// cursor onto a row it may actually rest on afterward.
//
// THE RESEAT IS CHECKED EXACTLY ON THE WAY BACK UP: pgup clamps scroll back to 0,
// onto the section's own two rows of chrome, so the cursor landing on the first
// plan requires firstSelectableRowAtOrBelow's skip-forward to have run.
func TestPageDownAndPageUpMoveByAPageAndReseatTheCursor(t *testing.T) {
	m := NewList(nil, keymap.Default(), nil)
	m.width, m.height = 80, 24
	m.applyRefresh(listSectionItems("Mine", 200))
	m.setMode(listExpanded)
	first := m.firstSelectableRow()
	if first != listSectionChrome {
		t.Fatalf("fixture assumption broken: firstSelectableRow() = %d, want %d -- the expanded section's own head", first, listSectionChrome)
	}
	m.cursor, m.scroll = first, 0

	wantPage := m.viewHeight() - 2
	if wantPage != m.pageSize() {
		t.Fatalf("pageSize() = %d, want viewHeight()-2 = %d", m.pageSize(), wantPage)
	}

	m, cmd := pressListKey(m, "pgdown")
	if cmd != nil {
		t.Fatalf("pgdn dispatched %T, want nothing -- paging only moves the viewport", cmd())
	}
	if m.scroll != wantPage {
		t.Fatalf("scroll after pgdn = %d, want exactly one page (%d) -- 200 plans leave room, so this must not have clamped", m.scroll, wantPage)
	}
	if got := m.rows[m.cursor].kind; !got.selectable() {
		t.Fatalf("cursor after pgdn rests on a %v row, not selectable", got)
	}
	if m.cursor != m.scroll {
		t.Fatalf("cursor after pgdn = %d, want %d (m.scroll itself, a plan row) -- every row past the head is one", m.cursor, m.scroll)
	}

	m, cmd = pressListKey(m, "pgup")
	if cmd != nil {
		t.Fatalf("pgup dispatched %T, want nothing", cmd())
	}
	if m.scroll != 0 {
		t.Fatalf("scroll after pgdn then pgup = %d, want back to 0 -- one page down and one page up cancel", m.scroll)
	}
	if m.cursor != first {
		t.Fatalf("cursor after pgup = %d, want %d -- the section's first plan, past the chrome scroll 0 lands on", m.cursor, first)
	}
}

// TestPageScrollsChromeRunNeverReachesTheMinimumViewport pins the inequality
// pageScroll depends on: the longest run of consecutive chrome a legal scroll
// can land on is listSectionChrome (the expanded section's own band and
// column-label row), and it must stay strictly under the smallest viewHeight()
// the minimum-size gate ever presents -- otherwise firstSelectableRowAtOrBelow's
// forward scan could run past the viewport clampScroll just set, landing the
// cursor off-screen. Driven at the gate's own floor (m.height = listMinHeight)
// rather than restated in constants, so a change to mastheadHeight or
// viewHeight's own arithmetic is caught here too.
func TestPageScrollsChromeRunNeverReachesTheMinimumViewport(t *testing.T) {
	m := NewList(nil, keymap.Default(), nil)
	m.width, m.height = listMinWidth, listMinHeight
	m.setMode(listExpanded)

	if got := m.viewHeight(); got != 3 {
		t.Fatalf("fixture assumption broken: viewHeight() at listMinHeight = %d, want 3 -- the derivation "+
			"this test pins assumes that reduction", got)
	}
	if listSectionChrome >= m.viewHeight() {
		t.Fatalf("listSectionChrome (%d) >= the minimum viewHeight() (%d): pageScroll's cursor containment "+
			"assumes the longest chrome run stays strictly under the smallest viewport the gate ever presents",
			listSectionChrome, m.viewHeight())
	}
}

// TestFirstSelectableRowAtOrBelowFallsBackWhenNoRowRemains drives
// firstSelectableRowAtOrBelow's fallback DIRECTLY. A route to it stays open,
// since a landed msgListRefreshed applies regardless of mode and can leave the
// expanded section with no selectable row of its own at all. So this drives the
// function's CONTRACT rather than a branch proven unreachable: it must not break
// for a caller that hands it a scroll past every selectable row.
//
// listChromeRows ends in exactly the shape that matters: a +N more row
// (index 4, the last selectable one) followed by three rows of pure chrome
// and nothing else. A scroll anywhere past index 4 hands the forward scan
// nothing to find, so the fallback must run and answer lastSelectableRow's
// own 4 -- not fall off the end silently.
func TestFirstSelectableRowAtOrBelowFallsBackWhenNoRowRemains(t *testing.T) {
	m := NewList(nil, keymap.Default(), nil)
	seedRows(t, m, listChromeRows(), 0)

	if want := m.lastSelectableRow(); want != 4 {
		t.Fatalf("fixture assumption broken: lastSelectableRow() = %d, want 4 (the +N more row) -- "+
			"listChromeRows must end in chrome for this test to drive the fallback it claims to", want)
	}
	for _, scroll := range []int{5, 6, 7, 8, 100} {
		if got := m.firstSelectableRowAtOrBelow(scroll); got != 4 {
			t.Fatalf("firstSelectableRowAtOrBelow(%d) = %d, want 4 (lastSelectableRow's own fallback answer)", scroll, got)
		}
	}
}

// TestPageSizeIsFlooredAtOne pins pageSize()'s floor, mirrored verbatim from
// Model.pageSize (app/model.go): viewHeight()-2, floored at 1, so a page
// jump on the smallest possible viewport still moves by one row rather than
// stalling at zero or reversing at a negative page.
//
// UNREACHABLE THROUGH THE GATE -- fitsMinimum refuses every key below
// listMinHeight, and viewHeight() (3 at that floor) never drops this low
// behind it -- so this drives the method directly rather than through a
// keystroke. The floor is Model.pageScroll's own contract and this mirrors it
// rather than assuming the gate makes it moot: dropping it would leave pgdn
// silently doing nothing, or moving backward, on any embedder that skips the
// gate.
func TestPageSizeIsFlooredAtOne(t *testing.T) {
	for h := 1; h <= 8; h++ {
		t.Run(fmt.Sprintf("height=%d", h), func(t *testing.T) {
			m := NewList(nil, keymap.Default(), nil)
			m.width, m.height = 80, h
			if got := m.pageSize(); got < 1 {
				t.Fatalf("pageSize() at height %d (viewHeight %d) = %d, want >= 1 (the floor)", h, m.viewHeight(), got)
			}
		})
	}
	// The floor's OWN engagement, not merely a bound every value happens to
	// satisfy: at height 9, viewHeight() is 2 (9-2-listBrowseMastheadHeight,
	// unfloored -- viewHeight's own floor only bites below that), so the
	// unfloored arithmetic viewHeight()-2 is exactly 0. Without the floor
	// pageSize() would answer 0 here, not >= 1.
	m := NewList(nil, keymap.Default(), nil)
	m.width, m.height = 80, 9
	if vh := m.viewHeight(); vh != 2 {
		t.Fatalf("fixture assumption broken: viewHeight() at height 9 = %d, want 2 -- the floor case above proves nothing without this", vh)
	}
	if got := m.pageSize(); got != 1 {
		t.Fatalf("pageSize() at height 9 = %d, want 1 -- viewHeight()-2 is 0 there, and the floor is what turns 0 into 1", got)
	}
}

// TestARecoveredFailureRetractsItsOwnStatusSentence is about a PAIR rather
// than about either half.
//
// A failed load writes its sentence in the same breath as the flag that says
// it failed, and applies no items, so the sentence describes the rows still on
// screen. The next load that succeeds replaces exactly those rows, so it is the
// sentence's clearer -- without one, a recovered failure left the bar reporting
// it for the life of the process. m.err is not the partner flag it looks like:
// nothing in this file reads or clears it.
//
// THE LAST ROW IS THE OVER-CORRECTION, and it is why the fix retracts a
// remembered sentence rather than blanking the bar: a write's confirmation
// ("plan deleted") is followed immediately by the refresh that write triggers
// (handleListActionDone), so a clearer that cleared unconditionally would
// delete the confirmation before any human read it. That is the same property
// app_test.go's "unclobbered" cases pin for the review model's own bar.
func TestARecoveredFailureRetractsItsOwnStatusSentence(t *testing.T) {
	const stale = "everything is on fire"
	// failLoad builds a list whose last load failed, leaving both halves of the
	// pair set, over a service that answers again from here on.
	failLoad := func(t *testing.T) *ListModel {
		t.Helper()
		svc := &flakyListSvc{plans: freshestFirstPlans(5)}
		m := NewList(svc, keymap.Default(), nil)
		m.width, m.height = 80, 24
		m = drainList(t, m, m.Init())
		svc.err = errors.New(stale)
		m = drainList(t, m, m.refreshCmd())
		if !strings.Contains(m.status, stale) {
			t.Fatalf("the fixture did not set the failed load's status: %q", m.status)
		}
		svc.err = nil
		return m
	}

	for _, tc := range []struct {
		name     string
		recover  func(t *testing.T) *ListModel
		wantKeep string
	}{
		{
			name: "a later successful refresh retracts a failed refresh's error",
			recover: func(t *testing.T) *ListModel {
				m := failLoad(t)
				return drainList(t, m, m.refreshCmd())
			},
		},
		{
			name:     "a write's confirmation survives the refresh that write triggers",
			wantKeep: "plan deleted",
			recover: func(t *testing.T) *ListModel {
				m := failLoad(t)
				cur, cmd := m.Update(msgListActionDone{status: "plan deleted"})
				return drainList(t, cur.(*ListModel), cmd)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.recover(t)
			if strings.Contains(m.status, stale) {
				t.Fatalf("status = %q, want the recovered failure's sentence retracted", m.status)
			}
			if bar := m.statusBarText(120, m.styles.StatusBar); strings.Contains(bar, stale) {
				t.Fatalf("status bar = %q, want the recovered failure's sentence retracted", bar)
			}
			if tc.wantKeep != "" && !strings.Contains(m.status, tc.wantKeep) {
				t.Fatalf("status = %q, want it to still carry %q", m.status, tc.wantKeep)
			}
		})
	}
}

// flakyListSvc answers ListPlans with its plans, or with err while err is set:
// a store that fails one load and answers the next.
type flakyListSvc struct {
	client.PlanService
	plans []domain.Plan
	err   error
}

func (f *flakyListSvc) ListPlans(context.Context) ([]domain.Plan, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.plans, nil
}

// TestExpandedTakesOnlyTheKeysItNames is the wrong-hint rule in expanded: the
// help bar names enter, esc, pgdn/pgup and quit --
// updateExpanded's own accepted keys apart from plain cursor movement
// (j/k/g/G), which hintLine leaves unnamed the same way in browse. e, d, / and
// ctrl+r are not offered and do nothing -- entering a panel would draw the flat
// body and silently undo the expansion, and a reload would re-sort the body
// under the reader's scroll.
func TestExpandedTakesOnlyTheKeysItNames(t *testing.T) {
	m := newLoadedList(t, 9)
	m.setMode(listExpanded)
	m.cursor = m.firstSelectableRow()

	if got, want := m.helpBarLine(), m.expandedHint(); got != want {
		t.Fatalf("helpBarLine() in expanded = %q, want %q", got, want)
	}
	for _, dead := range []string{"rename", "delete"} {
		if strings.Contains(m.helpBarLine(), dead) {
			t.Fatalf("the expanded help bar advertises %q, which the mode does not take: %q", dead, m.helpBarLine())
		}
	}
	pageWant := fmt.Sprintf("%s/%s page", findKey(m.km, keymap.ActPageDown), findKey(m.km, keymap.ActPageUp))
	for _, want := range []string{"enter open", "esc collapse", pageWant} {
		if !strings.Contains(m.helpBarLine(), want) {
			t.Fatalf("the expanded help bar never says %q: %q", want, m.helpBarLine())
		}
	}

	// "/" joins e and d as a gesture this mode deliberately does not take: the
	// LIST'S filter has ONE door and it is browse, which is one esc away. An
	// already-active filter still narrows this body (expandedRows reads
	// filteredItems); what expand does not do is start a query.
	for _, key := range []string{"e", "d", "/"} {
		var cmd tea.Cmd
		m, cmd = pressListKey(m, key)
		if m.mode != listExpanded || cmd != nil {
			t.Fatalf("%q in expanded left mode = %v with cmd %v, want expanded and nothing", key, m.mode, cmd != nil)
		}
	}
	if m.filterActive() {
		t.Fatalf("%q in expanded started a filter query: %q", "/", m.filter)
	}
	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = cur.(*ListModel)
	if cmd != nil || m.mode != listExpanded {
		t.Fatalf("ctrl+r in expanded left mode = %v with cmd %v, want expanded and no reload", m.mode, cmd != nil)
	}

	// enter still opens the plan under the cursor.
	m.cursor = m.firstSelectableRow()
	_, cmd = pressListKey(m, "enter")
	if cmd == nil {
		t.Fatal("enter on a plan row in expanded opened nothing")
	}
	if _, ok := cmd().(msgOpenPlan); !ok {
		t.Fatalf("enter dispatched %T, want msgOpenPlan", cmd())
	}
}

// TestARefreshWhileExpandedIsDeferredRatherThanApplied pins the seam: a state
// change landing while the section is expanded would re-sort the body the
// reader is scrolling through, so it is deferred to the seam where expand is
// left, and applied there.
func TestARefreshWhileExpandedIsDeferredRatherThanApplied(t *testing.T) {
	svc := &flakyListSvc{plans: freshestFirstPlans(5)}
	m := NewList(svc, keymap.Default(), nil)
	m.width, m.height = 80, 24
	m = drainList(t, m, m.Init())
	m.setMode(listExpanded)
	m.cursor = m.firstSelectableRow()

	svc.plans = freshestFirstPlans(6)
	cur, cmd := m.Update(msgStateChanged{})
	m = cur.(*ListModel)
	if cmd != nil {
		t.Fatalf("a state change in expanded dispatched %T, want it deferred", cmd())
	}
	if !m.pendingRefresh {
		t.Fatal("the state change was neither applied nor remembered")
	}
	if len(m.items) != 5 {
		t.Fatalf("the deferred refresh still ran: %d loaded, want 5", len(m.items))
	}

	// esc is the seam: it collapses AND consumes the deferred refresh.
	m, cmd = pressListKey(m, "esc")
	if cmd == nil {
		t.Fatal("esc did not consume the deferred refresh")
	}
	m = drainList(t, m, cmd)
	if m.mode != listBrowse || m.pendingRefresh {
		t.Fatalf("after esc: mode = %v, pendingRefresh = %v; want browse and consumed", m.mode, m.pendingRefresh)
	}
	if len(m.items) != 6 {
		t.Fatalf("the refresh applied at the seam left %d loaded, want the store's 6", len(m.items))
	}
}

// TestStatusBarCountsLeftOriginRight: the counts on the left, THE SELECTED
// ROW'S ORIGIN on the right. It drives the composed line rather than the
// helper alone, at the two widths a real terminal takes and under both
// palettes, because the bar is also where an unclipped overflow lived.
//
// THE FOURTH CASE IS THE ONE THE BUDGET EXISTS FOR: a long status message on
// the left with a long path on the right. The obvious composition (append the
// origin, clip the line) drops the origin entirely there, which is the whole
// disclosure gone at exactly the moment the bar has something to say.
func TestStatusBarCountsLeftOriginRight(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	const path = "/Users/example/very/deeply/nested/tree/docs/plans/width-testing.md"

	newModel := func(tm *theme.Theme, width int) *ListModel {
		m := NewList(nil, keymap.Default(), tm)
		m.width, m.height = width, 24
		m.applyRefresh([]planItem{
			{plan: domain.Plan{ID: "l_a", Title: "Plan A", SourceHint: path}, open: 1, total: 2},
			{plan: domain.Plan{ID: "l_b", Title: "Plan B"}},
		})
		return m
	}
	statusLine := func(m *ListModel) string {
		lines := strings.Split(m.View().Content, "\n")
		return ansi.Strip(lines[len(lines)-2]) // ... then the help bar
	}

	for _, tm := range []*theme.Theme{nil, th} {
		t.Run(fmt.Sprintf("painted=%v", tm != nil), func(t *testing.T) {
			for _, width := range []int{80, 40} {
				m := newModel(tm, width)
				bar := statusLine(m)
				if w := ansi.StringWidth(bar); w != width {
					t.Fatalf("width %d: status bar is %d cells (%q)", width, w, bar)
				}
				// The count leads, and the origin ends the line. What
				// SURVIVES the origin's clip is its HEAD, so this can no
				// longer look for the filename -- it asks the question the
				// column now answers instead: is the thing on the right a
				// leading slice of the path?
				if !strings.HasPrefix(strings.TrimSpace(bar), "2 plans") {
					t.Fatalf("width %d: status bar does not lead with the counts: %q", width, bar)
				}
				if err := originEndsTheBar(bar, path); err != nil {
					t.Fatalf("width %d: %v: %q", width, err, bar)
				}
				if width == 40 && !strings.Contains(bar, "…") {
					t.Fatalf("width 40: a path that cannot fit must be clipped with a marker: %q", bar)
				}

				// A long status message may take the left side's cells and
				// never the origin's.
				m.status = strings.Repeat("something went wrong reading the plans; ", 4)
				if bar := statusLine(m); originEndsTheBar(bar, path) != nil {
					t.Fatalf("width %d: a long status truncated the origin away: %q", width, bar)
				}
			}
		})
	}

	// Nothing selected is nothing shown: the cursor on a section band has no
	// origin, and the previous row's would be a lie about the current one.
	m := newModel(nil, 80)
	m.cursor = 0 // rebuildRows parks this on a plan; a band is row 0
	if _, onPlan := m.selectedPlan(); onPlan {
		t.Fatal("test setup: row 0 must be chrome, not a plan")
	}
	if bar := statusLine(m); strings.Contains(bar, "/Users/example/") {
		t.Fatalf("a chrome row's status bar carries an origin: %q", bar)
	}
}

// TestPlanCountTextIsSingularAtOne pins planCountText's English: "1 plan",
// never "1 plans". 0 and 2 are the plural's own boundaries, driven alongside
// it so a fix that only special-cased 1 cannot pass by accident.
func TestPlanCountTextIsSingularAtOne(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{0, "0 plans"},
		{1, "1 plan"},
		{2, "2 plans"},
	} {
		t.Run(fmt.Sprintf("%d", tc.n), func(t *testing.T) {
			m := NewList(nil, keymap.Default(), nil)
			items := make([]planItem, tc.n)
			for i := range items {
				items[i] = planItem{plan: domain.Plan{ID: domain.PlanID(fmt.Sprintf("l_%d", i)), Title: "Plan"}}
			}
			m.applyRefresh(items)
			if got := m.planCountText(); got != tc.want {
				t.Fatalf("planCountText() = %q, want %q", got, tc.want)
			}
		})
	}
}

// originEndsTheBar reports whether the status bar's right-hand side carries
// the selected row's origin, by the property the head-clipped column now has:
// whatever ends the bar must be a LEADING slice of the path, modulo the
// ellipsis the clip appends. Matching on a filename literal is exactly what
// stopped working, and matching on a recomputed sourceLabel would only assert
// that the code agrees with itself.
func originEndsTheBar(bar, path string) error {
	fields := strings.Fields(bar)
	if len(fields) == 0 {
		return errors.New("status bar is empty")
	}
	// The path carries no spaces, so the bar's last field IS the origin cell.
	origin := strings.TrimSuffix(fields[len(fields)-1], "…")
	if origin == "" || !strings.HasPrefix(path, origin) {
		return fmt.Errorf("the text ending the bar (%q) is not a leading slice of %q", origin, path)
	}
	return nil
}

// TestMastheadBoxRendersThePinnedGeometry pins the box's own rendered shape
// against the settled sketch, byte for byte rather than by containment:
// rounded corners, outer indent 1 between the rail inset and the box's own
// left border (mastheadIndent), inner pad 4 between that border and the sigil
// -- and again between the name and the border on the other side
// (mastheadPad) -- and the content itself, "§ Draftplane" and nothing else in
// its place: no other glyph for the sigil, no missing or doubled space.
func TestMastheadBoxRendersThePinnedGeometry(t *testing.T) {
	// mastheadIndent and mastheadPad are pinned as LITERALS here, deliberately,
	// and the expected rows below are built from literals too, not from either
	// constant: a test that read its own expectation off them would move with
	// the constants and pin nothing about the numbers actually settled on.
	if mastheadIndent != 1 {
		t.Fatalf("mastheadIndent = %d, want 1", mastheadIndent)
	}
	if mastheadPad != 4 {
		t.Fatalf("mastheadPad = %d, want 4", mastheadPad)
	}

	m := NewList(nil, keymap.Default(), nil)
	m.width, m.height = 80, 24
	rows := strings.Split(ansi.Strip(m.View().Content), "\n")
	margin := strings.Repeat(" ", m.listMargin())
	// rows[0] is the ground row; the box is rows 1-3, and row 4 is blank. One
	// leading space (indent), then the border; four spaces (pad) either side of
	// the sigil and name.
	for i, want := range []string{
		" ╭────────────────────╮",
		" │    § Draftplane    │",
		" ╰────────────────────╯",
	} {
		got := strings.TrimPrefix(rows[i+1], margin)
		if !strings.HasPrefix(got, want) {
			t.Fatalf("box row %d = %q, want it to start with %q", i, got, want)
		}
	}
	if got := strings.TrimSpace(rows[4]); got != "" {
		t.Fatalf("row 4 = %q, want the blank row under the box", got)
	}
}

// TestMastheadBoxRowBudgetHoldsAtBothSizes is the row-budget claim the
// masthead box's design demands driven, not reasoned about: the frame is exactly
// the terminal's own rows, and no row wider than its own columns, at 80x24 and
// at the minimum-size gate.
func TestMastheadBoxRowBudgetHoldsAtBothSizes(t *testing.T) {
	for _, sz := range []struct {
		name string
		w, h int
	}{
		{"80x24", 80, 24},
		{"the minimum gate", listMinWidth, listMinHeight},
	} {
		t.Run(sz.name, func(t *testing.T) {
			m := NewList(nil, keymap.Default(), nil)
			m.width, m.height = sz.w, sz.h
			m.applyRefresh(listSectionItems("Mine", 3))
			if !m.fitsMinimum() {
				t.Fatalf("test setup: %dx%d does not clear fitsMinimum() -- this case is not exercising a real screen", sz.w, sz.h)
			}
			screen := ansi.Strip(m.View().Content)
			rows := strings.Split(screen, "\n")
			if len(rows) != sz.h {
				t.Fatalf("the frame is %d rows against a terminal of %d:\n%s", len(rows), sz.h, screen)
			}
			for i, row := range rows {
				if w := ansi.StringWidth(row); w != sz.w {
					t.Fatalf("row %d is %d cells against a terminal of %d: %q", i, w, sz.w, row)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The filter.
// ---------------------------------------------------------------------------

// THE FIXTURE, and it exists to defeat one specific vacuity. On a fixture whose
// plans all fit on screen, "filters visible" and "filters loaded" are THE SAME
// FUNCTION, and a test written against one cannot fail against the other. So
// every number below is chosen to make loaded > visible:
//
//	terminal     80x24
//	rowBudget    24 - listBrowseChrome = 15
//	drawn        14 plans + "+66 more"
//	loaded       80
//
// filterNeedleIndex is 40: well past the plans "Your plans" draws, so the needle
// sits behind its "+N more" and is on no screen this fixture ever renders. A
// filter over the VISIBLE rows finds nothing, which is what makes the assertions
// below able to fail.
const (
	filterFixturePlans = 80
	filterNeedleIndex  = 40
)

// newFilterList builds the fixture above: 80 plans over a store that answers
// with them, the one at filterNeedleIndex retitled "Marigold rollout". No other
// title contains "mar", so the queries below match the needle and nothing else.
func newFilterList(t *testing.T) *ListModel {
	t.Helper()
	plans := freshestFirstPlans(filterFixturePlans)
	plans[filterNeedleIndex].Title = "Marigold rollout"
	m := NewList(listOfSvc{plans: plans}, keymap.Default(), nil)
	cur, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = cur.(*ListModel)
	return drainList(t, m, m.Init())
}

// planTitles is the body as the reader sees it: every PLAN row's title, in row
// order, chrome excluded.
func planTitles(rows []row) []string {
	out := []string{}
	for _, r := range rows {
		if r.kind == rowPlan {
			out = append(out, r.item.plan.Title)
		}
	}
	return out
}

// typeFilterQuery types query one keystroke per rune, the way a human does, and
// fails if any of them dispatches a cmd -- a filter keystroke is pure.
func typeFilterQuery(t *testing.T, m *ListModel, query string) *ListModel {
	t.Helper()
	for _, r := range query {
		var cmd tea.Cmd
		m, cmd = pressListKey(m, string(r))
		if cmd != nil {
			t.Fatalf("typing %q into the filter dispatched %T; a filter keystroke must dispatch nothing", r, cmd())
		}
	}
	return m
}

// TestTheFilterReachesRowsBehindPlusNMore is the property an implementer gets
// wrong: the filter matches over what is LOADED, not over what is on the
// screen. It is driven from the keyboard against a fixture where the two
// differ by more than sixty rows (see the constants above).
//
// THE MUTATION THAT PROVES IT CAN FAIL: narrow the filter to the visible slice
// -- build sectionRows from the UNFILTERED set, allocate, and then drop the rows
// whose item does not match -- and every row of this table that wants a match
// fails with an empty body, because the needle is on no screen this fixture
// draws.
func TestTheFilterReachesRowsBehindPlusNMore(t *testing.T) {
	m := newFilterList(t)

	// The fixture's own assumption, checked rather than asserted in prose: the
	// needle is loaded, and it is not drawn.
	if len(m.items) != filterFixturePlans {
		t.Fatalf("loaded = %d, want %d", len(m.items), filterFixturePlans)
	}
	drawn := planTitles(m.rows)
	if len(drawn) >= filterNeedleIndex {
		t.Fatalf("the section draws %d plans, which reaches the needle at index %d -- "+
			"loaded is not greater than visible and this test cannot fail", len(drawn), filterNeedleIndex)
	}
	if strings.Contains(ansi.Strip(m.View().Content), "Marigold rollout") {
		t.Fatal("the needle is on screen before any filter runs; it must sit behind a +N more")
	}
	if _, _, ok := sectionMoreRow(m, sectionMine); !ok {
		t.Fatal("the section draws no +N more row, so nothing is hidden behind one")
	}

	for _, tc := range []struct {
		name  string
		query string
		want  []string
	}{
		{"a query finds the plan behind the +N more", "mar", []string{"Marigold rollout"}},
		// Case-folded, and matching mid-string rather than as a prefix -- a
		// human filtering a list of sentences types a word from the middle of
		// one.
		{"case folded and matched anywhere in the title", "GOLD ROLL", []string{"Marigold rollout"}},
		{"a query nothing matches empties the section", "zzz", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newFilterList(t)
			m, cmd := pressListKey(m, "/")
			if cmd != nil {
				t.Fatalf("opening the filter dispatched %T, want nothing", cmd())
			}
			if m.mode != listFilter {
				t.Fatalf("mode after / = %v, want listFilter", m.mode)
			}
			m = typeFilterQuery(t, m, tc.query)

			if got := planTitles(m.rows); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("filtered body = %v, want exactly %v", got, tc.want)
			}
			// The same set is on the SCREEN, not merely in the row model.
			view := ansi.Strip(m.View().Content)
			for _, title := range tc.want {
				if !strings.Contains(view, title) {
					t.Fatalf("%q matched the filter but is not drawn:\n%s", title, view)
				}
			}
			// And the help bar carries the query, so a reader can see what is
			// doing the narrowing.
			if !strings.Contains(view, "/"+tc.query) {
				t.Fatalf("the query %q is nowhere on the filtered screen:\n%s", tc.query, view)
			}
		})
	}
}

// TestPlusNMoreCountsMatchesNotTheLoadedSet is about the ARITHMETIC the layout
// does over the filtered set rather than about anything the filter draws for
// itself. The counts are derived at 80x24 and stated so they can be re-derived:
// rowBudget is 15, so a body holding more than 15 matches draws 14 and a tail.
func TestPlusNMoreCountsMatchesNotTheLoadedSet(t *testing.T) {
	t.Run("the tail counts matches, not the loaded set", func(t *testing.T) {
		// "1" matches the titles numbered 01, 10-19, 21, 31, 41, 51, 61 and 71
		// -- 17 of them. Unfiltered the tail reads +66, which is the number a
		// tail counting the LOADED set would keep.
		m := newFilterList(t)
		m, _ = pressListKey(m, "/")
		m = typeFilterQuery(t, m, "1")

		rows := sectionContentRows(m, listMineSectionTitle)
		if got := countKind(rows, rowPlan); got != 14 {
			t.Fatalf("the section drew %d plan rows, want 14", got)
		}
		_, more, ok := sectionMoreRow(m, sectionMine)
		if !ok {
			t.Fatal("the section lost its +N more row under a filter; the disclosure must stay")
		}
		if !strings.Contains(more.text, "+3 more") {
			t.Fatalf("tail = %q, want it to count MATCHES (+3 more), not the loaded set", more.text)
		}
	})

	t.Run("a filter matching nothing says so", func(t *testing.T) {
		m := newFilterList(t)
		m, _ = pressListKey(m, "/")
		m = typeFilterQuery(t, m, "zzz")
		view := ansi.Strip(m.View().Content)
		if n := strings.Count(view, "no plans match"); n != 1 {
			t.Fatalf("the section says the filter matched nothing %d times, want once:\n%s", n, view)
		}
		if strings.Contains(view, listEmptyHint) {
			t.Fatalf("an empty-because-filtered section is explaining itself with an unfiltered reason:\n%s", view)
		}
	})
}

// TestTheFilterIsEnteredAndLeftAndTheCursorFollowsItsPlan pins the two exits --
// they are NOT the same exit -- and what happens to the cursor across each,
// which is the half a mode transition gets wrong silently.
//
// THE THIRD ROW IS THE INTERESTING ONE, and it is the case a filter creates
// that nothing else in browse does: the reader is on a plan that is ONLY
// reachable because the filter narrowed its section to fit. Clearing the filter
// widens the section past its allocation again, that plan stops being a row at
// all, and rebuildRows' by-plan re-find has nothing to find. Keeping the row
// INDEX there lands the cursor at an arbitrary place in a body that just
// changed length, so it goes to the section's own "+N more" instead -- the row
// that stands for exactly the plans the layout dropped.
//
// THE GROWTH IS A ROW COUNT, NOT AN ITEM COUNT -- the loaded set minus the
// matches is the wrong number. The browse body is structurally bounded at
// listSectionChrome + rowBudget, so clearing "mar" takes it from 3 rows to
// that bound. The bound is the point: the plans the widened body still cannot
// draw are the ones its tail stands for, which is why the tail is where the
// cursor goes.
func TestTheFilterIsEnteredAndLeftAndTheCursorFollowsItsPlan(t *testing.T) {
	for _, tc := range []struct {
		name      string
		query     string
		leave     string
		wantQuery string
		wantTail  bool // the cursor lands on "Your plans"' +N more, not on a plan
	}{
		{
			name:      "enter accepts: the filter stays on and browse takes over",
			query:     "mar",
			leave:     "enter",
			wantQuery: "mar",
		},
		{
			// The plan is the section's first row either way, so the widened
			// body still draws it and the cursor simply stays.
			name:      "esc clears and the widened list still draws the plan",
			query:     "plan 00",
			leave:     "esc",
			wantQuery: "",
		},
		{
			// "Marigold rollout" is plan 40 of a section that draws fourteen.
			name:      "esc clears and the plan it drops hands the cursor to the section's tail",
			query:     "mar",
			leave:     "esc",
			wantQuery: "",
			wantTail:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newFilterList(t)
			m, _ = pressListKey(m, "/")
			m = typeFilterQuery(t, m, tc.query)

			// The cursor is put on a MATCH, so there is an identity to follow.
			m.cursor = m.firstSelectableRow()
			it, ok := m.selectedPlan()
			if !ok {
				t.Fatal("the filtered body has no plan row for the cursor to rest on")
			}

			before := len(m.rows)
			m, _ = pressListKey(m, tc.leave)
			if m.mode != listBrowse {
				t.Fatalf("%q left mode = %v, want browse", tc.leave, m.mode)
			}
			if m.filter != tc.wantQuery {
				t.Fatalf("%q left query %q, want %q", tc.leave, m.filter, tc.wantQuery)
			}
			if tc.wantTail {
				idx, _, ok := sectionMoreRow(m, sectionMine)
				if !ok {
					t.Fatal("the widened section has no +N more row for the cursor to land on")
				}
				if m.cursor != idx {
					t.Fatalf("the cursor is at row %d after dropping %q, want its section's tail at %d",
						m.cursor, it.plan.Title, idx)
				}
				// The widened body is the STRUCTURAL BOUND (chrome + budget),
				// asserted symbolically so it cannot drift.
				if got, want := len(m.rows), listSectionChrome+m.rowBudget(); got != want {
					t.Fatalf("the widened body is %d rows, want the bound listSectionChrome+rowBudget = %d", got, want)
				}
				// The worked value of the symbolic bound just above, which is
				// the assertion that cannot drift.
				if grew := len(m.rows) - before; grew != 14 {
					t.Fatalf("clearing the filter grew the body by %d rows, want 14 (3 -> 17 at 80x24)", grew)
				}
			} else {
				// THE CURSOR IS STILL ON THE SAME PLAN -- rebuildRows re-finds
				// it by id -- which is what makes the transition invisible.
				after, ok := m.selectedPlan()
				if !ok || after.plan.ID != it.plan.ID {
					t.Fatalf("%q moved the cursor off %q (now on %q, ok=%v)", tc.leave, it.plan.Title, after.plan.Title, ok)
				}
			}
			// And the help bar tells the truth about which of the two states
			// the list is now in.
			hint := m.helpBarLine()
			if tc.wantQuery == "" {
				if strings.Contains(hint, "esc clear") {
					t.Fatalf("browse with no filter still offers to clear one: %q", hint)
				}
			} else if !strings.Contains(hint, "/"+tc.wantQuery) || !strings.Contains(hint, "esc clear") {
				t.Fatalf("browse under a filter = %q, want it to name the query and the key that clears it", hint)
			}
		})
	}

	// esc from BROWSE clears an accepted filter, and it lands the cursor where
	// esc from inside the mode does -- one rule, two doors. Without the shared
	// rule this door alone would leave the cursor at an arbitrary index.
	m := newFilterList(t)
	m, _ = pressListKey(m, "/")
	m = typeFilterQuery(t, m, "mar")
	m, _ = pressListKey(m, "enter")
	m.cursor = m.firstSelectableRow()
	it, ok := m.selectedPlan()
	if !ok || it.plan.Title != "Marigold rollout" {
		t.Fatalf("test setup: the cursor is on %+v, want the section's only match", it.plan.Title)
	}
	m, cmd := pressListKey(m, "esc")
	if cmd != nil {
		t.Fatalf("esc in browse dispatched %T, want nothing", cmd())
	}
	if m.filterActive() || m.mode != listBrowse {
		t.Fatalf("esc in browse left query %q in mode %v, want no filter in browse", m.filter, m.mode)
	}
	if idx, _, ok := sectionMoreRow(m, sectionMine); !ok || m.cursor != idx {
		t.Fatalf("esc in browse left the cursor at row %d, want the section's tail (%d, found=%v)", m.cursor, idx, ok)
	}

	// "/" over an ACTIVE filter re-opens it for editing rather than clearing
	// it: the reader who typed "mar" and meant "mary" adds a letter.
	m = newFilterList(t)
	m, _ = pressListKey(m, "/")
	m = typeFilterQuery(t, m, "mar")
	m, _ = pressListKey(m, "enter")
	m, _ = pressListKey(m, "/")
	if m.mode != listFilter || m.filter != "mar" {
		t.Fatalf("/ over an active filter left mode %v and query %q, want listFilter and %q", m.mode, m.filter, "mar")
	}
	m = typeFilterQuery(t, m, "igold")
	if m.filter != "marigold" {
		t.Fatalf("editing an active filter produced %q, want %q", m.filter, "marigold")
	}
}

// TestTheFilterKeyIsRebindableInBothDirections is what
// TestDefaultCoversEveryAction cannot be: that one proves ActFilter is
// DECLARED with a default binding, and declaring is not dispatching -- an
// action in AllActions with no case in Update passes it while doing nothing.
//
// BOTH DIRECTIONS, and each is a separate way to ship a dead gesture:
//
//  1. The bound key DISPATCHES. "/" opens the filter by default; a rebound "F"
//     opens it after keymap.Load overlays it -- and Load itself only accepts
//     "filter" because it is in AllActions, so the declaration is proved
//     through the real config path rather than by reading the slice.
//  2. The OLD key goes dead. "/" after the rebind does nothing, which is what
//     fails if the mode is entered on a literal keystroke somewhere instead of
//     on the action.
//
// The help bar is checked alongside, since a hint naming a key the keymap no
// longer holds is the same defect one layer out.
func TestTheFilterKeyIsRebindableInBothDirections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keymap.json")
	if err := os.WriteFile(path, []byte(`{"filter": "F"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rebound, err := keymap.Load(path)
	if err != nil {
		t.Fatalf("keymap.Load rejected a filter rebind: %v -- ActFilter is not in AllActions", err)
	}

	for _, tc := range []struct {
		name  string
		km    keymap.Map
		opens string
		dead  string
	}{
		{"the default binding", keymap.Default(), "/", ""},
		{"a rebound one", rebound, "F", "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build := func() *ListModel {
				m := NewList(nil, tc.km, nil)
				m.width, m.height = 80, 24
				m.applyRefresh(listSectionItems("Mine", 3))
				return m
			}

			m := build()
			m, cmd := pressListKey(m, tc.opens)
			if cmd != nil {
				t.Fatalf("%q dispatched %T, want nothing", tc.opens, cmd())
			}
			if m.mode != listFilter {
				t.Fatalf("%q left mode %v, want listFilter -- the action is declared but never dispatched", tc.opens, m.mode)
			}
			if hint := m.hintLine(); !strings.Contains(hint, tc.opens+" filter") {
				t.Fatalf("the browse hint = %q, want it to name %q as the filter key", hint, tc.opens)
			}

			if tc.dead == "" {
				return
			}
			m = build()
			m, cmd = pressListKey(m, tc.dead)
			if cmd != nil || m.mode != listBrowse {
				t.Fatalf("the rebound-away %q still did something (mode %v, cmd %v)", tc.dead, m.mode, cmd != nil)
			}
		})
	}
}

// TestARefreshWhileFilteringIsDeferredToTheSeam pins the third thing about the
// filter that is an ABSENCE: while the query is being typed, a state change
// does not land. The mode defers it, exactly as rename does, because a body
// reshuffling under a half-typed query is the interruption both modes exist to
// prevent -- and both of the mode's exits then consume it, which is what stops
// a deferral from becoming a permanent one.
//
// THE QUERY IS NOT WHAT DEFERS, and the second half of this test is what says
// so: back in browse with the filter STILL ON, a state change refreshes at once
// and the filter is simply re-applied to what arrived.
func TestARefreshWhileFilteringIsDeferredToTheSeam(t *testing.T) {
	for _, leave := range []string{"enter", "esc"} {
		t.Run("left with "+leave, func(t *testing.T) {
			m := newFilterList(t)
			m, _ = pressListKey(m, "/")
			m = typeFilterQuery(t, m, "mar")
			base := m.refreshSeq

			cur, cmd := m.Update(msgStateChanged{})
			m = cur.(*ListModel)
			if cmd != nil {
				t.Fatalf("a state change while filtering dispatched %T, want it deferred", cmd())
			}
			if !m.pendingRefresh || m.refreshSeq != base {
				t.Fatalf("pendingRefresh = %v after %d refreshes, want deferred with none",
					m.pendingRefresh, m.refreshSeq-base)
			}

			m, cmd = pressListKey(m, leave)
			if cmd == nil {
				t.Fatalf("%q did not consume the deferred refresh; it would be stranded", leave)
			}
			m = drainList(t, m, cmd)
			if m.pendingRefresh || m.mode != listBrowse {
				t.Fatalf("after %q: pendingRefresh = %v, mode = %v; want consumed and browse", leave, m.pendingRefresh, m.mode)
			}
		})
	}

	// In BROWSE with the filter on, a state change is not deferred at all.
	m := newFilterList(t)
	m, _ = pressListKey(m, "/")
	m = typeFilterQuery(t, m, "mar")
	m, _ = pressListKey(m, "enter")
	base := m.appliedRefreshSeq

	cur, cmd := m.Update(msgStateChanged{})
	m = cur.(*ListModel)
	if cmd == nil {
		t.Fatal("a state change in browse under an active filter was deferred; the MODE defers, the query does not")
	}
	m = drainList(t, m, cmd)
	if m.appliedRefreshSeq <= base {
		t.Fatal("the refresh never landed")
	}
	if m.filter != "mar" {
		t.Fatalf("the refresh cleared the query (%q); nothing a refresh does may touch it", m.filter)
	}
}

// TestAFilterDoesNotOverwriteWhyASectionWasAlreadyEmpty: the question an empty
// section answers is "did the FILTER empty me", not "is a filter on", and those
// differ whenever the section was empty before any query was typed: a store with
// no plans at all must still say how to make one.
func TestAFilterDoesNotOverwriteWhyASectionWasAlreadyEmpty(t *testing.T) {
	for _, tc := range []struct {
		name  string
		items []planItem
		want  string
	}{
		{"no plans at all", nil, listEmptyHint},
		// THE POSITIVE CONTROL. This section HAS rows and the query excluded
		// every one of them, so here the filter genuinely is the reason --
		// without this row the table passes against an emptyHint that never
		// mentions a filter at all.
		{"a populated section the query emptied", listSectionItems("Mine", 3), `no plans match "zzz"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewList(nil, keymap.Default(), nil)
			m.width, m.height = 100, 24
			m.applyRefresh(tc.items)

			m, _ = pressListKey(m, "/")
			m = typeFilterQuery(t, m, "zzz")
			if !m.filterActive() {
				t.Fatal("test setup: no filter is active")
			}

			if got := strings.Join(m.emptyHint(), "\n"); got != tc.want {
				t.Fatalf("the section under a filter says %q, want %q", got, tc.want)
			}
			if view := ansi.Strip(m.View().Content); !strings.Contains(view, tc.want) {
				t.Fatalf("view is missing %q:\n%s", tc.want, view)
			}
		})
	}
}

// TestTheQueryIsTrimmedAndEditedByRune pins two pieces of the query's own
// handling that each survived being deleted outright while the whole suite
// stayed green.
//
// TRIMMING is what makes "" the single answer for "no filter": a query of pure
// whitespace must not be an ACTIVE filter matching nothing, which is a body
// emptied by a keystroke the reader cannot see.
//
// BACKSPACE BY RUNE is what keeps a multi-byte query a valid string.
// Truncating a byte off "café" leaves an invalid fragment that matches nothing
// at all -- so the filter would silently empty the list one backspace after a
// reader typed an accented character, and the query on screen would look right.
func TestTheQueryIsTrimmedAndEditedByRune(t *testing.T) {
	// Titles carry the accented word the rune case needs.
	items := []planItem{
		{plan: domain.Plan{ID: "l_1", Title: "Caffeine budget"}},
		{plan: domain.Plan{ID: "l_2", Title: "Rollout"}},
	}
	build := func(t *testing.T) *ListModel {
		t.Helper()
		m := NewList(nil, keymap.Default(), nil)
		m.width, m.height = 80, 24
		m.applyRefresh(items)
		m, _ = pressListKey(m, "/")
		return m
	}

	t.Run("a query of pure whitespace is no filter at all", func(t *testing.T) {
		m := typeFilterQuery(t, build(t), "   ")
		if m.filter != "   " {
			t.Fatalf("the raw query is %q, want the spaces the reader typed", m.filter)
		}
		if m.filterActive() {
			t.Fatalf("a whitespace-only query is ACTIVE (%q); it would empty the body invisibly", m.filterQuery())
		}
		if got := planTitles(m.rows); len(got) != len(items) {
			t.Fatalf("the body shows %v, want every plan -- whitespace narrowed the list", got)
		}
	})

	t.Run("the ends are trimmed before matching", func(t *testing.T) {
		m := typeFilterQuery(t, build(t), "  caffeine  ")
		if got := planTitles(m.rows); len(got) != 1 || got[0] != "Caffeine budget" {
			t.Fatalf("body = %v, want the one match -- the query's own spaces were matched literally", got)
		}
	})

	t.Run("backspace deletes a rune, not a byte", func(t *testing.T) {
		m := build(t)
		// "café" typed one rune at a time, the multi-byte one built as a
		// terminal delivers it rather than through the ASCII press helper.
		m = typeFilterQuery(t, m, "caf")
		cur, cmd := m.Update(tea.KeyPressMsg{Code: 'é', Text: "é"})
		m = cur.(*ListModel)
		if cmd != nil {
			t.Fatalf("typing an accented character dispatched %T, want nothing", cmd())
		}
		if m.filter != "café" {
			t.Fatalf("query = %q, want %q", m.filter, "café")
		}
		m, _ = pressListKey(m, "backspace")
		if m.filter != "caf" {
			t.Fatalf("backspace over a multi-byte rune left %q (%d bytes), want %q -- it cut a byte", m.filter, len(m.filter), "caf")
		}
		// The user-visible half: the query still matches, where an invalid
		// fragment would match nothing and empty the list.
		if got := planTitles(m.rows); len(got) != 1 || got[0] != "Caffeine budget" {
			t.Fatalf("body after backspacing to %q = %v, want the one match", m.filter, got)
		}
	})
}

// TestTheFilterClauseIsShownOnlyWhereEscClearsIt: an active filter puts "esc
// clear" in the help bar, and esc in RENAME cancels the rename instead, so the
// clause is emitted in browse alone.
//
// It is true twice over. The bar under rename is renameHint, so hintLine is not
// consulted there at all -- and hintLine's own `m.mode == listBrowse` guard is
// unreachable through any call site. It is kept as defence against the next
// mode routed there, so the second half of this test calls hintLine DIRECTLY:
// nothing else can reach the branch.
func TestTheFilterClauseIsShownOnlyWhereEscClearsIt(t *testing.T) {
	m := newFilterList(t)
	m, _ = pressListKey(m, "/")
	m = typeFilterQuery(t, m, "mar")
	m, _ = pressListKey(m, "enter")
	if !strings.Contains(m.helpBarLine(), "esc clear") {
		t.Fatalf("browse under a filter does not offer to clear it: %q", m.helpBarLine())
	}

	// Into rename, on a filtered row, with the filter still on.
	m.cursor = m.firstSelectableRow()
	m, _ = pressListKey(m, "e")
	if m.mode != listRename || !m.filterActive() {
		t.Fatalf("test setup: mode %v, query %q; want rename with the filter still on", m.mode, m.filter)
	}
	if strings.Contains(m.helpBarLine(), "esc clear") {
		t.Fatalf("the rename help bar offers %q, but esc there cancels the rename: %q", "esc clear", m.helpBarLine())
	}
	if strings.Contains(m.hintLine(), "esc clear") {
		t.Fatalf("hintLine emits %q outside browse, where esc means something else: %q", "esc clear", m.hintLine())
	}
	// esc proves it: the rename is cancelled and the filter is untouched.
	m, _ = pressListKey(m, "esc")
	if m.mode != listBrowse || m.filter != "mar" {
		t.Fatalf("esc in rename left mode %v and query %q, want browse with the filter intact", m.mode, m.filter)
	}
}

// TestALongQueryIsClippedAndNeverWrapsTheFrame is what actually protects the
// help bar now that it carries the reader's own text: no query length is
// impossible, so the line's width is bounded by nothing this file controls, and
// View's ansi.Truncate is the whole of the defence. A wrapped help bar is not a
// cosmetic problem -- it adds a row the height reservation never budgeted and
// pushes the frame past m.height. Pinned across both palettes at the sizes the
// gate actually admits.
//
// listSort is in the sweep because the modal is COMPOSITED over this very
// frame: a help bar that wrapped underneath it would push the box off the
// bottom of a frame the composite cannot lengthen.
func TestALongQueryIsClippedAndNeverWrapsTheFrame(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	for _, tm := range []*theme.Theme{nil, th} {
		for _, width := range []int{listMinWidth, 40, 80, 120} {
			for _, mode := range []listMode{listBrowse, listFilter, listSort} {
				name := fmt.Sprintf("painted=%v/width=%d/mode=%d", tm != nil, width, mode)
				t.Run(name, func(t *testing.T) {
					m := NewList(nil, keymap.Default(), tm)
					m.width, m.height = width, 24
					m.applyRefresh(listSectionItems("Mine", 3))
					m.filter = strings.Repeat("z", 200)
					m.setMode(mode)
					m.rebuildRows()

					lines := strings.Split(ansi.Strip(m.View().Content), "\n")
					if len(lines) > m.height {
						t.Fatalf("a 200-character query drew %d lines, want <= %d -- the help bar wrapped", len(lines), m.height)
					}
					for i, l := range lines {
						if w := ansi.StringWidth(l); w > width {
							t.Fatalf("line %d is %d cells wide, want <= %d: %q", i, w, width, l)
						}
					}
				})
			}
		}
	}
}

// sortFixtureItems is the sort modal's own section fixture, and it differs from
// listSectionItems in the one way every test below depends on: THE TITLES RUN
// AGAINST THE ACTIVITY CLOCK. listSectionItems numbers both in the same
// direction, so name-ascending and updated-descending happen to draw the
// identical list and a test over it cannot tell a working column from a column
// that silently kept the previous key. Here plan i is the newest and the
// alphabetically last, so every one of the three columns produces a different
// order from the other two.
func sortFixtureItems(prefix string, n int) []planItem {
	out := listSectionItems(prefix, n)
	for i := range out {
		out[i].plan.Title = fmt.Sprintf("%s Plan %03d", prefix, n-1-i)
		// created runs the same way as the title and against lastActivity, so
		// `created` is distinguishable from `updated` as well.
		out[i].created = listFixtureEpoch.Add(time.Duration(i) * time.Hour)
	}
	return out
}

// newSortList is the sort modal's own fixture: "Your plans" populated at a
// stated size, in theme.Default()'s palette unless one is handed in. It holds
// more plans than the layout can draw at 80x24, which is what makes the
// re-sort's behind-the-tail case reachable at all -- a plan can leave the drawn
// set under a new order only if there is a "+N more" for it to leave behind.
func newSortList(t *testing.T, th *theme.Theme, width, height int) *ListModel {
	t.Helper()
	return newSortListOfSize(t, th, width, height, 20)
}

func newSortListOfSize(t *testing.T, th *theme.Theme, width, height, n int) *ListModel {
	t.Helper()
	m := NewList(nil, keymap.Default(), th)
	m.width, m.height = width, height
	m.applyRefresh(sortFixtureItems("Mine", n))
	return m
}

// TestTheSortModalChangesNoLayoutArithmetic is the composite's own proof, and
// it asserts the invariant a LINE-COUNT CHECK CANNOT SEE: an over-wide row
// wraps and adds a visual row that no line count notices.
//
// FOUR THINGS, and the last three are what "no layout arithmetic" means:
//
//  1. The modal is actually on screen. Without this the rest passes trivially
//     against a frame with no box in it.
//  2. The composite's LINE COUNT equals the browse frame's -- no row added, no
//     row removed.
//  3. Every composite row is exactly as wide as the row ui.Overlay was handed.
//     The browse frame is checked to be within m.width first, so squaring it is
//     pure padding and cannot be hiding an over-wide row.
//  4. viewHeight, mastheadHeight and the row count are IDENTICAL in browse and
//     in sort. This is the reservation half: a modal that took a row out of the
//     body would move the first two, and one that changed the body's shape would
//     move the third. All three are what a bottom-anchored panel does move, and
//     the reason the modal is a composite instead.
func TestTheSortModalChangesNoLayoutArithmetic(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []struct {
		name  string
		theme *theme.Theme
	}{{"system", nil}, {"painted", th}} {
		for _, size := range []struct{ w, h int }{
			{listMinWidth, listMinHeight}, // the gate's own floor
			{80, 24},
			{120, 40},
		} {
			t.Run(fmt.Sprintf("%s/%dx%d", path.name, size.w, size.h), func(t *testing.T) {
				browse := newSortList(t, path.theme, size.w, size.h)
				sorted, _ := pressListKey(newSortList(t, path.theme, size.w, size.h), "s")
				if sorted.mode != listSort {
					t.Fatalf("s left mode %v, want listSort", sorted.mode)
				}

				before := strings.Split(browse.View().Content, "\n")
				after := strings.Split(sorted.View().Content, "\n")
				if !strings.Contains(ansi.Strip(strings.Join(after, "\n")), listSortTitlePrefix+listSortTitleSeparator) {
					t.Fatalf("the composite carries no modal at all; every assertion below would pass on an empty splice")
				}
				if len(before) != len(after) {
					t.Fatalf("the composite drew %d lines against browse's %d -- ui.Overlay must add and remove none", len(after), len(before))
				}
				background := strings.Split(squareFrame(browse.View().Content, size.w), "\n")
				for i := range after {
					if w := ansi.StringWidth(before[i]); w > size.w {
						t.Fatalf("browse row %d is %d cells wide against a terminal of %d; squareFrame would be truncating, not padding", i, w, size.w)
					}
					if got, want := ansi.StringWidth(after[i]), ansi.StringWidth(background[i]); got != want {
						t.Fatalf("composite row %d is %d cells wide, want %d -- an over-wide row wraps and adds a visual row, and the line count above cannot see it", i, got, want)
					}
				}

				// The box lands INTACT, row for row. This is the assertion that
				// names the defect the width check catches: hand ui.Overlay a
				// ragged background and the rows whose background is short come
				// out with the box chewed off them. The assertion stays because
				// the property is compositing's, not this renderer's favour.
				box := strings.Split(ansi.Strip(sorted.sortModal()), "\n")
				top := (size.h - len(box)) / 2
				for i, want := range box {
					if got := ansi.Strip(after[top+i]); !strings.Contains(got, want) {
						t.Fatalf("frame row %d is %q and does not carry the modal's own row %q", top+i, got, want)
					}
				}

				if a, b := browse.viewHeight(), sorted.viewHeight(); a != b {
					t.Fatalf("viewHeight is %d in browse and %d in sort; the modal reserves nothing", a, b)
				}
				if a, b := browse.mastheadHeight(), sorted.mastheadHeight(); a != b {
					t.Fatalf("mastheadHeight is %d in browse and %d in sort; the modal reserves nothing", a, b)
				}
				if a, b := len(browse.rows), len(sorted.rows); a != b {
					t.Fatalf("the body holds %d rows in browse and %d in sort; the modal changes no body", a, b)
				}
			})
		}
	}
}

// TestTheSortModalFitsInsideTheMinimumSizeGate re-derives the box's own size
// from the strings actually in it and checks it against the two bounds the gate
// refuses below. That pairing is what makes composeSortModal's centring
// arithmetic non-negative on every terminal a view is ever DRAWN on -- View
// answers gateView below the gate, so the modal never reaches a canvas smaller
// than this. ui.Overlay's own clamp still owns the out-of-range answer; this is
// why it is a guarantee rather than a live correction.
func TestTheSortModalFitsInsideTheMinimumSizeGate(t *testing.T) {
	m := NewList(nil, keymap.Default(), nil)
	m.width, m.height = listMinWidth, listMinHeight
	rows := strings.Split(m.sortModalPainted(), "\n")

	if len(rows) > listMinHeight {
		t.Fatalf("the modal is %d rows against a gate floor of %d", len(rows), listMinHeight)
	}
	for i, r := range rows {
		if w := ansi.StringWidth(r); w > listMinWidth {
			t.Fatalf("the modal's row %d is %d cells against a gate floor of %d", i, w, listMinWidth)
		}
		if w, want := ansi.StringWidth(r), ansi.StringWidth(rows[0]); w != want {
			t.Fatalf("the modal's row %d is %d cells and its top border is %d -- a ragged box paints the list through its own short rows", i, w, want)
		}
	}
}

// TestTheModalNamesItsSectionAndNoBandCarriesAMark drives the sort from the
// keyboard and checks the ONE place its scope is disclosed: the modal's own
// title, "sort · Your plans".
//
// A ▸ in a band's lead once named the section the cursor was in, so what s would
// reorder was legible BEFORE it was pressed; it was removed for reading as an
// expand affordance on a row no gesture can act on (bandTitle's note). SO THE
// GLYPH'S ABSENCE IS ASSERTED RATHER THAN MERELY UNMENTIONED: a test that simply
// stopped naming ▸ would go green on a band that had it back, and what put it
// out is a human's reading of the glyph rather than anything the code can notice
// on its own.
//
// THE ALIGNMENT OUTLIVES THE MARK and is still checked, because it was never the
// mark's business: a band's words sit in the column the NAME label and a plan's
// own title share, and the lead that puts them there is a constant now. A change
// that dropped the indent along with the glyph would leave every band hanging a
// glyph-width to the left of the body it heads, which no colour assertion sees.
func TestTheModalNamesItsSectionAndNoBandCarriesAMark(t *testing.T) {
	items := sortFixtureItems("Mine", 20)
	m := newRecentList(t, items, recent.Entry{PlanID: items[3].plan.ID}, recent.Entry{PlanID: items[7].plan.ID})
	cur, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = cur.(*ListModel)

	bandRow := func(section listSection) string {
		for _, r := range m.rows {
			if r.head && r.section == section {
				return ansi.Strip(m.renderBodyRowPainted(r, false))
			}
		}
		t.Fatalf("no name band for section %v", section)
		return ""
	}
	recentBand, mineBand := bandRow(sectionRecent), bandRow(sectionMine)
	for _, band := range []string{recentBand, mineBand} {
		if strings.Contains(band, "▸") {
			t.Fatalf("a band renders %q and carries ▸ -- that mark was removed for reading as an expand affordance on a row nothing can act on", band)
		}
	}

	// In CELLS, not bytes: these rows carry multi-byte glyphs, so a byte offset
	// would compare the wrong thing.
	lead := func(band, title string) int {
		return ansi.StringWidth(band[:strings.Index(band, title)])
	}
	bandLead := lead(mineBand, listMineSectionTitle)
	if got := lead(recentBand, listRecentSectionTitle); got != bandLead {
		t.Fatalf("the two band titles start at cell %d and %d (%q, %q); every band spends the same lead", got, bandLead, recentBand, mineBand)
	}
	// AND AGAINST THE TITLE COLUMN, which band-against-band alone cannot see: two
	// bands agreeing with each other while both sit a glyph-width off the body is
	// exactly what dropping the indent would produce.
	var labels, plan string
	for i, r := range m.rows {
		if r.head && r.section == sectionMine {
			labels = ansi.Strip(m.renderBodyRowPainted(m.rows[i+1], false))
			plan = ansi.Strip(m.renderBodyRowPainted(m.rows[i+2], false))
			break
		}
	}
	if got := lead(labels, "NAME"); got != bandLead {
		t.Fatalf("the band's title starts at cell %d and NAME at %d; the lead must align a band with the column it heads", bandLead, got)
	}
	if got := lead(plan, "Mine"); got != bandLead {
		t.Fatalf("the band's title starts at cell %d and a plan's title at %d", bandLead, got)
	}

	// s from "Your plans" opens the modal, and its title says what it orders.
	for m.cursorSection() != sectionMine {
		m, _ = pressListKey(m, "j")
	}
	m, _ = pressListKey(m, "s")
	if m.mode != listSort {
		t.Fatalf("s in Your plans left mode %v, want listSort", m.mode)
	}
	title := listSortTitlePrefix + listSortTitleSeparator + listMineSectionTitle
	if !strings.Contains(ansi.Strip(m.View().Content), title) {
		t.Fatalf("the modal's title does not read %q, and it is the ONLY disclosure of scope there is", title)
	}
}

// TestTheSortArrowMarksTheActiveColumnAndItsDirection drives the whole
// interaction from the keyboard: the arrow is ONE marker doing TWO jobs, so
// enter has two meanings decided by where the modal's cursor is.
//
// updated DESCENDING IS THE DERIVATION'S OWN ORDER, which the first row asserts
// by comparing the drawn section against sortItems' own output over the same
// plans.
func TestTheSortArrowMarksTheActiveColumnAndItsDirection(t *testing.T) {
	// FOUR PLANS WHOSE THREE KEYS DISAGREE PAIRWISE, so every one of the four
	// orders below is a DIFFERENT permutation: a column that silently kept the
	// previous key, or read a neighbouring one, cannot pass by coincidence.
	// Chosen by hand, not generated, and the four expectations are derived from
	// these numbers rather than the other way round.
	//
	//	title  activity(min ago)  created(h)
	//	d      40 newest          3
	//	c      10 oldest          1
	//	b      30                 4 newest
	//	a      20                 2
	keys := []struct {
		title    string
		activity int
		created  int
	}{{"d", 40, 3}, {"c", 10, 1}, {"b", 30, 4}, {"a", 20, 2}}
	items := make([]planItem, 0, len(keys))
	for i, k := range keys {
		items = append(items, planItem{
			plan:         domain.Plan{ID: domain.PlanID(fmt.Sprintf("p%d", i)), Title: k.title},
			lastActivity: listFixtureEpoch.Add(time.Duration(k.activity) * time.Minute),
			created:      listFixtureEpoch.Add(time.Duration(k.created) * time.Hour),
		})
	}

	for _, tc := range []struct {
		name  string
		down  int // j presses inside the modal before enter
		again bool
		want  sortOrder
		order []string
	}{
		{"the default is the derivation's own order", 0, false, sortOrder{sortUpdated, sortDescending}, []string{"d", "b", "a", "c"}},
		{"enter on the active column flips it", 0, true, sortOrder{sortUpdated, sortAscending}, []string{"c", "a", "b", "d"}},
		{"created takes its own natural default, newest first", 1, true, sortOrder{sortCreated, sortDescending}, []string{"b", "d", "a", "c"}},
		{"name takes ascending, because that is what alphabetical means", 2, true, sortOrder{sortName, sortAscending}, []string{"a", "b", "c", "d"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewList(nil, keymap.Default(), nil)
			m.width, m.height = 80, 24
			m.applyRefresh(items)

			if tc.again {
				m, _ = pressListKey(m, "s")
				for i := 0; i < tc.down; i++ {
					m, _ = pressListKey(m, "j")
				}
				m, _ = pressListKey(m, "enter")
			}
			if got := m.sortOrder; got != tc.want {
				t.Fatalf("the order is %+v, want %+v", got, tc.want)
			}
			if got := planTitles(sectionContentRows(m, listMineSectionTitle)); !reflect.DeepEqual(got, tc.order) {
				t.Fatalf("the section drew %v, want %v", got, tc.order)
			}
			if !tc.again {
				// updated descending is not merely A default, it is
				// byte-for-byte the order deriveListItems already produces
				// (sortItems), so a fresh list and a re-sorted-to-default one
				// draw the same rows.
				derived := make([]planItem, len(items))
				copy(derived, items)
				sortItems(derived)
				want := make([]string, 0, len(derived))
				for _, it := range derived {
					want = append(want, it.plan.Title)
				}
				if got := planTitles(sectionContentRows(m, listMineSectionTitle)); !reflect.DeepEqual(got, want) {
					t.Fatalf("the default order drew %v and the derivation's own sortItems gives %v; updated descending must BE the derivation's order", got, want)
				}
			}

			// THE BAND NAMES THE SECTION AND NOT THE ORDER. Asserted as an
			// ABSENCE, and against a sort this table has just CHANGED, so that
			// re-adding the clause reddens here rather than silently restoring a
			// row of chrome nobody asked for -- and so that the pin cannot pass
			// by the band happening to be in the default order.
			band := m.bandTitle(sectionMine)
			if band != listMineSectionTitle {
				t.Fatalf("the band reads %q, want exactly %q -- it names the section and nothing else", band, listMineSectionTitle)
			}
			if strings.Contains(band, tc.want.direction.arrow()) || strings.Contains(band, tc.want.column.label()) {
				t.Fatalf("the band %q still carries its sort clause; the order belongs to the modal", band)
			}
			// The modal's arrow is on the ACTIVE column and on no other, which is
			// what removes the need for a separate selection column beside it.
			lines, _ := m.sortModalContent()
			for _, l := range lines {
				if l.kind != sortLineOption && l.kind != sortLineCursorOption {
					continue
				}
				name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l.text), listCursorGlyph))
				marked := strings.Contains(l.text, tc.want.direction.arrow())
				if want := strings.HasPrefix(name, tc.want.column.label()); marked != want {
					t.Fatalf("option row %q carries the arrow = %v, want %v -- the arrow marks the active column and nothing else", l.text, marked, want)
				}
			}
		})
	}
}

// TestEscapingTheSortModalMovesNothing is the ABSENCE pin, and the absence is
// the whole design: there is NO LIVE PREVIEW, so nothing in the body moves
// until enter. Build one -- reorder as the modal's cursor walks and restore on
// esc -- and both halves below fail.
//
// The two reasons are both about the cursor: a preview has to hold the
// pre-modal order for esc to restore, which lands in the cursor-identity-
// across-resort code that needed six fixes (rebuildRows); and a live reorder
// could carry the plan under the LIST cursor behind the "+N more"
// mid-interaction. Neither can happen if nothing moves until commit.
func TestEscapingTheSortModalMovesNothing(t *testing.T) {
	m := newSortList(t, nil, 80, 24)
	m, _ = pressListKey(m, "j")
	before := planTitles(m.rows)
	cursorBefore, sectionBefore := m.cursor, m.cursorSection()
	orderBefore := m.sortOrder

	m, _ = pressListKey(m, "s")
	for _, key := range []string{"j", "j", "k"} {
		m, _ = pressListKey(m, key)
		if got := planTitles(m.rows); !reflect.DeepEqual(got, before) {
			t.Fatalf("walking the modal to %q reordered the body; there is no live preview", key)
		}
		if m.cursor != cursorBefore || m.cursorSection() != sectionBefore {
			t.Fatalf("walking the modal moved the LIST cursor to row %d in section %v", m.cursor, m.cursorSection())
		}
	}

	m, cmd := pressListKey(m, "esc")
	if cmd != nil {
		t.Fatalf("esc out of the modal dispatched %T, want nothing", cmd())
	}
	if m.mode != listBrowse {
		t.Fatalf("esc left mode %v, want listBrowse", m.mode)
	}
	if m.sortOrder != orderBefore {
		t.Fatalf("esc wrote %+v over %+v; esc applies nothing", m.sortOrder, orderBefore)
	}
	if got := planTitles(m.rows); !reflect.DeepEqual(got, before) {
		t.Fatalf("esc left the body as %v, want it untouched at %v", got, before)
	}
	if m.cursor != cursorBefore {
		t.Fatalf("esc left the cursor at row %d, want %d", m.cursor, cursorBefore)
	}
}

// TestASortLeavesRecentlyOpenedAloneAndSurvivesNoRestart pins both halves of
// the storage rule at once, because they are one decision:
//
//   - IT ORDERS "Your plans" ALONE. Recently opened stays in the order things
//     were opened, whatever "Your plans" is sorted by.
//   - NOT PERSISTED, and the only observable form of that is this: a second
//     ListModel over the identical plans opens on the default order. Nothing on
//     the commit path reads or writes client/config: the first TUI-side config
//     write would sit on a keystroke handler where a failure has no useful
//     remedy.
func TestASortLeavesRecentlyOpenedAloneAndSurvivesNoRestart(t *testing.T) {
	items := sortFixtureItems("Mine", 20)
	entries := []recent.Entry{{PlanID: items[3].plan.ID}, {PlanID: items[1].plan.ID}, {PlanID: items[2].plan.ID}}
	build := func() *ListModel {
		m := newRecentList(t, items, entries...)
		cur, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		return cur.(*ListModel)
	}
	m := build()
	recentBefore := planTitles(sectionContentRows(m, listRecentSectionTitle))
	mineBefore := planTitles(sectionContentRows(m, listMineSectionTitle))

	// Walk into "Your plans" and sort it by name.
	for m.cursorSection() != sectionMine {
		m, _ = pressListKey(m, "j")
	}
	m, _ = pressListKey(m, "s")
	m, _ = pressListKey(m, "j")
	m, _ = pressListKey(m, "j")
	m, _ = pressListKey(m, "enter")

	if m.sortOrder.column != sortName {
		t.Fatalf("the order is %+v, want name", m.sortOrder)
	}
	if got := planTitles(sectionContentRows(m, listMineSectionTitle)); reflect.DeepEqual(got, mineBefore) {
		t.Fatalf("sorting %q changed nothing about it", listMineSectionTitle)
	}
	if got := planTitles(sectionContentRows(m, listRecentSectionTitle)); !reflect.DeepEqual(got, recentBefore) {
		t.Fatalf("sorting %q reordered %q to %v, want %v", listMineSectionTitle, listRecentSectionTitle, got, recentBefore)
	}

	fresh := build()
	if got := fresh.sortOrder; got != (sortOrder{}) {
		t.Fatalf("a fresh list opened on %+v, want the default -- nothing persists a sort", got)
	}
	if got := planTitles(sectionContentRows(fresh, listMineSectionTitle)); !reflect.DeepEqual(got, mineBefore) {
		t.Fatalf("a fresh list drew %q as %v, want the default order %v", listMineSectionTitle, got, mineBefore)
	}
}

// TestAReSortKeepsTheCursorOnItsPlan pins that the re-sort goes through
// rebuildRows and no other rule: rebuildRows re-finds the cursor's plan by id
// in the newly ordered rows, and commitSort adds no second rule beside it.
//
// TWO CASES, and they are the two answers that one helper gives:
//
//   - THE PLAN IS STILL DRAWN. Identity wins: the cursor follows the plan to
//     wherever the new order put it, and the row index changes underneath it.
//     The fixture reverses the section outright (titles run against the activity
//     clock) in a section small enough that every plan stays on screen, so an
//     index kept across the commit would land on a different plan.
//   - THE PLAN RE-SORTED BEHIND THE SECTION'S TAIL. rebuildRows falls back to
//     keeping the row POSITION, which is a real row in a body of the same shape
//     and the same length -- deliberately NOT cursorAfterWidening's jump to the
//     tail, whose subject is a body that grew.
func TestAReSortKeepsTheCursorOnItsPlan(t *testing.T) {
	for _, tc := range []struct {
		name      string
		plans     int
		presses   int
		keepsPlan bool
	}{
		{"the plan is still drawn, so identity wins", 6, 5, true},
		// Plan 4 of the 14 the section draws, and the name order sends it to
		// position 35, behind the tail.
		{"the plan re-sorts behind the tail, so the position is kept", 40, 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newSortListOfSize(t, nil, 80, 24, tc.plans)
			for i := 0; i < tc.presses; i++ {
				m, _ = pressListKey(m, "j")
			}
			before, ok := m.selectedPlan()
			if !ok {
				t.Fatal("the cursor is on no plan before the sort")
			}
			rowBefore := m.cursor

			m, _ = pressListKey(m, "s")
			m, _ = pressListKey(m, "j") // created
			m, _ = pressListKey(m, "j") // name
			m, _ = pressListKey(m, "enter")

			after, ok := m.selectedPlan()
			if !ok {
				t.Fatalf("the cursor is on no plan after the re-sort (row %d of %d)", m.cursor, len(m.rows))
			}
			if tc.keepsPlan {
				if after.plan.ID != before.plan.ID {
					t.Fatalf("the cursor moved from %q to %q across the re-sort; rebuildRows re-finds by plan id", before.plan.ID, after.plan.ID)
				}
				if m.cursor == rowBefore {
					t.Fatalf("the cursor is still at row %d, so the order did not move this plan and the case proves nothing", rowBefore)
				}
				return
			}
			if _, drawn := m.planRowIndex(before.plan.ID); drawn {
				t.Fatalf("%q is still drawn, so this case is not the one it means to cover", before.plan.ID)
			}
			if m.cursor != rowBefore {
				t.Fatalf("the cursor left row %d for %d; with no plan to re-find, the position is what is kept", rowBefore, m.cursor)
			}
		})
	}
}

// TestTheSortKeyIsRebindableInBothDirections is what
// TestDefaultCoversEveryAction cannot be: that one proves ActSort is DECLARED
// with a default binding, and an action in AllActions with no case in Update
// passes it while doing nothing at all.
//
// BOTH DIRECTIONS, each a separate way to ship a dead gesture: the bound key
// must DISPATCH (and a rebound one only loads at all because "sort" is in
// AllActions, so the declaration is proved through the real config path), and
// the OLD key must go DEAD (which fails if the modal is opened on a literal
// keystroke somewhere instead of on the action). The help bar is checked
// alongside, since a hint naming a key the keymap no longer holds is the same
// defect one layer out.
func TestTheSortKeyIsRebindableInBothDirections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keymap.json")
	if err := os.WriteFile(path, []byte(`{"sort": "S"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rebound, err := keymap.Load(path)
	if err != nil {
		t.Fatalf("keymap.Load rejected a sort rebind: %v -- ActSort is not in AllActions", err)
	}

	for _, tc := range []struct {
		name  string
		km    keymap.Map
		opens string
		dead  string
	}{
		{"the default binding", keymap.Default(), "s", ""},
		{"a rebound one", rebound, "S", "s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build := func() *ListModel {
				m := NewList(nil, tc.km, nil)
				m.width, m.height = 80, 24
				m.applyRefresh(listSectionItems("Mine", 3))
				return m
			}

			m, cmd := pressListKey(build(), tc.opens)
			if cmd != nil {
				t.Fatalf("%q dispatched %T, want nothing", tc.opens, cmd())
			}
			if m.mode != listSort {
				t.Fatalf("%q left mode %v, want listSort -- the action is declared but never dispatched", tc.opens, m.mode)
			}
			if hint := m.hintLine(); !strings.Contains(hint, tc.opens+" sort") {
				t.Fatalf("the browse hint = %q, want it to name %q as the sort key", hint, tc.opens)
			}

			if tc.dead == "" {
				return
			}
			m, cmd = pressListKey(build(), tc.dead)
			if cmd != nil || m.mode != listBrowse {
				t.Fatalf("the rebound-away %q still did something (mode %v, cmd %v)", tc.dead, m.mode, cmd != nil)
			}
		})
	}
}

// TestTheSortCursorClampsAtTheLastOption drives updateSort's clamp (j beyond
// the last option does nothing), the KEYBOARD-LEVEL proof that the modal offers
// three keys. THE WANTED CLAMP IS A LITERAL, not len(sortColumns) read a second
// time: deriving "want" from the same slice updateSort itself reads would make
// this pass on ANY count the two agreed on, including a wrong one.
func TestTheSortCursorClampsAtTheLastOption(t *testing.T) {
	m := newSortList(t, nil, 80, 24)
	m, _ = pressListKey(m, "s")
	for i := 0; i < 8; i++ { // walk well past the last option
		m, _ = pressListKey(m, "j")
	}
	if m.sortCursor != 2 {
		t.Fatalf("the cursor clamped at %d, want 2 (three options)", m.sortCursor)
	}
}

// TestTheOpenSortModalSurvivesAResizeAndARefresh pins the two transitions that
// reach the list while the modal holds the keyboard: a tea.WindowSizeMsg
// rebuilds every row REGARDLESS OF MODE (Update's WindowSizeMsg arm runs ahead
// of the mode switch), and msgStateChanged DEFERS while the modal is open ("THE
// SORT MODAL DEFERS TOO", Update's msgStateChanged case). Neither may move the
// modal's cursor or write an order the reader has not chosen -- rebuildRows only
// ever READS m.sortOrder, through sortedItems -- and this pins that as a
// property of the RUNNING PROGRAM, not only of the source.
func TestTheOpenSortModalSurvivesAResizeAndARefresh(t *testing.T) {
	m := newSortList(t, nil, 80, 24)
	m, _ = pressListKey(m, "s")
	m, _ = pressListKey(m, "j")
	m, _ = pressListKey(m, "j") // onto name, the third row
	if got := sortColumns[m.sortCursor]; got != sortName {
		t.Fatalf("test setup: the modal's cursor is on %v, want it on name", got)
	}

	cur, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = cur.(*ListModel)
	if cmd != nil {
		t.Fatalf("a resize dispatched %T, want nothing", cmd())
	}
	cur, cmd = m.Update(msgStateChanged{})
	m = cur.(*ListModel)
	if cmd != nil {
		t.Fatalf("a state change with the sort modal open dispatched %T, want it deferred", cmd())
	}
	if !m.pendingRefresh {
		t.Fatal("the state change was neither applied nor remembered")
	}
	if m.mode != listSort || sortColumns[m.sortCursor] != sortName {
		t.Fatalf("the transitions left mode = %v with the cursor on %v; the open modal must survive untouched", m.mode, sortColumns[m.sortCursor])
	}
	if m.sortOrder != (sortOrder{}) {
		t.Fatalf("the transitions wrote %+v, an order nobody chose", m.sortOrder)
	}

	m, _ = pressListKey(m, "enter")
	if got, want := m.sortOrder, (sortOrder{sortName, sortAscending}); got != want {
		t.Fatalf("committing after the transitions gave %+v, want %+v", got, want)
	}
}

// TestThePaintedSortModalPaintsEveryCellOfItsOwnBox is the PAINT half of "the
// modal paints its own padding". WIDTH is the gate test's per-row check (unpad
// a modal row and the box goes ragged); PAINT is this one, because a row can be
// full width and still leave cells the terminal draws in its OWN default
// background -- through which the list shows.
//
// ui.Overlay is a pure byte splice with no notion of a box, so anything the
// modal does not paint is not painted at all. That is why every fragment,
// BORDER CELLS INCLUDED, goes through a style carrying its own background: a
// nested lipgloss.Render ends in a full SGR reset, so an unstyled fragment
// after one shows the terminal's default rather than the modal's.
//
// hasUnpaintedGap (app/painted.go) is the detector the compose path already
// uses for exactly this failure, reused rather than re-invented -- and its own
// operating invariant (every style on the path carries a background) is
// separately guarded by TestComposePaintStylesAlwaysCarryBackground.
//
// THE WHOLE COMPOSITE IS SWEPT, not the box alone, so the splice is covered
// too: a background row that lost its paint under the box would fail here
// exactly as an unpainted border cell does.
func TestThePaintedSortModalPaintsEveryCellOfItsOwnBox(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []struct{ w, h int }{{listMinWidth, listMinHeight}, {80, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			m, _ := pressListKey(newSortList(t, th, size.w, size.h), "s")
			frame := m.View().Content
			box := strings.Split(ansi.Strip(m.sortModal()), "\n")
			top := (size.h - len(box)) / 2

			rows := strings.Split(frame, "\n")
			for i, row := range rows {
				if hasUnpaintedGap(row) {
					t.Fatalf("row %d of the composite has a run of cells with no background active: %q", i, row)
				}
			}
			// The sweep above passes on a frame with no modal in it at all, so the
			// box has to be there and its own rows are checked by name.
			for i, want := range box {
				if got := ansi.Strip(rows[top+i]); !strings.Contains(got, want) {
					t.Fatalf("frame row %d is %q and does not carry the modal's own row %q", top+i, got, want)
				}
			}
			// And the box on its own, BEFORE the splice, which is where the
			// border cells live: a bare "│ " concatenated in front of a styled
			// interior leaves printable content with no escape ahead of it, and
			// that is the whole of what this detector reports.
			for i, row := range strings.Split(m.sortModalPainted(), "\n") {
				if hasUnpaintedGap(row) {
					t.Fatalf("box row %d has a run of cells with no background active: %q", i, row)
				}
			}
		})
	}
}

// TestTheSortModalBorderIsOneColourAllTheWayRound is the sort box catching up
// with the correction the CENTRED panel already had, and it is the same
// sentence: a border shouting with the headline leaves nothing quieter for it
// to be louder THAN (TestTheCentredPanelBorderIsOneColourAndItsHeadIsWarn,
// app/sourcefault_test.go).
//
// sortModalPainted drew its TOP AND BOTTOM RULES through st.FocusHeader and its
// SIDES through st.Chrome, so the rectangle came out in two colours -- a lid
// and a floor in the headline's own ink standing on posts in the body's -- and
// the title inside it was then the third thing on screen wearing the loudest
// colour in the box. THE SIDES WERE NEVER WRONG; the rules were, and the fix
// moves the rules onto the sides rather than the sides onto the rules.
//
// THE HEAD COLOUR IS COUNTED ACROSS THE WHOLE BOX rather than merely asserted
// absent from the two rules, because "the border is quiet" and "the title is
// still loud" are one claim: split, a fix that also greyed the title would pass
// the half that was failing and take the point of it away.
//
// IT IS DRIVEN IN BOTH PRESETS. The two styles hold different hexes in each
// preset, and a box reading the right token in one and the wrong one in the
// other is the exact shape of what is being removed.
func TestTheSortModalBorderIsOneColourAllTheWayRound(t *testing.T) {
	for _, name := range []string{"dark", "light"} {
		th, err := theme.Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		st := ui.NewStyles(th)
		chromeFG, headFG := paintedFG(st.Chrome.Render("x")), paintedFG(st.FocusHeader.Render("x"))
		// Without this the walk below passes on a palette spending one colour
		// on both roles, and would go on passing with the rules back on
		// FocusHeader.
		if chromeFG == "" || headFG == "" || chromeFG == headFG {
			t.Fatalf("%s: Chrome ink %q, panel-head ink %q -- the two must differ or this test asserts nothing", name, chromeFG, headFG)
		}
		t.Run(name, func(t *testing.T) {
			m := newSortList(t, th, 80, 24)
			rows := strings.Split(m.sortModalPainted(), "\n")
			if len(rows) < 3 {
				t.Fatalf("the box is %d rows, want a rule, a title and a rule at the very least", len(rows))
			}
			// The title has to actually BE on row 1, or "the head colour is on
			// exactly one row" pins whatever else landed there.
			if got := ansi.Strip(rows[1]); !strings.Contains(got, listMineSectionTitle) {
				t.Fatalf("box row 1 is %q, want the modal's title -- this is reading the wrong row", got)
			}
			for _, i := range []int{0, len(rows) - 1} {
				if got := paintedFG(rows[i]); got != chromeFG {
					t.Fatalf("the rule at row %d is inked %q, want Chrome's %q -- the border is one colour and it is the sides' one: %q",
						i, got, chromeFG, ansi.Strip(rows[i]))
				}
			}
			for i, row := range rows {
				if got, want := strings.Contains(row, headFG), i == 1; got != want {
					t.Fatalf("box row %d %s the head colour, want it on the title and nowhere else: %q",
						i, map[bool]string{true: "carries", false: "does not carry"}[got], ansi.Strip(row))
				}
			}
		})
	}
}

// TestTheSortModalTakesOnlyTheKeysItNames is the wrong-hint rule for listSort:
// the bar must say what the box says and nothing else.
//
// THE HELP BAR AND THE BOX'S FOOTER ARE ONE CONSTANT, checked by identity
// rather than by spelling, so a reworded footer can never leave the bar
// advertising the old words. Falling back to hintLine() there -- the mutation
// that used to leave the suite green -- puts five browse keys on screen while
// the modal has claimed the keyboard: the defect is not a missing hint, it is
// a wrong one. Silence is fine; misinformation is not.
//
// THE KEYS ARE DRIVEN, not merely absent from the string. Each browse gesture
// the bar no longer names is pressed inside the modal and must leave it
// standing: naming a key is one defect, and taking a key you did not name is
// the other.
func TestTheSortModalTakesOnlyTheKeysItNames(t *testing.T) {
	m, _ := pressListKey(newSortList(t, nil, 80, 24), "s")

	if got, want := m.helpBarLine(), listSortHint; got != want {
		t.Fatalf("helpBarLine() in the sort modal = %q, want the box's own footer %q", got, want)
	}
	if got := m.helpBarLine(); got == m.hintLine() {
		t.Fatalf("the sort help bar is browse's own line %q; every key on it but enter is dead while the box is open", got)
	}
	for _, dead := range []string{"rename", "delete", "filter", "open"} {
		if strings.Contains(m.helpBarLine(), dead) {
			t.Fatalf("the sort help bar advertises %q, which the modal does not take: %q", dead, m.helpBarLine())
		}
	}
	for _, want := range []string{"enter apply", "esc cancel"} {
		if !strings.Contains(m.helpBarLine(), want) {
			t.Fatalf("the sort help bar never says %q: %q", want, m.helpBarLine())
		}
	}
	// The bar is what the frame actually shows, not just what the method
	// returns -- View clips it, and a mode routed to the wrong arm would show
	// the wrong words however this method answered.
	if !strings.Contains(ansi.Strip(m.View().Content), listSortHint) {
		t.Fatalf("the drawn frame's help bar does not carry %q", listSortHint)
	}

	// e/d//: named by browse, dead here, and each must leave the box standing.
	rowsBefore := len(m.rows)
	for _, key := range []string{"e", "d", "/"} {
		var cmd tea.Cmd
		m, cmd = pressListKey(m, key)
		if m.mode != listSort || cmd != nil {
			t.Fatalf("%q in the sort modal left mode = %v with cmd %v, want listSort and nothing", key, m.mode, cmd != nil)
		}
	}
	if m.filterActive() {
		t.Fatalf("%q inside the modal started a filter query: %q", "/", m.filter)
	}
	if len(m.rows) != rowsBefore {
		t.Fatalf("a dead key rebuilt the body (%d rows, was %d)", len(m.rows), rowsBefore)
	}
}

// TestEveryListModeDeclaresItsOwnHelpBar is the counterpart of
// TestEveryModeDeclaresItsOwnHelpBar. helpBarLine's default was hintLine(),
// and four of the six modes below had each added a special case against it,
// one at a time, as they were written.
//
// THE SET IS DRIVEN FROM THE ENUM: a list typed out here goes stale every time
// a mode is added, while listModeCount moves with the constants above it.
//
// listRename is the mode this test exists to have caught: it takes ctrl+d and
// esc and nothing else, and it drew browse's six keys in a mode whose textarea
// had already claimed every printable one of them.
// ⚠️ "?" STOPPED BEING ONLY A MISS MARKER: it is ActKeys'
// own default binding, so browse's bar (its trailing "? keys") and listKeys'
// own (its "esc/? close") now carry a LEGITIMATE "?" apiece -- see the
// review model's identical note on TestEveryModeDeclaresItsOwnHelpBar
// (app_test.go), whose fix this mirrors exactly.
func TestEveryListModeDeclaresItsOwnHelpBar(t *testing.T) {
	m := newFilterList(t)
	browse := m.hintLine()
	keysKey := findKey(m.km, keymap.ActKeys)
	for lm := listMode(0); lm < listModeCount; lm++ {
		m.mode = lm
		got := m.helpBarLine()
		checkedForMiss := got
		switch lm {
		case listBrowse:
			checkedForMiss = strings.TrimSuffix(checkedForMiss, keysKey+" keys · "+findKey(m.km, keymap.ActQuit)+" quit")
		case listKeys:
			checkedForMiss = strings.ReplaceAll(checkedForMiss, "esc/"+keysKey, "")
		}
		switch {
		case got == "":
			t.Errorf("list mode %d draws helpHint's silent default: nobody declared what its keys are", lm)
		case strings.Contains(checkedForMiss, "?"):
			t.Errorf("list mode %d's help bar %q carries findKey's miss marker -- a literal key looked up as an action", lm, got)
		case lm != listBrowse && got == browse:
			t.Errorf("list mode %d shows browse's own line %q while it owns the keyboard", lm, got)
		}
	}
	for lm := range listModeHints {
		if lm < 0 || lm >= listModeCount {
			t.Errorf("listModeHints declares a hint for %d, which is not a list mode", lm)
		}
	}

	// The mechanism's default, pinned the only way it can be: listModeCount is
	// not a mode and no call site produces it, so a browse fallback here would
	// pass every assertion above and still hand the seventh list mode a wrong
	// line on the day it is added.
	m.mode = listModeCount
	if got := m.helpBarLine(); got != "" {
		t.Fatalf("an unregistered list mode's help bar = %q, want silence", got)
	}
}

// TestRenameTakesOnlyTheKeysItNames is the wrong-hint rule for listRename --
// the shape TestExpandedTakesOnlyTheKeysItNames and
// TestTheSortModalTakesOnlyTheKeysItNames already hold their own modes to.
//
// THE BAR AND THE PANEL'S STRIP ARE ONE CONSTANT, checked by identity rather
// than spelling, so a reworded strip cannot leave the bar naming the old keys.
// And the keys are DRIVEN: every browse gesture the bar no longer names is
// pressed inside the rename, where the textarea takes it as a character -- so
// the mode is still standing and the draft has grown by exactly that character.
func TestRenameTakesOnlyTheKeysItNames(t *testing.T) {
	m := newLoadedList(t, 9)
	m.cursor = m.firstSelectableRow()
	m, _ = pressListKey(m, "e")
	if m.mode != listRename {
		t.Fatalf("test setup: mode = %v, want listRename", m.mode)
	}

	if got, want := m.helpBarLine(), renameHint; got != want {
		t.Fatalf("helpBarLine() in rename = %q, want the panel's own strip %q", got, want)
	}
	for _, dead := range []string{"rename", "delete", "filter", "sort", "quit"} {
		if strings.Contains(m.helpBarLine(), dead) {
			t.Fatalf("the rename help bar advertises %q, which the textarea has claimed: %q", dead, m.helpBarLine())
		}
	}
	if !strings.Contains(ansi.Strip(m.View().Content), renameHint) {
		t.Fatalf("the drawn frame's help bar does not carry %q", renameHint)
	}

	// The cmd each keypress hands back is the textarea's own cursor-blink timer
	// and is deliberately dropped rather than drained: draining it would make
	// this test wait on a clock.
	for _, dead := range []string{"d", "/", "s", "q"} {
		before := m.ta.Value()
		m, _ = pressListKey(m, dead)
		if m.mode != listRename {
			t.Fatalf("%q in the rename panel left mode = %v, want listRename", dead, m.mode)
		}
		if got, want := m.ta.Value(), before+dead; got != want {
			t.Fatalf("%q in the rename panel left the draft %q, want %q -- it was read as a key, not a character", dead, got, want)
		}
	}
}

// TestTheSortTieBreakIsTitleAscendingInEveryColumnAndDirection pins the one
// line of sortSectionItems that had no test: the fallback comparison when the
// chosen column cannot tell two rows apart. Deleting it (`return false`) left
// the whole suite green.
//
// TWO PROPERTIES, and the second is the one a swapped-argument implementation
// of "ascending" gets wrong:
//
//  1. A TIE FALLS BACK TO TITLE ASCENDING rather than to whatever order the
//     load happened to deliver. Without it SliceStable keeps arrival order, so
//     the same plans re-sorted from two different loads come out differently.
//  2. IT IS ASCENDING IN BOTH DIRECTIONS. Reversing the whole comparator --
//     the obvious way to write "ascending" -- reverses the tie-break too, so
//     flipping the arrow would reorder rows whose sort key never differed at
//     all. Here the four tied rows must keep the SAME relative order under both
//     directions of the same column.
func TestTheSortTieBreakIsTitleAscendingInEveryColumnAndDirection(t *testing.T) {
	// Every row shares one activity instant and one creation instant, so
	// `updated` and `created` can only answer by the tie-break; the titles
	// arrive in reverse order, so arrival order and title order disagree.
	tied := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	items := make([]planItem, 0, 4)
	for _, title := range []string{"delta", "charlie", "bravo", "alpha"} {
		items = append(items, planItem{
			plan:         domain.Plan{ID: domain.PlanID(title), Title: title},
			lastActivity: tied,
			created:      tied,
		})
	}
	sorted := []string{"alpha", "bravo", "charlie", "delta"}

	for _, column := range []sortColumn{sortUpdated, sortCreated} {
		for _, direction := range []sortDirection{sortDescending, sortAscending} {
			t.Run(fmt.Sprintf("%s %s", column.label(), direction.arrow()), func(t *testing.T) {
				m := NewList(nil, keymap.Default(), nil)
				m.width, m.height = 80, 24
				m.sortOrder = sortOrder{column: column, direction: direction}
				m.applyRefresh(items)

				got := planTitles(sectionContentRows(m, listMineSectionTitle))
				if !reflect.DeepEqual(got, sorted) {
					t.Fatalf("four rows tied on %s drew %v, want %v -- a tie falls back to Title ASCENDING, the same way in both directions",
						column.label(), got, sorted)
				}
			})
		}
	}
}

// sgrRGB converts "#rrggbb" to the "r;g;b" form used in SGR truecolor
// params, so a role assertion can be derived from the live preset rather
// than hardcoding an escape that goes stale the moment the palette moves.
func sgrRGB(t *testing.T, hex string) string {
	t.Helper()
	var r, g, b int
	if _, err := fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b); err != nil {
		t.Fatalf("bad hex %q: %v", hex, err)
	}
	return fmt.Sprintf("%d;%d;%d", r, g, b)
}

// TestListMastheadAndCursorBandCarryBrand pins the role split in the plan
// list. The masthead names the product and the cursor band marks position, so
// both take Brand -- not the Accent the ◐ state glyph uses, which is the
// overload the split exists to break. The two were literally one style, so
// this also guards their having been separated rather than merely recoloured
// together. IT ALSO PINS THE MASTHEAD BOX'S BOLD, off, at both presets --
// viewPainted's own masthead-block comment has the full account of why.
func TestListMastheadAndCursorBandCarryBrand(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}

	// The masthead BOX's own ink -- Brand, and NOT bold (viewPainted's own
	// masthead-block comment records why: the wordmark's weight was fixed by
	// turning bold off, and the border was left bold-off too for one consistent
	// style, even though that half is close to invisible on most terminals).
	// Driven at both presets, since Brand's hex differs between them and a
	// single-theme check could pass on the wrong constant by coincidence.
	for _, name := range []string{"dark", "light"} {
		t.Run(name, func(t *testing.T) {
			tm, err := theme.Lookup(name)
			if err != nil {
				t.Fatal(err)
			}
			mp := NewList(nil, keymap.Default(), tm)
			mp.width, mp.height = 80, 24
			rows := strings.Split(mp.View().Content, "\n")
			// rows[0] is the top ground row; rows[1] is the box's own top
			// border -- carrying the same style as the sigil-and-name row
			// beneath it, since one style paints all three box rows.
			masthead := rows[1]
			if want := "38;2;" + sgrRGB(t, tm.Brand); !strings.Contains(masthead, want) {
				t.Fatalf("masthead box missing the Brand foreground: %q", masthead)
			}
			if hasBoldSGR(masthead) {
				t.Fatalf("masthead box is bold, want the border and wordmark plain: %q", masthead)
			}
		})
	}

	m := newSortListOfSize(t, th, 100, 24, 3)
	cursor, ok := m.cursorRow()
	if !ok {
		t.Fatal("fixture has no cursor row")
	}
	rows := strings.Split(m.View().Content, "\n")

	// rows[1]: rows[0] is the top ground row.
	masthead := rows[1]
	if want := "38;2;" + sgrRGB(t, th.Brand); !strings.Contains(masthead, want) {
		t.Fatalf("masthead row missing the Brand foreground: %q", masthead)
	}
	if stale := "38;2;" + sgrRGB(t, th.Accent); strings.Contains(masthead, stale) {
		t.Fatalf("masthead row still carries Accent, the colour it shared with state: %q", masthead)
	}

	// The band is a Brand ▌ on the row's own background now, not a cell of
	// Brand background, so the glyph is what locates it -- and the glyph
	// rather than the colour, because the masthead above carries Brand too
	// and a colour-only sweep would count it. The count alone is not enough
	// either: a band on exactly one WRONG row satisfies it, so the row is
	// identified by the cursor's own title below.
	brandFG := "38;2;" + sgrRGB(t, th.Brand)
	var banded []string
	for _, row := range rows {
		if strings.Contains(ansi.Strip(row), listCursorGlyph) {
			if !strings.Contains(row, brandFG) {
				t.Fatalf("the cursor band is not Brand: %q", row)
			}
			banded = append(banded, ansi.Strip(row))
		}
	}
	if len(banded) != 1 {
		t.Fatalf("rows carrying the Brand cursor band = %d, want exactly the cursor's 1", len(banded))
	}
	// row.item is the zero planItem on every kind but rowPlan, and rowMore is
	// selectable too -- so a cursor resting on a "+N more" row gives an empty
	// title, which strings.Contains satisfies unconditionally and which would
	// quietly return this assertion to "some row somewhere carries a band".
	title := cursor.item.plan.Title
	if title == "" {
		t.Fatalf("cursor rests on a %v row with no title; the identity check below would be vacuous", cursor.kind)
	}
	if !strings.Contains(banded[0], title) {
		t.Fatalf("band landed on %q, not on the cursor's row %q", banded[0], title)
	}

	// A SPACE between the band and the state glyph, on cursor and non-cursor
	// rows alike so the glyph keeps one column. The band was a single cell hard
	// against the glyph, and nothing caught narrowing it back, since every
	// other assertion here is about colour and placement rather than width.
	if got := strings.TrimPrefix(ansi.Strip(banded[0]), strings.Repeat(" ", m.listMargin())); !strings.HasPrefix(got, listCursorGlyph+" ") {
		t.Fatalf("cursor row starts %q, want the band glyph and a space", got[:min(6, len(got))])
	}
	for _, row := range rows {
		plain := strings.TrimPrefix(ansi.Strip(row), strings.Repeat(" ", m.listMargin()))
		if !strings.Contains(plain, "Mine Plan") || strings.Contains(plain, listCursorGlyph) {
			continue
		}
		// A LITERAL two, not listBandWidth: an expectation derived from the
		// same constant the code reads moves with it and pins nothing, which
		// is how narrowing the band back to one column first went unnoticed.
		if !strings.HasPrefix(plain, "  ") || plain[2] == ' ' {
			t.Fatalf("non-cursor row starts %q, want two blank band columns then the state glyph", plain[:min(6, len(plain))])
		}
		break
	}
}

// TestListSectionAndLabelsPartCompany pins three treatments where there were
// two, separated now by a COLOUR as well as by weight.
//
// A section's name is Accent and bold. Its column-label row is Dim and not. The
// plan titles between them keep Text. Both chrome rows bold would make them
// identical to each other AND heavier than the plan titles they label -- NAME
// COMMENTS PUB outranking the plans, backwards for a row that describes them.
// row.head is what tells the two apart.
//
// WEIGHT WAS ONCE THE WHOLE ASSERTION, because both rows were the same colour.
// Recently opened made three sections, and a name a reader has to find
// three times on one screen earns an ink of its own.
//
// THE BOLD IS STILL ASSERTED BESIDE IT, and that is not belt-and-braces:
// dropping the weight once the colour landed was a live option -- three
// coloured names is a lot of ink -- and it was driven on a real terminal and
// turned down. A test that let the bold go would let that be reversed by
// accident instead of on purpose. The negative halves matter for the older
// reason: a change painting EVERY chrome row Accent satisfies "the name is
// Accent" while destroying the distinction the ink was spent on.
//
// IT DRIVES BOTH PRESETS, because the ink is the claim and Accent is a
// different hue in each -- gold on dark, mauve on light. A dark-only check
// cannot see a light preset whose section name went somewhere else.
func TestListSectionAndLabelsPartCompany(t *testing.T) {
	for _, preset := range []string{"dark", "light"} {
		th, err := theme.Lookup(preset)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(preset, func(t *testing.T) { testSectionAndLabelsPartCompany(t, th) })
	}
}

func testSectionAndLabelsPartCompany(t *testing.T, th *theme.Theme) {
	m := newSortListOfSize(t, th, 100, 24, 3)
	rows := strings.Split(m.View().Content, "\n")

	find := func(want string) string {
		t.Helper()
		for _, r := range rows {
			if strings.Contains(ansi.Strip(r), want) {
				return r
			}
		}
		t.Fatalf("no row containing %q in:\n%s", want, m.View().Content)
		return ""
	}
	// listMineSectionTitle, not the fixture's "Mine" prefix -- that appears in
	// every plan TITLE in this section and matches a plan row first.
	section, labels := find(listMineSectionTitle), find("NAME")
	if section == labels {
		t.Fatal("the section name and its column labels are the same row; this test cannot tell them apart")
	}
	dim := "38;2;" + sgrRGB(t, th.Dim)
	accent := "38;2;" + sgrRGB(t, th.Accent)
	if accent == dim {
		t.Fatalf("Accent and Dim are both %q in this preset; the two rows cannot be told apart by ink and this test asserts half of nothing", dim)
	}
	for _, tc := range []struct {
		name, row string
		wantInk   string
		notInk    string
		wantBold  bool
	}{
		{"the section name is Accent and bold", section, accent, dim, true},
		{"its column labels are Dim and not", labels, dim, accent, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.row, tc.wantInk) {
				t.Fatalf("row does not carry the ink %q: %q", tc.wantInk, tc.row)
			}
			if strings.Contains(tc.row, tc.notInk) {
				t.Fatalf("row carries the OTHER chrome row's ink %q too; the two parting company is the whole point: %q", tc.notInk, tc.row)
			}
			if got := hasBoldSGR(tc.row); got != tc.wantBold {
				t.Fatalf("row bold = %v, want %v -- the colour replaced the weight's job here and did not inherit it: %q", got, tc.wantBold, tc.row)
			}
		})
	}
	// And the plan titles they sit above are NOT dim, or the whole list is one
	// shade and the chrome no longer recedes from anything.
	if title := find("Mine Plan"); strings.Contains(title, dim+"m") {
		t.Fatalf("a plan title is dim, so the chrome recedes from nothing: %q", title)
	}
}

// hasBoldSGR reports whether any SGR sequence in s turns bold on. Parameter 1
// can lead a sequence or ride inside one, so a substring match for "1" would
// hit every truecolor triple that happens to contain it.
func hasBoldSGR(s string) bool {
	for _, seq := range ansiSGR.FindAllString(s, -1) {
		params := strings.Split(strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m"), ";")
		for i := 0; i < len(params); i++ {
			switch params[i] {
			case "1":
				return true
			case "38", "48":
				if i+1 < len(params) && params[i+1] == "2" {
					i += 4
				} else if i+1 < len(params) && params[i+1] == "5" {
					i += 2
				}
			}
		}
	}
	return false
}

// TestHomeRelative pins the abbreviation the SOURCE column depends on, and
// most of its cases are about what must NOT be abbreviated. The column spends
// thirteen of its twenty-eight cells on "/Users/alice/" without this, which is
// why it exists; a wrong answer here renames a directory in front of a reader
// who is using the column to find a file.
func TestHomeRelative(t *testing.T) {
	const home = "/Users/alice"

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"a path under home is abbreviated", home + "/plans/x.md", "~/plans/x.md"},
		{"nesting is preserved", home + "/development/draftplane/README.md", "~/development/draftplane/README.md"},
		{"the home directory itself", home, "~"},

		// THE BOUNDARY CASE, and the reason the match is on home+separator
		// rather than on home: this path shares every character of home and
		// is not under it. "~2/x.md" would name a directory that does not
		// exist, in the one column a reader consults to locate a file.
		{"a sibling whose name merely starts with home's", "/Users/alice2/x.md", "/Users/alice2/x.md"},
		{"a longer sibling", "/Users/alicefoo/plans/x.md", "/Users/alicefoo/plans/x.md"},

		{"a path outside home is untouched", "/etc/draftplane/x.md", "/etc/draftplane/x.md"},
		{"a relative path is untouched", "plans/x.md", "plans/x.md"},
		{"a URL source is not a path at all", "notion://abc123", "notion://abc123"},
		{"empty stays empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", home)
			if got := homeRelative(tt.in); got != tt.want {
				t.Errorf("homeRelative(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestHomeRelativeWithoutAHomeDirectory pins the failure this must not turn
// into a broken row: os.UserHomeDir errors when $HOME is unset, and the answer
// is the path unchanged rather than an empty cell or a panic.
func TestHomeRelativeWithoutAHomeDirectory(t *testing.T) {
	t.Setenv("HOME", "")
	const path = "/Users/alice/plans/x.md"
	if got := homeRelative(path); got != path {
		t.Errorf("homeRelative(%q) = %q with no $HOME, want it unchanged", path, got)
	}
}

// TestSourceLabelNeverExceedsItsBudget is the property this column's own
// history says to pin: sourceLabel is, in its doc comment's words, "the site
// that made the frame overflow", because a cell one wider than its budget
// wraps the whole list. A wide-character path is the case a width-in-runes
// assumption gets wrong.
func TestSourceLabelNeverExceedsItsBudget(t *testing.T) {
	paths := []string{
		"/Users/alice/plans/quarterly-planning-and-followups.md",
		"/Users/alice/計画/四半期計画と追跡調査.md",
		"/Users/alice/plans/🙂🙂🙂🙂🙂🙂🙂🙂🙂🙂.md",
		"notion://abc123",
	}
	for _, p := range paths {
		for width := 1; width <= 40; width++ {
			it := planItem{plan: domain.Plan{ID: "l_w", SourceHint: p}}
			got := it.sourceLabel(width, lipgloss.NewStyle())
			if w := ansi.StringWidth(got); w > width {
				t.Fatalf("sourceLabel(%d) on %q = %q, which is %d cells wide", width, p, got, w)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// The first load.
// ---------------------------------------------------------------------------

// deliverList hands each message to Update in order and DROPS the cmd each one
// returns, so a test delivers exactly the answers it is about.
func deliverList(m *ListModel, msgs ...tea.Msg) *ListModel {
	for _, msg := range msgs {
		cur, _ := m.Update(msg)
		m = cur.(*ListModel)
	}
	return m
}

// sectionBodyText is what a section's body says, row by row: a plan row's
// title, or a hint row's words with the indent trimmed -- so a section still
// awaiting its first answer reads as exactly one "".
func sectionBodyText(m *ListModel, section listSection) []string {
	var out []string
	for _, r := range sectionContentRows(m, section.title()) {
		if r.kind == rowPlan {
			out = append(out, r.item.plan.Title)
			continue
		}
		out = append(out, strings.TrimSpace(r.text))
	}
	return out
}

// TestTheFirstAnswerOfAnyKindEndsTheWait pins what happens until Init's one
// load answers: the list claims nothing -- "No plans yet!" least of all, and no
// "0 plans" on the bar -- and its FIRST answer, whatever kind it is, ends the
// wait: the plans, or an error said on the bar over the hint the empty list
// owes.
func TestTheFirstAnswerOfAnyKindEndsTheWait(t *testing.T) {
	alpha := domain.Plan{ID: "l_alpha", Title: "Alpha rollout", OpenThreads: 1, TotalThreads: 1, LastActivityAt: listFixtureEpoch}
	beta := domain.Plan{ID: "l_beta", Title: "Beta cleanup", LastActivityAt: listFixtureEpoch.Add(-time.Minute)}
	for _, tc := range []struct {
		name     string
		svc      *flakyListSvc
		wantMine []string
		wantBar  string
	}{
		{
			name:     "plans",
			svc:      &flakyListSvc{plans: []domain.Plan{beta, alpha}},
			wantMine: []string{"Alpha rollout", "Beta cleanup"},
			wantBar:  "2 plans · 1 with open threads",
		},
		{
			name:     "an error",
			svc:      &flakyListSvc{err: errors.New("state.json: permission denied")},
			wantMine: []string{listEmptyHint},
			wantBar:  "0 plans · error: state.json: permission denied",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewList(tc.svc, keymap.Default(), nil)
			cmd := m.Init()

			if got := sectionBodyText(m, sectionMine); !slices.Equal(got, []string{""}) {
				t.Fatalf("before the answer, Your plans = %q, want one blank row", got)
			}
			if bar := strings.TrimSpace(ansi.Strip(m.statusBarText(200, m.styles.StatusBar))); bar != "" {
				t.Fatalf("before the answer, the status bar = %q, want no count at all", bar)
			}

			answer, ok := cmd().(msgListRefreshed)
			if !ok {
				t.Fatalf("Init produced %T, want its one load", answer)
			}
			m = deliverList(m, answer)

			if got := sectionBodyText(m, sectionMine); !slices.Equal(got, tc.wantMine) {
				t.Fatalf("Your plans = %q, want %q", got, tc.wantMine)
			}
			if bar := ansi.Strip(m.statusBarText(200, m.styles.StatusBar)); !strings.HasPrefix(bar, tc.wantBar) {
				t.Fatalf("status bar = %q, want it to start %q", bar, tc.wantBar)
			}
		})
	}
}

// TestAFilterLeavesAWaitingSectionBlank: a query typed before the first load
// answers must not have "Your plans" answer for it. The filter's hint says the
// query emptied the section, and a section with no answer yet was emptied by
// nothing. The same query once the load has landed is the positive control.
func TestAFilterLeavesAWaitingSectionBlank(t *testing.T) {
	m := NewList(&flakyListSvc{plans: freshestFirstPlans(3)}, keymap.Default(), nil)
	cmd := m.Init()

	m, _ = pressListKey(m, "/")
	m = typeFilterQuery(t, m, "zzz")
	if got := sectionBodyText(m, sectionMine); !slices.Equal(got, []string{""}) {
		t.Fatalf("Your plans under the filter = %q, want one blank row -- it has not answered yet", got)
	}

	m = deliverList(m, cmd())
	if got, want := sectionBodyText(m, sectionMine), []string{`no plans match "zzz"`}; !slices.Equal(got, want) {
		t.Fatalf("Your plans under the filter once it answered = %q, want %q", got, want)
	}
}

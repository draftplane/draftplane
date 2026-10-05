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

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/client/localfs"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/keymap"
	"github.com/draftplane/draftplane/session"
)

// pressRoot mirrors press/pressList for *Root: keys are dispatched through
// Root.Update, which forwards to whichever child is currently active. Like them
// it discards the returned cmd — callers that need to drain a key's effect
// (enter's msgOpenPlan, an embedded q's msgCloseReview) call r.Update directly
// and drainRoot the result instead.
func pressRoot(r *Root, keys ...string) *Root {
	cur := tea.Model(r)
	for _, k := range keys {
		var key tea.KeyPressMsg
		switch k {
		case "enter":
			key = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "tab":
			key = tea.KeyPressMsg{Code: tea.KeyTab}
		case "ctrl+d":
			key = tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
		default:
			key = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		cur, _ = cur.Update(key)
	}
	return cur.(*Root)
}

// drainRoot mirrors drain/drainList for *Root.
func drainRoot(t *testing.T, r *Root, cmd tea.Cmd) *Root {
	t.Helper()
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			break
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				r = drainRoot(t, r, c)
			}
			break
		}
		cur, next := r.Update(msg)
		r = cur.(*Root)
		cmd = next
	}
	return r
}

// openViaEnter drives the real enter-key path: dispatches enter to the
// (assumed-active) list, then drains everything it sets off — msgOpenPlan's
// open cmd, msgSessionOpened's switch, and the incoming review model's
// Init() — leaving r with review active and fully refreshed.
func openViaEnter(t *testing.T, r *Root) *Root {
	t.Helper()
	cur, cmd := r.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return drainRoot(t, cur.(*Root), cmd)
}

// closeViaQuit drives the real embedded-quit path: dispatches q to the
// (assumed-active) review model and drains msgCloseReview's effects.
func closeViaQuit(t *testing.T, r *Root) *Root {
	t.Helper()
	cur, cmd := r.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("q while review is embedded must return the msgCloseReview cmd")
	}
	return drainRoot(t, cur.(*Root), cmd)
}

// showListViaL drives the real l-key path: dispatches l to the
// (assumed-active) review model and drains msgCloseReview's effects — the same
// machinery closeViaQuit exercises for q, but reachable regardless of whether
// the review is embedded (Model.showList is unconditional).
func showListViaL(t *testing.T, r *Root) *Root {
	t.Helper()
	cur, cmd := r.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if cmd == nil {
		t.Fatal("l must return the msgCloseReview cmd")
	}
	return drainRoot(t, cur.(*Root), cmd)
}

// deliverStateChanged sends msgStateChanged{} to r and drains whatever it
// returns. Safe only when r.stateChanged is nil: with a real channel,
// tea.Batch(activeChildCmd, waitCmd) collapses to the bare wait cmd whenever the
// active child's own cmd is nil (compactCmds' single-survivor case), and
// draining that would block forever on a channel nothing ever writes to.
// TestRootWaitStateChangeArmsOnlyWithChannel covers the real-channel case
// without ever invoking the wait cmd.
func deliverStateChanged(t *testing.T, r *Root) *Root {
	t.Helper()
	if r.stateChanged != nil {
		t.Fatal("deliverStateChanged is only safe for a nil-channel Root")
	}
	cur, cmd := r.Update(msgStateChanged{})
	return drainRoot(t, cur.(*Root), cmd)
}

// addThreadForPlan comments on an already-created plan at path through f,
// mirroring list_test.go's commentOnFirstParagraph but opening the session
// itself first — used here to write from a second actor's store handle.
func addThreadForPlan(t *testing.T, f fixture, path, body string) {
	t.Helper()
	s, err := session.Open(f.ctx, f.svc, path)
	if err != nil {
		t.Fatal(err)
	}
	commentOnFirstParagraph(t, f, s, body)
}

// TestRootOpensAndReturns pins the round trip: enter on a list row switches
// to the review model on that plan, and the embedded quit path (q) switches
// back to a refreshed list rather than ending the program.
func TestRootOpensAndReturns(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())
	if r.inReview {
		t.Fatal("root must start on the list")
	}
	if len(r.list.items) != 1 {
		t.Fatalf("list items = %d, want 1", len(r.list.items))
	}

	r = openViaEnter(t, r)
	if !r.inReview {
		t.Fatal("root must be showing the review model after enter's open completes")
	}
	if !strings.Contains(r.View().Content, "Rate Limiter") {
		t.Fatalf("review view = %q, want it to render the document", r.View().Content)
	}

	r = closeViaQuit(t, r)
	if r.inReview {
		t.Fatal("q while embedded must return to the list, not quit")
	}
	if len(r.list.items) != 1 {
		t.Fatalf("list items after return = %d, want 1 (refreshed)", len(r.list.items))
	}
}

// TestRootOpensSnapshotWhenNoLocalCopy pins the snapshot-open seam: a plan whose
// file was deleted after seeding opens via OpenVersion, not Open, and the review
// model's status names it a snapshot; a comment made against that session anchors
// to the plan's registered hash.
//
// It also opens under panel 1: this fixture is exactly the population that
// panel exists for, so the review arrives with modeSourceFault holding the
// keyboard and the read-mode gestures below do not reach the document until it
// is taken down, by o.
func TestRootOpensSnapshotWhenNoLocalCopy(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	s := seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")
	if err := os.Remove(s.Path); err != nil {
		t.Fatal(err)
	}

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())
	if len(r.list.items) != 1 {
		t.Fatalf("list items = %d, want 1: %+v", len(r.list.items), r.list.items)
	}
	// Stated against the filesystem itself rather than against a list field:
	// the fact each of these assertions needs is whether a file exists at the
	// row's SourceHint when the open cmd runs, which is what os.Stat asks.
	// TestRootOpensASourcelessPlan cannot ask it and says why at its own line:
	// its row names no path to stat.
	if _, err := os.Stat(r.list.items[0].plan.SourceHint); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(%q) = %v, want it gone after the file's removal", r.list.items[0].plan.SourceHint, err)
	}

	r = openViaEnter(t, r)
	if !r.inReview {
		t.Fatal("root must switch to review even without a local copy")
	}
	if !strings.Contains(r.review.status, "snapshot") {
		t.Fatalf("review status = %q, want it to mention reviewing a snapshot", r.review.status)
	}
	if r.review.mode != modeSourceFault {
		t.Fatalf("mode = %v, want modeSourceFault -- a plan whose file is gone opens under the panel that says so", r.review.mode)
	}
	if !strings.Contains(r.View().Content, "Rate Limiter") {
		t.Fatalf("review view = %q, want the CAS content rendered", r.View().Content)
	}

	// PANEL 1 NOW STANDS BETWEEN THIS OPEN AND THE DOCUMENT: this fixture
	// is exactly the population the panel exists for -- a plan whose file is
	// gone -- so the review opens with modeSourceFault holding the keyboard and
	// read-mode gestures (j, c) do not reach it. The panel is asserted here
	// because a test that merely worked around it would stop noticing if it
	// silently stopped opening.
	//
	// TAKEN DOWN BY THE REAL KEY, o ("keep it in draftplane with no file"):
	// this panel has no read-it-and-decide-later dismissal, so there is no
	// other key that leaves it. o's write (runRelease) is async, so the
	// press is driven and drained through r.Update and drainRoot rather than
	// through pressRoot, which discards a key's cmd by design. What this test is
	// about is the SESSION underneath -- a snapshot open, and a comment anchored
	// to the plan's registered hash -- and o's own outcome belongs to
	// sourcefault_test.go's TestOReleasesTheHintAndKeepsThePlan.
	cur, cmd := r.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	r = drainRoot(t, cur.(*Root), cmd)
	if r.review.mode != modeRead {
		t.Fatalf("mode after o = %v, want modeRead", r.review.mode)
	}
	if got, want := r.review.status, "reviewing from draftplane"; got != want {
		t.Fatalf("status after o = %q, want %q -- the line this behaviour exists to produce", got, want)
	}

	r = pressRoot(r, "j") // off the heading, onto the body paragraph
	r = pressRoot(r, "c") // ActComment: opens compose
	r.review.ta.SetValue("a snapshot comment")
	// tab off the editor onto the post button, then enter -- compose's post is a
	// button, and the review model's own tests drive it through pressComposePost.
	// Spelled out here because the keys have to travel
	// through Root, which is the point of this test.
	r = pressRoot(r, "tab")
	cur, cmd = r.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainRoot(t, cur.(*Root), cmd)

	planID := s.Plan.ID
	threads, err := f.svc.Threads(f.ctx, planID)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 {
		t.Fatalf("threads = %d, want 1 posted against the snapshot session", len(threads))
	}
	if threads[0].AnchorHash != s.Hash {
		t.Fatalf("thread anchor hash = %v, want it anchored to the plan's registered hash %v", threads[0].AnchorHash, s.Hash)
	}
}

// TestRootFileVanishedFallsBackToSnapshot pins the mid-flight fallback: the
// file existed when the list last refreshed, but is gone by the time enter's
// open cmd actually runs. The same cmd must fall back to OpenVersion rather
// than surfacing the file error or crashing.
func TestRootFileVanishedFallsBackToSnapshot(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	s := seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())
	if _, err := os.Stat(r.list.items[0].plan.SourceHint); err != nil {
		t.Fatalf("test setup: file must still be present at refresh time: %v", err)
	}

	if err := os.Remove(s.Path); err != nil {
		t.Fatal(err)
	}

	r = openViaEnter(t, r)

	if !r.inReview {
		t.Fatal("root must not crash or stay on the list when the file vanished mid-flight")
	}
	if r.list.err != nil {
		t.Fatalf("list.err = %v, want no hard failure (OpenVersion must have succeeded)", r.list.err)
	}
	if !strings.Contains(r.review.status, "snapshot") {
		t.Fatalf("review status = %q, want the snapshot status", r.review.status)
	}
	// The fallback is no longer the whole answer: the session it produced
	// carries WHY it fell back, latched at open. This is the list door's share
	// of the three-doors-one-answer property; ctrl+r and `draftplane review
	// <path>` reach the identical state through their own tests.
	fault := r.review.sess.SourceFault
	if fault == nil {
		t.Fatal("SourceFault = nil, want the reason this session is not following its file")
	}
	if fault.State != session.SourceFileGone {
		t.Errorf("SourceFault.State = %s, want %s", fault.State, session.SourceFileGone)
	}
	if fault.Path != s.Path {
		t.Errorf("SourceFault.Path = %q, want %q", fault.Path, s.Path)
	}
}

// TestRootOpensFileMaterializedAfterRefresh pins openPlanCmd's other
// staleness direction: no file existed at refresh time, but one came back at
// SourceHint before enter — creating a file touches no state.json, so no
// watch event re-derives the list. The open must find the file and open it as
// the working copy it now is (a real file session, reloadable by path), not
// take the refresh-time state into a snapshot blind to local edits.
func TestRootOpensFileMaterializedAfterRefresh(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	s := seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nOriginal body.\n")
	if err := os.Remove(s.Path); err != nil {
		t.Fatal(err)
	}

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())
	if _, err := os.Stat(r.list.items[0].plan.SourceHint); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("test setup: no file may exist at the row's source at refresh time: os.Stat = %v", err)
	}

	// The file comes back — edited — after the refresh, before enter.
	if err := os.WriteFile(s.Path, []byte("# Rate Limiter\n\nEdited body, back on disk.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r = openViaEnter(t, r)
	if !r.inReview {
		t.Fatal("root must be in review")
	}
	if r.review.sess.FromSnapshot() {
		t.Fatal("a file present at open time must produce a file session, not a snapshot one")
	}
	if !strings.Contains(r.View().Content, "Edited body, back on disk.") {
		t.Fatal("the review must show the materialized file's content, not the registered snapshot")
	}
}

// TestRootOpensASourcelessPlan is journeys 1a and 1b at the TUI's own front
// door: an agent handed draftplane the plan bytes over MCP with no source at all
// (mcptools.Save's default arm, which is session.OpenSupplied with "" then
// Create -- built here through those same session calls rather than through
// mcptools, which app cannot reach). Draftplane minted the id and holds the only
// copy; SourceHint is "" and NO FILE DRAFTPLANE TRACKS EXISTS. Enter on that row
// must open the plan from its registered version and render it.
//
// This is the case openPlanCmd used to serve by accident, via
// os.ReadFile("")'s ENOENT falling into the missing-file arm, and the reason it
// now travels session.ErrNoSourcePath instead. This test is the door half of the
// pair: drop the sourceless trigger from openPlanCmd's arm and it fails here.
// The refusal half is session's own TestOpenRefusesAnEmptyPathAsItsOwnFact --
// deleting the refusal alone leaves THIS test green, because ENOENT would sweep
// the case up again, which is why the seam is stated in both places.
//
// TestRootOpensAPlanWithAnUnprefixedID travels this same arm, so its green is
// no proof of the missing-file arm; the two tests above are.
func TestRootOpensASourcelessPlan(t *testing.T) {
	f := newListFixture(t)
	content := []byte("# Sourceless Plan\n\nBytes the agent handed draftplane directly.\n")
	s, err := session.OpenSupplied(f.ctx, f.svc, content, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(f.ctx, "Sourceless Plan"); err != nil {
		t.Fatal(err)
	}
	if s.Plan.SourceHint != "" {
		t.Fatalf("test setup: SourceHint = %q, want empty -- this plan must have no source at all", s.Plan.SourceHint)
	}

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())
	if len(r.list.items) != 1 || r.list.items[0].plan.ID != s.Plan.ID {
		t.Fatalf("list items = %+v, want the one sourceless plan", r.list.items)
	}
	// A sourceless plan names no file at all, and THE EMPTY HINT IS THE WHOLE
	// OBSERVATION -- against the row the list derived, where the assertion above
	// is against the session's. Deliberately NOT paired with an os.Stat of that
	// hint, which the other re-expressions do make: os.Stat("") is ENOENT for
	// every input on every machine, so it would observe nothing while looking
	// like it observed something.
	if hint := r.list.items[0].plan.SourceHint; hint != "" {
		t.Fatalf("list row SourceHint = %q, want empty -- a sourceless plan can have no local copy", hint)
	}

	r = openViaEnter(t, r)

	if r.list.err != nil {
		t.Fatalf("list.err = %v, want no hard failure -- a plan with no path opens by id", r.list.err)
	}
	if !r.inReview {
		t.Fatal("root must switch to review for a plan that never had a path")
	}
	if !strings.Contains(r.View().Content, "Bytes the agent handed draftplane directly") {
		t.Fatalf("review view = %q, want the sourceless plan's registered content rendered", r.View().Content)
	}
	// The reason snapshotOpenStatus exists: this line said "reviewing snapshot
	// (no local copy)", which is misleading twice for a plan draftplane holds
	// the only copy of, and CONTRADICTED the screen one keystroke away once the
	// list called the same plan "draftplane". The counterpart arms are pinned by
	// their own fixtures: TestRootFileVanishedFallsBackToSnapshot (a plan WITH
	// a source whose file is gone -- "no local copy" is literally true there)
	// and TestRootOpensAPlanWithAnUnprefixedID (sourceless, its id unprefixed).
	if r.review.status != "reviewing from draftplane" {
		t.Fatalf("review status = %q, want \"reviewing from draftplane\" -- a sourceless plan is not a snapshot of anything and has no local copy it could be missing", r.review.status)
	}
}

// TestNewRootInReviewNamesASnapshotOpen pins the CLI door's own share of
// snapshotOpenStatus, which had one writer -- handleSessionOpened, the LIST's
// door. `draftplane review <something>` is a second way into a snapshot
// session (a path whose file is gone, resolved to its plan by the store), and
// for the cases that get no fault and no panel -- a URL source draftplane
// cannot read, or a plan opened by its id -- this line is the whole of what the
// user is told.
func TestNewRootInReviewNamesASnapshotOpen(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	s := seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")
	if err := os.Remove(s.Path); err != nil {
		t.Fatal(err)
	}
	sess, err := session.OpenPlan(attrCtx("alice"), f.svc, s.Plan)
	if err != nil {
		t.Fatal(err)
	}

	r := NewRootInReview(f.svc, keymap.Default(), nil, "", nil, sess)
	if !strings.Contains(r.review.status, "snapshot") {
		t.Fatalf("review status = %q, want the snapshot sentence -- this session is not following the file the user named", r.review.status)
	}
}

// fakeUnprefixedPlanService is a *localfs.Store plus one plan whose id lacks the
// l_ prefix. localfs mints only l_ ids, so the fake answers for that plan itself
// and leaves every other plan to the embedded store. The point under test is
// that openPlanCmd opens this plan at all, whatever its id's shape.
type fakeUnprefixedPlanService struct {
	*localfs.Store
	id           domain.PlanID
	title        string
	tip          domain.ContentHash
	content      []byte
	contentCalls int
}

func (f *fakeUnprefixedPlanService) ListPlans(ctx context.Context) ([]domain.Plan, error) {
	plans, err := f.Store.ListPlans(ctx)
	if err != nil {
		return nil, err
	}
	return append(plans, domain.Plan{ID: f.id, Title: f.title}), nil
}

func (f *fakeUnprefixedPlanService) Versions(ctx context.Context, id domain.PlanID) ([]domain.Version, error) {
	if id == f.id {
		return []domain.Version{{VersionRef: domain.VersionRef{Hash: f.tip}}}, nil
	}
	return f.Store.Versions(ctx, id)
}

// PlanByID answers for the unprefixed plan the same way ListPlans lists it: the
// embedded *localfs.Store does not hold this plan, so without this override the
// open below would report a plan the list just showed as nonexistent.
func (f *fakeUnprefixedPlanService) PlanByID(ctx context.Context, id domain.PlanID) (domain.Plan, error) {
	if id == f.id {
		return domain.Plan{ID: f.id, Title: f.title}, nil
	}
	return f.Store.PlanByID(ctx, id)
}

// Content, for the unprefixed plan, answers its bytes directly and counts the
// call -- the embedded store does not hold them. session.OpenVersion reads
// content through exactly this method, so the fake must answer it, not just
// ListPlans and Versions.
func (f *fakeUnprefixedPlanService) Content(ctx context.Context, id domain.PlanID, h domain.ContentHash) ([]byte, error) {
	if id == f.id {
		f.contentCalls++
		if h != f.tip {
			return nil, fmt.Errorf("plan %s: %w", id, client.ErrNotFound)
		}
		return f.content, nil
	}
	return f.Store.Content(ctx, id, h)
}

var _ client.PlanService = (*fakeUnprefixedPlanService)(nil)

// TestRootOpensAPlanWithAnUnprefixedID pins that a plan whose id lacks the l_
// prefix lists and opens like any other: enter on its row opens it through
// session.OpenVersion, whose one Content call supplies the bytes the review
// renders.
func TestRootOpensAPlanWithAnUnprefixedID(t *testing.T) {
	dir := t.TempDir()
	store, err := localfs.New(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("# Unprefixed Plan\n\nBody of the unprefixed plan.\n")
	svc := &fakeUnprefixedPlanService{
		Store: store, id: "a1b2c3d4", title: "Unprefixed Plan",
		tip: domain.HashContent(content), content: content,
	}

	r := NewRoot(svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())
	if len(r.list.items) != 1 {
		t.Fatalf("list items = %d, want 1 (the unprefixed plan)", len(r.list.items))
	}

	r = openViaEnter(t, r)

	if r.list.err != nil {
		t.Fatalf("list.err = %v, want no hard failure -- Content must have supplied the bytes", r.list.err)
	}
	if !r.inReview {
		t.Fatal("root must switch to review for a plan whose id lacks the l_ prefix")
	}
	if svc.contentCalls != 1 {
		t.Fatalf("Content was called %d times, want exactly 1 -- the bytes must have come from opening the plan, not something else", svc.contentCalls)
	}
	if !strings.Contains(r.View().Content, "Body of the unprefixed plan") {
		t.Fatalf("review view = %q, want the opened content rendered", r.View().Content)
	}
	// An unprefixed id is a plan like any other: this one is sourceless, so
	// it reads exactly as an l_ plan with no file does.
	if want := "reviewing from draftplane"; r.review.status != want {
		t.Fatalf("review status = %q, want %q -- a sourceless plan reads the same whatever its id's shape", r.review.status, want)
	}
}

// TestRootStaleOpenResultDropped pins handleSessionOpened's dispatch-order
// guard: the list dispatches msgOpenPlan on every enter with no in-flight
// latch, so a slow open for plan A can still be running when a second enter
// dispatches plan B's fast one, and bubbletea gives no ordering guarantee
// on whose msgSessionOpened lands first. A's result landing after B's must
// be dropped, not silently replace the review the user is already in.
func TestRootStaleOpenResultDropped(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	seedListPlan(t, f, dir, "plan-a.md", "Plan A", "# Plan A\n\nBody of plan A.\n")
	seedListPlan(t, f, dir, "plan-b.md", "Plan B", "# Plan B\n\nBody of plan B.\n")

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())

	var planA, planB domain.Plan
	for _, it := range r.list.items {
		switch it.plan.Title {
		case "Plan A":
			planA = it.plan
		case "Plan B":
			planB = it.plan
		}
	}

	// Dispatch A's open and hold its cmd (the slow one); dispatch B's open
	// and let its result land first.
	curA, cmdA := r.Update(msgOpenPlan{plan: planA})
	r = curA.(*Root)
	curB, cmdB := r.Update(msgOpenPlan{plan: planB})
	r = drainRoot(t, curB.(*Root), cmdB)
	if !r.inReview || r.review.sess.Plan.ID != planB.ID {
		t.Fatal("test setup: the newer open (plan B) must be the active review")
	}

	// A's stale result arrives late: it must be dropped.
	r = drainRoot(t, r, cmdA)
	if r.review.sess.Plan.ID != planB.ID {
		t.Fatalf("stale open result replaced the active review: reviewing %v, want %v (plan B)",
			r.review.sess.Plan.ID, planB.ID)
	}
}

// TestRootEmbeddedQuitRefusedWhileWriteInFlight pins the embedded quit
// path's in-flight discipline: unlike standalone (where process exit makes
// a dropped result moot, so quit-mid-write stays allowed), an embedded quit
// would strand the write's msgActionDone on the list, which has no case for
// it — a failed write's error silently lost. q must refuse with the same
// "action in progress" status as every other in-flight refusal, then land
// normally once the write resolves.
func TestRootEmbeddedQuitRefusedWhileWriteInFlight(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())
	r = openViaEnter(t, r)

	// Dispatch an approve, holding its result so inFlight stays true.
	r = pressRoot(r, "a") // confirm-approve panel
	cur, held := r.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	r = cur.(*Root)
	if !r.review.inFlight {
		t.Fatal("test setup: dispatching approve must claim inFlight")
	}

	cur, cmd := r.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	r = cur.(*Root)
	if cmd != nil {
		t.Fatal("embedded q while a write is in flight must not dispatch msgCloseReview")
	}
	if !r.inReview {
		t.Fatal("refused quit must stay in review")
	}
	if !strings.Contains(r.review.status, "in progress") {
		t.Fatalf("status = %q, want an in-progress hint", r.review.status)
	}

	// The write resolves; the very next q lands.
	r = drainRoot(t, r, held)
	if r.review.inFlight {
		t.Fatal("inFlight must clear once the write's result lands")
	}
	r = closeViaQuit(t, r)
	if r.inReview {
		t.Fatal("q after the write resolved must return to the list")
	}
}

// TestEmbeddedCtrlCAlwaysQuits pins the unconditional exit lane: q's
// embedded in-flight refusal must never leave the program with no keyboard
// exit at all (a write wedged on the state lock would make q refuse
// forever), so ctrl+c returns tea.Quit even embedded, even mid-write —
// process exit makes the dropped result moot, exactly as in standalone.
func TestEmbeddedCtrlCAlwaysQuits(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())
	r = openViaEnter(t, r)

	r.review.inFlight = true // a wedged write: its msgActionDone never arrives
	_, cmd := r.review.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c must return a cmd even embedded with a write in flight")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c embedded must produce tea.QuitMsg — the unconditional exit lane")
	}
}

// TestRootPropagatesCtrlCQuit pins Root's cmd plumbing itself, distinct from
// TestEmbeddedCtrlCAlwaysQuits above (which dispatches straight to
// r.review.Update): Root.Update's default case is a bare passthrough
// (return r, r.updateActive(msg)), so a child's tea.Quit cmd must come back
// out of r.Update unmodified when ctrl+c is dispatched through Root, the way
// a real KeyPressMsg arrives during the program's actual event loop.
func TestRootPropagatesCtrlCQuit(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())
	r = openViaEnter(t, r)

	_, cmd := r.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c through Root must return a cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c through Root must propagate tea.QuitMsg from the embedded review")
	}
}

// TestRootRelocateQuitReturnsToList pins the same embedded-quit discipline
// for modeRelocate: q while triaging orphans previously fell through to the
// panel switch's own `case keymap.ActQuit: return m, tea.Quit`, ending the
// whole program instead of going through m.quit() like every other quit
// path. It must behave exactly like closeViaQuit from modeRead — return to
// the list via msgCloseReview, not tea.Quit.
func TestRootRelocateQuitReturnsToList(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	planPath := filepath.Join(dir, "plan.md")
	seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")
	seedOrphan(t, fixture{svc: f.svc, cas: f.cas, path: planPath, ctx: f.ctx}, "lost comment")

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())
	r = openViaEnter(t, r)

	r = pressRoot(r, "m")
	if r.review.mode != modeRelocate {
		t.Fatalf("test setup: mode = %v, want modeRelocate", r.review.mode)
	}

	r = closeViaQuit(t, r)
	if r.inReview {
		t.Fatal("q while triaging orphans in relocate mode must return to the list, not quit the program")
	}
}

// TestRootIgnoresOpenPlanWhileInReview pins the queued-straggler guard: only
// the list emits msgOpenPlan, so one arriving while a review is active (a
// double-enter whose second dispatch cmd ran late) must be ignored, not
// dispatched into an open that would replace the review in use.
func TestRootIgnoresOpenPlanWhileInReview(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	seedListPlan(t, f, dir, "plan-a.md", "Plan A", "# Plan A\n\nBody of plan A.\n")
	seedListPlan(t, f, dir, "plan-b.md", "Plan B", "# Plan B\n\nBody of plan B.\n")

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())
	current := r.list.items[0].plan
	other := r.list.items[1].plan
	r = openViaEnter(t, r)
	if !r.inReview || r.review.sess.Plan.ID != current.ID {
		t.Fatal("test setup: must be reviewing the first plan")
	}

	cur, cmd := r.Update(msgOpenPlan{plan: other})
	r = cur.(*Root)
	if cmd != nil {
		t.Fatal("msgOpenPlan while a review is active must be ignored, not dispatched")
	}
	if r.review.sess.Plan.ID != current.ID {
		t.Fatal("the active review must be untouched")
	}
}

// TestRootOpenResultDroppedWhileListBusy pins handleSessionOpened's
// list-state guard: a slow open resolving after the user moved on — into
// the rename panel, or into a dispatched delete — must not yank them into
// the review (discarding a half-typed draft, or stranding the write's
// msgListActionDone on the review and wedging list.inFlight forever). The
// stale result is dropped; the plan opens again on the next enter.
func TestRootOpenResultDroppedWhileListBusy(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())

	// Enter dispatches the open; hold its cmd (the slow open) and let the
	// user move on into rename.
	cur, enterCmd := r.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	r = cur.(*Root)
	openMsg := enterCmd() // msgOpenPlan
	cur, openCmd := r.Update(openMsg)
	r = cur.(*Root)

	r = pressRoot(r, "e")
	if r.list.mode != listRename {
		t.Fatal("test setup: list must be in rename mode")
	}

	r = drainRoot(t, r, openCmd) // the slow open's result lands now
	if r.inReview {
		t.Fatal("an open resolving while the rename panel is up must not switch to the review")
	}
	if r.list.mode != listRename {
		t.Fatal("the rename panel (and its draft) must be left standing")
	}
}

// TestRootPlanDeletedWhileFileRemainsSurfacesError pins openPlanCmd's
// Exists guard: a plan record deleted between refresh and enter, with its
// file still on disk, must surface a visible error on the list — not
// silently open an empty, plan-less review whose first write would
// re-create the plan under a new ID.
func TestRootPlanDeletedWhileFileRemainsSurfacesError(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	s := seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())

	// Another actor deletes the plan record; the file stays.
	if err := f.svc.DeletePlan(f.ctx, s.Plan.ID); err != nil {
		t.Fatal(err)
	}

	r = openViaEnter(t, r)
	if r.inReview {
		t.Fatal("a deleted plan must not open as a silent plan-less review")
	}
	if r.list.err == nil || !strings.Contains(r.list.status, "error") {
		t.Fatalf("list status = %q (err %v), want a visible error", r.list.status, r.list.err)
	}
}

// TestRootUnreadableFileFallsBackAndNamesWhy: only a MISSING file used to fall
// back, and any other open failure was surfaced as-is because falling back would
// SILENTLY swap a real, possibly diverged working copy for the snapshot, with
// comments then anchoring to the wrong content. Every word of that still holds
// except silently -- which was the load-bearing one. The session now carries
// SourceFault, so the swap announces itself and the door can say which of the
// four things happened; a hard error for a file the user can see, on a plan they
// can still read and act on, is a dead end. The identity half of Open is what
// stays narrow (TestRootPlanDeletedWhileFileRemainsSurfacesError above).
//
// THE PATH IS TURNED INTO A DIRECTORY rather than chmod 0o000: a permission bit
// is a no-op under a root test runner, so that fixture could pass for the wrong
// reason on one machine and fail on another. EISDIR is nobody's privilege to
// bypass, and it is the same classification (session.ErrSourceUnreadable, not
// fs.ErrNotExist).
func TestRootUnreadableFileFallsBackAndNamesWhy(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	s := seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())

	if err := os.Remove(s.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.Path, 0o755); err != nil {
		t.Fatal(err)
	}

	r = openViaEnter(t, r)
	if !r.inReview {
		t.Fatalf("a file that will not read must fall back and say why, not dead-end the review (list.err = %v)", r.list.err)
	}
	fault := r.review.sess.SourceFault
	if fault == nil {
		t.Fatal("SourceFault = nil -- a fallback nobody can explain is the silent swap the old narrowness existed to prevent")
	}
	if fault.State != session.SourceFileUnreadable {
		t.Errorf("SourceFault.State = %s, want %s -- the file is there, it just will not read", fault.State, session.SourceFileUnreadable)
	}
}

// TestSnapshotStatusSurvivesOrphanFlash pins the startup-status seam between
// Root and the review model: handleSessionOpened sets "reviewing snapshot
// (no local copy)" synchronously, but review.Init()'s msgRefreshed lands a
// tick later — and snapshot plans (anchors authored against content that
// has since moved on) are exactly the ones likely to arrive with unresolved
// orphans. The orphan flash must append to the snapshot notice, not clobber
// it.
func TestSnapshotStatusSurvivesOrphanFlash(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	s := seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nThe token bucket refills at a fixed rate per second.\n")
	commentOnFirstParagraph(t, f, s, "will orphan when the body changes")

	// The document is rewritten wholesale (orphaning the thread's anchor)
	// and the new content registered via approve; then the file vanishes,
	// leaving only the registered snapshot.
	if err := os.WriteFile(s.Path, []byte("# Rate Limiter\n\nEntirely different content now, sharing no words at all.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s2, err := session.Open(f.ctx, f.svc, s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Approve(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.Path); err != nil {
		t.Fatal(err)
	}

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())
	r = openViaEnter(t, r)

	if !r.inReview || !r.review.sess.FromSnapshot() {
		t.Fatal("test setup: must be reviewing the snapshot")
	}
	if len(r.review.unresolvedOrphans()) == 0 {
		t.Fatal("test setup: the rewritten content must orphan the thread")
	}
	if !strings.Contains(r.review.status, "snapshot") {
		t.Fatalf("status = %q, the snapshot notice must survive the orphan flash", r.review.status)
	}
	if !strings.Contains(r.review.status, "orphaned") {
		t.Fatalf("status = %q, the orphan flash must still appear alongside the snapshot notice", r.review.status)
	}
}

// TestRootForwardsStateChanged pins Root's watcher ownership: a state change
// delivered while review is active refreshes the review model (its own
// waitStateChange is a permanent no-op with a nil channel, so this only
// works via Root's forwarding); after switching back to the list, delivery
// refreshes the list instead.
func TestRootForwardsStateChanged(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	seedListPlan(t, f, dir, "plan-a.md", "Plan A", "# Plan A\n\nBody of plan A.\n")

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())

	r = openViaEnter(t, r)
	if !r.inReview {
		t.Fatal("test setup: root must be in review")
	}

	f2 := secondFixtureStore(t, f)
	addThreadForPlan(t, f2, filepath.Join(dir, "plan-a.md"), "posted while embedded")

	r = deliverStateChanged(t, r)
	r = pressRoot(r, "n", "enter") // jump to the new thread's block and expand it (app_test.go's own convention)
	if !strings.Contains(r.View().Content, "posted while embedded") {
		t.Fatal("msgStateChanged while review is active must refresh the review model, not the list")
	}

	r = closeViaQuit(t, r)
	if r.inReview {
		t.Fatal("test setup: root must be back on the list")
	}

	seedListPlan(t, f2, dir, "plan-b.md", "Plan B", "# Plan B\n\nBody.\n")
	r = deliverStateChanged(t, r)
	if len(r.list.items) != 2 {
		t.Fatalf("list items after state change = %d, want 2 (msgStateChanged must reach the now-active list)", len(r.list.items))
	}
}

// TestRootWaitStateChangeArmsOnlyWithChannel pins Root's own
// waitStateChange contract — the one copy of the watch loop that reads a
// real channel now that children are always built with a nil one (see
// TestRootForwardsStateChanged): nil disables it, a real injected channel
// arms it. The cmd is never invoked (it would block forever on a channel
// nothing writes to) — its mere non-nilness is the contract being pinned.
func TestRootWaitStateChangeArmsOnlyWithChannel(t *testing.T) {
	f := newListFixture(t)

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	if r.waitStateChange() != nil {
		t.Fatal("waitStateChange must be nil when Root was built with a nil channel")
	}

	ch := make(chan struct{}, 1)
	r2 := NewRoot(f.svc, keymap.Default(), nil, "", ch)
	if r2.waitStateChange() == nil {
		t.Fatal("waitStateChange must be non-nil when Root was built with a real channel")
	}
}

// TestRootReplaysWindowSize pins the resize-then-switch contract: a resize
// while the list is active must be replayed onto the review model when it
// becomes active, since Root only forwards WindowSizeMsg to whichever child
// is active at the time it arrives.
func TestRootReplaysWindowSize(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())

	cur, _ := r.Update(tea.WindowSizeMsg{Width: 77, Height: 22})
	r = cur.(*Root)

	r = openViaEnter(t, r)
	if !r.inReview {
		t.Fatal("test setup: root must be in review")
	}
	if r.review.width != 77 || r.review.height != 22 {
		t.Fatalf("review dimensions = %dx%d, want 77x22 (the size recorded before the switch)", r.review.width, r.review.height)
	}
}

// TestRootInReviewStartsInReview pins NewRootInReview's construction: the
// review is active immediately, on exactly the session passed in — no open
// seam runs, unlike the list-driven path.
func TestRootInReviewStartsInReview(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	s := seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")

	r := NewRootInReview(f.svc, keymap.Default(), nil, "", nil, s)
	if !r.inReview {
		t.Fatal("NewRootInReview must start with the review active")
	}
	if r.review.sess != s {
		t.Fatal("the review's session must be the one passed to NewRootInReview")
	}
}

// TestRootInReviewListNotEagerlyRefreshed pins the conditional-Init
// correction: Init must init only the active child. Eagerly Init'ing the
// list too would cost every `draftplane review <path>` launch a wasted
// derivation the user may never look at — closeReview already refreshes the
// list at switch time.
func TestRootInReviewListNotEagerlyRefreshed(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	s := seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")

	r := NewRootInReview(f.svc, keymap.Default(), nil, "", nil, s)
	r = drainRoot(t, r, r.Init())
	if r.list.refreshSeq != 0 {
		t.Fatalf("list.refreshSeq = %d, want 0 (the list must not be eagerly Init'd)", r.list.refreshSeq)
	}
}

// TestRootInReviewShowListSwitchesAndRefreshes pins that l from a
// directly-opened review reaches the list, freshly refreshed and sized from
// whatever WindowSizeMsg Root has already seen.
func TestRootInReviewShowListSwitchesAndRefreshes(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	s := seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")

	r := NewRootInReview(f.svc, keymap.Default(), nil, "", nil, s)
	r = drainRoot(t, r, r.Init())

	cur, _ := r.Update(tea.WindowSizeMsg{Width: 77, Height: 22})
	r = cur.(*Root)

	r = showListViaL(t, r)
	if r.inReview {
		t.Fatal("l must switch away from the review")
	}
	if len(r.list.items) != 1 {
		t.Fatalf("list items = %d, want 1 (populated by the switch-time refresh)", len(r.list.items))
	}
	if r.list.width != 77 || r.list.height != 22 {
		t.Fatalf("list dimensions = %dx%d, want 77x22 (replayed from the size seen while in review)", r.list.width, r.list.height)
	}
}

// TestRootInReviewQuitStillEndsProgram pins the deliberate asymmetry's other
// half: q from a directly-opened review still ends the whole program —
// embedded stays false, exactly as before Root existed for this entry
// point.
func TestRootInReviewQuitStillEndsProgram(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	s := seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")

	r := NewRootInReview(f.svc, keymap.Default(), nil, "", nil, s)
	r = drainRoot(t, r, r.Init())

	_, cmd := r.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("q must return a cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q from a directly-opened review must end the program (embedded stays false)")
	}
}

// TestRootInReviewShowListRefusedWhileWriteInFlight mirrors
// TestRootEmbeddedQuitRefusedWhileWriteInFlight for l instead of q: l must
// refuse (via the same dispatchOK discipline showList shares with quit)
// while a write is in flight, landing on the very next l once it resolves.
func TestRootInReviewShowListRefusedWhileWriteInFlight(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	s := seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")

	r := NewRootInReview(f.svc, keymap.Default(), nil, "", nil, s)
	r = drainRoot(t, r, r.Init())

	// Dispatch an approve, holding its result so inFlight stays true.
	r = pressRoot(r, "a") // confirm-approve panel
	cur, held := r.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	r = cur.(*Root)
	if !r.review.inFlight {
		t.Fatal("test setup: dispatching approve must claim inFlight")
	}

	cur, cmd := r.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	r = cur.(*Root)
	if cmd != nil {
		t.Fatal("l while a write is in flight must not dispatch msgCloseReview")
	}
	if !r.inReview {
		t.Fatal("refused l must stay in review")
	}
	if !strings.Contains(r.review.status, "in progress") {
		t.Fatalf("status = %q, want an in-progress hint", r.review.status)
	}

	// The write resolves; the very next l lands.
	r = drainRoot(t, r, held)
	if r.review.inFlight {
		t.Fatal("inFlight must clear once the write's result lands")
	}
	r = showListViaL(t, r)
	if r.inReview {
		t.Fatal("l after the write resolved must return to the list")
	}
}

// TestRootInReviewRelocateShowListReturnsToList mirrors
// TestRootRelocateQuitReturnsToList's discipline for l: it must reach the
// list from modeRelocate too, on a directly-opened review, not just modeRead.
func TestRootInReviewRelocateShowListReturnsToList(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	planPath := filepath.Join(dir, "plan.md")
	s := seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")
	seedOrphan(t, fixture{svc: f.svc, cas: f.cas, path: planPath, ctx: f.ctx}, "lost comment")

	r := NewRootInReview(f.svc, keymap.Default(), nil, "", nil, s)
	r = drainRoot(t, r, r.Init())

	r = pressRoot(r, "m")
	if r.review.mode != modeRelocate {
		t.Fatalf("test setup: mode = %v, want modeRelocate", r.review.mode)
	}

	r = showListViaL(t, r)
	if r.inReview {
		t.Fatal("l while triaging orphans in relocate mode must return to the list, not stay in review")
	}
}

// TestRootInReviewReenterIsEmbedded pins the deliberate asymmetry: after l
// switches a directly-opened (non-embedded) review to the list, reentering
// the SAME plan from the list goes through handleSessionOpened's ordinary
// NewEmbedded path — the new review IS embedded, so q from it now returns to
// the list instead of quitting.
func TestRootInReviewReenterIsEmbedded(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	s := seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")

	r := NewRootInReview(f.svc, keymap.Default(), nil, "", nil, s)
	r = drainRoot(t, r, r.Init())

	r = showListViaL(t, r)
	if r.inReview {
		t.Fatal("test setup: l must switch to the list")
	}

	r = openViaEnter(t, r)
	if !r.inReview {
		t.Fatal("test setup: enter must reopen the review")
	}
	if !r.review.embedded {
		t.Fatal("a review reentered from the list must be embedded (q returns to list, not quit)")
	}
}

// TestRootInReviewCtrlCQuits pins the unconditional exit lane for the
// review-first construction too: ctrl+c always quits, embedded or not.
func TestRootInReviewCtrlCQuits(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	s := seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")

	r := NewRootInReview(f.svc, keymap.Default(), nil, "", nil, s)
	r = drainRoot(t, r, r.Init())

	_, cmd := r.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c must return a cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c from the review-first construction must produce tea.QuitMsg")
	}
}

// TestRootInReviewWindowSizeAppliesDirectlyToReview pins that a size arriving
// after Init reaches the already-active review directly (Root's
// WindowSizeMsg case forwards to updateActive), while the list — not shown
// yet — stays at NewList's construction defaults.
func TestRootInReviewWindowSizeAppliesDirectlyToReview(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	s := seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")

	r := NewRootInReview(f.svc, keymap.Default(), nil, "", nil, s)
	r = drainRoot(t, r, r.Init())

	cur, _ := r.Update(tea.WindowSizeMsg{Width: 77, Height: 22})
	r = cur.(*Root)

	if r.review.width != 77 || r.review.height != 22 {
		t.Fatalf("review dimensions = %dx%d, want 77x22", r.review.width, r.review.height)
	}
	if r.list.width != 100 || r.list.height != 30 {
		t.Fatalf("list dimensions = %dx%d, want the NewList construction defaults (100x30) until shown", r.list.width, r.list.height)
	}
}

// TestStandaloneReviewQuitUnchanged pins that a non-embedded Model's quit
// path is byte-identical to before this change: q still returns tea.Quit
// (executed, it yields tea.QuitMsg), never msgCloseReview.
func TestStandaloneReviewQuitUnchanged(t *testing.T) {
	f := setup(t)
	m := openModel(t, f)
	if m.embedded {
		t.Fatal("a standalone Model built with New must not be embedded")
	}

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("q must return a cmd")
	}
	switch cmd().(type) {
	case tea.QuitMsg:
	default:
		t.Fatal("standalone q must still produce tea.QuitMsg")
	}
}

// clearScreenIn reports whether cmd's message tree contains tea.ClearScreen's
// message.
//
// IT COMPARES TYPES RATHER THAN VALUES, and that is not fastidiousness: the
// message type is unexported, so a type assertion is unavailable here, and ==
// on two tea.Msg values PANICS the moment the tree carries a non-comparable
// message (tea.BatchMsg is []Cmd, and it is not the only one). reflect.TypeOf
// is total over every message a cmd can answer.
func clearScreenIn(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	msg := cmd()
	if msg == nil {
		return false
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if clearScreenIn(c) {
				return true
			}
		}
		return false
	}
	return reflect.TypeOf(msg) == reflect.TypeOf(tea.ClearScreen())
}

// TestRootRepaintsOnEveryViewSwitch pins the repaint containment: BOTH
// directions of the list<->review switch ask for a full-screen repaint.
//
// WHY A REPAINT IS OWED, AND WHY THE FRAME ASSERTIONS CANNOT STAND IN FOR IT.
// Bubbletea renders through a curses-style cell differ: transformLine walks the
// renderer's model of the screen against the new frame and emits only cells
// whose MODEL value changed. Every assertion this repository owns measures the
// frame -- the string we hand the renderer -- and that frame can be correct to
// the cell while the SCREEN is not, because a terminal is free to paint a
// grapheme cluster in a different number of cells than any width ruler answers.
// U+1F468 U+200D U+1F469 U+200D U+1F467 measures 2 to every ruler we have and
// Zellij paints it in 6, so four terminal cells hold glyphs the renderer has no
// model of. Nothing repairs them: on the next frame the model says those cells
// are unchanged, cellEqual agrees, and NOTHING IS EMITTED FOR THEM AGAIN. The
// stale glyph outlives the document it came from and lands on the list.
//
// THIS DOES NOT FIX THE DISAGREEMENT AND IS NOT MEANT TO. It bounds it. A
// repaint costs one frame at a gesture the user already expects to be a scene
// change, and it turns "corrupted until the process restarts" into "corrupted
// until you leave the view". The disagreement itself is Zellij's and is
// disclosed, not closed.
func TestRootRepaintsOnEveryViewSwitch(t *testing.T) {
	f := newListFixture(t)
	dir := filepath.Dir(f.path)
	sess := seedListPlan(t, f, dir, "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r = drainRoot(t, r, r.Init())
	if len(r.list.items) != 1 {
		t.Fatalf("test setup: list items = %d, want 1", len(r.list.items))
	}

	// list -> review. Driven at handleSessionOpened rather than through enter so
	// the cmd under assertion is the SWITCH's own and not a batch some earlier
	// step in the open chain contributed to.
	cur, cmd := r.Update(msgSessionOpened{seq: r.openSeq, sess: sess})
	r = cur.(*Root)
	if !r.inReview {
		t.Fatal("test setup: msgSessionOpened must have switched to the review")
	}
	if !clearScreenIn(cmd) {
		t.Error("switching list -> review must ask for a full-screen repaint: the incoming " +
			"frame cannot overwrite cells the renderer does not know the outgoing one painted")
	}

	// review -> list, the direction the defect was reported in.
	cur, cmd = r.Update(msgCloseReview{})
	r = cur.(*Root)
	if r.inReview {
		t.Fatal("test setup: msgCloseReview must have returned to the list")
	}
	if !clearScreenIn(cmd) {
		t.Error("switching review -> list must ask for a full-screen repaint: this is the " +
			"direction that strands a document's glyphs on the plan list")
	}
}

// TestTheListFirstShownFromAReviewSaysNothingUntilItLoads is the review-first
// door onto the list's first load. `draftplane review <path>` starts Root on
// the review and never Inits the list, so the first l is the list's first load:
// until an answer lands, the list may claim nothing and the bar may count
// nothing -- "No plans yet!" and "0 plans" over a load still in flight are the
// defect this guards against, whichever door the list was reached through.
//
// THE CLOSE'S CMD IS NOT RUN until the blank frame has been read, so no answer
// can have landed; draining it afterwards is the control that the load it
// started does arrive.
func TestTheListFirstShownFromAReviewSaysNothingUntilItLoads(t *testing.T) {
	f := newListFixture(t)
	sess := seedListPlan(t, f, filepath.Dir(f.path), "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody of the plan.\n")

	r := NewRootInReview(f.svc, keymap.Default(), nil, "", nil, sess)
	r = drainRoot(t, r, r.Init())

	cur, cmd := r.Update(msgCloseReview{})
	r = cur.(*Root)
	if got := sectionBodyText(r.list, sectionMine); !slices.Equal(got, []string{""}) {
		t.Fatalf("before any answer, Your plans = %q, want one blank row", got)
	}
	if bar := ansi.Strip(r.list.statusBarText(120, r.list.styles.StatusBar)); strings.Contains(bar, "plans") {
		t.Fatalf("status bar before any answer = %q, want no count", bar)
	}

	r = drainRoot(t, r, cmd)
	if got, want := sectionBodyText(r.list, sectionMine), []string{"Rate Limiter"}; !slices.Equal(got, want) {
		t.Fatalf("after the load, Your plans = %q, want %q", got, want)
	}
}

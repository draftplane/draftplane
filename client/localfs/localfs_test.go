package localfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/reanchor"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

// attrCtx stands in for the actor string every test in this file used to pass
// to New: Store no longer carries one (see New's own comment), so every write
// here must decorate its own context instead.
//
// IT TAKES ONE STRING AND WRITES TWO. domain.Attribution carries a login
// beside the display name, and a fixture holding the same string in both halves
// cannot say which half a renderer read. attrLogin derives the login half rather
// than accepting it, so no caller can make the two equal by accident.
func attrCtx(actorDisplay string) context.Context {
	return client.WithAttribution(context.Background(), domain.Attribution{
		ActorLogin:   attrLogin(actorDisplay),
		ActorDisplay: actorDisplay,
	})
}

// attrLogin is the login every fixture in this file pairs with a display name,
// and it guarantees the two are never the same string.
//
// It lowercases and hyphenates whatever it is handed, then appends a suffix.
// The suffix is what makes the result DIFFER, by construction rather than by
// inspection of today's callers: the mapping is length-preserving in runes, so
// a mapped string plus a non-empty suffix can never equal its own input.
func attrLogin(actorDisplay string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return '-'
	}, actorDisplay) + "-codes"
}

var anchor = reanchor.Anchor{HeadingPath: []string{"Plan", "Context"}, Span: "the important sentence"}

// TestFreshInstallMissingParentDir pins the fresh-install fix: New is pointed at
// a state path whose parent directory does not exist yet, and the very first
// operation — a read as well as a write — must still succeed. Before the fix,
// withLock's O_CREATE open of the sibling lockfile failed ENOENT because only
// save() created the parent directory, and only on a write.
func TestFreshInstallMissingParentDir(t *testing.T) {
	readPath := filepath.Join(t.TempDir(), "nested", "does", "not", "exist", "state.json")
	s, err := New(readPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListPlans(attrCtx("alice")); err != nil {
		t.Fatalf("first read against a missing parent dir: %v", err)
	}

	writePath := filepath.Join(t.TempDir(), "nested", "does", "not", "exist", "state.json")
	s2, err := New(writePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.CreatePlan(attrCtx("alice"), "Foo", "/plans/foo.md", "", []byte("aaa")); err != nil {
		t.Fatalf("first write against a missing parent dir: %v", err)
	}
}

func TestPlanLifecycle(t *testing.T) {
	s, _ := newStore(t)
	ctx := attrCtx("alice")

	if _, err := s.ResolvePlan(ctx, "/plans/foo.md"); !errors.Is(err, client.ErrNoPlan) {
		t.Fatalf("err = %v, want ErrNoPlan", err)
	}

	plan, err := s.CreatePlan(ctx, "Foo", "/plans/foo.md", "", []byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.ID == "" {
		t.Fatalf("CreatePlan returned no id")
	}

	resolved, err := s.ResolvePlan(ctx, "/plans/foo.md")
	if err != nil || resolved.ID != plan.ID {
		t.Fatalf("resolve = %+v, %v", resolved, err)
	}

	plans, err := s.ListPlans(ctx)
	if err != nil || len(plans) != 1 {
		t.Fatalf("list = %v, %v", plans, err)
	}

	versions, err := s.Versions(ctx, plan.ID)
	if err != nil || len(versions) != 1 || versions[0].Hash != domain.HashContent([]byte("aaa")) {
		t.Fatalf("versions = %+v, %v", versions, err)
	}
	if versions[0].Attribution.ActorDisplay != "alice" || versions[0].RegisteredAt.IsZero() {
		t.Fatalf("service must stamp attribution: %+v", versions[0])
	}
	if versions[0].Seq != 1 {
		t.Fatalf("first version's Seq = %d, want 1 (position is this Store's assignment, whatever Seq the caller's initial carried)", versions[0].Seq)
	}

	// CreatePlan's OWN return value -- not a subsequent ListPlans/ResolvePlan
	// call, which would compute these fresh regardless -- must already carry
	// its derived facts: a fresh plan has one fact (its own first version),
	// so LastActivityAt must already reflect it rather than reading as the
	// zero value until the next read.
	if plan.OpenThreads != 0 || plan.TotalThreads != 0 || plan.TipApproved {
		t.Fatalf("CreatePlan's own return = %+v, want a fresh plan's zero-valued counts", plan)
	}
	if !plan.LastActivityAt.Equal(versions[0].RegisteredAt) {
		t.Fatalf("CreatePlan's own return LastActivityAt = %v, want %v (its own first version's RegisteredAt)", plan.LastActivityAt, versions[0].RegisteredAt)
	}
}

// TestListPlansCarriesDerivedFactsScopedPerPlan proves ListPlans' four derived
// fields (OpenThreads, TotalThreads, TipApproved, LastActivityAt) are each
// computed for the RIGHT plan, not leaked from or summed across a sibling -- a
// single-plan fixture cannot tell "reads this plan's own facts" apart from
// "reads every plan's facts and got lucky," so this seeds two, deliberately
// shaped so a scoping bug shows up as a wrong number rather than a
// coincidentally correct one:
//
//   - plan A: 2 threads (1 open, 1 resolved) and an approval on its FIRST
//     version, superseded by a second RegisterVersion before this test ever
//     reads it back. TipApproved must be false. A bug that compared against
//     versions[0] instead of the LAST element of the log would read this as
//     true.
//   - plan B: 1 thread (open) and an approval on its only, and therefore
//     current, version. TipApproved must be true.
//
// LastActivityAt is checked against the EXACT CreatedAt/RegisteredAt this test's
// own calls got back, not a loose "is recent" check: a bug that stopped folding
// comments or approvals into the max would still pass a loose recency check.
func TestListPlansCarriesDerivedFactsScopedPerPlan(t *testing.T) {
	s, _ := newStore(t)
	ctx := attrCtx("alice")

	// Plan A: register v1, approve v1, THEN register v2 -- the approval now
	// names a version that is no longer the tip.
	planA, err := s.CreatePlan(ctx, "Plan A", "/plans/a.md", "", []byte("a-v1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, planA.ID, domain.HashContent([]byte("a-v1"))); err != nil {
		t.Fatal(err)
	}
	versionsA, err := s.Versions(ctx, planA.ID)
	if err != nil || len(versionsA) != 1 {
		t.Fatalf("versions = %+v, %v", versionsA, err)
	}
	if _, err := s.RegisterVersion(ctx, planA.ID, client.VersionRegistration{Content: []byte("a-v2"), Base: versionsA[0].Hash}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateThread(ctx, planA.ID, domain.HashContent([]byte("a-v2")), anchor, "open on A"); err != nil {
		t.Fatal(err)
	}
	resolvedThA, err := s.CreateThread(ctx, planA.ID, domain.HashContent([]byte("a-v2")), anchor, "resolved on A")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetResolved(ctx, planA.ID, resolvedThA.ID, true); err != nil {
		t.Fatal(err)
	}

	// Plan B: one version, one open thread, an approval on the (only, hence
	// current) tip.
	planB, err := s.CreatePlan(ctx, "Plan B", "/plans/b.md", "", []byte("b-v1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateThread(ctx, planB.ID, domain.HashContent([]byte("b-v1")), anchor, "open on B"); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, planB.ID, domain.HashContent([]byte("b-v1"))); err != nil {
		t.Fatal(err)
	}
	approvalsB, err := s.Approvals(ctx, planB.ID)
	if err != nil || len(approvalsB) != 1 {
		t.Fatalf("approvals = %+v, %v", approvalsB, err)
	}

	plans, err := s.ListPlans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[domain.PlanID]domain.Plan{}
	for _, p := range plans {
		byID[p.ID] = p
	}
	gotA, gotB := byID[planA.ID], byID[planB.ID]

	if gotA.OpenThreads != 1 || gotA.TotalThreads != 2 {
		t.Errorf("plan A open/total = %d/%d, want 1/2", gotA.OpenThreads, gotA.TotalThreads)
	}
	if gotA.TipApproved {
		t.Error("plan A's approval targets a version that is no longer the tip; TipApproved must be false")
	}
	if gotB.OpenThreads != 1 || gotB.TotalThreads != 1 {
		t.Errorf("plan B open/total = %d/%d, want 1/1", gotB.OpenThreads, gotB.TotalThreads)
	}
	if !gotB.TipApproved {
		t.Error("plan B's approval targets its actual tip; TipApproved must be true")
	}

	// Plan A's most recent fact is resolvedThA's own comment (created after
	// its approval and both of its version registrations); plan B's is its
	// approval (created after its one thread's comment).
	wantA := resolvedThA.Comments[0].CreatedAt
	if !gotA.LastActivityAt.Equal(wantA) {
		t.Errorf("plan A LastActivityAt = %v, want %v (its own most recent fact)", gotA.LastActivityAt, wantA)
	}
	wantB := approvalsB[0].CreatedAt
	if !gotB.LastActivityAt.Equal(wantB) {
		t.Errorf("plan B LastActivityAt = %v, want %v (its own most recent fact)", gotB.LastActivityAt, wantB)
	}

	// CreatedAt is the FIRST version's registration and not the last, which
	// plan A is the fixture for: it has two versions, so a derivation reading
	// the tip would answer the second one's instant instead. It is also NOT
	// LastActivityAt -- plan A's most recent fact is a comment registered after
	// both versions -- so the two fields have to differ here for this to be
	// checking anything at all.
	if !gotA.CreatedAt.Equal(versionsA[0].RegisteredAt) {
		t.Errorf("plan A CreatedAt = %v, want its FIRST version's %v", gotA.CreatedAt, versionsA[0].RegisteredAt)
	}
	if gotA.CreatedAt.Equal(gotA.LastActivityAt) {
		t.Errorf("plan A's CreatedAt and LastActivityAt are both %v; the fixture cannot tell the two derivations apart", gotA.CreatedAt)
	}
}

func TestThreadFlow(t *testing.T) {
	s, _ := newStore(t)
	ctx := attrCtx("alice")
	plan, err := s.CreatePlan(ctx, "Foo", "/plans/foo.md", "", []byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}

	thread, err := s.CreateThread(ctx, plan.ID, "aaa", anchor, "this seems wrong")
	if err != nil {
		t.Fatal(err)
	}
	if thread.AnchorHash != "aaa" || len(thread.Comments) != 1 || thread.Comments[0].Attribution.ActorDisplay != "alice" {
		t.Fatalf("thread = %+v", thread)
	}

	reply, err := s.Reply(ctx, plan.ID, thread.ID, "agreed, will fix")
	if err != nil || reply.Body != "agreed, will fix" || reply.Attribution.ActorDisplay != "alice" {
		t.Fatalf("reply = %+v, %v", reply, err)
	}

	if err := s.SetResolved(ctx, plan.ID, thread.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Rehome(ctx, plan.ID, thread.ID, "bbb", reanchor.Anchor{HeadingPath: []string{"Plan"}, Span: "moved sentence"}); err != nil {
		t.Fatal(err)
	}

	threads, err := s.Threads(ctx, plan.ID)
	if err != nil || len(threads) != 1 {
		t.Fatalf("threads = %v, %v", threads, err)
	}
	got := threads[0]
	if !got.Resolved || got.AnchorHash != "bbb" || got.Anchor.Span != "moved sentence" || len(got.Comments) != 2 {
		t.Fatalf("thread after updates = %+v", got)
	}

	if _, err := s.Reply(ctx, plan.ID, "thread-nope", "x"); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestThreadWritesRefuseAThreadOfAnotherPlan is client.PlanService's own
// unknown-id contract for the four thread-keyed writes, proven on this
// implementation: a real, live thread named alongside a real, live OTHER plan's
// id must refuse exactly as an absent thread does, and must leave the thread
// untouched.
//
// Enumerated over all four writes, not spot-checked: each is its own call, and a
// scoping proven on one is not proof for the other three. Both plans are real
// and this store holds both, so nothing but findThread's plan scoping can be
// what refuses.
//
// THE CONTROL RUNS ON A SECOND THREAD OF PLAN B, not on the thread the refusal
// then names, and DeleteThread is why: a control that DESTROYED its subject
// would leave the refusal below aimed at a thread that is already gone, refused
// for the wrong reason.
func TestThreadWritesRefuseAThreadOfAnotherPlan(t *testing.T) {
	ctx := attrCtx("alice")

	for _, tc := range []struct {
		name  string
		write func(s *Store, id domain.PlanID, tid domain.ThreadID) error
	}{
		{"Reply", func(s *Store, id domain.PlanID, tid domain.ThreadID) error {
			_, err := s.Reply(ctx, id, tid, "a reply")
			return err
		}},
		{"SetResolved", func(s *Store, id domain.PlanID, tid domain.ThreadID) error {
			return s.SetResolved(ctx, id, tid, true)
		}},
		{"Rehome", func(s *Store, id domain.PlanID, tid domain.ThreadID) error {
			return s.Rehome(ctx, id, tid, "ccc", reanchor.Anchor{Span: "moved elsewhere"})
		}},
		{"DeleteThread", func(s *Store, id domain.PlanID, tid domain.ThreadID) error {
			return s.DeleteThread(ctx, id, tid)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newStore(t)
			planA, err := s.CreatePlan(ctx, "A", "/plans/a.md", "", []byte("aaa"))
			if err != nil {
				t.Fatal(err)
			}
			planB, err := s.CreatePlan(ctx, "B", "/plans/b.md", "", []byte("bbb"))
			if err != nil {
				t.Fatal(err)
			}
			threadB, err := s.CreateThread(ctx, planB.ID, "bbb", anchor, "plan B's own thread")
			if err != nil {
				t.Fatal(err)
			}
			controlB, err := s.CreateThread(ctx, planB.ID, "bbb", anchor, "plan B's control thread")
			if err != nil {
				t.Fatal(err)
			}

			// Control: through its OWN plan the same write must succeed, or
			// the refusal below would prove nothing. On its own thread, per
			// this test's own comment above.
			if err := tc.write(s, planB.ID, controlB.ID); err != nil {
				t.Fatalf("control (the thread's own plan): %v", err)
			}
			before, err := s.Threads(ctx, planB.ID)
			if err != nil {
				t.Fatal(err)
			}

			// The refusal's own subject is still there to be refused ABOUT,
			// which is what keeps the comparison below from comparing two
			// absences.
			present := false
			for _, th := range before {
				present = present || th.ID == threadB.ID
			}
			if !present {
				t.Fatalf("the thread the refusal names is already gone before the refusal: %+v", before)
			}

			if err := tc.write(s, planA.ID, threadB.ID); !errors.Is(err, client.ErrNotFound) {
				t.Fatalf("err = %v, want client.ErrNotFound -- a thread of another plan must refuse "+
					"exactly as an absent thread does", err)
			}
			after, err := s.Threads(ctx, planB.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("plan B's thread changed despite the refusal:\nbefore: %+v\nafter:  %+v", before, after)
			}
			// The refusal must also not have invented anything on plan A.
			threadsA, err := s.Threads(ctx, planA.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(threadsA) != 0 {
				t.Fatalf("plan A gained threads from the refused write: %+v", threadsA)
			}
		})
	}
}

// TestDeleteThreadRemovesTheThreadAndItsComments aims every assertion at the
// failure that looks exactly like success: a delete that removes nothing returns
// nil too.
//
// The target carries a REPLY, so "its comments went with it" is a claim about
// more than the head comment. There is no comment collection of its own to read
// back here, so the reply is proven gone the only way this store can prove it:
// nothing can reach the thread that held it any more.
//
// The SIBLING is the assertion an over-broad filter fails and nothing else here
// would: a delete matching on tid alone, or one that rebuilt the slice from the
// wrong predicate, still answers nil and still empties the plan. The other
// plan's thread widens that by one step.
//
// A REPEAT is client.ErrNotFound rather than a second nil (which would say a
// delete that removed nothing succeeded) and rather than a sentinel of its
// own: a deleted thread leaves no state to report.
//
// Reopening the store is what proves the removal reached DISK.
func TestDeleteThreadRemovesTheThreadAndItsComments(t *testing.T) {
	s, path := newStore(t)
	ctx := attrCtx("alice")

	planA, err := s.CreatePlan(ctx, "A", "/plans/a.md", "", []byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}
	planB, err := s.CreatePlan(ctx, "B", "/plans/b.md", "", []byte("bbb"))
	if err != nil {
		t.Fatal(err)
	}

	target, err := s.CreateThread(ctx, planA.ID, "aaa", anchor, "the deploy step needs a rollback")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reply(ctx, planA.ID, target.ID, "agreed, and here is the key: hunter2"); err != nil {
		t.Fatal(err)
	}
	sibling, err := s.CreateThread(ctx, planA.ID, "aaa", anchor, "a second note")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reply(ctx, planA.ID, sibling.ID, "still open"); err != nil {
		t.Fatal(err)
	}
	otherPlans, err := s.CreateThread(ctx, planB.ID, "bbb", anchor, "b's own note")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteThread(ctx, planA.ID, target.ID); err != nil {
		t.Fatalf("DeleteThread: %v", err)
	}

	threadsA, err := s.Threads(ctx, planA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(threadsA) != 1 || threadsA[0].ID != sibling.ID {
		t.Fatalf("plan A's threads after the delete = %+v, want only the sibling %s", threadsA, sibling.ID)
	}
	if len(threadsA[0].Comments) != 2 {
		t.Fatalf("the sibling's comments = %+v, want its head and its reply untouched", threadsA[0].Comments)
	}

	if _, err := s.Reply(ctx, planA.ID, target.ID, "one more"); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("replying to the deleted thread = %v, want client.ErrNotFound -- the comments went with it", err)
	}

	if err := s.DeleteThread(ctx, planA.ID, target.ID); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("a second DeleteThread = %v, want client.ErrNotFound", err)
	}

	threadsB, err := s.Threads(ctx, planB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(threadsB) != 1 || threadsB[0].ID != otherPlans.ID {
		t.Fatalf("plan B's threads after plan A's delete = %+v, want its own thread %s", threadsB, otherPlans.ID)
	}

	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.Threads(ctx, planA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 1 || persisted[0].ID != sibling.ID {
		t.Fatalf("plan A's threads after reopening the store = %+v, want only the sibling", persisted)
	}
}

func TestApprovals(t *testing.T) {
	s, _ := newStore(t)
	ctx := attrCtx("alice")
	plan, err := s.CreatePlan(ctx, "Foo", "/plans/foo.md", "", []byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, plan.ID, "aaa"); err != nil {
		t.Fatal(err)
	}
	approvals, err := s.Approvals(ctx, plan.ID)
	if err != nil || len(approvals) != 1 || approvals[0].Attribution.ActorDisplay != "alice" || approvals[0].Hash != "aaa" {
		t.Fatalf("approvals = %+v, %v", approvals, err)
	}
}

// TestApproveIsIdempotentPerActor pins Approve's (plan, hash, actor)
// idempotency (see client.PlanService's doc comment on Approve): a repeat
// approval from the SAME actor, of the SAME plan and hash, is a no-op that
// leaves the first call's fact -- including its agent -- unchanged, even
// though the retry names a DIFFERENT agent. All three components of the key
// are exercised, each ruling out a narrower predicate that would still pass
// the others: a DIFFERENT actor approving the identical (plan, hash) is a
// distinct fact, proving the key is not just (plan, hash); and the SAME
// actor approving the identical hash on a SECOND, distinct plan is also a
// distinct fact, proving the key is not just (hash, actor) -- the plan
// really is part of it, not just along for the ride because every earlier
// case in this test used a single plan.
func TestApproveIsIdempotentPerActor(t *testing.T) {
	s, _ := newStore(t)
	plan, err := s.CreatePlan(attrCtx("alice"), "Foo", "/plans/foo.md", "", []byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}

	actor1First := client.WithAttribution(context.Background(), domain.Attribution{ActorID: "u1", ActorLogin: "alice-codes", ActorDisplay: "Alice", Agent: "agent-1"})
	if err := s.Approve(actor1First, plan.ID, "aaa"); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	actor1Retry := client.WithAttribution(context.Background(), domain.Attribution{ActorID: "u1", ActorLogin: "alice-codes", ActorDisplay: "Alice", Agent: "agent-2"})
	if err := s.Approve(actor1Retry, plan.ID, "aaa"); err != nil {
		t.Fatalf("repeat approve: %v", err)
	}
	actor2 := client.WithAttribution(context.Background(), domain.Attribution{ActorID: "u2", ActorLogin: "priya-r", ActorDisplay: "Priya", Agent: ""})
	if err := s.Approve(actor2, plan.ID, "aaa"); err != nil {
		t.Fatalf("second actor's approve: %v", err)
	}

	approvals, err := s.Approvals(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("Approvals: %v", err)
	}
	if len(approvals) != 2 {
		t.Fatalf("approvals = %+v, want 2 (one per distinct actor -- the same actor's retry folded into the first)", approvals)
	}
	for _, a := range approvals {
		if a.Attribution.ActorID == "u1" && a.Attribution.Agent != "agent-1" {
			t.Errorf("actor u1's recorded agent = %q, want the FIRST call's agent-1, unchanged by the retry's agent-2", a.Attribution.Agent)
		}
	}

	// The SAME actor (u1) approving the SAME hash ("aaa") again, but on a
	// SECOND, distinct plan: this must be recorded as its own fact, not
	// folded into u1's existing approval on planA above. Without a.Plan ==
	// id in the dedup predicate, a check scoped to (hash, actor) alone would
	// treat this as the identical repeat proven above and silently drop it
	// -- exactly the bug this case exists to catch, since len(approvals)
	// above only ever exercised ONE plan and could not distinguish the two.
	planB, err := s.CreatePlan(attrCtx("someone else"), "Bar", "/plans/bar.md", "", []byte("bbb"))
	if err != nil {
		t.Fatal(err)
	}
	actor1OnPlanB := client.WithAttribution(context.Background(), domain.Attribution{ActorID: "u1", ActorLogin: "alice-codes", ActorDisplay: "Alice", Agent: "agent-3"})
	if err := s.Approve(actor1OnPlanB, planB.ID, "aaa"); err != nil {
		t.Fatalf("actor u1 approving planB: %v", err)
	}

	approvalsB, err := s.Approvals(context.Background(), planB.ID)
	if err != nil {
		t.Fatalf("Approvals(planB): %v", err)
	}
	if len(approvalsB) != 1 || approvalsB[0].Attribution.Agent != "agent-3" || approvalsB[0].Hash != "aaa" {
		t.Fatalf("Approvals(planB) = %+v, want exactly one entry with agent-3 and hash aaa -- "+
			"u1's approval on planA above must not have suppressed this one", approvalsB)
	}
	approvalsAAfter, err := s.Approvals(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("Approvals(planA) after planB's approve: %v", err)
	}
	if len(approvalsAAfter) != 2 {
		t.Fatalf("Approvals(planA) = %+v, want still 2 -- planB's approval must not leak into planA", approvalsAAfter)
	}
}

func TestCreatePlanSourceHintUniqueness(t *testing.T) {
	s, _ := newStore(t)
	ctx := attrCtx("alice")

	first, err := s.CreatePlan(ctx, "Foo", "/plans/foo.md", "", []byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}

	second, err := s.CreatePlan(ctx, "Foo Duplicate", "/plans/foo.md", "", []byte("bbb"))
	if err != nil {
		t.Fatal(err)
	}

	if first.ID != second.ID {
		t.Fatalf("duplicate CreatePlan returned different IDs: %s vs %s", first.ID, second.ID)
	}

	plans, err := s.ListPlans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("want exactly 1 plan after duplicate CreatePlan, got %d", len(plans))
	}
}

// TestCreatePlanReturnExistingBranchCarriesDerivedFacts proves the SECOND of
// CreatePlan's two return paths -- the return-existing branch taken when
// sourceHint already names a plan -- routes through planWithDerivedFacts exactly
// like the fresh-create branch, not just like ListPlans/ResolvePlan. It is not a
// rare corner: it is the convergence path EVERY racing caller of CreatePlan
// takes, so a caller that hit it against a plan with real threads/approvals
// already accrued would silently get back zero counts and a zero
// LastActivityAt.
func TestCreatePlanReturnExistingBranchCarriesDerivedFacts(t *testing.T) {
	s, _ := newStore(t)
	ctx := attrCtx("alice")

	first, err := s.CreatePlan(ctx, "Foo", "/plans/foo.md", "", []byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateThread(ctx, first.ID, domain.HashContent([]byte("aaa")), anchor, "a thread"); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, first.ID, domain.HashContent([]byte("aaa"))); err != nil {
		t.Fatal(err)
	}

	// A second CreatePlan against the SAME sourceHint takes the
	// return-existing branch (TestCreatePlanSourceHintUniqueness already
	// pins that it never mints a new id); this test is scoped to whether
	// that branch's RETURN VALUE carries the plan's real facts.
	second, err := s.CreatePlan(ctx, "Foo Duplicate", "/plans/foo.md", "", []byte("bbb"))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("test setup: return-existing branch minted a new plan: %s vs %s", second.ID, first.ID)
	}
	if second.OpenThreads != 1 || second.TotalThreads != 1 {
		t.Errorf("return-existing CreatePlan open/total = %d/%d, want 1/1", second.OpenThreads, second.TotalThreads)
	}
	if !second.TipApproved {
		t.Error("return-existing CreatePlan TipApproved = false, want true (its approval targets its only, hence current, version)")
	}
	if second.LastActivityAt.IsZero() {
		t.Error("return-existing CreatePlan LastActivityAt is zero, want the plan's most recent fact")
	}
}

// TestResolvePlanCarriesDerivedFacts proves ResolvePlan's own return value
// routes through planWithDerivedFacts, the same as ListPlans and CreatePlan.
// session.Session.Plan is filled from ResolvePlan (see session.Open), so a
// silent regression here would leave the Plan of every session opened by path
// carrying zero counts and a zero sort key, with nothing naming the cause.
func TestResolvePlanCarriesDerivedFacts(t *testing.T) {
	s, _ := newStore(t)
	ctx := attrCtx("alice")

	plan, err := s.CreatePlan(ctx, "Foo", "/plans/foo.md", "", []byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateThread(ctx, plan.ID, domain.HashContent([]byte("aaa")), anchor, "a thread"); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, plan.ID, domain.HashContent([]byte("aaa"))); err != nil {
		t.Fatal(err)
	}

	got, err := s.ResolvePlan(ctx, "/plans/foo.md")
	if err != nil {
		t.Fatal(err)
	}
	if got.OpenThreads != 1 || got.TotalThreads != 1 {
		t.Errorf("ResolvePlan open/total = %d/%d, want 1/1", got.OpenThreads, got.TotalThreads)
	}
	if !got.TipApproved {
		t.Error("ResolvePlan TipApproved = false, want true (its approval targets its only, hence current, version)")
	}
	if got.LastActivityAt.IsZero() {
		t.Error("ResolvePlan LastActivityAt is zero, want the plan's most recent fact")
	}
}

// TestPlanByIDAnswersFromThePlanSetAndCarriesDerivedFacts covers all three of
// this method's outcomes in one pass, and the middle row is the one worth
// having: a plan whose version log is EMPTY is still found, because this Store
// answers from its plan set and never from the version log. A state file can
// hold such a plan, and a log-based predicate would report it as nonexistent.
//
// The found row also asserts the derived facts: routing into
// planWithDerivedFacts is invisible to every assertion that only checks
// identity, and this is a fourth caller of it.
func TestPlanByIDAnswersFromThePlanSetAndCarriesDerivedFacts(t *testing.T) {
	s, _ := newStore(t)
	ctx := attrCtx("alice")

	withFacts, err := s.CreatePlan(ctx, "Foo", "/plans/foo.md", "repo", []byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateThread(ctx, withFacts.ID, domain.HashContent([]byte("aaa")), anchor, "a thread"); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, withFacts.ID, domain.HashContent([]byte("aaa"))); err != nil {
		t.Fatal(err)
	}
	const noVersions = domain.PlanID("l_noversions")
	if err := s.withLock(func() error {
		s.st.Plans = append(s.st.Plans, domain.Plan{ID: noVersions, Title: "No versions", SourceHint: "/work/rollout.md"})
		return s.save()
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("a plan with facts answers with them derived", func(t *testing.T) {
		got, err := s.PlanByID(ctx, withFacts.ID)
		if err != nil {
			t.Fatalf("PlanByID: %v", err)
		}
		if got.ID != withFacts.ID || got.Title != "Foo" || got.RepoHint != "repo" {
			t.Errorf("PlanByID = %+v, want the seeded plan", got)
		}
		if got.OpenThreads != 1 || got.TotalThreads != 1 || !got.TipApproved || got.LastActivityAt.IsZero() {
			t.Errorf("PlanByID = %+v, want the derived facts planWithDerivedFacts computes", got)
		}
	})

	t.Run("a plan with no registered version is still found", func(t *testing.T) {
		versions, err := s.Versions(ctx, noVersions)
		if err != nil {
			t.Fatal(err)
		}
		if len(versions) != 0 {
			t.Fatalf("fixture is not the case this row is about: %d versions, want 0", len(versions))
		}
		got, err := s.PlanByID(ctx, noVersions)
		if err != nil {
			t.Fatalf("PlanByID: err = %v, want the plan -- an empty version log is not absence", err)
		}
		if got.ID != noVersions {
			t.Errorf("PlanByID = %+v, want %s", got, noVersions)
		}
	})

	t.Run("an unknown id is ErrNotFound", func(t *testing.T) {
		got, err := s.PlanByID(ctx, "l_nosuchplan")
		if !errors.Is(err, client.ErrNotFound) {
			t.Fatalf("PlanByID: err = %v, want errors.Is(err, client.ErrNotFound)", err)
		}
		if !reflect.DeepEqual(got, domain.Plan{}) {
			t.Errorf("PlanByID = %+v on a miss, want the zero plan", got)
		}
	})
}

// TestCreatePlanSourcelessNeverCollides pins the sourceless-create fix: an
// empty sourceHint is never a match, so two sourceless CreatePlan calls in a
// row create two distinct plans rather than the second silently returning
// the first (the bug: exact-string equality on SourceHint matched "" == "").
func TestCreatePlanSourcelessNeverCollides(t *testing.T) {
	s, _ := newStore(t)
	ctx := attrCtx("alice")

	first, err := s.CreatePlan(ctx, "First", "", "", []byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreatePlan(ctx, "Second", "", "", []byte("bbb"))
	if err != nil {
		t.Fatal(err)
	}

	if first.ID == second.ID {
		t.Fatalf("sourceless CreatePlan calls collided onto one plan: %s", first.ID)
	}
	plans, err := s.ListPlans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 {
		t.Fatalf("want 2 distinct sourceless plans, got %d: %+v", len(plans), plans)
	}
}

// TestResolvePlanEmptySourceHintNeverMatches pins ResolvePlan's other half
// of the never-equal rule: even once a sourceless plan exists,
// ResolvePlan("") must still miss rather than arbitrarily returning it —
// consistent with CreatePlan("") never treating "" as an identity either.
func TestResolvePlanEmptySourceHintNeverMatches(t *testing.T) {
	s, _ := newStore(t)
	ctx := attrCtx("alice")

	if _, err := s.CreatePlan(ctx, "Sourceless", "", "", []byte("aaa")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolvePlan(ctx, ""); !errors.Is(err, client.ErrNoPlan) {
		t.Fatalf("err = %v, want ErrNoPlan even with a sourceless plan present", err)
	}
}

// TestRegisterVersionComparesAndSwaps enumerates every cell of the
// (base, hash) x (tip) space rather than the one reproduction. The order of
// the arms is the subtle part: the no-op check comes FIRST, so registering
// content that is already the tip succeeds even from a caller whose base is
// stale.
func TestRegisterVersionComparesAndSwaps(t *testing.T) {
	ctx := attrCtx("alice")
	v1, v2, v3 := []byte("v1"), []byte("v2"), []byte("v3")
	h1, h2, h3 := domain.HashContent(v1), domain.HashContent(v2), domain.HashContent(v3)

	setup := func(t *testing.T) (*Store, domain.PlanID) {
		t.Helper()
		s, _ := newStore(t)
		plan, err := s.CreatePlan(ctx, "Plan", "/plan.md", "", v1)
		if err != nil {
			t.Fatal(err)
		}
		return s, plan.ID
	}

	t.Run("base matches tip appends", func(t *testing.T) {
		s, id := setup(t)
		ref, err := s.RegisterVersion(ctx, id, client.VersionRegistration{Content: v2, Base: h1})
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		if ref.Hash != h2 || ref.Seq != 2 {
			t.Fatalf("ref = %+v, want {%s 2}", ref, h2)
		}
		vs, _ := s.Versions(ctx, id)
		if len(vs) != 2 || vs[1].Hash != h2 {
			t.Fatalf("versions = %v, want v1 then v2", vs)
		}
		// Seq agrees with position: 1-based, gapless, and derived from where
		// each entry landed in the append-only log, not carried in from
		// anywhere else.
		if vs[0].Seq != 1 || vs[1].Seq != 2 {
			t.Fatalf("seqs = [%d %d], want [1 2]", vs[0].Seq, vs[1].Seq)
		}
	})

	t.Run("stale base conflicts and carries the tip", func(t *testing.T) {
		s, id := setup(t)
		if _, err := s.RegisterVersion(ctx, id, client.VersionRegistration{Content: v2, Base: h1}); err != nil {
			t.Fatal(err)
		}
		ref, err := s.RegisterVersion(ctx, id, client.VersionRegistration{Content: v3, Base: h1})
		var conflict *client.ConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("err = %v, want *client.ConflictError", err)
		}
		if conflict.Tip.Hash != h2 || conflict.Tip.Seq != 2 {
			t.Fatalf("conflict.Tip = %+v, want {%s 2}", conflict.Tip, h2)
		}
		if ref != (domain.VersionRef{}) {
			t.Fatalf("ref on a conflict = %+v, want the zero value -- the tip travels on the error, not the ordinary return", ref)
		}
		vs, _ := s.Versions(ctx, id)
		if len(vs) != 2 {
			t.Fatalf("a conflicted register must not append: %v", vs)
		}
		// Content a rejected register carried must never land in s.cas, or
		// "content enters the store only when a review fact references it"
		// would be false the instant a conflict's content got Put anyway.
		if has, err := s.cas.Has(ctx, h3); err != nil || has {
			t.Fatalf("s.cas.Has(v3) after a conflicted register = %v, %v, want false, nil -- rejected content must never reach local storage", has, err)
		}
	})

	t.Run("hash already the tip is a no-op even with a stale base", func(t *testing.T) {
		s, id := setup(t)
		if _, err := s.RegisterVersion(ctx, id, client.VersionRegistration{Content: v2, Base: h1}); err != nil {
			t.Fatal(err)
		}
		// Base is two versions stale, but v2 IS the tip: nothing to do.
		ref, err := s.RegisterVersion(ctx, id, client.VersionRegistration{Content: v2, Base: ""})
		if err != nil {
			t.Fatalf("re-registering the tip should be a no-op, got %v", err)
		}
		// A no-op success still answers the tip, exactly like a real append
		// would: a caller must not be able to tell the two apart by whether
		// the returned ref came back zero.
		if ref.Hash != h2 || ref.Seq != 2 {
			t.Fatalf("no-op ref = %+v, want {%s 2}", ref, h2)
		}
		vs, _ := s.Versions(ctx, id)
		if len(vs) != 2 {
			t.Fatalf("no-op must not append: %v", vs)
		}
	})

	// The no-op arm fires ONLY when the hash is already the LATEST version.
	// A hash that appeared earlier in history -- an agent reverting -- must
	// append again rather than silently no-opping, so a plan can be revived
	// to an older version by re-registering its hash.
	t.Run("a revert re-promotes an older hash", func(t *testing.T) {
		s, id := setup(t)
		if _, err := s.RegisterVersion(ctx, id, client.VersionRegistration{Content: v2, Base: h1}); err != nil {
			t.Fatal(err)
		}
		// v1 is in the log but is not the tip, so this is a legitimate revert.
		ref, err := s.RegisterVersion(ctx, id, client.VersionRegistration{Content: v1, Base: h2})
		if err != nil {
			t.Fatalf("revert: %v", err)
		}
		if ref.Hash != h1 || ref.Seq != 3 {
			t.Fatalf("revert ref = %+v, want {%s 3}", ref, h1)
		}
		vs, _ := s.Versions(ctx, id)
		if len(vs) != 3 || vs[2].Hash != h1 {
			t.Fatalf("versions = %v, want v1,v2,v1", vs)
		}
		// The reverted entry gets a NEW, higher seq (3) rather than reusing
		// the seq (1) its hash first appeared under: Seq guards a position in
		// THIS append-only log, not an identity for the hash, and the same
		// hash legitimately occupies two positions here.
		if vs[2].Seq != 3 {
			t.Fatalf("reverted entry's Seq = %d, want 3 (new and higher, not a reuse of %d)", vs[2].Seq, vs[0].Seq)
		}
		if vs[0].Seq == vs[2].Seq {
			t.Fatalf("revert reused seq %d instead of assigning a new one -- seq cannot identify a point in the log (see VersionRef's doc comment)", vs[0].Seq)
		}
	})

	t.Run("empty base against an existing plan conflicts", func(t *testing.T) {
		s, id := setup(t)
		ref, err := s.RegisterVersion(ctx, id, client.VersionRegistration{Content: v2, Base: ""})
		var conflict *client.ConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("err = %v, want *client.ConflictError", err)
		}
		if conflict.Tip.Hash != h1 || conflict.Tip.Seq != 1 {
			t.Fatalf("conflict.Tip = %+v, want {%s 1}", conflict.Tip, h1)
		}
		if ref != (domain.VersionRef{}) {
			t.Fatalf("ref on a conflict = %+v, want the zero value", ref)
		}
	})

	t.Run("unknown plan is ErrNotFound, not a conflict", func(t *testing.T) {
		s, _ := setup(t)
		_, err := s.RegisterVersion(ctx, "l_nope", client.VersionRegistration{Content: v2, Base: h1})
		if !errors.Is(err, client.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

// TestRegisterVersionSeqIsPositionOneBasedAndGapless pins the general form of
// what TestRegisterVersionComparesAndSwaps's subtests only check two or three
// entries of: across a longer append-only log, every entry's Seq is exactly its
// 1-based position in the returned slice -- no gaps, no reuse. Position IS the
// Seq this Store assigns: one writer under the flock means an entry's place
// in the log and its assigned Seq are the same fact, which is also why this
// asserts Seq against position rather than an independently-tracked counter.
func TestRegisterVersionSeqIsPositionOneBasedAndGapless(t *testing.T) {
	ctx := attrCtx("alice")
	s, _ := newStore(t)
	plan, err := s.CreatePlan(ctx, "Plan", "/plan.md", "", []byte("v1"))
	if err != nil {
		t.Fatal(err)
	}

	base := domain.HashContent([]byte("v1"))
	for _, content := range [][]byte{[]byte("v2"), []byte("v3"), []byte("v4")} {
		ref, err := s.RegisterVersion(ctx, plan.ID, client.VersionRegistration{Content: content, Base: base})
		if err != nil {
			t.Fatalf("register %s: %v", content, err)
		}
		base = domain.HashContent(content)
		if ref.Hash != base {
			t.Fatalf("ref.Hash = %s, want %s", ref.Hash, base)
		}
	}

	vs, err := s.Versions(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.ContentHash{
		domain.HashContent([]byte("v1")), domain.HashContent([]byte("v2")),
		domain.HashContent([]byte("v3")), domain.HashContent([]byte("v4")),
	}
	if len(vs) != len(want) {
		t.Fatalf("versions = %+v, want %d entries", vs, len(want))
	}
	for i, v := range vs {
		if v.Hash != want[i] {
			t.Fatalf("versions[%d].Hash = %q, want %q -- versions must come back in seq order", i, v.Hash, want[i])
		}
		if v.Seq != i+1 {
			t.Fatalf("versions[%d].Seq = %d, want %d (1-based, gapless)", i, v.Seq, i+1)
		}
	}
}

// TestRegisterVersionDerivesHashMatchingDomainHashContent pins one hazard by
// name: the hash a registered version carries must be exactly
// domain.HashContent(r.Content) for content shapes a caller-supplied hash never
// had to agree with the bytes on. Trailing whitespace and a trailing newline are
// exactly the two shapes a stray strings.TrimSpace anywhere in this path would
// silently normalize away, corrupting identity without ever producing an error.
// CreatePlan is the derivation set's other member and gets the identical pin
// against the same table, in this package.
func TestRegisterVersionDerivesHashMatchingDomainHashContent(t *testing.T) {
	ctx := attrCtx("alice")
	tests := []struct {
		name    string
		content []byte
	}{
		{"trailing whitespace", []byte("some plan content   ")},
		{"trailing newline", []byte("some plan content\n")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want := domain.HashContent(tc.content)

			s, _ := newStore(t)
			plan, err := s.CreatePlan(ctx, "Plan", "/plan.md", "", []byte("v0"))
			if err != nil {
				t.Fatal(err)
			}
			ref, err := s.RegisterVersion(ctx, plan.ID, client.VersionRegistration{
				Content: tc.content, Base: domain.HashContent([]byte("v0")),
			})
			if err != nil {
				t.Fatalf("RegisterVersion: %v", err)
			}
			if ref.Hash != want {
				t.Fatalf("RegisterVersion ref.Hash = %q, want %q (domain.HashContent(content), untrimmed)", ref.Hash, want)
			}
			got, err := s.Content(ctx, plan.ID, want)
			if err != nil {
				t.Fatalf("Content(%s): %v", want, err)
			}
			if string(got) != string(tc.content) {
				t.Fatalf("Content(%s) = %q, want %q", want, got, tc.content)
			}

			// CreatePlan derives independently, on a fresh plan/store, so a
			// mismatch here cannot be masked by whatever RegisterVersion did
			// above.
			s2, _ := newStore(t)
			created, err := s2.CreatePlan(ctx, "Plan2", "/plan2.md", "", tc.content)
			if err != nil {
				t.Fatalf("CreatePlan: %v", err)
			}
			versions, err := s2.Versions(ctx, created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(versions) != 1 || versions[0].Hash != want {
				t.Fatalf("CreatePlan's initial version = %+v, want a single version with hash %q", versions, want)
			}
			got2, err := s2.Content(ctx, created.ID, want)
			if err != nil {
				t.Fatalf("Content(%s) after CreatePlan: %v", want, err)
			}
			if string(got2) != string(tc.content) {
				t.Fatalf("Content(%s) after CreatePlan = %q, want %q", want, got2, tc.content)
			}
		})
	}
}

// TestContentReadsBytesForARegisteredHash proves the success path: a hash this
// plan's own log carries answers with the exact bytes s.cas holds for it.
// CreatePlan puts content into s.cas itself as it creates the plan's initial
// version, so no separate seeding step is needed here.
func TestContentReadsBytesForARegisteredHash(t *testing.T) {
	ctx := attrCtx("alice")
	s, _ := newStore(t)
	content := []byte("the plan's real content, byte for byte")
	plan, err := s.CreatePlan(ctx, "Plan", "/plan.md", "", content)
	if err != nil {
		t.Fatal(err)
	}
	h := domain.HashContent(content)

	got, err := s.Content(ctx, plan.ID, h)
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("Content() = %q, want %q", got, content)
	}
}

// TestContentUnknownHashIsNotFound pins client.PlanService's Content contract:
// a hash id's own log never registered is ErrNotFound.
//
// The "registered under a different plan" case is the sharper version of the
// same property: s.cas is one flat store shared by every plan this machine
// holds, so a hash present in cas at all must still miss here if THIS plan's log
// never named it.
func TestContentUnknownHashIsNotFound(t *testing.T) {
	ctx := attrCtx("alice")
	s, _ := newStore(t)
	if _, err := s.CreatePlan(ctx, "Other", "/other.md", "", []byte("other-v1")); err != nil {
		t.Fatal(err)
	}
	plan, err := s.CreatePlan(ctx, "Plan", "/plan.md", "", []byte("v1"))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		id   domain.PlanID
		hash domain.ContentHash
	}{
		{"a hash never registered anywhere", plan.ID, "never-registered"},
		{"a hash registered, but under a different plan", plan.ID, domain.HashContent([]byte("other-v1"))},
		{"an unknown plan id entirely", "l_nope", domain.HashContent([]byte("v1"))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Content(ctx, tc.id, tc.hash)
			if !errors.Is(err, client.ErrNotFound) {
				t.Fatalf("Content(%s, %s): err = %v, want errors.Is(err, client.ErrNotFound)", tc.id, tc.hash, err)
			}
		})
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	s, path := newStore(t)
	ctx := attrCtx("alice")
	plan, err := s.CreatePlan(ctx, "Foo", "/plans/foo.md", "", []byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateThread(ctx, plan.ID, "aaa", anchor, "note"); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := reopened.ResolvePlan(ctx, "/plans/foo.md")
	if err != nil || resolved.ID != plan.ID {
		t.Fatalf("resolve after reopen = %+v, %v", resolved, err)
	}
	threads, err := reopened.Threads(ctx, plan.ID)
	if err != nil || len(threads) != 1 || threads[0].Comments[0].Attribution.ActorDisplay != "alice" {
		t.Fatalf("threads after reopen = %+v, %v", threads, err)
	}
}

// TestReloadMigratesLegacyPathHint pins the read-side migration: a state.json
// written before the PathHint→SourceHint rename (raw JSON, the old field
// name — not a marshaled domain.Plan, which would already use the new name
// and make this test vacuous) still resolves to a populated SourceHint. The
// migration is self-healing: the very next write re-serializes the plan
// under the new field name, so a fresh Store opened afterward finds
// "SourceHint" on disk with no migration needed.
func TestReloadMigratesLegacyPathHint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := `{
		"Plans": [{"ID": "plan-1", "Title": "Legacy", "PathHint": "/tmp/x.md", "RepoHint": "", "Status": "in_review"}],
		"Versions": {},
		"Threads": null,
		"Approvals": null
	}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := attrCtx("alice")
	plans, err := s.ListPlans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].SourceHint != "/tmp/x.md" {
		t.Fatalf("plans = %+v, want SourceHint migrated from legacy PathHint", plans)
	}

	// Any write re-saves the whole store, self-healing the on-disk schema.
	if err := s.RenamePlan(ctx, plans[0].ID, "Legacy Renamed"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"PathHint"`) {
		t.Fatalf("state.json still carries the legacy PathHint key after a write: %s", raw)
	}
	if !strings.Contains(string(raw), `"SourceHint": "/tmp/x.md"`) {
		t.Fatalf("state.json missing the migrated SourceHint value: %s", raw)
	}

	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	plans, err = reopened.ListPlans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].SourceHint != "/tmp/x.md" {
		t.Fatalf("plans after reopen = %+v, want SourceHint still present", plans)
	}
}

// TestDerivedFieldsNeverPersistToStateJSON is the direct proof for this
// package's named trap: domain.Plan carries no JSON tags of its own, so an
// exported field added to it without one would silently round-trip into
// state.json under its bare Go name, becoming a second, stale copy of a fact
// planWithDerivedFacts already recomputes correctly at read time.
//
// This asserts the RULE, not today's field list: a plan object's key set on disk
// must be EXACTLY {ID, RepoHint, SourceHint, Title} -- the four identity fields
// domain.Plan's own doc comment names as persisted -- and nothing else. An
// enumeration of today's derived field names cannot see the next untagged field
// added to the struct; an exact key-set comparison can.
func TestDerivedFieldsNeverPersistToStateJSON(t *testing.T) {
	s, path := newStore(t)
	ctx := attrCtx("alice")

	plan, err := s.CreatePlan(ctx, "Foo", "/plans/foo.md", "", []byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateThread(ctx, plan.ID, domain.HashContent([]byte("aaa")), anchor, "a thread"); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, plan.ID, domain.HashContent([]byte("aaa"))); err != nil {
		t.Fatal(err)
	}

	// Confirm the derived facts really are non-zero in memory first -- a
	// test asserting an empty key-set diff would pass just as well against
	// a Store that computed nothing at all.
	plans, err := s.ListPlans(ctx)
	if err != nil || len(plans) != 1 {
		t.Fatalf("list = %+v, %v", plans, err)
	}
	if plans[0].TotalThreads == 0 || plans[0].LastActivityAt.IsZero() {
		t.Fatalf("test setup: derived facts still zero-valued: %+v", plans[0])
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Plans []map[string]json.RawMessage
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("state.json did not decode as the expected {Plans: [...]} shape: %v\n%s", err, raw)
	}
	if len(doc.Plans) != 1 {
		t.Fatalf("state.json carries %d plan objects, want 1: %s", len(doc.Plans), raw)
	}

	var gotKeys []string
	for k := range doc.Plans[0] {
		gotKeys = append(gotKeys, k)
	}
	sort.Strings(gotKeys)
	wantKeys := []string{"ID", "RepoHint", "SourceHint", "Title"}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Errorf("state.json plan object's key set = %v, want exactly %v -- every OTHER exported domain.Plan field must carry json:\"-\": %s",
			gotKeys, wantKeys, raw)
	}
}

// TestPersistedAttributionKeySetIsExact is the twin of the test above, for the
// type the test above cannot see.
//
// domain.Attribution is a PERSISTED type -- this package writes it into
// state.json inside every comment and every approval -- and it carries no JSON
// tags at all, so its on-disk keys ARE its Go field names. Two silent failures
// follow:
//
//   - A field added to domain.Attribution widens state.json.
//   - Renaming an existing field changes the persisted key, orphaning the author
//     of every fact already on disk -- the identical trap PathHint→SourceHint
//     sprang on domain.Plan, which is why the test above exists and why a
//     legacyPlan shadow struct exists to undo it.
//
// So this asserts the RULE and not a spelling: a persisted attribution's key set
// is EXACTLY the four fields domain.Attribution declares. All four are meant to
// persist -- unlike domain.Plan, where all but four must NOT -- so the list here
// is the whole type, and a field added to it is a deliberate widening of the
// file format that should have to say so here.
//
// The comment and the approval are both read, because they are separate arrays
// reached by separate write paths, and a Store that persisted an attribution
// correctly in one and not the other is a state a single read cannot tell from a
// correct one.
func TestPersistedAttributionKeySetIsExact(t *testing.T) {
	s, path := newStore(t)
	ctx := attrCtx("alice")

	plan, err := s.CreatePlan(ctx, "Foo", "/plans/foo.md", "", []byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateThread(ctx, plan.ID, domain.HashContent([]byte("aaa")), anchor, "a thread"); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, plan.ID, domain.HashContent([]byte("aaa"))); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Threads []struct {
			Comments []struct {
				Attribution map[string]json.RawMessage
			}
		}
		Approvals []struct {
			Attribution map[string]json.RawMessage
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("state.json did not decode as the expected shape: %v\n%s", err, raw)
	}
	if len(doc.Threads) != 1 || len(doc.Threads[0].Comments) != 1 || len(doc.Approvals) != 1 {
		t.Fatalf("state.json carries %d threads and %d approvals, want one comment and one approval: %s",
			len(doc.Threads), len(doc.Approvals), raw)
	}

	// Both halves of the identity are asserted by VALUE as well as by key,
	// because a key-set diff over an attribution nothing ever filled in would
	// pass just as well against a Store that stamped nothing at all -- and
	// because "alice" and "alice-codes" are different strings, so a Store
	// that wrote the display name into both halves fails here too.
	wantKeys := []string{"ActorDisplay", "ActorID", "ActorLogin", "Agent"}
	for _, tc := range []struct {
		where string
		obj   map[string]json.RawMessage
	}{
		{"a comment", doc.Threads[0].Comments[0].Attribution},
		{"an approval", doc.Approvals[0].Attribution},
	} {
		var gotKeys []string
		for k := range tc.obj {
			gotKeys = append(gotKeys, k)
		}
		sort.Strings(gotKeys)
		if !reflect.DeepEqual(gotKeys, wantKeys) {
			t.Errorf("%s's persisted attribution key set = %v, want exactly %v -- domain.Attribution carries no "+
				"json tags, so a field added to it widens state.json, and a field RENAMED on it orphans "+
				"the author of every fact already on disk: %s", tc.where, gotKeys, wantKeys, raw)
		}
		// Checked AFTER the key set, not before, so a rename is reported as
		// the key-set diff that names the moved key rather than as this
		// weaker "the value is missing" line.
		if string(tc.obj["ActorDisplay"]) != `"alice"` || string(tc.obj["ActorLogin"]) != `"alice-codes"` {
			t.Errorf("test setup: %s's persisted attribution is not the one attrCtx wrote, so the key-set "+
				"comparison above would pass over an attribution nothing ever filled in: %s", tc.where, raw)
		}
	}
}

// TestReloadPreservesNilPlans pins the legacy shadow decode's nil handling for
// Plans: a state.json whose "Plans" key is JSON null must decode to a nil Plans
// slice — the same treatment Threads and Approvals already get — not a spurious
// non-nil empty slice manufactured by an unconditional make(). No current write
// path persists a null Plans key, but state.json is hand-editable, so reload
// must handle it as gracefully as it already handles a null Threads or
// Approvals. Checks the unexported field directly (same package) since
// ListPlans's own copy-out always returns a fresh non-nil slice regardless.
func TestReloadPreservesNilPlans(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	seed := `{"Plans": null, "Versions": {}, "Threads": null, "Approvals": null}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.st.Plans != nil {
		t.Fatalf("st.Plans = %#v, want nil", s.st.Plans)
	}
	plans, err := s.ListPlans(attrCtx("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 0 {
		t.Fatalf("plans = %+v, want none", plans)
	}
}

func TestTwoStoresInterleaveWithoutLostUpdates(t *testing.T) {
	s1, path := newStore(t) // actor "alice"
	s2, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := attrCtx("alice")
	plan, err := s1.CreatePlan(ctx, "P", "/plans/p.md", "", []byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}

	// Two Stores on one state file, interleaved writes, zero lost updates:
	// the clobber scenario this test is required to catch.
	var wg sync.WaitGroup
	post := func(s *Store, who string) {
		defer wg.Done()
		for i := 0; i < 25; i++ {
			if _, err := s.CreateThread(ctx, plan.ID, "aaa", anchor, fmt.Sprintf("%s-%d", who, i)); err != nil {
				t.Errorf("%s-%d: %v", who, i, err)
				return
			}
		}
	}
	wg.Add(2)
	go post(s1, "human")
	go post(s2, "agent")
	wg.Wait()

	threads, err := s1.Threads(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 50 {
		t.Fatalf("lost updates: %d threads, want 50", len(threads))
	}
	seen := map[string]bool{}
	for _, th := range threads {
		seen[th.Comments[0].Body] = true
	}
	for _, who := range []string{"human", "agent"} {
		for i := 0; i < 25; i++ {
			if !seen[fmt.Sprintf("%s-%d", who, i)] {
				t.Fatalf("missing %s-%d", who, i)
			}
		}
	}
}

func TestRenamePlanPersists(t *testing.T) {
	s, path := newStore(t)
	ctx := attrCtx("alice")
	plan, err := s.CreatePlan(ctx, "Foo", "/plans/foo.md", "", []byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}

	if err := s.RenamePlan(ctx, plan.ID, "Bar"); err != nil {
		t.Fatal(err)
	}
	plans, err := s.ListPlans(ctx)
	if err != nil || len(plans) != 1 || plans[0].Title != "Bar" {
		t.Fatalf("plans = %+v, %v", plans, err)
	}

	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := reopened.ResolvePlan(ctx, "/plans/foo.md")
	if err != nil || resolved.Title != "Bar" {
		t.Fatalf("resolve after reopen = %+v, %v", resolved, err)
	}
}

func TestRenamePlanValidation(t *testing.T) {
	s, _ := newStore(t)
	ctx := attrCtx("alice")
	plan, err := s.CreatePlan(ctx, "Foo", "/plans/foo.md", "", []byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}

	if err := s.RenamePlan(ctx, "plan-nope", "Bar"); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	for _, title := range []string{"", "   "} {
		err := s.RenamePlan(ctx, plan.ID, title)
		if err == nil {
			t.Fatalf("title %q: want error, got nil", title)
		}
		if !strings.Contains(err.Error(), "title") {
			t.Fatalf("title %q: err = %v, want message mentioning title", title, err)
		}
	}
}

// TestSetSourceHintMovesOnlyThePath is TWO claims rather than one. The path
// moves and survives a reopen -- RenamePlanPersists' own shape one field over --
// and NOTHING ELSE MOVES, which is the claim this guarantee actually rests
// on: the write this panel needed is narrow, and a test that asserted only
// the new hint would pass against an implementation that quietly emptied
// the review.
//
// THE EMPTY HINT IS A ROW AND NOT A SEPARATE TEST, because it is not a separate
// behaviour: one mutation takes the new hint and the empty string means
// RELEASE.
func TestSetSourceHintMovesOnlyThePath(t *testing.T) {
	for _, tc := range []struct {
		name string
		hint string
	}{
		{"points at another file", "/plans/moved.md"},
		{"releases the path entirely", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, path := newStore(t)
			ctx := attrCtx("alice")
			plan, err := s.CreatePlan(ctx, "Foo", "/plans/foo.md", "repo", []byte("v1"))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Approve(ctx, plan.ID, domain.HashContent([]byte("v1"))); err != nil {
				t.Fatal(err)
			}
			th, err := s.CreateThread(ctx, plan.ID, domain.HashContent([]byte("v1")), anchor, "keep me")
			if err != nil {
				t.Fatal(err)
			}

			if err := s.SetSourceHint(ctx, plan.ID, tc.hint); err != nil {
				t.Fatal(err)
			}

			// Reopened rather than read back through s: the claim is about
			// state.json, and an in-memory field that was never saved reads
			// back correctly from the store that set it.
			reopened, err := New(path)
			if err != nil {
				t.Fatal(err)
			}
			after, err := reopened.PlanByID(ctx, plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.SourceHint != tc.hint {
				t.Fatalf("SourceHint = %q, want %q", after.SourceHint, tc.hint)
			}
			if after.ID != plan.ID || after.Title != "Foo" || after.RepoHint != "repo" {
				t.Fatalf("plan = %+v, want its id, title and repo hint untouched", after)
			}
			threads, err := reopened.Threads(ctx, plan.ID)
			if err != nil || len(threads) != 1 || threads[0].ID != th.ID {
				t.Fatalf("threads = %+v, %v -- want the one thread this plan already had", threads, err)
			}
			approvals, err := reopened.Approvals(ctx, plan.ID)
			if err != nil || len(approvals) != 1 {
				t.Fatalf("approvals = %+v, %v -- want the approval untouched", approvals, err)
			}
			versions, err := reopened.Versions(ctx, plan.ID)
			if err != nil || len(versions) != 1 {
				t.Fatalf("versions = %+v, %v -- want the version log untouched", versions, err)
			}

			// The OLD path stops addressing this plan and the new one starts,
			// which is what makes the write meaningful rather than merely
			// recorded. A released plan is addressable by neither: an empty
			// hint never matches (ResolvePlan's own rule).
			if _, err := reopened.ResolvePlan(ctx, "/plans/foo.md"); !errors.Is(err, client.ErrNoPlan) {
				t.Fatalf("resolve on the old path = %v, want ErrNoPlan", err)
			}
			resolved, err := reopened.ResolvePlan(ctx, tc.hint)
			if tc.hint == "" {
				if !errors.Is(err, client.ErrNoPlan) {
					t.Fatalf("resolve on the empty hint = %+v, %v, want ErrNoPlan", resolved, err)
				}
				return
			}
			if err != nil || resolved.ID != plan.ID {
				t.Fatalf("resolve on the new path = %+v, %v, want plan %s", resolved, err, plan.ID)
			}
		})
	}
}

func TestSetSourceHintUnknownID(t *testing.T) {
	s, _ := newStore(t)
	if err := s.SetSourceHint(attrCtx("alice"), "l_nope", "/plans/x.md"); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDeletePlanRemovesAllFacts(t *testing.T) {
	s, _ := newStore(t)
	ctx := attrCtx("alice")

	planA, err := s.CreatePlan(ctx, "A", "/plans/a.md", "", []byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}
	planB, err := s.CreatePlan(ctx, "B", "/plans/b.md", "", []byte("bbb"))
	if err != nil {
		t.Fatal(err)
	}

	threadA1, err := s.CreateThread(ctx, planA.ID, "aaa", anchor, "a note")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateThread(ctx, planA.ID, "aaa", anchor, "a second note"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetResolved(ctx, planA.ID, threadA1.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, planA.ID, "aaa"); err != nil {
		t.Fatal(err)
	}

	threadB1, err := s.CreateThread(ctx, planB.ID, "bbb", anchor, "b note")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateThread(ctx, planB.ID, "bbb", anchor, "b second note"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetResolved(ctx, planB.ID, threadB1.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, planB.ID, "bbb"); err != nil {
		t.Fatal(err)
	}

	if err := s.DeletePlan(ctx, planA.ID); err != nil {
		t.Fatal(err)
	}

	plans, err := s.ListPlans(ctx)
	if err != nil || len(plans) != 1 || plans[0].ID != planB.ID {
		t.Fatalf("plans after delete = %+v, %v", plans, err)
	}

	// Unknown-ID-returns-empty read semantics apply to the deleted plan.
	versionsA, err := s.Versions(ctx, planA.ID)
	if err != nil || len(versionsA) != 0 {
		t.Fatalf("planA versions after delete = %+v, %v", versionsA, err)
	}
	threadsA, err := s.Threads(ctx, planA.ID)
	if err != nil || len(threadsA) != 0 {
		t.Fatalf("planA threads after delete = %+v, %v", threadsA, err)
	}
	approvalsA, err := s.Approvals(ctx, planA.ID)
	if err != nil || len(approvalsA) != 0 {
		t.Fatalf("planA approvals after delete = %+v, %v", approvalsA, err)
	}

	// Plan B's facts are fully intact.
	versionsB, err := s.Versions(ctx, planB.ID)
	if err != nil || len(versionsB) != 1 {
		t.Fatalf("planB versions after delete = %+v, %v", versionsB, err)
	}
	threadsB, err := s.Threads(ctx, planB.ID)
	if err != nil || len(threadsB) != 2 {
		t.Fatalf("planB threads after delete = %+v, %v", threadsB, err)
	}
	resolvedCount := 0
	for _, th := range threadsB {
		if th.Resolved {
			resolvedCount++
		}
	}
	if resolvedCount != 1 {
		t.Fatalf("planB resolved thread count = %d, want 1", resolvedCount)
	}
	approvalsB, err := s.Approvals(ctx, planB.ID)
	if err != nil || len(approvalsB) != 1 {
		t.Fatalf("planB approvals after delete = %+v, %v", approvalsB, err)
	}
	_ = threadB1
}

func TestDeletePlanUnknown(t *testing.T) {
	s, _ := newStore(t)
	ctx := attrCtx("alice")
	if err := s.DeletePlan(ctx, "plan-nope"); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestStoreSeesOtherStoresWrites(t *testing.T) {
	// s2 opens BEFORE the plan exists; every operation must see current
	// on-disk truth, not construction-time state.
	s1, path := newStore(t)
	s2, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := attrCtx("alice")
	plan, err := s1.CreatePlan(ctx, "P", "/plans/p.md", "", []byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.ResolvePlan(ctx, "/plans/p.md"); err != nil {
		t.Fatalf("s2 must see s1's plan: %v", err)
	}
	th, err := s1.CreateThread(ctx, plan.ID, "aaa", anchor, "from s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.SetResolved(ctx, plan.ID, th.ID, true); err != nil {
		t.Fatalf("s2 must see s1's thread: %v", err)
	}
	threads, err := s1.Threads(ctx, plan.ID)
	if err != nil || len(threads) != 1 || !threads[0].Resolved {
		t.Fatalf("s1 must see s2's resolve: %+v, %v", threads, err)
	}
}

// TestEveryMintedIDCarriesTheIDPrefix enumerates the three ID-minting
// paths rather than checking the one that happens to be convenient. The l_
// prefix is the shape every id already in a state.json has (see newID), so a
// path that forgot it would put a second shape of id into one store.
func TestEveryMintedIDCarriesTheIDPrefix(t *testing.T) {
	ctx := attrCtx("alice")
	s, _ := newStore(t)

	plan, err := s.CreatePlan(ctx, "Plan", "/plan.md", "", []byte("h1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(plan.ID), "l_") {
		t.Errorf("plan id %q lacks the l_ prefix", plan.ID)
	}

	th, err := s.CreateThread(ctx, plan.ID, "h1", reanchor.Anchor{}, "first")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(th.ID), "l_") {
		t.Errorf("thread id %q lacks the l_ prefix", th.ID)
	}
	if !strings.HasPrefix(string(th.Comments[0].ID), "l_") {
		t.Errorf("comment id %q lacks the l_ prefix", th.Comments[0].ID)
	}

	c, err := s.Reply(ctx, plan.ID, th.ID, "second")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(c.ID), "l_") {
		t.Errorf("reply comment id %q lacks the l_ prefix", c.ID)
	}
}

// This store holds every plan's title and source, every thread and the comment
// bodies in it, every approval. It once wrote world-readable, 0644 in a 0755
// directory, where client/config is 0600 in a 0700 directory.
//
// THE LOCKFILE IS ASSERTED WITH THE STATE FILE, not separately, because it is
// created by a different call on a different path (withLock's OpenFile, not
// save's WriteFile) and the two have already disagreed once.
//
// NOTHING HERE REPAIRS AN EXISTING FILE. A state.json already on disk keeps
// whatever mode it was created with: all local data is throwaway, so new files
// get the right mode and no migration runs.
func TestStateFileAndLockAreOwnerOnly(t *testing.T) {
	// A directory this Store creates, not t.TempDir() itself: MkdirAll leaves
	// an existing directory's mode alone, so asserting on t.TempDir() would
	// only ever measure os.MkdirTemp. <data-home>/draftplane/state.json is the
	// production shape (see cmd/draftplane's dataPath), and the "draftplane"
	// component is the one this code is responsible for.
	path := filepath.Join(t.TempDir(), "draftplane", "state.json")
	s, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.CreatePlan(attrCtx("ada"), "a plan", "/plans/a.md", "", []byte("# a plan\n")); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	for _, tc := range []struct {
		name string
		path string
		want os.FileMode
	}{
		{"state.json", path, 0o600},
		{"the lockfile", path + ".lock", 0o600},
		{"the directory holding both", filepath.Dir(path), 0o700},
		{"the directory EnsureDir makes for the CLI", ensuredDir(t), 0o700},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := os.Stat(tc.path)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if got := info.Mode().Perm(); got != tc.want {
				t.Errorf("mode = %04o, want %04o", got, tc.want)
			}
		})
	}
}

// ensuredDir drives the exported door cmd/draftplane's buildDeps calls before
// this Store exists, and returns the directory it made. That call site is the
// one that decided the mode on a fresh install, so it is asserted beside the
// Store's own writes rather than trusted to match them.
func ensuredDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "draftplane")
	if err := EnsureDir(filepath.Join(dir, "state.json")); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	return dir
}

// A stale temp file is how the mode above gets undone without anybody editing
// it. save writes s.path+".tmp" under a STABLE name and renames it over
// state.json, so the renamed file carries the TEMP's mode -- and os.WriteFile
// leaves an existing file's mode alone. A 0644 leftover from a crashed save
// therefore reinstates 0644 on state.json however save spells its own mode
// argument. Driven with the leftover placed by hand, because a crash cannot be
// staged deterministically.
func TestAStaleTempDoesNotHandItsModeToTheStateFile(t *testing.T) {
	s, path := newStore(t)
	if err := os.WriteFile(path+".tmp", []byte("{}"), 0o644); err != nil {
		t.Fatalf("planting the stale temp: %v", err)
	}
	if _, err := s.CreatePlan(attrCtx("ada"), "a plan", "/plans/a.md", "", []byte("# a plan\n")); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %04o, want 0600 -- a stale temp handed its own mode to state.json", got)
	}
}

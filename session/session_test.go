package session

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/client/localfs"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/placement"
	"github.com/draftplane/draftplane/reanchor"
	"github.com/draftplane/draftplane/store/localcas"
)

const docV1 = `# Rate Limiter Plan

## Context

Requests are currently unbounded and the database suffers under load spikes.

## Design

We will use a token bucket with a burst capacity of fifty requests.

## Rollout

Ship behind a feature flag to internal users first.
`

// V2: Design reworded (fuzzy), Rollout deleted (orphan), Context unchanged.
const docV2 = `# Rate Limiter Plan

## Context

Requests are currently unbounded and the database suffers under load spikes.

## Design

A token bucket limiter with burst capacity fifty is the chosen approach.
`

type fixture struct {
	svc  *localfs.Store
	cas  *localcas.Store
	path string
	ctx  context.Context
}

func setup(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	svc, err := localfs.New(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(path, []byte(docV1), 0o644); err != nil {
		t.Fatal(err)
	}
	return fixture{svc: svc, cas: localcas.New(filepath.Join(dir, "objects")), path: path, ctx: attrCtx("alice")}
}

// attrCtx stands in for the actor string localfs.New used to take: every
// package's Session methods thread the ctx a caller passes straight through to
// svc (session.go never substitutes its own), so decorating the fixture's one
// context here is enough for every write this file makes.
//
// IT TAKES ONE STRING AND WRITES TWO. A fixture holding the same string in both
// halves of domain.Attribution cannot say which half a renderer read, so it
// proves nothing; attrLogin derives the ActorLogin half rather than accepting
// it.
func attrCtx(actorDisplay string) context.Context {
	return client.WithAttribution(context.Background(), domain.Attribution{
		ActorLogin:   attrLogin(actorDisplay),
		ActorDisplay: actorDisplay,
	})
}

// attrLogin is the ActorLogin every fixture in this file pairs with a display
// name, and it guarantees the two are never the same string.
//
// Its output holds only [a-z0-9-], so it cannot spell ui.FormatAttribution's
// agent composite or a "user-<pseudonym>" fallback.
//
// The result DIFFERS from its input by construction rather than by inspection of
// today's callers: the mapping is length-preserving in runes, so a mapped string
// plus a non-empty suffix can never equal its own input.
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

func mustOpen(t *testing.T, f fixture) *Session {
	t.Helper()
	s, err := Open(f.ctx, f.svc, f.path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func anchorFor(t *testing.T, content, span string) reanchor.Anchor {
	t.Helper()
	a, err := reanchor.CreateAnchor(content, span, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestOpenReadsNeverPersist(t *testing.T) {
	f := setup(t)
	s := mustOpen(t, f)

	if s.Exists {
		t.Fatal("no plan should exist yet")
	}
	if s.Hash != domain.HashContent([]byte(docV1)) {
		t.Fatal("hash mismatch")
	}
	if ok, _ := f.cas.Has(f.ctx, s.Hash); ok {
		t.Fatal("open must not write to the content store")
	}
	if plans, _ := f.svc.ListPlans(f.ctx); len(plans) != 0 {
		t.Fatal("open must not create plans")
	}
}

func TestWriteActionsRequireCreate(t *testing.T) {
	f := setup(t)
	s := mustOpen(t, f)
	_, err := s.Comment(f.ctx, anchorFor(t, docV1, "token bucket with a burst capacity"), "why fifty?")
	if !errors.Is(err, ErrPlanNotCreated) {
		t.Fatalf("err = %v, want ErrPlanNotCreated", err)
	}
	if err := s.Approve(f.ctx); !errors.Is(err, ErrPlanNotCreated) {
		t.Fatalf("err = %v, want ErrPlanNotCreated", err)
	}
}

func TestCreateSnapshotsAndRegisters(t *testing.T) {
	f := setup(t)
	s := mustOpen(t, f)
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	if !s.Exists {
		t.Fatal("Exists should be true after Create")
	}
	if ok, _ := f.cas.Has(f.ctx, s.Hash); !ok {
		t.Fatal("Create must Put the snapshot (Put-before-reference)")
	}
	versions, err := f.svc.Versions(f.ctx, s.Plan.ID)
	if err != nil || len(versions) != 1 || versions[0].Hash != s.Hash {
		t.Fatalf("versions = %+v, %v", versions, err)
	}
}

func TestCommentAnchorsToCurrentVersion(t *testing.T) {
	f := setup(t)
	s := mustOpen(t, f)
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	thread, err := s.Comment(f.ctx, anchorFor(t, docV1, "token bucket with a burst capacity"), "why fifty?")
	if err != nil {
		t.Fatal(err)
	}
	if thread.AnchorHash != s.Hash {
		t.Fatalf("thread anchored to %s, want %s", thread.AnchorHash, s.Hash)
	}
}

func TestRevisionFlow(t *testing.T) {
	f := setup(t)
	s := mustOpen(t, f)
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	designAnchor := anchorFor(t, docV1, "token bucket with a burst capacity of fifty requests")
	rolloutAnchor := anchorFor(t, docV1, "Ship behind a feature flag to internal users first.")
	if _, err := s.Comment(f.ctx, designAnchor, "why fifty?"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Comment(f.ctx, rolloutAnchor, "which flag system?"); err != nil {
		t.Fatal(err)
	}
	v1Hash := s.Hash

	// The agent revises the file in place.
	if err := os.WriteFile(f.path, []byte(docV2), 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := mustOpen(t, f)
	if !s2.Exists || s2.Hash == v1Hash {
		t.Fatalf("reopened session: exists=%v hash=%s", s2.Exists, s2.Hash)
	}

	placements, err := s2.Placements(f.ctx)
	if err != nil || len(placements) != 2 {
		t.Fatalf("placements = %+v, %v", placements, err)
	}
	byBody := map[string]placement.Placement{}
	for _, p := range placements {
		byBody[p.Thread.Comments[0].Body] = p
	}
	design := byBody["why fifty?"]
	if design.Status != reanchor.StatusFuzzy && design.Status != reanchor.StatusExact && design.Status != reanchor.StatusMoved {
		t.Fatalf("design thread should attach, got %s", design.Status)
	}
	rollout := byBody["which flag system?"]
	if rollout.Status != reanchor.StatusOrphaned {
		t.Fatalf("rollout thread should orphan, got %s", rollout.Status)
	}

	// New version registers lazily on the next write action, superseding v1.
	if _, err := s2.Comment(f.ctx, anchorFor(t, docV2, "burst capacity fifty is the chosen approach"), "capacity confirmed"); err != nil {
		t.Fatal(err)
	}
	versions, _ := f.svc.Versions(f.ctx, s2.Plan.ID)
	if len(versions) != 2 {
		t.Fatalf("want 2 versions, got %d", len(versions))
	}
	// Ancestry is the log's order, not a field: v1 first, the new version
	// appended after it.
	if versions[0].Hash != v1Hash || versions[1].Hash != s2.Hash {
		t.Fatalf("versions = %+v, want %s then %s", versions, v1Hash, s2.Hash)
	}
	if ok, _ := f.cas.Has(f.ctx, s2.Hash); !ok {
		t.Fatal("v2 snapshot must be in the store after the write")
	}
}

func TestRehomeDurability(t *testing.T) {
	f := setup(t)
	s := mustOpen(t, f)
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	thread, err := s.Comment(f.ctx, anchorFor(t, docV1, "Ship behind a feature flag to internal users first."), "which flag system?")
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(f.path, []byte(docV2), 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := mustOpen(t, f)
	newAnchor := anchorFor(t, docV2, "A token bucket limiter with burst capacity fifty")
	if err := s2.RehomeToAnchor(f.ctx, thread.ID, newAnchor); err != nil {
		t.Fatal(err)
	}

	placements, err := s2.Placements(f.ctx)
	if err != nil || len(placements) != 1 {
		t.Fatalf("placements = %+v, %v", placements, err)
	}
	if placements[0].Status == reanchor.StatusOrphaned {
		t.Fatal("rehomed thread must not be orphaned")
	}
	if placements[0].Thread.AnchorHash != s2.Hash {
		t.Fatal("rehome must anchor to the current version")
	}
}

func TestApproveCurrentVersion(t *testing.T) {
	f := setup(t)
	s := mustOpen(t, f)
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	if approved, _ := s.ApprovedCurrent(f.ctx); approved {
		t.Fatal("not approved yet")
	}
	if err := s.Approve(f.ctx); err != nil {
		t.Fatal(err)
	}
	approved, err := s.ApprovedCurrent(f.ctx)
	if err != nil || !approved {
		t.Fatalf("approved = %v, %v", approved, err)
	}

	// Approval binds to the exact content: a revision is not approved.
	if err := os.WriteFile(f.path, []byte(docV2), 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := mustOpen(t, f)
	if approved, _ := s2.ApprovedCurrent(f.ctx); approved {
		t.Fatal("revised content must not inherit approval")
	}
}

// TestCreatePlanStoresContentByConstruction pins that content is already in the
// CAS the moment CreatePlan returns, with no session, no Approve and no
// self-heal involved: CreatePlan takes content directly and Puts it before it
// ever creates the version row (see localfs.Store.CreatePlan), so a version with
// no bytes behind it is not constructible at all.
func TestCreatePlanStoresContentByConstruction(t *testing.T) {
	f := setup(t)

	// Register a version directly via the service, bypassing session (and
	// its own cas.Put call) entirely.
	if _, err := f.svc.CreatePlan(f.ctx, "Rate Limiter", f.path, "", []byte(docV1)); err != nil {
		t.Fatal(err)
	}

	hash := domain.HashContent([]byte(docV1))
	if ok, err := f.cas.Has(f.ctx, hash); err != nil || !ok {
		t.Fatalf("cas.Has right after CreatePlan = %v, %v; want true, nil", ok, err)
	}
}

func TestOpenMissingFile(t *testing.T) {
	f := setup(t)
	if _, err := Open(f.ctx, f.svc, filepath.Join(t.TempDir(), "nope.md")); err == nil {
		t.Fatal("want error for missing file")
	}
}

// TestOpenRefusesAnEmptyPathAsItsOwnFact pins the half of the sourceless open
// seam that lives here (the other half is app's TestRootOpensASourcelessPlan): a
// sourceless plan's SourceHint is "", and Open must answer that with
// ErrNoSourcePath rather than the ENOENT os.ReadFile("") says. ENOENT is a
// DIFFERENT fact -- a file that should be there and is not -- and a caller that
// can open the plan another way (by id) is entitled to recognise the sourceless
// case without also swallowing every vanished file.
func TestOpenRefusesAnEmptyPathAsItsOwnFact(t *testing.T) {
	f := setup(t)
	sess, err := Open(f.ctx, f.svc, "")
	if sess != nil {
		t.Fatal("an empty path must yield no session at all")
	}
	if !errors.Is(err, ErrNoSourcePath) {
		t.Fatalf("Open(\"\") = %v, want ErrNoSourcePath -- a sourceless plan has no file to open", err)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open(\"\") = %v, must not also read as a missing file: a plan that never had a path is not a file that vanished", err)
	}
}

var _ client.PlanService = (*localfs.Store)(nil)

// secondFixtureStore opens a second localfs.Store on the same state path as
// f, standing in for another actor (an MCP agent) operating on the same
// review concurrently with the session under test.
func secondFixtureStore(t *testing.T, f fixture) fixture {
	t.Helper()
	svc, err := localfs.New(filepath.Join(filepath.Dir(f.path), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return fixture{svc: svc, cas: f.cas, path: f.path, ctx: attrCtx("agent")}
}

// lookupFailingSvc is the real store with ResolvePlan failing: Open's identity
// half fails while its file half and every by-id read (PlanByID, Versions,
// Content) still work, so a fallback that fired here would succeed.
type lookupFailingSvc struct {
	client.PlanService
	err error
}

func (s lookupFailingSvc) ResolvePlan(context.Context, string) (domain.Plan, error) {
	return domain.Plan{}, s.err
}

// corruptState overwrites f's state file with bytes that will not parse, so
// Open's file half still succeeds while its identity half fails.
func corruptState(t *testing.T, f fixture) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(filepath.Dir(f.path), "state.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRediscoverPicksUpLazilyCreatedPlan pins the live-refresh bug's session-
// level root cause: Exists is a snapshot taken at Open, so a session opened
// before any plan exists for its path can never see one appear on its own.
// Rediscover re-resolves plan identity so a plan (and its threads) created by
// another actor after Open becomes visible without reopening the session.
func TestRediscoverPicksUpLazilyCreatedPlan(t *testing.T) {
	f := setup(t)
	s := mustOpen(t, f)
	if s.Exists {
		t.Fatal("no plan should exist yet")
	}
	if placements, err := s.Placements(f.ctx); err != nil || len(placements) != 0 {
		t.Fatalf("placements before any plan exists = %+v, %v", placements, err)
	}

	// Another actor creates the plan and posts a thread through a second
	// store on the same state path. It creates against docV2 — NOT the
	// content this session holds — so the plan's tip is a hash s has never
	// seen. Creating against docV1 would leave the tip equal to s.Hash, and
	// the write below would hit RegisterVersion's no-op arm and succeed
	// whatever base Rediscover did or did not establish.
	f2 := secondFixtureStore(t, f)
	s2, err := OpenSupplied(f2.ctx, f2.svc, []byte(docV2), f2.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Create(f2.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Comment(f2.ctx, anchorFor(t, docV2, "burst capacity fifty is the chosen approach"), "why fifty?"); err != nil {
		t.Fatal(err)
	}

	if err := s.Rediscover(f.ctx); err != nil {
		t.Fatal(err)
	}
	if !s.Exists {
		t.Fatal("Rediscover should pick up the lazily created plan")
	}
	placements, err := s.Placements(f.ctx)
	if err != nil || len(placements) != 1 {
		t.Fatalf("placements after Rediscover = %+v, %v", placements, err)
	}

	// The write Rediscover made possible. Its precondition can only be the
	// base Rediscover established from the plan it just adopted — this
	// session never held one before, and ensureRegistered never re-reads.
	if err := s.Register(f.ctx); err != nil {
		t.Fatalf("a write after Rediscover must use the base Rediscover established: %v", err)
	}
}

// TestRediscoverStillNoPlanIsNotError pins the "no plan yet" outcome: exactly
// like Open, a still-missing plan is not an error, just Exists staying false.
func TestRediscoverStillNoPlanIsNotError(t *testing.T) {
	f := setup(t)
	s := mustOpen(t, f)
	if err := s.Rediscover(f.ctx); err != nil {
		t.Fatal(err)
	}
	if s.Exists {
		t.Fatal("Exists should still be false: no plan was created")
	}
}

// TestRediscoverNoOpWhenAlreadyExists pins the no-op guard: once a session
// already knows its plan, Rediscover must not re-resolve or touch it.
func TestRediscoverNoOpWhenAlreadyExists(t *testing.T) {
	f := setup(t)
	s := mustOpen(t, f)
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	wantPlan := s.Plan
	if err := s.Rediscover(f.ctx); err != nil {
		t.Fatal(err)
	}
	if s.Plan != wantPlan {
		t.Fatalf("Plan = %+v, want unchanged %+v (Rediscover must no-op once Exists)", s.Plan, wantPlan)
	}
}

func TestPlacementCarriesMatchedText(t *testing.T) {
	f := setup(t)
	s := mustOpen(t, f)
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Comment(f.ctx, anchorFor(t, docV1, "token bucket with a burst capacity of fifty requests"), "why fifty?"); err != nil {
		t.Fatal(err)
	}
	placements, err := s.Placements(f.ctx)
	if err != nil || len(placements) != 1 {
		t.Fatalf("placements = %+v, %v", placements, err)
	}
	if placements[0].MatchedText == "" {
		t.Fatal("attached placement must carry MatchedText")
	}
}

func TestOpenVersionReviewsSnapshot(t *testing.T) {
	f := setup(t)
	s := mustOpen(t, f)
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	thread, err := s.Comment(f.ctx, anchorFor(t, docV1, "token bucket with a burst capacity"), "why fifty?")
	if err != nil {
		t.Fatal(err)
	}
	planID := s.Plan.ID

	// The file goes away: the reviewed bytes now live only in the CAS.
	if err := os.Remove(f.path); err != nil {
		t.Fatal(err)
	}

	sv, err := OpenVersion(f.ctx, f.svc, planID)
	if err != nil {
		t.Fatal(err)
	}
	if !sv.Exists || sv.Hash != s.Hash || string(sv.Content) != docV1 {
		t.Fatalf("snapshot session = exists=%v hash=%s content=%q", sv.Exists, sv.Hash, sv.Content)
	}

	placements, err := sv.Placements(f.ctx)
	if err != nil || len(placements) != 1 {
		t.Fatalf("placements = %+v, %v", placements, err)
	}
	if placements[0].Thread.ID != thread.ID {
		t.Fatalf("placement thread = %+v, want %s", placements[0].Thread, thread.ID)
	}
	if placements[0].Status == reanchor.StatusOrphaned {
		t.Fatal("thread should attach against the CAS content")
	}

	versionsBefore, err := f.svc.Versions(f.ctx, planID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sv.Comment(f.ctx, anchorFor(t, docV1, "Ship behind a feature flag to internal users first."), "which flag?"); err != nil {
		t.Fatal(err)
	}
	versionsAfter, err := f.svc.Versions(f.ctx, planID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versionsAfter) != len(versionsBefore) {
		t.Fatalf("Comment on an already-registered snapshot must not register a new version: before=%d after=%d", len(versionsBefore), len(versionsAfter))
	}
}

func TestOpenVersionNoPlan(t *testing.T) {
	f := setup(t)
	if _, err := OpenVersion(f.ctx, f.svc, "plan-nope"); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestOpenVersionNoVersions covers a plan that exists but has never
// registered a version — not reachable through localfs.CreatePlan (which
// always registers its initial version), but the read-path defense against
// it is exercised by seeding state.json directly with the on-disk schema.
func TestOpenVersionNoVersions(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	seed := `{
		"Plans": [{"ID": "plan-empty", "Title": "Empty", "SourceHint": "/plans/empty.md", "RepoHint": "", "Status": "in_review"}],
		"Versions": {},
		"Threads": null,
		"Approvals": null
	}`
	if err := os.WriteFile(statePath, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, err := localfs.New(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenVersion(context.Background(), svc, "plan-empty"); err == nil || !strings.Contains(err.Error(), "no registered versions") {
		t.Fatalf("err = %v, want error mentioning 'no registered versions'", err)
	}
}

// TestOpenByIDOpensTheRegisteredVersion proves OpenByID opens a plan by id as a
// snapshot session over its registered content.
func TestOpenByIDOpensTheRegisteredVersion(t *testing.T) {
	f := setup(t)
	s := mustOpen(t, f)
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}

	got, err := OpenByID(f.ctx, f.svc, s.Plan.ID)
	if err != nil {
		t.Fatalf("OpenByID: %v", err)
	}
	if !got.FromSnapshot() {
		t.Error("FromSnapshot() = false, want true")
	}
	if string(got.Content) != docV1 {
		t.Errorf("Content = %q, want %q", got.Content, docV1)
	}
}

// TestOpenSuppliedResolvesBySource pins OpenSupplied's identity resolution
// against a source hint: same shape as Open, but keyed by an arbitrary
// source string (e.g. a Notion URL) instead of a filesystem path, and driven
// off supplied bytes instead of a file read.
func TestOpenSuppliedResolvesBySource(t *testing.T) {
	f := setup(t)
	source := "notion://x"

	// Seed the plan under the source hint from supplied bytes (no file
	// involved at all).
	seed, err := OpenSupplied(f.ctx, f.svc, []byte(docV1), source)
	if err != nil {
		t.Fatal(err)
	}
	if seed.Exists {
		t.Fatal("no plan should exist yet for this source")
	}
	if err := seed.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name           string
		content        string
		wantRegistered bool
	}{
		{"known hash", docV1, true},
		{"unknown hash", docV2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := OpenSupplied(f.ctx, f.svc, []byte(tc.content), source)
			if err != nil {
				t.Fatal(err)
			}
			if !s.Exists || s.Plan.ID != seed.Plan.ID {
				t.Fatalf("exists=%v plan=%s, want exists=true plan=%s", s.Exists, s.Plan.ID, seed.Plan.ID)
			}
			if s.registered != tc.wantRegistered {
				t.Fatalf("registered = %v, want %v", s.registered, tc.wantRegistered)
			}
			if s.Path != source {
				t.Fatalf("Path = %q, want %q", s.Path, source)
			}
			if !s.fromSnapshot {
				t.Fatal("OpenSupplied sessions must set fromSnapshot=true")
			}
		})
	}
}

// TestOpenSuppliedCopiesContent pins the defensive copy: the session's
// Content/Hash pair must stay intact if the caller reuses or mutates the
// supplied byte slice after the call.
func TestOpenSuppliedCopiesContent(t *testing.T) {
	f := setup(t)
	supplied := []byte(docV1)
	s, err := OpenSupplied(f.ctx, f.svc, supplied, "")
	if err != nil {
		t.Fatal(err)
	}
	supplied[0] = 'X'
	if string(s.Content) != docV1 {
		t.Fatal("mutating the supplied slice must not change session content")
	}
}

// TestOpenSuppliedSourceless pins the no-source-at-all path: a plan-less
// session the caller can Create, exactly like Open with no plan on disk.
func TestOpenSuppliedSourceless(t *testing.T) {
	f := setup(t)
	s, err := OpenSupplied(f.ctx, f.svc, []byte(docV1), "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Exists {
		t.Fatal("sourceless OpenSupplied must start plan-less")
	}
	if s.Hash != domain.HashContent([]byte(docV1)) {
		t.Fatal("hash mismatch")
	}
	if err := s.Create(f.ctx, "Ad Hoc Plan"); err != nil {
		t.Fatal(err)
	}
	if !s.Exists {
		t.Fatal("Create should set Exists")
	}
}

// TestOpenSuppliedForPlanRegisteredFlag pins the explicit-plan seam's registered
// computation: true when supplied content matches the plan's current latest
// version, false otherwise (new content, or a revert to older content) --
// exactly resolveIdentity's rule, but reachable for a sourceless plan too, which
// OpenSupplied(content, "") can never find. A caller's ensureRegistered must see
// a correct signal from the session itself, not merely happen to behave
// correctly because RegisterVersion separately dedupes.
func TestOpenSuppliedForPlanRegisteredFlag(t *testing.T) {
	f := setup(t)
	seed, err := OpenSupplied(f.ctx, f.svc, []byte(docV1), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Create(f.ctx, "Ad Hoc Plan"); err != nil {
		t.Fatal(err)
	}
	plan := seed.Plan

	same, err := OpenSuppliedForPlan(f.ctx, f.svc, []byte(docV1), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !same.registered {
		t.Fatal("registered should be true: content matches the plan's latest version")
	}
	if !same.Exists || same.Plan.ID != plan.ID {
		t.Fatalf("exists=%v plan=%s, want exists=true plan=%s", same.Exists, same.Plan.ID, plan.ID)
	}
	if !same.fromSnapshot {
		t.Fatal("OpenSuppliedForPlan sessions must set fromSnapshot=true")
	}

	changed, err := OpenSuppliedForPlan(f.ctx, f.svc, []byte(docV2), plan)
	if err != nil {
		t.Fatal(err)
	}
	if changed.registered {
		t.Fatal("registered should be false: content is not the plan's latest version")
	}
	if err := changed.Register(f.ctx); err != nil {
		t.Fatal(err)
	}
	versions, err := f.svc.Versions(f.ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[1].Hash != changed.Hash {
		t.Fatalf("versions = %+v, want 2 with v2 latest", versions)
	}
}

// TestRegisterSupersedes pins Register as the save path's explicit write:
// new supplied bytes registered over an existing plan grow Versions by one,
// superseding the previous latest, and Put the bytes into the CAS.
func TestRegisterSupersedes(t *testing.T) {
	f := setup(t)
	s := mustOpen(t, f)
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	v1Hash := s.Hash

	s2, err := OpenSupplied(f.ctx, f.svc, []byte(docV2), f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Register(f.ctx); err != nil {
		t.Fatal(err)
	}

	versions, err := f.svc.Versions(f.ctx, s2.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 {
		t.Fatalf("versions = %+v, want 2", versions)
	}
	if versions[0].Hash != v1Hash || versions[1].Hash != s2.Hash {
		t.Fatalf("versions = %+v, want %s then %s", versions, v1Hash, s2.Hash)
	}
	if ok, _ := f.cas.Has(f.ctx, s2.Hash); !ok {
		t.Fatal("Register must Put the supplied content into the CAS")
	}
}

// TestRegisterIdempotent pins Register's idempotence: registering the same
// content twice must not grow Versions a second time.
func TestRegisterIdempotent(t *testing.T) {
	f := setup(t)
	s := mustOpen(t, f)
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenSupplied(f.ctx, f.svc, []byte(docV1), f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Register(f.ctx); err != nil {
		t.Fatal(err)
	}
	versions, err := f.svc.Versions(f.ctx, s2.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 {
		t.Fatalf("versions = %+v, want 1 (same bytes already registered)", versions)
	}
}

// TestOpenSuppliedThenPlacements pins the projection source: placements
// against an OpenSupplied session must compute off the supplied bytes
// (s.Content), not whatever happens to be latest in the CAS. A thread
// anchored against v1 re-anchors onto supplied v2 content, proving the
// placements read s.Content and not some other source.
func TestOpenSuppliedThenPlacements(t *testing.T) {
	f := setup(t)
	s := mustOpen(t, f)
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Comment(f.ctx, anchorFor(t, docV1, "token bucket with a burst capacity"), "why fifty?"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Comment(f.ctx, anchorFor(t, docV1, "Ship behind a feature flag to internal users first."), "which flag system?"); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenSupplied(f.ctx, f.svc, []byte(docV2), f.path)
	if err != nil {
		t.Fatal(err)
	}
	placements, err := s2.Placements(f.ctx)
	if err != nil || len(placements) != 2 {
		t.Fatalf("placements = %+v, %v", placements, err)
	}
	byBody := map[string]placement.Placement{}
	for _, p := range placements {
		byBody[p.Thread.Comments[0].Body] = p
	}
	design := byBody["why fifty?"]
	if design.Status != reanchor.StatusFuzzy && design.Status != reanchor.StatusExact && design.Status != reanchor.StatusMoved {
		t.Fatalf("design thread should attach against supplied v2, got %s", design.Status)
	}
	rollout := byBody["which flag system?"]
	if rollout.Status != reanchor.StatusOrphaned {
		t.Fatalf("rollout thread should orphan against supplied v2, got %s", rollout.Status)
	}
}

// TestSourcelessSessionsStayPlanless pins that an empty source is not an
// identity: neither Rediscover on a live sourceless session nor a fresh
// OpenSupplied may adopt some other sourceless plan just because both
// carry SourceHint "".
func TestSourcelessSessionsStayPlanless(t *testing.T) {
	f := setup(t)
	s, err := OpenSupplied(f.ctx, f.svc, []byte(docV1), "")
	if err != nil {
		t.Fatal(err)
	}

	// Another actor creates an unrelated sourceless plan.
	f2 := secondFixtureStore(t, f)
	other, err := OpenSupplied(f2.ctx, f2.svc, []byte(docV2), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Create(f2.ctx, "Unrelated"); err != nil {
		t.Fatal(err)
	}

	if err := s.Rediscover(f.ctx); err != nil {
		t.Fatal(err)
	}
	if s.Exists {
		t.Fatal("sourceless Rediscover must not adopt an unrelated sourceless plan")
	}

	fresh, err := OpenSupplied(f.ctx, f.svc, []byte(docV1), "")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Exists {
		t.Fatal("sourceless OpenSupplied must not adopt an unrelated sourceless plan")
	}
}

// TestRegisterOnPlanlessSessionErrors pins ensureRegistered's existing guard
// through the new exported Register wrapper: a sourceless (or otherwise
// plan-less) session cannot Register.
func TestRegisterOnPlanlessSessionErrors(t *testing.T) {
	f := setup(t)
	s, err := OpenSupplied(f.ctx, f.svc, []byte(docV1), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Register(f.ctx); !errors.Is(err, ErrPlanNotCreated) {
		t.Fatalf("err = %v, want ErrPlanNotCreated", err)
	}
}

// TestRegisterRepromotesRevertedContent pins revert re-promotion at the session
// altitude: save's semantics ("registers the content as the plan's newest
// version") must hold even when the content's hash was registered before. A
// registered scan that treats ANY past hash as "already registered" makes
// Register on reverted content silently no-op, leaving the plan's latest
// pointing at the unwanted newer version.
func TestRegisterRepromotesRevertedContent(t *testing.T) {
	f := setup(t)
	s := mustOpen(t, f)
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	v1Hash := s.Hash

	s2, err := OpenSupplied(f.ctx, f.svc, []byte(docV2), f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Register(f.ctx); err != nil {
		t.Fatal(err)
	}
	v2Hash := s2.Hash

	// Revert: supply v1's bytes again. resolveIdentity must NOT treat this
	// as already registered, since v1 is no longer the latest version.
	s3, err := OpenSupplied(f.ctx, f.svc, []byte(docV1), f.path)
	if err != nil {
		t.Fatal(err)
	}
	if s3.Hash != v1Hash {
		t.Fatalf("hash = %s, want %s", s3.Hash, v1Hash)
	}
	if err := s3.Register(f.ctx); err != nil {
		t.Fatal(err)
	}

	versions, err := f.svc.Versions(f.ctx, s.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 3 {
		t.Fatalf("versions = %+v, want 3 (revert re-promotes rather than no-op)", versions)
	}
	if versions[2].Hash != v1Hash {
		t.Fatalf("latest version hash = %s, want %s (v1 re-promoted)", versions[2].Hash, v1Hash)
	}
	if versions[1].Hash != v2Hash {
		t.Fatalf("versions = %+v, want v2 (%s) between the two v1 entries", versions, v2Hash)
	}
	// Re-supplying content that already equals the latest staying idempotent
	// is already covered by TestRegisterIdempotent, not re-asserted here.
}

// TestEnsureRegisteredUsesTheRememberedBase is the test that makes the
// compare-and-swap real. If the session re-reads the tip at write time, the
// precondition it sends is always current and the CAS can never fail -- which is
// invisible to any test that does not move the tip UNDERNEATH an open session.
func TestEnsureRegisteredUsesTheRememberedBase(t *testing.T) {
	f := setup(t)
	s := mustOpen(t, f)
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	v1Hash := s.Hash

	// The stale session opens against v1 — establishing v1 as its base — and
	// holds content it has not registered yet. Registration is the only
	// moment the base is used, so the session must carry unwritten content
	// for the precondition to be exercised at all.
	stale, err := OpenSupplied(f.ctx, f.svc, []byte(docV2), f.path)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Base() != v1Hash {
		t.Fatalf("Base() = %s, want the tip it opened against (%s)", stale.Base(), v1Hash)
	}

	// A sibling actor advances the plan while this session holds its base.
	f2 := secondFixtureStore(t, f)
	siblingHash := domain.HashContent([]byte("sibling content"))
	if _, err := f2.svc.RegisterVersion(f.ctx, s.Plan.ID, client.VersionRegistration{
		Content: []byte("sibling content"), Base: v1Hash,
	}); err != nil {
		t.Fatal(err)
	}

	// Now the stale session writes. Its base is stale, so it must conflict —
	// not silently supersede the sibling's version.
	err = stale.Register(f.ctx)
	var conflict *client.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale session err = %v, want *client.ConflictError", err)
	}
	if conflict.Tip.Hash != siblingHash {
		t.Fatalf("conflict.Tip.Hash = %s, want %s", conflict.Tip.Hash, siblingHash)
	}

	// A session opened AFTER the sibling write legitimately establishes the
	// new tip as its base and must succeed. This half proves the test is not
	// simply asserting that everything conflicts.
	fresh, err := OpenSupplied(f.ctx, f.svc, []byte(docV2), f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Register(f.ctx); err != nil {
		t.Fatalf("a session opened after the sibling write must succeed: %v", err)
	}
}

func TestCreateRegistersThroughEnsureRegistered(t *testing.T) {
	// If CreatePlan returns an existing plan (duplicate SourceHint) whose
	// registered versions do not include this session's hash, Create must
	// still register it rather than assuming registration.
	f := setup(t)

	// A prior session created the plan against docV1.
	s1 := mustOpen(t, f)
	if err := s1.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}

	// The file changes, and a second session (opened before knowing about
	// the plan? simulated by direct construction) calls Create and loses
	// the race: CreatePlan returns the existing plan.
	if err := os.WriteFile(f.path, []byte(docV2), 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := mustOpen(t, f)
	// Force the raced path: pretend s2 didn't see the plan at Open.
	s2.Exists = false
	if err := s2.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	versions, err := f.svc.Versions(f.ctx, s2.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range versions {
		if v.Hash == s2.Hash {
			found = true
		}
	}
	if !found {
		t.Fatal("losing Create must still register the session's own hash")
	}
}

// TestBaseIsEstablishedOnEveryPath tables every way a session comes into
// existence, plus the one way its base legitimately advances. Base() is not
// internal bookkeeping: it is the compare-and-swap precondition a register on
// this session sends, so a path that leaves it empty or stale turns the next
// register into a conflict against the tip it should have stood on.
//
// Two of these are otherwise unobservable. Deleting OpenVersion's establishment,
// or the `s.base = s.Hash` advance after a successful register, leaves the whole
// suite green, because in both cases registered=true short-circuits the write
// that would have noticed.
func TestBaseIsEstablishedOnEveryPath(t *testing.T) {
	// seed leaves a plan at f.path with two versions — docV1 then docV2 — so
	// the tip is deliberately NOT the hash of the file's on-disk content. A
	// path that forgets to establish a base leaves "", and one that takes it
	// from the session's own content rather than the plan lands on v1.
	type env struct {
		f      fixture
		plan   domain.Plan
		v1Hash domain.ContentHash
		tip    domain.ContentHash
	}
	seed := func(t *testing.T) env {
		t.Helper()
		f := setup(t)
		s := mustOpen(t, f)
		if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
			t.Fatal(err)
		}
		s2, err := OpenSupplied(f.ctx, f.svc, []byte(docV2), f.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s2.Register(f.ctx); err != nil {
			t.Fatal(err)
		}
		return env{f: f, plan: s.Plan, v1Hash: s.Hash, tip: s2.Hash}
	}

	cases := []struct {
		name  string
		build func(t *testing.T) (*Session, domain.ContentHash)
	}{
		{"Open", func(t *testing.T) (*Session, domain.ContentHash) {
			e := seed(t)
			// The file still holds docV1, so a base taken from the session's
			// own hash instead of the plan's tip would read v1.
			return mustOpen(t, e.f), e.tip
		}},
		{"OpenSupplied", func(t *testing.T) (*Session, domain.ContentHash) {
			e := seed(t)
			s, err := OpenSupplied(e.f.ctx, e.f.svc, []byte(docV1), e.f.path)
			if err != nil {
				t.Fatal(err)
			}
			return s, e.tip
		}},
		{"OpenVersion", func(t *testing.T) (*Session, domain.ContentHash) {
			e := seed(t)
			s, err := OpenVersion(e.f.ctx, e.f.svc, e.plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			return s, e.tip
		}},
		{"OpenSuppliedForPlan", func(t *testing.T) (*Session, domain.ContentHash) {
			e := seed(t)
			s, err := OpenSuppliedForPlan(e.f.ctx, e.f.svc, []byte(docV1), e.plan)
			if err != nil {
				t.Fatal(err)
			}
			return s, e.tip
		}},
		{"Create", func(t *testing.T) (*Session, domain.ContentHash) {
			e := seed(t)
			// A source no plan holds, so Create takes the create path rather
			// than return-existing. The base ends on the hash it registered.
			s, err := OpenSupplied(e.f.ctx, e.f.svc, []byte(docV2), "notion://fresh")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Create(e.f.ctx, "Fresh"); err != nil {
				t.Fatal(err)
			}
			return s, s.Hash
		}},
		{"Rediscover", func(t *testing.T) (*Session, domain.ContentHash) {
			e := seed(t)
			const source = "notion://late"
			late, err := OpenSupplied(e.f.ctx, e.f.svc, []byte(docV1), source)
			if err != nil {
				t.Fatal(err)
			}
			f2 := secondFixtureStore(t, e.f)
			other, err := OpenSupplied(f2.ctx, f2.svc, []byte(docV2), source)
			if err != nil {
				t.Fatal(err)
			}
			if err := other.Create(f2.ctx, "Late"); err != nil {
				t.Fatal(err)
			}
			if err := late.Rediscover(e.f.ctx); err != nil {
				t.Fatal(err)
			}
			return late, other.Hash
		}},
		{"after Register", func(t *testing.T) (*Session, domain.ContentHash) {
			e := seed(t)
			// Re-supplying docV1 over a tip of docV2 is a revert: it appends,
			// so the base must advance to what this session just registered
			// rather than stay on the tip it opened against.
			s, err := OpenSupplied(e.f.ctx, e.f.svc, []byte(docV1), e.f.path)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Register(e.f.ctx); err != nil {
				t.Fatal(err)
			}
			return s, e.v1Hash
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, want := tc.build(t)
			if s.Base() != want {
				t.Fatalf("Base() = %q, want %q", s.Base(), want)
			}
		})
	}
}

// countingContentSvc wraps a real client.PlanService and counts every Content
// call. Proving the constructors COMPILE with a narrower signature says nothing
// about whether OpenVersion's bytes still genuinely flow through svc.Content, or
// whether Open, OpenSupplied and OpenSuppliedForPlan ever touch it at all.
// Wrapping this file's ordinary fixture backend keeps
// ResolvePlan/Versions/CreatePlan/RegisterVersion's real semantics.
type countingContentSvc struct {
	client.PlanService
	contentCalls int
}

func (c *countingContentSvc) Content(ctx context.Context, id domain.PlanID, h domain.ContentHash) ([]byte, error) {
	c.contentCalls++
	return c.PlanService.Content(ctx, id, h)
}

// TestConstructorsSourceBytesOnlyThroughContent enumerates all four session
// constructors -- Open, OpenSupplied, OpenSuppliedForPlan and OpenVersion --
// pinning that none of them takes or consults a content store of its own.
// Content is the only place bytes can come from besides an argument the caller
// already holds, so the assertion counts calls to it rather than trusting the
// compiler: this codebase's signature defect is a rule proven for one
// constructor and silently absent for another, so all four get their own case
// rather than one standing in for the rest.
func TestConstructorsSourceBytesOnlyThroughContent(t *testing.T) {
	cases := []struct {
		name             string
		build            func(t *testing.T, f fixture, csvc *countingContentSvc) (*Session, error)
		wantContentCalls int
	}{
		{
			name: "Open",
			build: func(t *testing.T, f fixture, csvc *countingContentSvc) (*Session, error) {
				return Open(f.ctx, csvc, f.path)
			},
			wantContentCalls: 0,
		},
		{
			name: "OpenSupplied",
			build: func(t *testing.T, f fixture, csvc *countingContentSvc) (*Session, error) {
				return OpenSupplied(f.ctx, csvc, []byte(docV1), f.path)
			},
			wantContentCalls: 0,
		},
		{
			name: "OpenSuppliedForPlan",
			build: func(t *testing.T, f fixture, csvc *countingContentSvc) (*Session, error) {
				// Versions on an unknown id returns empty, nil rather than an
				// error (client.PlanService's own read-semantics rule), so this
				// plan need not have been created first -- OpenSuppliedForPlan
				// never calls Content either way.
				plan := domain.Plan{ID: "plan-never-created", SourceHint: f.path}
				return OpenSuppliedForPlan(f.ctx, csvc, []byte(docV1), plan)
			},
			wantContentCalls: 0,
		},
		{
			name: "OpenVersion",
			build: func(t *testing.T, f fixture, csvc *countingContentSvc) (*Session, error) {
				plan, err := csvc.CreatePlan(f.ctx, "T", f.path, "", []byte(docV1))
				if err != nil {
					t.Fatal(err)
				}
				return OpenVersion(f.ctx, csvc, plan.ID)
			},
			wantContentCalls: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			csvc := &countingContentSvc{PlanService: f.svc}

			s, err := tc.build(t, f, csvc)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if csvc.contentCalls != tc.wantContentCalls {
				t.Fatalf("%s: svc.Content called %d times, want %d", tc.name, csvc.contentCalls, tc.wantContentCalls)
			}
			if tc.wantContentCalls > 0 && string(s.Content) != docV1 {
				t.Fatalf("%s: Content = %q, want the bytes svc.Content answered with", tc.name, s.Content)
			}
		})
	}
}

// --- the source fault: one vocabulary for two carriers ---

// TestSourceFileFaultClassifiesWhatTheFileDid drives the classifier's rules
// the source-fault panel keys on, one row per distinguishable cause, including
// the three that are NOT faults at all. Every door reaches this one function, so
// no door gets to decide for itself which trigger deserves a panel.
//
// EVERY ERROR ROW BUILDS ITS ERROR BY CALLING THE REAL Open, deliberately. A row
// that hand-rolled an error would assert against this test's own idea of what
// Open returns, and the gap between those two things is exactly the defect:
// Open reads a file AND resolves identity, and classifying an unmarked error
// makes an unreadable state file answer "your file is unavailable" with the
// document sitting readable on disk.
//
// The last three rows are the non-vacuity half and each is its own bug if it
// ever goes green the other way. A SOURCELESS plan must classify as nothing at
// all: raising a fault would put "your file is gone" in front of a plan that
// never had one. An IDENTITY failure must classify as nothing either -- nothing
// happened to the file. And a file that resolves to its own plan is the ordinary
// open, which must stay silent.
func TestSourceFileFaultClassifiesWhatTheFileDid(t *testing.T) {
	const thisPlan = domain.PlanID("l_this_plan")
	other := domain.Plan{ID: "l_the_other_plan", Title: "Somebody Else's Plan"}

	tests := []struct {
		name      string
		sess      *Session
		openErr   func(t *testing.T) error
		wantState SourceFileState
		wantPlan  domain.Plan
	}{
		{
			name: "a deleted file is gone",
			openErr: func(t *testing.T) error {
				f := setup(t)
				if err := os.Remove(f.path); err != nil {
					t.Fatal(err)
				}
				_, err := Open(f.ctx, f.svc, f.path)
				return err
			},
			wantState: SourceFileGone,
		},
		{
			name: "a path that is there and will not read is unreadable",
			openErr: func(t *testing.T) error {
				f := setup(t)
				if err := os.Remove(f.path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(f.path, 0o755); err != nil {
					t.Fatal(err)
				}
				_, err := Open(f.ctx, f.svc, f.path)
				return err
			},
			wantState: SourceFileUnreadable,
		},
		{
			name:      "a file that reads fine and resolves to another plan is claimed",
			sess:      &Session{Exists: true, Plan: other},
			wantState: SourceFileClaimed,
			wantPlan:  other,
		},
		{
			name:      "a file that reads fine and resolves to no plan is released",
			sess:      &Session{},
			wantState: SourceFileReleased,
		},
		{
			name: "a sourceless plan has no file to be missing",
			openErr: func(t *testing.T) error {
				f := setup(t)
				_, err := Open(f.ctx, f.svc, "")
				return err
			},
		},
		{
			name: "a state file that will not parse is not a broken file",
			openErr: func(t *testing.T) error {
				f := setup(t)
				corruptState(t, f)
				_, err := Open(f.ctx, f.svc, f.path)
				return err
			},
		},
		{
			name: "a file that resolves to this very plan is not a fault",
			sess: &Session{Exists: true, Plan: domain.Plan{ID: thisPlan}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var openErr error
			if tc.openErr != nil {
				openErr = tc.openErr(t)
				if openErr == nil {
					t.Fatal("test setup: this row's Open was supposed to fail")
				}
			}
			got := SourceFileFault(thisPlan, "/tmp/plan.md", tc.sess, openErr)
			if tc.wantState == 0 {
				if got != nil {
					t.Fatalf("SourceFileFault = %v (state %s), want nil -- this is not a fault", got, got.State)
				}
				return
			}
			if got == nil {
				t.Fatalf("SourceFileFault = nil, want state %s", tc.wantState)
			}
			if got.State != tc.wantState {
				t.Errorf("State = %s, want %s", got.State, tc.wantState)
			}
			if got.PlanID != thisPlan {
				t.Errorf("PlanID = %s, want %s", got.PlanID, thisPlan)
			}
			if got.Path != "/tmp/plan.md" {
				t.Errorf("Path = %q, want /tmp/plan.md", got.Path)
			}
			if got.Plan.ID != tc.wantPlan.ID || got.Plan.Title != tc.wantPlan.Title {
				t.Errorf("Plan = %+v, want %+v -- only a claimed file has another plan to name", got.Plan, tc.wantPlan)
			}
			if !errors.Is(got, ErrSourceFileGone) {
				t.Errorf("errors.Is(%v, ErrSourceFileGone) = false -- every existing arm must behave identically", got)
			}
			// THE CAUSE SURVIVES; DRAFTPLANE'S OWN MARKER DOES NOT. Asserting
			// errors.Is(got, openErr) -- identity with the whole `%w: %w` value Open
			// composes -- would be true and would hide the defect it looks like it covers:
			// the field's own doc promises "the failure the file system reported", and a
			// panel drawing it verbatim would draw ErrSourceUnreadable's sentence restating
			// that panel's own headline. So the claim is made about the CAUSE, and the
			// marker is asserted ABSENT from the field. It is still on the error Open
			// returned, where every reader of it looks (readCause).
			if openErr != nil {
				cause := error(nil)
				var pe *fs.PathError
				if errors.As(openErr, &pe) {
					cause = pe
				} else {
					cause = openErr
				}
				if !errors.Is(got, cause) {
					t.Errorf("errors.Is(%v, %v) = false -- the cause the %%w:%%w wrap used to carry must survive", got, cause)
				}
				if got.Err != nil && errors.Is(got.Err, ErrSourceUnreadable) {
					t.Errorf("Err = %v still carries Draftplane's own marker -- this field is the file system's own report, and a door renders it", got.Err)
				}
			}
		})
	}
}

// TestSourceFileFaultDoesNotExemptAFileURL pins the boundary of
// sourceFileFault's URL-source arm: this arm exempts sources that were
// NEVER local files, and a file:// URL IS one. client.URLSource trims
// exactly that scheme before asking "://" of what remains, so a file:// path
// keeps reporting a genuinely missing file rather than being silently
// swallowed alongside notion:// and https://.
//
// THIS IS NOT A VACUOUS CLAIM ABOUT TODAY'S CODE; IT IS A TRIPWIRE FOR A
// TOMORROW ONE. The obvious way to "simplify" URLSource later is to drop
// its file:// trim and test the raw source for "://" alone -- which would
// still answer correctly for notion:// and https://, so nothing else in this
// package's own test suite would notice. It would also make THIS arm exempt
// every file://-sourced plan's source file from ever being reported gone or
// unreadable again, silently, for both doors at once -- exactly the class of
// regression this test exists to close, reopened one line at a time.
func TestSourceFileFaultDoesNotExemptAFileURL(t *testing.T) {
	f := setup(t)
	if err := os.Remove(f.path); err != nil {
		t.Fatal(err)
	}
	fileURL := "file://" + f.path
	_, openErr := Open(f.ctx, f.svc, fileURL)
	if openErr == nil {
		t.Fatal("test setup: Open on a removed file was supposed to fail")
	}
	got := SourceFileFault(domain.PlanID("l_this_plan"), fileURL, nil, openErr)
	if got == nil {
		t.Fatal("SourceFileFault = nil, want state gone -- a file:// URL names a real local file, and " +
			"the URL-source arm must not treat it as one that was never local")
	}
	if got.State != SourceFileGone {
		t.Errorf("State = %s, want %s", got.State, SourceFileGone)
	}
}

// TestOpenPlanLatchesTheFaultOnTheSnapshotItFallsBackTo is the helper this
// guarantee requires -- one function all three doors reach -- proving both
// halves in one table: the fallback still happens exactly as openPlanCmd's
// own switch made it happen, and the reason it happened is recorded on the
// session it produced instead of being thrown away.
//
// The sourceless row is the one this helper could most easily have got wrong: it
// fires the SAME fallback body as a broken file (one rule, not three
// copies), and a helper that latched a fault across the whole fallback rather
// than classifying its trigger would raise a panel for the sourceless plan,
// where nothing is broken.
//
// THE UNREADABLE ROW IS THE WIDENING: with the trigger at fs.ErrNotExist
// alone this row hard-errors instead of falling back, and the unreadable state
// can never appear on this carrier at all -- half of the classifier's rule with
// nothing able to produce it. A directory in the file's place rather than chmod:
// EISDIR is not a privilege a root test runner bypasses.
//
// THE IDENTITY ROW is the line the widening does not cross for anybody. It
// asserts on the SESSION being refused rather than only on the error, because
// the session is what a false "reviewing snapshot (no local copy)" would have
// been stamped on. It fails Open's plan lookup while the file reads and every
// by-id read still works, so a fallback widened past these triggers would open
// the snapshot there instead of staying the hard error a failed lookup always
// was: it is not a fact about the document.
func TestOpenPlanLatchesTheFaultOnTheSnapshotItFallsBackTo(t *testing.T) {
	errLookup := errors.New("plan lookup failed")
	tests := []struct {
		name          string
		sourceless    bool
		urlSource     bool
		removeFile    bool
		dirInItsPlace bool
		lookupFails   bool
		wantSnapshot  bool
		wantState     SourceFileState
		wantErr       error
	}{
		{name: "the file is gone: fall back and say why", removeFile: true, wantSnapshot: true, wantState: SourceFileGone},
		{name: "the file will not read: fall back and say that instead", dirInItsPlace: true, wantSnapshot: true, wantState: SourceFileUnreadable},
		{name: "a sourceless plan falls back with nothing to say", sourceless: true, wantSnapshot: true},
		// A plan whose SourceHint is itself a client.URLSource URL rather than a
		// path. Open still tries os.ReadFile on the literal URL and gets ENOENT, so
		// this row would misclassify as SourceFileGone without the fix -- see
		// sourceFileFault's own URL-source arm for why that failure is not this
		// file's to report.
		{name: "a plan with a URL source falls back with nothing to say", urlSource: true, wantSnapshot: true},
		{name: "a failed identity lookup stays a hard error, never a snapshot", lookupFails: true, wantErr: errLookup},
		{name: "the file is there: no fallback and no fault", wantSnapshot: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			var plan domain.Plan
			switch {
			case tc.sourceless:
				p, err := f.svc.CreatePlan(f.ctx, "Sourceless", "", "", []byte(docV1))
				if err != nil {
					t.Fatal(err)
				}
				plan = p
			case tc.urlSource:
				p, err := f.svc.CreatePlan(f.ctx, "Notion Plan", "notion://url-source-test", "", []byte(docV1))
				if err != nil {
					t.Fatal(err)
				}
				plan = p
			default:
				s := mustOpen(t, f)
				if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
					t.Fatal(err)
				}
				plan = s.Plan
			}
			if tc.removeFile || tc.dirInItsPlace {
				if err := os.Remove(f.path); err != nil {
					t.Fatal(err)
				}
			}
			if tc.dirInItsPlace {
				if err := os.Mkdir(f.path, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			svc := client.PlanService(f.svc)
			if tc.lookupFails {
				svc = lookupFailingSvc{PlanService: f.svc, err: errLookup}
			}

			got, err := OpenPlan(f.ctx, svc, plan)
			if tc.wantErr != nil {
				if got != nil {
					t.Fatalf("OpenPlan returned a session (FromSnapshot=%v, SourceFault=%v) where it must refuse: "+
						"a fallback nobody can explain is the silent document swap this fallback's narrowness exists to prevent",
						got.FromSnapshot(), got.SourceFault)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("OpenPlan err = %v, want errors.Is(err, %v)", err, tc.wantErr)
				}
				if errors.Is(err, ErrSourceFileGone) {
					t.Fatalf("OpenPlan err = %v, must not read as a source-file problem", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("OpenPlan: %v", err)
			}
			if got.FromSnapshot() != tc.wantSnapshot {
				t.Errorf("FromSnapshot() = %v, want %v", got.FromSnapshot(), tc.wantSnapshot)
			}
			if tc.wantState == 0 {
				if got.SourceFault != nil {
					t.Fatalf("SourceFault = %v (state %s), want nil", got.SourceFault, got.SourceFault.State)
				}
				return
			}
			if got.SourceFault == nil {
				t.Fatalf("SourceFault = nil, want state %s", tc.wantState)
			}
			if got.SourceFault.State != tc.wantState {
				t.Errorf("SourceFault.State = %s, want %s", got.SourceFault.State, tc.wantState)
			}
			if got.SourceFault.Path != f.path {
				t.Errorf("SourceFault.Path = %q, want %q", got.SourceFault.Path, f.path)
			}
		})
	}
}

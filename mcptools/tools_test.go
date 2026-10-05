package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/client/identity"
	"github.com/draftplane/draftplane/client/localfs"
	"github.com/draftplane/draftplane/client/recent"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/reanchor"
	"github.com/draftplane/draftplane/session"
	"github.com/draftplane/draftplane/store/localcas"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// docV1 mirrors the Rate-Limiter-style fixture used across app and session
// tests, with a sentence duplicated verbatim across two sections (Context and
// Design) so a quote can be ambiguous, and a Rollout section that V2 deletes
// outright so a thread anchored there orphans.
const docV1 = `# Rate Limiter Plan

## Context

Requests are currently unbounded and the database suffers under load spikes. This system must remain highly available under peak traffic.

## Design

We will use a token bucket with a burst capacity of fifty requests. This system must remain highly available under peak traffic.

## Rollout

Ship behind a feature flag to internal users first.
`

// docV2: Design reworded (fuzzy), Rollout deleted (orphan), Context unchanged.
const docV2 = `# Rate Limiter Plan

## Context

Requests are currently unbounded and the database suffers under load spikes. This system must remain highly available under peak traffic.

## Design

A token bucket limiter with burst capacity fifty is the chosen approach.
`

type fixture struct {
	dir  string
	path string
	cas  *localcas.Store
	ctx  context.Context
}

// setup's ctx carries "agent" attribution by default, for the handful of
// fixture calls that write directly against a *localfs.Store or *session.Session
// rather than through Tools -- those bypass Tools.attribute entirely. A Tools
// call computes and decorates its own attribution every time regardless of what
// f.ctx already carries: passing it in is a convenience, not a source of
// identity. TestTwoActorJourney needs a second, distinct identity for the human
// half of its journey; see its own ctxAs("alice") calls.
func setup(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(path, []byte(docV1), 0o644); err != nil {
		t.Fatal(err)
	}
	return fixture{dir: dir, path: path, cas: localcas.New(filepath.Join(dir, "objects")), ctx: ctxAs("agent")}
}

func (f fixture) statePath() string { return filepath.Join(f.dir, "state.json") }

func (f fixture) newStore(t *testing.T) *localfs.Store {
	t.Helper()
	svc, err := localfs.New(f.statePath())
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// planClaimingPath gives path a second plan: one titled title, with content as
// its one version, pointed at path after the plan that already follows it.
func planClaimingPath(t *testing.T, f fixture, local *localfs.Store, title, path string, content []byte) domain.PlanID {
	t.Helper()
	p, err := local.CreatePlan(f.ctx, title, "", "", content)
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if err := local.SetSourceHint(f.ctx, p.ID, path); err != nil {
		t.Fatalf("SetSourceHint: %v", err)
	}
	return p.ID
}

// ctxAs decorates a fresh background context with actorDisplay's attribution,
// standing in for the actor string localfs.New used to take.
//
// IT IS A FIFTH attrCtx UNDER A DIFFERENT NAME: the other four (app,
// cmd/draftplane, client/localfs, session) are byte-identical to each other, and
// a grep for "func attrCtx" cannot see this copy. Keep it in step with them.
//
// IT TAKES ONE STRING AND WRITES TWO. A fixture holding the same string in both
// halves cannot say which half a renderer read -- it passes whichever one the
// code picks, so it proves nothing. attrLogin derives the login half rather than
// accepting it, so no caller can make the two equal by accident.
func ctxAs(actorDisplay string) context.Context {
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

func TestTwoActorJourney(t *testing.T) {
	f := setup(t)

	// A human seeds a comment via a real session, before the agent ever
	// touches the file. Its own ctx, distinct from f.ctx: this is the one
	// test in the file where a human and an agent write to the same store,
	// so it is the one place attribution must actually vary by call rather
	// than take the fixture's default.
	humanCtx := ctxAs("alice")
	human := f.newStore(t)
	hs, err := session.Open(humanCtx, human, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := hs.Create(humanCtx, "Rate Limiter Plan"); err != nil {
		t.Fatal(err)
	}
	designAnchor, err := session.AnchorForBlockText(docV1, "token bucket with a burst capacity of fifty requests", nil)
	if err != nil {
		t.Fatal(err)
	}
	designThread, err := hs.Comment(humanCtx, designAnchor, "why fifty?")
	if err != nil {
		t.Fatal(err)
	}

	// From here on, everything happens through the Tools surface, which
	// mints its own agent identity per call (f.ctx's own attribution is
	// never consulted), sharing the same on-disk store.
	agentSvc := f.newStore(t)
	tools := New(agentSvc, "calm-mountain")
	ctx := f.ctx

	review, err := tools.GetReview(ctx, GetReviewArgs{Source: f.path})
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Threads) != 1 {
		t.Fatalf("threads = %+v, want 1", review.Threads)
	}
	th := review.Threads[0]
	if th.Status != "exact" {
		t.Fatalf("status = %q, want exact", th.Status)
	}
	if got := strings.Join(th.HeadingPath, ">"); got != "Rate Limiter Plan>Design" {
		t.Fatalf("heading_path = %q, want Rate Limiter Plan>Design", got)
	}
	// The LOGIN, not the display name: ReviewComment.Author renders through
	// ui.FormatAttribution, which reads domain.Attribution.ActorLogin.
	// humanCtx seeded both halves and they differ, so naming either one
	// here is a real choice between two strings the fixture holds -- and it is
	// derived through the same attrLogin that built it, so the two cannot drift.
	if len(th.Comments) != 1 || th.Comments[0].Author != attrLogin("alice") {
		t.Fatalf("comments = %+v, want 1 from %s", th.Comments, attrLogin("alice"))
	}

	// Reply and resolve as the agent.
	replyRes, err := tools.Reply(ctx, ReplyArgs{Source: f.path, ThreadID: string(designThread.ID), Body: "agreed, will fix"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Resolve(ctx, ResolveArgs{Source: f.path, ThreadID: string(designThread.ID), Resolved: true}); err != nil {
		t.Fatal(err)
	}
	review, err = tools.GetReview(ctx, GetReviewArgs{Source: f.path})
	if err != nil {
		t.Fatal(err)
	}
	th = findThread(t, review, string(designThread.ID))
	if !th.Resolved {
		t.Fatal("thread should be resolved")
	}
	// The agent's reply carries no ActorLogin: Tools.attribute records only
	// the agent. ReviewComment.Author renders through ui.FormatAttribution,
	// which surfaces Attribution.Agent when there is no login to pair it with
	// -- the minted identity Reply itself returned, prefixed "agent-".
	wantReplyAuthor := "agent-" + replyRes.AgentID
	if len(th.Comments) != 2 || th.Comments[0].Author != attrLogin("alice") || th.Comments[1].Author != wantReplyAuthor {
		t.Fatalf("comments = %+v, want [alice, %q]", th.Comments, wantReplyAuthor)
	}

	// A too-short quote is rejected with agent-coaching mentioning "3".
	_, err = tools.Comment(ctx, CommentArgs{Source: f.path, Quote: "token bucket", Body: "which?"})
	if err == nil || !strings.Contains(err.Error(), "3") {
		t.Fatalf("err = %v, want mention of 3", err)
	}

	// An ambiguous quote (present in two sections) is rejected, coaching the
	// agent toward heading_path.
	dup := "This system must remain highly available under peak traffic."
	_, err = tools.Comment(ctx, CommentArgs{Source: f.path, Quote: dup, Body: "how available?"})
	if err == nil || !strings.Contains(err.Error(), "heading_path") {
		t.Fatalf("err = %v, want mention of heading_path", err)
	}

	// Retrying with heading_path disambiguates and succeeds.
	contextThread, err := tools.Comment(ctx, CommentArgs{
		Source: f.path, Quote: dup, HeadingPath: []string{"Rate Limiter Plan", "Context"}, Body: "how available?",
	})
	if err != nil {
		t.Fatal(err)
	}

	// A section comment succeeds and is marked section:true.
	rolloutThread, err := tools.CommentSection(ctx, CommentSectionArgs{
		Source: f.path, HeadingPath: []string{"Rate Limiter Plan", "Rollout"}, Body: "need a rollback plan",
	})
	if err != nil {
		t.Fatal(err)
	}
	review, err = tools.GetReview(ctx, GetReviewArgs{Source: f.path})
	if err != nil {
		t.Fatal(err)
	}
	if !findThread(t, review, string(rolloutThread.ThreadID)).Section {
		t.Fatal("comment_section thread should have section: true")
	}

	// The document is edited (by the agent's own file tools, not draftplane):
	// Design is reworded, Rollout is deleted outright.
	if err := os.WriteFile(f.path, []byte(docV2), 0o644); err != nil {
		t.Fatal(err)
	}

	review, err = tools.GetReview(ctx, GetReviewArgs{Source: f.path})
	if err != nil {
		t.Fatal(err)
	}
	design := findThread(t, review, string(designThread.ID))
	if design.Status != "fuzzy" {
		t.Fatalf("design status = %q, want fuzzy", design.Status)
	}
	rollout := findThread(t, review, string(rolloutThread.ThreadID))
	if rollout.Status != "orphaned" || len(rollout.Candidates) == 0 {
		t.Fatalf("rollout thread = %+v, want orphaned with candidates", rollout)
	}
	_ = contextThread

	// Rehome the orphan onto surviving text.
	if _, err := tools.Rehome(ctx, RehomeArgs{
		Source: f.path, ThreadID: string(rolloutThread.ThreadID),
		Quote: "Requests are currently unbounded and the database suffers under load spikes.",
	}); err != nil {
		t.Fatal(err)
	}
	review, err = tools.GetReview(ctx, GetReviewArgs{Source: f.path})
	if err != nil {
		t.Fatal(err)
	}
	rollout = findThread(t, review, string(rolloutThread.ThreadID))
	if rollout.Status == "orphaned" {
		t.Fatalf("rollout thread still orphaned after rehome: %+v", rollout)
	}

	// Approve binds to the exact current hash.
	approveRes, err := tools.Approve(ctx, ApproveArgs{Source: f.path})
	if err != nil {
		t.Fatal(err)
	}
	if !approveRes.OK || approveRes.Hash == "" {
		t.Fatalf("approve result = %+v", approveRes)
	}
	review, err = tools.GetReview(ctx, GetReviewArgs{Source: f.path})
	if err != nil {
		t.Fatal(err)
	}
	if !review.ApprovedCurrent {
		t.Fatal("approved_current should be true after approve")
	}
	if review.Hash != approveRes.Hash {
		t.Fatalf("review hash = %q, want %q", review.Hash, approveRes.Hash)
	}

	// The approved content is stored as an exhibit.
	has, err := f.cas.Has(ctx, domain.ContentHash(approveRes.Hash))
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("approved content should be stored in the content store")
	}
}

func findThread(t *testing.T, review GetReviewResult, id string) ReviewThread {
	t.Helper()
	for _, th := range review.Threads {
		if th.ID == id {
			return th
		}
	}
	t.Fatalf("thread %q not found in %+v", id, review.Threads)
	return ReviewThread{}
}

// TestGetReviewAuthorAllFourAttributionShapes drives all four Attribution
// shapes get_review can encounter through one thread's comments -- seeded
// directly against the store, bypassing Tools.attribute (which always mints its
// own Agent) -- and checks ReviewComment.Author renders each through the
// identical ui.FormatAttribution the TUI does. This distinction matters most
// for multi-agent coordination: a coordinator's get_review must tell its own
// subagents' comments apart from each other and from a human's, not fold them
// all under one field.
func TestGetReviewAuthorAllFourAttributionShapes(t *testing.T) {
	f := setup(t)
	store := f.newStore(t)
	tools := New(store, "calm-mountain")

	s, err := session.Open(f.ctx, store, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(f.ctx, "Rate Limiter Plan"); err != nil {
		t.Fatal(err)
	}
	anchor, err := session.AnchorForBlockText(docV1, "token bucket with a burst capacity of fifty requests", nil)
	if err != nil {
		t.Fatal(err)
	}

	loginOnly := domain.Attribution{ActorID: "u1", ActorLogin: "dana-loves-coding", ActorDisplay: "danaLovesCoding"}
	neither := domain.Attribution{}
	both := domain.Attribution{ActorID: "u1", ActorLogin: "dana-loves-coding", ActorDisplay: "danaLovesCoding", Agent: "blue-parakeet-f9"}
	agentOnly := domain.Attribution{Agent: "blue-parakeet-f9"}

	th, err := s.Comment(client.WithAttribution(context.Background(), loginOnly), anchor, "login, no agent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reply(client.WithAttribution(context.Background(), neither), th.ID, "no login, no agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reply(client.WithAttribution(context.Background(), both), th.ID, "login and agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reply(client.WithAttribution(context.Background(), agentOnly), th.ID, "agent, no login"); err != nil {
		t.Fatal(err)
	}

	review, err := tools.GetReview(f.ctx, GetReviewArgs{Source: f.path})
	if err != nil {
		t.Fatal(err)
	}
	got := findThread(t, review, string(th.ID))
	// WHEREVER A HUMAN IS NAMED HERE, IT IS BY LOGIN. The two attributions
	// above that name one seed ActorLogin and ActorDisplay with different
	// strings, so these wants say which half the MCP surface relays -- and an
	// agent reading a thread over MCP must see the identical voice the TUI
	// paints, which is the login.
	want := []string{
		"dana-loves-coding",
		"user-calm-mountain",
		"agent-blue-parakeet-f9 ● dana-loves-coding",
		"agent-blue-parakeet-f9",
	}
	if len(got.Comments) != len(want) {
		t.Fatalf("comments = %+v, want %d entries", got.Comments, len(want))
	}
	for i, w := range want {
		if got.Comments[i].Author != w {
			t.Fatalf("comment %d author = %q, want %q", i, got.Comments[i].Author, w)
		}
	}
}

func TestGetReviewNoPlanGuidance(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	review, err := tools.GetReview(f.ctx, GetReviewArgs{Source: f.path})
	if err != nil {
		t.Fatal(err)
	}
	if review.Plan != nil {
		t.Fatalf("plan = %+v, want nil", review.Plan)
	}
	if review.Guidance == "" {
		t.Fatal("guidance should be non-empty when no plan exists")
	}
}

// TestGetReviewRemembersNothing is a guard, not a feature test: it passes
// today and is meant to fail the day someone records an open below the
// doors (in session.Open, say) rather than only at the terminal's two
// doors -- an agent's get_review must leave no trace in Recently
// opened, ever.
func TestGetReviewRemembersNothing(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")
	if _, err := tools.GetReview(f.ctx, GetReviewArgs{Source: f.path}); err != nil {
		t.Fatal(err)
	}
	path, err := recent.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("get_review left %s behind (stat err %v) -- an agent's read must never reach Recently opened", path, err)
	}
}

// TestListPlansSourceHintJSONTag pins the MCP JSON contract for a plan's
// origin: ListPlans surfaces the plan's actual SourceHint end to end, and
// PlanInfo marshals it under the "source" key — renamed, alongside the
// domain.Plan field itself, from the pre-rename "path_hint".
func TestListPlansSourceHintJSONTag(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	s, err := session.Open(f.ctx, tools.svc, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}

	res, err := tools.ListPlans(f.ctx, ListPlansArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Plans) != 1 || res.Plans[0].SourceHint != f.path {
		t.Fatalf("plans = %+v, want one plan with SourceHint %q", res.Plans, f.path)
	}

	raw, err := json.Marshal(res.Plans[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"source":`) {
		t.Fatalf(`PlanInfo JSON must carry the "source" key: %s`, raw)
	}
	if strings.Contains(string(raw), "path_hint") {
		t.Fatalf("PlanInfo JSON must not carry the legacy path_hint key: %s", raw)
	}
}

func TestRehomeArgValidation(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	cases := []struct {
		name        string
		quote       string
		headingPath []string
	}{
		{"neither", "", nil},
		{"both", "some exact quote", []string{"Rate Limiter Plan", "Design"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := tools.Rehome(f.ctx, RehomeArgs{
				Source: f.path, ThreadID: "thread-1", Quote: c.quote, HeadingPath: c.headingPath,
			})
			if err == nil || !strings.Contains(err.Error(), "exactly one") {
				t.Fatalf("err = %v, want mention of exactly one", err)
			}
		})
	}
}

func TestInMemoryTransportSmoke(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")
	server := NewServer(tools)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	serverSession, err := server.Connect(f.ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = serverSession.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	clientSession, err := client.Connect(f.ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = clientSession.Close() }()

	listRes, err := clientSession.ListTools(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listRes.Tools) != len(ToolNames()) {
		t.Fatalf("got %d tools, want %d", len(listRes.Tools), len(ToolNames()))
	}
	want := map[string]bool{}
	for _, n := range ToolNames() {
		want[n] = true
	}
	for _, tl := range listRes.Tools {
		if !want[tl.Name] {
			t.Fatalf("unexpected tool %q", tl.Name)
		}
		delete(want, tl.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing tools: %v", want)
	}

	callRes, err := clientSession.CallTool(f.ctx, &mcp.CallToolParams{
		Name:      "get_review",
		Arguments: map[string]any{"source": f.path},
	})
	if err != nil {
		t.Fatal(err)
	}
	if callRes.IsError {
		t.Fatalf("get_review call errored: %+v", callRes.Content)
	}
	sc, ok := callRes.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content = %#v, want a map", callRes.StructuredContent)
	}
	if _, ok := sc["hash"]; !ok {
		t.Fatalf("structured content missing hash: %#v", sc)
	}

	saveRes, err := clientSession.CallTool(f.ctx, &mcp.CallToolParams{
		Name:      "save",
		Arguments: map[string]any{"content": docV1, "source": "notion://smoke-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if saveRes.IsError {
		t.Fatalf("save call errored: %+v", saveRes.Content)
	}
	saveSC, ok := saveRes.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("save structured content = %#v, want a map", saveRes.StructuredContent)
	}
	// Save must NOT echo the supplied bytes; the agent-visible readback path
	// is get_review, asserted next.
	if got, present := saveSC["content"]; present && got != "" {
		t.Fatalf("save structured content echoes supplied bytes: %#v", saveSC)
	}
	planID, _ := saveSC["plan"].(map[string]any)["id"].(string)
	grRes, err := clientSession.CallTool(f.ctx, &mcp.CallToolParams{
		Name:      "get_review",
		Arguments: map[string]any{"plan_id": planID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if grRes.IsError {
		t.Fatalf("get_review call errored: %+v", grRes.Content)
	}
	grSC, ok := grRes.StructuredContent.(map[string]any)
	if !ok || grSC["content"] != docV1 {
		t.Fatalf("get_review structured content missing readback: %#v", grRes.StructuredContent)
	}

	badCall, err := clientSession.CallTool(f.ctx, &mcp.CallToolParams{
		Name:      "comment",
		Arguments: map[string]any{"source": f.path, "quote": "too short", "body": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !badCall.IsError {
		t.Fatal("comment with a 2-word quote should be a tool-result error")
	}
	if badCall.StructuredContent != nil {
		t.Fatalf("error result must not carry structured content, got %#v", badCall.StructuredContent)
	}
	var text string
	for _, c := range badCall.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if !strings.Contains(text, "3") {
		t.Fatalf("error text = %q, want mention of 3", text)
	}
}

// ---- save ----

func TestSaveCreatesBySource(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	res, err := tools.Save(f.ctx, SaveArgs{Content: docV1, Source: "notion://rate-limiter"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan == nil || res.Plan.ID == "" {
		t.Fatalf("plan = %+v, want a created plan", res.Plan)
	}
	if res.Plan.Title != "Rate Limiter Plan" {
		t.Fatalf("title = %q, want the doc's H1", res.Plan.Title)
	}
	if len(res.Threads) != 0 {
		t.Fatalf("threads = %+v, want none on a fresh save", res.Threads)
	}
	// Save never echoes content back — the caller supplied those bytes one
	// argument ago. get_review is the readback path (asserted below).
	if res.Content != "" {
		t.Fatalf("save content = %q, want empty (no readback of supplied bytes)", res.Content)
	}
	review, err := tools.GetReview(f.ctx, GetReviewArgs{PlanID: res.Plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	if review.Content != docV1 {
		t.Fatalf("get_review content = %q, want the saved bytes", review.Content)
	}

	plans, err := tools.ListPlans(f.ctx, ListPlansArgs{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range plans.Plans {
		if p.ID == res.Plan.ID && p.SourceHint == "notion://rate-limiter" {
			found = true
		}
	}
	if !found {
		t.Fatalf("list_plans should show the saved source: %+v", plans.Plans)
	}
}

// TestSaveUpdateReanchors pins save's update path: a second save under the
// same source registers a new version, and the thread seeded against v1
// re-anchors (recomputed on the fly, same as get_review after a file edit)
// onto the reworded v2 text.
func TestSaveUpdateReanchors(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	v1, err := tools.Save(f.ctx, SaveArgs{Content: docV1, Source: "notion://rate-limiter"})
	if err != nil {
		t.Fatal(err)
	}
	designThread, err := tools.Comment(f.ctx, CommentArgs{
		Source: "notion://rate-limiter", Quote: "token bucket with a burst capacity of fifty requests", Body: "why fifty?",
	})
	if err != nil {
		t.Fatal(err)
	}

	v2, err := tools.Save(f.ctx, SaveArgs{Content: docV2, Source: "notion://rate-limiter"})
	if err != nil {
		t.Fatal(err)
	}
	if v2.Plan.ID != v1.Plan.ID {
		t.Fatalf("save v2 created a different plan: %s vs %s", v2.Plan.ID, v1.Plan.ID)
	}
	design := findThread(t, v2, string(designThread.ThreadID))
	if design.Status != "fuzzy" {
		t.Fatalf("design status = %q, want fuzzy after the v2 reword", design.Status)
	}
}

// TestSaveSourceless pins the no-source-at-all path: save with neither
// source nor plan_id creates a plan an agent can address purely by the
// plan_id save hands back.
func TestSaveSourceless(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	res, err := tools.Save(f.ctx, SaveArgs{Content: docV1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan == nil || res.Plan.ID == "" {
		t.Fatalf("plan = %+v, want a created plan", res.Plan)
	}

	if _, err := tools.Comment(f.ctx, CommentArgs{
		PlanID: res.Plan.ID, Quote: "token bucket with a burst capacity of fifty requests", Body: "why fifty?",
	}); err != nil {
		t.Fatal(err)
	}

	review, err := tools.GetReview(f.ctx, GetReviewArgs{PlanID: res.Plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	if review.Content != docV1 {
		t.Fatalf("content = %q, want %q", review.Content, docV1)
	}
	if len(review.Threads) != 1 {
		t.Fatalf("threads = %+v, want the follow-up comment", review.Threads)
	}
}

// TestSaveSourcelessNoHeadingGetsPlaceholderTitle pins ui.InferTitle's
// empty-path fallback fix end to end: a sourceless save whose content has
// no H1 must not create a plan titled "." (filepath.Base("")'s nonsensical
// answer) — it falls back to a fixed placeholder instead.
func TestSaveSourcelessNoHeadingGetsPlaceholderTitle(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	res, err := tools.Save(f.ctx, SaveArgs{Content: "Just a body, no heading.\n"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan == nil || res.Plan.Title != "Untitled" {
		t.Fatalf("plan = %+v, want title Untitled", res.Plan)
	}
}

func TestSaveIdempotent(t *testing.T) {
	f := setup(t)
	store := f.newStore(t)
	tools := New(store, "calm-mountain")

	res, err := tools.Save(f.ctx, SaveArgs{Content: docV1, Source: "notion://idempotent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Save(f.ctx, SaveArgs{Content: docV1, Source: "notion://idempotent"}); err != nil {
		t.Fatal(err)
	}

	versions, err := store.Versions(f.ctx, domain.PlanID(res.Plan.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 {
		t.Fatalf("versions = %+v, want 1 (same bytes saved twice)", versions)
	}
}

// TestSaveRefusesReadableFile pins the capability refusal, and the
// no-plan_id-backdoor rule: a plan backed by a real readable file refuses
// save identically whether addressed by its source (the path) or its
// plan_id. The capability rule is the ONLY thing standing between an agent's
// bytes and a document draftplane re-reads from disk on every open.
func TestSaveRefusesReadableFile(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	_, err := tools.Save(f.ctx, SaveArgs{Content: docV2, Source: f.path})
	if err == nil || !strings.Contains(err.Error(), "readable file") {
		t.Fatalf("err = %v, want coaching about a readable local file", err)
	}

	// Create a real file-backed plan, then confirm plan_id addressing hits
	// the exact same refusal — no backdoor around the capability rule.
	if _, err := tools.Comment(f.ctx, CommentArgs{
		Source: f.path, Quote: "token bucket with a burst capacity of fifty requests", Body: "why fifty?",
	}); err != nil {
		t.Fatal(err)
	}
	review, err := tools.GetReview(f.ctx, GetReviewArgs{Source: f.path})
	if err != nil {
		t.Fatal(err)
	}

	_, err = tools.Save(f.ctx, SaveArgs{Content: docV2, PlanID: review.Plan.ID})
	if err == nil || !strings.Contains(err.Error(), "readable file") {
		t.Fatalf("plan_id-addressed save err = %v, want the same capability refusal", err)
	}

	// THE REFUSAL NAMES BOTH REMEDIES. "edit it with your own tools and use the
	// review verbs" is correct for the call site that fires on a plan already
	// tracking a human's document and actively harmful on the other, which
	// fires on a path the CALLER named: that remedy walks an agent naming its
	// own staging copy into having draftplane track a file that is about to be
	// deleted. A refusal whose stated remedy produces the outcome
	// it exists to prevent is worse than no remedy at all, so both clauses are
	// pinned rather than the sentinel alone.
	for _, want := range []string{
		"a document a human works in",            // the existing-plan door's remedy
		"omit source and save the content alone", // the named-a-staging-path door's
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not carry %q; it reads %q", want, err.Error())
		}
	}
}

func TestSaveBothHandlesError(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	_, err := tools.Save(f.ctx, SaveArgs{Content: docV1, Source: "notion://x", PlanID: "plan-1"})
	if err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Fatalf("err = %v, want mention of 'at most one'", err)
	}
}

// TestSaveSourcelessTwiceCreatesDistinctPlans pins the sourceless-create fix
// (localfs's empty-sourceHint-never-equal rule) at the MCP handler
// altitude: two sourceless saves in a row must create two distinct plans,
// each with its own title and its own single-entry version set — not the
// second save silently landing on the first plan.
func TestSaveSourcelessTwiceCreatesDistinctPlans(t *testing.T) {
	f := setup(t)
	store := f.newStore(t)
	tools := New(store, "calm-mountain")

	docA := "# Plan A\n\nFirst plan content.\n"
	docB := "# Plan B\n\nSecond plan content.\n"

	first, err := tools.Save(f.ctx, SaveArgs{Content: docA})
	if err != nil {
		t.Fatal(err)
	}
	second, err := tools.Save(f.ctx, SaveArgs{Content: docB})
	if err != nil {
		t.Fatal(err)
	}

	if first.Plan.ID == second.Plan.ID {
		t.Fatalf("two sourceless saves collided onto one plan: %s", first.Plan.ID)
	}
	if first.Plan.Title != "Plan A" || second.Plan.Title != "Plan B" {
		t.Fatalf("titles = %q, %q, want each plan's own H1", first.Plan.Title, second.Plan.Title)
	}

	v1, err := store.Versions(f.ctx, domain.PlanID(first.Plan.ID))
	if err != nil {
		t.Fatal(err)
	}
	v2, err := store.Versions(f.ctx, domain.PlanID(second.Plan.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(v1) != 1 || len(v2) != 1 || v1[0].Hash == v2[0].Hash {
		t.Fatalf("each sourceless plan should own a distinct single-version set: v1=%+v v2=%+v", v1, v2)
	}
}

// TestSavePlanIDRevertRepromotes pins the revert re-promotion fix at the MCP
// handler altitude (save v1 -> v2 -> v1 must serve v1 again, not silently
// stay on v2) and, since all three saves are addressed by plan_id, doubles
// as the pin that the plan_id identity seam keeps the supersedes chain
// linear across repeated saves rather than always superseding the first
// version.
func TestSavePlanIDRevertRepromotes(t *testing.T) {
	f := setup(t)
	store := f.newStore(t)
	tools := New(store, "calm-mountain")

	v1, err := tools.Save(f.ctx, SaveArgs{Content: docV1})
	if err != nil {
		t.Fatal(err)
	}
	planID := v1.Plan.ID

	v2, err := tools.Save(f.ctx, SaveArgs{Content: docV2, PlanID: planID})
	if err != nil {
		t.Fatal(err)
	}
	if v2.Plan.ID != planID {
		t.Fatalf("save by plan_id changed identity: %s vs %s", v2.Plan.ID, planID)
	}

	v3, err := tools.Save(f.ctx, SaveArgs{Content: docV1, PlanID: planID})
	if err != nil {
		t.Fatal(err)
	}
	if v3.Content != "" {
		t.Fatalf("save content = %q, want empty (save never echoes supplied bytes)", v3.Content)
	}
	if v3.Hash == v2.Hash {
		t.Fatal("revert save must re-promote v1's hash, not keep v2's")
	}

	// get_review by plan_id (OpenVersion's own path) must now serve v1: the
	// revert re-promoted it to newest rather than silently no-opping.
	review, err := tools.GetReview(f.ctx, GetReviewArgs{PlanID: planID})
	if err != nil {
		t.Fatal(err)
	}
	if review.Content != docV1 {
		t.Fatalf("get_review content = %q, want docV1 (reverted content served)", review.Content)
	}

	versions, err := store.Versions(f.ctx, domain.PlanID(planID))
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 3 {
		t.Fatalf("versions = %+v, want 3 (v1, v2, v1-again)", versions)
	}
	// Ancestry is the log's order, not a field: the middle entry is v2, so
	// the revert appended after v2 rather than re-forking from v1.
	if versions[1].Hash != domain.ContentHash(v2.Hash) {
		t.Fatalf("versions = %+v, want v2 (%s) in the middle", versions, v2.Hash)
	}
	if versions[2].Hash != versions[0].Hash {
		t.Fatalf("v3 hash = %s, want %s (same content as v1, re-promoted)", versions[2].Hash, versions[0].Hash)
	}
}

// TestSaveRefusesEmptyContent pins a BEHAVIOUR CHANGE: before this fix, an
// empty Content registered an empty version with nothing to guard it --
// SaveArgs.Content is a string, so an absent field and an explicitly empty one
// are the same Go zero value, and this is why empty is refused outright rather
// than distinguished from "forgot to pass it." This test must fail against
// the pre-change Save (it would find err == nil, an empty version silently
// registered) and pass once content resolution refuses an empty result.
func TestSaveRefusesEmptyContent(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	_, err := tools.Save(f.ctx, SaveArgs{Content: "", Source: "notion://empty-plan"})
	if err == nil {
		t.Fatal("want a refusal for empty content, got nil (an empty version was registered)")
	}
	for _, want := range []string{"content", "content_from"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q -- both ways to supply bytes belong in the sentence", err.Error(), want)
		}
	}
}

// TestResolveSuppliedContent tables the content-resolution rule directly:
// exactly one of content/content_from, content_from's regular-file
// requirement, the size ceiling, and the empty-content refusal.
func TestResolveSuppliedContent(t *testing.T) {
	dir := t.TempDir()
	staged := filepath.Join(dir, "staged.md")
	if err := os.WriteFile(staged, []byte(docV1), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.md")
	overCeiling := strings.Repeat("a", maxSuppliedContentBytes+1)
	atCeiling := strings.Repeat("a", maxSuppliedContentBytes)

	// oversized is SPARSE -- os.Truncate sets its reported size without
	// writing a single one of those bytes to disk or ever holding them in
	// memory, which is the point: this row exists to prove the ceiling is
	// enforced off os.Stat's Size(), BEFORE os.ReadFile, not merely
	// discovered afterward once the (real, 10MB+) content is already
	// resident. Actually allocating that much content, here or in the
	// resolver, is exactly the anti-pattern this row guards against.
	oversized := filepath.Join(dir, "oversized.md")
	oversizedFile, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := oversizedFile.Truncate(maxSuppliedContentBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := oversizedFile.Close(); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name        string
		content     string
		contentFrom string
		wantContent string // ignored when wantErr is set
		wantErr     string
	}{
		{name: "content alone", content: docV1, wantContent: docV1},
		{name: "content_from alone reads the file", contentFrom: staged, wantContent: docV1},
		{name: "neither is refused", wantErr: "exactly one"},
		{name: "both is refused -- no silent precedence", content: docV1, contentFrom: staged, wantErr: "exactly one"},
		{name: "content_from a directory is refused", contentFrom: dir, wantErr: "not a regular file"},
		{
			// THE CASE THAT DISTINGUISHES THIS FROM localFile's PREDICATE:
			// /dev/null is not a directory (localFile's !info.IsDir() would
			// call it readable), but it is also not a regular file.
			name:        "content_from a character device is refused",
			contentFrom: "/dev/null", wantErr: "not a regular file",
		},
		{name: "content_from a missing file is refused", contentFrom: missing, wantErr: "content_from"},
		{name: "content_from an empty file is refused as empty content", contentFrom: empty, wantErr: "empty content"},
		{name: "content over the size ceiling is refused", content: overCeiling, wantErr: "ceiling"},
		{name: "content exactly at the size ceiling is admitted", content: atCeiling, wantContent: atCeiling},
		{
			// wantErr names content_from AND the path, which only the
			// pre-read size check's own message says -- the post-read
			// len(data) fallback says "content is %d bytes" with neither.
			// A row that merely asserted "ceiling" would pass whether the
			// file was rejected off info.Size() or read in full first and
			// rejected after, which is the exact gap under repair.
			name:        "content_from an oversized file is refused off its stat size, never read into memory",
			contentFrom: oversized, wantErr: fmt.Sprintf("content_from %q is", oversized),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveSuppliedContent(tc.content, tc.contentFrom)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want success", err)
			}
			if string(got) != tc.wantContent {
				t.Fatalf("content = %q, want %q", clipRunes(string(got), 40), clipRunes(tc.wantContent, 40))
			}
		})
	}
}

// TestSaveContentFrom pins content_from's wiring end to end: draftplane reads
// the named file once and registers its bytes, exactly as content does, and
// does not remember the path afterward -- get_review's later readback comes
// from the plan's stored content, not a re-read of the file.
func TestSaveContentFrom(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	staged := filepath.Join(t.TempDir(), "staged.md")
	if err := os.WriteFile(staged, []byte(docV1), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := tools.Save(f.ctx, SaveArgs{ContentFrom: staged, Source: "notion://from-a-path"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan == nil || res.Plan.ID == "" {
		t.Fatalf("plan = %+v, want a created plan", res.Plan)
	}

	review, err := tools.GetReview(f.ctx, GetReviewArgs{PlanID: res.Plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	if review.Content != docV1 {
		t.Fatalf("content = %q, want the bytes read from content_from", review.Content)
	}
}

// TestSaveRefusesBothContentDoors pins the no-silent-precedence rule at save's
// own door -- TestResolveSuppliedContent above pins the rule itself; this
// confirms Save is actually wired to it.
func TestSaveRefusesBothContentDoors(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	_, err := tools.Save(f.ctx, SaveArgs{Content: docV1, ContentFrom: f.path, Source: "notion://both-doors"})
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("err = %v, want a refusal naming exactly one of content/content_from", err)
	}
}

// ---- download ----
//
// download is a READ that produces a file, and every test below is shaped by
// that: what it writes is asserted, and what it RECORDS is asserted to be
// nothing. The pairing matters -- a verb that wrote the right bytes and quietly
// recorded something would pass a content assertion on its own.

// TestDownloadWritesTheCurrentContentAndAnswersWithPathAndHash is the happy
// path, and it checks the three facts the round trip is built on: the bytes on
// disk are the plan's current content EXACTLY (no normalisation, no trailing
// newline added -- reviewResult writes string(s.Content) raw and so must this),
// the hash answered is the hash of those bytes and the same one get_review
// reports for the same session, and the path answered is absolute.
//
// THE PATH ARGUMENT CARRIES SURROUNDING WHITESPACE on purpose, which is the one
// case where "absolute" is not automatic: a string starting with a space is not
// an absolute path, so filepath.Abs would Join it onto this process's working
// directory instead of refusing. resolvedPath trims first; this is what proves
// the trimmed form is both what was opened and what is reported, since the file
// is then found at the untrimmed argument's trimmed name.
func TestDownloadWritesTheCurrentContentAndAnswersWithPathAndHash(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	saved, err := tools.Save(f.ctx, SaveArgs{Content: docV1, Source: "notion://downloadable"})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "worktree.md")

	res, err := tools.Download(f.ctx, DownloadArgs{PlanID: saved.Plan.ID, Path: "  " + out + "\n"})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if res.Path != out {
		t.Errorf("Path = %q, want the trimmed absolute path %q", res.Path, out)
	}
	if !filepath.IsAbs(res.Path) {
		t.Errorf("Path = %q, want an absolute path", res.Path)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading what download wrote: %v", err)
	}
	if string(got) != docV1 {
		t.Errorf("file holds %q, want the plan's content verbatim", got)
	}
	if res.Hash != string(domain.HashContent(got)) {
		t.Errorf("Hash = %s, want the hash of the bytes actually written (%s)", res.Hash, domain.HashContent(got))
	}
	if res.Hash != saved.Hash {
		t.Errorf("Hash = %s, want the same hash the review projection reports for this version (%s)", res.Hash, saved.Hash)
	}
	// Never more permissive than the 0o644 the create asks for. Asserted as a
	// bound rather than as equality because a caller's umask can only take bits
	// away, and a test that demanded exactly 0644 would fail under umask 077 for
	// a reason that is not a defect.
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&^0o644 != 0 {
		t.Errorf("mode = %v, want a regular file no more permissive than 0644", info.Mode())
	}
}

// TestDownloadRefusesAPlanDraftplaneReadsLive pins the gate for a session
// draftplane holds no bytes for: a readable local file addressed by source,
// which draftplane reads live. The refusal names the plan's own path (s.Path).
//
// It also asserts that nothing was written at the path the caller named: a gate
// that refuses after creating the file would leave the agent an empty document
// and a refusal, and no assertion on the message alone would see it.
func TestDownloadRefusesAPlanDraftplaneReadsLive(t *testing.T) {
	f := setup(t)
	local := f.newStore(t)
	s, err := session.Open(f.ctx, local, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	tools := New(local, "calm-mountain")
	out := filepath.Join(t.TempDir(), "worktree.md")

	_, err = tools.Download(f.ctx, DownloadArgs{Source: f.path, Path: out})
	if err == nil {
		t.Fatal("download succeeded, want a refusal -- draftplane holds no content of its own for this plan")
	}
	if want := fmt.Sprintf(downloadReadsLiveMsg, f.path); err.Error() != want {
		t.Errorf("err = %q, want the refusal naming the plan's own path:\n%q", err, want)
	}
	if _, statErr := os.Stat(out); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("os.Stat(%s) = %v, want the path untouched -- a refusal must not leave a file behind", out, statErr)
	}
}

// TestNeitherDoorHandsOverAFileBackedPlanReachedByID pins the door the first
// gate cannot see, at BOTH verbs. A plan whose SourceHint names a readable
// file on this machine, addressed by plan_id, goes OpenByID -> OpenVersion and
// arrives with FromSnapshot TRUE -- so !FromSnapshot waves it through and only
// the second question, asked of the plan's SourceHint, holds the content back:
// download refuses, reviewResult omits GetReviewResult.Content.
//
// ⚠️ BOTH get_review AND download MUST AGREE ON THIS, AND A READER MEETING
// THIS FILE COLD COULD REASONABLY EXPECT EITHER BEHAVIOUR: that download
// SERVES this plan, from the latest registered version, on the argument that
// get_review answers that same version at that same door -- the two doors
// cannot be allowed to disagree -- or that download REFUSES while get_review
// goes on answering, because a file reads as authoritative and invites
// editing where an inlined string does not. Both doors ask FromSnapshot and
// the readable-source question, so the two must agree, and this test is what
// makes deleting either half of that agreement redden. reviewResult's doc
// owns the sentence this test pins.
//
// THE FILE IS STILL EDITED BETWEEN THE CREATE AND THE CALL, which is what
// makes the outcome MEAN something: at this point the registered bytes and
// the document have genuinely come apart, and that gap is precisely the file
// download would have written and the string get_review would have inlined.
// A fixture where the two agreed would hold content back for a reason nobody
// could see.
//
// THE REMEDY IS ASSERTED AS THE THING THAT SURVIVES, and it is PlanInfo's
// SourceHint rather than the content field: the caller is not stranded, it is
// pointed at the document -- whose bytes are asserted to be the ones the
// projection does NOT carry. Threads, placements and approval are untouched by
// this rule, which is why get_review is still asserted to SUCCEED rather than
// refuse; only the readback is gone.
func TestNeitherDoorHandsOverAFileBackedPlanReachedByID(t *testing.T) {
	f := setup(t)
	local := f.newStore(t)
	s, err := session.Open(f.ctx, local, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	tools := New(local, "calm-mountain")

	// The human edits their own file after the plan was registered. Nothing
	// tells draftplane, which is the ordinary state of a file-backed plan.
	if err := os.WriteFile(f.path, []byte(docV2), 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "worktree.md")
	_, err = tools.Download(f.ctx, DownloadArgs{PlanID: string(s.Plan.ID), Path: out})
	if err == nil {
		t.Fatal("download by plan_id succeeded, want a refusal -- this plan's content is a file on this machine, " +
			"and the copy download would write is the last REGISTERED version rather than what that file says")
	}
	if want := fmt.Sprintf(downloadFileBackedMsg, f.path); err.Error() != want {
		t.Errorf("err = %q, want the refusal coaching to the plan's own file:\n%q", err, want)
	}
	if _, statErr := os.Stat(out); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("os.Stat(%s) = %v, want the path untouched -- a refusal must not leave a file behind", out, statErr)
	}

	review, err := tools.GetReview(f.ctx, GetReviewArgs{PlanID: string(s.Plan.ID)})
	if err != nil {
		t.Fatalf("get_review on the plan download just refused: %v -- this rule omits a field, it does not refuse "+
			"the read: the threads are what the call was for and they still answer", err)
	}
	onDisk, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) == docV1 {
		t.Fatal("the file still says what was registered; this fixture cannot tell a held-back readback from " +
			"one that merely agreed with the document")
	}
	if review.Content != "" {
		t.Errorf("get_review answers %q while the file says %q; want NO content field -- what draftplane holds is "+
			"the version registered at the last review fact, and handing that back unmarked is two answers for "+
			"one plan", review.Content, onDisk)
	}
	// PlanInfo.SourceHint IS `json:"source"`, so what an agent following the
	// coaching reads is plan.source. The Go field and the JSON field differ in
	// name, which is exactly how a description once came to name a key that is
	// not in the JSON at all -- so the assertion names both.
	if review.Plan == nil || review.Plan.SourceHint != f.path {
		t.Errorf("plan = %+v, want SourceHint (plan.source in the JSON) = %s -- the path IS the remedy the "+
			"omission leaves, and a caller handed neither the bytes nor the path would be stranded",
			review.Plan, f.path)
	}
}

// TestBothDoorsAnswerWhenAFileBackedPlansFileIsGone is the row that proves the
// predicate asks "is this file readable RIGHT NOW" rather than "does this plan
// name a source", and it is the one place either door's rule bends back the
// other way.
//
// THE PLAN IS THE PREVIOUS TEST'S, MINUS THE FILE. Same shape -- SourceHint
// naming a local path, reached by plan_id -- so the ONLY difference between
// "content held back" and "content served" is whether os.Stat succeeds
// inside localFile. That is what makes this a test of the predicate rather than
// of a special case: nothing here knows about deleted files.
//
// SERVING IS THE RECOVERY, not a leak in the gate. The registered version is now
// the ONLY copy of this plan's document that exists, and the coaching the two
// rules otherwise lean on -- "read the file yourself", "plan.source names the
// file" -- points at nothing. An agent told to go and read a path that is not
// there has been stranded by a rule meant to protect it.
//
// BOTH DOORS ARE ASSERTED IN ONE TEST BECAUSE THE INVARIANT IS THE SUBJECT: what
// get_review inlines is exactly what download writes. This row is where a
// half-applied change would show first -- a gate hard-coded to "has a source"
// rather than asking localFile would refuse here at one door and answer at the
// other -- so the two assertions are deliberately the same fixture and the same
// bytes.
func TestBothDoorsAnswerWhenAFileBackedPlansFileIsGone(t *testing.T) {
	f := setup(t)
	local := f.newStore(t)
	s, err := session.Open(f.ctx, local, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	tools := New(local, "calm-mountain")

	// The human's document is gone -- deleted, on a volume no longer mounted,
	// renamed out from under the plan. draftplane is not told; it finds out by
	// stat'ing the hint it recorded.
	if err := os.Remove(f.path); err != nil {
		t.Fatal(err)
	}

	review, err := tools.GetReview(f.ctx, GetReviewArgs{PlanID: string(s.Plan.ID)})
	if err != nil {
		t.Fatalf("get_review: %v", err)
	}
	if review.Content != docV1 {
		t.Errorf("get_review answers %q, want the registered version %q -- with the document gone this is the "+
			"only copy left, and coaching to a path that is not there strands the caller",
			clipRunes(review.Content, 40), clipRunes(docV1, 40))
	}
	if review.Plan == nil || review.Plan.SourceHint != f.path {
		t.Errorf("plan = %+v, want the hint still naming %s -- a missing file is not a plan that lost its "+
			"source, and nothing about this read rewrites the record", review.Plan, f.path)
	}

	out := filepath.Join(t.TempDir(), "worktree.md")
	dl, err := tools.Download(f.ctx, DownloadArgs{PlanID: string(s.Plan.ID), Path: out})
	if err != nil {
		t.Fatalf("download: %v -- the two doors must agree on this row as on every other, and get_review just "+
			"answered content for this plan", err)
	}
	got, err := os.ReadFile(dl.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != review.Content {
		t.Errorf("download wrote %q and get_review answered %q -- the invariant is that these are the same bytes",
			clipRunes(string(got), 40), clipRunes(review.Content, 40))
	}
}

// TestResolveComputesSourceFaultWhenALocalFileBackedPlansFileIsGone is the
// field report itself, read off resolve directly rather than through a verb:
// TestBothDoorsAnswerWhenAFileBackedPlansFileIsGone above already proves
// get_review and download keep answering the registered snapshot, which is
// correct -- there is no other copy left. What that test cannot see is
// whether anything on the session says so, and until this fix nothing did.
//
// BOTH OF resolve's ID-SHAPED BRANCHES ARE RUN, over the identical fixture,
// because both funnel to the identical session.OpenByID call (openByID) and
// must agree: fixing only one would leave the other silently answering a
// snapshot with no fault attached, indistinguishable from a healthy plan.
func TestResolveComputesSourceFaultWhenALocalFileBackedPlansFileIsGone(t *testing.T) {
	for _, tc := range []struct {
		name string
		byID bool
	}{
		{"by plan_id directly", true},
		{"by a source ResolvePlan resolves to the plan id", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			local := f.newStore(t)
			s, err := session.Open(f.ctx, local, f.path)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
				t.Fatal(err)
			}
			tools := New(local, "calm-mountain")

			// The human's document is gone, exactly as in the test above.
			if err := os.Remove(f.path); err != nil {
				t.Fatal(err)
			}

			source, planID := "", string(s.Plan.ID)
			if !tc.byID {
				source, planID = f.path, ""
			}
			got, err := tools.resolve(f.ctx, source, planID)
			if err != nil {
				t.Fatalf("resolve: %v -- OpenByID's OpenVersion fallback must still answer the registered "+
					"snapshot; only SourceFault should change", err)
			}
			if got.SourceFault == nil {
				t.Fatal("SourceFault = nil, want a fault -- the plan's own file is gone and this is the " +
					"MCP surface's only way to say so")
			}
			if got.SourceFault.State != session.SourceFileGone {
				t.Errorf("SourceFault.State = %v, want %v", got.SourceFault.State, session.SourceFileGone)
			}
			if got.SourceFault.PlanID != s.Plan.ID {
				t.Errorf("SourceFault.PlanID = %s, want %s", got.SourceFault.PlanID, s.Plan.ID)
			}
			if got.SourceFault.Path != f.path {
				t.Errorf("SourceFault.Path = %q, want %q", got.SourceFault.Path, f.path)
			}
		})
	}
}

// TestResolveComputesNoSourceFaultWhenThereIsNothingToReport covers the three
// negatives the fault check above must not fire for: a healthy file-backed
// plan (the file is exactly where it was), a sourceless plan (no file ever
// existed to go missing), and a plan whose SourceHint is itself a URL
// source (client.URLSource) rather than a path. The fault the third must
// not report is caught by sourceFileFault's own URL-source arm: see that
// function's doc comment for why the taxonomy question belongs there and not
// at this door.
func TestResolveComputesNoSourceFaultWhenThereIsNothingToReport(t *testing.T) {
	t.Run("healthy local file-backed plan", func(t *testing.T) {
		f := setup(t)
		local := f.newStore(t)
		s, err := session.Open(f.ctx, local, f.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
			t.Fatal(err)
		}
		tools := New(local, "calm-mountain")

		got, err := tools.resolve(f.ctx, "", string(s.Plan.ID))
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got.SourceFault != nil {
			t.Errorf("SourceFault = %+v, want nil -- the file is right where it was", got.SourceFault)
		}
	})

	t.Run("sourceless plan", func(t *testing.T) {
		f := setup(t)
		tools := New(f.newStore(t), "calm-mountain")
		res, err := tools.Save(f.ctx, SaveArgs{Content: docV1})
		if err != nil {
			t.Fatal(err)
		}

		got, err := tools.resolve(f.ctx, "", res.Plan.ID)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got.SourceFault != nil {
			t.Errorf("SourceFault = %+v, want nil -- a plan that never had a file has no file to be missing",
				got.SourceFault)
		}
	})

	// A plan whose SourceHint is itself a URL source (client.URLSource) rather
	// than a path: exactly what mcptools.Save mints for a source draftplane
	// cannot read (Save's own doc comment). session.Open still tries
	// os.ReadFile on the literal URL and gets ENOENT, which without
	// sourceFileFault's URL-source arm classifies as SourceFileGone --
	// reporting a real plan's notion:// or https:// source as a missing local
	// file.
	t.Run("plan with a URL source", func(t *testing.T) {
		f := setup(t)
		local := f.newStore(t)
		plan, err := local.CreatePlan(f.ctx, "Notion Plan", "notion://url-source-test", "", []byte(docV1))
		if err != nil {
			t.Fatalf("CreatePlan: %v", err)
		}
		tools := New(local, "calm-mountain")

		got, err := tools.resolve(f.ctx, "", string(plan.ID))
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got.SourceFault != nil {
			t.Errorf("SourceFault = %+v, want nil -- a URL source is never a file on this machine "+
				"to lose", got.SourceFault)
		}
	})
}

// TestGetReviewReportsSourceFaultForADeletedFile proves that once
// resolve latches Session.SourceFault (the tests above), get_review's MCP
// result has to project it -- a machine-readable state PLUS a state-specific
// sentence in Guidance -- while still answering with this plan's content and
// threads exactly as TestBothDoorsAnswerWhenAFileBackedPlansFileIsGone already
// proves it does. This test does not re-prove that invariant; it seeds a
// thread specifically so it can prove THREADS survive the fault the same way
// (that test's fixture never has any), and checks what got added on top.
func TestGetReviewReportsSourceFaultForADeletedFile(t *testing.T) {
	f := setup(t)
	local := f.newStore(t)
	s, err := session.Open(f.ctx, local, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	tools := New(local, "calm-mountain")

	// Seeded BEFORE the file goes missing, so the assertion below is "this
	// exact thread survived" rather than "an empty slice stayed empty".
	th, err := tools.Comment(f.ctx, CommentArgs{
		PlanID: string(s.Plan.ID), Quote: "token bucket with a burst capacity of fifty requests", Body: "why fifty?",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(f.path); err != nil {
		t.Fatal(err)
	}

	review, err := tools.GetReview(f.ctx, GetReviewArgs{PlanID: string(s.Plan.ID)})
	if err != nil {
		t.Fatalf("get_review: %v", err)
	}
	if review.SourceFault != session.SourceFileGone.String() {
		t.Errorf("SourceFault = %q, want %q", review.SourceFault, session.SourceFileGone.String())
	}
	if !strings.Contains(review.Guidance, "gone or has moved") {
		t.Errorf("Guidance = %q, want the gone-specific sentence -- the one state this wording is true of",
			review.Guidance)
	}
	if strings.Contains(strings.ToLower(review.Guidance), "save") {
		t.Errorf("Guidance = %q, names save -- it must not: save accepts the write and leaves SourceHint "+
			"pointing at the dead path, which is not a cure", review.Guidance)
	}
	if review.Content != docV1 {
		t.Errorf("Content = %q, want the registered version %q, unmoved by the new fault signal",
			clipRunes(review.Content, 40), clipRunes(docV1, 40))
	}
	if len(review.Threads) != 1 || review.Threads[0].ID != th.ThreadID {
		t.Fatalf("Threads = %+v, want the one comment made before the file went missing, unmoved by the new "+
			"fault signal", review.Threads)
	}
	// gone is one of the two states of four (unreadable is the other) whose
	// content is not withheld, and the response must say so explicitly rather
	// than leave it to be inferred from the state name alone.
	if !strings.Contains(review.Guidance, "content above is this plan's last registered version, unaffected by this state") {
		t.Errorf("Guidance = %q, want it to say content was included for gone specifically -- nothing may "+
			"leave that to be inferred", review.Guidance)
	}
}

// TestGetReviewNoSourceFaultForAHealthyPlan is
// TestGetReviewReportsSourceFaultForADeletedFile's negative: a plan whose file
// was never touched carries neither field, on the same MCP projection a
// caller cannot otherwise tell apart from a plan whose file has a fault.
func TestGetReviewNoSourceFaultForAHealthyPlan(t *testing.T) {
	f := setup(t)
	local := f.newStore(t)
	s, err := session.Open(f.ctx, local, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	tools := New(local, "calm-mountain")

	review, err := tools.GetReview(f.ctx, GetReviewArgs{PlanID: string(s.Plan.ID)})
	if err != nil {
		t.Fatalf("get_review: %v", err)
	}
	if review.SourceFault != "" {
		t.Errorf("SourceFault = %q, want \"\" -- the file is right where it was", review.SourceFault)
	}
	if review.Guidance != "" {
		t.Errorf("Guidance = %q, want \"\" -- nothing about this plan's file needs saying", review.Guidance)
	}
}

// TestSourceFaultGuidanceIsStateSpecific drives sourceFaultGuidance directly
// with all four states, rather than through a real fault: SourceFileClaimed and
// SourceFileReleased both require session.Open to have actually SUCCEEDED at
// s.Path and resolved to the wrong identity (sourceFileFault's own arms), which
// needs a second plan racing the first for one exact path -- plumbing this
// test has no need of, because the requirement under test is about the TEXT,
// not about how a real one gets constructed: what is said about
// SourceFileGone must be false of the other three.
func TestSourceFaultGuidanceIsStateSpecific(t *testing.T) {
	// contentIncluded is the content gate's OWN TABLE, NOT the one-vs-three
	// split the four-state text might suggest: gone and unreadable are the two
	// states whose bytes are not withheld by the content gate, because neither
	// has a live document to defer to; released and claimed correctly
	// withhold, because refuseSaveForReadableSource's real predicate finds
	// their file genuinely readable -- it has simply stopped being this
	// plan's document. Before the open-for-read fix, that predicate decided
	// "readable" with os.Stat alone, which also passes a permission-denied
	// file, so unreadable was measured (chmod 0000 fixture) withholding right
	// alongside released and claimed -- a lopsided 1-of-4 split corrected to
	// this principled 2-of-4 one.
	for _, tc := range []struct {
		state           session.SourceFileState
		wantSub         string
		contentIncluded bool
	}{
		{session.SourceFileGone, "gone or has moved", true},
		{session.SourceFileUnreadable, "can't read", true},
		{session.SourceFileReleased, "no longer resolves to this plan", false},
		{session.SourceFileClaimed, "already following this file", false},
	} {
		t.Run(tc.state.String(), func(t *testing.T) {
			f := &session.SourceFileError{PlanID: "l_1", Path: "/tmp/plan.md", State: tc.state, Err: errors.New("permission denied")}
			got := sourceFaultGuidance(f, tc.contentIncluded, false)
			if got == "" {
				t.Fatal("sourceFaultGuidance returned \"\" for a non-nil fault")
			}
			if !strings.Contains(got, tc.wantSub) {
				t.Errorf("guidance = %q, want it to contain %q", got, tc.wantSub)
			}
			if strings.Contains(strings.ToLower(got), "save") {
				t.Errorf("guidance = %q, names save -- no state may do that", got)
			}
			// sourceFaultText's "gone or has moved" claim is
			// true of exactly one state. Assert the negative directly for the
			// three it is false of, rather than trusting wantSub's presence to
			// also prove its absence.
			if tc.state != session.SourceFileGone && strings.Contains(got, "nothing on disk to read") {
				t.Errorf("guidance = %q, wrongly claims the file is gone -- true of exactly one state", got)
			}
			// released and claimed must never call the path
			// "this plan's source file" -- that framing is self-contradictory,
			// asserting the file is this plan's own in the same breath as
			// denying it resolves to this plan (or resolves to a different
			// one). gone and unreadable are unaffected -- for them the phrase
			// is true, since only the file itself is missing or unreadable
			// there.
			isReleasedOrClaimed := tc.state == session.SourceFileReleased || tc.state == session.SourceFileClaimed
			if isReleasedOrClaimed && strings.Contains(got, "this plan's source file") {
				t.Errorf("guidance = %q, still frames the path as this plan's own file while denying it "+
					"resolves to this plan -- a self-contradiction no state's wording may carry", got)
			}
			// "no single gesture in draftplane resolves it today" is false of
			// both states: claimed's fix IS a single gesture (repoint one
			// plan, or remove the wrong one), and released's usual fix is a
			// refresh, which is one too.
			if isReleasedOrClaimed && strings.Contains(got, "no single gesture") {
				t.Errorf("guidance = %q, still claims no single gesture resolves this -- false of both states "+
					"now that each names its real remedy", got)
			}
			// The content situation is stated explicitly and must match
			// what the caller says actually happened, in EITHER direction --
			// a test that only checked the withheld branch would pass if gone
			// silently started claiming withholding too.
			wantNote := "content above is empty for this call"
			if tc.contentIncluded {
				wantNote = "content above is this plan's last registered version, unaffected by this state"
			}
			if !strings.Contains(got, wantNote) {
				t.Errorf("guidance = %q, want it to contain %q -- nothing may leave the content situation "+
					"to be inferred from the state alone", got, wantNote)
			}
		})
	}
	if got := sourceFaultGuidance(nil, true, false); got != "" {
		t.Errorf("sourceFaultGuidance(nil, true, false) = %q, want \"\" -- nothing to report", got)
	}
}

// TestSourceFaultContentNoteMatchesWhatActuallyHappened drives
// sourceFaultGuidance's content-inclusion clause directly, isolating the ONE
// question that matters: does the sentence say what the
// caller told it happened, for BOTH directions, independent of which state
// the caller passed. The state-mapping table above already proves each
// state's headline and content note pair correctly against the real table;
// this test proves the content note itself is driven by the boolean alone,
// so a future state added to the switch cannot forget to wire it up
// correctly and still pass by accident.
func TestSourceFaultContentNoteMatchesWhatActuallyHappened(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		contentIncluded, empty bool
		wantNote               string
	}{
		{"included, real content", true, false,
			"content above is this plan's last registered version, unaffected by this state"},
		{"included, genuinely empty", true, true,
			"content above is genuinely empty -- this plan's last registered version is zero bytes"},
		{"withheld", false, false, "content above is empty for this call: this plan's document cannot " +
			"currently be read as its own file, and there is no other copy on hand to serve instead"},
		// withheld must win regardless of empty: there is nothing here to call
		// "genuinely empty" when the gate never let the actual bytes through in
		// the first place -- included false is the discriminator, not a
		// combination of the two.
		{"withheld, empty is irrelevant here", false, true, "content above is empty for this call: this " +
			"plan's document cannot currently be read as its own file, and there is no other copy on hand " +
			"to serve instead"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sourceFaultContentNote(tc.contentIncluded, tc.empty); got != tc.wantNote {
				t.Errorf("sourceFaultContentNote(%v, %v) = %q, want %q", tc.contentIncluded, tc.empty, got, tc.wantNote)
			}
		})
	}
}

// TestGetReviewIncludesContentForAnUnreadableFile pins the open-for-read fix
// end to end through the real pipeline rather than through sourceFaultGuidance's
// fixtures above: a chmod 0000 file classifies as SourceFileUnreadable
// (session's sourceFileFault -- a read failure that is not fs.ErrNotExist).
// Before the open-for-read fix, the file's own os.Stat succeeding while
// os.ReadFile failed made refuseSaveForReadableSource (via localFile) call
// the source "readable" and withhold res.Content -- an empirical finding,
// measured rather than assumed. The open-for-read fix added a real
// open-for-read after localFile's existing Stat+IsDir gate, so this exact
// fixture now classifies as readable no longer, and the registered content
// comes back -- the same recovery gone already had, extended to the other
// state with no live document to defer to. released and claimed are NOT
// affected: their file is genuinely readable, just not this plan's document
// any more, and TestGetReviewStillWithholdsContentForReleasedAndClaimed
// below is the negative that would otherwise leave this unstated.
//
// A CHMOD'D FILE, NOT A DIRECTORY. app/'s own unreadable fixtures use a
// directory instead (see app/sourcefault_test.go's
// TestTheMissingFilePanelOpensOnTheFaultTheSessionLatched: "EISDIR is
// nobody's privilege to bypass"), which is the right, portable choice for
// classifying SourceFileState -- but it does NOT reproduce this finding:
// localFile's IsDir guard excludes directories on its own, before the
// open-for-read fix's own open ever runs, so a directory-shaped "unreadable"
// fixture leaves refuseSaveForReadableSource unrefused and content INCLUDED
// on both sides of that change -- it cannot tell the fix from its absence.
// Reproducing the case this test exists for requires the exact fixture that
// finding was measured with.
func TestGetReviewIncludesContentForAnUnreadableFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read a 0000 file, so this fixture would prove nothing")
	}
	f := setup(t)
	local := f.newStore(t)
	s, err := session.Open(f.ctx, local, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	tools := New(local, "calm-mountain")

	if err := os.Chmod(f.path, 0o000); err != nil {
		t.Fatal(err)
	}
	// Restored, not left at 0000: t.TempDir()'s own cleanup needs to remove
	// this file afterward.
	t.Cleanup(func() { _ = os.Chmod(f.path, 0o644) })

	review, err := tools.GetReview(f.ctx, GetReviewArgs{PlanID: string(s.Plan.ID)})
	if err != nil {
		t.Fatalf("get_review: %v", err)
	}
	if review.SourceFault != session.SourceFileUnreadable.String() {
		t.Fatalf("SourceFault = %q, want %q -- the fixture must actually classify as unreadable for this "+
			"test to mean anything", review.SourceFault, session.SourceFileUnreadable.String())
	}
	if review.Content != docV1 {
		t.Errorf("Content = %q, want %q -- this state has no live document to defer to, same as "+
			"gone, so the registered version comes back rather than being withheld",
			clipRunes(review.Content, 40), clipRunes(docV1, 40))
	}
	if !strings.Contains(review.Guidance, "content above is this plan's last registered version, unaffected by this state") {
		t.Errorf("Guidance = %q, want it to say content was included -- no state "+
			"may leave that to be inferred", review.Guidance)
	}
	if strings.Contains(review.Guidance, "content above is empty for this call") {
		t.Errorf("Guidance = %q, wrongly claims content was withheld for this state", review.Guidance)
	}
	// THE PATH IS NAMED ONCE, not twice. f.Err here is a REAL
	// *fs.PathError (os.Chmod(0o000) against a real file), whose own Error()
	// already renders "open <path>: permission denied" -- before readReason
	// existed, sourceFaultGuidance's unreadable arm put %s (the bare path)
	// and %v (that whole rendered PathError) beside each other in one
	// parenthetical, printing the path twice.
	if n := strings.Count(review.Guidance, f.path); n != 1 {
		t.Errorf("Guidance = %q, contains the path %d times, want exactly 1 -- the *fs.PathError beside it "+
			"already names it once on its own", review.Guidance, n)
	}
}

// TestDownloadServesTheRegisteredSnapshotForAnUnreadableFile pins the
// open-for-read fix's ripple onto this caller, measured rather than assumed:
// that fix touches localFile, which refuseSaveForReadableSource alone calls,
// which in turn gates FIVE callers -- reviewResult (pinned above by
// TestGetReviewIncludesContentForAnUnreadableFile) and four others, of which
// this is one. Before the fix, os.Stat succeeding on a chmod 0000 file made
// refuseSaveForReadableSource call it "readable," and Download refused with
// downloadFileBackedMsg exactly as it does for a genuinely readable file. The
// fix's real open call now sees SourceFileUnreadable has no live document to
// defer to -- refuseSaveForReadableSource stops refusing -- and Download falls
// through to writing the plan's own last-registered snapshot.
//
// ⚠️ THIS PINS WHAT THE CODE DOES, NOT A JUDGMENT THAT IT SHOULD: the same
// 2-of-4 split reviewResult's own doc comment claims (gone and unreadable
// recover; released and claimed still withhold) now holds for this door too,
// because both ask the identical predicate of the identical field. Whether
// serving a stale copy here is the right call for an agent that asked to
// download a file it cannot itself read is a product question this test does
// not answer.
//
// THE FILE IS EDITED TO docV2 BEFORE THE CHMOD, and restored (not merely
// re-chmod'd) in cleanup so the test can read it back afterward -- the same
// technique TestNeitherDoorHandsOverAFileBackedPlanReachedByID uses for the
// readable case -- so "served docV1" is provably STALE rather than a lucky
// coincidence with a disk this test could not otherwise inspect while it was
// unreadable.
//
// THE ASSERTIONS BELOW COVER MORE THAN THE BYTES ALONE: it is not enough that
// the bytes are served silently -- the same stale content get_review reports
// source_fault beside must not have download report nothing beside it. The
// assertions below close that gap for this one state; the sibling "gone"
// test below covers the other.
func TestDownloadServesTheRegisteredSnapshotForAnUnreadableFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read a 0000 file, so this fixture would prove nothing")
	}
	f := setup(t)
	local := f.newStore(t)
	s, err := session.Open(f.ctx, local, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	tools := New(local, "calm-mountain")

	// The human edits their own file after the plan was registered, same
	// setup TestNeitherDoorHandsOverAFileBackedPlanReachedByID uses for the
	// readable case, so the on-disk bytes and the registered snapshot have
	// genuinely come apart before the file goes unreadable.
	if err := os.WriteFile(f.path, []byte(docV2), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.path, 0o000); err != nil {
		t.Fatal(err)
	}
	// Restored, not left at 0000: t.TempDir()'s own cleanup needs to remove
	// this file afterward, and this test itself reads it back once restored.
	t.Cleanup(func() { _ = os.Chmod(f.path, 0o644) })

	out := filepath.Join(t.TempDir(), "worktree.md")
	dl, err := tools.Download(f.ctx, DownloadArgs{PlanID: string(s.Plan.ID), Path: out})
	if err != nil {
		t.Fatalf("download: %v -- this file classifies as unreadable rather than readable, so "+
			"refuseSaveForReadableSource no longer refuses it here, same as get_review's own content gate", err)
	}
	got, err := os.ReadFile(dl.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != docV1 {
		t.Errorf("download wrote %q, want the registered snapshot %q -- an unreadable file has no live "+
			"document to defer to, the same recovery a gone file already gets",
			clipRunes(string(got), 40), clipRunes(docV1, 40))
	}
	if dl.Hash != string(s.Hash) {
		t.Errorf("Hash = %q, want %q -- the version this call wrote out", dl.Hash, string(s.Hash))
	}
	if dl.SourceFault != session.SourceFileUnreadable.String() {
		t.Errorf("SourceFault = %q, want %q -- this call succeeded against a file it could not read, and that "+
			"is exactly what get_review would say beside the identical stale bytes", dl.SourceFault, session.SourceFileUnreadable.String())
	}
	if !strings.Contains(dl.Guidance, "LAST REGISTERED version") {
		t.Errorf("Guidance = %q, want it to say the file just written is this plan's last registered version, "+
			"not a copy of the document -- the whole reason this field exists is a file that outlives the call", dl.Guidance)
	}
	if !strings.Contains(strings.ToLower(dl.Guidance), "tell your human") {
		t.Errorf("Guidance = %q, want it to tell the agent to relay the staleness to a human -- a bare field "+
			"can die silently in an agent that does not branch on one it has not seen before", dl.Guidance)
	}
	// THE PATH IS NAMED ONCE, not twice, here too. f.Err here
	// is a REAL *fs.PathError (os.Chmod(0o000) against a real file), whose
	// own Error() already names f.path -- before readReason reached this
	// third site, downloadSourceFaultGuidance's unreadable arm put %s (the
	// bare path) and %v (that whole rendered error) beside each other in one
	// parenthetical, printing the path twice, the identical bug already
	// fixed in sourceFaultGuidance.
	if n := strings.Count(dl.Guidance, f.path); n != 1 {
		t.Errorf("Guidance = %q, contains the path %d times, want exactly 1 -- the *fs.PathError beside it "+
			"already names it once on its own", dl.Guidance, n)
	}

	// Prove staleness rather than assume it: restore permissions and read the
	// real document back. If this equalled docV1 the fixture never diverged
	// and the assertion above could not tell a stale readback from one that
	// merely agreed with the document.
	if err := os.Chmod(f.path, 0o644); err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) == docV1 {
		t.Fatal("the file still says what was registered; this fixture cannot tell a stale readback from one " +
			"that merely agreed with the document")
	}
}

// TestDownloadServesTheRegisteredSnapshotForAGoneFile is
// TestDownloadServesTheRegisteredSnapshotForAnUnreadableFile's sibling for the
// other state that falls through to the write -- gone had no dedicated
// Download test of its own before this fix, only TestDownloadServesASourcelessPlan
// (a plan that never had a file at all, a different population: FromSnapshot
// true by construction rather than by a fault). This is the one that proves
// gone specifically, the same way TestResolveComputesSourceFaultWhenALocalFileBackedPlansFileIsGone
// and TestGetReviewReportsSourceFaultForADeletedFile prove it for resolve and
// get_review.
func TestDownloadServesTheRegisteredSnapshotForAGoneFile(t *testing.T) {
	f := setup(t)
	local := f.newStore(t)
	s, err := session.Open(f.ctx, local, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	tools := New(local, "calm-mountain")

	if err := os.Remove(f.path); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "worktree.md")
	dl, err := tools.Download(f.ctx, DownloadArgs{PlanID: string(s.Plan.ID), Path: out})
	if err != nil {
		t.Fatalf("download: %v -- a gone file has no live document to defer to, so this "+
			"call falls through to the plan's last registered snapshot rather than refusing", err)
	}
	got, err := os.ReadFile(dl.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != docV1 {
		t.Errorf("download wrote %q, want the registered snapshot %q", clipRunes(string(got), 40), clipRunes(docV1, 40))
	}
	if dl.Hash != string(s.Hash) {
		t.Errorf("Hash = %q, want %q -- the version this call wrote out", dl.Hash, string(s.Hash))
	}
	if dl.SourceFault != session.SourceFileGone.String() {
		t.Errorf("SourceFault = %q, want %q", dl.SourceFault, session.SourceFileGone.String())
	}
	if !strings.Contains(dl.Guidance, "LAST REGISTERED version") {
		t.Errorf("Guidance = %q, want it to say the file just written is this plan's last registered version, "+
			"not a copy of the document", dl.Guidance)
	}
	if !strings.Contains(strings.ToLower(dl.Guidance), "tell your human") {
		t.Errorf("Guidance = %q, want it to tell the agent to relay this to a human", dl.Guidance)
	}
	if !strings.Contains(dl.Guidance, "gone or has moved") {
		t.Errorf("Guidance = %q, want the gone-specific wording -- it must not be the unreadable sentence "+
			"misapplied to a file that is not there at all", dl.Guidance)
	}
}

// TestDownloadStillRefusesReleasedAndClaimed pins this verb's own boundary,
// stated as a test rather than only in DownloadResult's doc comment: of
// the four states, only two (gone, unreadable) fall through to the write --
// released and claimed still hit the pre-existing gate
// (refuseSaveForReadableSource, of the plan's SourceHint) unchanged, because in
// both the file IS readable and belongs to something else. This is the negative
// TestDownloadServesTheRegisteredSnapshotForAGoneFile and
// TestDownloadServesTheRegisteredSnapshotForAnUnreadableFile need for their own
// "only two of four" claim to mean anything: without it, nothing here would
// distinguish "the other two also succeed" from "the other two were never
// tried."
//
// claimed IS BUILT AS A REAL COLLISION, the identical settled fixture
// TestResolveBySourceAnswersTheCurrentFileOwnerForAClaimedPath uses: plan A
// owns f.path first, plan B is created second and pointed at the same path, and B's
// own plan_id is what resolves claimed -- no race required, because B's own
// stored SourceHint genuinely disagrees with who f.path currently belongs to.
//
// released IS NOT BUILT THE SAME WAY, and that asymmetry is recorded rather
// than papered over: released cannot be reached on a settled filesystem, for a
// structural reason -- applyMCPSourceFault classifies released only when a FRESH open of the plan's
// own SourceHint resolves to no plan at all, and a plan addressed by its own
// current, unchanged SourceHint is exactly the plan that fresh open finds
// (TestResolveBySourceInvitesACreateForAReleasedPath's own doc comment: this
// state "cannot arise here from a settled state alone," and needs a frozen
// snapshot raced against a later SetSourceHint to construct even for a
// by-SOURCE open, which download never takes for a plan reached by plan_id).
// What this test asserts for released instead is the one thing that
// generalizes: refuseSaveForReadableSource -- Download's actual gate, and the
// ONE predicate this whole boundary rests on -- refuses a released session's
// SourceHint exactly as it refuses claimed's, because the predicate asks
// nothing about SourceFault at all, only whether the named file can be read.
// The fixture is the real race TestResolveBySourceInvitesACreateForAReleasedPath
// already built and proved classifies as released; this test asks Download's
// own gate the identical question of the identical session, rather than
// re-deriving a second released fixture that would only repeat that proof.
func TestDownloadStillRefusesReleasedAndClaimed(t *testing.T) {
	t.Run("claimed", func(t *testing.T) {
		f := setup(t)
		local := f.newStore(t)
		a, err := session.Open(f.ctx, local, f.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Create(f.ctx, "Plan A"); err != nil {
			t.Fatal(err)
		}
		bID := planClaimingPath(t, f, local, "Plan B", f.path, []byte(docV2))
		tools := New(local, "calm-mountain")

		// The fixture actually collides, or the refusal below proves nothing.
		review, err := tools.GetReview(f.ctx, GetReviewArgs{PlanID: string(bID)})
		if err != nil {
			t.Fatalf("get_review: %v", err)
		}
		if review.SourceFault != session.SourceFileClaimed.String() {
			t.Fatalf("SourceFault = %q, want %q -- this fixture must actually collide for the refusal below to "+
				"mean anything", review.SourceFault, session.SourceFileClaimed.String())
		}

		out := filepath.Join(t.TempDir(), "worktree.md")
		_, err = tools.Download(f.ctx, DownloadArgs{PlanID: string(bID), Path: out})
		if err == nil {
			t.Fatal("download succeeded for a claimed plan, want a refusal -- the file is readable and belongs " +
				"to plan A now, and download's ordinary file-backed refusal already covers this, unchanged")
		}
		if want := fmt.Sprintf(downloadFileBackedMsg, f.path); err.Error() != want {
			t.Errorf("err = %q, want the ordinary file-backed refusal:\n%q", err, want)
		}
		if _, statErr := os.Stat(out); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("os.Stat(%s) = %v, want the path untouched -- a refusal must not leave a file behind", out, statErr)
		}
	})

	t.Run("released", func(t *testing.T) {
		f := setup(t)
		local := f.newStore(t)
		b, err := session.Open(f.ctx, local, f.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Create(f.ctx, "Plan B"); err != nil {
			t.Fatal(err)
		}

		// The frozen-snapshot race TestResolveBySourceInvitesACreateForAReleasedPath
		// already proves classifies as released: captured before B's own record
		// is re-pointed away from f.path, so f.path itself is left on disk,
		// genuinely readable, and (after the SetSourceHint below) claimed by no
		// plan at all.
		snap, err := session.OpenVersion(f.ctx, local, b.Plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := local.SetSourceHint(f.ctx, b.Plan.ID, "/nowhere/else.md"); err != nil {
			t.Fatalf("SetSourceHint: %v", err)
		}
		applyMCPSourceFault(f.ctx, local, snap)
		if snap.SourceFault == nil || snap.SourceFault.State != session.SourceFileReleased {
			t.Fatalf("SourceFault = %+v, want state %q -- this fixture must actually replay the race for the "+
				"assertion below to mean anything", snap.SourceFault, session.SourceFileReleased)
		}

		// Download's OWN gate, asked of this genuinely-released session's
		// SourceHint -- the exact expression Download's success path is guarded
		// by. A plan_id call cannot be driven to this state on a settled
		// filesystem (see this test's own doc comment), so this is the
		// narrowest true statement available: the predicate that decides
		// refuse-vs-serve for Download does not distinguish released from
		// claimed, and refuses both alike.
		if err := refuseSaveForReadableSource(snap.Plan.SourceHint); err == nil {
			t.Fatal("refuseSaveForReadableSource(released session's SourceHint) = nil, want a refusal -- the " +
				"file is still genuinely readable, which is the one fact this predicate asks about")
		}
	})
}

// TestSaveReportsSourceFaultForAGoneFile is applyMCPSourceFault's fourth
// caller: save asks the SAME question
// get_review's own plan_id and source doors already ask, and tables both
// addressing modes for the identical reason
// TestResolveComputesSourceFaultWhenALocalFileBackedPlansFileIsGone does --
// an earlier investigation already established the two funnel to the
// identical check for gone and unreadable, and the source-door row is what
// proves the fault is latched on the SESSION applyMCPSourceFault builds
// rather than on one calling arm.
func TestSaveReportsSourceFaultForAGoneFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		byID bool
	}{
		{"plan_id door", true},
		{"source door, addressing an already-existing plan", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			local := f.newStore(t)
			s, err := session.Open(f.ctx, local, f.path)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
				t.Fatal(err)
			}
			tools := New(local, "calm-mountain")

			if err := os.Remove(f.path); err != nil {
				t.Fatal(err)
			}

			args := SaveArgs{Content: docV2}
			if tc.byID {
				args.PlanID = string(s.Plan.ID)
			} else {
				args.Source = f.path
			}
			res, err := tools.Save(f.ctx, args)
			if err != nil {
				t.Fatalf("save: %v -- this write must SUCCEED against a plan whose file is gone; a "+
					"refusal here fails that requirement, it does not satisfy it", err)
			}
			if res.Plan == nil || res.Plan.ID != string(s.Plan.ID) {
				t.Fatalf("res.Plan = %+v, want the existing plan %s updated, not a second one created", res.Plan, s.Plan.ID)
			}
			if res.SourceFault != session.SourceFileGone.String() {
				t.Errorf("SourceFault = %q, want %q", res.SourceFault, session.SourceFileGone.String())
			}
			if !strings.Contains(res.Guidance, "gone or has moved") {
				t.Errorf("Guidance = %q, want the gone-specific wording", res.Guidance)
			}
			if !strings.Contains(res.Guidance, "registered as a new version") {
				t.Errorf("Guidance = %q, want it to say the content was registered as a new version", res.Guidance)
			}
			if !strings.Contains(res.Guidance, "did not change that") {
				t.Errorf("Guidance = %q, want it to say the plan still names the dead path (save is not the fix)", res.Guidance)
			}
			if strings.Contains(res.Guidance, "content above") {
				t.Errorf("Guidance = %q, must not reuse get_review's content-withheld wording -- save never "+
					"includes content at all", res.Guidance)
			}
			if n := strings.Count(res.Guidance, f.path); n != 1 {
				t.Errorf("Guidance = %q, contains the path %d times, want exactly 1", res.Guidance, n)
			}

			// The write actually landed against the EXISTING plan, not merely
			// a bystander of the fault report.
			review, err := tools.GetReview(f.ctx, GetReviewArgs{PlanID: string(s.Plan.ID)})
			if err != nil {
				t.Fatalf("get_review: %v", err)
			}
			if review.Content != docV2 {
				t.Fatalf("get_review content = %q, want save's own bytes %q", review.Content, docV2)
			}
		})
	}
}

// TestSaveReportsSourceFaultForAnUnreadableFile is
// TestSaveReportsSourceFaultForAGoneFile's sibling for the state os.Stat
// alone cannot see -- the same chmod 0000 fixture, the same idiom every other
// unreadable test in this file already uses, driven rather than reasoned
// about for the identical reason those do.
func TestSaveReportsSourceFaultForAnUnreadableFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read a 0000 file, so this fixture would prove nothing")
	}
	f := setup(t)
	local := f.newStore(t)
	s, err := session.Open(f.ctx, local, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	tools := New(local, "calm-mountain")

	if err := os.Chmod(f.path, 0o000); err != nil {
		t.Fatal(err)
	}
	// Restored, not left at 0000: t.TempDir()'s own cleanup needs to remove
	// this file afterward.
	t.Cleanup(func() { _ = os.Chmod(f.path, 0o644) })

	res, err := tools.Save(f.ctx, SaveArgs{PlanID: string(s.Plan.ID), Content: docV2})
	if err != nil {
		t.Fatalf("save: %v -- this file classifies as unreadable rather than readable, so "+
			"refuseSaveForReadableSource does not refuse it and this write must succeed", err)
	}
	if res.SourceFault != session.SourceFileUnreadable.String() {
		t.Errorf("SourceFault = %q, want %q", res.SourceFault, session.SourceFileUnreadable.String())
	}
	if !strings.Contains(res.Guidance, "registered as a new version") {
		t.Errorf("Guidance = %q, want it to say the content was registered as a new version", res.Guidance)
	}
	if !strings.Contains(res.Guidance, "make the file readable") {
		t.Errorf("Guidance = %q, want the unreadable-specific remedy", res.Guidance)
	}
	// THE SAME SHAPE, checked again at this fourth site: the path
	// named exactly once, via readReason, never beside the raw *fs.PathError
	// that already names it. See saveSourceFaultGuidance's own doc comment.
	if n := strings.Count(res.Guidance, f.path); n != 1 {
		t.Errorf("Guidance = %q, contains the path %d times, want exactly 1", res.Guidance, n)
	}
}

// TestSaveStillRefusesReleasedAndClaimed is
// TestDownloadStillRefusesReleasedAndClaimed's sibling for save: of the four
// states only gone and unreadable ever reach applyMCPSourceFault's new call
// in Save -- released and claimed still hit the pre-existing capability gate
// (refuseSaveForReadableSource) unchanged, because in both the file IS
// readable and belongs to something else. This is the negative
// TestSaveReportsSourceFaultForAGoneFile and
// TestSaveReportsSourceFaultForAnUnreadableFile need for their own "only two
// of four" claim to mean anything -- without it, nothing here would
// distinguish "the other two also succeed" from "the other two were never
// tried."
func TestSaveStillRefusesReleasedAndClaimed(t *testing.T) {
	t.Run("claimed", func(t *testing.T) {
		f := setup(t)
		local := f.newStore(t)
		a, err := session.Open(f.ctx, local, f.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Create(f.ctx, "Plan A"); err != nil {
			t.Fatal(err)
		}
		bID := planClaimingPath(t, f, local, "Plan B", f.path, []byte(docV2))
		tools := New(local, "calm-mountain")

		// The fixture actually collides, or the refusal below proves nothing.
		review, err := tools.GetReview(f.ctx, GetReviewArgs{PlanID: string(bID)})
		if err != nil {
			t.Fatalf("get_review: %v", err)
		}
		if review.SourceFault != session.SourceFileClaimed.String() {
			t.Fatalf("SourceFault = %q, want %q -- this fixture must actually collide for the refusal below "+
				"to mean anything", review.SourceFault, session.SourceFileClaimed.String())
		}

		_, err = tools.Save(f.ctx, SaveArgs{PlanID: string(bID), Content: "new content"})
		if err == nil {
			t.Fatal("save succeeded for a claimed plan, want a refusal -- the file is readable and belongs " +
				"to plan A now, and save's ordinary capability refusal already covers this, unchanged")
		}
		if err.Error() != saveCapabilityMsg {
			t.Errorf("err = %q, want the ordinary capability refusal:\n%q", err, saveCapabilityMsg)
		}
	})

	t.Run("released", func(t *testing.T) {
		f := setup(t)
		local := f.newStore(t)
		b, err := session.Open(f.ctx, local, f.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Create(f.ctx, "Plan B"); err != nil {
			t.Fatal(err)
		}

		// The frozen-snapshot race TestResolveBySourceInvitesACreateForAReleasedPath
		// already proves classifies as released -- see
		// TestDownloadStillRefusesReleasedAndClaimed's own "released" subtest,
		// whose fixture this reproduces verbatim.
		snap, err := session.OpenVersion(f.ctx, local, b.Plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := local.SetSourceHint(f.ctx, b.Plan.ID, "/nowhere/else.md"); err != nil {
			t.Fatalf("SetSourceHint: %v", err)
		}
		applyMCPSourceFault(f.ctx, local, snap)
		if snap.SourceFault == nil || snap.SourceFault.State != session.SourceFileReleased {
			t.Fatalf("SourceFault = %+v, want state %q -- this fixture must actually replay the race for the "+
				"assertion below to mean anything", snap.SourceFault, session.SourceFileReleased)
		}

		// Save's OWN gate, asked of this genuinely-released session's
		// SourceHint -- a plan_id call cannot be driven to this state on a
		// settled filesystem (see TestDownloadStillRefusesReleasedAndClaimed's
		// own doc comment for why), so this is the narrowest true statement
		// available, and it is the SAME predicate Save's own `if s.Exists`
		// block calls.
		if err := refuseSaveForReadableSource(snap.Plan.SourceHint); err == nil {
			t.Fatal("refuseSaveForReadableSource(released session's SourceHint) = nil, want a refusal -- the " +
				"file is still genuinely readable, which is the one fact this predicate asks about")
		}
	})
}

// TestSaveNoSourceFaultForAHealthyPlan is
// TestGetReviewNoSourceFaultForAHealthyPlan's sibling for save, and it CANNOT
// be built the same way that test is: a local FILE this machine can still
// read is exactly what refuseSaveForReadableSource refuses save outright for
// (TestSaveRefusesReadableFile), whether addressed by source or by the
// plan_id it resolved to -- so there is no such thing as a "healthy
// file-backed plan's save" to carry a negative about. The one shape of
// "healthy" save can actually reach is a URL source (notion://,
// https://): FromSnapshot is true for it, exactly as for gone or
// unreadable, so applyMCPSourceFault's gate does not exempt it --
// what exempts it is sourceFileFault's own URL-source arm (its own doc
// comment has the field report this arm exists for), which answers nil
// regardless of what the wasted os.ReadFile(the literal URL string) found.
// This is the negative TestSaveReportsSourceFaultForAGoneFile's and
// TestSaveReportsSourceFaultForAnUnreadableFile's own "only two of four
// states ever set this field" claim needs: without a URL-source row,
// nothing here would show the gate runs and finds nothing, rather than never
// running at all.
func TestSaveNoSourceFaultForAHealthyPlan(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	v1, err := tools.Save(f.ctx, SaveArgs{Content: docV1, Source: "notion://rate-limiter"})
	if err != nil {
		t.Fatal(err)
	}

	res, err := tools.Save(f.ctx, SaveArgs{PlanID: v1.Plan.ID, Content: docV2})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if res.SourceFault != "" {
		t.Errorf("SourceFault = %q, want \"\" -- a URL source is never a file this machine could lose", res.SourceFault)
	}
	if res.Guidance != "" {
		t.Errorf("Guidance = %q, want \"\" -- nothing about this plan's source needs saying", res.Guidance)
	}
}

// TestSaveSourcelessCarriesNoSourceFault pins the create path save's new
// applyMCPSourceFault call must not perturb: a sourceless save's session has
// Path == "", session.Open(ctx, svc, "") answers ErrNoSourcePath, and
// sourceFileFault's own first arm maps a non-ErrSourceUnreadable failure to
// nil unconditionally -- the same negative TestDownloadServesASourcelessPlan
// already pins for download.
func TestSaveSourcelessCarriesNoSourceFault(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	res, err := tools.Save(f.ctx, SaveArgs{Content: docV1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan == nil || res.Plan.ID == "" {
		t.Fatalf("plan = %+v, want a created plan", res.Plan)
	}
	if res.SourceFault != "" {
		t.Errorf("SourceFault = %q, want \"\" -- a sourceless plan has no file to be missing", res.SourceFault)
	}
	if res.Guidance != "" {
		t.Errorf("Guidance = %q, want \"\" -- nothing about this plan's source needs saying", res.Guidance)
	}
}

// TestSaveReportsReleasedAndClaimedAcrossTheDoorMeasurementRace pins the case
// an instruction to "mirror Download" got wrong: that instruction was read as
// "give saveSourceFaultGuidance the identical unreachable default arm
// Download has," but Download's default arm is unreachable BECAUSE it
// measures the fault first and re-asks its door last -- a mid-flight state
// flip fails CLOSED there. Save's ordering is the mirror image: the door
// runs first, applyMCPSourceFault runs LAST, after s.Register and
// reviewResult both return, and nothing re-validates the door's answer in
// that gap. A concurrent repoint of the EXACT plan being saved, with or
// without a second plan taking its path, landing in that window, makes the
// LATER read observe released or claimed -- states the door was supposed to
// have excluded by the time this call reaches its tail.
//
// DRIVEN AT THE HELPER LEVEL, NOT THROUGH A SINGLE tools.Save CALL, and said
// so rather than pretended otherwise -- the same split
// TestSaveStillRefusesReleasedAndClaimed's own "released" subtest already
// uses, for the identical reason: the race this proves lives INSIDE one
// invocation of Save, in the gap between two statements of its own body, and
// there is no hook from outside a single call to pause it there. What this
// test drives instead is Save's own tail, verbatim and in its own order --
// build the session the plan_id door builds (session.OpenSuppliedForPlan),
// register (s.Register), THEN mutate the store exactly as a racing repoint
// and a racing create would, THEN call applyMCPSourceFault, sourceFaultState
// and appendGuidance in the identical sequence Save's own tail calls them --
// so the assertions below are checking Save's real composition logic against
// a real, if hand-driven, sequence of real store states, deterministically
// and without a goroutine, a channel or a sleep anywhere in it.
func TestSaveReportsReleasedAndClaimedAcrossTheDoorMeasurementRace(t *testing.T) {
	t.Run("claimed", func(t *testing.T) {
		f := setup(t)
		local := f.newStore(t)
		a, err := session.Open(f.ctx, local, f.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Create(f.ctx, "Plan A"); err != nil {
			t.Fatal(err)
		}

		// The door's own moment: the file is gone, so
		// refuseSaveForReadableSource passes exactly as it would inside
		// Save's plan_id arm.
		if err := os.Remove(f.path); err != nil {
			t.Fatal(err)
		}
		if err := refuseSaveForReadableSource(a.Plan.SourceHint); err != nil {
			t.Fatalf("refuseSaveForReadableSource = %v, want nil -- the file is gone, which is this test's own premise", err)
		}

		// Save's plan_id door: OpenSuppliedForPlan on the plan the door just
		// cleared, then the write.
		s, err := session.OpenSuppliedForPlan(f.ctx, local, []byte(docV2), a.Plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Register(f.ctx); err != nil {
			t.Fatal(err)
		}

		// THE RACE: a human repoints plan A elsewhere (freeing f.path) and a
		// second plan claims the now-vacant path, with a real file put back
		// under it -- all landing in the window between the door check above
		// and the measurement below, which is exactly what Save's own
		// ordering (door first, fault last) leaves open.
		if err := local.SetSourceHint(f.ctx, a.Plan.ID, "/repointed/elsewhere.md"); err != nil {
			t.Fatalf("SetSourceHint (repoint A away): %v", err)
		}
		if _, err := local.CreatePlan(f.ctx, "Plan C", f.path, "", []byte(docV1)); err != nil {
			t.Fatalf("CreatePlan (a fresh plan claims the vacated path): %v", err)
		}
		if err := os.WriteFile(f.path, []byte(docV1), 0o644); err != nil {
			t.Fatalf("putting a real, readable file back at the raced path: %v", err)
		}

		// Save's own tail, verbatim and in its own order.
		applyMCPSourceFault(f.ctx, local, s)
		if s.SourceFault == nil || s.SourceFault.State != session.SourceFileClaimed {
			t.Fatalf("SourceFault = %+v, want state %q -- this fixture must actually race for the assertions "+
				"below to mean anything", s.SourceFault, session.SourceFileClaimed)
		}
		sourceFault := sourceFaultState(s)
		guidance := appendGuidance("", saveSourceFaultGuidance(s.SourceFault))

		if sourceFault != session.SourceFileClaimed.String() {
			t.Errorf("source_fault = %q, want %q", sourceFault, session.SourceFileClaimed.String())
		}
		if guidance == "" {
			t.Fatal("guidance = \"\", want a non-empty sentence -- an enum reaching the result must never be " +
				"left unexplained, and this is exactly the state that requires it")
		}
		if !strings.Contains(guidance, "another plan is already following this file") {
			t.Errorf("guidance = %q, want the claimed-specific headline", guidance)
		}
		if !strings.Contains(guidance, "registered as a new version of THIS plan, not that one") {
			t.Errorf("guidance = %q, want save's own claimed consequence, distinct from the gone/unreadable "+
				"wording", guidance)
		}
		if strings.Contains(guidance, "if they point it at a new file") {
			t.Errorf("guidance = %q, must not carry the dead-path remedy -- this file is not dead, it belongs "+
				"to another plan", guidance)
		}
	})

	t.Run("released", func(t *testing.T) {
		f := setup(t)
		local := f.newStore(t)
		a, err := session.Open(f.ctx, local, f.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Create(f.ctx, "Plan A"); err != nil {
			t.Fatal(err)
		}

		if err := os.Remove(f.path); err != nil {
			t.Fatal(err)
		}
		if err := refuseSaveForReadableSource(a.Plan.SourceHint); err != nil {
			t.Fatalf("refuseSaveForReadableSource = %v, want nil -- the file is gone, which is this test's own premise", err)
		}

		s, err := session.OpenSuppliedForPlan(f.ctx, local, []byte(docV2), a.Plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Register(f.ctx); err != nil {
			t.Fatal(err)
		}

		// THE RACE: a human repoints plan A elsewhere and the vacated path
		// gets a real, readable file back -- but nothing else ever claims
		// it, so it resolves to no plan at all.
		if err := local.SetSourceHint(f.ctx, a.Plan.ID, "/repointed/elsewhere.md"); err != nil {
			t.Fatalf("SetSourceHint (repoint A away): %v", err)
		}
		if err := os.WriteFile(f.path, []byte(docV1), 0o644); err != nil {
			t.Fatalf("putting a real, readable file back at the raced path: %v", err)
		}

		applyMCPSourceFault(f.ctx, local, s)
		if s.SourceFault == nil || s.SourceFault.State != session.SourceFileReleased {
			t.Fatalf("SourceFault = %+v, want state %q -- this fixture must actually race for the assertions "+
				"below to mean anything", s.SourceFault, session.SourceFileReleased)
		}
		sourceFault := sourceFaultState(s)
		guidance := appendGuidance("", saveSourceFaultGuidance(s.SourceFault))

		if sourceFault != session.SourceFileReleased.String() {
			t.Errorf("source_fault = %q, want %q", sourceFault, session.SourceFileReleased.String())
		}
		if guidance == "" {
			t.Fatal("guidance = \"\", want a non-empty sentence -- an enum reaching the result must never be " +
				"left unexplained, and this is exactly the state that requires it")
		}
		if !strings.Contains(guidance, "no longer resolves to this plan") {
			t.Errorf("guidance = %q, want the released-specific headline", guidance)
		}
		if !strings.Contains(guidance, "registered as a new version of this plan regardless") {
			t.Errorf("guidance = %q, want save's own released consequence, distinct from the gone/unreadable "+
				"wording", guidance)
		}
		if strings.Contains(guidance, "if they point it at a new file") {
			t.Errorf("guidance = %q, must not carry the dead-path remedy -- this file is not dead, it belongs "+
				"to no one, which is a different fact", guidance)
		}

		// CHECKED AGAINST THE REAL STORE, not merely another substring: a
		// claim like "the plan still names that file," carried over from
		// gone/unreadable, is false BY CONSTRUCTION for released --
		// SourceFileReleased's own definition (sess == nil || !sess.Exists)
		// means the path resolves to no plan at all, which requires this
		// plan's own record to have already moved off it (if it still named
		// this path, ResolvePlan would find it and there would be no fault
		// to report). Checked against plan A's ACTUAL current record, not
		// merely reasoned about: a substring-only assertion could not have
		// caught that false clause, because it never reads what the store
		// actually says.
		current, err := local.PlanByID(f.ctx, a.Plan.ID)
		if err != nil {
			t.Fatalf("PlanByID: %v", err)
		}
		if current.SourceHint == f.path {
			t.Fatalf("test premise broken: plan A's current SourceHint is still %q, want it repointed away -- "+
				"the assertion below is meaningless unless this plan genuinely no longer names the raced path",
				f.path)
		}
		if strings.Contains(guidance, "still names") {
			t.Errorf("guidance = %q claims the plan still names a file, but plan A's ACTUAL current record "+
				"names %q, not %q -- this is exactly the false-by-construction clause that must never reappear",
				guidance, current.SourceHint, f.path)
		}
	})
}

// TestLocalFileAsksTheQuestionItClaims is a direct, package-internal unit
// test of localFile itself, previously untested at this level even though
// six callers' doc comments all claimed to know its answer. A healthy file
// and a missing one prove the ordinary cases are unmoved; the chmod 0000 row
// is the open-for-read fix (os.Stat alone said "readable" here, os.Open
// correctly does not); the directory row is a named trap,
// MUTATION-PINNED rather than merely avoided: os.Open succeeds on a
// directory (only a later read fails, EISDIR), so if the existing Stat+IsDir
// guard that must be KEPT were ever dropped in favour of the open
// alone, this row -- and only this row -- would flip from readable=false to
// readable=true and catch it.
func TestLocalFileAsksTheQuestionItClaims(t *testing.T) {
	dir := t.TempDir()
	healthy := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(healthy, []byte(docV1), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "never-existed.md")
	unreadable := filepath.Join(dir, "no-permission.md")
	if err := os.WriteFile(unreadable, []byte(docV1), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name         string
		path         string
		skipAsRoot   bool
		wantReadable bool
	}{
		{name: "a healthy file is readable", path: healthy, wantReadable: true},
		{name: "a missing path is not readable", path: missing, wantReadable: false},
		{
			name: "a directory is not readable -- a named trap: os.Open " +
				"succeeds on one, so this is the IsDir guard's own pin",
			path: dir, wantReadable: false,
		},
		{
			name:         "a permission-denied file is not readable",
			path:         unreadable,
			skipAsRoot:   true,
			wantReadable: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipAsRoot && os.Getuid() == 0 {
				t.Skip("root can read a 0000 file, so this fixture would prove nothing")
			}
			if tc.path == unreadable {
				if err := os.Chmod(unreadable, 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(unreadable, 0o644) })
			}
			gotPath, gotReadable := localFile(tc.path)
			if gotReadable != tc.wantReadable {
				t.Errorf("localFile(%q) readable = %v, want %v", tc.path, gotReadable, tc.wantReadable)
			}
			if tc.wantReadable {
				abs, err := filepath.Abs(tc.path)
				if err != nil {
					t.Fatal(err)
				}
				if gotPath != abs {
					t.Errorf("localFile(%q) path = %q, want %q", tc.path, gotPath, abs)
				}
			} else if gotPath != "" {
				t.Errorf("localFile(%q) path = %q, want \"\" alongside readable=false", tc.path, gotPath)
			}
		})
	}
}

// TestGetReviewStillWithholdsContentForReleasedAndClaimed is the negative the
// open-for-read fix requires and TestGetReviewIncludesContentForAnUnreadableFile
// would otherwise leave unstated: the split that fix corrects is 2-of-4, not
// 4-of-4, so a change that accidentally opened every state's content back up
// -- for instance by loosening refuseSaveForReadableSource itself, rather
// than localFile's narrower stat-vs-open predicate -- must still fail here.
//
// s.SourceFault IS SET DIRECTLY rather than raced into existence, for the
// same reason TestSourceFaultGuidanceIsStateSpecific above does not construct
// a real released or claimed session: doing so needs a second plan racing
// the first for one exact path (sourceFileFault's own arms), plumbing this
// test has no need of. What IS real here, and is the whole point of driving
// reviewResult rather than sourceFaultGuidance directly as that test does, is
// s.Plan.SourceHint: a genuinely healthy, genuinely readable file on disk,
// so refuseSaveForReadableSource -- the actual predicate the open-for-read
// fix touched, via localFile -- runs for real and answers for itself rather than being told
// what to answer.
func TestGetReviewStillWithholdsContentForReleasedAndClaimed(t *testing.T) {
	for _, state := range []session.SourceFileState{session.SourceFileReleased, session.SourceFileClaimed} {
		t.Run(state.String(), func(t *testing.T) {
			f := setup(t)
			local := f.newStore(t)
			s, err := session.Open(f.ctx, local, f.path)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
				t.Fatal(err)
			}
			// f.path is untouched -- still on disk, still perfectly readable --
			// which is the headline for these two states and the reason
			// their content stays withheld: not because the file failed, but
			// because it is not this plan's document any more.
			s.SourceFault = &session.SourceFileError{PlanID: s.Plan.ID, Path: f.path, State: state}

			res, err := reviewResult(f.ctx, s, true, "calm-mountain")
			if err != nil {
				t.Fatalf("reviewResult: %v", err)
			}
			if res.SourceFault != state.String() {
				t.Fatalf("SourceFault = %q, want %q", res.SourceFault, state.String())
			}
			if res.Content != "" {
				t.Errorf("Content = %q, want \"\" -- %s's file is genuinely readable but is not this plan's "+
					"document any more", clipRunes(res.Content, 40), state)
			}
			if !strings.Contains(res.Guidance, "content above is empty for this call") {
				t.Errorf("Guidance = %q, want it to say content was withheld", res.Guidance)
			}
			if strings.Contains(res.Guidance, "unaffected by this state") {
				t.Errorf("Guidance = %q, wrongly claims content is unaffected -- true of gone and unreadable "+
					"alone", res.Guidance)
			}
		})
	}
}

// TestResolveBySourceAnswersTheCurrentFileOwnerForAClaimedPath is an
// investigation of the strongest defect candidate for this behaviour, proven
// rather than assumed: addressing a CLAIMED file's path returns a DIFFERENT
// plan's review with no source_fault at all. This is the readable-local-file
// branch's refusal-to-refuse (resolve's own doc, one paragraph up) meeting the
// claimed state rather than a deleted plan, and it is NOT a defect -- session.Open
// answers the path's TRUE current owner, exactly as a healthy path always
// does, and that owner really is a healthy plan with nothing wrong to report.
//
// A REAL COLLISION, NOT A FAKED FAULT: A is created first at f.path
// (session.Create), so client/localfs.Store.ResolvePlan -- first-SourceHint-
// match wins -- answers A for that path from here on. B is created second and
// pointed at f.path too (planClaimingPath).
func TestResolveBySourceAnswersTheCurrentFileOwnerForAClaimedPath(t *testing.T) {
	f := setup(t)
	local := f.newStore(t)
	a, err := session.Open(f.ctx, local, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Create(f.ctx, "Plan A"); err != nil {
		t.Fatal(err)
	}
	bID := planClaimingPath(t, f, local, "Plan B", f.path, []byte(docV2))
	tools := New(local, "calm-mountain")

	// By B's own id: the fault-check machinery fires correctly.
	byID, err := tools.GetReview(f.ctx, GetReviewArgs{PlanID: string(bID)})
	if err != nil {
		t.Fatalf("get_review by plan_id: %v", err)
	}
	if byID.SourceFault != session.SourceFileClaimed.String() {
		t.Fatalf("SourceFault = %q, want %q -- this fixture must actually collide for the test below to mean "+
			"anything", byID.SourceFault, session.SourceFileClaimed.String())
	}

	// By the path itself: no fault, and the plan named is A -- the file's real,
	// current owner -- not B and not an error.
	bySource, err := tools.GetReview(f.ctx, GetReviewArgs{Source: f.path})
	if err != nil {
		t.Fatalf("get_review by source: %v", err)
	}
	if bySource.SourceFault != "" {
		t.Errorf("SourceFault = %q, want \"\" -- the path answered honestly, which is not a fault", bySource.SourceFault)
	}
	if bySource.Plan == nil || bySource.Plan.ID != string(a.Plan.ID) {
		t.Errorf("Plan = %+v, want A (%s) -- the file's true current owner, per "+
			"client/localfs.Store.ResolvePlan's first-match rule", bySource.Plan, a.Plan.ID)
	}
}

// TestResolveBySourceInvitesACreateForAReleasedPath pins the other divergence
// resolve's own doc comment names (the first is
// TestResolveBySourceAnswersTheCurrentFileOwnerForAClaimedPath's): a released
// file invites creating a new, unrelated plan at that path. Also not a defect
// -- see that doc comment for why closing it costs either the readable-file
// ruling or a cost this path cannot pay for a state a settled filesystem
// cannot even produce here without help.
//
// THE RACE IS REPLAYED BY HAND, deliberately, for the identical reason
// TestGetReviewStillWithholdsContentForReleasedAndClaimed's own doc comment
// gives: a plan's record IS the one ResolvePlan would have to fail to find at
// its own path, so SourceFileReleased cannot arise here from a settled state
// alone. session.OpenVersion captures a real snapshot BEFORE a SetSourceHint
// re-points B's OWN record away from f.path -- simulating exactly the
// concurrent-call window applyMCPSourceFault's doc says it exists to catch --
// and applyMCPSourceFault is then run for real against that frozen snapshot,
// not faked.
func TestResolveBySourceInvitesACreateForAReleasedPath(t *testing.T) {
	f := setup(t)
	local := f.newStore(t)
	b, err := session.Open(f.ctx, local, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Create(f.ctx, "Plan B"); err != nil {
		t.Fatal(err)
	}

	snap, err := session.OpenVersion(f.ctx, local, b.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.SetSourceHint(f.ctx, b.Plan.ID, "/nowhere/else.md"); err != nil {
		t.Fatalf("SetSourceHint: %v", err)
	}
	applyMCPSourceFault(f.ctx, local, snap)
	if snap.SourceFault == nil || snap.SourceFault.State != session.SourceFileReleased {
		t.Fatalf("SourceFault = %+v, want state %q -- this fixture must actually replay the race for the test "+
			"below to mean anything", snap.SourceFault, session.SourceFileReleased)
	}

	// f.path itself is untouched: still on disk, still perfectly readable, and
	// (after the SetSourceHint above) claimed by no plan at all.
	tools := New(local, "calm-mountain")
	bySource, err := tools.GetReview(f.ctx, GetReviewArgs{Source: f.path})
	if err != nil {
		t.Fatalf("get_review by source: %v", err)
	}
	if bySource.SourceFault != "" {
		t.Errorf("SourceFault = %q, want \"\" -- the live-follow branch never asks applyMCPSourceFault's "+
			"question", bySource.SourceFault)
	}
	if bySource.Plan != nil {
		t.Errorf("Plan = %+v, want nil -- indistinguishable, on this arm, from a document draftplane has "+
			"never seen", bySource.Plan)
	}
	if !strings.Contains(bySource.Guidance, "no plan is registered for this file yet") {
		t.Errorf("Guidance = %q, want the ordinary create invitation -- an agent that acts on it mints a "+
			"SECOND, unrelated plan at this path, exactly as "+
			"TestADownloadedPathAddressedAsSourceForksASecondPlan's fork already does", bySource.Guidance)
	}
}

// TestResolveBySourcePermissionDeniedAndNeverRegisteredNamesTheRealCause pins
// the regression the open-for-read fix created: before that fix,
// localFile's os.Stat-only check called a permission-denied
// file "readable", so it took the live-follow branch and session.Open's own
// os.ReadFile failure reached the caller with the real cause attached. The
// fix correctly made localFile ask for a real read, so the identical file now
// answers readable=false and falls to ResolvePlan instead -- which is a bare
// SourceHint-index lookup that never touches the filesystem, so for a source
// NOTHING has ever registered it answers only the bare client.ErrNoPlan,
// discarding the one actionable fact (permission denied, not absent) the
// caller had. sourceOpenFailure restores it without touching localFile,
// which already asks the right question, or reading the path a second time
// on any OTHER call shape.
func TestResolveBySourcePermissionDeniedAndNeverRegisteredNamesTheRealCause(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read a 0000 file, so this fixture would prove nothing")
	}
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	if err := os.Chmod(f.path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.path, 0o644) })

	_, err := tools.GetReview(f.ctx, GetReviewArgs{Source: f.path})
	if err == nil {
		t.Fatal("err = nil, want a refusal -- nothing was ever registered for this source")
	}
	if !errors.Is(err, client.ErrNoPlan) {
		t.Fatalf("err = %v, want errors.Is(err, client.ErrNoPlan)", err)
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("err = %q, want the real OS cause named", err.Error())
	}
	if strings.Contains(err.Error(), "if you have its content, save it first") {
		t.Errorf("err = %q, still gives the generic never-seen wording alongside the real cause -- the two "+
			"are mutually exclusive branches of one message", err.Error())
	}
}

// TestResolveBySourceUnregisteredMissingFileKeepsTheGenericWording is the
// negative TestResolveBySourcePermissionDeniedAndNeverRegisteredNamesTheRealCause
// requires: a path that plain never existed must keep the ordinary "no plan
// known ... save it first" wording, unchanged, rather than being told it
// could not be opened -- ENOENT is not the diagnostic sourceOpenFailure exists
// to add, since the generic wording is already correct for a document that
// was simply never there.
func TestResolveBySourceUnregisteredMissingFileKeepsTheGenericWording(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	_, err := tools.GetReview(f.ctx, GetReviewArgs{Source: filepath.Join(f.dir, "never-existed.md")})
	if err == nil || !errors.Is(err, client.ErrNoPlan) {
		t.Fatalf("err = %v, want errors.Is(err, client.ErrNoPlan)", err)
	}
	if !strings.Contains(err.Error(), "if you have its content, save it first") {
		t.Errorf("err = %q, want the generic never-seen wording -- a missing path is not a permission failure",
			err.Error())
	}
	if strings.Contains(err.Error(), "could not be opened") {
		t.Errorf("err = %q, wrongly claims a diagnosable open failure for a path that was simply never there",
			err.Error())
	}
}

// TestGetReviewDistinguishesGenuinelyEmptyContentFromWithheld is proven
// against running code rather than assumed: approve
// runs no emptiness check at all (ensurePlan creates unconditionally,
// Session.Approve registers whatever content is already loaded -- a
// long-standing, separate gap; the TUI has the identical one independently
// in updatePanel's modeConfirmApprove case (app/actions.go), and closing it
// is a product question that is NOT this package's to answer), so a plan
// whose registered content is genuinely the empty string is constructible.
// Addressed by gone -- one of the two states (with unreadable) the content
// gate never withholds for -- get_review must say the content is genuinely
// empty, NOT that it was withheld: nothing here was hidden, the document
// really is zero bytes, and a simpler stand-in, res.Content != "", would get
// this exact case wrong (it cannot tell "the gate said no" from "the gate
// said yes and the answer was nothing").
func TestGetReviewDistinguishesGenuinelyEmptyContentFromWithheld(t *testing.T) {
	f := setup(t)
	local := f.newStore(t)
	emptyPath := filepath.Join(f.dir, "empty.md")
	if err := os.WriteFile(emptyPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	tools := New(local, "calm-mountain")

	if _, err := tools.Approve(f.ctx, ApproveArgs{Source: emptyPath}); err != nil {
		t.Fatalf("approve: %v -- approve has no emptiness guard (a separate, standing gap), so a 0-byte "+
			"file must be approvable", err)
	}

	// Read the plan id back by source, BEFORE deleting the file -- get_review
	// by plan_id afterward is what actually funnels through
	// applyMCPSourceFault (t.resolve's id-shaped branches), which is the
	// path this test means to exercise.
	before, err := tools.GetReview(f.ctx, GetReviewArgs{Source: emptyPath})
	if err != nil {
		t.Fatalf("get_review by source: %v", err)
	}
	if before.Plan == nil || before.Plan.ID == "" {
		t.Fatalf("plan = %+v, want a plan approve just created", before.Plan)
	}
	if before.Content != "" {
		t.Fatalf("Content = %q, want \"\" -- approve registered zero bytes", before.Content)
	}
	planID := before.Plan.ID

	if err := os.Remove(emptyPath); err != nil {
		t.Fatal(err)
	}

	review, err := tools.GetReview(f.ctx, GetReviewArgs{PlanID: planID})
	if err != nil {
		t.Fatalf("get_review: %v", err)
	}
	if review.SourceFault != session.SourceFileGone.String() {
		t.Fatalf("SourceFault = %q, want %q -- the fixture must actually classify as gone for this test to "+
			"mean anything", review.SourceFault, session.SourceFileGone.String())
	}
	if review.Content != "" {
		t.Errorf("Content = %q, want \"\" -- the plan's real registered content is zero bytes", review.Content)
	}
	if !strings.Contains(review.Guidance, "content above is genuinely empty") {
		t.Errorf("Guidance = %q, want the genuinely-empty note -- nothing was withheld here, the document "+
			"really is empty", review.Guidance)
	}
	if strings.Contains(review.Guidance, "cannot currently be read as its own file") {
		t.Errorf("Guidance = %q, wrongly claims content was withheld -- gone's gate let this content "+
			"through; it was empty on arrival, not hidden on the way out", review.Guidance)
	}
}

// seededPlan is a real plan and a real thread over a real local store at
// f.path, so t.resolve and every read leading up to a write succeeds exactly as
// production would.
func seededPlan(t *testing.T, f fixture) (local *localfs.Store, planID domain.PlanID, threadID domain.ThreadID) {
	t.Helper()
	local = f.newStore(t)
	s, err := session.Open(f.ctx, local, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(f.ctx, "Rate Limiter"); err != nil {
		t.Fatal(err)
	}
	th, err := s.Comment(f.ctx, reanchor.Anchor{HeadingPath: []string{"Rate Limiter Plan", "Design"}, Span: "burst capacity of fifty"}, "seed")
	if err != nil {
		t.Fatal(err)
	}
	return local, s.Plan.ID, th.ID
}

// TestWriteVerbsCarrySourceFaultAndStillSucceed pins that the six
// review-fact writes that resolve a plan --
// comment, comment_section, reply, resolve, rehome, approve -- carry
// get_review's own source_fault signal, and that they DO SO
// WITHOUT REFUSING. Both halves are asserted for every verb, in every case,
// because a test that checked only the state would still pass if a later
// change made one of these verbs start refusing on this state instead.
//
// ONE SHARED FIXTURE (seededPlan: one plan, one seeded thread, so
// reply/resolve/rehome have a real thread_id without each growing its own
// setup) drives both the healthy and the file-gone case, so the two differ in
// nothing but whether f.path was removed before the table runs.
func TestWriteVerbsCarrySourceFaultAndStillSucceed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		removeFile bool
		want       string
	}{
		// The negative first: absence must be the ordinary case, not merely
		// the untested one. If this subtest is wrong, the "gone" subtest below
		// proves nothing -- it would be comparing two states that already
		// agree.
		{"healthy plan: field absent", false, ""},
		// Stated at the assertion rather than only in the doc comment
		// above: every one of the six calls below is expected to SUCCEED
		// against a plan whose source file is gone. A test that asserts a
		// refusal here would be asserting against that ruling, not proving
		// one -- see the per-verb t.Fatalf below, which names this
		// explicitly so the failure is legible on its own.
		{"file gone: field carries the state", true, session.SourceFileGone.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			local, planID, threadID := seededPlan(t, f)
			tools := New(local, "calm-mountain")
			if tc.removeFile {
				if err := os.Remove(f.path); err != nil {
					t.Fatal(err)
				}
			}

			for _, verb := range []struct {
				name string
				call func() (string, error)
			}{
				{"comment", func() (string, error) {
					res, err := tools.Comment(f.ctx, CommentArgs{
						PlanID: string(planID), Quote: "database suffers under load spikes", Body: "flag this",
					})
					return res.SourceFault, err
				}},
				{"comment_section", func() (string, error) {
					res, err := tools.CommentSection(f.ctx, CommentSectionArgs{
						PlanID: string(planID), HeadingPath: []string{"Rate Limiter Plan", "Rollout"}, Body: "needs detail",
					})
					return res.SourceFault, err
				}},
				{"reply", func() (string, error) {
					res, err := tools.Reply(f.ctx, ReplyArgs{PlanID: string(planID), ThreadID: string(threadID), Body: "agreed"})
					return res.SourceFault, err
				}},
				{"resolve", func() (string, error) {
					res, err := tools.Resolve(f.ctx, ResolveArgs{PlanID: string(planID), ThreadID: string(threadID), Resolved: true})
					return res.SourceFault, err
				}},
				{"rehome", func() (string, error) {
					res, err := tools.Rehome(f.ctx, RehomeArgs{
						PlanID: string(planID), ThreadID: string(threadID), Quote: "unbounded and the database",
					})
					return res.SourceFault, err
				}},
				{"approve", func() (string, error) {
					res, err := tools.Approve(f.ctx, ApproveArgs{PlanID: string(planID)})
					return res.SourceFault, err
				}},
			} {
				t.Run(verb.name, func(t *testing.T) {
					got, err := verb.call()
					if err != nil {
						t.Fatalf("%s: %v -- this write must SUCCEED against a plan whose file is gone; "+
							"a refusal here fails that requirement, it does not satisfy it", verb.name, err)
					}
					if got != tc.want {
						t.Errorf("%s: source_fault = %q, want %q", verb.name, got, tc.want)
					}
				})
			}
		})
	}
}

// TestAppendGuidanceComposesRatherThanOverwrites pins the composition
// appendGuidance exists for (see its own doc comment for the full argument):
// two non-empty producers are JOINED, not one replacing the other, so a future
// change that lets both fire on one plan degrades to a longer sentence rather
// than a silently dropped one.
func TestAppendGuidanceComposesRatherThanOverwrites(t *testing.T) {
	for _, tc := range []struct {
		name             string
		guidance, latest string
		want             string
	}{
		{"both empty", "", "", ""},
		{"only the new sentence", "", "second", "second"},
		{"only the existing sentence", "first", "", "first"},
		{"both -- joined, neither dropped", "first", "second", "first; second"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := appendGuidance(tc.guidance, tc.latest); got != tc.want {
				t.Errorf("appendGuidance(%q, %q) = %q, want %q", tc.guidance, tc.latest, got, tc.want)
			}
		})
	}
}

// TestDownloadServesASourcelessPlan pins the shape this verb exists for
// -- a plan written in a conversation and never on disk -- and it is
// here because every OTHER shape at this door had a package-level test while
// this one was pinned only by a gated harness that no longer exists.
// A regression that made download refuse the plan it was BUILT to serve would
// have kept `make check` green.
//
// IT IS THE SAME PREDICATE AS THE TWO REFUSAL TESTS ABOVE, answering the other
// way: a sourceless plan's SourceHint is "", localFile finds nothing readable,
// and both of download's questions are satisfied. So this row and those rows are
// one rule read from both sides, which is why it sits beside them.
func TestDownloadServesASourcelessPlan(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	saved, err := tools.Save(f.ctx, SaveArgs{Content: docV1})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Plan.SourceHint != "" {
		t.Fatalf("source_hint = %q, want empty -- this test's premise is a plan with no source at all",
			saved.Plan.SourceHint)
	}

	out := filepath.Join(t.TempDir(), "worktree.md")
	dl, err := tools.Download(f.ctx, DownloadArgs{PlanID: saved.Plan.ID, Path: out})
	if err != nil {
		t.Fatalf("download of a sourceless plan: %v -- draftplane holds the only copy of this plan's bytes, "+
			"which is the case this verb exists for", err)
	}
	if dl.Path != out {
		t.Errorf("Path = %s, want the path asked for (%s)", dl.Path, out)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != docV1 {
		t.Errorf("download wrote %q, want the saved bytes %q", clipRunes(string(got), 40), clipRunes(docV1, 40))
	}
	if dl.Hash != saved.Hash {
		t.Errorf("Hash = %s, want the version save landed (%s) -- the hash names the bytes written",
			dl.Hash, saved.Hash)
	}
	// The two new fields' own negative: a sourceless plan has no file on this
	// machine to fail, so applyMCPSourceFault's fresh open of the empty
	// SourceHint answers ErrNoSourcePath and neither field appears -- the same
	// rule TestGetReviewNoSourceFaultForAHealthyPlan already pins for get_review.
	if dl.SourceFault != "" {
		t.Errorf("SourceFault = %q, want \"\" -- a sourceless plan has no file to be missing", dl.SourceFault)
	}
	if dl.Guidance != "" {
		t.Errorf("Guidance = %q, want \"\" -- nothing about this plan's source needs saying", dl.Guidance)
	}
}

// TestDownloadRefusesEveryPathItCannotCreate tables the four ways the kernel can
// refuse the create plus the empty path this package refuses ahead of it. Three
// of the four -- an existing file, a DIRECTORY and a symlink pointing at nothing
// -- are one EEXIST and get one sentence, implemented that way deliberately:
// the sentence enumerates the three shapes rather than classifying them, so it
// is true of all of them; this table is what proves each really does arrive
// there.
//
// Every row also asserts THE PATH IS UNCHANGED, which is the half a message
// assertion cannot see: download must not truncate the file it refuses, must not
// replace the directory, and must not create the missing parent.
func TestDownloadRefusesEveryPathItCannotCreate(t *testing.T) {
	const existing = "a document somebody else put here"
	for _, tc := range []struct {
		name string
		// plant prepares the path and returns the argument to pass.
		plant func(t *testing.T, dir string) string
		// wantMsg is the whole refusal, formatted with the resolved path.
		wantMsg func(abs string) string
		// check asserts the path is exactly as plant left it.
		check func(t *testing.T, path string)
	}{
		{
			name: "a file is already there",
			plant: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "taken.md")
				if err := os.WriteFile(p, []byte(existing), 0o644); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantMsg: func(abs string) string { return fmt.Sprintf(downloadPathInTheWayMsg, abs) },
			check: func(t *testing.T, p string) {
				got, err := os.ReadFile(p)
				if err != nil || string(got) != existing {
					t.Errorf("file holds %q (err %v), want it untouched -- download never writes over what is there", got, err)
				}
			},
		},
		{
			name: "a directory is already there",
			plant: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "taken")
				if err := os.Mkdir(p, 0o755); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantMsg: func(abs string) string { return fmt.Sprintf(downloadPathInTheWayMsg, abs) },
			check: func(t *testing.T, p string) {
				info, err := os.Stat(p)
				if err != nil || !info.IsDir() {
					t.Errorf("os.Stat = %v, %v, want the directory still a directory", info, err)
				}
			},
		},
		{
			name: "a symlink pointing at nothing is already there",
			plant: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "dangling.md")
				if err := os.Symlink(filepath.Join(dir, "never-existed.md"), p); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantMsg: func(abs string) string { return fmt.Sprintf(downloadPathInTheWayMsg, abs) },
			check: func(t *testing.T, p string) {
				info, err := os.Lstat(p)
				if err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("os.Lstat = %v, %v, want the symlink still a symlink", info, err)
				}
				if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("os.Stat = %v, want the symlink still dangling -- download must not have created its target", err)
				}
			},
		},
		{
			name: "the directory that would hold it does not exist",
			plant: func(_ *testing.T, dir string) string {
				return filepath.Join(dir, "no-such-dir", "worktree.md")
			},
			wantMsg: func(abs string) string { return fmt.Sprintf(downloadNoParentMsg, abs) },
			check: func(t *testing.T, p string) {
				if _, err := os.Stat(filepath.Dir(p)); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("os.Stat(%s) = %v, want the parent still missing -- download does not MkdirAll", filepath.Dir(p), err)
				}
			},
		},
		{
			// Refused ahead of the kernel, and the refusal must not be the
			// path-in-the-way one: filepath.Abs("") answers the working
			// directory, so an unguarded empty path asks the kernel to create
			// the directory this process is sitting in and is told "a directory
			// is already there" about somewhere the caller never named.
			name:  "no path at all",
			plant: func(_ *testing.T, _ string) string { return "   " },
			wantMsg: func(string) string {
				return "download needs a path: pass the file to write the content to"
			},
			check: func(_ *testing.T, _ string) {},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			tools := New(f.newStore(t), "calm-mountain")
			saved, err := tools.Save(f.ctx, SaveArgs{Content: docV1, Source: "notion://downloadable"})
			if err != nil {
				t.Fatal(err)
			}
			path := tc.plant(t, t.TempDir())

			_, err = tools.Download(f.ctx, DownloadArgs{PlanID: saved.Plan.ID, Path: path})
			if err == nil {
				t.Fatal("download succeeded, want a refusal")
			}
			if want := tc.wantMsg(path); err.Error() != want {
				t.Errorf("err = %q, want:\n%q", err, want)
			}
			tc.check(t, path)
		})
	}
}

// savedPlan saves docV1 under a source draftplane cannot read, so the plan's
// bytes are the ones draftplane holds and download can serve them by plan_id.
func savedPlan(t *testing.T, f fixture) (*Tools, domain.PlanID) {
	t.Helper()
	tools := New(f.newStore(t), "calm-mountain")
	saved, err := tools.Save(f.ctx, SaveArgs{Content: docV1, Source: "notion://rate-limiter"})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	return tools, domain.PlanID(saved.Plan.ID)
}

// readIfPresent reads path, answering nil for a file that is not there. A
// missing store file and an empty one are the same fact for the comparison
// below -- "nothing has been written here" -- and os.ReadFile distinguishes them
// with an error the assertion has no use for.
func readIfPresent(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return b
}

// TestDownloadPersistsNothing pins that download records nothing: supplying a
// path changes nothing about what is persisted. The baseline is taken after a
// get_review by the same plan_id, so the comparison measures only what writing
// the file adds.
func TestDownloadPersistsNothing(t *testing.T) {
	f := setup(t)
	tools, planID := savedPlan(t, f)

	review, err := tools.GetReview(f.ctx, GetReviewArgs{PlanID: string(planID)})
	if err != nil {
		t.Fatalf("get_review: %v", err)
	}
	before := readIfPresent(t, f.statePath())
	if len(before) == 0 {
		t.Fatal("state.json is empty after save -- the assertion below would be vacuous")
	}

	out := filepath.Join(t.TempDir(), "worktree.md")
	res, err := tools.Download(f.ctx, DownloadArgs{PlanID: string(planID), Path: out})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if res.Hash != review.Hash {
		t.Fatalf("download wrote hash %s, get_review answered %s -- the two must resolve the same version", res.Hash, review.Hash)
	}
	if got := readIfPresent(t, f.statePath()); !bytes.Equal(got, before) {
		t.Errorf("state.json changed across the download:\nbefore: %s\nafter:  %s\n"+
			"download is a read that produces a file -- supplying a path must change nothing about what is persisted",
			before, got)
	}
}

// TestADownloadedPathAddressedAsSourceForksASecondPlan PINS A DEFECT, NOT A
// RULING: the fork happens on the call AFTER download, so nothing download
// itself can assert reaches it.
//
// THE MECHANISM, EXACTLY. download answers an absolute Path, and an agent's
// natural next call is comment(source: <that path>). t.resolve sees a readable
// local file and takes session.Open's live-follow branch; nothing claims the
// path, so the session comes back with Exists false; ensurePlan then CREATES a
// plan for it, titled from the document's own H1 and carrying the downloaded
// path as its SourceHint. refuseSaveForReadableSource guards save alone --
// comment, comment_section and approve reach ensurePlan with nothing in the way.
//
// THE COST IS NOT AN EXTRA ROW IN A LIST, which is why this test reads BOTH ends
// rather than counting plans: the comment the agent believes it left on the plan
// is not on the plan. get_review by plan_id answers the real plan with ZERO
// threads; get_review by the downloaded path answers the fork holding the one
// thread.
//
// THE MITIGATION IS WORDS, and both are pinned: the download row in
// TestToolDescriptionsCoachAddressingAndRefusal and
// TestDownloadsPathSchemaSaysThePathIsNotAnAddress. The two remedies that would
// close it -- a refusal at the verbs that lazily create, for a path draftplane
// just wrote, or download remembering the path so t.resolve can route it back
// to the plan -- are a product decision not yet made. WHEN ONE LANDS, INVERT
// THIS TEST: its failure is the fix arriving, not a regression.
func TestADownloadedPathAddressedAsSourceForksASecondPlan(t *testing.T) {
	f := setup(t)
	tools, planID := savedPlan(t, f)

	dl, err := tools.Download(f.ctx, DownloadArgs{PlanID: string(planID), Path: filepath.Join(t.TempDir(), "worktree.md")})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	before, err := tools.ListPlans(f.ctx, ListPlansArgs{})
	if err != nil {
		t.Fatalf("list_plans: %v", err)
	}
	if len(before.Plans) != 1 || before.Plans[0].ID != string(planID) {
		t.Fatalf("before the second call there are %d plan(s) (%+v), want only %s -- this test's premise is that "+
			"download created nothing", len(before.Plans), before.Plans, planID)
	}

	// THE SECOND CALL. An agent that just downloaded the plan comments on what
	// it read, addressing it by the path it was handed.
	th, err := tools.Comment(f.ctx, CommentArgs{
		Source: dl.Path,
		Quote:  "Ship behind a feature flag",
		Body:   "Which internal users?",
	})
	if err != nil {
		t.Fatalf("comment at the downloaded path was refused: %v\n"+
			"IF THIS IS A REFUSAL SOMEBODY DELIBERATELY ADDED, that is the fix this test's doc comment refers to: "+
			"invert this test to assert the refusal, and say so here", err)
	}

	after, err := tools.ListPlans(f.ctx, ListPlansArgs{})
	if err != nil {
		t.Fatalf("list_plans: %v", err)
	}
	if len(after.Plans) != 2 {
		t.Fatalf("after the comment there are %d plan(s) (%+v), want the 2 this defect produces", len(after.Plans), after.Plans)
	}
	var fork PlanInfo
	for _, p := range after.Plans {
		if p.ID != string(planID) {
			fork = p
		}
	}
	if fork.SourceHint != dl.Path {
		t.Fatalf("the second plan is %+v, want a fresh plan whose SourceHint is the downloaded path %s", fork, dl.Path)
	}

	// BOTH ENDS. The plan the agent meant has nothing on it; the thread is on
	// the copy.
	onThePlan, err := tools.GetReview(f.ctx, GetReviewArgs{PlanID: string(planID)})
	if err != nil {
		t.Fatalf("get_review by plan_id: %v", err)
	}
	if onThePlan.Plan == nil || onThePlan.Plan.ID != string(planID) || len(onThePlan.Threads) != 0 {
		t.Errorf("get_review on %s answers %+v with %d thread(s), want the plan itself carrying NONE of the comment "+
			"the agent believed it left there", planID, onThePlan.Plan, len(onThePlan.Threads))
	}
	staged, err := tools.GetReview(f.ctx, GetReviewArgs{Source: dl.Path})
	if err != nil {
		t.Fatalf("get_review by the downloaded path: %v", err)
	}
	if staged.Plan == nil || staged.Plan.ID != fork.ID || len(staged.Threads) != 1 || staged.Threads[0].ID != th.ThreadID {
		t.Errorf("get_review on %s answers %+v with %d thread(s), want the forked plan %s holding thread %s -- "+
			"the copy is where the review fact landed", dl.Path, staged.Plan, len(staged.Threads), fork.ID, th.ThreadID)
	}
}

// ---- resolver / addressing ----

// TestVerbsSourceXorPlanID tables every verb over the source/plan_id XOR
// validation resolve() enforces: neither or both must coach "exactly one".
func TestVerbsSourceXorPlanID(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")
	downloadPath := filepath.Join(t.TempDir(), "worktree.md")

	verbs := []struct {
		name string
		call func(source, planID string) error
	}{
		{"get_review", func(source, planID string) error {
			_, err := tools.GetReview(f.ctx, GetReviewArgs{Source: source, PlanID: planID})
			return err
		}},
		{"comment", func(source, planID string) error {
			_, err := tools.Comment(f.ctx, CommentArgs{Source: source, PlanID: planID, Quote: "some exact quote here", Body: "x"})
			return err
		}},
		{"comment_section", func(source, planID string) error {
			_, err := tools.CommentSection(f.ctx, CommentSectionArgs{Source: source, PlanID: planID, HeadingPath: []string{"Doc", "Section"}, Body: "x"})
			return err
		}},
		{"reply", func(source, planID string) error {
			_, err := tools.Reply(f.ctx, ReplyArgs{Source: source, PlanID: planID, ThreadID: "thread-1", Body: "x"})
			return err
		}},
		{"resolve", func(source, planID string) error {
			_, err := tools.Resolve(f.ctx, ResolveArgs{Source: source, PlanID: planID, ThreadID: "thread-1", Resolved: true})
			return err
		}},
		{"rehome", func(source, planID string) error {
			_, err := tools.Rehome(f.ctx, RehomeArgs{Source: source, PlanID: planID, ThreadID: "thread-1", Quote: "some exact quote here"})
			return err
		}},
		{"approve", func(source, planID string) error {
			_, err := tools.Approve(f.ctx, ApproveArgs{Source: source, PlanID: planID})
			return err
		}},
		// download's path argument is deliberately a real, writable one: the
		// XOR is enforced by t.resolve, which runs before anything is created,
		// so a row that passed because the path was unusable would be proving
		// the wrong refusal. One path for both rows is safe for the same
		// reason -- neither call reaches the create.
		{"download", func(source, planID string) error {
			_, err := tools.Download(f.ctx, DownloadArgs{Source: source, PlanID: planID, Path: downloadPath})
			return err
		}},
	}

	for _, v := range verbs {
		t.Run(v.name+"/neither", func(t *testing.T) {
			err := v.call("", "")
			if err == nil || !strings.Contains(err.Error(), "exactly one") {
				t.Fatalf("err = %v, want mention of exactly one", err)
			}
		})
		t.Run(v.name+"/both", func(t *testing.T) {
			err := v.call("some-source", "plan-1")
			if err == nil || !strings.Contains(err.Error(), "exactly one") {
				t.Fatalf("err = %v, want mention of exactly one", err)
			}
		})
	}
}

// TestAnUnknownPlanIDIsCoachedAtEveryDoor pins coachOpenVersionErr's sentence
// at every verb that takes a plan_id: eight reach it through t.resolve's
// openByID, and save through its own planByID door. An id this machine has
// never minted must tell the agent where a real one comes from, whichever verb
// it was handed to.
func TestAnUnknownPlanIDIsCoachedAtEveryDoor(t *testing.T) {
	const unknown = "does-not-exist"
	const want = "plan_id must come from list_plans, get_review, or save"
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	for _, v := range []struct {
		name string
		call func() error
	}{
		{"get_review", func() error {
			_, err := tools.GetReview(f.ctx, GetReviewArgs{PlanID: unknown})
			return err
		}},
		{"comment", func() error {
			_, err := tools.Comment(f.ctx, CommentArgs{PlanID: unknown, Quote: "Ship behind a feature flag", Body: "x"})
			return err
		}},
		{"comment_section", func() error {
			_, err := tools.CommentSection(f.ctx, CommentSectionArgs{PlanID: unknown, HeadingPath: []string{"Rate Limiter Plan", "Rollout"}, Body: "x"})
			return err
		}},
		{"reply", func() error {
			_, err := tools.Reply(f.ctx, ReplyArgs{PlanID: unknown, ThreadID: "thread-1", Body: "x"})
			return err
		}},
		{"resolve", func() error {
			_, err := tools.Resolve(f.ctx, ResolveArgs{PlanID: unknown, ThreadID: "thread-1", Resolved: true})
			return err
		}},
		{"rehome", func() error {
			_, err := tools.Rehome(f.ctx, RehomeArgs{PlanID: unknown, ThreadID: "thread-1", Quote: "Ship behind a feature flag"})
			return err
		}},
		{"approve", func() error {
			_, err := tools.Approve(f.ctx, ApproveArgs{PlanID: unknown})
			return err
		}},
		{"save", func() error {
			_, err := tools.Save(f.ctx, SaveArgs{PlanID: unknown, Content: docV2})
			return err
		}},
		{"download", func() error {
			_, err := tools.Download(f.ctx, DownloadArgs{PlanID: unknown, Path: filepath.Join(t.TempDir(), "worktree.md")})
			return err
		}},
	} {
		t.Run(v.name, func(t *testing.T) {
			err := v.call()
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want it to contain %q", err, want)
			}
		})
	}
}

// TestVerbsByPlanID runs comment/reply/resolve/approve entirely by plan_id
// against a saved plan with a URL source: the full journey happens on snapshot
// content, with the comment's quote anchored against the exact bytes save
// supplied.
func TestVerbsByPlanID(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	saved, err := tools.Save(f.ctx, SaveArgs{Content: docV1, Source: "notion://by-plan-id"})
	if err != nil {
		t.Fatal(err)
	}
	planID := saved.Plan.ID

	th, err := tools.Comment(f.ctx, CommentArgs{
		PlanID: planID, Quote: "token bucket with a burst capacity of fifty requests", Body: "why fifty?",
	})
	if err != nil {
		t.Fatal(err)
	}
	replyRes, err := tools.Reply(f.ctx, ReplyArgs{PlanID: planID, ThreadID: string(th.ThreadID), Body: "ack"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Resolve(f.ctx, ResolveArgs{PlanID: planID, ThreadID: string(th.ThreadID), Resolved: true}); err != nil {
		t.Fatal(err)
	}
	approveRes, err := tools.Approve(f.ctx, ApproveArgs{PlanID: planID})
	if err != nil {
		t.Fatal(err)
	}
	if !approveRes.OK {
		t.Fatalf("approve = %+v", approveRes)
	}

	review, err := tools.GetReview(f.ctx, GetReviewArgs{PlanID: planID})
	if err != nil {
		t.Fatal(err)
	}
	rth := findThread(t, review, string(th.ThreadID))
	if !rth.Resolved {
		t.Fatal("thread should be resolved")
	}
	// Both comments carry no ActorLogin: Tools.attribute records only the
	// agent, so each Author renders as that comment's own minted agent
	// identity (see TestTwoActorJourney's identical note).
	wantCommentAuthor, wantReplyAuthor := "agent-"+th.AgentID, "agent-"+replyRes.AgentID
	if len(rth.Comments) != 2 || rth.Comments[0].Author != wantCommentAuthor || rth.Comments[1].Author != wantReplyAuthor {
		t.Fatalf("comments = %+v, want [%q, %q]", rth.Comments, wantCommentAuthor, wantReplyAuthor)
	}
	if !review.ApprovedCurrent {
		t.Fatal("approved_current should be true")
	}
}

// TestVerbUnknownSourceCoachesSave pins the resolver's terminal coaching: a
// source that is neither a readable local file nor a known plan tells the
// agent to save first.
func TestVerbUnknownSourceCoachesSave(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	_, err := tools.GetReview(f.ctx, GetReviewArgs{Source: "notion://does-not-exist"})
	if err == nil || !strings.Contains(err.Error(), "save") {
		t.Fatalf("err = %v, want coaching to save first", err)
	}
}

// TestFileURLNormalization pins localFile's file:// stripping: a plan
// created by a bare absolute path is the SAME plan when later addressed by
// its file:// URL.
func TestFileURLNormalization(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	th, err := tools.Comment(f.ctx, CommentArgs{
		Source: f.path, Quote: "token bucket with a burst capacity of fifty requests", Body: "why fifty?",
	})
	if err != nil {
		t.Fatal(err)
	}

	review, err := tools.GetReview(f.ctx, GetReviewArgs{Source: "file://" + f.path})
	if err != nil {
		t.Fatal(err)
	}
	if review.Plan == nil {
		t.Fatal("file:// URL should resolve to the same plan created by bare path")
	}
	_ = findThread(t, review, string(th.ThreadID))
}

// tipMover is a client.PlanService that advances the plan's tip once,
// immediately before delegating the next RegisterVersion. It stands in for a
// sibling process writing in the instant between a session establishing its
// base and using it — the only way to fail the compare-and-swap from outside
// the session, and therefore the only way to prove a verb relays the conflict
// rather than swallowing it.
type tipMover struct {
	client.PlanService
	sibling []byte
}

// arm makes the NEXT RegisterVersion be preceded by a sibling write of
// content.
func (m *tipMover) arm(content []byte) { m.sibling = content }

func (m *tipMover) RegisterVersion(ctx context.Context, id domain.PlanID, r client.VersionRegistration) (domain.VersionRef, error) {
	if m.sibling != nil {
		content := m.sibling
		m.sibling = nil
		versions, err := m.Versions(ctx, id)
		if err != nil {
			return domain.VersionRef{}, err
		}
		// Both RegisterVersion calls name the embedded field explicitly:
		// unqualified, they would recurse into this method.
		var tip domain.ContentHash
		if len(versions) > 0 {
			tip = versions[len(versions)-1].Hash
		}
		if _, err := m.PlanService.RegisterVersion(ctx, id, client.VersionRegistration{Content: content, Base: tip}); err != nil {
			return domain.VersionRef{}, err
		}
	}
	return m.PlanService.RegisterVersion(ctx, id, r)
}

// TestWriteVerbsRelayVersionConflicts enumerates every verb that can reach
// ensureRegistered, rather than the one that was found broken: rehome wrapped
// its error with %v, so errors.As could not recover the *client.ConflictError
// and the agent was coached about a thread_id that was never wrong — it would
// re-fetch thread ids in a loop — while comment, comment_section, approve and
// save all relayed it correctly. Four of five correct is exactly the shape that
// hides, so the property is asserted for every member of the set. This table's
// whole point is to be re-checked against the full set whenever the set grows,
// rather than trusting that a new verb inherited the property by resemblance.
//
// reply and resolve carry the identical %v wrap but record review facts without
// registering a version, so they cannot produce a conflict to lose and are
// deliberately absent here.
func TestWriteVerbsRelayVersionConflicts(t *testing.T) {
	const notionSource = "notion://rate-limiter"

	// Each subtest gets its own store, plan and file: a conflicted write
	// leaves the tip where the sibling put it, which would change what the
	// next subtest's session establishes as its base.
	newCase := func(t *testing.T) (fixture, *tipMover, *Tools, string) {
		t.Helper()
		f := setup(t)
		mover := &tipMover{PlanService: f.newStore(t)}
		tools := New(mover, "calm-mountain")

		// The local-file plan, seeded at docV1 with one thread for rehome.
		th, err := tools.Comment(f.ctx, CommentArgs{
			Source: f.path, Quote: "Ship behind a feature flag to internal users first.", Body: "which flag?",
		})
		if err != nil {
			t.Fatal(err)
		}
		// save refuses a source draftplane can read itself, so it needs its own.
		if _, err := tools.Save(f.ctx, SaveArgs{Content: docV1, Source: notionSource}); err != nil {
			t.Fatal(err)
		}
		// Revise the file. A session opening now carries content that is not
		// the tip, so the write verbs actually reach RegisterVersion instead
		// of short-circuiting on registered.
		if err := os.WriteFile(f.path, []byte(docV2), 0o644); err != nil {
			t.Fatal(err)
		}
		return f, mover, tools, th.ThreadID
	}

	verbs := []struct {
		name string
		call func(f fixture, tools *Tools, threadID string) error
	}{
		{"comment", func(f fixture, tools *Tools, _ string) error {
			_, err := tools.Comment(f.ctx, CommentArgs{
				Source: f.path, Quote: "burst capacity fifty is the chosen approach", Body: "still fifty?",
			})
			return err
		}},
		{"comment_section", func(f fixture, tools *Tools, _ string) error {
			_, err := tools.CommentSection(f.ctx, CommentSectionArgs{
				Source: f.path, HeadingPath: []string{"Rate Limiter Plan", "Design"}, Body: "section note",
			})
			return err
		}},
		{"rehome", func(f fixture, tools *Tools, threadID string) error {
			_, err := tools.Rehome(f.ctx, RehomeArgs{
				Source: f.path, ThreadID: threadID, Quote: "A token bucket limiter with burst capacity fifty",
			})
			return err
		}},
		{"approve", func(f fixture, tools *Tools, _ string) error {
			_, err := tools.Approve(f.ctx, ApproveArgs{Source: f.path})
			return err
		}},
		{"save", func(f fixture, tools *Tools, _ string) error {
			_, err := tools.Save(f.ctx, SaveArgs{Content: docV2, Source: notionSource})
			return err
		}},
	}

	for _, v := range verbs {
		t.Run(v.name, func(t *testing.T) {
			f, mover, tools, threadID := newCase(t)
			siblingContent := []byte("sibling content")
			mover.arm(siblingContent)

			err := v.call(f, tools, threadID)
			var conflict *client.ConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("err = %v, want a recoverable *client.ConflictError", err)
			}
			wantTip := domain.HashContent(siblingContent)
			if conflict.Tip.Hash != wantTip {
				t.Fatalf("conflict.Tip.Hash = %s, want %s", conflict.Tip.Hash, wantTip)
			}
			// The coaching names a mechanism instead of deferring to a
			// human: every MCP verb opens a fresh session on the current tip,
			// so a get_review call shows what landed and the retry itself is
			// the escape (see conflictCoaching's own comment). The message
			// must not point at `draftplane review`, a human-in-the-loop
			// escape an agent has no need of.
			for _, want := range []string{"get_review", "retry"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%s: err = %q, want it to mention %q", v.name, err.Error(), want)
				}
			}
			if strings.Contains(err.Error(), "draftplane review") {
				t.Errorf("%s: err = %q, points the agent at `draftplane review`, a human-in-the-loop escape it does not need", v.name, err.Error())
			}
		})
	}
}

// TestSourceFaultStateCoversAllFourStates measures sourceFaultState -- the one
// function every write verb in this file answers this field with -- against
// all four of session.SourceFileState's values, plus the no-fault case, rather
// than leaving three of the four to be inferred from gone alone. released
// cannot be reached on a settled
// filesystem through any verb, so this is the direct measurement that state's
// mapping gets instead of an inference.
func TestSourceFaultStateCoversAllFourStates(t *testing.T) {
	for _, st := range []session.SourceFileState{
		session.SourceFileGone, session.SourceFileUnreadable,
		session.SourceFileReleased, session.SourceFileClaimed,
	} {
		got := sourceFaultState(&session.Session{SourceFault: &session.SourceFileError{State: st}})
		if got != st.String() {
			t.Errorf("sourceFaultState(%v) = %q, want %q", st, got, st.String())
		}
	}
	if got := sourceFaultState(&session.Session{}); got != "" {
		t.Errorf("sourceFaultState(no fault) = %q, want \"\" -- absent, never a zero value that reads as a state", got)
	}
}

// TestReadReasonStripsThePathFromAPathError is dedicated unit
// coverage for the helper: a REAL *fs.PathError (os.Chmod(0o000)
// against a real file, not a hand-built stand-in) whose own Error() already
// renders "open <path>: permission denied" must come back with the path
// gone, because both of this function's callers (sourceFaultGuidance's and
// downloadSourceFaultGuidance's unreadable arms) already name the same path in
// the same parenthetical, one clause earlier.
func TestReadReasonStripsThePathFromAPathError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read a 0000 file, so this fixture would prove nothing")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "unreadable.md")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	_, readErr := os.ReadFile(path)
	if readErr == nil {
		t.Fatal("os.ReadFile succeeded against a 0000 file -- this fixture is not exercising the real bug")
	}
	var pe *fs.PathError
	if !errors.As(readErr, &pe) {
		t.Fatalf("os.ReadFile's error is not a *fs.PathError: %v (%T)", readErr, readErr)
	}
	if !strings.Contains(pe.Error(), path) {
		t.Fatalf("fixture invalid: %q does not contain %q -- this test would pass for the wrong reason", pe.Error(), path)
	}

	got := readReason(pe)
	if strings.Contains(got, path) {
		t.Fatalf("readReason(%v) = %q, still contains the path -- a caller naming the path beside this in one "+
			"parenthetical prints it twice", pe, got)
	}
	if got != pe.Err.Error() {
		t.Fatalf("readReason(%v) = %q, want the bare cause %q", pe, got, pe.Err.Error())
	}

	// The fallback: a cause that is not a *fs.PathError must still say
	// SOMETHING, rather than going silently empty.
	if got := readReason(errors.New("boom")); got != "boom" {
		t.Errorf("readReason(plain error) = %q, want the error's own message", got)
	}
}

// TestEveryToolAcceptsAndEchoesAgentID enumerates every registered tool: each
// must accept agent_id and echo it back on its own result -- not a sample of
// them. This codebase's signature defect is a rule proven for some members of a
// set and silently missing from another, so the table below is keyed by name and
// checked against ToolNames() (the same list server.go's registrations and
// cmd/draftplane's --list-tools both answer from) before any case runs: a tool
// added to server.go and forgotten here fails that check rather than being
// silently skipped. The name deliberately counts nothing; the WHOLE SET is the
// property, and the length check below asserts it from ToolNames() rather than
// from a word in the name.
//
// Each entry's func takes the running subtest's own *testing.T, for download's
// t.TempDir(): a failure inside a case must run against the goroutine actually
// executing it, never a captured outer t.
func TestEveryToolAcceptsAndEchoesAgentID(t *testing.T) {
	f := setup(t)
	tools := New(f.newStore(t), "calm-mountain")

	// Seed a plan and a thread so reply, resolve, rehome, approve, get_review
	// and list_plans all have something real to act on.
	seed, err := tools.Comment(f.ctx, CommentArgs{
		Source: f.path, Quote: "Ship behind a feature flag to internal users first.", Body: "seed",
	})
	if err != nil {
		t.Fatal(err)
	}

	// call returns the agent_id a tool's result echoed back for a given
	// agent_id argument (possibly empty). Every entry runs with a bare
	// context.Background() -- exactly what the MCP transport hands a real
	// call in production -- so nothing here can pass by inheriting
	// attribution the fixture's own f.ctx happens to carry.
	cases := map[string]func(t *testing.T, agentID string) (echoed string, err error){
		"list_plans": func(_ *testing.T, id string) (string, error) {
			res, err := tools.ListPlans(context.Background(), ListPlansArgs{AgentID: id})
			return res.AgentID, err
		},
		"get_review": func(_ *testing.T, id string) (string, error) {
			res, err := tools.GetReview(context.Background(), GetReviewArgs{Source: f.path, AgentID: id})
			return res.AgentID, err
		},
		"comment": func(_ *testing.T, id string) (string, error) {
			res, err := tools.Comment(context.Background(), CommentArgs{
				Source: f.path, Quote: "We will use a token bucket with a burst capacity of fifty requests.",
				Body: "x", AgentID: id,
			})
			return res.AgentID, err
		},
		"comment_section": func(_ *testing.T, id string) (string, error) {
			res, err := tools.CommentSection(context.Background(), CommentSectionArgs{
				Source: f.path, HeadingPath: []string{"Rate Limiter Plan", "Rollout"}, Body: "x", AgentID: id,
			})
			return res.AgentID, err
		},
		"reply": func(_ *testing.T, id string) (string, error) {
			res, err := tools.Reply(context.Background(), ReplyArgs{
				Source: f.path, ThreadID: seed.ThreadID, Body: "x", AgentID: id,
			})
			return res.AgentID, err
		},
		"resolve": func(_ *testing.T, id string) (string, error) {
			res, err := tools.Resolve(context.Background(), ResolveArgs{
				Source: f.path, ThreadID: seed.ThreadID, Resolved: true, AgentID: id,
			})
			return res.AgentID, err
		},
		"rehome": func(_ *testing.T, id string) (string, error) {
			res, err := tools.Rehome(context.Background(), RehomeArgs{
				Source: f.path, ThreadID: seed.ThreadID,
				Quote: "Requests are currently unbounded and the database suffers under load spikes.", AgentID: id,
			})
			return res.AgentID, err
		},
		"approve": func(_ *testing.T, id string) (string, error) {
			res, err := tools.Approve(context.Background(), ApproveArgs{Source: f.path, AgentID: id})
			return res.AgentID, err
		},
		"save": func(_ *testing.T, id string) (string, error) {
			res, err := tools.Save(context.Background(), SaveArgs{
				Content: docV1, Source: "notion://agent-id-enumeration", AgentID: id,
			})
			return res.AgentID, err
		},
		// download needs a plan draftplane holds the bytes OF -- f.path is a
		// readable local file, which download refuses by its own gate -- so this
		// case saves a supplied-content plan first and downloads that. The save
		// is idempotent across this case's two invocations (same source, same
		// content), and t.TempDir() is unique per call, so neither invocation
		// can collide with the other over the file it writes.
		"download": func(t *testing.T, id string) (string, error) {
			saved, err := tools.Save(context.Background(), SaveArgs{
				Content: docV1, Source: "notion://download-agent-id-enumeration",
			})
			if err != nil {
				return "", err
			}
			res, err := tools.Download(context.Background(), DownloadArgs{
				PlanID: saved.Plan.ID, Path: filepath.Join(t.TempDir(), "downloaded.md"), AgentID: id,
			})
			return res.AgentID, err
		},
	}

	names := ToolNames()
	if len(cases) != len(names) {
		t.Fatalf("table covers %d tools, want %d (ToolNames): keep this table in sync with server.go's registrations", len(cases), len(names))
	}
	for _, name := range names {
		if _, ok := cases[name]; !ok {
			t.Fatalf("tool %q is registered in server.go but missing from this table", name)
		}
	}

	for _, name := range names {
		call := cases[name]
		t.Run(name+"/mints_when_absent", func(t *testing.T) {
			got, err := call(t, "")
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if _, ok := identity.Canonical(got); !ok {
				t.Fatalf("%s: agent_id = %q, want a validly-shaped minted identity", name, got)
			}
		})
		t.Run(name+"/echoes_a_supplied_identity", func(t *testing.T) {
			const supplied = "amber-otter-42"
			got, err := call(t, supplied)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if got != supplied {
				t.Fatalf("%s: agent_id = %q, want %q echoed back unchanged", name, got, supplied)
			}
		})
	}
}

// clipRunes truncates s to at most n runes, ending with … when clipped, so a
// failure message quoting a whole document stays readable.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

package main

// This file drives the human-and-agent loop and the two-agent loop against a
// REAL `draftplane mcp` subprocess over real stdio JSON-RPC (never mcptools
// called in-process) for the agent half, and against buildDeps' own production
// wiring -- the same session/service construction the TUI itself uses -- for
// the human half, since the TUI's keystroke loop cannot be driven headlessly.
// What it proves is the human-and-agent loop and the two-agent loop end to
// end: an agent authoring and commenting over a real `draftplane mcp`
// subprocess, a second agent reading as a distinct voice, and the human half
// reviewing through the same production wiring the TUI uses.
//
// It invents its own machine rather than running as the developer -- a harness
// invents its own scaffolding, including its user(s), rather than polluting a
// real environment -- through freshMachine / newFreshMachine /
// setMachineEnv (machine_harness_test.go): its own XDG_CONFIG_HOME and
// XDG_DATA_HOME under t.TempDir(), created empty -- which is what makes the
// "user(s)" half structural rather than aspirational: config.MachinePseudonym
// MINTS a pseudonym into a config.json that has none, so the name every human
// write below renders as is this run's own, minted into this run's own config,
// never the developer's. assertOwnMachine checks both halves of that
// instead of trusting them.
//
// And the guard, which is that rule written as a check rather than as prose:
// guardRealState (machine_harness_test.go, CALLED and not copied) fingerprints
// both real XDG roots -- read before any t.Setenv -- and re-checks them in a
// cleanup registered FIRST from the parent so it runs LAST.
//
// The gate stays for the one reason it has: this drive compiles a binary with
// `go build` and runs it as a real subprocess, which is not a cost `go test
// ./...` and `make check` should pay on every invocation. It does not mean
// EXCLUDED FROM THE RUNNABLE SET; it is run, by name.
//
// Run it with:
//
//	DRAFTPLANE_DOGFOOD=1 go test ./cmd/draftplane -run TestReviewLoopDogfood -v

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/client/config"
	"github.com/draftplane/draftplane/client/identity"
	"github.com/draftplane/draftplane/client/localfs"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/mcptools"
	"github.com/draftplane/draftplane/session"
	"github.com/draftplane/draftplane/ui"
)

// envDogfoodGate is this harness's gate, DECLARED rather than spelled inline so
// an enumeration built from this package's gate constants finds it -- a gate
// only a string literal names is a harness that silently skips.
const envDogfoodGate = "DRAFTPLANE_DOGFOOD"

func requireDogfood(t *testing.T) {
	t.Helper()
	if os.Getenv(envDogfoodGate) == "" {
		t.Skip("set DRAFTPLANE_DOGFOOD=1 to run this drive: it builds this repo's own draftplane binary and speaks to it as a real `draftplane mcp` subprocess, which is why it is excluded from `go test ./...`. It drives entirely under its own t.TempDir()-rooted XDG state (see this file's own doc comment) and guardRealState fails it if this machine's real draftplane directories move.")
	}
}

// buildDogfoodBinary compiles the real cmd/draftplane binary the agent half
// must speak to over stdio -- never mcptools called in-process. Built fresh
// into a temp dir so this test never depends on (or clobbers) ./bin/draftplane.
//
// ⚠️ `go test -overlay` CANNOT REACH THIS BINARY AND A MUTATED RUN PRINTS PASS.
// -overlay is a flag on the test build; this exec.Command does not carry it, so
// the subprocess compiles from the real, unmutated tree. Mutating client source
// under -overlay to check that a harness built through here really asserts
// something fails GREEN -- byte-identical to a real pass, the same shape as a
// misspelled gate silently skipping. (A test that drives the client in-process
// is where -overlay does work.) What works here: copy the tree out instead of
// overlaying it -- `git archive HEAD | tar -x` into scratch, mutate the copy,
// and run the harness from there.
func buildDogfoodBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "draftplane-dogfood")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building draftplane binary: %v\n%s", err, out)
	}
	return bin
}

// connectDogfoodMCP starts bin as a real `draftplane mcp` subprocess and
// connects a real MCP client to it over stdio JSON-RPC.
//
// It inherits this process's environment unchanged, which is exactly how the
// agent half comes to open the identical on-disk store the human half's
// buildDeps() call resolves to: setMachineEnv runs BEFORE this, and
// t.Setenv writes the process environment, so the XDG_CONFIG_HOME/XDG_DATA_HOME
// this subprocess picks up are the caller's own t.TempDir() roots. The ordering
// is load-bearing in one direction only and TestReviewLoopDogfood keeps it:
// nothing may connect before the machine is set.
//
// That the two halves really do share one store is not asserted separately
// because the human-and-agent loop already proves it end to end -- the human
// half posts two comments through buildDeps' wiring and the agent half reads
// them back over this connection, where a split store would answer zero
// threads.
func connectDogfoodMCP(t *testing.T, bin string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "dogfood-client", Version: "0.0.1"}, nil)
	transport := &mcp.CommandTransport{Command: exec.Command(bin, "mcp")}
	cs, err := c.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("connecting to real draftplane mcp subprocess: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// callTool places one real tool call over cs's live stdio connection and
// decodes the structured result into Out, logging the exact request and
// response JSON.
func callTool[Out any](t *testing.T, cs *mcp.ClientSession, name string, args any) Out {
	t.Helper()
	reqJSON, err := json.MarshalIndent(args, "", "  ")
	if err != nil {
		t.Fatalf("%s: marshaling args: %v", name, err)
	}
	t.Logf("--> %s %s", name, reqJSON)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: transport error: %v", name, err)
	}
	if res.IsError {
		var msg strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msg.WriteString(tc.Text)
			}
		}
		t.Fatalf("%s: tool returned an error result: %s", name, msg.String())
	}

	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("%s: marshaling structured content: %v", name, err)
	}
	t.Logf("<-- %s", raw)

	var out Out
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: decoding structured content into %T: %v\nraw=%s", name, out, err, raw)
	}
	return out
}

// writePlanFile is "the agent writes the plan to a file on disk" -- the
// human-and-agent loop and the two-agent loop both open with this, before
// draftplane has ever seen the plan.
func writePlanFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing plan file: %v", err)
	}
	return path
}

// renderPlanText reproduces app/model.go's RefreshFromSession + rerender
// pipeline (ParseBlocks, Placements, ResolveAnchor, BadgeFor, RenderDoc)
// against a fresh session opened the same way buildDeps' svc would open it for
// the TUI, with every placed thread expanded -- the same projection a human
// reviewing this plan in draftplane would see. ANSI is stripped so the result
// is safe to paste as plain text -- and it is a WHOLE PAINTED CANVAS being
// stripped, not the odd cursor or rule escape: a nil *Styles is the DEFAULT
// THEME'S Styles (see ui.RenderDoc), which is the only rendering there now is.
//
// THE TWO NILS BELOW ARE NOT THE SAME NIL. ParseBlocks' leaves both projections
// empty and displayIn falls back to the normalized source, which is what a
// caller wanting the plan's own text wants; RenderDoc's selects the default
// theme. Only the second one ever meant "unpainted".
//
// This is a CLOSE reproduction, not an exact one. app.project no longer asks
// ui.ResolveAnchor where a placement goes: a placement that reported a window
// is drawn on the block of greatest byte overlap (app.placedBlock), which is a
// different answer for a fuzzy match and for a quote crossing a block
// boundary. The loop below still asks ResolveAnchor, and cannot do otherwise --
// placedBlock, blockOfGreatestOverlap and project are unexported and this is
// package main -- so a plan whose threads cross block boundaries will differ
// from the TUI here. The loops this harness drives anchor whole blocks, so
// no case in it is affected; a rendering taken from it should not be read as
// the screen for one that is not.
func renderPlanText(t *testing.T, svc client.PlanService, path, pseudonym string) string {
	t.Helper()
	ctx := context.Background()
	s, err := session.Open(ctx, svc, path)
	if err != nil {
		t.Fatalf("renderPlanText: session.Open: %v", err)
	}
	blocks := ui.ParseBlocks(s.Content, nil)
	views := map[int][]ui.ThreadView{}
	var unanchored []ui.ThreadView
	expanded := map[int]bool{}

	placements, err := s.Placements(ctx)
	if err != nil {
		t.Fatalf("renderPlanText: Placements: %v", err)
	}
	for _, p := range placements {
		if p.Status == "orphaned" {
			unanchored = append(unanchored, ui.ThreadView{Thread: p.Thread})
			continue
		}
		idx := ui.ResolveAnchor(blocks, p.Anchor)
		if idx < 0 {
			unanchored = append(unanchored, ui.ThreadView{Thread: p.Thread})
			continue
		}
		views[idx] = append(views[idx], ui.ThreadView{Thread: p.Thread, Badge: ui.BadgeFor(p), Placed: true})
		expanded[idx] = true
	}

	lines := ui.RenderDoc(blocks, views, unanchored, expanded, ui.OnLine(-1), 100, pseudonym, nil)
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(ansi.Strip(l.Text))
		b.WriteString("\n")
	}
	return b.String()
}

// dumpStoreJSON reads statePath directly off disk (not through any
// PlanService) so what is logged is what actually landed in the store, not a
// transcript of the commands that were run against it.
func dumpStoreJSON(t *testing.T, statePath string) string {
	t.Helper()
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("reading state.json directly: %v", err)
	}
	var pretty any
	if err := json.Unmarshal(data, &pretty); err != nil {
		t.Fatalf("state.json did not parse as JSON: %v", err)
	}
	out, err := json.MarshalIndent(pretty, "", "  ")
	if err != nil {
		t.Fatalf("re-marshaling state.json: %v", err)
	}
	return string(out)
}

// assertOwnMachine proves the scaffolding actually took, rather than
// assuming it: buildDeps resolved its state.json UNDER m's own data root, and
// the pseudonym it answered was minted into m's own config.json by this run.
//
// config.MachinePseudonym mints a pair into a config.json that carries none and
// saves it, so a machine whose config root is a fresh t.TempDir() cannot borrow
// the developer's name -- it has to make one. Reading it back off m's own disk
// distinguishes "minted here" from "inherited from somewhere and coincidentally
// non-empty".
func assertOwnMachine(t *testing.T, m freshMachine, statePath, pseudonym string) {
	t.Helper()
	if !strings.HasPrefix(statePath, m.dataHome+string(os.PathSeparator)) {
		t.Fatalf("buildDeps resolved statePath=%s, which is NOT under this harness's own data root %s -- the drive would be writing somebody else's store", statePath, m.dataHome)
	}
	cfgPath := filepath.Join(m.configHome, "draftplane", "config.json")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("reading this harness's own config at %s: %v", cfgPath, err)
	}
	if cfg.MachinePseudonym == "" || cfg.MachinePseudonym != pseudonym {
		t.Fatalf("buildDeps answered pseudonym %q but this harness's own config at %s holds %q -- the pseudonym was not minted into this run's machine", pseudonym, cfgPath, cfg.MachinePseudonym)
	}
	t.Logf("this harness's own machine: dataHome=%s configHome=%s, pseudonym %q minted into its own config.json", m.dataHome, m.configHome, pseudonym)
}

// TestReviewLoopDogfood drives the human-and-agent loop and the two-agent loop
// end to end, plus the agent-identity mechanism, all through one real
// `draftplane mcp` subprocess -- on a machine this test invents rather than on
// the developer's.
//
// ORDER IS LOAD-BEARING in three places, all of them in the first four
// statements. guardRealState is registered FIRST so its cleanup runs LAST
// (t.Cleanup is LIFO) and so it reads the real roots BEFORE any t.Setenv has
// moved them. buildDogfoodBinary runs BEFORE setMachineEnv, because `go
// build` reads its own env file out of os.UserConfigDir() -- which is
// XDG_CONFIG_HOME on Linux -- and has no business being redirected into this
// test's temp root. And nothing may connect a subprocess before
// setMachineEnv, since inheriting the process environment is how the agent
// half lands on the same store.
func TestReviewLoopDogfood(t *testing.T) {
	requireDogfood(t)
	guardRealState(t)

	bin := buildDogfoodBinary(t)
	t.Logf("built real draftplane binary at %s", bin)

	machine := newFreshMachine(t)
	setMachineEnv(t, machine)

	cs := connectDogfoodMCP(t, bin)
	t.Logf("connected a real MCP client to a real `draftplane mcp` subprocess (pid via os/exec, stdio JSON-RPC)")

	planDir := t.TempDir()

	t.Run("HumanAgentLoop", func(t *testing.T) { testHumanAgentLoop(t, cs, planDir, machine) })
	t.Run("TwoAgentLoop", func(t *testing.T) { testTwoAgentLoop(t, cs, planDir, machine) })
	t.Run("IdentityMechanism", func(t *testing.T) { testIdentityMechanism(t, cs) })
	// save needs no local file at all (its whole point is a source draftplane
	// cannot read itself), so it cannot entangle with either loop's state.
	t.Run("Save", func(t *testing.T) { testSaveVerb(t, cs) })
}

// findThreadByID finds a ReviewThread by id in a get_review/save result's
// Threads slice, failing the test rather than returning a zero value so a
// caller's next assertion fails at the right line instead of a confusing one.
func findThreadByID(t *testing.T, threads []mcptools.ReviewThread, id string) mcptools.ReviewThread {
	t.Helper()
	for _, th := range threads {
		if th.ID == id {
			return th
		}
	}
	t.Fatalf("thread %q not found in %+v", id, threads)
	return mcptools.ReviewThread{}
}

const humanAgentDoc = `# Cache Warming Plan

## Context

The homepage currently suffers a cold-cache penalty on every deploy, because the CDN edge nodes evict all cached fragments the instant a new build ships. Users hitting the site in the first few minutes after a deploy see full-latency renders.

## Design

We will add a warming step to the deploy pipeline that requests the top twenty pages from each edge region before traffic is cut over. The warming step runs with a five minute timeout and is non-blocking: a warming failure logs a warning but never blocks the deploy.

## Rollout

Ship behind a feature flag to the internal staging region first, then expand to all regions after one week of clean metrics.
`

// testHumanAgentLoop drives the human-and-agent loop: a human comments in the
// (headless-driven) TUI wiring, a real agent over real MCP replies and edits
// the file, the human resolves. Verification happens in the caller via the
// store dump and rendered output logged here.
func testHumanAgentLoop(t *testing.T, cs *mcp.ClientSession, dir string, machine freshMachine) {
	path := writePlanFile(t, dir, "human-agent-plan.md", humanAgentDoc)
	t.Logf("human-and-agent loop plan written to %s", path)

	// --- human half: buildDeps' own wiring, not a reimplementation. ---
	svc, _, statePath, pseudonym, err := buildDeps()
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	assertOwnMachine(t, machine, statePath, pseudonym)
	t.Logf("buildDeps(): statePath=%s pseudonym=%s (every human write below carries an empty attribution and renders as user-%s)", statePath, pseudonym, pseudonym)

	// Decorated with the empty attribution runReview and runList hand app,
	// which is what app stamps on every context it originates
	// (app.attributedCtx) -- the human half of this harness stands in for the
	// TUI, so it has to carry attribution the way the TUI does rather than the
	// way nothing does.
	ctx := client.WithAttribution(context.Background(), domain.Attribution{})
	human, err := session.Open(ctx, svc, path)
	if err != nil {
		t.Fatalf("session.Open (human): %v", err)
	}
	// Lazy plan creation, exactly what the TUI's own comment action does
	// (app/actions.go: "comment on a plan-less file creates the plan first,
	// with its inferred title") -- session.Comment alone does not create a
	// plan for a session that has never seen one.
	if !human.Exists {
		if err := human.Create(ctx, ui.InferTitle(ui.ParseBlocks(human.Content, nil), human.Path)); err != nil {
			t.Fatalf("lazily creating plan for the human-and-agent loop: %v", err)
		}
	}

	designAnchor, err := session.AnchorForBlockText(string(human.Content), "requests the top twenty pages from each edge region", nil)
	if err != nil {
		t.Fatalf("anchoring comment 1: %v", err)
	}
	designThread, err := human.Comment(ctx, designAnchor, "Should this be configurable per region, or hardcoded to twenty everywhere?")
	if err != nil {
		t.Fatalf("human comment 1: %v", err)
	}
	t.Logf("human comment 1 -> thread %s", designThread.ID)

	rolloutAnchor, err := session.AnchorForBlockText(string(human.Content), "feature flag to the internal staging region first", nil)
	if err != nil {
		t.Fatalf("anchoring comment 2: %v", err)
	}
	rolloutThread, err := human.Comment(ctx, rolloutAnchor, "Which flag system are we using for this -- LaunchDarkly, or the homegrown one?")
	if err != nil {
		t.Fatalf("human comment 2: %v", err)
	}
	t.Logf("human comment 2 -> thread %s", rolloutThread.ID)

	// --- agent half: a REAL draftplane mcp subprocess, real stdio JSON-RPC. ---
	review := callTool[mcptools.GetReviewResult](t, cs, "get_review", mcptools.GetReviewArgs{Source: path})
	if len(review.Threads) != 2 {
		t.Fatalf("get_review threads = %d, want 2", len(review.Threads))
	}
	for _, th := range review.Threads {
		if th.Comments[0].Author != "user-"+pseudonym {
			t.Fatalf("thread %s author = %q, want %q (the human's comments must read as the machine pseudonym)", th.ID, th.Comments[0].Author, "user-"+pseudonym)
		}
	}

	reply1 := callTool[mcptools.OKResult](t, cs, "reply", mcptools.ReplyArgs{
		Source: path, ThreadID: string(designThread.ID),
		Body: "Making it configurable per region -- some edges see far less traffic than others and don't need all twenty warmed.",
	})
	agentID := reply1.AgentID
	if agentID == "" {
		t.Fatal("agent's first reply carried no agent_id")
	}
	t.Logf("agent minted identity on its first call: agent-%s", agentID)

	reply2 := callTool[mcptools.OKResult](t, cs, "reply", mcptools.ReplyArgs{
		Source: path, ThreadID: string(rolloutThread.ID), AgentID: agentID,
		Body: "Using our homegrown flag service -- added a note in Rollout.",
	})
	if reply2.AgentID != agentID {
		t.Fatalf("agent's second reply came back with agent_id %q, want the carried %q", reply2.AgentID, agentID)
	}
	t.Logf("agent carried its identity across both replies: agent-%s", agentID)

	// approve runs over the real subprocess because mcptools.Tools.Approve
	// decorates its context through the per-call mint/carry path
	// (Tools.attribute), never through the one fixed attribution the human half
	// above stamps, and that is exactly the class of gap this harness exists to
	// catch. Carrying the agent's own identity here, not omitting it, proves the
	// CARRY path specifically, not just mint-on-approve.
	approveRes := callTool[mcptools.ApproveResult](t, cs, "approve", mcptools.ApproveArgs{
		Source: path, AgentID: agentID,
	})
	if approveRes.AgentID != agentID {
		t.Fatalf("agent's approve came back with agent_id %q, want the carried %q", approveRes.AgentID, agentID)
	}
	if approveRes.Hash == "" {
		t.Fatal("approve returned no hash")
	}
	t.Logf("agent approved over MCP, carrying its identity: agent-%s (hash %s)", agentID, approveRes.Hash)

	// Verify from the store, not from the transcript above: read the
	// Approval back through *localfs.Store (a fresh instance, same
	// statePath) and confirm its Attribution.Agent is the agent's own
	// carried identity -- proof that the agent path stamped what the human
	// path (one fixed, empty attribution for the whole TUI process, and no
	// agent at all) structurally cannot.
	verifyStore, err := localfs.New(statePath)
	if err != nil {
		t.Fatalf("localfs.New for approval verification: %v", err)
	}
	approvals, err := verifyStore.Approvals(ctx, domain.PlanID(review.Plan.ID))
	if err != nil {
		t.Fatalf("reading approvals from the store: %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("approvals for the human-and-agent loop's plan = %d, want 1", len(approvals))
	}
	if approvals[0].Attribution.Agent != agentID {
		t.Fatalf("VERIFY-FROM-STORE FAILED: stored Approval.Attribution = %+v, want Agent = %q", approvals[0].Attribution, agentID)
	}
	if approvals[0].Hash != domain.ContentHash(approveRes.Hash) {
		t.Fatalf("stored approval hash = %q, want %q", approvals[0].Hash, approveRes.Hash)
	}
	t.Logf("VERIFIED FROM THE STORE: Approval.Attribution = %+v", approvals[0].Attribution)

	// comment_section over the real subprocess: same plan, same carried
	// identity, one more real call. Left unresolved deliberately (the human
	// resolve loop below only resolves the two threads the human raised above,
	// designThread and rolloutThread); it shows up open in the final render.
	sectionThread := callTool[mcptools.ThreadResult](t, cs, "comment_section", mcptools.CommentSectionArgs{
		Source: path, HeadingPath: []string{"Cache Warming Plan", "Context"}, AgentID: agentID,
		Body: "Worth a one-line pointer to the incident that prompted this, for future readers.",
	})
	if sectionThread.AgentID != agentID {
		t.Fatalf("agent's comment_section came back with agent_id %q, want the carried %q", sectionThread.AgentID, agentID)
	}
	sectionReview := callTool[mcptools.GetReviewResult](t, cs, "get_review", mcptools.GetReviewArgs{Source: path})
	sectionTh := findThreadByID(t, sectionReview.Threads, sectionThread.ThreadID)
	if !sectionTh.Section {
		t.Fatalf("comment_section's thread has Section=false, want true: %+v", sectionTh)
	}
	t.Logf("agent's comment_section landed as a section comment (section:true), carrying agent-%s", agentID)

	// The agent edits the file with its own file tools -- draftplane never
	// touches the file itself.
	edited := strings.Replace(humanAgentDoc,
		"requests the top twenty pages from each edge region before traffic is cut over.",
		"requests the top twenty pages from each edge region before traffic is cut over, with the count configurable per region.",
		1)
	edited = strings.Replace(edited,
		"Ship behind a feature flag to the internal staging region first,",
		"Ship behind a feature flag (our homegrown flag service) to the internal staging region first,",
		1)
	if edited == humanAgentDoc {
		t.Fatal("edit did not change the document -- quotes drifted from the fixture")
	}
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatalf("agent editing plan file: %v", err)
	}
	t.Logf("agent edited %s on disk", path)

	// --- human returns: reload (a fresh session.Open, exactly what the
	// TUI's fsnotify-triggered runReload does for a file-backed session)
	// and sees the plan has changed, then resolves both threads. ---
	human, err = session.Open(ctx, svc, path)
	if err != nil {
		t.Fatalf("human reload: %v", err)
	}
	if string(human.Content) != edited {
		t.Fatal("human's reloaded session does not reflect the agent's on-disk edit")
	}
	if err := human.Resolve(ctx, designThread.ID, true); err != nil {
		t.Fatalf("human resolve 1: %v", err)
	}
	if err := human.Resolve(ctx, rolloutThread.ID, true); err != nil {
		t.Fatalf("human resolve 2: %v", err)
	}
	t.Logf("human resolved both threads")

	rendered := renderPlanText(t, svc, path, pseudonym)
	t.Logf("human-and-agent loop rendered plan:\n%s", rendered)

	dump := dumpStoreJSON(t, statePath)
	t.Logf("human-and-agent loop store dump (state.json):\n%s", dump)
}

const twoAgentDoc = `# Search Reindex Plan

## Context

The search index falls behind the primary database by up to ten minutes under peak write load, because reindexing is triggered synchronously from the write path and gets starved by slower requests ahead of it in the queue.

## Design

We will move reindexing onto a dedicated background queue consumed by two worker processes, decoupled entirely from the write path's request lifecycle. A write enqueues a reindex job and returns immediately; the workers drain the queue independently.

## Rollout

Deploy the worker processes first, run them in shadow mode against the existing synchronous path for 48 hours, then cut the write path over to enqueue-only.
`

// testTwoAgentLoop drives the two-agent loop: two subagents comment over MCP
// without ever exchanging identities, a coordinator (also over MCP) reads
// both, accepts one and rejects the other, replies in both threads, edits the
// file, and resolves the accepted one -- all through the SAME `draftplane mcp`
// process cs is already connected to. The human-and-agent loop's human half
// then follows on the same plan.
func testTwoAgentLoop(t *testing.T, cs *mcp.ClientSession, dir string, machine freshMachine) {
	path := writePlanFile(t, dir, "two-agent-plan.md", twoAgentDoc)
	t.Logf("two-agent loop plan written to %s", path)

	// --- subagent s1: its very first draftplane call, no agent_id supplied. ---
	s1Comment := callTool[mcptools.ThreadResult](t, cs, "comment", mcptools.CommentArgs{
		Source: path, Quote: "consumed by two worker processes",
		Body: "Two workers might not be enough at peak -- can we make the worker count configurable instead of hardcoding it to two?",
	})
	s1ID := s1Comment.AgentID
	if s1ID == "" {
		t.Fatal("subagent s1 got no agent_id on its first call")
	}
	t.Logf("subagent s1 minted: agent-%s, thread %s", s1ID, s1Comment.ThreadID)

	// --- subagent s2: also its very first call, independently minted --
	// s1 and s2 never exchange identities with each other. ---
	s2Comment := callTool[mcptools.ThreadResult](t, cs, "comment", mcptools.CommentArgs{
		Source: path, Quote: "shadow mode against the existing synchronous path for 48 hours",
		Body: "48 hours seems short for shadow mode on something touching search consistency -- recommend at least a full week including a weekend traffic pattern.",
	})
	s2ID := s2Comment.AgentID
	if s2ID == "" {
		t.Fatal("subagent s2 got no agent_id on its first call")
	}
	t.Logf("subagent s2 minted: agent-%s, thread %s", s2ID, s2Comment.ThreadID)

	if s1ID == s2ID {
		t.Fatalf("subagents s1 and s2 minted the SAME identity (%s) -- they must stay distinct with no exchange", s1ID)
	}

	// --- coordinator: its own first call, independently minted; reads
	// both subagents' comments over the SAME mcp process. ---
	review := callTool[mcptools.GetReviewResult](t, cs, "get_review", mcptools.GetReviewArgs{Source: path})
	coordID := review.AgentID
	if coordID == "" || coordID == s1ID || coordID == s2ID {
		t.Fatalf("coordinator identity = %q, want a third identity distinct from s1=%s and s2=%s", coordID, s1ID, s2ID)
	}
	t.Logf("coordinator minted: agent-%s", coordID)

	var sawS1Author, sawS2Author bool
	for _, th := range review.Threads {
		author := th.Comments[0].Author
		switch th.ID {
		case s1Comment.ThreadID:
			sawS1Author = author == "agent-"+s1ID
		case s2Comment.ThreadID:
			sawS2Author = author == "agent-"+s2ID
		}
	}
	if !sawS1Author || !sawS2Author {
		t.Fatalf("coordinator's get_review did not render s1 and s2 as distinct authors: %+v", review.Threads)
	}
	t.Logf("coordinator's get_review renders s1 and s2 as two distinct authors, confirmed by author string")

	// Coordinator accepts s1's comment (worker count), rejects s2's
	// (shadow window), replying in both threads and carrying its own
	// identity across every call it makes from here on.
	acceptReply := callTool[mcptools.OKResult](t, cs, "reply", mcptools.ReplyArgs{
		Source: path, ThreadID: string(s1Comment.ThreadID), AgentID: coordID,
		Body: "Agreed -- making the worker count configurable via an env var, defaulting to two.",
	})
	if acceptReply.AgentID != coordID {
		t.Fatalf("coordinator's accept reply agent_id = %q, want carried %q", acceptReply.AgentID, coordID)
	}
	if _, err := callToolErr(t, cs, "resolve", mcptools.ResolveArgs{
		Source: path, ThreadID: string(s1Comment.ThreadID), Resolved: true, AgentID: coordID,
	}); err != nil {
		t.Fatalf("coordinator resolving s1's thread: %v", err)
	}

	rejectReply := callTool[mcptools.OKResult](t, cs, "reply", mcptools.ReplyArgs{
		Source: path, ThreadID: string(s2Comment.ThreadID), AgentID: coordID,
		Body: "Keeping 48 hours -- this mirrors our existing incident-response SLA window, and a full week would push the cutover past the deadline. Leaving this open for the human to weigh in.",
	})
	if rejectReply.AgentID != coordID {
		t.Fatalf("coordinator's reject reply agent_id = %q, want carried %q", rejectReply.AgentID, coordID)
	}
	t.Logf("coordinator accepted s1's thread (replied + resolved) and rejected s2's thread (replied, left open)")

	// Coordinator edits the file with its own file tools.
	edited := strings.Replace(twoAgentDoc,
		"consumed by two worker processes,",
		"consumed by a configurable number of worker processes (two by default),",
		1)
	if edited == twoAgentDoc {
		t.Fatal("coordinator's edit did not change the document -- quote drifted from the fixture")
	}
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatalf("coordinator editing plan file: %v", err)
	}
	t.Logf("coordinator edited %s on disk", path)

	// --- THE HEADLINE CLAIM: render the plan now, before any human
	// involvement, and confirm it reads as three distinct voices. ---
	svc, _, statePath, pseudonym, err := buildDeps()
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	assertOwnMachine(t, machine, statePath, pseudonym)
	threeVoiceRender := renderPlanText(t, svc, path, pseudonym)
	t.Logf("two-agent loop rendered plan AFTER the coordinator+two-subagents phase (the three-voices claim):\n%s", threeVoiceRender)
	for _, want := range []string{"agent-" + s1ID, "agent-" + s2ID, "agent-" + coordID} {
		if !strings.Contains(threeVoiceRender, want) {
			t.Fatalf("rendered plan does not contain %q -- three-voices claim not confirmed:\n%s", want, threeVoiceRender)
		}
	}

	// --- then the human-and-agent loop's human half follows, on this same
	// plan. ---
	ctx := client.WithAttribution(context.Background(), domain.Attribution{})
	human, err := session.Open(ctx, svc, path)
	if err != nil {
		t.Fatalf("session.Open (human): %v", err)
	}
	contextAnchor, err := session.AnchorForBlockText(string(human.Content), "starved by slower requests ahead of it in the queue", nil)
	if err != nil {
		t.Fatalf("anchoring human comment 1: %v", err)
	}
	humanThread1, err := human.Comment(ctx, contextAnchor, "Do we have paging/alerting if the queue backs up?")
	if err != nil {
		t.Fatalf("human comment 1: %v", err)
	}
	designAnchor, err := session.AnchorForBlockText(string(human.Content), "drain the queue independently", nil)
	if err != nil {
		t.Fatalf("anchoring human comment 2: %v", err)
	}
	humanThread2, err := human.Comment(ctx, designAnchor, "What's the retry policy if a worker crashes mid-job?")
	if err != nil {
		t.Fatalf("human comment 2: %v", err)
	}
	t.Logf("human (pseudonym %s) added two more comments: %s, %s", pseudonym, humanThread1.ID, humanThread2.ID)

	// A different agent identity now -- a fresh conversation, minted fresh,
	// exactly like the human-and-agent loop's own agent.
	greenBadgerReply1 := callTool[mcptools.OKResult](t, cs, "reply", mcptools.ReplyArgs{
		Source: path, ThreadID: string(humanThread1.ID),
		Body: "Adding a queue-depth alert with a 5 minute threshold.",
	})
	greenBadgerID := greenBadgerReply1.AgentID
	if greenBadgerID == "" || greenBadgerID == s1ID || greenBadgerID == s2ID || greenBadgerID == coordID {
		t.Fatalf("the human-and-agent loop's agent identity = %q, want a fourth identity distinct from every earlier one", greenBadgerID)
	}
	greenBadgerReply2 := callTool[mcptools.OKResult](t, cs, "reply", mcptools.ReplyArgs{
		Source: path, ThreadID: string(humanThread2.ID), AgentID: greenBadgerID,
		Body: "Failed jobs retry twice with backoff, then dead-letter for manual replay.",
	})
	if greenBadgerReply2.AgentID != greenBadgerID {
		t.Fatalf("the human-and-agent loop agent's second reply agent_id = %q, want carried %q", greenBadgerReply2.AgentID, greenBadgerID)
	}
	t.Logf("a fourth identity (agent-%s) replied to both of the human's new comments", greenBadgerID)

	finalEdited := strings.Replace(edited,
		"the workers drain the queue independently.",
		"the workers drain the queue independently, alerting when queue depth exceeds a 5 minute backlog and retrying a failed job twice with backoff before dead-lettering it.",
		1)
	if finalEdited == edited {
		t.Fatal("agent's second edit did not change the document -- quote drifted")
	}
	if err := os.WriteFile(path, []byte(finalEdited), 0o644); err != nil {
		t.Fatalf("agent editing plan file (round 2): %v", err)
	}

	human, err = session.Open(ctx, svc, path)
	if err != nil {
		t.Fatalf("human reload: %v", err)
	}
	if err := human.Resolve(ctx, humanThread1.ID, true); err != nil {
		t.Fatalf("human resolve 1: %v", err)
	}
	if err := human.Resolve(ctx, humanThread2.ID, true); err != nil {
		t.Fatalf("human resolve 2: %v", err)
	}
	t.Logf("human resolved both of their new threads")

	// Unlike the human-and-agent loop above, this scenario has the human approve
	// the plan right after resolving both new comments.
	if err := human.Approve(ctx); err != nil {
		t.Fatalf("human approve: %v", err)
	}
	approved, err := human.ApprovedCurrent(ctx)
	if err != nil {
		t.Fatalf("ApprovedCurrent: %v", err)
	}
	if !approved {
		t.Fatal("plan should read as approved for its current content after human.Approve")
	}
	t.Logf("human approved the plan's current content (hash %s)", human.Hash)

	finalRender := renderPlanText(t, svc, path, pseudonym)
	t.Logf("two-agent loop FINAL rendered plan (all five voices: s1, s2, coordinator, human, and the human-and-agent-loop-style agent):\n%s", finalRender)

	dump := dumpStoreJSON(t, statePath)
	t.Logf("two-agent loop store dump (state.json):\n%s", dump)
}

// callToolErr is callTool without the fatal-on-error behavior, for the one
// call (resolve) whose error path testTwoAgentLoop wants to report itself.
func callToolErr(t *testing.T, cs *mcp.ClientSession, name string, args any) (mcptools.OKResult, error) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return mcptools.OKResult{}, err
	}
	if res.IsError {
		var msg strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msg.WriteString(tc.Text)
			}
		}
		return mcptools.OKResult{}, fmt.Errorf("%s", msg.String())
	}
	raw, _ := json.Marshal(res.StructuredContent)
	t.Logf("--> %s %+v\n<-- %s", name, args, raw)
	var out mcptools.OKResult
	_ = json.Unmarshal(raw, &out)
	return out, nil
}

// testIdentityMechanism proves the identity mechanism directly: not a drop
// rate (a scripted run carries its identity exactly as told, so that number
// would describe this harness, not agent behaviour) but the mechanism itself,
// observed directly against the real subprocess with list_plans -- the one
// tool that needs no plan and touches no store, so each observation below is
// isolated from every other.
func testIdentityMechanism(t *testing.T, cs *mcp.ClientSession) {
	// (1) A call arriving with no identity mints one.
	r1 := callTool[mcptools.ListPlansResult](t, cs, "list_plans", mcptools.ListPlansArgs{})
	if r1.AgentID == "" {
		t.Fatal("call with no agent_id got no minted identity back")
	}
	if _, ok := identity.Canonical(r1.AgentID); !ok {
		t.Fatalf("minted identity %q is not well-formed", r1.AgentID)
	}
	t.Logf("(1) id-less call minted: agent-%s", r1.AgentID)

	// A second id-less call mints an INDEPENDENT identity -- distinctness
	// with nothing exchanged.
	r2 := callTool[mcptools.ListPlansResult](t, cs, "list_plans", mcptools.ListPlansArgs{})
	if r2.AgentID == "" {
		t.Fatal("second id-less call got no minted identity back")
	}
	if r2.AgentID == r1.AgentID {
		t.Fatalf("two independent id-less calls minted the SAME identity: %s", r1.AgentID)
	}
	t.Logf("(1b) a second, independent id-less call minted a DIFFERENT identity: agent-%s (never exchanged with the first)", r2.AgentID)

	// (2) A call carrying a valid identity is honoured: the same identity
	// comes back, not a fresh mint.
	r3 := callTool[mcptools.ListPlansResult](t, cs, "list_plans", mcptools.ListPlansArgs{AgentID: r1.AgentID})
	if r3.AgentID != r1.AgentID {
		t.Fatalf("carrying valid agent_id %q came back as %q, want it honoured unchanged", r1.AgentID, r3.AgentID)
	}
	t.Logf("(2) carried valid identity honoured: agent-%s in, agent-%s out", r1.AgentID, r3.AgentID)

	// (3) A call carrying a MALFORMED identity is treated as absent and
	// gets a freshly minted one -- not an error, not echoed back verbatim.
	const malformed = "eve-agent-blue-thunder"
	r4 := callTool[mcptools.ListPlansResult](t, cs, "list_plans", mcptools.ListPlansArgs{AgentID: malformed})
	if r4.AgentID == malformed {
		t.Fatalf("malformed agent_id %q was echoed back verbatim instead of being re-minted", malformed)
	}
	if r4.AgentID == "" {
		t.Fatal("malformed agent_id produced no replacement identity")
	}
	if _, ok := identity.Canonical(r4.AgentID); !ok {
		t.Fatalf("replacement for malformed agent_id is itself malformed: %q", r4.AgentID)
	}
	if r4.AgentID == r1.AgentID || r4.AgentID == r2.AgentID {
		t.Fatalf("malformed-id re-mint collided with an earlier mint: %s", r4.AgentID)
	}
	t.Logf("(3) malformed identity %q sent in -> treated as absent, freshly minted agent-%s back", malformed, r4.AgentID)
}

// testSaveVerb drives save over the real subprocess. save is for a plan whose
// source draftplane cannot read itself (Notion, another machine, generated
// content) -- deliberately not a local file, so this needs no plan file and
// cannot entangle with either loop's state, the same isolation
// testIdentityMechanism gets from list_plans.
func testSaveVerb(t *testing.T, cs *mcp.ClientSession) {
	const source = "notion://dogfood-save-demo"
	const content = "# Notion-Sourced Plan\n\nThis plan's source is not a file draftplane can read directly, so it must be saved.\n"

	created := callTool[mcptools.GetReviewResult](t, cs, "save", mcptools.SaveArgs{Source: source, Content: content})
	if created.AgentID == "" {
		t.Fatal("save's first call got no minted identity back")
	}
	if created.Plan == nil || created.Plan.SourceHint != source {
		t.Fatalf("save did not create a plan for source %q: %+v", source, created.Plan)
	}
	t.Logf("save created plan %s (%q) from a non-local source, agent-%s", created.Plan.ID, created.Plan.Title, created.AgentID)

	// A second save, carrying the identity, with updated content: proves
	// the update-by-plan_id path, not just creation, and that the identity
	// carries across a save call the same as every other verb.
	updated := content + "\nAppended after the initial save.\n"
	res2 := callTool[mcptools.GetReviewResult](t, cs, "save", mcptools.SaveArgs{
		PlanID: created.Plan.ID, Content: updated, AgentID: created.AgentID,
	})
	if res2.AgentID != created.AgentID {
		t.Fatalf("second save agent_id = %q, want carried %q", res2.AgentID, created.AgentID)
	}
	if res2.Hash == created.Hash {
		t.Fatal("second save did not register a new hash for the updated content")
	}
	t.Logf("save updated the plan to a new hash, carrying agent-%s", created.AgentID)
}

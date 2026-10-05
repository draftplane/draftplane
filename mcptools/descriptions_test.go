package mcptools

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolNamesListsTheTenTools(t *testing.T) {
	want := []string{"list_plans", "get_review", "comment", "comment_section",
		"reply", "resolve", "rehome", "approve", "save", "download"}
	if got := ToolNames(); !slices.Equal(got, want) {
		t.Fatalf("ToolNames() = %v, want %v", got, want)
	}
}

// connected drives NewServer over a real in-memory MCP session, so every test
// below reads what an agent is actually told rather than the constants behind
// it: a registration that drops a description or a schema the SDK could not
// infer fails here and not silently in production.
func connected(t *testing.T) *mcp.ClientSession {
	t.Helper()
	f := setup(t)
	server := NewServer(New(f.newStore(t), "calm-mountain"))
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(f.ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	clientSession, err := c.Connect(f.ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func listedTools(t *testing.T) []*mcp.Tool {
	t.Helper()
	cs := connected(t)
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) == 0 {
		t.Fatal("no tools registered -- every assertion below would hold vacuously")
	}
	return res.Tools
}

func descriptionsByName(t *testing.T) map[string]string {
	t.Helper()
	byName := map[string]string{}
	for _, tl := range listedTools(t) {
		byName[tl.Name] = tl.Description
	}
	return byName
}

// TestToolDescriptionsCoachAddressingAndRefusal pins the clauses an agent acts
// on in each registered description. A description is in the model's context at
// every call, so a sentence nothing pins is one a later tightening of the prose
// deletes with the whole suite green.
func TestToolDescriptionsCoachAddressingAndRefusal(t *testing.T) {
	byName := descriptionsByName(t)

	for _, tc := range []struct {
		name string
		want []string
	}{
		// comment and rehome hand a free-form quote to session.AnchorForBlockText,
		// and a quote crossing a block boundary is accepted and anchors to the block
		// it overlaps most. The preference and the consequence are pinned together:
		// the preference alone reads as an enforced rule, the consequence alone gives
		// the agent nothing to do differently.
		{"comment", []string{
			"source or plan_id", "3+ consecutive words", "SINGLE paragraph", "overlaps most",
		}},
		{"rehome", []string{
			"source or plan_id", "SINGLE paragraph", "overlaps most",
		}},
		{"save", []string{
			// Whose file it is: the staging clause without the human clause invites
			// saving a document someone is editing, and the human clause alone
			// invites naming a staging copy as source.
			"no file anyone else opens", "staged the text in a file of your own", "do NOT name that path",
			"let the file go", "a HUMAN works in",
			// All three addressing forms; NEITHER is the right answer for the
			// commonest caller and the one an agent will not guess.
			"plan_id", "NEITHER for content you generated",
			// content_from is the door beside the staging refusal: the rule is about
			// registering a path, not about the path.
			"pass it as content_from",
		}},
		// What get_review inlines is exactly what download writes, and what download
		// refuses is exactly what get_review omits (reviewResult's doc), so both
		// descriptions are pinned.
		{"get_review", []string{
			"read live and re-placed against what's on disk",
			"that field is absent whenever draftplane reads this plan",
			"plan.source then names the file to read",
			"or plan_id (from list_plans, get_review, or save) — exactly one",
		}},
		// TestADownloadedPathAddressedAsSourceForksASecondPlan proves the fork this
		// description warns about is still reachable; the description and
		// DownloadResult.Path's schema are the whole of the mitigation.
		{"download", []string{
			"NOT an address for the plan", "go on addressing it by plan_id",
			"creates a SECOND plan",
			"source or plan_id", "must NOT already exist",
			"hash names the exact version whose bytes were written",
		}},
	} {
		desc, ok := byName[tc.name]
		if !ok {
			t.Fatalf("%s: not registered at all", tc.name)
		}
		for _, w := range tc.want {
			if !strings.Contains(desc, w) {
				t.Errorf("%s description = %q, want it to contain %q", tc.name, desc, w)
			}
		}
	}
}

// TestDownloadsPathSchemaSaysThePathIsNotAnAddress is the description test's
// other half, at the altitude many hosts render: a result field's schema sits
// beside the value the agent is holding, which is the moment the warning has to
// arrive, since the fork happens on the next call with that path in hand.
func TestDownloadsPathSchemaSaysThePathIsNotAnAddress(t *testing.T) {
	var schema string
	for _, tl := range listedTools(t) {
		if tl.Name != "download" {
			continue
		}
		if tl.OutputSchema == nil {
			t.Fatal("download registered no output schema; every assertion below would be about an empty string")
		}
		raw, err := json.Marshal(tl.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		schema = string(raw)
	}
	if schema == "" {
		t.Fatal("download is not registered at all")
	}

	for _, tc := range []struct {
		want string
		why  string
	}{
		{"NOT an address for the plan", "the denial itself. Without it the field says only that the file is the agent's, which is true and is not the thing that costs somebody their comment."},
		{"keep addressing that by plan_id", "the instruction. A denial with no address left to use is a field an agent reads and then does the natural thing anyway."},
		{"creates a second plan for it", "the consequence, and the half that makes an agent stop: a fork nobody is looking at is not an outcome it would otherwise weigh."},
	} {
		if !strings.Contains(schema, tc.want) {
			t.Errorf("download's path field schema does not carry %q\nwhy it must: %s\nschema: %s", tc.want, tc.why, schema)
		}
	}
}

// TestEveryToolDescriptionCarriesAgentIDNote loops over ToolNames() rather than
// naming tools, so a tool added later is covered with no edit here. It asserts
// the exact constant: agentIDNote is one constant appended once per tool so the
// wording cannot drift out of sync with itself.
func TestEveryToolDescriptionCarriesAgentIDNote(t *testing.T) {
	byName := descriptionsByName(t)

	names := ToolNames()
	if len(names) != len(byName) {
		t.Fatalf("ToolNames() lists %d tools, server registered %d -- keep them in sync", len(names), len(byName))
	}
	for _, name := range names {
		desc, ok := byName[name]
		if !ok {
			t.Fatalf("%s: registered in ToolNames() but not on the server", name)
		}
		if !strings.Contains(desc, agentIDNote) {
			t.Errorf("%s description does not contain agentIDNote verbatim -- an agent calling this tool has no reminder to carry its identity forward:\n%s", name, desc)
		}
	}
}

// TestServerInstructionsReachAnAgentAtInitialize reads the blob off the
// initialize result, so a wiring mistake (ServerOptions dropped back to nil)
// fails here. It pins the frame and not the prose: each clause below is a
// decision, asserted as a substring of the sentence that carries it.
func TestServerInstructionsReachAnAgentAtInitialize(t *testing.T) {
	got := connected(t).InitializeResult().Instructions
	if got == "" {
		t.Fatal("the initialize result carries no instructions; every assertion below would pass on an empty string if it were a Contains check alone")
	}
	if got != serverInstructions {
		t.Fatalf("the initialize result carries instructions that are not serverInstructions:\n got: %q\nwant: %q", got, serverInstructions)
	}

	for _, tc := range []struct {
		clause string
		why    string
	}{
		{"reviewed before it is built", "the identity line. An agent matches on what it has just been asked to do, and nothing downstream of the decision to call a tool can tell it this server exists for that."},
		{"almost always written by agents", "the fact everything else follows from. Without it an agent reasons about this server as if humans authored the plans."},
		{"asked to review one", "a trigger: a subagent handed a plan to review has nothing else telling it the findings belong here rather than in a reply to its coordinator."},
		{"act on the feedback on one", "the commonest trigger: the human asks for changes and the comments are the specification."},
		{"If nothing is being reviewed, you do not need it", "the bound, and it matters as much as the triggers. Without it a server described this warmly gets reached for on every file the agent touches."},
		{"no opinion about how a plan should be written or reviewed", "Draftplane is the coordination layer and the ecosystem owns the methodology. Without this an agent supplies the missing opinion itself."},
		{"re-reads the file live", "the file case: the bytes are re-read rather than copied, which is what makes addressing by path correct instead of merely cheaper."},
		{"never writes to the file", "an agent that believes Draftplane may edit the plan will route the human's edits through it."},
		{"Draftplane holds the plan itself", "the supplied case: save hands over the plan itself, which is the mode an agent that wrote the plan in-conversation is actually in."},
		{"NOT A SOURCE", "a scratch file may exist on the agent's side and Draftplane must never learn about it. This is the rule; save's refusal of a readable source is only its backstop."},
		{"second thing claiming to be the plan", "why the staging rule exists, which is the half a refusal cannot deliver -- an agent meets the refusal only after it has already decided to name the path."},
		{"nothing to register", "there is no handshake or setup step, and an agent that assumes one asks the human for something that does not exist."},
		{"draftplane review <path or plan id>", "the agent hands the human the line that opens what it just made, rather than pasting a plan back into the conversation."},
	} {
		if !strings.Contains(got, tc.clause) {
			t.Errorf("the instructions do not carry %q\nwhy it must: %s", tc.clause, tc.why)
		}
	}
}

// TestServerInstructionsNameOnlyVerbsThatExist is the anti-drift half, and the
// reason serverInstructions backticks every name it mentions: every backticked
// token must be a registered tool or a `draftplane` command line, checked
// against ToolNames(), so a renamed verb reddens here instead of teaching an
// agent a word the binary does not answer to.
func TestServerInstructionsNameOnlyVerbsThatExist(t *testing.T) {
	registered := map[string]bool{}
	for _, n := range ToolNames() {
		registered[n] = true
	}

	backticked := regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(serverInstructions, -1)
	if len(backticked) == 0 {
		t.Fatal("no backticked token in the instructions at all; this test would pass vacuously on a blob that named a dozen dead verbs in plain prose")
	}
	for _, m := range backticked {
		tok := m[1]
		if registered[tok] || strings.HasPrefix(tok, "draftplane") {
			continue
		}
		t.Errorf("the instructions name %q, which is neither a registered tool (ToolNames) nor a `draftplane` command line", tok)
	}

	for _, tc := range []struct {
		absent string
		why    string
	}{
		{"agent_id", "appended to every tool description (agentIDNote), where it is in context at every call. A copy here is a second place for one rule to drift."},
		{"Only approve when", "approve's own description owns this. The instructions say when to reach for the server, never what any one verb's rule is."},
		{"never the plan itself", "false in supplied mode, where save hands over the plan itself and Draftplane is the source of truth."},
		{"readable file", "readability is not the test for whether to address a plan by path -- an agent's own scratch copy is readable too. Whose file it is, is the test; this phrase is save's refusal text, and quoting a backstop here teaches it as the rule."},
	} {
		if strings.Contains(serverInstructions, tc.absent) {
			t.Errorf("the instructions repeat %q\nwhy they must not: %s", tc.absent, tc.why)
		}
	}

	// The budget is ours and not the protocol's: hosts treat this as orienting
	// text that competes with the user's own context, and a bound makes growing
	// it a decision rather than an accident.
	const budget = 2048
	if n := len(serverInstructions); n > budget {
		t.Errorf("the instructions are %d bytes against a self-imposed budget of %d; growing them is a decision to take deliberately", n, budget)
	} else {
		t.Logf("instructions: %d bytes of a %d-byte budget", n, budget)
	}
}

// capsRun is the longest run of consecutive ALL-CAPS words in s, counting a bare
// "A" as a word the way a reader's eye does: "A LOCAL-FILE source" shouts two
// words, not one.
func capsRun(s string) (int, string) {
	shouted := func(w string) bool {
		w = strings.Trim(w, `".,:;()-`)
		if w == "" {
			return false
		}
		letter := false
		for _, r := range w {
			switch {
			case r >= 'A' && r <= 'Z':
				letter = true
			case r >= '0' && r <= '9', r == '-', r == '\'':
			default:
				return false
			}
		}
		return letter
	}
	words := strings.Fields(s)
	best, bestAt, run, at := 0, "", 0, 0
	for i, w := range words {
		if !shouted(w) {
			run = 0
			continue
		}
		if run == 0 {
			at = i
		}
		run++
		if run > best {
			best, bestAt = run, strings.Join(words[at:i+1], " ")
		}
	}
	return best, bestAt
}

// TestNoToolDescriptionShoutsAWholeClause bounds the emphasis in the prose an
// agent reads at every call: capitals mark a single term -- ORPHANED, SECTION --
// never a sentence. The description tests check what a description says; this
// is the one assertion about how it says it. Comments in this repository keep
// their own louder convention and are not the subject.
func TestNoToolDescriptionShoutsAWholeClause(t *testing.T) {
	const budget = 3

	for _, tl := range listedTools(t) {
		if n, run := capsRun(tl.Description); n > budget {
			t.Errorf("%s's description shouts %d consecutive words, want at most %d: %q\n"+
				"capitals here mark a TERM, never a clause -- lower-case it and emphasise the one word that carries the point",
				tl.Name, n, budget, run)
		}
	}
}

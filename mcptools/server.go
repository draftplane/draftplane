package mcptools

import (
	"context"

	"github.com/draftplane/draftplane/version"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverVersion is what an MCP client is told in the initialize handshake.
// It is derived rather than declared.
func serverVersion() string { return version.Short() }

// agentIDNote is appended to every tool's description below. A description is
// in the model's context at every call it makes; a response is in context only
// once -- so this, not the result field's own echo, is the main lever against
// an agent that drops its identity between calls (each drop fragments one agent
// into two indistinguishable voices). One constant rather than a hand-typed
// copy per tool, so the wording cannot drift out of sync with itself. It states
// no count of tools: every version of this sentence that named a number went
// stale in the commit that added a verb.
const agentIDNote = " Carry agent_id across every call you make this conversation: pass back whatever the most recent draftplane response gave you, and omit it only on your very first draftplane call here, which mints you a fresh identity -- dropping a carried identity splits you into a second, indistinguishable voice."

// wrap adapts a transport-free Tools method into a typed SDK handler.
// Errors are returned to the SDK, which turns every non-protocol error into
// a tool result (SetError: IsError plus the coaching text as content) so the
// model can self-correct — and, critically, skips output marshaling, so an
// error result never carries a zero-value StructuredContent that a client
// reading structured output before checking isError could mistake for a real
// payload. On success a nil result lets the SDK populate the result from Out.
func wrap[In, Out any](fn func(ctx context.Context, args In) (Out, error)) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args In) (*mcp.CallToolResult, Out, error) {
		out, err := fn(ctx, args)
		return nil, out, err
	}
}

// ToolNames lists the registered tool names, for `draftplane mcp --list-tools`.
//
// HAND-MAINTAINED AND NOT DERIVED FROM THE REGISTRATIONS BELOW, which is a
// standing finding rather than a design: a tool added to NewServer and
// forgotten here would be served and unlisted. What holds the two together is
// that several tests in this package read this list and check it against the
// server's own answer, so the drift fails loudly here rather than silently in
// production.
func ToolNames() []string {
	return []string{
		"list_plans", "get_review", "comment", "comment_section",
		"reply", "resolve", "rehome", "approve", "save", "download",
	}
}

// serverInstructions is the ONE PLACE THIS SERVER ANSWERS "WHEN" AND "WHAT IS
// THIS". A tool description answers "how do I call this", is in the model's
// context at every call, and is never read by an agent that has not already
// decided to reach for a tool. This blob arrives once, at initialize, in every
// session that connects the server -- so it is the only surface that reaches an
// agent BEFORE it has decided anything.
//
// IT OPENS WITH WHAT DRAFTPLANE IS AND WHEN TO REACH FOR IT, said as a trigger
// rather than as a category, because an agent matches on what it has just been
// asked to do. The bound matters as much as the triggers: if nothing is being
// reviewed, an agent should not reach for this at all. Pin the TRIGGERS
// explicitly and not only the mechanism -- a test table cannot notice that
// something it never named is gone.
//
// ITS CENTRE IS WHERE THE BYTES COME FROM, and getting that wrong is the one
// mistake an agent makes that no refusal fully catches. It has two answers -- a
// file a human works in, and bytes the agent supplies -- and an agent chooses
// between them.
//
// "AND NEVER THE PLAN ITSELF" MUST NOT GO BACK IN. As a blanket rule it is
// FALSE in supplied mode, which is the mode an agent is most likely to be in:
// when the plan was written in the conversation or lives in Notion, `save`
// hands over the plan itself and Draftplane IS the source of truth.
//
// NOR IS READABILITY THE TEST -- a scratch copy the agent staged at /tmp is
// readable too. The test is WHOSE FILE IT IS: a file a human opens and edits is
// what Draftplane follows; a file the agent wrote to get text out of context is
// not a source at all and Draftplane must never learn about it. Save's refusal
// of a readable source is the backstop for getting this wrong, not the rule
// itself, and a blob that teaches the backstop as the rule teaches the wrong
// thing.
//
// THE THIRD BULLET SPLITS REGISTRATION FROM TRANSPORT, and the rule it carries
// was always about the first. Naming a staging path as the plan's SOURCE
// REGISTERS it -- Draftplane follows that path from then on, which is exactly
// how a second thing claiming to be the plan comes to exist. Handing the same
// path to `save` as content_from registers nothing: the bytes are read once and
// the path is never stored, so it is the right way to move a document too large
// to carry through the conversation. Hence "read once, never followed", which
// answers bullet one's "re-reads the file live" in the same vocabulary. The
// bullet's first sentence is untouched because it was never wrong; what changed
// is that letting the file go now has a door beside it instead of only a
// prohibition.
//
// content_from IS DELIBERATELY NOT BACKTICKED, unlike every verb here. The
// sweep below admits a backticked token only if it is a registered tool or a
// `draftplane` command line, and an argument name is neither -- widening it to
// admit argument names would cost the anti-drift property the sweep exists for,
// to buy typography.
//
// WHAT IT MUST NOT CARRY, each ruled rather than preferred:
//
//   - METHODOLOGY. Draftplane is the coordination layer; the ecosystem owns the
//     opinions. A sentence here about what makes a plan good is out of scope,
//     not merely unnecessary.
//   - A COUNT OF ANYTHING. Every sentence in this package that named a number
//     of verbs went stale in the commit that added one.
//   - PER-VERB RULES the tool descriptions already own. agent_id is appended
//     to every description precisely because it must be in context at every
//     call; repeating it here buys nothing and gives it a second place to
//     drift. Likewise "only approve when asked", which is approve's own
//     description's sentence.
//   - DELETE, at either granularity. There is no verb for it and there never
//     is. The absence is the mechanism; naming it advertises a door.
//
// EVERY VERB NAME IT MENTIONS IS BACKTICKED, and that is load-bearing rather
// than typographical: TestServerInstructionsNameOnlyVerbsThatExist extracts the
// backticked tokens and requires each to be a registered tool or a `draftplane`
// command line, so a renamed verb reddens here instead of teaching an agent a
// word the binary does not answer to.
//
// THE LENGTH IS A SELF-IMPOSED BUDGET rather than a protocol limit: hosts treat
// this as orienting text and a long one competes with the user's own context for
// attention. The test bounds it so growth is a decision.
const serverInstructions = `Draftplane is where a plan gets reviewed before it is built. The plans are almost always written by agents; the review comes from a human or from other agents. Carrying that feedback between them is the whole of what this server does, and it ships no opinion about how a plan should be written or reviewed.

REACH FOR IT when you have written a plan someone is going to read, when you are asked to review one, or when you are asked to act on the feedback on one. If nothing is being reviewed, you do not need it.

It holds the threads, replies, resolutions and approvals — who said what, about exactly which words — and re-anchors them onto the plan's current bytes as it changes. Where those bytes come from depends on where the plan lives, and it is the thing to get right:

- A FILE A HUMAN WORKS IN — they asked you to write it, or they opened it themselves. Draftplane follows that file: address the plan by its path and it re-reads the file live. Do not send the contents. Draftplane never writes to the file either; it is the human's.
- NO FILE ANYONE ELSE OPENS — you wrote the plan in this conversation, or it lives somewhere Draftplane cannot reach, such as Notion or another machine. Then Draftplane holds the plan itself: ` + "`save`" + ` takes the bytes and returns a plan id, and later versions arrive the same way against that id.
- A STAGING FILE OF YOUR OWN IS NOT A SOURCE. If you put the text on disk to get it out of context, do not name that path as source: a staging copy Draftplane learns about becomes a second thing claiming to be the plan. Hand it to ` + "`save`" + ` as content_from instead — read once, never followed.

A plan is created by the first review fact left on it: there is nothing to register.

A human reads and answers all of this in a terminal — ` + "`draftplane review <path or plan id>`" + ` opens one plan, ` + "`draftplane`" + ` opens the list. Point them there rather than pasting a plan back into the conversation.`

// NewServer builds the MCP server with every tool ToolNames lists registered.
// Tool descriptions double as agent documentation for the review loop, and
// serverInstructions above is the frame they sit inside: the descriptions say
// how each verb is called, the instructions say what this server is for and
// when to reach for it at all. The two are deliberately disjoint -- see
// serverInstructions' own list of what it must not repeat. This sentence
// deliberately does not count the verbs: the number is the part that keeps
// going stale.
func NewServer(t *Tools) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "draftplane", Version: serverVersion()},
		&mcp.ServerOptions{Instructions: serverInstructions})
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(s, &mcp.Tool{Name: "list_plans", Annotations: ro,
		Description: "List the plans in this machine's review store: id, title, and source hint, " +
			"alongside total — how many plans there are. " +
			"Each plan's id addresses it directly via any other tool's plan_id parameter." + agentIDNote},
		wrap(t.ListPlans))
	mcp.AddTool(s, &mcp.Tool{Name: "get_review", Annotations: ro,
		Description: "Read the full review state of a plan: its threads placed onto the CURRENT " +
			"content (status exact/moved/fuzzy, or orphaned with ranked candidate locations), each " +
			"thread's comments, and whether the current content is approved. Address the plan with " +
			"source (a file path or URL) or plan_id (from list_plans, get_review, or save) — exactly one. A source " +
			"naming a readable local file is read live and re-placed against what's on disk; call this " +
			"again after you edit it. Anything else resolves to the plan's latest saved content, " +
			"returned in the result's content field — but that field is absent whenever draftplane reads this plan " +
			"from a file on this machine, and plan.source then names the file to read for the current bytes. " +
			"If you hold newer content, register it first with save." + agentIDNote},
		wrap(t.GetReview))
	// WHAT A GOOD QUOTE IS, deliberately guidance rather than a refusal. An
	// agent's quote reaches session.AnchorForBlockText verbatim, and a quote
	// crossing a block boundary anchors perfectly well against a SECTION --
	// reanchor.Normalize collapses newlines -- so nothing here is a wrong
	// call to reject: the thread lands on the block it overlaps most, the
	// same judgement the re-anchorer makes for a fuzzy match. What the agent
	// cannot otherwise know is that a quote spanning two paragraphs picks ONE
	// of them, so the description says which, and says to prefer a single
	// block if the choice matters.
	mcp.AddTool(s, &mcp.Tool{Name: "comment",
		Description: "Start a review thread anchored to specific document text. Address the plan with " +
			"source or plan_id — exactly one. Quote 3+ consecutive words EXACTLY as they appear, and " +
			"prefer text from a SINGLE paragraph, list item or code block: a quote spanning a paragraph " +
			"break — or a fenced block and the prose after it — is accepted, and the thread anchors to " +
			"whichever block it overlaps most, which may not be the one you meant. Pass " +
			"heading_path when the quote appears in more than one section. The first review fact on a " +
			"local-file source creates its plan automatically; a source draftplane cannot read must be " +
			"saved first." + agentIDNote},
		wrap(t.Comment))
	mcp.AddTool(s, &mcp.Tool{Name: "comment_section",
		Description: "Start a review thread on a whole SECTION (anchored to its heading), for feedback " +
			"about the section itself — naming, scope, ordering — rather than specific text. Address " +
			"the plan with source or plan_id — exactly one. heading_path is the full path from the " +
			"document title down." + agentIDNote},
		wrap(t.CommentSection))
	mcp.AddTool(s, &mcp.Tool{Name: "reply",
		Description: "Add a comment to an existing thread (thread_id from get_review). Address the " +
			"plan with source or plan_id — exactly one." + agentIDNote},
		wrap(t.Reply))
	mcp.AddTool(s, &mcp.Tool{Name: "resolve",
		Description: "Mark a thread resolved (or reopen it). Address the plan with source or plan_id " +
			"— exactly one. Resolve threads you have addressed in the document." + agentIDNote},
		wrap(t.Resolve))
	mcp.AddTool(s, &mcp.Tool{Name: "rehome",
		Description: "Durably re-anchor an ORPHANED thread onto the current content, after you " +
			"determine where its target moved: pass quote for exact text, or heading_path to make it " +
			"a section comment. quote takes the same free-form text comment does, so prefer text from " +
			"a SINGLE paragraph, list item or code block: a quote spanning a paragraph break is " +
			"accepted and the thread anchors to whichever block it overlaps most. Address the plan " +
			"with source or plan_id — exactly one. Use get_review's " +
			"candidates plus the content to decide." + agentIDNote},
		wrap(t.Rehome))
	mcp.AddTool(s, &mcp.Tool{Name: "approve",
		Description: "Record approval of the plan's CURRENT content (bound to its exact hash). Address " +
			"the plan with source or plan_id — exactly one. Only approve when asked to, or when your " +
			"review finds no remaining issues." + agentIDNote},
		wrap(t.Approve))
	mcp.AddTool(s, &mcp.Tool{Name: "save",
		// The distinction is not readable-versus-not: it is WHOSE FILE IT
		// IS. An agent that generated a plan and staged it on disk to get it
		// out of context holds a local file, and coaching it to address that
		// path is precisely how draftplane comes to track a scratch copy that
		// an agent can rationalize as "it gets cleaned up by system processes
		// anyway". serverInstructions' own doc comment states the rule this
		// carries: a scratch file may exist on the agent's side and
		// draftplane must never learn about it.
		//
		// AND THE PROHIBITION IS ON REGISTRATION, NOT ON THE PATH. content_from
		// takes that same staging path, reads it once and remembers nothing, so
		// the staging clause names a door beside the refusal now rather than a
		// refusal alone: the scratch file still never becomes a source, and the
		// bytes no longer have to come back through the conversation to get
		// here. serverInstructions' third bullet carries the same split.
		Description: "Create or update a plan by supplying its content directly — for a plan with no file " +
			"anyone else opens: content you generated in this conversation, or a document somewhere " +
			"draftplane cannot reach (Notion, another machine). Supply the bytes as content, or as " +
			"content_from — a path draftplane reads once and does not remember — when the document is too " +
			"large to carry through this conversation. Registers them as the plan's newest version and " +
			"returns the full review with every thread re-anchored onto it. " +
			"Address with plan_id to update, source only for a stable reference draftplane cannot read (a URL), " +
			"or NEITHER for content you generated. If you staged the text in a file of your own, do NOT name that " +
			"path as source — pass it as content_from, or send the content alone, and let the file go: a source is " +
			"a path draftplane follows from then on, so naming this one leaves draftplane tracking a file that is " +
			"about to be deleted. The one document not to save is one a HUMAN works in: edit it and use the review " +
			"verbs, and draftplane reads it directly." + agentIDNote},
		wrap(t.Save))
	// NO ReadOnlyHint, and the omission is a ruling rather than an oversight.
	// download is a READ of the plan -- it records nothing -- but that hint is
	// a claim about the tool's effect on its ENVIRONMENT, and this one creates
	// a file on the caller's disk. get_review keeps the hint precisely because
	// download exists to be the door that writes, which is the whole reason no
	// read verb grew a path argument.
	//
	// THE PATH IS COACHED AS "NOT AN ADDRESS", AND THAT IS MITIGATION RATHER
	// THAN A GUARD. TestADownloadedPathAddressedAsSourceForksASecondPlan proves
	// the call still forks: comment(source: <the path download answered with>)
	// reaches ensurePlan with nothing in its way, mints a SECOND plan for that
	// path, and lands the thread on it -- the plan the agent believes it
	// commented on comes back with zero threads. Until a refusal exists, this
	// description and DownloadResult.Path's schema are the whole of what stands
	// between an agent and that outcome, so both say it DIRECTLY: this path is
	// not an address, keep using plan_id. What they must not do is lean on "the
	// plan is still addressed exactly as it was before", which was already in
	// this description when the defect was found -- an agent can read that as
	// permission to use either address, and one of the two forks.
	mcp.AddTool(s, &mcp.Tool{Name: "download",
		Description: "Write a plan's current content to a path on this machine, and answer with that path and the " +
			"content's hash. This is a READ that happens to produce a file: draftplane records nothing about it and " +
			"does not follow it, so the file is yours, editing it changes nothing here, and the plan is " +
			"still addressed exactly as it was before. The path it answers with is NOT an address for the plan: go " +
			"on addressing it by plan_id. Hand that path to another verb as source and draftplane creates a SECOND " +
			"plan for it -- the comment you meant for the plan lands on the copy, and nothing says so. Reach for it " +
			"instead of get_review's content field when the document is too large to carry back through a response. " +
			"Address the plan with source or plan_id -- " +
			"exactly one; path names the file to create. It must NOT already exist -- draftplane never writes over " +
			"anything that is there -- and the directory holding it must, since no directory is created for you. " +
			"Refused for a plan draftplane reads from a file on this machine: that file already holds the content, " +
			"so read it with your own tools instead. hash names the exact version whose bytes were written." + agentIDNote},
		wrap(t.Download))
	return s
}

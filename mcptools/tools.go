// Package mcptools exposes the review session verbs to agents over MCP.
// Handlers are transport-free and stateless: every call resolves a fresh
// session from the given source or plan_id, so agents always see current
// truth (localfs reloads under flock per operation). Statelessness is a
// requirement, not a style: the SDK dispatches every tool call asynchronously
// (jsonrpc2.Async), so two calls on one session can be in flight at once and
// nothing here serializes them. Handlers that carried state between calls
// would race; the flock is what serializes the writes underneath. Error
// messages are written as agent coaching — they surface as tool results, and a
// well-behaved agent can self-correct from the text alone. Only save writes
// document content, and only for sources draftplane cannot read itself; every
// other tool edits nothing and records review facts only — a local file is
// always edited with the agent's own file tools. Deleting a plan is
// deliberately not on this surface: it is destructive and stays a human
// gesture in the TUI. Deleting a THREAD is the same ruling one level down:
// there is no MCP verb for it and there never is. Renaming is likewise
// TUI-only.
package mcptools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/client/identity"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/reanchor"
	"github.com/draftplane/draftplane/session"
	"github.com/draftplane/draftplane/ui"
)

type Tools struct {
	svc client.PlanService
	// pseudonym is this machine's display name (client/config.MachinePseudonym),
	// which ui.FormatAttribution renders for a review fact whose Attribution
	// names no agent.
	pseudonym string
}

func New(svc client.PlanService, pseudonym string) *Tools {
	return &Tools{svc: svc, pseudonym: pseudonym}
}

// attribute resolves this call's agent identity -- agentID canonicalized if
// it is well-formed, freshly minted otherwise -- and returns a context
// decorated with that identity, alongside the identity itself for echoing back
// on the result. Every handler calls this first, before t.resolve builds a
// session.
//
// Minting on every call that arrives without one is not a fallback: it is what
// turns a subagent's fresh context into a distinct voice. There is deliberately
// no other path, because nothing here can tell "a subagent's genuinely first
// call" apart from "an agent that lost its identity" -- they are the same
// signal, and a fallback would trade the former for making the latter harmless.
func (t *Tools) attribute(ctx context.Context, agentID string) (context.Context, string) {
	id, ok := identity.Canonical(agentID)
	if !ok {
		id = identity.Mint()
	}
	return client.WithAttribution(ctx, domain.Attribution{Agent: id}), id
}

// resolve turns (source XOR plan_id) into a session, applying the local-
// capability rule: a source naming a readable file on this machine is
// live-followed (draftplane reads it); anything else resolves to the plan's
// latest registered version (session.OpenByID). file:// URLs normalize to bare
// paths first.
//
// Both id-shaped branches below -- plan_id directly, and a source ResolvePlan
// has already turned into a plan id -- go through openByID, so the two doors
// onto one plan cannot disagree on FromSnapshot() or on its source fault
// depending on which argument an agent happened to pass. A source nothing is
// registered for answers the two ways it always does: a readable file opens
// plan-less (Exists false), so the first comment or approve creates a plan
// there (ensurePlan); an unreadable one gets coachResolvePlanErr's "no plan
// known for source" instead, since there is no create prompt over MCP.
//
// THE FOUR SOURCE-FAULT STATES SPLIT ACROSS THE BRANCHES, AND THIS WAS
// INVESTIGATED RATHER THAN ASSUMED: SourceFileGone and
// SourceFileUnreadable read identically whichever argument named the plan,
// because both id-shaped branches and a readable-local-file's OWN later
// re-check (openByID, applyMCPSourceFault) ask the filesystem the same question
// about the same path -- TestResolveComputesSourceFaultWhenALocalFileBackedPlansFileIsGone
// tables both addressing modes for exactly this reason. SourceFileReleased and
// SourceFileClaimed do NOT agree, and neither gap is a defect to close:
//
//   - CLAIMED: when a later plan's own record still names a path an earlier
//     plan already claims (client/localfs.Store.ResolvePlan answers the FIRST
//     match), addressing the LATER plan by its id reaches applyMCPSourceFault,
//     which reports source_fault=claimed and withholds content.
//     Addressing the SAME PATH instead never asks that question at all --
//     the readable-local-file branch calls session.Open on the path and
//     returns whatever plan it resolves to NOW, which is the EARLIER plan's
//     own healthy review, no fault attached. That is not a different plan's
//     review escaping a check: the caller asked what the file is, and the
//     earlier plan genuinely is its current, true answer. See
//     TestResolveBySourceAnswersTheCurrentFileOwnerForAClaimedPath.
//   - RELEASED: a plan's own record can go on naming a path nothing resolves
//     to any more, but only via a genuine race between two calls -- see
//     applyMCPSourceFault's own doc for why a settled filesystem state can
//     never produce this on its own. Addressed by that plan's id mid-race,
//     this reports source_fault=released. Addressed by the same path,
//     live-follow finds no plan there either and answers the ordinary "no
//     plan is registered for this file yet" invitation any never-seen document
//     gets -- an agent that acts on it mints a SECOND, unrelated plan at that
//     path. See TestResolveBySourceInvitesACreateForAReleasedPath.
//
// Neither gap closes without either refusing a readable file or paying
// applyMCPSourceFault's identity-resolution cost on every live-follow call for
// a state that a settled filesystem cannot produce there unaided. Do not "fix"
// either into agreement.
func (t *Tools) resolve(ctx context.Context, source, planID string) (*session.Session, error) {
	if (source == "") == (planID == "") {
		return nil, fmt.Errorf("pass exactly one of source (file path or URL) or plan_id (from list_plans, get_review, or save)")
	}
	if planID != "" {
		return t.openByID(ctx, domain.PlanID(planID))
	}
	if path, readable := localFile(source); readable {
		return session.Open(ctx, t.svc, path) // live-follow: today's behavior
	}
	plan, err := t.svc.ResolvePlan(ctx, canonicalSource(source))
	if err != nil {
		return nil, coachResolvePlanErr(err, source)
	}
	return t.openByID(ctx, plan.ID)
}

// openByID is resolve's one call onto session.OpenByID, shared by both of its
// id-shaped branches so the fault check below is structurally impossible to
// apply to one branch and not the other.
//
// A SUCCESSFUL OpenByID CAN STILL BE SILENTLY STALE. It answers from this
// machine's last REGISTERED version and never touches the plan's own file, so
// a plan whose file has since been deleted opens exactly like a healthy one.
// THE FIX IS TO ASK THE FILE OURSELVES, the same question session.OpenPlan
// asks before it ever reaches OpenByID -- but this door does not call OpenPlan:
// that function is the TUI's list-open door, and routing MCP through it
// would change which session every MCP call gets. applyMCPSourceFault below
// asks the same question independently, at this call site.
func (t *Tools) openByID(ctx context.Context, id domain.PlanID) (*session.Session, error) {
	s, err := session.OpenByID(ctx, t.svc, id)
	if err != nil {
		return nil, coachOpenVersionErr(err)
	}
	applyMCPSourceFault(ctx, t.svc, s)
	return s, nil
}

// applyMCPSourceFault latches Session.SourceFault when s was answered from a
// version rather than its file, and that file no longer says so -- see
// openByID's own comment for why the check is needed at all.
//
// FromSnapshot GATES THE WHOLE CHECK: a LIVE session has just read its file
// successfully, so it has nothing to re-check, and calling session.Open a
// second time over a file just proven readable would only waste the read.
// THIS DOES NOT MAKE THE CHECK RARE: a plan addressed by plan_id is opened
// through OpenVersion, which sets fromSnapshot unconditionally. So the common
// agent path -- get_review on a perfectly healthy plan -- pays this function's
// full cost, not merely the deleted-file path it exists for.
//
// THAT COST IS ACCEPTED AND DELIBERATE, NOT AN OVERSIGHT TO OPTIMISE AWAY. A
// cheaper version exists -- os.Stat(s.Path) alone would catch SourceFileGone
// and SourceFileUnreadable without a second identity resolution -- and it
// would be WRONG rather than merely slower: SourceFileReleased (the file is
// there but resolves to no plan) and SourceFileClaimed (there, but resolves
// to a DIFFERENT plan) can only be told apart from a healthy file by actually
// resolving identity against it, which is what session.Open does and a stat
// cannot. A fast path here would silently drop two of the four source-fault
// states for every MCP caller -- exactly the collapse this check exists to
// undo, reintroduced as a performance fix. The TUI already pays the
// identical cost per list-open (OpenPlan's own unconditional Open call), so
// this is not a new order of cost, only a new caller of it.
//
// REOPEN THIS ONLY ON MEASUREMENT, not suspicion: if a future profile shows
// this second Open/ResolvePlan/Versions round trip actually costing an agent
// session something real, the fix is a cache or a cheaper pre-check ahead of
// it -- not silently narrowing the four states back to two.
//
// The file check itself is session.Open followed by session.SourceFileFault --
// the exact pair session.OpenPlan itself calls -- so the two doors classify a
// failed file identically without sharing a call path at all -- see
// openByID's own comment for why this package avoids that.
func applyMCPSourceFault(ctx context.Context, svc client.PlanService, s *session.Session) {
	if s == nil || !s.FromSnapshot() {
		return
	}
	fileSess, err := session.Open(ctx, svc, s.Path)
	s.SourceFault = session.SourceFileFault(s.Plan.ID, s.Path, fileSess, err)
}

// sourceFaultState projects Session.SourceFault into the identical result value
// GetReviewResult.SourceFault carries -- session.SourceFileState.String()'s own
// four words, or "" when there is nothing to report, never a zero value that
// could be misread as a state (get_review's own rule, restated here rather
// than re-derived).
//
// THE WRITE VERBS REPORT IT TOO, AND STILL SUCCEED. Comment, CommentSection,
// Reply, Resolve, Rehome and Approve call this
// at their own success return because each resolves a plan through t.resolve
// exactly as get_review does, so each can carry a snapshot session whose file
// has stopped answering for it, and each writes its review fact against that
// snapshot regardless: THE WRITE IS NEVER REFUSED FOR THIS. An agent that just
// left a comment against a plan's registered snapshot needs to know that is
// what it did, the same reason get_review's own field exists. A test that
// asserted a refusal here would be asserting against that ruling, not proving
// one.
//
// THIS IS THE RULE THIS FUNCTION APPLIES, NOT A GUARD IT ENFORCES: nothing below
// inspects s.SourceFault to decide whether to proceed, because there is nothing
// to decide -- the human's panel (app/'s sourceFaultText) exists because a
// human can answer one; an agent cannot, and "keep it in draftplane with no
// file" is a supported destination, not a wrong answer requiring a refusal to
// prevent.
//
// save and download reach it by doors of their own rather than t.resolve:
//
//   - save builds its session with OpenSupplied or OpenSuppliedForPlan and
//     calls applyMCPSourceFault itself, after s.Register -- see Save's own call
//     site for the ordering argument. No sentence on this surface may name
//     save as the remedy for the state this field reports:
//     save accepts the write and leaves SourceHint pointing at the dead path
//     rather than curing anything.
//   - download is a read that happens to produce a file, and it refuses
//     outright for exactly the two states (released, claimed) where get_review
//     withholds content, so only gone and unreadable ever reach its result --
//     see DownloadResult.SourceFault.
func sourceFaultState(s *session.Session) string {
	if s.SourceFault == nil {
		return ""
	}
	return s.SourceFault.State.String()
}

// coachOpenVersionErr adds agent-facing guidance to a session.OpenByID
// failure, once a plan id (passed directly, or a source that resolved to one)
// is in hand: it names no plan or version this machine holds.
func coachOpenVersionErr(err error) error {
	return fmt.Errorf("%w — plan_id must come from list_plans, get_review, or save", err)
}

// coachResolvePlanErr adds agent-facing guidance to a ResolvePlan failure,
// resolve's source-keyed counterpart to coachOpenVersionErr above.
// client.ErrNoPlan means the source is unknown to this machine -- save it, the
// generic advice below.
//
// Deliberately its own function rather than a call to coachOpenVersionErr:
// that function tells the agent "plan_id must come from list_plans,
// get_review, or save," which is nonsense here -- this call never had a
// plan_id, only a source.
//
// THE GENERIC BRANCH BELOW WRAPS ITS CAUSE (%w) so a caller can tell a genuine
// miss from an unexpected failure by something other than the message text. The
// cause travels at the END of the sentence deliberately, so the coaching an
// agent must act on is still the first thing it reads.
//
// ⚠️ THE GENERIC BRANCH ALSO CARRIES A REGRESSION THE OPEN-FOR-READ FIX BELOW
// CREATED, recorded here because this is where it surfaces. Before that fix,
// localFile decided "readable" with os.Stat alone, so a permission-denied
// file passed that check, took resolve's live-follow branch, and
// session.Open's own os.ReadFile failure reached the caller with the real
// OS cause attached. The fix correctly made localFile ask for a real read --
// so that same file now answers readable=false and resolve falls through to
// ResolvePlan instead, which is a bare SourceHint-index lookup that never
// touches the filesystem and answers only client.ErrNoPlan. For a plan
// draftplane already knows about, that is exactly right: the fault belongs on
// applyMCPSourceFault's SourceFileUnreadable path, not a hard error here. But
// for a source NOTHING has ever registered, there is no such path to land on,
// and this generic branch is the only voice left -- one that, unpatched, says
// "no plan known ... if you have its content, save it first," which reads as
// "this file was simply never seen" and discards the one actionable fact
// (permission denied, not absent) the caller had. sourceOpenFailure below
// restores it without touching localFile, which already asks the right
// question, and without a second read on the path localFile itself would
// take (it only runs here, on the failure branch, never on the live-follow
// one).
func coachResolvePlanErr(err error, source string) error {
	if cause := sourceOpenFailure(source); cause != nil {
		return fmt.Errorf("no plan known for source %q, and the file itself could not be opened (%v) — that "+
			"failure, not absence, is very likely why nothing is registered for it; draftplane can't read this "+
			"file any more than that error already told you, so save its content directly if you hold it some "+
			"other way (%w)", source, cause, err)
	}
	return fmt.Errorf("no plan known for source %q — if you have its content, save it first; draftplane can only read local files itself (%w)", source, err)
}

// sourceOpenFailure is coachResolvePlanErr's own diagnostic probe, reached
// only after ResolvePlan has already answered client.ErrNoPlan (or something
// else the arms above did not recognize) -- never on the live-follow branch,
// and never a second reading of a document anything else is about to read.
// It is deliberately NOT a second copy of localFile's readability question:
// localFile already asks the right question, and this asks a narrower one --
// specifically, whether a NEVER-REGISTERED local path exists and could not be
// opened, so the message above can name the real OS cause instead of implying
// the file was simply never there. See coachResolvePlanErr's own doc comment
// for the regression this restores and why this fix matters.
//
// nil for a URL source (never local, client.URLSource), a path
// that plain does not exist (fs.ErrNotExist -- the generic wording is already
// correct for that: nothing was ever here to register), or one that opens
// fine (a directory included -- IsDir is not asked here, because os.Open
// succeeding is already proof there is nothing to diagnose about THIS
// question, whatever ResolvePlan's own miss means instead).
func sourceOpenFailure(source string) error {
	if client.URLSource(source) {
		return nil
	}
	raw := strings.TrimPrefix(source, "file://")
	abs, err := filepath.Abs(raw)
	if err != nil {
		return nil
	}
	f, err := os.Open(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	_ = f.Close()
	return nil
}

// localFile reports whether source names a readable, regular file on this
// machine. A file:// URL is stripped to a bare path first; a bare path with
// no scheme is tried as-is; any other URL scheme is never local (see
// client.URLSource, the shared home for that split). readable is false
// for a directory, a missing path, or anything draftplane cannot open.
//
// ⚠️ THIS ASKS THE QUESTION ITS SIX CALLERS' OWN DOC COMMENTS ALREADY
// CLAIMED IT ASKED -- "can draftplane read the document this call
// would land on, RIGHT NOW" -- which os.Stat alone cannot answer: Stat
// succeeds on a permission-denied file that a real read cannot open, so the
// Stat-only version of this function called such a file readable and every
// caller downstream (refuseSaveForReadableSource chief among them) inherited
// the lie. Measured with a chmod 0000 fixture: of the four filesystem shapes
// considered, a dangling symlink and an unsearchable parent directory already
// made Stat and a real open agree, and a directory is handled by the IsDir
// guard below -- the file's own permission bits were the one live gap.
//
// THE IsDir GUARD STAYS, AND THE OPEN RUNS AFTER IT, DELIBERATELY IN THAT
// ORDER. os.Open SUCCEEDS ON A DIRECTORY -- only a later read fails with
// EISDIR -- so dropping this guard and trusting Open alone would silently
// reintroduce a directory-shaped bug this function has never had.
//
// os.Open, NOT os.ReadFile: the same kernel permission check, without a
// second full read of a document session.Open is about to read anyway.
func localFile(source string) (path string, readable bool) {
	if client.URLSource(source) {
		return "", false
	}
	raw := strings.TrimPrefix(source, "file://")
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return "", false
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", false
	}
	_ = f.Close()
	return abs, true
}

// canonicalSource normalizes a source for plan-identity lookups: a file://
// URL or a bare local path canonicalizes to its absolute path, so a plan
// created by path and later addressed via a file:// URL unify onto the same
// plan. Any other URL scheme (notion://, https://, ...) passes through
// verbatim — it already IS the stable identity (client.URLSource).
func canonicalSource(source string) string {
	if client.URLSource(source) {
		return source
	}
	raw := strings.TrimPrefix(source, "file://")
	abs, err := filepath.Abs(raw)
	if err != nil {
		return source
	}
	return abs
}

// planByID finds a plan by id -- save's plan_id door, and the string-to-
// domain.PlanID boundary between an MCP argument and the client.
func (t *Tools) planByID(ctx context.Context, id string) (domain.Plan, error) {
	return t.svc.PlanByID(ctx, domain.PlanID(id))
}

// identify renders whichever of source/plan_id a caller supplied, for error
// text — resolve's XOR check guarantees exactly one is non-empty.
func identify(source, planID string) string {
	if source != "" {
		return source
	}
	return planID
}

// ensurePlan lazily creates the plan exactly as the TUI does: first review
// fact creates it, titled from the doc's first H1 (filename stem fallback),
// by session.Session.InferredTitle's rule.
func ensurePlan(ctx context.Context, s *session.Session) error {
	if s.Exists {
		return nil
	}
	return s.Create(ctx, s.InferredTitle())
}

// conflictCoaching enriches a *client.ConflictError with the coaching every
// write verb that reaches ensureRegistered owes an agent: there is no prompt
// over MCP, so the message has to tell the agent how to unstick itself rather
// than draftplane silently picking re-read-first or overwrite-anyway on its
// behalf. ok is false for every other error, so a caller can fall through to
// its own, more specific coaching unaffected.
//
// %w, never %v: %v loses errors.As's ability to recover the
// *client.ConflictError, and the agent is coached about a thread_id that was
// never wrong instead. TestWriteVerbsRelayVersionConflicts guards every verb
// that can reach ensureRegistered.
//
// The conflict is another writer moving the plan's tip between this call's
// session establishing its base and registering against it. Every MCP verb
// opens a fresh session, so the retried write takes the moved tip as its
// base, and the get_review call before it shows the agent what landed: no
// extra verb, no base argument, no human required.
func conflictCoaching(err error) (wrapped error, ok bool) {
	var conflict *client.ConflictError
	if !errors.As(err, &conflict) {
		return nil, false
	}
	return fmt.Errorf("%w — call get_review with plan_id %s, then retry: reading shows what landed, "+
		"and the retry takes the current tip as its base",
		conflict, conflict.Plan), true
}

// ---- list_plans ----

type ListPlansArgs struct {
	AgentID string `json:"agent_id,omitempty" jsonschema:"identity from an earlier draftplane response this conversation; omit on your very first draftplane call to be issued a fresh one"`
}

type PlanInfo struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	SourceHint string `json:"source"`
}

type ListPlansResult struct {
	Plans []PlanInfo `json:"plans"`
	// Total is emitted always and with no omitempty -- an agent that could not
	// tell "zero plans" from "the field is missing" would have to guess whether
	// the number it was given means anything.
	Total   int    `json:"total" jsonschema:"how many plans there are"`
	AgentID string `json:"agent_id" jsonschema:"this call's identity; carry it forward as agent_id on every later draftplane call this conversation"`
}

// ListPlans answers every plan in this machine's review store.
func (t *Tools) ListPlans(ctx context.Context, args ListPlansArgs) (ListPlansResult, error) {
	ctx, id := t.attribute(ctx, args.AgentID)
	plans, err := t.svc.ListPlans(ctx)
	if err != nil {
		return ListPlansResult{}, err
	}
	out := ListPlansResult{Plans: make([]PlanInfo, 0, len(plans)), Total: len(plans), AgentID: id}
	for _, p := range plans {
		out.Plans = append(out.Plans, PlanInfo{
			ID: string(p.ID), Title: p.Title, SourceHint: p.SourceHint,
		})
	}
	return out, nil
}

// ---- get_review ----

type GetReviewArgs struct {
	Source  string `json:"source,omitempty" jsonschema:"file path or URL identifying the plan; mutually exclusive with plan_id"`
	PlanID  string `json:"plan_id,omitempty" jsonschema:"plan id from list_plans, get_review, or save; mutually exclusive with source"`
	AgentID string `json:"agent_id,omitempty" jsonschema:"identity from an earlier draftplane response this conversation; omit on your very first draftplane call to be issued a fresh one"`
}

type ReviewComment struct {
	// Author is the composite string the TUI renders: an agent identity, a
	// human's login (domain.Attribution.ActorLogin), both
	// ("agent-blue-parakeet-f9 ● dana-loves-coding" -- the agent acting for
	// that human), or this machine's pseudonym when neither is known. See
	// ui.FormatAttribution. Nothing in this build writes ActorLogin, so a
	// login appears only on a review fact whose recorded Attribution already
	// carries one.
	//
	// THE LOGIN AND NOT THE DISPLAY NAME, and the schema below says so
	// because an agent relaying this string is the reader most likely to hand
	// it to a human as "who said this": a display name is free text its owner
	// may set to another person's.
	Author    string `json:"author" jsonschema:"who wrote this: an agent identity, a human's login, \"agent ● login\" when an agent wrote it on a human's behalf, or a machine pseudonym when neither is known"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

type ReviewCandidate struct {
	HeadingPath []string `json:"heading_path"`
	Text        string   `json:"text"`
	Score       float64  `json:"score"`
}

type ReviewThread struct {
	ID          string            `json:"id"`
	Resolved    bool              `json:"resolved"`
	Status      string            `json:"status"` // exact | moved | fuzzy | orphaned
	HeadingPath []string          `json:"heading_path"`
	Section     bool              `json:"section"` // section comment (empty-span anchor)
	MatchedText string            `json:"matched_text,omitempty"`
	Confidence  float64           `json:"confidence,omitempty"`
	Candidates  []ReviewCandidate `json:"candidates,omitempty"` // for orphans: where it might belong
	Comments    []ReviewComment   `json:"comments"`
}

type GetReviewResult struct {
	Plan *PlanInfo `json:"plan"`
	Hash string    `json:"hash"`
	// Content's own doc string used to say plan.source is always the fallback
	// for an absent Content -- true of a healthy live-followed plan, FALSE for
	// source_fault claimed (that path now names a DIFFERENT plan) and a dead
	// end for released.
	Content         string         `json:"content,omitempty" jsonschema:"the document content, included by get_review when draftplane cannot read the plan's document for you. It is absent for a plan draftplane reads from a file on this machine: read the path in plan.source instead, which answers what that file says now rather than the last version registered here -- EXCEPT when source_fault is set: see that field's own description for which of its four states this still holds for and which it does not, because for two of them plan.source is actively wrong or useless advice. It is also absent from save responses (the caller supplied the bytes). When it is present but too large to carry back through a response, call download instead: it writes the same bytes to a path you name and answers with the hash"`
	ApprovedCurrent bool           `json:"approved_current"`
	Threads         []ReviewThread `json:"threads"`
	// SourceFault is the four-state machine-readable signal: which of the four
	// things this plan's own source file did when it stopped answering as
	// this plan's document. See sourceFaultGuidance's doc comment for the
	// human half of the same fact for get_review (Guidance below carries
	// saveSourceFaultGuidance's own words instead, for a save response), and
	// applyMCPSourceFault (resolve's neighbour, and Tools.Save's own late,
	// direct caller too) for exactly which plans this can ever be non-empty
	// for.
	//
	// THE FOUR STATES DO NOT SHARE ONE ANSWER ON WHETHER Content ABOVE IS
	// INCLUDED, and the schema text below says so explicitly rather than
	// leaving it to be inferred from the state name -- see
	// sourceFaultContentNote's own doc comment for the mechanism. A later fix
	// corrected that mechanism rather than merely documenting it: gone
	// and unreadable are now the two states of four that still get their
	// bytes, because neither has a live document to defer to; released and
	// claimed still withhold, correctly, because the file they name genuinely
	// IS readable -- it has simply stopped being this plan's document. NONE
	// OF THAT PARAGRAPH IS TRUE OF A SAVE RESPONSE, where Content is absent
	// for every one of the four states alike (the caller supplied the bytes;
	// see Content's own description) -- the jsonschema text below carries the
	// agent-facing version of this caveat.
	SourceFault string `json:"source_fault,omitempty" jsonschema:"set only when this plan's own source file failed to answer as this plan's current document -- one of four values: gone (the file is not there -- deleted or moved), unreadable (it is there but could not be read), released (it is there but no longer resolves to any plan), or claimed (it is there but now resolves to a DIFFERENT plan). Absent entirely when there is nothing to report -- never a zero value that could be misread as a state. In three of the four the file is still sitting on disk exactly where a human left it; only gone means there is nothing there to read at all. CONTENT ABOVE FOLLOWS A DIFFERENT SPLIT THAN THAT ON A get_review RESPONSE: it is included for gone and unreadable -- neither has a live document to defer to -- and empty for released and claimed, where the file is genuinely readable but is not this plan's document any more; Guidance says so explicitly for this call. Do not assume in either direction, because the two splits (whether the file is on disk, whether content was returned) do not divide the same way, and plan.source is not a reliable fallback for released or claimed (see Content's own description). ON A save RESPONSE THIS FIELD FOLLOWS A THIRD SPLIT, NOT EITHER OF THOSE TWO: content is absent regardless of state (see Content's own description again). released and claimed are refused at save's own door in the ordinary case; the only way either still reaches this field is a concurrent repoint of this exact plan, or another plan taking its path, in the window between that door and this field's own measurement, a race recorded rather than closed (see reviewResult's own doc comment)."`
	Guidance    string `json:"guidance,omitempty"`
	AgentID     string `json:"agent_id" jsonschema:"this call's identity; carry it forward as agent_id on every later draftplane call this conversation"`
}

func (t *Tools) GetReview(ctx context.Context, args GetReviewArgs) (GetReviewResult, error) {
	ctx, id := t.attribute(ctx, args.AgentID)
	s, err := t.resolve(ctx, args.Source, args.PlanID)
	if err != nil {
		return GetReviewResult{}, err
	}
	res, err := reviewResult(ctx, s, true, t.pseudonym)
	if err != nil {
		return GetReviewResult{}, err
	}
	res.AgentID = id
	return res, nil
}

// appendGuidance joins a new sentence onto an existing Guidance value with
// "; ", so two coachings landing in one payload read as one voice instead of
// one silently replacing the other. Appending costs nothing while a payload has
// one producer and loses nothing the day it gains a second.
func appendGuidance(guidance, sentence string) string {
	if sentence == "" {
		return guidance
	}
	if guidance == "" {
		return sentence
	}
	return guidance + "; " + sentence
}

// sourceFaultGuidance renders the four-state taxonomy (session.SourceFileState)
// into the sentence get_review's Guidance carries for this call's own plan, one
// state at a time -- because what is true of SourceFileGone ("there is nothing
// on disk to read") is false of the other three, where the file is still sitting
// exactly where a human left it and something else about it is wrong. See
// session.SourceFileState's own doc comment for the identical rule.
//
// THESE ARE NOT sourceFaultText's SENTENCES REUSED (app/actions.go): those
// name "f"/"o"/"d" keys this surface does not have. Remedy verbs for these
// four states are deliberately out of scope here: an agent that detects the
// state reports it and stops, and a human opens the TUI. gone and
// unreadable's sentences end there.
//
// ⚠️ CLAIMED AND RELEASED DO NOT STOP AT "OPEN THE TUI", because each has a
// cause specific enough to act on, and naming it is strictly more useful to
// the human who gets this sentence than a bare pointer to the TUI would be.
// Wording that instead opens by naming the path as THIS plan's own source
// file, then closes by saying no plan (or a different plan) is what it
// resolves to, asserts the ownership the rest of the sentence denies, so the
// case arms below avoid that shape. CLAIMED IS DURABLE, NOT A RACE: once two
// plans share a path, the loser reproduces it on every open, across
// restarts, and retrying is a no-op, so its sentence names the conflict as
// standing and the one manual fix that exists. RELEASED SPLITS, because its
// causes have different fixes: a stale caller value a refresh clears, or a
// genuine MCP race a retry clears -- so its sentence leads with refresh and
// names hand-repair only as the fallback when that does not resolve it. See
// the exact wording at each case arm below.
//
// NO SENTENCE HERE NAMES save AS A FIX. refuseSaveForReadableSource
// refuses a save only while the source is READABLE; the instant this plan's
// file goes missing that predicate stops refusing, so save accepts the write
// and leaves SourceHint pointing at the dead path unchanged -- a second silent
// success on this surface, not the first one's cure. Naming it here would tell
// an agent to reach for the one verb that makes this worse, not better.
//
// contentIncluded IS THE CONTENT GATE'S OWN DECISION, threaded through
// rather than re-derived. A simpler stand-in, res.Content != "", would
// conflate "the gate withheld it" with "the string happens to be empty," and
// those are different facts for one population: session.Create has no
// emptiness guard (nor does approve's ensurePlan path, which mints a plan
// with zero content-emptiness check at all -- a long-standing, separate gap
// this fix does not close), so a genuinely empty-content plan is
// constructible. Reviewed and PROVEN by running code: approve a 0-byte file,
// delete it -- gone, one of the two states (unreadable is the other, since
// the open-for-read fix) the gate does not withhold for -- and
// res.Content != "" answers false for a reason that has nothing to do with
// withholding. Threading the gate's own bool instead removes the assumption
// rather than documenting it: it can never disagree with what the gate
// actually did, by construction, whatever that gate's behaviour is or
// becomes (the open-for-read fix changed the gate itself; this line's
// correctness never depended on how). See reviewResult's own call site for
// where this bool is computed --
// the SAME expression that gates res.Content's own assignment, not a second
// copy of it.
//
// readReason is this package's own copy of app/actions.go's identically
// named helper, kept as a separate copy rather than shared because this
// package must not import app/ (this package's own layering rule, restated
// for a one-line function rather than bent for it): the reason clause of a
// file-read failure, with the operation and the path it already names
// elsewhere taken off, fixing a bug on the doors that render
// session.SourceFileError.Err beside its own Path in one parenthetical
// (sourceFaultGuidance's own unreadable arm below, and
// downloadSourceFaultGuidance's), which without this prints the path twice:
// Err is a raw *fs.PathError, and its own Error() already renders "<op>
// <path>: <reason>".
//
// NOT session.readCause, and that is a decision this function does not get
// to revisit: readCause's own doc comment keeps the unstripped *fs.PathError on
// SourceFileError.Err deliberately, because the carrier's job is to hand the
// real cause to whichever renderer meets it, and stripping it there would
// take that choice away from every OTHER caller of Err, most of which do not
// have this function's own duplication to worry about. This function is the
// renderer's own choice, made once, for the two renderers that need it.
func readReason(cause error) string {
	var pe *fs.PathError
	if errors.As(cause, &pe) {
		return pe.Err.Error()
	}
	return fmt.Sprint(cause)
}

// contentEmpty is orthogonal and safe to derive from the bytes themselves
// (unlike contentIncluded, emptiness of the DATA is not being used to infer
// what the GATE decided): it distinguishes "included, and it is this plan's
// real prose" from "included, and this plan's registered version is
// genuinely zero bytes" -- the second is a real, reachable combination (the
// same empty-content plan above, addressed by gone) and reads as a bug if
// left unlabelled next to Content: "".
func sourceFaultGuidance(f *session.SourceFileError, contentIncluded, contentEmpty bool) string {
	if f == nil {
		return ""
	}
	var headline string
	switch f.State {
	case session.SourceFileGone:
		headline = fmt.Sprintf("this plan's source file (%s) is gone or has moved -- there is nothing on disk "+
			"to read anymore; a human needs to open the plan in the TUI for options to fix it", f.Path)
	case session.SourceFileUnreadable:
		headline = fmt.Sprintf("draftplane can't read this plan's source file (%s: %s) -- the file is still on "+
			"disk, but unreadable; a human needs to make the file readable or open the plan in the TUI for options to fix "+
			"it", f.Path, readReason(f.Err))
	case session.SourceFileReleased:
		headline = fmt.Sprintf("this file (%s) no longer resolves to this plan. Your view of "+
			"it might be stale; try refresh to clear it. If this persists after refresh, a human needs "+
			"to open the plan in the TUI for options to fix it", f.Path)
	case session.SourceFileClaimed:
		headline = fmt.Sprintf("another plan is already following this file (%s) -- "+
			"and retrying won't change it; a human needs to open the plan in the TUI for options to fix it", f.Path)
	default:
		return ""
	}
	return headline + "; " + sourceFaultContentNote(contentIncluded, contentEmpty)
}

// sourceFaultContentNote states the content-inclusion ruling, at the one call
// site that needed it: "every response that names a state must also say
// whether bytes were withheld. No exceptions, no inference left to the
// caller." included is the content gate's OWN DECISION (see
// sourceFaultGuidance's doc comment for the reasoning there), not a table
// keyed on f.State, because the gate's real behaviour does not split the way
// the four states do: gone and unreadable are the two of four the gate does
// not withhold for -- neither has a live document to defer to, so the
// registered version is let through. released and claimed read as "the file
// is right there" while ALSO having their content withheld, correctly:
// refuseSaveForReadableSource (via localFile) finds the file genuinely
// readable at that path -- it has simply stopped being this plan's document,
// resolving to no plan or to a different one.
//
// ⚠️ THIS WAS NOT ALWAYS TRUE OF unreadable. Before the open-for-read fix,
// localFile decided "readable" with os.Stat alone, which succeeds on a
// permission-denied file that a real read cannot open, measured with a chmod
// 0000 fixture rather than assumed from the code -- so unreadable trivially
// passed that check and had its content withheld right alongside released and
// claimed, a lopsided 1-of-4 split rather than this principled 2-of-4 one. The
// fix changed localFile itself (an open-for-read after the existing Stat+IsDir
// gate) rather than patching this note's mapping, so it holds for every caller
// of localFile, not only this one.
//
// empty IS A SEPARATE AXIS FROM included, not a fallback reading of it. A
// plan can be BOTH included and empty (approve a 0-byte file, delete it --
// gone, gate does not withhold, s.Content is genuinely "" because that is
// what was registered), and conflating the two would be the identical
// mistake moved one field over instead of fixed. The three cases are
// therefore mutually exclusive by construction: not included; included and
// empty; included and not empty.
//
// NEITHER OF THE FIRST TWO BRANCHES POINTS AT plan.source AS A WORKAROUND.
// For gone that field names a dead path; for the withheld two (released,
// claimed), GetReviewResult.Content's own doc comment already carries the
// reason plan.source is not a substitute (wrong plan for claimed, no plan
// for released) -- restating it here would be a second copy of that fact to
// keep in step with the first. unreadable dropped out of this branch
// entirely once the open-for-read fix landed: its content is included now,
// same as gone's, so there is no "withheld" case left to explain a
// workaround for.
func sourceFaultContentNote(included, empty bool) string {
	switch {
	case !included:
		return "content above is empty for this call: this plan's document cannot currently be read as its " +
			"own file, and there is no other copy on hand to serve instead"
	case empty:
		return "content above is genuinely empty -- this plan's last registered version is zero bytes"
	default:
		return "content above is this plan's last registered version, unaffected by this state"
	}
}

// reviewResult projects a session into the MCP result shared by get_review
// and save: threads placed onto the session's content, plus — for a plan whose
// bytes draftplane holds and whose document it cannot read for you — the
// content itself. includeContent gates the readback: get_review includes it
// because its caller may not hold the bytes (another agent, a later session —
// for a sourceless plan the CAS is the only copy); save omits it because save's
// caller literally just supplied those bytes. pseudonym is threaded through
// rather than read off a receiver since this is a free function shared by both
// callers below.
//
// THE CONTENT GATE IS TWO QUESTIONS, AND THEY ARE Tools.Download's TWO, in its
// order and of its fields: does draftplane hold these bytes at all
// (s.FromSnapshot), and is this plan's own document a file on this machine
// (refuseSaveForReadableSource, of the plan's SourceHint). The second is save's
// predicate, reused rather than re-derived -- it is the ONE place this package
// asks "can draftplane read the document this call is about", and three verbs
// now ask it of one field.
//
// THE INVARIANT WITH download IS THAT BOTH ASK BOTH QUESTIONS, so the two doors
// can never disagree about which plans draftplane hands content over for: what
// get_review inlines is exactly what download would write, and what download
// refuses is exactly what this line omits.
//
// ⚠️ THE TWO GATES CAN DRIFT OUT OF SYNC, AND THAT HAS HAPPENED BEFORE:
// download's own gate changed from asking one question (FromSnapshot alone)
// to asking two before this line did, which left download refusing a
// file-backed plan reached by plan_id while this line went on inlining its
// content, for as long as the two disagreed. The fix was to bring this line
// into step rather than retire the invariant. A reader who finds only one of
// the two gates asking two questions has found a half-applied change, not a
// deliberate asymmetry.
//
// WHY OMITTING IS RIGHT, NOT MERELY SYMMETRICAL, and it is the same fact
// download's doc turns into a refusal. A file-backed plan reached by plan_id
// arrives here from session.OpenVersion -- the plan's latest REGISTERED version
// -- and for such a plan that version is the file AS OF THE LAST REVIEW FACT,
// since every review verb registers the session's content before it writes
// (session.Session.ensureRegistered). Between facts the human edits the document
// and the registered version lags. So the old behaviour handed an agent bytes
// that were not what the human was editing, with NO MARKER saying so, while
// addressing the same plan by its path re-read the file live: one plan, two
// answers, and nothing in the response to tell them apart.
//
// THE CALLER IS NOT STRANDED, which is why this is an omission and not a
// refusal: PlanInfo.SourceHint below hands back the path, and reading it gets
// the CURRENT bytes rather than the lagged ones. The rest of the projection --
// threads, their placements, approval -- is unaffected and is what get_review
// was called for.
//
// ⚠️ A FILE-BACKED PLAN WHOSE FILE IS GONE OR UNREADABLE STILL GETS ITS
// CONTENT, and that is the question the predicate actually asks: "is this
// file readable RIGHT NOW", not "does this plan name a source". localFile
// opens the path (a Stat+IsDir gate first, then a real open-for-read), so
// a vanished or unreadable document answers false and the content comes back
// -- correctly, and it is the whole recovery: the registered version is the
// only copy left, and coaching an agent to a file it cannot read would strand
// it exactly as one it cannot find would. download behaves identically at its
// own gate, which is what keeps the two in step for this row as well as the
// others.
//
// ⚠️ THIS SENTENCE WAS ASPIRATIONAL, NOT DESCRIPTIVE, ABOUT unreadable UNTIL
// THE OPEN-FOR-READ FIX MADE IT TRUE. Before that fix, localFile decided
// "readable" with os.Stat alone, which succeeds on a permission-denied file
// that a real read cannot open -- so an unreadable file's content was
// silently withheld, exactly as a released or claimed file's correctly is,
// contradicting this paragraph's own claim about it. That gap is the best
// evidence this was a correction rather than new behaviour: the code had
// claimed the fix's outcome for as long as this comment existed, and simply
// had not delivered it for one of the two states the claim covers.
func reviewResult(ctx context.Context, s *session.Session, includeContent bool, pseudonym string) (GetReviewResult, error) {
	res := GetReviewResult{Hash: string(s.Hash)}
	// THE ORDER MIRRORS Tools.Download's GATE AND IS NOT A NIL GUARD, said
	// because it reads like one. session.Session.Plan is a domain.Plan VALUE, so
	// there is nothing here that can be nil: a plan-less session carries the ZERO
	// plan, whose SourceHint is "", and refuseSaveForReadableSource answers that
	// safely whichever conjunct runs first. The order is kept because download
	// asks these same two questions in it, and two gates required to agree are
	// easier to hold in step when they read the same way round.
	//
	// ⚠️ "FromSnapshot TRUE IMPLIES A PLAN" IS TRUE OF THIS FUNCTION'S CALLERS
	// AND NOT OF THE PREDICATE, which is a distinction worth keeping because
	// Download's doc states the caller-side version for its own door.
	// session.OpenSupplied sets FromSnapshot true before any plan is created, so
	// the implication does not hold in general -- it holds here because get_review
	// builds its session through t.resolve, whose plan-less arm goes through
	// session.Open and leaves FromSnapshot false, and because the one door that
	// does reach this line through OpenSupplied is save, which passes
	// includeContent false and never evaluates the rest.
	// contentIncluded is captured HERE, once, as its own named value -- not
	// only inlined into the if below -- because the SourceFault block further
	// down needs the identical decision, and a second call to
	// refuseSaveForReadableSource would be a second os.Stat of the same path
	// for no reason, as well as a second place this predicate's exact
	// wording would need to be kept in step with itself.
	contentIncluded := includeContent && s.FromSnapshot() && refuseSaveForReadableSource(s.Plan.SourceHint) == nil
	if contentIncluded {
		res.Content = string(s.Content)
	}
	if !s.Exists {
		res.Guidance = "no plan is registered for this file yet; your first comment or approve creates one automatically"
		return res, nil
	}
	res.Plan = &PlanInfo{ID: string(s.Plan.ID), Title: s.Plan.Title, SourceHint: s.Plan.SourceHint}

	// s.SourceFault is the four-state signal, latched by applyMCPSourceFault
	// (resolve's neighbour) onto a plan's snapshot session whose own source
	// file no longer answers as its own document.
	//
	// THIS BLOCK NEVER GATES res.Content -- it only reads what the existing
	// content gate above already decided, exactly once, and reports it. This
	// is why that reporting is necessary rather than cosmetic: the standing
	// ruling in this function's own doc comment ("A FILE-BACKED PLAN WHOSE
	// FILE IS GONE [OR UNREADABLE] STILL GETS ITS CONTENT") is true of gone
	// and unreadable and FALSE of the other two -- released and claimed
	// correctly withhold, because refuseSaveForReadableSource's os.Open-based
	// readability check finds their file genuinely readable at that path; it
	// has simply stopped being this plan's document. Before the open-for-read
	// fix, that same check withheld res.Content for unreadable too, measured
	// with a chmod 0000 fixture -- a lopsided 1-of-4 split corrected to a
	// principled 2-of-4 one.
	//
	// sourceFaultGuidance IS HANDED contentIncluded ITSELF, NOT
	// res.Content != "" -- the simpler stand-in would be wrong: a genuinely
	// empty-content plan -- constructible via approve, which mints a plan
	// with no emptiness check at all -- has contentIncluded true and
	// res.Content == "" AT THE SAME TIME, for gone specifically, since gone
	// and unreadable are the two
	// states the gate never withholds for. res.Content != "" would have
	// called that "withheld" and been wrong; contentIncluded, being the
	// gate's own decision rather than a reading of its result, cannot make
	// that mistake. res.Content == "" is passed too, but only to describe the
	// DATA (genuinely empty vs. real prose) once inclusion is already
	// settled -- never to decide inclusion itself.
	//
	// TWO READS OF THE SAME PATH, AT TWO DIFFERENT INSTANTS, NOT ONE SHARED
	// READ -- worth stating here because this is where a reader meets both
	// answers side by side and could otherwise assume they came from one
	// look at the file. s.SourceFault was decided earlier, inside t.resolve's
	// call to applyMCPSourceFault (session.Open of s.Path, at openByID time);
	// contentIncluded above is refuseSaveForReadableSource's OWN os.Open of
	// that identical path, made fresh, later, back in this function. Nothing
	// between the two calls shares a read or serializes them against each
	// other -- they simply agree, on every fixture this package's tests hold
	// the file still for, because the file does not move during a single
	// request. NOTHING STRUCTURAL GUARANTEES THAT: a file that changed state
	// (deleted, chmod'd, replaced) in the gap between the two opens would
	// leave res.SourceFault describing the FIRST read while contentIncluded
	// -- and so res.Content -- reflects the SECOND, an agreement this
	// response would then merely assert rather than have proven. Narrow in
	// practice (two syscalls apart inside one handler, not two calls apart in
	// time), and not this function's to close by sharing the read: see
	// session.Session.SourceFault's own "TWO WRITERS IS SAFE HERE" paragraph
	// and resolve's own doc comment (the replayed released/claimed races) for
	// how this package otherwise handles a race window it cannot design away
	// -- by recording it, not by restructuring around it.
	if s.SourceFault != nil {
		res.SourceFault = sourceFaultState(s)
		// Appended, not assigned -- see appendGuidance's own doc comment.
		res.Guidance = appendGuidance(res.Guidance,
			sourceFaultGuidance(s.SourceFault, contentIncluded, res.Content == ""))
	}

	approved, err := s.ApprovedCurrent(ctx)
	if err != nil {
		return GetReviewResult{}, err
	}
	res.ApprovedCurrent = approved
	placements, err := s.Placements(ctx)
	if err != nil {
		return GetReviewResult{}, err
	}
	for _, p := range placements {
		th := ReviewThread{
			ID:          string(p.Thread.ID),
			Resolved:    p.Thread.Resolved,
			Status:      string(p.Status),
			HeadingPath: p.Anchor.HeadingPath,
			Section:     p.Thread.Anchor.Span == "",
			MatchedText: p.MatchedText,
			Confidence:  p.Confidence,
		}
		// Orphaned placements carry no current anchor; fall back to the
		// thread's original heading path so the agent still sees where the
		// comment used to live.
		if len(th.HeadingPath) == 0 {
			th.HeadingPath = p.Thread.Anchor.HeadingPath
		}
		for _, c := range p.Candidates {
			th.Candidates = append(th.Candidates, ReviewCandidate{
				HeadingPath: c.HeadingPath, Text: c.Text, Score: c.Score,
			})
		}
		for _, c := range p.Thread.Comments {
			th.Comments = append(th.Comments, ReviewComment{
				Author: ui.FormatAttribution(c.Attribution, pseudonym), Body: c.Body, CreatedAt: c.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			})
		}
		res.Threads = append(res.Threads, th)
	}
	return res, nil
}

// ---- comment / comment_section ----

type CommentArgs struct {
	Source      string   `json:"source,omitempty" jsonschema:"file path or URL identifying the plan; mutually exclusive with plan_id"`
	PlanID      string   `json:"plan_id,omitempty" jsonschema:"plan id from list_plans, get_review, or save; mutually exclusive with source"`
	Quote       string   `json:"quote" jsonschema:"exact text quoted from the document (3+ words) that the comment anchors to; prefer text from a single paragraph or block, since a quote crossing a block boundary anchors to whichever block it overlaps most"`
	HeadingPath []string `json:"heading_path,omitempty" jsonschema:"disambiguates when the quote appears in more than one section"`
	Body        string   `json:"body"`
	AgentID     string   `json:"agent_id,omitempty" jsonschema:"identity from an earlier draftplane response this conversation; omit on your very first draftplane call to be issued a fresh one"`
}

type ThreadResult struct {
	ThreadID string `json:"thread_id"`
	// SourceFault is the four-state signal, carried here for the reason
	// sourceFaultState's own doc comment gives: THE WRITE ABOVE STILL
	// SUCCEEDED whether or not this plan's source file is currently answering
	// as its own document, and an agent that just left a comment against a
	// cached snapshot needs to know that is what it did.
	SourceFault string `json:"source_fault,omitempty" jsonschema:"set only when this plan's own source file failed to answer as its current document at the moment of this call -- one of four values: gone (the file is not there -- deleted or moved), unreadable (it is there but could not be read), released (it is there but no longer resolves to any plan), or claimed (it is there but now resolves to a DIFFERENT plan); see get_review's own source_fault field for the full account. This call still succeeded and was recorded normally -- none of the four states refuses a write. Absent entirely when there is nothing to report, never a zero value that could be misread as a state."`
	AgentID     string `json:"agent_id" jsonschema:"this call's identity; carry it forward as agent_id on every later draftplane call this conversation"`
}

func (t *Tools) Comment(ctx context.Context, args CommentArgs) (ThreadResult, error) {
	ctx, id := t.attribute(ctx, args.AgentID)
	s, err := t.resolve(ctx, args.Source, args.PlanID)
	if err != nil {
		return ThreadResult{}, err
	}
	anchor, err := session.AnchorForBlockText(string(s.Content), args.Quote, args.HeadingPath)
	if err != nil {
		return ThreadResult{}, fmt.Errorf("%v — quote at least 3 consecutive words EXACTLY as they appear in the document; if the quote occurs in multiple sections, pass heading_path (e.g. [\"Doc Title\",\"Section\"]) to pick one", err)
	}
	if err := ensurePlan(ctx, s); err != nil {
		return ThreadResult{}, err
	}
	th, err := s.Comment(ctx, anchor, strings.TrimSpace(args.Body))
	if err != nil {
		if wrapped, ok := conflictCoaching(err); ok {
			return ThreadResult{}, wrapped
		}
		return ThreadResult{}, err
	}
	return ThreadResult{ThreadID: string(th.ID), SourceFault: sourceFaultState(s), AgentID: id}, nil
}

type CommentSectionArgs struct {
	Source      string   `json:"source,omitempty" jsonschema:"file path or URL identifying the plan; mutually exclusive with plan_id"`
	PlanID      string   `json:"plan_id,omitempty" jsonschema:"plan id from list_plans, get_review, or save; mutually exclusive with source"`
	HeadingPath []string `json:"heading_path" jsonschema:"full heading path of the section, from the document title down, e.g. [\"Doc Title\",\"Design\"]"`
	Body        string   `json:"body"`
	AgentID     string   `json:"agent_id,omitempty" jsonschema:"identity from an earlier draftplane response this conversation; omit on your very first draftplane call to be issued a fresh one"`
}

func (t *Tools) CommentSection(ctx context.Context, args CommentSectionArgs) (ThreadResult, error) {
	ctx, id := t.attribute(ctx, args.AgentID)
	s, err := t.resolve(ctx, args.Source, args.PlanID)
	if err != nil {
		return ThreadResult{}, err
	}
	anchor, err := session.SectionAnchor(string(s.Content), args.HeadingPath)
	if err != nil {
		return ThreadResult{}, fmt.Errorf("%v — heading_path must be the FULL path from the document title down; call get_review to see each thread's heading_path for examples", err)
	}
	if err := ensurePlan(ctx, s); err != nil {
		return ThreadResult{}, err
	}
	th, err := s.Comment(ctx, anchor, strings.TrimSpace(args.Body))
	if err != nil {
		if wrapped, ok := conflictCoaching(err); ok {
			return ThreadResult{}, wrapped
		}
		return ThreadResult{}, err
	}
	return ThreadResult{ThreadID: string(th.ID), SourceFault: sourceFaultState(s), AgentID: id}, nil
}

// ---- reply / resolve ----

type ReplyArgs struct {
	Source   string `json:"source,omitempty" jsonschema:"file path or URL identifying the plan; mutually exclusive with plan_id"`
	PlanID   string `json:"plan_id,omitempty" jsonschema:"plan id from list_plans, get_review, or save; mutually exclusive with source"`
	ThreadID string `json:"thread_id" jsonschema:"a thread id from get_review"`
	Body     string `json:"body"`
	AgentID  string `json:"agent_id,omitempty" jsonschema:"identity from an earlier draftplane response this conversation; omit on your very first draftplane call to be issued a fresh one"`
}

type OKResult struct {
	OK bool `json:"ok"`
	// SourceFault -- see ThreadResult.SourceFault's own doc comment and
	// sourceFaultState's: the write above still succeeded regardless of this
	// plan's file state, and this reports that state rather than gating on
	// it.
	SourceFault string `json:"source_fault,omitempty" jsonschema:"set only when this plan's own source file failed to answer as its current document at the moment of this call -- one of four values: gone (the file is not there -- deleted or moved), unreadable (it is there but could not be read), released (it is there but no longer resolves to any plan), or claimed (it is there but now resolves to a DIFFERENT plan); see get_review's own source_fault field for the full account. This call still succeeded and was recorded normally -- none of the four states refuses a write. Absent entirely when there is nothing to report, never a zero value that could be misread as a state."`
	AgentID     string `json:"agent_id" jsonschema:"this call's identity; carry it forward as agent_id on every later draftplane call this conversation"`
}

func (t *Tools) Reply(ctx context.Context, args ReplyArgs) (OKResult, error) {
	ctx, id := t.attribute(ctx, args.AgentID)
	s, err := t.resolve(ctx, args.Source, args.PlanID)
	if err != nil {
		return OKResult{}, err
	}
	if !s.Exists {
		return OKResult{}, fmt.Errorf("no plan exists for %s yet — there are no threads to reply to; use comment to start one", identify(args.Source, args.PlanID))
	}
	// %v rather than %w is safe here only because Reply never reaches
	// ensureRegistered: a bad thread id is the sole failure, so there is no
	// *client.ConflictError chain to preserve. See Rehome, which does.
	if _, err := s.Reply(ctx, domain.ThreadID(args.ThreadID), strings.TrimSpace(args.Body)); err != nil {
		return OKResult{}, fmt.Errorf("%v — thread_id must come from get_review", err)
	}
	return OKResult{OK: true, SourceFault: sourceFaultState(s), AgentID: id}, nil
}

type ResolveArgs struct {
	Source   string `json:"source,omitempty" jsonschema:"file path or URL identifying the plan; mutually exclusive with plan_id"`
	PlanID   string `json:"plan_id,omitempty" jsonschema:"plan id from list_plans, get_review, or save; mutually exclusive with source"`
	ThreadID string `json:"thread_id"`
	Resolved bool   `json:"resolved" jsonschema:"true to resolve, false to reopen"`
	AgentID  string `json:"agent_id,omitempty" jsonschema:"identity from an earlier draftplane response this conversation; omit on your very first draftplane call to be issued a fresh one"`
}

func (t *Tools) Resolve(ctx context.Context, args ResolveArgs) (OKResult, error) {
	ctx, id := t.attribute(ctx, args.AgentID)
	s, err := t.resolve(ctx, args.Source, args.PlanID)
	if err != nil {
		return OKResult{}, err
	}
	if !s.Exists {
		return OKResult{}, fmt.Errorf("no plan exists for %s yet", identify(args.Source, args.PlanID))
	}
	// Same as Reply: SetResolved records a review fact without registering a
	// version, so the %v wrap cannot swallow a conflict. See Rehome.
	if err := s.Resolve(ctx, domain.ThreadID(args.ThreadID), args.Resolved); err != nil {
		return OKResult{}, fmt.Errorf("%v — thread_id must come from get_review", err)
	}
	return OKResult{OK: true, SourceFault: sourceFaultState(s), AgentID: id}, nil
}

// ---- rehome ----

type RehomeArgs struct {
	Source      string   `json:"source,omitempty" jsonschema:"file path or URL identifying the plan; mutually exclusive with plan_id"`
	PlanID      string   `json:"plan_id,omitempty" jsonschema:"plan id from list_plans, get_review, or save; mutually exclusive with source"`
	ThreadID    string   `json:"thread_id" jsonschema:"an orphaned thread id from get_review"`
	Quote       string   `json:"quote,omitempty" jsonschema:"exact document text to re-anchor the thread to (span rehome); prefer text from a single paragraph or block, since a quote crossing a block boundary anchors to whichever block it overlaps most; mutually exclusive with heading_path"`
	HeadingPath []string `json:"heading_path,omitempty" jsonschema:"full heading path to re-anchor the thread to as a section comment; mutually exclusive with quote"`
	AgentID     string   `json:"agent_id,omitempty" jsonschema:"identity from an earlier draftplane response this conversation; omit on your very first draftplane call to be issued a fresh one"`
}

func (t *Tools) Rehome(ctx context.Context, args RehomeArgs) (OKResult, error) {
	ctx, id := t.attribute(ctx, args.AgentID)
	if (args.Quote == "") == (len(args.HeadingPath) == 0) {
		return OKResult{}, fmt.Errorf("pass exactly one of quote (re-anchor to text) or heading_path (re-anchor to a section)")
	}
	s, err := t.resolve(ctx, args.Source, args.PlanID)
	if err != nil {
		return OKResult{}, err
	}
	if !s.Exists {
		return OKResult{}, fmt.Errorf("no plan exists for %s yet", identify(args.Source, args.PlanID))
	}
	var anchor reanchor.Anchor
	if args.Quote != "" {
		anchor, err = session.AnchorForBlockText(string(s.Content), args.Quote, nil)
		if err != nil {
			return OKResult{}, fmt.Errorf("%v — quote 3+ consecutive words exactly as they appear in the CURRENT document", err)
		}
	} else {
		anchor, err = session.SectionAnchor(string(s.Content), args.HeadingPath)
		if err != nil {
			return OKResult{}, fmt.Errorf("%v — heading_path must exist in the CURRENT document; call get_review for the live paths", err)
		}
	}
	if err := s.RehomeToAnchor(ctx, domain.ThreadID(args.ThreadID), anchor); err != nil {
		// Rehome is the one thread-id verb that writes a version:
		// RehomeToAnchor goes through ensureRegistered, so it can fail the
		// compare-and-swap, and the thread_id coaching below is not merely
		// lossy for that case, it is WRONG (the thread id was fine, the tip
		// moved) -- an agent told to re-fetch thread ids would loop.
		if wrapped, ok := conflictCoaching(err); ok {
			return OKResult{}, wrapped
		}
		return OKResult{}, fmt.Errorf("%v — thread_id must come from get_review", err)
	}
	return OKResult{OK: true, SourceFault: sourceFaultState(s), AgentID: id}, nil
}

// ---- approve ----

type ApproveArgs struct {
	Source  string `json:"source,omitempty" jsonschema:"file path or URL identifying the plan; mutually exclusive with plan_id"`
	PlanID  string `json:"plan_id,omitempty" jsonschema:"plan id from list_plans, get_review, or save; mutually exclusive with source"`
	AgentID string `json:"agent_id,omitempty" jsonschema:"identity from an earlier draftplane response this conversation; omit on your very first draftplane call to be issued a fresh one"`
}

type ApproveResult struct {
	OK   bool   `json:"ok"`
	Hash string `json:"hash" jsonschema:"the exact content hash this approval binds to"`
	// SourceFault -- see ThreadResult.SourceFault's own doc comment and
	// sourceFaultState's: the approval above still succeeded and binds to
	// Hash exactly as recorded, regardless of this plan's file state; this
	// reports that state rather than gating on it.
	SourceFault string `json:"source_fault,omitempty" jsonschema:"set only when this plan's own source file failed to answer as its current document at the moment of this call -- one of four values: gone (the file is not there -- deleted or moved), unreadable (it is there but could not be read), released (it is there but no longer resolves to any plan), or claimed (it is there but now resolves to a DIFFERENT plan); see get_review's own source_fault field for the full account. This call still succeeded and was recorded normally -- none of the four states refuses a write. Absent entirely when there is nothing to report, never a zero value that could be misread as a state."`
	AgentID     string `json:"agent_id" jsonschema:"this call's identity; carry it forward as agent_id on every later draftplane call this conversation"`
}

func (t *Tools) Approve(ctx context.Context, args ApproveArgs) (ApproveResult, error) {
	ctx, id := t.attribute(ctx, args.AgentID)
	s, err := t.resolve(ctx, args.Source, args.PlanID)
	if err != nil {
		return ApproveResult{}, err
	}
	if err := ensurePlan(ctx, s); err != nil {
		return ApproveResult{}, err
	}
	if err := s.Approve(ctx); err != nil {
		if wrapped, ok := conflictCoaching(err); ok {
			return ApproveResult{}, wrapped
		}
		return ApproveResult{}, err
	}
	return ApproveResult{OK: true, Hash: string(s.Hash), SourceFault: sourceFaultState(s), AgentID: id}, nil
}

// ---- save ----

// saveCapabilityMsg refuses save for a plan whose source draftplane can read
// itself.
//
// IT NAMES A REMEDY PER MISTAKE, AND ITS TWO CALL SITES ARE TWO DIFFERENT
// MISTAKES. One fires on a plan that ALREADY tracks a human's document
// (s.Plan.SourceHint) -- there the answer is to edit that file and use the
// review verbs. The other fires on a path the CALLER just named (args.Source),
// and an agent that names a path is usually naming the staging copy it wrote
// itself -- there "edit it and use the review verbs" coaches it into having
// draftplane track a file that is about to be deleted, which is the exact
// outcome the staging-file rule exists to prevent. The clauses SELF-SELECT on
// what the caller did, which is what lets one sentence serve both doors
// rather than splitting a predicate whose whole point is that the doors
// answer identically.
//
// THE STAGING CLAUSE LATER GAINED A SECOND DOOR AND IS STILL ONE CLAUSE.
// content_from takes that same staging path, reads it once and stores
// nothing, so an agent that staged a document precisely because it was too large
// to carry in context no longer has to choose between naming the path (refused,
// and rightly) and pulling the bytes back through the conversation. The
// prohibition was always about REGISTRATION and never about the path: a source
// is a path draftplane follows, and content_from is a path it opens once. Both
// remedies hang off the same "if you named a staging copy of your own" test, so
// the self-selection above is unchanged -- the caller who answers yes to it is
// handed two ways to do the one thing they were trying to do.
//
// ⚠️ BOTH REMEDIES REPEAT "OMIT SOURCE", AND THE REPETITION IS THE FIX. The
// second clause first read "or hand that same path over as content_from",
// which an agent can read as coordinate with the first -- keep source, add
// content_from -- and refuseSaveForReadableSource asks about args.Source
// REGARDLESS of what content_from holds, so that retry earns the byte-identical
// refusal it just read. A remedy a caller can follow into the same door is
// worse than one door named twice, which is why the sentence is redundant on
// purpose rather than tightened.
const saveCapabilityMsg = "this plan's source is a readable file on this machine, and draftplane reads those itself — if it is a document a human works in, edit it with your own tools and use the review verbs directly; if you named a staging copy of your own, do not name it at all: omit source and save the content alone, or omit source and hand that same path over as content_from instead, which reads it once and remembers nothing"

// refuseSaveForReadableSource is the ONE predicate both of save's doors apply,
// asking one question -- "can draftplane read the document this call would land
// on?" -- of whichever source names that document. A function rather than the
// stat call inlined at each site, so the two doors answer identically rather
// than merely similarly.
//
// ⚠️ THIS PREDICATE SILENTLY STOPS APPLYING THE MOMENT THE FILE GOES
// MISSING OR UNREADABLE, AND THAT IS WHAT LETS save ACCEPT A WRITE AGAINST A
// PLAN WHOSE SourceHint STILL POINTS AT A DEAD PATH. localFile answers false
// for a vanished or unreadable file exactly as it does for any other
// unreadable source, so this function returns nil for those two -- not
// because there is nothing left to protect, but because the file this
// refusal exists to keep draftplane from overwriting can no longer be read to
// prove it is still that live document. save then proceeds: it registers the
// supplied bytes, and OpenSuppliedForPlan builds the resulting session with
// Path: plan.SourceHint unchanged -- nothing anywhere blanks SourceHint on a
// save, so the plan goes on naming the same dead path after the write as
// before it. That was settled from source rather than hypothesised: save is
// not the remedy for a source-faulted plan, and no sentence on this surface
// may present it as one. Whether save should
// instead convert such a plan to sourceless on write is a separate,
// deliberately unresolved product question -- not a defect in this
// predicate, which is doing exactly what its one question asks of a file it
// cannot read.
//
// ⚠️ THIS PARAGRAPH ITSELF ONCE NAMED released AND claimed ALONGSIDE
// vanished AND unreadable, AND THAT WAS THE ERROR: an over-generalisation
// from the two states it was actually derived from to two more where the
// claim is false -- the identical defect shape this whole line of work exists
// to catch, found this time inside a record written to prevent it. For
// released and claimed the file is PRESENT AND READABLE -- it has simply
// stopped being this plan's document, or become a different plan's --
// localFile answers TRUE for it, not false, and this predicate REFUSES rather
// than returning nil. TestDownloadStillRefusesReleasedAndClaimed drives both
// states through this exact function and pins the refusal. The finding above
// is unchanged by this correction: it was always about gone and
// unreadable alone, the two states where the file itself cannot be read to
// prove anything, and the sentence now says only that.
func refuseSaveForReadableSource(source string) error {
	if _, readable := localFile(source); readable {
		return errors.New(saveCapabilityMsg)
	}
	return nil
}

type SaveArgs struct {
	Content string `json:"content,omitempty" jsonschema:"the plan's complete current markdown content, inline; mutually exclusive with content_from — pass exactly one"`
	// ContentFrom's own name is content_from, not content_path,
	// deliberately not symmetric with GetReviewArgs.Source. source (and
	// get_review's own live-follow of it) is remembered and re-read on every
	// later call; a path named here is read ONCE, right now, at this call,
	// and then forgotten — draftplane keeps no reference to it afterward. A
	// name that looked like source's would hide exactly that difference.
	ContentFrom string `json:"content_from,omitempty" jsonschema:"path to a file holding the plan's complete current markdown content; read once, right now, and not remembered afterward (unlike get_review's source, which draftplane keeps following) — mutually exclusive with content"`
	Source      string `json:"source,omitempty" jsonschema:"stable reference for this plan (URL etc.) — creates the plan on first save; omit with plan_id to update, omit both for a sourceless plan"`
	PlanID      string `json:"plan_id,omitempty" jsonschema:"update an existing plan by id; mutually exclusive with source"`
	AgentID     string `json:"agent_id,omitempty" jsonschema:"identity from an earlier draftplane response this conversation; omit on your very first draftplane call to be issued a fresh one"`
}

// maxSuppliedContentBytes bounds what resolveSuppliedContent will ever hand
// back, whichever door produced it. Nothing in client/ bounds plan content, so
// this door is the only guard there is. A markdown plan has no business
// approaching 10 MB.
const maxSuppliedContentBytes = 10 * 1024 * 1024

// resolveSuppliedContent turns save's two content doors -- inline content and
// content_from, a path read once right here and then forgotten -- into the
// bytes to register, applying the one rule both doors obey identically: the
// exclusivity below, the regular-file requirement, the size ceiling and the
// empty-content refusal.
//
// EXACTLY ONE OF content OR contentFrom -- never both, never neither. Neither
// is refused rather than merely defaulted because SaveArgs.Content is a bare
// string: an omitted field and an explicitly empty one are the identical Go
// zero value, so there is no way to tell "I meant to send zero bytes" apart
// from "I forgot the argument," and the honest answer is to refuse both the
// same way. The message names both doors, not just the one a caller happened
// to try, so an agent reads what it could have done instead.
//
// content_from's file check is info.Mode().IsRegular(), not localFile's (this
// package's, above): that predicate checks only !info.IsDir(), which answers
// true for a character device or a fifo -- naming /dev/zero here would read
// until this process runs out of memory, and a fifo would block forever,
// ignoring ctx entirely, since neither os.Stat nor os.ReadFile takes one.
// localFile itself is not reused for a second reason beyond the weaker check:
// it also answers false for a URL source, a distinction meaningless for a
// bare path that is read once and never becomes an identity.
//
// content_from is deliberately NOT run through refuseSaveForReadableSource:
// that predicate asks "does draftplane already read this document itself,"
// which is a question about source's role as a plan's ADDRESS. content_from
// names no plan and is never remembered as one -- serverInstructions' own doc
// comment draws that distinction out in full; this function does not restate
// it.
func resolveSuppliedContent(content, contentFrom string) ([]byte, error) {
	if (content == "") == (contentFrom == "") {
		return nil, fmt.Errorf("pass exactly one of content (inline bytes) or content_from " +
			"(a file path read once, right now, and not remembered)")
	}

	data := []byte(content)
	if contentFrom != "" {
		info, err := os.Stat(contentFrom)
		if err != nil {
			return nil, fmt.Errorf("content_from %q: %w", contentFrom, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("content_from %q: not a regular file", contentFrom)
		}
		// info.Size() is a PRE-CHECK, not the guarantee -- it answers "is this
		// worth reading at all," off the SAME os.Stat already taken for the
		// IsRegular check above (a second os.Stat here would be a second
		// answer to a question this function already asked). It is what keeps
		// a file the size of a disk image from ever reaching os.ReadFile -- a
		// ceiling that fires only after the bytes are already resident in
		// memory has defeated its own purpose. The len(data) check below stays rather
		// than being replaced by this one: a regular file can grow between
		// this stat and the read that follows it, so the post-read count is
		// still the authoritative answer and this is only how early the
		// common (non-adversarial, non-racing) case is caught.
		if info.Size() > maxSuppliedContentBytes {
			return nil, fmt.Errorf("content_from %q is %d bytes, over this door's %d-byte ceiling", contentFrom, info.Size(), maxSuppliedContentBytes)
		}
		data, err = os.ReadFile(contentFrom)
		if err != nil {
			return nil, fmt.Errorf("content_from %q: %w", contentFrom, err)
		}
	}

	if len(data) > maxSuppliedContentBytes {
		return nil, fmt.Errorf("content is %d bytes, over this door's %d-byte ceiling", len(data), maxSuppliedContentBytes)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("save refuses empty content -- pass non-empty bytes via content or content_from")
	}
	return data, nil
}

// saveSourceFaultGuidance renders all four states into the sentence THIS
// call's Guidance carries -- naming save's own consequence, not get_review's
// and not download's.
//
// ⚠️ released AND claimed CARRY REAL ARMS HERE, EVEN THOUGH AN EARLIER
// INSTRUCTION TO "MIRROR DOWNLOAD" SAID THE DEFAULT ARM WOULD DO. That
// instruction was a locally-true claim generalized past the case it was
// proven in. It read: refuseSaveForReadableSource refuses released and
// claimed at Save's door, before either field is built, SO THEREFORE (the
// leap) this function's default arm is as unreachable as Download's own.
// Download's really is: downloadSourceFaultGuidance's own doc comment
// (immediately below) records that Download measures the fault FIRST, inside
// t.resolve, and re-asks its door LAST, right before it would write anything
// -- so a state that flips in the gap fails CLOSED, at the fresher check.
// Save's ordering is the mirror image (see Save's own call site for why):
// the door runs FIRST and applyMCPSourceFault runs LAST, after s.Register
// and reviewResult both return. Nothing re-validates the door's answer in
// that gap, so a CONCURRENT repoint of this exact plan, or another plan
// taking its path -- a human in the TUI, or another agent, racing this call
// -- can make the later read observe released or claimed, a state the door
// was supposed to have excluded by then. A scratch reproduction drove
// exactly this: Save's plan_id door passes on a gone file, s.Register lands,
// and in the window before applyMCPSourceFault runs a repoint followed by a
// second plan's create re-installs the same path under a different plan --
// producing source_fault: "claimed" beside a default arm's guidance: "",
// exactly the contradiction a basic rule of this package exists to forbid:
// the enum is the machine-readable signal and must never be suppressed
// because prose was unavailable. See GetReviewResult.SourceFault's own field
// comment for the hedged, accurate version of this population claim, and
// reviewResult's own doc comment for this package's standing policy on a
// race window it cannot design away by restructuring: recorded, not closed.
// TestSaveReportsReleasedAndClaimedAcrossTheDoorMeasurementRace pins both new
// arms against exactly this race, driven deterministically rather than by
// timing.
//
// NEITHER sourceFaultGuidance NOR downloadSourceFaultGuidance IS REUSED, and
// each is excluded for its own reason. sourceFaultGuidance's second half is
// sourceFaultContentNote, whose rule is that every response naming a state
// must also say whether bytes were withheld -- and save always calls
// reviewResult with includeContent false, so contentIncluded is always false and that note
// would render "content above is empty for this call: this plan's document
// cannot currently be read as its own file, and there is no other copy on
// hand to serve instead", FALSE ON BOTH HALVES for save: content is absent
// because the caller supplied the bytes one argument ago, not because
// draftplane could not read anything. This is exactly the reason
// downloadSourceFaultGuidance already exists rather than reusing
// sourceFaultGuidance -- see its own doc comment for the argument stated
// once. downloadSourceFaultGuidance itself is not reused either: its sentence
// describes A FILE THIS CALL JUST WROTE TO DISK, which save never does --
// save's own consequence is a VERSION THIS CALL JUST REGISTERED, into a plan
// that goes on naming the dead path afterward exactly as before
// (refuseSaveForReadableSource's own doc comment explains why). No sentence
// here names save as the fix, for the identical reason that doc comment
// already forbids it of sourceFaultGuidance: this call is not one.
//
// THE TAIL IS DUPLICATED ACROSS THE gone AND unreadable ARMS DELIBERATELY,
// mirroring downloadSourceFaultGuidance's own shape for the identical reason
// stated there: two full sentences are easier to keep correct under a later
// edit than a shared suffix stitched onto two headlines with different
// shapes. released AND claimed DO NOT CARRY THAT TAIL, on purpose: both are
// race-only paths (see the correction above), the file they name genuinely IS
// readable and belongs to something else, and "if they point it at a new
// file, that file becomes the plan's document" is gone/unreadable's own
// remedy for a DEAD path -- naming it here would coach a human to fix a file
// that was never broken. Their own headlines are transcribed verbatim from
// sourceFaultGuidance's wording for the identical states, with save's own
// consequence substituted for get_review's "content above" tail, which this
// call has none of.
//
// ⚠️ NEITHER released NOR claimed'S ARM SAYS "the plan still names that
// file", AND A FUTURE EDITOR MUST NOT ADD IT BACK FOR SYMMETRY
// WITH gone/unreadable -- that symmetry is exactly the trap to avoid. The
// clause is true of gone/unreadable (nothing blanks SourceHint on a save, so
// the plan goes on naming the dead path) and FALSE, BY CONSTRUCTION, of at least
// one of these two: SourceFileReleased's own definition
// (sourceFileFault, session/session.go) is `sess == nil || !sess.Exists` --
// the path resolves to NO plan at all. If THIS plan still named that path,
// ResolvePlan would find it and Exists would be true, which is the opposite
// of what makes this state released in the first place; the clause is not
// merely usually false here, it cannot be true and reach this arm. claimed's
// own definition is `sess.Plan.ID != id` -- the path resolves to a
// DIFFERENT plan -- and the clause is only SOMETIMES true there (two plans
// can share a SourceHint, and which one a lookup finds first is a store
// detail, not a guarantee about which one still names the path), so
// asserting it unconditionally is wrong there too, just for a different
// reason than released's. Neither arm asserts anything about what this
// plan's own SourceHint currently says, because by the time either state is
// even possible, the answer is no longer knowable from inside this function
// without a second store read this call has no reason to make.
//
// readReason, NOT f.Err DIRECTLY, IS THE UNREADABLE ARM'S SECOND %s -- the
// same correction sourceFaultGuidance's and downloadSourceFaultGuidance's own
// unreadable arms already carry: f.Err is a raw *fs.PathError whose own
// Error() already renders "<op> <path>: <reason>", so naming both beside each
// other would print the path twice.
func saveSourceFaultGuidance(f *session.SourceFileError) string {
	if f == nil {
		return ""
	}
	switch f.State {
	case session.SourceFileGone:
		return fmt.Sprintf("this plan's source file (%s) is gone or has moved -- your content was registered "+
			"as a new version, but the plan still names that path and this save did not change that; a human "+
			"needs to open the plan in the TUI for options to fix it, and if they point it at a new file, that "+
			"file becomes the plan's document and this version stays in its history", f.Path)
	case session.SourceFileUnreadable:
		return fmt.Sprintf("draftplane can't read this plan's source file (%s: %s) -- your content was "+
			"registered as a new version, but the plan still names that file and this save did not change "+
			"that; a human needs to make the file readable or open the plan in the TUI for options to fix it, "+
			"and if they point it at a new file, that file becomes the plan's document and this version stays "+
			"in its history", f.Path, readReason(f.Err))
	case session.SourceFileReleased:
		// RACE-ONLY (see this function's own doc comment): Save's door
		// (refuseSaveForReadableSource) refuses a released file in the
		// ordinary case, before either result field is ever built. This arm
		// exists for the window a concurrent repoint can still open between
		// that door and applyMCPSourceFault's own measurement, so an enum
		// reaching the result this way is never left with an empty sentence
		// beside it.
		return fmt.Sprintf("this file (%s) no longer resolves to this plan -- your content was registered as "+
			"a new version of this plan regardless; a human needs to open the plan in the TUI for options to "+
			"fix it", f.Path)
	case session.SourceFileClaimed:
		// RACE-ONLY, for the identical reason the released arm above is.
		return fmt.Sprintf("another plan is already following this file (%s) -- your content was registered "+
			"as a new version of THIS plan, not that one; a human needs to open the plan in the TUI for "+
			"options to fix it", f.Path)
	default:
		// A genuine fifth session.SourceFileState, not one of today's four --
		// see downloadSourceFaultGuidance's own default arm for the identical
		// reason kept rather than omitted, and this function's own doc
		// comment for why, unlike Download's, that default is no longer the
		// ALSO-unreachable rationale it once shared with Download's: this one
		// is reserved for a state that does not exist yet, not for two that
		// do but were once (wrongly) assumed unreachable.
		return ""
	}
}

// Save creates or updates a plan from agent-supplied content: the entry
// point for sources draftplane cannot read itself. It registers the content as
// the plan's newest version — even if that exact content was registered
// before (a revert) — and returns the same projection get_review does, with
// every thread re-anchored onto what was just supplied.
func (t *Tools) Save(ctx context.Context, args SaveArgs) (GetReviewResult, error) {
	ctx, id := t.attribute(ctx, args.AgentID)
	if args.Source != "" && args.PlanID != "" {
		return GetReviewResult{}, fmt.Errorf("pass at most one of source or plan_id — omit both for a sourceless plan")
	}
	content, err := resolveSuppliedContent(args.Content, args.ContentFrom)
	if err != nil {
		return GetReviewResult{}, err
	}

	var s *session.Session
	switch {
	case args.PlanID != "":
		plan, err := t.planByID(ctx, args.PlanID)
		if err != nil {
			// The id door's own coaching, so this site cannot drift out of step
			// with get_review's plan_id path; %w keeps the cause recoverable.
			return GetReviewResult{}, coachOpenVersionErr(err)
		}
		// OpenSuppliedForPlan, not OpenSupplied(content, plan.SourceHint):
		// a sourceless plan's SourceHint is "", which OpenSupplied could
		// never resolve back to THIS plan (empty Path is never an identity
		// — see resolveIdentity), and re-resolving a sourced plan we
		// already looked up by id would be redundant. The capability question
		// is asked once below, of the session's own s.Plan.SourceHint.
		s, err = session.OpenSuppliedForPlan(ctx, t.svc, content, plan)
		if err != nil {
			return GetReviewResult{}, err
		}
	case args.Source != "":
		// THE ARGUMENT CHECK, and it is the one this predicate is named for:
		// the caller just handed draftplane a path, and a readable file no plan
		// exists for yet is save's CREATE path -- there is no resolved plan to
		// ask (s.Exists false) and the argument is precisely the prospective
		// plan's SourceHint, so dropping it would let an agent save content over
		// a document draftplane can read for itself, the whole point of the
		// capability rule.
		//
		// The resolved-plan check below cannot be moved up here to replace it
		// for exactly that reason, and this one cannot be widened to cover that
		// one either: a plan's own SourceHint is only knowable once identity is
		// in hand. OpenSupplied writes nothing, so refusing after it costs one
		// resolve and no state.
		if err := refuseSaveForReadableSource(args.Source); err != nil {
			return GetReviewResult{}, err
		}
		var err error
		s, err = session.OpenSupplied(ctx, t.svc, content, canonicalSource(args.Source))
		if err != nil {
			return GetReviewResult{}, err
		}
	default:
		var err error
		s, err = session.OpenSupplied(ctx, t.svc, content, "")
		if err != nil {
			return GetReviewResult{}, err
		}
	}

	// THE CAPABILITY RULE, ASKED ONCE, OF THE PLAN THE CALL ACTUALLY LANDED ON.
	// What it catches is a plan whose SourceHint is a readable file -- the plan
	// a comment or approve on a local path creates. save is not how that
	// document is edited: get_review re-reads the file live, so bytes
	// registered here are overwritten by the next read and no door would ever
	// serve them. This is the no-plan_id-backdoor half of
	// TestSaveRefusesReadableFile.
	if s.Exists {
		if err := refuseSaveForReadableSource(s.Plan.SourceHint); err != nil {
			return GetReviewResult{}, err
		}
	}

	if err := ensurePlan(ctx, s); err != nil {
		return GetReviewResult{}, err
	}
	if err := s.Register(ctx); err != nil {
		if wrapped, ok := conflictCoaching(err); ok {
			return GetReviewResult{}, wrapped
		}
		return GetReviewResult{}, err
	}
	// The caller supplied these bytes one argument ago — no readback.
	res, err := reviewResult(ctx, s, false, t.pseudonym)
	if err != nil {
		return GetReviewResult{}, err
	}

	// save does not call t.resolve, so it applies applyMCPSourceFault itself,
	// to a session OpenSupplied* built. Both set fromSnapshot at construction.
	// An os.Stat-shaped fast path would be wrong here for the reason
	// applyMCPSourceFault's own doc gives.
	//
	// MEASURED AFTER reviewResult: reviewResult carries its own SourceFault
	// block, gated on contentIncluded, and save's contentIncluded is always
	// false -- were s.SourceFault already non-nil when reviewResult ran, that
	// block would fire sourceFaultGuidance's content note false on both halves
	// for save (see saveSourceFaultGuidance's own doc comment for the sentence
	// that replaces it). Both helpers answer "" for a nil fault, so the two
	// fields are set unconditionally.
	applyMCPSourceFault(ctx, t.svc, s)
	res.SourceFault = sourceFaultState(s)
	res.Guidance = appendGuidance(res.Guidance, saveSourceFaultGuidance(s.SourceFault))

	res.AgentID = id
	return res, nil
}

// ---- download ----
//
// download writes a plan's current content to a path the agent names and
// RECORDS NOTHING. It is a read that happens to produce a file, and it leaves
// the plan exactly as it found it: afterwards nothing in draftplane's store has
// changed (TestDownloadPersistsNothing). The verb opens no store for
// writing and has no commit point, which is what keeps that structural rather
// than ruled.
//
// IT HANDS BACK THE PATH AND THE HASH, and the hash names the exact version
// whose bytes were written.

// downloadReadsLiveMsg refuses a plan whose content draftplane does not hold:
// there is nothing to hand over, because the bytes an agent is asking for are in
// a file on this machine that draftplane reads live on every call. It names the
// plan's own path (s.Path), which session.Open sets from the path it read, so a
// session with FromSnapshot false always has one.
//
// NO SECOND REMEDY, and the omission is the rule this file's refusals follow: a
// refusal may not name a door its reader cannot reach. There is no verb here
// that turns a live-followed file into held content -- save refuses a readable
// source by the capability rule -- so what is left is the agent's own file
// tools, which is also the honest answer.
const downloadReadsLiveMsg = "draftplane reads %s on this machine live, and that file is the plan's content: " +
	"read it with your own tools — download hands over content draftplane holds, and for this plan it holds none"

// downloadFileBackedMsg refuses a FILE-BACKED plan reached by plan_id -- the one
// draftplane CAN serve and deliberately will not (see Download's own
// doc for the reasoning). download is for ephemeral
// scratch-based editing of a plan draftplane holds the only copy of; a plan whose
// SourceHint names a readable document on this machine already has its content in
// a file, and there is nothing for this verb to add to that.
//
// IT NAMES THE PLAN'S OWN PATH, downloadReadsLiveMsg's own reason at the same
// fork: the caller reached this by plan_id and has no path in its hand at all,
// so the refusal has to supply the one to go and read.
//
// IT SAYS WHAT THE FILE WOULD HAVE BEEN, because the harm is not that the write
// is redundant -- it is that the copy would read as authoritative while being
// the last version REGISTERED here, which a human editing the document has very
// likely moved past. An agent told only "you already have it" reasons that a
// download is harmless; one told the copy can be stale does not.
//
// NO SECOND REMEDY, this file's standing rule: there is no verb that turns a
// file-backed plan's document into content draftplane will hand over -- save
// refuses a readable source by the capability rule -- so the agent's own file
// tools are the whole of the honest answer.
const downloadFileBackedMsg = "draftplane reads this plan from %s on this machine, and that file already holds its " +
	"content: read it with your own tools — download would write the last version registered here, which is not " +
	"what that file says once a human has edited it"

// downloadPathInTheWayMsg is ONE sentence for every way the kernel can answer
// "there is already something here": O_CREATE|O_EXCL answers EEXIST for a
// regular file, for a DIRECTORY and for a symlink pointing at nothing alike, so
// the sentence names all three rather than classifying which one it met. That
// costs nothing, tells the reader exactly what to go and look at, and cannot go
// stale the way a classifier that learns about a fourth shape can; a second
// os.Lstat after the kernel has already answered would buy a distinction this
// sentence has no use for.
const downloadPathInTheWayMsg = "%s: something is already at that path — a file, a directory, or a symlink " +
	"pointing at nothing — and download never writes over what is there: name a path with nothing at it"

// downloadNoParentMsg refuses a path whose parent directory does not exist.
// download does not MkdirAll: the path is the agent's, and
// silently manufacturing directories for a typo is how a document ends up
// somewhere nobody meant -- and nothing records that draftplane made either the
// file or the directories above it.
const downloadNoParentMsg = "%s: the directory that would hold it does not exist, and download does not create " +
	"directories: make it first, or name a path under one that is already there"

type DownloadArgs struct {
	Source  string `json:"source,omitempty" jsonschema:"file path or URL identifying the plan whose content to write out; mutually exclusive with plan_id"`
	PlanID  string `json:"plan_id,omitempty" jsonschema:"plan id from list_plans, get_review, or save; mutually exclusive with source"`
	Path    string `json:"path" jsonschema:"path to the file to create, holding the plan's current content -- must not already exist, and its parent directory must; download never overwrites and never creates a directory"`
	AgentID string `json:"agent_id,omitempty" jsonschema:"identity from an earlier draftplane response this conversation; omit on your very first draftplane call to be issued a fresh one"`
}

// DownloadResult is deliberately the SMALLEST result on this surface: where the
// bytes went, and which version they are.
//
// IT CARRIES NO plan_id: this call changes nothing about the plan, so there is
// no new state to report -- an agent that addressed by source still addresses
// by source afterwards, and one that addressed by plan_id already holds the id.
//
// Hash is the same string get_review's own Hash field carries for the same
// session, so an agent can tell which version the file holds without a
// second read.
//
// ⚠️ Path'S SCHEMA SAYS IT IS NOT AN ADDRESS, and that clause is carrying a
// KNOWN DEFECT rather than describing a design. An answered path reads as an
// address, so an agent's natural next call is comment(source: <this path>) --
// and TestADownloadedPathAddressedAsSourceForksASecondPlan proves what that
// does: t.resolve finds a readable local file nothing claims, ensurePlan mints
// a SECOND plan for it, and the thread lands there while the real plan answers
// with none. Nothing refuses it today. The words are mitigation, not a guard,
// and they are deliberately DIRECT -- "not an address, keep using plan_id" --
// because the softer true sentence already in download's description ("the
// plan is still addressed exactly as it was before") was there when the fork
// was found and reads as permission to use either address.
//
// ⚠️ SourceFault AND Guidance WERE ADDED AFTER THE FIVE OTHER DOORS ALREADY
// HAD SourceFault (sourceFaultState's own doc comment named this verb as
// deliberately out of scope for that pass), and download needed BOTH fields
// where the other six needed only the one. Every other door this signal
// reaches returns data an agent reads once and discards; this one WRITES A
// FILE THAT OUTLIVES THE CALL, and a human may open it tomorrow with nothing
// about it marked stale. A bare machine-readable field can die silently in an
// agent that does not branch on a field it has not seen before -- Guidance is
// the sentence that survives that, in words meant to be relayed rather than
// parsed. See SourceFault's own field comment for why only two of the four
// states can ever set either field, which is narrower than every other door
// carrying this signal.
type DownloadResult struct {
	Path string `json:"path" jsonschema:"the absolute path just written; draftplane records nothing about it and this file is yours. It is NOT an address for the plan -- keep addressing that by plan_id: hand this path to another verb as source and draftplane creates a second plan for it, and the review facts you meant for the plan land on that copy instead"`
	Hash string `json:"hash" jsonschema:"the content hash of exactly the bytes written"`
	// SourceFault and Guidance are the four-state signal, one door later than
	// the other five: see this type's own doc comment for why download needed both
	// fields rather than the one. THE POPULATION IS NARROWER THAN EVERY OTHER
	// DOOR CARRYING THIS FIELD -- released and claimed never reach here, because
	// the gate above (refuseSaveForReadableSource, of the plan's SourceHint)
	// refuses this call outright for exactly those two, before either field is
	// ever constructed: in both, the file IS readable and simply belongs to
	// something else, which is download's ordinary refusal already, not a new
	// one. gone and unreadable are what falls through to the write below, and
	// they are the only two values this field, or Guidance beside it, will
	// ever carry.
	SourceFault string `json:"source_fault,omitempty" jsonschema:"the same four-state vocabulary get_review's own source_fault field carries (see its own description for the full account), but only two of the four ever reach this field: gone (the file is not there -- deleted or moved) or unreadable (it is there but could not be read). released and claimed refuse this call outright before anything is written, because in both the file IS readable and simply belongs to something else -- download's ordinary refusal already covers those two. When this field IS set, the file just WRITTEN TO DISK at the path above is this plan's LAST REGISTERED version, not a copy of what that file currently says or would say if it could be read. Absent entirely when there is nothing to report, never a zero value that could be misread as a state."`
	// Guidance is the human half of the fact SourceFault reports, present only
	// alongside a non-empty SourceFault -- the sentence to relay verbatim to
	// whoever opens the file this call just wrote, because nothing else about
	// that file marks it stale.
	Guidance string `json:"guidance,omitempty"`
	AgentID  string `json:"agent_id" jsonschema:"this call's identity; carry it forward as agent_id on every later draftplane call this conversation"`
}

// downloadSourceFaultGuidance renders the two states Download can ever reach
// (see DownloadResult.SourceFault's own field comment for why released and
// claimed cannot) into the sentence this call's Guidance carries -- naming
// THIS verb's own consequence, not get_review's. sourceFaultGuidance above is
// deliberately not reused for it: that function's second half is get_review's
// own content-gate note ("content above is..."), naming a field this result
// does not have, and its first half tells a human to open the TUI, which is
// not what an agent holding a freshly written file needs told. What this call
// needs said is narrower and points at the one artifact this call made: the
// file just left on disk outlives the call, and nothing else about it marks
// that, unless this sentence does.
//
// ⚠️ ITS UNREADABLE ARM GOES THROUGH readReason FOR THE SAME REASON
// sourceFaultGuidance's does: f.Path and f.Err named side by side in one
// parenthetical duplicate the path, because Err is a raw *fs.PathError whose
// own Error() already renders "<op> <path>: <reason>". `grep -n '(%s: %v)'`
// over this file finds none; that grep, not a re-reading of every message by
// eye, is how another is caught if one is ever added.
func downloadSourceFaultGuidance(f *session.SourceFileError) string {
	if f == nil {
		return ""
	}
	switch f.State {
	case session.SourceFileGone:
		return fmt.Sprintf("this plan's source file (%s) is gone or has moved -- the file just written to disk "+
			"is this plan's LAST REGISTERED version, not a copy of that document. Tell your human it may be "+
			"stale before they treat it as current.", f.Path)
	case session.SourceFileUnreadable:
		return fmt.Sprintf("draftplane can't read this plan's source file (%s: %s) right now -- the file just "+
			"written to disk is this plan's LAST REGISTERED version, not a copy of that document. Tell your "+
			"human it may be stale before they treat it as current.", f.Path, readReason(f.Err))
	default:
		// released and claimed refuse before Download ever reaches its success
		// return (see the gate below), so this arm is unreachable through that
		// door in practice. Kept rather than omitted: a caller that reaches this
		// function directly (a unit test, or some later caller it acquires) gets
		// silence instead of a sentence written for a fact that is not true of
		// the state it was actually handed.
		return ""
	}
}

// Download writes the plan's current content to args.Path and answers with that
// path and the content's hash. It touches no store.
//
// THE GATE IS TWO QUESTIONS, ASKED IN THIS ORDER: does draftplane hold these
// bytes at all (!FromSnapshot), and is this plan's own document a file on this
// machine (refuseSaveForReadableSource, of the plan's SourceHint).
//
// THE FIRST asks whether these bytes came from draftplane's store rather than
// from a file.
//
// BOTH QUESTIONS ARE reviewResult's TOO, and that is the invariant this gate
// exists to keep: what get_review inlines into GetReviewResult.Content is
// exactly what this verb will write, and what this verb refuses is exactly what
// that line omits. See reviewResult's own doc, which owns the invariant's
// statement.
//
// THE SECOND is save's, reused rather than re-derived --
// refuseSaveForReadableSource is the ONE predicate for "can draftplane read the
// document this call would land on", and asking it here deletes an asymmetry
// rather than adding a rule: save and download now ask the same question of the
// same field. Only its VERDICT is borrowed; the sentence is download's own
// (downloadFileBackedMsg), because saveCapabilityMsg's remedies -- omit source,
// hand the path to content_from -- are doors this caller cannot reach.
//
// ⚠️ THE SECOND QUESTION MUST BE ASKED OF THE SourceHint, NOT ANSWERED FROM
// FromSnapshot ALONE: a plan whose SourceHint names a perfectly readable file
// on this machine, addressed by its id, arrives here with FromSnapshot true
// exactly as a sourceless plan does -- Open leaves fromSnapshot false;
// OpenVersion, OpenSupplied and OpenSuppliedForPlan set it true; OpenByID
// answers through OpenVersion. FromSnapshot alone cannot tell the two apart. A
// reader must not "fix" this gate into asking FromSnapshot alone -- doing so
// would make download SERVE plans get_review does not inline content for,
// breaking the one property this gate exists to keep.
//
// THE ASYMMETRY WITH get_review: get_review INLINES content into a response: it
// is text in a conversation, plainly a reading of a version, and nobody can
// mistake it for the document. download WRITES A FILE, and a file reads as
// authoritative and invites editing -- that is what the verb is for. For a
// file-backed plan the two are not the same act at all: the copy is the last
// version REGISTERED here, the human is editing the document, and an agent that
// edits the copy is working against bytes that have already moved. That
// asymmetry is why the file is worth refusing here.
//
// ⚠️ get_review's OWN GATE STAYS IN STEP WITH THIS ONE, AND THAT IS NOT FREE:
// reviewResult asks these same two questions of the same two fields, so what
// get_review inlines is always exactly what this verb would write. What
// differs is only the SHAPE of each door's answer, which is why they are
// still not one code path: download owes a refusal that names the file
// (downloadFileBackedMsg), because a caller who asked for a file and got none
// needs to be told where to go, while get_review simply omits the field and
// hands back PlanInfo.SourceHint, because the rest of its projection --
// threads, placements, approval -- is what it was called for and still
// answers.
//
// download IS FOR EPHEMERAL SCRATCH-BASED PLAN EDITING, stated positively, so
// the extension is derivable rather than remembered: it serves a plan whose
// only copy draftplane holds -- sourceless, or a URL source (notion://,
// https://) and therefore unreadable here. Those two shapes are what is left
// once both questions have been asked.
//
// s.Exists NEEDS NO CHECK OF ITS OWN, and it was checked rather than assumed:
// every arm of t.resolve that can produce a plan-less session goes through
// session.Open (the readable-local-file branch), which leaves FromSnapshot
// false, so the gate below has already refused it. The other arms either refuse
// outright (coachResolvePlanErr, coachOpenVersionErr) or answer a session with
// Exists true. FromSnapshot true therefore implies a plan.
//
// ui.VisibleControls IS NOT IN THIS PATH, deliberately. reviewResult writes
// string(s.Content) raw and so does this: what an agent asked for is the plan's
// bytes, and a download that sanitised them would hand back a file that does not
// hash to the value in the same response.
//
// THE SUCCESS RETURN ALSO REPORTS s.SourceFault, ALREADY POPULATED BY t.resolve
// ABOVE -- nothing here computes it. Only gone and unreadable can ever reach
// this point with a non-nil one: the gate two paragraphs up already refused
// released and claimed, for the identical reason (the file is readable, and
// belongs to something else), before either of these fields is ever built. See
// DownloadResult's own doc comment for why a bare field was not enough here.
func (t *Tools) Download(ctx context.Context, args DownloadArgs) (DownloadResult, error) {
	ctx, id := t.attribute(ctx, args.AgentID)
	s, err := t.resolve(ctx, args.Source, args.PlanID)
	if err != nil {
		return DownloadResult{}, err
	}
	if !s.FromSnapshot() {
		return DownloadResult{}, fmt.Errorf(downloadReadsLiveMsg, s.Path)
	}
	// The capability rule's own question, asked of the plan this call landed
	// on: only the VERDICT is shared, because save's sentence coaches doors
	// this caller has not got. s.Plan.SourceHint rather than s.Path only to
	// name the field the question is about; the two are one value for every
	// session that gets past the gate above, since t.resolve reaches this line
	// only through session.OpenVersion, which sets Path from plan.SourceHint.
	if refuseSaveForReadableSource(s.Plan.SourceHint) != nil {
		return DownloadResult{}, fmt.Errorf(downloadFileBackedMsg, s.Plan.SourceHint)
	}

	// TRIM, THEN CHECK EMPTY, THEN RESOLVE: filepath.Abs("") answers the
	// WORKING DIRECTORY, so an empty or all-whitespace path that reached
	// resolvedPath would come back as the directory this process happens to be
	// sitting in, and the kernel would then refuse it as "something is already
	// at that path" -- a refusal about somewhere the caller never named. The
	// refusal is here, worded for this verb's caller, because resolvedPath
	// answers a path and never an error.
	if strings.TrimSpace(args.Path) == "" {
		return DownloadResult{}, errors.New("download needs a path: pass the file to write the content to")
	}
	// ONE call, and its answer is what both the write and the report use --
	// resolvedPath's own doc comment says why that matters.
	abs := resolvedPath(args.Path)

	// O_CREATE|O_EXCL, never os.Stat followed by a write: the
	// kernel makes "does it exist" and "create it" one indivisible decision;
	// two calls make them two decisions with a window between, and losing that
	// race is precisely clobbering a file somebody else put there. There is
	// no earlier, cheaper existence check either: a second check would be a
	// second answer, and the one that would then be tempting to trust is not
	// the one guarding the write.
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	switch {
	case errors.Is(err, fs.ErrExist):
		return DownloadResult{}, fmt.Errorf(downloadPathInTheWayMsg, abs)
	case errors.Is(err, fs.ErrNotExist):
		// A missing parent directory. No MkdirAll -- see downloadNoParentMsg.
		return DownloadResult{}, fmt.Errorf(downloadNoParentMsg, abs)
	case err != nil:
		// Everything else the kernel can refuse a create with: a parent
		// component that is not a directory, a permission denial, a read-only
		// filesystem. Wrapped rather than reworded, because draftplane has
		// nothing to add to what the operating system just said.
		return DownloadResult{}, fmt.Errorf("download could not create %s: %w", abs, err)
	}
	if _, err := f.Write(s.Content); err != nil {
		_ = f.Close()
		// The partial file is left exactly where it is. Removing it would be
		// draftplane deleting a file, which the never-delete rule forbids with no
		// exception for "but draftplane made it" -- and nothing here records
		// that draftplane made it anyway, which is that rule's mechanism
		// rather than a gap in it.
		return DownloadResult{}, fmt.Errorf("download wrote part of %s and then stopped: %w — what is there now is "+
			"left exactly as it is, because draftplane never deletes a file, including one it just made: look at it, "+
			"and download to a path with nothing at it", abs, err)
	}
	if err := f.Close(); err != nil {
		return DownloadResult{}, fmt.Errorf("download could not finish writing %s: %w", abs, err)
	}
	return DownloadResult{
		Path:        abs,
		Hash:        string(s.Hash),
		SourceFault: sourceFaultState(s),
		Guidance:    downloadSourceFaultGuidance(s.SourceFault),
		AgentID:     id,
	}, nil
}

// resolvedPath turns download's caller-supplied path into the absolute form
// this package reports as DownloadResult's own Path field.
//
// Download calls this ONE time and hands the SAME string to os.OpenFile and to
// its result, so the path it reports and the path it opened cannot disagree --
// including on the fallback, where both are the trimmed argument. (Abs fails
// only when os.Getwd does; a relative path then stays relative and is opened
// relative to the same working directory the report is read against.)
//
// TrimSpace FIRST: without it, a leading space makes a path non-absolute, so
// Abs joins it onto this process's cwd instead of refusing, and download would
// open and report a DIFFERENT path than the caller named.
func resolvedPath(path string) string {
	path = strings.TrimSpace(path)
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

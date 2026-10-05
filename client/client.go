// Package client defines the contract between Draftplane's review surfaces (TUI,
// MCP tools) and the store that holds every plan's review facts.
// client/localfs implements PlanService over this machine's state file.
package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/reanchor"
)

var (
	// ErrNoPlan means no plan is registered for a source hint; callers offer
	// to create one.
	ErrNoPlan = errors.New("no plan registered for source")
	// ErrNotFound covers missing plans, threads, and snapshots.
	ErrNotFound = errors.New("not found")
)

// Ago renders how long before now at was -- "2h ago", "3d ago", "moments ago"
// -- and "" when at is zero, so a door appends it unconditionally rather than
// testing the field itself. It lives here rather than at a door so no door
// computes it twice; the plan list's OPENED column is its
// reader.
//
// now is a parameter, not time.Now(), so this is a pure function of two
// timestamps and every test pins the clock by passing one.
//
// A negative interval is "moments ago", not "-1m ago": a clock stepped back
// after at was recorded would otherwise render something that has not happened
// yet.
//
// The coarsest non-zero unit, floored, matching how a human says it: sub-
// minute is "moments ago" rather than a second count nobody reads, and
// something three days and twenty hours old is "3d ago" rather than "92h ago".
func Ago(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	d := now.Sub(at)
	switch {
	case d < time.Minute:
		return "moments ago"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
}

// URLSource reports whether source names a URL rather than a file on this
// machine: any URL scheme other than file:// (https://, notion://,
// slack://, ...) counts as a URL; a bare local path or an explicit file://
// URL does not.
//
// This is the single, shared home for that rule. Do not write another copy;
// import this one.
func URLSource(source string) bool {
	raw := strings.TrimPrefix(source, "file://")
	return strings.Contains(raw, "://")
}

// SourceResolver is one method of PlanService -- ResolvePlan -- named on its
// own so PathClaimant below can be asked of a store that is not a whole
// PlanService. Every PlanService satisfies it by construction.
type SourceResolver interface {
	ResolvePlan(ctx context.Context, sourceHint string) (domain.Plan, error)
}

// PathClaimant answers the plan that already follows sourceHint when that plan
// is not self, and it is the one statement of "is this path somebody else's"
// in the product. The missing-file panel's re-point (app) asks it before
// it writes a plan's SourceHint.
//
// SELF-MATCH IS NOT A CONFLICT, and that is the load-bearing half: a plan
// re-pointed at a path it already claims has changed nothing.
//
// It returns the plan and not a bool because the caller has something to say:
// a refusal that names the other plan tells a human they have two plans
// following one file, where "that path is taken" describes a mechanic and
// leaves them nowhere.
//
// A MISS IS ErrNoPlan AND IS NOT AN ERROR HERE: no plan follows the path, so
// there is no claimant and nothing went wrong. Every other failure is
// returned, and callers must refuse on it rather than treat it as "free": a
// door that cannot find out whether a path is taken must not write to it. An
// empty sourceHint needs no special case -- ResolvePlan never matches one, so
// it answers ErrNoPlan and this answers "unclaimed".
func PathClaimant(ctx context.Context, svc SourceResolver, sourceHint string, self domain.PlanID) (domain.Plan, bool, error) {
	other, err := svc.ResolvePlan(ctx, sourceHint)
	switch {
	case errors.Is(err, ErrNoPlan):
		return domain.Plan{}, false, nil
	case err != nil:
		return domain.Plan{}, false, err
	case other.ID == self:
		return domain.Plan{}, false, nil
	}
	return other, true, nil
}

// SourceHintSetter is the re-point capability: setting the local path a plan
// follows, or -- for the empty hint -- releasing it from the one it has. It is
// an optional interface rather than a method on PlanService because it writes
// domain.Plan.SourceHint, a field no review fact carries, so a test double
// built for review facts need not implement it.
//
// client/localfs.Store implements the write. Ask through SetSourceHint below,
// never by asserting directly.
type SourceHintSetter interface {
	SetSourceHint(ctx context.Context, id domain.PlanID, hint string) error
}

// SetSourceHint points a plan at hint, or releases it when hint is empty,
// asked of whatever PlanService a door happens to hold.
//
// Every caller uses this rather than asserting for itself, and a MISSING
// CAPABILITY REFUSES HERE WITH NO FALLBACK. A read can degrade to a worse
// answer; a write cannot -- a door that treated "this store cannot re-point"
// as success would tell a human their plan now follows a file it does not, and
// the next open would raise the same missing-file panel over the same path.
func SetSourceHint(ctx context.Context, svc PlanService, id domain.PlanID, hint string) error {
	setter, ok := svc.(SourceHintSetter)
	if !ok {
		return fmt.Errorf("this service cannot re-point plan %s", id)
	}
	return setter.SetSourceHint(ctx, id, hint)
}

// VersionRegistration is a request to make the hash of Content the plan's
// newest version. Content carries the bytes, never a caller-computed hash: the
// store derives the hash itself, so a caller-supplied hash and the bytes
// beside it can never disagree.
//
// Base is the tip the caller believed was current when it started editing -- a
// PRECONDITION, not an ancestry assertion. The store assigns the edge from the
// tip it actually holds, so a writer that states a stale base cannot fork
// history; it can only fail the precondition and be told to fetch. Base stays
// a bare ContentHash and deliberately not a domain.VersionRef: a revert can
// re-register a hash that already occupies an earlier position in the log, so
// a hash is the only thing that identifies a point in the version DAG. Do not
// "improve" this into a VersionRef.
//
// Named fields rather than positional parameters: []byte beside ContentHash
// makes transposing them a compile error, where two positional ContentHash
// values would silently invert the precondition.
//
// An empty Base means "I have no idea what the tip is." Against an existing
// plan that conflicts -- unless the content it carries already IS the tip,
// which is a no-op success because there is nothing to register.
type VersionRegistration struct {
	Content []byte
	Base    domain.ContentHash
}

// ConflictError means the caller's precondition is not what the store
// currently holds: the tip moved out from under a version registration. It
// carries the current Tip, so the caller can offer to fetch before writing.
//
// A conflict is a normal outcome in product terms -- one prompt, no alarm --
// but it is an error in the type system deliberately: it travels from
// PlanService through session to the TUI or the MCP surface, and every
// intermediate layer does nothing but return err.
//
// Declining the fetch is a retry with Base set to Tip.Hash. There is no force
// flag: the same call admits it, and it overwrites only the version the human
// was actually shown.
//
// Tip is a domain.VersionRef rather than a bare hash for uniformity with
// domain.Version. No caller reads Tip.Seq -- the decline path sends only
// Tip.Hash as the retry's precondition, and both doors render Tip.Hash alone
// -- but a conflict is about one particular version, so a door rendering the
// hash can say where in the log it stands without widening this type again.
type ConflictError struct {
	Plan domain.PlanID
	Tip  domain.VersionRef
}

// Error is the floor, not the ceiling: a door that must also say what the
// retry will DO composes its own text around it.
func (e *ConflictError) Error() string {
	return fmt.Sprintf("plan %s has moved; it now stands at %s", e.Plan, e.Tip.Hash.Short())
}

// PlanService is the review surfaces' whole view of the store. It is
// deliberately dumb: re-anchoring and all file awareness stay caller-side.
// Implementations stamp actor attribution and timestamps on writes.
//
// A review fact and the bytes it references arrive together, or neither does --
// structurally, not by convention. RegisterVersion and CreatePlan are the only
// two content-bearing writes: each takes the content itself and the
// implementation sequences storing it and creating the review fact that names
// it as one call, rather than trusting a caller to have stored the bytes
// first. Approve and CreateThread reference a hash a version has already
// registered, so "content enters the store only when a review fact references
// it" holds by construction for every write on this interface.
//
// Read semantics: Versions, Threads, and Approvals called with an unknown plan
// ID return empty results without error. Write methods validate existence and
// return ErrNotFound. PlanByID is the one READ that also validates existence,
// because its whole question IS existence and its return type has no honest
// empty value; its own doc comment carries the argument.
type PlanService interface {
	ListPlans(ctx context.Context) ([]domain.Plan, error)
	// ResolvePlan looks a plan up by its exact SourceHint. An empty
	// sourceHint never matches — mirroring CreatePlan's never-equal rule —
	// so it always returns ErrNoPlan rather than an arbitrary sourceless
	// plan.
	ResolvePlan(ctx context.Context, sourceHint string) (domain.Plan, error)
	// PlanByID answers ONE plan by its id -- ResolvePlan's counterpart for the
	// other identity a caller can hold.
	//
	// A MISS IS ErrNotFound, AND THIS IS THE ONE READ THE "empty results
	// without error" RULE ABOVE DOES NOT REACH. Versions, Threads and
	// Approvals answer an unknown id with an empty slice because a plan with
	// no versions, threads or approvals is an ordinary plan. There is no such
	// empty value here: a zero domain.Plan is indistinguishable from a real
	// plan whose fields happen to be zero, so answering one with a nil error
	// would hand every caller "no such plan" wearing the shape of success.
	//
	// ErrNotFound AND NOT ErrNoPlan, though ResolvePlan uses the other one.
	// ErrNoPlan means "no plan is registered for this SOURCE; offer to create
	// one", and a create offer is meaningless against an id -- nothing can
	// create a plan AT an id a caller already holds. A door whose own caller
	// needs the create offer wraps this answer into ErrNoPlan itself, where
	// the source that would be created at is in hand.
	//
	// It does not answer a version log, deliberately: a caller that needs one
	// asks Versions.
	PlanByID(ctx context.Context, id domain.PlanID) (domain.Plan, error)
	// CreatePlan creates a plan keyed by sourceHint, with content as its
	// initial version -- a version registration wearing a different name,
	// so it takes the same treatment RegisterVersion does: bytes, not a
	// caller-supplied hash, with the implementation deriving the hash and
	// putting the content into its own storage as it creates the plan.
	// Unlike RegisterVersion there is no existing tip to compare-and-swap
	// against -- a new plan has none -- so content is a bare []byte rather
	// than a VersionRegistration; carrying an always-unused Base field
	// forward would document a precondition that can never apply here.
	//
	// A non-empty sourceHint that matches an existing plan's SourceHint
	// returns that plan unchanged (return-existing semantics): callers
	// racing on the same source converge on one plan rather than forking
	// review facts, and content is never even hashed on that path. An empty
	// sourceHint is never an identity match — every sourceless CreatePlan
	// call creates a distinct new plan, even against another sourceless one.
	CreatePlan(ctx context.Context, title, sourceHint, repoHint string, content []byte) (domain.Plan, error)
	// RenamePlan sets a plan's title. ErrNotFound for unknown IDs; empty
	// titles are rejected.
	RenamePlan(ctx context.Context, id domain.PlanID, title string) error
	// DeletePlan removes a plan and all its review facts (versions, threads,
	// approvals). Content snapshots in the CAS are NOT touched: they are
	// content-addressed, possibly shared, and deleting review facts must never
	// destroy the reviewed bytes. ErrNotFound for unknown IDs.
	DeletePlan(ctx context.Context, id domain.PlanID) error

	// RegisterVersion makes the hash of r.Content the plan's newest version,
	// under a compare-and-swap on r.Base. Three outcomes, checked in this
	// order, and every one of them -- including the no-op -- answers the
	// tip as a domain.VersionRef:
	//
	//	hash(r.Content) is already the tip -> that hash, unchanged. Nothing
	//	                                      to register, whatever base the
	//	                                      caller believed, but a success
	//	                                      whose tip is knowable all the
	//	                                      same: answering a zero
	//	                                      VersionRef here would make a
	//	                                      caller's advance decision
	//	                                      depend on which flavour of
	//	                                      success it got.
	//	r.Base is not the tip              -> *ConflictError carrying the
	//	                                      tip (a zero VersionRef is
	//	                                      returned alongside the error;
	//	                                      the tip travels on the error).
	//	otherwise                          -> appended, and the new tip is
	//	                                      returned.
	//
	// The order is load-bearing: an idle save from a writer with a stale base
	// succeeds silently rather than prompting a human to fetch bytes they
	// already hold.
	//
	// Re-registering a hash that appears EARLIER in the log -- a revert --
	// appends again and re-promotes that content, so the log stays
	// append-only. Do not add a unique constraint on (plan, hash).
	RegisterVersion(ctx context.Context, id domain.PlanID, r VersionRegistration) (domain.VersionRef, error)
	// Versions returns all registered versions for a plan. Returns empty, nil
	// for an unknown plan ID — callers must not treat empty as an error.
	Versions(ctx context.Context, id domain.PlanID) ([]domain.Version, error)

	// Threads returns all threads for a plan. Returns empty, nil for an unknown
	// plan ID — callers must not treat empty as an error.
	Threads(ctx context.Context, id domain.PlanID) ([]domain.Thread, error)
	CreateThread(ctx context.Context, id domain.PlanID, h domain.ContentHash, a reanchor.Anchor, body string) (domain.Thread, error)

	// Reply, SetResolved and Rehome each name a thread OF A PLAN.
	//
	// Unknown-id behaviour, which all three share: ErrNotFound for an unknown
	// plan id, for an unknown thread id, AND for a thread id that really
	// exists but belongs to some OTHER plan than id. That third case is a
	// refusal, not a fallback: a write must never land on a thread the caller
	// did not address. The three are deliberately indistinguishable -- there
	// is nothing a caller could do differently with the narrower answer.
	//
	// Unlike Versions, Threads and Approvals, an unknown id here is never an
	// empty success: these are writes, and the read semantics stated above do
	// not reach them.
	Reply(ctx context.Context, id domain.PlanID, tid domain.ThreadID, body string) (domain.Comment, error)
	SetResolved(ctx context.Context, id domain.PlanID, tid domain.ThreadID, resolved bool) error

	// Rehome durably re-anchors a thread of id onto a version: orphan
	// adjudication and (later) fuzzy-match confirmation. Same unknown-id
	// contract as Reply and SetResolved above.
	Rehome(ctx context.Context, id domain.PlanID, tid domain.ThreadID, h domain.ContentHash, a reanchor.Anchor) error

	// DeleteThread destroys a thread of id and every comment under it. It is
	// ON the contract rather than beside it as an optional interface: a
	// store that cannot answer this is BROKEN, so there is no same-answer
	// fallback for a failed type assertion to take.
	//
	// Whole threads only, never single comments: a reply that answers
	// nothing is not worth keeping, and that granularity is what makes
	// "nothing survives" below affordable.
	//
	// Same unknown-id contract as Reply, SetResolved and Rehome above, all
	// three cases included and deliberately indistinguishable. The third --
	// a thread that exists but belongs to some other plan -- matters more
	// here than on the other three, since destroying a thread the caller did
	// not address is the worst outcome this method has.
	//
	// A SECOND DELETE OF THE SAME THREAD IS ALSO ErrNotFound, with no sentinel
	// of its own: NOTHING SURVIVES a thread delete -- no placeholder, no
	// "comment deleted" marker -- so a thread destroyed a moment ago and one
	// that never existed are the same absence.
	DeleteThread(ctx context.Context, id domain.PlanID, tid domain.ThreadID) error

	// Approve records that the caller approved h's exact bytes. Idempotent on
	// (plan, hash, actor): a repeat approval of content this exact actor
	// already approved is a no-op success, not an error, and leaves the
	// ORIGINAL fact -- including its agent -- unchanged even when the retry
	// names a different agent. That is what makes a lost-response retry
	// safe, the same reason RegisterVersion's no-op arm exists above.
	//
	// Approve records a fact and answers nothing about "is this plan
	// approved" -- there is no such method on this interface, deliberately.
	// That judgment is a caller's to make from the facts Approvals below
	// returns, never something an implementation computes, stores, or
	// returns on its own.
	Approve(ctx context.Context, id domain.PlanID, h domain.ContentHash) error
	// Approvals returns all approvals for a plan. Returns empty, nil for an
	// unknown plan ID — callers must not treat empty as an error.
	Approvals(ctx context.Context, id domain.PlanID) ([]domain.Approval, error)

	// Content answers the bytes of one version of one plan. Scoped to a plan,
	// never to a bare hash: the content store is shared by every plan this
	// machine holds, so the same bytes can sit under a hash two plans both
	// reference, and a plan answers only for the versions its own log
	// registered.
	//
	// A hash id never registered is ErrNotFound.
	Content(ctx context.Context, id domain.PlanID, h domain.ContentHash) ([]byte, error)
}

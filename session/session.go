// Package session is the headless core of a Draftplane review: one open plan
// file, its identity and version state, and every review action the TUI (or a
// test) can take. Opening a session performs no writes of any kind: every
// write goes through the first write action, which snapshots and registers the
// current version.
package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/placement"
	"github.com/draftplane/draftplane/reanchor"
	"github.com/draftplane/draftplane/ui"
)

// ErrPlanNotCreated is returned by write actions before Create has been called
// on a path with no registered plan: the verbs that ADD to a conversation --
// comment, reply, resolve, rehome, approve, register -- where a missing plan
// means the thing being added to does not exist.
var ErrPlanNotCreated = errors.New("plan not created yet")

type Session struct {
	svc client.PlanService

	Path    string
	Content []byte
	Hash    domain.ContentHash
	Plan    domain.Plan
	Exists  bool

	// SourceFault is why this session is NOT following the file its plan names,
	// when it should be: the classified reason SourceFileFault produced at open,
	// or nil when there is nothing to say.
	//
	// LATCHED AT OPEN, NEVER CLEARED: it records what the file did at the moment
	// this session was built, and a session that has already fallen back to a
	// frozen snapshot does not quietly start following the file again if the user
	// puts it back. Reload opens a NEW session, which is the one place a restored
	// file stops being a fault.
	//
	// IT HAS TWO WRITERS, each on a *Session it alone constructed, exactly once,
	// before that session is handed to a caller: OpenPlan, inside the TUI process,
	// and mcptools.applyMCPSourceFault, inside a separate `draftplane mcp`
	// subprocess, which asks the same question OpenPlan asks (session.Open, then
	// SourceFileFault) because that door must not call OpenPlan itself (see
	// mcptools' openByID for why).
	// Nothing is shared between them but the type, and the type is what keeps the
	// doors reading it from disagreeing about what the states are. Reload is
	// deliberately not a third writer: a reload that cannot read the file produces
	// NO session at all, so that door raises the same *SourceFileError as an error
	// instead.
	//
	// A FACT FOR A DOOR TO SAY, NEVER A STATE FOR ONE TO ACT ON: the session is
	// otherwise entirely ordinary, every gesture on it behaves exactly as it did,
	// and nothing about this field stops anything. What it buys is that the door
	// can name what happened instead of presenting a frozen snapshot with no
	// explanation.
	//
	// BOTH WRITERS LATCH IT FOR EVERY PLAN WHOSE SOURCE IS A PATH, however the
	// plan was created. A snapshot session with this field nil is not a bug --
	// sourceFileFault found nothing to say, or the plan's source was never a file
	// draftplane reads (a URL source, or no source at all).
	SourceFault *SourceFileError

	registered bool

	// base is the tip this session's content was derived from: the plan's newest
	// registered version at the moment the session was opened, or the hash this
	// session itself last registered. Rediscover is the one path where "derived
	// from" overstates it -- a plan that appears underneath a session adopts
	// ANOTHER actor's tip, which this session's content was never derived from.
	// That is sanctioned (leaving it empty would conflict every write after a
	// Rediscover), and it is why base is a precondition this session is entitled
	// to assert rather than a provenance claim.
	//
	// It is REMEMBERED, never re-read at write time. Fetching the tip one step
	// before registering and passing it as the precondition makes the
	// compare-and-swap unfailable: a stale writer supersedes work it never read.
	// Reading to ESTABLISH a base you do not have (Open, Create, Rediscover) is
	// correct; re-reading to REFRESH a base you already hold is the bug.
	base domain.ContentHash

	// fromSnapshot is true when this session was opened via OpenVersion or
	// OpenSupplied (no local working copy — content sourced through
	// svc.Content or supplied directly by the caller) rather than Open (a
	// file on disk). Reload needs to know which: a snapshot session has no
	// path to re-read.
	fromSnapshot bool
}

// Service exposes the session's PlanService for callers that need to
// re-open a fresh session (reload) without threading it separately.
func (s *Session) Service() client.PlanService { return s.svc }

// Base reports the tip this session's content was derived from. Callers
// that register a version on this session's behalf must pass it unchanged.
func (s *Session) Base() domain.ContentHash { return s.base }

// FromSnapshot reports whether this session was opened via OpenVersion or
// OpenSupplied (content sourced through svc.Content or supplied directly, no
// local working copy) rather than Open (a file on disk).
func (s *Session) FromSnapshot() bool { return s.fromSnapshot }

// InferredTitle is the title a plan created for this session gets when nobody
// was asked for one: the document's first H1, else the file's stem, else
// "Untitled" for a sourceless session (ui.InferTitle's own three arms). Every
// lazy create in the product uses this rule, because a plan whose title
// depends on WHICH gesture happened to create it is a plan that renames
// itself.
//
// It is a method rather than a free function over (content, path) so that the
// pair can never be supplied by halves: a caller with the session in hand
// cannot title a plan from one session's bytes and another's path.
//
// THIS IS WHY session IMPORTS ui, and the cost was weighed rather than missed.
// ui.InferTitle and ui.ParseBlocks are PURE over their arguments and render
// nothing, and no binary gains a dependency: every program that links session
// already links ui. The alternative is a second implementation of "what is a
// lazily created plan called". The one rule this creates is that ui may never
// import session.
func (s *Session) InferredTitle() string {
	return ui.InferTitle(ui.ParseBlocks(s.Content, nil), s.Path)
}

// ErrNoSourcePath is Open's refusal for an empty path: not a file that is
// missing, but a plan that never had one. A SOURCELESS plan -- an agent hands
// draftplane the bytes over MCP with neither source nor plan_id
// (mcptools.Save's default arm), draftplane mints the id, and draftplane
// holds the only copy there has ever been -- carries SourceHint "", so every
// door that opens a plan by its hint reaches this call with nothing to open.
//
// os.ReadFile("") answers ENOENT, which leaves the two facts
// indistinguishable to callers; the one door that has to tell them apart is
// Root.openPlanCmd (app/root.go). Naming the refusal makes the sourceless case
// a fact a caller can branch on rather than an accident it inherits.
var ErrNoSourcePath = errors.New("plan has no source path")

// ErrSourceUnreadable marks the FILE half of Open's failure: os.ReadFile
// refused. It wraps that refusal rather than replacing it, so
// errors.Is(err, fs.ErrNotExist) answers exactly as it always did and only a
// caller that needs to know WHICH HALF failed reaches for this.
//
// OPEN DOES TWO THINGS AND THEY FAIL FOR UNRELATED REASONS: it reads the file,
// then it resolves identity against the PlanService (resolveIdentity ->
// svc.ResolvePlan, Versions, establishBase), which reads the state file. The
// fault classification below asks "did this plan's file fail?"; answering that
// from an unmarked Open error makes an unreadable state file answer
// errors.Is(err, ErrSourceFileGone) -- "your file is unavailable" about a store
// fault, with the file untouched.
//
// The identity half is deliberately left UNMARKED. It is not one kind of
// failure -- it is every way the store can refuse -- and a second marker over
// the top would only invite a door to treat them as one thing.
var ErrSourceUnreadable = errors.New("source file could not be read")

// Open loads the file and resolves plan identity. It performs no writes of
// any kind: no snapshot, no registration, no plan creation. An empty path
// is refused outright with ErrNoSourcePath -- see there. A file that will
// not read is refused with ErrSourceUnreadable wrapping the cause; anything
// that fails after the read is an identity failure and is returned as it
// came.
func Open(ctx context.Context, svc client.PlanService, path string) (*Session, error) {
	if path == "" {
		return nil, ErrNoSourcePath
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSourceUnreadable, err)
	}
	s := &Session{svc: svc, Path: path, Content: content, Hash: domain.HashContent(content)}
	if err := s.resolveIdentity(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// resolveIdentity resolves plan identity against the session's Path and, if
// a plan is found, checks whether the session's already-loaded Content is
// the plan's LATEST registered version — not merely some version in its
// history. Content matching an older, superseded hash is intentionally left
// registered=false: it is a revert, and the first write action must
// re-register (re-promote) it as the newest version rather than treating it
// as already current. The shared tail of Open and OpenSupplied, which differ
// only in how Content arrives (file read vs. supplied bytes). An empty Path
// is not an identity — the session stays plan-less rather than matching
// whatever plan happens to carry an empty SourceHint.
func (s *Session) resolveIdentity(ctx context.Context) error {
	if s.Path == "" {
		return nil
	}
	plan, err := s.svc.ResolvePlan(ctx, s.Path)
	switch {
	case err == nil:
		s.Plan = plan
		s.Exists = true
	case errors.Is(err, client.ErrNoPlan):
		// Caller offers Create.
	default:
		return err
	}

	if !s.Exists {
		return nil
	}
	versions, err := s.svc.Versions(ctx, s.Plan.ID)
	if err != nil {
		return err
	}
	// Establishing the base we do not yet have -- legitimate; from here on it is
	// remembered, never refreshed.
	s.base = establishBase(versions)
	if s.base == s.Hash {
		s.registered = true
	}
	return nil
}

// establishBase is the compare-and-swap base a session adopts when it is
// establishing identity for a plan it does not yet hold one for --
// resolveIdentity, OpenVersion, OpenSuppliedForPlan, Create and Rediscover,
// never ensureRegistered, which must only ever remember (see Session.base): the
// plan's newest registered version, or empty for a plan with none. versions is
// whatever the caller already fetched from svc.Versions for its own reasons, so
// this never fetches a second time.
func establishBase(versions []domain.Version) domain.ContentHash {
	if len(versions) == 0 {
		return ""
	}
	return versions[len(versions)-1].Hash
}

// OpenVersion opens a review session on a plan's latest registered version,
// sourced from svc.Content instead of a file on disk. This is how a plan
// without a readable working copy gets reviewed -- one an agent saved from
// bytes, or one whose file is gone: the stored bytes ARE the document. The
// session behaves identically to a file-opened one -- the content is already
// registered, so writes attach to its hash without re-registration.
//
// svc.PlanByID answers whether the plan exists: client.ErrNotFound for a clean
// miss, and a failed read of this machine's state as that error, verbatim.
func OpenVersion(ctx context.Context, svc client.PlanService, id domain.PlanID) (*Session, error) {
	plan, err := svc.PlanByID(ctx, id)
	if err != nil {
		return nil, err
	}

	versions, err := svc.Versions(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("plan %s: no registered versions", id)
	}
	latest := versions[len(versions)-1]

	content, err := svc.Content(ctx, id, latest.Hash)
	if err != nil {
		return nil, err
	}

	return &Session{
		svc:          svc,
		Path:         plan.SourceHint,
		Content:      content,
		Hash:         latest.Hash,
		Plan:         plan,
		Exists:       true,
		registered:   true,
		base:         establishBase(versions),
		fromSnapshot: true,
	}, nil
}

// ErrSourceFileGone marks a plan whose own file cannot be opened as itself:
// deleted, moved, or become unreadable, or present but resolving to another
// plan or to no plan at all. SourceFileError carries which.
var ErrSourceFileGone = errors.New("source file is unavailable")

// SourceFileState names WHICH of the four things a plan's file did when it
// failed to answer as that plan's own document. ErrSourceFileGone's doc comment
// above enumerates the same four in PROSE, where a door that wanted to say WHICH
// one had happened had nothing to read. A "your file is gone" headline is true
// of exactly one of them; for the other three the file is sitting on disk
// where the user can see it, and a door that said "your file is gone" about
// those would be wrong three times out of four.
//
// THE ZERO VALUE IS NOT ONE OF THEM, deliberately (hence iota + 1). A fault is
// carried by POINTER at both carriers -- Session.SourceFault and the
// *SourceFileError a reload raises -- so "nothing is wrong" is nil and is never
// a state, and a zero-valued struct literal cannot pass itself off as
// SourceFileGone, the one state with a remedy a user might act on destructively.
type SourceFileState int

const (
	// SourceFileGone is the file that is not there: Open failed with
	// fs.ErrNotExist. Deleted, or moved, or renamed out from under the plan.
	// It is the ONE state sourceFaultText's "gone or has moved" headline is
	// true of, and the reason the other three had to be told apart from it.
	SourceFileGone SourceFileState = iota + 1

	// SourceFileUnreadable is the file that IS there and would not open: a
	// permission change, a path that became a directory, EIO. The remedy is
	// the user's to apply and the file is where they left it, so telling
	// them it is missing would send them looking for something that is not
	// lost.
	SourceFileUnreadable

	// SourceFileClaimed is the file that read fine and resolved to a DIFFERENT
	// plan than the one being opened -- two plans following one path, and the
	// other one won. SourceFileError.Plan carries that plan, title and all; see
	// that field's own doc comment for who still reads it and who does not.
	SourceFileClaimed

	// SourceFileReleased is the file that read fine and resolved to NO plan
	// at all -- the plan was deleted while its file stayed. It cannot share
	// SourceFileClaimed's sentence: there is no plan here to name.
	SourceFileReleased
)

// String is the vocabulary's own four words, so a failing test or a log
// prints "claimed" rather than "3".
func (s SourceFileState) String() string {
	switch s {
	case SourceFileGone:
		return "gone"
	case SourceFileUnreadable:
		return "unreadable"
	case SourceFileClaimed:
		return "claimed"
	case SourceFileReleased:
		return "released"
	}
	return fmt.Sprintf("SourceFileState(%d)", int(s))
}

// SourceFileError is ErrSourceFileGone plus WHICH of the four states above
// happened, the path it happened to, and -- for SourceFileClaimed alone -- the
// plan that took the file.
//
// IT JOINS THE SENTINEL RATHER THAN REPLACING IT: every errors.Is arm on
// ErrSourceFileGone behaves identically, and only a door that wants to SAY what
// happened reaches for errors.As.
//
// UNWRAP RETURNS TWO ERRORS, NOT ONE -- ErrSourceFileGone AND the file system's
// own failure -- so errors.Is(err, fs.ErrNotExist) answers true for a deleted
// file. The cause is the caller's only way to tell a deleted file from an
// unreadable one without reading State.
type SourceFileError struct {
	// PlanID is the plan the DOOR asked for -- what a message about this
	// names.
	PlanID domain.PlanID

	// Path is the file that did not answer: the path the open actually
	// tried, passed in rather than re-derived from the plan, so a door
	// naming it names the string this machine really read.
	Path string

	// State is which of the four things happened. Never the zero value on a
	// value this package built.
	State SourceFileState

	// Plan is the plan that claimed the file, set for SourceFileClaimed ONLY --
	// there is no other plan in any other state, and a door must test State rather
	// than this field's emptiness.
	//
	// NO PANEL SPENDS IT: sourceFaultText
	// (app/actions.go) says "another plan is already following this file," which
	// says what happened and deliberately not who did it, and switches on State
	// alone. Error() below still spends the ID. It is kept for a caller that DOES
	// want to name the claimant -- a future revision of that panel, or some other
	// surface -- because the fact is already in hand where the state is classified
	// rather than re-derived a second time.
	Plan domain.Plan

	// Err is the failure the file system reported, for SourceFileGone and
	// SourceFileUnreadable. Nil for the two states where the read SUCCEEDED and it
	// was identity, not IO, that failed.
	//
	// IT IS THE *fs.PathError AND NOT Open's WRAPPED REFUSAL. The marker belongs on
	// the ERROR -- Open returns it and sourceFileFault classifies on it -- and this
	// field carries what the operating system actually said; carrying the wrapped
	// form instead makes a panel draw Draftplane's own headline back in worse
	// words. See readCause, the one place the two are separated.
	//
	// NOTHING errors.Is ASKS OF THIS VALUE MOVES: Unwrap hands the cause out beside
	// the sentinel, and a *fs.PathError unwraps to its own errno, so
	// errors.Is(fault, fs.ErrNotExist) answers exactly as it did. What is not true
	// THROUGH THIS TYPE is errors.Is(fault, ErrSourceUnreadable), which nothing
	// asks: the callers that care about that marker (sourceFileFault's own first
	// arm, OpenPlan's fallback trigger) all read the error Open returned, where it
	// still is.
	Err error
}

// Error renders one sentence per state. The two read-fine states get their
// own sentences rather than sharing one -- still true of THIS sentence though
// no longer of the panel's own (sourceFaultText): claimed can name the other
// plan's id, released cannot share its words, because there is nothing there
// to name.
func (e *SourceFileError) Error() string {
	switch e.State {
	case SourceFileClaimed:
		return fmt.Sprintf("plan %s follows %s, but that file now resolves to plan %s: %v", e.PlanID, e.Path, e.Plan.ID, ErrSourceFileGone)
	case SourceFileReleased:
		return fmt.Sprintf("plan %s follows %s, but that file no longer resolves to it: %v", e.PlanID, e.Path, ErrSourceFileGone)
	default:
		if e.Err == nil {
			return fmt.Sprintf("plan %s follows %s: %v", e.PlanID, e.Path, ErrSourceFileGone)
		}
		return fmt.Sprintf("plan %s follows %s: %v: %v", e.PlanID, e.Path, ErrSourceFileGone, e.Err)
	}
}

// Unwrap returns the sentinel first and the file system's own failure
// second -- see the type's own comment for why both, and why dropping the
// second would be a narrowing rather than a simplification. A nil Err (the
// two identity states, or a struct literal composed outside this package)
// yields the sentinel alone rather than a nil entry errors.Is would have to
// step over.
func (e *SourceFileError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrSourceFileGone}
	}
	return []error{ErrSourceFileGone, e.Err}
}

// SourceFileFault classifies what a plan's own file did, for the carrier that is
// a SESSION rather than a refusal: OpenPlan latches what this returns on the
// snapshot it falls back to, and app.Model.runReload raises it for a file that
// stopped answering mid-review. It returns nil whenever there is no fault to
// say, so a caller assigns the result blind.
//
// sess is the session the open produced and openErr is what it answered; exactly
// one of them is ever meaningful, and a caller that only ever has the failure
// (reload) passes a nil session.
//
// WHAT KEEPS IT HONEST IS Open'S OWN TAXONOMY, not the caller's care: only a
// failure carrying ErrSourceUnreadable is a file failure at all. An identity
// failure -- an unreadable state file -- leaves the document sitting on disk and
// answers nil here, so no door can turn a state-read failure into "your file is
// unavailable". A path that is itself a URL source (client.URLSource) is the
// same kind of non-answer for a different reason: see sourceFileFault's own arm
// for that one.
func SourceFileFault(id domain.PlanID, path string, sess *Session, openErr error) *SourceFileError {
	return sourceFileFault(id, path, sess, openErr)
}

// sourceFileFault is the table, one arm per row, and the ONE place in this
// repository that decides which state a failed open was -- every carrier
// composes its value here.
//
// id is the plan this fault is being asked about: it is the PlanID recorded
// on a non-nil result, and it is the identity the file at path must still
// resolve to for the open to count as this plan's own -- the last two arms
// below ask exactly that.
//
// THE FIRST ARM IS THE WHOLE OF THE TAXONOMY QUESTION: a failure that is not
// ErrSourceUnreadable is not this file's failure. It covers the identity half of
// Open (a corrupt state read) AND ErrNoSourcePath, the sourceless plan's own
// refusal. Both answer nil, and the arm cannot be reordered into a special
// case for either without re-admitting the other.
//
// THE ARM AHEAD OF IT IS A SECOND, NARROWER TAXONOMY QUESTION, AND IT HAS TO BE
// ASKED FIRST: client.URLSource(path) -- a notion://, https://, or other
// scheme URI standing in for path -- is never a file this machine could lose,
// whatever Open's error says. The false SourceFileGone this arm guards against
// was traced to mcptools.applyMCPSourceFault and session.OpenPlan BOTH calling
// Open on a plan's raw SourceHint with no guard at all: for a URL source, Open's
// os.ReadFile(path) tries to read the literal URL string, fails ENOENT, and gets
// wrapped in ErrSourceUnreadable exactly as a genuinely deleted file would --
// so without this arm, the FIRST arm's own ErrSourceUnreadable check lets it
// through, and the fs.ErrNotExist arm below reports SourceFileGone for a plan
// whose file was never on disk to begin with. This is TWO DOORS' bug, not one:
// OpenPlan is the TUI's list-open door and has carried it since before this
// package split source-fault reporting out to MCP, so a Notion- or
// URL-sourced plan opened from the list raised the same false "gone or has
// moved" panel a genuinely lost file does -- reproduced directly against both
// doors before this arm was added. Each door's own test package now carries
// the row that pins it: session's
// TestOpenPlanLatchesTheFaultOnTheSnapshotItFallsBackTo and mcptools'
// TestResolveComputesNoSourceFaultWhenThereIsNothingToReport.
//
// THE GUARD BELONGS HERE AND NOT AT EITHER CALL SITE, AND NOT INSIDE Open
// ITSELF. Open is a shared primitive with callers that never hand it a URL
// source -- resolve's readable-local-file branch (mcptools/tools.go) has already
// asked client.URLSource and only reaches Open when it answered false;
// openReviewTarget (cmd/draftplane/main.go) only reaches Open after
// localReviewFile has stat'd rawPath as a real file; runReload's file arm
// (app/actions.go) is reachable only because Open already read this exact path
// successfully once. Rejecting a URL source inside Open would need a new
// sentinel AND a matching addition to OpenPlan's own fallback-trigger
// disjunction (its switch reads the error Open returned, and a sentinel that is
// not ErrNoSourcePath or fs.ErrNotExist would fall to its default arm and
// refuse the open outright, rather than falling back to the snapshot this plan
// already has) -- two edits, in two files, to solve a problem that is entirely
// about how a failed read gets CLASSIFIED. Putting the check at the two doors
// instead (OpenPlan, applyMCPSourceFault) fares no better: it states the same
// question twice (mcptools cannot reach an unexported helper here),
// and neither door is asking "is this failure a fault" in the first place --
// applyMCPSourceFault's own gate is FromSnapshot, a question about the session
// rather than the file. This function is already the ONE place that turns a
// failed Open into a state (its own doc comment above), so a second cause of
// "not this file's failure" is one more case in the switch that owns exactly
// that question, not a new question asked somewhere else. The wasted
// os.ReadFile attempt on the literal URL string is accepted for the same
// reason ErrNoSourcePath does not either: a local disk read that fails
// immediately costs nothing worth a second code path to avoid.
//
// client.URLSource IS A PURE STRING TEST -- trim a leading "file://", then
// ask whether "://" remains anywhere in what's left -- and this arm inherits
// exactly the shape that defeats it. A file:// URL is handled correctly BY
// THAT TRIM: "file:///home/alice/plan.md" answers false, stays a local file in
// this arm's eyes, and its absence still reports SourceFileGone
// (TestSourceFileFaultDoesNotExemptAFileURL pins this, because a bare
// strings.Contains(source, "://") would be the obvious way to "simplify"
// URLSource later and would silently swallow the report for every
// file://-sourced plan at once). What the trim CANNOT tell apart is a scheme
// URI from a local path that merely contains the substring "://" somewhere
// past it: a real file at "/home/alice/weird://name.md" -- an ordinary path
// through a directory whose name happens to end in a colon, the "//"
// collapsing the way any doubled path separator does -- answers URLSource true
// and this arm exempts it, wrongly; that file going missing now reports nothing
// on either door. Reachable, if unusual, and NOT fixed here: URLSource is also
// canonicalSource's own identity predicate, deciding which sources unify onto
// the same plan, so narrowing it to close this one path shape is a change to
// plan identity across the whole client package and a strictly larger question
// than this arm's own. This arm takes the predicate's answer as given, exactly
// as canonicalSource, localFile and every other caller already do, and the gap
// is recorded here rather than left for a future reader to rediscover the day
// someone names a directory after a URL scheme.
func sourceFileFault(id domain.PlanID, path string, sess *Session, openErr error) *SourceFileError {
	switch {
	case client.URLSource(path):
		return nil
	case openErr != nil && !errors.Is(openErr, ErrSourceUnreadable):
		return nil
	case errors.Is(openErr, fs.ErrNotExist):
		return &SourceFileError{PlanID: id, Path: path, State: SourceFileGone, Err: readCause(openErr)}
	case openErr != nil:
		return &SourceFileError{PlanID: id, Path: path, State: SourceFileUnreadable, Err: readCause(openErr)}
	case sess == nil || !sess.Exists:
		return &SourceFileError{PlanID: id, Path: path, State: SourceFileReleased}
	case sess.Plan.ID != id:
		return &SourceFileError{PlanID: id, Path: path, State: SourceFileClaimed, Plan: sess.Plan}
	}
	return nil
}

// readCause is what the FILE SYSTEM said, dug out of what Open returned:
// os.ReadFile answers a *fs.PathError, and Open wraps it with ErrSourceUnreadable
// to mark WHICH HALF of itself failed. The two facts are for two different
// audiences -- the marker is how this package tells a file failure from an
// identity one, and the cause is the only part a human can act on -- so
// SourceFileError carries them apart.
//
// It answers the whole error unchanged when there is no *fs.PathError in it, so
// a future producer that is not os.ReadFile still has its failure reported
// rather than swallowed.
func readCause(openErr error) error {
	var pe *fs.PathError
	if errors.As(openErr, &pe) {
		return pe
	}
	return openErr
}

// OpenByID opens a session for a plan already identified by id: the plan's
// latest registered version, through OpenVersion. It is the one call
// `draftplane review <plan id>` (cmd/draftplane) and mcptools' id-addressed
// opens share.
func OpenByID(ctx context.Context, svc client.PlanService, id domain.PlanID) (*Session, error) {
	return OpenVersion(ctx, svc, id)
}

// OpenPlan opens ONE plan the way every door has to open it: the plan's own file
// first (Open on SourceHint), the plan's latest registered version second
// (OpenVersion), and the reason the fallback fired recorded on the session it
// produced.
//
// IT EXISTS SO THE THREE DOORS CANNOT DISAGREE -- the plan list
// (app.Root.openPlanCmd), `draftplane review <path>` (cmd/draftplane's
// openReviewTarget) and reload, which each used to decide for themselves what a
// missing file meant.
//
// ALWAYS THE FILE FIRST, rather than trusting anything a caller derived earlier,
// which makes the decision robust in both staleness directions at once. A file
// that vanished between a refresh and an enter falls back to the snapshot
// instead of surfacing the error, and a file another actor materialized at
// SourceHint after that refresh (creating a file touches no state.json, so no
// watch event re-derives anything) opens as the working copy it now is rather
// than a snapshot blind to it.
//
// THE FALLBACK ITSELF IS NARROW: exactly three facts fire it and nothing else
// does. The plan has NO PATH AT ALL (ErrNoSourcePath -- the sourceless plan,
// whose SourceHint is "" and whose only honest answer is "open this by id
// instead"); the FILE WOULD NOT READ -- missing for any plan
// (fs.ErrNotExist), and for a plan with a fault to show, any other read failure
// too; or it read and DID NOT RESOLVE to the plan asked for -- either to no plan
// (the plan record was deleted between refresh and enter while its file
// lingered; accepting that session would silently present an empty, plan-less
// review whose first write re-creates the plan under a new id) or to a DIFFERENT
// one.
//
// THE SECOND TRIGGER COVERS MORE THAN fs.ErrNotExist, DELIBERATELY, stated
// here because the old narrowness is the kind of rule a later reader
// re-derives and "restores". The reason to be narrow was never that
// an unreadable file deserves a hard error on its merits -- it was that falling
// back would SILENTLY swap a real, possibly diverged working copy for a frozen
// snapshot, and comments would then anchor against content the user never saw.
// Every word of that still holds except SILENTLY: the session carries SourceFault,
// and the door says which of the four things happened.
//
// SO THE WIDENING EXTENDS EXACTLY AS FAR AS THE FAULT DOES, AND NOT ONE PLAN
// FURTHER, which is why the condition below reads `fault != nil` rather than
// naming ErrSourceUnreadable a second time. Where sourceFileFault answers nil there
// is nothing to speak, so a widened fallback there would swap the document in silence
// and hand snapshotOpenStatus a sentence -- "reviewing snapshot (no local copy)" --
// that is false over a local copy sitting right there on disk. Writing the
// condition as the fault itself is what makes the two extents impossible to drift
// apart: there is no second predicate to update.
//
// What is NOT widened for anybody is the identity half: an unreadable state file
// is not a fact about the document, has no panel to raise, and stays a hard error
// on the default arm below. That is the line Open's own taxonomy draws (see
// ErrSourceUnreadable), and it is why fs.ErrNotExist still appears in the
// condition on its own account: a missing file has fired this fallback for every
// plan since long before the widening.
//
// A hard failure (the file failed AND the fallback fails too) surfaces the
// fallback's own error, not the doomed file open's: for a plan that never had a
// file the file error would misdirect -- the plan is not broken for lacking a
// file, it is broken for lacking versions.
//
// ONE BODY FOR EVERY TRIGGER, and the fault is CAPTURED BEFORE IT: what
// happens after any of them fires is a single rule -- open
// this plan's latest version -- and splitting it into an arm each is how a later
// change comes to hold for some of them and not the rest. So the classification
// is one line ahead of the shared body and changes no control flow at all. It
// must be a classification and not a latch across the whole fallback: not every
// trigger is a fault for this plan (sourceFileFault's own first arm), and a helper
// that recorded one for all of them would put "your file is gone" in front of
// the sourceless plan too.
//
// THE CLASSIFICATION IS ASKED BEFORE the switch rather than inside the fallback
// arm, which is not a rearrangement: the fallback's own condition reads the
// result, so it has to have been asked first.
func OpenPlan(ctx context.Context, svc client.PlanService, plan domain.Plan) (*Session, error) {
	sess, err := Open(ctx, svc, plan.SourceHint)
	fault := SourceFileFault(plan.ID, plan.SourceHint, sess, err)
	switch {
	case err == nil && sess.Exists && sess.Plan.ID == plan.ID:
		return sess, nil
	case errors.Is(err, ErrNoSourcePath) || err == nil || errors.Is(err, fs.ErrNotExist) || fault != nil:
		snap, snapErr := OpenVersion(ctx, svc, plan.ID)
		if snapErr != nil {
			return nil, snapErr
		}
		snap.SourceFault = fault
		return snap, nil
	default:
		return nil, err
	}
}

// OpenSupplied constructs a session over agent-supplied bytes — the entry
// path for sources draftplane cannot read (URLs, other machines, no source at
// all). Identity resolves like Open: by source hint when one is given, else
// the session starts plan-less (Exists=false) and the caller creates.
// Like Open, this performs NO writes; Register (or the first write action)
// persists. source may be "" for a sourceless plan.
func OpenSupplied(ctx context.Context, svc client.PlanService, content []byte, source string) (*Session, error) {
	// Clone: the session's Hash is computed once from these bytes and never
	// re-verified, so a caller reusing its buffer must not be able to desync
	// the Content/Hash pair underneath registered review facts.
	content = bytes.Clone(content)
	s := &Session{svc: svc, Path: source, Content: content, Hash: domain.HashContent(content), fromSnapshot: true}
	if err := s.resolveIdentity(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// OpenSuppliedForPlan constructs a session over agent-supplied bytes for a
// plan the caller has already looked up by id — the explicit-plan variant
// of OpenSupplied, for identity that cannot be resolved by source hint. A
// sourceless plan is the reason this exists: OpenSupplied("", ...) can never
// find it (empty Path is never an identity, by design — see
// resolveIdentity), so an id-addressed update needs a way in that skips
// source-hint resolution entirely. registered is computed exactly like
// resolveIdentity: true only when content matches plan's LATEST version, so
// the caller's ensureRegistered gets a correct idempotence signal from the
// session itself rather than depending on the PlanService backend to
// dedupe RegisterVersion calls on its own.
func OpenSuppliedForPlan(ctx context.Context, svc client.PlanService, content []byte, plan domain.Plan) (*Session, error) {
	content = bytes.Clone(content)
	s := &Session{
		svc:          svc,
		Path:         plan.SourceHint,
		Content:      content,
		Hash:         domain.HashContent(content),
		Plan:         plan,
		Exists:       true,
		fromSnapshot: true,
	}
	versions, err := svc.Versions(ctx, plan.ID)
	if err != nil {
		return nil, err
	}
	s.base = establishBase(versions)
	if s.base == s.Hash {
		s.registered = true
	}
	return s, nil
}

// Rediscover re-resolves plan identity for a session that found no plan at
// Open. Exists is a snapshot taken once, at Open time; a plan another actor
// creates for this path afterward is otherwise invisible to this session for
// its entire lifetime. Rediscover is a no-op once Exists is already true — a
// plan never disappears out from under a session — and mirrors Open's own
// resolution exactly: a still-missing plan is not an error, it just leaves
// Exists false for a later call to retry. On success, registered is left
// false rather than assumed true: the plan's registered version may belong
// to another actor, and ensureRegistered runs RegisterVersion unconditionally
// on this session's next write, which self-heals (a no-op if that version
// already is the tip) rather than trusting the stale flag.
func (s *Session) Rediscover(ctx context.Context) error {
	if s.Exists {
		return nil
	}
	// A sourceless session (OpenSupplied with source "") has no identity to
	// re-resolve; see resolveIdentity's empty-Path rule.
	if s.Path == "" {
		return nil
	}
	plan, err := s.svc.ResolvePlan(ctx, s.Path)
	switch {
	case err == nil:
		s.Plan = plan
		s.Exists = true
		s.registered = false
		// A plan that appeared underneath us is one we have never held a
		// base for: establish it from what that plan actually holds. A first
		// read, not a refresh.
		versions, err := s.svc.Versions(ctx, plan.ID)
		if err != nil {
			return err
		}
		s.base = establishBase(versions)
		return nil
	case errors.Is(err, client.ErrNoPlan):
		return nil
	default:
		return err
	}
}

// Create registers this path as a new plan with the current content as its
// initial version. It can return a *client.ConflictError: on a lost create
// race CreatePlan hands back an existing plan whose tip another actor may
// move before this session registers against it.
func (s *Session) Create(ctx context.Context, title string) error {
	if s.Exists {
		return fmt.Errorf("plan already exists for %s", s.Path)
	}
	plan, err := s.svc.CreatePlan(ctx, title, s.Path, "", s.Content)
	if err != nil {
		return err
	}
	s.Plan = plan
	s.Exists = true
	// Do not assume the initial version we passed is the one registered:
	// CreatePlan has return-existing semantics on duplicate SourceHint, so a
	// lost create race can hand back a plan that has never seen our hash.
	// Establish the base from what that plan actually holds -- this is a
	// first read, not a refresh -- then ensureRegistered registers it if
	// needed.
	versions, err := s.svc.Versions(ctx, plan.ID)
	if err != nil {
		return err
	}
	s.base = establishBase(versions)
	s.registered = false
	return s.ensureRegistered(ctx)
}

// ensureRegistered registers the current content as the plan's newest
// version before any write that references its hash, unless it already is,
// under a compare-and-swap on the base this session remembers. A
// *client.ConflictError travels straight back out: the tip moved and the
// caller must offer to fetch.
func (s *Session) ensureRegistered(ctx context.Context) error {
	if !s.Exists {
		return ErrPlanNotCreated
	}
	if s.registered {
		return nil
	}
	// s.base, not a fresh Versions() call. See Session.base.
	if _, err := s.svc.RegisterVersion(ctx, s.Plan.ID, client.VersionRegistration{
		Content: s.Content,
		Base:    s.base,
	}); err != nil {
		return err
	}
	s.registered = true
	s.base = s.Hash
	return nil
}

// Register snapshots and registers the current content as the plan's newest
// version — save's explicit write. Idempotent for already-registered
// content. Returns a *client.ConflictError when the tip moved after this
// session established its base; the caller must offer to fetch rather than
// treat it as a failure.
func (s *Session) Register(ctx context.Context) error {
	return s.ensureRegistered(ctx)
}

func (s *Session) Comment(ctx context.Context, anchor reanchor.Anchor, body string) (domain.Thread, error) {
	if err := s.ensureRegistered(ctx); err != nil {
		return domain.Thread{}, err
	}
	return s.svc.CreateThread(ctx, s.Plan.ID, s.Hash, anchor, body)
}

// Reply keeps its thread-id-only signature, as do Resolve and RehomeToAnchor
// below: the plan client.PlanService now wants alongside the thread id is
// s.Plan.ID, which this session already holds and every caller already
// addressed it by. Widening these three to take one would make every caller
// re-supply the plan the session IS -- and would let a caller supply a
// different one, which is the whole class of mistake the plan id exists to
// refuse.
func (s *Session) Reply(ctx context.Context, tid domain.ThreadID, body string) (domain.Comment, error) {
	if !s.Exists {
		return domain.Comment{}, ErrPlanNotCreated
	}
	return s.svc.Reply(ctx, s.Plan.ID, tid, body)
}

func (s *Session) Resolve(ctx context.Context, tid domain.ThreadID, resolved bool) error {
	if !s.Exists {
		return ErrPlanNotCreated
	}
	return s.svc.SetResolved(ctx, s.Plan.ID, tid, resolved)
}

// RehomeToAnchor durably re-anchors a thread onto the current version —
// orphan adjudication's write path.
func (s *Session) RehomeToAnchor(ctx context.Context, tid domain.ThreadID, anchor reanchor.Anchor) error {
	if err := s.ensureRegistered(ctx); err != nil {
		return err
	}
	return s.svc.Rehome(ctx, s.Plan.ID, tid, s.Hash, anchor)
}

// Approve records approval of the current loaded content. The hash is
// captured silently — callers never ask the user for it.
func (s *Session) Approve(ctx context.Context) error {
	if err := s.ensureRegistered(ctx); err != nil {
		return err
	}
	return s.svc.Approve(ctx, s.Plan.ID, s.Hash)
}

func (s *Session) ApprovedCurrent(ctx context.Context) (bool, error) {
	if !s.Exists {
		return false, nil
	}
	approvals, err := s.svc.Approvals(ctx, s.Plan.ID)
	if err != nil {
		return false, err
	}
	for _, a := range approvals {
		if a.Hash == s.Hash {
			return true, nil
		}
	}
	return false, nil
}

// AnchorForBlockText builds the anchor for a block-granularity comment.
func AnchorForBlockText(doc, span string, headingHint []string) (reanchor.Anchor, error) {
	return reanchor.CreateAnchor(doc, span, headingHint)
}

// SectionAnchor builds the anchor for a comment on a section itself, located
// by heading path alone (no span).
func SectionAnchor(doc string, headingPath []string) (reanchor.Anchor, error) {
	return reanchor.CreateSectionAnchor(doc, headingPath)
}

// Placements re-anchors every thread onto the current content. Anchors are
// self-contained (heading path + span), so this needs no old snapshots -- it is a
// pure recomputation, a materialized view that is never stored.
//
// The recomputation is placement.PlaceThreads, called rather than re-declared or
// wrapped in a rule of its own: the TUI and mcptools both answer "where does this
// thread land" through this one function.
func (s *Session) Placements(ctx context.Context) ([]placement.Placement, error) {
	if !s.Exists {
		return nil, nil
	}
	threads, err := s.svc.Threads(ctx, s.Plan.ID)
	if err != nil {
		return nil, err
	}
	return placement.PlaceThreads(threads, string(s.Content)), nil
}

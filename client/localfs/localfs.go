// Package localfs is the file-backed PlanService: every plan's review facts as
// one JSON document on this machine. Attribution for every write is
// read from the context (client.AttributionFrom), not fixed at
// construction: one process can serve more than one attributed identity
// over its lifetime, so the identity has to travel with the call, not the
// Store.
//
// Concurrency: safe across goroutines AND processes on a LOCAL filesystem.
// Every operation holds an exclusive kernel flock on a stable sibling
// lockfile (<state>.lock), reloads state.json, operates, and (for writes)
// saves atomically before releasing. The kernel releases the lock on process
// death — no stale-lock state exists. Synced or network filesystems
// (NFS, Dropbox-managed dirs) are NOT supported; point XDG_DATA_HOME at a
// local path if your home directory is synced.
package localfs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/reanchor"
	"github.com/draftplane/draftplane/store/localcas"
)

type Store struct {
	path string
	// cas is this store's own content-addressed snapshot store, answering
	// Content -- see that method's own comment for why it lives here rather
	// than arriving as a constructor argument.
	cas *localcas.Store

	mu sync.Mutex
	st state
}

type state struct {
	Plans     []domain.Plan
	Versions  map[domain.PlanID][]domain.Version
	Threads   []domain.Thread
	Approvals []domain.Approval
}

// legacyState/legacyPlan let reload decode a state.json written before the
// domain.Plan PathHint→SourceHint rename. domain.Plan itself carries no JSON
// tags, so that rename silently changed the persisted key: an old file's plans
// would otherwise decode with an empty SourceHint. legacyState embeds state
// rather than re-declaring Versions/Threads/Approvals itself — those decode
// straight through the promoted fields, so a future field added to state needs
// no matching update here — and shadows only Plans, the one field whose
// decoding must differ.
type legacyState struct {
	state
	Plans []legacyPlan
}

type legacyPlan struct {
	domain.Plan
	PathHint string
}

// New builds a Store and does the first, construction-time reload — err
// surfaces synchronously here for corrupt state, rather than deferring the
// failure to whatever operation happens to run first.
//
// New takes no actor: a Store is shared across every call a process makes over
// its lifetime, and one draftplane mcp process serves both a parent agent and
// every subagent it spawns, each its own attributed identity. A value baked in
// here would be one identity for the whole process -- exactly the shape of bug
// that let $DRAFTPLANE_ACTOR make an agent's writes sign as the human running
// the TUI. Each write instead reads client.AttributionFrom off its own context,
// so attribution varies per call the way the callers issuing those calls do.
//
// New takes no content store either, and that is deliberate rather than an
// oversight Content merely papers over. cas is derived from path -- a sibling
// "objects" directory next to the state file -- because path already names
// exactly where THIS Store's own content belongs, so there is no second axis a
// caller could legitimately want to point it at instead. Taking cas as a second
// parameter would let a caller supply one pointed somewhere else, reopening the
// two-independently-chosen-parameters hazard one call site at a time.
func New(path string) (*Store, error) {
	s := &Store{path: path, cas: localcas.New(filepath.Join(filepath.Dir(path), "objects"))}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// EnsureDir creates the directory that holds the state file at path, owner-only.
//
// IT IS EXPORTED FOR ONE CALLER AND THAT CALLER IS THE REASON THE MODE WAS
// WRONG IN PRODUCTION. This Store creates its directory lazily -- save's first
// write, or withLock's entry -- so cmd/draftplane's buildDeps has to create it
// eagerly before startWatch arms the watch, and buildDeps runs FIRST: on a
// fresh install that call, not this package, decides the mode, and MkdirAll
// leaves an existing directory alone. One exported door means the mode is
// spelled once and there is no second site to drift.
func EnsureDir(path string) error { return os.MkdirAll(filepath.Dir(path), 0o700) }

// save persists atomically (temp + rename). Callers hold s.mu.
//
// 0600 IN A 0700 DIRECTORY, matching client/config. This file is every plan's
// title and source, every thread and the comment bodies in it, every approval.
//
// THE STALE TEMP IS REMOVED FIRST AND THAT IS NOT TIDINESS. The temp has a
// stable name, so a save that crashed between write and rename leaves one
// behind; os.WriteFile leaves an EXISTING file's mode alone, and the rename
// installs whatever mode the temp carries, so a 0644 leftover would hand 0644
// back to state.json however this call spells its own mode. Removing it means
// the file this rename installs was created 0600 by this call. Safe under the
// flock every caller holds (see withLock), which is what makes the
// remove-then-create pair indivisible against another process.
//
// NOTHING HERE REPAIRS AN EXISTING FILE OR DIRECTORY. MkdirAll leaves a
// directory that already exists at whatever mode it has, and no chmod runs on a
// state.json written before this change: all local data is throwaway, and
// nothing is built to assist a migration.
func (s *Store) save() error {
	data, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	if err := EnsureDir(s.path); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// withLock serializes an operation against every other Store in every process:
// in-process via s.mu, cross-process via an exclusive flock on a stable
// lockfile (state.json itself is replaced by rename on save, so its inode
// cannot carry the lock). State is reloaded from disk before fn runs, making
// s.st per-operation scratch — helpers like findThread that take interior
// pointers are safe because they run after the reload inside the same hold.
func (s *Store) withLock(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A fresh install has no state dir yet, and the dir can also vanish
	// mid-run if something external removes it between operations — ensure
	// it exists on every entry rather than only in New.
	if err := EnsureDir(s.path); err != nil {
		return err
	}
	// 0600, like the file it guards: the lockfile carries no content, but its
	// name says this machine has a Draftplane store and where it is, and every
	// sibling store's lockfile is 0600 already. It is created HERE rather than
	// in save, so a mode fixed only there would have left this one behind.
	lf, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = lf.Close() }() // close releases the flock
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	if err := s.reload(); err != nil {
		return err
	}
	return fn()
}

// reload replaces s.st with current on-disk state. Called both under the lock
// (withLock) and from New, before s is shared with anyone — one decode path for
// both means New's construction-time read gets the same legacy migration as
// every later reload. Decodes through legacyState so a plan whose SourceHint
// arrives empty falls back to its legacy PathHint value — self-healing, since
// the next save persists every plan under the current field name.
func (s *Store) reload() error {
	s.st = state{Versions: map[domain.PlanID][]domain.Version{}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var shadow legacyState
	if err := json.Unmarshal(data, &shadow); err != nil {
		return fmt.Errorf("corrupt local store %s: %w", s.path, err)
	}
	s.st = shadow.state
	if shadow.Plans != nil {
		s.st.Plans = make([]domain.Plan, len(shadow.Plans))
		for i, p := range shadow.Plans {
			plan := p.Plan
			if plan.SourceHint == "" && p.PathHint != "" {
				plan.SourceHint = p.PathHint
			}
			s.st.Plans[i] = plan
		}
	}
	if s.st.Versions == nil {
		s.st.Versions = map[domain.PlanID][]domain.Version{}
	}
	return nil
}

// newID mints an identifier. Every one carries the l_ prefix, the shape every id
// already in a state.json has, so one store never holds two shapes of its own
// minting; the prefix also marks a Draftplane id wherever it travels -- chat,
// agent arguments, logs.
func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "l_" + hex.EncodeToString(b[:])
}

func (s *Store) ListPlans(_ context.Context) ([]domain.Plan, error) {
	var out []domain.Plan
	err := s.withLock(func() error {
		out = make([]domain.Plan, len(s.st.Plans))
		for i, p := range s.st.Plans {
			out[i] = s.planWithDerivedFacts(p)
		}
		return nil
	})
	return out, err
}

// ResolvePlan looks a plan up by its exact SourceHint. An empty sourceHint
// never matches — mirroring CreatePlan's never-equal rule — even if a
// sourceless plan happens to exist, so this always misses for one rather
// than returning it arbitrarily.
func (s *Store) ResolvePlan(_ context.Context, sourceHint string) (domain.Plan, error) {
	var out domain.Plan
	err := s.withLock(func() error {
		for _, p := range s.st.Plans {
			if sourceHint != "" && p.SourceHint == sourceHint {
				out = s.planWithDerivedFacts(p)
				return nil
			}
		}
		return fmt.Errorf("%s: %w", sourceHint, client.ErrNoPlan)
	})
	return out, err
}

// PlanByID answers one plan by id -- ResolvePlan's twin for the other identity.
//
// IT ANSWERS FROM THE PLAN SET, NEVER FROM THE VERSION LOG. This Store holds
// the records themselves, so the log would be a strictly worse question here
// AND a wrong one: a state file can hold a plan whose version log is empty,
// and a log-based predicate would report that plan as nonexistent.
//
// A miss is client.ErrNotFound, wrapped exactly as this Store's write methods
// wrap it -- see client.PlanService.PlanByID for why a read validates existence
// at all. There is no third outcome.
func (s *Store) PlanByID(_ context.Context, id domain.PlanID) (domain.Plan, error) {
	var out domain.Plan
	err := s.withLock(func() error {
		for _, p := range s.st.Plans {
			if p.ID == id {
				out = s.planWithDerivedFacts(p)
				return nil
			}
		}
		return fmt.Errorf("plan %s: %w", id, client.ErrNotFound)
	})
	return out, err
}

// planWithDerivedFacts fills p's derived, never-persisted fields (see
// domain.Plan's own doc comment for why they carry json:"-") from s.st,
// computed here because this Store already holds every plan's facts in memory.
// Every method on this Store that returns a domain.Plan routes through this one
// function rather than each recomputing its own copy, so its answers can only
// agree or disagree in ONE place.
//
// Callers must already hold s.mu (every call site is inside a withLock closure,
// after that closure's own reload has run): this reads
// s.st.Threads/Versions/Approvals directly rather than through those methods'
// own locked accessors, which would deadlock re-entering s.withLock.
//
// Open/total threads, tip-approved and last-activity are computed by the exact
// rule app/list.go's own list-row derivation used before this Store took it
// over: tip-approved reports whether any approval's Hash equals the LAST
// element of p's version log (append order, so the newest), and last activity
// is the max of every version's RegisteredAt, every comment's CreatedAt and
// every approval's CreatedAt for p. Reproducing that exact rule, rather than
// something merely equivalent, is what keeps a plan's list-row sort position
// unchanged.
//
// It carries forward a real gap that rule always had: last activity does not
// move on resolve or rehome, because domain.Thread carries no timestamp for
// either event. See domain.Plan's own LastActivityAt doc comment for the
// user-visible consequence.
//
// TipApproved's contract is hash-equality against the plan's LATEST REGISTERED
// VERSION, so a plan with no registered version reports false whatever
// approvals it carries.
func (s *Store) planWithDerivedFacts(p domain.Plan) domain.Plan {
	versions := s.st.Versions[p.ID]
	var latestHash domain.ContentHash
	if len(versions) > 0 {
		latestHash = versions[len(versions)-1].Hash
	}

	var open, total int
	var last time.Time
	for _, t := range s.st.Threads {
		if t.Plan != p.ID {
			continue
		}
		total++
		if !t.Resolved {
			open++
		}
		for _, c := range t.Comments {
			if c.CreatedAt.After(last) {
				last = c.CreatedAt
			}
		}
	}
	for _, v := range versions {
		if v.RegisteredAt.After(last) {
			last = v.RegisteredAt
		}
	}
	var tipApproved bool
	for _, a := range s.st.Approvals {
		if a.Plan != p.ID {
			continue
		}
		if a.CreatedAt.After(last) {
			last = a.CreatedAt
		}
		if len(versions) > 0 && a.Hash == latestHash {
			tipApproved = true
		}
	}

	p.OpenThreads = open
	p.TotalThreads = total
	p.TipApproved = tipApproved
	p.LastActivityAt = last
	// CreatedAt is the FIRST version's registration, append order, which is
	// the version CreatePlan writes in the same breath as the plan record --
	// so it is the plan's own creation instant.
	if len(versions) > 0 {
		p.CreatedAt = versions[0].RegisteredAt
	}
	return p
}

// CreatePlan creates a new plan keyed by sourceHint, content as its initial
// version. If a plan with the same non-empty SourceHint already exists it is
// returned unchanged (return-existing semantics): callers that race on the same
// source converge on one plan rather than forking review facts across
// duplicates, and content is never even hashed on that path. An empty
// sourceHint is never a match — every sourceless CreatePlan call creates a
// fresh plan, mirroring the session layer's "empty Path is not an identity"
// rule.
//
// content is put into s.cas before the version row is created, the same
// put-then-reference sequencing RegisterVersion uses below, and s.cas.Put's
// returned hash -- not a caller-supplied one -- is what the stored version and
// the plan's first Content read agree on.
func (s *Store) CreatePlan(ctx context.Context, title, sourceHint, repoHint string, content []byte) (domain.Plan, error) {
	var plan domain.Plan
	err := s.withLock(func() error {
		for _, p := range s.st.Plans {
			if sourceHint != "" && p.SourceHint == sourceHint {
				plan = s.planWithDerivedFacts(p)
				return nil
			}
		}
		hash, err := s.cas.Put(ctx, content)
		if err != nil {
			return err
		}
		stamped, err := s.stamp(ctx, domain.Version{VersionRef: domain.VersionRef{Hash: hash}})
		if err != nil {
			return err
		}
		plan = domain.Plan{
			ID:         domain.PlanID(newID()),
			Title:      title,
			SourceHint: sourceHint,
			RepoHint:   repoHint,
		}
		s.st.Plans = append(s.st.Plans, plan)
		s.st.Versions[plan.ID] = []domain.Version{stamped}
		// The plan's derived fields are computed AFTER s.st.Versions is
		// updated above, from the same in-memory state ListPlans/ResolvePlan
		// read, rather than left zero-valued: a just-created plan already has
		// one fact (its first version), and a caller comparing this return
		// value against a subsequent ListPlans must see the same plan.
		plan = s.planWithDerivedFacts(plan)
		return s.save()
	})
	if err != nil {
		return domain.Plan{}, err
	}
	return plan, nil
}

// stamp attributes v to the write making it, read off ctx, and returns
// client.ErrNoAttribution unchanged when ctx carries none (see
// client.AttributionFrom) rather than falling back to a default -- a silent
// default here is exactly the shape of bug a per-call requirement exists to
// rule out.
func (s *Store) stamp(ctx context.Context, v domain.Version) (domain.Version, error) {
	a, err := client.AttributionFrom(ctx)
	if err != nil {
		return domain.Version{}, err
	}
	v.Attribution = a
	v.RegisteredAt = time.Now().UTC()
	return v, nil
}

func (s *Store) findPlan(id domain.PlanID) bool {
	for _, p := range s.st.Plans {
		if p.ID == id {
			return true
		}
	}
	return false
}

// RegisterVersion makes the hash of r.Content the plan's newest version under a
// compare-and-swap on r.Base. The three arms, and their order, are
// client.PlanService.RegisterVersion's contract. Every arm answers the tip as
// a domain.VersionRef, including the no-op: a success whose tip is knowable
// must not come back as a zero value.
//
//	hash(r.Content) == tip -> no-op success, tip returned unchanged.
//	                          Registering content that is already current is
//	                          nothing to do, whatever base the caller
//	                          believed. Checked FIRST so an idle save from a
//	                          stale writer does not prompt a human to fetch
//	                          bytes they already hold.
//	r.Base != tip           -> *client.ConflictError carrying the tip (a
//	                          zero VersionRef travels as the ordinary return
//	                          value; the real tip is on the error).
//	otherwise               -> put, then append: r.Content lands in s.cas
//	                          before the version row is created, so a
//	                          version never exists in this log without its
//	                          bytes already in the store that backs Content.
//	                          The new tip is returned.
//
// s.cas.Put is called ONLY on the append arm above, never on the no-op or
// conflict arms, and that placement is deliberate: client.PlanService states the
// rule this enforces -- "content enters the store only when a review fact
// references it" -- so content for a conflicted or no-op register must never
// reach s.cas. Hoisting this Put above the no-op/conflict checks would silently
// leak an unreferenced blob into local storage on every rejected write.
//
// Re-registering a hash that appears EARLIER in history -- an agent reverting to
// prior content -- passes the first arm and appends, re-promoting that content
// to newest. The log is append-only and must never carry a unique constraint on
// (plan, hash). Content for such a hash is put again (s.cas.Put is idempotent)
// rather than skipped, since knowing it is already there costs the same read.
//
// The precondition is checked against the tip's HASH rather than resolved to a
// version row, because a revert means a hash can appear more than once and does
// not identify a point in the log.
func (s *Store) RegisterVersion(ctx context.Context, id domain.PlanID, r client.VersionRegistration) (domain.VersionRef, error) {
	var ref domain.VersionRef
	err := s.withLock(func() error {
		if !s.findPlan(id) {
			return fmt.Errorf("plan %s: %w", id, client.ErrNotFound)
		}
		hash := domain.HashContent(r.Content)
		versions := s.st.Versions[id]
		var tip domain.ContentHash
		if len(versions) > 0 {
			tip = versions[len(versions)-1].Hash
		}
		if hash == tip {
			ref = domain.VersionRef{Hash: tip, Seq: len(versions)}
			return nil
		}
		if r.Base != tip {
			// Seq mirrors Versions' own derivation below: the tip's position
			// in this log, 1-based, 0 for a plan with no versions yet.
			return &client.ConflictError{Plan: id, Tip: domain.VersionRef{Hash: tip, Seq: len(versions)}}
		}
		if _, err := s.cas.Put(ctx, r.Content); err != nil {
			return err
		}
		// r's Seq (there isn't one -- client.VersionRegistration carries no
		// VersionRef) is not needed here: Versions derives every entry's Seq
		// from its position in the log when it is read, not from anything
		// recorded at append time.
		stamped, err := s.stamp(ctx, domain.Version{VersionRef: domain.VersionRef{Hash: hash}})
		if err != nil {
			return err
		}
		s.st.Versions[id] = append(versions, stamped)
		if err := s.save(); err != nil {
			return err
		}
		ref = domain.VersionRef{Hash: hash, Seq: len(versions) + 1}
		return nil
	})
	if err != nil {
		return domain.VersionRef{}, err
	}
	return ref, nil
}

// Versions derives every returned entry's Seq from its 1-based position in this
// plan's log rather than trusting any Seq already sitting on the stored value:
// the log is append-only under a single flock, so position IS the Seq this
// Store assigns it. Deriving it here, in the one place versions leave the
// Store, also means a state.json written before Seq existed decodes its old
// entries with a correct Seq for free.
func (s *Store) Versions(_ context.Context, id domain.PlanID) ([]domain.Version, error) {
	var out []domain.Version
	err := s.withLock(func() error {
		out = make([]domain.Version, len(s.st.Versions[id]))
		copy(out, s.st.Versions[id])
		for i := range out {
			out[i].Seq = i + 1
		}
		return nil
	})
	return out, err
}

// Content reads one version's bytes out of s.cas.
//
// A hash id's own log never registered is client.ErrNotFound, checked against
// s.st.Versions[id] rather than left to s.cas.Get alone: content is
// content-addressed and s.cas is a single flat store shared by every plan this
// machine holds, so the same bytes can legitimately sit under a hash two
// different plans both reference. Content answers for THIS plan's log, not for
// the CAS's global keyspace.
func (s *Store) Content(ctx context.Context, id domain.PlanID, h domain.ContentHash) ([]byte, error) {
	var found bool
	if err := s.withLock(func() error {
		for _, v := range s.st.Versions[id] {
			if v.Hash == h {
				found = true
				break
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("plan %s hash %s: %w", id, h.Short(), client.ErrNotFound)
	}
	return s.cas.Get(ctx, h)
}

func (s *Store) Threads(_ context.Context, id domain.PlanID) ([]domain.Thread, error) {
	var out []domain.Thread
	err := s.withLock(func() error {
		for _, t := range s.st.Threads {
			if t.Plan == id {
				out = append(out, t)
			}
		}
		return nil
	})
	return out, err
}

func (s *Store) CreateThread(ctx context.Context, id domain.PlanID, h domain.ContentHash, a reanchor.Anchor, body string) (domain.Thread, error) {
	var thread domain.Thread
	err := s.withLock(func() error {
		if !s.findPlan(id) {
			return fmt.Errorf("plan %s: %w", id, client.ErrNotFound)
		}
		attribution, err := client.AttributionFrom(ctx)
		if err != nil {
			return err
		}
		thread = domain.Thread{
			ID:         domain.ThreadID(newID()),
			Plan:       id,
			Anchor:     a,
			AnchorHash: h,
			Comments: []domain.Comment{{
				ID:          domain.CommentID(newID()),
				Attribution: attribution,
				Body:        body,
				CreatedAt:   time.Now().UTC(),
			}},
		}
		s.st.Threads = append(s.st.Threads, thread)
		return s.save()
	})
	if err != nil {
		return domain.Thread{}, err
	}
	return thread, nil
}

// findThread resolves tid WITHIN id: a thread whose Plan is some other plan
// answers the identical client.ErrNotFound a thread that does not exist at all
// does, per client.PlanService's contract for the four thread-keyed writes. The
// plan scoping lives here, in the one lookup all four share, rather than in each
// of them: a thread id from another plan must not be reachable by forgetting a
// step, and on DeleteThread forgetting it would DESTROY the other plan's thread.
//
// An unknown PLAN id lands on the same answer without a separate branch: nothing
// in s.st.Threads can name it, so the loop simply finds nothing. That is the
// contract, not a happy accident -- the three cases are deliberately
// indistinguishable (see client.PlanService.Reply).
func (s *Store) findThread(id domain.PlanID, tid domain.ThreadID) (*domain.Thread, error) {
	for i := range s.st.Threads {
		if s.st.Threads[i].ID == tid && s.st.Threads[i].Plan == id {
			return &s.st.Threads[i], nil
		}
	}
	return nil, fmt.Errorf("thread %s: %w", tid, client.ErrNotFound)
}

func (s *Store) Reply(ctx context.Context, id domain.PlanID, tid domain.ThreadID, body string) (domain.Comment, error) {
	var comment domain.Comment
	err := s.withLock(func() error {
		thread, err := s.findThread(id, tid)
		if err != nil {
			return err
		}
		attribution, err := client.AttributionFrom(ctx)
		if err != nil {
			return err
		}
		comment = domain.Comment{
			ID:          domain.CommentID(newID()),
			Attribution: attribution,
			Body:        body,
			CreatedAt:   time.Now().UTC(),
		}
		thread.Comments = append(thread.Comments, comment)
		return s.save()
	})
	if err != nil {
		return domain.Comment{}, err
	}
	return comment, nil
}

func (s *Store) SetResolved(_ context.Context, id domain.PlanID, tid domain.ThreadID, resolved bool) error {
	return s.withLock(func() error {
		thread, err := s.findThread(id, tid)
		if err != nil {
			return err
		}
		thread.Resolved = resolved
		return s.save()
	})
}

func (s *Store) Rehome(_ context.Context, id domain.PlanID, tid domain.ThreadID, h domain.ContentHash, a reanchor.Anchor) error {
	return s.withLock(func() error {
		thread, err := s.findThread(id, tid)
		if err != nil {
			return err
		}
		thread.AnchorHash = h
		thread.Anchor = a
		return s.save()
	})
}

// DeleteThread removes tid from id, and with it every comment under it.
//
// THERE IS NO CASCADE, and not because a step was forgotten: a comment lives ON
// its thread here (domain.Thread.Comments), so dropping the thread drops the
// conversation with it and there is no second collection to keep in step.
//
// findThread is called for its ERROR rather than for the pointer it returns: an
// unknown thread, an unknown plan, and a thread that really exists on some OTHER
// plan all answer the identical client.ErrNotFound, from the one lookup that
// carries the plan scoping. The filter below repeats that same (plan, thread)
// pair rather than matching on tid alone, because a thread of another plan must
// not be reachable by forgetting a step, and here forgetting it would destroy it.
//
// CAS exhibits are untouched, exactly as DeletePlan's own comment says for the
// wider gesture: a thread's AnchorHash names the plan's own reviewed bytes,
// which its version log still references.
func (s *Store) DeleteThread(_ context.Context, id domain.PlanID, tid domain.ThreadID) error {
	return s.withLock(func() error {
		if _, err := s.findThread(id, tid); err != nil {
			return err
		}
		threads := s.st.Threads[:0:0]
		for _, t := range s.st.Threads {
			if t.ID != tid || t.Plan != id {
				threads = append(threads, t)
			}
		}
		s.st.Threads = threads
		return s.save()
	})
}

// Approve is idempotent on (plan, hash, actor). A repeat approval of content
// this exact actor already approved is a no-op success that leaves the
// ORIGINAL fact -- and its agent -- untouched, even when the retry names a
// different agent: "first writer wins" for a retried write. Unlike
// RegisterVersion's version log, which must NEVER carry a unique constraint on
// (plan, hash) because a revert legitimately re-registers an earlier hash, an
// approval has no revert concept, so deduplicating here loses nothing.
func (s *Store) Approve(ctx context.Context, id domain.PlanID, h domain.ContentHash) error {
	return s.withLock(func() error {
		if !s.findPlan(id) {
			return fmt.Errorf("plan %s: %w", id, client.ErrNotFound)
		}
		attribution, err := client.AttributionFrom(ctx)
		if err != nil {
			return err
		}
		for _, a := range s.st.Approvals {
			if a.Plan == id && a.Hash == h && a.Attribution.ActorID == attribution.ActorID {
				return nil
			}
		}
		s.st.Approvals = append(s.st.Approvals, domain.Approval{
			Plan:        id,
			Hash:        h,
			Attribution: attribution,
			CreatedAt:   time.Now().UTC(),
		})
		return s.save()
	})
}

func (s *Store) Approvals(_ context.Context, id domain.PlanID) ([]domain.Approval, error) {
	var out []domain.Approval
	err := s.withLock(func() error {
		for _, a := range s.st.Approvals {
			if a.Plan == id {
				out = append(out, a)
			}
		}
		return nil
	})
	return out, err
}

// RenamePlan sets a plan's title. Empty (or whitespace-only) titles are
// rejected before the lock is taken.
func (s *Store) RenamePlan(_ context.Context, id domain.PlanID, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("plan title must not be empty")
	}
	return s.withLock(func() error {
		for i := range s.st.Plans {
			if s.st.Plans[i].ID == id {
				s.st.Plans[i].Title = title
				return s.save()
			}
		}
		return fmt.Errorf("plan %s: %w", id, client.ErrNotFound)
	})
}

// SetSourceHint points a plan at a different file, or -- for the empty hint --
// releases it from the one it names.
//
// ONE FUNCTION AND NOT TWO, WITH THE EMPTY STRING MEANING RELEASE. The
// missing-file panel's f sets a new path and its o clears the one it has, and
// those are one question -- "which file does this plan follow" -- asked with two
// answers. Two named methods would be two answers to what a re-point may write.
//
// THE ONLY FIELD IT TOUCHES IS SourceHint: a plan whose file moved has lost
// nothing but its path.
//
// NOT TRIMMED AND NOT ABSOLUTIZED, unlike RenamePlan's title just above: this
// field is matched by EXACT STRING against what a door resolved a review target
// to (ResolvePlan, and cmd/draftplane's localReviewFile and mcptools'
// canonicalSource, which both filepath.Abs first), so canonicalization is the
// door's job and doing it a second time here would put a second, quieter rule in
// front of the one that decides identity. A door that hands this a relative path
// files a plan nothing can resolve; that is the door's defect and it is visible,
// which a silent repair here would not be.
//
// An unknown id is client.ErrNotFound, wrapped exactly as RenamePlan wraps its
// own.
func (s *Store) SetSourceHint(_ context.Context, id domain.PlanID, hint string) error {
	return s.withLock(func() error {
		for i := range s.st.Plans {
			if s.st.Plans[i].ID == id {
				s.st.Plans[i].SourceHint = hint
				return s.save()
			}
		}
		return fmt.Errorf("plan %s: %w", id, client.ErrNotFound)
	})
}

// DeletePlan removes a plan and all its review facts (versions, threads,
// approvals). CAS exhibits are deliberately untouched: content is
// content-addressed and possibly shared across plans, and deleting review
// facts must never destroy the reviewed bytes.
func (s *Store) DeletePlan(_ context.Context, id domain.PlanID) error {
	return s.withLock(func() error {
		if !s.findPlan(id) {
			return fmt.Errorf("plan %s: %w", id, client.ErrNotFound)
		}
		plans := s.st.Plans[:0:0]
		for _, p := range s.st.Plans {
			if p.ID != id {
				plans = append(plans, p)
			}
		}
		s.st.Plans = plans
		delete(s.st.Versions, id)
		threads := s.st.Threads[:0:0]
		for _, t := range s.st.Threads {
			if t.Plan != id {
				threads = append(threads, t)
			}
		}
		s.st.Threads = threads
		approvals := s.st.Approvals[:0:0]
		for _, a := range s.st.Approvals {
			if a.Plan != id {
				approvals = append(approvals, a)
			}
		}
		s.st.Approvals = approvals
		return s.save()
	})
}

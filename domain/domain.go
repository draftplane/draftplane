// Package domain holds Draftplane's data model: plans, versions,
// threads, and approvals, all keyed by content hash. Content hashes are
// addressing, never UX — no user is ever asked to type or select one.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/draftplane/draftplane/reanchor"
)

type PlanID string
type ThreadID string
type CommentID string

func (t ThreadID) String() string { return string(t) }

// ContentHash is the hex SHA-256 of a document's raw bytes — plain content
// hashing, deliberately not git's blob format.
type ContentHash string

func HashContent(content []byte) ContentHash {
	sum := sha256.Sum256(content)
	return ContentHash(hex.EncodeToString(sum[:]))
}

// Short returns the informational display prefix of a hash (status lines
// only — never an input affordance).
func (h ContentHash) Short() string {
	if len(h) > 8 {
		return string(h[:8])
	}
	return string(h)
}

// Plan is a reviewable document: an identity plus, below the blank line,
// every fact the store computes from its review history.
//
// This type carries no JSON tags of its own, so client/localfs persists every
// exported field into state.json under its bare Go name — a rename silently
// orphans data on disk, and a new untagged field is silently persisted. Every
// field below the blank line is `json:"-"`; see OpenThreads.
type Plan struct {
	ID         PlanID
	Title      string
	SourceHint string // discovery hint (absolute path, or a scheme URI like notion://…), not identity
	RepoHint   string // optional; set only when the file lives in a repo

	// LastActivityAt is the sort key for the plan list, zero for a plan with no
	// facts at all: the max of version RegisteredAt, comment CreatedAt and
	// approval CreatedAt, so resolving or rehoming does NOT move it —
	// domain.Thread carries no timestamp for either event.
	LastActivityAt time.Time `json:"-"`

	// CreatedAt is the sort key behind the `created` column: the first element
	// of the plan's version log. A plan whose version log is empty reads zero,
	// and the zero is not neutral: it sorts last descending and first
	// ascending.
	CreatedAt time.Time `json:"-"`

	// OpenThreads counts this plan's unresolved threads, TotalThreads all of
	// them, and TipApproved reports whether any approval's Hash equals the
	// latest registered version's Hash.
	//
	// These and every other field below Plan's blank line are `json:"-"`, and
	// must stay so. Each is derivable from facts client/localfs already holds
	// and recomputes on every read; persisting one makes a second, stale source
	// of truth the instant a later write changes the fact it came from.
	OpenThreads  int  `json:"-"`
	TotalThreads int  `json:"-"`
	TipApproved  bool `json:"-"`
}

// Attribution is who made a review fact: an optional recorded author identity
// and, when an agent made it, the identity that agent was issued.
//
// ActorID, ActorLogin and ActorDisplay are that recorded author identity. Each
// is optional, and nothing in this build writes them. All three empty and
// Agent empty means exactly one thing — written by the human on this machine —
// and is what the machine pseudonym renders in place of.
//
// ActorLogin is what renders when it is set (ui.FormatAttribution's chain is
// ActorLogin then ActorID, with no step onto ActorDisplay). ActorDisplay is a
// display name and must not be repurposed to carry a login.
//
// These fields carry no json tag: an attribution is a persisted fact, written
// into state.json under each field's bare Go name. A fact written before a
// field existed decodes it as "".
type Attribution struct {
	ActorID      string
	ActorLogin   string
	ActorDisplay string
	Agent        string // "blue-parakeet-f9"; empty for a human write
}

// VersionRef names one point in a plan's version log: the content and the
// store's sequence number for it, embedded together in Version so that
// updating one and forgetting the other is not representable.
//
// Seq is an ordering/dedup guard only. It must never become a precondition —
// RegisterVersion's CAS is keyed on Hash. A revert re-registers an earlier
// hash by appending a new entry, so the same hash can legitimately occupy two
// different seqs: a seq does not identify a point in the version DAG, only a
// hash does.
type VersionRef struct {
	Hash ContentHash
	Seq  int
}

// Version is a registered snapshot of the plan. Attribution/RegisteredAt are
// stamped by the PlanService implementation, not the caller.
//
// Attribution records who REGISTERED the version, not who wrote the content.
// Registration is lazy, so an agent can change a file and a human's next
// comment or approval is what lands the new version, attributing it to the
// human. Do not build a blame or authorship view on this field.
//
// There is no ancestry field: history is linear by decision, and the store
// assigns each version's predecessor from the tip it holds.
type Version struct {
	VersionRef
	Attribution  Attribution
	RegisteredAt time.Time
}

type Comment struct {
	ID          CommentID
	Attribution Attribution
	Body        string
	CreatedAt   time.Time
}

type Thread struct {
	ID         ThreadID
	Plan       PlanID
	Anchor     reanchor.Anchor // heading path + normalized span
	AnchorHash ContentHash     // version the anchor was created or last rehomed against
	Resolved   bool
	Comments   []Comment
}

type Approval struct {
	Plan        PlanID
	Hash        ContentHash
	Attribution Attribution
	CreatedAt   time.Time
}

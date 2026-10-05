// Package placement is where a review thread lands on a document -- and,
// with it, the ONE definition of "orphaned" this codebase has.
//
// It must stay a LEAF, importing domain and reanchor and nothing else: that is
// what lets every package reach the definition instead of forking its own
// reanchor.Reanchor call plus a status check. Anything below session in the
// import graph could not reach it otherwise.
//
// reanchor.Reanchor has exactly ONE production call site, inside PlaceThreads
// below, and every function here that needs a placement calls PlaceThreads
// rather than re-deriving one.
package placement

import (
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/reanchor"
)

// Placement is where (and how confidently) a thread lands on the current
// version of the document.
type Placement struct {
	Thread      domain.Thread
	Status      reanchor.Status
	Anchor      reanchor.Anchor
	Confidence  float64
	MatchedText string // the matched text in the current doc; empty when orphaned

	// MatchStart and MatchEnd are the byte offsets of the matched window in the
	// document PlaceThreads was given: wherever they are reported,
	// reanchor.Normalize(doc[MatchStart:MatchEnd]) is MatchedText. Every arm
	// that located a span reports them -- fuzzy, exact and moved alike.
	//
	// A placement that located no span carries 0..0, and there are exactly two:
	// an orphan, and a SECTION anchor, whose empty Span is located by heading
	// path alone. Status does not separate those from the rest -- a section
	// anchor comes back StatusExact or StatusMoved like any other placement.
	//
	// ZERO IS A LEGAL OFFSET, so a consumer decides whether it was handed a
	// location by testing that the range is non-empty (MatchEnd > MatchStart),
	// never that either end is zero. A window at the top of a document starts at
	// 0, and a located span can never be empty because reanchor.CreateAnchor
	// refuses a span under three words.
	MatchStart int
	MatchEnd   int

	Candidates []reanchor.Candidate
}

// PlaceThreads re-anchors threads onto doc and classifies each one Exact,
// Moved, Fuzzy, or Orphaned — the ONE definition of "orphaned" this codebase
// has. session.Placements wraps it. A caller writing its own
// reanchor.Reanchor + status check instead is the fork this exists to prevent.
func PlaceThreads(threads []domain.Thread, doc string) []Placement {
	placements := make([]Placement, 0, len(threads))
	for _, t := range threads {
		r := reanchor.Reanchor(t.Anchor, doc, nil)
		p := Placement{Thread: t, Status: r.Status}
		if r.Status == reanchor.StatusOrphaned {
			p.Candidates = r.Candidates
		} else {
			p.Anchor = r.Anchor
			p.Confidence = r.Confidence
			p.MatchedText = r.MatchedText
			p.MatchStart = r.MatchStart
			p.MatchEnd = r.MatchEnd
		}
		placements = append(placements, p)
	}
	return placements
}

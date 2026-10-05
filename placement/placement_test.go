package placement_test

import (
	"testing"

	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/placement"
	"github.com/draftplane/draftplane/reanchor"
)

// TestPlaceThreadsCarriesTheMatchedWindow pins Placement.MatchStart/MatchEnd
// against reanchor.Result's own contract rather than against a remembered
// pair of numbers: wherever a placement located a SPAN the offsets bracket
// the matched window, so reanchor.Normalize(doc[MatchStart:MatchEnd]) is
// MatchedText.
//
// The zero half is the section row and the orphan, the set that genuinely has
// no position. The section row is here because STATUS cannot tell it from the
// exact row above it: both come back StatusExact, and only the empty range
// separates them -- which is why nothing may read "no location" off either end
// being zero.
func TestPlaceThreadsCarriesTheMatchedWindow(t *testing.T) {
	const doc = "# Plan\n\n## Context\n\nRequests are currently unbounded and cause problems under load.\n\n## Design\n\nWe will use a token bucket with a burst capacity of fifty requests per second.\n"

	for _, tc := range []struct {
		name   string
		anchor reanchor.Anchor
		want   reanchor.Status
	}{
		{
			name:   "exact",
			anchor: reanchor.Anchor{HeadingPath: []string{"Plan", "Context"}, Span: "Requests are currently unbounded and cause problems under load."},
			want:   reanchor.StatusExact,
		},
		{
			name:   "moved",
			anchor: reanchor.Anchor{HeadingPath: []string{"Plan", "Background"}, Span: "Requests are currently unbounded and cause problems under load."},
			want:   reanchor.StatusMoved,
		},
		{
			name:   "fuzzy",
			anchor: reanchor.Anchor{HeadingPath: []string{"Plan", "Design"}, Span: "We will use a token bucket with a burst capacity of sixty requests per second."},
			want:   reanchor.StatusFuzzy,
		},
		{
			name:   "orphaned",
			anchor: reanchor.Anchor{HeadingPath: []string{"Plan", "Gone"}, Span: "nothing in this sentence appears anywhere in the document it is looking at"},
			want:   reanchor.StatusOrphaned,
		},
		{
			// A section anchor: StatusExact, like the first row, and no
			// window at all. Nothing but the empty range tells them apart.
			name:   "section anchor",
			anchor: reanchor.Anchor{HeadingPath: []string{"Plan", "Design"}},
			want:   reanchor.StatusExact,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := placement.PlaceThreads([]domain.Thread{{ID: "l_t1", Anchor: tc.anchor}}, doc)[0]
			if p.Status != tc.want {
				t.Fatalf("status = %s, want %s -- the case no longer drives the arm it names", p.Status, tc.want)
			}
			if tc.want == reanchor.StatusOrphaned || tc.anchor.Span == "" {
				if p.MatchStart != 0 || p.MatchEnd != 0 {
					t.Fatalf("a %s placement over span %q carries offsets %d..%d; a placement that located no span locates no window",
						tc.want, tc.anchor.Span, p.MatchStart, p.MatchEnd)
				}
				return
			}
			if p.MatchStart < 0 || p.MatchEnd > len(doc) || p.MatchStart >= p.MatchEnd {
				t.Fatalf("offsets %d..%d are not a window into a %d-byte document", p.MatchStart, p.MatchEnd, len(doc))
			}
			if got := reanchor.Normalize(doc[p.MatchStart:p.MatchEnd]); got != p.MatchedText {
				t.Fatalf("Normalize(doc[%d:%d]) = %q, want MatchedText %q", p.MatchStart, p.MatchEnd, got, p.MatchedText)
			}
		})
	}
}

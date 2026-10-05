package reanchor

// Anchor locates a comment inside a specific version (blob) of a markdown
// document. The blob hash lives on the comment record, not here — an anchor
// is only meaningful paired with the exact content it was created against.
// Span is stored whitespace-normalized (see Normalize). An empty Span marks a
// section anchor: a comment on the section itself, located by heading path
// alone (see CreateSectionAnchor).
type Anchor struct {
	HeadingPath []string
	Span        string
}

type Status string

const (
	StatusExact    Status = "exact"
	StatusMoved    Status = "moved"
	StatusFuzzy    Status = "fuzzy"
	StatusOrphaned Status = "orphaned"
)

// Candidate is a near-miss returned with an orphaned result, for an agent
// (or human) to adjudicate. Score is span similarity for fuzzy-tier orphans
// and heading-path similarity for exact-tier orphans (ambiguous or
// uncorroborated exact hits) — comparable within one candidate list, not
// across orphan kinds.
type Candidate struct {
	HeadingPath []string
	Text        string
	Score       float64
}

// Result is the outcome of re-anchoring. Anchor, Confidence, and MatchedText
// are meaningful when Status != StatusOrphaned; Candidates is populated only
// when orphaned.
//
// MatchStart and MatchEnd are the byte offsets of the matched window in the
// document Reanchor was given, and where they are reported at all,
// Normalize(newDoc[MatchStart:MatchEnd]) is MatchedText. Every arm that located
// a SPAN reports them, fuzzy, exact and moved alike.
//
// A result that located no span leaves them 0..0, and there are exactly two: an
// orphan, and a SECTION anchor, whose empty Span is located by heading path
// alone. Status does not separate those from the rest — a section anchor comes
// back StatusExact or StatusMoved like any other — so the offsets themselves
// have to be the discriminator.
//
// ZERO IS A LEGAL OFFSET, so the test is that the range is NON-EMPTY (MatchEnd
// > MatchStart) and never that either end is zero: a window at the very top of
// a document starts at 0, while a located span can never be empty, since
// CreateAnchor refuses a span under three words.
//
// The Normalize identity presupposes a normalized Anchor.Span, as Anchor's own
// doc requires. Hand Reanchor a span with one leading or trailing space and the
// exact arm still accepts it, but MatchedText carries that space and the
// identity fails. Every production door normalizes before an Anchor exists, so
// this is a precondition on hand-built anchors.
//
// Confidence is a similarity score, not a calibrated probability. Bigram
// similarity rates sentences differing by one content word around 0.9, so
// when the true target was deleted and a near-twin survives with no
// competitor, this tier attaches confidently and wrongly — that failure mode
// is deferred to the agent-fallback tier.
type Result struct {
	Status      Status
	Anchor      Anchor
	Confidence  float64
	MatchedText string
	MatchStart  int
	MatchEnd    int
	Candidates  []Candidate
}

// Options tune the precision gates. DefaultOptions are the thresholds the
// golden corpus was generated with (see CORPUS.md).
type Options struct {
	// MinScore is the minimum span similarity to accept a fuzzy match.
	MinScore float64
	// MinMargin is the required lead over the runner-up at a different
	// location, as a fraction of the best score.
	MinMargin float64
	// ExactAmbiguityDelta is the heading-similarity delta under which
	// duplicate exact hits are ambiguous.
	ExactAmbiguityDelta float64
	// ShortSpanChars: normalized spans shorter than this (in runes) need
	// heading corroboration.
	ShortSpanChars int
	// ShortSpanHeadingSim is the minimum heading similarity for a short
	// span's hit to be trusted.
	ShortSpanHeadingSim float64
	// SectionMinSim is the minimum heading-path similarity for a section
	// anchor to relocate.
	SectionMinSim float64
}

var DefaultOptions = Options{
	MinScore:            0.65,
	MinMargin:           0.05,
	ExactAmbiguityDelta: 0.05,
	ShortSpanChars:      25,
	ShortSpanHeadingSim: 0.35,
	SectionMinSim:       0.5,
}

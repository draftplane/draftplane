package reanchor

import (
	"strings"
	"testing"
)

const doc = "# Payments Plan\n\nIntro paragraph about the payments system.\n\n## Context\n\nWe currently process refunds by hand, which does not scale\npast a handful of customers per week.\n\n## Design decisions\n\n1. Use Stripe as the payment processor because of its API quality.\n2. Store only opaque customer tokens, never card numbers.\n\n## Rollout\n\nShip behind a feature flag to internal users first.\n\n```bash\n# this is a comment inside a fence, not a heading\nflag enable payments\n```\n\n## Verification\n\nRefund a test charge end to end in staging.\n"

func mustAnchor(t *testing.T, d, span string, hint []string) Anchor {
	t.Helper()
	a, err := CreateAnchor(d, span, hint)
	if err != nil {
		t.Fatalf("CreateAnchor(%q): %v", span, err)
	}
	return a
}

func anchorTo(t *testing.T, span string) Anchor {
	t.Helper()
	return mustAnchor(t, doc, span, nil)
}

func TestReanchor(t *testing.T) {
	cases := []struct {
		name       string
		span       string
		transform  func(string) string
		wantStatus Status
		wantPath   string // "/"-joined; "" = don't check
	}{
		{
			"unchanged document is exact",
			"Ship behind a feature flag",
			func(d string) string { return d },
			StatusExact, "Payments Plan/Rollout",
		},
		{
			"line rewrapping is exact",
			"by hand, which does not scale past a handful",
			func(d string) string {
				return strings.Replace(d,
					"process refunds by hand, which does not scale\npast a handful of customers per week.",
					"process refunds by hand,\nwhich does not scale past a handful\nof customers per week.", 1)
			},
			StatusExact, "",
		},
		{
			"intact span under renamed heading is moved",
			"Ship behind a feature flag",
			func(d string) string { return strings.Replace(d, "## Rollout", "## Launch strategy", 1) },
			StatusMoved, "Payments Plan/Launch strategy",
		},
		{
			"reworded span is fuzzy in the same section",
			"Store only opaque customer tokens, never card numbers.",
			func(d string) string {
				return strings.Replace(d,
					"Store only opaque customer tokens, never card numbers.",
					"Persist opaque tokens for each customer and never store raw card numbers.", 1)
			},
			StatusFuzzy, "Payments Plan/Design decisions",
		},
		{
			"deleted span orphans",
			"Refund a test charge end to end in staging.",
			func(d string) string {
				return strings.Replace(d, "Refund a test charge end to end in staging.\n", "", 1)
			},
			StatusOrphaned, "",
		},
		{
			"weak lookalike does not attach",
			"Refund a test charge end to end in staging.",
			func(d string) string {
				d = strings.Replace(d, "Refund a test charge end to end in staging.", "Confirm the dashboard loads.", 1)
				return strings.Replace(d, "## Verification", "## Checks", 1)
			},
			StatusOrphaned, "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := Reanchor(anchorTo(t, tc.span), tc.transform(doc), nil)
			if result.Status != tc.wantStatus {
				t.Fatalf("status = %s, want %s (result: %+v)", result.Status, tc.wantStatus, result)
			}
			if tc.wantPath != "" && strings.Join(result.Anchor.HeadingPath, "/") != tc.wantPath {
				t.Fatalf("path = %v, want %s", result.Anchor.HeadingPath, tc.wantPath)
			}
			if tc.wantStatus == StatusOrphaned && len(result.Candidates) == 0 {
				t.Fatal("orphan must carry candidates")
			}
		})
	}
}

func TestReanchorExactConfidences(t *testing.T) {
	result := Reanchor(anchorTo(t, "Ship behind a feature flag"), doc, nil)
	if result.Confidence != 1 {
		t.Fatalf("unchanged exact confidence = %v, want 1", result.Confidence)
	}
	renamed := strings.Replace(doc, "## Rollout", "## Launch strategy", 1)
	if r := Reanchor(anchorTo(t, "Ship behind a feature flag"), renamed, nil); r.Confidence != 0.95 {
		t.Fatalf("moved confidence = %v, want 0.95", r.Confidence)
	}
}

func TestReanchorDuplicateDisambiguation(t *testing.T) {
	dup := "# T\n\n## Alpha\n\nThe retry limit is three attempts.\n\n## Beta\n\nThe retry limit is three attempts.\n"
	anchor := mustAnchor(t, dup, "retry limit is three attempts", []string{"T", "Beta"})
	shuffled := "# T\n\n## Beta\n\nThe retry limit is three attempts.\n\n## Alpha\n\nThe retry limit is three attempts.\n"
	result := Reanchor(anchor, shuffled, nil)
	if result.Status != StatusExact || strings.Join(result.Anchor.HeadingPath, "/") != "T/Beta" {
		t.Fatalf("result = %+v, want exact at T/Beta", result)
	}
}

func TestReanchorPrecisionTraps(t *testing.T) {
	t.Run("two surviving near-duplicates orphan regardless of score", func(t *testing.T) {
		before := "# T\n\n## Alpha\n\nThe exporter retries the upload three times before giving up on the batch.\n"
		after := "# T\n\n## Gamma\n\nThe exporter retries the upload three times before abandoning the batch.\n\n## Delta\n\nThe exporter retries the upload three times before abandoning the batch.\n"
		anchor := mustAnchor(t, before, "The exporter retries the upload three times before giving up on the batch.", nil)
		result := Reanchor(anchor, after, nil)
		if result.Status != StatusOrphaned {
			t.Fatalf("status = %s, want orphaned (result: %+v)", result.Status, result)
		}
		if len(result.Candidates) < 2 {
			t.Fatalf("want >=2 candidates, got %d", len(result.Candidates))
		}
	})

	t.Run("short generic span needs heading corroboration in both tiers", func(t *testing.T) {
		before := "# T\n\n## Error handling\n\nThe parser returns an error code when the payload is malformed.\n"
		anchor := mustAnchor(t, before, "returns an error code", nil)
		exactElsewhere := "# T\n\n## Logging\n\nThe logger returns an error code when the disk is full.\n"
		if r := Reanchor(anchor, exactElsewhere, nil); r.Status != StatusOrphaned {
			t.Fatalf("exact tier: status = %s, want orphaned", r.Status)
		}
		inexactElsewhere := "# T\n\n## Logging\n\nThe logger returns error codes when the disk is full.\n"
		if r := Reanchor(anchor, inexactElsewhere, nil); r.Status != StatusOrphaned {
			t.Fatalf("fuzzy tier: status = %s, want orphaned", r.Status)
		}
		if r := Reanchor(anchor, before, nil); r.Status != StatusExact {
			t.Fatalf("same doc: status = %s, want exact", r.Status)
		}
	})

	t.Run("same-path duplicate does not mask a rival location", func(t *testing.T) {
		before := "# T\n\n## Alpha\n\nThe quota is reset at midnight UTC every day.\n"
		anchor := mustAnchor(t, before, "quota is reset at midnight UTC", nil)
		after := "# T\n\n## Gamma\n\nThe quota is reset at midnight UTC every day.\n\nAgain: the quota is reset at midnight UTC for good measure.\n\n## Delta\n\nThe quota is reset at midnight UTC every day.\n"
		if r := Reanchor(anchor, after, nil); r.Status != StatusOrphaned {
			t.Fatalf("status = %s, want orphaned", r.Status)
		}
	})
}

func TestCreateAnchor(t *testing.T) {
	t.Run("resolves section and normalizes span", func(t *testing.T) {
		a := mustAnchor(t, doc, "refunds by hand, which does not scale\npast a handful", nil)
		if strings.Join(a.HeadingPath, "/") != "Payments Plan/Context" {
			t.Fatalf("path = %v", a.HeadingPath)
		}
		if a.Span != "refunds by hand, which does not scale past a handful" {
			t.Fatalf("span = %q", a.Span)
		}
	})

	t.Run("errors", func(t *testing.T) {
		cases := []struct {
			name, span string
			hint       []string
			wantErr    string
		}{
			{"not present", "no such text anywhere", nil, "not found"},
			{"too short", "feature flag", nil, "too short"},
			{"blank", "  ", nil, "too short"},
			{"hint path missing", "Refund a test charge end to end", []string{"Payments Plan", "Nope"}, "not found under heading path"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := CreateAnchor(doc, tc.span, tc.hint)
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
			})
		}
	})

	t.Run("requires hint for multi-section spans", func(t *testing.T) {
		dup := "# T\n\n## Alpha\n\nThe retry limit is three attempts.\n\n## Beta\n\nThe retry limit is three attempts.\n"
		if _, err := CreateAnchor(dup, "retry limit is three attempts", nil); err == nil || !strings.Contains(err.Error(), "headingHint") {
			t.Fatalf("err = %v, want headingHint guidance", err)
		}
		a := mustAnchor(t, dup, "retry limit is three attempts", []string{"T", "Beta"})
		if strings.Join(a.HeadingPath, "/") != "T/Beta" {
			t.Fatalf("path = %v", a.HeadingPath)
		}
	})
}

func TestCreateSectionAnchor(t *testing.T) {
	a, err := CreateSectionAnchor(doc, []string{"Payments Plan", "Rollout"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Span != "" || strings.Join(a.HeadingPath, "/") != "Payments Plan/Rollout" {
		t.Fatalf("anchor = %+v", a)
	}
	if _, err := CreateSectionAnchor(doc, []string{"Payments Plan", "Nope"}); err == nil {
		t.Fatal("want error for absent path")
	}
}

func TestReanchorSectionTier(t *testing.T) {
	anchor, err := CreateSectionAnchor(doc, []string{"Payments Plan", "Rollout"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		transform  func(string) string
		wantStatus Status
		wantLeaf   string // "" = don't check
	}{
		{"unchanged path is exact", func(d string) string { return d }, StatusExact, "Rollout"},
		{
			"lightly renamed heading is moved",
			func(d string) string { return strings.Replace(d, "## Rollout", "## Rollout phases", 1) },
			StatusMoved, "Rollout phases",
		},
		{
			"drastically renamed heading orphans with candidates",
			func(d string) string { return strings.Replace(d, "## Rollout", "## Ship qualification", 1) },
			StatusOrphaned, "",
		},
		{
			"deleted section orphans",
			func(d string) string {
				return strings.Replace(d, "## Rollout\n\nShip behind a feature flag to internal users first.\n\n```bash\n# this is a comment inside a fence, not a heading\nflag enable payments\n```\n\n", "", 1)
			},
			StatusOrphaned, "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := Reanchor(anchor, tc.transform(doc), nil)
			if result.Status != tc.wantStatus {
				t.Fatalf("status = %s, want %s (%+v)", result.Status, tc.wantStatus, result)
			}
			if tc.wantStatus == StatusOrphaned {
				if len(result.Candidates) == 0 {
					t.Fatal("orphan must carry candidates")
				}
				return
			}
			if result.Anchor.Span != "" {
				t.Fatal("section anchor must stay a section anchor")
			}
			if tc.wantLeaf != "" && result.MatchedText != tc.wantLeaf {
				t.Fatalf("MatchedText = %q, want %q", result.MatchedText, tc.wantLeaf)
			}
		})
	}
}

func TestReanchorSectionAmbiguityOrphans(t *testing.T) {
	// Two equally-similar renamed candidates must orphan, not guess.
	before := "# T\n\n## Alpha metrics\n\nbody a.\n"
	anchor, err := CreateSectionAnchor(before, []string{"T", "Alpha metrics"})
	if err != nil {
		t.Fatal(err)
	}
	after := "# T\n\n## Alpha metrics east\n\nbody a.\n\n## Alpha metrics west\n\nbody b.\n"
	result := Reanchor(anchor, after, nil)
	if result.Status != StatusOrphaned {
		t.Fatalf("status = %s, want orphaned", result.Status)
	}
	if len(result.Candidates) < 2 {
		t.Fatalf("want both candidates, got %d", len(result.Candidates))
	}
}

// TestLargeSpanReanchorPin pins every visible field of the largespan case,
// which the golden corpus cannot reach: its longest span is 97 runes, and this
// one is 300+, where windowSizes(60) is nine widths over thousands of starts. A
// change that altered an answer only long spans can reach would leave the
// golden corpus green.
//
// The anchor comes from largeSpanAnchor rather than a second builder, so this
// and BenchmarkReanchor measure the same passage by construction and its
// non-vacuity guards cover this test too.
//
// Equality is exact rather than tolerant: a confidence that moved in its last
// bit is a finding, not noise.
func TestLargeSpanReanchorPin(t *testing.T) {
	original, rewritten, _, _ := loadCorpus(t)
	got := Reanchor(largeSpanAnchor(t, original, rewritten), rewritten, nil)

	wantPath := []string{"Execution plan", "Phase 1 — Schema", "Sensors, channels, and placement"}
	const wantMatched = `id uuid default gen_random_uuid() primary key, station_id uuid not null references stations (id), model text not null, -- vendor model string, e.g. 'HMP155' serial text not null, -- from the label on the housing installed_at timestamptz not null, removed_at timestamptz, -- null while mounted created_at timestamptz default now() not null, updated_at timestamptz default now() not null ); create table channels (`
	const wantConfidence = 0.9328449328449329

	if got.Status != StatusFuzzy {
		t.Errorf("status = %s, want %s", got.Status, StatusFuzzy)
	}
	if !pathsEqual(got.Anchor.HeadingPath, wantPath) {
		t.Errorf("path = %v, want %v", got.Anchor.HeadingPath, wantPath)
	}
	if got.Confidence != wantConfidence {
		t.Errorf("confidence = %v, want %v", got.Confidence, wantConfidence)
	}
	if got.MatchedText != wantMatched {
		t.Errorf("matchedText = %q, want %q", got.MatchedText, wantMatched)
	}
	if got.Anchor.Span != got.MatchedText {
		t.Errorf("anchor span %q differs from matched text %q", got.Anchor.Span, got.MatchedText)
	}
}

// TestWindowSliceEqualsJoin asserts the identity fuzzySearch's window
// construction rests on: for content that came from Normalize, slicing at the
// token offsets reproduces strings.Join(tokens[start:start+size], " ") byte for
// byte. It calls the production tokenOffsets so what it compares is the code
// fuzzySearch runs, not a second copy that could drift.
//
// The sample size is asserted because the failure mode here is going vacuous.
// It is also the guard on Normalize: if Normalize stopped collapsing runs of
// whitespace, or started leaving a leading or trailing space, this reddens
// rather than silently mis-slicing a window in production.
func TestWindowSliceEqualsJoin(t *testing.T) {
	original, rewritten, _, _ := loadCorpus(t)
	sizes := []int{1, 2, 3, 5, 8, 13, 21, 34, 55}
	windows := 0
	for _, doc := range []string{original, rewritten} {
		for _, s := range ParseSections(doc) {
			norm := Normalize(s.Content)
			if norm == "" {
				continue
			}
			tokens := strings.Split(norm, " ")
			offsets := tokenOffsets(tokens)
			for _, size := range sizes {
				for start := 0; start+size <= len(tokens); start++ {
					got := norm[offsets[start] : offsets[start+size]-1]
					want := strings.Join(tokens[start:start+size], " ")
					if got != want {
						t.Fatalf("section %v window [%d,%d): slice = %q, join = %q",
							s.HeadingPath, start, start+size, got, want)
					}
					windows++
				}
			}
		}
	}
	const minWindows = 50000
	if windows < minWindows {
		t.Fatalf("compared %d windows, want at least %d -- the sample went vacuous", windows, minWindows)
	}
	t.Logf("%d windows compared across both fixtures", windows)
}

// checkResultOffsets asserts Result's whole offset contract against the
// document the result came from, at BOTH ends of the range. A range checked
// only at its start reads as verified and is not: an end that ran long, or
// short by a token, would satisfy every one-sided check and still draw the
// wrong region.
//
// The airtight form is Normalize(doc[MatchStart:MatchEnd]) == MatchedText.
// It has to be Normalize on the left because the two are in different
// coordinate systems by construction — MatchedText is a slice of the
// section's NORMALIZED content, and the document slice carries whatever
// whitespace the author wrote — and that is exactly the mapping under test.
//
// It also asserts the other half: the line between a result that locates and
// one that does not is not the STATUS but whether it located a SPAN at all. A
// section anchor has no span to locate and an orphan located nothing; both must
// carry 0..0, which is what keeps "zero is a legal offset" safe for a consumer
// discriminating on the range being non-empty.
func checkResultOffsets(t *testing.T, doc string, r Result) {
	t.Helper()
	if r.Status == StatusOrphaned || r.Anchor.Span == "" {
		if r.MatchStart != 0 || r.MatchEnd != 0 {
			t.Fatalf("%s result over span %q carries offsets %d..%d; a result that located no span must locate no window",
				r.Status, r.Anchor.Span, r.MatchStart, r.MatchEnd)
		}
		return
	}
	if r.MatchStart < 0 || r.MatchEnd > len(doc) || r.MatchStart >= r.MatchEnd {
		t.Fatalf("offsets %d..%d are not a non-empty range inside a %d-byte document",
			r.MatchStart, r.MatchEnd, len(doc))
	}
	if got := Normalize(doc[r.MatchStart:r.MatchEnd]); got != r.MatchedText {
		t.Fatalf("Normalize(doc[%d:%d]) = %q, want MatchedText %q", r.MatchStart, r.MatchEnd, got, r.MatchedText)
	}
	// The text alone would also be satisfied by an identical passage
	// somewhere else in the document, so the range must additionally lie
	// inside a section carrying the heading path the result reports —
	// which is the thing a caller draws the thread on.
	for _, s := range ParseSections(doc) {
		if pathsEqual(s.HeadingPath, r.Anchor.HeadingPath) &&
			r.MatchStart >= s.ContentStart && r.MatchEnd <= s.ContentStart+len(s.Content) {
			return
		}
	}
	t.Fatalf("offsets %d..%d fall outside every section with path %v", r.MatchStart, r.MatchEnd, r.Anchor.HeadingPath)
}

// TestResultOffsetsBracketTheMatch drives checkResultOffsets over a generated
// population: the committed corpus lands only five comments fuzzy, too few to
// tell a correct token-to-byte mapping from one that happens to work for short
// windows in single-line sections. Every section of original.md contributes
// windows at three widths and two start positions.
//
// Both locating arms are counted separately at the end. They map their
// positions by different routes -- a token index for fuzzy, a byte offset for
// exact -- so a pooled count could go vacuous on one and not say so.
func TestResultOffsetsBracketTheMatch(t *testing.T) {
	original, rewritten, comments, _ := loadCorpus(t)

	anchors := []Anchor{largeSpanAnchor(t, original, rewritten)}
	for _, c := range comments {
		anchors = append(anchors, Anchor{HeadingPath: c.HeadingPath, Span: c.Span})
	}
	// Widths and starts are literals rather than an exhaustive sweep: a full
	// sweep costs minutes of fuzzy search for coverage this already has, since
	// what the mapping can get wrong is a section, a start or a width.
	for _, s := range ParseSections(original) {
		tokens := strings.Fields(Normalize(s.Content))
		for _, size := range []int{5, 8, 13} {
			for _, start := range []int{1, len(tokens) / 2} {
				if start+size > len(tokens) {
					continue
				}
				anchors = append(anchors, Anchor{
					HeadingPath: s.HeadingPath,
					Span:        strings.Join(tokens[start:start+size], " "),
				})
			}
		}
	}

	fuzzy, exactly := 0, 0
	for _, a := range anchors {
		r := Reanchor(a, rewritten, nil)
		checkResultOffsets(t, rewritten, r)
		switch r.Status {
		case StatusFuzzy:
			fuzzy++
		case StatusExact, StatusMoved:
			exactly++
		}
	}
	const minFuzzy, minExact = 40, 40
	if fuzzy < minFuzzy {
		t.Fatalf("%d of %d anchors landed fuzzy, want at least %d — the offset assertions ran over nothing",
			fuzzy, len(anchors), minFuzzy)
	}
	if exactly < minExact {
		t.Fatalf("%d of %d anchors landed exact or moved, want at least %d — the exact arm's offsets ran over nothing",
			exactly, len(anchors), minExact)
	}
	t.Logf("%d anchors, %d fuzzy and %d exact/moved with offsets asserted at both ends", len(anchors), fuzzy, exactly)
}

// TestResultOffsetsAcrossNonASCIIWhitespace is the case the committed fixtures
// cannot make: they carry multibyte characters but no non-ASCII WHITESPACE, so
// they never separate unicode.IsSpace from an ASCII-only space test. Normalize
// is built on strings.Fields, which splits on U+00A0; a mapping that did not
// would merge two tokens into one field and put every later offset out by one.
func TestResultOffsetsAcrossNonASCIIWhitespace(t *testing.T) {
	const before = "# T\n\n## S\n\nThe\u00a0exporter retries the upload three times before giving up on the batch.\n"
	const after = "# T\n\n## S\n\nThe\u00a0exporter retries the upload three times before abandoning the batch.\n"
	anchor := mustAnchor(t, before, "retries the upload three times before giving up on the batch.", nil)
	r := Reanchor(anchor, after, nil)
	if r.Status != StatusFuzzy {
		t.Fatalf("status = %s, want %s -- this test only exercises the mapping on the fuzzy arm", r.Status, StatusFuzzy)
	}
	// The span deliberately begins after the non-breaking space. An
	// ASCII-only splitter would fuse "The" and "exporter" into one field and
	// shift every field index past it, so the offsets asserted below are
	// only reachable by a splitter that agrees with strings.Fields.
	if !strings.Contains(after[:r.MatchStart], "\u00a0") {
		t.Fatalf("match starts at %d, at or before the non-breaking space -- the split rule is untested here", r.MatchStart)
	}
	checkResultOffsets(t, after, r)
}

// TestFieldBoundsTracksNormalizedTokens asserts the identity the whole
// token-to-byte mapping rests on: fieldBounds returns one entry per token of
// Normalize(raw), in the same order, each bracketing that token's own bytes.
//
// It matters because Reanchor indexes the returned slice by a fuzzyHit's token
// index: if the two sequences stopped lining up, production would see an
// index-out-of-range panic or a silently shifted window. The raw inputs include
// the whitespace forms the committed fixtures lack — a non-breaking space, a
// tab, leading and trailing space — since those separate strings.Fields from a
// naive splitter.
func TestFieldBoundsTracksNormalizedTokens(t *testing.T) {
	original, rewritten, _, _ := loadCorpus(t)
	raws := []string{
		"",
		"   \t\n  ",
		"one",
		"  leading and trailing  ",
		"tab\tseparated\tfields",
		"non breaking space",
		"em — dash and arrow → between words",
		"windows\r\nline\r\nendings",
	}
	for _, doc := range []string{original, rewritten} {
		for _, s := range ParseSections(doc) {
			raws = append(raws, s.Content)
		}
	}
	checked := 0
	for _, raw := range raws {
		bounds := fieldBounds(raw)
		tokens := strings.Fields(Normalize(raw))
		if len(bounds) != len(tokens) {
			t.Fatalf("fieldBounds returned %d entries for %d tokens of %q", len(bounds), len(tokens), truncateRunes(raw, 80))
		}
		for i, b := range bounds {
			if got := raw[b[0]:b[1]]; got != tokens[i] {
				t.Fatalf("field %d of %q = %q, token %d of its normalization = %q",
					i, truncateRunes(raw, 80), got, i, tokens[i])
			}
		}
		checked += len(tokens)
	}
	const minTokens = 5000
	if checked < minTokens {
		t.Fatalf("checked %d tokens, want at least %d — the sample went vacuous", checked, minTokens)
	}
	t.Logf("%d tokens located across %d strings", checked, len(raws))
}

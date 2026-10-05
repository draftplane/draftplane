// Package reanchor re-locates comment anchors across revisions of a markdown
// document, so a comment made against one version still points at the right
// text after the document is edited. A golden corpus holds it to exact
// results; see CORPUS.md.
package reanchor

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

func pathsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func truncateRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}

// CreateAnchor creates an anchor for a span of text in a document. The span
// may include arbitrary line breaks/indentation; it is stored normalized.
// When the span occurs in more than one section, headingHint selects which
// one; without a hint a multi-section span is an error.
func CreateAnchor(doc, span string, headingHint []string) (Anchor, error) {
	normSpan := Normalize(span)
	if len(strings.Split(normSpan, " ")) < 3 || normSpan == "" {
		return Anchor{}, fmt.Errorf("span too short to anchor reliably: %q (need at least 3 words)", normSpan)
	}
	var containing []Section
	for _, s := range ParseSections(doc) {
		if strings.Contains(Normalize(s.Content), normSpan) {
			containing = append(containing, s)
		}
	}
	if len(containing) == 0 {
		return Anchor{}, fmt.Errorf("span not found in document: %q", truncateRunes(normSpan, 60))
	}
	section := containing[0]
	if headingHint != nil {
		found := false
		for _, s := range containing {
			if pathsEqual(s.HeadingPath, headingHint) {
				section = s
				found = true
				break
			}
		}
		if !found {
			return Anchor{}, fmt.Errorf("span not found under heading path: %s", strings.Join(headingHint, " > "))
		}
	} else if len(containing) > 1 {
		return Anchor{}, fmt.Errorf("span occurs in %d sections; pass a headingHint to disambiguate: %q", len(containing), truncateRunes(normSpan, 60))
	}
	return Anchor{HeadingPath: section.HeadingPath, Span: normSpan}, nil
}

// CreateSectionAnchor creates an anchor for a comment on a section itself —
// located by heading path alone (Span empty). The path must exist verbatim.
func CreateSectionAnchor(doc string, headingPath []string) (Anchor, error) {
	for _, s := range ParseSections(doc) {
		if pathsEqual(s.HeadingPath, headingPath) {
			return Anchor{HeadingPath: headingPath, Span: ""}, nil
		}
	}
	return Anchor{}, fmt.Errorf("no section with heading path: %s", strings.Join(headingPath, " > "))
}

type sectionContent struct {
	section     Section
	normContent string
}

type exactHit struct {
	sectionIdx int
	// normStart is the byte offset of THIS occurrence within the section's
	// normalized content, so an accepted hit reports where it actually landed
	// rather than where the span first happens to appear.
	normStart int
}

type fuzzyHit struct {
	sectionIdx int
	// text is a SLICE of the section's normalized content, so it holds that
	// whole string alive for as long as the hit -- and as long as any
	// Result.MatchedText built from it. Deliberate: a result carries at most
	// three candidates plus one match, so the retention is bounded by the
	// document, and copying every window is the allocation this avoids.
	text        string
	spanScore   float64
	combined    float64
	tokenStart  int
	tokenLength int
}

// Reanchor locates an anchor created against one version of a document in a
// new version. nil opts means DefaultOptions. Strategy, in order:
//  1. Exact (whitespace-normalized) span match — unique hit wins outright;
//     duplicate hits are disambiguated by heading-path similarity against the
//     best hit under a different heading path.
//  2. Fuzzy token-window search scored by character-bigram similarity,
//     weighted toward the section whose heading path resembles the anchor's,
//     behind an unconditional relative-margin ambiguity gate.
//  3. Orphaned, with the best near-misses returned for adjudication.
//
// Short spans (under Options.ShortSpanChars runes) additionally require
// heading corroboration in both tiers.
func Reanchor(anchor Anchor, newDoc string, opts *Options) Result {
	options := DefaultOptions
	if opts != nil {
		options = *opts
	}

	var sections []sectionContent
	for _, s := range ParseSections(newDoc) {
		sections = append(sections, sectionContent{section: s, normContent: Normalize(s.Content)})
	}

	if anchor.Span == "" {
		return reanchorSection(anchor, sections, options)
	}

	shortSpan := len([]rune(anchor.Span)) < options.ShortSpanChars

	var exactHits []exactHit
	for idx, sc := range sections {
		from := 0
		for {
			at := strings.Index(sc.normContent[from:], anchor.Span)
			if at == -1 {
				break
			}
			exactHits = append(exactHits, exactHit{sectionIdx: idx, normStart: from + at})
			from += at + 1
		}
	}

	if len(exactHits) > 0 {
		type scoredHit struct {
			hit exactHit
			sim float64
		}
		scored := make([]scoredHit, len(exactHits))
		for i, h := range exactHits {
			scored[i] = scoredHit{hit: h, sim: HeadingPathSimilarity(anchor.HeadingPath, sections[h.sectionIdx].section.HeadingPath)}
		}
		sort.SliceStable(scored, func(i, j int) bool { return scored[i].sim > scored[j].sim })
		best := scored[0]
		bestPath := sections[best.hit.sectionIdx].section.HeadingPath

		// The rival is the best hit under a *different* heading path — a
		// same-path duplicate must not mask a competing location further
		// down the ranking.
		var rival *scoredHit
		for i := range scored {
			if !pathsEqual(sections[scored[i].hit.sectionIdx].section.HeadingPath, bestPath) {
				rival = &scored[i]
				break
			}
		}
		ambiguous := rival != nil && rival.sim > best.sim-options.ExactAmbiguityDelta
		// A short span is weak evidence on its own: a generic phrase can
		// recur coincidentally after its true target is deleted. Require the
		// heading to corroborate before trusting the hit.
		uncorroboratedShortSpan := shortSpan && best.sim < options.ShortSpanHeadingSim

		if !ambiguous && !uncorroboratedShortSpan {
			samePath := pathsEqual(bestPath, anchor.HeadingPath)
			status := StatusMoved
			if samePath {
				status = StatusExact
			}
			confidence := 0.9
			if len(exactHits) == 1 {
				if samePath {
					confidence = 1
				} else {
					confidence = 0.95
				}
			}
			// These are the WINNING hit's own offsets. The scan above
			// enumerates every occurrence of the span across every section
			// and the stable sort ranks them by heading-path similarity, so
			// a span occurring more than once has no single location until
			// that ranking picks one; the offset says which one it picked.
			//
			// The offset is carried because of a defect a second text lookup
			// structurally cannot close: reanchor matches a span against a
			// SECTION, ui.ResolveAnchor asks which single ui.Block contains
			// it, and a section holds many blocks. Normalize collapses
			// newlines, so a quote spanning a paragraph break matches a
			// section perfectly -- StatusExact, confidence 1 -- and then
			// resolves to no block at all. A POSITION answers where such a
			// match sits; see app.placedBlock for the routing.
			sec := sections[best.hit.sectionIdx].section
			start, end := rawRange(sec.Content, sections[best.hit.sectionIdx].normContent,
				best.hit.normStart, best.hit.normStart+len(anchor.Span))
			return Result{
				Status:      status,
				Anchor:      Anchor{HeadingPath: bestPath, Span: anchor.Span},
				Confidence:  confidence,
				MatchedText: anchor.Span,
				MatchStart:  sec.ContentStart + start,
				MatchEnd:    sec.ContentStart + end,
			}
		}
		// Ambiguous or uncorroborated exact hits: orphan with the hits as
		// candidates rather than guess.
		n := min(len(scored), 3)
		candidates := make([]Candidate, n)
		for i := 0; i < n; i++ {
			candidates[i] = Candidate{
				HeadingPath: sections[scored[i].hit.sectionIdx].section.HeadingPath,
				Text:        anchor.Span,
				Score:       scored[i].sim,
			}
		}
		return Result{Status: StatusOrphaned, Candidates: candidates}
	}

	hits := fuzzySearch(anchor, sections)
	if len(hits) > 0 {
		best := hits[0]
		var runnerUp *fuzzyHit
		for i := 1; i < len(hits); i++ {
			if !overlaps(hits[i], best) {
				runnerUp = &hits[i]
				break
			}
		}
		margin := 1.0
		if runnerUp != nil {
			margin = (best.combined - runnerUp.combined) / best.combined
		}
		// The margin gate is unconditional: two high-scoring near-duplicates
		// must orphan as ambiguous no matter how well the best one scores.
		// Short spans need heading corroboration here just as in the exact
		// tier.
		uncorroboratedShortSpan := shortSpan &&
			HeadingPathSimilarity(anchor.HeadingPath, sections[best.sectionIdx].section.HeadingPath) < options.ShortSpanHeadingSim
		accepted := best.spanScore >= options.MinScore &&
			margin >= options.MinMargin &&
			!uncorroboratedShortSpan
		if accepted {
			sec := sections[best.sectionIdx].section
			// Where the window landed, in the document's own bytes. The
			// section's fields and its normalized tokens are the same
			// sequence (see fieldBounds), so the hit's token indices select
			// the fields that bracket the match; computed here, for the one
			// accepted hit, rather than for each of the tens of thousands
			// fuzzySearch scores.
			bounds := fieldBounds(sec.Content)
			return Result{
				Status:      StatusFuzzy,
				Anchor:      Anchor{HeadingPath: sec.HeadingPath, Span: best.text},
				Confidence:  best.spanScore,
				MatchedText: best.text,
				MatchStart:  sec.ContentStart + bounds[best.tokenStart][0],
				MatchEnd:    sec.ContentStart + bounds[best.tokenStart+best.tokenLength-1][1],
			}
		}
	}

	return Result{Status: StatusOrphaned, Candidates: dedupeCandidates(hits, sections)}
}

// reanchorSection relocates a section anchor by heading-path matching alone.
// Exact path wins; otherwise the most similar path must clear SectionMinSim
// and lead the best different runner-up by the relative margin — the same
// ambiguity posture as the span tiers. Sections sharing an identical path
// collapse to one candidate (first wins; known limitation).
func reanchorSection(anchor Anchor, sections []sectionContent, options Options) Result {
	type pathCand struct {
		path []string
		leaf string
		sim  float64
	}
	var cands []pathCand
	for _, sc := range sections {
		p := sc.section.HeadingPath
		if len(p) == 0 {
			continue
		}
		dup := false
		for _, c := range cands {
			if pathsEqual(c.path, p) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		cands = append(cands, pathCand{path: p, leaf: sc.section.Title, sim: HeadingPathSimilarity(anchor.HeadingPath, p)})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].sim > cands[j].sim })

	for _, c := range cands {
		if pathsEqual(c.path, anchor.HeadingPath) {
			return Result{Status: StatusExact, Anchor: anchor, Confidence: 1, MatchedText: c.leaf}
		}
	}
	if len(cands) > 0 {
		best := cands[0]
		margin := 1.0
		if len(cands) > 1 && best.sim > 0 {
			margin = (best.sim - cands[1].sim) / best.sim
		}
		if best.sim >= options.SectionMinSim && margin >= options.MinMargin {
			return Result{
				Status:      StatusMoved,
				Anchor:      Anchor{HeadingPath: best.path, Span: ""},
				Confidence:  best.sim,
				MatchedText: best.leaf,
			}
		}
	}
	n := min(len(cands), 3)
	out := make([]Candidate, n)
	for i := 0; i < n; i++ {
		out[i] = Candidate{HeadingPath: cands[i].path, Text: cands[i].leaf, Score: cands[i].sim}
	}
	return Result{Status: StatusOrphaned, Candidates: out}
}

func overlaps(a, b fuzzyHit) bool {
	if a.sectionIdx != b.sectionIdx {
		return false
	}
	return a.tokenStart < b.tokenStart+b.tokenLength && b.tokenStart < a.tokenStart+a.tokenLength
}

func dedupeCandidates(hits []fuzzyHit, sections []sectionContent) []Candidate {
	var picked []fuzzyHit
	for _, hit := range hits {
		conflict := false
		for _, p := range picked {
			if overlaps(p, hit) {
				conflict = true
				break
			}
		}
		if !conflict {
			picked = append(picked, hit)
		}
		if len(picked) == 3 {
			break
		}
	}
	candidates := make([]Candidate, len(picked))
	for i, h := range picked {
		candidates[i] = Candidate{
			HeadingPath: sections[h.sectionIdx].section.HeadingPath,
			Text:        h.text,
			Score:       h.spanScore,
		}
	}
	return candidates
}

func fuzzySearch(anchor Anchor, sections []sectionContent) []fuzzyHit {
	spanTokenCount := len(strings.Split(anchor.Span, " "))
	sizes := windowSizes(spanTokenCount)
	// The anchor's side of every window comparison is invariant across the
	// whole search.
	spanScorer := newBigramScorer(anchor.Span)
	// One roller for the whole search, restarted per section and per size; it
	// makes this loop O(token) per start instead of O(window).
	roll := spanScorer.roller()
	var hits []fuzzyHit

	for idx, sc := range sections {
		if sc.normContent == "" {
			continue
		}
		tokens := strings.Split(sc.normContent, " ")
		offsets := tokenOffsets(tokens)
		lowered, runeStarts := loweredTokenRunes(sc.normContent, tokens)
		headingSim := HeadingPathSimilarity(anchor.HeadingPath, sc.section.HeadingPath)
		for _, size := range sizes {
			if size > len(tokens) {
				continue
			}
			// start returns to 0, which is backwards for the roller.
			roll.restart(lowered)
			for start := 0; start+size <= len(tokens); start++ {
				text := sc.normContent[offsets[start] : offsets[start+size]-1]
				spanScore := roll.scoreWindow(runeStarts[start], runeStarts[start+size]-1, text)
				if spanScore < 0.3 {
					continue
				}
				// Explicit float64 conversions round each operation
				// individually and prevent FMA fusion, so results are
				// identical across platforms.
				spanPart := float64(0.85 * spanScore)
				headingPart := float64(0.15 * headingSim)
				hits = append(hits, fuzzyHit{
					sectionIdx:  idx,
					text:        text,
					spanScore:   spanScore,
					combined:    spanPart + headingPart,
					tokenStart:  start,
					tokenLength: size,
				})
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].combined > hits[j].combined })
	return hits
}

// tokenOffsets returns the byte offset of each of norm's tokens within it,
// plus a terminator one past the end, so that the window of tokens
// [start, start+size) is the substring norm[offsets[start] : offsets[start+size]-1].
//
// It is sound only because norm came from Normalize: tokens are separated by
// exactly one space, none leading, trailing or repeated, so tokens and
// separators tile the string with no slack. TestWindowSliceEqualsJoin reddens
// if Normalize ever stops collapsing whitespace.
func tokenOffsets(tokens []string) []int {
	offsets := make([]int, len(tokens)+1)
	at := 0
	for i, tok := range tokens {
		offsets[i] = at
		at += len(tok) + 1
	}
	offsets[len(tokens)] = at
	return offsets
}

// loweredTokenRunes decodes norm's lowering ONCE for a whole section and
// returns it alongside the RUNE index at which each of norm's tokens starts,
// plus a terminator one past the end -- so the window of tokens
// [start, start+size) is the rune slice
// lowered[starts[start] : starts[start+size]-1], the rune-space twin of the
// byte-space slice tokenOffsets gives.
//
// Two properties make it sound, and NEITHER IS TRUE IN BYTES. Lowering
// commutes with slicing in rune space, because strings.ToLower is
// strings.Map(unicode.ToLower, s) -- rune for rune, none added or deleted --
// but lowering can change a rune's encoded length, so tokenOffsets' byte
// offsets cannot be reused here. And rune counts survive lowering, so starts
// can be accumulated from the tokens rather than by re-scanning.
//
// Token boundaries are rune boundaries even for invalid UTF-8: norm's tokens
// split on the ASCII space, which no multi-byte or invalid sequence can
// consume.
func loweredTokenRunes(norm string, tokens []string) ([]rune, []int) {
	lowered := make([]rune, 0, len(norm))
	for _, r := range norm {
		lowered = append(lowered, unicode.ToLower(r))
	}
	starts := make([]int, len(tokens)+1)
	at := 0
	for i, tok := range tokens {
		starts[i] = at
		at += utf8.RuneCountInString(tok) + 1
	}
	starts[len(tokens)] = at
	return lowered, starts
}

// fieldBounds returns the byte offsets [start, end) of every
// whitespace-delimited field of raw, in order.
//
// It bridges a fuzzyHit's token indices to the raw document, which are not the
// same coordinate system: tokenStart and tokenLength index the NORMALIZED
// content, so a normalized offset is not a raw offset. What survives is the
// SEQUENCE -- Normalize is strings.Join(strings.Fields(text), " "), so token i
// of Normalize(raw) is field i of raw.
//
// The split rule must stay unicode.IsSpace, matching strings.Fields: an
// ASCII-only space test would swallow U+00A0 and its relatives into the
// surrounding field, merging two tokens and putting every later index out by
// one.
func fieldBounds(raw string) [][2]int {
	var bounds [][2]int
	start := -1
	for i, r := range raw {
		if unicode.IsSpace(r) {
			if start >= 0 {
				bounds = append(bounds, [2]int{start, i})
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		bounds = append(bounds, [2]int{start, len(raw)})
	}
	return bounds
}

// rawRange maps the byte range [start, end) of norm — which must be
// Normalize(raw) — back to the byte range of raw that produced it.
//
// It is the exact arm's half of the bridge fieldBounds documents, separate
// because that arm knows a POSITION where the fuzzy arm knows token indices. An
// anchor's span need not begin or end on a token boundary -- an agent may quote
// from mid-word -- so rounding out to the containing tokens would report a
// range whose Normalize is not MatchedText, the one identity Result's offsets
// promise. It is exact because token i of norm IS field i of raw byte for byte,
// so the intra-token remainder carries across untouched.
func rawRange(raw, norm string, start, end int) (int, int) {
	bounds := fieldBounds(raw)
	offsets := tokenOffsets(strings.Split(norm, " "))
	first := tokenContaining(offsets, start)
	last := tokenContaining(offsets, end-1)
	return bounds[first][0] + start - offsets[first], bounds[last][0] + end - offsets[last]
}

// tokenContaining is the index of the token whose own bytes hold the
// normalized offset at, given that string's token offsets. offsets is
// ascending with a terminator one past the end (see tokenOffsets), so the
// containing token is the last one starting at or before at.
func tokenContaining(offsets []int, at int) int {
	i := sort.SearchInts(offsets, at)
	if i == len(offsets) || offsets[i] > at {
		i--
	}
	return i
}

func windowSizes(n int) []int {
	set := map[int]bool{}
	for d := -3; d <= 3; d++ {
		if n+d >= 1 {
			set[n+d] = true
		}
	}
	set[max(1, int(math.Round(float64(n)*0.75)))] = true
	set[int(math.Round(float64(n)*1.3))] = true
	sizes := make([]int, 0, len(set))
	for s := range set {
		sizes = append(sizes, s)
	}
	sort.Ints(sizes)
	return sizes
}

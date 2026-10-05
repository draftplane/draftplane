package reanchor

import "strings"

// Normalize collapses runs of whitespace to single spaces and trims. Anchor
// spans are stored normalized so line rewrapping never breaks an exact match.
func Normalize(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// bigramCounts counts each adjacent rune pair of rs. The key is the PAIR and
// not the two-rune string it spells: rollingOverlap keys its window map the
// same way and the two must agree, and string(rs[i:i+2]) allocates per bigram.
// The re-keying is bijective -- two rune pairs spell equal strings exactly when
// they are equal pairs -- so no score changes.
func bigramCounts(rs []rune) map[[2]rune]int {
	counts := make(map[[2]rune]int, len(rs))
	for i := 0; i+1 < len(rs); i++ {
		counts[[2]rune{rs[i], rs[i+1]}]++
	}
	return counts
}

// bigramScorer is one side of DiceSimilarity, prepared once so it can be scored
// against many strings. Which side is prepared changes no score: Dice is
// symmetric in both the min-overlap and the denominator, and the sum stays in
// integers until the single division.
//
// It carries scratch -- counts is borrowed and restored by similarityTo, buf and
// spent are reused -- so a scorer is NOT safe for concurrent use and each caller
// makes its own. Do not drop the restore to "simplify": it is what keeps the
// prepare-once-score-many contract true, and a reuse after a lossy borrow would
// return quietly lowered scores rather than an error.
type bigramScorer struct {
	lower string
	// lowerRunes is the prepared side decoded once: its LENGTH is the
	// denominator's half of the Dice formula, and on the rolled path nothing
	// else ever decodes the prepared side.
	lowerRunes []rune
	counts     map[[2]rune]int
	spent      [][2]rune
	buf        []rune
}

func newBigramScorer(s string) *bigramScorer {
	lower := strings.ToLower(s)
	rs := []rune(lower)
	return &bigramScorer{lower: lower, lowerRunes: rs, counts: bigramCounts(rs)}
}

// score is the tail EVERY caller shares: the two early returns and the single
// division. Two paths in this package arrive at an overlap -- the one-shot walk
// below and the rolled counter in rollingOverlap -- and what is done with the
// number must not fork between them.
//
// equalLowering must be true exactly when strings.ToLower(other) == s.lower for
// the side this overlap was counted against. It is checked FIRST, so it wins
// over the under-two-runes return; only TestRolledWindowsMatchDiceSimilarity
// pins that order, and swapping the two leaves the golden corpus green.
func (b *bigramScorer) score(equalLowering bool, overlap, otherRunes int) float64 {
	if equalLowering {
		return 1
	}
	if otherRunes < 2 || len(b.lowerRunes) < 2 {
		return 0
	}
	return float64(2*overlap) / float64(otherRunes-1+len(b.lowerRunes)-1)
}

// similarityTo returns exactly what DiceSimilarity(other, s) returns for the s
// this scorer was built from, early returns included: equal lowerings score 1
// before the length check, and a side under two runes scores 0.
//
// It counts the overlap by CONSUMING from counts rather than intersecting a
// second map: taking one unit of a gram whenever any is left takes exactly
// min(count in other, count here) units of it. Every unit taken is recorded in
// spent and put back before returning, so counts is identical on exit, and
// there must remain no path out of this function between the two loops.
func (b *bigramScorer) similarityTo(other string) float64 {
	lower := strings.ToLower(other)
	if lower == b.lower {
		return 1
	}
	// A reused buffer rather than []rune(lower): identical runes, U+FFFD for
	// invalid UTF-8 included.
	b.buf = b.buf[:0]
	for _, r := range lower {
		b.buf = append(b.buf, r)
	}
	rs := b.buf
	overlap := 0
	b.spent = b.spent[:0]
	for i := 0; i+1 < len(rs); i++ {
		gram := [2]rune{rs[i], rs[i+1]}
		if left := b.counts[gram]; left > 0 {
			b.counts[gram] = left - 1
			b.spent = append(b.spent, gram)
			overlap++
		}
	}
	for _, gram := range b.spent {
		b.counts[gram]++
	}
	return b.score(false, overlap, len(rs))
}

// rollingOverlap scores a window that SLIDES along one text against a prepared
// scorer, so a one-token slide costs O(runes in the token) where scoring the
// window from scratch costs O(runes in the window).
//
// What it maintains is the OVERLAP itself, not merely the window's counts:
//
//	overlap = sum over g of min(window[g], anchor[g])
//
//	adding g:    min(w+1, a) - min(w, a) is 1 exactly when w+1 <= a
//	removing g:  min(w-1, a) - min(w, a) is -1 exactly when w <= a
//
// take and drop are those two lines and nothing else. The result is EXACT, not
// approximate: every step is integer arithmetic and the float appears only in
// bigramScorer.score's single division, so a rolled score and a one-shot score
// are bit-identical whenever the integers agree.
//
// It slides in RUNE space, because strings.ToLower maps rune to rune: the
// lowering of a token-aligned slice is the same slice of the lowering in runes,
// though not in bytes, since lowering can change a rune's encoded length.
//
// A roller is scratch, like the scorer it points at: not safe for concurrent
// use, and it must not be slid while that scorer has its counts borrowed by
// similarityTo.
type rollingOverlap struct {
	scorer *bigramScorer
	// runes is the lowered text the window slides along, set by restart.
	runes []rune
	// window counts the grams of the current window. Grams that fall out are
	// left at zero rather than deleted: a zero entry reads the same as an
	// absent one.
	window map[[2]rune]int
	// lo and hi are the half-open range of BIGRAM indices currently counted;
	// bigram i is the pair (runes[i], runes[i+1]). A window of runes [a, b)
	// holds bigrams [a, b-1), which is empty when the window is under two
	// runes long.
	lo, hi  int
	overlap int
}

// roller returns a slider against this scorer's prepared side. One roller
// serves a whole search: restart re-points it at each section.
func (b *bigramScorer) roller() *rollingOverlap {
	return &rollingOverlap{scorer: b, window: make(map[[2]rune]int)}
}

// restart empties the window and points the roller at runes. It is what makes
// slideTo's forward-only precondition keepable: a caller that is about to move
// the window backwards -- to the next window SIZE over the same section, or to
// a new section -- restarts instead.
func (r *rollingOverlap) restart(runes []rune) {
	clear(r.window)
	r.runes = runes
	r.lo, r.hi, r.overlap = 0, 0, 0
}

func (r *rollingOverlap) take(i int) {
	g := [2]rune{r.runes[i], r.runes[i+1]}
	n := r.window[g] + 1
	r.window[g] = n
	if n <= r.scorer.counts[g] {
		r.overlap++
	}
}

func (r *rollingOverlap) drop(i int) {
	g := [2]rune{r.runes[i], r.runes[i+1]}
	n := r.window[g]
	if n <= r.scorer.counts[g] {
		r.overlap--
	}
	r.window[g] = n - 1
}

// slideTo makes the window runes[lo:hi]. NEITHER END MAY MOVE BACKWARDS since
// the last restart: the roller only knows how to leave a bigram behind on the
// left and pick one up on the right.
//
// When the new window shares no bigram with the old one the multiset is rebuilt.
// Do NOT widen that test to lo > r.hi+1: at lo == r.hi+1 the drop loop would
// drop bigram r.hi, which was never taken, driving a count below zero and
// reading one rune past the end. The suite stays green either way, because
// fuzzySearch only ever produces a gap of exactly 2.
func (r *rollingOverlap) slideTo(lo, hi int) {
	end := hi - 1
	if end < lo {
		end = lo
	}
	if lo > r.hi {
		clear(r.window)
		r.overlap = 0
		r.lo, r.hi = lo, lo
	}
	for i := r.lo; i < lo; i++ {
		r.drop(i)
	}
	for i := r.hi; i < end; i++ {
		r.take(i)
	}
	r.lo, r.hi = lo, end
}

// scoreWindow slides to runes[lo:hi] and returns exactly what
// similarityTo(text) would return, where text is the string that rune range
// spells -- caller-supplied because the window is a slice of the section's
// content the caller already holds, and because the equal-lowering early
// return compares BYTES.
//
// The equality return is gated, and behind the gate sits similarityTo's own
// line verbatim: strings.ToLower(text) == b.lower, compared over BYTES so the
// fast path runs the very predicate it accelerates rather than an equivalent of
// it. The gate is a NECESSARY condition for that equality -- equal lowerings
// imply equal rune counts and an identical bigram multiset, hence
// overlap == runes-1 -- so the answer is the original's, not an approximation.
func (r *rollingOverlap) scoreWindow(lo, hi int, text string) float64 {
	r.slideTo(lo, hi)
	n := hi - lo
	b := r.scorer
	equal := n == len(b.lowerRunes) && r.overlap >= n-1 && strings.ToLower(text) == b.lower
	return b.score(equal, r.overlap, n)
}

// DiceSimilarity is the Sørensen–Dice coefficient over character bigrams,
// case-insensitive. Bigrams are computed over runes (code points), never bytes;
// CORPUS.md lists this among the choices the golden corpus depends on.
//
// It delegates to bigramScorer so that the one-shot and the prepared caller
// share a single scoring implementation: a second copy that could drift from
// this one is the failure this package's golden corpus exists to prevent.
func DiceSimilarity(a, b string) float64 {
	return newBigramScorer(b).similarityTo(a)
}

// HeadingPathSimilarity compares two heading paths. The leaf heading
// dominates: a comment follows its section even when the section moves under
// a new parent.
func HeadingPathSimilarity(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	leaf := DiceSimilarity(a[len(a)-1], b[len(b)-1])
	full := DiceSimilarity(strings.Join(a, " > "), strings.Join(b, " > "))
	// Explicit float64 conversions round each operation individually and
	// prevent FMA fusion, so results are identical across platforms.
	leafPart := float64(0.7 * leaf)
	fullPart := float64(0.3 * full)
	return leafPart + fullPart
}

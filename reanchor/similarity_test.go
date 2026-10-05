package reanchor

import (
	"math"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"collapses whitespace runs", "a  b\n   c\t d", "a b c d"},
		{"trims edges", "  hello world \n", "hello world"},
		{"multibyte preserved", "reuse → revoke family", "reuse → revoke family"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Normalize(tc.in); got != tc.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDiceSimilarity(t *testing.T) {
	cases := []struct {
		name, a, b string
		min, max   float64
	}{
		{"identical", "identical text", "identical text", 1, 1},
		{"case-insensitive identical", "Hello World", "hello world", 1, 1},
		{"disjoint", "completely different", "zzz qqq xxx", 0, 0.1},
		{"reworded", "store only opaque customer tokens", "store just opaque tokens for customers", 0.5, 0.95},
		{"too short", "a", "a longer string", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DiceSimilarity(tc.a, tc.b)
			if got < tc.min || got > tc.max {
				t.Fatalf("DiceSimilarity(%q, %q) = %v, want in [%v, %v]", tc.a, tc.b, got, tc.min, tc.max)
			}
		})
	}
}

func TestDiceSimilarityUsesRunesNotBytes(t *testing.T) {
	// "→→" is one distinct bigram as runes; as bytes it would be six bytes
	// and five bigrams, changing the denominator and the score.
	got := DiceSimilarity("→→", "→→")
	if got != 1 {
		t.Fatalf("identical multibyte strings must score 1, got %v", got)
	}
	if got := DiceSimilarity("a→b", "a→b"); got != 1 {
		t.Fatalf("identical multibyte strings must score 1, got %v", got)
	}
	// Byte bigrams would share the interior bytes of the two arrows and
	// score > 0; rune bigrams share nothing.
	if got := DiceSimilarity("a→b", "x→y"); got != 0 {
		t.Fatalf("disjoint multibyte strings must score 0, got %v", got)
	}
}

func TestHeadingPathSimilarity(t *testing.T) {
	cases := []struct {
		name     string
		a, b     []string
		min, max float64
	}{
		{"both empty", nil, nil, 1, 1},
		{"one empty", []string{"A"}, nil, 0, 0},
		{"identical", []string{"Doc", "Section"}, []string{"Doc", "Section"}, 1, 1},
		{"leaf dominates", []string{"Old Doc", "Rollout"}, []string{"New Doc", "Rollout"}, 0.7, 1},
		{"unrelated", []string{"Alpha"}, []string{"Zzz"}, 0, 0.35},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := HeadingPathSimilarity(tc.a, tc.b)
			if got < tc.min || got > tc.max {
				t.Fatalf("HeadingPathSimilarity(%v, %v) = %v, want in [%v, %v]", tc.a, tc.b, got, tc.min, tc.max)
			}
		})
	}
}

// TestBigramScorerReuseMatchesFresh guards bigramScorer's contract -- prepare
// once, score many -- against the way similarityTo counts an overlap: it
// borrows from the prepared side's counts and must leave them exactly as it
// found them. A restore that missed a gram would quietly lower every subsequent
// score rather than fail.
//
// No production caller reuses a scorer today, which is why this matters more
// rather than less: it is the only thing standing between the restore and a
// silent deletion, after which the first caller to reuse one would get wrong
// scores and no failure.
//
// The cases with repeated grams are the ones that matter: overlap is a MULTISET
// intersection, so a scorer that restored presence but not count would pass a
// test built only from strings with distinct bigrams.
func TestBigramScorerReuseMatchesFresh(t *testing.T) {
	const base = "banana bandana abandon banana"
	others := []string{
		base,                            // equal lowering: the score-1 early return
		"BANANA BANDANA ABANDON BANANA", // same, via case folding
		"banana",
		"bananananana",
		"aaaa",
		"a", // under two runes: the score-0 early return
		"",
		"completely unrelated wording",
		"banana bandana abandon banan",
		"→→ multibyte →→",
	}
	scorer := newBigramScorer(base)
	for pass := 0; pass < 2; pass++ {
		for _, other := range others {
			want := DiceSimilarity(other, base)
			if got := scorer.similarityTo(other); got != want {
				t.Fatalf("pass %d: reused scorer on %q = %v, fresh = %v", pass, other, got, want)
			}
		}
	}
}

// TestRolledWindowsMatchDiceSimilarity is the measurement behind the word
// "exact" in rollingOverlap's doc comment. There is no tolerance to argue
// about: overlap is an integer and bigramScorer.score turns it into a float
// with one division, so the rolled score is either the SAME BITS as the
// one-shot score or the arithmetic is wrong. It compares math.Float64bits
// rather than ==, which would let a last-place difference pass as agreement.
//
// It compares against DiceSimilarity, which ships, rather than a frozen copy of
// the pre-rolling scorer -- that would be the second implementation this
// package's golden corpus exists to prevent. The two routes share only
// bigramScorer.score, so a defect in either accumulator shows up here.
//
// Both argument orders, because DiceSimilarity is documented as symmetric and
// the roller only ever prepares one of the two sides.
//
// It addresses windows the way fuzzySearch addresses them, but the drive loop
// is a COPY and this test cannot see it drift: hoisting fuzzySearch's
// roll.restart out of its size loop leaves this green and reddens the corpus
// tests instead. Those cover the caller; this covers the accumulator.
func TestRolledWindowsMatchDiceSimilarity(t *testing.T) {
	original, rewritten, _, _ := loadCorpus(t)

	// A synthetic section carries the shapes the fixtures do not guarantee:
	// single-rune tokens (a size-1 window is under two runes and must take the
	// score-0 return), repeated grams, and multibyte runes either side of a
	// token boundary.
	const synthetic = "# S\n\na b c → →→ banana bandana abandon banana a — b\n"

	// The anchors span the shapes the early returns turn on: one long enough
	// to be the expensive case, one that IS a window of rewritten.md so the
	// equal-lowering return fires, one whose lowering differs from its own
	// text, one under two runes, and one multibyte.
	spans := []string{
		Normalize(strings.Join(strings.Fields(Normalize(ParseSections(original)[3].Content))[:60], " ")),
		"banana bandana abandon banana",
		"A B C",
		"a",
		"→→ banana —",
	}
	if w := firstWindow(t, rewritten, 5); w != "" {
		spans = append(spans, w, strings.ToUpper(w))
	}

	sizes := []int{1, 2, 3, 5, 13, 34, 60}
	windows, ones, zeros, rebuilds, rolled := 0, 0, 0, 0, 0
	for _, span := range spans {
		scorer := newBigramScorer(span)
		roll := scorer.roller()
		for _, doc := range []string{original, rewritten, synthetic} {
			for _, sec := range ParseSections(doc) {
				norm := Normalize(sec.Content)
				if norm == "" {
					continue
				}
				tokens := strings.Split(norm, " ")
				offsets := tokenOffsets(tokens)
				lowered, runeStarts := loweredTokenRunes(norm, tokens)
				for _, size := range sizes {
					if size > len(tokens) {
						continue
					}
					roll.restart(lowered)
					for start := 0; start+size <= len(tokens); start++ {
						text := norm[offsets[start] : offsets[start+size]-1]
						lo, hi := runeStarts[start], runeStarts[start+size]-1
						if lo > roll.hi {
							rebuilds++
						} else {
							rolled++
						}
						got := roll.scoreWindow(lo, hi, text)
						for _, want := range []float64{DiceSimilarity(text, span), DiceSimilarity(span, text)} {
							if math.Float64bits(got) != math.Float64bits(want) {
								t.Fatalf("span %q window %q: rolled %v (%#016x) != one-shot %v (%#016x)",
									span, text, got, math.Float64bits(got), want, math.Float64bits(want))
							}
						}
						switch got {
						case 1:
							ones++
						case 0:
							zeros++
						}
						windows++
					}
				}
			}
		}
	}

	// Non-vacuity, one floor per thing that could quietly stop happening: a
	// sweep that never took the rebuild branch, never rolled one, or never
	// reached either early return would pass while measuring less than it
	// claims.
	for _, g := range []struct {
		name  string
		got   int
		floor int
	}{
		{"windows compared", windows, 250000},
		{"windows rebuilt from scratch", rebuilds, 20000},
		{"windows rolled incrementally", rolled, 200000},
		{"windows scoring exactly 1 (the equal-lowering return)", ones, 50},
		{"windows scoring exactly 0", zeros, 1000},
	} {
		if g.got < g.floor {
			t.Fatalf("%s = %d, want at least %d -- the sample went vacuous", g.name, g.got, g.floor)
		}
	}
	t.Logf("%d windows x 2 argument orders over %d anchors; %d rebuilt, %d rolled, %d scored 1, %d scored 0",
		windows, len(spans), rebuilds, rolled, ones, zeros)
}

// firstWindow returns the first size-token window of the first section of doc
// that has one, normalized -- an anchor guaranteed to be met verbatim during
// the sweep, which is what makes the equal-lowering early return reachable.
func firstWindow(t *testing.T, doc string, size int) string {
	t.Helper()
	for _, sec := range ParseSections(doc) {
		tokens := strings.Fields(Normalize(sec.Content))
		if len(tokens) >= size {
			return strings.Join(tokens[:size], " ")
		}
	}
	return ""
}

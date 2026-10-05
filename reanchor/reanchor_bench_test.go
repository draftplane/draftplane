package reanchor

// This file is the committed method behind this package's cost figures, so the
// method is a command rather than a sentence. Two arms, answering different
// questions:
//
//   - fixtures  All 17 comments of the golden corpus re-anchored against
//     rewritten.md -- the workload TestCorpusMatchesGolden already drives,
//     timed. It says "did anything get slower".
//   - largespan ONE anchor, a deterministic 60-token window out of
//     original.md. A 300+-rune span that no longer matches exactly costs
//     orders of magnitude more than one that does, because fuzzySearch is
//     sections x window sizes x start positions.
//
// Both arms run on any checkout: no corpus directory, no environment variable
// and no network, because original.md and rewritten.md are committed.
//
// Run it as:
//
//	go test ./reanchor -run '^$' -bench BenchmarkReanchor -benchtime 10x -count 2
//
// -benchtime 10x PINS THE ITERATION COUNT so every arm, and both sides of a
// before/after, average over the same n. Left to itself Go sizes n from the
// arm's own cost, so the default can compare a 2-sample mean against a
// 54-sample one, and at n=2 a single scheduling stall IS the figure. Pinning is
// for comparability, not for speed -- it can cost time or save it.
//
// Report a RANGE rather than a point, and DISCLOSE THE MACHINE'S LOAD. A ratio
// taken across two sittings is two measurements pretending to be one.
//
// WHAT IT CANNOT DO. It cannot measure session.Placements, which is what a
// reader actually waits on when a plan opens: that lives two packages up, and
// reaching it from here would invert the import graph this package's existence
// as a leaf depends on. The relation is len(threads) x this.
import (
	"strings"
	"testing"
)

// benchLargeSpanSection names the section the largespan arm draws its anchor
// from, by HEADING PATH rather than by index, so an edit to the fixture that
// adds or removes a section ahead of it fails loudly at the lookup instead of
// silently measuring a different passage.
var benchLargeSpanSection = []string{"Phase 1 — Schema", "1b. Sensors and channels"}

// benchLargeSpanTokens is the window taken out of that section: tokens
// [start, start+count). Both stay literals rather than derived, because the
// arm's whole value is that it measures the same passage at every commit.
const (
	benchLargeSpanStart = 5
	benchLargeSpanCount = 60
)

// largeSpanAnchor builds the largespan arm's anchor and asserts it is
// actually the case the arm claims to measure. Every guard here is a
// non-vacuity guard: an anchor that stopped being long, or stopped landing
// fuzzy, would still produce a number, and the number would be measuring the
// exact/moved path rather than the fuzzy one this benchmark exists to measure.
func largeSpanAnchor(tb testing.TB, original, rewritten string) Anchor {
	tb.Helper()
	var sec *Section
	for i, s := range ParseSections(original) {
		if pathsEqual(s.HeadingPath, benchLargeSpanSection) {
			sec = &ParseSections(original)[i]
			break
		}
	}
	if sec == nil {
		tb.Fatalf("largespan: no section %v in original.md; the fixture moved and this arm measures nothing it claims to",
			benchLargeSpanSection)
		// Unreachable: Fatalf does not return. Written out because tb is
		// an INTERFACE, so staticcheck cannot see that and reads every
		// later use of sec as a possible nil dereference (SA5011).
		return Anchor{}
	}
	tokens := strings.Fields(Normalize(sec.Content))
	if len(tokens) < benchLargeSpanStart+benchLargeSpanCount {
		tb.Fatalf("largespan: section %v holds %d tokens, need %d",
			benchLargeSpanSection, len(tokens), benchLargeSpanStart+benchLargeSpanCount)
	}
	span := strings.Join(tokens[benchLargeSpanStart:benchLargeSpanStart+benchLargeSpanCount], " ")
	// 250 runes rather than DefaultOptions.ShortSpanChars: the short-span
	// floor is 25 and clearing it says nothing about whether this is the
	// EXPENSIVE case. What makes the case expensive is a span long enough
	// that windowSizes spans nine widths over thousands of starts.
	if n := len([]rune(span)); n < 250 {
		tb.Fatalf("largespan: span is %d runes, under the 250 this arm exists to measure", n)
	}
	a := Anchor{HeadingPath: sec.HeadingPath, Span: span}
	if got := Reanchor(a, rewritten, nil).Status; got != StatusFuzzy {
		tb.Fatalf("largespan: anchor lands %s, not %s -- this arm would be measuring the cheap path",
			got, StatusFuzzy)
	}
	return a
}

// BenchmarkReanchor's two span arms re-anchor into rewritten.md, which is what
// the golden corpus does.
func BenchmarkReanchor(b *testing.B) {
	original, rewritten, comments, _ := loadCorpus(b)

	anchors := make([]Anchor, 0, len(comments))
	for _, c := range comments {
		anchors = append(anchors, Anchor{HeadingPath: c.HeadingPath, Span: c.Span})
	}
	// Reported rather than asserted at a fixed count: pinning it here would be
	// a second, weaker copy of TestCorpusMatchesGolden's count check.
	fuzzy := 0
	for _, a := range anchors {
		if Reanchor(a, rewritten, nil).Status == StatusFuzzy {
			fuzzy++
		}
	}
	b.Logf("fixtures: %d anchors, %d landing fuzzy; original %d bytes, rewritten %d bytes, %d sections",
		len(anchors), fuzzy, len(original), len(rewritten), len(ParseSections(rewritten)))
	if fuzzy == 0 {
		b.Fatal("fixtures: no anchor lands fuzzy, so this arm measures none of the fuzzy-search path it is meant to measure")
	}

	b.Run("fixtures", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for _, a := range anchors {
				Reanchor(a, rewritten, nil)
			}
		}
	})

	large := largeSpanAnchor(b, original, rewritten)
	b.Logf("largespan: %d runes, %d tokens, path %v",
		len([]rune(large.Span)), len(strings.Fields(large.Span)), large.HeadingPath)

	b.Run("largespan", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			Reanchor(large, rewritten, nil)
		}
	})

	// The cheap path, measured in the same sitting rather than quoted from
	// another one, because the claim is a RATIO. An exact hit re-anchors the
	// span it was built from.
	exactSpan := Normalize(strings.Join(strings.Fields(large.Span), " "))
	exact := Anchor{HeadingPath: large.HeadingPath, Span: exactSpan}
	if got := Reanchor(exact, original, nil).Status; got != StatusExact {
		b.Fatalf("exact arm lands %s, not %s", got, StatusExact)
	}
	b.Run("exact", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			Reanchor(exact, original, nil)
		}
	})
}

package reanchor

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
)

type goldenCandidate struct {
	HeadingPath []string `json:"headingPath"`
	Text        string   `json:"text"`
	Score       float64  `json:"score"`
}

type goldenResult struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	HeadingPath []string          `json:"headingPath"`
	Confidence  float64           `json:"confidence"`
	MatchedText string            `json:"matchedText"`
	Candidates  []goldenCandidate `json:"candidates"`
}

type fixtureComment struct {
	ID          string   `json:"id"`
	HeadingPath []string `json:"headingPath"`
	Span        string   `json:"span"`
}

const floatTolerance = 1e-12

// loadCorpus reads the four corpus fixtures (see CORPUS.md). It takes
// testing.TB rather than *testing.T so BenchmarkReanchor reads the SAME four
// files through the SAME reader; a second loader would be a second answer to
// "what is the corpus".
func loadCorpus(t testing.TB) (original, rewritten string, comments []fixtureComment, golden []goldenResult) {
	t.Helper()
	read := func(name string) []byte {
		data, err := os.ReadFile("fixtures/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	original = string(read("original.md"))
	rewritten = string(read("rewritten.md"))
	if err := json.Unmarshal(read("comments.json"), &comments); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(read("golden-corpus.json"), &golden); err != nil {
		t.Fatal(err)
	}
	return original, rewritten, comments, golden
}

// TestCorpusMatchesGolden holds the package to golden-corpus.json exactly:
// each comment's status, and either its heading path, matched text and
// confidence, or an orphan's candidates in order with their paths, text and
// scores. A failure here is a behaviour change; CORPUS.md says when
// regenerating the golden file is the right answer and when it is not.
func TestCorpusMatchesGolden(t *testing.T) {
	original, rewritten, comments, golden := loadCorpus(t)
	if len(golden) != len(comments) {
		t.Fatalf("golden has %d entries for %d comments", len(golden), len(comments))
	}
	goldenByID := map[string]goldenResult{}
	for _, g := range golden {
		goldenByID[g.ID] = g
	}

	for _, c := range comments {
		t.Run(c.ID, func(t *testing.T) {
			want := goldenByID[c.ID]
			var anchor Anchor
			var err error
			if c.Span == "" {
				anchor, err = CreateSectionAnchor(original, c.HeadingPath)
			} else {
				anchor, err = CreateAnchor(original, c.Span, c.HeadingPath)
			}
			if err != nil {
				t.Fatalf("building anchor: %v", err)
			}
			got := Reanchor(anchor, rewritten, nil)

			if string(got.Status) != want.Status {
				t.Fatalf("status = %s, want %s", got.Status, want.Status)
			}
			if want.Status == "orphaned" {
				if len(got.Candidates) != len(want.Candidates) {
					t.Fatalf("candidates = %d, want %d", len(got.Candidates), len(want.Candidates))
				}
				for i, wc := range want.Candidates {
					gc := got.Candidates[i]
					if !pathsEqual(gc.HeadingPath, wc.HeadingPath) {
						t.Fatalf("candidate %d path = %v, want %v", i, gc.HeadingPath, wc.HeadingPath)
					}
					if gc.Text != wc.Text {
						t.Fatalf("candidate %d text = %q, want %q", i, gc.Text, wc.Text)
					}
					if math.Abs(gc.Score-wc.Score) > floatTolerance {
						t.Fatalf("candidate %d score = %v, want %v", i, gc.Score, wc.Score)
					}
				}
				return
			}
			if !pathsEqual(got.Anchor.HeadingPath, want.HeadingPath) {
				t.Fatalf("path = %v, want %v", got.Anchor.HeadingPath, want.HeadingPath)
			}
			if got.MatchedText != want.MatchedText {
				t.Fatalf("matchedText = %q, want %q", got.MatchedText, want.MatchedText)
			}
			if math.Abs(got.Confidence-want.Confidence) > floatTolerance {
				t.Fatalf("confidence = %v, want %v", got.Confidence, want.Confidence)
			}
		})
	}
}

// TestCorpusAggregate tracks the 12 span comments' known attach/orphan split
// as a regression guard. Section comments have their own dedicated
// attach/orphan reporting in TestSectionAnchorCorpusContract, so they're
// skipped here to avoid duplicating that coverage.
func TestCorpusAggregate(t *testing.T) {
	original, rewritten, comments, _ := loadCorpus(t)
	attached, orphaned := 0, 0
	var report []string
	for _, c := range comments {
		if c.Span == "" {
			continue
		}
		anchor, err := CreateAnchor(original, c.Span, c.HeadingPath)
		if err != nil {
			t.Fatal(err)
		}
		result := Reanchor(anchor, rewritten, nil)
		if result.Status == StatusOrphaned {
			orphaned++
			report = append(report, c.ID+" orphaned")
		} else {
			attached++
			report = append(report, c.ID+" "+string(result.Status)+" @ "+strings.Join(result.Anchor.HeadingPath, " > "))
		}
	}
	t.Log("\n" + strings.Join(report, "\n"))
	if attached != 9 || orphaned != 3 {
		t.Fatalf("corpus = %d attached / %d orphaned, want 9/3", attached, orphaned)
	}
}

// sectionGroundTruth is the destination each s-comment's section moved to in
// the rewrite. The contract is threshold-agnostic: attach exactly there, or
// orphan with it among the candidates. Attaching anywhere else is a failure.
var sectionGroundTruth = map[string][]string{
	"s1": {"Meridian Station Data: Calibration-Aware Readings and Tiered Retention", "Ground rules"},
	"s2": {"Execution plan", "Phase 1 — Schema", "Sensors, channels, and placement"},
	"s3": {"Meridian Station Data: Calibration-Aware Readings and Tiered Retention", "The batch model"},
	"s4": {"Execution plan", "Exit criteria"},
	"s5": {"Meridian Station Data: Calibration-Aware Readings and Tiered Retention", "Out of scope for v1 (tracked)"},
}

// structurallyUndiscoverable allowlists section comments whose ground-truth
// destination heading-path similarity cannot rank: retitles with no words in
// common, where coincidental bigrams decide where the destination ranks and
// unrelated headings outrank it, so keeping more candidates is no remedy.
// Only these IDs get the log-not-fail relaxation in
// TestSectionAnchorCorpusContract's orphan branch, and adding one requires a
// verified similarity ranking showing unrelated headings outranking the true
// destination, documented in CORPUS.md.
var structurallyUndiscoverable = map[string]bool{
	"s1": true, // "Design decisions (settled — not open questions)" → "Ground rules"
	"s4": true, // "Verification" → "Exit criteria"
}

// TestSectionAnchorCorpusContract asserts the one thing a path-only section
// anchor must never do: attach confidently to the wrong place. Wrong
// attachments always fail. Orphans must carry the ground-truth destination
// among their candidates, except for the structurallyUndiscoverable IDs
// above, whose miss is logged rather than failed. See the matching CORPUS.md
// limitation.
func TestSectionAnchorCorpusContract(t *testing.T) {
	original, rewritten, comments, _ := loadCorpus(t)
	for _, c := range comments {
		if c.Span != "" {
			continue
		}
		want, ok := sectionGroundTruth[c.ID]
		if !ok {
			t.Fatalf("no ground truth registered for section comment %s", c.ID)
		}
		t.Run(c.ID, func(t *testing.T) {
			anchor, err := CreateSectionAnchor(original, c.HeadingPath)
			if err != nil {
				t.Fatalf("CreateSectionAnchor: %v", err)
			}
			result := Reanchor(anchor, rewritten, nil)
			if result.Status == StatusOrphaned {
				found := false
				for _, cand := range result.Candidates {
					if pathsEqual(cand.HeadingPath, want) {
						found = true
						break
					}
				}
				t.Logf("%s orphaned; ground-truth destination %v among candidates: %v", c.ID, want, found)
				if !found && !structurallyUndiscoverable[c.ID] {
					t.Fatalf("%s orphaned without ground-truth destination %v among its candidates", c.ID, want)
				}
				return
			}
			t.Logf("%s attached %s @ %v", c.ID, result.Status, result.Anchor.HeadingPath)
			if !pathsEqual(result.Anchor.HeadingPath, want) {
				t.Fatalf("%s attached to %v, want %v (wrong confident attachment)", c.ID, result.Anchor.HeadingPath, want)
			}
		})
	}
}

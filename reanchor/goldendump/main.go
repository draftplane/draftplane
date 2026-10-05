// Command goldendump regenerates reanchor/fixtures/golden-corpus.json from
// this package. Run from the repo root (or point -fixtures elsewhere):
//
//	go run ./reanchor/goldendump
//
// See reanchor/CORPUS.md: never edit the golden file by hand; regenerate it
// from this package whenever the anchor model deliberately changes, and call
// out the behavioral change in the commit message.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/draftplane/draftplane/reanchor"
)

type fixtureComment struct {
	ID          string   `json:"id"`
	HeadingPath []string `json:"headingPath"`
	Span        string   `json:"span"`
}

type goldenCandidate struct {
	HeadingPath []string `json:"headingPath"`
	Text        string   `json:"text"`
	Score       float64  `json:"score"`
}

type goldenEntry struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	HeadingPath []string          `json:"headingPath,omitempty"`
	Confidence  float64           `json:"confidence,omitempty"`
	MatchedText string            `json:"matchedText,omitempty"`
	Candidates  []goldenCandidate `json:"candidates,omitempty"`
}

func main() {
	dir := flag.String("fixtures", "reanchor/fixtures", "path to the fixtures directory")
	flag.Parse()

	original := string(readFile(filepath.Join(*dir, "original.md")))
	rewritten := string(readFile(filepath.Join(*dir, "rewritten.md")))

	var comments []fixtureComment
	if err := json.Unmarshal(readFile(filepath.Join(*dir, "comments.json")), &comments); err != nil {
		log.Fatalf("parsing comments.json: %v", err)
	}

	entries := make([]goldenEntry, len(comments))
	for i, c := range comments {
		var anchor reanchor.Anchor
		var err error
		if c.Span == "" {
			anchor, err = reanchor.CreateSectionAnchor(original, c.HeadingPath)
		} else {
			anchor, err = reanchor.CreateAnchor(original, c.Span, c.HeadingPath)
		}
		if err != nil {
			log.Fatalf("comment %s: building anchor: %v", c.ID, err)
		}

		result := reanchor.Reanchor(anchor, rewritten, nil)
		entry := goldenEntry{ID: c.ID, Status: string(result.Status)}
		if result.Status == reanchor.StatusOrphaned {
			entry.Candidates = make([]goldenCandidate, len(result.Candidates))
			for j, cand := range result.Candidates {
				entry.Candidates[j] = goldenCandidate{
					HeadingPath: cand.HeadingPath,
					Text:        cand.Text,
					Score:       cand.Score,
				}
			}
		} else {
			entry.HeadingPath = result.Anchor.HeadingPath
			entry.Confidence = result.Confidence
			entry.MatchedText = result.MatchedText
		}
		entries[i] = entry
	}

	out, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		log.Fatalf("marshaling golden corpus: %v", err)
	}
	out = append(out, '\n')

	dest := filepath.Join(*dir, "golden-corpus.json")
	if err := os.WriteFile(dest, out, 0o644); err != nil {
		log.Fatalf("writing %s: %v", dest, err)
	}
	fmt.Printf("wrote %d entries to %s\n", len(entries), dest)
}

func readFile(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("reading %s: %v", path, err)
	}
	return data
}

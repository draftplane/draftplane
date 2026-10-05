package reanchor

import (
	"strings"
	"testing"
)

const parserDoc = "# Payments Plan\n\nIntro paragraph.\n\n## Context\n\nRefunds are manual today.\n\n## Rollout\n\nShip behind a flag.\n\n```bash\n# this is a comment inside a fence, not a heading\nflag enable payments\n```\n\n## Verification\n\nRefund a test charge.\n"

func pathStrings(sections []Section) []string {
	out := make([]string, len(sections))
	for i, s := range sections {
		out[i] = strings.Join(s.HeadingPath, " > ")
	}
	return out
}

func TestParseSectionsHeadingPaths(t *testing.T) {
	paths := pathStrings(ParseSections(parserDoc))
	for _, want := range []string{"Payments Plan > Context", "Payments Plan > Rollout"} {
		found := false
		for _, p := range paths {
			if p == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing path %q in %v", want, paths)
		}
	}
	for _, p := range paths {
		if strings.Contains(p, "this is a comment") {
			t.Fatalf("fenced heading leaked into paths: %v", paths)
		}
	}
}

func TestParseSectionsFencedContentStaysInOwningSection(t *testing.T) {
	var rollout *Section
	sections := ParseSections(parserDoc)
	for i := range sections {
		if sections[i].Title == "Rollout" {
			rollout = &sections[i]
		}
	}
	if rollout == nil || !strings.Contains(rollout.Content, "flag enable payments") {
		t.Fatal("fenced content missing from Rollout section")
	}
}

func TestParseSectionsNesting(t *testing.T) {
	sections := ParseSections("# A\n\n## B\n\ntext b\n\n### C\n\ntext c\n\n## D\n\ntext d\n")
	byTitle := map[string][]string{}
	for _, s := range sections {
		byTitle[s.Title] = s.HeadingPath
	}
	if got := strings.Join(byTitle["C"], "/"); got != "A/B/C" {
		t.Fatalf("C path = %q, want A/B/C", got)
	}
	if got := strings.Join(byTitle["D"], "/"); got != "A/D" {
		t.Fatalf("D path = %q, want A/D", got)
	}
}

func TestParseSectionsFenceRules(t *testing.T) {
	cases := []struct {
		name, doc string
		sections  int
		contains  string
	}{
		{
			"three backticks inside four-backtick fence stays content",
			"# A\n\n````md\n```\n# not a heading\n```\n````\n\nafter\n",
			1, "# not a heading",
		},
		{
			"closing fence with info string does not close",
			"# A\n\n```\n```js\n# not a heading\n```\n\nafter\n",
			1, "# not a heading",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sections := ParseSections(tc.doc)
			if len(sections) != tc.sections {
				t.Fatalf("got %d sections, want %d", len(sections), tc.sections)
			}
			if !strings.Contains(sections[0].Content, tc.contains) {
				t.Fatalf("section content missing %q", tc.contains)
			}
		})
	}
}

func TestParseSectionsHeadingClosingSequence(t *testing.T) {
	sections := ParseSections("# Migrating to F#\n\nbody\n\n# Title #\n\nbody 2\n")
	if sections[0].Title != "Migrating to F#" {
		t.Fatalf("title = %q, want %q", sections[0].Title, "Migrating to F#")
	}
	if sections[1].Title != "Title" {
		t.Fatalf("title = %q, want %q", sections[1].Title, "Title")
	}
}

func TestParseSectionsPreamble(t *testing.T) {
	sections := ParseSections("preamble text\n\n# A\n\nbody\n")
	if len(sections[0].HeadingPath) != 0 || !strings.Contains(sections[0].Content, "preamble text") {
		t.Fatal("preamble before the first heading must become an empty-path section")
	}
}

// TestSectionContentStartLocatesContent proves Section.ContentStart against the
// document it was parsed from: doc[ContentStart:] must begin with exactly that
// section's Content.
//
// ParseSections discards some sections — push() declines a run of blank lines
// under no heading — while still having consumed their bytes, so the documents
// here include preambles, blank runs, fences and nesting, where a counter
// tracking "bytes emitted" instead of "position in the document" would drift.
//
// The LAST section of EVERY document carries one "\n" more than the document
// does, so it is required to be the tail with at most that one newline added
// while every other section must match its slice exactly.
func TestSectionContentStartLocatesContent(t *testing.T) {
	original, rewritten, _, _ := loadCorpus(t)
	docs := []struct{ name, doc string }{
		{"fixture original.md", original},
		{"fixture rewritten.md", rewritten},
		{"parser doc", parserDoc},
		{"preamble before first heading", "preamble text\n\n# A\n\nbody\n"},
		{"nesting", "# A\n\n## B\n\ntext b\n\n### C\n\ntext c\n\n## D\n\ntext d\n"},
		{"fenced code", "# A\n\n````md\n```\n# not a heading\n```\n````\n\nafter\n"},
		{"blank section between headings", "# A\n\n## B\n\n\n\n## C\n\ntext c\n"},
		{"no trailing newline", "# A\n\nbody with no final newline"},
		{"heading is the last line", "# A\n\nbody\n\n## B"},
		{"non-ascii", "# \u00c5\n\nbody \u2014 with an em dash and a\u00a0non-breaking space\n"},
		{"empty", ""},
		{"blank lines only", "\n\n\n"},
	}
	checked := 0
	for _, tc := range docs {
		t.Run(tc.name, func(t *testing.T) {
			sections := ParseSections(tc.doc)
			for i, s := range sections {
				if s.ContentStart < 0 || s.ContentStart > len(tc.doc) {
					t.Fatalf("section %d %v: ContentStart = %d, outside [0,%d]",
						i, s.HeadingPath, s.ContentStart, len(tc.doc))
				}
				// Compared against the tail rather than a fixed-length slice:
				// the last section's Content is longer than what remains, so
				// slicing by len(Content) runs out of range.
				tail := tc.doc[s.ContentStart:]
				if i < len(sections)-1 {
					if !strings.HasPrefix(tail, s.Content) {
						t.Fatalf("section %d %v: doc[%d:] = %q..., Content = %q",
							i, s.HeadingPath, s.ContentStart, truncateRunes(tail, 60), s.Content)
					}
					checked++
					continue
				}
				if s.Content != tail && s.Content != tail+"\n" {
					t.Fatalf("last section %v: doc[%d:] = %q, Content = %q",
						s.HeadingPath, s.ContentStart, tail, s.Content)
				}
				checked++
			}
		})
	}
	// Non-vacuity: the fixtures alone carry tens of sections, so a loop that
	// checked a handful would mean ParseSections stopped emitting them.
	const minSections = 50
	if checked < minSections {
		t.Fatalf("checked %d sections, want at least %d — the sample went vacuous", checked, minSections)
	}
	t.Logf("%d sections located across %d documents", checked, len(docs))
}

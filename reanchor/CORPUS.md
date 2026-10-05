# reanchor — the golden corpus

`fixtures/` holds a golden corpus that pins this package's behaviour to exact
results:

- `original.md`, a realistic technical plan, and `rewritten.md`, an aggressive
  independent rewrite of it (retitled, restructured, sections moved, merged
  and deleted);
- `comments.json`, 17 comments on `original.md`: span comments `c1`–`c12` and
  section comments `s1`–`s5` (an empty `span`);
- `golden-corpus.json`, what this package returns for each comment
  re-anchored into `rewritten.md`: the status, and either the heading path,
  matched text and confidence, or an orphan's candidates with their scores.

`corpus_test.go`'s `TestCorpusMatchesGolden` holds the package to the golden
file exactly, floats to ±1e-12. Other tests read the documents too:
`TestCorpusAggregate` counts the span comments' attach/orphan split,
`TestSectionAnchorCorpusContract` checks where each section comment lands,
`TestLargeSpanReanchorPin` and `BenchmarkReanchor` use a 60-word passage of
`original.md`, and several sweeps use both documents as a large sample.

## Rules

- Never edit `golden-corpus.json` by hand, and never fix a corpus failure by
  changing it.
- When behaviour changes deliberately, change the implementation and its unit
  tests first, then regenerate the golden file from this package with
  `go run ./reanchor/goldendump` (run from the repo root) in the same commit,
  and call out the behavioural change in the commit message.
- After regenerating, diff the file. Every entry the change was not meant to
  affect must be byte-unchanged; a diff touching one means the change altered
  more than intended, and must be found and fixed before it is accepted.
- Changing the documents or the comments changes every entry they feed.
  Regenerate, then re-establish from the new text what the tests pin: the
  status mix, each section comment's destination, the largespan passage and
  its pinned result.
- A second implementation, in any language, proves conformance by
  reproducing this golden file.

## What conformance requires

These choices decide the golden values, so a second implementation must make
the same ones:

- **Runes.** A span's length (against `ShortSpanChars`) and every bigram are
  counted in Unicode code points, not bytes or UTF-16 code units. Window
  widths are counted in whitespace-separated words, and each window is then
  scored over its runes.
- **Whitespace** is `unicode.IsSpace`, as `strings.Fields` splits.
- **Case folding** is `unicode.ToLower` applied rune by rune, with no
  locale-specific mappings (no Turkish dotted I).
- **Stable sorts everywhere.** Candidate ordering and ambiguity decisions
  depend on stability.
- **Every floating-point operation rounds on its own.** Explicit `float64`
  conversions in the weighted sums forbid FMA fusion, so scores are identical
  across architectures.
- **Heading syntax** is matched with RE2, whose `\s` is ASCII-only.

## Known algorithmic limitations

- Confidence is a similarity score, not a calibrated probability.
- A single surviving near-twin of a deleted target attaches confidently and
  wrongly; that failure mode is deferred to the agent-fallback tier. It
  applies to section anchors too, with a twist: because path similarity is
  0.7·leaf + 0.3·full-path, the effective leaf floor erodes as common parent
  depth grows, so a similarly spelled sibling ("Rollout" → "Rollback") can
  clear the floor at a depth where its leaf similarity alone would not. A
  future hardening option is an independent leaf-similarity floor.
- Two distinct sections with identical heading paths are indistinguishable to
  path-based identity: they collapse to one candidate, and the first wins.
- A section anchor (`CreateSectionAnchor`, relocated by `reanchorSection`) has
  only its heading path. Span anchors on heading text are impossible by
  design: heading text is the coordinate system, deliberately excluded from
  section content.
- A heading retitled with no words in common is therefore structurally
  undiscoverable, even when the section's content is a faithful paraphrase.
  It orphans without its true destination among the candidates, and keeping
  more candidates is no remedy: coincidental bigrams decide where the
  destination ranks, and unrelated headings outrank it. Corpus entries `s1`
  and `s4` demonstrate this (`Design decisions (settled — not open
  questions)` → `Ground rules`; `Verification` → `Exit criteria`): ranked by
  `HeadingPathSimilarity` against all 29 headings of `rewritten.md`, their
  true destinations come 10th and 9th. `TestSectionAnchorCorpusContract`
  logs rather than fails on this miss, since it is not a wrong attachment,
  and only a wrong attachment is a bug for this tier.

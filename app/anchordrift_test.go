package app

// This file is the anchor-drift defect stated on the SCREEN: a thread the
// re-anchorer ACCEPTED is drawn on the block where it was accepted.
//
// The case is ui/testdata/anchor-drift.md and its four stored anchors. All
// four come back reanchor.StatusFuzzy at 0.881-0.890 -- accepted by a gate
// requiring a 0.65 score and an unconditional 0.05 margin over the best
// rival elsewhere -- and every one of them used to render under "unanchored"
// because ui.ResolveAnchor, rebuilding a location from the matched TEXT,
// answers -1 for a window that crosses a ui.Block boundary. The status bar
// counted four threads and n reached none of them.
//
// BLOCKS ARE IDENTIFIED BY THEIR TEXT, NEVER BY INDEX: asserting "block 42"
// would pin ui.ParseBlocks' numbering, which is not this fixture's subject
// and which a paragraph added above them would break.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/placement"
	"github.com/draftplane/draftplane/reanchor"
	"github.com/draftplane/draftplane/session"
	"github.com/draftplane/draftplane/store/localcas"
	"github.com/draftplane/draftplane/ui"
)

// storedAnchor is one record of ui/testdata/anchor-drift-anchors.json: the
// four anchors as they were stored against the version BEFORE the edits.
type storedAnchor struct {
	ID          string   `json:"id"`
	HeadingPath []string `json:"headingPath"`
	Span        string   `json:"span"`
	Resolved    bool     `json:"resolved"`
	Comment     string   `json:"comment"`
}

// The two panels the four anchors matched, by the first words of each code
// block. The missing-file panel takes threads 1 and 3 of the fixture, the
// unreadable-file panel threads 2 and 4; the badges differ because the two
// windows score differently, and pinning them is what says the matcher's own
// confidence reaches the reader.
const (
	missingFileOpening    = "The file this plan follows is gone."
	unreadableFileOpening = "This plan's file is there but could not be read."
	missingFileBadge      = "fuzzy ~88%"
	unreadableFileBadge   = "fuzzy ~89%"
)

// anchorDriftFixture reads ui/testdata across the package boundary: `go
// test` runs with the package directory as the working directory, so the
// relative path is stable, and one copy of the fixture is better than two
// that can drift.
//
// It rebuilds the four threads through a real session rather than hand-
// assembling domain.Thread values, so the whole route a reader travels --
// CreateThread, session.Placements, placement.PlaceThreads, project -- is
// what the assertions run over. The store mints its own thread IDs, so every
// assertion below identifies a thread by its comment BODY, which the four do
// not share.
func anchorDriftFixture(t *testing.T) fixture {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "ui", "testdata", "anchor-drift.md"))
	if err != nil {
		t.Fatalf("reading the shared anchor-drift fixture: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "ui", "testdata", "anchor-drift-anchors.json"))
	if err != nil {
		t.Fatalf("reading the anchor-drift anchors: %v", err)
	}
	var anchors []storedAnchor
	if err := json.Unmarshal(raw, &anchors); err != nil {
		t.Fatalf("decoding the anchor-drift anchors: %v", err)
	}
	if len(anchors) != 4 {
		t.Fatalf("the fixture holds %d anchors, want the fixture's four", len(anchors))
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "anchor-drift.md")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}
	f := fixture{
		svc:  newFixtureStore(t, filepath.Join(dir, "state.json")),
		cas:  localcas.New(filepath.Join(dir, "objects")),
		path: path,
		ctx:  attrCtx("alice"),
	}
	s, err := session.Open(f.ctx, f.svc, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(f.ctx, "Missing and unreadable plan files"); err != nil {
		t.Fatal(err)
	}
	for _, a := range anchors {
		th, err := s.Comment(f.ctx, reanchor.Anchor{HeadingPath: a.HeadingPath, Span: a.Span}, a.Comment)
		if err != nil {
			t.Fatalf("seeding %s: %v", a.ID, err)
		}
		// All four are resolved, which is not incidental: resolved is what
		// excludes a thread from unresolvedOrphans, so it removed the only
		// affordance that would have surfaced them.
		if a.Resolved {
			if err := s.Resolve(f.ctx, th.ID, true); err != nil {
				t.Fatalf("resolving %s: %v", a.ID, err)
			}
		}
	}
	return f
}

// blockOpening is the index of the one block whose source text begins with
// prefix. It fails rather than returning -1: an absent block is a broken
// fixture, not a case to branch on.
func blockOpening(t *testing.T, blocks []ui.Block, prefix string) int {
	t.Helper()
	found := -1
	for i, b := range blocks {
		if !strings.HasPrefix(b.Text, prefix) {
			continue
		}
		if found >= 0 {
			t.Fatalf("two blocks open with %q (%d and %d): the fixture no longer identifies one block by that text", prefix, found, i)
		}
		found = i
	}
	if found < 0 {
		t.Fatalf("no block opens with %q", prefix)
	}
	return found
}

// proseUnder is the index of the one non-heading block in the section whose
// heading path is path. The document this serves has two paragraphs with
// identical text, so the section is the only thing that tells them apart.
func proseUnder(t *testing.T, blocks []ui.Block, path []string) int {
	t.Helper()
	found := -1
	for i, b := range blocks {
		if b.Kind == ui.KindHeading || !slices.Equal(b.HeadingPath, path) {
			continue
		}
		if found >= 0 {
			t.Fatalf("section %v holds more than one non-heading block (%d and %d)", path, found, i)
		}
		found = i
	}
	if found < 0 {
		t.Fatalf("no non-heading block sits under %v", path)
	}
	return found
}

// bodies maps each of a block's threads to its badge, keyed by the thread's
// first comment body. A map because the order threads come back in is the
// store's, and nothing here is a claim about it.
func bodies(views []ui.ThreadView) map[string]string {
	out := map[string]string{}
	for _, v := range views {
		out[v.Thread.Comments[0].Body] = v.Badge
	}
	return out
}

// The four threads of ui/testdata/anchor-drift.md land on the two code
// blocks their windows overlap, carrying the confidence the matcher committed
// to, and NOTHING is left in the unanchored section.
//
// Two mutations it discriminates:
//
//   - route the fuzzy arm back through ui.ResolveAnchor and all four return
//     to unanchored -- the panels hold 0 threads each and len(m.unanchored)
//     is 4.
//   - swap blockOfGreatestOverlap for the block containing the window's
//     FIRST BYTE and two land nowhere while two land on the
//     "**Panel for an unreadable file -- ...:**" LABEL paragraph -- both
//     panels hold 0 threads and len(m.unanchored) is 2. That mutation is
//     why the fixture keeps the two panels' exact text and the label
//     paragraph between them.
func TestFuzzyThreadsLandOnThePanelTheMatcherMatched(t *testing.T) {
	m := openModel(t, anchorDriftFixture(t))

	// Errorf, not Fatalf: the per-panel cases below say WHERE each thread
	// went, which is the difference between the two wrong answers.
	if len(m.unanchored) != 0 {
		t.Errorf("%d of the four threads are unanchored; the matcher located every one of them", len(m.unanchored))
	}

	for _, tc := range []struct {
		name    string
		opening string
		badge   string
		want    []string
	}{
		{
			name:    "the missing-file panel's code block",
			opening: missingFileOpening,
			badge:   missingFileBadge,
			want: []string{
				`Shorten this to "Draftplane still has the plan, its comment threads, and the most recent version."`,
				`Say which list: "esc to go back to the plan list".`,
			},
		},
		{
			name:    "the unreadable-file panel's code block",
			opening: unreadableFileOpening,
			badge:   unreadableFileBadge,
			want: []string{
				`Cut the last sentence here so the paragraph is just "The file is still on disk; only reading it failed."`,
				`Same here: "esc to go back to the plan list", so both panels name the list the same way.`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idx := blockOpening(t, m.blocks, tc.opening)
			got := bodies(m.views[idx])
			for _, body := range tc.want {
				badge, ok := got[body]
				if !ok {
					t.Errorf("thread %q is not on the block opening %q; it is on %s", body, tc.opening, whereIs(m, body))
					continue
				}
				if badge != tc.badge {
					t.Errorf("thread %q carries badge %q, want %q", body, badge, tc.badge)
				}
			}
			if len(got) != len(tc.want) {
				t.Errorf("the block opening %q carries %d threads, want %d: %v", tc.opening, len(got), len(tc.want), got)
			}
			for _, v := range m.views[idx] {
				if !v.Placed {
					t.Errorf("thread %q is drawn on a block but not marked Placed", v.Thread.Comments[0].Body)
				}
			}
		})
	}
}

// whereIs says where a thread actually ended up, for a failure message: the
// two wrong answers this test discriminates (unanchored, or the panel LABEL)
// are told apart by exactly this.
func whereIs(m *Model, body string) string {
	for _, v := range m.unanchored {
		if v.Thread.Comments[0].Body == body {
			return "the unanchored section"
		}
	}
	for idx, vs := range m.views {
		for _, v := range vs {
			if v.Thread.Comments[0].Body == body {
				return "the block " + strconv.Quote(truncate(m.blocks[idx].Text, 50))
			}
		}
	}
	return "no block and not unanchored -- it reached the projection at all?"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// The invariant the defect broke: the status bar's thread count equals the
// number of threads n reaches walking from the top of the document until it
// stops. It was 4 counted and 0 reachable.
//
// EVERY CARD IS OPENED FIRST, and that is a statement about jumpThread rather
// than a convenience: n on a COLLAPSED block walks block to block
// (visibleThreads answers 0 there, so the within-block arm never runs), so a
// second thread on a collapsed card is not an n stop at all, by design. The
// question here is whether n reaches the threads, not what card state it
// leaves them in.
func TestEveryCountedThreadIsReachableByN(t *testing.T) {
	m := openModel(t, anchorDriftFixture(t))
	for i := range m.blocks {
		m.expanded[i] = true
	}
	m = press(m, "g")
	if m.cursor != 0 {
		t.Fatalf("g left the cursor on block %d, so this walk does not start at the top", m.cursor)
	}

	// THE WALK IS A CYCLE AND ITS CLOSING IS THE TERMINATION CONDITION. n wraps,
	// so there is no refusal to stop on and no fixed point to detect -- what ends
	// the traversal is arriving back at the thread it started from, and reaching
	// every thread exactly once on the way is the property under test. A body seen
	// twice BEFORE the cycle closes is still the failure it always was: it means
	// the walk short-cycles over a subset rather than covering the document.
	m = press(m, "n")
	start := nAndNCycle(m)

	var reached []string
	seen := map[string]bool{}
	closed := false
	for step := 0; step <= m.threadCount(); step++ {
		v, ok := m.selectedThread()
		if !ok {
			t.Fatalf("n moved to block %d thread %d, where no thread is selected", m.cursor, m.selectedIndex(m.cursor))
		}
		body := v.Thread.Comments[0].Body
		if seen[body] {
			t.Fatalf("n returned to %q before the cycle closed; the walk covers a subset, not the document", body)
		}
		seen[body] = true
		reached = append(reached, body)
		if m = press(m, "n"); nAndNCycle(m) == start {
			closed = true
			break
		}
	}
	if !closed {
		t.Fatalf("n never returned to the thread it started on after %d threads; the walk is not a cycle: %v", m.threadCount(), reached)
	}

	if got := m.threadCount(); len(reached) != got {
		t.Fatalf("the status bar counts %d threads and n reaches %d of them (%v)", got, len(reached), reached)
	}
	if len(reached) != 4 {
		t.Fatalf("n reached %d threads, want the fixture's four: %v", len(reached), reached)
	}
}

// The routing rule: a placement that REPORTED A WINDOW is drawn on the block
// that window overlaps most, and a placement that reported none -- a section
// anchor, which has no span and therefore no position -- keeps
// ui.ResolveAnchor.
//
// THE FIRST ROW IS THE ONE TO WORRY ABOUT: the span occurs twice, word for
// word, so its TEXT cannot say which block it means and a FIRST-occurrence
// offset would answer Alpha. It still lands under Beta, because the offset
// carried is the one the heading-path ranking selected -- the same
// disambiguation ui.ResolveAnchor was doing, made at the arm that has the
// evidence for it.
//
// THE TWO CROSS-BLOCK ROWS ARE THE DEFECT, and they are ordinary input rather
// than a construction: mcptools' comment and rehome hand an agent's free-form
// quote to session.AnchorForBlockText with no block-boundary rule, and
// reanchor.Normalize collapses newlines, so a quote spanning a paragraph
// break -- or a fenced block and the prose after it -- matches a SECTION
// perfectly at StatusExact/confidence 1 and then contains in no single BLOCK.
// Both are built by the real reanchor.CreateAnchor for that reason. Routing
// exact and moved back through ui.ResolveAnchor reddens exactly these two and
// nothing else in the tree.
//
// THE SECTION ROW IS WHY THE DISCRIMINATOR IS THE RANGE AND NOT Status. A
// section anchor comes back StatusExact, indistinguishable by status from the
// first row, and it has nothing to locate: only its empty range says so, and
// routing its 0..0 through blockOfGreatestOverlap would answer -1 and file it
// under "unanchored".
func TestPlacementsRouteByLocationNotByStatus(t *testing.T) {
	const doc = `# Rate Limiter Plan

## Alpha

The token bucket refills at a steady rate of fifty per second.

## Beta

The token bucket refills at a steady rate of fifty per second.

## Gamma

An entirely different sentence about queue depth and backpressure.

## Delta

Retries are capped at three attempts per request.

Anything beyond that is reported to the caller as a failure.

## Epsilon

` + "```" + `
budget = rate * window
` + "```" + `

The budget above is recomputed on every refill tick.
`
	blocks := ui.ParseBlocks([]byte(doc), nil)

	for _, tc := range []struct {
		name   string
		anchor func(t *testing.T) reanchor.Anchor
		want   reanchor.Status
		// located is whether reanchor reported a window -- the very predicate
		// placedBlock routes on.
		located bool
		// block is the one block the thread must be drawn on, named by
		// something the fixture guarantees is unique rather than by index.
		block func(t *testing.T, blocks []ui.Block) int
	}{
		{
			// Under Alpha is the first occurrence and the wrong answer; the
			// anchor's own heading path is the only thing that makes Beta
			// right, which is why this case is named by its section.
			name: "exact, disambiguated by heading path rather than by position",
			anchor: func(*testing.T) reanchor.Anchor {
				return reanchor.Anchor{
					HeadingPath: []string{"Rate Limiter Plan", "Beta"},
					Span:        "The token bucket refills at a steady rate of fifty per second.",
				}
			},
			want:    reanchor.StatusExact,
			located: true,
			block: func(t *testing.T, blocks []ui.Block) int {
				return proseUnder(t, blocks, []string{"Rate Limiter Plan", "Beta"})
			},
		},
		{
			name: "moved: the span survives under a heading that has been renamed",
			anchor: func(*testing.T) reanchor.Anchor {
				return reanchor.Anchor{
					HeadingPath: []string{"Rate Limiter Plan", "Vanished"},
					Span:        "An entirely different sentence about queue depth and backpressure.",
				}
			},
			want:    reanchor.StatusMoved,
			located: true,
			block: func(t *testing.T, blocks []ui.Block) int {
				return proseUnder(t, blocks, []string{"Rate Limiter Plan", "Gamma"})
			},
		},
		{
			// The window straddles the blank line between two paragraphs:
			// 37 of its 59 bytes lie in the first, 20 in the second, and
			// the 2 between them are the paragraph break, which belongs to
			// no block. Greatest overlap answers the first.
			name: "exact, over a quote crossing a paragraph break",
			anchor: func(t *testing.T) reanchor.Anchor {
				return mustCreateAnchor(t, doc, "capped at three attempts per request. Anything beyond that")
			},
			want:    reanchor.StatusExact,
			located: true,
			block: func(t *testing.T, blocks []ui.Block) int {
				return blockOpening(t, blocks, "Retries are capped")
			},
		},
		{
			// The fence's own ``` lines are outside the code block's
			// [SrcStart, SrcEnd), so of this window's 38 bytes 23 are the
			// code block -- "budget = rate * window" WITH ITS TRAILING
			// NEWLINE, which is the quantity that decides the winner rather
			// than the 22 of bare code text -- 5 are the closing fence and
			// the blank line after it, belonging to no block, and 10 are
			// the paragraph that follows. The code block wins.
			name: "exact, over a quote crossing a fenced block and the prose after it",
			anchor: func(t *testing.T) reanchor.Anchor {
				return mustCreateAnchor(t, doc, "budget = rate * window ``` The budget")
			},
			want:    reanchor.StatusExact,
			located: true,
			block: func(t *testing.T, blocks []ui.Block) int {
				return blockOpening(t, blocks, "budget = rate * window")
			},
		},
		{
			name: "a section anchor: no span, no position, still located by heading path",
			anchor: func(t *testing.T) reanchor.Anchor {
				a, err := reanchor.CreateSectionAnchor(doc, []string{"Rate Limiter Plan", "Gamma"})
				if err != nil {
					t.Fatalf("CreateSectionAnchor: %v", err)
				}
				return a
			},
			want:    reanchor.StatusExact,
			located: false,
			block: func(t *testing.T, blocks []ui.Block) int {
				return firstUnder(t, blocks, []string{"Rate Limiter Plan", "Gamma"})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pls := placement.PlaceThreads([]domain.Thread{{
				ID:       "l_t1",
				Anchor:   tc.anchor(t),
				Comments: []domain.Comment{{ID: "l_c1", Body: "a note"}},
			}}, doc)
			pl := pls[0]
			if pl.Status != tc.want {
				t.Fatalf("status = %s, want %s -- the case no longer drives the arm it names", pl.Status, tc.want)
			}
			if got := pl.MatchEnd > pl.MatchStart; got != tc.located {
				t.Fatalf("MatchEnd > MatchStart = %v (offsets %d..%d), want %v", got, pl.MatchStart, pl.MatchEnd, tc.located)
			}
			if tc.located {
				// Both ends, not just the start: an end short or long by a
				// token satisfies every one-sided check and still draws the
				// wrong region.
				if pl.MatchStart < 0 || pl.MatchEnd > len(doc) {
					t.Fatalf("offsets %d..%d are not a window into a %d-byte document", pl.MatchStart, pl.MatchEnd, len(doc))
				}
				if got := reanchor.Normalize(doc[pl.MatchStart:pl.MatchEnd]); got != pl.MatchedText {
					t.Fatalf("Normalize(doc[%d:%d]) = %q, want MatchedText %q", pl.MatchStart, pl.MatchEnd, got, pl.MatchedText)
				}
			} else if pl.MatchStart != 0 || pl.MatchEnd != 0 {
				t.Fatalf("offsets = %d..%d; a placement that located no span must carry 0..0", pl.MatchStart, pl.MatchEnd)
			}

			p := project(blocks, pls, false)
			if len(p.unanchored) != 0 {
				t.Fatalf("this %s placement went to the unanchored section, drawn on nothing", tc.want)
			}
			want := tc.block(t, blocks)
			if len(p.views[want]) != 1 {
				t.Fatalf("the block %q carries %d threads, want 1; it landed on %s instead",
					truncate(blocks[want].Text, 50), len(p.views[want]), placedElsewhere(p, want))
			}
		})
	}
}

// The one clause of blockOfGreatestOverlap's contract nothing else in this
// tree can falsify: mutating `overlap > most` to `overlap >= most &&
// overlap > 0` -- ties to the LATER block, the exact opposite of what the
// function documents -- left `go test ./...` fully green.
//
// THE RULE IS FOR DETERMINISM AND NOT FOR CORRECTNESS. Blocks never overlap
// each other, so a tie means one window is split exactly evenly between two
// of them and NOTHING makes either the better answer; what the rule buys is
// that the same document draws the same thread on the same block every time
// it is opened. Anyone with a reason to flip it to the later block has to
// change a stated decision rather than an unwatched comparison.
func TestGreatestOverlapTiesGoToTheEarlierBlock(t *testing.T) {
	const doc = `# Rate Limiter Plan

## Delta

Retries are capped at three attempts per request.

Anything beyond that is reported to the caller as a failure.
`
	blocks := ui.ParseBlocks([]byte(doc), nil)
	first := blockOpening(t, blocks, "Retries are capped")
	second := blockOpening(t, blocks, "Anything beyond that")
	if first >= second {
		t.Fatalf("the two paragraphs parse as blocks %d and %d; this test needs the earlier one first", first, second)
	}

	// A window reaching equally far back into the first block and forward
	// into the second, computed from the blocks rather than written down, so
	// that an edit to the document above cannot quietly make the split
	// uneven and leave this asserting nothing.
	const half = 12
	start, end := blocks[first].SrcEnd-half, blocks[second].SrcStart+half
	overlap := func(b ui.Block) int { return min(b.SrcEnd, end) - max(b.SrcStart, start) }
	if a, b := overlap(blocks[first]), overlap(blocks[second]); a != b || a != half {
		t.Fatalf("the window [%d,%d) overlaps blocks %d and %d by %d and %d bytes, so this is not the tie it exists to pin",
			start, end, first, second, a, b)
	}

	if got := blockOfGreatestOverlap(blocks, start, end); got != first {
		t.Errorf("a window split evenly between blocks %d and %d landed on %d; ties go to the EARLIER block", first, second, got)
	}
}

// mustCreateAnchor builds an anchor the way an agent's quote reaches the
// store -- reanchor.CreateAnchor over free-form text, which is what
// session.AnchorForBlockText calls -- so a cross-block row is a case the
// door actually admits and not a Placement assembled to order.
func mustCreateAnchor(t *testing.T, doc, quote string) reanchor.Anchor {
	t.Helper()
	a, err := reanchor.CreateAnchor(doc, quote, nil)
	if err != nil {
		t.Fatalf("CreateAnchor(%q): %v", quote, err)
	}
	return a
}

// firstUnder is the first block carrying path: what a SECTION anchor, having
// no span to locate, must still be drawn on. Stated as "the first block of
// that section" rather than as "whatever ui.ResolveAnchor answers", so the
// row asserts the outcome and not the implementation.
func firstUnder(t *testing.T, blocks []ui.Block, path []string) int {
	t.Helper()
	for i, b := range blocks {
		if slices.Equal(b.HeadingPath, path) {
			return i
		}
	}
	t.Fatalf("no block carries the heading path %v", path)
	return -1
}

// placedElsewhere names the block a projection actually used, for the failure
// message above: "not where I wanted" and "on the FIRST occurrence of a span
// that occurs twice" are the two outcomes this test exists to tell apart.
func placedElsewhere(p projection, notThis int) string {
	for idx := range p.views {
		if idx != notThis && len(p.views[idx]) > 0 {
			return "block " + strconv.Quote(truncate(p.blocks[idx].Text, 50))
		}
	}
	return "no block at all"
}

// The partition: BOTH branches of project land a thread in the unanchored
// section, and only one of them is a true orphan. keptButUndisplayable is what
// tells them apart.
//
// THE LOCATED ROW IS HAND-BUILT, deliberately and not for convenience. Under
// the greatest-overlap rule a located window reaches project's residual arm
// only by overlapping NO block anywhere, and no thread in any real store
// produces that -- blocks cover all but a sliver of a document's bytes, and
// the uncovered gaps are far shorter than the three-word span
// reanchor.CreateAnchor demands. So that row states its placement as a
// literal, over a window that IS one of those gaps (a fence's own ``` line,
// outside the code block's [SrcStart, SrcEnd)), rather than pretending to be
// a case somebody observed.
func TestKeptButUndisplayableIsNotAnOrphan(t *testing.T) {
	const doc = `# Rate Limiter Plan

## Alpha

The token bucket refills at a steady rate of fifty per second.

Requests above that rate are queued until capacity returns.

## Beta

` + "```" + `
budget = rate * window
` + "```" + `
`
	// The two documents differ in ONE thing, and the difference is what the
	// two orphan rows are for: this one opens with a paragraph before its
	// first heading, so it holds a block whose heading path is EMPTY -- which
	// is what ui.ResolveAnchor's empty-span arm answers with, and a true
	// orphan carries the ZERO Anchor. The same orphan therefore gets -1 from
	// placedBlock over doc and the preamble block over this one: placedBlock's
	// answer for an orphan is arbitrary, not merely negative, which is why
	// keptButUndisplayable cannot read the index alone.
	const preambleOpening = "A preamble paragraph standing"
	const docWithPreamble = preambleOpening + " before this document's first heading.\n\n" + doc

	vanished := reanchor.Anchor{
		HeadingPath: []string{"Rate Limiter Plan", "Vanished"},
		Span:        "words that no longer occur anywhere in this document",
	}

	for _, tc := range []struct {
		name string
		doc  string
		// place produces the row's placement the way production would,
		// except where the doc comment above says it cannot.
		place func(t *testing.T, doc string) placement.Placement
		// placedBlockOpens is the text the block placedBlock answers with
		// begins with, or "" for the -1 meaning "no block at all".
		placedBlockOpens string
		wantKept         bool
	}{
		{
			// The row that reddens if keptButUndisplayable is reduced to
			// idx < 0: this orphan's index is -1 too, so an index-only
			// predicate calls a true orphan a kept one.
			name: "a true orphan resolving to no block",
			doc:  doc,
			place: func(t *testing.T, doc string) placement.Placement {
				return placeOne(t, doc, vanished, reanchor.StatusOrphaned)
			},
			placedBlockOpens: "",
			wantKept:         false,
		},
		{
			name: "the same orphan over a document with a preamble: placedBlock answers a real block",
			doc:  docWithPreamble,
			place: func(t *testing.T, doc string) placement.Placement {
				return placeOne(t, doc, vanished, reanchor.StatusOrphaned)
			},
			placedBlockOpens: preambleOpening,
			wantKept:         false,
		},
		{
			name: "hand-built: a fuzzy window inside a gap no block covers",
			doc:  doc,
			place: func(t *testing.T, doc string) placement.Placement {
				at := strings.Index(doc, "```")
				if at < 0 {
					t.Fatal("the document no longer holds a fence, so this row has no gap to sit in")
				}
				return placement.Placement{
					Thread: domain.Thread{
						ID:       "l_hand_built",
						Comments: []domain.Comment{{ID: "l_c1", Body: "a window over the fence's own markup"}},
					},
					Status:      reanchor.StatusFuzzy,
					Confidence:  0.7,
					MatchedText: "```",
					MatchStart:  at,
					MatchEnd:    at + len("```"),
				}
			},
			placedBlockOpens: "",
			wantKept:         true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocks := ui.ParseBlocks([]byte(tc.doc), nil)
			pl := tc.place(t, tc.doc)

			idx := placedBlock(blocks, pl)
			want := -1
			if tc.placedBlockOpens != "" {
				want = blockOpening(t, blocks, tc.placedBlockOpens)
			}
			if idx != want {
				t.Fatalf("placedBlock = %d, want %d", idx, want)
			}
			if got := keptButUndisplayable(pl, idx); got != tc.wantKept {
				t.Errorf("keptButUndisplayable = %v, want %v for a %s placement", got, tc.wantKept, pl.Status)
			}

			p := project(blocks, []placement.Placement{pl}, false)
			if len(p.unanchored) != 1 || len(p.orphans) != 1 {
				t.Fatalf("unanchored = %d and orphans = %d, want 1 and 1: both branches send a thread to the unanchored section",
					len(p.unanchored), len(p.orphans))
			}
		})
	}
}

// placeOne is one thread's placement through the production route,
// asserting the status the row is about: a row that silently re-anchored
// some other way would prove nothing about the branch it names.
func placeOne(t *testing.T, doc string, a reanchor.Anchor, want reanchor.Status) placement.Placement {
	t.Helper()
	pls := placement.PlaceThreads([]domain.Thread{{
		ID:       "l_t1",
		Anchor:   a,
		Comments: []domain.Comment{{ID: "l_c1", Body: "a note"}},
	}}, doc)
	if pls[0].Status != want {
		t.Fatalf("status = %s, want %s", pls[0].Status, want)
	}
	return pls[0]
}

package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/keymap"
	"github.com/draftplane/draftplane/session"
	"github.com/draftplane/draftplane/theme"
	"github.com/draftplane/draftplane/ui"
)

// paintedTheme is production's own default path: cmd/draftplane's resolveTheme
// returns a non-nil theme for EVERY user -- "system" included, which selects
// detection rather than an unpainted rendering, so a panel missing from
// panelViewPainted is missing from EVERY real screen.
func paintedTheme(t *testing.T) *theme.Theme {
	t.Helper()
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	return th
}

// deleteHead is the head comment every test below deletes a thread by. It is
// this panel's own worked example, so the confirm strings asserted here are
// read back from it rather than a shape retyped by hand.
const deleteHead = "the deploy step needs a rollback"

// keyYes is dispatched through Update rather than through press(): press
// drops the tea.Cmd, and the whole subject here is what the write behind that
// cmd actually did.
var keyYes = tea.KeyPressMsg{Code: 'y', Text: "y"}

// addReplies posts replies through the same session.Reply an r in the TUI
// would -- the reply count is len(Comments)-1, and this is what makes the
// counts asserted below real data rather than hand-built domain.Thread values.
func addReplies(t *testing.T, f fixture, head string, bodies ...string) {
	t.Helper()
	s, err := session.Open(f.ctx, f.svc, f.path)
	if err != nil {
		t.Fatal(err)
	}
	tid := threadIDByHead(t, f, s.Plan.ID, head)
	for _, b := range bodies {
		if _, err := s.Reply(f.ctx, tid, b); err != nil {
			t.Fatal(err)
		}
	}
}

// threadIDByHead answers the id of the thread whose head comment is head: the
// one way the tests in this file name a thread, since nothing they drive knows
// an id and the head comment is what the confirm panel and the assertions
// both speak in.
func threadIDByHead(t *testing.T, f fixture, id domain.PlanID, head string) domain.ThreadID {
	t.Helper()
	threads, err := f.svc.Threads(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range threads {
		if len(th.Comments) > 0 && th.Comments[0].Body == head {
			return th.ID
		}
	}
	t.Fatalf("test setup: no thread whose head comment is %q", head)
	return ""
}

// threadHeads reads the plan's threads back out of the store and returns their
// head comments, in store order. Every assertion about what a delete destroyed
// is made against this rather than against the projection: a thread can leave
// the screen for reasons that have nothing to do with its being gone.
func threadHeads(t *testing.T, f fixture, id domain.PlanID) []string {
	t.Helper()
	threads, err := f.svc.Threads(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	heads := make([]string, 0, len(threads))
	for _, th := range threads {
		if len(th.Comments) > 0 {
			heads = append(heads, th.Comments[0].Body)
		}
	}
	return heads
}

// The wording at both boundaries: the count clause is omitted OUTRIGHT at
// zero replies rather than rendered as "including 0 replies", and one reply
// is singular. The counts are real -- session.Comment then session.Reply, the
// two calls the TUI's own c and r make -- so this pins len(Comments)-1
// against data rather than against arithmetic retyped from the plan.
func TestDeleteConfirmNamesTheThreadAndCountsItsReplies(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replies []string
		want    string
	}{
		{"no replies", nil, `Delete "` + deleteHead + `"?`},
		{"one reply", []string{"agreed"}, `Delete "` + deleteHead + `" including 1 reply?`},
		{"two replies", []string{"agreed", "on it"}, `Delete "` + deleteHead + `" including 2 replies?`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			seedThread(t, f, deleteHead)
			addReplies(t, f, deleteHead, tc.replies...)
			m := openModel(t, f)

			m = press(m, "n", "d") // n jumps to the thread's block

			if m.mode != modeConfirmDeleteThread {
				t.Fatalf("mode = %v, want modeConfirmDeleteThread; status=%q", m.mode, m.status)
			}
			if m.confirm != tc.want {
				t.Fatalf("confirm = %q, want %q", m.confirm, tc.want)
			}
			if !strings.Contains(ansi.Strip(m.View().Content), tc.want) {
				t.Fatalf("View() does not render the confirm text on screen:\n%s", ansi.Strip(m.View().Content))
			}
			// The bar names the keys this panel's handler actually answers,
			// through the one composer every y/n panel in this package uses.
			if got, want := m.helpBar(), confirmHint("delete"); got != want {
				t.Fatalf("help bar = %q, want %q", got, want)
			}
		})
	}
}

// What the excerpt is when the head comment is not one short line: its FIRST
// line, clipped to deleteExcerptRunes. The last case is one no door in this
// package can produce -- a thread with no comments at all -- so it installs
// the state directly on the model, then presses the real key. "-1 replies"
// must not be renderable.
func TestDeleteConfirmExcerptsOneClippedLine(t *testing.T) {
	long := strings.Repeat("rollback ", 20) // 180 runes, well past the clip
	for _, tc := range []struct {
		name     string
		comments []string
		want     string
	}{
		{
			"a multi-line head comment excerpts its first line only",
			[]string{deleteHead + "\nand a second paragraph nobody needs to read here"},
			`Delete "` + deleteHead + `"?`,
		},
		{
			"a long first line is clipped",
			[]string{long},
			`Delete "` + string([]rune(long)[:deleteExcerptRunes-1]) + `…"?`,
		},
		{
			"a thread with no comments names no excerpt and no count",
			nil,
			"Delete this thread?",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			seedThread(t, f, "seed")
			m := openModel(t, f)
			m = press(m, "n")

			cs := make([]domain.Comment, len(tc.comments))
			for i, body := range tc.comments {
				cs[i] = domain.Comment{Attribution: domain.Attribution{ActorLogin: "alice-codes", ActorDisplay: "alice"}, Body: body}
			}
			m.views[m.cursor] = []ui.ThreadView{{Thread: domain.Thread{ID: "synthetic", Comments: cs}}}

			m = press(m, "d")

			if m.mode != modeConfirmDeleteThread {
				t.Fatalf("mode = %v, want modeConfirmDeleteThread; status=%q", m.mode, m.status)
			}
			if m.confirm != tc.want {
				t.Fatalf("confirm = %q, want %q", m.confirm, tc.want)
			}
		})
	}
}

// This gesture's headline assertion, driven at a NON-ZERO m.selected: three
// threads on one block, tab twice to the third, then d and y.
//
// Three threads on one block is the shape that makes this gesture's selection
// problem real, since ui.RenderDoc draws no marker saying which of them the
// keyboard is aimed at. A delete that read selection 0 while the panel named
// thread C would pass every assertion that only counted threads, which is why
// the assertions below are by BODY and cover both directions: C is gone from
// the store, and A and B are still in it.
func TestDeleteDestroysTheSelectedThreadAndNothingElse(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "thread A")
	addThread(t, f, "thread B")
	addThread(t, f, deleteHead)
	m := openModel(t, f)
	planID := m.sess.Plan.ID

	// n reaches the block and enter puts its threads on screen -- WITHOUT
	// moving the focus into them, so the first n after it steps onto thread 1
	// and two more reach the third. A collapsed block's threads are not in
	// the walk at all: a focus on a row nobody can see is what this removed.
	m = press(m, "n", "enter", "n", "n", "n")
	if got := m.selected[m.cursor]; got != 2 {
		t.Fatalf("selected index after three n = %d, want 2 -- the selection never moved", got)
	}

	m = press(m, "d")
	if want := `Delete "` + deleteHead + `"?`; m.confirm != want {
		t.Fatalf("confirm = %q, want %q -- the panel named a thread the selection is not on", m.confirm, want)
	}

	cur, cmd := m.Update(keyYes)
	m = drain(t, cur.(*Model), cmd)

	if m.mode != modeRead {
		t.Fatalf("mode = %v, want modeRead after the write", m.mode)
	}
	if m.status != "thread deleted" {
		t.Fatalf("status = %q, want %q", m.status, "thread deleted")
	}
	if got, want := threadHeads(t, f, planID), []string{"thread A", "thread B"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("threads in the store = %v, want %v", got, want)
	}
	// A SCREEN assertion that can actually fail, unlike "the body is gone":
	// collapsed anchored threads never render their bodies, so looking for the
	// deleted comment's text would have passed whether or not the delete
	// landed. The status bar's own thread count moves from 3 to 2 only if the
	// write landed AND handleActionDone re-projected.
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "· 2 threads") {
		t.Fatalf("the status bar does not say two threads are left:\n%s", got)
	}
}

// TestDeleteCancelledDestroysNothing pins both cancels. The thread is still
// there afterwards -- asserted against the store, not the screen -- and the
// armed thread id is dropped with the panel, so no later y can spend it.
func TestDeleteCancelledDestroysNothing(t *testing.T) {
	for _, key := range []string{"n", "esc"} {
		t.Run(key, func(t *testing.T) {
			f := setup(t)
			seedThread(t, f, deleteHead)
			m := openModel(t, f)
			planID := m.sess.Plan.ID

			m = press(m, "n", "d")
			if m.mode != modeConfirmDeleteThread {
				t.Fatalf("mode = %v, want modeConfirmDeleteThread", m.mode)
			}
			// "n" is ActNextThread in read mode and a literal cancel here, so
			// this also pins that the panel answers its own literals rather
			// than the keymap.
			m = press(m, key)

			if m.mode != modeRead {
				t.Fatalf("mode after %q = %v, want modeRead", key, m.mode)
			}
			if m.deleteTID != "" {
				t.Fatalf("deleteTID = %q after cancel, want it dropped with the panel", m.deleteTID)
			}
			if got := threadHeads(t, f, planID); len(got) != 1 || got[0] != deleteHead {
				t.Fatalf("threads in the store = %v, want the thread untouched", got)
			}
		})
	}
}

// The door's own refusal, in the words ActReply and ActToggleResolve already
// use for the identical state: the cursor is on a block no thread hangs off,
// so there is nothing to name and no panel to open.
func TestDeleteWithNothingSelectedOpensNoPanel(t *testing.T) {
	f := setup(t)
	seedThread(t, f, deleteHead)
	m := openModel(t, f)

	m = press(m, "g", "d") // g is the top of the document; no thread there

	if m.mode != modeRead {
		t.Fatalf("mode = %v, want modeRead -- a panel opened with nothing selected", m.mode)
	}
	if m.status != "no thread at cursor" {
		t.Fatalf("status = %q, want %q", m.status, "no thread at cursor")
	}
}

// The two rows below are TWO PALETTES over one render path, not two
// renderers: a nil theme is theme.Default()'s palette rather than an
// unpainted rendering.
//
// What no text assertion catches at all is viewHeight's reservation, since
// the render appends the panel unconditionally. Dropping this mode from
// viewHeight leaves the text visible while the panel silently overlaps the
// document's last rows.
func TestDeleteConfirmRendersOnBothPathsAndReservesItsSpace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		painted bool
	}{
		{"system", false},
		{"painted", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var th *theme.Theme
			if tc.painted {
				th = paintedTheme(t)
			}
			f := setup(t)
			seedThread(t, f, deleteHead)
			s, err := session.Open(f.ctx, f.svc, f.path)
			if err != nil {
				t.Fatal(err)
			}
			m := New(s, keymap.Default(), th, "", nil)
			if err := m.RefreshFromSession(f.ctx); err != nil {
				t.Fatal(err)
			}
			m.width, m.height = 100, 30
			m.rerender()
			readVH := m.viewHeight()

			m = press(m, "n", "d")
			if m.mode != modeConfirmDeleteThread {
				t.Fatalf("mode = %v, want modeConfirmDeleteThread; status=%q", m.mode, m.status)
			}
			if !strings.Contains(ansi.Strip(m.View().Content), deleteHead) {
				t.Fatalf("View() does not render the delete confirm:\n%s", ansi.Strip(m.View().Content))
			}
			panelHeight := m.confirmPanelHeight()
			if panelHeight < 2 {
				t.Fatalf("test setup: confirmPanelHeight() = %d, want at least the text row plus its spacer", panelHeight)
			}
			if got, want := m.viewHeight(), readVH-panelHeight; got != want {
				t.Fatalf("viewHeight() during the delete confirm = %d, want %d (read-mode height minus this panel's "+
					"own reservation) -- the panel overlaps the document's last rows instead of displacing them", got, want)
			}
		})
	}
}

// deleteThreadFromStore destroys the thread whose head comment is head straight
// through the service, without the model's knowledge -- the way an agent over
// MCP, or a second draftplane process, destroys one. The model finds out at its
// next refresh and not before, which is the whole state these tests are about.
func deleteThreadFromStore(t *testing.T, f fixture, id domain.PlanID, head string) {
	t.Helper()
	if err := f.svc.DeleteThread(f.ctx, id, threadIDByHead(t, f, id, head)); err != nil {
		t.Fatal(err)
	}
}

// resolvedHeads is threadHeads narrowed to the threads marked resolved, and it
// is how the tests below ask WHICH thread a keystroke wrote to. The screen
// cannot answer that -- ui.RenderDoc draws no marker saying which of a block's
// threads the keyboard is aimed at -- so the verdict is read out of the store.
func resolvedHeads(t *testing.T, f fixture, id domain.PlanID) []string {
	t.Helper()
	threads, err := f.svc.Threads(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	heads := make([]string, 0, len(threads))
	for _, th := range threads {
		if th.Resolved && len(th.Comments) > 0 {
			heads = append(heads, th.Comments[0].Body)
		}
	}
	return heads
}

// NOT a panic: m.selected maps a block index to a thread index within that
// block, and when a thread is destroyed out from under the model the rebuilt
// views renumber it. selectedThread absorbed every stale index with a MODULO,
// so the index never pointed out of range -- it WRAPPED, and came back a
// different, perfectly valid-looking thread. The keystroke that follows then
// writes to a thread the user never selected.
//
// So the assertion is an IDENTITY one, and it has to be: "no panic" and "the
// index is in range" were both already true of the defect, and would pass
// against it. R resolves through the stale selection and the verdict is read
// back out of the store BY THREAD -- which thread came back Resolved.
//
// TWO of the four cases below kill the defect, and they kill two different
// halves of it. Selection on C with C deleted resolved A (3 views down to 2,
// 2%2=0) -- the wrap. Selection on B with A deleted resolved C (1%2=1, in range
// and wrong) -- and that one is why a CLAMP is not the repair: the index was
// never out of range there at all, so only a by-ID re-find answers it. The
// other two pin the RULING rather than the defect, and pass against the unfixed
// code.
//
// THE CURSOR-LANDING RULING: when the selected thread is itself gone the
// selection HOLDS ITS POSITION, clamped into the shrunken list -- the same
// ruling rebuildRows applies to the list's own cursor. It has two branches,
// separately pinned, because a PREVIOUS-thread ruling would satisfy either one
// alone:
//
//   - the deleted thread was MID-BLOCK, so the index is still in range and the
//     selection lands on the NEIGHBOUR THAT SLID UP into the vacated slot (B of
//     A/B/C deleted, selection on B, lands on C). This is the ruling's headline
//     sentence and reaimSelection's `return i`.
//   - the deleted thread was LAST, so the index ran off the end and the clamp
//     lands the selection on the NEW LAST thread (C of A/B/C deleted, selection
//     on C, lands on B).
//
// A block left with no threads at all keeps no selection: the entry is not
// carried over, so selectedThread answers "no thread" -- the fourth case.
//
// Both production refresh paths are driven, because they are two different
// seams into the same rebuild: ctrl+r reloads and re-projects through
// handleActionDone, msgStateChanged re-projects on the loop.
func TestSelectionFollowsItsThreadAcrossARefresh(t *testing.T) {
	for _, path := range []struct {
		name    string
		refresh func(t *testing.T, m *Model) *Model
	}{
		{"ctrl+r", func(t *testing.T, m *Model) *Model {
			t.Helper()
			cur, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
			return drain(t, cur.(*Model), cmd)
		}},
		{"msgStateChanged", func(t *testing.T, m *Model) *Model {
			t.Helper()
			cur, cmd := m.Update(msgStateChanged{})
			return drain(t, cur.(*Model), cmd)
		}},
	} {
		for _, tc := range []struct {
			name         string
			steps        int
			deleted      []string
			wantResolved []string
			wantStatus   string
			wantSelected int
		}{
			{
				name:         "the selected thread was last and the selection holds its position",
				steps:        2,
				deleted:      []string{"thread C"},
				wantResolved: []string{"thread B"},
				wantStatus:   "thread updated",
				wantSelected: 1,
			},
			{
				name:         "the selected thread was mid-block and the selection holds its position",
				steps:        1,
				deleted:      []string{"thread B"},
				wantResolved: []string{"thread C"},
				wantStatus:   "thread updated",
				wantSelected: 1,
			},
			{
				name:         "an earlier thread is deleted and the selection keeps its own thread",
				steps:        1,
				deleted:      []string{"thread A"},
				wantResolved: []string{"thread B"},
				wantStatus:   "thread updated",
				wantSelected: 0,
			},
			{
				name:         "every thread on the block is deleted and nothing is selected",
				steps:        2,
				deleted:      []string{"thread A", "thread B", "thread C"},
				wantResolved: nil,
				wantStatus:   "no thread at cursor",
				wantSelected: 0,
			},
		} {
			t.Run(path.name+"/"+tc.name, func(t *testing.T) {
				f := setup(t)
				seedThread(t, f, "thread A")
				addThread(t, f, "thread B")
				addThread(t, f, "thread C")
				m := openModel(t, f)
				planID := m.sess.Plan.ID

				// enter opens the card and leaves the focus on the line, so
				// the n after it is what steps onto the first thread.
				m = press(m, "n", "enter", "n")
				for i := 0; i < tc.steps; i++ {
					m = press(m, "n")
				}
				if got := m.selected[m.cursor]; got != tc.steps {
					t.Fatalf("selected index after %d further n = %d, want %d -- the selection never moved", tc.steps, got, tc.steps)
				}

				for _, head := range tc.deleted {
					deleteThreadFromStore(t, f, planID, head)
				}
				m = path.refresh(t, m)

				cur, cmd := m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
				m = drain(t, cur.(*Model), cmd)

				if m.status != tc.wantStatus {
					t.Fatalf("status = %q, want %q", m.status, tc.wantStatus)
				}
				if got := resolvedHeads(t, f, planID); strings.Join(got, "|") != strings.Join(tc.wantResolved, "|") {
					t.Fatalf("R through the stale selection resolved %v, want %v -- the keystroke wrote to a thread the user never selected", got, tc.wantResolved)
				}

				// The repaired index itself, which is what the write path
				// aimed through above and what the rail now draws. The
				// emptied-block case carries its substance in wantStatus
				// rather than here -- with no threads left there is no index
				// to be right or wrong.
				if got := m.selected[m.cursor]; got != tc.wantSelected {
					t.Fatalf("repaired selection index = %d, want %d", got, tc.wantSelected)
				}
			})
		}
	}
}

// The navigation the help bar has always advertised. n/N used to scan for the
// next BLOCK carrying any threads and land on it, skipping every sibling on the
// way, so on a block with three threads one press reached the first and no key
// reached the other two except tab — which was unadvertised and left no mark on
// screen.
//
// Entering a block forward lands on its first thread and backward on its last,
// so walking past a block with n and back with N returns through the same
// threads in reverse rather than skipping them.
func TestNextThreadWalksThreadsNotBlocks(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "thread A")
	addThread(t, f, "thread B")
	addThread(t, f, "thread C")
	m := openModel(t, f)

	// n reaches the block, enter opens the card without moving the focus into
	// it, and the n after that steps onto the first thread.
	m = press(m, "n", "enter", "n")
	block := m.cursor
	if got := len(m.views[block]); got != 3 {
		t.Fatalf("fixture put %d threads on the cursor's block, want 3", got)
	}
	for want := 0; want < 3; want++ {
		if got := m.focusedThread(); got != want {
			t.Fatalf("after %d presses the selection is thread %d, want %d -- n is skipping siblings", want+1, got, want)
		}
		if m.cursor != block {
			t.Fatalf("n left the block at selection %d, before its threads were exhausted", want)
		}
		m = press(m, "n")
	}
	// Only once they are exhausted does the block change — and there is nowhere
	// further in this fixture, so the press after the last thread CYCLES back to
	// the first rather than refusing. Position only: the wrap's silence and the
	// cross-block cycle are TestNAndNWrapAtTheEndsOfTheThreadCycle's subject.
	if m.cursor != block {
		t.Fatalf("the wrap left the cursor on block %d, want it back on %d", m.cursor, block)
	}
	if got := m.focusedThread(); got != 0 {
		t.Fatalf("the press after the last thread selected %d, want the wrap onto the first", got)
	}

	// Back through the same threads in reverse rather than past them. The walk
	// above wrapped ONTO the first thread, so the first N wraps in turn onto the
	// last and the rest are steps down from it.
	for _, want := range []int{2, 1, 0} {
		m = press(m, "N")
		if got := m.focusedThread(); got != want {
			t.Fatalf("walking back, the selection is thread %d, want %d", got, want)
		}
	}
}

// The shape of the visible sequence down and up walk.
//
// A block's threads are rows of that sequence only while they are ON SCREEN.
// Collapsed, the focus passes the block by; expanded, down steps from the
// commented line into the first thread and through the rest, and up returns
// the same way. That is what lets one rail mark one focus: every row the focus
// can rest on is a row the reader can see.
//
// The walk is a ROUND TRIP rather than a step out the far side, because the
// fixture anchors its threads to the document's LAST block -- there is nothing
// below to step out to.
func TestDownWalksIntoExpandedThreadsAndOverCollapsedOnes(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "thread A")
	addThread(t, f, "thread B")
	m := openModel(t, f)
	for i := 0; i < len(m.blocks) && len(m.views[m.cursor]) == 0; i++ {
		m = press(m, "n")
	}
	block := m.cursor
	if got := len(m.views[block]); got != 2 {
		t.Fatalf("fixture put %d threads on the cursor's block, want 2", got)
	}
	if block == 0 {
		t.Fatal("fixture has no block above the commented one to step out to")
	}

	// Collapsed, its threads are not in the sequence: down finds nothing below
	// (this is the last block) and the focus stays on the line rather than
	// dropping into a card nobody can see.
	if got := m.focusedThread(); got != ui.NoThread {
		t.Fatalf("arriving at a collapsed block the focus is thread %d, want the line", got)
	}
	m = press(m, "j")
	if got := m.focusedThread(); got != ui.NoThread {
		t.Fatalf("down entered thread %d of a COLLAPSED block; its rows are off screen", got)
	}

	// Expanded, down enters them one at a time.
	m = press(m, "enter")
	if got := m.focusedThread(); got != ui.NoThread {
		t.Fatalf("expanding moved the focus to thread %d; it must stay on the line", got)
	}
	for _, want := range []int{0, 1} {
		m = press(m, "j")
		if m.cursor != block {
			t.Fatalf("down left the block before reaching thread %d", want)
		}
		if got := m.focusedThread(); got != want {
			t.Fatalf("down put the focus on thread %d, want %d", got, want)
		}
	}

	// And up returns through the same rows, out to the line and then off the
	// block -- not straight past its threads.
	for _, want := range []int{0, ui.NoThread} {
		m = press(m, "k")
		if m.cursor != block {
			t.Fatalf("up left the block before returning to %d", want)
		}
		if got := m.focusedThread(); got != want {
			t.Fatalf("up put the focus on thread %d, want %d", got, want)
		}
	}
	if m = press(m, "k"); m.cursor != block-1 {
		t.Fatalf("up off the line stayed on block %d, want %d", m.cursor, block-1)
	}
}

// nAndNCycle is where the focus is, as the pair n and N actually move: the
// cursor BLOCK and the thread selected under it. Both halves are needed and
// neither is enough -- the block alone cannot tell a walk within a card from a
// fixed point, and the thread index alone is 0 on the first thread of every
// block.
func nAndNCycle(m *Model) [2]int { return [2]int{m.cursor, m.selectedIndex(m.cursor)} }

// TestNAndNWrapAtTheEndsOfTheThreadCycle is the walk's shape after the ruling
// that n and N CYCLE: the press after the last thread is the first thread, not a
// refusal.
//
// THE WRAP IS SILENT, like seekMatch's own (see it). That is not decoration --
// jumpThread writing to the status bar on a SUCCESSFUL jump would clobber the
// live-refresh flash TestStateChangeRefreshesInReadMode reads off the same row,
// and a wrap is a successful jump.
//
// TWO ANNOTATED BLOCKS AND ONE THREAD APIECE is the smallest fixture that can
// tell a wrap from a stall: on one block the wrap lands where a within-block
// step would, so nothing distinguishes them.
func TestNAndNWrapAtTheEndsOfTheThreadCycle(t *testing.T) {
	f := setup(t)
	seedThreadOn(t, f, "Rate Limiter", "unbounded", "thread on context")
	seedThreadOn(t, f, "Rate Limiter", "token bucket", "thread on design")
	m := openModel(t, f)
	for i := range m.blocks {
		m.expanded[i] = true
	}

	m = press(m, "g", "n")
	first := nAndNCycle(m)
	m = press(m, "n")
	last := nAndNCycle(m)
	if last == first {
		t.Fatalf("n did not reach the second thread; both presses landed at %v and this fixture cannot show a wrap", first)
	}

	m = press(m, "n")
	if got := nAndNCycle(m); got != first {
		t.Fatalf("n past the last thread landed at %v, want the wrap back to the first at %v", got, first)
	}
	if m.status != "" {
		t.Fatalf("the wrap wrote %q to the status bar; a successful jump is silent", m.status)
	}

	m = press(m, "N")
	if got := nAndNCycle(m); got != last {
		t.Fatalf("N before the first thread landed at %v, want the wrap back to the last at %v", got, last)
	}
	if m.status != "" {
		t.Fatalf("the backward wrap wrote %q to the status bar; a successful jump is silent", m.status)
	}
}

// TestNAndNOnACycleWithNowhereToGo is the two degenerate cycles, and the ruling
// is that NEITHER is a refusal to be read as one: with a single thread the wrap
// lands where it started, which is a no-op and says nothing, and with no threads
// at all there is a cycle of length zero and the reader is told.
//
// THE SENTENCE IS ABOUT THE GESTURE AND NOT ABOUT THE DOCUMENT, which the
// wording has to carry: the status bar counts UNANCHORED threads in its total
// (projection.threadCount) and n/N cannot reach one, so "no threads in this
// document" would contradict a bar reading "2 threads" on the same screen.
func TestNAndNOnACycleWithNowhereToGo(t *testing.T) {
	for _, tc := range []struct {
		name       string
		seed       func(t *testing.T, f fixture)
		wantStatus string
	}{
		{
			name:       "one thread is a fixed point",
			seed:       func(t *testing.T, f fixture) { seedThread(t, f, "the only thread") },
			wantStatus: "",
		},
		{
			name:       "no threads at all is a cycle of length zero",
			wantStatus: noJumpTargets,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			if tc.seed != nil {
				tc.seed(t, f)
			}
			m := openModel(t, f)
			for i := range m.blocks {
				m.expanded[i] = true
			}
			m = press(m, "g")
			if tc.seed != nil {
				m = press(m, "n") // onto the only thread there is
			}

			want := nAndNCycle(m)
			for _, k := range []string{"n", "n", "N", "N", "n"} {
				m.status = ""
				m = press(m, k)
				if got := nAndNCycle(m); got != want {
					t.Fatalf("%q moved the focus from %v to %v; there is nowhere else in this cycle to go", k, want, got)
				}
				if m.status != tc.wantStatus {
					t.Fatalf("%q wrote status %q, want %q", k, m.status, tc.wantStatus)
				}
			}
		})
	}
}

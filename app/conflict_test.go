package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/keymap"
	"github.com/draftplane/draftplane/session"
	"github.com/draftplane/draftplane/theme"
)

// conflictSetup opens svc as a review model, then arranges for the next
// write to conflict. The model opens on v0; the file on disk is rewritten
// and reloaded so the session reads as unregistered (otherwise
// ensureRegistered short-circuits on "already registered" and never reaches
// RegisterVersion), then a sibling registers its own content on top of v0.
// Whatever write the caller triggers next asserts Base=v0 against a tip
// that is now siblingHash.
func conflictSetup(t *testing.T, f fixture, svc client.PlanService, width int, th *theme.Theme) (m *Model, siblingHash domain.ContentHash, siblingContent, humanContent []byte) {
	t.Helper()
	s, err := session.Open(f.ctx, svc, f.path)
	if err != nil {
		t.Fatal(err)
	}
	m = New(s, keymap.Default(), th, "", nil)
	if err := m.RefreshFromSession(f.ctx); err != nil {
		t.Fatal(err)
	}
	m.width, m.height = width, 30
	m.rerender()

	humanContent = []byte(testDoc + "\n\n## Human Addendum\n\nWritten by the person at this keyboard.\n")
	if err := os.WriteFile(f.path, humanContent, 0o644); err != nil {
		t.Fatal(err)
	}
	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = drain(t, cur.(*Model), cmd)
	if m.sess.Hash != domain.HashContent(humanContent) {
		t.Fatalf("reload did not pick up the human's own edit; sess.Hash = %s", m.sess.Hash)
	}

	siblingContent = []byte(testDoc + "\n\n## Sibling Addendum\n\nWritten by someone else, elsewhere.\n")
	siblingHash, err = f.cas.Put(f.ctx, siblingContent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RegisterVersion(f.ctx, m.sess.Plan.ID, client.VersionRegistration{
		Content: siblingContent, Base: m.sess.Base(),
	}); err != nil {
		t.Fatal(err)
	}
	return m, siblingHash, siblingContent, humanContent
}

// conflictFixtureWith runs conflictSetup over a plain local svc, then
// triggers the conflict via approve -- this suite's vehicle for the
// accept/decline/re-conflict mechanism, which is generic rather than
// approve-specific.
func conflictFixtureWith(t *testing.T, width int, th *theme.Theme) (f fixture, m *Model, siblingHash domain.ContentHash, siblingContent, humanContent []byte) {
	t.Helper()
	f = setup(t)
	seedThread(t, f, "seed")
	m, siblingHash, siblingContent, humanContent = conflictSetup(t, f, f.svc, width, th)

	m = press(m, "a")
	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = drain(t, cur.(*Model), cmd)

	if m.mode != modeConflict {
		t.Fatalf("mode after conflicting approve = %v, want modeConflict", m.mode)
	}
	if m.conflictTip.Hash != siblingHash {
		t.Fatalf("conflictTip.Hash = %s, want the sibling's hash %s", m.conflictTip.Hash, siblingHash)
	}
	return f, m, siblingHash, siblingContent, humanContent
}

func conflictFixture(t *testing.T) (fixture, *Model, domain.ContentHash, []byte, []byte) {
	return conflictFixtureWith(t, 100, nil)
}

// The tip a conflicting write carried must be on screen, not just in model
// state.
func TestConflictPromptOpensCarryingTip(t *testing.T) {
	_, m, siblingHash, _, _ := conflictFixture(t)

	if !strings.Contains(m.confirm, siblingHash.Short()) {
		t.Fatalf("confirm text %q does not name the conflicting tip %s", m.confirm, siblingHash.Short())
	}
	if !strings.Contains(m.View().Content, siblingHash.Short()) {
		t.Fatalf("View() does not render the tip %s on screen", siblingHash.Short())
	}
}

// Accept: the session adopts the sibling's tip (content and hash both), the
// pending write is abandoned (nothing new registered), and the fetched
// content is what actually renders.
func TestConflictAcceptFetchesAndReanchors(t *testing.T) {
	f, m, siblingHash, siblingContent, _ := conflictFixture(t)

	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = drain(t, cur.(*Model), cmd)

	if m.mode != modeRead {
		t.Fatalf("mode after accept = %v, want modeRead", m.mode)
	}
	// Accept detaches a file-backed session from its file -- ctrl+r from here
	// on reloads the tip, not the file -- and the status line is the only
	// in-TUI signal of that.
	if !strings.Contains(m.status, "Reopen with your file") {
		t.Fatalf("status = %q, want it to say the session no longer follows the file on disk", m.status)
	}
	if m.sess.Hash != siblingHash {
		t.Fatalf("session hash after accept = %s, want the fetched tip %s", m.sess.Hash, siblingHash)
	}
	if !bytes.Equal(m.sess.Content, siblingContent) {
		t.Fatalf("session content after accept does not match the fetched tip's bytes")
	}
	// Body text rather than the heading above it: heading text renders one
	// escape-coded rune at a time, so a substring match on it would fail for
	// reasons unrelated to what this test pins.
	if !strings.Contains(m.View().Content, "Written by someone else") {
		t.Fatalf("View() does not render the fetched content")
	}
	versions, err := f.svc.Versions(f.ctx, m.sess.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 {
		t.Fatalf("version count after accept = %d, want 2 -- accept must only fetch, never register", len(versions))
	}
}

// Decline: the retry registers the human's OWN content on top of the
// sibling's tip specifically, and the original write (approve) actually
// completes -- not just the version registration underneath it.
func TestConflictDeclineRetriesWithTheReturnedTipAndSucceeds(t *testing.T) {
	f, m, siblingHash, _, humanContent := conflictFixture(t)

	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = drain(t, cur.(*Model), cmd)

	if m.mode != modeRead {
		t.Fatalf("mode after decline = %v, want modeRead", m.mode)
	}
	if !strings.Contains(m.status, "review saved for") {
		t.Fatalf("status = %q, want the original approve's own success message (proving the write itself replayed, not just the registration)", m.status)
	}

	wantHash := domain.HashContent(humanContent)
	versions, err := f.svc.Versions(f.ctx, m.sess.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 3 {
		t.Fatalf("version count after decline = %d, want 3 (v0, sibling, human)", len(versions))
	}
	if got := versions[len(versions)-1].Hash; got != wantHash {
		t.Fatalf("tip after decline = %s, want the human's own content hash %s", got, wantHash)
	}
	if got := versions[len(versions)-2].Hash; got != siblingHash {
		t.Fatalf("version before the retry = %s, want the sibling's hash %s -- Base must be the returned tip, not the session's stale one", got, siblingHash)
	}

	approvals, err := f.svc.Approvals(f.ctx, m.sess.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range approvals {
		if a.Hash == wantHash {
			found = true
		}
	}
	if !found {
		t.Fatalf("no approval recorded for %s -- decline's retry must re-run the original write, not stop at registering the version", wantHash)
	}
}

// The property a force flag would not have: if a second sibling pushes while
// the prompt is open, declining conflicts again and re-opens the SAME prompt
// with the NEW tip instead of overwriting that sibling's work.
func TestConflictOnTheRetryReprompts(t *testing.T) {
	f, m, siblingHash, _, _ := conflictFixture(t)

	secondContent := []byte(testDoc + "\n\n## Second Sibling\n\nA later, independent push.\n")
	secondHash, err := f.cas.Put(f.ctx, secondContent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RegisterVersion(f.ctx, m.sess.Plan.ID, client.VersionRegistration{
		Content: secondContent, Base: siblingHash,
	}); err != nil {
		t.Fatal(err)
	}

	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = drain(t, cur.(*Model), cmd)

	if m.mode != modeConflict {
		t.Fatalf("mode after a second sibling push mid-decline = %v, want modeConflict again -- a force flag would have silently overwritten this", m.mode)
	}
	if m.conflictTip.Hash != secondHash {
		t.Fatalf("conflictTip.Hash = %s, want the newer tip %s", m.conflictTip.Hash, secondHash)
	}
	versions, err := f.svc.Versions(f.ctx, m.sess.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := versions[len(versions)-1].Hash; got != secondHash {
		t.Fatalf("tip after the re-conflicted decline = %s, want it untouched at %s -- nothing was overwritten", got, secondHash)
	}
	if len(versions) != 3 {
		t.Fatalf("version count after the re-conflicted decline = %d, want 3 (v0, sibling, second sibling) -- the retry's own RegisterVersion must not have appended anything", len(versions))
	}
}

// The full rendered output must never exceed m.height. The four cases sweep
// two widths under two PALETTES, not two render paths -- a nil theme is
// theme.Default()'s palette.
func TestConflictPromptNeverOverflowsTheScreen(t *testing.T) {
	tests := []struct {
		name    string
		width   int
		painted bool
	}{
		{"system, width 80", 80, false},
		{"system, width 100", 100, false},
		{"painted, width 80", 80, true},
		{"painted, width 100", 100, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var th *theme.Theme
			if tt.painted {
				var err error
				th, err = theme.Lookup("dark")
				if err != nil {
					t.Fatal(err)
				}
			}
			_, m, _, _, _ := conflictFixtureWith(t, tt.width, th)

			// Exact, not "at most": ">" alone lets an under-render (rows dropping
			// below height) slip past silently.
			rows := strings.Split(m.View().Content, "\n")
			if len(rows) != m.height {
				t.Fatalf("rendered %d rows, want exactly %d (height)", len(rows), m.height)
			}
		})
	}
}

// A row-count check alone cannot catch an under-render on the painted path:
// panelViewPainted's switch silently missing modeConflict would still
// reserve confirmPanelHeight() rows via viewHeight and leave them blank.
// Only asserting the tip is actually on screen can.
func TestConflictPromptPaintedRendersPrompt(t *testing.T) {
	th, err := theme.Lookup("dark")
	if err != nil {
		t.Fatal(err)
	}
	_, m, siblingHash, _, _ := conflictFixtureWith(t, 100, th)

	out := ansi.Strip(m.View().Content)
	if !strings.Contains(out, siblingHash.Short()) {
		t.Fatalf("painted View() does not render the tip %s -- the conflict panel is not on screen", siblingHash.Short())
	}
}

// The mechanism is generic -- wired at handleActionDone, not special-cased
// to approve: the accept/decline/re-conflict suite above already proves the
// shared machinery once. What a comment adds is the DRAFT: the composer is
// reset before the write is dispatched, so the retry closure is the only place
// the typed body still lives, and declining has to post it.
func TestConflictPromptOpensFromComment(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "seed")
	m, siblingHash, _, _ := conflictSetup(t, f, f.svc, 100, nil)

	m = press(m, "j", "j") // off the H1 heading, onto a content block
	m = press(m, "c")
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want modeCompose", m.mode)
	}
	m = press(m, "h", "i")
	m, cmd := pressComposePost(t, m)
	m = drain(t, m, cmd)

	if m.mode != modeConflict {
		t.Fatalf("mode after a conflicting comment = %v, want modeConflict", m.mode)
	}
	if m.conflictTip.Hash != siblingHash {
		t.Fatalf("conflictTip.Hash = %s, want %s", m.conflictTip.Hash, siblingHash)
	}

	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = drain(t, cur.(*Model), cmd)
	if m.mode != modeRead || m.status != "comment posted" {
		t.Fatalf("after declining: mode = %v, status = %q, want modeRead and the comment's own success", m.mode, m.status)
	}
	threads, err := f.svc.Threads(f.ctx, m.sess.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	posted := false
	for _, th := range threads {
		if len(th.Comments) > 0 && th.Comments[0].Body == "hi" {
			posted = true
		}
	}
	if !posted {
		t.Fatalf("no thread carries the typed body %q after declining -- the draft was lost", "hi")
	}
}

// A reply to a nonexistent thread fails without ever reaching
// ensureRegistered (Reply cannot conflict), and that failure must leave
// nothing armed for a LATER, unrelated conflict to adopt: a subsequent
// conflicting rehome must not find the stale reply closure and offer to
// replay it. Retry travels on msgActionDone itself, so a rehome's result
// can only ever carry a rehome's own closure.
func TestFailedNonConflictWriteNeverArmsARetryForAnUnrelatedConflict(t *testing.T) {
	f := setup(t)
	seedOrphan(t, f, "orphaned body")
	m := openModel(t, f)

	threadsBefore, err := f.svc.Threads(f.ctx, m.sess.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	commentsBefore := 0
	for _, th := range threadsBefore {
		commentsBefore += len(th.Comments)
	}

	// A reply to a thread id that does not exist: Reply never reaches
	// ensureRegistered, so this is an ordinary, non-conflict failure.
	m.enterCompose("does-not-exist")
	m = press(m, "h", "i")
	m, cmd := pressComposePost(t, m)
	m = drain(t, m, cmd)

	if m.mode != modeRead {
		t.Fatalf("mode after the failed reply = %v, want modeRead", m.mode)
	}
	if !strings.Contains(m.status, "error") {
		t.Fatalf("status = %q, want an error (the reply target does not exist)", m.status)
	}
	if m.pendingRetry != nil {
		t.Fatal("pendingRetry must stay nil after a non-conflict failure -- nothing conflicted, so nothing should be armed to retry")
	}

	// Arrange a real conflict on the NEXT write: revise the file so the
	// session's content is no longer the tip, then have a sibling register a
	// version on top of it.
	revised := []byte(testDoc + "\n\n## Human Addendum\n\nWritten by the person at this keyboard.\n")
	if err := os.WriteFile(f.path, revised, 0o644); err != nil {
		t.Fatal(err)
	}
	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = drain(t, cur.(*Model), cmd)

	siblingContent := []byte(testDoc + "\n\n## Sibling Addendum\n\nWritten by someone else, elsewhere.\n")
	if _, err := f.svc.RegisterVersion(f.ctx, m.sess.Plan.ID, client.VersionRegistration{
		Content: siblingContent, Base: m.sess.Base(),
	}); err != nil {
		t.Fatal(err)
	}
	// The store's own sentence for this exact stale base, asked of it directly so
	// the assertion below pins where the words go and not what they say.
	_, err = f.svc.RegisterVersion(f.ctx, m.sess.Plan.ID, client.VersionRegistration{
		Content: m.sess.Content, Base: m.sess.Base(),
	})
	var conflict *client.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("RegisterVersion over the stale base = %v, want a *client.ConflictError", err)
	}

	// Dispatching the orphan onto the cursor's block reaches ensureRegistered,
	// so this rehome conflicts against the sibling's push above.
	m = press(m, "m")
	if m.mode != modeRelocate {
		t.Fatalf("mode = %v, want modeRelocate", m.mode)
	}
	cur, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = cur.(*Model)
	m = drain(t, m, cmd)

	// The whole point: this must NOT be the reply's closure re-surfacing. A
	// rehome conflict has no retry wired, so it must show a plain error naming
	// the tip and the way out (modeRelocate has no in-mode reload) -- never
	// modeConflict, and never silently posting the stale reply.
	if m.mode != modeRelocate {
		t.Fatalf("mode after the conflicting rehome = %v, want modeRelocate (unchanged; no retry is wired for it)", m.mode)
	}
	// The sentence is the store's own (conflict.Error(), asked for above), so it
	// is free to change. The subject here is that a rehome conflict surfaces
	// plainly and names where the plan now stands.
	movedTip := domain.HashContent(siblingContent)
	if !strings.Contains(m.status, conflict.Error()) || !strings.Contains(m.status, movedTip.Short()) {
		t.Fatalf("status = %q, want it to name the moved tip %s", m.status, movedTip.Short())
	}
	if !strings.Contains(m.status, "ctrl+r") {
		t.Fatalf("status = %q, want it to name the recovery path (esc, ctrl+r, m) since modeRelocate cannot reload in place", m.status)
	}

	threadsAfter, err := f.svc.Threads(f.ctx, m.sess.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	commentsAfter := 0
	for _, th := range threadsAfter {
		commentsAfter += len(th.Comments)
	}
	if commentsAfter != commentsBefore {
		t.Fatalf("comment count = %d, want unchanged at %d -- the stale reply must never have posted", commentsAfter, commentsBefore)
	}
}

// wrappingConflictSvc wraps whatever *client.ConflictError RegisterVersion
// returns one layer deeper (%w, so errors.As can still recover it), to prove
// handleActionDone uses errors.As rather than a bare type assertion.
type wrappingConflictSvc struct {
	client.PlanService
}

func (w *wrappingConflictSvc) RegisterVersion(ctx context.Context, id domain.PlanID, r client.VersionRegistration) (domain.VersionRef, error) {
	ref, err := w.PlanService.RegisterVersion(ctx, id, r)
	var conflict *client.ConflictError
	if errors.As(err, &conflict) {
		return domain.VersionRef{}, fmt.Errorf("save failed: %w", conflict)
	}
	return ref, err
}

// Every other conflict test drives session.go's own methods, which return
// the conflict unwrapped, so a bare msg.err.(*client.ConflictError) type
// assertion in handleActionDone would leave the whole app package green with
// nothing catching it.
func TestConflictSurvivesAWrappedError(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "seed")
	wrapping := &wrappingConflictSvc{PlanService: f.svc}
	m, siblingHash, _, _ := conflictSetup(t, f, wrapping, 100, nil)

	m = press(m, "a")
	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = drain(t, cur.(*Model), cmd)

	if m.mode != modeConflict {
		t.Fatalf("mode after a WRAPPED conflicting approve = %v, want modeConflict -- errors.As must recover it through the extra layer", m.mode)
	}
	if m.conflictTip.Hash != siblingHash {
		t.Fatalf("conflictTip.Hash = %s, want %s", m.conflictTip.Hash, siblingHash)
	}
}

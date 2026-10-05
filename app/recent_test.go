package app

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/draftplane/draftplane/client/recent"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/keymap"
	"github.com/draftplane/draftplane/session"
)

func TestRecordOpenRemembersWhatTheOpenLandedOn(t *testing.T) {
	f := newListFixture(t)
	file := filepath.Join(filepath.Dir(f.path), "notes.md")
	if err := os.WriteFile(file, []byte("# Scratch notes\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	planLess, err := session.Open(f.ctx, f.svc, file)
	if err != nil {
		t.Fatal(err)
	}
	withPlan := seedListPlan(t, f, filepath.Dir(f.path), "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody.\n")
	byID, err := session.OpenByID(f.ctx, f.svc, withPlan.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		sess *session.Session
		want recent.Entry
	}{
		{"a file with no plan is its path", planLess, recent.Entry{Path: file}},
		{"a plan is its id and its file", withPlan, recent.Entry{PlanID: withPlan.Plan.ID, Path: withPlan.Path}},
		{"a plan opened by id is the same entry", byID, recent.Entry{PlanID: withPlan.Plan.ID, Path: byID.Path}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "recent.json")
			if err := RecordOpen(path, tc.sess, listFixtureEpoch); err != nil {
				t.Fatal(err)
			}
			got, err := recent.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].PlanID != tc.want.PlanID || got[0].Path != tc.want.Path || !got[0].OpenedAt.Equal(listFixtureEpoch) {
				t.Fatalf("recorded %+v, want %+v at %v", got, tc.want, listFixtureEpoch)
			}
		})
	}
}

func TestOnlyAnOpenThatSucceedsIsRemembered(t *testing.T) {
	f := newListFixture(t)
	seedListPlan(t, f, filepath.Dir(f.path), "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody.\n")
	path := filepath.Join(t.TempDir(), "recent.json")

	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r.SetRecents(path)
	r = drainRoot(t, r, r.Init())
	r = openViaEnter(t, r)
	if got, _ := recent.Load(path); len(got) != 1 || got[0].PlanID == "" {
		t.Fatalf("after opening from the list, recent.json = %+v, want the plan", got)
	}

	// A refused open: the list dispatches an open for a plan that is not there.
	r = drainRoot(t, r, func() tea.Msg { return msgCloseReview{} })
	before, _ := recent.Load(path)
	cur, cmd := r.Update(msgOpenPlan{plan: domain.Plan{ID: "l_gone", SourceHint: "/nowhere/gone.md"}})
	drainRoot(t, cur.(*Root), cmd)
	if after, _ := recent.Load(path); len(after) != len(before) {
		t.Fatalf("a refused open changed recent.json: %+v → %+v", before, after)
	}
}

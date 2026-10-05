package app

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/draftplane/draftplane/client/recent"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/keymap"
)

// fakeStat answers each path with its error in answers. A path with no entry
// does not answer until answerAll, which the test's cleanup also calls, so a
// stat the budget outran is left blocked, as one on a hung mount is, without
// outliving the test.
type fakeStat struct {
	answers map[string]error
	release chan struct{}
	once    sync.Once

	mu    sync.Mutex
	asked []string
}

func newFakeStat(t *testing.T, answers map[string]error) *fakeStat {
	t.Helper()
	f := &fakeStat{answers: answers, release: make(chan struct{})}
	t.Cleanup(f.answerAll)
	return f
}

func (f *fakeStat) stat(path string) (fs.FileInfo, error) {
	f.mu.Lock()
	f.asked = append(f.asked, path)
	f.mu.Unlock()
	if err, ok := f.answers[path]; ok {
		return nil, err
	}
	<-f.release
	return nil, errors.New("answered after the budget")
}

func (f *fakeStat) answerAll() { f.once.Do(func() { close(f.release) }) }

func (f *fakeStat) askedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

// spentBudget has already run out; openBudget never will.
func spentBudget(time.Duration) <-chan time.Time {
	c := make(chan time.Time, 1)
	c <- time.Time{}
	return c
}

func openBudget(time.Duration) <-chan time.Time { return nil }

// TestAPlansFileIsMissingOnlyWhenItsStatSaysSo checks, over an injected stat
// and budget: which path a plan's file is at, and which answers mean it is
// missing. Only fs.ErrNotExist does. Any other error, or no answer within the
// budget, is "don't know", and the circle stays.
func TestAPlansFileIsMissingOnlyWhenItsStatSaysSo(t *testing.T) {
	notExist := &fs.PathError{Op: "stat", Path: "/plans/a.md", Err: fs.ErrNotExist}
	denied := &fs.PathError{Op: "stat", Path: "/plans/a.md", Err: fs.ErrPermission}
	local := domain.Plan{ID: "l_a", SourceHint: "/plans/a.md"}
	for _, tc := range []struct {
		name        string
		plan        domain.Plan
		answers     map[string]error
		outran      bool
		wantAsked   []string
		wantMissing bool
	}{
		{name: "a file that is there", plan: local, answers: map[string]error{"/plans/a.md": nil}, wantAsked: []string{"/plans/a.md"}},
		{name: "a file that is not", plan: local, answers: map[string]error{"/plans/a.md": notExist}, wantAsked: []string{"/plans/a.md"}, wantMissing: true},
		{name: "a file the stat may not look at", plan: local, answers: map[string]error{"/plans/a.md": denied}, wantAsked: []string{"/plans/a.md"}},
		{name: "a stat the budget outran", plan: local, outran: true},
		{name: "a plan with no file", plan: domain.Plan{ID: "l_a"}},
		{name: "a URL source", plan: domain.Plan{ID: "l_a", SourceHint: "notion://page/abc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stat := newFakeStat(t, tc.answers)
			budget := openBudget
			if tc.outran {
				budget = spentBudget
			}
			got := newFileCheck(stat.stat, os.ReadFile, budget).batch().missing([]domain.Plan{tc.plan})
			if got[tc.plan.ID] != tc.wantMissing {
				t.Fatalf("missing = %v, want %v", got[tc.plan.ID], tc.wantMissing)
			}
			if tc.outran {
				return // the stat may not have been reached yet, and is abandoned either way
			}
			if asked := stat.askedPaths(); !slices.Equal(asked, tc.wantAsked) {
				t.Fatalf("stat asked for %q, want %q", asked, tc.wantAsked)
			}
		})
	}
}

// TestAHungMountHoldsTheListForOneBudget is the bound on what a mount that
// never answers can cost. One check arms one budget for all its files, and
// returns when it runs out. The next check joins the stats still out rather
// than starting more, so a hung path holds one goroutine however often the
// list refreshes; and once they answer, the check after asks afresh.
func TestAHungMountHoldsTheListForOneBudget(t *testing.T) {
	stat := newFakeStat(t, nil)
	budgets := 0
	check := newFileCheck(stat.stat, os.ReadFile, func(d time.Duration) <-chan time.Time {
		budgets++
		if d != listFileCheckBudget {
			t.Errorf("budget = %v, want listFileCheckBudget", d)
		}
		return spentBudget(d)
	})
	plans := []domain.Plan{
		{ID: "l_a", SourceHint: "/mnt/a.md"},
		{ID: "l_b", SourceHint: "/mnt/b.md"},
		{ID: "l_c", SourceHint: "/mnt/c.md"},
	}
	inflight := func() map[fileAsk]*fileCall {
		check.mu.Lock()
		defer check.mu.Unlock()
		return maps.Clone(check.inflight)
	}

	if got := check.batch().missing(plans); len(got) != 0 {
		t.Fatalf("missing = %v, want none: an unanswered stat is unknown", got)
	}
	if budgets != 1 {
		t.Fatalf("one check armed %d budgets, want 1 for the whole batch", budgets)
	}
	first := inflight()
	if len(first) != len(plans) {
		t.Fatalf("stats still out = %d, want %d", len(first), len(plans))
	}

	check.batch().missing(plans)
	if again := inflight(); !maps.Equal(again, first) {
		t.Fatalf("a second check started its own stats (%v), want it to join the %d still out", again, len(first))
	}

	stat.answerAll()
	for _, call := range first {
		<-call.done
	}
	asked := stat.askedPaths()
	slices.Sort(asked)
	if want := []string{"/mnt/a.md", "/mnt/b.md", "/mnt/c.md"}; !slices.Equal(asked, want) {
		t.Fatalf("stat asked for %q across two checks, want each path once: %q", asked, want)
	}
	if left := inflight(); len(left) != 0 {
		t.Fatalf("stats still out after answering = %v, want none, so the next check asks afresh", left)
	}
}

// TestALoadWaitsOnOneBudget is the bound held across a whole load: the
// plans' files and Recently opened's are stat'ed as one batch, so a mount that
// never answers delays a load by one budget and not one per kind of file.
func TestALoadWaitsOnOneBudget(t *testing.T) {
	stat := newFakeStat(t, nil)
	plans := []domain.Plan{{ID: "l_a", Title: "A", SourceHint: "/mnt/a.md"}}
	budgets := 0
	m := NewList(listOfSvc{plans: plans}, keymap.Default(), nil)
	m.SetRecents(writeRecents(t, recent.Entry{Path: "/mnt/notes.md"}))
	m.files = newFileCheck(stat.stat, os.ReadFile, func(d time.Duration) <-chan time.Time {
		budgets++
		return spentBudget(d)
	})
	m.refreshCmd()()
	if budgets != 1 {
		t.Fatalf("the load armed %d budgets, want 1 for its plans' files and Recently opened's together", budgets)
	}
}

// hangingRead never answers until the test ends, as a read on a mount that
// answered the file's stat and then hung.
func hangingRead(t *testing.T) func(string) ([]byte, error) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	return func(string) ([]byte, error) {
		<-release
		return nil, errors.New("answered after the budget")
	}
}

// TestAReadThatHangsHoldsTheListForOneBudget is the bound over the other
// thing a load asks of a file: Recently opened reads a file with no plan for
// its title, and a mount can answer the stat and hang on the read. The
// load answers after its one budget, the entry unknown -- neither titled nor
// gone -- and Recently opened skips it, as it skips any entry it cannot draw.
func TestAReadThatHangsHoldsTheListForOneBudget(t *testing.T) {
	notes := writeDoc(t, "notes.md", "# Scratch notes\n")
	plan := domain.Plan{ID: "l_alpha", Title: "Alpha rollout", LastActivityAt: listFixtureEpoch}
	budgets := 0
	m := NewList(listOfSvc{plans: []domain.Plan{plan}}, keymap.Default(), nil)
	m.SetRecents(writeRecents(t, recent.Entry{Path: notes}, recent.Entry{PlanID: plan.ID}))
	m.files = newFileCheck(os.Stat, hangingRead(t), func(d time.Duration) <-chan time.Time {
		budgets++
		return spentBudget(d)
	})
	answer, ok := m.Init()().(msgListRefreshed)
	if !ok {
		t.Fatal("Init produced no load")
	}
	if budgets != 1 {
		t.Fatalf("the load armed %d budgets, want one", budgets)
	}
	if e := answer.recents.entries[0]; e.title != "" || e.gone {
		t.Fatalf("the load answered the file as title %q, gone %v; want it unknown", e.title, e.gone)
	}
	m = deliverList(m, answer)
	if got := sectionBodyText(m, sectionRecent); !slices.Equal(got, []string{plan.Title}) {
		t.Fatalf("Recently opened = %q, want the plan alone -- the unknown file is skipped", got)
	}
}

// untimedFileCheck is the real stat under a budget that never runs out, for a
// test whose answer must depend on the file alone and never on the clock.
func untimedFileCheck() *fileCheck { return newFileCheck(os.Stat, os.ReadFile, openBudget) }

package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/client/recent"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/keymap"
	"github.com/draftplane/draftplane/session"
)

// writeRecents writes a recent.json holding entries, given NEWEST FIRST, the
// order the section draws: they are touched oldest first so the file ends up
// in that order.
func writeRecents(t *testing.T, entries ...recent.Entry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "recent.json")
	for i := len(entries) - 1; i >= 0; i-- {
		if err := recent.Touch(path, entries[i]); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// recentsFrom reads recent.json as a load does, its files checked by files in
// a batch of their own, with no index behind it.
func recentsFrom(path string, files *fileCheck) recentsAnswer {
	batch := files.batch()
	saved, read := loadRecents(path, batch)
	return recentsAnswer{entries: recentEntries(saved, batch.wait()), read: read}
}

// newRecentList builds a list over literal items with a real recent.json
// behind it, its files checked with no budget to run out.
func newRecentList(t *testing.T, items []planItem, entries ...recent.Entry) *ListModel {
	t.Helper()
	path := writeRecents(t, entries...)
	m := NewList(newListFixture(t).svc, keymap.Default(), nil)
	m.SetRecents(path)
	m.files = untimedFileCheck()
	m.now = func() time.Time { return listFixtureEpoch }
	cur, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = cur.(*ListModel)
	m.takeRecents(recentsFrom(path, m.files))
	m.applyRefresh(items)
	return m
}

// writeDoc writes a markdown file for a path-only entry to name.
func writeDoc(t *testing.T, name, doc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRecentlyOpenedListsWhatWasOpenedNewestFirst(t *testing.T) {
	notes := writeDoc(t, "notes.md", "# Scratch notes\n\nbody\n")
	items := listSectionItems("l_", 4)
	m := newRecentList(t, items,
		recent.Entry{PlanID: items[3].plan.ID},
		recent.Entry{Path: notes},
		recent.Entry{PlanID: items[0].plan.ID},
	)
	if got, want := sectionBodyText(m, sectionRecent), []string{items[3].plan.Title, "Scratch notes", items[0].plan.Title}; !slices.Equal(got, want) {
		t.Fatalf("Recently opened = %q, want %q", got, want)
	}
	// The catalog is whole: a plan in Recently opened is still in "Your plans".
	if got := sectionBodyText(m, sectionMine); !slices.Equal(got, titlesOf(items)) {
		t.Fatalf("Your plans = %q, want all four plans", got)
	}
	if m.rows[0].section != sectionRecent || !m.rows[0].head {
		t.Fatalf("first row = %+v, want Recently opened's band on top", m.rows[0])
	}
	// What is true of every row in it is when each was opened, so that labels
	// the region -- not "Your plans"' SOURCE.
	if labels := m.rows[1].text; !strings.Contains(labels, listOpenedLabel) || strings.Contains(labels, listSourceLabel) {
		t.Fatalf("Recently opened's column labels = %q, want %s alone in the region", labels, listOpenedLabel)
	}
}

// TestRecentlyOpenedDrawsWhatStillResolves pins how an entry resolves to a
// row, and the section's five-row cap: an entry is drawn as the loaded plan it
// names, by id and then by path, or as the file it opened, by its name alone
// once the file is gone. A plan that is no longer listed is skipped and the
// next entry takes its slot.
func TestRecentlyOpenedDrawsWhatStillResolves(t *testing.T) {
	notes := writeDoc(t, "notes.md", "# Scratch notes\n\nbody\n")
	moved := filepath.Join(t.TempDir(), "moved.md")
	items := listSectionItems("l_", 7)
	items[0].plan.SourceHint = notes // notes.md has become Plan 000
	items[2].plan.SourceHint = moved // and moved.md Plan 002, then left
	gone := filepath.Join(t.TempDir(), "gone.md")
	dir := t.TempDir()
	titles := func(its ...planItem) []string {
		var out []string
		for _, it := range its {
			out = append(out, it.plan.Title)
		}
		return out
	}
	byID := func(its ...planItem) []recent.Entry {
		var out []recent.Entry
		for _, it := range its {
			out = append(out, recent.Entry{PlanID: it.plan.ID})
		}
		return out
	}
	for _, tc := range []struct {
		name    string
		entries []recent.Entry
		want    []string
	}{
		{"the newest five", byID(items...), titles(items[:5]...)},
		// The fourth edge: a plan deleted elsewhere, opened by id, has no name
		// to draw and nothing to open.
		{"a plan no longer listed lets the sixth in",
			append([]recent.Entry{{PlanID: "l_gone"}}, byID(items[1:]...)...), titles(items[1:6]...)},
		{"a file that is gone draws its name",
			[]recent.Entry{{Path: gone}, {PlanID: items[1].plan.ID}}, []string{"gone", items[1].plan.Title}},
		// A device reads without error, so only the regular-file check marks it
		// gone; the same check is what keeps a path that became a FIFO from
		// blocking the load that reads it.
		{"a path that is no longer a regular file is gone too",
			[]recent.Entry{{Path: dir}, {Path: os.DevNull}, {PlanID: items[1].plan.ID}},
			[]string{filepath.Base(dir), "null", items[1].plan.Title}},
		{"a file that became a plan draws the plan",
			[]recent.Entry{{Path: notes}}, titles(items[0])},
		{"a gone file a plan carries draws the plan",
			[]recent.Entry{{Path: moved}}, titles(items[2])},
		{"two entries naming one plan draw it once",
			[]recent.Entry{{PlanID: items[0].plan.ID}, {Path: notes}, {PlanID: items[1].plan.ID}}, titles(items[0], items[1])},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newRecentList(t, items, tc.entries...)
			if got := sectionBodyText(m, sectionRecent); !slices.Equal(got, tc.want) {
				t.Fatalf("Recently opened = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAFileWhoseStatDidNotAnswerIsNotDrawn pins the "don't know" case: a file
// whose stat outran the budget is neither drawn nor called gone. It is
// skipped, and the next entry takes its slot.
func TestAFileWhoseStatDidNotAnswerIsNotDrawn(t *testing.T) {
	items := listSectionItems("l_", 1)
	path := writeRecents(t, recent.Entry{Path: "/mnt/hung/notes.md"}, recent.Entry{PlanID: items[0].plan.ID})
	rows := recentJoin(recentsFrom(path, newFileCheck(newFakeStat(t, nil).stat, os.ReadFile, spentBudget)).entries, items)
	if len(rows) != 1 || rows[0].kind != rowPlan {
		t.Fatalf("rows = %+v; want the plan alone", rows)
	}
}

// TestRecentlyOpenedStandsAside pins the empty section and the catalog-only
// modes: in each, no row of the section is drawn at all.
func TestRecentlyOpenedStandsAside(t *testing.T) {
	items := listSectionItems("l_", 3)
	for _, tc := range []struct {
		name  string
		setup func(m *ListModel) *ListModel
	}{
		{"with nothing to show", nil},
		{"while a filter narrows the catalog", func(m *ListModel) *ListModel { m.filter = "Plan"; m.rebuildRows(); return m }},
		{"while the filter is being typed", func(m *ListModel) *ListModel { m.setMode(listFilter); return m }},
		{"while a section is expanded", func(m *ListModel) *ListModel { m.setMode(listExpanded); return m }},
		{"under the rename panel", func(m *ListModel) *ListModel { return pressList(m, "e") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m *ListModel
			if tc.setup == nil {
				m = newRecentList(t, items)
			} else {
				m = tc.setup(newRecentList(t, items, recent.Entry{PlanID: items[0].plan.ID}))
			}
			for _, r := range m.rows {
				if r.section == sectionRecent {
					t.Fatalf("a Recently opened row is drawn: %+v", r)
				}
			}
		})
	}
}

// recentLines is the rendered screen from Recently opened's band down to the
// line before "Your plans", ANSI stripped.
func recentLines(t *testing.T, m *ListModel) []string {
	t.Helper()
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	start, end := -1, -1
	for i, l := range lines {
		switch {
		case start < 0 && strings.Contains(l, listRecentSectionTitle):
			start = i
		case start >= 0 && strings.Contains(l, listMineSectionTitle):
			end = i
		}
		if end >= 0 {
			break
		}
	}
	if start < 0 || end < 0 {
		t.Fatalf("no Recently opened section on screen:\n%s", strings.Join(lines, "\n"))
	}
	return lines[start:end]
}

// TestRecentlyOpenedSaysWhenEachWasOpened pins the section's three columns: a
// plan keeps its own glyph and counts, a file draws what a plan with no
// threads draws, and both carry OPENED where the catalog carries SOURCE. A
// file that is gone draws a dash and its name, and leaves COMMENTS blank,
// since it has no threads to count.
func TestRecentlyOpenedSaysWhenEachWasOpened(t *testing.T) {
	notes := writeDoc(t, "notes.md", "# Scratch notes\n\nbody\n")
	gone := filepath.Join(t.TempDir(), "rollout.md")
	items := listSectionItems("l_", 1)
	items[0].open, items[0].total = 1, 3
	m := newRecentList(t, items,
		recent.Entry{PlanID: items[0].plan.ID, OpenedAt: listFixtureEpoch.Add(-5 * time.Minute)},
		recent.Entry{Path: notes, OpenedAt: listFixtureEpoch.Add(-2 * time.Hour)},
		recent.Entry{Path: gone, OpenedAt: listFixtureEpoch.Add(-3 * 24 * time.Hour)},
	)
	lines := recentLines(t, m)
	if !strings.Contains(lines[1], "NAME") || !strings.Contains(lines[1], "COMMENTS") || !strings.Contains(lines[1], listOpenedLabel) || strings.Contains(lines[1], listSourceLabel) {
		t.Fatalf("column labels = %q, want NAME, COMMENTS and %s", lines[1], listOpenedLabel)
	}
	commentsAt := strings.Index(lines[1], "COMMENTS") // an ASCII line, so the byte is the cell
	for _, tc := range []struct {
		title, glyph, counts, opened string
	}{
		{items[0].plan.Title, "◐", "2/3", "5m ago"},
		// A file with no plan behind it draws what a plan with no comments
		// draws, and a plan with no comments now draws blank, not 0/0 --
		// this branch's own extension of that rule (formatCounts).
		{"Scratch notes", "○", "", "2h ago"},
		{"rollout", "─", "", "3d ago"},
	} {
		var line string
		for _, l := range lines {
			if strings.Contains(l, tc.title) {
				line = l
			}
		}
		if !strings.Contains(line, tc.glyph+" "+tc.title) || !strings.Contains(line, tc.opened) {
			t.Fatalf("row %q = %q, want %q before its title and %q", tc.title, line, tc.glyph, tc.opened)
		}
		if cell := strings.TrimSpace(string([]rune(line)[commentsAt : commentsAt+len("COMMENTS")])); cell != tc.counts {
			t.Fatalf("row %q's COMMENTS = %q, want %q", tc.title, cell, tc.counts)
		}
	}
}

// TestAMissingFileDrawsOneDashInBothCopies pins this in Recently opened too:
// the copy of a plan whose file is gone draws the ─ its catalog row draws, and
// a plan whose file is there keeps its circle in both.
func TestAMissingFileDrawsOneDashInBothCopies(t *testing.T) {
	items := listSectionItems("l_", 2)
	items[0].fileMissing = true
	m := newRecentList(t, items, recent.Entry{PlanID: items[0].plan.ID}, recent.Entry{PlanID: items[1].plan.ID})
	for _, section := range []listSection{sectionRecent, sectionMine} {
		if got := drawnGlyph(t, m, section, items[0].plan.Title); got != '─' {
			t.Errorf("%s draws %q for the plan whose file is gone, want ─", section.title(), got)
		}
		if got := drawnGlyph(t, m, section, items[1].plan.Title); got != '○' {
			t.Errorf("%s draws %q for the plan whose file is there, want ○", section.title(), got)
		}
	}
}

// TestAFileRowNamesItsPathOnTheStatusBar pins this: a file has no SOURCE cell,
// so the status bar is where its path shows, as a selected plan's source
// does. A file that is gone is still a file row: its bar names where it was,
// and its help bar is the file row's own help bar.
func TestAFileRowNamesItsPathOnTheStatusBar(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{"a file that reads", writeDoc(t, "notes.md", "# Scratch notes\n\nbody\n")},
		{"a file that is gone", filepath.Join(t.TempDir(), "rollout.md")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newRecentList(t, nil, recent.Entry{Path: tc.path})
			if r, _ := m.cursorRow(); r.kind != rowFile {
				t.Fatalf("cursor row = %+v, want the file row", r)
			}
			bar := strings.TrimSpace(ansi.Strip(m.statusBarText(400, m.styles.StatusBar)))
			if !strings.HasSuffix(bar, homeRelative(tc.path)) {
				t.Fatalf("status bar = %q, want it to end in %q", bar, homeRelative(tc.path))
			}
			if hint, want := m.hintLine(), "enter open · / filter · d remove · ? keys · q quit"; hint != want {
				t.Fatalf("help bar = %q, want %q", hint, want)
			}
		})
	}
}

// TestSortIsNotOfferedInRecentlyOpened pins the s key and the help bar it
// offers. Recently opened has no order to choose -- it is always in the order
// things were opened -- and a bar naming s there would advertise a key
// that does nothing. A file row has no plan to rename, delete or inspect, so
// its bar names only what it answers.
func TestSortIsNotOfferedInRecentlyOpened(t *testing.T) {
	notes := writeDoc(t, "notes.md", "# Scratch notes\n\nbody\n")
	items := listSectionItems("l_", 2)
	m := newRecentList(t, items, recent.Entry{PlanID: items[0].plan.ID}, recent.Entry{Path: notes})
	for _, tc := range []struct {
		name    string
		section listSection
		kind    rowKind
		hint    string // "" = only assert on sort
		sorts   bool
	}{
		{"a plan in Recently opened", sectionRecent, rowPlan, "", false},
		{"a file in Recently opened", sectionRecent, rowFile, "enter open · / filter · d remove · ? keys · q quit", false},
		{"a plan in Your plans", sectionMine, rowPlan, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.cursor = -1
			for i, r := range m.rows {
				if r.section == tc.section && r.kind == tc.kind {
					m.cursor = i
					break
				}
			}
			if m.cursor < 0 {
				t.Fatalf("no row of kind %d in section %d", tc.kind, tc.section)
			}
			hint := m.hintLine()
			if got := strings.Contains(hint, "s sort"); got != tc.sorts {
				t.Fatalf("hint %q offers sort = %v, want %v", hint, got, tc.sorts)
			}
			if tc.hint != "" && hint != tc.hint {
				t.Fatalf("hint = %q, want %q", hint, tc.hint)
			}
			if after := pressList(m, "s"); (after.mode == listSort) != tc.sorts {
				t.Fatalf("s opened the sort modal = %v, want %v", after.mode == listSort, tc.sorts)
			}
			m.setMode(listBrowse)
		})
	}
}

func TestAllocateRecentSeatsItselfFirstAndKeepsTheFloor(t *testing.T) {
	for _, tc := range []struct {
		name               string
		budget, want, rows int
	}{
		{"all it wants", 14, 5, 5},
		{"nothing to draw", 14, 0, 0},
		{"trimmed to leave Your plans its floor", 8, 5, 4},
		{"one row is the least it draws", 5, 5, 1},
		{"no room for a row draws no section", 4, 5, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := allocateRecent(tc.budget, tc.want); got != tc.rows {
				t.Fatalf("allocateRecent(%d, %d) = %d, want %d", tc.budget, tc.want, got, tc.rows)
			}
		})
	}
}

// TestTheRefreshReadsRecentlyOpened drives the real first load: recent.json is
// read off the loop beside the plans, with no call made to draw it.
func TestTheRefreshReadsRecentlyOpened(t *testing.T) {
	f := newListFixture(t)
	sess := seedListPlan(t, f, filepath.Dir(f.path), "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody.\n")
	path := filepath.Join(t.TempDir(), "recent.json")
	if err := RecordOpen(path, sess, listFixtureEpoch); err != nil {
		t.Fatal(err)
	}
	m := NewList(f.svc, keymap.Default(), nil)
	m.SetRecents(path)
	m = drainList(t, m, m.Init())
	if got := sectionBodyText(m, sectionRecent); !slices.Equal(got, []string{"Rate Limiter"}) {
		t.Fatalf("Recently opened after the first load = %q, want the plan", got)
	}
}

// TestTheCursorRidesTheTopUntilTheFirstInput pins this rule: untouched, the
// cursor stays on Recently opened's top row through every load,
// so enter opens the last thing opened even when a later load puts something
// newer there; once a human has moved, the cursor stays where they put it.
func TestTheCursorRidesTheTopUntilTheFirstInput(t *testing.T) {
	plans := freshestFirstPlans(4) // l_p00 .. l_p03
	a, b, c, d := plans[0], plans[1], plans[2], plans[3]
	for _, tc := range []struct {
		name        string
		input       func(t *testing.T, m *ListModel) tea.Msg // delivered between the two loads; nil for none
		wantSection listSection
		wantID      domain.PlanID
	}{
		{"untouched, it lands on Recently opened's new top row", nil, sectionRecent, c.ID},
		{"a key lets go of the top",
			func(*testing.T, *ListModel) tea.Msg { return tea.KeyPressMsg{Code: 'j', Text: "j"} },
			sectionRecent, b.ID},
		{"a wheel lets go of the top",
			func(*testing.T, *ListModel) tea.Msg { return tea.MouseWheelMsg{Button: tea.MouseWheelDown} },
			sectionRecent, b.ID},
		{"a click lets go of the top",
			func(t *testing.T, m *ListModel) tea.Msg {
				y := frameLineContaining(t, strings.Split(m.View().Content, "\n"), d.Title)
				return tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft}
			},
			sectionMine, d.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeRecents(t, recent.Entry{PlanID: a.ID}, recent.Entry{PlanID: b.ID})
			m := NewList(listOfSvc{plans: plans}, keymap.Default(), nil)
			m.SetRecents(path)
			m.files = untimedFileCheck()
			m = drainList(t, m, m.Init())
			if r, _ := m.cursorRow(); r.section != sectionRecent || r.item.plan.ID != a.ID {
				t.Fatalf("test setup: after the first load the cursor is on %v %q, want Recently opened's %q", r.section, r.item.plan.ID, a.ID)
			}
			if tc.input != nil {
				m = deliverList(m, tc.input(t, m))
			}
			// Another door opens c: it is the newest entry by the next load.
			if err := recent.Touch(path, recent.Entry{PlanID: c.ID}); err != nil {
				t.Fatal(err)
			}
			m = drainList(t, m, m.refreshCmd())
			r, _ := m.cursorRow()
			if r.section != tc.wantSection || r.item.plan.ID != tc.wantID {
				t.Fatalf("cursor on %v %q, want %v %q", r.section, r.item.plan.ID, tc.wantSection, tc.wantID)
			}
		})
	}
}

// TestWithoutRecentlyOpenedTheCursorKeepsItsPlan: the rule above is Recently
// opened's alone, so a list with no recent.json follows identity
// through a load as it always has -- here past a plan the next load sorts
// above the one under the cursor.
func TestWithoutRecentlyOpenedTheCursorKeepsItsPlan(t *testing.T) {
	svc := &flakyListSvc{plans: freshestFirstPlans(2)}
	m := NewList(svc, keymap.Default(), nil)
	m = drainList(t, m, m.Init())
	first, ok := m.selectedPlan()
	if !ok {
		t.Fatal("test setup: the cursor is on no plan")
	}
	svc.plans = append(svc.plans, domain.Plan{ID: "l_newer", Title: "Newer draft", LastActivityAt: listFixtureEpoch.Add(time.Minute)})
	m = drainList(t, m, m.refreshCmd())
	if it, ok := m.selectedPlan(); !ok || it.plan.ID != first.plan.ID {
		t.Fatalf("cursor on %q (%v), want it still on %q", it.plan.ID, ok, first.plan.ID)
	}
}

// cursorTo puts the cursor on the first row of kind in section, as a click
// would, for a test that starts on a particular copy of a plan drawn twice.
func cursorTo(t *testing.T, m *ListModel, section listSection, kind rowKind) *ListModel {
	t.Helper()
	for i, r := range m.rows {
		if r.section == section && r.kind == kind {
			m.cursor = i
			return m
		}
	}
	t.Fatalf("no %v row in %v", kind, section)
	return nil
}

// TestACopyKeepsItsCursor pins this: a plan in Recently opened is drawn
// twice, and a rebuild keeps the cursor on the copy it was on -- also
// there and back through a body that draws it once: the rename panel's flat
// one, and the filter's.
func TestACopyKeepsItsCursor(t *testing.T) {
	items := listSectionItems("l_", 3)
	for _, section := range []listSection{sectionRecent, sectionMine} {
		t.Run(section.title(), func(t *testing.T) {
			thereAndBack := func(key string, via listMode) func(m *ListModel) *ListModel {
				return func(m *ListModel) *ListModel {
					m = pressList(m, key)
					if m.mode != via {
						t.Fatalf("%s opened %v, want %v", key, m.mode, via)
					}
					m, _ = pressListKey(m, "esc")
					return m
				}
			}
			for _, tc := range []struct {
				name string
				trip func(m *ListModel) *ListModel
			}{
				{"across a refresh", func(m *ListModel) *ListModel { m.applyRefresh(items); return m }},
				{"there and back through a panel", thereAndBack("e", listRename)},
				{"there and back through the filter", thereAndBack("/", listFilter)},
			} {
				t.Run(tc.name, func(t *testing.T) {
					m := newRecentList(t, items, recent.Entry{PlanID: items[1].plan.ID})
					m = cursorTo(t, m, section, rowPlan)
					if section == sectionMine {
						m.moveCursor(1) // Plan 001, the plan drawn twice
					}
					m = tc.trip(m)
					if r, _ := m.cursorRow(); r.section != section || r.item.plan.ID != items[1].plan.ID {
						t.Fatalf("cursor on %v %q, want %v %q", r.section, r.item.plan.ID, section, items[1].plan.ID)
					}
				})
			}
		})
	}
}

// TestACursorOnAFileRowFollowsItsFile: a file row has no plan id, so the
// cursor re-finds it by path, and an open recorded above it does not slide it
// onto the plan that took its place.
func TestACursorOnAFileRowFollowsItsFile(t *testing.T) {
	notes := writeDoc(t, "notes.md", "# Scratch notes\n\nbody\n")
	items := listSectionItems("l_", 2)
	m := newRecentList(t, items, recent.Entry{Path: notes})
	m = cursorTo(t, m, sectionRecent, rowFile)
	if err := recent.Touch(m.recentPath, recent.Entry{PlanID: items[1].plan.ID}); err != nil {
		t.Fatal(err)
	}
	m.takeRecents(recentsFrom(m.recentPath, m.files))
	m.rebuildRows()
	if r, _ := m.cursorRow(); r.kind != rowFile || r.path != notes {
		t.Fatalf("cursor on %+v, want the file row it was on, now one lower", r)
	}
}

// TestAFilterHoldsTheCopyForItsPlan pins the same rule through the filter,
// whose bodies draw each plan once: both ways out of it land on the copy the
// cursor went in on, and a plan the cursor was moved to inside it comes back on
// its own section's copy, not the copy held for another plan.
func TestAFilterHoldsTheCopyForItsPlan(t *testing.T) {
	items := listSectionItems("l_", 3)
	for _, tc := range []struct {
		name        string
		keys        []string // after "/ plan", before the esc that clears it
		wantSection listSection
		wantID      domain.PlanID
	}{
		{"esc while typing", nil, sectionRecent, items[1].plan.ID},
		{"enter, then esc", []string{"enter"}, sectionRecent, items[1].plan.ID},
		{"enter, down a plan, then esc", []string{"enter", "j"}, sectionMine, items[2].plan.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newRecentList(t, items, recent.Entry{PlanID: items[1].plan.ID}, recent.Entry{PlanID: items[2].plan.ID})
			m = cursorTo(t, m, sectionRecent, rowPlan)
			m = pressList(m, append([]string{"/", "p", "l", "a", "n"}, tc.keys...)...)
			m, _ = pressListKey(m, "esc")
			if m.filterActive() {
				t.Fatalf("filter %q still on, want it cleared", m.filter)
			}
			if r, _ := m.cursorRow(); r.section != tc.wantSection || r.item.plan.ID != tc.wantID {
				t.Fatalf("cursor on %v %q, want %v %q", r.section, r.item.plan.ID, tc.wantSection, tc.wantID)
			}
		})
	}
}

// TestAFilterHoldsTheCopyOfAPlanWithNoRoom is the same two exits for a plan its
// own section has no room for, so that once the filter clears only Recently
// opened draws it: the query narrows to it, and both exits widen straight back
// onto its Recently opened copy.
func TestAFilterHoldsTheCopyOfAPlanWithNoRoom(t *testing.T) {
	items := listSectionItems("l_", 40)
	deep := items[30]
	for _, tc := range []struct {
		name string
		keys []string // after "/ 030", before the esc that clears it
	}{
		{"esc while typing", nil},
		{"enter, then esc", []string{"enter"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newRecentList(t, items, recent.Entry{PlanID: deep.plan.ID})
			m = cursorTo(t, m, sectionRecent, rowPlan)
			m = pressList(m, append([]string{"/", "0", "3", "0"}, tc.keys...)...)
			if it, ok := m.selectedPlan(); !ok || it.plan.ID != deep.plan.ID {
				t.Fatalf("the filter left the cursor on %q, want %q, its one match", it.plan.ID, deep.plan.ID)
			}
			m, _ = pressListKey(m, "esc")
			if r, _ := m.cursorRow(); r.section != sectionRecent || r.item.plan.ID != deep.plan.ID {
				t.Fatalf("cursor on %v %v %q, want %v %q", r.kind, r.section, r.item.plan.ID, sectionRecent, deep.plan.ID)
			}
		})
	}
}

// cursorToPlan puts the cursor on plan id's row, as a click would, in a body
// that draws the plan once.
func cursorToPlan(t *testing.T, m *ListModel, id domain.PlanID) *ListModel {
	t.Helper()
	i, ok := m.planRowIndex(id)
	if !ok {
		t.Fatalf("%s is not drawn", id)
	}
	m.cursor = i
	return m
}

// TestAPlanWithNoRoomGoesToItsSectionsMore pins the fallback: when a body
// shrinks under a plan its own section has no room for, the cursor
// goes to that section's +N more, as it did before Recently opened existed --
// not to the plan's Recently opened copy, and not to whichever plan its old
// position now holds, which Recently opened's rows above the section shift.
func TestAPlanWithNoRoomGoesToItsSectionsMore(t *testing.T) {
	items := listSectionItems("l_", 40)
	expandTo := func(id domain.PlanID) func(*testing.T, *ListModel) *ListModel {
		return func(t *testing.T, m *ListModel) *ListModel {
			m = pressList(cursorTo(t, m, sectionMine, rowMore), "enter")
			if m.mode != listExpanded {
				t.Fatalf("enter on +N more opened %v, want %v", m.mode, listExpanded)
			}
			m, _ = pressListKey(cursorToPlan(t, m, id), "esc")
			return m
		}
	}
	for _, tc := range []struct {
		name    string
		recents []domain.PlanID
		trip    func(*testing.T, *ListModel) *ListModel
	}{
		{"leaving expanded", nil, expandTo(items[30].plan.ID)},
		{"leaving expanded, the plan in Recently opened", []domain.PlanID{items[30].plan.ID}, expandTo(items[30].plan.ID)},
		{"leaving expanded, its Recently opened copy held by an earlier rebuild",
			[]domain.PlanID{items[30].plan.ID},
			func(t *testing.T, m *ListModel) *ListModel {
				m = cursorTo(t, m, sectionRecent, rowPlan)
				m.applyRefresh(items)
				return expandTo(items[30].plan.ID)(t, m)
			}},
		// At 100x30 "Your plans" draws plans 0-15 under one Recently opened row,
		// so plan 17's expanded row is plan 13's once collapsed.
		{"leaving expanded, its old position now another plan's", []domain.PlanID{items[39].plan.ID}, expandTo(items[17].plan.ID)},
		// At 100x21 "Your plans" draws plans 0-2 under five Recently opened rows,
		// so plan 4's expanded row is its own Recently opened copy once collapsed.
		{"leaving expanded, its old position now its own Recently opened copy",
			[]domain.PlanID{items[0].plan.ID, items[1].plan.ID, items[2].plan.ID, items[3].plan.ID, items[4].plan.ID},
			func(t *testing.T, m *ListModel) *ListModel {
				cur, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 21})
				return expandTo(items[4].plan.ID)(t, cur.(*ListModel))
			}},
		{"clearing a filter, the plan in Recently opened", []domain.PlanID{items[35].plan.ID},
			func(t *testing.T, m *ListModel) *ListModel {
				m = pressList(cursorTo(t, m, sectionMine, rowPlan), "/", "0", "3", "enter")
				m, _ = pressListKey(cursorToPlan(t, m, items[35].plan.ID), "esc")
				return m
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var entries []recent.Entry
			for _, id := range tc.recents {
				entries = append(entries, recent.Entry{PlanID: id})
			}
			m := tc.trip(t, newRecentList(t, items, entries...))
			if r, _ := m.cursorRow(); r.kind != rowMore || r.section != sectionMine {
				t.Fatalf("cursor on %v %v %q, want %v's +N more", r.kind, r.section, r.item.plan.ID, sectionMine)
			}
		})
	}
}

// TestAReSortKeepsThePositionOfAPlanItPushesOut is commitSort's own rule:
// a re-sort leaves the body's shape alone, so a plan it
// pushes behind +N more leaves the cursor on the line the reader was looking at,
// even while Recently opened still draws that plan.
func TestAReSortKeepsThePositionOfAPlanItPushesOut(t *testing.T) {
	items := listSectionItems("l_", 40)
	items[0].plan.Title = "zzz" // first by activity, last by name
	m := newRecentList(t, items, recent.Entry{PlanID: items[0].plan.ID})
	m = cursorTo(t, m, sectionMine, rowPlan)
	at := m.cursor
	m = pressList(m, "s", "j", "j", "enter")
	if got := m.sortOrder.column; got != sortName {
		t.Fatalf("Your plans sorted by %v, want %v", got, sortName)
	}
	if r, _ := m.cursorRow(); m.cursor != at || r.section != sectionMine || r.item.plan.ID == items[0].plan.ID {
		t.Fatalf("cursor on row %d, %v %q; want row %d, the Your plans plan now there", m.cursor, r.section, r.item.plan.ID, at)
	}
}

// TestACentredPanelLeavesTheBodyBehindIt pins this for a centred box: it
// composes over browse's own body, so opening one from a Recently
// opened row, and closing it again, moves no row and no cursor.
func TestACentredPanelLeavesTheBodyBehindIt(t *testing.T) {
	items := listSectionItems("l_", 3)
	for _, tc := range []struct {
		key string
		via listMode
	}{
		{"i", listInfo},
		{"?", listKeys},
		{"d", listConfirmDelete},
	} {
		t.Run(tc.key, func(t *testing.T) {
			m := newRecentList(t, items, recent.Entry{PlanID: items[1].plan.ID})
			m = cursorTo(t, m, sectionRecent, rowPlan)
			rows, cursor := slices.Clone(m.rows), m.cursor
			for _, step := range []struct {
				key  string
				want listMode
			}{{tc.key, tc.via}, {"esc", listBrowse}} {
				m, _ = pressListKey(m, step.key)
				if m.mode != step.want {
					t.Fatalf("%s opened %v, want %v", step.key, m.mode, step.want)
				}
				if !slices.Equal(m.rows, rows) || m.cursor != cursor {
					t.Fatalf("after %s: %d rows, cursor %d; want the %d rows behind it, cursor %d", step.key, len(m.rows), m.cursor, len(rows), cursor)
				}
			}
		})
	}
}

// TestEnterOnAFileRowOpensTheFile pins enter on a file row: it opens exactly
// as `draftplane review <path>` would, with no plan yet, and the open is
// re-recorded as newest -- the same door records what it just opened.
func TestEnterOnAFileRowOpensTheFile(t *testing.T) {
	f := newListFixture(t)
	notes := writeDoc(t, "notes.md", "# Scratch notes\n\nbody\n")
	path := filepath.Join(t.TempDir(), "recent.json")
	if err := recent.Touch(path, recent.Entry{Path: notes, OpenedAt: listFixtureEpoch}); err != nil {
		t.Fatal(err)
	}
	r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
	r.list.files = untimedFileCheck()
	r.SetRecents(path)
	r = drainRoot(t, r, r.Init())
	cur, cmd := r.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // the pinned cursor is on the file
	r = drainRoot(t, cur.(*Root), cmd)
	if !r.inReview || r.review.sess.Path != notes || r.review.sess.Exists {
		t.Fatalf("inReview=%v, want a review of %s with no plan yet", r.inReview, notes)
	}
	got, _ := recent.Load(path)
	if len(got) != 1 || got[0].Path != notes || !got[0].OpenedAt.After(listFixtureEpoch) {
		t.Fatalf("recent.json = %+v, want the file re-remembered as newest", got)
	}
}

// TestEnterOnAGoneFileRowSaysWhyItDidNotOpen pins what enter does here: the
// open is tried exactly as on any file row, since a file put back since the
// last refresh must open, and nothing is re-recorded when it fails. A file
// that is not there says so in its own words, whose d clears the row; any
// other read failure shows the read error's own line.
func TestEnterOnAGoneFileRowSaysWhyItDidNotOpen(t *testing.T) {
	for _, tc := range []struct {
		name     string
		path     string
		notFound bool
	}{
		{"a file that is not there", filepath.Join(t.TempDir(), "rollout.md"), true},
		{"a path that will not read", t.TempDir(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newListFixture(t)
			path := writeRecents(t, recent.Entry{Path: tc.path, OpenedAt: listFixtureEpoch})
			r := NewRoot(f.svc, keymap.Default(), nil, "", nil)
			r.list.files = untimedFileCheck()
			r.SetRecents(path)
			r = drainRoot(t, r, r.Init())
			if row, _ := r.list.cursorRow(); row.kind != rowFile || !row.gone || row.path != tc.path {
				t.Fatalf("pinned cursor on %+v, want the gone file's row", row)
			}
			cur, cmd := r.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			r = drainRoot(t, cur.(*Root), cmd)
			if r.inReview {
				t.Fatalf("a review of %s opened, want none", r.review.sess.Path)
			}
			if got, _ := recent.Load(path); len(got) != 1 || !got[0].OpenedAt.Equal(listFixtureEpoch) {
				t.Fatalf("recent.json = %+v, want the failed open left unrecorded", got)
			}
			if !tc.notFound {
				if errors.Is(r.list.err, fs.ErrNotExist) || r.list.status != "error: "+r.list.err.Error() {
					t.Fatalf("status = %q (err %v), want the read failure's error line", r.list.status, r.list.err)
				}
				return
			}
			if want := "file not found · d to clear"; r.list.status != want {
				t.Fatalf("status = %q, want %q", r.list.status, want)
			}
			cur, cmd = r.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
			r = drainRoot(t, cur.(*Root), cmd)
			if _, drawn := r.list.fileRowIndex(tc.path); drawn || r.list.status != "" {
				t.Fatalf("after d: row drawn = %v, status = %q; want the row and the sentence gone", drawn, r.list.status)
			}
		})
	}
}

// TestOnlyAFileRowSaysFileNotFound pins the scope of that message: an open
// from any other door keeps the read error's own line, even when its file is
// not there.
func TestOnlyAFileRowSaysFileNotFound(t *testing.T) {
	r := NewRoot(newListFixture(t).svc, keymap.Default(), nil, "", nil)
	err := fmt.Errorf("%w: %w", session.ErrSourceUnreadable, fs.ErrNotExist)
	r.Update(msgSessionOpened{seq: r.openSeq, err: err})
	if want := "error: " + err.Error(); r.list.status != want {
		t.Fatalf("status = %q, want %q", r.list.status, want)
	}
}

// TestDOnAFileRowRemovesItAtOnce pins the other half of that gesture: there is
// nothing to destroy, so d takes the row out at once, with no confirm panel,
// and forgets it on disk without waiting on the write. A file that is gone
// goes the same way, which is the one way to clear it.
func TestDOnAFileRowRemovesItAtOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{"a file that reads", writeDoc(t, "notes.md", "# Scratch notes\n\nbody\n")},
		{"a file that is gone", filepath.Join(t.TempDir(), "rollout.md")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := listSectionItems("l_", 1)
			m := newRecentList(t, items, recent.Entry{Path: tc.path}, recent.Entry{PlanID: items[0].plan.ID})
			m = cursorTo(t, m, sectionRecent, rowFile)
			cur, cmd := m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
			m = cur.(*ListModel)
			if m.mode != listBrowse {
				t.Fatalf("d on a file row opened %v, want no panel", m.mode)
			}
			if got := sectionBodyText(m, sectionRecent); !slices.Equal(got, []string{items[0].plan.Title}) {
				t.Fatalf("before the write lands, Recently opened = %q, want the file already gone", got)
			}
			m = drainList(t, m, cmd)
			if got, _ := recent.Load(m.recentPath); slices.ContainsFunc(got, func(e recent.Entry) bool { return e.Path == tc.path }) {
				t.Fatalf("recent.json still remembers %s: %+v", tc.path, got)
			}
		})
	}
}

// TestDeletingAPlanTakesBothCopies pins the plan delete: today's confirm, and
// on y both the catalog row and the Recently opened copy leave in the same
// redraw, whichever copy the cursor was on when d was pressed.
func TestDeletingAPlanTakesBothCopies(t *testing.T) {
	for _, from := range []listSection{sectionRecent, sectionMine} {
		t.Run(from.title(), func(t *testing.T) {
			f := newListFixture(t)
			sess := seedListPlan(t, f, filepath.Dir(f.path), "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody.\n")
			path := filepath.Join(t.TempDir(), "recent.json")
			if err := RecordOpen(path, sess, listFixtureEpoch); err != nil {
				t.Fatal(err)
			}
			m := NewList(f.svc, keymap.Default(), nil)
			m.SetRecents(path)
			m = drainList(t, m, m.Init())
			m = cursorTo(t, m, from, rowPlan)
			m = pressList(m, "d")
			if m.mode != listConfirmDelete {
				t.Fatalf("d opened %v, want today's delete confirm", m.mode)
			}
			cur, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
			m = drainList(t, cur.(*ListModel), cmd)
			for _, r := range m.rows {
				if r.kind == rowPlan && r.item.plan.ID == sess.Plan.ID {
					t.Fatalf("a copy of the deleted plan is still drawn in %v", r.section)
				}
			}
			if got, _ := recent.Load(path); len(got) != 0 {
				t.Fatalf("recent.json = %+v, want the deleted plan forgotten", got)
			}
		})
	}

	// A file opened before it had a plan holds a path-only entry even once a
	// plan later appears at that path -- nothing re-records it by id. Its
	// Recently opened copy still resolves as the plan (matched by path), so
	// deleting it must forget by path, not by id: dropping the id-matched entry
	// alone would leave a path-only orphan, and the file would come back as a
	// file row instead of leaving Recently opened altogether.
	t.Run("a file viewed before it had a plan", func(t *testing.T) {
		f := newListFixture(t)
		sess := seedListPlan(t, f, filepath.Dir(f.path), "plan.md", "Rate Limiter", "# Rate Limiter\n\nBody.\n")
		path := filepath.Join(t.TempDir(), "recent.json")
		if err := recent.Touch(path, recent.Entry{Path: sess.Path}); err != nil {
			t.Fatal(err)
		}
		m := NewList(f.svc, keymap.Default(), nil)
		m.files = untimedFileCheck() // so "the file came back" is read from the file, never the clock
		m.SetRecents(path)
		m = drainList(t, m, m.Init())
		m = cursorTo(t, m, sectionRecent, rowPlan)
		m = pressList(m, "d")
		if m.mode != listConfirmDelete {
			t.Fatalf("d opened %v, want today's delete confirm", m.mode)
		}
		cur, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
		m = drainList(t, cur.(*ListModel), cmd)
		for _, r := range m.rows {
			if r.kind == rowPlan && r.item.plan.ID == sess.Plan.ID {
				t.Fatalf("a copy of the deleted plan is still drawn in %v", r.section)
			}
			if r.kind == rowFile && r.path == sess.Path {
				t.Fatalf("the file came back as a %v row in %v, want it forgotten too", r.kind, r.section)
			}
		}
		if got, _ := recent.Load(path); len(got) != 0 {
			t.Fatalf("recent.json = %+v, want the path-only entry forgotten too", got)
		}
	})
}

package app

import (
	"context"
	"io/fs"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/draftplane/draftplane/client/recent"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/ui"
)

// recentEntry is one recent.json entry as the list holds it: the file's own
// facts, plus what a path-only entry's file said, read off the loop at load
// time so drawing the section touches no disk.
type recentEntry struct {
	id    domain.PlanID // the plan the open landed on; "" for a file that had none
	path  string        // the file opened; "" for a plan opened by id alone
	title string        // a path-only entry's name: its inferred title, or its stem when gone; "" when its check did not answer
	// gone is a path-only entry whose path is no regular file that reads:
	// missing, a directory, a device, or a path the stat or the read
	// refused. Wider than a plan's fileMissing, which is fs.ErrNotExist alone,
	// because a plan's row stands whatever its file does, and this row exists
	// only to open the file: none of these opens as a plan file.
	gone   bool
	opened time.Time
}

// loadRecents reads recent.json for the list, in the goroutine that reads the
// plans, and adds each path-only entry's file to the load's batch, so it is
// stat'ed and read for its title under the one budget the plans' files are
// (listFileCheckBudget). read is false only when the list was never pointed at
// a file, so a missing or undecodable one is still an answer: nothing to draw.
func loadRecents(path string, b *fileBatch) (saved []recent.Entry, read bool) {
	if path == "" {
		return nil, false
	}
	saved, err := recent.Load(path)
	if err != nil {
		// A file that will not read draws no section; the next open rewrites it.
		return nil, true
	}
	for _, e := range saved {
		if e.PlanID == "" && e.Path != "" {
			b.add(fileAsk{path: e.Path, title: true})
		}
	}
	return saved, true
}

// recentEntries is saved as the list holds it, each path-only entry's file
// told from the batch's answer for it. An ask that did not answer within the
// budget -- its stat or its read -- is "don't know": the entry keeps no title
// and is not gone, so the join skips it, and it holds the section as any entry
// the join cannot draw does.
func recentEntries(saved []recent.Entry, answered map[fileAsk]*fileCall) []recentEntry {
	entries := make([]recentEntry, 0, len(saved))
	for _, e := range saved {
		re := recentEntry{id: e.PlanID, path: e.Path, opened: e.OpenedAt}
		if call, ok := answered[fileAsk{path: e.Path, title: true}]; ok && e.PlanID == "" {
			re.title, re.gone = call.title, call.gone
		}
		entries = append(entries, re)
	}
	return entries
}

// fileTitle is a path-only entry's inferred title, from the stat that answered
// for it, or its stem and gone when that path is no regular file that reads.
// It runs in its ask's own goroutine (fileCheck.start), under the load's
// budget, so a read that hangs costs the load that budget and no more. Only a
// regular file is read: nothing else opens as a plan file, and a FIFO would
// never answer. One that becomes a FIFO after the stat blocks only that
// goroutine, which the budget abandons.
func fileTitle(read func(string) ([]byte, error), path string, info fs.FileInfo, err error) (title string, gone bool) {
	if err != nil || !info.Mode().IsRegular() {
		return ui.InferTitle(nil, path), true
	}
	content, err := read(path)
	if err != nil {
		return ui.InferTitle(nil, path), true
	}
	return ui.InferTitle(ui.ParseBlocks(content, nil), path), false
}

// recentsAnswer is one load's answer for Recently opened: recent.json as the
// list holds it. read is loadRecents' own.
type recentsAnswer struct {
	entries []recentEntry
	read    bool
}

// takeRecents installs a load's answer. One that read nothing -- the list was
// never pointed at a recent.json -- changes nothing.
func (m *ListModel) takeRecents(a recentsAnswer) {
	if a.read {
		m.recents = a.entries
	}
}

// drawsRecent is whether this body draws Recently opened at all, by mode and
// filter; whether the screen has room for it is allocateRecent's question. It
// is drawn in browse, and under the sort modal and every centred box, which
// compose over browse's own body and change nothing about it: hiding the
// section there would reflow the list under the box as it opened and again as
// it closed. Never while a filter narrows the catalog, in any mode, and
// never in expanded or under the rename panel, whose bodies are the catalog's
// alone (rename's is one flat row per plan, with no sections to draw it in).
func (m *ListModel) drawsRecent() bool {
	return m.recentPath != "" && !m.filterActive() &&
		(m.mode == listBrowse || m.mode == listSort || m.drawsCentredPanel())
}

// recentJoin is Recently opened's join, walked in draw order: each entry drawn
// as the plan in items it names -- by id, then by path -- or, for a file
// opened with no plan, as the file, until listRecentShown rows are drawn. A
// file that is gone is drawn too, as its ─ row: disk has answered for it, so
// it is as settled as a file that reads.
//
// AN ENTRY THE WALK CAN DRAW NO WAY IS SKIPPED, and the next one takes its
// slot: an entry recorded under an id that no longer names a plan, as any
// missing plan is, and a file whose check did not answer within the budget,
// which is "don't know" rather than gone.
//
// A PLAN THAT CARRIES THE PATH STILL WINS, gone or not: the plan is looked up
// first, so a gone file whose plan the load brings in becomes that plan's row,
// in the same slot.
func recentJoin(entries []recentEntry, items []planItem) []row {
	byID := make(map[domain.PlanID]planItem, len(items))
	byPath := make(map[string]planItem, len(items))
	for _, it := range items {
		byID[it.plan.ID] = it
		if hint := it.plan.SourceHint; hint != "" {
			if _, taken := byPath[hint]; !taken {
				byPath[hint] = it
			}
		}
	}
	var rows []row
	shown := make(map[domain.PlanID]bool)
	for _, e := range entries {
		if len(rows) == listRecentShown {
			break
		}
		it, ok := byID[e.id]
		if !ok && e.path != "" {
			it, ok = byPath[e.path]
		}
		switch {
		case ok && !shown[it.plan.ID]:
			shown[it.plan.ID] = true
			rows = append(rows, row{kind: rowPlan, item: it, section: sectionRecent, opened: e.opened})
		case ok:
			// A second entry naming a plan already drawn: the newer one stands.
		case e.id == "" && (e.title != "" || e.gone):
			rows = append(rows, row{kind: rowFile, text: e.title, path: e.path, gone: e.gone, section: sectionRecent, opened: e.opened})
		}
	}
	return rows
}

// recentBody is the rows Recently opened asks sectionRows to seat, or none
// where the section is not drawn. Pure over m.recents and m.items, so no
// service call is made to draw it.
func (m *ListModel) recentBody() []row {
	if !m.drawsRecent() {
		return nil
	}
	return recentJoin(m.recents, m.items)
}

// forgetRecentFile is d on a file row: nothing is destroyed, so there is no
// panel, and the row goes at once rather than when the write lands.
func (m *ListModel) forgetRecentFile(path string) (tea.Model, tea.Cmd) {
	if !m.dispatchOK() {
		return m, nil
	}
	m.recents = slices.DeleteFunc(slices.Clone(m.recents), func(e recentEntry) bool {
		return e.id == "" && e.path == path
	})
	m.rebuildRows()
	m.inFlight = true
	recentPath := m.recentPath
	return m, m.runListAction(func(context.Context) (string, error) {
		return "", recent.Forget(recentPath, recent.Entry{Path: path})
	})
}

// allocateRecent seats Recently opened ahead of "Your plans",
// taking all the rows it wants, less whatever would leave "Your plans" without
// its floor, and 0 -- no section, chrome included -- when that leaves it no row.
// It never draws a "+N more": it cannot be expanded, and what a trim cuts is the
// oldest opens.
func allocateRecent(budget, want int) int {
	if want <= 0 {
		return 0
	}
	room := budget - listRecentChrome - listSectionFloor
	if room < 1 {
		return 0
	}
	return min(want, room)
}

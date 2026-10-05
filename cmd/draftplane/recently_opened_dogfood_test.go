package main

// This file drives the plan list's "Recently opened" section end to end:
// two opens through draftplane review's own door (openAndRemember), oldest
// first -- a plan with a file, then a file with no plan yet -- land in
// Recently opened, newest first. Then two removals through the list's own d
// key, pressed as real tea.KeyPressMsg values against the same Root the
// binary itself builds (app.NewRoot): d on the file row removes it at once,
// with no panel; d on the plan's Recently opened copy brings up the
// ordinary delete confirm, and y removes both its rows -- Recently opened
// and "Your plans" -- in the same redraw, with recent.json losing the entry.
//
// GATED BY THE SAME VARIABLE AS TestReviewLoopDogfood (requireDogfood,
// DRAFTPLANE_DOGFOOD) rather than a gate of its own -- one variable
// for a human to remember, not two -- and it reuses that drive's machine
// scaffolding (newFreshMachine, setMachineEnv, guardRealState,
// machine_harness_test.go) rather than touching this developer's own
// draftplane state.
//
// Run it with:
//
//	DRAFTPLANE_DOGFOOD=1 go test ./cmd/draftplane -run TestRecentlyOpenedJourneyDogfood -v

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/app"
	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/client/recent"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/session"
)

// journeyKey builds the tea.KeyPressMsg for a printable rune, the same shape
// bubbletea's own runtime delivers for ordinary typing.
func journeyKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

// journeyDrain runs cmd off the loop the way bubbletea's own runtime would,
// feeding every message it produces back into m until the chain ends. A
// tea.BatchMsg fans out into its members.
func journeyDrain(m tea.Model, cmd tea.Cmd) tea.Model {
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			return m
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				m = journeyDrain(m, c)
			}
			return m
		}
		m, cmd = m.Update(msg)
	}
	return m
}

// journeyPress delivers one keystroke and drops the cmd it returns -- for a
// key that only moves the cursor or opens a panel, whose cmd (if any) is a
// cursor-blink timer no assertion here reads.
func journeyPress(m tea.Model, k tea.KeyPressMsg) tea.Model {
	m, _ = m.Update(k)
	return m
}

// journeyDispatch delivers one keystroke this drive expects to commit a
// write, and runs the resulting cmd chain to completion: a nil cmd means the
// gesture did not dispatch what was expected, which is fatal here rather
// than silently continuing against a world nothing changed.
func journeyDispatch(t *testing.T, m tea.Model, k tea.KeyPressMsg) tea.Model {
	t.Helper()
	cur, cmd := m.Update(k)
	if cmd == nil {
		t.Fatalf("keystroke %q dispatched no write; view was:\n%s", k.String(), ansi.Strip(cur.View().Content))
	}
	return journeyDrain(cur, cmd)
}

// recentRows is the titles found between "Recently opened" and "Your plans"
// on screen, in the order they are drawn -- newest open first.
func recentRows(t *testing.T, screen string, titles ...string) []string {
	t.Helper()
	lines := strings.Split(screen, "\n")
	start, end := -1, -1
	for i, l := range lines {
		switch {
		case start < 0 && strings.Contains(l, "Recently opened"):
			start = i
		case start >= 0 && strings.Contains(l, "Your plans"):
			end = i
		}
		if end >= 0 {
			break
		}
	}
	if start < 0 || end < 0 {
		t.Fatalf("no Recently opened section on screen:\n%s", screen)
	}
	var out []string
	for _, l := range lines[start:end] {
		for _, title := range titles {
			if strings.Contains(l, title) {
				out = append(out, title)
			}
		}
	}
	return out
}

// TestRecentlyOpenedJourneyDogfood drives the Recently opened journey end to
// end, against the same production wiring the binary itself uses (buildDeps,
// openAndRemember) and the same real keystrokes bubbletea's own runtime would
// deliver to app.NewRoot.
func TestRecentlyOpenedJourneyDogfood(t *testing.T) {
	requireDogfood(t)
	guardRealState(t)

	machine := newFreshMachine(t)
	setMachineEnv(t, machine)

	svc, km, _, pseudonym, err := buildDeps()
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	recentPath, err := recent.DefaultPath()
	if err != nil {
		t.Fatalf("recent.DefaultPath: %v", err)
	}

	dir := t.TempDir()
	write := func(name, doc string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
		return p
	}

	ctx := client.WithAttribution(context.Background(), domain.Attribution{})

	planFile := write("rollout.md", "# Rollout\n\n## Design\n\nStage by stage.\n")
	planSess, err := session.Open(ctx, svc, planFile)
	if err != nil {
		t.Fatalf("session.Open (plan file): %v", err)
	}
	if err := planSess.Create(ctx, "Rollout"); err != nil {
		t.Fatalf("creating the plan: %v", err)
	}

	fileOnly := write("scratch.md", "# Scratch notes\n\nNothing reviewed yet.\n")

	// Oldest first, as `draftplane review` itself would open them one at a
	// time -- openAndRemember's own now parameter stands in for the clock, so
	// the ordering below needs no sleep to be deterministic.
	epoch := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	if _, err := openAndRemember(ctx, svc, planFile, recentPath, epoch); err != nil {
		t.Fatalf("opening the plan: %v", err)
	}
	if _, err := openAndRemember(ctx, svc, fileOnly, recentPath, epoch.Add(time.Minute)); err != nil {
		t.Fatalf("opening the plan-less file: %v", err)
	}

	root := app.NewRoot(svc, km, nil, pseudonym, nil)
	root.SetRecents(recentPath)
	var m tea.Model = root
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = journeyDrain(m, root.Init())
	view := func() string { return ansi.Strip(m.View().Content) }

	if got, want := recentRows(t, view(), "Scratch notes", "Rollout"), []string{"Scratch notes", "Rollout"}; !slices.Equal(got, want) {
		t.Fatalf("Recently opened = %q, want %q (newest first)\n%s", got, want, view())
	}
	if n := strings.Count(view(), "Rollout"); n != 2 {
		t.Fatalf("the plan is drawn %d times, want twice: Recently opened and Your plans\n%s", n, view())
	}

	// === d on the file row: gone at once, no panel. The pinned cursor
	// is already on Recently opened's newest row, the file. ===
	m = journeyDispatch(t, m, journeyKey('d'))
	if strings.Contains(view(), "Scratch notes") {
		t.Fatalf("file row still drawn after d:\n%s", view())
	}
	if strings.Contains(view(), "y · delete") {
		t.Fatalf("d on a file row must not open the delete confirm panel:\n%s", view())
	}
	if got, err := recent.Load(recentPath); err != nil || len(got) != 1 || got[0].PlanID == "" {
		t.Fatalf("recent.json after d on the file = %+v (err %v), want only the plan left", got, err)
	}

	// === d on the plan's Recently opened copy: the ordinary confirm.
	// The row the file's removal left behind is the plan's own row, shifted
	// up into its slot. ===
	m = journeyPress(m, journeyKey('d'))
	if want := `Delete "Rollout" and its comment threads?`; !strings.Contains(view(), want) {
		t.Fatalf("d on the plan's Recently opened copy = %q, want the ordinary confirm naming it:\n%s", view(), view())
	}

	// === y: both copies go in the same redraw, and recent.json drops the
	// entry. ===
	m = journeyDispatch(t, m, journeyKey('y'))
	if strings.Contains(view(), "Rollout") {
		t.Fatalf("a copy of the deleted plan is still drawn:\n%s", view())
	}
	if got, err := recent.Load(recentPath); err != nil || len(got) != 0 {
		t.Fatalf("recent.json after the plan delete = %+v (err %v), want it emptied", got, err)
	}
}

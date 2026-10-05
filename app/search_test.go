package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/keymap"
	"github.com/draftplane/draftplane/session"
	"github.com/draftplane/draftplane/store/localcas"
	"github.com/draftplane/draftplane/ui"
)

// typeSearch feeds text into the model one printable rune at a time, exactly as
// a terminal delivers it: Code is the rune and Text is that rune's own string.
// app_test.go's press builds Code from a key NAME's first byte, so it cannot
// spell a multi-byte character at all.
func typeSearch(m *Model, text string) *Model {
	cur := tea.Model(m)
	for _, r := range text {
		cur, _ = cur.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return cur.(*Model)
}

// namedKey drives a REAL named keystroke -- the code alone, Text empty -- which
// press cannot produce: its fallback builds an unrecognized key name as
// tea.KeyPressMsg{Code: 'e', Text: "esc"}, a shape no terminal emits. An exit
// asserted only through press is an assertion about the helper.
func namedKey(m *Model, code rune) *Model {
	cur, _ := tea.Model(m).Update(tea.KeyPressMsg{Code: code})
	return cur.(*Model)
}

// TestTheSearchOpensFromBothItsKeys pins the two doors as one handler: f arrives
// as keymap.ActSearch and "/" as keymap.ActFilter, and updateRead answers both
// in a single case body.
func TestTheSearchOpensFromBothItsKeys(t *testing.T) {
	for _, key := range []string{"f", "/"} {
		t.Run(key, func(t *testing.T) {
			m := press(openModel(t, setup(t)), key)
			if m.mode != modeSearch {
				t.Fatalf("%q left the model in mode %d, want modeSearch", key, m.mode)
			}
		})
	}
}

// TestEveryPrintableKeyIsACharacterInTheSearch asserts updateSearch never
// consults the keymap, at the four keys that would hurt most: j is ActMoveDown,
// n is ActNextThread, q is ActQuit and "/" is the door itself. Each must land in
// the term instead, and the cmd is checked because that is what a dispatch would
// produce -- q's would end the program. Four keys are a sample, not the alphabet:
// they catch the realistic wrong turn, wiring m.km into updateSearch at all, not
// a handler that consults the keymap for only SOME actions.
func TestEveryPrintableKeyIsACharacterInTheSearch(t *testing.T) {
	m := press(openModel(t, setup(t)), "f")
	cursor, scroll := m.cursor, m.scroll

	const typed = "jq/n"
	cur := tea.Model(m)
	for _, r := range typed {
		var cmd tea.Cmd
		cur, cmd = cur.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		if cmd != nil {
			t.Fatalf("%q produced a cmd -- it was dispatched as an action rather than typed", r)
		}
	}
	m = cur.(*Model)

	if m.search != typed {
		t.Fatalf("m.search = %q, want %q", m.search, typed)
	}
	if m.mode != modeSearch {
		t.Fatalf("mode = %d after typing %q, want modeSearch", m.mode, typed)
	}
	if m.cursor != cursor || m.scroll != scroll {
		t.Fatalf("cursor/scroll moved to %d/%d from %d/%d -- a navigation key was dispatched", m.cursor, m.scroll, cursor, scroll)
	}
}

// TestTheSearchBackspacesARuneNotAByte pins dropLastRune's reuse: backspacing a
// byte off a multi-byte character leaves an invalid fragment, and a term is
// whatever the keyboard can produce.
func TestTheSearchBackspacesARuneNotAByte(t *testing.T) {
	m := typeSearch(press(openModel(t, setup(t)), "f"), "café")
	if m = namedKey(m, tea.KeyBackspace); m.search != "caf" {
		t.Fatalf("m.search = %q after backspace, want %q", m.search, "caf")
	}
}

// TestTheSearchExitsToReadModeAndOnlyEscDropsTheTerm pins the one difference
// between the two exits: enter commits the term and searchActive is true from
// there on, esc leaves no search on at all. Enter's landing on a match belongs to
// TestAcceptingASearchLandsOnTheFirstMatchAtOrAfterTheCursor. Both exits are
// driven as REAL keystrokes; see namedKey for why that matters at esc.
func TestTheSearchExitsToReadModeAndOnlyEscDropsTheTerm(t *testing.T) {
	for _, tc := range []struct {
		name string
		code rune
		want string
	}{
		{name: "enter accepts", code: tea.KeyEnter, want: "bucket"},
		{name: "esc clears", code: tea.KeyEscape, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := typeSearch(press(openModel(t, setup(t)), "f"), "bucket")
			m = namedKey(m, tc.code)
			if m.mode != modeRead {
				t.Fatalf("mode = %d, want modeRead", m.mode)
			}
			if m.search != tc.want {
				t.Fatalf("m.search = %q, want %q", m.search, tc.want)
			}
			if got, want := m.searchActive(), tc.want != ""; got != want {
				t.Fatalf("searchActive() = %v, want %v", got, want)
			}
		})
	}
}

// TestARefreshWhileSearchingIsDeferredToEitherExit drives BOTH exits because
// they are separate arms of one switch, and a test that exercised only one would
// not notice the other losing its maybeApplyPendingRefresh call. Nothing has to
// defer on the way IN: Update's msgStateChanged arm holds a state change for any
// mode that is not modeRead.
func TestARefreshWhileSearchingIsDeferredToEitherExit(t *testing.T) {
	for _, tc := range []struct {
		name string
		code rune
	}{
		{name: "enter accepts", code: tea.KeyEnter},
		{name: "esc clears", code: tea.KeyEscape},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			seedThread(t, f, "seed") // see TestStateChangeRefreshesInReadMode
			m := typeSearch(press(openModel(t, f), "f"), "bucket")

			addThread(t, secondFixtureStore(t, f), "written while searching")

			cur, cmd := m.Update(msgStateChanged{})
			m = cur.(*Model)
			_ = cmd // nil here: m.stateChanged is nil, same as any other disabled-watch Update

			if m.mode != modeSearch {
				t.Fatalf("mode = %d, a deferred state change must not leave the search", m.mode)
			}
			if !m.pendingRefresh {
				t.Fatal("a state change while the search owns the keyboard must set pendingRefresh")
			}
			if got := m.threadCount(); got != 1 {
				t.Fatalf("threadCount = %d, want 1 -- the refresh must be held, not applied while typing", got)
			}

			m = namedKey(m, tc.code)
			if m.pendingRefresh {
				t.Fatal("the exit must consume the pending refresh")
			}
			if got := m.threadCount(); got != 2 {
				t.Fatalf("threadCount = %d, want 2 -- the exit must apply the deferred refresh", got)
			}
		})
	}
}

// TestReopeningTheSearchKeepsTheTerm is enterSearch's edit rule: the second
// press opens ON the term already in hand. Starting empty would duplicate esc
// and lose text with no undo.
func TestReopeningTheSearchKeepsTheTerm(t *testing.T) {
	m := namedKey(typeSearch(press(openModel(t, setup(t)), "f"), "token"), tea.KeyEnter)
	if m = press(m, "/"); m.search != "token" {
		t.Fatalf("m.search = %q on re-entry, want the term still in hand", m.search)
	}
	// The raw field is what the row echoes, mid-word space included.
	if m = typeSearch(m, " bucket"); m.search != "token bucket" {
		t.Fatalf("m.search = %q, want %q", m.search, "token bucket")
	}
}

// TestTheSearchTermFoldsAndTrims splits Model.search from searchQuery: the row
// echoes the characters typed, the match is made on the folded and trimmed
// form, and a term of pure whitespace is no term at all -- so "on but matching
// nothing" is a state searchActive cannot report.
func TestTheSearchTermFoldsAndTrims(t *testing.T) {
	for _, tc := range []struct {
		typed  string
		query  string
		active bool
	}{
		{typed: "Deploy", query: "deploy", active: true},
		{typed: "  deploy ", query: "deploy", active: true},
		{typed: "   ", query: "", active: false},
	} {
		t.Run(fmt.Sprintf("%q", tc.typed), func(t *testing.T) {
			m := typeSearch(press(openModel(t, setup(t)), "f"), tc.typed)
			if m.search != tc.typed {
				t.Fatalf("m.search = %q, want the raw characters %q", m.search, tc.typed)
			}
			if got := m.searchQuery(); got != tc.query {
				t.Fatalf("searchQuery() = %q, want %q", got, tc.query)
			}
			if got := m.searchActive(); got != tc.active {
				t.Fatalf("searchActive() = %v, want %v", got, tc.active)
			}
		})
	}
}

// TestTypingASearchMovesNothing is the ONE line of ListModel.updateFilter that
// must not travel: setFilter rebuilds the list's body on every keystroke and
// this rebuilds nothing.
//
// The scroll is parked far from the cursor on purpose, which catches an
// ensureVisible snap back and a jump-to-first-match. It does not catch the third
// and likeliest shape: a bare m.rerender() is invisible to any comparison of
// VALUES -- the projection knows nothing about a search term, so a rebuild is
// byte-identical. What a rebuild cannot fake is the buffer's IDENTITY, so that
// is what is compared: rerender assigns a fresh slice.
func TestTypingASearchMovesNothing(t *testing.T) {
	m := press(openModel(t, longDocFixture(t)), "j", "j", "j")
	for range 20 {
		m = press(m, "J")
	}
	if m.cursor == 0 || m.scroll == 0 {
		t.Fatalf("setup did not move the view: cursor %d, scroll %d", m.cursor, m.scroll)
	}
	if m.scroll <= ui.FirstLineOfFocus(m.lines, m.docCursor()) {
		t.Fatalf("setup left the cursor on screen at scroll %d -- ensureVisible would not move", m.scroll)
	}

	m = press(m, "f")
	cursor, scroll := m.cursor, m.scroll
	lines := reflect.ValueOf(m.lines).Pointer()

	m = typeSearch(m, "section")
	if m.cursor != cursor {
		t.Fatalf("cursor moved to %d from %d while typing -- a match was navigated to", m.cursor, cursor)
	}
	if m.scroll != scroll {
		t.Fatalf("scroll moved to %d from %d while typing -- the viewport was re-anchored", m.scroll, scroll)
	}
	if reflect.ValueOf(m.lines).Pointer() != lines {
		t.Fatal("the line buffer was rebuilt while typing -- nothing is re-rendered until enter")
	}
}

// TestTheSearchAnswersItsExitsByNameNotByText pins the order updateSearch reads
// a keystroke in: msg.String() first and msg.Text second, exactly as
// ListModel.updateFilter does. The two orderings are indistinguishable on a real
// keyboard, where a named key carries no Text at all -- they come apart on
// app_test.go's press helper, whose fallback builds esc as
// tea.KeyPressMsg{Code: 'e', Text: "esc"}, so a Text-first handler types "esc"
// into the term rather than leaving.
func TestTheSearchAnswersItsExitsByNameNotByText(t *testing.T) {
	m := press(typeSearch(press(openModel(t, setup(t)), "f"), "bucket"), "esc")
	if m.mode != modeRead || m.search != "" {
		t.Fatalf("esc left mode %d and term %q, want modeRead and no term", m.mode, m.search)
	}
}

// searchLeakedStatus is a status message set only so its ABSENCE can be asserted.
const searchLeakedStatus = "review saved for someone"

// TestTheSearchInputRowReplacesTheStatusLine pins the row's rule: while
// modeSearch owns the keyboard the status row is the search's whole, at exactly
// the width every other row has. Both widths are drawn because the rail collapses
// below 80 and the bar's own width moves with it.
//
// All three suppressed segments are SET before the row is drawn, which is what
// makes their absence evidence rather than an accident of the fixture. m.status
// and the approved tint are conditional and nothing else in this package sets
// either: left at their zero values, two of the three segments are absent from a
// row that never had them to begin with.
//
// The empty term is the row at the door, and its own case because it is the one
// this row's gate can get wrong. viewPainted chooses on m.mode alone; gating it
// on m.searchActive() as well -- which reads as tidy -- draws the ordinary status
// bar for every keystroke up to the first printable one, so the reader presses f
// and nothing visible happens. Every row with a term in it survives that; this
// one reddens on all three suppressed segments at once.
func TestTheSearchInputRowReplacesTheStatusLine(t *testing.T) {
	for _, term := range []string{"deploy", ""} {
		for _, width := range []int{80, 40} {
			t.Run(fmt.Sprintf("term=%q/width=%d", term, width), func(t *testing.T) {
				m := openModel(t, setup(t))
				m.width = width
				m.status = searchLeakedStatus
				m.approved = true
				m.rerender()
				m = typeSearch(press(m, "f"), term)

				status := searchStatusRow(m)
				if n := ansi.StringWidth(status); n != width {
					t.Fatalf("status row is %d cells, want exactly %d: %q", n, width, status)
				}
				// The cursor cell is part of what is asserted: it is what
				// makes an empty query line a QUERY LINE rather than a blank.
				if want := "/" + term + "█"; !strings.Contains(status, want) {
					t.Fatalf("status row %q does not show %q", status, want)
				}
				for _, leak := range []string{"block 1/", searchLeakedStatus, "approved"} {
					if strings.Contains(status, leak) {
						t.Fatalf("status row %q still carries %q beside the search", status, leak)
					}
				}
				if strings.Contains(status, searchHint) {
					t.Fatalf("status row %q names the exits -- they belong to the help bar alone", status)
				}
			})
		}
	}
}

// TestTheSearchRowKeepsTheTailOfALongTerm is why searchInputLine clips with
// leftClip and never ansi.Truncate: the newest characters are the ones being
// typed, so a term wider than the row must show its END. A head-keeping
// truncation freezes the visible text at the first keystroke past the edge and
// leaves backspace corresponding to nothing on screen.
//
// A wide-character term at both width parities is the half an ASCII term cannot
// reach. A 2-cell character cannot be cut in half, so leftClip's budget lands
// mid-character at every ODD terminal width. The measured WIDTH of the row is
// asserted rather than its text because the text looked right while the row drew
// width+1 and wrapped a real terminal.
//
// The tail is matched past trailing padding, which is the same arithmetic from
// the other side: the clip comes back one cell SHORT at those widths, since a
// 2-cell character cannot fill an odd remainder, and searchInputLine pads what is
// left BEHIND the cursor -- which still sits immediately after the last character
// typed.
func TestTheSearchRowKeepsTheTailOfALongTerm(t *testing.T) {
	for _, tc := range []struct {
		name string
		term string
		tail string
	}{
		{name: "ascii", term: strings.Repeat("ab", 30) + "zz", tail: "zz█"},
		{name: "wide", term: strings.Repeat("測", 40) + "終", tail: "終█"},
	} {
		for _, width := range []int{40, 41} {
			t.Run(fmt.Sprintf("%s/width=%d", tc.name, width), func(t *testing.T) {
				m := openModel(t, setup(t))
				m.width = width
				m.rerender()
				m = typeSearch(press(m, "f"), tc.term)

				status := searchStatusRow(m)
				if n := ansi.StringWidth(status); n != width {
					t.Fatalf("status row is %d cells, want exactly %d: %q", n, width, status)
				}
				if !strings.HasSuffix(strings.TrimRight(status, " "), tc.tail) {
					t.Fatalf("status row %q does not end in the newest characters and the cursor", status)
				}
				if !strings.HasPrefix(status, "…") {
					t.Fatalf("status row %q does not mark a cut at its head", status)
				}
				if strings.HasPrefix(status, "/") {
					t.Fatalf("status row %q kept the HEAD of the term", status)
				}
			})
		}
	}
}

// TestTheSearchDrawsItsOwnHintExactlyOnce covers both halves of the help bar
// rule. The bar is this mode's own line, not read mode's -- every printable key
// here is a character, so advertising "q quit" would name a key the handler
// answers as the letter q. And searchHint is COUNTED rather than looked for:
// presence cannot tell one hint from two.
func TestTheSearchDrawsItsOwnHintExactlyOnce(t *testing.T) {
	m := typeSearch(press(openModel(t, setup(t)), "f"), "deploy")
	if got := m.helpBar(); got != searchHint {
		t.Fatalf("help bar = %q, want %q", got, searchHint)
	}

	screen := ansi.Strip(m.View().Content)
	if n := strings.Count(screen, searchHint); n != 1 {
		t.Fatalf("searchHint is drawn %d times on the rendered screen, want exactly 1", n)
	}
	if strings.Contains(screen, "q quit") {
		t.Fatal("read mode's help line is still on screen while the search owns the keyboard")
	}
}

// TestTheSearchCostsNoRow is what makes "reserves no panel and no row" evidence
// rather than a claim: viewHeight is identical in the two modes across every
// height a terminal can plausibly have, and the rendered screen is exactly
// m.height rows in both -- so the input row is one the layout already had.
func TestTheSearchCostsNoRow(t *testing.T) {
	m := openModel(t, longDocFixture(t))
	m.width = 80
	m.rerender()
	for height := 4; height <= 40; height++ {
		m.height = height

		m.mode = modeRead
		readHeight := m.viewHeight()
		readRows := len(strings.Split(m.View().Content, "\n"))

		m.mode = modeSearch
		if got := m.viewHeight(); got != readHeight {
			t.Fatalf("height %d: viewHeight is %d in modeSearch and %d in modeRead", height, got, readHeight)
		}
		if got := len(strings.Split(m.View().Content, "\n")); got != readRows || got != height {
			t.Fatalf("height %d: rendered %d rows in modeSearch, %d in modeRead, want %d", height, got, readRows, height)
		}
	}
}

// searchStatusRow returns the rendered status row, stripped of styling. It is
// the second-to-last row of the canvas -- viewPainted writes the status bar and
// then the help bar, and nothing follows them.
func searchStatusRow(m *Model) string {
	rows := strings.Split(m.View().Content, "\n")
	return ansi.Strip(rows[len(rows)-2])
}

// The three paragraphs searchNavFixture spells the term into, quoted here so
// every landing below is asserted BY THE CONTENT OF THE BLOCK IT LANDED ON. An
// assertion that the cursor merely MOVED passes for a search that lands one block
// early on every document in the world.
const (
	firstCanary  = "The first canary of the rollout."
	middleCanary = "The middle canary of the rollout."
	lastCanary   = "The last canary of the rollout."
)

// searchNavFixture builds a document whose three matches are far apart: 60
// headed sections of ordinary prose, three of which name the term once and say
// which of the three they are. The DISTANCE is the point -- a jump from one match
// to the next is well past a screen, so the landing has to scroll, which is what
// makes ensureVisible's presence at it visible to a test.
//
// The first match is below the first screen too: section 12's paragraph is buffer
// line 62, past every viewport this file drives, so ENTER's own landing scrolls
// as well. With the first match inside the opening screen only n and N ever had
// the distance, and "accepting a term moved the viewport" was vacuous however it
// was spelled. TestTheSearchGestureEndToEndThroughTheFrame re-derives that in the
// test rather than trusting this sentence.
func searchNavFixture(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	svc := newFixtureStore(t, filepath.Join(dir, "state.json"))
	var doc strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&doc, "## Section %d\n\n", i)
		switch i {
		case 12:
			fmt.Fprintf(&doc, "%s\n\n", firstCanary)
		case 30:
			fmt.Fprintf(&doc, "%s\n\n", middleCanary)
		case 57:
			fmt.Fprintf(&doc, "%s\n\n", lastCanary)
		default:
			fmt.Fprintf(&doc, "Ordinary content for section %d.\n\n", i)
		}
	}
	path := filepath.Join(dir, "canary.md")
	if err := os.WriteFile(path, []byte(doc.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return fixture{svc: svc, cas: localcas.New(filepath.Join(dir, "objects")), path: path, ctx: attrCtx("alice")}
}

// cursorBlockText names the block the review cursor is on. It fails rather than
// panicking on an out-of-range cursor, because that is a state the model reaches
// (see seekMatch) and a test that crashed on it would report the wrong thing.
func cursorBlockText(t *testing.T, m *Model) string {
	t.Helper()
	if m.cursor < 0 || m.cursor >= len(m.blocks) {
		t.Fatalf("cursor %d is outside the document's %d blocks", m.cursor, len(m.blocks))
	}
	return m.blocks[m.cursor].Text
}

// cursorOnScreen is ensureVisible's whole job at a landing: the row the rail is
// drawn on is inside the viewport.
func cursorOnScreen(m *Model) bool {
	line := ui.FirstLineOfFocus(m.lines, m.docCursor())
	return line >= m.scroll && line < m.scroll+m.viewHeight()
}

// acceptSearchTerm drives the whole gesture a reader makes: f, the term one
// character at a time, then a REAL enter.
func acceptSearchTerm(m *Model, term string) *Model {
	return namedKey(typeSearch(press(m, "f"), term), tea.KeyEnter)
}

// TestAcceptingASearchLandsOnTheFirstMatchAtOrAfterTheCursor is enter's half of
// the navigation, and the second case is the asymmetry against n: accepting a
// term while ALREADY SITTING ON a block that contains it holds still, because
// the reader who typed it there did not ask to leave, and n is what moves on.
func TestAcceptingASearchLandsOnTheFirstMatchAtOrAfterTheCursor(t *testing.T) {
	m := acceptSearchTerm(openModel(t, searchNavFixture(t)), "canary")
	if got := cursorBlockText(t, m); got != firstCanary {
		t.Fatalf("enter landed on %q, want %q", got, firstCanary)
	}

	m = namedKey(press(m, "f"), tea.KeyEnter)
	if got := cursorBlockText(t, m); got != firstCanary {
		t.Fatalf("re-accepting the same term moved to %q -- enter lands AT or after the cursor", got)
	}

	if m = press(m, "n"); cursorBlockText(t, m) != middleCanary {
		t.Fatalf("n stayed on %q -- n lands strictly AFTER the cursor", cursorBlockText(t, m))
	}
}

// TestNAndNWalkTheMatchesInDocumentOrderAndWrapAtBothEnds is the walk: n
// forward, N back, both silently wrapping when they run out. The landing is
// checked for being ON SCREEN at every step, which is the assertion the fixture
// is 60 sections long for -- the matches are further apart than a viewport, so
// a landing that did not scroll would leave the rail off it.
func TestNAndNWalkTheMatchesInDocumentOrderAndWrapAtBothEnds(t *testing.T) {
	m := acceptSearchTerm(openModel(t, searchNavFixture(t)), "canary")
	for _, step := range []struct {
		key  string
		want string
	}{
		{key: "n", want: middleCanary},
		{key: "n", want: lastCanary},
		{key: "n", want: firstCanary},
		{key: "N", want: lastCanary},
		{key: "N", want: middleCanary},
		{key: "N", want: firstCanary},
	} {
		m = press(m, step.key)
		if got := cursorBlockText(t, m); got != step.want {
			t.Fatalf("%q landed on %q, want %q", step.key, got, step.want)
		}
		if !cursorOnScreen(m) {
			t.Fatalf("%q landed on %q at scroll %d, outside the %d-row viewport", step.key, step.want, m.scroll, m.viewHeight())
		}
		if m.status != "" {
			t.Fatalf("%q set status %q -- the wrap is silent", step.key, m.status)
		}
	}
}

// TestASearchWithNoMatchesMovesNothingAndSaysSo is jumpThread's precedent ("no
// more threads in that direction") applied to a term that hits nothing: the
// cursor stays exactly where the reader left it and the status bar carries the
// term as TYPED.
//
// The term is spelled so that raw and folded differ, which is the whole of what
// "as TYPED" asserts: against a term identical to its own searchQuery, quoting
// either field passes. Capitals and surrounding spaces are what searchQuery
// removes, so this is the cheapest term that can tell the two fields apart.
func TestASearchWithNoMatchesMovesNothingAndSaysSo(t *testing.T) {
	m := press(openModel(t, searchNavFixture(t)), "j", "j", "j")
	cursor, scroll, where := m.cursor, m.scroll, cursorBlockText(t, m)
	const typed = "  Penguin  "
	const want = `no matches for "  Penguin  "`

	for _, key := range []string{"enter", "n", "N"} {
		m.status = ""
		if key == "enter" {
			m = acceptSearchTerm(m, typed)
		} else {
			m = press(m, key)
		}
		if got := cursorBlockText(t, m); got != where || m.cursor != cursor || m.scroll != scroll {
			t.Fatalf("%q moved to %q (block %d, scroll %d) with nothing to match", key, got, m.cursor, m.scroll)
		}
		if m.status != want {
			t.Fatalf("%q set status %q, want %q", key, m.status, want)
		}
	}
}

// TestLandingOnAMatchPutsTheRailOnTheBlockAndNotInsideItsCard is the
// load-bearing half of the landing. A block the reader stepped into earlier keeps
// a stored thread index, and focusedThread would put the rail on one of that
// card's rows rather than on the block the match is actually in -- so the landing
// clears the selection, exactly as ActToggleExpand's handler does.
func TestLandingOnAMatchPutsTheRailOnTheBlockAndNotInsideItsCard(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "a thread to step into")
	// n reaches the only commented block, enter opens its card leaving the
	// focus on the line, and the second n is what steps INTO the card.
	m := press(openModel(t, f), "n", "enter", "n")
	if m.focusedThread() == ui.NoThread {
		t.Fatal("setup did not step the focus into the card")
	}
	block := m.cursor
	if ui.FirstLineOfFocus(m.lines, m.docCursor()) == ui.FirstLineOf(m.lines, block) {
		t.Fatal("setup left the rail on the block's own line, so clearing the selection would show nothing")
	}

	m = acceptSearchTerm(m, "token bucket")
	if got := cursorBlockText(t, m); !strings.Contains(got, "token bucket") {
		t.Fatalf("the search landed on %q, want the block naming the term", got)
	}
	if m.cursor != block {
		t.Fatalf("the search left block %d for %d", block, m.cursor)
	}
	if got, want := ui.FirstLineOfFocus(m.lines, m.docCursor()), ui.FirstLineOf(m.lines, m.cursor); got != want {
		t.Fatalf("the rail is on line %d and the block's own line is %d -- the landing must leave no thread selected", got, want)
	}
	// AND THE BUFFER ACTUALLY SHOWS IT THERE, which is what pins the landing's
	// m.rerender(). Everything above is computed from m.docCursor() and is just as
	// true of a line buffer drawn before the cursor moved: line POSITIONS do not
	// depend on the cursor, so dropping the rerender leaves a byte-identical
	// buffer except for the one column the rail is painted in -- and that survived
	// the whole package. The glyph is COUNTED, since a rail left on the old block
	// is a second one, not a missing one.
	rails := 0
	for _, line := range m.lines {
		if strings.Contains(ansi.Strip(line.Text), searchRailGlyph) {
			rails++
		}
	}
	if rails != 1 {
		t.Fatalf("the rail glyph is drawn on %d lines, want exactly 1 -- the landing did not re-render", rails)
	}
	// AND THIS LINE CARRIES THE RAIL ONLY BECAUSE THE LANDING IS A PARAGRAPH,
	// which is a limit of the fixture rather than of the product: a table's first
	// row and any row under a comment card begin with the box's border, and
	// renderDocPainted draws no focus indicator on a border line. A fixture later
	// pointed at such a row would fail HERE and read as a search defect.
	if b := m.blocks[m.cursor]; b.Kind == ui.KindTableRow {
		t.Fatalf("this landing is now a table row, whose first line is a grid border and carries no rail by design -- point the fixture at a block with no box, or assert the rail on the row's own text")
	}
	if got := ansi.Strip(m.lines[ui.FirstLineOf(m.lines, m.cursor)].Text); !strings.Contains(got, searchRailGlyph) {
		t.Fatalf("the landed block's own line is %q, which carries no rail -- the buffer is the one drawn before the cursor moved", got)
	}
}

// searchRailGlyph is the review cursor's mark in a rendered line. It is a plain
// rune in the line's text, so a test can find it with the styles switched off.
const searchRailGlyph = "\u2503"

// TestEscClearsAnActiveSearchInReadModeAndIsOtherwiseInert covers both halves of
// read mode's esc. It clears an active search and MOVES NOTHING doing it -- a
// search narrowed no body, so there is nothing to widen back into, which is the
// difference from ListModel.clearFilter. The other two cases say the key was not
// stolen: esc is inert with no search on, and the gate is searchActive, so a term
// of pure whitespace is left in hand for the next f to edit.
func TestEscClearsAnActiveSearchInReadModeAndIsOtherwiseInert(t *testing.T) {
	for _, tc := range []struct {
		name  string
		typed string
		want  string
	}{
		{name: "an active search is cleared", typed: "canary", want: ""},
		{name: "no search at all leaves esc inert", typed: "", want: ""},
		{name: "a term of pure whitespace is no search to clear", typed: "   ", want: "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := openModel(t, searchNavFixture(t))
			if tc.typed != "" {
				m = acceptSearchTerm(m, tc.typed)
			}
			cursor, scroll, mode := m.cursor, m.scroll, m.mode
			m.status = ""

			m = namedKey(m, tea.KeyEscape)
			if m.search != tc.want {
				t.Fatalf("m.search = %q after esc, want %q", m.search, tc.want)
			}
			if m.cursor != cursor || m.scroll != scroll {
				t.Fatalf("esc moved the view to %d/%d from %d/%d -- clearing a search widens no body", m.cursor, m.scroll, cursor, scroll)
			}
			if m.mode != mode || m.status != "" {
				t.Fatalf("esc left mode %d and status %q", m.mode, m.status)
			}
		})
	}
}

// TestClearingTheSearchGivesNAndNBackToTheThreads is the other side of
// updateRead's one branch: with a search on n walks matches, and once esc has
// turned it off the same key is the document-wide THREAD jump again. The term
// matches a block that carries no thread and the thread sits on a block the
// term does not match, so the two answers name different blocks.
func TestClearingTheSearchGivesNAndNBackToTheThreads(t *testing.T) {
	f := setup(t)
	seedThread(t, f, "on the token bucket")
	m := acceptSearchTerm(openModel(t, f), "unbounded")
	if got := cursorBlockText(t, m); !strings.Contains(got, "currently unbounded") {
		t.Fatalf("the search landed on %q, want the unbounded paragraph", got)
	}

	m = press(namedKey(m, tea.KeyEscape), "n")
	if got := cursorBlockText(t, m); !strings.Contains(got, "token bucket") {
		t.Fatalf("n landed on %q, want the block carrying the only thread", got)
	}
	if got := m.selected[m.cursor]; got != 0 {
		t.Fatalf("selected thread = %d, want 0 -- n is a THREAD jump again, and those enter the card", got)
	}
}

// shrunkDoc replaces a 120-block document with four blocks, exactly one of
// which contains "section" -- and at an index (2) no wrap over the OLD
// document's matches could produce, so a cached match list lands somewhere
// this test can name.
const shrunkDoc = `# Fresh title

A paragraph that mentions no such word.

## The only section left

Trailing prose about nothing in particular.
`

// TestNAfterAReloadLandsInTheDocumentTheModelNowHas is why searchMatches is
// derived and never stored, and it drives ctrl+r rather than RefreshFromSession
// deliberately: the refresh re-projects placements over m.sess.Content, which
// session.Open read ONCE and no refresh re-reads, so rewriting the file and
// refreshing leaves m.blocks untouched. A test built on it would pass against a
// cached match list. ctrl+r reopens the session and really does replace the
// blocks.
//
// It is also the totality case for an out-of-range cursor, which reload does not
// clamp -- the document shrinks under it and the status bar goes on reading
// "block 120/4". If the assertion below ever fails because the cursor IS clamped
// now, this test has lost that subject and the state wants setting directly.
func TestNAfterAReloadLandsInTheDocumentTheModelNowHas(t *testing.T) {
	f := longDocFixture(t)
	m := acceptSearchTerm(press(openModel(t, f), "G"), "section")
	if len(m.blocks) != 120 || m.cursor != 119 {
		t.Fatalf("setup has %d blocks and cursor %d, want 120 and 119", len(m.blocks), m.cursor)
	}

	if err := os.WriteFile(f.path, []byte(shrunkDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	cur, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = drain(t, cur.(*Model), cmd)
	if len(m.blocks) != 4 || m.cursor < len(m.blocks) {
		t.Fatalf("the reload left %d blocks and cursor %d, want 4 blocks and a cursor still past them", len(m.blocks), m.cursor)
	}

	m = press(m, "n")
	if got, want := cursorBlockText(t, m), "The only section left"; got != want {
		t.Fatalf("n landed on %q, want %q -- the matches are the NEW document's", got, want)
	}
}

// TestTheSearchAnswersOnADocumentWithNoBlocks is the other totality case:
// statusIdentity reads "block 1/0" on an empty document, and nothing here may
// subscript m.blocks, so both exits answer instead of panicking.
func TestTheSearchAnswersOnADocumentWithNoBlocks(t *testing.T) {
	f := setup(t)
	if err := os.WriteFile(f.path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := openModel(t, f)
	if len(m.blocks) != 0 {
		t.Fatalf("fixture parsed %d blocks, want an empty document", len(m.blocks))
	}

	const want = `no matches for "anything"`
	m = acceptSearchTerm(m, "anything")
	if m.status != want || m.cursor != 0 {
		t.Fatalf("enter left status %q and cursor %d, want %q and 0", m.status, m.cursor, want)
	}
	m.status = ""
	if m = press(m, "n"); m.status != want || m.cursor != 0 {
		t.Fatalf("n left status %q and cursor %d, want %q and 0", m.status, m.cursor, want)
	}
}

// noThreadAtCursorStatus is the sentence all three read-mode writes already say
// when there is nothing to aim them at (app/actions.go, three sites).
const noThreadAtCursorStatus = "no thread at cursor"

// TestAWriteKeyWithTheRailOnABlocksOwnLineRefusesInsteadOfPanicking is
// selectedThread's guard: r, R and d CRASHED THE PROGRAM with index out of range
// [-1] whenever the rail sat on a commented block's own line -- a state
// m.selected records as ui.NoThread while selectedThread's clamp bounded only
// from above.
//
// Both producers of that state are driven. ActToggleExpand's handler clears the
// selection when a card is opened, which is the ordinary sequence "n, enter, r";
// seekMatch's landing carries the same clearing. One guard answers both, and a
// test that drove only the newer one would say nothing about the gesture readers
// actually make. All three keys, because a fix written into ActReply's arm alone
// would pass a test that only pressed r -- while d, the one that destroys what it
// lands on, went on crashing.
//
// The control at the bottom is not optional: `return ui.ThreadView{}, false`
// unconditionally satisfies every case above and breaks reply, resolve and delete
// entirely, so the same three keys are pressed with a thread genuinely selected
// and each is required to still do its own thing.
func TestAWriteKeyWithTheRailOnABlocksOwnLineRefusesInsteadOfPanicking(t *testing.T) {
	for _, producer := range []struct {
		name string
		open func(*testing.T, fixture) *Model
	}{
		{
			name: "enter opens the card",
			open: func(t *testing.T, f fixture) *Model { return press(openModel(t, f), "n", "enter") },
		},
		{
			name: "the search lands on the block",
			open: func(t *testing.T, f fixture) *Model {
				return acceptSearchTerm(openModel(t, f), "token bucket")
			},
		},
	} {
		for _, key := range []string{"r", "R", "d"} {
			t.Run(producer.name+"/"+key, func(t *testing.T) {
				f := setup(t)
				seedThread(t, f, "a thread nobody selected")
				m := producer.open(t, f)
				if len(m.views[m.cursor]) == 0 {
					t.Fatalf("setup: block %d carries no threads, so the write would refuse for the ordinary reason", m.cursor)
				}
				if got := m.selected[m.cursor]; got != ui.NoThread {
					t.Fatalf("setup: selection on block %d is %d, want ui.NoThread -- the rail is not on the block's own line", m.cursor, got)
				}
				if got := m.focusedThread(); got != ui.NoThread {
					t.Fatalf("setup: focusedThread = %d, want ui.NoThread -- the screen is not showing what this test is about", got)
				}

				m = press(m, key)
				if m.status != noThreadAtCursorStatus {
					t.Fatalf("%q left status %q, want %q", key, m.status, noThreadAtCursorStatus)
				}
				if m.mode != modeRead {
					t.Fatalf("%q left mode %d, want modeRead -- a write with nothing aimed at opens no panel", key, m.mode)
				}
				if m.inFlight {
					t.Fatalf("%q dispatched a write with no thread selected", key)
				}
			})
		}
	}

	for _, tc := range []struct {
		key        string
		wantMode   mode
		wantFlight bool
	}{
		{key: "r", wantMode: modeCompose},
		{key: "R", wantMode: modeRead, wantFlight: true},
		{key: "d", wantMode: modeConfirmDeleteThread},
	} {
		t.Run("a selected thread still answers/"+tc.key, func(t *testing.T) {
			f := setup(t)
			seedThread(t, f, "a thread nobody selected")
			m := press(openModel(t, f), "n")
			if got := m.selected[m.cursor]; got != 0 {
				t.Fatalf("setup: selection on block %d is %d, want thread 0", m.cursor, got)
			}

			m = press(m, tc.key)
			if m.status == noThreadAtCursorStatus {
				t.Fatalf("%q refused a thread that IS selected", tc.key)
			}
			if m.mode != tc.wantMode {
				t.Fatalf("%q left mode %d, want %d", tc.key, m.mode, tc.wantMode)
			}
			if m.inFlight != tc.wantFlight {
				t.Fatalf("%q left inFlight %v, want %v", tc.key, m.inFlight, tc.wantFlight)
			}
		})
	}
}

// TestAcceptingATermThatIsNoSearchReportsNothing is seekMatch's searchActive
// guard, which had no coverage at all: deleting it survived the whole package.
// Without it, enter over an empty query line answers `no matches for ""` -- a
// complaint about a search nobody made.
//
// The whitespace case could not be seen from the esc test, which drives the same
// term: that one sets m.status to "" AFTER accepting and before pressing esc, so
// the evidence was wiped between the two keystrokes. Here the accept is the last
// thing that happens.
func TestAcceptingATermThatIsNoSearchReportsNothing(t *testing.T) {
	for _, typed := range []string{"", "   "} {
		t.Run(fmt.Sprintf("%q", typed), func(t *testing.T) {
			m := openModel(t, searchNavFixture(t))
			cursor := m.cursor
			m.status = ""

			m = acceptSearchTerm(m, typed)
			if m.searchActive() {
				t.Fatalf("searchActive() is true for %q -- the fixture is not the state this test is about", typed)
			}
			if m.status != "" {
				t.Fatalf("enter on %q left status %q, want nothing said at all", typed, m.status)
			}
			if m.cursor != cursor {
				t.Fatalf("enter on %q moved the cursor to %d from %d", typed, m.cursor, cursor)
			}
		})
	}
}

// TestAResultThatSeizesTheKeyboardDropsAnUnacceptedTerm is the branch's
// signature defect: a write's result that SEIZES m.mode used to KEEP a typed
// search term -- as an ACTIVE SEARCH the reader never accepted. Four letters
// typed, an in-flight write lands, a panel takes the keyboard, and once it is
// dismissed n means "next match" while the help bar still reads "n/N threads".
// A navigation gesture that appears to move and lands on the wrong thing.
//
// A term is accepted by enter AND BY NOTHING ELSE, which is the rule, so the
// seizure drops it (dropSeizedSearch) exactly as modeSearch's own esc does.
//
// Both results that set a mode of their own are driven, even though one guard
// answers them, because a repair written into one arm would leave the other
// shipping the defect: ctrl+r's finding the file gone (Panel 1) and a write that
// conflicts (the conflict prompt). They are driven by handing the model the
// message the goroutine would have produced.
func TestAResultThatSeizesTheKeyboardDropsAnUnacceptedTerm(t *testing.T) {
	fileGone := func(m *Model) msgActionDone {
		fault := &session.SourceFileError{State: session.SourceFileGone, PlanID: m.sess.Plan.ID, Path: m.sess.Path}
		return msgActionDone{err: fault, sourceFault: fault, expandBlock: -1}
	}
	conflicted := func(*Model) msgActionDone {
		conflict := &client.ConflictError{Plan: domain.PlanID("plan"), Tip: domain.VersionRef{}}
		return msgActionDone{err: conflict, expandBlock: -1, retry: func(context.Context) (string, error) { return "", nil }}
	}
	for _, tc := range []struct {
		name string
		msg  func(*Model) msgActionDone
		want mode
	}{
		{"a reload that finds the file gone", fileGone, modeSourceFault},
		{"a conflict with a retry", conflicted, modeConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			seedThread(t, f, "on the token bucket")
			m := typeSearch(press(openModel(t, f), "f"), "unbo")

			cur, _ := m.Update(tc.msg(m))
			m = cur.(*Model)

			if m.mode != tc.want {
				t.Fatalf("mode = %d, want %d -- this result no longer seizes the keyboard and the case is testing nothing", m.mode, tc.want)
			}
			if m.search != "" {
				t.Fatalf("the panel left %q in hand as an active search, and enter was never pressed", m.search)
			}
			if m.searchActive() {
				t.Fatal("searchActive() is true on a term the reader never accepted")
			}
		})
	}

	// A RESULT THAT SEIZES NOTHING TAKES NOTHING: a write landing is not a reason
	// to empty the query line under a reader who is still typing into it. Dropping
	// on `before == modeSearch` alone passes every case above.
	t.Run("a result that changes no mode leaves the reader typing", func(t *testing.T) {
		f := setup(t)
		seedThread(t, f, "on the token bucket")
		m := typeSearch(press(openModel(t, f), "f"), "unbo")

		cur, _ := m.Update(msgActionDone{status: "thread updated", expandBlock: -1})
		m = cur.(*Model)
		if m.mode != modeSearch {
			t.Fatalf("mode = %d, want modeSearch -- a successful result took the keyboard", m.mode)
		}
		if m.search != "unbo" {
			t.Fatalf("m.search = %q, want the term still being typed", m.search)
		}
	})

	// The gesture end to end, through a panel dismissed by a key that writes
	// nothing: the term matches a block carrying no thread and the thread sits on
	// a block the term does not match, so n's two answers name different blocks.
	t.Run("n is the thread jump the help bar still promises", func(t *testing.T) {
		f := setup(t)
		seedThread(t, f, "on the token bucket")
		m := typeSearch(press(openModel(t, f), "f"), "unbo")

		cur, _ := m.Update(fileGone(m))
		m = namedKey(cur.(*Model), tea.KeyEscape)
		if m.mode != modeRead {
			t.Fatalf("esc left mode %d, want modeRead -- the panel was not dismissed", m.mode)
		}

		m = press(m, "n")
		if got := cursorBlockText(t, m); !strings.Contains(got, "token bucket") {
			t.Fatalf("n landed on %q, want the block carrying the only thread", got)
		}
	})
}

// TestAMissedTermsMessageDoesNotOutliveTheSearch is the status bar's half of the
// term-editing gesture. `no matches for "penguin"` survived every one of the
// things that falsify it -- a later accept that landed on a real match, the esc
// that turns the search off, and both of the ways out of the query line itself --
// so the bar went on naming a term the reader had already deleted, beside a
// cursor that had moved.
//
// One subtest per falsifier, deliberately, because each one is a separate
// clearSearchMiss call and a test that drove several through one assertion would
// let a call be deleted in silence behind the first failure. Deleting
// updateSearch's esc call reddens only "the query line's own esc takes it back";
// deleting its enter call reddens only the two enter cases below it.
//
// "A status the search did not write survives" is what keeps the repair honest.
// `m.status = ""` at any of these sites also survives the whole package, and would
// eat every other message the bar carries -- including maybeApplyPendingRefresh's
// "review updated", which lands on the very same keystroke one line earlier.
func TestAMissedTermsMessageDoesNotOutliveTheSearch(t *testing.T) {
	missed := func(t *testing.T) *Model {
		t.Helper()
		m := acceptSearchTerm(openModel(t, searchNavFixture(t)), "penguin")
		if m.status != `no matches for "penguin"` {
			t.Fatalf("setup: status = %q, want the miss this test is about", m.status)
		}
		return m
	}

	t.Run("a landing takes it back", func(t *testing.T) {
		m := press(missed(t), "f")
		for range len("penguin") {
			m = namedKey(m, tea.KeyBackspace)
		}
		m = namedKey(typeSearch(m, "canary"), tea.KeyEnter)

		if got := cursorBlockText(t, m); got != firstCanary {
			t.Fatalf("the edited term landed on %q, want %q", got, firstCanary)
		}
		if m.status != "" {
			t.Fatalf("status = %q beside a cursor sitting on a match", m.status)
		}
	})

	t.Run("esc takes it back", func(t *testing.T) {
		m := namedKey(missed(t), tea.KeyEscape)
		if m.searchActive() {
			t.Fatal("esc left the search on")
		}
		if m.status != "" {
			t.Fatalf("status = %q after esc turned the search off", m.status)
		}
	})

	// reopened drives the gesture the three cases below share: the miss is on the
	// bar, and the reader presses f to edit the term that earned it.
	reopened := func(t *testing.T) *Model {
		t.Helper()
		m := press(missed(t), "f")
		if m.mode != modeSearch {
			t.Fatalf("setup: f left mode %d, want modeSearch", m.mode)
		}
		return m
	}

	// backspaceAll empties the query line a rune at a time, as a reader does.
	backspaceAll := func(m *Model) *Model {
		for range len([]rune(m.search)) {
			m = namedKey(m, tea.KeyBackspace)
		}
		return m
	}

	t.Run("the query line's own esc takes it back", func(t *testing.T) {
		m := namedKey(reopened(t), tea.KeyEscape)
		if m.searchActive() {
			t.Fatal("esc out of the query line left a search on")
		}
		if m.status != "" {
			t.Fatalf("status = %q with no search on at all", m.status)
		}
	})

	// The two enter cases are the ones seekMatch cannot answer: it returns at
	// its searchActive guard without writing anything, so the sentence the
	// earlier accept left standing is nobody else's to take back.
	for _, tc := range []struct {
		name  string
		typed string
	}{
		{name: "backspacing the term away and accepting nothing", typed: ""},
		{name: "replacing it with whitespace, which is no search", typed: "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := namedKey(typeSearch(backspaceAll(reopened(t)), tc.typed), tea.KeyEnter)
			if m.searchActive() {
				t.Fatalf("searchActive() is true for %q -- this case is about a term that is no search", tc.typed)
			}
			if m.status != "" {
				t.Fatalf("status = %q with no search on at all", m.status)
			}
			// And read mode's esc is not the way out: it is gated on
			// searchActive, so it cannot reach a bar left in this state.
			if m = namedKey(m, tea.KeyEscape); m.status != "" {
				t.Fatalf("status = %q after a read-mode esc -- nothing takes it back", m.status)
			}
		})
	}

	t.Run("a status the search did not write survives", func(t *testing.T) {
		m := missed(t)
		m.status = searchLeakedStatus
		if m = namedKey(m, tea.KeyEscape); m.status != searchLeakedStatus {
			t.Fatalf("esc took back %q, which is not the search's to take", searchLeakedStatus)
		}
	})
}

// TestTheLandingAndADeferredRefreshEachKeepTheirOwnStatus drives the one
// keystroke that can produce two messages at once -- enter, which consumes a
// refresh deferred while the search held the keyboard and THEN lands -- and pins
// both halves of who owns the status bar.
//
// The miss case is what updateSearch's ENTER ORDERING is for. A refresh cannot
// change m.blocks (searchMatches), so jumping first survives the package and
// must; what the order really decides is which message the reader is left with,
// and the search's is the answer to the key that was pressed. Swapping the two
// lines reddens this case.
//
// The hit case is why the landing clears clearSearchMiss AND NOT m.status. Taking
// the bar back wholesale also survives the package, and it would eat "review
// updated" -- news with no other way to reach the reader -- with the same
// keystroke that revealed it.
func TestTheLandingAndADeferredRefreshEachKeepTheirOwnStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		term string
		want string
	}{
		{name: "a miss is this keystroke's own answer", term: "penguin", want: `no matches for "penguin"`},
		{name: "a hit takes back nothing that is not the search's", term: "token bucket", want: "review updated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			seedThread(t, f, "seed") // see TestStateChangeRefreshesInReadMode
			m := typeSearch(press(openModel(t, f), "f"), tc.term)

			addThread(t, secondFixtureStore(t, f), "written while searching")
			cur, _ := m.Update(msgStateChanged{})
			m = cur.(*Model)
			if !m.pendingRefresh {
				t.Fatal("setup: nothing was deferred, so there is no second message in play")
			}

			m = namedKey(m, tea.KeyEnter)
			if got := m.threadCount(); got != 2 {
				t.Fatalf("threadCount = %d, want 2 -- the deferred refresh never ran, so its status never competed", got)
			}
			if m.status != tc.want {
				t.Fatalf("status = %q, want %q", m.status, tc.want)
			}
		})
	}
}

// readHelpBarWithNoSearch is read mode's help bar, captured by running the code
// rather than retyped. It is pinned as a literal because the constraint on the
// swap below is that nothing changes for a reader who never presses f or "/" --
// a claim no assertion built from helpLine's own parts could make, since it would
// move with the function it is checking.
//
// It is the setup fixture's line, so there is no relocate segment: that one is
// gated on orphanCount and has its own test.
const readHelpBarWithNoSearch = "? keys · n/N threads · enter expand · c comment · r reply · R resolve · a approve · shift select · ctrl+r reload · l list · q quit"

// searchHelpRow returns the rendered help row, stripped of styling. It is the
// LAST row of the canvas, which is why searchStatusRow beside it takes the
// second-to-last.
func searchHelpRow(m *Model) string {
	rows := strings.Split(m.View().Content, "\n")
	return ansi.Strip(rows[len(rows)-1])
}

// TestTheHelpBarIsUnchangedForAReaderWhoNeverSearches is the constraint that
// makes the swap checkable: a reader who never opens the query line must see the
// bar this package shipped yesterday, byte for byte. The second case is the one a
// guard can fail -- building the clause unconditionally and letting it come out
// empty leaves a stray leading "/ no matches" -- so what esc leaves behind must be
// the identical line, not merely a line without the clause.
func TestTheHelpBarIsUnchangedForAReaderWhoNeverSearches(t *testing.T) {
	m := openModel(t, setup(t))
	if got := m.helpBar(); got != readHelpBarWithNoSearch {
		t.Fatalf("help bar = %q, want the line that shipped before the search:\n%q", got, readHelpBarWithNoSearch)
	}

	m = acceptSearchTerm(m, "bucket")
	if m.helpBar() == readHelpBarWithNoSearch {
		t.Fatal("accepting a term left the bar untouched -- the case below proves nothing")
	}
	m = namedKey(m, tea.KeyEscape)
	if got := m.helpBar(); got != readHelpBarWithNoSearch {
		t.Fatalf("help bar after esc = %q, want the line it started as:\n%q", got, readHelpBarWithNoSearch)
	}
}

// TestAnActiveSearchSwapsTheThreadClauseForItsOwn applies this package's hardest
// rule to the one bar that was breaking it: no hint may assert a key its handler
// rejects, and a WRONG hint is worse than a missing one. With a search on,
// updateRead's single n/N branch walks MATCHES -- so "n/N threads" was not an
// omission but misinformation, and it goes out as the search clause comes in.
//
// THE CLAUSE LEADS, asserted as a PREFIX rather than by containment, because the
// bar is already clipped at 80 columns before the search clause adds a cell
// (TestTheSearchClauseSurvivesTheClipThatEatsTheTailOfTheBar).
//
// Every term is spelled so raw and folded differ, and the folded form is asserted
// ABSENT: against a term that folds to itself, quoting searchQuery instead of
// m.search passes.
//
// The three counts are three different sentences, not one with a number in it. A
// cursor sitting ON a match wants its place in the walk; a cursor that walked off
// one with j wants the size of what n would rejoin; and a term that hits nothing
// has no walk to describe at all, so it names neither n nor N. "1/1" is its own
// row because it is the only state where n and N are genuinely silent -- they wrap
// to the block the cursor is already on -- and the clause is kept anyway, so a
// reordering of clauses' switch, which would render it "1 match", is visible.
//
// The last two rows are about what SURROUNDS the clause rather than the clause
// itself, and each asserts the WHOLE bar. One accepts a term of pure whitespace,
// which is TYPED and KEPT and no search at all, so the bar must be the unsearched
// line with "n/N threads" intact -- exactly what n and N still do there. The other
// seeds an ORPHAN, the only fixture in this file where the relocate segment exists
// at all and the widest line this mode can produce; without it, a gate that let
// the thread clause through on an orphan would never be read.
func TestAnActiveSearchSwapsTheThreadClauseForItsOwn(t *testing.T) {
	canaries := func(t *testing.T) *Model { return openModel(t, searchNavFixture(t)) }
	plain := func(t *testing.T) *Model { return openModel(t, setup(t)) }
	for _, tc := range []struct {
		name string
		open func(*testing.T) *Model
		term string
		// then walks the cursor away from where enter put it; "" asserts the landing.
		then string
		// clause is the search's own leading segments; "" marks the one row whose
		// term is NO SEARCH, where wantBar is the whole assertion.
		clause string
		// wantBar, when set, is asserted as the ENTIRE bar rather than the lead.
		wantBar string
	}{
		{
			name:   "on the first of three matches",
			open:   canaries,
			term:   "Canary",
			clause: `/"Canary" 1/3 · n/N match · esc clear`,
		},
		{
			name: "on the second, after n",
			open: canaries,
			term: "Canary",
			then: "n",
			// The only case whose position is not 1, which makes an off-by-one
			// visible AS a position rather than as a fall-through to the count.
			clause: `/"Canary" 2/3 · n/N match · esc clear`,
		},
		{
			name:   "walked off a match with j",
			open:   canaries,
			term:   "Canary",
			then:   "j",
			clause: `/"Canary" 3 matches · n/N match · esc clear`,
		},
		{
			name: "on the only match there is",
			open: plain,
			term: "Bucket",
			// THE SILENT STATE, and the clause is kept in it. seekMatch finds no
			// hit past the cursor, so n wraps to the block the cursor is already
			// on: nothing moves and nothing is said. It stays because "n/N match"
			// names a set that is not empty, and a clause that vanished here would
			// flicker off the moment the row below used n to arrive.
			clause: `/"Bucket" 1/1 · n/N match · esc clear`,
		},
		{
			name: "a lone match the cursor has left",
			open: plain,
			term: "Bucket",
			// k rather than j: the only match in this fixture is its LAST block,
			// so j would move nothing and the case would be the one above it.
			then:   "k",
			clause: `/"Bucket" 1 match · n/N match · esc clear`,
		},
		{
			name:   "an accepted term that hits nothing",
			open:   canaries,
			term:   "  Penguin  ",
			clause: `/"  Penguin  " no matches · esc clear`,
		},
		{
			name: "a term that would forge a segment boundary",
			open: canaries,
			// " · " is the separator helpLine joins segments with, and "/" is the
			// clause's own lead. Unquoted, this row renders "/a · b no matches"
			// and a reader counts one clause too many.
			term:   "a · b",
			clause: `/"a · b" no matches · esc clear`,
		},
		{
			name: "a term of pure whitespace is no search to describe",
			open: plain,
			term: "   ",
			// No clause at all: searchQuery trims, so this term is off and
			// updateRead goes on giving n and N to the THREADS. A clause here
			// would be the defect this test exists to hold shut.
			wantBar: readHelpBarWithNoSearch,
		},
		{
			name: "the widest line this mode draws: a search and an orphan",
			open: func(t *testing.T) *Model {
				fx := setup(t)
				seedOrphan(t, fx, "lost comment")
				return openModel(t, fx)
			},
			term:   "Bucket",
			clause: `/"Bucket" 1/1 · n/N match · esc clear`,
			// The relocate segment is the point: the one segment the swap has
			// never been read beside, so the whole bar is asserted.
			wantBar: `? keys · /"Bucket" 1/1 · n/N match · esc clear · enter expand · c comment · r reply · R resolve · m relocate · a approve · shift select · ctrl+r reload · l list · q quit`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := acceptSearchTerm(tc.open(t), tc.term)
			if tc.then != "" {
				m = press(m, tc.then)
			}
			got := m.helpBar()

			// A ROW WITH NO CLAUSE IS ABOUT A TERM THAT IS NOT A SEARCH.
			// searchQuery trims it away, so the bar has to be read mode's own
			// line. EVERY assertion below inverts for it -- including the
			// folded-query check, which for an empty query compares against a bare
			// "/" and would match "n/N threads" on any bar at all. That is why this
			// returns rather than sharing the tail.
			if tc.clause == "" {
				if m.searchActive() {
					t.Fatalf("searchActive() is true for %q -- this row is about a term that is no search at all", tc.term)
				}
				if got != tc.wantBar {
					t.Fatalf("help bar = %q,\nwant the unsearched line %q", got, tc.wantBar)
				}
				return
			}

			if !m.searchActive() {
				t.Fatalf("searchActive() is false for %q -- the fixture is not the state this case is about", tc.term)
			}
			// "? keys" leads every row of this bar unconditionally, ahead of the
			// search clause this table is actually about.
			if want := "? keys · " + tc.clause + " · enter expand · "; !strings.HasPrefix(got, want) {
				t.Fatalf("help bar = %q,\nwant it to lead with %q", got, want)
			}
			if strings.Contains(got, "n/N threads") {
				t.Fatalf("help bar = %q -- n and N walk MATCHES while a search is on", got)
			}
			if folded := "/" + m.searchQuery(); strings.Contains(got, folded) {
				t.Fatalf("help bar = %q carries the folded query %q rather than the term as typed", got, folded)
			}
			if tc.wantBar != "" && got != tc.wantBar {
				t.Fatalf("help bar = %q,\nwant the whole line %q", got, tc.wantBar)
			}
		})
	}

	// A REBIND MOVES THE CLAUSE, and this is the case that makes that a fact
	// rather than a sentence. Every row above runs on keymap.Default(), where
	// findKey(ActNextThread) answers "n" -- so the whole table passes against a
	// clause that hardcodes the literal "n/N match", which is exactly the shape
	// the clause exists not to be. Mutating the two findKey calls to that literal
	// survives every other test in this package; it fails here.
	//
	// It rebinds ONE of the pair on purpose: moving both would leave a clause that
	// could still be built from either action, and the asymmetric "z/N" is what
	// proves each half is looked up rather than spelled. z rather than N: N is
	// ActPrevThread's own default, so reusing it here would silently steal
	// prev-thread's only binding instead of adding a second one.
	t.Run("a rebind of n moves the clause with it", func(t *testing.T) {
		m := canaries(t)
		km := keymap.Default()
		for k, a := range km {
			if a == keymap.ActNextThread {
				delete(km, k)
			}
		}
		km["z"] = keymap.ActNextThread
		m.km = km

		m = acceptSearchTerm(m, "Canary")
		if got, want := m.helpBar(), `? keys · /"Canary" 1/3 · z/N match · esc clear · `; !strings.HasPrefix(got, want) {
			t.Fatalf("help bar = %q,\nwant it to lead with %q -- the clause is spelling n as a literal", got, want)
		}
		// And the key the clause now names is the key that walks, or the bar
		// would be telling the truth about a binding and lying about a handler.
		if m = press(m, "z"); m.cursor != m.searchMatches()[1] {
			t.Fatalf("z left the cursor at block %d, want the second match at %d", m.cursor, m.searchMatches()[1])
		}
		if got, want := m.helpBar(), `? keys · /"Canary" 2/3 · z/N match · esc clear · `; !strings.HasPrefix(got, want) {
			t.Fatalf("help bar = %q after z,\nwant it to lead with %q", got, want)
		}
	})
}

// TestTheSearchClauseSurvivesTheClipThatEatsTheTailOfTheBar is why the clause is
// PREPENDED, asserted on what the terminal actually shows rather than on the
// string helpBar returns -- appending is invisible to any test that reads the
// whole line, because the whole line is not what a reader gets.
//
// The pre-existing clip is part of the assertion, and it is recorded rather than
// repaired. Read mode's bar is 130 cells with no orphans, against a bar width of
// m.width - 2*ui.RailWidth(m.width) = 76 at 80 columns, so the tail of the bar is
// off-screen before a search clause exists. The first case pins that so the
// second's "the clause is visible and q quit is not" cannot be read as something
// this change broke. It is pinned at all, rather than derived from helpBar(), for
// the reason the guard below states: a test that computed the bar's width from
// the bar would agree with any bar, including one that fits and makes leading
// with the clause pointless.
//
// Both widths render exactly their row, 80 where the rail takes two cells a side
// and 40 where it collapses to none, and the canvas stays m.height rows -- so a
// bar that overflowed its row rather than being clipped into it would show up
// here as a taller screen.
func TestTheSearchClauseSurvivesTheClipThatEatsTheTailOfTheBar(t *testing.T) {
	m := openModel(t, searchNavFixture(t))
	m.width, m.height = 80, 30
	m.rerender()

	if n := ansi.StringWidth(m.helpBar()); n != 130 {
		t.Fatalf("read mode's bar is %d cells, not the 130 this test's arithmetic is written against", n)
	}
	if barWidth := m.width - 2*ui.RailWidth(m.width); ansi.StringWidth(" "+m.helpBar()) <= barWidth {
		t.Fatalf("the bar fits in %d cells -- nothing is clipped, so leading with the clause buys nothing", barWidth)
	}
	if row := searchHelpRow(m); strings.Contains(row, "q quit") {
		t.Fatalf("the unsearched row %q still shows its tail at 80 columns", row)
	}

	m = press(acceptSearchTerm(m, "Canary"), "n")
	for _, width := range []int{80, 40} {
		t.Run(fmt.Sprintf("width=%d", width), func(t *testing.T) {
			m.width = width
			m.rerender()

			rows := strings.Split(m.View().Content, "\n")
			if len(rows) != m.height {
				t.Fatalf("the canvas is %d rows at width %d, want %d -- the bar wrapped", len(rows), width, m.height)
			}
			row := searchHelpRow(m)
			if n := ansi.StringWidth(row); n != width {
				t.Fatalf("help row is %d cells, want exactly %d: %q", n, width, row)
			}
			if !strings.Contains(row, `/"Canary" 2/3`) {
				t.Fatalf("help row %q does not show the search clause -- it was appended into the part nobody sees", row)
			}
			if strings.Contains(row, "q quit") {
				t.Fatalf("help row %q shows its own tail at width %d -- there is no clip here for the clause to be leading", row, width)
			}
		})
	}
}

// searchRail returns the rendered rows the review cursor's rail is drawn on,
// stripped of styling and joined, and it is the frame's own answer to "where is
// the cursor". It takes the rail from m.View() rather than from m.lines for the
// property the buffer cannot have: the frame holds ONE viewport, so a landing
// that did not scroll has no rail to find at all.
//
// It is a run of rows and not one row, because the rail marks a whole FOCUS:
// ui/painted.go's cursorGlyph draws it on every row the focus occupies, one for a
// paragraph that fits the width and two for a thread card. What may not happen is
// a SECOND run -- a rail left behind where the cursor came from -- so the rows are
// required to be contiguous. That is not idle either: beat 11's thread jump moves
// five lines, and a rail left behind there IS on screen.
//
// At a SEARCH landing the frame's answer to a missing rerender is 0 rows and not
// 2: the matches are further apart than the viewport, so a rail left behind is off
// screen and simply absent. The count-2 case is the BUFFER's and belongs to
// TestLandingOnAMatchPutsTheRailOnTheBlockAndNotInsideItsCard, which counts over
// all of m.lines and sees both blocks at once.
func searchRail(t *testing.T, m *Model) string {
	t.Helper()
	rows := strings.Split(m.View().Content, "\n")
	var found []int
	for i, row := range rows {
		if strings.Contains(ansi.Strip(row), searchRailGlyph) {
			found = append(found, i)
		}
	}
	if len(found) == 0 {
		t.Fatal("no rendered row carries the rail -- the cursor is not on screen at all")
	}
	var b strings.Builder
	for k, i := range found {
		if k > 0 && i != found[k-1]+1 {
			t.Fatalf("the rail is drawn on two separate runs of rows %v -- one of them was left behind", found)
		}
		b.WriteString(ansi.Strip(rows[i]))
		b.WriteString("\n")
	}
	return b.String()
}

// The two blocks the drive below hangs a thread on, chosen for opposite reasons.
//
// matchHost IS A MATCH, which is what puts the rail INSIDE A CARD while a search
// is on -- the one state where read-mode esc's "it moves nothing" and the
// landing's "the rail seats on the block, never in a card" can disagree with each
// other. It is the last of the three canaries, so the walk both leaves it and
// returns to it.
//
// threadHost MATCHES NOTHING and sits after every match, so the n that lands on
// it can only be the THREAD jump the restored help bar promises. It is the second
// thread rather than the only one: with the single thread moved onto a match, the
// jump would otherwise have nowhere to go.
//
// matchHostThread is that first thread's BODY, and it is what tells a rail on a
// card apart from a rail on a block: these words are in the frame only when the
// card is open and the focus is inside it.
const (
	matchHost       = lastCanary
	matchHostThread = "the thread the reader stepped into"
	threadHost      = "Ordinary content for section 58."
)

// TestTheSearchGestureEndToEndThroughTheFrame is the ONE end-to-end drive of this
// feature: one Model, one sequence, and every beat asserted on the RENDERED FRAME
// rather than on the fields behind it. Each beat below has its own unit test
// already; none of them has the composition, and the composition is the thing a
// reader actually performs.
//
// IT ASSERTS ON THE BARS BECAUSE THE BARS ARE WHAT A READER GETS. Two field
// assertions in this feature have disagreed with the paint: searchInputLine drew
// width+1 while its text looked right, and seekMatch's landing was correct in
// m.cursor while the buffer still carried the rail on the block it left.
//
// The rail is the landing's whole assertion and it is read by CONTENT
// (searchRail): a drive that checked m.cursor != before would pass for a search
// that lands one block early on every document in the world, and a rail read from
// the frame is also a rail that is ON SCREEN, since the frame holds one viewport.
//
// The rail is also stepped into a card, at beats 6 and 9, and that is not
// decoration. Two behaviours that are each correct alone -- read-mode esc MOVES
// NOTHING, and a landing seats the rail on the BLOCK and never inside one of its
// cards -- compose into a defect the moment the reader is inside a card with a
// search on: an esc that "tidied up" by clearing the selection would yank the rail
// out of the card, and a landing that forgot to clear one would drop it into a
// card the reader left there three keystrokes ago. No other test in this package
// reaches that state, since it lives in m.selected and
// TestEscClearsAnActiveSearchInReadModeAndIsOtherwiseInert compares only m.cursor
// and m.scroll. The esc half reddens beat 10 and the landing half beat 8.
//
// THE SCROLL IS ASSERTED FROM 0 TO NOT-0, and the guard above beat 3 is what makes
// that worth writing: ensureVisible is a NO-OP for a match that is already
// visible, so the same assertion over a match in the first screen is true of a
// landing that never scrolled. The guard re-derives the first match's line and the
// viewport's height and refuses to run if the match is already on screen --
// searchNavFixture's own comment says section 12 for exactly this, and a comment
// is not a check.
//
// One door, measured rather than assumed: enterSearch is a single assignment and
// both keys reach it through ONE case body (updateRead's ActSearch, ActFilter
// arm), so past the first keystroke the two runs are the same bytes and nothing
// downstream of that assignment can separate them. The doors are
// TestTheSearchOpensFromBothItsKeys's subject and are left there.
//
// ESC IS THE REAL ONE, tea.KeyPressMsg{Code: tea.KeyEscape} with no Text at all,
// and never press's synthetic {Code: 'e', Text: "esc"} -- see namedKey. It is not
// the stricter key here: updateRead answers esc on msg.String(), which both
// spellings produce. What this drive owes the production path is simply to send
// what a terminal sends.
func TestTheSearchGestureEndToEndThroughTheFrame(t *testing.T) {
	f := searchNavFixture(t)
	seedThreadOn(t, f, inferPlanTitle, matchHost, matchHostThread)
	seedThreadOn(t, f, inferPlanTitle, threadHost, "the thread past the last match")
	m := openModel(t, f)
	m.width, m.height = 80, 24
	m.rerender()

	first := -1
	for i, b := range m.blocks {
		if b.Text == firstCanary {
			first = i
		}
	}
	if first < 0 {
		t.Fatalf("no block of the fixture is %q", firstCanary)
	}
	line := ui.FirstLineOf(m.lines, first)
	if m.scroll != 0 {
		t.Fatalf("the drive starts at scroll %d, want the top of the document", m.scroll)
	}
	if line < m.viewHeight() {
		t.Fatalf("the first match is line %d of a %d-row viewport, so it is already on screen and beat 3's scroll assertion proves nothing", line, m.viewHeight())
	}

	// 1. The door: the query line REPLACES the status row, and the help bar
	// names this mode's two exits and no read-mode key.
	m = press(m, "f")
	if got := searchStatusRow(m); !strings.Contains(got, "/█") {
		t.Fatalf("f left the status row %q, want the empty query line", got)
	}
	if got := searchHelpRow(m); !strings.Contains(got, searchHint) {
		t.Fatalf("the help row is %q, want it naming the search's exits %q", got, searchHint)
	}

	// 2. Typing: the row echoes the term AS TYPED, capital and all.
	m = typeSearch(m, "Canary")
	if got := searchStatusRow(m); !strings.Contains(got, "/Canary█") {
		t.Fatalf("the status row is %q, want it echoing the term the reader typed", got)
	}
	if m.scroll != 0 {
		t.Fatalf("typing scrolled to %d -- and beat 3 now has nothing to prove", m.scroll)
	}

	// 3. enter: the rail is on the first match BY CONTENT, the viewport travelled
	// to it, the status row has stopped being the query line, and the help bar
	// carries the search's clause IMMEDIATELY BEHIND "? keys" (which leads
	// every row of this bar unconditionally) -- which is the assertion, not
	// merely that "n/N threads" is gone: the clause replaces it
	// outright, so "enter expand" is the very next thing after it rather than
	// the clause sliding in behind some other segment.
	m = namedKey(m, tea.KeyEnter)
	if m.scroll == 0 {
		t.Fatalf("enter landed without scrolling, and %q is line %d of a %d-row viewport", firstCanary, line, m.viewHeight())
	}
	if rail := searchRail(t, m); !strings.Contains(rail, firstCanary) {
		t.Fatalf("enter put the rail on %q, want the row carrying %q", rail, firstCanary)
	}
	// The identity is NOT compared against m.statusIdentity(): both sides would
	// come from one function, so the only thing such a check can catch is the
	// query row outliving the mode -- which the two checks below catch harder.
	status := searchStatusRow(m)
	if !strings.Contains(status, "block ") {
		t.Fatalf("the status row is %q after enter, want the document's identity back", status)
	}
	if strings.Contains(status, "█") {
		t.Fatalf("the status row %q still carries the query line's cursor -- the row belongs to the mode, not to the term", status)
	}
	if strings.Contains(status, "/Canary") {
		t.Fatalf("the status row %q still carries the term -- the query line outlived modeSearch", status)
	}
	if got, want := strings.TrimLeft(searchHelpRow(m), " "), `? keys · /"Canary" 1/3 · n/N match · esc clear`; !strings.HasPrefix(got, want) {
		t.Fatalf("the help row is %q, want it leading with %q -- n and N walk MATCHES now, and beat 10 has nothing to restore", got, want)
	}

	// 4, 5. The walk forward, each landing named by the block it is on.
	walk := func(key, want string) {
		t.Helper()
		m = press(m, key)
		if rail := searchRail(t, m); !strings.Contains(rail, want) {
			t.Fatalf("%q left the rail on %q, want the row carrying %q", key, rail, want)
		}
	}
	walk("n", middleCanary)
	walk("n", matchHost)

	// 6. The reader steps INTO the card on the match they landed on: enter opens
	// it leaving the rail on the block's line, and j is the step in. From here to
	// beat 10 the rail is on a THREAD, the state beats 8 and 10 are about and the
	// one no other test in this package reaches with a search on.
	stepIntoCard := func(keys ...string) {
		t.Helper()
		m = press(m, keys...)
		if rail := searchRail(t, m); !strings.Contains(rail, matchHostThread) {
			t.Fatalf("the rail is on %q, want it inside the card on %q", rail, matchHost)
		}
	}
	stepIntoCard("enter", "j")

	// 7, 8. The wraps: n off the end to the first match, N off the front back
	// to the last. The second is the one that matters -- the reader left a
	// thread selected on that block at beat 6, and the landing must put the
	// rail back on the block's OWN line rather than into the card they left.
	walk("n", firstCanary)
	walk("N", matchHost)
	if rail := searchRail(t, m); strings.Contains(rail, matchHostThread) {
		t.Fatalf("the landing put the rail inside the card on %q -- a match is a BLOCK, and the selection the reader left there is not where the search landed", matchHost)
	}

	// 9. Step back into the card, so esc is pressed from inside one.
	stepIntoCard("j")

	// 10. esc: the search is off, and the two bars that said it was on say so.
	// The rail HAS NOT MOVED -- clearing a search widens no body, so it is still
	// on the thread the reader stepped onto rather than back on the block's line.
	m = namedKey(m, tea.KeyEscape)
	if got := searchStatusRow(m); strings.Contains(got, "/Canary") || strings.Contains(got, "█") {
		t.Fatalf("the status row is %q after esc, want no trace of the search on it", got)
	}
	if got := searchHelpRow(m); !strings.Contains(got, "n/N threads") {
		t.Fatalf("the help row is %q after esc, want n and N given back to the threads", got)
	}
	if rail := searchRail(t, m); !strings.Contains(rail, matchHostThread) {
		t.Fatalf("esc moved the rail to %q, want it still inside the card on %q", rail, matchHost)
	}

	// 11. And n is the THREAD jump the restored bar promises, landing on a
	// block no term here matches.
	m = press(m, "n")
	if rail := searchRail(t, m); !strings.Contains(rail, threadHost) {
		t.Fatalf("n landed the rail on %q, want the row carrying %q", rail, threadHost)
	}
}

// benchmarkFixture is a plan of the given block count: a heading every tenth
// block, and paragraphs carrying one emphasis span and one soft line break so the
// parse and the projection both do real work. Deterministic, and every paragraph
// contains "deploy" so the search below is a term that hits.
func benchmarkFixture(tb testing.TB, blocks int) fixture {
	tb.Helper()
	dir := tb.TempDir()
	var doc strings.Builder
	for i := range blocks {
		if i%10 == 0 {
			fmt.Fprintf(&doc, "## Section %d\n\n", i/10)
			continue
		}
		fmt.Fprintf(&doc, "The **deploy** step %d needs a rollback plan\nbefore the gate is run again.\n\n", i)
	}
	path := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(path, []byte(doc.String()), 0o644); err != nil {
		tb.Fatal(err)
	}
	return fixture{
		svc:  newFixtureStore(tb, filepath.Join(dir, "state.json")),
		cas:  localcas.New(filepath.Join(dir, "objects")),
		path: path,
		ctx:  attrCtx("alice"),
	}
}

// BenchmarkReadHelpBar measures read mode's bar, which is derived at every
// repaint and stores nothing, so the search's arm rescans the whole document each
// time. The no-search arm beside it is the control that says how much of the cost
// is the scan and how much is the bar.
func BenchmarkReadHelpBar(b *testing.B) {
	for _, blocks := range []int{250, 2500} {
		for _, arm := range []struct {
			name string
			term string
		}{
			{"no search", ""},
			{"a search that hits every paragraph", "deploy"},
		} {
			b.Run(fmt.Sprintf("%d blocks/%s", blocks, arm.name), func(b *testing.B) {
				m := openModel(b, benchmarkFixture(b, blocks))
				if got := len(m.blocks); got != blocks {
					b.Fatalf("the fixture parsed to %d blocks, want %d", got, blocks)
				}
				m.search = arm.term
				if m.searchActive() != (arm.term != "") {
					b.Fatalf("searchActive() = %v for %q -- the fixture is not the state this arm is about", m.searchActive(), arm.term)
				}
				b.ReportAllocs()
				for b.Loop() {
					m.helpBar()
				}
			})
		}
	}
}

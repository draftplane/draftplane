package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/client/recent"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/keymap"
	"github.com/draftplane/draftplane/theme"
	"github.com/draftplane/draftplane/ui"
)

// listMode mirrors the review model's mode enum: browse is the default, read-only
// navigation state; rename and confirm-delete are transient modes that own key
// handling until they resolve.
//
// EXPANDED IS A FOURTH KIND, and it is neither of the other two. It draws no panel
// and reserves no rows for one, and the body it draws is Your plans' whole list
// rather than the rows browse has room for. So the two questions this file used
// to answer with one comparison are now two: isPanelMode (does a panel come out of
// the body, and does the masthead carry a column-header row) and mode == listBrowse
// (is the body browse's allocation).
//
// FILTER IS A FIFTH, and it splits them the other way: its body is EXACTLY browse's
// -- same allocation, same chrome, same geometry -- and what makes it a mode is the
// KEYBOARD, which consumes literal characters into a query. It is the second mode
// holding TYPED TEXT a quit would discard (typesLiterals).
//
// SORT IS A SIXTH, and it answers BOTH questions exactly the way browse does, which
// is why it costs no geometry: its body IS browse's, and the modal is COMPOSITED
// over the finished frame rather than laid out beside it. What makes it a mode is
// that the keyboard is claimed by a DECISION, and nothing in the body moves until
// enter (updateSort).
type listMode int

const (
	listBrowse listMode = iota
	listExpanded
	listFilter
	listSort
	listRename
	listConfirmDelete

	// listKeys is the "?" panel, this model's own half of the review model's
	// modeKeys -- see that mode's doc comment (app/model.go) for the shared shape
	// (an ordinary centred confirm body, dismissible by esc or ActKeys again).
	// Entered from listBrowse alone (updateBrowse's own ActKeys case,
	// enterKeysPanel), the one door every other confirm panel on this model also
	// opens from.
	//
	// ITS CONTENT IS NOT A HAND-TYPED SNAPSHOT EITHER: listKeyGroups is this
	// model's own declared list, pinned by
	// TestListKeysPanelMatchesActualDispatch (app/keys_test.go) against
	// updateBrowse's and updateExpanded's real switches -- see
	// reviewKeyGroups' own doc comment for what that pinning can and cannot
	// prove, which applies here unchanged.
	listKeys

	// listInfo is `i`'s panel: what the plan under the cursor IS -- its title,
	// id, when it last changed, its file, and the command that opens it. Entered
	// from listBrowse alone (enterInfoPanel), the door every other confirm panel
	// on this model also opens from.
	//
	// A REFERENCE PANEL, listKeys' OWN PRECEDENT: it reads the row under the
	// cursor and writes nothing, so it takes no dispatchOK gate (updateBrowse's
	// ActInfo case) and both esc and ActInfo again close it (updateInfo),
	// listKeys' identical toggle.
	//
	// ITS BODY IS NOT m.confirm BUILT FROM A FRESH READ: every field it shows is
	// already on the row's own planItem/domain.Plan, so entering it costs nothing
	// a refresh has not already paid for.
	listInfo

	// listModeCount is one past the last list mode, the counterpart of the review
	// model's own modeCount and there for the same one consumer: the help bar's
	// exhaustiveness test walks listMode(0)..listModeCount-1, so the set it checks
	// is derived from this declaration rather than retyped beside it.
	listModeCount
)

// isPanelMode reports whether a bottom panel is open, which is the question
// viewHeight's reservation, mastheadHeight's top block and viewPainted's
// column-header row are actually asking. It is deliberately NOT "mode is not
// browse": expanded opens no panel, draws its own band and keeps browse's
// geometry exactly, so folding it in with rename would hand it rename's
// masthead block and a header row labelling a body that has one of its own.
// FILTER is the same answer for a stronger reason -- its body IS browse's,
// allocation and all -- and SORT and every CENTRED CONFIRM MODE are stronger
// still: their boxes are composited over the finished frame, so there is nothing
// for any reservation to hold room for (see drawsCentredPanel, whose members
// this method excludes simply by never naming them).
//
// A PANEL MODE IS RENAME, AND RENAME ALONE: the one mode left in this file whose
// body is a textarea drawn as its own strip rather than a body spliced into the
// frame.
func (m *ListModel) isPanelMode() bool {
	return m.mode == listRename
}

// drawsCentredPanel reports whether the mode in play draws its body as the
// BORDERED BOX SPLICED INTO THE MIDDLE OF THE FINISHED FRAME
// (composeCentredPanel below) rather than as a bottom strip -- this model's
// counterpart of the review model's package-level drawsCentredPanel, a method
// here instead of a function because it reads m.mode without a caller having to
// pass it.
//
// FIVE READERS: viewHeight (must NOT reserve -- the splice adds no row),
// panelViewPainted (must NOT draw a strip -- absence from its switch is this
// model's version of that), confirmGroups/confirmBudget (size the text and the
// budget against the BOX's own geometry via centredInterior, not a strip's),
// composeCentredPanel below (doing the splice itself), and drawsRecent (a box
// changes nothing about the body behind it, Recently opened included). A
// mode moves between the two placements by being named here and nowhere
// else; get it half-named and the panel either draws twice or reserves rows
// nothing paints.
//
// A MODE MUST LEAVE isPanelMode AND JOIN THIS SET IN THE SAME COMMIT: dropped
// from isPanelMode while it still painted a strip, it would reserve no row and
// paint one anyway -- the exact overflow this predicate exists to close.
//
// listKeys and listInfo are here for the same reason: each is an ordinary
// read-only body with nothing about it that argues for rename's strip.
func (m *ListModel) drawsCentredPanel() bool {
	return m.mode == listConfirmDelete || m.mode == listKeys || m.mode == listInfo
}

// centredGroups is the body the centred box draws for this model, in the shape
// centredBox (app/painted.go) takes: one group of screen rows per logical line,
// headed by whichever line the panel leads with (headThenBody, the same
// package-level rule the review model's own centredGroups applies).
func (m *ListModel) centredGroups() []panelGroup {
	return headThenBody(m.confirmGroups())
}

// typesLiterals reports whether the mode in play is consuming literal characters
// into text a human is composing: rename's textarea draft and the filter's
// query. It is the question the minimum-size gate's quit exception and
// gateQuitKey are both asking.
//
// THE CONSEQUENCE IS ABOUT ONE KEY, and it is why this is worth a predicate.
// Above the gate q is a CHARACTER in these modes; below it, the gate would read
// the same q as ActQuit and end the process -- one key with two opposite meanings
// on either side of a resize, and the losing one silently discards text the user
// typed and cannot currently see.
func (m *ListModel) typesLiterals() bool {
	return m.mode == listRename || m.mode == listFilter
}

var (
// THIS BLOCK IS EMPTY. A row's colours now come from the ROW's own theme zone
// (zone/zoneStyles below), which is where a palette belongs once there is
// exactly one canvas to paint.
)

// planItem is one PLAN row's derived facts, re-read fresh off the service's own
// ListPlans on every refresh -- see deriveListItems for where each field comes
// from.
type planItem struct {
	plan  domain.Plan
	open  int // unresolved threads
	total int // all threads for the plan

	// approvedTip reports whether the plan's latest REGISTERED version (the last
	// element of Versions, service call order) carries an approval. Approximation:
	// a local working copy can have unregistered edits ahead of that version --
	// session.ApprovedCurrent is the precise answer for one open session, while the
	// list synthesizes this across every plan without opening each one's file, so
	// it can only speak to what was last registered and approved.
	approvedTip bool

	// lastActivity is the max of every version's RegisteredAt, every
	// thread's comments' CreatedAt, and every approval's CreatedAt for the
	// plan — the zero value when none of those facts exist yet (a fresh,
	// untouched plan). SORT KEY ONLY: never displayed, there's no room for
	// it in a single-line row at width 80.
	lastActivity time.Time

	// created is when the plan came into existence, off domain.Plan.CreatedAt, and
	// it is the key behind the sort modal's `created` column. SORT KEY ONLY, like
	// lastActivity above and for the same reason -- no column renders it.
	//
	// THE ZERO VALUE IS A REAL ANSWER: a plan with no registered version has no
	// creation instant to read. A zero sorts last descending and first ascending,
	// the convention compareDescendingTime documents.
	created time.Time

	// fileMissing is whether this plan's file on this machine was not there when
	// the row was derived: the plan's SourceHint, when that names a
	// file (planFile). It is why glyph draws ─. Enter on such a plan lands on the
	// missing-file panel, and the list is where the reader should see that coming.
	//
	// THIS MARK WAS ONCE DEFERRED FOR ITS COST: a
	// stat per row on every refresh. The cost is microseconds per plan on a local
	// disk, and listFileCheckBudget bounds what a slow mount's stats can add to a
	// load.
	// False is also the answer for "don't know": a stat that outran the budget, or
	// failed for any reason but the file not existing, keeps the circle.
	fileMissing bool
}

// glyphTint names the treatment a plan-state glyph takes, ONE PER CIRCLE, and it
// is deliberately not a colour: the palette lives in the ROW's own theme zone,
// which maps this to one of the styles it already carries (zoneStyles.forGlyph).
// glyph() reporting a bool is what made ○ unrenderable as its own state;
// reporting WHICH state leaves the colour where the palette is. ─'s tint is
// ○'s rather than a fourth: both say something is absent.
type glyphTint int

const (
	tintApproved  glyphTint = iota // ● approved
	tintAttention                  // ◐ unapproved, threads open
	tintQuiet                      // ○ unapproved, nothing open; ─ file missing
)

// glyphFileGone is ─, drawn where a row's circle would be when its file is not
// there: a plan's (glyph below) and a file Recently opened remembers
// with no plan (renderRecentRowPainted). One rune for both, so ─
// means one thing wherever it is drawn.
const glyphFileGone = '─'

// glyph reports the plan-state glyph and its tint. This is the positional-icon
// rule in code: approval folds into the one leading glyph instead of a separate
// marker column, so there is exactly one icon decision per row. Four states, all
// derived, no stored status anywhere:
//
//	─  the file is missing         dim      (theme Dim)
//	●  approved                    OK       (theme OK)
//	◐  unapproved, threads open    OK       (theme OK)
//	○  unapproved, nothing open    dim      (theme Dim)
//
// ○ RENDERS MUTED BECAUSE AN ABSENCE MUST NOT READ AS AN ALERT, which a
// two-valued return could not express: the row renderer branched on a bool, so ◐
// and ○ came out in the identical accent and were distinguishable by shape
// alone -- a different collapse from the one ● and ◐ deliberately share today
// (forGlyph's tint table, below).
//
// A MISSING FILE OUTRANKS THE OTHER THREE: "the file isn't there" is
// what the reader has to act on first, since enter on the plan reaches the
// missing-file panel rather than the approval or the threads. The dash replaces
// the circle and nothing else: COMMENTS still shows the counts. It is dim like ○
// because it is an absence too, and its shape is what tells the two apart. One
// function draws every section and Recently opened's copy of a plan, so ─ means
// the same thing wherever it is drawn.
//
// it.open feeds this glyph directly and the COMMENTS column derivatively
// (formatCounts renders total-open, its resolved count), both off the one
// field deriveListItems computes once -- so the glyph and the counts cannot
// disagree. Do not recompute it here from threads.
func (it planItem) glyph() (rune, glyphTint) {
	if it.fileMissing {
		return glyphFileGone, tintQuiet
	}
	if it.approvedTip {
		return '●', tintApproved
	}
	if it.open > 0 {
		return '◐', tintAttention
	}
	return '○', tintQuiet
}

// sourceLabel is BOTH the "Your plans" row's own SOURCE cell (renderRowPainted)
// and the SELECTED row's source, as the status bar shows it (see
// statusBarText) -- one renderer, two call sites, because both say "SOURCE"
// and neither reading needs its own copy of the clip below.
//
// ⚠️ IT TAKES A STYLE BECAUSE IT IS THE SITE THAT MADE THE FRAME OVERFLOW.
// SourceHint can hold any byte and reaches this row raw; a bare C0 byte in it is
// ZERO cells to ansi.StringWidth -- which is what leftClip and statusBarText budget
// with -- and ONE to lipgloss's Width().Render, so the bar came back one cell over
// its budget and wrapped (driven at 100x30: a 31-row frame against a 30-row
// terminal). Reverse video needs the style the cell is about to be drawn in,
// which is why it is a parameter. IT MUST RUN BEFORE THE CLIP.
//
// A non-empty SourceHint is shown HOME-RELATIVE and clipped from the RIGHT, so
// what survives truncation is where the file lives rather than what it is called.
// THIS REVERSED AN EARLIER RULING that kept the filename tail instead, and the
// argument that turned it is that a bare filename is not an answer: a
// reader who cannot tell ~/plans/x.md from ~/archive/x.md has been told the one
// thing they already knew. The path is still shown verbatim whether or not it
// resolves to a readable file.
//
// THE HOME ABBREVIATION IS WHAT MAKES THE CLIP WORTH DOING, rather than a
// cosmetic pass over it. This region is listRegionWidth (28) cells, and an
// absolute path under a macOS home directory spends THIRTEEN of them on
// "/Users/alice/" -- a prefix identical on every row, which the reader is
// paying nearly half the column to be told repeatedly. Measured on the
// developer's own paths, clipping without it rendered two different
// repositories under ~/development as the SAME 28 cells, indistinguishable in
// the one column that exists to tell them apart.
//
// NOTHING HERE STATS THE PATH: whether the plan opens from disk is a different
// question. homeRelative is a string operation for that reason -- it does not
// ask the filesystem whether either half exists.
//
// A BLANK SourceHint reads "draftplane": a plan an agent handed draftplane over
// MCP with no source at all lives in draftplane's own store and nowhere else.
func (it planItem) sourceLabel(width int, style lipgloss.Style) string {
	if it.plan.SourceHint != "" {
		return pathLabel(it.plan.SourceHint, width, style)
	}
	return ansi.Truncate("draftplane", width, "…")
}

// pathLabel draws a path the way sourceLabel draws a non-empty SourceHint --
// home-relative, filtered, clipped from the right; see its doc for why each --
// and it is shared with the status bar's file row, so a
// file's path and a plan's source read alike.
func pathLabel(path string, width int, style lipgloss.Style) string {
	// homeRelative FIRST, on the raw bytes, because its prefix match is about
	// what the path IS; VisibleControls then runs before the clip, which is the
	// invariant sourceLabel's own warning protects.
	return ansi.Truncate(ui.VisibleControls(homeRelative(path), style), width, "…")
}

// homeRelative rewrites a path under the user's home directory to start with
// "~", and returns everything else untouched.
//
// IT IS A DISPLAY CONCERN AND GOES NOWHERE NEAR A STORED VALUE. domain.Plan's
// SourceHint is matched by EXACT string elsewhere in this package (see
// actions.go's absolutization comment), so a "~" that reached storage would
// break that match; this runs at the paint, on a copy, for one column.
//
// A PREFIX MATCH ON home + separator, NEVER on home alone: "/Users/alice2/x"
// shares twelve characters with "/Users/alice" and is not under it, and
// abbreviating it to "~2/x" would name a directory that does not exist. The
// exact-equality arm above it is what still answers a SourceHint that IS the
// home directory.
//
// A SourceHint IS NOT ALWAYS A PATH -- "notion://abc123" is a URL source
// this column renders verbatim -- and such a value simply fails the prefix
// test, which is why this needs no separate guard for it.
//
// A FAILED HOME LOOKUP RETURNS THE PATH UNCHANGED rather than erroring: this
// is the difference between a row that reads a little longer and a row that
// cannot be drawn, and os.UserHomeDir fails only where $HOME is unset -- a
// state in which the abbreviation would have nothing to say anyway.
func homeRelative(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	home = filepath.Clean(home)
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return path
}

// rowKind names the five kinds of line the list body can hold. Only rowPlan
// is a plan; rowFile is a file Recently opened remembers with no plan behind
// it, and the other three are the chrome the sections bring with them --
// which is what makes a cursor that indexes plans unsafe (see row below).
type rowKind int

const (
	rowPlan rowKind = iota
	rowBand         // a section's header band, or its column-label row
	rowHint         // an empty section's own one-line hint
	rowMore         // "+N more", the tail a section had no room for
	rowFile         // a file opened with no plan, in Recently opened alone
)

// selectable reports whether the cursor may rest on a row of this kind.
//
// rowPlan, because every gesture that reaches a plan starts from the cursor.
// rowMore, because enter ON that row is the expand gesture -- a row the cursor
// cannot reach cannot be pressed. rowFile, because enter opens the file as
// `draftplane review <path>` would, and d removes the row at once with no
// confirm -- there being nothing to destroy. A band and an empty section's
// hint are pure chrome: moveCursor walks over them and rebuildRows pushes the
// cursor off.
func (k rowKind) selectable() bool { return k == rowPlan || k == rowMore || k == rowFile }

// listSection names the list's two sections as a value a row can carry: "Your
// plans", the catalog of every plan, and Recently opened, the
// working set drawn above it from plans the catalog already holds.
// cursorSection reads it off the row under the cursor, which is how s knows
// that Recently opened, whose order is when things were opened, has no
// sort to offer.
type listSection int

const (
	sectionMine listSection = iota
	sectionRecent
)

// title is the section's own band text, and the reason the two constants are
// not read directly anywhere a listSection is in hand.
func (s listSection) title() string {
	if s == sectionRecent {
		return listRecentSectionTitle
	}
	return listMineSectionTitle
}

// cursorSection is WHICH SECTION THE CURSOR IS IN, off the row it is on -- and
// every row carries a section, chrome included (row.section is set on every kind
// precisely so a reader like this one never derives it from a position). s
// reorders "Your plans" from anywhere but Recently opened, which has no order to
// choose.
//
// A CURSOR ON NOTHING answers sectionMine: with no rows at all there is no
// section to be in, and "Your plans" is the one the screen opens on.
func (m *ListModel) cursorSection() listSection {
	if r, ok := m.cursorRow(); ok {
		return r.section
	}
	return sectionMine
}

// bandTitle is a section's header band: its name, and nothing else.
//
// THE SORT CLAUSE WAS HERE AND IS DELIBERATELY GONE. The band read "Your plans ·
// updated ↓", and disclosed something the reader necessarily already knew:
// m.sortOrder is in-memory state with no config behind it, so a fresh process
// always opens on the default order and the only way the list is in any other
// is that this reader pressed s in this session -- which the modal states the
// instant it opens.
//
// A ▸ MARKING THE CURSOR'S SECTION WAS HERE TOO, AND IS GONE: ▸ is the
// disclosure triangle of every file tree, and a band wearing
// one invites an enter that expands something, on a row no gesture can act on.
// An affordance that lies costs more than a disclosure that arrives with the
// box. The constant's own note had already rejected "▌" for this exact failure
// and then picked a glyph with the same one.
//
// IT SURVIVES AS A SEAM rather than collapsing into section.title() at its call
// site: this is where a band's words are decided. Used by every mode that draws
// bands -- browse, filter, sort and expanded -- through the one sectionHead.
func (m *ListModel) bandTitle(section listSection) string {
	return section.title()
}

// lastColumnLabel is the header this section puts over the trailing region:
// SOURCE over "Your plans", where each row names its file (planItem.sourceLabel),
// and OPENED over Recently opened, where each row says when it was opened.
// It is a label per SECTION because that is the only place a single label is
// true of every row beneath it.
func (s listSection) lastColumnLabel() string {
	if s == sectionRecent {
		return listOpenedLabel
	}
	return listSourceLabel
}

// row is one line of the list body, and the unit the cursor and scroll both
// index. Indexing plans and indexing rows agreed only while buildRows emitted one
// plan row per plan; browse mode now emits a band and a column-label row per
// section, a separator, an empty section's hint and a +N more row around them,
// so selectedPlan is the only way any gesture may reach a plan.
//
// item carries the plan BY VALUE for rowPlan (the zero planItem otherwise), and
// text carries the line's own words for every other kind. By value rather than an
// index into m.items is load-bearing in one place: rebuildRows reads the outgoing
// cursor's plan AFTER m.items has already been replaced.
//
// section is which section emitted the row, and it is set on EVERY
// row a section emits rather than only on the kinds that read it. A field set on
// some rows and not others is a field whose zero value means two things --
// "sectionMine" and "nobody said" -- which is the ambiguity the next reader
// resolves by guessing. Several readers rely on that guarantee holding for
// every kind, cursorSection among them: it asks the row under the cursor of
// ANY kind.
type row struct {
	kind    rowKind
	item    planItem
	text    string
	section listSection

	// head marks a section's NAME band, the first of the two rows sectionHead
	// emits, and it exists so the renderers can tell it from the column-label row
	// beside it -- which they must, because only the name band takes the
	// band's own ink (renderBodyRowPainted).
	//
	// A BOOL AND NOT ANOTHER rowKind, deliberately: rowKind is the CURSOR's
	// vocabulary, and this distinguishes two rows the cursor already treats
	// identically -- both chrome, both unselectable -- for the renderer alone.
	head bool

	// path is a rowFile's file, and opened is when a Recently opened row's entry
	// was recorded; both are zero on every other row.
	path   string
	opened time.Time

	// gone marks a rowFile whose file is no longer there to open, and
	// it is head's kind of field: A BOOL AND NOT ANOTHER rowKind, because the
	// cursor, enter, d, the status bar and the help bar all treat the row as the
	// file row it is. Only renderRecentRowPainted reads it, to draw ─ in place of
	// the circle and no counts.
	gone bool
}

// buildRows derives the body's rows from the plan set, and it is MODE-AWARE because
// the layout is. Browse draws Recently opened, when it has rows,
// above "Your plans" -- each a header band, a column-label row and its own content,
// with one separator row between them -- seating Recently opened first and giving
// "Your plans" whatever it leaves (sectionRows). listFilter, listSort AND EVERY
// CENTRED BOX draw the same, which is why none has an arm of its own: the filter
// changes the item set "Your plans" is drawn from and stands Recently opened aside,
// and the sort modal and the boxes change nothing at all about the body
// (drawsRecent). EXPANDED draws "Your plans" whole (expandedRows), and no Recently
// opened. A panel mode draws none of that: one plan row per plan, in m.items' own
// sorted order.
//
// ALL THREE SHAPES READ filteredItems RATHER THAN m.items, so a filter narrows ONE
// set and every body agrees about which plans exist -- including the flat one a
// panel draws, whose cursor is carried across by plan id. A filter that stopped at
// the section builder would make e on a filtered row open a rename panel over a body
// showing plans the filter had just excluded.
//
// THE FLAT SHAPE IS NOT A DEGRADED FALLBACK, it is what leaves the panel geometry
// untouched: sections are a BROWSE-MODE feature, so a panel mode's chrome stays
// the rows it was measured at and confirmBudget's m.height-8 keeps its budget
// exactly.
//
// TWO INPUTS BEYOND m.items, and each buys an obligation elsewhere. It reads
// GEOMETRY, which is why tea.WindowSizeMsg rebuilds the rows rather than merely
// re-clamping scroll; and it reads MODE, which is why every mode transition goes
// through setMode -- a row slice built for the wrong mode would leave the cursor
// indexing a body nobody is drawing.
func (m *ListModel) buildRows() []row {
	if m.isPanelMode() {
		items := m.filteredItems()
		rows := make([]row, 0, len(items))
		for _, it := range items {
			rows = append(rows, row{kind: rowPlan, item: it, section: sectionMine})
		}
		return rows
	}
	if m.mode == listExpanded {
		return m.expandedRows()
	}
	return m.sectionRows()
}

// filterQuery is the filter's query in the form the match is actually made in:
// case-folded, with the ends trimmed so a trailing space typed before the next
// word narrows nothing on its own. "" means NO FILTER, and it is the one answer
// filterActive and filteredItems both key on, so a query of pure whitespace can
// never be "active but matching nothing". THE RAW STRING IS WHAT THE HELP BAR
// SHOWS (m.filter): the reader must see the characters they typed, including a
// space they are in the middle of, or backspace stops corresponding to the screen.
func (m *ListModel) filterQuery() string {
	return strings.ToLower(strings.TrimSpace(m.filter))
}

// filterActive reports whether a filter is narrowing the list right now, in EVERY
// mode rather than only in listFilter: the query outlives the mode that typed it
// (enter leaves the mode and keeps it -- see leaveFilter), so "is a filter on" and
// "am I typing one" are two questions and this is the first.
func (m *ListModel) filterActive() bool {
	return m.filterQuery() != ""
}

// matches is the filter's predicate for one row, against an already-folded query
// (filterQuery): the plan's title. Substring rather than prefix: "redesign" should
// find "Auth redesign", which is how a human filters a list of sentences.
func (it planItem) matches(query string) bool {
	return strings.Contains(strings.ToLower(it.plan.Title), query)
}

// filteredItems is THE SET EVERY BODY IS BUILT FROM, and the whole of the
// filter's mechanism is that it is applied HERE -- to the loaded set, upstream of
// the sort and of the rows browse has room for -- rather than to the rows a body
// emitted.
//
// TWO SETS EXIST AND ONLY ONE IS RIGHT. VISIBLE is what the allocation put
// on the screen; LOADED is every plan the last load read. The filter matches
// LOADED, so it reaches rows sitting behind a "+N more" -- which is the entire
// point, and the half an implementer gets wrong, because on a fixture whose plans
// all fit the two are the same function. TestTheFilterReachesRowsBehindPlusNMore
// is built on a fixture where they are not.
//
// m.items ITSELF IS RETURNED UNFILTERED WHEN NO FILTER IS ON, so the everyday
// path allocates nothing -- this runs on every rebuild, which is every keystroke
// that moves the cursor.
func (m *ListModel) filteredItems() []planItem {
	q := m.filterQuery()
	if q == "" {
		return m.items
	}
	out := make([]planItem, 0, len(m.items))
	for _, it := range m.items {
		if it.matches(q) {
			out = append(out, it)
		}
	}
	return out
}

// sortedItems puts the plan set in "Your plans"' order (m.sortOrder), on a
// copy.
//
// THE SORT IS APPLIED HERE AND NOWHERE ELSE, which is what makes one keystroke
// enough. Both bodies that draw the section read this (sectionRows and
// expandedRows), so a re-sort is one field assignment plus a rebuild. It is also
// what keeps the sort OFF m.items: the loaded set stays in its derivation order
// (sortItems), and the chosen order is a property of the body being drawn. The
// copy is what makes that true, since sortSectionItems sorts in place and
// filteredItems hands back m.items itself when no filter is on.
//
// A PANEL MODE IS DELIBERATELY UNSORTED BY THIS: buildRows' panel arm draws the
// FLAT body straight off filteredItems and never calls this, exactly as it never
// draws a band -- sections are a browse-mode feature, and so is their sort.
func (m *ListModel) sortedItems(items []planItem) []planItem {
	return sortSectionItems(slices.Clone(items), m.sortOrder)
}

// rowBudget is the rows left for "Your plans"' CONTENT once browse mode's whole
// chrome is reserved. viewHeight has already taken the masthead block and the
// status/help bars off m.height; listSectionChrome is the part that lives INSIDE
// the body and so cannot come off there -- the band and the column-label row. The
// two together are listBrowseChrome, which is why this is m.height -
// listBrowseChrome at every width and height. Recently opened, when it is drawn,
// is paid for out of this (sectionRows).
func (m *ListModel) rowBudget() int {
	return m.viewHeight() - listSectionChrome
}

// sectionRows is browse mode's body: "Your plans", under Recently opened when it
// is drawn. The row slice it returns is never longer than viewHeight, which
// is what pins m.scroll at 0 in the collapsed state (clampScroll's
// len(rows)-viewHeight is never positive) -- see moveCursor's doc comment for why
// that is a ruling and not an accident.
//
// RECENTLY OPENED DOES NOT BREAK THAT, because it is paid for out of the same
// budget: its chrome and rows come off rowBudget first, allocateRecent seats it
// only where "Your plans" keeps its floor, and "Your plans" draws into whatever is
// left (sectionBody). Rows neither claims are blank filler, drawn by View's own pad
// loop.
func (m *ListModel) sectionRows() []row {
	items := m.sortedItems(m.filteredItems())

	budget := m.rowBudget()
	recentBody := m.recentBody()
	recentRows := allocateRecent(budget, len(recentBody))
	if recentRows > 0 {
		budget -= listRecentChrome + recentRows
	}

	rows := make([]row, 0, listRecentChrome+recentRows+listSectionChrome+max(budget, 0))
	if recentRows > 0 {
		rows = append(rows, m.sectionHead(sectionRecent)...)
		rows = append(rows, recentBody[:recentRows]...)
		// The separator: one blank line between the sections, and the third of
		// Recently opened's own chrome rows. A band row with no words rather than a
		// rule, so the two sections read as two blocks without a second horizontal
		// line competing with each band's own.
		rows = append(rows, row{kind: rowBand, section: sectionRecent})
	}
	rows = append(rows, m.sectionHead(sectionMine)...)
	return append(rows, m.sectionBody(items, m.emptyHint(), budget)...)
}

// expandedRows is the expanded mode's body: "Your plans"' WHOLE list, under its
// own band.
//
// THE ROW SLICE IS ALLOWED TO RUN PAST viewHeight HERE, and that is the entire
// difference from sectionRows above. In the collapsed state the slice is never
// longer than the viewport, so m.scroll is structurally pinned at 0; here the slice
// is as long as the section is, and m.scroll/ensureVisible carry the viewport over
// it. Because the band is IN that slice, scrolling can carry it off the top exactly
// as it carries a plan row off: the chrome is a statement about the BUDGET, not a
// promise that it is on screen at every offset, and a sticky band would mean two
// independently scrolled regions and a cursor that indexes neither.
func (m *ListModel) expandedRows() []row {
	items := m.sortedItems(m.filteredItems())
	hint := m.emptyHint()
	// max(len, len) rather than a bare count: sectionBody reads its row allowance as
	// a cap, and a section whose plans all went away while it was expanded still
	// owes its hint the lines the hint has.
	return append(m.sectionHead(sectionMine), m.sectionBody(items, hint, max(len(items), len(hint)))...)
}

// sectionHead is a section's two-row band: its name, then the column labels, which
// are two of browse mode's chrome rows. The labels are laid out for a row the
// cursor band cell is prepended to (renderBodyRowPainted does that), which is why
// they come from columnLabels with the glyph width alone as their lead rather than
// from listHeaderRow.
func (m *ListModel) sectionHead(section listSection) []row {
	rowWidth := m.rowWidth()
	return []row{
		{kind: rowBand, text: m.bandTitle(section), section: section, head: true},
		{kind: rowBand, text: m.columnLabels(listGlyphWidth, max(rowWidth-listBandWidth, 0), section), section: section},
	}
}

// sectionBody is "Your plans"' allocated rows: its plans, its +N more tail, or --
// with no plans -- as much of its hint as the allocation holds. It draws no more
// rows than it has content for: what is left of the allocation is filler.
//
// +N MORE CONSUMES AN ALLOCATED ROW rather than being drawn beside the section's
// last plan, so the chrome never becomes conditional and the layout never jumps: a
// section with more plans than its r rows draws exactly r. That makes the count
// n - (r - 1) and not n - r. UNDER A FILTER IT COUNTS MATCHES without a line of its
// own, since items arrives already narrowed -- "+N more stays and counts
// matches, no new vocabulary".
func (m *ListModel) sectionBody(items []planItem, hint []string, rows int) []row {
	if rows <= 0 {
		return nil
	}
	indent := strings.Repeat(" ", listGlyphWidth)
	if len(items) == 0 {
		lines := hint
		if len(lines) > rows {
			// The hint asked for more rows than there are (a short terminal): keep
			// the opening lines, which is where a hint says what state it is.
			// Nothing here rewords them.
			lines = lines[:rows]
		}
		out := make([]row, 0, len(lines))
		for _, l := range lines {
			out = append(out, row{kind: rowHint, text: indent + l, section: sectionMine})
		}
		return out
	}
	if rows >= len(items) {
		out := make([]row, 0, len(items))
		for _, it := range items {
			out = append(out, row{kind: rowPlan, item: it, section: sectionMine})
		}
		return out
	}
	shown := rows - 1
	out := make([]row, 0, rows)
	for _, it := range items[:shown] {
		out = append(out, row{kind: rowPlan, item: it, section: sectionMine})
	}
	return append(out, m.moreRow(len(items)-shown))
}

// moreRow builds "Your plans"' "+N more" line: the n plans browse had no room for,
// and the row enter expands the section from (enterExpanded).
func (m *ListModel) moreRow(n int) row {
	return row{kind: rowMore, text: fmt.Sprintf("%s+%d more", strings.Repeat(" ", listGlyphWidth), n), section: sectionMine}
}

// emptyHint is what "Your plans" says with nothing in it, and it is ONE function for
// both body builders so the two can never answer the same state differently.
//
// THE QUESTION IS NOT "IS A FILTER ON", IT IS "DID THE FILTER EMPTY THIS SECTION",
// and those differ whenever the section was empty anyway. So the filter arm is
// guarded on the loaded set holding plans the query excluded, rather than on
// filterActive alone: a query typed over a store with no plans at all must not
// replace the hint that says how to make one. It names the query, because "no
// plans match" without it leaves the reader unable to see what they must change.
//
// A LIST STILL AWAITING ITS FIRST ANSWER CLAIMS NOTHING, and that arm is first
// because every arm after it is a claim about an answer: neither "No plans yet!"
// nor "no plans match" is known until the load lands. It is one
// blank row so the section keeps its slot, and the frame around it is the one the
// answer will land in.
func (m *ListModel) emptyHint() []string {
	if m.awaiting {
		return []string{""}
	}
	if m.filterActive() && len(m.items) > 0 {
		return []string{fmt.Sprintf("no plans match %q", m.filter)}
	}
	return []string{listEmptyHint}
}

// leftClip truncates s from the left to at most width display cells, keeping the
// tail and marking the cut with a leading "…" -- ansi.Truncate's mirror image for
// chrome whose salient end is on the RIGHT.
//
// THREE CALLERS, AND THE SAME RULE THREE TIMES: the status bar's own origin (a
// path's filename, not its root), the review model's search input row
// (searchInputLine) and the re-point pane's field (repointInputRow), where the tail
// is the newest characters the reader is typing -- a head-keeping truncation there
// freezes the visible text at the first keystroke past the edge and leaves backspace
// corresponding to nothing on screen.
//
// "AT MOST" IS ENFORCED HERE, and it was not until this had a second caller.
// ansi.TruncateLeft cannot cut INSIDE a double-width cell: when the n cells it is
// asked to drop end mid-character it keeps that whole character and comes back one
// cell OVER the budget -- an EVEN budget over a run of wide characters, which is
// every ODD terminal width once a caller reserves a cell of its own. Asking for
// one more cell lands on the far side of that character and fits.
//
// THE RETRY CAN EMPTY THE STRING, at width 2 and nowhere else: that is the only
// budget where n+1 reaches StringWidth(s). A bare prefix is the answer there for
// the same reason it is at width 1.
func leftClip(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	const prefix = "…"
	prefixWidth := ansi.StringWidth(prefix)
	if width <= prefixWidth {
		// No room for even one original character alongside the prefix:
		// ansi.TruncateLeft's ignoring-count only crosses over on curWidth
		// exceeding n, which n == StringWidth(s) (the "drop everything"
		// case this reduces to) can never do — it would silently return ""
		// instead of a bare, budget-fitting prefix.
		return ansi.Truncate(prefix, width, "")
	}
	n := ansi.StringWidth(s) - width + prefixWidth
	out := ansi.TruncateLeft(s, n, prefix)
	if ansi.StringWidth(out) > width {
		out = ansi.TruncateLeft(s, n+1, prefix)
	}
	if out == "" {
		return prefix
	}
	return out
}

// padRight pads s with spaces out to width display cells; a no-op if s is
// already at or past width. Every caller has already clipped s to width
// (ansi.Truncate/leftClip), so this only ever adds trailing padding to keep
// column positions identical across rows.
func padRight(s string, width int) string {
	if pad := width - ansi.StringWidth(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

// padRightIn is padRight for a cell that has been through ui.VisibleControls, and
// it exists for this file's own rule: every fragment is rendered through some
// style, never concatenated as a naked string (renderCellsPainted says why).
//
// A SUBSTITUTED CONTROL GLYPH IS A NESTED Render, and a nested Render ends with a
// full SGR reset, so bare pad spaces after one paint on the terminal's default
// background instead of the zone's -- a visible notch. When the filter substituted
// nothing there is no reset to be after, and this is byte-for-byte padRight: the
// ESC test is what keeps every benign row identical, and an untrusted string cannot
// hold an ESC this renderer did not put there, because that byte is in the
// predicate too.
func padRightIn(s string, width int, style lipgloss.Style) string {
	pad := width - ansi.StringWidth(s)
	if pad <= 0 {
		return s
	}
	blanks := strings.Repeat(" ", pad)
	if !strings.Contains(s, "\x1b") {
		return s + blanks
	}
	return s + style.Render(blanks)
}

// formatCounts renders the resolved/total column, clipped to listCountsWidth as
// a backstop -- OR OMITS IT: total==0 returns "", not "0/0", because a plan
// with no comments has nothing to count, and a blank cell says that. A zero
// would say something else -- that comments exist and every one of them is
// resolved -- which is not this plan's situation. This is the same rule
// ("with no file there is nothing to count, not zero", the blank COMMENTS cell a
// Recently-opened row draws for a gone file) applied to a plan instead of a
// file: the principle was never about files specifically, it was about what a
// count means when there is nothing behind it. The name is narrower than the
// job now that it formats a count OR omits one, kept anyway because every
// caller reaches this function to answer the same question -- what goes in
// the COMMENTS cell -- and that has not changed.
//
// An oversized count (many threads piled onto one plan, plausible with agents
// posting over MCP) handed to padRight unclipped would exceed the column --
// and would silently word-wrap under Width().Render instead of truncating,
// desyncing the one-line-per-row assumption the layout depends on.
//
// IT TAKES resolved, NOT open: a caller computes total-open itself (it.open is
// unresolved threads, never resolved ones), so the subtraction sits where the
// two meanings could be confused rather than hiding inside a helper whose name
// does not say it inverts anything. The glyph's own reading is why --
// ◐ is "unapproved, threads open", and 1/1 read as complete beside a glyph
// saying otherwise. resolved/total agrees with it: 0/1 unresolved, ◐.
func formatCounts(resolved, total int) string {
	if total == 0 {
		return ""
	}
	return ansi.Truncate(fmt.Sprintf("%d/%d", resolved, total), listCountsWidth, "")
}

const (
	// listMaxTitleWidth is NAME's ceiling, and it is what makes the trailing
	// region grow on a wide terminal. Until it, NAME
	// absorbed EVERY surplus cell -- listColumnWidths' "the surplus goes to NAME"
	// -- against a region pinned at listRegionWidth, so a 47-cell title sat in 73
	// cells while SOURCE beside it clipped a 56-cell path to 28. NAME now stops
	// here and the region takes the rest, because a path grows with the terminal:
	// there is always more path to show.
	//
	// 68 IS RULED, NOT DERIVED, and what it costs is measured: a title of
	// 69-73 cells clips where it did not before, and ONLY on a terminal at or
	// above 115 cells. NAME reaches 68 at rowWidth 111, so every narrower tier --
	// 80 included -- is byte-identical to what shipped before this.
	listMaxTitleWidth = 68
	// listBandWidth is the cursor band: the ▌ glyph and ONE TRAILING SPACE. A single
	// cell put the band hard against the plan-state glyph beside it.
	listBandWidth     = 2
	listGlyphWidth    = 2 // glyph rune + 1 trailing space
	listCountsWidth   = 6
	listGapWidth      = 2
	listMinTitleWidth = 16

	// listLastGapWidth is the gap between the counts column and the last column, and
	// it is THREE where every other gap is two -- derived from the header, not
	// chosen. "COMMENTS" is 8 cells over a 6-cell column, so the label already spends
	// the ordinary 2-cell gap and at listGapWidth the header row reads
	// "COMMENTSSOURCE". The third cell is spent on every row so the columns stay
	// aligned with their labels, and it costs the title column one cell.
	listLastGapWidth = 3

	// listRegionWidth is the trailing region's BASE width: what SOURCE (or
	// Recently opened's OPENED) gets at every width until NAME reaches its ceiling
	// (listMaxTitleWidth), which hands the region everything above. 28 cells holds
	// an everyday home-relative path whole -- "~/plans/billing-migration.md" is 28
	// -- and it is the width the region has always started from, so every terminal
	// draws the columns where it always has. Below the width that affords it, the
	// region yields to NAME's floor (listColumnWidths).
	listRegionWidth = 28

	// listRowFixedWidth is every column a row keeps at every width: the
	// cursor band, the glyph, both gaps and the counts column. Named here
	// rather than left inline in listColumnWidths because listMinWidth is
	// derived from it — the width gate and the column math must not be able
	// to disagree about what "fixed" means.
	listRowFixedWidth = listBandWidth + listGlyphWidth + listGapWidth + listCountsWidth + listLastGapWidth

	// listMastheadHeight is a PANEL mode's fixed top block: a row of ground, the
	// masthead line, a blank line, and the column-header row. See viewHeight for why
	// it is reserved unconditionally. THE GROUND ROW IS COUNTED HERE: it is a real
	// row of the altscreen, and a top margin the height arithmetic does not know
	// about is one row of overflow at the bottom.
	//
	// THIS IS RENAME'S OWN BLOCK, AND RENAME ALONE (isPanelMode is exactly
	// listRename), and it does NOT draw the masthead box listBrowseMastheadHeight
	// reserves below: rename's textarea strip already spends m.ta.Height()+2 more
	// rows on top of whatever this reserves, on a mode whose own height arithmetic
	// already runs close to the floor (confirmBudget's "-8, not -7" and pageSize's
	// own floor test both name it), and the box has never been driven there at a
	// real terminal the way the browse-family masthead has. listMastheadText, a
	// plain Brand+bold line, is what it still draws.
	listMastheadHeight = 4

	// listBrowseMastheadHeight is the top block of every mode that draws SECTIONS
	// -- browse, filter, sort and expanded alike, and every CENTRED confirm mode
	// too (isPanelMode is false for those; mastheadHeight falls back to this
	// branch) -- and NOT a column-header row: the column labels belong to each
	// section's own band pair, so another copy above them would label a body that
	// has headers of its own. Its ground row is counted the same way
	// listMastheadHeight's is.
	//
	// THE MASTHEAD LINE IS A BOX, THREE ROWS AND NOT ONE: ground(1) + the box's top
	// rule, its sigil-and-name, and its bottom rule (3) + blank(1) = 5. The box's own
	// geometry -- indent, pad, border, colour -- is built by mastheadBoxLines and
	// styled at viewPainted's own call site; what belongs here is only its ROW
	// COUNT, because listBrowseChrome and, through it, listMinHeight are DERIVED
	// from this constant and both must grow with it or "Your plans"' guaranteed
	// floor row stops fitting below the gate that promises it -- see listMinHeight.
	listBrowseMastheadHeight = 5

	// listSectionChrome is the part of browse mode's chrome that lives inside the
	// BODY, and so cannot be taken off m.height by viewHeight: "Your plans"' header
	// band and its column-label row.
	listSectionChrome = 2

	// listRecentChrome is Recently opened's own chrome: its band, its
	// column-label row and the separator under it. It is NOT part of
	// listSectionChrome, because the section is drawn only when it has rows to
	// show -- sectionRows takes it out of rowBudget's answer then, and only then.
	listRecentChrome = 3

	// listRecentShown is how many rows Recently opened draws at most.
	// The file keeps recent.Keep, so an entry that no longer resolves lets the
	// next one in rather than shrinking the section.
	listRecentShown = 5

	// listSectionFloor is the row "Your plans" keeps before Recently opened is
	// seated (allocateRecent), so the catalog always draws at least one row of its
	// own: its one plan, a "+N more" standing for all of them when it holds more
	// than one (sectionBody), or the first line of its empty-state hint.
	listSectionFloor = 1

	// listBrowseChrome is the chrome of every mode that draws sections -- browse,
	// filter, sort and expanded alike, since the sort modal is composited over the
	// frame and takes no row out of it: ground, the masthead box, blank, "Your
	// plans"' band and its column-label row, status, help. Written as its three
	// parts rather than as a literal so that moving any one of them moves this
	// number with it.
	//
	// IT IS THE BUDGET, NOT A PROMISE ABOUT THE SCREEN. In browse the rows are always
	// drawn; in expanded the row slice can be longer than the viewport, so scrolling
	// can carry the band off the top -- the reservation is unchanged (see
	// expandedRows).
	listBrowseChrome = listBrowseMastheadHeight + listSectionChrome + 2

	// listMinHeight is the minimum-size gate's height, DERIVED and not picked, from
	// what a browse frame draws: the ground row, the masthead box's three rows and
	// the blank under it (listBrowseMastheadHeight, 5); "Your plans"' band and its
	// column-label row (listSectionChrome, 2); the status and help bars (2); and one
	// row of the section's own content (listSectionFloor) -- 5 + 2 + 2 + 1 = 10.
	// Below it "Your plans" has no row of its own at all, which is the smallest
	// thing this screen can honestly be -- so the list refuses rather than degrades.
	// Recently opened adds nothing here: it is drawn only where there is room for it
	// beside that floor (allocateRecent).
	listMinHeight = listBrowseChrome + listSectionFloor

	// listMinWidth is that gate's width, derived the same way from the row layout's
	// own two numbers: the columns that never shrink, plus the title column's
	// declared minimum. Below it listColumnWidths is already out of cells to give.
	//
	// IT ALSO CLOSES THE m.width <= 12 RESIDUAL, which is why the gate extends to
	// width at all: 31 > 12, so no drawn view can be in that state any more.
	// (listRowFixedWidth is 15 -- 2 + 2 + 2 + 6 + 3 -- and listMinTitleWidth is 16.
	// This line read "30 > 12" while confirmGroups' own doc already said
	// "listMinWidth = 31" a thousand lines below, so the file disagreed with
	// itself about the number its gate is set at.)
	listMinWidth = listRowFixedWidth + listMinTitleWidth

	// listMastheadText is the masthead's literal content, drawn Brand+bold. It spans
	// the FULL m.width, uncapped by listMaxRowWidth -- the status/help-bar
	// convention, since a 120-capped title line would contradict the uncapped status
	// bar below it.
	//
	// TODAY THIS IS RENAME'S OWN MASTHEAD LINE ALONE, drawn by isPanelMode's branch
	// of viewPainted's masthead block: every other mode draws mastheadBoxContent's
	// box instead. It survives as its own literal, rather than being folded into
	// mastheadBoxContent, because the two are two different renderings of the same
	// word -- a plain line and a box's fixed interior -- that must not drift apart
	// on a rename of the product; see mastheadBoxContent.
	listMastheadText = " Draftplane"

	// mastheadSigil is Draftplane's adopted logo mark -- the section-sign glyph
	// the product's own favicon draws in #ef86b2, theme.Brand on dark. It is ONE
	// display cell in every terminal,
	// verified with ansi.StringWidth (the same measure mastheadBoxLines' own
	// truncation uses, and every other width check in this file): no emoji, no
	// ZWJ, nothing a terminal might render at width 2 or split across cells.
	// Nothing else may stand in for it here.
	mastheadSigil = "§"

	// mastheadBoxContent is the masthead box's fixed interior: the sigil, then
	// listMastheadText's own leading space and name, giving "§ Draftplane" --
	// built off that literal rather than retyped, so a rename of the product
	// moves both without one drifting out of step with the other.
	//
	// IT NEVER VARIES WITH ANYTHING READ AT DRAW TIME: a logo whose width changes
	// with the data on screen is not a logo.
	mastheadBoxContent = mastheadSigil + listMastheadText

	// mastheadIndent is the OUTER gap between the rail inset and the box's own
	// left border -- outside the box entirely, unlike mastheadPad below, which is
	// inside it. It was driven up from 0 at a real terminal and stopped at 1; a
	// body row's own glyph and title columns
	// (2 and 4 cells past the inset) are a coincidence of that same layout, not
	// where this axis was aimed -- see that prototype's own note on the point.
	mastheadIndent = 1

	// mastheadPad is the INNER gap: border to sigil on the left, name to border on
	// the right, four columns each side. Driven at a real terminal, and where it
	// settled.
	mastheadPad = 4

	// mastheadBorderTL, mastheadBorderTR, mastheadBorderBL, mastheadBorderBR,
	// mastheadBorderH and mastheadBorderV are the box's one border alphabet:
	// rounded corners at the light weight. All four of Unicode's box-drawing
	// alphabets were tried at a real terminal -- light/rounded, light/square,
	// heavy, double (rounded corners exist only at the light weight, so "heavy
	// rounded" is not a real fifth option) -- and heavy and double were both
	// rejected because they drew too much attention and overwhelmed the plans: a
	// stroke that thick, sitting one row above the plans, stopped reading as a
	// mark and started competing with the screen it introduces. Light/rounded is
	// what stayed. (See viewPainted's own masthead block for why bold is off too,
	// and why that is a SEPARATE finding from this one, not the same one stated
	// twice.)
	mastheadBorderTL = "╭"
	mastheadBorderTR = "╮"
	mastheadBorderBL = "╰"
	mastheadBorderBR = "╯"
	mastheadBorderH  = "─"
	mastheadBorderV  = "│"

	// listMineSectionTitle is the catalog's band: every plan this store holds.
	listMineSectionTitle = "Your plans"
	// listRecentSectionTitle is Recently opened's section, drawn above it: the
	// working set, where "Your plans" is the catalog.
	listRecentSectionTitle = "Recently opened"

	// The trailing region's own labels -- see listSection.lastColumnLabel for which
	// section gets which. listSourceLabel heads "Your plans"' paths
	// (planItem.sourceLabel); listOpenedLabel heads Recently opened's ages.
	listSourceLabel = "SOURCE"
	listOpenedLabel = "OPENED"

	// listCursorGlyph is "the cursor is on THIS row", and it is one constant because
	// it is drawn in two places over two different lists: the body's plan rows
	// (paintedBand) and the sort modal's own option rows (sortModalContent). Two
	// literals for one mark is how one of them comes to be a different glyph.
	listCursorGlyph = "▌"

	// The sort modal's own vocabulary. The MODAL is the only reader of all five. They stay one
	// constant apiece because the modal itself reads each label from two places
	// that must agree: the width pass that sizes the box against the longest word,
	// and the render pass that pads the option row to it (sortModalContent).
	listSortUpdatedLabel = "updated"
	listSortCreatedLabel = "created"
	listSortNameLabel    = "name"

	// The arrow does BOTH jobs at once -- it names the active column and its
	// direction -- which removes the separate selection column an options list would
	// otherwise need. Both are single-cell glyphs, which the modal's width arithmetic
	// depends on.
	listSortDescendingArrow = "↓"
	listSortAscendingArrow  = "↑"

	// listSortTitleSeparator joins the modal's own title to the section it names --
	// "sort · Your plans" -- the same " · " the status bar already joins its clauses
	// with.
	listSortTitleSeparator = " · "

	// listSortTitlePrefix leads the modal's own title, which then names the section
	// the order applies to: "Your plans", never Recently opened, whose order is when
	// things were opened.
	listSortTitlePrefix = "sort"

	// listSortHint is the modal's own footer and the help bar's line while it is
	// open, one constant for both so a key can never be named in one and not the
	// other. It names both exits and they are not the same exit: enter APPLIES
	// the highlighted column, esc discards the whole interaction.
	listSortHint = "enter apply · esc cancel"
)

// listColumnWidths derives the title and trailing-region widths for a row of
// rowWidth cells (already capped to listMaxRowWidth by the caller). ONE
// GEOMETRY FOR EVERY SECTION: this returns a single titleWidth and a single
// regionWidth for every row, whichever section drew it. What goes IN the region
// -- SOURCE or OPENED -- is decided at the renderer (renderRowPainted and
// renderRecentRowPainted for the body, columnLabels for the header), never here.
//
// The title column absorbs surplus width as padding and gives up width first;
// only once it would drop below listMinTitleWidth does the region give up
// cells in its place, one for one. The cursor band, glyph, gaps and the counts
// column never shrink -- the positional-icon rule requires their positions stay
// identical on every row at every width.
//
// THE REGION STARTS AT listRegionWidth and grows only once NAME reaches its
// ceiling (listMaxTitleWidth); see that constant for why the surplus goes to the
// region from there.
func listColumnWidths(rowWidth int) (titleWidth, regionWidth int) {
	fixed := listRowFixedWidth
	regionWidth = listRegionWidth
	titleWidth = rowWidth - fixed - regionWidth
	// NAME's ceiling, and the whole of how the region grows: past this the surplus
	// is the REGION's. Checked before the deficit arm below, never after -- the two
	// never both apply (a row too narrow for listMinTitleWidth is nowhere near this
	// ceiling).
	if titleWidth > listMaxTitleWidth {
		titleWidth = listMaxTitleWidth
		regionWidth = rowWidth - fixed - titleWidth
	}
	if titleWidth < listMinTitleWidth {
		deficit := listMinTitleWidth - titleWidth
		regionWidth = max(regionWidth-deficit, 0)
		titleWidth = rowWidth - fixed - regionWidth
	}
	if titleWidth < 0 {
		titleWidth = 0
	}
	return titleWidth, regionWidth
}

// zoneStyles bundles the foreground treatments a list row needs against one
// background zone: plain text, dim (chrome rows, the last column, and the
// quiet glyph), accent (the attention glyph), and OK (the approved glyph).
type zoneStyles struct {
	text, dim, accent, ok lipgloss.Style
}

// forGlyph is the tint table: three styles this zone already carries, so the
// third tint adds no colour to the theme. It was one of a pair, beside the
// systemGlyphStyle of a foreground-only render path since deleted -- see
// glyphTint.
func (z zoneStyles) forGlyph(t glyphTint) lipgloss.Style {
	switch t {
	case tintApproved:
		return z.ok
	case tintQuiet:
		return z.dim
	}
	// ◐ IS OK's TOKEN AND NOT Accent's (green on dark, cerulean on light),
	// WHICH IS A SHARED COLOUR ON PURPOSE:
	// ● approved and ◐ open are both "this plan has been engaged with", and the
	// GLYPH carries which. That is the review view's own scheme, where the
	// gutter mark draws ※ open and ○ all-resolved in one colour and lets the
	// shape say the rest (ui/painted.go) -- the list was the exception.
	//
	// IT ALSO GIVES Accent BACK. The section head above wears it now, and a
	// colour doing double duty one row apart is what sent this here: the head
	// and the glyphs were competing for the same eye.
	return z.ok
}

// zone builds the Doc- or Card-background style set for a row: ui.Styles
// carries no Dim- or OK-tinted style against the Card background (only the
// document's own Card/CardHeader/CardBody combinations), so this pairing —
// needed only by the list — is built directly from the theme's raw colors
// instead of extending the shared ui.Styles for it.
func (m *ListModel) zone(isCursor bool) zoneStyles {
	bg := lipgloss.Color(m.theme.Doc)
	if isCursor {
		bg = lipgloss.Color(m.theme.Card)
	}
	base := lipgloss.NewStyle().Background(bg)
	return zoneStyles{
		text:   base.Foreground(lipgloss.Color(m.theme.Text)),
		dim:    base.Foreground(lipgloss.Color(m.theme.Dim)),
		accent: base.Foreground(lipgloss.Color(m.theme.Accent)),
		ok:     base.Foreground(lipgloss.Color(m.theme.OK)),
	}
}

// renderRowPainted lays out one plan row in "Your plans": its own glyph, title
// and counts, and its SOURCE (planItem.sourceLabel).
func (m *ListModel) renderRowPainted(it planItem, isCursor bool) string {
	glyph, tint := it.glyph()
	return m.renderCellsPainted(glyph, tint, it.plan.Title, formatCounts(it.total-it.open, it.total), it.sourceLabel, isCursor)
}

// renderRecentRowPainted draws a Recently opened row: a plan's own glyph,
// title and counts, or -- for a file with no plan -- what a plan with no
// comments draws, and in both the OPENED age where the catalog draws SOURCE.
//
// COMMENTS NOW GOES BLANK FOR TWO DIFFERENT REASONS, one principle behind
// both. A file that is gone draws the ─ a plan whose file is missing draws,
// and counts is set to "" directly right here (case r.gone), because a gone
// file has no plan behind it and so nothing to count. Every row that is not
// itself gone goes through formatCounts instead: the default (a file with no
// plan behind it, formatCounts(0, 0), fixed at zero because there is no plan
// to hold a count) and the rowPlan case (an actual plan, formatCounts(resolved,
// total), which is zero exactly when the plan has no comments). formatCounts
// blanks its cell whenever total is 0, for either reason. All three are the
// same reasoning ("with no file there is nothing to count, not zero")
// applied to whatever is actually missing here -- the file, the plan, or just
// the comments on it.
func (m *ListModel) renderRecentRowPainted(r row, isCursor bool) string {
	glyph, tint, title, counts := '○', tintQuiet, r.text, formatCounts(0, 0)
	switch {
	case r.kind == rowPlan:
		glyph, tint = r.item.glyph()
		title, counts = r.item.plan.Title, formatCounts(r.item.total-r.item.open, r.item.total)
	case r.gone:
		glyph, counts = glyphFileGone, ""
	}
	opened := client.Ago(r.opened, m.now())
	return m.renderCellsPainted(glyph, tint, title, counts, func(width int, _ lipgloss.Style) string {
		return ansi.Truncate(opened, width, "…")
	}, isCursor)
}

// renderCellsPainted lays out one row's cells for the full-canvas painted path,
// which is the only path this model has: the cursor row paints Card background
// behind a Brand ▌ (paintedBand); every other row paints Doc. It takes the cells
// rather than a planItem because Recently opened draws a file with no plan in the
// same columns.
//
// That band was once literally the same style as the document's thread-card left
// bar, until state and position stopped sharing a colour. Accent still marks that
// a comment exists, as the '|' foreground on a card header, while this marks where
// the cursor is and takes Brand, the same colour as the masthead above it.
//
// Every fragment is rendered through some style, never concatenated as a naked
// string, for the same reason renderDocPainted does it: a nested lipgloss.Render
// call ends with a full SGR reset, so an unstyled fragment after one would show
// the terminal's default background instead of the zone's. THAT RULE IS WHY THE
// TITLE AND THE TRAILING REGION PAD THROUGH padRightIn.
//
// THE TITLE AND THE TRAILING REGION ARE FILTERED BEFORE THEY ARE MEASURED. Either
// can carry any byte, neither is parsed or projected, and a bare C0 byte in
// either is zero cells to ansi.Truncate and one to lipgloss -- see
// planItem.sourceLabel for the frame overflow that costs. The title is filtered
// here; trailing filters its own cells, with the style it is handed.
func (m *ListModel) renderCellsPainted(glyph rune, tint glyphTint, title, counts string, trailing func(int, lipgloss.Style) string, isCursor bool) string {
	st := m.styles
	rowWidth := m.rowWidth()
	titleWidth, regionWidth := listColumnWidths(rowWidth)
	z := m.zone(isCursor)

	rowBG := st.DocBG
	if isCursor {
		rowBG = st.Card
	}
	band := m.paintedBand(isCursor, rowBG)

	title = padRightIn(ansi.Truncate(ui.VisibleControls(title, z.text), titleWidth, "…"), titleWidth, z.text)
	counts = padRight(counts, listCountsWidth)
	last := padRightIn(trailing(regionWidth, z.dim), regionWidth, z.dim)
	gap := z.text.Render(strings.Repeat(" ", listGapWidth))
	lastGap := z.text.Render(strings.Repeat(" ", listLastGapWidth))

	content := z.forGlyph(tint).Render(string(glyph)) + z.text.Render(" ") +
		z.text.Render(title) + gap + z.text.Render(counts) + lastGap + z.dim.Render(last)

	line := band + rowBG.Width(rowWidth-listBandWidth).Render(content)
	return m.inset(line)
}

// renderBodyRowPainted is the body's one render dispatcher: a plan row through
// renderRowPainted above, a Recently opened row -- plan or file -- through
// renderRecentRowPainted, and every chrome row as one Doc- (or, under the cursor,
// Card-) painted line carrying its own text, so a band or a hint paints edge to
// edge exactly as a plan row does.
//
// TWO CHROME TREATMENTS AND NOT THREE, in the zone's own colors: a section's NAME
// band takes dim+bold, every other chrome row plain dim. Telling a name band from
// the column-label row beside it is a RENDERER's distinction and not the cursor's,
// so it is a bool on row rather than another rowKind.
func (m *ListModel) renderBodyRowPainted(r row, isCursor bool) string {
	switch {
	case r.kind == rowFile, r.kind == rowPlan && r.section == sectionRecent:
		return m.renderRecentRowPainted(r, isCursor)
	case r.kind == rowPlan:
		return m.renderRowPainted(r.item, isCursor)
	}
	st := m.styles
	rowWidth := m.rowWidth()

	rowBG := st.DocBG
	if isCursor {
		rowBG = st.Card
	}
	band := m.paintedBand(isCursor, rowBG)

	z := m.zone(isCursor)
	textStyle := z.dim
	if r.kind == rowBand && r.head {
		// A SECTION'S NAME AND ITS COLUMN LABELS ARE NOT THE SAME KIND OF LINE,
		// and bold alone was carrying the whole of that: both rows were Dim. The
		// name says what you are looking at, the labels say how to read it, and
		// with two sections on screen the reader needs the first at a glance.
		// Accent is the ink because the client already spends it on a heading a
		// reader is meant to find (theme.PanelHead holds its dark hex for the
		// panel headlines), and it is the one standing role that answers a
		// legible colour in BOTH presets -- gold on dark, mauve on light.
		textStyle = z.accent.Bold(true)
	}
	inner := max(rowWidth-listBandWidth, 0)
	content := textStyle.Render(ansi.Truncate(m.bandRowText(r), inner, "…"))
	return m.inset(band + rowBG.Width(inner).Render(content))
}

// bandRowText is a chrome row's words as they actually reach the screen: a
// section's NAME band indented to the glyph column, every other chrome row's
// text as it was already built. Both then start their words in the column the
// body's own titles use.
//
// THE INDENT IS A CONSTANT AND USED TO BE A MARK. A ▸ sat here naming the
// section the cursor was in. IT WAS REMOVED FOR READING AS AN AFFORDANCE: ▸ is
// the disclosure triangle of every file tree and settings pane, so a band
// wearing it invites an enter that expands something, on a row that is not even
// selectable. The cells it spent are kept, because the alignment was never the
// mark's own business -- it is what puts a band's words over the column it
// heads, and bandTitle's note says what was given up with the glyph.
func (m *ListModel) bandRowText(r row) string {
	if !r.head {
		return r.text
	}
	return strings.Repeat(" ", listGlyphWidth) + r.text
}

// paintedBand is the cursor band on the painted path: the ▌ glyph tinted Brand on
// the ROW's own background, and a trailing space.
//
// A GLYPH, not a filled cell. Two cells of Brand background is twice the ink for
// the same fact and reads as a slab beside the thread rail it is meant to rhyme
// with. Tinting the row's background rather than carrying one of its own is what
// lets it sit on Doc or Card unchanged.
func (m *ListModel) paintedBand(isCursor bool, rowBG lipgloss.Style) string {
	if !isCursor {
		return rowBG.Render(strings.Repeat(" ", listBandWidth))
	}
	// The pad is DERIVED from listBandWidth, not a literal " ": the band's
	// rendered width and the width the row arithmetic reserves for it are the
	// same fact, and writing it twice lets them disagree silently.
	return rowBG.Foreground(m.styles.BrandColor).Render(listCursorGlyph) +
		rowBG.Render(strings.Repeat(" ", max(listBandWidth-1, 0)))
}

// listMargin is the ground either side of the list's canvas, and canvasWidth what
// is left for the list itself. Both come from the DOCUMENT's rail constants rather
// than constants of their own: how far content sits from the terminal's edge is
// one fact about the application, not two, and the two views disagreeing about it
// is visible the moment a reader switches between them.
func (m *ListModel) listMargin() int { return ui.RailWidth(m.width) }

func (m *ListModel) canvasWidth() int { return m.width - 2*m.listMargin() }

// rowWidth is the width of a row's CONTENT, margins excluded -- the whole canvas,
// with no cap of its own.
//
// ⚠️ IT WAS CAPPED AT listMaxRowWidth (120) AND THE CAP IS GONE. What that
// number bought was stated as "title↔metadata association stays
// tight at any terminal width": past 120 cells the row froze, so a reader's eye
// never had to cross more than that between a title and its counts. It froze the
// WRONG thing. NAME took every surplus cell, so what the cap actually pinned was
// the size of the EMPTY GAP inside NAME, while SOURCE stayed clipped at 28 and
// the status bar -- never capped -- showed the whole path one row below.
//
// WHAT REPLACES IT IS listMaxTitleWidth, and it serves the same goal better: NAME
// stops at 68, so the distance between a title and its counts is BOUNDED BY A
// COLUMN rather than by the terminal, and it is tighter at every width past 115
// than the cap ever made it. Everything the row gains beyond that goes to the
// trailing region, whose content -- SOURCE, or Recently opened's OPENED -- is
// LEFT-BOUND in it (renderCellsPainted pads it on the right), so a wider terminal
// lengthens what is shown and never pushes it away from the row it belongs to.
//
// paintedRowSurplus went with the cap. It painted the canvas between a capped row
// and the terminal edge; with the row now the whole canvas there is no such span,
// and a function that can only ever return "" is worse than no function.
func (m *ListModel) rowWidth() int {
	return m.canvasWidth()
}

// inset puts a row between its two margins, and a row is INSET BY THE THING THAT
// BUILDS IT rather than by the view assembling them -- so a painted row is m.width
// wide wherever it is asked for.
//
// EVERY row goes through it, blank filler included: a margin drawn only on rows
// that carry content paints a ragged block above the empty space beneath them.
func (m *ListModel) inset(row string) string {
	w := m.listMargin()
	if w == 0 {
		return row
	}
	// Not a styled render of nothing when w is 0: a style with a background emits
	// its escape even for an empty string. Guarded above.
	pad := m.styles.Rail.Render(strings.Repeat(" ", w))
	return pad + row + pad
}

// msgOpenPlan is emitted by enter in browse mode. Root consumes it to switch into
// the review model on the chosen plan; standalone (no Root in the loop), it is
// inert. It deliberately carries no is-there-a-file fact, and the list no longer
// derives one at all: any such fact is a derivation-time snapshot that can be stale
// in either direction by the time the open cmd runs, so Root's openPlanCmd always
// tries the file first and falls back to the registered snapshot itself.
type msgOpenPlan struct {
	plan domain.Plan
}

// msgOpenFile is enter on a Recently opened file row: a file this
// machine remembers looking at with no plan behind it, opened exactly as
// `draftplane review <path>` would -- session.Open, not session.OpenPlan,
// since there is no domain.Plan to hand it.
type msgOpenFile struct {
	path string
}

// msgListRefreshed carries refreshCmd's derived plan set back to the loop. seq
// is the dispatch-order sequence number refreshCmd stamped it with -- see
// ListModel.refreshSeq.
//
// recents is Recently opened's answer, read in the same goroutine as the plans:
// recent.json, its files checked in their batch (loadRecents, recentEntries).
// Recently opened is a join over the plans, so the two land in one message
// and one redraw: a plan deleted elsewhere leaves both of its rows at once.
type msgListRefreshed struct {
	seq     uint64
	items   []planItem
	err     error
	recents recentsAnswer
}

// msgListActionDone carries a rename/delete write's result back to the loop, the
// list's counterpart to the review model's msgActionDone.
type msgListActionDone struct {
	status string
	err    error
}

// ListModel is draftplane's plan list: browse, rename, and delete plans. See
// NewList.
type ListModel struct {
	svc    client.PlanService
	km     keymap.Map
	theme  *theme.Theme
	styles *ui.Styles

	// files is the check every load runs for planItem.fileMissing and
	// Recently opened's files (recentEntry.gone), read into each load's
	// closure beside svc. It is listFileCheck, the process's one, unless a test
	// gives the model a check whose budget never fires, so that what the test
	// sees depends on the file alone and never on the clock.
	files *fileCheck

	// recentPath is where this machine's recent.json lives, set once by
	// SetRecents. Unset, the list draws no Recently opened section, which is every
	// test that never calls it.
	recentPath string

	// recents is recent.json as the last load read it, newest first. It arrives
	// on msgListRefreshed beside the plans (takeRecents), read off the loop.
	recents []recentEntry

	// now is Recently opened's clock for how long ago each row was opened:
	// time.Now, and pinned in tests.
	now func() time.Time

	// items is the plan set, freshest first; rows is the body, derived from items by
	// buildRows -- one line per element only in a panel mode, since browse mode's own
	// bands, hints and +N more rows are rows too. The cursor and scroll both index
	// ROWS, while everything that counts plans reads items. applyRefresh is the only
	// writer of items and rebuilds rows in the same breath, so the two cannot drift.
	items  []planItem
	rows   []row
	cursor int
	scroll int
	width  int
	height int

	mode    listMode
	ta      textarea.Model
	confirm string

	// filter is the query, exactly as the human typed it -- raw, so the help bar can
	// echo the characters on the keyboard (filterQuery is the folded, trimmed form
	// every match is made against).
	//
	// IT OUTLIVES listFilter, which is why it is a field on the model rather than
	// state owned by that mode: enter leaves the mode and KEEPS the query, so the
	// reader can navigate, open, rename or delete inside a narrowed list. esc is what
	// clears it, from either side.
	//
	// NOTHING A REFRESH DOES TOUCHES IT. applyRefresh replaces m.items and the filter
	// is re-applied to whatever arrived -- the query is about the reader's intent,
	// not about a particular load.
	filter string

	// sortOrder is the ordering of "Your plans", and its ZERO VALUE IS THE DEFAULT
	// rather than something NewList has to install: sortOrder's own zero is
	// {sortUpdated, sortDescending}, which is the order deriveListItems already
	// produces.
	//
	// NOT PERSISTED -- see sortOrder's own comment for why.
	sortOrder sortOrder

	// sortCursor is which option row the OPEN modal's own cursor is on, an index
	// into sortColumns. It is NOT the chosen column -- nothing is chosen until
	// enter -- which is why the arrow and this are two separate marks in the box.
	sortCursor int

	// renameTarget/deleteTarget are captured at panel-entry time (mirroring the
	// review model's relocateTID pattern): the plan a pending rename/delete commit
	// applies to, looked up by ID rather than by cursor index so a list re-render
	// between opening the panel and committing can't silently retarget the write.
	renameTarget domain.PlanID
	deleteTarget domain.PlanID

	// deleteTargetHint is deleteTarget's plan's SourceHint, captured the same way
	// and for the same reason: a deleted plan's forget from recent.json needs
	// its path along with its id -- an entry recorded by path alone (a file opened
	// before it became a plan) would otherwise survive the plan it names.
	deleteTargetHint string

	status string

	// flagStatus is the status sentence a STATE FLAG put on the bar, kept so the line
	// that clears that flag can retract exactly that sentence. The one such sentence
	// is a failed load's (Update's msgListRefreshed arm), and the next load that
	// lands is its clearer.
	//
	// REMEMBERING THE SENTENCE IS WHAT MAKES THE RETRACTION SAFE, and is why this is
	// a field rather than an m.status = "" at the clearer: m.status is one channel
	// with many writers, and a clearer that blanked the bar would delete "plan
	// deleted" before anyone read it. clearFlagStatus retracts only a sentence still
	// equal to the one a flag wrote.
	flagStatus string

	err error

	// inFlight is true while a rename/delete write cmd is running off the
	// input path — same serialization invariant as the review model's field
	// of the same name: only one write in flight at a time, everything else
	// refused until it resolves.
	inFlight bool

	// pendingRefresh is true when msgStateChanged arrived while a panel was
	// open or a write was in flight; consumed at the next safe
	// return-to-browse seam (maybeApplyPendingRefresh).
	pendingRefresh bool

	// awaiting is whether the first load has yet to answer. Init
	// sets it; the load's answer OF ANY KIND -- the plans, or an error -- clears
	// it, and nothing sets it again: a later refresh keeps its rows on screen, as
	// it always has.
	//
	// TWO READERS DRAW FROM IT. emptyHint draws an awaiting "Your plans" as one
	// blank row, because every empty-state hint is a claim about an answer that has
	// not landed: "No plans yet!" over plans that were a moment away was the
	// defect. statusBarText leaves the counts off while it waits, because "0 plans"
	// is the same false thing.
	//
	// SET IN Init AND NOT IN NewList, deliberately: a model a test builds and seeds
	// directly never runs Init, is not awaiting, and draws its hints as before.
	awaiting bool

	// cursorPinned holds the cursor on the first selectable row through every
	// rebuild from Init until the first key, click or wheel, so
	// launching and pressing enter reopens the last thing opened, whatever a
	// load landing before that first input does to the rows. Init sets it only
	// for a list pointed at a recent.json, since the pin exists for that section
	// alone: without one, and in a model nobody Inits, the cursor follows
	// identity through the first load as it always has.
	cursorPinned bool

	// heldPlan and heldSection are the copy the cursor was on the last time a
	// rebuild left a body that draws Recently opened (rowsDrewRecent says m.rows
	// is one), the only body in which a plan is two rows. The rename
	// panel's and the filter's bodies draw each plan once, so a cursor coming
	// back from one is on the catalog copy even when it went in on Recently
	// opened's; rebuildRows asks the held pair instead. (Expanded draws each
	// plan once too, but is entered from a +N more row, which holds no plan, so
	// nothing is held into it.) The section is held WITH its plan because it
	// says nothing about any other: a cursor moved, or snapped by a narrowing
	// filter, onto another plan comes back on that plan's own copy. heldPlan is
	// empty when the cursor left on no plan.
	heldPlan       domain.PlanID
	heldSection    listSection
	rowsDrewRecent bool

	// refreshSeq/appliedRefreshSeq guard against refreshCmd's own I/O running off the
	// loop: msgStateChanged and handleListActionDone can each independently dispatch
	// a fresh refreshCmd while an earlier one is still outstanding, and bubbletea
	// gives no ordering guarantee on which result lands first. refreshCmd stamps
	// every dispatch with the next refreshSeq; msgListRefreshed's handler drops any
	// result whose seq is older than the newest already applied.
	refreshSeq        uint64
	appliedRefreshSeq uint64
}

// NewList builds a ListModel. A NIL th IS THE DEFAULT THEME, NOT AN UNPAINTED
// RENDERING -- New's contract for the review model, mirrored exactly: the
// full-canvas painted path is the only one left, so a nil chooses a palette and
// nothing else.
func NewList(svc client.PlanService, km keymap.Map, th *theme.Theme) *ListModel {
	th = themeOrDefault(th)
	st := ui.NewStyles(th)

	ta := textarea.New()
	ta.SetHeight(1)
	ta.Placeholder = "plan title…"
	ta.SetStyles(paintedTextareaStyles(st))

	m := &ListModel{
		svc:    svc,
		files:  listFileCheck,
		km:     km,
		theme:  th,
		styles: st,
		ta:     ta,
		now:    time.Now,
		width:  100,
		height: 30,
	}
	// The section exists before the first load does. Init's load runs off the loop,
	// so the very first frame is drawn with no items at all -- and the chrome is
	// invariant, so that frame must still show the band. It used to show the
	// empty-state hint under it as well, which followed from the chrome rather than
	// being required by it, and said "No plans yet!" while the plans were still on
	// the way. Init now marks the list awaiting before that frame, so the section
	// holds its slot with one blank row instead (emptyHint); a model nobody Inits is
	// not awaiting and draws the hint built here. A failed first load brings the hint
	// back: it is still an answer and ends the wait like any other, but it applies no
	// rows.
	m.rebuildRows()
	return m
}

// SetRecents points Recently opened at this machine's recent.json. Unset, the
// list draws no such section, which is every test that never calls it. A
// setter rather than a NewList parameter, since Root is its one caller.
func (m *ListModel) SetRecents(path string) { m.recentPath = path }

// Init marks the list awaiting its first load and, when there is a recent.json to
// read, pins the cursor to the top row until the first input; then it
// dispatches that load. Like the review model's Init, it returns only
// message-producing cmds -- every service call runs off the loop, in a cmd's own
// goroutine, never inline in Update.
//
// THE ROWS ARE REBUILT HERE because NewList built them before anything was
// awaiting, and the first frame is drawn from them after this returns.
func (m *ListModel) Init() tea.Cmd {
	m.awaiting = true
	m.cursorPinned = m.recentPath != ""
	m.rebuildRows()
	return m.refreshCmd()
}

// refreshCmd loads the plans and Recently opened off the loop. svc and the
// dispatch's own sequence number are read once here, into locals, before the
// goroutine runs -- the goroutine must never touch ListModel state directly,
// only the values it captured. The seq is stamped at dispatch time, not when the
// goroutine finishes, so it reflects dispatch order even though completion order
// can differ.
//
// ONE ListPlans IS THE WHOLE LOAD, and Recently opened's files join the plans'
// batch before its wait, so one budget covers both (listFileCheckBudget). A
// failed list never waits on the batch, and the wait below is then Recently
// opened's alone.
//
// THE CONTEXT BELOW IS BARE, deliberately: ListPlans is a read, and nothing it
// reaches reads client.AttributionFrom.
func (m *ListModel) refreshCmd() tea.Cmd {
	m.refreshSeq++
	seq := m.refreshSeq
	svc, files, recentPath := m.svc, m.files, m.recentPath
	return func() tea.Msg {
		ctx := context.Background()
		batch := files.batch()
		saved, read := loadRecents(recentPath, batch)
		items, err := deriveListItems(ctx, svc, batch)
		recents := recentsAnswer{entries: recentEntries(saved, batch.wait()), read: read}
		return msgListRefreshed{seq: seq, items: items, err: err, recents: recents}
	}
}

// cursorRow answers the row under the cursor, of any kind — selectedPlan's
// weaker sibling, for the gestures that act on a row that is not a plan (enter
// on +N more, and d and enter on a Recently opened file). selectedPlan remains
// the ONLY way a gesture reaches a PLAN; this reaches a row.
func (m *ListModel) cursorRow() (row, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return row{}, false
	}
	return m.rows[m.cursor], true
}

// deriveListItems reads every plan's list-row facts off the projection ListPlans
// already carries -- domain.Plan's OpenThreads/TotalThreads/TipApproved/
// LastActivityAt -- instead of fanning out to Threads/Versions/Approvals per plan.
// ONE service call regardless of how many plans come back: a refresh reloads
// state.json under flock once, not 1 + 3N times. This function trusts the place
// that computes those four values rather than recomputing them.
//
// ONE STAT PER PLAN WHOSE FILE IS ON THIS MACHINE, AND NO OTHER: its row loop
// (itemsFromPlans) used to os.Stat every plan's SourceHint to fill a field with no
// production reader, and that stat was removed: at 500 plans that was 500
// syscalls on a path that runs on every state change. A stat now comes back
// WITH a reader, fileMissing's ─, and only for a plan whose SourceHint names a
// file (planFile). The stats run concurrently, before the rows are built, under
// one listFileCheckBudget for the whole load; batch is the load's, which a refresh
// has already given Recently opened's files.
func deriveListItems(ctx context.Context, svc client.PlanService, batch *fileBatch) ([]planItem, error) {
	plans, err := svc.ListPlans(ctx)
	if err != nil {
		return nil, err
	}
	items := itemsFromPlans(plans, batch.missing(plans))
	sortItems(items)
	return items, nil
}

// itemsFromPlans is deriveListItems' row derivation: one planItem per plan, in
// the order given. missing is taken rather than checked here, because a stat must
// not run on the loop: each load checks in its own goroutine and hands the
// answer in.
func itemsFromPlans(plans []domain.Plan, missing map[domain.PlanID]bool) []planItem {
	items := make([]planItem, 0, len(plans))
	for _, p := range plans {
		items = append(items, planItem{
			plan:         p,
			open:         p.OpenThreads,
			total:        p.TotalThreads,
			approvedTip:  p.TipApproved,
			lastActivity: p.LastActivityAt,
			created:      p.CreatedAt,
			fileMissing:  missing[p.ID],
		})
	}
	return items
}

// sortColumn names the plan list's orderings: three of them. AUTHOR IS NOT ONE, and
// a COMMENTS-COUNT column was proposed and rejected outright.
type sortColumn int

const (
	sortUpdated sortColumn = iota
	sortCreated
	sortName
)

// sortDirection is which way a column runs, and DESCENDING IS THE ZERO VALUE
// deliberately: sortOrder's zero is therefore {sortUpdated, sortDescending}, which
// is exactly the order deriveListItems already produces. A ListModel built any way
// at all starts on the list's existing order without anyone having to remember to
// initialise a field.
type sortDirection int

const (
	sortDescending sortDirection = iota
	sortAscending
)

// sortOrder is "Your plans"' ordering, and it is NOT PERSISTED: nothing on
// this path writes client/config, which would be taking its first write from a
// keystroke handler on a path where a failure has no useful remedy. A restart
// opens on the zero value.
type sortOrder struct {
	column    sortColumn
	direction sortDirection
}

// naturalDirection is the direction a column takes when it is CHOSEN, as
// against flipped: the two time columns run newest-first because recency is
// what a reader scanning either of them is after, and a name runs A-Z because
// that is what an alphabetical list means. enter on a column that is already
// active flips instead -- see updateSort, where the arrow's two jobs are.
func naturalDirection(c sortColumn) sortDirection {
	if c == sortName {
		return sortAscending
	}
	return sortDescending
}

// label is the column's own word, read by the modal's option row.
func (c sortColumn) label() string {
	switch c {
	case sortCreated:
		return listSortCreatedLabel
	case sortName:
		return listSortNameLabel
	}
	return listSortUpdatedLabel
}

// arrow is the ONE MARKER this box asks for: it names the active column AND its
// direction in a single glyph, which is what removes the need for a separate
// selection column beside the options.
func (d sortDirection) arrow() string {
	if d == sortAscending {
		return listSortAscendingArrow
	}
	return listSortDescendingArrow
}

// sortSectionItems returns items in ord's order.
//
// IT SORTS IN PLACE, and its one caller is what makes that safe: sortedItems hands
// it a copy, never m.items itself (filteredItems returns m.items unmodified when no
// filter is on, so a sort reaching that slice would REORDER THE LOADED SET --
// exactly what "apply on enter" must not do).
func sortSectionItems(out []planItem, ord sortOrder) []planItem {
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		// ONE COMPARATOR PER COLUMN AND THE DIRECTION IS APPLIED TO ITS SIGN, rather
		// than a second comparator (or the same one with its arguments swapped):
		// swapping the arguments would reverse the TIE-BREAK along with the key, so
		// flipping the arrow would reorder rows whose sort key never differed at all.
		if cmp := compareByColumn(ord.column, a, b); cmp != 0 {
			if ord.direction == sortAscending {
				return cmp > 0
			}
			return cmp < 0
		}
		// The tie-break is Title ascending in BOTH directions and in every column,
		// exactly as sortItems' own is: without one, two plans with the same instant
		// would keep whatever order the load happened to deliver, and a re-sort that
		// answers differently on identical input is a list that shuffles under a
		// keystroke that changed nothing. What reaches it is two plans genuinely
		// created (or touched) at the same moment, and two plans with no instant at
		// all.
		return a.plan.Title < b.plan.Title
	})
	return out
}

// compareByColumn orders a against b on one column, DESCENDING-FIRST: negative
// when a comes first under a descending order, positive when b does, zero when the
// column cannot tell them apart. One orientation for every column is what lets
// sortSectionItems apply the direction to a sign rather than branch per column.
func compareByColumn(c sortColumn, a, b planItem) int {
	switch c {
	case sortCreated:
		return compareDescendingTime(a.created, b.created)
	case sortName:
		return strings.Compare(b.plan.Title, a.plan.Title)
	}
	return compareDescendingTime(a.lastActivity, b.lastActivity)
}

// compareDescendingTime is compareByColumn's time arm for both of its time columns,
// so the zero value is handled identically by each: it sorts LAST descending and
// FIRST ascending, because a zero is before every real instant. A zero means the
// plan has no such fact at all: nothing has happened on it yet for lastActivity,
// and no registered version for created.
func compareDescendingTime(a, b time.Time) int {
	switch {
	case a.After(b):
		return -1
	case b.After(a):
		return 1
	}
	return 0
}

// sortItems is the list's DERIVATION-TIME ordering -- what deriveListItems produces:
// lastActivity descending, ties broken by Title ascending via SliceStable.
//
// IT IS NOT THE SORT MODAL'S, and the two are deliberately separate functions. This
// one keeps m.items in the one order every other reader of that slice assumes; the
// modal's order is applied further down (sortedItems), so a re-sort is a fact about
// the BODY and never a mutation of the loaded set.
func sortItems(items []planItem) {
	sort.SliceStable(items, func(i, j int) bool {
		li, lj := items[i].lastActivity, items[j].lastActivity
		if !li.Equal(lj) {
			return li.After(lj)
		}
		return items[i].plan.Title < items[j].plan.Title
	})
}

// mastheadHeight is the top block's row count for the mode being drawn, and the
// question it branches on is isPanelMode rather than "is this browse": rename
// alone keeps its plain masthead line and column-header row (listMastheadHeight,
// unconditionally 4 -- see that constant's own note on why rename does not draw
// the box); every other mode draws the masthead box (listBrowseMastheadHeight).
//
// Either way it is reserved unconditionally, including for an empty list. A
// conditional reservation would make the layout jump on the 0->1-plan transition
// under live refresh: msgStateChanged landing as the first plan appears would
// suddenly steal rows from the body the user was just looking at.
func (m *ListModel) mastheadHeight() int {
	if m.isPanelMode() {
		return listMastheadHeight
	}
	return listBrowseMastheadHeight
}

func (m *ListModel) viewHeight() int {
	h := m.height - 2 - m.mastheadHeight()
	if m.mode == listRename {
		h -= m.ta.Height() + 2
	}
	if h < 1 {
		h = 1
	}
	return h
}

// confirmLines is confirmGroups' rows in order -- the shape every caller that only
// DRAWS the panel wants. The wrap, the budget and the truncation are one level
// down, on confirmGroups, which has a caller of its own (centredGroups) that wants
// the groups and not the flattened rows.
func (m *ListModel) confirmLines() []string {
	return flattenConfirmGroups(m.confirmGroups())
}

// confirmGroups wraps m.confirm to the panel's width via real word-wrap (inserting
// line breaks, never truncating within a line), mirroring the review model's
// identical Model.confirmGroups, and then truncates (truncateConfirmGroups) to
// confirmBudget(), because a body is not bounded by the terminal: the info
// panel's paths and a keys panel at a short terminal both outgrow the box, and
// word-wrap alone is not sufficient there.
//
// ansi.Wordwrap treats only spaces and "-" as breakpoints, so a single unbreakable
// token wider than width comes out still over width; ansi.Hardwrap is the second
// pass that forces a break. It is safe over the WHOLE already-wrapped string in one
// call, not per line: it treats every existing "\n" as its own forced break. Without
// it, a Width().Render downstream hard-wraps the over-wide line itself and paints
// more rows than the panel reserved.
//
// m.width <= 12 WAS a residual and is now UNREACHABLE. The arithmetic is unchanged:
// row 0 carries a 3-cell "── " prefix on top of whatever this returns, so at the
// width floor (10, engaged below width 14) row 0 can render 13 cells against a
// narrower m.width -- measured at width 12, the strip reserved 18 rows and the
// strip drew 19. What changed is that listMinWidth = 31 refuses the whole screen
// first. The remaining sibling is the m.height <= 7 floor confirmBudget names, which
// listMinHeight likewise puts out of reach.
//
// A CENTRED MODE TAKES centredInterior AND NOT max(m.width-4, 10): the box spends
// three cells a side, so m.width-6 is the interior a full-width box has where
// m.width-4 is what the bottom strip wants. centredInterior is one function shared
// by both models rather than this expression written twice.
//
// EVERY MODE THIS METHOD IS EVER ASKED ABOUT TAKES THE CENTRED BRANCH, so nothing
// that ships still asks for the width-4 arm above. It stays rather than collapsing
// to one branch, for the same reason the review model's own centred branch is a
// BRANCH: one accessor feeding one truncator is what keeps a single mode's body from
// becoming two panels that can disagree.
func (m *ListModel) confirmGroups() [][]string {
	width := max(m.width-4, 10)
	if m.drawsCentredPanel() {
		width = centredInterior(m.width)
	}
	// THE CONTROL-BYTE FILTER IS IN FRONT OF THE WRAP, for the same reason every
	// other budget in this file takes it first: this panel's whole contract is that
	// no line it returns exceeds width DISPLAY CELLS, and a bare C0 byte is zero cells
	// to ansi.Wordwrap and one to the Width().Render that draws the row -- so a
	// confirm naming a plan whose title carries one painted a row more than its box
	// reserved. It covers EVERY writer of m.confirm at once, which
	// is the placement this argues for -- at the DRAW, not at the field. It is spent
	// inside wrapConfirmGroups, one line at a time rather than once over the whole
	// body, for the lipgloss block-padding reason that function measures.
	//
	// AND IT IS WHY THE DELETE TEXT NO LONGER %q's ITS TITLE. That verb was read once
	// as an "accidental defence" this panel depended on; it is not, because the filter
	// covers the whole string. What it actually did was spell a control byte as a Go
	// escape where every other surface draws a Control Picture. Every surface uses
	// one spelling now.
	return truncateConfirmGroups(wrapConfirmGroups(m.confirm, width, m.styles.FocusHeader), m.confirmBudget())
}

// confirmBudget is the most LINES confirmLines() may return without letting the
// alt-screen total exceed m.height. In a PANEL mode -- the only mode this method is
// asked in -- viewPainted is exactly 3 masthead + vh + H + 2 status/help by
// construction, H being a strip panel's own height (its lines plus one spacer
// row). viewHeight's own "if h < 1" floor stops vh from going negative, but
// nothing stopped H from growing without bound, so a long enough body silently
// pushed the total past m.height while every individual reservation still looked
// internally consistent.
//
// Solving total <= m.height for H with vh's floor folded in: while H <= m.height-6
// the total comes out to exactly m.height; past that point vh floors to 1 and the
// total becomes H+6. So the invariant is "H must never exceed m.height-7" --
// m.height-8 for the LINE count this returns, since H adds the spacer row back on
// top. minConfirmBudget (1) is a
// floor so a very short terminal still shows SOMETHING; the total can still exceed
// m.height below roughly 8 rows, the same geometric floor listRename's own textarea
// reservation has never guaranteed against either.
func (m *ListModel) confirmBudget() int {
	if m.drawsCentredPanel() {
		// THIS MODEL'S OWN CHROME, DERIVED FRESH -- not the review model's m.height-4
		// borrowed, even though the two numbers land the same. drawsCentredPanel()
		// true means isPanelMode() is false too (isPanelMode is exactly `m.mode ==
		// listRename`, which no centred member is), so mastheadHeight() falls back to
		// its BROWSE form and viewHeight() subtracts nothing further: mastheadHeight
		// (3) + viewHeight (m.height-2-3) + panelViewPainted (0) + 2 comes to exactly
		// m.height, the SAME total browse mode has. A centred mode therefore has the
		// WHOLE frame to fit its box in: centredBox draws a border row and a blank row
		// above the lines and the same two below, so len(lines)+4 <= m.height, solved
		// for the LINE count as m.height-4.
		//
		// THE REVIEW MODEL'S CENTRED BRANCH SOLVES TO THE IDENTICAL NUMBER, and that is
		// a coincidence of the two invariants agreeing rather than a borrowed figure:
		// this model's masthead and status/help chrome are entirely different numbers,
		// and they cancel out only because BOTH models hold "a centred mode's frame is
		// exactly m.height rows". Solve this branch against the PANEL-MODE masthead or
		// against the non-centred m.height-8 below and the box is sized for a
		// reservation that no longer exists.
		if b := m.height - 4; b >= minConfirmBudget {
			return b
		}
		return minConfirmBudget
	}
	// m.height-8, not -7: the top ground row took panel-mode chrome from five rows to
	// six, and this budget is solved against that chrome. Left at -7 the panel is one
	// row too generous and pushes the help bar off the altscreen.
	if b := m.height - 8; b >= minConfirmBudget {
		return b
	}
	return minConfirmBudget
}

// minConfirmBudget is confirmBudget's floor -- see that method for why 1 (the
// question alone; a strip still adds its spacer row on top).
const minConfirmBudget = 1

// wrapConfirmGroups is the wrap BOTH confirmLines methods run, and it answers ONE
// GROUP OF SCREEN ROWS PER LOGICAL LINE rather than a flat list of rows. That
// grouping is what lets truncateConfirmLines keep a line whole or drop it: by the
// time a []string reaches a truncator, a break between two lines and a break inside
// one wrapped line look identical, and the truncator cuts wherever the budget runs
// out. THE DEFECT IT CLOSES was read off a 30x11 terminal: the headline came out cut
// mid-sentence and a key line survived with the row that named the KEY dropped.
//
// THE TWO PASSES ARE ansi.Wordwrap for real word breaks, then ansi.Hardwrap for the
// single unbreakable token Wordwrap declines to split, run once PER LOGICAL LINE
// rather than once over the body. Both treat an existing "\n" as a forced break, so
// the split changes no row.
//
// THE CONTROL-BYTE FILTER IS INSIDE THE SPLIT AND STILL IN FRONT OF THE WRAP:
// lipgloss.Render BLOCK-PADS a multi-line string -- every line out to the longest
// one's width -- so filtering the whole body at once handed the wrap a rectangle of
// trailing spaces, which at narrow widths wrapped into rows of nothing but blanks
// (measured at width 4: 38 rows against the 24 the same text needs). Filtering each
// line separately hands lipgloss one line and there is nothing to pad.
//
// pathRowMarker IS GLUED TO ITS PATH BEFORE EITHER PASS SEES THE LINE. The marker's
// ">  " is two ASCII spaces, precisely the byte ansi.Wordwrap treats as an ordinary word
// break, so it read ">" and the path as two words and stranded the marker alone on
// its own row (measured at interior 72: from a break-free path of 70 cells up).
// RULED: ">  path" is one unbreakable unit. THE GLUE IS A NON-BREAKING SPACE
// (U+00A0), because ansi.Wordwrap's own vendored wordwrap() tests `r != nbsp` before
// treating a rune as a break, AND IT IS REVERSED ONCE THE ROW IS CUT so the pinned
// body's exact bytes are the ones a reader copies off the panel. THE FIX LIVES HERE,
// one level above the composers that write a path row, because
// this is the one place every one of their bodies is wrapped, and nothing else
// opens a logical line with ">  ".
//
// THE SAME HYPHEN RULE IS APPLIED HERE TOO, and it is a SECOND IMPLEMENTATION of the
// same rule rather than a call into ui.wrapPlain -- see wrapConfirmLine for why.
// Without it the two-pass shape above reproduces the rejected alternative whenever
// ansi.Wordwrap's own hyphen-arm bug fires: the row comes out one cell over width
// ending in a lone "-", and Hardwrap plants the newline immediately behind that
// hyphen rather than moving it. Reachable on this file's own pinned text.
func wrapConfirmGroups(body string, width int, style lipgloss.Style) [][]string {
	logical := strings.Split(body, "\n")
	groups := make([][]string, 0, len(logical))
	for _, line := range logical {
		glued, n := gluePathRowMarker(line)
		wrapped := wrapConfirmLine(ui.VisibleControls(glued, style), width)
		hard := ansi.Hardwrap(wrapped, width, true)
		if n > 0 {
			// ONLY THE FIRST n GLUE RUNES COME BACK, not every U+00A0 on the row:
			// gluePathRowMarker wrote exactly n, immediately after whichever marker
			// matched, so they are always the row's first n occurrences. ReplaceAll
			// over-reaches -- a path can carry its own U+00A0 (legal on macOS and
			// Linux, routinely pasted from a browser, and reachable from an MCP save
			// source hint), and rewriting that byte too showed the same character two
			// ways on one panel. EACH OF THE n STILL COMES BACK ON ITS OWN, not as the
			// adjacent run: at a width so narrow Hardwrap must split between them
			// (unreachable at any interior this product hands the function, but cheap
			// to make true unconditionally), a count of n still restores every one.
			hard = strings.Replace(hard, pathRowGlueRune, " ", n)
		}
		groups = append(groups, strings.Split(hard, "\n"))
	}
	return groups
}

// panelHyphenSentinel is wrapConfirmLine's own placeholder for a '-' it has hidden
// from ansi.Wordwrap's hyphen-arm bug -- see that function for the defect and
// protectPanelHyphens for why this package cannot reuse ui.hyphenSentinel's
// tokenizer even though the two exist for the identical reason.
//
// A DIFFERENT CODE POINT FROM ui.hyphenSentinel, DELIBERATELY, even though the two
// packages never combine a string a runtime could confuse them within: this one
// sits beside pathRowGlueRune, whose count-exact reversal in wrapConfirmGroups is
// load-bearing. A reader auditing "does this collide with either rune that reversal
// depends on" should find two visibly different Private Use Area constants rather
// than having to prove two identically-valued constants in two files are never
// handed to the same strings.Replace call. wrapConfirmLine checks for its PRESENCE
// rather than assuming its absence.
const panelHyphenSentinel = ''

// protectPanelHyphens is protectHyphens' (ui/render.go) own reasoning -- hide a '-'
// from ansi.Wordwrap inside any whitespace-delimited token whose own rendered width
// already fits, so Wordwrap's ordinary word-buffer logic carries the WHOLE token
// down instead of taking its unchecked hyphen arm -- but it CANNOT BE THAT FUNCTION
// CALLED FROM HERE, because this package's own lines can open with
// gluePathRowMarker's U+00A0 and ui.protectHyphens ends a token on ANY
// unicode.IsSpace rune. unicode.IsSpace(nbsp) answers true in Go, but
// ansi.Wordwrap's vendored wordwrap() does NOT treat it as a break, so an NBSP
// merges into whatever word Wordwrap is already building. A tokenizer that ended a
// token at NBSP would measure ">" and the glued path as two SEPARATE pieces where
// Wordwrap treats them as ONE -- and protecting a hyphen because the narrower,
// wrong-sized piece "fits" when the wider, Wordwrap-accurate one does not is exactly
// the corruption this mechanism exists to prevent. The boundary predicate below is
// Wordwrap's own: unicode.IsSpace(r) && r is not NBSP.
func protectPanelHyphens(s string, limit int) string {
	var b strings.Builder
	b.Grow(len(s))
	flushFrom := 0
	sawFirst, prevBreak := false, false
	isBreak := func(r rune) bool { return unicode.IsSpace(r) && r != ' ' }
	flush := func(end int) {
		tok := s[flushFrom:end]
		if strings.ContainsRune(tok, '-') && ansi.StringWidth(tok) <= limit {
			tok = strings.ReplaceAll(tok, "-", string(panelHyphenSentinel))
		}
		b.WriteString(tok)
	}
	for i, r := range s {
		br := isBreak(r)
		switch {
		case !sawFirst:
			sawFirst = true
		case br != prevBreak:
			flush(i)
			flushFrom = i
		}
		prevBreak = br
	}
	flush(len(s))
	return b.String()
}

// protectPanelHyphenAt is protectHyphenAt's (ui/render.go) own algorithm, PORTED
// rather than shared: it counts literal '-' runes by POSITION alone, left to right,
// never by token, so it has nothing protectPanelHyphens' NBSP-awareness needs to
// duplicate. Returns protected unchanged if ordinal names nothing.
func protectPanelHyphenAt(protected string, ordinal int) string {
	if ordinal < 1 {
		return protected
	}
	var b strings.Builder
	b.Grow(len(protected))
	seen := 0
	for _, r := range protected {
		if r == '-' {
			seen++
			if seen == ordinal {
				b.WriteRune(panelHyphenSentinel)
				continue
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// wrapConfirmLine is one already-glued, already-filtered logical line's own
// ansi.Wordwrap call, run through the same SHAPE ui.wrapPlain runs over
// a document paragraph: protect every hyphen whose own token already fits, wrap, and
// if a row still overflows AND (once ansi.Strip'd) ends in a real '-', protect
// exactly THAT hyphen and wrap the WHOLE line again rather than patch the
// already-split rows by hand. BOUNDED the identical way: each retry protects one
// more real '-' than the last, and a line has finitely many.
//
// "THE SAME SHAPE," NOT "IDENTICAL": the two loops make ONE DIFFERENT DECISION.
// ui.wrapPlain's retry loop rejects a retry that grows the worst row; this one
// accepts every retry unconditionally, because THIS function has ansi.Hardwrap
// running behind it and ui.wrapPlain has no such floor -- see the retry loop's own
// comment for the mutation evidence that rejecting here actively orphans a hyphen on
// real pinned text.
//
// NOT A CALL INTO ui.wrapPlain, AND THIS IS DELIBERATE: the retry-acceptance
// divergence above; this function is handed ONE caller-glued line and returns the
// still-Wordwrapped string for wrapConfirmGroups' second pass, where ui.wrapPlain
// owns both; and, more load-bearing than either, protectPanelHyphens' NBSP exemption
// has no document-path analogue, so one shared function would mean the document path
// carrying a branch that exists only for this package's marker glue.
//
// THE SENTINEL IS ASSERTED ABSENT, NOT ASSUMED: a caller-supplied path is foreign
// text exactly as a document's prose is, so if filtered already carries U+E001,
// protecting hyphens would swap some of THEIR characters for the placeholder this
// function turns back into a plain hyphen. Falls back to today's ansi.Wordwrap,
// hyphen bug included, rather than risk that. THE SUFFIX CHECK IS TAKEN AFTER
// ansi.Strip, because ui.VisibleControls can hand back real SGR and an escape
// sequence right after a hyphen must never be mistaken for the reader's own text.
func wrapConfirmLine(filtered string, width int) string {
	if strings.ContainsRune(filtered, panelHyphenSentinel) {
		return ansi.Wordwrap(filtered, width, "")
	}
	protected := protectPanelHyphens(filtered, width)
	for {
		wrapped := ansi.Wordwrap(protected, width, "")
		lines := strings.Split(wrapped, "\n")
		bad := firstOverWidthRow(lines, width)
		if bad < 0 {
			return unprotectPanelHyphens(wrapped)
		}
		prefix := ansi.Strip(strings.Join(lines[:bad+1], "\n"))
		if !strings.HasSuffix(prefix, "-") {
			// Over budget with no hyphen at the end is not this function's bug to fix --
			// the pre-existing "single unbreakable token" class ansi.Hardwrap handles
			// it, exactly as it always has.
			return unprotectPanelHyphens(wrapped)
		}
		ordinal := strings.Count(prefix, "-")
		next := protectPanelHyphenAt(protected, ordinal)
		if next == protected {
			// Cannot happen given the HasSuffix check above -- kept as a hard stop
			// rather than an assumption, so a future change to either function fails
			// loudly instead of spinning forever.
			return unprotectPanelHyphens(wrapped)
		}

		// protectPanelHyphenAt EXTENDS PROTECTION UNCONDITIONALLY, WITH NO FIT CHECK OF
		// ITS OWN, where ui.wrapPlain's retry loop rejects a retry that grows the first
		// over-width row. THIS LOOP DOES NOT, AND THAT IS DELIBERATE: unlike
		// ui.wrapPlain, this function has ansi.Hardwrap running behind it
		// unconditionally, so an intermediate row growing here can never reach the
		// reader over budget.
		//
		// A WIDTH-GROWTH GUARD WAS TRIED HERE AND REVERTED, PROVEN WRONG BY MUTATION:
		// rejecting a retry that measures worse by ansi.Wordwrap's own row width --
		// ui.wrapPlain's exact rule -- reddened
		// TestWrapConfirmGroupsNeverOrphansAHyphenAcrossWidths on TWO real, pinned
		// fixtures, because rejecting left a real hyphen for Hardwrap's blind per-width
		// cut to land on. ansi.Wordwrap's row width and this path's actual output
		// quality are UNCORRELATED here, since Hardwrap does not consult word or hyphen
		// boundaries at all -- so the check ui.wrapPlain needs is not merely
		// unnecessary on this path, it is the wrong question to ask of it.
		protected = next
	}
}

// firstOverWidthRow is ui.firstOverWidthRow's own algorithm, ported rather than
// shared on this file's own precedent: the index of the first row in lines wider
// than width, or -1 if every row fits.
func firstOverWidthRow(lines []string, width int) int {
	for i, l := range lines {
		if ansi.StringWidth(l) > width {
			return i
		}
	}
	return -1
}

// unprotectPanelHyphens restores every panelHyphenSentinel wrapConfirmLine's loop
// left behind to a plain '-'. Safe to run before wrapConfirmGroups' own
// ansi.Hardwrap pass rather than after: Hardwrap looks only at unicode.IsSpace and
// width, and neither the sentinel nor '-' is a space and both are 1 cell wide.
func unprotectPanelHyphens(s string) string {
	return strings.ReplaceAll(s, string(panelHyphenSentinel), "-")
}

// pathRowMarker is the literal every path row a panel body draws
// begins a logical line with -- ">" then two ASCII spaces,
// immediately followed by the path.
// repointRefusalGlyph (app/actions.go) is the re-point pane's refusal row's
// leading marker, on the identical shape one space narrower.
// pathRowGlueRune is the non-breaking space gluePathRowMarker substitutes, one for
// one, for whichever marker's own trailing ASCII spaces separate it from what
// follows -- see wrapConfirmGroups.
const (
	pathRowMarker   = ">  "
	pathRowGlueRune = "\u00a0"
)

// gluePathRowMarker returns line and 0 unchanged unless it opens with the path
// marker's ">  " or repointRefusalGlyph's "⚠ ", in which case the ASCII spaces that
// separate the marker from what follows come back as that many pathRowGlueRunes
// instead, and the count travels back so wrapConfirmGroups reverses exactly that
// many and no more. A line that opens with neither marker is returned
// byte-identical at count 0, which is what keeps this safe to run on every logical
// line in every confirm body.
//
// TWO FOR THE PATH MARKER, ONE FOR THE REFUSAL'S: ">" is followed by two ASCII
// spaces where "⚠" is followed by one, so the count a caller must reverse differs
// by which marker matched. Before this recognised the refusal's marker, the glyph
// and its path stranded the same way ">" and its path used to -- measured at
// interior 57, from a break-free path of 56 cells up.
func gluePathRowMarker(line string) (string, int) {
	switch {
	case strings.HasPrefix(line, pathRowMarker):
		return ">" + pathRowGlueRune + pathRowGlueRune + line[len(pathRowMarker):], 2
	case strings.HasPrefix(line, repointRefusalGlyph):
		return "⚠" + pathRowGlueRune + line[len(repointRefusalGlyph):], 1
	}
	return line, 0
}

// truncateConfirmLines caps a wrapped panel body to at most budget screen rows,
// preferring to keep the LAST logical line (the one line an irreversible-action
// panel must never silently drop), then the FIRST (the panel's own question), then
// as much of the tail as still fits, with everything elided in between collapsing
// into a single "…" row. Chosen over blind tail-truncation because THIS panel's own
// trailing rows are its cancel/confirm legend, and over a scrolling panel because
// degrading the DISPLAY under real space pressure -- while every key still works --
// is the narrower change. A body that already fits is returned unmodified.
//
// IT TAKES GROUPS AND NOT ROWS: the unit it keeps and drops is the LOGICAL LINE, so
// a line is shown with every row it wraps to or is not shown at all
// (wrapConfirmGroups carries the defect this closed). IT ANSWERS GROUPS TOO, and is
// the flattening of truncateConfirmGroups below rather than a second copy of the
// rule: the callers that only DRAW rows want a []string, and centredPanelBox, which
// colours the headline, asks for the groups instead.
//
// A LINE THAT CANNOT FIT WHOLE IS THE ONE EXCEPTION, and the exception is the way
// out itself: when the last logical line alone is wider than the entire budget there
// is no whole-line answer that shows the reader how to leave, and a fragment of the
// way out beats none of it. That branch is truncateConfirmRows below.
func truncateConfirmLines(groups [][]string, budget int) []string {
	return flattenConfirmGroups(truncateConfirmGroups(groups, budget))
}

// flattenConfirmGroups is the groups as the rows they are drawn as, in order.
func flattenConfirmGroups(groups [][]string) []string {
	var rows []string
	for _, g := range groups {
		rows = append(rows, g...)
	}
	return rows
}

// truncateConfirmGroups is truncateConfirmLines' whole rule, answered in the shape
// the rule is stated in. See that function for every decision this body makes; what
// is only true HERE is the shape of what comes back:
//
//   - a body that fits comes back exactly as it went in, one group per logical
//     line;
//   - the "…" is its own group of one row, because that is what it is;
//   - the fallback's rows come back ONE GROUP EACH, which is not a technicality:
//     that branch runs precisely when no logical line could be kept whole, so
//     claiming its rows still belong together would be a grouping this function did
//     not achieve.
func truncateConfirmGroups(groups [][]string, budget int) [][]string {
	if budget < 1 {
		budget = 1
	}
	rows := flattenConfirmGroups(groups)
	if len(rows) <= budget {
		return groups
	}
	last := groups[len(groups)-1]
	if len(last) > budget {
		return rowsAsConfirmGroups(truncateConfirmRows(rows, budget))
	}

	// What is left once the way out has its rows. The "…" is charged first
	// out of it, because a body that reaches here is one with something
	// elided by definition -- see the early return above.
	remaining := budget - len(last)
	first := len(groups) - 1 // the earliest logical line kept in the tail run
	head := false
	if remaining > 0 {
		remaining--
		if len(groups[0]) <= remaining {
			head, remaining = true, remaining-len(groups[0])
		}
		// Backwards from the way out and STOPPING at the first line too big for what
		// is left, rather than skipping it for a smaller one further up: the "…" stands
		// for one unbroken run of elided lines, and a kept line from the far side of a
		// dropped one would make it a lie. Index 0 is the head's and is never taken
		// here.
		for i := len(groups) - 2; i >= 1 && len(groups[i]) <= remaining; i-- {
			remaining -= len(groups[i])
			first = i
		}
	}

	out := make([][]string, 0, len(groups))
	if head {
		out = append(out, groups[0])
	}
	// AN ELLIPSIS ONLY WHERE THERE IS ROOM FOR ONE BESIDE THE WAY OUT. At budget ==
	// len(last) there is none, and the way out takes the whole panel rather than
	// giving a row up to say something was dropped -- the keys still work whatever is
	// on screen, and a human reading the screen deserves the line that says so.
	if budget > len(last) {
		out = append(out, []string{"…"})
	}
	return append(out, groups[first:]...)
}

// rowsAsConfirmGroups is one group per row, for the fallback branch that
// produced rows rather than lines. See truncateConfirmGroups on why this is a
// statement rather than a formality.
func rowsAsConfirmGroups(rows []string) [][]string {
	groups := make([][]string, 0, len(rows))
	for _, r := range rows {
		groups = append(groups, []string{r})
	}
	return groups
}

// truncateConfirmRows is truncateConfirmLines' fallback for the body no whole-line
// answer fits: one head row, an "…", and as many trailing rows as the budget has
// left. It is the ENTIRE function truncateConfirmLines was until the groups split,
// kept verbatim because it is still the right answer for the case that reaches it.
//
// budget is at least 1 and rows is longer than it: both are truncateConfirmLines'
// to establish, and no other caller exists.
func truncateConfirmRows(rows []string, budget int) []string {
	if budget == 1 {
		// No room even for an ellipsis alongside the tail: the tail wins outright
		// here too. An earlier version returned a bare "…", dropping the panel's own
		// final row at exactly the size where protecting it matters least on the
		// PANEL'S side and most on the KEY'S -- the keys still work regardless of
		// what is on screen, but a human reading it deserves the same line the
		// budget==2 branch already keeps.
		return []string{rows[len(rows)-1]}
	}
	if budget == 2 {
		// No room for a head line AND an ellipsis AND even one tail line:
		// the tail (the panel's own final row) wins over the head.
		return []string{"…", rows[len(rows)-1]}
	}
	tail := budget - 2 // 1 head row + 1 ellipsis row, budget-2 tail rows
	out := make([]string, 0, budget)
	out = append(out, rows[0])
	out = append(out, "…")
	out = append(out, rows[len(rows)-tail:]...)
	return out
}

// clampScroll bounds the viewport's top edge, which indexes ROWS like the
// cursor does, not plans.
func (m *ListModel) clampScroll() {
	if top := len(m.rows) - m.viewHeight(); m.scroll > top {
		m.scroll = top
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

func (m *ListModel) ensureVisible() {
	vh := m.viewHeight()
	if m.cursor < m.scroll {
		m.scroll = m.cursor
	}
	if m.cursor >= m.scroll+vh {
		m.scroll = m.cursor - vh + 1
	}
	m.clampScroll()
}

// selectedPlan answers "the plan under the cursor, if any" -- THE one way any
// gesture may reach a plan. ok is false whenever the cursor is on a band, an empty
// section's hint or a +N more row, or on nothing at all, and every caller's answer
// to that is the same: do nothing.
//
// Its GESTURE callers are openSelected, enterRename, enterConfirmDelete and the
// other row-scoped doors. NO GESTURE may reach a plan any other way -- which is
// narrower than "nothing else reads a row's plan": rebuildRows re-finds the
// cursor's row off cursorRow, since a file row has an identity too,
// the render dispatcher hands a plan row to renderRowPainted or
// renderRecentRowPainted, and statusBarText reads the selected row's ORIGIN, all
// re-seating or drawing rather than acting on a plan.
func (m *ListModel) selectedPlan() (planItem, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return planItem{}, false
	}
	r := m.rows[m.cursor]
	if r.kind != rowPlan {
		return planItem{}, false
	}
	return r.item, true
}

// cursorOn reports whether row i should be DRAWN with the cursor band, which is not
// quite "i is the cursor": with nothing selectable anywhere snapCursor parks the
// cursor at row 0, which is a section band, and painting the band there would offer
// a selection on a row no gesture can act on. Only the paint is withheld.
func (m *ListModel) cursorOn(i int) bool {
	return i == m.cursor && i >= 0 && i < len(m.rows) && m.rows[i].kind.selectable()
}

// nextSelectableRow scans from the row one step after (step +1) or before
// (step -1) from, and answers the first row the cursor may rest on. false
// when the walk runs off that end without finding one.
func (m *ListModel) nextSelectableRow(from, step int) (int, bool) {
	for i := from + step; i >= 0 && i < len(m.rows); i += step {
		if m.rows[i].kind.selectable() {
			return i, true
		}
	}
	return 0, false
}

// firstSelectableRow is g's landing row: the top of the row slice is chrome -- a
// section's band -- not a plan. It answers 0 when the list holds no selectable row
// at all, which is where snapCursor parks the cursor for that same case.
func (m *ListModel) firstSelectableRow() int {
	for i, r := range m.rows {
		if r.kind.selectable() {
			return i
		}
	}
	return 0
}

// lastSelectableRow is G's landing row and firstSelectableRow's mirror, down
// to the same answer for a list with nothing selectable in it: the bottom of
// the row slice can be an empty section's hint, or blank filler.
func (m *ListModel) lastSelectableRow() int {
	for i := len(m.rows) - 1; i >= 0; i-- {
		if m.rows[i].kind.selectable() {
			return i
		}
	}
	return 0
}

// snapCursor puts the cursor back on a row it is allowed to rest on: into range
// first, then -- if that row is chrome -- onto the nearest selectable row below it,
// and only failing that the nearest above. Downward first because a rebuild that
// removed the cursor's own plan should carry on reading DOWN the list rather than
// reversing direction. A list with no selectable row leaves the cursor at 0, where
// selectedPlan answers "no plan" and every gesture does nothing.
func (m *ListModel) snapCursor() {
	if len(m.rows) == 0 {
		m.cursor = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.rows[m.cursor].kind.selectable() {
		return
	}
	if i, ok := m.nextSelectableRow(m.cursor, 1); ok {
		m.cursor = i
		return
	}
	if i, ok := m.nextSelectableRow(m.cursor, -1); ok {
		m.cursor = i
		return
	}
	m.cursor = 0
}

// moveCursor walks the cursor delta SELECTABLE rows, stepping over the bands and
// empty-section hints in between, and stops on the last selectable row in that
// direction rather than running off the end.
//
// RULED: moving the cursor past a section's allocation does NOTHING. It does not
// scroll, and it does not expand. Between the sections there is nothing to move
// "past": the row after Recently opened's last row is the separator, then "Your
// plans"' band, which this walk steps straight over. At the very bottom there is
// no row below, so the walk stops. The one gesture that reaches beyond an
// allocation is enter ON the +N more row -- which is why rowMore is selectable.
// Scrolling is not the answer either: in the collapsed state browse never builds
// more rows than the viewport holds, so m.scroll is structurally pinned at 0.
// Scrolling belongs to expanded mode, where this same walk reaching the last plan
// only lands the cursor there.
func (m *ListModel) moveCursor(delta int) {
	// The walk only ever steps from where the cursor already is, so it is snapped
	// onto a legal row first: a walk that could not recover from a cursor past the
	// end of a shorter row slice would leave j and k doing nothing at all until g or
	// G rescued them.
	m.snapCursor()
	step := 1
	if delta < 0 {
		step = -1
	}
	for n := delta * step; n > 0; n-- {
		i, ok := m.nextSelectableRow(m.cursor, step)
		if !ok {
			break
		}
		m.cursor = i
	}
	m.ensureVisible()
}

// pageScroll mirrors Model.pageScroll rather than restating it: move the viewport's
// top by a full page, clamp it, then re-seat the cursor at the new top -- never
// leaving the cursor off the screen the jump just landed on. Wired in
// listExpanded ONLY; browse's own row slice never exceeds viewHeight, so m.scroll is
// pinned at 0 there and a page gesture would have nothing to move.
//
// WHERE IT DIFFERS FROM Model.pageScroll, and this is the one place it must: that
// version re-seats onto any block, because every block in the review view is a
// landing. This list's rows are not all landings, so firstSelectableRowAtOrBelow
// does the same forward search but skips past what the cursor may not rest on.
//
// THE RE-SEATED CURSOR STAYS INSIDE THE VIEWPORT clampScroll just set WHENEVER THE
// EXPANDED SECTION STILL HOLDS A SELECTABLE ROW OF ITS OWN, and that rests on an
// inequality this function does not itself state: the longest run of CONSECUTIVE
// chrome a legal scroll can start on is listSectionChrome (2), and viewHeight()
// never drops below listSectionChrome + listSectionFloor (3) at any height
// fitsMinimum lets a keystroke through, because listMinHeight is exactly
// listBrowseChrome + listSectionFloor and the common terms cancel. So the margin
// is listSectionFloor, positive for exactly as long as the section's floor is -- BY
// CONSTRUCTION, not because 2 and 3 happen to be today's numbers.
//
// THE EXCEPTION IS NAMED HERE, NOT CLOSED: an expanded section a landed refresh has
// left with no selectable row falls back to lastSelectableRow's 0 REGARDLESS of
// where m.scroll landed, and containment can fail there. Whether pageScroll should
// do something better is a navigation question this comment does not scope.
// TestPageScrollsChromeRunNeverReachesTheMinimumViewport pins the inequality.
func (m *ListModel) pageScroll(dir int) {
	if len(m.rows) == 0 {
		return
	}
	m.scroll += dir * m.pageSize()
	m.clampScroll()
	m.cursor = m.firstSelectableRowAtOrBelow(m.scroll)
}

// pageSize mirrors Model.pageSize (app/model.go): one page is the viewport
// less two lines of carried-over context, FLOORED AT ONE so a page jump on a
// viewport of one or two rows still moves rather than never advancing.
func (m *ListModel) pageSize() int {
	if page := m.viewHeight() - 2; page > 1 {
		return page
	}
	return 1
}

// firstSelectableRowAtOrBelow scans forward from scroll (inclusive) for the first
// selectable row, mirroring Model.firstBlockAtOrBelow's search over blocks with the
// one difference pageScroll names: a row along the way can be chrome.
//
// FALLING OFF THE END WITHOUT FINDING ONE falls back to lastSelectableRow, the same
// "nearest selectable row behind" rule snapCursor uses. THIS IS A CONTRACT FOR ANY
// scroll THE FUNCTION IS HANDED: pageScroll is the only caller, and it clamps scroll
// to at most len(m.rows)-viewHeight() first.
//
// ONE ROUTE TO THE FALLBACK IS OPEN and this comment does not close it: Update's
// msgListRefreshed case applies UNCONDITIONALLY regardless of mode, so a refresh
// landing while expanded can leave "Your plans" with no plan at all, and the body
// with zero selectable rows. So the branch may run, and it is kept for the
// function's own contract either way.
// TestFirstSelectableRowAtOrBelowFallsBackWhenNoRowRemains drives it directly.
func (m *ListModel) firstSelectableRowAtOrBelow(scroll int) int {
	for i := scroll; i < len(m.rows); i++ {
		if m.rows[i].kind.selectable() {
			return i
		}
	}
	return m.lastSelectableRow()
}

// planRowIndex finds the row holding plan id.
func (m *ListModel) planRowIndex(id domain.PlanID) (int, bool) {
	for i, r := range m.rows {
		if r.kind == rowPlan && r.item.plan.ID == id {
			return i, true
		}
	}
	return 0, false
}

// planRowIndexIn finds the row holding plan id in section: a plan drawn in
// Recently opened and in its own section is two rows, and the cursor belongs on
// the copy it was on. Only a Recently opened copy falls back, to the
// catalog's, which is then the plan's one row; a catalog copy is never traded
// for Recently opened's (see rebuildRows).
func (m *ListModel) planRowIndexIn(id domain.PlanID, section listSection) (int, bool) {
	for i, r := range m.rows {
		if r.kind == rowPlan && r.item.plan.ID == id && r.section == section {
			return i, true
		}
	}
	if section == sectionRecent {
		return m.planRowIndex(id)
	}
	return 0, false
}

// fileRowIndex finds the file row for path: a file with no plan has no id, so
// its path is the identity a rebuild keeps.
func (m *ListModel) fileRowIndex(path string) (int, bool) {
	for i, r := range m.rows {
		if r.kind == rowFile && r.path == path {
			return i, true
		}
	}
	return 0, false
}

// rebuildRows re-derives the body's rows from m.items and keeps the cursor on the
// same PLAN it was on rather than the same index: the list resorts by lastActivity
// on every refresh, so an index that pointed at "Plan A" before a rebuild can
// silently point at "Plan B" after one. e/d/enter all read the cursor's row at
// keypress time, so that is not a cosmetic flicker: a user who last saw Plan A could
// rename or delete Plan B without ever having looked at it.
//
// EVERY rebuild of the row slice goes through here -- one helper, not several,
// because cursor identity across a rebuild is a single question and it took six
// fixes to answer the first time. Each caller names the thing that changed under the
// rows: the plan set (applyRefresh), the body's shape (setMode, which is also how
// commitSort and the filter reach it), the geometry the layout reads
// (tea.WindowSizeMsg), and what an awaiting section's row SAYS (the failed-load
// arm, whose answer ends the wait without applying a plan set).
//
// THE ANSWER FOR A NON-PLAN ROW: a band has no plan id to re-find, so the cursor
// keeps its POSITION instead of its identity -- clamped into the new rows and
// snapped onto the nearest selectable one. Inventing an identity for a band would
// mean following the wrong plan. Same fallback when the plan IS gone. A FILE ROW
// in Recently opened is the exception: it has no plan id either, but its path is
// an identity, and an open recorded above it moves it down a row.
//
// A PLAN DRAWN TWICE IS RE-FOUND ON THE COPY THE CURSOR WAS ON: a plan
// in Recently opened is also a row in its own section, and the first copy by id
// alone would pull a cursor left in "Your plans" up into Recently opened on every
// refresh. For the plan it was held for, the section it looks in is heldSection
// rather than the outgoing row's: the cursor comes back from the rename panel or
// the filter off a body that draws Recently opened nowhere, whose row names the
// catalog copy for a cursor that may have gone in on the other one.
//
// ONLY A RECENTLY OPENED COPY FALLS BACK BY ID, to the catalog copy, for the plan
// that aged out of Recently opened or a body that draws it once. A CATALOG COPY
// WITH NO ROOM -- behind its section's +N more after a re-sort, or on leaving
// expanded or a filter -- is not traded for the Recently opened one: that is
// another part of the screen. The position fallback answers instead, and the
// callers' own landings after it: commitSort keeps the position and
// cursorAfterWidening goes to the section's +N more, both as before Recently
// opened existed. leaveExpanded goes to the +N more too, but now whenever the
// cursor is not back on its own plan (cursorBackOn) -- a change even in a list
// with no Recently opened, where it used to stay on whatever plan the fallen
// position held.
//
// UNTIL THE FIRST INPUT THERE IS NO IDENTITY TO KEEP: the cursor is
// on the row it is on because the first load put it there, not because a human
// chose it, so while cursorPinned it takes the first selectable row instead.
// Following identity then would leave it on whatever plan a load happened to put
// it on, and enter on a fresh launch would miss the plan last opened.
//
// It reads the outgoing cursor's plan off the OLD m.rows before replacing them,
// which is why a row carries its planItem by value: m.items has already been
// replaced by the time applyRefresh calls this.
//
// IT FINISHES WITH ensureVisible, NOT clampScroll, and the difference is the whole
// of whether the cursor is on screen after a rebuild: clampScroll never moves scroll
// TOWARD the cursor. That stopped being survivable when the row set became
// mode-dependent -- a browse row index and a flat one diverge without bound, since
// browse draws only the plans it has room for and the flat body draws every one --
// so a rebuild across a mode switch could leave the cursor a hundred rows below a
// viewport still scrolled to 0, with a rename panel editing a plan the body does not
// show. ensureVisible calls clampScroll itself, so nothing is lost.
func (m *ListModel) rebuildRows() {
	prev, hadRow := m.cursorRow()
	if m.rowsDrewRecent {
		m.heldPlan = ""
		if hadRow && prev.kind == rowPlan {
			m.heldPlan, m.heldSection = prev.item.plan.ID, prev.section
		}
	}
	m.rows = m.buildRows()
	m.rowsDrewRecent = m.drawsRecent()
	switch {
	case m.cursorPinned:
		m.cursor = m.firstSelectableRow()
	case hadRow && prev.kind == rowPlan:
		section := prev.section
		if prev.item.plan.ID == m.heldPlan {
			section = m.heldSection
		}
		if i, ok := m.planRowIndexIn(prev.item.plan.ID, section); ok {
			m.cursor = i
		}
	case hadRow && prev.kind == rowFile:
		if i, ok := m.fileRowIndex(prev.path); ok {
			m.cursor = i
		}
	}
	m.snapCursor()
	m.ensureVisible()
}

// setMode is the one writer of m.mode, and it exists because the mode now decides
// the SHAPE of the body: browse draws Recently opened and "Your plans", expanded
// draws "Your plans" whole, and a panel mode draws a flat plan row per plan
// (buildRows). Switching one without rebuilding the other would leave the cursor
// indexing rows nobody is drawing.
//
// The rebuild carries the cursor by PLAN rather than by index, and brings the
// viewport back to wherever that plan landed (rebuildRows' own ensureVisible --
// BOTH halves are needed): e on a plan in Recently opened opens the rename panel
// with that same plan under the cursor of a flat body, on screen, and esc puts it
// back on the copy it was on.
func (m *ListModel) setMode(mode listMode) {
	if m.mode == mode {
		return
	}
	m.mode = mode
	m.rebuildRows()
}

// fitsMinimum reports whether this terminal is big enough to draw the list at all.
// Below either minimum the list REFUSES rather than degrades: View draws the
// refusal alone and Update takes no key but quit. Both bounds are derived -- see
// listMinHeight and listMinWidth -- and the width one is load-bearing beyond its own
// screen: it is what makes the m.width <= 12 painted-overflow residual unreachable.
func (m *ListModel) fitsMinimum() bool {
	return m.width >= listMinWidth && m.height >= listMinHeight
}

// applyRefresh installs a freshly derived plan set and rebuilds the rows
// around it. It is the only writer of m.items: the two slices must never
// drift apart, since a gesture reads the cursor's ROW while the status bar
// counts PLANS. The cursor discipline across the rebuild lives in
// rebuildRows.
func (m *ListModel) applyRefresh(items []planItem) {
	m.items = items
	m.rebuildRows()
}

// setFlagStatus writes a status sentence OWNED by the state flag set beside
// it, so that clearing the flag can take the sentence with it. Every write
// paired with a flag goes through here; every other status write is a transient
// notice with no flag behind it and keeps its plain assignment.
func (m *ListModel) setFlagStatus(s string) {
	m.status = s
	m.flagStatus = s
}

// clearFlagStatus retracts whatever setFlagStatus last wrote, and is called from
// every line that clears one of those flags. THE EQUALITY TEST IS THE WHOLE POINT:
// it retracts the flag's own sentence and never a line some later writer put on the
// same bar.
func (m *ListModel) clearFlagStatus() {
	if m.status == m.flagStatus {
		m.status = ""
	}
	m.flagStatus = ""
}

func (m *ListModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ta.SetWidth(max(m.width-4, 1))
		// A full rebuild, not a bare clampScroll: the layout reads geometry, so a
		// resize changes which plans are rows at all -- a taller terminal turns "+8
		// more" into eight plan rows. rebuildRows carries the cursor by plan across the
		// change and brings the viewport back to it.
		m.rebuildRows()
		return m, nil
	case msgListRefreshed:
		// A stale result from an earlier dispatch, superseded by a later
		// one that already landed — drop it outright, before even looking
		// at err, so a slow refresh's failure can't blank out a faster,
		// later refresh's success either.
		if msg.seq < m.appliedRefreshSeq {
			return m, nil
		}
		m.appliedRefreshSeq = msg.seq
		// Both arms below take recent.json's answer, the failed one included: it is
		// read beside the plans, and true whatever ListPlans said. Before either of
		// them rebuilds.
		m.takeRecents(msg.recents)
		// THE FIRST ANSWER OF ANY KIND ENDS THE WAIT -- the plans, or
		// the error below, which returns early -- so this sits above both and before
		// either rebuilds.
		m.awaiting = false
		if msg.err != nil {
			m.err = msg.err
			// setFlagStatus because this arm applies no items: the sentence describes
			// the rows still on screen, and the next refresh that succeeds replaces
			// exactly those rows and retracts it there. m.err is not the partner flag
			// it looks like -- nothing reads or clears it -- so without this the
			// sentence had no clearer at all.
			m.setFlagStatus("error: " + msg.err.Error())
			// The rows are not rebuilt anywhere else on this arm -- it applies no items
			// by design -- and the wait just ended, which changes what an empty
			// section's row SAYS.
			m.rebuildRows()
			return m, nil
		}
		m.clearFlagStatus()
		m.applyRefresh(msg.items)
		return m, nil
	case msgStateChanged:
		// Idle browse mode is the only moment a refresh is unconditionally safe --
		// same reasoning as the review model's Update: rename has a live draft it must
		// not clobber, and an in-flight write owns the next refresh via
		// handleListActionDone.
		//
		// EXPANDED DEFERS TOO: the reader is scrolling a body a refresh would re-sort
		// under them, moving the rows they are reading. Deferred to leaveExpanded.
		//
		// THE FILTER MODE DEFERS AND THE FILTER ITSELF DOES NOT. What defers is the
		// MODE: a refresh reshuffling the body under a half-typed query is the same
		// interruption rename defers for. The QUERY survives into browse, and browse
		// refreshes exactly as it always did.
		//
		// THE SORT MODAL DEFERS TOO, for the same reason that makes it apply on
		// enter: while the box is open the reader is deciding about the list AS THEY
		// CAN SEE IT. The CHOSEN ORDER survives the deferral exactly as the filter's
		// query does, re-applied (sortedItems) to whatever the refresh brought.
		if m.mode == listBrowse && !m.inFlight {
			return m, m.refreshCmd()
		}
		m.pendingRefresh = true
		return m, nil
	case msgListActionDone:
		return m.handleListActionDone(msg)
	case tea.KeyPressMsg:
		// Any input at all lets go of the top row: from here the cursor
		// is where the human put it, and a rebuild carries it by identity.
		m.cursorPinned = false
		if msg.String() == "ctrl+c" {
			// ctrl+c is the unconditional kill switch, ahead of mode dispatch -- same
			// invariant and matching key-string check as the review model's Update: it
			// quits from every list mode, deliberately not rebindable and not
			// mode-gated, and runs before ta.Update ever sees the keypress so rename's
			// textarea never absorbs it as input.
			return m, tea.Quit
		}
		// The minimum-size gate, ahead of mode dispatch for the same reason ctrl+c is:
		// it is a property of the SCREEN, not of the mode. Below either minimum the
		// list takes no key but quit -- j/k on a body that is not drawn, or e/d on a
		// plan the user cannot see, are gestures aimed at a screen that isn't there.
		//
		// EXCEPT ActQuit IN A MODE THAT IS TYPING, and the exception is about the text
		// rather than about the gate. Above the gate, q in rename is a LITERAL
		// CHARACTER and so is q in the filter, so letting the gate quit on it would
		// give one key two opposite meanings on either side of a resize, and the
		// losing one silently discards text the user typed and cannot currently see.
		// ctrl+c still ends the program from here, and gateQuitKey names it so the
		// refusal never advertises a key it has stopped taking.
		if !m.fitsMinimum() {
			if action, ok := m.km[msg.String()]; ok && action == keymap.ActQuit && !m.typesLiterals() {
				return m, tea.Quit
			}
			return m, nil
		}
		switch m.mode {
		case listBrowse:
			return m.updateBrowse(msg)
		case listExpanded:
			return m.updateExpanded(msg)
		case listFilter:
			return m.updateFilter(msg)
		case listSort:
			return m.updateSort(msg)
		case listRename:
			return m.updateRename(msg)
		case listConfirmDelete:
			return m.updateConfirmDelete(msg)
		case listKeys:
			return m.updateKeys(msg)
		case listInfo:
			return m.updateInfo(msg)
		}
	case tea.MouseWheelMsg:
		m.cursorPinned = false
		// The same minimum-size gate the KeyPressMsg case answers above, and the SAME
		// CALL -- m.fitsMinimum() -- not a second condition a reader has to prove
		// agrees with it. A wheel that still reached moveCursor below the gate would
		// re-aim enter/e/d at a cursor sitting on a body that isn't drawn. There is no
		// ActQuit counterpart for a wheel to mirror, so this has no exception to make.
		if !m.fitsMinimum() {
			return m, nil
		}
		return m.updateMouseWheel(msg)
	case tea.MouseClickMsg:
		m.cursorPinned = false
		// The identical gate and the SAME CALL as the wheel's case above: below either
		// minimum the list is not drawn at all, so a click that still reached
		// updateMouseClick would re-aim enter, e and d at a body the reader cannot see.
		if !m.fitsMinimum() {
			return m, nil
		}
		return m.updateMouseClick(msg)
	}
	return m, nil
}

// updateBrowse handles browse mode. enter and esc are literal key checks, not routed
// through the keymap -- enter is ALWAYS valid rather than rebindable, and esc turns
// off what the reader turned on. j/k/g/G/q reuse the review model's existing
// rebindable actions; e/d dispatch ActRename/ActDelete and "/" ActFilter, all three
// rebindable. ONE of the three is list-only and inert in the review model:
// ActRename. ActDelete takes the THREAD under the review cursor and ActFilter takes
// the OPEN DOCUMENT, so d and "/" are the two keys here that mean something on both
// screens, and neither means the same thing on both.
//
// THE LIST'S FILTER IS ENTERED FROM HERE AND FROM NOWHERE ELSE, the same door
// rename and delete get: browse is the mode a reader navigates in, and every other
// mode is one esc away from it.
func (m *ListModel) updateBrowse(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "esc" {
		// esc CLEARS AN ACTIVE FILTER, and it is a literal key here for the same
		// reason it is one in every other transient state: esc is how a thing you
		// turned on is turned off, and a filter left on with no way back but retyping
		// the query and emptying it is a list that has silently lost rows. With no
		// filter on it does nothing at all.
		//
		// IT SHADOWS ANY ACTION REBOUND TO esc, DELIBERATELY, on the precedent of the
		// literal enter below: both are checked ahead of the keymap lookup. That is
		// the same trade this mode already makes for enter -- a mode's own cancel has
		// to be reachable without consulting a rebindable map -- and esc is bound to
		// nothing in Default().
		m.clearFilter()
		return m, nil
	}
	if msg.String() == "enter" {
		// The same key, two meanings, decided by the row kind and not by a second
		// binding: enter on a plan opens it, enter on "Your plans"' +N more expands
		// the section. The row is selectable precisely so this can be pressed.
		if r, ok := m.cursorRow(); ok && r.kind == rowMore {
			return m.enterExpanded()
		}
		return m.openSelected()
	}
	action, ok := m.km[msg.String()]
	if !ok {
		return m, nil
	}
	switch action {
	case keymap.ActQuit:
		return m, tea.Quit
	case keymap.ActMoveDown:
		m.moveCursor(1)
	case keymap.ActMoveUp:
		m.moveCursor(-1)
	case keymap.ActTop:
		// Both jumps set the cursor directly, so both must honour the skip rule
		// themselves -- moveCursor is not on this path. Row 0 is a section band, and
		// the last row can be an empty section's hint.
		m.cursor = m.firstSelectableRow()
		// scroll = 0 puts the viewport's own top edge at the first row, mirroring the
		// review model's ActTop; ensureVisible then covers the one case bare scroll =
		// 0 does not, a viewport too short to reach past the leading bands.
		m.scroll = 0
		m.ensureVisible()
	case keymap.ActBottom:
		m.cursor = m.lastSelectableRow()
		// Unlike Top, Bottom can land far past whatever the viewport is currently
		// scrolled to, so it needs ensureVisible (not just clampScroll, which never
		// repositions relative to the cursor) to actually bring the cursor on screen.
		m.ensureVisible()
	case keymap.ActRename:
		return m.enterRename()
	case keymap.ActDelete:
		// A file row has nothing to destroy, so d takes it out at once rather than
		// confirming: the row leaves before the forget even lands.
		if r, ok := m.cursorRow(); ok && r.kind == rowFile {
			return m.forgetRecentFile(r.path)
		}
		return m.enterConfirmDelete()
	case keymap.ActFilter:
		return m.enterFilter()
	case keymap.ActSort:
		return m.enterSort()
	case keymap.ActReload:
		// Gated on dispatchOK like every other browse-mode dispatch: a rename/delete
		// commit returns to listBrowse immediately but leaves inFlight true until its
		// write resolves, so browse mode isn't unconditionally refresh-safe the way
		// msgStateChanged's idle case assumes. ctrl+r is the manual-reload fallback
		// for a watch that never armed, or a change that arrived while this process
		// wasn't watching.
		if !m.dispatchOK() {
			return m, nil
		}
		return m, m.refreshCmd()
	case keymap.ActKeys:
		// No dispatchOK gate: a reference panel reads nothing and writes
		// nothing, ActFilter's and ActSort's own precedent just above.
		m.enterKeysPanel()
	case keymap.ActInfo:
		// No dispatchOK gate, ActKeys' own reasoning one line up: a fact panel
		// about the row under the cursor reads nothing and writes nothing.
		// Silently inert off any row (a band, a +N more row, an empty list),
		// same as ActRename/ActDelete above answer nothing there today.
		m.enterInfoPanel()
	}
	return m, nil
}

// openSelected emits msgOpenPlan for a plan row under the cursor, msgOpenFile
// for a file row, and nothing at all off any other row. A +N more row
// never reaches here: updateBrowse's enter case sends that row to
// enterExpanded first, which is the whole reason rowMore is selectable.
//
// It refuses while a rename/delete write is in flight, same discipline as every
// other dispatch: opening switches Root's active child to the review, and the
// write's msgListActionDone would then be routed to the review (which drops it),
// leaving inFlight stuck true for the rest of the process.
func (m *ListModel) openSelected() (tea.Model, tea.Cmd) {
	r, ok := m.cursorRow()
	if !ok || (r.kind != rowPlan && r.kind != rowFile) {
		return m, nil
	}
	if !m.dispatchOK() {
		return m, nil
	}
	if r.kind == rowFile {
		return m, func() tea.Msg { return msgOpenFile{path: r.path} }
	}
	it, ok := m.selectedPlan()
	if !ok {
		return m, nil
	}
	return m, func() tea.Msg { return msgOpenPlan{plan: it.plan} }
}

// enterExpanded is the +N more row's enter: "Your plans" takes the whole body,
// Recently opened stands aside, and the cursor lands on the section's first plan.
//
// THE FIRST PLAN RATHER THAN THE FIRST HIDDEN ONE, and the alternative is worth
// naming because it looks better on paper: landing on plan r would put the cursor
// exactly where "+8 more" was pointing. It is rejected because it opens the section
// already scrolled -- there is no way back up to the rows the collapsed view WAS
// showing except by scrolling against the gesture.
//
// No dispatchOK check: expanding reads nothing and writes nothing.
func (m *ListModel) enterExpanded() (tea.Model, tea.Cmd) {
	m.setMode(listExpanded)
	// After setMode's rebuild, not before: firstSelectableRow indexes the rows the new
	// mode built, and the outgoing cursor was on a +N more row with no plan for
	// rebuildRows to follow.
	m.cursor = m.firstSelectableRow()
	m.scroll = 0
	m.ensureVisible()
	return m, nil
}

// leaveExpanded is how expanded is left: esc, the same key that cancels every other
// transient state in this program, and the only one. Quit still quits and enter
// still opens a plan.
//
// THE CURSOR IS RESTORED TO WHERE THE GESTURE STARTED, which needs saying because
// rebuildRows' own re-find cannot do it alone. That helper carries the cursor by
// PLAN, which is right whenever the plan under it is still a row of the collapsed
// layout. But the interesting case is the other one: the cursor is usually deep in
// the section, on a plan the collapsed layout has no room for, and the re-find then
// falls back to keeping the row INDEX into a body that just changed shape. That
// index can hold ANOTHER plan: the collapsed body does not line up row for row with
// the expanded one, since Recently opened's rows sit above it. Or it can hold this
// plan's Recently opened copy, which the re-find never trades a catalog copy for.
// So unless the cursor is back on its own plan (cursorBackOn), it goes
// back to the +N more row -- the row enter was pressed on.
func (m *ListModel) leaveExpanded() tea.Cmd {
	prev, hadPlan := m.selectedPlan()
	m.setMode(listBrowse)
	if !hadPlan || !m.cursorBackOn(prev.plan.ID) {
		m.cursorToMore()
	}
	return m.maybeApplyPendingRefresh()
}

// cursorToMore parks the cursor on "Your plans"' own "+N more" row, THE LANDING FOR
// A CURSOR WHOSE PLAN THE COLLAPSED BODY HAS NO ROOM FOR -- the one row that stands
// for exactly those plans, and the one place the reader can press enter again and be
// back where they were.
//
// TWO CALLERS AND ONE ANSWER: leaving expand, and clearing a filter. Both are a body
// SHRINKING under a cursor that was legitimately past its new end, and answering
// them differently would mean two rules for one situation.
//
// It leaves the cursor where it is if there is no +N more row at all, and
// snapCursor then puts it somewhere legal.
func (m *ListModel) cursorToMore() {
	for i, r := range m.rows {
		if r.kind == rowMore {
			m.cursor = i
			break
		}
	}
	m.snapCursor()
	m.ensureVisible()
}

// updateExpanded handles expanded mode: navigation, enter, esc to collapse, quit.
//
// WHAT IT DELIBERATELY DOES NOT TAKE, since a mode that silently swallows keys is
// the defect helpBarLine exists to prevent (and expandedHint is what tells the
// reader):
//
//   - RENAME AND DELETE. Both open a panel, and rename draws the FLAT body, so
//     entering one from here would collapse the expansion and esc would return to a
//     browse list -- an expand silently undone by a rename.
//   - RELOAD. A reload re-sorts the body under the reader's scroll -- the same
//     reason msgStateChanged defers here -- and esc puts ctrl+r one key away.
//   - THE LIST'S FILTER and THE SORT MODAL. Each opens from browse and nowhere else,
//     so each is one door rather than two; an already-active filter or order still
//     applies to this body, and what this mode does not do is start one.
func (m *ListModel) updateExpanded(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return m, m.leaveExpanded()
	case "enter":
		return m.openSelected()
	}
	action, ok := m.km[msg.String()]
	if !ok {
		return m, nil
	}
	switch action {
	case keymap.ActQuit:
		return m, tea.Quit
	case keymap.ActMoveDown:
		m.moveCursor(1)
	case keymap.ActMoveUp:
		m.moveCursor(-1)
	case keymap.ActTop:
		m.cursor = m.firstSelectableRow()
		m.scroll = 0
		m.ensureVisible()
	case keymap.ActBottom:
		m.cursor = m.lastSelectableRow()
		m.ensureVisible()
	case keymap.ActPageDown:
		m.pageScroll(1)
	case keymap.ActPageUp:
		m.pageScroll(-1)
	}
	return m, nil
}

// enterFilter opens the filter, and it opens it ON THE QUERY ALREADY IN HAND rather
// than on an empty one: "/" over an active filter is how you EDIT it, which is the
// gesture a reader who typed "mar" and meant "mary" actually makes. Starting empty
// would make the second "/" a clear, duplicating esc and losing text with no undo.
//
// No dispatchOK check: a filter reads nothing and writes nothing.
func (m *ListModel) enterFilter() (tea.Model, tea.Cmd) {
	m.setMode(listFilter)
	return m, nil
}

// setFilter installs a query and rebuilds the body around it. It is the ONLY writer
// of m.filter, for the reason applyRefresh is the only writer of m.items: the query
// and the rows derived from it must never describe different sets, and a keystroke
// handler that assigned the field and forgot the rebuild would leave the screen
// showing the previous query's matches.
//
// THE REBUILD IS WHAT CARRIES THE CURSOR while a query is being TYPED, through
// rebuildRows' own by-plan-id re-find. WIDENING is the harder direction and is
// answered in cursorAfterWidening.
func (m *ListModel) setFilter(query string) {
	if m.filter == query {
		return
	}
	m.filter = query
	m.rebuildRows()
}

// clearFilter turns the filter off from BROWSE, where esc is the key that does
// it (updateBrowse). It is leaveFilter's esc arm minus the mode change, and it
// shares that arm's cursor rule rather than restating it: the same widening,
// under the same cursor, has to land the same way from either side.
func (m *ListModel) clearFilter() {
	prev, hadPlan := m.selectedPlan()
	m.setFilter("")
	m.cursorAfterWidening(prev, hadPlan)
}

// cursorAfterWidening keeps the cursor meaningful across a body that just GREW,
// which is the one direction rebuildRows' re-find cannot answer alone. When the
// cursor is back on the plan it was on (cursorBackOn) it leaves it there; when the
// widened body has no row for that plan -- the everyday case, since a filtered
// match is very often one the collapsed allocation has no room for -- the cursor
// goes to "Your plans"' +N more. Without it the re-find falls back to keeping the
// row INDEX, into a body of a different length. "Is the plan drawn" is not the
// test: Recently opened can still draw a plan its section has no room for, and the
// re-find never trades the catalog copy for that one.
//
// HOW MUCH LONGER: the browse body is STRUCTURALLY BOUNDED at viewHeight, so
// clearing a query that matched two of eighty plans grows it to the viewport, and
// never to the 78 rows the loaded set minus the matches would suggest.
func (m *ListModel) cursorAfterWidening(prev planItem, hadPlan bool) {
	if !hadPlan || m.cursorBackOn(prev.plan.ID) {
		return
	}
	m.cursorToMore()
}

// cursorBackOn reports whether the cursor is on plan id's copy after the rebuilds
// a gesture ran: on the plan, and on its Recently opened copy only when that is
// the copy held for it. A re-find that missed leaves the cursor where its position
// fell, which can be another plan -- two bodies need not line up row for row --
// or, with Recently opened drawn, this plan's other copy; either way
// the gesture's own landing applies instead.
func (m *ListModel) cursorBackOn(id domain.PlanID) bool {
	r, ok := m.cursorRow()
	return ok && r.kind == rowPlan && r.item.plan.ID == id &&
		(r.section != sectionRecent || (id == m.heldPlan && m.heldSection == sectionRecent))
}

// leaveFilter returns to browse with keep as the query, which is the one difference
// between the mode's two exits: enter keeps what was typed (the filter stays on),
// esc passes "" and the whole list comes back.
//
// IT CONSUMES A DEFERRED REFRESH, like rename's esc and leaveExpanded: while the
// filter mode owns the keyboard a state change is held, and this is the seam where
// holding it stops being necessary.
//
// THE CURSOR IS THE PART THAT NEEDS CARE, and only on the esc path -- but the same
// call answers both, because accepting cannot trigger it: filter and browse draw the
// identical body, so cursorAfterWidening finds the plan and does nothing.
//
// THE MODE CHANGES BEFORE THE QUERY, and that identical body is why it can: browse
// first draws exactly what the filter drew, and only then does the query widen,
// into browse itself. The other way round the query widens inside listFilter,
// whose body draws no Recently opened, so a plan its own section has no room for
// loses its row there and falls to a position before browse is rebuilt -- and esc
// would miss the Recently opened copy the cursor went in on, which enter then esc
// (clearFilter, one widening, into browse) lands on.
func (m *ListModel) leaveFilter(keep string) tea.Cmd {
	prev, hadPlan := m.selectedPlan()
	m.setMode(listBrowse)
	m.setFilter(keep)
	m.cursorAfterWidening(prev, hadPlan)
	return m.maybeApplyPendingRefresh()
}

// updateFilter handles the filter mode, and IT NEVER CONSULTS THE KEYMAP. That is
// not an oversight to be repaired by a later reader wiring m.km in here -- it is
// what makes EVERY PRINTABLE KEY A CHARACTER: j is the letter j, not ActMoveDown;
// "/" is a slash, not a second door into this mode; q is a letter, not a quit.
// tea.Key.Text is populated only for printable characters, so it is the whole test
// for "this keystroke is input".
//
// ctrl+c is unaffected: Update takes it ahead of every mode dispatch.
//
// EVERY OTHER NAMED KEY IS IGNORED, INCLUDING THE ARROWS: the query is the one
// thing this mode edits, and enter is how the reader goes back to moving through
// what it matched.
func (m *ListModel) updateFilter(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return m, m.leaveFilter("")
	case "enter":
		return m, m.leaveFilter(m.filter)
	case "backspace":
		m.setFilter(dropLastRune(m.filter))
		return m, nil
	}
	if msg.Text == "" {
		return m, nil
	}
	m.setFilter(m.filter + msg.Text)
	return m, nil
}

// dropLastRune removes the last RUNE of s, not the last byte: a query can hold any
// character a keyboard produces, and backspacing a byte off a multi-byte one leaves
// an invalid fragment strings.Contains would then match nothing with.
func dropLastRune(s string) string {
	if s == "" {
		return ""
	}
	_, size := utf8.DecodeLastRuneInString(s)
	return s[:len(s)-size]
}

// sortColumns is the modal's option list, in the order it draws them. One slice
// rather than a switch per site, so the three options and their order on screen
// are one fact.
var sortColumns = []sortColumn{sortUpdated, sortCreated, sortName}

// sortColumnIndex is sortColumns read backwards -- the row the modal opens with
// its cursor on. It answers 0 for a column not in the list, which m.sortOrder
// should never legitimately hold in the first place: commitSort only ever writes
// back a column sortColumns itself offered. The fallback is cheap insurance for
// that invariant, not a load-bearing branch.
func sortColumnIndex(c sortColumn) int {
	for i, col := range sortColumns {
		if col == c {
			return i
		}
	}
	return 0
}

// flip is the direction the arrow takes when enter lands on the column that is
// already active -- the second of the arrow's two jobs (see updateSort).
func (d sortDirection) flip() sortDirection {
	if d == sortAscending {
		return sortDescending
	}
	return sortAscending
}

// enterSort opens the sort modal over "Your plans", the section its title names.
// From BROWSE and from nowhere else, the same one door the LIST'S filter gets and
// for the same reason: browse is the mode a reader navigates in, and every other
// mode is one esc away from it.
//
// IT OPENS WITH ITS CURSOR ON THE ACTIVE COLUMN, not on the first option, and that
// is what makes the commonest gesture two keystrokes: s then enter flips the
// direction of the order already in force.
//
// No dispatchOK check and no fetch: the modal reads nothing and writes nothing --
// not the service, and not client/config either.
func (m *ListModel) enterSort() (tea.Model, tea.Cmd) {
	// Recently opened is always in the order things were opened, so a
	// cursor in it has no order to choose and the modal has nothing to offer.
	if m.cursorSection() == sectionRecent {
		return m, nil
	}
	m.sortCursor = sortColumnIndex(m.sortOrder.column)
	m.setMode(listSort)
	return m, nil
}

// updateSort handles the modal: j/k (and the arrows bound beside them) walk the
// three options, enter APPLIES, esc discards, quit quits.
//
// THE ARROW HAS TWO JOBS AND ENTER IS WHERE THE SECOND ONE HAPPENS. The single glyph
// in an option row marks the ACTIVE column and its DIRECTION, which removes the
// separate selection column a list of options would otherwise need. So enter has two
// meanings decided by where the cursor is: ON THE ACTIVE COLUMN it FLIPS the
// direction, and nothing else can; ANYWHERE ELSE it selects that column with its
// NATURAL default (naturalDirection) rather than carrying the outgoing column's
// direction across -- which would make `name ↓` (Z-A) the answer to choosing `name`
// while `updated` was descending.
//
// APPLY ON ENTER, AND THERE IS NO LIVE PREVIEW. That absence is the design's and it
// has two reasons, both about the cursor: a live preview must hold the PRE-MODAL
// order so esc can restore it, which lands squarely in the
// cursor-identity-across-resort code that needed six fixes (rebuildRows); and
// reordering as the modal cursor moves could carry the plan under the LIST cursor
// behind "+N more" mid-interaction, so the reader would be deciding about rows that
// had already moved. Neither can happen if nothing moves until commit, which is why
// this method writes m.sortCursor and nothing else.
//
// THE CURSOR CLAMPS AT BOTH ENDS rather than wrapping: three options is few enough
// that a wrap saves nothing and costs the reader the cue that says they are at the
// end.
func (m *ListModel) updateSort(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return m, m.leaveSort()
	case "enter":
		return m, m.commitSort()
	}
	action, ok := m.km[msg.String()]
	if !ok {
		return m, nil
	}
	switch action {
	case keymap.ActQuit:
		return m, tea.Quit
	case keymap.ActMoveDown:
		m.sortCursor = min(m.sortCursor+1, len(sortColumns)-1)
	case keymap.ActMoveUp:
		m.sortCursor = max(m.sortCursor-1, 0)
	}
	return m, nil
}

// commitSort applies the highlighted column to "Your plans" and returns to browse.
//
// THE RE-SORT'S CURSOR DISCIPLINE IS rebuildRows AND NOTHING ELSE: it re-finds the
// cursor's plan BY ID in the newly ordered rows and puts the cursor back on it, and
// the order is assigned before setMode precisely so that rebuild reads the new one.
//
// WHEN THE PLAN IS NO LONGER DRAWN IN ITS SECTION -- it re-sorted behind "+N more"
// -- rebuildRows falls back to keeping the row POSITION, even while Recently opened
// still draws the plan, and that is the right answer here rather than a
// gap: a re-sort produces a body of the SAME shape and the SAME length, so the
// position the cursor keeps is a real row showing the plan that now occupies the
// line the reader was looking at. That is deliberately NOT cursorAfterWidening,
// whose subject is a body that GREW.
func (m *ListModel) commitSort() tea.Cmd {
	chosen := sortColumns[m.sortCursor]
	next := sortOrder{column: chosen, direction: naturalDirection(chosen)}
	if m.sortOrder.column == chosen {
		next.direction = m.sortOrder.direction.flip()
	}
	m.sortOrder = next
	m.setMode(listBrowse)
	return m.maybeApplyPendingRefresh()
}

// leaveSort is esc: the modal closes and NOTHING ELSE HAPPENS. No order is written,
// so the rebuild setMode runs re-derives the identical rows and rebuildRows' re-find
// puts the cursor back on the identical plan -- which is what "apply on enter" is
// worth. It consumes a deferred refresh, like every other seam that returns to a
// browse body; commitSort consumes one too, by the same call.
func (m *ListModel) leaveSort() tea.Cmd {
	m.setMode(listBrowse)
	return m.maybeApplyPendingRefresh()
}

// sortModalLineKind names the treatments a modal row takes, so a renderer maps ONE
// set of rows onto its palette instead of building a box of its own. Keeping the
// rows out of the box is what stops a renderer disagreeing about the box's WIDTH,
// the one number ui.Overlay's per-row invariant is stated against: the width is
// derived once, in sortModalContent, and written down nowhere else.
type sortModalLineKind int

const (
	sortLineTitle sortModalLineKind = iota
	sortLineBlank
	sortLineOption
	sortLineCursorOption
	sortLineFooter
)

// sortModalLine is one INTERIOR row of the modal: text already padded to
// exactly the interior width, plus which treatment it takes. The border and the
// one cell of padding either side belong to the renderer (sortModalPainted) and
// are no part of this text.
type sortModalLine struct {
	text string
	kind sortModalLineKind
}

// sortModalContent builds the modal's interior and answers its own width, so nothing
// anywhere hardcodes the box's size: the width is DERIVED from the longest thing that
// has to fit in it, which is the only definition that stays true when a word changes.
//
// MEASURED: the footer is the widest row at 24 cells, the title "sort · Your
// plans" runs 17, and an option row needs at most 11 (lead+tail is 4, and
// "updated" / "created" are the longest labels at 7). So the interior is 24 and
// the whole box is 28 CELLS WIDE.
//
// THE HEIGHT is a plain function of how many options there are: title + blank +
// N options + blank + footer is an N+4 line interior, N+6 rows with the border.
// The three options make 9 rows, inside the minimum-size gate's own height
// (listMinHeight) at every terminal a view is ever DRAWN on, which is what makes
// composeSortModal's centring arithmetic non-negative in practice.
// TestTheSortModalFitsInsideTheMinimumSizeGate re-derives all of this.
//
// EVERY ROW IS PADDED TO THE FULL INTERIOR WIDTH, and that is not cosmetic: the modal
// must paint its own interior or ui.Overlay -- a pure splice, with no notion of a box
// -- leaves whatever the frame had there showing through the short end of a row. The
// padding lives on the rows rather than in whichever renderer draws them.
func (m *ListModel) sortModalContent() ([]sortModalLine, int) {
	title := listSortTitlePrefix + listSortTitleSeparator + sectionMine.title()
	active := m.sortOrder
	columns := sortColumns

	// The lead is the modal cursor's own cell plus one space; the tail is the
	// arrow's cell plus one space in front of it. Both are constant, so the name
	// column is whatever the interior has left over.
	const lead, tail = 2, 2
	width := max(ansi.StringWidth(title), ansi.StringWidth(listSortHint))
	for _, c := range columns {
		width = max(width, lead+ansi.StringWidth(c.label())+tail)
	}

	lines := []sortModalLine{
		{text: padRight(ansi.Truncate(title, width, "…"), width), kind: sortLineTitle},
		{text: strings.Repeat(" ", width), kind: sortLineBlank},
	}
	for i, c := range columns {
		// TWO MARKS, ONE PER JOB, and they are not the same mark: the cursor cell says
		// where enter will land, the arrow says which column is ACTIVE and which way it
		// runs. Collapsing them would make the box unable to state the order currently
		// in force while the reader considers a different one.
		cursor := " "
		kind := sortLineOption
		if i == m.sortCursor {
			cursor, kind = listCursorGlyph, sortLineCursorOption
		}
		arrow := " "
		if c == active.column {
			arrow = active.direction.arrow()
		}
		name := padRight(c.label(), width-lead-tail)
		lines = append(lines, sortModalLine{text: cursor + " " + name + " " + arrow, kind: kind})
	}
	return append(lines,
		sortModalLine{text: strings.Repeat(" ", width), kind: sortLineBlank},
		sortModalLine{text: padRight(ansi.Truncate(listSortHint, width, "…"), width), kind: sortLineFooter},
	), width
}

// sortModalPainted draws the box in the chrome zone the compose and confirm panels
// already use. EVERY FRAGMENT GOES THROUGH A STYLE CARRYING ITS OWN BACKGROUND --
// the border cells included -- for the reason renderCellsPainted gives: a nested
// lipgloss.Render ends in a full SGR reset, and here that gap would show the LIST
// through the box rather than merely the wrong colour.
//
// THE BORDER IS Chrome's STYLE ALL THE WAY ROUND, rules and sides alike, and
// deliberately NOT the title's -- the centred panel's own correction applied
// here (TestTheCentredPanelBorderIsOneColourAndItsHeadIsWarn): a border
// shouting with the headline leaves nothing quieter for it to be louder THAN.
// The two RULES wore FocusHeader and the SIDES never did, so the rectangle was
// drawn in two colours and the title was the third thing in the box wearing the
// loudest of them.
func (m *ListModel) sortModalPainted() string {
	st := m.styles
	lines, width := m.sortModalContent()
	rule := strings.Repeat("─", width+2)

	out := make([]string, 0, len(lines)+2)
	out = append(out, st.Chrome.Render("┌"+rule+"┐"))
	for _, l := range lines {
		style := st.Chrome
		switch l.kind {
		case sortLineTitle:
			style = st.FocusHeader
		case sortLineCursorOption:
			style = st.Chrome.Bold(true)
		case sortLineFooter:
			style = st.Help
		}
		out = append(out, st.Chrome.Render("│ ")+style.Render(l.text)+st.Chrome.Render(" │"))
	}
	return strings.Join(append(out, st.Chrome.Render("└"+rule+"┘")), "\n")
}

// composeSortModal is the WHOLE of how the modal reaches the screen: viewPainted
// hands it the finished frame, and it splices the box into the middle of it. It is a
// no-op in every other mode, which is what lets the call be unconditional on that
// last line rather than carrying a branch.
//
// IT CHANGES NO LAYOUT ARITHMETIC, and that is the point of building the modal on a
// composite rather than as one more panel. ui.Overlay replaces spans of rows the
// frame already has: no row added, no row removed, and no row wider than it went in.
// So viewHeight reserves nothing for this, mastheadHeight reserves nothing, rowBudget
// is untouched, and there is no reservation that could drift out of step with what
// is painted.
//
// THE SPLICE ITSELF IS spliceCentred -- this method's own gate and body are the only
// things left here; see that function for the centring arithmetic, the no-clamp
// argument and why both numbers are measured off the box.
func (m *ListModel) composeSortModal(frame string) string {
	if m.mode != listSort {
		return frame
	}
	return spliceCentred(frame, m.sortModal(), m.width, m.height)
}

// squareFrame pads every row of frame out to width, so the splice below it is stated
// over a RECTANGLE rather than over whatever shape the frame happened to come out.
//
// THE FRAME IT WAS WRITTEN FOR WAS RAGGED BY DESIGN: the foreground-only render path
// padded no row to the terminal's width, and ui.Overlay splices INTO the row it is
// given and clamps x to that row's own width (correctly -- staying inside the
// background is its invariant), so over a 24-cell band a box whose left edge was at
// column 26 collapsed onto the end of the band and came out with rows chewed off
// (measured at 80x24: three of nine box rows mangled).
//
// IT IS NOT A SECOND CLAMP: a clamp bounds the FOREGROUND against the background, and
// this widens the BACKGROUND to the rectangle the composite is stated over. The clamp
// still runs and still owns every out-of-range answer.
//
// IT PADS NOTHING TODAY -- every row of the one frame left is already exactly m.width
// -- which is also why it introduces no UNSTYLED span, the one hazard a pad would
// carry here. It stays because "the background a splice is stated over is a
// rectangle" is a property of compositing, not a favour the current renderer does.
func squareFrame(frame string, width int) string {
	lines := strings.Split(frame, "\n")
	for i, l := range lines {
		lines[i] = padRight(l, width)
	}
	return strings.Join(lines, "\n")
}

// sortModal is the modal's one renderer, under the name the composite above and this
// file's tests both ask for.
func (m *ListModel) sortModal() string { return m.sortModalPainted() }

// spliceCentred is the compositing step composeSortModal and composeCentredPanel both
// reduce to once each has its own box in hand.
//
// THERE IS NO CLAMP HERE, DELIBERATELY. x and y are the plain centring arithmetic and
// nothing bounds them at this call site: ui.Overlay clamps inside itself, because
// staying inside the background is an invariant of compositing rather than a fact
// about where one caller puts its box, and a second clamp here would be a second
// place for the two to disagree. Behind the minimum-size gate both callers' numbers
// are non-negative anyway, so the clamp is a guarantee rather than a live correction.
//
// BOTH NUMBERS ARE MEASURED OFF THE BOX ITSELF rather than off a constant: each
// caller's own body function decides its box's width and height, so a reworded footer
// or a rewrapped panel body moves the box and this centring with it, in one place.
//
// MEASURED OFF width/height AND NOT OFF THE FRAME, unlike the review model's own
// composeCentredPanel -- a considered difference. That method measures off the frame
// because ui.Line.Text may carry an embedded newline that makes a real document frame
// a row taller than m.height. Nothing this model ever draws goes through ui.Line, so
// the frame this function is handed is always exactly m.height rows, and measuring it
// a second, different way would not catch a defect that invariant cannot already see.
func spliceCentred(frame, box string, width, height int) string {
	rows := strings.Split(box, "\n")
	return ui.Overlay(squareFrame(frame, width), box,
		(width-ansi.StringWidth(rows[0]))/2, (height-len(rows))/2)
}

// composeCentredPanel is the WHOLE of how a centred confirm panel reaches this
// model's screen: viewPainted hands it the finished frame on its last line and it
// splices the box (centredBox over m.centredGroups() -- the package-level box that
// belongs to NEITHER MODEL) into the middle of it, through spliceCentred. It is a
// no-op in every other mode, which is what lets viewPainted call this and
// composeSortModal unconditionally, one wrapped in the other.
//
// WHAT STILL DIFFERS FROM composeSortModal is exactly what a sibling call site should
// differ in and nothing more: which body function draws the box and which predicate
// gates it. The centring arithmetic is no longer either method's own.
//
// THE GATE IS drawsCentredPanel(), NOT isPanelMode(): the box is composited over rows
// the frame already has and reserves none of its own, so this is the one call site
// among that predicate's five readers that actually performs the splice the other
// four merely avoid interfering with.
func (m *ListModel) composeCentredPanel(frame string) string {
	if !m.drawsCentredPanel() {
		return frame
	}
	return spliceCentred(frame, centredBox(m.centredGroups(), m.styles), m.width, m.height)
}

// dispatchOK reports whether a write (or a panel that leads directly to one)
// may be dispatched right now, mirroring the review model's dispatchOK.
func (m *ListModel) dispatchOK() bool {
	if m.inFlight {
		m.status = "action in progress"
		return false
	}
	return true
}

// enterRename opens the rename panel unconditionally on inFlight, like compose's
// ActComment/ActReply entry in the review model: this panel is read-only until
// its own ctrl+d commits, and stays live while some other write is still
// resolving -- exactly what compose's own post button does now instead of
// ctrl+d. The commit itself is what checks dispatchOK, refusing without losing
// the typed draft.
func (m *ListModel) enterRename() (tea.Model, tea.Cmd) {
	it, ok := m.selectedPlan()
	if !ok {
		return m, nil
	}
	m.renameTarget = it.plan.ID
	// ON THE PREFILL, THIS REPLACES A SILENT STRIP RATHER THAN A RAW
	// PASS-THROUGH: bubbles' textarea removes C0 and DEL from a value handed to
	// SetValue and folds a bare CR into a newline, so this panel showed a clean title
	// and ctrl+d on an untouched prefill committed that clean title over the hostile
	// one. The only question is whether the mutation strips or visualises, and the
	// ruling was to visualise. ansi.Strip OF ui.VisibleControlsKeepingSelector16 IS
	// HOW A PLAIN GLYPH IS SPELT HERE, rather than a second substitution function that could drift from
	// ui/control.go's.
	//
	// ui.VisibleControlsKeepingSelector16, NOT ui.VisibleControls, AND THAT IS RULED.
	// The stripSelector16 widening was accepted as display-only -- true at every OTHER
	// caller and false at this one, since ctrl+d commits m.ta.Value() through
	// svc.RenamePlan, so a title opened here and committed unedited PERSISTS the
	// prefill and plain VisibleControls would silently turn "⚠️" into "⚠" on disk. The
	// exemption is narrow and does not touch the rule above: only the U+FE0F strip
	// is skipped.
	//
	// ⚠️ ansi.Strip IS A TERMINAL PARSER, so a raw 0x9F is an APC introducer to it and
	// this prefill would be TRUNCATED at such a byte, with ctrl+d committing the
	// truncated title. NOT REACHABLE TODAY, and the reason is the seam rather than this
	// line: it.plan.Title arrives through encoding/json, which replaces invalid UTF-8
	// with U+FFFD in both directions. If plan.Title ever stops being JSON-bounded, this
	// is the line that has to move.
	m.ta.SetValue(ansi.Strip(ui.VisibleControlsKeepingSelector16(it.plan.Title, lipgloss.NewStyle())))
	m.ta.CursorEnd()
	m.status = ""
	m.setMode(listRename)
	return m, m.ta.Focus()
}

func (m *ListModel) updateRename(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.setMode(listBrowse)
		m.ta.Reset()
		return m, m.maybeApplyPendingRefresh()
	case "ctrl+d":
		// Refusal check before any mutation: if another write is in flight, stay in
		// rename with the typed text intact -- same non-lossy discipline as compose's
		// own post button.
		if !m.dispatchOK() {
			return m, nil
		}
		title := strings.TrimSpace(m.ta.Value())
		if title == "" {
			m.status = "title must not be empty"
			return m, nil
		}
		target := m.renameTarget
		svc := m.svc
		m.setMode(listBrowse)
		m.ta.Reset()
		m.inFlight = true
		return m, m.runListAction(func(ctx context.Context) (string, error) {
			if err := svc.RenamePlan(ctx, target, title); err != nil {
				return "", err
			}
			return "plan renamed", nil
		})
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

// mastheadBoxLines is the masthead box's three rows, UNSTYLED plain text: the
// top rule, the sigil-and-name padded by mastheadPad columns each side, and
// the bottom rule, all drawn in this box's one border alphabet. Width is
// 2*mastheadPad plus mastheadBoxContent's own width -- nothing read at draw time,
// which is mastheadBoxContent's own rule and not restated here -- so the box is
// exactly this width at every terminal wide enough to hold it.
//
// A WIDE ENOUGH BOX AT A NARROW ENOUGH CANVAS still measures more cells than
// canvasWidth in principle -- viewPainted's own ansi.Truncate on every row this
// returns is what would keep that from widening the frame, matching the idiom
// every other row in this file already uses. IT DOES NOT ACTUALLY OCCUR AT ANY
// WIDTH fitsMinimum LETS THROUGH: this box is 23 cells wide (indent 1 + border
// 1 + pad 4 + "§ Draftplane" 12 + pad 4 + border 1) against a 31-cell
// listMinWidth floor, so the truncation is defensive -- proven unreachable at
// the app's own gate, not proven necessary by it -- the same relationship
// rename's masthead line's own truncation (viewPainted clips it to m.width) has
// always had to a narrow canvas.
func mastheadBoxLines() (top, mid, bottom string) {
	interior := 2*mastheadPad + ansi.StringWidth(mastheadBoxContent)
	rule := strings.Repeat(mastheadBorderH, interior)
	pad := strings.Repeat(" ", mastheadPad)
	return mastheadBorderTL + rule + mastheadBorderTR,
		mastheadBorderV + pad + mastheadBoxContent + pad + mastheadBorderV,
		mastheadBorderBL + rule + mastheadBorderBR
}

// ordinaryDeleteConfirmText is the delete confirm panel's body, verbatim
// wording: `Delete "Auth redesign" and its comment threads?`. No count: someone
// deleting a plan wants it gone and does not need to know how many threads it has.
//
// THE QUOTES ARE LITERAL AND NOT %q, AND THAT IS DELIBERATE RATHER THAN A STYLE CHOICE:
// %q spelled a control byte in the title as a Go escape, where every other frame
// hands it to confirmLines' filter and draws a Control Picture -- one byte, two
// spellings.
//
// ⚠️ AND THE %q WAS DOING ONE THING MORE: strconv.Quote escapes the whole Cf
// category, so a right-to-left override in a plan title used to arrive here
// NEUTRALISED, by accident, and now arrives RAW. IT WAS NEVER A DECIDED DEFENCE and
// is not kept -- it covered one frame while the row title and the status bar
// carried the same override verbatim, so what it bought was UNPREDICTABILITY. The
// class is a recorded follow-on, written up beside its sibling residual at
// ui.isVisibleControl.
//
// TWO KEY LINES: "y · delete" and "esc · go back". n IS NOT NAMED -- "a
// narrowing of what the bodies SAY, never of what the handlers ANSWER" -- and both
// handlers that read this string still answer n. This one function is both handlers'
// body, so naming the change once here covers both doors.
func ordinaryDeleteConfirmText(plan domain.Plan) string {
	return fmt.Sprintf("Delete \"%s\" and its comment threads?", plan.Title) + "\n\ny · delete\nesc · go back"
}

// enterConfirmDelete, unlike enterRename, refuses entry outright while a write is in
// flight rather than just gating the eventual commit: the panel carries no draft to
// protect, so there is nothing non-lossy about staying open, and refusing here means
// a rapid double-d can never even show a confirm panel naming a plan whose delete
// the user already dispatched.
func (m *ListModel) enterConfirmDelete() (tea.Model, tea.Cmd) {
	it, ok := m.selectedPlan()
	if !ok {
		return m, nil
	}
	if !m.dispatchOK() {
		return m, nil
	}
	m.deleteTarget = it.plan.ID
	m.deleteTargetHint = it.plan.SourceHint
	// The confirm text is set BEFORE the mode. Nothing rebuildRows touches on this
	// mode's path still reads m.confirm, so the order is now simply harmless -- it is
	// kept because every other writer of a panel's state in this file sets its body
	// before its mode too.
	m.confirm = ordinaryDeleteConfirmText(it.plan)
	m.setMode(listConfirmDelete)
	return m, nil
}

// updateConfirmDelete's y/n/esc are literal panel keys, not routed through
// the keymap — same precedent as the review model's confirm-approve panel.
func (m *ListModel) updateConfirmDelete(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y":
		if !m.dispatchOK() {
			m.setMode(listBrowse)
			return m, nil
		}
		target := m.deleteTarget
		hint := m.deleteTargetHint
		recentPath := m.recentPath
		svc := m.svc
		m.setMode(listBrowse)
		m.inFlight = true
		return m, m.runListAction(func(ctx context.Context) (string, error) {
			if err := svc.DeletePlan(ctx, target); err != nil {
				return "", err
			}
			// Forgotten BEFORE this returns, not after: msgListActionDone's own
			// refresh follows right behind it, and this promises both copies gone in
			// that same redraw -- a refresh that ran ahead of this write would still
			// find the entry and draw the row it just deleted.
			forgetRecent(recentPath, recent.Entry{PlanID: target, Path: hint})
			return "plan deleted", nil
		})
	case "n", "esc":
		m.setMode(listBrowse)
		return m, m.maybeApplyPendingRefresh()
	}
	return m, nil
}

// runListAction executes a write off the input path, the list's counterpart to the
// review model's runAction: fn must perform ONLY service I/O -- it runs in a
// goroutine concurrently with the Update loop, so it must never touch ListModel
// state.
//
// A bare context, unlike runAction's: the list dispatches rename and delete, and
// neither is a review fact -- a plan carries no record of who renamed it, so no
// implementation of either reads client.AttributionFrom. If one ever does, this
// fails closed and says so in the status line rather than attributing the write to
// the wrong person, and the day attribution is wanted it belongs on this line
// explicitly.
func (m *ListModel) runListAction(fn func(context.Context) (string, error)) tea.Cmd {
	return func() tea.Msg {
		status, err := fn(context.Background())
		return msgListActionDone{status: status, err: err}
	}
}

// handleListActionDone is the only place a write's result is allowed to
// touch ListModel state, mirroring the review model's handleActionDone. A
// failure says so on the status line; a success says what it did and re-derives
// items via a fresh cmd rather than inline — refresh is a service call too, and
// the loop must never block on one directly.
func (m *ListModel) handleListActionDone(msg msgListActionDone) (tea.Model, tea.Cmd) {
	m.inFlight = false
	if msg.err != nil {
		m.status = "error: " + msg.err.Error()
		return m, nil
	}
	m.status = msg.status
	m.pendingRefresh = false
	return m, m.refreshCmd()
}

// maybeApplyPendingRefresh consumes a state change msgStateChanged deferred, at the
// seams where deferring stops being necessary -- same role as the review model's
// method of the same name.
//
// ITS CALLERS ARE THE MOMENTS THE LIST RETURNS TO A BROWSE BODY, and they are not
// counted here, because every new panel adds one. Most are a panel closing: the
// delete confirm's n or esc, "?" and the info panel toggled shut, and rename's
// esc. Four close a mode that defers a refresh for a reason of its own:
// leaveExpanded (the reader was scrolling the whole section), leaveFilter (the
// filter mode is holding a query mid-keystroke), and leaveSort and commitSort (the
// sort modal is holding a decision about the list AS THE READER CAN CURRENTLY SEE
// IT). What they share is the seam, not the shape. An arm that dispatches a write
// does not call this: the write refreshes when it lands, and this would decline
// anyway while the write is in flight.
func (m *ListModel) maybeApplyPendingRefresh() tea.Cmd {
	if !m.pendingRefresh || m.inFlight {
		return nil
	}
	m.pendingRefresh = false
	return m.refreshCmd()
}

// listEmptyHint is "Your plans" with nothing in it. It occupies the one row its own
// section reserved and reaches the screen through the same renderBodyRow* clip every
// other row does.
const listEmptyHint = "No plans yet! Run `draftplane review <path>` or ask your agent to add one."

// hintLine is browse mode's own key reference -- rebindable, so built from the live
// keymap via findKey rather than a literal. Each mode's own line is an entry in
// listModeHints, and this is browse's.
//
// AN ACTIVE FILTER REPLACES ONE CLAUSE RATHER THAN ADDING ONE. It LEADS with the
// query, because a filter is a fact about what the body is showing and the reader
// wondering where a plan went reads left to right; it names the query VERBATIM,
// because saying a filter is on without saying which one leaves the reader unable to
// act on it; and it drops "/ filter", which the "/query" in front of it has already
// named. THE REASON IS REDUNDANCY AND NOT WIDTH.
//
// WIDTH IS NOT A BOUND HERE AT ALL, which is the more durable statement: the query
// is the reader's own text and no length is impossible. What holds is View's
// ansi.Truncate, not this line's arithmetic.
//
// THE FILTER CLAUSE IS EMITTED ONLY WHERE ITS OWN KEY IS TRUE. esc clears the filter
// in BROWSE; in listRename it cancels the rename, so naming "esc clear" there would
// be a new instance of exactly the defect this file rules on. The guard is defence
// against the next caller rather than a live branch, now that the table routes
// browse here and nothing else.
//
// THE ENTER CLAUSE IS ROW-AWARE: enter on a +N more row EXPANDS the section, not
// "open".
//
// MEASURED BY RENDERING THE REAL m.hintLine() AND READING ansi.StringWidth OF IT:
// 79 cells on a plan row, unfiltered; 50 on a Recently opened file row; and 88 on
// a plan row under a committed filter ("/roll · esc clear" in place of "/
// filter"). 100 COLUMNS IS THIS PRODUCT'S REFERENCE WIDTH, where the bar has
// 96 cells (barWidth = m.width - 2*ui.RailWidth(m.width),
// painted.go's own arithmetic), and every one of those fits there. At 80 columns
// the bar has 76, so a plan row's bar clips its tail -- View's own ansi.Truncate,
// this bar's standing contract (WIDTH IS NOT A BOUND, above). Re-derive these
// figures before building on them; do not do arithmetic on them.
func (m *ListModel) hintLine() string {
	r, onRow := m.cursorRow()
	enter := "enter open"
	if onRow && r.kind == rowMore {
		enter = "enter expand"
	}
	parts := []string{enter}
	if m.filterActive() && m.mode == listBrowse {
		parts = append([]string{"/" + m.filter, "esc clear"}, parts...)
	} else {
		parts = append(parts, findKey(m.km, keymap.ActFilter)+" filter")
	}
	// THE BAND DOES NOT SAY WHICH SECTION s WILL REORDER: its ▸ was removed for
	// reading as an expand affordance (bandTitle's own note). So this clause names
	// the KEY and the modal's title names the SECTION. Recently opened has no order
	// to choose, and naming s there would advertise a key that does nothing --
	// the defect this function's doc rules on for the filter clause.
	if m.cursorSection() != sectionRecent {
		parts = append(parts, findKey(m.km, keymap.ActSort)+" sort")
	}
	if onRow && r.kind == rowFile {
		// A file has no plan to rename or inspect, and d on it destroys nothing:
		// it only drops the row, so the word is remove.
		parts = append(parts, findKey(m.km, keymap.ActDelete)+" remove")
	} else {
		parts = append(parts,
			findKey(m.km, keymap.ActRename)+" rename",
			findKey(m.km, keymap.ActDelete)+" delete",
			// "i info" SITS RIGHT HERE (RULED): immediately after "d
			// delete", clustering it with the other plan-as-object actions rather
			// than with keys/quit at the tail -- the same clustering listKeyGroups'
			// own "plan management" group gives it in the "?" panel, so the two
			// surfaces teach one mental model rather than two.
			findKey(m.km, keymap.ActInfo)+" info")
	}
	return strings.Join(append(parts,
		// "? keys" SITS BESIDE "q quit": the two act on the
		// WHOLE VIEW rather than the row, so they close the bar after the row's own
		// actions.
		findKey(m.km, keymap.ActKeys)+" keys",
		findKey(m.km, keymap.ActQuit)+" quit"), " · ")
}

// listKeyGroups is this model's OWN declaration of what it answers, the "?"
// panel's content -- see reviewKeyGroups' own doc comment (app/model.go) for
// the shape shared by both, what pins this one against updateBrowse's and
// updateExpanded's real switches (TestListKeysPanelMatchesActualDispatch,
// app/keys_test.go), and what a table like this can and cannot prove.
//
// THREE GROUPS: NAVIGATION, PLAN MANAGEMENT and EXIT. There
// is no document to read facts about or a lifecycle to advance here, only rows
// to find, reorder and act on, so this table stays shorter than the review
// model's.
//
// enter IS A LITERAL, NOT AN ACTION: updateBrowse matches "enter" ahead of
// the keymap lookup (open the plan under the cursor, or expand a "+N more"
// row) exactly as it matches esc, so this row carries no keymap.Action at
// all -- literalKeys alone, the same shape modeKeys' own esc/? row uses for
// its literal half.
//
// PAGE ONLY EVER MOVES IN listExpanded (updateExpanded), never in
// listBrowse: it is named here anyway, because a key this model dispatches
// from a mode other than the one the panel opens from is still a key this
// model dispatches -- which is why its row says when it applies.
var listKeyGroups = []keysGroup{
	{
		heading: "navigation",
		rows: []keysRow{
			{literalKeys: []string{"enter"}, does: "open selected plan"},
			{actions: []keymap.Action{keymap.ActMoveDown, keymap.ActMoveUp}, keyOrder: movementKeyOrder, does: "scroll"},
			{actions: []keymap.Action{keymap.ActTop, keymap.ActBottom}, does: "jump to top/bottom"},
			{actions: []keymap.Action{keymap.ActPageDown, keymap.ActPageUp}, does: "page (while a section is expanded)"},
			{actions: []keymap.Action{keymap.ActReload}, does: "reload"},
		},
	},
	{
		heading: "plan management",
		rows: []keysRow{
			{actions: []keymap.Action{keymap.ActRename}, does: "rename"},
			{actions: []keymap.Action{keymap.ActDelete}, does: "delete"},
			// ActInfo sits between the two clusters: "show what a plan is" is a
			// plan-as-object action like rename and delete above it, not a
			// view-wide reference action like filter and sort below.
			{actions: []keymap.Action{keymap.ActInfo}, does: "show plan details"},
			{actions: []keymap.Action{keymap.ActFilter}, does: "filter"},
			{actions: []keymap.Action{keymap.ActSort}, does: "sort"},
		},
	},
	{
		heading: "exit",
		rows: []keysRow{
			{literalKeys: []string{"esc"}, actions: []keymap.Action{keymap.ActKeys}, does: "close this panel"},
			{actions: []keymap.Action{keymap.ActQuit}, does: "quit"},
		},
	},
}

// listKeysText is listKeys' body, built once at entry (enterKeysPanel) from
// the live keymap -- reviewKeysText's own shape, one model over.
//
// "Key Actions", NOT "Keys in this view:" -- see reviewKeysText's own doc
// comment (app/model.go) for the same change on the review side.
func listKeysText(km keymap.Map) string {
	return keysPanelText(km, "Key Actions", listKeyGroups)
}

// enterKeysPanel opens listKeys: no dispatchOK gate, ActFilter's and
// ActSort's own precedent in updateBrowse -- a reference panel reads nothing
// and writes nothing.
func (m *ListModel) enterKeysPanel() {
	m.confirm = listKeysText(m.km)
	m.setMode(listKeys)
}

// updateKeys answers listKeys' two keys -- Model.updateKeys' own shape, one
// model over: esc, or ActKeys again through the keymap, toggle the panel
// shut, back to listBrowse.
func (m *ListModel) updateKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "esc" || m.km[msg.String()] == keymap.ActKeys {
		m.setMode(listBrowse)
		return m, m.maybeApplyPendingRefresh()
	}
	return m, nil
}

// enterInfoPanel opens listInfo on the row under the cursor: no dispatchOK
// gate, listKeys' own precedent (a reference panel reads nothing and writes
// nothing). Silently does nothing off a plan row, mirroring every other
// row-scoped door in updateBrowse (ActRename, ActDelete).
func (m *ListModel) enterInfoPanel() {
	it, ok := m.selectedPlan()
	if !ok {
		return
	}
	m.confirm = infoPanelText(it)
	m.setMode(listInfo)
}

// updateInfo answers listInfo's two keys, updateKeys' own shape one mode
// over: esc, or ActInfo again through the keymap, toggle the panel shut, back
// to listBrowse.
func (m *ListModel) updateInfo(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "esc" || m.km[msg.String()] == keymap.ActInfo {
		m.setMode(listBrowse)
		return m, m.maybeApplyPendingRefresh()
	}
	return m, nil
}

// infoPanelDateLine renders the panel's date line, or nothing at all when t is
// the zero value -- the panel's own invariant: "a label with a
// zero date beside it is worse than an absent line". A plan nothing has
// happened on yet has no LastActivityAt to show.
//
// UTC, ABSOLUTE, NEVER RELATIVE: this panel is opened
// deliberately, to read the actual value, and a relative age is the one form
// nobody can quote to a colleague. THE FORMAT ITSELF IS CHOSEN HERE RATHER
// THAN REUSED, because there is no earlier convention to match -- this panel
// is the first surface in this codebase to render a timestamp at all.
func infoPanelDateLine(label string, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return label + ": " + t.UTC().Format("2006-01-02 15:04 UTC") + "\n"
}

// infoPanelLine is infoPanelDateLine's rule applied to the rest of this panel:
// "no row of this panel may state a fact the plan does not carry -- a blank label
// is worse than an absent line". A plan an agent saved with no source has no
// File: to show.
func infoPanelLine(label, value string) string {
	if value == "" {
		return ""
	}
	return label + ": " + value + "\n"
}

// infoPanelText is listInfo's body -- the pinned copy for line order, labels,
// capitalisation and blank lines. Every field comes off the row's own
// planItem/domain.Plan -- no store read here.
//
// NEITHER COPYABLE LINE IS CLIPPED, HOME-RELATIVISED, OR TRUNCATED: this
// function reads it.plan.ID and it.plan.SourceHint RAW, never through
// planItem.sourceLabel or homeRelative, both of which exist specifically to
// shorten a path for a fixed-width column -- the one thing a line meant to be
// pasted into a shell must never have done to it. The panel BOX still
// word-wraps a line too wide for the terminal (confirmGroups' own real
// word-wrap, never a truncation), which keeps every character on screen
// somewhere without cutting any of them.
func infoPanelText(it planItem) string {
	var b strings.Builder
	b.WriteString(it.plan.Title + "\n")
	fmt.Fprintf(&b, "Plan ID: %s\n", it.plan.ID)
	b.WriteString(infoPanelDateLine("Last Updated", it.plan.LastActivityAt))
	b.WriteString(infoPanelLine("File", it.plan.SourceHint))
	// THE COPYABLE LINE FALLS BACK TO THE ID WHEN THERE IS NO FILE, or WHEN THE
	// FILE LINE NAMES A URL RATHER THAN A FILE ON THIS MACHINE, and neither case
	// is exotic. A SOURCELESS plan is what the MCP `save` verb creates with no
	// source argument at all, and it lists here beside every other row.
	// Interpolating an empty SourceHint produced the literal string "draftplane
	// review " -- a command that cannot be run, offered under a heading that
	// says to run it, which is worse than either fact on its own. A URL-sourced
	// plan (notion://, ...) fails the same way for a different reason:
	// openReviewTarget has no file to stat and the URL is not shaped like a plan
	// id, so it falls through to a lookup that cannot succeed. The id is the
	// honest fallback for both, and not a second-best: `draftplane review <id>`
	// opens either plan regardless of where its content actually lives. A file
	// path stays as it is even when the file is gone, because openReviewTarget
	// resolves a gone path back to its plan via its own index lookup.
	addr := it.plan.SourceHint
	if addr == "" || client.URLSource(addr) {
		addr = string(it.plan.ID)
	}
	fmt.Fprintf(&b, "\nReview with:\n\ndraftplane review %s", addr)
	return b.String()
}

// filterHint is the help bar WHILE a query is being typed, and it is the one place
// the query itself is drawn as the reader types it. It leads with the filter key's
// own conventional shape ("/mary") because that is the line every terminal reader
// already knows, and it costs NO ROW: the help bar is reserved in every mode, so the
// filter reserves nothing of its own and the chrome is untouched -- which is what
// lets the body re-allocate under a filter without the geometry moving at all.
//
// It names both exits, and they are not the same exit: enter ACCEPTS (the filter
// stays on and browse takes over), esc CLEARS (the whole list comes back). A hint
// naming one of them would leave the other undiscoverable.
//
// ⚠️ m.filter IS DRAWN UNFILTERED HERE AND IN hintLine, AND WHAT MAKES THAT SAFE IS
// A PROPERTY OF THE MESSAGE LOOP RATHER THAN OF THE STRING: no byte a reader did not
// type ever reaches this field. updateFilter's tea.KeyPressMsg case is the query's
// only writer that GROWS it, a rune at a time; clearFilter and leaveFilter write it
// too, through the same setFilter, but only ever with "" or the query already
// standing. No paste reaches this model at all (Update has no tea.PasteMsg arm), so
// the day a paste handler is aimed at m.filter is the day these two draws need the
// control-byte filter the way this comment always said they would.
func (m *ListModel) filterHint() string {
	return fmt.Sprintf("/%s · enter accept · esc clear", m.filter)
}

// confirmDeleteHint names updateConfirmDelete's own literal keys -- y/n/esc, not
// routed through the keymap -- so this is a bare string rather than something findKey
// builds from a live binding. One row, unconditionally, and shorter than hintLine's
// own text at every width this program supports, so swapping it in never changes the
// "+2 status/help" row count viewHeight already reserves.
//
// COMPOSED BY confirmHint rather than spelled out here: this was the first y/n panel
// in the program to get a bar of its own, and the review model's now all take the
// identical shape. One composer means they cannot drift into naming their literal
// keys differently. The review model's delete-thread panel renders the same string,
// since both panels' verb is "delete", and they are still two call sites rather than
// one shared name: the coincidence is in the verb, not in the panel.
var confirmDeleteHint = confirmHint("delete")

// renameHint names updateRename's own two literal keys, and it closes the last of
// the modes that inherited browse's line: rename's textarea claims every printable
// key, so hintLine's "enter open · / filter · s sort · e rename · d delete · q quit"
// named six gestures the textarea takes as characters instead. Drawn in the panel's
// hint strip too, one constant for both so the strip and the bar cannot disagree.
const renameHint = "ctrl+d save · esc cancel"

// expandedHint names updateExpanded's own accepted keys apart from plain cursor
// movement (j/k/g/G, left unnamed here exactly as hintLine leaves them unnamed in
// browse): enter, esc, pgdn/pgup and quit. hintLine's "e rename · d delete" would
// advertise two gestures that mode deliberately does not take, which is exactly the
// wrong-hint defect: "the defect is not a missing hint, it is a wrong one. Silence
// is fine; misinformation is not."
//
// "collapse" rather than "back" or "cancel": esc here undoes an expansion rather
// than abandoning a draft or refusing an action. The page segment's wording is
// taken from the review keys panel's own "page" row (reviewKeyGroups) rather than
// invented, and both keys are read through findKey so a rebind moves them here too.
func (m *ListModel) expandedHint() string {
	return fmt.Sprintf("enter open · esc collapse · %s/%s page · %s quit",
		findKey(m.km, keymap.ActPageDown), findKey(m.km, keymap.ActPageUp), findKey(m.km, keymap.ActQuit))
}

// listModeHints is the list model's help bar: one entry per mode, and the whole of
// what the bottom line may say. It is the review model's modeHints in the same shape
// (see helpHint, which both share), for the same reason and against the same history.
//
// THIS FILE IS WHERE THE ACCRETION WAS MEASURED. helpBarLine was a switch whose
// default was hintLine() -- browse's keys -- so a mode that said nothing said
// browse's: "enter open · e rename · d delete · q quit" told the user those keys were
// live the instant a delete confirm claimed every keystroke for y/n/esc. The defect
// is not a missing hint -- it is a wrong one. Silence is fine; misinformation is not.
// That earned one case, and each mode added afterwards added ANOTHER as it went --
// four cases, four modes, one at a time -- while listRename, which no case covered,
// still inherited the browse line its textarea had already claimed every key of.
//
// THE DEFAULT IS NOW SILENCE, WHICH IS WHY THERE IS NO FIFTH CASE. Every mode
// declares its own line here, and a mode that declares none gets helpHint's zero
// value: an empty bar, not browse's. A tenth list mode cannot inherit a wrong line,
// and the exhaustiveness test over listMode(0)..listModeCount-1 fails until it
// declares a right one.
var listModeHints = map[listMode]helpHint[*ListModel]{
	listBrowse:        {derive: (*ListModel).hintLine},
	listExpanded:      {derive: (*ListModel).expandedHint},
	listFilter:        {derive: (*ListModel).filterHint},
	listSort:          {lit: listSortHint},
	listRename:        {lit: renameHint},
	listConfirmDelete: {lit: confirmDeleteHint},
	listKeys:          {derive: func(m *ListModel) string { return keysHint(m.km) }},
	// listInfo is keysHint's own shape (findKey, since ActInfo is rebindable
	// like everything else on this bar) rather than a literal: "close", not
	// "dismiss" or "cancel" -- listKeys' identical wording, for the identical
	// reason: nothing here is pending to be cancelled.
	listInfo: {derive: func(m *ListModel) string { return infoHint(m.km) }},
}

// infoHint is listInfo's own help-bar line, keysHint's shape (app/model.go)
// spent a second time: the one key the handler answers besides esc, named
// through findKey because ActInfo is rebindable like everything else here.
func infoHint(km keymap.Map) string {
	return fmt.Sprintf("esc/%s close", findKey(km, keymap.ActInfo))
}

// helpBarLine is the hint bar for whichever mode owns the keyboard, or nothing
// at all for a mode that has not said what its keys are -- see listModeHints.
func (m *ListModel) helpBarLine() string {
	return listModeHints[m.mode].line(m)
}

// planCountText is the status bar's leading count: how many plans the list holds.
//
// IT CARRIES NO PRODUCT NAME, and that is a width decision: the origin beside it is
// budgeted FIRST, at up to half the bar, so a 10-cell brand token made the counts
// themselves truncate below width 44, and the masthead two rows above already reads
// " Draftplane".
//
// A FILTER DOES NOT MOVE THE NUMBER: this line counts the LIST, a filter narrows
// the BODY, and recounting it against the matches would delete the one disclosure of
// the whole set at exactly the moment a reader is most likely to conclude a plan
// does not exist.
func (m *ListModel) planCountText() string {
	return pluralCount(len(m.items), "plan")
}

// statusBarText composes the whole status bar as ONE line of exactly width cells:
// the counts on the left (planCountText, plus the open-thread tally and any status
// message), the SELECTED ROW'S source right-aligned on the right (planItem.sourceLabel).
//
// IT IS THE SELECTION-DETAIL PATTERN: costing no column on every row to answer a
// question about one. On a "Your plans" row it repeats that row's SOURCE cell, and
// that is left in rather than special-cased away -- selection-detail bars in this
// product do not ask "is this column already visible" before drawing -- and it is
// the whole path where the cell had to clip it. On a Recently opened row it is the
// one place the source shows, since that section spends the region on OPENED, and a
// Recently opened file's own path is drawn the same way, through
// sourceLabel's own pathLabel. It is blank whenever the cursor is on neither --
// inventing one for a band or a tail would put the previous selection's path under
// the current one.
//
// THE ORIGIN IS BUDGETED FIRST, and that ordering is the point: appending it to the
// left text and clipping the line truncates away the very thing the bar exists to
// show, because the left text is what is always present. So the origin takes what it
// needs up to HALF the bar and the counts take the rest.
//
// ⚠️ IT TAKES THE BAR'S STYLE, AND THAT IS DELIBERATE RATHER THAN A TREATMENT. Two of the
// strings composed here can carry any byte, and this row is budgeted to EXACTLY
// width cells by ansi.StringWidth while lipgloss's Width().Render measures a bare C0
// byte one cell wider (driven at 100x30: one such byte drew a 31-row frame against a
// 30-row terminal). So both are filtered before they are measured, and the filter
// needs the style the reverse-video treatment is derived from.
//
// THE COUNTS WAIT FOR THE FIRST LOAD, both of them: the bar read
// "0 plans" through the wait, the same false thing the hints said. A status message
// still shows while they wait, which is why the parts are joined rather than each
// appended behind a separator of its own.
//
// THE width <= 0 GUARD IS NOT REACHABLE FROM A DRAWN VIEW: its one drawing call site
// clamps with max(..., 0), and the minimum-width gate refuses the whole screen long
// before it could reach zero. It keeps this function total for a direct caller.
func (m *ListModel) statusBarText(width int, style lipgloss.Style) string {
	if width <= 0 {
		return ""
	}
	var parts []string
	if !m.awaiting {
		parts = append(parts, m.planCountText())
		if n := m.openThreadPlanCount(); n > 0 {
			parts = append(parts, fmt.Sprintf("%d with open threads", n))
		}
	}
	if m.status != "" {
		parts = append(parts, ui.VisibleControls(m.status, style))
	}
	left := strings.Join(parts, " · ")

	var origin string
	if it, onPlan := m.selectedPlan(); onPlan {
		origin = it.sourceLabel(width/2, style)
	} else if r, onRow := m.cursorRow(); onRow && r.kind == rowFile {
		origin = pathLabel(r.path, width/2, style)
	} else {
		return padRightIn(ansi.Truncate(left, width, "…"), width, style)
	}
	originWidth := ansi.StringWidth(origin)
	leftBudget := max(width-originWidth-listGapWidth, 0)
	// ONE PAD RATHER THAN A PAD AND A GAP: the left half is padded straight up to
	// where the origin starts, which comes to the same cells, and it means the one
	// fragment that can carry a nested reset is followed by nothing unstyled.
	return padRightIn(ansi.Truncate(left, leftBudget, "…"), width-originWidth, style) + origin
}

// openThreadPlanCount is the status bar's "M with open threads" count: the number of
// plans with at least one unresolved thread, not the total open thread count across
// every plan.
//
// OVER m.items AND NOT THE FILTERED SET, like planCountText beside it: the two
// numbers on that line have to count the same set or the bar contradicts itself --
// "3 plans · 7 with open threads" is what filtering one and not the other produces.
func (m *ListModel) openThreadPlanCount() int {
	n := 0
	for _, it := range m.items {
		if it.open > 0 {
			n++
		}
	}
	return n
}

// listHeaderRow is the column-header row a PANEL mode draws in its masthead block:
// the labels with the cursor-band cell included in their own lead, since nothing
// prepends one there. A section's column-label row is the same labels through the
// same helper with a shorter lead -- see sectionHead. Its body is the flat list of
// every plan, so its region is "Your plans"' SOURCE.
func (m *ListModel) listHeaderRow() string {
	rowWidth := m.rowWidth()
	return m.columnLabels(listBandWidth+listGlyphWidth, rowWidth, sectionMine)
}

// columnLabels renders the column-header labels aligned to renderCellsPainted's
// own column math -- titleWidth and regionWidth come from listColumnWidths, the
// SAME call the body makes: one geometry, not a copy computed per section.
// What differs by section is which word heads the region
// (listSection.lastColumnLabel), at the identical width.
//
// COMMENTS (8 characters) fits the counts+gap span with ONE CELL TO SPARE, and
// that spare cell is not spare at all: it is the separation from the next
// label, which is the whole reason listLastGapWidth is 3 rather than 2.
// Unstyled -- each of its two callers wraps it in one dim style -- and finished
// with the same ansi.Truncate backstop renderCellsPainted uses.
//
// ⚠️ THIS BUDGET IS WHAT A SECTION-AWARE listColumnWidths BROKE ONCE ALREADY:
// a section computing its own trailing-region width from scratch has no
// listLastGapWidth to spend if it draws no trailing column at all, so COMMENTS
// truncated to "COMMEN" -- three cells short -- the moment "Your plans" tried
// it. budget here is always rowWidth (or less, when lead itself eats into it),
// the SAME total for every section, precisely because titleWidth and
// regionWidth are computed exactly once, above, regardless of which section
// asked.
//
// lead and budget exist because the labels are drawn in two places whose column
// origins differ by exactly the cursor-band cell: the masthead block writes the line
// itself, while a section's column-label row goes through renderBodyRowPainted,
// which prepends the band cell. Getting this wrong shifts one of the two copies by
// one cell against the plan rows beneath it -- the exact misalignment the
// positional-icon rule exists to prevent.
func (m *ListModel) columnLabels(lead, budget int, section listSection) string {
	titleWidth, regionWidth := listColumnWidths(m.rowWidth())

	name := padRight(ansi.Truncate("NAME", titleWidth, ""), titleWidth)
	gap := strings.Repeat(" ", listGapWidth)
	comments := padRight("COMMENTS", listCountsWidth+listLastGapWidth)
	lastLabel := padRight(ansi.Truncate(section.lastColumnLabel(), regionWidth, ""), regionWidth)

	line := strings.Repeat(" ", max(lead, 0)) + name + gap + comments + lastLabel
	return ansi.Truncate(line, budget, "")
}

// gateQuitKey names the key that actually ends the program from the refused screen,
// in the mode it is drawn over: the rebindable ActQuit binding everywhere except the
// two modes that are TYPING (typesLiterals), where the gate deliberately does not
// take it and ctrl+c is the only key left that quits. Both halves branch on the same
// condition, and the reason lives at the one in Update.
func (m *ListModel) gateQuitKey() string {
	if m.typesLiterals() {
		return "ctrl+c"
	}
	return findKey(m.km, keymap.ActQuit)
}

// gateLines is the refusal's own text, wrapped to this (too small) terminal and
// capped at its rows. It names both minimums, the size actually on offer, and the
// one key that still works -- a screen must not advertise keys it has stopped taking,
// and this one takes exactly one.
//
// Wrapped the way confirmLines wraps its panel, and for the identical reason:
// ansi.Wordwrap alone leaves a token wider than the width intact, so the Hardwrap
// pass behind it is what actually guarantees no line exceeds m.width. The width
// floor of 1 is measured rather than defensive: at a width of 0 or less BOTH wrappers
// return the string completely unmodified, so a terminal reporting 0 columns --
// exactly the kind of size this gate exists for -- would otherwise get the whole
// sentence back as one unwrapped line.
func (m *ListModel) gateLines() []string {
	width := max(m.width, 1)
	text := fmt.Sprintf("draftplane needs at least %d×%d to show the plan list. This terminal is %d×%d. Resize it, or press %s to quit.",
		listMinWidth, listMinHeight, m.width, m.height, m.gateQuitKey())
	lines := strings.Split(ansi.Hardwrap(ansi.Wordwrap(text, width, ""), width, true), "\n")
	if h := max(m.height, 1); len(lines) > h {
		lines = lines[:h]
	}
	return lines
}

// gateView is the whole view below either minimum: the refusal, and nothing else. No
// masthead, no sections, no status bar and no help bar -- every one of them is chrome
// this terminal has no room for, and the help bar in particular would name four keys
// the gate refuses. Padded out to m.height so the canvas stays painted edge to edge.
func (m *ListModel) gateView() tea.View {
	lines := m.gateLines()
	var b strings.Builder
	blankDoc := m.styles.DocBG.Width(m.width).Render("")
	for i, l := range lines {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(m.styles.DocBG.Width(m.width).Render(l))
	}
	for i := len(lines); i < m.height; i++ {
		b.WriteString("\n")
		b.WriteString(blankDoc)
	}
	// stampMouseMode: every tea.NewView in this package is stamped, including this
	// one. The refusal has no cursor for a wheel to move, but that is Update's
	// business, not View's: this function answers what the screen IS, and stamping it
	// keeps that answer uniform across all three of this package's views rather than
	// carrying an exception for the one screen with nothing to move.
	v := stampMouseMode(tea.NewView(b.String()))
	v.AltScreen = true
	return v
}

func (m *ListModel) View() tea.View {
	if !m.fitsMinimum() {
		return m.gateView()
	}
	return m.viewPainted()
}

// viewPainted is the whole browse frame, mirroring the review model's viewPainted:
// every row, including blank filler below a short list and the status/help bars, is
// painted edge to edge. View above gates on the minimum size and then comes straight
// here.
func (m *ListModel) viewPainted() tea.View {
	st := m.styles
	var b strings.Builder
	blankDoc := m.inset(st.DocBG.Width(m.canvasWidth()).Render(""))

	// The masthead block -- the masthead itself, blank, and (in a panel mode
	// only) the column-header row -- is written before the body loop below,
	// matching mastheadHeight's unconditional reservation in viewHeight(). Every
	// fragment is rendered through a style carrying its own Doc background so the
	// canvas stays fully painted even where there is no text. The first row is
	// ground above the masthead, Rail rather than Doc: it is margin, not an empty
	// document row.
	b.WriteString(m.styles.Rail.Width(m.width).Render(""))
	b.WriteString("\n")

	if m.isPanelMode() {
		// RENAME'S OWN MASTHEAD: the plain Brand+bold line -- see
		// listMastheadHeight's own note on why rename keeps it rather than the
		// box below.
		mastheadStyle := st.DocBG.Foreground(st.BrandColor).Bold(true)
		b.WriteString(m.inset(mastheadStyle.Width(m.canvasWidth()).Render(ansi.Truncate(listMastheadText, m.canvasWidth(), ""))))
		b.WriteString("\n")
	} else {
		// THE MASTHEAD BOX. Brand, NOT BOLD -- one style for the border and the
		// wordmark alike, and the bold half of that decision is two separate
		// findings, not one:
		//
		// THE WORDMARK is what bold was actually fixed for. Bold was driven on and
		// off at a real terminal, alongside the border-weight choices
		// mastheadBorderTL's own comment records, and found bold on "§ Draftplane"
		// carried the same too-much-attention complaint the heavy and double
		// borders were separately rejected for -- turning it off is what actually
		// settled the mark's weight.
		//
		// THE BORDER is bold OFF here too, for the same one style rather than a
		// second one scoped to the text alone -- but toggling bold didn't seem to
		// have any effect on the borders, only the copy, and on reflection it was
		// likely too minimal a change to notice. The likely cause is the terminal
		// rather than this code: box-drawing glyphs are usually drawn through a
		// line-drawing path that barely thickens under SGR bold, so the border's
		// own share of the fix is close to invisible on most terminals. IT IS
		// RECORDED ANYWAY so a future reader who turns bold back on expecting a
		// visible frame change is not surprised when the border does not move, and
		// does not "fix" an apparent no-op by scoping bold to the wordmark alone --
		// there is nothing broken to fix there.
		boxStyle := st.DocBG.Foreground(st.BrandColor)
		indent := strings.Repeat(" ", mastheadIndent)
		top, mid, bottom := mastheadBoxLines()
		for _, line := range [3]string{top, mid, bottom} {
			b.WriteString(m.inset(boxStyle.Width(m.canvasWidth()).Render(ansi.Truncate(indent+line, m.canvasWidth(), ""))))
			b.WriteString("\n")
		}
	}
	b.WriteString(blankDoc)
	b.WriteString("\n")
	if m.isPanelMode() {
		if len(m.items) > 0 {
			rowWidth := m.rowWidth()
			header := st.Dim.Width(rowWidth).Render(m.listHeaderRow())
			b.WriteString(m.inset(header))
		} else {
			b.WriteString(blankDoc)
		}
		b.WriteString("\n")
	}

	vh := m.viewHeight()
	end := min(m.scroll+vh, len(m.rows))
	for i := m.scroll; i < end; i++ {
		b.WriteString(m.renderBodyRowPainted(m.rows[i], m.cursorOn(i)))
		b.WriteString("\n")
	}
	// Whatever the sections did not claim is blank filler.
	for i := end - m.scroll; i < vh; i++ {
		b.WriteString(blankDoc)
		b.WriteString("\n")
	}
	b.WriteString(m.panelViewPainted())

	// The leading space is the bar's own inset -- st.StatusBar carries no padding of
	// its own -- so the composed line gets m.width-1 cells to fill and the two together
	// come to exactly m.width. Both bars are inset like every other row: the margins
	// run the full height of the frame, so a bar that spanned edge to edge would break
	// it at the one place a reader's eye rests between glances.
	barWidth := max(m.canvasWidth(), 0)
	b.WriteString(m.inset(st.StatusBar.Width(barWidth).Render(" "+m.statusBarText(max(barWidth-1, 0), st.StatusBar))) + "\n")
	b.WriteString(m.inset(st.Help.Width(barWidth).Render(ansi.Truncate(" "+m.helpBarLine(), barWidth, ""))))

	// The composite goes last, over a finished frame -- TWO composites now, chained,
	// since listSort and a centred confirm mode are mutually exclusive and each is a
	// no-op outside its own mode, so nesting them costs nothing either can reach.
	// stampMouseMode: this is the view a terminal reads while the wheel is live, so
	// this is the site that actually turns reporting on.
	v := stampMouseMode(tea.NewView(m.composeCentredPanel(m.composeSortModal(b.String()))))
	v.AltScreen = true
	return v
}

// panelViewPainted mirrors the review model's panelViewPainted for rename -- the one
// mode left in this model whose body is a bottom strip.
//
// THE CENTRED MODES DRAW NOTHING HERE, and are named nowhere in this switch --
// deliberately, unlike the review model's own panelViewPainted, which NAMES its
// centred members and returns "" inside the case (its switch's case list is also
// "every mode whose body is m.confirm", a second question this model's switch does
// not ask). This switch answers one question only -- "does this mode draw a STRIP" --
// so a mode absent from it draws nothing here by the switch's own default, which is
// what every one of drawsCentredPanel's members relies on. Their body is spliced
// into the middle of the finished frame instead, and a strip drawn as well would put
// the same words on screen twice under a viewHeight that, correctly, reserves no
// room for them.
func (m *ListModel) panelViewPainted() string {
	st := m.styles
	switch m.mode {
	case listRename:
		header := st.FocusHeader.Width(m.width).Render("── rename plan")
		hint := st.Strip.Width(m.width).Render(" " + renameHint)
		return header + "\n" + hint + "\n" + paintTextareaRows(m.ta, m.width, st) + "\n"
	}
	return ""
}

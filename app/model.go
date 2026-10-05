// Package app is the Bubbletea front of a review session: the affirmed
// inline-flow layout (threads beneath their blocks, unanchored section at
// document end, transient bottom panel, status+help lines). All review
// state lives in session; the model only projects and dispatches.
package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/keymap"
	"github.com/draftplane/draftplane/placement"
	"github.com/draftplane/draftplane/reanchor"
	"github.com/draftplane/draftplane/session"
	"github.com/draftplane/draftplane/theme"
	"github.com/draftplane/draftplane/ui"
)

type mode int

const (
	modeRead mode = iota
	modeCompose
	modeConfirmApprove
	modeRelocate
	// modeConflict is entered by handleActionDone when a write's error unwraps to a
	// *client.ConflictError: the tip moved out from under this session's remembered
	// base. y opens the new tip and re-anchors onto it (runAcceptTip); n retries the
	// SAME write with Base set to the tip the conflict carried (pendingRetry) -- there
	// is no force flag, so a second conflict re-prompts instead of overwriting work
	// nobody saw.
	modeConflict
	// modeConfirmDeleteThread is d over a thread in read mode, answered here
	// before anything is destroyed. An ordinary y/n panel like
	// modeConfirmApprove, sharing its exact
	// m.confirm/confirmLines/confirmPanelHeight machinery.
	//
	// THE PANEL NAMES THE THREAD (deleteThreadConfirmText), and the excerpt stays
	// even though ui.Cursor now carries the selection and the rail draws it:
	// delete is the one write that cannot be undone, so an excerpt of the
	// thread's own head comment is what a human checks the selection against
	// before pressing y.
	//
	// THE THREAD IS CAPTURED WHEN THE PANEL OPENS (m.deleteTID), never read back
	// off the selection at y, so what the excerpt named and what y destroys are
	// the same thread by construction.
	modeConfirmDeleteThread

	// modeSearch is the review view's own query line, entered by f (keymap.ActSearch)
	// or by "/" (keymap.ActFilter, which here means the OPEN DOCUMENT), typed into a
	// character at a time, and left by enter (keep the term) or esc (drop it). It is
	// ListModel's filter shape a second time -- one raw field, no boolean beside it --
	// with one deliberate divergence: TYPING MOVES NOTHING. This computes no match,
	// scrolls nothing and moves no cursor until enter (updateSearch).
	//
	// IT RESERVES NO PANEL AND NO ROW: it draws IN a row the layout already has -- the
	// status row, which viewPainted hands over whole for the duration.
	//
	// IT NEVER CONSULTS THE KEYMAP, the mechanism and not an oversight to repair by
	// wiring m.km into updateSearch: EVERY PRINTABLE KEY IS A CHARACTER here.
	// tea.Key.Text is populated only for printable keys, so it is the whole test for
	// "this keystroke is input". It is also why this mode's help bar is its own
	// literal line: no hint here may name a key the handler answers as text.
	modeSearch

	// modeSourceFault is Panel 1: the review of a plan whose own file this machine
	// could not read, drawn OVER the document it fell back to. Its input is a
	// session.SourceFileError, and it has TWO entries: New, off
	// session.Session.SourceFault -- latched at open, nil when nothing is wrong -- so
	// every door that builds a review model gets the panel without deciding to; and
	// handleActionDone's reload arm, where the session opened perfectly and the fault
	// travels on msgActionDone instead. Both go through Model.enterSourceFault, which
	// keeps them one panel rather than two, and the fault the standing panel is open
	// over is latched on m.sourceFault for the gestures that borrow its body.
	//
	// THE PLAN OPENED SUCCESSFULLY AND THAT IS THE POINT: the file is gone, the plan
	// and its threads are not, and being able to READ the thing is what lets a human
	// answer "where did this file go?".
	//
	// ITS POPULATION IS EVERY PLAN AT BOTH ENTRIES: session.OpenPlan latches
	// SourceFault for any plan whose file failed, and the reload arm classifies for
	// every plan. FOUR TEXTS, NOT TWO (sourceFaultText): SourceFileClaimed and
	// SourceFileReleased are each reachable once a plan's file resolves to another
	// plan or to none.
	//
	// THE PANEL NAMES FOUR KEYS AND THIS HANDLER ANSWERS ALL FOUR: d, esc, f and o,
	// the last two a narrow state.json mutation on SourceHint (client.SetSourceHint,
	// the empty string meaning release), each landing beside its own clause in
	// sourceFaultHint in the same commit as its handler.
	modeSourceFault

	// modeConfirmDeletePlan is modeSourceFault's d, answered before anything is
	// destroyed -- an ordinary y/n panel sharing the exact
	// m.confirm/confirmLines/confirmPanelHeight machinery.
	//
	// A SEPARATE MODE RATHER THAN A SECOND PAGE OF THE PANEL ABOVE: the two take
	// completely different key sets (f/o/d/esc against y/n/esc) and y here DESTROYS a
	// plan, so folding them would put two key sets behind one mode told apart by a
	// field -- and the help bar, keyed on the mode alone, could then only be right
	// about one of them.
	//
	// ITS BODY IS ordinaryDeleteConfirmText, CALLED AND NOT COPIED.
	//
	// n AND esc RETURN TO THE PANEL THAT OFFERED IT rather than to read mode:
	// declining destroys nothing, the file is still missing, and the question
	// modeSourceFault is asking is still open.
	modeConfirmDeletePlan

	// modeRepoint is modeSourceFault's f: the path pane, opened in Panel 1's own
	// box so the reader can name where the missing file went. It opens EMPTY, and
	// enterPathInput says why.
	//
	// A MODE OF ITS OWN RATHER THAN A SECOND PAGE OF PANEL 1, for what its two
	// keys do. enter writes ONE field of the plan's record
	// (client.SetSourceHint) and re-opens the plan on the file it now names. esc
	// returns to Panel 1 rather than to read mode, for modeConfirmDeletePlan's own
	// reason: cancelling destroys nothing, the file is still missing, and the
	// question Panel 1 asks is still open.
	//
	// Its own listing in modeHints is repointPaneHint.
	modeRepoint

	// modeKeys is the "?" panel: every action THIS model
	// dispatches, named by its live keystroke and grouped by what a reader is
	// trying to do. Entered from modeRead alone (updateRead's own ActKeys case,
	// enterKeysPanel), the one door every other confirm panel in this file also
	// opens from.
	//
	// DISMISSIBLE: it takes esc, and ActKeys again to toggle it shut -- both
	// answered by updateKeys, which this mode is routed to directly from Update's
	// own top-level switch rather than through updatePanel, so the whole mode
	// lives in this file without a second switch arm elsewhere.
	//
	// AN ORDINARY CENTRED PANEL, in drawsCentredPanel's set (app/painted.go): its
	// body is m.confirm (reviewKeysText), built once at entry
	// (enterKeysPanel) rather than re-derived on every keystroke while it is
	// open -- the live keymap does not change mid-session, so there is nothing
	// for a later repaint to catch that entry did not already see.
	//
	// ITS CONTENT IS NOT A HAND-TYPED SNAPSHOT: reviewKeyGroups (below) is the
	// one declared list of what this model answers, and it is pinned by
	// TestReviewKeysPanelMatchesActualDispatch (app/keys_test.go) against the
	// real switches (updateRead, dispatchWrite and modeRelocate's own case)
	// rather than trusted on the strength of having been read once. A future
	// change that moves an action's case out of this model's dispatch (or
	// into it) fails that test until reviewKeyGroups is edited to match --
	// see its own doc comment for exactly what that test can and cannot prove.
	modeKeys

	// modeCount is one past the last mode, and it exists so the SET can be derived
	// from this declaration rather than retyped somewhere else. It is not a mode:
	// nothing assigns it and no switch answers it. What derives it is the help
	// bar's own exhaustiveness test, which walks mode(0)..modeCount-1 and fails on
	// any mode that declared no hint (modeHints), so the loop bound moves when a
	// constant is added above it and nothing else has to.
	modeCount
)

// composeFocus is which control inside the comment composer's panel currently
// owns the keyboard: the editor textarea, or one of its two buttons.
// EDITOR IS THE ZERO VALUE, so a *Model built
// by New lands there with no assignment at all, and enterCompose's own reset
// to it on every entry is a real assignment only because a PRIOR compose
// session may have left the ring somewhere else.
//
// THE RING IS editor -> post -> cancel -> editor, tab's own direction
// (updatePanel's modeCompose arm, via composeFocusStep), and shift+tab walks
// it backward. It is a RING RATHER THAN A RANGE WITH ENDS TO REFUSE, unlike
// m.cursor against m.blocks: all three stops are always valid regardless of
// what the draft holds, so there is no analogue of cursorBlock's !ok to write.
type composeFocus int

const (
	composeFocusEditor composeFocus = iota
	composeFocusPost
	composeFocusCancel
)

// composeButtonSpan is one of the composer's buttons as the frame last DREW it:
// which stop of the ring a press selects, and the half-open column range
// [col, col+width) the label's own glyphs occupy.
//
// THE COLUMNS ARE FRAME COLUMNS, 0-indexed from the left edge of the terminal,
// which is already the coordinate tea.MouseClickMsg.X arrives in: the compose
// panel's rows are drawn at the full m.width with no rail margin either side
// (panelViewPainted), unlike the document rows above them.
//
// THE SPACING BETWEEN AND AROUND THE BUTTONS IS IN NO SPAN. A span covers the
// label and nothing else, so the gap between [ post ] and [ cancel ] resolves to
// no button at all, and the gap is two cells wide for exactly that reason.
type composeButtonSpan struct {
	focus composeFocus
	col   int
	width int
}

// composeButtonGeometry is WHERE THE RENDERER PUT THE COMPOSER'S BUTTONS. It
// exists because nothing else in this model can work that out.
//
// docTopRow + viewHeight() + 1 IS NOT THE BUTTON ROW AND MUST NOT COME BACK.
// That formula was measured wrong on 8.9% of real corpus viewports at width 80,
// by as much as +18 rows. viewPainted writes one "\n" per ui.Line, but a
// Line.Text may hold embedded newlines and occupy two or three screen rows --
// ui/render.go's live residual, the same one lineAtFrameRow walks the window to
// work around -- and the panel's own header word-wraps under Width().Render when
// the heading path is long. Both drifts are properties of the document on
// screen at the moment of the draw, so the draw is the only thing that knows
// them.
//
// A ZERO VALUE MEANS "THE LAST FRAME DREW NO BUTTON ROW", and the test for that
// is len(spans) == 0, NEVER row == 0: row 0 is a real frame row -- viewPainted's
// own band of ground above the document -- so it cannot double as the absent
// marker.
//
// ON THE MODEL, row IS ALWAYS ABSOLUTE, and that is enforced by there being
// exactly ONE WRITER: viewPainted, which assigns the whole struct once per frame
// after completing it. panelViewPainted ANSWERS a geometry rather than storing
// one, and the row it answers is panel-relative -- the only row it can know,
// since only the frame can count the document rows above the panel. Were it to
// write that value here, the field would spend the rest of the process holding
// non-empty spans (which every reader takes as "present") beside a row naming a
// DOCUMENT row near the top of the frame, and a click would resolve against it:
// the mis-aimed post this field exists to prevent.
// A caller that is not a frame -- panelView, and the tests that draw a panel on
// its own -- drops the geometry, and dropping it is the whole of what such a
// caller has to do.
type composeButtonGeometry struct {
	row   int
	spans []composeButtonSpan
}

// themeOrDefault is the one place a missing theme becomes a real one, shared
// by both models so neither can answer the question differently.
func themeOrDefault(th *theme.Theme) *theme.Theme {
	if th == nil {
		return theme.Default()
	}
	return th
}

type Model struct {
	sess *session.Session
	km   keymap.Map

	// styles is NEVER nil: a missing theme becomes the default one at
	// construction (themeOrDefault). Built once from the *theme.Theme passed to
	// New -- the theme never changes over a Model's lifetime, so this is computed
	// once rather than threaded as a render argument everywhere. The theme named
	// "system" is a SETTING that chooses a palette and nothing else, never a
	// second way to draw.
	styles *ui.Styles

	// pseudonym is this machine's stable display name for a write
	// (client/config.MachinePseudonym), substituted by ui.FormatAttribution
	// wherever a comment's Attribution carries neither an actor nor an agent.
	// Resolved once by the caller and carried unchanged for a Model's life.
	pseudonym string

	blocks     []ui.Block
	views      map[int][]ui.ThreadView
	unanchored []ui.ThreadView
	// orphans is the full set of orphaned placements, retained alongside the bare
	// ui.ThreadView projection in m.unanchored so the relocate flow has the thread
	// (original anchor, full comments) to triage from. Candidates ride along in
	// the Placement but the TUI no longer surfaces them -- they remain for agents
	// over MCP. unresolvedOrphans derives the actual triage queue from it.
	orphans  []placement.Placement
	lines    []ui.Line
	expanded map[int]bool
	// selected is which thread of a block the keyboard is aimed at: block index to
	// thread index within m.views[block], with no entry meaning the first. The
	// index only means anything against the views it was chosen over, so every
	// refresh re-derives it BY THREAD ID rather than carrying the number forward,
	// and falls back to the POSITION -- the same index, clamped -- only when that
	// thread is gone from the block entirely. See reselect and reaimSelection.
	selected map[int]int

	cursor int
	scroll int
	width  int
	height int

	mode mode
	ta   textarea.Model
	// composeFocus is modeCompose's own focus ring (composeFocus's own doc
	// comment has the shape and the ruling). It means nothing outside that
	// mode and is reset to composeFocusEditor -- its zero value -- on every
	// entry (enterCompose), so a position a PRIOR compose session left the
	// ring on can never leak into the next one.
	composeFocus composeFocus
	// composeButtons is the button row's geometry as the last frame drew it,
	// written by the renderer and read by the click hit test -- see
	// composeButtonGeometry, which carries the whole of why it is stored rather
	// than computed.
	composeButtons composeButtonGeometry
	confirm        string
	// sourceFault is the fault Panel 1 was last opened OVER, held here because the
	// panel's body is re-derived after every gesture that borrows m.confirm
	// (modeConfirmDeletePlan's question, modeRepoint's pane) and one of the
	// panel's two producers is not the session. session.Session.SourceFault is the
	// other and stays the only home for a session that latched one at open;
	// ctrl+r's fault reaches the panel on msgActionDone and lands here rather than
	// being written onto a session that opened perfectly.
	//
	// enterSourceFault is its only writer, and it writes it only when a panel
	// actually opened -- so "the panel is up" and "this names what it is up over"
	// cannot come apart. It is never cleared, exactly as the session's own fault
	// is never cleared: every reader is a re-entry to a panel that is conceptually
	// still open, and the doors that leave Panel 1 for good ask enterSourceFault
	// about the session they now hold instead.
	sourceFault *session.SourceFileError
	// pathTA is the path pane's own input widget (modeRepoint) -- a second,
	// separate textarea.Model rather than resizing and reusing m.ta (compose's
	// own, height 8): the two panels are never open at once, but sharing one
	// widget would mean remembering to reset height AND value on every mode
	// switch between them, and a bug in that bookkeeping would leak a stale
	// multi-line compose draft into the one-line path pane or vice versa.
	pathTA textarea.Model
	// repointRefusal is the path pane's OWN refusal line rather than m.status,
	// the way most other refusals in this file are written: THAT line is
	// truncated to the bar's own width (statusRow, app/painted.go) and a long
	// plan title can consume every cell of it before the refusal's own bytes are
	// ever reached. A field of its own is what lets repointGroups draw it INSIDE
	// the box, wrapped to the box's own interior instead of clipped to the bar's.
	// It is whichever of re-point's checks last refused: repointTarget's
	// (errPathRequired, or its file test), the claimed-path sentence
	// pathClaimedRefusal composes, or the "error: " line written when
	// client.PathClaimant could not answer at all.
	//
	// EMPTY IS "NO REFUSAL STANDING", and is the zero value on every entry into
	// the pane (enterPathInput) and on every keystroke or paste that changes the
	// field afterwards (updatePanel's modeRepoint case, updatePaste): a refusal
	// that survived a correction would sit under a path it is no longer about.
	repointRefusal string
	approved       bool
	status         string
	err            error

	// relocateTID tracks modeRelocate's position: the orphan under triage,
	// looked up in m.orphans by ID each time, never cached by index —
	// RefreshFromSession rebuilds the slice.
	relocateTID domain.ThreadID

	// search is modeSearch's query exactly as the reader TYPED it -- raw,
	// unfolded, untrimmed -- because the input row echoes those characters,
	// including a space they are in the middle of, or backspace stops
	// corresponding to what is on screen. searchQuery folds and trims it for the
	// match; searchActive asks whether there is one.
	//
	// ONE FIELD AND NO BOOLEAN BESIDE IT, on ListModel.filterQuery's precedent: a
	// flag kept alongside the string is a second answer that can desync from it.
	// The term outlives the mode that typed it (enter leaves modeSearch and keeps
	// it), which is why one field answers both "is a search on" and "am I typing
	// one".
	search string

	// title and planExists are snapshots of m.sess.Plan.Title / m.sess.Exists
	// taken in RefreshFromSession, which only ever runs on the Update loop after
	// the write that produced them has fully returned. View() and dispatchWrite
	// MUST read these cached fields, never m.sess.Plan or m.sess.Exists directly:
	// those live off the *session.Session that a write's goroutine (session.Create,
	// via the lazy-create paths) is free to mutate concurrently with any other
	// message the loop is processing.
	title      string
	planExists bool

	replyTo string // thread ID a pending compose replies to; "" for a new comment

	// conflictTip is the tip a *client.ConflictError most recently carried,
	// captured by handleActionDone when it enters modeConflict and shown by the
	// conflict prompt. Meaningless outside modeConflict. A domain.VersionRef,
	// matching ConflictError.Tip -- but the decline path (modeConflict's "n") sends
	// only its Hash half as the retry's precondition, never Seq.
	conflictTip domain.VersionRef

	// pendingRetry is the write closure that produced the pending conflict,
	// captured by its dispatch site so a decline (modeConflict's "n") can run it
	// again -- unchanged, same captured session and content -- after first clearing
	// the conflict with a direct RegisterVersion call whose Base is the tip the
	// conflict carried. nil whenever no conflict is pending.
	pendingRetry func(context.Context) (string, error)
	// pendingExpandBlock is the expandBlock a decline retry replays
	// unchanged, captured at the same dispatch site as pendingRetry.
	pendingExpandBlock int

	// deleteTID is the thread modeConfirmDeleteThread's y destroys, captured by
	// dispatchWrite at the moment that panel opens. The panel names that thread by
	// an excerpt of its head comment, so reading the selection back at y instead
	// would let the two disagree -- and the one they would disagree about is a
	// destruction. Cleared on every exit from that panel; empty at every other
	// moment.
	deleteTID domain.ThreadID

	// inFlight is true while a write cmd is running off the input path. All
	// model mutation from that cmd's result lands in handleActionDone on the
	// Update loop; while inFlight, dispatching another write is refused so
	// two writes never race each other or the loop.
	inFlight bool

	// stateChanged delivers a coalesced signal each time another process
	// (an agent over MCP) writes state.json; nil disables live refresh
	// entirely. waitStateChange re-arms a read on it after every delivery.
	stateChanged <-chan struct{}

	// pendingRefresh is true when a state change arrived while compose was open or
	// a write was in flight -- either would make an immediate refresh unsafe
	// (clobbering a draft, or racing the write's own projection).
	// maybeApplyPendingRefresh consumes it at the next safe seam.
	pendingRefresh bool

	// embedded is true when this Model is Root's active review child rather than
	// the standalone `draftplane review <path>` program. It gates quit() alone:
	// embedded, ActQuit (q) returns to the list via msgCloseReview instead of
	// tea.Quit. showList (l) is unconditional, and ctrl+c is unaffected either way
	// -- Update intercepts it before mode dispatch and always returns tea.Quit.
	embedded bool

	// recentPath is Root's own recentPath, copied here so runDeletePlan (the
	// missing-file panel's delete) can act on Recently opened without
	// threading the path through a second parameter list. Unset for a Model no
	// Root ever set it on (every existing test).
	recentPath string
}

// attributedCtx is the context every off-loop call this package originates for
// the review runs on, stamped with the empty attribution. app never inherits a
// context from a caller: each cmd runs on its own goroutine and builds a fresh
// one (see runAction), so this package is the only place the TUI's attribution
// can be stamped. The store behind it reads the attribution off the context and
// nothing else (client.AttributionFrom), and that accessor FAILS CLOSED -- a
// call dispatched through a bare context.Background() reaches localfs with
// nothing to stamp and errors. The empty attribution is what a review fact
// written from this TUI carries; ui.FormatAttribution shows it under the
// machine's pseudonym.
//
// Applied to every such context, not only the ones that end in a write: a call
// site would otherwise have to know which store methods behind it happen to be
// attribution-bearing today, and the wiring under it is free to change that
// without touching app.
//
// Every context ListModel originates goes bare on purpose (runListAction) --
// see its own doc comment.
func attributedCtx() context.Context {
	return client.WithAttribution(context.Background(), domain.Attribution{})
}

// New builds a Model. A NIL th IS THE DEFAULT THEME, NOT AN UNPAINTED
// RENDERING: every render path below is the full-canvas painted one either way,
// and the only thing a nil chooses is which palette it paints with
// (themeOrDefault, then one ui.NewStyles here for the model's whole life). A nil
// arriving here means only "no theme was chosen for me" -- cmd/draftplane's
// resolveTheme has already turned theme.Setting's own (nil, nil) for "system"
// into a detected preset. pseudonym is this machine's display name for a write
// (see the Model fields' own comments). stateChanged is nil to disable live
// refresh, or the channel WatchState returns to enable it.
func New(s *session.Session, km keymap.Map, th *theme.Theme, pseudonym string, stateChanged <-chan struct{}) *Model {
	th = themeOrDefault(th)
	st := ui.NewStyles(th)

	ta := textarea.New()
	ta.Placeholder = "Write a comment…"
	// TEN ROWS IS AN EXPERIMENT, NOT A DERIVED NUMBER. Eight
	// measured fine and did not FEEL it, so ten is being tried and will
	// walk back through nine to eight if it reads as too much. No test pins this:
	// one would have to be edited by every step of the experiment it exists to
	// permit. The height never tracks the terminal -- only the width does, in
	// Update's WindowSizeMsg arm -- and the textarea scrolls past whatever it is.
	ta.SetHeight(10)
	// pathTA shares the SAME style-derivation logic as ta just below but is its
	// own textarea.Model value with its own placeholder and height; see the Model
	// field's own doc comment for why the path pane does not reuse ta itself.
	pathTA := textarea.New()
	pathTA.Placeholder = "path…"
	pathTA.SetHeight(1)
	ta.SetStyles(paintedTextareaStyles(st))
	pathTA.SetStyles(paintedTextareaStyles(st))
	m := &Model{
		sess:         s,
		km:           km,
		styles:       st,
		pseudonym:    pseudonym,
		expanded:     map[int]bool{},
		selected:     map[int]int{},
		ta:           ta,
		pathTA:       pathTA,
		width:        100,
		height:       30,
		stateChanged: stateChanged,
	}
	// SAY WHAT THE FILE DID, over the document the session fell back to. Set HERE
	// because the review model has more than one constructor door -- the plan
	// list's and `draftplane review <path>`'s -- and both funnel through this
	// function, so a panel opened here is a panel every door gets without any of
	// them deciding to.
	//
	// A SESSION WITH NO WORDS FOR ITS FAULT GETS SILENCE AND NOT A GUESS, which is
	// helpHint's own zero-value ruling one layer up. sourceFaultText has words for
	// all four states, and what it answers "" for is a nil fault or a
	// SourceFileState its switch does not recognize: that residual case is what
	// this guard is left defending, silence rather than telling a reader their
	// file is gone when it is sitting on disk.
	if s.SourceFault != nil {
		m.enterSourceFault(s.SourceFault)
	}
	return m
}

// NewEmbedded builds a Model for use as Root's active review child: same as
// New, but marked embedded so its quit paths hand control back to Root's
// list (via msgCloseReview) instead of ending the whole program.
func NewEmbedded(s *session.Session, km keymap.Map, th *theme.Theme, pseudonym string, stateChanged <-chan struct{}) *Model {
	m := New(s, km, th, pseudonym, stateChanged)
	m.embedded = true
	return m
}

// RefreshFromSession recomputes the projection: blocks from content,
// placements from the service, thread views resolved onto blocks, and each
// block's selection re-aimed at the thread it was on (reselect). Called at
// startup and after reload/write actions.
func (m *Model) RefreshFromSession(ctx context.Context) error {
	// Exists is a snapshot session.Open took once, before any other actor (an
	// agent's first MCP comment) may have lazily created the plan. Rediscover
	// re-checks plan identity -- a no-op once Exists is already true -- so a plan
	// that appeared after Open stops being permanently invisible to every refresh.
	if err := m.sess.Rediscover(ctx); err != nil {
		return err
	}
	placements, err := m.sess.Placements(ctx)
	if err != nil {
		return err
	}
	approved, err := m.sess.ApprovedCurrent(ctx)
	if err != nil {
		return err
	}
	// Both service reads happen before anything is installed, so a failure in
	// either leaves the previous projection whole.
	//
	// before is the OUTGOING views, held across the install below: they are what
	// says which thread each block's selection was actually on, and reselect has
	// no other way to ask once they are gone. project builds a fresh map, so this
	// is a handle on the old one rather than an alias of the new.
	before := m.views
	p := project(ui.ParseBlocks(m.sess.Content, m.styles), placements, approved)
	m.blocks, m.views, m.unanchored, m.orphans, m.approved = p.blocks, p.views, p.unanchored, p.orphans, p.approved
	// THE ONE PLACE m.selected IS REPAIRED, and it is here because this is the
	// one place the views it indexes are renumbered -- see reselect.
	m.reselect(before)
	m.title = m.sess.Plan.Title
	m.planExists = m.sess.Exists
	m.rerender()
	return nil
}

// reselect re-aims m.selected at the threads it was aimed at before the refresh
// above renumbered them. It is the ONE repair for a stale selection and it sits here
// rather than at the three sites that read the index -- selectedThread, and
// ActCycleThread's advance and its "thread n/m selected" status -- because a repair
// at each is three chances to drift apart.
//
// m.selected is a thread index WITHIN a block, and a thread destroyed out from under
// the model renumbers every thread after it. Nothing crashes on the stale index,
// which is why it survived: selectedThread reduced it with a MODULO, so it WRAPPED
// and came back a DIFFERENT, perfectly valid-looking thread, and the keystroke that
// followed wrote to a thread the user never selected -- one of which DESTROYS what
// it lands on. SO THE RE-FIND IS BY THREAD ID: an index that is merely in range is
// no evidence that it still means the same thread.
//
// WHERE THE SELECTION LANDS WHEN ITS OWN THREAD IS GONE is reaimSelection's. A block
// with no threads left carries NO selection at all, and until one arrives
// selectedThread answers "no thread at cursor", which all three of its callers
// handle. Blocks the document no longer has are dropped by the same rule.
func (m *Model) reselect(before map[int][]ui.ThreadView) {
	sel := make(map[int]int, len(m.selected))
	for b, i := range m.selected {
		// A focus resting on the block's LINE names no thread, so there is
		// nothing to re-aim and reaimSelection would index [-1]. It survives
		// a refresh unchanged: the line it names cannot be renumbered by
		// threads appearing or vanishing beneath it.
		if i == ui.NoThread {
			sel[b] = ui.NoThread
			continue
		}
		if vs := m.views[b]; len(vs) > 0 {
			sel[b] = reaimSelection(vs, before[b], i)
		}
	}
	m.selected = sel
}

// selectCreated aims block's focus at the thread a write just created: posting
// opens the new card AND puts the focus on the new thread, so the very next
// r/R/d writes to it.
//
// THIS DOES NOT WEAKEN "REFUSE RATHER THAN CLAMP" (selectedThread's ruling): that
// rule exists so a write never lands on a thread NOBODY chose, and a thread the
// reader authored one keystroke ago is the opposite case. Every other block's focus
// is untouched. ENTER'S RULE IS UNCHANGED: ActToggleExpand still clears the focus
// to the line, and what the two gestures share is a card becoming visible, not who
// chose the thread inside it.
//
// THE RE-FIND IS BY THREAD ID, for reselect's reason: the refresh that ran just
// above renumbered this block's threads. A tid that is not on this block selects
// NOTHING and leaves the focus on the line.
func (m *Model) selectCreated(block int, tid domain.ThreadID) {
	if tid == "" {
		return
	}
	for i, v := range m.views[block] {
		if v.Thread.ID == tid {
			m.selected[block] = i
			return
		}
	}
}

// reaimSelection answers which of vs the selection that sat on index i of before
// belongs on. vs is never empty (reselect's own guard).
//
// The thread itself if it is still there, wherever it moved to WITHIN THIS BLOCK
// -- the search is over vs, so a thread that re-anchored onto a DIFFERENT block
// reads here as gone. That scoping is deliberate: following it would mean moving
// m.cursor, which is a block index nothing about a thread delete may touch. It is
// the one path by which this function still hands back a thread other than the
// one selected, and it costs a keystroke to see rather than a write to the wrong
// place.
//
// Otherwise index i AGAIN, clamped into the shorter list: the selection keeps its
// POSITION when it cannot keep its identity, so it lands on the neighbour that
// slid up into the vacated slot -- and on the new last thread when the one that
// went was last. That is rebuildRows' ruling for the list's own cursor, down to
// the reason snapCursor gives for preferring downward.
func reaimSelection(vs, before []ui.ThreadView, i int) int {
	if i < len(before) {
		for j, v := range vs {
			if v.Thread.ID == before[i].Thread.ID {
				return j
			}
		}
	}
	if i >= len(vs) {
		return len(vs) - 1
	}
	return i
}

// projection is one (document, review facts) pair resolved for display: the parsed
// blocks, the thread views resolved onto them, the ones that landed nowhere, and
// whether that document is approved. RefreshFromSession builds one from the
// session (project) and copies it into the Model's own fields.
type projection struct {
	blocks     []ui.Block
	views      map[int][]ui.ThreadView
	unanchored []ui.ThreadView
	orphans    []placement.Placement
	approved   bool
}

// project resolves placements onto blocks -- RefreshFromSession's own loop. The
// two branches that send a thread to the unanchored section are not the same
// branch, and preserving that is most of why this is one function: PlaceThreads'
// own StatusOrphaned is the ONE definition of "orphaned" this codebase has, and
// keptButUndisplayable -- a placement the re-anchorer LOCATED that no block can
// draw -- is the second. Both land in unanchored/orphans.
func project(blocks []ui.Block, placements []placement.Placement, approved bool) projection {
	p := projection{blocks: blocks, views: map[int][]ui.ThreadView{}, approved: approved}
	for _, pl := range placements {
		if pl.Status == reanchor.StatusOrphaned {
			p.unanchored = append(p.unanchored, ui.ThreadView{Thread: pl.Thread})
			p.orphans = append(p.orphans, pl)
			continue
		}
		idx := placedBlock(blocks, pl)
		if keptButUndisplayable(pl, idx) {
			p.unanchored = append(p.unanchored, ui.ThreadView{Thread: pl.Thread})
			p.orphans = append(p.orphans, pl)
			continue
		}
		p.views[idx] = append(p.views[idx], ui.ThreadView{
			Thread: pl.Thread,
			Badge:  ui.BadgeFor(pl),
			Placed: true,
		})
	}
	return p
}

// keptButUndisplayable is project's residual arm: a placement whose Status is
// anything BUT reanchor.StatusOrphaned -- the re-anchorer located this thread and
// committed to the location -- that placedBlock (idx) can draw on no ui.Block.
// Naming it here is what leaves placement.PlaceThreads the ONE definition of
// "orphaned" this codebase has.
//
// IT READS THE STATUS AS WELL AS THE INDEX, and that is not defensive padding. A
// true orphan's Anchor is the ZERO anchor and ui.ResolveAnchor's empty-span arm
// answers with the first block whose heading path is empty, so on any document
// opening with a preamble paragraph placedBlock answers 0 for a true orphan -- a
// predicate reading only idx would call a true orphan displayable.
// project never asks it that question, so the conjunct is unreached at the only call
// site and is here so the name is true of any placement handed to it.
//
// EVERY ROUTE INTO IT IS NEARLY UNREACHABLE, AND THE ARM STAYS ANYWAY: every
// placement that located a span is drawn by greatest byte overlap, blocks cover
// 98.9% of a document's bytes with a largest gap of 33, and reanchor.CreateAnchor
// refuses a span under three words. A SECTION anchor can still reach here -- an
// empty span has no position to route by -- over a document holding no block under
// its heading path.
func keptButUndisplayable(pl placement.Placement, idx int) bool {
	return pl.Status != reanchor.StatusOrphaned && idx < 0
}

// placedBlock is which block a placement is drawn on, or -1 for one that lands on no
// block at all (project's residual arm, the unanchored section).
//
// A PLACEMENT IS DRAWN WHERE THE MATCHER PUT IT, and that is the whole of what this
// function adds. reanchor reports the window it accepted -- MatchStart/MatchEnd,
// offsets into the very document these blocks were parsed from. Handing the matched
// TEXT back to ui.ResolveAnchor asks a DIFFERENT question, "which single block
// contains these words", and a window that crosses a block boundary has no such
// block: the answer is -1, and a thread the matcher deliberately located is filed as
// an orphan.
//
// IT ROUTES ON WHETHER A LOCATION WAS REPORTED, NOT ON Status: the exact and moved
// arms carry their winning hit's offsets too, and what is left with no position is a
// SECTION anchor and a true orphan, NEITHER separable by Status. THE TEST IS THAT
// THE RANGE IS NON-EMPTY, never that an end is zero: zero is a legal offset, but a
// located span can never be EMPTY because reanchor.CreateAnchor refuses a span under
// three words, while a section anchor and an orphan both leave 0..0.
//
// THE OFFSETS AND THESE BLOCKS MUST INDEX THE SAME BYTES, AND NOTHING HERE CHECKS
// THAT THEY DO. The production caller passes one document's bytes to both:
// RefreshFromSession parses m.sess.Content while Session.Placements places against
// that same field. IT IS WRITTEN DOWN BECAUSE THE FAILURE MODE CHANGED: mismatched
// inputs used to degrade LOUDLY, with the thread appearing under "unanchored" where
// someone would notice, and an offset into the wrong document is still a valid
// offset, so the same mismatch would now draw the thread on whatever block occupies
// those bytes, silently. A later change introducing a second document is what this
// paragraph is addressed to.
func placedBlock(blocks []ui.Block, pl placement.Placement) int {
	if pl.MatchEnd > pl.MatchStart {
		return blockOfGreatestOverlap(blocks, pl.MatchStart, pl.MatchEnd)
	}
	return ui.ResolveAnchor(blocks, pl.Anchor)
}

// blockOfGreatestOverlap is the block sharing the most bytes with the source range
// [start, end), or -1 when the range touches no block at all -- which an empty range
// always does, since no block can share bytes with nothing.
//
// GREATEST OVERLAP RATHER THAN THE BLOCK CONTAINING THE FIRST BYTE, which is the
// cheaper obvious rule and is wrong on the case this exists for. Blocks do not tile a
// document -- a fence's own ``` lines sit outside the code block's [SrcStart,
// SrcEnd), and blocks cover 98.9% of a document's bytes -- so a matched window's
// first byte can land in a gap, or inside the paragraph that merely LABELS what
// follows it.
//
// TIES GO TO THE EARLIER BLOCK, for determinism rather than for correctness: blocks
// never overlap, so a tie means one window is split evenly between two of them and
// what matters is that the same document places the same thread twice the same way.
// It scans every block rather than stopping at the first one starting past end,
// because nothing in ui asserts that blocks arrive in ascending SrcStart order.
func blockOfGreatestOverlap(blocks []ui.Block, start, end int) int {
	best, most := -1, 0
	for i, b := range blocks {
		if overlap := min(b.SrcEnd, end) - max(b.SrcStart, start); overlap > most {
			best, most = i, overlap
		}
	}
	return best
}

// render is this projection's line buffer, with the view state -- which blocks
// are expanded, and the cursor -- supplied by the caller. It reads blocks, views
// and unanchored, and those three ONLY; orphans and approved are read by the
// relocate queue and the status line instead.
func (p projection) render(expanded map[int]bool, cur ui.Cursor, width int, pseudonym string, st *ui.Styles) []ui.Line {
	return ui.RenderDoc(p.blocks, p.views, p.unanchored, expanded, cur, width, pseudonym, st)
}

// unresolvedOrphans is the triage queue: orphaned threads still awaiting a human
// verdict. Resolved orphans need no triage -- resolution IS a verdict.
//
// THAT RULE IS RIGHT FOR A TRUE ORPHAN AND WRONG FOR A KEPT-BUT-UNDISPLAYABLE ONE,
// and the difference was weighed and DEFERRED rather than missed: a true orphan's
// home is gone, while a displaced thread's home EXISTS and the re-anchorer found
// it. Splitting the bucket was NOT taken, because greatest-overlap routing empties
// the set it would guard. REOPEN IT WHEN SOMEONE REPORTS A THREAD THE STATUS BAR
// COUNTS THAT NOTHING CAN WALK TO.
func (m *Model) unresolvedOrphans() []placement.Placement {
	var out []placement.Placement
	for _, p := range m.orphans {
		if !p.Thread.Resolved {
			out = append(out, p)
		}
	}
	return out
}

// factsFingerprint is a cheap deterministic summary of the projected review facts:
// every thread's ID, whether it is placed on a block or fell into the unanchored
// section, its badge, resolved flag and comment count, plus the current approval
// state. It exists to tell a real change in the underlying store apart from a no-op
// reload -- most notably the TUI's own write echoing back through the fsnotify
// watch after handleActionDone has already projected it. Two calls that see the
// same facts produce the same string regardless of map iteration order.
//
// placed is included alongside badge, not folded into it: badge is "" both for an
// exactly-placed thread and for every orphaned one, so a rehome would otherwise
// fingerprint identically before and after. planExists is included alongside
// approved for the same reason: a plan another actor creates with no comment and no
// approval would otherwise drop the flash for a document that just went from
// unreviewed to under review.
func (m *Model) factsFingerprint() string {
	type row struct {
		id       string
		placed   bool
		badge    string
		resolved bool
		comments int
	}
	var rows []row
	collect := func(vs []ui.ThreadView) {
		for _, v := range vs {
			rows = append(rows, row{
				id:       v.Thread.ID.String(),
				placed:   v.Placed,
				badge:    v.Badge,
				resolved: v.Thread.Resolved,
				comments: len(v.Thread.Comments),
			})
		}
	}
	for _, vs := range m.views {
		collect(vs)
	}
	collect(m.unanchored)
	sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })

	var b strings.Builder
	fmt.Fprintf(&b, "planExists=%v approved=%v\n", m.planExists, m.approved)
	for _, r := range rows {
		fmt.Fprintf(&b, "%s|%v|%s|%v|%d\n", r.id, r.placed, r.badge, r.resolved, r.comments)
	}
	return b.String()
}

// rerender rebuilds the line buffer from the SESSION's projection -- the one
// RefreshFromSession installed into the model's fields, and the only one this
// package ever installs (see projection, and render for why the view state is
// passed rather than held).
func (m *Model) rerender() {
	m.lines = projection{blocks: m.blocks, views: m.views, unanchored: m.unanchored}.
		render(m.expanded, m.docCursor(), m.width, m.pseudonym, m.styles)
}

// Init returns only a trigger message: RefreshFromSession mutates model
// state (m.blocks, m.lines, ...) and must run on the Update loop, never in
// the cmd's own goroutine where View could read it concurrently. When
// stateChanged is non-nil, it also arms the live-refresh watch loop.
func (m *Model) Init() tea.Cmd {
	init := func() tea.Msg { return msgRefreshed{} }
	if wait := m.waitStateChange(); wait != nil {
		return tea.Batch(init, wait)
	}
	return init
}

type msgRefreshed struct{}

// waitStateChange returns a cmd that blocks for the next signal on
// stateChanged, or nil if live refresh is disabled — see waitStateChangeCmd
// for the semantics.
func (m *Model) waitStateChange() tea.Cmd {
	return waitStateChangeCmd(m.stateChanged)
}

// waitStateChangeCmd is the one wait-loop implementation behind both
// Model.waitStateChange and Root.waitStateChange: a cmd blocking for the next
// signal on ch, or nil (live refresh disabled) for a nil channel. ch is captured
// here, at call time, into the closure: the cmd itself runs off the loop in its
// own goroutine and must never touch model state directly. A closed channel (the
// watcher stopped) yields no message rather than a bogus refresh.
func waitStateChangeCmd(ch <-chan struct{}) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		if _, ok := <-ch; ok {
			return msgStateChanged{}
		}
		return nil
	}
}

// msgStateChanged signals that another process wrote state.json. It is
// coalesced (fsnotify events collapse into a 1-buffered channel) and
// delivered at most as fast as the loop can re-arm waitStateChange.
type msgStateChanged struct{}

func (m *Model) viewHeight() int {
	// -3: the status and help rows, and the row of ground above the document.
	// That top row is a real row of the altscreen, so a budget that does not
	// count it is one row of overflow at the bottom.
	h := m.height - 3
	if m.mode == modeCompose {
		h -= m.ta.Height() + 2
	}
	// THE PATH PANE RESERVES NOTHING HERE: a centred body is spliced into rows
	// the finished frame already has (drawsCentredPanel).
	//
	// NOT drawsCentredPanel: a centred mode's body is SPLICED into rows the
	// finished frame already has (composeCentredPanel), so it costs the
	// document nothing and must reserve nothing. See that predicate.
	if (m.mode == modeConfirmApprove || m.mode == modeConflict || m.mode == modeConfirmDeleteThread || m.mode == modeSourceFault ||
		m.mode == modeConfirmDeletePlan || m.mode == modeKeys) && !drawsCentredPanel(m.mode) {
		h -= m.confirmPanelHeight()
	}
	if m.mode == modeRelocate {
		h -= m.relocatePanelHeight()
	}
	if h < 1 {
		h = 1
	}
	return h
}

func (m *Model) clampScroll() {
	if max := len(m.lines) - m.viewHeight(); m.scroll > max {
		m.scroll = max
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

// scrollMargin is the number of context lines ensureVisible leaves between
// a newly revealed target line and the viewport edge it scrolled to reveal,
// so jumping to a block or thread never lands it flush against the top or
// bottom edge. clampScroll still bounds the result, so a target near either
// end of the document is unaffected (no blank overscroll, no negative
// scroll) — the margin only ever pulls scroll toward the middle.
const scrollMargin = 3

func (m *Model) ensureVisible() {
	target := ui.FirstLineOfFocus(m.lines, m.docCursor())
	vh := m.viewHeight()
	// A viewport shorter than the margin would otherwise let the overshoot
	// scroll the target itself out of view — worse than doing nothing.
	// viewHeight() never returns less than 1, so margin never goes negative.
	margin := min(scrollMargin, vh-1)
	if target < m.scroll {
		m.scroll = target - margin
	}
	if target >= m.scroll+vh {
		m.scroll = target - vh + 1 + margin
	}
	m.clampScroll()
}

// moveCursor advances the focus by one row of the VISIBLE sequence: every block,
// and -- where a block is expanded -- each of its threads in turn between that
// block's line and the next. So down off a commented line steps INTO its first
// thread rather than over its whole card, and down off its last thread steps out
// to the next line. A collapsed block has no threads in the sequence: they are
// not on screen, and a focus that cannot be seen is the thing this model exists
// to remove.
//
// delta is a single step; nothing calls it with more.
func (m *Model) moveCursor(delta int) {
	if len(m.blocks) == 0 {
		return
	}
	if delta > 0 {
		m.stepForward()
	} else {
		m.stepBack()
	}
	m.rerender()
	m.ensureVisible()
}

// stepForward moves one row down the visible sequence: into this block's
// threads if it has any on screen, then on to the next block.
func (m *Model) stepForward() {
	if n := m.visibleThreads(m.cursor); n > 0 {
		switch t := m.focusedThread(); {
		case t == ui.NoThread:
			m.selected[m.cursor] = 0
			return
		case t+1 < n:
			m.selected[m.cursor] = t + 1
			return
		}
	}
	if m.cursor+1 < len(m.blocks) {
		m.cursor++
		m.selected[m.cursor] = ui.NoThread
	}
}

// stepBack is stepForward's mirror: out of this block's threads, then up to
// the previous block and in through the BOTTOM of its threads, so a walk down
// and back up passes the same rows in reverse.
func (m *Model) stepBack() {
	if t := m.focusedThread(); t != ui.NoThread {
		if t == 0 {
			m.selected[m.cursor] = ui.NoThread
		} else {
			m.selected[m.cursor] = t - 1
		}
		return
	}
	if m.cursor > 0 {
		m.cursor--
		if n := m.visibleThreads(m.cursor); n > 0 {
			m.selected[m.cursor] = n - 1
		} else {
			m.selected[m.cursor] = ui.NoThread
		}
	}
}

// visibleThreads is how many of a block's threads are on screen: none unless
// it is expanded, which is what keeps a collapsed card's threads out of the
// sequence moveCursor walks.
func (m *Model) visibleThreads(block int) int {
	if !m.expanded[block] {
		return 0
	}
	return len(m.views[block])
}

// pageScroll moves the scroll top by a full page (viewHeight-2 lines, so two
// lines of context carry over from the previous screen), clamps it, then
// moves the cursor to the first block whose first line is now visible at or
// below the new scroll top — the cursor never leaves the viewport a page
// jump lands on.
func (m *Model) pageScroll(dir int) {
	if len(m.blocks) == 0 {
		return
	}
	m.scroll += dir * m.pageSize()
	m.clampScroll()
	m.cursor = m.firstBlockAtOrBelow(m.scroll)
	m.rerender()
}

// pageSize is one page of scrolling: the viewport less two lines, so two lines of
// context carry over from the previous screen, floored at one.
func (m *Model) pageSize() int {
	if page := m.viewHeight() - 2; page > 1 {
		return page
	}
	return 1
}

// firstBlockAtOrBelow returns the lowest block index whose first rendered
// line is at or below scrollTop, or the last block if scrolling has reached
// the end of the document and no block's first line qualifies.
func (m *Model) firstBlockAtOrBelow(scrollTop int) int {
	for i := range m.blocks {
		if ui.FirstLineOf(m.lines, i) >= scrollTop {
			return i
		}
	}
	return len(m.blocks) - 1
}

// docCursor is where the reader is, for rendering: the block their keys act
// on and which of its threads is selected. selectedThread aims the WRITES at
// the same pair; this is what puts it on screen.
func (m *Model) docCursor() ui.Cursor {
	return ui.Cursor{Block: m.cursor, Thread: m.focusedThread()}
}

// selectedIndex is the only INDEXED reading of m.selected, and that is the whole
// of what it is for: an ABSENT entry is ui.NoThread -- the block's own line -- and
// no caller may be left to discover that for itself.
//
// THE MAP'S ZERO VALUE IS 0, A REAL THREAD. Only THREAD gestures write this map:
// jumpThread (n/N), ActCycleThread (tab), stepForward/stepBack walking into and
// out of a card, selectCreated after a post, and the two clearings that put the
// focus back on the block's line (ActToggleExpand, seekMatch). Plain cursor
// movement writes NOTHING -- ActTop (g), ActBottom (G), pageScroll and relocate's
// targetRelocate all move m.cursor and leave the block they land on with no entry
// at all -- so a bare read there answers "thread 0", a thread the reader made no
// gesture to choose, and one of the three writes selectedThread aims DESTROYS what
// it lands on. Reading the map through one function is what makes that unwritable
// rather than merely fixed.
func (m *Model) selectedIndex(block int) int {
	if i, ok := m.selected[block]; ok {
		return i
	}
	return ui.NoThread
}

// focusedThread is which of the cursor block's threads the focus rests on, or
// ui.NoThread for the block's own line. m.selected holds ui.NoThread for the line,
// and selectedIndex answers the same for a block with no entry at all -- see it
// for why nothing may take the map bare.
//
// IT REFUSES ONE THING selectedThread DOES NOT, deliberately. A thread focus on a
// block that is NOT EXPANDED answers as the line: the rows it named are not on
// screen to carry a rail. That is a statement about what can be DRAWN. The writes
// keep their aim there, because n onto a collapsed card is still the reader
// choosing that thread -- selectedThread holds that argument and the evidence.
//
// WHAT REACHES THAT ARM is n/N and tab landing a selection on a card that was
// never opened, and a stale entry: m.expanded is keyed by block index and is never
// rebuilt, so a reload that renumbers the document can leave a selection under a
// block that is not open. NOT a collapse stranding a focus that was inside one:
// ActToggleExpand clears the selection to the line on BOTH toggle directions.
func (m *Model) focusedThread() int {
	t := m.selectedIndex(m.cursor)
	if t == ui.NoThread || !m.expanded[m.cursor] {
		return ui.NoThread
	}
	if n := len(m.views[m.cursor]); t >= n {
		return n - 1 // the same floor under a stale index selectedThread applies
	}
	return t
}

// jumpThread is n/N: the next THREAD anywhere in the document, skipping every line
// that carries none. It is the same walk moveCursor makes, restricted to the rows
// that are threads -- so it steps through an expanded block's threads exactly as
// down does, and then crosses uncommented prose in one press instead of a hundred.
//
// THEY ARE NOT SYNONYMS FOR DOWN/UP, which the plan called for and which this
// tried. Aliasing them cost fast travel: n crosses a document to the next comment
// and nothing else does. What differs from down is only what happens at the END of
// a block's threads: down steps to the next LINE, n to the next THREAD.
//
// THE WALK IS A CYCLE AND HAS NO END: the press after the last thread is the
// FIRST thread, and the press before the first is the last. It used to stop
// dead and say so, which made g or a long walk back
// the price of every overshoot -- the same argument seekMatch's own wrap was
// settled on, now answered the same way for the threads. The wrap is SILENT for
// the reason recorded at noJumpTargets.
//
// A CYCLE OF ONE IS A FIXED POINT, and that is the ruling rather than a fallout:
// with a single thread in the document n and N land back on it and nothing
// happens, no movement and no sentence. A reader pressing n on the only comment
// there is has already arrived.
func (m *Model) jumpThread(dir int) {
	if n := m.visibleThreads(m.cursor); n > 0 {
		switch t := m.focusedThread(); {
		case t == ui.NoThread && dir > 0:
			m.selected[m.cursor] = 0
			m.rerender()
			m.ensureVisible()
			return
		case t != ui.NoThread:
			if next := t + dir; next >= 0 && next < n {
				m.selected[m.cursor] = next
				m.rerender()
				m.ensureVisible()
				return
			}
		}
	}
	// THE SCAN IS MODULAR, AND THAT IS THE WHOLE OF THE WRAP. It steps at most
	// len(m.blocks) times, so it visits every block exactly once and the LAST
	// candidate it considers is m.cursor itself -- which is what makes the wrap
	// fall out rather than being a second code path: running off the end of the
	// document and coming back to the block the reader is already on are the same
	// walk, and the arrival below does not care which one happened.
	//
	// THE MODULO IS WRITTEN TWICE BECAUSE Go's % KEEPS THE SIGN OF ITS LEFT
	// OPERAND: p from block 0 computes -1, and a bare -1 % n is -1 rather than
	// n-1. The +n and the second % are what make it an index.
	//
	// blocks == 0 IS WHY THE BOUND IS A COUNT AND NOT A CONDITION: the loop simply
	// does not run, where a modulo against a zero length would divide by zero.
	for step, blocks := 1, len(m.blocks); step <= blocks; step++ {
		i := ((m.cursor+dir*step)%blocks + blocks) % blocks
		n := len(m.views[i])
		if n == 0 {
			continue
		}
		m.cursor = i
		// Entering forward lands on the first thread and backward on the
		// last, so n past a block and p back returns through the same threads.
		// A COLLAPSED block shows none of them, and focusedThread answers
		// NoThread there however this index is set -- the rail lands on the
		// line, which is the only row of that block on screen.
		if dir > 0 {
			m.selected[i] = 0
		} else {
			m.selected[i] = n - 1
		}
		m.rerender()
		m.ensureVisible()
		return
	}
	m.status = noJumpTargets
}

// noJumpTargets is the ONLY sentence n and N write, and it is reachable in one
// state alone: no block in the document carries a thread. Every other press is
// silent, including the wrap and including the single-thread cycle whose wrap
// lands back where it started -- jumpThread writing on a SUCCESSFUL jump would
// clobber the live-refresh flash that shares the row (see the walk above).
//
// IT IS A CLAIM ABOUT THE GESTURE AND NOT ABOUT THE DOCUMENT, deliberately. n
// and p walk threads PLACED ON BLOCKS; the unanchored section is drawn under
// UnanchoredIdx, outside the m.cursor range entirely, and is reached with
// relocate instead. The status bar's own count includes those (projection.
// threadCount), so a sentence saying the document has no threads would
// contradict a bar reading "2 threads" one row above it.
const noJumpTargets = "no threads to jump to"

// searchMatches is which blocks the active search hits, in document order. It is
// DERIVED on every ask and stored nowhere, which is the design and not an
// efficiency note: a match list is a slice of BLOCK INDICES, and an index means a
// different block the moment m.blocks stops being the document it was computed
// against -- every index still in range, so nothing crashes and the cursor simply
// lands somewhere else.
//
// A REFRESH IS NOT WHAT DOES THAT. RefreshFromSession re-parses m.sess.Content,
// which session.Open read once and no refresh re-reads (Rediscover re-checks plan
// IDENTITY, never the bytes), so the slice it rebuilds is block for block the one
// it replaced and a list held across it stays correct. What renumbers blocks is a
// SESSION REPLACEMENT -- handleActionDone's `m.sess = msg.reloaded`, from
// runReload (ctrl+r), runAcceptTip, runRepoint and runRelease. That is why
// TestNAfterAReloadLandsInTheDocumentTheModelNowHas drives ctrl+r rather than
// RefreshFromSession: a test built on the refresh would pass against a cached
// list.
func (m *Model) searchMatches() []int {
	return ui.SearchBlocks(m.blocks, m.searchQuery())
}

// jumpMatch is what n/N mean while a search is active: the next matching
// block strictly after the cursor (dir > 0) or strictly before it (dir < 0).
// updateRead picks between this and jumpThread on searchActive alone.
func (m *Model) jumpMatch(dir int) {
	m.seekMatch(dir, false)
}

// searchMissPrefix heads the one status line the search writes, and it is a
// constant so the places that TAKE IT BACK can recognize one without knowing
// which term it named -- by the time a miss stops being true the term has
// usually been edited, which is the whole reason it had to go.
const searchMissPrefix = "no matches for "

// clearSearchMiss drops a "no matches" line that has stopped being true.
//
// THE RULE, AND NOT A LIST OF THE SITES: every keystroke that moves the search off
// the term this sentence names must call this. That is stated as a rule because
// the list is what went stale -- a landing said "both places that falsify it now
// clear it" while updateSearch's two exits, added later, falsified it and
// cleared nothing, so a term escaped or backspaced away left its verdict
// standing with no key left that could answer it.
//
// IT IS DELIBERATELY NOT `m.status = ""`. The other thing that writes the bar on
// the very same keystroke is maybeApplyPendingRefresh's "review updated", which
// updateSearch's enter arm runs one line before the landing, and a blanket clear
// would eat news the reader has no other way to see. This clears the search's own
// sentence and nothing else's; seekMatch is its only writer.
func (m *Model) clearSearchMiss() {
	if strings.HasPrefix(m.status, searchMissPrefix) {
		m.status = ""
	}
}

// seekMatch moves the review cursor to a match, WRAPPING at either end. The wrap is
// silent: a search that stopped dead at the last hit would make g the price of every
// second n. includeCursor is enter's landing rather than n's; updateSearch's enter
// arm is the only caller that passes true.
//
// NO MATCHES MOVES NOTHING and says so. The RAW term is quoted, so the reader is
// shown the characters they typed rather than the folded, trimmed form the match
// was made on (searchQuery).
//
// NOTHING IS REPORTED FOR A TERM THAT IS NO SEARCH, which is what the guard below
// says and not "a term nobody has typed": it is keyed on searchActive, so a term of
// three spaces -- typed, visible on the input row, and no search at all -- is as
// silent as an empty one. Without it, enter over an empty line answers `no matches
// for ""`.
//
// IT COMPARES INDICES AND NEVER SUBSCRIPTS m.blocks, which is a requirement and not
// a style: the review cursor is not clamped to the document, so it can be past the
// end when this runs, and every landing here is an index SearchBlocks itself
// returned. A list of the ways a cursor gets out of range is the shape that goes
// stale, so the producers are deliberately not enumerated.
func (m *Model) seekMatch(dir int, includeCursor bool) {
	if !m.searchActive() {
		return
	}
	hits := m.searchMatches()
	if len(hits) == 0 {
		m.status = fmt.Sprintf("%s%q", searchMissPrefix, m.search)
		return
	}
	// hits is ascending. The wrap is the DEFAULT and a hit past the cursor
	// overwrites it, so falling out of either walk is what wrapping is.
	target := hits[0]
	if dir < 0 {
		target = hits[len(hits)-1]
	}
	if dir > 0 {
		for _, i := range hits {
			if i > m.cursor || (includeCursor && i == m.cursor) {
				target = i
				break
			}
		}
	} else {
		for k := len(hits) - 1; k >= 0; k-- {
			if hits[k] < m.cursor || (includeCursor && hits[k] == m.cursor) {
				target = hits[k]
				break
			}
		}
	}
	m.cursor = target
	// A LANDING TAKES BACK A MISS: `no matches for "penguin"` names a term the
	// reader has since edited away. clearSearchMiss's own declaration holds the rule
	// and the whole set of calls it requires.
	m.clearSearchMiss()
	// The rail lands on the BLOCK's own line, never on a thread the reader
	// stepped into on an earlier visit -- ActToggleExpand's handler carries
	// this same repair with its reason (see it), and selectedThread is what
	// answers r/R/d over the state the two of them leave behind.
	m.selected[m.cursor] = ui.NoThread
	m.rerender()
	m.ensureVisible()
}

func (m *Model) CursorBlock() int { return m.cursor }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ta.SetWidth(m.width - 4)
		m.pathTA.SetWidth(m.width - 4)
		m.rerender()
		m.ensureVisible()
		return m, nil
	case msgRefreshed:
		// Runs on the loop: RefreshFromSession mutates model state, which is
		// only ever safe to do here, never inside a cmd's goroutine.
		if err := m.RefreshFromSession(attributedCtx()); err != nil {
			m.err = err
			m.status = "error: " + err.Error()
		} else if n := len(m.unresolvedOrphans()); n > 0 {
			// msgRefreshed fires only from Init, so this is a startup-only flash:
			// reload and live refresh keep their own statuses. A status already
			// standing at startup -- Root's snapshot notice -- is appended to, not
			// clobbered.
			flash := fmt.Sprintf("%d orphaned — %s to relocate", n, findKey(m.km, keymap.ActRelocate))
			if m.status != "" {
				m.status += " · " + flash
			} else {
				m.status = flash
			}
		}
		return m, nil
	case msgStateChanged:
		// Idle read mode is the only moment a refresh is unconditionally
		// safe: compose has a live draft it must not clobber, and an
		// in-flight write owns the next projection via handleActionDone.
		// Both other cases defer via pendingRefresh instead. Either way the
		// wait re-arms, so a later state change is never missed.
		if m.mode == modeRead && !m.inFlight {
			before := m.factsFingerprint()
			if err := m.RefreshFromSession(attributedCtx()); err != nil {
				m.err = err
				m.status = "error: " + err.Error()
			} else {
				// This refresh subsumes any deferral still pending from an earlier
				// state change: clear it so no later seam re-refreshes for state
				// already projected here. Only on success -- an error must leave the
				// flag set so a later seam retries instead of forfeiting it.
				m.pendingRefresh = false
				// Only flash (and reposition) when the projection actually changed.
				// Every save the TUI itself makes triggers this same watch, and
				// handleActionDone has already projected and flashed that write's own
				// confirmation by the time the echo arrives here; an unconditional
				// flash would clobber it with a generic message about nothing.
				if after := m.factsFingerprint(); after != before {
					m.status = "review updated"
					m.ensureVisible()
				}
			}
		} else {
			m.pendingRefresh = true
		}
		return m, m.waitStateChange()
	case msgActionDone:
		return m.handleActionDone(msg)
	case editorDoneMsg:
		return m.handleEditorDone(msg)
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			// ctrl+c is the unconditional kill switch, ahead of mode dispatch: it
			// quits the whole program from every mode and whether embedded in Root
			// or standalone, deliberately not rebindable and not mode-gated. It must
			// run before ta.Update ever sees the keypress -- compose's textarea must
			// never absorb it as input -- and before quit()'s embedded in-flight
			// refusal, which would otherwise leave the program with no keyboard exit
			// at all. Process exit makes any dropped write result moot.
			return m, tea.Quit
		}
		switch m.mode {
		case modeRead:
			return m.updateRead(msg)
		case modeKeys:
			// Routed here directly rather than through updatePanel (actions.go),
			// which is every OTHER panel mode's door: modeKeys' whole handler is
			// two literals, and giving it its own case keeps the mode, its entry,
			// its exit and its content declaration all in this one file -- see
			// modeKeys' own doc comment.
			return m.updateKeys(msg)
		default:
			return m.updatePanel(msg)
		}
	case tea.MouseWheelMsg:
		return m.updateMouseWheel(msg)
	case tea.MouseClickMsg:
		return m.updateMouseClick(msg)
	case tea.PasteMsg:
		return m.updatePaste(msg)
	}
	return m, nil
}

// updatePaste answers the bracketed paste bubbletea emits as tea.PasteMsg. Until
// this case existed nothing in Update's switch matched it, so the message was
// dropped before bubbles' own textarea -- which DOES handle tea.PasteMsg -- ever
// saw one, and a pasted path left the field empty.
//
// TWO FIELDS, TWO RULES. The path pane pastes a PATH into m.pathTA and
// modeCompose pastes PROSE into m.ta, and what each does with a newline is the
// opposite of the other -- see collapseCRLF beside sanitizePastedPath. The gate is
// still the MODE rather than the widget, so every other mode stays paste-blind.
//
// ⚠️ modeCompose WAS OMITTED RATHER THAN REFUSED, and the record says so. The
// bug this handler was written for was "a pasted PATH never reached the field",
// so the fix was scoped to the two re-point panes and the composer was only ever
// driven as a scope guard. Meanwhile the composer itself had rejected
// ctrl+v as its editor key to avoid "stealing paste inside the one box whose job
// is composing prose" -- protecting a capability that was not there. It then
// surfaced the ordinary way: quoting the comment you are replying to is what a
// reader pastes, and nothing happened.
//
// ⚠️ ctrl+v IS STILL BROKEN AND THIS DOES NOT FIX IT. bubbles binds ctrl+v to its
// own Paste command, which reads the clipboard and answers textarea.pasteMsg --
// an UNEXPORTED type, so Update's switch cannot match it and it falls out of the
// bottom exactly as tea.PasteMsg used to. Reaching it means intercepting ctrl+v
// here and reading the clipboard ourselves, which promotes
// github.com/atotto/clipboard from indirect to direct; left until something
// needs it. Terminal paste -- the gesture a reader actually makes -- is this one.
func (m *Model) updatePaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	if m.mode == modeCompose {
		var cmd tea.Cmd
		m.ta, cmd = updateComposePaste(m.ta, msg)
		return m, cmd
	}
	if m.mode != modeRepoint {
		return m, nil
	}
	// SANITIZING, UPDATING AND REPORTING WHETHER THE VALUE CHANGED are
	// updateRepointPaste's own (app/actions.go), shared with ListModel.updatePaste.
	//
	// SAME EDIT-CLEARS-THE-REFUSAL RULE AS EVERY OTHER KEY IN THE PANE: a paste is
	// the most likely edit this pane will ever see, and skipping the rule for it
	// would leave a stale refusal under the very gesture this exists to fix.
	var cmd tea.Cmd
	var changed bool
	m.pathTA, cmd, changed = updateRepointPaste(m.pathTA, msg)
	if changed {
		m.repointRefusal = ""
	}
	return m, cmd
}

func (m *Model) updateRead(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "esc" {
		// esc TURNS OFF A SEARCH, and it is matched here ahead of the lookup because the
		// lookup returns for an unbound key and esc is unbound -- a case in the switch
		// below could never run. The trade updateBrowse's own esc names holds here too:
		// an action a keymap.json rebinds onto esc is shadowed.
		//
		// IT MOVES NOTHING, unlike ListModel.clearFilter: a search narrowed nothing, so
		// there is nothing to re-aim the cursor into. The gate is searchActive rather
		// than m.search != "", so a term of pure whitespace is left in hand for the next
		// f to edit rather than thrown away by a key that cancelled nothing. THE TWO
		// ESCS DIFFER BECAUSE THEIR SUBJECTS DO: updateSearch's cancels an EDIT and drops
		// whatever is in hand, while this turns off a SEARCH.
		//
		// It also takes back the search's own status line (clearSearchMiss).
		if m.searchActive() {
			m.search = ""
			m.clearSearchMiss()
		}
		return m, nil
	}
	action, ok := m.km[msg.String()]
	if !ok {
		return m, nil
	}
	switch action {
	case keymap.ActQuit:
		return m.quit()
	case keymap.ActList:
		return m.showList()
	case keymap.ActMoveDown:
		m.moveCursor(1)
	case keymap.ActMoveUp:
		m.moveCursor(-1)
	case keymap.ActTop:
		m.cursor = 0
		m.scroll = 0
		m.rerender()
	case keymap.ActBottom:
		if len(m.blocks) > 0 {
			m.cursor = len(m.blocks) - 1
			m.rerender()
			m.ensureVisible()
		}
	case keymap.ActScrollDown:
		m.scroll++
		m.clampScroll()
	case keymap.ActScrollUp:
		m.scroll--
		m.clampScroll()
	case keymap.ActPageDown:
		m.pageScroll(1)
	case keymap.ActPageUp:
		m.pageScroll(-1)
	case keymap.ActNextThread, keymap.ActPrevThread:
		// ONE BRANCH FOR BOTH KEYS, so n and N cannot drift into meaning different
		// subjects: while a search is on they walk the MATCHES, otherwise the
		// THREADS, and the choice is written once.
		//
		// THESE TWO ACTIONS ALREADY CARRY A THIRD MEANING -- modeRelocate dispatches
		// them to skip orphans -- and the three are deliberately NOT unified: that
		// site is reached through updatePanel and never through here.
		dir := 1
		if action == keymap.ActPrevThread {
			dir = -1
		}
		if m.searchActive() {
			m.jumpMatch(dir)
		} else {
			m.jumpThread(dir)
		}
	case keymap.ActToggleExpand:
		if len(m.views[m.cursor]) > 0 {
			m.expanded[m.cursor] = !m.expanded[m.cursor]
			// ENTER puts the card on screen; it does not move the focus into it.
			// Without this the focus REAPPEARS wherever it was left -- n stores a
			// thread index on arrival, and focusedThread hides it only while the block
			// is collapsed -- so opening a card would jump the rail into it rather than
			// leaving the reader to step in. THIS IS ENTER'S RULE, NOT A RULE ABOUT
			// EVERY CARD THAT OPENS: see selectCreated for why a post is a different
			// gesture rather than this rule being broken.
			m.selected[m.cursor] = ui.NoThread
			m.rerender()
			m.ensureVisible()
		}
	case keymap.ActCycleThread:
		// A SAME-BLOCK cycle, kept beside n/N's document-wide walk. n/N reach
		// every thread and do not wrap; this wraps within the block and never
		// leaves it, which is what lets a caller say "thread 3 of THIS block"
		// without needing to know what precedes it.
		if n := len(m.views[m.cursor]); n > 1 {
			// THROUGH selectedIndex, not the map: a bare read on a block the
			// reader has never made a thread gesture on answers 0, so the
			// first tab landed on thread TWO and skipped the one the reader
			// was looking at. ui.NoThread + 1 is 0, which is thread one.
			next := (m.selectedIndex(m.cursor) + 1) % n
			m.selected[m.cursor] = next
			m.status = fmt.Sprintf("thread %d/%d selected", next+1, n)
			m.rerender()
		}
	case keymap.ActRelocate:
		m.enterRelocate()
	case keymap.ActSearch, keymap.ActFilter:
		// TWO ACTIONS, TWO KEYS, ONE HANDLER: f arrives as ActSearch and "/" as
		// ActFilter, and both mean the same thing here. keymap.Map is
		// map[string]Action, so one keystroke carries one action for the whole
		// program and "/" is already the plan list's filter -- binding the search to
		// it would have taken the list's door away rather than adding one.
		m.enterSearch()
	case keymap.ActKeys:
		// Reads nothing, writes nothing: NO dispatchOK gate, enterRelocate's and
		// enterSearch's own precedent just above -- opening a reference panel
		// cannot race a write in flight, and a write's own msgActionDone can
		// still land and seize the mode out from under this one exactly as it
		// can while a search or a triage queue is open.
		m.enterKeysPanel()
	default:
		return m.dispatchWrite(action)
	}
	return m, nil
}

// quit is ActQuit's decision: embedded (Root's active review child) hands control
// back to the list via msgCloseReview instead of ending the whole program.
// Standalone is tea.Quit, including with a write still in flight, where process
// exit makes the dropped result moot. Embedded is different: the program keeps
// running, so closing mid-write would strand the write's msgActionDone on the
// list (which has no case for it) -- an error silently lost, and a success whose
// list refresh raced the write itself. Refuse via the standard dispatchOK
// discipline instead; writes resolve quickly and the very next q lands, while
// ctrl+c remains the unconditional way out.
func (m *Model) quit() (tea.Model, tea.Cmd) {
	if !m.embedded {
		return m, tea.Quit
	}
	if !m.dispatchOK() {
		return m, nil
	}
	return m, func() tea.Msg { return msgCloseReview{} }
}

// showList is ActList's decision: l always emits msgCloseReview, regardless of
// embedded. Under Root this switches the active child from review back to the
// list; on a standalone Model the message has no case in Update and is simply
// dropped -- harmless, since there is no list to switch to. Deliberately not gated
// on embedded: unlike quit(), which must choose between two different exits,
// showList only ever means "go to the list", and a standalone Model swallowing it
// unchanged is the fall-through TestMsgCloseReviewUnhandledOnBareModel pins.
func (m *Model) showList() (tea.Model, tea.Cmd) {
	if !m.dispatchOK() {
		return m, nil
	}
	return m, func() tea.Msg { return msgCloseReview{} }
}

func (m *Model) View() tea.View { return m.viewPainted() }

// statusIdentity is the status bar's leading segment: WHICH document is on
// screen and what is on it.
func (m *Model) statusIdentity() string {
	title := m.title
	if !m.planExists {
		// No plan yet: infer the same title the lazy-create path will use,
		// so the status line never flashes a placeholder before matching
		// the real title once the plan exists.
		title = ui.InferTitle(m.blocks, m.sess.Path)
	}
	return fmt.Sprintf("%s · block %d/%d · %s", title, m.cursor+1, len(m.blocks), pluralCount(m.threadCount(), "thread"))
}

// pluralCount renders a count with its noun in the regular English plural --
// "1 plan"/"1 thread", never "1 plans"/"1 threads" -- the status bar's two
// live counts share. An irregular noun, like deleteThreadConfirmText's own
// reply/replies, stays its own switch rather than taking this helper.
func pluralCount(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// helpHint is one mode's help bar. Both models in this package hold their hints in a
// map keyed by their own mode enum, and the shape is shared, which is why this is
// generic over the model rather than written twice.
//
// THE UNIT IS A RENDERED LINE, not a list of keymap actions, and that is the decision
// the whole mechanism turns on. Most modes claim at least some of their keys as
// LITERAL STRINGS -- y, n, esc, ctrl+d, matched against msg.String() -- and
// no keymap.Action names what they do there. There is no ActAccept and no ActCancel,
// so findKey, which answers "?" when it finds no action, cannot build ONE clause of
// a confirm panel's bar. THAT SOME OF THOSE KEYSTROKES ARE BOUND ELSEWHERE IS THE
// TRAP, not the way out: n is ActNextThread in read mode, so a lookup could produce
// the right character for the wrong reason and go on producing it after a rebind.
//
// lit is the whole line for a mode whose bar is a constant; derive is for a line that
// depends on the model. The two split on "does it vary" and not on "is it literal":
// relocate's enter is a literal and its line still is not a constant
// (relocateHintText swaps that clause on a block with nothing to anchor to).
//
// EXACTLY ONE IS SET, and the zero value is the safe default: a mode nobody
// registered has no lit and no derive, so line answers "" and the bar goes silent
// instead of falling back to read mode's. "The defect is not a missing hint -- it is
// a wrong one. Silence is fine; misinformation is not."
type helpHint[M any] struct {
	lit    string
	derive func(M) string
}

// line renders this hint for m, or "" for the zero value -- the silence a mode
// that declared nothing gets.
func (h helpHint[M]) line(m M) string {
	if h.derive != nil {
		return h.derive(m)
	}
	return h.lit
}

// confirmHint is the help bar every y/n confirm panel in this package shows: the
// key that acts, named with the gesture it acts on, then anything else the panel
// takes, then the two that back out. ONE COMPOSER RATHER THAN ONE STRING PER
// PANEL, because the panels differ in one word and hand-written lines would each
// be a chance to name a key the panel does not take -- which is the defect this
// whole mechanism exists for, at the granularity of a literal. A panel that
// answers no y at all is NOT one of the callers and must not become one.
//
// y, n and esc are literals, not keymap lookups, because that is what the handlers
// match: naming a rebindable key there would advertise a keystroke no panel
// answers to.
func confirmHint(verb string) string {
	return "y " + verb + " · n/esc cancel"
}

const (
	// composeHint is modeCompose's help-bar line, and only that now: the panel's
	// own hint strip was deleted, replacing the words that used to name post and
	// cancel there with the button row itself (composeButtonRowPainted,
	// app/painted.go). ONE CONSTANT FOR TWO PLACES was the old discipline --
	// listSortHint's own comment still needs it, because a modal's footer and the
	// help bar under it are both WORDS naming the same keys, and two spellings of
	// one mode's keys are how they come to disagree. That hazard RETIRES HERE
	// RATHER THAN BEING VIOLATED: a button is the control itself, not a second
	// spelling of a key that could drift from the first, so there is no longer a
	// second place for "esc cancel" to agree with.
	composeHint = "esc cancel"

	// conflictHint is modeConflict's, and it names what each answer DOES rather than
	// saying yes/no/cancel: the panel asks a two-armed question where neither answer
	// is the safe one, so a bar reading "y accept · n/esc cancel" would flatten the
	// choice the panel exists to pose. The words are that question's own.
	conflictHint = "y fetch and re-anchor · n register on top · esc cancel"

	// searchHint names modeSearch's two exits and nothing else -- enter ACCEPTS (the
	// term is kept, read mode takes the keyboard back), esc CLEARS. Naming one would
	// leave the other undiscoverable, which is filterHint's ruling in the list. It is
	// rendered in ONE place, the help bar: the input row directly above carries the
	// query alone (searchInputLine), and naming the exits on both drew them twice,
	// one line apart.
	searchHint = "enter search · esc cancel"

	// sourceFaultHint is modeSourceFault's, and it is NOT confirmHint's line: this
	// panel answers no y at all -- it asks a human which way out they want, not
	// whether they are sure. It names ALL FOUR
	// keys the handler answers, each clause having landed in the same commit as its
	// handler, which is "a hint may name fewer keys than its mode owns and may NEVER
	// name one its handler does not answer" (modeHints) spent rather than stated.
	//
	// ITS ORDER IS THE PANEL BODY's, not the handler's: a reader who has just read
	// four key lines finds the bar naming them in the same order.
	sourceFaultHint = "f different file · o no file · d delete · esc plan list"
)

// keysHint is modeKeys' own help-bar line: the one key this mode's handler
// answers besides esc, named through findKey because ActKeys is rebindable
// like everything else on this bar -- unlike composeHint's literal, whose keys
// are matched ahead of the keymap and can never move.
func keysHint(km keymap.Map) string {
	return fmt.Sprintf("esc/%s close", findKey(km, keymap.ActKeys))
}

// modeHints is the review model's help bar: one entry per mode, and the whole of
// what the bottom line may say.
//
// A TABLE RATHER THAN A SWITCH WITH A FALLBACK. Until it, helpBar answered every
// mode but one with read mode's own line -- so every panel this package has ever
// added advertised comment/reply/resolve/approve/quit while a y/n prompt held the
// keyboard, and each new mode inherited that silently. Here the default is silence
// (helpHint's zero value), so a mode added next year cannot inherit a wrong line
// at all: it gets none until somebody says what its keys are, and the
// exhaustiveness test over mode(0)..modeCount-1 fails until they do.
var modeHints = map[mode]helpHint[*Model]{
	modeRead: {derive: func(m *Model) string {
		return helpLine(m.km, len(m.unresolvedOrphans()), m.searchState(), m.blockHint())
	}},
	modeCompose:             {lit: composeHint},
	modeConfirmApprove:      {lit: confirmHint("approve")},
	modeRelocate:            {derive: (*Model).relocateHintText},
	modeConflict:            {lit: conflictHint},
	modeConfirmDeleteThread: {lit: confirmHint("delete")},
	modeSearch:              {lit: searchHint},
	modeSourceFault:         {lit: sourceFaultHint},
	modeConfirmDeletePlan:   {lit: confirmHint("delete")},
	modeRepoint:             {lit: repointPaneHint},
	modeKeys:                {derive: func(m *Model) string { return keysHint(m.km) }},
}

// helpBar is the hint bar for whichever mode owns the keyboard, or nothing at
// all for a mode that has not said what its keys are -- see modeHints.
func (m *Model) helpBar() string {
	return modeHints[m.mode].line(m)
}

// noContentAtCursor is the ruled sentence for a write aimed at a block with no
// words to anchor to (ui.Anchorable). It is ONE CONSTANT because it is said in one
// voice about one fact: the status both block-aimed writes set when they refuse
// (app/actions.go), and the clause both of their help bars show in place of the
// key they would otherwise advertise (blockHints, below).
const noContentAtCursor = "no content at cursor"

// blockHints is what a hint says IN PLACE OF THE CLAUSE NAMING THE KEY THAT AIMS A
// WRITE AT THE BLOCK UNDER THE CURSOR, while the cursor rests on a block of that
// kind. It is modeHints' mechanism one level in and for the same reason: a TABLE
// whose ZERO VALUE IS SILENCE, so a kind added next year says nothing here rather
// than inheriting a clause somebody wrote about a different construct.
//
// TWO BARS READ IT, one clause each, because there are two block-aimed writes and
// they are refused together: read mode's "c comment" (helpLine) and relocate's
// "enter place here" (relocateHintText). ONLY THOSE CLAUSES ARE SUBSTITUTED, which
// is the narrow claim this table makes: they name the keys their modes REFUSE on a
// rule, and helpLine's own rule -- no hint may assert a key its handler rejects --
// is what obliges the swap.
//
// NOTHING WRAPS ON ACCOUNT OF THIS: the bar is ansi.Truncate'd to the row it is
// painted in, so length past the visible width costs clause tails, never a row.
var blockHints = map[ui.BlockKind]string{
	ui.KindRule: noContentAtCursor,
}

// blockHint is blockHints' entry for the block under the cursor, and "" when the
// cursor is not on a block at all -- the same silence the table's zero value gives,
// for a state no BlockKind can be keyed on. It is DERIVED at every repaint like
// searchState beside it, and like that one it refuses to subscript a cursor the
// document may have shrunk out from under.
//
// SILENCE IS RIGHT FOR THE DOCUMENT WITH NO BLOCKS and merely tolerable for the
// cursor past the end of one, which is the honest way round to say it. On an empty
// document c is answered by a DIFFERENT sentence ("document has no content
// blocks", dispatchWrite), so a bar carrying this one would name a refusal the
// reader will not be given. Past the end of a non-empty document -- rare, and
// repaired by the next move or search landing -- the bar goes on offering c while
// dispatchWrite refuses it. Recorded rather than fixed: the alternative is a clause
// keyed on something other than a kind, which is a second mechanism for one
// transient state.
func (m *Model) blockHint() string {
	b, ok := m.cursorBlock()
	if !ok {
		return ""
	}
	return blockHints[b.Kind]
}

func (m *Model) threadCount() int {
	return projection{views: m.views, unanchored: m.unanchored}.threadCount()
}

// threadCount counts every thread a projection puts on screen, placed or
// unanchored. On the projection rather than on the Model because the status bar
// asks it of two documents.
func (p projection) threadCount() int {
	n := len(p.unanchored)
	for _, vs := range p.views {
		n += len(vs)
	}
	return n
}

// findKey looks up the display keystroke for an action in km. It is
// deterministic across repaints despite Go's randomized map iteration:
// shortest keystroke wins, ties broken lexicographically. It is findAllKeys'
// own first element -- the SAME rule, kept as one function rather than two so
// the bar (which wants one key) and the "?" panel (which wants all of them)
// cannot drift onto different tie-breaks.
func findKey(km keymap.Map, a keymap.Action) string {
	keys := findAllKeys(km, a)
	if len(keys) == 0 {
		return "?"
	}
	return keys[0]
}

// findAllKeys is findKey's own search, kept whole instead of collapsed to a
// single winner: the "?" panel's whole point is naming EVERY key that reaches
// an action, not the one findKey would pick to save a cell on the help bar.
// Sorted shortest-first, ties broken
// lexicographically -- findKey's own rule, stated once here and read by it
// rather than duplicated.
func findAllKeys(km keymap.Map, a keymap.Action) []string {
	var keys []string
	for k, act := range km {
		if act != a || len(k) > 6 {
			continue
		}
		keys = append(keys, k)
	}
	sortKeysShortestFirst(keys)
	return keys
}

// sortKeysShortestFirst is findKey's tie-break rule, applied to a whole slice
// rather than folded one key at a time into a running "best": shortest wins,
// equal lengths break lexicographically ('P' sorts before 'p' in ASCII).
func sortKeysShortestFirst(keys []string) {
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) < len(keys[j])
		}
		return keys[i] < keys[j]
	})
}

// helpLine is modeRead's own hint bar, derived from the live keymap so rebinds show
// up.
//
// orphanCount gates the "relocate" segment: the m key stays live (with its own "no
// orphaned threads" status) even with nothing to triage, but advertising it with
// nothing to relocate would invite a no-op keypress.
//
// AN ACTIVE SEARCH SWAPS ONE CLAUSE FOR ANOTHER, one out and one in, so the bar
// names n and N as exactly what updateRead will do with them. This model's hardest
// rule is that no hint may assert a key its handler rejects, and its corollary is
// that a WRONG hint is worse than a missing one.
//
// blockHint SWAPS THE COMMENT CLAUSE THE SAME WAY: dispatchWrite REFUSES c on a
// block with nothing to anchor to, so a bar offering "c comment" there asserts a key
// its handler rejects. It is a string and not a ui.BlockKind because the KIND has no
// value meaning "the cursor is on no block" -- its zero value is a real kind,
// KindHeading -- and this bar is derived over a cursor that can be past the end.
//
// THERE IS NO MOVE SEGMENT: j/k are vim's own motions and need this bar least
// of anything on it.
//
// "? keys" LEADS THE BAR, ahead of everything else -- see this function's own
// leading comment for why the front and not the tail.
//
// THE WIDTHS, MEASURED by rendering helpLine's own output through ansi.Truncate
// at painted.go's real barWidth arithmetic (m.width - 2*ui.RailWidth(m.width)):
// "shift select" first shows intact at a terminal width of 101, and "reload" at
// 117. At 80 the bar cuts inside "a approve".
func helpLine(km keymap.Map, orphanCount int, search searchState, blockHint string) string {
	// "? keys" LEADS THE BAR, unconditionally -- present whatever search or
	// orphan state the rest of the line is in. This bar
	// clips at 80 columns (see this function's own doc for where), and a
	// segment naming the panel that answers every OTHER segment on this line
	// is the one that must never be the one truncation reaches: appended at
	// the tail it would be invisible on exactly the narrow terminal where a
	// lost reader needs it most.
	segs := []string{fmt.Sprintf("%s keys", findKey(km, keymap.ActKeys))}
	if search.on() {
		segs = append(segs, search.clauses(km)...)
	}
	// THE CLAUSE THE SEARCH REPLACES. n and N route through ONE branch in
	// updateRead, which picks the matches over the threads on searchActive
	// alone -- so with a search on, "threads" names the subject the handler
	// will NOT give those keys.
	if !search.on() {
		segs = append(segs, fmt.Sprintf("%s/%s threads", findKey(km, keymap.ActNextThread), findKey(km, keymap.ActPrevThread)))
	}
	comment := fmt.Sprintf("%s comment", findKey(km, keymap.ActComment))
	if blockHint != "" {
		comment = blockHint
	}
	segs = append(segs,
		fmt.Sprintf("%s expand", findKey(km, keymap.ActToggleExpand)),
		comment,
		fmt.Sprintf("%s reply", findKey(km, keymap.ActReply)),
		fmt.Sprintf("%s resolve", findKey(km, keymap.ActToggleResolve)))
	if orphanCount > 0 {
		segs = append(segs, fmt.Sprintf("%s relocate", findKey(km, keymap.ActRelocate)))
	}
	// THE TAIL IS TWO GROUPS AND THE BOUNDARY IS THE SUBJECT: Everything up to
	// and including "shift select" acts on THIS PLAN -- move within it, write on
	// it, approve it, select
	// out of it, re-read it. "list" and "quit" LEAVE it, and they are the only two
	// that do, so list sits beside quit rather than beside approve. That is what
	// moved "shift select" ahead of "list": selecting text is an act on the
	// document under the cursor, not a way out of it.
	segs = append(segs,
		fmt.Sprintf("%s approve", findKey(km, keymap.ActApprove)),
		// A LITERAL, AND THE ONLY SEGMENT ON THIS BAR THAT IS NOT DERIVED FROM A BINDING.
		// shift is not a keymap.Action and never can be: it names the TERMINAL's own
		// bypass of the mouse reporting this view turns on (stampMouseMode), so a reader
		// who rebinds every key in keymap.json must still hold shift.
		//
		// IT IS HERE BECAUSE THE BYPASS IS UNDISCOVERABLE: a reader who tries to select
		// and gets nothing concludes selection is impossible rather than modified. Naming
		// it is the whole remedy -- the capture stays.
		//
		// IT IS DELIBERATELY A WIDE-TERMINAL SEGMENT: it first appears at the width
		// helpLine's own doc names, and at 80 the bar already cuts before it (see
		// searchState.clauses, which records that pre-existing clip and declines to
		// repair it). Ruled acceptable because nobody is held to 80 columns and q is
		// the key a reader reaches for without being told.
		"shift select",
		// THE RELOAD GESTURE BELONGS HERE. It is the one key that changes THE
		// DOCUMENT'S OWN BYTES on screen: WatchState (app/watch.go) watches
		// state.json, so review facts an agent writes arrive unaided, but it does
		// not watch the plan file -- an agent that rewrites the PLAN leaves the
		// reader on stale text with nothing naming the way forward. Recovering
		// from that is possible only through this key, so the bar has to say it.
		//
		// IT IS A WIDE-TERMINAL SEGMENT TOO, AND THAT IS DISCLOSED RATHER THAN FIXED:
		// measured, it first appears at the width helpLine's own doc names, so an
		// 80-column reader still cannot see it and the gesture is still learned some
		// other way there.
		// Moving it left of the writes would buy that back at the cost of the grouping
		// above; the bar is over-budget as a whole, and thinning it is its own decision
		// rather than this clause's to take.
		fmt.Sprintf("%s reload", findKey(km, keymap.ActReload)),
		fmt.Sprintf("%s list", findKey(km, keymap.ActList)),
		fmt.Sprintf("%s quit", findKey(km, keymap.ActQuit)))
	return strings.Join(segs, " · ")
}

// searchState is everything read mode's help bar knows about an active search, and
// it is a PARAMETER rather than a field because it is DERIVED at every repaint
// (Model.searchState below). at and of are positions in the list searchMatches
// returns, and that function's own declaration is a standing ruling that such a
// list may never be held: a block index means a different block the moment
// m.blocks stops being the document it was computed against. A bar that cached its
// own count would be one more holder of exactly that.
//
// A ZERO VALUE IS "NO SEARCH", answered by on() below, so the test call sites that
// are not about the search say searchState{} and mean it.
type searchState struct {
	// term is m.search VERBATIM -- the characters the reader pressed, not the folded
	// and trimmed form the match was made on (searchQuery). clauses QUOTES it, which
	// is not decoration: the term is arbitrary text on a line whose segments are
	// joined by " · ", so an unquoted `a · b` forges a segment boundary, an unquoted
	// `/bucket` reads as `//bucket`, and an unquoted `  Penguin  ` leaves three
	// spaces where the reader cannot see the term end and the verdict begin.
	term string
	// at is the cursor's 1-BASED position among the matches, or 0 when the
	// cursor is not on one at all. That is an ordinary state and not an error:
	// a reader lands on a match and then walks off it with j, and the bar has
	// to say something true about where they now are.
	at int
	// of is len(searchMatches()) -- how many BLOCKS the term hits, which is the
	// same unit n and N step in.
	of int
}

// on reports whether there is a search to describe, and it answers from the term
// ALONE so that one field does the work without a boolean beside it -- the ruling
// Model.search already carries for the stored form.
//
// IT TRIMS, and that is load-bearing rather than tidy. searchActive keys on
// searchQuery, which trims, so a term of pure whitespace is TYPED, VISIBLE on the
// input row, KEPT by enter -- and no search at all, with updateRead still routing
// n and N to jumpThread. A bar that asked only whether the string was empty would
// answer that state with `/"   " no matches · esc clear` over a keyboard that
// still walks threads. Model.searchState's guard would hide it today, but a clause
// that needs a caller's guard to stay honest is one edit from lying.
func (s searchState) on() bool { return strings.TrimSpace(s.term) != "" }

// clauses is the search's half of the bar, and it LEADS the line rather than trailing
// it -- ListModel.hintLine's filter clause is the precedent, and the reason is the
// same measured one: read mode's bar already runs past the visible width of an
// 80-column terminal, cutting it off inside "a approve" (helpLine's own doc has
// the measurement), so appending would put the state of the thing the reader is
// doing in the part of the row nobody can see. That pre-existing clip is
// RECORDED AND NOT REPAIRED.
//
// n/N ARE NAMED THROUGH THE SAME ACTIONS THE THREAD CLAUSE USES, because they are the
// same two bindings, so a rebind moves both spellings together and neither can be a
// literal.
//
// WITH NO MATCHES AT ALL, THE n/N CLAUSE NAMES AN EMPTY SET AND GOES. The reason is
// NOT that the keys are inert -- they answer, and this file names inert keys
// elsewhere -- it is about REFERENCE: "n/N match" names a SET, and with no matches
// the set is empty. AT EXACTLY ONE MATCH THE CLAUSE STAYS, cursor on it or not:
// there n wraps to itself, which is the documented silent wrap working, and dropping
// it at "1/1" would make the clause FLICKER -- shown at "1 match" while the cursor
// is off the lone hit, gone the instant n lands on it.
func (s searchState) clauses(km keymap.Map) []string {
	found := "no matches"
	switch {
	case s.at > 0:
		found = fmt.Sprintf("%d/%d", s.at, s.of)
	case s.of == 1:
		found = "1 match"
	case s.of > 1:
		found = fmt.Sprintf("%d matches", s.of)
	}
	out := []string{fmt.Sprintf("/%q %s", s.term, found)}
	if s.of > 0 {
		out = append(out, fmt.Sprintf("%s/%s match", findKey(km, keymap.ActNextThread), findKey(km, keymap.ActPrevThread)))
	}
	// esc is a LITERAL and not a findKey lookup: updateRead matches it ahead of
	// the keymap (see it), so no action can be looked up to spell it.
	return append(out, "esc clear")
}

// searchState derives the bar's view of the search from the live model, and stores
// nothing. The cursor's position among the matches is found by comparing indices,
// never by subscripting m.blocks -- the review cursor is not clamped to the
// document (seekMatch says why), so it can be past the end while this runs.
//
// THE searchActive GUARD SAVES A SCAN AND NOTHING ELSE. It is not what makes the
// bar honest about a whitespace term -- on() trims for itself -- so weakening it to
// `m.search == ""` costs a wasted SearchBlocks over a query that cannot hit, not a
// wrong line.
//
// helpBar is derived at every repaint, so while a search is on this walks the whole
// document once per frame: about 250x the no-search bar, and still sub-millisecond
// at the size a plan actually is (BenchmarkReadHelpBar). DERIVING IT IS THE RIGHT
// CALL and the ruling at searchState's own declaration stands: a cached count is a
// held list of block indices, and searchMatches says what those cost when m.blocks
// moves under them.
func (m *Model) searchState() searchState {
	if !m.searchActive() {
		return searchState{}
	}
	hits := m.searchMatches()
	s := searchState{term: m.search, of: len(hits)}
	for k, i := range hits {
		if i == m.cursor {
			s.at = k + 1
			break
		}
	}
	return s
}

// keysRow is one line of a "?" keys panel: the keymap action(s) it names --
// almost always one, occasionally two or more whose keys are conventionally
// shown together the way the bar already shows n/N and g/G -- and what
// pressing them does, in this model's own words. Shared by both models'
// panels (app/list.go's is the same type), because the shape of "a key, or
// several, and a description" does not differ between them; only which
// actions each one owns does.
//
// EVERY KEY IS DERIVED THROUGH findAllKeys, same tie-break findKey's own
// segments on the bar use, but the WHOLE set rather than findKey's single
// pick: a row that showed only one of two live bindings would be silently
// lying about a rebind that added the other one. literalKeys is for the keystrokes no
// derivation can answer because they are not actions at all (shift, and this
// very panel's own esc, matched ahead of the keymap lookup exactly as it is
// everywhere else in this package) -- not a shortcut around deriving the
// ones that can be.
//
// keyOrder EXISTS FOR EXACTLY THE ROWS pure derivation cannot order: the
// movement row's "j/k ↑/↓" interleaves ActMoveDown{j,down} and
// ActMoveUp{k,up} by KEY STYLE (vim pair, then arrow pair) rather than by
// which action each key answers. It is a DISPLAY preference over the SAME
// derived set, not a second source of truth for it: see keys'
// own doc comment for how a keystroke keyOrder does not know about still
// reaches the row rather than vanishing, and TestExplicitKeyOrderCoversTheDerivedSet
// (app/keys_test.go) for what pins keyOrder itself against Default() so it
// cannot go stale unnoticed.
//
// rawLine IS THE ONE ESCAPE HATCH, and it exists for exactly the ONE row
// findKey's own doc comment already carves out an exception for: "shift
// select" is a literal on the bar because shift is not an action and never
// can be, and it is a literal here for the identical reason.
//
// blank IS A ROW THAT ISN'T ONE: the pinned copy specifies one blank line INSIDE
// the review panel's NAVIGATION group (between the viewport-motion rows and
// the thread-reading rows), a sub-grouping distinct from keysPanelText's own
// blank line BETWEEN groups. Giving it a row of its own, rather than a
// special case in keysGroup.render, is what lets that render method stay one
// loop over rows with no row-index arithmetic naming "after the fifth row".
type keysRow struct {
	literalKeys []string
	actions     []keymap.Action
	keyOrder    []string
	does        string
	rawLine     string
	blank       bool
}

// derivedKeys is every keystroke km currently binds to one of r's own
// actions, in NO particular display order (findAllKeys' own per-action order,
// concatenated action by action) -- the set keys (below) is answerable for
// covering exactly, whatever keyOrder does or does not say.
func (r keysRow) derivedKeys(km keymap.Map) []string {
	var keys []string
	for _, a := range r.actions {
		keys = append(keys, findAllKeys(km, a)...)
	}
	return keys
}

// keys is the row's own keys in DISPLAY order: literalKeys first (they are
// not derived from anything), then either derivedKeys as-is (the ordinary
// case: concatenated per action in the order this row declares them, which is
// already correct for every row but the two keyOrder names) or keyOrder's own
// preferred arrangement of that SAME set, via applyKeyOrder.
func (r keysRow) keys(km keymap.Map) []string {
	keys := append([]string{}, r.literalKeys...)
	if r.keyOrder != nil {
		keys = append(keys, applyKeyOrder(r.keyOrder, r.derivedKeys(km))...)
	} else {
		keys = append(keys, r.derivedKeys(km)...)
	}
	return keys
}

// applyKeyOrder arranges derived (a row's own derivedKeys) by order where
// order names a key, and appends whatever derived contains that order does
// NOT name -- shortest-then-lexicographic, findAllKeys' own fallback --
// rather than dropping it. THE APPEND IS THE WHOLE POINT: order is a fixed
// list written against TODAY's keymap.Default(), and without the fallback a
// keymap.json that rebinds ActMoveUp off k and up entirely would make its new
// keystroke vanish from the panel rather than merely show up out of its usual
// place -- the one failure this panel cannot afford. order naming a key
// derived does not currently contain is simply never emitted; it is not an
// error; a stale order and a live rebind agree here without either being
// told about the other.
func applyKeyOrder(order, derived []string) []string {
	live := make(map[string]bool, len(derived))
	for _, k := range derived {
		live[k] = true
	}
	out := make([]string, 0, len(derived))
	for _, k := range order {
		if live[k] {
			out = append(out, k)
			delete(live, k)
		}
	}
	var leftover []string
	for k := range live {
		leftover = append(leftover, k)
	}
	sortKeysShortestFirst(leftover)
	return append(out, leftover...)
}

// glyphKey renders a keystroke for display: "up" and "down" are how tea's key
// parser names the arrow keys, and the pinned copy revision renders them as the
// arrow glyphs they are (↑, ↓) rather than the four-letter names, which read
// as prose next to single letters like j and k. Every other keystroke is
// unchanged. Applied only to keys() -- never to literalKeys' raw text or
// rawLine, neither of which names an arrow key.
func glyphKey(k string) string {
	switch k {
	case "up":
		return "↑"
	case "down":
		return "↓"
	default:
		return k
	}
}

// render is one row's own text, Panel 1's "key · what it does" shape
// (sourceFaultText) rather than the bar's "key verb": this is a panel body,
// not a bar, and the front-of-bar placement is a different segment entirely
// (helpLine's own "? keys").
//
// THE SEPARATOR IS joinKeyGroups' OWN RULE, reversing an earlier
// "|"-everywhere draft: "/" between the two keys of a pair, a SPACE between
// one pair and the next on the same row. See
// joinKeyGroups' own doc comment for why "|" is gone rather than merely
// swapped for "/" everywhere.
func (r keysRow) render(km keymap.Map) string {
	if r.blank {
		return ""
	}
	if r.rawLine != "" {
		return r.rawLine
	}
	keys := r.keys(km)
	display := make([]string, len(keys))
	for i, k := range keys {
		display[i] = glyphKey(k)
	}
	return joinKeyGroups(display) + " · " + r.does
}

// joinKeyGroups joins a row's own display keys (already in display order --
// see keys' own doc comment) two at a time: "/" inside a pair, a SPACE
// between pairs. keys() already HANDS BACK its keys grouped this way --
// action-declaration order for an ordinary two-key row, or keyOrder's own
// arrangement for the movement row -- so grouping here is
// purely positional, no action or intent inspected again. A trailing single
// key (never true of any row the pinned copy specifies, but possible if a
// keyOrder's leftover fallback hands back an odd count after a rebind)
// stands alone rather than dangling a "/" onto nothing.
//
// "/" IS NOT A ROW-WIDE SEPARATOR, and that is deliberate, not merely a
// style choice: "/" is itself a live keystroke (ActFilter). A row that
// joined ALL its keys with "/" and also happened to name "/" as one of them
// would print it doubled ("f//"), unreadable as anything but a typo. That is
// exactly why the pinned copy gives the review search actions TWO ROWS --
// "f · search" and "/ · search" -- rather than one row naming both keys: the
// pairing rule below assumes neither key in a pair IS the separator
// character, and search is the one place in either panel that assumption
// would break.
func joinKeyGroups(keys []string) string {
	var groups []string
	for i := 0; i < len(keys); i += 2 {
		end := i + 2
		if end > len(keys) {
			end = len(keys)
		}
		groups = append(groups, strings.Join(keys[i:end], "/"))
	}
	return strings.Join(groups, " ")
}

// keysGroup is one named cluster of rows, grouped by "what the
// reader is trying to do" rather than the order actions were declared in
// keymap.go.
type keysGroup struct {
	heading string
	rows    []keysRow
}

// render is the heading (upper-cased, to read as a section label against the
// plain rows under it) followed by each row's own line -- a blank row's own
// render answers "", which is exactly the sub-grouping line the pinned copy
// wants inside a group, with no special case here for it.
func (g keysGroup) render(km keymap.Map) []string {
	lines := make([]string, 0, len(g.rows)+1)
	lines = append(lines, strings.ToUpper(g.heading))
	for _, r := range g.rows {
		lines = append(lines, r.render(km))
	}
	return lines
}

// keysPanelText is both panels' shared assembly: a headline, then every
// group with one blank line ahead of it. A blank line in m.confirm is its
// own logical line to wrapConfirmGroups (app/list.go) -- an empty group of
// one empty row -- which is what buys the blank row centredBox draws between
// sections, the SAME mechanism every other confirm body's "\n\n" already
// spends for prose, spent here for structure instead.
func keysPanelText(km keymap.Map, headline string, groups []keysGroup) string {
	lines := []string{headline}
	for _, g := range groups {
		lines = append(lines, "")
		lines = append(lines, g.render(km)...)
	}
	return strings.Join(lines, "\n")
}

// movementKeyOrder is j/k/↑/↓'s own display order, shared by both panels'
// movement row (reviewKeyGroups below, listKeyGroups in app/list.go): the vim
// pair first, then the arrow pair, matching how each style already reads
// elsewhere in this program ("j/k", not "j/↓") rather than grouped by which
// of ActMoveDown/ActMoveUp each key answers -- grouped by action would
// interleave as j, down, k, up, since Default() binds "down" to ActMoveDown
// and "up" to ActMoveUp. Pure derivation cannot produce this order (see
// keysRow.keyOrder's own doc comment); TestExplicitKeyOrderCoversTheDerivedSet
// (app/keys_test.go) pins it against keymap.Default() so it cannot silently
// stop covering what ActMoveDown/ActMoveUp actually bind.
var movementKeyOrder = []string{"j", "k", "up", "down"}

// reviewKeyGroups is the review model's OWN declaration of what it answers,
// and the one place the "?" panel's content is written down: every action
// updateRead's switch, dispatchWrite's switch and modeRelocate's own case
// answer. THREE GROUPS: NAVIGATION (moving and reading threads: everything that
// only looks), REVIEW (writing review facts) and EXIT.
//
// ⚠️ THIS IS A DECLARED LIST, NOT A DERIVED ONE, and that is worth being
// honest about rather than calling it something it is not: Go cannot walk a
// switch statement's own case labels at runtime, so nothing SHORT of
// rewriting updateRead/dispatchWrite as data rather than code could make this
// table impossible to typo. What stands in for that is
// TestReviewKeysPanelMatchesActualDispatch (app/keys_test.go), which presses
// every keymap.Action at a live *Model from modeRead and fails the moment
// this table disagrees with what actually happens.
//
// THE BLANK ROW INSIDE NAVIGATION IS A DELIBERATE SUB-GROUPING, not padding:
// it separates the viewport's own motion (move, jump, scroll, page, the
// mouse's shift+drag) from reading the document's threads (next/prev thread
// onward). See keysRow.blank's own doc comment for why it is a row and not a
// special case in the render loop.
var reviewKeyGroups = []keysGroup{
	{
		heading: "navigation",
		rows: []keysRow{
			{actions: []keymap.Action{keymap.ActMoveDown, keymap.ActMoveUp}, keyOrder: movementKeyOrder, does: "next/prev block"},
			{actions: []keymap.Action{keymap.ActTop, keymap.ActBottom}, does: "jump to top/bottom"},
			{actions: []keymap.Action{keymap.ActScrollDown, keymap.ActScrollUp}, does: "scroll"},
			{actions: []keymap.Action{keymap.ActPageDown, keymap.ActPageUp}, does: "page"},
			{rawLine: "shift + drag · select text"},
			{blank: true},
			{actions: []keymap.Action{keymap.ActNextThread, keymap.ActPrevThread}, does: "next/prev thread"},
			{actions: []keymap.Action{keymap.ActToggleExpand}, does: "expand/collapse thread"},
			// TWO ROWS, NOT ONE, FOR ONE HANDLER: updateRead answers ActSearch
			// ("f") and ActFilter ("/") from the SAME case body (keymap.go's own
			// doc comment on ActSearch) -- doing the identical thing is not a
			// reason to name them on one row, because joinKeyGroups' pairing rule
			// cannot join a "/"-keyed pair without printing "/" twice (see its own
			// doc comment). The pinned copy rules this exact duplication -- "f ·
			// search" and "/ · search" both present, worded identically -- rather
			// than collapsing it away.
			{actions: []keymap.Action{keymap.ActSearch}, does: "search"},
			{actions: []keymap.Action{keymap.ActFilter}, does: "search"},
			{actions: []keymap.Action{keymap.ActCycleThread}, does: "cycle thread in this block"},
			{actions: []keymap.Action{keymap.ActReload}, does: "reload"},
		},
	},
	{
		heading: "review",
		rows: []keysRow{
			{actions: []keymap.Action{keymap.ActComment}, does: "comment"},
			{actions: []keymap.Action{keymap.ActReply}, does: "reply"},
			{actions: []keymap.Action{keymap.ActToggleResolve}, does: "resolve/unresolve"},
			{actions: []keymap.Action{keymap.ActDelete}, does: "delete thread"},
			{actions: []keymap.Action{keymap.ActRelocate}, does: "relocate an orphaned thread"},
			{actions: []keymap.Action{keymap.ActApprove}, does: "approve"},
		},
	},
	{
		heading: "exit",
		rows: []keysRow{
			{actions: []keymap.Action{keymap.ActList}, does: "back to the plan list"},
			{actions: []keymap.Action{keymap.ActQuit}, does: "quit"},
			{literalKeys: []string{"esc"}, actions: []keymap.Action{keymap.ActKeys}, does: "close this panel"},
		},
	},
}

// reviewKeysText is modeKeys' body in this model, built once at entry
// (enterKeysPanel) from the live keymap.
//
// "Key Actions", NOT "Keys in this view:" -- the pinned copy revision drops
// the earlier headline (and its trailing colon) in both panels; see
// listKeysText's own identical change.
func reviewKeysText(km keymap.Map) string {
	return keysPanelText(km, "Key Actions", reviewKeyGroups)
}

// enterKeysPanel opens modeKeys: no dispatchOK gate (see its ActKeys case in
// updateRead), no status to clear -- a reference panel reads nothing and
// writes nothing, enterSearch's and enterRelocate's own shape.
func (m *Model) enterKeysPanel() {
	m.confirm = reviewKeysText(m.km)
	m.mode = modeKeys
}

// updateKeys answers modeKeys' two keys: esc, and ActKeys again
// (found through the keymap, not matched as the literal "?", since it is
// rebindable like everything else) toggle the panel shut. Both return to
// modeRead, exactly like every other panel's cancel.
func (m *Model) updateKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "esc" || m.km[msg.String()] == keymap.ActKeys {
		m.mode = modeRead
		m.maybeApplyPendingRefresh()
	}
	return m, nil
}

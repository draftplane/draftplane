package app

import (
	"errors"
	"io/fs"

	tea "charm.land/bubbletea/v2"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/keymap"
	"github.com/draftplane/draftplane/session"
	"github.com/draftplane/draftplane/theme"
)

// Root is draftplane's top-level program: the plan list and a review session are
// one Bubbletea program, not two separate binaries glued together. Root owns
// the switch between them and the single watcher wait-loop both share —
// children are always constructed with a nil stateChanged channel (their own
// waitStateChange is then a permanent no-op), and Root forwards
// msgStateChanged to whichever child is active, re-arming its own wait after
// every delivery.
type Root struct {
	svc client.PlanService
	km  keymap.Map
	th  *theme.Theme
	// pseudonym is handed to every review Model Root constructs (New,
	// NewEmbedded) -- see Model's own field comment.
	pseudonym string

	// recentPath is where this machine's recent.json lives, threaded to the
	// list (SetRecents) and to every review this Root builds -- see SetRecents
	// for how a review already active picks it up too. Empty disables
	// recording entirely (RecordOpen and recordOpenCmd both no-op on "").
	recentPath string

	list     *ListModel
	review   *Model
	inReview bool

	// listStarted is whether the list's Init has been dispatched: by Init for a
	// list-first Root, by the first closeReview for one NewRootInReview started on
	// a review. closeReview reads it to tell the list's first load, which only
	// ListModel.Init starts (awaiting, the cursor's pin), from a return to a list
	// that has already loaded, which refreshes.
	listStarted bool

	stateChanged <-chan struct{}

	// lastSize is the most recent tea.WindowSizeMsg Root has seen; haveSize is
	// false until the first one arrives. On every switch, the incoming child is
	// replayed lastSize before it's rendered -- WindowSizeMsg otherwise only
	// reaches whichever child was active when it arrived.
	lastSize tea.WindowSizeMsg
	haveSize bool

	// openSeq guards against out-of-order open results, the same dispatch-order
	// discipline as ListModel.refreshSeq: the list dispatches msgOpenPlan on
	// every enter with no in-flight latch, and bubbletea gives no ordering
	// guarantee on whose msgSessionOpened lands first. Each dispatch stamps the
	// next openSeq; handleSessionOpened accepts only the newest dispatch's
	// result, so a stale, slower open can never clobber the review the user is
	// already in.
	openSeq uint64
}

// NewRoot builds Root with the list as its initial active child.
func NewRoot(svc client.PlanService, km keymap.Map, th *theme.Theme, pseudonym string, stateChanged <-chan struct{}) *Root {
	return &Root{
		svc:          svc,
		km:           km,
		th:           th,
		pseudonym:    pseudonym,
		list:         NewList(svc, km, th),
		stateChanged: stateChanged,
	}
}

// SetRecents points Recently opened at this machine's recent.json, forwarding
// to the list (ListModel.SetRecents) and, when NewRootInReview has already
// built one, to the active review too, since a review built before this call
// must still receive it.
func (r *Root) SetRecents(path string) {
	r.recentPath = path
	r.list.SetRecents(path)
	if r.review != nil {
		r.review.recentPath = path
	}
}

// NewRootInReview builds Root with a review already active -- `draftplane review
// <path>`, opened directly rather than reached through the list. The list child
// is built (NewList) but deliberately not Init'd here -- see Init -- so it stays
// at construction defaults until first shown.
//
// The review is built with New, not NewEmbedded: embedded stays false, so q
// still ends the whole program for this entry point. l reaches the list anyway,
// and reentering any plan from there goes through handleSessionOpened's
// ordinary NewEmbedded path, so THAT review's q returns to the list. The
// asymmetry is deliberate and permanent for the lifetime of a review instance.
func NewRootInReview(svc client.PlanService, km keymap.Map, th *theme.Theme, pseudonym string, stateChanged <-chan struct{}, sess *session.Session) *Root {
	r := NewRoot(svc, km, th, pseudonym, stateChanged)
	r.review = New(sess, km, th, pseudonym, nil)
	r.inReview = true
	// The same sentence handleSessionOpened stamps at the list's door, said here
	// for the CLI's -- one fact, one wording, two doors. Without it,
	// `draftplane review <plan id>` on a plan with a file would present the
	// registered version with nothing on screen to say the file is not being
	// followed: session.OpenByID always opens that version and latches no fault.
	if sess.FromSnapshot() {
		r.review.status = snapshotOpenStatus(sess.Path)
	}
	return r
}

// msgSessionOpened carries the result of the off-loop open seam dispatched
// for msgOpenPlan back to Root. seq is the openSeq the dispatch was stamped
// with — see Root.openSeq's doc comment.
type msgSessionOpened struct {
	seq  uint64
	sess *session.Session
	err  error

	// fromFileRow is an open dispatched from a Recently opened file row
	// (msgOpenFile), the one door whose missing file is answered in
	// fileNotFoundStatus's words.
	fromFileRow bool
}

// msgCloseReview is emitted by the review model's quit paths when it is
// Root's embedded child: Root discards the review model and switches back
// to the list rather than ending the program.
type msgCloseReview struct{}

// Init initializes only the active child, mirroring updateActive's branch:
// whichever child Root did NOT start on is left uninitialized until the user
// actually switches to it (closeReview Inits the list at that point -- see
// listStarted). Init'ing both unconditionally would cost every `draftplane
// review <path>` launch a wasted list derivation (an O(4·plans) walk) the user
// may never see.
func (r *Root) Init() tea.Cmd {
	var active tea.Cmd
	if r.inReview {
		active = r.review.Init()
	} else {
		r.listStarted = true
		active = r.list.Init()
	}
	cmds := []tea.Cmd{active}
	if wait := r.waitStateChange(); wait != nil {
		cmds = append(cmds, wait)
	}
	return tea.Batch(cmds...)
}

// waitStateChange returns a cmd that blocks for the next signal on Root's
// channel, or nil if live refresh is disabled. Children are always built with a
// nil channel (their own method is then a permanent no-op), so this is the one
// copy that reads the real watch. Shares waitStateChangeCmd with Model's.
func (r *Root) waitStateChange() tea.Cmd {
	return waitStateChangeCmd(r.stateChanged)
}

// updateActive dispatches msg to whichever child is currently active,
// installing its returned model back onto Root.
func (r *Root) updateActive(msg tea.Msg) tea.Cmd {
	if r.inReview {
		m, cmd := r.review.Update(msg)
		r.review = m.(*Model)
		return cmd
	}
	m, cmd := r.list.Update(msg)
	r.list = m.(*ListModel)
	return cmd
}

func (r *Root) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		r.lastSize = msg
		r.haveSize = true
		return r, r.updateActive(msg)
	case msgOpenPlan:
		// Only the list emits this, so one arriving while a review is already
		// active is a queued straggler -- dispatching it would eventually
		// replace the review the user is now interacting with.
		if r.inReview {
			return r, nil
		}
		return r, r.openPlanCmd(msg)
	case msgOpenFile:
		// Same staleness argument as msgOpenPlan just above: only the list emits
		// this, so one that arrives once a review is already active is a queued
		// straggler.
		if r.inReview {
			return r, nil
		}
		return r, r.openFileCmd(msg)
	case msgSessionOpened:
		return r, r.handleSessionOpened(msg)
	case msgCloseReview:
		return r, r.closeReview()
	case msgStateChanged:
		// Forward to the active child so it does its own refresh (its own
		// waitStateChange is a no-op -- nil channel -- so this is the only path
		// that reaches it), then re-arm Root's own wait regardless of what the
		// child returned, so a later change is never missed.
		return r, tea.Batch(r.updateActive(msg), r.waitStateChange())
	default:
		return r, r.updateActive(msg)
	}
}

// openPlanCmd runs the open seam off the loop, dispatching session.OpenPlan -- the
// one function all three doors onto a plan share -- and handing whatever it answers
// back through msgSessionOpened. The rule for what an open attempts, and in what
// order, lives in session.OpenPlan's own doc comment.
//
// What this door owns is everything around that call: the openSeq stamp, the
// attributed context, and the two shapes handleSessionOpened can receive -- a
// session, whose SourceFault may name what its file did, or an error.
func (r *Root) openPlanCmd(msg msgOpenPlan) tea.Cmd {
	r.openSeq++
	seq := r.openSeq
	svc := r.svc
	plan := msg.plan
	ctx := attributedCtx()
	return func() tea.Msg {
		sess, err := session.OpenPlan(ctx, svc, plan)
		return msgSessionOpened{seq: seq, sess: sess, err: err}
	}
}

// openFileCmd is openPlanCmd's shape for a Recently opened file row:
// session.Open in place of session.OpenPlan, since there is no domain.Plan to
// hand it.
func (r *Root) openFileCmd(msg msgOpenFile) tea.Cmd {
	r.openSeq++
	seq := r.openSeq
	svc := r.svc
	path := msg.path
	ctx := attributedCtx()
	return func() tea.Msg {
		sess, err := session.Open(ctx, svc, path)
		return msgSessionOpened{seq: seq, sess: sess, err: err, fromFileRow: true}
	}
}

// snapshotOpenStatus names, on the status line, WHY the review the user just opened
// is not following a file -- the notice handleSessionOpened (the list's door) and
// NewRootInReview (the CLI's) stamp on every session that came back FromSnapshot.
//
// A SOURCELESS PLAN READS "reviewing from draftplane"; every other snapshot open
// reads "reviewing snapshot (no local copy)". For a plan an agent handed
// draftplane the bytes of over MCP the second sentence misleads twice: "snapshot"
// reads as a frozen copy of something else, and "no local copy" implies an
// absence that could be filled. It is the same distinction planItem.sourceLabel already
// draws in the plan list's status bar (and also in "Your plans"' own SOURCE
// column), and drawing it there and not here is what left the two screens
// contradicting each other.
//
// THE CONDITION IS THE EMPTY SOURCE HINT ALONE. A plan WITH a source comes back
// FromSnapshot, and so reads "reviewing snapshot (no local copy)", in exactly
// three cases:
//
//   - session.OpenPlan fell back to the registered version because the plan's
//     file is gone, will not read, or no longer resolves to the plan. The fault
//     is latched and Panel 1 is raised over the document. Only a gone file has
//     no local copy; the other two are still on disk.
//   - The source is a URL draftplane cannot read: os.ReadFile fails, and
//     session.SourceFileFault latches nothing for a URL, so there is no panel.
//   - `draftplane review <plan id>`: session.OpenByID always opens the registered
//     version, so a plan reads "no local copy" even while its file is on disk,
//     and this line is the only sign that the file is not being followed.
//
// path, not the plan's own SourceHint, only because they are the same value here by
// construction (session.OpenVersion sets Path from plan.SourceHint).
func snapshotOpenStatus(path string) string {
	if path == "" {
		return "reviewing from draftplane"
	}
	return "reviewing snapshot (no local copy)"
}

// fileNotFoundStatus is what enter on a Recently opened file row says
// when the file is not there. The open is still tried, since a file put back
// since the last refresh must open; this replaces only the read error it failed
// with, which the bar clipped before its reason, and names the key that clears
// the row. Any other read failure keeps its "error: " line.
func fileNotFoundStatus(km keymap.Map) string {
	return "file not found · " + findKey(km, keymap.ActDelete) + " to clear"
}

// handleSessionOpened installs the freshly opened session as Root's active
// review child on success, or leaves the list active with an error status
// on hard failure (no local copy AND no registered version either).
func (r *Root) handleSessionOpened(msg msgSessionOpened) tea.Cmd {
	// A stale result from a superseded dispatch -- the user pressed enter again
	// before this open completed. Drop it before even looking at err, so a slow
	// open can neither clobber the newer review nor smear its error on the list.
	if msg.seq != r.openSeq {
		return nil
	}
	// The list moved on while this open was in flight: a rename panel is open
	// (switching would discard a half-typed draft and leave the list stuck in
	// panel mode on return), or a rename/delete write is running (switching would
	// strand its msgListActionDone on the review, permanently wedging
	// list.inFlight). The enter is stale intent by now -- drop the result; the
	// plan opens again on the next enter.
	if r.list.mode != listBrowse || r.list.inFlight {
		return nil
	}
	if msg.err != nil {
		r.list.err = msg.err
		if msg.fromFileRow && errors.Is(msg.err, session.ErrSourceUnreadable) && errors.Is(msg.err, fs.ErrNotExist) {
			r.list.status = fileNotFoundStatus(r.km)
			return nil
		}
		r.list.status = "error: " + msg.err.Error()
		return nil
	}
	review := NewEmbedded(msg.sess, r.km, r.th, r.pseudonym, nil)
	review.recentPath = r.recentPath
	if msg.sess.FromSnapshot() {
		review.status = snapshotOpenStatus(msg.sess.Path)
	}
	r.review = review
	r.inReview = true

	// The repaint owed at every view switch -- see closeReview for why it is
	// owed and what it does not buy.
	cmds := []tea.Cmd{tea.ClearScreen, review.Init(), recordOpenCmd(r.recentPath, msg.sess)}
	if r.haveSize {
		cmds = append(cmds, r.updateActive(r.lastSize))
	}
	return tea.Batch(cmds...)
}

// closeReview discards the review model and returns to the list, refreshing it
// -- the facts it shows (thread counts, approval tip, activity sort) likely
// changed while reviewing.
//
// A LIST SHOWN FOR THE FIRST TIME IS Init'D INSTEAD (listStarted). A Root that
// NewRootInReview started on a review never Init'd it, and a bare refresh would
// draw its construction-time rows -- "No plans yet!" and "0 plans" -- until the
// answer landed. Init carries its own load, which removes that claim.
//
// A LIST STILL ON ITS FIRST LOAD IS ANSWERED BY THE REFRESH BELOW: its first
// answer, if it arrived while the review was active, was handed to the review
// and dropped, and this refresh's answer ends the wait in its place.
func (r *Root) closeReview() tea.Cmd {
	r.review = nil
	r.inReview = false

	// ⚠️ THE REPAINT IS A CONTAINMENT AND NOT A FIX, and it is owed at BOTH
	// switches. Bubbletea renders through a cell differ that emits only cells
	// whose MODEL value changed, so a terminal painting a grapheme cluster in
	// more cells than any ruler answers -- Zellij paints U+1F468 U+200D U+1F469
	// U+200D U+1F467 in 6 where every ruler we have says 2 -- leaves glyphs the
	// model has no record of, and NO LATER FRAME EVER ADDRESSES THEM. They
	// outlive the document and land on the plan list.
	//
	// ESC[2J ERASES BY THE TERMINAL'S COLUMNS RATHER THAN BY OUR BELIEFS ABOUT
	// THEM, which is what makes it the one instrument here immune to a width
	// disagreement -- the same argument that makes ESC[K the construction-general
	// answer to the right margin, still unspent and left for later.
	//
	// WHAT IT DOES NOT DO is make the document view render correctly. The
	// disagreement is Zellij's, it is disclosed rather than closed, and a
	// document containing such a cluster still paints wrong WHILE IT IS OPEN.
	// This bounds the blast radius to the view that caused it. Measured base
	// rate over a corpus of 74 real plans: 0 occurrences.
	//
	// IT IS SPENT AT A SCENE CHANGE THE USER ALREADY EXPECTS, which is what
	// keeps one wasted full frame from being felt. Do not reach for it on an
	// ordinary redraw.
	cmds := []tea.Cmd{tea.ClearScreen}
	if r.listStarted {
		cmds = append(cmds, r.list.refreshCmd())
	} else {
		r.listStarted = true
		cmds = append(cmds, r.list.Init())
	}
	if r.haveSize {
		cmds = append(cmds, r.updateActive(r.lastSize))
	}
	return tea.Batch(cmds...)
}

func (r *Root) View() tea.View {
	if r.inReview {
		return r.review.View()
	}
	return r.list.View()
}

package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/client/recent"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/keymap"
	"github.com/draftplane/draftplane/placement"
	"github.com/draftplane/draftplane/reanchor"
	"github.com/draftplane/draftplane/session"
	"github.com/draftplane/draftplane/ui"
)

// msgActionDone carries a write cmd's result back to the loop. All model
// mutation implied by the write (installing a reloaded session, expanding a
// block, re-projecting) happens in handleActionDone, never in the cmd
// itself.
type msgActionDone struct {
	status string
	err    error

	// expandBlock is the block to auto-expand on success, or -1 for none.
	// It is captured by the dispatching call site at dispatch time (the
	// cursor as it stood then), not read back off the model later.
	expandBlock int

	// selectTID is the thread this write CREATED, for handleActionDone to aim
	// expandBlock's focus at once the refresh has placed it. It is the one fact on
	// this message the dispatching call site CANNOT capture, because it does not
	// exist until the write returns -- which is why the comment post has its own
	// dispatch wrapper (runComposeAction). Empty for every other write.
	selectTID domain.ThreadID

	// reloaded is the freshly opened session from a reload write; nil for
	// every other write.
	reloaded *session.Session
	// fromReload is true only for reload's result, gating the auto-expand
	// of re-anchored (badged) threads to reload alone, not every write.
	fromReload bool

	// fromRelocate is true only for a relocate-mode write (dispatchRelocate,
	// dispatchRelocateResolve), gating handleActionDone's auto-advance step
	// to that flow alone.
	fromRelocate bool
	// nextTID is the successor orphan to advance to, captured at dispatch
	// time by successorTID — see its doc comment for why it can't be
	// recomputed here.
	nextTID domain.ThreadID

	// planDeleted is true only for the ONE write in this model that destroys the
	// thing the model is about: modeConfirmDeletePlan's y (runDeletePlan). It
	// exists because handleActionDone's success path ends in RefreshFromSession and
	// there is nothing left to refresh FROM -- that read would fail and paint a read
	// error over a write that worked perfectly. This flag lets the success path
	// leave early rather than teaching the refresh a special case.
	planDeleted bool

	// retry is the write closure that produced THIS result, carried on the message
	// itself rather than a model-global field. handleActionDone only ever adopts
	// msg.retry into m.pendingRetry for the conflict that message itself carries: a
	// model-global field set eagerly at dispatch stays armed across a write that
	// failed WITHOUT conflicting, and an unrelated later conflict would then adopt a
	// stale closure. runAction sets it on every call (harmless for a write that can
	// never conflict -- errors.As simply never matches); runRelocateAction leaves it
	// nil, since modeRelocate's queue-advance is a different problem (see
	// handleActionDone's fromRelocate coaching instead).
	retry func(context.Context) (string, error)
	// retryExpand is expandBlock, duplicated onto the message under its own
	// name for retry's use: expandBlock itself is consumed unconditionally
	// by every result (a conflict must never expand anything), so a
	// SUBSEQUENT retry dispatch needs its own copy, read only from inside
	// modeConflict's decline handler.
	retryExpand int

	// fromRepoint is true for a re-point's result (runRepoint) AND for a release's
	// (runRelease) -- both write the plan's SourceHint through the identical
	// client.SetSourceHint call, one instant apart from a re-open through
	// session.OpenPlan, so the ONE thing this flag gates -- whether Panel 1 is still
	// owed -- is the identical question for both.
	//
	// THE OUTCOME ARM KEYS ON THE RELOADED SESSION'S FAULT, NEVER ON WHETHER THE
	// WRITE SUCCEEDED, and that is one rule covering two states a success/failure
	// test splits wrongly: a failed write leaves the OLD fault latched and the panel
	// owed; a SUCCEEDED re-point whose new file vanished before the re-open lands a
	// session back on the snapshot with a NEW fault latched, and the panel is owed
	// for that one too. Asking m.sess answers both, and it is the same question New
	// asks.
	//
	// It is a flag rather than a blanket rule for every reloaded session because no
	// OTHER write in this file can install one carrying a fault: runReload's file
	// arm returns no `reloaded` at all.
	fromRepoint bool

	// sourceFault is the classified source-file fault a RELOAD met, set by exactly
	// one producer -- runReload's file arm -- and it is fromRepoint's counterpart
	// one field up. That flag asks handleActionDone to look at the session this
	// message installed; this door installs none, and the session already in hand
	// opened perfectly and latched nothing, so the fault travels here or nowhere.
	//
	// A FIELD RATHER THAN AN errors.As OFF msg.err, which would be a rule about
	// every message instead of about this door.
	//
	// SET FOR EVERY SESSION, NOT ONLY THE ONES THAT GET A PANEL: the door that
	// classifies does not decide who may be told. handleActionDone's arm does, and
	// raises the panel only for a session with a plan behind it. msg.err still
	// carries the fault too, and the two are not one fact with two owners: err is
	// what the gesture FAILED with, while this is what the outcome arm OPENS A
	// PANEL over.
	sourceFault *session.SourceFileError
}

type editorDoneMsg struct {
	content string
	err     error
}

// planLabel names a session for the write confirmations and status messages
// below: the file's base name when the session has a path, or the plan's own
// title when it does not. filepath.Base("") answers "." -- accurate of the
// function, meaningless to a person -- and a sourceless plan always has an empty
// Path. The title is always available in the empty-path case: every door that can
// hand app a path-less session (session.OpenByID, OpenVersion) opens a plan that
// already Exists, so there is no "no path and no title yet" state left for this
// to cover.
func planLabel(path, planTitle string) string {
	if path == "" {
		return planTitle
	}
	return filepath.Base(path)
}

func (m *Model) dispatchWrite(action keymap.Action) (tea.Model, tea.Cmd) {
	if len(m.blocks) == 0 {
		switch action {
		case keymap.ActComment, keymap.ActReply, keymap.ActToggleResolve, keymap.ActDelete:
			m.status = "document has no content blocks"
			return m, nil
		}
	}
	switch action {
	case keymap.ActComment:
		// REFUSED BEFORE THE PANEL OPENS, which is where a refusal about the CURSOR'S
		// BLOCK belongs. This panel is the one that carries a draft, so the only
		// other place to catch it is the post button, after the reviewer has typed
		// the comment that is then thrown away -- and past the point where a
		// plan-less file has been lazily CREATED for a comment that cannot be
		// anchored.
		//
		// REFUSE RATHER THAN CLAMP: the cursor may rest on a rule, and
		// a write aimed there is answered rather than quietly re-aimed at a block the
		// reviewer did not choose.
		//
		// !ok IS THE CURSOR PAST THE END, not the empty document the guard above
		// already answered. The review cursor is not clamped to m.blocks, and both
		// this panel's post button and composeTarget subscript it, so the
		// alternative here is an index panic.
		if b, ok := m.cursorBlock(); !ok || !ui.Anchorable(b) {
			m.status = noContentAtCursor
			return m, nil
		}
		return m, m.enterCompose("")
	case keymap.ActReply:
		if v, ok := m.selectedThread(); ok {
			return m, m.enterCompose(v.Thread.ID.String())
		}
		m.status = "no thread at cursor"
	case keymap.ActToggleResolve:
		v, ok := m.selectedThread()
		if !ok {
			m.status = "no thread at cursor"
			return m, nil
		}
		if !m.dispatchOK() {
			return m, nil
		}
		m.inFlight = true
		sess, tid, want := m.sess, v.Thread.ID, !v.Thread.Resolved
		return m, m.runAction(-1, func(ctx context.Context) (string, error) {
			if err := sess.Resolve(ctx, tid, want); err != nil {
				return "", err
			}
			return "thread updated", nil
		})
	case keymap.ActApprove:
		m.mode = modeConfirmApprove
		m.confirm = fmt.Sprintf("approve the current state of %s? (y/n)", planLabel(m.sess.Path, m.sess.Plan.Title))
	case keymap.ActReload:
		if !m.dispatchOK() {
			return m, nil
		}
		m.inFlight = true
		return m, m.runReload(m.svcOf(), m.sess.Path, m.sess.FromSnapshot(), m.sess.Plan.ID)
	case keymap.ActDelete:
		// The plan list dispatches the same action at the plan under ITS cursor
		// (app/list.go's updateBrowse); one action, each model acting on what it
		// selects.
		v, ok := m.selectedThread()
		if !ok {
			m.status = "no thread at cursor"
			return m, nil
		}
		// Refused BEFORE the panel opens, not only at y -- the list's own
		// enterConfirmDelete precedent, and for its reason: this panel carries no draft
		// to protect, and refusing here means a rapid double-d can never put a panel
		// naming a thread in front of a human while that thread's delete is already in
		// flight. The y arm checks again anyway, because the world can move while a
		// panel is up.
		if !m.dispatchOK() {
			return m, nil
		}
		m.deleteTID = v.Thread.ID
		m.mode = modeConfirmDeleteThread
		m.confirm = deleteThreadConfirmText(v.Thread)
	}
	return m, nil
}

// deleteThreadConfirmText is modeConfirmDeleteThread's body:
//
//	Delete "the deploy step needs a rollback…" including 2 replies?
//
// THE EXCERPT IS THE HEAD COMMENT'S FIRST LINE, and it is here because the screen
// behind this panel does not say which thread is selected. First LINE rather than
// whole body so a multi-line comment cannot turn the question into a wall of text,
// then clipped to deleteExcerptRunes.
//
// THE COUNT CLAUSE IS OMITTED OUTRIGHT AT ZERO rather than rendered as "including 0
// replies". replies is len(Comments)-1: the head comment is the thread's first
// comment, never a reply to itself.
//
// NO AUTHOR CLAUSE, and no "(y/n)" legend in the BODY: capitalized, ending in the
// question mark, with y/n/esc named by the help bar (confirmHint("delete")) and
// nowhere else. This is NOT the plan list's delete panel's shape any more, since
// listConfirmDelete draws in a centred box and this mode is still a strip.
// confirmLines' warning about the legend an irreversible-action panel must never
// silently drop is answered regardless: the keys live on the bar.
//
// THE ZERO-COMMENT CASE IS GUARDED rather than assumed away -- "-1 replies" must
// not be reachable -- which leaves a question with nothing to name, and that is
// honest.
func deleteThreadConfirmText(t domain.Thread) string {
	if len(t.Comments) == 0 {
		return "Delete this thread?"
	}
	head, _, _ := strings.Cut(t.Comments[0].Body, "\n")
	q := fmt.Sprintf("Delete %q", clipRunes(strings.TrimSpace(head), deleteExcerptRunes))
	if replies := len(t.Comments) - 1; replies > 0 {
		noun := "replies"
		if replies == 1 {
			noun = "reply"
		}
		q += fmt.Sprintf(" including %d %s", replies, noun)
	}
	return q + "?"
}

// enterSourceFault opens Panel 1 over the document: the mode and the body,
// together, because the body is DERIVED from the fault it is given and never held
// anywhere else. That is what lets modeConfirmDeletePlan borrow m.confirm for its
// own question and hand it straight back on n -- there is no saved copy to
// restore and none to go stale.
//
// THE FAULT IS A PARAMETER AND NOT READ OFF m.sess, because one caller has one
// the session does not carry: ctrl+r meets a missing file on a session that opened
// perfectly, so m.sess.SourceFault is nil at that door and the fault travels on
// msgActionDone instead. ONE function asked of whichever fault the caller holds.
//
// IT IS m.sourceFault's ONLY WRITER, and it writes only on the way to opening a
// panel, so that field can never name a fault no panel is showing.
//
// It opens nothing at all for a fault this panel has no pinned words for, and
// nothing for no fault at all (sourceFaultText). IT REPORTS WHETHER IT OPENED for
// the reload door, which hands the status line over to the panel and must fall
// back to the plain error sentence when no panel opened. The callers that only
// re-derive the panel they came from ignore the answer.
func (m *Model) enterSourceFault(fault *session.SourceFileError) bool {
	text := sourceFaultText(fault)
	if text == "" {
		return false
	}
	m.sourceFault = fault
	m.mode = modeSourceFault
	m.confirm = text
	return true
}

// sourceFaultText is Panel 1's body and the ONE place that decides which of the four
// texts a fault gets.
//
// THE ">" IN FRONT OF THE PATH IS A LITERAL CHARACTER AND NOT AN INDENT: an indent
// at the old strip's left edge read as nothing at all. The separator in the key
// lines is "·" (U+00B7) rather than "to": it is the review model's own help-bar
// vocabulary (sourceFaultHint), which makes the panel and the bar read as one voice.
//
// THE UNREADABLE TEXT SHOWS THE OS's OWN REASON and the gone one does not: an
// unreadable file is the one state whose remedy is the reader's to apply --
// fix the permission, or notice the path became a directory -- and naming
// what the file system said is what makes that possible. A missing file
// has no such reason to give.
//
// THE REASON IS readReason(f.Err) -- THE INNERMOST CAUSE AND NOT THE WHOLE ERROR --
// AND IT SITS IN THE HEADLINE, IN PARENTHESES. A *fs.PathError prints as "read
// <path>: <reason>", and this panel puts <path> on its own row two lines below, so
// rendering the error whole drew <path> twice. It also makes both arms the same
// shape. THE FALLBACK IS THE WHOLE STRING, for a cause that is not a
// *fs.PathError.
//
// ALL FOUR STATES ANSWER: CLAIMED AND RELEASED ONCE ANSWERED "" HERE, on the
// premise that this carrier could never see them;
// TestRepointReopensThisPlanAndNotWhateverAnswersAtThePath reaches claimed
// live, so they now get a panel like the other two. claimed DOES NOT NAME the
// plan that took the file: it says what happened, deliberately not who did it.
//
// THE STATE IS THE DISCRIMINATOR AND NEVER A FIELD'S EMPTINESS, which is
// SourceFileError's own rule: Err is nil for SourceFileClaimed and
// SourceFileReleased, the two states where the read SUCCEEDED and it was
// identity, not IO, that failed -- so testing it instead would work today and
// silently pick the wrong text the day a state moves.
func sourceFaultText(f *session.SourceFileError) string {
	if f == nil {
		return ""
	}
	const tail = "Draftplane still has a cache of the most recent version.\n\n" +
		"f · point at a new file location\n" +
		"o · keep it in Draftplane with no file (agent edits only)\n" +
		"d · delete this plan\n" +
		"esc · go back to the plan list"
	switch f.State {
	case session.SourceFileGone:
		return fmt.Sprintf("This plan's original file is gone or has moved:\n\n>  %s\n\n%s", f.Path, tail)
	case session.SourceFileUnreadable:
		return fmt.Sprintf("Draftplane can't read this plan's original file (%s):\n\n>  %s\n\n%s",
			readReason(f.Err), f.Path, tail)
	case session.SourceFileClaimed:
		return fmt.Sprintf("Another plan is already following this file. Two plans can't follow the same file:\n\n>  %s\n\n%s",
			f.Path, tail)
	case session.SourceFileReleased:
		return fmt.Sprintf("This plan's original file is still on disk, but it no longer resolves back to this plan:\n\n>  %s\n\n%s",
			f.Path, tail)
	}
	return ""
}

// readReason is the reason clause of a file-read failure, with the operation and
// the path it names taken off -- see sourceFaultText, which carries the argument
// for why a panel wants the cause alone: it names the reason once in the
// headline and puts the path on its own row below, so the whole error would draw
// the path twice.
func readReason(cause error) string {
	var pe *fs.PathError
	if errors.As(cause, &pe) {
		return pe.Err.Error()
	}
	return fmt.Sprint(cause)
}

// repointPaneHint is the path pane's help-bar line, modeRepoint's listing in
// modeHints.
//
// IT SAYS "esc back" AND NOT "esc cancel", because "back" is where esc goes: the
// pane hands back whatever it was opened from.
const repointPaneHint = "enter submit · esc back"

// repointPaneHeadline and repointPaneKeys are the CENTRED path pane's body,
// drawn as a bordered box in the middle (drawsCentredPanel). They are in Panel
// 1's own shape and voice: a headline ending in a colon, the ">" line that panel
// puts the dead path on -- here the line the reader TYPES on -- and one "key ·
// what it does" line per exit.
//
// THE KEY LINES NAME enter RATHER THAN ctrl+d, and the help bar under the box
// says the same thing in the bar's own words (repointPaneHint), exactly as Panel
// 1's key lines and sourceFaultHint already do.
const (
	repointPaneHeadline = "Enter the new path for the file:"
	repointPaneKeys     = "enter · submit\nesc · back"

	// repointRefusalGlyph leads the refusal row -- "⚠ path is required" is the
	// pinned wording for the empty-path case (errPathRequired). Every
	// other refusal the pane carries gets this same glyph in front: re-point's own
	// three -- repointTarget's file test, the claimed-path sentence
	// pathClaimedRefusal composes, and the "error: " line written when
	// client.PathClaimant could not answer at all. So a
	// reader sees one visual language for "the last thing you typed was refused"
	// rather than several sentences that happen to say so in words. It is spent
	// once, in repointBody, rather than baked into each refusal's own stored
	// string: the glyph is a DISPLAY fact about the box, not a fact about the error.
	repointRefusalGlyph = "⚠ "

	// repointCursor is the input row's cursor cell. A GLYPH AND NOT REVERSE
	// VIDEO: centredPanelBox paints one style over a whole row, so a cell
	// that differs from its neighbours cannot be expressed in the []string
	// body it is handed -- and a block at the cursor's column is what
	// searchInputLine already draws at the end of its own row.
	repointCursor = "█"

	// repointFieldInset is the space between the field band's left edge and
	// the text in it. The band spans the box's whole interior, so without it
	// the first character a reader types sits flush against the ground
	// change.
	repointFieldInset = " "

	// repointMinInterior is the floor under the box's interior width. The
	// pane takes the width of the panel it replaces (repointInterior), which
	// is the right number in every state a reader can reach it from; the
	// floor covers the one it is not -- a Panel 1 body cut so short by a
	// narrow terminal that the box left no field to type in.
	repointMinInterior = 40
)

// deleteExcerptRunes bounds the excerpt deleteThreadConfirmText quotes. The
// panel as a whole is bounded by confirmLines (word-wrap, hard-wrap, then a
// budget truncation) whatever this is, so what this number buys is that one
// long comment cannot push the question's own words off the head of the panel.
const deleteExcerptRunes = 80

// enterPathInput opens Panel 1's path pane (modeRepoint): one widget in the
// centred box (repointGroups), with its own key rows and hint line
// (repointPaneKeys, repointPaneHint).
//
// IT OPENS EMPTY, AND THE EMPTY PANE IS THE POINT. This pane is reached from a
// panel whose headline says that path is gone, by a reader who pressed f BECAUSE
// of it -- so a prefill of the missing path was a string to clear before typing
// anything, and a commit on an untouched pane earned repointTarget's "does not
// exist" about the very path the panel two rows above had just said was missing.
// That affordance still pays for the pane staying open on a refusal (updatePanel's
// modeRepoint case): what is expensive to lose is what the reader typed.
func (m *Model) enterPathInput() {
	m.mode = modeRepoint
	m.pathTA.Reset()
	m.pathTA.CursorEnd()
	m.status = ""
	// CLEARED ON EVERY ENTRY: a refusal belongs to the attempt that earned it, and
	// one surviving a re-open (esc, then f again) would sit under a path it is no
	// longer about.
	m.repointRefusal = ""
}

// runDeletePlan destroys the plan this session is reviewing -- Panel 1's d, after
// modeConfirmDeletePlan's y -- and it is the only write in this file that ENDS
// the review rather than changing something inside it. The success message says
// so with planDeleted; handleActionDone is where that lands.
//
// A BARE CONTEXT, and not attributedCtx like every other write dispatched from
// this model. That is the plan list's own ruling about this exact verb: a delete
// is not a review fact -- no implementation of DeletePlan reads
// client.AttributionFrom -- and one verb reached from two doors must not be
// attributed one way on one of them.
//
// No retry closure: a delete registers no version and asserts no base, so there
// is no conflict for one to answer.
//
// recentPath AND path ARE CAPTURED HERE, not read inside the closure, on the
// same discipline svc and id already follow: the closure runs off the loop, so
// anything it reads off m must be copied out first. The forget runs on success,
// before the message returns -- this door's own copy of the list's
// updateConfirmDelete rule, so a plan deleted from Panel 1 leaves Recently
// opened exactly as one deleted from the list does.
func (m *Model) runDeletePlan(svc client.PlanService, id domain.PlanID) tea.Cmd {
	recentPath, path := m.recentPath, m.sess.Path
	return func() tea.Msg {
		if err := svc.DeletePlan(context.Background(), id); err != nil {
			return msgActionDone{err: err, expandBlock: -1}
		}
		forgetRecent(recentPath, recent.Entry{PlanID: id, Path: path})
		return msgActionDone{status: "plan deleted", expandBlock: -1, planDeleted: true}
	}
}

// dropSeizedSearch drops a search term that was still being TYPED when a write's
// result took the keyboard away from modeSearch. enterSearch carries the ruling;
// this is the mechanism.
//
// ONE PLACE RATHER THAN A LINE IN EACH REFUSAL ARM, and written as a comparison
// of modes rather than a list of arms: a list of producers is the shape that goes
// stale, and an arm added later gets this without having to know it exists. A
// result that changes no mode leaves the reader typing.
//
// It drops the term WHOLE, exactly as modeSearch's own esc does, including one an
// earlier enter accepted: there is one field (Model.search) and no separate
// memory of what was accepted, so the honest state to leave behind is no search
// at all -- which is what the help bar under the panel is already advertising.
func (m *Model) dropSeizedSearch(before mode) {
	if before == modeSearch && m.mode != modeSearch {
		m.search = ""
	}
}

// dispatchOK reports whether a write may be dispatched right now. It never
// claims inFlight itself — only the call site does, immediately before
// returning an accepted write cmd — so it is safe to call it early, before
// state a refusal must preserve is touched (the compose panel's post button
// checks it before closing the panel so a refusal keeps the typed text), and
// then still bail out for other reasons (no thread selected, empty body, ...)
// without ever having claimed anything.
func (m *Model) dispatchOK() bool {
	if m.inFlight {
		m.status = "action in progress"
		return false
	}
	return true
}

// svcOf exposes the session's PlanService for reload.
func (m *Model) svcOf() client.PlanService { return m.sess.Service() }

// selectedThread is the thread every read-mode write is aimed at: ActReply,
// ActToggleResolve and ActDelete all ask it, and the last of the three destroys what
// it lands on. m.selected is what aims it, and reselect (model.go) is what keeps
// that index meaning the thread the user picked across a refresh.
//
// NO THREAD SELECTED IS AN ANSWER, NOT AN INDEX. ui.NoThread means the rail is on
// the BLOCK's own line, so this answers false for it and all three writes say "no
// thread at cursor" -- the sentence that matches what the screen is showing.
//
// IT IS ALSO A PANIC GUARD: reselect skips a NoThread entry because "reaimSelection
// would index [-1]", and the clamp below bounds i only from ABOVE, so -1 fell
// through it into vs[-1] and r/R/d crashed the program.
//
// REFUSING is the direction rather than clamping to some thread on the block, and d
// is why: the delete gesture must never land on a thread nobody selected. The bound
// below is a FLOOR under a stale entry, not the rule -- reselect is the rule. It used
// to be `% len(vs)`, and a modulo is the wrong bound for an index that has fallen
// behind: it wraps, so a stale index came back as some OTHER thread on the block,
// indistinguishable from a deliberate selection.
//
// AN ABSENT ENTRY IS NO SELECTION, and this read goes through selectedIndex rather
// than the map so it cannot be anything else: the map's zero value is 0, a real
// thread, so a block reached by g, G or a page scroll aimed all three writes at its
// FIRST thread while the rail sat on the block's own line.
//
// IT DOES NOT INHERIT focusedThread's !expanded REFUSAL, and that is the one place
// the two deliberately differ. focusedThread refuses there because a collapsed card
// has no thread row to draw a rail on, which is a fact about PAINTING. n onto a
// collapsed card is still the reader saying "the next thread".
func (m *Model) selectedThread() (ui.ThreadView, bool) {
	vs := m.views[m.cursor]
	if len(vs) == 0 {
		return ui.ThreadView{}, false
	}
	i := m.selectedIndex(m.cursor)
	if i == ui.NoThread {
		return ui.ThreadView{}, false
	}
	if i >= len(vs) {
		i = len(vs) - 1
	}
	return vs[i], true
}

// cursorBlock is the block a BLOCK-AIMED write lands on -- the first comment
// on a block, and relocating an orphan onto one -- and it is selectedThread's
// sibling: that one aims the three thread-aimed writes and answers false when
// the focus is not on a thread, this one aims the two block-aimed writes and
// answers false when the cursor is not on a block.
//
// FALSE IS AN ANSWER AND NOT AN INDEX, for selectedThread's reason. The review
// cursor is deliberately not clamped to m.blocks (seekMatch), so it can be past
// the end -- a document that shrank under a reload is one way -- and the
// callers refuse rather than subscript.
func (m *Model) cursorBlock() (ui.Block, bool) {
	if m.cursor < 0 || m.cursor >= len(m.blocks) {
		return ui.Block{}, false
	}
	return m.blocks[m.cursor], true
}

// enterCompose opens the composer: mode, panel state, and the focus ring
// (composeFocus's own doc comment) all reset in the same breath, so nothing
// left over from a PRIOR compose session -- a partial draft, a ring position
// off the editor -- can be mistaken for this one's start state.
//
// IT FOCUSES THE TEXTAREA ITSELF, closing a split that used to run through
// this function's two production callers instead: ActComment and ActReply
// both landed here and THEN both called m.ta.Focus() themselves, one call
// duplicated at every site that will ever open this panel rather than made
// once at the panel's own door. A third caller could have forgotten it; this
// function cannot forget itself. Both callers now return this call's own cmd
// unchanged.
func (m *Model) enterCompose(replyTo string) tea.Cmd {
	m.mode = modeCompose
	m.replyTo = replyTo
	m.composeFocus = composeFocusEditor
	m.ta.Reset()
	m.status = ""
	return m.ta.Focus()
}

// composeFocusStep moves modeCompose's focus ring by one stop: delta is 1 for
// tab's own direction and -1 for shift+tab's reverse (updatePanel's modeCompose
// arm), never anything else -- the two arithmetic facts below both lean on
// that. +3 THEN mod 3, rather than a bare %3, because Go's % keeps the
// dividend's sign: -1 % 3 is -1, not the 2 a ring needs.
//
// THE TEXTAREA'S OWN FOCUS TRACKS THE RING'S EDITOR STOP AND NOTHING ELSE.
// Since delta is always ±1, m.composeFocus == composeFocusEditor before the
// step is exactly "this step is LEAVING editor" (Blur), and next ==
// composeFocusEditor is exactly "this step is ARRIVING at it" (Focus, whose
// cmd is returned) -- neither true for the one transition, post <-> cancel,
// that never touches editor at all. Both keys are FREE to spend here: the
// textarea's own KeyMap binds neither, and Text is empty for both, so this
// costs the editor no keystroke it could otherwise have used.
func (m *Model) composeFocusStep(delta int) tea.Cmd {
	next := composeFocus((int(m.composeFocus) + delta + 3) % 3)
	if m.composeFocus == composeFocusEditor {
		m.ta.Blur()
	}
	var cmd tea.Cmd
	if next == composeFocusEditor {
		cmd = m.ta.Focus()
	}
	m.composeFocus = next
	return cmd
}

// exitCompose is the composer's ONE way out, shared by esc and by the [ cancel ]
// button so the two cannot drift: this panel has a second door, and two doors
// spelling "leave without writing" differently is how one of them comes to
// forget the deferred refresh.
//
// IT DROPS THE DRAFT, which is what cancel MEANS here and is the asymmetry worth
// naming: the post path guards its text against a refusal because a comment is
// expensive to lose by accident, while a reader who presses cancel has said to
// lose it. THE PENDING REFRESH IS CONSUMED HERE and not on the post path -- see
// maybeApplyPendingRefresh -- because no write is dispatched, so no
// handleActionDone is coming to consume it.
//
// It leaves m.composeFocus alone: enterCompose resets the ring at the door it
// owns, and resetting it twice would only invent a second place for that rule to
// live.
func (m *Model) exitCompose() {
	m.mode = modeRead
	m.ta.Reset()
	m.maybeApplyPendingRefresh()
}

// pressComposeButton is what PRESSING one of the composer's two controls means,
// and it is one function for exitCompose's own reason: each button has TWO
// ways to be pressed -- enter on the ring stop that holds the focus
// (updatePanel's modeCompose arm) and a left click on the label the frame drew
// (updateComposeButtonClick, app/mouse.go) -- and two spellings of "post" is how
// one of them comes to skip the refusal check dispatchComposePost opens with.
//
// composeFocusEditor IS NOT A BUTTON, and it reaches the inert return rather than
// a panic or a default arm. No span can name it (the editor stop draws no label,
// so composeButtonBar has no entry for it) and the keyboard never arrives here
// from it (enter on the editor falls through to the textarea instead). Answering
// "press the control the reader is on" with nothing, when the reader is on no
// control, is the honest reading rather than a guard papering over a caller.
func (m *Model) pressComposeButton(focus composeFocus) (tea.Model, tea.Cmd) {
	switch focus {
	case composeFocusPost:
		return m.dispatchComposePost()
	case composeFocusCancel:
		m.exitCompose()
	}
	return m, nil
}

// dispatchComposePost is the [ post ] button's whole body, and every line of it
// arrived here UNCHANGED from the deleted ctrl+d arm. The move was
// the only thing that changed, deliberately: what looks like four steps is
// fourteen behaviours, and an earlier reading of this function named four of
// them.
//
// THE ORDER IS THE CONTRACT. dispatchOK comes first and it MUTATES (it writes
// "action in progress"), so it must run before anything blurs, resets or exits:
// a refusal has to leave the panel standing with the typed text in it, one
// keystroke from a retry. TrimSpace is a SUBMISSION-time rule and only that --
// the widget renders a whitespace-only draft as the real input it is
// (composeTextareaPainted) -- and here it is what routes such a draft into the
// empty branch. mode and Reset are unconditional and sit ABOVE that branch, so
// an empty post is a cancel in effect and leaves by exactly the same states.
//
// EVERYTHING THE GOROUTINE NEEDS IS CAPTURED ON THE LOOP: replyTo, the cursor,
// m.blocks[cursor], the inferred title and the session. That subscript is
// UNCHECKED and is safe only because a cursor with nothing anchorable under it
// was refused when the panel opened; moving it inside the closure would read
// m.blocks off the goroutine, which is the discipline every write in this file
// exists under.
//
// runComposeAction AND NOT runAction. This is the one write that CREATES the
// thread the model must then aim at, so fn reports the new ThreadID beside the
// status and handleActionDone selects it on expandBlock -- which is also why
// expandBlock is the cursor for a comment and -1 for a reply, a reply landing in
// a card that is already open. runAction would compile and pass most of this
// package's tests while quietly losing both the selection and the retry
// closure's shape.
//
// WHAT IT DOES NOT DO IS ALSO SETTLED: it does not clear m.replyTo, does not
// consume a pending refresh on the dispatch path (handleActionDone does), moves
// neither m.cursor nor m.scroll nor m.expanded, and does not rerender -- the read
// screen behind the panel is exactly the one the reader left, and the write's own
// result is what changes it.
func (m *Model) dispatchComposePost() (tea.Model, tea.Cmd) {
	// Refusal check before any reset: if another write is in flight,
	// stay in compose with the typed text intact so the user just
	// retries the post — closing the panel here would silently
	// discard their comment.
	if !m.dispatchOK() {
		return m, nil
	}
	body := strings.TrimSpace(m.ta.Value())
	m.mode = modeRead
	m.ta.Reset()
	if body == "" {
		// An empty-draft post is a cancel in effect: back to read
		// with no write dispatched, so no handleActionDone will
		// consume a refresh deferred while compose was open.
		m.maybeApplyPendingRefresh()
		return m, nil
	}
	replyTo := m.replyTo
	cursor := m.cursor
	block := m.blocks[cursor]
	m.inFlight = true
	// Captured synchronously here, before the goroutine runs: a
	// plan-less file's first comment creates the plan itself, using
	// the same inferred title the status line already shows.
	title := ui.InferTitle(m.blocks, m.sess.Path)
	sess := m.sess
	expandBlock := -1
	if replyTo == "" {
		expandBlock = cursor
	}
	// The created thread rides back on the message so
	// handleActionDone can select it -- see selectCreated for the
	// ruling. A REPLY creates no thread and returns "" here: it
	// lands inside a card that already exists, on a thread the
	// reader had to select to reply to in the first place.
	fn := func(ctx context.Context) (domain.ThreadID, string, error) {
		if replyTo != "" {
			if _, err := sess.Reply(ctx, domain.ThreadID(replyTo), body); err != nil {
				return "", "", err
			}
			return "", "reply posted", nil
		}
		// Lazy plan creation: comment on a plan-less file creates the
		// plan first, with its inferred title.
		if !sess.Exists {
			if err := sess.Create(ctx, title); err != nil {
				return "", "", err
			}
		}
		if ui.AnchorsAsSection(block) {
			a, err := session.SectionAnchor(string(sess.Content), block.HeadingPath)
			if err != nil {
				return "", "", err
			}
			th, err := sess.Comment(ctx, a, body)
			if err != nil {
				return "", "", err
			}
			return th.ID, "comment posted", nil
		}
		a, err := session.AnchorForBlockText(string(sess.Content), ui.BlockAnchorSpan(block), block.HeadingPath)
		if err != nil {
			return "", "", err
		}
		th, err := sess.Comment(ctx, a, body)
		if err != nil {
			return "", "", err
		}
		return th.ID, "comment posted", nil
	}
	return m, m.runComposeAction(expandBlock, fn)
}

// runAction executes a session write off the input path. fn must perform ONLY
// session I/O -- it runs in a goroutine concurrently with the Update loop, so it
// must never touch Model state or call RefreshFromSession; that projection happens
// in handleActionDone, back on the loop. expandBlock is whatever the call site
// captured at dispatch time, or -1 for none.
//
// The context fn runs with is attributedCtx's, built HERE, on the loop, and
// captured into the closure -- the same dispatch-time capture discipline
// expandBlock and successorTID follow.
func (m *Model) runAction(expandBlock int, fn func(context.Context) (string, error)) tea.Cmd {
	ctx := attributedCtx()
	return func() tea.Msg {
		status, err := fn(ctx)
		return msgActionDone{
			status: status, err: err, expandBlock: expandBlock,
			retry: fn, retryExpand: expandBlock,
		}
	}
}

// runRelocateAction is runAction's relocate-mode counterpart: runAction
// itself is generic and can't set fromRelocate/nextTID, so this thin
// wrapper carries them through to handleActionDone's advance step.
func (m *Model) runRelocateAction(nextTID domain.ThreadID, fn func(context.Context) (string, error)) tea.Cmd {
	ctx := attributedCtx()
	return func() tea.Msg {
		status, err := fn(ctx)
		return msgActionDone{status: status, err: err, expandBlock: -1, fromRelocate: true, nextTID: nextTID}
	}
}

// runComposeAction is runAction for the one write that CREATES the thread the
// model must then aim at: fn reports the new thread's id alongside the status, and
// handleActionDone selects it on expandBlock.
//
// The RETRY closure handed back is fn with its ThreadID dropped, deliberately. A
// conflict decline re-dispatches through runAction, which has no field to carry an
// id on; threading one through would buy the post-select on a retried post and
// nothing else. A retried post still opens its card, and the focus sits on the line
// one n away. The closure holds the comment's body, so a conflict never loses what
// the reader typed.
func (m *Model) runComposeAction(expandBlock int, fn func(context.Context) (domain.ThreadID, string, error)) tea.Cmd {
	ctx := attributedCtx()
	plain := func(ctx context.Context) (string, error) {
		_, status, err := fn(ctx)
		return status, err
	}
	return func() tea.Msg {
		tid, status, err := fn(ctx)
		return msgActionDone{
			status: status, err: err, expandBlock: expandBlock, selectTID: tid,
			retry: plain, retryExpand: expandBlock,
		}
	}
}

// successorTID is the queue-forward continuation captured at dispatch time:
// the orphan after the current one in the current queue, or "" when the
// current orphan is the only one left.
func (m *Model) successorTID() domain.ThreadID {
	q := m.unresolvedOrphans()
	if len(q) < 2 {
		return ""
	}
	for i, p := range q {
		if p.Thread.ID == m.relocateTID {
			return q[(i+1)%len(q)].Thread.ID
		}
	}
	return q[0].Thread.ID
}

// runReload re-opens the session fresh, off the input path. Like runAction,
// it touches no Model state directly: the new *session.Session travels back
// in the message and is installed by handleActionDone. fromSnapshot and
// planID are captured at dispatch time (mirroring every other runAction
// call's synchronous-capture discipline): a snapshot-opened session has no
// path to re-read, so it reloads via OpenVersion instead, picking up
// whatever is now the latest registered version — that IS reload's meaning
// for a snapshot. A file-opened session reloads by path as before.
func (m *Model) runReload(svc client.PlanService, path string, fromSnapshot bool, planID domain.PlanID) tea.Cmd {
	ctx := attributedCtx()
	return func() tea.Msg {
		var fresh *session.Session
		var err error
		if fromSnapshot {
			fresh, err = session.OpenVersion(ctx, svc, planID)
		} else {
			fresh, err = session.Open(ctx, svc, path)
		}
		if err != nil {
			// THE FILE ARM RAISES THE FAULT rather than handing up the file system's own
			// sentence under handleActionDone's "error: " prefix. ctrl+r is the one door
			// that meets a missing file while the user is already reading the document,
			// and until now all it could say was `error: open /path: no such file or
			// directory` -- true, unactionable, and identical to what a transient read
			// failure says. session.SourceFileFault classifies it into the vocabulary the
			// other two doors carry.
			//
			// THE SESSION IS UNTOUCHED: this returns no `reloaded`, so the document stays
			// on screen with Panel 1 over it. THE FAULT TRAVELS ON THE MESSAGE, because
			// this door produces no session and the one in hand opened perfectly. WHO GETS
			// THE PANEL IS THAT ARM'S QUESTION AND NOT THIS ONE'S.
			//
			// The snapshot arm is deliberately not classified: an OpenVersion failure is a
			// plan-content failure, not a file's. A FAILURE THAT IS NOT THE FILE'S ANSWERS
			// NOTHING HERE either -- this arm's error can be the store's own, such as an
			// unreadable state file with the document readable on disk, and
			// session.SourceFileFault answers nil for every one of those.
			//
			// NO GATE HERE: this arm is reachable ONLY because the file was read
			// successfully at open, and whether a fault raises the panel is
			// handleActionDone's question.
			if !fromSnapshot {
				if fault := session.SourceFileFault(planID, path, nil, err); fault != nil {
					return msgActionDone{err: fault, sourceFault: fault, expandBlock: -1}
				}
			}
			return msgActionDone{err: err, expandBlock: -1}
		}
		return msgActionDone{status: "reloaded", reloaded: fresh, fromReload: true, expandBlock: -1}
	}
}

// runAcceptTip is the conflict prompt's accept path (modeConflict's "y"): reopen
// the plan on its current tip, exactly like runReload's OpenVersion arm, reusing the
// identical reloaded/fromReload plumbing in handleActionDone.
//
// A plain session.OpenVersion, nothing more: the session it opens stands on the
// tip as its base, which is what makes accept-then-retry work at all, since the
// write that conflicted a moment ago now has a real base to succeed against.
func (m *Model) runAcceptTip(svc client.PlanService, id domain.PlanID) tea.Cmd {
	ctx := attributedCtx()
	return func() tea.Msg {
		fresh, err := session.OpenVersion(ctx, svc, id)
		if err != nil {
			return msgActionDone{err: err, expandBlock: -1}
		}
		// Says plainly that this session no longer follows the file on
		// disk: OpenVersion always returns fromSnapshot true, so ctrl+r
		// from here on reloads the registered tip again, not the file --
		// the human's edits are untouched on disk, but there is no in-TUI
		// way back to following them without reopening.
		status := "Now reviewing the latest version. Reopen with your file to go back to editing it."
		return msgActionDone{status: status, reloaded: fresh, fromReload: true, expandBlock: -1}
	}
}

// sanitizePastedPath strips every carriage return and newline out of a bracketed
// paste before it ever reaches the re-point field. The commonest way to copy a
// path carries a trailing "\n", and bubbles' own textarea holds that as a SECOND
// LOGICAL LINE regardless of SetHeight(1) (the viewport height and the value's own
// line count are different things): Column() then lands at 0 on the new second
// line and repointInputRow's cursor-centred window shows the HEAD of the path --
// the identifying tail scrolled off -- rather than where the reader just finished
// pasting. It was display-only (the commit path TrimSpace's the value regardless),
// but a two-line paste additionally split the refusal row across two rows of the
// box.
//
// A ONE-ROW FIELD HAS NO USE FOR A NEWLINE, which is why this deletes rather than
// substitutes one visible rune for another. Both CR and LF are stripped and CRLF
// is not special-cased: stripping "\r" then "\n" independently removes a "\r\n"
// pair as the two runes it is, with nothing left over to reassemble.
func sanitizePastedPath(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// collapseCRLF turns every "\r\n" pair and every lone "\r" into a single "\n",
// and it runs BEFORE the widget because the widget is what would otherwise
// double them.
//
// bubbles' textarea sanitizes a paste through runeutil, which maps '\r' and
// '\n' to a newline INDEPENDENTLY -- so a "\r\n" pair arrives as TWO newlines
// and a paragraph copied from anything Windows-authored lands double-spaced.
// Measured against the real widget: paste "first\r\nsecond", read back
// "first\n\nsecond".
//
// IT IS THE OPPOSITE TREATMENT FROM sanitizePastedPath, DELIBERATELY. That one
// DELETES both, because a one-row path field has no use for a newline and a
// copied path's trailing "\n" would strand the cursor window on the wrong end of
// the path. A comment body is the other case: newlines are the content, and
// quoting the comment you are replying to is the commonest paste this composer
// will ever see. Same message type, two fields, two rules -- which is why this is
// its own function rather than an argument to that one.
//
// NOTHING ELSE IS TOUCHED. Control bytes and invalid UTF-8 are left to the
// widget, which drops them; U+FE0F is left alone, because a
// draft is WRITTEN BACK and stripping the selector here would persist "⚠" for a
// reader who pasted "⚠️" -- ui.VisibleControlsKeepingSelector16 carries that
// argument for ListModel.enterRename, and the strip belongs at the renderer.
func collapseCRLF(s string) string {
	return strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)
}

// updateComposePaste is updateRepointPaste's sibling for the COMMENT COMPOSER:
// collapse the pair the widget would double, then hand the message straight to
// the draft's own textarea.
//
// IT REPORTS NO "changed" FLAG, unlike updateRepointPaste, because there is no
// refusal under this pane for an edit to clear.
func updateComposePaste(ta textarea.Model, msg tea.PasteMsg) (textarea.Model, tea.Cmd) {
	msg.Content = collapseCRLF(msg.Content)
	return ta.Update(msg)
}

// updateRepointPaste is Model.updatePaste's (app/model.go) and
// ListModel.updatePaste's (app/list.go) shared body once each door's own mode gate
// has passed: sanitize the paste, hand it to the widget, report whether the value
// changed. Nothing in it is model-specific once handed the widget, so the caller
// is left only its own gate and its own refusal field to clear.
func updateRepointPaste(ta textarea.Model, msg tea.PasteMsg) (textarea.Model, tea.Cmd, bool) {
	// SANITIZED BEFORE THE WIDGET EVER SEES IT: sanitizePastedPath's own doc
	// comment carries the argument and the driven repro -- a trailing newline, the
	// commonest shape a copied path arrives in, otherwise becomes a second logical
	// line and strands the cursor window on the wrong end of the path.
	msg.Content = sanitizePastedPath(msg.Content)
	before := ta.Value()
	var cmd tea.Cmd
	ta, cmd = ta.Update(msg)
	return ta, cmd, ta.Value() != before
}

// errPathRequired is the re-point pane's refusal of an empty field.
var errPathRequired = errors.New("path is required")

// pathClaimedRefusal is the re-point pane's refusal of a path some OTHER plan
// already follows (client.PathClaimant): abs, then that plan's title, then the
// rule. It names the other plan because a human told "that path is taken" is
// left nowhere, where one told which plan already follows the file knows they
// have two plans for one document. abs is absolute (repointTarget) because the
// sentence names the file the refusal is about, and a relative spelling would
// name it only from the directory it was typed in.
func pathClaimedRefusal(abs string, other domain.Plan) string {
	return fmt.Sprintf("%s is already followed by another plan: %q. Only one plan can follow a file in Draftplane.", abs, other.Title)
}

// repointTarget resolves what the re-point pane was committed with into the absolute
// path a plan may actually be filed under, or says why it may not be. It answers
// with one of two refusals: a path that is not there, and a path that is a directory.
//
// IT NEVER CREATES ANYTHING: draftplane never writes to a
// user's document.
//
// A THIRD STATEMENT OF localReviewFile's RULE (cmd/draftplane/main.go), named rather
// than hidden: mcptools.localFile is the second, and both are unexported to packages
// app cannot import. The trigger for hoisting is a FOURTH copy. THE THREE ARE NOT
// INTERCHANGEABLE: mcptools.localFile asks a THIRD question first, stripping a
// file:// prefix and answering "not local" for any other URL scheme, so
// `file:///work/plan.md` is accepted at MCP's door and refused here. A future hoist
// has to decide which of the two rules it is hoisting.
//
// THE ABSOLUTIZATION IS NOT COSMETIC. domain.Plan.SourceHint is matched by EXACT
// STRING, so a relative path written into that field files a plan reachable only
// from the directory it was typed in. It also has to happen BEFORE the claim check,
// or two spellings of one path would answer "unclaimed" about a path that is very
// much claimed.
func repointTarget(raw string) (string, error) {
	if raw == "" {
		// repointBody puts repointRefusalGlyph's warning glyph in front of it as it
		// does the other two refusals.
		return "", errPathRequired
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		// Its own sentence rather than the file system's, because the file
		// system's ("no such file or directory") is what the panel behind
		// this pane already said about the OLD path and would read as a
		// restatement rather than as an answer about the new one.
		return "", fmt.Errorf("%s does not exist -- re-point never creates a file", abs)
	}
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, not a file", abs)
	}
	return abs, nil
}

// runRepoint drives the re-point: one narrow write to state.json
// (client.SetSourceHint, the same mutation o spends with an empty hint), then a
// re-open of the same plan on the file it now names.
//
// THE RE-OPEN GOES THROUGH session.OpenPlan AND NOT session.Open, and the
// difference is which door decides what happened. OpenPlan is the shared door --
// the plan's own file first, the narrow fallback second, the reason recorded on
// the session it produces -- so this is the same re-open the plan list's enter and
// `draftplane review` perform. session.Open resolves by SOURCE and has no identity
// to check against, so a path that stopped resolving to this plan between the
// checks above and this call would come back as a DIFFERENT plan's session under
// this write's success message. That race is not driven by a test: reaching it
// needs a write to land between two statements of this closure.
//
// THE PLAN IS RE-READ BY ID rather than patched from m.sess.Plan: the record this
// write just changed is the one to open from, and the session's copy is by
// definition one field out of date.
//
// THE WINDOW BETWEEN THE CHECKS AND THE WRITE IS NOT CLOSED, and it cannot be from
// here -- no transaction spans the file system and state.json. What it costs is
// bounded: a file deleted in that window re-opens on the snapshot with the fault
// latched, so the next open raises Panel 1 again over the new path, which is true.
func (m *Model) runRepoint(path string) tea.Cmd {
	ctx := attributedCtx()
	svc := m.svcOf()
	id := m.sess.Plan.ID
	return func() tea.Msg {
		if err := client.SetSourceHint(ctx, svc, id, path); err != nil {
			return msgActionDone{err: err, expandBlock: -1, fromRepoint: true}
		}
		plan, err := svc.PlanByID(ctx, id)
		if err != nil {
			return msgActionDone{err: err, expandBlock: -1, fromRepoint: true}
		}
		fresh, err := session.OpenPlan(ctx, svc, plan)
		if err != nil {
			return msgActionDone{err: err, expandBlock: -1, fromRepoint: true}
		}
		return msgActionDone{status: "now following " + path, reloaded: fresh, fromReload: true, fromRepoint: true, expandBlock: -1}
	}
}

// runRelease is o's write: the SAME state.json mutation runRepoint spends
// (client.SetSourceHint, the empty string meaning release), then the SAME re-open
// through session.OpenPlan, for the SAME reason.
//
// THE STATUS LINE IS THE ONE THING THAT DIFFERS, and it is derived rather than
// hand-written: snapshotOpenStatus (app/root.go) is the SAME predicate every other
// snapshot-open door already stamps for a sourceless plan, read here off the
// session this write just produced.
//
// fromRepoint IS SET, DELIBERATELY REUSED AND NOT A SECOND FLAG: the outcome arm
// asks one question of a reloaded session -- is Panel 1 still owed -- and that
// question does not care which write produced it. A released plan has no
// SourceHint left to fail reading, so the answer is modeRead by construction; a
// write that fails leaves the OLD fault latched exactly as a failed re-point does.
func (m *Model) runRelease() tea.Cmd {
	ctx := attributedCtx()
	svc := m.svcOf()
	id := m.sess.Plan.ID
	return func() tea.Msg {
		if err := client.SetSourceHint(ctx, svc, id, ""); err != nil {
			return msgActionDone{err: err, expandBlock: -1, fromRepoint: true}
		}
		plan, err := svc.PlanByID(ctx, id)
		if err != nil {
			return msgActionDone{err: err, expandBlock: -1, fromRepoint: true}
		}
		fresh, err := session.OpenPlan(ctx, svc, plan)
		if err != nil {
			return msgActionDone{err: err, expandBlock: -1, fromRepoint: true}
		}
		return msgActionDone{status: snapshotOpenStatus(fresh.Path), reloaded: fresh, fromReload: true, fromRepoint: true, expandBlock: -1}
	}
}

// enterSearch opens the document search ON THE TERM ALREADY IN HAND rather than on
// an empty one: "/" (or f) over an active search is how you EDIT it -- add a word,
// backspace one off. Starting empty would make the second press a clear,
// duplicating esc and losing the text with no undo. ListModel.enterFilter's own
// ruling, applied to the second subject the same key now has.
//
// NO dispatchOK GATE, and enterRelocate has none either: a search reads m.blocks
// and writes nothing, so there is no write it could race. It is NOT thereby
// isolated from one already in flight -- that write's msgActionDone can set a mode
// of its own, taking the keyboard away mid-term, and a seizure DROPS the term
// (dropSeizedSearch). A term is accepted by enter and by nothing else, and a
// partial one left standing as an active search would silently change what n and N
// mean while the help bar went on reading "n/N threads", so the reader's next
// navigation keystroke would appear to move and land on the wrong thing.
//
// A MOVING CURSOR CANNOT CORRUPT AN IN-FLIGHT WRITE: accepting a term moves the
// review cursor, but that is NAVIGATION, and moveCursor, jumpThread and pageScroll
// are ungated too -- an in-flight write captured its subject at dispatch and reads
// the cursor for nothing.
func (m *Model) enterSearch() {
	m.mode = modeSearch
}

// searchQuery is the term case-folded with its ends trimmed, so a trailing
// space typed before the next word asks for nothing on its own and "" means NO
// SEARCH -- the one answer searchActive keys on, which is what keeps a term of
// pure whitespace from reading as "on but matching nothing". The RAW string is
// what the input row shows (m.search).
//
// IT IS NOT THE NEEDLE, before somebody makes it one: ui.SearchBlocks folds and
// normalizes whatever it is handed, so this and m.search produce identical hits
// there. What this answers is the question the projection cannot -- whether the
// reader has asked for anything yet.
func (m *Model) searchQuery() string {
	return strings.ToLower(strings.TrimSpace(m.search))
}

// searchActive reports whether a term is in hand in EVERY mode, not only in
// modeSearch: the term outlives the mode that typed it (enter keeps it), so "is
// a search on" and "am I typing one" are two questions and this is the first.
// ListModel.filterActive is the shape; Model.search says why neither has a
// boolean beside the string.
func (m *Model) searchActive() bool {
	return m.searchQuery() != ""
}

// updateSearch handles modeSearch. It NEVER CONSULTS THE KEYMAP (see that mode's
// declaration) and reuses ListModel.updateFilter's input rules verbatim: msg.Text
// == "" is the whole test for "this keystroke is input", backspace drops a RUNE
// (dropLastRune, shared with the filter rather than copied), and every other named
// key including the arrows is ignored.
//
// THE ONE LINE OF updateFilter THAT MUST NOT TRAVEL is its setFilter call, which
// rebuilds the list's body on EVERY keystroke. Here TYPING MOVES NOTHING -- no
// match is computed, nothing scrolls, no cursor moves and m.lines is not
// re-rendered. An implementer reading updateFilter for the shape will copy the
// live update with it; that is the single most likely wrong turn here, and one a
// test comparing rendered VALUES cannot see (TestTypingASearchMovesNothing
// compares the buffer's IDENTITY for exactly that reason).
//
// msg.String() IS CHECKED BEFORE msg.Text, as updateFilter does. A real terminal
// cannot tell the two orderings apart -- a named key carries no Text -- but
// app_test.go's press helper builds an unrecognized key NAME as {Code: 'e', Text:
// "esc"}, where a Text-first handler types the word into the term instead of
// leaving.
//
// BOTH EXITS CONSUME A DEFERRED REFRESH (maybeApplyPendingRefresh) and BOTH TAKE
// BACK A STANDING MISS (clearSearchMiss). enter ACCEPTS AND JUMPS: the term is
// committed and remembered, and the review cursor lands on the first match at or
// after it. esc drops the term and moves nothing, which is the whole difference
// between them.
func (m *Model) updateSearch(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.search = ""
		// THE MISS GOES WITH THE TERM. esc leaves this mode with no search on at
		// all, so `no matches for "penguin"` names something that has stopped
		// existing -- and read mode's esc cannot come along behind and take it back,
		// because that arm is gated on searchActive, which the line above just made
		// false.
		m.clearSearchMiss()
		m.mode = modeRead
		m.maybeApplyPendingRefresh()
		return m, nil
	case "enter":
		m.mode = modeRead
		// THE PREVIOUS TERM'S MISS, TAKEN BACK BEFORE THIS TERM IS JUDGED. The case
		// that needs it is enter over a term that is NO SEARCH -- "" after a
		// backspace, or "   " -- where seekMatch returns at its guard without
		// writing anything.
		//
		// ABOVE seekMatch AND NOT BELOW IT, which is the one placement that is
		// wrong: seekMatch writes this keystroke's own miss, and a clear underneath
		// would eat the answer to the key that was just pressed.
		m.clearSearchMiss()
		m.maybeApplyPendingRefresh()
		// THE REFRESH FIRST, THE LANDING SECOND, and what the order decides is
		// WHICH STATUS THE READER IS LEFT WITH -- not which blocks are searched,
		// which a refresh cannot change (searchMatches says why). A deferred
		// refresh that found new facts writes "review updated"; a term that hits
		// nothing writes its own miss. The SEARCH's answer is the one the key was
		// pressed for, so it goes last and wins.
		//
		// AT OR AFTER THE CURSOR, where n is strictly after it (jumpMatch): a reader
		// who typed a term while sitting on a block that contains it has not asked
		// to leave it, so accepting holds still and n is what moves on.
		m.seekMatch(1, true)
		return m, nil
	case "backspace":
		m.search = dropLastRune(m.search)
		return m, nil
	}
	if msg.Text == "" {
		return m, nil
	}
	m.search += msg.Text
	return m, nil
}

func (m *Model) updatePanel(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeConfirmApprove:
		switch msg.String() {
		case "y":
			if !m.dispatchOK() {
				m.mode = modeRead
				return m, nil
			}
			m.inFlight = true
			m.mode = modeRead
			sess := m.sess
			// Captured synchronously here, before the goroutine runs: a
			// plan-less file's approve creates the plan itself, using the
			// same inferred title the status line already shows.
			title := ui.InferTitle(m.blocks, sess.Path)
			fn := func(ctx context.Context) (string, error) {
				if !sess.Exists {
					if err := sess.Create(ctx, title); err != nil {
						return "", err
					}
				}
				if err := sess.Approve(ctx); err != nil {
					return "", err
				}
				return fmt.Sprintf("review saved for %s (%s)", planLabel(sess.Path, sess.Plan.Title), sess.Hash.Short()), nil
			}
			return m, m.runAction(-1, fn)
		case "n", "esc":
			m.mode = modeRead
			m.maybeApplyPendingRefresh()
		}
		return m, nil

	case modeConflict:
		switch msg.String() {
		case "y":
			// Accept: fetch the tip the conflict carried and re-anchor onto
			// it. The pending write is abandoned, not replayed against
			// content the human never looked at -- they asked to see what
			// changed instead of pushing over it.
			if !m.dispatchOK() {
				m.mode = modeRead
				return m, nil
			}
			m.pendingRetry = nil
			m.inFlight = true
			m.mode = modeRead
			return m, m.runAcceptTip(m.svcOf(), m.sess.Plan.ID)
		case "n":
			// Decline: retry with Base set to the tip the conflict handed back -- no
			// force flag. sess.Service().RegisterVersion is called directly (bypassing
			// sess's own remembered base, which is still stale) with that exact tip; if
			// it still matches the store's real tip this succeeds or no-ops, and the
			// ORIGINAL write then runs for real. If another writer registered again
			// while the prompt was open, THIS call conflicts too and handleActionDone
			// re-enters modeConflict with the new tip -- exactly the property a force
			// flag would not have. fn is never nil here: modeConflict is only ever
			// entered when msg.retry was already non-nil.
			//
			// Only tip.Hash crosses here, never tip.Seq: VersionRegistration's Base is,
			// and stays, a content hash.
			if !m.dispatchOK() {
				m.mode = modeRead
				return m, nil
			}
			m.inFlight = true
			m.mode = modeRead
			sess, tip, fn, expandBlock := m.sess, m.conflictTip, m.pendingRetry, m.pendingExpandBlock
			return m, m.runAction(expandBlock, func(ctx context.Context) (string, error) {
				if _, err := sess.Service().RegisterVersion(ctx, sess.Plan.ID, client.VersionRegistration{
					Content: sess.Content, Base: tip.Hash,
				}); err != nil {
					return "", err
				}
				return fn(ctx)
			})
		case "esc":
			m.mode = modeRead
			m.pendingRetry = nil
			m.maybeApplyPendingRefresh()
		}
		return m, nil

	case modeConfirmDeleteThread:
		switch msg.String() {
		case "y":
			if !m.dispatchOK() {
				// The armed thread goes with the panel here: a y refused because another
				// write is in flight destroyed nothing, and an id left armed would be spent
				// by a later y nobody was shown this panel for.
				m.mode = modeRead
				m.deleteTID = ""
				return m, nil
			}
			m.inFlight = true
			m.mode = modeRead
			// Everything the write needs, captured HERE on the loop and read nowhere
			// else: runAction's own dispatch-time capture discipline -- the closure below
			// touches no Model state at all.
			svc, id, tid := m.svcOf(), m.sess.Plan.ID, m.deleteTID
			m.deleteTID = ""
			// Straight at the service rather than through a session method, like
			// modeConflict's decline above and unlike every other write in this file: a
			// thread delete registers no version and asserts no base, so there is
			// nothing for a Session method to ensure on the way past.
			return m, m.runAction(-1, func(ctx context.Context) (string, error) {
				if err := svc.DeleteThread(ctx, id, tid); err != nil {
					return "", err
				}
				return "thread deleted", nil
			})
		case "n", "esc":
			m.mode = modeRead
			m.deleteTID = ""
			m.maybeApplyPendingRefresh()
		}
		return m, nil

	case modeSourceFault:
		// ALL FOUR OF THE PANEL'S KEYS -- see modeSourceFault, which says why a key
		// arrives with its write and not before it, and sourceFaultHint, which grew in
		// the same change as each one.
		switch msg.String() {
		case "f":
			// f opens the path pane this package already has (enterPathInput). No
			// write happens here and none can until enter, so this could have gone
			// ungated -- it is gated because the one write that can be in flight while
			// this panel is up is the delete d authorized, and a pane opened over a plan
			// being destroyed is a pane whose enter would be refused.
			if !m.dispatchOK() {
				return m, nil
			}
			// FOCUS IS THE COMMAND THIS ARM RETURNS, and its absence is why the pane did
			// nothing at all until a later input pass: a blurred textarea.Model
			// drops every key in its own Update, so f opened a pane that could not be
			// typed into. Every test drove the widget with SetValue rather than with
			// keystrokes, so the whole suite stayed green.
			m.enterPathInput()
			return m, m.pathTA.Focus()
		case "o":
			// This is the release valve, and NO CONFIRMATION -- unlike d this destroys
			// nothing, so there is no y/n question to ask before it runs. Dispatched
			// straight from here rather than through a second pane the way f is, on
			// ToggleResolve's own precedent: nothing this gesture could ask first would
			// change what it does.
			//
			// GATED FOR f's OWN REASON: releasing the hint out from under a plan
			// mid-destruction would race the delete d authorized.
			if !m.dispatchOK() {
				return m, nil
			}
			m.inFlight = true
			return m, m.runRelease()
		case "d":
			// This reuses the plan list's own delete text, called and not copied, in
			// front of the delete this model is about to dispatch. Refused BEFORE the
			// panel opens while another write is in flight, on enterConfirmDelete's
			// precedent; the y arm checks again anyway.
			if !m.dispatchOK() {
				return m, nil
			}
			m.mode = modeConfirmDeletePlan
			m.confirm = ordinaryDeleteConfirmText(m.sess.Plan)
		case "esc":
			// Back to the plan list, having written nothing.
			//
			// GATED BEFORE THE MODE IS TOUCHED, and that ordering is the whole of this
			// arm. showList opens with its own dispatchOK refusal, so dropping the mode
			// first and calling it second dismissed the panel and then declined to leave
			// -- reachable by a race rather than a mistake: d, y, then esc before the
			// delete's result lands. What that left was a review of a plan whose file is
			// gone, in read mode, with NO KEY THAT RE-OPENS THE PANEL --
			// the "read it and decide later" state this flow forbids, arrived at sideways.
			//
			// The mode still goes down on the way out, and that matters only away from
			// Root: msgCloseReview has no handler on a bare Model, and a panel left
			// standing there would be one the user could never dismiss.
			if !m.dispatchOK() {
				return m, nil
			}
			m.mode = modeRead
			m.maybeApplyPendingRefresh()
			return m.showList()
		}
		return m, nil

	case modeConfirmDeletePlan:
		switch msg.String() {
		case "y":
			if !m.dispatchOK() {
				m.enterSourceFault(m.sourceFault)
				return m, nil
			}
			m.inFlight = true
			// BACK TO THE PANEL THAT OFFERED IT rather than to read mode, which every
			// other y arm in this switch goes to: this write either ends the review or
			// fails, and if it fails the file is still missing and the question this
			// panel was opened from is still open. The body is recomposed rather than
			// remembered -- see enterSourceFault.
			m.enterSourceFault(m.sourceFault)
			return m, m.runDeletePlan(m.svcOf(), m.sess.Plan.ID)
		case "n", "esc":
			m.enterSourceFault(m.sourceFault)
			m.maybeApplyPendingRefresh()
		}
		return m, nil

	case modeRepoint:
		switch msg.String() {
		case "esc":
			// BACK TO THE PANEL THAT OFFERED IT: cancelling a re-point writes nothing,
			// the file is still missing, and Panel 1's question is still open -- so
			// dropping to read mode here would be the "read it and decide later" state
			// this forbids, reached by backing out of the remedy.
			//
			// UNGATED, unlike Panel 1's own esc: that one has to reach showList, which
			// refuses in flight. This one goes nowhere -- it re-derives the very panel
			// it came from -- so there is nothing for a write in flight to make
			// inconsistent.
			m.pathTA.Reset()
			m.enterSourceFault(m.sourceFault)
			m.maybeApplyPendingRefresh()
			return m, nil
		case "enter", "ctrl+m":
			if !m.dispatchOK() {
				return m, nil
			}
			// enter AND NOT ctrl+d. Pressing f has already said what this pane is for; a
			// modifier chord to say it again is ceremony, and the pane is one line with no
			// newline to protect.
			//
			// "ctrl+m" IS THE SAME KEY AND NOT A SECOND ONE: bubbles' own
			// textarea.KeyMap.InsertNewline binds BOTH "enter" and "ctrl+m" (the historical
			// ASCII CR a terminal still sends for either), and matching only the first let
			// ctrl+m fall through to pathTA.Update, which inserted a newline. MEASURED:
			// tea.KeyPressMsg{Code: 'm', Mod: tea.ModCtrl}.String() == "ctrl+m", reachable
			// from a real terminal. THIS FIELD IS KEPT SINGLE-LINE BY BOTH OF ITS ENTRY
			// POINTS, not by its own SetHeight(1): the keyboard, matched here ahead of the
			// widget, and a bracketed paste, sanitized by sanitizePastedPath. It is the
			// plan list's key too, which is what keeps the one hint both doors show honest.
			//
			// THE PANE STAYS OPEN ON EVERY REFUSAL BELOW: what is expensive to lose is the
			// path the reader typed. THE BOX'S OWN REFUSAL LINE says what was wrong
			// (m.repointRefusal, drawn by repointBody under the field) rather than the
			// status bar, which truncates before a long plan title's own bytes are through.
			// AND NEITHER REFUSAL IS RE-DERIVED FROM SOMEWHERE ELSE'S RULE: the file test
			// is repointTarget's and the claim test is client.PathClaimant, the ONE
			// statement of that question in the product.
			//
			// EVERY ARM BELOW CLEARS m.status ALONGSIDE SETTING m.repointRefusal: a stale
			// m.status from an EARLIER attempt in this same pane would sit on the bar
			// contradicting whatever the box says now -- an attempt whose write failed
			// leaves "error: ..." there, the reader retries with an empty field, and the
			// bar would still carry that error under a box saying "path is required".
			path, err := repointTarget(strings.TrimSpace(m.pathTA.Value()))
			if err != nil {
				m.repointRefusal = err.Error()
				m.status = ""
				return m, nil
			}
			other, taken, err := client.PathClaimant(attributedCtx(), m.svcOf(), path, m.sess.Plan.ID)
			if err != nil {
				// A door that cannot find out whether a path is taken does not write to
				// it -- client.PathClaimant folds a miss into taken=false, so anything
				// reaching here is a store that could not answer at all. IN THE BOX,
				// like the other three: the sentence itself is unchanged, only its
				// ADDRESS moves.
				m.repointRefusal = "error: " + err.Error()
				m.status = ""
				return m, nil
			}
			if taken {
				m.repointRefusal = pathClaimedRefusal(path, other)
				m.status = ""
				return m, nil
			}
			m.inFlight = true
			// BACK TO THE PANEL THAT OFFERED IT while the write is in flight, which is
			// modeConfirmDeletePlan's y arm verbatim. Nothing has
			// changed yet -- the file is still missing and Panel 1's question is still
			// open. It is also what makes handleActionDone's error path need no arm of
			// its own: the session is untouched by a failure, so the panel standing here
			// is already the right one, with the right body.
			m.pathTA.Reset()
			m.enterSourceFault(m.sourceFault)
			return m, m.runRepoint(path)
		}
		// EVERY OTHER KEY IS AN EDIT, and an edit is what clears the refusal line: a
		// stale refusal under a path the reader has since corrected is worse than none.
		// Checked by VALUE rather than cleared unconditionally, so a bare cursor move
		// leaves a standing refusal alone -- it is the text, not the caret, that the
		// refusal is about.
		before := m.pathTA.Value()
		var repointCmd tea.Cmd
		m.pathTA, repointCmd = m.pathTA.Update(msg)
		if m.pathTA.Value() != before {
			m.repointRefusal = ""
		}
		return m, repointCmd

	case modeRelocate:
		if msg.String() == "esc" {
			m.exitRelocate("")
			return m, nil
		}
		if msg.String() == "enter" {
			return m.dispatchRelocate()
		}
		if msg.String() == "tab" {
			m.skipOrphan(1)
			return m, nil
		}
		switch m.km[msg.String()] {
		case keymap.ActNextThread:
			m.skipOrphan(1)
		case keymap.ActPrevThread:
			m.skipOrphan(-1)
		case keymap.ActToggleResolve:
			return m.dispatchRelocateResolve()
		case keymap.ActMoveDown:
			m.moveCursor(1)
		case keymap.ActMoveUp:
			m.moveCursor(-1)
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
		case keymap.ActQuit:
			return m.quit()
		case keymap.ActList:
			return m.showList()
		}
		return m, nil

	case modeCompose:
		switch msg.String() {
		case "esc":
			m.exitCompose()
			return m, nil
		case "tab":
			return m, m.composeFocusStep(1)
		case "shift+tab":
			return m, m.composeFocusStep(-1)
		case "enter", "ctrl+m":
			// THE RING DECIDES WHAT THIS KEY MEANS, and it is the only key this
			// panel answers that changes meaning with the focus: on a button it
			// presses that button, on the editor it falls out of this switch to the
			// textarea below as InsertNewline, carrying that widget's own cmd back.
			//
			// IT REPLACED ctrl+d: ctrl+d
			// is bubbles' own textarea.KeyMap.DeleteCharacterForward, so the key that
			// posted was a key the reader was already pressing to EDIT, and a post is
			// close to unrecoverable -- nothing in this product edits a comment after
			// the fact. The post now costs a deliberate walk onto a control that says
			// what it does.
			//
			// "ctrl+m" IS THE SAME KEY AND NOT A SECOND ONE. A terminal sends ASCII CR
			// for both (tea.KeyPressMsg{Code: 'm', Mod: tea.ModCtrl}.String() ==
			// "ctrl+m"), which is why textarea.KeyMap.InsertNewline binds the pair --
			// and matching only "enter" is a mistake this package has already made
			// twice, at the repoint pane's own arm above and at the plan list's. Here
			// it would leave ctrl+m falling through to ta.Update while a BUTTON holds
			// the focus, typing a newline into a draft whose caret is off screen.
			//
			// THE TEST IS "NOT THE EDITOR" RATHER THAN THE TWO BUTTONS BY NAME, so
			// this arm keeps the fall-through the comment above promises while what a
			// press MEANS lives in one place for both of the panel's doors -- the
			// click arm calls the same function with the button the pointer landed on
			// (pressComposeButton).
			if m.composeFocus != composeFocusEditor {
				return m.pressComposeButton(m.composeFocus)
			}
		}
		// THE RING IS WHO OWNS THE KEYBOARD, not the widget's own focus flag. This
		// says the same thing composeFocusStep's Blur() already arranges -- a
		// blurred bubbles textarea drops every key handed to it -- and it says it
		// HERE, where the routing decision is, so that a missed Blur() somewhere in
		// the ring's bookkeeping cannot silently start feeding a reader's keystrokes
		// into a draft they are not looking at.
		if m.composeFocus != composeFocusEditor {
			return m, nil
		}
		var cmd tea.Cmd
		m.ta, cmd = m.ta.Update(msg)
		return m, cmd

	case modeSearch:
		return m.updateSearch(msg)
	}
	return m, nil
}

// handleActionDone is the only place a write's result is allowed to touch
// Model state: it runs on the Update loop, never inside the cmd's
// goroutine. Order matters — status/err, then install any reloaded
// session, then re-project, then apply the expand/auto-expand asked for by
// this particular result.
func (m *Model) handleActionDone(msg msgActionDone) (tea.Model, tea.Cmd) {
	// A RESULT THAT SEIZES THE KEYBOARD DROPS AN UNACCEPTED SEARCH TERM. Any arm
	// below that sets a mode of its own can land while the reader is mid-term in
	// modeSearch. The argument is evaluated where the defer is INSTALLED, which is
	// what makes it the mode this result ARRIVED in rather than the mode it leaves.
	defer m.dropSeizedSearch(m.mode)
	m.inFlight = false
	if msg.err != nil {
		// A *client.ConflictError, from this write or from a decline's own retry,
		// opens (or re-opens) the conflict prompt instead of showing a raw error.
		// m.pendingRetry is adopted from msg.retry HERE, never set eagerly at dispatch
		// time: retry lives on the message that actually produced this result, so a
		// write that fails WITHOUT conflicting can never leave a stale closure armed
		// for some later, unrelated conflict. errors.As, never a %v/%w string match --
		// a wrap anywhere upstream would otherwise silently swallow it.
		var conflict *client.ConflictError
		isConflict := errors.As(msg.err, &conflict)
		if isConflict && msg.retry != nil {
			m.mode = modeConflict
			m.conflictTip = conflict.Tip
			m.pendingRetry = msg.retry
			m.pendingExpandBlock = msg.retryExpand
			m.confirm = fmt.Sprintf(
				"the version changed to %s since this session last saw it. Fetch it and re-anchor now? (y) "+
					"or keep what you have and register it on top of the new version? (n)",
				conflict.Tip.Hash.Short())
			return m, nil
		}
		// PANEL 1, RAISED BY ctrl+r. runReload classifies a failed re-read into the same
		// vocabulary the plan list's door and the CLI's already carry, and until
		// this arm existed it could name the fault and had nowhere to put it.
		//
		// THE RULE, ASKED OF THE MESSAGE: the panel is up if and only if a fault is
		// latched. There is no reloaded session to ask here, so the fault comes off
		// msg.sourceFault, which runReload's file arm sets and nothing else does.
		//
		// THE PANEL IS THE ACCOUNT, so the status line goes rather than standing under
		// it: Panel 1's headline says what happened AND asks. A FAULT WITH NO PINNED
		// WORDS FALLS THROUGH to the plain error line below, because someone who
		// pressed a key is owed an account of why nothing happened.
		//
		// A SESSION WITH NO PLAN BEHIND IT GETS NO PANEL: a file opened before any
		// plan was made for it has no cache to fall back on and nothing for f, o or d
		// to write, so its fault falls through to the error line as well.
		if fault := msg.sourceFault; fault != nil && fault.PlanID != "" {
			if m.enterSourceFault(fault) {
				m.status = ""
				return m, nil
			}
		}
		m.status = "error: " + msg.err.Error()
		// msg.retry is nil for a relocate write, so a rehome conflict falls through to
		// here rather than opening the prompt. modeRelocate has no in-mode way to
		// reload (ctrl+r is read-mode only), so naming the way out is the least this
		// message owes.
		if isConflict && msg.fromRelocate {
			m.status += " -- esc, ctrl+r, then m to retry relocating with the new tip"
		}
		return m, nil
	}
	m.pendingRetry = nil
	m.status = msg.status
	// THE PLAN IS GONE, SO THE REVIEW IS OVER -- and this returns before the refresh
	// below, because that refresh reads the plan's placements and approvals and
	// there is no plan left to read them from: it would fail and paint a read error
	// over a write that worked.
	//
	// The panel goes with it. Under Root the model is discarded a moment later and
	// nothing here is seen; on a bare Model msgCloseReview has no handler at all,
	// and leaving Panel 1 up would leave a screen offering to delete a plan that no
	// longer exists.
	if msg.planDeleted {
		m.mode = modeRead
		return m, func() tea.Msg { return msgCloseReview{} }
	}
	if msg.reloaded != nil {
		m.sess = msg.reloaded
	}
	if msg.fromRepoint {
		// It is deliberately the SAME TWO LINES New runs rather than a test
		// of its own: drop the panel, then ask enterSourceFault whether the session in
		// hand still owes one. That answers all three states this arm can be in -- no
		// fault, a gone/unreadable fault, and a fault Panel 1 has no pinned words
		// for. Keying on msg.err instead would have covered only one of the two
		// failure shapes and would have re-decided a question sourceFaultText owns.
		//
		// It runs BEFORE the refresh below because that call can fail and return
		// early, and the mode a reader is left in must not depend on whether
		// re-projecting the document worked.
		m.mode = modeRead
		m.enterSourceFault(m.sess.SourceFault)
	}
	if err := m.RefreshFromSession(attributedCtx()); err != nil {
		m.status = "error: " + err.Error()
		return m, nil
	}
	if msg.expandBlock >= 0 {
		m.expanded[msg.expandBlock] = true
		m.selectCreated(msg.expandBlock, msg.selectTID)
	}
	if msg.fromReload {
		m.expandReanchored()
	}
	m.rerender()
	m.ensureVisible()
	if msg.fromRelocate && m.mode == modeRelocate {
		// The refresh above already removed the handled orphan from the
		// queue (relocated → placed; resolved → filtered out). Advance
		// forward: the successor captured at dispatch, falling back to the
		// queue head if it too has since left the queue. Skipped orphans
		// come around at the wrap, not immediately.
		if q := m.unresolvedOrphans(); len(q) > 0 {
			m.status = msg.status
			next := q[0]
			for _, p := range q {
				if p.Thread.ID == msg.nextTID {
					next = p
					break
				}
			}
			m.targetRelocate(next)
		} else {
			// Queue exhausted: back to read mode with the write's own status
			// ("thread relocated"/"thread resolved") left standing — the mode
			// ending is visible on its own; no completion banner needed.
			m.exitRelocate(msg.status)
		}
	}
	// A state change deferred while this write was in flight is subsumed by the
	// RefreshFromSession above: the service re-reads state on every operation. Only
	// consume the flag -- refreshing again would waste a full re-projection and
	// clobber this write's own status with the generic live-refresh flash.
	m.pendingRefresh = false
	return m, nil
}

// maybeApplyPendingRefresh consumes a state change deferred by msgStateChanged
// while compose or a confirm panel was open. Called at the panels' cancel seams,
// it re-projects from the session and, only if the projection actually changed,
// flashes the same status an idle-mode refresh would show -- the same no-op-echo
// guard as the idle msgStateChanged branch, and needed for the same reason: a
// pure echo of the TUI's own write can set pendingRefresh while that write is
// still in flight. Accepted writes don't come here: their own handleActionDone
// refresh subsumes the pending change and consumes the flag itself.
//
// The inFlight guard: ActApprove opens the confirm panel without checking
// dispatchOK, so its "n"/"esc" cancel can run while an earlier write is still in
// flight. Refreshing then would read m.sess.Plan and m.sess.Exists concurrently
// with that write's goroutine mutating them, the exact race RefreshFromSession's
// callers must never risk. Skipping leaves pendingRefresh set for that write's
// own handleActionDone to consume.
func (m *Model) maybeApplyPendingRefresh() {
	if !m.pendingRefresh || m.inFlight {
		return
	}
	before := m.factsFingerprint()
	if err := m.RefreshFromSession(attributedCtx()); err != nil {
		m.status = "error: " + err.Error()
		return
	}
	m.pendingRefresh = false
	if after := m.factsFingerprint(); after != before {
		m.status = "review updated"
		m.ensureVisible()
	}
}

// expandReanchored auto-expands any thread whose placement carries a badge
// (fuzzy match or moved) so a re-anchor is visible without an extra keypress.
func (m *Model) expandReanchored() {
	for i, vs := range m.views {
		for _, v := range vs {
			if v.Badge != "" {
				m.expanded[i] = true
			}
		}
	}
}

func (m *Model) handleEditorDone(msg editorDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.status = "editor: " + msg.err.Error()
		return m, nil
	}
	m.ta.SetValue(strings.TrimRight(msg.content, "\n"))
	return m, nil
}

// enterRelocate starts triage at the first unresolved orphan.
func (m *Model) enterRelocate() {
	q := m.unresolvedOrphans()
	if len(q) == 0 {
		m.status = "no orphaned threads"
		return
	}
	m.mode = modeRelocate
	m.status = ""
	m.targetRelocate(q[0])
}

// targetRelocate points triage at p and jumps the cursor to the nearest
// surviving ancestor of the orphan's OLD location: p.Thread.Anchor (the
// thread's original anchor, not today's failed reanchor) carries the
// heading path it used to live under. Candidates are gone from this flow —
// they remain in placement.Placement and over MCP for agents — so there is no
// per-candidate position to jump between; this jump only orients the human
// near where the discussion used to be, they still navigate and pick the
// real destination themselves. Walking from the full path down to its
// shortest non-empty prefix finds the deepest heading that still exists; no
// prefix matching anything leaves the cursor exactly where it was.
func (m *Model) targetRelocate(p placement.Placement) {
	m.relocateTID = p.Thread.ID
	path := p.Thread.Anchor.HeadingPath
	for i := len(path); i > 0; i-- {
		if idx := ui.FindHeadingBlock(m.blocks, path[:i]); idx >= 0 {
			m.cursor = idx
			break
		}
	}
	m.rerender()
	// Needed even when nothing moved the cursor: entering relocate just
	// shrank viewHeight by the panel's height, which can leave the cursor's
	// line hidden behind it.
	m.ensureVisible()
}

func (m *Model) currentOrphan() (placement.Placement, bool) {
	for _, p := range m.orphans {
		if p.Thread.ID == m.relocateTID {
			return p, true
		}
	}
	return placement.Placement{}, false
}

func (m *Model) exitRelocate(status string) {
	m.mode = modeRead
	m.status = status
	m.rerender()
	m.maybeApplyPendingRefresh()
}

// dispatchRelocate places the current orphan at the cursor block, anchoring
// through the same session constructors compose uses: a cursor on a section
// makes a section anchor, everything else a span anchor. Which blocks are
// sections is ui.AnchorsAsSection's answer and not this site's -- a quoted
// heading is a heading and is not one.
func (m *Model) dispatchRelocate() (tea.Model, tea.Cmd) {
	if !m.dispatchOK() {
		return m, nil
	}
	p, ok := m.currentOrphan()
	if !ok {
		m.exitRelocate("")
		return m, nil
	}
	if len(m.blocks) == 0 {
		m.status = "document has no content blocks"
		return m, nil
	}
	// The same refusal the first-comment path makes, for the same reason and
	// with the same sentence: a rule has no words for either anchor
	// constructor below to key a thread to. It stays in modeRelocate, the way
	// the guard above does -- the orphan is still waiting and the next cursor
	// move can place it.
	block, ok := m.cursorBlock()
	if !ok || !ui.Anchorable(block) {
		m.status = noContentAtCursor
		return m, nil
	}
	m.inFlight = true
	sess, tid := m.sess, p.Thread.ID
	return m, m.runRelocateAction(m.successorTID(), func(ctx context.Context) (string, error) {
		var a reanchor.Anchor
		var err error
		if ui.AnchorsAsSection(block) {
			a, err = session.SectionAnchor(string(sess.Content), block.HeadingPath)
		} else {
			a, err = session.AnchorForBlockText(string(sess.Content), ui.BlockAnchorSpan(block), block.HeadingPath)
		}
		if err != nil {
			return "", err
		}
		if err := sess.RehomeToAnchor(ctx, tid, a); err != nil {
			return "", err
		}
		return "thread relocated", nil
	})
}

// dispatchRelocateResolve resolves the current orphan without relocating it
// — the common addressed-by-rewrite verdict, where the discussion no longer
// applies to any live location.
func (m *Model) dispatchRelocateResolve() (tea.Model, tea.Cmd) {
	if !m.dispatchOK() {
		return m, nil
	}
	p, ok := m.currentOrphan()
	if !ok {
		m.exitRelocate("")
		return m, nil
	}
	m.inFlight = true
	sess, tid := m.sess, p.Thread.ID
	return m, m.runRelocateAction(m.successorTID(), func(ctx context.Context) (string, error) {
		if err := sess.Resolve(ctx, tid, true); err != nil {
			return "", err
		}
		return "thread resolved", nil
	})
}

// skipOrphan moves triage forward (dir=1) or backward (dir=-1) through the
// unresolved-orphan queue without writing. tab is a standing alias for
// forward; n/N (ActNextThread/ActPrevThread) drive both directions now that
// there are no per-orphan candidates left to cycle through in this mode.
func (m *Model) skipOrphan(dir int) {
	q := m.unresolvedOrphans()
	if len(q) == 0 {
		m.exitRelocate("")
		return
	}
	idx := 0
	for i, p := range q {
		if p.Thread.ID == m.relocateTID {
			idx = i
			break
		}
	}
	n := len(q)
	next := ((idx+dir)%n + n) % n
	m.targetRelocate(q[next])
}

// relocateHeaderLine is the relocate panel's header. With candidates gone, the
// cursor bar IS the enter-target display (dispatchRelocate always anchors to the
// cursor block, never anything named here), so the header has nothing left to
// restate about the target and just orients: how many orphans remain.
//
// Clipped like every other panel line: the painted path's Width().Render
// word-wraps -- inserting real newlines -- instead of truncating, so an unclipped
// line here would silently grow the painted panel's row count past what
// relocatePanelHeight reserved.
func (m *Model) relocateHeaderLine() string {
	return clipRunes(fmt.Sprintf("relocate · %d to triage", len(m.unresolvedOrphans())), m.width-4)
}

// relocateBodyMaxLines caps the relocate panel's comment body at 8 wrapped
// lines; minRelocateViewport is the document viewport the panel must leave
// standing on a short terminal. Squeeze order, tallest to shortest screen:
// the BODY SECTION yields first, shrinking line by line (truncation marker
// riding within its budget) until the viewport is back at
// minRelocateViewport, and only once the body is fully gone (terminals
// under 4+minRelocateViewport rows) does viewHeight continue toward its
// floor of 1. The header and hint never collapse — mode identity and the
// controls stay visible at any height. (Compose can't shrink this way — its
// textarea is a fixed 8-line widget — but this panel derives its own lines,
// so it can.)
const (
	relocateBodyMaxLines = 8
	minRelocateViewport  = 3
)

// relocateBodyLines derives the relocate panel's comment-body section: the
// current orphan rendered as a thread card via ui.ThreadCardLines -- the same
// per-comment attribution header, accent bar and indented replies the document
// view uses for an expanded thread -- capped at relocateBodyMaxLines lines (with
// a trailing "…" line when the card ran longer), the whole section further bounded
// by the terminal-height budget on the constants above. Replies render natively
// inside the card and count against the same cap; a clipped card's trailing "…"
// already says more is there. panelViewPainted calls this once, passing m.styles
// into ui.ThreadCardLines so the lines come back fully styled, and
// relocatePanelHeight derives viewHeight's reservation from the very same call,
// so the two can never drift apart.
//
// Every returned line is already <= width (ui.ThreadCardLines' own guarantee):
// the painted path's Width().Render silently WORD-WRAPS rather than truncating,
// and relying on that guarantee here -- rather than re-clipping -- is what keeps
// this from re-doing or forking the card's own layout.
func (m *Model) relocateBodyLines() []string {
	p, ok := m.currentOrphan()
	if !ok || len(p.Thread.Comments) == 0 {
		return nil
	}
	// Terminal-height budget for this whole section: what the screen has
	// left after the top ground row, the status and help rows, the panel's
	// own header and hint, and the minimum document viewport.
	budget := m.height - 5 - minRelocateViewport
	if budget < 0 {
		budget = 0
	}
	width := m.width - 4
	card := ui.ThreadCardLines(ui.ThreadView{Thread: p.Thread}, width, m.pseudonym, m.styles)
	if bodyCap := min(relocateBodyMaxLines, budget); len(card) <= bodyCap {
		return card
	}
	if budget <= 0 {
		return nil
	}
	// Truncated: the "…" marker line rides beyond relocateBodyMaxLines on a
	// tall terminal (8 card lines + "…") but within the terminal budget
	// when that is the binding cap, so the section never exceeds what
	// viewHeight was told.
	keep := min(relocateBodyMaxLines, budget-1)
	lines := append([]string{}, card[:keep]...)
	return append(lines, "…")
}

// relocatePanelHeight is viewHeight's relocate-mode reservation, summed the
// same way compose's viewHeight case sums m.ta.Height()+2: the header line,
// the content-driven body section, and the hint line.
func (m *Model) relocatePanelHeight() int {
	return 1 + len(m.relocateBodyLines()) + 1
}

// confirmLines is confirmGroups' rows in order -- the shape every caller that only
// DRAWS the panel wants: panelViewPainted's strip arm, repointGroups and
// confirmPanelHeight. The wrap, the budget and the truncation are one level down.
func (m *Model) confirmLines() []string {
	return flattenConfirmGroups(m.confirmGroups())
}

// confirmGroups wraps m.confirm to the panel's width via real word-wrap (inserting
// line breaks, never truncating within a line -- unlike clipRunes, which exists for
// a single line that must fit as-is by losing its tail), hard-wraps whatever
// word-wrap could not break, and caps the result to confirmBudget(). It answers the
// LOGICAL LINES still separated: one group of screen rows per surviving line.
//
// An unwrapped line here silently grows the painted panel's row count past whatever
// viewHeight reserved for it. It is shared by every panel that draws prose here, by
// panelViewPainted and by confirmPanelHeight, so the reservation can never drift
// from what actually gets painted. FIND THE MEMBER MODES BY WHAT viewHeight RESERVES
// FOR, never by a list in prose: this comment has carried a wrong one four times.
//
// THE TWO EXTRA PASSES:
//
//   - ansi.Wordwrap's only breakpoints are a space and a "-", so a single
//     unbreakable token wider than width comes out still over width. ansi.Hardwrap
//     forces the break, and it is safe over the WHOLE already-wrapped string in one
//     call: every existing "\n" is its own forced break. Without it,
//     Width(m.width).Render breaks the over-wide line itself and paints MORE rows
//     than confirmPanelHeight reserved (measured at 80x24, an 86-rune hyphen-free
//     path: 10 against a reserved 9).
//   - truncateConfirmGroups caps the row count to confirmBudget(), keeping the head
//     and the TAIL -- the key rows (a strip panel's "(y/n)" legend) an
//     irreversible-action panel must never silently drop. Without it a long body
//     wrapped to a narrow box's interior paints more rows than the terminal has.
//
// m.width <= 12 IS A RESIDUAL, measured rather than assumed away: row 0 carries a
// 3-cell "── " prefix on top of whatever this returns, so at the width floor
// (max(m.width-4, 10), engaged below width 14) row 0 can render 13 cells against a
// narrower m.width.
//
// A CENTRED MODE SUBTRACTS SIX AND NOT FOUR, which is a BRANCH here: the box spends
// THREE cells a side (a border cell and two of padding), so m.width-6 is the
// interior a full-width box has where m.width-4 is what the bottom strip wants.
// Reading the coincidence of the two as a shared rule is what would have made the
// wider padding overflow -- at 30 columns a box drawn round 26-cell lines is 32, two
// columns past the terminal. The residual is the mirror of the one above.
//
// THE GROUPS ARE A SHAPE AND NOT A COUNT: a box given them cannot ask "which line
// does the panel lead with" wrongly, whatever the first line is and however many
// rows it takes. ListModel.confirmGroups is this method ported onto that model's own
// arithmetic; only the WIDTH each accessor asks for is each model's own.
func (m *Model) confirmGroups() [][]string {
	width := max(m.width-4, 10)
	if drawsCentredPanel(m.mode) {
		width = centredInterior(m.width)
		// modeConfirmDeletePlan ALONE narrows this further, to Panel 1's own width
		// rather than the terminal's -- see deleteConfirmInterior for the argument
		// (the stability rule binds the box's SIZE, not only its
		// placement). Every other centred member is entered from read mode or from a
		// panel with no box of its own to hold still against.
		if m.mode == modeConfirmDeletePlan {
			width = m.deleteConfirmInterior()
		}
	}
	// THE CONTROL-BYTE FILTER RUNS BEFORE THE WRAP, which is ListModel.confirmLines'
	// own placement. It is spent inside wrapConfirmGroups -- once per logical line
	// rather than once over the body, for the lipgloss block-padding reason that
	// function measures -- which changes neither what it covers nor that it runs
	// before any wrap sees the string. EVERY WRITER of m.confirm reaches it, and some
	// of them interpolate text draftplane did not write -- a plan's title, a file's
	// path, the OS's own error -- verbatim, so without it a hostile document or agent
	// could put a backspace forgery, an unterminated APC or an OSC 8 hyperlink on the
	// very panel that asks a human to authorize an action.
	//
	// And BEFORE the budget, never after it: a bare C0 byte is ZERO cells to
	// ansi.Wordwrap and ONE to the Width().Render that draws the row, so a panel
	// sized to exactly its width came back a row taller than confirmPanelHeight had
	// reserved. A Control Picture is one cell to both authorities.
	//
	// ui.VisibleControls and not a second predicate, for the reason ui/control.go's
	// exports give. This method and the list's are line-for-line the same by design,
	// and a filter on one of a matched pair is this project's recurring defect
	// wearing a security label.
	return truncateConfirmGroups(wrapConfirmGroups(m.confirm, width, m.styles.FocusHeader), m.confirmBudget())
}

// repointGroups is the CENTRED path pane's body, in the same shape centredPanelBox
// takes from confirmGroups: one group of screen rows per logical line, so the box
// can ink the line the panel leads with without asking how many rows a headline
// took.
//
// IT IS A SECOND BODY AND NOT A SECOND PANEL. Everything below the body is shared
// with Panel 1 -- the box, the border, the padding, the warn head, the centring,
// the budget -- and the only thing this decides is what the rows say. That is what
// makes re-point's pane and Panel 1 read as one panel that changed its question
// rather than as two panels one keystroke apart.
//
// THE BLANK ROWS ARE SPENT FIRST, and that is the one rule truncateConfirmGroups
// could not have been left to apply: its policy keeps the head, the last logical
// line and as much of the tail as fits -- correct for prose, and wrong here,
// because the row it would drop on a short terminal is the INPUT row. So the
// padded body is offered only while it fits whole.
//
// THE GLUE IS A METHOD and the policy is not: repointPanelGroups is
// package-level because nothing in it is model-specific once handed a widget, a
// style set, a width and a budget.
//
// THE BOX'S WIDTH IS PANEL 1'S: repointInterior is handed Panel 1's own lines,
// because f is pressed on that box and the pane must hold its width.
func (m *Model) repointGroups() []panelGroup {
	interior := repointInterior(m.confirmLines(), m.width)
	return repointPanelGroups(m.pathTA, m.styles, interior, m.confirmBudget(), m.repointRefusal)
}

// repointPanelGroups is the padded-first, unpadded-fallback policy repointGroups
// reduces to once its own glue (the widget, the styles, the interior, the budget
// and the current refusal) is supplied. A padded body is offered while it fits
// whole; once it does not, repointFit takes over rather than the generic
// truncateConfirmGroups.
func repointPanelGroups(ta textarea.Model, st *ui.Styles, interior, budget int, refusal string) []panelGroup {
	if padded := repointBody(ta, st, interior, true, refusal); panelGroupRows(padded) <= budget {
		return padded
	}
	return repointFit(repointBody(ta, st, interior, false, refusal), budget)
}

// repointFit is what a body four rows tall (five once a refusal is standing) does on
// a terminal with room for fewer, and it is deliberately NOT truncateConfirmGroups.
// That function keeps the head and the LAST logical line and elides the middle,
// which is right for prose and wrong for a panel whose middle is the input: it would
// drop the field and leave a pane asking for a path with nowhere to type it.
//
// SO IT DROPS FROM THE END, BUT NEITHER THE FIELD NOR THE WAY OUT EVER GOES.
// Shedding BOTH key lines grows the box tall enough that its own bottom border lands
// on the terminal's last row, covering the very help bar the drop was betting on --
// MEASURED at 31x12 with a short refusal standing, and reachable at 80x12 and 80x10
// past a path of roughly 100 and 60 cells.
//
// So only ONE key line is shed first, "enter · submit" -- repointPaneHint still names
// enter whenever the bar is visible. Then the refusal, row by row and then whole,
// then the headline whole. THE WAY OUT ITSELF is never a candidate for as long as it
// and the field can still fit together, and there is no "…": nothing is elided from
// the MIDDLE of a run. BELOW BUDGET 2, THE FIELD STILL WINS: an input pane with
// nothing to type into is worse than one with no key named on it at all.
func repointFit(groups []panelGroup, budget int) []panelGroup {
	budget = max(budget, 1)
	for panelGroupRows(groups) > budget && len(groups) > 2 {
		// cut is the group immediately before the way out -- never the way out
		// itself, which sits at len(groups)-1 and is a candidate only once cut can no
		// longer avoid landing on it (the budget < 2 branch below).
		cut := len(groups) - 2
		if groups[cut].ink == inkField {
			if budget >= 2 {
				// Everything droppable other than the way out is gone and the field alone
				// beside it is still over budget. Handing back both is the same call
				// minConfirmBudget makes one level up: show the two rows that matter and
				// let ui.Overlay's clamp answer for the geometry.
				return []panelGroup{groups[cut], groups[len(groups)-1]}
			}
			// The terminal has room for one content row, not two: the field is
			// repointPanelGroups' own irreducible reason, so it survives alone.
			return []panelGroup{groups[cut]}
		}
		if n := len(groups[cut].rows); n > 1 {
			groups[cut].rows = groups[cut].rows[:n-1]
			continue
		}
		groups = append(groups[:cut:cut], groups[cut+1:]...)
	}
	return groups
}

// panelGroupRows is how many screen rows a centred body occupies.
func panelGroupRows(groups []panelGroup) int {
	n := 0
	for _, g := range groups {
		n += len(g.rows)
	}
	return n
}

// repointBody is repointGroups' rows with the padding decision made by the caller.
// The static lines go through wrapConfirmGroups -- the same wrap, hard-wrap and
// control-byte filter every other body in this box gets -- and the input row does
// not, because it is clipped rather than wrapped (repointInputRow).
//
// refusal IS ABSENT WHEN "": whichever refusal last fired, held on the model
// between commits rather than composed here -- re-point's own checks (repointTarget,
// and the claimed-path sentence pathClaimedRefusal composes). It goes
// through wrapConfirmGroups at the SAME interior every other row is held to,
// because a refusal built from an interpolated abs path is unbounded and
// centredBox derives the box's WIDTH from the widest row it is handed -- so a
// refusal drawn any wider than interior would grow the box past the panel it
// replaced the instant a long path was typed.
//
// ITS OWN ZONE (inkWarn) AND NOT inkHead: it is not the line the panel leads with,
// and reusing inkHead for a row that is not the head would make that constant's
// own doc comment false about whichever row wore it. Visually the two paint
// identically -- theme.Warn is the one warning colour this box has.
func repointBody(ta textarea.Model, st *ui.Styles, interior int, padded bool, refusal string) []panelGroup {
	groups := headThenBody(wrapConfirmGroups(repointPaneHeadline, interior, st.WarnHeader))
	blank := panelGroup{rows: []string{""}}
	if padded {
		groups = append(groups, blank)
	}
	groups = append(groups, panelGroup{rows: []string{repointInputRow(ta, st, interior)}, ink: inkField})
	if refusal != "" {
		if padded {
			groups = append(groups, blank)
		}
		for _, g := range wrapConfirmGroups(repointRefusalGlyph+refusal, interior, st.WarnHeader) {
			groups = append(groups, panelGroup{rows: g, ink: inkWarn})
		}
	}
	if padded {
		groups = append(groups, blank)
	}
	for _, g := range wrapConfirmGroups(repointPaneKeys, interior, st.Chrome) {
		groups = append(groups, panelGroup{rows: g})
	}
	return groups
}

// repointInterior is the width of the box a re-point pane draws in, and it is THE
// WIDTH OF THE PANEL IT REPLACES rather than a size of its own: lines is that
// panel's own wrapped body (each caller's own confirmLines()) and width is the
// terminal's. f is pressed on a box that is already on screen and already centred;
// a pane that sized itself off its own shorter lines would shrink that box
// horizontally under the reader at the moment it asks them to type, moving the one
// row they are about to look at.
//
// SHARED BY BOTH DOORS: one function taking each caller's own lines rather than
// one method per model reaching for its own confirmLines() by name.
//
// IT IS DERIVED AND NOT A CONSTANT, and exactly: each caller's m.confirm still
// holds the panel it replaced while re-point owns the keyboard -- neither door's
// entry into this mode writes that field, and every exit re-derives the panel it
// hands back to -- so confirmLines() answers the very rows the box was drawn from,
// and moves when that panel's wording changes.
//
// THE CAP IS centredInterior's OWN and the floor is repointMinInterior: the cap
// keeps the box inside a narrow terminal (ui.Overlay would clip its right-hand
// edge rather than let a row wrap), and the floor covers a panel body truncated so
// hard that the widest surviving row leaves no field.
//
// L AND x NEVER CALL IT. They are pressed on no panel -- browse in the plan list,
// the document in the plan view -- and at both doors m.confirm is whatever panel
// closed last rather than one the pane replaces, so lines read from it sized the
// pane off a panel the reader had already dismissed. Their pane takes the full
// centred measure instead; each model's repointGroups carries
// the measurement and the ruling.
func repointInterior(lines []string, width int) int {
	w := 0
	for _, l := range lines {
		w = max(w, ansi.StringWidth(l))
	}
	return min(max(w, repointMinInterior), centredInterior(width))
}

// confirmFaultInterior is deleteConfirmInterior's measurement: the width of the
// fault panel the confirm replaces, re-derived from its text and the model's own
// confirmBudget.
func confirmFaultInterior(faultText string, width, budget int, style lipgloss.Style) int {
	lines := truncateConfirmLines(wrapConfirmGroups(faultText, centredInterior(width), style), budget)
	return repointInterior(lines, width)
}

// deleteConfirmInterior is modeConfirmDeletePlan's own box width, and it is
// deliberately NOT this panel's own widest line. ordinaryDeleteConfirmText is a
// headline, a blank and two key lines against modeSourceFault's ten -- MEASURED at
// 80x24 over the "gone" fault: an empty-titled plan's confirm text is 34 cells wide
// against the panel's 57, so a box sized off its own content would draw a 40-cell
// box the instant d is pressed against the 63-cell one the reader is already looking
// at. THE FAILURE MODE: "the box will shrink under the reader at the moment they are
// asked to confirm a destructive act."
//
// THE DECISION IS STABLE: pressing esc, then d, must show the same box, in the
// same place, naming its keys the same way. A box
// that resizes is a different box by the reader's own eye, so "the same box" binds
// the SIZE and not only the column -- which is what modeRepoint's f already answers
// the same way one door over.
//
// repointInterior DOES THE MEASUREMENT, called and not copied: widest line, floored
// at repointMinInterior, capped at centredInterior. What it cannot be handed is
// m.confirmLines() -- the "d" arm overwrites m.confirm before this mode is entered
// -- so sourceFaultText(m.sourceFault) re-derives Panel 1's own body instead, off
// the one field that survives the trip.
//
// THE WIDTH ALONE IS NOT WHAT MAKES THE BOX STABLE, and that is centredGroups' job:
// centredBox derives the box from the WIDEST ROW IT IS HANDED, not from a wrap
// threshold, so a short confirm text still measures its own short lines unless
// something is padded out to match. What this method fixes is the OTHER direction: a
// long title would, without the centredInterior cap, wrap the box WIDER than the
// panel it replaced -- MEASURED at 80x24 with a 126-cell title, the box holds at 63.
func (m *Model) deleteConfirmInterior() int {
	return confirmFaultInterior(sourceFaultText(m.sourceFault), m.width, m.confirmBudget(), m.styles.FocusHeader)
}

// repointInputRow is the one row of this pane that is not a literal: the text as the
// reader has typed it, and a cursor cell, on a band of the box's whole interior
// width.
//
// THE BAND IS THE BOX. A box inside a box is two frames for one thing, and the
// ground change already says everything a second border would, so the field is a ROW
// IN A DIFFERENT ZONE (inkField -> ui.Styles.Field) carrying no rule of its own.
//
// IT CLIPS AND NEVER WRAPS, which is searchInputLine's rule one panel over and holds
// for a second reason here. Its own: the newest characters are the ones being typed,
// so a row that kept its HEAD would freeze at the first keystroke past the edge. And
// this box's: a wrapped field grows the box DOWNWARD mid-keystroke.
//
// IT IS PADDED OUT TO THE FULL INTERIOR, AND THAT IS WHAT MAKES THE BOX THE WIDTH
// ITS CALLER ASKED FOR (repointInterior): centredPanelBox sizes the box off the
// WIDEST ROW IT IS HANDED, so a field that stopped at the cursor would leave the
// box as wide as the headline and no wider.
//
// THE WINDOW IS KEPT AROUND THE CURSOR AND NOT AROUND THE END OF THE STRING:
// leftClip over the text BEFORE the cursor is what guarantees the cursor cell is on
// screen, and whatever budget is left goes to the text after it.
//
// A NEWLINE IS THE ONE CONTROL BYTE ui.VisibleControls LEAVES ALONE, because in every
// other body reaching this box it IS a line break. Here it cannot be: this row is one
// row of a box whose height is already decided, so it is spent as the Control
// Picture the rest of the C0 range gets.
func repointInputRow(ta textarea.Model, st *ui.Styles, interior int) string {
	style := st.Field
	caret := repointCursor
	value := strings.ReplaceAll(ta.Value(), "\n", "\u240a")
	runes := []rune(value)
	col := min(max(ta.Column(), 0), len(runes))
	field := max(interior-ansi.StringWidth(repointFieldInset)-ansi.StringWidth(caret), 0)
	head := leftClip(ui.VisibleControls(string(runes[:col]), style), field)
	tail := ansi.Truncate(ui.VisibleControls(string(runes[col:]), style), max(field-ansi.StringWidth(head), 0), "")
	return padRightIn(repointFieldInset+head+caret+tail, interior, style)
}

// panelMeasure is the WIDEST a centred panel's text may be, in display cells, and
// it is the one number that keeps a box a box rather than a full-width band.
//
// THE FIGURE IS DERIVED FROM THE TERMINAL AND NOT PICKED. centredPanelBox draws a
// border cell and two of padding each side, so the box is its widest row plus six:
// 72 makes a 78-cell box, which fits an 80-column terminal -- the width this
// product's own panels have always been measured at -- with a cell to spare either
// side.
//
// WITHOUT IT THE BOX HAD NO UPPER BOUND AT ALL: driven with a single unwrapped
// paragraph, 76 cells at an 80-column terminal, 120 AT 120 -- edge to edge -- and
// 124 at 200. SO IT IS WHAT LETS A PANEL'S BODY BE PROSE: three of the bodies in
// the box carry hand-placed newlines whose only job is to hold the box in, which
// is layout hidden inside a string and the first thing that breaks when the words
// are edited.
//
// IT BINDS ONLY THE CENTRED PLACEMENT. A bottom strip is full-width by design and
// the confirm panels outside the box still draw as one; narrowing their text to 72
// would reflow panels this pass is not touching.
const panelMeasure = 72

// centredInterior is the text width a centred panel gets on a terminal of
// this width: the box's own six cells of frame off the top, floored so the
// arithmetic never goes negative, and capped at the measure.
//
// ONE FUNCTION AND NOT THE EXPRESSION WRITTEN AT EACH READER, and the f gesture
// is why: confirmGroups wraps to it and repointInterior caps to it, so the panel
// and the input pane it hands off to cannot come to disagree about how wide this
// box is -- which is the property the whole f gesture rests on. L's and x's path
// pane takes it whole at both doors (Model.repointGroups), which is what shows a
// long path there.
func centredInterior(width int) int {
	return min(max(width-6, 10), panelMeasure)
}

// confirmBudget is the most LINES confirmLines() may return without letting the
// alt-screen total exceed m.height -- ListModel.confirmBudget's counterpart for
// this model, solved against THIS model's own arithmetic.
//
// viewPainted is exactly vh + confirmPanelHeight + 3 (a top ground row, status,
// help) rows by construction, where vh is viewHeight()'s own m.height - 3 - H with
// its "if h < 1" floor. So while H <= m.height-4, vh lands on m.height-3-H and the
// total comes out to exactly m.height; past that point vh floors to 1 and the
// total becomes H+4. The invariant is therefore "H must never exceed m.height-4"
// -- m.height-5 for the LINE count returned here, since confirmPanelHeight adds
// the spacer row back on top. Every number went up by one when the ground row
// joined the top of the document: the budget is only correct against a chrome it
// names.
//
// minConfirmBudget (1, shared with the list) is the floor, so a very short
// terminal still shows the panel's last line rather than nothing; the total can
// still exceed m.height below about 6 rows.
//
// A CENTRED MODE IS SOLVED AGAINST A DIFFERENT SHAPE and takes the branch in the
// body: it reserves nothing and displaces nothing, so only the box drawn around it
// bounds it. It is a BRANCH AND NOT A SECOND METHOD on purpose: one budget feeding
// one confirmLines feeding one truncateConfirmLines is what keeps the two
// placements from becoming two panels.
//
// ⚠️ THE BOX BUYS ROWS AND SPENDS COLUMNS, AND THE SECOND HALF IS THE ONE
// NOBODY MEASURES. The branch below is the row half, and it is a gain: a
// centred mode gets m.height-4 where the strip got m.height-5. The column
// half runs the other way and is not visible from here at all, because it is
// centredInterior's and not this function's: a strip is m.width-4 cells wide,
// while the box at the SAME terminal width is far narrower once its margins
// and border are paid for -- at a 40-column terminal the box interior
// measures 14 cells against the strip's 36. Fewer columns means more wrapped
// rows for the same sentence, which is how a body that fit the strip's
// smaller row budget can still blow the box's larger one.
//
// So below roughly 40x24 a centred panel can lose the middle of its body while
// its key rows stay legible, because they are the protected tail. RULED
// ACCEPTABLE on the ground that the document itself is
// unreadable at 40 columns so nobody is genuinely reviewing a plan there, and on
// the ground that this is THE BOX'S property: modeSourceFault and
// modeConfirmDeletePlan have had it since they moved, and the 30x11 measurement
// in the branch below is the same effect seen from the row side.
//
// WHAT WOULD ACTUALLY FIX IT is widening the box at narrow terminals, which
// is centredInterior's decision and touches every centred panel at once --
// its own change, not a clause here. Do not "fix" it by sending a panel
// back to the strip: placement is drawsCentredPanel's routing decision and
// making it size-dependent would give this model two panels again.
func (m *Model) confirmBudget() int {
	b := m.height - 5
	if drawsCentredPanel(m.mode) {
		// THE BOX'S OWN ARITHMETIC AND NOT THE STRIP'S, derived the same way the
		// paragraph above derives the strip's. A centred panel displaces no document
		// row and reserves nothing, so the only thing bounding it is the box itself:
		// centredPanelBox draws a border row and a blank row above the lines and the
		// same two below, so the whole box is len(lines)+4 rows and "the box must fit
		// the terminal" is len(lines) <= m.height-4. One row more than the strip could
		// afford at the same height, and not slack to be traded away: MEASURED at
		// 30x11, the strip's budget of 6 leaves only d and esc on screen and the box's
		// 7 buys back the head of f -- which is the size at which the panel stops
		// fitting AT ALL, so every row is one the reader would otherwise not get.
		b = m.height - 4
	}
	if b >= minConfirmBudget {
		return b
	}
	return minConfirmBudget
}

// confirmPanelHeight is viewHeight's reservation for every confirm panel
// (the set confirmLines names): the wrapped, hard-wrapped, budget-truncated
// confirm text's own rows, plus one blank spacer row (matching the pre-wrap
// layout's trailing blank line), derived from the exact same lines the panel
// renderer draws. It can no longer exceed confirmBudget()+1.
func (m *Model) confirmPanelHeight() int {
	return len(m.confirmLines()) + 1
}

// clipRunes truncates s to at most n runes, ending with … when clipped.
// (Rune count, not display width: consistent with the existing panels'
// handling; CJK-width overflow is an accepted limitation there too.)
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// relocateHintText derives modeRelocate's key reference from the live keymap so
// rebinds show up. Shared with the help bar (modeHints), unclipped: the panel
// clips it to its own budget through relocateHint below, and the bar is truncated
// by whichever View draws it, so the two cannot name different keys while
// differing in where they cut.
//
// It names the keys that TRIAGE and not the navigation this mode also takes,
// because the panel's whole subject is the orphan under the cursor. Naming fewer
// keys than a mode owns is silence, which the rule this file follows allows;
// naming keys it does not own is not.
//
// WHICH IS WHY ENTER IS NOT UNCONDITIONAL. dispatchRelocate refuses a placement on
// a block with nothing to anchor to, so over a rule "enter place here" would name
// a key this cursor position rejects. The clause is swapped for the refusal's own
// sentence out of blockHints (app/model.go), the one table both bars read, so the
// two surfaces cannot come to disagree about which blocks take a write.
func (m *Model) relocateHintText() string {
	place := "enter place here"
	if hint := m.blockHint(); hint != "" {
		place = hint
	}
	return fmt.Sprintf("%s · %s resolve · %s/%s orphans · esc done",
		place,
		findKey(m.km, keymap.ActToggleResolve),
		findKey(m.km, keymap.ActNextThread), findKey(m.km, keymap.ActPrevThread))
}

// relocateHint is relocateHintText clipped to the relocate panel's own width.
func (m *Model) relocateHint() string {
	return clipRunes(m.relocateHintText(), max(m.width-2, 1))
}

// composeTarget derives the compose panel's header text as plain WORDS: the
// renderer that draws it (panelViewPainted) adds the style wrapper and nothing
// else. It stays a derivation of its own so the header's words are decided in one
// place rather than inside a paint step.
func (m *Model) composeTarget() string {
	if m.replyTo != "" {
		return "reply to thread"
	}
	if len(m.blocks) == 0 {
		return "new comment"
	}
	target := "comment on: " + strings.Join(m.blocks[m.cursor].HeadingPath, " › ")
	if ui.AnchorsAsSection(m.blocks[m.cursor]) {
		target += " (section)"
	}
	return target
}

// panelView is the panel's ROWS ALONE. It drops the button geometry
// panelViewPainted answers, which is what a caller that is not drawing a frame
// must do: that geometry's row is panel-relative until viewPainted completes it,
// and viewPainted is the only writer of m.composeButtons.
func (m *Model) panelView() string {
	panel, _ := m.panelViewPainted()
	return panel
}

// openEditor is DORMANT, and deliberately so: the ctrl+e binding that was its
// only caller was removed and the machinery kept, because Draftplane is
// pre-release and nobody has yet told us whether composing a comment in $EDITOR
// is a gesture they want. Nothing reaches it -- compose mode's switch answers
// esc, tab, shift+tab, enter and its own ctrl+m alias, and none of them this
// -- so the round trip below is unexercised, and the editorDoneMsg arm in
// Update that receives its result is unreachable with it.
//
// WHY THE BINDING WENT AND NOT THE CODE. ctrl+e is the bubbles textarea's own
// KeyMap.LineEnd, so intercepting it here cost a reader end-of-line inside the
// one panel they type prose in, and every free replacement was worse: ctrl+v is
// the textarea's Paste, ctrl+w its DeleteWordBackward, ctrl+t its transpose and
// Zellij-reserved besides, and ctrl+i IS Tab and could never have matched. The
// keystroke was the scarce thing; the feature could wait.
//
// FOLLOW-UP, and it is a deletion rather than a decision to revisit: if no user
// asks for an editor round trip, remove this function, editorDoneMsg,
// handleEditorDone and the Update arm that dispatches it. Restoring it instead
// means picking a key -- ctrl+x is the one candidate left standing, tab and
// shift+tab having gone to the compose focus ring (composeFocusStep) -- and
// NOT ctrl+e, which is the textarea's and should stay so.
//
// The nolint is what keeps `make check` honest about all of the above: .golangci.yml
// enables `unused`, which is right to flag a function nothing calls, and the
// suppression is the place this reasoning has to live so that removing the
// directive is the same act as removing the excuse.
//
//nolint:unused // dormant by decision; see the note above for the removal condition
func openEditor(initial string) tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	f, err := os.CreateTemp("", "draftplane-comment-*.md")
	if err != nil {
		return func() tea.Msg { return editorDoneMsg{err: err} }
	}
	path := f.Name()
	_, _ = f.WriteString(initial)
	_ = f.Close()
	cmd := exec.Command(editor, path)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		defer func() { _ = os.Remove(path) }()
		if err != nil {
			return editorDoneMsg{err: err}
		}
		content, readErr := os.ReadFile(path)
		return editorDoneMsg{content: string(content), err: readErr}
	})
}

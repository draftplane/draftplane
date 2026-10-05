package app

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/draftplane/draftplane/client/recent"
	"github.com/draftplane/draftplane/session"
)

// RecordOpen remembers that a human opened sess in the terminal, for the plan
// list's Recently opened. The doors call it, never session.Open: an open from
// MCP must leave no trace here.
func RecordOpen(recentPath string, sess *session.Session, now time.Time) error {
	if recentPath == "" || sess == nil {
		return nil
	}
	e := recent.Entry{Path: sess.Path, OpenedAt: now.UTC()}
	if sess.Exists {
		e.PlanID = sess.Plan.ID
	}
	return recent.Touch(recentPath, e)
}

// recordOpenCmd is RecordOpen off the loop, for Root's list door. It answers no
// message: a failed record never reaches the reader.
func recordOpenCmd(recentPath string, sess *session.Session) tea.Cmd {
	if recentPath == "" {
		return nil
	}
	return func() tea.Msg {
		_ = RecordOpen(recentPath, sess, time.Now())
		return nil
	}
}

// forgetRecent drops e from Recently opened after a delete, so the row leaves
// on the same redraw as the plan it named. Best-effort like RecordOpen:
// a failed forget never fails the delete it follows, and its only
// visible cost is a stale row that d then removes.
func forgetRecent(recentPath string, e recent.Entry) {
	if recentPath == "" {
		return
	}
	_ = recent.Forget(recentPath, e)
}

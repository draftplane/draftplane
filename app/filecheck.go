package app

import (
	"errors"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/domain"
)

// listFileCheckBudget is how long one load of the list waits on what it asks of
// files, for the whole batch at once and never per file: its plans'
// files and Recently opened's are one fileBatch. The load's answer
// waits on those asks, and one on a hung network mount blocks for as long as the
// mount does. So whatever has not answered when the budget runs out is "don't
// know": a plan keeps its circle, and a Recently opened file is not drawn and
// the next entry takes its slot. A hung mount delays the list by this much at
// most, rather than holding it hostage. A local disk answers in microseconds,
// far inside it.
//
// EVERYTHING A LOAD ASKS OF A FILE THE LIST NAMES IS INSIDE IT: a plan's stat,
// and a Recently opened file's stat and the title read after it, which are one
// call (fileAsk), so a mount that answers the stat and hangs on the read is
// bounded too. Outside it is only what the load reads to know its plans at all:
// recent.json and this machine's plan store.
const listFileCheckBudget = 200 * time.Millisecond

// listFileCheck is the process's one fileCheck, so every load shares the asks
// still out (see fileCheck). ListModel.files starts as this.
var listFileCheck = newFileCheck(os.Stat, os.ReadFile, time.After)

// fileCheck answers a load's file asks under listFileCheckBudget, in a batch
// per load: two loads running at once each have their own batch and budget, and
// share only the asks still out. stat, read and after are os.Stat,
// os.ReadFile and time.After in production; a test injects its own, to hang a
// stat or a read, or spend the budget, without touching a disk or a clock.
//
// AN ASK THE BUDGET OUTRAN IS ABANDONED, NOT CANCELLED: os.Stat and os.ReadFile
// take no context, so its goroutine blocks until the mount answers, and the
// answer is then dropped. It is JOINED rather than repeated: a check that finds
// the same ask still out waits on that one instead of starting another. So a
// mount that never answers holds one goroutine, and the OS thread stuck in its
// syscall, per ask, however often the list refreshes -- at most two per path,
// a plan's stat and a Recently opened file's. One per refresh would grow
// without bound on a list that refreshes at every state change. Once the ask
// answers, the next check asks afresh.
type fileCheck struct {
	stat  func(name string) (fs.FileInfo, error)
	read  func(name string) ([]byte, error)
	after func(d time.Duration) <-chan time.Time

	mu       sync.Mutex
	inflight map[fileAsk]*fileCall
}

// fileAsk is one question a load asks of a path. A plan's file is only
// stat'ed; a Recently opened file with no plan is also read for its
// title (fileTitle), in the same call, so the budget bounds the read
// as well as the stat. The kind is part of the key, so the two are never
// joined: a plan's stat must not wait on a read that hangs, and a bare stat has
// no title to give.
type fileAsk struct {
	path  string
	title bool
}

// fileCall is one ask, shared by every check that asks it while it is out.
// done closes once the rest is set: err is the stat's, and a title ask also
// sets title and gone.
type fileCall struct {
	done  chan struct{}
	err   error
	title string
	gone  bool
}

func newFileCheck(stat func(string) (fs.FileInfo, error), read func(string) ([]byte, error), after func(time.Duration) <-chan time.Time) *fileCheck {
	return &fileCheck{stat: stat, read: read, after: after, inflight: make(map[fileAsk]*fileCall)}
}

// start answers the same ask still out, or starts one.
func (c *fileCheck) start(ask fileAsk) *fileCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	if call, ok := c.inflight[ask]; ok {
		return call
	}
	call := &fileCall{done: make(chan struct{})}
	c.inflight[ask] = call
	go func() {
		info, err := c.stat(ask.path)
		call.err = err
		if ask.title {
			call.title, call.gone = fileTitle(c.read, ask.path, info, err)
		}
		c.mu.Lock()
		delete(c.inflight, ask)
		c.mu.Unlock()
		close(call.done)
	}()
	return call
}

// fileBatch is one load's file asks: its plans' files and Recently opened's,
// under ONE listFileCheckBudget. Each part of the load adds the asks it knows
// as it learns them, and each starts at once; the first wait arms the budget
// for all of them, and every later wait answers the same asks. One goroutine,
// the load's own, uses a batch.
//
// EVERY ADD COMES BEFORE THE FIRST WAIT. The load adds Recently opened's files
// before its plans' check waits, and reads its answers after. An ask added
// after the wait is never waited on, so it would be "don't know" every time.
type fileBatch struct {
	check    *fileCheck
	calls    map[fileAsk]*fileCall
	answered map[fileAsk]*fileCall // nil until the wait
}

func (c *fileCheck) batch() *fileBatch {
	return &fileBatch{check: c, calls: make(map[fileAsk]*fileCall)}
}

// add starts asks, or joins the ones still out.
func (b *fileBatch) add(asks ...fileAsk) {
	for _, a := range asks {
		if _, ok := b.calls[a]; !ok {
			b.calls[a] = b.check.start(a)
		}
	}
}

// wait answers the asks that answered within the batch's one budget. An ask it
// leaves out is "don't know".
func (b *fileBatch) wait() map[fileAsk]*fileCall {
	if b.answered != nil {
		return b.answered
	}
	b.answered = make(map[fileAsk]*fileCall, len(b.calls))
	if len(b.calls) == 0 {
		return b.answered
	}
	budget := b.check.after(listFileCheckBudget)
	spent := false
	for a, call := range b.calls {
		if !spent {
			select {
			case <-call.done:
			case <-budget:
				spent = true
			}
		}
		// Asked again, without waiting, so an ask that answered as the budget ran
		// out still counts.
		select {
		case <-call.done:
			b.answered[a] = call
		default:
		}
	}
	return b.answered
}

// missing is computed over one load's plans: the ids whose file on this machine a
// stat answered is not there. Only fs.ErrNotExist says so; any other error is
// not a missing file. It waits on the batch, so whatever else the load asks is
// added first.
func (b *fileBatch) missing(plans []domain.Plan) map[domain.PlanID]bool {
	files := make(map[domain.PlanID]fileAsk, len(plans))
	for _, p := range plans {
		if path, ok := planFile(p); ok {
			files[p.ID] = fileAsk{path: path}
			b.add(files[p.ID])
		}
	}
	answered := b.wait()
	missing := make(map[domain.PlanID]bool)
	for id, ask := range files {
		if call, ok := answered[ask]; ok && errors.Is(call.err, fs.ErrNotExist) {
			missing[id] = true
		}
	}
	return missing
}

// planFile is where p's file is on this machine, if it has one here: its
// SourceHint, when that hint is a filesystem path rather than a URL.
func planFile(p domain.Plan) (string, bool) {
	if p.SourceHint != "" && !client.URLSource(p.SourceHint) {
		return p.SourceHint, true
	}
	return "", false
}

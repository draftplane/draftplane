package app

import (
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// stateWatchOps are the events at state.json that signal. Rename is carried
// over unchanged from the original watch.
const stateWatchOps = fsnotify.Write | fsnotify.Create | fsnotify.Rename

// fileWatchOps are the events at a followed plan file that signal. Every way a
// save ends lands one of them on the file's name -- a write in place, a new
// file renamed over it, vim's rename-away-then-write -- while Rename and Remove
// there mean the file is leaving, and following it then would read a file that
// is not there (in vim's case, before the new one exists).
const fileWatchOps = fsnotify.Write | fsnotify.Create

// WatchState watches dir for changes to state.json and coalesces them into
// a 1-buffered channel: the TUI refreshes review facts live as other
// processes (an agent over MCP) write them. The returned stop function
// closes the watcher and the channel. Watch the DIRECTORY, not the file:
// localfs saves via atomic rename, which breaks inode-level watches.
func WatchState(dir string) (<-chan struct{}, func(), error) {
	return watchName(dir, "state.json", stateWatchOps)
}

// WatchFile watches the plan file at path the way WatchState watches
// state.json -- its directory, for the same reason: editors and agents
// commonly save by renaming a new file over the old one. A review follows the
// file through it (Root.syncFollow).
func WatchFile(path string) (<-chan struct{}, func(), error) {
	return watchName(filepath.Dir(path), filepath.Base(path), fileWatchOps)
}

// watchEventMatches reports whether ev is one of ops at the file called name.
func watchEventMatches(ev fsnotify.Event, name string, ops fsnotify.Op) bool {
	return filepath.Base(ev.Name) == name && ev.Op&ops != 0
}

// watchName is the loop behind WatchState and WatchFile: it watches dir and
// coalesces every event watchEventMatches accepts into a 1-buffered channel.
func watchName(dir, name string, ops fsnotify.Op) (<-chan struct{}, func(), error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, err
	}
	if err := w.Add(dir); err != nil {
		_ = w.Close()
		return nil, nil, err
	}
	ch := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(ch)
		for {
			select {
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if !watchEventMatches(ev, name, ops) {
					continue
				}
				select {
				case ch <- struct{}{}:
				default: // already pending; coalesce
				}
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
				// Non-fatal: drop watcher errors; live refresh is best-effort.
			case <-done:
				return
			}
		}
	}()
	stop := func() {
		close(done)
		_ = w.Close()
	}
	return ch, stop, nil
}

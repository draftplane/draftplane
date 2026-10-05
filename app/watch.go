package app

import (
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// WatchState watches dir for changes to state.json and coalesces them into
// a 1-buffered channel: the TUI refreshes review facts live as other
// processes (an agent over MCP) write them. The returned stop function
// closes the watcher and the channel. Watch the DIRECTORY, not the file:
// localfs saves via atomic rename, which breaks inode-level watches.
func WatchState(dir string) (<-chan struct{}, func(), error) {
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
				if filepath.Base(ev.Name) != "state.json" {
					continue
				}
				if !ev.Op.Has(fsnotify.Write) && !ev.Op.Has(fsnotify.Create) && !ev.Op.Has(fsnotify.Rename) {
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

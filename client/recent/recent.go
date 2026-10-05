// Package recent holds the terminal's own memory of what a human opened
// most recently -- for the list's "Recently opened" section, which answers
// "what was I just looking at" rather than duplicating the plan catalog.
//
// IT HOLDS NO CONTENT AND CREATES NO PLAN. An entry is nothing but an
// identifier and a timestamp; content enters Draftplane's store only when a
// review fact is created against it, and this package never creates one.
// That rule stays intact for every door this package is reachable from.
//
// ONLY THE TERMINAL'S DOORS WRITE IT -- draftplane review <path>,
// draftplane review <id>, and enter on a list row. session.Open itself stays
// write-free, and so does every MCP tool: an agent's own opens are scratch
// work, not a human's "what did I just look at", and recording them would
// also turn get_review's ReadOnlyHint into a lie.
package recent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/xdg"
)

// Entry records one thing opened: a plan by id, a file by path, or -- once a
// file has a plan -- both, so the two collapse into the one entry `names`
// describes.
type Entry struct {
	PlanID   domain.PlanID `json:"plan_id,omitempty"`
	Path     string        `json:"path,omitempty"`
	OpenedAt time.Time     `json:"opened_at"`
}

// Keep is how many entries the file holds, well past the five the list
// shows: when one of the five names a plan that has since gone, the
// next entry fills the slot instead of the section shrinking.
const Keep = 20

var errUndecodable = errors.New("not a valid recent file")

// names reports whether e and o record the same thing opened. Either key is
// enough: a file that became a plan is one entry, not two.
func (e Entry) names(o Entry) bool {
	return (e.PlanID != "" && e.PlanID == o.PlanID) || (e.Path != "" && e.Path == o.Path)
}

func (e Entry) empty() bool { return e.PlanID == "" && e.Path == "" }

// DefaultPath is $XDG_DATA_HOME/draftplane/recent.json, beside state.json --
// data, not config: it records what was opened, not how draftplane behaves.
func DefaultPath() (string, error) {
	dir, err := xdg.DataHome()
	if err != nil {
		return "", fmt.Errorf("recent: %w", err)
	}
	return filepath.Join(dir, "draftplane", "recent.json"), nil
}

// Load reads every entry, newest first. A missing file returns nil, nil: a
// machine that has never opened anything here is the normal starting state.
func Load(path string) ([]Entry, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("recent: reading %s: %w", path, err)
	}
	var entries []Entry
	if err := json.Unmarshal(b, &entries); err != nil {
		// The decoder's own error is not forwarded: a path out of this file
		// is not this process's to echo back.
		return nil, fmt.Errorf("recent: %s: %w", path, errUndecodable)
	}
	return entries, nil
}

// Touch records e as the newest thing opened, dropping any older entry that
// names the same plan or the same path and trimming to Keep. A refused open
// never reaches here, and an entry naming nothing is a no-op rather
// than a malformed record.
func Touch(path string, e Entry) error {
	if e.empty() {
		return nil
	}
	return rewrite(path, func(entries []Entry) ([]Entry, bool) {
		out := append(make([]Entry, 0, Keep), e)
		for _, old := range entries {
			if len(out) == Keep {
				break
			}
			if !e.names(old) {
				out = append(out, old)
			}
		}
		return out, true
	})
}

// Forget drops every entry naming e's plan or path, without creating the
// file if it does not already exist. It is how `d` on a Recently opened row
// keeps the list and this store in the same redraw.
func Forget(path string, e Entry) error {
	if e.empty() {
		return nil
	}
	return rewrite(path, func(entries []Entry) ([]Entry, bool) {
		out := make([]Entry, 0, len(entries))
		for _, old := range entries {
			if !e.names(old) {
				out = append(out, old)
			}
		}
		return out, len(out) != len(entries)
	})
}

// rewrite is every writer's load-change-save under the lock. An undecodable
// file reads as empty here and is overwritten: it is a convenience list,
// and refusing every later open over it would lose the section for good.
func rewrite(path string, fn func([]Entry) ([]Entry, bool)) error {
	return withLock(path, func() error {
		entries, err := Load(path)
		if errors.Is(err, errUndecodable) {
			entries, err = nil, nil
		}
		if err != nil {
			return err
		}
		out, changed := fn(entries)
		if !changed {
			return nil
		}
		return save(path, out)
	})
}

// save writes every entry atomically at mode 0600, matching
// client/config's Save: the temp file is created in the target directory,
// so the rename cannot cross a filesystem boundary, and never widens past
// 0600.
func save(path string, entries []Entry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("recent: creating directory: %w", err)
	}
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("recent: encoding: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".recent-*")
	if err != nil {
		return fmt.Errorf("recent: creating temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op once renamed

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("recent: setting permissions: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("recent: writing: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("recent: closing temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("recent: replacing %s: %w", path, err)
	}
	return nil
}

// withLock runs fn holding an exclusive lock on a sibling lockfile, so
// concurrent readers/writers -- across goroutines and processes -- serialize
// their Load-modify-Save cycles, matching client/config's WithLock. The
// lock is on a separate file because save replaces the data file by rename,
// which would drop a lock held on the old inode.
func withLock(path string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("recent: creating directory: %w", err)
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("recent: opening lock: %w", err)
	}
	defer func() { _ = f.Close() }()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("recent: locking: %w", err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	return fn()
}

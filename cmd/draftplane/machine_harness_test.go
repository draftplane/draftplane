package main

// This file is the machine a gated journey drives instead of the developer's
// own: a fresh pair of XDG roots under t.TempDir(), and a guard that fails the
// test if this machine's real draftplane directories moved while it ran.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// freshMachine is one independent draftplane state: its own
// XDG_CONFIG_HOME/XDG_DATA_HOME under t.TempDir(), created empty -- no
// config.json, no state.json -- which is exactly "a machine that has never
// done anything."
type freshMachine struct {
	configHome, dataHome string
}

func newFreshMachine(t *testing.T) freshMachine {
	t.Helper()
	root := t.TempDir()
	return freshMachine{
		configHome: filepath.Join(root, "config"),
		dataHome:   filepath.Join(root, "data"),
	}
}

// setMachineEnv points this process's XDG roots at m, for the in-process
// calls a journey makes directly (buildDeps) and for every subprocess it starts
// afterwards, which inherits them -- t.Setenv, not a raw os.Setenv, so each
// call is restored automatically and cannot leak into whichever test Go runs
// next in this process.
func setMachineEnv(t *testing.T, m freshMachine) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", m.configHome)
	t.Setenv("XDG_DATA_HOME", m.dataHome)
}

// realStateRoots names this machine's REAL draftplane directories, resolved
// the way the product itself resolves them (package xdg, which dataPath and
// config.DefaultPath both go through: XDG first, ~/.local/share and ~/.config
// as the fallback). Read at the very top of the test, BEFORE any t.Setenv has
// run, so the "real" roots are genuinely this developer's own even on a
// machine that sets XDG itself.
func realStateRoots(t *testing.T) []string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("locating this machine's home directory: %v", err)
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	return []string{filepath.Join(dataHome, "draftplane"), filepath.Join(configHome, "draftplane")}
}

// fingerprintTree is a whole directory tree's stat listing -- every entry's
// relative path, mode, size and modification time, sorted so the comparison
// does not depend on walk order. "ABSENT" for a root that does not exist,
// which is itself a fingerprint worth comparing: a harness that CREATED
// ~/.config/draftplane on a machine that never had one would be caught by the
// absent-to-present transition exactly as a harness that edited an existing
// one is caught by a changed listing.
//
// Mode/size/mtime together rather than any one of them: mtime alone misses a
// same-length rewrite, and size alone misses a rewrite of identical length
// with different bytes. Directory entries carry their own mtime, which moves
// the moment anything is created or removed inside them, so a new file in a
// subdirectory shows up twice over.
func fingerprintTree(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		lines = append(lines, fmt.Sprintf("%s\t%s\t%d\t%s", rel, info.Mode(), info.Size(), info.ModTime().UTC().Format(time.RFC3339Nano)))
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return "ABSENT"
	}
	if err != nil {
		t.Fatalf("fingerprinting %s: %v", root, err)
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

// guardRealState fingerprints both real XDG roots now and re-checks them in a
// cleanup, failing if either moved. Registered FIRST, from the parent test, so
// its cleanup runs LAST (t.Cleanup is LIFO) -- after every subtest's own
// cleanups, after every t.Setenv restore, and after every subprocess the test
// started is gone.
//
// t.Errorf, not t.Fatalf: a cleanup that stops at the first mismatched root
// would hide whether the other one moved too, and by cleanup time there is
// nothing left to abort.
func guardRealState(t *testing.T) {
	t.Helper()
	roots := realStateRoots(t)
	before := make([]string, len(roots))
	for i, root := range roots {
		before[i] = fingerprintTree(t, root)
		t.Logf("real draftplane state fingerprinted BEFORE this drive: %s (%d entries)", root, strings.Count(before[i], "\n")+1)
	}
	t.Cleanup(func() {
		for i, root := range roots {
			after := fingerprintTree(t, root)
			if after == before[i] {
				t.Logf("real draftplane state UNTOUCHED: %s is byte-identical to its pre-drive fingerprint", root)
				continue
			}
			t.Errorf("THIS HARNESS TOUCHED THIS MACHINE'S REAL DRAFTPLANE STATE at %s -- every machine it drives is supposed to live entirely under its own t.TempDir()\nbefore:\n%s\nafter:\n%s", root, before[i], after)
		}
	})
}

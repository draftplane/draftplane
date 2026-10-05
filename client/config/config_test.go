package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/draftplane/draftplane/client/config"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	want := config.Config{MachinePseudonym: "calm-mountain"}
	if err := config.Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Errorf("round trip: got %+v, want %+v", got, want)
	}
}

// A missing config file is the ordinary state of a machine that has never
// minted a pseudonym -- not an error condition every caller has to
// special-case.
func TestLoadMissingReturnsZeroConfigNotAnError(t *testing.T) {
	got, err := config.Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("Load: %v, want nil error for a missing file", err)
	}
	if got != (config.Config{}) {
		t.Errorf("Load() = %+v, want a zero Config", got)
	}
}

func TestSaveCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "config.json")
	if err := config.Save(path, config.Config{MachinePseudonym: "calm-mountain"}); err != nil {
		t.Fatalf("Save into a missing directory must succeed: %v", err)
	}
}

// A rename-based Save leaves the original inode untouched; an in-place write
// would mutate it out from under a concurrent reader, and this directory is
// read by more than one process, so that reader is a real one.
func TestSaveReplacesByRenameNotInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, config.Config{MachinePseudonym: "calm-mountain"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	witness := path + ".witness"
	if err := os.Link(path, witness); err != nil {
		t.Fatalf("Link: %v", err)
	}
	if err := config.Save(path, config.Config{MachinePseudonym: "hazy-scarf"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	b, err := os.ReadFile(witness)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(b), "calm-mountain") {
		t.Errorf("the original inode was mutated in place, so a concurrent reader can see a partial file: %s", b)
	}
}

// Config's only field is a plain string, so no field decodes through a custom
// UnmarshalJSON that could echo a raw value into the error.
//
// This test pins the actual defense instead: Load returns a flat, path-naming
// message rather than forwarding encoding/json's own error text, the thing
// that would have to change before any future value-echoing field could leak
// through it.
func TestLoadMalformedContentNeverWrapsTheRawError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"machine_pseudonym": 12345}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load: want an error for malformed content, got nil")
	}
	if !strings.Contains(err.Error(), "not a valid config file") {
		t.Errorf("Load error = %q, want the flat generic message naming the path", err.Error())
	}
	if strings.Contains(err.Error(), "12345") {
		t.Errorf("Load error %q echoes the malformed value", err.Error())
	}
}

// Config holds no secret today, but the mode is set explicitly rather than
// relying on the directory's 0700 and os.CreateTemp's default -- see Save's doc
// comment.
func TestSaveIsOwnerReadableOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, config.Config{MachinePseudonym: "calm-mountain"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

// Two writers at once is the ordinary case, not the exotic one: a TUI session
// and one or more `draftplane mcp` processes share this file.
func TestWithLockSerializesWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, config.Config{MachinePseudonym: "0"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	const writers = 20
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = config.WithLock(path, func() error {
				c, err := config.Load(path)
				if err != nil {
					return err
				}
				c.MachinePseudonym += "x"
				return config.Save(path, c)
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := 1 + writers; len(got.MachinePseudonym) != want {
		t.Errorf("MachinePseudonym length = %d, want %d — a writer's update was lost", len(got.MachinePseudonym), want)
	}
}

// A sequential base case for MachinePseudonym: minting happens once and the
// result survives to a later Load, not just to the caller that minted it.
func TestMachinePseudonymMintsOnceAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	first, err := config.MachinePseudonym(path)
	if err != nil {
		t.Fatalf("MachinePseudonym: %v", err)
	}
	if first == "" {
		t.Fatal("MachinePseudonym returned an empty value")
	}

	second, err := config.MachinePseudonym(path)
	if err != nil {
		t.Fatalf("MachinePseudonym: %v", err)
	}
	if second != first {
		t.Errorf("MachinePseudonym() = %q on the second call, want %q — a minted pseudonym must not change", second, first)
	}

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.MachinePseudonym != first {
		t.Errorf("Load().MachinePseudonym = %q, want %q — the mint must be saved to disk, not just returned", got.MachinePseudonym, first)
	}
}

// The mint-on-first-read helper is exactly where two processes race: a TUI
// session and a `draftplane mcp` process starting at the same moment can both
// find no pseudonym on disk. Every concurrent caller must come away with the
// same pseudonym -- one mints and saves it, and the other adopts that same
// value rather than minting and saving one of its own.
//
// DO NOT judge this test at -cpu 1: only real hardware parallelism lets two
// goroutines execute the gap between the read and the mint truly
// simultaneously, and at GOMAXPROCS=1 that gap is too short for the
// cooperative scheduler to usually interrupt. Run it with -race at -cpu 4 or
// higher.
func TestMachinePseudonymConcurrentCallersAgree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	const callers = 2
	var wg sync.WaitGroup
	got := make([]string, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], errs[i] = config.MachinePseudonym(path)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	if got[0] == "" {
		t.Fatal("MachinePseudonym returned an empty value")
	}
	for i, g := range got {
		if g != got[0] {
			t.Errorf("caller %d = %q, caller 0 = %q, want every concurrent caller to get the same minted pseudonym", i, g, got[0])
		}
	}
}

func TestLoadIgnoresAFieldItDoesNotKnow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"some_other_field":"x","machine_pseudonym":"hazy-scarf"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MachinePseudonym != "hazy-scarf" {
		t.Fatalf("MachinePseudonym = %q, want %q", cfg.MachinePseudonym, "hazy-scarf")
	}
}

func TestDefaultPath(t *testing.T) {
	tests := []struct {
		name      string
		xdgConfig string
	}{
		{name: "XDG_CONFIG_HOME set", xdgConfig: "/xdg/config"},
		{name: "XDG_CONFIG_HOME unset falls back to ~/.config", xdgConfig: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", tt.xdgConfig)
			got, err := config.DefaultPath()
			if err != nil {
				t.Fatalf("DefaultPath: %v", err)
			}
			base := tt.xdgConfig
			if base == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					t.Fatalf("UserHomeDir: %v", err)
				}
				base = filepath.Join(home, ".config")
			}
			want := filepath.Join(base, "draftplane", "config.json")
			if got != want {
				t.Errorf("DefaultPath() = %q, want %q", got, want)
			}
		})
	}
}

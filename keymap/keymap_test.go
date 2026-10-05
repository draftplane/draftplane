package keymap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultCoversEveryAction walks AllActions and Default() against each
// other in BOTH directions, because each direction is its own defect.
//
// AllActions -> Default: an action declared with no default binding is a
// gesture with no key.
//
// Default -> AllActions is the quiet one. Load rejects an override naming an
// action the slice does not hold, so a binding whose action is missing from
// AllActions keeps working on its default keystroke while being silently
// UNREBINDABLE, and it drops out of every AllActions loop besides.
func TestDefaultCoversEveryAction(t *testing.T) {
	m := Default()
	bound := map[Action]bool{}
	for _, a := range m {
		bound[a] = true
	}
	for _, a := range AllActions {
		if !bound[a] {
			t.Fatalf("action %s has no default binding", a)
		}
	}
	known := map[Action]bool{}
	for _, a := range AllActions {
		known[a] = true
	}
	for k, a := range m {
		if !known[a] {
			t.Fatalf("%q is bound to %s, which is not in AllActions -- Load cannot rebind it", k, a)
		}
	}
}

func TestDefaultAvoidsMultiplexerKeys(t *testing.T) {
	// Zellij's default layer swallows these before the TUI sees them.
	reserved := []string{"ctrl+g", "ctrl+p", "ctrl+t", "ctrl+n", "ctrl+h", "ctrl+s", "ctrl+o", "ctrl+q"}
	m := Default()
	for key := range m {
		for _, r := range reserved {
			if key == r {
				t.Fatalf("default binding %q collides with multiplexer reserved keys", key)
			}
		}
	}
	// pgup/pgdown are dedicated terminal keys, never intercepted by a
	// multiplexer's own prefix layer — safe defaults for paging.
	if m["pgdown"] != ActPageDown || m["pgup"] != ActPageUp {
		t.Fatalf("pgup/pgdown must be bound to paging by default: %v", m)
	}
}

// TestPrevThreadIsNAlone pins prev-thread's default key: N, beside rather than
// displacing m/ActRelocate. The last assertion guards the OTHER direction -- a
// stale second ActPrevThread binding is exactly what Load's own doc says a
// rebind removes, so the default table must not ship one on its own.
func TestPrevThreadIsNAlone(t *testing.T) {
	m := Default()
	for _, c := range []struct {
		key  string
		want Action
	}{
		{"N", ActPrevThread},
		{"m", ActRelocate},
	} {
		if got := m[c.key]; got != c.want {
			t.Fatalf("Default()[%q] = %q, want %q", c.key, got, c.want)
		}
	}
	prevThreadKeys := 0
	for k, a := range m {
		if a == ActPrevThread {
			prevThreadKeys++
			if k != "N" {
				t.Fatalf("ActPrevThread bound to unexpected key %q alongside N", k)
			}
		}
	}
	if prevThreadKeys != 1 {
		t.Fatalf("ActPrevThread bound to %d keys, want exactly 1 (N)", prevThreadKeys)
	}
}

// TestSearchDoesNotTakeTheFilterKey pins the search's two-door design, and both
// halves are assertions about the SAME map: "f" opens the review view's
// document search, and "/" is still the plan list's filter.
//
// A Map holds ONE action per keystroke for the whole program, so the search
// having f and the list keeping "/" is a single fact about a single map rather
// than two, and a test asserting only f would not say what this map is being
// held to.
func TestSearchDoesNotTakeTheFilterKey(t *testing.T) {
	m := Default()
	for _, c := range []struct {
		key  string
		want Action
	}{
		{"f", ActSearch},
		{"/", ActFilter},
	} {
		if got := m[c.key]; got != c.want {
			t.Fatalf("Default()[%q] = %q, want %q", c.key, got, c.want)
		}
	}
}

// TestLoadRebindsSearch is TestLoadOverrides' shape at this action: the new key
// drives the search and the default goes dead, since an override that only
// ADDED a key would leave two live bindings for one action. "/" is checked
// alongside because Load's removal loop walks every entry whose action matches,
// and ActFilter is the neighbour a wrong comparison would take with it.
func TestLoadRebindsSearch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keymap.json")
	if err := os.WriteFile(path, []byte(`{"search": "F"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatalf("Load rejected a search rebind: %v -- ActSearch is not in AllActions", err)
	}
	if m["F"] != ActSearch {
		t.Fatalf("m[\"F\"] = %q, want %q", m["F"], ActSearch)
	}
	if _, stillF := m["f"]; stillF {
		t.Fatal("rebinding search must remove its default keystroke f")
	}
	if m["/"] != ActFilter {
		t.Fatalf("m[\"/\"] = %q, want %q -- a search rebind must not disturb the filter", m["/"], ActFilter)
	}
}

func TestLoadOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keymap.json")
	if err := os.WriteFile(path, []byte(`{"quit": "Q", "approve": "ctrl+a"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m["Q"] != ActQuit || m["ctrl+a"] != ActApprove {
		t.Fatalf("overrides not applied: %v", m)
	}
	if m["j"] != ActMoveDown {
		t.Fatal("defaults must survive overlay")
	}
	if _, stillQ := m["q"]; stillQ {
		t.Fatal("rebinding an action must remove its default keystroke")
	}
}

func TestLoadMissingFileIsDefault(t *testing.T) {
	m, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m["j"] != ActMoveDown {
		t.Fatal("missing file must yield defaults")
	}
}

func TestLoadDuplicateTargetKeyErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keymap.json")
	if err := os.WriteFile(path, []byte(`{"quit": "zzqq", "approve": "zzqq"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "zzqq") {
		t.Fatalf("err = %v, want error naming the duplicate keystroke %q", err, "zzqq")
	}
}

func TestLoadUnknownActionErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keymap.json")
	if err := os.WriteFile(path, []byte(`{"warp_drive": "w"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "warp_drive") {
		t.Fatalf("err = %v, want unknown-action naming warp_drive", err)
	}
}

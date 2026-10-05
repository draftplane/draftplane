// Package keymap maps keystrokes to review actions. Defaults are vim-style
// and deliberately avoid the ctrl-keys common terminal multiplexers reserve
// (Zellij: ctrl+g/p/t/n/h/s/o/q; tmux: ctrl+b; screen: ctrl+a). Every
// read-mode action is rebindable via a JSON file of {"action": "keystroke"}.
package keymap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/draftplane/draftplane/xdg"
)

type Action string

const (
	ActMoveDown      Action = "move_down"
	ActMoveUp        Action = "move_up"
	ActNextThread    Action = "next_thread"
	ActPrevThread    Action = "prev_thread"
	ActTop           Action = "top"
	ActBottom        Action = "bottom"
	ActScrollDown    Action = "scroll_down"
	ActScrollUp      Action = "scroll_up"
	ActPageDown      Action = "page_down"
	ActPageUp        Action = "page_up"
	ActToggleExpand  Action = "toggle_expand"
	ActComment       Action = "comment"
	ActReply         Action = "reply"
	ActToggleResolve Action = "toggle_resolve"
	ActCycleThread   Action = "cycle_thread"
	ActApprove       Action = "approve"
	ActReload        Action = "reload"
	ActQuit          Action = "quit"
	ActRelocate      Action = "relocate"
	ActList          Action = "list"
	// ActRename is dispatched only by the plan list's Update: the review model
	// has no case for it, so it is inert there.
	ActRename Action = "rename"
	// ActDelete is dispatched by BOTH models at a DIFFERENT SUBJECT apiece: the
	// plan list deletes the plan under its cursor, the review model deletes the
	// THREAD under its own. Both open a confirm panel first; neither destroys
	// anything on the keystroke itself. ActFilter is the only other action the
	// two models mean different subjects by.
	ActDelete Action = "delete"
	// ActFilter: the plan list's "/" opens a FILTER, narrowing its plans to the
	// ones that match a query.
	//
	// Dispatched by BOTH models at a different subject: the list narrows its
	// plans, the review model queries the WHOLE open document through the same
	// handler ActSearch reaches. It carries that second meaning because Map is
	// map[string]Action, so "/" holds one action for the whole program, and "/"
	// is the key a reader expects for both gestures.
	//
	// Declaring it here does not make it live in either model; each has to
	// dispatch it.
	ActFilter Action = "filter"
	// ActSearch is the review view's document search: "f" finds the blocks of
	// the OPEN PLAN whose rendered text contains a query. Enter lands the cursor
	// on the first match at or after it, n and N walk the rest in document order
	// and wrap, and esc turns off an active search.
	//
	// TWO KEYS REACH THE SEARCH AND ONLY ONE OF THEM IS THIS ACTION: "f" is
	// bound below, while "/" stays ActFilter and picks up a second meaning.
	// updateRead answers this action and ActFilter from ONE case body, so the
	// two doors cannot come to mean different things.
	//
	// Inert in the plan list by construction: updateBrowse's action switch has
	// no default arm, so an action it holds no case for falls straight out.
	ActSearch Action = "search"
	// ActSort opens the plan list's centred sort modal over the section the
	// cursor is in. Dispatched only by the plan list's Update, inert in the
	// review model. TestDefaultCoversEveryAction guards the declaration and
	// cannot guard the dispatch; the list's own
	// TestTheSortKeyIsRebindableInBothDirections guards both.
	//
	// The sort it opens is NOT persisted: nothing on this path writes
	// client/config, a write whose failure would have no useful remedy here.
	ActSort Action = "sort"
	// ActKeys opens the "?" panel naming every key the current view answers --
	// one mode per model, each built from that model's own dispatch (see
	// app.reviewKeyGroups and app.listKeyGroups). Dispatched by
	// BOTH models, from their own resting mode (modeRead, listBrowse) alone: it
	// is the one action every view answers, which is the whole point of it.
	// Rebindable like everything else; "?" was free before this (grep the table
	// below).
	ActKeys Action = "keys"
	// ActInfo opens the plan list's `i` panel: what a plan IS --
	// its id, dates and file -- and the one line that opens it for review.
	// Dispatched by the LIST ALONE, from listBrowse, on a plan-as-object footing
	// with ActRename/ActDelete rather than with ActKeys: it is a fact panel about
	// the ROW under the cursor, not a reference panel about the view, so it takes
	// no case in the review model at all -- `i` answers what the list already
	// knows about a row, and the review model has no row.
	ActInfo Action = "info"
)

var AllActions = []Action{
	ActMoveDown, ActMoveUp, ActNextThread, ActPrevThread, ActTop, ActBottom,
	ActScrollDown, ActScrollUp, ActPageDown, ActPageUp, ActToggleExpand, ActComment, ActReply,
	ActToggleResolve, ActCycleThread, ActApprove, ActReload, ActQuit, ActRelocate, ActList,
	ActRename, ActDelete, ActFilter, ActSearch, ActSort,
	ActKeys, ActInfo,
}

type Map map[string]Action

func Default() Map {
	return Map{
		"j":    ActMoveDown,
		"down": ActMoveDown,
		"k":    ActMoveUp,
		"up":   ActMoveUp,
		"n":    ActNextThread,
		// N, not the more obvious m: m is ActRelocate's own mnemonic (move an
		// orphan). N is the reverse-of-next pairing every vi user already holds.
		"N":      ActPrevThread,
		"g":      ActTop,
		"G":      ActBottom,
		"J":      ActScrollDown,
		"K":      ActScrollUp,
		"pgdown": ActPageDown,
		"pgup":   ActPageUp,
		"enter":  ActToggleExpand,
		"tab":    ActCycleThread,
		"c":      ActComment,
		"r":      ActReply,
		"R":      ActToggleResolve,
		"a":      ActApprove,
		"ctrl+r": ActReload,
		"q":      ActQuit,
		"m":      ActRelocate,
		"l":      ActList,
		"e":      ActRename,
		"d":      ActDelete,
		// "/" is the key a reader expects to narrow a list.
		"/": ActFilter,
		// A binding is dead if a handler matches its keystroke literally ahead
		// of the keymap lookup. The literals either model checks that way are
		// ctrl+c, esc, enter, tab, y and n -- across the panel modes, not just
		// read mode -- so check any new binding against that set.
		//
		// "f" is the review view's own door to the search; "/" reaches the same
		// handler through ActFilter above, which is why the search has no second
		// entry in this map.
		"f": ActSearch,
		"s": ActSort,
		"?": ActKeys,
		"i": ActInfo,
	}
}

// Load overlays JSON overrides on the defaults. A rebound action's default
// keystrokes are removed so stale bindings don't linger.
func Load(path string) (Map, error) {
	m := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	var overrides map[string]string
	if err := json.Unmarshal(data, &overrides); err != nil {
		return nil, fmt.Errorf("keymap %s: %w", path, err)
	}
	seenKeys := map[string]bool{}
	for _, key := range overrides {
		if seenKeys[key] {
			return nil, fmt.Errorf("keymap %s: keystroke %q claimed by more than one action", path, key)
		}
		seenKeys[key] = true
	}
	known := map[Action]bool{}
	for _, a := range AllActions {
		known[a] = true
	}
	for name, key := range overrides {
		action := Action(name)
		if !known[action] {
			return nil, fmt.Errorf("keymap %s: unknown action %q", path, name)
		}
		for k, a := range m {
			if a == action {
				delete(m, k)
			}
		}
		m[key] = action
	}
	return m, nil
}

func DefaultPath() (string, error) {
	dir, err := xdg.ConfigHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "draftplane", "keymap.json"), nil
}

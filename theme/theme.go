// Package theme names the color zones the review TUI paints, and both reads
// and writes the user's chosen setting (Load, Save). Every PALETTE name
// resolves to a *Theme with every zone set, so a caller holding one never
// needs a partial-theme check -- but Setting and Load return a *Theme a caller
// must test, because of the one setting that resolves to no palette at all.
//
// There are two PALETTES, "dark" and "light", and Lookup answers only those.
// "system" belongs to a different vocabulary: it is a SETTING, a legal value of
// theme.json's "theme" key meaning "no preset chosen, match the terminal".
// Setting accepts it and answers a nil Theme; Lookup refuses it. See
// SystemName.
package theme

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/draftplane/draftplane/xdg"
)

// Theme assigns a hex color to each rendering zone. All fields are set by
// every built-in preset.
type Theme struct {
	Rail   string
	Chrome string
	Doc    string
	Text   string
	Dim    string
	// Brand is the product's own colour: the plan list's masthead and all
	// three cursor markers — the document's rail band and ┃ glyph plus the
	// plan list's own band.
	Brand string
	// Heading colours EVERY heading, sigil and words alike, at every level.
	//
	// The two presets assign the roles differently, deliberately: dark spends
	// rose on identity and position and bronze on state, light a cerulean on
	// identity and a rose on state. A role is a role in both; which hue fills
	// it is a question about the ground it sits on.
	//
	// In the dark preset it holds the SAME VALUE as Brand, and the two stay
	// separate tokens: collapsing them would silently move every heading the
	// next time the cursor's colour is retuned.
	Heading string
	// Warn is the colour of a centred panel's own HEAD ZONE (app.headThenBody's
	// inkHead, spent through ui.Styles.WarnHeader) -- the head line of every
	// centred panel, whether it asks for a decision, reports a fault, or merely
	// prompts -- plus the re-point pane's own refusal row (app.inkWarn), which
	// sits under the input rather than at the top of the body.
	//
	// It is a role of its own because no other token is rose in BOTH presets:
	// it is Heading/Brand in dark and Accent in light. Dark's Heading, Brand
	// and Warn happen to hold the identical value today; that is a fact about
	// today's values, not a promise, and sharing a token would drag this
	// headline's colour along the next time one of them is retuned.
	Warn string
	// PanelHead is the ink of A PANEL'S LEADING LINE -- the line naming what the
	// panel is about to do. The compose panel's "comment on:", every confirm
	// strip that is not centred, the relocate header, and the plan list's sort
	// modal TITLE and rename prompt all wear it, through the one
	// ui.Styles.FocusHeader. The sort modal's BORDER is Chrome's, like every
	// other box's in the TUI.
	//
	// IT IS Warn's OPPOSITE NUMBER, BUT NOT BY SHAPE -- that is the reading to
	// resist. Four of the five are the full-width strip along the bottom of the
	// frame and THE SORT MODAL IS NOT ONE: it is a bordered box spliced into the
	// middle (ListModel.composeSortModal), the very geometry Warn's own panels
	// use, so a rule drawn round shape would hand it to the wrong role. What holds
	// the two apart is the MODE GATE: a mode puts a Warn-headed box on screen or
	// it puts one of these there, never both, so the two are never on screen at
	// once and nothing has to tell them apart by looking.
	//
	// IT IS A ROLE OF ITS OWN FOR Warn's OWN REASON. The product's own site
	// inks this line in Accent (its own amber) and the TUI inked it in Focus;
	// matching the site IN DARK WITHOUT MOVING LIGHT is a pair no standing role
	// holds. Accent is that gold in dark and ROSE in light (#a8386c, Warn's own
	// value); Focus is the cerulean light already drew and the blue in dark the
	// match removes. So dark carries Accent's hex and light carries Focus's, and
	// NEITHER TOKEN IS READ TO GET THEM: retuning the thread card's author line
	// must not drag a panel's headline along behind it.
	PanelHead string
	// Field is the ground a TEXT INPUT sits on inside a panel (app.centredBox's
	// inkField) -- the re-point panes on both doors today.
	//
	// It holds StatusBar's value in both presets but stays a separate token:
	// retuning the status bar should not move a panel's input field. Strip is
	// not reused for it because Strip sits only three units off Chrome in
	// light, which is invisible on a real terminal; a role has to answer in
	// both presets.
	Field string
	// Control is the ground a PANEL CONTROL AT REST sits on -- a button nobody
	// has armed. Chip is the same control once the ring lands on it, and the
	// pair is deliberately two tokens: they are two states of one thing and
	// nothing about the armed colour predicts the resting one.
	//
	// THE TWO PRESETS STEP OFF THEIR ROW IN OPPOSITE DIRECTIONS, and that is
	// the whole reason this token exists rather than the compose panel reusing
	// Field. An idle button sits on the button row, which is Strip. Light's
	// Control is DARKER than Strip and reads as a raised band; dark's is
	// LIGHTER than Strip, because dark's row is already close to the palette's
	// floor and one more step down reads as a hole punched through the row
	// rather than as a button. Field is a step down in both presets, which is
	// right for a text input -- an inset a reader types into -- and wrong here
	// in exactly one of the two. No single role can face both ways, so the
	// direction is what forced a second token, not the size of the step: the
	// rejected dark ground and the one chosen in its place stand 1.45:1 and
	// 1.44:1 off the same row.
	//
	// IT CARRIES NO INK TOKEN OF ITS OWN, unlike Chip/ChipText: Text clears
	// WCAG AA on both grounds (6.0:1 dark, 9.6:1 light), so the button's label
	// is the same ink as the panel around it. What fails if a retune takes
	// either fact away is TestControlReadsAsAControlInBothPresets.
	Control string
	Card    string
	Accent  string
	Strip   string
	// Focus is the ARMED hue -- what the ring lands on. It reaches the screen
	// only as Chip's ground (ui.Styles.Chip, under ChipText), which holds the
	// identical hex in both presets.
	//
	// IT HAD A SECOND JOB UNTIL PanelHead TOOK IT, and that is why ui.Styles
	// still calls the panel headline's style FocusHeader: every panel's leading
	// line was inked in Focus, so in dark the headline and the
	// armed button were one blue, a foreground and a ground of the same value.
	// Matching the product's site broke that pairing in dark; in light the two hexes
	// coincide anyway and nothing moved. NO STYLE READS THIS TOKEN DIRECTLY
	// TODAY -- Chip's own value is what paints.
	Focus      string
	StatusBar  string
	StatusText string
	Chip       string
	ChipText   string
	OK         string
	Badge      string
	Code       string
	CodeBg     string
}

var presets = map[string]*Theme{
	"dark": {
		Rail:       "#14171c",
		Chrome:     "#1a1e25",
		Doc:        "#232831",
		Text:       "#d6d3cc",
		Dim:        "#8b8a85",
		Brand:      "#ef86b2",
		Heading:    "#ef86b2",
		Warn:       "#ef86b2",
		PanelHead:  "#e2a356",
		Field:      "#0f1217",
		Control:    "#404a59",
		Card:       "#303949",
		Accent:     "#e2a356",
		Strip:      "#2b323c",
		Focus:      "#6189e8",
		StatusBar:  "#0f1217",
		StatusText: "#b8b6b0",
		Chip:       "#6189e8",
		ChipText:   "#0e1116",
		OK:         "#84c084",
		Badge:      "#d9b34a",
		Code:       "#d4b87e",
		CodeBg:     "#1b2129",
	},
	"light": {
		Rail:       "#e0d3b4",
		Chrome:     "#f0e7d2",
		Doc:        "#f9f2e0",
		Text:       "#2e2a21",
		Dim:        "#6f6a5c",
		Brand:      "#00699f",
		Heading:    "#00699f",
		Warn:       "#a8386c",
		PanelHead:  "#00699f",
		Field:      "#e0d3b4",
		Control:    "#e0d3b4",
		Card:       "#f1e9d5",
		Accent:     "#a8386c",
		Strip:      "#ede4d0",
		Focus:      "#00699f",
		StatusBar:  "#e0d3b4",
		StatusText: "#4a443a",
		Chip:       "#00699f",
		ChipText:   "#f9f2e0",
		OK:         "#00699f",
		Badge:      "#6f6a5c",
		Code:       "#5f4a12",
		CodeBg:     "#f9f2e0",
	},
}

// DefaultName is the preset a caller gets when it names none, and the one
// detection falls back to when the terminal will not answer.
const DefaultName = "dark"

// Default is DefaultName's Theme. It never errors: the name is a built-in.
func Default() *Theme { return presets[DefaultName] }

// Names lists every PALETTE name, sorted. It is not the list of values
// theme.json may hold -- that is SettingNames, which is this plus SystemName.
func Names() []string {
	names := make([]string, 0, len(presets))
	for name := range presets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Lookup resolves a PRESET name to its Theme. An unknown name is an error
// naming it -- "system" included, because it names no palette. It is a legal
// setting all the same, and Setting is where it resolves; the two are different
// questions on purpose and must not be made to agree.
func Lookup(name string) (*Theme, error) {
	t, ok := presets[name]
	if !ok {
		return nil, fmt.Errorf("unknown theme %q", name)
	}
	return t, nil
}

type config struct {
	Theme string `json:"theme"`
}

// Load reads the theme config at path, {"theme": "<setting>"}, where a setting
// is any member of SettingNames().
//
// There are two ways to be told "detect" and only one has an empty name: a
// missing file returns ("", nil, nil), a file naming SystemName returns
// ("system", nil, nil). A caller deciding whether to detect must therefore test
// the THEME and never the name -- `name != ""` silently hands a nil *Theme to a
// renderer. The name is returned so a caller reporting the configuration to a
// human can still tell the two states apart.
//
// An unknown setting is an error naming it and the legal set.
func Load(path string) (string, *Theme, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", nil, fmt.Errorf("theme %s: %w", path, err)
	}
	if cfg.Theme == "" {
		// A file that exists but names no theme is misconfiguration, not
		// the missing-file detect signal. Falling through to Setting would
		// name the empty string back at a human as though they had typed it.
		return "", nil, fmt.Errorf("theme %s: missing \"theme\" key", path)
	}
	t, err := Setting(cfg.Theme)
	if err != nil {
		return "", nil, fmt.Errorf("theme %s: %w", path, err)
	}
	return cfg.Theme, t, nil
}

// SystemName is the setting that means "no preset chosen -- match the
// terminal": the ABSENCE of a choice, resolved by asking the terminal for its
// background and picking one of the two real presets. It is a legal value of
// theme.json's "theme" key and it is NOT a palette, so Setting accepts it and
// answers a nil Theme while Lookup goes on refusing it.
const SystemName = "system"

// SettingNames lists every value theme.json's "theme" key may take: the preset
// names plus SystemName, sorted.
//
// It is deliberately a second list beside Names() rather than a widening of
// it: Names() answers "which palettes exist", this answers "what may a human
// write here". Folding system into Names() would hand anything walking palettes
// a name with no colours behind it.
func SettingNames() []string {
	names := append(Names(), SystemName)
	sort.Strings(names)
	return names
}

// Setting resolves a config VALUE to its Theme. A preset name answers its
// Theme; SystemName answers (nil, nil), which every caller reads as "detect".
// An unknown name -- the empty string included -- is an error naming both the
// value and the legal set.
//
// A nil Theme with a nil error is a LEGAL answer and must not be treated as
// failure: anything discriminating on "did we get a name" rather than "did we
// get a theme" silently renders nothing.
func Setting(name string) (*Theme, error) {
	if name == SystemName {
		return nil, nil
	}
	t, ok := presets[name]
	if !ok {
		return nil, fmt.Errorf("unknown theme %q (want one of: %s)", name, strings.Join(SettingNames(), ", "))
	}
	return t, nil
}

// Save writes {"theme": name} at path, atomically, creating the parent
// directory if it is missing. name is validated FIRST, so a refused write
// touches no file at all -- validating after the open would truncate a working
// config to write a value that was never legal.
//
// No lock, and none is needed: the file holds one scalar written whole from an
// argument, so there is no read-modify step to lose and last-writer-wins is
// correct. What a concurrent reader needs is never to see a half-written file,
// and that is the atomic rename.
//
// 0600 and 0700 match client/config and client/recent.
func Save(path, name string) error {
	if _, err := Setting(name); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("theme: creating directory: %w", err)
	}
	b, err := json.MarshalIndent(config{Theme: name}, "", "  ")
	if err != nil {
		return fmt.Errorf("theme: encoding: %w", err)
	}
	// A trailing newline, which the two siblings above do not write: this is
	// the one of the three a human is expected to open in an editor.
	b = append(b, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".theme-*")
	if err != nil {
		return fmt.Errorf("theme: creating temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op once renamed

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("theme: setting permissions: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("theme: writing: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("theme: closing temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("theme: replacing %s: %w", path, err)
	}
	return nil
}

// DefaultPath is ~/.config/draftplane/theme.json, honoring XDG_CONFIG_HOME — the
// same pattern as keymap.DefaultPath.
func DefaultPath() (string, error) {
	dir, err := xdg.ConfigHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "draftplane", "theme.json"), nil
}

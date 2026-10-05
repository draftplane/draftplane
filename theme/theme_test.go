package theme

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestLookup(t *testing.T) {
	cases := []struct {
		name    string
		wantNil bool
		wantErr bool
	}{
		{name: "dark"},
		{name: "light"},
		// All three were theme names once and none is a palette now. "system"
		// is still a legal SETTING; see
		// TestSettingIsTheCONFIGVocabularyAndLookupIsThePALETTEOne.
		{name: "harbor-dark", wantNil: true, wantErr: true},
		{name: "ember-dark", wantNil: true, wantErr: true},
		{name: "system", wantNil: true, wantErr: true},
		{name: "no-such-theme", wantNil: true, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			th, err := Lookup(tc.name)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), tc.name) {
					t.Fatalf("err = %v, want error naming %q", err, tc.name)
				}
				return
			}
			if err != nil {
				t.Fatalf("Lookup(%q) error: %v", tc.name, err)
			}
			if tc.wantNil && th != nil {
				t.Fatalf("Lookup(%q) = %+v, want nil", tc.name, th)
			}
			if !tc.wantNil && th == nil {
				t.Fatalf("Lookup(%q) = nil, want a theme", tc.name)
			}
		})
	}
}

func TestPresetsHaveEveryZoneSet(t *testing.T) {
	for name, th := range presets {
		fields := map[string]string{
			"Rail": th.Rail, "Chrome": th.Chrome, "Doc": th.Doc, "Text": th.Text,
			"Dim": th.Dim, "Brand": th.Brand, "Heading": th.Heading, "Warn": th.Warn,
			"PanelHead": th.PanelHead,
			"Field":     th.Field, "Control": th.Control,
			"Card": th.Card, "Accent": th.Accent, "Strip": th.Strip, "Focus": th.Focus,
			"StatusBar": th.StatusBar, "StatusText": th.StatusText, "Chip": th.Chip,
			"ChipText": th.ChipText, "OK": th.OK, "Badge": th.Badge,
			"Code": th.Code, "CodeBg": th.CodeBg,
		}
		for field, val := range fields {
			if !strings.HasPrefix(val, "#") || len(val) != 7 {
				t.Fatalf("preset %s field %s = %q, want a 6-digit hex color", name, field, val)
			}
		}
	}
}

// TestLightIsWarmParchmentNotBlueWhite pins the light palette's character: a
// decisively warm, parchment/paper canvas rather than a blue-white one.
// TestSurfacesAreActuallyDistinct pins that Card differs from Doc; this pins
// that it is still parchment while doing so.
func TestLightIsWarmParchmentNotBlueWhite(t *testing.T) {
	th, err := Lookup("light")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Doc": "#f9f2e0", "Chrome": "#f0e7d2", "Rail": "#e0d3b4", "Card": "#f1e9d5",
		"Strip": "#ede4d0", "StatusBar": "#e0d3b4", "StatusText": "#4a443a",
	}
	got := map[string]string{
		"Doc": th.Doc, "Chrome": th.Chrome, "Rail": th.Rail, "Card": th.Card,
		"Strip": th.Strip, "StatusBar": th.StatusBar, "StatusText": th.StatusText,
	}
	for field, w := range want {
		if got[field] != w {
			t.Fatalf("light %s = %q, want %q", field, got[field], w)
		}
	}
}

// TestCodeZonesSetForEveryPreset asserts the Code/CodeBg tokens exist and are
// well-formed in every preset. Exact palette values are deliberately NOT
// pinned: the palette is the repo owner's iteration surface, and tests assert
// plumbing, not taste.
func TestCodeZonesSetForEveryPreset(t *testing.T) {
	hex := regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	for _, name := range Names() {
		th, err := Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		if !hex.MatchString(th.Code) || !hex.MatchString(th.CodeBg) {
			t.Fatalf("%s Code/CodeBg = %q/%q, want well-formed hex colors", name, th.Code, th.CodeBg)
		}
	}
}

func TestLoadMissingFileSignalsDetect(t *testing.T) {
	name, th, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if name != "" || th != nil {
		t.Fatalf("Load(missing) = (%q, %+v), want (\"\", nil) to signal detection", name, th)
	}
}

func TestLoadValidPreset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "theme.json")
	if err := os.WriteFile(path, []byte(`{"theme": "light"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	name, th, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if name != "light" || th == nil || th.Doc != "#f9f2e0" {
		t.Fatalf("Load = (%q, %+v), want light", name, th)
	}
}

func TestLoadUnknownPresetErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "theme.json")
	if err := os.WriteFile(path, []byte(`{"theme": "neon-nowhere"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path); err == nil || !strings.Contains(err.Error(), "neon-nowhere") {
		t.Fatalf("err = %v, want error naming neon-nowhere", err)
	}
}

func TestLoadMalformedJSONErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "theme.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path); err == nil {
		t.Fatal("want error for malformed JSON")
	}
}

// TestLoadEmptyThemeKeyErrors pins that a config file which exists but names
// no theme is misconfiguration, not the missing-file detect signal.
func TestLoadEmptyThemeKeyErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "theme.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v, want a missing-key error, not the detect signal", err)
	}
}

func TestDefaultPathHonorsXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg-home")
	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join("/xdg-home", "draftplane", "theme.json") {
		t.Fatalf("path = %q, want XDG_CONFIG_HOME honored", path)
	}
}

func TestDefaultPathFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir available")
	}
	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(home, ".config", "draftplane", "theme.json") {
		t.Fatalf("path = %q, want under home", path)
	}
}

// relLuminance and contrastRatio are WCAG's, used here only to ask whether
// two surfaces differ enough to be seen as two surfaces.
func relLuminance(t *testing.T, hex string) float64 {
	t.Helper()
	var c [3]float64
	for i := 0; i < 3; i++ {
		var v int
		if _, err := fmt.Sscanf(hex[1+2*i:3+2*i], "%02x", &v); err != nil {
			t.Fatalf("bad hex %q: %v", hex, err)
		}
		f := float64(v) / 255
		if f <= 0.04045 {
			c[i] = f / 12.92
		} else {
			c[i] = math.Pow((f+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2]
}

func contrastRatio(t *testing.T, a, b string) float64 {
	t.Helper()
	la, lb := relLuminance(t, a), relLuminance(t, b)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

// TestSurfacesAreActuallyDistinct pins that a preset's inset surfaces differ
// from the surface they sit on.
//
// The threshold is deliberately low and the exact values are deliberately NOT
// pinned: the palette is the repo owner's iteration surface, and what belongs
// in a test is that these surfaces are two surfaces, not which two.
func TestSurfacesAreActuallyDistinct(t *testing.T) {
	const minStep = 1.05
	for _, tc := range []struct {
		name string
		// codeSurface is whether the preset gives inline code a background
		// of its OWN. Dark does; light deliberately does not, and the
		// difference is pinned rather than tolerated because "CodeBg equals
		// Doc" is also what the bug it guards looked like.
		codeSurface bool
	}{
		{"dark", true},
		{"light", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			th, err := Lookup(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if th.Card == th.Doc {
				t.Fatalf("Card is the same hex as Doc (%s); a comment card renders no background at all", th.Doc)
			}
			if got := contrastRatio(t, th.Card, th.Doc); got < minStep {
				t.Fatalf("Card (%s) vs Doc (%s) contrast = %.2f, want >= %.2f", th.Card, th.Doc, got, minStep)
			}
			if !tc.codeSurface {
				// Light has none: Code sits at 7.6 against parchment
				// unaided, so a box adds nothing.
				if th.CodeBg != th.Doc {
					t.Fatalf("CodeBg = %s, want Doc (%s): this preset marks code by ink alone", th.CodeBg, th.Doc)
				}
				return
			}
			if th.CodeBg == th.Doc {
				t.Fatalf("CodeBg is the same hex as Doc (%s); the zone renders no background at all", th.Doc)
			}
			if got := contrastRatio(t, th.CodeBg, th.Doc); got < minStep {
				t.Fatalf("CodeBg (%s) vs Doc (%s) contrast = %.2f, want >= %.2f", th.CodeBg, th.Doc, got, minStep)
			}
			if got := contrastRatio(t, th.Card, th.CodeBg); got < minStep {
				t.Fatalf("Card (%s) vs CodeBg (%s) contrast = %.2f, want >= %.2f", th.Card, th.CodeBg, got, minStep)
			}
		})
	}
}

// TestPanelHeadIsDarksGoldAndLeavesLightWhereItWas pins the role the PANEL
// HEADLINE reads -- ui.Styles.FocusHeader, worn by the compose panel's "comment
// on:", the confirm strips that are not centred, the relocate header, and the
// plan list's sort modal and rename prompt. Those are one style, so this is one
// claim about five rows.
//
// IT IS A ROLE OF ITS OWN FOR theme.Warn's REASON, and the shape of the
// argument is identical: no standing role answers gold in dark AND the cerulean
// light already drew. Accent is that gold in dark and ROSE in light (#a8386c,
// Warn's own value); Focus is that cerulean in light and the BLUE in dark this
// change was made to remove. Reusing either would have moved light, and light
// was to stay exactly where it was.
//
// THE HEXES ARE PINNED RATHER THAN COMPARED TO THOSE TWO TOKENS. Asserting
// PanelHead == Accent would make a retune of the thread card's author line
// silently move the compose panel's headline, which is the drag this token
// exists to stop.
//
// DARK'S IS PINNED AGAINST Chip ON TOP OF ITS OWN VALUE, because the blue did
// not leave the palette: Chip is where it goes on living, as the armed compose
// button's GROUND (TestComposeButtonRowMarksTheFocusedButton pins that escape).
// A retune walking the headline back onto that hex would put a panel's ink and
// its armed button's ground on one colour again, which is the reading
// the product's site was taken as the reference against. LIGHT CANNOT MAKE THAT
// CLAIM AND DOES NOT TRY: its headline and its chip ground have both been
// #00699f since before this role existed, and leaving light alone means leaving
// that alone too.
func TestPanelHeadIsDarksGoldAndLeavesLightWhereItWas(t *testing.T) {
	// WCAG AA for body text. The headline is bold, but it is a sentence read
	// rather than a mark spotted, so it answers to the body floor.
	const minInk = 4.5
	for _, tc := range []struct {
		name string
		want string
		// offChip is whether this preset's headline is a different hex from
		// its own armed-button ground. Dark's is, and that IS this change;
		// light's never was.
		offChip bool
	}{
		{"dark", "#e2a356", true},
		{"light", "#00699f", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			th, err := Lookup(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if th.PanelHead != tc.want {
				t.Fatalf("PanelHead = %s, want %s -- the ink of a panel's leading line", th.PanelHead, tc.want)
			}
			if got := th.PanelHead != th.Chip; got != tc.offChip {
				t.Fatalf("PanelHead (%s) differs from Chip (%s) = %v, want %v -- dark's headline must not be its armed button's own ground",
					th.PanelHead, th.Chip, got, tc.offChip)
			}
			if got := contrastRatio(t, th.PanelHead, th.Chrome); got < minInk {
				t.Fatalf("PanelHead (%s) on Chrome (%s) contrast = %.2f, want >= %.2f: that line names what the panel is about to do",
					th.PanelHead, th.Chrome, got, minInk)
			}
		})
	}
}

// TestControlReadsAsAControlInBothPresets pins the one property the Control
// token exists to hold, and it pins the DIRECTION of the step rather than only
// its size, because size alone cannot tell the rejected design from the chosen
// one. The dark idle button tried on a real terminal and turned down (it wore
// Field, #0f1217) stood 1.45:1 off the row it sits on; the ground chosen in
// its place stands 1.44:1 off that same row. Amount was never the fault. The
// rejected ground stepped DOWN from a row already close to the
// palette's floor and read as a hole punched through the button row; light
// steps down from its own row too and reads as a raised band, because light's
// row is nowhere near its ceiling.
//
// So a control at rest is LIGHTER than its row in dark and DARKER than it in
// light -- opposite directions, which is precisely what no single shared role
// could express and the whole reason this token is not Field. Retuning the
// palette is the repo owner's business and no hex is pinned here; reversing
// one of these directions is not a retune, it would reinstate the rejected
// ground this test's first paragraph measures.
//
// THE INK IS CHECKED IN THE SAME BREATH, because it is why ONE token was
// enough: Text clears WCAG AA on both grounds (6.0:1 dark, 9.6:1 light), so the
// idle button needed no paired ink token the way Chip needed ChipText. A retune
// that walks Control toward Text takes that away silently -- the label goes on
// being drawn in Text either way.
//
// AND THE ARMED HALF'S INK IS CHECKED BESIDE IT, in the test named for the
// resting half, because the two are ONE CONTROL IN TWO STATES: a reader asking
// what a compose button owes their eyes should not have to know which of the
// pair they are looking at to find the answer, and a floor that lives beside
// its opposite number is the one a retune trips over. The ARMED state is the
// HIGHER-STAKES of the two -- it is the button a keystroke acts on, so its
// label is the last thing read before a comment is posted or a draft thrown
// away -- and it is also the one nothing else guards: Control's ink IS Text, so
// the palette's own body-text discipline drags it along, while ChipText exists
// for exactly one ground and, before this line, the only thing any test in this
// package asked of it was that it be a well-formed hex.
//
// IT CLEARS AA WITH ROOM TODAY (5.6:1 dark, 5.3:1 light) AND THAT IS THE POINT.
// theme.Chip holds theme.Focus's own hex in both presets, so retuning the FOCUS
// colour -- a change nobody would file under "buttons" -- moves this ground out
// from under an ink that goes on being painted on it regardless. Room is what a
// silent slide is made of, not a reason to leave the floor unstated.
func TestControlReadsAsAControlInBothPresets(t *testing.T) {
	const (
		minStep = 1.05
		minInk  = 4.5 // WCAG AA for body text
	)
	for _, tc := range []struct {
		name string
		// lighterThanRow is which way this preset's idle button steps off its
		// row: up, away from the palette floor, in dark; down, away from the
		// parchment ceiling, in light.
		lighterThanRow bool
	}{
		{"dark", true},
		{"light", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			th, err := Lookup(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if got := contrastRatio(t, th.Control, th.Strip); got < minStep {
				t.Fatalf("Control (%s) vs Strip (%s) contrast = %.2f, want >= %.2f: an idle button that close to its row is not a button", th.Control, th.Strip, got, minStep)
			}
			if got := relLuminance(t, th.Control) > relLuminance(t, th.Strip); got != tc.lighterThanRow {
				t.Fatalf("Control (%s) lighter than Strip (%s) = %v, want %v", th.Control, th.Strip, got, tc.lighterThanRow)
			}
			if got := contrastRatio(t, th.Text, th.Control); got < minInk {
				t.Fatalf("Text (%s) on Control (%s) contrast = %.2f, want >= %.2f: that is the button's own label", th.Text, th.Control, got, minInk)
			}
			if got := contrastRatio(t, th.ChipText, th.Chip); got < minInk {
				t.Fatalf("ChipText (%s) on Chip (%s) contrast = %.2f, want >= %.2f: that is the ARMED button's label, the one a keystroke acts on", th.ChipText, th.Chip, got, minInk)
			}
		})
	}
}

// TestSettingIsTheCONFIGVocabularyAndLookupIsThePALETTEOne pins that "system"
// is a legal SETTING and not a PALETTE: Setting accepts it and answers a nil
// Theme, Lookup refuses it.
//
// The two are asserted together because the danger is not either answer on its
// own: it is somebody later "fixing" the inconsistency by making them agree, in
// whichever direction.
func TestSettingIsTheCONFIGVocabularyAndLookupIsThePALETTEOne(t *testing.T) {
	th, err := Setting(SystemName)
	if err != nil {
		t.Fatalf("Setting(%q): %v, want it accepted as a setting", SystemName, err)
	}
	if th != nil {
		t.Fatalf("Setting(%q) = %+v, want nil -- system names no palette", SystemName, th)
	}
	if _, err := Lookup(SystemName); err == nil {
		t.Fatalf("Lookup(%q) = nil error, want it still refused -- there is no system palette", SystemName)
	}
}

func TestSettingResolvesPresetsAndRefusesTheRest(t *testing.T) {
	cases := []struct {
		name    string
		wantNil bool
		wantErr bool
	}{
		{name: "dark"},
		{name: "light"},
		{name: SystemName, wantNil: true},
		{name: "harbor-dark", wantNil: true, wantErr: true},
		{name: "", wantNil: true, wantErr: true},
		{name: "no-such-theme", wantNil: true, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			th, err := Setting(tc.name)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Setting(%q) = %+v, want an error", tc.name, th)
				}
				return
			}
			if err != nil {
				t.Fatalf("Setting(%q): %v", tc.name, err)
			}
			if tc.wantNil != (th == nil) {
				t.Fatalf("Setting(%q) nil-ness = %v, want %v", tc.name, th == nil, tc.wantNil)
			}
		})
	}
}

// TestSaveRoundTripsThroughLoad drives every settable value back through the
// reader that startup uses rather than asserting on the bytes: a test that read
// the file itself would pass just as happily against two halves that disagree.
func TestSaveRoundTripsThroughLoad(t *testing.T) {
	for _, name := range SettingNames() {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "theme.json")
			if err := Save(path, name); err != nil {
				t.Fatalf("Save(%q): %v", name, err)
			}
			got, th, err := Load(path)
			if err != nil {
				t.Fatalf("Load after Save(%q): %v", name, err)
			}
			if got != name {
				t.Errorf("Load = %q, want %q", got, name)
			}
			if (th == nil) != (name == SystemName) {
				t.Errorf("Load theme nil-ness = %v for %q", th == nil, name)
			}
		})
	}
}

// TestSaveRefusesAnUnknownNameWithoutCreatingTheFile is the load-bearing half.
// Validating AFTER opening the file would truncate a good config to write a
// value that was never legal, so the assertion is the file's ABSENCE, not the
// error.
func TestSaveRefusesAnUnknownNameWithoutCreatingTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "theme.json")
	err := Save(path, "neon-nowhere")
	if err == nil || !strings.Contains(err.Error(), "neon-nowhere") {
		t.Fatalf("err = %v, want an error naming neon-nowhere", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("Save created %s on a refused name; it must touch no file", path)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("directory holds %d entries after a refused Save, want none (a stray temp file counts)", len(entries))
	}
}

// TestSaveDoesNotClobberAGoodFileOnARefusal is the same rule from the side that
// costs a user something: a working theme already on disk and a typo'd next one.
func TestSaveDoesNotClobberAGoodFileOnARefusal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "theme.json")
	if err := Save(path, "light"); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, "lgiht"); err == nil {
		t.Fatal("want an error for a typo'd name")
	}
	name, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load after a refused Save: %v", err)
	}
	if name != "light" {
		t.Errorf("theme = %q after a refused Save, want the original light", name)
	}
}

// TestSaveReplacesByRenameNotInPlace: a rename-based Save leaves the original
// inode untouched, while an in-place write would mutate it under a reader --
// and this file IS read by another process, every draftplane startup.
func TestSaveReplacesByRenameNotInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "theme.json")
	if err := Save(path, "dark"); err != nil {
		t.Fatal(err)
	}
	witness := path + ".witness"
	if err := os.Link(path, witness); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if err := Save(path, "light"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(witness)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "dark") {
		t.Fatalf("witness = %q, want the first Save's bytes -- the second wrote in place", b)
	}
}

// TestSaveIsOwnerReadableOnly pins the mode Save LEAVES, not the Chmod call
// that sets it: os.CreateTemp already creates at 0600, so deleting that line
// leaves this green under any ordinary umask. What it catches is a switch to
// os.WriteFile at 0644.
func TestSaveIsOwnerReadableOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "theme.json")
	if err := Save(path, "dark"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %04o, want 0600 to match config.json and its siblings", got)
	}
}

func TestSettingNamesIsThePresetsPlusSystem(t *testing.T) {
	got := SettingNames()
	if len(got) != len(Names())+1 {
		t.Fatalf("SettingNames() = %v, want every preset plus %q", got, SystemName)
	}
	var sawSystem bool
	for _, n := range got {
		if n == SystemName {
			sawSystem = true
		}
	}
	if !sawSystem {
		t.Errorf("SettingNames() = %v, missing %q", got, SystemName)
	}
	// Names() is the PALETTE list and must not have grown one.
	for _, n := range Names() {
		if n == SystemName {
			t.Errorf("Names() lists %q; system has no palette and belongs only to SettingNames()", SystemName)
		}
	}
}

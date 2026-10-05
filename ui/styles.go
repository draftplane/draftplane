package ui

import (
	"image/color"
	"sync"

	"charm.land/lipgloss/v2"

	"github.com/draftplane/draftplane/theme"
)

// Styles holds every zone style derived from a *theme.Theme. It is never nil
// in a running model.
//
// Every style here carries its own background (Doc, Card, Chrome, Strip, or
// StatusBar as appropriate) as well as its foreground: nested lipgloss.Render
// calls emit a full SGR reset at their end, so a leaf that doesn't declare
// its own background would let the terminal default bleed through the first
// time it follows a styled span. Declaring the background on every leaf, not
// just the outer row wrap, is what keeps the canvas fully painted.
type Styles struct {
	// Rail is the 2-column band painted at the left edge of every row when
	// the terminal is wide enough; RailCursor is the same band for the row
	// under the cursor. RailCursor draws from Brand, NOT from Focus.
	Rail       lipgloss.Style
	RailCursor lipgloss.Style

	// Document zone (Doc background).
	DocBG lipgloss.Style
	Text  lipgloss.Style
	Dim   lipgloss.Style
	// Heading and HeadingGlyph are every heading at every level: one colour
	// for the sigil and the words alike, separated only by weight -- the
	// words bold, the § run regular. Level is left to the sigil's LENGTH.
	Heading      lipgloss.Style
	HeadingGlyph lipgloss.Style
	CodeText     lipgloss.Style
	CodeSpan     lipgloss.Style
	SectionRule  lipgloss.Style

	// CodeBg is the code-block zone's generic background carrier (gutter,
	// mark, and the outer row-width fill), mirroring how Card serves the
	// same dual role for thread cards. CodeText/CodeSpan carry the Code
	// foreground for the code glyphs themselves, block and inline alike.
	CodeBg lipgloss.Style

	// Thread cards (Card background).
	Card       lipgloss.Style
	CardHeader lipgloss.Style
	// CardResolved is the "(resolved)" row. NOT struck through: it struck a
	// '●' status glyph legibly, but striking the WORD reads as the word being
	// retracted -- resolved, then decided not to be after all.
	CardResolved lipgloss.Style

	// CardHeaderResolved and CardBodyResolved are CardHeader and CardBody
	// inside a RESOLVED thread: the whole card falls back to Dim, the
	// "(resolved)" row's own colour, so a settled thread recedes as one piece
	// rather than announcing itself in Accent beside the label saying it is
	// done. The attribution is ITALIC too -- bodies were already italic, so
	// dimming alone left the header upright above them and the card only half
	// receded -- and it keeps its bold so the card's structure survives.
	CardHeaderResolved lipgloss.Style
	CardBodyResolved   lipgloss.Style
	CardBody           lipgloss.Style
	Badge              lipgloss.Style

	// Compose/confirm panel chrome.
	Chrome lipgloss.Style
	Strip  lipgloss.Style
	// FocusHeader is A PANEL'S LEADING LINE: the compose panel's "comment on:",
	// every confirm strip that is not centred, the relocate header, and the plan
	// list's sort modal title and rename prompt. Four of those are the strip
	// along the bottom of the frame and the sort modal is a centred box;
	// theme.PanelHead's own note says why that does not make it WarnHeader's
	// business.
	//
	// IT IS NAMED FOR THE PANEL AND NOT FOR ITS TOKEN, which is theme.PanelHead
	// rather than theme.Focus. It inked in Focus until the marketing site was
	// taken as the reference for this line's colour. The name stays because
	// what it describes is still true -- the header of the panel currently
	// holding the keyboard -- and because naming a style for its role rather
	// than its hex is already the rule beside it: CardHeader is theme.Accent
	// and Help is theme.Dim.
	FocusHeader lipgloss.Style
	// WarnHeader is FocusHeader's twin for a centred panel's own HEAD ZONE
	// (headThenBody's inkHead, app.centredBox), worn on the leading line of
	// every centred panel whether it asks for a decision, reports a fault, or
	// merely prompts. Same ground, same weight, one colour apart from
	// FocusHeader -- see theme.Warn for why that colour is a role of its own
	// rather than one of the standing ones reused.
	//
	// ONE ROW THAT IS NOT A LEADING LINE paints through it too: the re-point
	// pane's own refusal (app.inkWarn), sat under the input row. It is a second
	// zone name for the identical style rather than a second style.
	//
	// IT CARRIES THE CHROME BACKGROUND, like every style beside it here, and
	// that is load-bearing rather than incidental: centredPanelBox mixes it
	// with Chrome ACROSS A SINGLE ROW (border cell, padding, headline), so a
	// background of its own would change the ground mid-row and draw a band
	// the width of the headline.
	WarnHeader lipgloss.Style
	// Field is a TEXT INPUT'S OWN GROUND inside a panel: the Strip zone's
	// background under the Text foreground, so a row of it reads as an inset
	// band the reader types into rather than as more panel.
	//
	// IT IS theme.Field AND NOT theme.Strip, for a measured reason rather than
	// a symmetrical one: Strip is a step that reads in dark and THREE UNITS in
	// light, so reusing it would have drawn a band in one preset and nothing at
	// all in the other.
	//
	// IT IS THE ONE STYLE HERE THAT DOES NOT CARRY THE CHROME BACKGROUND, and
	// that is the point rather than an oversight: the band IS the background
	// change, drawn by mixing it with Chrome across a single row (see
	// app.centredPanelBox) -- the same mechanic WarnHeader's note warns about,
	// spent deliberately.
	Field lipgloss.Style
	// Chip is a PANEL CONTROL THAT IS CURRENTLY ARMED -- the compose panel's
	// focused button, and the one style here that inverts rather than tints:
	// theme.ChipText's ink on theme.Chip's ground, where theme.Chip holds
	// theme.Focus's own hex in both presets and ChipText is the ink chosen for
	// it. Focus reaches the screen NOWHERE ELSE: the panel headline it used to
	// ink is theme.PanelHead now, so in dark this ground is the last of that blue
	// and a control wearing it reads as the one thing on the row that a keystroke
	// would act on.
	//
	// IT IS A REVIVAL AND NOT A NEW ZONE. theme.Chip/ChipText have been in both
	// presets since the status bar carried a mode chip, and a later change
	// removed that chip and left the pair with no consumer at all. The chip
	// rendered as st.Chip.Render(" " + label + " "); the composer's button is
	// that same span with the label's own brackets in place of the padding.
	Chip lipgloss.Style
	// Control is Chip's OTHER HALF: the same panel control while nothing has
	// armed it -- theme.Text's ink on theme.Control's ground, which is a step
	// off the button row in whichever direction that preset's row leaves free
	// (see theme.Control). An idle button on it reads as an unpressed control
	// rather than as more panel.
	//
	// IT IS theme.Control AND NOT theme.Field, and the difference only shows in
	// ONE preset: they hold the same hex in light, and the idle button rendered
	// through Field until someone drove it on a real terminal. In dark,
	// Field's step is DOWNWARD from a row already near the palette floor and
	// the two buttons read as gaps in the line rather than as controls.
	//
	// UNLIKE Chip IT NEEDS NO INK TOKEN: Text clears WCAG AA on the ground in
	// both presets, so the label wears the same ink as the panel around it.
	Control lipgloss.Style
	Help    lipgloss.Style

	// Status bar.
	StatusBar lipgloss.Style
	OK        lipgloss.Style

	// BrandColor and AccentColor are raw colors, for tinting a foreground
	// against a zone background chosen at the call site: the cursor glyph
	// shows in Brand and the gutter's annotation mark in Accent, each on
	// whichever zone its row turned out to be.
	BrandColor  color.Color
	AccentColor color.Color
}

// NewStyles builds the styles from t. A NIL t STILL ANSWERS NIL -- ParseBlocks
// takes one for callers that want blocks without a projection to draw them
// with -- where the exported RENDERERS take a nil to mean the DEFAULT styles;
// see stylesOrDefault.
func NewStyles(t *theme.Theme) *Styles {
	if t == nil {
		return nil
	}
	c := func(hex string) color.Color { return lipgloss.Color(hex) }
	doc := c(t.Doc)
	card := c(t.Card)
	chrome := c(t.Chrome)
	codeBg := c(t.CodeBg)

	return &Styles{
		Rail:       lipgloss.NewStyle().Background(c(t.Rail)),
		RailCursor: lipgloss.NewStyle().Background(c(t.Brand)),

		DocBG:        lipgloss.NewStyle().Background(doc),
		Text:         lipgloss.NewStyle().Background(doc).Foreground(c(t.Text)),
		Dim:          lipgloss.NewStyle().Background(doc).Foreground(c(t.Dim)),
		Heading:      lipgloss.NewStyle().Background(doc).Foreground(c(t.Heading)).Bold(true),
		HeadingGlyph: lipgloss.NewStyle().Background(doc).Foreground(c(t.Heading)),
		CodeText:     lipgloss.NewStyle().Background(codeBg).Foreground(c(t.Code)),
		CodeSpan:     lipgloss.NewStyle().Background(codeBg).Foreground(c(t.Code)),
		SectionRule:  lipgloss.NewStyle().Background(doc).Foreground(c(t.Dim)),
		CodeBg:       lipgloss.NewStyle().Background(codeBg).Foreground(c(t.Text)),

		Card:         lipgloss.NewStyle().Background(card).Foreground(c(t.Text)),
		CardHeader:   lipgloss.NewStyle().Background(card).Foreground(c(t.Accent)).Bold(true),
		CardResolved: lipgloss.NewStyle().Background(card).Foreground(c(t.Dim)),
		CardBody:     lipgloss.NewStyle().Background(card).Foreground(c(t.Text)).Italic(true),

		CardHeaderResolved: lipgloss.NewStyle().Background(card).Foreground(c(t.Dim)).Bold(true).Italic(true),
		CardBodyResolved:   lipgloss.NewStyle().Background(card).Foreground(c(t.Dim)).Italic(true),
		Badge:              lipgloss.NewStyle().Background(card).Foreground(c(t.Badge)),

		Chrome:      lipgloss.NewStyle().Background(chrome).Foreground(c(t.Text)),
		Strip:       lipgloss.NewStyle().Background(c(t.Strip)).Foreground(c(t.Dim)),
		FocusHeader: lipgloss.NewStyle().Background(chrome).Foreground(c(t.PanelHead)).Bold(true),
		WarnHeader:  lipgloss.NewStyle().Background(chrome).Foreground(c(t.Warn)).Bold(true),
		Field:       lipgloss.NewStyle().Background(c(t.Field)).Foreground(c(t.Text)),
		Chip:        lipgloss.NewStyle().Background(c(t.Chip)).Foreground(c(t.ChipText)),
		Control:     lipgloss.NewStyle().Background(c(t.Control)).Foreground(c(t.Text)),
		Help:        lipgloss.NewStyle().Background(chrome).Foreground(c(t.Dim)),

		StatusBar: lipgloss.NewStyle().Background(c(t.StatusBar)).Foreground(c(t.StatusText)),
		OK:        lipgloss.NewStyle().Background(c(t.StatusBar)).Foreground(c(t.OK)),

		BrandColor:  c(t.Brand),
		AccentColor: c(t.Accent),
	}
}

// defaultStyles is the *Styles a nil one stands for, built once and SHARED by
// every caller that reaches it. Shared rather than freshly built per call
// because tableGridCache keys on the *Styles BY POINTER IDENTITY
// (tableGridCacheHit): a new Styles per render would miss the grid memo on
// every repaint while painting a document that had not changed. Sharing is safe
// for the same reason a Model shares one *Styles across every row on screen --
// lipgloss styles are values and nothing here mutates them in place (OnCard
// copies).
var defaultStyles = sync.OnceValue(func() *Styles { return NewStyles(theme.Default()) })

// stylesOrDefault is the one place a missing *Styles becomes a real one, shared
// by ui's exported render doors so that they cannot answer a nil differently
// from each other. app.themeOrDefault is the identical decision one layer up,
// for a missing *theme.Theme.
//
// A NIL *Styles IS THE DEFAULT ONE, NOT AN UNPAINTED RENDERING.
//
// IT LIVES AT THE EXPORTED DOORS AND NOT IN THE PAINTERS. Every private
// function below them -- renderDocPainted, renderThreadCardPainted,
// renderBlockPainted, paintTables and the rest -- dereferences st freely, and
// that is an invariant worth keeping rather than a check worth spreading: this
// is the package boundary, so establishing "st is never nil" here means there
// is exactly one place in ui where a nil can still exist.
func stylesOrDefault(st *Styles) *Styles {
	if st == nil {
		return defaultStyles()
	}
	return st
}

// OnCard returns a copy of s whose DOCUMENT-zone styles are rebased onto the
// Card background, for rendering a block that carries comments. Every other
// zone (Chrome, StatusBar, the card styles themselves) is left alone: they
// already name their own background and are not what a commented block row
// switches.
//
// A copy rather than a mutation because lipgloss styles are values and the
// original *Styles is shared by every other row on screen.
func (s *Styles) OnCard() *Styles {
	if s == nil {
		return nil
	}
	card, doc := s.Card.GetBackground(), s.DocBG.GetBackground()
	c := *s
	for _, f := range []*lipgloss.Style{
		&c.DocBG, &c.Text, &c.Dim, &c.Heading, &c.HeadingGlyph,
	} {
		*f = f.Background(card)
	}
	// Code recesses to DOC on a card row, not to CodeBg. It still steps down
	// from the row it sits in -- code owns a zone of its own wherever it
	// appears -- but by the one step Card/Doc rather than the two of
	// Card/CodeBg, which read as a hole punched in a raised row.
	for _, f := range []*lipgloss.Style{
		&c.CodeText, &c.CodeSpan, &c.CodeBg,
	} {
		*f = f.Background(doc)
	}
	return &c
}

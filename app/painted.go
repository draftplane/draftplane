package app

import (
	"regexp"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/draftplane/draftplane/ui"
)

// paintedTextareaStyles reuses the panel's Chrome/Strip zone styles so the
// compose textarea reads as part of the same painted block rather than a
// separately-themed widget.
func paintedTextareaStyles(st *ui.Styles) textarea.Styles {
	state := textarea.StyleState{
		Base:             st.Chrome,
		Text:             st.Chrome,
		CursorLine:       st.Chrome,
		LineNumber:       st.Strip,
		CursorLineNumber: st.Strip,
		Placeholder:      st.Chrome,
		Prompt:           st.Chrome,
		EndOfBuffer:      st.Chrome,
	}
	return textarea.Styles{Focused: state, Blurred: state}
}

// viewPainted is the whole review frame: every row, including the blank filler
// below a short document and the status/help bars, is painted edge to edge so
// the terminal background never shows. View is a one-line delegate to it.
func (m *Model) viewPainted() tea.View {
	st := m.styles
	var b strings.Builder
	vh := m.viewHeight()
	railWidth := ui.RailWidth(m.width)
	margin := ""
	if railWidth > 0 {
		margin = st.Rail.Render(strings.Repeat(" ", railWidth))
	}
	// A row of ground above the document, closing the frame at the top the way
	// the margins close it either side. Counted by viewHeight, which is what
	// keeps the help bar on the canvas. DocBG where the rail has collapsed:
	// below railMinWidth there are no margins to close, and a Rail-painted row
	// would be the only rail colour on a screen that is meant to have none.
	topRow := st.DocBG
	if railWidth > 0 {
		topRow = st.Rail
	}
	// THIS IS THE ROW app/mouse.go's docTopRow COUNTS. A masthead added here
	// moves the document down and every click with it.
	b.WriteString(topRow.Width(m.width).Render(""))
	b.WriteString("\n")
	lines, scroll := m.lines, m.scroll
	end := min(scroll+vh, len(lines))
	// ONE "\n" PER ui.Line, which is what makes vh a count of ROWS -- and it is
	// an assumption ui.Line.Text does not currently keep. A Line whose Text holds
	// an embedded newline lands here as two rows written under one row's budget,
	// so the frame comes out taller than the viewport it was cut to. Recorded
	// here rather than worked around here: counting rows in this loop would paper
	// over a mapping the ui package is answerable for.
	//
	// panelRow COUNTS WHAT IS WRITTEN AND DOES NOT REPAIR IT. That is the other
	// side of the note above rather than a reversal of it: the frame is still
	// emitted exactly as it always was, over-tall window and all, and the count
	// merely reports where the panel below actually landed. It is lineAtFrameRow's
	// walk run forwards, for lineAtFrameRow's own reason, and when ui makes
	// Line.Text one screen row again this collapses to docTopRow + vh on its own.
	panelRow := docTopRow
	for i := scroll; i < end; i++ {
		b.WriteString(lines[i].Text)
		b.WriteString("\n")
		panelRow += 1 + strings.Count(lines[i].Text, "\n")
	}
	// BOTH margins, like every row renderDocPainted emits.
	blankDoc := st.DocBG.Width(m.width - 2*railWidth).Render("")
	for i := end - scroll; i < vh; i++ {
		b.WriteString(margin + blankDoc + margin)
		b.WriteString("\n")
		panelRow++
	}
	panel, buttons := m.panelViewPainted()
	b.WriteString(panel)
	// THE COMPOSER'S BUTTON ROW BECOMES A FRAME ROW HERE, AND THIS IS THE ONLY
	// WRITER OF m.composeButtons IN THE PACKAGE. The panel answered a row relative
	// to itself, because only the panel can know how many rows its own header
	// wrapped to; only this function can know how many rows the document window
	// drew. Adding them is what puts the stored row in the coordinate space
	// tea.MouseClickMsg.Y arrives in -- and completing it BEFORE the assignment,
	// rather than in a second statement after one, is what stops the field ever
	// holding a present-looking geometry whose row is still the panel's.
	// See composeButtonGeometry, and do not reintroduce docTopRow + viewHeight() + 1.
	if len(buttons.spans) > 0 {
		buttons.row += panelRow
	}
	m.composeButtons = buttons

	// Both bars sit between the margins too, so the frame closes all the way
	// round rather than opening at the two rows a reader's eye rests on.
	barWidth := max(m.width-2*railWidth, 0)
	// THE STATUS ROW IS THE SEARCH'S FOR THE DURATION: while modeSearch owns the
	// keyboard this row draws the query and NOTHING else -- not statusIdentity,
	// not "✓ approved", not m.status. That is what lets the search cost no row of
	// its own (it is absent from viewHeight for exactly this reason).
	var statusRow string
	if m.mode == modeSearch {
		statusRow = m.searchInputLine(barWidth)
	} else {
		// FILTER BEFORE THE TRUNCATION, and this row is where ui.InferTitle's
		// display call site lands. statusIdentity answers a heading's Block.Text
		// RAW, and m.status's error writers put an error's own text here
		// verbatim. A bare C0 byte in either is zero cells to ansi.Truncate below
		// and one to the Width().Render around it, so a bar budgeted to exactly
		// barWidth comes back as two rows and the frame outgrows the terminal.
		rest := " " + ui.VisibleControls(m.statusIdentity(), st.StatusBar)
		if m.approved {
			rest += st.StatusBar.Render(" · ") + st.OK.Render("✓ approved")
		}
		if m.status != "" {
			// RenderControls and not VisibleControls: st.OK's own Render may
			// have closed just before this, so this fragment needs a style
			// opened in front of it (see ui.RenderControls).
			rest += ui.RenderControls(" · "+m.status, st.StatusBar)
		}
		statusRow = st.StatusBar.Width(barWidth).Render(ansi.Truncate(rest, barWidth, ""))
	}
	b.WriteString(margin + statusRow + margin + "\n")
	b.WriteString(margin + st.Help.Width(barWidth).Render(ansi.Truncate(" "+m.helpBar(), barWidth, "")) + margin)

	// THE SPLICE IS THE LAST THING THAT HAPPENS TO THE FRAME: it needs a frame
	// that is already complete and already m.height rows, because it replaces
	// spans of rows rather than contributing any.
	v := stampMouseMode(tea.NewView(m.composeCentredPanel(b.String())))
	v.AltScreen = true
	return v
}

// searchInputLine is modeSearch's input row: exactly width display cells of
// "/", the term as the reader typed it, and a cursor cell, in the status bar's
// own colours. IT CARRIES THE QUERY AND NOTHING ELSE -- the two exits are named
// on the help bar directly below and only there (searchHint).
//
// IT CLIPS WITH leftClip AND NEVER ansi.Truncate. A term wider than the row has
// to keep its TAIL: the newest characters are the ones being typed, and a
// head-keeping truncation would freeze the visible text at the first keystroke
// past the edge, leaving backspace corresponding to nothing on screen.
//
// "EXACTLY width CELLS" IS leftClip'S BUDGET PLUS THIS PADDING, and it needs
// both: padding a non-negative remainder behind an OVERSHOOT still draws
// width+1, so leftClip has to keep the budget it claims.
func (m *Model) searchInputLine(width int) string {
	if width <= 0 {
		return ""
	}
	st := m.styles
	text := leftClip("/"+m.search, width-1)
	// The cursor takes the brand foreground through the bar's own style and sits
	// IMMEDIATELY after the last character rather than at the row's far edge, with
	// the remainder padded behind it: a cursor parked at the end of the bar reads
	// as decoration, not as the place the next keystroke lands.
	row := st.StatusBar.Render(text) + st.StatusBar.Foreground(st.BrandColor).Render("█")
	if pad := width - 1 - ansi.StringWidth(text); pad > 0 {
		row += st.StatusBar.Render(strings.Repeat(" ", pad))
	}
	return row
}

// panelViewPainted draws every panel body this model puts on screen as a bottom
// strip: the compose header and button row and the confirm header are painted
// Chrome/Strip edge to edge, and the header text carries the Focus color.
// panelView (app/actions.go) is a delegate to it.
//
// IT ANSWERS THE COMPOSER'S BUTTON GEOMETRY AND NEVER WRITES IT ONTO THE MODEL,
// which is what makes m.composeButtons impossible to catch half-finished. The
// row this returns is PANEL-RELATIVE -- the only row this function can know,
// since only viewPainted can count the document rows above the panel -- so a
// value that reached the field from here would be a "present" geometry naming a
// document row near the top of the frame, and a click would resolve against it.
// viewPainted completes it and assigns it; every other caller drops it, and
// dropping it is the whole of what a caller that is not a frame has to do.
//
// EVERY ARM ANSWERS ITS OWN GEOMETRY, the zero value for the panels that draw no
// buttons. Named at each return rather than defaulted once at the top, for the
// reason modeRepoint's own case gives about falling through: a panel that draws
// no buttons is a decision, and the arms that take it say so.
func (m *Model) panelViewPainted() (string, composeButtonGeometry) {
	st := m.styles
	switch m.mode {
	case modeCompose:
		// THE HEADER NAMES THE BLOCK BY ITS HEADING PATH, AND Block.HeadingPath
		// HOLDS RAW SOURCE BYTES. renderBlockPainted's heading arm filters the
		// heading ROW; this string never goes near that arm, so without the filter
		// here the reviewer reads the forged heading in the very panel that asks
		// them to comment on it. Filtered before Width().Render measures, for the
		// reason ui.VisibleControls gives.
		header := st.FocusHeader.Width(m.width).Render(composeHeaderRule+ui.VisibleControls(m.composeTarget(), st.FocusHeader)) + "\n"
		buttons, spans := m.composeButtonRowPainted()
		// THE BUTTON ROW'S OWN ROW, COUNTED FROM WHAT WAS WRITTEN ABOVE IT rather
		// than assumed to be 1: the header is a Width().Render, which WORD-WRAPS a
		// long heading path into two or three rows instead of clipping it, and every
		// one of those rows pushes the buttons down.
		return header + buttons + "\n" + m.composeTextareaPainted() + "\n",
			composeButtonGeometry{row: strings.Count(header, "\n"), spans: spans}
	case modeRepoint:
		// THE PATH PANE DRAWS NOTHING HERE, for modeSourceFault's own reason one
		// case down: its body is spliced into the middle of the finished frame
		// instead (composeCentredPanel) and a strip drawn as well would put the same
		// question on screen twice, under a viewHeight that correctly reserves
		// nothing for it. IT IS NAMED RATHER THAN LEFT TO FALL THROUGH to this
		// switch's silent default, because falling through is what a mode does when
		// nobody has decided where it draws, and this one has been decided.
		return "", composeButtonGeometry{}
	case modeConfirmApprove, modeConflict, modeConfirmDeleteThread, modeSourceFault, modeConfirmDeletePlan, modeKeys:
		// THE CENTRED MODES DRAW NOTHING HERE. Their body is spliced into the
		// middle of the finished frame instead (composeCentredPanel), and a strip
		// drawn as well would put the same words on screen twice. They are still
		// NAMED by this case, and by viewHeight's own list, because both lists are
		// "every mode whose body is m.confirm" and where that body is drawn is
		// drawsCentredPanel's decision rather than theirs.
		if drawsCentredPanel(m.mode) {
			return "", composeButtonGeometry{}
		}
		// Every line m.confirmLines() returns is <= max(m.width-4, 10) DISPLAY
		// CELLS -- guaranteed by its hard-wrap pass, not by word-wrap alone, and not
		// a rune count (see that method's own doc comment). Rendered one row at a
		// time through its own Width(m.width).Render call rather than joined into
		// one multi-line string first, which would let lipgloss's own word-wrap
		// silently add rows behind confirmPanelHeight's back. Width().Render
		// hard-wraps an over-wide line on its own, which is why that guarantee has
		// to hold on the way in.
		lines := m.confirmLines()
		rows := make([]string, 0, len(lines)+1)
		for i, l := range lines {
			prefix := ""
			if i == 0 {
				prefix = "── "
			}
			rows = append(rows, st.FocusHeader.Width(m.width).Render(prefix+l))
		}
		rows = append(rows, st.Chrome.Width(m.width).Render(""))
		return strings.Join(rows, "\n") + "\n", composeButtonGeometry{}
	case modeRelocate:
		// Every element of relocateBodyLines() is already <= width runes (see its
		// doc comment) and each is rendered through its own Width().Render call, one
		// row at a time: Width().Render silently word-wraps rather than truncating
		// when handed content wider than its own width, so a pre-wrapped multi-line
		// blob handed to a single Render call would multiply rows behind
		// relocatePanelHeight's back.
		//
		// The body rows are already-styled thread-card lines carrying their own
		// Card/CardHeader/CardBody backgrounds, so the outer wrap here is st.Card
		// and not st.Chrome: the pad it adds then blends with the card's own
		// background instead of leaving a Chrome-coloured gap.
		rows := make([]string, 0, m.relocatePanelHeight())
		rows = append(rows, st.FocusHeader.Width(m.width).Render("── "+m.relocateHeaderLine()))
		for _, l := range m.relocateBodyLines() {
			rows = append(rows, st.Card.Width(m.width).Render(" "+l))
		}
		rows = append(rows, st.Strip.Width(m.width).Render(" "+m.relocateHint()))
		return strings.Join(rows, "\n") + "\n", composeButtonGeometry{}
	}
	return "", composeButtonGeometry{}
}

// drawsCentredPanel reports whether md's confirm body reaches the screen as a
// BORDERED BOX SPLICED INTO THE MIDDLE OF THE FINISHED FRAME (composeCentredPanel)
// rather than as the full-width strip along the bottom that panelViewPainted draws
// for every other confirm mode.
//
// ONE PREDICATE, FIVE READERS: viewHeight (must NOT reserve -- the splice adds no
// row), panelViewPainted (must NOT draw the strip -- the body would then be on
// screen twice), confirmGroups (wraps the text to the BOX's own interior width,
// centredInterior, not the strip's m.width-4), confirmBudget (caps the ROW COUNT
// against the same box geometry), and composeCentredPanel (does the splice). A mode
// moves between the two placements BY BEING NAMED HERE AND NOWHERE ELSE; get it
// half-named and the panel either draws twice, reserves rows nothing paints, or
// wraps to one placement's width while budgeted for the other's.
//
// THE LIST IS A ROUTING DECISION RATHER THAN A PROPERTY OF THE MODE. Panel 1
// (modeSourceFault) draws in the box, and so do the two gestures it opens:
// modeRepoint, the path pane, the one member whose body is not m.confirm (which
// changes nothing here and one line in the box, centredGroups), and
// modeConfirmDeletePlan, whose box does not simply take confirmGroups' own default
// width either -- see deleteConfirmInterior for why "the same box" binds the WIDTH
// and not only the placement.
//
// modeKeys BELONGS HERE: the "?" panel is an ordinary confirm
// body (m.confirm, built once at entry by enterKeysPanel) with nothing about
// it that argues for the bottom strip over the box.
func drawsCentredPanel(md mode) bool {
	return md == modeSourceFault || md == modeRepoint || md == modeConfirmDeletePlan || md == modeKeys
}

// panelInk names WHICH OF THE BOX'S ZONES a group of rows is painted in, and
// the box infers nothing: a body with an INPUT ROW in the middle of it has
// emphasis that is not positional at all, so the group carries its own zone.
//
// A CLOSED SET OF ZONES AND NOT A lipgloss.Style PER GROUP: what a group may
// ask for is closed here, so the box keeps deciding what each zone actually
// looks like and a body cannot hand it a colour of its own.
type panelInk uint8

const (
	// inkBody is the panel's own ground -- Chrome, and the zero value, so a
	// group that says nothing is body text.
	inkBody panelInk = iota
	// inkHead is the line the panel leads with (WarnHeader).
	inkHead
	// inkField is a text input's inset band (Field): one step off the
	// panel's ground, spanning the box's whole interior, which is what makes
	// it read as somewhere to type rather than as more panel.
	inkField
	// inkWarn is a warning row that is NOT the panel's leading line -- the
	// re-point refusal, which sits under the input row rather than at the top of
	// the body. IT PAINTS THROUGH WarnHeader, THE SAME STYLE AS inkHead
	// (theme.Warn is the one warning colour this box has), so the two are
	// visually identical; they stay two consts because inkHead's own doc comment
	// ties it to POSITION, and a group saying inkHead when it is not the head
	// would make that comment a lie about whichever row wore it.
	inkWarn
)

// panelGroup is one logical line of a centred panel's body, as the screen rows
// it wrapped to plus the zone it is painted in.
type panelGroup struct {
	rows []string
	ink  panelInk
}

// centredGroups is the body whichever centred mode is open puts in the box.
// Every member but the path pane and modeConfirmDeletePlan draws the confirm body
// every panel in this model composes into m.confirm -- head, then body
// (headThenBody). The exceptions differ in WHY they cannot leave it at that: the
// path pane's body is built row by row because one of its rows is a live input in
// a zone of its own (repointGroups); modeConfirmDeletePlan's is headThenBody's own
// output, widened by one row so the box holds Panel 1's width rather than its own
// four short lines -- see deleteConfirmInterior.
//
// A DISPATCH AND NOT A FIELD: holding the rows on the model would mean
// recomposing them on every keystroke and would give the box a second thing
// that can be stale.
func (m *Model) centredGroups() []panelGroup {
	if m.mode == modeRepoint {
		return m.repointGroups()
	}
	groups := headThenBody(m.confirmGroups())
	if m.mode == modeConfirmDeletePlan {
		// ONE ROW IS ENOUGH: centredBox derives the box's width from the WIDEST row
		// across every group it is handed, so forcing a single row out to the target
		// interior forces the derived max there too -- every other row then gets the
		// identical pad from centredBox's own per-row padRightIn call. The head row
		// is picked because every confirm body has one; which row carries the
		// padding is otherwise arbitrary.
		groups[0].rows[0] = padRightIn(groups[0].rows[0], m.deleteConfirmInterior(), m.styles.WarnHeader)
	}
	return groups
}

// headThenBody is the rule centredPanelBox used to hold: the first logical
// line is what the panel leads with and everything after it is body. It is
// stated here, once, for every confirm panel that reaches the box, rather than
// inside the box where a body with a different shape could not opt out of it.
func headThenBody(groups [][]string) []panelGroup {
	out := make([]panelGroup, 0, len(groups))
	for i, g := range groups {
		ink := inkBody
		if i == 0 {
			ink = inkHead
		}
		out = append(out, panelGroup{rows: g, ink: ink})
	}
	return out
}

// centredPanelBox draws m.centredGroups() as a bordered box: TWO cells of padding
// and one border cell each side, and one blank row inside the border above the lines
// and below them -- so the box is the widest line + 6 cells across, and its own line
// count + 4 down -- sized off the LINES rather than off the terminal. That is what
// lets it centre correctly: a reworded line moves the box's width and the centring
// with it, in one place. Both numbers travel through confirmBudget, which derives
// the rows and columns the box costs.
//
// THE BORDER IS HEAVY (┏━┓┃┗┛) AND NOT LIGHT, drawn as literal runes rather than
// through a lipgloss.Border because the box is assembled fragment by fragment below
// and never handed to lipgloss's border renderer -- the SAME six glyphs a
// lipgloss.ThickBorder would supply, and the box has no junctions to match. ui's
// TABLES stay light (tableGridBorder, ui/painted.go).
//
// EVERY FRAGMENT GOES THROUGH A STYLE CARRYING ITS OWN BACKGROUND, border cells
// included: a nested lipgloss.Render ends in a full SGR reset, so an unstyled
// fragment after one shows the terminal's default background -- here, the DOCUMENT
// through the box. Chrome and WarnHeader share one background, so mixing them across
// a row changes the ink and never the ground. THE WHOLE BORDER IS ONE STYLE, AND IT
// IS CHROME'S: two colours on one rectangle reads as a rendering fault rather than
// as emphasis, and it is DELIBERATELY NOT THE HEAD ROW'S COLOUR EITHER -- a border
// shouting with the headline leaves nothing quieter for it to be louder THAN.
//
// THE PAD IS padRightIn AND NOT padRight: every line here has been through
// ui.VisibleControls, and that filter renders a substituted Control Picture through
// a NESTED style whose SGR reset ends the outer Render early, so naked pad spaces
// after one paint a visible notch inside the box. It is byte-for-byte padRight for a
// row with no ESC in it.
//
// EVERY GROUP CARRIES THE ZONE IT IS PAINTED IN AND THIS LOOP INFERS NOTHING (see
// panelInk): re-point's input row is emphasis in the middle of a body, and no
// positional rule reaches it. IT IS WarnHeader AND NOT FocusHeader, and the role is
// new rather than borrowed -- theme.Warn carries the argument.
func (m *Model) centredPanelBox() string { return centredBox(m.centredGroups(), m.styles) }

// centredBox is the box itself, over the rows it is handed and the palette it is
// handed, and it belongs to NEITHER MODEL -- which is the whole of what lets the
// plan list draw the same panel as the review model instead of a second one that
// looks nearly like it. A SECOND COPY IS HOW TWO PANELS COME TO DISAGREE ABOUT A
// BORDER: this box already came out two colours once, and that was one box.
func centredBox(groups []panelGroup, st *ui.Styles) string {
	width, rows := 0, 0
	for _, g := range groups {
		rows += len(g.rows)
		for _, l := range g.rows {
			width = max(width, ansi.StringWidth(l))
		}
	}
	rule := strings.Repeat("━", width+4)
	gap := strings.Repeat(" ", width+4)

	out := make([]string, 0, rows+4)
	out = append(out, st.Chrome.Render("┏"+rule+"┓"), st.Chrome.Render("┃"+gap+"┃"))
	for _, g := range groups {
		style := st.Chrome
		switch g.ink {
		case inkHead, inkWarn:
			style = st.WarnHeader
		case inkField:
			style = st.Field
		}
		for _, l := range g.rows {
			out = append(out, st.Chrome.Render("┃  ")+style.Render(padRightIn(l, width, style))+st.Chrome.Render("  ┃"))
		}
	}
	out = append(out, st.Chrome.Render("┃"+gap+"┃"))
	return strings.Join(append(out, st.Chrome.Render("┗"+rule+"┛")), "\n")
}

// composeCentredPanel is the WHOLE of how a centred confirm panel reaches the
// screen: viewPainted hands it the finished frame on its last line and it splices
// the box into the middle. A no-op in every other mode, which is what lets that one
// call site carry no branch of its own.
//
// IT CHANGES NO LAYOUT ARITHMETIC: ui.Overlay replaces spans of rows the frame
// already has -- no row added, no row removed, and no row wider than it went in. So
// viewHeight reserves nothing for a centred mode and there is no reservation left
// that could drift out of step with what is painted.
//
// THERE IS NO CLAMP HERE, DELIBERATELY: ui.Overlay clamps inside itself because
// staying inside the background is an invariant of compositing, and a second clamp
// at this call site would be a second place for the two to disagree. THIS PANEL HAS
// NO MINIMUM-SIZE GATE AT ALL (the review model has none, only the list does), so
// below about 30x11 both numbers go negative and Overlay's clamp is a live
// correction rather than a guarantee. That is the ruled behaviour -- strip to fit,
// never refuse and never fall back to the bottom strip -- and confirmBudget does the
// stripping.
//
// ⚠️ BOTH NUMBERS ARE MEASURED OFF THE FRAME AND NOT OFF m.width/m.height, because
// the frame it is handed is NOT reliably m.height rows: ui.Line.Text may carry an
// EMBEDDED NEWLINE and viewPainted writes exactly one "\n" per Line, so one heading
// too wide for its slot makes the frame a row taller than the viewport it was cut
// to. A box centred against a height the frame does not have sits off-centre from
// what the reader sees. It is deliberately NOT a repair of the row count: trimming
// the frame here would hide a mapping the ui package is answerable for.
func (m *Model) composeCentredPanel(frame string) string {
	if !drawsCentredPanel(m.mode) {
		return frame
	}
	bg := squareFrame(frame, m.width)
	bgRows := strings.Split(bg, "\n")
	box := m.centredPanelBox()
	rows := strings.Split(box, "\n")
	return ui.Overlay(bg, box,
		(ansi.StringWidth(bgRows[0])-ansi.StringWidth(rows[0]))/2,
		(len(bgRows)-len(rows))/2)
}

// ansiSGR matches one SGR escape sequence, e.g. "\x1b[38;2;1;2;3;48;2;4;5;6m".
var ansiSGR = regexp.MustCompile("\x1b\\[[0-9;]*m")

// hasUnpaintedGap reports whether raw -- one row straight from
// textarea.Model.View(), escape sequences included -- contains a run of visible
// characters with no color escape active: a bare reset (or no escape at all)
// followed by printable content. bubbles' textarea only reliably backgrounds a
// row when it carries a real glyph (typed text, the placeholder line, or the
// line-number gutter); its "blank" filler rows go through an internal,
// un-backgrounded width pad that leaves exactly this kind of gap. Such a row
// can't be fixed by re-padding its tail -- it's already at its target width --
// so it has to be discarded and rebuilt clean instead.
//
// INVARIANT this detector depends on: "any active SGR" is treated as
// "background painted", without parsing for an actual 48; background parameter.
// That is only sound because every style ui.NewStyles builds for the compose
// paint path (Chrome, Strip, FocusHeader, and the textarea styles derived from
// them) sets a background alongside its foreground -- a foreground-only style
// reaching this path would make a naked-background run read as painted.
// TestComposePaintStylesAlwaysCarryBackground guards that across every preset.
// (The widget's own reverse-video cursor cell, SGR 7 with no color parameters,
// also counts as painted: that IS the visible cursor, not a leak.)
func hasUnpaintedGap(raw string) bool {
	codes := ansiSGR.FindAllString(raw, -1)
	texts := ansiSGR.Split(raw, -1)
	colored := false
	for i, text := range texts {
		if text != "" && !colored {
			return true
		}
		if i < len(codes) {
			colored = codes[i] != "\x1b[m" && codes[i] != "\x1b[0m"
		}
	}
	return false
}

const (
	// composeHeaderRule is what the compose panel's header is drawn behind, and it
	// is named rather than spelled inline at the one place that renders it because
	// the button row is indented to its WIDTH (composeButtonLead). The panel's
	// other neighbours draw the same three cells as literals and keep them: this
	// constant exists to tie TWO ROWS OF ONE PANEL together, not to sweep a string
	// nobody has to agree with.
	composeHeaderRule = "── "
	// composeButtonGap separates the two buttons, and it is TWO CELLS rather than
	// one because no span covers it: a click that lands between the buttons
	// presses neither, so the gap is the miss zone between two controls that
	// do OPPOSITE things. One cell of margin for error between "post this" and
	// "throw it away" is not enough.
	composeButtonGap = "  "
)

// composeButtonLead is the button row's own left inset, and it is exactly the
// width of the rule the header above it is drawn behind: the '[' of [ post ]
// lands under the first letter of "comment on:", so the panel's two rows of
// chrome share one left edge instead of stepping.
//
// IT WAS ONE SPACE UNTIL A HUMAN DROVE IT ON A REAL TERMINAL. That single cell
// is the inset every OTHER strip in this file draws its text behind (the path
// pane's hint, one arm down), and copying it was the obvious thing to do -- but
// no other strip sits directly beneath a header it has to line up with, and on
// screen the two-cell step reads as the row having come loose from the panel.
//
// MEASURED OFF composeHeaderRule AND NOT WRITTEN AS THREE SPACES, because the
// number is not a fact about this row: it is a fact about what the header draws
// in front of its text, which is two box-drawing runes and a space TODAY. A
// literal here would be a second place holding the same number, and the way that
// goes wrong is silent -- the rows drift apart and everything still compiles.
// TestComposeButtonRowStartsUnderTheHeaderText is what fails if they ever do.
var composeButtonLead = strings.Repeat(" ", ansi.StringWidth(composeHeaderRule))

// composeButtonBar is the composer's two controls in the order they are drawn,
// which is the RING's own order minus the editor (composeFocus): a reader who
// tabs rightward walks the row rightward.
//
// THE LABELS CARRY THEIR OWN BRACKETS. They are what says "this is a control and
// not a line of help" on a row that reads as prose otherwise, and they are the
// target the pointer aims at -- eight cells for post, ten for cancel.
var composeButtonBar = []struct {
	focus composeFocus
	label string
}{
	{composeFocusPost, "[ post ]"},
	{composeFocusCancel, "[ cancel ]"},
}

// composeButtonRowPainted is the composer's second row: [ post ] and [ cancel ]
// as two pressable controls, drawn where the hint strip used to be. ONE ROW OUT,
// ONE ROW IN -- the panel is still m.ta.Height()+2 rows and viewHeight's
// reservation is untouched.
//
// CONCATENATED SPANS AND NOT ONE Width().Render, because this row carries TWO
// GROUNDS. A single styled Render expresses exactly one background, and the whole
// of what the focused button says is that it is on a different one. The shape is
// renderCellsPainted's band-plus-content and centredBox's three spans across one
// row; it is also the shape a now-deleted mode chip drew, st.Chip.Render(" " +
// label + " "), on this file's status bar.
//
// ARMED IS Chip, IDLE IS Control, and they are two states of one control rather
// than two zones that happen to meet here. Chip is theme.Focus's hex worn as a
// background under an ink chosen for it, so the armed button is the one thing on
// the row a keystroke would act on. Control is that same button at rest, a step
// off the button row in whichever direction its preset leaves free, so it reads
// as an unpressed control rather than as more panel. Dim ink was the obvious
// idle choice and falls under AA in both presets, where Text clears AA on both
// Control grounds -- which is why Control needs no ink token beside it.
//
// IT WAS Field UNTIL A HUMAN DROVE IT ON A REAL TERMINAL. Field is a step DOWN
// from its surroundings in both presets, which is right for a text input and
// right in light here by luck of where light's row sits -- but in dark it lands
// near the palette floor and the two buttons read as gaps in the line rather
// than as controls. Light kept its byte-for-byte rendering through the fix;
// theme.Control carries the argument for why the fix had to be a token.
//
// THE PAD GOES THROUGH padRightIn AND NEVER BARE SPACES: a nested Render ends in
// a full SGR reset, so blanks written raw behind one would let the reader's own
// terminal background through the tail of every compose panel. ui/styles.go's
// header states the invariant and hasUnpaintedGap is what catches a breach.
//
// IT ANSWERS THE SPANS IT DREW, which is half of what the function is for rather
// than a side effect of it. The hit test resolves a click against these
// columns instead of re-deriving them, so a label that stops being pure ASCII --
// or spacing that changes -- moves the target and the glyphs together. They are
// RETURNED rather than written onto the model here so that the row and the spans
// land in one assignment at the caller: they are one fact about one row, and a
// geometry half-written by this function would be a state a later edit could
// leave behind.
func (m *Model) composeButtonRowPainted() (string, []composeButtonSpan) {
	st := m.styles
	row := st.Strip.Render(composeButtonLead)
	col := ansi.StringWidth(composeButtonLead)
	var spans []composeButtonSpan
	for i, b := range composeButtonBar {
		if i > 0 {
			row += st.Strip.Render(composeButtonGap)
			col += ansi.StringWidth(composeButtonGap)
		}
		ground := st.Control
		if m.composeFocus == b.focus {
			ground = st.Chip
		}
		row += ground.Render(b.label)
		width := ansi.StringWidth(b.label)
		// A BUTTON THE ROW CANNOT HOLD WHOLE IS PRESSABLE OVER THE PART THAT IS ON
		// SCREEN AND DEAD OVER THE PART THAT IS NOT, because the row is clipped to
		// m.width below and a span claiming the whole label would name columns the
		// reader cannot see. Only a terminal narrower than the two labels reaches
		// this, and such a terminal already overflows the frame for its own reasons.
		if visible := min(width, m.width-col); visible > 0 {
			spans = append(spans, composeButtonSpan{focus: b.focus, col: col, width: visible})
		}
		col += width
	}
	// EXACTLY m.width CELLS EITHER WAY, and it takes both calls: the truncation is
	// inert on a row that fits and the pad is inert on one that does not, and a row
	// off the budget in either direction breaks a frame the status and help bars
	// are counted into.
	return padRightIn(ansi.Truncate(row, m.width, ""), m.width, st.Strip), spans
}

// composeTextareaPainted repaints the compose textarea's rows for the painted
// path. The widget's own rendering is always the base -- never bypassed or
// hand-rebuilt, so its cursor cell, line-number gutter and placeholder come
// through exactly as the widget draws them, in every draft state. The widget is
// 4 columns narrower than the panel (see the WindowSizeMsg handler), so every
// clean row is re-padded out to the panel's full width; a row whose own
// rendering carries an unpainted gap (see hasUnpaintedGap) is discarded and
// rebuilt instead, as the widget's own left column and then the field -- and
// the field's ground is Chrome, the SAME ground the rest of the panel and every
// typed row are painted on, in every draft state, empty or not.
// paintTextareaRows carries that column's own reasoning.
//
// THAT USED TO BE TWO GROUNDS, Strip while the draft was empty and Chrome from
// the first keystroke on; that branch was deleted: a field that changes colour
// under the reader's own cursor reads as two widgets wearing one frame, not
// one field learning to hold text. The branch's justification went with it --
// it once matched the placeholder row's own Strip text and the hint line
// printed above the textarea, but that hint was replaced with the button row,
// and paintedTextareaStyles now hands the placeholder Chrome too, so there is
// no Strip left on this panel for a rebuilt row's FIELD to match.
// The Strip that is still rendered here is the gutter band, and it is a claim
// about WHERE a cell sits rather than about whether the draft is empty: it
// stands at the same width on the same rows before and after the first
// keystroke, which is exactly what the deleted branch did not do.
//
// WHETHER THE DRAFT COUNTS AS "EMPTY" IS BUBBLES' DECISION NOW, AND BUBBLES
// DECIDES IT ON THE LITERAL STRING, NEVER TrimSpace. Do not hunt this file for
// the conditional: with the branch above deleted there is no emptiness test on
// this render path at all, and the only one left is the widget's own -- it
// draws its placeholder while Value() is exactly "", and renders the buffer
// otherwise. (Its condition carries two further terms, cursor at the origin and
// a placeholder set, and neither can be false here: an empty value puts the
// cursor at the origin by construction, and this panel always sets a
// placeholder.) The rule is recorded on this path because the path depends on
// it, not because the path applies it. A whitespace-only draft is real, visible
// input mid-edit -- spaces the reader typed, a cursor sitting among them -- and
// has to render through the widget exactly like any other content. TrimSpace is
// dispatchComposePost's rule for whether there is anything worth POSTING,
// decided once at submission; it has never governed what the field looks like
// while the reader is still typing, and nothing on this render path should
// start asking TrimSpace that question either.
func (m *Model) composeTextareaPainted() string {
	return paintTextareaRows(m.ta, m.width, m.styles)
}

// textareaGutterGap restates the gap bubbles hardcodes between its line-number
// field and the text. textareaGutterWidth, its only caller, carries the argument
// for restating a constant that is not ours.
const textareaGutterGap = 2

// textareaPromptWidth is the LEFT PART of the gutter described above: the
// prompt alone, and the part bubbles draws on the TEXT's ground rather than the
// line-number field's (paintedTextareaStyles hands Prompt st.Chrome and
// LineNumber st.Strip). That split is what paintTextareaRows' band is built
// around, so it is named here rather than spelled at the caller -- and
// textareaGutterWidth is defined in terms of it, so the prompt's width is read
// in exactly one place and the two can never disagree.
func textareaPromptWidth(ta textarea.Model) int {
	return ansi.StringWidth(ta.Prompt)
}

// textareaGutterWidth is the display width of the column bubbles draws to the
// LEFT of a row's text: its prompt, then the line-number field.
//
// DERIVED FROM THE WIDGET'S OWN EXPORTED SETTINGS AND THEN PINNED, because the
// third term is not ours. bubbles reserves numDigits(MaxHeight) plus a gap it
// hardcodes to 2 -- and carries an XXX on that constant saying it should be
// reduced to one. An upgrade that acts on it silently misaligns this band, so
// TestTextareaGutterWidthMatchesWhatTheWidgetDraws measures the real rendering
// and reddens rather than letting the drift ship.
//
// MaxHeight, not the current line count: the reservation is fixed for the
// widget's lifetime, so this width does not move as the draft grows.
func textareaGutterWidth(ta textarea.Model) int {
	if !ta.ShowLineNumbers {
		return textareaPromptWidth(ta)
	}
	return textareaPromptWidth(ta) + len(strconv.Itoa(ta.MaxHeight)) + textareaGutterGap
}

// paintTextareaRows is composeTextareaPainted's logic, factored out so the
// list's rename panel (a second, independent textarea.Model instance) can
// share the identical widget-quirk workaround instead of forking it. See
// composeTextareaPainted's own doc comment for the rationale.
//
// A REBUILT ROW IS THREE SPANS, NOT ONE, because it has to carry the same left
// GROUND the widget draws on the rows it painted itself -- the grounds, and
// deliberately not the glyphs standing on them (see NOT SYNTHESISED below).
// Two different widget paths hand this arm a row with no gutter on it. While
// the draft is EMPTY, bubbles writes a line number for row 0 and for the rows
// its placeholder text occupies, and every row past that reaches a bare
// default: arm in its placeholder view that draws an end-of-buffer character
// and stops. Once the draft is TYPED, the rows past the last line come from a
// different mechanism entirely -- the end-of-buffer loop at the foot of the
// widget's own view(), which pads its row short by the width of the
// line-number field it never writes. Either way the row arrives with an
// unpainted gap where the gutter should be, so on an empty ten-row composer the
// gutter appeared on one row, the field had no visible left edge until the
// reader typed, and the edge then grew downwards a row at a time. The Strip
// span restores the column the widget stopped drawing; the Chrome spans either
// side of it are the prompt's own ground and the field, both on the ground the
// rest of the panel and every typed row already use.
//
// THE BAND STARTS AT THE PROMPT'S WIDTH AND NOT AT COLUMN 0, and this is the
// non-obvious half. The widget's left column is NOT all one ground: bubbles
// draws its prompt through Prompt and its line number through LineNumber, and
// paintedTextareaStyles hands those st.Chrome and st.Strip respectively -- so on
// a row the widget painted, the first textareaPromptWidth cells are the TEXT's
// ground and only the line-number field is the gutter's. A band run from column
// 0 would therefore be two cells wider than the widget's own, and the field's
// left edge would carry a visible notch at row 0, where the two meet. Do not
// "simplify" this to a single Strip span starting at 0; the notch is what that
// costs.
//
// NOT SYNTHESISED: NEITHER THE NUMBERS NOR THE PROMPT BAR. Only the band is
// wanted. For the numbers: a new number appearing as each new line of text is
// created is correct, and drawing digits the widget did not draw would be
// inventing content on the reader's behalf. The prompt bar is the same
// reasoning applied to the glyph in front of them: bubbles draws its Prompt
// ("┃ ", two cells) on every row it renders itself and a rebuilt row is fill
// rather than a re-rendering, so the bar now stops at the last row the widget
// drew while the ground beneath it runs the full height of the field.
//
// THAT ABSENCE IS THE WANTED RESULT, and this paragraph exists so the next
// reader does not "finish" the job by drawing it. The bar was never asked for;
// it turned up as a consequence and was then driven and looked at, and the
// ruling was that its absence below the draft is an IMPROVEMENT -- the bar
// creeping downwards one row per typed line was itself the odd artifact, and
// what those rows owe the reader is the field's shape, which is the ground's
// job and now the band's. A full-height ground under a bar that stops at the
// last typed line is therefore the finished state, not a half-finish: do not
// synthesise a ┃ onto rebuilt rows to "complete" it, and do not read the
// asymmetry as a bug. Drawing either the digits or the bar would breach the
// rule stated directly below as well.
//
// ONLY REBUILT ROWS ARE TOUCHED. The band goes exclusively to rows the
// widget rendered with an unpainted gap, so the cursor row, the placeholder row
// and every typed row keep coming through exactly as the widget drew them --
// ta.View() is still the base in every draft state, never bypassed and never
// hand-rebuilt, which is this path's strongest guarantee.
//
// BOTH BOUNDARIES ARE CLAMPED TO width, because each span's width is a
// difference: on a frame narrower than the gutter -- or narrower than the prompt
// in front of it -- an unclamped boundary would ask lipgloss for a negative
// width, and the row would stop being exactly width cells, the invariant every
// panel row here is held to. Clamping both to width also keeps them ordered
// (lead <= gutter holds because textareaGutterWidth is textareaPromptWidth plus
// a non-negative remainder), which is what makes the middle span safe. They are
// computed once rather than per row because no term moves inside the loop:
// MaxHeight fixes the gutter for the widget's lifetime, so neither boundary
// shifts as the draft grows.
func paintTextareaRows(ta textarea.Model, width int, st *ui.Styles) string {
	lead := min(textareaPromptWidth(ta), width)
	gutter := min(textareaGutterWidth(ta), width)
	rows := strings.Split(ta.View(), "\n")
	for i, row := range rows {
		if hasUnpaintedGap(row) {
			rows[i] = st.Chrome.Width(lead).Render("") +
				st.Strip.Width(gutter-lead).Render("") +
				st.Chrome.Width(width-gutter).Render("")
			continue
		}
		rows[i] = st.Chrome.Width(width).Render(row)
	}
	return strings.Join(rows, "\n")
}

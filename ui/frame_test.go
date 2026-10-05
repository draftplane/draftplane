package ui

import (
	"fmt"
	"strings"
	"testing"
)

// docFrame is a document rendered as one frame: RenderDoc's rows joined with
// the row separator, which is what app/painted.go writes them as.
func docFrame(t *testing.T, src string, width int, st *Styles) string {
	t.Helper()
	var b strings.Builder
	for _, l := range RenderDoc(ParseBlocks([]byte(src), st), nil, nil, nil, OnLine(NoCursor), width, "cm", st) {
		b.WriteString(l.Text)
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		t.Fatalf("the fixture rendered to nothing at all: %q", src)
	}
	return b.String()
}

// requireCleanFrame is the invariant, applied. It reports every violation
// rather than the first, because a payload that gets through usually gets
// through in more than one place and a list says which channels rather than
// which byte.
//
// THE CSI GUARD IS THE NON-VACUITY HALF and it runs first. A frame with no CSI
// in it at all is an unstyled or empty string, and "no OSC and no stray C0" is
// true of the empty string -- so without this an assertion that had quietly
// stopped rendering anything would keep passing.
func requireCleanFrame(t *testing.T, what, frame string) {
	t.Helper()
	if csi := FrameCSICount(frame); csi == 0 {
		t.Fatalf("%s carries no CSI sequence at all, so it is not a styled frame and this assertion proves nothing: %q", what, frame)
	}
	if v := FrameViolations(frame); len(v) != 0 {
		var lines []string
		for _, one := range v {
			lines = append(lines, "  "+one.String())
		}
		t.Fatalf("%s carries %d thing(s) a benign frame must not:\n%s", what, len(v), strings.Join(lines, "\n"))
	}
}

// frameKindCases is one synthetic frame per kind the scan knows, each built by
// splicing a payload into an ordinary styled-looking row.
//
// SYNTHETIC ON PURPOSE, and it is the only place in this file that is. Every
// other assertion drives a real renderer, which is the point -- but a renderer
// that has been taught to filter cannot produce an OSC to check the scan
// against, so the scan's own coverage has to be stated directly or the day
// somebody breaks the OSC arm nothing fails.
var frameKindCases = []struct {
	name    string
	payload string
	kind    FrameViolationKind
	b       byte
}{
	{"an OSC 8 hyperlink", "\x1b]8;;https://evil.example\x07", ViolationOSC, ']'},
	{"a DCS", "\x1bPq#0;2;0;0;0\x1b\\", ViolationDCS, 'P'},
	{"an APC", "\x1b_payload\x1b\\", ViolationAPC, '_'},
	{"an SOS, the sibling the invariant does not name", "\x1bXpayload\x1b\\", ViolationSOS, 'X'},
	{"a PM, the other one", "\x1b^payload\x1b\\", ViolationPM, '^'},
	{"a backspace, which needs no escape at all", "\b", ViolationC0, '\b'},
	{"a carriage return", "\r", ViolationC0, '\r'},
	{"DEL", "\x7f", ViolationC0, 0x7f},
	{"a bare ESC that opens nothing", "\x1b", ViolationC0, esc},
	{"a tab, whose two width authorities disagree", "\t", ViolationC0, '\t'},
	// THE EIGHT-BIT HALF OF THE SAME FIVE, which this scan was once blind to.
	// Each is one byte where the rows above are two, it opens the identical
	// hazard, and Draftplane emits none -- which is also why the 8-bit CSI is
	// here and the 7-bit one is not.
	{"an 8-bit APC, the raw byte", "\x9fpayload", ViolationC1, 0x9f},
	{"an 8-bit OSC", "\x9d8;;https://evil.example\x9c", ViolationC1, 0x9d},
	{"an 8-bit DCS", "\x90q#0;2;0;0;0\x9c", ViolationC1, 0x90},
	{"an 8-bit SOS", "\x98payload\x9c", ViolationC1, 0x98},
	{"an 8-bit PM", "\x9epayload\x9c", ViolationC1, 0x9e},
	{"an 8-bit CSI, which the 7-bit spelling cannot be asserted as", "\x9b31m", ViolationC1, 0x9b},
	{"an ST, a C1 that introduces nothing", "\x9c", ViolationC1, 0x9c},
	// THE SPELLING THAT SURVIVES A JSON ROUND TRIP, and so the one that reaches
	// this frame through an agent's save rather than only off a local file.
	{"a UTF-8-encoded CSI, U+009B", "\u009b31m", ViolationC1, 0x9b},
	{"a UTF-8-encoded APC, U+009F", "\u009fpayload", ViolationC1, 0x9f},
}

// TestFrameViolationsNamesWhatItFound is the scan's own coverage, and the
// assertion is on the MESSAGE as much as on the kind: a frame invariant that
// failed without naming the byte would report that the frame moved rather than
// what moved it, which is the discipline every mutation test in this file is
// held to.
func TestFrameViolationsNamesWhatItFound(t *testing.T) {
	// A row shaped like a real one: our own SGR either side of the payload, so
	// every case also proves the scan is not confused by the hundreds of CSI
	// sequences a real frame buries a payload in.
	row := func(payload string) string {
		return "\x1b[38;2;214;211;204m before " + payload + " after \x1b[m\n\x1b[1msecond row\x1b[m\n"
	}
	for _, c := range frameKindCases {
		t.Run(c.name, func(t *testing.T) {
			frame := row(c.payload)
			// The payload must add no CSI of its own, or the case is
			// measuring the scan stepping over ITS sequence rather than
			// finding it.
			if got, base := FrameCSICount(frame), FrameCSICount(row("plain text")); got != base {
				t.Fatalf("the row holds %d CSI sequences with this payload in it and %d without -- the payload is being counted as one of ours", got, base)
			}
			v := FrameViolations(frame)
			if len(v) == 0 {
				t.Fatalf("FrameViolations found nothing in %q", frame)
			}
			if v[0].Kind != c.kind || v[0].Byte != c.b {
				t.Fatalf("first violation is %v/0x%02x, want %v/0x%02x: %v", v[0].Kind, v[0].Byte, c.kind, c.b, v)
			}
			if v[0].Row != 0 {
				t.Fatalf("the violation is reported on row %d, want 0 -- rows are counted by the separator and the payload is on the first", v[0].Row)
			}
			msg := v[0].String()
			if !strings.Contains(msg, string(c.kind)) {
				t.Fatalf("the message does not name the kind: %q", msg)
			}
			if !strings.Contains(msg, "before") && !strings.Contains(msg, "after") {
				t.Fatalf("the message carries no sample of the row it is on: %q", msg)
			}
		})
	}
	t.Run("a frame with nothing wrong with it", func(t *testing.T) {
		if v := FrameViolations(row("plain text")); len(v) != 0 {
			t.Fatalf("FrameViolations reported %v on a frame carrying only our own SGR -- every case above is then meaningless", v)
		}
	})
	// THE FALSE-POSITIVE GUARD FOR THE C1 ARM, and it is the reason that arm
	// decodes instead of testing the byte. 0x80-0x9F is also the UTF-8
	// CONTINUATION-BYTE range, so every rune below carries a byte in it: ␍ is
	// E2 90 8D (0x90 is DCS), – is E2 80 93, and ␈ -- the glyph THIS FIX
	// PUTS ON THE SCREEN for a backspace -- is E2 90 88. A byte test would make
	// the invariant permanently red on every frame the fix draws.
	t.Run("ordinary runes whose UTF-8 bytes fall in the C1 range", func(t *testing.T) {
		if v := FrameViolations(row("␍ ␈ ␡ – — § ✅ 日本")); len(v) != 0 {
			t.Fatalf("FrameViolations reported %v on a row of ordinary runes -- the C1 scan is reading continuation bytes as controls, which makes it red on every benign frame", v)
		}
	})
	// Not in the table above, because whether a CSI is truncated depends on
	// what comes AFTER it: `\x1b[38;2;1` followed by ordinary text is a
	// perfectly well-formed CSI, since a space is a legal intermediate byte and
	// any letter is a legal final one. Only a frame that ENDS mid-sequence is
	// truncated -- which is what a row splitter that cut on cells rather than
	// on sequences leaves behind.
	t.Run("a CSI the frame ends in the middle of", func(t *testing.T) {
		v := FrameViolations("\x1b[1mrow\x1b[m\n\x1b[38;2;1")
		if len(v) != 1 || v[0].Kind != ViolationC0 || v[0].Byte != esc {
			t.Fatalf("FrameViolations = %v, want one stray-ESC violation -- a sequence with no final byte is an ESC that opens no CSI", v)
		}
		if v[0].Row != 1 {
			t.Fatalf("the violation is on row %d, want 1", v[0].Row)
		}
	})
}

// TestABenignDocumentFrameHoldsNoControlSequence is the same invariant over
// the document view: a committed fidelity fixture, which carries every
// construct a real corpus nests inside another one at every depth it
// reaches, driven through the real RenderDoc.
//
// A COMMITTED FIXTURE AND NOT THE CORPUS, which is this package's own standing
// rule (see corpus_projection_dogfood_test.go): ~/plans is one machine's
// directory and an assertion against it fails on every other. The corpus sweep
// is what says this fixture still resembles the documents it stands in for,
// and a sweep of a real corpus drove the same invariant over every file of it
// -- zero violations.
func TestABenignDocumentFrameHoldsNoControlSequence(t *testing.T) {
	st := darkStyles(t)
	for _, tc := range []struct {
		name, src string
	}{
		{"the fidelity fixture -- every construct at every depth", string(loadFidelity(t))},
		{"the totality document -- every node kind goldmark emits", totalityDoc},
	} {
		for _, width := range []int{80, 160} {
			t.Run(fmt.Sprintf("%s/width %d", tc.name, width), func(t *testing.T) {
				frame := docFrame(t, tc.src, width, st)
				// The census, and the rate is its durable half: a CSI count
				// is a function of the document and the row count, so a
				// figure quoted without its fixture is one nobody can
				// reproduce.
				rows, csi := strings.Count(frame, "\n"), FrameCSICount(frame)
				t.Logf("ui.RenderDoc at width %d: %d rows, %d bytes, %d CSI, %d per row", width, rows, len(frame), csi, csi/rows)
				requireCleanFrame(t, "the document frame", frame)
			})
		}
	}
}

// framePayload is one thing a document or a comment can put in a string this
// renderer draws.
type framePayload struct{ name, payload string }

// framePayloads is DELIBERATELY WIDER THAN THE ARM TESTS' OWN BYTE TABLES. The
// arm tests carry \b, \r and \x7f, which is the headline -- the forgeries that
// need no escape sequence at all. These add the five string-terminated
// introducers, which are a different hazard behind the same predicate: each
// swallows every byte after it until a terminator the PAYLOAD chooses, so an
// unterminated one eats the rest of the screen. They are covered because ESC is
// a C0 byte and the control-byte substitution visualises it, and nothing
// before this asserted that.
var framePayloads = []framePayload{
	{"the backspace forgery", "Requires approval\b\b\b\b\b\b\b\b\b\b\b\b\b\b\b\b\bNo approval needed"},
	{"the carriage-return forgery", "We will NOT rotate the keys.\rWe will rotate them"},
	{"DEL", "before\x7fafter"},
	{"an OSC 8 hyperlink", "before\x1b]8;;https://evil.example\x07label\x1b]8;;\x07after"},
	{"an unterminated APC", "before\x1b_payload that never endsafter"},
	{"a DCS", "before\x1bPq#0;2;0;0;0\x1b\\after"},
	{"an SOS, the sibling the invariant does not name", "before\x1bXpayload\x1b\\after"},
	{"a PM, the other one", "before\x1b^payload\x1b\\after"},
}

// TestAHostileDocumentFrameHoldsNoControlSequence is the same invariant over
// documents that CARRY the bytes, which is the assertion actually worth having:
// a benign document proves the renderer does not invent an escape, and only a
// hostile one proves the filter takes one away.
//
// EVERY ARM AND EVERY PAYLOAD, because the arms are covered by three different
// placements -- four by the filter at the inline leaf, two at the arm, one
// inside displayIn -- and an invariant driven on paragraphs alone would ship
// green over exactly the arms nothing covers by accident.
//
// THE LINK DESTINATION IS A ROW OF ITS OWN. renderInlines' *ast.Link arm
// recurses into the link's CHILDREN and never reads node.Destination, so no leaf
// filter has ever seen a destination on any arm -- and it still reaches a
// ui.Line on the three arms that draw Block.Text. It is covered by the
// arm-level filter rather than by the leaf, so a change that moved the raw
// arms' filter to the leaf would pass every other case here and fail this one.
func TestAHostileDocumentFrameHoldsNoControlSequence(t *testing.T) {
	st := darkStyles(t)
	payloads := append(append([]framePayload{}, framePayloads...),
		// Two the thread-card channels have no equivalent of. The first is
		// the finding that motivated this test's existence; the second is the
		// grid tests' own byte set, so the two populations cannot drift.
		framePayload{"an inline link whose DESTINATION carries the byte", "a [label](https://example.com/a\bb) here"},
		framePayload{"every byte the grid tests name, in one string", "a" + strings.Join(gridControlBytes, "b") + "c"},
	)
	for _, arm := range controlArms {
		for _, p := range payloads {
			t.Run(arm.name+"/"+p.name, func(t *testing.T) {
				src := fmt.Sprintf(arm.src, p.payload)
				// The fixture guard: the payload has to be IN the document, or
				// a clean frame says nothing about the filter.
				if !strings.Contains(src, p.payload) {
					t.Fatalf("the fixture lost its own payload: %q", src)
				}
				requireCleanFrame(t, "the frame for "+arm.name, docFrame(t, src, 100, st))
			})
		}
	}
}

// TestFrameViolationsFindsAPayloadPlantedInARealFrame is the non-vacuity proof,
// and it is deliberately a SPLICE rather than a document.
//
// An invariant that has never failed is not known to hold, and the assertions
// above cannot fail by design -- every channel they drive is filtered. So the
// payload is planted directly into a real rendered frame, mid-row, surrounded
// by hundreds of Draftplane's own CSI sequences: that is the condition under
// which a scan can plausibly go wrong, by losing the payload inside our SGR or
// by reading our SGR as a payload.
//
// IT IS PLANTED AT A ROW BOUNDARY and not at an arbitrary byte, because an
// arbitrary offset in a styled frame lands inside one of our own CSI sequences
// and cuts it in half -- so the test would report a stray ESC before ever
// reaching the thing it planted.
//
// IT IS A SPLICE AND NOT AN OPEN CHANNEL on purpose. Planting the payload in a
// surface that still passes raw bytes would make this a landmine: it would go
// red the day that surface is fixed, and a non-vacuity proof that fails when the
// code improves is worse than none.
func TestFrameViolationsFindsAPayloadPlantedInARealFrame(t *testing.T) {
	st := darkStyles(t)
	clean := docFrame(t, string(loadFidelity(t)), 80, st)
	requireCleanFrame(t, "the frame before anything is planted in it", clean)
	csi := FrameCSICount(clean)

	for _, c := range frameKindCases {
		t.Run(c.name, func(t *testing.T) {
			at := strings.Index(clean[len(clean)/2:], "\n") + len(clean)/2 + 1
			planted := clean[:at] + c.payload + clean[at:]
			v := FrameViolations(planted)
			if len(v) == 0 {
				t.Fatalf("nothing was reported for %q planted at byte %d of a real %d-byte frame -- the invariant cannot fail, so it is not known to hold", c.payload, at, len(clean))
			}
			if v[0].Kind != c.kind || v[0].Byte != c.b {
				t.Fatalf("first violation is %v/0x%02x, want %v/0x%02x: %v", v[0].Kind, v[0].Byte, c.kind, c.b, v)
			}
			if got := FrameCSICount(planted); got < csi {
				t.Fatalf("planting the payload lost %d of the frame's %d CSI sequences -- the scan is being knocked out of step rather than stepping over it", csi-got, csi)
			}
		})
	}
}

// TestAHostileThreadCardFrameHoldsNoControlSequence is the invariant over the
// channel whose bytes are STORED REVIEW FACTS rather than document text, which
// is the one this whole file is least able to argue about from the
// document side.
//
// A comment body, an attribution's login, its agent half and the machine
// pseudonym have no accidental cover at all: nothing parses them, nothing
// projects them, and no inline leaf ever splits them the way renderInlines
// splits a paragraph. Both frames are driven because ThreadCardLines
// re-truncates every row to the relocate panel's own budget, which is a second
// place a payload can be cut mid-sequence.
func TestAHostileThreadCardFrameHoldsNoControlSequence(t *testing.T) {
	st := darkStyles(t)
	for _, p := range framePayloads {
		for _, ch := range threadCardChannels {
			for _, fr := range threadCardFrames {
				t.Run(p.name+"/"+ch.name+"/"+fr.name, func(t *testing.T) {
					v := ch.view(p.payload)
					if raw := ch.raw(v); raw != "" && !strings.Contains(raw, p.payload) {
						t.Fatalf("the stored value is %q and does not carry the payload -- this fixture is not driving the channel it names", raw)
					}
					requireCleanFrame(t, "the thread card in "+fr.name, strings.Join(fr.draw(t, v, ch.pseudonym(p.payload), 100, st), "\n"))
				})
			}
		}
	}
}

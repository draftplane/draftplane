package ui

import (
	"fmt"
	"strconv"
	"unicode/utf8"
)

// This file is the invariant, made executable:
//
//	A benign frame carries zero OSC, DCS, APC (and their SOS and PM
//	siblings) IN EITHER THE SEVEN-BIT OR THE EIGHT-BIT SPELLING, no other
//	C1 control in either spelling, and no C0 byte or DEL other than the row
//	separator and the ESC that introduces one of Draftplane's own CSI
//	sequences.
//
// Every channel that reaches a terminal carries its own control-byte filter;
// this says what the result has to look like, over a whole frame rather than a
// field -- so a channel nobody enumerated fails here even though no test names
// it.
//
// BOTH QUALIFIERS ARE LOAD-BEARING. Without "other than the row separator" the
// assertion is false on every frame ever drawn, since a frame is rows joined
// with '\n'. And ESC is itself a C0 byte, of which a benign frame carries
// thousands, so the C0 clause has to exempt the ESC that opens a well-formed
// CSI or the invariant is vacuously red.
//
// CSI ITSELF CANNOT BE ASSERTED THIS WAY AND THAT IS THE TRAP: NOTHING
// DISTINGUISHES ONE OF OURS FROM A SOURCE-SUPPLIED ONE, so any invariant that
// counted them would be either vacuous or permanently red. That is why
// FrameCSICount is a CENSUS and a non-vacuity guard and not a limit. What
// protects the frame from a document's CSI is the control-byte substitution:
// the ESC that opens it is a C0 byte, so it is visualised at the leaf or at the
// arm.
//
// OSC, DCS AND APC ARE ASSERTABLE BECAUSE DRAFTPLANE EMITS ZERO OF EACH. SOS
// and PM are named alongside them because they are byte-for-byte the same
// hazard -- an introducer that swallows every byte after it until a string
// terminator the payload controls -- and an invariant that named three of the
// five would be one a hostile document could route around.
//
// AND EVERY INTRODUCER ABOVE HAS AN EIGHT-BIT SPELLING -- 0x90 DCS, 0x98 SOS,
// 0x9B CSI, 0x9D OSC, 0x9E PM, 0x9F APC -- and a scan that knew only the
// ESC-prefixed one reported a frame carrying a raw 0x9F CLEAN. The eight-bit
// spelling IS assertable for exactly the reason the seven-bit one is not:
// lipgloss writes `ESC [`, always, so an 8-bit CSI is in the same position as
// an OSC. BOTH SPELLINGS OF A C1 ARE REPORTED, AND THE SECOND IS THE ONE THAT
// TRAVELS: a raw 0x9B is invalid UTF-8 and arrives only in a file; the two-byte
// UTF-8 encoding of U+009B survives a JSON round trip and so also reaches this
// frame through an agent's save.
//
// THE SCAN IS RUNE-AWARE FROM HERE DOWN, and it has to be: 0x80-0x9F is also
// the UTF-8 CONTINUATION-BYTE range, so a byte test would report `␍` (E2 90 8D)
// as a DCS and `–` (E2 80 93) as a C1, and the invariant would be permanently
// red on every benign frame this package draws.
//
// AND IT REPORTS RATHER THAN REPAIRS: isVisibleControl stays a byte scan over
// C0 and DEL by ruling, so a C1 byte still reaches the terminal unvisualised.
// This function is what makes that residual OBSERVABLE.

// esc is the byte every escape sequence opens with, and is itself C0.
const esc = 0x1b

// FrameViolationKind names what was found. It leads every failure message,
// because a frame assertion that failed on a COUNT would say the frame moved
// and not what moved it -- the distinction this package's whole
// mutation-testing discipline rests on.
type FrameViolationKind string

const (
	// ViolationC0 is a C0 byte or DEL that is neither the row separator nor a
	// CSI introducer. It is the most consequential kind this file catches:
	// \r and \b need no escape sequence at all, every emulator honours them,
	// and what they falsify is the bytes a reviewer is agreeing to.
	ViolationC0 FrameViolationKind = "C0"
	// The four string-terminated introducers plus SOS. Each swallows every
	// byte after it until a terminator the PAYLOAD chooses, so an unterminated
	// one eats the rest of the frame.
	ViolationOSC FrameViolationKind = "OSC"
	ViolationDCS FrameViolationKind = "DCS"
	ViolationAPC FrameViolationKind = "APC"
	ViolationSOS FrameViolationKind = "SOS"
	ViolationPM  FrameViolationKind = "PM"
	// ViolationC1 is a control in U+0080-U+009F: the 8-bit spelling of the
	// five above and of CSI, plus the rest of the C1 set. See the ⚠️ at the
	// top of this file for why both of its spellings are reported and why the
	// 8-bit CSI is assertable where the 7-bit one is not.
	ViolationC1 FrameViolationKind = "C1"
)

// FrameViolation is one byte or one sequence a frame must not carry.
//
// IT NAMES THE BYTE AND NOT A TALLY, which is the whole shape of it. A caller
// asserting `len(FrameViolations(frame)) == 0` and printing what came back
// fails with the offending byte, the row it is on and the text either side of
// it; a caller comparing counts fails with a number.
type FrameViolation struct {
	Kind FrameViolationKind
	// Byte identifies the violation: the offending byte itself for
	// ViolationC0, the C1 control's own code point (0x80-0x9F, which fits a
	// byte in both of its spellings) for ViolationC1, and the introducer's
	// second byte (']', 'P', '_', 'X', '^') for the escape kinds.
	Byte byte
	// Offset is the byte offset in the frame, and Row the number of row
	// separators before it. Row and not a line and column pair, because a
	// frame's rows carry SGR and a column in bytes is not a column on screen.
	Offset, Row int
	// Sample is the frame either side of Offset, quoted -- so a control byte
	// appears as an escape in the message rather than acting on the terminal
	// the failure is being read in.
	Sample string
}

func (v FrameViolation) String() string {
	if v.Kind == ViolationC1 {
		where := fmt.Sprintf("C1 control 0x%02x", v.Byte)
		if name := c1Name(v.Byte); name != "" {
			where += " (" + name + ")"
		}
		return fmt.Sprintf("%s at row %d, byte %d: %s", where, v.Row, v.Offset, v.Sample)
	}
	if v.Kind != ViolationC0 {
		return fmt.Sprintf("%s introducer (ESC %c) at row %d, byte %d: %s", v.Kind, v.Byte, v.Row, v.Offset, v.Sample)
	}
	where := fmt.Sprintf("C0 byte 0x%02x (%c) at row %d, byte %d", v.Byte, controlPicture(v.Byte), v.Row, v.Offset)
	if v.Byte == esc {
		// The one C0 byte the invariant exempts, when it is not doing the
		// thing it is exempted for.
		where += ", opening no CSI"
	}
	return where + ": " + v.Sample
}

// FrameViolations is the invariant: everything in frame that a benign one must
// not carry, in the order it appears, and nil for a frame that carries none.
//
// A FRAME AND NOT A FIELD, on purpose. A hostile-document harness drives
// crafted documents through both views and asserts this over what comes out;
// the benign assertions in ui and app drive committed fixtures through the
// same two views and assert the same thing. Neither inlines its own scan, so
// there is one definition of what a frame may contain and one place to widen
// it.
//
// ONE VIOLATION PER INTRODUCER, AND THE PAYLOAD AFTER IT IS STILL SCANNED. An
// OSC is reported at its introducer and the scan then continues through what
// follows as ordinary text rather than skipping to a string terminator -- so
// the bytes hidden inside it are named too, and a payload that never terminates
// cannot swallow the rest of the report the way it would swallow the rest of
// the screen.
func FrameViolations(frame string) []FrameViolation {
	var out []FrameViolation
	scanFrame(frame, func(v FrameViolation) { out = append(out, v) })
	return out
}

// FrameCSICount is the census half, and it is a NON-VACUITY GUARD rather than a
// limit. Draftplane's own SGR is indistinguishable from a document's, so
// nothing here can be asserted as a maximum; what it is good for is the
// opposite question -- a frame asserted to hold no OSC and no stray C0 which
// also holds no CSI at all is probably an empty string or an unstyled one, and
// the assertion over it proved nothing.
func FrameCSICount(frame string) int { return scanFrame(frame, nil) }

// scanFrame walks frame once, reporting every violation to report (which may
// be nil) and returning the number of well-formed CSI sequences it stepped
// over.
func scanFrame(frame string, report func(FrameViolation)) int {
	csi, row := 0, 0
	violate := func(kind FrameViolationKind, b byte, at int) {
		if report == nil {
			return
		}
		report(FrameViolation{Kind: kind, Byte: b, Offset: at, Row: row, Sample: frameSample(frame, at)})
	}
	for i := 0; i < len(frame); {
		b := frame[i]
		switch {
		case b == esc:
			if end := csiEnd(frame, i); end > 0 {
				csi++
				i = end
				continue
			}
			if i+1 < len(frame) {
				if kind, ok := stringEscapeKind(frame[i+1]); ok {
					violate(kind, frame[i+1], i)
					i += 2
					continue
				}
			}
			// An ESC that opens neither a well-formed CSI nor a known string
			// escape is just a C0 byte, and the qualifier exempts only the one
			// that opens a CSI.
			violate(ViolationC0, esc, i)
			i++
		case b == '\n':
			row++
			i++
		// isVisibleControl is reused here rather than respelt, and TAB is
		// added back because that predicate excludes it there for a reason
		// that does not apply here: a tab is spent as four spaces before it can
		// reach a row, and one that arrived anyway would be the two-authority
		// width disagreement (ansi.StringWidth 0 cells, lipgloss 4) landing in
		// a finished frame.
		case b == '\t' || isVisibleControl(b):
			violate(ViolationC0, b, i)
			i++
		case b < utf8.RuneSelf:
			i++
		// The C1 arm, and it decodes rather than testing the byte. A raw byte
		// in 0x80-0x9F is invalid UTF-8 and stands alone; a UTF-8-encoded one
		// decodes to the same code point out of two bytes; and every ordinary
		// non-ASCII rune on the frame runs through the same decode and is
		// stepped over whole.
		default:
			r, size := utf8.DecodeRuneInString(frame[i:])
			switch {
			case r == utf8.RuneError && size == 1 && b <= 0x9f:
				violate(ViolationC1, b, i)
				i++
			case r >= 0x80 && r <= 0x9f:
				violate(ViolationC1, byte(r), i)
				i += size
			default:
				i += size
			}
		}
	}
	return csi
}

// csiEnd is the offset just past the CSI sequence opening at i, or -1 when
// what stands there is not a well-formed one -- including a truncated one,
// which is what a naive row splitter makes of a frame it cut mid-sequence and
// is a thing worth failing on rather than counting.
func csiEnd(frame string, i int) int {
	if i+1 >= len(frame) || frame[i+1] != '[' {
		return -1
	}
	j := i + 2
	for j < len(frame) && frame[j] >= 0x30 && frame[j] <= 0x3f {
		j++
	}
	for j < len(frame) && frame[j] >= 0x20 && frame[j] <= 0x2f {
		j++
	}
	if j >= len(frame) || frame[j] < 0x40 || frame[j] > 0x7e {
		return -1
	}
	return j + 1
}

// c1Name names the C1 controls worth naming in a failure message: the six
// introducers, which are the hazard, and the string terminator that ends the
// five that take a payload. The other 25 print as a bare hex byte, which is all
// there is to say about them.
func c1Name(b byte) string {
	switch b {
	case 0x90:
		return "DCS"
	case 0x98:
		return "SOS"
	case 0x9b:
		return "CSI"
	case 0x9c:
		return "ST"
	case 0x9d:
		return "OSC"
	case 0x9e:
		return "PM"
	case 0x9f:
		return "APC"
	}
	return ""
}

// stringEscapeKind maps an introducer's second byte to its kind.
func stringEscapeKind(b byte) (FrameViolationKind, bool) {
	switch b {
	case ']':
		return ViolationOSC, true
	case 'P':
		return ViolationDCS, true
	case '_':
		return ViolationAPC, true
	case 'X':
		return ViolationSOS, true
	case '^':
		return ViolationPM, true
	}
	return "", false
}

// frameSampleSpan is how much of the frame either side of a violation goes
// into its message: enough to recognise the row, short enough that a failure
// listing several does not bury the first.
const frameSampleSpan = 32

// frameSample is the frame around at, quoted. strconv.Quote and not %q on a
// slice, because the slice can begin or end mid-rune and Quote renders those
// bytes as escapes rather than as replacement characters.
func frameSample(frame string, at int) string {
	lo, hi := at-frameSampleSpan, at+frameSampleSpan
	if lo < 0 {
		lo = 0
	}
	if hi > len(frame) {
		hi = len(frame)
	}
	return strconv.Quote(frame[lo:hi])
}

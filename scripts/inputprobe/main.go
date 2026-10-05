// Command inputprobe shows exactly what a terminal (and any multiplexer
// between it and this process) delivers on stdin, in the one condition
// draftplane runs in: raw mode, alternate screen.
//
// It exists to settle three questions that were being reasoned about rather
// than measured:
//
//  1. With mouse reporting OFF, what does a trackpad scroll arrive as?
//     The expectation is synthesised arrow keys -- tmux calls this
//     alternate-scroll and Zellij does the same -- which is why draftplane's
//     block cursor moves when you scroll.
//  2. With mouse reporting ON, do SGR mouse events arrive at all, and are the
//     coordinates accurate through the multiplexer?
//  3. Does ?1000 (press/release/wheel) behave differently from ?1002
//     (which adds drag)? If the multiplexer keeps motion for itself under
//     ?1000, we could take click and wheel without taking drag.
//
// Every line carries a timestamp and a sequence number, so the DENSITY of a
// momentum flick is readable directly rather than inferred.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	altOn  = "\x1b[?1049h"
	altOff = "\x1b[?1049l"
	sgrOn  = "\x1b[?1006h"
	sgrOff = "\x1b[?1006l"
)

// The three tracking modes, in the order they widen. Only one is ever set.
var tracking = []struct {
	name string
	desc string
	on   string
	off  string
}{
	{"off", "no mouse reporting -- the terminal/multiplexer keeps the mouse", "", ""},
	{"?1000 normal", "press, release, WHEEL -- no drag", "\x1b[?1000h", "\x1b[?1000l"},
	{"?1002 button-event", "press, release, wheel, DRAG  <- what bubbletea's MouseModeCellMotion sets", "\x1b[?1002h", "\x1b[?1002l"},
	{"?1003 any-event", "the above plus bare motion with no button held", "\x1b[?1003h", "\x1b[?1003l"},
}

var (
	out     = os.Stdout
	mode    = 0
	started = time.Now()
	seq     int
	tally   = map[string]int{}
	logFile *os.File
	logPath string

	// ruler mode: the one question the streaming view cannot answer is
	// whether a reported row is PANE-relative or carries a constant offset
	// from the multiplexer's own chrome. A labelled row answers it by
	// putting the probe's answer next to the label it should match.
	ruler        bool
	lastX, lastY int
	lastPress    bool

	// The ruler alone cannot prove an offset: the probe writes its answer at
	// the row it RECEIVED, so the answer always agrees with the label it
	// lands beside. The targeted test removes the judgement call by naming
	// the row to click BEFORE the click, then comparing.
	testActive  bool
	testTargets []int
	testIdx     int
	testResults []string
)

func main() {
	if err := stty("raw", "-echo"); err != nil {
		fmt.Fprintf(os.Stderr, "could not put the terminal in raw mode: %v\n", err)
		fmt.Fprintln(os.Stderr, "run this from a real terminal (it needs /dev/tty)")
		os.Exit(1)
	}
	cleanup := func() {
		setMode(0)
		fmt.Fprint(out, altOff)
		_ = stty("sane")
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sig
		cleanup()
		os.Exit(1)
	}()

	if f, err := os.Create("probe-transcript.txt"); err == nil {
		logFile = f
		if abs, err := os.Getwd(); err == nil {
			logPath = abs + "/probe-transcript.txt"
		}
		defer f.Close()
	}

	fmt.Fprint(out, altOn)
	header()
	setMode(0)

	buf := make([]byte, 0, 256)
	chunk := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(chunk)
		if n > 0 {
			at := time.Since(started)
			buf = append(buf, chunk[:n]...)
			// How many bytes arrived in ONE read matters: a momentum flick
			// usually delivers many events in a single burst, and that is
			// half the "too far, too fast" answer.
			if n > 8 && !ruler {
				line("  \x1b[2m-- one read delivered %d bytes --\x1b[0m", n)
			}
			for {
				used, desc, quit := parseOne(buf)
				if used == 0 {
					break
				}
				raw := escape(string(buf[:used]))
				buf = buf[used:]
				if quit {
					cleanup()
					summary()
					return
				}
				if desc == "" {
					continue // a mode key; already reported
				}
				seq++
				if ruler {
					if lastPress {
						if testActive {
							recordTest(lastY)
						} else {
							annotate(lastX, lastY)
						}
					}
					continue
				}
				line("[%6dms] #%-4d %-28s %s", at.Milliseconds(), seq, raw, desc)
			}
		}
		if err != nil {
			cleanup()
			return
		}
	}
}

// parseOne consumes one complete keystroke or mouse event from the front of
// buf. It returns 0 when what is there is a prefix of something longer, so a
// sequence split across two reads is reassembled rather than mis-decoded.
//
// The one case it cannot resolve is a LONE escape keypress, which is
// indistinguishable from the start of a sequence without a timer. It waits,
// so pressing esc shows nothing until the next key arrives. Deliberate: a
// timer here would make the probe's own answer depend on timing, which is the
// thing it exists to measure.
func parseOne(buf []byte) (used int, desc string, quit bool) {
	if len(buf) == 0 {
		return 0, "", false
	}
	if buf[0] != 0x1b {
		b := buf[0]
		switch {
		case b == 'q':
			return 1, "", true
		case b >= '1' && b <= '4':
			setMode(int(b - '1'))
			return 1, "", false
		case b == 'r':
			toggleRuler()
			return 1, "", false
		case b == 't':
			startTest()
			return 1, "", false
		case b == 'c':
			ruler = false
			fmt.Fprint(out, "\x1b[2J\x1b[H")
			header()
			return 1, "", false
		case b == ' ':
			line("")
			return 1, "", false
		}
		return 1, count(describeByte(b)), false
	}
	if len(buf) == 1 {
		return 0, "", false // could be esc, could be a prefix
	}
	switch buf[1] {
	case 'O': // SS3 -- arrows under DECCKM (application cursor keys)
		if len(buf) < 3 {
			return 0, "", false
		}
		if a := arrow(buf[2]); a != "" {
			return 3, count(a + "  \x1b[2m(SS3 -- application cursor keys)\x1b[0m"), false
		}
		return 3, count("SS3 " + string(buf[2])), false
	case '[': // CSI
		i := 2
		for i < len(buf) && buf[i] >= 0x20 && buf[i] <= 0x3f {
			i++
		}
		if i >= len(buf) {
			return 0, "", false
		}
		final := buf[i]
		params := string(buf[2:i])
		if strings.HasPrefix(params, "<") && (final == 'M' || final == 'm') {
			return i + 1, count(decodeSGR(params[1:], final)), false
		}
		if a := arrow(final); a != "" && params == "" {
			return i + 1, count(a + "  \x1b[2m(CSI -- normal cursor keys)\x1b[0m"), false
		}
		return i + 1, count(fmt.Sprintf("CSI %s%c", params, final)), false
	}
	return 2, count("ESC " + describeByte(buf[1])), false
}

func decodeSGR(params string, final byte) string {
	f := strings.Split(params, ";")
	if len(f) != 3 {
		return "SGR mouse (unparsed: " + params + ")"
	}
	b, _ := strconv.Atoi(f[0])
	x, _ := strconv.Atoi(f[1])
	y, _ := strconv.Atoi(f[2])
	lastX, lastY = x, y
	lastPress = b&64 == 0 && b&32 == 0 && final == 'M'

	var what string
	switch {
	case b&64 != 0:
		switch b & 3 {
		case 0:
			what = "WHEEL UP"
		case 1:
			what = "WHEEL DOWN"
		case 2:
			what = "wheel left"
		case 3:
			what = "wheel right"
		}
	case b&32 != 0:
		what = "MOTION/DRAG"
		if b&3 != 3 {
			what += " with " + button(b&3)
		}
	case final == 'm':
		what = "release"
	default:
		what = strings.ToUpper(button(b&3)) + " press"
	}

	var mods []string
	if b&4 != 0 {
		mods = append(mods, "shift")
	}
	if b&8 != 0 {
		mods = append(mods, "alt")
	}
	if b&16 != 0 {
		mods = append(mods, "ctrl")
	}
	m := ""
	if len(mods) > 0 {
		m = " +" + strings.Join(mods, "+")
	}
	// col/row are 1-based in the protocol; draftplane's hit test wants row.
	return fmt.Sprintf("%-16s col %-3d row %-3d%s", what, x, y, m)
}

func button(n int) string {
	switch n {
	case 0:
		return "left"
	case 1:
		return "middle"
	case 2:
		return "right"
	}
	return "none"
}

func arrow(final byte) string {
	switch final {
	case 'A':
		return "Up arrow"
	case 'B':
		return "DOWN ARROW"
	case 'C':
		return "Right arrow"
	case 'D':
		return "Left arrow"
	}
	return ""
}

func describeByte(b byte) string {
	switch {
	case b == 0x0d:
		return "CR (enter)"
	case b == 0x09:
		return "TAB"
	case b == 0x7f:
		return "DEL (backspace)"
	case b < 0x20:
		return fmt.Sprintf("ctrl+%c", b+'a'-1)
	case b < 0x7f:
		return fmt.Sprintf("key %q", string(rune(b)))
	}
	return fmt.Sprintf("byte 0x%02x", b)
}

func count(desc string) string {
	key := desc
	if i := strings.Index(key, "  \x1b["); i >= 0 {
		key = key[:i]
	}
	if i := strings.Index(key, " col "); i >= 0 {
		key = key[:i]
	}
	tally[strings.TrimSpace(key)]++
	return desc
}

func setMode(i int) {
	// Reset every tracking mode before setting one, so switching cannot
	// leave two enabled at once.
	fmt.Fprint(out, "\x1b[?1000l\x1b[?1002l\x1b[?1003l"+sgrOff)
	mode = i
	t := tracking[i]
	if t.on != "" {
		fmt.Fprint(out, t.on+sgrOn)
	}
	line("")
	line("\x1b[7m MODE %d: %s \x1b[0m  %s", i+1, t.name, t.desc)
	line("")
}

// toggleRuler paints one labelled line per screen row. A left click then
// writes the row the probe RECEIVED beside the row the screen SHOWS, so an
// offset is visible rather than inferred: the two numbers either sit on the
// same line or they do not.
func toggleRuler() {
	ruler = !ruler
	if !ruler {
		testActive = false
		fmt.Fprint(out, "\x1b[2J\x1b[H")
		header()
		return
	}
	paintRuler()
}

func paintRuler() {
	rows, cols := screenSize()
	fmt.Fprint(out, "\x1b[2J")
	for i := 1; i <= rows; i++ {
		bar := strings.Repeat("\u2500", max(0, min(18, cols-24)))
		fmt.Fprintf(out, "\x1b[%d;1Hrow %-4d %s", i, i, bar)
	}
	fmt.Fprintf(out, "\x1b[1;1H\x1b[7m RULER \x1b[0m click any row -- the answer lands beside its own label")
	if mode == 0 {
		fmt.Fprintf(out, "\x1b[2;1H\x1b[7m mouse is OFF -- press 2 or 3 first \x1b[0m")
	}
	fmt.Fprintf(out, "\x1b[%d;1H", rows)
}

// startTest names the row to click BEFORE the click, so the comparison is the
// probe's rather than the reader's. Four targets spread top to bottom, because
// a constant offset and a scaling error look identical at one point.
func startTest() {
	rows, _ := screenSize()
	testTargets = []int{1, rows / 3, (2 * rows) / 3, rows}
	testIdx, testResults, testActive, ruler = 0, nil, true, true
	paintRuler()
	promptTest()
}

func promptTest() {
	rows, _ := screenSize()
	if testIdx >= len(testTargets) {
		finishTest()
		return
	}
	fmt.Fprintf(out, "\x1b[1;30H\x1b[K\x1b[7m  CLICK THE ROW LABELLED   %d   (%d of %d)  \x1b[0m",
		testTargets[testIdx], testIdx+1, len(testTargets))
	fmt.Fprintf(out, "\x1b[%d;1H", rows)
}

func recordTest(y int) {
	asked := testTargets[testIdx]
	verdict := "EXACT"
	if d := y - asked; d != 0 {
		verdict = fmt.Sprintf("OFFSET %+d", d)
	}
	r := fmt.Sprintf("asked row %-4d received row %-4d -> %s", asked, y, verdict)
	testResults = append(testResults, r)
	if logFile != nil {
		fmt.Fprintf(logFile, "TEST: %s\n", r)
	}
	testIdx++
	promptTest()
}

func finishTest() {
	testActive, ruler = false, false
	fmt.Fprint(out, "\x1b[2J\x1b[H")
	header()
	line("\x1b[7m TARGETED HIT TEST -- mode %d \x1b[0m", mode+1)
	exact := 0
	for _, r := range testResults {
		line("  %s", r)
		if strings.HasSuffix(r, "EXACT") {
			exact++
		}
	}
	if exact == len(testResults) {
		line("  \x1b[1mall %d exact -- coordinates are pane-relative and need no correction\x1b[0m", exact)
	} else {
		line("  \x1b[1m%d of %d exact -- read the offsets above\x1b[0m", exact, len(testResults))
	}
	line("")
}

func annotate(x, y int) {
	rows, _ := screenSize()
	if y < 1 || y > rows {
		return
	}
	fmt.Fprintf(out, "\x1b[%d;26H\x1b[K\x1b[7m received row %d, col %d \x1b[0m", y, y, x)
	fmt.Fprintf(out, "\x1b[%d;1H", rows)
	if logFile != nil {
		fmt.Fprintf(logFile, "RULER: click reported row %d col %d\n", y, x)
	}
}

func screenSize() (rows, cols int) {
	rows, cols = 24, 80
	outBytes, err := exec.Command("stty", "-f", "/dev/tty", "size").Output()
	if err == nil {
		f := strings.Fields(string(outBytes))
		if len(f) == 2 {
			if r, err := strconv.Atoi(f[0]); err == nil {
				rows = r
			}
			if c, err := strconv.Atoi(f[1]); err == nil {
				cols = c
			}
		}
	}
	return rows, cols
}

func header() {
	line("\x1b[1mdraftplane input probe\x1b[0m -- raw mode, alternate screen (what draftplane runs in)")
	line("")
	line("  \x1b[1m1\x1b[0m mouse off      \x1b[1m2\x1b[0m ?1000 normal      \x1b[1m3\x1b[0m ?1002 button-event      \x1b[1m4\x1b[0m ?1003 any-event")
	line("  \x1b[1mt\x1b[0m TARGETED HIT TEST (do this one)   \x1b[1mr\x1b[0m ruler   \x1b[1mc\x1b[0m clear   \x1b[1mspace\x1b[0m blank   \x1b[1mq\x1b[0m quit  (ctrl+c will NOT quit)")
	line("")
	line("  \x1b[2mIf this ever exits badly, run:  stty sane; printf '\\033[?1049l'\x1b[0m")
	line("")
}

// summary prints AFTER the alternate screen has been torn down, on the normal
// screen, so the findings survive the run instead of scrolling away with it.
func summary() {
	fmt.Println()
	fmt.Println("-- summary --")
	keys := make([]string, 0, len(tally))
	for k := range tally {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return tally[keys[i]] > tally[keys[j]] })
	for _, k := range keys {
		fmt.Printf("  %5d  %s\n", tally[k], stripSGR(k))
	}
	fmt.Printf("\n  %d events over %.1fs\n", seq, time.Since(started).Seconds())
	if logPath != "" {
		fmt.Printf("  full transcript: %s\n", logPath)
	}
	fmt.Println()
}

func stripSGR(s string) string {
	for {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			return s
		}
		j := strings.IndexByte(s[i:], 'm')
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+1:]
	}
}

// line writes with an explicit CR: raw mode turns ONLCR off, so a bare "\n"
// would step down a row without returning to column 0.
func line(format string, a ...any) {
	fmt.Fprintf(out, format+"\r\n", a...)
	if logFile != nil {
		fmt.Fprintf(logFile, "%s\n", stripSGR(fmt.Sprintf(format, a...)))
	}
}

func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == 0x1b:
			b.WriteString("\\e")
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\x%02x", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func stty(args ...string) error {
	cmd := exec.Command("stty", append([]string{"-f", "/dev/tty"}, args...)...)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

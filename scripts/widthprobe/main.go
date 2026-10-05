// widthprobe asks the TERMINAL how many cells it paints a glyph in.
//
// WHY THIS EXISTS. Draftplane budgets every document row with
// ansi.StringWidth and pads it with lipgloss's Width. Both of those are
// OUR rulers, and a census taken with either can only ever surface rows
// where our code disagrees with ITSELF -- never rows where our code
// disagrees with the terminal. That second kind is exactly the one this
// probe exists to catch, and there is exactly one instrument that can
// measure it: the terminal, asked directly.
//
// HOW. Print the string at column 1, then send CPR (ESC[6n). The terminal
// answers ESC[<row>;<col>R with the cursor's 1-based column, so the cells
// it actually painted is col-1. Nothing here interprets a font, a table or
// a Unicode property; it reports where the cursor ended up.
//
// AND A SECOND QUESTION THE FIRST ONE CANNOT REACH:
// whether the terminal CLAIMS grapheme clustering (DEC mode 2027) and whether
// that claim survives being taken up. Bubbletea asks DECRQM for 2027 at
// startup and, on any answer but "not recognised", moves the renderer's cell
// buffer from ansi.WcWidth to ansi.GraphemeWidth. So the claim decides our
// RENDERER's ruler while the CPR column decides the TERMINAL's, and a terminal
// that answers the query without changing what it paints desynchronises the
// two -- which a width table can never show, because both sides of it are
// ours. Every glyph is therefore measured TWICE, once with 2027 reset and
// once with it set, and the mode is put back the way it was found.
//
// WHAT TO COMPARE IT AGAINST. Our own side, re-derived rather than
// hardcoded here so the two cannot drift:
//
//	go run - <<'EOF'
//	package main
//	import ("fmt"; "charm.land/lipgloss/v2"; "github.com/charmbracelet/x/ansi")
//	func main() { s := "⚠️"; fmt.Println(ansi.StringWidth(s), lipgloss.Width(s)) }
//	EOF
//
// Measured at ansi v0.11.7 / lipgloss v2.0.5, both answer 2 for
// U+26A0 U+FE0F and 1 for a bare U+26A0.
//
// It has no make target and no test calls it, on scripts/inputprobe's and
// scripts/glyph-raster.py's precedent: it exists so a figure has a method
// somebody can run.
//
// Run it in EVERY terminal that matters, because the answer is a property
// of the terminal and not of this program:
//
//	go run ./scripts/widthprobe/main.go          # bare Ghostty
//	go run ./scripts/widthprobe/main.go          # again, inside Zellij
//
// The escape traffic goes to /dev/tty and the REPORT goes to stdout, so
// redirecting stdout to a file still measures the terminal:
//
//	go run ./scripts/widthprobe/main.go > /tmp/widths.txt
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type probe struct {
	name string
	s    string
	note string
}

var probes = []probe{
	{"control 'ab'", "ab", "must answer 2, or the harness itself is wrong"},
	{"WARNING SIGN + VS16", "⚠️", "this probe's whole subject; we count 2"},
	{"WARNING SIGN bare", "⚠", "we count 1"},
	{"WARNING SIGN + VS15", "⚠︎", "explicit TEXT presentation; we count 1"},
	{"WHITE HEAVY CHECK", "✅", "no selector, we count 2"},
	{"RIGHTWARDS ARROW", "→", "we count 1"},
	{"SECTION SIGN", "§", "the heading sigil; we count 1"},
	{"MIDDLE DOT", "·", "the help bar separator; we count 1"},
	{"LEFT BLOCK U+258E", "▎", "the quote bar; we count 1"},
	{"HEAVY VERTICAL U+2503", "┃", "the cursor rail; we count 1"},
	{"ZWJ family", "\U0001f468‍\U0001f469‍\U0001f467", "we count 2; Zellij painted 6"},
	{"SKIN TONE +1F3FD", "\U0001f44d\U0001f3fd", "same class as the ZWJ family; we count 2"},
	{"FLAG (RI pair)", "\U0001f1fa\U0001f1f8", "no ZWJ and no selector; we count 2"},
	{"SOFT HYPHEN", "\u00ad", "found twice in real body text in the wild; both our rulers say 0"},
	{"KEYCAP 1", "1\ufe0f\u20e3", "U+FE0F is STRUCTURAL here, not a hint"},
	{"CJK", "漢", "we count 2"},

	// IS IT ONE GLYPH, OR IS IT THE SELECTOR? The four rows below are what
	// tell those apart, and they are two different classes.
	//
	// CLASS 1 -- a GENUINE emoji-presentation sequence: ansi=2, uniseg=2. 1,307
	// codepoints are in it (found by enumerating every base our rulers call one
	// cell that becomes two under U+FE0F). If Zellij answers 1 for U+23ED too,
	// its rule is "ignore VS16" and all 1,307 are broken there, not just the
	// warning sign.
	{"NEXT TRACK + VS16", "⏭️", "class 1, and a real corpus's OTHER VS16 cluster"},
	{"NEXT TRACK bare", "⏭", "we count 1"},

	// CLASS 2 -- ansi widens where uniseg does NOT: 10,961 codepoints,
	// including ordinary letters and the heading sigil. Here ansi is the
	// one that is wrong, so BOTH terminals should answer 1 and the row is
	// budgeted short in Ghostty as well. Nothing in a real corpus reaches this
	// class today; any document a reader opens can.
	{"LETTER A + VS16", "A️", "class 2: ansi says 2, uniseg says 1"},
	{"SECTION SIGN + VS16", "§️", "class 2: ansi says 2, uniseg says 1"},
}

var tty *os.File

func main() {
	if !isTTY() {
		fmt.Fprintln(os.Stderr, "widthprobe: no controlling terminal -- run this IN the terminal you want to measure")
		os.Exit(2)
	}
	// THE ESCAPE TRAFFIC GOES TO /dev/tty, NEVER TO STDOUT. A run whose
	// stdout is redirected to a file would otherwise send the CPR query to
	// the file, the terminal would never see it, and every row would time
	// out -- which is exactly what happened the first time this was driven
	// under screen. Keeping the report on stdout and the queries on the tty
	// is what makes `> widths.txt` measure the terminal rather than nothing.
	var ttyErr error
	tty, ttyErr = os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if ttyErr != nil {
		fmt.Fprintf(os.Stderr, "widthprobe: cannot open /dev/tty: %v\n", ttyErr)
		os.Exit(2)
	}
	defer tty.Close()

	restore, err := rawMode()
	if err != nil {
		fmt.Fprintf(os.Stderr, "widthprobe: could not put the terminal in raw mode: %v\n", err)
		os.Exit(2)
	}
	defer restore()

	in := startReader(bufio.NewReader(tty))

	// THE HANDSHAKE IS ASKED FIRST, AND IT IS THE HALF THE CPR COLUMNS CANNOT
	// SEE. Bubbletea sends DECRQM for mode 2027 at startup and, on ANY answer
	// except "not recognised", switches the renderer's cell buffer from
	// ansi.WcWidth to ansi.GraphemeWidth and SETS the mode. So this reply
	// decides which ruler the RENDERER measures with, while the two columns
	// below decide which one the TERMINAL paints with. Those are different
	// questions and this program is the only place they are asked together.
	modeBefore, modeErr := queryMode(in, 2027)

	type row struct {
		p      probe
		off    int
		on     int
		offErr error
		onErr  error
	}
	var rows []row
	for _, p := range probes {
		setMode(2027, false)
		off, offErr := paintedCells(in, p.s)
		setMode(2027, true)
		on, onErr := paintedCells(in, p.s)
		rows = append(rows, row{p, off, on, offErr, onErr})
	}
	// Put the mode back the way it was FOUND rather than the way this program
	// last left it. A probe that changes the terminal it measures has to undo
	// the change, and the DECRQM answer above is what says which way that is.
	setMode(2027, modeBefore == 1 || modeBefore == 3)

	// Leave the line clean before printing the report.
	fmt.Fprint(tty, "\r\x1b[2K")
	restore()

	fmt.Println()
	fmt.Println("widthprobe -- cells the TERMINAL painted, measured by CPR")
	fmt.Printf("TERM=%q TERM_PROGRAM=%q ZELLIJ=%q COLUMNS=%q\n\n",
		os.Getenv("TERM"), os.Getenv("TERM_PROGRAM"), os.Getenv("ZELLIJ"), os.Getenv("COLUMNS"))
	if modeErr != nil {
		fmt.Printf("DECRQM 2027 (grapheme clustering): NO REPLY -- %v\n", modeErr)
	} else {
		fmt.Printf("DECRQM 2027 (grapheme clustering): %d -- %s\n", modeBefore, modeMeaning(modeBefore))
	}
	fmt.Println()
	fmt.Printf("%-26s %-14s %8s %8s   %s\n", "glyph", "codepoints", "2027 off", "2027 on", "note")
	for _, r := range rows {
		fmt.Printf("%-26s %-14s %8s %8s   %s\n",
			r.p.name, codepoints(r.p.s), cellStr(r.off, r.offErr), cellStr(r.on, r.onErr), r.p.note)
		if r.offErr != nil {
			fmt.Printf("%-26s 2027 off: %s\n", "", r.offErr)
		}
		if r.onErr != nil {
			fmt.Printf("%-26s 2027 on:  %s\n", "", r.onErr)
		}
	}
	fmt.Println()
	fmt.Println("READ IT LIKE THIS. For U+26A0 U+FE0F:")
	fmt.Println("  2 -- the terminal agrees with ansi/lipgloss: this glyph is not short a")
	fmt.Println("       cell. A visible gap here would have another cause, and nobody has")
	fmt.Println("       named it yet. Do not blame the emoji for it.")
	fmt.Println("  1 -- the terminal paints one cell where we budget two, so every row")
	fmt.Println("       carrying the glyph is padded one cell SHORT and the rail's last")
	fmt.Println("       column is never drawn -- a real width defect, confirmed here.")
	fmt.Println()
	fmt.Println("The control row must read 2. If it does not, this program measured nothing.")
	fmt.Println()
	fmt.Println("AND THE FOUR CLASS ROWS ANSWER A DIFFERENT QUESTION -- is it one glyph or")
	fmt.Println("the selector? Compare NEXT TRACK + VS16 against WARNING SIGN + VS16:")
	fmt.Println("  same answer -- the terminal's rule is about VS16, not about a character,")
	fmt.Println("       and every one of the 1,307 emoji-presentation sequences behaves alike.")
	fmt.Println("  different  -- it is per-glyph, and the fix has to be enumerated rather")
	fmt.Println("       than stated as a rule about the selector.")
	fmt.Println("LETTER A + VS16 and SECTION SIGN + VS16 should read 1 in BOTH terminals:")
	fmt.Println("there ansi is the ruler that is wrong, and the row draws short in Ghostty too.")
	fmt.Println()
	fmt.Println("THE TWO COLUMNS AND THE DECRQM LINE ANSWER THE RENDERER's QUESTION, and they")
	fmt.Println("are read TOGETHER or not at all. The DECRQM line says which ruler the")
	fmt.Println("RENDERER will measure with; the columns say which one the TERMINAL paints")
	fmt.Println("with. Four readings, and only one of them is benign:")
	fmt.Println("  DECRQM 0 or 4, columns equal -- honest. The renderer stays on WcWidth")
	fmt.Println("       and the terminal never claimed otherwise. Any width defect here is")
	fmt.Println("       ours to budget for, not a broken promise.")
	fmt.Println("  DECRQM 1/2/3, 2027-on column matches grapheme clustering -- honest, and")
	fmt.Println("       the handshake did its job. This is what Ghostty should look like.")
	fmt.Println("  DECRQM 1/2/3, BOTH COLUMNS IDENTICAL AND NOT THE CLUSTERED ANSWER --")
	fmt.Println("       ⚠️ THE CAPABILITY LIE, and the whole reason this section exists. The")
	fmt.Println("       terminal answered for a grid that is not the one painting: bubbletea")
	fmt.Println("       reads the reply, moves the cell buffer to GraphemeWidth, and every")
	fmt.Println("       cluster the grid measures differently is now a cell the renderer")
	fmt.Println("       believes it owns and does not. Suspect a multiplexer forwarding the")
	fmt.Println("       query to the terminal underneath it.")
	fmt.Println("  Columns DIFFER -- the mode is live and honoured. Note WHICH rows moved.")
	fmt.Println()
	fmt.Println("ZWJ family, SKIN TONE and FLAG are the rows that carry the answer, because")
	fmt.Println("they are where grapheme clustering and a per-rune width sum diverge MOST:")
	fmt.Println("clustered they are 2, summed per rune they are 6, 4 and 2. A terminal")
	fmt.Println("painting 6 for the family is doing no clustering at all, and the 4 cells")
	fmt.Println("past our budget are the ones that stay on screen after the frame moves on.")
}

func codepoints(s string) string {
	var b []string
	for _, r := range s {
		b = append(b, fmt.Sprintf("%04X", r))
	}
	return strings.Join(b, " ")
}

// paintedCells prints s at column 1 and asks the terminal where the cursor
// ended up. The answer is 1-based, so the cells painted is col-1.
func paintedCells(in <-chan byte, s string) (int, error) {
	fmt.Fprint(tty, "\r\x1b[2K")
	fmt.Fprint(tty, s)
	fmt.Fprint(tty, "\x1b[6n")

	// CPR is ESC [ rows ; cols R
	body, err := readReply(in, 'R', "ESC[6n")
	if err != nil {
		return 0, err
	}
	i := strings.IndexByte(body, '[')
	if i < 0 {
		return 0, fmt.Errorf("malformed CPR reply %q", body)
	}
	parts := strings.SplitN(strings.TrimSuffix(body[i+1:], "R"), ";", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("malformed CPR reply %q", body)
	}
	col, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("malformed CPR reply %q", body)
	}
	return col - 1, nil
}

// readReply collects bytes from the terminal until term arrives, and is
// factored out of paintedCells rather than copied into queryMode so the two
// share ONE timeout and ONE 32-byte ceiling. A second copy of this loop is
// exactly the shape that drifts.
func readReply(in <-chan byte, term byte, what string) (string, error) {
	// DRAIN WHAT A TIMED-OUT PREDECESSOR LEFT BEHIND before waiting on this
	// query's own reply, so a late answer cannot be read as this one's.
	for drained := true; drained; {
		select {
		case <-in:
		default:
			drained = false
		}
	}

	var buf []byte
	deadline := time.After(2 * time.Second)
	for {
		select {
		case b, ok := <-in:
			if !ok {
				return "", fmt.Errorf("terminal closed while waiting for %s", what)
			}
			buf = append(buf, b)
			if b == term {
				return string(buf), nil
			}
			if len(buf) > 32 {
				return "", fmt.Errorf("no %s reply in 32 bytes (%q)", what, buf)
			}
		case <-deadline:
			return "", fmt.Errorf("terminal did not answer %s within 2s", what)
		}
	}
}

// startReader reads the terminal for the life of the program, in ONE goroutine.
//
// ⚠️ A GOROUTINE PER QUERY IS THE BUG THIS SHAPE EXISTS TO NOT HAVE. Orphaned
// by its own timeout, it stays blocked in ReadByte and consumes the NEXT
// query's reply -- so a single unanswered DECRQM, which is the ordinary
// behaviour of a terminal that does not know mode 2027, would cascade and turn
// every measured row after it into ERR. The probe would report a terminal that
// paints nothing rather than one that answered nothing.
func startReader(rd *bufio.Reader) <-chan byte {
	ch := make(chan byte, 4096)
	go func() {
		defer close(ch)
		for {
			b, err := rd.ReadByte()
			if err != nil {
				return
			}
			ch <- b
		}
	}()
	return ch
}

// queryMode asks DECRQM for one DEC private mode and answers the Ps the
// terminal reports: 0 not recognised, 1 set, 2 reset, 3 permanently set,
// 4 permanently reset.
//
// IT ASKS THE QUESTION BUBBLETEA ASKS, SPELT THE SAME WAY, because the point
// is to learn what BUBBLETEA WILL CONCLUDE and not what a careful program
// could work out. A multiplexer that forwards this query to the terminal
// underneath it answers for a grid that is not the one doing the painting,
// and no amount of care at this end would notice.
func queryMode(in <-chan byte, mode int) (int, error) {
	fmt.Fprintf(tty, "\x1b[?%d$p", mode)
	// DECRPM is ESC [ ? mode ; Ps $ y
	body, err := readReply(in, 'y', fmt.Sprintf("DECRQM %d", mode))
	if err != nil {
		return 0, err
	}
	i := strings.IndexByte(body, ';')
	if i < 0 {
		return 0, fmt.Errorf("malformed DECRPM reply %q", body)
	}
	ps := strings.TrimSuffix(strings.TrimSuffix(body[i+1:], "y"), "$")
	v, err := strconv.Atoi(strings.TrimSpace(ps))
	if err != nil {
		return 0, fmt.Errorf("malformed DECRPM reply %q", body)
	}
	return v, nil
}

// modeMeaning spells what bubbletea does with each DECRPM answer, and the two
// that keep ansi.WcWidth are 0 and 4 ALONE. Set, reset and permanently-set all
// send the renderer to ansi.GraphemeWidth -- "reset" included, because the
// question bubbletea is asking is whether the mode EXISTS, not whether it is
// currently on.
func modeMeaning(v int) string {
	switch v {
	case 0:
		return "not recognised -- bubbletea KEEPS ansi.WcWidth"
	case 1:
		return "set -- bubbletea switches the renderer to ansi.GraphemeWidth"
	case 2:
		return "reset -- bubbletea switches the renderer to ansi.GraphemeWidth"
	case 3:
		return "permanently set -- bubbletea switches the renderer to ansi.GraphemeWidth"
	case 4:
		return "permanently reset -- bubbletea KEEPS ansi.WcWidth"
	}
	return "undocumented value"
}

// setMode sets or resets a DEC private mode. Nothing is read back, on purpose:
// a terminal that does not know the mode ignores both spellings silently, and
// the two measurement columns are what expose that rather than any reply.
func setMode(mode int, on bool) {
	verb := "l"
	if on {
		verb = "h"
	}
	fmt.Fprintf(tty, "\x1b[?%d%s", mode, verb)
}

// cellStr is one measured cell of the report.
func cellStr(n int, err error) string {
	if err != nil {
		return "ERR"
	}
	return strconv.Itoa(n)
}

// isTTY asks whether a controlling terminal exists at all, NOT whether
// stdin is one: stdout and stdin may both be redirected and the run is
// still a legitimate measurement, because every escape goes to /dev/tty.
func isTTY() bool {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// rawMode shells out to stty rather than taking a dependency, so this
// program's go.mod stays empty and go list ./... in the parent module is
// unaffected. scripts/inputprobe does the same.
func rawMode() (func(), error) {
	saved, err := stty("-g")
	if err != nil {
		return nil, err
	}
	if _, err := stty("raw", "-echo"); err != nil {
		return nil, err
	}
	var once bool
	return func() {
		if once {
			return
		}
		once = true
		_, _ = stty(strings.TrimSpace(saved))
	}, nil
}

func stty(args ...string) (string, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = tty
	out, err := cmd.Output()
	return string(out), err
}

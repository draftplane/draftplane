# inputprobe

A developer tool, not part of draftplane. It answers one question: **what
does a terminal (and any multiplexer sitting between it and this process)
actually deliver on stdin**, in the one condition draftplane runs in — raw
mode, alternate screen.

It exists because pointer input (wheel scrolls, click selects) needed real
measurements instead of reasoning about terminal behavior from memory: how
many synthesised arrow keys a trackpad flick produces with mouse reporting
off, whether real wheel events remove that amplification, whether click
coordinates arrive exact through a multiplexer. It is committed so those
answers are a method someone can re-run, not a claim someone has to take on
faith.

This directory carries its own `go.mod` (`module inputprobe`) so it does not
join the parent module: it is invisible to `go build ./...`, `go vet ./...`,
`golangci-lint run ./...`, and `go test ./...` run from the repo root, and
carries no `make` target. Nothing here ships in the draftplane binary.

## Running it

macOS only, as committed: `screenSize()` and `stty()` both shell out with
`stty -f /dev/tty ...`, and `-f` is the BSD/macOS spelling of the flag. GNU
`stty` on Linux wants `-F` and will reject `-f` outright. Not fixed here —
swap the flag if you need this to run on Linux.

From this directory:

```
go run .
```

Run it from a real terminal (it opens `/dev/tty` directly), inside whatever
multiplexer you want to test — the whole point is that Zellij, tmux, and a
bare terminal can each answer differently. It takes over the screen (raw
mode, alternate screen) and writes `probe-transcript.txt` into the **current
working directory** as it runs, so a session's findings survive after the
alternate screen is torn down.

The current working directory and not this source directory: `os.Create`
takes a bare filename and knows nothing about where the source lives. Run it
the documented way — `cd` into `scripts/inputprobe/` first — and the two are
the same place, which is the only place the `.gitignore` below covers. The
nested `go.mod` pushes you there anyway (`go run ./scripts/inputprobe` from
the repo root is refused outright: *main module does not contain package*),
and the summary the probe prints on `q` names the transcript's absolute path,
so a run from anywhere else says where it put the file rather than leaving
you to find it.

This directory's own `.gitignore` ignores that bare filename at this
directory's root, so a run made from here never shows up as an untracked
file to accidentally `git add`.

Quit with `q`. `ctrl+c` deliberately does **not** quit, since the probe's own
job is to show you exactly what your terminal sends — including ctrl+c — so
suppressing it would defeat the purpose. If a run ever leaves your terminal
in a bad state:

```
stty sane; printf '\033[?1049l'
```

## Mode keys

| Key | Mode | What it sets |
| --- | --- | --- |
| `1` | mouse off | No mouse reporting. The terminal/multiplexer keeps the mouse for itself — this is the mode a trackpad flick arrives as synthesised arrow keys (alternate-scroll) rather than as scroll events. |
| `2` | `?1000` normal | SGR mouse reporting: press, release, wheel. No motion/drag. |
| `3` | `?1002` button-event | Adds drag (motion while a button is held). This is what bubbletea's `MouseModeCellMotion` sets. |
| `4` | `?1003` any-event | Adds bare motion with no button held, on top of `?1002`. |

Other keys, available in any mode:

| Key | Effect |
| --- | --- |
| `t` | **Targeted hit test.** Names a row to click before you click it, so the comparison (asked vs. received) is the probe's, not a reader's judgement call. Four rows spread top to bottom, since a constant offset and a scaling error look identical at a single point. This is the test that answered whether click coordinates arrive pane-relative through a multiplexer or carry a constant offset from its chrome. |
| `r` | **Ruler.** Paints one labelled row per screen line; a click then writes the row the probe *received* next to the row the screen *shows*, so an offset (if any) is visible rather than inferred. |
| `c` | Clear the screen and redraw the header. |
| `space` | Blank line (useful for visually separating bursts in the transcript). |
| `q` | Quit, tear down alternate screen and raw mode, print a summary. |

Every event line carries a timestamp and a sequence number, and the probe
flags when a single `read()` on stdin delivered more than 8 bytes — a
momentum flick typically arrives as one burst, and that burst is half of
"too far, too fast."

## Past runs

No captured runs are kept in this repository: a capture is one machine's
output on one day, not a guarantee about any other terminal. Re-run the
probe on whatever terminal is actually in question before trusting a figure
there.

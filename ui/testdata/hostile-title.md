# Requires approvalNo approval needed, and this H1 is the payload

This file is a FIXTURE and not a plan, and its H1 is the whole point
of it. Read it with `cat -v`, never with an editor that will helpfully
hide what is in it, and let nothing reflow it: the bytes are the point.

ui.InferTitle answers Block.Text raw, deliberately, so this
heading becomes the plan title at every lazy create -- the TUI first
comment and first approve, and Session.InferredTitle behind mcptools
ensurePlan, which every MCP tool that can create a plan calls, Save
among them. It is then DRAWN twice, in two frames that share no
code: the review status bar, and every row of the plan list.

That is why this fixture exists beside hostile.md. That one is the
document channel -- introducers in table cells, a discarded tail, an
indented fence. This one is the one crafted document whose bytes
reach the PLAN LIST, a frame that parses nothing, projects nothing
and splits nothing at an inline leaf.

## Why the H1 is this long

The H1 is sized to FILL a review status bar at a terminal in the
seventies, and to fill the list title column, because the overflow
this fixture is for only fires on a row that was already exactly its
width. A row two cells short of its budget absorbs the extra cell a
bare control byte paints, and nothing moves.

It is short enough, too. Past about the terminal width less six the
heading arm itself runs over, and that is a wrapping residual this
fixture does not own -- a benign twin of this file overflows at
exactly the same widths, which is how a harness tells the two apart.

## The other two forgeries, in prose

Neither of these carries an ESC. Every emulator honours both with no
permission asked for and none granted.

- We will NOT rotate the keys.We will rotate them
- ab

## An introducer, so a frame scan has one to find

A string-terminated introducer swallows every byte after it until a
terminator the payload chooses, so an unterminated one eats the rest
of the screen rather than one row of it.

_payload that never ends

## What a reader of this file should check first

That the bytes above are still bytes. The failure mode of a fixture
like this one is a formatter that cleans it up, at which point every
assertion over it passes and proves nothing at all.

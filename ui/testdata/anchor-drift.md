# Missing and unreadable plan files

## Goal

A reader who opens a plan whose file has gone missing, or whose file can no longer be read, is told plainly what happened and offered a way forward. Today the same broken file produces a different outcome depending on how the reader arrived at it: one route quietly shows an older version, another prints a raw error, and a third clears the screen. None of them says what is wrong or what to do next.

This plan gives each of the two failures one panel, reached the same way from every entry point, and lists the keys that resolve it.

## Background

Draftplane keeps a plan's comment threads and its version history apart from the file the plan follows. The file belongs to whoever writes it; Draftplane only ever reads it. That separation is what makes a missing file survivable: the threads and the most recent version are still there even when the file is not.

A file can break in two ways that matter here. It can disappear — moved, renamed or deleted — or it can stay where it was and refuse to be read, because its permissions changed or a directory now sits at its path. The two need different panels because the honest advice differs. A missing file may never come back; an unreadable one is still on disk and usually needs only a permission fixed.

A plan that never followed a file is not part of this. It has nothing to lose, and it already opens from its latest version.

## What the reader sees

### When the file is missing

The plan still opens, from the most recent version Draftplane holds, and a panel is drawn over the document. Its first line, "The file this plan follows is gone.", says what happened; the path underneath says which file; and the paragraph after that tells the reader that nothing about the review was lost. The keys come last.

Showing the document behind the panel is deliberate. Being able to read the plan is often the quickest way to remember where its file went.

### When the file cannot be read

The plan does not open, because Draftplane will not present an older version as if it were the live file. Instead the plan list shows a panel for the selected row. Its headline, "This plan's file is there but could not be read.", avoids the word "gone" on purpose: the file is exactly where the reader left it, and saying otherwise would send them searching for something that was never lost.

### From every entry point

Opening the plan from the list, opening it by path from the command line, and reloading it while it is open all reach the same panel for the same file. Which panel to show is decided in one place and every entry point asks that one place, so the three cannot drift apart again.

## Keys

Both panels offer the same key for the common remedy. `f to point this plan at a different file` opens the existing path prompt, filled in with the old path so that a small correction is a small edit. The new path must name a readable file that no other plan already follows; anything else is refused and nothing changes. Pointing the plan elsewhere changes where it reads from and nothing more — its threads, approvals and version history stay exactly as they were.

The missing-file panel adds two keys. `o` keeps the plan in Draftplane with no file at all, which is the quickest way to stop being asked about a file that is not coming back. `d` deletes the plan, through the same confirmation the list already uses for deleting one.

The unreadable-file panel adds one key instead. `r to read the file again once it is readable` retries the open after the reader has fixed the permission; if the file still refuses, the reader lands back on the same panel. There is no delete key on this panel: a permission problem should never be one keystroke away from losing every thread on the plan. Deleting stays available from the list row, behind its own confirmation.

On both panels, `esc to go back to the plan list` closes the panel and changes nothing.

Every key a panel offers is also named on the help bar, and the help bar names no key the panel does not handle.

## Layout

Both panels fit an 80-column, 24-row terminal without truncation. When a smaller window forces truncation, the headline and the key list are kept and the explanation in the middle is what gets dropped, which is why the keys are listed last. A long path wraps onto a second line rather than being clipped, so the reader always sees all of it.

## Tasks

### One answer for every entry point

- Record, when a plan is opened, whether its file was missing, so later code reads that fact instead of working it out again.
- Route the list, the command-line open and reload through a single helper that tries the file first and falls back to the latest version only when the file is missing.
- Make reload keep the current document on screen when its file disappears, instead of discarding it.
- Test that all three entry points reach the same state for the same broken file, and that a genuinely mistyped path on the command line still reports an error.

### The missing-file panel

- Add the panel and its keys to the review screen, drawn over the document.
- Reuse the existing delete confirmation for `d` rather than writing a second copy of its text.
- Test that the panel renders exactly as written at 80 columns, and that the help bar lists exactly the keys the panel accepts.

### The unreadable-file panel

- Add the panel to the plan list, shown when an open fails while the file is still on disk.
- Send `r` through the list's ordinary open, so a repaired file opens like any other plan.
- Test that an unreadable file reaches the panel rather than a bare error, that `r` opens the plan once the file is readable, and that `d` on the row still works.

### Pointing a plan at a new file

- Add a store change that sets a plan's path and touches no other field.
- Test that the change leaves threads, approvals and versions byte for byte unchanged, and that a path another plan already follows, a directory, and a path that does not exist are each refused with nothing written.

## Non-goals

- No restoring a missing file from the copy Draftplane holds. The bytes are there, but writing them back to disk is a separate feature.
- No marker on list rows for broken files. Checking every row's file on each refresh costs more than it is worth when opening the plan already explains the problem.
- No confirmation on `o`. The plan, its threads and its versions are untouched; only the link to the file is dropped.
- No change to the refusal to show an older version for an unreadable file.
- No new tool for agents. Pointing a plan at a different file is a person's decision about a path on their own machine.
- No change to the stored format and no new dependency.

## The panels, verbatim

Both panels are hand-wrapped paragraphs with blank lines between them and the key list last.

**Panel for a missing file — review model, over the document:**

```
The file this plan follows is gone.

  ~/plans/auth-redesign.md

Draftplane still has the plan, its comment threads, and the most recent
version.

f to point this plan at a different file
o to keep it in Draftplane with no file
d to delete this plan
esc to go back to the plan list
```

**Panel for an unreadable file — list model:**

```
This plan's file is there but could not be read.

  ~/plans/auth-redesign.md

The file is still on disk; only reading it failed.

f to point this plan at a different file
r to read the file again once it is readable
esc to go back to the plan list
```

*("still on disk", never "damaged" — the panel does not diagnose a file nobody has looked at yet. The path is left to hard-wrap rather than clipped: `leftClip` keeps a path's tail and is right for a one-line status bar, and wrong here, where two readable lines are lossless.)*

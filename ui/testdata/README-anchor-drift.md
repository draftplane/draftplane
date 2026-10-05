# `anchor-drift.md` and `anchor-drift-anchors.json`

A document and four comment anchors stored against an earlier version of it.
Each anchor quotes one of the two panels under "The panels, verbatim" as it
read before its copy was edited; the document holds the edited copy. The pair
is the regression fixture for cross-block fuzzy placement in
`app/anchordrift_test.go`.

## Why these four

All four re-anchor as `StatusFuzzy`, accepted by the matcher's gate (a 0.65
score and a 0.05 margin over the best rival elsewhere), and every winning
window runs across a block boundary, so no single `ui.Block` contains one —
which is what `ui.ResolveAnchor` would need.

Two windows overlap the missing-file panel's code block and two the
unreadable-file panel's. Mapping a window to the block holding its first byte
is wrong for all four; mapping by greatest byte overlap is right for all four.

All four threads are resolved. Resolution excludes a thread from the orphan
list, so a resolved thread that fails to place has no other way to surface.

## Editing it

Identify blocks by their opening text, never by index: the missing-file panel
opens `The file this plan follows is gone.` and the unreadable-file panel
opens `This plan's file is there but could not be read.`

After any edit to either file, re-measure: all four must still come back
`StatusFuzzy`, the badges pinned in the test must match, and both mutations
described in `TestFuzzyThreadsLandOnThePanelTheMatcherMatched` must still turn
it red.

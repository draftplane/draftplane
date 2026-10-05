package ui

import "strings"

// This file is the four-terminal disagreement over a variation-selector-16
// sequence, and the one-line rule that closes it.
//
// scripts/widthprobe asked four terminals directly (CPR, not a Unicode table)
// what they paint for U+26A0 U+FE0F (warning sign + VS16): ansi.StringWidth
// and lipgloss.Width both answer 2; Ghostty and tmux PAINT 2; Zellij and GNU
// screen PAINT 1. Four authorities, two answers, no majority -- so NO WIDTH
// RULER can be right against all four at once, and this package's own two
// never disagree with each other, so the defect is not our rulers disagreeing
// with themselves either.
//
// THE BARE BASE -- U+26A0 alone, selector removed -- IS THE ONE REGION EVERY
// RULER AND EVERY TERMINAL MEASURED AGREES ON: 1 cell, everywhere. So the
// selector is removed, rather than a ruler picked that two of four terminals
// will disagree with regardless.
//
// vs16 is that selector, U+FE0F VARIATION SELECTOR-16: it carries no width
// of its own on any of the four terminals or either ruler -- it only tells a
// renderer which of two glyphs (text or emoji presentation) to draw for the
// base character in front of it.
const vs16 = '\uFE0F'

// stripSelector16 removes every U+FE0F in s.
//
// IT IS A RULE ABOUT THE SELECTOR AND NOT A TABLE OF CHARACTERS. 12,268 base
// characters have a measured width that flips under U+FE0F: 1,307 where ansi
// and uniseg already agree the sequence is two cells wide (a real corpus's own
// clusters), and 10,961 more where ansi alone is wrong. A table keyed to the
// 1,307 would leave the other 10,961 exactly as broken as before this file
// existed. Stripping the selector closes both at once, because neither class's
// disagreement survives the selector's removal.
//
// IT RUNS UNCONDITIONALLY, ON BOTH CLASSES, WITH NO BRANCH ON WHICH TERMINAL
// IS RUNNING. Ghostty and tmux already paint the two-cell answer this package
// budgets for, so stripping there changes no row's width; Zellij and GNU
// screen paint one cell short of the budget, and removing the selector is what
// stops the short paint. A document is rendered once and has to be right in
// EVERY terminal that opens it afterward, not in whichever one motivated the
// fix -- the same argument fourSpaceTabs (ui/painted.go) makes for a tab, and
// there is no ZELLIJ environment check here for the same reason there is none
// there.
//
// IT ANSWERS s UNCHANGED, WITH NO ALLOCATION, WHEN THERE IS NOTHING TO DO.
func stripSelector16(s string) string {
	if !strings.ContainsRune(s, vs16) {
		return s
	}
	return strings.ReplaceAll(s, string(vs16), "")
}

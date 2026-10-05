package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/yuin/goldmark/ast"
)

// renderInlines walks a block's inline AST and produces styled display text:
// **bold**, *italic*, `code`, and links become terminal styling instead of
// literal markers. Each text leaf is rendered with its full accumulated style
// (no nested SGR state), so wrapped lines never inherit dangling styles.
//
// codeSpan and breakSpace are threaded as parameters rather than fixed package
// styles so that ONE walk serves all three of a block's projections: the styled
// walks are handed the theme's own code-span style and a space PRE-RENDERED
// from the base text style, so a span keeps its own background and a break
// carries the zone's rather than leaving an unpainted gap; plainProjection
// hands both a zero style and a bare " ". breakSpace is deliberately not
// computed here as style.Render(" "), because style at that point is the
// ACCUMULATED style and inside an emphasis or link span carries
// italic/bold/underline -- a break belongs to no span. Both are forwarded
// unaltered through every recursive call.
//
// IT IS ALSO WHERE THE CONTROL-BYTE FILTER SITS, AND THE LEAF IS FOUR
// EXPRESSIONS AND NOT ONE. Every place below where a slice of the SOURCE
// becomes a rendered string
// goes through renderControls (ui/control.go): the text segment, a code span's
// plainText, an autolink's URL and an *ast.String's value. A filter installed
// only at the first would cover neither `a\x08b` inside backticks nor a hostile
// autolink, both of which reach a ui.Line. The recursive arms carry no source
// of their own.
//
// WHAT IS STILL NOT COVERED HERE is an inline LINK's destination, and it is a
// property of the AST rather than of this function: the *ast.Link arm recurses
// into the link's children and never reads node.Destination, so a destination
// is not a leaf at all -- it reaches a screen only where a whole raw source
// span does (the heading arm, the fence arm, displayIn's fallback), and all
// three filter at the arm instead. The adjacent *ast.AutoLink arm does render
// its URL, which is why that one is a leaf and is filtered here.
func renderInlines(n ast.Node, source []byte, style, codeSpan lipgloss.Style, breakSpace string) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch node := c.(type) {
		case *ast.Text:
			seg := node.Segment
			b.WriteString(renderControls(string(source[seg.Start:seg.Stop]), style))
			if node.SoftLineBreak() || node.HardLineBreak() {
				b.WriteString(breakSpace)
			}
		case *ast.CodeSpan:
			b.WriteString(renderControls(plainText(node, source), codeSpan))
		case *ast.Emphasis:
			st := style.Italic(true)
			if node.Level >= 2 {
				st = style.Bold(true)
			}
			b.WriteString(renderInlines(node, source, st, codeSpan, breakSpace))
		case *ast.Link:
			b.WriteString(renderInlines(node, source, style.Underline(true), codeSpan, breakSpace))
		case *ast.AutoLink:
			b.WriteString(renderControls(string(node.URL(source)), style.Underline(true)))
		default:
			if c.HasChildren() {
				b.WriteString(renderInlines(c, source, style, codeSpan, breakSpace))
			} else if node, ok := c.(*ast.String); ok {
				b.WriteString(renderControls(string(node.Value), style))
			}
		}
	}
	return b.String()
}

func plainText(n ast.Node, source []byte) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); ok {
			b.WriteString(string(source[t.Segment.Start:t.Segment.Stop]))
		} else if c.HasChildren() {
			b.WriteString(plainText(c, source))
		}
	}
	return b.String()
}

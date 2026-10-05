package reanchor

import (
	"regexp"
	"strings"
)

// Section is one heading-delimited region of a markdown document. Content is
// direct content only — it ends at the next heading of any level.
//
// ContentStart is the byte offset of Content within the document ParseSections
// was given, so an offset into Content is an offset into that document. It
// names the line AFTER the section's heading, because a heading line is not
// part of its own section's content.
//
// It does NOT round-trip to a document slice for the LAST section, in any
// document: Content accumulates line + "\n" for every line, so the section
// holding the document's tail carries one "\n" the document does not have and
// doc[ContentStart:ContentStart+len(Content)] runs past the end. Every other
// section equals its slice byte for byte.
type Section struct {
	HeadingPath  []string
	Title        string
	Level        int
	Content      string
	ContentStart int
}

// Closing hashes are only a closing sequence when whitespace-separated from
// the title ("# Title #" → "Title", but "# Migrating to F#" keeps its hash).
var headingRe = regexp.MustCompile(`^(#{1,6})\s+(.+?)(?:\s+#+)?\s*$`)
var fenceRe = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")

// ParseSections splits a markdown document into sections keyed by heading
// path. ATX headings only; headings inside fenced code blocks are ignored.
// Content before the first heading becomes a section with an empty path.
func ParseSections(doc string) []Section {
	lines := strings.Split(doc, "\n")
	var sections []Section
	type stackEntry struct {
		title string
		level int
	}
	var stack []stackEntry
	current := Section{}
	var fence string

	push := func() {
		if strings.TrimSpace(current.Content) != "" || len(current.HeadingPath) > 0 {
			sections = append(sections, current)
		}
	}

	// next is the byte offset just past the current line. It must advance for
	// EVERY line, the fence branch and the lines of a section push() declines
	// to emit included: it tracks position in the DOCUMENT, not bytes of
	// section content emitted.
	next := 0

	for li, line := range lines {
		next += len(line)
		if li < len(lines)-1 {
			next++ // the "\n" strings.Split consumed; the last line had none
		}
		if m := fenceRe.FindStringSubmatch(line); m != nil {
			marker := m[1]
			rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimLeft(line, " "), marker))
			if fence == "" {
				fence = marker
			} else if marker[0] == fence[0] && len(marker) >= len(fence) && rest == "" {
				// A closing fence must match the opening character, be at
				// least as long, and carry no info string (CommonMark).
				fence = ""
			}
			current.Content += line + "\n"
			continue
		}
		if fence == "" {
			if m := headingRe.FindStringSubmatch(line); m != nil {
				push()
				level := len(m[1])
				title := m[2]
				for len(stack) > 0 && stack[len(stack)-1].level >= level {
					stack = stack[:len(stack)-1]
				}
				stack = append(stack, stackEntry{title: title, level: level})
				path := make([]string, len(stack))
				for i, e := range stack {
					path[i] = e.title
				}
				current = Section{HeadingPath: path, Title: title, Level: level, ContentStart: next}
				continue
			}
		}
		current.Content += line + "\n"
	}
	push()
	return sections
}

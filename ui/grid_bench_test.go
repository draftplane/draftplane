package ui

// This file is the harness behind the performance figures recorded at
// tableGridCache (ui/painted.go). It exists because those figures named a
// method -- `go test -bench` -- and no benchmark of the grid was committed, so
// the method was a sentence rather than a command: a reviewer wanting to check
// the ranges had to write its own harness first, and two harnesses measuring
// one thing is how this package once produced a figure wrong by 2.5x.
//
// WHAT IT MEASURES, one sub-benchmark per row of that comment's table:
//
//	paint/cold     paintTables, every table drawn and cut from scratch
//	paint/hit      paintTablesCached against an entry it can reuse
//	render/warm    RenderDoc with the memo answering
//	render/cold    RenderDoc with the memo invalidated before every call
//
// TWO DOCUMENTS, AND THEY ANSWER DIFFERENT QUESTIONS. `committed` is
// benchGridDoc below: synthetic, deterministic and in this file, so the
// benchmark produces numbers on any checkout with no corpus and no network --
// the arm a later change should be compared against, because it is the only one
// that will still exist unchanged in a year. `corpus` is a real document,
// whatever markdown file DRAFTPLANE_BENCH_DOC names -- tableGridCache's
// table was measured on the largest document in a real corpus -- and is simply
// ABSENT when that variable is unset. A path that names nothing IS a failure,
// because it is a typo in something the runner asked for.
//
// THE METHOD IS PART OF THE FIGURE. Every number taken from this harness was
// taken with `-benchtime 20x -count 5`, reported as a RANGE over TWO sittings,
// because the run-to-run spread is 5-10% and one sitting's range reads tighter
// than the measurement is. One figure in this package was recorded 2.5x wrong
// because it was measured under heavy concurrent load on the same machine.
//
// WHAT IT CANNOT DO: produce the "before the grid was wired in" row of
// tableGridCache's table. That figure is a property of ANOTHER TREE, and no
// benchmark committed here can be run there; to re-derive it, copy this file
// onto that checkout.
//
// The figures themselves are at tableGridCache, where the decision they support
// is. The two arms are NOT comparable as absolute numbers and are not meant to
// be: this one draws 385 rows of deliberately wrap-heavy cells and that one
// draws 368 rows of somebody's real notes.

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

const envBenchDoc = "DRAFTPLANE_BENCH_DOC"

// benchGridDoc is a table-heavy plan document with the shape of the corpus
// document tableGridCache's figures were taken on: 35 tables of 10 body rows
// and a header -- 385 table rows -- each behind a heading and a paragraph, for
// 455 blocks. It is table rows this measures, and on those the two arms are
// within 5% of each other; a real corpus document's remaining ~900 blocks are
// prose the grid never touches, which is why matching the table population
// matters and matching the block count does not.
//
// IT IS NOT A COPY OF THAT DOCUMENT and does not try to be. What a grid costs is
// decided by the number of tables, their row and column counts, and how much
// wrapping their cells force. So the cells are deterministic strings of a length
// chosen to make the third column WRAP at the widths this is driven at, because
// a table that fits on one line per row exercises the resizer and not the
// wrap.
func benchGridDoc(tables, rows int) []byte {
	var b strings.Builder
	for t := range tables {
		fmt.Fprintf(&b, "## Section %d\n\n", t)
		fmt.Fprintf(&b, "The **deploy** step for section %d needs a rollback plan\nbefore the gate is run again.\n\n", t)
		b.WriteString("| Step | Owner | Notes |\n|---|---|---|\n")
		for r := range rows {
			fmt.Fprintf(&b, "| Step %d.%d | owner-%d | %s |\n", t, r, r, strings.Repeat(fmt.Sprintf("note %d ", r), 12))
		}
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// benchGridDocs is the two arms this file drives, in order. The corpus arm is
// absent rather than skipped when no document is named, so a run without one
// reports the committed arm's numbers and says nothing at all about the other
// -- a skipped sub-benchmark in the output reads like a failure and this is
// not one.
func benchGridDocs(b *testing.B) []struct {
	name string
	src  []byte
} {
	b.Helper()
	out := []struct {
		name string
		src  []byte
	}{{"committed", benchGridDoc(35, 10)}}

	path := os.Getenv(envBenchDoc)
	if path == "" {
		return out
	}
	src, err := os.ReadFile(path)
	if err != nil {
		// A named document that cannot be read is a typo in something the
		// runner asked for, and reporting one arm as though nothing were
		// missing would hide it.
		b.Fatalf("%s names %s, which is not readable: %v", envBenchDoc, path, err)
	}
	return append(out, struct {
		name string
		src  []byte
	}{"corpus", src})
}

// BenchmarkTableGrid is the committed method behind tableGridCache's figures.
// Run it as:
//
//	go test ./ui -run '^$' -bench BenchmarkTableGrid -benchtime 20x -count 5
//
// The width is 160 because that is the terminal this product is used in and
// the width every figure at tableGridCache was taken at; a narrower budget
// makes every table taller and moves all four rows together.
func BenchmarkTableGrid(b *testing.B) {
	const width = 160
	st := darkStyles(b)
	for _, doc := range benchGridDocs(b) {
		b.Run(doc.name, func(b *testing.B) {
			blocks := ParseBlocks(doc.src, st)
			// The arm is reported rather than asserted: the corpus arm's
			// document is somebody's working directory and its table count
			// moves. A benchmark that failed on that would be pinning a corpus
			// figure, which this package does not do.
			tables, rows := 0, 0
			seen := map[int]bool{}
			for _, blk := range blocks {
				if blk.Kind != KindTableRow {
					continue
				}
				rows++
				if !seen[blk.Table.ID] {
					seen[blk.Table.ID] = true
					tables++
				}
			}
			b.Logf("%s: %d blocks, %d table rows in %d tables, at width %d", doc.name, len(blocks), rows, tables, width)
			if rows == 0 {
				b.Fatalf("%s holds no table rows, so nothing below measures a grid", doc.name)
			}

			b.Run("paint/cold", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					paintTables(blocks, width, st, nil, nil)
				}
			})

			b.Run("paint/hit", func(b *testing.B) {
				// Primed OUTSIDE the loop, so what is timed is the key walk
				// and not the paint it is there to avoid. If this ever
				// measures the same as paint/cold, the key stopped matching
				// its own arguments.
				paintTablesCached(blocks, width, st, nil, nil)
				b.ReportAllocs()
				for b.Loop() {
					paintTablesCached(blocks, width, st, nil, nil)
				}
			})

			b.Run("render/warm", func(b *testing.B) {
				RenderDoc(blocks, nil, nil, nil, Cursor{}, width, "", st)
				b.ReportAllocs()
				for b.Loop() {
					RenderDoc(blocks, nil, nil, nil, Cursor{}, width, "", st)
				}
			})

			b.Run("render/cold", func(b *testing.B) {
				// coldTableGridCache is inside the timed region and its cost
				// is a mutex and a bool -- nanoseconds against tens of
				// milliseconds. Taking it out would need a b.StopTimer pair
				// per iteration, which costs more than what it would exclude.
				b.ReportAllocs()
				for b.Loop() {
					coldTableGridCache()
					RenderDoc(blocks, nil, nil, nil, Cursor{}, width, "", st)
				}
			})
		})
	}
	coldTableGridCache()
}

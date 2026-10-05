# Fidelity fixture

This document is one property in one file: every block-level construct nested
inside another, at every depth real documents reach, with **inline markup**
that must reach the screen as text and never as markers.

A second population sits under Tables: every SHAPE a table can come in, which
nesting depth cannot express and a bordered grid draws very differently. Each
of those tables is labelled with the shape it stands in for. The measured
share each one represents is recorded once, beside the census in
ui/ui_test.go, and deliberately not repeated here.

## Quotations

> Quoted at depth one.

> Depth one again, and beneath it the two deeper levels:
>
> > Quoted at depth two.
> >
> > > Quoted at depth three, which is as deep as quotations in real plans go,
> > > written long enough here that it wraps at eighty columns and shows what
> > > a continuation row does with its bars.
>
> > - a bullet quoted two deep

> ### A quoted heading
>
> - a quoted bullet
> - a second quoted bullet
>   - a list nested two deep inside the quotation
>
> ~~~
> quoted_code_line()
> ~~~

- A bullet that carries a quotation.

  > Quoted inside a list item.

## Tables

| Step | Owner |
|---|---|
| Deploy the gateway | dana |
| Roll back | sam |

| Phase | Owner | Window | Signal | Notes |
|---|---|---|---|---|
| Soak | dana | Tuesday | error rate | nothing yet |
| Cutover | sam | Wednesday | latency | after the soak |

> | Quoted step | Quoted owner |
> |---|---|
> | Quoted deploy | dana |

| Header with no rows | Second such header |
|---|---|

An empty header cell: a column whose heading nobody wrote.

|  | Owner | Note |
|---|---|---|
| Deploy | dana | the header cell above the first column is empty |

Seven columns, which is as wide as real plans' tables get and is the table
the grid was built for.

| Chan | Auth | Rate | Audit | Retry | Owner | State |
|---|---|---|---|---|---|---|
| hook | hmac | 100/s | yes | three | dana | live |
| email | none | 10/m | no | one | sam | soak |

A body row with more cells than the header. The parser discards the excess
before the AST exists, so those bytes reach no cell; the last cell carries them
raw, pipes and all, rather than losing text a reviewer can see in an editor.

| Step | Owner |
|---|---|
| Roll forward | dana |
| Roll back | sam | a third cell, which the header has no column for |

A token longer than any column it can be given, which is what makes a grid
break a word rather than a line.

| Case | Where |
|---|---|
| the gesture journey | app/search_test.go:TestTheSearchGestureEndToEndThroughTheFrame |
| the sample depths fixture | ui/ui_test.go:TestFidelityFixtureReachesEveryCorpusDepth |

A cell of two thousand characters: a paragraph boxed into one column.

| Note | Detail |
|---|---|
| the long one | A cell can hold a paragraph, and real plans do carry one, written into a single column instead of split across many rows, because its author simply kept writing. This cell exists so that shape lives in a committed fixture, because a grid boxes a paragraph into a channel of whatever width the resizer gives that column, and the row that comes out can run to tens of screen rows tall. A prose cell inside a bordered grid is a shape most readers of a wiki or a changelog have seen before, and the alternative is two different renderings of the same kind of content depending on which column it landed in. What this cell has to demonstrate is that nothing breaks when the tall row arrives. The box must still close under it. The row must still cut into exactly one line group, so a reviewer can anchor a comment on it and the cursor can step onto it as a unit. Every line of it must still fit the width budget, and no single line may carry two screen rows, because the frame is cut to a viewport height and a line that secretly holds two makes the frame taller than the terminal it was measured for. The wrap must break at a space wherever a space is available, and where a single token is longer than the column it must break inside that token rather than run over the border, which is the mid-word breaking a cell this wide will sometimes need. And the search index must still match this text on the row it is drawn on, because the governing rule of the search this view ships is that a reader searches what a reader sees. Not one of those properties is interesting on a cell of four words. All of them are interesting here, which is the whole reason this paragraph is a table cell and not a paragraph. One more thing it stands for: a handful of real tables carry a cell exactly this shape, so a fixture that stopped at a cell of forty characters would be standing in for a population it does not contain, and the tables it leaves out are the ones that cost the most to draw. A caption of forty characters would not need any of this at all. |

## Breaks

---

***

## The ordinary document around them

Prose with `a code span`, *emphasis* and [a link](https://example.com).

1. an ordered item
2. another ordered item
   - a nested bullet
     - and one deeper still

~~~go
func fenced() int { return 0 }
~~~

    indented code

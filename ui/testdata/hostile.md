# A crafted hostile document

This file is a FIXTURE and not a plan. Every payload in it is a real control
byte or a real escape introducer, written here because ordinary documents hold
no benign witness for any of them -- some of the defects it carries can only be
exercised by a document built for the purpose, which is why they are fixture
work rather than sweep work.

Read it with `cat -v`, not with an editor that will helpfully hide what is in
it. Nothing here should ever be reflowed by a formatter: the bytes are the
point.

## A table whose body cells carry a string-terminated introducer

Each of these swallows every byte after it until a terminator the payload
chooses, so an unterminated one eats the rest of the screen. The grid wraps a
cell to its column, which is what made "an escape sequence split across two
ui.Line values" a real question; a leaf filter that visualises the introducer
leaves nothing to split.

| step | note | state |
|---|---|---|
| _payload that never ends | run after 5pm, once the freeze is over | done |
| ]8;;https://evil.examplelabel]8;; | the hyperlink is the payload, not the label | done |
| Pq#0;2;0;0;0\ | a device control string | done |
| Xpayload\ | the sibling an invariant naming three of five would miss | done |
| ^payload\ | and the other one | done |

## A table whose HEADER cell carries one

| _payload that never ends | note | state |
|---|---|---|
| deploy the thing | run after 5pm | done |

## A malformed row, whose tail the parser throws away before the AST exists

The row below has more cells than its header, so GFM discards the excess and
those bytes reach no cell and no inline projection. They are drawn anyway --
tableRowCells appends them to the last cell raw -- which is the one span in the
whole channel that is not renderInlines output.

| a | b |
|---|---|
| one | two | ZQTAILZQmore |

## A fence indented by the list item that contains it

- an item, whose fence is indented two columns

  ```go
  func main() {
    	println("the tab and the indent are both real")
  }
  ```

## The two forgeries that need no escape sequence at all

Neither of these carries an ESC. Every emulator honours both with no permission
asked for and none granted, and what they falsify is the bytes a reviewer is
agreeing to.

- Requires approvalNo approval needed
- We will NOT rotate the keys.We will rotate them
- ab

### Requires approvalNo approval needed

A heading is a raw arm: it draws Block.Text and never calls renderInlines.

```
We will NOT rotate the keys.We will rotate them
```

A fence is the other raw arm.

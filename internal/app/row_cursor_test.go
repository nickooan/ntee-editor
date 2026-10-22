package app

import "testing"

func spanOf(total, height int, widths ...int) rowSpan {
	span := rowSpan{total: total, height: height}
	if widths != nil {
		span.rowWidth = func(row int) int { return widths[row%len(widths)] }
	}
	return span
}

func TestRowCursorMoveClampsRowAndColumn(t *testing.T) {
	span := spanOf(5, 3, 10, 2)
	c := rowCursor{row: 0, column: 8}
	c = c.moved(1, span) // row 1 is 2 runes wide
	if c.row != 1 || c.column != 2 {
		t.Fatalf("moved = %+v, want row 1 column clamped to 2", c)
	}
	c = c.moved(99, span)
	if c.row != 4 {
		t.Fatalf("moved past the end = %d, want 4", c.row)
	}
	if got := (rowCursor{row: 3}).moved(1, rowSpan{}); got.row != 3 {
		t.Fatal("an empty span (still loading) must leave the cursor alone")
	}
}

func TestRowCursorPageKeepsScreenRowWithOverlap(t *testing.T) {
	span := spanOf(100, 10)
	c := rowCursor{row: 3}.paged(1, span)
	if c.row != 12 || c.scrollY != 9 {
		t.Fatalf("paged down = %+v, want row 12 scrollY 9 (one-line overlap)", c)
	}
	if c.screenRow(span) != 3 {
		t.Fatalf("screen row = %d, want 3", c.screenRow(span))
	}
	c = rowCursor{row: 95, scrollY: 90}.paged(1, span)
	if c.row != 99 || c.scrollY != 90 {
		t.Fatalf("paged at the end = %+v", c)
	}
}

func TestRowCursorBlockJump(t *testing.T) {
	span := spanOf(20, 10)
	starts := map[int]bool{2: true, 9: true, 15: true}
	isStart := func(row int) bool { return starts[row] }
	c := rowCursor{row: 0}.blockJump(1, span, isStart)
	if c.row != 2 {
		t.Fatalf("next block = %d, want 2", c.row)
	}
	c = c.blockJump(1, span, isStart).blockJump(1, span, isStart).blockJump(1, span, isStart)
	if c.row != 15 {
		t.Fatalf("past the last block = %d, want to stay on 15", c.row)
	}
	if c = (rowCursor{row: 11}).blockJump(-1, span, isStart); c.row != 9 {
		t.Fatalf("from inside a block, up lands on its start: %d", c.row)
	}
	if c.scrollY != anchorScroll(9, 10, 20) {
		t.Fatalf("a jump anchors ~30%% from the top: scrollY %d", c.scrollY)
	}
}

func TestRowNavigationKeysWithAndWithoutColumn(t *testing.T) {
	withColumn := spanOf(10, 5, 4)
	c, handled := handleRowNavigationKey("end", rowCursor{row: 2}, withColumn)
	if !handled || c.column != 4 || c.row != 2 {
		t.Fatalf("end with a column cursor goes to the row end: %+v", c)
	}
	c, _ = handleRowNavigationKey("right", c, withColumn)
	if c.column != 4 {
		t.Fatalf("right stops at the row end: %d", c.column)
	}

	noColumn := spanOf(10, 5)
	c, handled = handleRowNavigationKey("end", rowCursor{row: 2}, noColumn)
	if !handled || c.row != 9 || c.scrollY != 5 {
		t.Fatalf("end without a column cursor goes to the document end: %+v", c)
	}
	if _, handled := handleRowNavigationKey("left", c, noColumn); handled {
		t.Fatal("left is not a navigation key without a column cursor")
	}
	if _, handled := handleRowNavigationKey("x", c, noColumn); handled {
		t.Fatal("other keys are the caller's")
	}
}

func TestRowCursorScrolledAndClickedAt(t *testing.T) {
	span := spanOf(30, 10)
	c := rowCursor{}.scrolled(1, span)
	if c.scrollY != 9 || c.row != 0 {
		t.Fatalf("scrolled = %+v, want only scrollY to move", c)
	}
	if c = c.scrolled(1, span).scrolled(1, span); c.scrollY != 20 {
		t.Fatalf("scroll clamps at the end: %d", c.scrollY)
	}

	c = rowCursor{row: 5, scrollY: 0}.clickedAt(9, 3, span) // bottom visible row
	if c.row != 9 || c.column != 3 || c.scrollY != 9 {
		t.Fatalf("clicking the bottom row pages down: %+v", c)
	}
	c = rowCursor{row: 5, scrollY: 0}.clickedAt(4, 0, span)
	if c.scrollY != 0 {
		t.Fatalf("an ordinary click keeps the window: %+v", c)
	}
}

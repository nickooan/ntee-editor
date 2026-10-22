package app

import "github.com/nickooan/ntee-editor/internal/input"

// rowSpan is what a read-only row view tells its cursor about itself: how
// many rows exist, how many fit on screen, and — for views with a column
// cursor (diff, blame, review) — each row's rune length. rowWidth is nil for
// views without a column cursor (document previews, the message history).
type rowSpan struct {
	total    int
	height   int
	rowWidth func(row int) int
}

// rowCursor is the cursor and viewport of a read-only row view. It is a plain
// value shared by every such view so moving, paging, and block jumps behave
// identically across diff, blame, previews, review, and the message history.
// Every method is a no-op on an empty span (the view is still loading).
type rowCursor struct {
	row     int
	column  int
	scrollY int
}

func (c rowCursor) clampColumn(span rowSpan) rowCursor {
	if span.rowWidth == nil {
		c.column = 0
		return c
	}
	c.column = input.Clamp(c.column, 0, span.rowWidth(c.row))
	return c
}

// moved moves the cursor by dy rows; the viewport follows at render time.
func (c rowCursor) moved(dy int, span rowSpan) rowCursor {
	if span.total == 0 {
		return c
	}
	c.row = input.Clamp(c.row+dy, 0, span.total-1)
	return c.clampColumn(span)
}

// paged pages with a one-line overlap, the cursor keeping its on-screen row.
func (c rowCursor) paged(dir int, span rowSpan) rowCursor {
	if span.total == 0 {
		return c
	}
	step := max(1, span.height-1)
	top := c.viewportTop(span)
	c.row = input.Clamp(c.row+dir*step, 0, span.total-1)
	c = c.clampColumn(span)
	c.scrollY = input.Clamp(top+dir*step, 0, max(0, span.total-span.height))
	return c
}

func (c rowCursor) toStart() rowCursor {
	c.row, c.column, c.scrollY = 0, 0, 0
	return c
}

func (c rowCursor) toEnd(span rowSpan) rowCursor {
	if span.total == 0 {
		return c
	}
	c.row = span.total - 1
	c.scrollY = max(0, span.total-span.height)
	return c.clampColumn(span)
}

// anchoredAt puts the cursor on row and scrolls it ~30% from the top, like
// every landed jump or match in the editor.
func (c rowCursor) anchoredAt(row int, span rowSpan) rowCursor {
	if span.total == 0 {
		return c
	}
	c.row = input.Clamp(row, 0, span.total-1)
	c = c.clampColumn(span)
	c.scrollY = anchorScroll(c.row, span.height, span.total)
	return c
}

// blockJump moves to the previous (dir<0) or next (dir>0) block start —
// diff hunks, blame commit groups. No wraparound; inert when there is no
// block in that direction. From inside a block, jumping up lands on that
// block's own start.
func (c rowCursor) blockJump(dir int, span rowSpan, isBlockStart func(row int) bool) rowCursor {
	target := -1
	for i := 0; i < span.total; i++ {
		if !isBlockStart(i) {
			continue
		}
		if dir < 0 {
			if i < c.row {
				target = i
			}
		} else if i > c.row {
			target = i
			break
		}
	}
	if target == -1 {
		return c
	}
	return c.anchoredAt(target, span)
}

// scrolled pages a cursorless view (a text panel) by one screen with a
// one-line overlap; only scrollY moves.
func (c rowCursor) scrolled(dir int, span rowSpan) rowCursor {
	step := max(1, span.height-1)
	c.scrollY = input.Clamp(c.scrollY+dir*step, 0, max(0, span.total-span.height))
	return c
}

// viewportTop is the first visible row, the same window the renderer draws.
func (c rowCursor) viewportTop(span rowSpan) int {
	return fileViewportTop(c.row, c.scrollY, span.height, span.total)
}

// screenRow is the cursor's on-screen row within the viewport.
func (c rowCursor) screenRow(span rowSpan) int {
	return c.row - c.viewportTop(span)
}

// handleRowNavigationKey applies the navigation keys every read-only row view
// shares. Views with a column cursor use ←/→/Home/End within the row; views
// without one use Home/End for the document's start and end and leave ←/→
// unhandled. The caller's own switch handles everything else.
func handleRowNavigationKey(key string, c rowCursor, span rowSpan) (rowCursor, bool) {
	hasColumn := span.rowWidth != nil
	switch key {
	case "up":
		return c.moved(-1, span), true
	case "down":
		return c.moved(1, span), true
	case "pgup":
		return c.paged(-1, span), true
	case "pgdown":
		return c.paged(1, span), true
	case "left":
		if hasColumn {
			c.column = max(0, c.column-1)
			return c, true
		}
	case "right":
		if hasColumn {
			c.column = min(c.column+1, span.rowWidth(c.row))
			return c, true
		}
	case "home":
		if hasColumn {
			c.column = 0
			return c, true
		}
		return c.toStart(), true
	case "end":
		if hasColumn {
			c.column = span.rowWidth(c.row)
			return c, true
		}
		return c.toEnd(span), true
	}
	return c, false
}

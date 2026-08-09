package app

import (
	"strconv"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/input"
)

// sidebarWidth is the left pane's rendered width in terminal columns — the
// single source of truth shared by View() and the mouse hit-testing, so the
// click math can't drift from the layout.
func (m Model) sidebarWidth() int {
	return input.Clamp(m.width/4, 16, max(16, m.width-24))
}

// editClickTarget maps a terminal cell (x, y) to a buffer position in edit
// mode, mirroring View()/renderFile()'s layout math: header row, pane border,
// tab strip, sidebar, line-number gutter, the viewport follow, and the cursor
// line's horizontal window. ok=false when the click lands outside the file
// content area (sidebar, chrome, below EOF).
func (m Model) editClickTarget(x, y int) (int, int, bool) {
	height := m.contentHeight() + 1 // rendered file rows in edit mode (single-line status)
	total := len(m.edit.lines)
	if total == 0 {
		return 0, 0, false
	}

	// Rows: header(1) + pane top border(1) + tab strip rows above the content.
	paneRow := y - 2 - m.tabRows()
	if paneRow < 0 || paneRow >= height {
		return 0, 0, false
	}
	start := fileViewportTop(m.edit.cy, m.fileScrollY, height, total)
	line := start + paneRow
	if line >= total {
		return 0, 0, false // pane background below the last line
	}

	// Columns: sidebar + pane left border, then the gutter ("NN │ ").
	sidebar := m.sidebarWidth()
	mainWidth := max(3, m.width-sidebar)
	paneCol := x - (sidebar + 1)
	if paneCol < 0 || paneCol >= mainWidth-2 {
		return 0, 0, false
	}
	gutterWidth := len(strconv.Itoa(max(total, height)))
	contentCol := max(0, paneCol-(gutterWidth+3)) // gutter click → column 0

	// Only the cursor line renders with a horizontal window (renderEditLine);
	// every other line starts at column 0 in edit mode.
	off := 0
	if line == m.edit.cy {
		contentWidth := max(1, (mainWidth-4)-gutterWidth-3)
		if at := input.Clamp(m.edit.cx, 0, len([]rune(m.edit.lines[line]))); at >= contentWidth {
			off = at - contentWidth + 1
		}
	}
	col := input.Clamp(off+contentCol, 0, len([]rune(m.edit.lines[line])))
	return line, col, true
}

// wheelScrollLines is how many lines one wheel notch moves.
const wheelScrollLines = 3

// handleMouse routes mouse input: a left-click press places the cursor, the
// vertical wheel scrolls (moving the cursor in edit mode, since the viewport
// follows it). Every other event — horizontal wheel, other buttons, drag,
// release — is intentionally ignored so a trackpad swipe never moves the
// cursor or types anything.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// Overlays own their own navigation.
	if m.fuzzyOpen || m.messageOverlay != "" || m.defPickOpen || m.grepOpen || m.confirmRm != "" {
		return m, nil
	}
	// Only clicks and wheel notches act; motion (drag) and release messages
	// fall through untouched so a trackpad swipe never moves the cursor.
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		mo := msg.Mouse()
		switch mo.Button {
		case tea.MouseLeft, tea.MouseRight:
			ctrl := mo.Mod.Contains(tea.ModCtrl)
			// Bare right-click (no Ctrl) is reserved — nothing yet. It's only
			// handled as a safety net for terminals that map a physical
			// Ctrl+click to the right button while still forwarding the Ctrl
			// modifier.
			if mo.Button == tea.MouseRight && !ctrl {
				return m, nil
			}
			if m.mode == modeDiff {
				next, hit := m.handleDiffClick(mo.X, mo.Y)
				if hit && ctrl {
					return next.diffJumpToReference() // Ctrl+click = jump to definition
				}
				return next, nil
			}
			if m.mode == modeBlame {
				next, hit := m.handleBlameClick(mo.X, mo.Y)
				if hit && ctrl {
					return next.blameJumpToReference() // Ctrl+click = jump to definition
				}
				return next, nil
			}
			if m.mode == modeConflict {
				// Plain cursor placement only (no Ctrl+click jump): the click
				// math is edit mode's, plus a popup realign for the new line.
				next, _ := m.handleEditClick(mo.X, mo.Y)
				return next.syncConflictChoice(), nil
			}
			next, hit := m.handleEditClick(mo.X, mo.Y)
			if hit && ctrl {
				return next.jumpToReference() // Ctrl+click = jump to definition
			}
			return next, nil
		}
	case tea.MouseWheelMsg:
		switch msg.Mouse().Button {
		case tea.MouseWheelUp:
			return m.wheelScroll(-1), nil
		case tea.MouseWheelDown:
			return m.wheelScroll(1), nil
		}
	}
	return m, nil
}

// handleEditClick moves the edit cursor to a clicked position in the file pane.
// hit=false when the click misses the file content area (or we're not in edit
// mode), so callers can tell a real landing from a no-op.
//
// The viewport is anchored explicitly so a click never drags the view along
// with the cursor: an ordinary click freezes the window where it was, while
// clicking the top visible line pages up (that line re-renders at the bottom)
// and clicking the bottom visible line pages down (it re-renders at the top).
func (m Model) handleEditClick(x, y int) (Model, bool) {
	if (m.mode != modeEdit && m.mode != modeConflict) || m.openFile == nil {
		return m, false
	}
	line, col, ok := m.editClickTarget(x, y)
	if !ok {
		return m, false
	}
	h := m.contentHeight() + 1
	total := len(m.edit.lines)
	top := fileViewportTop(m.edit.cy, m.fileScrollY, h, total) // window rendered when the user clicked
	bottom := min(top+h-1, total-1)
	switch {
	case line == top && top > 0:
		m.fileScrollY = max(0, line-h+1) // clicked top row → page up, line lands at the bottom
	case line == bottom && bottom < total-1:
		m.fileScrollY = line // clicked bottom row → page down, line lands at the top
	default:
		m.fileScrollY = top // ordinary click: the viewport stays put
	}
	if line != m.edit.cy {
		m = m.flushBurst() // undo boundary on line change, like moveEditCursor
	}
	m.edit.clearSelection()
	m.edit.cy, m.edit.cx = line, col
	m.edit.clampCursor()
	return m.sigCheckCursor(), true
}

// diffClickTarget maps a terminal cell (x, y) to a diff display row and rune
// column — editClickTarget's math over diffRows instead of buffer lines: same
// chrome offsets, same gutter formula as renderDiff, and the horizontal
// window applies only to the review cursor's row.
func (m Model) diffClickTarget(x, y int) (int, int, bool) {
	height := m.contentHeight() + 1 // rendered rows: single-line status, like edit mode
	total := len(m.diffRows)
	if total == 0 {
		return 0, 0, false
	}

	paneRow := y - 2 - m.tabRows()
	if paneRow < 0 || paneRow >= height {
		return 0, 0, false
	}
	start := fileViewportTop(m.diffCursor, m.diffScrollY, height, total)
	row := start + paneRow
	if row >= total {
		return 0, 0, false // pane background below the last row
	}

	sidebar := m.sidebarWidth()
	mainWidth := max(3, m.width-sidebar)
	paneCol := x - (sidebar + 1)
	if paneCol < 0 || paneCol >= mainWidth-2 {
		return 0, 0, false
	}
	gutterWidth := len(strconv.Itoa(max(len(m.edit.lines), height)))
	contentCol := max(0, paneCol-(gutterWidth+3)) // gutter click → column 0

	text := []rune(m.diffRowText(row))
	off := 0
	if row == m.diffCursor {
		contentWidth := max(1, (mainWidth-4)-gutterWidth-3)
		if at := input.Clamp(m.diffCx, 0, len(text)); at >= contentWidth {
			off = at - contentWidth + 1
		}
	}
	col := input.Clamp(off+contentCol, 0, len(text))
	return row, col, true
}

// handleDiffClick moves the review cursor to a clicked diff row, with
// handleEditClick's viewport anchoring: an ordinary click freezes the window,
// clicking the top visible row pages up, the bottom visible row pages down.
func (m Model) handleDiffClick(x, y int) (Model, bool) {
	if m.mode != modeDiff || m.openFile == nil {
		return m, false
	}
	row, col, ok := m.diffClickTarget(x, y)
	if !ok {
		return m, false
	}
	h := m.contentHeight() + 1
	total := len(m.diffRows)
	top := fileViewportTop(m.diffCursor, m.diffScrollY, h, total)
	bottom := min(top+h-1, total-1)
	switch {
	case row == top && top > 0:
		m.diffScrollY = max(0, row-h+1) // clicked top row → page up, row lands at the bottom
	case row == bottom && bottom < total-1:
		m.diffScrollY = row // clicked bottom row → page down, row lands at the top
	default:
		m.diffScrollY = top // ordinary click: the viewport stays put
	}
	m.diffCursor = row
	m.diffCx = col
	return m, true
}

// blameClickTarget maps a terminal cell (x, y) to a blame row and rune
// column — diffClickTarget's math with the author+date gutter instead of
// line numbers, and rows are buffer lines directly.
func (m Model) blameClickTarget(x, y int) (int, int, bool) {
	height := m.contentHeight() + 1 // rendered rows: single-line status, like edit mode
	total := len(m.blameRows)
	if total == 0 {
		return 0, 0, false
	}

	paneRow := y - 2 - m.tabRows()
	if paneRow < 0 || paneRow >= height {
		return 0, 0, false
	}
	start := fileViewportTop(m.blameCursor, m.blameScrollY, height, total)
	row := start + paneRow
	if row >= total {
		return 0, 0, false // pane background below the last row
	}

	sidebar := m.sidebarWidth()
	mainWidth := max(3, m.width-sidebar)
	paneCol := x - (sidebar + 1)
	if paneCol < 0 || paneCol >= mainWidth-2 {
		return 0, 0, false
	}
	gutterWidth := m.blameGutterWidth()
	contentCol := max(0, paneCol-(gutterWidth+3)) // gutter click → column 0

	text := []rune(m.blameRowText(row))
	off := 0
	if row == m.blameCursor {
		contentWidth := max(1, (mainWidth-4)-gutterWidth-3)
		if at := input.Clamp(m.blameCx, 0, len(text)); at >= contentWidth {
			off = at - contentWidth + 1
		}
	}
	col := input.Clamp(off+contentCol, 0, len(text))
	return row, col, true
}

// handleBlameClick moves the blame cursor to a clicked row, with
// handleEditClick's viewport anchoring: an ordinary click freezes the window,
// clicking the top visible row pages up, the bottom visible row pages down.
func (m Model) handleBlameClick(x, y int) (Model, bool) {
	if m.mode != modeBlame || m.openFile == nil {
		return m, false
	}
	row, col, ok := m.blameClickTarget(x, y)
	if !ok {
		return m, false
	}
	h := m.contentHeight() + 1
	total := len(m.blameRows)
	top := fileViewportTop(m.blameCursor, m.blameScrollY, h, total)
	bottom := min(top+h-1, total-1)
	switch {
	case row == top && top > 0:
		m.blameScrollY = max(0, row-h+1) // clicked top row → page up, row lands at the bottom
	case row == bottom && bottom < total-1:
		m.blameScrollY = row // clicked bottom row → page down, row lands at the top
	default:
		m.blameScrollY = top // ordinary click: the viewport stays put
	}
	m.blameCursor = row
	m.blameCx = col
	return m, true
}

// wheelScroll handles a vertical wheel notch (dir = -1 up, +1 down). In edit
// and diff-review modes it moves the cursor (the viewport follows via
// fileViewportTop); in the query/command file view it nudges the scroll
// offset. Other modes are inert.
func (m Model) wheelScroll(dir int) Model {
	switch {
	case m.mode == modeEdit && m.openFile != nil:
		return m.moveEditCursor(0, dir*wheelScrollLines)
	case m.mode == modeDiff && m.openFile != nil:
		return m.moveDiffCursor(dir * wheelScrollLines)
	case m.mode == modeBlame && m.openFile != nil:
		return m.moveBlameCursor(dir * wheelScrollLines)
	case (m.mode == modeOpenAPI || m.mode == modeGraphQL) && m.openFile != nil:
		return m.movePreviewCursor(dir * wheelScrollLines)
	case m.mode == modeConflict && m.openFile != nil:
		return m.moveConflictCursor(0, dir*wheelScrollLines)
	case (m.mode == modeQuery || m.mode == modeCommand) && m.openFile != nil:
		m.fileScrollY = input.Clamp(m.fileScrollY+dir*wheelScrollLines, 0, max(0, len(m.fileLines)-1))
	}
	return m
}

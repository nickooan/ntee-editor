package app

import (
	"strconv"

	tea "github.com/charmbracelet/bubbletea"

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
	height := m.contentHeight()
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
	if m.fuzzyOpen || m.messageOverlay != "" || m.defPickOpen || m.grepOpen {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonLeft:
		if msg.Action == tea.MouseActionPress { // ignore drag-motion / release
			return m.handleEditClick(msg), nil
		}
	case tea.MouseButtonWheelUp:
		return m.wheelScroll(-1), nil
	case tea.MouseButtonWheelDown:
		return m.wheelScroll(1), nil
	}
	return m, nil
}

// handleEditClick moves the edit cursor to a left-clicked position in the file
// pane. A click outside the file content area (or outside edit mode) is a
// no-op.
func (m Model) handleEditClick(msg tea.MouseMsg) Model {
	if m.mode != modeEdit || m.openFile == nil {
		return m
	}
	line, col, ok := m.editClickTarget(msg.X, msg.Y)
	if !ok {
		return m
	}
	if line != m.edit.cy {
		m = m.flushBurst() // undo boundary on line change, like moveEditCursor
	}
	m.edit.clearSelection()
	m.edit.cy, m.edit.cx = line, col
	m.edit.clampCursor()
	return m
}

// wheelScroll handles a vertical wheel notch (dir = -1 up, +1 down). In edit
// mode it moves the cursor (the viewport follows via fileViewportTop); in the
// query/command file view it nudges the scroll offset. Other modes are inert.
func (m Model) wheelScroll(dir int) Model {
	switch {
	case m.mode == modeEdit && m.openFile != nil:
		return m.moveEditCursor(0, dir*wheelScrollLines)
	case (m.mode == modeQuery || m.mode == modeCommand) && m.openFile != nil:
		m.fileScrollY = input.Clamp(m.fileScrollY+dir*wheelScrollLines, 0, max(0, len(m.fileLines)-1))
	}
	return m
}

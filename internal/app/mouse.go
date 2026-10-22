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

// statusRowCount is how many rows renderStatusLine produces — only the query
// bar carries a second (hint) row. bodyHeight derives from it, so a new status
// row must land here too (a pinned test guards the agreement).
func (m Model) statusRowCount() int {
	if m.mode == modeQuery {
		return 2
	}
	return 1
}

// bodyHeight is the panes' row count including their borders: terminal height
// minus the header and status rows. Shared by render() and the hit-testing.
func (m Model) bodyHeight() int {
	return max(3, m.height-1-m.statusRowCount())
}

// sidebarInnerHeight is the number of tree rows the sidebar body shows.
func (m Model) sidebarInnerHeight() int {
	return m.bodyHeight() - 2
}

// overlayOpen reports whether a whole-pane overlay owns the screen — overlays
// run their own navigation, so mouse routing and the tab strip both yield.
func (m Model) overlayOpen() bool {
	return m.fuzzyOpen || m.messageOverlay != "" || m.defPickOpen || m.grepOpen || m.choice.open() || m.opMode.open
}

// tabStripVisible mirrors render()'s decision to draw the tab strip.
func (m Model) tabStripVisible() bool {
	return len(m.tabs) > 0 && !m.overlayOpen() && m.mode != modeInspect && !m.inListDetailView()
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
	if m.opMode.open {
		return m.handleOpMouse(msg)
	}
	// Overlays own their own navigation.
	if m.overlayOpen() {
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
			if next, handled := m.handleTabStripClick(mo.X, mo.Y); handled {
				return next, nil
			}
			if next, handled := m.handleSidebarClick(mo.X, mo.Y); handled {
				return next, nil
			}
			if m.mode == modeDiff {
				next, hit := m.handleDiffClick(mo.X, mo.Y)
				if hit && ctrl {
					return next.diffJumpToReference() // Ctrl+click = jump to definition
				}
				return next, nil
			}
			if m.mode == modeReview {
				next, _ := m.handleReviewClick(mo.X, mo.Y)
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
		m.fileScrollY = max(0, line-h+1)
	case line == bottom && bottom < total-1:
		m.fileScrollY = line
	default:
		m.fileScrollY = top
	}
	if line != m.edit.cy {
		m = m.flushBurst() // undo boundary on line change, like moveEditCursor
	}
	m.edit.clearSelection()
	m.edit.cy, m.edit.cx = line, col
	m.edit.clampCursor()
	return m.sigCheckCursor(), true
}

// rowViewClickTarget maps a terminal cell (x, y) to a row and rune column of
// a read-only row view in the main pane — editClickTarget's math over the
// view's rows: same chrome offsets, a gutter of gutterWidth+3 columns, and
// topRows lines (tab strip, view header) drawn above the rows. The horizontal
// window applies only to the cursor's row, like the renderers.
func (m Model) rowViewClickTarget(x, y int, position rowCursor, span rowSpan, gutterWidth, topRows int) (int, int, bool) {
	if span.total == 0 {
		return 0, 0, false
	}
	paneRow := y - 2 - topRows
	if paneRow < 0 || paneRow >= span.height {
		return 0, 0, false
	}
	row := position.viewportTop(span) + paneRow
	if row >= span.total {
		return 0, 0, false // pane background below the last row
	}

	sidebar := m.sidebarWidth()
	mainWidth := max(3, m.width-sidebar)
	paneCol := x - (sidebar + 1)
	if paneCol < 0 || paneCol >= mainWidth-2 {
		return 0, 0, false
	}
	if span.rowWidth == nil {
		return row, 0, true
	}
	contentCol := max(0, paneCol-(gutterWidth+3)) // gutter click → column 0

	width := span.rowWidth(row)
	off := 0
	if row == position.row {
		contentWidth := max(1, (mainWidth-4)-gutterWidth-3)
		if at := input.Clamp(position.column, 0, width); at >= contentWidth {
			off = at - contentWidth + 1
		}
	}
	return row, input.Clamp(off+contentCol, 0, width), true
}

// clickedAt moves the cursor to a clicked row and column with
// handleEditClick's viewport anchoring: an ordinary click freezes the window,
// clicking the top visible row pages up, the bottom visible row pages down.
func (c rowCursor) clickedAt(row, column int, span rowSpan) rowCursor {
	top := c.viewportTop(span)
	bottom := min(top+span.height-1, span.total-1)
	switch {
	case row == top && top > 0:
		c.scrollY = max(0, row-span.height+1)
	case row == bottom && bottom < span.total-1:
		c.scrollY = row
	default:
		c.scrollY = top
	}
	c.row, c.column = row, column
	return c
}

// handleDiffClick moves the review cursor to a clicked diff row.
func (m Model) handleDiffClick(x, y int) (Model, bool) {
	if m.mode != modeDiff || m.openFile == nil {
		return m, false
	}
	span := m.diffSpan()
	gutterWidth := diffGutterWidth(m.liveDiffDocument(), span.height)
	row, column, ok := m.rowViewClickTarget(x, y, m.diffPosition, span, gutterWidth, m.tabRows())
	if !ok {
		return m, false
	}
	m.diffPosition = m.diffPosition.clickedAt(row, column, span)
	return m, true
}

// handleBlameClick moves the blame cursor to a clicked row; the gutter is the
// author+date annotation instead of line numbers.
func (m Model) handleBlameClick(x, y int) (Model, bool) {
	if m.mode != modeBlame || m.openFile == nil {
		return m, false
	}
	span := m.blameSpan()
	row, column, ok := m.rowViewClickTarget(x, y, m.blamePosition, span, m.blameGutterWidth(), m.tabRows())
	if !ok {
		return m, false
	}
	m.blamePosition = m.blamePosition.clickedAt(row, column, span)
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
		m.diffPosition = m.diffPosition.moved(dir*wheelScrollLines, m.diffSpan())
	case m.mode == modeBlame && m.openFile != nil:
		m.blamePosition = m.blamePosition.moved(dir*wheelScrollLines, m.blameSpan())
	case (m.mode == modeOpenAPI || m.mode == modeGraphQL) && m.openFile != nil:
		m.preview.position = m.preview.position.moved(dir*wheelScrollLines, m.previewSpan())
		return m.syncPreviewSel()
	case m.mode == modeReview:
		m.review.position = m.review.position.moved(dir*wheelScrollLines, m.reviewSpan())
	case m.mode == modeExternalHistory:
		m.externalHistory.selection = m.externalHistory.selection.moved(dir, len(m.externalHistory.records))
	case m.mode == modeConflict && m.openFile != nil:
		return m.moveConflictCursor(0, dir*wheelScrollLines)
	case (m.mode == modeQuery || m.mode == modeCommand) && m.openFile != nil:
		m.fileScrollY = input.Clamp(m.fileScrollY+dir*wheelScrollLines, 0, max(0, len(m.fileLines)-1))
	}
	return m
}

// handleSidebarClick resolves a click to a sidebar row and activates it.
// A miss (including modes that draw the file tree but do not act on it)
// falls through so the main pane can handle the click.
func (m Model) handleSidebarClick(x, y int) (Model, bool) {
	rowIndex, ok := m.sidebarListClickIndex(x, y)
	if !ok {
		return m, false
	}
	return m.activateSidebarRow(rowIndex)
}

// tabClickTarget maps a cell on the tab-strip row to a tab index, mirroring
// renderTabStrip via tabStripWindow: strip row y=2, cells left to right from
// the window start; the trailing fill and never-drawn overflow are misses.
func (m Model) tabClickTarget(x, y int) (int, bool) {
	if !m.tabStripVisible() || y != 2 {
		return 0, false
	}
	width := max(3, m.width-m.sidebarWidth()) - 4
	col := x - (m.sidebarWidth() + 1)
	if col < 0 || col >= width {
		return 0, false
	}
	start, widths := m.tabStripWindow(width)
	used := 0
	for i := start; i < len(m.tabs); i++ {
		if used+widths[i] > width {
			break
		}
		if col < used+widths[i] {
			return i, true
		}
		used += widths[i]
	}
	return 0, false
}

// handleTabStripClick switches to a clicked tab. activateTab stashes the
// outgoing buffer's draft and restores the incoming one, so an unsaved tab
// stays red and its edits survive the switch.
func (m Model) handleTabStripClick(x, y int) (Model, bool) {
	i, ok := m.tabClickTarget(x, y)
	if !ok {
		return m, false
	}
	// Already editing the clicked tab: swallow the click rather than churn
	// through a stash/restore round trip (and a redundant LSP DidOpen).
	if i == m.tabActive && m.tabs[i] == m.openRel && m.mode == modeEdit {
		return m, true
	}
	return m.activateTab(i), true
}

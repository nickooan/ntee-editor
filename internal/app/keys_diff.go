package app

import (
	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/input"
)

// handleDiffKey drives diff review mode. The switch is closed on purpose:
// any key not listed falls through inert, which is what makes the mode
// read-only — typing, enter, backspace, ctrl+s/z/… never reach the buffer.
func (m Model) handleDiffKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		m.mode = modeQuery
		return m.clearDiffState(), nil
	}

	switch msg.String() {
	case "esc":
		return m.exitDiff(), nil

	case "up":
		return m.moveDiffCursor(-1), nil
	case "down":
		return m.moveDiffCursor(1), nil

	case "left":
		m.diffCx = max(0, m.diffCx-1)
		return m, nil
	case "right":
		m.diffCx = min(m.diffCx+1, len([]rune(m.diffRowText(m.diffCursor))))
		return m, nil
	case "home":
		m.diffCx = 0
		return m, nil
	case "end":
		m.diffCx = len([]rune(m.diffRowText(m.diffCursor)))
		return m, nil

	case "pgup":
		return m.pageDiff(-1), nil
	case "pgdown":
		return m.pageDiff(1), nil

	case "ctrl+j":
		return m.diffJumpToReference()

	case "ctrl+o":
		return m.jumpBack()
	}
	return m, nil
}

// exitDiff returns to edit mode at the reviewed position: the cursor row maps
// to its buffer line and keeps its on-screen row, so the page doesn't shift
// even when del rows above it compress away. Safe while the diff is still
// loading (empty rows map to the unchanged edit cursor).
func (m Model) exitDiff() Model {
	if len(m.diffRows) > 0 {
		h := m.contentHeight() + 1
		total := len(m.diffRows)
		screenRow := m.diffCursor - fileViewportTop(m.diffCursor, m.diffScrollY, h, total)
		m.edit.cy = diffRowBufLine(m.diffRows, m.diffCursor)
		m.edit.cx = m.diffCx
		m.edit.clampCursor()
		m.fileScrollY = input.Clamp(m.edit.cy-screenRow, 0, max(0, len(m.edit.lines)-h))
	}
	m.mode = modeEdit
	return m.clearDiffState()
}

// moveDiffCursor moves the review cursor by dy rows, keeping the column
// within the new row's text.
func (m Model) moveDiffCursor(dy int) Model {
	if len(m.diffRows) == 0 {
		return m // still loading
	}
	m.diffCursor = input.Clamp(m.diffCursor+dy, 0, len(m.diffRows)-1)
	m.diffCx = input.Clamp(m.diffCx, 0, len([]rune(m.diffRowText(m.diffCursor))))
	return m
}

// pageDiff pages the review view with a one-line overlap, cursor keeping its
// on-screen row — the diff-mode mirror of pageEdit.
func (m Model) pageDiff(dir int) Model {
	total := len(m.diffRows)
	if total == 0 {
		return m
	}
	h := m.contentHeight() + 1
	step := max(1, h-1)
	top := fileViewportTop(m.diffCursor, m.diffScrollY, h, total)
	m.diffCursor = input.Clamp(m.diffCursor+dir*step, 0, total-1)
	m.diffCx = input.Clamp(m.diffCx, 0, len([]rune(m.diffRowText(m.diffCursor))))
	m.diffScrollY = input.Clamp(top+dir*step, 0, max(0, total-h))
	return m
}

// diffJumpToReference is Ctrl+J from a diff row: sync the edit cursor to the
// row's buffer position and reuse the whole jump ladder (which reads m.edit).
// Del rows refuse — their text no longer exists in the buffer, so an LSP
// position on the successor line would silently query the wrong identifier.
func (m Model) diffJumpToReference() (tea.Model, tea.Cmd) {
	if len(m.diffRows) == 0 {
		return m, nil // still loading
	}
	row := m.diffRows[input.Clamp(m.diffCursor, 0, len(m.diffRows)-1)]
	if row.kind == diffDel {
		m.errText = "cannot jump from a removed line"
		return m, nil
	}
	m.edit.cy = input.Clamp(row.bufLine, 0, len(m.edit.lines)-1)
	m.edit.cx = m.diffCx
	m.edit.clampCursor()
	return m.jumpToReference()
}

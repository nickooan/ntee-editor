package app

import (
	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/keymap"
)

// handleDiffKey drives diff review mode. The switch is closed on purpose:
// any key not listed falls through inert, which is what makes the mode
// read-only — typing, enter, backspace, ctrl+s/z/… never reach the buffer.
func (m Model) handleDiffKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		m.mode = modeQuery
		return m.clearDiffState(), nil
	}

	k := msg.String()
	switch {
	case m.keys.Matches(keymap.JumpToReference, k):
		return m.diffJumpToReference()
	case m.keys.Matches(keymap.JumpBack, k):
		return m.jumpBack()
	}
	span := m.diffSpan()
	if position, handled := handleRowNavigationKey(k, m.diffPosition, span); handled {
		m.diffPosition = position
		return m, nil
	}
	switch k {
	case "esc":
		return m.exitDiff(), nil
	case "shift+up":
		m.diffPosition = m.diffPosition.blockJump(-1, span, m.diffDocument.isHunkStart)
	case "shift+down":
		m.diffPosition = m.diffPosition.blockJump(1, span, m.diffDocument.isHunkStart)
	}
	return m, nil
}

// exitDiff returns to edit mode at the reviewed position: the cursor row maps
// to its buffer line and keeps its on-screen row, so the page doesn't shift
// even when del rows above it compress away. Safe while the diff is still
// loading (empty rows map to the unchanged edit cursor).
func (m Model) exitDiff() Model {
	if len(m.diffDocument.rows) > 0 {
		h := m.contentHeight() + 1
		screenRow := m.diffPosition.screenRow(m.diffSpan())
		m.edit.cy = diffRowBufLine(m.diffDocument.rows, m.diffPosition.row)
		m.edit.cx = m.diffPosition.column
		m.edit.clampCursor()
		m.fileScrollY = input.Clamp(m.edit.cy-screenRow, 0, max(0, len(m.edit.lines)-h))
	}
	m.mode = modeEdit
	return m.clearDiffState()
}

// diffJumpToReference is Ctrl+J from a diff row: sync the edit cursor to the
// row's buffer position and reuse the whole jump ladder (which reads m.edit).
// Del rows refuse — their text no longer exists in the buffer, so an LSP
// position on the successor line would silently query the wrong identifier.
func (m Model) diffJumpToReference() (tea.Model, tea.Cmd) {
	if len(m.diffDocument.rows) == 0 {
		return m, nil // still loading
	}
	row := m.diffDocument.rows[input.Clamp(m.diffPosition.row, 0, len(m.diffDocument.rows)-1)]
	if row.kind == diffDel {
		m.errText = "cannot jump from a removed line"
		return m, nil
	}
	m.edit.cy = input.Clamp(row.bufLine, 0, len(m.edit.lines)-1)
	m.edit.cx = m.diffPosition.column
	m.edit.clampCursor()
	return m.jumpToReference()
}

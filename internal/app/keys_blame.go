package app

import (
	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/keymap"
)

// handleBlameKey drives blame mode. The switch is closed on purpose: any key
// not listed falls through inert, which is what makes the mode read-only —
// typing, enter, backspace, ctrl+s/z/… never reach the buffer.
func (m Model) handleBlameKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		m.mode = modeQuery
		return m.clearBlameState(), nil
	}

	k := msg.String()
	switch {
	case m.keys.Matches(keymap.JumpToReference, k):
		return m.blameJumpToReference()
	case m.keys.Matches(keymap.JumpBack, k):
		return m.jumpBack()
	}
	span := m.blameSpan()
	if position, handled := handleRowNavigationKey(k, m.blamePosition, span); handled {
		m.blamePosition = position
		return m, nil
	}
	switch k {
	case "esc":
		return m.exitBlame(), nil
	case "shift+up":
		m.blamePosition = m.blamePosition.blockJump(-1, span, m.isBlameGroupStart)
	case "shift+down":
		m.blamePosition = m.blamePosition.blockJump(1, span, m.isBlameGroupStart)
	}
	return m, nil
}

// blameSpan is the blame view's cursor span: rows are buffer lines, drawn
// below a single-line status like edit mode.
func (m Model) blameSpan() rowSpan {
	return rowSpan{
		total:    len(m.blameRows),
		height:   m.contentHeight() + 1,
		rowWidth: func(row int) int { return len([]rune(m.blameRowText(row))) },
	}
}

// isBlameGroupStart reports whether row opens a commit group: a maximal run
// of rows from the same commit.
func (m Model) isBlameGroupStart(row int) bool {
	return row == 0 || m.blameRows[row-1].group != m.blameRows[row].group
}

// exitBlame returns to edit mode at the annotated position: rows map 1:1 to
// buffer lines, so the cursor carries over directly and keeps its on-screen
// row. Safe while the blame is still loading (empty rows leave the unchanged
// edit cursor alone).
func (m Model) exitBlame() Model {
	if len(m.blameRows) > 0 {
		h := m.contentHeight() + 1
		screenRow := m.blamePosition.screenRow(m.blameSpan())
		m.edit.cy = input.Clamp(m.blamePosition.row, 0, len(m.edit.lines)-1)
		m.edit.cx = m.blamePosition.column
		m.edit.clampCursor()
		m.fileScrollY = input.Clamp(m.edit.cy-screenRow, 0, max(0, len(m.edit.lines)-h))
	}
	m.mode = modeEdit
	return m.clearBlameState()
}

// blameJumpToReference is Ctrl+J from a blame row: sync the edit cursor to
// the row's buffer position (the identity mapping) and reuse the whole jump
// ladder, which reads m.edit.
func (m Model) blameJumpToReference() (tea.Model, tea.Cmd) {
	if len(m.blameRows) == 0 {
		return m, nil // still loading
	}
	m.edit.cy = input.Clamp(m.blamePosition.row, 0, len(m.edit.lines)-1)
	m.edit.cx = m.blamePosition.column
	m.edit.clampCursor()
	return m.jumpToReference()
}

package app

import (
	tea "charm.land/bubbletea/v2"
)

// handleConflictKey drives conflict-solving mode. The switch is closed on
// purpose: any key not listed falls through inert, so typing, backspace,
// ctrl+s/z/… never reach the buffer — the only edit path is Enter on a
// marker line (applyConflictChoice).
func (m Model) handleConflictKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		m.mode = modeQuery
		return m.clearConflictState(), nil
	}

	switch msg.String() {
	case "esc":
		return m.exitConflict(), nil

	case "up":
		return m.moveConflictCursor(0, -1), nil
	case "down":
		return m.moveConflictCursor(0, 1), nil

	case "left":
		// On a marker line the popup owns ←/→ (cycle the option); elsewhere
		// they move the column like edit mode.
		if _, ok := m.conflictBlockOnMarker(m.edit.cy); ok {
			m.conflictChoice = (m.conflictChoice + 2) % 3
			return m, nil
		}
		return m.moveConflictCursor(-1, 0), nil
	case "right":
		if _, ok := m.conflictBlockOnMarker(m.edit.cy); ok {
			m.conflictChoice = (m.conflictChoice + 1) % 3
			return m, nil
		}
		return m.moveConflictCursor(1, 0), nil

	case "shift+up":
		return m.jumpConflictBlock(-1), nil
	case "shift+down":
		return m.jumpConflictBlock(1), nil

	case "enter":
		return m.applyConflictChoice(), nil

	case "home":
		m.edit.cx = 0
		return m, nil
	case "end":
		m.edit.cx = len(m.edit.line())
		return m, nil

	case "pgup":
		return m.pageEdit(-1).syncConflictChoice(), nil
	case "pgdown":
		return m.pageEdit(1).syncConflictChoice(), nil
	}
	return m, nil
}

// exitConflict returns to edit mode. The cursor, scroll, and every applied
// resolution already live in the edit session — nothing to map back;
// unresolved conflicts simply stay in the buffer as text.
func (m Model) exitConflict() Model {
	m.mode = modeEdit
	return m.clearConflictState()
}

// moveConflictCursor moves the buffer cursor like edit mode (viewport follows
// via fileViewportTop) and realigns the popup with the new line.
func (m Model) moveConflictCursor(dx, dy int) Model {
	return m.moveEditCursor(dx, dy).syncConflictChoice()
}

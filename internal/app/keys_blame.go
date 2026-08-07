package app

import (
	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/input"
)

// handleBlameKey drives blame mode. The switch is closed on purpose: any key
// not listed falls through inert, which is what makes the mode read-only —
// typing, enter, backspace, ctrl+s/z/… never reach the buffer.
func (m Model) handleBlameKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		m.mode = modeQuery
		return m.clearBlameState(), nil
	}

	switch msg.String() {
	case "esc":
		return m.exitBlame(), nil

	case "up":
		return m.moveBlameCursor(-1), nil
	case "down":
		return m.moveBlameCursor(1), nil

	case "shift+up":
		return m.jumpBlameGroup(-1), nil
	case "shift+down":
		return m.jumpBlameGroup(1), nil

	case "left":
		m.blameCx = max(0, m.blameCx-1)
		return m, nil
	case "right":
		m.blameCx = min(m.blameCx+1, len([]rune(m.blameRowText(m.blameCursor))))
		return m, nil
	case "home":
		m.blameCx = 0
		return m, nil
	case "end":
		m.blameCx = len([]rune(m.blameRowText(m.blameCursor)))
		return m, nil

	case "pgup":
		return m.pageBlame(-1), nil
	case "pgdown":
		return m.pageBlame(1), nil

	case "ctrl+j":
		return m.blameJumpToReference()

	case "ctrl+o":
		return m.jumpBack()
	}
	return m, nil
}

// exitBlame returns to edit mode at the annotated position: rows map 1:1 to
// buffer lines, so the cursor carries over directly and keeps its on-screen
// row. Safe while the blame is still loading (empty rows leave the unchanged
// edit cursor alone).
func (m Model) exitBlame() Model {
	if len(m.blameRows) > 0 {
		h := m.contentHeight() + 1
		total := len(m.blameRows)
		screenRow := m.blameCursor - fileViewportTop(m.blameCursor, m.blameScrollY, h, total)
		m.edit.cy = input.Clamp(m.blameCursor, 0, len(m.edit.lines)-1)
		m.edit.cx = m.blameCx
		m.edit.clampCursor()
		m.fileScrollY = input.Clamp(m.edit.cy-screenRow, 0, max(0, len(m.edit.lines)-h))
	}
	m.mode = modeEdit
	return m.clearBlameState()
}

// moveBlameCursor moves the annotation cursor by dy rows, keeping the column
// within the new row's text.
func (m Model) moveBlameCursor(dy int) Model {
	if len(m.blameRows) == 0 {
		return m // still loading
	}
	m.blameCursor = input.Clamp(m.blameCursor+dy, 0, len(m.blameRows)-1)
	m.blameCx = input.Clamp(m.blameCx, 0, len([]rune(m.blameRowText(m.blameCursor))))
	return m
}

// jumpBlameGroup moves the cursor to the previous (dir<0) or next (dir>0)
// commit group's first row, anchored ~30% from the top like hunk jumps. A
// group is a maximal run of rows from the same commit. No wraparound; inert
// when there is no group in that direction. From inside a group, jumping up
// lands on that group's own start.
func (m Model) jumpBlameGroup(dir int) Model {
	if len(m.blameRows) == 0 {
		return m // still loading
	}
	target := -1
	for i, row := range m.blameRows {
		if i > 0 && m.blameRows[i-1].group == row.group {
			continue // not a group start
		}
		if dir < 0 {
			if i < m.blameCursor {
				target = i
			}
		} else if i > m.blameCursor {
			target = i
			break
		}
	}
	if target == -1 {
		return m
	}
	m.blameCursor = target
	m.blameCx = input.Clamp(m.blameCx, 0, len([]rune(m.blameRowText(target))))
	m.blameScrollY = anchorScroll(target, m.contentHeight()+1, len(m.blameRows))
	return m
}

// pageBlame pages the view with a one-line overlap, cursor keeping its
// on-screen row — the blame-mode mirror of pageDiff.
func (m Model) pageBlame(dir int) Model {
	total := len(m.blameRows)
	if total == 0 {
		return m
	}
	h := m.contentHeight() + 1
	step := max(1, h-1)
	top := fileViewportTop(m.blameCursor, m.blameScrollY, h, total)
	m.blameCursor = input.Clamp(m.blameCursor+dir*step, 0, total-1)
	m.blameCx = input.Clamp(m.blameCx, 0, len([]rune(m.blameRowText(m.blameCursor))))
	m.blameScrollY = input.Clamp(top+dir*step, 0, max(0, total-h))
	return m
}

// blameJumpToReference is Ctrl+J from a blame row: sync the edit cursor to
// the row's buffer position (the identity mapping) and reuse the whole jump
// ladder, which reads m.edit.
func (m Model) blameJumpToReference() (tea.Model, tea.Cmd) {
	if len(m.blameRows) == 0 {
		return m, nil // still loading
	}
	m.edit.cy = input.Clamp(m.blameCursor, 0, len(m.edit.lines)-1)
	m.edit.cx = m.blameCx
	m.edit.clampCursor()
	return m.jumpToReference()
}

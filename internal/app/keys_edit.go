package app

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/input"
)

// contentHeight approximates the file pane's inner height (header + borders +
// status line), used for paging and match centering.
func (m Model) contentHeight() int {
	return max(1, m.height-5)
}

func (m Model) handleEditKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		m.mode = modeQuery
		return m, nil
	}

	// The completion popup, when open, consumes its navigation/accept/dismiss
	// keys; any other key (except typing/backspace, which manage the popup
	// themselves) closes it and is then processed normally.
	if m.completionOpen {
		if next, cmd, done := m.completionKey(msg); done {
			return next, cmd
		}
		if msg.Type != tea.KeyRunes && msg.Type != tea.KeyBackspace {
			m = m.closeCompletion()
		}
	}

	switch msg.Type {
	case tea.KeyEsc:
		// First Esc clears a selection; the next discards unsaved edits and
		// returns to the query bar (the pane keeps showing the on-disk file).
		if m.edit.sel != nil {
			m.edit.clearSelection()
			return m, nil
		}
		m = m.flushBurst() // checkpoint so the discarded state stays reachable via undo history
		if m.edit.dirty {
			m.notice = "unsaved changes discarded"
		}
		m.mode = modeQuery
		m.jumpStack = nil // quitting edit mode ends the jump trail
		return m.refreshFileHighlights(), nil

	case tea.KeyCtrlS:
		return m.saveEdit(), nil

	case tea.KeyCtrlZ:
		return m.undo(), nil

	case tea.KeyCtrlY:
		return m.redo(), nil

	case tea.KeyCtrlF:
		m = m.flushBurst()
		return m.enterSearch(modeEdit, m.edit.content()), nil

	case tea.KeyCtrlJ:
		return m.jumpToReference()

	case tea.KeyCtrlO:
		return m.jumpBack()

	case tea.KeyCtrlA:
		m.edit.expandSelection()
		return m, nil

	case tea.KeyCtrlE:
		return m.enterExec(), nil

	case tea.KeyUp:
		return m.moveEditCursor(0, -1), nil
	case tea.KeyDown:
		return m.moveEditCursor(0, 1), nil
	case tea.KeyLeft:
		return m.moveEditCursor(-1, 0), nil
	case tea.KeyRight:
		return m.moveEditCursor(1, 0), nil
	case tea.KeyShiftUp:
		m.edit.extendLineSelection(-1)
		return m, nil
	case tea.KeyShiftDown:
		m.edit.extendLineSelection(1)
		return m, nil
	case tea.KeyPgUp:
		return m.pageEdit(-1), nil
	case tea.KeyPgDown:
		return m.pageEdit(1), nil
	case tea.KeyHome:
		m.edit.clearSelection()
		m.edit.cx = 0
		return m.flushBurst(), nil
	case tea.KeyEnd:
		m.edit.clearSelection()
		m.edit.cx = len(m.edit.line())
		return m.flushBurst(), nil

	case tea.KeyEnter:
		cy := m.edit.cy
		m.edit.newline()
		m = m.hlMarkLine(cy)
		m = m.hlInsertLine(cy + 1)
		m.snapDirty = true
		return m.flushBurst(), nil

	case tea.KeyBackspace:
		linesBefore := len(m.edit.lines)
		cyBefore := m.edit.cy
		m.edit.backspace()
		if len(m.edit.lines) < linesBefore {
			m = m.hlRemoveLine(cyBefore)
		}
		m = m.hlMarkLine(m.edit.cy)
		m.snapDirty = true
		return m.afterEditBackspace()

	case tea.KeyDelete:
		if m.edit.deleteSelection() {
			m = m.hlMarkLine(m.edit.cy)
			m.snapDirty = true
			return m, nil
		}
		if m.edit.cx < len(m.edit.line()) {
			m.edit.cx++
			m.edit.backspace()
			m = m.hlMarkLine(m.edit.cy)
			m.snapDirty = true
		}
		return m, nil

	case tea.KeyTab:
		m.edit.insert(strings.Repeat(" ", m.cfg.Editor.TabWidth))
		m = m.hlMarkLine(m.edit.cy)
		m.snapDirty = true
		return m, nil

	case tea.KeySpace:
		m.edit.insert(" ")
		m = m.hlMarkLine(m.edit.cy)
		m.snapDirty = true
		return m.flushBurst(), nil // word boundary → coalesce the burst

	case tea.KeyRunes:
		m.edit.insert(string(msg.Runes))
		m = m.hlMarkLine(m.edit.cy)
		m.snapDirty = true
		return m.afterEditType(string(msg.Runes))
	}
	return m, nil
}

// pageEdit scrolls the file pane by one page with a one-line overlap (the old
// bottom line becomes the new top on PgDown, and the old top becomes the new
// bottom on PgUp), moving the cursor with it so it keeps its on-screen row. dir
// is +1 (down) or -1 (up). It recomputes the current top from the cursor rather
// than trusting m.fileScrollY, which is stale after arrow navigation.
func (m Model) pageEdit(dir int) Model {
	m = m.flushBurst() // a page jump is a burst boundary, like leaving a line
	m.edit.clearSelection()
	total := len(m.edit.lines)
	h := m.contentHeight() + 1 // rendered file rows in edit mode (single-line status)
	step := max(1, h-1)        // a page minus one line → one-line overlap
	top := fileViewportTop(m.edit.cy, m.fileScrollY, h, total)
	m.edit.cy = input.Clamp(m.edit.cy+dir*step, 0, total-1)
	m.edit.clampCursor()
	m.fileScrollY = input.Clamp(top+dir*step, 0, max(0, total-h))
	return m
}

// moveEditCursor moves the cursor; leaving the line is a burst boundary.
func (m Model) moveEditCursor(dx, dy int) Model {
	prevCy := m.edit.cy
	m.edit.move(dx, dy)
	if m.edit.cy != prevCy {
		m = m.flushBurst()
	}
	return m
}

// saveEdit writes the buffer to disk, checkpoints a "save" snapshot, and syncs
// the open-file record. Shared by Ctrl+S and :w.
func (m Model) saveEdit() Model {
	content := m.edit.content()
	if err := filetree.WriteViewFile(m.openFile.Path, content); err != nil {
		m.errText = "save failed: " + err.Error()
		return m
	}
	m.openFile.Content = content
	m.edit.dirty = false
	m = m.pushSnapshot("save")
	if client, ok := m.lsp.ClientFor(m.openFile.Path); ok {
		client.DidSave(m.openFile.Path)
	}
	m.notice = "saved"
	return m.refreshFileHighlights()
}

package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/input"
)

// contentHeight approximates the file pane's inner height (header + borders +
// status line, minus the tab strip when shown), used for paging and match
// centering.
func (m Model) contentHeight() int {
	return max(1, m.height-5-m.tabRows())
}

// tabRows is the number of rows the tab area occupies (strip + divider), 0 when
// there are no tabs.
func (m Model) tabRows() int {
	if len(m.tabs) > 0 {
		return 2
	}
	return 0
}

func (m Model) handleEditKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		m.mode = modeQuery
		return m, nil
	}

	k := msg.String()
	isText := msg.Text != "" && msg.Mod == 0 && k != "space"

	// The completion popup, when open, consumes its navigation/accept/dismiss
	// keys; any other key (except typing/backspace, which manage the popup
	// themselves) closes it and is then processed normally.
	if m.completionOpen {
		if next, cmd, done := m.completionKey(msg); done {
			return next, cmd
		}
		if !isText && k != "backspace" {
			m = m.closeCompletion()
		}
	} else if !isText && k != "backspace" {
		// Any non-typing key is a word boundary: lift an Esc dismissal so the
		// popup auto-opens again at the next word. Without this, one Esc kept
		// completion suppressed across new lines until a punctuation rune was
		// typed (Space/Enter never went through afterEditType).
		m.completionDismissed = false
	}

	switch k {
	case "esc":
		// First Esc clears a selection; the next discards unsaved edits and
		// returns to the query bar (the pane keeps showing the on-disk file).
		if m.edit.sel != nil {
			m.edit.clearSelection()
			return m, nil
		}
		m = m.flushBurst() // checkpoint so the discarded state stays reachable via undo history
		if m.edit.dirty {
			// A deliberate discard: drop the draft and make disk content the
			// new timeline head — the tab stops being red, a later switch
			// won't re-stash, and one Ctrl+Z recovers the discarded text.
			_ = m.db.DeleteDraft(m.openRel)
			delete(m.draftSet, m.openRel)
			prevRev := m.edit.rev
			m.edit = newEditor(m.openFile.Content)
			m.edit.rev = prevRev + 1
			m.snapDirty = true
			m = m.pushSnapshot("edit")
			m.notice = "unsaved changes discarded"
		}
		m.mode = modeQuery
		m.jumpStack = nil // quitting edit mode ends the jump trail
		return m.refreshFileHighlights(), nil

	case "ctrl+s":
		m = m.saveEdit()
		if m.gitRepo {
			// A write just landed: refresh git status now instead of waiting
			// out the poll interval, so the tree yellows immediately.
			return m, m.refreshGitStatusCmd()
		}
		return m, nil

	case "ctrl+z":
		return m.undo(), nil

	case "ctrl+y":
		return m.redo(), nil

	case "ctrl+f":
		m = m.flushBurst()
		return m.enterSearch(modeEdit, m.edit.content()), nil

	case "ctrl+j":
		return m.jumpToReference()

	case "ctrl+o":
		return m.jumpBack()

	case "ctrl+a":
		m.edit.expandSelection()
		return m, nil

	case "ctrl+e":
		return m.enterExec(), nil

	case "up":
		return m.moveEditCursor(0, -1), nil
	case "down":
		return m.moveEditCursor(0, 1), nil
	case "left":
		return m.moveEditCursor(-1, 0), nil
	case "right":
		return m.moveEditCursor(1, 0), nil
	case "shift+up":
		m.edit.extendLineSelection(-1)
		return m, nil
	case "shift+down":
		m.edit.extendLineSelection(1)
		return m, nil
	case "pgup":
		return m.pageEdit(-1), nil
	case "pgdown":
		return m.pageEdit(1), nil
	case "home":
		m.edit.clearSelection()
		m.edit.cx = 0
		return m.flushBurst(), nil
	case "end":
		m.edit.clearSelection()
		m.edit.cx = len(m.edit.line())
		return m.flushBurst(), nil

	case "enter":
		cy := m.edit.cy
		m.edit.newline()
		m = m.hlMarkLine(cy)
		m = m.hlInsertLine(cy + 1)
		m.snapDirty = true
		return m.flushBurst(), nil

	case "backspace":
		linesBefore := len(m.edit.lines)
		cyBefore := m.edit.cy
		m.edit.backspace()
		if len(m.edit.lines) < linesBefore {
			m = m.hlRemoveLine(cyBefore)
		}
		m = m.hlMarkLine(m.edit.cy)
		m.snapDirty = true
		return m.afterEditBackspace()

	case "delete":
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

	case "tab":
		m.edit.insert(strings.Repeat(" ", m.cfg.Editor.TabWidth))
		m = m.hlMarkLine(m.edit.cy)
		m.snapDirty = true
		return m, nil

	case "space":
		m.edit.insert(" ")
		m = m.hlMarkLine(m.edit.cy)
		m.snapDirty = true
		return m.flushBurst(), nil // word boundary → coalesce the burst

	default:
		if isText {
			m.edit.insert(msg.Text)
			m = m.hlMarkLine(m.edit.cy)
			m.snapDirty = true
			return m.afterEditType(msg.Text)
		}
	}
	return m, nil
}

// editPaste inserts bracketed-paste text into the buffer. edit.insert is
// single-line, so the paste is split on newlines and stitched in with
// newline() so each segment lands on its own line (with highlight upkeep).
func (m Model) editPaste(text string) (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		return m, nil
	}
	s := strings.ReplaceAll(text, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	for i, seg := range strings.Split(s, "\n") {
		if i > 0 {
			cy := m.edit.cy
			m.edit.newline()
			m = m.hlMarkLine(cy)
			m = m.hlInsertLine(cy + 1)
		}
		if seg != "" {
			m.edit.insert(seg)
			m = m.hlMarkLine(m.edit.cy)
		}
	}
	m.snapDirty = true
	return m.flushBurst(), nil // a paste is one burst boundary
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
	_ = m.db.DeleteDraft(m.openRel) // saved — the stashed draft is obsolete
	delete(m.draftSet, m.openRel)
	if client, ok := m.lsp.ClientFor(m.openFile.Path); ok {
		client.DidSave(m.openFile.Path)
	}
	m.notice = "saved"
	return m.refreshFileHighlights()
}

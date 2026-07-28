package app

import (
	"github.com/nickooan/ntee-editor/internal/input"
)

// Conflict-solving mode ("git scf" in the @exec bar). Unlike diff review it
// has no display model of its own: it browses the LIVE edit buffer through
// the edit session's cursor and scroll, tints the conflict regions, and pops
// an inline picker (Use HEAD / Use <branch> / Use both) whenever the cursor
// sits on a marker line. Enter splices the chosen side in place — a real,
// undoable buffer edit — and Esc returns to editing right where the cursor
// is, resolutions kept, remaining conflicts untouched.

// enterConflict switches to conflict-solving mode over the current buffer.
// Errors keep the current mode (the @exec bar) so the user can correct the
// input. No gitRepo guard: parsing is pure-buffer, and conflict markers can
// exist outside a repository (e.g. patch output).
func (m Model) enterConflict() Model {
	if m.openFile == nil {
		m.errText = "no file open"
		return m
	}
	blocks := findConflictBlocks(m.edit.lines)
	if len(blocks) == 0 {
		m.errText = "no conflict markers in this file"
		return m
	}
	m = m.flushBurst()      // pending typing becomes the first apply's undo pre-state
	m.edit.clearSelection() // a stale selection would render behind the tints
	m.conflictBlocks = blocks
	m.conflictChoice, m.conflictChoiceIdx = 0, -1
	m = m.syncConflictChoice()
	m.mode = modeConflict
	return m
}

// clearConflictState drops the parsed blocks and popup selection. Called on
// exit and from openFileAt so a tab switch can't leave stale blocks behind.
func (m Model) clearConflictState() Model {
	m.conflictBlocks = nil
	m.conflictChoice, m.conflictChoiceIdx = 0, -1
	return m
}

// conflictBlockOnMarker reports which block (by index) has a marker line —
// <<<<<<<, |||||||, =======, or >>>>>>> — at buffer line cy. Only well-formed
// blocks are in conflictBlocks, so a stray marker in a malformed region never
// triggers the popup.
func (m Model) conflictBlockOnMarker(cy int) (int, bool) {
	for i, b := range m.conflictBlocks {
		if cy == b.start || cy == b.mid || cy == b.end || (b.base != -1 && cy == b.base) {
			return i, true
		}
	}
	return 0, false
}

// syncConflictChoice realigns the popup selection with the cursor: landing on
// a different block resets the choice to the first option; leaving the
// markers closes the popup. Called after every cursor move.
func (m Model) syncConflictChoice() Model {
	idx, ok := m.conflictBlockOnMarker(m.edit.cy)
	if !ok {
		m.conflictChoiceIdx = -1
		return m
	}
	if idx != m.conflictChoiceIdx {
		m.conflictChoice, m.conflictChoiceIdx = 0, idx
	}
	return m
}

// jumpConflictBlock moves the cursor to the previous (dir<0) or next (dir>0)
// block's <<<<<<< line, anchored ~30% from the top like search/jump landings.
// No wraparound; inert when there is no block in that direction. From inside
// a block, jumping up lands on that block's own start.
func (m Model) jumpConflictBlock(dir int) Model {
	target := -1
	if dir < 0 {
		for _, b := range m.conflictBlocks { // blocks are in buffer order
			if b.start < m.edit.cy {
				target = b.start
			}
		}
	} else {
		for _, b := range m.conflictBlocks {
			if b.start > m.edit.cy {
				target = b.start
				break
			}
		}
	}
	if target == -1 {
		return m
	}
	m.edit.cy, m.edit.cx = target, 0
	m.edit.clampCursor()
	m = m.anchorCursorLine()
	return m.syncConflictChoice()
}

// conflictOptionLabels are the popup's three entries for a block, in
// conflictChoice order (ours, theirs, both). Unlabeled markers fall back to
// generic side names.
func conflictOptionLabels(b conflictBlock) [3]string {
	ours, theirs := b.oursLabel, b.theirsLabel
	if ours == "" {
		ours = "ours"
	}
	if theirs == "" {
		theirs = "theirs"
	}
	return [3]string{"Use " + ours, "Use " + theirs, "Use both"}
}

// applyConflictChoice resolves the block under the cursor to the popup's
// selected side: markers removed, chosen content spliced in (diff3 base
// always dropped), exactly like the VS Code / Zed accept actions. The edit is
// one undoable snapshot; the cursor stays in place, clamped into the resolved
// content; the remaining blocks are re-parsed since their indices shifted.
// Inert when the cursor is not on a marker line.
func (m Model) applyConflictChoice() Model {
	idx, ok := m.conflictBlockOnMarker(m.edit.cy)
	if !ok {
		return m
	}
	b := m.conflictBlocks[idx]
	choice := input.Clamp(m.conflictChoice, 0, 2)
	side := [3]conflictSide{sideOurs, sideTheirs, sideBoth}[choice]
	// Notice label: the bare side name ("HEAD", the branch, or "both").
	label := conflictOptionLabels(b)[choice][len("Use "):]

	m = m.flushBurst()
	newLines := resolveConflicts(m.edit.lines, []conflictBlock{b}, []conflictSide{side})
	if len(newLines) == 0 {
		newLines = []string{""} // whole-file block, empty side — the buffer keeps ≥1 line
	}
	// Lines the block resolved to: the splice replaced (end-start+1) lines.
	kept := len(newLines) - (len(m.edit.lines) - (b.end - b.start + 1))
	m.edit.lines = newLines
	m.edit.cy = input.Clamp(m.edit.cy, 0, b.start+max(0, kept-1))
	m.edit.cy = input.Clamp(m.edit.cy, 0, len(newLines)-1)
	m.edit.cx = 0
	m.edit.clampCursor()
	m.fileScrollY = input.Clamp(m.fileScrollY, 0, max(0, len(newLines)-1))
	m.edit.dirty = true
	m.edit.rev++ // buffer changed: force a highlight rescan (see edit.go)
	m.snapDirty = true
	m = m.pushSnapshot("edit") // one apply = one undoable step
	m = m.refreshFileHighlights()
	m.conflictBlocks = findConflictBlocks(m.edit.lines)
	m.conflictChoice, m.conflictChoiceIdx = 0, -1
	m = m.syncConflictChoice()
	m.notice = "resolved → " + label
	return m
}

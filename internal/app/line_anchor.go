package app

import (
	"regexp"
	"strconv"
)

// lineAnchorPattern matches a GitHub-style line anchor at the end of a path:
// "#L8", "#l8", "#L8-L12", "#L8-12". Incomplete forms ("#", "#L", "#L8-") also
// match so the popup and sidebar stay steady while the anchor is being typed.
var lineAnchorPattern = regexp.MustCompile(`#[Ll]?(\d*)(?:-[Ll]?\d*)?$`)

// splitLineAnchor separates an optional trailing line anchor from a typed
// path. line is the 1-based (start) line, or 0 when the anchor carries no
// number or there is no anchor at all.
func splitLineAnchor(text string) (pathText string, line int) {
	match := lineAnchorPattern.FindStringSubmatchIndex(text)
	if match == nil {
		return text, 0
	}
	if match[3] > match[2] {
		line, _ = strconv.Atoi(text[match[2]:match[3]])
	}
	return text[:match[0]], line
}

// jumpCursorToLine moves the cursor to a 1-based line (clamped to the buffer)
// and anchors it ~30% from the top.
func (m Model) jumpCursorToLine(line int) Model {
	idx, _ := parseJumpTarget(strconv.Itoa(max(1, line)), len(m.edit.lines))
	m.edit.clearSelection()
	m.edit.cy = idx
	m.edit.cx = 0
	m.edit.clampCursor()
	return m.anchorCursorLine()
}

// openFileAtLine opens rel and, when line > 0, places the cursor on that
// 1-based line, overriding the remembered cursor/draft position.
func (m Model) openFileAtLine(rel string, line int) Model {
	m = m.openFileAt(rel)
	// A failed open from edit mode leaves the previous file open; its cursor
	// must not move.
	if line > 0 && m.mode == modeEdit && m.openRel == rel {
		m = m.jumpCursorToLine(line)
	}
	return m
}

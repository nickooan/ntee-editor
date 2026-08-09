package app

import (
	"sort"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/input"
)

// handlePreviewKey drives the OpenAPI/GraphQL document preview. Like diff
// review, the switch is closed on purpose: unlisted keys fall through inert,
// which is what makes the mode read-only — typing, enter, ctrl+s/z/… never
// reach the buffer.
func (m Model) handlePreviewKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		m.mode = modeQuery
		return m.clearPreviewState(), nil
	}
	if m.preview.searching {
		return m.handlePreviewSearchKey(msg)
	}

	switch msg.String() {
	case "esc":
		return m.exitPreview(), nil

	case "up":
		return m.movePreviewCursor(-1), nil
	case "down":
		return m.movePreviewCursor(1), nil
	case "pgup":
		return m.pagePreview(-1), nil
	case "pgdown":
		return m.pagePreview(1), nil
	case "home":
		m.preview.cursor, m.preview.scrollY = 0, 0
		return m.syncPreviewSel(), nil
	case "end":
		total := len(m.preview.lines)
		if total > 0 {
			h := m.contentHeight() + 1
			m.preview.cursor = total - 1
			m.preview.scrollY = max(0, total-h)
		}
		return m.syncPreviewSel(), nil

	case "shift+up":
		if len(m.preview.outline) > 0 {
			m.preview.sel = input.Clamp(m.preview.sel-1, 0, len(m.preview.outline)-1)
		}
		return m, nil
	case "shift+down":
		if len(m.preview.outline) > 0 {
			m.preview.sel = input.Clamp(m.preview.sel+1, 0, len(m.preview.outline)-1)
		}
		return m, nil
	case "enter":
		return m.jumpPreviewOutline(), nil

	case "/", "ctrl+f":
		m.preview.searching = true
		m.preview.search = ""
		m.preview.focused = 0
		return m, nil
	}
	return m, nil
}

// handlePreviewSearchKey drives the in-preview search bar: live query typing,
// ↑/↓ cycling matches, Enter committing the landing. Deliberately no replace
// or search-exec — the document is read-only.
func (m Model) handlePreviewSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.preview.searching = false
		m.preview.search = ""
		m.preview.focused = 0
	case "enter":
		m = m.landPreviewMatch()
		m.preview.searching = false
		m.preview.search = ""
		m.preview.focused = 0
	case "up":
		m = m.cyclePreviewMatch(-1)
	case "down", "ctrl+f":
		m = m.cyclePreviewMatch(1)
	case "backspace":
		if runes := []rune(m.preview.search); len(runes) > 0 {
			m.preview.search = string(runes[:len(runes)-1])
			m = m.focusPreviewMatch()
		}
	case "space":
		m.preview.search += " "
		m = m.focusPreviewMatch()
	default:
		if t := keyText(msg); t != "" {
			m.preview.search += t
			m = m.focusPreviewMatch()
		}
	}
	return m, nil
}

// focusPreviewMatch focuses the first match at/after the document cursor
// (wrapping to the first match) and scrolls to it — typed searches start near
// where the user is looking, like edit-mode search.
func (m Model) focusPreviewMatch() Model {
	matches := m.previewMatches()
	m.preview.focused = 0
	for i, mt := range matches {
		if mt.LineIndex >= m.preview.cursor {
			m.preview.focused = i
			break
		}
	}
	return m.landPreviewMatch()
}

// cyclePreviewMatch moves the focused match with wraparound and follows it.
func (m Model) cyclePreviewMatch(dir int) Model {
	n := len(m.previewMatches())
	if n == 0 {
		return m
	}
	m.preview.focused = ((m.preview.focused+dir)%n + n) % n
	return m.landPreviewMatch()
}

// landPreviewMatch moves the document cursor onto the focused match and
// anchors it ~30% from the top, so an Esc right after a search jumps to the
// found content's source.
func (m Model) landPreviewMatch() Model {
	matches := m.previewMatches()
	if len(matches) == 0 || m.preview.focused >= len(matches) {
		return m
	}
	line := input.Clamp(matches[m.preview.focused].LineIndex, 0, max(0, len(m.preview.lines)-1))
	m.preview.cursor = line
	m.preview.scrollY = anchorScroll(line, m.contentHeight()+1, len(m.preview.lines))
	return m.syncPreviewSel()
}

// pagePreview pages the document with a one-line overlap, cursor keeping its
// on-screen row — the preview mirror of pageDiff.
func (m Model) pagePreview(dir int) Model {
	total := len(m.preview.lines)
	if total == 0 {
		return m
	}
	h := m.contentHeight() + 1
	step := max(1, h-1)
	top := fileViewportTop(m.preview.cursor, m.preview.scrollY, h, total)
	m.preview.cursor = input.Clamp(m.preview.cursor+dir*step, 0, total-1)
	m.preview.scrollY = input.Clamp(top+dir*step, 0, max(0, total-h))
	return m.syncPreviewSel()
}

// syncPreviewSel tracks the outline selection to the document cursor: the
// last outline entry whose anchor is at/above it (anchors are ascending, so
// binary search).
func (m Model) syncPreviewSel() Model {
	o := m.preview.outline
	if len(o) == 0 {
		return m
	}
	i := sort.Search(len(o), func(i int) bool { return o[i].LineIdx > m.preview.cursor }) - 1
	m.preview.sel = max(0, i)
	return m
}

// jumpPreviewOutline (Enter) moves the document cursor to the selected
// outline entry's anchor row.
func (m Model) jumpPreviewOutline() Model {
	o := m.preview.outline
	if len(o) == 0 {
		return m
	}
	e := o[input.Clamp(m.preview.sel, 0, len(o)-1)]
	m.preview.cursor = input.Clamp(e.LineIdx, 0, max(0, len(m.preview.lines)-1))
	m.preview.scrollY = anchorScroll(m.preview.cursor, m.contentHeight()+1, len(m.preview.lines))
	return m
}

// exitPreview (Esc) returns to editing at the source of the content under the
// document cursor. Content rendered from another file (a cross-file $ref, a
// merged sibling schema) opens that file (jumpToLocation pushes a Ctrl+O
// frame either way).
func (m Model) exitPreview() Model {
	src := m.previewSrcAt(m.preview.cursor)
	m = m.clearPreviewState()
	m.mode = modeEdit
	if src.File == "" || src.Line <= 0 {
		return m
	}
	return m.jumpToLocation(src.File, src.Line-1, 0)
}

package app

import (
	"sort"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/view"
)

// handleOpenAPIKey drives the OpenAPI preview. Like diff review, the switch
// is closed on purpose: unlisted keys fall through inert, which is what makes
// the mode read-only — typing, enter, ctrl+s/z/… never reach the buffer.
func (m Model) handleOpenAPIKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		m.mode = modeQuery
		return m.clearOpenAPIState(), nil
	}
	if m.openapiSearching {
		return m.handleOpenAPISearchKey(msg)
	}

	switch msg.String() {
	case "esc":
		return m.exitOpenAPI(), nil

	case "up":
		return m.moveOpenAPICursor(-1), nil
	case "down":
		return m.moveOpenAPICursor(1), nil
	case "pgup":
		return m.pageOpenAPI(-1), nil
	case "pgdown":
		return m.pageOpenAPI(1), nil
	case "home":
		m.openapiCursor, m.openapiScrollY = 0, 0
		return m.syncOpenAPISel(), nil
	case "end":
		total := len(m.openapiLines)
		if total > 0 {
			h := m.contentHeight() + 1
			m.openapiCursor = total - 1
			m.openapiScrollY = max(0, total-h)
		}
		return m.syncOpenAPISel(), nil

	case "shift+up":
		if len(m.openapiOutline) > 0 {
			m.openapiSel = input.Clamp(m.openapiSel-1, 0, len(m.openapiOutline)-1)
		}
		return m, nil
	case "shift+down":
		if len(m.openapiOutline) > 0 {
			m.openapiSel = input.Clamp(m.openapiSel+1, 0, len(m.openapiOutline)-1)
		}
		return m, nil
	case "enter":
		return m.jumpOpenAPIOutline(), nil

	case "/", "ctrl+f":
		m.openapiSearching = true
		m.openapiSearch = ""
		m.openapiFocused = 0
		return m, nil
	}
	return m, nil
}

// handleOpenAPISearchKey drives the in-preview search bar: live query typing,
// ↑/↓ cycling matches, Enter committing the landing. Deliberately no replace
// or search-exec — the document is read-only.
func (m Model) handleOpenAPISearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.openapiSearching = false
		m.openapiSearch = ""
		m.openapiFocused = 0
	case "enter":
		m = m.landOpenAPIMatch()
		m.openapiSearching = false
		m.openapiSearch = ""
		m.openapiFocused = 0
	case "up":
		m = m.cycleOpenAPIMatch(-1)
	case "down", "ctrl+f":
		m = m.cycleOpenAPIMatch(1)
	case "backspace":
		if runes := []rune(m.openapiSearch); len(runes) > 0 {
			m.openapiSearch = string(runes[:len(runes)-1])
			m = m.focusOpenAPIMatch()
		}
	case "space":
		m.openapiSearch += " "
		m = m.focusOpenAPIMatch()
	default:
		if t := keyText(msg); t != "" {
			m.openapiSearch += t
			m = m.focusOpenAPIMatch()
		}
	}
	return m, nil
}

// openapiMatches is the live match set of the current query over the rendered
// document's plain text.
func (m Model) openapiMatches() []view.SearchMatch {
	return m.openapiMC.get(m.openapiCorpus, m.openapiSearch)
}

// focusOpenAPIMatch focuses the first match at/after the document cursor
// (wrapping to the first match) and scrolls to it — typed searches start near
// where the user is looking, like edit-mode search.
func (m Model) focusOpenAPIMatch() Model {
	matches := m.openapiMatches()
	m.openapiFocused = 0
	for i, mt := range matches {
		if mt.LineIndex >= m.openapiCursor {
			m.openapiFocused = i
			break
		}
	}
	return m.landOpenAPIMatch()
}

// cycleOpenAPIMatch moves the focused match with wraparound and follows it.
func (m Model) cycleOpenAPIMatch(dir int) Model {
	n := len(m.openapiMatches())
	if n == 0 {
		return m
	}
	m.openapiFocused = ((m.openapiFocused+dir)%n + n) % n
	return m.landOpenAPIMatch()
}

// landOpenAPIMatch moves the document cursor onto the focused match and
// anchors it ~30% from the top, so an Esc right after a search jumps to the
// found content's source.
func (m Model) landOpenAPIMatch() Model {
	matches := m.openapiMatches()
	if len(matches) == 0 || m.openapiFocused >= len(matches) {
		return m
	}
	line := input.Clamp(matches[m.openapiFocused].LineIndex, 0, max(0, len(m.openapiLines)-1))
	m.openapiCursor = line
	m.openapiScrollY = anchorScroll(line, m.contentHeight()+1, len(m.openapiLines))
	return m.syncOpenAPISel()
}

// moveOpenAPICursor moves the document cursor by dy rows; the viewport
// follows via fileViewportTop at render time.
func (m Model) moveOpenAPICursor(dy int) Model {
	if len(m.openapiLines) == 0 {
		return m // still loading
	}
	m.openapiCursor = input.Clamp(m.openapiCursor+dy, 0, len(m.openapiLines)-1)
	return m.syncOpenAPISel()
}

// pageOpenAPI pages the document with a one-line overlap, cursor keeping its
// on-screen row — the openapi mirror of pageDiff.
func (m Model) pageOpenAPI(dir int) Model {
	total := len(m.openapiLines)
	if total == 0 {
		return m
	}
	h := m.contentHeight() + 1
	step := max(1, h-1)
	top := fileViewportTop(m.openapiCursor, m.openapiScrollY, h, total)
	m.openapiCursor = input.Clamp(m.openapiCursor+dir*step, 0, total-1)
	m.openapiScrollY = input.Clamp(top+dir*step, 0, max(0, total-h))
	return m.syncOpenAPISel()
}

// syncOpenAPISel tracks the outline selection to the document cursor: the
// last outline entry whose anchor is at/above it (anchors are ascending, so
// binary search).
func (m Model) syncOpenAPISel() Model {
	o := m.openapiOutline
	if len(o) == 0 {
		return m
	}
	i := sort.Search(len(o), func(i int) bool { return o[i].LineIdx > m.openapiCursor }) - 1
	m.openapiSel = max(0, i)
	return m
}

// jumpOpenAPIOutline (Enter) moves the document cursor to the selected
// outline entry's anchor row.
func (m Model) jumpOpenAPIOutline() Model {
	o := m.openapiOutline
	if len(o) == 0 {
		return m
	}
	e := o[input.Clamp(m.openapiSel, 0, len(o)-1)]
	m.openapiCursor = input.Clamp(e.LineIdx, 0, max(0, len(m.openapiLines)-1))
	m.openapiScrollY = anchorScroll(m.openapiCursor, m.contentHeight()+1, len(m.openapiLines))
	return m
}

// exitOpenAPI (Esc) returns to editing at the source of the content under the
// document cursor. Content rendered from a cross-file $ref opens that file
// (jumpToLocation pushes a Ctrl+O frame either way).
func (m Model) exitOpenAPI() Model {
	src := m.openapiSrcAt(m.openapiCursor)
	m = m.clearOpenAPIState()
	m.mode = modeEdit
	if src.File == "" || src.Line <= 0 {
		return m
	}
	return m.jumpToLocation(src.File, src.Line-1, 0)
}

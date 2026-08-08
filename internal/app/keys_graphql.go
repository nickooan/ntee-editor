package app

import (
	"sort"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/view"
)

// handleGraphQLKey drives the GraphQL schema preview. Like the OpenAPI
// preview, the switch is closed on purpose: unlisted keys fall through inert,
// which is what makes the mode read-only — typing, enter, ctrl+s/z/… never
// reach the buffer.
func (m Model) handleGraphQLKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		m.mode = modeQuery
		return m.clearGraphQLState(), nil
	}
	if m.graphqlSearching {
		return m.handleGraphQLSearchKey(msg)
	}

	switch msg.String() {
	case "esc":
		return m.exitGraphQL(), nil

	case "up":
		return m.moveGraphQLCursor(-1), nil
	case "down":
		return m.moveGraphQLCursor(1), nil
	case "pgup":
		return m.pageGraphQL(-1), nil
	case "pgdown":
		return m.pageGraphQL(1), nil
	case "home":
		m.graphqlCursor, m.graphqlScrollY = 0, 0
		return m.syncGraphQLSel(), nil
	case "end":
		total := len(m.graphqlLines)
		if total > 0 {
			h := m.contentHeight() + 1
			m.graphqlCursor = total - 1
			m.graphqlScrollY = max(0, total-h)
		}
		return m.syncGraphQLSel(), nil

	case "shift+up":
		if len(m.graphqlOutline) > 0 {
			m.graphqlSel = input.Clamp(m.graphqlSel-1, 0, len(m.graphqlOutline)-1)
		}
		return m, nil
	case "shift+down":
		if len(m.graphqlOutline) > 0 {
			m.graphqlSel = input.Clamp(m.graphqlSel+1, 0, len(m.graphqlOutline)-1)
		}
		return m, nil
	case "enter":
		return m.jumpGraphQLOutline(), nil

	case "/", "ctrl+f":
		m.graphqlSearching = true
		m.graphqlSearch = ""
		m.graphqlFocused = 0
		return m, nil
	}
	return m, nil
}

// handleGraphQLSearchKey drives the in-preview search bar: live query typing,
// ↑/↓ cycling matches, Enter committing the landing. Deliberately no replace
// or search-exec — the document is read-only.
func (m Model) handleGraphQLSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.graphqlSearching = false
		m.graphqlSearch = ""
		m.graphqlFocused = 0
	case "enter":
		m = m.landGraphQLMatch()
		m.graphqlSearching = false
		m.graphqlSearch = ""
		m.graphqlFocused = 0
	case "up":
		m = m.cycleGraphQLMatch(-1)
	case "down", "ctrl+f":
		m = m.cycleGraphQLMatch(1)
	case "backspace":
		if runes := []rune(m.graphqlSearch); len(runes) > 0 {
			m.graphqlSearch = string(runes[:len(runes)-1])
			m = m.focusGraphQLMatch()
		}
	case "space":
		m.graphqlSearch += " "
		m = m.focusGraphQLMatch()
	default:
		if t := keyText(msg); t != "" {
			m.graphqlSearch += t
			m = m.focusGraphQLMatch()
		}
	}
	return m, nil
}

// graphqlMatches is the live match set of the current query over the rendered
// document's plain text.
func (m Model) graphqlMatches() []view.SearchMatch {
	return view.FindSearchMatches(m.graphqlCorpus, m.graphqlSearch)
}

// focusGraphQLMatch focuses the first match at/after the document cursor
// (wrapping to the first match) and scrolls to it — typed searches start near
// where the user is looking, like edit-mode search.
func (m Model) focusGraphQLMatch() Model {
	matches := m.graphqlMatches()
	m.graphqlFocused = 0
	for i, mt := range matches {
		if mt.LineIndex >= m.graphqlCursor {
			m.graphqlFocused = i
			break
		}
	}
	return m.landGraphQLMatch()
}

// cycleGraphQLMatch moves the focused match with wraparound and follows it.
func (m Model) cycleGraphQLMatch(dir int) Model {
	n := len(m.graphqlMatches())
	if n == 0 {
		return m
	}
	m.graphqlFocused = ((m.graphqlFocused+dir)%n + n) % n
	return m.landGraphQLMatch()
}

// landGraphQLMatch moves the document cursor onto the focused match and
// anchors it ~30% from the top, so an Esc right after a search jumps to the
// found content's source.
func (m Model) landGraphQLMatch() Model {
	matches := m.graphqlMatches()
	if len(matches) == 0 || m.graphqlFocused >= len(matches) {
		return m
	}
	line := input.Clamp(matches[m.graphqlFocused].LineIndex, 0, max(0, len(m.graphqlLines)-1))
	m.graphqlCursor = line
	m.graphqlScrollY = anchorScroll(line, m.contentHeight()+1, len(m.graphqlLines))
	return m.syncGraphQLSel()
}

// moveGraphQLCursor moves the document cursor by dy rows; the viewport
// follows via fileViewportTop at render time.
func (m Model) moveGraphQLCursor(dy int) Model {
	if len(m.graphqlLines) == 0 {
		return m // still loading
	}
	m.graphqlCursor = input.Clamp(m.graphqlCursor+dy, 0, len(m.graphqlLines)-1)
	return m.syncGraphQLSel()
}

// pageGraphQL pages the document with a one-line overlap, cursor keeping its
// on-screen row — the graphql mirror of pageOpenAPI.
func (m Model) pageGraphQL(dir int) Model {
	total := len(m.graphqlLines)
	if total == 0 {
		return m
	}
	h := m.contentHeight() + 1
	step := max(1, h-1)
	top := fileViewportTop(m.graphqlCursor, m.graphqlScrollY, h, total)
	m.graphqlCursor = input.Clamp(m.graphqlCursor+dir*step, 0, total-1)
	m.graphqlScrollY = input.Clamp(top+dir*step, 0, max(0, total-h))
	return m.syncGraphQLSel()
}

// syncGraphQLSel tracks the outline selection to the document cursor: the
// last outline entry whose anchor is at/above it (anchors are ascending, so
// binary search).
func (m Model) syncGraphQLSel() Model {
	o := m.graphqlOutline
	if len(o) == 0 {
		return m
	}
	i := sort.Search(len(o), func(i int) bool { return o[i].LineIdx > m.graphqlCursor }) - 1
	m.graphqlSel = max(0, i)
	return m
}

// jumpGraphQLOutline (Enter) moves the document cursor to the selected
// outline entry's anchor row.
func (m Model) jumpGraphQLOutline() Model {
	o := m.graphqlOutline
	if len(o) == 0 {
		return m
	}
	e := o[input.Clamp(m.graphqlSel, 0, len(o)-1)]
	m.graphqlCursor = input.Clamp(e.LineIdx, 0, max(0, len(m.graphqlLines)-1))
	m.graphqlScrollY = anchorScroll(m.graphqlCursor, m.contentHeight()+1, len(m.graphqlLines))
	return m
}

// exitGraphQL (Esc) returns to editing at the source of the content under the
// document cursor. Content merged from another schema file opens that file
// (jumpToLocation pushes a Ctrl+O frame either way).
func (m Model) exitGraphQL() Model {
	src := m.graphqlSrcAt(m.graphqlCursor)
	m = m.clearGraphQLState()
	m.mode = modeEdit
	if src.File == "" || src.Line <= 0 {
		return m
	}
	return m.jumpToLocation(src.File, src.Line-1, 0)
}

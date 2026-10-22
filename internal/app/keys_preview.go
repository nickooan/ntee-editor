package app

import (
	"sort"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/keymap"
	"github.com/nickooan/ntee-editor/internal/view"
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
	if m.preview.search.active {
		return m.handlePreviewSearchKey(msg)
	}

	k := msg.String()
	if m.keys.Matches(keymap.Find, k) {
		k = "/"
	}
	if position, handled := handleRowNavigationKey(k, m.preview.position, m.previewSpan()); handled {
		m.preview.position = position
		return m.syncPreviewSel(), nil
	}
	switch k {
	case "esc":
		return m.exitPreview(), nil
	case "shift+up":
		m.preview.selection = m.preview.selection.moved(-1, len(m.preview.outline))
	case "shift+down":
		m.preview.selection = m.preview.selection.moved(1, len(m.preview.outline))
	case "enter":
		return m.jumpPreviewOutline(), nil
	case "/":
		m.preview.search = documentSearch{active: true}
	}
	return m, nil
}

// handlePreviewSearchKey drives the in-preview search bar (documentSearch);
// a landing moves the document cursor ~30% from the top, so an Esc right
// after a search jumps to the found content's source.
func (m Model) handlePreviewSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	return m.landPreviewSearch(m.preview.search.handleKey(msg, m.keys.Matches(keymap.Find, msg.String()), m.previewMatchesFor, m.preview.position.row)), nil
}

// previewMatchesFor is the match set of a query over the rendered document.
func (m Model) previewMatchesFor(query string) []view.SearchMatch {
	return m.previewMC.get(m.preview.corpus, query)
}

// landPreviewSearch stores a search-bar step and follows its landing.
func (m Model) landPreviewSearch(search documentSearch, landRow int, land bool) Model {
	m.preview.search = search
	if land {
		m.preview.position = m.preview.position.anchoredAt(landRow, m.previewSpan())
		m = m.syncPreviewSel()
	}
	return m
}

// syncPreviewSel tracks the outline selection to the document cursor: the
// last outline entry whose anchor is at/above it (anchors are ascending, so
// binary search).
func (m Model) syncPreviewSel() Model {
	o := m.preview.outline
	if len(o) == 0 {
		return m
	}
	i := sort.Search(len(o), func(i int) bool { return o[i].LineIdx > m.preview.position.row }) - 1
	m.preview.selection.index = max(0, i)
	return m
}

// jumpPreviewOutline (Enter) moves the document cursor to the selected
// outline entry's anchor row.
func (m Model) jumpPreviewOutline() Model {
	o := m.preview.outline
	if len(o) == 0 {
		return m
	}
	e := o[input.Clamp(m.preview.selection.index, 0, len(o)-1)]
	m.preview.position = m.preview.position.anchoredAt(e.LineIdx, m.previewSpan())
	return m
}

// exitPreview (Esc) returns to editing at the source of the content under the
// document cursor. Content rendered from another file (a cross-file $ref, a
// merged sibling schema) opens that file (jumpToLocation pushes a Ctrl+O
// frame either way).
func (m Model) exitPreview() Model {
	src := m.previewSrcAt(m.preview.position.row)
	m = m.clearPreviewState()
	m.mode = modeEdit
	if src.File == "" || src.Line <= 0 {
		return m
	}
	return m.jumpToLocation(src.File, src.Line-1, 0)
}

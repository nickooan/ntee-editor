package app

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/view"
)

// documentSearch is the in-view search bar of a read-only document (the
// OpenAPI/GraphQL previews, review mode): live query typing, ↑/↓ cycling
// matches, Enter committing the landing. Deliberately no replace or
// search-exec — the documents are read-only. The owning view supplies the
// match set (from its own matchCache) and moves its cursor to the landing row.
type documentSearch struct {
	active  bool
	query   string
	focused int // focused match index
}

// handleKey applies one key to the search bar. matchesFor returns the match
// set of a query over the document; cursorRow is the view's cursor, where a
// typed search starts looking (like edit-mode search). When land is true the
// view moves its cursor to landRow.
func (s documentSearch) handleKey(msg tea.KeyPressMsg, isFindKey bool, matchesFor func(query string) []view.SearchMatch, cursorRow int) (next documentSearch, landRow int, land bool) {
	k := msg.String()
	if isFindKey {
		k = "down"
	}
	switch k {
	case "esc":
		return documentSearch{}, 0, false
	case "enter":
		landRow, land = s.focusedRow(matchesFor(s.query))
		return documentSearch{}, landRow, land
	case "up":
		s = s.cycled(-1, matchesFor(s.query))
	case "down":
		s = s.cycled(1, matchesFor(s.query))
	case "backspace":
		runes := []rune(s.query)
		if len(runes) == 0 {
			return s, 0, false
		}
		s.query = string(runes[:len(runes)-1])
		s = s.focusedFrom(cursorRow, matchesFor(s.query))
	case "space":
		return s.typed(" ", matchesFor, cursorRow)
	default:
		text := keyText(msg)
		if text == "" {
			return s, 0, false
		}
		return s.typed(text, matchesFor, cursorRow)
	}
	landRow, land = s.focusedRow(matchesFor(s.query))
	return s, landRow, land
}

// typed appends typed or pasted text to the query and lands on the first
// match at/after the cursor.
func (s documentSearch) typed(text string, matchesFor func(query string) []view.SearchMatch, cursorRow int) (next documentSearch, landRow int, land bool) {
	s.query += text
	matches := matchesFor(s.query)
	s = s.focusedFrom(cursorRow, matches)
	landRow, land = s.focusedRow(matches)
	return s, landRow, land
}

// focusedFrom focuses the first match at/after row, wrapping to the first.
func (s documentSearch) focusedFrom(row int, matches []view.SearchMatch) documentSearch {
	s.focused = 0
	for i, match := range matches {
		if match.LineIndex >= row {
			s.focused = i
			break
		}
	}
	return s
}

// cycled moves the focused match with wraparound.
func (s documentSearch) cycled(dir int, matches []view.SearchMatch) documentSearch {
	if n := len(matches); n > 0 {
		s.focused = ((s.focused+dir)%n + n) % n
	}
	return s
}

// focusedRow is the row of the focused match, false when there is none.
func (s documentSearch) focusedRow(matches []view.SearchMatch) (int, bool) {
	if len(matches) == 0 || s.focused >= len(matches) {
		return 0, false
	}
	return matches[s.focused].LineIndex, true
}

// renderDocumentSearchStatus is the status row while a document search bar is
// open: "@<name> /query/   3/12" plus the bar's key hints.
func (m Model) renderDocumentSearchStatus(name string, search documentSearch, matchCount int) string {
	summary := fmt.Sprintf("%d matches", matchCount)
	if matchCount > 0 {
		summary = fmt.Sprintf("%d/%d", min(search.focused+1, matchCount), matchCount)
	}
	line := promptStyle.Render("@"+name+" /") + statusTextStyle.Render(search.query+"/   "+summary)
	return withNotice(m, line) + statusTextStyle.Render("   ") +
		hintStyle.Render("↑/↓ next/prev · Enter go · Esc close")
}

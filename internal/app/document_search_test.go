package app

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/view"
)

func TestDocumentSearchTypingFocusesFromCursorAndWraps(t *testing.T) {
	corpus := "alpha\nbeta needle\ngamma\nneedle delta\nepsilon"
	cache := &matchCache{}
	matchesFor := func(query string) []view.SearchMatch { return cache.get(corpus, query) }

	search := documentSearch{active: true}
	var row int
	var land bool
	for _, r := range "needle" {
		search, row, land = search.handleKey(typeRune(r), false, matchesFor, 2)
	}
	if !land || row != 3 || search.query != "needle" {
		t.Fatalf("typed search from row 2 should land on row 3: row=%d land=%v %+v", row, land, search)
	}
	search, row, _ = search.handleKey(keyPress(tea.KeyDown), false, matchesFor, row)
	if row != 1 {
		t.Fatalf("↓ past the last match wraps to the first: row %d", row)
	}
	search, row, _ = search.handleKey(keyPress(tea.KeyUp), true, matchesFor, row)
	if row != 3 {
		t.Fatalf("the Find key cycles forward like ↓: row %d", row)
	}

	next, row, land := search.handleKey(keyPress(tea.KeyEnter), false, matchesFor, row)
	if !land || row != 3 || next.active {
		t.Fatalf("Enter lands on the focused match and closes the bar: row=%d land=%v %+v", row, land, next)
	}
	if next, _, land = search.handleKey(keyPress(tea.KeyEscape), false, matchesFor, 0); land || next.active || next.query != "" {
		t.Fatalf("Esc closes without landing: %+v", next)
	}
}

func TestDocumentSearchBackspaceOnEmptyIsInert(t *testing.T) {
	search := documentSearch{active: true}
	next, _, land := search.handleKey(keyPress(tea.KeyBackspace), false, func(string) []view.SearchMatch { return nil }, 0)
	if land || next != search {
		t.Fatalf("backspace on an empty query: %+v land=%v", next, land)
	}
}

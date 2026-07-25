package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// searchAnchorFixture opens an 80-line file ("line N" rows, with a needle on
// lines 5 and 60) in edit mode — tall enough that the search view must choose
// an anchor.
func searchAnchorFixture(t *testing.T) Model {
	t.Helper()
	m, root := newTestModel(t, nil)
	var b strings.Builder
	for i := 0; i < 80; i++ {
		if i == 5 || i == 60 {
			b.WriteString(fmt.Sprintf("needle %d\n", i))
			continue
		}
		b.WriteString(fmt.Sprintf("line %d\n", i))
	}
	must(t, os.WriteFile(filepath.Join(root, "tall.go"), []byte(b.String()), 0o644))
	return m.openFileAt("tall.go")
}

// Entering search with an empty query must keep the view at the editing
// position (~30% from the top), not jump to the top of the file.
func TestSearchOpensAtEditingPosition(t *testing.T) {
	m := searchAnchorFixture(t)
	m.edit.cy = 50
	m = key(m, ctrlKey('f'))
	if m.mode != modeSearch {
		t.Fatal("expected search mode")
	}
	rows := strings.Split(ansi.Strip(m.renderSearch(80, 20)), "\n")
	// anchorScroll(50, 20, …) = 50 - 20*3/10 = 44.
	if !strings.Contains(rows[0], "line 44") {
		t.Fatalf("first row should anchor at line 44, got %q", rows[0])
	}
	if strings.Contains(rows[0], "line 0") {
		t.Fatal("empty search must not scroll to the top of the file")
	}
}

// Typing a query must focus the first match at/after the cursor line, so the
// view stays near the editing position; Enter lands on that match.
func TestSearchFocusesNearestMatchBelowCursor(t *testing.T) {
	m := searchAnchorFixture(t)
	m.edit.cy = 50
	m = key(m, ctrlKey('f'))
	m = runes(m, "needle") // matches on lines 5 and 60
	if m.searchFocused != 1 {
		t.Fatalf("searchFocused = %d, want 1 (the line-60 match)", m.searchFocused)
	}
	m = key(m, keyPress(tea.KeyEnter))
	if m.mode != modeEdit || m.edit.cy != 60 {
		t.Fatalf("enter should land on line 60, got mode=%d cy=%d", m.mode, m.edit.cy)
	}
}

// With no match at/after the cursor, the focus wraps to the file's first match.
func TestSearchNearestMatchWrapsToTop(t *testing.T) {
	m := searchAnchorFixture(t)
	m.edit.cy = 70 // past the last needle
	m = key(m, ctrlKey('f'))
	m = runes(m, "needle")
	if m.searchFocused != 0 {
		t.Fatalf("searchFocused = %d, want 0 (wrap to the line-5 match)", m.searchFocused)
	}
}

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSplitLineAnchor(t *testing.T) {
	cases := []struct {
		text     string
		wantPath string
		wantLine int
	}{
		{"scripts/build.sh#L8", "scripts/build.sh", 8},
		{"scripts/build.sh#l10", "scripts/build.sh", 10},
		{"scripts/build.sh#L8-L12", "scripts/build.sh", 8},
		{"scripts/build.sh#L8-12", "scripts/build.sh", 8},
		{"scripts/build.sh#8", "scripts/build.sh", 8},
		{"scripts/build.sh#", "scripts/build.sh", 0},
		{"scripts/build.sh#L", "scripts/build.sh", 0},
		{"scripts/build.sh#L8-", "scripts/build.sh", 8},
		{"scripts/build.sh", "scripts/build.sh", 0},
		{"scripts/build.sh#Lx", "scripts/build.sh#Lx", 0},
		{"a#b/c.go", "a#b/c.go", 0},
		{"my dir/a.go#L3", "my dir/a.go", 3},
		{"#L5", "", 5},
		{"", "", 0},
	}
	for _, c := range cases {
		gotPath, gotLine := splitLineAnchor(c.text)
		if gotPath != c.wantPath || gotLine != c.wantLine {
			t.Errorf("splitLineAnchor(%q) = (%q, %d), want (%q, %d)", c.text, gotPath, gotLine, c.wantPath, c.wantLine)
		}
	}
}

func TestJumpCursorToLineClamps(t *testing.T) {
	m := execLineFixture(t, 10)
	if got, want := m.jumpCursorToLine(999).edit.cy, len(m.edit.lines)-1; got != want {
		t.Fatalf("past EOF cy = %d, want last line %d", got, want)
	}
	if got := m.jumpCursorToLine(0).edit.cy; got != 0 {
		t.Fatalf("line 0 cy = %d, want 0", got)
	}
	if got := m.jumpCursorToLine(4).edit.cy; got != 3 {
		t.Fatalf("line 4 cy = %d, want 3", got)
	}
}

func TestQueryLineAnchorOpensAtLine(t *testing.T) {
	for _, typed := range []string{"main.go#L3", "main.go#l3", "main.go#L3-L4"} {
		m, _ := newTestModel(t, nil)
		m = runes(m, typed)
		m = key(m, keyPress(tea.KeyEnter))
		if m.openRel != "main.go" || m.mode != modeEdit {
			t.Fatalf("%q: did not open main.go in edit: open=%q mode=%d", typed, m.openRel, m.mode)
		}
		if m.edit.cy != 2 || m.edit.cx != 0 {
			t.Fatalf("%q: cursor = (%d,%d), want (2,0)", typed, m.edit.cy, m.edit.cx)
		}
	}
}

func TestQueryLineAnchorDoesNotChangeMatching(t *testing.T) {
	plain, _ := newTestModel(t, nil)
	plain = runes(plain, "uts")
	anchored, _ := newTestModel(t, nil)
	anchored = runes(anchored, "uts#L1")

	plainSuggestions := plain.queryInputSuggestions(plain.treeEntries())
	anchoredSuggestions := anchored.queryInputSuggestions(anchored.treeEntries())
	if len(plainSuggestions) == 0 || !reflect.DeepEqual(plainSuggestions, anchoredSuggestions) {
		t.Fatalf("anchor changed suggestions:\n plain=%+v\n anchored=%+v", plainSuggestions, anchoredSuggestions)
	}
	if plain.highlightedSidebarCommand() != anchored.highlightedSidebarCommand() {
		t.Fatalf("anchor changed sidebar highlight: %q vs %q", plain.highlightedSidebarCommand(), anchored.highlightedSidebarCommand())
	}
}

func TestQueryLineAnchorClampsPastEnd(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = runes(m, "main.go#L999")
	m = key(m, keyPress(tea.KeyEnter))
	if want := len(m.edit.lines) - 1; m.edit.cy != want {
		t.Fatalf("cy = %d, want last line %d", m.edit.cy, want)
	}
}

func TestQueryLineAnchorFromPaste(t *testing.T) {
	m, root := newTestModel(t, nil)
	writeLines(t, root, "lib/long.ts", 12)
	m = rebuildCorpusNow(m)
	next, _ := m.Update(tea.PasteMsg{Content: "lib/long.ts#L9"})
	m = key(next.(Model), keyPress(tea.KeyEnter))
	if m.openRel != "lib/long.ts" || m.edit.cy != 8 {
		t.Fatalf("paste+enter: open=%q cy=%d, want lib/long.ts cy=8", m.openRel, m.edit.cy)
	}
}

func TestQueryLineAnchorOverridesRememberedCursor(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m.edit.cy = 3
	m = m.openFileAt("lib/util.ts") // records main.go's cursor
	m.mode = modeQuery
	m = runes(m, "main.go#L2")
	m = key(m, keyPress(tea.KeyEnter))
	if m.openRel != "main.go" || m.edit.cy != 1 {
		t.Fatalf("open=%q cy=%d, want main.go cy=1", m.openRel, m.edit.cy)
	}
}

func TestFuzzyOverlayLineAnchor(t *testing.T) {
	m, root := newTestModel(t, nil)
	writeLines(t, root, "lib/long.ts", 12)
	m = rebuildCorpusNow(m)
	m = key(m, ctrlKey('p'))
	m = runes(m, "long")
	plainMatches := append([]int(nil), matchIndexes(m)...)
	m = runes(m, "#L5")
	if !reflect.DeepEqual(plainMatches, matchIndexes(m)) || len(plainMatches) == 0 {
		t.Fatalf("anchor changed finder matches: %v vs %v", plainMatches, matchIndexes(m))
	}
	m = key(m, keyPress(tea.KeyEnter))
	if m.fuzzyOpen || m.openRel != "lib/long.ts" || m.mode != modeEdit || m.edit.cy != 4 {
		t.Fatalf("finder anchor: open=%q mode=%d cy=%d, want lib/long.ts cy=4", m.openRel, m.mode, m.edit.cy)
	}
}

func matchIndexes(m Model) []int {
	out := make([]int, len(m.fuzzyMatches))
	for i, match := range m.fuzzyMatches {
		out[i] = match.Index
	}
	return out
}

// writeLines writes an n-line file ("line 1"…"line n") at the root-relative rel.
func writeLines(t *testing.T, root, rel string, n int) {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	must(t, os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(b.String()), 0o644))
}

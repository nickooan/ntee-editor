package app

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/view"
)

// searchExecFixture opens a file with three "search" occurrences (two on one
// line), enters search mode, and types the query so matches are highlighted.
func searchExecFixture(t *testing.T) Model {
	t.Helper()
	m, root := newTestModel(t, nil)
	content := "search54321\nplain line\nsearch and search\n"
	must(t, os.WriteFile(filepath.Join(root, "notes.txt"), []byte(content), 0o644))
	m = m.openFileAt("notes.txt")
	m = ctrl(m, tea.KeyCtrlF)
	if m.mode != modeSearch {
		t.Fatalf("Ctrl+F should enter modeSearch, got %v", m.mode)
	}
	return runes(m, "search")
}

func TestSearchExecEnter(t *testing.T) {
	m := searchExecFixture(t)
	m = ctrl(m, tea.KeyCtrlE)
	if m.mode != modeSearchExec {
		t.Fatalf("Ctrl+E with matches should enter modeSearchExec, got %v", m.mode)
	}

	// No matches: stays in search with an error.
	m = searchExecFixture(t)
	m = runes(m, "zzz")
	m = ctrl(m, tea.KeyCtrlE)
	if m.mode != modeSearch || m.errText != "no matches to act on" {
		t.Fatalf("Ctrl+E without matches: mode=%v errText=%q", m.mode, m.errText)
	}

	// Empty query: same gate.
	m2, _ := newTestModel(t, nil)
	m2 = m2.openFileAt("main.go")
	m2 = ctrl(m2, tea.KeyCtrlF)
	m2 = ctrl(m2, tea.KeyCtrlE)
	if m2.mode != modeSearch || m2.errText != "no matches to act on" {
		t.Fatalf("Ctrl+E with empty query: mode=%v errText=%q", m2.mode, m2.errText)
	}
}

func TestSearchExecReplaceFocused(t *testing.T) {
	m := searchExecFixture(t)
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "c search123")
	m = ctrl(m, tea.KeyEnter)

	if m.mode != modeSearch {
		t.Fatalf("replace should return to modeSearch, got %v", m.mode)
	}
	if m.edit.lines[0] != "search12354321" {
		t.Fatalf("line 0 = %q, want %q", m.edit.lines[0], "search12354321")
	}
	if m.edit.lines[2] != "search and search" {
		t.Fatalf("other lines must be untouched, line 2 = %q", m.edit.lines[2])
	}
	if m.searchContent != m.edit.content() {
		t.Fatal("search snapshot must be re-frozen from the buffer")
	}
	if !m.edit.dirty {
		t.Fatal("buffer should be dirty after replace")
	}
	if m.notice != "replaced 1 match(es)" {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestSearchExecReplaceAll(t *testing.T) {
	m := searchExecFixture(t)
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "mlc X")
	m = ctrl(m, tea.KeyEnter)

	want := []string{"X54321", "plain line", "X and X", ""}
	if !reflect.DeepEqual(m.edit.lines, want) {
		t.Fatalf("lines = %q, want %q", m.edit.lines, want)
	}
	if m.searchFocused != 0 {
		t.Fatalf("searchFocused = %d after mlc, want 0", m.searchFocused)
	}
	if m.notice != "replaced 3 match(es)" {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestSearchExecFocusAfterReplace(t *testing.T) {
	m := searchExecFixture(t)
	m = ctrl(m, tea.KeyDown) // focus match 1 of 3 (first on line 2)
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "c gone")
	m = ctrl(m, tea.KeyEnter)

	if m.edit.lines[2] != "gone and search" {
		t.Fatalf("line 2 = %q", m.edit.lines[2])
	}
	// The kept index now denotes the formerly-next match (last "search").
	matches := view.FindSearchMatches(m.searchContent, m.searchInput)
	if len(matches) != 2 || m.searchFocused != 1 {
		t.Fatalf("matches=%d focused=%d, want 2/1", len(matches), m.searchFocused)
	}
	if mt := matches[m.searchFocused]; mt.LineIndex != 2 || mt.Start != len("gone and ") {
		t.Fatalf("focused match = %+v, want the last occurrence on line 2", mt)
	}
}

func TestSearchExecEmptyReplacementDeletes(t *testing.T) {
	m := searchExecFixture(t)
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "c")
	m = ctrl(m, tea.KeyEnter)
	if m.edit.lines[0] != "54321" {
		t.Fatalf("bare c should delete the span, line 0 = %q", m.edit.lines[0])
	}
}

func TestSearchExecUnknownCommandStays(t *testing.T) {
	m := searchExecFixture(t)
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "bogus x")
	m = ctrl(m, tea.KeyEnter)
	if m.errText != "unknown command: bogus" {
		t.Fatalf("errText = %q", m.errText)
	}
	if m.mode != modeSearchExec {
		t.Fatal("should stay in the bar on error")
	}
}

func TestSearchExecEscCancels(t *testing.T) {
	m := searchExecFixture(t)
	before := m.edit.content()
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "c junk")
	m = ctrl(m, tea.KeyEsc)
	if m.mode != modeSearch {
		t.Fatalf("Esc should return to modeSearch, got %v", m.mode)
	}
	if m.edit.content() != before {
		t.Fatal("Esc must leave the buffer unchanged")
	}
}

func TestSearchExecUndoIsOneStep(t *testing.T) {
	m := searchExecFixture(t)
	before := m.edit.content()
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "mlc Y")
	m = ctrl(m, tea.KeyEnter)
	m = ctrl(m, tea.KeyEsc) // back to edit mode
	if m.mode != modeEdit {
		t.Fatalf("expected edit mode, got %v", m.mode)
	}
	m = ctrl(m, tea.KeyCtrlZ)
	if m.edit.content() != before {
		t.Fatalf("one undo should restore pre-replace content, got %q", m.edit.content())
	}
}

func TestSearchExecBlocksOverlays(t *testing.T) {
	m := searchExecFixture(t)
	m = ctrl(m, tea.KeyCtrlE)
	m = ctrl(m, tea.KeyCtrlP)
	if m.fuzzyOpen {
		t.Fatal("Ctrl+P must not open the finder in the search-exec bar")
	}
	m = ctrl(m, tea.KeyCtrlG)
	if m.grepOpen {
		t.Fatal("Ctrl+G must not open grep in the search-exec bar")
	}
}

func TestSearchExecPreviewParse(t *testing.T) {
	cases := []struct {
		in   string
		all  bool
		repl string
		ok   bool
	}{
		{"", false, "", false},
		{"c", false, "", false},
		{"c ", false, "", false},
		{"c a", false, "a", true},
		{"c a b", false, "a b", true},
		{"mlc asdf", true, "asdf", true},
		{"  mlc x", true, "x", true},
		{"bogus x", false, "", false},
	}
	for _, tc := range cases {
		m := Model{searchExecInput: tc.in}
		all, repl, ok := m.searchExecPreview()
		if all != tc.all || repl != tc.repl || ok != tc.ok {
			t.Errorf("%q: got (%v,%q,%v), want (%v,%q,%v)", tc.in, all, repl, ok, tc.all, tc.repl, tc.ok)
		}
	}
}

func TestBuildSearchPreview(t *testing.T) {
	find := view.FindSearchMatches

	// c: focused target shrinks, survivor on the same line shifts left.
	lines := []string{"search and search"}
	out, pspans, rest := buildSearchPreview(lines, find("search and search", "search"), 0, false, "go")
	if out[0] != "go and search" {
		t.Fatalf("out = %q", out[0])
	}
	if !reflect.DeepEqual(pspans[0], [][2]int{{0, 2}}) {
		t.Fatalf("pspans = %v", pspans)
	}
	want := []view.SearchMatch{{LineIndex: 0, Start: 7, End: 13}}
	if !reflect.DeepEqual(rest, want) {
		t.Fatalf("rest = %+v, want %+v", rest, want)
	}

	// c: longer replacement shifts the survivor right.
	out, pspans, rest = buildSearchPreview(lines, find(lines[0], "search"), 0, false, "searching")
	if out[0] != "searching and search" || rest[0].Start != 14 || pspans[0][0] != [2]int{0, 9} {
		t.Fatalf("longer repl: out=%q rest=%+v pspans=%v", out[0], rest, pspans)
	}

	// mlc: every match becomes a preview span, none survive; untouched lines pass through.
	lines = []string{"search54321", "plain", "search and search"}
	content := "search54321\nplain\nsearch and search"
	out, pspans, rest = buildSearchPreview(lines, find(content, "search"), 0, true, "X")
	wantOut := []string{"X54321", "plain", "X and X"}
	if !reflect.DeepEqual(out, wantOut) {
		t.Fatalf("mlc out = %q", out)
	}
	if len(rest) != 0 {
		t.Fatalf("mlc rest = %+v, want none", rest)
	}
	if !reflect.DeepEqual(pspans[2], [][2]int{{0, 1}, {6, 7}}) {
		t.Fatalf("mlc line-2 spans = %v", pspans[2])
	}
}

func TestSearchExecPreviewRenders(t *testing.T) {
	m := searchExecFixture(t)
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "mlc X")
	// Exercises the preview path end-to-end: spliced lines, nil-segs fallback
	// on previewed rows, and the status-bar "replacing N" summary.
	if out := m.View(); out == "" {
		t.Fatal("View produced no output")
	}
	if m.edit.content() != m.searchContent {
		t.Fatal("preview must not touch the buffer or snapshot")
	}
}

func TestReplaceInLines(t *testing.T) {
	mk := func(li, s, e int) view.SearchMatch { return view.SearchMatch{LineIndex: li, Start: s, End: e} }
	cases := []struct {
		name    string
		lines   []string
		targets []view.SearchMatch
		repl    string
		want    []string
	}{
		{"single", []string{"search54321"}, []view.SearchMatch{mk(0, 0, 6)}, "search123", []string{"search12354321"}},
		{"multi same line", []string{"aa bb aa"}, []view.SearchMatch{mk(0, 0, 2), mk(0, 6, 8)}, "cc", []string{"cc bb cc"}},
		{"adjacent", []string{"abab"}, []view.SearchMatch{mk(0, 0, 2), mk(0, 2, 4)}, "x", []string{"xx"}},
		{"empty repl", []string{"drop it"}, []view.SearchMatch{mk(0, 0, 5)}, "", []string{"it"}},
		{"utf8 line", []string{"héllo héllo"}, []view.SearchMatch{mk(0, 7, 13)}, "yo", []string{"héllo yo"}},
		{"out of range line", []string{"a"}, []view.SearchMatch{mk(5, 0, 1)}, "x", []string{"a"}},
	}
	for _, tc := range cases {
		if got := replaceInLines(tc.lines, tc.targets, tc.repl); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

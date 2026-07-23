package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/store"
)

// newTestModel builds a model over a temp project with a Memory backend and a
// ready window. Returns the model and the project root.
func newTestModel(t *testing.T, db store.Backend) (Model, string) {
	t.Helper()
	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {\n}\n"), 0o644))
	must(t, os.MkdirAll(filepath.Join(root, "lib"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "lib", "util.ts"), []byte("export const héllo = 1\n"), 0o644))
	if db == nil {
		db = store.NewMemory()
	}
	m := New(config.Default(), db, root, "", nil)
	m.width, m.height, m.ready = 100, 30, true
	return m, root
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func key(m Model, msg tea.KeyMsg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

func runes(m Model, s string) Model {
	for _, r := range s {
		if r == ' ' {
			m = key(m, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
			continue
		}
		m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func TestOpenEditSaveUndoRedo(t *testing.T) {
	m, root := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	if m.mode != modeEdit {
		t.Fatal("expected edit mode")
	}

	// Type at the top of the file, then hit a burst boundary (space).
	m = runes(m, "// hi")
	m = key(m, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	if !strings.HasPrefix(m.edit.content(), "// hi package") {
		t.Fatalf("typed content wrong: %q", m.edit.lines[0])
	}

	// Save writes to disk.
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	data, err := os.ReadFile(filepath.Join(root, "main.go"))
	must(t, err)
	if !strings.HasPrefix(string(data), "// hi ") {
		t.Fatalf("save did not hit disk: %q", string(data)[:20])
	}
	if m.edit.dirty {
		t.Fatal("buffer should be clean after save")
	}

	// Undo steps back through the burst; redo returns.
	saved := m.edit.content()
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlZ})
	if m.edit.content() == saved {
		t.Fatal("undo did not change the buffer")
	}
	undone := m.edit.content()
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlY})
	if m.edit.content() != saved {
		t.Fatalf("redo did not restore: %q vs %q", m.edit.content(), saved)
	}
	// Undo to the baseline (original disk content).
	for i := 0; i < 10; i++ {
		m = key(m, tea.KeyMsg{Type: tea.KeyCtrlZ})
	}
	if !strings.HasPrefix(m.edit.content(), "package main") {
		t.Fatalf("undo chain did not reach baseline: %q", m.edit.lines[0])
	}
	_ = undone
}

func TestSearchJumpLandsOnUTF8Column(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("lib/util.ts")

	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlF})
	if m.mode != modeSearch {
		t.Fatal("expected search mode")
	}
	m = runes(m, "= 1")
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeEdit {
		t.Fatal("enter should return to edit mode")
	}
	// "export const héllo = 1" — "=" is rune column 19 (é is one rune).
	if m.edit.cy != 0 || m.edit.cx != 19 {
		t.Fatalf("cursor at cy=%d cx=%d, want 0,19", m.edit.cy, m.edit.cx)
	}
}

func TestSessionRestoreAcrossInstances(t *testing.T) {
	db := store.NewMemory()
	m, root := newTestModel(t, db)
	m = m.openFileAt("lib/util.ts")
	m.saveSession()

	// "Relaunch": a fresh model over the same backend and root.
	m2 := New(config.Default(), db, root, "", nil)
	m2.width, m2.height, m2.ready = 100, 30, true
	if m2.openRel != "lib/util.ts" || m2.openFile == nil {
		t.Fatalf("last file not reopened: %q", m2.openRel)
	}
	// The confirmed command restores, so the sidebar expands lib/ again.
	if m2.selectedCommand != "lib/util.ts" {
		t.Fatalf("selectedCommand not restored: %q", m2.selectedCommand)
	}
	entries := m2.treeEntries()
	found := false
	for _, e := range entries {
		if e.RelativePath == "lib/util.ts" {
			found = true
		}
	}
	if !found {
		t.Fatal("sidebar did not expand to the restored file")
	}
	recents := db.RecentFiles(5)
	if len(recents) == 0 || recents[0].Path != "lib/util.ts" {
		t.Fatalf("recents not recorded: %+v", recents)
	}
}

func TestQueryTypingExpandsAndEnterOpens(t *testing.T) {
	m, _ := newTestModel(t, nil)
	if m.mode != modeQuery {
		t.Fatal("home mode should be query")
	}

	// Typing "lib/" expands lib in the sidebar.
	m = runes(m, "lib/")
	entries := m.treeEntries()
	seen := false
	for _, e := range entries {
		if e.RelativePath == "lib/util.ts" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("typing lib/ did not expand lib: %+v", entries)
	}

	// Continue typing the filename; Enter opens it straight into edit mode
	// and clears the bar.
	m = runes(m, "util")
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.openRel != "lib/util.ts" || m.mode != modeEdit {
		t.Fatalf("enter did not open lib/util.ts in edit: open=%q mode=%d", m.openRel, m.mode)
	}
	if m.command != "" {
		t.Fatalf("bar not cleared after open: %q", m.command)
	}
}

func TestQueryFuzzyFindsCollapsedFile(t *testing.T) {
	m, _ := newTestModel(t, nil)
	// "uts" is a subsequence of util.ts, which lives in the collapsed lib/.
	m = runes(m, "uts")
	suggestions := m.queryInputSuggestions(m.treeEntries())
	if len(suggestions) == 0 || suggestions[0].Entry.RelativePath != "lib/util.ts" {
		t.Fatalf("fuzzy suggestion missing: %+v", suggestions)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.openRel != "lib/util.ts" {
		t.Fatalf("enter on fuzzy suggestion did not open: %q", m.openRel)
	}
}

func TestQueryShiftArrowsWalkTree(t *testing.T) {
	m, _ := newTestModel(t, nil)
	// Tree rows: lib/(dir), main.go. Shift+Down highlights the first row and
	// previews it in the bar without expanding.
	m = key(m, tea.KeyMsg{Type: tea.KeyShiftDown})
	if m.keyboardSelectedCommand != "lib/" || m.commandPreview != "lib/" {
		t.Fatalf("shift+down highlight/preview wrong: %q %q", m.keyboardSelectedCommand, m.commandPreview)
	}
	if m.command != "" {
		t.Fatal("typed command must stay untouched by navigation")
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyShiftDown})
	if m.keyboardSelectedCommand != "main.go" {
		t.Fatalf("second shift+down: %q", m.keyboardSelectedCommand)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.openRel != "main.go" {
		t.Fatalf("enter on highlighted row did not open: %q", m.openRel)
	}
}

func TestQueryEscGoesToParent(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = runes(m, "lib/util.ts")
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.command != "lib/" || m.selectedCommand != "lib/" {
		t.Fatalf("esc should go to parent dir: cmd=%q sel=%q", m.command, m.selectedCommand)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.command != "" {
		t.Fatalf("second esc should reach root: %q", m.command)
	}
}

func TestQueryColonCommand(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m.mode = modeQuery
	m = runes(m, ":jump 3")
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeQuery || m.fileScrollY != 2 {
		t.Fatalf(":jump 3 from query bar: mode=%d scrollY=%d", m.mode, m.fileScrollY)
	}
}

func TestEscFromEditReturnsToQuery(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = runes(m, "maingo")
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeEdit {
		t.Fatalf("open should land in edit mode, got %d", m.mode)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeQuery {
		t.Fatalf("esc should return to the query bar, got %d", m.mode)
	}
	if m.openFile == nil || m.fileLines == nil {
		t.Fatal("the pane should keep showing the file")
	}
}

func TestFuzzyOverlayOpensFile(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlP})
	if !m.fuzzyOpen {
		t.Fatal("ctrl+p should open the finder")
	}
	m = runes(m, "util")
	if len(m.fuzzyMatches) != 1 {
		t.Fatalf("want 1 match for 'util', got %d", len(m.fuzzyMatches))
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.fuzzyOpen || m.openRel != "lib/util.ts" || m.mode != modeEdit {
		t.Fatalf("finder should open lib/util.ts in edit mode, got %q", m.openRel)
	}
}

func TestFuzzyOverlayDirDrillDown(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlP})
	m = runes(m, "lib")
	dirIdx := -1
	for i, match := range m.fuzzyMatches {
		if m.fuzzyCorpus[match.Index].Text == "lib/" {
			dirIdx = i
		}
	}
	if dirIdx < 0 {
		t.Fatalf("dir candidate lib/ missing from matches")
	}
	m.fuzzyIndex = dirIdx
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.fuzzyOpen || m.fuzzyQuery != "lib/" {
		t.Fatalf("enter on a dir should drill down in place: open=%v query=%q", m.fuzzyOpen, m.fuzzyQuery)
	}
	// The dir itself is dropped from its own drill-down so Enter can't loop.
	sawFile := false
	for _, match := range m.fuzzyMatches {
		switch m.fuzzyCorpus[match.Index].Text {
		case "lib/":
			t.Fatal("the drilled dir should not list itself")
		case "lib/util.ts":
			sawFile = true
		}
	}
	if !sawFile {
		t.Fatalf("drill-down should list the dir's files")
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.fuzzyOpen || m.openRel != "lib/util.ts" {
		t.Fatalf("second enter should open the file, got %q", m.openRel)
	}
}

func TestHighlightCacheStaysAlignedAcrossNewline(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	if m.hlLines == nil {
		t.Fatal("go file should have highlight rows")
	}

	// Newline mid-file must keep hlLines row count == buffer line count.
	m.edit.cy, m.edit.cx = 1, 0
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.hlLines) != len(m.edit.lines) {
		t.Fatalf("hlLines %d rows != buffer %d lines", len(m.hlLines), len(m.edit.lines))
	}

	// Backspace joining lines must shrink the cache in step.
	m = key(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if len(m.hlLines) != len(m.edit.lines) {
		t.Fatalf("after join: hlLines %d rows != buffer %d lines", len(m.hlLines), len(m.edit.lines))
	}
}

func TestRevertRestoresLastSave(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m = runes(m, "AAA")
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	saved := m.edit.content()

	m = runes(m, "BBB")
	m = key(m, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}) // checkpoint the burst

	// :revert via the command bar.
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc}) // back to view (discard indicator only; buffer content persists in snapshots)
	m = m.beginEditSession(m.openFile.Content)
	m.mode = modeEdit
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	mm, _ := m.enterCommand().executeCommand("revert")
	m = mm.(Model)
	if m.edit.content() != saved {
		t.Fatalf("revert mismatch:\n got %q\nwant %q", m.edit.content(), saved)
	}
}

func TestRevertReachableFromQueryBar(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m = runes(m, "XX ")
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	saved := m.edit.content()
	m = runes(m, "YY ")
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlS})

	// Esc to the query bar, then :revert typed into it (the real key path).
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeQuery {
		t.Fatal("esc should return to the query bar")
	}
	m = runes(m, ":revert")
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeEdit {
		t.Fatalf("revert should land in edit mode, got %d (%s)", m.mode, m.errText)
	}
	// LastSave is the newest save — undo once to reach the previous one.
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlZ})
	_ = saved
	if m.errText != "" {
		t.Fatalf("unexpected error: %q", m.errText)
	}
}

func TestSuggestionsWalkBeyondEight(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.MkdirAll(filepath.Join(root, "many"), 0o755))
	for i := 0; i < 12; i++ {
		must(t, os.WriteFile(filepath.Join(root, "many", fmt.Sprintf("f%02d.go", i)), []byte("package many\n"), 0o644))
	}
	m = runes(m, "many/")
	suggestions := m.queryInputSuggestions(m.treeEntries())
	if len(suggestions) < 12 {
		t.Fatalf("suggestions capped too early: %d", len(suggestions))
	}
	// ↓ must be able to reach the last file, not wrap at 8.
	for i := 0; i < len(suggestions)-1; i++ {
		m = key(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	if m.commandPreview != "many/f11.go" {
		t.Fatalf("could not walk to the last suggestion: preview=%q idx=%d", m.commandPreview, m.inputSuggestIndex)
	}
}

func TestSearchEnterAnchorsLineAtThirtyPercent(t *testing.T) {
	m, root := newTestModel(t, nil)
	var b strings.Builder
	for i := 0; i < 100; i++ {
		if i == 50 {
			b.WriteString("var needleTarget = 1\n")
			continue
		}
		fmt.Fprintf(&b, "// line %d\n", i)
	}
	must(t, os.WriteFile(filepath.Join(root, "big.go"), []byte("package main\n"+b.String()), 0o644))
	m = m.openFileAt("big.go")

	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlF})
	m = runes(m, "needleTarget")
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.edit.cy != 51 {
		t.Fatalf("cursor line: %d", m.edit.cy)
	}
	want := 51 - m.contentHeight()*3/10
	if m.fileScrollY != want {
		t.Fatalf("scroll anchor: got %d want %d (contentHeight %d)", m.fileScrollY, want, m.contentHeight())
	}
}

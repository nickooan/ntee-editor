package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/lsp"
)

// jumpFixture builds a project with a path reference and cross-file Go
// definitions, opens main.go in edit mode, and returns the model.
func jumpFixture(t *testing.T) Model {
	t.Helper()
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "helper.go"), []byte(
		"package main\n\nfunc helperFunc() int {\n\treturn 1\n}\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "main.go"), []byte(
		"package main\n\n// see lib/util.ts\nfunc main() {\n\thelperFunc()\n\tlocalThing()\n}\n\nfunc localThing() {}\n"), 0o644))
	m = m.openFileAt("main.go")
	if m.mode != modeEdit {
		t.Fatal("fixture should be in edit mode")
	}
	return m
}

func ctrl(m Model, k tea.KeyType) Model { return key(m, tea.KeyMsg{Type: k}) }

func TestJumpToPathUnderCursorAndBack(t *testing.T) {
	m := jumpFixture(t)
	// Cursor on "lib/util.ts" in the comment line (line 2).
	m.edit.cy, m.edit.cx = 2, 8
	m = ctrl(m, tea.KeyCtrlJ)
	if m.openRel != "lib/util.ts" || m.mode != modeEdit {
		t.Fatalf("path jump failed: open=%q err=%q", m.openRel, m.errText)
	}
	if len(m.jumpStack) != 1 {
		t.Fatalf("stack depth = %d", len(m.jumpStack))
	}
	m = ctrl(m, tea.KeyCtrlO)
	if m.openRel != "main.go" || m.edit.cy != 2 || m.edit.cx != 8 {
		t.Fatalf("jump back mismatch: open=%q cy=%d cx=%d", m.openRel, m.edit.cy, m.edit.cx)
	}
	if len(m.jumpStack) != 0 {
		t.Fatal("stack should be empty after back")
	}
	m = ctrl(m, tea.KeyCtrlO)
	if m.errText != "no jump to return to" {
		t.Fatalf("empty-stack error missing: %q", m.errText)
	}
}

func TestJumpGuards(t *testing.T) {
	m := jumpFixture(t)

	// Dirty buffer blocks jumping.
	m = runes(m, "x")
	m = ctrl(m, tea.KeyCtrlJ)
	if !strings.Contains(m.errText, "save") {
		t.Fatalf("dirty guard missing: %q", m.errText)
	}
	m = ctrl(m, tea.KeyCtrlZ) // undo back to clean baseline
	if m.edit.dirty {
		t.Fatal("undo should restore the clean baseline")
	}

	// Unresolvable token errors and leaves no stack residue.
	m.edit.cy, m.edit.cx = 0, 0 // "package" — keyword line, no definition
	m = ctrl(m, tea.KeyCtrlJ)
	if m.errText == "" || len(m.jumpStack) != 0 {
		t.Fatalf("unresolvable jump: err=%q stack=%d", m.errText, len(m.jumpStack))
	}
}

func TestJumpStackClearedOnEscAndOpen(t *testing.T) {
	m := jumpFixture(t)
	m.edit.cy, m.edit.cx = 2, 8
	m = ctrl(m, tea.KeyCtrlJ) // → lib/util.ts, stack 1
	if len(m.jumpStack) != 1 {
		t.Fatalf("setup: stack=%d err=%q", len(m.jumpStack), m.errText)
	}

	// Esc out of edit mode ends the trail.
	m = ctrl(m, tea.KeyEsc)
	if len(m.jumpStack) != 0 {
		t.Fatal("esc should clear the jump stack")
	}

	// A deliberate open also starts a fresh trail.
	m = jumpFixture(t)
	m.edit.cy, m.edit.cx = 2, 8
	m = ctrl(m, tea.KeyCtrlJ)
	m = m.openFileAt("main.go")
	if len(m.jumpStack) != 0 {
		t.Fatal("deliberate open should clear the jump stack")
	}
}

func TestEnterSearchPopulatesHighlights(t *testing.T) {
	m := jumpFixture(t)
	m = ctrl(m, tea.KeyCtrlF)
	if m.mode != modeSearch {
		t.Fatal("expected search mode")
	}
	if m.searchHl == nil {
		t.Fatal("go file search should be highlighted")
	}
	lineCount := len(strings.Split(m.searchContent, "\n"))
	if len(m.searchHl) != lineCount {
		t.Fatalf("searchHl rows %d != content lines %d", len(m.searchHl), lineCount)
	}

	// Unknown extension falls back to plain.
	root := m.root
	must(t, os.WriteFile(filepath.Join(root, "notes.xyzunknown"), []byte("plain text\n"), 0o644))
	m2 := m
	m2 = m2.openFileAt("notes.xyzunknown")
	m2 = ctrl(m2, tea.KeyCtrlF)
	if m2.searchHl != nil {
		t.Fatal("unknown extension should search plain")
	}
}

func TestPickerEscCancelsWithoutMoving(t *testing.T) {
	m, client := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	root := m.root
	m.edit.cy, m.edit.cx = 3, 0 // "}" — not a candidate line

	// Two LSP hits open the picker.
	client.locs = []lsp.Location{
		{URI: lsp.PathToURI(filepath.Join(root, "lib", "util.ts")), Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}}},
		{URI: lsp.PathToURI(filepath.Join(root, "main.go")), Range: lsp.Range{Start: lsp.Position{Line: 2, Character: 0}}},
	}
	next, _ := m.handleDefinition(definitionMsg{token: "x", locs: client.locs})
	m = next.(Model)
	if !m.defPickOpen {
		t.Fatalf("picker should open: err=%q", m.errText)
	}
	m = ctrl(m, tea.KeyEsc)
	if m.defPickOpen || m.openRel != "main.go" || m.edit.cy != 3 {
		t.Fatalf("esc should cancel in place: open=%q cy=%d", m.openRel, m.edit.cy)
	}
	if len(m.jumpStack) != 0 {
		t.Fatal("cancelled pick must leave no stack residue")
	}
}

func TestPickerPreviewFollowsSelection(t *testing.T) {
	m, client := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	root := m.root
	m.edit.cy, m.edit.cx = 3, 0 // "}" — not a candidate line

	client.locs = []lsp.Location{
		{URI: lsp.PathToURI(filepath.Join(root, "main.go")), Range: lsp.Range{Start: lsp.Position{Line: 2, Character: 0}}},
		{URI: lsp.PathToURI(filepath.Join(root, "lib", "util.ts")), Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}}},
	}
	next, _ := m.handleDefinition(definitionMsg{token: "x", locs: client.locs})
	m = next.(Model)
	if !m.defPickOpen || len(m.defPickItems) != 2 {
		t.Fatalf("picker should open with 2 hits: open=%v n=%d err=%q", m.defPickOpen, len(m.defPickItems), m.errText)
	}
	if m.defPickPrevRel != m.defPickItems[0].rel {
		t.Fatalf("preview should load the first candidate: %q", m.defPickPrevRel)
	}
	if len(m.defPickPrevLines) == 0 || m.defPickPrevHl == nil {
		t.Fatal("preview lines/highlight missing")
	}

	first := m.defPickPrevRel
	m = ctrl(m, tea.KeyDown)
	if m.defPickPrevRel == first {
		t.Fatalf("preview should follow selection to the other file: %q", m.defPickPrevRel)
	}

	// The rendered overlay includes the second candidate's code and the divider.
	out := m.renderDefPickOverlay(100, 30)
	if !strings.Contains(out, "héllo") {
		t.Fatal("preview should show the second candidate's line")
	}
	if !strings.Contains(out, "─") {
		t.Fatal("divider missing")
	}
}

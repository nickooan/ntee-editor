package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func grepFixture(t *testing.T) Model {
	t.Helper()
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "alpha.go"), []byte(
		"package main\n\n// grepNeedle here\nfunc a() {}\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "beta.go"), []byte(
		"package main\n\nfunc b() {\n\t_ = \"grepNeedle too\"\n}\n"), 0o644))
	return m
}

func TestGrepSearchAcrossFiles(t *testing.T) {
	m := grepFixture(t)
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlG})
	if !m.grepOpen || len(m.grepFiles) == 0 {
		t.Fatal("ctrl+g should open with a loaded corpus")
	}

	// One rune → no search yet.
	m = runes(m, "g")
	if len(m.grepResults) != 0 {
		t.Fatal("sub-2-rune query must not search")
	}
	m = runes(m, "repNeedle")
	if len(m.grepResults) != 2 {
		t.Fatalf("want 2 hits, got %+v", m.grepResults)
	}
	if m.grepResults[0].rel != "alpha.go" || m.grepResults[0].line != 2 {
		t.Fatalf("first hit: %+v", m.grepResults[0])
	}
	if m.grepHlRel != "alpha.go" || m.grepHl == nil {
		t.Fatalf("preview highlight should follow selection: %q", m.grepHlRel)
	}

	// ↓ moves the selection; preview cache follows the new file.
	m = key(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.grepIndex != 1 || m.grepHlRel != "beta.go" {
		t.Fatalf("selection/preview: idx=%d rel=%q", m.grepIndex, m.grepHlRel)
	}

	// Enter opens the selected hit in edit mode, anchored.
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.grepOpen || m.openRel != "beta.go" || m.mode != modeEdit {
		t.Fatalf("enter open failed: open=%q mode=%d", m.openRel, m.mode)
	}
	if m.edit.cy != 3 {
		t.Fatalf("cursor line: %d", m.edit.cy)
	}
}

func TestGrepLiteralFallbackAndCase(t *testing.T) {
	m := grepFixture(t)
	root := m.root
	must(t, os.WriteFile(filepath.Join(root, "paren.go"), []byte("package main\n\n// call foo(bar\n"), 0o644))
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlG})

	m = runes(m, "foo(bar") // invalid regex → literal fallback
	if len(m.grepResults) != 1 || m.grepResults[0].rel != "paren.go" {
		t.Fatalf("literal fallback: %+v", m.grepResults)
	}
	for range "foo(bar" {
		m = key(m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m = runes(m, "GREPNEEDLE")
	if len(m.grepResults) != 2 {
		t.Fatalf("case-insensitive: %+v", m.grepResults)
	}
}

func TestGrepDirtyEditGuard(t *testing.T) {
	m := grepFixture(t)
	m = m.openFileAt("alpha.go")
	m = runes(m, "x") // dirty
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlG})
	if m.grepOpen {
		t.Fatal("grep must not open over unsaved changes")
	}
	if !strings.Contains(m.errText, "save (Ctrl+S)") {
		t.Fatalf("guard message: %q", m.errText)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlG})
	if !m.grepOpen {
		t.Fatal("grep should open after saving")
	}
}

func TestGrepEscClosesWithoutOpening(t *testing.T) {
	m := grepFixture(t)
	m = m.openFileAt("alpha.go")
	before := m.openRel
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlG})
	m = runes(m, "grepNeedle")
	m = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.grepOpen || m.openRel != before {
		t.Fatalf("esc should close in place: open=%q", m.openRel)
	}
	if m.grepFiles != nil {
		t.Fatal("corpus should be released on close")
	}
}

func TestGrepOverlayLayoutSplit(t *testing.T) {
	m := grepFixture(t)
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlG})
	m = runes(m, "grepNeedle")
	out := m.renderGrepOverlay(100, 30)
	lines := strings.Split(out, "\n")
	if len(lines) < 25 {
		t.Fatalf("overlay should be tall: %d rows", len(lines))
	}
	if !strings.Contains(out, "grepNeedle here") {
		t.Fatal("preview should show the hit line content")
	}
	if !strings.Contains(out, "alpha.go:3") {
		t.Fatal("result rows should show name:line")
	}
	if !strings.Contains(out, "2 results") {
		t.Fatal("result count missing")
	}
}

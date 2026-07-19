package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestExecCopyCommand(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	var captured string
	m.copyClipboard = func(s string) error { captured = s; return nil }

	// Select the whole first line via Ctrl+A twice.
	m.edit.cy, m.edit.cx = 0, 0
	m = ctrl(m, tea.KeyCtrlA)
	m = ctrl(m, tea.KeyCtrlA)
	if !m.edit.selLineMode {
		t.Fatal("expected line-mode selection after Ctrl+A twice")
	}

	// Ctrl+E enters exec mode without disturbing the editor selection.
	m = ctrl(m, tea.KeyCtrlE)
	if m.mode != modeExec {
		t.Fatalf("Ctrl+E should enter modeExec, got %v", m.mode)
	}
	if !m.edit.selLineMode || m.edit.cy != 0 {
		t.Fatal("entering exec must leave the selection intact")
	}

	m = runes(m, "copy")
	if m.execInput != "copy" {
		t.Fatalf("execInput = %q", m.execInput)
	}
	m = ctrl(m, tea.KeyEnter)

	if captured != "package main\n" {
		t.Fatalf("clipboard got %q", captured)
	}
	if m.notice != "copied" {
		t.Fatalf("notice = %q", m.notice)
	}
	if m.mode != modeEdit {
		t.Fatalf("copy should return to edit mode, got %v", m.mode)
	}
}

func TestExecCopyNothingSelected(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	called := false
	m.copyClipboard = func(string) error { called = true; return nil }

	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "copy")
	m = ctrl(m, tea.KeyEnter)

	if called {
		t.Fatal("copy must not run without a selection")
	}
	if m.errText != "nothing selected" {
		t.Fatalf("errText = %q", m.errText)
	}
	if m.mode != modeExec {
		t.Fatal("should stay in exec mode on error")
	}
}

func TestExecEscRestoresModeKeepingSelection(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m.edit.cy, m.edit.cx = 0, 0
	m = ctrl(m, tea.KeyCtrlA) // some selection
	m = ctrl(m, tea.KeyCtrlE)
	if m.mode != modeExec {
		t.Fatal("expected exec mode")
	}
	m = ctrl(m, tea.KeyEsc)
	if m.mode != modeEdit {
		t.Fatalf("Esc should restore edit mode, got %v", m.mode)
	}
	if m.edit.sel == nil {
		t.Fatal("Esc from exec must not clear the selection")
	}
}

func TestExecUnknownCommand(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "bogus")
	m = ctrl(m, tea.KeyEnter)
	if m.errText != "unknown command: bogus" {
		t.Fatalf("errText = %q", m.errText)
	}
	if m.mode != modeExec {
		t.Fatal("should stay in exec mode on unknown command")
	}
}

func TestExecBarReplacesEditStatus(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")

	// In edit mode the @edit status line is shown.
	if !strings.Contains(m.View(), "@edit") {
		t.Fatal("edit view should show the @edit status line")
	}

	// Ctrl+E replaces it with the @exec bar (single status row).
	m = ctrl(m, tea.KeyCtrlE)
	out := m.View()
	if !strings.Contains(out, "@exec >") {
		t.Fatal("exec view should show the @exec bar")
	}
	if strings.Contains(out, "@edit") {
		t.Fatal("exec view should replace, not stack under, the @edit status line")
	}

	// Esc brings the @edit status line back.
	m = ctrl(m, tea.KeyEsc)
	if !strings.Contains(m.View(), "@edit") {
		t.Fatal("exiting exec should restore the @edit status line")
	}
}

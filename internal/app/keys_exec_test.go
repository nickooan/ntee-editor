package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// execLineFixture writes an n-line file ("line 1"…"line n") and opens it in edit
// mode, for exec commands that need a tall buffer (jump anchoring, copy ranges).
func execLineFixture(t *testing.T, n int) Model {
	t.Helper()
	m, root := newTestModel(t, nil)
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	must(t, os.WriteFile(filepath.Join(root, "big.go"), []byte(b.String()), 0o644))
	return m.openFileAt("big.go")
}

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

func TestExecJumpAnchors(t *testing.T) {
	m := execLineFixture(t, 40)
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "jump 20")
	m = ctrl(m, tea.KeyEnter)

	if m.mode != modeEdit {
		t.Fatalf("jump should return to edit mode, got %v", m.mode)
	}
	if m.edit.cy != 19 {
		t.Fatalf("cy = %d, want 19", m.edit.cy)
	}
	want := anchorScroll(19, m.contentHeight(), len(m.edit.lines))
	if m.fileScrollY != want {
		t.Fatalf("fileScrollY = %d, want %d (30%% anchor)", m.fileScrollY, want)
	}
}

func TestExecJumpBadArg(t *testing.T) {
	m := execLineFixture(t, 10)
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "jump abc")
	m = ctrl(m, tea.KeyEnter)
	if m.errText != "jump needs a line number, top, or end" {
		t.Fatalf("errText = %q", m.errText)
	}
	if m.mode != modeExec {
		t.Fatal("bad jump should stay in exec mode")
	}
}

func TestExecJumpTopAndEnd(t *testing.T) {
	m := execLineFixture(t, 40)

	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "jump end")
	m = ctrl(m, tea.KeyEnter)
	if m.edit.cy != len(m.edit.lines)-1 {
		t.Fatalf("jump end cy = %d, want %d", m.edit.cy, len(m.edit.lines)-1)
	}

	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "jump top")
	m = ctrl(m, tea.KeyEnter)
	if m.edit.cy != 0 || m.fileScrollY != 0 {
		t.Fatalf("jump top cy=%d scrollY=%d, want 0/0", m.edit.cy, m.fileScrollY)
	}
}

func TestExecAliases(t *testing.T) {
	m := execLineFixture(t, 40)
	var captured string
	m.copyClipboard = func(s string) error { captured = s; return nil }

	// cp == copy
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "cp 1-2")
	m = ctrl(m, tea.KeyEnter)
	if captured != "line 1\nline 2\n" {
		t.Fatalf("cp alias = %q", captured)
	}

	// jp == jump
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "jp 15")
	m = ctrl(m, tea.KeyEnter)
	if m.edit.cy != 14 || m.mode != modeEdit {
		t.Fatalf("jp alias cy=%d mode=%v", m.edit.cy, m.mode)
	}
}

func TestExecCopyRange(t *testing.T) {
	m := execLineFixture(t, 40)
	var captured string
	m.copyClipboard = func(s string) error { captured = s; return nil }
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "copy 1-3")
	m = ctrl(m, tea.KeyEnter)
	if captured != "line 1\nline 2\nline 3\n" {
		t.Fatalf("range copy = %q", captured)
	}
	if m.notice != "copied" || m.mode != modeEdit {
		t.Fatalf("notice=%q mode=%v", m.notice, m.mode)
	}
}

func TestExecCopyAll(t *testing.T) {
	m := execLineFixture(t, 5)
	var captured string
	m.copyClipboard = func(s string) error { captured = s; return nil }
	want := m.edit.content() + "\n"
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "copy all")
	m = ctrl(m, tea.KeyEnter)
	if captured != want {
		t.Fatalf("copy all = %q, want %q", captured, want)
	}
}

func TestExecCopyFpath(t *testing.T) {
	m := execLineFixture(t, 3)
	var captured string
	m.copyClipboard = func(s string) error { captured = s; return nil }
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "copy fpath")
	m = ctrl(m, tea.KeyEnter)
	if captured != "big.go" {
		t.Fatalf("copy fpath = %q, want %q", captured, "big.go")
	}
}

func TestExecCopyBadRange(t *testing.T) {
	m := execLineFixture(t, 5)
	called := false
	m.copyClipboard = func(string) error { called = true; return nil }
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "copy x")
	m = ctrl(m, tea.KeyEnter)
	if called {
		t.Fatal("bad range must not copy")
	}
	if m.errText != "copy: bad range: x" {
		t.Fatalf("errText = %q", m.errText)
	}
	if m.mode != modeExec {
		t.Fatal("bad range should stay in exec")
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

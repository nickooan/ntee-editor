package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// conflictFixture opens a file containing one conflict block and returns the
// model in edit mode with the cursor on the given line.
//
//	0 package main        4 =======
//	1                     5 const databaseUrl = "dev"
//	2 <<<<<<< HEAD        6 >>>>>>> feature/login
//	3 …= "prod"           7 (blank)   8 func main() {}
func conflictFixture(t *testing.T, cursorLine int) Model {
	t.Helper()
	m, root := newTestModel(t, nil)
	content := "package main\n" +
		"\n" +
		"<<<<<<< HEAD\n" +
		"const databaseUrl = \"prod\"\n" +
		"=======\n" +
		"const databaseUrl = \"dev\"\n" +
		">>>>>>> feature/login\n" +
		"\n" +
		"func main() {}\n"
	must(t, os.WriteFile(filepath.Join(root, "conf.go"), []byte(content), 0o644))
	m = m.openFileAt("conf.go")
	m.edit.cy, m.edit.cx = cursorLine, 0
	return m
}

// multiConflictFixture opens a file with two blocks — the second in diff3
// form — and the cursor at the top.
//
//	0 package main    5 >>>>>>> branch/one   10 base2
//	1 <<<<<<< HEAD    6 middle               11 =======
//	2 a1              7 <<<<<<< HEAD         12 b2
//	3 =======         8 a2                   13 >>>>>>> branch/two
//	4 b1              9 ||||||| ancestor     14 tail
func multiConflictFixture(t *testing.T) Model {
	t.Helper()
	m, root := newTestModel(t, nil)
	content := strings.Join([]string{
		"package main",
		"<<<<<<< HEAD",
		"a1",
		"=======",
		"b1",
		">>>>>>> branch/one",
		"middle",
		"<<<<<<< HEAD",
		"a2",
		"||||||| ancestor",
		"base2",
		"=======",
		"b2",
		">>>>>>> branch/two",
		"tail",
	}, "\n") + "\n"
	must(t, os.WriteFile(filepath.Join(root, "multi.go"), []byte(content), 0o644))
	m = m.openFileAt("multi.go")
	m.edit.cy, m.edit.cx = 0, 0
	return m
}

// enterConflictMode types "git scf" into the @exec bar and runs it.
func enterConflictMode(m Model) Model {
	m = key(m, ctrlKey('e'))
	m = runes(m, "git scf")
	return key(m, keyPress(tea.KeyEnter))
}

func TestExecGitScfEntersConflictMode(t *testing.T) {
	m := conflictFixture(t, 2)
	before := m.edit.content()
	m = enterConflictMode(m)

	if m.mode != modeConflict {
		t.Fatalf("mode = %v, want modeConflict (err=%q)", m.mode, m.errText)
	}
	if len(m.conflictBlocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(m.conflictBlocks))
	}
	if m.edit.content() != before {
		t.Fatal("entering the mode must not touch the buffer")
	}
	if m.edit.cy != 2 {
		t.Fatalf("cursor moved on entry: cy = %d", m.edit.cy)
	}
	// The cursor sits on the <<<<<<< marker, so the popup is armed.
	if m.conflictChoiceIdx != 0 || m.conflictChoice != 0 {
		t.Fatalf("popup not armed: idx=%d choice=%d", m.conflictChoiceIdx, m.conflictChoice)
	}
}

func TestExecGitScfNoConflictsStaysInExec(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m = enterConflictMode(m)

	if m.mode != modeExec {
		t.Fatalf("should stay in exec, got %v", m.mode)
	}
	if m.errText != "no conflict markers in this file" {
		t.Fatalf("errText = %q", m.errText)
	}
}

func TestExecGitScfWithArgErrors(t *testing.T) {
	m := conflictFixture(t, 2)
	before := m.edit.content()
	m = key(m, ctrlKey('e'))
	m = runes(m, "git scf head")
	m = key(m, keyPress(tea.KeyEnter))

	if m.mode != modeExec {
		t.Fatalf("old-style arg should stay in exec, got %v", m.mode)
	}
	if m.errText != "git scf takes no argument" {
		t.Fatalf("errText = %q", m.errText)
	}
	if m.edit.content() != before {
		t.Fatal("buffer must be untouched")
	}
}

func TestExecGitUnknownSubcommand(t *testing.T) {
	m := conflictFixture(t, 2)
	m = key(m, ctrlKey('e'))
	m = runes(m, "git foo")
	m = key(m, keyPress(tea.KeyEnter))
	if m.mode != modeExec {
		t.Fatal("unknown git subcommand should stay in exec")
	}
	if !strings.Contains(m.errText, "unknown git command") {
		t.Fatalf("errText = %q", m.errText)
	}
}

func TestConflictModeIsReadOnly(t *testing.T) {
	m := conflictFixture(t, 3) // cursor on content, off the markers
	m = enterConflictMode(m)
	before := m.edit.content()
	rev := m.edit.rev
	for _, msg := range []tea.KeyPressMsg{
		typeRune('x'), keyPress(tea.KeyEnter), keyPress(tea.KeyBackspace),
		ctrlKey('s'), ctrlKey('z'), keyPress(tea.KeySpace), keyPress(tea.KeyTab),
	} {
		m = key(m, msg)
	}
	if m.edit.content() != before || m.edit.rev != rev {
		t.Fatal("conflict mode must not reach the buffer off the markers")
	}
	if m.mode != modeConflict {
		t.Fatalf("mode = %v, want modeConflict", m.mode)
	}
	res, _ := m.handlePaste("pasted")
	m = res.(Model)
	if m.edit.content() != before {
		t.Fatal("paste must be inert in conflict mode")
	}
}

func TestConflictNavigationAndShiftJumps(t *testing.T) {
	m := multiConflictFixture(t)
	m = enterConflictMode(m)

	m = key(m, keyPress(tea.KeyDown))
	if m.edit.cy != 1 {
		t.Fatalf("down: cy = %d, want 1", m.edit.cy)
	}
	m = key(m, keyPress(tea.KeyUp))
	if m.edit.cy != 0 {
		t.Fatalf("up: cy = %d, want 0", m.edit.cy)
	}

	m = key(m, shiftKey(tea.KeyDown))
	if m.edit.cy != 1 {
		t.Fatalf("shift+down: cy = %d, want block 1 start", m.edit.cy)
	}
	m = key(m, shiftKey(tea.KeyDown))
	if m.edit.cy != 7 {
		t.Fatalf("shift+down: cy = %d, want block 2 start", m.edit.cy)
	}
	m = key(m, shiftKey(tea.KeyDown)) // no block below: inert
	if m.edit.cy != 7 {
		t.Fatalf("shift+down past the last block moved to %d", m.edit.cy)
	}
	m = key(m, shiftKey(tea.KeyUp))
	if m.edit.cy != 1 {
		t.Fatalf("shift+up: cy = %d, want block 1 start", m.edit.cy)
	}
	m = key(m, shiftKey(tea.KeyUp)) // no block above: inert
	if m.edit.cy != 1 {
		t.Fatalf("shift+up past the first block moved to %d", m.edit.cy)
	}
}

func TestConflictPopupCycleAndReset(t *testing.T) {
	m := conflictFixture(t, 2)
	m = enterConflictMode(m)

	// →/← cycle the three options on a marker line.
	m = key(m, keyPress(tea.KeyRight))
	m = key(m, keyPress(tea.KeyRight))
	if m.conflictChoice != 2 {
		t.Fatalf("choice = %d, want 2", m.conflictChoice)
	}
	m = key(m, keyPress(tea.KeyRight)) // wraps
	if m.conflictChoice != 0 {
		t.Fatalf("choice = %d, want wrap to 0", m.conflictChoice)
	}
	m = key(m, keyPress(tea.KeyLeft))
	if m.conflictChoice != 2 {
		t.Fatalf("choice = %d, want 2 after left-wrap", m.conflictChoice)
	}

	// Off the markers the popup closes and ←/→ move the column instead.
	m = key(m, keyPress(tea.KeyDown))
	if m.conflictChoiceIdx != -1 {
		t.Fatalf("popup should close off-marker, idx = %d", m.conflictChoiceIdx)
	}
	m = key(m, keyPress(tea.KeyRight))
	if m.edit.cx != 1 {
		t.Fatalf("right off-marker should move the column, cx = %d", m.edit.cx)
	}

	// Landing back on a marker re-arms the popup at the first option.
	m = key(m, keyPress(tea.KeyUp))
	if m.conflictChoiceIdx != 0 || m.conflictChoice != 0 {
		t.Fatalf("popup not re-armed: idx=%d choice=%d", m.conflictChoiceIdx, m.conflictChoice)
	}
}

func TestConflictApplyOurs(t *testing.T) {
	m := conflictFixture(t, 2)
	m = enterConflictMode(m)
	m = key(m, keyPress(tea.KeyEnter))

	got := m.edit.content()
	if !strings.Contains(got, `"prod"`) || strings.Contains(got, `"dev"`) {
		t.Fatalf("ours not kept: %q", got)
	}
	if strings.Contains(got, "<<<<<<<") || strings.Contains(got, "=======") || strings.Contains(got, ">>>>>>>") {
		t.Fatalf("markers remain: %q", got)
	}
	if m.mode != modeConflict {
		t.Fatalf("apply must stay in conflict mode, got %v", m.mode)
	}
	if !m.edit.dirty {
		t.Fatal("buffer should be dirty after an apply")
	}
	if len(m.conflictBlocks) != 0 {
		t.Fatalf("blocks should re-parse to none, got %d", len(m.conflictBlocks))
	}
	// Cursor stays in place, sitting on the resolved content.
	if m.edit.cy != 2 || m.edit.lines[2] != `const databaseUrl = "prod"` {
		t.Fatalf("cursor at %d on %q, want the resolved line", m.edit.cy, m.edit.lines[m.edit.cy])
	}
	if m.notice != "resolved → HEAD" {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestConflictApplyTheirs(t *testing.T) {
	m := conflictFixture(t, 2)
	m = enterConflictMode(m)
	m = key(m, keyPress(tea.KeyRight)) // Use feature/login
	m = key(m, keyPress(tea.KeyEnter))

	got := m.edit.content()
	if !strings.Contains(got, `"dev"`) || strings.Contains(got, `"prod"`) {
		t.Fatalf("theirs not kept: %q", got)
	}
	if strings.Contains(got, "<<<<<<<") {
		t.Fatalf("markers remain: %q", got)
	}
	if m.notice != "resolved → feature/login" {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestConflictApplyBoth(t *testing.T) {
	m := conflictFixture(t, 2)
	m = enterConflictMode(m)
	m = key(m, keyPress(tea.KeyRight))
	m = key(m, keyPress(tea.KeyRight)) // Use both
	m = key(m, keyPress(tea.KeyEnter))

	got := m.edit.content()
	if !strings.Contains(got, `"prod"`) || !strings.Contains(got, `"dev"`) {
		t.Fatalf("both sides must be kept: %q", got)
	}
	if strings.Index(got, `"prod"`) > strings.Index(got, `"dev"`) {
		t.Fatalf("ours must precede theirs: %q", got)
	}
	if strings.Contains(got, "<<<<<<<") || strings.Contains(got, "=======") || strings.Contains(got, ">>>>>>>") {
		t.Fatalf("markers must be removed: %q", got)
	}
}

func TestConflictApplyReparsesRemainingBlocks(t *testing.T) {
	m := multiConflictFixture(t)
	m = enterConflictMode(m)
	m = key(m, shiftKey(tea.KeyDown)) // block 1's <<<<<<<
	m = key(m, keyPress(tea.KeyEnter))

	got := m.edit.content()
	if !strings.Contains(got, "a1") || strings.Contains(got, "b1") {
		t.Fatalf("block 1 not resolved to ours: %q", got)
	}
	if !strings.Contains(got, ">>>>>>> branch/two") {
		t.Fatalf("block 2 must stay untouched: %q", got)
	}
	// Block 1's five lines collapsed to one (net -4): block 2 re-parses at 3.
	if len(m.conflictBlocks) != 1 || m.conflictBlocks[0].start != 3 {
		t.Fatalf("re-parse = %+v, want one block starting at 3", m.conflictBlocks)
	}
}

func TestConflictApplyFromMidEndAndBaseMarkers(t *testing.T) {
	// ======= line.
	m := conflictFixture(t, 4)
	m = enterConflictMode(m)
	m = key(m, keyPress(tea.KeyEnter))
	if got := m.edit.content(); !strings.Contains(got, `"prod"`) || strings.Contains(got, "=======") {
		t.Fatalf("apply from ======= failed: %q", got)
	}

	// >>>>>>> line.
	m = conflictFixture(t, 6)
	m = enterConflictMode(m)
	m = key(m, keyPress(tea.KeyRight))
	m = key(m, keyPress(tea.KeyEnter))
	if got := m.edit.content(); !strings.Contains(got, `"dev"`) || strings.Contains(got, ">>>>>>>") {
		t.Fatalf("apply from >>>>>>> failed: %q", got)
	}

	// diff3 ||||||| line; the base section is always dropped, even for "both".
	m = multiConflictFixture(t)
	m.edit.cy = 9
	m = enterConflictMode(m)
	m = key(m, keyPress(tea.KeyRight))
	m = key(m, keyPress(tea.KeyRight)) // Use both
	m = key(m, keyPress(tea.KeyEnter))
	got := m.edit.content()
	if !strings.Contains(got, "a2\nb2") {
		t.Fatalf("both must keep ours then theirs: %q", got)
	}
	if strings.Contains(got, "base2") || strings.Contains(got, "|||||||") {
		t.Fatalf("diff3 base must be dropped: %q", got)
	}
}

func TestConflictApplyOnStrayMarkerInert(t *testing.T) {
	m, root := newTestModel(t, nil)
	content := strings.Join([]string{
		"code",
		"=======", // stray: no surrounding block
		"more",
		"<<<<<<< HEAD",
		"x",
		"=======",
		"y",
		">>>>>>> b",
	}, "\n") + "\n"
	must(t, os.WriteFile(filepath.Join(root, "stray.go"), []byte(content), 0o644))
	m = m.openFileAt("stray.go")
	m.edit.cy = 1 // the stray =======
	m = enterConflictMode(m)

	before := m.edit.content()
	if m.conflictChoiceIdx != -1 {
		t.Fatal("a stray marker must not arm the popup")
	}
	m = key(m, keyPress(tea.KeyEnter))
	if m.edit.content() != before {
		t.Fatal("enter on a stray marker must not edit the buffer")
	}
}

func TestConflictEscKeepsCursorAndEdits(t *testing.T) {
	m := multiConflictFixture(t)
	m = enterConflictMode(m)
	m = key(m, shiftKey(tea.KeyDown)) // block 1
	m = key(m, keyPress(tea.KeyEnter))
	m = key(m, keyPress(tea.KeyDown))
	m = key(m, keyPress(tea.KeyDown))
	cy := m.edit.cy
	m = key(m, keyPress(tea.KeyEsc))

	if m.mode != modeEdit {
		t.Fatalf("esc should land in edit mode, got %v", m.mode)
	}
	if m.edit.cy != cy {
		t.Fatalf("esc moved the cursor: %d, want %d", m.edit.cy, cy)
	}
	got := m.edit.content()
	if !strings.Contains(got, "a1") || strings.Contains(got, "b1") {
		t.Fatalf("applied resolution must persist: %q", got)
	}
	if !strings.Contains(got, ">>>>>>> branch/two") {
		t.Fatalf("unresolved conflict must stay: %q", got)
	}
	if len(m.conflictBlocks) != 0 {
		t.Fatal("esc should clear conflict state")
	}
}

func TestConflictUndoAfterExit(t *testing.T) {
	m := conflictFixture(t, 2)
	before := m.edit.content()
	m = enterConflictMode(m)
	m = key(m, keyPress(tea.KeyEnter))
	if m.edit.content() == before {
		t.Fatal("apply did not change the buffer")
	}
	m = key(m, keyPress(tea.KeyEsc))
	m = key(m, ctrlKey('z'))
	if m.edit.content() != before {
		t.Fatalf("undo should restore the conflict, got %q", m.edit.content())
	}
}

func TestConflictBlockAtEOFEmptySide(t *testing.T) {
	m, root := newTestModel(t, nil)
	// The whole file is one block whose theirs side is empty (no trailing
	// newline, so the >>>>>>> is the last line).
	content := "<<<<<<< HEAD\nx\n=======\n>>>>>>> b"
	must(t, os.WriteFile(filepath.Join(root, "eof.go"), []byte(content), 0o644))
	m = m.openFileAt("eof.go")
	m.edit.cy = 0
	m = enterConflictMode(m)
	m = key(m, keyPress(tea.KeyRight)) // Use b (empty side)
	m = key(m, keyPress(tea.KeyEnter))

	if len(m.edit.lines) != 1 || m.edit.lines[0] != "" {
		t.Fatalf("empty resolve should leave one empty line, got %q", m.edit.lines)
	}
	if m.edit.cy != 0 || m.edit.cx != 0 {
		t.Fatalf("cursor = %d,%d, want 0,0", m.edit.cy, m.edit.cx)
	}
}

func TestConflictWheelScrollMovesCursor(t *testing.T) {
	m := multiConflictFixture(t)
	m = enterConflictMode(m)
	m = m.wheelScroll(1)
	if m.edit.cy != wheelScrollLines {
		t.Fatalf("wheel: cy = %d, want %d", m.edit.cy, wheelScrollLines)
	}
}

func TestRenderConflictSmoke(t *testing.T) {
	m := conflictFixture(t, 2)
	m = enterConflictMode(m)

	frame := ansi.Strip(m.render())
	if !strings.Contains(frame, "Use HEAD") || !strings.Contains(frame, "Use both") {
		t.Fatalf("popup missing from the frame:\n%s", frame)
	}
	if !strings.Contains(frame, "@conflict") || !strings.Contains(frame, "1 conflict(s) left") {
		t.Fatalf("status line wrong:\n%s", frame)
	}

	// Off the markers the popup disappears.
	m = key(m, keyPress(tea.KeyDown))
	if strings.Contains(ansi.Strip(m.render()), "Use HEAD") {
		t.Fatal("popup should not render off a marker line")
	}

	// Tiny pane must not panic.
	m.width, m.height = 20, 8
	_ = m.render()
}

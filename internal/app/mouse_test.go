package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Test geometry (newTestModel: width=100, height=30, main.go fixture opened):
// sidebarWidth=25 → pane inner content starts at column 26; the 5-line file
// gives gutterWidth=2, so text starts at column 26+5=31. Tabs exist after
// openFileAt → tabRows=2, so text starts at row 2+2=4. contentHeight=23.
const (
	testTextX = 31
	testTextY = 4
)

func click(m Model, x, y int) Model {
	next, _ := m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	return next.(Model)
}

func wheel(m Model, button tea.MouseButton) Model {
	next, _ := m.Update(tea.MouseMsg{X: testTextX, Y: testTextY, Action: tea.MouseActionPress, Button: button})
	return next.(Model)
}

func mouseFixture(t *testing.T) Model {
	t.Helper()
	m, _ := newTestModel(t, nil)
	return m.openFileAt("main.go") // "package main\n\nfunc main() {\n}\n"
}

func TestClickMovesCursor(t *testing.T) {
	m := mouseFixture(t)
	m = click(m, testTextX+4, testTextY+2) // line 2 "func main() {", col 4
	if m.edit.cy != 2 || m.edit.cx != 4 {
		t.Fatalf("cursor = (%d,%d), want (2,4)", m.edit.cy, m.edit.cx)
	}
}

func TestClickClampsToLineEnd(t *testing.T) {
	m := mouseFixture(t)
	m = click(m, testTextX+50, testTextY) // far past "package main" (12 runes)
	if m.edit.cy != 0 || m.edit.cx != 12 {
		t.Fatalf("cursor = (%d,%d), want (0,12)", m.edit.cy, m.edit.cx)
	}
}

func TestClickGutterJumpsToColumnZero(t *testing.T) {
	m := mouseFixture(t)
	m.edit.cy, m.edit.cx = 0, 5
	m = click(m, testTextX-3, testTextY+2) // inside the "NN │ " gutter of line 2
	if m.edit.cy != 2 || m.edit.cx != 0 {
		t.Fatalf("cursor = (%d,%d), want (2,0)", m.edit.cy, m.edit.cx)
	}
}

func TestClickOutsideContentIgnored(t *testing.T) {
	m := mouseFixture(t)
	m.edit.cy, m.edit.cx = 2, 4
	for name, at := range map[string][2]int{
		"sidebar":    {10, testTextY},
		"header":     {testTextX, 0},
		"tab strip":  {testTextX, 2},
		"below EOF":  {testTextX, testTextY + 10},
		"status row": {testTextX, 29},
	} {
		m = click(m, at[0], at[1])
		if m.edit.cy != 2 || m.edit.cx != 4 {
			t.Fatalf("%s click moved the cursor to (%d,%d)", name, m.edit.cy, m.edit.cx)
		}
	}
}

func TestClickClearsSelection(t *testing.T) {
	m := mouseFixture(t)
	m.edit.cy, m.edit.cx = 0, 0
	m = ctrl(m, tea.KeyCtrlA)
	if m.edit.sel == nil {
		t.Fatal("expected a selection")
	}
	m = click(m, testTextX, testTextY+2)
	if m.edit.sel != nil || m.edit.selLineMode {
		t.Fatal("click should clear the selection")
	}
}

func TestClickOnScrolledCursorLine(t *testing.T) {
	m, root := newTestModel(t, nil)
	long := strings.Repeat("abcdefghij", 10) // 100 runes
	must(t, os.WriteFile(filepath.Join(root, "long.txt"), []byte(long+"\nshort\n"), 0o644))
	m = m.openFileAt("long.txt")
	// contentWidth = (mainWidth-4)-gutterWidth-3 = 71-2-3 = 66; cx=90 → the
	// cursor line renders with off = 90-66+1 = 25.
	m.edit.cy, m.edit.cx = 0, 90
	m = click(m, testTextX+10, testTextY) // contentCol 10 → col 25+10
	if m.edit.cy != 0 || m.edit.cx != 35 {
		t.Fatalf("cursor = (%d,%d), want (0,35)", m.edit.cy, m.edit.cx)
	}
	// A non-cursor line has no window: same x on line 1 lands at col 5 (EOL clamp).
	m = click(m, testTextX+10, testTextY+1)
	if m.edit.cy != 1 || m.edit.cx != 5 {
		t.Fatalf("cursor = (%d,%d), want (1,5)", m.edit.cy, m.edit.cx)
	}
}

func TestWheelScrollsCursorInEditMode(t *testing.T) {
	m, root := newTestModel(t, nil)
	var b strings.Builder
	for i := 0; i < 30; i++ {
		b.WriteString("line\n")
	}
	must(t, os.WriteFile(filepath.Join(root, "tall.txt"), []byte(b.String()), 0o644))
	m = m.openFileAt("tall.txt")
	m.edit.cy, m.edit.cx = 10, 0

	m = wheel(m, tea.MouseButtonWheelDown)
	if m.edit.cy != 10+wheelScrollLines {
		t.Fatalf("wheel down: cy = %d, want %d", m.edit.cy, 10+wheelScrollLines)
	}
	m = wheel(m, tea.MouseButtonWheelUp)
	if m.edit.cy != 10 {
		t.Fatalf("wheel up: cy = %d, want 10", m.edit.cy)
	}

	// Clamps at the top and bottom.
	m.edit.cy = 1
	m = wheel(m, tea.MouseButtonWheelUp)
	if m.edit.cy != 0 {
		t.Fatalf("wheel up should clamp at 0, got %d", m.edit.cy)
	}
	last := len(m.edit.lines) - 1
	m.edit.cy = last - 1
	m = wheel(m, tea.MouseButtonWheelDown)
	if m.edit.cy != last {
		t.Fatalf("wheel down should clamp at last line %d, got %d", last, m.edit.cy)
	}
}

func TestHorizontalWheelDoesNothing(t *testing.T) {
	m := mouseFixture(t)
	m.edit.cy, m.edit.cx = 2, 4
	before := m.edit.content()
	for _, btn := range []tea.MouseButton{tea.MouseButtonWheelLeft, tea.MouseButtonWheelRight} {
		m = wheel(m, btn)
		if m.edit.cy != 2 || m.edit.cx != 4 {
			t.Fatalf("horizontal wheel %v moved the cursor to (%d,%d)", btn, m.edit.cy, m.edit.cx)
		}
		if m.edit.content() != before {
			t.Fatalf("horizontal wheel %v changed the buffer", btn)
		}
	}
}

func TestWheelScrollsFileInQueryMode(t *testing.T) {
	m, root := newTestModel(t, nil)
	var b strings.Builder
	for i := 0; i < 30; i++ {
		b.WriteString("line\n")
	}
	must(t, os.WriteFile(filepath.Join(root, "tall.txt"), []byte(b.String()), 0o644))
	m = m.openFileAt("tall.txt") // opens in edit mode
	m = ctrl(m, tea.KeyEsc)      // back to query mode, file still shown
	if m.mode != modeQuery {
		t.Fatalf("expected query mode, got %v", m.mode)
	}
	before := m.edit.cy
	m = wheel(m, tea.MouseButtonWheelDown)
	if m.fileScrollY != wheelScrollLines {
		t.Fatalf("query wheel down: fileScrollY = %d, want %d", m.fileScrollY, wheelScrollLines)
	}
	if m.edit.cy != before {
		t.Fatal("query-mode scroll must not move the edit cursor")
	}
}

func TestWheelIgnoredWithOverlayOrNoFile(t *testing.T) {
	// Overlay open: wheel is a no-op.
	m := mouseFixture(t)
	m.edit.cy = 5
	m.fuzzyOpen = true
	if got := wheel(m, tea.MouseButtonWheelDown); got.edit.cy != 5 {
		t.Fatalf("wheel with overlay open moved cursor to %d", got.edit.cy)
	}

	// Query mode, no open file: no panic, no-op.
	m2, _ := newTestModel(t, nil)
	if got := wheel(m2, tea.MouseButtonWheelDown); got.fileScrollY != 0 {
		t.Fatalf("wheel with no file scrolled to %d", got.fileScrollY)
	}
}

func TestClickIgnoredOutsideEditMode(t *testing.T) {
	m, _ := newTestModel(t, nil) // query mode
	before := m
	m = click(m, testTextX, testTextY)
	if m.mode != before.mode || m.edit.cy != before.edit.cy {
		t.Fatal("click in query mode must be a no-op")
	}
}

func TestNonLeftClickIgnored(t *testing.T) {
	m := mouseFixture(t)
	m.edit.cy, m.edit.cx = 2, 4
	for _, ev := range []tea.MouseMsg{
		{X: testTextX, Y: testTextY, Action: tea.MouseActionPress, Button: tea.MouseButtonRight},
		{X: testTextX, Y: testTextY, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft},
		{X: testTextX, Y: testTextY, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft},
	} {
		next, _ := m.Update(ev)
		m = next.(Model)
		if m.edit.cy != 2 || m.edit.cx != 4 {
			t.Fatalf("event %+v moved the cursor", ev)
		}
	}
}

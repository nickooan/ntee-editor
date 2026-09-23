package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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
	next, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	return next.(Model)
}

func wheel(m Model, button tea.MouseButton) Model {
	next, _ := m.Update(tea.MouseWheelMsg{X: testTextX, Y: testTextY, Button: button})
	return next.(Model)
}

func ctrlClick(m Model, x, y int) (Model, tea.Cmd) {
	next, cmd := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft, Mod: tea.ModCtrl})
	return next.(Model), cmd
}

// "package main" — "main" identifier starts at rune column 8 on line 0.
const testIdentX = testTextX + 8

func TestCtrlClickJumpsToDefinition(t *testing.T) {
	m, client := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	client.locs = selfLoc(m, 2) // definition sits at line 2 of main.go

	m, cmd := ctrlClick(m, testIdentX, testTextY) // click "main" on line 0
	if m.edit.cy != 0 || m.edit.cx != 8 {
		t.Fatalf("ctrl+click should move the cursor to the token, got (%d,%d)", m.edit.cy, m.edit.cx)
	}
	if cmd == nil {
		t.Fatal("ctrl+click on an identifier should issue a definition request")
	}
	next, _ := m.Update(cmd()) // drive the async definition response
	m = next.(Model)
	if m.edit.cy != 2 {
		t.Fatalf("definition should jump to line 2, got %d", m.edit.cy)
	}
}

func TestPlainClickDoesNotJump(t *testing.T) {
	m, client := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	client.locs = selfLoc(m, 2)
	next, cmd := m.Update(tea.MouseClickMsg{X: testIdentX, Y: testTextY, Button: tea.MouseLeft})
	m = next.(Model)
	if m.edit.cy != 0 || m.edit.cx != 8 {
		t.Fatalf("plain click should still move the cursor, got (%d,%d)", m.edit.cy, m.edit.cx)
	}
	if cmd != nil {
		t.Fatal("plain click must not issue a definition request")
	}
}

func TestCtrlRightClickJumps(t *testing.T) {
	m, client := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	client.locs = selfLoc(m, 2)
	next, cmd := m.Update(tea.MouseClickMsg{X: testIdentX, Y: testTextY, Button: tea.MouseRight, Mod: tea.ModCtrl})
	m = next.(Model)
	if cmd == nil || m.edit.cy != 0 || m.edit.cx != 8 {
		t.Fatalf("ctrl+right-click should move cursor and jump: (%d,%d) cmd=%v", m.edit.cy, m.edit.cx, cmd)
	}
}

func TestBareRightClickIgnored(t *testing.T) {
	m := mouseFixture(t)
	m.edit.cy, m.edit.cx = 2, 1
	next, cmd := m.Update(tea.MouseClickMsg{X: testIdentX, Y: testTextY, Button: tea.MouseRight})
	m = next.(Model)
	if m.edit.cy != 2 || m.edit.cx != 1 || cmd != nil {
		t.Fatalf("bare right-click should be a no-op: (%d,%d) cmd=%v", m.edit.cy, m.edit.cx, cmd)
	}
}

func TestCtrlClickOutsideContentIgnored(t *testing.T) {
	m, client := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	client.locs = selfLoc(m, 2)
	m.edit.cy, m.edit.cx = 0, 0
	m, cmd := ctrlClick(m, 5, testTextY) // sidebar column
	if m.edit.cy != 0 || m.edit.cx != 0 || cmd != nil {
		t.Fatalf("ctrl+click outside content should do nothing: (%d,%d) cmd=%v", m.edit.cy, m.edit.cx, cmd)
	}
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
	m = key(m, ctrlKey('a'))
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

// tallFixture opens a file of n "line" rows (plus the trailing empty line), tall
// enough to scroll — for viewport-follow tests.
func tallFixture(t *testing.T, n int) Model {
	t.Helper()
	m, root := newTestModel(t, nil)
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("line\n")
	}
	must(t, os.WriteFile(filepath.Join(root, "tall.txt"), []byte(b.String()), 0o644))
	return m.openFileAt("tall.txt")
}

// Click-driven viewport rules: an ordinary click never scrolls the view; only a
// click on the top visible line pages up (that line re-renders at the bottom
// row) and a click on the bottom visible line pages down (it re-renders at the
// top row).

func TestClickMidPaneDoesNotScroll(t *testing.T) {
	m := tallFixture(t, 100)
	h := m.contentHeight() + 1
	total := len(m.edit.lines)
	// Wheel/arrow navigation moves only the cursor, leaving fileScrollY stale.
	m.edit.cy = 50
	top := fileViewportTop(m.edit.cy, m.fileScrollY, h, total)
	m = click(m, testTextX, testTextY+10)
	if m.edit.cy != top+10 {
		t.Fatalf("cy = %d, want %d (the line visible at the clicked row)", m.edit.cy, top+10)
	}
	if got := fileViewportTop(m.edit.cy, m.fileScrollY, h, total); got != top {
		t.Fatalf("mid-pane click scrolled the view: top %d → %d", top, got)
	}
}

func TestClickTopLinePagesUp(t *testing.T) {
	m := tallFixture(t, 100)
	h := m.contentHeight() + 1
	total := len(m.edit.lines)
	m.edit.cy = 50
	top := fileViewportTop(m.edit.cy, m.fileScrollY, h, total)
	m = click(m, testTextX, testTextY) // top visible row
	if m.edit.cy != top {
		t.Fatalf("cy = %d, want the clicked top line %d", m.edit.cy, top)
	}
	newTop := fileViewportTop(m.edit.cy, m.fileScrollY, h, total)
	if newTop != top-h+1 || m.edit.cy-newTop != h-1 {
		t.Fatalf("top-line click should render it at the bottom row: top %d → %d", top, newTop)
	}
}

func TestClickBottomLinePagesDown(t *testing.T) {
	m := tallFixture(t, 100)
	h := m.contentHeight() + 1
	total := len(m.edit.lines)
	m.edit.cy = 50
	top := fileViewportTop(m.edit.cy, m.fileScrollY, h, total)
	bottom := top + h - 1
	m = click(m, testTextX, testTextY+h-1) // bottom visible row
	if m.edit.cy != bottom {
		t.Fatalf("cy = %d, want the clicked bottom line %d", m.edit.cy, bottom)
	}
	if got := fileViewportTop(m.edit.cy, m.fileScrollY, h, total); got != bottom {
		t.Fatalf("bottom-line click should render it at the top row: top %d → %d", top, got)
	}
}

func TestClickEdgeLinesWithoutRoomDoNotScroll(t *testing.T) {
	m := tallFixture(t, 100)
	h := m.contentHeight() + 1
	total := len(m.edit.lines)

	// Top row while line 0 is already visible → nothing above, no page-up.
	m = click(m, testTextX, testTextY)
	if m.edit.cy != 0 || m.fileScrollY != 0 {
		t.Fatalf("top click at top of file: cy=%d scrollY=%d, want 0,0", m.edit.cy, m.fileScrollY)
	}

	// Bottom row while the last line is already visible → nothing below.
	m.edit.cy, m.fileScrollY = total-1, total-h
	m = click(m, testTextX, testTextY+h-1)
	if m.edit.cy != total-1 || m.fileScrollY != total-h {
		t.Fatalf("bottom click at EOF: cy=%d scrollY=%d, want %d,%d", m.edit.cy, m.fileScrollY, total-1, total-h)
	}
}

func TestWheelScrollsCursorInEditMode(t *testing.T) {
	m := tallFixture(t, 30)
	m.edit.cy, m.edit.cx = 10, 0

	m = wheel(m, tea.MouseWheelDown)
	if m.edit.cy != 10+wheelScrollLines {
		t.Fatalf("wheel down: cy = %d, want %d", m.edit.cy, 10+wheelScrollLines)
	}
	m = wheel(m, tea.MouseWheelUp)
	if m.edit.cy != 10 {
		t.Fatalf("wheel up: cy = %d, want 10", m.edit.cy)
	}

	// Clamps at the top and bottom.
	m.edit.cy = 1
	m = wheel(m, tea.MouseWheelUp)
	if m.edit.cy != 0 {
		t.Fatalf("wheel up should clamp at 0, got %d", m.edit.cy)
	}
	last := len(m.edit.lines) - 1
	m.edit.cy = last - 1
	m = wheel(m, tea.MouseWheelDown)
	if m.edit.cy != last {
		t.Fatalf("wheel down should clamp at last line %d, got %d", last, m.edit.cy)
	}
}

func TestHorizontalWheelDoesNothing(t *testing.T) {
	m := mouseFixture(t)
	m.edit.cy, m.edit.cx = 2, 4
	before := m.edit.content()
	for _, btn := range []tea.MouseButton{tea.MouseWheelLeft, tea.MouseWheelRight} {
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
	m := tallFixture(t, 30)          // opens in edit mode
	m = key(m, keyPress(tea.KeyEsc)) // back to query mode, file still shown
	if m.mode != modeQuery {
		t.Fatalf("expected query mode, got %v", m.mode)
	}
	before := m.edit.cy
	m = wheel(m, tea.MouseWheelDown)
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
	if got := wheel(m, tea.MouseWheelDown); got.edit.cy != 5 {
		t.Fatalf("wheel with overlay open moved cursor to %d", got.edit.cy)
	}

	// Query mode, no open file: no panic, no-op.
	m2, _ := newTestModel(t, nil)
	if got := wheel(m2, tea.MouseWheelDown); got.fileScrollY != 0 {
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
	for _, ev := range []tea.Msg{
		tea.MouseClickMsg{X: testTextX, Y: testTextY, Button: tea.MouseRight},
		tea.MouseReleaseMsg{X: testTextX, Y: testTextY, Button: tea.MouseLeft},
		tea.MouseMotionMsg{X: testTextX, Y: testTextY, Button: tea.MouseLeft},
	} {
		next, _ := m.Update(ev)
		m = next.(Model)
		if m.edit.cy != 2 || m.edit.cx != 4 {
			t.Fatalf("event %+v moved the cursor", ev)
		}
	}
}

// sidebarRowOf finds the screen row of a tree entry, mirroring the viewport
// math sidebarListClickIndex uses, so tests stay valid if the fixture's entry
// order changes.
func sidebarRowOf(t *testing.T, m Model, rel string) int {
	t.Helper()
	entries := m.treeEntries()
	start := sidebarWindowStart(len(entries), m.sidebarInnerHeight(), m.highlightedEntryIndex(entries))
	for i := range entries {
		if entries[i].RelativePath == rel {
			return 2 + i - start
		}
	}
	t.Fatalf("no sidebar entry %q", rel)
	return -1
}

func hasEntry(m Model, rel string) bool {
	for _, e := range m.treeEntries() {
		if e.RelativePath == rel {
			return true
		}
	}
	return false
}

func TestSidebarClickOpensFile(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = click(m, 2, sidebarRowOf(t, m, "main.go"))
	if m.mode != modeEdit || m.openRel != "main.go" {
		t.Fatalf("sidebar file click: mode=%v openRel=%q, want edit main.go", m.mode, m.openRel)
	}
	if m.command != "" {
		t.Fatalf("file click should clear the bar, got %q", m.command)
	}
}

func TestSidebarDirClickExpandsWithoutPopup(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = click(m, 2, sidebarRowOf(t, m, "lib"))
	if m.mode != modeQuery {
		t.Fatalf("dir click left query mode: %v", m.mode)
	}
	if m.command != "lib/" || m.selectedCommand != "lib/" {
		t.Fatalf("dir click should confirm lib/: command=%q selected=%q", m.command, m.selectedCommand)
	}
	if !hasEntry(m, "lib/util.ts") {
		t.Fatal("dir click should expand lib in the tree")
	}
	if sugs := m.queryInputSuggestions(m.treeEntries()); len(sugs) != 0 {
		t.Fatalf("popup must stay hidden after a click, got %d suggestions", len(sugs))
	}
	if rows := m.renderQuerySuggestions(40); rows != nil {
		t.Fatalf("popup rows rendered after a click: %v", rows)
	}
}

func TestSidebarDirReclickKeepsExpansion(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = click(m, 2, sidebarRowOf(t, m, "lib"))
	m = click(m, 2, sidebarRowOf(t, m, "lib"))
	if m.command != "lib/" || !hasEntry(m, "lib/util.ts") {
		t.Fatalf("re-click should keep the expansion: command=%q", m.command)
	}
}

func TestSidebarDirClickThenTypingShowsPopup(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = click(m, 2, sidebarRowOf(t, m, "lib"))
	m = key(m, typeRune('u'))
	if m.command != "lib/u" {
		t.Fatalf("typing should continue the clicked path, got %q", m.command)
	}
	if m.suppressQuerySuggestions {
		t.Fatal("typing must lift the popup suppression")
	}
	if sugs := m.queryInputSuggestions(m.treeEntries()); len(sugs) == 0 {
		t.Fatal("typing after a dir click should surface suggestions again")
	}
}

func TestSuppressionLiftedByBackspace(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = click(m, 2, sidebarRowOf(t, m, "lib"))
	m = key(m, keyPress(tea.KeyBackspace))
	if m.command != "lib" || m.suppressQuerySuggestions {
		t.Fatalf("backspace should edit the text and lift suppression: %q %v", m.command, m.suppressQuerySuggestions)
	}
	if sugs := m.queryInputSuggestions(m.treeEntries()); len(sugs) == 0 {
		t.Fatal("suggestions should return once suppression lifts")
	}
}

func TestSuppressedQueryKeysFallBackToSidebar(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = click(m, 2, sidebarRowOf(t, m, "lib"))
	// With the popup suppressed, shift+down walks the sidebar highlight …
	m = key(m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift})
	if m.keyboardSelectedCommand == "" || m.inputSuggestIndex != 0 {
		t.Fatalf("shift+down should move the sidebar highlight, not the popup: %q %d",
			m.keyboardSelectedCommand, m.inputSuggestIndex)
	}
	// … and Enter resolves from the highlight (lib/util.ts, the row after lib).
	m = key(m, keyPress(tea.KeyEnter))
	if m.mode != modeEdit || m.openRel != "lib/util.ts" {
		t.Fatalf("enter should open the highlighted entry: mode=%v openRel=%q", m.mode, m.openRel)
	}
}

func TestSidebarClickMissesInert(t *testing.T) {
	m, _ := newTestModel(t, nil)
	lastRow := 2 + len(m.treeEntries()) // first empty row below the tree
	for name, at := range map[string][2]int{
		"left border":  {0, 2},
		"right border": {24, 2},
		"top border":   {2, 1},
		"empty row":    {2, lastRow},
	} {
		m = click(m, at[0], at[1])
		if m.mode != modeQuery || m.command != "" || m.openRel != "" {
			t.Fatalf("%s click changed state: mode=%v command=%q openRel=%q", name, m.mode, m.command, m.openRel)
		}
	}
}

func TestSidebarFileClickFromEditModePreservesDraft(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "other.go"), []byte("package other\n"), 0o644))
	m = rebuildCorpusNow(m)
	m = m.openFileAt("main.go")
	m = key(m, typeRune('x'))
	if !m.edit.dirty {
		t.Fatal("fixture should be dirty")
	}
	m = click(m, 2, sidebarRowOf(t, m, "other.go"))
	if m.openRel != "other.go" || m.mode != modeEdit {
		t.Fatalf("edit-mode file click: mode=%v openRel=%q", m.mode, m.openRel)
	}
	if !m.draftSet["main.go"] {
		t.Fatal("the outgoing dirty buffer must be stashed (tab stays red)")
	}
	m = click(m, 2, sidebarRowOf(t, m, "main.go"))
	if m.openRel != "main.go" || !m.edit.dirty {
		t.Fatalf("returning should restore the draft: openRel=%q dirty=%v", m.openRel, m.edit.dirty)
	}
}

func TestSidebarDirClickFromEditModeStashes(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m = key(m, typeRune('x'))
	m = click(m, 2, sidebarRowOf(t, m, "lib"))
	if m.mode != modeQuery {
		t.Fatalf("dir click from edit mode should land in query mode, got %v", m.mode)
	}
	if m.command != "lib/" || !m.suppressQuerySuggestions {
		t.Fatalf("dir click should expand with the popup hidden: %q %v", m.command, m.suppressQuerySuggestions)
	}
	if !m.draftSet["main.go"] {
		t.Fatal("unsaved edits must be stashed, not discarded")
	}
	if m.notice != "" {
		t.Fatalf("no discard notice expected, got %q", m.notice)
	}
}

func TestSidebarClickSelectsInspectMenu(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = key(m, ctrlKey('t'))
	// Rows start at y=2. The second menu row is lsp.
	m = click(m, 2, 3)
	if m.mode != modeInspect || m.inspectMenu != inspectMenuLSP {
		t.Fatalf("inspect click: mode=%v menu=%d, want inspect lsp", m.mode, m.inspectMenu)
	}
}

func TestSidebarClickJumpsPreviewOutline(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.mode = modeOpenAPI
	m.preview.outline = []previewOutlineEntry{
		{Label: "Pets", Depth: 0, LineIdx: 0},
		{Label: "GET /pets", Depth: 1, Badge: "GET", Tail: "/pets", LineIdx: 4},
	}
	m.preview.lines = make([]previewLine, 8)
	m.preview.sel = 0
	m.preview.cursor = 0
	m = click(m, 2, 3)
	if m.preview.sel != 1 || m.preview.cursor != 4 {
		t.Fatalf("outline click: sel=%d cursor=%d, want 1 and 4", m.preview.sel, m.preview.cursor)
	}
}

func TestSidebarClickIgnoredInDiff(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.mode = modeDiff
	openRel := m.openRel
	m = click(m, 2, sidebarRowOf(t, m, "lib"))
	if m.mode != modeDiff || m.openRel != openRel || m.command != "" {
		t.Fatalf("diff sidebar click should be a miss: mode=%v open=%q command=%q", m.mode, m.openRel, m.command)
	}
}

// Tab strip geometry (width=100): cells start at x=26; the fixture labels
// " main.go " and " util.ts " are 9 columns each, so tab 0 spans 26..34 and
// tab 1 spans 35..43.

func TestTabClickSwitchesAndKeepsDirty(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m = m.openFileAt("lib/util.ts")
	m = key(m, typeRune('x'))
	m = click(m, 27, 2)
	if m.openRel != "main.go" || m.mode != modeEdit {
		t.Fatalf("tab click: mode=%v openRel=%q, want edit main.go", m.mode, m.openRel)
	}
	if !m.tabDirty(1) {
		t.Fatal("the switched-away tab must stay red")
	}
	m = click(m, 36, 2)
	if m.openRel != "lib/util.ts" || !m.edit.dirty {
		t.Fatalf("clicking back should restore the draft: openRel=%q dirty=%v", m.openRel, m.edit.dirty)
	}
}

func TestTabClickActiveTabIsNoOp(t *testing.T) {
	m := mouseFixture(t)
	m.edit.cy, m.edit.cx = 2, 1
	m = click(m, 27, 2)
	if m.openRel != "main.go" || m.edit.cy != 2 || m.edit.cx != 1 {
		t.Fatalf("active-tab click should not reopen: openRel=%q cursor=(%d,%d)", m.openRel, m.edit.cy, m.edit.cx)
	}
}

func TestTabClickFillAreaInert(t *testing.T) {
	m := mouseFixture(t)
	m.edit.cy, m.edit.cx = 2, 1
	m = click(m, 90, 2)
	if m.openRel != "main.go" || m.edit.cy != 2 || m.edit.cx != 1 {
		t.Fatalf("fill-area click changed state: openRel=%q cursor=(%d,%d)", m.openRel, m.edit.cy, m.edit.cx)
	}
}

func TestTabClickFromQueryModeEnters(t *testing.T) {
	m := mouseFixture(t)
	m = key(m, keyPress(tea.KeyEsc)) // back to query mode, the tab remains
	m = click(m, 27, 2)
	if m.mode != modeEdit || m.openRel != "main.go" {
		t.Fatalf("tab click from query mode: mode=%v openRel=%q", m.mode, m.openRel)
	}
}

func TestTabStripWindowSlidesToActive(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.tabs = []string{"aaaa.go", "bbbb.go", "cccc.go"} // cells are 9 columns each
	m.tabActive = 2
	start, widths := m.tabStripWindow(20)
	if start != 1 {
		t.Fatalf("window should slide right until the active tab fits: start=%d", start)
	}
	for i, w := range widths {
		if w != 9 {
			t.Fatalf("width[%d] = %d, want 9", i, w)
		}
	}
	m.tabActive = 0
	if start, _ = m.tabStripWindow(20); start != 0 {
		t.Fatalf("active first tab needs no slide: start=%d", start)
	}
	// The hit test honors the window. At width=45 the strip is 25 columns
	// (sidebarWidth=16, cells from x=17) — two 9-column cells fit, so with the
	// last tab active the window starts at tab 1 and the first visible cell
	// resolves to it.
	m.width, m.tabActive = 45, 2
	if i, ok := m.tabClickTarget(17, 2); !ok || i != 1 {
		t.Fatalf("first visible cell should be tab 1: %d %v", i, ok)
	}
}

// statusRowCount must agree with what renderStatusLine actually emits — the
// body/sidebar hit-testing heights are derived from it.
func TestStatusRowCountMatchesRenderStatusLine(t *testing.T) {
	m, _ := newTestModel(t, nil)
	check := func(name string, m Model) {
		t.Helper()
		if rows := strings.Count(m.renderStatusLine(), "\n") + 1; rows != m.statusRowCount() {
			t.Fatalf("%s: renderStatusLine has %d rows, statusRowCount says %d", name, rows, m.statusRowCount())
		}
	}
	check("query", m)
	edit := m.openFileAt("main.go")
	check("edit", edit)
	for name, md := range map[string]mode{"search": modeSearch, "exec": modeExec, "command": modeCommand} {
		alt := edit
		alt.mode = md
		check(name, alt)
	}
}

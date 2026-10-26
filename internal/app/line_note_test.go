package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/store"
)

// noteWorkspace is a test model with a 120-line long.go to scroll through.
func noteWorkspace(t *testing.T) (Model, string) {
	t.Helper()
	m, root := newTestModel(t, nil)
	var content strings.Builder
	for i := 1; i <= 120; i++ {
		fmt.Fprintf(&content, "line %d of the file\n", i)
	}
	must(t, os.WriteFile(filepath.Join(root, "long.go"), []byte(content.String()), 0o644))
	return rebuildCorpusNow(m), root
}

func noteRecord(path, note string) store.ExternalRecord {
	record := externalRecord("open", path)
	record.Base = store.ExternalDefaultBase
	record.LineNote = note
	return record
}

// openWithNote delivers an open with a note from the query view.
func openWithNote(t *testing.T, m Model, path, note string) (Model, string) {
	t.Helper()
	m, _, history := externalModel(t, m, true)
	m, cmd, outcome := receive(m, history, noteRecord(path, note))
	return settleExternal(m, cmd), outcome
}

func TestExternalOpenWithANoteShowsTheBox(t *testing.T) {
	m, _ := noteWorkspace(t)
	m, outcome := openWithNote(t, m, "long.go#L40", "# Why\nthis line **matters**")
	if m.mode != modeEdit || m.edit.cy != 39 || !m.lineNoteVisible() {
		t.Fatalf("open: mode=%v cy=%d visible=%v", m.mode, m.edit.cy, m.lineNoteVisible())
	}
	if !strings.Contains(outcome, "at line 40 with a note") {
		t.Fatalf("reply = %q", outcome)
	}
	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{"note · L40", "Why", "this line matters", "esc close"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("view is missing %q:\n%s", want, plain)
		}
	}
	// The box sits under the line it explains.
	lines := strings.Split(plain, "\n")
	for index, line := range lines {
		if strings.Contains(line, "40 │ line 40") {
			if index+1 >= len(lines) || !strings.Contains(lines[index+1], "╭─ note · L40") {
				t.Fatalf("the row under line 40 is not the box top:\n%s\n%s", line, lines[index+1])
			}
		}
	}
}

func TestLineNoteNeverCoversTheGutterOrLeavesThePane(t *testing.T) {
	m, _ := noteWorkspace(t)
	m.width = 140 // wide enough for the header and status hints, so only the panes are measured
	m, _ = openWithNote(t, m, "long.go#L40", strings.Repeat("a long sentence that must wrap ", 20))
	width, height := 70, 20
	with := strings.Split(ansi.Strip(m.renderFile(width, height)), "\n")
	without := strings.Split(ansi.Strip(m.dismissLineNote().renderFile(width, height)), "\n")
	gutterWidth := len(fmt.Sprint(len(m.edit.lines)))
	keep := gutterWidth + 3 + lineNoteMargin
	boxRows := 0
	for index := range with {
		if ansi.Cut(with[index], 0, keep) != ansi.Cut(without[index], 0, keep) {
			t.Fatalf("row %d changed in the gutter/margin:\n%q\n%q", index, with[index], without[index])
		}
		if lipgloss.Width(with[index]) > width {
			t.Fatalf("row %d is %d cells wide, pane is %d", index, lipgloss.Width(with[index]), width)
		}
		if strings.Contains(with[index], "sentence") {
			boxRows++
		}
	}
	if boxRows < 2 {
		t.Fatalf("the long line wraps onto several rows, got %d:\n%s", boxRows, strings.Join(with, "\n"))
	}

	frame := strings.Split(m.View().Content, "\n")
	if len(frame) != m.height {
		t.Fatalf("frame has %d lines, want %d", len(frame), m.height)
	}
	for index, line := range frame {
		if lipgloss.Width(line) > m.width {
			t.Fatalf("frame line %d is %d wide, terminal is %d", index, lipgloss.Width(line), m.width)
		}
	}
}

func TestLineNoteEscClosesOnlyTheBox(t *testing.T) {
	m, _ := noteWorkspace(t)
	m, _ = openWithNote(t, m, "long.go#L40", "why")
	m = key(m, keyPress(tea.KeyEscape))
	if m.mode != modeEdit || m.lineNoteVisible() {
		t.Fatalf("first Esc closes the note only: mode=%v visible=%v", m.mode, m.lineNoteVisible())
	}
	if strings.Contains(ansi.Strip(m.View().Content), "note · L40") {
		t.Fatal("the box is gone")
	}
	m = key(m, keyPress(tea.KeyEscape))
	if m.mode != modeQuery {
		t.Fatalf("the next Esc leaves edit mode as before: mode=%v", m.mode)
	}
}

func TestLineNoteStaysWhileTheCursorMoves(t *testing.T) {
	m, _ := noteWorkspace(t)
	m, _ = openWithNote(t, m, "long.go#L40", "why")
	m = key(m, keyPress(tea.KeyDown))
	m = key(m, keyPress(tea.KeyDown))
	if m.edit.cy != 41 || !m.lineNoteVisible() {
		t.Fatalf("arrows move the cursor under a visible note: cy=%d visible=%v", m.edit.cy, m.lineNoteVisible())
	}
	m = key(m, keyPress(tea.KeyPgDown))
	if m.edit.cy <= 41 || !m.lineNoteVisible() {
		t.Fatalf("PgDn scrolls with the note open: cy=%d visible=%v", m.edit.cy, m.lineNoteVisible())
	}
	m = key(m, typeRune('x'))
	if m.lineNoteVisible() {
		t.Fatal("an edit hides the note — it would drift off its line")
	}
}

func TestLineNoteClearsOnAnotherFile(t *testing.T) {
	m, _ := noteWorkspace(t)
	m, _ = openWithNote(t, m, "long.go#L40", "why")
	m = m.openFileAt("main.go")
	m = m.openFileAt("long.go")
	if m.lineNoteVisible() || m.lineNote.text != "" {
		t.Fatal("opening another file drops the note")
	}
}

func TestLineNoteFlipsAboveToKeepTheCursorVisible(t *testing.T) {
	m, _ := noteWorkspace(t)
	m, _ = openWithNote(t, m, "long.go#L119", "near the end")
	start := fileViewportTop(m.edit.cy, m.fileScrollY, 20, len(m.edit.lines))
	box, ok := m.lineNoteGeometry(start, 3, 60, 20)
	if !ok || box.top+box.height() > m.lineNote.line-start {
		t.Fatalf("no room below the last lines: the box sits above, top=%d height=%d anchor row=%d", box.top, box.height(), m.lineNote.line-start)
	}

	m, _ = openWithNote(t, m, "long.go#L40", "one\ntwo\nthree")
	m.edit.cy = m.lineNote.line + 2 // under where the box would go
	start = fileViewportTop(m.edit.cy, m.fileScrollY, 20, len(m.edit.lines))
	box, _ = m.lineNoteGeometry(start, 3, 60, 20)
	cursorRow := m.edit.cy - start
	if cursorRow >= box.top && cursorRow < box.top+box.height() {
		t.Fatalf("the box hides the cursor: top=%d height=%d cursor row=%d", box.top, box.height(), cursorRow)
	}
}

func TestTallLineNoteScrollsWithTheWheel(t *testing.T) {
	m, _ := noteWorkspace(t)
	var note []string
	for i := 1; i <= 40; i++ {
		note = append(note, fmt.Sprintf("note row %d", i))
	}
	m, _ = openWithNote(t, m, "long.go#L40", strings.Join(note, "\n"))
	plain := ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "↓ 30 more · esc") || strings.Contains(plain, "note row 40") {
		t.Fatalf("a tall note is capped with a hint:\n%s", plain)
	}

	box, ok := m.lineNoteScreenBox()
	if !ok {
		t.Fatal("the box is on screen")
	}
	x := m.sidebarWidth() + 1 + box.left + 2
	y := 2 + m.tabRows() + max(0, box.top) + 1
	cursor := m.edit.cy
	next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: x, Y: y})
	m = next.(Model)
	if m.lineNote.scroll != wheelScrollLines || m.edit.cy != cursor {
		t.Fatalf("the wheel over the box scrolls the note: scroll=%d cy=%d (was %d)", m.lineNote.scroll, m.edit.cy, cursor)
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, fmt.Sprintf("↑ %d", wheelScrollLines)) {
		t.Fatalf("the hint shows the rows above:\n%s", plain)
	}
	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	if next.(Model).edit.cy != cursor {
		t.Fatal("a click on the box does not move the cursor under it")
	}
	next, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: m.width - 1, Y: y})
	if next.(Model).edit.cy == cursor {
		t.Fatal("the wheel off the box still scrolls the file")
	}
}

func TestShiftArrowsScrollATallNote(t *testing.T) {
	m, _ := noteWorkspace(t)
	m, _ = openWithNote(t, m, "long.go#L40", "short")
	m.edit.selLineMode = true // an active line selection, which Shift+↓ extends
	m = key(m, shiftKey(tea.KeyDown))
	if m.edit.cy != 40 || m.edit.sel == nil || m.lineNote.scroll != 0 {
		t.Fatalf("a note that fits leaves Shift+↓ to the selection: cy=%d", m.edit.cy)
	}
	m.edit.selLineMode = false
	m.mode = modeQuery // so the next open lands without the confirm dialog

	var note []string
	for i := 1; i <= 40; i++ {
		note = append(note, fmt.Sprintf("note row %d", i))
	}
	m, _ = openWithNote(t, m.dismissLineNote(), "long.go#L40", strings.Join(note, "\n"))
	m.edit.clearSelection()
	cursor := m.edit.cy
	m = key(m, shiftKey(tea.KeyDown))
	m = key(m, shiftKey(tea.KeyDown))
	if m.lineNote.scroll != 2 || m.edit.sel != nil || m.edit.cy != cursor {
		t.Fatalf("Shift+↓ scrolls a tall note: scroll=%d sel=%v cy=%d", m.lineNote.scroll, m.edit.sel != nil, m.edit.cy)
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "note row 3") || strings.Contains(plain, "note row 2 ") {
		t.Fatalf("the box shows from row 3:\n%s", plain)
	}
	m = key(m, shiftKey(tea.KeyUp))
	if m.lineNote.scroll != 1 {
		t.Fatalf("Shift+↑ scrolls back: scroll=%d", m.lineNote.scroll)
	}
	for i := 0; i < 5; i++ {
		m = key(m, shiftKey(tea.KeyUp))
	}
	if m.lineNote.scroll != 0 {
		t.Fatalf("the scroll stops at the top: scroll=%d", m.lineNote.scroll)
	}
}

func TestLineNoteHintsFitTheBorder(t *testing.T) {
	box := lineNoteBox{lines: make([]string, 20), bodyRows: 5, scroll: 3}
	if hints := lineNoteHints(box); hints[0] != " ↑ 3 · ↓ 12 more · shift+↑↓/wheel · esc close " {
		t.Fatalf("full hint = %q", hints[0])
	}
	box.scroll = 15
	if hints := lineNoteHints(box); !strings.HasPrefix(hints[0], " ↑ 15 · shift") {
		t.Fatalf("at the bottom = %q", hints[0])
	}
	if hints := lineNoteHints(lineNoteBox{lines: make([]string, 3), bodyRows: 3}); hints[0] != " esc close " {
		t.Fatalf("a note that fits = %q", hints[0])
	}
}

func TestLineNoteFromTheReceiverNeedsALine(t *testing.T) {
	_, root := newTestModel(t, nil)
	settings := store.NewMemoryExternalSettings(store.ExternalSettings{Enabled: true, HistoryLimit: 10})
	history := store.NewMemoryExternalHistory()
	receiver := NewExternalReceiver(root, settings, history)
	if reply := receiver.Handle("open\nmain.go\n---\nwhy\n"); !strings.HasPrefix(reply, "error a note needs a line") {
		t.Fatalf("a note without a line is refused: %q", reply)
	}
	delivered := make(chan externalMessageMsg, 1)
	receiver.SetSink(func(msg any) { delivered <- msg.(externalMessageMsg) })
	go receiver.Handle("open\nmain.go#L3\n---\n# Why\n\n  kept\n")
	if msg := <-delivered; msg.record.LineNote != "# Why\n\n  kept" {
		t.Fatalf("the record carries the note: %q", msg.record.LineNote)
	}
	if records, _ := history.LoadExternal(); len(records) != 1 || records[0].LineNote == "" {
		t.Fatalf("the history keeps the note: %+v", records)
	}
}

func TestLineNoteConfirmAndReplay(t *testing.T) {
	m, _ := noteWorkspace(t)
	m = m.openFileAt("main.go")
	m.mode = modeEdit
	m, _, history := externalModel(t, m, true)
	record := noteRecord("long.go#L40", "first line of the note\nsecond")
	m, _, _ = receive(m, history, record)
	if !m.choice.open() {
		t.Fatal("editing asks first")
	}
	var body []string
	for _, line := range m.choice.body {
		body = append(body, line.text)
	}
	if !strings.Contains(strings.Join(body, "\n"), "with a note: first line of the note …") {
		t.Fatalf("the confirm overlay mentions the note:\n%s", strings.Join(body, "\n"))
	}
	next, cmd := m.Update(keyPress(tea.KeyEnter))
	m = settleExternal(next.(Model), cmd)
	if !m.lineNoteVisible() || m.lineNote.text != record.LineNote {
		t.Fatal("accepting shows the note")
	}

	m = m.dismissLineNote()
	m.mode = modeQuery
	next, cmd = m.handleExternalMessage(externalMessageMsg{record: record, replay: true})
	m = settleExternal(next.(Model), cmd)
	if !m.lineNoteVisible() {
		t.Fatal("a replay from the history restores the note")
	}
}

func TestLineNoteShowsAfterLeavingTheDiff(t *testing.T) {
	m, _ := gitReviewWorkspace(t)
	m, _ = deliver(m, workspaceReposMsg{repos: []string{"apps/web", "libs/core"}})
	m, _, history := externalModel(t, m, true)
	record := noteRecord("apps/web/main.go#L3", "explain")
	record.Args, record.Base = []string{"HEAD~0"}, "HEAD~0"
	m, cmd, outcome := receive(m, history, record)
	m = settleDiff(m, cmd)
	if m.mode != modeDiff || m.lineNoteVisible() || !strings.Contains(outcome, "with a note") {
		t.Fatalf("the diff opens first: mode=%v visible=%v reply=%q", m.mode, m.lineNoteVisible(), outcome)
	}
	m = key(m, keyPress(tea.KeyEscape))
	if m.mode != modeEdit || !m.lineNoteVisible() {
		t.Fatalf("leaving the diff shows the note: mode=%v visible=%v", m.mode, m.lineNoteVisible())
	}
}

func TestLayoutLineNote(t *testing.T) {
	text := strings.Join([]string{
		"# Heading here",
		"",
		"- a bullet whose text is long enough to wrap",
		"  2. numbered",
		"> quoted",
		"plain `code span` and **bold words**",
		"```",
		"func main() { fmt.Println(\"a very long code line\") }",
		"```",
		"中文中文中文中文中文中文中文中文",
		"averyveryveryverylongwordthatcannotfit",
	}, "\n")
	const width = 20
	lines := layoutLineNote(text, width, config.DefaultTabWidth)
	var plain []string
	for index, line := range lines {
		if got := ansi.StringWidth(line); got != width {
			t.Fatalf("line %d is %d cells, want %d: %q", index, got, width, ansi.Strip(line))
		}
		plain = append(plain, ansi.Strip(line))
	}
	joined := strings.Join(plain, "\n")
	for _, want := range []string{
		"Heading here",
		"• a bullet whose",
		"  text is long",
		"  2. numbered",
		"▎ quoted",
		"plain code span and",
		"bold words",
		"func main() { fmt.Pr",
		"averyveryveryverylon",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("layout is missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "```") || strings.Contains(joined, "**") || strings.Contains(joined, "`") || strings.Contains(joined, "# ") {
		t.Fatalf("markdown markers are rendered, not shown:\n%s", joined)
	}
	if !strings.Contains(lines[0], "\x1b[1") {
		t.Fatal("the heading is bold")
	}
}

func TestLineNoteLayoutIsMemoized(t *testing.T) {
	note := newLineNote("a.go", 0, "some note text", 0, config.DefaultTabWidth)
	first := note.lines(30)
	if second := note.lines(30); &first[0] != &second[0] {
		t.Fatal("the same width reuses the layout")
	}
	if resized := note.lines(20); ansi.StringWidth(resized[0]) != 20 {
		t.Fatal("a new width lays out again")
	}
}

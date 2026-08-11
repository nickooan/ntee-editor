package app

import "testing"

func TestEditorInsertAndNewline(t *testing.T) {
	e := newEditor("hello")
	e.cx = 5
	e.insert("!")
	if e.content() != "hello!" || e.cx != 6 {
		t.Fatalf("insert: %q cx=%d", e.content(), e.cx)
	}
	e.cx = 5
	e.newline()
	if e.content() != "hello\n!" || e.cy != 1 || e.cx != 0 {
		t.Fatalf("newline: %q cy=%d cx=%d", e.content(), e.cy, e.cx)
	}
}

func TestEditorBackspaceJoinsLines(t *testing.T) {
	e := newEditor("ab\ncd")
	e.cy, e.cx = 1, 0
	e.backspace()
	if e.content() != "abcd" || e.cy != 0 || e.cx != 2 {
		t.Fatalf("join: %q cy=%d cx=%d", e.content(), e.cy, e.cx)
	}
}

func TestEditorUTF8Cursor(t *testing.T) {
	e := newEditor("héllo")
	e.cx = 2 // between é and l — rune columns, not bytes
	e.insert("x")
	if e.content() != "héxllo" {
		t.Fatalf("utf8 insert: %q", e.content())
	}
}

func TestProgressiveSelectionWordThenLine(t *testing.T) {
	e := newEditor("foo bar baz")
	e.cx = 5 // inside "bar"
	e.expandSelection()
	if e.sel == nil || e.selectedText() != "bar" {
		t.Fatalf("first press should select word, got %q", e.selectedText())
	}
	e.expandSelection()
	if e.selectedText() != "foo bar baz" {
		t.Fatalf("second press should select line, got %q", e.selectedText())
	}
	e.expandSelection() // no-op at full line
	if e.selectedText() != "foo bar baz" {
		t.Fatalf("expansion past line changed selection: %q", e.selectedText())
	}
}

func TestSelectionDeleteAndReplace(t *testing.T) {
	e := newEditor("foo bar baz")
	e.cx = 5
	e.expandSelection() // "bar"
	e.insert("qux")     // typing over a selection replaces it
	if e.content() != "foo qux baz" {
		t.Fatalf("replace: %q", e.content())
	}
	if e.sel != nil {
		t.Fatal("selection should clear after replace")
	}
}

func TestWordSelectionIsNotLineMode(t *testing.T) {
	e := newEditor("foo bar baz")
	e.cx = 5
	e.expandSelection() // "bar" — a word, not the whole line
	if e.selLineMode {
		t.Fatal("word selection must not be line mode")
	}
	e.expandSelection() // whole line
	if !e.selLineMode {
		t.Fatal("whole-line selection must be line mode")
	}
}

func TestLineSelectionExtendAndText(t *testing.T) {
	e := newEditor("one\ntwo\nthree\nfour")
	e.cy, e.cx = 1, 0
	e.expandSelection() // "two" fills its line → line mode
	e.expandSelection() // idempotent at line level
	if !e.selLineMode || e.selLineAnchor != 1 {
		t.Fatalf("line mode should latch: mode=%v anchor=%d", e.selLineMode, e.selLineAnchor)
	}
	if e.selectionText() != "two\n" {
		t.Fatalf("single-line line selection: %q", e.selectionText())
	}

	e.extendLineSelection(1)
	e.extendLineSelection(1)
	if e.cy != 3 || e.selectionText() != "two\nthree\nfour\n" {
		t.Fatalf("extend down: cy=%d text=%q", e.cy, e.selectionText())
	}
	e.extendLineSelection(1) // clamp at last line
	if e.cy != 3 {
		t.Fatalf("should clamp at last line: cy=%d", e.cy)
	}

	e.extendLineSelection(-1)
	if e.selectionText() != "two\nthree\n" {
		t.Fatalf("shrink: %q", e.selectionText())
	}
	if !e.inLineSelection(1) || !e.inLineSelection(2) || e.inLineSelection(0) || e.inLineSelection(3) {
		t.Fatal("inLineSelection range wrong")
	}
}

func TestLineSelectionUpwardAnchor(t *testing.T) {
	e := newEditor("one\ntwo\nthree")
	e.cy = 2
	e.expandSelection() // "three" fills its line
	e.extendLineSelection(-1)
	if e.selectionText() != "two\nthree\n" {
		t.Fatalf("upward selection: %q", e.selectionText())
	}
}

func TestExtendLineSelectionNoopWithoutLineMode(t *testing.T) {
	e := newEditor("foo bar baz")
	e.cx = 5
	e.expandSelection() // word only
	before := e.cy
	e.extendLineSelection(1)
	if e.cy != before {
		t.Fatal("extendLineSelection must be a no-op outside line mode")
	}
}

func TestLineSelectionDelete(t *testing.T) {
	e := newEditor("one\ntwo\nthree\nfour")
	e.cy = 1
	e.expandSelection() // line mode on "two"
	e.extendLineSelection(1)
	e.deleteSelection()
	if e.content() != "one\n\nfour" {
		t.Fatalf("line delete: %q", e.content())
	}
	if e.cy != 1 || e.cx != 0 || e.selLineMode {
		t.Fatalf("after delete cy=%d cx=%d lineMode=%v", e.cy, e.cx, e.selLineMode)
	}
}

func TestRevBumpsOnEveryMutation(t *testing.T) {
	e := newEditor("a")
	start := e.rev
	e.insert("b")
	e.newline()
	e.backspace()
	e.insertLines([]string{"x", "y"})
	if e.rev != start+4 {
		t.Fatalf("rev = %d, want %d", e.rev, start+4)
	}
}

// insertLines is the bulk paste splice: first segment joins the head of the
// cursor line, last segment takes its tail, middles land as whole lines.
func TestEditorInsertLines(t *testing.T) {
	e := newEditor("abcXYZ\nrest")
	e.cx = 3
	e.insertLines([]string{"1", "2", "3"})
	if e.content() != "abc1\n2\n3XYZ\nrest" {
		t.Fatalf("content: %q", e.content())
	}
	if e.cy != 2 || e.cx != 1 {
		t.Fatalf("cursor: cy=%d cx=%d", e.cy, e.cx)
	}

	// A single segment behaves exactly like insert.
	e = newEditor("ab")
	e.cx = 1
	e.insertLines([]string{"-"})
	if e.content() != "a-b" || e.cy != 0 || e.cx != 2 {
		t.Fatalf("single segment: %q cy=%d cx=%d", e.content(), e.cy, e.cx)
	}

	// A trailing empty segment leaves the cursor at the start of the tail line.
	e = newEditor("tail")
	e.cx = 0
	e.insertLines([]string{"top", ""})
	if e.content() != "top\ntail" || e.cy != 1 || e.cx != 0 {
		t.Fatalf("trailing newline: %q cy=%d cx=%d", e.content(), e.cy, e.cx)
	}
}

// editPaste splices a multi-line paste in one pass and keeps the highlight
// cache's row indices aligned with the buffer.
func TestEditPasteMultiline(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m.edit.cy, m.edit.cx = 1, 0 // the blank line after "package main"

	next, _ := m.editPaste("one\r\ntwo\rthree")
	m = next.(Model)
	want := "package main\none\ntwo\nthree\nfunc main() {\n}\n"
	if got := m.edit.content(); got != want {
		t.Fatalf("paste result:\n%q\nwant:\n%q", got, want)
	}
	if m.edit.cy != 3 || m.edit.cx != 5 {
		t.Fatalf("cursor: cy=%d cx=%d", m.edit.cy, m.edit.cx)
	}
	if len(m.hlLines) != len(m.edit.lines) {
		t.Fatalf("highlight rows %d != buffer lines %d", len(m.hlLines), len(m.edit.lines))
	}
}

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

func TestRevBumpsOnEveryMutation(t *testing.T) {
	e := newEditor("a")
	start := e.rev
	e.insert("b")
	e.newline()
	e.backspace()
	if e.rev != start+3 {
		t.Fatalf("rev = %d, want %d", e.rev, start+3)
	}
}

package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/lsp"
)

func TestCompletionOpenFilterAccept(t *testing.T) {
	m, client := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	client.completions = []lsp.CompletionItem{
		{Label: "Println", InsertText: "Println"},
		{Label: "Printf", InsertText: "Printf"},
		{Label: "Scan", InsertText: "Scan"},
	}
	// A single-line buffer with the cursor after a typed "Printl".
	m.edit = newEditor("Printl")
	m.edit.cy, m.edit.cx = 0, 6

	next, cmd := m.requestCompletion()
	m = next.(Model)
	if cmd == nil {
		t.Fatal("expected a completion request cmd")
	}
	msg, ok := cmd().(completionMsg)
	if !ok {
		t.Fatalf("expected completionMsg, got %T", cmd())
	}

	next, _ = m.handleCompletion(msg)
	m = next.(Model)
	if !m.completionOpen {
		t.Fatal("popup should open")
	}
	// Prefix "Printl" matches only Println.
	if len(m.completionItems) != 1 || m.completionItems[0].Label != "Println" {
		t.Fatalf("filtered = %+v", m.completionItems)
	}

	// Accept replaces the partial word with the insert text.
	m = m.acceptCompletion()
	if m.edit.content() != "Println" {
		t.Fatalf("accept result = %q", m.edit.content())
	}
	if m.completionOpen {
		t.Fatal("popup should close after accept")
	}
}

func TestCompletionStaleAnswerDropped(t *testing.T) {
	m, _ := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	m.edit = newEditor("foo")
	m.edit.cy, m.edit.cx = 0, 3

	// A result whose tagged word start no longer matches the cursor context.
	msg := completionMsg{line: 0, start: 99, items: []lsp.CompletionItem{{Label: "foobar"}}}
	next, _ := m.handleCompletion(msg)
	m = next.(Model)
	if m.completionOpen {
		t.Fatal("a stale completion answer must be dropped")
	}
}

func TestCompletionEscDismissesAndSuppresses(t *testing.T) {
	m, _ := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	m.completionOpen = true
	m.completionItems = []lsp.CompletionItem{{Label: "Println"}}

	nm, _, done := m.completionKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !done || nm.completionOpen || !nm.completionDismissed {
		t.Fatalf("Esc should dismiss and suppress: open=%v dismissed=%v done=%v", nm.completionOpen, nm.completionDismissed, done)
	}
	// While dismissed, typing an identifier char should not re-request.
	nm.edit = newEditor("a")
	nm.edit.cx = 1
	_, cmd := nm.afterEditType("a")
	if cmd != nil {
		t.Fatal("dismissed popup must not auto-reopen mid-word")
	}
}

func TestCompletionViewSmoke(t *testing.T) {
	m, _ := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	m.completionOpen = true
	m.completionItems = []lsp.CompletionItem{{Label: "Println"}, {Label: "Printf"}}
	m.completionIndex = 1
	if out := m.View(); out == "" {
		t.Fatal("View with completion open should render")
	}
}

package app

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/lsp"
)

// stubClient records the doc-sync calls the app makes.
type stubClient struct {
	calls   []string
	locs    []lsp.Location
	refLocs []lsp.Location
}

func (s *stubClient) DidOpen(path, _ string) { s.calls = append(s.calls, "open:"+filepath.Base(path)) }
func (s *stubClient) DidChange(path string, _ string, _ int) {
	s.calls = append(s.calls, "change:"+filepath.Base(path))
}
func (s *stubClient) DidSave(path string)                                 { s.calls = append(s.calls, "save:"+filepath.Base(path)) }
func (s *stubClient) DidClose(path string)                                { s.calls = append(s.calls, "close:"+filepath.Base(path)) }
func (s *stubClient) Definition(string, int, int) ([]lsp.Location, error) { return s.locs, nil }
func (s *stubClient) References(string, int, int) ([]lsp.Location, error) {
	s.calls = append(s.calls, "references")
	return s.refLocs, nil
}

type stubRegistry struct{ client *stubClient }

func (r stubRegistry) ClientFor(string) (lsp.Client, bool) { return r.client, true }
func (r stubRegistry) ShutdownAll()                        {}

func newLSPTestModel(t *testing.T) (Model, *stubClient) {
	t.Helper()
	m, _ := newTestModel(t, nil)
	client := &stubClient{}
	m.lsp = stubRegistry{client: client}
	return m, client
}

func TestLifecycleCallsReachClient(t *testing.T) {
	m, client := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	m = runes(m, "x ") // burst flush → DidChange
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	m = m.openFileAt("lib/util.ts") // close main.go, open util.ts

	want := []string{"open:main.go", "change:main.go", "change:main.go", "save:main.go", "close:main.go", "open:util.ts"}
	if len(client.calls) < len(want) {
		t.Fatalf("calls: %v", client.calls)
	}
	for i, w := range want {
		if client.calls[i] != w {
			t.Fatalf("call %d = %q, want %q (all: %v)", i, client.calls[i], w, client.calls)
		}
	}
}

func TestLSPDefinitionJumpAndFallback(t *testing.T) {
	m, client := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	root := m.root

	// Ctrl+J issues an async cmd when a client resolves.
	m.edit.cy, m.edit.cx = 0, 2
	next, cmd := m.jumpToReference()
	m = next.(Model)
	if cmd == nil {
		t.Fatal("expected async definition cmd")
	}

	// LSP answer inside the project → jump + frame push.
	client.locs = []lsp.Location{{
		URI:   lsp.PathToURI(filepath.Join(root, "lib", "util.ts")),
		Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 7}},
	}}
	msg := cmd().(definitionMsg)
	msg.locs = client.locs // simulate the answer
	next, _ = m.handleDefinition(msg)
	m = next.(Model)
	if m.openRel != "lib/util.ts" || len(m.jumpStack) != 1 {
		t.Fatalf("lsp jump failed: open=%q stack=%d err=%q", m.openRel, len(m.jumpStack), m.errText)
	}
	if m.edit.cy != 0 || m.edit.cx != 7 {
		t.Fatalf("cursor: cy=%d cx=%d", m.edit.cy, m.edit.cx)
	}

	// Empty LSP answer → strict: report, don't guess; no stack residue.
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlO})
	next, _ = m.handleDefinition(definitionMsg{token: "nonexistentsymbolxyz"})
	m = next.(Model)
	if m.errText == "" || len(m.jumpStack) != 0 {
		t.Fatalf("fallback should error cleanly: err=%q stack=%d", m.errText, len(m.jumpStack))
	}
}

func TestDiagnosticsStoreAndClear(t *testing.T) {
	m, _ := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	abs := filepath.Join(m.root, "main.go")

	next, _ := m.Update(lsp.DiagnosticsMsg{Path: abs, Items: []lsp.Diagnostic{{Line: 2, Severity: 1, Message: "boom"}}})
	m = next.(Model)
	if len(m.diags["main.go"]) != 1 {
		t.Fatalf("diags not stored: %+v", m.diags)
	}
	if d, ok := m.diagAtLine(2); !ok || d.Message != "boom" {
		t.Fatal("diagAtLine miss")
	}

	// Empty publish clears.
	next, _ = m.Update(lsp.DiagnosticsMsg{Path: abs, Items: nil})
	m = next.(Model)
	if len(m.diags["main.go"]) != 0 {
		t.Fatal("diags not cleared")
	}
}

func TestLSPDefinitionPickerMultipleHits(t *testing.T) {
	m, client := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	root := m.root

	client.locs = []lsp.Location{
		{URI: lsp.PathToURI(filepath.Join(root, "lib", "util.ts")), Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}}},
		{URI: lsp.PathToURI(filepath.Join(root, "main.go")), Range: lsp.Range{Start: lsp.Position{Line: 2, Character: 0}}},
	}
	next, _ := m.handleDefinition(definitionMsg{token: "x", locs: client.locs})
	m = next.(Model)
	if !m.defPickOpen || len(m.defPickItems) != 2 {
		t.Fatalf("picker should open with 2 LSP hits: %v %d", m.defPickOpen, len(m.defPickItems))
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyDown})
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.openRel != "main.go" || m.edit.cy != 2 {
		t.Fatalf("picked jump failed: open=%q cy=%d", m.openRel, m.edit.cy)
	}
}

func TestLSPReferencesFromDefinitionLine(t *testing.T) {
	m, client := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	root := m.root
	m.edit.cy, m.edit.cx = 2, 5 // "func main() {" — the definition line

	// Definition answers with the cursor's own line → flips to references.
	selfLoc := []lsp.Location{{
		URI:   lsp.PathToURI(filepath.Join(root, "main.go")),
		Range: lsp.Range{Start: lsp.Position{Line: 2, Character: 5}},
	}}
	client.refLocs = []lsp.Location{
		{URI: lsp.PathToURI(filepath.Join(root, "lib", "util.ts")), Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}}},
		{URI: lsp.PathToURI(filepath.Join(root, "main.go")), Range: lsp.Range{Start: lsp.Position{Line: 3, Character: 1}}},
	}
	next, cmd := m.handleDefinition(definitionMsg{token: "main", locs: selfLoc})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("on-definition hit should chain a references request")
	}
	msg, ok := cmd().(referencesMsg)
	if !ok {
		t.Fatalf("expected referencesMsg, got %T", cmd())
	}
	next, _ = m.handleReferences(msg)
	m = next.(Model)
	if !m.defPickOpen || len(m.defPickItems) != 2 {
		t.Fatalf("references picker should open with 2 hits: open=%v n=%d err=%q",
			m.defPickOpen, len(m.defPickItems), m.errText)
	}
	if m.defPickTitle != "references of main" {
		t.Fatalf("picker title: %q", m.defPickTitle)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.openRel != "lib/util.ts" {
		t.Fatalf("picked reference jump failed: %q", m.openRel)
	}
}

func TestLSPStrictNoHeuristicFallback(t *testing.T) {
	m, _ := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	m.edit.cy, m.edit.cx = 3, 1

	// The heuristic WOULD find "func main" in this buffer, but with a server
	// configured an empty LSP answer must not guess a jump.
	next, _ := m.handleDefinition(definitionMsg{token: "main"})
	m = next.(Model)
	if m.openRel != "main.go" || m.edit.cy != 3 || len(m.jumpStack) != 0 {
		t.Fatalf("strict mode must not move: %q cy=%d stack=%d", m.openRel, m.edit.cy, len(m.jumpStack))
	}
	if !strings.Contains(m.errText, "no definition found") {
		t.Fatalf("err: %q", m.errText)
	}

	// A still-starting server gets the friendly message.
	next, _ = m.handleDefinition(definitionMsg{token: "x", err: errors.New("language server not ready")})
	m = next.(Model)
	if !strings.Contains(m.errText, "still starting") {
		t.Fatalf("err: %q", m.errText)
	}

	// References are strict too.
	next, _ = m.handleReferences(referencesMsg{token: "main"})
	m = next.(Model)
	if !strings.Contains(m.errText, "no references found") {
		t.Fatalf("refs err: %q", m.errText)
	}
}

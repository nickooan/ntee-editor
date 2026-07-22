package app

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/lsp"
)

// stubClient records the doc-sync calls the app makes, plus the UTF-16
// columns of definition/references lookups. When defQueue is set, Definition
// pops one scripted answer per call (for retry-ladder tests); otherwise it
// returns locs.
type stubClient struct {
	calls       []string
	locs        []lsp.Location
	defQueue    [][]lsp.Location
	defCols     []int
	refLocs     []lsp.Location
	refCols     []int
	completions []lsp.CompletionItem
}

func (s *stubClient) DidOpen(path, _ string) { s.calls = append(s.calls, "open:"+filepath.Base(path)) }
func (s *stubClient) DidChange(path string, _ string, _ int) {
	s.calls = append(s.calls, "change:"+filepath.Base(path))
}
func (s *stubClient) DidSave(path string)  { s.calls = append(s.calls, "save:"+filepath.Base(path)) }
func (s *stubClient) DidClose(path string) { s.calls = append(s.calls, "close:"+filepath.Base(path)) }
func (s *stubClient) Definition(_ string, _, col int) ([]lsp.Location, error) {
	s.defCols = append(s.defCols, col)
	if len(s.defQueue) > 0 {
		locs := s.defQueue[0]
		s.defQueue = s.defQueue[1:]
		return locs, nil
	}
	return s.locs, nil
}
func (s *stubClient) References(_ string, _, col int) ([]lsp.Location, error) {
	s.calls = append(s.calls, "references")
	s.refCols = append(s.refCols, col)
	return s.refLocs, nil
}
func (s *stubClient) Completion(string, int, int) ([]lsp.CompletionItem, error) {
	s.calls = append(s.calls, "completion")
	return s.completions, nil
}

type stubRegistry struct{ client *stubClient }

func (r stubRegistry) ClientFor(string) (lsp.Client, bool) { return r.client, true }
func (r stubRegistry) UnavailableReason(string) string     { return "" }
func (r stubRegistry) Statuses() []lsp.LangStatus          { return nil }
func (r stubRegistry) Enable(string) (bool, string)        { return true, "" }
func (r stubRegistry) Disable(string)                      {}
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

	// Collapse consecutive identical calls: completion syncs the buffer with
	// extra DidChange calls, so assert the lifecycle order, not exact counts.
	var seq []string
	for _, c := range client.calls {
		if len(seq) == 0 || seq[len(seq)-1] != c {
			seq = append(seq, c)
		}
	}
	want := []string{"open:main.go", "change:main.go", "save:main.go", "close:main.go", "open:util.ts"}
	if len(seq) < len(want) {
		t.Fatalf("calls: %v", client.calls)
	}
	for i, w := range want {
		if seq[i] != w {
			t.Fatalf("collapsed call %d = %q, want %q (all: %v)", i, seq[i], w, client.calls)
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

	// Empty LSP answer on an identifier → strict: no heuristic jump. The cursor
	// pivots to references (an identifier with no definition is treated as its
	// own declaration); an empty references answer reports cleanly, no residue.
	m = key(m, tea.KeyMsg{Type: tea.KeyCtrlO})
	next, cmd = m.handleDefinition(definitionMsg{token: "nonexistentsymbolxyz"})
	m = next.(Model)
	for cmd != nil {
		next, cmd = m.Update(cmd())
		m = next.(Model)
	}
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
	// configured an empty LSP answer must not guess a jump — it pivots to a
	// references request (a real LSP call, not an in-buffer guess) and does not
	// move on its own.
	next, cmd := m.handleDefinition(definitionMsg{token: "main"})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("empty definition on an identifier should pivot to references")
	}
	if _, ok := cmd().(referencesMsg); !ok {
		t.Fatalf("expected a references pivot, got %T", cmd())
	}
	if m.openRel != "main.go" || m.edit.cy != 3 || len(m.jumpStack) != 0 {
		t.Fatalf("strict mode must not move: %q cy=%d stack=%d", m.openRel, m.edit.cy, len(m.jumpStack))
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

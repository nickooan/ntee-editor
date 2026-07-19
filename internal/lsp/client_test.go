package lsp

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nickooan/ntee-editor/internal/config"
)

// fakeServer speaks just enough LSP over an in-memory pipe: answers
// initialize and definition, records doc-sync notifications, and can push
// publishDiagnostics.
type fakeServer struct {
	conn *Conn

	mu             sync.Mutex
	opened         []string
	changed        []int // versions seen
	closed         []string
	deflocs        []Location
	initDone       bool
	sawRefParams   bool
	refIncludeDecl bool
}

func newFakeServer(end pipeEnd) *fakeServer {
	s := &fakeServer{}
	s.conn = NewConn(end, s.handle)
	return s
}

func (s *fakeServer) handle(method string, params json.RawMessage) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch method {
	case "initialize":
		s.initDone = true
		return map[string]any{"capabilities": map[string]any{}}, nil
	case "textDocument/didOpen":
		var p didOpenParams
		_ = json.Unmarshal(params, &p)
		s.opened = append(s.opened, p.TextDocument.URI)
	case "textDocument/didChange":
		var p didChangeParams
		_ = json.Unmarshal(params, &p)
		s.changed = append(s.changed, p.TextDocument.Version)
	case "textDocument/didClose":
		var p didCloseParams
		_ = json.Unmarshal(params, &p)
		s.closed = append(s.closed, p.TextDocument.URI)
	case "textDocument/definition":
		return s.deflocs, nil
	case "textDocument/references":
		var p referenceParams
		_ = json.Unmarshal(params, &p)
		s.sawRefParams = true
		s.refIncludeDecl = p.Context.IncludeDeclaration
		return s.deflocs, nil
	}
	return nil, nil
}

// startTestClient wires a serverClient to a fake server, bypassing process
// spawning: the handshake runs over the pipe exactly as it would over stdio.
func startTestClient(t *testing.T, sink func(any)) (*serverClient, *fakeServer) {
	t.Helper()
	clientEnd, serverEnd := pipePair()
	server := newFakeServer(serverEnd)

	c := newServerClient("go", config.LSPServerConfig{Command: "unused"}, "/proj", sink)
	conn := NewConn(clientEnd, c.handle)
	// Replicate start()'s handshake without exec.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := conn.Request(ctx, "initialize", initializeParams{RootURI: PathToURI("/proj"), Capabilities: clientCapabilities}); err != nil {
		t.Fatal(err)
	}
	_ = conn.Notify("initialized", struct{}{})
	c.mu.Lock()
	c.conn = conn
	c.ready = true
	ops := c.pending
	c.pending = nil
	c.mu.Unlock()
	for _, op := range ops {
		op()
	}
	t.Cleanup(func() { _ = conn.Close(); _ = server.conn.Close() })
	return c, server
}

func TestClientDocSyncAndDefinition(t *testing.T) {
	c, server := startTestClient(t, nil)

	c.DidOpen("/proj/a.go", "package a")
	c.DidChange("/proj/a.go", "package a // x", 1)
	c.DidChange("/proj/a.go", "package a // y", 2)
	c.DidClose("/proj/a.go")

	waitFor(t, func() bool {
		server.mu.Lock()
		defer server.mu.Unlock()
		return len(server.opened) == 1 && len(server.changed) == 2 && len(server.closed) == 1
	})
	server.mu.Lock()
	if server.opened[0] != PathToURI("/proj/a.go") {
		t.Fatalf("didOpen uri: %q", server.opened[0])
	}
	if server.changed[0] != 2 || server.changed[1] != 3 {
		t.Fatalf("versions must increase: %v", server.changed)
	}
	server.deflocs = []Location{{URI: PathToURI("/proj/b.go"), Range: Range{Start: Position{Line: 4, Character: 2}}}}
	server.mu.Unlock()

	locs, err := c.Definition("/proj/a.go", 1, 0)
	if err != nil || len(locs) != 1 || locs[0].Range.Start.Line != 4 {
		t.Fatalf("definition: %+v err=%v", locs, err)
	}
}

func TestDiagnosticsReachSink(t *testing.T) {
	msgs := make(chan any, 4)
	c, server := startTestClient(t, func(m any) { msgs <- m })

	_ = server.conn.Notify("textDocument/publishDiagnostics", publishDiagnosticsParams{
		URI: PathToURI("/proj/a.go"),
		Diagnostics: []protoDiagnostic{
			{Range: Range{Start: Position{Line: 3, Character: 1}}, Severity: 1, Message: "boom"},
		},
	})

	select {
	case raw := <-msgs:
		msg, ok := raw.(DiagnosticsMsg)
		if !ok {
			t.Fatalf("wrong msg type %T", raw)
		}
		if msg.Path != "/proj/a.go" || len(msg.Items) != 1 || msg.Items[0].Line != 3 || msg.Items[0].Severity != 1 {
			t.Fatalf("diagnostics: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no DiagnosticsMsg")
	}
	_ = c
}

func TestQueuedOpsReplayAfterReady(t *testing.T) {
	// Ops issued before the handshake completes are queued in order.
	c := newServerClient("go", config.LSPServerConfig{}, "/proj", nil)
	c.DidOpen("/proj/a.go", "x")
	c.DidChange("/proj/a.go", "y", 1)
	if len(c.pending) != 2 {
		t.Fatalf("pending = %d", len(c.pending))
	}
	// Dead clients drop ops silently.
	c.dead = true
	c.DidSave("/proj/a.go")
	if len(c.pending) != 2 {
		t.Fatal("dead client must not queue")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}

func TestReferencesRequest(t *testing.T) {
	c, server := startTestClient(t, nil)
	server.mu.Lock()
	server.deflocs = []Location{{URI: PathToURI("/proj/x.go"), Range: Range{Start: Position{Line: 7}}}}
	server.mu.Unlock()

	locs, err := c.References("/proj/a.go", 2, 4)
	if err != nil || len(locs) != 1 || locs[0].Range.Start.Line != 7 {
		t.Fatalf("references: %+v err=%v", locs, err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if !server.sawRefParams || server.refIncludeDecl {
		t.Fatalf("params: saw=%v includeDecl=%v", server.sawRefParams, server.refIncludeDecl)
	}
}

func TestNewManagerHonorsEnable(t *testing.T) {
	off := false
	cfg := config.Config{Languages: map[string]config.LanguageConfig{
		"go":         {Extensions: []string{".go"}, LSP: &config.LSPServerConfig{Command: "gopls"}},
		"typescript": {Enabled: &off, Extensions: []string{".ts"}, LSP: &config.LSPServerConfig{Command: "tsls"}},
	}}
	m := NewManager(cfg, "/tmp")
	if m.extLang[".go"] != "go" {
		t.Fatalf("enabled language should route: %v", m.extLang)
	}
	if _, ok := m.extLang[".ts"]; ok {
		t.Fatal("disabled language must not be in extLang")
	}
}

func TestLanguageIDFor(t *testing.T) {
	cases := map[string]string{
		"a.go":  "go",
		"a.ts":  "typescript",
		"a.mts": "typescript",
		"a.cts": "typescript",
		"a.tsx": "typescriptreact",
		"a.js":  "javascript",
		"a.mjs": "javascript",
		"a.cjs": "javascript",
		"a.jsx": "javascriptreact",
		"a.rb":  "ruby",   // via fallback (config key)
		"a.vue": "vue",    // via fallback
		"a.py":  "python", // via fallback
	}
	for path, want := range cases {
		fallback := want // the caller passes the config language key
		if got := languageIDFor(path, fallback); got != want {
			t.Errorf("languageIDFor(%q) = %q, want %q", path, got, want)
		}
	}
}

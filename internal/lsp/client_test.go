package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
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
	addedFolders   []string          // workspace/didChangeWorkspaceFolders added URIs
	tsResponse     []json.RawMessage // last tsserver/response [id, result]
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
	case "workspace/didChangeWorkspaceFolders":
		var p didChangeWorkspaceFoldersParams
		_ = json.Unmarshal(params, &p)
		for _, f := range p.Event.Added {
			s.addedFolders = append(s.addedFolders, f.URI)
		}
	case "tsserver/response":
		// Mimic vscode-jsonrpc: array params are spread into the handler, so the
		// editor wraps the payload ([[id,result]]) and the handler sees [id,result].
		var outer []json.RawMessage
		_ = json.Unmarshal(params, &outer)
		if len(outer) == 1 {
			var inner []json.RawMessage
			if json.Unmarshal(outer[0], &inner) == nil && len(inner) >= 2 {
				s.tsResponse = inner
				break
			}
		}
		s.tsResponse = outer
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

// A DidChange for a doc this client never opened (e.g. after a crash restart:
// the file was opened on the dead predecessor) must upgrade to didOpen — most
// servers silently drop a didChange for an unknown document.
func TestDidChangeUpgradesToDidOpenForUnopenedDoc(t *testing.T) {
	c, server := startTestClient(t, nil)

	c.DidChange("/proj/a.go", "package a", 0)
	waitFor(t, func() bool {
		server.mu.Lock()
		defer server.mu.Unlock()
		return len(server.opened) == 1
	})
	server.mu.Lock()
	if len(server.changed) != 0 {
		t.Fatalf("first sync of an unopened doc must be didOpen, saw didChange %v", server.changed)
	}
	server.mu.Unlock()

	// Now the doc is known: the next DidChange is a plain didChange.
	c.DidChange("/proj/a.go", "package a // x", 0)
	waitFor(t, func() bool {
		server.mu.Lock()
		defer server.mu.Unlock()
		return len(server.changed) == 1
	})
	server.mu.Lock()
	if len(server.opened) != 1 || server.changed[0] != 2 {
		t.Fatalf("second sync: opened=%v changed=%v", server.opened, server.changed)
	}
	server.mu.Unlock()
}

// A dead server must be replaced on next demand instead of leaving the language
// silently broken for the session, and give up after maxServerRestarts.
func TestManagerReplacesDeadServer(t *testing.T) {
	msgs := make(chan any, 8)
	cfg := config.Config{Languages: map[string]config.LanguageConfig{
		// "true" exists everywhere and exits instantly — good enough: the test
		// only exercises the registry's replacement logic, not the handshake.
		"go": {Extensions: []string{".go"}, LSP: &config.LSPServerConfig{Command: "true"}},
	}}
	m := NewManager(cfg, "/tmp")
	m.SetSink(func(msg any) { msgs <- msg })

	// Seed a corpse: a client already marked dead, as after a crash.
	corpse := newServerClient("go", config.LSPServerConfig{Command: "true"}, "/tmp", m.emit)
	corpse.dead = true
	m.clients["go"] = corpse

	got, ok := m.ClientFor("/tmp/a.go")
	if !ok {
		t.Fatal("a dead server must be replaced, not reported unavailable")
	}
	if got == Client(corpse) {
		t.Fatal("ClientFor returned the dead client instead of a replacement")
	}
	if m.restarts["go"] != 1 {
		t.Fatalf("restarts = %d, want 1", m.restarts["go"])
	}

	// Crash-loop: after maxServerRestarts replacements the language disables.
	for i := m.restarts["go"]; i < maxServerRestarts; i++ {
		m.mu.Lock()
		c := m.clients["go"]
		m.mu.Unlock()
		c.mu.Lock()
		c.dead = true
		c.mu.Unlock()
		m.ClientFor("/tmp/a.go")
	}
	if m.disabled["go"] == "" {
		t.Fatal("language must disable after repeated crashes")
	}
	if _, ok := m.ClientFor("/tmp/a.go"); ok {
		t.Fatal("disabled language must not hand out clients")
	}
	// The disable reason surfaces through UnavailableReason (what Ctrl+J shows),
	// naming the crash — not the misleading --prepare-lsp install hint.
	if r := m.UnavailableReason("/tmp/a.go"); !strings.Contains(r, "crashed repeatedly") {
		t.Fatalf("UnavailableReason = %q, want a crashed-repeatedly explanation", r)
	}
	if r := m.UnavailableReason("/tmp/x.unknown"); !strings.Contains(r, "--prepare-lsp") {
		t.Fatalf("unmapped ext should keep the install hint, got %q", r)
	}
	m.ShutdownAll()
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

// EnsureFolder adds a repo as a workspace folder exactly once; the init folder
// (the client's root) and repeats are no-ops.
func TestEnsureFolderAddsOncePerRepo(t *testing.T) {
	c, server := startTestClient(t, nil) // root "/proj" → folders{"/proj"}

	c.EnsureFolder("/proj")  // already the init folder → no notify
	c.EnsureFolder("/other") // new → one didChangeWorkspaceFolders{added:[/other]}
	c.EnsureFolder("/other") // duplicate → no further notify

	waitFor(t, func() bool {
		server.mu.Lock()
		defer server.mu.Unlock()
		return len(server.addedFolders) == 1
	})
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.addedFolders[0] != PathToURI("/other") {
		t.Fatalf("added folder uri: %q", server.addedFolders[0])
	}
}

// A server that exits unexpectedly surfaces a notice carrying the stderr crash
// reason — the fix for silent "connection is closed" failures.
func TestWatchExitReportsCrashWithStderr(t *testing.T) {
	msgs := make(chan any, 4)
	c := newServerClient("go", config.LSPServerConfig{}, "/proj", func(m any) { msgs <- m })
	c.stderr = &tailBuffer{max: stderrTailBytes}
	cmd := exec.Command("sh", "-c", "echo 'TypeError: cannot read protocol' >&2; exit 1")
	cmd.Stderr = c.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c.exited = make(chan struct{})
	go c.watchExit(cmd)
	<-c.exited

	c.mu.Lock()
	dead := c.dead
	c.mu.Unlock()
	if !dead {
		t.Fatal("client should be marked dead after the process exits")
	}
	select {
	case raw := <-msgs:
		m, ok := raw.(NoticeMsg)
		if !ok {
			t.Fatalf("want NoticeMsg, got %T", raw)
		}
		if !strings.Contains(m.Text, "go lsp exited") || !strings.Contains(m.Text, "TypeError: cannot read protocol") {
			t.Fatalf("crash notice missing status/reason: %q", m.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no crash notice emitted")
	}
}

// A deliberate stop (stopping=true) must not emit a crash notice.
func TestWatchExitSilentOnDeliberateStop(t *testing.T) {
	msgs := make(chan any, 4)
	c := newServerClient("go", config.LSPServerConfig{}, "/proj", func(m any) { msgs <- m })
	c.stderr = &tailBuffer{max: stderrTailBytes}
	c.stopping = true
	cmd := exec.Command("sh", "-c", "exit 0")
	cmd.Stderr = c.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c.exited = make(chan struct{})
	go c.watchExit(cmd)
	<-c.exited

	select {
	case raw := <-msgs:
		t.Fatalf("deliberate stop must not notify, got %T %v", raw, raw)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestCrashReason(t *testing.T) {
	node := "/x/server.js:40\n\tprojectInfoPromise = ...\n\t^\nTypeError: Cannot read properties of undefined (reading 'protocol')\n    at getLanguageService\nNode.js v24.15.0\n"
	if got := crashReason(node); !strings.HasPrefix(got, "TypeError: Cannot read properties") {
		t.Fatalf("node crash: %q", got)
	}
	if got := crashReason("panic: runtime error\n\ngoroutine 1:\n"); !strings.HasPrefix(got, "panic: runtime error") {
		t.Fatalf("go panic: %q", got)
	}
	if got := crashReason("   \n\n  "); got != "" {
		t.Fatalf("blank stderr should yield empty, got %q", got)
	}
}

// The hybrid relay: a tsserver/request from the (Vue) server is forwarded via
// tsBridge and answered with a tsserver/response carrying the same id + result.
func TestTsserverRequestRelay(t *testing.T) {
	c, server := startTestClient(t, nil)
	var gotCommand string
	c.tsBridge = func(command string, args json.RawMessage) (json.RawMessage, error) {
		gotCommand = command
		return json.RawMessage(`{"configFileName":"/proj/tsconfig.json"}`), nil
	}

	// The (fake) Vue server asks the editor to run a tsserver command. vscode-
	// jsonrpc wraps a single array param, so the wire payload is [[id,cmd,args]].
	_ = server.conn.Notify("tsserver/request", []any{[]any{7, "_vue:projectInfo", map[string]any{"file": "/proj/a.vue"}}})

	waitFor(t, func() bool {
		server.mu.Lock()
		defer server.mu.Unlock()
		return server.tsResponse != nil
	})
	server.mu.Lock()
	defer server.mu.Unlock()
	if gotCommand != "_vue:projectInfo" {
		t.Fatalf("bridge got command %q", gotCommand)
	}
	if len(server.tsResponse) != 2 {
		t.Fatalf("response arity: %v", server.tsResponse)
	}
	var id int
	_ = json.Unmarshal(server.tsResponse[0], &id)
	if id != 7 {
		t.Fatalf("response id = %d, want 7", id)
	}
	var body struct {
		ConfigFileName string `json:"configFileName"`
	}
	_ = json.Unmarshal(server.tsResponse[1], &body)
	if body.ConfigFileName != "/proj/tsconfig.json" {
		t.Fatalf("response body = %s", server.tsResponse[1])
	}
}

// A bridge failure must still answer (null), so the Vue server's promise never
// hangs.
func TestTsserverRequestNullOnBridgeError(t *testing.T) {
	c, server := startTestClient(t, nil)
	c.tsBridge = func(string, json.RawMessage) (json.RawMessage, error) {
		return nil, errors.New("companion unavailable")
	}
	_ = server.conn.Notify("tsserver/request", []any{[]any{9, "_vue:quickinfo", map[string]any{}}})

	waitFor(t, func() bool {
		server.mu.Lock()
		defer server.mu.Unlock()
		return server.tsResponse != nil
	})
	server.mu.Lock()
	defer server.mu.Unlock()
	var id int
	_ = json.Unmarshal(server.tsResponse[0], &id)
	if id != 9 {
		t.Fatalf("id = %d", id)
	}
	if string(server.tsResponse[1]) != "null" {
		t.Fatalf("result should be null on error, got %s", server.tsResponse[1])
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
	// Disabled languages keep their extension mapping (extLang is immutable so
	// runtime Enable can work without touching it); they are gated by a seeded
	// disabled reason instead, so no client resolves and the reason surfaces.
	if m.extLang[".ts"] != "typescript" {
		t.Fatalf("disabled language should still map its extension: %v", m.extLang)
	}
	if _, ok := m.ClientFor("/x/a.ts"); ok {
		t.Fatal("disabled language must not resolve a client")
	}
	if reason := m.UnavailableReason("/x/a.ts"); reason != "disabled in config" {
		t.Fatalf("UnavailableReason = %q", reason)
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

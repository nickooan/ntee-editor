package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nickooan/ntee-editor/internal/config"
)

// serverClient is one running language server. Startup (spawn + initialize
// handshake) happens on a goroutine. Doc-sync notifications go through a
// bounded op queue drained by a single writer goroutine, so a server that
// stops consuming stdin can never block the caller (the UI goroutine) — ops
// enqueued before initialize completes simply wait in the same queue.
type serverClient struct {
	lang string // config key, used as languageId fallback
	conf config.LSPServerConfig
	root string
	sink func(any)

	// startedAt is when this client was constructed (immediately before its
	// process spawns); immutable, so read without the lock. The registry uses
	// it to tell a long-lived server's crash from a startup crash-loop.
	startedAt time.Time

	// wake nudges the writer goroutine (cap 1 — a pending nudge is enough).
	// Allocated at construction; never closed (the writer exits on dead).
	wake chan struct{}

	// bridgeSem caps concurrent tsserver/request relays (each can block up to
	// lspQueryTimeout on the companion). Saturation replies null immediately
	// instead of queueing — the Vue server treats null as "no result".
	bridgeSem chan struct{}

	mu        sync.Mutex
	conn      *Conn
	cmd       *exec.Cmd
	ready     bool
	dead      bool
	stopping  bool          // shutting down on purpose — suppress the crash notice
	exited    chan struct{} // closed when the exit watcher has reaped the process
	stderr    *tailBuffer   // last few KB of the server's stderr, for crash reports
	queue     []queuedOp
	queueFull bool // one-shot latch for the overflow notice
	writerOn  bool // writerLoop started (once, in becomeReady)
	versions  map[string]int
	folders   map[string]bool // repo roots registered as workspace folders
	tsBridge  tsBridgeFunc    // non-nil for a hybrid server (e.g. Vue): relays tsserver/request
	mirror    docMirrorFunc   // non-nil for a hybrid server: mirrors doc-sync to the companion
	// companionFor returns the companion server (tsserver) for a hybrid file.
	// In Volar hybrid mode the Vue server answers only template features;
	// <script> definitions/hover/refs come from tsserver, so queries are asked
	// of both and merged. nil for non-hybrid servers.
	companionFor func(file string) (*serverClient, bool)
}

// queuedOp is one doc-sync notification awaiting the writer goroutine. path
// is the coalescing key for opChange; do performs the actual (blocking) pipe
// write and any send-time bookkeeping (versions), given the live connection.
type queuedOp struct {
	kind opKind
	path string
	do   func(conn *Conn)
}

type opKind int

const (
	opOpen opKind = iota
	opChange
	opSave
	opClose
	opFolder
)

// maxQueuedOps bounds the writer queue. With per-path didChange coalescing
// the queue only grows past a handful of entries when the server has stopped
// consuming stdin entirely; at the cap, change/save ops are dropped (with one
// notice) while open/close/folder — protocol-state critical and bounded by
// user actions — always append.
const maxQueuedOps = 1024

// maxInflightBridge bounds concurrent tsserver/request relay goroutines.
const maxInflightBridge = 8

// tsBridgeFunc relays a hybrid server's tsserver/request command to its
// companion server and returns the (unwrapped) result. Injected by the Manager
// for servers whose config declares a bridge.
type tsBridgeFunc func(command string, args json.RawMessage) (json.RawMessage, error)

// docMirrorFunc forwards a hybrid server's document sync to its companion so
// the companion (tsserver + @vue/typescript-plugin) has the file in a project
// and can answer _vue:* commands. kind is open/change/save/close.
type docMirrorFunc func(kind, path, content string)

// stderrTailBytes bounds how much server stderr we retain to explain a crash.
const stderrTailBytes = 4096

// lspQueryTimeout bounds definition/references/completion (and the hybrid
// bridge relay). Generous because a cold server — especially a Vue→tsserver
// hybrid round-trip that loads the TS project on first use — can take several
// seconds; a short timeout surfaces as "context deadline exceeded".
const lspQueryTimeout = 12 * time.Second

// tailBuffer is an io.Writer that keeps only the last max bytes written — the
// tail of a server's stderr, enough to surface a crash reason without growing
// unbounded.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func newServerClient(lang string, conf config.LSPServerConfig, root string, sink func(any)) *serverClient {
	return &serverClient{
		lang:      lang,
		conf:      conf,
		root:      root,
		sink:      sink,
		wake:      make(chan struct{}, 1),
		bridgeSem: make(chan struct{}, maxInflightBridge),
		versions:  map[string]int{},
		folders:   map[string]bool{root: true}, // the initial workspace folder
		startedAt: time.Now(),
	}
}

// stdioConn adapts a child process's stdio pipes to io.ReadWriteCloser.
type stdioConn struct {
	stdout io.ReadCloser
	stdin  io.WriteCloser
}

func (s stdioConn) Read(p []byte) (int, error)  { return s.stdout.Read(p) }
func (s stdioConn) Write(p []byte) (int, error) { return s.stdin.Write(p) }
func (s stdioConn) Close() error {
	_ = s.stdin.Close()
	return s.stdout.Close()
}

// ResolveBinary reports the resolved path of a server command using the same
// rules the editor uses. Exposed so the --prepare-lsp tooling can verify
// installs identically.
func ResolveBinary(command string) (string, error) { return resolveBinary(command) }

// resolveBinary finds the server executable: absolute path, then PATH, then
// ~/go/bin (where go install puts gopls, often missing from GUI-shell PATHs).
func resolveBinary(command string) (string, error) {
	if filepath.IsAbs(command) {
		if info, err := os.Stat(command); err == nil && info.Mode().IsRegular() {
			return command, nil
		}
		return "", errors.New("not found: " + command)
	}
	// A non-absolute command containing a separator would make exec.LookPath
	// resolve it relative to the process cwd — the launch directory, which the
	// opened project controls. Only absolute paths or bare names are allowed.
	if strings.ContainsRune(command, '/') || strings.ContainsRune(command, os.PathSeparator) {
		return "", errors.New("relative server path not allowed: " + command)
	}
	if path, err := exec.LookPath(command); err == nil {
		return path, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, "go", "bin", command)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return "", errors.New("not found: " + command)
}

// envWithPathPrefix returns the process env with dir prepended to PATH.
func envWithPathPrefix(dir string) []string {
	env := os.Environ()
	newPath := "PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH")
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			env[i] = newPath
			return env
		}
	}
	return append(env, newPath)
}

func languageIDFor(path, fallback string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".ts", ".mts", ".cts":
		return "typescript"
	case ".tsx":
		return "typescriptreact"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".jsx":
		return "javascriptreact"
	case ".vue":
		return "vue" // Volar's @vue/typescript-plugin handles the "vue" languageId
	default:
		return fallback
	}
}

// start spawns the server and runs the initialize handshake. Failures mark
// the client dead and surface one notice.
func (c *serverClient) start() {
	fail := func(text string) {
		c.mu.Lock()
		c.dead = true
		c.queue = nil
		c.mu.Unlock()
		c.poke() // let a running writer observe dead and exit
		if c.sink != nil {
			c.sink(NoticeMsg{Text: text})
		}
	}

	bin, err := resolveBinary(c.conf.Command)
	if err != nil {
		fail(c.conf.Command + " not found — LSP disabled for " + c.lang)
		return
	}
	cmd := exec.Command(bin, c.conf.Args...)
	cmd.Dir = c.root
	// Script servers (typescript-language-server is `#!/usr/bin/env node`)
	// need their runtime on PATH; in nvm-style installs node sits next to the
	// script, so prepend the binary's own directory.
	cmd.Env = envWithPathPrefix(filepath.Dir(bin))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fail("lsp: " + err.Error())
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fail("lsp: " + err.Error())
		return
	}
	// Retain the tail of stderr so a crash can be explained.
	c.stderr = &tailBuffer{max: stderrTailBytes}
	cmd.Stderr = c.stderr
	if err := cmd.Start(); err != nil {
		fail(c.lang + " lsp failed to start: " + err.Error())
		return
	}

	// The process is live: one goroutine owns cmd.Wait() and reports an
	// unexpected exit (a crash) with the tail of stderr. If a stop() landed
	// while the process was spawning it saw cmd == nil and killed nothing —
	// reap the child here instead of registering it (no watcher exists yet).
	c.mu.Lock()
	if c.stopping || c.dead {
		c.mu.Unlock()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return
	}
	c.cmd = cmd
	c.exited = make(chan struct{})
	c.mu.Unlock()
	go c.watchExit(cmd)

	conn := NewConn(stdioConn{stdout: stdout, stdin: stdin}, c.handle)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err = conn.Request(ctx, "initialize", initializeParams{
		ProcessID:             os.Getpid(),
		RootURI:               PathToURI(c.root),
		WorkspaceFolders:      []workspaceFolder{{URI: PathToURI(c.root), Name: filepath.Base(c.root)}},
		Capabilities:          clientCapabilities,
		InitializationOptions: c.conf.Init,
	})
	if err != nil {
		// The handshake failed — often the server died on startup. Flag stopping
		// so watchExit stays quiet, then surface the reason (with stderr tail).
		c.mu.Lock()
		c.stopping = true
		c.mu.Unlock()
		conn.Close()
		_ = cmd.Process.Kill()
		fail(c.lang + " lsp initialize failed: " + describeExit(err, c.stderr))
		return
	}
	_ = conn.Notify("initialized", struct{}{})

	if !c.becomeReady(conn) {
		// A stop landed during the handshake: don't go live on a client the
		// registry has already dropped.
		conn.Close()
		_ = cmd.Process.Kill()
		return
	}
	if c.sink != nil {
		c.sink(NoticeMsg{Text: c.lang + " lsp ready"})
	}
}

// watchExit owns cmd.Wait(): when the server process exits it marks the client
// dead, unblocks any in-flight request by closing the connection, and — unless
// the exit was a deliberate stop — surfaces a crash notice with the reason.
func (c *serverClient) watchExit(cmd *exec.Cmd) {
	waitErr := cmd.Wait()

	c.mu.Lock()
	stopping := c.stopping
	c.dead = true
	c.ready = false
	c.queue = nil
	conn := c.conn
	c.mu.Unlock()
	c.poke() // let the writer observe dead and exit

	if conn != nil {
		_ = conn.Close() // fail pending requests instead of hanging
	}
	if !stopping && c.sink != nil {
		c.sink(NoticeMsg{Text: c.lang + " lsp exited: " + describeExit(waitErr, c.stderr)})
	}
	close(c.exited)
}

// describeExit combines the process exit status with the most error-looking
// line of its stderr, e.g. "exit status 1 — TypeError: Cannot read properties
// of undefined (reading 'protocol')".
func describeExit(waitErr error, stderr *tailBuffer) string {
	msg := "process exited"
	if waitErr != nil {
		msg = waitErr.Error()
	}
	if stderr != nil {
		if reason := crashReason(stderr.String()); reason != "" {
			msg += " — " + reason
		}
	}
	return msg
}

// crashReason picks the most useful single line from server stderr: an
// error/panic line if present (Node "TypeError: …", Go "panic: …"), else the
// first non-empty line. Clipped so the status bar stays readable.
func crashReason(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	clip := func(l string) string {
		l = strings.TrimSpace(l)
		if len(l) > 200 {
			return l[:200] + "…"
		}
		return l
	}
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.Contains(t, "Error") || strings.HasPrefix(t, "panic:") || strings.Contains(t, "Exception") {
			return clip(t)
		}
	}
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" {
			return clip(t)
		}
	}
	return ""
}

// isDead reports whether the server process has exited (crash or stop). The
// registry uses it to replace the corpse with a fresh server on next demand.
func (c *serverClient) isDead() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dead
}

// uptime is how long this client has existed — for a dead one, roughly how long
// the server ran before crashing.
func (c *serverClient) uptime() time.Duration {
	return time.Since(c.startedAt)
}

// enqueue appends a doc-sync op for the writer goroutine. An opChange for a
// path already queued replaces it in place (each didChange carries the full
// content, so the newest supersedes losslessly and queue order is preserved).
func (c *serverClient) enqueue(op queuedOp) {
	c.mu.Lock()
	if c.dead {
		c.mu.Unlock()
		return
	}
	if op.kind == opChange {
		for i := len(c.queue) - 1; i >= 0; i-- {
			if c.queue[i].kind == opChange && c.queue[i].path == op.path {
				c.queue[i] = op
				c.mu.Unlock()
				c.poke()
				return
			}
		}
	}
	if len(c.queue) >= maxQueuedOps && (op.kind == opChange || op.kind == opSave) {
		notify := !c.queueFull
		c.queueFull = true
		c.mu.Unlock()
		if notify && c.sink != nil {
			c.sink(NoticeMsg{Text: c.lang + " lsp is not consuming input — dropping edits"})
		}
		return
	}
	c.queue = append(c.queue, op)
	c.mu.Unlock()
	c.poke()
}

// poke nudges the writer without ever blocking (cap-1 channel).
func (c *serverClient) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// writerLoop is the only place doc-sync ops touch the child's stdin. It
// drains the queue whenever poked and the client is ready; the blocking pipe
// write happens with no locks held, so a wedged server parks this goroutine —
// never the UI. Exits when the client dies (death paths poke wake).
func (c *serverClient) writerLoop() {
	for range c.wake {
		for {
			c.mu.Lock()
			if c.dead {
				c.mu.Unlock()
				return
			}
			if !c.ready || len(c.queue) == 0 {
				c.mu.Unlock()
				break
			}
			op := c.queue[0]
			c.queue = c.queue[1:]
			conn := c.conn
			c.mu.Unlock()
			op.do(conn)
		}
	}
}

// becomeReady installs the live connection and starts the writer that drains
// the op queue (which doubles as the pre-ready replay buffer). Returns false
// when a stop landed during the handshake — the caller must tear the
// connection and process down instead of going live.
func (c *serverClient) becomeReady(conn *Conn) bool {
	c.mu.Lock()
	if c.stopping || c.dead {
		c.mu.Unlock()
		return false
	}
	c.conn = conn
	c.ready = true
	if !c.writerOn {
		c.writerOn = true
		go c.writerLoop()
	}
	c.mu.Unlock()
	c.poke()
	return true
}

func (c *serverClient) DidOpen(path, content string) {
	c.enqueue(queuedOp{kind: opOpen, path: path, do: func(conn *Conn) {
		c.mu.Lock()
		c.versions[path] = 1
		c.mu.Unlock()
		_ = conn.Notify("textDocument/didOpen", didOpenParams{TextDocument: textDocumentItem{
			URI:        PathToURI(path),
			LanguageID: languageIDFor(path, c.lang),
			Version:    1,
			Text:       content,
		}})
	}})
	if c.mirror != nil {
		c.mirror("open", path, content)
	}
}

// EnsureFolder registers repo as a workspace folder if the server does not
// already know it, so opening a file in a new repo scopes the (single) server
// to that repo instead of spawning another. Queued through run() so it lands
// after initialize and before the didOpen that follows it.
func (c *serverClient) EnsureFolder(repo string) {
	c.mu.Lock()
	if c.dead || c.folders[repo] {
		c.mu.Unlock()
		return // already known (or nothing to notify) — dedup at enqueue time
	}
	c.folders[repo] = true
	c.mu.Unlock()
	c.enqueue(queuedOp{kind: opFolder, path: repo, do: func(conn *Conn) {
		_ = conn.Notify("workspace/didChangeWorkspaceFolders", didChangeWorkspaceFoldersParams{
			Event: workspaceFoldersChangeEvent{
				Added: []workspaceFolder{{URI: PathToURI(repo), Name: filepath.Base(repo)}},
			},
		})
	}})
}

func (c *serverClient) DidChange(path, content string, _ int) {
	c.enqueue(queuedOp{kind: opChange, path: path, do: func(conn *Conn) {
		c.mu.Lock()
		_, opened := c.versions[path]
		c.versions[path]++
		version := c.versions[path]
		c.mu.Unlock()
		if !opened {
			// The doc was never opened on THIS client — e.g. the server was
			// restarted after a crash and the file predates it. Servers drop a
			// didChange for an unknown doc, so upgrade it to didOpen: the full
			// content is the same either way (full sync).
			_ = conn.Notify("textDocument/didOpen", didOpenParams{TextDocument: textDocumentItem{
				URI:        PathToURI(path),
				LanguageID: languageIDFor(path, c.lang),
				Version:    version,
				Text:       content,
			}})
			return
		}
		_ = conn.Notify("textDocument/didChange", didChangeParams{
			TextDocument:   versionedTextDocumentIdentifier{URI: PathToURI(path), Version: version},
			ContentChanges: []contentChange{{Text: content}}, // full sync
		})
	}})
	if c.mirror != nil {
		c.mirror("change", path, content)
	}
}

func (c *serverClient) DidSave(path string) {
	c.enqueue(queuedOp{kind: opSave, path: path, do: func(conn *Conn) {
		_ = conn.Notify("textDocument/didSave", didSaveParams{TextDocument: textDocumentIdentifier{URI: PathToURI(path)}})
	}})
	if c.mirror != nil {
		c.mirror("save", path, "")
	}
}

func (c *serverClient) DidClose(path string) {
	c.enqueue(queuedOp{kind: opClose, path: path, do: func(conn *Conn) {
		c.mu.Lock()
		delete(c.versions, path)
		c.mu.Unlock()
		_ = conn.Notify("textDocument/didClose", didCloseParams{TextDocument: textDocumentIdentifier{URI: PathToURI(path)}})
	}})
	if c.mirror != nil {
		c.mirror("close", path, "")
	}
}

// Definition asks the server for the symbol's definition. Errors while the
// server is still starting so the caller can fall back to the heuristic.
func (c *serverClient) Definition(path string, line, utf16Col int) ([]Location, error) {
	c.mu.Lock()
	conn, ready := c.conn, c.ready && !c.dead
	c.mu.Unlock()
	if !ready {
		return nil, errors.New("language server not ready")
	}
	ctx, cancel := context.WithTimeout(context.Background(), lspQueryTimeout)
	defer cancel()
	raw, err := conn.Request(ctx, "textDocument/definition", definitionParams{
		TextDocument: textDocumentIdentifier{URI: PathToURI(path)},
		Position:     Position{Line: line, Character: utf16Col},
	})
	if err != nil {
		return nil, err
	}
	locs := parseLocations(raw)
	// Hybrid: the Vue server only answers template definitions; ask tsserver for
	// <script> symbols/imports (it has the .vue open via the plugin) and merge.
	if c.companionFor != nil {
		if ts, ok := c.companionFor(path); ok {
			if extra, derr := ts.Definition(path, line, utf16Col); derr == nil {
				locs = mergeLocations(locs, extra)
			}
		}
	}
	return locs, nil
}

// mergeLocations concatenates location lists, dropping duplicates (same URI and
// start position).
func mergeLocations(a, b []Location) []Location {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]Location, 0, len(a)+len(b))
	for _, group := range [][]Location{a, b} {
		for _, l := range group {
			key := l.URI + fmt.Sprintf(":%d:%d", l.Range.Start.Line, l.Range.Start.Character)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, l)
		}
	}
	return out
}

// References asks the server for all reference sites of the symbol at the
// position (excluding the declaration itself — the caller is sitting on it).
func (c *serverClient) References(path string, line, utf16Col int) ([]Location, error) {
	c.mu.Lock()
	conn, ready := c.conn, c.ready && !c.dead
	c.mu.Unlock()
	if !ready {
		return nil, errors.New("language server not ready")
	}
	ctx, cancel := context.WithTimeout(context.Background(), lspQueryTimeout)
	defer cancel()
	raw, err := conn.Request(ctx, "textDocument/references", referenceParams{
		TextDocument: textDocumentIdentifier{URI: PathToURI(path)},
		Position:     Position{Line: line, Character: utf16Col},
		Context:      referenceContext{IncludeDeclaration: false},
	})
	if err != nil {
		return nil, err
	}
	locs := parseLocations(raw)
	if c.companionFor != nil {
		if ts, ok := c.companionFor(path); ok {
			if extra, rerr := ts.References(path, line, utf16Col); rerr == nil {
				locs = mergeLocations(locs, extra)
			}
		}
	}
	return locs, nil
}

// Completion asks the server for completion candidates at the position (member
// completions after ".", scope symbols for a bare prefix).
func (c *serverClient) Completion(path string, line, utf16Col int) ([]CompletionItem, error) {
	c.mu.Lock()
	conn, ready := c.conn, c.ready && !c.dead
	c.mu.Unlock()
	if !ready {
		return nil, errors.New("language server not ready")
	}
	ctx, cancel := context.WithTimeout(context.Background(), lspQueryTimeout)
	defer cancel()
	raw, err := conn.Request(ctx, "textDocument/completion", completionParams{
		TextDocument: textDocumentIdentifier{URI: PathToURI(path)},
		Position:     Position{Line: line, Character: utf16Col},
	})
	if err != nil {
		return nil, err
	}
	return parseCompletion(raw), nil
}

// ExecuteCommand runs a workspace/executeCommand on this server. Used by the
// hybrid bridge to relay a Vue tsserver command to the TypeScript server.
func (c *serverClient) ExecuteCommand(command string, args []any) (json.RawMessage, error) {
	c.mu.Lock()
	conn, ready := c.conn, c.ready && !c.dead
	c.mu.Unlock()
	if !ready {
		return nil, errors.New("language server not ready")
	}
	ctx, cancel := context.WithTimeout(context.Background(), lspQueryTimeout)
	defer cancel()
	return conn.Request(ctx, "workspace/executeCommand", executeCommandParams{
		Command:   command,
		Arguments: args,
	})
}

// handleTsserverRequest relays a hybrid server's `tsserver/request`
// `[id, command, args]` to its companion via c.tsBridge, then replies with
// `tsserver/response [id, result]`. Runs off the notification worker (the bridge
// blocks on the companion) and always replies — null on failure — so the Vue
// server's awaited promise never hangs.
func (c *serverClient) handleTsserverRequest(params json.RawMessage) {
	// Volar sends [requestId, command, args]; the JSON-RPC single-array param is
	// wrapped on the wire as [[requestId, command, args]]. Accept either.
	var arr []json.RawMessage
	if err := json.Unmarshal(params, &arr); err != nil {
		return
	}
	if len(arr) == 1 {
		var inner []json.RawMessage
		if json.Unmarshal(arr[0], &inner) == nil && len(inner) >= 2 {
			arr = inner
		}
	}
	if len(arr) < 2 {
		return
	}
	var id int
	var command string
	_ = json.Unmarshal(arr[0], &id)
	_ = json.Unmarshal(arr[1], &command)
	var args json.RawMessage
	if len(arr) >= 3 {
		args = arr[2]
	}
	bridge := c.tsBridge
	go func() {
		var result any // nil → JSON null
		if bridge != nil {
			select {
			case c.bridgeSem <- struct{}{}:
				if r, err := bridge(command, args); err == nil {
					result = r
				}
				<-c.bridgeSem
			default:
				// All relay slots are busy blocking on the companion: reply
				// null now rather than pile up more 12s waits.
			}
		}
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()
		if conn != nil {
			// vscode-jsonrpc spreads array params into the handler and Volar's
			// handler is `([id,res]) => …` (one arg), so the payload must be
			// wrapped: wire [[id, result]] → spread → handler([id, result]).
			_ = conn.Notify("tsserver/response", []any{[]any{id, result}})
		}
	}()
}

// handle answers server→client traffic: diagnostics to the sink, config
// requests with nulls, everything else tolerated silently.
func (c *serverClient) handle(method string, params json.RawMessage) (any, error) {
	switch method {
	case "tsserver/request":
		c.handleTsserverRequest(params)
		return nil, nil
	case "textDocument/publishDiagnostics":
		var p publishDiagnosticsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, nil
		}
		path, ok := URIToPath(p.URI)
		if !ok {
			return nil, nil
		}
		items := make([]Diagnostic, 0, len(p.Diagnostics))
		for _, d := range p.Diagnostics {
			items = append(items, Diagnostic{
				Line:     d.Range.Start.Line,
				Col:      d.Range.Start.Character,
				Severity: d.Severity,
				Message:  d.Message,
			})
		}
		if c.sink != nil {
			c.sink(DiagnosticsMsg{Path: path, Items: items})
		}
		return nil, nil
	case "workspace/configuration":
		var p struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(params, &p)
		return make([]any, len(p.Items)), nil
	default:
		// registerCapability, workDoneProgress/create, window/*, $/progress…
		return nil, nil
	}
}

// stop runs the shutdown/exit handshake with a short grace period, then kills.
// It flags stopping so watchExit (the sole cmd.Wait() owner) treats the exit as
// deliberate and stays silent, and waits on that watcher rather than reaping
// the process itself.
func (c *serverClient) stop() {
	c.mu.Lock()
	if c.stopping {
		c.mu.Unlock()
		return // already stopping (or a failed handshake already tore it down)
	}
	c.stopping = true
	conn, cmd, exited := c.conn, c.cmd, c.exited
	c.dead = true
	c.ready = false
	c.queue = nil
	c.mu.Unlock()
	c.poke() // let the writer observe dead and exit

	if conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, _ = conn.Request(ctx, "shutdown", nil)
		cancel()
		_ = conn.Notify("exit", nil)
		_ = conn.Close()
	}
	if cmd != nil && cmd.Process != nil {
		if exited == nil {
			_ = cmd.Process.Kill() // never got a watcher; best-effort
			return
		}
		select {
		case <-exited:
		case <-time.After(500 * time.Millisecond):
			_ = cmd.Process.Kill()
			<-exited
		}
	}
}

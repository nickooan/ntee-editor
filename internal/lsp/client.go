package lsp

import (
	"context"
	"encoding/json"
	"errors"
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
// handshake) happens on a goroutine; doc-sync notifications issued before the
// server is ready are queued and replayed in order once it is.
type serverClient struct {
	lang string // config key, used as languageId fallback
	conf config.LSPServerConfig
	root string
	sink func(any)

	mu       sync.Mutex
	conn     *Conn
	cmd      *exec.Cmd
	ready    bool
	dead     bool
	pending  []func()
	versions map[string]int
	folders  map[string]bool // repo roots registered as workspace folders
}

func newServerClient(lang string, conf config.LSPServerConfig, root string, sink func(any)) *serverClient {
	return &serverClient{
		lang:     lang,
		conf:     conf,
		root:     root,
		sink:     sink,
		versions: map[string]int{},
		folders:  map[string]bool{root: true}, // the initial workspace folder
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

// resolveBinary finds the server executable: absolute path, then PATH, then
// ~/go/bin (where go install puts gopls, often missing from GUI-shell PATHs).
// ResolveBinary reports the resolved path of a server command using the same
// rules the editor uses (absolute path, PATH, then ~/go/bin). Exposed so the
// --prepare-lsp tooling can verify installs identically.
func ResolveBinary(command string) (string, error) { return resolveBinary(command) }

func resolveBinary(command string) (string, error) {
	if filepath.IsAbs(command) {
		if info, err := os.Stat(command); err == nil && info.Mode().IsRegular() {
			return command, nil
		}
		return "", errors.New("not found: " + command)
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
		c.pending = nil
		c.mu.Unlock()
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
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		fail(c.lang + " lsp failed to start: " + err.Error())
		return
	}

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
		conn.Close()
		_ = cmd.Process.Kill()
		fail(c.lang + " lsp initialize failed: " + err.Error())
		return
	}
	_ = conn.Notify("initialized", struct{}{})

	c.mu.Lock()
	c.conn = conn
	c.cmd = cmd
	c.ready = true
	ops := c.pending
	c.pending = nil
	c.mu.Unlock()
	for _, op := range ops {
		op()
	}
	if c.sink != nil {
		c.sink(NoticeMsg{Text: c.lang + " lsp ready"})
	}
}

// run executes a doc-sync op now, or queues it until initialize completes.
func (c *serverClient) run(op func()) {
	c.mu.Lock()
	if c.dead {
		c.mu.Unlock()
		return
	}
	if !c.ready {
		c.pending = append(c.pending, op)
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	op()
}

func (c *serverClient) DidOpen(path, content string) {
	c.run(func() {
		c.mu.Lock()
		c.versions[path] = 1
		conn := c.conn
		c.mu.Unlock()
		_ = conn.Notify("textDocument/didOpen", didOpenParams{TextDocument: textDocumentItem{
			URI:        PathToURI(path),
			LanguageID: languageIDFor(path, c.lang),
			Version:    1,
			Text:       content,
		}})
	})
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
	c.run(func() {
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()
		if conn == nil {
			return
		}
		_ = conn.Notify("workspace/didChangeWorkspaceFolders", didChangeWorkspaceFoldersParams{
			Event: workspaceFoldersChangeEvent{
				Added: []workspaceFolder{{URI: PathToURI(repo), Name: filepath.Base(repo)}},
			},
		})
	})
}

func (c *serverClient) DidChange(path, content string, _ int) {
	c.run(func() {
		c.mu.Lock()
		c.versions[path]++
		version := c.versions[path]
		conn := c.conn
		c.mu.Unlock()
		_ = conn.Notify("textDocument/didChange", didChangeParams{
			TextDocument:   versionedTextDocumentIdentifier{URI: PathToURI(path), Version: version},
			ContentChanges: []contentChange{{Text: content}}, // full sync
		})
	})
}

func (c *serverClient) DidSave(path string) {
	c.run(func() {
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()
		_ = conn.Notify("textDocument/didSave", didSaveParams{TextDocument: textDocumentIdentifier{URI: PathToURI(path)}})
	})
}

func (c *serverClient) DidClose(path string) {
	c.run(func() {
		c.mu.Lock()
		delete(c.versions, path)
		conn := c.conn
		c.mu.Unlock()
		_ = conn.Notify("textDocument/didClose", didCloseParams{TextDocument: textDocumentIdentifier{URI: PathToURI(path)}})
	})
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, err := conn.Request(ctx, "textDocument/definition", definitionParams{
		TextDocument: textDocumentIdentifier{URI: PathToURI(path)},
		Position:     Position{Line: line, Character: utf16Col},
	})
	if err != nil {
		return nil, err
	}
	return parseLocations(raw), nil
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, err := conn.Request(ctx, "textDocument/references", referenceParams{
		TextDocument: textDocumentIdentifier{URI: PathToURI(path)},
		Position:     Position{Line: line, Character: utf16Col},
		Context:      referenceContext{IncludeDeclaration: false},
	})
	if err != nil {
		return nil, err
	}
	return parseLocations(raw), nil
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
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

// handle answers server→client traffic: diagnostics to the sink, config
// requests with nulls, everything else tolerated silently.
func (c *serverClient) handle(method string, params json.RawMessage) (any, error) {
	switch method {
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
func (c *serverClient) stop() {
	c.mu.Lock()
	conn, cmd := c.conn, c.cmd
	c.dead = true
	c.ready = false
	c.mu.Unlock()

	if conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, _ = conn.Request(ctx, "shutdown", nil)
		cancel()
		_ = conn.Notify("exit", nil)
		_ = conn.Close()
	}
	if cmd != nil && cmd.Process != nil {
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
			_ = cmd.Process.Kill()
			<-done
		}
	}
}

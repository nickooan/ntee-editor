// Package lsp integrates language servers (gopls, typescript-language-server)
// over stdio JSON-RPC. The app talks to the Client/Registry interfaces; the
// no-op registry keeps everything working when LSP is disabled or a server
// binary is missing.
package lsp

// Diagnostic is one server-reported issue. Line is 0-based; Col is in UTF-16
// code units (LSP native) — the app only uses Line for gutter markers.
type Diagnostic struct {
	Line     int
	Col      int
	Severity int // 1 error, 2 warning, 3 info, 4 hint (LSP severities)
	Message  string
}

// DiagnosticsMsg is emitted into the Bubble Tea loop on publishDiagnostics.
// Path is absolute.
type DiagnosticsMsg struct {
	Path  string
	Items []Diagnostic
}

// NoticeMsg surfaces a one-time LSP status note in the status line.
type NoticeMsg struct {
	Text string
}

// Client is one language server session. Doc-sync calls are fire-and-forget
// (queued until the server finishes initializing); Definition errors while
// the server is not ready, and callers fall back to the heuristic jump.
type Client interface {
	DidOpen(path, content string)
	DidChange(path, content string, rev int)
	DidSave(path string)
	DidClose(path string)
	Definition(path string, line, utf16Col int) ([]Location, error)
	References(path string, line, utf16Col int) ([]Location, error)
}

// Registry resolves the client responsible for a file path, if any.
type Registry interface {
	ClientFor(path string) (Client, bool)
	ShutdownAll()
}

type noopRegistry struct{}

// NewNoopRegistry returns a registry that never resolves a client (LSP off).
func NewNoopRegistry() Registry { return noopRegistry{} }

func (noopRegistry) ClientFor(string) (Client, bool) { return nil, false }
func (noopRegistry) ShutdownAll()                    {}

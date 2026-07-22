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
// the server is not ready, and callers report that error rather than guessing.
type Client interface {
	DidOpen(path, content string)
	DidChange(path, content string, rev int)
	DidSave(path string)
	DidClose(path string)
	Definition(path string, line, utf16Col int) ([]Location, error)
	References(path string, line, utf16Col int) ([]Location, error)
	Completion(path string, line, utf16Col int) ([]CompletionItem, error)
}

// LangState is a language server's lifecycle state for the inspection pane.
type LangState int

const (
	LangRunning  LangState = iota // server process alive
	LangStopped                   // enabled but not started (lazy) or exited
	LangDisabled                  // config-disabled, binary missing, crash-looped
)

// LangStatus is one configured language's server state.
type LangStatus struct {
	Lang   string
	State  LangState
	Reason string // why disabled; "" otherwise
}

// Registry resolves the client responsible for a file path, if any.
type Registry interface {
	ClientFor(path string) (Client, bool)
	// UnavailableReason explains why ClientFor returns false for path — e.g.
	// "binary not found" vs "crashed repeatedly" — so the UI can show the real
	// cause instead of a generic install hint. "" when a client is available
	// (or could be started) or the registry has nothing specific to say.
	UnavailableReason(path string) string
	// Statuses lists every configured language and its server state, sorted by
	// name. Empty for the noop registry (LSP globally off this session).
	Statuses() []LangStatus
	// Enable clears a language's disabled state and eagerly starts its server
	// rooted at the project, so its status flips to running now. started=false
	// → reason explains (binary missing, registry is noop, …); later crash or
	// handshake errors still arrive async as NoticeMsg.
	Enable(lang string) (started bool, reason string)
	// Disable stops the language's server (if running) and marks it disabled.
	// Safe when the server is not running.
	Disable(lang string)
	ShutdownAll()
}

type noopRegistry struct{}

// NewNoopRegistry returns a registry that never resolves a client (LSP off).
func NewNoopRegistry() Registry { return noopRegistry{} }

func (noopRegistry) ClientFor(string) (Client, bool) { return nil, false }
func (noopRegistry) UnavailableReason(string) string { return "" }
func (noopRegistry) Statuses() []LangStatus          { return nil }
func (noopRegistry) Enable(string) (bool, string) {
	return false, "lsp is off this session — config updated; restart ntee to apply"
}
func (noopRegistry) Disable(string) {}
func (noopRegistry) ShutdownAll()   {}

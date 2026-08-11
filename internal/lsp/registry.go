package lsp

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/filetree"
)

// Manager is the real Registry: it lazily starts one server per configured
// language, keyed by file extension. Messages emitted before the Bubble Tea
// program exists are buffered until SetSink.
type Manager struct {
	cfg     config.Config
	root    string
	extLang map[string]string // ".go" → "go"

	// Notices reach the program through a single dispatcher goroutine: emit
	// only appends to queued and signals wake. The sink is program.Send, whose
	// channel is unbuffered and drained by the goroutine that runs Update — a
	// synchronous sink call from the UI goroutine (ClientFor fires there on
	// every burst boundary) would deadlock the editor.
	sinkMu sync.Mutex
	sink   func(any)
	queued []any
	wake   chan struct{}

	// rootCache memoizes FindProjectRoot per file directory (13 marker stats
	// per level, otherwise repeated on every doc-sync call). Lock-free; no
	// eviction — bounded by the set of directories visited in a session. A
	// marker file appearing mid-session yields a slightly-wide workspace
	// folder, which is benign (same class as the folders dedupe).
	rootCache sync.Map // dir → repoRoot

	mu       sync.Mutex
	clients  map[string]*serverClient
	disabled map[string]string // language → reason it is off ("" / absent = usable)
	restarts map[string]int    // rapid dead-server replacements per language (capped)
	override map[string]bool   // language → runtime enable overriding config (inspection mode)
}

// maxServerRestarts bounds how many times a language's crashed server is
// replaced before the language is disabled for the session — a restart on
// demand heals one-off crashes without letting a broken install crash-loop.
const maxServerRestarts = 3

// longLivedUptime is how long a server must have run for its crash to count as
// news rather than part of a loop: a corpse that lived at least this long
// resets the language's restart budget, so a stable server that dies once in a
// while restarts forever, while one dying seconds after spawn burns through
// maxServerRestarts and disables.
const longLivedUptime = time.Minute

func NewManager(cfg config.Config, root string) *Manager {
	// extLang covers ALL configured languages and stays immutable afterwards
	// (ClientFor/UnavailableReason read it lock-free). Config-disabled
	// languages are gated by a seeded disabled reason instead, which runtime
	// Enable can clear without touching the extension map.
	extLang := map[string]string{}
	disabled := map[string]string{}
	for lang, lc := range cfg.Languages {
		for _, ext := range lc.Extensions {
			extLang[strings.ToLower(ext)] = lang
		}
		if !lc.IsEnabled() {
			disabled[lang] = "disabled in config"
		}
	}
	// A bridge cycle (self-bridge, A↔B, or a chain reaching one) would make
	// the runtime mirror/companion relays recurse without bound — disable any
	// language whose bridge chain doesn't terminate, with a reason
	// UnavailableReason can surface.
	for lang := range cfg.Languages {
		if reason := bridgeCycleReason(cfg.Languages, lang); reason != "" && disabled[lang] == "" {
			disabled[lang] = reason
		}
	}
	m := &Manager{
		cfg:      cfg,
		root:     root,
		extLang:  extLang,
		wake:     make(chan struct{}, 1),
		clients:  map[string]*serverClient{},
		disabled: disabled,
		restarts: map[string]int{},
		override: map[string]bool{},
	}
	go m.dispatchNotices()
	return m
}

// bridgeCycleReason follows lang's bridge.To chain and reports a disable
// reason when the chain revisits a language (a cycle the runtime relays would
// recurse through forever) — "" when the chain terminates.
func bridgeCycleReason(langs map[string]config.LanguageConfig, lang string) string {
	seen := map[string]bool{lang: true}
	chain := []string{lang}
	cur := lang
	for {
		lc, ok := langs[cur]
		if !ok || lc.LSP == nil || lc.LSP.Bridge == nil {
			return ""
		}
		next := lc.LSP.Bridge.To
		chain = append(chain, next)
		if seen[next] {
			return "lsp bridge cycle (" + strings.Join(chain, " → ") + ") — disabled"
		}
		seen[next] = true
		cur = next
	}
}

// maxQueuedNotices bounds the undelivered notice buffer: notices are rare, so
// hitting the cap means the program stopped draining — dropping is better than
// growing without bound.
const maxQueuedNotices = 64

// SetSink connects the manager to the running program (program.Send); anything
// emitted during startup is flushed by the dispatcher once Run starts draining.
func (m *Manager) SetSink(sink func(any)) {
	m.sinkMu.Lock()
	m.sink = sink
	m.sinkMu.Unlock()
	m.wakeDispatcher()
}

// emit hands a message to the dispatcher goroutine. Never blocks, so it is
// safe from any goroutine — including the UI goroutine, where a direct
// program.Send would deadlock.
func (m *Manager) emit(msg any) {
	m.sinkMu.Lock()
	if len(m.queued) < maxQueuedNotices {
		m.queued = append(m.queued, msg)
	}
	m.sinkMu.Unlock()
	m.wakeDispatcher()
}

func (m *Manager) wakeDispatcher() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// dispatchNotices is the single delivery goroutine: it drains queued in order
// and calls the sink outside the lock. One goroutine per Manager, alive for
// the process — after the program exits, program.Send returns immediately, so
// late notices drain harmlessly.
func (m *Manager) dispatchNotices() {
	for range m.wake {
		for {
			m.sinkMu.Lock()
			if m.sink == nil || len(m.queued) == 0 {
				m.sinkMu.Unlock()
				break
			}
			sink := m.sink
			queued := m.queued
			m.queued = nil
			m.sinkMu.Unlock()
			for _, msg := range queued {
				sink(msg)
			}
		}
	}
}

// ClientFor resolves (lazily starting) the server for a file's language.
func (m *Manager) ClientFor(path string) (Client, bool) {
	lang, ok := m.extLang[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return nil, false
	}
	// Scope the server to the file's nearest project (tsconfig/package.json/
	// go.mod/…), not the whole opened tree — so a monorepo frontend loads its
	// ~300 files, not the repo's ~15k. One server per language: the first file
	// visited roots it; later projects are added as workspace folders.
	c, ok := m.getOrStart(lang, m.projectRootFor(path))
	if !ok {
		return nil, false
	}
	return c, true
}

// projectRootFor resolves (and memoizes per directory) the file's nearest
// project root. Resolved before m.mu is taken — the walk stats the disk.
func (m *Manager) projectRootFor(path string) string {
	dir := filepath.Dir(path)
	if v, ok := m.rootCache.Load(dir); ok {
		return v.(string)
	}
	root := filetree.FindProjectRoot(m.root, path)
	m.rootCache.Store(dir, root)
	return root
}

// getOrStart is getOrStartLocked plus the lock. Notices produced under m.mu
// are fine: emit never calls the sink, it only queues for the dispatcher.
func (m *Manager) getOrStart(lang, repoRoot string) (*serverClient, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getOrStartLocked(lang, repoRoot)
}

// UnavailableReason explains why ClientFor fails for path: an unmapped
// extension gets the install hint, a disabled language gets the reason it was
// disabled (binary missing, repeated crashes, …). "" when a client is (or could
// be) available.
func (m *Manager) UnavailableReason(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	lang, ok := m.extLang[ext]
	if !ok {
		if ext == "" {
			return "no language server for this file type"
		}
		return "no language server for " + ext + " files — try: ntee --prepare-lsp"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.disabled[lang]
}

// clientForLang returns the server for an explicit language (lazily starting
// it) with the file's repo as a workspace folder. Used by the hybrid bridge to
// reach the companion (e.g. typescript) server.
func (m *Manager) clientForLang(lang, file string) (*serverClient, bool) {
	return m.getOrStart(lang, m.projectRootFor(file))
}

// getOrStartLocked returns the (single) server for lang, starting it lazily and
// ensuring repoRoot is one of its workspace folders. Caller must hold m.mu.
func (m *Manager) getOrStartLocked(lang, repoRoot string) (*serverClient, bool) {
	if m.disabled[lang] != "" {
		return nil, false
	}
	if c, ok := m.clients[lang]; ok {
		if !c.isDead() {
			c.EnsureFolder(repoRoot)
			return c, true
		}
		// The server exited (crash): drop the corpse and start a replacement on
		// demand. A long-lived server's crash resets the budget (news, not a
		// loop); only rapid successive deaths burn through it and disable the
		// language.
		delete(m.clients, lang)
		if c.uptime() >= longLivedUptime {
			m.restarts[lang] = 0
		}
		m.restarts[lang]++
		if m.restarts[lang] >= maxServerRestarts {
			m.disabled[lang] = lang + " lsp crashed repeatedly — disabled for this session (restart ntee to retry)"
			m.emit(NoticeMsg{Text: m.disabled[lang]})
			return nil, false
		}
		m.emit(NoticeMsg{Text: "restarting " + lang + " lsp"})
	}
	lc, ok := m.cfg.Languages[lang]
	enabled := ok && (lc.IsEnabled() || m.override[lang])
	if !enabled || lc.LSP == nil || lc.LSP.Command == "" {
		m.disabled[lang] = "no language server configured for " + lang + " — try: ntee --prepare-lsp"
		return nil, false
	}
	if _, err := resolveBinary(lc.LSP.Command); err != nil {
		m.disabled[lang] = lc.LSP.Command + " not found — try: ntee --prepare-lsp"
		m.emit(NoticeMsg{Text: lc.LSP.Command + " not found — LSP disabled for " + lang})
		return nil, false
	}
	c := newServerClient(lang, *lc.LSP, repoRoot, m.emit)
	// Runtime belt for the NewManager cycle check: never wire a self-bridge,
	// and never hand a client itself as its companion — either would recurse
	// (mirror → DidChange → mirror …) without bound.
	bridged := lc.LSP.Bridge != nil && lc.LSP.Bridge.To != lang
	if bridged {
		to := lc.LSP.Bridge.To
		c.tsBridge = m.makeBridge(*lc.LSP.Bridge)
		c.mirror = m.makeMirror(*lc.LSP.Bridge)
		c.companionFor = func(file string) (*serverClient, bool) {
			ts, ok := m.clientForLang(to, file)
			if !ok || ts == c {
				return nil, false
			}
			return ts, true
		}
	}
	m.clients[lang] = c
	go c.start()
	if bridged {
		// Warm the companion (e.g. typescript) now so it is loading its project
		// in parallel — otherwise the first bridged request starts it cold and
		// times out. Best-effort; safe under m.mu (getOrStartLocked doesn't lock).
		m.getOrStartLocked(lc.LSP.Bridge.To, repoRoot)
	}
	return c, true
}

// makeBridge builds the relay for a hybrid server: forward its tsserver/request
// command to the companion server (bridge.To) via bridge.Command
// (workspace/executeCommand), returning the unwrapped tsserver body.
func (m *Manager) makeBridge(bridge config.BridgeConfig) tsBridgeFunc {
	return func(command string, args json.RawMessage) (json.RawMessage, error) {
		file := fileFromArgs(args)
		if file == "" {
			file = m.root
		}
		ts, ok := m.clientForLang(bridge.To, file)
		if !ok {
			return nil, fmt.Errorf("lsp bridge: %q server unavailable", bridge.To)
		}
		raw, err := ts.ExecuteCommand(bridge.Command, []any{command, args})
		if err != nil {
			return nil, err
		}
		return unwrapBody(raw), nil
	}
}

// makeMirror forwards a hybrid server's document sync to its companion so
// tsserver (with @vue/typescript-plugin) has the .vue file in a project and can
// answer _vue:* commands like projectInfo. Without this, the bridge relays but
// tsserver errors "file not in project" and Volar falls back to a limited
// inferred-project service (no cross-file/type-aware results).
func (m *Manager) makeMirror(bridge config.BridgeConfig) docMirrorFunc {
	return func(kind, path, content string) {
		ts, ok := m.clientForLang(bridge.To, path)
		if !ok {
			return
		}
		switch kind {
		case "open":
			ts.DidOpen(path, content)
		case "change":
			ts.DidChange(path, content, 0)
		case "save":
			ts.DidSave(path)
		case "close":
			ts.DidClose(path)
		}
	}
}

// fileFromArgs extracts the tsserver command's target file (if any) so the
// companion server can scope to that file's repo.
func fileFromArgs(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var probe struct {
		File string `json:"file"`
	}
	_ = json.Unmarshal(args, &probe)
	return probe.File
}

// unwrapBody returns the tsserver response body: executeCommand yields the
// tsserver envelope `{..., body}`, but Volar's tsserver/response handler
// consumes the body directly. Passes the value through if there is no body.
func unwrapBody(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		if body, ok := obj["body"]; ok {
			return body
		}
	}
	return raw
}

// Statuses reports every configured language's server state for the
// inspection pane. cfg.Languages is never mutated after construction, so
// ranging it here is safe; the runtime maps are read under m.mu (calling
// isDead under the lock is the established order — see getOrStartLocked).
func (m *Manager) Statuses() []LangStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]LangStatus, 0, len(m.cfg.Languages))
	for lang := range m.cfg.Languages {
		st := LangStatus{Lang: lang}
		switch {
		case m.disabled[lang] != "":
			st.State, st.Reason = LangDisabled, m.disabled[lang]
		case m.clients[lang] != nil && !m.clients[lang].isDead():
			st.State = LangRunning
		default:
			st.State = LangStopped
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Lang < out[j].Lang })
	return out
}

// Enable clears lang's disabled state (config or session) and eagerly starts
// its server rooted at the project, so the inspection pane turns green now
// rather than on the next file open. Sync-detectable failures (missing binary,
// no command) come back in reason; async crashes arrive as NoticeMsg.
func (m *Manager) Enable(lang string) (bool, string) {
	m.mu.Lock()
	delete(m.disabled, lang)
	m.restarts[lang] = 0
	m.override[lang] = true
	_, ok := m.getOrStartLocked(lang, m.root)
	reason := m.disabled[lang]
	m.mu.Unlock()
	if !ok {
		return false, reason
	}
	return true, ""
}

// Disable stops lang's server (if running) and marks the language disabled
// for the session. Safe when nothing is running.
func (m *Manager) Disable(lang string) {
	m.mu.Lock()
	c := m.clients[lang]
	delete(m.clients, lang)
	m.override[lang] = false
	m.disabled[lang] = "disabled in config"
	m.mu.Unlock()
	if c != nil {
		// The client is already detached and flagged disabled; the handshake is
		// best-effort cleanup, kept off the caller (a key handler) so a wedged
		// server can't freeze the UI.
		go c.stop()
	}
}

// shutdownAllTimeout bounds ShutdownAll as a whole: each stop() has its own
// grace periods, but quit must return even if a handshake wedges.
const shutdownAllTimeout = 2 * time.Second

// ShutdownAll stops every running server in parallel and returns once all
// handshakes finish or the shared deadline passes — stragglers are left to
// their kill timers and process exit.
func (m *Manager) ShutdownAll() {
	m.mu.Lock()
	clients := make([]*serverClient, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.clients = map[string]*serverClient{}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for _, c := range clients {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c.stop()
			}()
		}
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownAllTimeout):
	}
}

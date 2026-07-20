package lsp

import (
	"path/filepath"
	"strings"
	"sync"

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

	sinkMu sync.Mutex
	sink   func(any)
	queued []any

	mu       sync.Mutex
	clients  map[string]*serverClient
	disabled map[string]bool
}

func NewManager(cfg config.Config, root string) *Manager {
	extLang := map[string]string{}
	for lang, lc := range cfg.Languages {
		if !lc.IsEnabled() {
			continue // disabled language: its files route to no server
		}
		for _, ext := range lc.Extensions {
			extLang[strings.ToLower(ext)] = lang
		}
	}
	return &Manager{
		cfg:      cfg,
		root:     root,
		extLang:  extLang,
		clients:  map[string]*serverClient{},
		disabled: map[string]bool{},
	}
}

// SetSink connects the manager to the running program (program.Send) and
// flushes anything emitted during startup.
func (m *Manager) SetSink(sink func(any)) {
	m.sinkMu.Lock()
	m.sink = sink
	queued := m.queued
	m.queued = nil
	m.sinkMu.Unlock()
	for _, msg := range queued {
		sink(msg)
	}
}

func (m *Manager) emit(msg any) {
	m.sinkMu.Lock()
	sink := m.sink
	if sink == nil {
		if len(m.queued) < 64 {
			m.queued = append(m.queued, msg)
		}
		m.sinkMu.Unlock()
		return
	}
	m.sinkMu.Unlock()
	sink(msg)
}

// ClientFor resolves (lazily starting) the server for a file's language.
func (m *Manager) ClientFor(path string) (Client, bool) {
	lang, ok := m.extLang[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return nil, false
	}

	// Scope the server to the file's own git repo (not the whole opened tree),
	// so a multi-repo root like ~/workspace does not make the server index all
	// of it. One server per language: the first file visited roots it; later
	// repos are added as workspace folders.
	repoRoot := filetree.FindRepoRoot(m.root, path)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.disabled[lang] {
		return nil, false
	}
	if c, ok := m.clients[lang]; ok {
		c.EnsureFolder(repoRoot)
		return c, true
	}
	lc := m.cfg.Languages[lang]
	if lc.LSP == nil || lc.LSP.Command == "" {
		m.disabled[lang] = true
		return nil, false
	}
	if _, err := resolveBinary(lc.LSP.Command); err != nil {
		m.disabled[lang] = true
		m.emit(NoticeMsg{Text: lc.LSP.Command + " not found — LSP disabled for " + lang})
		return nil, false
	}
	c := newServerClient(lang, *lc.LSP, repoRoot, m.emit)
	m.clients[lang] = c
	go c.start()
	return c, true
}

func (m *Manager) ShutdownAll() {
	m.mu.Lock()
	clients := make([]*serverClient, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.clients = map[string]*serverClient{}
	m.mu.Unlock()
	for _, c := range clients {
		c.stop()
	}
}

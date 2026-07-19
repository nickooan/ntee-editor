// Package app is the Bubble Tea model: one Model struct, mode-based key
// dispatch, and lipgloss rendering. The Model is passed by value; handlers
// return the updated copy.
package app

import (
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/clipboard"
	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/fuzzy"
	"github.com/nickooan/ntee-editor/internal/lsp"
	"github.com/nickooan/ntee-editor/internal/store"
	"github.com/nickooan/ntee-editor/internal/syntax"
	"github.com/nickooan/ntee-editor/internal/view"
)

type mode int

const (
	modeQuery mode = iota
	modeEdit
	modeSearch
	modeCommand
	modeExec // "@exec >" editor-command bar (Ctrl+E from edit mode)
)

type Model struct {
	cfg  config.Config
	db   store.Backend
	lsp  lsp.Registry
	root string // absolute project root

	// copyClipboard writes to the system clipboard; injectable so tests can
	// observe copies without touching the real clipboard.
	copyClipboard func(string) error

	// gitignore matches the project's .gitignore; matched sidebar entries render
	// gray. nil when the project has no .gitignore.
	gitignore *filetree.Gitignore

	width, height int
	ready         bool
	mode          mode

	notice         string // transient status note, cleared on the next keypress
	errText        string // transient error, cleared on the next keypress
	messageOverlay string // dismissible centered message (e.g. binary file)

	// Query input bar (home mode). Three-way state split, ported from
	// r1quest: `command` is the editable typed text; `selectedCommand` is the
	// CONFIRMED selection (set on Enter / parent-dir) and is the only thing
	// that drives directory EXPANSION; `keyboardSelectedCommand` is the
	// sidebar HIGHLIGHT (moved by Shift+arrows / popup nav) and never
	// expands. `commandPreview` is a display-only reflection of the
	// navigated entry in the bar — typing adopts it into `command`.
	command                 string
	qCursor                 int
	commandPreview          string
	selectedCommand         string
	keyboardSelectedCommand string
	inputSuggestIndex       int

	// Open file. openRel is the root-relative path — the store key.
	openFile    *filetree.OpenViewFile
	openRel     string
	fileScrollX int
	fileScrollY int
	fileLines   []string // view-mode line cache (rebuilt by refreshFileHighlights)

	edit editor

	// Undo timeline: snapshot seqs only; content lives in the store.
	undoSeqs   []int64
	undoCursor int
	nextSeq    int64
	snapDirty  bool // edits since the last snapshot

	// In-file search. searchContent is frozen at enterSearch time; searchHl is
	// its syntax highlighting (nil → plain), tokenized fresh on entry because
	// the edit-mode hlLines cache may hold nil rows between burst rescans.
	searchPrevMode mode
	searchContent  string
	searchInput    string
	searchFocused  int
	searchHl       [][]view.HighlightSegment

	// Jump trail (Ctrl+J/Ctrl+O in edit mode): origin frames to return to.
	// Lives only within one continuous edit session.
	jumpStack []jumpFrame

	// Bottom command bar (: commands).
	cmdInput    string
	cmdCursor   int
	cmdPrevMode mode

	// Bottom "@exec >" editor-command bar (Ctrl+E). Only entered from edit
	// mode; the editor is paused (m.edit untouched) so its selection stays
	// visible behind the bar.
	execInput    string
	execCursor   int
	execPrevMode mode

	// Fuzzy file finder overlay (Ctrl+P).
	fuzzyOpen    bool
	fuzzyQuery   string
	fuzzyIndex   int
	fuzzyCorpus  []string
	fuzzyMatches []fuzzy.Match

	// Per-line highlight cache. A nil row renders plain; rows are spliced on
	// line insert/join so indices stay aligned between full rescans.
	hlLines [][]view.HighlightSegment
	hlRev   int
	hlPath  string

	// LSP diagnostics, keyed by root-relative path.
	diags map[string][]lsp.Diagnostic

	// Definition/reference picker (Ctrl+J with multiple hits). The preview
	// caches the selected candidate's file (re-read on file change only).
	defPickOpen      bool
	defPickTitle     string
	defPickToken     string
	defPickItems     []defCandidate
	defPickIndex     int
	defPickPrevRel   string
	defPickPrevLines []string
	defPickPrevHl    [][]view.HighlightSegment

	// Repo-wide content search overlay (Ctrl+G).
	grepOpen    bool
	grepQuery   string
	grepIndex   int
	grepResults []grepHit
	grepFiles   []grepFile
	grepHlRel   string
	grepHl      [][]view.HighlightSegment
}

func New(cfg config.Config, db store.Backend, root, notice string, reg lsp.Registry) Model {
	syntax.SetStyle(cfg.Theme.Syntax)
	if reg == nil {
		reg = lsp.NewNoopRegistry()
	}
	m := Model{
		cfg:           cfg,
		db:            db,
		lsp:           reg,
		root:          root,
		notice:        notice,
		mode:          modeQuery,
		diags:         map[string][]lsp.Diagnostic{},
		copyClipboard: clipboard.Copy,
		gitignore:     filetree.LoadGitignore(root),
	}
	if sess, ok := db.LoadSession(); ok {
		m.selectedCommand = sess.Command
		if sess.LastFile != "" {
			m = m.openFileAt(sess.LastFile)
			m.mode = modeQuery // start on the query bar, last file visible
		}
	}
	return m
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		return m, nil

	case lsp.DiagnosticsMsg:
		if rel, err := filepath.Rel(m.root, msg.Path); err == nil {
			rel = filepath.ToSlash(rel)
			if len(msg.Items) == 0 {
				delete(m.diags, rel)
			} else {
				m.diags[rel] = msg.Items
			}
		}
		return m, nil

	case lsp.NoticeMsg:
		m.notice = msg.Text
		return m, nil

	case definitionMsg:
		return m.handleDefinition(msg)

	case referencesMsg:
		return m.handleReferences(msg)

	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC || msg.Type == tea.KeyCtrlQ {
			return m.quit()
		}
		m.notice = ""
		m.errText = ""

		if m.messageOverlay != "" {
			if msg.Type == tea.KeyEnter || msg.Type == tea.KeyEsc {
				m.messageOverlay = ""
			}
			return m, nil
		}
		if m.fuzzyOpen {
			return m.handleFuzzyKey(msg)
		}
		if m.defPickOpen {
			return m.handleDefPickKey(msg)
		}
		if m.grepOpen {
			return m.handleGrepKey(msg)
		}
		if msg.Type == tea.KeyCtrlP && m.mode != modeCommand && m.mode != modeSearch && m.mode != modeExec {
			return m.openFuzzy(), nil
		}
		if msg.Type == tea.KeyCtrlG && m.mode != modeCommand && m.mode != modeSearch && m.mode != modeExec {
			return m.openGrep(), nil
		}

		switch m.mode {
		case modeQuery:
			return m.handleQueryKey(msg)
		case modeEdit:
			return m.handleEditKey(msg)
		case modeSearch:
			return m.handleSearchKey(msg)
		case modeCommand:
			return m.handleCommandKey(msg)
		case modeExec:
			return m.handleExecKey(msg)
		}
	}
	return m, nil
}

func (m Model) quit() (tea.Model, tea.Cmd) {
	m.saveSession()
	m.lsp.ShutdownAll()
	return m, tea.Quit
}

func (m Model) saveSession() {
	_ = m.db.SaveSession(store.Session{
		LastFile: m.openRel,
		Command:  m.selectedCommand,
	})
}

// treeEntries builds the sidebar: expansion is a pure function of the path
// driving the sidebar (typed input, else the confirmed selection).
func (m Model) treeEntries() []filetree.FileTreeEntry {
	return filetree.BuildFileTreeEntries(
		m.root,
		filetree.BuildExpandedDirectoryPaths(m.sidebarCommand()),
		m.cfg.Tree.Ignore,
		m.gitignore,
	)
}

// sidebarCommand is the path that drives directory EXPANSION.
func (m Model) sidebarCommand() string {
	return filetree.ResolveSidebarCommand(m.command, m.selectedCommand)
}

// highlightedSidebarCommand is the path that drives the sidebar HIGHLIGHT:
// keyboard/popup navigation wins over the typed path.
func (m Model) highlightedSidebarCommand() string {
	if m.keyboardSelectedCommand != "" {
		return m.keyboardSelectedCommand
	}
	return m.sidebarCommand()
}

func (m Model) highlightedEntryIndex(entries []filetree.FileTreeEntry) int {
	return filetree.ResolveHighlightedEntry(entries, m.highlightedSidebarCommand())
}

// openFileAt loads a root-relative path straight into an edit session.
func (m Model) openFileAt(rel string) Model {
	f, ok := filetree.ReadViewFile(m.root, rel)
	if !ok {
		m.errText = "cannot open " + rel
		return m
	}
	if f.Binary {
		m.messageOverlay = f.FileName + " looks like a binary file."
		return m
	}
	if m.openFile != nil && m.openFile.Path != f.Path {
		if client, ok := m.lsp.ClientFor(m.openFile.Path); ok {
			client.DidClose(m.openFile.Path)
		}
	}
	m.openFile = &f
	m.openRel = rel
	m.fileScrollX, m.fileScrollY = 0, 0
	m.selectedCommand = rel // sidebar keeps tracking the open file
	m.jumpStack = nil       // a deliberate open starts a fresh navigation trail
	_ = m.db.TouchOpened(store.OpenedFile{Path: rel, LastOpenedAt: time.Now().UnixMilli()})
	if client, ok := m.lsp.ClientFor(f.Path); ok {
		client.DidOpen(f.Path, f.Content)
	}
	m = m.beginEditSession(f.Content)
	m.mode = modeEdit
	return m
}

// refreshFileHighlights rebuilds the line and highlight caches from the
// current buffer (edit mode) or the opened file. Whole-buffer tokenization —
// chroma is stateful across lines — so this runs at burst boundaries and file
// events, never per keystroke.
func (m Model) refreshFileHighlights() Model {
	if m.openFile == nil {
		m.fileLines, m.hlLines = nil, nil
		return m
	}
	content := m.openFile.Content
	if m.mode == modeEdit {
		content = m.edit.content()
	}
	m.fileLines = view.NormalizeLines(content)
	m.hlRev = m.edit.rev
	m.hlPath = m.openRel
	if kb := m.cfg.Editor.MaxHighlightKB; kb > 0 && len(content) > kb*1024 {
		m.hlLines = nil // too big: render plain
		return m
	}
	m.hlLines = syntax.HighlightLines(m.openFile.FileName, content)
	return m
}

// hlMarkLine invalidates one cached highlight row (renders plain until the
// next full rescan).
func (m Model) hlMarkLine(i int) Model {
	if i >= 0 && i < len(m.hlLines) {
		m.hlLines[i] = nil
	}
	return m
}

// hlInsertLine splices a plain row at i so cached rows below a new line keep
// their indices until the next full rescan.
func (m Model) hlInsertLine(i int) Model {
	if m.hlLines == nil || i < 0 || i > len(m.hlLines) {
		return m
	}
	m.hlLines = append(m.hlLines[:i], append([][]view.HighlightSegment{nil}, m.hlLines[i:]...)...)
	return m
}

// hlRemoveLine drops row i after a line join.
func (m Model) hlRemoveLine(i int) Model {
	if m.hlLines == nil || i < 0 || i >= len(m.hlLines) {
		return m
	}
	m.hlLines = append(m.hlLines[:i], m.hlLines[i+1:]...)
	return m
}

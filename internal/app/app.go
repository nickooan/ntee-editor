// Package app is the Bubble Tea model: one Model struct, mode-based key
// dispatch, and lipgloss rendering. The Model is passed by value; handlers
// return the updated copy.
package app

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/clipboard"
	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/fuzzy"
	"github.com/nickooan/ntee-editor/internal/input"
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
	modeExec       // "@exec >" editor-command bar (Ctrl+E from edit mode)
	modeSearchExec // "@search … >" replace-command bar (Ctrl+E from search mode)
	modeInspect    // "@inspection >" dashboard (Ctrl+T): store stats + lsp control
)

// inBarMode reports whether keystrokes are feeding a text-input bar, where
// global chords (Ctrl+P/U/G, Shift+Tab) must not fire.
func (m Model) inBarMode() bool {
	return m.mode == modeCommand || m.mode == modeSearch || m.mode == modeExec ||
		m.mode == modeSearchExec || m.mode == modeInspect
}

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

	// Git working-tree status for the sidebar: gitDirty holds every uncommitted
	// path plus its ancestor dirs (rendered yellow, folded dirs included). It is
	// refreshed by a background poll (gitStatusTickMsg) so changes made by
	// external processes — another terminal, an agent, a build — surface without
	// any editor input. gitRepo gates the whole feature at startup.
	gitRepo          bool
	gitDirty         map[string]bool
	gitStatusRunning bool

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

	// Open tabs (persisted): rels in display order, plus which is active.
	// draftSet caches which rels have a stashed unsaved draft (inactive tabs
	// render red from it; the active tab's redness comes from edit.dirty).
	tabs      []string
	tabActive int
	draftSet  map[string]bool
	cursorMem map[string]store.TabCursor // per-tab last cursor, restored on revisit

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

	// Search-exec command bar (Ctrl+E from search mode): "c <text>" replaces the
	// focused match's span, "mlc <text>" replaces every match. Always returns to
	// modeSearch; searchPrevMode stays untouched for the eventual search exit.
	searchExecInput  string
	searchExecCursor int

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
	// Inline suggestions for the bar's trailing token (execSuggestions);
	// execSugIndex is the ↑/↓-cycled candidate that Tab accepts.
	execSugs     []string
	execSugIndex int

	// Inspection dashboard (Ctrl+T): left menu (ntee-db / lsp), right info
	// pane, "@inspection >" command bar. Store stats are fetched async on
	// entry and after maintenance ops (BlobUsage does I/O).
	inspectPrevMode mode
	inspectMenu     int // 0 = ntee-db, 1 = lsp
	inspectInput    string
	inspectCursor   int
	inspectInfo     store.DBInfo
	inspectInfoErr  error // store.ErrNoStats → in-memory fallback text
	inspectLoading  bool  // stats fetch in flight
	inspectBusy     string // "" | "compact" | "relieve" — blocks duplicate runs

	// Fuzzy file finder overlay: Ctrl+P (whole project) and Ctrl+U (uncommitted
	// files only) share it; fuzzyPrompt labels which source is showing.
	fuzzyOpen    bool
	fuzzyQuery   string
	fuzzyIndex   int
	fuzzyPrompt  string           // overlay title: "goto " (Ctrl+P) or "uncommitted " (Ctrl+U)
	fuzzyCorpus  []fuzzy.Prepared // candidates with matching data precomputed once per open
	fuzzyMatches []fuzzy.Match

	// Search corpus: the full project file walk (BuildAllEntries), shared by the
	// query bar, the Ctrl+P finder, and Ctrl+G grep. Built once and reused —
	// walking it per keystroke is what made large repos lag. Kept fresh against
	// external changes by a background rebuild (see ensureCorpus/rebuildCorpusCmd).
	// corpusBuiltAt zero means "never built" (cold cache).
	corpus           []string
	dirCorpus        []string // "/"-suffixed rel dirs from the same walk (dirMtimes keys)
	corpusBuiltAt    time.Time
	corpusRebuilding bool
	corpusTruncated  bool // the walk hit Tree.MaxIndexFiles — index is partial

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

	// LSP autocomplete popup (edit mode). completionAll is the server's raw
	// list; completionItems is it filtered by the identifier prefix under the
	// cursor and sorted. completionStart is that identifier's start rune-column.
	completionOpen      bool
	completionAll       []lsp.CompletionItem
	completionItems     []lsp.CompletionItem
	completionIndex     int
	completionStart     int
	completionPending   bool // a request is in flight
	completionDismissed bool // Esc'd — suppress auto-reopen until a word boundary

	// Repo-wide content search overlay (Ctrl+G). All heavy work is async: the
	// snapshot loads via grepLoadedMsg (guarded by grepGen), searches are
	// debounced via grepTickMsg and land via grepResultsMsg (guarded by
	// grepSearchGen). grepResultsGen == grepSearchGen means the displayed
	// results are current.
	grepOpen       bool
	grepQuery      string
	grepIndex      int
	grepResults    []grepHit
	grepFiles      []grepFile // streams in per grepBatchMsg; released on close
	grepLoading    bool       // snapshot batches still arriving
	grepLoadBytes  int        // bytes loaded so far, for the maxGrepBytes cap
	grepGen        int        // bumped per openGrep; drops stale loads
	grepSearchGen  int        // bumped per query change; tags ticks + results
	grepResultsGen int        // grepSearchGen of the displayed grepResults
	grepPrevLines  []string   // selected file's lines, derived on demand
	grepHlRel      string
	grepHl         [][]view.HighlightSegment
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
		gitRepo:       filetree.IsGitRepo(root),
		draftSet:      map[string]bool{},
		cursorMem:     map[string]store.TabCursor{},
	}
	lastFile := ""
	if sess, ok := db.LoadSession(); ok {
		m.selectedCommand = sess.Command
		lastFile = sess.LastFile
	}
	if t, ok := db.LoadTabs(); ok && len(t.Paths) > 0 {
		// Tabs win over the legacy single LastFile. Activating the tab opens it
		// and restores its draft, so unsaved work survives a relaunch.
		m.tabs = t.Paths
		for rel, c := range t.Cursors {
			m.cursorMem[rel] = c
		}
		for _, rel := range m.tabs {
			if _, ok := db.LoadDraft(rel); ok {
				m.draftSet[rel] = true
			}
		}
		m = m.activateTab(input.Clamp(t.Active, 0, len(m.tabs)-1))
		m.mode = modeQuery // start on the query bar, active file visible
	} else if lastFile != "" {
		m = m.openFileAt(lastFile)
		m.mode = modeQuery
	}

	// Warm start: reuse the persisted corpus if its directory signature still
	// matches the tree (a cheap stat-sweep), skipping the full walk entirely.
	if idx, ok := db.LoadCorpus(); ok && idx.Version == store.CorpusVersion && m.signatureValid(idx) {
		m.corpus = idx.Files
		m.dirCorpus = filetree.DirsFromMtimes(idx.DirMtimes)
		m.corpusTruncated = idx.Truncated
		m.corpusBuiltAt = time.Now()
	}
	return m
}

// Init warms the search corpus in the background when there is no valid
// persisted index (a warm start skips the walk) and, in a git repo, kicks off
// the git-status poll loop.
func (m Model) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.corpusBuiltAt.IsZero() {
		cmds = append(cmds, m.rebuildCorpusCmd())
	}
	if m.gitRepo {
		cmds = append(cmds, m.refreshGitStatusCmd(), gitStatusTick())
	}
	return tea.Batch(cmds...)
}

// gitStatusInterval paces the background `git status` poll. Polling (rather
// than an fs watcher) is what surfaces changes made by external processes —
// another terminal, an agent editing files, a build — while the editor idles.
const gitStatusInterval = 3 * time.Second

// gitStatusTickMsg drives the poll loop; gitStatusMsg delivers a fresh dirty
// set from the worker goroutine.
type gitStatusTickMsg struct{}

type gitStatusMsg struct {
	dirty map[string]bool
	ok    bool
}

func gitStatusTick() tea.Cmd {
	return tea.Tick(gitStatusInterval, func(time.Time) tea.Msg { return gitStatusTickMsg{} })
}

// refreshGitStatusCmd runs the `git status` child process off the UI goroutine
// and delivers the parsed dirty set. The process is short-lived — one spawn per
// call, no daemon.
func (m Model) refreshGitStatusCmd() tea.Cmd {
	root := m.root
	return func() tea.Msg {
		dirty, ok := filetree.GitDirtySet(root)
		return gitStatusMsg{dirty: dirty, ok: ok}
	}
}

// signatureValid stat-sweeps a persisted index's directory-mtime map against
// the current tree. It returns false on the first missing or changed directory
// — any external add/remove/rename bumps the containing directory's mtime — so a
// true result means the cached file list is still accurate. O(#dirs) stats,
// far cheaper than the full walk's per-entry gitignore regex.
func (m Model) signatureValid(idx store.CorpusIndex) bool {
	if len(idx.DirMtimes) == 0 {
		return false
	}
	for dir, mtime := range idx.DirMtimes {
		info, err := os.Stat(filepath.Join(m.root, dir))
		if err != nil || !info.IsDir() || info.ModTime().UnixNano() != mtime {
			return false
		}
	}
	return true
}

// corpusTTL bounds how stale the cached corpus may be before a use triggers a
// background rebuild. External file/dir changes surface within this window.
const corpusTTL = 2 * time.Second

// corpusMsg delivers a freshly walked corpus (reloaded .gitignore, directory
// signature, and truncation flag) from the background rebuild goroutine.
type corpusMsg struct {
	files     []string
	gi        *filetree.Gitignore
	dirMtimes map[string]int64
	truncated bool
	builtAt   time.Time
}

// ensureCorpus guarantees m.corpus is populated. On a cold cache it builds once
// synchronously so the first query/fuzzy has data; on a warm cache it is an
// O(1) read. When the cache is older than corpusTTL it fires a background
// rebuild (off the UI goroutine) and returns its Cmd, so keystrokes are never
// blocked on a walk while the list refreshes. (Persistence happens only in the
// corpusMsg handler — the in-flight Init/TTL rebuild reconciles and stores.)
func (m Model) ensureCorpus() (Model, tea.Cmd) {
	if m.corpusBuiltAt.IsZero() {
		files, dirMtimes, truncated := filetree.BuildAllEntries(m.root, m.cfg.Tree.Ignore, m.gitignore, m.cfg.Tree.MaxIndexFiles)
		m.corpus = files
		m.dirCorpus = filetree.DirsFromMtimes(dirMtimes)
		m.corpusTruncated = truncated
		m.corpusBuiltAt = time.Now()
		if truncated {
			m.notice = truncatedNotice(m.cfg.Tree.MaxIndexFiles)
		}
		return m, nil
	}
	if !m.corpusRebuilding && time.Since(m.corpusBuiltAt) > corpusTTL {
		m.corpusRebuilding = true
		return m, m.rebuildCorpusCmd()
	}
	return m, nil
}

// rebuildCorpusCmd walks the project off the UI goroutine and delivers a fresh
// corpus via corpusMsg. .gitignore is reloaded so external edits to it re-filter
// the corpus. Safe to run concurrently: dirCache is mutex-guarded and the
// captured root/ignore are read-only.
func (m Model) rebuildCorpusCmd() tea.Cmd {
	root := m.root
	ignore := m.cfg.Tree.Ignore
	maxFiles := m.cfg.Tree.MaxIndexFiles
	return func() tea.Msg {
		gi := filetree.LoadGitignore(root)
		files, dirMtimes, truncated := filetree.BuildAllEntries(root, ignore, gi, maxFiles)
		return corpusMsg{
			files:     files,
			gi:        gi,
			dirMtimes: dirMtimes,
			truncated: truncated,
			builtAt:   time.Now(),
		}
	}
}

// truncatedNotice tells the user the index is partial and how to widen it.
func truncatedNotice(cap int) string {
	return fmt.Sprintf("search index capped at %d files — add ignores (tree.ignore) or open a subdirectory", cap)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		wasReady := m.ready
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		if !wasReady && m.openFile != nil {
			// The startup-restored cursor was anchored with height 0; redo it
			// now that the real pane size is known.
			m = m.anchorCursorLine()
		}
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

	case inspectStatsMsg:
		m.inspectLoading = false
		m.inspectInfo, m.inspectInfoErr = msg.info, msg.err
		return m, nil

	case inspectMaintMsg:
		// Landing after Esc is harmless: only cached fields and the transient
		// notice are touched (same contract as lsp.NoticeMsg).
		m.inspectBusy = ""
		if msg.err != nil {
			m.errText = "db " + msg.op + ": " + msg.err.Error()
			return m, nil
		}
		m.notice = "db " + msg.op + " done"
		m.inspectLoading = true
		return m, m.fetchDBInfoCmd() // re-fetch so the pane shows the shrink

	case corpusMsg:
		// A background rebuild landed: swap in the fresh corpus and the reloaded
		// .gitignore (keeps the sidebar's graying consistent with the corpus),
		// then persist it so the next launch is a warm start. Persisting here (on
		// the main goroutine) keeps all DB writes off the rebuild goroutine.
		m.corpus = msg.files
		m.dirCorpus = filetree.DirsFromMtimes(msg.dirMtimes)
		m.gitignore = msg.gi
		m.corpusBuiltAt = msg.builtAt
		m.corpusRebuilding = false
		m.corpusTruncated = msg.truncated
		_ = m.db.SaveCorpus(store.CorpusIndex{
			Version:   store.CorpusVersion,
			Files:     msg.files,
			DirMtimes: msg.dirMtimes,
			Truncated: msg.truncated,
		})
		if msg.truncated {
			m.notice = truncatedNotice(m.cfg.Tree.MaxIndexFiles)
		}
		return m, nil

	case gitStatusTickMsg:
		// Poll heartbeat: refresh unless one is already in flight, and always
		// re-arm the next tick so the loop survives a skipped round.
		if m.gitStatusRunning {
			return m, gitStatusTick()
		}
		m.gitStatusRunning = true
		return m, tea.Batch(m.refreshGitStatusCmd(), gitStatusTick())

	case gitStatusMsg:
		m.gitStatusRunning = false
		if msg.ok {
			m.gitDirty = msg.dirty
		}
		return m, nil

	case grepBatchMsg:
		return m.handleGrepBatch(msg)

	case grepTickMsg:
		return m.handleGrepTick(msg)

	case grepResultsMsg:
		return m.handleGrepResults(msg)

	case definitionMsg:
		return m.handleDefinition(msg)

	case referencesMsg:
		return m.handleReferences(msg)

	case completionMsg:
		return m.handleCompletion(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)

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
		if msg.Type == tea.KeyCtrlP && !m.inBarMode() {
			return m.openFuzzy()
		}
		if msg.Type == tea.KeyCtrlU && !m.inBarMode() {
			return m.openUncommitted()
		}
		if msg.Type == tea.KeyCtrlG && !m.inBarMode() {
			return m.openGrep()
		}
		if msg.Type == tea.KeyCtrlT && !m.inBarMode() {
			return m.enterInspect()
		}
		if msg.Type == tea.KeyShiftTab && !m.inBarMode() {
			return m.cycleTab(), nil
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
		case modeSearchExec:
			return m.handleSearchExecKey(msg)
		case modeInspect:
			return m.handleInspectKey(msg)
		}
	}
	return m, nil
}

func (m Model) quit() (tea.Model, tea.Cmd) {
	m = m.recordCursor()      // persist the active file's cursor for next launch
	m = m.stashDraftIfDirty() // unsaved edits survive a relaunch
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
		m.gitDirty,
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
	// Stat-first (like openJumpFile): a missing target errors cleanly instead
	// of opening an error buffer — and must never become a tab.
	if _, err := os.Stat(f.Path); err != nil {
		m.errText = "cannot open " + rel
		return m
	}
	if f.Binary {
		m.messageOverlay = f.FileName + " looks like a binary file."
		return m
	}
	m = m.recordCursor()      // remember where we were in the file being left
	m = m.stashDraftIfDirty() // the old buffer's unsaved edits become a draft
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
	if d, ok := m.db.LoadDraft(rel); ok {
		m = m.restoreDraft(d)
	}
	m = m.addTab(rel)
	// Restore the remembered cursor and anchor its line ~30% from the top.
	if p, ok := m.cursorMem[rel]; ok {
		m.edit.cy = input.Clamp(p.Cy, 0, len(m.edit.lines)-1)
		m.edit.cx = p.Cx
		m.edit.clampCursor()
		m = m.anchorCursorLine()
	}
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
	// Search modes always sit on top of a live edit session, so the buffer —
	// not the on-disk snapshot — is the truth there too.
	if m.mode == modeEdit || m.mode == modeSearch || m.mode == modeSearchExec {
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

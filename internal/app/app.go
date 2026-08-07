// Package app is the Bubble Tea model: one Model struct, mode-based key
// dispatch, and lipgloss rendering. The Model is passed by value; handlers
// return the updated copy.
package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/clipboard"
	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/fuzzy"
	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/lsp"
	"github.com/nickooan/ntee-editor/internal/openapi"
	"github.com/nickooan/ntee-editor/internal/store"
	"github.com/nickooan/ntee-editor/internal/syntax"
	"github.com/nickooan/ntee-editor/internal/view"
)

// Version is the release version, printed by --version and on the splash.
// Release builds inject the git tag via
// -ldflags "-X github.com/nickooan/ntee-editor/internal/app.Version=…";
// "dev" marks a plain `go build` / `go install`.
var Version = "dev"

// versionTag renders Version for chrome: "v0.3.1" for releases, "dev" as-is.
func versionTag() string {
	if Version == "dev" {
		return Version
	}
	return "v" + Version
}

type mode int

const (
	modeQuery mode = iota
	modeEdit
	modeSearch
	modeCommand
	modeExec       // "@exec >" editor-command bar (Ctrl+E from edit mode)
	modeSearchExec // "@search … >" replace-command bar (Ctrl+E from search mode)
	modeInspect    // "@inspection >" dashboard (Ctrl+T): store stats + lsp control
	modeDiff       // read-only git-diff review of the open file ("git diff" in @exec)
	modeConflict   // interactive conflict resolution over the live buffer ("git scf" in @exec)
	modeOpenAPI    // read-only OpenAPI v3 preview of the open spec file ("openapi" in @exec)
	modeBlame      // read-only git-blame annotation of the open file ("git blame" in @exec)
)

// inBarMode reports whether keystrokes are feeding a text-input bar, where
// global chords (Ctrl+P/U/G, Shift+Tab) must not fire.
func (m Model) inBarMode() bool {
	return m.mode == modeCommand || m.mode == modeSearch || m.mode == modeExec ||
		m.mode == modeSearchExec || m.mode == modeInspect ||
		(m.mode == modeOpenAPI && m.openapiSearching)
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

	// Diff review mode ("git diff [hash]" in the @exec bar): a read-only
	// unified view of the edit buffer vs a git base. diffRows is the display
	// model (ctx/add rows index into edit.lines; del rows carry the removed
	// text); it is computed once per entry by an async computeDiffCmd and
	// guarded against staleness by diffGen. diffPending* carry a Ctrl+O
	// restore position, applied when the recomputed diff lands.
	diffRows          []diffRow
	diffCursor        int // index into diffRows
	diffCx            int // rune column within the cursor row (Ctrl+J targeting)
	diffScrollY       int
	diffBase          string // "" = HEAD; else the user-typed revision
	diffNewFile       bool   // base has no such path: the whole file is added
	diffAdds          int
	diffDels          int
	diffLoading       bool
	diffGen           int
	diffPendingCursor int
	diffPendingScroll int
	diffHasPending    bool

	// Blame mode ("git blame" in the @exec bar): a read-only view of the edit
	// buffer with a per-line author+date gutter (no line numbers). blameRows
	// map 1:1 to buffer lines; computed once per entry by an async
	// computeBlameCmd and guarded against staleness by blameGen.
	// blamePending* carry a Ctrl+O restore position.
	blameRows          []blameRow
	blameCursor        int // index into blameRows == buffer line
	blameCx            int // rune column within the cursor row (Ctrl+J targeting)
	blameScrollY       int
	blameAuthorW       int  // author column width: min(longest author, blameAuthorCap)
	blameNewFile       bool // untracked path or empty repo: everything uncommitted
	blameLoading       bool
	blameGen           int
	blamePendingCursor int
	blamePendingScroll int
	blameHasPending    bool

	// OpenAPI preview mode ("openapi" in the @exec bar): a read-only rendered
	// view of the open spec file, built once per entry by renderOpenAPICmd
	// (guarded by openapiGen). openapiCursor is the highlighted document row —
	// the Esc source-jump target; every rendered row carries a {file, line}
	// anchor back into the YAML, including rows from cross-file $refs.
	openapiLines     []openapi.Line
	openapiOutline   []openapi.OutlineEntry
	openapiPlain     []string // per-row plain text (search + match overlay)
	openapiCorpus    string   // plain rows joined with \n (the search corpus)
	openapiScrollY   int
	openapiCursor    int
	openapiSel       int // outline selection index
	openapiSearching bool
	openapiSearch    string
	openapiFocused   int // focused match index
	openapiLoading   bool
	openapiGen       int
	openapiTitle     string // "Petstore API  v1.0.0" for the status bar

	// Conflict-solving mode ("git scf" in the @exec bar): browses and mutates
	// the LIVE edit buffer, so cursor and scroll are the edit session's own
	// (m.edit.cy/cx, m.fileScrollY) — Esc needs no position mapping.
	// conflictBlocks is re-parsed from the buffer after every apply.
	conflictBlocks    []conflictBlock
	conflictChoice    int // popup option: 0 ours, 1 theirs, 2 both
	conflictChoiceIdx int // block index the choice belongs to; -1 = none

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

	// Inspection dashboard (Ctrl+T): left menu (ntee-db / lsp / system),
	// right info pane, "@inspection >" command bar. Store stats are fetched
	// async on entry and after maintenance ops (BlobUsage does I/O).
	inspectPrevMode mode
	inspectMenu     int // indexes inspectMenuItems
	inspectInput    string
	inspectCursor   int
	inspectInfo     store.DBInfo
	inspectInfoErr  error  // store.ErrNoStats → in-memory fallback text
	inspectLoading  bool   // stats fetch in flight
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
	corpusTruncated  bool               // the walk hit Tree.MaxIndexFiles — index is partial
	pendingValidate  *store.CorpusIndex // warm-adopted index awaiting a background signature check

	// Opening splash: shown on a cold-cache start while the index builds in
	// the background. Any key skips it; a minimum display avoids a flash.
	splash      bool
	splashStart time.Time
	splashFrame int

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

	// Pinned signature row: typing "(" after a known function keeps a one-row
	// overlay (label + signature) visible while the arguments are typed.
	// sigDepth counts parens opened since the pin — the pin drops when the
	// matching ")" lands. sigLine/sigCol locate the opening "(" (leaving them
	// unpins); sigAnchor is the function name's start column (render anchor).
	// sigLastAccepted remembers the just-accepted completion so a "(" typed
	// right after accepting (the popup already closed) can still pin it.
	sigPinned       *lsp.CompletionItem
	sigDepth        int
	sigLine, sigCol int
	sigAnchor       int
	sigLastAccepted *lsp.CompletionItem

	// Repo-wide content search overlay (Ctrl+G). All heavy work is async: the
	// snapshot loads via grepLoadedMsg (guarded by grepGen), searches are
	// debounced via grepTickMsg and land via grepResultsMsg (guarded by
	// grepSearchGen). grepResultsGen == grepSearchGen means the displayed
	// results are current.
	grepOpen       bool
	grepQuery      string
	grepCursor     int // rune offset into grepQuery
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

	// Warm start: adopt the persisted corpus optimistically — no stat sweep on
	// the startup path, so the first frame paints immediately even on a huge
	// tree. Init re-checks the signature in the background and rebuilds on a
	// mismatch (the 2s-TTL refresh already tolerates a brief stale window).
	if idx, ok := db.LoadCorpus(); ok && idx.Version == store.CorpusVersion {
		m.corpus = idx.Files
		m.dirCorpus = filetree.DirsFromMtimes(idx.DirMtimes)
		m.corpusTruncated = idx.Truncated
		m.corpusBuiltAt = time.Now()
		m.pendingValidate = &idx
	}
	if m.corpusBuiltAt.IsZero() {
		// Cold cache: Init fires the background walk; the splash covers it.
		m.corpusRebuilding = true
		m.splash = true
		m.splashStart = time.Now()
	}
	return m
}

// Init warms the search corpus in the background when there is no valid
// persisted index (a warm start instead re-validates the adopted index off the
// UI goroutine) and, in a git repo, kicks off the git-status poll loop.
func (m Model) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.corpusBuiltAt.IsZero() {
		cmds = append(cmds, m.rebuildCorpusCmd())
	} else if m.pendingValidate != nil {
		cmds = append(cmds, m.validateCorpusCmd(*m.pendingValidate))
	}
	if m.splash {
		cmds = append(cmds, splashTick())
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

// splashTickInterval paces the splash spinner; splashMinDisplay keeps the
// splash up long enough not to flash on a fast index build.
const (
	splashTickInterval = 100 * time.Millisecond
	splashMinDisplay   = 800 * time.Millisecond
)

// splashTickMsg advances the splash animation frame.
type splashTickMsg struct{}

func splashTick() tea.Cmd {
	return tea.Tick(splashTickInterval, func(time.Time) tea.Msg { return splashTickMsg{} })
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
func signatureValidFor(root string, idx store.CorpusIndex) bool {
	if len(idx.DirMtimes) == 0 {
		return false
	}
	for dir, mtime := range idx.DirMtimes {
		info, err := os.Stat(filepath.Join(root, dir))
		if err != nil || !info.IsDir() || info.ModTime().UnixNano() != mtime {
			return false
		}
	}
	return true
}

// validateCorpusCmd re-checks a warm-adopted index's directory signature off
// the UI goroutine — the stat sweep is O(#dirs) and would stall the first
// frame on a huge tree. A valid signature yields no message; a mismatch runs
// the full walk and delivers a fresh corpusMsg that swaps the corpus in.
func (m Model) validateCorpusCmd(idx store.CorpusIndex) tea.Cmd {
	root := m.root
	rebuild := m.rebuildCorpusCmd()
	return func() tea.Msg {
		if signatureValidFor(root, idx) {
			return nil
		}
		return rebuild()
	}
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

// ensureCorpus keeps m.corpus fresh without ever walking on the UI goroutine.
// On a cold cache it fires the background build (unless Init's, or a prior
// keystroke's, is already in flight) — callers see an empty corpus until the
// corpusMsg lands, so a huge tree never freezes a keystroke. On a warm cache
// older than corpusTTL it fires a background rebuild the same way.
// (Persistence happens only in the corpusMsg handler.)
func (m Model) ensureCorpus() (Model, tea.Cmd) {
	if m.corpusBuiltAt.IsZero() {
		if m.corpusRebuilding {
			return m, nil // a build is already in flight
		}
		m.corpusRebuilding = true
		return m, m.rebuildCorpusCmd()
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

	case splashTickMsg:
		if !m.splash {
			return m, nil // dead tick after a skip/dismiss
		}
		m.splashFrame++
		if !m.corpusBuiltAt.IsZero() && time.Since(m.splashStart) >= splashMinDisplay {
			m.splash = false
			return m, nil // index landed and the minimum display elapsed
		}
		return m, splashTick()

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

	case diffReadyMsg:
		return m.handleDiffReady(msg)

	case blameReadyMsg:
		return m.handleBlameReady(msg)

	case openapiReadyMsg:
		return m.handleOpenAPIReady(msg)

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
		if m.splash {
			return m, nil
		}
		return m.handleMouse(msg)

	case tea.PasteMsg:
		if m.splash {
			return m, nil
		}
		return m.handlePaste(msg.Content)

	case tea.KeyPressMsg:
		k := msg.String()
		if k == "ctrl+c" || k == "ctrl+q" {
			return m.quit()
		}
		if m.splash {
			// Any key skips the splash and is consumed — it must not leak
			// into the query bar as typed text.
			m.splash = false
			return m, nil
		}
		m.notice = ""
		m.errText = ""

		if m.messageOverlay != "" {
			if k == "enter" || k == "esc" {
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
		if k == "ctrl+p" && !m.inBarMode() {
			return m.openFuzzy()
		}
		if k == "ctrl+u" && !m.inBarMode() {
			return m.openUncommitted()
		}
		if k == "ctrl+g" && !m.inBarMode() {
			return m.openGrep()
		}
		if k == "ctrl+t" && !m.inBarMode() {
			return m.enterInspect()
		}
		if k == "shift+tab" && !m.inBarMode() {
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
		case modeDiff:
			return m.handleDiffKey(msg)
		case modeBlame:
			return m.handleBlameKey(msg)
		case modeConflict:
			return m.handleConflictKey(msg)
		case modeOpenAPI:
			return m.handleOpenAPIKey(msg)
		}
	}
	return m, nil
}

// handlePaste routes bracketed-paste text to whichever input has focus (v1
// delivered pastes as one multi-rune KeyRunes message; v2 sends tea.PasteMsg).
// Only the grep query and the edit buffer are multi-line — the single-line
// bars take the paste with newlines collapsed to spaces.
func (m Model) handlePaste(text string) (tea.Model, tea.Cmd) {
	if m.messageOverlay != "" || m.defPickOpen {
		return m, nil
	}
	if m.fuzzyOpen {
		m.fuzzyQuery += pasteLine(text)
		return m.refreshFuzzy(), nil
	}
	if m.grepOpen {
		return m.grepPaste(text)
	}
	// modeDiff, modeBlame, and modeConflict have no case: all are read-only to
	// typing (conflict mode edits only through Enter on a marker), so pastes
	// are inert.
	switch m.mode {
	case modeQuery:
		m = m.adoptPreview()
		m.command, m.qCursor = input.InsertAtCursor(m.command, m.qCursor, pasteLine(text))
		m.inputSuggestIndex = 0
		m.keyboardSelectedCommand = ""
	case modeEdit:
		return m.editPaste(text)
	case modeSearch:
		m.searchInput += pasteLine(text)
		m = m.focusNearestMatch()
	case modeCommand:
		m.cmdInput, m.cmdCursor = input.InsertAtCursor(m.cmdInput, m.cmdCursor, pasteLine(text))
	case modeExec:
		m.execInput, m.execCursor = input.InsertAtCursor(m.execInput, m.execCursor, pasteLine(text))
		m = m.refreshExecSugs()
	case modeSearchExec:
		m.searchExecInput, m.searchExecCursor = input.InsertAtCursor(m.searchExecInput, m.searchExecCursor, pasteLine(text))
	case modeInspect:
		m.inspectInput, m.inspectCursor = input.InsertAtCursor(m.inspectInput, m.inspectCursor, pasteLine(text))
	case modeOpenAPI:
		if m.openapiSearching {
			m.openapiSearch += pasteLine(text)
			m = m.focusOpenAPIMatch()
		}
	}
	return m, nil
}

// keyText returns the printable text of a key press, or "" when the press is
// a chord rather than typing. Shift and the lock states count as typing —
// under the enhanced keyboard protocol Shift+d arrives as Text "D" with
// ModShift set, and CapsLock/NumLock report as modifiers too. Ctrl, Alt, and
// the other real chord modifiers do not produce text input.
func keyText(msg tea.KeyPressMsg) string {
	if msg.Mod&^(tea.ModShift|tea.ModCapsLock|tea.ModNumLock|tea.ModScrollLock) != 0 {
		return ""
	}
	return msg.Text
}

// pasteLine flattens pasted text for the single-line input bars: CR/CRLF
// normalize to \n, then newlines collapse to single spaces.
func pasteLine(text string) string {
	s := strings.ReplaceAll(text, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\n", " ")
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
	if p := m.fuzzySelectedPath(); p != "" {
		return p
	}
	return filetree.ResolveSidebarCommand(m.command, m.selectedCommand)
}

// highlightedSidebarCommand is the path that drives the sidebar HIGHLIGHT:
// the open finder owns the sidebar, then keyboard/popup navigation, then
// the typed path.
func (m Model) highlightedSidebarCommand() string {
	if p := m.fuzzySelectedPath(); p != "" {
		return p
	}
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
	m = m.sigUnpin() // the pin belongs to the buffer being left
	m.openFile = &f
	m.openRel = rel
	m.fileScrollX, m.fileScrollY = 0, 0
	m.selectedCommand = rel    // sidebar keeps tracking the open file
	m.jumpStack = nil          // a deliberate open starts a fresh navigation trail
	m = m.clearDiffState()     // and ends any diff review of the file being left
	m = m.clearConflictState() // likewise any conflict-solving session
	m = m.clearOpenAPIState()  // and any OpenAPI preview
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
	// Search, diff-review, and conflict-solving modes always sit on top of a
	// live edit session, so the buffer — not the on-disk snapshot — is the
	// truth there too (conflict mode even mutates it in place). The inspection
	// dashboard only pauses whatever mode it opened over.
	md := m.mode
	if md == modeInspect {
		md = m.inspectPrevMode
	}
	if md == modeEdit || md == modeSearch || md == modeSearchExec ||
		md == modeDiff || md == modeConflict || md == modeOpenAPI || md == modeBlame {
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

// invalidateHighlightCaches recomputes or clears every cached highlight
// segment after a syntax.SetStyle switch, so the new colors show immediately.
// segStyles (render.go) needs no reset — it is keyed by color hex — and the
// syntax package's entry cache is reset by SetStyle itself.
func (m Model) invalidateHighlightCaches() Model {
	m = m.refreshFileHighlights()
	if m.searchHl != nil && m.openFile != nil { // frozen at enterSearch
		m.searchHl = syntax.HighlightLines(m.openFile.FileName, m.searchContent)
	}
	m.grepHlRel, m.grepHl = "", nil // refreshGrepPreview recomputes on next selection
	m.defPickPrevRel, m.defPickPrevLines, m.defPickPrevHl = "", nil, nil
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

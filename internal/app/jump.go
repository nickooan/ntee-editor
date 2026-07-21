package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/lsp"
	"github.com/nickooan/ntee-editor/internal/store"
	"github.com/nickooan/ntee-editor/internal/syntax"
	"github.com/nickooan/ntee-editor/internal/view"
)

// Jump navigation (Ctrl+J / Ctrl+O in edit mode), ported from r1quest's jump
// stack. The target under the cursor resolves as a file path first (a language
// server does not resolve bare paths), then via the server's
// textDocument/definition. File types with no configured server report "no
// language server" rather than guessing; the stack mechanics are shared.

// jumpFrame is one origin to return to.
type jumpFrame struct {
	relPath string
	cy, cx  int
	scrollY int
}

// maxJumpFrames bounds the trail; oldest frames drop off.
const maxJumpFrames = 20

// jumpToken returns the text to resolve: the active selection, else the
// whitespace-delimited word under the cursor, trimmed of the punctuation that
// usually wraps code tokens (quotes, brackets, trailing commas…).
func (m Model) jumpToken() string {
	token := m.edit.selectedText()
	if strings.TrimSpace(token) == "" {
		r := wordRange(m.edit.line(), m.edit.cx)
		token = string(m.edit.line()[r.start:r.end])
	}
	return strings.Trim(token, "\"'`()[]{}<>,;:")
}

// looksLikePath reports whether a token plausibly names a file.
func looksLikePath(token string) bool {
	return strings.ContainsAny(token, "/.")
}

// statRegular reports whether abs names a regular file.
func statRegular(abs string) bool {
	info, err := os.Stat(abs)
	return err == nil && info.Mode().IsRegular()
}

// probeExtensions returns the extensions tried for an extensionless path
// token: the current file's own extension first (an import in a .ts file most
// likely names another .ts file), then every configured language extension.
// Driven by cfg.Languages so any language added via config resolves the same
// way — nothing here is language-specific.
func (m Model) probeExtensions() []string {
	own := strings.ToLower(filepath.Ext(m.openRel))
	seen := map[string]bool{own: true}
	var exts []string
	if own != "" {
		exts = append(exts, own)
	}
	var rest []string
	for _, lc := range m.cfg.Languages {
		for _, e := range lc.Extensions {
			e = strings.ToLower(e)
			if !seen[e] {
				seen[e] = true
				rest = append(rest, e)
			}
		}
	}
	sort.Strings(rest)
	return append(exts, rest...)
}

// resolveJumpPath tries the token as a file path: relative to the current
// file's directory, then relative to the project root. The target must stat
// as a regular file inside the root (stat-first so missing targets error
// cleanly instead of opening an error buffer). An extensionless token also
// probes the configured language extensions ("../config" → config.ts) and
// directory index files ("./lib" → lib/index.ts).
func (m Model) resolveJumpPath(token string) (string, bool) {
	if !looksLikePath(token) {
		return "", false
	}
	currentDir := filepath.Dir(m.openFile.Path)
	for _, abs := range []string{
		filepath.Clean(filepath.Join(currentDir, token)),
		filepath.Clean(filepath.Join(m.root, token)),
	} {
		rel, err := filepath.Rel(m.root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			continue // outside the project root
		}
		if statRegular(abs) {
			return filepath.ToSlash(rel), true
		}
		for _, ext := range m.probeExtensions() {
			if statRegular(abs + ext) {
				return filepath.ToSlash(rel) + ext, true
			}
			if statRegular(filepath.Join(abs, "index"+ext)) {
				return filepath.ToSlash(rel) + "/index" + ext, true
			}
		}
	}
	return "", false
}

// defCandidate is one possible jump destination.
type defCandidate struct {
	rel      string
	line     int
	utf16Col int
}

// maxDefCandidates caps the picker (reference lists can run long).
const maxDefCandidates = 50

// jumpToCandidates handles the 0/1/many outcome shared by the LSP and
// heuristic paths. title labels the picker ("definitions of X" / "references
// of X"); emptyErr is the 0-hit message.
func (m Model) jumpToCandidates(title, token, emptyErr string, cands []defCandidate) Model {
	switch len(cands) {
	case 0:
		m.errText = emptyErr
		return m
	case 1:
		return m.jumpToLocation(cands[0].rel, cands[0].line, cands[0].utf16Col)
	default:
		if len(cands) > maxDefCandidates {
			cands = cands[:maxDefCandidates]
		}
		m.defPickOpen = true
		m.defPickTitle = title
		m.defPickToken = token
		m.defPickItems = cands
		m.defPickIndex = 0
		m.defPickPrevRel = ""
		return m.refreshDefPickPreview()
	}
}

// refreshDefPickPreview loads (and highlights) the selected candidate's file
// for the picker's preview rows; re-reads only when the file changes.
func (m Model) refreshDefPickPreview() Model {
	if len(m.defPickItems) == 0 {
		m.defPickPrevRel, m.defPickPrevLines, m.defPickPrevHl = "", nil, nil
		return m
	}
	c := m.defPickItems[input.Clamp(m.defPickIndex, 0, len(m.defPickItems)-1)]
	if c.rel == m.defPickPrevRel {
		return m
	}
	m.defPickPrevRel = c.rel
	m.defPickPrevLines, m.defPickPrevHl = nil, nil
	f, ok := filetree.ReadViewFile(m.root, c.rel)
	if !ok || f.Binary {
		return m
	}
	m.defPickPrevLines = view.NormalizeLines(f.Content)
	if kb := m.cfg.Editor.MaxHighlightKB; kb <= 0 || len(f.Content) <= kb*1024 {
		m.defPickPrevHl = syntax.HighlightLines(filepath.Base(c.rel), f.Content)
	}
	return m
}

// handleDefPickKey drives the multi-definition picker overlay.
func (m Model) handleDefPickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.defPickOpen = false
	case tea.KeyUp:
		m.defPickIndex = max(0, m.defPickIndex-1)
		m = m.refreshDefPickPreview()
	case tea.KeyDown:
		m.defPickIndex = min(len(m.defPickItems)-1, m.defPickIndex+1)
		m = m.refreshDefPickPreview()
	case tea.KeyEnter:
		m.defPickOpen = false
		if len(m.defPickItems) > 0 {
			c := m.defPickItems[input.Clamp(m.defPickIndex, 0, len(m.defPickItems)-1)]
			m = m.jumpToLocation(c.rel, c.line, c.utf16Col)
		}
	}
	return m, nil
}

// definitionMsg carries an async textDocument/definition result. token labels
// the picker; col is the rune column that was queried (the references pivot
// re-queries there); tryCols holds fallback columns to try when the answer is
// empty; snapped marks a query at a snapped-to column rather than the cursor.
type definitionMsg struct {
	token   string
	col     int
	tryCols []int
	snapped bool
	locs    []lsp.Location
	err     error
}

// referencesMsg carries an async textDocument/references result.
type referencesMsg struct {
	token string
	locs  []lsp.Location
	err   error
}

// collectInRootCandidates converts LSP locations to in-project candidates,
// deduped by (file, line).
func (m Model) collectInRootCandidates(locs []lsp.Location) []defCandidate {
	var cands []defCandidate
	seen := map[string]bool{}
	for _, loc := range locs {
		abs, ok := lsp.URIToPath(loc.URI)
		if !ok {
			continue
		}
		rel, err := filepath.Rel(m.root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			continue // outside the project
		}
		c := defCandidate{
			rel:      filepath.ToSlash(rel),
			line:     loc.Range.Start.Line,
			utf16Col: loc.Range.Start.Character,
		}
		key := fmt.Sprintf("%s:%d", c.rel, c.line)
		if !seen[key] {
			seen[key] = true
			cands = append(cands, c)
		}
	}
	return cands
}

// maxJumpTries bounds how many nearby identifiers the no-symbol fallback
// queries before giving up.
const maxJumpTries = 4

// jumpToReference (Ctrl+J) jumps to whatever the cursor plausibly means,
// pushing the origin so Ctrl+O returns. Priority ladder:
//  1. a path-shaped token under the cursor that names a real file → open it
//     (a language server does not resolve bare paths);
//  2. cursor on an identifier → textDocument/definition there (a definition
//     resolving to the cursor's own line — or returning nothing at all, as
//     kotlin-language-server does on a declaration — pivots to references;
//     see handleDefinition);
//  3. cursor on no symbol: a quoted path elsewhere on the line that names a
//     real file → open it;
//  4. else snap to the nearest identifiers on the line and query definition,
//     retrying the next-nearest (bounded) while the server returns nothing.
func (m Model) jumpToReference() (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		return m, nil
	}
	token := m.jumpToken()
	if rel, ok := m.resolveJumpPath(token); ok {
		return m.jumpToLocation(rel, 0, 0), nil
	}

	line := m.edit.line()
	if _, _, onIdent := identifierAt(line, m.edit.cx); onIdent {
		return m.requestDefinition(token, m.edit.cx, nil, false)
	}
	if rel, ok := m.jumpLinePath(); ok {
		return m.jumpToLocation(rel, 0, 0), nil
	}
	cols := identifierCols(line, m.edit.cx, maxJumpTries)
	if len(cols) == 0 {
		m.errText = "nothing to jump to"
		return m, nil
	}
	return m.requestDefinition(identifierText(line, cols[0]), cols[0], cols[1:], true)
}

// jumpLinePath scans the cursor line's quoted spans for one that resolves to a
// real file — the "this line references a file" default for a cursor that is
// not on any symbol.
func (m Model) jumpLinePath() (string, bool) {
	line := m.edit.line()
	for i := 0; i < len(line); i++ {
		q := line[i]
		if q != '"' && q != '\'' && q != '`' {
			continue
		}
		for j := i + 1; j < len(line); j++ {
			if line[j] != q {
				continue
			}
			if rel, ok := m.resolveJumpPath(strings.TrimSpace(string(line[i+1 : j]))); ok {
				return rel, true
			}
			i = j // skip past this span; keep scanning for a later one
			break
		}
	}
	return "", false
}

// noServerError explains the missing server and how to install one —
// "ClientFor false" usually means the binary is absent, and --prepare-lsp is
// the remedy for that and for unmapped extensions alike.
func (m Model) noServerError() string {
	if ext := filepath.Ext(m.openRel); ext != "" {
		return fmt.Sprintf("no language server for %s files — try: ntee --prepare-lsp", ext)
	}
	return "no language server for this file type"
}

// requestDefinition queries textDocument/definition at rune column cx of the
// cursor line. tryCols carries the fallback columns handleDefinition may try
// next when the answer is empty.
func (m Model) requestDefinition(token string, cx int, tryCols []int, snapped bool) (tea.Model, tea.Cmd) {
	client, ok := m.lsp.ClientFor(m.openFile.Path)
	if !ok {
		// No server: a file referenced on the line is still a useful jump —
		// an import line redirects to the imported file instead of erroring.
		if rel, ok := m.jumpLinePath(); ok {
			return m.jumpToLocation(rel, 0, 0), nil
		}
		m.errText = m.noServerError()
		return m, nil
	}
	path := m.openFile.Path
	line := m.edit.cy
	utf16Col := lsp.UTF16Col(m.edit.lines[m.edit.cy], cx)
	return m, func() tea.Msg {
		locs, err := client.Definition(path, line, utf16Col)
		return definitionMsg{token: token, col: cx, tryCols: tryCols, snapped: snapped, locs: locs, err: err}
	}
}

// handleDefinition lands an LSP definition answer, falling back to the
// heuristic when the server had none (or is still starting). A definition that
// lands on the cursor's own line, or that comes back empty for an identifier
// the cursor sits directly on, pivots to references — the "who uses this
// declaration?" question (empty is how kotlin-language-server answers a
// definition query on a declaration). Guards re-run — the user may have typed
// while the request was in flight.
func (m Model) handleDefinition(msg definitionMsg) (tea.Model, tea.Cmd) {
	if m.openFile == nil || m.mode != modeEdit {
		return m, nil
	}
	// LSP-strict: when a server is configured for this file type, its answer
	// is final — no heuristic fallback that could jump somewhere plausible
	// but wrong. The heuristic path only serves files with no server at all
	// (it never reaches this handler).
	if msg.err != nil {
		m.errText = lspLookupError(msg.err)
		return m, nil
	}
	all := m.collectInRootCandidates(msg.locs)
	// Split off hits on the cursor's own line: those mean the cursor is
	// already ON the definition, where the useful question becomes "who
	// references this?".
	var others []defCandidate
	onCursor := false
	for _, c := range all {
		if c.rel == m.openRel && c.line == m.edit.cy {
			onCursor = true
			continue
		}
		others = append(others, c)
	}
	if len(others) > 0 {
		return m.jumpToCandidates("definitions of "+msg.token, msg.token,
			"no definition or path under cursor: "+msg.token, others), nil
	}
	if onCursor {
		return m.requestReferences(msg.token, msg.col)
	}
	// The queried spot produced nothing — a keyword like `import`, or noise.
	// A file referenced on the line is the better guess, so prefer it over
	// snapping to neighboring identifiers.
	if rel, ok := m.jumpLinePath(); ok {
		return m.jumpToLocation(rel, 0, 0), nil
	}
	if len(msg.tryCols) > 0 {
		// Nothing at the tried column — snap to the next-nearest identifier.
		line := m.edit.line()
		return m.requestDefinition(identifierText(line, msg.tryCols[0]), msg.tryCols[0], msg.tryCols[1:], true)
	}
	if msg.snapped {
		// A snapped guess with no answer stays an error — we don't chase
		// references for an identifier the user didn't actually point at.
		m.errText = "no definition found near cursor"
		return m, nil
	}
	// The cursor was directly on an identifier but the server returned no
	// definition. Some servers (kotlin-language-server) don't return a
	// self-location for a definition query on a declaration, so the onCursor
	// pivot above never fires. Treat the identifier as its own declaration and
	// ask who references it.
	return m.requestReferences(msg.token, msg.col)
}

// lspLookupError renders a server failure as a status message; the common
// case is a server that is still indexing.
func lspLookupError(err error) string {
	if strings.Contains(err.Error(), "not ready") {
		return "language server is still starting — try again shortly"
	}
	return "lsp lookup failed: " + err.Error()
}

// requestReferences chains a references lookup at rune column cx of the cursor
// line — the column whose definition resolved to this line, which is not
// necessarily the cursor column when the jump snapped to a nearby identifier.
func (m Model) requestReferences(token string, cx int) (tea.Model, tea.Cmd) {
	if client, ok := m.lsp.ClientFor(m.openFile.Path); ok {
		path := m.openFile.Path
		line := m.edit.cy
		utf16Col := lsp.UTF16Col(m.edit.lines[m.edit.cy], cx)
		return m, func() tea.Msg {
			locs, err := client.References(path, line, utf16Col)
			return referencesMsg{token: token, locs: locs, err: err}
		}
	}
	m.errText = m.noServerError()
	return m, nil
}

// handleReferences lands an LSP references answer, excluding the definition
// line itself. LSP-strict: an empty or failed answer reports rather than
// guessing via the heuristic.
func (m Model) handleReferences(msg referencesMsg) (tea.Model, tea.Cmd) {
	if m.openFile == nil || m.mode != modeEdit {
		return m, nil
	}
	if msg.err != nil {
		m.errText = lspLookupError(msg.err)
		return m, nil
	}
	var cands []defCandidate
	for _, c := range m.collectInRootCandidates(msg.locs) {
		if c.rel == m.openRel && c.line == m.edit.cy {
			continue
		}
		cands = append(cands, c)
	}
	return m.jumpToCandidates("references of "+msg.token, msg.token,
		"no references found: "+msg.token, cands), nil
}

// jumpToLocation pushes the origin frame and lands on (rel, line, utf16Col).
func (m Model) jumpToLocation(rel string, line, utf16Col int) Model {
	m.jumpStack = append(m.jumpStack, jumpFrame{
		relPath: m.openRel,
		cy:      m.edit.cy,
		cx:      m.edit.cx,
		scrollY: m.fileScrollY,
	})
	if len(m.jumpStack) > maxJumpFrames {
		m.jumpStack = append([]jumpFrame(nil), m.jumpStack[len(m.jumpStack)-maxJumpFrames:]...)
	}

	if rel == m.openRel {
		// Same-file jump: just move the cursor.
		m.edit.clearSelection()
		m.edit.cy = input.Clamp(line, 0, len(m.edit.lines)-1)
		m.edit.cx = lsp.RuneCol(m.edit.lines[m.edit.cy], utf16Col)
		m.edit.clampCursor()
		return m.anchorCursorLine()
	}

	next, opened := m.openJumpFile(rel, line, 0, 0)
	if !opened {
		// A failed jump leaves no stack residue.
		next.jumpStack = next.jumpStack[:len(next.jumpStack)-1]
		return next
	}
	next.edit.cx = lsp.RuneCol(next.edit.lines[next.edit.cy], utf16Col)
	next.edit.clampCursor()
	return next.anchorCursorLine()
}

// jumpBack (Ctrl+O) pops the trail and restores file, cursor, and scroll. A
// frame whose file has since vanished stays popped — retrying forever would
// be worse.
func (m Model) jumpBack() (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		return m, nil
	}
	if len(m.jumpStack) == 0 {
		m.errText = "no jump to return to"
		return m, nil
	}
	frame := m.jumpStack[len(m.jumpStack)-1]
	m.jumpStack = m.jumpStack[:len(m.jumpStack)-1]
	next, _ := m.openJumpFile(frame.relPath, frame.cy, frame.cx, frame.scrollY)
	return next, nil
}

// openJumpFile opens a target into a fresh edit session at (cy, cx), keeping
// the jump stack intact (unlike a deliberate openFileAt). Like openFileAt, an
// unsaved outgoing buffer is stashed as a draft (its tab stays red) and a
// stashed draft of the target is restored — jumping never loses edits.
func (m Model) openJumpFile(rel string, cy, cx, scrollY int) (Model, bool) {
	f, ok := filetree.ReadViewFile(m.root, rel)
	if !ok || f.Binary {
		m.errText = "cannot open " + rel
		return m, false
	}
	if _, err := os.Stat(f.Path); err != nil {
		m.errText = "cannot open " + rel
		return m, false
	}
	m = m.recordCursor()
	m = m.stashDraftIfDirty()
	m.openFile = &f
	m.openRel = rel
	m.selectedCommand = rel
	m = m.beginEditSession(f.Content) // does not touch the jump stack
	if d, ok := m.db.LoadDraft(rel); ok {
		m = m.restoreDraft(d)
	}
	m = m.addTab(rel)
	m.mode = modeEdit
	m.edit.cy, m.edit.cx = cy, cx
	m.edit.clampCursor()
	m.fileScrollY = scrollY
	_ = m.db.TouchOpened(store.OpenedFile{Path: rel, LastOpenedAt: time.Now().UnixMilli()})
	if client, ok := m.lsp.ClientFor(f.Path); ok {
		client.DidOpen(f.Path, f.Content)
	}
	return m, true
}

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
// stack. The target under the cursor resolves as a file path first, then via
// a Go/TS definition-pattern heuristic (current buffer, then same-extension
// project files). LSP replaces the heuristic in v2 without touching the stack
// mechanics.

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

// defPatterns builds the definition-line patterns for a word (Go + TS shapes).
func defPatterns(word string) []*regexp.Regexp {
	w := regexp.QuoteMeta(word)
	shapes := []string{
		`^\s*func\s+(\([^)]*\)\s*)?` + w + `\s*[([]`,                     // go func / method / generic
		`^\s*type\s+` + w + `\b`,                                         // go type
		`^\s*(const|var)\s+` + w + `\b`,                                  // go const/var
		`^\s*(export\s+)?(default\s+)?(async\s+)?function\s+` + w + `\b`, // ts function
		`^\s*(export\s+)?(abstract\s+)?class\s+` + w + `\b`,              // ts class
		`^\s*(export\s+)?interface\s+` + w + `\b`,                        // ts interface
		`^\s*(export\s+)?(const|let|var)\s+` + w + `\s*[=:(]`,            // ts binding
	}
	out := make([]*regexp.Regexp, 0, len(shapes))
	for _, s := range shapes {
		if re, err := regexp.Compile(s); err == nil {
			out = append(out, re)
		}
	}
	return out
}

// findDefinitionLine returns the first line matching any definition pattern,
// skipping `skipLine` (-1 to disable) so a jump from the definition itself
// does not land in place.
func findDefinitionLine(lines []string, patterns []*regexp.Regexp, skipLine int) int {
	for i, line := range lines {
		if i == skipLine {
			continue
		}
		for _, re := range patterns {
			if re.MatchString(line) {
				return i
			}
		}
	}
	return -1
}

// looksLikePath reports whether a token plausibly names a file.
func looksLikePath(token string) bool {
	return strings.ContainsAny(token, "/.")
}

// resolveJumpPath tries the token as a file path: relative to the current
// file's directory, then relative to the project root. The target must stat
// as a regular file inside the root (stat-first so missing targets error
// cleanly instead of opening an error buffer).
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
		if info, err := os.Stat(abs); err == nil && info.Mode().IsRegular() {
			return filepath.ToSlash(rel), true
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

// resolveJumpTargets resolves a token to jump candidates. The path branch is
// single-hit; the definition-pattern branch collects every match in the
// current buffer plus the first match per same-extension project file.
func (m Model) resolveJumpTargets(token string) []defCandidate {
	if rel, ok := m.resolveJumpPath(token); ok {
		return []defCandidate{{rel: rel}}
	}

	patterns := defPatterns(token)
	var out []defCandidate
	for i, line := range m.edit.lines {
		if i == m.edit.cy {
			continue // a jump from the definition itself must not land in place
		}
		for _, re := range patterns {
			if re.MatchString(line) {
				out = append(out, defCandidate{rel: m.openRel, line: i})
				break
			}
		}
	}
	ext := strings.ToLower(filepath.Ext(m.openFile.FileName))
	for _, rel := range filetree.BuildAllEntries(m.root, m.cfg.Tree.Ignore) {
		if len(out) >= maxDefCandidates {
			break
		}
		if rel == m.openRel || strings.ToLower(filepath.Ext(rel)) != ext {
			continue
		}
		f, ok := filetree.ReadViewFile(m.root, rel)
		if !ok || f.Binary {
			continue
		}
		if line := findDefinitionLine(view.NormalizeLines(f.Content), patterns, -1); line >= 0 {
			out = append(out, defCandidate{rel: rel, line: line})
		}
	}
	return out
}

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

// definitionMsg carries an async textDocument/definition result. token rides
// along so an empty/failed answer can fall back to the heuristic.
type definitionMsg struct {
	token string
	locs  []lsp.Location
	err   error
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

// jumpToReference (Ctrl+J) jumps to the definition or file under the cursor,
// pushing the origin so Ctrl+O returns. When a language server is available
// it is asked first (async — the answer arrives as definitionMsg); otherwise
// the regex heuristic runs synchronously.
func (m Model) jumpToReference() (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		return m, nil
	}
	if m.edit.dirty {
		// A jump rebuilds the editor and undo timeline; unsaved work would
		// be lost silently.
		m.errText = "unsaved changes — save (Ctrl+S) before jumping"
		return m, nil
	}
	token := m.jumpToken()
	if strings.TrimSpace(token) == "" {
		m.errText = "nothing to jump to"
		return m, nil
	}

	if client, ok := m.lsp.ClientFor(m.openFile.Path); ok {
		path := m.openFile.Path
		line := m.edit.cy
		utf16Col := lsp.UTF16Col(m.edit.lines[m.edit.cy], m.edit.cx)
		return m, func() tea.Msg {
			locs, err := client.Definition(path, line, utf16Col)
			return definitionMsg{token: token, locs: locs, err: err}
		}
	}
	return m.heuristicJump(token)
}

// handleDefinition lands an LSP definition answer, falling back to the
// heuristic when the server had none (or is still starting). Guards re-run —
// the user may have typed while the request was in flight.
func (m Model) handleDefinition(msg definitionMsg) (tea.Model, tea.Cmd) {
	if m.openFile == nil || m.mode != modeEdit {
		return m, nil
	}
	if m.edit.dirty {
		m.errText = "unsaved changes — save (Ctrl+S) before jumping"
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
		return m.requestReferences(msg.token)
	}
	m.errText = "no definition found: " + msg.token
	return m, nil
}

// lspLookupError renders a server failure as a status message; the common
// case is a server that is still indexing.
func lspLookupError(err error) string {
	if strings.Contains(err.Error(), "not ready") {
		return "language server is still starting — try again shortly"
	}
	return "lsp lookup failed: " + err.Error()
}

// requestReferences chains a references lookup for the symbol under the
// cursor (LSP when available, heuristic otherwise).
func (m Model) requestReferences(token string) (tea.Model, tea.Cmd) {
	if client, ok := m.lsp.ClientFor(m.openFile.Path); ok {
		path := m.openFile.Path
		line := m.edit.cy
		utf16Col := lsp.UTF16Col(m.edit.lines[m.edit.cy], m.edit.cx)
		return m, func() tea.Msg {
			locs, err := client.References(path, line, utf16Col)
			return referencesMsg{token: token, locs: locs, err: err}
		}
	}
	return m.heuristicReferences(token), nil
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

// heuristicReferences finds word-boundary occurrences of token across the
// buffer and same-extension project files, skipping definition-shaped lines.
func (m Model) heuristicReferences(token string) Model {
	wordRe, err := regexp.Compile(`\b` + regexp.QuoteMeta(token) + `\b`)
	if err != nil {
		m.errText = "no references found: " + token
		return m
	}
	defs := defPatterns(token)
	isDefLine := func(line string) bool {
		for _, re := range defs {
			if re.MatchString(line) {
				return true
			}
		}
		return false
	}

	var cands []defCandidate
	for i, line := range m.edit.lines {
		if i == m.edit.cy || isDefLine(line) {
			continue
		}
		if wordRe.MatchString(line) {
			cands = append(cands, defCandidate{rel: m.openRel, line: i})
		}
	}
	ext := strings.ToLower(filepath.Ext(m.openFile.FileName))
	for _, rel := range filetree.BuildAllEntries(m.root, m.cfg.Tree.Ignore) {
		if len(cands) >= maxDefCandidates {
			break
		}
		if rel == m.openRel || strings.ToLower(filepath.Ext(rel)) != ext {
			continue
		}
		f, ok := filetree.ReadViewFile(m.root, rel)
		if !ok || f.Binary {
			continue
		}
		for i, line := range view.NormalizeLines(f.Content) {
			if isDefLine(line) {
				continue
			}
			if wordRe.MatchString(line) {
				cands = append(cands, defCandidate{rel: rel, line: i})
				if len(cands) >= maxDefCandidates {
					break
				}
			}
		}
	}
	return m.jumpToCandidates("references of "+token, token, "no references found: "+token, cands)
}

// heuristicJump is the no-LSP path: path-under-cursor, else definition
// patterns over the buffer and same-extension project files. A cursor sitting
// on a definition-shaped line flips to a reference search instead.
func (m Model) heuristicJump(token string) (tea.Model, tea.Cmd) {
	for _, re := range defPatterns(token) {
		if re.MatchString(m.edit.lines[m.edit.cy]) {
			return m.heuristicReferences(token), nil
		}
	}
	return m.jumpToCandidates("definitions of "+token, token,
		"no definition or path under cursor: "+token, m.resolveJumpTargets(token)), nil
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
	if m.edit.dirty {
		m.errText = "unsaved changes — save (Ctrl+S) before jumping"
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
// the jump stack intact (unlike a deliberate openFileAt).
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
	m.openFile = &f
	m.openRel = rel
	m.selectedCommand = rel
	m = m.beginEditSession(f.Content) // does not touch the jump stack
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

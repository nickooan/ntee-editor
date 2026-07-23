package app

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/input"
)

// queryInputSuggestions completes the typed bar text: exact/prefix over the
// visible tree, fuzzy over the full corpus.
func (m Model) queryInputSuggestions(entries []filetree.FileTreeEntry) []filetree.InputSuggestion {
	// Reads the cached corpus (populated by ensureCorpus in the key handler);
	// never walks the tree here, so this is cheap on every keystroke and render.
	return filetree.BuildInputSuggestions(entries, m.corpus, m.dirCorpus, m.command, filetree.MaxInputSuggestions)
}

// handleQueryKey is the home-mode handler: the bottom input bar drives the
// sidebar (typing expands, navigation highlights) and Enter enters/opens.
func (m Model) handleQueryKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m, corpusCmd := m.ensureCorpus()
	entries := m.treeEntries()
	suggestions := m.queryInputSuggestions(entries)
	if m.inputSuggestIndex >= len(suggestions) {
		m.inputSuggestIndex = 0
	}
	popupOpen := len(suggestions) > 0

	switch msg.Type {
	case tea.KeyShiftUp:
		if popupOpen {
			return m.moveInputSuggestion(suggestions, -1), nil
		}
		return m.moveSidebarSelection(entries, -1), nil
	case tea.KeyShiftDown:
		if popupOpen {
			return m.moveInputSuggestion(suggestions, 1), nil
		}
		return m.moveSidebarSelection(entries, 1), nil

	case tea.KeyUp:
		if popupOpen {
			return m.moveInputSuggestion(suggestions, -1), nil
		}
		m.fileScrollY = input.Clamp(m.fileScrollY-1, 0, max(0, len(m.fileLines)-1))
	case tea.KeyDown:
		if popupOpen {
			return m.moveInputSuggestion(suggestions, 1), nil
		}
		m.fileScrollY = input.Clamp(m.fileScrollY+1, 0, max(0, len(m.fileLines)-1))
	case tea.KeyPgUp:
		m.fileScrollY = input.Clamp(m.fileScrollY-m.contentHeight(), 0, max(0, len(m.fileLines)-1))
	case tea.KeyPgDown:
		m.fileScrollY = input.Clamp(m.fileScrollY+m.contentHeight(), 0, max(0, len(m.fileLines)-1))
	case tea.KeyLeft:
		m.fileScrollX = max(0, m.fileScrollX-4)
	case tea.KeyRight:
		m.fileScrollX += 4

	case tea.KeyShiftLeft:
		m = m.adoptPreview()
		m.qCursor = input.MoveCursor(m.command, m.qCursor, -1)
	case tea.KeyShiftRight:
		m = m.adoptPreview()
		m.qCursor = input.MoveCursor(m.command, m.qCursor, 1)

	case tea.KeyEnter:
		return m.submitQuery(entries, suggestions)

	case tea.KeyEsc:
		return m.moveQueryToParentDirectory(), nil

	case tea.KeyTab:
		if m.openFile != nil {
			m = m.beginEditSession(m.openFile.Content)
			m.mode = modeEdit
		}

	case tea.KeyBackspace:
		m = m.adoptPreview()
		m.command, m.qCursor, _ = input.RemoveBeforeCursor(m.command, m.qCursor)
		m.inputSuggestIndex = 0
		m.keyboardSelectedCommand = "" // typing re-anchors the highlight to the text
	case tea.KeySpace:
		m = m.adoptPreview()
		m.command, m.qCursor = input.InsertAtCursor(m.command, m.qCursor, " ")
		m.inputSuggestIndex = 0
		m.keyboardSelectedCommand = ""
	case tea.KeyRunes:
		m = m.adoptPreview()
		m.command, m.qCursor = input.InsertAtCursor(m.command, m.qCursor, string(msg.Runes))
		m.inputSuggestIndex = 0
		m.keyboardSelectedCommand = ""
	}
	// corpusCmd (background revalidation, or nil) rides out on the typing paths
	// that fall through here — exactly when fresh results matter.
	return m, corpusCmd
}

// parseInlineFs recognizes the bar's filesystem commands — "<path> :mkdir
// <rel>", "<path> :touch <rel>", and "<path> :rm" (the prefix itself is the
// target; an empty prefix means the root for mkdir/touch) — and returns the
// verb plus the root-relative target (prefix joined with the argument,
// cleaned). ok is false for any other input, including a missing target or one
// escaping the root, so other ":" commands and plain navigation are untouched.
func parseInlineFs(trimmed string) (verb, rel string, ok bool) {
	var base, rest string
	switch {
	case strings.HasPrefix(trimmed, ":"):
		rest = trimmed[1:]
	default:
		i := strings.Index(trimmed, " :")
		if i == -1 {
			return "", "", false
		}
		base, rest = strings.TrimSpace(trimmed[:i]), trimmed[i+2:]
	}
	verb, arg, _ := strings.Cut(rest, " ")
	arg = strings.TrimSpace(arg)
	switch verb {
	case "mkdir", "touch":
		if arg == "" {
			return "", "", false
		}
	case "rm":
		// rm removes the typed path itself: no argument, and a bare ":rm"
		// (which would target the root) is refused.
		if arg != "" || base == "" {
			return "", "", false
		}
	default:
		return "", "", false
	}
	// An absolute argument must be rejected here — the slash-normalization
	// below would otherwise strip the leading "/" and mask it.
	if strings.HasPrefix(strings.ReplaceAll(arg, "\\", "/"), "/") {
		return "", "", false
	}

	base = strings.Trim(strings.ReplaceAll(base, "\\", "/"), "/")
	arg = strings.Trim(strings.ReplaceAll(arg, "\\", "/"), "/")
	rel = arg
	if base != "" {
		rel = base + "/" + arg
	}
	rel = path.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return "", "", false
	}
	return verb, rel, true
}

// queryCreate performs an inline create and enters the result: a new dir is
// confirmed into the bar/sidebar (like Enter on a directory), a new file opens
// straight into edit mode. Errors keep the typed input so it can be corrected.
func (m Model) queryCreate(verb, rel string) (tea.Model, tea.Cmd) {
	if verb == "mkdir" {
		if err := filetree.MakeDir(m.root, rel); err != nil {
			m.errText = "mkdir failed: " + err.Error()
			return m, nil
		}
		m.notice = "created " + rel + "/"
		m.keyboardSelectedCommand = ""
		m.inputSuggestIndex = 0
		m.selectedCommand = rel + "/"
		m.command = m.selectedCommand
		m.qCursor = len([]rune(m.command))
		return m, nil
	}

	created, err := filetree.EnsureFile(m.root, rel)
	if err != nil {
		m.errText = "touch failed: " + err.Error()
		return m, nil
	}
	if created {
		m.notice = "created " + rel
	} else {
		m.notice = "opened existing " + rel
	}
	m.keyboardSelectedCommand = ""
	m.inputSuggestIndex = 0
	m.command, m.qCursor = "", 0
	return m.openFileAt(rel), nil
}

// queryRemove deletes the typed path (file, or directory with its whole
// subtree), prunes any editor state that pointed into it (tabs, drafts, the
// open file), and moves the bar to the parent directory.
func (m Model) queryRemove(rel string) (tea.Model, tea.Cmd) {
	if _, err := os.Stat(filepath.Join(m.root, filepath.FromSlash(rel))); err != nil {
		m.errText = "rm: no such path: " + rel
		return m, nil
	}
	if err := filetree.Remove(m.root, rel); err != nil {
		m.errText = "rm failed: " + err.Error()
		return m, nil
	}
	m = m.dropRemovedPath(rel)
	parent, _ := filetree.ResolveParentDirectoryCommand(rel)
	m.selectedCommand = parent
	m.command = parent
	m.qCursor = len([]rune(parent))
	m.keyboardSelectedCommand = ""
	m.commandPreview = ""
	m.inputSuggestIndex = 0
	m.notice = "removed " + rel
	return m, nil
}

// dropRemovedPath forgets every rel at or under the removed path: its tabs,
// remembered cursors, and stashed drafts. If the open file was among them the
// editor resets to the empty query state (a buffer over a deleted file would
// silently resurrect it on the next save).
func (m Model) dropRemovedPath(rel string) Model {
	prefix := rel + "/"
	affected := func(p string) bool { return p == rel || strings.HasPrefix(p, prefix) }

	// The recent-visit records cover files that may never have been tabs this
	// session, so prune the store by path, not by tab list.
	_ = m.db.DeleteOpenedUnder(rel)

	kept := make([]string, 0, len(m.tabs))
	for _, t := range m.tabs {
		if affected(t) {
			delete(m.cursorMem, t)
			delete(m.draftSet, t)
			_ = m.db.DeleteDraft(t)
			continue
		}
		kept = append(kept, t)
	}
	m.tabs = kept
	m.tabActive = input.Clamp(m.tabActive, 0, max(0, len(m.tabs)-1))
	if affected(m.openRel) {
		m.openFile = nil
		m.openRel = ""
		m.fileLines, m.hlLines = nil, nil
		m.edit = newEditor("")
		m.mode = modeQuery
	} else {
		for i, t := range m.tabs {
			if t == m.openRel {
				m.tabActive = i
			}
		}
	}
	m.persistTabs()
	return m
}

// adoptPreview promotes a navigated preview into the editable command so the
// next keystroke continues from the highlighted value.
func (m Model) adoptPreview() Model {
	if m.commandPreview != "" {
		m.command = m.commandPreview
		m.qCursor = len([]rune(m.commandPreview))
		m.commandPreview = ""
	}
	return m
}

// moveInputSuggestion moves the popup selection (wrapping) and syncs all three
// surfaces: popup row, sidebar highlight, input-bar preview.
func (m Model) moveInputSuggestion(suggestions []filetree.InputSuggestion, direction int) Model {
	n := len(suggestions)
	if n == 0 {
		return m
	}
	m.inputSuggestIndex = ((m.inputSuggestIndex+direction)%n + n) % n
	s := suggestions[m.inputSuggestIndex]
	m.keyboardSelectedCommand = s.Entry.CommandValue
	m.commandPreview = s.InsertText
	return m
}

// moveSidebarSelection walks the sidebar highlight row-by-row (Shift+↑/↓ with
// the popup closed). Highlight + preview only — never expands.
func (m Model) moveSidebarSelection(entries []filetree.FileTreeEntry, direction int) Model {
	current := m.highlightedEntryIndex(entries)
	next := filetree.ResolveNextFileTreeSelectionIndex(entries, current, direction)
	if next >= 0 {
		m.keyboardSelectedCommand = entries[next].CommandValue
		m.commandPreview = entries[next].CommandValue
	}
	return m
}

// moveQueryToParentDirectory (Esc) drops the last path segment and confirms
// the parent, collapsing the tree accordingly.
func (m Model) moveQueryToParentDirectory() Model {
	source := m.command
	if strings.TrimSpace(source) == "" {
		source = m.selectedCommand
	}
	parent, ok := filetree.ResolveParentDirectoryCommand(source)
	if !ok {
		return m
	}
	m.keyboardSelectedCommand = ""
	m.commandPreview = ""
	m.selectedCommand = parent
	m.command = parent
	m.qCursor = len([]rune(parent))
	return m
}

// submitQuery acts on Enter: ":" runs an editor command, a directory is
// entered (confirming expansion), a file opens straight into edit mode. The
// target resolves from the selected suggestion when the popup is open, else
// from the sidebar highlight.
func (m Model) submitQuery(entries []filetree.FileTreeEntry, suggestions []filetree.InputSuggestion) (tea.Model, tea.Cmd) {
	m.commandPreview = ""
	trimmed := strings.TrimSpace(m.command)

	// Inline fs commands ride on the typed path prefix: "src/acp/ :mkdir sub",
	// ":touch a/b.go" (no prefix = root), "src/acp/old :rm". Checked before
	// the generic ":" branch so a root-level ":mkdir x" doesn't land in
	// executeCommand.
	if verb, rel, ok := parseInlineFs(trimmed); ok {
		if verb == "rm" {
			return m.queryRemove(rel)
		}
		return m.queryCreate(verb, rel)
	}

	if strings.HasPrefix(trimmed, ":") {
		m.command, m.qCursor = "", 0
		m.cmdPrevMode = modeQuery
		return m.executeCommand(strings.TrimSpace(strings.TrimPrefix(trimmed, ":")))
	}

	var target *filetree.FileTreeEntry
	if len(suggestions) > 0 {
		s := suggestions[input.Clamp(m.inputSuggestIndex, 0, len(suggestions)-1)]
		entry := s.Entry
		target = &entry
	} else if trimmed != "" || m.keyboardSelectedCommand != "" || m.selectedCommand != "" {
		if idx := m.highlightedEntryIndex(entries); idx >= 0 {
			entry := entries[idx]
			target = &entry
		}
	}
	if target == nil {
		return m, nil
	}

	m.keyboardSelectedCommand = ""
	m.inputSuggestIndex = 0

	if target.Type == "directory" {
		// Enter the directory: confirming it is what drives expansion.
		m.selectedCommand = target.CommandValue
		m.command = target.CommandValue
		m.qCursor = len([]rune(m.command))
		return m, nil
	}

	m.command, m.qCursor = "", 0
	return m.openFileAt(target.RelativePath), nil
}

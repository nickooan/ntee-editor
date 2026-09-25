package app

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/input"
)

// queryInputSuggestions completes the typed bar text: exact/prefix over the
// visible tree, fuzzy over the full corpus. Memoized per message via
// frameCache (like treeEntries) so the key handler and View share one filter
// pass over the corpus.
func (m Model) queryInputSuggestions(entries []filetree.FileTreeEntry) []filetree.InputSuggestion {
	// The single choke point for the popup: while a mouse click owns the bar
	// text every consumer (render, navigation, Enter) sees "no suggestions",
	// and nothing is memoized so lifting the flag recomputes cleanly.
	if m.suppressQuerySuggestions {
		return nil
	}
	f := m.frames
	sugKey := m.activeRepo + "\x00" + m.command
	if f != nil && f.sugOk && f.sugSeq == f.seq && f.sugKey == sugKey {
		return f.suggestions
	}
	// Reads the cached corpus and its precomputed fuzzy data (populated by
	// ensureCorpus in the key handler); never walks or re-prepares here.
	// A selected git repo limits the popup to that repo; Ctrl+P and Ctrl+G
	// keep using the full workspace corpus.
	files, dirs, prepared := m.corpus, m.dirCorpus, m.queryPrepared
	visible := entries
	if m.activeRepo != "" && !m.gitRepo {
		files, dirs, prepared = m.scopedFiles, m.scopedDirs, m.scopedPrepared
		visible = entriesUnderRepo(entries, m.activeRepo)
	}
	suggestions := filetree.BuildInputSuggestions(visible, files, dirs, prepared, m.command, filetree.MaxInputSuggestions)
	if f != nil {
		f.sugOk, f.sugSeq, f.sugKey, f.suggestions = true, f.seq, sugKey, suggestions
	}
	return suggestions
}

// entriesUnderRepo keeps sidebar rows that sit at or inside repo so the query
// popup's exact/prefix stage cannot offer another repo's files. The tree
// itself is unfiltered — this slice is only for suggestions.
func entriesUnderRepo(entries []filetree.FileTreeEntry, repo string) []filetree.FileTreeEntry {
	if repo == "" {
		return entries
	}
	out := make([]filetree.FileTreeEntry, 0, len(entries))
	for _, entry := range entries {
		if pathUnderRepo(entry.RelativePath, repo) {
			out = append(out, entry)
		}
	}
	return out
}

// handleQueryKey is the home-mode handler: the bottom input bar drives the
// sidebar (typing expands, navigation highlights) and Enter enters/opens.
// ensureCorpus's rebuild cmd is batched at this single return point so no
// dispatch branch can drop it — a dropped cmd would latch corpusRebuilding
// and freeze the search index for the rest of the session.
func (m Model) handleQueryKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m, corpusCmd := m.ensureCorpus()
	next, cmd := m.dispatchQueryKey(msg)
	return next, tea.Batch(corpusCmd, cmd)
}

// dispatchQueryKey routes one query-bar keypress to its branch.
func (m Model) dispatchQueryKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	entries := m.treeEntries()
	// Computed only by the branches that read the popup (navigation, enter):
	// the typing branches change m.command, so a filter pass for the pre-key
	// text would be wasted — View computes (and memoizes) the fresh one.
	suggest := func() []filetree.InputSuggestion {
		suggestions := m.queryInputSuggestions(entries)
		if m.inputSuggestIndex >= len(suggestions) {
			m.inputSuggestIndex = 0
		}
		return suggestions
	}

	switch msg.String() {
	case "shift+up":
		if suggestions := suggest(); len(suggestions) > 0 {
			return m.moveInputSuggestion(suggestions, -1), nil
		}
		return m.moveSidebarSelection(entries, -1), nil
	case "shift+down":
		if suggestions := suggest(); len(suggestions) > 0 {
			return m.moveInputSuggestion(suggestions, 1), nil
		}
		return m.moveSidebarSelection(entries, 1), nil

	case "up":
		if suggestions := suggest(); len(suggestions) > 0 {
			return m.moveInputSuggestion(suggestions, -1), nil
		}
		m.fileScrollY = input.Clamp(m.fileScrollY-1, 0, max(0, len(m.fileLines)-1))
	case "down":
		if suggestions := suggest(); len(suggestions) > 0 {
			return m.moveInputSuggestion(suggestions, 1), nil
		}
		m.fileScrollY = input.Clamp(m.fileScrollY+1, 0, max(0, len(m.fileLines)-1))
	case "pgup":
		m.fileScrollY = input.Clamp(m.fileScrollY-m.contentHeight(), 0, max(0, len(m.fileLines)-1))
	case "pgdown":
		m.fileScrollY = input.Clamp(m.fileScrollY+m.contentHeight(), 0, max(0, len(m.fileLines)-1))
	case "left":
		m.fileScrollX = max(0, m.fileScrollX-4)
	case "right":
		m.fileScrollX += 4

	case "shift+left":
		m = m.adoptPreview()
		m.qCursor = input.MoveCursor(m.command, m.qCursor, -1)
	case "shift+right":
		m = m.adoptPreview()
		m.qCursor = input.MoveCursor(m.command, m.qCursor, 1)

	case "enter":
		return m.submitQuery(entries, suggest())

	case "esc":
		return m.moveQueryToParentDirectory(), nil

	case "tab":
		if m.openFile != nil {
			if m.isReadOnlyPath(m.openRel) {
				m.errText = "read-only: outside repo " + m.activeRepo
				break
			}
			m = m.beginEditSession(m.openFile.Content)
			m.mode = modeEdit
		}

	case "ctrl+f":
		// In-file search over the viewed file. Works in the read-only pane too
		// (replace is refused there, in the search handler).
		if m.openFile != nil {
			m = m.flushBurst()
			return m.enterSearch(modeQuery, m.openFile.Content), nil
		}

	case "ctrl+o":
		// Walk back along the jump trail even when the last jump landed in a
		// read-only file (the trail survives view-pane navigation).
		return m.jumpBack()

	case "backspace":
		m = m.adoptPreview()
		m.command, m.qCursor, _ = input.RemoveBeforeCursor(m.command, m.qCursor)
		m.inputSuggestIndex = 0
		m.keyboardSelectedCommand = "" // typing re-anchors the highlight to the text
		m.suppressQuerySuggestions = false
	case "space":
		m = m.adoptPreview()
		m.command, m.qCursor = input.InsertAtCursor(m.command, m.qCursor, " ")
		m.inputSuggestIndex = 0
		m.keyboardSelectedCommand = ""
		m.suppressQuerySuggestions = false
	default:
		if t := keyText(msg); t != "" {
			m = m.adoptPreview()
			m.command, m.qCursor = input.InsertAtCursor(m.command, m.qCursor, t)
			m.inputSuggestIndex = 0
			m.keyboardSelectedCommand = ""
			m.suppressQuerySuggestions = false
		}
	}
	return m, nil
}

// isInlineFsVerb is the bar's filesystem-command verb set — the single
// definition parseInlineFs and inlineFsPathPrefix share.
func isInlineFsVerb(verb string) bool {
	return verb == "mkdir" || verb == "touch" || verb == "rm"
}

// inlineFsPathPrefix returns the path part of a suffix-form inline command
// ("<base> :verb …"), so the sidebar can keep treating the typed path as its
// target while the command is still being typed — without it, the unmatched
// " :rm" tail makes the highlight fall back to an ancestor directory. ok is
// false for anything that is not the suffix form of a known verb.
func inlineFsPathPrefix(trimmed string) (base string, ok bool) {
	i := strings.Index(trimmed, " :")
	if i == -1 {
		return "", false
	}
	verb, _, _ := strings.Cut(trimmed[i+2:], " ")
	if !isInlineFsVerb(verb) {
		return "", false
	}
	return strings.TrimSpace(trimmed[:i]), true
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
	if !isInlineFsVerb(verb) {
		return "", "", false
	}
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
		m.invalidateTreeEntries()
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
	m.invalidateTreeEntries()
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

// armRemoveConfirm stats the :rm target and opens the confirmation modal —
// deletion is irreversible (no trash, and dropRemovedPath also forgets tabs,
// drafts, and cursor memory), so a single Enter never deletes directly.
func (m Model) armRemoveConfirm(rel string) (tea.Model, tea.Cmd) {
	info, err := os.Stat(filepath.Join(m.root, filepath.FromSlash(rel)))
	if err != nil {
		m.errText = "rm: no such path: " + rel
		return m, nil
	}
	m.confirmRm = rel
	m.confirmRmDir = info.IsDir()
	return m, nil
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
	m.invalidateTreeEntries()
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
// the popup closed). Highlight + preview only — never expands. When a repo is
// selected the walk stops at its edge; a highlight that already drifted outside
// (mouse click, Ctrl+P open) snaps back to the repo root on the first press.
func (m Model) moveSidebarSelection(entries []filetree.FileTreeEntry, direction int) Model {
	current := m.highlightedEntryIndex(entries)
	if current >= 0 && m.isReadOnlyPath(entries[current].RelativePath) {
		if root := findEntryIndex(entries, m.activeRepo); root >= 0 {
			m.keyboardSelectedCommand = entries[root].CommandValue
			m.commandPreview = entries[root].CommandValue
			return m
		}
	}
	next := filetree.ResolveNextFileTreeSelectionIndex(entries, current, direction)
	if next < 0 {
		return m
	}
	if m.isReadOnlyPath(entries[next].RelativePath) {
		m.errText = "cannot select outside repo " + m.activeRepo
		return m
	}
	m.keyboardSelectedCommand = entries[next].CommandValue
	m.commandPreview = entries[next].CommandValue
	return m
}

// moveQueryToParentDirectory (Esc) drops the last path segment and confirms
// the parent, collapsing the tree accordingly. At the top edge of a selected
// repo it stops rather than stepping outside; from a highlight that already
// drifted outside it returns to the repo root.
func (m Model) moveQueryToParentDirectory() Model {
	source := m.command
	if strings.TrimSpace(source) == "" {
		source = m.selectedCommand
	}
	parent, ok := filetree.ResolveParentDirectoryCommand(source)
	if !ok {
		return m
	}
	if m.isReadOnlyPath(parent) {
		if !m.isReadOnlyPath(source) {
			// At the repo's top edge: stop rather than step outside.
			m.errText = "cannot go outside repo " + m.activeRepo
			return m
		}
		// The highlight had drifted outside the repo: come back to its root.
		root := m.repoRootCommand()
		m.keyboardSelectedCommand = ""
		m.commandPreview = ""
		m.selectedCommand = root
		m.command = root
		m.qCursor = len([]rune(root))
		m.suppressQuerySuggestions = false
		return m
	}
	m.keyboardSelectedCommand = ""
	m.commandPreview = ""
	m.selectedCommand = parent
	m.command = parent
	m.qCursor = len([]rune(parent))
	m.suppressQuerySuggestions = false
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
		if m.activeRepo != "" && !m.gitRepo && !pathUnderRepo(rel, m.activeRepo) {
			m.errText = "path is outside the current repo"
			return m, nil
		}
		if verb == "rm" {
			return m.armRemoveConfirm(rel)
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
	if m.activeRepo != "" && !m.gitRepo && trimmed != "" && !pathUnderRepo(target.RelativePath, m.activeRepo) {
		m.errText = "path is outside the current repo"
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

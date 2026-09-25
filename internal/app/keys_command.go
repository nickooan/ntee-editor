package app

import (
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/fuzzy"
	"github.com/nickooan/ntee-editor/internal/input"
)

// enterCommand focuses the bottom : command bar.
func (m Model) enterCommand() Model {
	m.cmdPrevMode = m.mode
	m.cmdInput = ""
	m.cmdCursor = 0
	m.mode = modeCommand
	return m
}

func (m Model) handleCommandKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = m.cmdPrevMode
	case "enter":
		return m.executeCommand(strings.TrimSpace(m.cmdInput))
	case "left":
		m.cmdCursor = input.MoveCursor(m.cmdInput, m.cmdCursor, -1)
	case "right":
		m.cmdCursor = input.MoveCursor(m.cmdInput, m.cmdCursor, 1)
	case "backspace":
		m.cmdInput, m.cmdCursor, _ = input.RemoveBeforeCursor(m.cmdInput, m.cmdCursor)
	case "space":
		m.cmdInput, m.cmdCursor = input.InsertAtCursor(m.cmdInput, m.cmdCursor, " ")
	default:
		if t := keyText(msg); t != "" {
			m.cmdInput, m.cmdCursor = input.InsertAtCursor(m.cmdInput, m.cmdCursor, t)
		}
	}
	return m, nil
}

func (m Model) executeCommand(cmd string) (tea.Model, tea.Cmd) {
	m.mode = m.cmdPrevMode
	if cmd == "" {
		return m, nil
	}
	name, arg, _ := strings.Cut(cmd, " ")
	arg = strings.TrimSpace(arg)

	switch name {
	// Save / quit / open have dedicated keys (Ctrl+S, Ctrl+Q, Ctrl+P and the
	// query bar), so they are intentionally not command-bar verbs.
	case "jump", "jp":
		if m.cmdPrevMode == modeEdit {
			idx, ok := parseJumpTarget(arg, len(m.edit.lines))
			if !ok {
				m.errText = "jump needs a line number, top, or end"
				break
			}
			m.edit.clearSelection()
			m.edit.cy = idx
			m.edit.cx = 0
			m = m.anchorCursorLine()
		} else if m.openFile != nil {
			idx, ok := parseJumpTarget(arg, len(m.fileLines))
			if !ok {
				m.errText = "jump needs a line number, top, or end"
				break
			}
			m.fileScrollY = input.Clamp(idx, 0, max(0, len(m.fileLines)-1))
		} else {
			m.errText = "no open file"
		}

	case "revert":
		// Works from any mode with an open file: loads the last "save"
		// snapshot into an edit session (undoable; Ctrl+S writes it).
		if m.openFile == nil {
			m.errText = "no open file"
			break
		}
		if m.isReadOnlyPath(m.openRel) {
			m.errText = "read-only: outside repo " + m.activeRepo
			break
		}
		snap, ok := m.db.LastSave(m.openRel)
		if !ok {
			m.errText = "no saved snapshot to revert to"
			break
		}
		if m.cmdPrevMode != modeEdit {
			m = m.beginEditSession(m.openFile.Content)
		}
		m.edit = newEditor(snap.Content)
		m.edit.dirty = snap.Content != m.openFile.Content
		m.snapDirty = true
		m = m.pushSnapshot("edit") // the revert itself is undoable
		m.mode = modeEdit
		m.notice = "reverted to last save — Ctrl+S to write"
		m = m.refreshFileHighlights()

	case "tab":
		// Name-jump lands in edit mode via openFileAt (superseding the
		// restored cmdPrevMode); close verbs just prune the tab list.
		m, _ = m.tabCommand(arg)

	case "refresh":
		// Force a full rebuild now, dropping the mtime-keyed dir cache so even
		// same-second external changes are picked up.
		filetree.ClearDirCache()
		m.corpusRebuilding = true
		m, gitCmd := m.maybeGitRefresh()
		return m, tea.Batch(m.rebuildCorpusCmd(), gitCmd)

	default:
		m.errText = "unknown command :" + name
	}
	return m, nil
}

// The finder's two variants share one overlay; the prompt is what tells them
// apart (and is matched by refreshFuzzyCandidates when a rebuild lands).
const (
	fuzzyPromptGoto        = "goto "
	fuzzyPromptUncommitted = "uncommitted "
	fuzzyPromptRepo        = "Repo: "
)

// fuzzyGotoCandidates builds the Ctrl+P list: recents first, then the rest of
// the corpus, directories last so an empty query stays a file list.
func (m Model) fuzzyGotoCandidates() []string {
	corpus := m.corpus
	inCorpus := make(map[string]int, len(corpus))
	for i, rel := range corpus {
		inCorpus[rel] = i
	}
	ordered := make([]string, 0, len(corpus))
	used := make(map[string]bool)
	for _, recent := range m.db.RecentFiles(20) {
		if _, ok := inCorpus[recent.Path]; ok && !used[recent.Path] {
			ordered = append(ordered, recent.Path)
			used[recent.Path] = true
		}
	}
	for _, rel := range corpus {
		if !used[rel] {
			ordered = append(ordered, rel)
		}
	}
	return append(ordered, m.dirCorpus...)
}

// fuzzyUncommittedCandidates builds the Ctrl+U list: the corpus ∩ gitDirty
// intersection (dirs, deleted files, and rename origins in the dirty set are
// never in the walk corpus, so only openable files remain).
func (m Model) fuzzyUncommittedCandidates() []string {
	var ordered []string
	for _, rel := range m.corpus {
		if m.gitDirty[rel] {
			ordered = append(ordered, rel)
		}
	}
	return ordered
}

// openFuzzy opens the Ctrl+P finder. The corpus is the full project walk with
// recents moved to the front, so an empty query lists recently opened files.
func (m Model) openFuzzy() (Model, tea.Cmd) {
	m = m.closeCompletion()
	m, cmd := m.ensureCorpus()
	m.fuzzyOpen = true
	m.fuzzyQuery = ""
	m.fuzzyIndex = 0
	m.fuzzyPrompt = fuzzyPromptGoto
	m.fuzzyCorpus = fuzzy.Prepare(m.fuzzyGotoCandidates())
	m.fuzzyMatches = fuzzy.Filter("", m.fuzzyCorpus)
	return m, cmd
}

// openUncommitted opens the Ctrl+U finder: the same fuzzy overlay as Ctrl+P,
// but its corpus is only the files with uncommitted git changes (the corpus ∩
// gitDirty intersection — dirs, deleted files, and rename origins in the dirty
// set are never in the walk corpus, so only openable files remain). A fresh
// status refresh is batched so the set stays honest for the next open.
func (m Model) openUncommitted() (Model, tea.Cmd) {
	if !m.gitStatusEnabled() {
		m.errText = "not a git repository"
		return m, nil
	}
	if m.corpusBuiltAt.IsZero() {
		// The corpus ∩ gitDirty intersection is empty until the index lands.
		m, cmd := m.ensureCorpus() // make sure the build is in flight
		m.errText = "index building — try again shortly"
		return m, cmd
	}
	m = m.closeCompletion()
	m, cmd := m.ensureCorpus()
	ordered := m.fuzzyUncommittedCandidates()
	if len(ordered) == 0 {
		m.notice = "no uncommitted files"
		m, gitCmd := m.maybeGitRefresh()
		return m, tea.Batch(cmd, gitCmd)
	}

	m.fuzzyOpen = true
	m.fuzzyQuery = ""
	m.fuzzyIndex = 0
	m.fuzzyPrompt = fuzzyPromptUncommitted
	m.fuzzyCorpus = fuzzy.Prepare(ordered)
	m.fuzzyMatches = fuzzy.Filter("", m.fuzzyCorpus)
	m, gitCmd := m.maybeGitRefresh()
	return m, tea.Batch(cmd, gitCmd)
}

// refreshFuzzyCandidates rebuilds an open finder's candidate list from the
// current corpus (and gitDirty for the uncommitted variant) when either input
// changes underneath it, preserving the typed filter and keeping the selection
// on the same file when it survives the refresh.
func (m Model) refreshFuzzyCandidates() Model {
	if !m.fuzzyOpen {
		return m
	}
	selected := m.fuzzySelectedPath()
	previousIndex := m.fuzzyIndex
	var ordered []string
	switch m.fuzzyPrompt {
	case fuzzyPromptUncommitted:
		ordered = m.fuzzyUncommittedCandidates()
	case fuzzyPromptRepo:
		ordered, m.repoRels = m.repoPickerCandidates()
	default:
		ordered = m.fuzzyGotoCandidates()
	}
	m.fuzzyCorpus = fuzzy.Prepare(ordered)
	m = m.refreshFuzzy() // re-filters the preserved fuzzyQuery, resets the index
	for i, match := range m.fuzzyMatches {
		if m.fuzzyCorpus[match.Index].Text == selected {
			m.fuzzyIndex = i
			return m
		}
	}
	m.fuzzyIndex = input.Clamp(previousIndex, 0, max(0, len(m.fuzzyMatches)-1))
	return m
}

// closeFuzzy hides the finder and releases the prepared corpus. That slice can
// be a few MB on a large workspace; there is no reason to keep it resident
// between opens, so drop it and let openFuzzy rebuild on demand.
func (m Model) closeFuzzy() Model {
	m.fuzzyOpen = false
	m.fuzzyCorpus = nil
	m.fuzzyMatches = nil
	m.fuzzyQuery = ""
	m.fuzzyIndex = 0
	m.fuzzyPrompt = ""
	m.repoRels = nil
	return m
}

// workspaceLabel is the Ctrl+W row that selects the whole opened directory,
// shown as its base name with a trailing slash.
func (m Model) workspaceLabel() string {
	return filepath.Base(m.root) + "/"
}

// repoPickerCandidates is the Ctrl+W list: the workspace itself first, then
// each nested git repo's root-relative directory. rels[i] is "" for the
// workspace row and the repo path for the others.
func (m Model) repoPickerCandidates() (labels, rels []string) {
	labels = append(labels, m.workspaceLabel())
	rels = append(rels, "")
	for _, rel := range m.workspaceRepos {
		labels = append(labels, rel+"/")
		rels = append(rels, rel)
	}
	return labels, rels
}

// openRepoPicker opens the Ctrl+W repo list. The opened directory has to be a
// workspace — a directory that is not itself a git repository. The list is
// repo roots only (plus the workspace row), filtered like Ctrl+P.
func (m Model) openRepoPicker() (Model, tea.Cmd) {
	if m.gitRepo {
		m.errText = "not a workspace directory"
		return m, nil
	}
	m = m.closeCompletion()
	labels, rels := m.repoPickerCandidates()
	m.fuzzyOpen = true
	m.fuzzyQuery = ""
	m.fuzzyIndex = 0
	m.fuzzyPrompt = fuzzyPromptRepo
	m.repoRels = rels
	m.fuzzyCorpus = fuzzy.Prepare(labels)
	m.fuzzyMatches = fuzzy.Filter("", m.fuzzyCorpus)
	return m, nil
}

// selectWorkspaceRepo remembers the Ctrl+W choice ("" = the whole workspace)
// and refreshes sidebar highlights for that scope. The file tree root stays
// the opened directory.
func (m Model) selectWorkspaceRepo(rel string) (Model, tea.Cmd) {
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	m = m.closeFuzzy()
	if rel == m.activeRepo {
		return m, nil
	}
	m.activeRepo = rel
	if rel != "" {
		// Move the sidebar and query bar onto the selected repo's root so the
		// tree expands into it and arrow navigation starts inside its edge.
		root := m.repoRootCommand()
		m.selectedCommand = root
		m.command = root
		m.qCursor = len([]rune(root))
		m.keyboardSelectedCommand = ""
		m.commandPreview = ""
		m.suppressQuerySuggestions = false
	}
	m = m.rebuildQueryScope()
	m.saveSession()
	if rel == "" {
		m.notice = "workspace " + strings.TrimSuffix(m.workspaceLabel(), "/")
	} else {
		m.notice = "repo " + rel
	}
	// A buffer left outside the new scope can no longer be edited: stash its
	// unsaved work and drop it to the view pane.
	if m.openFile != nil && m.mode != modeQuery && m.isReadOnlyPath(m.openRel) {
		m = m.leaveEditForReadOnly()
	}
	// Drop an in-flight scan for the previous scope. Its result carries the
	// old scope and is ignored; this spawn replaces the highlights now.
	m.gitStatusRunning = false
	return m.maybeGitRefresh()
}

func (m Model) handleFuzzyKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+p", "ctrl+u", "ctrl+w":
		if msg.String() == "ctrl+w" && m.fuzzyPrompt != fuzzyPromptRepo {
			return m.openRepoPicker()
		}
		m = m.closeFuzzy()
	case "enter":
		if len(m.fuzzyMatches) == 0 {
			m = m.closeFuzzy()
			break
		}
		idx := input.Clamp(m.fuzzyIndex, 0, len(m.fuzzyMatches)-1)
		matchIndex := m.fuzzyMatches[idx].Index
		if m.fuzzyPrompt == fuzzyPromptRepo {
			rel := ""
			if matchIndex >= 0 && matchIndex < len(m.repoRels) {
				rel = m.repoRels[matchIndex]
			}
			return m.selectWorkspaceRepo(rel)
		}
		rel := m.fuzzyCorpus[matchIndex].Text
		if strings.HasSuffix(rel, "/") {
			// Directory: drill down inside the finder instead of opening.
			m.fuzzyQuery = rel
			m = m.refreshFuzzy()
			break
		}
		m = m.closeFuzzy()
		if m.mode == modeEdit {
			m = m.flushBurst() // keep the abandoned buffer reachable in history
		}
		m = m.openFileAt(rel)
	case "up", "shift+up":
		m.fuzzyIndex = max(0, m.fuzzyIndex-1)
	case "down", "shift+down":
		m.fuzzyIndex = min(max(0, len(m.fuzzyMatches)-1), m.fuzzyIndex+1)
	case "backspace":
		if runes := []rune(m.fuzzyQuery); len(runes) > 0 {
			m.fuzzyQuery = string(runes[:len(runes)-1])
			m = m.refreshFuzzy()
		}
	case "space":
		m.fuzzyQuery += " "
		m = m.refreshFuzzy()
	default:
		if t := keyText(msg); t != "" {
			m.fuzzyQuery += t
			m = m.refreshFuzzy()
		}
	}
	return m, nil
}

// fuzzySelectedPath mirrors the finder's current position in the sidebar:
// the selected candidate, or the drilled-in directory prefix when the
// filter has no matches. Empty when the finder is closed.
func (m Model) fuzzySelectedPath() string {
	if !m.fuzzyOpen {
		return ""
	}
	if len(m.fuzzyMatches) > 0 {
		idx := input.Clamp(m.fuzzyIndex, 0, len(m.fuzzyMatches)-1)
		return m.fuzzyCorpus[m.fuzzyMatches[idx].Index].Text
	}
	// No matches: keep the tree anchored to the query's directory part.
	if i := strings.LastIndex(m.fuzzyQuery, "/"); i >= 0 {
		return m.fuzzyQuery[:i+1]
	}
	return ""
}

func (m Model) refreshFuzzy() Model {
	m.fuzzyMatches = fuzzy.Filter(m.fuzzyQuery, m.fuzzyCorpus)
	// Repo rows are directories the user selects, not drills into, so an
	// exact "repo/" query must stay in the list. The goto finder drops that
	// row: Enter-on-a-directory sets the query to the dir, and leaving it
	// ranked first would loop.
	if m.fuzzyPrompt == fuzzyPromptRepo {
		m.fuzzyIndex = 0
		return m
	}
	if q := strings.ToLower(m.fuzzyQuery); strings.HasSuffix(q, "/") {
		kept := m.fuzzyMatches[:0]
		for _, match := range m.fuzzyMatches {
			if strings.ToLower(m.fuzzyCorpus[match.Index].Text) == q {
				continue
			}
			kept = append(kept, match)
		}
		m.fuzzyMatches = kept
	}
	m.fuzzyIndex = 0
	return m
}

package app

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

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

func (m Model) handleCommandKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.mode = m.cmdPrevMode
	case tea.KeyEnter:
		return m.executeCommand(strings.TrimSpace(m.cmdInput))
	case tea.KeyLeft:
		m.cmdCursor = input.MoveCursor(m.cmdInput, m.cmdCursor, -1)
	case tea.KeyRight:
		m.cmdCursor = input.MoveCursor(m.cmdInput, m.cmdCursor, 1)
	case tea.KeyBackspace:
		m.cmdInput, m.cmdCursor, _ = input.RemoveBeforeCursor(m.cmdInput, m.cmdCursor)
	case tea.KeySpace:
		m.cmdInput, m.cmdCursor = input.InsertAtCursor(m.cmdInput, m.cmdCursor, " ")
	case tea.KeyRunes:
		m.cmdInput, m.cmdCursor = input.InsertAtCursor(m.cmdInput, m.cmdCursor, string(msg.Runes))
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

	case "recent":
		return m.openFuzzy()

	case "refresh":
		// Force a full rebuild now, dropping the mtime-keyed dir cache so even
		// same-second external changes are picked up.
		filetree.ClearDirCache()
		m.corpusRebuilding = true
		if m.gitRepo {
			return m, tea.Batch(m.rebuildCorpusCmd(), m.refreshGitStatusCmd())
		}
		return m, m.rebuildCorpusCmd()

	default:
		m.errText = "unknown command :" + name
	}
	return m, nil
}

// openFuzzy opens the Ctrl+P finder. The corpus is the full project walk with
// recents moved to the front, so an empty query lists recently opened files.
func (m Model) openFuzzy() (Model, tea.Cmd) {
	m = m.closeCompletion()
	m, cmd := m.ensureCorpus()
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

	m.fuzzyOpen = true
	m.fuzzyQuery = ""
	m.fuzzyIndex = 0
	m.fuzzyPrompt = "goto "
	m.fuzzyCorpus = fuzzy.Prepare(ordered)
	m.fuzzyMatches = fuzzy.Filter("", m.fuzzyCorpus)
	return m, cmd
}

// openUncommitted opens the Ctrl+U finder: the same fuzzy overlay as Ctrl+P,
// but its corpus is only the files with uncommitted git changes (the corpus ∩
// gitDirty intersection — dirs, deleted files, and rename origins in the dirty
// set are never in the walk corpus, so only openable files remain). A fresh
// status refresh is batched so the set stays honest for the next open.
func (m Model) openUncommitted() (Model, tea.Cmd) {
	if !m.gitRepo {
		m.errText = "not a git repository"
		return m, nil
	}
	m = m.closeCompletion()
	m, cmd := m.ensureCorpus()
	var ordered []string
	for _, rel := range m.corpus {
		if m.gitDirty[rel] {
			ordered = append(ordered, rel)
		}
	}
	if len(ordered) == 0 {
		m.notice = "no uncommitted files"
		return m, tea.Batch(cmd, m.refreshGitStatusCmd())
	}

	m.fuzzyOpen = true
	m.fuzzyQuery = ""
	m.fuzzyIndex = 0
	m.fuzzyPrompt = "uncommitted "
	m.fuzzyCorpus = fuzzy.Prepare(ordered)
	m.fuzzyMatches = fuzzy.Filter("", m.fuzzyCorpus)
	return m, tea.Batch(cmd, m.refreshGitStatusCmd())
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
	return m
}

func (m Model) handleFuzzyKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlP, tea.KeyCtrlU:
		m = m.closeFuzzy()
	case tea.KeyEnter:
		if len(m.fuzzyMatches) == 0 {
			m = m.closeFuzzy()
			break
		}
		idx := input.Clamp(m.fuzzyIndex, 0, len(m.fuzzyMatches)-1)
		rel := m.fuzzyCorpus[m.fuzzyMatches[idx].Index].Text
		m = m.closeFuzzy()
		if m.mode == modeEdit {
			m = m.flushBurst() // keep the abandoned buffer reachable in history
		}
		m = m.openFileAt(rel)
	case tea.KeyUp:
		m.fuzzyIndex = max(0, m.fuzzyIndex-1)
	case tea.KeyDown:
		m.fuzzyIndex = min(max(0, len(m.fuzzyMatches)-1), m.fuzzyIndex+1)
	case tea.KeyBackspace:
		if runes := []rune(m.fuzzyQuery); len(runes) > 0 {
			m.fuzzyQuery = string(runes[:len(runes)-1])
			m = m.refreshFuzzy()
		}
	case tea.KeySpace:
		m.fuzzyQuery += " "
		m = m.refreshFuzzy()
	case tea.KeyRunes:
		m.fuzzyQuery += string(msg.Runes)
		m = m.refreshFuzzy()
	}
	return m, nil
}

func (m Model) refreshFuzzy() Model {
	m.fuzzyMatches = fuzzy.Filter(m.fuzzyQuery, m.fuzzyCorpus)
	m.fuzzyIndex = 0
	return m
}

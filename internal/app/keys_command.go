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

	case "recent":
		m = m.openFuzzy()

	default:
		m.errText = "unknown command :" + name
	}
	return m, nil
}

// openFuzzy opens the Ctrl+P finder. The corpus is the full project walk with
// recents moved to the front, so an empty query lists recently opened files.
func (m Model) openFuzzy() Model {
	m = m.closeCompletion()
	corpus := filetree.BuildAllEntries(m.root, m.cfg.Tree.Ignore, m.gitignore)
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
	m.fuzzyCorpus = ordered
	m.fuzzyMatches = fuzzy.Filter("", ordered)
	return m
}

func (m Model) handleFuzzyKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlP:
		m.fuzzyOpen = false
	case tea.KeyEnter:
		m.fuzzyOpen = false
		if len(m.fuzzyMatches) == 0 {
			break
		}
		idx := input.Clamp(m.fuzzyIndex, 0, len(m.fuzzyMatches)-1)
		rel := m.fuzzyCorpus[m.fuzzyMatches[idx].Index]
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

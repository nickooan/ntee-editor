package app

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/input"
)

// queryInputSuggestions completes the typed bar text: exact/prefix over the
// visible tree, fuzzy over the full corpus.
func (m Model) queryInputSuggestions(entries []filetree.FileTreeEntry) []filetree.InputSuggestion {
	all := filetree.BuildAllEntries(m.root, m.cfg.Tree.Ignore, m.gitignore)
	return filetree.BuildInputSuggestions(entries, all, m.command, filetree.MaxInputSuggestions)
}

// handleQueryKey is the home-mode handler: the bottom input bar drives the
// sidebar (typing expands, navigation highlights) and Enter enters/opens.
func (m Model) handleQueryKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
	return m, nil
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

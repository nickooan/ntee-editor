package app

import (
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/syntax"
	"github.com/nickooan/ntee-editor/internal/view"
)

// enterSearch opens the in-file search over a frozen copy of content,
// remembering the mode to return to. The frozen content is tokenized so the
// search view keeps syntax colors (same size gate as the file pane).
func (m Model) enterSearch(prev mode, content string) Model {
	m.searchPrevMode = prev
	m.searchContent = content
	m.searchInput = ""
	m.searchFocused = 0
	m.searchHl = nil
	if m.openFile != nil {
		if kb := m.cfg.Editor.MaxHighlightKB; kb <= 0 || len(content) <= kb*1024 {
			m.searchHl = syntax.HighlightLines(m.openFile.FileName, content)
		}
	}
	m.mode = modeSearch
	return m
}

func (m Model) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.mode = m.searchPrevMode
	case tea.KeyEnter:
		m = m.acceptSearch()
	case tea.KeyUp:
		m = m.nextMatch(-1)
	case tea.KeyDown, tea.KeyCtrlF:
		m = m.nextMatch(1)
	case tea.KeyBackspace:
		if runes := []rune(m.searchInput); len(runes) > 0 {
			m.searchInput = string(runes[:len(runes)-1])
			m.searchFocused = 0
		}
	case tea.KeySpace:
		m.searchInput += " "
		m.searchFocused = 0
	case tea.KeyRunes:
		m.searchInput += string(msg.Runes)
		m.searchFocused = 0
	}
	return m, nil
}

// nextMatch cycles the focused match (wrapping) in either direction.
func (m Model) nextMatch(direction int) Model {
	matches := view.FindSearchMatches(m.searchContent, m.searchInput)
	if len(matches) == 0 {
		return m
	}
	n := len(matches)
	m.searchFocused = ((m.searchFocused+direction)%n + n) % n
	return m
}

// acceptSearch commits the focused match: in edit mode the cursor jumps onto
// it (byte offset → rune column — the single offset bridge); in view mode the
// match line is centered.
func (m Model) acceptSearch() Model {
	matches := view.FindSearchMatches(m.searchContent, m.searchInput)
	m.mode = m.searchPrevMode
	if len(matches) == 0 || m.searchFocused >= len(matches) {
		return m
	}
	mt := matches[m.searchFocused]
	if m.searchPrevMode == modeEdit {
		m.edit.clearSelection()
		m.edit.cy = input.Clamp(mt.LineIndex, 0, len(m.edit.lines)-1)
		line := m.edit.lines[m.edit.cy]
		m.edit.cx = utf8.RuneCountInString(line[:clampByte(mt.Start, len(line))])
		return m.anchorCursorLine()
	}
	m.fileScrollY = anchorScroll(mt.LineIndex, m.contentHeight(), len(m.fileLines))
	return m
}

// anchorScroll positions line ~30% from the top of a pane of the given height.
func anchorScroll(line, height, totalLines int) int {
	return input.Clamp(line-height*3/10, 0, max(0, totalLines-1))
}

// anchorCursorLine scrolls the edit pane so the cursor line sits ~30% from
// the top — a landed match or jump target should never hug the bottom edge.
func (m Model) anchorCursorLine() Model {
	m.fileScrollY = anchorScroll(m.edit.cy, m.contentHeight(), len(m.edit.lines))
	return m
}

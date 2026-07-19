package app

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/input"
)

// enterExec focuses the bottom "@exec >" editor-command bar. The editor is
// paused, not reset: m.edit (cursor + selection) is left untouched so the
// selection stays visible behind the bar. Only entered from edit mode.
func (m Model) enterExec() Model {
	m.execPrevMode = m.mode
	m.execInput = ""
	m.execCursor = 0
	m.mode = modeExec
	return m
}

// handleExecKey drives the @exec command bar. Text editing mirrors the : command
// bar (handleCommandKey); Enter runs the typed editor command.
func (m Model) handleExecKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.mode = m.execPrevMode
	case tea.KeyEnter:
		return m.runExecCommand(strings.TrimSpace(m.execInput))
	case tea.KeyLeft:
		m.execCursor = input.MoveCursor(m.execInput, m.execCursor, -1)
	case tea.KeyRight:
		m.execCursor = input.MoveCursor(m.execInput, m.execCursor, 1)
	case tea.KeyBackspace:
		m.execInput, m.execCursor, _ = input.RemoveBeforeCursor(m.execInput, m.execCursor)
	case tea.KeySpace:
		m.execInput, m.execCursor = input.InsertAtCursor(m.execInput, m.execCursor, " ")
	case tea.KeyRunes:
		m.execInput, m.execCursor = input.InsertAtCursor(m.execInput, m.execCursor, string(msg.Runes))
	}
	return m, nil
}

// runExecCommand dispatches an editor command typed into the @exec bar. It
// splits "name [arg]" so future commands (jump <line>, …) slot in as new cases.
// On success it returns to the previous mode; on error it stays so the user can
// correct the input.
func (m Model) runExecCommand(cmd string) (tea.Model, tea.Cmd) {
	name, _, _ := strings.Cut(cmd, " ")

	switch name {
	case "":
		m.mode = m.execPrevMode

	case "copy":
		text := m.edit.selectionText()
		if text == "" {
			m.errText = "nothing selected"
			return m, nil
		}
		if err := m.copyClipboard(text); err != nil {
			m.errText = "copy failed: " + err.Error()
			return m, nil
		}
		m.notice = "copied"
		m.mode = m.execPrevMode

	default:
		m.errText = "unknown command: " + name
	}
	return m, nil
}

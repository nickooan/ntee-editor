package app

import (
	"strconv"
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
	name, arg, _ := strings.Cut(cmd, " ")
	arg = strings.TrimSpace(arg)

	switch name {
	case "":
		m.mode = m.execPrevMode
	case "copy", "cp":
		m = m.execCopy(arg)
	case "jump", "jp":
		m = m.execJump(arg)
	default:
		m.errText = "unknown command: " + name
	}
	return m, nil
}

// execCopy copies to the clipboard by argument: the selection (no arg), a line
// range ("a" / "a-b", 1-based inclusive), "all" (whole buffer), or "fpath" (the
// file's root-relative path). On success it flashes "copied" and returns to edit
// mode; on error it stays in exec mode so the user can correct the input.
func (m Model) execCopy(arg string) Model {
	var text string
	switch {
	case arg == "":
		text = m.edit.selectionText()
		if text == "" {
			m.errText = "nothing selected"
			return m
		}
	case arg == "all":
		text = m.edit.content() + "\n"
	case arg == "fpath":
		text = m.openRel
	default:
		lo, hi, ok := parseLineRange(arg, len(m.edit.lines))
		if !ok {
			m.errText = "copy: bad range: " + arg
			return m
		}
		text = strings.Join(m.edit.lines[lo:hi+1], "\n") + "\n"
	}
	if err := m.copyClipboard(text); err != nil {
		m.errText = "copy failed: " + err.Error()
		return m
	}
	m.notice = "copied"
	m.mode = m.execPrevMode
	return m
}

// execJump moves the cursor to a target line and scrolls it ~30% from the top
// (via anchorCursorLine, shared with search/jump/grep), then returns to edit
// mode. The target is a 1-based line number, "top" (first line), or "end" (last
// line). Anything else stays in exec mode with an error.
func (m Model) execJump(arg string) Model {
	var line int
	switch arg {
	case "top":
		line = 1
	case "end":
		line = len(m.edit.lines)
	default:
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 {
			m.errText = "jump needs a line number, top, or end"
			return m
		}
		line = n
	}
	m.edit.clearSelection()
	m.edit.cy = input.Clamp(line-1, 0, len(m.edit.lines)-1)
	m.edit.cx = 0
	m.edit.clampCursor()
	m = m.anchorCursorLine()
	m.mode = m.execPrevMode
	return m
}

// parseLineRange parses "a" or "a-b" (1-based, inclusive) into 0-based [lo,hi]
// clamped to the buffer, swapping a reversed range. ok is false on malformed
// input.
func parseLineRange(arg string, total int) (lo, hi int, ok bool) {
	a, b, hasDash := strings.Cut(arg, "-")
	start, err := strconv.Atoi(strings.TrimSpace(a))
	if err != nil || start < 1 {
		return 0, 0, false
	}
	end := start
	if hasDash {
		end, err = strconv.Atoi(strings.TrimSpace(b))
		if err != nil || end < 1 {
			return 0, 0, false
		}
	}
	if start > end {
		start, end = end, start
	}
	return input.Clamp(start-1, 0, total-1), input.Clamp(end-1, 0, total-1), true
}

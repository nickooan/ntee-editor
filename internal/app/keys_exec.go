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
	case "git":
		m = m.execGit(arg)
	case "tab":
		next, ok := m.tabCommand(arg)
		if ok && next.mode == modeExec {
			// Close verbs don't change mode; a name-jump already landed in
			// edit via openFileAt. Restore edit mode for the close verbs.
			next.mode = next.execPrevMode
		}
		m = next // on error: stays in exec with errText set
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

// execGit dispatches the "git" namespace of editor commands. Today the only
// subcommand is "scf" (solve conflict); it splits "scf <side>" and delegates.
// Errors stay in exec mode (no execPrevMode restore) so the user can correct.
func (m Model) execGit(arg string) Model {
	sub, rest, _ := strings.Cut(arg, " ")
	rest = strings.TrimSpace(rest)
	switch sub {
	case "scf":
		return m.execSolveConflict(rest)
	case "":
		m.errText = "git needs a subcommand (scf)"
	default:
		m.errText = "unknown git command: " + sub
	}
	return m
}

// execSolveConflict resolves the git conflict block(s) touching the current
// line-wise selection (or the cursor line when nothing is selected), keeping the
// side whose marker label matches target (case-insensitive, e.g. "head" or a
// branch name) and deleting the markers and the losing side. The whole resolve
// is one undoable snapshot. On any error it edits nothing and stays in exec mode.
func (m Model) execSolveConflict(target string) Model {
	if target == "" {
		m.errText = "git scf needs a side (e.g. head or a branch name)"
		return m
	}
	blocks := findConflictBlocks(m.edit.lines)
	if len(blocks) == 0 {
		m.errText = "no conflict markers in this file"
		return m
	}

	// Region: the line-wise selection, else just the cursor line.
	lo, hi := m.edit.cy, m.edit.cy
	selecting := m.edit.selLineMode
	if selecting {
		lo, hi = m.edit.selLineAnchor, m.edit.cy
		if lo > hi {
			lo, hi = hi, lo
		}
	}

	var sel []conflictBlock
	for _, b := range blocks {
		if b.start > hi || b.end < lo {
			continue // block does not touch the region
		}
		// An explicit selection must cover a block end to end; a selection that
		// stops short of a marker is almost certainly a mistake, so refuse it
		// rather than silently resolving lines the user didn't select.
		if selecting && (b.start < lo || b.end > hi) {
			m.errText = "conflict block not fully selected — include <<<<<<< through >>>>>>>"
			return m
		}
		sel = append(sel, b)
	}
	if len(sel) == 0 {
		m.errText = "no conflict block in selection"
		return m
	}

	// Validate the target against every intersecting block before touching the
	// buffer, so a bad label leaves it untouched (no partial resolve).
	keepOurs := make([]bool, len(sel))
	for i, b := range sel {
		ko, ok := matchConflictSide(b, target)
		if !ok {
			m.errText = "git scf: '" + target + "' matches neither side (" +
				b.oursLabel + " | " + b.theirsLabel + ")"
			return m
		}
		keepOurs[i] = ko
	}

	m = m.flushBurst() // checkpoint pending typing as the undo pre-state
	newLines := resolveConflicts(m.edit.lines, sel, keepOurs)
	m.edit.lines = newLines
	m.edit.cy = input.Clamp(sel[0].start, 0, len(newLines)-1)
	m.edit.cx = 0
	m.edit.clearSelection()
	m.edit.clampCursor()
	m.edit.dirty = true
	m.edit.rev++ // buffer changed: force a highlight rescan (see edit.go)
	m.snapDirty = true
	m = m.pushSnapshot("edit") // the resolve is one undoable step
	m = m.refreshFileHighlights()
	m.notice = "resolved " + strconv.Itoa(len(sel)) + " conflict(s) → " + target
	m.mode = m.execPrevMode
	return m
}

// execJump moves the cursor to a target line and scrolls it ~30% from the top
// (via anchorCursorLine, shared with search/jump/grep), then returns to edit
// mode. The target is a 1-based line number, "top" (first line), or "end" (last
// line). Anything else stays in exec mode with an error.
func (m Model) execJump(arg string) Model {
	idx, ok := parseJumpTarget(arg, len(m.edit.lines))
	if !ok {
		m.errText = "jump needs a line number, top, or end"
		return m
	}
	m.edit.clearSelection()
	m.edit.cy = idx
	m.edit.cx = 0
	m.edit.clampCursor()
	m = m.anchorCursorLine()
	m.mode = m.execPrevMode
	return m
}

// parseJumpTarget resolves a jump argument to a 0-based line index: a 1-based
// number, "top" (first line), or "end" (last line). ok is false otherwise.
func parseJumpTarget(arg string, total int) (int, bool) {
	switch arg {
	case "top":
		return 0, true
	case "end":
		return max(0, total-1), true
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 {
		return 0, false
	}
	return input.Clamp(n-1, 0, max(0, total-1)), true
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

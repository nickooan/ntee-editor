package app

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

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
	return m.refreshExecSugs()
}

// refreshExecSugs recomputes the inline suggestions for the current input and
// resets the highlight to the first candidate.
func (m Model) refreshExecSugs() Model {
	m.execSugs = m.execSuggestions(m.execInput)
	m.execSugIndex = 0
	return m
}

// handleExecKey drives the @exec command bar. Text editing mirrors the : command
// bar (handleCommandKey); Enter runs the typed editor command; Tab accepts the
// highlighted inline suggestion and ↑/↓ cycle it.
func (m Model) handleExecKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = m.execPrevMode
	case "enter":
		return m.runExecCommand(strings.TrimSpace(m.execInput))
	case "tab":
		if len(m.execSugs) > 0 {
			sel := m.execSugs[input.Clamp(m.execSugIndex, 0, len(m.execSugs)-1)]
			m.execInput = acceptExecSuggestion(m.execInput, sel)
			m.execCursor = len([]rune(m.execInput))
			m = m.refreshExecSugs()
		}
	case "down":
		if n := len(m.execSugs); n > 0 {
			m.execSugIndex = (m.execSugIndex + 1) % n
		}
	case "up":
		if n := len(m.execSugs); n > 0 {
			m.execSugIndex = (m.execSugIndex + n - 1) % n
		}
	case "left":
		m.execCursor = input.MoveCursor(m.execInput, m.execCursor, -1)
	case "right":
		m.execCursor = input.MoveCursor(m.execInput, m.execCursor, 1)
	case "backspace":
		m.execInput, m.execCursor, _ = input.RemoveBeforeCursor(m.execInput, m.execCursor)
		m = m.refreshExecSugs()
	case "space":
		m.execInput, m.execCursor = input.InsertAtCursor(m.execInput, m.execCursor, " ")
		m = m.refreshExecSugs()
	default:
		if t := keyText(msg); t != "" {
			m.execInput, m.execCursor = input.InsertAtCursor(m.execInput, m.execCursor, t)
			m = m.refreshExecSugs()
		}
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
		return m.execGit(arg)
	case "openapi", "opapi":
		// Success switches straight to modeOpenAPI (Esc from the preview lands
		// in edit mode at the reviewed content's source, deliberately bypassing
		// execPrevMode, like "git diff").
		if arg != "" {
			m.errText = "openapi takes no argument"
			return m, nil
		}
		return m.enterOpenAPI()
	case "graphql", "gql":
		// Success switches straight to modeGraphQL (Esc from the preview lands
		// in edit mode at the reviewed content's source, deliberately bypassing
		// execPrevMode, like "openapi").
		if arg != "" {
			m.errText = "graphql takes no argument"
			return m, nil
		}
		return m.enterGraphQL()
	case "cpfp":
		m = m.execCopyPath(m.openRel)
	case "cpafp":
		var abs string
		if m.openFile != nil {
			abs = m.openFile.Path
		}
		m = m.execCopyPath(abs)
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

// execCopyPath copies the given open-file path to the clipboard ("cpfp" passes
// the root-relative path, "cpafp" the absolute one). An empty path means no
// file is open. Follows the exec conventions: success flashes a notice and
// returns to the previous mode; errors stay in exec mode.
func (m Model) execCopyPath(path string) Model {
	if path == "" {
		m.errText = "no file open"
		return m
	}
	if err := m.copyClipboard(path); err != nil {
		m.errText = "copy failed: " + err.Error()
		return m
	}
	m.notice = "copied " + path
	m.mode = m.execPrevMode
	return m
}

// execGit dispatches the "git" namespace of editor commands: "scf" (enters
// the interactive conflict-solving mode), "diff [rev]" (enters the async
// diff review mode), and "blame" (enters the async blame view). Errors stay
// in exec mode (no execPrevMode restore) so the user can correct the input.
func (m Model) execGit(arg string) (tea.Model, tea.Cmd) {
	sub, rest, _ := strings.Cut(arg, " ")
	rest = strings.TrimSpace(rest)
	switch sub {
	case "scf":
		// The old form took a side argument; reject it loudly so muscle-memory
		// "git scf head" errors instead of silently doing something new.
		if rest != "" {
			m.errText = "git scf takes no argument"
			return m, nil
		}
		// Success switches straight to modeConflict (Esc lands in edit mode,
		// deliberately bypassing execPrevMode, like "git diff").
		return m.enterConflict(), nil
	case "diff":
		// Success switches straight to modeDiff (Esc from the review lands in
		// edit mode, deliberately bypassing execPrevMode).
		return m.enterDiff(rest)
	case "blame":
		if rest != "" {
			m.errText = "git blame takes no argument"
			return m, nil
		}
		// Success switches straight to modeBlame (Esc lands in edit mode,
		// deliberately bypassing execPrevMode, like "git diff").
		return m.enterBlame()
	case "":
		m.errText = "git needs a subcommand (scf, diff, blame)"
	default:
		m.errText = "unknown git command: " + sub
	}
	return m, nil
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

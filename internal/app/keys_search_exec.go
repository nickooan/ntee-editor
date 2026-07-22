package app

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/view"
)

// enterSearchExec opens the search replace-command bar (Ctrl+E from search
// mode). The search view stays as the body so the match highlights the
// commands act on remain visible behind the bar.
func (m Model) enterSearchExec() Model {
	m.searchExecInput = ""
	m.searchExecCursor = 0
	m.mode = modeSearchExec
	return m
}

// handleSearchExecKey drives the search-exec bar. Text editing mirrors the
// @exec bar (handleExecKey) without suggestions; Enter runs the typed replace
// command and Esc returns to search mode untouched.
func (m Model) handleSearchExecKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.mode = modeSearch
	case tea.KeyEnter:
		// TrimLeft only: trailing spaces belong to the replacement text.
		return m.runSearchExecCommand(strings.TrimLeft(m.searchExecInput, " "))
	case tea.KeyLeft:
		m.searchExecCursor = input.MoveCursor(m.searchExecInput, m.searchExecCursor, -1)
	case tea.KeyRight:
		m.searchExecCursor = input.MoveCursor(m.searchExecInput, m.searchExecCursor, 1)
	case tea.KeyBackspace:
		m.searchExecInput, m.searchExecCursor, _ = input.RemoveBeforeCursor(m.searchExecInput, m.searchExecCursor)
	case tea.KeySpace:
		m.searchExecInput, m.searchExecCursor = input.InsertAtCursor(m.searchExecInput, m.searchExecCursor, " ")
	case tea.KeyRunes:
		m.searchExecInput, m.searchExecCursor = input.InsertAtCursor(m.searchExecInput, m.searchExecCursor, string(msg.Runes))
	}
	return m, nil
}

// searchExecPreview parses the bar input into a live-preview spec. Active once
// the command name is followed by a space and at least one argument character
// (bare "c "/"mlc " commits a deletion on Enter but is not previewed).
func (m Model) searchExecPreview() (all bool, repl string, ok bool) {
	name, arg, hasSpace := strings.Cut(strings.TrimLeft(m.searchExecInput, " "), " ")
	if !hasSpace || arg == "" {
		return false, "", false
	}
	switch name {
	case "c":
		return false, arg, true
	case "mlc":
		return true, arg, true
	}
	return false, "", false
}

// buildSearchPreview splices repl over the target matches (all of them, or the
// clamped focused one) purely for display: it returns the previewed lines, the
// byte spans of the inserted repl per line (for preview styling), and the
// surviving non-target matches with their byte offsets shifted by the splices.
// The commit path (searchReplace/replaceInLines) stays separate — this pass
// walks left-to-right per line because it must also track span positions.
func buildSearchPreview(lines []string, matches []view.SearchMatch, focused int, all bool, repl string) ([]string, map[int][][2]int, []view.SearchMatch) {
	out := append([]string(nil), lines...)
	pspans := map[int][][2]int{}
	var rest []view.SearchMatch
	target := func(i int) bool { return all || i == input.Clamp(focused, 0, max(0, len(matches)-1)) }

	delta, deltaLine := 0, -1
	for i, mt := range matches {
		if mt.LineIndex != deltaLine {
			delta, deltaLine = 0, mt.LineIndex
		}
		if !target(i) {
			rest = append(rest, view.SearchMatch{LineIndex: mt.LineIndex, Start: mt.Start + delta, End: mt.End + delta})
			continue
		}
		if mt.LineIndex < 0 || mt.LineIndex >= len(out) {
			continue
		}
		line := out[mt.LineIndex]
		s, e := clampByte(mt.Start+delta, len(line)), clampByte(mt.End+delta, len(line))
		if s > e {
			continue
		}
		out[mt.LineIndex] = line[:s] + repl + line[e:]
		pspans[mt.LineIndex] = append(pspans[mt.LineIndex], [2]int{s, s + len(repl)})
		delta += len(repl) - (e - s)
	}
	return out, pspans, rest
}

// runSearchExecCommand dispatches "c <text>" (replace the focused match) and
// "mlc <text>" (replace every match). The argument is kept verbatim — it may
// contain or end with spaces — and may be empty, which deletes the match span.
// On error it stays in the bar so the user can correct the input.
func (m Model) runSearchExecCommand(cmd string) (tea.Model, tea.Cmd) {
	name, arg, _ := strings.Cut(cmd, " ")
	switch name {
	case "":
		m.mode = modeSearch
	case "c":
		m = m.searchReplace(false, arg)
	case "mlc":
		m = m.searchReplace(true, arg)
	default:
		m.errText = "unknown command: " + name
	}
	return m, nil
}

// searchReplace rewrites the focused match (all=false) or every match
// (all=true) with repl as one undoable step, then re-freezes the search
// snapshot and returns to search mode — where matches are recomputed, so
// replaced text that no longer matches the query loses its highlight.
func (m Model) searchReplace(all bool, repl string) Model {
	if m.searchPrevMode != modeEdit || m.openFile == nil {
		m.errText = "replace needs an edit session"
		return m
	}
	matches := view.FindSearchMatches(m.searchContent, m.searchInput)
	if len(matches) == 0 {
		m.errText = "no matches"
		return m
	}
	targets := matches
	if !all {
		i := input.Clamp(m.searchFocused, 0, len(matches)-1)
		targets = matches[i : i+1]
	}

	m = m.flushBurst()
	m.edit.lines = replaceInLines(m.edit.lines, targets, repl)

	// Land the edit cursor on the first replaced span so leaving search drops
	// the user at the edit site (byte offset → rune column, as acceptSearch).
	first := targets[0]
	m.edit.clearSelection()
	m.edit.cy = input.Clamp(first.LineIndex, 0, len(m.edit.lines)-1)
	line := m.edit.lines[m.edit.cy]
	m.edit.cx = utf8.RuneCountInString(line[:clampByte(first.Start, len(line))])
	m.edit.clampCursor()

	m.edit.dirty = true
	m.edit.rev++
	m.snapDirty = true
	m = m.pushSnapshot("edit")
	m.mode = modeSearch // before refreshFileHighlights, which reads the mode
	m = m.refreshFileHighlights()
	m = m.freezeSearchSnapshot(m.edit.content())

	if all {
		m.searchFocused = 0
	} else {
		// Surviving matches shifted down one index, so the kept (clamped)
		// index naturally lands on the formerly-next match.
		n := len(view.FindSearchMatches(m.searchContent, m.searchInput))
		m.searchFocused = input.Clamp(m.searchFocused, 0, max(0, n-1))
	}
	m.notice = fmt.Sprintf("replaced %d match(es)", len(targets))
	return m
}

// replaceInLines splices repl over each target span. Targets carry byte
// offsets into their line; within a line they are applied right-to-left so
// earlier offsets stay valid after the line's length changes.
func replaceInLines(lines []string, targets []view.SearchMatch, repl string) []string {
	byLine := map[int][]view.SearchMatch{}
	for _, t := range targets {
		byLine[t.LineIndex] = append(byLine[t.LineIndex], t)
	}
	out := append([]string(nil), lines...)
	for li, ms := range byLine {
		if li < 0 || li >= len(out) {
			continue
		}
		sort.Slice(ms, func(a, b int) bool { return ms[a].Start > ms[b].Start })
		line := out[li]
		for _, t := range ms {
			s, e := clampByte(t.Start, len(line)), clampByte(t.End, len(line))
			if s > e {
				continue
			}
			line = line[:s] + repl + line[e:]
		}
		out[li] = line
	}
	return out
}

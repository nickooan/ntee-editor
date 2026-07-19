package app

import (
	"sort"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/lsp"
)

// completionMsg carries an async textDocument/completion result. line and start
// tag the request's cursor line and identifier-start column so a stale answer
// (the user moved or typed past the word) can be dropped.
type completionMsg struct {
	line  int
	start int
	items []lsp.CompletionItem
	err   error
}

// isIdentRune reports whether r can appear in a code identifier (incl. $ for JS).
func isIdentRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// identStart walks back from cx over identifier runes and returns the start col.
func identStart(line []rune, cx int) int {
	i := input.Clamp(cx, 0, len(line))
	for i > 0 && isIdentRune(line[i-1]) {
		i--
	}
	return i
}

// isIdentifierText reports whether s is one or more identifier runes.
func isIdentifierText(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !isIdentRune(r) {
			return false
		}
	}
	return true
}

// requestCompletion syncs the server to the current buffer (flushBurst sends
// DidChange) and fires an async textDocument/completion at the cursor.
func (m Model) requestCompletion() (tea.Model, tea.Cmd) {
	if m.openFile == nil || m.completionPending {
		return m, nil
	}
	client, ok := m.lsp.ClientFor(m.openFile.Path)
	if !ok {
		return m, nil
	}
	// The server must see the just-typed prefix. Sync directly rather than
	// flushBurst so completion doesn't snapshot (fragment) the undo history
	// mid-word; the burst still flushes at its normal boundary.
	client.DidChange(m.openFile.Path, m.edit.content(), m.edit.rev)
	line := m.edit.cy
	start := identStart(m.edit.line(), m.edit.cx)
	utf16Col := lsp.UTF16Col(m.edit.lines[line], m.edit.cx)
	path := m.openFile.Path
	m.completionPending = true
	return m, func() tea.Msg {
		items, err := client.Completion(path, line, utf16Col)
		return completionMsg{line: line, start: start, items: items, err: err}
	}
}

// handleCompletion lands an async completion answer, dropping it if the buffer
// moved on (different line or the identifier under the cursor shifted).
func (m Model) handleCompletion(msg completionMsg) (tea.Model, tea.Cmd) {
	m.completionPending = false
	if msg.err != nil || m.mode != modeEdit || m.openFile == nil {
		return m, nil
	}
	if m.edit.cy != msg.line || identStart(m.edit.line(), m.edit.cx) != msg.start {
		return m, nil // context moved while the request was in flight
	}
	if len(msg.items) == 0 {
		m.completionOpen = false
		return m, nil
	}
	m.completionAll = msg.items
	m.completionOpen = true
	return m.filterCompletions(), nil
}

// filterCompletions narrows completionAll to items whose FilterText/Label starts
// with the identifier prefix under the cursor (case-insensitive), sorted by
// SortText then Label. It closes the popup when nothing matches.
func (m Model) filterCompletions() Model {
	line := m.edit.line()
	start := identStart(line, m.edit.cx)
	m.completionStart = start
	prefix := strings.ToLower(string(line[start:input.Clamp(m.edit.cx, 0, len(line))]))

	out := make([]lsp.CompletionItem, 0, len(m.completionAll))
	for _, it := range m.completionAll {
		key := it.FilterText
		if key == "" {
			key = it.Label
		}
		if strings.HasPrefix(strings.ToLower(key), prefix) {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ki, kj := completionSortKey(out[i]), completionSortKey(out[j])
		if ki != kj {
			return ki < kj
		}
		return out[i].Label < out[j].Label
	})
	m.completionItems = out
	m.completionIndex = 0
	if len(out) == 0 {
		m.completionOpen = false
	}
	return m
}

func completionSortKey(it lsp.CompletionItem) string {
	if it.SortText != "" {
		return it.SortText
	}
	return it.Label
}

// acceptCompletion replaces the identifier prefix under the cursor with the
// selected item's insert text and closes the popup.
func (m Model) acceptCompletion() Model {
	if len(m.completionItems) == 0 {
		m.completionOpen = false
		return m
	}
	it := m.completionItems[input.Clamp(m.completionIndex, 0, len(m.completionItems)-1)]
	text := it.InsertText
	if text == "" {
		text = it.Label
	}
	start := identStart(m.edit.line(), m.edit.cx)
	// Select the partial word so insert() replaces it (insert deletes the
	// selection first).
	m.edit.sel = &selRange{start: start, end: m.edit.cx}
	m.edit.insert(text)
	m = m.hlMarkLine(m.edit.cy)
	m.snapDirty = true
	m.completionOpen = false
	m.completionDismissed = false
	return m.flushBurst()
}

// completionKey consumes the popup's navigation/accept/dismiss keys while it is
// open; handled is false for any other key (the caller processes it normally).
func (m Model) completionKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	switch msg.Type {
	case tea.KeyUp:
		m.completionIndex = max(0, m.completionIndex-1)
		return m, nil, true
	case tea.KeyDown:
		m.completionIndex = min(len(m.completionItems)-1, m.completionIndex+1)
		return m, nil, true
	case tea.KeyTab, tea.KeyEnter:
		return m.acceptCompletion(), nil, true
	case tea.KeyEsc:
		m.completionOpen = false
		m.completionDismissed = true
		return m, nil, true
	}
	return m, nil, false
}

// afterEditType manages the popup after a rune is typed: refilter an open popup,
// (re)request on "." or an identifier char, or close on anything else.
func (m Model) afterEditType(typed string) (tea.Model, tea.Cmd) {
	switch {
	case typed == ".":
		m.completionDismissed = false
		return m.requestCompletion()
	case isIdentifierText(typed):
		if m.completionOpen {
			return m.filterCompletions(), nil
		}
		if !m.completionDismissed {
			return m.requestCompletion()
		}
		return m, nil
	default:
		// Non-identifier char ends the word: drop the popup and its suppression.
		m.completionOpen = false
		m.completionDismissed = false
		return m, nil
	}
}

// afterEditBackspace refilters an open popup after a delete, closing it once the
// prefix is gone.
func (m Model) afterEditBackspace() (tea.Model, tea.Cmd) {
	if !m.completionOpen {
		return m, nil
	}
	line := m.edit.line()
	if identStart(line, m.edit.cx) == m.edit.cx {
		m.completionOpen = false // backspaced to (or before) the word start
		return m, nil
	}
	return m.filterCompletions(), nil
}

// closeCompletion clears popup state (used when leaving edit mode / opening an
// overlay).
func (m Model) closeCompletion() Model {
	m.completionOpen = false
	m.completionPending = false
	m.completionDismissed = false
	return m
}

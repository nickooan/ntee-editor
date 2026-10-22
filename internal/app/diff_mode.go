package app

import (
	"errors"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/input"
)

// diffReadyMsg delivers a computed diff from the worker goroutine. err is a
// user-facing message ("" on success); gen/rel guard against stale results.
type diffReadyMsg struct {
	gen       int
	rel, base string
	document  diffDocument
	err       string
}

// enterDiff switches to diff review mode and kicks off the async diff
// computation. base "" means HEAD — all uncommitted changes, including the
// buffer's unsaved edits. Errors keep the current mode (the @exec bar) so the
// user can correct the input.
func (m Model) enterDiff(base string) (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		m.errText = "no file open"
		return m, nil
	}
	if _, _, ok, loading := m.gitScopeFor(m.openRel); !ok {
		m.errText = gitScopeError(loading)
		return m, nil
	}
	// A revision starting with "-" would reach git's argv as an option, not an
	// operand — refuse it outright (no real revision starts with a dash).
	if strings.HasPrefix(base, "-") {
		m.errText = "git diff: bad revision: " + base
		return m, nil
	}
	m = m.clearDiffState()
	m.edit.clearSelection() // a stale selection would hijack Ctrl+J's token
	m.diffBase = base
	m.diffGen++
	m.diffLoading = true
	m.mode = modeDiff
	return m, m.computeDiffCmd()
}

// clearDiffState drops the diff view model and resets every diff field except
// diffGen, which stays monotonic so in-flight results remain identifiable as
// stale. Freeing the document releases the del rows' text copies.
func (m Model) clearDiffState() Model {
	m.diffDocument = diffDocument{}
	m.diffPosition = rowCursor{}
	m.diffBase = ""
	m.diffLoading = false
	m.diffHasPending = false
	m.diffPendingCursor, m.diffPendingScroll = 0, 0
	return m
}

// liveDiffDocument binds the diff rows to the edit session they index into:
// the buffer lines and its highlight cache. Valid because diff mode blocks
// edits — the buffer is exactly what the rows were computed against.
func (m Model) liveDiffDocument() diffDocument {
	document := m.diffDocument
	document.lines, document.highlights = m.edit.lines, m.hlLines
	return document
}

// diffSpan is the diff view's cursor span: the rendered rows below a
// single-line status, like edit mode.
func (m Model) diffSpan() rowSpan {
	return m.liveDiffDocument().span(m.contentHeight() + 1)
}

// computeDiffCmd snapshots the buffer and runs git + Myers off the UI
// goroutine (precedent: refreshGitStatusCmd). The snapshot copy also makes
// the closure safe against buffer edits racing a stale result.
func (m Model) computeDiffCmd() tea.Cmd {
	gen, rel, base := m.diffGen, m.openRel, m.diffBase
	root, repoRel, _, _ := m.gitScopeFor(rel)
	current := append([]string(nil), m.edit.lines...)
	return func() tea.Msg {
		document, err := computeDiffDocument(root, base, repoRel, current, true)
		if err != "" {
			return diffReadyMsg{gen: gen, rel: rel, base: base, err: "git diff: " + err}
		}
		// The rows index the live buffer (liveDiffDocument); drop the snapshot.
		document.lines = nil
		return diffReadyMsg{gen: gen, rel: rel, base: base, document: document}
	}
}

// handleDiffReady lands the async diff. A generation, file, or mode mismatch
// means the user Esc'd, re-ran, or switched files while it computed — drop it.
func (m Model) handleDiffReady(msg diffReadyMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.diffGen || msg.rel != m.openRel || m.mode != modeDiff {
		return m, nil
	}
	if msg.err != "" {
		m.errText = msg.err
		m.mode = modeEdit
		return m.clearDiffState(), nil
	}
	if msg.document.adds == 0 && msg.document.dels == 0 {
		m.notice = "git diff: no changes"
		m.mode = modeEdit
		return m.clearDiffState(), nil
	}
	m.diffLoading = false
	m.diffDocument = msg.document
	rows := m.diffDocument.rows
	h := m.contentHeight() + 1 // rendered rows: single-line status, like edit mode
	if m.diffHasPending {
		// Ctrl+O return: restore the remembered review position, clamped —
		// the re-derived diff may have fewer rows than when we left.
		m.diffHasPending = false
		m.diffPosition.row = input.Clamp(m.diffPendingCursor, 0, len(rows)-1)
		m.diffPosition.scrollY = input.Clamp(m.diffPendingScroll, 0, max(0, len(rows)-h))
	} else {
		// Fresh entry: stay at the current place — same buffer line, same
		// on-screen row. The exact inverse of the Esc mapping in exitDiff.
		screenRow := m.edit.cy - fileViewportTop(m.edit.cy, m.fileScrollY, h, len(m.edit.lines))
		m.diffPosition.row = diffRowForBufLine(rows, m.edit.cy)
		m.diffPosition.scrollY = input.Clamp(m.diffPosition.row-screenRow, 0, max(0, len(rows)-h))
	}
	m.diffPosition.column = m.edit.cx
	m.diffPosition = m.diffPosition.clampColumn(m.diffSpan())
	return m, nil
}

// splitBufferLines splits file content exactly like newEditor does, so the
// old and new sides compare like-for-like (trailing-newline changes surface
// as a real diff, identical content diffs empty).
func splitBufferLines(content string) []string {
	return strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
}

// firstStderrLine extracts the first stderr line of a failed exec, "" when
// there is none — git's first line carries the useful message.
func firstStderrLine(err error) string {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return ""
	}
	s := strings.TrimSpace(string(ee.Stderr))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

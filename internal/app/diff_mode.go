package app

import (
	"bytes"
	"errors"
	"os/exec"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/diff"
	"github.com/nickooan/ntee-editor/internal/input"
)

// diffRowKind classifies one display row of the diff review view.
type diffRowKind uint8

const (
	diffCtx diffRowKind = iota // unchanged: normal background, numbered
	diffAdd                    // added: green background, numbered
	diffDel                    // removed: red background, blank gutter
)

// diffRow is one rendered line of the unified diff. Ctx/add rows point into
// m.edit.lines (no text duplication); del rows carry the removed text and
// point at the buffer line the deletion sits before (its successor, clamped),
// which is what Esc and Ctrl+J map a del row to. bufLine is non-decreasing
// down the slice, so buffer-line lookups can binary-search.
type diffRow struct {
	kind    diffRowKind
	bufLine int
	text    string // del rows only
}

// diffReadyMsg delivers a computed diff from the worker goroutine. err is a
// user-facing message ("" on success); gen/rel guard against stale results.
type diffReadyMsg struct {
	gen        int
	rel, base  string
	rows       []diffRow
	adds, dels int
	newFile    bool
	err        string
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
	if !m.gitRepo {
		m.errText = "not a git repository"
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
// stale. Freeing diffRows releases the del rows' text copies.
func (m Model) clearDiffState() Model {
	m.diffRows = nil
	m.diffCursor, m.diffCx, m.diffScrollY = 0, 0, 0
	m.diffBase = ""
	m.diffNewFile = false
	m.diffAdds, m.diffDels = 0, 0
	m.diffLoading = false
	m.diffHasPending = false
	m.diffPendingCursor, m.diffPendingScroll = 0, 0
	return m
}

// computeDiffCmd snapshots the buffer and runs git + Myers off the UI
// goroutine (precedent: refreshGitStatusCmd). The snapshot copy also makes
// the closure safe against buffer edits racing a stale result.
func (m Model) computeDiffCmd() tea.Cmd {
	gen, rel, base, root := m.diffGen, m.openRel, m.diffBase, m.root
	cur := append([]string(nil), m.edit.lines...)
	return func() tea.Msg {
		msg := diffReadyMsg{gen: gen, rel: rel, base: base}
		rev := base
		if rev == "" {
			rev = "HEAD"
		}
		if _, err := gitOut(root, "rev-parse", "--verify", rev+"^{commit}"); err != nil {
			if base != "" {
				msg.err = "git diff: bad revision: " + base
				if line := firstStderrLine(err); line != "" {
					msg.err = "git diff: " + line
				}
				return msg
			}
			// A bare HEAD that doesn't resolve = a repo with no commits yet:
			// not an error, everything is new.
			msg.newFile = true
		}
		var old []string
		if !msg.newFile {
			out, err := gitOut(root, "show", rev+":"+rel)
			switch {
			case err != nil:
				msg.newFile = true // path absent in that revision: new file
			case bytes.IndexByte(out, 0) >= 0:
				msg.err = "git diff: old version is binary"
				return msg
			default:
				old = splitBufferLines(string(out))
			}
		}
		if msg.newFile {
			msg.rows = make([]diffRow, len(cur))
			for i := range cur {
				msg.rows[i] = diffRow{kind: diffAdd, bufLine: i}
			}
			msg.adds = len(cur)
			return msg
		}
		msg.rows, msg.adds, msg.dels = buildDiffRows(old, cur)
		return msg
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
	if msg.adds == 0 && msg.dels == 0 {
		m.notice = "git diff: no changes"
		m.mode = modeEdit
		return m.clearDiffState(), nil
	}
	m.diffLoading = false
	m.diffRows = msg.rows
	m.diffAdds, m.diffDels = msg.adds, msg.dels
	m.diffNewFile = msg.newFile
	h := m.contentHeight() + 1 // rendered rows: single-line status, like edit mode
	if m.diffHasPending {
		// Ctrl+O return: restore the remembered review position, clamped —
		// the re-derived diff may have fewer rows than when we left.
		m.diffHasPending = false
		m.diffCursor = input.Clamp(m.diffPendingCursor, 0, len(m.diffRows)-1)
		m.diffScrollY = input.Clamp(m.diffPendingScroll, 0, max(0, len(m.diffRows)-h))
	} else {
		// Fresh entry: stay at the current place — same buffer line, same
		// on-screen row. The exact inverse of the Esc mapping in exitDiff.
		screenRow := m.edit.cy - fileViewportTop(m.edit.cy, m.fileScrollY, h, len(m.edit.lines))
		m.diffCursor = diffRowForBufLine(m.diffRows, m.edit.cy)
		m.diffScrollY = input.Clamp(m.diffCursor-screenRow, 0, max(0, len(m.diffRows)-h))
	}
	m.diffCx = input.Clamp(m.edit.cx, 0, len([]rune(m.diffRowText(m.diffCursor))))
	return m, nil
}

// buildDiffRows converts the Myers edit script into display rows and fills
// each del row's bufLine with its successor ctx/add row's line (clamped at
// EOF), walking backwards once.
func buildDiffRows(old, cur []string) (rows []diffRow, adds, dels int) {
	ops := diff.Lines(old, cur)
	rows = make([]diffRow, 0, len(ops))
	for _, op := range ops {
		switch op.Kind {
		case diff.Equal:
			rows = append(rows, diffRow{kind: diffCtx, bufLine: op.BIdx})
		case diff.Insert:
			rows = append(rows, diffRow{kind: diffAdd, bufLine: op.BIdx})
			adds++
		case diff.Delete:
			rows = append(rows, diffRow{kind: diffDel, bufLine: -1, text: old[op.AIdx]})
			dels++
		}
	}
	next := max(0, len(cur)-1)
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].kind == diffDel {
			rows[i].bufLine = next
		} else {
			next = rows[i].bufLine
		}
	}
	return rows, adds, dels
}

// diffRowBufLine maps a display row to its buffer line (del rows map to their
// successor), clamped so callers can feed any cursor value.
func diffRowBufLine(rows []diffRow, i int) int {
	if len(rows) == 0 {
		return 0
	}
	return rows[input.Clamp(i, 0, len(rows)-1)].bufLine
}

// diffRowForBufLine maps a buffer line to its ctx/add display row. Every
// buffer line has exactly one such row, and bufLine is non-decreasing, so a
// binary search plus a skip over the preceding del run lands exactly.
func diffRowForBufLine(rows []diffRow, line int) int {
	if len(rows) == 0 {
		return 0
	}
	i := sort.Search(len(rows), func(i int) bool { return rows[i].bufLine >= line })
	for i < len(rows) && rows[i].kind == diffDel {
		i++
	}
	return input.Clamp(i, 0, len(rows)-1)
}

// diffRowText is the display text of a row: the live buffer line for ctx/add,
// the removed copy for del.
func (m Model) diffRowText(i int) string {
	if len(m.diffRows) == 0 {
		return ""
	}
	row := m.diffRows[input.Clamp(i, 0, len(m.diffRows)-1)]
	if row.kind == diffDel {
		return row.text
	}
	if row.bufLine >= 0 && row.bufLine < len(m.edit.lines) {
		return m.edit.lines[row.bufLine]
	}
	return ""
}

// splitBufferLines splits file content exactly like newEditor does, so the
// old and new sides compare like-for-like (trailing-newline changes surface
// as a real diff, identical content diffs empty).
func splitBufferLines(content string) []string {
	return strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
}

// gitOut runs one short-lived git child against root and returns stdout;
// stderr rides the *exec.ExitError for error reporting.
func gitOut(root string, args ...string) ([]byte, error) {
	return exec.Command("git", append([]string{"-C", root}, args...)...).Output()
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

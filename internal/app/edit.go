package app

import (
	"strings"

	"github.com/nickooan/ntee-editor/internal/input"
)

// Minimal multi-line editor state used by edit mode. Covers insert / delete /
// newline / cursor movement / save, plus a single-line selection (used by the
// progressive Ctrl+A select and selection-aware delete).
type editor struct {
	lines []string
	cx    int
	cy    int
	dirty bool

	// rev counts line mutations; the model compares it against its cached
	// highlight state to know when to rescan the buffer. Every method that
	// mutates `lines` MUST bump it (alongside setting dirty) or highlighting
	// goes stale.
	rev int

	// sel, when non-nil, is a highlighted range [start,end) of rune columns on
	// line cy. selLevel tracks how far the progressive Ctrl+A has expanded so
	// repeated presses grow the range; selAnchor is the cursor column captured
	// at the first press, so the expansion levels stay stable even though the
	// cursor rides the selection end.
	sel       *selRange
	selLevel  int
	selAnchor int
}

// selRange is a half-open [start,end) span of rune columns on the cursor line.
type selRange struct{ start, end int }

func newEditor(content string) editor {
	return editor{lines: strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")}
}

func (e editor) content() string { return strings.Join(e.lines, "\n") }

func (e *editor) line() []rune { return []rune(e.lines[e.cy]) }

func (e *editor) clampCursor() {
	if e.cy < 0 {
		e.cy = 0
	}
	if e.cy > len(e.lines)-1 {
		e.cy = len(e.lines) - 1
	}
	e.cx = input.Clamp(e.cx, 0, len(e.line()))
}

// clearSelection drops any active selection and resets the Ctrl+A level.
func (e *editor) clearSelection() {
	e.sel = nil
	e.selLevel = 0
}

func (e *editor) insert(text string) {
	e.deleteSelection() // typing over a selection replaces it
	line := e.line()
	at := input.Clamp(e.cx, 0, len(line))
	e.lines[e.cy] = string(line[:at]) + text + string(line[at:])
	e.cx = at + len([]rune(text))
	e.dirty = true
	e.rev++
}

func (e *editor) newline() {
	e.deleteSelection()
	line := e.line()
	at := input.Clamp(e.cx, 0, len(line))
	before := string(line[:at])
	after := string(line[at:])
	e.lines[e.cy] = before
	rest := append([]string{after}, e.lines[e.cy+1:]...)
	e.lines = append(e.lines[:e.cy+1], rest...)
	e.cy++
	e.cx = 0
	e.dirty = true
	e.rev++
}

func (e *editor) backspace() {
	// A backspace over a selection just deletes the selection (no extra char).
	if e.deleteSelection() {
		return
	}
	if e.cx > 0 {
		line := e.line()
		e.lines[e.cy] = string(line[:e.cx-1]) + string(line[e.cx:])
		e.cx--
		e.dirty = true
		e.rev++
		return
	}
	if e.cy == 0 {
		return
	}
	// Merge with the previous line.
	prev := []rune(e.lines[e.cy-1])
	e.cx = len(prev)
	e.lines[e.cy-1] = string(prev) + e.lines[e.cy]
	e.lines = append(e.lines[:e.cy], e.lines[e.cy+1:]...)
	e.cy--
	e.dirty = true
	e.rev++
}

func (e *editor) move(dx, dy int) {
	e.clearSelection() // any cursor move drops the selection
	e.cy = input.Clamp(e.cy+dy, 0, len(e.lines)-1)
	e.cx = input.Clamp(e.cx+dx, 0, len(e.line()))
}

// selectedText returns the text of the active selection on the cursor line,
// clamped like deleteSelection; "" when there is no selection.
func (e *editor) selectedText() string {
	if e.sel == nil {
		return ""
	}
	line := e.line()
	s := input.Clamp(e.sel.start, 0, len(line))
	end := input.Clamp(e.sel.end, 0, len(line))
	if s > end {
		s, end = end, s
	}
	return string(line[s:end])
}

// deleteSelection removes the active selection's text on line cy (a whole-line
// selection empties the line), moves the cursor to its start, and clears the
// selection. Reports whether anything was selected.
func (e *editor) deleteSelection() bool {
	if e.sel == nil {
		return false
	}
	line := e.line()
	s := input.Clamp(e.sel.start, 0, len(line))
	end := input.Clamp(e.sel.end, 0, len(line))
	if s > end {
		s, end = end, s
	}
	e.lines[e.cy] = string(line[:s]) + string(line[end:])
	e.cx = s
	e.clearSelection()
	e.dirty = true
	e.rev++
	return true
}

// expandSelection implements the progressive Ctrl+A select: the first press
// selects the word under the cursor, the next the whole line. The levels keep
// the dedupeRanges machinery so a syntax-scope level (via LSP or the lexer)
// can slot in between later. Once at the whole line, further presses are a
// no-op.
func (e *editor) expandSelection() {
	line := e.line()
	if e.sel == nil {
		e.selAnchor = input.Clamp(e.cx, 0, len(line))
	}
	// Levels are computed from the stable anchor, not the moving cursor, so
	// they do not flip as the cursor rides the selection end.
	cands := dedupeRanges([]selRange{
		wordRange(line, e.selAnchor),
		{0, len(line)},
	})
	if len(cands) == 0 {
		return
	}
	if e.sel == nil {
		e.selLevel = 0
	} else if e.selLevel < len(cands)-1 {
		e.selLevel++
	}
	if e.selLevel > len(cands)-1 {
		e.selLevel = len(cands) - 1
	}
	sel := cands[e.selLevel]
	e.sel = &sel
	e.cx = sel.end // cursor rides the end of the selection
}

func isEditSpace(r rune) bool { return r == ' ' || r == '\t' }

// wordRange returns the maximal run of non-whitespace runes containing (or, when
// the cursor sits on whitespace or at the line end, immediately before) cx.
// Returns an empty range at cx when the line has no word.
func wordRange(line []rune, cx int) selRange {
	n := len(line)
	i := input.Clamp(cx, 0, n)
	pos := i
	if pos >= n || isEditSpace(line[pos]) {
		pos = i - 1
		for pos >= 0 && isEditSpace(line[pos]) {
			pos--
		}
	}
	if pos < 0 { // nothing before the cursor — look after it
		pos = i
		for pos < n && isEditSpace(line[pos]) {
			pos++
		}
		if pos >= n {
			return selRange{i, i}
		}
	}
	start, end := pos, pos+1
	for start > 0 && !isEditSpace(line[start-1]) {
		start--
	}
	for end < n && !isEditSpace(line[end]) {
		end++
	}
	return selRange{start, end}
}

// dedupeRanges drops empty and consecutive-duplicate ranges, preserving order.
// The input is ordered smallest→largest, so this yields the distinct expansion
// levels the progressive select steps through.
func dedupeRanges(rs []selRange) []selRange {
	out := make([]selRange, 0, len(rs))
	for _, r := range rs {
		if r.start == r.end && r.start == 0 { // keep an empty whole-line for empty lines
			if len(out) == 0 {
				out = append(out, r)
			}
			continue
		}
		if r.start == r.end {
			continue // skip a genuinely empty word
		}
		if len(out) > 0 && out[len(out)-1] == r {
			continue
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		out = append(out, selRange{0, 0})
	}
	return out
}

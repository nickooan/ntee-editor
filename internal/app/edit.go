package app

import (
	"sort"
	"strings"
	"unicode"

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

	// selLineMode marks a whole-line (line-wise) selection: the selected lines
	// are [min(selLineAnchor, cy), max(selLineAnchor, cy)] inclusive. It is
	// entered by the whole-line level of the progressive Ctrl+A select and
	// extended by Shift+↑/↓. sel still tracks the cursor line's span so the
	// cursor-line highlight keeps drawing.
	selLineMode   bool
	selLineAnchor int
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
	e.selLineMode = false
	e.selLineAnchor = 0
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
	if e.selLineMode {
		lo, hi := e.selLineAnchor, e.cy
		if lo > hi {
			lo, hi = hi, lo
		}
		lo = input.Clamp(lo, 0, len(e.lines)-1)
		hi = input.Clamp(hi, 0, len(e.lines)-1)
		// Replace the whole-line range with a single empty line.
		rest := append([]string{""}, e.lines[hi+1:]...)
		e.lines = append(e.lines[:lo], rest...)
		e.cy = lo
		e.cx = 0
		e.clearSelection()
		e.dirty = true
		e.rev++
		return true
	}
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
	// Reaching the whole-line span latches line-wise mode so Shift+↑/↓ can
	// extend it across lines.
	if sel.start == 0 && sel.end == len(line) {
		e.selLineMode = true
		e.selLineAnchor = e.cy
	} else {
		e.selLineMode = false
	}
}

// extendLineSelection grows or shrinks a whole-line selection by dy lines, the
// cursor riding the moving end. It is a no-op unless a line-wise selection is
// already active (entered via the whole-line level of Ctrl+A).
func (e *editor) extendLineSelection(dy int) {
	if !e.selLineMode {
		return
	}
	e.cy = input.Clamp(e.cy+dy, 0, len(e.lines)-1)
	line := e.line()
	e.sel = &selRange{0, len(line)}
	e.cx = len(line)
}

// inLineSelection reports whether line i falls within an active line-wise
// selection (used by rendering to highlight the whole range).
func (e *editor) inLineSelection(i int) bool {
	if !e.selLineMode {
		return false
	}
	lo, hi := e.selLineAnchor, e.cy
	if lo > hi {
		lo, hi = hi, lo
	}
	return i >= lo && i <= hi
}

// selectionText returns the text to copy: the whole selected lines
// (newline-joined with a trailing newline) in line-wise mode, else the
// single-line selection.
func (e *editor) selectionText() string {
	if e.selLineMode {
		lo, hi := e.selLineAnchor, e.cy
		if lo > hi {
			lo, hi = hi, lo
		}
		lo = input.Clamp(lo, 0, len(e.lines)-1)
		hi = input.Clamp(hi, 0, len(e.lines)-1)
		return strings.Join(e.lines[lo:hi+1], "\n") + "\n"
	}
	return e.selectedText()
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

// identifierAt returns the identifier run containing cx (or ending exactly at
// cx — the cursor sitting just past a word still counts), else ok=false.
func identifierAt(line []rune, cx int) (start, end int, ok bool) {
	n := len(line)
	i := input.Clamp(cx, 0, n)
	pos := -1
	switch {
	case i < n && isIdentRune(line[i]):
		pos = i
	case i > 0 && isIdentRune(line[i-1]):
		pos = i - 1
	}
	if pos < 0 {
		return 0, 0, false
	}
	start, end = pos, pos+1
	for start > 0 && isIdentRune(line[start-1]) {
		start--
	}
	for end < n && isIdentRune(line[end]) {
		end++
	}
	return start, end, true
}

// identifierText returns the identifier at cx, or "".
func identifierText(line []rune, cx int) string {
	if s, e, ok := identifierAt(line, cx); ok {
		return string(line[s:e])
	}
	return ""
}

// identifierCols returns the start columns of up to max identifier runs on the
// line, nearest to cx first (ties resolve left-to-right). Runs starting with a
// digit are skipped — those are number literals, not names.
func identifierCols(line []rune, cx int, max int) []int {
	type run struct{ start, end int }
	var runs []run
	for i := 0; i < len(line); {
		if !isIdentRune(line[i]) {
			i++
			continue
		}
		start := i
		for i < len(line) && isIdentRune(line[i]) {
			i++
		}
		if !unicode.IsDigit(line[start]) {
			runs = append(runs, run{start, i})
		}
	}
	dist := func(r run) int {
		if cx >= r.start && cx <= r.end {
			return 0
		}
		if cx < r.start {
			return r.start - cx
		}
		return cx - r.end
	}
	sort.SliceStable(runs, func(a, b int) bool { return dist(runs[a]) < dist(runs[b]) })
	cols := make([]int, 0, min(max, len(runs)))
	for _, r := range runs[:min(max, len(runs))] {
		cols = append(cols, r.start)
	}
	return cols
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

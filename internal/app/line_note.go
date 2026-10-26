package app

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/input"
)

// lineNoteOverlay is an external open's note, shown in a box next to the line
// it explains. It is non-modal: keys and scrolling keep driving the editor.
type lineNoteOverlay struct {
	rel          string // the file it belongs to (m.openRel)
	line         int    // 0-based buffer line it explains
	text         string
	naturalWidth int // widest source line in cells, sizes the box
	editRevision int // m.edit.rev when shown — any edit hides the note
	scroll       int // first note row shown when the box is capped
	layout       *lineNoteLayoutCache
}

// lineNoteLayoutCache memoizes the wrapped, styled note for one inner width;
// a pointer so every Model copy shares it.
type lineNoteLayoutCache struct {
	innerWidth int
	lines      []string
}

const (
	lineNoteMargin   = 2   // cells kept clear on each side of the box
	lineNoteMinWidth = 30  // box width floor, when the pane allows it
	lineNoteMaxWidth = 100 // box width cap: prose past this gets hard to read
)

func newLineNote(rel string, line int, text string, editRevision int) lineNoteOverlay {
	naturalWidth := 0
	for _, sourceLine := range strings.Split(text, "\n") {
		naturalWidth = max(naturalWidth, ansi.StringWidth(expandTabs(sourceLine)))
	}
	return lineNoteOverlay{
		rel:          rel,
		line:         line,
		text:         text,
		naturalWidth: naturalWidth,
		editRevision: editRevision,
		layout:       &lineNoteLayoutCache{},
	}
}

// lineNoteVisible reports whether the note box is drawn: its file is the one
// being edited and the buffer is unchanged since it was shown.
func (m Model) lineNoteVisible() bool {
	note := m.lineNote
	return note.text != "" && (m.mode == modeEdit || m.mode == modeExec) && m.openFile != nil &&
		m.openRel == note.rel && m.edit.rev == note.editRevision
}

// lines is the note laid out for innerWidth, from the cache when the width
// is unchanged.
func (note lineNoteOverlay) lines(innerWidth int) []string {
	if note.layout == nil {
		return layoutLineNote(note.text, innerWidth)
	}
	if note.layout.lines == nil || note.layout.innerWidth != innerWidth {
		note.layout.innerWidth = innerWidth
		note.layout.lines = layoutLineNote(note.text, innerWidth)
	}
	return note.layout.lines
}

// lineNoteBox is where the note box sits in the file pane body: columns from
// the pane's left edge, rows from its top (rows outside [0, height) clip).
type lineNoteBox struct {
	left, width int
	top         int // the top border's row
	innerWidth  int
	lines       []string // the whole laid-out note
	bodyRows    int      // note rows shown at once
	scroll      int      // clamped first note row shown
}

func (box lineNoteBox) height() int { return box.bodyRows + 2 }

// lineNoteGeometry places the box in renderFile's coordinates: right of the
// gutter with a margin, as wide as the note needs up to the pane, below the
// note's line — or above it when only that fits, or when below would hide
// the cursor. ok=false when the pane is too narrow for a box.
func (m Model) lineNoteGeometry(start, gutterWidth, contentWidth, height int) (lineNoteBox, bool) {
	available := contentWidth - 2*lineNoteMargin
	if available < 12 || height < 4 {
		return lineNoteBox{}, false
	}
	note := m.lineNote
	box := lineNoteBox{left: gutterWidth + 3 + lineNoteMargin}
	box.width = input.Clamp(note.naturalWidth+4, min(lineNoteMinWidth, available), min(available, lineNoteMaxWidth))
	box.innerWidth = box.width - 4 // border + one cell of padding per side
	box.lines = note.lines(box.innerWidth)
	box.bodyRows = min(len(box.lines), max(3, height/2-2))
	box.scroll = input.Clamp(note.scroll, 0, len(box.lines)-box.bodyRows)

	anchorRow := note.line - start
	cursorRow := m.edit.cy - start
	belowTop, aboveTop := anchorRow+1, anchorRow-box.height()
	fitsBelow := belowTop+box.height() <= height
	fitsAbove := aboveTop >= 0
	covers := func(top int) bool { return cursorRow >= top && cursorRow < top+box.height() }

	box.top = belowTop
	if !fitsBelow && anchorRow > height-belowTop {
		box.top = aboveTop
	}
	switch {
	case box.top == belowTop && covers(belowTop) && fitsAbove && !covers(aboveTop):
		box.top = aboveTop
	case box.top == aboveTop && covers(aboveTop) && fitsBelow && !covers(belowTop):
		box.top = belowTop
	}
	return box, true
}

// lineNoteScreenBox is the box's geometry for the mouse: renderFile's edit
// mode inputs, as editClickTarget derives them.
func (m Model) lineNoteScreenBox() (lineNoteBox, bool) {
	if !m.lineNoteVisible() || len(m.edit.lines) == 0 {
		return lineNoteBox{}, false
	}
	height := m.contentHeight() + 1
	total := len(m.edit.lines)
	start := fileViewportTop(m.edit.cy, m.fileScrollY, height, total)
	mainWidth := max(3, m.width-m.sidebarWidth())
	gutterWidth := len(strconv.Itoa(max(total, height)))
	contentWidth := max(1, (mainWidth-4)-gutterWidth-3)
	box, ok := m.lineNoteGeometry(start, gutterWidth, contentWidth, height)
	if !ok {
		return lineNoteBox{}, false
	}
	// Clip to the rows actually drawn.
	if box.top+box.height() <= 0 || box.top >= height {
		return lineNoteBox{}, false
	}
	return box, true
}

// lineNoteHit reports whether terminal cell (x, y) lands on the note box.
func (m Model) lineNoteHit(x, y int) (lineNoteBox, bool) {
	box, ok := m.lineNoteScreenBox()
	if !ok {
		return box, false
	}
	paneRow := y - 2 - m.tabRows()
	paneCol := x - (m.sidebarWidth() + 1)
	height := m.contentHeight() + 1
	inRows := paneRow >= max(0, box.top) && paneRow < min(height, box.top+box.height())
	inColumns := paneCol >= box.left && paneCol < box.left+box.width
	return box, inRows && inColumns
}

// lineNoteScrollableBox is the on-screen box when its note is taller than
// the box, so Shift+↑/↓ have something to scroll.
func (m Model) lineNoteScrollableBox() (lineNoteBox, bool) {
	box, ok := m.lineNoteScreenBox()
	return box, ok && len(box.lines) > box.bodyRows
}

// scrollLineNote moves the note's text by delta rows within its capped box.
func (m Model) scrollLineNote(box lineNoteBox, delta int) Model {
	m.lineNote.scroll = input.Clamp(box.scroll+delta, 0, len(box.lines)-box.bodyRows)
	return m
}

// dismissLineNote closes the note box.
func (m Model) dismissLineNote() Model {
	m.lineNote = lineNoteOverlay{}
	return m
}

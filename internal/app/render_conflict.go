package app

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/input"
)

// conflictLineKind classifies a buffer line against the parsed blocks for
// tinting: marker rows, the ours/theirs content they fence, the diff3 base
// section, or plain code.
type conflictLineKind uint8

const (
	conflictPlainLine conflictLineKind = iota
	conflictMarkerLine
	conflictOursLine
	conflictBaseLine
	conflictTheirsLine
)

// conflictLineKindAt classifies buffer line i. Blocks are in buffer order and
// never overlap, so the scan can stop at the first block starting past i.
func conflictLineKindAt(blocks []conflictBlock, i int) conflictLineKind {
	for _, b := range blocks {
		if i < b.start {
			break
		}
		if i > b.end {
			continue
		}
		oursEnd := b.mid
		if b.base != -1 {
			oursEnd = b.base
		}
		switch {
		case i == b.start || i == b.mid || i == b.end || (b.base != -1 && i == b.base):
			return conflictMarkerLine
		case i < oursEnd:
			return conflictOursLine
		case b.base != -1 && i < b.mid:
			return conflictBaseLine
		default:
			return conflictTheirsLine
		}
	}
	return conflictPlainLine
}

// renderConflict draws the conflict-solving view: the live edit buffer with
// conflict regions tinted (ours green, theirs blue, diff3 base gray, marker
// lines bold yellow) and, when the cursor sits on a marker line, the inline
// Use-HEAD/branch/both picker overlaid on that row.
func (m Model) renderConflict(width, height int) string {
	lines := m.edit.lines
	if len(lines) == 0 {
		return ""
	}

	gutterWidth := len(strconv.Itoa(max(len(lines), height)))
	contentWidth := max(1, width-gutterWidth-3)
	start := fileViewportTop(m.edit.cy, m.fileScrollY, height, len(lines))

	rows := make([]string, 0, height)
	for i := start; i < start+height; i++ {
		if i >= len(lines) {
			rows = append(rows, "")
			continue
		}
		kind := conflictLineKindAt(m.conflictBlocks, i)

		numText := pad(strconv.Itoa(i+1), gutterWidth) + " │ "
		var number string
		switch kind {
		case conflictOursLine:
			number = diffGutterAddStyle.Render(numText)
		case conflictTheirsLine:
			number = conflictGutterInStyle.Render(numText)
		case conflictMarkerLine:
			number = conflictGutterMarkStyle.Render(numText)
		default:
			number = gutterStyle.Render(numText)
		}

		var content string
		if i == m.edit.cy {
			content = renderDiffCursorLine(lines[i], m.edit.cx, contentWidth, conflictCursorRowStyle(kind))
		} else {
			switch kind {
			case conflictMarkerLine:
				content = plainWindowStyled(lines[i], 0, contentWidth, conflictMarkerStyle)
			case conflictOursLine:
				content = m.renderDiffBufLine(i, lines[i], contentWidth, hexDiffAddBg, diffAddTextStyle)
			case conflictTheirsLine:
				content = m.renderDiffBufLine(i, lines[i], contentWidth, hexConflictInBg, conflictInTextStyle)
			case conflictBaseLine:
				content = plainWindowStyled(lines[i], 0, contentWidth, conflictBaseTextStyle)
			default:
				content = m.renderContentLine(i, lines[i], 0, contentWidth)
			}
		}
		rows = append(rows, number+content)
	}

	if idx, ok := m.conflictBlockOnMarker(m.edit.cy); ok {
		rows = m.overlayConflictPopup(rows, idx, start, gutterWidth, contentWidth)
	}
	return strings.Join(rows, "\n")
}

// conflictCursorRowStyle picks the cursor row's fill: tinted rows keep their
// tint so the context stays visible under the cursor; plain rows get the
// regular current-line highlight.
func conflictCursorRowStyle(kind conflictLineKind) lipgloss.Style {
	switch kind {
	case conflictMarkerLine:
		return conflictMarkerStyle
	case conflictOursLine:
		return diffAddTextStyle
	case conflictTheirsLine:
		return conflictInTextStyle
	case conflictBaseLine:
		return conflictBaseTextStyle
	default:
		return cursorLineStyle
	}
}

// overlayConflictPopup splices the one-row option picker onto the cursor's
// marker row, right after the marker text — the same ANSI-aware slicing as
// overlayCompletion, without any vertical placement logic (the strip lives on
// the row it acts on, so it can never fall off the viewport).
func (m Model) overlayConflictPopup(rows []string, blockIdx, start, gutterWidth, contentWidth int) []string {
	rowIdx := m.edit.cy - start
	if rowIdx < 0 || rowIdx >= len(rows) {
		return rows
	}

	labels := conflictOptionLabels(m.conflictBlocks[blockIdx])
	sel := input.Clamp(m.conflictChoice, 0, 2)
	var b strings.Builder
	for i, label := range labels {
		s := completionItemStyle
		if i == sel {
			s = completionSelStyle
		}
		b.WriteString(s.Render(" " + label + " "))
		if i < len(labels)-1 {
			b.WriteString(completionItemStyle.Render("·"))
		}
	}
	box := b.String()
	w := ansi.StringWidth(box)

	// Anchor two cells after the marker text, pulled left (and hard-truncated)
	// when the strip would overflow the content area.
	markerWidth := len([]rune(m.edit.lines[m.edit.cy]))
	anchorCol := input.Clamp(markerWidth+2, 0, max(0, contentWidth-w))
	if w > contentWidth {
		box = ansi.Truncate(box, contentWidth, "")
		w = contentWidth
	}
	anchor := gutterWidth + 3 + anchorCol

	orig := rows[rowIdx]
	left := ansi.Truncate(orig, anchor, "")
	if pad := anchor - ansi.StringWidth(left); pad > 0 {
		left += baseStyle.Render(strings.Repeat(" ", pad))
	}
	right := ansi.TruncateLeft(orig, anchor+w, "")
	rows[rowIdx] = left + box + right
	return rows
}

// renderConflictStatus is the single-line status row for conflict-solving
// mode: file, remaining conflict count, position, transient feedback, hints.
func (m Model) renderConflictStatus() string {
	name := ""
	if m.openFile != nil {
		name = m.openFile.FileName
	}
	line := promptStyle.Render("@conflict") + statusTextStyle.Render(" "+name+"   ")
	if n := len(m.conflictBlocks); n > 0 {
		line += editingStyle.Render(fmt.Sprintf("%d conflict(s) left", n))
	} else {
		line += noticeStyle.Render("all conflicts resolved")
	}
	line += statusTextStyle.Render(fmt.Sprintf("   Ln %d, Col %d", m.edit.cy+1, m.edit.cx+1))
	if m.notice != "" {
		line += statusTextStyle.Render("   ") + noticeStyle.Render(m.notice)
	}
	if m.errText != "" {
		line += statusTextStyle.Render("   ") + errStyle.Render(m.errText)
	}
	return line + statusTextStyle.Render("   ") +
		hintStyle.Render("←/→ pick · Enter apply · Shift+↑/↓ conflict · Esc done")
}

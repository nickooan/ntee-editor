package app

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/nickooan/ntee-editor/internal/input"
)

// renderDiff draws the diff review view: a unified GitHub-style listing of
// the edit buffer vs the git base. Gutter numbers are the buffer's (new-file)
// line numbers — they advance only past ctx/add rows; del rows show a blank
// red gutter. Only the visible window of rows is rendered, and ctx/add rows
// reuse the edit session's highlight cache.
func (m Model) renderDiff(width, height int) string {
	if m.diffLoading {
		return baseStyle.Render("computing diff…")
	}
	total := len(m.diffRows)
	if total == 0 {
		return ""
	}

	// Same gutter formula as renderFile, keyed to buffer line count so the
	// numbers align with what Esc returns to.
	gutterWidth := len(strconv.Itoa(max(len(m.edit.lines), height)))
	contentWidth := max(1, width-gutterWidth-3)
	start := fileViewportTop(m.diffCursor, m.diffScrollY, height, total)

	out := make([]string, 0, height)
	for i := start; i < start+height; i++ {
		if i >= total {
			out = append(out, "")
			continue
		}
		row := m.diffRows[i]
		text := m.diffRowText(i)

		var number string
		switch row.kind {
		case diffAdd:
			number = diffGutterAddStyle.Render(pad(strconv.Itoa(row.bufLine+1), gutterWidth) + " │ ")
		case diffDel:
			number = diffGutterDelStyle.Render(pad("", gutterWidth) + " │ ")
		default:
			number = gutterStyle.Render(pad(strconv.Itoa(row.bufLine+1), gutterWidth) + " │ ")
		}

		var content string
		if i == m.diffCursor {
			content = renderDiffCursorLine(text, m.diffCx, contentWidth, diffCursorRowStyle(row.kind))
		} else {
			switch row.kind {
			case diffAdd:
				content = m.renderDiffBufLine(row.bufLine, text, contentWidth, hexDiffAddBg, diffAddTextStyle)
			case diffDel:
				content = plainWindowStyled(text, 0, contentWidth, diffDelTextStyle)
			default:
				content = m.renderDiffBufLine(row.bufLine, text, contentWidth, hexBg, baseStyle)
			}
		}
		out = append(out, number+content)
	}
	return strings.Join(out, "\n")
}

// renderDiffBufLine draws a ctx/add row from the highlight cache when its
// buffer line has a cached row (valid here: diff mode sits on the live edit
// session and blocks edits), plain in the row's background otherwise.
func (m Model) renderDiffBufLine(bufLine int, text string, width int, bg string, style lipgloss.Style) string {
	if m.hlLines != nil && bufLine >= 0 && bufLine < len(m.hlLines) && m.hlLines[bufLine] != nil {
		return renderSegmentsBg(m.hlLines[bufLine], 0, width, bg, style)
	}
	return plainWindowStyled(text, 0, width, style)
}

// diffCursorRowStyle picks the cursor row's fill: changed rows keep their
// red/green tint (the review context must stay visible under the cursor);
// unchanged rows get the regular current-line highlight.
func diffCursorRowStyle(kind diffRowKind) lipgloss.Style {
	switch kind {
	case diffAdd:
		return diffAddTextStyle
	case diffDel:
		return diffDelTextStyle
	default:
		return cursorLineStyle
	}
}

// renderDiffCursorLine draws the review cursor's row: renderEditLine's
// horizontal-window walk, minus selections, with the row's diff background
// instead of an unconditional cursor-line repaint.
func renderDiffCursorLine(line string, cx, width int, lineStyle lipgloss.Style) string {
	if width < 1 {
		width = 1
	}
	runes := []rune(line)
	n := len(runes)
	at := input.Clamp(cx, 0, n)

	off := 0
	if at >= width {
		off = at - width + 1 // keep the cursor at the right edge once past it
	}
	end := min(off+width, n)
	curCol := at - off

	var b strings.Builder
	for col := 0; col < width; col++ {
		idx := off + col
		ch := " "
		if idx < end {
			ch = string(runes[idx])
		}
		if col == curCol {
			b.WriteString(cursorStyle.Render(ch))
		} else {
			b.WriteString(lineStyle.Render(ch))
		}
	}
	return b.String()
}

// renderDiffStatus is the single-line status row for diff review mode:
// file, base, add/del counts, position, transient feedback, and key hints.
func (m Model) renderDiffStatus() string {
	name := ""
	if m.openFile != nil {
		name = m.openFile.FileName
	}
	base := m.diffBase
	if base == "" {
		base = "HEAD"
	}
	line := promptStyle.Render("@diff") + statusTextStyle.Render(" "+name+"   base "+base)
	if m.diffNewFile {
		line += statusTextStyle.Render("   ") + noticeStyle.Render("new file")
	}
	if m.diffLoading {
		line += statusTextStyle.Render("   ") + editingStyle.Render("computing…")
	} else {
		line += statusTextStyle.Render("   ") +
			noticeStyle.Render(fmt.Sprintf("+%d", m.diffAdds)) + statusTextStyle.Render(" ") +
			errStyle.Render(fmt.Sprintf("-%d", m.diffDels)) +
			statusTextStyle.Render(fmt.Sprintf("   Ln %d/%d", m.diffCursor+1, len(m.diffRows)))
	}
	if m.notice != "" {
		line += statusTextStyle.Render("   ") + noticeStyle.Render(m.notice)
	}
	if m.errText != "" {
		line += statusTextStyle.Render("   ") + errStyle.Render(m.errText)
	}
	return line + statusTextStyle.Render("   ") +
		hintStyle.Render("↑/↓ review · PgUp/PgDn page · Ctrl+ J def, O back · Esc exit")
}

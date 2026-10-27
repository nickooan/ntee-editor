package app

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/keymap"
	"github.com/nickooan/ntee-editor/internal/view"
)

// renderDiff draws the diff review view: the edit buffer vs the git base,
// reusing the edit session's highlight cache, with a session step's box on
// its line.
func (m Model) renderDiff(width, height int) string {
	if m.diffLoading {
		return baseStyle.Render("computing diff…")
	}
	body := renderDiffDocument(m.liveDiffDocument(), m.diffPosition, diffSearchOverlay{}, width, height, m.tabWidthFor(m.openRel))
	if !m.lineNoteVisibleInDiff() {
		return body
	}
	anchorRow, cursorRow, gutterWidth, ok := m.diffNoteRows(height)
	if !ok {
		return body
	}
	rows := m.overlayLineNote(strings.Split(body, "\n"), anchorRow, cursorRow, gutterWidth, max(1, width-gutterWidth-3), height)
	return strings.Join(rows, "\n")
}

// diffSearchOverlay carries a document search's matches into the diff
// renderer: matched rows draw through renderSearchLine. Zero = no search.
type diffSearchOverlay struct {
	byLine  map[int][]view.LineMatch
	focused int
}

// renderDiffDocument draws a unified GitHub-style listing of a diff document.
// Gutter numbers are the new side's line numbers — they advance only past
// ctx/add rows; del rows show a blank red gutter. Only the visible window of
// rows is rendered, and ctx/add rows use the document's highlight cache.
func renderDiffDocument(document diffDocument, position rowCursor, search diffSearchOverlay, width, height, tabWidth int) string {
	total := len(document.rows)
	if total == 0 {
		return ""
	}

	// Same gutter formula as renderFile, keyed to the new side's line count
	// so the numbers align with what Esc returns to.
	gutterWidth := diffGutterWidth(document, height)
	contentWidth := max(1, width-gutterWidth-3)
	start := fileViewportTop(position.row, position.scrollY, height, total)

	out := make([]string, 0, height)
	for i := start; i < start+height; i++ {
		if i >= total {
			out = append(out, "")
			continue
		}
		row := document.rows[i]
		text := document.rowText(i)

		// The +/- marker sits in the spacer column after the number ("117+│ "),
		// keeping the gutter exactly gutterWidth+3 columns like renderFile.
		var number string
		switch row.kind {
		case diffAdd:
			number = diffGutterAddStyle.Render(pad(strconv.Itoa(row.bufLine+1), gutterWidth)) +
				diffMarkAddStyle.Render("+") + diffGutterAddStyle.Render("│ ")
		case diffDel:
			number = diffGutterDelStyle.Render(pad("", gutterWidth)) +
				diffMarkDelStyle.Render("-") + diffGutterDelStyle.Render("│ ")
		default:
			number = gutterStyle.Render(pad(strconv.Itoa(row.bufLine+1), gutterWidth) + " │ ")
		}

		var content string
		if matches := search.byLine[i]; len(matches) > 0 {
			content = renderSearchLine(text, document.rowHighlights(i), matches, nil, search.focused, 0, contentWidth, tabWidth)
		} else if i == position.row {
			content = renderDiffCursorLine(text, position.column, contentWidth, diffCursorRowStyle(row.kind), tabWidth)
		} else {
			switch row.kind {
			case diffAdd:
				content = renderHighlightedLine(document.highlights, row.bufLine, text, contentWidth, hexDiffAddBg, diffAddTextStyle, tabWidth)
			case diffDel:
				content = plainWindowStyled(text, 0, contentWidth, diffDelTextStyle, tabWidth)
			default:
				content = renderHighlightedLine(document.highlights, row.bufLine, text, contentWidth, hexBg, baseStyle, tabWidth)
			}
		}
		out = append(out, number+content)
	}
	return strings.Join(out, "\n")
}

// diffGutterWidth is the line-number column width of a diff document drawn
// in height rows — shared with click hit-testing so both agree.
func diffGutterWidth(document diffDocument, height int) int {
	return len(strconv.Itoa(max(len(document.lines), height)))
}

// renderDiffBufLine draws a buffer line from the edit session's highlight
// cache (valid in the views that sit on the live edit session and block
// edits), plain in the row's background otherwise.
func (m Model) renderDiffBufLine(bufLine int, text string, width int, bg string, style lipgloss.Style) string {
	return renderHighlightedLine(m.hlLines, bufLine, text, width, bg, style, m.tabWidthFor(m.openRel))
}

// renderHighlightedLine draws line bufLine from a highlight cache when it has
// a cached row, plain in the row's background otherwise.
func renderHighlightedLine(highlights [][]view.HighlightSegment, bufLine int, text string, width int, bg string, style lipgloss.Style, tabWidth int) string {
	if highlights != nil && bufLine >= 0 && bufLine < len(highlights) && highlights[bufLine] != nil {
		return renderSegmentsBg(highlights[bufLine], 0, width, bg, style, tabWidth)
	}
	return plainWindowStyled(text, 0, width, style, tabWidth)
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
func renderDiffCursorLine(line string, cx, width int, lineStyle lipgloss.Style, tabWidth int) string {
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

	// Three maximal runs — lineStyle prefix, the cursor cell, lineStyle
	// suffix+padding — instead of a Render per column.
	window := make([]rune, width)
	for col := 0; col < width; col++ {
		if idx := off + col; idx < end {
			window[col] = runes[idx]
		} else {
			window[col] = ' '
		}
	}
	var b strings.Builder
	if curCol > 0 {
		b.WriteString(renderRun(lineStyle, string(window[:curCol]), tabWidth))
	}
	b.WriteString(renderRun(cursorStyle, string(window[curCol:curCol+1]), tabWidth))
	if curCol+1 < width {
		b.WriteString(renderRun(lineStyle, string(window[curCol+1:]), tabWidth))
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
	if m.session.id != "" {
		line += statusTextStyle.Render("   ") + sessionBadgeStyle.Render(m.sessionBadge())
	}
	if m.diffDocument.newFile {
		line += statusTextStyle.Render("   ") + noticeStyle.Render("new file")
	}
	if m.diffLoading {
		line += statusTextStyle.Render("   ") + editingStyle.Render("computing…")
	} else {
		line += statusTextStyle.Render("   ") +
			noticeStyle.Render(fmt.Sprintf("+%d", m.diffDocument.adds)) + statusTextStyle.Render(" ") +
			errStyle.Render(fmt.Sprintf("-%d", m.diffDocument.dels)) +
			statusTextStyle.Render(fmt.Sprintf("   Ln %d/%d", m.diffPosition.row+1, len(m.diffDocument.rows)))
	}
	if m.notice != "" {
		line += statusTextStyle.Render("   ") + noticeStyle.Render(m.notice)
	}
	if m.errText != "" {
		line += statusTextStyle.Render("   ") + errStyle.Render(m.errText)
	}
	if m.sessionStepVisible() {
		return line + statusTextStyle.Render("   ") + hintStyle.Render("↑/↓ review · Shift+↑/↓ hunk · PgUp/PgDn page · Esc end session")
	}
	return line + statusTextStyle.Render("   ") +
		hintStyle.Render("↑/↓ review · Shift+↑/↓ hunk · PgUp/PgDn page · "+m.actionHints(
			hintFor("def", keymap.JumpToReference), hintFor("back", keymap.JumpBack))+" · Esc exit")
}

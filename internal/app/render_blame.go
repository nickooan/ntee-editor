package app

import (
	"fmt"
	"strings"
)

// blameDateWidth is the fixed date column width ("2006-01-02").
const blameDateWidth = 10

// blameGutterWidth is the annotation gutter's column count, before the same
// 3-column " │ " spacer every file view uses: author column + space + date.
func (m Model) blameGutterWidth() int {
	return m.blameAuthorW + 1 + blameDateWidth
}

// renderBlame draws the blame view: the edit buffer with a per-line
// author+date gutter — no line numbers. Rows map 1:1 to buffer lines and
// reuse the edit session's highlight cache; uncommitted lines (unsaved edits,
// new files) carry a dimmed placeholder instead of an author.
func (m Model) renderBlame(width, height int) string {
	if m.blameLoading {
		return baseStyle.Render("computing blame…")
	}
	total := len(m.blameRows)
	if total == 0 {
		return ""
	}

	gutterWidth := m.blameGutterWidth()
	contentWidth := max(1, width-gutterWidth-3)
	start := fileViewportTop(m.blameCursor, m.blameScrollY, height, total)

	out := make([]string, 0, height)
	for i := start; i < start+height; i++ {
		if i >= total {
			out = append(out, "")
			continue
		}
		row := m.blameRows[i]
		text := m.blameRowText(i)

		var annot string
		if row.uncommitted {
			annot = blameDimStyle.Render(padTo(blameUncommittedLabel, gutterWidth)) +
				gutterStyle.Render(" │ ")
		} else {
			annot = blameAuthorStyle.Render(padTo(truncateRunes(row.author, m.blameAuthorW), m.blameAuthorW)) +
				blameDateStyle.Render(" "+padTo(row.date, blameDateWidth)) +
				gutterStyle.Render(" │ ")
		}

		var content string
		if i == m.blameCursor {
			content = renderDiffCursorLine(text, m.blameCx, contentWidth, cursorLineStyle)
		} else {
			content = m.renderDiffBufLine(i, text, contentWidth, hexBg, baseStyle)
		}
		out = append(out, annot+content)
	}
	return strings.Join(out, "\n")
}

// renderBlameStatus is the single-line status row for blame mode: file,
// position, transient feedback, and key hints.
func (m Model) renderBlameStatus() string {
	name := ""
	if m.openFile != nil {
		name = m.openFile.FileName
	}
	line := promptStyle.Render("@blame") + statusTextStyle.Render(" "+name)
	if m.blameNewFile {
		line += statusTextStyle.Render("   ") + noticeStyle.Render("new file")
	}
	if m.blameLoading {
		line += statusTextStyle.Render("   ") + editingStyle.Render("computing…")
	} else {
		line += statusTextStyle.Render(fmt.Sprintf("   Ln %d/%d", m.blameCursor+1, len(m.blameRows)))
	}
	if m.notice != "" {
		line += statusTextStyle.Render("   ") + noticeStyle.Render(m.notice)
	}
	if m.errText != "" {
		line += statusTextStyle.Render("   ") + errStyle.Render(m.errText)
	}
	return line + statusTextStyle.Render("   ") +
		hintStyle.Render("↑/↓ move · Shift+↑/↓ commit · PgUp/PgDn page · Ctrl+ J def, O back · Esc exit")
}

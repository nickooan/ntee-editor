package app

import (
	"fmt"
	"strings"

	"github.com/nickooan/ntee-editor/internal/view"
)

// renderPreview draws the preview's main pane: a window over the rendered
// document rows, the cursor row tinted like edit mode's cursor line, and
// search matches overlaid via renderSearchLine (match backgrounds win over
// the row's own colors, like the search view).
func (m Model) renderPreview(width, height int) string {
	if m.preview.loading {
		return baseStyle.Render(m.previewDesc().loadingText)
	}
	total := len(m.preview.lines)
	if total == 0 {
		return ""
	}
	start := fileViewportTop(m.preview.cursor, m.preview.scrollY, height, total)

	var byLine map[int][]view.LineMatch
	focused := -1
	if m.preview.searching && m.preview.search != "" {
		byLine = m.previewMC.matchesByLine(m.preview.corpus, m.preview.search)
		focused = m.preview.focused
	}

	rows := make([]string, 0, height)
	for i := start; i < start+height; i++ {
		if i >= total {
			rows = append(rows, "")
			continue
		}
		line := m.preview.lines[i]
		switch {
		case len(byLine[i]) > 0:
			rows = append(rows, renderSearchLine(m.preview.plain[i], line.Segs, byLine[i], nil, focused, 0, width))
		case i == m.preview.cursor:
			rows = append(rows, renderSegmentsBg(line.Segs, 0, width, hexLineHl, cursorLineStyle))
		default:
			rows = append(rows, renderSegments(line.Segs, 0, width))
		}
	}
	return strings.Join(rows, "\n")
}

// renderPreviewStatus is the status row for the preview: search bar while
// searching, otherwise file + document title + position + key hints.
func (m Model) renderPreviewStatus() string {
	k := m.previewDesc()
	if m.preview.searching {
		matches := m.previewMatches()
		summary := fmt.Sprintf("%d matches", len(matches))
		if len(matches) > 0 {
			summary = fmt.Sprintf("%d/%d", min(m.preview.focused+1, len(matches)), len(matches))
		}
		line := promptStyle.Render("@"+k.name+" /") + statusTextStyle.Render(m.preview.search+"/   "+summary)
		return withNotice(m, line) + statusTextStyle.Render("   ") +
			hintStyle.Render("↑/↓ next/prev · Enter go · Esc close")
	}

	name := ""
	if m.openFile != nil {
		name = m.openFile.FileName
	}
	line := promptStyle.Render("@"+k.name) + statusTextStyle.Render(" "+name)
	if m.preview.title != "" {
		line += statusTextStyle.Render("   ") + noticeStyle.Render(m.preview.title)
	}
	if m.preview.loading {
		line += statusTextStyle.Render("   ") + editingStyle.Render("rendering…")
	} else {
		line += statusTextStyle.Render(fmt.Sprintf("   Ln %d/%d", m.preview.cursor+1, len(m.preview.lines)))
	}
	if m.notice != "" {
		line += statusTextStyle.Render("   ") + noticeStyle.Render(m.notice)
	}
	if m.errText != "" {
		line += statusTextStyle.Render("   ") + errStyle.Render(m.errText)
	}
	return line + statusTextStyle.Render("   ") +
		hintStyle.Render("↑/↓ move · Shift+↑/↓ outline · Enter jump · / search · Esc exit")
}

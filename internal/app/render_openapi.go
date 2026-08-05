package app

import (
	"fmt"
	"strings"

	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/openapi"
	"github.com/nickooan/ntee-editor/internal/view"
)

// renderOpenAPI draws the preview's main pane: a window over the rendered
// document rows, the cursor row tinted like edit mode's cursor line, and
// search matches overlaid via renderSearchLine (match backgrounds win over
// the row's own colors, like the search view).
func (m Model) renderOpenAPI(width, height int) string {
	if m.openapiLoading {
		return baseStyle.Render("rendering openapi document…")
	}
	total := len(m.openapiLines)
	if total == 0 {
		return ""
	}
	start := fileViewportTop(m.openapiCursor, m.openapiScrollY, height, total)

	var byLine map[int][]view.LineMatch
	focused := -1
	if m.openapiSearching && m.openapiSearch != "" {
		byLine = view.BuildMatchesByLine(m.openapiMatches())
		focused = m.openapiFocused
	}

	rows := make([]string, 0, height)
	for i := start; i < start+height; i++ {
		if i >= total {
			rows = append(rows, "")
			continue
		}
		line := m.openapiLines[i]
		switch {
		case len(byLine[i]) > 0:
			rows = append(rows, renderSearchLine(m.openapiPlain[i], line.Segs, byLine[i], nil, focused, 0, width))
		case i == m.openapiCursor:
			rows = append(rows, renderSegmentsBg(line.Segs, 0, width, hexLineHl, cursorLineStyle))
		default:
			rows = append(rows, renderSegments(line.Segs, 0, width))
		}
	}
	return strings.Join(rows, "\n")
}

// renderOpenAPISidebar draws the outline pane replacing the file tree: tag
// group headers and method+path rows, selection tracking the document cursor.
func (m Model) renderOpenAPISidebar(width, height int) string {
	o := m.openapiOutline
	if len(o) == 0 || height < 1 {
		return dirStyle.Render(padTo(truncateRunes(" outline", width), width))
	}
	start := input.Clamp(m.openapiSel-height/2, 0, max(0, len(o)-height))
	rows := make([]string, 0, height)
	for i := start; i < min(start+height, len(o)); i++ {
		e := o[i]
		switch {
		case i == m.openapiSel:
			label := " " + e.Label
			if e.Depth > 0 {
				label = "  " + padTo(e.Method, 7) + e.Path
			}
			rows = append(rows, selectedEntryStyle.Render(padTo(truncateRunes(label, width), width)))
		case e.Depth == 0:
			rows = append(rows, dirStyle.Render(padTo(truncateRunes(" "+e.Label, width), width)))
		default:
			method := segStyleFor(view.HighlightSegment{Color: openapi.MethodColor(e.Method), Bold: true}).
				Render("  " + padTo(e.Method, 7))
			path := fileStyle.Render(padTo(truncateRunes(e.Path, max(1, width-9)), max(1, width-9)))
			rows = append(rows, method+path)
		}
	}
	return strings.Join(rows, "\n")
}

// renderOpenAPIStatus is the status row for the preview: search bar while
// searching, otherwise file + spec title + position + key hints.
func (m Model) renderOpenAPIStatus() string {
	if m.openapiSearching {
		matches := m.openapiMatches()
		summary := fmt.Sprintf("%d matches", len(matches))
		if len(matches) > 0 {
			summary = fmt.Sprintf("%d/%d", min(m.openapiFocused+1, len(matches)), len(matches))
		}
		line := promptStyle.Render("@openapi /") + statusTextStyle.Render(m.openapiSearch+"/   "+summary)
		return withNotice(m, line) + statusTextStyle.Render("   ") +
			hintStyle.Render("↑/↓ next/prev · Enter go · Esc close")
	}

	name := ""
	if m.openFile != nil {
		name = m.openFile.FileName
	}
	line := promptStyle.Render("@openapi") + statusTextStyle.Render(" "+name)
	if m.openapiTitle != "" {
		line += statusTextStyle.Render("   ") + noticeStyle.Render(m.openapiTitle)
	}
	if m.openapiLoading {
		line += statusTextStyle.Render("   ") + editingStyle.Render("rendering…")
	} else {
		line += statusTextStyle.Render(fmt.Sprintf("   Ln %d/%d", m.openapiCursor+1, len(m.openapiLines)))
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

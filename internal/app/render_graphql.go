package app

import (
	"fmt"
	"strings"

	"github.com/nickooan/ntee-editor/internal/graphql"
	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/view"
)

// renderGraphQL draws the preview's main pane: a window over the rendered
// schema rows, the cursor row tinted like edit mode's cursor line, and search
// matches overlaid via renderSearchLine (match backgrounds win over the row's
// own colors, like the search view).
func (m Model) renderGraphQL(width, height int) string {
	if m.graphqlLoading {
		return baseStyle.Render("rendering graphql schema…")
	}
	total := len(m.graphqlLines)
	if total == 0 {
		return ""
	}
	start := fileViewportTop(m.graphqlCursor, m.graphqlScrollY, height, total)

	var byLine map[int][]view.LineMatch
	focused := -1
	if m.graphqlSearching && m.graphqlSearch != "" {
		byLine = view.BuildMatchesByLine(m.graphqlMatches())
		focused = m.graphqlFocused
	}

	rows := make([]string, 0, height)
	for i := start; i < start+height; i++ {
		if i >= total {
			rows = append(rows, "")
			continue
		}
		line := m.graphqlLines[i]
		switch {
		case len(byLine[i]) > 0:
			rows = append(rows, renderSearchLine(m.graphqlPlain[i], line.Segs, byLine[i], nil, focused, 0, width))
		case i == m.graphqlCursor:
			rows = append(rows, renderSegmentsBg(line.Segs, 0, width, hexLineHl, cursorLineStyle))
		default:
			rows = append(rows, renderSegments(line.Segs, 0, width))
		}
	}
	return strings.Join(rows, "\n")
}

// renderGraphQLSidebar draws the outline pane replacing the file tree: kind
// group headers and badge+name rows, selection tracking the document cursor.
func (m Model) renderGraphQLSidebar(width, height int) string {
	o := m.graphqlOutline
	if len(o) == 0 || height < 1 {
		return dirStyle.Render(padTo(truncateRunes(" outline", width), width))
	}
	start := input.Clamp(m.graphqlSel-height/2, 0, max(0, len(o)-height))
	rows := make([]string, 0, height)
	for i := start; i < min(start+height, len(o)); i++ {
		e := o[i]
		switch {
		case i == m.graphqlSel:
			label := " " + e.Label
			if e.Depth > 0 {
				label = "  " + padTo(graphql.KindBadge(e.Kind), 6) + e.Label
			}
			rows = append(rows, selectedEntryStyle.Render(padTo(truncateRunes(label, width), width)))
		case e.Depth == 0:
			rows = append(rows, dirStyle.Render(padTo(truncateRunes(" "+e.Label, width), width)))
		default:
			badge := segStyleFor(view.HighlightSegment{Color: graphql.KindColor(e.Kind), Bold: true}).
				Render("  " + padTo(graphql.KindBadge(e.Kind), 6))
			name := fileStyle.Render(padTo(truncateRunes(e.Label, max(1, width-8)), max(1, width-8)))
			rows = append(rows, badge+name)
		}
	}
	return strings.Join(rows, "\n")
}

// renderGraphQLStatus is the status row for the preview: search bar while
// searching, otherwise file + schema summary + position + key hints.
func (m Model) renderGraphQLStatus() string {
	if m.graphqlSearching {
		matches := m.graphqlMatches()
		summary := fmt.Sprintf("%d matches", len(matches))
		if len(matches) > 0 {
			summary = fmt.Sprintf("%d/%d", min(m.graphqlFocused+1, len(matches)), len(matches))
		}
		line := promptStyle.Render("@graphql /") + statusTextStyle.Render(m.graphqlSearch+"/   "+summary)
		return withNotice(m, line) + statusTextStyle.Render("   ") +
			hintStyle.Render("↑/↓ next/prev · Enter go · Esc close")
	}

	name := ""
	if m.openFile != nil {
		name = m.openFile.FileName
	}
	line := promptStyle.Render("@graphql") + statusTextStyle.Render(" "+name)
	if m.graphqlTitle != "" {
		line += statusTextStyle.Render("   ") + noticeStyle.Render(m.graphqlTitle)
	}
	if m.graphqlLoading {
		line += statusTextStyle.Render("   ") + editingStyle.Render("rendering…")
	} else {
		line += statusTextStyle.Render(fmt.Sprintf("   Ln %d/%d", m.graphqlCursor+1, len(m.graphqlLines)))
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

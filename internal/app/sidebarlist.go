package app

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/view"
)

// sidebarSegment is one styled run of a sidebar row. minimumWidth pads text
// before the next run (the outline badge column). The last run fills whatever
// width remains.
type sidebarSegment struct {
	text         string
	style        lipgloss.Style
	minimumWidth int
}

// sidebarRow is one left-pane item. A selected row joins its segments and
// paints them with the selection bar; an unselected row keeps each segment's
// own style.
type sidebarRow struct {
	segments []sidebarSegment
}

// sidebarList is the left pane's content. selectedIndex is -1 when nothing
// is highlighted. Modes fill the rows; they do not scroll or draw the bar.
type sidebarList struct {
	rows          []sidebarRow
	selectedIndex int
}

// sidebarWindowStart is the first visible row. A negative selectedIndex
// starts at 0; otherwise the selected row is centered. Click hit-testing
// uses this same window, so the painted row and the clicked row cannot drift.
func sidebarWindowStart(rowCount, height, selectedIndex int) int {
	if height < 1 || rowCount == 0 {
		return 0
	}
	maxScroll := max(0, rowCount-height)
	if selectedIndex < 0 {
		return 0
	}
	next := selectedIndex - max(1, height)/2
	return min(max(next, 0), maxScroll)
}

// renderSidebarList draws the visible window of a sidebar list.
func renderSidebarList(list sidebarList, width, height int) string {
	if width < 1 || height < 1 || len(list.rows) == 0 {
		return ""
	}
	start := sidebarWindowStart(len(list.rows), height, list.selectedIndex)
	end := min(start+height, len(list.rows))
	lines := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		lines = append(lines, renderSidebarRow(list.rows[index], width, index == list.selectedIndex))
	}
	return strings.Join(lines, "\n")
}

func renderSidebarRow(row sidebarRow, width int, selected bool) string {
	if selected || len(row.segments) <= 1 {
		style := selectedEntryStyle
		if !selected && len(row.segments) == 1 {
			style = row.segments[0].style
		}
		return style.Render(padTo(truncateRunes(sidebarRowPlain(row), width), width))
	}
	var built strings.Builder
	used := 0
	for index, segment := range row.segments {
		text := sidebarSegmentText(segment)
		if index == len(row.segments)-1 {
			remaining := width - used
			if remaining <= 0 {
				break
			}
			built.WriteString(segment.style.Render(padTo(truncateRunes(text, remaining), remaining)))
			break
		}
		if used >= width {
			break
		}
		room := width - used
		runes := []rune(text)
		if len(runes) > room {
			text = string(runes[:room])
		}
		built.WriteString(segment.style.Render(text))
		used += len([]rune(text))
	}
	return built.String()
}

func sidebarRowPlain(row sidebarRow) string {
	var built strings.Builder
	for _, segment := range row.segments {
		built.WriteString(sidebarSegmentText(segment))
	}
	return built.String()
}

func sidebarSegmentText(segment sidebarSegment) string {
	if segment.minimumWidth > 0 {
		return padTo(segment.text, segment.minimumWidth)
	}
	return segment.text
}

// sidebarListForMode is the left pane for the current mode: the inspection
// menu, a document outline, or the file tree. width is the inner pane width
// the file-tree labels are fitted to.
func (m Model) sidebarListForMode(width int) sidebarList {
	switch m.mode {
	case modeInspect:
		return m.inspectSidebarList()
	case modeOpenAPI, modeGraphQL:
		return m.previewSidebarList()
	default:
		return m.fileTreeSidebarList(width)
	}
}

func (m Model) fileTreeSidebarList(width int) sidebarList {
	entries := m.treeEntries()
	if len(entries) == 0 {
		return sidebarList{
			rows:          []sidebarRow{{segments: []sidebarSegment{{text: "(empty)", style: baseStyle}}}},
			selectedIndex: -1,
		}
	}
	rows := make([]sidebarRow, len(entries))
	for index, entry := range entries {
		rows[index] = sidebarRow{segments: []sidebarSegment{{
			text:  filetree.FormatFileTreeEntryLabel(entry, width),
			style: m.fileTreeEntryStyle(entry),
		}}}
	}
	return sidebarList{rows: rows, selectedIndex: m.highlightedEntryIndex(entries)}
}

func (m Model) fileTreeEntryStyle(entry filetree.FileTreeEntry) lipgloss.Style {
	switch {
	// Uncommitted outranks the open-file green: "yellow instead of green"
	// is the signal that the open file has unsaved-to-git work.
	case entry.Uncommitted && entry.Type == "directory":
		return uncommittedDirStyle
	case entry.Uncommitted:
		return uncommittedFileStyle
	case entry.RelativePath == m.openRel && m.openRel != "":
		return openFileStyle
	case entry.Dimmed:
		return ignoredFileStyle
	case entry.Type == "directory":
		return dirStyle
	default:
		return fileStyle
	}
}

func (m Model) inspectSidebarList() sidebarList {
	rows := make([]sidebarRow, len(inspectMenuItems))
	for index, item := range inspectMenuItems {
		rows[index] = sidebarRow{segments: []sidebarSegment{{text: " " + item, style: dirStyle}}}
	}
	return sidebarList{rows: rows, selectedIndex: m.inspectMenu}
}

func (m Model) previewSidebarList() sidebarList {
	outline := m.preview.outline
	if len(outline) == 0 {
		return sidebarList{
			rows:          []sidebarRow{{segments: []sidebarSegment{{text: " outline", style: dirStyle}}}},
			selectedIndex: -1,
		}
	}
	kind := m.previewDesc()
	rows := make([]sidebarRow, len(outline))
	for index, entry := range outline {
		if entry.Depth == 0 {
			rows[index] = sidebarRow{segments: []sidebarSegment{{text: " " + entry.Label, style: dirStyle}}}
			continue
		}
		badgeStyle := segStyleFor(view.HighlightSegment{Color: entry.Color, Bold: true})
		rows[index] = sidebarRow{segments: []sidebarSegment{
			{text: "  " + padTo(entry.Badge, kind.badgeW), style: badgeStyle, minimumWidth: kind.badgeW + 2},
			{text: entry.Tail, style: fileStyle},
		}}
	}
	return sidebarList{rows: rows, selectedIndex: m.preview.sel}
}

// sidebarListClickIndex maps a terminal cell to a row of the current sidebar
// list. Rows start at y=2 (header plus the pane's top border) with no
// tab-strip offset — the strip lives in the main pane only — and inner
// columns 1..sidebarWidth-2.
func (m Model) sidebarListClickIndex(x, y int) (int, bool) {
	if x < 1 || x > m.sidebarWidth()-2 {
		return 0, false
	}
	row := y - 2
	height := m.sidebarInnerHeight()
	if row < 0 || row >= height {
		return 0, false
	}
	list := m.sidebarListForMode(m.sidebarWidth() - 4)
	index := sidebarWindowStart(len(list.rows), height, list.selectedIndex) + row
	if index < 0 || index >= len(list.rows) {
		return 0, false
	}
	return index, true
}

// activateSidebarRow applies a click on a sidebar row. Query and edit open or
// expand the file-tree entry. Inspection selects that menu row. A document
// preview jumps to the outline entry. Other modes draw the file tree but
// leave the click untouched.
func (m Model) activateSidebarRow(rowIndex int) (Model, bool) {
	switch m.mode {
	case modeInspect:
		if rowIndex < 0 || rowIndex >= len(inspectMenuItems) {
			return m, false
		}
		m.inspectMenu = rowIndex
		return m, true
	case modeOpenAPI, modeGraphQL:
		if rowIndex < 0 || rowIndex >= len(m.preview.outline) {
			return m, false
		}
		m.preview.sel = rowIndex
		return m.jumpPreviewOutline(), true
	case modeQuery, modeEdit:
		return m.activateFileTreeRow(rowIndex)
	default:
		return m, false
	}
}

// activateFileTreeRow opens a clicked file or expands a clicked directory,
// mirroring submitQuery's two branches — except that a click is not typing,
// so the directory case keeps the completion popup hidden.
func (m Model) activateFileTreeRow(rowIndex int) (Model, bool) {
	entries := m.treeEntries()
	if rowIndex < 0 || rowIndex >= len(entries) {
		return m, false
	}
	entry := entries[rowIndex]
	m.commandPreview = ""
	m.keyboardSelectedCommand = ""
	m.inputSuggestIndex = 0

	if entry.Type == "directory" {
		if m.mode == modeEdit {
			// Leaving edit mode by mouse keeps the unsaved work (unlike Esc,
			// which deliberately discards): stash exactly like a tab switch.
			m = m.recordCursor()
			m = m.flushBurst()
			m = m.stashDraftIfDirty()
			m = m.closeCompletion()
			m = m.sigUnpin()
			m.jumpStack = nil
			m.mode = modeQuery
			m = m.refreshFileHighlights()
		}
		m.selectedCommand = entry.CommandValue
		m.command = entry.CommandValue
		m.qCursor = len([]rune(m.command))
		m.suppressQuerySuggestions = true
		return m, true
	}

	m.suppressQuerySuggestions = false
	m.command, m.qCursor = "", 0
	if m.mode == modeEdit {
		m = m.closeCompletion()
	}
	return m.openFileAt(entry.RelativePath), true
}

package app

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/lsp"
	"github.com/nickooan/ntee-editor/internal/view"
)

func (m Model) View() string {
	if !m.ready {
		return "starting…"
	}

	header := headerStyle.Width(m.width).Render("ntee-editor  ·  " + m.root)
	status := m.padStatusRows(m.renderStatusLine())
	bodyHeight := max(3, m.height-2-strings.Count(status, "\n"))

	sidebarWidth := input.Clamp(m.width/4, 16, max(16, m.width-24))
	// Panes tile the full width — a spare column would show as a stripe of
	// terminal-default background against the themed panes.
	mainWidth := max(3, m.width-sidebarWidth)

	sidebar := paneStyle.Width(sidebarWidth - 2).Height(bodyHeight - 2).
		Render(m.renderSidebar(sidebarWidth-4, bodyHeight-2))

	// Overlays own the whole pane; otherwise the tab strip steals the top row.
	overlayOpen := m.fuzzyOpen || m.messageOverlay != "" || m.defPickOpen || m.grepOpen
	showTabs := len(m.tabs) > 0 && !overlayOpen
	innerH := bodyHeight - 2
	if showTabs {
		innerH -= 2 // tab strip + divider row
	}

	var mainBody string
	switch {
	case m.fuzzyOpen:
		mainBody = m.renderFuzzyOverlay(mainWidth-4, bodyHeight-2)
	case m.messageOverlay != "":
		mainBody = m.renderMessageOverlay(mainWidth-4, bodyHeight-2)
	case m.defPickOpen:
		mainBody = m.renderDefPickOverlay(mainWidth-4, bodyHeight-2)
	case m.grepOpen:
		mainBody = m.renderGrepOverlay(mainWidth-4, bodyHeight-2)
	case m.mode == modeSearch:
		mainBody = m.renderSearch(mainWidth-4, innerH)
	case m.mode == modeQuery:
		mainBody = m.renderQueryMain(mainWidth-4, innerH)
	case m.openFile != nil:
		mainBody = m.renderFile(mainWidth-4, innerH)
	default:
		mainBody = baseStyle.Render("Type a path or fuzzy fragment · enter opens · ctrl+p goto.")
	}
	if showTabs {
		divider := tabDividerStyle.Render(strings.Repeat("─", max(0, mainWidth-4)))
		mainBody = m.renderTabStrip(mainWidth-4) + "\n" + divider + "\n" + mainBody
	}
	mainPane := paneStyle.Width(mainWidth - 2).Height(bodyHeight - 2).Render(mainBody)

	body := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, mainPane)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, status)
}

// padStatusRows extends each status row to the full terminal width so the
// chrome background runs edge-to-edge.
func (m Model) padStatusRows(status string) string {
	rows := strings.Split(status, "\n")
	for i, row := range rows {
		if pad := m.width - lipgloss.Width(row); pad > 0 {
			rows[i] = row + statusTextStyle.Render(strings.Repeat(" ", pad))
		}
	}
	return strings.Join(rows, "\n")
}

func (m Model) renderStatusLine() string {
	switch m.mode {
	case modeQuery:
		// Navigation reflects the highlighted entry as a preview in the bar;
		// the typed command underneath stays editable (adopted on next key).
		line := promptStyle.Render("@query >") + statusTextStyle.Render(" ")
		if m.commandPreview != "" {
			line += previewStyle.Render(m.commandPreview) + cursorStyle.Render(" ")
		} else {
			line += renderInputLine(m.command, m.qCursor)
		}
		return withNotice(m, line) + "\n" +
			hintStyle.Render("Enter open+edit · Shift+↑/↓ tree · Esc parent · :recent · Ctrl + P goto / Q quit")
	case modeEdit:
		return m.renderEditStatus()
	case modeExec:
		// The @exec bar replaces the @edit status line while active (the @edit
		// line returns on exit); its lighter dark background signals the mode.
		bar := execPromptStyle.Render("@exec >") + renderInputLineStyled(m.execInput, m.execCursor, execTextStyle) +
			"   " + m.renderExecSugs()
		// Pre-pad to full width in the exec background so padStatusRows (which
		// pads with the chrome style) leaves this row's color intact.
		if pad := m.width - lipgloss.Width(bar); pad > 0 {
			bar += execTextStyle.Render(strings.Repeat(" ", pad))
		}
		return bar
	case modeSearch:
		matches := view.FindSearchMatches(m.searchContent, m.searchInput)
		summary := fmt.Sprintf("%d matches", len(matches))
		if len(matches) > 0 {
			summary = fmt.Sprintf("%d/%d", min(m.searchFocused+1, len(matches)), len(matches))
		}
		return promptStyle.Render("@search /") + statusTextStyle.Render(m.searchInput+"/   "+summary+"   ") +
			hintStyle.Render("↑/↓ next · Enter jump · Esc back")
	case modeCommand:
		return promptStyle.Render(":") + renderInputLine(m.cmdInput, m.cmdCursor) +
			statusTextStyle.Render("   ") + hintStyle.Render("jump <line|top|end> · tab <name|cl|cr> · revert · recent")
	}
	return ""
}

// renderEditStatus builds the single-line @edit status row: filename, saved/
// editing state, transient feedback, diagnostics, position, and key hints.
// Shared by modeEdit and modeExec (which stacks the @exec bar beneath it).
func (m Model) renderEditStatus() string {
	name := ""
	if m.openFile != nil {
		name = m.openFile.FileName
	}
	state := savedStyle.Render("saved")
	if m.edit.dirty {
		state = editingStyle.Render("editing")
	}
	pos := fmt.Sprintf("Ln %d, Col %d", m.edit.cy+1, m.edit.cx+1)
	line := promptStyle.Render("@edit") + statusTextStyle.Render(" "+name+"   ") + state
	// Transient feedback sits right after the saved/editing indicator — near the
	// front so the terminal never truncates it, and where the user expects the
	// "copied" note.
	if m.notice != "" {
		line += statusTextStyle.Render("   ") + noticeStyle.Render(m.notice)
	}
	if m.errText != "" {
		line += statusTextStyle.Render("   ") + errStyle.Render(m.errText)
	}
	line += m.diagSummary() + statusTextStyle.Render("   "+pos)
	if diag, ok := m.diagAtLine(m.edit.cy); ok {
		style := gutterWarnStyle
		if diag.Severity == 1 {
			style = errStyle
		}
		line += statusTextStyle.Render("   ") + style.Render(truncateRunes(diag.Message, 60))
	}
	return line + statusTextStyle.Render("   ") +
		hintStyle.Render("Ctrl+ S save, F find, A select, J/O jump/back, Z/Y undo/redo · Esc discard")
}

// diagSummary renders "✗N ⚠M " counts for the open file ("" when clean).
func (m Model) diagSummary() string {
	errs, warns := 0, 0
	for _, d := range m.diags[m.openRel] {
		if d.Severity == 1 {
			errs++
		} else {
			warns++
		}
	}
	out := ""
	if errs > 0 {
		out += statusTextStyle.Render("   ") + errStyle.Render(fmt.Sprintf("✗%d", errs))
	}
	if warns > 0 {
		out += statusTextStyle.Render("   ") + editingStyle.Render(fmt.Sprintf("⚠%d", warns))
	}
	return out
}

// diagAtLine returns the first diagnostic on a line of the open file.
func (m Model) diagAtLine(line int) (lsp.Diagnostic, bool) {
	for _, d := range m.diags[m.openRel] {
		if d.Line == line {
			return d, true
		}
	}
	return lsp.Diagnostic{}, false
}

func withNotice(m Model, line string) string {
	if m.errText != "" {
		line += statusTextStyle.Render("   ") + errStyle.Render(m.errText)
	}
	if m.notice != "" {
		line += statusTextStyle.Render("   ") + noticeStyle.Render(m.notice)
	}
	return line
}

func (m Model) renderSidebar(width, height int) string {
	entries := m.treeEntries()
	if len(entries) == 0 {
		return baseStyle.Render("(empty)")
	}
	highlighted := m.highlightedEntryIndex(entries)
	vp := filetree.BuildFileTreeViewport(entries, height, 0, highlighted)

	lines := make([]string, 0, len(vp.Entries))
	for i, entry := range vp.Entries {
		label := filetree.FormatFileTreeEntryLabel(entry, width)
		switch {
		case highlighted >= 0 && vp.SafeScrollY+i == highlighted:
			lines = append(lines, selectedEntryStyle.Render(label))
		// Uncommitted outranks the open-file green: "yellow instead of green"
		// is exactly the signal that the open file has unsaved-to-git work.
		case entry.Uncommitted && entry.Type == "directory":
			lines = append(lines, uncommittedDirStyle.Render(label))
		case entry.Uncommitted:
			lines = append(lines, uncommittedFileStyle.Render(label))
		case entry.RelativePath == m.openRel && m.openRel != "":
			lines = append(lines, openFileStyle.Render(label))
		case entry.Dimmed:
			lines = append(lines, ignoredFileStyle.Render(label))
		case entry.Type == "directory":
			lines = append(lines, dirStyle.Render(label))
		default:
			lines = append(lines, fileStyle.Render(label))
		}
	}
	return strings.Join(lines, "\n")
}

// renderExecSugs renders the @exec bar's inline suggestion strip: candidates
// for the trailing token joined with · , the Tab-acceptable one highlighted,
// capped at 6 with a +N overflow. With no candidates it falls back to the
// static usage hint.
func (m Model) renderExecSugs() string {
	if len(m.execSugs) == 0 {
		return execHintStyle.Render("copy [a-b|all|fpath] · jump <line|top|end> · tab <name|cl|cr> · git scf <side> · Esc cancel")
	}
	const maxShown = 6
	sel := input.Clamp(m.execSugIndex, 0, len(m.execSugs)-1)
	var parts []string
	for i, s := range m.execSugs {
		if i >= maxShown {
			parts = append(parts, execHintStyle.Render(fmt.Sprintf("+%d", len(m.execSugs)-maxShown)))
			break
		}
		if i == sel {
			parts = append(parts, execSugSelStyle.Render(s))
		} else {
			parts = append(parts, execHintStyle.Render(s))
		}
	}
	strip := strings.Join(parts, execHintStyle.Render(" · "))
	return strip + execHintStyle.Render("   Tab complete · ↑/↓ pick · Esc cancel")
}

// renderQueryMain shows the open file (view-style) with the suggestion popup
// pinned to the pane bottom while the bar has matches.
func (m Model) renderQueryMain(width, height int) string {
	popup := m.renderQuerySuggestions(width)
	contentHeight := max(1, height-len(popup))

	var body string
	if m.openFile != nil {
		body = m.renderFile(width, contentHeight)
	} else {
		body = baseStyle.Render("Type a path or fuzzy fragment · enter opens · shift+↑/↓ walks the tree.")
	}
	if len(popup) == 0 {
		return body
	}
	lines := strings.Split(body, "\n")
	for len(lines) < contentHeight {
		lines = append(lines, "")
	}
	return strings.Join(append(lines[:contentHeight], popup...), "\n")
}

// renderQuerySuggestions renders the completion popup rows (≤6 visible,
// windowed around the selection).
func (m Model) renderQuerySuggestions(width int) []string {
	entries := m.treeEntries()
	suggestions := m.queryInputSuggestions(entries)
	if len(suggestions) == 0 {
		return nil
	}
	const maxVisible = 6
	selected := input.Clamp(m.inputSuggestIndex, 0, len(suggestions)-1)
	start := 0
	if selected >= maxVisible {
		start = selected - maxVisible + 1
	}
	end := min(start+maxVisible, len(suggestions))

	rows := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		s := suggestions[i]
		label := padTo(truncateRunes(" "+s.Label, width), width)
		switch {
		case i == selected:
			rows = append(rows, selectedEntryStyle.Render(label))
		case s.Source == "directory":
			rows = append(rows, dirStyle.Render(label))
		default:
			rows = append(rows, suggestionFileStyle.Render(label))
		}
	}
	return rows
}

// renderTabStrip draws the open-file tabs across the top of the main pane: one
// cell per tab (base filename), the active tab highlighted, unsaved names red.
// A sliding window keeps the active tab visible when the strip overflows.
func (m Model) renderTabStrip(width int) string {
	cells := make([]string, len(m.tabs))
	widths := make([]int, len(m.tabs))
	for i, rel := range m.tabs {
		label := " " + truncateRunes(filepath.Base(rel), max(1, width-2)) + " "
		style := tabInactiveStyle
		switch {
		case i == m.tabActive && m.tabDirty(i):
			style = tabDirtyActiveStyle
		case i == m.tabActive:
			style = tabActiveStyle
		case m.tabDirty(i):
			style = tabDirtyInactiveStyle
		}
		cells[i] = style.Render(label)
		widths[i] = lipgloss.Width(label)
	}

	// Slide the window start right until [start..active] fits.
	start := 0
	for {
		used := 0
		for i := start; i <= m.tabActive; i++ {
			used += widths[i]
		}
		if used <= width || start >= m.tabActive {
			break
		}
		start++
	}

	var b strings.Builder
	used := 0
	for i := start; i < len(cells); i++ {
		if used+widths[i] > width {
			break
		}
		b.WriteString(cells[i])
		used += widths[i]
	}
	if pad := width - used; pad > 0 {
		b.WriteString(tabFillStyle.Render(strings.Repeat(" ", pad)))
	}
	return b.String()
}

// fileViewportTop is the first line drawn in the file pane: fileScrollY nudged to
// keep the cursor within a height-row window, then clamped. Shared by renderFile
// and edit-mode paging so both agree on what is on screen.
func fileViewportTop(cy, scrollY, height, total int) int {
	start := scrollY
	if cy < start {
		start = cy
	}
	if cy >= start+height {
		start = cy - height + 1
	}
	return input.Clamp(start, 0, max(0, total-height))
}

func (m Model) renderFile(width, height int) string {
	// modeExec pauses the editor over the live buffer, so it renders like edit
	// mode (cursor + selection stay visible behind the @exec bar).
	editing := m.mode == modeEdit || m.mode == modeExec
	var lines []string
	if editing {
		lines = m.edit.lines
	} else {
		lines = m.fileLines
		if lines == nil && m.openFile != nil {
			lines = view.NormalizeLines(m.openFile.Content)
		}
	}
	if len(lines) == 0 {
		return ""
	}

	gutterWidth := len(strconv.Itoa(max(len(lines), height)))
	contentWidth := max(1, width-gutterWidth-3)

	// Worst diagnostic severity per line for the gutter markers.
	lineSev := map[int]int{}
	for _, d := range m.diags[m.openRel] {
		if cur, ok := lineSev[d.Line]; !ok || d.Severity < cur {
			lineSev[d.Line] = d.Severity
		}
	}

	start := input.Clamp(m.fileScrollY, 0, max(0, len(lines)-height))
	if editing {
		// Keep the cursor line in view.
		start = fileViewportTop(m.edit.cy, m.fileScrollY, height, len(lines))
	}

	scrollX := 0
	if !editing {
		scrollX = max(0, m.fileScrollX)
	}

	rows := make([]string, 0, height)
	for i := start; i < start+height; i++ {
		if i >= len(lines) {
			rows = append(rows, "")
			continue
		}
		numText := pad(strconv.Itoa(i+1), gutterWidth)
		var number string
		switch lineSev[i] {
		case 1:
			number = gutterErrStyle.Render(numText+"●") + gutterStyle.Render("│ ")
		case 2, 3, 4:
			number = gutterWarnStyle.Render(numText+"●") + gutterStyle.Render("│ ")
		default:
			number = gutterStyle.Render(numText + " │ ")
		}
		var content string
		switch {
		case editing && i == m.edit.cy:
			content = renderEditLine(lines[i], m.edit.cx, contentWidth, m.edit.sel)
		case editing && m.edit.inLineSelection(i):
			content = renderSelectedLine(lines[i], contentWidth)
		default:
			content = m.renderContentLine(i, lines[i], scrollX, contentWidth)
		}
		rows = append(rows, number+content)
	}
	if editing && m.completionOpen {
		rows = m.overlayCompletion(rows, start, gutterWidth, contentWidth, height)
	}
	return strings.Join(rows, "\n")
}

// overlayCompletion splices the autocomplete dropdown onto the rendered file
// rows, anchored under the identifier being completed. v1 covers the code lines
// it sits over; the gutter/height math mirrors renderFile so the box lands at
// the cursor.
func (m Model) overlayCompletion(rows []string, start, gutterWidth, contentWidth, height int) []string {
	items := m.completionItems
	if len(items) == 0 {
		return rows
	}
	const maxRows = 8
	n := min(len(items), maxRows)
	sel := input.Clamp(m.completionIndex, 0, len(items)-1)
	top := 0
	if sel >= n {
		top = sel - n + 1
	}

	// Popup width: longest visible label + padding, capped to the content area.
	w := 12
	for i := 0; i < n; i++ {
		if lw := lipgloss.Width(items[top+i].Label) + 2; lw > w {
			w = lw
		}
	}
	w = min(w, contentWidth)

	// Anchor at the identifier start, within renderEditLine's horizontal window.
	line := m.edit.line()
	at := input.Clamp(m.edit.cx, 0, len(line))
	off := 0
	if at >= contentWidth {
		off = at - contentWidth + 1
	}
	anchor := gutterWidth + 3 + input.Clamp(identStart(line, m.edit.cx)-off, 0, max(0, contentWidth-w))

	curRow := m.edit.cy - start
	below := curRow+n < height // room beneath the cursor?
	for i := 0; i < n; i++ {
		idx := top + i
		label := padTo(truncateRunes(items[idx].Label, w), w)
		box := completionItemStyle.Render(label)
		if idx == sel {
			box = completionSelStyle.Render(label)
		}
		rowIdx := curRow + 1 + i
		if !below {
			rowIdx = curRow - n + i
		}
		if rowIdx < 0 || rowIdx >= len(rows) {
			continue
		}
		// Overlay the box onto the real row, preserving the gutter and the code
		// to the left/right (ANSI-aware slicing) rather than blanking the line.
		orig := rows[rowIdx]
		left := ansi.Truncate(orig, anchor, "")
		if pad := anchor - ansi.StringWidth(left); pad > 0 {
			left += baseStyle.Render(strings.Repeat(" ", pad))
		}
		right := ansi.TruncateLeft(orig, anchor+w, "")
		rows[rowIdx] = left + box + right
	}
	return rows
}

// renderContentLine draws one non-cursor line: styled from the highlight cache
// when a row is available, plain otherwise (nil rows cover un-lexed files,
// oversized files, and lines edited since the last rescan).
func (m Model) renderContentLine(i int, line string, off, width int) string {
	if m.hlLines != nil && i < len(m.hlLines) && m.hlLines[i] != nil {
		return renderSegments(m.hlLines[i], off, width)
	}
	return plainWindow(line, off, width)
}

func plainWindow(line string, off, width int) string {
	runes := []rune(line)
	start := input.Clamp(off, 0, len(runes))
	end := input.Clamp(start+width, 0, len(runes))
	out := string(runes[start:end])
	if pad := width - (end - start); pad > 0 {
		out += strings.Repeat(" ", pad)
	}
	return baseStyle.Render(out)
}

// renderSegments renders styled segments through a rune window [off, off+width),
// padding the remainder.
func renderSegments(segs []view.HighlightSegment, off, width int) string {
	var b strings.Builder
	skipped, rendered := 0, 0
	for _, seg := range segs {
		if rendered >= width {
			break
		}
		runes := []rune(seg.Text)
		i := 0
		if skipped < off {
			need := off - skipped
			if len(runes) <= need {
				skipped += len(runes)
				continue
			}
			i = need
			skipped = off
		}
		take := min(len(runes)-i, width-rendered)
		b.WriteString(segStyleFor(seg).Render(string(runes[i : i+take])))
		rendered += take
	}
	if pad := width - rendered; pad > 0 {
		b.WriteString(baseStyle.Render(strings.Repeat(" ", pad)))
	}
	return b.String()
}

// renderEditLine draws the cursor line, scrolling horizontally so the cursor
// stays visible when the line is longer than width: it slices a width-wide
// window that follows the cursor and draws the cursor at its in-window column.
// An active selection (sel, in full-line rune columns) is highlighted where it
// intersects the window.
func renderEditLine(line string, cx, width int, sel *selRange) string {
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
	curCol := at - off // guaranteed within [0, width-1]

	selS, selE := -1, -1
	if sel != nil {
		s, e := sel.start, sel.end
		if s > e {
			s, e = e, s
		}
		selS = input.Clamp(s, off, end) - off
		selE = input.Clamp(e, off, end) - off
	}

	var b strings.Builder
	for col := 0; col < width; col++ {
		idx := off + col
		ch := " "
		if idx < end {
			ch = string(runes[idx])
		}
		switch {
		case col == curCol:
			b.WriteString(cursorStyle.Render(ch))
		case selS >= 0 && col >= selS && col < selE:
			b.WriteString(selectionStyle.Render(ch))
		default:
			// Sublime-style current-line highlight.
			b.WriteString(cursorLineStyle.Render(ch))
		}
	}
	return b.String()
}

// renderSelectedLine draws a whole line covered by a line-wise selection: its
// runes on the selection background, padded to width. No cursor, no horizontal
// scroll (line-wise selections start at column 0).
func renderSelectedLine(line string, width int) string {
	if width < 1 {
		width = 1
	}
	runes := []rune(line)
	var b strings.Builder
	for col := 0; col < width; col++ {
		ch := " "
		if col < len(runes) {
			ch = string(runes[col])
		}
		b.WriteString(selectionStyle.Render(ch))
	}
	return b.String()
}

func (m Model) renderSearch(width, height int) string {
	matches := view.FindSearchMatches(m.searchContent, m.searchInput)
	byLine := view.BuildMatchesByLine(matches)
	lines := strings.Split(m.searchContent, "\n")

	maxScrollY := max(0, len(lines)-height)
	start := 0
	off := 0
	if len(matches) > 0 && m.searchFocused < len(matches) {
		fm := matches[m.searchFocused]
		start = input.Clamp(fm.LineIndex-2, 0, maxScrollY)
		// Scroll horizontally so a focused match past the width is visible.
		fline := lines[fm.LineIndex]
		fcol := utf8.RuneCountInString(fline[:clampByte(fm.Start, len(fline))])
		if fcol >= width {
			off = max(0, fcol-width/2)
		}
	}

	rows := make([]string, 0, height)
	for i := start; i < start+height; i++ {
		if i >= len(lines) {
			rows = append(rows, "")
			continue
		}
		var segs []view.HighlightSegment
		if i < len(m.searchHl) {
			segs = m.searchHl[i]
		}
		rows = append(rows, renderSearchLine(lines[i], segs, byLine[i], m.searchFocused, off, width))
	}
	return strings.Join(rows, "\n")
}

// renderSearchLine draws one content line for the search view, slicing a
// width-wide window starting at rune column off. Matches (byte offsets,
// converted to rune columns here) render with their background styles and win
// over the line's syntax segments, which color everything else.
func renderSearchLine(line string, segs []view.HighlightSegment, matches []view.LineMatch, focused, off, width int) string {
	if width < 1 {
		width = 1
	}
	runes := []rune(line)
	n := len(runes)
	end := min(off+width, n)

	type mrange struct {
		start, end int
		focused    bool
	}
	mr := make([]mrange, 0, len(matches))
	for _, lm := range matches {
		s := utf8.RuneCountInString(line[:clampByte(lm.Start, len(line))])
		e := utf8.RuneCountInString(line[:clampByte(lm.End, len(line))])
		mr = append(mr, mrange{s, e, lm.MatchIndex == focused})
	}

	// Flatten segments into a per-rune segment index so syntax colors survive
	// the rune-window slicing.
	var segIdx []int
	if segs != nil {
		segIdx = make([]int, 0, n)
		for si, seg := range segs {
			for range []rune(seg.Text) {
				segIdx = append(segIdx, si)
			}
		}
	}

	// Style key per visible column: -3 focused match, -2 match, -1 plain,
	// >=0 index into segs. Matches win over syntax.
	keyAt := func(idx int) int {
		if idx < end {
			for _, r := range mr {
				if idx >= r.start && idx < r.end {
					if r.focused {
						return -3
					}
					return -2
				}
			}
			if idx < len(segIdx) {
				return segIdx[idx]
			}
		}
		return -1
	}
	styleFor := func(key int) lipgloss.Style {
		switch {
		case key == -3:
			return searchFocusedStyle
		case key == -2:
			return searchMatchStyle
		case key >= 0:
			return segStyleFor(segs[key])
		default:
			return baseStyle
		}
	}

	// Batch consecutive same-style columns into runs — with a background on
	// every cell, per-char Render would be ~width×height calls per frame.
	var b strings.Builder
	var run []rune
	runKey := 0
	flush := func() {
		if len(run) > 0 {
			b.WriteString(styleFor(runKey).Render(string(run)))
			run = run[:0]
		}
	}
	for col := 0; col < width; col++ {
		idx := off + col
		ch := ' '
		if idx < end {
			ch = runes[idx]
		}
		key := keyAt(idx)
		if len(run) > 0 && key != runKey {
			flush()
		}
		runKey = key
		run = append(run, ch)
	}
	flush()
	return b.String()
}

// clampByte clamps a byte offset into [0, n].
func clampByte(i, n int) int {
	if i < 0 {
		return 0
	}
	if i > n {
		return n
	}
	return i
}

func renderInputLine(text string, cursor int) string {
	return renderInputLineStyled(text, cursor, statusTextStyle)
}

// renderInputLineStyled draws an editable input line (before/cursor/after) using
// textStyle for the non-cursor text, so bars with a non-chrome background (e.g.
// the @exec bar) stay a single color.
func renderInputLineStyled(text string, cursor int, textStyle lipgloss.Style) string {
	runes := []rune(text)
	at := input.Clamp(cursor, 0, len(runes))
	before := string(runes[:at])
	cursorChar := " "
	after := ""
	if at < len(runes) {
		cursorChar = string(runes[at])
		after = string(runes[at+1:])
	}
	return textStyle.Render(before) + cursorStyle.Render(cursorChar) + textStyle.Render(after)
}

func padTo(s string, width int) string {
	if n := width - len([]rune(s)); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func truncateRunes(s string, width int) string {
	// Byte length ≤ width implies rune count ≤ width — skip the []rune alloc
	// for the common short line.
	if len(s) <= width {
		return s
	}
	runes := []rune(s)
	if len(runes) > width {
		return string(runes[:width])
	}
	return s
}

func pad(s string, width int) string {
	if n := width - len([]rune(s)); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// segStyles memoizes lipgloss styles per segment-style combination —
// renderSegments runs for every visible row on every frame, and the style set
// is tiny. The TUI render loop is single-goroutine, so a plain map is safe.
type segStyleKey struct {
	color     string
	bold      bool
	dim       bool
	underline bool
	italic    bool
	strike    bool
}

var segStyles = map[segStyleKey]lipgloss.Style{}

func segStyleFor(segment view.HighlightSegment) lipgloss.Style {
	key := segStyleKey{
		color:     segment.Color,
		bold:      segment.Bold,
		dim:       segment.DimColor,
		underline: segment.Underline,
		italic:    segment.Italic,
		strike:    segment.Strike,
	}
	if style, ok := segStyles[key]; ok {
		return style
	}

	style := lipgloss.NewStyle().Background(colBg)
	if key.color != "" {
		style = style.Foreground(colorFor(key.color))
	} else {
		style = style.Foreground(colFg)
	}
	if key.bold {
		style = style.Bold(true)
	}
	if key.dim {
		style = style.Faint(true)
	}
	if key.underline {
		style = style.Underline(true)
	}
	if key.italic {
		style = style.Italic(true)
	}
	if key.strike {
		style = style.Strikethrough(true)
	}
	segStyles[key] = style
	return style
}

func colorFor(name string) lipgloss.Color {
	if strings.HasPrefix(name, "#") {
		return lipgloss.Color(name) // chroma style hex; termenv degrades on non-truecolor terminals
	}
	switch name {
	case "red":
		return lipgloss.Color("1")
	case "green":
		return lipgloss.Color("2")
	case "yellow":
		return lipgloss.Color("3")
	case "blue":
		return lipgloss.Color("4")
	case "magenta":
		return lipgloss.Color("5")
	case "cyan":
		return lipgloss.Color("6")
	case "white":
		return lipgloss.Color("7")
	case "gray":
		return lipgloss.Color("8")
	default:
		return lipgloss.Color("")
	}
}

// Gruvbox-dark palette (matches the default grammar style). Every emitted
// run carries its own background — wrapping already-styled strings would
// break on their inner ANSI resets.
var (
	colBg        = lipgloss.Color("#282828") // editor background
	colBgChrome  = lipgloss.Color("#1d2021") // header / status chrome (bg0_h)
	colFg        = lipgloss.Color("#ebdbb2") // cream foreground
	colLineHl    = lipgloss.Color("#3c3836") // cursor-line highlight (bg1)
	colSelection = lipgloss.Color("#504945") // bg2
	colBorder    = lipgloss.Color("#3c3836")
	colGutter    = lipgloss.Color("#7c6f64") // bg4
	colComment   = lipgloss.Color("#928374") // gray
	colAqua      = lipgloss.Color("#8ec07c")
	colGreen     = lipgloss.Color("#b8bb26")
	colOrange    = lipgloss.Color("#fe8019")
	colRed       = lipgloss.Color("#fb4934")
	colYellow    = lipgloss.Color("#fabd2f")
	colFindBg    = lipgloss.Color("#fabd2f") // find highlight (gold)
)

var (
	baseStyle   = lipgloss.NewStyle().Foreground(colFg).Background(colBg)
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(colAqua).Background(colBgChrome)
	paneStyle   = lipgloss.NewStyle().Background(colBg).BorderBackground(colBg).
			BorderForeground(colBorder).Border(lipgloss.RoundedBorder())
	cursorStyle = lipgloss.NewStyle().Foreground(colBg).Background(colFg)
	gutterStyle = lipgloss.NewStyle().Foreground(colGutter).Background(colBg) // line numbers

	// Status/hint chrome (darker bar, like Sublime's).
	promptStyle     = lipgloss.NewStyle().Foreground(colAqua).Bold(true).Background(colBgChrome)
	statusTextStyle = lipgloss.NewStyle().Foreground(colFg).Background(colBgChrome)
	hintStyle       = lipgloss.NewStyle().Foreground(colComment).Background(colBgChrome)

	// @exec bar — a distinct, lighter dark band (bg1) sitting directly under the
	// @edit status line so the two rows read as separate without a gap.
	execPromptStyle = lipgloss.NewStyle().Foreground(colAqua).Bold(true).Background(colLineHl)
	execTextStyle   = lipgloss.NewStyle().Foreground(colFg).Background(colLineHl)
	execHintStyle   = lipgloss.NewStyle().Foreground(colComment).Background(colLineHl)
	execSugSelStyle = lipgloss.NewStyle().Foreground(colYellow).Bold(true).Background(colLineHl) // Tab-acceptable suggestion

	gutterErrStyle  = lipgloss.NewStyle().Foreground(colRed).Background(colBg)
	gutterWarnStyle = lipgloss.NewStyle().Foreground(colYellow).Background(colBg)

	cursorLineStyle    = lipgloss.NewStyle().Foreground(colFg).Background(colLineHl)
	selectedEntryStyle = lipgloss.NewStyle().Foreground(colFg).Background(colSelection)
	openFileStyle      = lipgloss.NewStyle().Foreground(colGreen).Background(colBg)
	dirStyle           = lipgloss.NewStyle().Foreground(colAqua).Bold(true).Background(colBg)
	fileStyle          = lipgloss.NewStyle().Foreground(colFg).Background(colBg)
	ignoredFileStyle   = lipgloss.NewStyle().Foreground(colComment).Background(colBg) // .gitignore'd: gray
	// Uncommitted git changes: yellow, bubbling up to ancestor (even folded)
	// dirs. The dir variant keeps dirStyle's bold weight.
	uncommittedFileStyle = lipgloss.NewStyle().Foreground(colYellow).Background(colBg)
	uncommittedDirStyle  = lipgloss.NewStyle().Foreground(colYellow).Bold(true).Background(colBg)

	// Autocomplete dropdown rows.
	completionItemStyle = lipgloss.NewStyle().Foreground(colFg).Background(colBgChrome)
	completionSelStyle  = lipgloss.NewStyle().Foreground(colBg).Background(colAqua)

	// Tab strip (top of the main pane). Red name = unsaved.
	tabActiveStyle        = lipgloss.NewStyle().Foreground(colFg).Background(colSelection).Bold(true)
	tabInactiveStyle      = lipgloss.NewStyle().Foreground(colComment).Background(colBg)
	tabDirtyActiveStyle   = lipgloss.NewStyle().Foreground(colRed).Background(colSelection).Bold(true)
	tabDirtyInactiveStyle = lipgloss.NewStyle().Foreground(colRed).Background(colBg)
	tabFillStyle          = lipgloss.NewStyle().Background(colBg)
	tabDividerStyle       = lipgloss.NewStyle().Foreground(colBorder).Background(colBg)

	searchMatchStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#000000")).Background(colFindBg)
	searchFocusedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#000000")).Background(colOrange).Bold(true)
	selectionStyle     = lipgloss.NewStyle().Foreground(colFg).Background(colSelection)

	previewStyle        = lipgloss.NewStyle().Foreground(colAqua).Background(colBgChrome)
	suggestionFileStyle = lipgloss.NewStyle().Foreground(colYellow).Background(colBg)

	noticeStyle  = lipgloss.NewStyle().Foreground(colGreen).Background(colBgChrome)
	editingStyle = lipgloss.NewStyle().Foreground(colYellow).Bold(true).Background(colBgChrome) // unsaved edits
	savedStyle   = lipgloss.NewStyle().Foreground(colGreen).Bold(true).Background(colBgChrome)  // in sync with disk
	errStyle     = lipgloss.NewStyle().Foreground(colRed).Bold(true).Background(colBgChrome)
)

package app

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	lineNoteTextStyle         = lipgloss.NewStyle().Foreground(colFg).Background(colBgChrome)
	lineNoteBorderStyle       = lipgloss.NewStyle().Foreground(colComment).Background(colBgChrome)
	lineNoteTitleStyle        = lipgloss.NewStyle().Foreground(colAqua).Bold(true).Background(colBgChrome)
	lineNoteSessionTitleStyle = lipgloss.NewStyle().Foreground(colOrange).Bold(true).Background(colBgChrome)
	lineNoteHeadingStyle      = lipgloss.NewStyle().Foreground(colAqua).Bold(true).Background(colBgChrome)
	lineNoteBoldStyle         = lipgloss.NewStyle().Foreground(colFg).Bold(true).Background(colBgChrome)
	lineNoteCodeStyle         = lipgloss.NewStyle().Foreground(colYellow).Background(colBgChrome)
	lineNoteCodeBlockStyle    = lipgloss.NewStyle().Foreground(colFg).Background(colBg)
	lineNoteMarkerStyle       = lipgloss.NewStyle().Foreground(colOrange).Background(colBgChrome)
	lineNoteQuoteStyle        = lipgloss.NewStyle().Foreground(colComment).Italic(true).Background(colBgChrome)
)

// overlayLineNote splices the note box onto the rendered file rows.
func (m Model) overlayLineNote(rows []string, start, gutterWidth, contentWidth, height int) []string {
	box, ok := m.lineNoteGeometry(start, gutterWidth, contentWidth, height)
	if !ok {
		return rows
	}
	title := fmt.Sprintf(" note · L%d ", m.lineNote.line+1)
	closeHints := [2]string{"esc close", "esc"}
	if note := m.lineNote; note.session != "" {
		title = fmt.Sprintf(" %s · step %d/%d · L%d ", note.session, m.session.current+1, len(m.session.steps), note.line+1)
		closeHints = m.sessionBoxHints()
	}
	boxRows := renderLineNoteBox(box, title, m.lineNote.session != "", closeHints)
	for i, boxRow := range boxRows {
		rowIndex := box.top + i
		if rowIndex < 0 || rowIndex >= len(rows) {
			continue
		}
		rows[rowIndex] = spliceOverlay(rows[rowIndex], box.left, box.width, boxRow)
	}
	return rows
}

// renderLineNoteBox draws the bordered box, every row exactly box.width
// cells: the title on the top border, the scroll state and key hints on the
// bottom one. A session step's title stands out in orange.
func renderLineNoteBox(box lineNoteBox, title string, session bool, closeHints [2]string) []string {
	horizontal := box.width - 2
	title = ansi.Truncate(title, max(0, horizontal-1), "")
	titleStyle := lineNoteTitleStyle
	if session {
		titleStyle = lineNoteSessionTitleStyle
	}
	top := lineNoteBorderStyle.Render("╭─") + titleStyle.Render(title) +
		lineNoteBorderStyle.Render(strings.Repeat("─", max(0, horizontal-1-ansi.StringWidth(title)))+"╮")

	// The longest hint that fits the border wins.
	hint := ""
	for _, candidate := range lineNoteHints(box, closeHints) {
		if ansi.StringWidth(candidate) <= horizontal-2 {
			hint = candidate
			break
		}
	}
	bottom := lineNoteBorderStyle.Render("╰" + strings.Repeat("─", max(0, horizontal-1-ansi.StringWidth(hint))) +
		hint + "─╯")

	side := lineNoteBorderStyle.Render("│")
	padding := lineNoteTextStyle.Render(" ")
	out := make([]string, 0, box.height())
	out = append(out, top)
	for _, line := range box.lines[box.scroll : box.scroll+box.bodyRows] {
		out = append(out, side+padding+line+padding+side)
	}
	return append(out, bottom)
}

// sessionBoxHints are a session step's keys, long and short: Enter next (or
// finish on the last step of a complete session), Backspace back after the
// first step, Esc end.
func (m Model) sessionBoxHints() [2]string {
	next := "enter next"
	if m.session.current == len(m.session.steps)-1 && m.session.complete {
		next = "enter finish"
	}
	if m.session.current == 0 {
		return [2]string{next + " · esc end session", next + " · esc end"}
	}
	return [2]string{next + " · ⌫ back · esc end", "enter · ⌫ · esc"}
}

// lineNoteHints are the bottom-border hints, longest first: the rows
// scrolled past and still below, the scroll keys, and the keys that close
// the box (closeHints, long and short).
func lineNoteHints(box lineNoteBox, closeHints [2]string) []string {
	if len(box.lines) <= box.bodyRows {
		return []string{" " + closeHints[0] + " ", " " + closeHints[1] + " "}
	}
	position := []string{}
	if above := box.scroll; above > 0 {
		position = append(position, fmt.Sprintf("↑ %d", above))
	}
	if below := len(box.lines) - box.scroll - box.bodyRows; below > 0 {
		position = append(position, fmt.Sprintf("↓ %d more", below))
	}
	where := strings.Join(position, " · ")
	return []string{
		" " + where + " · shift+↑↓/wheel · " + closeHints[0] + " ",
		" " + where + " · shift+↑↓ · " + closeHints[1] + " ",
		" " + where + " · " + closeHints[1] + " ",
		" " + where + " ",
	}
}

// noteSpan is a run of note text in one style.
type noteSpan struct {
	text  string
	style lipgloss.Style
}

var (
	noteHeadingPattern  = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	noteBulletPattern   = regexp.MustCompile(`^([-*+])\s+(.*)$`)
	noteNumberedPattern = regexp.MustCompile(`^(\d{1,9}[.)])\s+(.*)$`)
	noteQuotePattern    = regexp.MustCompile(`^>\s?(.*)$`)
	noteInlinePattern   = regexp.MustCompile("`[^`]+`|\\*\\*[^*]+\\*\\*")
)

// layoutLineNote wraps a note to innerWidth cells with light markdown
// styling: headings, bullets and numbered items with a hanging indent,
// quotes, fenced code blocks (hard-wrapped, never word-wrapped), inline
// `code` and **bold**. Indentation is kept. Every returned line is exactly
// innerWidth cells wide.
func layoutLineNote(text string, innerWidth, tabWidth int) []string {
	innerWidth = max(1, innerWidth)
	var out []string
	inFence := false
	sourceLines := strings.Split(text, "\n")
	for lineIndex := 0; lineIndex < len(sourceLines); lineIndex++ {
		sourceLine := expandTabs(sourceLines[lineIndex], tabWidth)
		trimmed := strings.TrimSpace(sourceLine)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			for _, piece := range hardWrapCells(sourceLine, innerWidth) {
				out = append(out, padStyled(lineNoteCodeBlockStyle.Render(piece), ansi.StringWidth(piece), innerWidth, lineNoteCodeBlockStyle))
			}
			continue
		}
		if trimmed == "" {
			out = append(out, lineNoteTextStyle.Render(strings.Repeat(" ", innerWidth)))
			continue
		}

		indent := len(sourceLine) - len(strings.TrimLeft(sourceLine, " "))
		indent = min(indent, innerWidth/2)
		if isNoteTableStart(sourceLines, lineIndex) {
			end := lineIndex + 2
			for end < len(sourceLines) && strings.Contains(sourceLines[end], "|") && strings.TrimSpace(sourceLines[end]) != "" {
				end++
			}
			rows := [][]string{splitNoteTableRow(trimmed)}
			for _, rowLine := range sourceLines[lineIndex+2 : end] {
				rows = append(rows, splitNoteTableRow(strings.TrimSpace(expandTabs(rowLine, tabWidth))))
			}
			out = append(out, layoutNoteTable(rows, indent, innerWidth)...)
			lineIndex = end - 1
			continue
		}
		marker, markerStyle, body, bodyStyle := "", lineNoteTextStyle, trimmed, lineNoteTextStyle
		if match := noteHeadingPattern.FindStringSubmatch(trimmed); match != nil {
			body, bodyStyle = match[2], lineNoteHeadingStyle
		} else if match := noteBulletPattern.FindStringSubmatch(trimmed); match != nil {
			marker, markerStyle, body = "• ", lineNoteMarkerStyle, match[2]
		} else if match := noteNumberedPattern.FindStringSubmatch(trimmed); match != nil {
			marker, markerStyle, body = match[1]+" ", lineNoteMarkerStyle, match[2]
		} else if match := noteQuotePattern.FindStringSubmatch(trimmed); match != nil {
			marker, markerStyle, body, bodyStyle = "▎ ", lineNoteBorderStyle, match[1], lineNoteQuoteStyle
		}
		hanging := min(indent+ansi.StringWidth(marker), innerWidth-1)
		firstPrefix := lineNoteTextStyle.Render(strings.Repeat(" ", indent)) + markerStyle.Render(marker)
		if marker == "" {
			firstPrefix = lineNoteTextStyle.Render(strings.Repeat(" ", indent))
		}
		continuationPrefix := lineNoteTextStyle.Render(strings.Repeat(" ", hanging))
		for index, wrapped := range wrapNoteSpans(parseNoteInline(body, bodyStyle), innerWidth-hanging) {
			prefix := continuationPrefix
			if index == 0 {
				prefix = firstPrefix
			}
			out = append(out, padStyled(prefix+wrapped.text, hanging+wrapped.width, innerWidth, lineNoteTextStyle))
		}
	}
	if len(out) == 0 {
		out = append(out, lineNoteTextStyle.Render(strings.Repeat(" ", innerWidth)))
	}
	return out
}

// noteTableDelimiterPattern is a table's second line: |---|:--:| cells.
var noteTableDelimiterPattern = regexp.MustCompile(`^\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?$`)

// isNoteTableStart reports whether lines[index] is a table's header row: it
// has a column bar and the next line is a delimiter row.
func isNoteTableStart(lines []string, index int) bool {
	if index+1 >= len(lines) || !strings.Contains(lines[index], "|") {
		return false
	}
	delimiter := strings.TrimSpace(lines[index+1])
	return strings.Contains(delimiter, "-") && noteTableDelimiterPattern.MatchString(delimiter)
}

// splitNoteTableRow cuts a table row into trimmed cells; the outer bars are
// optional and an escaped \| stays in the cell.
func splitNoteTableRow(line string) []string {
	line = strings.TrimPrefix(line, "|")
	if strings.HasSuffix(line, "|") && !strings.HasSuffix(line, "\\|") {
		line = line[:len(line)-1]
	}
	var cells []string
	var cell strings.Builder
	for index := 0; index < len(line); index++ {
		switch {
		case line[index] == '\\' && index+1 < len(line) && line[index+1] == '|':
			cell.WriteByte('|')
			index++
		case line[index] == '|':
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
		default:
			cell.WriteByte(line[index])
		}
	}
	return append(cells, strings.TrimSpace(cell.String()))
}

// layoutNoteTable draws a markdown table: a bold header, a rule, and the
// rows, columns split by │. Columns take their content's width; when the
// table is wider than the box, the widest columns give way first and their
// cells word-wrap onto more lines.
func layoutNoteTable(rows [][]string, indent, innerWidth int) []string {
	columns := 0
	for _, row := range rows {
		columns = max(columns, len(row))
	}
	cellSpans := make([][][]noteSpan, len(rows))
	natural := make([]int, columns)
	for rowIndex, row := range rows {
		style := lineNoteTextStyle
		if rowIndex == 0 {
			style = lineNoteHeadingStyle
		}
		cellSpans[rowIndex] = make([][]noteSpan, columns)
		for column := 0; column < columns; column++ {
			text := ""
			if column < len(row) {
				text = row[column]
			}
			spans := parseNoteInline(text, style)
			cellSpans[rowIndex][column] = spans
			width := 0
			for _, span := range spans {
				width += ansi.StringWidth(span.text)
			}
			natural[column] = max(natural[column], width, 1)
		}
	}

	const separatorWidth = 3 // " │ "
	widths := fitNoteTableColumns(natural, innerWidth-indent-separatorWidth*(columns-1))
	prefix := lineNoteTextStyle.Render(strings.Repeat(" ", indent))
	separator := lineNoteBorderStyle.Render(" │ ")
	tableWidth := indent + separatorWidth*(columns-1)
	for _, width := range widths {
		tableWidth += width
	}

	var out []string
	for rowIndex := range rows {
		wrapped := make([][]wrappedNoteLine, columns)
		height := 1
		for column := 0; column < columns; column++ {
			wrapped[column] = wrapNoteSpans(cellSpans[rowIndex][column], widths[column])
			height = max(height, len(wrapped[column]))
		}
		for lineIndex := 0; lineIndex < height; lineIndex++ {
			var line strings.Builder
			line.WriteString(prefix)
			for column := 0; column < columns; column++ {
				if column > 0 {
					line.WriteString(separator)
				}
				piece := wrappedNoteLine{}
				if lineIndex < len(wrapped[column]) {
					piece = wrapped[column][lineIndex]
				}
				line.WriteString(padStyled(piece.text, piece.width, widths[column], lineNoteTextStyle))
			}
			out = append(out, padStyled(line.String(), tableWidth, innerWidth, lineNoteTextStyle))
		}
		if rowIndex == 0 {
			rule := make([]string, columns)
			for column, width := range widths {
				rule[column] = strings.Repeat("─", width)
			}
			ruleLine := prefix + lineNoteBorderStyle.Render(strings.Join(rule, "─┼─"))
			out = append(out, padStyled(ruleLine, tableWidth, innerWidth, lineNoteTextStyle))
		}
	}
	return out
}

// fitNoteTableColumns shrinks column widths to fit available cells, taking
// from the widest column one cell at a time so narrow columns keep their
// text; no column drops below 3 cells (or its own width, when narrower).
func fitNoteTableColumns(natural []int, available int) []int {
	widths := slices.Clone(natural)
	total := 0
	for _, width := range widths {
		total += width
	}
	for total > available {
		widest := 0
		for column, width := range widths {
			if width > widths[widest] {
				widest = column
			}
		}
		if widths[widest] <= 3 {
			break
		}
		widths[widest]--
		total--
	}
	return widths
}

// parseNoteInline splits text into spans: `code` and **bold** runs styled,
// the rest in base.
func parseNoteInline(text string, base lipgloss.Style) []noteSpan {
	var spans []noteSpan
	last := 0
	for _, match := range noteInlinePattern.FindAllStringIndex(text, -1) {
		if match[0] > last {
			spans = append(spans, noteSpan{text: text[last:match[0]], style: base})
		}
		token := text[match[0]:match[1]]
		if strings.HasPrefix(token, "`") {
			spans = append(spans, noteSpan{text: token[1 : len(token)-1], style: lineNoteCodeStyle})
		} else {
			spans = append(spans, noteSpan{text: token[2 : len(token)-2], style: base.Bold(true)})
		}
		last = match[1]
	}
	if last < len(text) {
		spans = append(spans, noteSpan{text: text[last:], style: base})
	}
	return spans
}

// wrappedNoteLine is one wrapped line: styled text and its width in cells.
type wrappedNoteLine struct {
	text  string
	width int
}

// wrapNoteSpans word-wraps styled spans to width cells. A word may cross
// span boundaries ("**bold**,"); a word wider than the line hard-breaks.
func wrapNoteSpans(spans []noteSpan, width int) []wrappedNoteLine {
	width = max(1, width)
	type wordPiece struct {
		text  string
		style lipgloss.Style
	}
	// Split into words of pieces; a space in any span ends the current word.
	var words [][]wordPiece
	var current []wordPiece
	for _, span := range spans {
		for index, field := range strings.Split(span.text, " ") {
			if index > 0 && len(current) > 0 {
				words = append(words, current)
				current = nil
			}
			if field != "" {
				current = append(current, wordPiece{text: field, style: span.style})
			}
		}
	}
	if len(current) > 0 {
		words = append(words, current)
	}

	var lines []wrappedNoteLine
	var line strings.Builder
	lineWidth := 0
	flush := func() {
		lines = append(lines, wrappedNoteLine{text: line.String(), width: lineWidth})
		line.Reset()
		lineWidth = 0
	}
	for _, word := range words {
		wordWidth := 0
		for _, piece := range word {
			wordWidth += ansi.StringWidth(piece.text)
		}
		if lineWidth > 0 && lineWidth+1+wordWidth > width {
			flush()
		}
		if lineWidth > 0 {
			line.WriteString(lineNoteTextStyle.Render(" "))
			lineWidth++
		}
		for _, piece := range word {
			for remaining := piece.text; remaining != ""; {
				if lineWidth >= width {
					flush()
				}
				var head string
				var headWidth int
				head, remaining, headWidth = takeCells(remaining, width-lineWidth)
				line.WriteString(piece.style.Render(head))
				lineWidth += headWidth
			}
		}
	}
	if lineWidth > 0 || len(lines) == 0 {
		flush()
	}
	return lines
}

// hardWrapCells cuts text into chunks of at most width cells.
func hardWrapCells(text string, width int) []string {
	var chunks []string
	for {
		var head string
		head, text, _ = takeCells(text, width)
		chunks = append(chunks, head)
		if text == "" {
			return chunks
		}
	}
}

// takeCells splits off the longest prefix of text that fits in limit cells
// (at least one rune, so a wide rune in a 1-cell limit still advances).
func takeCells(text string, limit int) (head, tail string, headWidth int) {
	for index, r := range text {
		runeWidth := ansi.StringWidth(string(r))
		if headWidth > 0 && headWidth+runeWidth > limit {
			return text[:index], text[index:], headWidth
		}
		headWidth += runeWidth
	}
	return text, "", headWidth
}

// padStyled fills a styled line of width cells out to total with fill.
func padStyled(line string, width, total int, fill lipgloss.Style) string {
	if width >= total {
		return ansi.Truncate(line, total, "")
	}
	return line + fill.Render(strings.Repeat(" ", total-width))
}

// expandTabs turns tabs into tabWidth spaces so widths are countable.
func expandTabs(text string, tabWidth int) string {
	if strings.IndexByte(text, '\t') < 0 {
		return text
	}
	return strings.ReplaceAll(text, "\t", strings.Repeat(" ", tabWidth))
}

package app

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The inspect dashboard's panel kit: a heading, a rounded two-column table,
// and a legend. Op-commands and the info panels (ntee-db, lsp, system) all
// draw through it so they read as one design.

var colBlue = lipgloss.Color("#83a598")

var (
	panelBorderStyle   = lipgloss.NewStyle().Foreground(colGutter).Background(colBg)
	panelRuleStyle     = lipgloss.NewStyle().Foreground(colSelection).Background(colBg)
	panelTitleStyle    = lipgloss.NewStyle().Bold(true).Foreground(colAqua).Background(colBg)
	panelSubtitleStyle = lipgloss.NewStyle().Foreground(colComment).Background(colBg)
	panelHeaderStyle   = lipgloss.NewStyle().Bold(true).Foreground(colComment).Background(colBg)
	panelLabelStyle    = lipgloss.NewStyle().Foreground(colFg).Background(colBg)
	panelCommandStyle  = lipgloss.NewStyle().Bold(true).Foreground(colAqua).Background(colBg)
)

// panelTable holds a table's column widths. A row is
// "│ [m ]key… │ value… │": with marker, the key cell gains a 2-column gutter
// for a row marker (▸/✎).
type panelTable struct {
	keyWidth, valueWidth int
	marker               bool
}

// newPanelTable fits a table into width, the key column sized to keyWidth
// (callers cap it) and the value column taking the rest.
func newPanelTable(width, keyWidth int, marker bool) panelTable {
	table := panelTable{keyWidth: keyWidth, marker: marker}
	table.valueWidth = max(4, width-keyWidth-table.overhead())
	return table
}

// overhead is the border, padding, and marker columns around the two cells.
func (table panelTable) overhead() int {
	if table.marker {
		return 9
	}
	return 7
}

func (table panelTable) keySpan() int {
	if table.marker {
		return table.keyWidth + 4
	}
	return table.keyWidth + 2
}

func (table panelTable) contentWidth() int {
	return table.keyWidth + table.valueWidth + table.overhead() - 4
}

func (table panelTable) border(left, middle, right string) string {
	line := left + strings.Repeat("─", table.keySpan())
	if middle != "" {
		line += middle + strings.Repeat("─", table.valueWidth+2)
	} else {
		line += strings.Repeat("─", table.valueWidth+3)
	}
	return panelBorderStyle.Render(line + right)
}

// rowRule separates two rows: a quieter line than the table's own borders,
// joined to them at the edges.
func (table panelTable) rowRule() string {
	return panelBorderStyle.Render("├") +
		panelRuleStyle.Render(strings.Repeat("─", table.keySpan())+"┼"+strings.Repeat("─", table.valueWidth+2)) +
		panelBorderStyle.Render("┤")
}

// row joins an (optional) marker, key cell, and value cell, each already
// exactly sized; gap paints the spacing in the row's background.
func (table panelTable) row(marker, keyCell, valueCell string, gap, markerStyle lipgloss.Style) string {
	bar := panelBorderStyle.Render("│")
	left := bar + gap.Render(" ")
	if table.marker {
		left += markerStyle.Render(marker) + gap.Render(" ")
	}
	return left + keyCell + gap.Render(" ") + bar + gap.Render(" ") + valueCell + gap.Render(" ") + bar
}

// spanning is a full-width row ignoring the column split; content is already
// rendered and exactly contentWidth() wide.
func (table panelTable) spanning(content string) string {
	bar := panelBorderStyle.Render("│")
	return bar + baseStyle.Render(" ") + content + baseStyle.Render(" ") + bar
}

func (table panelTable) header(keyLabel, valueLabel string) string {
	return table.row(" ", panelHeaderStyle.Render(padTo(keyLabel, table.keyWidth)),
		panelHeaderStyle.Render(padTo(valueLabel, table.valueWidth)), baseStyle, baseStyle)
}

// panelCell fits already-styled text to exactly width cells: cut with "…"
// when too wide, padded in the panel background otherwise.
func panelCell(styled string, width int) string {
	return panelCellFill(styled, width, baseStyle)
}

// panelCellFill is panelCell padding with fill — a highlighted line pads in
// its own background so the highlight spans the whole cell.
func panelCellFill(styled string, width int, fill lipgloss.Style) string {
	if lipgloss.Width(styled) > width {
		styled = ansi.Truncate(styled, width, fill.Render("…"))
	}
	if pad := width - lipgloss.Width(styled); pad > 0 {
		styled += fill.Render(strings.Repeat(" ", pad))
	}
	return styled
}

// panelRow is one info-table row: a label and pre-styled value pieces. The
// pieces flow onto continuation lines (empty key cell) when the value column
// is too narrow for them side by side; stacked puts each on its own line.
// selectedPiece (1-based, 0 = none) pads that line in the selection color.
type panelRow struct {
	label         string
	pieces        []string
	stacked       bool
	selectedPiece int
}

// lines lays the row's pieces out for a value column of width.
func (row panelRow) lines(width int) []string {
	if row.stacked {
		return row.pieces
	}
	return wrapStyled(row.pieces, baseStyle.Render("  "), width)
}

type panelLegendEntry struct {
	token  string
	style  lipgloss.Style
	detail string
}

// infoPanel describes a read-only inspect panel. Exactly one of rows or
// message fills the table body.
type infoPanel struct {
	title, subtitle        string
	titleHint              string // focus hint after the title ("press → to …")
	keyHeader, valueHeader string
	rows                   []panelRow
	message                []string // plain text lines spanning the table (loading, unavailable)
	after                  []string // pre-styled lines under the table
	legend                 []panelLegendEntry
}

// table sizes the panel's table: the key column fits the longest label,
// capped at a third of the width.
func (panel infoPanel) table(width int) panelTable {
	keyWidth := len(panel.keyHeader)
	for _, row := range panel.rows {
		keyWidth = max(keyWidth, len([]rune(row.label)))
	}
	return newPanelTable(width, min(keyWidth, max(6, width/3)), false)
}

// render draws the panel: heading, table, notes, and the Commands legend.
// When it would be taller than height it sheds the least useful parts first —
// the legend (the status bar shows the keys too), then spacing, then the
// subtitle — and clips what still doesn't fit, so the frame never grows past
// the terminal.
func (panel infoPanel) render(width, height int) string {
	width = panelInnerWidth(width)
	table := panel.table(width)

	heading := panelHeading(panel.title, panel.subtitle, width)
	if panel.titleHint != "" {
		heading[0] += panelSubtitleStyle.Render("  ·  ") + opFocusHintStyle.Render(panel.titleHint)
	}
	body := []string{table.border("╭", "┬", "╮"), table.header(panel.keyHeader, panel.valueHeader)}
	if len(panel.rows) == 0 {
		body = append(body, table.border("├", "┴", "┤"))
		for _, text := range panel.message {
			for _, line := range wrapWords(text, table.contentWidth()) {
				body = append(body, table.spanning(panelSubtitleStyle.Render(padTo(line, table.contentWidth()))))
			}
		}
		body = append(body, table.border("╰", "", "╯"))
	} else {
		body = append(body, table.border("├", "┼", "┤"))
		for index, row := range panel.rows {
			if index > 0 {
				body = append(body, table.rowRule())
			}
			for valueIndex, value := range row.lines(table.valueWidth) {
				label := ""
				if valueIndex == 0 {
					label = row.label
				}
				fill := baseStyle
				if valueIndex+1 == row.selectedPiece {
					fill = selectedEntryStyle
				}
				body = append(body, table.row("", panelCell(panelLabelStyle.Render(label), table.keyWidth),
					panelCellFill(value, table.valueWidth, fill), baseStyle, baseStyle))
			}
		}
		body = append(body, table.border("╰", "┴", "╯"))
	}
	var legend []string
	if len(panel.legend) > 0 {
		legend = panelLegend("Commands", "type them in the @inspection > bar", panel.legend, width)
	}

	fits := func(extra []string) bool {
		spacing := 1 // under the heading
		if len(panel.after) > 0 {
			spacing++
		}
		if len(extra) > 0 {
			spacing++
		}
		return len(heading)+len(body)+len(panel.after)+len(extra)+spacing <= height
	}
	lines := heading
	switch {
	case fits(legend):
		lines = append(lines, "")
		lines = append(lines, body...)
		lines = appendSection(lines, panel.after)
		lines = appendSection(lines, legend)
	case fits(nil):
		lines = append(lines, "")
		lines = append(lines, body...)
		lines = appendSection(lines, panel.after)
	default:
		if len(heading)+len(body)+len(panel.after) > height {
			lines = heading[:1] // the subtitle goes before the table does
		}
		lines = append(lines, body...)
		lines = append(lines, panel.after...)
	}
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	}
	return panelIndent(lines)
}

// appendSection adds a blank-line-separated block when it has any lines.
func appendSection(lines, section []string) []string {
	if len(section) == 0 {
		return lines
	}
	lines = append(lines, "")
	return append(lines, section...)
}

func panelHeading(title, subtitle string, width int) []string {
	lines := []string{panelTitleStyle.Render(truncateRunes(title, width))}
	for _, line := range wrapWords(subtitle, width) {
		lines = append(lines, panelSubtitleStyle.Render(line))
	}
	return lines
}

// panelLegend lists tokens (commands, placeholders) with a description; the
// token column fits the longest token.
func panelLegend(heading, note string, entries []panelLegendEntry, width int) []string {
	tokenWidth := 0
	for _, entry := range entries {
		tokenWidth = max(tokenWidth, len([]rune(entry.token)))
	}
	tokenWidth += 3
	title := panelHeaderStyle.Render(heading)
	if note != "" {
		title += panelSubtitleStyle.Render(truncateRunes("  ·  "+note, max(0, width-len(heading))))
	}
	lines := []string{title}
	for _, entry := range entries {
		// Descriptions wrap under themselves, keeping the token column clear.
		for index, detail := range wrapWords(entry.detail, max(8, width-tokenWidth-2)) {
			token := strings.Repeat(" ", tokenWidth)
			if index == 0 {
				token = padTo(entry.token, tokenWidth)
			}
			lines = append(lines, baseStyle.Render("  ")+entry.style.Render(token)+panelSubtitleStyle.Render(detail))
		}
	}
	return lines
}

// panelInnerWidth is what a panel draws into once panelIndent's margin is
// taken out of the width it was given.
func panelInnerWidth(width int) int { return max(1, width-1) }

// panelIndent gives the panel a one-column left margin inside the pane.
func panelIndent(lines []string) string {
	margin := baseStyle.Render(" ")
	for index := range lines {
		lines[index] = margin + lines[index]
	}
	return strings.Join(lines, "\n")
}

// wrapWords word-wraps plain text to width, hard-breaking words that alone
// exceed it.
func wrapWords(text string, width int) []string {
	width = max(1, width)
	var lines []string
	current := ""
	for _, word := range strings.Fields(text) {
		for len([]rune(word)) > width {
			if current != "" {
				lines = append(lines, current)
				current = ""
			}
			runes := []rune(word)
			lines = append(lines, string(runes[:width]))
			word = string(runes[width:])
		}
		switch {
		case current == "":
			current = word
		case len([]rune(current))+1+len([]rune(word)) <= width:
			current += " " + word
		default:
			lines = append(lines, current)
			current = word
		}
	}
	if current != "" || len(lines) == 0 {
		lines = append(lines, current)
	}
	return lines
}

// wrapStyled packs pre-styled items into lines of at most width cells,
// separated by sep.
func wrapStyled(items []string, sep string, width int) []string {
	var lines []string
	current := ""
	for _, item := range items {
		switch {
		case current == "":
			current = item
		case lipgloss.Width(current)+lipgloss.Width(sep)+lipgloss.Width(item) <= width:
			current += sep + item
		default:
			lines = append(lines, current)
			current = item
		}
	}
	if current != "" || len(lines) == 0 {
		lines = append(lines, current)
	}
	return lines
}

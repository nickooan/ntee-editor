package app

import (
	"math"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// floatPanel is a small bordered box floated over a pane's right side: a
// title on the top border and key → label rows (a row with no key is plain
// text). Any mode can float one over its content with floatPanelPlacement
// and spliceFloatPanel.
type floatPanel struct {
	title      string
	titleStyle lipgloss.Style
	rows       []floatPanelRow
}

type floatPanelRow struct {
	key, label string
}

var (
	floatPanelBorderStyle = lipgloss.NewStyle().Foreground(colComment).Background(colBgChrome)
	floatPanelTitleStyle  = lipgloss.NewStyle().Foreground(colAqua).Bold(true).Background(colBgChrome)
	floatPanelKeyStyle    = lipgloss.NewStyle().Foreground(colYellow).Bold(true).Background(colBgChrome)
	floatPanelLabelStyle  = lipgloss.NewStyle().Foreground(colFg).Background(colBgChrome)
	floatPanelTextStyle   = lipgloss.NewStyle().Foreground(colComment).Background(colBgChrome)
)

const (
	floatPanelRightMargin = 2
	floatPanelEdgeMargin  = 1 // rows kept clear above (at the top) or below (at the bottom)
	// The room inside the border: cells each side, a blank row above and below.
	floatPanelPaddingX = 2
	floatPanelPaddingY = 1
	// floatPanelFlipAt is where in the pane the cursor sends the panel from
	// the bottom to the top, so it never sits on the code being read.
	floatPanelFlipAt = 0.65
)

// keyWidth is the widest key column, so the labels line up.
func (panel floatPanel) keyWidth() int {
	width := 0
	for _, row := range panel.rows {
		width = max(width, ansi.StringWidth(row.key))
	}
	return width
}

// width is the box's width in cells, borders included.
func (panel floatPanel) width() int {
	keys := panel.keyWidth()
	inner := ansi.StringWidth(panel.title) + 2
	for _, row := range panel.rows {
		rowWidth := ansi.StringWidth(row.label)
		if row.key != "" {
			rowWidth += keys + 2
		}
		inner = max(inner, rowWidth+2*floatPanelPaddingX)
	}
	return inner + 2
}

func (panel floatPanel) height() int { return len(panel.rows) + 2*floatPanelPaddingY + 2 }

// render draws the box, every row exactly panel.width() cells.
func (panel floatPanel) render() []string {
	width, keys := panel.width(), panel.keyWidth()
	horizontal := width - 2
	title := panel.titleStyle.Render(panel.title)
	lines := make([]string, 0, panel.height())
	lines = append(lines, floatPanelBorderStyle.Render("╭─")+title+
		floatPanelBorderStyle.Render(strings.Repeat("─", max(0, horizontal-1-ansi.StringWidth(panel.title)))+"╮"))
	side := floatPanelBorderStyle.Render("│")
	padding := floatPanelLabelStyle.Render(strings.Repeat(" ", floatPanelPaddingX))
	blank := side + floatPanelLabelStyle.Render(strings.Repeat(" ", horizontal)) + side
	for range floatPanelPaddingY {
		lines = append(lines, blank)
	}
	for _, row := range panel.rows {
		var body string
		used := 0
		if row.key != "" {
			body = floatPanelKeyStyle.Render(padTo(row.key, keys)) + floatPanelLabelStyle.Render("  "+row.label)
			used = keys + 2 + ansi.StringWidth(row.label)
		} else {
			body = floatPanelTextStyle.Render(row.label)
			used = ansi.StringWidth(row.label)
		}
		fill := floatPanelLabelStyle.Render(strings.Repeat(" ", max(0, horizontal-2*floatPanelPaddingX-used)))
		lines = append(lines, side+padding+body+fill+padding+side)
	}
	for range floatPanelPaddingY {
		lines = append(lines, blank)
	}
	lines = append(lines, floatPanelBorderStyle.Render("╰"+strings.Repeat("─", horizontal)+"╯"))
	return lines
}

// floatPanelPlacement places a panel of panelWidth × panelHeight over a
// pane: at its right edge with a margin, at the bottom — or at the top once
// cursorRow (a pane row) is at or below floatPanelFlipAt of the pane, so the
// panel never covers the line under review. ok=false when the pane is too
// small to float it and still read the code beside it.
func floatPanelPlacement(paneWidth, paneHeight, cursorRow, panelWidth, panelHeight int) (top, left int, ok bool) {
	if paneWidth < panelWidth+floatPanelRightMargin+12 || paneHeight < panelHeight+2*floatPanelEdgeMargin+2 {
		return 0, 0, false
	}
	left = paneWidth - panelWidth - floatPanelRightMargin
	if cursorRow >= int(math.Ceil(floatPanelFlipAt*float64(paneHeight))) {
		return floatPanelEdgeMargin, left, true
	}
	return paneHeight - panelHeight - floatPanelEdgeMargin, left, true
}

// spliceFloatPanel draws the panel's rows over rows (a pane's rendered
// lines) at top/left.
func spliceFloatPanel(rows []string, panel floatPanel, top, left int) []string {
	width := panel.width()
	for index, line := range panel.render() {
		if row := top + index; row >= 0 && row < len(rows) {
			rows[row] = spliceOverlay(rows[row], left, width, line)
		}
	}
	return rows
}

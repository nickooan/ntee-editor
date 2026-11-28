package app

import (
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
)

// framePieces are a frame's parts before composition: the header and status
// rows span the terminal; the two pane bodies sit inside paneStyle borders.
type framePieces struct {
	header, status          string
	sidebarBody, mainBody   string
	sidebarWidth, mainWidth int
	bodyHeight              int
}

// paneChrome is paneStyle's border and background, cut from lipgloss's own
// output so the hand-built panes match paneStyle.Render byte for byte.
type paneChrome struct {
	borderOpen string // the border's SGR (fg + bg)
	backOpen   string // the pane background's SGR
	reset      string
	edge       string // a styled side border "│"
}

var (
	paneChromeOnce   sync.Once
	paneChromeCached paneChrome
)

func currentPaneChrome() paneChrome {
	paneChromeOnce.Do(func() {
		rows := strings.Split(paneStyle.Width(3).Height(3).Render("x"), "\n")
		top, middle := rows[0], rows[1]
		chrome := paneChrome{
			borderOpen: top[:strings.Index(top, "╭")],
			reset:      top[strings.Index(top, "╮")+len("╮"):],
		}
		chrome.edge = chrome.borderOpen + "│" + chrome.reset
		inside := strings.TrimSuffix(strings.TrimPrefix(middle, chrome.edge), chrome.edge)
		chrome.backOpen = inside[:strings.Index(inside, "x")]
		paneChromeCached = chrome
	})
	return paneChromeCached
}

// framePane is one bordered pane: its body lines already cut to fit, with
// their widths, or — for a size the fast path does not cover — the rows
// lipgloss rendered.
type framePane struct {
	lines    []string
	widths   []int
	width    int
	height   int
	rendered []string
}

func newFramePane(body string, width, height int) framePane {
	lines, widths := fitPaneLines(body, width-4)
	pane := framePane{lines: lines, widths: widths, width: width, height: height}
	if width < 3 || height < 2 || maxOf(widths) > width-2 {
		pane.rendered = strings.Split(paneStyle.Width(width).Height(height).Render(strings.Join(lines, "\n")), "\n")
	}
	return pane
}

func (pane framePane) rowCount() int {
	if pane.rendered != nil {
		return len(pane.rendered)
	}
	return max(pane.height-2, len(pane.lines)) + 2
}

// writeRow writes row of the pane as paneStyle.Width(width).Height(height)
// would draw it: a content line is the styled line plus a styled fill, a
// row past the body is one styled fill.
func (pane framePane) writeRow(builder *strings.Builder, chrome paneChrome, row int) {
	if pane.rendered != nil {
		builder.WriteString(pane.rendered[row])
		return
	}
	inner := pane.width - 2
	last := pane.rowCount() - 1
	switch {
	case row == 0 || row == last:
		left, right := "╭", "╮"
		if row == last {
			left, right = "╰", "╯"
		}
		builder.WriteString(chrome.borderOpen)
		builder.WriteString(left)
		for range inner {
			builder.WriteString("─")
		}
		builder.WriteString(right)
		builder.WriteString(chrome.reset)
	case row-1 < len(pane.lines):
		builder.WriteString(chrome.edge)
		builder.WriteString(chrome.backOpen)
		builder.WriteString(pane.lines[row-1])
		builder.WriteString(chrome.reset)
		if fill := inner - pane.widths[row-1]; fill > 0 {
			builder.WriteString(chrome.backOpen)
			writeSpaces(builder, fill)
			builder.WriteString(chrome.reset)
		}
		builder.WriteString(chrome.edge)
	default:
		builder.WriteString(chrome.edge)
		builder.WriteString(chrome.backOpen)
		writeSpaces(builder, inner)
		builder.WriteString(chrome.reset)
		builder.WriteString(chrome.edge)
	}
}

// composeFrame lays the header, the two panes side by side and the status
// rows out as lipgloss.JoinVertical(Left, header, JoinHorizontal(Top,
// sidebar, main), status) of the paneStyle-rendered panes would, without
// re-measuring every line of the frame several times over.
func composeFrame(pieces framePieces) string {
	chrome := currentPaneChrome()
	sidebar := newFramePane(pieces.sidebarBody, pieces.sidebarWidth, pieces.bodyHeight)
	main := newFramePane(pieces.mainBody, pieces.mainWidth, pieces.bodyHeight)
	headerRows, headerWidths := joinedRows(pieces.header)
	statusRows, statusWidths := joinedRows(pieces.status)

	bodyWidth := sidebar.paneWidth() + main.paneWidth()
	frameWidth := max(bodyWidth, maxOf(headerWidths), maxOf(statusWidths))
	bodyRows := max(sidebar.rowCount(), main.rowCount())

	var builder strings.Builder
	builder.Grow((len(headerRows) + bodyRows + len(statusRows)) * (frameWidth + 64))
	for index, row := range headerRows {
		builder.WriteString(row)
		writeSpaces(&builder, frameWidth-headerWidths[index])
		builder.WriteByte('\n')
	}
	for row := range bodyRows {
		for _, pane := range [2]framePane{sidebar, main} {
			if row < pane.rowCount() {
				pane.writeRow(&builder, chrome, row)
			} else {
				writeSpaces(&builder, pane.paneWidth())
			}
		}
		writeSpaces(&builder, frameWidth-bodyWidth)
		builder.WriteByte('\n')
	}
	for index, row := range statusRows {
		if index > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(row)
		writeSpaces(&builder, frameWidth-statusWidths[index])
	}
	return builder.String()
}

// paneWidth is the pane block's width, which JoinHorizontal pads a missing
// row to.
func (pane framePane) paneWidth() int {
	if pane.rendered == nil {
		return pane.width
	}
	widest := 0
	for _, row := range pane.rendered {
		widest = max(widest, ansi.StringWidth(row))
	}
	return widest
}

// joinedRows splits a header or status block the way lipgloss's joins read
// it: tabs as 4 spaces, CRLF as LF.
func joinedRows(block string) ([]string, []int) {
	if strings.ContainsAny(block, "\t\r") {
		block = strings.ReplaceAll(strings.ReplaceAll(block, "\t", "    "), "\r\n", "\n")
	}
	rows := strings.Split(block, "\n")
	widths := make([]int, len(rows))
	for index, row := range rows {
		widths[index] = ansi.StringWidth(row)
	}
	return rows, widths
}

func maxOf(values []int) int {
	widest := 0
	for _, value := range values {
		widest = max(widest, value)
	}
	return widest
}

const spaceRun = "                                                                                                                                "

func writeSpaces(builder *strings.Builder, count int) {
	for count > 0 {
		chunk := min(count, len(spaceRun))
		builder.WriteString(spaceRun[:chunk])
		count -= chunk
	}
}

// fitPaneLines splits a pane body into lines cut to width terminal cells,
// with each line's width. Row renderers window text by runes, but a tab
// (drawn as 4 spaces) or a wide rune takes more than one cell; an over-wide
// line would wrap inside the pane and spill its tail into the next row's
// gutter. Tabs become the 4 spaces paneStyle would draw before measuring.
func fitPaneLines(body string, width int) ([]string, []int) {
	if strings.ContainsAny(body, "\t\r") {
		body = strings.ReplaceAll(strings.ReplaceAll(body, "\t", "    "), "\r\n", "\n")
	}
	lines := strings.Split(body, "\n")
	widths := make([]int, len(lines))
	for index, line := range lines {
		lineWidth := ansi.StringWidth(line)
		if width >= 1 && lineWidth > width {
			lines[index] = ansi.Truncate(line, width, "")
			lineWidth = ansi.StringWidth(lines[index])
		}
		widths[index] = lineWidth
	}
	return lines, widths
}

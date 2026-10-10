package app

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/nickooan/ntee-editor/internal/fuzzy"
	"github.com/nickooan/ntee-editor/internal/input"
)

var (
	opSuccessStyle  = lipgloss.NewStyle().Foreground(colGreen).Bold(true).Background(colBg)
	opFailureStyle  = lipgloss.NewStyle().Foreground(colRed).Bold(true).Background(colBg)
	opRunningStyle  = lipgloss.NewStyle().Foreground(colYellow).Bold(true).Background(colBg)
	opInputStyle    = lipgloss.NewStyle().Foreground(colFg).Background(colBg)
	opLinkStyle     = lipgloss.NewStyle().Foreground(colBlue).Underline(true).Background(colBg)
	opLinkHintStyle = lipgloss.NewStyle().Foreground(colBlue).Background(colBg)
)

const opPreviewMaxLines = 4

const opLinkHint = " click a link to open it "

// renderOpOverlay draws the Ctrl+R overlay for its current stage: the command
// picker, the args prompt, or the streaming run output.
func (m Model) renderOpOverlay(width, height int) string {
	var box string
	switch m.opMode.stage {
	case opStageRun:
		box = m.renderOpRunBox(width, height)
	case opStageArgs:
		box = m.renderOpArgsBox(width)
	default:
		box = m.renderOpPickBox(width)
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceStyle(whitespaceStyle))
}

func (m Model) renderOpPickBox(width int) string {
	const maxRows = 12
	boxWidth := input.Clamp(width*8/10, 24, max(24, width-2))
	rowWidth := max(1, boxWidth-2)

	lines := []string{promptStyle.Render("run ") + renderInputLine(m.opMode.query, len([]rune(m.opMode.query)))}
	// With inline args ("test 10"), show exactly what Enter will run.
	if commandLine, ok, err := m.opInlinePreview(); ok {
		if err != nil {
			lines = append(lines, opFailureStyle.Render(truncateRunes(err.Error(), rowWidth)))
		} else {
			for _, line := range wrapRunes("$ "+commandLine, rowWidth, opPreviewMaxLines) {
				lines = append(lines, opSuccessStyle.Render(line))
			}
		}
	}
	lines = append(lines, "")
	if len(m.opMode.matches) == 0 {
		switch {
		case len(m.opMode.candidates) == 0 && m.opCommandsLoading:
			lines = append(lines, overlayHintStyle.Render("(loading op-commands…)"))
		case len(m.opMode.candidates) == 0:
			lines = append(lines, overlayHintStyle.Render(truncateRunes("(no op-commands — add some in Ctrl+T › op-commands)", rowWidth)))
		default:
			lines = append(lines, overlayHintStyle.Render("(no matches)"))
		}
	}

	visible := min(len(m.opMode.matches), maxRows)
	selected := input.Clamp(m.opMode.index, 0, max(0, len(m.opMode.matches)-1))
	start := 0
	if selected >= visible {
		start = selected - visible + 1
	}
	for index := start; index < start+visible && index < len(m.opMode.matches); index++ {
		match := m.opMode.matches[index]
		command := m.opMode.candidates[match.Index]
		if index == selected {
			lines = append(lines, selectedEntryStyle.Render(padTo(truncateRunes(" "+command.Name+"  "+command.Command, rowWidth), rowWidth)))
			continue
		}
		nameWidth := min(rowWidth, len([]rune(command.Name))+1)
		positions := fuzzy.Positions(m.opMode.filteredName, m.opMode.corpus[match.Index])
		row := renderFuzzyRow(command.Name, positions, nameWidth, false)
		if rest := rowWidth - nameWidth; rest > 0 {
			row += overlayHintStyle.Render(padTo(truncateRunes("  "+command.Command, rest), rest))
		}
		lines = append(lines, row)
	}
	lines = append(lines, "", overlayHintStyle.Render(truncateRunes("name [args…] · ↑/↓ choose · Enter run · Esc close", rowWidth)))
	return modalStyle.Width(boxWidth + 2).Render(strings.Join(lines, "\n"))
}

func (m Model) renderOpArgsBox(width int) string {
	boxWidth := input.Clamp(width*8/10, 24, max(24, width-2))
	rowWidth := max(1, boxWidth-2)
	command := m.opMode.selected

	lines := []string{
		modalTitleStyle.Render(truncateRunes("run "+command.Name, rowWidth)),
		overlayHintStyle.Render(truncateRunes(command.Command, rowWidth)),
		"",
		promptStyle.Render("args › ") + renderInputLineStyled(m.opMode.args, m.opMode.argsCursor, opInputStyle),
	}
	if commandLine, err := m.renderOpCommandLine(); err != nil {
		lines = append(lines, opFailureStyle.Render(truncateRunes(err.Error(), rowWidth)))
	} else {
		for _, line := range wrapRunes("$ "+commandLine, rowWidth, opPreviewMaxLines) {
			lines = append(lines, opSuccessStyle.Render(line))
		}
	}
	lines = append(lines, "", overlayHintStyle.Render(truncateRunes(`enter run · esc back · quote args with spaces: "a b"`, rowWidth)))
	return modalStyle.Width(boxWidth + 2).Render(strings.Join(lines, "\n"))
}

// opRunLayout is the run box's geometry, shared by renderOpRunBox and the
// link hit-test (opRunLinkAt) so click math can't drift from the drawing.
type opRunLayout struct {
	boxWidth, innerWidth, innerHeight int
	header                            []string // wrapped "$ command" rows
	outputHeight                      int
}

func newOpRunLayout(run *opRunState, width, height int) opRunLayout {
	boxWidth := input.Clamp(width*9/10, 40, max(40, width-2))
	innerWidth := max(1, boxWidth-2)
	innerHeight := max(6, height-4)
	header := wrapRunes("$ "+run.commandLine, innerWidth, opPreviewMaxLines)
	return opRunLayout{
		boxWidth:     boxWidth,
		innerWidth:   innerWidth,
		innerHeight:  innerHeight,
		header:       header,
		outputHeight: max(1, innerHeight-len(header)-3), // two dividers, footer
	}
}

// visibleRange is the output window. Scrolling back never leaves a
// part-empty window at the top.
func (layout opRunLayout) visibleRange(lineCount, scroll int) (start, end int) {
	end = max(min(lineCount, layout.outputHeight), lineCount-scroll)
	return max(0, end-layout.outputHeight), end
}

// opDisplayLine is an output line as drawn: tabs expanded, so rune columns
// are screen columns.
func opDisplayLine(line string) string {
	return strings.ReplaceAll(line, "\t", "    ")
}

// renderOpRunBox draws the run view: the command line, the output tail (or a
// scrolled-back window of it) with clickable links, and the running /
// finished footer.
func (m Model) renderOpRunBox(width, height int) string {
	run := m.opMode.run
	layout := newOpRunLayout(run, width, height)
	innerWidth := layout.innerWidth

	divider := overlayHintStyle.Render(strings.Repeat("─", innerWidth))
	rows := make([]string, 0, layout.innerHeight)
	for _, line := range layout.header {
		rows = append(rows, modalTitleStyle.Render(padTo(line, innerWidth)))
	}
	rows = append(rows, divider)

	lines := run.output.displayLines()
	start, end := layout.visibleRange(len(lines), run.scroll)
	hasLinks := false
	for _, line := range lines[start:end] {
		line = opDisplayLine(line)
		links := findOutputLinks(line)
		hasLinks = hasLinks || len(links) > 0
		rows = append(rows, renderOpOutputLine(line, links, innerWidth))
	}
	for len(rows) < len(layout.header)+1+layout.outputHeight {
		rows = append(rows, baseStyle.Render(strings.Repeat(" ", innerWidth)))
	}

	text, ok := run.status()
	style := opSuccessStyle
	switch {
	case !run.finished:
		style = opRunningStyle
	case !ok:
		style = opFailureStyle
	}
	if run.scroll > 0 {
		text += "  ·  End follow"
	}
	footerDivider := divider
	// The hint rides on the divider so it never crowds the status text.
	if hasLinks && innerWidth > len(opLinkHint)+2 {
		footerDivider = overlayHintStyle.Render(strings.Repeat("─", innerWidth-len(opLinkHint)-2)) +
			opLinkHintStyle.Render(opLinkHint) + overlayHintStyle.Render("──")
	}
	rows = append(rows, footerDivider, style.Render(padTo(truncateRunes(text, innerWidth), innerWidth)))
	return modalStyle.Width(layout.boxWidth + 2).Render(strings.Join(rows, "\n"))
}

// renderOpOutputLine draws one output line clipped to width, links underlined.
func renderOpOutputLine(line string, links []opLink, width int) string {
	runes := []rune(line)
	visible := min(len(runes), width)
	var b strings.Builder
	position := 0
	for _, link := range links {
		if link.start >= visible {
			break
		}
		if link.start > position {
			b.WriteString(baseStyle.Render(string(runes[position:link.start])))
		}
		linkEnd := min(link.end, visible)
		b.WriteString(opLinkStyle.Render(string(runes[link.start:linkEnd])))
		position = linkEnd
	}
	if position < visible {
		b.WriteString(baseStyle.Render(string(runes[position:visible])))
	}
	if pad := width - visible; pad > 0 {
		b.WriteString(baseStyle.Render(strings.Repeat(" ", pad)))
	}
	return b.String()
}

// wrapRunes hard-wraps text into at most maxLines rows of width runes, marking
// a cut with a trailing "…".
func wrapRunes(text string, width, maxLines int) []string {
	runes := []rune(text)
	width = max(1, width)
	var lines []string
	for len(runes) > 0 && len(lines) < maxLines {
		cut := min(width, len(runes))
		lines = append(lines, string(runes[:cut]))
		runes = runes[cut:]
	}
	if len(runes) > 0 {
		last := []rune(lines[len(lines)-1])
		lines[len(lines)-1] = string(last[:len(last)-1]) + "…"
	}
	return lines
}

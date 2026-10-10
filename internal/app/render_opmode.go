package app

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/nickooan/ntee-editor/internal/fuzzy"
	"github.com/nickooan/ntee-editor/internal/input"
)

var (
	opSuccessStyle = lipgloss.NewStyle().Foreground(colGreen).Bold(true).Background(colBg)
	opFailureStyle = lipgloss.NewStyle().Foreground(colRed).Bold(true).Background(colBg)
	opRunningStyle = lipgloss.NewStyle().Foreground(colYellow).Bold(true).Background(colBg)
	opInputStyle   = lipgloss.NewStyle().Foreground(colFg).Background(colBg)
)

const opPreviewMaxLines = 4

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

	var b strings.Builder
	b.WriteString(promptStyle.Render("run ") + renderInputLine(m.opMode.query, len([]rune(m.opMode.query))) + "\n")
	if len(m.opMode.matches) == 0 {
		switch {
		case len(m.opMode.candidates) == 0 && m.opCommandsLoading:
			b.WriteString(overlayHintStyle.Render("(loading op-commands…)"))
		case len(m.opMode.candidates) == 0:
			b.WriteString(overlayHintStyle.Render(truncateRunes("(no op-commands — add some in Ctrl+T › op-commands)", rowWidth)))
		default:
			b.WriteString(overlayHintStyle.Render("(no matches)"))
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
			b.WriteString("\n" + selectedEntryStyle.Render(padTo(truncateRunes(" "+command.Name+"  "+command.Command, rowWidth), rowWidth)))
			continue
		}
		nameWidth := min(rowWidth, len([]rune(command.Name))+1)
		positions := fuzzy.Positions(m.opMode.query, m.opMode.corpus[match.Index])
		row := renderFuzzyRow(command.Name, positions, nameWidth, false)
		if rest := rowWidth - nameWidth; rest > 0 {
			row += overlayHintStyle.Render(padTo(truncateRunes("  "+command.Command, rest), rest))
		}
		b.WriteString("\n" + row)
	}
	return modalStyle.Width(boxWidth + 2).Render(b.String())
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

// renderOpRunBox draws the run view: the command line, the output tail (or a
// scrolled-back window of it), and the running / finished footer.
func (m Model) renderOpRunBox(width, height int) string {
	run := m.opMode.run
	boxWidth := input.Clamp(width*9/10, 40, max(40, width-2))
	innerWidth := max(1, boxWidth-2)
	innerHeight := max(6, height-4)
	header := wrapRunes("$ "+run.commandLine, innerWidth, opPreviewMaxLines)
	outputHeight := max(1, innerHeight-len(header)-3) // two dividers, footer

	divider := overlayHintStyle.Render(strings.Repeat("─", innerWidth))
	rows := make([]string, 0, innerHeight)
	for _, line := range header {
		rows = append(rows, modalTitleStyle.Render(padTo(line, innerWidth)))
	}
	rows = append(rows, divider)

	lines := run.output.displayLines()
	// Scrolling back never leaves a part-empty window at the top.
	end := max(min(len(lines), outputHeight), len(lines)-run.scroll)
	start := max(0, end-outputHeight)
	for _, line := range lines[start:end] {
		line = strings.ReplaceAll(line, "\t", "    ")
		rows = append(rows, baseStyle.Render(padTo(truncateRunes(line, innerWidth), innerWidth)))
	}
	for len(rows) < len(header)+1+outputHeight {
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
	rows = append(rows, divider, style.Render(padTo(truncateRunes(text, innerWidth), innerWidth)))
	return modalStyle.Width(boxWidth + 2).Render(strings.Join(rows, "\n"))
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

// renderInspectOpCommands draws the op-commands key/value table. The row being
// edited shows inline inputs; the trailing row adds a new command.
func (m Model) renderInspectOpCommands(width, height int) string {
	title := dirStyle.Render(truncateRunes("op-commands (global · run with Ctrl+R)", width))
	switch {
	case m.opCommandsLoading && len(m.opCommands) == 0:
		return title + "\n\n" + baseStyle.Render("loading op-commands…")
	case m.opCommandsErr != nil:
		title += "\n" + errStyle.Render(truncateRunes("load failed: "+m.opCommandsErr.Error(), width))
	}

	keyWidth := len("key")
	for _, command := range m.opCommands {
		keyWidth = max(keyWidth, len([]rune(command.Name)))
	}
	if m.opTable.editing {
		keyWidth = max(keyWidth, len([]rune(m.opTable.key))+1)
	}
	keyWidth = min(keyWidth+2, max(8, width/3))
	valueWidth := max(1, width-keyWidth)

	header := hintStyle.Render(padTo("key", keyWidth) + "value")
	rowCount := len(m.opCommands) + 1
	tableHeight := max(1, height-6)
	start := 0
	if m.opTable.index >= tableHeight {
		start = m.opTable.index - tableHeight + 1
	}

	rows := []string{title, "", header}
	for index := start; index < rowCount && index < start+tableHeight; index++ {
		selected := m.opTable.focused && index == m.opTable.index
		if selected && m.opTable.editing {
			rows = append(rows, m.renderOpEditRow(keyWidth, valueWidth))
			continue
		}
		var key, value string
		if index < len(m.opCommands) {
			key, value = m.opCommands[index].Name, m.opCommands[index].Command
		} else {
			key = "+ new command"
		}
		text := padTo(truncateRunes(key, keyWidth-1), keyWidth) + truncateRunes(value, valueWidth)
		switch {
		case selected:
			rows = append(rows, selectedEntryStyle.Render(padTo(text, width)))
		case index == len(m.opCommands):
			rows = append(rows, ignoredFileStyle.Render(text))
		default:
			rows = append(rows, fileStyle.Render(padTo(truncateRunes(key, keyWidth-1), keyWidth))+baseStyle.Render(truncateRunes(value, valueWidth)))
		}
	}
	if m.opTable.confirmDelete != "" {
		rows = append(rows, "", errStyle.Render(truncateRunes("delete "+m.opTable.confirmDelete+"? y/n", width)))
	}
	rows = append(rows, "", hintStyle.Render(truncateRunes("value: shell command · {$1} {$2} … user args · {$fpath} open file's path", width)))
	return strings.Join(rows, "\n")
}

func (m Model) renderOpEditRow(keyWidth, valueWidth int) string {
	keyCell, valueCell := opInputStyle.Render(m.opTable.key), opInputStyle.Render(truncateRunes(m.opTable.value, valueWidth))
	if m.opTable.field == opFieldKey {
		keyCell = renderInputLineStyled(m.opTable.key, m.opTable.cursor, opInputStyle)
	} else {
		valueCell = renderInputLineStyled(m.opTable.value, m.opTable.cursor, opInputStyle)
	}
	if pad := keyWidth - lipgloss.Width(keyCell); pad > 0 {
		keyCell += opInputStyle.Render(strings.Repeat(" ", pad))
	}
	return keyCell + valueCell
}

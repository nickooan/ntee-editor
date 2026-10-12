package app

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/nickooan/ntee-editor/internal/fuzzy"
	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/opcmd"
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

	lines := []string{promptStyle.Render("run ") + renderInputLine(m.opMode.query, m.opMode.queryCursor)}
	lines = append(lines, m.renderOpTargetLines(rowWidth)...)
	// With inline args ("test 10"), show exactly what Enter will run.
	if commandLine, ok, err := m.opInlinePreview(); ok {
		if err != nil {
			lines = append(lines, renderOpError(err, rowWidth)...)
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
		case len(m.opCommands) == 0:
			lines = append(lines, overlayHintStyle.Render(truncateRunes("(no op-commands — add some in Ctrl+T › op-commands)", rowWidth)))
		case len(m.opMode.candidates) == 0:
			fileType := opcmd.FileTypeOf(m.openRel)
			hint := fmt.Sprintf("(no op-commands for %s files — set TYPE to %s or * in Ctrl+T › op-commands)", fileType, fileType)
			for _, line := range wrapWords(hint, rowWidth) {
				lines = append(lines, overlayHintStyle.Render(line))
			}
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
	lines = append(lines, "", overlayHintStyle.Render(truncateRunes("name [args…] [-- flags] · ↑/↓ choose · Enter run · Esc close", rowWidth)))
	return modalStyle.Width(boxWidth + 2).Render(strings.Join(lines, "\n"))
}

func (m Model) renderOpArgsBox(width int) string {
	boxWidth := input.Clamp(width*8/10, 24, max(24, width-2))
	rowWidth := max(1, boxWidth-2)
	command := m.opMode.selected

	lines := []string{
		modalTitleStyle.Render(truncateRunes("run "+command.Name, rowWidth)),
		overlayHintStyle.Render(truncateRunes(command.Command, rowWidth)),
	}
	lines = append(lines, m.renderOpTargetLines(rowWidth)...)
	lines = append(lines, renderOpArgLines(m.opMode.template, m.opMode.args, rowWidth)...)
	lines = append(lines, "",
		promptStyle.Render("args › ")+renderInputLineStyled(m.opMode.args, m.opMode.argsCursor, opInputStyle))
	if commandLine, err := m.renderOpCommandLine(); err != nil {
		lines = append(lines, renderOpError(err, rowWidth)...)
	} else {
		for _, line := range wrapRunes("$ "+commandLine, rowWidth, opPreviewMaxLines) {
			lines = append(lines, opSuccessStyle.Render(line))
		}
	}
	lines = append(lines, "", overlayHintStyle.Render(truncateRunes(`enter run · esc back · quote args with spaces: "a b" · -- flags go at the end`, rowWidth)))
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

// renderOpArgLines describes the arguments in the args stage. Once the typed
// args bind (the same opcmd.Bind the command preview renders with), it shows
// what each index receives: "$1 a · $2 master (default) · $3 b", then any
// words after "--" ("-- -v (appended)"). Until then it shows the spec
// ("$1 required · $2 = master") and, when required and optional args mix, how
// many values to type.
func renderOpArgLines(template opcmd.Template, argsLine string, width int) []string {
	separator := overlayHintStyle.Render(" · ")
	name := func(index int) string { return opRowNormal.arg.Render("$" + strconv.Itoa(index)) }
	if args, extra, err := opcmd.SplitArgs(argsLine); err == nil && (len(args) > 0 || len(extra) > 0 || template.MinArg == 0) {
		if bindings, err := template.Bind(args); err == nil {
			var parts []string
			for _, binding := range bindings {
				switch {
				case !binding.FromDefault:
					parts = append(parts, name(binding.Index)+opInputStyle.Render(" "+opcmd.ShellQuote(binding.Value)))
				case binding.Value == "":
					parts = append(parts, name(binding.Index)+overlayHintStyle.Render(" (omitted)"))
				default:
					parts = append(parts, name(binding.Index)+overlayHintStyle.Render(" "+opcmd.ShellQuote(binding.Value)+" (default)"))
				}
			}
			if len(extra) > 0 {
				quoted := make([]string, len(extra))
				for index, word := range extra {
					quoted[index] = opcmd.ShellQuote(word)
				}
				parts = append(parts, opRowNormal.arg.Render("--")+opInputStyle.Render(" "+strings.Join(quoted, " "))+
					overlayHintStyle.Render(" (appended)"))
			}
			return wrapStyled(parts, separator, width)
		}
	}

	var parts, required []string
	for _, spec := range template.ArgSpecs() {
		switch {
		case !spec.Optional:
			parts = append(parts, name(spec.Index)+overlayHintStyle.Render(" required"))
			required = append(required, "$"+strconv.Itoa(spec.Index))
		case spec.Default == "" && spec.Fragment != "":
			parts = append(parts, name(spec.Index)+overlayHintStyle.Render(" optional: "+spec.Fragment))
		case spec.Default == "":
			parts = append(parts, name(spec.Index)+overlayHintStyle.Render(` = "" (omitted)`))
		default:
			parts = append(parts, name(spec.Index)+overlayHintStyle.Render(" = "+spec.Default))
		}
	}
	lines := wrapStyled(parts, separator, width)
	if template.MinArg > 0 && template.MinArg < template.MaxArg {
		hint := fmt.Sprintf("type %s for %s, or up to %d to fill $1…$%d in order",
			opcmd.Plural(template.MinArg, "value"), strings.Join(required, " "), template.MaxArg, template.MaxArg)
		for _, line := range wrapWords(hint, width) {
			lines = append(lines, overlayHintStyle.Render(line))
		}
	}
	return lines
}

const (
	opTargetPrefix      = "file "
	opUnsavedWarning    = "unsaved changes — the command reads the saved file (Ctrl+S in the editor first)"
	opRunUnsavedSuffix  = " · unsaved edits not included"
	opRunDividerLeading = 2 // "──" before the run divider's label
)

// renderOpTargetLines names the file the command runs against and warns when
// it has unsaved edits the command won't see.
func (m Model) renderOpTargetLines(width int) []string {
	pathWidth := max(1, width-len(opTargetPrefix))
	lines := []string{overlayHintStyle.Render(opTargetPrefix) +
		opInputStyle.Render(truncateRunesLeft(m.opTargetPath(), pathWidth))}
	if m.opTargetUnsaved() {
		for _, line := range wrapWords(opUnsavedWarning, width) {
			lines = append(lines, opRunningStyle.Render(line))
		}
	}
	return lines
}

// truncateRunesLeft keeps the end of text (a path's file name) and marks the
// cut with a leading "…".
func truncateRunesLeft(text string, width int) string {
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width <= 1 {
		return "…"
	}
	return "…" + string(runes[len(runes)-width+1:])
}

// renderOpRunTargetDivider is the run box's top divider labelled with the
// target file (yellow when the run started with unsaved edits). It keeps the
// divider's width, so the box layout and link click math are unchanged.
func renderOpRunTargetDivider(run *opRunState, width int) string {
	suffix, style := "", overlayHintStyle
	if run.unsaved {
		suffix, style = opRunUnsavedSuffix, opRunningStyle
	}
	// Room for the path: the divider keeps its leading "──", the two spaces
	// around the label, and at least one trailing "─".
	pathWidth := width - opRunDividerLeading - 2 - len([]rune(suffix)) - 1
	if run.target == "" || pathWidth < 4 {
		return overlayHintStyle.Render(strings.Repeat("─", width))
	}
	label := " " + truncateRunesLeft(run.target, pathWidth) + suffix + " "
	rest := width - opRunDividerLeading - len([]rune(label))
	return overlayHintStyle.Render(strings.Repeat("─", opRunDividerLeading)) + style.Render(label) +
		overlayHintStyle.Render(strings.Repeat("─", rest))
}

// renderOpError word-wraps an args/render error so it is never cut off.
func renderOpError(err error, width int) []string {
	var lines []string
	for _, line := range wrapWords(err.Error(), width) {
		lines = append(lines, opFailureStyle.Render(line))
	}
	return lines
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
	rows = append(rows, renderOpRunTargetDivider(run, innerWidth))

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

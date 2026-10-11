package app

import (
	"image/color"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/nickooan/ntee-editor/internal/opcmd"
	"github.com/nickooan/ntee-editor/internal/store"
)

var (
	opFocusHintStyle = lipgloss.NewStyle().Foreground(colYellow).Background(colBg)
	opButtonStyle    = lipgloss.NewStyle().Bold(true).Foreground(colGreen).Background(colBg)
	opButtonSelStyle = lipgloss.NewStyle().Bold(true).Foreground(colBg).Background(colGreen)
	opDeleteStyle    = lipgloss.NewStyle().Bold(true).Foreground(colRed).Background(colBg)
)

// opRowStyles is one row's palette: every run carries the row's background,
// so a highlighted row stays one solid bar.
type opRowStyles struct {
	text, dim, key, fileType, marker, arg, system, invalid lipgloss.Style
}

func newOpRowStyles(background, textColor, keyColor color.Color) opRowStyles {
	base := lipgloss.NewStyle().Background(background)
	return opRowStyles{
		text:     base.Foreground(textColor),
		dim:      base.Foreground(colComment),
		key:      base.Foreground(keyColor).Bold(true),
		fileType: base.Foreground(colAqua),
		marker:   base.Foreground(colAqua).Bold(true),
		arg:      base.Foreground(colPurple).Bold(true),
		system:   base.Foreground(colBlue).Bold(true),
		invalid:  base.Foreground(colRed).Underline(true),
	}
}

var (
	opRowNormal   = newOpRowStyles(colBg, colFg, colYellow)
	opRowSelected = newOpRowStyles(colSelection, colFg, colYellow)
	// The row being edited: the inactive cell is dimmed on the row color and
	// the active cell is an inset input on the darkest background.
	opRowEditing = newOpRowStyles(colLineHl, colComment, colComment)
	opRowInput   = newOpRowStyles(colBgChrome, colFg, colYellow)
)

type opTokenClass uint8

const (
	opTokenText opTokenClass = iota
	opTokenArg
	opTokenSystem
	opTokenInvalid
)

// opTokenClasses colors a template per rune: {$n} / {$n or default} args,
// system variables, and anything opcmd would reject (half-typed included).
// Spans is the parser's own scanner, so the colors match what Parse accepts.
func opTokenClasses(runes []rune) []opTokenClass {
	classes := make([]opTokenClass, len(runes))
	text := string(runes)
	for _, span := range opcmd.Spans(text) {
		class := opTokenInvalid
		switch span.Kind {
		case opcmd.SpanArg:
			class = opTokenArg
		case opcmd.SpanSystem:
			class = opTokenSystem
		}
		first := utf8.RuneCountInString(text[:span.Start])
		for index := first; index < first+utf8.RuneCountInString(text[span.Start:span.End]); index++ {
			classes[index] = class
		}
	}
	return classes
}

func (styles opRowStyles) forClass(class opTokenClass) lipgloss.Style {
	switch class {
	case opTokenArg:
		return styles.arg
	case opTokenSystem:
		return styles.system
	case opTokenInvalid:
		return styles.invalid
	}
	return styles.text
}

// opCellKind picks how a table cell colors its text.
type opCellKind uint8

const (
	opCellKey opCellKind = iota
	opCellFileType
	opCellTemplate // placeholders colored by opTokenClasses
)

// renderOpCell renders text into exactly width cells. With cursor >= 0 it is
// an input: the view scrolls horizontally to keep the cursor visible.
// Otherwise overlong text ends in "…".
func renderOpCell(text string, width, cursor int, kind opCellKind, styles opRowStyles) string {
	runes := []rune(text)
	asTemplate := kind == opCellTemplate
	var classes []opTokenClass
	if asTemplate {
		classes = opTokenClasses(runes)
	}
	styleAt := func(index int) lipgloss.Style {
		switch kind {
		case opCellFileType:
			return styles.fileType
		case opCellTemplate:
			return styles.forClass(classes[index])
		}
		return styles.key
	}

	start, end, truncated := 0, min(len(runes), width), false
	if cursor >= 0 {
		start = max(0, cursor-(width-1))
		end = min(len(runes), start+width)
	} else if len(runes) > width {
		end, truncated = max(0, width-1), true
	}

	var b strings.Builder
	used := 0
	for index := start; index < end; {
		if index == cursor {
			b.WriteString(cursorStyle.Render(string(runes[index])))
			index++
			used++
			continue
		}
		style := styleAt(index)
		runEnd := index + 1
		for runEnd < end && runEnd != cursor && (!asTemplate || classes[runEnd] == classes[index]) {
			runEnd++
		}
		b.WriteString(style.Render(string(runes[index:runEnd])))
		used += runEnd - index
		index = runEnd
	}
	if cursor >= 0 && cursor == len(runes) && used < width {
		b.WriteString(cursorStyle.Render(" "))
		used++
	}
	if truncated {
		b.WriteString(styles.text.Render("…"))
		used++
	}
	if pad := width - used; pad > 0 {
		b.WriteString(styles.text.Render(strings.Repeat(" ", pad)))
	}
	return b.String()
}

// opTypePlaceholder fills an empty TYPE input: left empty, it saves as "*".
const opTypePlaceholder = "*  all files"

// newOpTableLayout sizes the op-commands table: keys fit the longest name (or
// the key being typed), capped at a third of the width; TYPE fits the longest
// file-type spec, capped at a fifth; a marker gutter shows the selected (▸)
// or edited (✎) row.
func (m Model) newOpTableLayout(width int) panelTable {
	keyWidth, typeWidth := len("KEY"), len("TYPE")
	for _, command := range m.opCommands {
		keyWidth = max(keyWidth, len([]rune(command.Name)))
		typeWidth = max(typeWidth, len([]rune(command.FileTypes)))
	}
	if m.opTable.editing {
		keyWidth = max(keyWidth, len([]rune(m.opTable.key))+1)
		typeWidth = max(typeWidth, len([]rune(m.opTable.fileTypes))+1)
		if m.opTable.field == opFieldType && m.opTable.fileTypes == "" {
			typeWidth = max(typeWidth, len([]rune(opTypePlaceholder))+1)
		}
	}
	return newPanelTable(width, min(keyWidth, max(6, width/3)), min(typeWidth, max(6, width/5)), true)
}

// renderOpTypeCell draws a stored TYPE: "*" dimmed, specific types in aqua.
func renderOpTypeCell(fileTypes string, width int, styles opRowStyles) string {
	if fileTypes == "" || fileTypes == store.AllFileTypes {
		return styles.dim.Render(padTo("*", width))
	}
	return renderOpCell(fileTypes, width, -1, opCellFileType, styles)
}

// renderInspectOpCommands draws the op-commands panel: a bordered key/command
// table with colored placeholders, an inline editor with placeholder
// completion, a "+ New command" button row, and a placeholder legend.
func (m Model) renderInspectOpCommands(width, height int) string {
	width = panelInnerWidth(width)
	layout := m.newOpTableLayout(width)
	contentWidth := layout.contentWidth()

	title := panelTitleStyle.Render("op-commands")
	if !m.opTable.focused {
		title += panelSubtitleStyle.Render("  ·  ") + opFocusHintStyle.Render("press → to edit")
	}
	rows := panelHeading("", "Shell commands shared by every project — run one on the open file with Ctrl+R.", width)
	rows[0] = title
	if m.opCommandsErr != nil {
		rows = append(rows, opDeleteStyle.Render(truncateRunes("load failed: "+m.opCommandsErr.Error(), width)))
	}
	rows = append(rows, "",
		layout.border("╭", "┬", "╮"),
		layout.header("KEY", "TYPE", "COMMAND"),
	)

	body, anchor, anchorSpan := m.opTableBody(layout)
	if len(body) == 0 {
		rows = append(rows, layout.border("├", "┴", "┤"))
		for _, line := range m.opTableEmptyLines(contentWidth) {
			rows = append(rows, layout.spanning(line))
		}
		rows = append(rows, layout.border("├", "", "┤"))
	} else {
		rows = append(rows, layout.border("├", "┼", "┤"))
		// Fixed rows around the body: title block, borders, button, legend.
		visible := max(3, height-len(rows)-9)
		start := 0
		if anchor+anchorSpan > visible {
			start = anchor + anchorSpan - visible
		}
		rows = append(rows, body[start:min(len(body), start+visible)]...)
		rows = append(rows, layout.border("├", "┴", "┤"))
	}
	rows = append(rows, layout.spanning(m.opNewButton(contentWidth)), layout.border("╰", "", "╯"))

	if existing, conflict := m.opEditConflict(); m.opTable.editing && conflict {
		rows = append(rows, "", opDeleteStyle.Render(truncateRunes(
			"“"+strings.TrimSpace(m.opTable.key)+"” already exists for "+describeFileTypes(existing.FileTypes)+
				" — change the key or its file types", width)))
	}
	if deleteKey := m.opTable.confirmDelete; deleteKey != "" {
		rows = append(rows, "", opDeleteStyle.Render("Delete “"+m.opCommandLabel(deleteKey)+"”?")+
			panelSubtitleStyle.Render("  y delete · n keep"))
	}
	rows = append(rows, "")
	rows = append(rows, m.opLegend(width)...)
	return panelIndent(rows)
}

// opTableBody renders the command rows (plus the edit row and its suggestion
// dropdown). anchor is the body index of the selected row and anchorSpan how
// many rows (row + dropdown) must stay visible below it.
func (m Model) opTableBody(layout panelTable) (body []string, anchor, anchorSpan int) {
	anchorSpan = 1
	editRow := func() []string {
		rows := []string{m.renderOpEditRow(layout)}
		return append(rows, m.renderOpSuggestionRows(layout)...)
	}
	for index, command := range m.opCommands {
		if index > 0 {
			body = append(body, layout.rowRule())
		}
		selected := m.opTable.focused && index == m.opTable.index
		if selected && m.opTable.editing {
			anchor = len(body)
			edit := editRow()
			anchorSpan = len(edit)
			body = append(body, edit...)
			continue
		}
		styles, marker := opRowNormal, " "
		if selected {
			styles, marker = opRowSelected, "▸"
			anchor = len(body)
		}
		body = append(body, layout.row(marker, []string{
			renderOpCell(command.Name, layout.keyWidth, -1, opCellKey, styles),
			renderOpTypeCell(command.FileTypes, layout.middleWidth, styles),
			renderOpCell(command.Command, layout.valueWidth, -1, opCellTemplate, styles),
		}, styles.text, styles.marker))
	}
	if m.opTable.editing && m.opTable.index >= len(m.opCommands) {
		if len(body) > 0 {
			body = append(body, layout.rowRule())
		}
		anchor = len(body)
		edit := editRow()
		anchorSpan = len(edit)
		body = append(body, edit...)
	}
	return body, anchor, anchorSpan
}

func (m Model) renderOpEditRow(layout panelTable) string {
	stylesFor := func(field int) (opRowStyles, int) {
		if m.opTable.field == field {
			return opRowInput, m.opTable.cursor
		}
		return opRowEditing, -1
	}
	keyStyles, keyCursor := stylesFor(opFieldKey)
	typeStyles, typeCursor := stylesFor(opFieldType)
	valueStyles, valueCursor := stylesFor(opFieldValue)
	if _, conflict := m.opEditConflict(); conflict {
		keyStyles.key = keyStyles.key.Foreground(colRed)
	}

	var typeCell string
	switch {
	case m.opTable.fileTypes != "":
		typeCell = renderOpCell(m.opTable.fileTypes, layout.middleWidth, typeCursor, opCellFileType, typeStyles)
	case typeCursor >= 0:
		typeCell = cursorStyle.Render(" ") + typeStyles.dim.Render(padTo(truncateRunes(opTypePlaceholder, layout.middleWidth-1), layout.middleWidth-1))
	default:
		typeCell = typeStyles.dim.Render(padTo("*", layout.middleWidth))
	}

	bar := panelBorderStyle.Render("│")
	gap := opRowEditing.text.Render(" ")
	return bar + gap + opRowEditing.key.Foreground(colYellow).Render("✎") + gap +
		renderOpCell(m.opTable.key, layout.keyWidth, keyCursor, opCellKey, keyStyles) + gap + bar + gap +
		typeCell + gap + bar + gap +
		renderOpCell(m.opTable.value, layout.valueWidth, valueCursor, opCellTemplate, valueStyles) + gap + bar
}

// opSuggestionMaxRows caps the completion dropdown; the window follows the
// selection (the built-in file-type list is long).
const opSuggestionMaxRows = 8

// renderOpSuggestionRows draws the completion menu as a dropdown under the
// active cell: placeholders in the command column, file types starting at
// the TYPE column (spanning into the command column, which is wider).
func (m Model) renderOpSuggestionRows(layout panelTable) []string {
	_, candidates := m.opSuggestions()
	if len(candidates) == 0 {
		return nil
	}
	selected := min(m.opTable.suggestIndex, len(candidates)-1)
	first := max(0, selected-opSuggestionMaxRows+1)
	visible := candidates[first:min(len(candidates), first+opSuggestionMaxRows)]

	areaWidth := layout.valueWidth
	if m.opTable.field == opFieldType {
		areaWidth = layout.middleWidth + 3 + layout.valueWidth
	}
	insertWidth := 0
	for _, candidate := range visible {
		insertWidth = max(insertWidth, len([]rune(candidate.Insert)))
	}
	menuWidth := min(areaWidth, insertWidth+36)
	blank := func(width int) string { return opRowNormal.text.Render(strings.Repeat(" ", width)) }
	rows := make([]string, 0, len(visible))
	for offset, candidate := range visible {
		labelStyle, detailStyle := completionItemStyle, completionDetailStyle
		if first+offset == selected {
			labelStyle, detailStyle = completionSelStyle, completionSelDetailStyle
		}
		label := " " + padTo(candidate.Insert, insertWidth) + "  "
		item := labelStyle.Render(truncateRunes(label, menuWidth))
		if rest := menuWidth - len([]rune(label)); rest > 0 {
			item += detailStyle.Render(padTo(truncateRunes(candidate.Detail, rest), rest))
		}
		menuCell := item + blank(max(0, areaWidth-menuWidth))
		cells := []string{blank(layout.keyWidth), blank(layout.middleWidth), menuCell}
		if m.opTable.field == opFieldType {
			cells = []string{blank(layout.keyWidth), menuCell}
		}
		rows = append(rows, layout.row(" ", cells, opRowNormal.text, opRowNormal.marker))
	}
	return rows
}

func (m Model) opTableEmptyLines(width int) []string {
	line := func(style lipgloss.Style, text string) string {
		return style.Render(padTo(truncateRunes(text, width), width))
	}
	if m.opCommandsLoading {
		return []string{line(panelSubtitleStyle, "Loading op-commands…")}
	}
	example := opRowNormal.key.Render("deploy") + opRowNormal.text.Render("  ") + opRowNormal.fileType.Render("yaml") +
		opRowNormal.text.Render("  →  kubectl apply -f ") +
		opRowNormal.system.Render("{$fpath}") + opRowNormal.text.Render(" -n ") + opRowNormal.arg.Render("{$1}")
	exampleWidth := lipgloss.Width(example)
	if exampleWidth > width {
		example = line(opRowNormal.text, "deploy  yaml  →  kubectl apply -f {$fpath} -n {$1}")
	} else {
		example += opRowNormal.text.Render(strings.Repeat(" ", width-exampleWidth))
	}
	return []string{
		line(opRowNormal.text, "No op-commands yet."),
		line(panelSubtitleStyle, "Save a shell command under a short key and the file types it is for, for example:"),
		example,
		line(panelSubtitleStyle, "Press → then Enter on “+ New command” below to add one."),
	}
}

// opNewButton is the table's last row; it is the selectable row after the
// commands (opTable.index == len(opCommands)).
func (m Model) opNewButton(width int) string {
	label := " + New command "
	selected := m.opTable.focused && !m.opTable.editing && m.opTable.index >= len(m.opCommands)
	button := opButtonStyle.Render(label)
	hint := ""
	if selected {
		button = opButtonSelStyle.Render(label)
		hint = "  Enter to add"
	}
	rest := max(0, width-len([]rune(label)))
	return button + panelSubtitleStyle.Render(padTo(truncateRunes(hint, rest), rest))
}

func (m Model) opLegend(width int) []string {
	legend := panelLegend("Types & placeholders", "", []panelLegendEntry{
		{"go,rb  *", opRowNormal.fileType, "TYPE: the files Ctrl+R offers the command for (* = all)"},
		{"{$1} {$2} …", opRowNormal.arg, "arguments you type when running the command"},
		{"{$1 or x}", opRowNormal.arg, `default when the argument is left out ("x y" quoted, "" omits it)`},
		{"{$fpath}", opRowNormal.system, opcmd.SystemVarDetails[opcmd.SystemFilePath]},
		{"{$dpath}", opRowNormal.system, opcmd.SystemVarDetails[opcmd.SystemDirPath]},
	}, width)
	if m.opTable.editing {
		legend = append(legend, panelSubtitleStyle.Render(truncateRunes("  Tab next column · type for suggestions · ↑/↓ choose · Tab insert", width)))
	}
	return legend
}

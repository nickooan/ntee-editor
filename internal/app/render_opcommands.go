package app

import (
	"image/color"
	"regexp"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/nickooan/ntee-editor/internal/opcmd"
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
	text, key, marker, arg, system, invalid lipgloss.Style
}

func newOpRowStyles(background, textColor, keyColor color.Color) opRowStyles {
	base := lipgloss.NewStyle().Background(background)
	return opRowStyles{
		text:    base.Foreground(textColor),
		key:     base.Foreground(keyColor).Bold(true),
		marker:  base.Foreground(colAqua).Bold(true),
		arg:     base.Foreground(colOrange).Bold(true),
		system:  base.Foreground(colBlue).Bold(true),
		invalid: base.Foreground(colRed).Underline(true),
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

// opPlaceholderToken matches a placeholder, including an unfinished one
// (missing "}") so a half-typed reference shows as invalid.
var opPlaceholderToken = regexp.MustCompile(`\{\$[^{}\s]*\}?`)

// opTokenClasses colors a template per rune: {$n} args, system variables, and
// anything opcmd would reject.
func opTokenClasses(runes []rune) []opTokenClass {
	classes := make([]opTokenClass, len(runes))
	text := string(runes)
	for _, location := range opPlaceholderToken.FindAllStringIndex(text, -1) {
		token := text[location[0]:location[1]]
		class := opTokenInvalid
		if template, err := opcmd.Parse(token); err == nil && strings.HasSuffix(token, "}") {
			class = opTokenSystem
			if template.MaxArg > 0 {
				class = opTokenArg
			}
		}
		first := utf8.RuneCountInString(text[:location[0]])
		for index := first; index < first+utf8.RuneCountInString(token); index++ {
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

// renderOpCell renders text into exactly width cells. With cursor >= 0 it is
// an input: the view scrolls horizontally to keep the cursor visible.
// Otherwise overlong text ends in "…". asTemplate colors placeholders;
// without it the whole text uses the key style.
func renderOpCell(text string, width, cursor int, asTemplate bool, styles opRowStyles) string {
	runes := []rune(text)
	var classes []opTokenClass
	if asTemplate {
		classes = opTokenClasses(runes)
	}
	styleAt := func(index int) lipgloss.Style {
		if !asTemplate {
			return styles.key
		}
		return styles.forClass(classes[index])
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

// newOpTableLayout sizes the op-commands table: keys fit the longest name (or
// the key being typed), capped at a third of the width; a marker gutter shows
// the selected (▸) or edited (✎) row.
func (m Model) newOpTableLayout(width int) panelTable {
	keyWidth := len("KEY")
	for _, command := range m.opCommands {
		keyWidth = max(keyWidth, len([]rune(command.Name)))
	}
	if m.opTable.editing {
		keyWidth = max(keyWidth, len([]rune(m.opTable.key))+1)
	}
	return newPanelTable(width, min(keyWidth, max(6, width/3)), true)
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
		layout.header("KEY", "COMMAND"),
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

	if m.opTable.editing && m.opEditDuplicatesName() {
		rows = append(rows, "", opDeleteStyle.Render(truncateRunes(
			"“"+strings.TrimSpace(m.opTable.key)+"” already exists — choose a different key", width)))
	}
	if name := m.opTable.confirmDelete; name != "" {
		rows = append(rows, "", opDeleteStyle.Render("Delete “"+name+"”?")+panelSubtitleStyle.Render("  y delete · n keep"))
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
		body = append(body, layout.row(marker,
			renderOpCell(command.Name, layout.keyWidth, -1, false, styles),
			renderOpCell(command.Command, layout.valueWidth, -1, true, styles), styles.text, styles.marker))
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
	keyStyles, valueStyles := opRowInput, opRowEditing
	keyCursor, valueCursor := m.opTable.cursor, -1
	if m.opTable.field == opFieldValue {
		keyStyles, valueStyles = opRowEditing, opRowInput
		keyCursor, valueCursor = -1, m.opTable.cursor
	}
	if m.opEditDuplicatesName() {
		keyStyles.key = keyStyles.key.Foreground(colRed)
	}
	bar := panelBorderStyle.Render("│")
	gap := opRowEditing.text.Render(" ")
	return bar + gap + opRowEditing.key.Foreground(colYellow).Render("✎") + gap +
		renderOpCell(m.opTable.key, layout.keyWidth, keyCursor, false, keyStyles) + gap + bar + gap +
		renderOpCell(m.opTable.value, layout.valueWidth, valueCursor, true, valueStyles) + gap + bar
}

// renderOpSuggestionRows draws the placeholder completion menu as a dropdown
// in the value column, directly under the edit row.
func (m Model) renderOpSuggestionRows(layout panelTable) []string {
	_, candidates := m.opSuggestions()
	if len(candidates) == 0 {
		return nil
	}
	selected := min(m.opTable.suggestIndex, len(candidates)-1)
	insertWidth := 0
	for _, candidate := range candidates {
		insertWidth = max(insertWidth, len(candidate.Insert))
	}
	menuWidth := min(layout.valueWidth, insertWidth+36)
	rows := make([]string, 0, len(candidates))
	for index, candidate := range candidates {
		labelStyle, detailStyle := completionItemStyle, completionDetailStyle
		if index == selected {
			labelStyle, detailStyle = completionSelStyle, completionSelDetailStyle
		}
		label := " " + padTo(candidate.Insert, insertWidth) + "  "
		item := labelStyle.Render(truncateRunes(label, menuWidth))
		if rest := menuWidth - len([]rune(label)); rest > 0 {
			item += detailStyle.Render(padTo(truncateRunes(candidate.Detail, rest), rest))
		}
		valueCell := item + opRowNormal.text.Render(strings.Repeat(" ", max(0, layout.valueWidth-menuWidth)))
		rows = append(rows, layout.row(" ", opRowNormal.text.Render(strings.Repeat(" ", layout.keyWidth)), valueCell,
			opRowNormal.text, opRowNormal.marker))
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
	example := opRowNormal.key.Render("deploy") + opRowNormal.text.Render("  →  kubectl apply -f ") +
		opRowNormal.system.Render("{$fpath}") + opRowNormal.text.Render(" -n ") + opRowNormal.arg.Render("{$1}")
	exampleWidth := lipgloss.Width(example)
	if exampleWidth > width {
		example = line(opRowNormal.text, "deploy  →  kubectl apply -f {$fpath} -n {$1}")
	} else {
		example += opRowNormal.text.Render(strings.Repeat(" ", width-exampleWidth))
	}
	return []string{
		line(opRowNormal.text, "No op-commands yet."),
		line(panelSubtitleStyle, "Save a shell command under a short key, for example:"),
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
	legend := panelLegend("Placeholders", "", []panelLegendEntry{
		{"{$1} {$2} …", opRowNormal.arg, "arguments you type when running the command"},
		{"{$fpath}", opRowNormal.system, opcmd.SystemVarDetails[opcmd.SystemFilePath]},
		{"{$dpath}", opRowNormal.system, opcmd.SystemVarDetails[opcmd.SystemDirPath]},
	}, width)
	if m.opTable.editing {
		legend = append(legend, panelSubtitleStyle.Render(truncateRunes("  type { or $ in the command for suggestions · ↑/↓ choose · Tab insert", width)))
	}
	return legend
}

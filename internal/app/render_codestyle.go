package app

import (
	"strconv"
	"strings"

	"github.com/nickooan/ntee-editor/internal/config"
)

// renderInspectCodeStyle draws the code-style panel like the keybinds one: a
// LANGUAGE | TAB SIZE | DESCRIPTION table — the editor-wide row first, then
// one row per language — windowed around the selection and closed by the
// "reset all" row. A size that differs from what it would inherit is
// highlighted and names what it replaces.
func (m Model) renderInspectCodeStyle(width, height int) string {
	width = panelInnerWidth(width)
	picker := m.codeStylePicker

	const editPrompt = "type 1–16…"
	nameWidth := len("all files")
	for _, language := range config.CodeStyleLanguages {
		nameWidth = max(nameWidth, len(language.Key))
	}
	sizeWidth := max(len("TAB SIZE"), len([]rune(editPrompt)), len("4 · all files"))
	table := newPanelTable(width, min(nameWidth, max(6, width/3)), min(sizeWidth, 16), true)
	contentWidth := table.contentWidth()

	rows := panelHeading("code style", "Tab size per language — Tab inserts that many spaces, and tabs in files draw that wide. Saved to your config.yaml.", width)
	if !picker.focused {
		rows[0] += panelSubtitleStyle.Render("  ·  ") + opFocusHintStyle.Render("press → to edit")
	}
	rows = append(rows, "",
		table.border("╭", "┬", "╮"),
		table.header("LANGUAGE", "TAB SIZE", "DESCRIPTION"),
		table.border("├", "┼", "┤"),
	)

	global := m.cfg.Editor.TabWidth
	body := make([]string, 0, len(config.CodeStyleLanguages)+1)
	for index := 0; index <= len(config.CodeStyleLanguages); index++ {
		language := codeStyleRowLanguage(index)
		selected := picker.focused && index == picker.index
		styles, marker := opRowNormal, " "
		if selected {
			styles, marker = opRowSelected, "▸"
		}

		var name, sizeText, description string
		sizeStyle := styles.dim
		if language == "" {
			name, sizeText = "all files", strconv.Itoa(global)
			description = styles.dim.Render("every file without its own size · Tab inserts " + strconv.Itoa(global) + " spaces")
			sizeStyle = styles.text
			if global != config.DefaultTabWidth {
				sizeStyle = styles.key
				description = styles.key.Render("default "+strconv.Itoa(config.DefaultTabWidth)) + styles.dim.Render(" · ") + description
			}
		} else {
			entry := config.CodeStyleLanguages[index-1]
			name = entry.Key
			files := strings.Join(append(append([]string{}, entry.Extensions...), entry.Basenames...), " ")
			description = styles.dim.Render(entry.Label + " · " + files)
			if size, custom := m.cfg.Editor.LanguageTabWidths[language]; custom {
				sizeStyle, sizeText = styles.key, strconv.Itoa(size)
				description = styles.key.Render("instead of "+strconv.Itoa(global)) + styles.dim.Render(" · ") + description
			} else {
				sizeText = strconv.Itoa(global) + " · all files"
			}
		}
		if m.codeStyleProjectRefusal(language) != "" {
			description = styles.fileType.Render("project") + styles.dim.Render(" · ") + description
		}
		if selected && picker.editing {
			sizeStyle, sizeText = styles.key, picker.input+"▏"
			if picker.input == "" {
				sizeText = editPrompt
			}
		}
		body = append(body, table.row(marker, []string{
			panelCellFill(styles.text.Render(name), table.keyWidth, styles.text),
			panelCellFill(sizeStyle.Render(sizeText), table.middleWidth, styles.text),
			panelCellFill(description, table.valueWidth, styles.text),
		}, styles.text, styles.marker))
	}

	legend := panelLegend("Commands", "type them in the @inspection > bar", []panelLegendEntry{
		{"tab <size>", panelCommandStyle, "set the size for all files, e.g. tab 2"},
		{"tab <language> <size>", panelCommandStyle, "give one language its own size, e.g. tab go 8"},
		{"tab <language> default", panelCommandStyle, "let a language use all files' size again"},
		{"tab reset", panelCommandStyle, "restore every default"},
	}, width)
	fixedBelow := 3
	visible := height - len(rows) - fixedBelow - len(legend) - 1
	if visible < len(body) {
		legend = nil
		visible = max(3, height-len(rows)-fixedBelow)
	}
	start := sidebarWindowStart(len(body), visible, min(picker.index, len(body)-1))
	rows = append(rows, body[start:min(len(body), start+visible)]...)

	rows = append(rows, table.border("├", "┴", "┤"), table.spanning(m.renderCodeStyleResetRow(contentWidth)), table.border("╰", "", "╯"))
	if len(legend) > 0 {
		rows = append(rows, "")
		rows = append(rows, legend...)
	}
	return panelIndent(rows)
}

// renderCodeStyleResetRow is the table's closing "reset all" row, which turns
// into the y/n prompt while the reset is being confirmed.
func (m Model) renderCodeStyleResetRow(width int) string {
	picker := m.codeStylePicker
	selected := picker.focused && picker.index == codeStyleResetRow
	switch {
	case picker.confirmReset:
		return panelCellFill(opDeleteStyle.Render("Reset all "+strconv.Itoa(m.codeStyleCustomCount())+" custom tab sizes?")+
			panelSubtitleStyle.Render("  y reset · n keep"), width, baseStyle)
	case selected:
		return panelCellFill(selectedEntryStyle.Render("▸ ↺ reset all to defaults"), width, selectedEntryStyle)
	}
	return panelCellFill(panelDimStyle.Render("  ↺ reset all to defaults"), width, baseStyle)
}

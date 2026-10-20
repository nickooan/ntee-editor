package app

import (
	"strconv"

	"github.com/nickooan/ntee-editor/internal/keymap"
)

// renderInspectKeybinds draws the keybinds panel: an ACTION | KEY |
// DESCRIPTION table (one line per action, windowed around the selection so
// short terminals still show it) closed by the "reset all" row. A custom
// binding is highlighted and names its default.
func (m Model) renderInspectKeybinds(width, height int) string {
	width = panelInnerWidth(width)
	picker := m.keybindPicker
	actions := keymap.Actions()

	const capturePrompt = "press a key…"
	actionWidth := len("ACTION")
	keyWidth := len([]rune(capturePrompt))
	for _, action := range actions {
		actionWidth = max(actionWidth, len(action.Name()))
		keyWidth = max(keyWidth, len(m.keys.Label(action)))
	}
	table := newPanelTable(width, min(actionWidth, max(6, width/3)), min(keyWidth, 16), true)
	contentWidth := table.contentWidth()

	rows := panelHeading("keybinds", "Key chords for editor actions — saved to your config.yaml.", width)
	if !picker.focused {
		rows[0] += panelSubtitleStyle.Render("  ·  ") + opFocusHintStyle.Render("press → to rebind")
	}
	rows = append(rows, "",
		table.border("╭", "┬", "╮"),
		table.header("ACTION", "KEY", "DESCRIPTION"),
		table.border("├", "┼", "┤"),
	)

	body := make([]string, len(actions))
	for index, action := range actions {
		selected := picker.focused && index == picker.index
		styles, marker := opRowNormal, " "
		if selected {
			styles, marker = opRowSelected, "▸"
		}
		keyStyle, keyText := styles.dim, m.keys.Label(action)
		description := styles.dim.Render(action.Description())
		if m.keys.IsCustom(action) {
			// The default leads so a narrow column truncates the description,
			// not the way back.
			keyStyle = styles.key
			description = styles.key.Render("default "+keymap.Label(action.DefaultKey())) + styles.dim.Render(" · ") + description
		}
		if selected && picker.capturing {
			keyStyle, keyText = styles.key, capturePrompt
		}
		body[index] = table.row(marker, []string{
			panelCellFill(styles.text.Render(action.Name()), table.keyWidth, styles.text),
			panelCellFill(keyStyle.Render(keyText), table.middleWidth, styles.text),
			panelCellFill(description, table.valueWidth, styles.text),
		}, styles.text, styles.marker)
	}

	legend := panelLegend("Commands", "type them in the @inspection > bar", []panelLegendEntry{
		{"keybind <action> <key>", panelCommandStyle, "bind a ctrl/alt chord or F-key, e.g. keybind goto_file ctrl+n"},
		{"keybind <action> default", panelCommandStyle, "restore one action's default key"},
		{"keybind reset", panelCommandStyle, "restore every default"},
	}, width)
	// Fixed rows around the body: the borders and reset row below, then the
	// legend when there is room for it.
	fixedBelow := 3
	visible := height - len(rows) - fixedBelow - len(legend) - 1
	if visible < len(body) {
		legend = nil
		visible = max(3, height-len(rows)-fixedBelow)
	}
	start := sidebarWindowStart(len(body), visible, min(picker.index, len(body)-1))
	rows = append(rows, body[start:min(len(body), start+visible)]...)

	rows = append(rows, table.border("├", "┴", "┤"), table.spanning(m.renderKeybindResetRow(contentWidth)), table.border("╰", "", "╯"))
	if len(legend) > 0 {
		rows = append(rows, "")
		rows = append(rows, legend...)
	}
	return panelIndent(rows)
}

// renderKeybindResetRow is the table's closing "reset all" row, which turns into
// the y/n prompt while the reset is being confirmed.
func (m Model) renderKeybindResetRow(width int) string {
	picker := m.keybindPicker
	selected := picker.focused && picker.index == keybindResetRow
	switch {
	case picker.confirmReset:
		return panelCellFill(opDeleteStyle.Render("Reset all "+strconv.Itoa(m.keys.CustomCount())+" custom bindings?")+
			panelSubtitleStyle.Render("  y reset · n keep"), width, baseStyle)
	case selected:
		return panelCellFill(selectedEntryStyle.Render("▸ ↺ reset all to defaults"), width, selectedEntryStyle)
	}
	return panelCellFill(panelDimStyle.Render("  ↺ reset all to defaults"), width, baseStyle)
}

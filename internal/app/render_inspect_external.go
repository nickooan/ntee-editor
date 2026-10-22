package app

import (
	"fmt"
	"strings"

	"github.com/nickooan/ntee-editor/internal/store"
)

// renderInspectExternal draws the external-messages panel: the socket's
// status, the global on/off switch and history limit, and this project's
// saved-message count with the way into the history view.
func (m Model) renderInspectExternal(width, height int) string {
	picker := m.externalPicker
	panel := infoPanel{
		title:       "external",
		subtitle:    "Let other programs (coding agents, scripts) open files and send reviews to this editor.",
		keyHeader:   "SETTING",
		valueHeader: "VALUE",
		legend: []panelLegendEntry{
			{"ntee --msg open <file>#L<n>", panelCommandStyle, "open a file at a line"},
			{"ntee --msg review <files…>", panelCommandStyle, "review changed files (or pipe paths on stdin)"},
			{"ext on|off · ext limit <n>", panelCommandStyle, "the same switches from the @inspection > bar"},
		},
	}
	if !picker.focused {
		panel.titleHint = "press → to change"
	}

	socket := panelGoodStyle.Render("● listening") + panelDimStyle.Render("  "+m.external.socketPath)
	if m.external.socketProblem != "" {
		socket = panelBadStyle.Render("● not listening") + panelDimStyle.Render("  "+m.external.socketProblem)
	}

	accept, limit := panelDimStyle.Render("loading…"), panelDimStyle.Render("loading…")
	if m.external.settingsLoaded {
		accept = panelBadStyle.Render("off") + panelDimStyle.Render("  senders are told messages are disabled")
		if m.external.settings.Enabled {
			accept = panelGoodStyle.Render("on") + panelDimStyle.Render("  requests are accepted (and confirmed when you are busy)")
		}
		if m.onlyFile != "" {
			// This window never listens, whatever the global switch says.
			global := "off"
			if m.external.settings.Enabled {
				global = "on"
			}
			accept = panelDimStyle.Render("disabled") + panelDimStyle.Render("  single-file mode — the global switch is "+global+" for folder windows")
		}
		limit = panelStrongStyle.Render(fmt.Sprintf("%d", m.external.settings.HistoryLimit)) +
			panelDimStyle.Render(fmt.Sprintf("  latest messages kept per project (%d–%d)", store.MinExternalHistoryLimit, store.MaxExternalHistoryLimit))
	}
	if picker.editingLimit {
		limit = panelPickedStyle.Render(picker.limitInput+"▏") + panelPickedNoteStyle.Render("  Enter save · Esc cancel")
	}
	history := panelStrongStyle.Render(fmt.Sprintf("%d saved", m.external.savedCount)) + panelDimStyle.Render("  ▸ view history")

	rows := []panelRow{
		{label: "socket", pieces: []string{socket}},
		{label: "accept messages", pieces: []string{accept}},
		{label: "history limit", pieces: []string{limit}},
		{label: "this project", pieces: []string{history}},
	}
	if picker.focused && !picker.editingLimit {
		rows[1+picker.index].selectedPiece = 1
		rows[1+picker.index].label = "▸ " + strings.TrimSpace(rows[1+picker.index].label)
	}
	panel.rows = rows
	return panel.render(width, height)
}

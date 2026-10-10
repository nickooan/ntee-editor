package app

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/nickooan/ntee-editor/internal/lsp"
	"github.com/nickooan/ntee-editor/internal/store"
)

// renderInspectMain draws the right info pane for the selected menu item.
func (m Model) renderInspectMain(width, height int) string {
	switch m.inspectMenu {
	case inspectMenuLSP:
		return m.renderInspectLSP(width, height)
	case inspectMenuSystem:
		return m.renderInspectSystem(width, height)
	case inspectMenuOpCommands:
		return m.renderInspectOpCommands(width, height)
	default:
		return m.renderInspectDB(width, height)
	}
}

var (
	panelGoodStyle    = lipgloss.NewStyle().Foreground(colGreen).Background(colBg)
	panelWarnStyle    = lipgloss.NewStyle().Foreground(colYellow).Background(colBg)
	panelBadStyle     = lipgloss.NewStyle().Foreground(colRed).Background(colBg)
	panelDimStyle     = lipgloss.NewStyle().Foreground(colComment).Background(colBg)
	panelStrongStyle  = lipgloss.NewStyle().Bold(true).Foreground(colFg).Background(colBg)
	panelCurrentStyle = lipgloss.NewStyle().Bold(true).Foreground(colYellow).Background(colBg)
	// The highlighted line of a focused list, on the selection bar.
	panelPickedStyle     = lipgloss.NewStyle().Bold(true).Foreground(colFg).Background(colSelection)
	panelPickedNoteStyle = lipgloss.NewStyle().Foreground(colYellow).Background(colSelection)
)

const usageBarCells = 10

func (m Model) renderInspectSystem(width, height int) string {
	picker := m.stylePicker
	available := panelRow{label: "available", stacked: true}
	for index, name := range syntaxStyles {
		current := name == m.cfg.Theme.Syntax
		highlighted := picker.focused && index == picker.index
		marker, nameStyle, noteStyle := "  ", panelDimStyle, panelDimStyle
		switch {
		case highlighted:
			marker, nameStyle, noteStyle = "▸ ", panelPickedStyle, panelPickedNoteStyle
			available.selectedPiece = index + 1
		case current:
			nameStyle = panelCurrentStyle
		}
		if current && !highlighted {
			marker = "● "
		}
		piece := nameStyle.Render(marker + name)
		if current {
			piece += noteStyle.Render("  current")
		}
		available.pieces = append(available.pieces, piece)
	}
	panel := infoPanel{
		title:       "system",
		subtitle:    "Editor build and appearance.",
		keyHeader:   "SETTING",
		valueHeader: "VALUE",
		rows: []panelRow{
			{label: "version", pieces: []string{panelStrongStyle.Render(versionTag())}},
			{label: "color style", pieces: []string{panelCurrentStyle.Render(m.cfg.Theme.Syntax)}},
			available,
		},
		legend: []panelLegendEntry{
			{"syscolor <name>", panelCommandStyle, "switch the grammar colors (chrome stays gruvbox)"},
		},
	}
	if picker.focused {
		preview := panelRow{label: "preview", stacked: true}
		for index, line := range strings.Split(stylePreviewSample, "\n") {
			piece := panelLabelStyle.Render(line)
			if index < len(picker.preview) && picker.preview[index] != nil {
				piece = renderSegments(picker.preview[index], 0, len([]rune(line)))
			}
			preview.pieces = append(preview.pieces, piece)
		}
		panel.rows = append(panel.rows, preview)
	} else {
		panel.titleHint = "press → to choose a style"
	}
	return panel.render(width, height)
}

func (m Model) renderInspectDB(width, height int) string {
	panel := infoPanel{
		title:       "ntee-db",
		subtitle:    "This project's store: recent files, undo history, drafts and session.",
		keyHeader:   "METRIC",
		valueHeader: "VALUE",
		legend: []panelLegendEntry{
			{"db compact", panelCommandStyle, "drop dead records from the main log"},
			{"db relieve", panelCommandStyle, "also rewrite blobs, releasing orphaned ones"},
		},
	}
	switch {
	case m.inspectLoading:
		panel.message = []string{"Gathering store statistics…"}
		return panel.render(width, height)
	case errors.Is(m.inspectInfoErr, store.ErrNoStats):
		panel.message = []string{"In-memory store (persistence disabled) — no statistics."}
		return panel.render(width, height)
	}

	info := m.inspectInfo
	blobs := usageSummary(info.BlobTotalBytes, info.BlobLiveBytes, "orphaned")
	if m.inspectInfoErr != nil {
		blobs = []string{panelBadStyle.Render("blob scan failed: " + m.inspectInfoErr.Error())}
	}
	generations := panelStrongStyle.Render(fmt.Sprintf("%d", info.Generations))
	if info.Generations > 1 {
		generations += panelBadStyle.Render("  stray file — run db relieve")
	}
	panel.rows = []panelRow{
		{label: "records", pieces: []string{panelStrongStyle.Render(fmt.Sprintf("%d", info.Records))}},
		{label: "main log", pieces: usageSummary(info.MainBytes, info.LiveBytes, "dead")},
		{label: "blobs", pieces: blobs},
		{label: "generations", pieces: []string{generations}},
	}
	if m.inspectBusy != "" {
		panel.after = []string{panelWarnStyle.Render("db " + m.inspectBusy + " running…")}
	}
	return panel.render(width, height)
}

// usageSummary is "<total>  ██████░░░░" and "<live> live · <waste>
// <wasteLabel> (<pct>)" as two row pieces: the bar shows the live share, and
// the waste percentage turns yellow at 30% and red at 60% — the point where
// compacting pays off.
func usageSummary(total, live int64, wasteLabel string) []string {
	waste := max(0, total-live)
	filled := 0
	if total > 0 {
		filled = int((live*usageBarCells + total/2) / total)
	}
	wasteStyle := panelDimStyle
	switch share := percentValue(waste, total); {
	case share >= 60:
		wasteStyle = panelBadStyle
	case share >= 30:
		wasteStyle = panelWarnStyle
	}
	return []string{
		panelStrongStyle.Render(padTo(humanBytes(total), 8)) +
			panelGoodStyle.Render(strings.Repeat("█", filled)) +
			panelDimStyle.Render(strings.Repeat("░", usageBarCells-filled)),
		panelGoodStyle.Render(humanBytes(live)+" live") + panelDimStyle.Render(" · ") +
			wasteStyle.Render(fmt.Sprintf("%s %s (%s)", humanBytes(waste), wasteLabel, percent(waste, total))),
	}
}

func (m Model) renderInspectLSP(width, height int) string {
	panel := infoPanel{
		title:       "lsp",
		subtitle:    "Language servers start on demand when a matching file opens.",
		keyHeader:   "LANGUAGE",
		valueHeader: "STATUS",
		legend: []panelLegendEntry{
			{"lsp enable <lang|all>", panelCommandStyle, "start now and persist enable: true to your config"},
			{"lsp disable <lang|all>", panelCommandStyle, "stop and persist enable: false"},
		},
	}
	statuses := m.lsp.Statuses()
	if len(statuses) == 0 {
		panel.message = []string{"LSP is disabled globally (lsp.enabled: false). `lsp enable all` writes the config; restart ntee to apply."}
		return panel.render(width, height)
	}
	for _, status := range statuses {
		var state string
		switch status.State {
		case lsp.LangRunning:
			state = panelGoodStyle.Render("● running")
		case lsp.LangStopped:
			state = panelWarnStyle.Render("○ stopped")
		default:
			state = panelDimStyle.Render("⊘ disabled")
			if status.Reason != "" {
				state += panelDimStyle.Render(" — " + status.Reason)
			}
		}
		panel.rows = append(panel.rows, panelRow{label: status.Lang, pieces: []string{state}})
	}
	return panel.render(width, height)
}

// humanBytes formats a byte count for the inspection pane (B/KB/MB/GB).
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// percent renders part/total as "N%", guarding the empty store.
func percent(part, total int64) string {
	return fmt.Sprintf("%d%%", percentValue(part, total))
}

func percentValue(part, total int64) int64 {
	if total <= 0 {
		return 0
	}
	return part * 100 / total
}

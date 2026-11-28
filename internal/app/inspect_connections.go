package app

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/store"
)

// inspectConnectionsRefresh is how often the ntee-db pane redraws while a
// lease is open, so its idle and closes-in times count.
const inspectConnectionsRefresh = time.Second

type inspectConnectionsTickMsg struct{}

func inspectConnectionsTick() tea.Cmd {
	return tea.Tick(inspectConnectionsRefresh, func(time.Time) tea.Msg { return inspectConnectionsTickMsg{} })
}

// withInspectConnectionsTick arms the pane's refresh while the ntee-db pane
// is shown and a lease is open; each tick lands, clears the flag and comes
// back here, so the chain stops on its own once either is no longer true.
func withInspectConnectionsTick(model tea.Model, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	m, ok := model.(Model)
	if !ok || m.inspectConnectionsTicking || m.mode != modeInspect || m.inspectMenu != inspectMenuDB || !anyLeaseOpen(m.dbConnections()) {
		return model, cmd
	}
	m.inspectConnectionsTicking = true
	return m, tea.Batch(cmd, inspectConnectionsTick())
}

func anyLeaseOpen(statuses []store.ConnectionStatus) bool {
	for _, status := range statuses {
		if status.Open && status.Mode == store.ConnectionSessionLease {
			return true
		}
	}
	return false
}

// dbConnections snapshots every db connection the editor has. A store that
// keeps nothing on disk reports one row with no mode: in memory.
func (m Model) dbConnections() []store.ConnectionStatus {
	var statuses []store.ConnectionStatus
	add := func(source any, name string) {
		if reporter, ok := source.(store.ConnectionReporter); ok {
			if reported := reporter.Connections(); len(reported) > 0 {
				statuses = append(statuses, reported...)
				return
			}
		}
		statuses = append(statuses, store.ConnectionStatus{Name: name})
	}
	add(m.db, "project store")
	if m.agentEdits.registry != nil {
		add(m.agentEdits.registry.Log(), "agent edits")
	}
	if m.external.historyStore != nil {
		add(m.external.historyStore, "message history")
	}
	if m.external.settingsStore != nil {
		add(m.external.settingsStore, "global")
	}
	return statuses
}

func connectionsSection(statuses []store.ConnectionStatus, now time.Time) panelSection {
	section := panelSection{
		title:     "connections",
		note:      "agent dbs open per session and close when idle",
		keyHeader: "CONNECTION", valueHeader: "STATE",
	}
	for _, status := range statuses {
		section.rows = append(section.rows, panelRow{label: status.Name, stacked: true, pieces: connectionPieces(status, now)})
	}
	return section
}

var (
	connectionPerCallStyle = panelGoodStyle.Foreground(colBlue)
	connectionOpenStyle    = panelGoodStyle.Bold(true)
)

// connectionPieces are a connection's lines: its state with the times that
// matter for it, how much a lease saved, and its last error.
func connectionPieces(status store.ConnectionStatus, now time.Time) []string {
	dot := panelDimStyle.Render(" · ")
	fact := func(value, label string) string {
		return panelStrongStyle.Render(value) + panelDimStyle.Render(label)
	}
	var state []string
	switch {
	case status.Mode == "":
		return []string{panelDimStyle.Render("◌ in memory") + panelDimStyle.Render("  nothing kept on disk")}
	case status.Mode == store.ConnectionSessionLong:
		state = append(state, connectionOpenStyle.Render("● open"), fact("session-long", "")+dot+fact(compactDuration(now.Sub(status.OpenedAt)), " since start"))
	case status.Mode == store.ConnectionPerCall:
		state = append(state, connectionPerCallStyle.Render("◌ per call"), usedAgo(status, now))
	case status.Open:
		sessions := fact(fmt.Sprintf("%d", status.Sessions), plural(status.Sessions, " session", " sessions"))
		timing := fact(compactDuration(now.Sub(status.OpenedAt)), " open") + dot + fact(compactDuration(now.Sub(status.LastUsed)), " idle")
		if !status.ClosesAt.IsZero() {
			timing += dot + panelDimStyle.Render("closes in ") + panelWarnStyle.Render(compactDuration(max(0, status.ClosesAt.Sub(now))))
		}
		state = append(state, connectionOpenStyle.Render("● open"), sessions+dot+timing)
	default:
		state = append(state, panelDimStyle.Render("○ closed"), usedAgo(status, now))
	}
	pieces := []string{strings.Join(state, panelDimStyle.Render("  "))} // a bare gap would show the terminal background
	if status.Mode == store.ConnectionSessionLease && status.Calls > 0 {
		pieces = append(pieces, reuseLine(status))
	}
	if status.Note != "" {
		noteStyle := panelGoodStyle
		if strings.Contains(status.Note, "stale") {
			noteStyle = panelWarnStyle
		}
		pieces = append(pieces, panelDimStyle.Render("  ")+noteStyle.Render(status.Note)+dot+fact(fmt.Sprintf("%d", status.Opens), plural(status.Opens, " open", " opens")))
	}
	if status.LastError != "" {
		pieces = append(pieces, panelBadStyle.Render("  ✗ "+status.LastError))
	}
	return pieces
}

func usedAgo(status store.ConnectionStatus, now time.Time) string {
	if status.LastUsed.IsZero() {
		return panelDimStyle.Render("not used yet")
	}
	return panelDimStyle.Render("last used ") + panelStrongStyle.Render(compactDuration(now.Sub(status.LastUsed))) + panelDimStyle.Render(" ago")
}

// reuseLine shows the share of calls a lease served without opening: each
// of those skipped ntee-db's open (index rebuild) and close (hint, fsync).
func reuseLine(status store.ConnectionStatus) string {
	served := max(0, status.Calls-status.Opens)
	filled := 0
	if status.Calls > 0 {
		filled = served * usageBarCells / status.Calls // full only when every call reused it
	}
	reconnects := panelDimStyle.Render(fmt.Sprintf("%d reconnects", status.Reconnects))
	if status.Reconnects > 0 {
		reconnects = panelWarnStyle.Render(fmt.Sprintf("%d %s", status.Reconnects, plural(status.Reconnects, "reconnect", "reconnects")))
	}
	return panelDimStyle.Render("  reuse ") +
		panelGoodStyle.Render(strings.Repeat("█", filled)) + panelDimStyle.Render(strings.Repeat("░", usageBarCells-filled)) +
		panelDimStyle.Render("  ") + panelStrongStyle.Render(fmt.Sprintf("%d", status.Calls)) + panelDimStyle.Render(plural(status.Calls, " call", " calls")+" on ") +
		panelStrongStyle.Render(fmt.Sprintf("%d", status.Opens)) + panelDimStyle.Render(plural(status.Opens, " open", " opens")+" · ") + reconnects
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}

// compactDuration is a duration in its two largest units: 4s, 1m12s, 2h5m.
func compactDuration(duration time.Duration) string {
	duration = duration.Round(time.Second)
	switch {
	case duration < time.Minute:
		return fmt.Sprintf("%ds", int(duration.Seconds()))
	case duration < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(duration.Minutes()), int(duration.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(duration.Hours()), int(duration.Minutes())%60)
	}
}

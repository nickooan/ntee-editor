package app

import (
	"cmp"
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/store"
)

// renderInspectAgentEdits draws the agent edits panel: a settings table
// (what happens when an agent ends an edit session, how many finished
// sessions are kept) over the history table of sessions, pending ones first.
// A session's details page replaces it while open.
func (m Model) renderInspectAgentEdits(width, height int) string {
	picker := m.agentEditsPicker
	if picker.detail != nil && m.agentEdits.registry != nil {
		return m.renderAgentEditDetail(*picker.detail, width, height)
	}
	panel := infoPanel{
		title:       "agent edits",
		subtitle:    "Agents stage file edits here instead of writing the files; nothing changes on disk until the session is applied.",
		keyHeader:   "SETTING",
		valueHeader: "VALUE",
		legend: []panelLegendEntry{
			{"ntee --msg --msg-edit-session <id> replace <file>", panelCommandStyle, "stage an edit (see ntee --msg help)"},
			{"Enter on a session", panelCommandStyle, "its details: stages, changes and agents, and its review"},
			{"a · x on a pending session", panelCommandStyle, "apply · discard it (apply waits for every agent in it to end)"},
			{"f on a session agents still edit", panelCommandStyle, "stop waiting for them: review and apply what is staged"},
			{"r on a ready session", panelCommandStyle, "request changes: its agents ask you in their terminal, then edit it again"},
			{"edits review|apply · edits keep <n>", panelCommandStyle, "the same switches from the @inspection > bar"},
		},
	}
	if !picker.focused {
		panel.titleHint = "press → to change"
	}
	if m.agentEdits.registry == nil {
		panel.message = []string{"single-file mode — open a folder to take agent edits"}
		return panel.render(width, height)
	}

	// The selected row, if any: the settings rows first, then the sessions.
	selected := -1
	if picker.focused && !picker.editingKeep {
		selected = picker.index
	}
	settingPicked := func(row int) (strong, dim lipgloss.Style) {
		return panelPicked(panelStrongStyle, selected == row), panelPicked(panelDimStyle, selected == row)
	}
	policyStrong, policyDim := settingPicked(agentEditsRowPolicy)
	keepStrong, keepDim := settingPicked(agentEditsRowKeep)
	policy, keep := policyDim.Render("loading…"), keepDim.Render("loading…")
	if m.external.settingsLoaded {
		policy = policyStrong.Render("review first") + policyDim.Render("  you review the staged files, then apply or discard them")
		if m.external.settings.AgentEditPolicy == store.AgentEditPolicyApply {
			policy = policyStrong.Render("agent applies") + policyDim.Render("  every staged file is written at once when the agent ends")
		}
		keep = keepStrong.Render(fmt.Sprintf("%d", m.external.settings.AgentEditKeep)) +
			keepDim.Render(fmt.Sprintf("  finished sessions kept for tracing (%d–%d)", store.MinAgentEditKeep, store.MaxAgentEditKeep))
	}
	if picker.editingKeep {
		keep = panelPickedStyle.Render(picker.keepInput+"▏") + panelPickedNoteStyle.Render("  Enter save · Esc cancel")
	}
	panel.rows = []panelRow{
		{label: "when the agent ends", pieces: []string{policy}},
		{label: "keep sessions", pieces: []string{keep}},
	}

	sessionCount := len(m.agentEdits.pending) + len(m.agentEdits.finished)
	// Each session row takes two lines (its rule), under the heading, the
	// settings table and the history table's frame.
	visible := max(3, (height-agentEditsPanelChrome+1)/2)
	selectedSession := picker.index - agentEditsRowSessions
	first := input.Clamp(selectedSession-visible/2, 0, max(0, sessionCount-visible))
	var sessions []panelRow
	for index := first; index < min(sessionCount, first+visible); index++ {
		record, pending, _ := m.agentEditsSessionAt(index + agentEditsRowSessions)
		sessions = append(sessions, agentEditSessionRow(record, pending, selected == index+agentEditsRowSessions))
	}
	if selected >= 0 {
		rows, rowIndex := panel.rows, selected
		if selected >= agentEditsRowSessions {
			rows, rowIndex = sessions, selectedSession-first
		}
		if rowIndex >= 0 && rowIndex < len(rows) {
			rows[rowIndex].selected = true
			rows[rowIndex].label = "▸ " + strings.TrimSpace(rows[rowIndex].label)
		}
	}
	history := panelSection{title: "history", note: "pending first, then finished · Enter for details", keyHeader: "SESSION", valueHeader: "STATUS", rows: sessions}
	switch {
	case sessionCount == 0 && m.agentEdits.finishedLoaded:
		history.rows = []panelRow{{label: "", pieces: []string{panelDimStyle.Render("no agent edit sessions yet")}}}
	case sessionCount == 0:
		history.rows = []panelRow{{label: "", pieces: []string{panelDimStyle.Render("loading…")}}}
	case sessionCount > visible:
		panel.after = []string{panelDimStyle.Render(fmt.Sprintf("sessions %d–%d of %d", first+1, min(sessionCount, first+visible), sessionCount))}
	}
	panel.sections = []panelSection{history}
	return panel.render(width, height)
}

// agentEditsPanelChrome is the lines the agent edits panel takes besides
// its session rows: the heading, the settings table, the history title and
// frame, and the paging line.
const agentEditsPanelChrome = 18

// agentEditSessionRow is one session in the panel: its id, status, files,
// and when it started or ended; picked draws it on the selection bar.
func agentEditSessionRow(record store.AgentEditSession, pending, picked bool) panelRow {
	dim := panelPicked(panelDimStyle, picked)
	files := countText(len(record.Files), "file")
	if pending {
		state := agentEditPendingState(record)
		agents := record.WithAgents().Agents
		detail := "  " + files
		if len(agents) > 1 || record.Phase() != store.PhaseEditing {
			parts := make([]string, len(agents))
			for index, participant := range agents {
				parts[index] = cmp.Or(participant.Agent, "unnamed") + " " + agentState(participant)
			}
			detail += " · " + strings.Join(parts, ", ")
		}
		detail += " · started " + agentEditTime(record.StartedAt)
		return panelRow{label: record.Session, pieces: []string{
			panelPicked(panelCurrentStyle, picked).Render("● "+state) + dim.Render(detail),
		}}
	}
	style := panelGoodStyle
	switch record.Status {
	case store.AgentEditDiscarded:
		style = panelDimStyle
	case store.AgentEditPartlyApplied, store.AgentEditNotApplied:
		style = panelWarnStyle
	}
	detail := "  " + files + " · " + agentEditTime(record.EndedAt)
	if label := gitReportLabel(record); label != "" && record.Status != store.AgentEditDiscarded {
		detail += " · " + label
	}
	return panelRow{label: record.Session, pieces: []string{
		panelPicked(style, picked).Render(string(record.Status)) + dim.Render(detail),
	}}
}

// agentEditPendingState is where a pending session stands: staging, how
// many of its agents still edit or check, or ready to review.
func agentEditPendingState(record store.AgentEditSession) string {
	state := "staging"
	agents := record.WithAgents().Agents
	if editing := len(record.EditingAgents()); len(agents) > 1 && editing > 0 {
		state = fmt.Sprintf("%d of %d agents editing", editing, len(agents))
	}
	switch record.Phase() {
	case store.PhaseFinalising:
		state = fmt.Sprintf("checking: %d of %d in", len(agents)-len(record.CheckingAgents()), len(agents))
	case store.PhaseReady:
		state = "ready to review"
	}
	if _, open := record.OpenChangeRequest(); open {
		state = "changes requested — " + state
	}
	return state
}

// agentEditTime is a session time: the clock today, the date before.
func agentEditTime(at time.Time) string {
	at = at.Local()
	now := time.Now()
	if at.Year() == now.Year() && at.YearDay() == now.YearDay() {
		return at.Format("15:04")
	}
	return at.Format("Jan 2 15:04")
}

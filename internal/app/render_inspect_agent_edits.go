package app

import (
	"cmp"
	"fmt"
	"strings"
	"time"

	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/store"
)

// renderInspectAgentEdits draws the agent edits panel: what happens when an
// agent ends an edit session, how many finished sessions are kept, and the
// sessions — pending ones to review, finished ones to trace back.
func (m Model) renderInspectAgentEdits(width, height int) string {
	picker := m.agentEditsPicker
	panel := infoPanel{
		title:       "agent edits",
		subtitle:    "Agents stage file edits here instead of writing the files; nothing changes on disk until the session is applied.",
		keyHeader:   "SETTING",
		valueHeader: "VALUE",
		legend: []panelLegendEntry{
			{"ntee --msg --msg-edit-session <id> replace <file>", panelCommandStyle, "stage an edit (see ntee --msg help)"},
			{"Enter on a session", panelCommandStyle, "review a pending one · trace a finished one"},
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

	policy, keep := panelDimStyle.Render("loading…"), panelDimStyle.Render("loading…")
	if m.external.settingsLoaded {
		policy = panelStrongStyle.Render("review first") + panelDimStyle.Render("  you review the staged files, then apply or discard them")
		if m.external.settings.AgentEditPolicy == store.AgentEditPolicyApply {
			policy = panelStrongStyle.Render("agent applies") + panelDimStyle.Render("  every staged file is written at once when the agent ends")
		}
		keep = panelStrongStyle.Render(fmt.Sprintf("%d", m.external.settings.AgentEditKeep)) +
			panelDimStyle.Render(fmt.Sprintf("  finished sessions kept for tracing (%d–%d)", store.MinAgentEditKeep, store.MaxAgentEditKeep))
	}
	if picker.editingKeep {
		keep = panelPickedStyle.Render(picker.keepInput+"▏") + panelPickedNoteStyle.Render("  Enter save · Esc cancel")
	}
	rows := []panelRow{
		{label: "when the agent ends", pieces: []string{policy}},
		{label: "keep sessions", pieces: []string{keep}},
	}

	sessionCount := len(m.agentEdits.pending) + len(m.agentEdits.finished)
	visible := max(3, height-16)
	selectedSession := picker.index - agentEditsRowSessions
	first := input.Clamp(selectedSession-visible/2, 0, max(0, sessionCount-visible))
	for index := first; index < min(sessionCount, first+visible); index++ {
		record, pending, _ := m.agentEditsSessionAt(index + agentEditsRowSessions)
		rows = append(rows, agentEditSessionRow(record, pending))
	}
	if picker.focused && !picker.editingKeep {
		rowIndex := picker.index
		if picker.index >= agentEditsRowSessions {
			rowIndex = agentEditsRowSessions + picker.index - agentEditsRowSessions - first
		}
		if rowIndex >= 0 && rowIndex < len(rows) {
			rows[rowIndex].selectedPiece = 1
			rows[rowIndex].label = "▸ " + strings.TrimSpace(rows[rowIndex].label)
		}
	}
	panel.rows = rows
	switch {
	case sessionCount == 0 && m.agentEdits.finishedLoaded:
		panel.after = []string{panelDimStyle.Render("no agent edit sessions yet")}
	case sessionCount > visible:
		panel.after = []string{panelDimStyle.Render(fmt.Sprintf("sessions %d–%d of %d", first+1, min(sessionCount, first+visible), sessionCount))}
	}
	return panel.render(width, height)
}

// agentEditSessionRow is one session in the panel: its id, status, files,
// and when it started or ended.
func agentEditSessionRow(record store.AgentEditSession, pending bool) panelRow {
	files := countText(len(record.Files), "file")
	if pending {
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
			panelCurrentStyle.Render("● "+state) + panelDimStyle.Render(detail),
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
		style.Render(string(record.Status)) + panelDimStyle.Render(detail),
	}}
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

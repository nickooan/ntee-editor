package app

import (
	"cmp"
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/nickooan/ntee-editor/internal/store"
)

// renderAgentEditDetail draws a session's details page: the row that opens
// its review, then its status, stages, changes and follow-ups, and a table
// of its agents. It lists no file names: the review has those.
func (m Model) renderAgentEditDetail(detail agentEditDetailState, width, height int) string {
	record := detail.record
	panel := infoPanel{
		title:       "edit session " + record.Session,
		subtitle:    "What happened in this session, stage by stage, and who did what. Enter opens the code it changed.",
		titleHint:   "Esc back to the sessions",
		keyHeader:   "SESSION",
		valueHeader: "DETAIL",
		keyLegend:   true,
		legend: []panelLegendEntry{
			{"Enter", panelCommandStyle, "open the review: each file's diff and edit log"},
			{"Esc · ←", panelCommandStyle, "back to the sessions"},
		},
	}
	if detail.pending {
		panel.legend = append(panel.legend,
			panelLegendEntry{"a · x", panelCommandStyle, "apply · discard the session"},
			panelLegendEntry{"f · r", panelCommandStyle, "stop waiting for its agents · request changes"},
		)
	}
	switch {
	case detail.missing:
		panel.message = []string{"edit session " + record.Session + " is no longer in the log (the keep limit trimmed it)"}
		return panel.render(width, height)
	case detail.err != nil:
		panel.message = []string{"cannot read edit session " + record.Session + ": " + detail.err.Error()}
		return panel.render(width, height)
	}
	now := time.Now()
	panel.rows = []panelRow{
		agentEditReviewRow(detail),
		{label: "status", pieces: []string{agentEditStatusBadge(record, detail.pending)}},
		{label: "stages", pieces: agentEditStageLines(record, detail.pending, now), stacked: true},
		{label: "changes", pieces: agentEditChangePieces(detail)},
	}
	if builtOn := detail.facts.builtOn; len(builtOn) > 0 {
		panel.rows = append(panel.rows, panelRow{label: "built on", pieces: []string{
			panelLabelStyle.Render("edit session "+strings.Join(builtOn, ", ")) + panelDimStyle.Render("  its staged copy is under this one's"),
		}})
	}
	if !detail.pending {
		decided := "you reviewed it, then decided"
		if record.Policy == store.AgentEditPolicyApply {
			decided = "applied at once when the agent ended"
		}
		panel.rows = append(panel.rows, panelRow{label: "policy", pieces: []string{panelDimStyle.Render(decided)}})
	}
	if len(record.ChangeRequests) > 0 {
		panel.rows = append(panel.rows, panelRow{label: "changes asked", pieces: agentEditRequestLines(record.ChangeRequests), stacked: true})
	}
	if git := agentEditGitPieces(record); len(git) > 0 {
		panel.rows = append(panel.rows, panelRow{label: "git", pieces: git, stacked: true})
	}
	agents := record.WithAgents().Agents
	section := panelSection{title: "agents", note: countText(len(agents), "agent") + " · who did what", keyHeader: "AGENT", valueHeader: "ACTIVITY"}
	for _, participant := range agents {
		section.rows = append(section.rows, panelRow{
			label:   cmp.Or(participant.Agent, "unnamed"),
			pieces:  agentActivityLines(record, participant, detail, now),
			stacked: true,
		})
	}
	if len(section.rows) == 0 {
		section.rows = []panelRow{{pieces: []string{panelDimStyle.Render("no agent has edited yet")}}}
	}
	panel.sections = []panelSection{section}
	return panel.render(width, height)
}

// agentEditReviewRow is the page's one selectable row: it opens the review.
func agentEditReviewRow(detail agentEditDetailState) panelRow {
	action := "open the review (read-only trace)"
	if detail.pending {
		action = "open the live review"
	}
	facts := detail.facts
	summary := "  " + countText(facts.files, "file")
	if detail.whole {
		summary += " · " + countText(facts.edits, "edit")
	}
	piece := panelPickedStyle.Render("▶ "+action) + panelPickedNoteStyle.Render(summary)
	if !detail.pending && !detail.whole {
		piece = panelPickedStyle.Render("▶ "+action) + panelPickedNoteStyle.Render("  loading the session…")
	}
	return panelRow{label: "▸ review", pieces: []string{piece}, selectedPiece: 1}
}

// agentEditStatusBadge is where the session stands, in its color.
func agentEditStatusBadge(record store.AgentEditSession, pending bool) string {
	if pending {
		return panelCurrentStyle.Render("● " + agentEditPendingState(record))
	}
	mark, style := "✔", panelGoodStyle
	switch record.Status {
	case store.AgentEditPartlyApplied:
		mark, style = "◐", panelWarnStyle
	case store.AgentEditNotApplied:
		mark, style = "✗", panelWarnStyle
	case store.AgentEditDiscarded:
		mark, style = "⊘", panelDimStyle
	}
	badge := style.Bold(true).Render(mark + " " + string(record.Status))
	// The outcome names the files not applied after a ";": the review has
	// them. A plain apply's outcome only repeats the status.
	if outcome := strings.TrimSpace(strings.SplitN(record.Outcome, ";", 2)[0]); outcome != "" && record.Status != store.AgentEditApplied {
		badge += panelDimStyle.Render("  " + outcome)
	}
	return badge
}

// agentEditStage is one step of a session's life on the stages timeline.
type agentEditStage struct {
	label     string
	at, until time.Time // until is zero while the stage runs (or was never left)
}

// agentEditStageLines draws the timeline: ✓ a stage passed, ● the one the
// session is in, ○ one to come, – one a finished session skipped.
func agentEditStageLines(record store.AgentEditSession, pending bool, now time.Time) []string {
	ready := record.AgentEndedAt
	stages := []agentEditStage{{label: "editing", at: record.StartedAt, until: cmp.Or(record.AgentEndedAt, record.EndedAt)}}
	if record.Checks {
		stages = append(stages, agentEditStage{label: "checking", at: record.AgentEndedAt, until: cmp.Or(record.ReadyAt, record.EndedAt)})
		ready = record.ReadyAt
	}
	stages = append(stages, agentEditStage{label: "ready", at: ready, until: record.EndedAt})
	final := agentEditStage{label: "applied or discarded"}
	if !pending {
		final = agentEditStage{label: string(record.Status), at: record.EndedAt}
	}

	labelWidth := len("checking") + 2
	current := -1
	if pending {
		current = map[store.AgentEditPhase]int{store.PhaseEditing: 0, store.PhaseFinalising: 1, store.PhaseReady: len(stages) - 1}[record.Phase()]
	}
	lines := make([]string, 0, len(stages)+1)
	for index, stage := range stages {
		var line string
		switch {
		case index == current:
			line = panelCurrentStyle.Render("● "+padTo(stage.label, labelWidth)) + panelStrongStyle.Render(agentEditTime(stage.at)) +
				panelCurrentStyle.Render("  "+agentEditDuration(now.Sub(stage.at))+" so far")
		case stage.at.IsZero() && pending:
			line = panelDimStyle.Render("○ " + stage.label)
		case stage.at.IsZero():
			line = panelDimStyle.Render("– " + padTo(stage.label, labelWidth) + "skipped")
		default:
			line = panelGoodStyle.Render("✓ ") + panelLabelStyle.Render(padTo(stage.label, labelWidth)) + panelLabelStyle.Render(agentEditTime(stage.at))
			if !stage.until.IsZero() {
				line += panelDimStyle.Render("  " + agentEditDuration(stage.until.Sub(stage.at)))
			}
		}
		lines = append(lines, line)
	}
	if pending {
		return append(lines, panelDimStyle.Render("○ "+final.label))
	}
	mark, style := "✔ ", panelGoodStyle
	switch record.Status {
	case store.AgentEditPartlyApplied, store.AgentEditNotApplied:
		mark, style = "✗ ", panelWarnStyle
	case store.AgentEditDiscarded:
		mark, style = "⊘ ", panelDimStyle
	}
	line := style.Bold(true).Render(mark+padTo(final.label, labelWidth)) + panelLabelStyle.Render(agentEditTime(final.at))
	if !record.StartedAt.IsZero() {
		line += panelDimStyle.Render("  " + agentEditDuration(final.at.Sub(record.StartedAt)) + " in all")
	}
	return append(lines, line)
}

// agentEditChangePieces counts the session's files, edits and lines, and
// what applying did with the files.
func agentEditChangePieces(detail agentEditDetailState) []string {
	facts := detail.facts
	files := panelStrongStyle.Render(countText(facts.files, "file"))
	if facts.newFiles > 0 {
		files += panelDimStyle.Render(fmt.Sprintf(" (%d new)", facts.newFiles))
	}
	pieces := []string{files}
	if detail.whole || facts.edits > 0 {
		pieces = append(pieces, panelLabelStyle.Render(countText(facts.edits, "edit")), lineCountPieces(facts.added, facts.removed))
	}
	for _, result := range []struct {
		result store.AgentEditFileResult
		style  lipgloss.Style
	}{{store.AgentEditFileApplied, panelGoodStyle}, {store.AgentEditFileConflicted, panelWarnStyle}, {store.AgentEditFileSkipped, panelWarnStyle}} {
		if count := facts.results[result.result]; count > 0 && (!detail.pending || result.result == store.AgentEditFileApplied) {
			pieces = append(pieces, result.style.Render(fmt.Sprintf("%d %s", count, result.result)))
		}
	}
	return pieces
}

// lineCountPieces is "+12 −3" in the diff colors.
func lineCountPieces(added, removed int) string {
	return panelGoodStyle.Render(fmt.Sprintf("+%d", added)) + panelDimStyle.Render(" ") + panelBadStyle.Render(fmt.Sprintf("−%d", removed))
}

// agentEditRequestLines lists the user's change requests, oldest first.
func agentEditRequestLines(requests []store.AgentEditChangeRequest) []string {
	lines := make([]string, len(requests))
	for index, request := range requests {
		if request.AnsweredAt.IsZero() {
			lines[index] = panelCurrentStyle.Render("● asked "+agentEditTime(request.At)) + panelDimStyle.Render("  waiting for an agent to answer")
			continue
		}
		lines[index] = panelGoodStyle.Render("✓ ") + panelLabelStyle.Render("asked "+agentEditTime(request.At)) +
			panelDimStyle.Render(fmt.Sprintf("  answered by %s %s later", cmp.Or(request.AnsweredBy, "an unnamed agent"), agentEditDuration(request.AnsweredAt.Sub(request.At))))
	}
	return lines
}

// agentEditGitPieces is the git work asked with the apply, and the agent's
// word on it; nothing when none was asked.
func agentEditGitPieces(record store.AgentEditSession) []string {
	label := gitReportLabel(record)
	if label == "" {
		return nil
	}
	style := panelCurrentStyle
	if report := record.GitReport; report != nil {
		style = map[store.AgentEditGitReportResult]lipgloss.Style{store.GitReportDone: panelGoodStyle, store.GitReportBlocked: panelWarnStyle, store.GitReportDeclined: panelDimStyle}[report.Result]
	}
	pieces := []string{style.Render(label)}
	if report := record.GitReport; report != nil {
		if note := strings.TrimSpace(strings.SplitN(report.Note, "\n", 2)[0]); note != "" {
			pieces = append(pieces, panelDimStyle.Render(note))
		}
	}
	for repo, branch := range record.GitBranches {
		pieces = append(pieces, panelDimStyle.Render(cmp.Or(repo, "the project")+" on "+branch))
	}
	return pieces
}

// agentActivityLines is one agent's row: its state and time in the
// session, its share of the edits, and its check.
func agentActivityLines(record store.AgentEditSession, participant store.AgentEditParticipant, detail agentEditDetailState, now time.Time) []string {
	span := "  joined " + agentEditTime(participant.JoinedAt)
	var state string
	switch {
	case participant.Stopped:
		state = panelWarnStyle.Render("⊘ stopped by you")
	case participant.Editing() && detail.pending:
		state = panelCurrentStyle.Render("● editing")
		span += " · " + agentEditDuration(now.Sub(participant.JoinedAt)) + " so far"
	case participant.Editing():
		state = panelDimStyle.Render("– never ended")
	default:
		state = panelGoodStyle.Render("✓ ended")
		span += " · ended " + agentEditTime(participant.EndedAt) + " · " + agentEditDuration(participant.EndedAt.Sub(participant.JoinedAt))
	}
	lines := []string{state + panelDimStyle.Render(span)}

	switch share, edited := detail.facts.agents[participant.Agent]; {
	case edited:
		line := panelLabelStyle.Render(countText(share.edits, "edit")+" in "+countText(share.files, "file")) + panelDimStyle.Render("  ") + lineCountPieces(share.added, share.removed)
		if !share.lastEditAt.IsZero() {
			line += panelDimStyle.Render("  · last edit " + agentEditTime(share.lastEditAt))
		}
		lines = append(lines, line)
	case detail.whole:
		lines = append(lines, panelDimStyle.Render("no edits"))
	}

	switch {
	case participant.Check != "":
		mark, style := map[store.AgentEditCheckResult]string{store.CheckPassed: "✓", store.CheckFailed: "✗", store.CheckSkipped: "–"}[participant.Check], panelGoodStyle
		switch participant.Check {
		case store.CheckFailed:
			style = panelBadStyle
		case store.CheckSkipped:
			style = panelDimStyle
		}
		line := style.Render(mark + " check " + string(participant.Check))
		if note := strings.TrimSpace(strings.SplitN(participant.CheckNote, "\n", 2)[0]); note != "" {
			line += panelDimStyle.Render(" — " + note)
		}
		lines = append(lines, line)
	case record.Checks && detail.pending && record.Phase() == store.PhaseFinalising && !participant.Stopped:
		lines = append(lines, panelCurrentStyle.Render("◌ checking…"))
	}
	return lines
}

// agentEditDuration is a span the way the page shows it: 42s, 8m 12s,
// 2h 5m, 3d 4h.
func agentEditDuration(span time.Duration) string {
	span = max(0, span).Round(time.Second)
	switch {
	case span < time.Minute:
		return fmt.Sprintf("%ds", int(span.Seconds()))
	case span < time.Hour:
		return fmt.Sprintf("%dm %ds", int(span.Minutes()), int(span.Seconds())%60)
	case span < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(span.Hours()), int(span.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(span.Hours())/24, int(span.Hours())%24)
}

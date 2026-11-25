package app

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/staging"
	"github.com/nickooan/ntee-editor/internal/store"
)

// agentEditsReadyChoice offers to review a session the agent just ended.
func agentEditsReadyChoice(record store.AgentEditSession) choiceOverlay {
	banner, meaning := sessionKindBanner(extmsg.SessionEdit, "")
	choice := choiceOverlay{kind: choiceAgentEditsReady, title: banner + " " + record.Session, choices: []string{"Review edits", "Later"}}
	choice.body = append(choice.body, overlayLine{text: meaning, style: overlayLineHighlight})
	choice.body = append(choice.body, overlayLine{text: countText(len(record.Files), "file") + " staged", style: overlayLineStrong})
	choice.body = append(choice.body, agentEditFileLines(record)...)
	for _, line := range checkLines(record) {
		style := overlayLineStrong
		if strings.HasPrefix(line, "✗") {
			style = overlayLineHighlight
		}
		choice.body = append(choice.body, overlayLine{text: line, style: style})
	}
	choice.body = append(choice.body, overlayLine{text: "later: Ctrl+T › agent edits", style: overlayLineDim})
	return choice
}

// agentEditApplyChoice confirms applying a session, and asks what the agent
// should do in git afterwards: nothing, commit, or a new branch with a pull
// request. The git choices are offered only when every staged file is in a
// git repo the editor knows of. It returns the follow-up behind each choice
// but the last (Cancel).
func (m Model) agentEditApplyChoice(record store.AgentEditSession) (choiceOverlay, []store.AgentEditGitFollowUp) {
	choice := choiceOverlay{kind: choiceApplyAgentEdits, title: "Apply edit session " + record.Session + "?"}
	choice.body = append(choice.body, overlayLine{text: "write " + countText(len(record.Files), "file") + " to disk", style: overlayLineStrong})
	choice.body = append(choice.body, agentEditFileLines(record)...)
	for _, line := range checkLines(record) {
		choice.body = append(choice.body, overlayLine{text: line})
	}
	if checking := record.CheckingAgents(); record.Phase() == store.PhaseFinalising && len(checking) > 0 {
		choice.body = append(choice.body, overlayLine{text: "checks still running: " + staging.AgentNames(checking) + " — applying now skips them", style: overlayLineHighlight})
	}
	apply := "Apply"
	if stacked := m.stackedAgentEditLines(record); len(stacked) > 0 {
		choice.body = append(choice.body, stacked...)
		apply = "Apply both"
	}
	if unsaved := m.unsavedAgentEditFiles(record); len(unsaved) > 0 {
		what := "skipped"
		if m.agentEdits.applyReplaceUnsaved {
			what = "replaced"
		}
		choice.body = append(choice.body, overlayLine{text: "unsaved edits in " + strings.Join(unsaved, ", ") + " — " + what, style: overlayLineHighlight})
	}
	repos, inRepos := m.agentEditRepos(record)
	if !inRepos {
		choice.choices = []string{apply, "Cancel"}
		return choice, []store.AgentEditGitFollowUp{store.GitFollowUpNone}
	}
	choice.body = append(choice.body,
		overlayLine{text: "then the agent: commits in " + strings.Join(repos, ", ") + ", or opens a PR on a new branch (it asks you the name) — Apply only leaves git alone", style: overlayLineDim})
	choice.choices = []string{apply + " only", apply + " & commit", apply + " & new PR", "Cancel"}
	return choice, []store.AgentEditGitFollowUp{store.GitFollowUpNone, store.GitFollowUpCommit, store.GitFollowUpPullRequest}
}

// agentEditUnsavedChoice asks, before the apply popup, what becomes of
// unsaved work in files the session changes: replace it (undo brings it back
// in the open buffer) or skip those files.
func agentEditUnsavedChoice(record store.AgentEditSession, unsaved []string) choiceOverlay {
	choice := choiceOverlay{kind: choiceAgentEditsUnsaved, title: "Apply edit session " + record.Session + "?", choices: []string{"Replace unsaved", "Skip those files", "Cancel"}}
	choice.body = append(choice.body,
		overlayLine{text: "unsaved edits in " + strings.Join(unsaved, ", "), style: overlayLineHighlight},
		overlayLine{text: "replace them with the staged content (undo brings an open buffer's back), or apply the other files only", style: overlayLineDim},
	)
	return choice
}

// agentEditRepos names the git repos a session's files are in, by
// workspace path ("." = the workspace itself); inRepos is false when some
// file is in none. It reads only what the editor already knows: no disk.
func (m Model) agentEditRepos(record store.AgentEditSession) (repos []string, inRepos bool) {
	for _, file := range record.Files {
		name, _, ok := m.agentEditRepoOf(file.Rel)
		if !ok {
			return nil, false
		}
		if !slices.Contains(repos, name) {
			repos = append(repos, name)
		}
	}
	return repos, len(repos) > 0
}

// agentEditRepoOf is the git repo a workspace file is in: its name as
// agentEditRepos gives it, and its folder on disk.
func (m Model) agentEditRepoOf(rel string) (name, folder string, ok bool) {
	repo := "."
	if !m.workspaceIsRepo {
		if repo = m.deepestRepoFor(rel); repo == "" {
			return "", "", false
		}
	}
	name = repo
	if repo == "." {
		name = filepath.Base(m.workspaceRoot)
	}
	return name, filepath.Join(m.workspaceRoot, repo), true
}

// pendingBaseSessions lists the pending sessions the record's files build
// on: applying it writes their edits to those files too.
func (m Model) pendingBaseSessions(record store.AgentEditSession) []string {
	var bases []string
	for _, file := range record.Files {
		if file.Result != "" {
			continue
		}
		if base := m.agentEdits.registry.PendingBase(file); base != "" && !slices.Contains(bases, base) {
			bases = append(bases, base)
		}
	}
	return bases
}

// stackedAgentEditLines warns, per file, that applying writes the edits of
// the pending session it builds on too.
func (m Model) stackedAgentEditLines(record store.AgentEditSession) []overlayLine {
	var lines []overlayLine
	for _, file := range record.Files {
		if file.Result != "" {
			continue
		}
		if base := m.agentEdits.registry.PendingBase(file); base != "" {
			lines = append(lines, overlayLine{text: file.Rel + " builds on edit session " + base + "'s edits, which aren't applied yet — applying writes them to it too", style: overlayLineHighlight})
		}
	}
	return lines
}

// agentEditDiscardChoice confirms dropping a session's staged edits.
func agentEditDiscardChoice(record store.AgentEditSession) choiceOverlay {
	choice := choiceOverlay{kind: choiceDiscardAgentEdits, title: "Discard edit session " + record.Session + "?", choices: []string{"Discard", "Cancel"}}
	choice.body = append(choice.body, overlayLine{text: countText(len(record.Files), "staged file") + " — nothing will be written", style: overlayLineStrong})
	choice.body = append(choice.body, overlayLine{text: "the session stays in Ctrl+T › agent edits for tracing", style: overlayLineDim})
	return choice
}

// agentEditFileLines lists a session's files for an overlay, a few at most.
func agentEditFileLines(record store.AgentEditSession) []overlayLine {
	const shown = 6
	lines := make([]overlayLine, 0, shown+1)
	for _, file := range record.Files[:min(len(record.Files), shown)] {
		mark := "M "
		if !file.BaseExists {
			mark = "A "
		}
		lines = append(lines, overlayLine{text: mark + file.Rel})
	}
	if len(record.Files) > shown {
		lines = append(lines, overlayLine{text: "… and " + strconv.Itoa(len(record.Files)-shown) + " more", style: overlayLineDim})
	}
	return lines
}

// resolveAgentEditsChoice acts on an agent edits overlay's answer.
func (m Model) resolveAgentEditsChoice(kind choiceOverlayKind, chosen int, choiceCount int) (Model, tea.Cmd) {
	id := m.agentEdits.choiceSession
	m.agentEdits.choiceSession = ""
	record, ok := m.agentEdits.registry.Snapshot(id)
	if !ok {
		return m, nil // it finished while the overlay was up
	}
	switch kind {
	case choiceAgentEditsReady:
		if chosen == 0 {
			return m.enterAgentEditReview(record, false)
		}
		m = m.deferAgentEdits(id)
		m.notice = "agent edits " + id + " wait under Ctrl+T › agent edits"
	case choiceLeaveAgentEditReview:
		switch chosen {
		case 0:
			return m.askApplyAgentEdits(id), nil
		case 1:
			return m.startAgentEditDiscard(id)
		case 2:
			return m.startAgentEditRequestChanges(id)
		}
		m = m.exitReview().deferAgentEdits(id)
		m.notice = "agent edits " + id + " kept for later — reopen them under Ctrl+T › agent edits"
	case choiceAgentEditsUnsaved:
		if chosen >= 2 {
			m.notice = "apply cancelled"
			return m, nil
		}
		m.agentEdits.applyReplaceUnsaved = chosen == 0
		m.agentEdits.choiceSession = id
		m.choice, m.agentEdits.applyFollowUps = m.agentEditApplyChoice(record)
	case choiceApplyAgentEdits:
		followUps := m.agentEdits.applyFollowUps
		m.agentEdits.applyFollowUps = nil
		if chosen < 0 || chosen >= len(followUps) {
			m.notice = "apply cancelled"
			return m, nil
		}
		return m.startAgentEditApply(id, store.AgentEditPolicyReview, m.agentEdits.applyReplaceUnsaved, followUps[chosen], m.agentEditKeep(), nil)
	case choiceDiscardAgentEdits:
		if chosen == 0 {
			return m.startAgentEditDiscard(id)
		}
		m.notice = "discard cancelled"
	case choiceForceEndAgentEdits:
		if chosen == 0 {
			return m.startAgentEditForceEnd(id)
		}
		m.notice = "still waiting for the agents"
	}
	return m, nil
}

// askApplyAgentEdits opens the apply confirm for a pending session — once
// every agent in it has ended.
func (m Model) askApplyAgentEdits(id string) Model {
	record, ok := m.agentEdits.registry.Snapshot(id)
	if !ok {
		m.errText = "no edit session " + id + " is pending"
		return m
	}
	if record.AgentEndedAt.IsZero() {
		m.errText = agentsStillEditingText(record)
		return m
	}
	m.agentEdits.choiceSession = id
	m.agentEdits.applyReplaceUnsaved = false
	if unsaved := m.unsavedAgentEditFiles(record); len(unsaved) > 0 {
		m.choice = agentEditUnsavedChoice(record, unsaved)
		return m
	}
	m.choice, m.agentEdits.applyFollowUps = m.agentEditApplyChoice(record)
	return m
}

// agentsStillEditingText is why a session cannot be applied yet.
func agentsStillEditingText(record store.AgentEditSession) string {
	return "agents are still editing edit session " + record.Session + " (" + staging.AgentNames(record.WithAgents().EditingAgents()) +
		") — apply once they end, or stop waiting with f under Ctrl+T › agent edits"
}

// askForceEndAgentEdits confirms no longer waiting for a session's agents.
func (m Model) askForceEndAgentEdits(id string) Model {
	record, ok := m.agentEdits.registry.Snapshot(id)
	switch {
	case !ok:
		m.errText = "no edit session " + id + " is pending"
		return m
	case record.Phase() == store.PhaseReady:
		m.notice = "edit session " + id + " is ready — a applies it"
		return m
	}
	m.agentEdits.choiceSession = id
	m.choice = choiceOverlay{kind: choiceForceEndAgentEdits, title: "Stop waiting for edit session " + id + "?", choices: []string{"Stop waiting", "Cancel"}}
	if editing := record.WithAgents().EditingAgents(); len(editing) > 0 {
		m.choice.body = append(m.choice.body,
			overlayLine{text: "still editing: " + staging.AgentNames(editing), style: overlayLineStrong},
			overlayLine{text: "their further edits are refused; you then review and apply what is staged", style: overlayLineHighlight},
		)
		return m
	}
	m.choice.body = append(m.choice.body,
		overlayLine{text: "still checking: " + staging.AgentNames(record.CheckingAgents()), style: overlayLineStrong},
		overlayLine{text: "their checks are skipped; you then review and apply what is staged", style: overlayLineHighlight},
	)
	return m
}

// askLeaveAgentEditReview is Esc on a staged review: the user decides
// before leaving — apply, discard, or keep the edits staged for later.
func (m Model) askLeaveAgentEditReview(id string) Model {
	record, ok := m.agentEdits.registry.Snapshot(id)
	if !ok {
		return m.exitReview()
	}
	m.agentEdits.choiceSession = id
	m.choice = agentEditLeaveChoice(record)
	return m
}

func agentEditLeaveChoice(record store.AgentEditSession) choiceOverlay {
	choice := choiceOverlay{kind: choiceLeaveAgentEditReview, title: "Edit session " + record.Session + " — what now?", choices: []string{"Apply all", "Discard all", "Request changes", "Keep for later"}}
	choice.body = append(choice.body, overlayLine{text: countText(len(record.Files), "file") + " staged — nothing is written yet", style: overlayLineStrong})
	choice.body = append(choice.body, agentEditFileLines(record)...)
	choice.body = append(choice.body, overlayLine{text: "request changes: the agent asks you in its terminal", style: overlayLineDim})
	choice.body = append(choice.body, overlayLine{text: "later: reopen under Ctrl+T › agent edits", style: overlayLineDim})
	return choice
}

// askDiscardAgentEdits opens the discard confirm for a pending session.
func (m Model) askDiscardAgentEdits(id string) Model {
	record, ok := m.agentEdits.registry.Snapshot(id)
	if !ok {
		m.errText = "no edit session " + id + " is pending"
		return m
	}
	m.agentEdits.choiceSession = id
	m.choice = agentEditDiscardChoice(record)
	return m
}

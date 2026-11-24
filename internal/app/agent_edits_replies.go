package app

import (
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/staging"
	"github.com/nickooan/ntee-editor/internal/store"
)

// handleAgentEditMessage takes an edit session's end or wait from the
// socket (its edits never reach the UI).
func (m Model) handleAgentEditMessage(msg externalMessageMsg) (tea.Model, tea.Cmd) {
	id := msg.record.Session
	registry := m.agentEdits.registry
	if registry == nil {
		sendExternalReply(msg.reply, extmsg.ErrorReply("this editor takes no agent edits"))
		return m, nil
	}
	if msg.record.Command == extmsg.CommandWait {
		if record, ok := registry.Finished(id); ok {
			sendExternalReply(msg.reply, agentEditOutcomeReply(record))
			return m, nil
		}
		record, ok := registry.Snapshot(id)
		if !ok {
			sendExternalReply(msg.reply, extmsg.ErrorReply("no edit session "+id+" — nothing was staged"))
			return m, nil
		}
		if _, open := record.OpenChangeRequest(); open {
			// The user asked while this agent was not waiting.
			sendExternalReply(msg.reply, agentEditChangesRequestedReply(id))
			return m, nil
		}
		waiters := cloneWaiters(m.agentEdits.waiters)
		waiters[id] = append(slices.Clip(waiters[id]), msg.reply)
		m.agentEdits.waiters = waiters
		return m, nil
	}

	m = m.refreshAgentEditsPending()
	record, ok := registry.Snapshot(id)
	if !ok {
		sendExternalReply(msg.reply, extmsg.ErrorReply("no edit session "+id+" — nothing was staged"))
		return m, nil
	}
	bases, failed := m.pendingBaseSessions(record), failedChecks(record)
	if msg.agentEdits.policy == store.AgentEditPolicyApply && len(bases) == 0 && len(failed) == 0 {
		return m.startAgentEditApply(id, store.AgentEditPolicyApply, false, store.GitFollowUpNone, msg.agentEdits.keep, msg.reply)
	}
	reviewing := "edit session " + id + " ended — " + extmsg.PhraseUserReviewing + " " + countText(len(record.Files), "staged file")
	switch {
	case len(bases) > 0:
		reviewing += " (it builds on edit session " + strings.Join(bases, ", ") + ", still pending, so the user decides)"
	case len(failed) > 0 && msg.agentEdits.policy == store.AgentEditPolicyApply:
		reviewing += " (a check failed, so the user decides)"
	}
	sendExternalReply(msg.reply, reviewing+"; to hear the outcome run: ntee --msg --msg-edit-session "+id+" wait")
	switch {
	case msg.agentEdits.policy == store.AgentEditPolicyApply && len(bases) > 0:
		m.notice = "✎ edit session " + id + " builds on " + strings.Join(bases, ", ") + "'s pending edits — review it"
	case msg.agentEdits.policy == store.AgentEditPolicyApply:
		m.notice = "✎ edit session " + id + ": " + staging.AgentNames(failed) + "'s check failed — review it"
	}
	if m.mode == modeReview && m.review.agentEdits == id && !m.review.agentEditsTrace {
		// The user watched it live: it unlocks where they are.
		m, refresh := m.refreshAgentEditReview(id)
		m.notice = agentEditsFinishedNotice(record)
		return m, refresh
	}
	if m.choice.open() || m.reviewSessionRunning() {
		m.notice = "✎ agent edits ready: session " + id + " · " + countText(len(record.Files), "file") + " — review them under Ctrl+T › agent edits"
		return m, nil
	}
	if msg.sessionAutoStart || msg.agentEdits.policy == store.AgentEditPolicyApply {
		m, prepare := m.enterAgentEditReview(record, false)
		if msg.agentEdits.policy != store.AgentEditPolicyApply {
			m.notice = "✎ EDITS opened without asking"
		}
		return m, prepare
	}
	if m.canAutoJumpToAgentReview() {
		m, prepare := m.enterAgentEditReview(record, false)
		m.notice = agentEditsFinishedNotice(record)
		return m, prepare
	}
	m.agentEdits.choiceSession = id
	m.choice = agentEditsReadyChoice(record)
	return m, nil
}

// dropExpiredWait forgets a wait whose sender gave up, so a review left open
// for hours does not collect a dead channel per re-wait — nor count one as
// an agent still listening.
func (m Model) dropExpiredWait(reply chan<- string) Model {
	for id, channels := range m.agentEdits.waiters {
		if index := slices.Index(channels, reply); index >= 0 {
			waiters := cloneWaiters(m.agentEdits.waiters)
			waiters[id] = slices.Delete(slices.Clone(channels), index, index+1)
			if len(waiters[id]) == 0 {
				delete(waiters, id)
			}
			m.agentEdits.waiters = waiters
			return m
		}
	}
	if index := slices.Index(m.session.waiters, reply); index >= 0 {
		m.session.waiters = slices.Delete(slices.Clone(m.session.waiters), index, index+1)
	}
	return m
}

// cloneWaiters copies a waiter map for copy-on-write.
func cloneWaiters(waiters map[string][]chan<- string) map[string][]chan<- string {
	copied := make(map[string][]chan<- string, len(waiters)+1)
	for id, channels := range waiters {
		copied[id] = channels
	}
	return copied
}

// agentEditOutcomeReply is what the agent hears about a finished session:
// all applied is ok (exit 0), some not applied an error (exit 1) naming
// them, discarded ended (exit 3).
func agentEditOutcomeReply(record store.AgentEditSession) string {
	switch record.Status {
	case store.AgentEditApplied:
		return "finished — edit session " + record.Session + " " + record.Outcome + agentEditGitFollowUpText(record)
	case store.AgentEditDiscarded:
		return extmsg.EndedReply("edit session " + record.Session + " was " + record.Outcome + " — nothing was written")
	}
	return extmsg.ErrorReply("edit session " + record.Session + " " + record.Outcome + agentEditGitFollowUpText(record))
}

// maxFollowUpFiles caps the files a git follow-up names.
const maxFollowUpFiles = 20

// agentEditGitFollowUpText is the git work the user asked of the agent with
// applying, for its outcome reply; "" when they asked for none (the agent
// then leaves git alone).
func agentEditGitFollowUpText(record store.AgentEditSession) string {
	if record.GitFollowUp != store.GitFollowUpCommit && record.GitFollowUp != store.GitFollowUpPullRequest {
		return ""
	}
	var applied []string
	for _, file := range record.Files {
		if file.Result == store.AgentEditFileApplied {
			applied = append(applied, file.Rel)
		}
	}
	if len(applied) == 0 {
		return ""
	}
	files := strings.Join(applied[:min(len(applied), maxFollowUpFiles)], ", ")
	if len(applied) > maxFollowUpFiles {
		files += " … and " + strconv.Itoa(len(applied)-maxFollowUpFiles) + " more"
	}
	if record.GitFollowUp == store.GitFollowUpCommit {
		return " · the user asks you to commit the applied files on the current branch: commit only " + files +
			", with a message you write from the change — no new branch, no push"
	}
	return " · the user asks for a new branch and a pull request: first ask them for the branch name (suggest one from the change); then create the branch, commit only " +
		files + ", push it and open the pull request"
}

// gitFollowUpLabel names a follow-up for the user: "commit", "PR", "".
func gitFollowUpLabel(followUp store.AgentEditGitFollowUp) string {
	switch followUp {
	case store.GitFollowUpCommit:
		return "commit"
	case store.GitFollowUpPullRequest:
		return "PR"
	}
	return ""
}

// answerAgentEditWaiters answers every agent waiting on the session with
// reply, a "waiting — …" that says what changed; they wait again.
func (m Model) answerAgentEditWaiters(id, reply string) Model {
	if len(m.agentEdits.waiters[id]) == 0 {
		return m
	}
	for _, waiter := range m.agentEdits.waiters[id] {
		select {
		case waiter <- reply:
		default:
		}
	}
	waiters := cloneWaiters(m.agentEdits.waiters)
	delete(waiters, id)
	m.agentEdits.waiters = waiters
	return m
}

// handleAgentEditsLayerChanged tells the waiting agents of a session that
// the copy one of its files builds on changed underneath it.
func (m Model) handleAgentEditsLayerChanged(msg agentEditsLayerChangedMsg) (tea.Model, tea.Cmd) {
	m = m.refreshAgentEditsPending()
	text := "waiting — edit session " + msg.below + " changed " + msg.rel + " after edit session " + msg.session + " built on it: "
	if msg.rebased {
		text += "your edits to it were replayed onto the new copy — read it again to see both; wait again to hear the outcome"
	} else {
		text += "your copy can no longer be applied as it is (" + msg.reason + ") — read it again and stage your change on the current content (in a new session, or join " + msg.below + ")"
		if m.mode != modeReview {
			m.notice = "✎ edit session " + msg.session + "'s " + msg.rel + " no longer fits edit session " + msg.below + "'s newer copy"
		}
	}
	m = m.answerAgentEditWaiters(msg.session, text)
	return m, nil
}

// handleAgentEditsFinalising asks the session's waiting agents to check it.
func (m Model) handleAgentEditsFinalising(msg agentEditsFinalisingMsg) (tea.Model, tea.Cmd) {
	m = m.refreshAgentEditsPending()
	m = m.answerAgentEditWaiters(msg.session, "waiting — edit session "+msg.session+" "+extmsg.PhraseFinalising+": every agent ended; build and test it in its sandbox (sandbox), report with check, then wait again for the user's decision")
	if m.mode != modeReview {
		m.notice = "⚙ edit session " + msg.session + " finalising — its agents check it in a sandbox"
	}
	return m.refreshAgentEditReview(msg.session)
}

// handleAgentEditsReopened tells the session's waiting agents it is back to
// editing: their checks are due again later.
func (m Model) handleAgentEditsReopened(msg agentEditsReopenedMsg) (tea.Model, tea.Cmd) {
	m = m.refreshAgentEditsPending()
	m = m.answerAgentEditWaiters(msg.session, "waiting — edit session "+msg.session+" is "+extmsg.PhraseBackToEditing+" ("+staging.AgentLabel(msg.agent)+" is fixing it): its sandbox is gone; check again once every agent has sent end — wait again to hear when")
	if m.mode != modeReview {
		m.notice = "✎ edit session " + msg.session + " is back to editing — " + staging.AgentLabel(msg.agent) + " is fixing it"
	}
	return m, nil
}

// agentEditChangesRequestedReply is what a waiting agent hears when the
// user asks for changes.
func agentEditChangesRequestedReply(id string) string {
	return extmsg.ChangesRequestedReply("the user has questions or changes for edit session " + id +
		": stop and tell them to switch to this terminal, then ask what they want; do not wait again yet. " +
		"The session is open for edits again: stage the follow-ups in it, then end it")
}

func agentEditDeferredReply(id string) string {
	return "waiting — the user set edit session " + id + " " + extmsg.PhraseSetAside + ": still staged, nothing written; wait again to hear their decision"
}

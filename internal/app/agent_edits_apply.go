package app

import (
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/gitcmd"
	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/staging"
	"github.com/nickooan/ntee-editor/internal/store"
)

// unsavedAgentEditFiles are the session's files with unsaved work in the
// editor: the open buffer's edits, or another tab's draft. Applying would
// replace that work.
func (m Model) unsavedAgentEditFiles(record store.AgentEditSession) []string {
	var unsaved []string
	for _, file := range record.Files {
		if m.agentEditFileIsOpen(file.Rel) {
			if m.edit.dirty {
				unsaved = append(unsaved, file.Rel)
			}
			continue
		}
		if rootRel, ok := m.rootRelOf(file.Rel); ok && m.draftSet[rootRel] {
			unsaved = append(unsaved, file.Rel)
		}
	}
	return unsaved
}

// agentEditFileIsOpen reports whether the workspace file rel is the open
// buffer's.
func (m Model) agentEditFileIsOpen(rel string) bool {
	return m.openFile != nil && filepath.Clean(m.openFile.Path) == filepath.Join(m.workspaceRoot, filepath.FromSlash(rel))
}

// rootRelOf maps a workspace path to the current root's, which keys drafts
// and tabs; ok is false outside the root.
func (m Model) rootRelOf(rel string) (string, bool) {
	rootRel, err := filepath.Rel(m.root, filepath.Join(m.workspaceRoot, filepath.FromSlash(rel)))
	if err != nil || rootRel == ".." || strings.HasPrefix(rootRel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rootRel), true
}

// startAgentEditApply writes a session's staged files. The open buffer's
// file is applied here, into the buffer as one undoable step and saved, so
// the user can undo back to what they had; the rest are written off the UI
// goroutine. Unsaved work in the editor is replaced when replaceUnsaved,
// otherwise its file is skipped.
func (m Model) startAgentEditApply(id, policy string, replaceUnsaved bool, followUp store.AgentEditGitFollowUp, keep int, reply chan<- string) (Model, tea.Cmd) {
	registry := m.agentEdits.registry
	record, ok := registry.Snapshot(id)
	switch {
	case !ok:
		sendExternalReply(reply, extmsg.ErrorReply("no edit session "+id+" is pending"))
		m.errText = "no edit session " + id + " is pending"
		return m, nil
	case m.agentEdits.applying[id]:
		sendExternalReply(reply, extmsg.ErrorReply("edit session "+id+" is already being applied"))
		return m, nil
	case record.AgentEndedAt.IsZero():
		m.errText = agentsStillEditingText(record)
		return m, nil
	}
	record, err := registry.BeginApply(id)
	if err != nil {
		sendExternalReply(reply, extmsg.ErrorReply(err.Error()))
		m.errText = err.Error()
		return m, nil
	}
	applying := make(map[string]bool, len(m.agentEdits.applying)+1)
	for session := range m.agentEdits.applying {
		applying[session] = true
	}
	applying[id] = true
	m.agentEdits.applying = applying

	decided := map[string]staging.FileResult{}
	replacedDrafts := map[string]string{} // staged rel → root rel of a draft the apply replaces
	for _, file := range record.Files {
		if file.Result != "" {
			continue // written along with a session stacked on this one
		}
		if m.agentEditFileIsOpen(file.Rel) {
			if m.edit.dirty && !replaceUnsaved {
				decided[file.Rel] = staging.FileResult{Result: store.AgentEditFileSkipped, Note: "unsaved edits in the editor"}
				continue
			}
			var result staging.FileResult
			m, result = m.applyAgentEditToBuffer(file)
			decided[file.Rel] = result
			continue
		}
		rootRel, inRoot := m.rootRelOf(file.Rel)
		if !inRoot || !m.draftSet[rootRel] {
			continue
		}
		if !replaceUnsaved {
			decided[file.Rel] = staging.FileResult{Result: store.AgentEditFileSkipped, Note: "unsaved draft in the editor"}
			continue
		}
		replacedDrafts[file.Rel] = rootRel
	}
	m.notice = "applying edit session " + id + "…"
	repoFolders := map[string]string{}
	if followUp == store.GitFollowUpCommit || followUp == store.GitFollowUpPullRequest {
		repoFolders = m.agentEditRepoFolders(record)
	}
	return m, func() tea.Msg {
		results := registry.ApplyFiles(id, record.Files, decided)
		finished, err := registry.FinishWithGitBranches(id, policy, followUp, currentGitBranches(repoFolders), results, keep)
		return agentEditsFinishedMsg{session: id, record: finished, err: err, reply: reply, replacedDrafts: replacedDrafts}
	}
}

// agentEditRepoFolders maps each repo name agentEditRepos gives the record
// to its folder on disk.
func (m Model) agentEditRepoFolders(record store.AgentEditSession) map[string]string {
	folders := map[string]string{}
	for _, file := range record.Files {
		if name, folder, ok := m.agentEditRepoOf(file.Rel); ok {
			folders[name] = folder
		}
	}
	return folders
}

// currentGitBranches is each repo's checked-out branch, by repo name; a repo
// on a detached HEAD, or where git fails, is left out. It runs git, so off
// the UI goroutine.
func currentGitBranches(repoFolders map[string]string) map[string]string {
	branches := map[string]string{}
	for name, folder := range repoFolders {
		output, err := gitcmd.Out(folder, "branch", "--show-current")
		if branch := strings.TrimSpace(string(output)); err == nil && branch != "" {
			branches[name] = branch
		}
	}
	return branches
}

func (m Model) handleAgentEditsReported(msg agentEditsReportedMsg) (tea.Model, tea.Cmd) {
	text := "edit session " + msg.record.Session + " · " + gitReportLabel(msg.record)
	if report := msg.record.GitReport; report != nil {
		if note, _, _ := strings.Cut(report.Note, "\n"); note != "" {
			text += " — " + note
		}
		if report.Result == store.GitReportDone {
			m.notice = text
		} else {
			m.errText = text
		}
	}
	m, loadFinished := m.loadAgentEditsFinishedCmd()
	return m, loadFinished
}

// applyAgentEditToBuffer puts a staged file's content into the open buffer
// as one undoable step and saves it — unless it cannot be written
// (staging's ApplyCheck: it changed on disk, or a layer below it did). The
// result names the layers below, settled once the session finishes.
func (m Model) applyAgentEditToBuffer(file store.AgentEditFile) (Model, staging.FileResult) {
	below, refusal, ok := m.agentEdits.registry.ApplyCheck(file)
	if !ok {
		return m, refusal
	}
	m = m.flushBurst()
	lines := splitBufferLines(file.Content)
	m.edit.lines = lines
	m.edit.cy = input.Clamp(m.edit.cy, 0, len(lines)-1)
	m.edit.clampCursor()
	m.fileScrollY = input.Clamp(m.fileScrollY, 0, max(0, len(lines)-1))
	m.edit.dirty = true
	m.edit.rev++ // buffer changed: force a highlight rescan (see edit.go)
	m.snapDirty = true
	m = m.pushSnapshot("edit") // the whole staged file = one undoable step
	previousError := m.errText
	m = m.saveEdit()
	if m.edit.dirty {
		note := strings.TrimPrefix(m.errText, "save failed: ")
		m.errText = previousError
		return m, staging.FileResult{Result: store.AgentEditFileSkipped, Note: "write failed: " + note}
	}
	return m, staging.FileResult{Result: store.AgentEditFileApplied, Settles: below}
}

// handleAgentEditsFinished lands an applied or discarded session: the agent
// and its waiters hear the outcome, the views and the panel follow.
func (m Model) handleAgentEditsFinished(msg agentEditsFinishedMsg) (tea.Model, tea.Cmd) {
	applying := make(map[string]bool, len(m.agentEdits.applying))
	for session := range m.agentEdits.applying {
		if session != msg.session {
			applying[session] = true
		}
	}
	m.agentEdits.applying = applying
	if msg.err != nil {
		sendExternalReply(msg.reply, extmsg.ErrorReply(msg.err.Error()))
		m.errText = "edit session " + msg.session + ": " + msg.err.Error()
		return m.refreshAgentEditsPending(), nil
	}
	for _, file := range msg.record.Files {
		rootRel, replaced := msg.replacedDrafts[file.Rel]
		if replaced && file.Result == store.AgentEditFileApplied {
			_ = m.db.DeleteDraft(rootRel)
			delete(m.draftSet, rootRel)
		}
	}
	sendExternalReply(msg.reply, agentEditOutcomeReply(msg.record))
	agentHeard := msg.reply != nil || len(m.agentEdits.waiters[msg.session]) > 0
	m = m.agentEditSessionOver(msg.session, msg.record)
	outcome := "edit session " + msg.session + " " + msg.record.Outcome
	if agentEditGitFollowUpText(msg.record) != "" {
		outcome += " · asked the agent to " + map[store.AgentEditGitFollowUp]string{store.GitFollowUpCommit: "commit", store.GitFollowUpPullRequest: "open a PR"}[msg.record.GitFollowUp]
		if !agentHeard {
			outcome += " (no agent is waiting now — it hears on its next wait)"
		} else {
			outcome += " — it may ask you to confirm in its terminal"
		}
	}
	if msg.record.Status == store.AgentEditApplied || msg.record.Status == store.AgentEditDiscarded {
		m.notice = outcome
	} else {
		m.errText = outcome
	}
	var gitCmd tea.Cmd
	if msg.record.Status != store.AgentEditDiscarded {
		m, gitCmd = m.maybeGitRefresh() // the files changed on disk
	}
	m, loadFinished := m.loadAgentEditsFinishedCmd()
	return m, tea.Batch(gitCmd, loadFinished)
}

// startAgentEditDiscard drops a pending session's staged edits for the user.
func (m Model) startAgentEditDiscard(id string) (Model, tea.Cmd) {
	if m.agentEdits.applying[id] {
		m.errText = "edit session " + id + " is being applied"
		return m, nil
	}
	registry, keep := m.agentEdits.registry, m.agentEditKeep()
	return m, func() tea.Msg {
		record, err := registry.Discard(id, "the user", keep)
		return agentEditsFinishedMsg{session: id, record: record, err: err}
	}
}

// agentEditKeep is the finished sessions kept, from the settings once
// loaded.
func (m Model) agentEditKeep() int {
	if m.external.settingsLoaded {
		return m.external.settings.AgentEditKeep
	}
	return store.DefaultAgentEditKeep
}

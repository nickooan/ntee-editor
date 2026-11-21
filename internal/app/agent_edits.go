package app

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/staging"
	"github.com/nickooan/ntee-editor/internal/store"
)

// agentEditsState is the editor's side of agent edit sessions: the registry
// the socket stages into, the agents waiting to hear how a session ended,
// and the lists the Ctrl+T panel shows. waiters and applying are replaced,
// never mutated: Model copies share them.
type agentEditsState struct {
	registry *staging.Registry
	waiters  map[string][]chan<- string
	applying map[string]bool // sessions being applied or discarded right now
	// pending mirrors registry.Pending() as of the last change, so frames
	// never take the registry's locks; finished is the log's finished
	// sessions, loaded off the UI goroutine for the panel.
	pending []store.AgentEditSession
	// stagedFiles maps each root-relative file with unsettled staged edits
	// to the session on top, rebuilt with pending — the tree's mark and the
	// open notice read it per frame.
	stagedFiles map[string]string
	// openNotice is the staged-file notice shown on the last open, so the
	// next open of an unstaged file clears it rather than leave it stale.
	openNotice string
	// stillEditing holds the pending sessions some agent still edits,
	// rebuilt with pending: the header's label and the review's hint.
	stillEditing map[string]bool
	// checking holds the finalising sessions: their agents check them.
	checking       map[string]bool
	finished       []store.AgentEditSession
	finishedLoaded bool
	// choiceSession is the session an open apply/discard/ready overlay is
	// about.
	choiceSession string
}

// agentEditsFinishedMsg lands a session that was applied or discarded off
// the UI goroutine. reply is an agent-applies end waiting for the outcome.
type agentEditsFinishedMsg struct {
	session string
	record  store.AgentEditSession
	err     error
	reply   chan<- string
}

// agentEditsFinishedListMsg lands the log's finished sessions.
type agentEditsFinishedListMsg struct {
	sessions []store.AgentEditSession
	err      error
}

// WithAgentEdits attaches the edit session registry the socket stages into.
func (m Model) WithAgentEdits(registry *staging.Registry) Model {
	m.agentEdits.registry = registry
	return m.refreshAgentEditsPending()
}

// refreshAgentEditsPending re-reads the pending sessions for the panel and
// the status bar.
func (m Model) refreshAgentEditsPending() Model {
	if m.agentEdits.registry != nil {
		m.agentEdits.pending = m.agentEdits.registry.Pending()
		m.agentEdits.stagedFiles = m.stagedFilesOf(m.agentEdits.pending)
		m.agentEdits.stillEditing, m.agentEdits.checking = map[string]bool{}, map[string]bool{}
		for _, session := range m.agentEdits.pending {
			switch session.Phase() {
			case store.PhaseEditing:
				m.agentEdits.stillEditing[session.Session] = true
			case store.PhaseFinalising:
				m.agentEdits.checking[session.Session] = true
			}
		}
	}
	return m
}

// stagedFilesOf maps each file with unsettled staged edits, by root path,
// to its top session: the newest one no other session builds on. Unlike
// staging's Latest it reads no disk, so a stale layer still marks its file.
func (m Model) stagedFilesOf(sessions []store.AgentEditSession) map[string]string {
	builtOn := map[string]bool{}
	for _, session := range sessions {
		for _, file := range session.Files {
			if file.Result == "" && file.BaseSession != "" {
				builtOn[file.BaseSession+"\x00"+file.Rel] = true
			}
		}
	}
	staged := map[string]string{}
	for _, session := range sessions { // oldest first: a newer top wins
		for _, file := range session.Files {
			if file.Result != "" || builtOn[session.Session+"\x00"+file.Rel] {
				continue
			}
			if rootRel, ok := m.rootRelOf(file.Rel); ok {
				staged[filepath.ToSlash(rootRel)] = session.Session
			}
		}
	}
	return staged
}

// stagedFileNotice tells the user the open file has staged edits that are
// not on disk; "" when it has none.
func (m Model) stagedFileNotice() string {
	session, ok := m.agentEdits.stagedFiles[m.openRel]
	if !ok || m.openRel == "" {
		return ""
	}
	return "✎ " + m.openRel + " has staged edits (edit session " + session + ") — not on disk yet · Ctrl+T › agent edits"
}

func (m Model) loadAgentEditsFinishedCmd() tea.Cmd {
	if m.agentEdits.registry == nil {
		return nil
	}
	log := m.agentEdits.registry.Log()
	return func() tea.Msg {
		sessions, err := log.LoadFinishedAgentEdits()
		return agentEditsFinishedListMsg{sessions: sessions, err: err}
	}
}

func (m Model) handleAgentEditsFinishedList(msg agentEditsFinishedListMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.errText = "agent edits: " + msg.err.Error()
	}
	m.agentEdits.finished, m.agentEdits.finishedLoaded = msg.sessions, true
	m.agentEditsPicker.index = input.Clamp(m.agentEditsPicker.index, 0, m.agentEditsPickerRowCount()-1)
	return m, nil
}

// handleAgentEditsChanged follows the socket side: a staged edit updates the
// pending list; a session the agent discarded ends for its waiters and
// leaves the screen.
func (m Model) handleAgentEditsChanged(msg agentEditsChangedMsg) (tea.Model, tea.Cmd) {
	m = m.refreshAgentEditsPending()
	if record, ok := m.agentEdits.registry.Finished(msg.session); ok {
		m = m.agentEditSessionOver(msg.session, record)
		m.notice = "edit session " + msg.session + " " + record.Outcome
		return m, m.loadAgentEditsFinishedCmd()
	}
	if record, ok := m.agentEdits.registry.Snapshot(msg.session); ok && m.mode != modeReview {
		m.notice = "✎ agent edit session " + msg.session + " · " + countText(len(record.Files), "file") + " staged"
		if notice := m.stagedFileNotice(); notice != "" && m.agentEdits.stagedFiles[m.openRel] == msg.session {
			m.notice = notice
		}
	}
	return m, nil
}

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
		if _, ok := registry.Snapshot(id); !ok {
			sendExternalReply(msg.reply, extmsg.ErrorReply("no edit session "+id+" — nothing was staged"))
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
		return m.startAgentEditApply(id, store.AgentEditPolicyApply, false, msg.agentEdits.keep, msg.reply)
	}
	reviewing := "edit session " + id + " ended — the user is reviewing " + countText(len(record.Files), "staged file")
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
	m.agentEdits.choiceSession = id
	m.choice = agentEditsReadyChoice(record)
	return m, nil
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
		return "finished — edit session " + record.Session + " " + record.Outcome
	case store.AgentEditDiscarded:
		return extmsg.EndedReply("edit session " + record.Session + " was " + record.Outcome + " — nothing was written")
	}
	return extmsg.ErrorReply("edit session " + record.Session + " " + record.Outcome)
}

// agentEditSessionOver answers the session's waiters and leaves its review.
func (m Model) agentEditSessionOver(id string, record store.AgentEditSession) Model {
	for _, waiter := range m.agentEdits.waiters[id] {
		// Reply channels hold one answer, so a sender that gave up never
		// blocks the UI.
		select {
		case waiter <- agentEditOutcomeReply(record):
		default:
		}
	}
	waiters := cloneWaiters(m.agentEdits.waiters)
	delete(waiters, id)
	m.agentEdits.waiters = waiters
	if m.mode == modeReview && m.review.agentEdits == id && !m.review.agentEditsTrace {
		m = m.exitReview()
	}
	if m.agentEdits.choiceSession == id && m.choice.open() {
		m.choice = choiceOverlay{}
	}
	return m.refreshAgentEditsPending()
}

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
func (m Model) startAgentEditApply(id, policy string, replaceUnsaved bool, keep int, reply chan<- string) (Model, tea.Cmd) {
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
	applying := make(map[string]bool, len(m.agentEdits.applying)+1)
	for session := range m.agentEdits.applying {
		applying[session] = true
	}
	applying[id] = true
	m.agentEdits.applying = applying

	decided := map[string]staging.FileResult{}
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
		_ = m.db.DeleteDraft(rootRel)
		delete(m.draftSet, rootRel)
	}
	m.notice = "applying edit session " + id + "…"
	return m, func() tea.Msg {
		results := registry.ApplyFiles(id, record.Files, decided)
		finished, err := registry.Finish(id, policy, results, keep)
		return agentEditsFinishedMsg{session: id, record: finished, err: err, reply: reply}
	}
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
	sendExternalReply(msg.reply, agentEditOutcomeReply(msg.record))
	m = m.agentEditSessionOver(msg.session, msg.record)
	if msg.record.Status == store.AgentEditApplied || msg.record.Status == store.AgentEditDiscarded {
		m.notice = "edit session " + msg.session + " " + msg.record.Outcome
	} else {
		m.errText = "edit session " + msg.session + " " + msg.record.Outcome
	}
	var gitCmd tea.Cmd
	if msg.record.Status != store.AgentEditDiscarded {
		m, gitCmd = m.maybeGitRefresh() // the files changed on disk
	}
	return m, tea.Batch(gitCmd, m.loadAgentEditsFinishedCmd())
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

// agentEditApplyChoice confirms applying a session; unsaved work in the
// editor gets its own choice: replace it (undo brings it back in the open
// buffer) or skip its files.
func (m Model) agentEditApplyChoice(record store.AgentEditSession) choiceOverlay {
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
	unsaved := m.unsavedAgentEditFiles(record)
	if len(unsaved) == 0 {
		choice.choices = []string{apply, "Cancel"}
		return choice
	}
	choice.body = append(choice.body, overlayLine{text: "unsaved edits in " + strings.Join(unsaved, ", "), style: overlayLineHighlight})
	choice.choices = []string{apply + ", replace unsaved", apply + ", skip those", "Cancel"}
	return choice
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
			if len(m.unsavedAgentEditFiles(record)) > 0 || len(m.pendingBaseSessions(record)) > 0 {
				m.agentEdits.choiceSession = id
				m.choice = m.agentEditApplyChoice(record)
				return m, nil
			}
			return m.startAgentEditApply(id, store.AgentEditPolicyReview, false, m.agentEditKeep(), nil)
		case 1:
			return m.startAgentEditDiscard(id)
		}
		m = m.exitReview().deferAgentEdits(id)
		m.notice = "agent edits " + id + " kept for later — reopen them under Ctrl+T › agent edits"
	case choiceApplyAgentEdits:
		switch {
		case chosen == choiceCount-1:
			m.notice = "apply cancelled"
		case choiceCount == 3:
			return m.startAgentEditApply(id, store.AgentEditPolicyReview, chosen == 0, m.agentEditKeep(), nil)
		default:
			return m.startAgentEditApply(id, store.AgentEditPolicyReview, false, m.agentEditKeep(), nil)
		}
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
	m.choice = m.agentEditApplyChoice(record)
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

// startAgentEditForceEnd stops waiting for the session's agents, off the UI
// goroutine (the record is saved).
func (m Model) startAgentEditForceEnd(id string) (Model, tea.Cmd) {
	registry := m.agentEdits.registry
	return m, func() tea.Msg {
		record, stopped, err := registry.ForceEnd(id)
		return agentEditsForceEndedMsg{session: id, record: record, stopped: stopped, err: err}
	}
}

// agentEditsForceEndedMsg lands a session the user stopped waiting for.
type agentEditsForceEndedMsg struct {
	session string
	record  store.AgentEditSession
	stopped []string
	err     error
}

// handleAgentEditsForceEnded offers the session's review, like an end from
// its last agent.
func (m Model) handleAgentEditsForceEnded(msg agentEditsForceEndedMsg) (tea.Model, tea.Cmd) {
	m = m.refreshAgentEditsPending()
	if msg.err != nil {
		m.errText = "edit session " + msg.session + ": " + msg.err.Error()
		return m, nil
	}
	m.notice = "stopped waiting for " + staging.AgentNames(msg.stopped) + " in edit session " + msg.session
	if m.choice.open() {
		return m, nil
	}
	m.agentEdits.choiceSession = msg.session
	m.choice = agentEditsReadyChoice(msg.record)
	return m, nil
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
	choice := choiceOverlay{kind: choiceLeaveAgentEditReview, title: "Edit session " + record.Session + " — what now?", choices: []string{"Apply all", "Discard all", "Keep for later"}}
	choice.body = append(choice.body, overlayLine{text: countText(len(record.Files), "file") + " staged — nothing is written yet", style: overlayLineStrong})
	choice.body = append(choice.body, agentEditFileLines(record)...)
	choice.body = append(choice.body, overlayLine{text: "later: reopen under Ctrl+T › agent edits", style: overlayLineDim})
	return choice
}

// deferAgentEdits answers the session's waiting agents that the user set
// it aside: still staged, so a new wait blocks until the real decision.
func (m Model) deferAgentEdits(id string) Model {
	if len(m.agentEdits.waiters[id]) == 0 {
		return m
	}
	for _, waiter := range m.agentEdits.waiters[id] {
		select {
		case waiter <- agentEditDeferredReply(id):
		default:
		}
	}
	waiters := cloneWaiters(m.agentEdits.waiters)
	delete(waiters, id)
	m.agentEdits.waiters = waiters
	return m
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
	m = m.answerAgentEditWaiters(msg.session, "waiting — edit session "+msg.session+" is finalising: every agent ended; build and test it in its sandbox (sandbox), report with check, then wait again for the user's decision")
	if m.mode != modeReview {
		m.notice = "⚙ edit session " + msg.session + " finalising — its agents check it in a sandbox"
	}
	return m, nil
}

// handleAgentEditsReopened tells the session's waiting agents it is back to
// editing: their checks are due again later.
func (m Model) handleAgentEditsReopened(msg agentEditsReopenedMsg) (tea.Model, tea.Cmd) {
	m = m.refreshAgentEditsPending()
	m = m.answerAgentEditWaiters(msg.session, "waiting — edit session "+msg.session+" is back to editing ("+staging.AgentLabel(msg.agent)+" is fixing it): its sandbox is gone; check again once every agent has sent end — wait again to hear when")
	if m.mode != modeReview {
		m.notice = "✎ edit session " + msg.session + " is back to editing — " + staging.AgentLabel(msg.agent) + " is fixing it"
	}
	return m, nil
}

func agentEditDeferredReply(id string) string {
	return "waiting — the user set edit session " + id + " aside for later: still staged, nothing written; wait again to hear their decision"
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

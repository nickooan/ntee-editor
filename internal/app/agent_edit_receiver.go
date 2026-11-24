package app

import (
	"cmp"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/staging"
	"github.com/nickooan/ntee-editor/internal/store"
)

// agentEditSettings are the settings that decide what an edit session's end
// does, read by the receiver when the end arrives.
type agentEditSettings struct {
	policy string // store.AgentEditPolicyReview or store.AgentEditPolicyApply
	keep   int
}

// agentEditsChangedMsg tells the UI an edit session changed on the socket
// side: an edit was staged, or the agent discarded the session.
type agentEditsChangedMsg struct {
	session string
	// started: this edit started the session; policy is the user's setting
	// when it arrived (the live review opens only to review first).
	started bool
	policy  string
}

// WithAgentEdits attaches the edit session registry: edit sessions are
// refused without one.
func (r *ExternalReceiver) WithAgentEdits(registry *staging.Registry) *ExternalReceiver {
	r.agentEdits = registry
	return r
}

// notify tells the UI something changed without waiting for it: the sender
// already has its reply. Notices reach the UI in the order raised, through
// one dispatcher (dispatchNotices); a change notice already waiting for the
// same session is not queued twice.
func (r *ExternalReceiver) notify(msg any) {
	r.sinkMu.Lock()
	hasSink := r.sink != nil
	r.sinkMu.Unlock()
	if !hasSink {
		return
	}
	r.noticeMu.Lock()
	if !r.noticeAlreadyWaiting(msg) {
		r.notices = append(r.notices, msg)
	}
	r.noticeMu.Unlock()
	select {
	case r.noticeWake <- struct{}{}:
	default: // the dispatcher is already due to run
	}
}

// noticeAlreadyWaiting reports a plain change notice that an identical
// waiting one covers. r.noticeMu is held.
func (r *ExternalReceiver) noticeAlreadyWaiting(msg any) bool {
	changed, ok := msg.(agentEditsChangedMsg)
	if !ok || changed.started {
		return false
	}
	for _, waiting := range r.notices {
		if waiting == any(changed) {
			return true
		}
	}
	return false
}

// dispatchNotices delivers notices to sink one at a time, so a slow UI
// holds back only this goroutine, never a socket sender.
func (r *ExternalReceiver) dispatchNotices(sink func(msg any)) {
	for range r.noticeWake {
		r.noticeMu.Lock()
		batch := r.notices
		r.notices = nil
		r.noticeMu.Unlock()
		for _, notice := range batch {
			sink(notice)
		}
	}
}

// handleAgentEdit carries out an edit session's write, replace, show, status
// or discard right here on the socket goroutine: staging never needs the UI,
// so the agent hears at once whether its edit applied.
func (r *ExternalReceiver) handleAgentEdit(message extmsg.Message, settings store.ExternalSettings) string {
	id := message.Session
	switch message.Command {
	case extmsg.CommandStatus:
		record, ok := r.agentEdits.Snapshot(id)
		if !ok {
			if finished, ok := r.agentEdits.Finished(id); ok {
				return extmsg.OkReply("edit session " + id + " is finished: " + finished.Outcome)
			}
			return extmsg.ErrorReply("no edit session " + id + " — nothing was staged")
		}
		return extmsg.BodyReply(agentEditStatusText(record))
	case extmsg.CommandDiscard:
		record, err := r.agentEdits.Discard(id, "the agent", settings.AgentEditKeep)
		if err != nil {
			return extmsg.ErrorReply(err.Error())
		}
		r.notify(agentEditsChangedMsg{session: id})
		return extmsg.OkReply("discarded edit session " + id + " — " + countText(len(record.Files), "staged file") + " dropped, nothing written")
	case extmsg.CommandSandbox:
		return r.handleSandbox(message)
	}

	anchored, err := r.resolvePath(message.Paths[0], false)
	if err != nil {
		return extmsg.ErrorReply(err.Error())
	}
	rel, line := splitLineAnchor(anchored)
	var result staging.EditResult
	switch message.Command {
	case extmsg.CommandShow, extmsg.CommandRead:
		content, from, exists, err := r.agentEdits.Content(id, rel)
		switch {
		case err != nil:
			return extmsg.ErrorReply(err.Error())
		case !exists:
			return extmsg.ErrorReply("no such file: " + rel + " — not staged and not on disk")
		case from == "" && len(content) > extmsg.MaxBodyBytes:
			return extmsg.ErrorReply(rel + " is larger than " + strconv.Itoa(extmsg.MaxBodyBytes>>20) + " MB and has no staged edits — read it with your own tools")
		case message.Command == extmsg.CommandShow:
			return extmsg.BodyReply(content)
		}
		detail := extmsg.WithVersion(extmsg.ReadSourceDetail(from), r.agentEdits.Remember(content))
		if from != "" && from == id {
			if stale := r.agentEdits.StaleNote(id, rel); stale != "" {
				detail = extmsg.WithStale(detail, stale)
			}
		}
		repo, others := r.agentEdits.SessionsInRepo(rel, id, from)
		return extmsg.BodyReplyWith(content, detail+repoSessionsDetail(repo, others))
	case extmsg.CommandStaged:
		session, err := r.agentEdits.StagedIn(rel)
		switch {
		case err != nil:
			return extmsg.ErrorReply(err.Error())
		case session == "":
			return extmsg.OkReply("not staged")
		}
		return extmsg.StagedReply(session)
	case extmsg.CommandWrite:
		if line > 0 {
			return extmsg.ErrorReply("write sets the whole file — leave #L" + strconv.Itoa(line) + " off")
		}
		result, err = r.agentEdits.WriteExpecting(id, message.Agent, rel, message.Payload, message.Expect)
	case extmsg.CommandReplace:
		payload, decodeErr := extmsg.DecodeReplace(message.Payload)
		if decodeErr != nil {
			return extmsg.ErrorReply(decodeErr.Error())
		}
		result, err = r.agentEdits.Replace(id, message.Agent, rel, staging.Replacement{Old: payload.Old, New: payload.New, All: payload.All, HintLine: line, Expect: message.Expect})
	}
	if err != nil {
		return extmsg.ErrorReply(agentEditErrorText(err))
	}
	r.notify(agentEditsChangedMsg{session: id, started: result.SessionStart, policy: settings.AgentEditPolicy})
	if result.Reopened {
		r.notify(agentEditsReopenedMsg{session: id, agent: message.Agent})
	}
	for _, rebase := range result.Rebased {
		r.notify(agentEditsLayerChangedMsg{session: rebase.Session, below: id, rel: rel, rebased: rebase.Rebased, reason: rebase.Reason})
	}
	text := agentEditReplyText(message.Command, result, id)
	for _, note := range result.Notes {
		text += " · note: " + note
	}
	for _, rebase := range result.Rebased {
		if rebase.Rebased {
			text += " · edit session " + rebase.Session + " built on your older copy of " + rel + "; its edits were replayed onto this one"
			continue
		}
		text += " · edit session " + rebase.Session + " built on your older copy of " + rel + " and can no longer be applied as it is (" + rebase.Reason + ") — it was told"
	}
	if result.SessionStart {
		if repo, others := r.agentEdits.SessionsInRepo(rel, id, result.BaseSession); len(others) > 0 {
			text += " · edit session " + others[0].Session + " is also changing " + staging.RepoName(repo) + editingText(others[0].Editing) +
				" — to join it instead, discard this session and edit in session " + others[0].Session
		}
	}
	return extmsg.OkReply(extmsg.WithVersion(text, result.Version))
}

// agentEditErrorText words a refused edit for the socket's agents, who use
// ntee --msg: staging's typed refusals get its commands and flags (the MCP
// tools reword those into theirs).
func agentEditErrorText(err error) string {
	var stale *staging.StaleReadError
	var missing *staging.OldTextMissingError
	var ambiguous *staging.OldTextAmbiguousError
	switch {
	case errors.As(err, &stale):
		return fmt.Sprintf("%s changed since you read it: %s — %s; read it again (ntee --msg read) and stage your change on the current content", stale.Rel, stale.Change, stale.Conflict)
	case errors.As(err, &missing):
		return missing.Error() + " (ntee --msg show prints the staged content)"
	case errors.As(err, &ambiguous):
		return fmt.Sprintf("the old text matches %d times in %s (lines %s) — add surrounding lines to make it unique, add #L<n> to the path, or replace them all with --msg-replace-all", ambiguous.Count, ambiguous.Rel, ambiguous.Lines)
	}
	return err.Error()
}

// handleSandbox builds, refreshes or drops the session's sandbox, here on
// the socket goroutine: a copy can take a while, and the UI never waits.
func (r *ExternalReceiver) handleSandbox(message extmsg.Message) string {
	id := message.Session
	mode := ""
	if len(message.Args) == 1 {
		mode = message.Args[0]
	}
	if mode == extmsg.SandboxDrop {
		r.agentEdits.DropSandbox(id)
		return extmsg.OkReply("dropped the sandbox of edit session " + id)
	}
	include := make([]string, 0, len(message.Paths))
	for _, raw := range message.Paths {
		rel, err := r.resolvePath(raw, false)
		if err != nil {
			return extmsg.ErrorReply(err.Error())
		}
		include = append(include, rel)
	}
	result, err := r.agentEdits.Sandbox(id, mode == extmsg.SandboxFresh, include)
	if err != nil {
		return extmsg.ErrorReply(err.Error())
	}
	units := make([]string, len(result.Units))
	for index, unit := range result.Units {
		units[index] = staging.RepoName(unit)
	}
	return extmsg.OkReply("sandbox " + result.Path + " · holds " + strings.Join(units, ", ") + " (laid out like the project) · " +
		countText(result.Placed, "staged file") + " in place · copied " + result.BuiltAt.Local().Format("15:04:05") +
		" — build, test and search there; edits made there are not staged (stage them, then ask for the sandbox again to refresh); it is deleted when the session is applied or discarded")
}

// repoSessionsDetail adds to a read's detail the sessions still open that
// change the file's repo, so the reader can join one: " · also editing
// <repo>: S (a, b); T (c)".
func repoSessionsDetail(repo string, sessions []staging.RepoSession) string {
	if len(sessions) == 0 {
		return ""
	}
	parts := make([]string, len(sessions))
	for index, session := range sessions {
		parts[index] = session.Session + " (" + staging.AgentNames(session.Editing) + ")"
	}
	return extmsg.WithAlsoEditing("", staging.RepoName(repo), strings.Join(parts, "; "))
}

func editingText(agents []string) string {
	if len(agents) == 0 {
		return ""
	}
	return " (still editing: " + staging.AgentNames(agents) + ")"
}

// agentEditReplyText is what an agent hears after a staged edit.
func agentEditReplyText(command string, result staging.EditResult, session string) string {
	var text string
	if command == extmsg.CommandWrite {
		text = fmt.Sprintf("staged %s (+%d −%d)", result.Rel, result.Added, result.Removed)
		if result.NewFile {
			text = fmt.Sprintf("staged %s (new file, %s)", result.Rel, countText(result.Added, "line"))
		}
	} else {
		text = "replaced " + result.Rel + " " + lineRangeText(result.LineFrom, result.LineTo)
		if result.Replaced > 1 {
			text += " (" + strconv.Itoa(result.Replaced) + " matches)"
		}
	}
	text += " · edit " + strconv.Itoa(result.EditCount) + " · " + countText(result.StagedFiles, "file") + " staged"
	if result.EditCount == 1 && result.BaseSession != "" {
		text += " · builds on edit session " + result.BaseSession + "'s staged edits to it (not applied yet; to add to that change set instead, edit in session " + result.BaseSession + ")"
	}
	if result.Reopened {
		text += " · edit session " + session + " is back to editing — its sandbox is gone and every agent checks again once you send end"
	}
	if result.Joined {
		text += " · joined edit session " + session
		if len(result.OtherAgents) > 0 {
			text += " (still editing: " + staging.AgentNames(result.OtherAgents) + ")"
		}
		text += " — the user reviews it once every agent in it sends end"
	}
	if result.SessionStart {
		text += " · edit session " + session + " started — the files stay untouched until it is applied; send end when every edit is staged"
	}
	return text
}

// agentEditStatusText is status's body: the session, then one line per
// staged file.
func agentEditStatusText(record store.AgentEditSession) string {
	var builder strings.Builder
	state := "open for edits"
	switch record.Phase() {
	case store.PhaseFinalising:
		state = "finalising — its agents check it in the sandbox"
	case store.PhaseReady:
		state = "ended — waiting for the user to apply or discard"
	}
	if request, open := record.OpenChangeRequest(); open {
		state += " · the user requested changes at " + request.At.Local().Format("15:04:05") + " — ask them in your terminal"
	}
	fmt.Fprintf(&builder, "edit session %s · %s staged · %s\n", record.Session, countText(len(record.Files), "file"), state)
	if agents := record.WithAgents().Agents; len(agents) > 1 || len(agents) == 1 && agents[0].Agent != "" {
		parts := make([]string, len(agents))
		for index, participant := range agents {
			parts[index] = cmp.Or(participant.Agent, "unnamed") + " " + agentState(participant)
		}
		fmt.Fprintf(&builder, "agents: %s\n", strings.Join(parts, " · "))
	}
	for _, file := range record.Files {
		_, _, added, removed := staging.LineChanges(file.BaseContent, file.Content)
		kind := "modified"
		if !file.BaseExists {
			kind = "new"
		}
		fmt.Fprintf(&builder, "%s\t%s · +%d −%d · %s", file.Rel, kind, added, removed, countText(len(file.Edits), "edit"))
		switch {
		case file.Result != "":
			fmt.Fprintf(&builder, " · %s (%s)", file.Result, file.ResultNote)
		case file.BaseSession != "":
			fmt.Fprintf(&builder, " · builds on edit session %s", file.BaseSession)
		}
		builder.WriteString("\n")
	}
	return builder.String()
}

// agentState is an agent's part in a session, for status and the panel:
// editing, then its check once the session finalises.
func agentState(participant store.AgentEditParticipant) string {
	switch {
	case participant.Stopped:
		return "stopped by the user"
	case participant.Editing():
		return "editing"
	case participant.Check != "":
		return string(participant.Check)
	}
	return "done"
}

// checkLines lists each agent's check for the user: "✓ agent-a passed —
// go test ./... ok".
func checkLines(record store.AgentEditSession) []string {
	var lines []string
	for _, participant := range record.WithAgents().Agents {
		mark := map[store.AgentEditCheckResult]string{store.CheckPassed: "✓", store.CheckFailed: "✗", store.CheckSkipped: "–"}[participant.Check]
		if mark == "" {
			continue
		}
		line := mark + " " + cmp.Or(participant.Agent, "unnamed") + " " + string(participant.Check)
		if note := strings.TrimSpace(strings.SplitN(participant.CheckNote, "\n", 2)[0]); note != "" {
			line += " — " + note
		}
		lines = append(lines, line)
	}
	return lines
}

// failedChecks lists the agents whose check failed.
func failedChecks(record store.AgentEditSession) []string {
	var failed []string
	for _, participant := range record.Agents {
		if participant.Check == store.CheckFailed {
			failed = append(failed, participant.Agent)
		}
	}
	return failed
}

// agentEditsFinalisingMsg tells the UI a session's agents all ended: its
// waiting agents are asked to check it.
type agentEditsFinalisingMsg struct {
	session string
}

// agentEditsReopenedMsg tells the UI one of a finalising session's agents
// edited it again: it is back to editing, and the checks are due anew.
type agentEditsReopenedMsg struct {
	session, agent string
}

// agentEditsLayerChangedMsg tells the UI a session's copy of rel was built
// on a lower session's copy that just changed: its edits were replayed onto
// the new copy, or could not be.
type agentEditsLayerChangedMsg struct {
	session, below, rel string
	rebased             bool
	reason              string
}

// agentEditFinalisingText tells agents what finalising asks of them.
func agentEditFinalisingText(session string) string {
	return "edit session " + session + " is finalising — every agent's edits are in and no one else may join; build and test them in its sandbox (run: ntee --msg --msg-edit-session " + session +
		" sandbox), then report with check pass, fail or skip (what you ran in a note); the user reviews once every agent checked — to hear the outcome run: ntee --msg --msg-edit-session " + session + " wait"
}

// agentEditPartEndedText answers an end that leaves other agents editing.
func agentEditPartEndedText(record store.AgentEditSession) string {
	editing := record.EditingAgents()
	return "your part of edit session " + record.Session + " is done — waiting for " + countText(len(editing), "other agent") + " (" + staging.AgentNames(editing) +
		") to end; then the session finalises and you check it in its sandbox — to hear when, run: ntee --msg --msg-edit-session " + record.Session + " wait"
}

func lineRangeText(from, to int) string {
	if from == 0 {
		return "(no change)"
	}
	if to <= from {
		return "L" + strconv.Itoa(from)
	}
	return "L" + strconv.Itoa(from) + "–L" + strconv.Itoa(to)
}

// countText is "1 file" / "3 files".
func countText(count int, noun string) string {
	return strconv.Itoa(count) + " " + pluralize(noun, count)
}

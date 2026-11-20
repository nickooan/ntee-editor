package app

import (
	"cmp"
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
}

// WithAgentEdits attaches the edit session registry: edit sessions are
// refused without one.
func (r *ExternalReceiver) WithAgentEdits(registry *staging.Registry) *ExternalReceiver {
	r.agentEdits = registry
	return r
}

// notify tells the UI something changed without waiting for it: the sender
// already has its reply.
func (r *ExternalReceiver) notify(msg any) {
	r.sinkMu.Lock()
	sink := r.sink
	r.sinkMu.Unlock()
	if sink != nil {
		go sink(msg)
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
		return extmsg.BodyReplyWith(content, readDetail(from))
	case extmsg.CommandStaged:
		session, err := r.agentEdits.StagedIn(rel)
		switch {
		case err != nil:
			return extmsg.ErrorReply(err.Error())
		case session == "":
			return extmsg.OkReply("not staged")
		}
		return extmsg.OkReply("staged · edit session " + session)
	case extmsg.CommandWrite:
		if line > 0 {
			return extmsg.ErrorReply("write sets the whole file — leave #L" + strconv.Itoa(line) + " off")
		}
		result, err = r.agentEdits.Write(id, message.Agent, rel, message.Payload)
	case extmsg.CommandReplace:
		payload, decodeErr := extmsg.DecodeReplace(message.Payload)
		if decodeErr != nil {
			return extmsg.ErrorReply(decodeErr.Error())
		}
		result, err = r.agentEdits.Replace(id, message.Agent, rel, staging.Replacement{Old: payload.Old, New: payload.New, All: payload.All, HintLine: line})
	}
	if err != nil {
		return extmsg.ErrorReply(err.Error())
	}
	r.notify(agentEditsChangedMsg{session: id})
	return extmsg.OkReply(agentEditReplyText(message.Command, result, id))
}

// readDetail says where a read's content came from, on its reply's first
// line: "staged · edit session <id> · not written" or "disk".
func readDetail(session string) string {
	if session == "" {
		return "disk"
	}
	return "staged · edit session " + session + " · not written"
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
	if !record.AgentEndedAt.IsZero() {
		state = "ended — waiting for the user to apply or discard"
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

// agentState is an agent's part in a session, for status and the panel.
func agentState(participant store.AgentEditParticipant) string {
	switch {
	case participant.Stopped:
		return "stopped by the user"
	case participant.Editing():
		return "editing"
	}
	return "done"
}

// agentEditPartEndedText answers an end that leaves other agents editing.
func agentEditPartEndedText(record store.AgentEditSession) string {
	editing := record.EditingAgents()
	return "your part of edit session " + record.Session + " is done — waiting for " + countText(len(editing), "other agent") + " (" + staging.AgentNames(editing) +
		") to end before the user reviews it; to hear the outcome run: ntee --msg --msg-edit-session " + record.Session + " wait"
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

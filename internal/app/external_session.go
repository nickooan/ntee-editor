package app

import (
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/store"
)

// externalSession is a guided session an agent runs through the editor: a
// chain of open-with-note steps the user confirmed once. answer is the
// current step's answer — state, not a one-shot message — so a `wait` that
// arrives after the user answered still gets it; waiters are the
// connections blocked on it.
type externalSession struct {
	id      string // "" = no session
	step    int
	answer  string
	waiters []chan<- string
}

// endedSession remembers why a session ended, so its agent is told to stop
// instead of starting it over.
type endedSession struct {
	id, reason string
}

// maxEndedSessions bounds the remembered ended sessions.
const maxEndedSessions = 16

const sessionAnswerNext = "next"

// handleSessionMessage takes a session request: wait and end act on the
// running session; an open is its next step — carried out at once, or, for
// a session not yet running, confirmed first.
func (m Model) handleSessionMessage(msg externalMessageMsg) (tea.Model, tea.Cmd) {
	id := msg.record.Session
	switch msg.record.Command {
	case extmsg.CommandWait:
		return m.waitForSessionAnswer(id, msg.reply), nil
	case extmsg.CommandEnd:
		return m.endSessionByAgent(id, msg.reply), nil
	}

	if m.session.id == "" {
		if reason, ended := m.endedSessionReason(id); ended {
			sendExternalReply(msg.reply, sessionEndedReply(id, reason))
			return m, nil
		}
	} else if m.session.id != id {
		sendExternalReply(msg.reply, extmsg.ErrorReply("another session ("+m.session.id+") is running — end it first: ntee --msg --msg-session "+m.session.id+" end"))
		return m, nil
	}

	pending, problem := m.resolveExternal(msg.record)
	pending.session, pending.sessionReply = id, msg.reply
	if problem != "" {
		sendExternalReply(msg.reply, extmsg.ErrorReply(problem))
		m.errText = "session " + id + ": " + problem
		return m, m.saveExternalStatusCmd(pending, store.ExternalFailed, problem)
	}
	if m.session.id == id {
		return m.carryOutSessionStep(pending)
	}

	// A new session: the user confirms once, even from the plain query view.
	m, cmds := m.supersedePendingExternal()
	if m.choice.kind == choiceRemovePath {
		m.confirmRm, m.confirmRmDir = "", false
		m.notice = "rm cancelled"
	}
	m.external.pending = &pending
	m.choice = m.sessionChoice(pending)
	return m, tea.Batch(cmds...)
}

// sessionChoice is the confirm overlay that starts a session: who asks, the
// first step, and what agreeing allows.
func (m Model) sessionChoice(pending pendingExternal) choiceOverlay {
	choice := choiceOverlay{kind: choiceExternalRequest, title: "Agent session · " + pending.session}
	firstLine, _, _ := strings.Cut(strings.TrimSpace(pending.lineNote), "\n")
	choice.body = append(choice.body,
		overlayLine{text: truncateRunes(strings.TrimLeft(strings.TrimSpace(firstLine), "# "), 56), style: overlayLineStrong},
		overlayLine{text: "first step: " + pending.record.Paths[0]},
		overlayLine{text: "later steps jump files and repos without asking", style: overlayLineHighlight},
		overlayLine{text: "on each step: Enter shows the next · Esc ends the session", style: overlayLineDim},
	)
	if m.openFile != nil && m.edit.dirty {
		choice.body = append(choice.body, overlayLine{text: "unsaved edits in " + m.openRel + " are kept as a draft", style: overlayLineDim})
	}
	choice.choices = []string{"Start session", "Decline"}
	return choice
}

// resolveSessionStart applies the user's answer to a session's confirm.
func (m Model) resolveSessionStart(pending pendingExternal, accept bool) (Model, tea.Cmd) {
	if !accept {
		m = m.rememberEndedSession(pending.session, "was declined by the user")
		sendExternalReply(pending.sessionReply, sessionEndedReply(pending.session, "was declined by the user"))
		m.notice = "session " + pending.session + " declined"
		return m, m.saveExternalStatusCmd(pending, store.ExternalIgnored, "session declined")
	}
	m.session = externalSession{id: pending.session}
	return m.carryOutSessionStep(pending)
}

// carryOutSessionStep shows a step: whatever the user is in is left, the file
// opens (switching repo when needed) with the step's note, and the sender
// waits for the user's Enter or Esc. A step that can't open is reported to
// the agent and the session stays on.
func (m Model) carryOutSessionStep(pending pendingExternal) (Model, tea.Cmd) {
	var cmds []tea.Cmd
	if m.choice.open() {
		if m.choice.kind == choiceRemovePath {
			m.confirmRm, m.confirmRmDir = "", false
			m.notice = "rm cancelled"
		}
		m.choice = choiceOverlay{}
		var superseded []tea.Cmd
		m, superseded = m.supersedePendingExternal()
		cmds = append(cmds, superseded...)
	}
	// An earlier step nobody answered (its sender gave up waiting) is over.
	m = m.notifySessionWaiters("replaced by step " + strconv.Itoa(m.session.step+1))

	next, cmd := m.carryOutExternal(pending)
	cmds = append(cmds, cmd)
	if !next.lineNoteVisible() && !(next.mode == modeDiff && next.openRel == pending.rootRel) {
		outcome := next.externalOutcome(pending)
		if !extmsg.IsErrorReply(outcome) {
			outcome = extmsg.ErrorReply("cannot show the step: " + pending.record.Paths[0])
		}
		sendExternalReply(pending.sessionReply, outcome)
		return next, tea.Batch(cmds...)
	}
	next.session.step++
	next.session.answer = ""
	next.session.waiters = []chan<- string{pending.sessionReply}
	next.lineNote.step = next.session.step
	next.notice = "Enter next step · Esc end session"
	return next, tea.Batch(cmds...)
}

// sessionStepVisible reports whether a session step's box is on screen —
// the file is then read-only and Enter/Esc answer the step.
func (m Model) sessionStepVisible() bool {
	return m.lineNote.session != "" && m.lineNote.session == m.session.id && m.mode == modeEdit && m.lineNoteVisible()
}

// handleSessionStepKey answers a visible step: Enter asks for the next one,
// Esc ends the session. Moving and scrolling pass through (handled=false);
// anything that would edit is swallowed, since the box must stay on its line.
func (m Model) handleSessionStepKey(key string) (Model, bool) {
	switch key {
	case "enter":
		return m.answerSessionStep(), true
	case "esc":
		return m.endSessionByUser(), true
	case "up", "down", "left", "right", "pgup", "pgdown", "home", "end", "shift+up", "shift+down":
		return m, false
	}
	m.notice = "session step is read-only — Enter next · Esc end session"
	return m, true
}

// answerSessionStep is Enter on a step: the agent hears "next", and the box
// closes while it prepares the next step.
func (m Model) answerSessionStep() Model {
	m.session.answer = sessionAnswerNext
	m = m.notifySessionWaiters(sessionAnswerNext)
	m = m.dismissLineNote()
	m.notice = "next step requested — waiting for the agent (session " + m.session.id + ")"
	return m
}

// endSessionByUser is Esc on a step: the agent hears the session is over.
func (m Model) endSessionByUser() Model {
	id := m.session.id
	m = m.endSession("was closed by the user")
	m.notice = "session " + id + " ended"
	return m
}

// endSessionByAgent is the agent's `end`: the session closes like an Esc,
// and the sender hears it finished.
func (m Model) endSessionByAgent(id string, reply chan<- string) Model {
	if m.session.id != id {
		if reason, ended := m.endedSessionReason(id); ended {
			sendExternalReply(reply, sessionEndedReply(id, reason))
		} else {
			sendExternalReply(reply, extmsg.ErrorReply("no session "+id+" is running"))
		}
		return m
	}
	m = m.endSession("was finished by the agent")
	sendExternalReply(reply, extmsg.EndedReply("session "+id+" finished"))
	m.notice = "session " + id + " finished"
	return m
}

// waitForSessionAnswer is the agent's `wait`: the current step's answer at
// once when the user already gave it, otherwise when they do.
func (m Model) waitForSessionAnswer(id string, reply chan<- string) Model {
	switch {
	case m.session.id == id && m.session.answer != "":
		sendExternalReply(reply, m.session.answer)
	case m.session.id == id:
		m.session.waiters = append(slices.Clip(m.session.waiters), reply)
	default:
		if reason, ended := m.endedSessionReason(id); ended {
			sendExternalReply(reply, sessionEndedReply(id, reason))
		} else {
			sendExternalReply(reply, extmsg.ErrorReply("no session "+id+" is running"))
		}
	}
	return m
}

// endSession closes the running session: its waiters hear why, its box
// closes, and its id is remembered so the agent is told to stop.
func (m Model) endSession(reason string) Model {
	id := m.session.id
	m = m.notifySessionWaiters(sessionEndedReply(id, reason))
	if m.lineNote.session == id {
		m = m.dismissLineNote()
	}
	m.session = externalSession{}
	return m.rememberEndedSession(id, reason)
}

// notifySessionWaiters gives every connection waiting on the current step
// its answer. Reply channels hold one answer, so a sender that gave up
// never blocks the UI.
func (m Model) notifySessionWaiters(outcome string) Model {
	for _, waiter := range m.session.waiters {
		select {
		case waiter <- outcome:
		default:
		}
	}
	m.session.waiters = nil
	return m
}

func (m Model) rememberEndedSession(id, reason string) Model {
	ended := make([]endedSession, 0, maxEndedSessions)
	for _, previous := range m.endedSessions {
		if previous.id != id {
			ended = append(ended, previous)
		}
	}
	ended = append(ended, endedSession{id: id, reason: reason})
	if len(ended) > maxEndedSessions {
		ended = ended[len(ended)-maxEndedSessions:]
	}
	m.endedSessions = ended
	return m
}

func (m Model) endedSessionReason(id string) (string, bool) {
	for _, ended := range m.endedSessions {
		if ended.id == id {
			return ended.reason, true
		}
	}
	return "", false
}

func sessionEndedReply(id, reason string) string {
	return extmsg.EndedReply("session " + id + " " + reason + " — stop sending steps")
}

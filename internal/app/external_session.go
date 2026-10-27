package app

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/store"
)

// externalSession is a session an agent runs through the editor, confirmed
// by the user once. A note or diff session is a queue of open-with-note
// steps: the agent sends them as fast as it likes and the user walks them at
// their own pace — Enter next, Backspace back — so moving on never waits for
// the agent. current is the step on screen; len(steps) means the user is
// past the last one, waiting for the agent's next. A review session instead
// grows a live review, one file per request (review_session.go). waiters
// are `wait` requests, answered when the session is over.
type externalSession struct {
	id       string             // "" = no session
	kind     extmsg.SessionKind // what each request shows
	started  bool               // the user accepted it
	steps    []pendingExternal
	current  int
	complete bool // the agent sent end: no more steps will come
	waiters  []chan<- string

	// A review session's files in arrival order (a resent file keeps its
	// place, with its latest note), the base its first request named, and
	// whether its history record was written.
	reviews []reviewRequest
	base    string
	saved   bool
}

// endedSession remembers how a session ended, so its agent is told to stop
// instead of starting it over.
type endedSession struct {
	id, reason string
	finished   bool // the user went through every step
}

// maxEndedSessions bounds the remembered ended sessions.
const maxEndedSessions = 16

// handleSessionMessage takes a session request: an open queues a step (the
// first one asks the user to start the session), end says no more steps
// will come, wait blocks until the session is over.
func (m Model) handleSessionMessage(msg externalMessageMsg) (tea.Model, tea.Cmd) {
	id := msg.record.Session
	switch msg.record.Command {
	case extmsg.CommandWait:
		return m.waitForSessionEnd(id, msg.reply), nil
	case extmsg.CommandEnd:
		return m.completeSession(id, msg.reply)
	}

	if m.session.id == "" {
		if ended, ok := m.endedSessionFor(id); ok {
			sendExternalReply(msg.reply, extmsg.EndedReply("session "+id+" "+ended.reason+" — stop sending steps"))
			return m, nil
		}
	} else if m.session.id != id {
		sendExternalReply(msg.reply, extmsg.ErrorReply("another "+m.session.kindName()+" ("+m.session.id+") is running — end it first: ntee --msg "+m.session.flag()+" "+m.session.id+" end"))
		return m, nil
	} else if m.session.kind != msg.sessionKind {
		sendExternalReply(msg.reply, extmsg.ErrorReply("session "+id+" is a "+m.session.kindName()+" — send its "+m.session.requestsName()+" with "+m.session.flag()))
		return m, nil
	}

	pending, problem := m.resolveExternal(msg.record)
	pending.session, pending.sessionKind = id, msg.sessionKind
	if problem != "" {
		sendExternalReply(msg.reply, extmsg.ErrorReply(problem))
		m.errText = "session " + id + ": " + problem
		return m, m.saveExternalStatusCmd(pending, store.ExternalFailed, problem)
	}

	if m.session.id == id {
		if m.session.complete {
			sendExternalReply(msg.reply, extmsg.ErrorReply("session "+id+" already got end — start a new session for more steps"))
			return m, nil
		}
		if m.session.kind == extmsg.SessionReview {
			if len(msg.record.Args) > 0 && pending.base != m.session.base {
				sendExternalReply(msg.reply, extmsg.ErrorReply("review session "+id+" compares against "+cmp.Or(m.session.base, store.ExternalDefaultBase)+" — leave --msg-base off after the first file"))
				return m, nil
			}
			next, cmd, reply := m.receiveReviewSessionFile(pending.reviews[0])
			sendExternalReply(msg.reply, reply)
			return next, cmd
		}
		m.session.steps = append(slices.Clip(m.session.steps), pending)
		count := len(m.session.steps)
		sendExternalReply(msg.reply, "step "+strconv.Itoa(count)+" queued")
		if m.session.started && m.session.current == count-1 {
			// The user is past the last step, waiting for this one.
			return m.showSessionStep(count - 1)
		}
		return m, nil
	}

	// A new session: the user confirms once, even from the plain query view.
	m, cmds := m.supersedePendingExternal()
	if m.choice.kind == choiceRemovePath {
		m.confirmRm, m.confirmRmDir = "", false
		m.notice = "rm cancelled"
	}
	m.session = externalSession{id: id, kind: msg.sessionKind, steps: []pendingExternal{pending}, base: pending.base}
	m.external.pending = &pending
	m.choice = m.sessionChoice(pending)
	if m.session.kind == extmsg.SessionReview {
		m.session.reviews = []reviewRequest{pending.reviews[0]}
		sendExternalReply(msg.reply, "file 1 queued — waiting for the user to start the review session")
		return m, tea.Batch(cmds...)
	}
	sendExternalReply(msg.reply, "step 1 queued — waiting for the user to start the session")
	return m, tea.Batch(cmds...)
}

// sessionChoice is the confirm overlay that starts a session: who asks, the
// first step, and what agreeing allows.
func (m Model) sessionChoice(pending pendingExternal) choiceOverlay {
	choice := choiceOverlay{kind: choiceExternalRequest, title: "Agent " + externalSession{kind: pending.sessionKind}.kindName() + " · " + pending.session}
	firstLine, _, _ := strings.Cut(strings.TrimSpace(pending.lineNote), "\n")
	if firstLine != "" {
		choice.body = append(choice.body, overlayLine{text: truncateRunes(strings.TrimLeft(strings.TrimSpace(firstLine), "# "), 56), style: overlayLineStrong})
	}
	base := cmp.Or(pending.base, store.ExternalDefaultBase)
	switch pending.sessionKind {
	case extmsg.SessionReview:
		choice.body = append(choice.body,
			overlayLine{text: "first file: " + pending.record.Paths[0]},
			overlayLine{text: "a live review: files keep arriving as the agent works, diffed against " + base, style: overlayLineHighlight},
			overlayLine{text: "Esc in the review asks first, then tells the agent to stop", style: overlayLineDim},
		)
	case extmsg.SessionDiff:
		choice.body = append(choice.body,
			overlayLine{text: "first step: " + pending.record.Paths[0]},
			overlayLine{text: "each step shows the diff, first against " + base, style: overlayLineHighlight},
		)
	default:
		choice.body = append(choice.body, overlayLine{text: "first step: " + pending.record.Paths[0]})
	}
	if pending.sessionKind != extmsg.SessionReview {
		choice.body = append(choice.body,
			overlayLine{text: "steps jump files and repos without asking", style: overlayLineHighlight},
			overlayLine{text: "on each step: Enter next · Backspace back · Esc end", style: overlayLineDim},
		)
	}
	if m.openFile != nil && m.edit.dirty {
		choice.body = append(choice.body, overlayLine{text: "unsaved edits in " + m.openRel + " are kept as a draft", style: overlayLineDim})
	}
	choice.choices = []string{"Start session", "Decline"}
	return choice
}

// resolveSessionStart applies the user's answer to a session's confirm.
func (m Model) resolveSessionStart(pending pendingExternal, accept bool) (Model, tea.Cmd) {
	if m.session.id != pending.session {
		return m, nil // it ended while the confirm was up
	}
	if !accept {
		if m.session.kind == extmsg.SessionReview {
			m, save := m.saveReviewSession(store.ExternalIgnored, "session declined")
			m = m.endSession("was declined by the user", false)
			m.notice = "review session " + pending.session + " declined"
			return m, save
		}
		m = m.endSession("was declined by the user", false)
		m.notice = "session " + pending.session + " declined"
		return m, m.saveExternalStatusCmd(pending, store.ExternalIgnored, "session declined")
	}
	m.session.started = true
	if m.session.kind == extmsg.SessionReview {
		return m.enterReviewSession()
	}
	return m.showSessionStep(0)
}

// showSessionStep puts step index on screen: whatever the user is in is
// left, the file opens (switching repo when needed) with the step's note. A
// step whose file can't open any more is dropped and the next one shows.
func (m Model) showSessionStep(index int) (Model, tea.Cmd) {
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
	for index < len(m.session.steps) {
		step := m.session.steps[index]
		m.session.current = index
		next, cmd := m.carryOutExternal(step)
		cmds = append(cmds, cmd)
		if next.lineNoteVisible() || (next.mode == modeDiff && next.openRel == step.rootRel) {
			next.notice = next.sessionStepHint()
			return next, tea.Batch(cmds...)
		}
		m = next
		m.errText = "session step skipped — cannot open " + step.record.Paths[0]
		m.session.steps = slices.Delete(slices.Clone(m.session.steps), index, index+1)
	}
	return m.waitForNextSessionStep(), tea.Batch(cmds...)
}

// sessionStepVisible reports whether a session step's box is on screen, over
// the file or the diff — the view is then read-only and the keys walk the
// steps.
func (m Model) sessionStepVisible() bool {
	if m.lineNote.session == "" || m.lineNote.session != m.session.id {
		return false
	}
	return (m.mode == modeEdit && m.lineNoteVisible()) || m.lineNoteVisibleInDiff()
}

// diffSessionStepOn reports whether rel is the file of the diff session's
// step on screen: its diff stays even with no changes.
func (m Model) diffSessionStepOn(rel string) bool {
	return m.session.kind == extmsg.SessionDiff && m.lineNote.session != "" && m.lineNote.session == m.session.id && m.lineNote.rel == rel
}

// kindName names the session's kind for the user.
func (session externalSession) kindName() string {
	switch session.kind {
	case extmsg.SessionDiff:
		return "diff session"
	case extmsg.SessionReview:
		return "review session"
	}
	return "session"
}

// requestsName names what the agent sends to the session.
func (session externalSession) requestsName() string {
	if session.kind == extmsg.SessionReview {
		return "files"
	}
	return "steps"
}

// flag is the --msg flag that sends the session's requests.
func (session externalSession) flag() string { return sessionFlag(session.kind) }

// handleSessionStepKey walks a visible step: Enter next, Backspace back,
// Esc ends the session. Moving and scrolling pass through (handled=false);
// anything that would edit is swallowed, since the box must stay on its line.
func (m Model) handleSessionStepKey(key string) (Model, tea.Cmd, bool) {
	switch key {
	case "enter":
		next, cmd := m.nextSessionStep()
		return next, cmd, true
	case "backspace":
		if m.session.current == 0 {
			m.notice = "this is the first step"
			return m, nil, true
		}
		next, cmd := m.showSessionStep(m.session.current - 1)
		return next, cmd, true
	case "esc":
		id := m.session.id
		m = m.endSession("was closed by the user", false)
		m.notice = "session " + id + " ended"
		return m, nil, true
	case "up", "down", "left", "right", "pgup", "pgdown", "home", "end", "shift+up", "shift+down":
		return m, nil, false
	}
	m.notice = "session step is read-only — " + m.sessionStepHint()
	return m, nil, true
}

// nextSessionStep is Enter: the next queued step at once; past the last one
// the tour finishes when the agent said it was complete, otherwise the box
// closes until the agent's next step arrives.
func (m Model) nextSessionStep() (Model, tea.Cmd) {
	next := m.session.current + 1
	switch {
	case next < len(m.session.steps):
		return m.showSessionStep(next)
	case m.session.complete:
		return m.finishSession(), nil
	}
	m.session.current = len(m.session.steps)
	return m.waitForNextSessionStep(), nil
}

// waitForNextSessionStep closes the box while the user is past the last
// queued step; the agent's next step shows as soon as it arrives.
func (m Model) waitForNextSessionStep() Model {
	m.session.current = len(m.session.steps)
	if m.session.complete && m.session.started {
		return m.finishSession()
	}
	if m.lineNote.session == m.session.id {
		m = m.dismissLineNote()
	}
	m.notice = "waiting for the agent's next step (session " + m.session.id + ")"
	return m
}

// finishSession ends a session the user walked to the end.
func (m Model) finishSession() Model {
	id, steps := m.session.id, len(m.session.steps)
	m = m.endSession("was finished — the user went through all "+strconv.Itoa(steps)+" steps", true)
	m.notice = "session " + id + " finished"
	return m
}

// sessionBadge is the status bar's mark of a running session: the step on
// screen of those queued, or that the user waits for the agent.
func (m Model) sessionBadge() string {
	badge := "● " + m.session.kindName() + " " + m.session.id
	switch {
	case !m.session.started:
		return badge + " · waiting to start"
	case m.session.current >= len(m.session.steps):
		return badge + " · waiting for step " + strconv.Itoa(len(m.session.steps)+1)
	}
	return badge + " · step " + strconv.Itoa(m.session.current+1) + "/" + strconv.Itoa(len(m.session.steps))
}

// sessionStepHint names the keys for the step on screen.
func (m Model) sessionStepHint() string {
	parts := []string{"Enter next"}
	if m.session.current == len(m.session.steps)-1 && m.session.complete {
		parts[0] = "Enter finish"
	}
	if m.session.current > 0 {
		parts = append(parts, "Backspace back")
	}
	return strings.Join(append(parts, "Esc end session"), " · ")
}

// completeSession is the agent's end: no more steps will come. The user
// keeps walking the queued ones; Enter on the last finishes the session. A
// review session stays open for the user, and its history record is
// written now (or when the user starts it).
func (m Model) completeSession(id string, reply chan<- string) (Model, tea.Cmd) {
	if m.session.id != id {
		if ended, ok := m.endedSessionFor(id); ok {
			sendExternalReply(reply, ended.reply())
		} else {
			sendExternalReply(reply, extmsg.ErrorReply("no session "+id+" is running"))
		}
		return m, nil
	}
	m.session.complete = true
	if m.session.kind == extmsg.SessionReview {
		sendExternalReply(reply, "session "+id+" complete — the user is reviewing "+strconv.Itoa(len(m.session.reviews))+" files")
		if !m.session.started {
			return m, nil
		}
		m.notice = "the agent finished sending files"
		return m.saveReviewSession(store.ExternalReviewed, "the agent ended the session")
	}
	sendExternalReply(reply, "session "+id+" complete — it ends after step "+strconv.Itoa(len(m.session.steps)))
	if m.session.started && m.session.current >= len(m.session.steps) {
		return m.finishSession(), nil // the user is already past the last step
	}
	return m, nil
}

// waitForSessionEnd is the agent's wait: it is answered when the session is
// over — the user finished it or closed it.
func (m Model) waitForSessionEnd(id string, reply chan<- string) Model {
	if m.session.id == id {
		m.session.waiters = append(slices.Clip(m.session.waiters), reply)
		return m
	}
	if ended, ok := m.endedSessionFor(id); ok {
		sendExternalReply(reply, ended.reply())
	} else {
		sendExternalReply(reply, extmsg.ErrorReply("no session "+id+" is running"))
	}
	return m
}

// endSession closes the running session: its waiters hear how it ended, its
// box closes, and its id is remembered so the agent is told to stop.
func (m Model) endSession(reason string, finished bool) Model {
	ended := endedSession{id: m.session.id, reason: reason, finished: finished}
	for _, waiter := range m.session.waiters {
		// Reply channels hold one answer, so a sender that gave up never
		// blocks the UI.
		select {
		case waiter <- ended.reply():
		default:
		}
	}
	if m.lineNote.session == ended.id {
		m = m.dismissLineNote()
	}
	m.session = externalSession{}
	return m.rememberEndedSession(ended)
}

// reply is what the agent hears about an ended session.
func (ended endedSession) reply() string {
	if ended.finished {
		return "finished — session " + ended.id + " " + ended.reason
	}
	return extmsg.EndedReply("session " + ended.id + " " + ended.reason + " — stop sending steps")
}

func (m Model) rememberEndedSession(ended endedSession) Model {
	remembered := make([]endedSession, 0, maxEndedSessions)
	for _, previous := range m.endedSessions {
		if previous.id != ended.id {
			remembered = append(remembered, previous)
		}
	}
	remembered = append(remembered, ended)
	if len(remembered) > maxEndedSessions {
		remembered = remembered[len(remembered)-maxEndedSessions:]
	}
	m.endedSessions = remembered
	return m
}

func (m Model) endedSessionFor(id string) (endedSession, bool) {
	for _, ended := range m.endedSessions {
		if ended.id == id {
			return ended, true
		}
	}
	return endedSession{}, false
}

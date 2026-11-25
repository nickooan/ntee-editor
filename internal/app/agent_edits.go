package app

import (
	"cmp"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

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
	finished       []store.AgentEditSession // summaries (store.SummarizeAgentEdit)
	finishedLoaded bool
	finishedGen    int // the newest finished-list load; older answers are dropped
	// choiceSession is the session an open apply/discard/ready overlay is
	// about.
	choiceSession string
	// The apply popup's state: whether the unsaved-work step chose to
	// replace that work, and the git follow-up behind each of its choices
	// (parallel to the choices, Cancel left out).
	applyReplaceUnsaved bool
	applyFollowUps      []store.AgentEditGitFollowUp
	// ticking: an agentEditsTickMsg is on its way, so the header's label
	// animates while agents work; frame is its animation step.
	ticking bool
	frame   int
	// lastActivityAt is the newest edit, join, end or check of a session its
	// agents still work on; the label stops animating after
	// agentEditsAnimationIdle without one (a restored session whose agent is
	// gone), and starts again on the next.
	lastActivityAt time.Time
	// lastSeenAt is each session's latest agent call that changed nothing
	// (a read, a sandbox, a status), and waitExpiredAt when its last waiter
	// gave up: both keep the label honest about an agent still there.
	// Replaced, never mutated.
	lastSeenAt    map[string]time.Time
	waitExpiredAt map[string]time.Time
	// typingPause is how long the user must have stopped typing before an
	// agent edit session may move them to its review: a keystroke meant for
	// the editor must never land on the review's a/x/r.
	typingPause time.Duration
}

// defaultTypingPause is agentEditsState.typingPause outside tests.
const defaultTypingPause = 1500 * time.Millisecond

// agentEditsTickInterval paces the header label's animation while agents
// work. Nothing ticks when none does.
const agentEditsTickInterval = 150 * time.Millisecond

// agentEditsTickMsg advances the header label's animation.
type agentEditsTickMsg struct{}

// agentEditsChangesRequestedMsg lands the user's request for changes, saved
// off the UI goroutine: reopened are the agents asked.
type agentEditsChangesRequestedMsg struct {
	session  string
	record   store.AgentEditSession
	reopened []string
	err      error
}

// agentEditsFinishedMsg lands a session that was applied or discarded off
// the UI goroutine. reply is an agent-applies end waiting for the outcome.
type agentEditsFinishedMsg struct {
	session string
	record  store.AgentEditSession
	err     error
	reply   chan<- string
	// replacedDrafts are the drafts (staged rel → root rel) the user chose
	// to replace; each is dropped only once its file was actually written.
	replacedDrafts map[string]string
}

// agentEditsFinishedListMsg lands the log's finished sessions.
type agentEditsFinishedListMsg struct {
	gen      int
	sessions []store.AgentEditSession
	err      error
}

// WithAgentEdits attaches the edit session registry the socket stages into.
func (m Model) WithAgentEdits(registry *staging.Registry) Model {
	m.agentEdits.registry = registry
	m.agentEdits.typingPause = defaultTypingPause
	m = m.refreshAgentEditsPending()
	m.agentEdits.ticking = m.agentsAnimating() // Init starts the tick
	return m
}

// agentsWorking reports whether any pending session's agents are editing
// or checking it.
func (m Model) agentsWorking() bool {
	return len(m.agentEdits.stillEditing) > 0 || len(m.agentEdits.checking) > 0
}

// agentEditsAnimationIdle is how long the header label animates after the
// working sessions' last activity: a redraw ~7 times a second is for agents
// visibly at work, not for one that went quiet hours ago. It is long enough
// for a build or test run between calls, which the editor cannot see.
const agentEditsAnimationIdle = 10 * time.Minute

// agentEditWaitGrace keeps a session counted as awaited for a moment after
// its waiter gave up: the agent's next edit_wait is on its way.
const agentEditWaitGrace = 5 * time.Second

// agentsAnimating reports whether the header label should animate.
func (m Model) agentsAnimating() bool {
	return m.agentsWorkingRecently() || len(m.sessionsAwaitingReview()) > 0
}

func (m Model) agentsWorkingRecently() bool {
	return m.agentsWorking() && m.agentsQuietFor() < agentEditsAnimationIdle
}

func (m Model) agentsQuietFor() time.Duration {
	return time.Since(m.agentEdits.lastActivityAt)
}

// sessionsAwaitingReview are the pending sessions an agent is blocked on in
// edit_wait while no agent edits or checks them: the user's turn.
func (m Model) sessionsAwaitingReview() []string {
	var awaited []string
	for _, session := range m.agentEdits.pending {
		id := session.Session
		if m.agentEdits.stillEditing[id] || m.agentEdits.checking[id] {
			continue
		}
		if len(m.agentEdits.waiters[id]) > 0 || time.Since(m.agentEdits.waitExpiredAt[id]) < agentEditWaitGrace {
			awaited = append(awaited, id)
		}
	}
	return awaited
}

// agentEditsSpinner is the animation frame while agents visibly work, and a
// still mark once they went quiet, so a stopped tick never shows a frozen
// frame.
func (m Model) agentEditsSpinner(animated bool) string {
	if !animated {
		return "·"
	}
	return splashSpinner[m.agentEdits.frame%len(splashSpinner)]
}

// agentsIdleText is " · idle 12m" once the working agents went quiet past
// the animation, else "".
func (m Model) agentsIdleText() string {
	if quiet := m.agentsQuietFor(); quiet >= agentEditsAnimationIdle {
		return fmt.Sprintf(" · idle %dm", int(quiet.Minutes()))
	}
	return ""
}

// agentEditsSeenMsg says an agent called in for the session without
// changing it.
type agentEditsSeenMsg struct {
	session string
}

func (m Model) handleAgentEditsSeen(msg agentEditsSeenMsg) (tea.Model, tea.Cmd) {
	now := time.Now()
	lastSeen := maps.Clone(m.agentEdits.lastSeenAt)
	if lastSeen == nil {
		lastSeen = map[string]time.Time{}
	}
	lastSeen[msg.session] = now
	m.agentEdits.lastSeenAt = lastSeen
	if (m.agentEdits.stillEditing[msg.session] || m.agentEdits.checking[msg.session]) && now.After(m.agentEdits.lastActivityAt) {
		m.agentEdits.lastActivityAt = now
	}
	return m, nil
}

// sessionLastActivity is the newest timestamp the session's agents left.
func sessionLastActivity(session store.AgentEditSession) time.Time {
	latest := session.StartedAt
	later := func(candidate time.Time) {
		if candidate.After(latest) {
			latest = candidate
		}
	}
	for _, participant := range session.Agents {
		later(participant.JoinedAt)
		later(participant.EndedAt)
		later(participant.CheckedAt)
	}
	for _, file := range session.Files {
		if count := len(file.Edits); count > 0 {
			later(file.Edits[count-1].At)
		}
	}
	return latest
}

// withAgentEditsTick starts the header label's animation once agents work,
// one tick chain at a time.
func withAgentEditsTick(model tea.Model, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	m, ok := model.(Model)
	if !ok || m.agentEdits.ticking || !m.agentsAnimating() {
		return model, cmd
	}
	m.agentEdits.ticking = true
	return m, tea.Batch(cmd, agentEditsTick())
}

func agentEditsTick() tea.Cmd {
	return tea.Tick(agentEditsTickInterval, func(time.Time) tea.Msg { return agentEditsTickMsg{} })
}

// handleAgentEditsTick advances the animation, and stops once no agent works.
func (m Model) handleAgentEditsTick() (tea.Model, tea.Cmd) {
	if !m.agentsAnimating() {
		m.agentEdits.ticking = false
		return m, nil
	}
	m.agentEdits.frame++
	return m, agentEditsTick()
}

// refreshAgentEditsPending re-reads the pending sessions for the panel and
// the status bar.
func (m Model) refreshAgentEditsPending() Model {
	if m.agentEdits.registry != nil {
		m.agentEdits.pending = m.agentEdits.registry.Pending()
		m.agentEdits.stagedFiles = m.stagedFilesOf(m.agentEdits.pending)
		m.agentEdits.stillEditing, m.agentEdits.checking = map[string]bool{}, map[string]bool{}
		m.agentEdits.lastActivityAt = time.Time{}
		for _, session := range m.agentEdits.pending {
			switch session.Phase() {
			case store.PhaseEditing:
				m.agentEdits.stillEditing[session.Session] = true
			case store.PhaseFinalising:
				m.agentEdits.checking[session.Session] = true
			default:
				continue
			}
			activity := sessionLastActivity(session)
			if seen := m.agentEdits.lastSeenAt[session.Session]; seen.After(activity) {
				activity = seen
			}
			if activity.After(m.agentEdits.lastActivityAt) {
				m.agentEdits.lastActivityAt = activity
			}
		}
	}
	return m
}

// stagedReadOnly reports whether the open file is read-only because an agent
// edit session staged edits for it: applying would replace whatever the user
// typed. A buffer that was already dirty when the file got staged stays
// editable (applying then asks to replace or skip it), and locks once saved.
func (m Model) stagedReadOnly() (session string, locked bool) {
	if m.openFile == nil || m.edit.dirty {
		return "", false
	}
	session, locked = m.agentEdits.stagedFiles[m.openRel]
	return session, locked
}

// refuseStagedEdit refuses changing a read-only staged file, with why.
func (m Model) refuseStagedEdit(session string) Model {
	m.errText = "read-only — edit session " + session + " has staged edits for this file · click the agent label in the header to review them"
	return m
}

// agentEditsPendingRecord is a pending session from the mirror (no registry
// locks: safe per frame).
func (m Model) agentEditsPendingRecord(id string) (store.AgentEditSession, bool) {
	for _, session := range m.agentEdits.pending {
		if session.Session == id {
			return session, true
		}
	}
	return store.AgentEditSession{}, false
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

func (m Model) loadAgentEditsFinishedCmd() (Model, tea.Cmd) {
	return m.loadAgentEditsFinishedAfter(nil)
}

// loadAgentEditsFinishedAfter is loadAgentEditsFinishedCmd after before (a
// trim), numbered so only the newest load's answer lands.
func (m Model) loadAgentEditsFinishedAfter(before func() error) (Model, tea.Cmd) {
	if m.agentEdits.registry == nil {
		return m, nil
	}
	m.agentEdits.finishedGen++
	return m, loadAgentEditSummariesCmd(m.agentEdits.registry.Log(), m.agentEdits.finishedGen, before)
}

// loadAgentEditSummariesCmd lists the finished sessions' summaries off the
// UI goroutine, after before (a trim) when given.
func loadAgentEditSummariesCmd(log store.AgentEditLogStore, gen int, before func() error) tea.Cmd {
	return func() tea.Msg {
		if before != nil {
			if err := before(); err != nil {
				return agentEditsFinishedListMsg{gen: gen, err: err}
			}
		}
		sessions, err := log.LoadFinishedAgentEditSummaries()
		return agentEditsFinishedListMsg{gen: gen, sessions: sessions, err: err}
	}
}

func (m Model) handleAgentEditsFinishedList(msg agentEditsFinishedListMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.agentEdits.finishedGen {
		return m, nil // a newer load is on its way
	}
	if msg.err != nil {
		m.errText = "agent edits: " + msg.err.Error()
	}
	m.agentEdits.finished, m.agentEdits.finishedLoaded = msg.sessions, true
	m.agentEditsPicker.index = input.Clamp(m.agentEditsPicker.index, 0, m.agentEditsPickerRowCount()-1)
	return m, nil
}

// handleAgentEditsChanged follows the socket side: a staged edit updates the
// pending list and a live review of the session; a session's first edit
// opens its live review when the user reviews first and is free to be
// moved; a session the agent discarded ends for its waiters and leaves the
// screen.
func (m Model) handleAgentEditsChanged(msg agentEditsChangedMsg) (tea.Model, tea.Cmd) {
	before, _ := m.agentEditsPendingRecord(msg.session)
	m = m.refreshAgentEditsPending()
	next, cmd := m.handleAgentEditsChangedRefreshed(msg)
	if after, ok := m.agentEditsPendingRecord(msg.session); ok && len(before.Files) > 0 {
		if added := m.agentEditReposAdded(before, after); len(added) > 0 {
			model := next.(Model)
			model.notice = "✎ edit session " + msg.session + " · now also in " + strings.Join(added, ", ")
			if model.mode == modeReview && model.review.agentEdits == msg.session {
				model.notice += " · {/} jumps between repos"
			}
			return model, cmd
		}
	}
	return next, cmd
}

// agentEditReposAdded are the repos after's files reach that before's did
// not, in after's file order.
func (m Model) agentEditReposAdded(before, after store.AgentEditSession) []string {
	known := map[string]bool{}
	for _, file := range before.Files {
		if name, _, ok := m.agentEditRepoOf(file.Rel); ok {
			known[name] = true
		}
	}
	var added []string
	for _, file := range after.Files {
		if name, _, ok := m.agentEditRepoOf(file.Rel); ok && !known[name] {
			known[name] = true
			added = append(added, name)
		}
	}
	return added
}

func (m Model) handleAgentEditsChangedRefreshed(msg agentEditsChangedMsg) (tea.Model, tea.Cmd) {
	if record, ok := m.agentEdits.registry.Finished(msg.session); ok {
		m = m.agentEditSessionOver(msg.session, record)
		m.notice = "edit session " + msg.session + " " + record.Outcome
		return m.loadAgentEditsFinishedCmd()
	}
	record, ok := m.agentEdits.registry.Snapshot(msg.session)
	if !ok {
		return m, nil
	}
	if m.mode == modeReview && m.review.agentEdits == msg.session {
		return m.refreshAgentEditReview(msg.session)
	}
	if msg.started && msg.policy != store.AgentEditPolicyApply && m.canAutoJumpToAgentReview() {
		m, prepare := m.enterAgentEditReview(record, false)
		m.notice = "✎ agent started editing — watching edit session " + msg.session + " live · Esc back to the editor"
		return m, prepare
	}
	if m.mode != modeReview {
		m.notice = "✎ agent edit session " + msg.session + " · " + countText(len(record.Files), "file") + " staged"
		if notice := m.stagedFileNotice(); notice != "" && m.agentEdits.stagedFiles[m.openRel] == msg.session {
			m.notice = notice
		}
	}
	return m, nil
}

// agentEditsFinishedNotice says the agents finished, with each one's check.
func agentEditsFinishedNotice(record store.AgentEditSession) string {
	notice := "✎ agents finished"
	for _, participant := range record.WithAgents().Agents {
		if mark := map[store.AgentEditCheckResult]string{store.CheckPassed: "✓", store.CheckFailed: "✗", store.CheckSkipped: "–"}[participant.Check]; mark != "" {
			notice += " · " + mark + " " + cmp.Or(participant.Agent, "unnamed") + " " + string(participant.Check)
		}
	}
	return notice
}

// canAutoJumpToAgentReview reports whether an agent edit session may take
// the user to its review without asking: only from the plain edit or query
// view, since leaving anything else (a bar, an overlay, another view) would
// throw away what the user was doing there — and not while they type.
func (m Model) canAutoJumpToAgentReview() bool {
	return (m.mode == modeEdit || m.mode == modeQuery) && !m.overlayOpen() && !m.reviewSessionRunning() && m.agentEdits.registry != nil &&
		time.Since(m.lastInputAt) >= m.agentEdits.typingPause
}

// openWorkingAgentEditReview is a click on the header's agent label: it
// opens the review of the newest session whose agents work — else the
// newest one ready for the user — that is not on screen already.
func (m Model) openWorkingAgentEditReview() (Model, tea.Cmd) {
	onScreen := ""
	if m.mode == modeReview && !m.review.agentEditsTrace {
		onScreen = m.review.agentEdits
	}
	var working, ready string
	for _, session := range m.agentEdits.pending { // oldest first: the newest wins
		switch {
		case session.Session == onScreen:
		case m.agentEdits.stillEditing[session.Session] || m.agentEdits.checking[session.Session]:
			working = session.Session
		default:
			ready = session.Session
		}
	}
	id := cmp.Or(working, ready)
	if id == "" {
		return m, nil
	}
	record, ok := m.agentEdits.registry.Snapshot(id)
	if !ok {
		return m, nil
	}
	return m.enterAgentEditReview(record, false)
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

// startAgentEditRequestChanges asks a ready session's agents for changes:
// the session goes back to editing (saved off the UI goroutine) and the
// waiting agents are told to ask the user in their terminal.
func (m Model) startAgentEditRequestChanges(id string) (Model, tea.Cmd) {
	if m.agentEdits.applying[id] {
		m.errText = "edit session " + id + " is being applied"
		return m, nil
	}
	registry := m.agentEdits.registry
	return m, func() tea.Msg {
		record, reopened, err := registry.RequestChanges(id)
		return agentEditsChangesRequestedMsg{session: id, record: record, reopened: reopened, err: err}
	}
}

// handleAgentEditsChangesRequested tells the waiting agents the user wants
// to talk; the live review stays up, locked again while they work.
func (m Model) handleAgentEditsChangesRequested(msg agentEditsChangesRequestedMsg) (tea.Model, tea.Cmd) {
	m = m.refreshAgentEditsPending()
	if msg.err != nil {
		m.errText = msg.err.Error()
		return m, nil
	}
	waiting := len(m.agentEdits.waiters[msg.session]) > 0
	m = m.answerAgentEditWaiters(msg.session, agentEditChangesRequestedReply(msg.session))
	if m.agentEdits.choiceSession == msg.session && m.choice.open() {
		m.choice = choiceOverlay{}
	}
	if waiting {
		m.notice = "✎ asked " + staging.AgentNames(msg.reopened) + " for changes to edit session " + msg.session + " — switch to the agent's terminal"
	} else {
		m.notice = "✎ changes requested for edit session " + msg.session + " — no agent is waiting now; it hears on its next wait"
	}
	return m.refreshAgentEditReview(msg.session)
}

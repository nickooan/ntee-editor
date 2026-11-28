package app

import (
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/store"
)

// agentEditDetailState is the session details page (Ctrl+T › agent edits ›
// Enter on a session). record is the whole session: a pending one from the
// mirror, kept current as its agents work; a finished one loaded from the
// log, since the panel lists summaries. facts are derived once per record,
// not per frame.
type agentEditDetailState struct {
	session string
	pending bool
	record  store.AgentEditSession
	facts   agentEditFacts
	whole   bool // record holds every edit (not a summary)
	loading bool
	missing bool // trimmed from the log
	err     error
}

// agentEditDetailLoadedMsg lands a finished session's whole record.
type agentEditDetailLoadedMsg struct {
	gen     int
	session string
	record  store.AgentEditSession
	found   bool
	err     error
}

// agentEditFacts is what the details page counts up from a session's edits.
type agentEditFacts struct {
	files, newFiles, edits, added, removed int
	results                                map[store.AgentEditFileResult]int // "" = not settled yet
	builtOn                                []string                          // pending sessions some file was built on
	agents                                 map[string]agentEditAgentFacts    // by agent name
}

// agentEditAgentFacts is one agent's share of a session's edits.
type agentEditAgentFacts struct {
	edits, files, added, removed int
	lastEditAt                   time.Time
}

func agentEditFactsOf(record store.AgentEditSession) agentEditFacts {
	facts := agentEditFacts{files: len(record.Files), results: map[store.AgentEditFileResult]int{}, agents: map[string]agentEditAgentFacts{}}
	for _, file := range record.Files {
		if !file.BaseExists {
			facts.newFiles++
		}
		facts.results[file.Result]++
		if file.BaseSession != "" && !slices.Contains(facts.builtOn, file.BaseSession) {
			facts.builtOn = append(facts.builtOn, file.BaseSession)
		}
		touched := map[string]bool{}
		for _, edit := range file.Edits {
			facts.edits++
			facts.added += edit.Added
			facts.removed += edit.Removed
			agent := facts.agents[edit.Agent]
			agent.edits++
			agent.added += edit.Added
			agent.removed += edit.Removed
			if !touched[edit.Agent] {
				touched[edit.Agent] = true
				agent.files++
			}
			if edit.At.After(agent.lastEditAt) {
				agent.lastEditAt = edit.At
			}
			facts.agents[edit.Agent] = agent
		}
	}
	return facts
}

// withDetailRecord puts record on the page and counts its facts up again.
func (detail agentEditDetailState) withDetailRecord(record store.AgentEditSession, whole bool) agentEditDetailState {
	detail.record, detail.whole = record.WithStoredDefaults(), whole
	detail.facts = agentEditFactsOf(detail.record)
	return detail
}

// openAgentEditDetail shows a panel session's details. A pending one is
// already whole in the mirror; a finished one loads off the UI goroutine.
func (m Model) openAgentEditDetail(record store.AgentEditSession, pending bool) (Model, tea.Cmd) {
	detail := agentEditDetailState{session: record.Session, pending: pending}.withDetailRecord(record, pending)
	m.agentEditsPicker.detail = &detail
	if pending {
		return m, nil
	}
	return m.loadAgentEditDetail()
}

func (m Model) closeAgentEditDetail() Model {
	m.agentEditsPicker.detail = nil
	return m
}

func (m Model) loadAgentEditDetail() (Model, tea.Cmd) {
	detail := *m.agentEditsPicker.detail
	detail.loading = true
	m.agentEditsPicker.detail = &detail
	m.agentEdits.detailGen++
	gen, id, log := m.agentEdits.detailGen, detail.session, m.agentEdits.registry.Log()
	return m, func() tea.Msg {
		record, found, err := log.LoadFinishedAgentEdit(id)
		return agentEditDetailLoadedMsg{gen: gen, session: id, record: record, found: found, err: err}
	}
}

func (m Model) handleAgentEditDetailLoaded(msg agentEditDetailLoadedMsg) (tea.Model, tea.Cmd) {
	current := m.agentEditsPicker.detail
	if msg.gen != m.agentEdits.detailGen || current == nil || current.session != msg.session || current.pending {
		return m, nil
	}
	detail := *current
	detail.loading, detail.err, detail.missing = false, msg.err, msg.err == nil && !msg.found
	if msg.found {
		detail = detail.withDetailRecord(msg.record, true)
	}
	m.agentEditsPicker.detail = &detail
	return m, nil
}

// syncAgentEditDetailWithPending follows the pending mirror: a pending
// session's page shows its latest copy, and one that just finished waits
// for the finished list (syncAgentEditDetailWithFinished).
func (m Model) syncAgentEditDetailWithPending() Model {
	current := m.agentEditsPicker.detail
	if current == nil || !current.pending {
		return m
	}
	detail := *current
	if record, ok := m.agentEditsPendingRecord(detail.session); ok {
		detail = detail.withDetailRecord(record, true)
	} else {
		detail.pending, detail.whole = false, false
	}
	m.agentEditsPicker.detail = &detail
	return m
}

// syncAgentEditDetailWithFinished puts a session that finished while its
// page was open on the page: its summary at once, its whole record once
// loaded.
func (m Model) syncAgentEditDetailWithFinished() (Model, tea.Cmd) {
	current := m.agentEditsPicker.detail
	if current == nil || current.pending || current.whole || current.loading {
		return m, nil
	}
	detail := *current
	for _, summary := range m.agentEdits.finished {
		if summary.Session == detail.session {
			// The summary has no edits: keep the last counts until the load.
			facts := detail.facts
			detail = detail.withDetailRecord(summary, false)
			detail.facts.edits, detail.facts.added, detail.facts.removed, detail.facts.agents = facts.edits, facts.added, facts.removed, facts.agents
		}
	}
	m.agentEditsPicker.detail = &detail
	return m.loadAgentEditDetail()
}

// handleAgentEditDetailKey: Enter opens the session's content review,
// Esc or ← go back to the sessions, and a pending session takes the list's
// a · x · f · r.
func (m Model) handleAgentEditDetailKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	detail := m.agentEditsPicker.detail
	switch msg.String() {
	case "esc", "left":
		return m.closeAgentEditDetail(), nil
	case "enter", "space":
		if !detail.pending && !detail.whole {
			m.notice = "edit session " + detail.session + " is still loading"
			return m, nil
		}
		record := detail.record
		if detail.pending {
			// The live copy: edits may have arrived since the mirror was read.
			if live, ok := m.agentEdits.registry.Snapshot(record.Session); ok {
				record = live
			}
		}
		return m.enterAgentEditReviewOver(record, !detail.pending)
	case "a", "x", "f", "r":
		if !detail.pending {
			return m, nil
		}
		return m.agentEditSessionAction(msg.String(), detail.session)
	}
	return m, nil
}

// agentEditSessionAction is a · x · f · r on a pending session.
func (m Model) agentEditSessionAction(key, id string) (tea.Model, tea.Cmd) {
	switch key {
	case "a":
		return m.askApplyAgentEdits(id), nil
	case "f":
		return m.askForceEndAgentEdits(id), nil
	case "r":
		return m.startAgentEditRequestChanges(id)
	}
	return m.askDiscardAgentEdits(id), nil
}

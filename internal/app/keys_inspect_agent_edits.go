package app

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/store"
)

// agentEditsPickerState is the Ctrl+T agent edits panel once → focuses it.
// index runs over the policy row, the keep row, then the sessions: pending
// first, then finished, newest first. editingKeep makes the keep row a
// number input holding keepInput. detail is the session details page open
// over the list (nil: the list); replaced, never mutated.
type agentEditsPickerState struct {
	focused     bool
	index       int
	editingKeep bool
	keepInput   string
	detail      *agentEditDetailState
}

const (
	agentEditsRowPolicy = iota
	agentEditsRowKeep
	agentEditsRowSessions // the first session row
)

func (m Model) agentEditsPickerRowCount() int {
	return agentEditsRowSessions + len(m.agentEdits.pending) + len(m.agentEdits.finished)
}

// agentEditsSessionAt is the session on panel row index, and whether it is
// pending.
func (m Model) agentEditsSessionAt(index int) (store.AgentEditSession, bool, bool) {
	index -= agentEditsRowSessions
	switch {
	case index < 0:
		return store.AgentEditSession{}, false, false
	case index < len(m.agentEdits.pending):
		return m.agentEdits.pending[index], true, true
	case index-len(m.agentEdits.pending) < len(m.agentEdits.finished):
		return m.agentEdits.finished[index-len(m.agentEdits.pending)], false, true
	}
	return store.AgentEditSession{}, false, false
}

func (m Model) handleAgentEditsPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	picker := &m.agentEditsPicker
	if picker.editingKeep {
		return m.handleAgentEditsKeepKey(msg)
	}
	if picker.detail != nil {
		return m.handleAgentEditDetailKey(msg)
	}
	switch msg.String() {
	case "up":
		picker.index = input.Clamp(picker.index-1, 0, m.agentEditsPickerRowCount()-1)
	case "down":
		picker.index = input.Clamp(picker.index+1, 0, m.agentEditsPickerRowCount()-1)
	case "left", "esc":
		picker.focused = false
	case "enter", "space":
		switch picker.index {
		case agentEditsRowPolicy:
			next := store.AgentEditPolicyApply
			if m.external.settings.AgentEditPolicy == store.AgentEditPolicyApply {
				next = store.AgentEditPolicyReview
			}
			return m.setAgentEditPolicy(next)
		case agentEditsRowKeep:
			picker.editingKeep = true
			picker.keepInput = strconv.Itoa(m.external.settings.AgentEditKeep)
			return m, nil
		}
		record, pending, ok := m.agentEditsSessionAt(picker.index)
		if !ok {
			return m, nil
		}
		return m.openAgentEditDetail(record, pending)
	case "a", "x", "f", "r":
		record, pending, ok := m.agentEditsSessionAt(picker.index)
		if !ok || !pending {
			return m, nil
		}
		return m.agentEditSessionAction(msg.String(), record.Session)
	}
	return m, nil
}

// handleAgentEditsKeepKey edits the keep limit in place: digits only, Enter
// saves within the allowed range, Esc keeps the old value.
func (m Model) handleAgentEditsKeepKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	picker := &m.agentEditsPicker
	switch k := msg.String(); k {
	case "esc":
		picker.editingKeep = false
	case "enter":
		keep, err := strconv.Atoi(picker.keepInput)
		if err != nil || keep < store.MinAgentEditKeep || keep > store.MaxAgentEditKeep {
			m.errText = fmt.Sprintf("sessions kept must be %d–%d", store.MinAgentEditKeep, store.MaxAgentEditKeep)
			return m, nil
		}
		picker.editingKeep = false
		return m.setAgentEditKeep(keep)
	case "backspace":
		if picker.keepInput != "" {
			picker.keepInput = picker.keepInput[:len(picker.keepInput)-1]
		}
	default:
		if text := keyText(msg); len(text) == 1 && text[0] >= '0' && text[0] <= '9' && len(picker.keepInput) < 3 {
			picker.keepInput += text
		}
	}
	return m, nil
}

func (m Model) setAgentEditPolicy(policy string) (Model, tea.Cmd) {
	if !m.external.settingsLoaded {
		m.errText = "external settings are still loading"
		return m, nil
	}
	settings := m.external.settings
	settings.AgentEditPolicy = policy
	notice := "agent edits: you review them before they are applied"
	if policy == store.AgentEditPolicyApply {
		notice = "agent edits: applied at once when the agent ends"
	}
	return m, m.saveExternalSettingsCmd(settings, notice)
}

// setAgentEditKeep saves the keep limit; a lowered one trims the finished
// sessions at once.
func (m Model) setAgentEditKeep(keep int) (Model, tea.Cmd) {
	if !m.external.settingsLoaded {
		m.errText = "external settings are still loading"
		return m, nil
	}
	settings := m.external.settings
	settings.AgentEditKeep = store.ClampAgentEditKeep(keep)
	save := m.saveExternalSettingsCmd(settings, fmt.Sprintf("agent edits: the latest %d finished sessions are kept", settings.AgentEditKeep))
	if m.agentEdits.registry == nil || settings.AgentEditKeep >= m.external.settings.AgentEditKeep {
		return m, save
	}
	log := m.agentEdits.registry.Log()
	m, trim := m.loadAgentEditsFinishedAfter(func() error { return log.TrimAgentEdits(settings.AgentEditKeep) })
	return m, tea.Sequence(save, trim)
}

// inspectAgentEditsCommand handles the bar forms "edits review",
// "edits apply", "edits keep <n>" and "edits discard <session>".
func (m Model) inspectAgentEditsCommand(args string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(args)
	usage := fmt.Sprintf("usage: edits review|apply · edits keep <%d-%d> · edits discard <session>", store.MinAgentEditKeep, store.MaxAgentEditKeep)
	m.inspectMenu = inspectMenuAgentEdits
	clearInput := func(m Model) Model {
		m.inspectInput, m.inspectCursor = "", 0
		return m
	}
	switch {
	case len(fields) == 1 && fields[0] == store.AgentEditPolicyReview:
		return clearInput(m).setAgentEditPolicy(store.AgentEditPolicyReview)
	case len(fields) == 1 && fields[0] == store.AgentEditPolicyApply:
		return clearInput(m).setAgentEditPolicy(store.AgentEditPolicyApply)
	case len(fields) == 2 && fields[0] == "keep":
		keep, err := strconv.Atoi(fields[1])
		if err != nil || keep < store.MinAgentEditKeep || keep > store.MaxAgentEditKeep {
			m.errText = usage
			return m, nil
		}
		return clearInput(m).setAgentEditKeep(keep)
	case len(fields) == 2 && fields[0] == "discard":
		if m.agentEdits.registry == nil {
			m.errText = "this editor takes no agent edits"
			return m, nil
		}
		return clearInput(m).askDiscardAgentEdits(fields[1]), nil
	}
	m.errText = usage
	return m, nil
}

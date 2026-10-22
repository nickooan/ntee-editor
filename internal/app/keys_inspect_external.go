package app

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/store"
)

// externalPickerState is the Ctrl+T external panel once → focuses it. index
// runs over the panel's three settable rows; editingLimit means the history
// limit row is a number input holding limitInput.
type externalPickerState struct {
	focused      bool
	index        int
	editingLimit bool
	limitInput   string
}

const (
	externalRowAccept = iota
	externalRowLimit
	externalRowHistory
	externalRowCount
)

// externalSettingsMsg lands loaded or saved settings. saved names what was
// saved, for the notice ("" for a plain load).
type externalSettingsMsg struct {
	settings store.ExternalSettings
	err      error
	saved    string
}

func (m Model) loadExternalSettingsCmd() tea.Cmd {
	settingsStore := m.external.settingsStore
	if settingsStore == nil {
		return nil
	}
	return func() tea.Msg {
		settings, err := settingsStore.LoadExternalSettings()
		return externalSettingsMsg{settings: settings, err: err}
	}
}

// saveExternalSettingsCmd writes the settings off the UI goroutine. A lowered
// limit trims this project's history at once rather than on the next message.
func (m Model) saveExternalSettingsCmd(settings store.ExternalSettings, saved string) tea.Cmd {
	settingsStore, historyStore := m.external.settingsStore, m.external.historyStore
	trim := settings.HistoryLimit < m.external.settings.HistoryLimit
	if settingsStore == nil {
		return nil
	}
	return func() tea.Msg {
		err := settingsStore.SaveExternalSettings(settings)
		if err == nil && trim && historyStore != nil {
			err = historyStore.TrimExternal(settings.HistoryLimit)
		}
		return externalSettingsMsg{settings: settings, err: err, saved: saved}
	}
}

func (m Model) handleExternalSettings(msg externalSettingsMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.errText = "external settings: " + msg.err.Error()
		return m, nil
	}
	m.external.settings = msg.settings
	m.external.settingsLoaded = true
	if msg.saved == "" {
		return m, nil
	}
	m.notice = msg.saved
	return m.loadExternalHistoryCmd() // a lowered limit may have trimmed it
}

func (m Model) handleExternalPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	picker := &m.externalPicker
	if picker.editingLimit {
		return m.handleExternalLimitKey(msg)
	}
	switch msg.String() {
	case "up":
		picker.index = input.Clamp(picker.index-1, 0, externalRowCount-1)
	case "down":
		picker.index = input.Clamp(picker.index+1, 0, externalRowCount-1)
	case "left", "esc":
		picker.focused = false
	case "enter", "space":
		switch picker.index {
		case externalRowAccept:
			return m.setExternalEnabled(!m.external.settings.Enabled)
		case externalRowLimit:
			picker.editingLimit = true
			picker.limitInput = strconv.Itoa(m.external.settings.HistoryLimit)
		case externalRowHistory:
			return m.enterExternalHistory()
		}
	}
	return m, nil
}

// handleExternalLimitKey edits the history limit in place: digits only,
// Enter saves within the allowed range, Esc keeps the old value.
func (m Model) handleExternalLimitKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	picker := &m.externalPicker
	switch k := msg.String(); k {
	case "esc":
		picker.editingLimit = false
	case "enter":
		limit, err := strconv.Atoi(picker.limitInput)
		if err != nil || limit < store.MinExternalHistoryLimit || limit > store.MaxExternalHistoryLimit {
			m.errText = fmt.Sprintf("history limit must be %d–%d", store.MinExternalHistoryLimit, store.MaxExternalHistoryLimit)
			return m, nil
		}
		picker.editingLimit = false
		return m.setExternalHistoryLimit(limit)
	case "backspace":
		if picker.limitInput != "" {
			picker.limitInput = picker.limitInput[:len(picker.limitInput)-1]
		}
	default:
		if text := keyText(msg); len(text) == 1 && text[0] >= '0' && text[0] <= '9' && len(picker.limitInput) < 3 {
			picker.limitInput += text
		}
	}
	return m, nil
}

func (m Model) setExternalEnabled(enabled bool) (Model, tea.Cmd) {
	if !m.external.settingsLoaded {
		m.errText = "external settings are still loading"
		return m, nil
	}
	settings := m.external.settings
	settings.Enabled = enabled
	notice := "external messages off — senders are told they are disabled"
	if enabled {
		notice = "external messages on"
	}
	return m, m.saveExternalSettingsCmd(settings, notice)
}

func (m Model) setExternalHistoryLimit(limit int) (Model, tea.Cmd) {
	if !m.external.settingsLoaded {
		m.errText = "external settings are still loading"
		return m, nil
	}
	settings := m.external.settings
	settings.HistoryLimit = store.ClampExternalHistoryLimit(limit)
	return m, m.saveExternalSettingsCmd(settings, fmt.Sprintf("external history keeps the latest %d messages", settings.HistoryLimit))
}

// inspectExternalCommand handles the bar forms "ext on", "ext off",
// "ext limit <n>", and "ext history".
func (m Model) inspectExternalCommand(args string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(args)
	usage := fmt.Sprintf("usage: ext on|off · ext limit <%d-%d> · ext history", store.MinExternalHistoryLimit, store.MaxExternalHistoryLimit)
	m.inspectMenu = inspectMenuExternal
	switch {
	case len(fields) == 1 && fields[0] == "on":
		m.inspectInput, m.inspectCursor = "", 0
		return m.setExternalEnabled(true)
	case len(fields) == 1 && fields[0] == "off":
		m.inspectInput, m.inspectCursor = "", 0
		return m.setExternalEnabled(false)
	case len(fields) == 2 && fields[0] == "limit":
		limit, err := strconv.Atoi(fields[1])
		if err != nil || limit < store.MinExternalHistoryLimit || limit > store.MaxExternalHistoryLimit {
			m.errText = usage
			return m, nil
		}
		m.inspectInput, m.inspectCursor = "", 0
		return m.setExternalHistoryLimit(limit)
	case len(fields) == 1 && fields[0] == "history":
		m.inspectInput, m.inspectCursor = "", 0
		return m.enterExternalHistory()
	}
	m.errText = usage
	return m, nil
}

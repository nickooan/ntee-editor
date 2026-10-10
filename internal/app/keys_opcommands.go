package app

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/opcmd"
	"github.com/nickooan/ntee-editor/internal/store"
)

const (
	opFieldKey = iota
	opFieldValue
)

// opTableState is the inspect dashboard's op-commands table. index ranges over
// the saved commands plus one trailing "+ new command" row.
type opTableState struct {
	focused       bool
	index         int
	editing       bool
	field         int // opFieldKey | opFieldValue
	key           string
	value         string
	cursor        int
	originalName  string // "" when the edit creates a new command
	saving        bool
	confirmDelete string
	selectName    string // row to select once the next reload lands
}

type opCommandsLoadedMsg struct {
	gen      int
	commands []store.OpCommand
	err      error
}

type opCommandSavedMsg struct {
	name    string
	deleted bool
	err     error
}

// WithOpCommandStore swaps in the persistent (global) op-command store.
func (m Model) WithOpCommandStore(commandStore store.OpCommandStore) Model {
	m.opStore = commandStore
	return m
}

func (m Model) loadOpCommandsCmd() (Model, tea.Cmd) {
	m.opCommandsGen++
	m.opCommandsLoading = true
	gen, commandStore := m.opCommandsGen, m.opStore
	return m, func() tea.Msg {
		commands, err := commandStore.LoadOpCommands()
		return opCommandsLoadedMsg{gen: gen, commands: commands, err: err}
	}
}

func (m Model) handleOpCommandsLoaded(msg opCommandsLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.opCommandsGen {
		return m, nil
	}
	m.opCommandsLoading = false
	m.opCommandsErr = msg.err
	if msg.err == nil {
		m.opCommands = msg.commands
	}
	if name := m.opTable.selectName; name != "" {
		m.opTable.selectName = ""
		for index, command := range m.opCommands {
			if command.Name == name {
				m.opTable.index = index
			}
		}
	}
	m.opTable.index = input.Clamp(m.opTable.index, 0, len(m.opCommands))
	if m.opMode.open && m.opMode.stage == opStagePick {
		m = m.refreshOpMatches()
	}
	return m, nil
}

func (m Model) handleOpCommandSaved(msg opCommandSavedMsg) (tea.Model, tea.Cmd) {
	m.opTable.saving = false
	if msg.err != nil {
		m.errText = "op-commands: " + msg.err.Error()
		return m, nil
	}
	if msg.deleted {
		m.notice = "deleted " + msg.name
	} else {
		m.notice = "saved " + msg.name
		m.opTable.editing = false
		m.opTable.selectName = msg.name
	}
	return m.loadOpCommandsCmd()
}

// handleOpTableKey drives the focused op-commands table: row navigation,
// delete with confirmation, and the inline key/value editor.
func (m Model) handleOpTableKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.opTable.confirmDelete != "" {
		name := m.opTable.confirmDelete
		switch msg.String() {
		case "y", "enter":
			m.opTable.confirmDelete = ""
			commandStore := m.opStore
			return m, func() tea.Msg {
				return opCommandSavedMsg{name: name, deleted: true, err: commandStore.DeleteOpCommand(name)}
			}
		case "n", "esc":
			m.opTable.confirmDelete = ""
			m.notice = "delete cancelled"
		}
		return m, nil
	}
	if m.opTable.editing {
		return m.handleOpEditKey(msg)
	}
	switch msg.String() {
	case "up":
		m.opTable.index = input.Clamp(m.opTable.index-1, 0, len(m.opCommands))
	case "down":
		m.opTable.index = input.Clamp(m.opTable.index+1, 0, len(m.opCommands))
	case "enter":
		m = m.startOpEdit()
	case "d", "delete":
		if m.opTable.index < len(m.opCommands) {
			m.opTable.confirmDelete = m.opCommands[m.opTable.index].Name
		}
	case "left", "esc":
		m.opTable.focused = false
	}
	return m, nil
}

func (m Model) startOpEdit() Model {
	m.opTable.editing = true
	m.opTable.field = opFieldKey
	m.opTable.key, m.opTable.value, m.opTable.originalName = "", "", ""
	if m.opTable.index < len(m.opCommands) {
		command := m.opCommands[m.opTable.index]
		m.opTable.key, m.opTable.value, m.opTable.originalName = command.Name, command.Command, command.Name
	}
	m.opTable.cursor = len([]rune(m.opTable.key))
	return m
}

func (m Model) handleOpEditKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	field := &m.opTable.key
	if m.opTable.field == opFieldValue {
		field = &m.opTable.value
	}
	switch msg.String() {
	case "esc":
		m.opTable.editing = false
		m.notice = "edit reverted"
	case "ctrl+s":
		return m.saveOpEdit()
	case "tab", "shift+tab":
		m = m.switchOpEditField(1 - m.opTable.field)
	case "enter":
		if m.opTable.field == opFieldKey {
			m = m.switchOpEditField(opFieldValue)
		}
	case "left":
		m.opTable.cursor = input.MoveCursor(*field, m.opTable.cursor, -1)
	case "right":
		m.opTable.cursor = input.MoveCursor(*field, m.opTable.cursor, 1)
	case "home", "ctrl+a":
		m.opTable.cursor = 0
	case "end":
		m.opTable.cursor = len([]rune(*field))
	case "backspace":
		*field, m.opTable.cursor, _ = input.RemoveBeforeCursor(*field, m.opTable.cursor)
	case "space":
		*field, m.opTable.cursor = input.InsertAtCursor(*field, m.opTable.cursor, " ")
	default:
		if text := keyText(msg); text != "" {
			*field, m.opTable.cursor = input.InsertAtCursor(*field, m.opTable.cursor, text)
		}
	}
	return m, nil
}

func (m Model) switchOpEditField(field int) Model {
	m.opTable.field = field
	if field == opFieldValue {
		m.opTable.cursor = len([]rune(m.opTable.value))
	} else {
		m.opTable.cursor = len([]rune(m.opTable.key))
	}
	return m
}

func (m Model) opEditPaste(text string) Model {
	if m.opTable.field == opFieldValue {
		m.opTable.value, m.opTable.cursor = input.InsertAtCursor(m.opTable.value, m.opTable.cursor, pasteLine(text))
	} else {
		m.opTable.key, m.opTable.cursor = input.InsertAtCursor(m.opTable.key, m.opTable.cursor, pasteLine(text))
	}
	return m
}

// saveOpEdit validates the edited row and persists it off the UI goroutine;
// on failure the editor stays open so the input can be corrected.
func (m Model) saveOpEdit() (tea.Model, tea.Cmd) {
	if m.opTable.saving {
		return m, nil
	}
	name := strings.TrimSpace(m.opTable.key)
	command := strings.TrimSpace(m.opTable.value)
	switch {
	case name == "":
		m.errText = "key is empty"
		return m, nil
	case strings.ContainsAny(name, " \t"):
		m.errText = "key must not contain spaces"
		return m, nil
	case command == "":
		m.errText = "value is empty"
		return m, nil
	}
	for _, existing := range m.opCommands {
		if existing.Name == name && name != m.opTable.originalName {
			m.errText = fmt.Sprintf("%q already exists", name)
			return m, nil
		}
	}
	if _, err := opcmd.Parse(command); err != nil {
		m.errText = err.Error()
		return m, nil
	}
	m.opTable.saving = true
	commandStore, previousName := m.opStore, m.opTable.originalName
	record := store.OpCommand{Name: name, Command: command, UpdatedAt: time.Now()}
	return m, func() tea.Msg {
		return opCommandSavedMsg{name: name, err: commandStore.PutOpCommand(previousName, record)}
	}
}

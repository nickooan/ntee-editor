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
	opFieldType
	opFieldValue
	opFieldCount
)

// opTableState is the inspect dashboard's op-commands table. index ranges over
// the saved commands plus one trailing "+ new command" row.
type opTableState struct {
	focused       bool
	index         int
	editing       bool
	field         int // opFieldKey | opFieldType | opFieldValue
	key           string
	fileTypes     string
	value         string
	cursor        int
	originalKey   string // store key of the edited command; "" for a new one
	saving        bool
	confirmDelete string // store key of the command awaiting delete confirmation
	selectKey     string // row to select once the next reload lands
	// Completion in the type and value fields: suggestIndex is the
	// highlighted candidate; Esc (or accepting a type) hides the menu until
	// the text changes.
	suggestIndex  int
	suggestHidden bool
}

// activeField is the text the edit cursor is in.
func (state *opTableState) activeField() *string {
	switch state.field {
	case opFieldType:
		return &state.fileTypes
	case opFieldValue:
		return &state.value
	}
	return &state.key
}

type opCommandsLoadedMsg struct {
	gen      int
	commands []store.OpCommand
	err      error
}

type opCommandSavedMsg struct {
	label   string // for the status notice
	key     string
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
	if selectKey := m.opTable.selectKey; selectKey != "" {
		m.opTable.selectKey = ""
		for index, command := range m.opCommands {
			if command.Key() == selectKey {
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
		m.notice = "deleted " + msg.label
	} else {
		m.notice = "saved " + msg.label
		m.opTable.editing = false
		m.opTable.selectKey = msg.key
	}
	return m.loadOpCommandsCmd()
}

// handleOpTableKey drives the focused op-commands table: row navigation,
// delete with confirmation, and the inline key/value editor.
func (m Model) handleOpTableKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.opTable.confirmDelete != "" {
		deleteKey := m.opTable.confirmDelete
		switch msg.String() {
		case "y", "enter":
			m.opTable.confirmDelete = ""
			commandStore, label := m.opStore, m.opCommandLabel(deleteKey)
			return m, func() tea.Msg {
				return opCommandSavedMsg{label: label, key: deleteKey, deleted: true, err: commandStore.DeleteOpCommand(deleteKey)}
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
			m.opTable.confirmDelete = m.opCommands[m.opTable.index].Key()
		}
	case "left", "esc":
		m.opTable.focused = false
	}
	return m, nil
}

func (m Model) startOpEdit() Model {
	m.opTable.editing = true
	m.opTable.suggestIndex, m.opTable.suggestHidden = 0, false
	m.opTable.field = opFieldKey
	m.opTable.key, m.opTable.fileTypes, m.opTable.value, m.opTable.originalKey = "", "", "", ""
	if m.opTable.index < len(m.opCommands) {
		command := m.opCommands[m.opTable.index]
		m.opTable.key, m.opTable.fileTypes, m.opTable.value = command.Name, command.FileTypes, command.Command
		m.opTable.originalKey = command.Key()
	}
	m.opTable.cursor = len([]rune(m.opTable.key))
	return m
}

// opSuggestions returns the completions for the cursor position — file types
// in the type field, placeholders in the command — and where the fragment
// they replace starts. Types are suggested once a letter is typed, so Tab
// through an empty TYPE never picks one by accident.
func (m Model) opSuggestions() (int, []opcmd.Candidate) {
	if !m.opTable.editing || m.opTable.suggestHidden {
		return 0, nil
	}
	switch m.opTable.field {
	case opFieldType:
		start, candidates := opcmd.CompleteFileType(m.opTable.fileTypes, m.opTable.cursor, m.opPreferredFileType())
		typed := strings.ToLower(strings.TrimPrefix(string([]rune(m.opTable.fileTypes)[start:m.opTable.cursor]), "."))
		if start == m.opTable.cursor || (len(candidates) == 1 && candidates[0].Insert == typed) {
			return 0, nil
		}
		return start, candidates
	case opFieldValue:
		return opcmd.Complete(m.opTable.value, m.opTable.cursor)
	}
	return 0, nil
}

// opPreferredFileType is the open file's type, offered first while typing a TYPE.
func (m Model) opPreferredFileType() string {
	if m.openFile == nil {
		return ""
	}
	return opcmd.FileTypeOf(m.openRel)
}

// acceptOpSuggestion replaces the typed fragment with the candidate. In the
// command it swallows a "}" already typed after the cursor; in the type field
// it closes the menu, so the next Enter moves on instead of re-accepting.
func (m Model) acceptOpSuggestion(start int, candidate opcmd.Candidate) Model {
	field := m.opTable.activeField()
	runes := []rune(*field)
	rest := runes[m.opTable.cursor:]
	if m.opTable.field == opFieldValue && len(rest) > 0 && rest[0] == '}' {
		rest = rest[1:]
	}
	*field = string(runes[:start]) + candidate.Insert + string(rest)
	m.opTable.cursor = start + len([]rune(candidate.Insert))
	m.opTable.suggestIndex = 0
	m.opTable.suggestHidden = m.opTable.field == opFieldType
	return m
}

func (m Model) handleOpEditKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if start, candidates := m.opSuggestions(); len(candidates) > 0 {
		selected := input.Clamp(m.opTable.suggestIndex, 0, len(candidates)-1)
		switch msg.String() {
		case "up":
			m.opTable.suggestIndex = input.Clamp(selected-1, 0, len(candidates)-1)
			return m, nil
		case "down":
			m.opTable.suggestIndex = input.Clamp(selected+1, 0, len(candidates)-1)
			return m, nil
		case "tab", "enter":
			return m.acceptOpSuggestion(start, candidates[selected]), nil
		case "esc":
			m.opTable.suggestHidden = true
			return m, nil
		}
	}
	// Any other key edits or moves: the menu re-derives from the new position.
	m.opTable.suggestIndex, m.opTable.suggestHidden = 0, false

	field := m.opTable.activeField()
	switch msg.String() {
	case "esc":
		m.opTable.editing = false
		m.notice = "edit reverted"
	case "ctrl+s":
		return m.saveOpEdit()
	case "tab":
		m = m.switchOpEditField((m.opTable.field + 1) % opFieldCount)
	case "shift+tab":
		m = m.switchOpEditField((m.opTable.field + opFieldCount - 1) % opFieldCount)
	case "enter":
		if m.opTable.field != opFieldValue {
			m = m.switchOpEditField(m.opTable.field + 1)
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

// opEditConflict finds another saved command the edited row would clash with:
// the same name and overlapping file types. The command's own saved entry is
// not a conflict, and a TYPE that doesn't parse is reported on save instead.
func (m Model) opEditConflict() (store.OpCommand, bool) {
	name := strings.TrimSpace(m.opTable.key)
	fileTypes, err := opcmd.NormalizeFileTypes(m.opTable.fileTypes)
	if name == "" || err != nil {
		return store.OpCommand{}, false
	}
	for _, existing := range m.opCommands {
		if existing.Key() != m.opTable.originalKey && existing.Name == name && opcmd.FileTypesOverlap(existing.FileTypes, fileTypes) {
			return existing, true
		}
	}
	return store.OpCommand{}, false
}

// describeFileTypes names a spec for messages: "go,rb" or "all files".
func describeFileTypes(fileTypes string) string {
	if fileTypes == "" || fileTypes == store.AllFileTypes {
		return "all files"
	}
	return fileTypes
}

// opCommandLabel is how messages name a command: "test (go)", or just the
// name for a "*" command.
func (m Model) opCommandLabel(key string) string {
	for _, command := range m.opCommands {
		if command.Key() == key {
			if command.FileTypes == store.AllFileTypes {
				return command.Name
			}
			return command.Name + " (" + command.FileTypes + ")"
		}
	}
	return key
}

func (m Model) switchOpEditField(field int) Model {
	m.opTable.field = field
	m.opTable.cursor = len([]rune(*m.opTable.activeField()))
	return m
}

func (m Model) opEditPaste(text string) Model {
	field := m.opTable.activeField()
	*field, m.opTable.cursor = input.InsertAtCursor(*field, m.opTable.cursor, pasteLine(text))
	m.opTable.suggestIndex, m.opTable.suggestHidden = 0, false
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
	// An empty TYPE is stored as an explicit "*".
	fileTypes, fileTypesErr := opcmd.NormalizeFileTypes(m.opTable.fileTypes)
	switch {
	case name == "":
		m.errText = "key is empty"
		return m, nil
	case strings.ContainsAny(name, " \t"):
		m.errText = "key must not contain spaces"
		return m, nil
	case strings.Contains(name, "@"):
		m.errText = "key must not contain @"
		return m, nil
	case fileTypesErr != nil:
		m.errText = fileTypesErr.Error()
		return m, nil
	}
	if existing, conflict := m.opEditConflict(); conflict {
		m.messageOverlay = fmt.Sprintf("An op-command named %q already exists for %s — change the key or its file types.",
			name, describeFileTypes(existing.FileTypes))
		return m, nil
	}
	if command == "" {
		m.errText = "value is empty"
		return m, nil
	}
	if _, err := opcmd.Parse(command); err != nil {
		m.errText = err.Error()
		return m, nil
	}
	m.opTable.saving = true
	commandStore, previousKey := m.opStore, m.opTable.originalKey
	record := store.OpCommand{Name: name, FileTypes: fileTypes, Command: command, UpdatedAt: time.Now()}
	label := name
	if fileTypes != store.AllFileTypes {
		label += " (" + fileTypes + ")"
	}
	return m, func() tea.Msg {
		return opCommandSavedMsg{label: label, key: record.Key(), err: commandStore.PutOpCommand(previousKey, record)}
	}
}

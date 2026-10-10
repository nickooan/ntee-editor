package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/store"
)

// settle runs cmd and every follow-up Cmd it triggers, delivering each message
// through Update the way the runtime would.
func settle(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	queue := execCmds(cmd)
	for delivered := 0; len(queue) > 0; delivered++ {
		if delivered > 10000 {
			t.Fatal("message chain did not settle")
		}
		msg := queue[0]
		queue = queue[1:]
		var next tea.Cmd
		m, next = deliver(m, msg)
		queue = append(queue, execCmds(next)...)
	}
	return m
}

// press sends a key and settles whatever async work it started.
func press(t *testing.T, m Model, msg tea.KeyPressMsg) Model {
	t.Helper()
	next, cmd := m.Update(msg)
	return settle(t, next.(Model), cmd)
}

// opCommandsFixture opens the inspect dashboard on the op-commands section
// over a memory op-command store seeded with commands.
func opCommandsFixture(t *testing.T, commands ...store.OpCommand) (Model, *store.MemoryOpCommands) {
	t.Helper()
	commandStore := store.NewMemoryOpCommands()
	for _, command := range commands {
		must(t, commandStore.PutOpCommand("", command))
	}
	m, _ := newTestModel(t, nil)
	m = m.WithOpCommandStore(commandStore)
	m = press(t, m, ctrlKey('t'))
	for m.inspectMenu != inspectMenuOpCommands {
		m = key(m, shiftKey(tea.KeyDown))
	}
	return m, commandStore
}

func loadedCommands(t *testing.T, commandStore store.OpCommandStore) []store.OpCommand {
	t.Helper()
	commands, err := commandStore.LoadOpCommands()
	must(t, err)
	return commands
}

func TestOpCommandsFocusMovesWithArrows(t *testing.T) {
	m, _ := opCommandsFixture(t, store.OpCommand{Name: "build", Command: "make"})
	m = key(m, keyPress(tea.KeyRight))
	if !m.opTable.focused || m.opTable.index != 0 {
		t.Fatalf("→ should focus the table: focused=%v index=%d", m.opTable.focused, m.opTable.index)
	}
	m = key(m, keyPress(tea.KeyDown))
	if m.opTable.index != 1 {
		t.Fatalf("↓ should reach the + new command row, got %d", m.opTable.index)
	}
	m = key(m, keyPress(tea.KeyDown)) // clamped
	if m.opTable.index != 1 {
		t.Fatalf("selection should clamp at the + row, got %d", m.opTable.index)
	}
	m = key(m, keyPress(tea.KeyLeft))
	if m.opTable.focused || m.mode != modeInspect {
		t.Fatalf("← should return focus to the menu: focused=%v mode=%v", m.opTable.focused, m.mode)
	}
	m = key(m, keyPress(tea.KeyRight))
	m = key(m, keyPress(tea.KeyEsc))
	if m.opTable.focused || m.mode != modeInspect {
		t.Fatal("Esc in the table should return focus to the menu, not leave inspect")
	}
	m = key(m, keyPress(tea.KeyEsc))
	if m.mode == modeInspect {
		t.Fatal("Esc from the menu should leave inspect")
	}
}

func TestOpCommandsRightMovesBarCursorBeforeFocusing(t *testing.T) {
	m, _ := opCommandsFixture(t)
	m = runes(m, "ab")
	m = key(m, keyPress(tea.KeyLeft))
	m = key(m, keyPress(tea.KeyRight))
	if m.opTable.focused || m.inspectCursor != 2 {
		t.Fatalf("→ mid-input should move the bar cursor: focused=%v cursor=%d", m.opTable.focused, m.inspectCursor)
	}
	m = key(m, keyPress(tea.KeyRight))
	if !m.opTable.focused {
		t.Fatal("→ at the end of the input should focus the table")
	}
}

func TestOpCommandsRightOnOtherSectionsOnlyMovesCursor(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = press(t, m, ctrlKey('t'))
	m = key(m, keyPress(tea.KeyRight))
	if m.opTable.focused {
		t.Fatal("→ must not focus anything on the ntee-db section")
	}
}

func TestOpCommandsCreateAndSave(t *testing.T) {
	m, commandStore := opCommandsFixture(t)
	m = key(m, keyPress(tea.KeyRight))
	m = key(m, keyPress(tea.KeyEnter))
	if !m.opTable.editing || m.opTable.field != opFieldKey {
		t.Fatalf("Enter on + row should start editing the key: %+v", m.opTable)
	}
	m = runes(m, "plan")
	m = key(m, keyPress(tea.KeyTab))
	m = runes(m, "terraform plan -var-file={$1}")
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "terraform plan") {
		t.Fatalf("editing row should render the value:\n%s", frame)
	}
	m = press(t, m, ctrlKey('s'))
	if m.opTable.editing || m.notice != "saved plan" {
		t.Fatalf("Ctrl+S should save and exit editing: editing=%v notice=%q err=%q", m.opTable.editing, m.notice, m.errText)
	}
	commands := loadedCommands(t, commandStore)
	if len(commands) != 1 || commands[0].Name != "plan" || commands[0].Command != "terraform plan -var-file={$1}" {
		t.Fatalf("store: %+v", commands)
	}
	if len(m.opCommands) != 1 || m.opTable.index != 0 {
		t.Fatalf("table should reload and select the saved row: %+v index=%d", m.opCommands, m.opTable.index)
	}
}

func TestOpCommandsEscRevertsEdit(t *testing.T) {
	m, commandStore := opCommandsFixture(t, store.OpCommand{Name: "build", Command: "make"})
	m = key(m, keyPress(tea.KeyRight))
	m = key(m, keyPress(tea.KeyEnter))
	m = key(m, keyPress(tea.KeyBackspace))
	m = key(m, keyPress(tea.KeyTab))
	m = runes(m, " all")
	m = key(m, keyPress(tea.KeyEsc))
	if m.opTable.editing || !m.opTable.focused {
		t.Fatalf("Esc should stop editing but keep table focus: %+v", m.opTable)
	}
	if commands := loadedCommands(t, commandStore); commands[0].Name != "build" || commands[0].Command != "make" {
		t.Fatalf("store must be untouched: %+v", commands)
	}
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "build") || strings.Contains(frame, "make all") {
		t.Fatalf("table should show the original row:\n%s", frame)
	}
}

func TestOpCommandsRenameReplacesEntry(t *testing.T) {
	m, commandStore := opCommandsFixture(t, store.OpCommand{Name: "build", Command: "make"})
	m = key(m, keyPress(tea.KeyRight))
	m = key(m, keyPress(tea.KeyEnter))
	m = runes(m, "2")
	m = press(t, m, ctrlKey('s'))
	commands := loadedCommands(t, commandStore)
	if len(commands) != 1 || commands[0].Name != "build2" || commands[0].Command != "make" {
		t.Fatalf("rename should replace the old key: %+v", commands)
	}
}

func TestOpCommandsSaveValidation(t *testing.T) {
	cases := []struct {
		name, key, value, wantErr string
	}{
		{"empty key", "", "make", "key is empty"},
		{"spaced key", "my cmd", "make", "key must not contain spaces"},
		{"empty value", "x", "", "value is empty"},
		{"duplicate", "build", "make", `"build" already exists`},
		{"bad template", "x", "echo {$home}", "unknown variable {$home}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, commandStore := opCommandsFixture(t, store.OpCommand{Name: "build", Command: "make"})
			m = key(m, keyPress(tea.KeyRight))
			m = key(m, keyPress(tea.KeyDown))
			m = key(m, keyPress(tea.KeyEnter))
			m = runes(m, tc.key)
			m = key(m, keyPress(tea.KeyTab))
			m = runes(m, tc.value)
			m = press(t, m, ctrlKey('s'))
			if !m.opTable.editing || !strings.Contains(m.errText, tc.wantErr) {
				t.Fatalf("editing=%v errText=%q, want %q", m.opTable.editing, m.errText, tc.wantErr)
			}
			if commands := loadedCommands(t, commandStore); len(commands) != 1 {
				t.Fatalf("nothing should be saved: %+v", commands)
			}
		})
	}
}

func TestOpCommandsDeleteNeedsConfirmation(t *testing.T) {
	m, commandStore := opCommandsFixture(t, store.OpCommand{Name: "build", Command: "make"})
	m = key(m, keyPress(tea.KeyRight))
	m = key(m, typeRune('d'))
	if m.opTable.confirmDelete != "build" {
		t.Fatalf("d should ask for confirmation, got %q", m.opTable.confirmDelete)
	}
	m = key(m, typeRune('n'))
	if m.opTable.confirmDelete != "" || len(loadedCommands(t, commandStore)) != 1 {
		t.Fatal("n should cancel the delete")
	}
	m = key(m, typeRune('d'))
	m = press(t, m, typeRune('y'))
	if len(loadedCommands(t, commandStore)) != 0 || len(m.opCommands) != 0 || m.notice != "deleted build" {
		t.Fatalf("y should delete: %+v notice=%q", m.opCommands, m.notice)
	}
	m = key(m, typeRune('d')) // on the + row: nothing to delete
	if m.opTable.confirmDelete != "" {
		t.Fatal("d on the + row must not ask to delete")
	}
}

func TestOpCommandsPasteIntoEditedField(t *testing.T) {
	m, _ := opCommandsFixture(t)
	m = key(m, keyPress(tea.KeyRight))
	m = key(m, keyPress(tea.KeyEnter))
	next, _ := m.Update(tea.PasteMsg{Content: "deploy"})
	m = next.(Model)
	m = key(m, keyPress(tea.KeyTab))
	next, _ = m.Update(tea.PasteMsg{Content: "kubectl apply\n-f {$1}"})
	m = next.(Model)
	if m.opTable.key != "deploy" || m.opTable.value != "kubectl apply -f {$1}" || m.inspectInput != "" {
		t.Fatalf("paste should land in the edited field: key=%q value=%q bar=%q", m.opTable.key, m.opTable.value, m.inspectInput)
	}
}

func TestOpCommandsSurviveRestart(t *testing.T) {
	m, commandStore := opCommandsFixture(t)
	m = key(m, keyPress(tea.KeyRight))
	m = key(m, keyPress(tea.KeyEnter))
	m = runes(m, "build")
	m = key(m, keyPress(tea.KeyTab))
	m = runes(m, "make")
	_ = press(t, m, ctrlKey('s'))

	restarted, _ := newTestModel(t, nil)
	restarted = restarted.WithOpCommandStore(commandStore)
	restarted = press(t, restarted, ctrlKey('t'))
	if len(restarted.opCommands) != 1 || restarted.opCommands[0].Name != "build" {
		t.Fatalf("a fresh model over the same store should load the command: %+v", restarted.opCommands)
	}
}

func TestOpCommandsStaleLoadIgnored(t *testing.T) {
	m, _ := opCommandsFixture(t, store.OpCommand{Name: "build", Command: "make"})
	m, _ = deliver(m, opCommandsLoadedMsg{gen: m.opCommandsGen - 1, commands: nil})
	if len(m.opCommands) != 1 {
		t.Fatal("a stale load must not replace the list")
	}
}

func TestOpCommandsSidebarClickReleasesTableFocus(t *testing.T) {
	m, _ := opCommandsFixture(t, store.OpCommand{Name: "build", Command: "make"})
	m = key(m, keyPress(tea.KeyRight))
	m = key(m, keyPress(tea.KeyEnter))
	m, _ = m.activateSidebarRow(inspectMenuDB)
	if m.opTable.focused || m.opTable.editing || m.inspectMenu != inspectMenuDB {
		t.Fatalf("clicking another section should drop table focus: %+v menu=%d", m.opTable, m.inspectMenu)
	}
	m = runes(m, "db")
	if m.inspectInput != "db" {
		t.Fatalf("keys should reach the bar again, got %q", m.inspectInput)
	}
}

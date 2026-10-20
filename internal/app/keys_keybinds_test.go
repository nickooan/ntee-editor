package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"gopkg.in/yaml.v3"

	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/keymap"
	"github.com/nickooan/ntee-editor/internal/store"
)

// withKeybinds rebinds a test model as if its user config held raw.
func withKeybinds(t *testing.T, m Model, raw map[string]string) Model {
	t.Helper()
	var warnings []string
	m.keys, warnings = keymap.Resolve(raw)
	if len(warnings) != 0 {
		t.Fatalf("keybinds %v warned: %v", raw, warnings)
	}
	return m
}

// keybindsPanel opens the inspect dashboard on the keybinds section with an
// isolated user config dir, returned for reading what got written.
func keybindsPanel(t *testing.T) (Model, string) {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	m, _ := newTestModel(t, nil)
	m = key(m, ctrlKey('t'))
	for m.inspectMenu != inspectMenuKeybinds {
		m = key(m, shiftKey(tea.KeyDown))
	}
	return m, filepath.Join(xdg, "ntee-editor", "config.yaml")
}

// selectKeybindRow focuses the picker and moves to action's row.
func selectKeybindRow(m Model, action keymap.Action) Model {
	if !m.keybindPicker.focused {
		m = key(m, keyPress(tea.KeyRight))
	}
	for m.keybindPicker.index < int(action) {
		m = key(m, keyPress(tea.KeyDown))
	}
	for m.keybindPicker.index > int(action) {
		m = key(m, keyPress(tea.KeyUp))
	}
	return m
}

func persistedKeybinds(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	var file config.Config
	must(t, yaml.Unmarshal(data, &file))
	return file.Keybinds
}

func TestReboundGlobalChord(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = withKeybinds(t, m, map[string]string{"goto_file": "ctrl+n"})
	if next := key(m, ctrlKey('p')); next.fuzzyOpen {
		t.Fatal("the old Ctrl+P must not open goto after rebinding")
	}
	m = key(m, ctrlKey('n'))
	if !m.fuzzyOpen {
		t.Fatal("Ctrl+N should open goto")
	}
	m = key(m, ctrlKey('n'))
	if m.fuzzyOpen {
		t.Fatal("the rebound key should also close the goto overlay")
	}
}

func TestReboundQuit(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = withKeybinds(t, m, map[string]string{"quit": "alt+q"})
	if _, cmd := m.Update(ctrlKey('q')); cmd != nil {
		t.Fatal("Ctrl+Q must not quit once quit is rebound")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModAlt})
	if cmd == nil {
		t.Fatal("Alt+Q should quit")
	}
	if _, isQuit := cmd().(tea.QuitMsg); !isQuit {
		t.Fatal("Alt+Q should end in tea.QuitMsg")
	}
	if _, cmd := m.Update(ctrlKey('c')); cmd == nil {
		t.Fatal("Ctrl+C always quits")
	}
}

func TestReboundEditActions(t *testing.T) {
	m, root := newTestModel(t, nil)
	m = withKeybinds(t, m, map[string]string{"save": "f5", "undo": "ctrl+y", "redo": "ctrl+z"})
	m = m.openFileAt("main.go")
	m = runes(m, "// hi")
	m = key(m, keyPress(tea.KeySpace))

	m = key(m, ctrlKey('s'))
	if data, _ := os.ReadFile(filepath.Join(root, "main.go")); strings.HasPrefix(string(data), "// hi") {
		t.Fatal("Ctrl+S must not save once save is rebound")
	}
	m = key(m, tea.KeyPressMsg{Code: tea.KeyF5})
	if data, _ := os.ReadFile(filepath.Join(root, "main.go")); !strings.HasPrefix(string(data), "// hi") {
		t.Fatalf("F5 should save: %q", data)
	}
	saved := m.edit.content()
	m = key(m, ctrlKey('y')) // swapped: now undo
	if m.edit.content() == saved {
		t.Fatal("Ctrl+Y should undo after the swap")
	}
	m = key(m, ctrlKey('z')) // now redo
	if m.edit.content() != saved {
		t.Fatal("Ctrl+Z should redo after the swap")
	}
}

func TestReboundSearchAndJumpKeys(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = withKeybinds(t, m, map[string]string{"find": "alt+f", "exec": "alt+e"})
	m = m.openFileAt("main.go")
	if next := key(m, ctrlKey('f')); next.mode == modeSearch {
		t.Fatal("Ctrl+F must not open search once find is rebound")
	}
	m = key(m, tea.KeyPressMsg{Code: 'f', Mod: tea.ModAlt})
	if m.mode != modeSearch {
		t.Fatalf("Alt+F should open search, mode=%v", m.mode)
	}
	m = runes(m, "main")
	m = key(m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModAlt})
	if m.mode != modeSearchExec {
		t.Fatalf("Alt+E should open replace from search, mode=%v", m.mode)
	}
}

func TestKeybindHintsFollowKeymap(t *testing.T) {
	m, _ := newTestModel(t, nil)
	frame := ansi.Strip(m.render())
	if !strings.Contains(frame, "Ctrl+P goto · Ctrl+W repo · Ctrl+Q quit") {
		t.Fatalf("default query hint:\n%s", frame)
	}
	m = m.openFileAt("main.go")
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "Ctrl+ S save, F find, A select, J/O jump/back, Z/Y undo/redo") {
		t.Fatalf("default edit hint keeps the compact form:\n%s", frame)
	}
	m = withKeybinds(t, m, map[string]string{"save": "f5", "goto_file": "ctrl+n"})
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "F5 save · Ctrl+F find") {
		t.Fatalf("a non-Ctrl binding should switch the edit hint to full labels:\n%s", frame)
	}
	m.mode = modeQuery
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "Ctrl+N goto") {
		t.Fatalf("query hint should show the rebound key:\n%s", frame)
	}
}

func TestNewReportsBadKeybinds(t *testing.T) {
	cfg := config.Default()
	cfg.Keybinds = map[string]string{"save": "x", "goto_file": "ctrl+n"}
	m := New(cfg, store.NewMemory(), t.TempDir(), "persistence disabled", nil)
	if !strings.HasPrefix(m.notice, "persistence disabled · keybinds.save") {
		t.Fatalf("notice = %q", m.notice)
	}
	if m.keys.Key(keymap.Save) != "ctrl+s" || m.keys.Key(keymap.GotoFile) != "ctrl+n" {
		t.Fatalf("keys = %v", m.keys.Bindings())
	}
	if clean := New(config.Default(), store.NewMemory(), t.TempDir(), "", nil); clean.notice != "" {
		t.Fatalf("clean config notice = %q", clean.notice)
	}
}

func TestKeybindPaneCaptureAppliesAndPersists(t *testing.T) {
	m, path := keybindsPanel(t)
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "press → to rebind") || !frameHasRow(frame, "goto_file", "Ctrl+P") {
		t.Fatalf("unfocused keybinds panel:\n%s", frame)
	}
	m = selectKeybindRow(m, keymap.GotoFile)
	m = key(m, keyPress(tea.KeyEnter))
	if !m.keybindPicker.capturing {
		t.Fatal("Enter should arm capture")
	}
	if frame := ansi.Strip(m.render()); !frameHasRow(frame, "▸", "goto_file", "press a key…") {
		t.Fatalf("capturing row:\n%s", frame)
	}
	m = key(m, ctrlKey('n'))
	if m.keybindPicker.capturing || m.keys.Key(keymap.GotoFile) != "ctrl+n" {
		t.Fatalf("capture should bind ctrl+n: capturing=%v key=%q err=%q", m.keybindPicker.capturing, m.keys.Key(keymap.GotoFile), m.errText)
	}
	if !strings.Contains(m.notice, "goto_file → ctrl+n (config updated)") {
		t.Fatalf("notice = %q", m.notice)
	}
	persisted := persistedKeybinds(t, path)
	if len(persisted) != len(keymap.Actions()) || persisted["goto_file"] != "ctrl+n" || persisted["save"] != "ctrl+s" {
		t.Fatalf("config should hold the full map: %v", persisted)
	}
	if frame := ansi.Strip(m.render()); !frameHasRow(frame, "goto_file", "Ctrl+N", "default Ctrl+P · goto any") {
		t.Fatalf("rebound row should be marked custom:\n%s", frame)
	}
	// Live: leaving the dashboard, the new chord opens goto.
	m = key(m, keyPress(tea.KeyEsc))
	m = key(m, keyPress(tea.KeyEsc))
	m = key(m, ctrlKey('n'))
	if !m.fuzzyOpen {
		t.Fatal("the captured binding should be live")
	}
}

func TestKeybindCaptureRejectsAndCancels(t *testing.T) {
	m, path := keybindsPanel(t)
	m = selectKeybindRow(m, keymap.Save)
	m = key(m, keyPress(tea.KeyEnter))

	m = key(m, ctrlKey('p'))
	if !strings.Contains(m.errText, "ctrl+p is already bound to goto_file") || !m.keybindPicker.capturing {
		t.Fatalf("a taken key is rejected and capture stays armed: err=%q capturing=%v", m.errText, m.keybindPicker.capturing)
	}
	m = key(m, typeRune('x'))
	if !strings.Contains(m.errText, "not a ctrl/alt chord") || !m.keybindPicker.capturing {
		t.Fatalf("a plain key is rejected: err=%q", m.errText)
	}
	if m.mode != modeInspect || m.inspectInput != "" {
		t.Fatal("captured keys must not leak into the bar")
	}
	m = key(m, keyPress(tea.KeyEsc))
	if m.keybindPicker.capturing || !m.keybindPicker.focused || m.notice != "rebind cancelled" {
		t.Fatalf("Esc cancels capture only: %+v notice=%q", m.keybindPicker, m.notice)
	}
	if m.keys != keymap.Default() {
		t.Fatal("nothing should have changed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a rejected capture must not write the config (stat err = %v)", err)
	}
}

func TestKeybindCaptureTakesGlobalChords(t *testing.T) {
	m, _ := keybindsPanel(t)
	// Free ctrl+q by moving quit, then give it to grep.
	m = selectKeybindRow(m, keymap.Quit)
	m = key(m, keyPress(tea.KeyEnter))
	m = key(m, tea.KeyPressMsg{Code: tea.KeyF10})
	m = selectKeybindRow(m, keymap.Grep)
	m = key(m, keyPress(tea.KeyEnter))
	next, cmd := m.Update(ctrlKey('q'))
	m = next.(Model)
	if cmd != nil || m.keys.Key(keymap.Grep) != "ctrl+q" {
		t.Fatalf("capture should take ctrl+q instead of quitting: grep=%q err=%q", m.keys.Key(keymap.Grep), m.errText)
	}
	m = key(m, keyPress(tea.KeyEnter))
	if _, cmd := m.Update(ctrlKey('c')); cmd == nil {
		t.Fatal("Ctrl+C must still quit while capturing")
	}
}

func TestKeybindResetOne(t *testing.T) {
	m, path := keybindsPanel(t)
	m = withKeybinds(t, m, map[string]string{"goto_file": "ctrl+n", "grep": "ctrl+p"})
	m = selectKeybindRow(m, keymap.GotoFile)
	m = key(m, typeRune('r'))
	if !strings.Contains(m.errText, "ctrl+p is already bound to grep — rebind that first") {
		t.Fatalf("reset onto a taken default should be refused: %q", m.errText)
	}
	m = selectKeybindRow(m, keymap.Grep)
	m = key(m, typeRune('r'))
	if m.keys.Key(keymap.Grep) != "ctrl+g" || !strings.Contains(m.notice, "grep reset to ctrl+g") {
		t.Fatalf("grep reset: key=%q notice=%q err=%q", m.keys.Key(keymap.Grep), m.notice, m.errText)
	}
	m = selectKeybindRow(m, keymap.GotoFile)
	m = key(m, typeRune('r'))
	if m.keys.Key(keymap.GotoFile) != "ctrl+p" {
		t.Fatalf("goto reset: %q err=%q", m.keys.Key(keymap.GotoFile), m.errText)
	}
	if persisted := persistedKeybinds(t, path); persisted["goto_file"] != "ctrl+p" || persisted["grep"] != "ctrl+g" {
		t.Fatalf("persisted = %v", persisted)
	}
	m = key(m, typeRune('r'))
	if !strings.Contains(m.notice, "already the default") {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestKeybindResetAll(t *testing.T) {
	m, path := keybindsPanel(t)
	m = key(m, keyPress(tea.KeyRight))
	m = key(m, shiftRune('r'))
	if m.keybindPicker.confirmReset || m.notice != "keybinds already at defaults" {
		t.Fatalf("nothing to reset: %+v notice=%q", m.keybindPicker, m.notice)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("an empty reset must not write the config")
	}

	m = withKeybinds(t, m, map[string]string{"goto_file": "ctrl+n", "save": "f5"})
	for m.keybindPicker.index < keybindResetRow {
		m = key(m, keyPress(tea.KeyDown))
	}
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "▸ ↺ reset all to defaults") {
		t.Fatalf("reset row should be selectable:\n%s", frame)
	}
	m = key(m, keyPress(tea.KeyEnter))
	if !m.keybindPicker.confirmReset {
		t.Fatal("Enter on the reset row should ask first")
	}
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "Reset all 2 custom bindings?") {
		t.Fatalf("confirmation prompt:\n%s", frame)
	}
	m = key(m, typeRune('n'))
	if m.keybindPicker.confirmReset || m.keys.Key(keymap.Save) != "f5" {
		t.Fatal("n keeps the bindings")
	}
	m = key(m, shiftRune('r'))
	m = key(m, typeRune('y'))
	if m.keys != keymap.Default() || !strings.Contains(m.notice, "keybinds reset to defaults") {
		t.Fatalf("y resets everything: %v notice=%q err=%q", m.keys.Bindings(), m.notice, m.errText)
	}
	persisted := persistedKeybinds(t, path)
	if len(persisted) != len(keymap.Actions()) || persisted["save"] != "ctrl+s" {
		t.Fatalf("the full default map should be written: %v", persisted)
	}
}

func TestKeybindBarCommands(t *testing.T) {
	m, path := keybindsPanel(t)
	m = runes(m, "keybind grep alt+g")
	m = key(m, keyPress(tea.KeyEnter))
	if m.keys.Key(keymap.Grep) != "alt+g" || m.inspectInput != "" {
		t.Fatalf("bind: key=%q input=%q err=%q", m.keys.Key(keymap.Grep), m.inspectInput, m.errText)
	}
	m = runes(m, "keybind save ctrl+g")
	m = key(m, keyPress(tea.KeyEnter))
	if m.keys.Key(keymap.Save) != "ctrl+g" {
		t.Fatalf("grep's freed default should be bindable: %q err=%q", m.keys.Key(keymap.Save), m.errText)
	}
	m = runes(m, "keybind save default")
	m = key(m, keyPress(tea.KeyEnter))
	if m.keys.Key(keymap.Save) != "ctrl+s" {
		t.Fatalf("default: %q err=%q", m.keys.Key(keymap.Save), m.errText)
	}
	m = runes(m, "keybind reset")
	m = key(m, keyPress(tea.KeyEnter))
	if m.keys != keymap.Default() || persistedKeybinds(t, path)["grep"] != "ctrl+g" {
		t.Fatalf("reset: %v err=%q", m.keys.Bindings(), m.errText)
	}
	for _, bad := range []string{"keybind teleport ctrl+n", "keybind save", "keybind save ctrl+p"} {
		m = runes(m, bad)
		m = key(m, keyPress(tea.KeyEnter))
		if m.errText == "" || m.inspectInput != bad {
			t.Fatalf("%q should error and stay in the bar: err=%q input=%q", bad, m.errText, m.inspectInput)
		}
		m.inspectInput, m.inspectCursor = "", 0
	}
}

func TestKeybindPickerReleasedBySidebarClick(t *testing.T) {
	m, _ := keybindsPanel(t)
	m = key(m, keyPress(tea.KeyRight))
	m = key(m, keyPress(tea.KeyEnter))
	m, _ = m.activateSidebarRow(inspectMenuDB)
	if m.keybindPicker.focused || m.keybindPicker.capturing {
		t.Fatal("switching section by click should release the picker and its capture")
	}
}

func TestKeybindsPanelFitsShortTerminal(t *testing.T) {
	m, _ := keybindsPanel(t)
	m.height = 20
	m = key(m, keyPress(tea.KeyRight))
	for m.keybindPicker.index < keybindResetRow {
		m = key(m, keyPress(tea.KeyDown))
	}
	frame := ansi.Strip(m.render())
	if lines := strings.Count(frame, "\n") + 1; lines > m.height {
		t.Fatalf("frame is %d lines for height %d", lines, m.height)
	}
	if !strings.Contains(frame, "reset all to defaults") || !frameHasRow(frame, "exec") {
		t.Fatalf("the window should follow the selection to the bottom:\n%s", frame)
	}
}

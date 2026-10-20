package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/keymap"
)

// keybindPickerState is the keybinds panel's action list once → focuses it.
// index runs over keymap.Actions() plus one trailing "reset all" row.
// capturing means the next key press becomes the highlighted action's key;
// confirmReset is the y/n prompt for resetting every binding.
type keybindPickerState struct {
	focused      bool
	index        int
	capturing    bool
	confirmReset bool
}

// keybindResetRow is the picker index of the "reset all to defaults" row.
var keybindResetRow = len(keymap.Actions())

func (m Model) handleKeybindPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.keybindPicker.confirmReset {
		switch msg.String() {
		case "y", "enter":
			m.keybindPicker.confirmReset = false
			m = m.applyKeymap(keymap.Default(), "keybinds reset to defaults")
		case "n", "esc":
			m.keybindPicker.confirmReset = false
			m.notice = "reset cancelled"
		}
		return m, nil
	}
	switch msg.String() {
	case "up":
		m.keybindPicker.index = input.Clamp(m.keybindPicker.index-1, 0, keybindResetRow)
	case "down":
		m.keybindPicker.index = input.Clamp(m.keybindPicker.index+1, 0, keybindResetRow)
	case "enter":
		if m.keybindPicker.index == keybindResetRow {
			m = m.beginKeybindResetAll()
		} else {
			m.keybindPicker.capturing = true
		}
	case "r":
		if m.keybindPicker.index < keybindResetRow {
			m = m.resetKeybind(keymap.Action(m.keybindPicker.index))
		}
	case "R":
		m = m.beginKeybindResetAll()
	case "left", "esc":
		m.keybindPicker.focused = false
	}
	return m, nil
}

// captureKeybind takes the key pressed while capturing as the highlighted
// action's new binding. Update routes here before any chord fires. A rejected
// key keeps capture armed so the user can try another.
func (m Model) captureKeybind(key string) (tea.Model, tea.Cmd) {
	m.notice, m.errText = "", ""
	if key == "esc" {
		m.keybindPicker.capturing = false
		m.notice = "rebind cancelled"
		return m, nil
	}
	action := keymap.Action(m.keybindPicker.index)
	if m.keys.Matches(action, keymap.Normalize(key)) {
		m.keybindPicker.capturing = false
		m.notice = action.Name() + " is already " + keymap.Label(key)
		return m, nil
	}
	rebound, err := m.keys.With(action, key)
	if err != nil {
		m.errText = err.Error()
		return m, nil
	}
	m.keybindPicker.capturing = false
	return m.applyKeymap(rebound, action.Name()+" → "+rebound.Key(action)), nil
}

// resetKeybind restores one action's default key. It refuses (rather than
// silently swapping) when another action has since taken that default.
func (m Model) resetKeybind(action keymap.Action) Model {
	if !m.keys.IsCustom(action) {
		m.notice = action.Name() + " is already the default " + keymap.Label(action.DefaultKey())
		return m
	}
	restored, err := m.keys.With(action, action.DefaultKey())
	if err != nil {
		m.errText = err.Error() + " — rebind that first"
		return m
	}
	return m.applyKeymap(restored, action.Name()+" reset to "+action.DefaultKey())
}

// beginKeybindResetAll arms the reset-all confirmation, or says there is
// nothing to reset.
func (m Model) beginKeybindResetAll() Model {
	if m.keys.CustomCount() == 0 {
		m.notice = "keybinds already at defaults"
		return m
	}
	m.keybindPicker.index = keybindResetRow
	m.keybindPicker.confirmReset = true
	return m
}

// applyKeymap persists the full binding map first (nothing is half-applied
// when the config write fails), then switches the live keymap.
func (m Model) applyKeymap(next keymap.Keymap, notice string) Model {
	bindings := next.Bindings()
	if _, err := config.SetKeybinds(bindings); err != nil {
		m.errText = "config write failed: " + err.Error()
		return m
	}
	m.keys = next
	// Replaced wholesale, never mutated: cfg is copied by value and the map
	// would otherwise be shared between copies.
	m.cfg.Keybinds = bindings
	m.notice = notice + " (config updated)"
	return m
}

// inspectKeybindCommand handles the bar forms "keybind <action> <key>",
// "keybind <action> default", and "keybind reset" (typing the command out is
// the confirmation, so reset-all runs immediately).
func (m Model) inspectKeybindCommand(args string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(args)
	usage := "usage: keybind <action> <key|default> · keybind reset"
	switch {
	case len(fields) == 1 && fields[0] == "reset":
		if m.keys.CustomCount() == 0 {
			m.notice = "keybinds already at defaults"
		} else {
			m = m.applyKeymap(keymap.Default(), "keybinds reset to defaults")
		}
	case len(fields) == 2:
		action, known := keymap.ActionByName(fields[0])
		if !known {
			m.errText = "unknown action " + fields[0] + " — see the keybinds pane"
			return m, nil
		}
		if fields[1] == "default" {
			m = m.resetKeybind(action)
			break
		}
		rebound, err := m.keys.With(action, fields[1])
		if err != nil {
			m.errText = err.Error()
			return m, nil
		}
		m = m.applyKeymap(rebound, action.Name()+" → "+rebound.Key(action))
	default:
		m.errText = usage
		return m, nil
	}
	m.inspectMenu = inspectMenuKeybinds
	if m.errText == "" {
		m.inspectInput, m.inspectCursor = "", 0
	}
	return m, nil
}

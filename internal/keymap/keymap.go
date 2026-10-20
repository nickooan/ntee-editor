// Package keymap maps the editor's rebindable actions to key chords. The
// effective map is the built-in defaults overlaid with the user config's
// `keybinds:` section; every key is unique across the whole map.
package keymap

import (
	"fmt"
	"sort"
	"strings"
)

// Action is one rebindable editor function.
type Action int

const (
	Quit Action = iota
	RepoPicker
	GotoFile
	Uncommitted
	Grep
	Inspect
	OpMode
	NextTab
	Save
	Undo
	Redo
	Find
	JumpToReference
	JumpBack
	ExpandSelection
	Exec
	actionCount
)

type actionSpec struct {
	name        string
	defaultKey  string
	description string
}

// actionSpecs is indexed by Action and fixes the display order.
var actionSpecs = [actionCount]actionSpec{
	Quit:            {"quit", "ctrl+q", "quit the editor"},
	RepoPicker:      {"repo_picker", "ctrl+w", "switch the nested repo"},
	GotoFile:        {"goto_file", "ctrl+p", "goto anything"},
	Uncommitted:     {"uncommitted", "ctrl+u", "uncommitted files"},
	Grep:            {"grep", "ctrl+g", "search the project"},
	Inspect:         {"inspect", "ctrl+t", "inspection dashboard"},
	OpMode:          {"op_mode", "ctrl+r", "run an op-command"},
	NextTab:         {"next_tab", "shift+tab", "cycle open tabs"},
	Save:            {"save", "ctrl+s", "save the buffer"},
	Undo:            {"undo", "ctrl+z", "undo"},
	Redo:            {"redo", "ctrl+y", "redo"},
	Find:            {"find", "ctrl+f", "find in file / next match"},
	JumpToReference: {"jump_to_reference", "ctrl+j", "jump to definition"},
	JumpBack:        {"jump_back", "ctrl+o", "jump back"},
	ExpandSelection: {"expand_selection", "ctrl+a", "expand the selection"},
	Exec:            {"exec", "ctrl+e", "@exec bar / replace"},
}

// Actions lists every action in display order.
func Actions() []Action {
	actions := make([]Action, actionCount)
	for index := range actions {
		actions[index] = Action(index)
	}
	return actions
}

func (a Action) Name() string        { return actionSpecs[a].name }
func (a Action) DefaultKey() string  { return actionSpecs[a].defaultKey }
func (a Action) Description() string { return actionSpecs[a].description }

// ActionByName looks an action up by its config name.
func ActionByName(name string) (Action, bool) {
	for index, spec := range actionSpecs {
		if spec.name == name {
			return Action(index), true
		}
	}
	return 0, false
}

// Keymap is the effective binding of every action. It is a small value type so
// the app's by-value Model can carry it without sharing.
type Keymap struct {
	keys [actionCount]string
}

// Default returns the built-in bindings.
func Default() Keymap {
	var keymap Keymap
	for index, spec := range actionSpecs {
		keymap.keys[index] = spec.defaultKey
	}
	return keymap
}

// Matches reports whether a key press (tea.KeyPressMsg.String()) triggers
// action. Called per keystroke, so it is a plain compare.
func (k Keymap) Matches(action Action, key string) bool {
	return k.keys[action] == key
}

// ActionFor returns the action bound to a key press, if any. A linear scan
// over a handful of strings: cheaper than a map and allocation-free.
func (k Keymap) ActionFor(key string) (Action, bool) {
	for index, bound := range k.keys {
		if bound == key {
			return Action(index), true
		}
	}
	return 0, false
}

func (k Keymap) Key(action Action) string { return k.keys[action] }

func (k Keymap) Label(action Action) string { return Label(k.keys[action]) }

func (k Keymap) IsCustom(action Action) bool { return k.keys[action] != action.DefaultKey() }

// CustomCount is the number of actions bound to a non-default key.
func (k Keymap) CustomCount() int {
	count := 0
	for _, action := range Actions() {
		if k.IsCustom(action) {
			count++
		}
	}
	return count
}

// Bindings returns every action's key keyed by config name — the full map is
// what gets written to config.yaml, so the file is self-describing.
func (k Keymap) Bindings() map[string]string {
	bindings := make(map[string]string, actionCount)
	for index, spec := range actionSpecs {
		bindings[spec.name] = k.keys[index]
	}
	return bindings
}

// boundTo returns the action holding key, ignoring except.
func (k Keymap) boundTo(key string, except Action) (Action, bool) {
	for index, bound := range k.keys {
		if Action(index) != except && bound == key {
			return Action(index), true
		}
	}
	return 0, false
}

// With returns a copy of the keymap with action bound to key, or an error when
// the key is not bindable or already belongs to another action.
func (k Keymap) With(action Action, key string) (Keymap, error) {
	key = Normalize(key)
	if err := ValidKey(key); err != nil {
		return k, err
	}
	if holder, taken := k.boundTo(key, action); taken {
		return k, fmt.Errorf("%s is already bound to %s", key, holder.Name())
	}
	k.keys[action] = key
	return k, nil
}

// Resolve overlays raw (config name → key) onto the defaults. Unknown actions,
// unbindable keys, and keys that collide with another action are reported as
// warnings and leave the affected action on its default, so a bad config
// entry never takes a key away from the user.
func Resolve(raw map[string]string) (Keymap, []string) {
	keymap := Default()
	var warnings []string
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)
	overridden := map[Action]bool{}
	for _, name := range names {
		action, known := ActionByName(name)
		if !known {
			warnings = append(warnings, "keybinds: unknown action "+name)
			continue
		}
		key := Normalize(raw[name])
		if err := ValidKey(key); err != nil {
			warnings = append(warnings, fmt.Sprintf("keybinds.%s: %s — kept %s", name, err, action.DefaultKey()))
			continue
		}
		keymap.keys[action] = key
		overridden[action] = true
	}
	// Each pass reverts every override caught in a duplicate at once (so no
	// side of a tie wins by order). Defaults never collide, so the override
	// set strictly shrinks and the loop ends; a pure swap has no duplicate.
	for {
		var colliding []Action
		for _, action := range Actions() {
			if !overridden[action] {
				continue
			}
			if holder, taken := keymap.boundTo(keymap.keys[action], action); taken {
				warnings = append(warnings, fmt.Sprintf("keybinds.%s: %s is also bound to %s — kept %s",
					action.Name(), keymap.keys[action], holder.Name(), action.DefaultKey()))
				colliding = append(colliding, action)
			}
		}
		if len(colliding) == 0 {
			return keymap, warnings
		}
		for _, action := range colliding {
			keymap.keys[action] = action.DefaultKey()
			delete(overridden, action)
		}
	}
}

// Normalize trims and lower-cases a configured key so "Ctrl+N" matches the
// "ctrl+n" Bubble Tea reports.
func Normalize(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

// ambiguousChords are reported by legacy terminals as the plain key they share
// a control code with, so a binding on them would never fire there.
var ambiguousChords = map[string]string{
	"ctrl+i": "tab",
	"ctrl+m": "enter",
	"ctrl+[": "esc",
	"ctrl+h": "backspace",
}

// ValidKey reports whether key may carry an action: a ctrl/alt chord, a
// function key, or shift+tab. Plain and shift-only keys are refused because
// binding one would break typing or navigation; ctrl+c stays the fixed quit
// escape hatch.
func ValidKey(key string) error {
	switch {
	case key == "":
		return fmt.Errorf("empty key")
	case key == "ctrl+c":
		return fmt.Errorf("ctrl+c is reserved (always quits)")
	case ambiguousChords[key] != "":
		return fmt.Errorf("%s is indistinguishable from %s in most terminals", key, ambiguousChords[key])
	case key == "shift+tab", isFunctionKey(strings.TrimPrefix(key, "shift+")):
		return nil
	}
	for _, modifier := range []string{"ctrl+", "alt+"} {
		if rest, ok := strings.CutPrefix(key, modifier); ok {
			rest = strings.TrimPrefix(strings.TrimPrefix(rest, "alt+"), "shift+")
			if rest == "" || strings.HasSuffix(rest, "+") {
				return fmt.Errorf("%s has no key after the modifier", key)
			}
			return nil
		}
	}
	return fmt.Errorf("%s is not a ctrl/alt chord or function key", key)
}

func isFunctionKey(key string) bool {
	number, ok := strings.CutPrefix(key, "f")
	if !ok || number == "" || len(number) > 2 {
		return false
	}
	value := 0
	for _, digit := range number {
		if digit < '0' || digit > '9' {
			return false
		}
		value = value*10 + int(digit-'0')
	}
	return value >= 1 && value <= 12
}

// Label renders a key for display: "ctrl+shift+s" → "Ctrl+Shift+S".
func Label(key string) string {
	parts := strings.Split(key, "+")
	for index, part := range parts {
		if part != "" {
			parts[index] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, "+")
}

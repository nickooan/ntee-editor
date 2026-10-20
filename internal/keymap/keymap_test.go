package keymap

import (
	"maps"
	"strings"
	"testing"
)

func TestDefaultBindingsAreUniqueAndValid(t *testing.T) {
	seen := map[string]Action{}
	for _, action := range Actions() {
		key := action.DefaultKey()
		if err := ValidKey(key); err != nil {
			t.Errorf("%s default %q invalid: %v", action.Name(), key, err)
		}
		if previous, dup := seen[key]; dup {
			t.Errorf("%s and %s share default %q", previous.Name(), action.Name(), key)
		}
		seen[key] = action
	}
	if Default().CustomCount() != 0 {
		t.Error("defaults must not count as custom")
	}
}

func TestResolveEmptyIsDefault(t *testing.T) {
	for _, raw := range []map[string]string{nil, {}} {
		keymap, warnings := Resolve(raw)
		if keymap != Default() || len(warnings) != 0 {
			t.Fatalf("Resolve(%v) = %v, %v; want defaults, no warnings", raw, keymap, warnings)
		}
	}
}

func TestResolveFullDefaultMapRoundTrips(t *testing.T) {
	keymap, warnings := Resolve(Default().Bindings())
	if keymap != Default() || len(warnings) != 0 {
		t.Fatalf("round trip = %v, %v", keymap, warnings)
	}
}

func TestResolveOverrides(t *testing.T) {
	keymap, warnings := Resolve(map[string]string{"goto_file": " Ctrl+N ", "save": "f5"})
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if !keymap.Matches(GotoFile, "ctrl+n") || keymap.Matches(GotoFile, "ctrl+p") {
		t.Errorf("goto_file = %q, want ctrl+n", keymap.Key(GotoFile))
	}
	if keymap.Key(Save) != "f5" || !keymap.IsCustom(Save) || keymap.CustomCount() != 2 {
		t.Errorf("save = %q custom=%d", keymap.Key(Save), keymap.CustomCount())
	}
}

func TestResolveSwapIsValid(t *testing.T) {
	keymap, warnings := Resolve(map[string]string{"undo": "ctrl+y", "redo": "ctrl+z"})
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if keymap.Key(Undo) != "ctrl+y" || keymap.Key(Redo) != "ctrl+z" {
		t.Errorf("undo=%q redo=%q", keymap.Key(Undo), keymap.Key(Redo))
	}
}

func TestResolveRejectsBadEntries(t *testing.T) {
	cases := map[string]map[string]string{
		"unknown action":  {"teleport": "ctrl+n"},
		"reserved ctrl+c": {"save": "ctrl+c"},
		"plain letter":    {"save": "a"},
		"enter":           {"save": "enter"},
		"shift letter":    {"save": "shift+a"},
		"ambiguous":       {"save": "ctrl+i"},
		"empty":           {"save": ""},
	}
	for name, raw := range cases {
		keymap, warnings := Resolve(raw)
		if keymap != Default() {
			t.Errorf("%s: keymap changed: %v", name, keymap.Bindings())
		}
		if len(warnings) != 1 {
			t.Errorf("%s: warnings = %v, want one", name, warnings)
		}
	}
}

func TestResolveCollisionWithDefaultReverts(t *testing.T) {
	keymap, warnings := Resolve(map[string]string{"save": "ctrl+p"})
	if keymap.Key(Save) != "ctrl+s" || keymap.Key(GotoFile) != "ctrl+p" {
		t.Fatalf("save=%q goto=%q", keymap.Key(Save), keymap.Key(GotoFile))
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "goto_file") {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestResolveTwoOverridesOnOneKeyBothRevert(t *testing.T) {
	keymap, warnings := Resolve(map[string]string{"save": "ctrl+n", "grep": "ctrl+n"})
	if keymap != Default() {
		t.Fatalf("keymap = %v", keymap.Bindings())
	}
	if len(warnings) != 2 {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestResolveRevertCascade(t *testing.T) {
	// save takes grep's default; grep moved to ctrl+s is fine on its own, but
	// redo also claims ctrl+s, so grep and redo revert, which then makes
	// save collide with grep and revert too.
	keymap, warnings := Resolve(map[string]string{"save": "ctrl+g", "grep": "ctrl+s", "redo": "ctrl+s"})
	if keymap != Default() {
		t.Fatalf("keymap = %v", keymap.Bindings())
	}
	if len(warnings) != 3 {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestWith(t *testing.T) {
	keymap, err := Default().With(GotoFile, "Ctrl+N")
	if err != nil || keymap.Key(GotoFile) != "ctrl+n" {
		t.Fatalf("With = %q, %v", keymap.Key(GotoFile), err)
	}
	if _, err := keymap.With(Save, "ctrl+n"); err == nil || !strings.Contains(err.Error(), "already bound to goto_file") {
		t.Errorf("conflict err = %v", err)
	}
	if _, err := keymap.With(Save, "x"); err == nil {
		t.Error("plain key accepted")
	}
	same, err := keymap.With(GotoFile, "ctrl+n")
	if err != nil || same != keymap {
		t.Errorf("rebinding to its own key: %v", err)
	}
}

func TestValidKey(t *testing.T) {
	valid := []string{"ctrl+n", "alt+x", "ctrl+shift+s", "ctrl+alt+up", "f1", "f12", "shift+f5", "shift+tab", "alt+enter"}
	for _, key := range valid {
		if err := ValidKey(key); err != nil {
			t.Errorf("ValidKey(%q) = %v", key, err)
		}
	}
	invalid := []string{"", "a", "shift+a", "tab", "enter", "esc", "up", "space", "f0", "f13", "fx", "ctrl+", "ctrl+c", "ctrl+m", "ctrl+["}
	for _, key := range invalid {
		if ValidKey(key) == nil {
			t.Errorf("ValidKey(%q) accepted", key)
		}
	}
}

func TestLabel(t *testing.T) {
	cases := map[string]string{
		"ctrl+s":       "Ctrl+S",
		"shift+tab":    "Shift+Tab",
		"ctrl+shift+s": "Ctrl+Shift+S",
		"f5":           "F5",
		"alt+.":        "Alt+.",
	}
	for key, want := range cases {
		if got := Label(key); got != want {
			t.Errorf("Label(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestBindingsListsEveryAction(t *testing.T) {
	bindings := Default().Bindings()
	if len(bindings) != len(Actions()) {
		t.Fatalf("len = %d", len(bindings))
	}
	for _, action := range Actions() {
		if bindings[action.Name()] != action.DefaultKey() {
			t.Errorf("%s = %q", action.Name(), bindings[action.Name()])
		}
		if found, ok := ActionByName(action.Name()); !ok || found != action {
			t.Errorf("ActionByName(%s) = %v, %v", action.Name(), found, ok)
		}
	}
	copied := maps.Clone(bindings)
	copied["save"] = "ctrl+n"
	if Default().Bindings()["save"] != "ctrl+s" {
		t.Error("Bindings must return a fresh map")
	}
}

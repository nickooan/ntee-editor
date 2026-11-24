package app

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestEveryBarEditsWithTheSameKeys(t *testing.T) {
	text, cursor := "jump 12", 7
	for _, step := range []struct {
		key        tea.KeyPressMsg
		wantText   string
		wantCursor int
	}{
		{keyPress(tea.KeyHome), "jump 12", 0},
		{keyPress(tea.KeyDelete), "ump 12", 0},
		{keyPress(tea.KeyEnd), "ump 12", 6},
		{keyPress(tea.KeyBackspace), "ump 1", 5},
		{typeRune('9'), "ump 19", 6},
		{keyPress(tea.KeyLeft), "ump 19", 5},
		{ctrlKey('a'), "ump 19", 0},
		{ctrlKey('e'), "ump 19", 6},
	} {
		text, cursor = editLineInput(text, cursor, step.key)
		if text != step.wantText || cursor != step.wantCursor {
			t.Fatalf("after %q: %q @%d, want %q @%d", step.key.String(), text, cursor, step.wantText, step.wantCursor)
		}
	}

	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m.mode, m.cmdInput, m.cmdCursor = modeCommand, "jump 2", 6
	m = key(m, keyPress(tea.KeyHome))
	m = key(m, keyPress(tea.KeyDelete))
	if m.cmdInput != "ump 2" || m.cmdCursor != 0 {
		t.Fatalf("the command bar takes Home and Delete like every bar: %q @%d", m.cmdInput, m.cmdCursor)
	}
}

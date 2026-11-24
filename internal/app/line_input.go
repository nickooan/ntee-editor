package app

import (
	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/input"
)

// editLineInput applies one editing key to a single-line input: move
// (←/→, Home/End, Ctrl+A/Ctrl+E), delete (Backspace, Delete), or type. Keys
// it doesn't know leave the line and cursor unchanged. Every bar and
// overlay input edits through it, so they all take the same keys; each
// handles its own Enter, Esc and list keys first.
func editLineInput(text string, cursor int, msg tea.KeyPressMsg) (string, int) {
	switch msg.String() {
	case "left":
		return text, input.MoveCursor(text, cursor, -1)
	case "right":
		return text, input.MoveCursor(text, cursor, 1)
	case "home", "ctrl+a":
		return text, 0
	case "end", "ctrl+e":
		return text, len([]rune(text))
	case "backspace":
		text, cursor, _ = input.RemoveBeforeCursor(text, cursor)
		return text, cursor
	case "delete":
		text, cursor, _ = input.RemoveAtCursor(text, cursor)
		return text, cursor
	case "space":
		return input.InsertAtCursor(text, cursor, " ")
	}
	if typed := keyText(msg); typed != "" {
		return input.InsertAtCursor(text, cursor, typed)
	}
	return text, cursor
}

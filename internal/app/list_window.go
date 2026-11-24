package app

import "github.com/nickooan/ntee-editor/internal/input"

// listWindow is the visible slice [start, end) of a count-row list showing
// at most maxRows, scrolled just far enough that the selected row (clamped
// into the list) is the last visible one when it is past the first page.
// The finder, definition picker, grep, op-command and suggestion lists all
// scroll this way.
func listWindow(count, selectedIndex, maxRows int) (selected, start, end int) {
	visible := max(0, min(count, maxRows))
	selected = input.Clamp(selectedIndex, 0, max(0, count-1))
	if visible > 0 && selected >= visible {
		start = selected - visible + 1
	}
	return selected, start, min(start+visible, count)
}

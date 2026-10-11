// Package input holds pure cursor/text-edit helpers. Rune-based so multibyte
// input behaves correctly.
package input

// Clamp constrains v to [lo, hi].
func Clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ClampCursor keeps a cursor within [0, len(runes)].
func ClampCursor(s string, cursor int) int {
	return Clamp(cursor, 0, len([]rune(s)))
}

// MoveCursor moves the cursor by direction (-1 or 1), clamped.
func MoveCursor(s string, cursor, direction int) int {
	return ClampCursor(s, cursor+direction)
}

// InsertAtCursor inserts next at the cursor, returning the new string and cursor.
func InsertAtCursor(current string, cursor int, next string) (string, int) {
	runes := []rune(current)
	at := Clamp(cursor, 0, len(runes))
	out := string(runes[:at]) + next + string(runes[at:])
	return out, at + len([]rune(next))
}

// RemoveBeforeCursor deletes the rune before the cursor (backspace). The bool is
// false when there is nothing to delete (cursor at start).
func RemoveBeforeCursor(s string, cursor int) (string, int, bool) {
	runes := []rune(s)
	at := Clamp(cursor, 0, len(runes))
	if at == 0 {
		return s, at, false
	}
	out := string(runes[:at-1]) + string(runes[at:])
	return out, at - 1, true
}

// RemoveAtCursor deletes the rune under the cursor (forward delete). The bool
// is false when the cursor is at the end and there is nothing to delete.
func RemoveAtCursor(s string, cursor int) (string, int, bool) {
	runes := []rune(s)
	at := Clamp(cursor, 0, len(runes))
	if at == len(runes) {
		return s, at, false
	}
	return string(runes[:at]) + string(runes[at+1:]), at, true
}

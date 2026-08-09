# internal/input

**Introduction**

Tiny pure helpers for cursor-aware text editing, used by the app's command bars (search, command, grep inputs) and by `view`'s viewport clamping. Everything works on runes, not bytes, so multibyte input behaves correctly.

**Architecture**

One file, five functions, no state. Each takes a string plus a cursor and returns the updated pair — the caller owns the input field's state.

**Functions**

### input.go

- `InsertAtCursor(current, cursor, next)` — inserts text at the cursor and returns the new string plus the cursor moved past the insertion. Clamps the cursor first, so a stale cursor can't panic.
- `RemoveBeforeCursor(s, cursor)` — backspace: deletes the rune before the cursor. The bool return is false when the cursor is at the start and there was nothing to delete.

*Plus small helpers: `Clamp` (constrain an int to a range), `ClampCursor` (keep a cursor within the string's rune length), `MoveCursor` (step the cursor by ±1, clamped).*

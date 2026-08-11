# internal/view

**Introduction**

Shared view-layer primitives, used by nearly every mode in `internal/app` and by the `syntax` package. Everything here is pure data-in, data-out: search regex construction, match finding over content, per-line match maps, viewport slicing, and the `HighlightSegment` type the renderers style. No UI framework — these produce data that the Bubble Tea `View` functions turn into terminal output.

**Architecture**

Three small, independent pieces:

- `search.go` — build a regex from a user query, find its matches across content, and bucket them per line for rendering.
- `viewport.go` — slice content into a fixed width×height window at a clamped scroll offset.
- `segment.go` — the `HighlightSegment` struct, the shared currency between syntax highlighting and the renderers.

**Functions**

### search.go

- `CreateSearchRegex(query)` — compiles the query as a case-sensitive regex, falling back to a quoted literal match when the query isn't valid regex. So users can type regex, but a broken pattern still searches for its literal text.
- `CreateMultilineSearchRegex(query)` — the same fallback logic with `(?im)` prepended: case-insensitive, and `^`/`$` match at line boundaries. Needed when a caller matches whole file content in one pass and still wants anchors to behave line-at-a-time.
- `FindSearchMatches(content, query)` — runs the search regex over every line and returns byte-offset `SearchMatch`es, skipping empty matches.
- `BuildMatchesByLine(matches)` — buckets matches into a `map[line][]LineMatch`, tagging each with its global match index (so the renderer can style the focused match differently). Buckets inherit the input's order: `FindSearchMatches` already emits line by line with increasing starts, so no per-bucket sort.

### viewport.go

- `NormalizeLineBreaks(content)` — rewrites CRLF and lone CR to `\n`, the same conversion chroma's `EnsureLF` applies before tokenizing, so buffer line counts and highlight rows agree.
- `NormalizeLines(content)` — `NormalizeLineBreaks` + split on `\n`, so a stray `\r` from any source never reaches the terminal.
- `BuildTerminalViewport(content, width, height, scrollX, scrollY)` — the workhorse. Clamps the scroll offsets against the content's real extents, slices out the visible window rune-by-rune, and pads short lines and missing rows so the result is always exactly width×height. Returns the padded lines plus the max and clamped scroll values so callers can keep their own scroll state honest.

*Plus a small helper: `sliceLine` — cuts one line to the visible column range and right-pads it.*

### segment.go

- `HighlightSegment` — one styled run of text within a line: text, a color string the renderer maps to a lipgloss style ("" = default), and bold/dim/underline/italic/strike flags. The `syntax` package produces these; the app's renderers consume them.

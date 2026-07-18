# ntee-editor

A Sublime-style text editor that lives in the terminal. Bubble Tea TUI, with
project state persisted in [ntee-db](https://github.com/nickooan/ntee-db):
recently opened files, per-file edit snapshots (undo history), and the session
(last file, expanded directories) all survive relaunch.

```
┌ header: project root ─────────────────────────────────┐
│ file tree        │ file content (chroma-highlighted)  │
│ (cursor-driven)  │ line numbers · cursor · selection  │
└───────────────────────────────────────────────────────┘
 status line / : command bar
```

## Run

```sh
go run ./cmd/ntee [project-root]     # defaults to the current directory
```

## Keymap

**Everywhere**
| Key | Action |
|---|---|
| `Ctrl+P` | goto file (fuzzy finder; empty query lists recent files) |
| `Ctrl+G` | search contents across the whole repo — tall overlay: colored preview of the selected hit on top (60%), query + result list (`name.go:LINE — dir`) below; ↑/↓ selects, Enter jumps. (Ctrl+Tab can't reach terminal apps, hence G for "grep".) Editing with unsaved changes asks you to save first. |
| `Ctrl+Q` / `Ctrl+C` | quit (session saved) |

**Query bar (home mode — r1quest-style)**

Type in the bottom `@query >` bar; the sidebar follows live: directories in the
typed path expand, the best match highlights, and a popup suggests
exact/prefix/fuzzy completions (fuzzy reaches into collapsed directories).

| Key | Action |
|---|---|
| type a path / fragment | expand + highlight + suggest |
| `↑/↓` (popup open) | move popup selection — previews in the bar, highlights in the sidebar |
| `↑/↓` (no popup) | scroll the open file |
| `Shift+↑/↓` | walk the sidebar tree row-by-row (never expands) |
| `Enter` | directory → enter it · file → **open straight into edit mode** · `:cmd` → run command |
| `Ctrl+F` | find in the open file |
| `Esc` | go up one directory |
| `Tab` | edit the open file |

**Edit**
| Key | Action |
|---|---|
| `Ctrl+S` | save |
| `Ctrl+Z` / `Ctrl+Y` | undo / redo (snapshot bursts, persisted in ntee-db) |
| `Ctrl+A` | progressive select: word → line |
| `Ctrl+F` | find in file (`Enter` jumps the cursor to the match) |
| `Ctrl+J` | jump to the definition or file path under the cursor — multiple hits open a picker (`name.go:LINE — dir`, ↑/↓ + Enter, with a 5-line colored code preview that follows the selection); **on a definition line it finds all references instead** |
| `Ctrl+O` | jump back (restores file, cursor, and scroll; 20-deep trail) |
| `Esc` | clear selection, then discard unsaved edits and return to the query bar |

**Command bar (`:`)**
`w` save · `q` quit · `e <path>` open · `g <line>` go to line · `revert`
restore last saved snapshot · `recent` recent files.

## Configuration

Defaults ← `~/.config/ntee-editor/config.yaml` ← `<project>/.ntee-editor.yaml`:

```yaml
version: 1
editor: { tab_width: 4, max_snapshots: 50, max_highlight_kb: 512 }
tree:   { ignore: [".git", "node_modules", "dist"] }
theme:  { syntax: "gruvbox" }   # any chroma style name; default is gruvbox dark
languages:
  go:         { extensions: [".go"],         lsp: { command: "gopls" } }
  typescript: { extensions: [".ts", ".tsx"], lsp: { command: "typescript-language-server", args: ["--stdio"] } }
lsp: { enabled: true }    # gopls / typescript-language-server, started lazily
```

## Persistence

Each project gets its own ntee-db store under
`~/.ntee-editor/stores/<hash(project-root)>/`. ntee-db is single-writer
(flock): opening the same project in a second instance falls back to in-memory
state with a status notice (undo works, nothing persists).

## Architecture notes

- `internal/view`, `internal/input`, `internal/filetree`, `internal/fuzzy` are
  pure (no Bubble Tea) and unit-tested; `internal/app` holds the single Model
  with per-mode key handlers.
- Highlighting is whole-buffer chroma tokenization cached per line, refreshed
  at edit-burst boundaries. Grammar colors come from a chroma style — the
  default is a tuned `gruvbox` dark (red keywords/operators, gold
  types/functions, green strings, gray-italic comments, cream text); any
  chroma style name works via `theme.syntax` (`monokai`, `dracula`, …) —
  rendered as truecolor hex, auto-degraded on 256-color terminals. The UI
  chrome is a matching gruvbox palette: `#282828` editor background,
  `#3c3836` cursor-line highlight, `#504945` selections, gold/orange find
  highlights, and a darker `#1d2021` status bar.
- Undo is full-content snapshots keyed `versions:<seq>` with a `file`
  secondary index (`MaxPerValue` auto-evicts the oldest per file).
- The search view re-tokenizes the frozen content so matches overlay syntax
  colors.
- **LSP is live** (`internal/lsp`): a hand-rolled stdio JSON-RPC client
  (Content-Length framing, ordered notifications) starts one server per
  language lazily on first file open — `gopls` for Go,
  `typescript-language-server` for TS (both resolvable from PATH, `~/go/bin`,
  or an absolute `command` in config; `init:` passes initializationOptions,
  e.g. `tsserver.path` for a globally installed typescript ≤5.x — the TS 7
  native preview has no `lib/tsserver.js` and won't work). Diagnostics render
  as colored `●` gutter markers + `✗N ⚠M` status counts + the cursor line's
  message in edit mode. `Ctrl+J` asks the server for
  `textDocument/definition` / `references` (UTF-16 columns bridged both
  ways). **LSP-strict**: for languages with a configured server, its answer
  is final ("still starting…" / "no definition found" rather than a guessed
  jump); the regex heuristic only serves file types with no server at all. A
  missing binary degrades to a one-time notice with everything else
  unchanged.

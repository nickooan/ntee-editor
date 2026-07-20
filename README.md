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

## Install

One command (macOS or Linux) — checks Go (installs it via brew/apt/dnf/pacman if
missing), clones and builds the editor to `~/go/bin/ntee`, then installs the
language servers for every supported language whose runtime is present:

```sh
curl -fsSL https://raw.githubusercontent.com/nickooan/ntee-editor/main/install.sh | bash
```

From a local checkout the same script builds in place:

```sh
./install.sh
```

**Updating** is the same command — re-running pulls the latest source
(`git pull --ff-only`) and rebuilds.

Make sure `~/go/bin` is on your PATH (the script prints the exact `export` line
if it isn't), then use the editor globally:

```sh
ntee <path>        # open a project (defaults to the current directory)
```

Knobs: `NTEE_INSTALL_DIR=<dir>` overrides the clone destination
(default `~/.ntee-editor/src`); `NTEE_SKIP_LSP=1` skips the language-server
step (run `ntee --prepare-lsp` later).

## Run

```sh
ntee [project-root]                  # installed binary
go run ./cmd/ntee [project-root]     # from a checkout; defaults to the current directory
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
| `Ctrl+S` | save (also clears the file's stashed draft) |
| `Ctrl+Z` / `Ctrl+Y` | undo / redo (snapshot bursts, persisted in ntee-db) |
| `Ctrl+A` | progressive select: word → line; then `Shift+↑/↓` extends the selection by whole lines |
| `Ctrl+E` | open the `@exec >` editor-command bar: `copy [a-b\|all\|fpath]` · `jump <line\|top\|end>` (aliases `cp`, `jp`; jump lands ~30% from the top) · `tab <name\|cl\|cr>` |
| `Ctrl+F` | find in file (`Enter` jumps the cursor to the match) |
| `Ctrl+J` | jump to the file path under the cursor, or ask the language server for the definition — multiple hits open a picker (`name.go:LINE — dir`, ↑/↓ + Enter, with a 5-line colored code preview that follows the selection); **on a definition line it finds all references instead**. File types with no configured server report "no language server" |
| `Ctrl+O` | jump back (restores file, cursor, and scroll; 20-deep trail) |
| `Shift+Tab` | cycle the focused tab left→right (wraps) |
| `PgUp` / `PgDn` | page with a one-line overlap (the old edge line carries over) |
| `Esc` | clear selection, then discard unsaved edits (deletes the stashed draft) and return to the query bar |

**Tabs & drafts** — every opened file becomes a tab at the top of the file pane
(base filename; **red = unsaved**). Switching away from a dirty buffer stashes a
draft in ntee-db (content + up to 15 undo steps); reopening the tab — or
relaunching the editor — restores it, with undo stepping back to the on-disk
version. `Ctrl+S` saves and drops the draft; `Esc` discards it. The tab list and
active tab persist per project.

**Command bar (`:`)**
`jump <line|top|end>` go to line (alias `jp`) · `tab <name|cl|cr>` switch to a
tab / close-left / close-right (unsaved tabs refuse to close) · `revert`
restore last saved snapshot · `recent` recent files.

## Configuration

Defaults ← `~/.config/ntee-editor/config.yaml` ← `<project>/.ntee-editor.yaml`:

```yaml
version: 1
editor: { tab_width: 4, max_snapshots: 50, max_highlight_kb: 512 }
tree:   { ignore: [".git"] }    # only .git is hidden; .gitignore'd paths show grayed
theme:  { syntax: "gruvbox" }   # any chroma style name; default is gruvbox dark
languages:
  go:         { extensions: [".go"], lsp: { command: "gopls" } }
  # tsserver also handles JS; a config's `extensions` are UNIONED with these defaults.
  typescript: { extensions: [".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"], lsp: { command: "typescript-language-server", args: ["--stdio"] } }
lsp: { enabled: true }    # gopls / typescript-language-server, started lazily
```

`languages.<name>.extensions` extend (union with) the built-in defaults rather than
replacing them, so you can add a file type to an existing server without re-listing the
defaults; `command`/`args`/`init` overlay the default when set. Each language also takes an
`enable: true|false` toggle (omitted = on) — `typescript: { enable: false }` turns a server
off without deleting its config.

### Installing language servers

`ntee-editor --prepare-lsp` installs the servers for the built-in recipes — **go** (gopls),
**typescript/js** (typescript-language-server), **java** (jdtls), **kotlin**
(kotlin-language-server), **ruby** (ruby-lsp), **python** (pyright), **vue**
(@vue/language-server) — using the platform's native tool (`brew` / `go install` / `npm` /
`gem`), then writes the resolved commands into `~/.config/ntee-editor/config.yaml`. It prints a
plan and asks before running installers (`--yes` skips the prompt); it **adds only missing
languages** (your tuned entries are kept, and the old file is backed up to `config.yaml.bak`),
and skips servers whose runtime (Node/JDK/Ruby/Go) is absent, telling you what to install.
Recipes can be overridden per language via an `install:` block in the config.

### Toggling LSP from the command line

```sh
ntee --disable-lsp typescript vue   # write enable: false for these languages
ntee --disable-lsp all              # turn LSP off globally (lsp.enabled: false)
ntee --enable-lsp typescript        # flip a language (or 'all') back on
```

Both write to `~/.config/ntee-editor/config.yaml` (previous file backed up to
`config.yaml.bak`; per-language tuning like a custom `command` is kept — only the
`enable` flag changes). Unknown names error with the list of known languages.

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
  jump); file types with no configured server report "no language server"
  instead of guessing (a bare file path under the cursor still opens directly).
  A missing binary degrades to a one-time notice with everything else
  unchanged.

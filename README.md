# ntee-editor

A Sublime-style code editor that lives in your terminal. Built on Bubble Tea,
powered by [ntee-db](https://github.com/nickooan/ntee-db) — your tabs, drafts,
undo history, and session all survive relaunch.

```
┌ ntee-editor · ~/projects/my-app ──────────────────────────────┐
│ file tree          │  tab1.go  tab2.ts  (red = unsaved)       │
│ (cursor-driven,    │ ─────────────────────────────────────────│
│  git-aware colors) │  1 │ package main        ← syntax colors │
│                    │  2 │                     ← ● diagnostics │
│                    │  3 │ func main() {       ← click to move │
└───────────────────────────────────────────────────────────────┘
 @query > _                                  status / command bar
```

## Highlights

- **Instant project navigation** — fuzzy file finder, repo-wide content grep,
  and a live query bar that expands the file tree as you type.
- **Real code intelligence** — LSP-backed autocomplete, diagnostics in the
  gutter, go-to-definition / find-references, for Go, TypeScript/React, Python,
  Ruby, Java, Kotlin, Vue. One command installs the servers.
- **Find & replace with live preview** — see every replacement rendered in the
  buffer *before* you commit it.
- **Mouse support** — click anywhere in the file to place the cursor.
- **Everything persists** — tabs, unsaved drafts, undo timeline, and session
  are stored per-project in ntee-db and restored on relaunch.
- **Built-in inspection dashboard** — see store disk usage and language-server
  status, compact the database, and start/stop LSPs without leaving the editor.

## Install

One command (macOS or Linux). It checks Go (installs via brew/apt/dnf/pacman if
missing), builds the **latest release tag** of `ntee` into `~/go/bin` (never an
untagged commit — while the repo has no tags yet it warns and builds the
default branch), and installs language servers for **TypeScript, Vue, and
Kotlin** when their runtimes are present:

```sh
curl -fsSL https://raw.githubusercontent.com/nickooan/ntee-editor/main/install.sh | bash
```

Other languages (Go, Python, Ruby, Java) are one command away — see
[Installing language servers](#installing-language-servers).

From a local checkout: `./install.sh` (builds the checkout as-is, for
development). **Updating** is the same curl command — it fetches tags and
rebuilds the newest release.

Make sure `~/go/bin` is on your PATH (the script prints the exact `export` line
if it isn't). Knobs: `NTEE_INSTALL_DIR=<dir>` overrides the clone destination
(default `~/.ntee-editor/src`); `NTEE_SKIP_LSP=1` skips the language-server step
(run `ntee --prepare-lsp` later).

## Quick start

```sh
ntee            # open the current directory
ntee <path>     # open a project
```

Five things to know in your first five minutes:

1. **Type to navigate.** The `@query >` bar at the bottom drives the sidebar:
   type a path fragment and directories expand, the best match highlights, and
   a popup suggests completions. `Enter` opens a file **straight into edit
   mode**.
2. **`Ctrl+P` finds files, `Ctrl+G` greps contents** — anywhere, any time.
3. **Click to place the cursor**, type to edit, `Ctrl+S` to save.
4. **`Esc` walks you back out** — clears the selection, then leaves edit mode,
   then goes up a directory.
5. **`Ctrl+Q` quits.** Your tabs, drafts, and undo history will be there when
   you come back.

---

## Keymap

### Everywhere

| Key | Action |
|---|---|
| `Ctrl+P` | **Goto file** — fuzzy finder; empty query lists recent files |
| `Ctrl+U` | Goto **uncommitted** file — same finder, limited to git-dirty paths |
| `Ctrl+G` | **Grep the repo** — colored preview on top, results below; `↑/↓` + `Enter` jumps |
| `Ctrl+T` | **Inspection dashboard** — store stats + LSP control ([below](#inspection-mode-ctrlt)) |
| `Shift+Tab` | Cycle the focused tab (wraps) |
| `Ctrl+Q` / `Ctrl+C` | Quit (session saved) |

### Query bar (home)

| Key | Action |
|---|---|
| type a path / fragment | expand directories · highlight best match · suggest completions |
| `↑/↓` (popup open) | move the popup selection — previews in the bar, highlights in the sidebar |
| `↑/↓` (no popup) | scroll the open file |
| `Shift+↑/↓` | walk the sidebar tree row-by-row (never expands) |
| `Enter` | directory → enter it · file → open in edit mode · `:cmd` → run command |
| `Tab` | edit the currently open file |
| `Ctrl+F` | find in the open file |
| `Esc` | go up one directory |

### Edit mode

| Key | Action |
|---|---|
| **mouse click** | move the cursor to the clicked position (gutter click → column 0). Hold `Shift` for native terminal text selection |
| **`Ctrl`+click** | jump to definition at the clicked token (same as `Ctrl+J`). Some terminals capture Ctrl+click as a right-click — then use `Ctrl+J` |
| **wheel / two-finger scroll** | scroll up/down (moves the cursor); horizontal scroll does nothing |
| `Ctrl+S` | save (also clears the file's stashed draft) |
| `Ctrl+Z` / `Ctrl+Y` | undo / redo — snapshot bursts, persisted across relaunch |
| `Ctrl+A` | progressive select: word → line; then `Shift+↑/↓` extends line-wise |
| `Ctrl+F` | find in file ([below](#find--replace-ctrlf)) |
| `Ctrl+J` | **jump to definition** (or the file path under the cursor); on a definition line it finds **references** instead. Multiple hits open a picker with a live code preview |
| `Ctrl+O` | jump back (restores file, cursor, scroll; 20-deep trail) |
| `Ctrl+E` | open the `@exec >` command bar ([below](#exec-bar-ctrle)) |
| `PgUp` / `PgDn` | page with a one-line overlap |
| `Home` / `End` | line start / end |
| `Esc` | clear selection → then discard unsaved edits and return to the query bar |

**Autocomplete** pops up on its own as you type an identifier or `.` —
`↑/↓` selects, `Tab`/`Enter` accepts, keep typing to filter, `Esc` dismisses.
Powered by the file's language server.

### Find & replace (`Ctrl+F`)

Type to search — matches highlight live (case-insensitive; regex supported,
falling back to literal). `↑/↓` cycles the focused match (orange), `Enter`
jumps the cursor to it, `Esc` backs out.

Press **`Ctrl+E` while matches are highlighted** to enter the replace bar:

| Command | Action |
|---|---|
| `c <text>` | replace the **focused** match — then focus lands on the next one, so you can `c` again |
| `mlc <text>` | **m**ulti-**l**ine **c**ursor: replace **all** matches at once |

As you type the replacement, the buffer shows a **live preview** — target spans
render in green exactly as they'll look after `Enter`. Only the matched span is
touched (`search54321` + `c search123` → `search12354321`); a bare `c`/`mlc`
deletes the match. Every replace — even `mlc` across a hundred lines — is one
`Ctrl+Z` step.

### Exec bar (`Ctrl+E`)

Editor commands with Tab-completed suggestions:

| Command | Action |
|---|---|
| `copy` (`cp`) `[a-b\|all\|fpath]` | copy the selection, a line range, the whole buffer, or the file's path |
| `jump` (`jp`) `<line\|top\|end>` | go to a line (lands ~30% from the top) |
| `git scf <head\|branch\|both>` | **s**olve **c**on**f**lict: resolve the git conflict block at the cursor/selection, keeping the named side (or both) — one undo step |
| `tab <name\|cl\|cr>` | switch tab / close-left / close-right |

### Command bar (`:`)

`jump <line|top|end>` (alias `jp`) · `tab <name|cl|cr>` · `revert` (restore
last saved snapshot) · `refresh` (re-scan the file tree).

### Inspection mode (`Ctrl+T`)

A dashboard for the editor's own machinery. `Shift+↑/↓` switches the left menu;
`Esc` returns to where you were.

- **ntee-db** — the project store's disk usage: live records, main-log size
  with dead-space percentage, blob usage, orphaned bytes.
- **lsp** — every configured language server with its live status:
  **running** (green) · **stopped** (yellow — starts on demand) ·
  **disabled** (gray, with the reason).

The `@inspection >` bar takes:

| Command | Action |
|---|---|
| `db compact` | drop dead records from the main log (runs in the background) |
| `db relieve` | also rewrite the blob store, releasing orphaned blobs |
| `lsp enable <lang\|all>` | start the server **now** and persist `enable: true` to your config |
| `lsp disable <lang\|all>` | stop the server and persist `enable: false` |

LSP commands act live *and* write `~/.config/ntee-editor/config.yaml` (previous
file backed up to `config.yaml.bak`), so the change sticks across restarts. If a
server fails to start (e.g. binary missing), the error shows right in the bar.

---

## Tabs & drafts

Every opened file becomes a tab at the top of the pane (**red = unsaved**).
Switching away from a dirty buffer stashes a draft — content plus up to 15 undo
steps — in ntee-db. Reopening the tab, or relaunching the editor, restores it
exactly, with undo stepping back to the on-disk version. `Ctrl+S` saves and
drops the draft; `Esc` discards it. The tab list and active tab persist per
project.

## Code intelligence (LSP)

Language servers start **lazily** — opening the first file of a language spawns
its server, scoped to the file's nearest project root (so a monorepo frontend
loads its ~300 files, not the repo's 15k). You get:

- **Diagnostics** — colored `●` gutter markers, `✗N ⚠M` status counts, and the
  cursor line's message in the status bar.
- **Autocomplete** — as you type, server-ranked.
- **Definition & references** — `Ctrl+J` / `Ctrl+O`, with UTF-16 column
  bridging done for you.

ntee is **LSP-strict**: when a language has a configured server, its answer is
final — you'll see "still starting…" or "no definition found" rather than a
guessed jump. File types with no server say so (a bare file path under the
cursor still opens directly). A missing binary degrades to a one-time notice.

A crashed server restarts on demand; a rapid crash-loop disables the language
for the session with a note. Check or override any of this live in the
[inspection dashboard](#inspection-mode-ctrlt).

### Installing language servers

```sh
ntee --prepare-lsp                        # all languages: plan, ask, install, write config
ntee --prepare-lsp python ruby            # only the named languages
ntee --prepare-lsp --yes go java          # skip the prompt (flags before names)
```

Built-in recipes: **go** (gopls) · **typescript/js/react** (typescript-language-server) ·
**python** (pyright) · **ruby** (ruby-lsp) · **java** (jdtls) · **kotlin**
(kotlin-language-server) · **vue** (@vue/language-server). `install.sh` runs
only the typescript/vue/kotlin recipes; install the rest with the commands
above whenever you need them. Installs use the platform's native tool
(`brew` / `go install` / `npm` / `gem`), skip languages whose runtime is
absent (telling you what to install), keep your tuned config entries, and back
the old file up to `config.yaml.bak`. Recipes can be overridden per language
via an `install:` block.

### Toggling LSP

In-editor: `Ctrl+T`, then `lsp enable ruby`, `lsp disable all`, etc.

From the shell:

```sh
ntee --disable-lsp typescript vue   # write enable: false for these languages
ntee --disable-lsp all              # turn LSP off globally (lsp.enabled: false)
ntee --enable-lsp typescript        # flip a language (or 'all') back on
```

## Configuration

Defaults ← `~/.config/ntee-editor/config.yaml` ← `<project>/.ntee-editor.yaml`:

```yaml
version: 1
editor: { tab_width: 4, max_snapshots: 50, max_highlight_kb: 512 }
tree:   { ignore: [".git"] }    # only .git is hidden; .gitignore'd paths show grayed
theme:  { syntax: "gruvbox" }   # any chroma style name (monokai, dracula, …)
languages:
  go:         { extensions: [".go"], lsp: { command: "gopls" } }
  typescript: { extensions: [".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"],
                lsp: { command: "typescript-language-server", args: ["--stdio"] } }
  # ruby: { enable: false }     # turn a server off without deleting its config
lsp: { enabled: true }
```

- `languages.<name>.extensions` are **unioned** with the built-in defaults, so
  you can add a file type to an existing server without re-listing them.
- `command` / `args` / `init` overlay the defaults when set (`init:` passes
  LSP initializationOptions — e.g. `tsserver.path` for a globally installed
  typescript ≤5.x; the TS 7 native preview has no `lib/tsserver.js` and won't
  work).
- `enable: true|false` per language (omitted = on).
- The default theme is a tuned gruvbox dark; any chroma style name works,
  rendered truecolor and auto-degraded on 256-color terminals.

## Persistence & maintenance

Each project gets its own ntee-db store under
`~/.ntee-editor/stores/<hash(project-root)>/` holding recent files, undo
snapshots, drafts, tabs, and the session. ntee-db is single-writer (flock):
opening the same project twice falls back to in-memory state with a notice —
undo still works, nothing persists.

Old undo versions auto-evict per file, but dead space accumulates in the log
over time. Press `Ctrl+T` to see exactly how much, and `db compact` /
`db relieve` to reclaim it — no external tools needed.

## Architecture notes

- `internal/view`, `internal/input`, `internal/filetree`, `internal/fuzzy` are
  pure (no Bubble Tea) and unit-tested; `internal/app` holds the single Model
  with per-mode key handlers (`keys_edit.go`, `keys_search.go`, …).
- Highlighting is whole-buffer chroma tokenization cached per line, refreshed
  at edit-burst boundaries. The search view re-tokenizes its frozen snapshot so
  matches (and replace previews) overlay syntax colors.
- Undo is full-content snapshots keyed `versions:<seq>` in ntee-db, with a
  `file` secondary index whose `MaxPerValue` auto-evicts the oldest per file.
- `internal/lsp` is a hand-rolled stdio JSON-RPC client (Content-Length
  framing, ordered notifications), one server per language, with a bridge
  mechanism for hybrid servers (Vue → TypeScript).
- Mouse hit-testing shares its layout math with the renderer (single
  `sidebarWidth()` source of truth), so clicks can't drift from what's drawn.

## License

Apache License 2.0 — see [LICENSE](LICENSE). Copyright 2026 Nick An.

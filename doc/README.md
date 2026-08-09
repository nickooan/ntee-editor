# ntee-editor — internal packages documentation

ntee-editor is a Sublime-style terminal text editor built on [Bubble Tea](https://github.com/charmbracelet/bubbletea): a file tree on the left, highlighted content on the right, a command bar at the bottom, and per-project state (recent files, undo history, unsaved drafts) persisted in ntee-db. Everything interesting lives under `internal/`; `cmd/ntee` is a thin entry point that loads config, opens the store, wires the language-server manager, and starts the Bubble Tea program.

This directory contains one document per internal package. Each follows the same shape: an **Introduction** (what the package is for), an **Architecture** section (main types, data flow, concurrency where it matters), and a **Functions** tour grouped by source file — important functions explained in plain language, trivial helpers rolled up in a single line.

## Packages

| Package | Job | Doc |
|---|---|---|
| `internal/app` | The editor itself: the Bubble Tea Model, all modes (query, edit, search, diff, blame, conflict, previews), key/mouse handling, rendering | [app.md](app.md) |
| `internal/clipboard` | Writing to the system clipboard (OSC52 + platform tools) | [clipboard.md](clipboard.md) |
| `internal/config` | YAML config loading and editing, with a trust rule: project-local config can never name executables | [config.md](config.md) |
| `internal/diff` | Myers line diff with a bounded memory trace — feeds the diff review mode | [diff.md](diff.md) |
| `internal/filetree` | Sidebar tree, project file walk (search corpus), the symlink-aware file jail, atomic saves, gitignore + git status | [filetree.md](filetree.md) |
| `internal/fuzzy` | The Goto-Anything matcher: subsequence scoring, basename bonuses, directory-browse ordering | [fuzzy.md](fuzzy.md) |
| `internal/gitcmd` | The single place git is spawned — every call has a hard timeout | [gitcmd.md](gitcmd.md) |
| `internal/graphql` | Gathers, merges, and renders GraphQL SDL schemas for the preview mode | [graphql.md](graphql.md) |
| `internal/input` | Small cursor-aware text-input helpers for the command bars | [input.md](input.md) |
| `internal/lsp` | Language-server manager: lazy per-language servers, a writer queue that can't freeze the UI, Vue↔TypeScript hybrid bridging | [lsp.md](lsp.md) |
| `internal/lspsetup` | The `--prepare-lsp` installer: pinned per-language recipes via brew/go/npm/gem | [lspsetup.md](lspsetup.md) |
| `internal/openapi` | Parses OpenAPI v3 YAML (order-preserving), resolves cross-file `$ref`s, renders the spec preview | [openapi.md](openapi.md) |
| `internal/store` | Per-project persistence on ntee-db: recents, undo snapshots, drafts, session, corpus cache | [store.md](store.md) |
| `internal/syntax` | Syntax highlighting: tokenizers and themes producing per-line styled segments | [syntax.md](syntax.md) |
| `internal/view` | Shared view primitives: search regexes, match finding, highlight segment types | [view.md](view.md) |

## How the pieces fit

```
cmd/ntee ──▶ config.Load ──▶ store.Open ──▶ lsp.NewManager ──▶ app.New ──▶ Bubble Tea
```

- **`app` is the hub.** Its `Model` is copied by value through every update; long-lived caches ride behind pointer fields so all copies share them. Every keystroke flows through `Update`, every frame through `View`.
- **Slow work never blocks the UI.** Directory walks, git commands, diffs, blame, document rendering, and LSP queries all run on `tea.Cmd` goroutines and land back as messages, guarded by generation counters so stale results are dropped.
- **All file access is jailed.** Creating, reading, writing, and deleting files goes through `filetree`'s symlink-aware containment check — a path (or symlink) that resolves outside the project root is refused. Saves are atomic (temp file + rename).
- **All git access is bounded.** `gitcmd` wraps every git spawn with a 10-second deadline, so a hung `git status` (network filesystem, lock contention) degrades gracefully instead of wedging a goroutine forever.
- **Language servers are decoupled.** `lsp` starts one server per language on first use. Document sync goes through a bounded writer queue per server — if a server stops reading its stdin, edits coalesce and the editor keeps typing. Crashes restart with a budget; repeated crash-loops disable the language for the session.
- **Trust boundaries are explicit.** `config` merges defaults ← user config ← project config, but strips anything from the project file that could name an executable — cloning a repo can never hand it code execution. `lspsetup` installs only pinned versions.
- **State survives restarts.** `store` keeps recents, tabs, session, the walk corpus (for instant warm starts), undo snapshots (content-hashed for cheap dedupe), and unsaved drafts, all in a per-project ntee-db.

# internal/app

## Introduction

`internal/app` is the editor itself: one Bubble Tea model that owns the whole terminal UI. `cmd/ntee` builds a `Model` with `app.New(...)` and hands it to a Bubble Tea program; from then on everything the user sees and types flows through this package. The package deliberately holds no file-format, git, or LSP logic of its own — it orchestrates the sibling packages (`filetree` for jailed file access, `store` for persistence, `lsp` for language servers, `gitcmd` for git spawns, `syntax`/`view` for highlighting and search primitives) and turns their answers into a lipgloss-rendered screen.

Three design rules shape almost every function here:

- **The Model is a value.** Handlers receive a `Model` copy, mutate the copy, and return it. There is no shared mutable model — except for a few caches that deliberately ride behind pointer fields (`searchMC`/`previewMC` match caches, the `frames` frame cache, `grepPreviewRC`) so every copy shares the same memo. That's safe because Update and View both run on the program goroutine.
- **Anything slow runs as a `tea.Cmd`.** Git status polls, diff/blame computation, corpus walks, grep scans, LSP lookups, document rendering — all run off the UI goroutine and come back as messages. Every async family carries a generation counter (`diffGen`, `blameGen`, `grepGen`/`grepSearchGen`, `preview.gen`) plus usually the file's rel path; the handler drops any result whose generation or file no longer matches, so a user who Esc'd or switched files never sees a stale answer land.
- **Modes are read-only by closed key switches.** Diff review, blame, conflict solving, and the document previews are made read-only not by guards on every edit path but by their key handlers only listing the keys they accept — anything unlisted falls through inert, so typing simply cannot reach the buffer.

## Architecture

**The Model.** One large struct (`app.go`) holding: config, the store backend, the LSP registry, and the project root; the open file and its edit session (`editor`); the tab list and per-file drafts; per-feature state blocks for search, diff, blame, conflict, preview, grep, fuzzy finder, completion, and the inspection dashboard; and the caches (highlight lines, corpus, match/frame caches). Comments in the struct are the best field-level reference — this document stays at the function level.

**Modes.** A `mode` enum drives dispatch:

| mode | what it is |
|---|---|
| `modeQuery` | the home screen: a path/fuzzy input bar driving the sidebar file tree |
| `modeEdit` | the text editor over the open file |
| `modeSearch` | in-file search over a frozen content snapshot |
| `modeCommand` | the bottom `:` command bar |
| `modeExec` | the `@exec >` editor-command bar (Ctrl+E from edit) |
| `modeSearchExec` | the replace bar layered on search (Ctrl+E from search) |
| `modeInspect` | the Ctrl+T inspection dashboard (store stats, LSP control, theme, op-commands table) |
| `modeDiff` | read-only git-diff review of the buffer (`git diff` in @exec) |
| `modeConflict` | interactive merge-conflict resolution over the live buffer (`git scf`) |
| `modeOpenAPI` / `modeGraphQL` | read-only rendered document previews (`openapi` / `graphql`) |
| `modeBlame` | read-only git-blame annotation (`git blame`) |

**The Update/View loop.** `Update` first bumps the frame cache's sequence (per-message memos go stale), then handles non-key messages (window size, LSP diagnostics, async results, git status ticks, mouse, paste). For key presses it peels overlays in priority order — quit chords, splash, message overlay, `:rm` confirm, fuzzy finder, definition picker, grep, the Ctrl+R op overlay — then global chords (Ctrl+P/U/G/T/R, Shift+Tab, all suppressed while a text bar has focus via `inBarMode`), and finally dispatches on `m.mode` to the per-mode handler. `View` (render.go) assembles header + sidebar pane + main pane + status rows, choosing the main body by the same overlay-then-mode priority.

**How async results land.** Each worker Cmd captures everything it needs by value (including a copy of the buffer lines where relevant), does its work, and returns a message tagged with `gen` and usually `rel`. The `handleXReady` handler compares `gen` against the Model's current counter, `rel` against the open file, and often the mode too; a mismatch means the result is stale and it is silently dropped. Generations stay monotonic across state clears precisely so in-flight results remain identifiable.

**How the files divide the work.** `app.go` is the hub: Model, `New`/`Init`/`Update`, corpus and git-status upkeep, file opening, highlight caches. `edit.go` is the pure editor struct; `keys_edit.go` its key handler; `history.go`/`drafts.go`/`tabs.go` the undo/draft/tab machinery. Each extra mode gets a `*_mode.go` (state + async compute), a `keys_*.go` (key handler), and usually a `render_*.go`. `render.go` holds View, the shared line renderers, and the whole color palette. `sidebarlist.go` is the one left-pane list: each mode supplies rows, and that file windows, paints, and hit-tests them. `jump.go` and `complete.go` are the LSP consumers; `keys_grep.go` and parts of `keys_command.go` are the overlays; `mouse.go` maps clicks back through the same layout math the renderers use.

## Functions

### Query bar & sidebar

#### app.go (core & corpus)

- `New` constructs the Model: wires the store, LSP registry, and clipboard, picks the root to start in (the opened directory, or the Ctrl+W repo it was last rooted at if that repo still has a `.git`), and hands off to `enterRoot`. A cold cache shows the splash and lets `Init` build the index in the background.
- **Roots.** `workspaceRoot`/`workspaceDB` are the opened directory and its store; `root`/`db` are the *current* root — the workspace, or the nested repo Ctrl+W selected (`activeRepo`). Every root-relative feature (tree, query bar, Ctrl+P/U/G, git status, diff/blame, save, `:mkdir/:touch/:rm`) reads `root`/`db` and so needs no per-feature scoping: rooted at a repo, the editor behaves as if that repo had been opened directly. `db` is `store.Scoped(workspaceDB, activeRepo)`, so file records stay keyed by workspace path. `workspaceIsRepo` covers what is fixed by the opened directory (Ctrl+W's refusal, the nested-repo scan).
- `enterRoot` points `root`/`db`/`gitRepo` at `activeRepo`, resets all root-relative state, and restores that root's tabs, open file, and tree position (`rootSessionPosition`) plus its warm search index — only store reads and one `.gitignore`, no walk. `leaveRoot` persists the current root first: cursor, tabs, position, unsaved edits stashed as a draft, and every buffer-bound view (completion, diff, blame, conflict, preview, jump trail) ended. `switchRoot` (the Ctrl+W choice) runs leave → `rootGen++` → enter, then fires the index load/rebuild and git refresh in the background. The `rootGen` bump is what drops a corpus walk or git status still in flight for the old root — both messages carry the generation they ran for.
- `saveSession` read-modify-writes the workspace-level session: the Ctrl+W selection, plus the current root's last file and tree path (the workspace's own in `LastFile`/`Command`, a repo's in `Repos[repo]`), so every root keeps its own position.
- `applyWorkspaceRepos` takes a nested-repo scan (always run over `workspaceRoot`: inside the corpus walk when rooted at the workspace, via `scanWorkspaceReposCmd` otherwise). Rooted at the workspace it starts the git poll on the first repo found; if the repo the editor is rooted at has vanished from the scan, it switches back to the workspace with a notice.
- `Init` fires the startup Cmds: the corpus build (cold) or a background signature re-validation of the adopted index (warm), the splash ticker, — when the root is a git repo — the first status refresh plus the poll loop, and the nested-repo scan when the corpus walk won't run it (a warm start, or a start rooted inside a repo).
- `Update` is the dispatcher described above; `View`/`render` live in render.go.
- `ensureCorpus` keeps the file index fresh without ever walking on the UI goroutine: a cold cache or one older than the 2s TTL fires `rebuildCorpusCmd` in the background, and callers simply use whatever corpus is currently resident. This is the pattern that stopped large repos from lagging per keystroke. Caller contract: the returned Cmd must always reach the runtime — it marks the rebuild in flight (`corpusRebuilding`), and dropping it latches the flag and freezes the index for the rest of the session. Callers: the query-bar handler, the finder/grep openers, the `gitStatusMsg` handler when the dirty set changed, and `FocusMsg`.
- `rebuildCorpusCmd` does the actual walk off-goroutine (reloading `.gitignore` so external edits to it re-filter) and delivers a `corpusMsg`; the `Update` handler swaps it in and persists it so the next launch warm-starts. `validateCorpusCmd`/`signatureValidFor` are the warm-start check: an O(#dirs) stat sweep over the saved directory-mtime map, rebuilding only on a mismatch.
- `maybeGitRefresh` is the single gate for spawning `git status` — no-op outside a repo or while one is in flight, so Ctrl+S, `:refresh`, and the 3s poller can't stack child processes. `refreshGitStatusCmd` runs the spawn; the `gitStatusTickMsg` branch of Update pauses polling when the editor is idle (60s without input) or unfocused.
- `treeEntries` builds the sidebar entries — a stat per expanded directory plus gitignore matching — memoized per message through the shared `frameCache`, so the key handler, the sidebar renderer, and the query popup all reuse one walk per Update+View cycle. `invalidateTreeEntries` drops the memo when a handler mutates the filesystem mid-message (`:touch`/`:mkdir`/`:rm`); `enterRoot` drops it too, since the same text under a new root means different rows.
- `sidebarCommand` and `highlightedSidebarCommand` encode the query bar's three-way state split: the confirmed selection drives directory *expansion*, keyboard/popup navigation drives only the *highlight*, and an open fuzzy finder overrides both.
- `openFileAt` is the one deliberate-open path: stat-first (a missing file errors instead of becoming a tab), stashes the outgoing buffer as a draft, closes/opens LSP documents, clears any diff/conflict/preview state, starts a fresh edit session, restores a stashed draft and the remembered cursor, and lands in edit mode.
- `gitScopeFor` is the read counterpart for running git on a file: it returns the folder to run git in and the path relative to that repo. When the current root is a repo (opened directly, or rooted there by Ctrl+W) it returns the root and the unchanged path; rooted at a workspace it picks the deepest nested repo containing the file. An empty repo list that has not been scanned yet reports `loading` so callers can say "still loading" instead of "not a repository". It reads only the in-memory repo list, never the disk. `repoRelativePath` wraps it for the copy commands, and `gitScopeError` maps the failure to its status message.
- `refreshFileHighlights` rebuilds the line and syntax-highlight caches from the buffer (chroma is stateful across lines, so it's whole-buffer work — run at burst boundaries, never per keystroke); files over `MaxHighlightKB` render plain. It skips the whole rescan when the same file content is already highlighted, keyed by `hlPath` + a content hash (hash, not `edit.rev` — `newEditor` resets rev on reopen, so a rev compare could false-hit), so a save right after a flushed burst or an Esc with no edits costs nothing. `hlMarkLine`/`hlInsertLine`/`hlInsertLines`/`hlRemoveLine` keep the per-line cache index-aligned between full rescans. `invalidateHighlightCaches` clears the content key first (same bytes, new colors) and recomputes everything after a theme switch.
- `handlePaste` routes bracketed-paste text to whichever input has focus, collapsing newlines for the single-line bars; the read-only modes take no paste at all.
- `quit` persists cursor, draft, and session, then shuts the language servers down inside the returned `tea.Cmd` (bounded in `ShutdownAll`) before yielding `tea.QuitMsg` — a wedged server must not freeze the UI on the way out.

*Plus small helpers: `versionTag`, `inBarMode`, `gitStatusTick`, `splashTick`, `truncatedNotice`, `keyText`, `pasteLine`, `saveSession`, `highlightedEntryIndex` — formatting, tick constructors, and thin lookups.*

#### matchcache.go

- `matchCache.get` memoizes `view.FindSearchMatches` per (content, query) — search mode re-derives the match list in the status line, the body, and every navigation key, so this turns 3–4 full-content regex scans per keystroke into one. Shared by pointer across Model copies.
- `matchCache.matchesByLine` / `matchCache.splitLines` memoize the render-side derivatives under the same keys: the per-line match buckets and the content split into lines. The search body (and the document previews) rebuilt both from scratch every frame — an O(file) split per render on a large buffer.
- `regexCache.multiline` does the same for one compiled regex (the grep overlay's preview would otherwise recompile per frame).

*Plus: `searchMatches` — the Model-level accessor over `searchMC`.*

#### keys_query.go

- `handleQueryKey` is the home-mode handler: typing edits the bar (expanding the tree), Shift+arrows walk the popup or sidebar highlight, plain arrows scroll the previewed file, Enter submits, Esc is layered — with the popup open it only dismisses it (`dismissQuerySuggestions`, dropping any popup navigation but keeping the typed text), otherwise it climbs to the parent directory with the popup kept hidden, so repeated Esc climbs one level per press. It calls `ensureCorpus` first so the fuzzy suggestions always have an index to read, then routes through `dispatchQueryKey` and batches the rebuild cmd with the branch's cmd at one return point — no branch can drop it (a dropped cmd used to latch `corpusRebuilding` and freeze the index). Suggestions are computed lazily — only the branches that read the popup pay for the corpus filter; typing branches leave it to View, which computes (and memoizes) the fresh one.
- `queryInputSuggestions` completes the typed bar text (exact/prefix over the visible tree, fuzzy over the full corpus). This is the dominant per-keystroke cost in home mode, so it's memoized per message through `frameCache` keyed by the typed text — the key handler and View share one filter pass instead of each running their own. It also gates on `suppressQuerySuggestions` (before the memo, so nothing suppressed is ever cached): a sidebar directory click or an Esc climb sets the bar text without being typing, and an Esc dismiss closes the popup outright, so in each case the popup stays hidden until the next keystroke edits the text and lifts the flag.
- `submitQuery` acts on Enter: inline fs commands first, then `:` commands, then the resolved target — a directory is confirmed (which is what drives expansion), a file opens straight into edit mode. A trailing GitHub-style line anchor (`scripts/build.sh#L8`, `#l8`, `#L8-L12`) is split off first and the file opens on that line via `openFileAtLine`; the popup (`queryInputSuggestions`) and the sidebar (`sidebarCommand`) match on the path part only, so the anchor never changes what is found.
- `parseInlineFs` recognizes the bar's filesystem commands (`<path> :mkdir <rel>`, `:touch`, `<path> :rm`), rejecting anything absolute or escaping the root; `inlineFsPathPrefix` lets the sidebar keep highlighting the target path while the command suffix is still being typed.
- `queryCreate` performs mkdir/touch and enters the result (a new file opens for editing). `armRemoveConfirm` opens the `:rm` confirmation modal — deletion is irreversible, so Enter alone never deletes. `queryRemove` deletes and then `dropRemovedPath` forgets every tab, draft, and cursor under the removed path (an open buffer over a deleted file would silently resurrect it on save, so the editor resets instead).
- `adoptPreview` promotes a navigated highlight into the editable text so the next keystroke continues from it — the glue between navigation and typing.

*Plus small helpers: `isInlineFsVerb`, `moveInputSuggestion`, `moveSidebarSelection`, `moveQueryToParentDirectory`, `dismissQuerySuggestions` — suggestion plumbing and highlight movement.*

### Editing & history

#### edit.go

The `editor` struct is a minimal multi-line buffer: `lines`, cursor, a `dirty` flag, and `rev` — a mutation counter every mutator must bump so the highlight cache knows when to rescan.

- `insert`, `newline`, `backspace`, `move` are the primitive mutations; typing over a selection replaces it because `insert` calls `deleteSelection` first. `insertLines` is the bulk multi-line splice paste uses — one `slices.Insert` regardless of line count, where per-line stitching was O(pasted × file lines).
- `contentHashed` joins and hashes the buffer at most once per `rev` — burst boundaries need content+hash for dedupe, snapshot writes, and LSP sync, and without the memo each caller re-joined the whole file.
- `expandSelection` is the progressive Ctrl+A: first press selects the word under a stable anchor, the next the whole line (which latches line-wise mode so Shift+↑/↓ can extend across lines via `extendLineSelection`). `deleteSelection` handles both span and line-wise selections as one operation.
- `wordRange`, `identifierAt`, `identifierCols` are the token finders shared with jump/complete: whitespace-word, identifier run, and nearest-identifier-columns respectively.

*Plus small helpers: `newEditor`, `content`, `line`, `clampCursor`, `clearSelection`, `selectedText`, `inLineSelection`, `selectionText`, `isEditSpace`, `identifierText`, `dedupeRanges` — accessors, clamps, and selection utilities.*

#### keys_edit.go

- `handleEditKey` is the edit-mode keymap. The interesting choreography: the completion popup consumes its keys first; Esc peels state in order (signature pin → selection → the mode itself, where discarding unsaved edits deletes the draft and pushes a snapshot so one Ctrl+Z recovers the text); every buffer mutation marks its highlight line and sets `snapDirty`; and word boundaries (space, enter, cursor line change) call `flushBurst` so undo coalesces typing bursts.
- `saveEdit` (Ctrl+S and `:w`'s shared path) writes the buffer through `filetree.WriteViewFile`, pushes a "save" snapshot, deletes the now-obsolete draft, and notifies the language server.
- `pageEdit` pages with a one-line overlap, recomputing the viewport top from the cursor because `fileScrollY` goes stale during arrow navigation; `moveEditCursor` treats leaving a line as a burst boundary.
- `editPaste` splits pasted text on newlines and splices the whole block in with one `insertLines` call (plus one bulk highlight-cache splice), as one undo step.

*Plus small helpers: `contentHeight`, `tabRows` — layout arithmetic.*

#### history.go

The undo timeline is a list of snapshot seqs plus a cursor; the content lives in the store, not the Model.

- `pushSnapshot` is the heart of undo: it dedupes against the current head by hash (a save right after a flushed burst upgrades the snapshot's kind instead of adding a no-op step), drops and deletes any orphaned redo branch, trims to `MaxSnapshots`, persists the new snapshot, and syncs the LSP. It runs on every burst boundary, so the hot path is hash-only — no store read.
- `flushBurst` checkpoints only when edits are pending; this is what makes one snapshot cover a whole typing burst.
- `undo`/`redo` move the cursor along the timeline and `loadSnapshot` swaps the buffer to that content, keeping (clamped) cursor position and forcing a highlight rescan.
- `beginEditSession` resets the timeline for a fresh open and records the on-disk baseline.

*Plus: `nextSeqAfter` — a strictly-increasing millisecond seq.*

#### drafts.go

- `stashDraftIfDirty` persists an unsaved buffer (content, cursor, and up to 15 recent undo steps) before any switch-away or quit — the reason tab switching and jumping never lose work.
- `restoreDraft` rebuilds that state on reopen: draft steps stack on top of the disk baseline so undo can walk back down to the on-disk content; if the disk caught up with the draft (saved elsewhere) it self-heals by deleting it.

#### tabs.go

Every opened file becomes a tab; the list, active index, and per-tab cursors persist on every mutation.

- `activateTab` opens the i-th tab through `openFileAt` (so stash/restore happen for free) and lazily drops tabs whose files have vanished. It serves Shift+Tab cycling, the `tab <name>` command, and tab-strip clicks alike.
- `closeTabsSide` implements `tab cl`/`cr`: closes the clean tabs on one side, but unsaved tabs refuse to close and stay red.
- `tabDirty` decides the red rendering: the active tab from the live buffer's dirty flag, inactive ones from having a stashed draft.

*Plus small helpers: `addTab`, `persistTabs`, `recordCursor`, `cycleTab`, `findTab`, `tabCommand` — list management and the name→index resolver.*

### Search & replace

#### keys_search.go

- `enterSearch` freezes a snapshot of the content (and re-tokenizes its highlighting) so the search view is stable while the query is typed; the frozen string also keeps the match cache's key compare on the pointer-equality fast path.
- `handleSearchKey` types the query, cycles matches with ↑/↓, opens the replace bar with Ctrl+E, and commits with Enter.
- `acceptSearch` lands the focused match: in edit mode the cursor jumps onto it (byte offset → rune column — the one offset bridge), otherwise the line is centered.
- `focusNearestMatch` starts a fresh query at the first match at/after the cursor line instead of the top of the file.
- `anchorScroll`/`anchorCursorLine` place a landed line ~30% from the top — the shared landing convention used by search, jump, grep, conflict, and preview alike.

*Plus small helpers: `freezeSearchSnapshot`, `nextMatch` — snapshot upkeep and wrap-around cycling.*

#### keys_search_exec.go

- `enterSearchExec`/`handleSearchExecKey` run the replace bar over the search view (the highlights stay visible behind it); Enter dispatches `runSearchExecCommand` — `c <text>` replaces the focused match, `mlc <text>` replaces all.
- `searchReplace` performs the replacement as one undoable snapshot, lands the edit cursor on the first replaced span, re-freezes the search snapshot, and returns to search mode where matches recompute.
- `buildSearchPreview` powers the live green preview while the command is typed: it splices the replacement over the target matches purely for display, tracking byte-offset shifts so surviving matches keep their highlights.
- `replaceInLines` is the commit-path splice, applied right-to-left within each line so earlier offsets stay valid.

*Plus: `searchExecPreview` — parses the bar into a preview spec.*

### Command bars

#### keys_command.go

- `executeCommand` dispatches the `:` bar: `jump` (line/top/end), `revert` (loads the last "save" snapshot as an undoable edit), `tab <name|cl|cr>`, and `refresh` (a forced corpus + git-status rebuild).
- `openFuzzy` (Ctrl+P) builds the finder corpus with recently-opened files moved to the front — an empty query is a recents list — and directories appended last. `openUncommitted` (Ctrl+U) is the same overlay over the corpus ∩ gitDirty intersection. `openRepoPicker` (Ctrl+W) is that overlay over nested git repo roots only (prompt `Repo: `), plus a first row named for the opened directory (`name/`) that selects the whole workspace. It refuses when the opened directory is already a git repository, and lists the same rows whichever root the editor is at. `selectWorkspaceRepo` hands the choice to `switchRoot` (app.go), which re-roots the whole editor there — tree, query bar, Ctrl+P/U/G, git views — so nothing outside the repo is reachable; picking the current root just closes the picker. Both file finders build their candidate lists via `fuzzyGotoCandidates`/`fuzzyUncommittedCandidates`, shared with `refreshFuzzyCandidates`: when a corpus rebuild or a changed git dirty set lands while the finder is open, the list is re-derived in place — typed filter preserved, selection re-found by path — so externally created or deleted files appear/vanish without reopening (and no "refreshing" hint is needed). The grep overlay's snapshot is deliberately not re-derived (its results stream under `grepGen`); reopening Ctrl+G re-snapshots. A workspace that is not itself a repo discovers nested roots with `FindNestedGitRepos` (see `applyWorkspaceRepos`) and, rooted at the workspace, polls `MergeRepoDirty` over all of them.
- `handleFuzzyKey` runs the overlay: Enter on a file opens it (flushing the burst first so the abandoned buffer stays in history) — on the line of an optional `#L<n>` anchor in the query — and Enter on a directory drills into it inside the finder. `fuzzySearchText` is the query without that anchor; filtering (`refreshFuzzy`), the sidebar mirror, and the overlay's match highlighting all use it.
- `fuzzySelectedPath` mirrors the finder's selection into the sidebar so the tree follows along.
- `closeFuzzy` releases the prepared corpus — it can be a few MB, and nothing keeps it useful between opens.

*Plus small helpers: `enterCommand`, `handleCommandKey`, `refreshFuzzy` — bar entry, standard bar editing, and filter refresh.*

#### keys_exec.go

- `runExecCommand` dispatches the `@exec >` bar: `copy`/`cp` variants, `cpfp`/`cpfpl`/`cpafp`, `jump`, `git scf|diff|blame`, `openapi`, `graphql`, and `tab`. Success returns to the previous mode; errors keep the bar so the input can be corrected. The git/preview verbs deliberately bypass `execPrevMode` on exit — Esc from those views lands in edit mode at the reviewed content.
- `execCopy` copies by argument: the selection, a 1-based line range, `all`, or `fpath` (the repo-relative path, falling back to the workspace path when the file is in no known repo); `execGit` fans out to the three git modes with argument validation. `cpfp` shares the repo-relative path via `repoRelativePath`; `cpfpl` appends the cursor line as `#L<n>` (the same anchor the query bar and finder accept, so a copied location pastes straight back); `cpafp` stays absolute.

*Plus small helpers: `enterExec`, `refreshExecSugs`, `handleExecKey`, `execCopyPath`, `execJump`, `parseJumpTarget`, `parseLineRange` — bar editing, suggestion refresh, and argument parsing.*

#### line_anchor.go

- `splitLineAnchor` separates a trailing GitHub-style line anchor (`#L8`, `#l8`, `#L8-L12`, `#L8-12`) from typed path text and returns the 1-based start line. Incomplete anchors (`#`, `#L`) are stripped too, so the popup and highlight stay steady while the anchor is typed.
- `openFileAtLine` is `openFileAt` plus a cursor jump to that line (clamped to the last line), overriding the remembered cursor/draft position. It only moves the cursor when the open actually succeeded, so a failed open from edit mode leaves the previous file's cursor alone.

*Plus: `jumpCursorToLine` — the clamp-and-anchor cursor move shared with `execJump`.*

#### exec_suggest.go

- `execSuggestions` completes the bar's trailing token from a fixed verb/argument table (plus live tab names), prefix-filtered case-insensitively; `acceptExecSuggestion` replaces the token and appends a space so multi-token completions chain (`git`⇥`diff`⇥).

*Plus: `splitTrailingToken`, `tabBaseNames` — tokenizing and candidate listing.*

#### keys_inspect.go

- `enterInspect` (Ctrl+T) opens the dashboard and fires `fetchDBInfoCmd` — store statistics involve real I/O (`BlobUsage` does per-record preads), so they load async.
- `runInspectCommand` dispatches the `@inspection >` bar: `db compact|relieve` (run via `maintCmd` in the background, guarded by `inspectBusy` against duplicates; completion re-fetches stats), `lsp enable|disable <lang|all>` and `syscolor <style>`.
- `inspectLSPCommand` persists the config change *first* (nothing half-applied on a failed write) and then applies it live through the registry — `m.cfg` itself is never mutated because its Languages map is shared with server goroutines.
- `inspectSyscolorCommand` validates against the curated style list, persists, applies via `syntax.SetStyle`, and invalidates every highlight cache so the new colors show immediately.

- `handleInspectKey` hands every key to the op-commands table while it has focus. On the op-commands section, `→` at the end of the bar input moves focus into the table.

*Plus small helpers: `inspectDBCommand`, `knownLanguages`, `inspectHint` (the status-bar hint for the current focus) — bar editing and validation.*

#### keys_opcommands.go

The inspect dashboard's op-commands table: a key/value list of saved shell command templates (`opTableState`) kept in the global `OpCommandStore` (`m.opStore`, swapped in by `WithOpCommandStore`). `m.opCommands` is the cached list, shared with the Ctrl+R overlay.

- `loadOpCommandsCmd` reloads the list off the UI goroutine. It runs when the dashboard opens, when Ctrl+R opens, and after every save or delete. The result lands as `opCommandsLoadedMsg`, which is dropped unless its `opCommandsGen` is current. It also refreshes the picker if the overlay is open.
- `handleOpTableKey`: ↑/↓ moves through the rows plus a trailing `+ new command` row. Enter edits the row. `d` asks `delete <name>? y/n` before deleting (asynchronously). ←/Esc returns focus to the menu.
- `handleOpEditKey` is the inline editor. Tab switches between the key and value fields, and the rest is cursor-aware typing. Ctrl+S calls `saveOpEdit`. Esc throws the buffer away, so the row shows its stored value again.
- Placeholder completion in the value field. `opSuggestions` asks `opcmd.Complete` for the fragment at the cursor (`{`, `{$`, `$`, `{$f`, …), so the menu derives from the text and cursor and needs no stored list. While it is open, ↑/↓ choose a candidate, Tab/Enter call `acceptOpSuggestion`, and Esc hides the menu (`suggestHidden`) until the next edit, so the first Esc never reverts the row. `acceptOpSuggestion` replaces the fragment with the full placeholder and absorbs a `}` already typed after the cursor.
- `saveOpEdit` validates before writing anything, checking the key before the value. The key must be non-empty and have no spaces. It must also be unique (`opEditDuplicatesName`; keeping a row's own name is fine). A duplicate raises the editor's centred alert (`messageOverlay`), not just a status-bar line. Dismissing the alert keeps the edit open. The value must be non-empty and pass `opcmd.Parse`. Other failures keep editing open with the error in the bar. The `PutOpCommand` write then runs in a Cmd.

*Plus small helpers: `startOpEdit`, `switchOpEditField`, `opEditPaste`, `handleOpCommandsLoaded`, `handleOpCommandSaved`.*

### Git views

#### diff_mode.go

- `enterDiff` (from `git diff [rev]`) validates the revision (rejecting `-`-prefixed input that would reach git's argv as an option), bumps `diffGen`, and switches to diff mode while `computeDiffCmd` works. The file's own repo is resolved with `gitScopeFor`, so a file inside a nested repo of a workspace diffs against *its* repo, not the opened directory.
- `computeDiffCmd` snapshots the buffer lines and, off the UI goroutine, runs git from the file's repo folder with the repo-relative path (`show rev:path`), then runs the Myers diff; a repo with no commits or a path absent in the base becomes a "new file" (everything added) rather than an error.
- `handleDiffReady` lands the result behind the gen/rel/mode guard. A fresh entry keeps the user's exact place (same buffer line, same on-screen row — the exact inverse of `exitDiff`'s mapping); a Ctrl+O return restores the remembered review position instead.
- `buildDiffRows` converts the edit script into display rows: ctx/add rows index into the live buffer (no text duplication), del rows carry the removed text and point at their successor buffer line — the anchor Esc and Ctrl+J use.
- `diffRowForBufLine` maps buffer line → display row by binary search (bufLine is non-decreasing by construction).

*Plus small helpers: `clearDiffState`, `diffRowBufLine`, `diffRowText`, `splitBufferLines`, `firstStderrLine` — state reset, row/text lookups, and git error extraction.*

#### blame_mode.go

- `enterBlame`/`computeBlameCmd` mirror the diff pair, but blame runs with `--contents=-` so it annotates the *buffer*, not the worktree file — unsaved edits surface as "uncommitted" lines, the same "the buffer is the truth" rule diff mode follows. Like diff, blame resolves the file's own repo via `gitScopeFor` and runs `blame -- <repo-relative path>` from that repo.
- `parseBlamePorcelain` decodes `git blame --porcelain`: attributes arrive only on a sha's first appearance, so they're cached per sha; zero-sha lines become uncommitted rows, and contiguous same-commit runs get a group id (the Shift+↑/↓ jump target).
- `handleBlameReady` lands the rows (padded/clamped to reconcile git's line counting with the buffer's trailing empty element) with the same fresh-entry vs pending-restore positioning as diff.

*Plus small helpers: `clearBlameState`, `lastBlameGroup`, `isHexString`, `restStartsWithDigit`, `blameAuthorWidth`, `blameRowText` — parsing guards and width/text lookups.*

#### conflict_mode.go

Conflict mode has no display model of its own: it browses the live buffer through the edit session's cursor and scroll, so Esc needs no position mapping.

- `enterConflict` parses the buffer for well-formed conflict blocks (no git required — markers can come from patch output too) and flushes the burst so pending typing becomes the first apply's undo pre-state.
- `applyConflictChoice` is the only edit path in the mode: Enter on a marker line splices the chosen side in (diff3 base always dropped), as one undoable snapshot, then re-parses the remaining blocks since every index shifted.
- `syncConflictChoice` realigns the inline popup with the cursor after every move — landing on a new block resets the choice, leaving the markers closes it.
- `jumpConflictBlock` moves between blocks' `<<<<<<<` lines with the ~30% anchor.

*Plus small helpers: `clearConflictState`, `conflictBlockOnMarker`, `conflictOptionLabels` — state reset, marker hit-testing, and popup labels.*

#### gitconflict.go

Pure functions over line slices — no Model involved, which keeps them independently testable.

- `findConflictBlocks`/`parseBlockAt` scan for well-formed `<<<<<<< / ||||||| / ======= / >>>>>>>` regions, skipping malformed sequences silently (this drives an edit action, not a linter).
- `resolveConflicts` splices each block's chosen side back in, back-to-front so earlier indices stay valid, without mutating the input.

*Plus: `hasMarker`, `markerLabel` — marker predicates.*

#### keys_diff.go

- `handleDiffKey` is the closed read-only switch: arrows move the review cursor, Shift+↑/↓ jump hunks, Ctrl+J/O join the jump ladder, Esc exits — everything else is inert.
- `exitDiff` maps the cursor row back to its buffer line while preserving its on-screen row, so the page doesn't shift even when del rows compress away.
- `diffJumpToReference` syncs the edit cursor to the row's buffer position and reuses the whole jump ladder; del rows refuse (their text no longer exists in the buffer, so an LSP query there would silently resolve the wrong identifier).

*Plus small helpers: `moveDiffCursor`, `jumpDiffHunk`, `pageDiff` — cursor movement, hunk jumping, and paging (each the diff twin of an edit-mode function).*

#### keys_blame.go

- `handleBlameKey`, `exitBlame`, and `blameJumpToReference` mirror the diff versions but with the identity row↔line mapping (blame rows are buffer lines), which makes exit and jump trivial.

*Plus small helpers: `moveBlameCursor`, `jumpBlameGroup`, `pageBlame` — movement, commit-group jumping, and paging.*

#### keys_conflict.go

- `handleConflictKey`: another closed switch — the only buffer edit is Enter on a marker (`applyConflictChoice`). On a marker line ←/→ cycle the popup option; elsewhere they move the cursor like edit mode.

*Plus small helpers: `exitConflict`, `moveConflictCursor` — exit (nothing to map back; resolutions already live in the buffer) and movement with popup realignment.*

#### Git status handling (app.go)

Covered above under the core section: `maybeGitRefresh`, `refreshGitStatusCmd`, and the `gitStatusTickMsg`/`gitStatusMsg`/`FocusMsg`/`BlurMsg` branches of `Update` form the background poll that keeps the sidebar's yellow "uncommitted" markers honest against changes made by other processes, with an idle/blur pause and a once-per-transition failure notice. The poll doubles as the search index's freshness signal: when the porcelain key-set changes (`dirtySetChanged`), the handler re-derives an open Ctrl+U list and runs `ensureCorpus` (TTL-gated), so files created or deleted externally surface in search within one poll interval without any keystroke; `FocusMsg` does the same nudge, which is the only passive trigger in a non-git root.

### Document previews

#### preview_mode.go

The shared engine for the OpenAPI and GraphQL previews. The two modes were byte-identical apart from three seams — detection, document production, and outline row shape — so each is a `previewKind` descriptor (a struct of function fields) driving one state machine and one `previewState` on the Model.

- `enterPreview` validates via the kind's `detect`, bumps `preview.gen`, and fires the kind's `produce` Cmd.
- `handlePreviewReady` lands the rendered document (guarded by mode+gen+rel, since both kinds share one generation counter) and positions the cursor on the row nearest the edit cursor's source line — so entering and Esc'ing straight back roughly round-trips.
- Every rendered row carries a `{file, line}` source anchor — including rows that came from cross-file `$ref`s or sibling schema files — which is what makes Esc a real source jump.

*Plus small helpers: `previewDesc`, `clearPreviewState`, `previewRowForSource`, `previewSrcAt`, `previewMatches`, `movePreviewCursor` — descriptor lookup, state reset, and anchor/match/cursor plumbing.*

#### keys_preview.go

- `handlePreviewKey`: the closed read-only switch — cursor movement, Shift+↑/↓ over the outline, Enter jumping to the selected outline entry, `/` or Ctrl+F opening the in-preview search.
- `exitPreview` (Esc) jumps to the source of the row under the cursor via `jumpToLocation` — content from another file opens that file, and a Ctrl+O frame is pushed either way.
- `handlePreviewSearchKey` is a search-only bar (no replace — the document is read-only); `syncPreviewSel` keeps the outline selection tracking the document cursor by binary search.

*Plus small helpers: `focusPreviewMatch`, `cyclePreviewMatch`, `landPreviewMatch`, `pagePreview`, `jumpPreviewOutline` — match focusing/cycling/landing, paging, and the outline jump.*

#### openapi_mode.go

- `openapiKind` plugs OpenAPI into the engine: detection is content-based (any buffer declaring `openapi: 3.x`). `renderOpenAPICmd` parses and renders off the UI goroutine, resolving cross-file `$ref`s lazily through a reader jailed to the project root (`filetree.ReadViewFile`).

*Plus: `firstErrLine` — one-line yaml errors for the one-row status bar.*

#### graphql_mode.go

- `graphqlKind` plugs GraphQL in: detection is filename-based (`.graphql`/`.gql`/`.graphqls`). `renderGraphQLCmd` gathers the schema file set (directory listing, graphqlrc globs), parses all sources — the open file from the captured buffer snapshot so unsaved edits show, siblings from disk — and renders, all through root-jailed readers.

#### render_preview.go

- `renderPreview` windows over the rendered rows, tints the cursor row, and overlays search matches through the same `renderSearchLine` pipeline the search view uses. The outline itself is a `sidebarList` built by `previewSidebarList` (group headers plus badge+tail rows) and drawn by `renderSidebarList`.

*Plus: `renderPreviewStatus` — the search-or-position status row.*

### Overlays

#### keys_grep.go

Repo-wide content search (Ctrl+G). Everything expensive is async and generation-guarded: the file snapshot streams in per-batch Cmds, and searches are debounced then scanned in the background.

- `openGrep` opens the overlay immediately and starts the snapshot load; it refuses from a dirty edit buffer (Enter on a hit opens another file, which would silently discard edits) and from a cold corpus.
- `grepLoadBatchCmd` reads one 512-file batch of the corpus with a bounded worker pool; `handleGrepBatch` appends it (capped at 10k files / 200MB), schedules the next batch, and re-searches the grown prefix when a query is active — completion always fires a final covering search, the guarantee that displayed results span the whole snapshot.
- `queueGrepSearch` registers a query change: a generation bump invalidates earlier debounce ticks so only the last keystroke's tick (100ms) fires a scan; old results stay displayed until new ones land, so no flicker.
- `grepSearchCmd` scans the snapshot with a worker pool sharing one compiled regex (regexp pools match state internally since Go 1.12), merging per-worker hits in corpus order.
- `appendGrepHits` scans whole content instead of line-at-a-time (regexp's literal-prefix fast path), resuming at the next line start after each hit so same-line matches dedupe and empty-width matches still terminate.
- `handleGrepKey` supports a *multi-line* query (Ctrl+J inserts a newline; ↑/↓ move within the query before falling through to the result list); Enter opens the selected hit at its line.
- `closeGrep` releases the snapshot — it can hold hundreds of MB.
- `refreshGrepPreview` keeps the selected hit's preview current: the line split lands synchronously (the preview text must render this frame) and the whole-file tokenize runs in a `tea.Cmd`, landing as `grepPreviewMsg` guarded by `grepGen` + the selected rel — a per-arrow-key synchronous tokenize stalled key handling on large files. Rows render plain until it lands.
- `grepSelectedFile` resolves the selection via `grepFileIndex` (rel → snapshot index, maintained as batches land) — the renderer calls it every frame, so a linear scan over up to 10k files was per-frame work.

*Plus small helpers: `handleGrepTick`, `handleGrepResults`, `handleGrepPreview`, `buildLineStarts`, `lineForOffset`, `grepPaste`, `grepInsert`, `grepLineCol`, `grepOffsetAt`, `grepMoveCursorLine` — tick/result landing, offset math, and query-cursor plumbing.*

#### keys_opmode.go (Ctrl+R)

The operation overlay runs a saved op-command against the open file. It only opens while a file is open, because `{$fpath}` and `{$dpath}` need one. `opSystemValues` builds both from the *workspace*-relative path (`activeRepo` joined with `openRel`), not the Ctrl+W repo-relative one, because commands always run from the workspace directory. It moves through three stages (`opModeState.stage`):

- **pick**: fuzzy search over command names. This reuses `fuzzy.Prepare`/`Filter`, and rows show the template dimmed. The query is `name [args…]`: `opQueryParts` splits it at the first space, and only the name part filters. `setOpQuery` re-filters and resets the selection only when the name part changes, so typing args never moves the chosen row. With inline args, the box previews the exact command line, or the error (`opInlinePreview`). Enter calls `selectOpCommand`:
  - With inline args that fit the template (`test 10`), the command runs at once. If they don't fit, the args stage opens pre-filled with them.
  - Without inline args, a template with no `{$n}` runs immediately; otherwise the overlay moves to the args stage.
- **args**: the user types the arguments. `renderOpCommandLine` splits them with `opcmd.SplitArgs`, adds the system values, and renders the template every frame, so the box shows either the exact `$ command` or the error (`needs 2 args, got 1`). Enter runs it. Esc goes back to the picker with the query kept.
- **run**: `runOpCommand` bumps `opRunGen` and starts the process (see opmode_run.go). `handleOpRunMsg` appends output and re-arms the wait until the done event arrives. A message with an older generation is dropped, which also ends its wait chain. While the run view is open, ↑/↓/PgUp/PgDn/Home/End scroll back through the output; End returns to following the tail.

`closeOpMode` (Esc, or `quit`) cancels a still-running process and bumps `opRunGen`, so the cancelled run's last messages are dropped.

*Plus small helpers: `openOpMode`, `refreshOpMatches`, `handleOpArgsKey`, `handleOpRunKey`, `opRunState.status` (the footer text, and whether it is green or red).*

#### opmode_links.go

Clickable links in the run output.

- `findOutputLinks` finds `http(s)://` URLs in a display line and returns their rune spans. Sentence punctuation after a URL is trimmed. A closing `)`, `]` or `}` is trimmed only when the URL didn't open it, so `(see https://x.dev/a)` loses the `)` but `…/Go_(language)` keeps it. It runs on the *tab-expanded* line (`opDisplayLine`), so rune columns are screen columns.
- `handleOpMouse`: while the op overlay is open it owns the mouse (`handleMouse` routes to it before the generic overlay early return). Only a left click in the run stage acts. A click on a link sets the `opening <url>` notice and opens the URL through the injectable `m.openBrowser` (`browser.Open`) on a Cmd. A failure lands as `opLinkOpenedMsg` and shows in the status bar.
- `opRunLinkAt` maps a cell back to a link using the same `opRunLayout` the renderer draws with. The main pane starts at `(sidebarWidth+1, 2)`. The box is centred the way `lipgloss.Place` centres (floor of half the gap), then the border and padding are skipped. From there it finds the output row, the line through `visibleRange`, and the link span under the column. A link cut off at the box edge still opens its full URL.

#### opmode_run.go

- `opRunState.execute` runs `sh -c <command line>` in the workspace directory (`workspaceRoot`, never the Ctrl+W repo root). stdin is `/dev/null`, so an interactive tool can't hang. stdout and stderr are merged into one pipe. The process runs in its own process group, so cancelling SIGTERMs the shell's children too. `WaitDelay` limits how long a child that ignores the signal, or keeps the pipe open, can block.
- A reader goroutine sends output chunks into a bounded channel, so a fast producer is slowed down rather than buffered without limit. Sends give up once the run is cancelled, because nobody drains the channel after Esc and a blocked send would leak the goroutine.
- `next` blocks for one event, then folds whatever else is already queued into the same `opRunMsg` (up to 64 KB). A chatty process therefore costs a few frames, not one frame per write.
- `opRunOutput` turns raw output into display lines. It strips ANSI escapes and treats CRLF as a newline, including when the CR and LF arrive in different chunks. A bare CR rewinds the current line, so progress bars redraw in place. Only the last ~5,000 lines are kept, trimmed in batches.

*Plus small helpers: `newOpRun`, `startOpRunCmd`, `waitOpRunCmd`, `send`, `pushLine`, `displayLines`.*

#### jump.go (definition picker)

- `jumpToCandidates` handles the 0/1/many outcome shared by every lookup: zero errors, one jumps, many open the picker overlay (capped at 50) with a preview and a precompiled token-highlight regex.
- `refreshDefPickPreview` fires the selected candidate's preview load — a disk read plus whole-file tokenize — as a `tea.Cmd`, landing as `defPickPreviewMsg` guarded by `defPickGen` + the selected rel (each arrow key changing the file used to pay both synchronously in the key handler). Re-fires only when the selection changes file.

*Plus: `handleDefPickKey` — ↑/↓/Enter/Esc over the picker.*

#### render_overlay.go

- `renderGrepOverlay` is the largest overlay: top ~60% a syntax-colored preview of the selected hit, a divider, then the (possibly multi-line) query input and the result list, with loading/searching states in both panes.
- `renderPreviewRows` renders a highlighted code window with the target line ~40% down and regex matches overlaid — shared by the grep overlay and the definition picker, which is why the two previews look identical.
- `renderGrepInputRows` renders the multi-line grep query with the cursor on its own line/column, windowing long queries around the cursor.
- `renderFuzzyOverlay`/`renderFuzzyRow` draw the Ctrl+P/Ctrl+U finder, computing matched-rune bold positions only for the visible rows rather than during filtering, and rendering contiguous matched/unmatched runs in one lipgloss call each (a call per rune was width×rows ANSI emissions per frame).
- `renderSplash` is the cold-start page: name, version, and an animated indexing spinner while the index builds.

*Plus small helpers: `renderMessageOverlay`, `renderConfirmRmOverlay`, `renderDefPickOverlay` — centered modal boxes.*

### LSP integration

#### complete.go

- `requestCompletion` syncs the server to the current buffer via a direct `DidChange` (not `flushBurst` — completion must not fragment the undo history mid-word; the content comes from the rev-keyed `contentHashed` memo, not a fresh join) and fires an async `textDocument/completion`; `handleCompletion` drops the answer if the file, cursor line, or identifier start moved while it was in flight.
- `filterCompletions` narrows the server's raw list by the identifier prefix under the cursor, sorted by SortText; `acceptCompletion` replaces the prefix by selecting it and letting `insert` overwrite.
- `afterEditType` is the popup lifecycle after each typed rune: `.` and identifier chars (re)request or refilter, `(`/`)` manage the signature pin, anything else closes the popup and lifts an Esc suppression.
- `pinSignatureOnParen` implements the pinned signature row: typing `(` after a known function keeps a one-row label+signature overlay visible while arguments are typed, matching the word before the paren against the popup's candidates (or the just-accepted item). `sigCloseParen`, `sigAfterBackspace`, and `sigCheckCursor` track paren depth and cursor position so the pin drops exactly when the call is closed or left.

*Plus small helpers: `isIdentRune`, `identStart`, `isIdentifierText`, `completionDetail`, `completionOrigin`, `completionSortKey`, `completionKey`, `afterEditBackspace`, `dismissPopup`, `closeCompletion`, `sigUnpin`, `completionMatchesWord` — identifier predicates, column extraction, and popup/pin state resets.*

#### jump.go (Ctrl+J / Ctrl+O)

- `jumpToReference` (Ctrl+J) is a priority ladder: a path-shaped token that names a real file opens it (servers don't resolve bare paths); a cursor on an identifier queries `textDocument/definition`; a cursor on nothing tries a quoted path elsewhere on the line, then snaps to the nearest identifiers and retries (bounded at 4).
- `handleDefinition` lands the async answer — LSP-strict: when a server exists its answer is final, no plausible-but-wrong heuristic fallback. A definition resolving to the cursor's own line (or an empty answer on an identifier the cursor sits on, as kotlin-language-server produces on declarations) pivots to `requestReferences` — the "who uses this declaration?" question. `handleReferences` lands that, excluding the definition line itself. Both accept diff/blame modes too, since Ctrl+J there fires the same lookups, and both drop an answer whose tagged `rel` no longer matches the open file — a tab switch mid-flight would otherwise interpret it against the wrong buffer. Rooted at a Ctrl+W repo, a definition the server found only in another repo (`collectInRootCandidates` drops everything outside the root) reports "definition is outside repo x" rather than "not found".
- `resolveJumpPath` tries a token as a file path relative to the current file's directory then the root, stat-first and root-jailed, probing configured language extensions and `index.*` files for extensionless imports.
- `jumpLinePath` scans the cursor line's quoted spans for one that resolves to a real file — the fallback that makes Ctrl+J work on import lines even when no language server answers.
- `jumpToLocation` pushes the origin frame (including a diff/blame-review origin, base and position) and lands at the target — same-file jumps just move the cursor; cross-file jumps go through `openJumpFile`, and a failed jump leaves no stack residue.
- `jumpBack` (Ctrl+O) pops the trail; a frame that originated inside a diff or blame view *re-enters* that view, re-deriving it (the buffer may have changed at the jump target) with the pending position restored when the recomputed rows land.
- `openJumpFile` is `openFileAt`'s jump-flavored sibling: same draft-stash/restore guarantees, but it keeps the jump stack intact and lands at an exact position.

*Plus small helpers: `jumpToken`, `looksLikePath`, `statRegular`, `probeExtensions`, `collectInRootCandidates`, `maxJumpTries`, `noServerError`, `requestDefinition`, `lspLookupError` — token extraction, path probing, candidate collection, and error wording.*

Diagnostics themselves land in `Update`'s `lsp.DiagnosticsMsg` branch (app.go), keyed by rel path, and render as gutter dots and status-line counts (render.go's `diagSummary`/`diagAtLine`).

### Rendering

#### render.go

- `View` wraps `render()` in a `tea.View` with alt-screen, mouse cell-motion, and focus reporting (which is what feeds the git-poll pause).
- `render` assembles the frame: header, sidebar pane, main pane chosen by overlay-then-mode priority, tab strip, and status rows — panes tile the full width so no terminal-default stripe shows. The sidebar body is always `renderSidebarList` over whatever `sidebarListForMode` built.
- `renderHeader` builds the top row: the version and the opened directory, then — when Ctrl+W has rooted the editor at a nested repo — a bold orange `working repo: x` label directly after the path (left side, where the eye already is, not pushed to the far edge of a wide terminal). Styling the label apart from the title means the row is padded by hand rather than through `Width()`, and a row wider than the terminal cuts the title first so the repo name survives.
- `renderStatusLine` is the per-mode bottom bar; `renderEditStatus` the edit variant (filename, saved/editing, diagnostics, position, hints). The `@exec`/`@inspection` bars pre-pad in their own background so the chrome padding doesn't repaint them.
- `renderFile` is the main file renderer: line-number gutter with diagnostic dots, viewport following the cursor via `fileViewportTop` (the shared function that keeps renderers, paging, and mouse math agreeing on what's on screen), the cursor line via `renderEditLine`, and — in edit mode — the completion dropdown or pinned signature spliced over the rows.
- `renderEditLine` draws the cursor line with a horizontal window that follows the cursor and at most five style runs (line-highlight / selection / cursor cell) instead of a Render per column — a deliberate ANSI-call-count optimization repeated across the renderers.
- `renderSearchLine` renders a line with syntax segments, match backgrounds, and replace-preview spans layered by priority (preview > matches > syntax), batching consecutive same-style columns into runs; it serves the search view, the previews, and both overlay code panes.
- `overlayCompletion`/`overlaySignature` splice the dropdown/signature box onto rendered rows with ANSI-aware slicing, anchored at the identifier/call column, flipping above the cursor when there's no room below; `completionLayout`/`completionRow` size and draw its label/signature/origin columns.
- `renderSegments`/`renderSegmentsBg` window styled segments through a rune range, threading a row background so diff/conflict tints cover text and EOL padding alike; `segStyleWithBg` memoizes the lipgloss style per (color, background, attrs) combination in the package-level `segStyles` map — renderSegments runs for every visible row of every frame.
- `renderSearch` draws the search body: scrolls to the focused match (or holds the edit position when there are no matches yet) and, in search-exec mode, splices the live replace preview in. The line split and per-line match buckets come from `matchCache`'s memos rather than being rebuilt per frame.
- The bottom of the file holds the entire Gruvbox-derived palette and every lipgloss style — the single place colors are defined.

*Plus small helpers: `padStatusRows`, `diagSummary`, `diagAtLine`, `withNotice`, `renderExecSugs`, `renderQueryMain`, `renderQuerySuggestions`, `renderTabStrip` (window math delegated to `tabStripWindow`/`tabLabel`, shared with the tab click hit-test), `fileViewportTop`, `renderContentLine`, `plainWindow`, `plainWindowStyled`, `renderSelectedLine`, `clampByte`, `renderInputLine`, `renderInputLineStyled`, `padTo`, `truncateRunes`, `pad`, `segStyleFor`, `colorFor` — row assembly, windows, input-line drawing, and padding/truncation utilities.*

#### sidebarlist.go

The left pane is one list. File tree, inspection menu, and preview outline each build rows; scrolling, the selection bar, and click-to-row live here so a new sidebar behavior is added once.

- `sidebarListForMode` picks the list: `inspectSidebarList` (the menu names), `previewSidebarList` (group headers, or a colored badge plus a tail), or `fileTreeSidebarList` (tree labels and the uncommitted / open / dimmed / directory colors). `selectedIndex` is whatever that mode already chose — the typed-path highlight, `inspectMenu`, or `preview.sel`.
- `renderSidebarList` draws the visible window. A selected row is one selection-bar label; an unselected row keeps each segment's own style, and the last segment fills the remaining width.
- `sidebarListClickIndex` maps a cell to a row with `sidebarWindowStart`, the same centering the renderer uses (a negative index starts at the top). `activateSidebarRow` then acts: query and edit open a file or expand a directory (popup stays hidden; a directory click from edit stashes the draft), inspection selects that menu row, and a preview jumps via `jumpPreviewOutline`. Diff, blame, search, conflict, and the command bars draw the file tree but leave the click as a miss.

*Plus small helpers: `sidebarWindowStart`, `renderSidebarRow`, `sidebarRowPlain`, `sidebarSegmentText`, `fileTreeEntryStyle`, `activateFileTreeRow` — window math, row painting, tree colors, and the file-tree click itself.*

#### render_diff.go

- `renderDiff` draws the unified review: buffer line numbers that advance only past ctx/add rows, `+`/`-` markers in the gutter spacer column, green/red row tints, and ctx/add rows reusing the edit session's highlight cache (valid because diff mode blocks edits).
- `renderDiffCursorLine` is the cursor-row renderer shared with blame and conflict modes: renderEditLine's window walk in three style runs, with the row's own tint instead of an unconditional cursor-line repaint.

*Plus small helpers: `renderDiffBufLine`, `diffCursorRowStyle`, `renderDiffStatus` — cached-vs-plain row drawing, cursor fill choice, and the status row.*

#### render_blame.go

- `renderBlame` draws the buffer with an author+date gutter instead of line numbers — authors in aqua, dates receding into gutter gray, uncommitted lines as a dimmed placeholder — reusing `renderDiffBufLine`/`renderDiffCursorLine` for the content.

*Plus small helpers: `blameGutterWidth`, `renderBlameStatus` — gutter width and status row.*

#### render_conflict.go

- `renderConflict` tints the live buffer by region — ours green, theirs blue (red would read as "removed"), diff3 base gray, marker lines bold yellow — and overlays the inline Use-ours/theirs/both picker on the cursor's marker row.
- `conflictLineKindAt` classifies a buffer line against the parsed blocks in one early-exit scan.
- `overlayConflictPopup` splices the one-row picker right after the marker text with the same ANSI-aware slicing as the completion overlay — on the row it acts on, so it can never fall off the viewport.

*Plus small helpers: `conflictCursorRowStyle`, `renderConflictStatus` — cursor fill and status row.*

#### render_inspect.go

- `renderInspectMain` routes to the selected panel: `renderInspectDB` (record counts, live/dead log bytes, blob usage, generation warnings), `renderInspectLSP` (per-language running/stopped/disabled status from the registry), `renderInspectSystem` (version and syntax style), `renderInspectOpCommands` (render_opcommands.go).

*Plus small helpers: `humanBytes`, `percent` — number formatting. The left menu is `inspectSidebarList` in sidebarlist.go.*

#### render_opmode.go

- `renderOpOverlay` draws the Ctrl+R overlay for its stage.
  - **Pick:** a box like the fuzzy finder, with the template dimmed after each name.
  - **Args:** the template, the args input, and the live `$ command` preview in green, or the error in red. The preview wraps over a few lines so long paths stay readable.
  - **Run:** a large box with the command line, the output tail (or a scrolled-back window that never leaves empty space at the top), and a footer. Its geometry is `opRunLayout` (`newOpRunLayout`, `visibleRange`), shared with the click hit-test. Output lines draw links underlined in blue (`renderOpOutputLine`). When a visible line has a link, the divider above the footer reads `click a link to open it`. The footer is yellow while running, green `✓ finished (exit 0)`, or red with the exit status.
*Plus: `wrapRunes` — hard wrap with a trailing `…` past the line cap.*

#### render_opcommands.go

The op-commands panel in the inspect dashboard.

- `renderInspectOpCommands` draws the panel:
  - A title. The `press → to edit` hint shows until the table has focus.
  - A rounded-border table with a KEY and a COMMAND column (`opTableLayout`). The key column fits the longest name, capped at a third of the width.
  - When there are no commands, the table body explains what op-commands are and shows an example.
  - A `+ New command` button row, which becomes a solid green pill when selected.
  - A placeholder legend.
  - The body scrolls so the selected row, and its dropdown, stay visible.
- Command rows are separated by `rowRule`: a quieter `├──┼──┤` drawn in the selection grey, joined to the table's own borders. A dropdown stays attached to its edit row, with no rule in between. While a typed key duplicates another command, the key turns red and a line under the table says so.
- Each row uses one palette (`opRowStyles`): normal, selected (a solid selection bar marked `▸`), editing (dimmed, marked `✎`), or input (the active cell, drawn as a darker inset with a cursor block). Every text run carries the row's background, so a highlight is never broken by an inner style reset.
- `renderOpCell` renders a cell at an exact width. Input cells scroll horizontally to keep the cursor visible; other cells end in `…` when cut. `opTokenClasses` colours placeholders per rune using `opcmd.Parse`: `{$n}` arguments orange, system variables blue, broken or half-typed placeholders red.
- `renderOpSuggestionRows` draws the completion menu as a dropdown in the COMMAND column under the edit row, styled like the LSP completion popup.

*Plus small helpers: `opTableBody`, `renderOpEditRow`, `opTableEmptyLines`, `opNewButton`, `opLegend`, and the layout's `border`/`row`/`spanning`. The inspect bar also hides its own cursor while the table has focus (`renderInspectInput`), so only one cursor is ever visible.*

### Mouse

#### mouse.go

- `handleMouse` routes mouse input: left-click (and Ctrl+click = jump-to-definition) places the cursor; the vertical wheel scrolls. Drags, releases, and horizontal wheel are deliberately ignored so a trackpad swipe never moves the cursor. Overlays own their own navigation and swallow everything. Tab-strip and sidebar clicks are checked first (they sit above the mode-specific content handlers); misses fall through untouched.
- `editClickTarget` maps a terminal cell to a buffer position by mirroring View/renderFile's exact layout math — header, borders, tab strip, `sidebarWidth` (the single source of truth shared with View so click math can't drift), gutter, viewport, and the cursor line's horizontal window. `diffClickTarget` and `blameClickTarget` are the same math over their own rows and gutters.
- `handleEditClick` anchors the viewport explicitly so a click never drags the view: an ordinary click freezes the window, clicking the top visible line pages up, the bottom visible line pages down. `handleDiffClick`/`handleBlameClick` reuse the same anchoring.
- `handleSidebarClick` asks `sidebarListClickIndex` which row was hit, then `activateSidebarRow` (both in sidebarlist.go). Rows start at y=2 with no tab offset — the strip lives in the main pane — and the window is the same one the list was drawn with. A miss falls through to the main-pane handler.
- `tabClickTarget` maps a cell on the strip row to a tab index via `tabStripWindow`, honoring the sliding window and treating the trailing fill as a miss; `handleTabStripClick` switches through `activateTab` (draft stash/restore, so unsaved tabs stay red) and swallows a click on the already-active tab.
- `wheelScroll` dispatches a wheel notch per mode — moving the cursor where the viewport follows it (edit/diff/blame/preview/conflict), nudging the scroll offset in the query view.

*Plus the shared layout constants — `sidebarWidth`, `statusRowCount`/`bodyHeight`/`sidebarInnerHeight` (a pinned test keeps `statusRowCount` agreeing with `renderStatusLine`), `overlayOpen`, and `tabStripVisible` — one source of truth each for render() and the hit-testers.*

### Persistence & lifecycle

The store touchpoints are spread across the features but follow one pattern: everything a relaunch needs is written *when it changes*, not at exit.

- Session and tabs: `New` restores tabs (with cursors and drafts) or the legacy last-file; `persistTabs` (tabs.go) writes on every tab mutation; `saveSession` and `quit` (app.go) capture the final state, stash a dirty buffer as a draft, and shut the language servers down.
- Snapshots and drafts: `pushSnapshot` writes every undo checkpoint through `store.SnapshotPut` (deleting orphaned/trimmed ones); `stashDraftIfDirty`/`restoreDraft` round-trip unsaved work; `TouchOpened` records recent files, which is what seeds the Ctrl+P recents ordering.
- The corpus: the `corpusMsg` handler in `Update` persists each fresh walk (`SaveCorpus`) so the next launch warm-starts, with `validateCorpusCmd` re-checking the adopted index's directory signature in the background.
- Maintenance: the inspection dashboard's `db compact`/`db relieve` are the only user-facing store maintenance, run async with a busy latch.

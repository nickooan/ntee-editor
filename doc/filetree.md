# internal/filetree

**Introduction**

`internal/filetree` is everything the editor knows about the project on disk. It builds the sidebar tree, walks the whole root to produce the fuzzy-search corpus, and is the only door through which files are created, read, written, or deleted. The app layer consumes its trees, corpora, and suggestions; it in turn leans on `internal/fuzzy` for ranking and `internal/gitcmd` for git status.

Three design constraints shape the package:

- **Everything is jailed to the root.** Every file operation resolves through `containedPath`, which checks containment both lexically and after symlink resolution. A symlink inside the root that points outside it is refused — read, write, and delete alike.
- **The tree rebuilds constantly, so walking must be cheap.** The sidebar is rebuilt on every keystroke/frame. Directory listings (`dirCache`) and compiled `.gitignore` matchers (`gitignoreCache`) are cached keyed by mtime, so a steady-state walk does almost no disk I/O.
- **Saves are atomic.** `WriteViewFile` writes to a temp file in the target's directory and renames it over the target, so a crash mid-save can never leave a truncated file.

Beyond the tree itself, the package handles gitignore matching (including nested, per-directory `.gitignore` files), a git-status "dirty set" that turns changed paths yellow in the sidebar, and the query-bar suggestion popup.

**Architecture**

The central type is `FileTreeEntry` — one visible row of the sidebar, carrying its depth, type, and two render flags: `Dimmed` (gitignored or soft-ignored, shown gray and excluded from search) and `Uncommitted` (in the git dirty set, shown yellow). Its `CommandValue` is the path as typed in the query bar: `rel+"/"` for directories, `rel` for files.

Two walks share the same machinery but serve different needs. `BuildFileTreeEntries` walks only *expanded* directories and produces the visible tree. `BuildAllEntries` walks *everything* (depth-capped at 16 as a symlink-loop guard) and produces the search corpus — matching must find files inside collapsed directories, so a full walk is unavoidable. Both walks read directories through `readDirectorySorted`, which serves from `dirCache` when the directory's mtime hasn't changed, and both thread a chain of directory-scoped gitignore matchers (`scopedGitignore`) down the recursion so nested `.gitignore` files apply with git's last-match-wins semantics. Compiled matchers live in `gitignoreCache`, keyed by the `.gitignore` file's own mtime — content edits don't bump the parent directory's mtime, so the file gets its own key.

Ignoring comes in two strengths. Hard-ignored names (`.git`, plus config `tree.ignore`) never appear anywhere. Soft-ignored names (`node_modules`) show up dimmed in the tree but stay out of the corpus — one workspace had 1200+ `node_modules` directories, so this is load-bearing.

The files divide the work cleanly:

- `filetree.go` — the walks, caches, ignore sets, viewport/selection helpers, and repo/project-root discovery.
- `create.go` — the path jail (`containedPath`, `resolveInsideRoot`) and create/delete operations.
- `viewfile.go` — jailed reads and atomic writes of file content.
- `gitignore.go` — the `.gitignore` compiler and matcher.
- `gitstatus.go` — the uncommitted-changes set, via `gitcmd`.
- `inputsuggest.go` — query-bar completion, via `fuzzy`.

**Functions**

### filetree.go

`BuildFileTreeEntries` builds the visible sidebar rows. It descends only into directories listed in `expanded`, skips hard-ignored names outright, and flags entries as `Dimmed` (gitignore/soft-ignore, inherited downward once a directory dims) or `Uncommitted` (present in the dirty set).

`BuildAllEntries` is the corpus walk: every regular file's relative path, regardless of expansion, with hard-, soft-, and gitignored entries all excluded. It also returns a map of every visited directory's mtime — a signature callers can persist and stat-sweep later to decide whether the corpus is stale — plus a `truncated` flag when the `maxFiles` cap is hit.

`readDirectorySorted` is the walk's disk interface: it stats the directory, serves the cached listing when the mtime matches, and otherwise re-reads and re-sorts (directories first, then by name).

`ClearDirCache` empties both caches so the next walk hits disk — the manual `:refresh` escape hatch for changes mtime comparison can't see (like a same-second external edit).

`chainIgnored` asks a shallow-to-deep chain of directory-scoped matchers whether a path is ignored; a deeper file's opinion wins, including `!` re-includes. `loadNestedGitignore` compiles a directory's own `.gitignore` through the mtime-keyed cache, and `extendChain` appends it to the chain — using a full-slice-expression append so sibling recursions can't clobber each other's backing array. `hasGitignore` lets the walk skip all of that for the vast majority of directories that have no `.gitignore` at all.

`FindRepoRoot` walks up from a file to the nearest `.git` (bounded by the editor root); `FindProjectRoot` does the same for language-project markers (`go.mod`, `package.json`, `Cargo.toml`, …) so a language server in a monorepo scopes to the sub-project, not the whole repo. `FindNestedGitRepos` lists every directory under a workspace root that contains a `.git` entry (the root itself excluded), using the same cached listings as the tree — this is what Ctrl+W offers.

`BuildExpandedDirectoryPaths` turns a typed command path into the set of directories to expand — every ancestor, plus the last segment when the path ends in `/`. `FindFileTreeMatchIndex` picks the entry a typed input refers to (exact beats prefix beats substring), and `ResolveHighlightedEntry` falls back to the nearest expanded ancestor directory when nothing matches.

`BuildFileTreeViewport` windows the entry list to the visible height, centering the highlighted row.

*Plus small helpers: `isInsideRoot`, `hardIgnored`, `softIgnored`, `IsGitRepo`, `DirsFromMtimes`, `ResolveNextFileTreeSelectionIndex`, `ResolveSidebarCommand`, `ResolveParentDirectoryCommand`, `FormatFileTreeEntryLabel`, `splitNonEmpty`, `padRight` — containment/ignore predicates, walk-signature formatting, selection movement, and label rendering.*

### create.go

`containedPath` is the jail. It joins a root-relative path to the root, checks containment lexically, then resolves symlinks (including a possibly-symlinked root — macOS's `/tmp` → `/private/tmp`) and checks again in resolved space, carrying any not-yet-existing tail along verbatim. It returns both the lexical path (for display, and for acting on a link itself) and the resolved one; `ok` is false on any escape.

`resolveInsideRoot` layers on the stricter create/remove rules: absolute paths are rejected up front (a `filepath.Join` would silently swallow them), and the root itself is never a valid target. It returns the lexical path because `Remove` on a symlink must delete the link, not its target — but the link's resolution is still verified, so a link pointing outside the root is refused rather than removed.

`MakeDir` is `mkdir -p` inside the jail. `EnsureFile` is touch: it creates the file and any missing parents, and reports `created=false` (not an error) when the file already exists. `Remove` deletes a file or a whole directory tree, jailed as above.

### viewfile.go

`ReadViewFile` loads a file for viewing/editing through the jail. Read errors surface *as the file content* (the caller just displays them), and `looksBinary` — the classic NUL-byte-in-the-first-8KB heuristic — flags native files so the editor doesn't render garbage.

`WriteViewFile` is the atomic save: temp file in the target's directory, content written, permissions copied from the existing file (0644 for new ones), then a rename over the target. Saving through an in-root symlink replaces the resolved file and leaves the link intact.

`ListDirFiles` lists the non-directory names in one jailed directory, in `os.ReadDir`'s sorted order.

### gitignore.go

`Gitignore` matches directory-relative slash paths against one `.gitignore` file — a pragmatic subset of the spec covering comments, negation (`!`), directory-only trailing `/`, anchoring, and `*`/`?`/`**` globs, with last-match-wins ordering. Note it drives a visual cue and a search filter, not a hard exclusion from the tree.

`MatchState` is the chaining-friendly matcher: it distinguishes "no rule applied" from "matched, and here's the verdict," so a deeper `.gitignore`'s opinion only overrides when it actually has one. `Match` is the plain boolean wrapper.

`compileGitRule` parses one line into a regexp-backed rule, and `globToRegexp` does the glob translation — `**` spans segments (with the trailing-slash collapse for `**/`), `*` stays within one segment, `?` is a single non-slash rune.

*Plus small helpers: `LoadGitignore`, `CompileGitignore` — read/compile entry points; a nil `*Gitignore` matches nothing.*

### gitstatus.go

`GitDirtySet` shells out to `git status --porcelain -z --untracked-files=all` (via `gitcmd`, so it can't hang) and returns every dirty path *plus every ancestor directory* as a set — that pre-marking is what lets a collapsed directory render yellow with a plain O(1) lookup at walk time. `ok=false` means "not a repo or git failed"; callers treat it as feature-off. Run it off the UI goroutine.

`parsePorcelain` extracts repo-relative paths from the NUL-separated porcelain records, handling the extra origin-path record that rename/copy entries carry (both sides count as changes). It's a pure function, unit-tested without git. `markDirty` inserts one path and all its ancestors into the set. `MergeRepoDirty` runs `GitDirtySet` in each nested repo of a workspace and prefixes the paths so they match the workspace-rooted tree; ancestors above each repo are marked too.

### inputsuggest.go

`BuildInputSuggestions` powers the query-bar popup in three stages: exact and prefix matches run over the *visible* tree entries (preserving directory-path navigation), then a fuzzy stage runs score-ranked over the full file-and-directory corpus so keywords reach into collapsed directories. Results are deduped by path, ordered exact ++ prefix ++ fuzzy, and capped (the `MaxInputSuggestions` cap of 200 is deliberately large so ↓ can walk every match, not just one screenful).

`PrepareCorpus` pre-runs `fuzzy.Prepare` over the concatenated files+dirs corpus, for callers that keep it alive across keystrokes instead of re-preparing per call.

*Plus a small helper: `suggestionFor` — wraps a tree entry as a suggestion row.*

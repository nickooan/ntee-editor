# internal/store

**Introduction**

This package is the editor's memory between launches. It persists per-project state in [ntee-db](https://github.com/nickooan/ntee-db): the recently-opened file list, full-content undo snapshots (fingerprinted with SHA-256 content hashes), stashed drafts for files with unsaved edits, the session and open-tab list, and a cached search corpus so startup can skip a full file walk.

Everything is scoped per project. Each project root gets its own store directory under `~/.ntee-editor/stores/<hash(projectRoot)>/`, which matters because ntee-db uses a single-writer flock — with per-project stores, two editor instances only clash when they open the *same* project. When that happens, the app swaps in an in-memory fallback instead of failing: the `Backend` interface is the persistence surface the rest of the app depends on, and both the real `Store` and the `Memory` fallback satisfy it. With the fallback, undo still works for the session; nothing survives exit.

A workspace (an opened directory holding several git repos) keeps one store, even when Ctrl+W roots the editor inside one of those repos. The app then talks to `Scoped(store, repo)`, a view that maps the repo-relative paths the editor sees to the workspace-relative paths records are keyed by — so a file has one undo history and one draft however it was reached, and no second store (with its own flock) is ever opened.

One more key constraint: snapshot history is capped per file. The undo timeline could otherwise grow without bound, so the store leans on ntee-db's secondary-index eviction (`MaxPerValue` on the `file` index) to drop the oldest snapshots per file automatically.

**Architecture**

The main types are the records themselves: `OpenedFile` (recents, with cursor/scroll position), `Snapshot` (one undo checkpoint — path, sequence number, `"edit"`/`"save"` kind, full content plus its `Hash`), `Session` (what to restore on relaunch: the Ctrl+W repo, the workspace's own position, and each repo's in `Repos` as a `RepoSession`), `Draft` (unsaved edit state with its undo `Steps` carried inline so it is self-contained), `Tabs` (open-tab paths, active index, per-tab cursors), and `CorpusIndex` (cached search corpus plus a directory-mtime signature; `CorpusVersion` guards against format drift). `DBInfo` reports disk usage for the maintenance UI, and `ErrNoStats` marks a backend that has nothing on disk to inspect.

The key layout is a flat namespace with prefixes:

- `opened:<relpath>` — one record per opened file
- `versions:<seq>` — snapshots; `versionKey` zero-pads the sequence to 16 digits so keys sort in write order. These are the only *indexed* records: each `PutIndexed` tags the snapshot with its file path on the `file` index, and that index's `MaxPerValue` (set from config's `max_snapshots` at `Open`) evicts the oldest snapshots per file.
- `draft:<relpath>` — drafts deliberately use plain, non-indexed keys so index eviction can never delete a stashed draft.
- `session:current`, `tabs:current`, `corpus:current` — singletons; each save overwrites the last, so they self-cap at one record. `Session.WorkspaceRepo` is the Ctrl+W choice (`""` = the whole opened directory).
- `tabs:repo:<repo>`, `corpus:repo:<repo>` — the same singletons for each repo the editor has been rooted at (`SaveTabsFor`/`SaveCorpusFor` with a non-empty scope). Tab and index paths inside are repo-relative; everything else (opened, versions, drafts) stays under workspace paths.

**Functions**

### store.go

- `ContentHash` — the canonical SHA-256 fingerprint of snapshot content. The store and the editor's dedupe both use it, and they must agree for hash-only comparisons to work.
- `Dir` — maps a project root to its store directory (`~/.ntee-editor/stores/` + the first 16 hex chars of the root's SHA-256).
- `Open` — opens (creating if needed) the project's ntee-db store, declaring the `file` secondary index with `MaxPerValue: maxSnapshotsPerFile`. That one option is the whole snapshot-eviction story.
- `Maintenance` — gathers records, log bytes, live bytes, and blob usage into a `DBInfo`. `BlobUsage` does O(records) preads, so callers run this from a background goroutine, never during render.
- `RecentFiles` — prefix-scans `opened:` and sorts by `LastOpenedAt` in memory. It doesn't rely on index recency semantics because the record count is bounded by the project's file count and an in-memory sort is unambiguous.
- `DeleteOpenedUnder` — drops recent-visit records for a deleted file or directory. The prefix scan alone isn't enough — `lib` must not also match `library/…` — so it filters on an exact match or a `/` path boundary.
- `SnapshotPut` — writes one undo checkpoint via `PutIndexed`, computing and storing the content hash so later readers can compare without loading full contents.
- `LastSave` — walks the file's snapshots newest-first via the `file` index and returns the first `"save"`-kind one. This powers `:revert`.
- `SaveDraft` / `LoadDraft` / `DeleteDraft` — drafts on plain keys, on purpose: index eviction must never take out a stashed draft.
- `SaveCorpus` / `LoadCorpus` — the corpus is a singleton at a fixed key per scope; each save overwrites the last. A large index (≥64 KiB of JSON) auto-offloads to ntee-db's blob side-file. `SaveTabsFor`/`LoadTabsFor`/`SaveCorpusFor`/`LoadCorpusFor` take the scope explicitly (`""` = the project's own key, which the plain methods use); `scopedKey` picks the key.

*Plus small helpers and straight JSON put/get wrappers: `versionKey`, `Close`, `Compact`, `RelieveBlobs`, `TouchOpened`, `SnapshotGet`, `SnapshotDelete`, `SaveSession`/`LoadSession`, `SaveTabs`/`LoadTabs` — marshal, store, unmarshal, done.*

### memory.go

- `NewMemory` — builds the fallback `Backend` used when the flock is held: plain Go maps, nothing on disk.
- `Maintenance` / `Compact` / `RelieveBlobs` — return `ErrNoStats`; there's no on-disk store to inspect or compact.
- `LastSave` — linear scan of the snapshot map for the highest-`Seq` `"save"` record, mirroring the real store's answer.

*Plus map-backed mirrors of the rest of the interface: `Close`, `TouchOpened`, `RecentFiles`, `DeleteOpenedUnder`, `SnapshotPut`, `SnapshotGet`, `SnapshotDelete`, `SaveSession`/`LoadSession`, `SaveDraft`/`LoadDraft`/`DeleteDraft`, `SaveTabs`/`LoadTabs`, `SaveCorpus`/`LoadCorpus` and their `*For` scoped variants (tabs and corpus kept per scope) — same semantics as the real store, minus persistence.*

### scoped.go

- `Scoped` — returns the view of a store for a nested repo the editor is rooted at (`repo` "" returns the store itself). File-record methods prefix `repo/` on every path going in and strip it coming out: `TouchOpened`, `RecentFiles` (filtered to the repo, with the limit applied after filtering), `DeleteOpenedUnder`, `SnapshotPut`/`SnapshotGet`, `LastSave`, and the draft methods. Tabs and the corpus route to the repo's own keys. Session, maintenance, snapshot delete, and `Close` pass straight through: the session stays workspace-level (the app writes each root's position into it), and the view owns no resources.

*Plus: `wrap`/`unwrap` — the prefix add/strip.*
